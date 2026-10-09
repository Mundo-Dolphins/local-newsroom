// Package main implements the newsroom verify command.
//
// This command verifies a ResearchDossier by running the Verifier stage:
// - Reads an existing dossier from JSON
// - Evaluates each claim against evidence
// - Produces a VerificationResult with structured assessment
// - Writes the verification to JSON output
//
// The verify command can run independently from research, enabling:
// - Re-verification with updated evidence indices
// - Independent quality assessment
// - Debugging individual stages

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

	"github.com/Mundo-Dolphins/local-newsroom/internal/config"
	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/researcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/verifier"
	"github.com/spf13/cobra"
)

// verifyCmd is the "newsroom verify" subcommand.
var verifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Verify claims in a ResearchDossier",
	Long: `Verify claims in a ResearchDossier by evaluating each claim against existing evidence.

This command implements the verification stage of the newsroom pipeline:

  dossier -> verify -> verification_result

It takes a required --dossier flag pointing to a ResearchDossier JSON file
and produces a VerificationResult JSON output.

Configuration follows the precedence: CLI flags > environment variables > defaults.

Examples:

  # Verify a dossier from research
  newsroom verify \\
    --dossier dossier.json \\
    --output verification.json

  # Verify with custom LLM settings via environment
  export OMLX_BASE_URL="http://localhost:8080"
  export OMLX_MODEL="llama3.1"
  export OMLX_API_KEY="my-key"
  newsroom verify --dossier dossier.json --output verification.json

  # Re-verify with different quality threshold
  newsroom verify \\
    --dossier dossier.json \\
    --verification-date "2024-01-15T12:00:00Z" \\
    --quality-threshold 80 \\
    --output verification.json

  # Dry run - don't write output
  newsroom verify \\
    --dossier dossier.json \\
    --dry-run`,
	RunE: doVerify,
}

// verifyFlags holds the flags for the verify command.
type verifyFlags struct {
	dossierPath      string
	outputPath       string
	verificationDate string
	qualityThreshold float64
	llmBaseURL       string
	llmModel         string
	llmAPIKey        string
	llmTimeout       float64
	dryRun           bool
	verbosity        int
}

// flags are the verify command flags.
var vFlags verifyFlags

func init() {
	// Required flags
	verifyCmd.Flags().StringVarP(&vFlags.dossierPath, "dossier", "d", "", "Path to ResearchDossier JSON file (required)")

	// Output flag
	verifyCmd.Flags().StringVarP(&vFlags.outputPath, "output", "o", "verification.json", "Output path for verification JSON")

	// Verification parameters
	verifyCmd.Flags().StringVar(&vFlags.verificationDate, "verification-date", "", "Verification date (RFC3339, default: now)")
	verifyCmd.Flags().Float64Var(&vFlags.qualityThreshold, "quality-threshold", 60.0, "Minimum quality threshold (0-100)")

	// LLM configuration flags
	verifyCmd.Flags().StringVar(&vFlags.llmBaseURL, "llm-base-url", "", "LLM API base URL (default: OMLX_BASE_URL env var)")
	verifyCmd.Flags().StringVar(&vFlags.llmModel, "llm-model", "", "LLM model name (default: OMLX_MODEL env var)")
	verifyCmd.Flags().StringVar(&vFlags.llmAPIKey, "llm-api-key", "", "LLM API key for authentication (default: OMLX_API_KEY env var)")
	verifyCmd.Flags().Float64Var(&vFlags.llmTimeout, "llm-timeout", 120, "LLM request timeout in seconds")

	// Other flags
	verifyCmd.Flags().BoolVar(&vFlags.dryRun, "dry-run", false, "Run verification without writing output")
	verifyCmd.Flags().IntVarP(&vFlags.verbosity, "verbose", "v", 0, "Verbosity level (0=quiet, 1=normal, 2=debug)")

	// Mark dossier as required
	_ = verifyCmd.MarkFlagRequired("dossier")

	rootCmd.AddCommand(verifyCmd)
}

func doVerify(cmd *cobra.Command, args []string) error {
	// Validate inputs
	if vFlags.dossierPath == "" {
		return errors.New("--dossier is required")
	}

	// Read the dossier file
	dossierBytes, err := os.ReadFile(vFlags.dossierPath)
	if err != nil {
		return fmt.Errorf("failed to read dossier: %w", err)
	}

	var dossier researcher.ResearchDossier
	if err := json.Unmarshal(dossierBytes, &dossier); err != nil {
		return fmt.Errorf("failed to parse dossier JSON: %w", err)
	}

	if err := dossier.Validate(); err != nil {
		return fmt.Errorf("dossier validation failed: %w", err)
	}

	log(vFlags.verbosity, 1, "Loaded dossier with %d claims and %d sources",
		len(dossier.Claims), len(dossier.Sources))

	// Parse verification date
	verificationDate := time.Now().UTC()
	if vFlags.verificationDate != "" {
		parsed, err := time.Parse(time.RFC3339, vFlags.verificationDate)
		if err != nil {
			return fmt.Errorf("invalid verification date format (use RFC3339): %w", err)
		}
		verificationDate = parsed.UTC()
	}

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

	// Set up LLM client via the full precedence chain
	// (CLI flags > environment variables > config file > built-in defaults).
	llmClient, err := buildLLMClient(cmd)
	if err != nil {
		return err
	}

	// Resolve the verifier's generation parameters (CLI > env > config file
	// > stage built-in defaults 0.3 / 50000).
	temperature, tempSet, err := appConfig.GetFloat(config.SettingTemperature, flagOverride(cmd, "temperature"))
	if err != nil {
		return err
	}
	if !tempSet {
		temperature = 0.3
	}
	maxTokens, tokensSet, err := appConfig.GetInt(config.SettingMaxTokens, flagOverride(cmd, "max-tokens"))
	if err != nil {
		return err
	}
	if !tokensSet {
		maxTokens = 50000
	}

	// Build verifier config
	verifierConfig := verifier.VerifierConfig{
		Temperature:     temperature,
		MaxOutputTokens: maxTokens,
	}

	// Create verifier
	verifierInstance, err := verifier.New(llmClient, defaultModel, verifierConfig)
	if err != nil {
		return fmt.Errorf("failed to create verifier: %w", err)
	}

	// Create verification input
	verifyInput := verifier.Input{
		Dossier: &dossier,
		Parameters: verifier.VerificationParameters{
			VerifierID:       contracts.NewUUIDGenerator().GenerateIDWithPrefix("ver"),
			VerificationDate: contracts.MustTimeToUTC(verificationDate),
			QualityThreshold: vFlags.qualityThreshold,
		},
	}

	log(vFlags.verbosity, 1, "Starting verification of %d claims...", len(dossier.Claims))

	// Run verification
	result, err := verifierInstance.Verify(ctx, verifyInput)
	if err != nil {
		return fmt.Errorf("verification failed: %w", err)
	}

	// Validate result
	if err := verifier.ValidateResult(result); err != nil {
		return fmt.Errorf("verification result validation failed: %w", err)
	}

	// Print summary
	printVerificationSummary(result)

	if vFlags.dryRun {
		log(vFlags.verbosity, 1, "Dry run complete. No output written.")
		return nil
	}

	// Write verification result
	resultBytes, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal verification result: %w", err)
	}

	if err := os.WriteFile(vFlags.outputPath, resultBytes, 0644); err != nil {
		return fmt.Errorf("failed to write verification result: %w", err)
	}

	log(vFlags.verbosity, 1, "Verification result written to: %s", vFlags.outputPath)
	log(vFlags.verbosity, 1, "Quality score: %.1f/100", result.QualityScore)

	return nil
}

func printVerificationSummary(result *contracts.VerificationResult) {
	fmt.Fprintf(os.Stderr, "\n=== Verification Summary ===\n")
	fmt.Fprintf(os.Stderr, "Stable ID: %s\n", result.StableID)
	fmt.Fprintf(os.Stderr, "Verified at: %s\n", result.VerifiedAt.Format(time.RFC3339))
	fmt.Fprintf(os.Stderr, "Input dossier: %s\n", result.InputDossierID)
	fmt.Fprintf(os.Stderr, "Quality score: %.1f/100\n", result.QualityScore)
	fmt.Fprintf(os.Stderr, "Verification summary: %s\n", result.VerificationSummary)
	fmt.Fprintf(os.Stderr, "\n")

	// Count by status
	statusCount := make(map[contracts.VerificationStatus]int)
	for _, status := range result.VerificationStatuses {
		statusCount[status]++
	}

	fmt.Fprintf(os.Stderr, "Claims by status:\n")
	for status, count := range statusCount {
		fmt.Fprintf(os.Stderr, "  - %s: %d\n", status, count)
	}
	fmt.Fprintf(os.Stderr, "\n")

	// Show claim details
	fmt.Fprintf(os.Stderr, "Detailed results:\n")
	for claimID, details := range result.ClaimDetails {
		status := details.VerificationStatus
		conf := details.ConfidenceLevel
		fmt.Fprintf(os.Stderr, "  %s: %s (confidence: %s)\n", claimID, status, conf)
		if details.VerificationNotes != "" {
			fmt.Fprintf(os.Stderr, "    Notes: %s\n", details.VerificationNotes)
		}
	}
	fmt.Fprintf(os.Stderr, "\n")
}
