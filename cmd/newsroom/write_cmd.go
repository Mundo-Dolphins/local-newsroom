// Package main implements the newsroom write command.
//
// This command writes from a verified dossier by running the Writer stage:
// - Reads a VerificationResult JSON
// - Optionally loads editorial profile from profile directory
// - Generates an EditorialArtifact with verified claims
// - Produces format-neutral JSON output
//
// The write command can run independently from research, enabling:
// - Re-writing from the same verification with different profiles
// - Output format experimentation (article vs thread vs hybrid)
// - Profile-specific content generation

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/profiles"
	"github.com/Mundo-Dolphins/local-newsroom/internal/writer"
	"github.com/spf13/cobra"
)

// writeCmd is the "newsroom write" subcommand.
var writeCmd = &cobra.Command{
	Use:   "write",
	Short: "Write an artifact from a verified dossier",
	Long: `Write an EditorialArtifact from a VerificationResult.

This command implements the writing stage of the newsroom pipeline:

  verification_result -> writer -> editorial_artifact

It takes a required --dossier flag pointing to a VerificationResult JSON file
and produces an EditorialArtifact JSON output.

Configuration follows the precedence: CLI flags > environment variables > defaults.

The command supports optional profile directory loading for style, facts policy,
vocabulary, and examples. Profile loading follows this order:

  engine defaults -> publication common -> author profile -> CLI overrides

Examples:

  # Write an article from verification
  newsroom write \\
    --dossier verification.json \\
    --format article \\
    --output article.json

  # Write with a profile directory
  newsroom write \\
    --dossier verification.json \\
    --format article \\
    --profile-dir ./profiles/miami-dolphins \\
    --output article.json

  # Write a thread instead of article
  newsroom write \\
    --dossier verification.json \\
    --format thread \\
    --output thread.json

  # Write hybrid (article + thread)
  newsroom write \\
    --dossier verification.json \\
    --format hybrid \\
    --output hybrid.json`,
	RunE: doWrite,
}

// writeFlags holds the flags for the write command.
type writeFlags struct {
	dossierPath string
	outputPath  string
	format      string
	profileDir  string
	profileName string
	temperature float64
	maxTokens   int
	language    string
	tone        string
	promptFile  string
	dryRun      bool
	verbosity   int
}

// flags are the write command flags.
var wFlags writeFlags

// supportedFormats is the list of supported artifact types.
var supportedFormats = []string{
	"article",
	"thread",
	"hybrid",
}

func init() {
	// Required flags
	writeCmd.Flags().StringVarP(&wFlags.dossierPath, "dossier", "d", "", "Path to VerificationResult JSON file (required)")

	// Output flag
	writeCmd.Flags().StringVarP(&wFlags.outputPath, "output", "o", "artifact.json", "Output path for editorial artifact JSON")

	// Format flag
	writeCmd.Flags().StringVarP(&wFlags.format, "format", "f", "article", fmt.Sprintf("Output format (one of: %v)", supportedFormats))

	// Profile flags
	writeCmd.Flags().StringVar(&wFlags.profileDir, "profile-dir", "", "Path to profile directory")
	writeCmd.Flags().StringVar(&wFlags.profileName, "profile-name", "default", "Profile name within profile directory")

	// Generation parameters
	writeCmd.Flags().Float64Var(&wFlags.temperature, "temperature", 0.3, "Generation temperature (0.0-1.0)")
	writeCmd.Flags().IntVar(&wFlags.maxTokens, "max-tokens", 20000, "Maximum output tokens")
	writeCmd.Flags().StringVar(&wFlags.language, "language", "en", "Language code (e.g., 'en', 'es', 'fr')")
	writeCmd.Flags().StringVar(&wFlags.tone, "tone", "neutral", "Writing tone (e.g., 'neutral', 'analytical', 'conversational')")

	// Other flags
	writeCmd.Flags().StringVar(&wFlags.promptFile, "prompt-file", "", "Path to custom prompt file")
	writeCmd.Flags().BoolVar(&wFlags.dryRun, "dry-run", false, "Run generation without writing output")
	writeCmd.Flags().IntVarP(&wFlags.verbosity, "verbose", "v", 0, "Verbosity level (0=quiet, 1=normal, 2=debug)")

	// Mark dossier as required
	_ = writeCmd.MarkFlagRequired("dossier")

	// Validate format on pre-run
	writeCmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		if !isValidFormat(wFlags.format) {
			return fmt.Errorf("invalid format %q: must be one of %v", wFlags.format, supportedFormats)
		}
		return nil
	}

	rootCmd.AddCommand(writeCmd)
}

func doWrite(cmd *cobra.Command, args []string) error {
	// Validate inputs
	if wFlags.dossierPath == "" {
		return errors.New("--dossier is required")
	}

	// Read the verification result file
	verificationBytes, err := os.ReadFile(wFlags.dossierPath)
	if err != nil {
		return fmt.Errorf("failed to read verification result: %w", err)
	}

	var verificationResult contracts.VerificationResult
	if err := json.Unmarshal(verificationBytes, &verificationResult); err != nil {
		return fmt.Errorf("failed to parse verification result JSON: %w", err)
	}

	if err := verificationResult.Validate(); err != nil {
		return fmt.Errorf("verification result validation failed: %w", err)
	}

	log(wFlags.verbosity, 1, "Loaded verification result with %d claims and quality score %.1f",
		len(verificationResult.VerificationStatuses), verificationResult.QualityScore)

	// Parse the verification statuses
	supported := verificationResult.GetSupportedClaims()
	uncertain := verificationResult.GetUncertainClaims()
	contradicted := verificationResult.GetContradictedClaims()
	insufficient := verificationResult.GetInsufficientEvidenceClaims()

	log(wFlags.verbosity, 1, "Claims: %d supported, %d uncertain, %d contradicted, %d insufficient evidence",
		len(supported), len(uncertain), len(contradicted), len(insufficient))

	// Create context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Fprintf(os.Stderr, "\nInterrupted, cancelling...\n")
		cancel()
	}()

	// Build writer config
	writerConfig := writer.WriterConfig{
		Temperature:     wFlags.temperature,
		MaxOutputTokens: wFlags.maxTokens,
		Language:        wFlags.language,
		Tone:            wFlags.tone,
		ArtifactType:    wFlags.format,
	}

	if wFlags.promptFile != "" {
		promptBytes, err := os.ReadFile(wFlags.promptFile)
		if err != nil {
			return fmt.Errorf("failed to read prompt file: %w", err)
		}
		writerConfig.PromptOverride = string(promptBytes)
	}

	// Load profile if directory specified
	if wFlags.profileDir != "" {
		profileInjector, err := loadProfile(context.Background(), wFlags.profileDir, wFlags.profileName)
		if err != nil {
			return fmt.Errorf("failed to load profile: %w", err)
		}
		log(wFlags.verbosity, 1, "Loaded profile from %s with author %s", wFlags.profileDir, wFlags.profileName)
		writerConfig.ProfileInjector = profileInjector
	}

	// Create writer with LLM client
	llmClient := buildLLMClientFromFlags("", "", "", 120)
	writerInstance, err := writer.New(llmClient, defaultModel, writerConfig)
	if err != nil {
		return fmt.Errorf("failed to create writer: %w", err)
	}

	// Generate artifact
	log(wFlags.verbosity, 2, "Generating editorial artifact...")
	artifact, err := writerInstance.Generate(ctx, &verificationResult, nil)
	if err != nil {
		return fmt.Errorf("generation failed: %w", err)
	}

	// Validate artifact
	if err := artifact.Validate(); err != nil {
		return fmt.Errorf("artifact validation failed: %w", err)
	}

	// Print summary
	printArtifactSummary(artifact)

	if wFlags.dryRun {
		log(wFlags.verbosity, 1, "Dry run complete. No output written.")
		return nil
	}

	// Write artifact
	artifactBytes, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal artifact: %w", err)
	}

	if err := os.WriteFile(wFlags.outputPath, artifactBytes, 0644); err != nil {
		return fmt.Errorf("failed to write artifact: %w", err)
	}

	log(wFlags.verbosity, 1, "Editorial artifact written to: %s", wFlags.outputPath)
	log(wFlags.verbosity, 1, "Artifact type: %s, Claims referenced: %d, Warnings: %d",
		artifact.ArtifactType, len(artifact.ClaimReferences), len(artifact.Warnings))

	return nil
}

func isValidFormat(format string) bool {
	for _, f := range supportedFormats {
		if format == f {
			return true
		}
	}
	return false
}

func loadProfile(ctx context.Context, profileDir, authorName string) (profiles.Injector, error) {
	// Build profile loader config
	loaderConfig := profiles.Config{
		BasePath:           profileDir,
		MaxFileSize:        profiles.DefaultMaxFileSize,
		MaxExamplesTotal:   profiles.DefaultMaxExamplesTotal,
		MaxExamplesPerFile: profiles.DefaultMaxExamplesPerFile,
		AuthorOverride:     authorName,
	}

	// Create profile loader
	loader, err := profiles.NewLoader(loaderConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create profile loader: %w", err)
	}

	// Load profile
	profile, err := loader.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load profile: %w", err)
	}

	// Create injector
	return profiles.NewInjector(profile), nil
}

func printArtifactSummary(artifact *contracts.EditorialArtifact) {
	fmt.Fprintf(os.Stderr, "\n=== Artifact Summary ===\n")
	fmt.Fprintf(os.Stderr, "Stable ID: %s\n", artifact.StableID)
	fmt.Fprintf(os.Stderr, "Artifact type: %s\n", artifact.ArtifactType)
	fmt.Fprintf(os.Stderr, "Generated at: %s\n", artifact.GenerationMetadata.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(os.Stderr, "Input verification: %s\n", artifact.GenerationMetadata.InputVerificationID)
	fmt.Fprintf(os.Stderr, "\n")

	// Title info
	if artifact.Title != "" {
		fmt.Fprintf(os.Stderr, "Title: %s\n", artifact.Title)
	}
	if artifact.Subtitle != "" {
		fmt.Fprintf(os.Stderr, "Subtitle: %s\n", artifact.Subtitle)
	}
	fmt.Fprintf(os.Stderr, "\n")

	// Content counts
	bodyLen := len(artifact.Body)
	sectionCount := len(artifact.Sections)
	postCount := len(artifact.Posts)

	fmt.Fprintf(os.Stderr, "Content:\n")
	if bodyLen > 0 {
		fmt.Fprintf(os.Stderr, "  - Body: %d bytes\n", bodyLen)
	}
	if sectionCount > 0 {
		fmt.Fprintf(os.Stderr, "  - Sections: %d\n", sectionCount)
	}
	if postCount > 0 {
		fmt.Fprintf(os.Stderr, "  - Posts: %d\n", postCount)
	}
	fmt.Fprintf(os.Stderr, "\n")

	// Claim references
	fmt.Fprintf(os.Stderr, "Claim references: %d\n", len(artifact.ClaimReferences))
	for claimID, usage := range artifact.ClaimReferences {
		fmt.Fprintf(os.Stderr, "  - %s: %s\n", claimID, usage.UsageType)
	}
	fmt.Fprintf(os.Stderr, "\n")

	// Warnings
	if len(artifact.Warnings) > 0 {
		fmt.Fprintf(os.Stderr, "Warnings:\n")
		for _, warning := range artifact.Warnings {
			fmt.Fprintf(os.Stderr, "  - [%s] %s\n", warning.Severity, warning.Message)
		}
		fmt.Fprintf(os.Stderr, "\n")
	}
}
