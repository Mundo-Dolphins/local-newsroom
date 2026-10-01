package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/extractor"
	"github.com/Mundo-Dolphins/local-newsroom/internal/fetcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/researcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workflow"
	"github.com/spf13/cobra"
)

// researchCmd is the "newsroom research" subcommand.
var researchCmd = &cobra.Command{
	Use:   "research",
	Short: "Research a topic from URLs",
	Long: `Research a topic by fetching, extracting, and analyzing content from URLs.

This command implements the research pipeline:
  fetch -> extract -> research -> validate -> write

It takes a required topic and one or more URLs, then produces a validated
research dossier in JSON format.

Examples:

  newsroom research --topic "Miami Dolphins changes" \\
    --url https://example.com/article1 \\
    --url https://example.com/article2 \\
    --output dossier.json

  newsroom research --topic "Sports news" \\
    --url https://example.com/sports \\
    --llm-base-url http://localhost:8000/v1 \\
    --output report.json`,
	RunE: doResearch,
}

// researchFlags holds the flags for the research command.
type researchFlags struct {
	topic        string
	urls         []string
	outputPath   string
	llmBaseURL   string
	llmModel     string
	llmTimeout   float64
	fetchTimeout float64
	maxSize      int64
	maxWords     int
	promptFile   string
}

// flags are the research command flags.
var rFlags researchFlags

func init() {
	researchCmd.Flags().StringVarP(&rFlags.topic, "topic", "t", "", "Research topic (required)")
	researchCmd.Flags().StringSliceVarP(&rFlags.urls, "url", "u", nil, "URL to fetch (can be specified multiple times)")
	researchCmd.Flags().StringVarP(&rFlags.outputPath, "output", "o", "dossier.json", "Output path for dossier JSON")
	researchCmd.Flags().StringVar(&rFlags.llmBaseURL, "llm-base-url", "", "LLM API base URL (default: OMLX_BASE_URL env var)")
	researchCmd.Flags().StringVar(&rFlags.llmModel, "llm-model", "", "LLM model name (default: OMLX_MODEL env var)")
	researchCmd.Flags().Float64Var(&rFlags.llmTimeout, "llm-timeout", 120, "LLM request timeout in seconds")
	researchCmd.Flags().Float64Var(&rFlags.fetchTimeout, "fetch-timeout", 30, "HTTP fetch timeout in seconds")
	researchCmd.Flags().Int64Var(&rFlags.maxSize, "max-size", 10*1024*1024, "Maximum response size in bytes")
	researchCmd.Flags().IntVar(&rFlags.maxWords, "max-words", 50000, "Maximum words per document")
	researchCmd.Flags().StringVar(&rFlags.promptFile, "prompt-file", "", "Path to custom prompt file")

	_ = researchCmd.MarkFlagRequired("topic")
	_ = researchCmd.MarkFlagRequired("url")

	rootCmd.AddCommand(researchCmd)
}

func doResearch(cmd *cobra.Command, args []string) error {
	// Validate inputs
	if rFlags.topic == "" {
		return errors.New("--topic is required")
	}
	if len(rFlags.urls) == 0 {
		return errors.New("at least one --url is required")
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

	// Build workflow config
	workflowConfig := workflow.Config{
		Topic:            rFlags.topic,
		URLs:             rFlags.urls,
		OutputPath:       rFlags.outputPath,
		FetcherConfig:    fetchConfig,
		ExtractorConfig:  extractorConfig,
		ResearcherConfig: researcherConfig,
		LLMBaseURL:       rFlags.llmBaseURL,
		LLMModel:         rFlags.llmModel,
		LLMTimeout:       rFlags.llmTimeout,
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
