// Package planner implements a search query planner that converts a research topic
// into a structured set of web-search queries.
//
// The planner:
// - Uses a prompt template stored in prompts/search-planner.prompt
// - Depends on the generic llm.Client interface
// - Parses and validates model responses before accepting them
// - Returns errors when model output cannot be validated
// - Is unit-testable with a fake LLM client
//
// This package is intentionally separate from the researcher because query
// planning happens before any source documents exist.
package planner

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"

	"strings"
)

//go:embed prompts/*.prompt
var promptsFS embed.FS

// Planner produces a SearchPlan from a research topic and optional hints.
//
// It uses an llm.Client to generate search queries by prompting the model to:
// - Generate targeted search queries (not article prose)
// - Include core topic entities and concepts
// - Use complementary query angles (definitions, primary sources, data, etc.)
// - Prefer queries likely to surface authoritative sources
// - Return machine-parseable JSON
type Planner struct {
	client Client
	model  string
	config Config
	prompt string
}

// Client is the interface for chat completion with an LLM.
//
// This is a local alias of llm.Client to avoid circular dependencies
// between planner and llm packages during imports.
type Client interface {
	Complete(ctx context.Context, req Request) (Response, error)
}

// Request represents a chat completion request.
type Request struct {
	SystemPrompt    string
	UserPrompt      string
	Model           string
	Temperature     float64
	MaxOutputTokens int
}

// Response represents a chat completion response.
type Response struct {
	Content string
}

// New creates a new Planner with the given LLM client.
//
// The prompt template is loaded from prompts/search-planner.prompt via file read
// unless PromptOverride is specified in config.
func New(client Client, model string, config Config) (*Planner, error) {
	if client == nil {
		return nil, errors.New("llm.Client must not be nil")
	}

	// Use defaults for unspecified config values
	if config.MinQueries == 0 {
		config.MinQueries = 1
	}
	if config.MaxQueries == 0 {
		config.MaxQueries = 5
	}
	if config.Temperature == 0 {
		config.Temperature = 0.5
	}
	if config.MaxOutputTokens == 0 {
		config.MaxOutputTokens = 4000
	}

	// Validate config bounds (only if explicitly set to invalid values)
	if config.MinQueries < 1 {
		return nil, fmt.Errorf("MinQueries must be between 1 and 10, got %d", config.MinQueries)
	}
	if config.MaxQueries < 1 || config.MaxQueries > 10 {
		return nil, fmt.Errorf("MaxQueries must be between 1 and 10, got %d", config.MaxQueries)
	}
	if config.MinQueries > config.MaxQueries {
		return nil, fmt.Errorf("MinQueries (%d) cannot exceed MaxQueries (%d)", config.MinQueries, config.MaxQueries)
	}

	// Load the prompt template
	prompt := config.PromptOverride
	if prompt == "" {
		// Read prompt from embedded filesystem
		promptBytes, err := promptsFS.ReadFile("prompts/search-planner.prompt")
		if err != nil {
			return nil, fmt.Errorf("failed to read prompt template: %w", err)
		}
		prompt = string(promptBytes)
	}

	return &Planner{
		client: client,
		model:  model,
		config: config,
		prompt: prompt,
	}, nil
}

// Generate produces a validated SearchPlan from a research topic and optional hints.
//
// Parameters:
//   - ctx: Context for cancellation and timeouts
//   - topic: The research topic to plan queries for
//   - language: Optional 2-letter ISO language code (e.g., "en", "es", "fr")
//   - timeRangeHint: Optional time-range hint for search recency
//
// Returns:
//   - *SearchPlan: The validated search plan
//   - error: If generation or validation fails
//
// The Planner will:
//  1. Build a prompt with the topic and hints
//  2. Send the prompt to the LLM client
//  3. Parse and validate the JSON response
//  4. Validate the plan structure and query bounds
//  5. Deduplicate normalized duplicate queries
//  6. Return the validated plan
func (p *Planner) Generate(ctx context.Context, topic string, language string, timeRangeHint *TimeRangeHint) (*SearchPlan, error) {
	if strings.TrimSpace(topic) == "" {
		return nil, ErrEmptyTopic
	}

	// Build the user prompt with topic and hints
	userPrompt := buildUserPrompt(topic, language, timeRangeHint)

	// Create the LLM request
	req := Request{
		SystemPrompt:    p.prompt,
		UserPrompt:      userPrompt,
		Model:           p.model,
		Temperature:     p.config.Temperature,
		MaxOutputTokens: p.config.MaxOutputTokens,
	}

	// Get the LLM response
	resp, err := p.client.Complete(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("llm completion failed: %w", err)
	}

	// Parse and validate the response
	plan, err := parseAndValidateResponse(resp.Content, p.config)
	if err != nil {
		return nil, fmt.Errorf("failed to parse or validate plan: %w", err)
	}

	return plan, nil
}

// buildUserPrompt constructs the user prompt from topic and hints.
func buildUserPrompt(topic string, language string, timeRangeHint *TimeRangeHint) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "topic: %s\n", topic)

	if language != "" {
		fmt.Fprintf(&sb, "language: %s\n", language)
	}

	if timeRangeHint != nil {
		var rangeParts []string
		if timeRangeHint.StartDate != "" {
			rangeParts = append(rangeParts, timeRangeHint.StartDate)
		}
		if timeRangeHint.EndDate != "" {
			rangeParts = append(rangeParts, timeRangeHint.EndDate)
		}
		if len(rangeParts) > 0 {
			fmt.Fprintf(&sb, "time_range: %s\n", strings.Join(rangeParts, " to "))
		}
	}

	sb.WriteString("\nGenerate the search plan JSON as instructed.\n")

	return sb.String()
}

// parseAndValidateResponse parses the LLM response and performs validation.
func parseAndValidateResponse(content string, config Config) (*SearchPlan, error) {
	if content == "" {
		return nil, errors.New("empty model response")
	}

	// Trim whitespace and remove markdown code blocks if present
	content = strings.TrimSpace(content)
	// Remove opening markdown fence
	if strings.HasPrefix(content, "```json") || strings.HasPrefix(content, "```JSON") {
		content = strings.TrimPrefix(content, "```json")
		content = strings.TrimPrefix(content, "```JSON")
	} else if strings.HasPrefix(content, "```") {
		content = strings.TrimPrefix(content, "```")
	}
	// Remove closing markdown fence
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	if content == "" {
		return nil, errors.New("model response contained only whitespace or markdown formatting")
	}

	// Parse JSON
	var plan SearchPlan
	if err := json.Unmarshal([]byte(content), &plan); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidJSON, err)
	}

	// Validate required top-level fields
	if strings.TrimSpace(plan.OriginalTopic) == "" {
		return nil, ErrEmptyTopic
	}

	if len(plan.Queries) == 0 {
		return nil, ErrEmptyQueries
	}

	// Validate each query
	for i, q := range plan.Queries {
		if strings.TrimSpace(q.Query) == "" {
			return nil, fmt.Errorf("%w at index %d", ErrQueryEmpty, i)
		}
		if strings.TrimSpace(q.Purpose) == "" {
			return nil, fmt.Errorf("%w at index %d", ErrPurposeEmpty, i)
		}
	}

	// Validate time range hints if present
	if plan.TimeRangeHint != nil {
		if err := plan.TimeRangeHint.ValidateStartTime(); err != nil {
			return nil, fmt.Errorf("invalid time_range_hint.start_date: %w", err)
		}
		if err := plan.TimeRangeHint.ValidateEndTime(); err != nil {
			return nil, fmt.Errorf("invalid time_range_hint.end_date: %w", err)
		}
	}

	for i, q := range plan.Queries {
		if q.TimeRange != nil {
			if err := q.TimeRange.ValidateStartTime(); err != nil {
				return nil, fmt.Errorf("invalid queries[%d].time_range.start_date: %w", i, err)
			}
			if err := q.TimeRange.ValidateEndTime(); err != nil {
				return nil, fmt.Errorf("invalid queries[%d].time_range.end_date: %w", i, err)
			}
		}
	}

	// Validate against config bounds
	if len(plan.Queries) < config.MinQueries {
		return nil, fmt.Errorf("%w: got %d, minimum is %d", ErrTooFewQueries, len(plan.Queries), config.MinQueries)
	}
	if len(plan.Queries) > config.MaxQueries {
		return nil, fmt.Errorf("%w: got %d, maximum is %d", ErrTooManyQueries, len(plan.Queries), config.MaxQueries)
	}

	// Deduplicate queries based on normalized form
	plan.Queries = deduplicateQueries(plan.Queries)

	// Re-validate after deduplication
	if len(plan.Queries) < config.MinQueries {
		return nil, fmt.Errorf("%w after deduplication: got %d, minimum is %d", ErrTooFewQueries, len(plan.Queries), config.MinQueries)
	}
	if len(plan.Queries) > config.MaxQueries {
		return nil, fmt.Errorf("%w after deduplication: got %d, maximum is %d", ErrTooManyQueries, len(plan.Queries), config.MaxQueries)
	}

	// Validate the final plan
	if err := plan.Validate(); err != nil {
		return nil, fmt.Errorf("plan validation failed: %w", err)
	}

	return &plan, nil
}
