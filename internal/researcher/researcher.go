// Package researcher implements the Researcher stage: topic + normalized documents -> validated research dossier.
//
// The Researcher:
// - Uses a prompt template stored in prompts/researcher.prompt
// - Depends on the generic llm.Client interface
// - Parses and validates model responses before accepting them
// - Returns errors when model output cannot be validated
// - Is unit-testable with a fake LLM client
package researcher

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/llm"
	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
)

//go:embed prompts/*.prompt
var researcherPromptsFS embed.FS

// Researcher produces a ResearchDossier from a topic and source documents.
//
// It uses an llm.Client to generate the dossier by prompting the model to:
// - Extract atomic factual claims from the provided documents
// - Attach claims to source IDs with excerpts
// - Record uncertainty and contradictions
// - Distinguish sourced facts from inference
// - Avoid article prose and editorial voice
type Researcher struct {
	client llm.Client
	model  string
	config ClientConfig
	tmpl   *template.Template
	prompt string
}

// ClientConfig holds configuration for the Researcher.
type ClientConfig struct {
	// Temperature controls randomness in generation (0.0 = deterministic, higher = more creative)
	// Recommended: 0.3-0.5 for factual extraction
	Temperature float64

	// MaxOutputTokens limits the response size.
	// A research dossier with 10-20 claims typically needs 8k-20k tokens.
	MaxOutputTokens int

	// PromptOverride allows customizing the system prompt for testing or special cases.
	// If empty, uses the researcher.prompt file from prompts/researcher.prompt.
	PromptOverride string

	// TimeNow is used to get the current time for generated_at timestamps.
	// For testing, this can be overridden to return a fixed time.
	TimeNow func() time.Time
}

// New creates a new Researcher with the given LLM client.
//
// The prompt template is loaded from prompts/researcher.prompt via go:embed
// unless PromptOverride is specified in config.
func New(client llm.Client, model string, config ClientConfig) (*Researcher, error) {
	if client == nil {
		return nil, errors.New("llm.Client must not be nil")
	}

	// Load the prompt template
	prompt := config.PromptOverride
	if prompt == "" {
		// Read prompt from embedded filesystem
		promptBytes, err := researcherPromptsFS.ReadFile("prompts/researcher.prompt")
		if err != nil {
			return nil, fmt.Errorf("failed to read prompt template: %w", err)
		}
		prompt = string(promptBytes)
	}

	// Parse the template
	tmpl, err := template.New("researcher").Parse(prompt)
	if err != nil {
		return nil, fmt.Errorf("failed to parse prompt template: %w", err)
	}

	// Set default time function
	if config.TimeNow == nil {
		config.TimeNow = time.Now
	}

	return &Researcher{
		client: client,
		model:  model,
		config: config,
		tmpl:   tmpl,
		prompt: prompt,
	}, nil
}

// Generate produces a validated ResearchDossier from a topic and source documents.
// The prompt template is provided by the embedded prompts/researcher.prompt file.
//
// Parameters:
//   - ctx: Context for cancellation and timeouts
//   - topic: The research topic to investigate
//   - documents: Normalized documents to extract facts from
//
// Returns:
//   - *ResearchDossier: The validated research findings
//   - error: If generation or validation fails
//
// The Researcher will:
//  1. Build a prompt with the topic and all document contents
//  2. Send the prompt to the LLM client
//  3. Parse and validate the JSON response
//  4. Validate the dossier structure and references
//  5. Return the validated dossier
func (r *Researcher) Generate(ctx context.Context, topic string, documents []types.Document) (*ResearchDossier, error) {
	if topic == "" {
		return nil, errors.New("topic cannot be empty")
	}

	if len(documents) == 0 {
		return nil, errors.New("no documents provided for research")
	}

	// Build the user prompt with topic and documents
	userPrompt := buildUserPrompt(topic, documents)

	// Create the LLM request
	req := llm.Request{
		SystemPrompt:    r.prompt,
		UserPrompt:      userPrompt,
		Model:           r.model,
		Temperature:     r.config.Temperature,
		MaxOutputTokens: r.config.MaxOutputTokens,
	}

	// Get the LLM response
	resp, err := r.client.Complete(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("llm completion failed: %w", err)
	}

	// Parse and validate the response
	dossier, err := parseAndValidateResponse(resp.Content)
	if err != nil {
		return nil, fmt.Errorf("failed to parse or validate dossier: %w", err)
	}

	// Validate the dossier references
	if err := dossier.Validate(); err != nil {
		return nil, fmt.Errorf("dossier validation failed: %w", err)
	}

	return dossier, nil
}

// buildUserPrompt constructs the user prompt from topic and documents.
func buildUserPrompt(topic string, documents []types.Document) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "Research topic: %s\n\n", topic)
	sb.WriteString("=== DOCUMENTS ===\n\n")

	for i, doc := range documents {
		fmt.Fprintf(&sb, "Document %d:\n", i+1)
		fmt.Fprintf(&sb, "Source ID: %s\n", doc.SourceID)

		if doc.CanonicalURL != nil {
			fmt.Fprintf(&sb, "Canonical URL: %s\n", *doc.CanonicalURL)
		}

		if doc.Title != nil {
			fmt.Fprintf(&sb, "Title: %s\n", *doc.Title)
		}

		if doc.Author != nil {
			fmt.Fprintf(&sb, "Author: %s\n", *doc.Author)
		}

		if doc.PublishedAt != nil {
			fmt.Fprintf(&sb, "Published: %s\n", doc.PublishedAt.Format(time.RFC3339))
		}

		fmt.Fprintf(&sb, "Plain Text:\n%s\n", doc.PlainText)
		fmt.Fprintf(&sb, "=== END DOCUMENT %d ===\n\n", i+1)
	}

	sb.WriteString("=== END DOCUMENTS ===\n\n")
	sb.WriteString("Generate a research dossier JSON following the format in your instructions.\n")

	return sb.String()
}

// parseAndValidateResponse parses the LLM response and performs initial validation.
func parseAndValidateResponse(content string) (*ResearchDossier, error) {
	if content == "" {
		return nil, errors.New("empty model response")
	}

	// Trim whitespace and remove markdown code blocks if present
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```JSON")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	if content == "" {
		return nil, errors.New("model response contained only whitespace or markdown formatting")
	}

	// Parse JSON
	var dossier ResearchDossier
	if err := json.Unmarshal([]byte(content), &dossier); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	// Validate required top-level fields
	if dossier.StableID == "" {
		return nil, errors.New("missing required field: stable_id")
	}

	if dossier.Topic == "" {
		return nil, errors.New("missing required field: topic")
	}

	if len(dossier.Claims) == 0 {
		return nil, errors.New("missing required field: claims (must contain at least one claim)")
	}

	// Validate timestamps are valid
	if err := validateTimestamps(&dossier); err != nil {
		return nil, fmt.Errorf("invalid timestamps: %w", err)
	}

	return &dossier, nil
}

// validateTimestamps checks that all timestamps are valid ISO 8601 datetime strings.
func validateTimestamps(d *ResearchDossier) error {
	// Check generated_at
	if d.GeneratedAt.IsZero() {
		return errors.New("generated_at timestamp is zero value")
	}

	// Check source timestamps
	for i, s := range d.Sources {
		if s.RetrievedAt.IsZero() {
			return fmt.Errorf("source[%d].retrieved_at is zero value", i)
		}
		if s.PublishedAt != nil && s.PublishedAt.IsZero() {
			return fmt.Errorf("source[%d].published_at is zero value", i)
		}
	}

	// Check research note timestamps
	for i, n := range d.ResearchNotes {
		if n.CreatedAt.IsZero() {
			return fmt.Errorf("research_note[%d].created_at is zero value", i)
		}
	}

	return nil
}
