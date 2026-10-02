package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/discovery"
	"github.com/Mundo-Dolphins/local-newsroom/internal/extractor"
	"github.com/Mundo-Dolphins/local-newsroom/internal/fetcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/researcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workflow"
	"github.com/spf13/cobra"
)

// researchCmd is the "newsroom research" subcommand.
var researchCmd = &cobra.Command{
	Use:   "research",
	Short: "Research a topic from URLs or automatic discovery",
	Long: `Research a topic by fetching, extracting, and analyzing content from URLs.

This command implements the research pipeline:
  fetch -> extract -> research -> validate -> write

It takes a required topic and one or more source inputs:
  - Explicit URLs (--url): URL-only mode (v0.1 behavior)
  - SearXNG discovery (--search-base-url): automatic source discovery
  - Both: supplement mode (explicit URLs + discovered sources)

Automatic discovery uses SearXNG to search the web for sources related to
the topic, then fetches and analyzes those sources.

Examples:

  # URL-only mode (v0.1)
  newsroom research --topic "Miami Dolphins changes" \\
    --url https://example.com/article1 \\
    --url https://example.com/article2 \\
    --output dossier.json

  # Automatic discovery via SearXNG
  newsroom research --topic "Miami Dolphins injury report Week 5" \\
    --search-base-url http://raspberrypi:8080 \\
    --output dossier.json

  # Supplement mode: explicit URLs + discovered sources
  newsroom research --topic "Sports news" \\
    --url https://example.com/sports \\
    --search-base-url http://raspberrypi:8080 \\
    --search-max-queries 3 \\
    --output report.json

  # Using environment variables for SearXNG configuration
  export SEARXNG_BASE_URL="http://raspberrypi:8080"
  newsroom research --topic "Research topic" --output dossier.json`,
	RunE: doResearch,
}

// researchFlags holds the flags for the research command.
type researchFlags struct {
	topic                 string
	urls                  []string
	outputPath            string
	searchBaseURL         string
	searchLanguage        string
	searchTimeRange       string
	searchMaxQueries      int
	searchResultsPerQuery int
	searchMaxSources      int
	llmBaseURL            string
	llmModel              string
	llmAPIKey             string
	llmTimeout            float64
	fetchTimeout          float64
	maxSize               int64
	maxWords              int
	promptFile            string
}

// flags are the research command flags.
var rFlags researchFlags

func init() {
	// Required flags
	researchCmd.Flags().StringVarP(&rFlags.topic, "topic", "t", "", "Research topic (required)")

	// URL flags
	researchCmd.Flags().StringSliceVarP(&rFlags.urls, "url", "u", nil, "URL to fetch (can be specified multiple times)")

	// Output flag
	researchCmd.Flags().StringVarP(&rFlags.outputPath, "output", "o", "dossier.json", "Output path for dossier JSON")

	// LLM configuration flags
	researchCmd.Flags().StringVar(&rFlags.llmBaseURL, "llm-base-url", "", "LLM API base URL (default: OMLX_BASE_URL env var)")
	researchCmd.Flags().StringVar(&rFlags.llmModel, "llm-model", "", "LLM model name (default: OMLX_MODEL env var)")
	researchCmd.Flags().StringVar(&rFlags.llmAPIKey, "llm-api-key", "", "LLM API key for authentication (default: OMLX_API_KEY env var)")
	researchCmd.Flags().Float64Var(&rFlags.llmTimeout, "llm-timeout", 120, "LLM request timeout in seconds")
	researchCmd.Flags().Float64Var(&rFlags.fetchTimeout, "fetch-timeout", 30, "HTTP fetch timeout in seconds")
	researchCmd.Flags().Int64Var(&rFlags.maxSize, "max-size", 10*1024*1024, "Maximum response size in bytes")
	researchCmd.Flags().IntVar(&rFlags.maxWords, "max-words", 50000, "Maximum words per document")
	researchCmd.Flags().StringVar(&rFlags.promptFile, "prompt-file", "", "Path to custom prompt file")

	// Search configuration flags
	researchCmd.Flags().StringVar(&rFlags.searchBaseURL, "search-base-url", "", "SearXNG base URL (default: SEARXNG_BASE_URL env var)")
	researchCmd.Flags().StringVar(&rFlags.searchLanguage, "search-language", "", "Search language (RFC 5646 tag, e.g., 'en', 'en-US')")
	researchCmd.Flags().StringVar(&rFlags.searchTimeRange, "search-time-range", "", "Search time range (e.g., 'last_week', 'last_month', 'last_year')")
	researchCmd.Flags().IntVar(&rFlags.searchMaxQueries, "search-max-queries", 5, "Maximum search queries to execute (1-10)")
	researchCmd.Flags().IntVar(&rFlags.searchResultsPerQuery, "search-results-per-query", 10, "Maximum results per search query (1-100)")
	researchCmd.Flags().IntVar(&rFlags.searchMaxSources, "search-max-sources", 0, "Maximum candidate sources to return (0 = unlimited)")

	// Mark topic as required
	_ = researchCmd.MarkFlagRequired("topic")

	// Add help information about source modes
	researchCmd.HelpTemplate()

	rootCmd.AddCommand(researchCmd)
}

func doResearch(cmd *cobra.Command, args []string) error {
	// Validate inputs
	if rFlags.topic == "" {
		return errors.New("--topic is required")
	}

	// Validate URL-only mode configuration (when URLs are provided without search)
	urlsProvided := len(rFlags.urls) > 0
	searchConfigured := rFlags.searchBaseURL != ""

	// Validate at least one source mode is configured
	if !urlsProvided && !searchConfigured {
		return errors.New("at least one source mode required: provide --url(s) or configure --search-base-url (or SEARXNG_BASE_URL)")
	}

	// Set up fetcher config
	fetchConfig := fetcher.Config{
		Timeout:   time.Duration(rFlags.fetchTimeout) * time.Second,
		MaxSize:   rFlags.maxSize,
		UserAgent: "local-newsroom/0.0.1",
	}

	// Set up extractor config
	extractorConfig := extractor.Config{
		MaxWordCount:       rFlags.maxWords,
		MaxPlainTextLength: 1024 * 1024,
		MaxTitleLength:     1000,
	}
	extractorConfig.Validate()

	// Set up researcher config
	researcherConfig := researcher.ClientConfig{
		Temperature:     0.3,
		MaxOutputTokens: 50000,
	}

	if rFlags.promptFile != "" {
		promptBytes, err := os.ReadFile(rFlags.promptFile)
		if err != nil {
			return fmt.Errorf("failed to read prompt file: %w", err)
		}
		researcherConfig.PromptOverride = string(promptBytes)
	}

	// Determine mode and build workflow config
	var workflowConfig workflow.Config

	if !searchConfigured {
		// URL-only mode (v0.1)
		workflowConfig = workflow.Config{
			Topic:            rFlags.topic,
			URLs:             rFlags.urls,
			OutputPath:       rFlags.outputPath,
			FetcherConfig:    fetchConfig,
			ExtractorConfig:  extractorConfig,
			ResearcherConfig: researcherConfig,
			LLMBaseURL:       rFlags.llmBaseURL,
			LLMModel:         rFlags.llmModel,
			LLMAPIKey:        rFlags.llmAPIKey,
			LLMTimeout:       rFlags.llmTimeout,
			AutoDiscover:     false,
		}
	} else if urlsProvided {
		// Supplement mode: explicit URLs + discovered sources
		workflowConfig = buildSupplementConfig()
	} else {
		// Automatic discovery mode: search only
		workflowConfig = buildDiscoveryConfig()
	}

	// Create workflow
	w := workflow.New(workflowConfig)

	// Set up context with cancellation on interrupt
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Fprintf(os.Stderr, "\nInterrupted, cancelling...\n")
		cancel()
	}()

	// Run the workflow
	if err := w.Run(ctx); err != nil {
		// ResearchFailure already prints detailed messages
		return err
	}

	return nil
}

// buildSupplementConfig builds workflow config for supplement mode.
func buildSupplementConfig() workflow.Config {
	return workflow.Config{
		Topic:            rFlags.topic,
		URLs:             rFlags.urls,
		OutputPath:       rFlags.outputPath,
		FetcherConfig:    buildFetcherConfig(),
		ExtractorConfig:  buildExtractorConfig(),
		ResearcherConfig: buildResearcherConfig(),
		LLMBaseURL:       rFlags.llmBaseURL,
		LLMModel:         rFlags.llmModel,
		LLMAPIKey:        rFlags.llmAPIKey,
		LLMTimeout:       rFlags.llmTimeout,
		AutoDiscover:     true,
		SupplementMode:   true,
		DiscoveryConfig:  buildDiscoveryConfigInternal(),
		SearchBaseURL:    rFlags.searchBaseURL,
		SearchLanguage:   rFlags.searchLanguage,
		SearchTimeRange:  rFlags.searchTimeRange,
		SearchAPIKey:     os.Getenv("SEARXNG_API_KEY"),
		SearchAPIHeader:  os.Getenv("SEARXNG_API_HEADER"),
	}
}

// buildDiscoveryConfig builds workflow config for automatic discovery mode.
func buildDiscoveryConfig() workflow.Config {
	return workflow.Config{
		Topic:            rFlags.topic,
		URLs:             []string{},
		OutputPath:       rFlags.outputPath,
		FetcherConfig:    buildFetcherConfig(),
		ExtractorConfig:  buildExtractorConfig(),
		ResearcherConfig: buildResearcherConfig(),
		LLMBaseURL:       rFlags.llmBaseURL,
		LLMModel:         rFlags.llmModel,
		LLMAPIKey:        rFlags.llmAPIKey,
		LLMTimeout:       rFlags.llmTimeout,
		AutoDiscover:     true,
		SupplementMode:   false,
		DiscoveryConfig:  buildDiscoveryConfigInternal(),
		SearchBaseURL:    rFlags.searchBaseURL,
		SearchLanguage:   rFlags.searchLanguage,
		SearchTimeRange:  rFlags.searchTimeRange,
		SearchAPIKey:     os.Getenv("SEARXNG_API_KEY"),
		SearchAPIHeader:  os.Getenv("SEARXNG_API_HEADER"),
	}
}

// buildFetcherConfig returns fetcher.Config.
func buildFetcherConfig() fetcher.Config {
	return fetcher.Config{
		Timeout:   time.Duration(rFlags.fetchTimeout) * time.Second,
		MaxSize:   rFlags.maxSize,
		UserAgent: "local-newsroom/0.0.1",
	}
}

// buildExtractorConfig returns extractor.Config.
func buildExtractorConfig() extractor.Config {
	return extractor.Config{
		MaxWordCount:       rFlags.maxWords,
		MaxPlainTextLength: 1024 * 1024,
		MaxTitleLength:     1000,
	}
}

// buildResearcherConfig returns researcher.ClientConfig.
func buildResearcherConfig() researcher.ClientConfig {
	return researcher.ClientConfig{
		Temperature:     0.3,
		MaxOutputTokens: 50000,
	}
}

// buildDiscoveryConfigInternal builds discovery.Config from flags.
func buildDiscoveryConfigInternal() discovery.Config {
	cfg := discovery.Config{
		MaxSearchQueries:         rFlags.searchMaxQueries,
		ResultsPerQuery:          rFlags.searchResultsPerQuery,
		MaxCandidates:            rFlags.searchMaxSources,
		EnableDuplicateReporting: true,
	}

	// Validate config
	if err := cfg.Validate(); err != nil {
		// Use defaults if config is invalid
		cfg = discovery.DefaultConfig()
	}

	return cfg
}
