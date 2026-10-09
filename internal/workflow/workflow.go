// Package workflow implements the research pipeline: fetch -> extract -> research -> validate -> write.
//
// It wires together the fetcher, extractor, and researcher components to produce
// a validated dossier from user-provided URLs and a research topic.
package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/archiverev"
	"github.com/Mundo-Dolphins/local-newsroom/internal/discovery"
	"github.com/Mundo-Dolphins/local-newsroom/internal/extractor"
	"github.com/Mundo-Dolphins/local-newsroom/internal/extractor/html"
	"github.com/Mundo-Dolphins/local-newsroom/internal/fetcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/llm"
	"github.com/Mundo-Dolphins/local-newsroom/internal/planner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/researcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/search"
	"github.com/Mundo-Dolphins/local-newsroom/internal/search/searxng"
	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
)

// Config holds configuration for the research workflow.
type Config struct {
	// Topic is the research topic (required).
	Topic string

	// URLs to fetch and research.
	// When AutoDiscover is false (default), at least one URL is required.
	// When AutoDiscover is true and URLs is empty, URLs will be discovered.
	// When AutoDiscover is true and URLs is provided, supplement mode is enabled.
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

	// AutoDiscover enables automatic source discovery from the topic.
	// When true and URLs is empty, discovery is performed before fetching.
	// When true and URLs is provided, supplement mode merges discovered URLs.
	// When false (default), requires explicit URLs.
	AutoDiscover bool

	// DiscoveryConfig configures the source discovery process.
	// Only used when AutoDiscover is true.
	DiscoveryConfig discovery.Config

	// PlannersPromptOverride is an optional prompt override for the Search Query Planner.
	// If empty, uses the search-planner.prompt file from prompts/search-planner.prompt.
	PlannerPromptOverride string

	// DiscoveryService is an optional discovery service instance.
	// If nil, a new Discovery instance is created with DiscoveryConfig.
	// This allows dependency injection for testing.
	DiscoveryService discoveryService

	// SupplementMode enables supplement mode when AutoDiscover is true.
	// In supplement mode, explicit URLs are fetched first, then discovered
	// URLs are appended (with deduplication). When false, discovered URLs
	// only (no explicit URLs) are used.
	SupplementMode bool

	// SearchBaseURL is the base URL for the SearXNG search instance.
	// Required when AutoDiscover is true. Can also be set via SEARXNG_BASE_URL.
	SearchBaseURL string

	// SearchLanguage is the preferred language for search results.
	// RFC 5646 language tag (e.g., "en", "en-US", "es").
	// Can also be set via SEARXNG_SEARCH_LANGUAGE.
	SearchLanguage string

	// SearchTimeRange specifies time range for search results.
	// SearXNG-specific values like "last_week", "last_month", "last_year".
	// Can also be set via SEARXNG_SEARCH_TIME_RANGE.
	SearchTimeRange string

	// SearchAPIHeader is the header name for SearXNG API key.
	// Can also be set via SEARXNG_API_HEADER.
	SearchAPIKey string

	// SearchAPIHeader is the header name for SearXNG API key.
	// Can also be set via SEARXNG_API_HEADER.
	SearchAPIHeader string

	// ArchiveConfig enables optional archive retrieval to supplement
	// live web sources with historical archive chunks.
	// When enabled, the workflow will query the archive for relevant
	// chunks based on the research topic.
	ArchiveConfig archiverev.Config
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
// 1. If AutoDiscover is enabled and no URLs provided, discover source candidates
// 2. If SupplementMode is enabled, merge explicit URLs with discovered candidates
// 3. Fetch all URLs concurrently
// 4. Extract documents from successful fetches
// 5. Send normalized documents to the Researcher
// 6. Validate the generated dossier
// 7. Write the dossier to the output path
//
// Returns an error if any step fails. Invalid output (malformed JSON, invalid
// references, missing required fields) causes a clear non-zero exit.
func (w *Workflow) Run(ctx context.Context) error {
	// Validate inputs
	if w.config.Topic == "" {
		return fmt.Errorf("topic is required")
	}

	// Determine the final URLs to fetch
	var finalURLs []string

	if w.config.AutoDiscover {
		// Discovery mode: either discover only or supplement mode
		if w.config.SupplementMode && len(w.config.URLs) > 0 {
			// Supplement mode: fetch explicit URLs first, then discovered
			// Step 1a: Fetch explicit URLs
			fetchClient := fetcher.NewClient(w.config.FetcherConfig)
			explicitFetchResults := fetchClient.FetchMany(ctx, w.config.URLs)

			// Collect successful fetches from explicit URLs
			var explicitSuccesses []fetcher.FetchItem
			var fetchErrors []fetchFailure
			for _, result := range explicitFetchResults {
				if result.Success {
					explicitSuccesses = append(explicitSuccesses, result)
				} else {
					fetchErrors = append(fetchErrors, fetchFailure{
						URL: result.URL,
						Err: result.Error,
					})
				}
			}

			// If all explicit URL fetches failed, fail clearly
			if len(explicitSuccesses) == 0 && len(fetchErrors) > 0 {
				return &ResearchFailure{
					Type:          "fetch_failure",
					Message:       "All explicit URL fetches failed",
					FetchFailures: convertToFetchFailures(fetchErrors),
				}
			}

			// Report partial failures
			if len(fetchErrors) > 0 {
				fmt.Fprintf(os.Stderr, "Warning: %d of %d explicit URLs failed to fetch:\n", len(fetchErrors), len(w.config.URLs))
				for _, failure := range convertToFetchFailures(fetchErrors) {
					fmt.Fprintf(os.Stderr, "  %s: %s\n", failure.String(), failure.Err.Error())
				}
				fmt.Fprintf(os.Stderr, "Proceeding with %d successful explicit fetches and discovery...\n", len(explicitSuccesses))
			}

			// Collect explicit URLs for deduplication
			explicitURLs := make([]string, 0, len(explicitSuccesses))
			for _, ex := range explicitSuccesses {
				explicitURLs = append(explicitURLs, ex.Result.FinalURL)
			}

			// Step 1b: Discover additional sources
			discService, discErr := w.getDiscoveryService()
			if discErr != nil {
				// Discovery service not configured - fail with clear error
				return &ResearchFailure{
					Type:    "config_error",
					Message: "discovery service not configured: " + discErr.Error(),
				}
			}
			candidates, err := discService.Discover(ctx, w.config.Topic, "", nil)
			if err != nil {
				// Discovery failure in supplement mode - continue with explicit URLs
				fmt.Fprintf(os.Stderr, "Warning: discovery failed: %v. Proceeding with explicit URLs only.\n", err)
				finalURLs = explicitURLs
			} else if len(candidates.Candidates) == 0 {
				// No candidates discovered - continue with explicit URLs
				fmt.Fprintf(os.Stderr, "Warning: no candidates discovered. Proceeding with explicit URLs only.\n")
				finalURLs = explicitURLs
			} else {
				// Merge explicit and discovered URLs with deduplication
				finalURLs = mergeURLs(explicitURLs, extractCandidateURLs(candidates.Candidates))
			}

			// Continue to extraction with explicit success results
			return w.continueFromFetches(ctx, explicitSuccesses, finalURLs)
		} else if len(w.config.URLs) == 0 {
			// Discovery only: discover URLs first
			// Step 1: Discover sources
			discService, discErr := w.getDiscoveryService()
			if discErr != nil {
				// Discovery service not configured - fail with clear error
				return &ResearchFailure{
					Type:    "config_error",
					Message: "discovery service not configured: " + discErr.Error(),
				}
			}
			candidates, err := discService.Discover(ctx, w.config.Topic, "", nil)
			if err != nil {
				// Discovery failure - return structured error
				var de *discovery.DiscoveryError
				if discovery.IsDiscoveryError(err) != nil {
					de = discovery.IsDiscoveryError(err)
					return &ResearchFailure{
						Type:              "discovery_failure",
						Message:           de.Message,
						DiscoveryQueryErr: de.QueryResults,
					}
				}
				return &ResearchFailure{
					Type:    "discovery_failure",
					Message: "source discovery failed: " + err.Error(),
				}
			}

			if len(candidates.Candidates) == 0 {
				return &ResearchFailure{
					Type:    "discovery_failure",
					Message: "no source candidates discovered",
				}
			}

			// Extract URLs from candidates
			finalURLs = extractCandidateURLs(candidates.Candidates)

			// Step 2: Fetch discovered URLs
			return w.continueFromFetches(ctx, nil, finalURLs)
		} else {
			// AutoDiscover true but URLs provided - this is not a valid combination
			// unless SupplementMode is enabled
			return &ResearchFailure{
				Type:    "config_error",
				Message: "AutoDiscover=true with URLs provided requires SupplementMode=true",
			}
		}
	} else {
		// Explicit mode (v0.1): URLs must be provided
		if len(w.config.URLs) == 0 {
			return fmt.Errorf("at least one URL is required")
		}

		// Step 1: Fetch all URLs
		fetchClient := fetcher.NewClient(w.config.FetcherConfig)
		fetchResults := fetchClient.FetchMany(ctx, w.config.URLs)

		// Continue to extraction with explicit URLs
		return w.continueFromFetches(ctx, fetchResults, w.config.URLs)
	}
}

// continueFromFetches continues the pipeline from fetch results, supporting
// both explicit fetches (when SupplementMode is enabled) and discovered URLs.
func (w *Workflow) continueFromFetches(ctx context.Context, explicitSuccesses []fetcher.FetchItem, finalURLs []string) error {
	var fetchResults []fetcher.FetchItem

	if explicitSuccesses != nil {
		// In supplement mode, we already have successful fetches
		fetchResults = explicitSuccesses
	} else {
		// Fetch discovered URLs
		fetchClient := fetcher.NewClient(w.config.FetcherConfig)
		fetchResults = fetchClient.FetchMany(ctx, finalURLs)
	}

	// Collect successful fetches and report failures
	var successfulURLs []string
	var fetchErrors []fetchFailure
	for _, result := range fetchResults {
		if result.Success {
			successfulURLs = append(successfulURLs, result.Result.FinalURL)
		} else {
			fetchErrors = append(fetchErrors, fetchFailure{
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
			FetchFailures: convertToFetchFailures(fetchErrors),
		}
	}

	// Report partial fetch failures
	if len(fetchErrors) > 0 {
		fmt.Fprintf(os.Stderr, "Warning: %d of %d URLs failed to fetch:\n", len(fetchErrors), len(fetchResults))
		for _, failure := range convertToFetchFailures(fetchErrors) {
			fmt.Fprintf(os.Stderr, "  %s: %s\n", failure.String(), failure.Err.Error())
		}
		fmt.Fprintf(os.Stderr, "Proceeding with %d successful fetches...\n", len(successfulURLs))
	}

	// Step: Extract documents from successful fetches
	extractorConfig := w.config.ExtractorConfig
	extractorConfig.Validate()
	hex := html.New(extractorConfig)

	var documents []types.Document
	var extractionErrors []extractionFailure
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
			extractionErrors = append(extractionErrors, extractionFailure{
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
			FetchFailures:      convertToFetchFailures(fetchErrors),
			ExtractionFailures: convertToExtractionFailures(extractionErrors),
		}
	}

	// Report partial extraction failures
	if len(extractionErrors) > 0 {
		fmt.Fprintf(os.Stderr, "Warning: %d of %d URLs failed extraction:\n", len(extractionErrors), len(successfulURLs))
		for _, failure := range convertToExtractionFailures(extractionErrors) {
			fmt.Fprintf(os.Stderr, "  %s: %s\n", failure.String(), failure.Err.Error())
		}
		fmt.Fprintf(os.Stderr, "Proceeding with %d successfully extracted documents...\n", len(documents))
	}

	// Step: Retrieve archive chunks to supplement live sources
	var allDocuments []types.Document
	var archiveResult *archiverev.RetrieveResult
	var archiveErr error

	if w.config.ArchiveConfig.ArchiveStore != nil {
		// Perform archive retrieval
		archiveClient := archiverev.New(w.config.ArchiveConfig)
		archiveResult, archiveErr = archiveClient.Retrieve(ctx, w.config.Topic, w.config.URLs)
		if archiveErr != nil {
			// Archive retrieval failure - warn but continue if we have live sources
			// This is not a fail-fast error
			fmt.Fprintf(os.Stderr, "Warning: archive retrieval failed: %v. Proceeding with live sources only.\n", archiveErr)
			archiveResult = nil
		}
	}

	// Combine live and archive documents
	if archiveResult != nil && len(archiveResult.Documents) > 0 {
		// Prepend archive documents to live documents
		// This gives them prominence in the prompt
		allDocuments = append(archiveResult.Documents, documents...)
		fmt.Fprintf(os.Stderr, "Supplementing with %d archive document(s) from %d source(s).\n",
			len(archiveResult.Documents), len(archiveResult.SourceIDs))
	}
	if archiveResult == nil || len(archiveResult.Documents) == 0 {
		// No archive supplementation
		allDocuments = documents
	}

	// Step: Generate dossier using the Researcher
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

	dossier, err := r.Generate(ctx, w.config.Topic, allDocuments)
	if err != nil {
		return &ResearchFailure{
			Type:    "researcher_error",
			Message: fmt.Sprintf("research failed: %v", err),
		}
	}

	// Step: Validate the dossier
	if err := dossier.Validate(); err != nil {
		return &ResearchFailure{
			Type:    "dossier_validation",
			Message: fmt.Sprintf("dossier validation failed: %v", err),
		}
	}

	// Step: Write the dossier to output path
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

// getDiscoveryService returns the configured discovery service.
// When AutoDiscover is enabled, it constructs a discovery.Discovery instance
// from DiscoveryConfig if DiscoveryService is not explicitly set.
func (w *Workflow) getDiscoveryService() (discoveryService, error) {
	if w.config.DiscoveryService != nil {
		return w.config.DiscoveryService, nil
	}

	// Construct discovery service from DiscoveryConfig
	if w.config.DiscoveryConfig.MaxSearchQueries == 0 {
		w.config.DiscoveryConfig = discovery.DefaultConfig()
	}

	// Build SearXNG provider from discovery config
	// The discovery.Config should contain search provider info
	discService, err := w.buildDiscoveryServiceFromConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to build discovery service: %w", err)
	}

	return discService, nil
}

// buildDiscoveryServiceFromConfig constructs a discovery.Discovery from the config.
func (w *Workflow) buildDiscoveryServiceFromConfig() (*discovery.Discovery, error) {
	// Build SearXNG provider with config precedence: CLI flag -> env var -> default
	searchBaseURL := w.config.SearchBaseURL
	if searchBaseURL == "" {
		searchBaseURL = os.Getenv("SEARXNG_BASE_URL")
	}

	if searchBaseURL == "" {
		return nil, errors.New("no search base URL configured. Set SEARXNG_BASE_URL or use --search-base-url")
	}

	searchConfig := &searxng.Config{
		BaseURL:         searchBaseURL,
		HTTPTimeout:     30 * time.Second,
		MaxResponseSize: 1024 * 1024,
		UserAgent:       "local-newsroom/0.0.1",
	}
	// SearXNG API authentication, resolved upstream (CLI > env > config
	// file > built-in defaults) and passed through unchanged. Secret values
	// are never logged by the provider.
	if w.config.SearchAPIKey != "" {
		searchConfig.APIKey = w.config.SearchAPIKey
	}
	if w.config.SearchAPIHeader != "" {
		searchConfig.APIHeader = w.config.SearchAPIHeader
	}

	// Build LLM client for planner
	llmBaseURL, llmModel, llmAPIKey := w.getLLMConfig()

	if llmBaseURL == "" {
		return nil, errors.New("no LLM endpoint configured. Set OMLX_BASE_URL or use --llm-base-url")
	}

	llmClient := llm.NewClient(llm.Config{
		BaseURL: llmBaseURL,
		Model:   llmModel,
		APIKey:  llmAPIKey,
		Timeout: 120 * time.Second,
	})

	// Build planner
	plannerConfig := planner.Config{
		MinQueries:      2,
		MaxQueries:      w.config.DiscoveryConfig.MaxSearchQueries,
		Temperature:     0.5,
		MaxOutputTokens: 4000,
		PromptOverride:  w.config.PlannerPromptOverride,
	}

	// Adapt llmClient to planner.Client interface
	adapter := &llmToPlannerClientAdapter{
		client: llmClient,
		model:  llmModel,
	}

	plg, err := planner.New(adapter, llmModel, plannerConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create planner: %w", err)
	}

	// Validate and create SearXNG provider
	if err := searchConfig.Validate(); err != nil {
		return nil, fmt.Errorf("invalid SearXNG configuration: %w", err)
	}

	provider, err := searxng.NewProvider(searchConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create SearXNG provider: %w", err)
	}

	// Create discovery service
	discService := discovery.NewDiscovery(plg, provider, w.config.DiscoveryConfig)

	return discService, nil
}

// getLLMConfig returns LLM configuration from workflow config or environment.
func (w *Workflow) getLLMConfig() (string, string, string) {
	// Precedence: workflow config -> environment variables
	baseURL := w.config.LLMBaseURL
	if baseURL == "" {
		baseURL = os.Getenv("OMLX_BASE_URL")
	}

	model := w.config.LLMModel
	if model == "" {
		model = os.Getenv("OMLX_MODEL")
	}

	apiKey := w.config.LLMAPIKey
	if apiKey == "" {
		apiKey = os.Getenv("OMLX_API_KEY")
	}

	return baseURL, model, apiKey
}

// llmToPlannerClientAdapter adapts llm.Client to planner.Client.
// This is needed because llm.Client and planner.Client use different
// request/response types (llm.Request/Response vs planner.Request/Response).
type llmToPlannerClientAdapter struct {
	client llm.Client
	model  string
}

// Complete implements planner.Client by converting between types.
func (a *llmToPlannerClientAdapter) Complete(ctx context.Context, req planner.Request) (planner.Response, error) {
	// Convert planner.Request to llm.Request
	llmReq := llm.Request{
		SystemPrompt:    req.SystemPrompt,
		UserPrompt:      req.UserPrompt,
		Model:           req.Model,
		Temperature:     req.Temperature,
		MaxOutputTokens: req.MaxOutputTokens,
	}

	// Call the underlying llm client
	llmResp, err := a.client.Complete(ctx, llmReq)
	if err != nil {
		return planner.Response{}, err
	}

	// Convert llm.Response to planner.Response
	return planner.Response{Content: llmResp.Content}, nil
}

// discoveryService is an interface for source discovery services.
// This abstraction allows dependency injection and testing without
// coupling the workflow to specific discovery implementations.
type discoveryService interface {
	// Discover performs source discovery and returns candidates.
	// The candidates can then be used as URLs for fetching.
	Discover(ctx context.Context, topic string, language string, timeRangeHint *planner.TimeRangeHint) (*discovery.DiscoveryResult, error)
}

// fetchFailure represents a failed URL fetch (internal type).
type fetchFailure struct {
	URL      string
	FinalURL string
	Err      *fetcher.FetchError
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

// extractionFailure represents a failed document extraction (internal type).
type extractionFailure struct {
	URL      string
	FinalURL string
	Err      *extractor.ExtractionError
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

// convertToFetchFailures converts internal fetchFailures to public FetchFailures.
func convertToFetchFailures(internal []fetchFailure) []FetchFailure {
	result := make([]FetchFailure, 0, len(internal))
	for _, f := range internal {
		result = append(result, FetchFailure(f))
	}
	return result
}

// convertToExtractionFailures converts internal extractionFailures to public ExtractionFailures.
func convertToExtractionFailures(internal []extractionFailure) []ExtractionFailure {
	result := make([]ExtractionFailure, 0, len(internal))
	for _, f := range internal {
		result = append(result, ExtractionFailure(f))
	}
	return result
}

// extractCandidateURLs extracts URLs from a list of discovery candidates.
func extractCandidateURLs(candidates []discovery.Candidate) []string {
	result := make([]string, 0, len(candidates))
	for _, c := range candidates {
		result = append(result, c.CandidateURL)
	}
	return result
}

// mergeURLs merges two URL lists with deduplication, preserving explicit URLs first.
// Explicit URLs appear first in the result, followed by discovered URLs that are not duplicates.
func mergeURLs(explicitURLs, discoveredURLs []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(explicitURLs)+len(discoveredURLs))

	// Add explicit URLs first
	for _, url := range explicitURLs {
		normalized := strings.ToLower(strings.TrimSpace(url))
		if !seen[normalized] {
			seen[normalized] = true
			result = append(result, url)
		}
	}

	// Add discovered URLs that are not duplicates
	for _, url := range discoveredURLs {
		normalized := strings.ToLower(strings.TrimSpace(url))
		if !seen[normalized] {
			seen[normalized] = true
			result = append(result, url)
		}
	}

	return result
}

// ResearchFailure is a structured error for research pipeline failures.
type ResearchFailure struct {
	Type               string
	Message            string
	FetchFailures      []FetchFailure
	ExtractionFailures []ExtractionFailure
	DiscoveryQueryErr  []discovery.QueryResult
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
	case "llm_config", "researcher_init", "researcher_error", "dossier_validation", "discovery_failure", "config_error":
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

// NewDiscoveryService creates a new discovery service from configuration.
// This is a convenience function for production use when you want to use
// discovery.Discovery with default dependencies.
//
// To use this function, you must provide:
//   - planner: A discovery.Planner implementation
//   - provider: A search.Provider implementation
//
// Example:
//
//	discCfg := workflow.DefaultDiscoveryConfig()
//	discService := workflow.NewDiscoveryService(
//		planner.New(plannerClient, "local-llm", plannerCfg),
//		searchProvider,
//		discCfg,
//	)
//	wfConfig := workflow.Config{
//		Topic:            "European AI regulations",
//		AutoDiscover:     true,
//		DiscoveryService: discService,
//	}
func NewDiscoveryService(planner discovery.Planner, provider search.Provider, cfg discovery.Config) *discovery.Discovery {
	return discovery.NewDiscovery(planner, provider, cfg)
}
