// Package workflow implements the research pipeline: fetch -> extract -> research -> validate -> write.
//
// It wires together the fetcher, extractor, and researcher components to produce
// a validated dossier from user-provided URLs and a research topic.
package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/extractor"
	"github.com/Mundo-Dolphins/local-newsroom/internal/extractor/html"
	"github.com/Mundo-Dolphins/local-newsroom/internal/fetcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/llm"
	"github.com/Mundo-Dolphins/local-newsroom/internal/researcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
)

// Config holds configuration for the research workflow.
type Config struct {
	// Topic is the research topic (required).
	Topic string

	// URLs to fetch and research (at least one required).
	URLs []string

	// OutputPath is the path to write the dossier JSON.
	OutputPath string

	// FetcherConfig configures the HTTP fetcher.
	FetcherConfig fetcher.Config

	// ExtractorConfig configures the content extractor.
	ExtractorConfig extractor.Config

	// ResearcherConfig configures the Researcher.
	ResearcherConfig researcher.ClientConfig

	// LLMBaseURL is the base URL for the OpenAI-compatible LLM endpoint.
	// Can also be set via OMLX_BASE_URL environment variable.
	LLMBaseURL string

	// LLMModel is the model name to use.
	// Can also be set via OMLX_MODEL environment variable.
	LLMModel string

	// LLMAPIKey is the API key for LLM authentication.
	// Can also be set via OMLX_API_KEY environment variable.
	LLMAPIKey string

	// LLMTimeout is the timeout for LLM requests in seconds.
	LLMTimeout float64

	// FetchTimeout is the timeout for HTTP fetch requests in seconds.
	FetchTimeout float64
}

// Workflow performs the research pipeline.
type Workflow struct {
	config Config
}

// New creates a new Workflow with the given configuration.
func New(cfg Config) *Workflow {
	return &Workflow{config: cfg}
}

// Run executes the research pipeline.
//
// It performs these steps:
// 1. Fetch all URLs concurrently
// 2. Extract documents from successful fetches
// 3. Send normalized documents to the Researcher
// 4. Validate the generated dossier
// 5. Write the dossier to the output path
//
// Returns an error if any step fails. Invalid output (malformed JSON, invalid
// references, missing required fields) causes a clear non-zero exit.
func (w *Workflow) Run(ctx context.Context) error {
	// Validate inputs
	if w.config.Topic == "" {
		return fmt.Errorf("topic is required")
	}
	if len(w.config.URLs) == 0 {
		return fmt.Errorf("at least one URL is required")
	}

	// Step 1: Fetch all URLs
	fetchClient := fetcher.NewClient(w.config.FetcherConfig)
	fetchResults := fetchClient.FetchMany(ctx, w.config.URLs)

	// Collect successful fetches and report failures
	var successfulURLs []string
	var fetchErrors []FetchFailure
	for _, result := range fetchResults {
		if result.Success {
			successfulURLs = append(successfulURLs, result.Result.FinalURL)
		} else {
			fetchErrors = append(fetchErrors, FetchFailure{
				URL: result.URL,
				Err: result.Error,
			})
		}
	}

	// If all fetches failed, report and fail
	if len(fetchErrors) == len(fetchResults) {
		return &ResearchFailure{
			Type:          "fetch_failure",
			Message:       "All URL fetches failed",
			FetchFailures: fetchErrors,
		}
	}

	// Report partial fetch failures
	if len(fetchErrors) > 0 {
		fmt.Fprintf(os.Stderr, "Warning: %d of %d URLs failed to fetch:\n", len(fetchErrors), len(fetchResults))
		for _, failure := range fetchErrors {
			fmt.Fprintf(os.Stderr, "  %s: %s\n", failure.String(), failure.Err.Error())
		}
		fmt.Fprintf(os.Stderr, "Proceeding with %d successful fetches...\n", len(successfulURLs))
	}

	// Step 2: Extract documents from successful fetches
	extractorConfig := w.config.ExtractorConfig
	extractorConfig.Validate()
	hex := html.New(extractorConfig)

	var documents []types.Document
	var extractionErrors []ExtractionFailure
	for _, result := range fetchResults {
		if !result.Success {
			continue
		}

		doc, err := hex.Extract(extractor.Input{
			Source:  fetchResultToSource(result.Result),
			Content: result.Result.Body,
		})

		if err != nil {
			extractionErr := err.(*extractor.ExtractionError)
			extractionErrors = append(extractionErrors, ExtractionFailure{
				URL:      result.URL,
				FinalURL: result.Result.FinalURL,
				Err:      extractionErr,
			})
			continue
		}

		documents = append(documents, *doc)
	}

	// If all extractions failed, report and fail
	if len(documents) == 0 {
		return &ResearchFailure{
			Type:               "extraction_failure",
			Message:            "All extractions failed",
			FetchFailures:      fetchErrors,
			ExtractionFailures: extractionErrors,
		}
	}

	// Report partial extraction failures
	if len(extractionErrors) > 0 {
		fmt.Fprintf(os.Stderr, "Warning: %d of %d URLs failed extraction:\n", len(extractionErrors), len(successfulURLs))
		for _, failure := range extractionErrors {
			fmt.Fprintf(os.Stderr, "  %s: %s\n", failure.String(), failure.Err.Error())
		}
		fmt.Fprintf(os.Stderr, "Proceeding with %d successfully extracted documents...\n", len(documents))
	}

	// Step 3: Generate dossier using the Researcher
	// Build LLM client
	llmBaseURL := w.config.LLMBaseURL
	if llmBaseURL == "" {
		llmBaseURL = os.Getenv("OMLX_BASE_URL")
	}

	llmModel := w.config.LLMModel
	if llmModel == "" {
		llmModel = os.Getenv("OMLX_MODEL")
	}

	// If no model is configured, use a sensible default
	if llmModel == "" {
		llmModel = "llama3.1:8b" // Common default for oMLX
	}

	// Set LLM timeout
	llmTimeout := 0.0
	if w.config.LLMTimeout > 0 {
		llmTimeout = w.config.LLMTimeout
	}

	// If no base URL is configured, try localhost first
	if llmBaseURL == "" {
		// Check for OMLX_BASE_URL env var first
		if val := os.Getenv("OMLX_BASE_URL"); val != "" {
			llmBaseURL = val
		}
	}

	// Get API key from flag or environment
	llmAPIKey := w.config.LLMAPIKey
	if llmAPIKey == "" {
		llmAPIKey = os.Getenv("OMLX_API_KEY")
	}

	// Set up LLM client
	var llmClient llm.Client
	if llmBaseURL != "" {
		llmClient = llm.NewClient(llm.Config{
			BaseURL: llmBaseURL,
			Model:   llmModel,
			APIKey:  llmAPIKey,
			Timeout: defaultDuration(llmTimeout),
		})
	} else {
		// Return error if no LLM endpoint is configured
		return &ResearchFailure{
			Type:    "llm_config",
			Message: "No LLM endpoint configured. Set OMLX_BASE_URL environment variable or use --llm-base-url flag",
		}
	}

	resConfig := w.config.ResearcherConfig
	resConfig.TimeNow = nil // Will be set by New if not already
	r, err := researcher.New(llmClient, llmModel, resConfig)
	if err != nil {
		return &ResearchFailure{
			Type:    "researcher_init",
			Message: fmt.Sprintf("failed to initialize researcher: %v", err),
		}
	}

	dossier, err := r.Generate(ctx, w.config.Topic, documents)
	if err != nil {
		return &ResearchFailure{
			Type:    "researcher_error",
			Message: fmt.Sprintf("research failed: %v", err),
		}
	}

	// Step 4: Validate the dossier
	if err := dossier.Validate(); err != nil {
		return &ResearchFailure{
			Type:    "dossier_validation",
			Message: fmt.Sprintf("dossier validation failed: %v", err),
		}
	}

	// Step 5: Write the dossier to output path
	if err := writeDossier(w.config.OutputPath, dossier); err != nil {
		return &ResearchFailure{
			Type:    "output_error",
			Message: fmt.Sprintf("failed to write dossier: %v", err),
		}
	}

	if _, err := fmt.Fprintf(os.Stdout, "Dossier written to %s\n", w.config.OutputPath); err != nil {
		return err
	}
	return nil
}

// FetchFailure represents a failed URL fetch.
type FetchFailure struct {
	URL      string
	FinalURL string
	Err      *fetcher.FetchError
}

func (f FetchFailure) String() string {
	if f.URL != "" {
		return f.URL
	}
	return f.FinalURL
}

// ExtractionFailure represents a failed document extraction.
type ExtractionFailure struct {
	URL      string
	FinalURL string
	Err      *extractor.ExtractionError
}

func (e ExtractionFailure) String() string {
	if e.URL != "" {
		return e.URL
	}
	return e.FinalURL
}

// ResearchFailure is a structured error for research pipeline failures.
type ResearchFailure struct {
	Type               string
	Message            string
	FetchFailures      []FetchFailure
	ExtractionFailures []ExtractionFailure
}

func (r *ResearchFailure) Error() string {
	if r.Message != "" {
		return r.Message
	}
	return "research failed"
}

// IsFailFast returns true if the failure type indicates the entire pipeline should stop.
func (r *ResearchFailure) IsFailFast() bool {
	switch r.Type {
	case "llm_config", "researcher_init", "researcher_error", "dossier_validation":
		return true
	default:
		return false
	}
}

// writeDossier marshals the dossier to JSON and writes it to the output path.
func writeDossier(outputPath string, dossier *researcher.ResearchDossier) error {
	jsonBytes, err := json.MarshalIndent(dossier, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal dossier: %w", err)
	}

	// Write atomically: write to temp file, then rename
	tempPath := outputPath + ".tmp." + fmt.Sprintf("%d", os.Getpid())
	if err := os.WriteFile(tempPath, jsonBytes, 0644); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tempPath, outputPath); err != nil {
		// Clean up temp file on failure
		_ = os.Remove(tempPath)
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}

// fetchResultToSource converts a fetcher.FetchResult to a types.Source.
func fetchResultToSource(result *fetcher.FetchResult) types.Source {
	source := types.Source{
		StableID:    fetchResultStableID(result),
		OriginalURL: result.FinalURL,
		SourceType:  types.SourceTypeWeb,
		RetrievedAt: result.RetrievedAt,
		FetchStatus: fetcher.FetchStatusFromResult(result),
		Metadata:    fetchResultMetadata(result),
	}
	return source
}

// fetchResultStableID generates a deterministic stable ID for a fetch result.
func fetchResultStableID(result *fetcher.FetchResult) string {
	// Use the final URL and retrieved time to create a stable ID
	// In production, you might want to use a hash of the URL + timestamp
	now := result.RetrievedAt.Format("2006-01-02T15:04:05Z")
	return strings.ReplaceAll(result.FinalURL, ":", "-") + "-" + now
}

// fetchResultMetadata extracts metadata from the fetch result.
func fetchResultMetadata(result *fetcher.FetchResult) map[string]string {
	meta := make(map[string]string)

	if result.ContentType != "" {
		meta["content_type"] = result.ContentType
	}

	if result.ContentLength > 0 {
		meta["content_length"] = fmt.Sprintf("%d", result.ContentLength)
	}

	if result.HTTPStatus > 0 {
		meta["http_status"] = fmt.Sprintf("%d", result.HTTPStatus)
	}

	// Include relevant headers
	headerKeys := []string{"x-request-id", "server", "date"}
	for _, key := range headerKeys {
		if val := result.Headers.Get(key); val != "" {
			meta[key] = val
		}
	}

	return meta
}

// defaultDuration converts seconds to time.Duration, defaulting to 60 seconds.
func defaultDuration(seconds float64) time.Duration {
	if seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return 60 * time.Second // 60 seconds
}
