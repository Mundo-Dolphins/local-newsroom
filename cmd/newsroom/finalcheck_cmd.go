// Package main implements the newsroom final-check command.
//
// This command performs final factual checking on an EditorialArtifact:
// - Reads an EditorialArtifact JSON
// - Reads the source VerificationResult
// - Performs LLM-based factual consistency checks
// - Produces a FactualCheckResult with findings
// - Outputs pass/fail/review status
//
// The final-check stage is the final gate before publishing.
// By default, output is not written if the check fails.

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
	"github.com/Mundo-Dolphins/local-newsroom/internal/finalchecker"
	"github.com/spf13/cobra"
)

// finalCheckCmd is the "newsroom final-check" subcommand.
var finalCheckCmd = &cobra.Command{
	Use:   "final-check",
	Short: "Perform final factual checking on an artifact",
	Long: `Perform final factual checking on an EditorialArtifact.

This command implements the final factual-check stage of the newsroom pipeline:

  editorial_artifact + verification_result -> factual_check_result

It takes required --artifact and --verification flags pointing to JSON files
and produces a FactualCheckResult JSON output.

Configuration follows the precedence: CLI flags > environment variables > defaults.

By default, the command exits with status 0 only if the factual check passes.
If the check fails or requires review, it exits with status 1 and prints findings.

Examples:

  # Check an artifact against its verification
  newsroom final-check \\
    --artifact article.json \\
    --verification verification.json \\
    --output check.json

  # Check without writing output (just report status)
  newsroom final-check \\
    --artifact article.json \\
    --verification verification.json \\
    --dry-run

  # Allow writing failed artifacts for debugging
  newsroom final-check \\
    --artifact article.json \\
    --verification verification.json \\
    --allow-failed-output \\
    --output check.json`,
	RunE: doFinalCheck,
}

// finalCheckFlags holds the flags for the final-check command.
type finalCheckFlags struct {
	artifactPath      string
	verificationPath  string
	outputPath        string
	temperature       float64
	maxTokens         int
	language          string
	promptFile        string
	dryRun            bool
	allowFailedOutput bool
	writeDraft        bool
	writeFindingsFile string
	verbosity         int
}

// flags are the final-check command flags.
var fcFlags finalCheckFlags

func init() {
	// Required flags
	finalCheckCmd.Flags().StringVarP(&fcFlags.artifactPath, "artifact", "a", "", "Path to EditorialArtifact JSON file (required)")
	finalCheckCmd.Flags().StringVarP(&fcFlags.verificationPath, "verification", "V", "", "Path to VerificationResult JSON file (required)")

	// Output flag
	finalCheckCmd.Flags().StringVarP(&fcFlags.outputPath, "output", "o", "check.json", "Output path for factual check JSON")

	// Generation parameters
	finalCheckCmd.Flags().Float64Var(&fcFlags.temperature, "temperature", 0.1, "Checking temperature (0.0 = deterministic)")
	finalCheckCmd.Flags().IntVar(&fcFlags.maxTokens, "max-tokens", 5000, "Maximum output tokens for check")
	finalCheckCmd.Flags().StringVar(&fcFlags.language, "language", "en", "Language code for analysis (e.g., 'en', 'es', 'fr')")

	// Other flags
	finalCheckCmd.Flags().StringVar(&fcFlags.promptFile, "prompt-file", "", "Path to custom prompt file")
	finalCheckCmd.Flags().BoolVar(&fcFlags.dryRun, "dry-run", false, "Run check without writing output")
	finalCheckCmd.Flags().BoolVar(&fcFlags.allowFailedOutput, "allow-failed-output", false, "Allow writing output even if check fails")
	finalCheckCmd.Flags().BoolVar(&fcFlags.writeDraft, "write-draft", false, "Write artifact as draft even if check fails")
	finalCheckCmd.Flags().StringVar(&fcFlags.writeFindingsFile, "write-findings", "", "Write findings to separate file (implies --write-draft)")
	finalCheckCmd.Flags().IntVarP(&fcFlags.verbosity, "verbose", "v", 0, "Verbosity level (0=quiet, 1=normal, 2=debug)")

	// Mark flags as required
	_ = finalCheckCmd.MarkFlagRequired("artifact")
	_ = finalCheckCmd.MarkFlagRequired("verification")

	rootCmd.AddCommand(finalCheckCmd)
}

func doFinalCheck(cmd *cobra.Command, args []string) error {
	// Validate inputs
	if fcFlags.artifactPath == "" {
		return errors.New("--artifact is required")
	}
	if fcFlags.verificationPath == "" {
		return errors.New("--verification is required")
	}

	// Read the artifact file
	artifactBytes, err := os.ReadFile(fcFlags.artifactPath)
	if err != nil {
		return fmt.Errorf("failed to read artifact: %w", err)
	}

	var artifact contracts.EditorialArtifact
	if err := json.Unmarshal(artifactBytes, &artifact); err != nil {
		return fmt.Errorf("failed to parse artifact JSON: %w", err)
	}

	if err := artifact.Validate(); err != nil {
		return fmt.Errorf("artifact validation failed: %w", err)
	}

	log(fcFlags.verbosity, 1, "Loaded artifact: %s (%s) with %d sections, %d posts",
		artifact.StableID, artifact.ArtifactType,
		len(artifact.Sections), len(artifact.Posts))

	// Read the verification result file
	verificationBytes, err := os.ReadFile(fcFlags.verificationPath)
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

	log(fcFlags.verbosity, 1, "Loaded verification result: %s (quality %.1f, %d claims)",
		verificationResult.StableID, verificationResult.QualityScore,
		len(verificationResult.VerificationStatuses))

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

	// Resolve the checker's generation parameters through the full
	// precedence chain (CLI > env > config file > stage built-in defaults
	// 0.1 / 5000 / en).
	temperature, tempSet, err := appConfig.GetFloat(config.SettingTemperature, flagOverride(cmd, "temperature"))
	if err != nil {
		return err
	}
	if !tempSet {
		temperature = 0.1
	}
	maxTokens, tokensSet, err := appConfig.GetInt(config.SettingMaxTokens, flagOverride(cmd, "max-tokens"))
	if err != nil {
		return err
	}
	if !tokensSet {
		maxTokens = 5000
	}
	language, _, err := appConfig.GetString(config.SettingDefaultsLanguage, flagOverride(cmd, "language"))
	if err != nil {
		return err
	}
	if language == "" {
		language = "en"
	}

	// Build checker config
	checkerConfig := finalchecker.CheckerConfig{
		Temperature:     temperature,
		MaxOutputTokens: maxTokens,
		Language:        language,
	}

	if fcFlags.promptFile != "" {
		promptBytes, err := os.ReadFile(fcFlags.promptFile)
		if err != nil {
			return fmt.Errorf("failed to read prompt file: %w", err)
		}
		checkerConfig.PromptOverride = string(promptBytes)
	}

	// Create checker with LLM client via the full precedence chain
	llmClient, err := buildLLMClient(cmd)
	if err != nil {
		return err
	}
	checker, err := finalchecker.New(llmClient, defaultModel, checkerConfig)
	if err != nil {
		return fmt.Errorf("failed to create checker: %w", err)
	}

	// Perform check
	log(fcFlags.verbosity, 2, "Starting factual check...")
	result, err := checker.Check(ctx, &artifact, &verificationResult)
	if err != nil {
		return fmt.Errorf("check failed: %w", err)
	}

	// Validate result
	if err := result.Validate(); err != nil {
		return fmt.Errorf("check result validation failed: %w", err)
	}

	// Print summary
	printCheckSummary(result)

	if fcFlags.dryRun {
		log(fcFlags.verbosity, 1, "Dry run complete. No output written.")
		return exitByStatus(result.OverallStatus)
	}

	// Determine if we should write output
	shouldWrite := fcFlags.allowFailedOutput || result.OverallStatus == contracts.FactualCheckStatusPass

	if shouldWrite {
		// Write check result
		resultBytes, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal check result: %w", err)
		}

		if err := os.WriteFile(fcFlags.outputPath, resultBytes, 0644); err != nil {
			return fmt.Errorf("failed to write check result: %w", err)
		}

		log(fcFlags.verbosity, 1, "Factual check result written to: %s", fcFlags.outputPath)
	} else {
		fmt.Fprintf(os.Stderr, "Factual check failed. Output not written (use --allow-failed-output to override).\n")
	}

	// Handle draft/writing failed artifacts
	if !shouldWrite && (fcFlags.writeDraft || fcFlags.writeFindingsFile != "") {
		fmt.Fprintf(os.Stderr, "Writing draft/failures for debugging...\n")

		// Write artifact as draft if requested
		if fcFlags.writeDraft {
			draftPath := fcFlags.outputPath + ".draft"
			artifactBytes, err := json.MarshalIndent(&artifact, "", "  ")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to write draft artifact: %v\n", err)
			} else {
				if err := os.WriteFile(draftPath, artifactBytes, 0644); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to write draft artifact: %v\n", err)
				} else {
					log(fcFlags.verbosity, 1, "Draft artifact written to: %s", draftPath)
				}
			}
		}

		// Write findings file if requested
		if fcFlags.writeFindingsFile != "" {
			findingsData := map[string]interface{}{
				"check_result":    result,
				"artifact_id":     artifact.StableID,
				"verification_id": verificationResult.StableID,
				"failed":          true,
			}
			findingsBytes, err := json.MarshalIndent(findingsData, "", "  ")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to marshal findings: %v\n", err)
			} else {
				if err := os.WriteFile(fcFlags.writeFindingsFile, findingsBytes, 0644); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to write findings: %v\n", err)
				} else {
					log(fcFlags.verbosity, 1, "Findings written to: %s", fcFlags.writeFindingsFile)
				}
			}
		}
	}

	return exitByStatus(result.OverallStatus)
}

func printCheckSummary(result *contracts.FactualCheckResult) {
	fmt.Fprintf(os.Stderr, "\n=== Factual Check Summary ===\n")
	fmt.Fprintf(os.Stderr, "Stable ID: %s\n", result.StableID)
	fmt.Fprintf(os.Stderr, "Input artifact: %s\n", result.InputArtifactID)
	fmt.Fprintf(os.Stderr, "Checked at: %s\n", result.CheckedAt.Format(time.RFC3339))
	fmt.Fprintf(os.Stderr, "Overall status: %s\n", result.OverallStatus)
	fmt.Fprintf(os.Stderr, "Confidence: %s\n", result.ConfidenceLevel)
	fmt.Fprintf(os.Stderr, "\n")

	// Summary
	if result.Summary != "" {
		fmt.Fprintf(os.Stderr, "Summary: %s\n", result.Summary)
		fmt.Fprintf(os.Stderr, "\n")
	}

	// Claim verification
	fmt.Fprintf(os.Stderr, "Claim verification status:\n")
	for claimID, status := range result.ClaimVerificationStatus {
		fmt.Fprintf(os.Stderr, "  - %s: %s\n", claimID, status)
	}
	fmt.Fprintf(os.Stderr, "\n")

	// Findings by severity
	findingsBySeverity := make(map[contracts.WarningSeverity][]contracts.FactualFinding)
	for _, finding := range result.FactualFindings {
		findingsBySeverity[finding.Severity] = append(findingsBySeverity[finding.Severity], finding)
	}

	fmt.Fprintf(os.Stderr, "Findings:\n")
	for severity, findings := range findingsBySeverity {
		fmt.Fprintf(os.Stderr, "  [%s] %d findings\n", severity, len(findings))
		for _, finding := range findings {
			fmt.Fprintf(os.Stderr, "    - [%s] %s\n", finding.FindingType, finding.Statement)
		}
	}
	fmt.Fprintf(os.Stderr, "\n")

	// Unsupported statements
	if len(result.UnsupportedStatements) > 0 {
		fmt.Fprintf(os.Stderr, "Unsupported statements: %d\n", len(result.UnsupportedStatements))
		for _, stmt := range result.UnsupportedStatements {
			fmt.Fprintf(os.Stderr, "  - [%s] %s\n", stmt.StatementType, stmt.Statement)
		}
		fmt.Fprintf(os.Stderr, "\n")
	}

	// Altered details
	if len(result.AlteredDetails) > 0 {
		fmt.Fprintf(os.Stderr, "Altered details: %d\n", len(result.AlteredDetails))
		for _, detail := range result.AlteredDetails {
			fmt.Fprintf(os.Stderr, "  - [%s: %s] %s -> %s\n",
				detail.DetailType, detail.Significance,
				detail.OriginalValue, detail.AlteredImageValue)
		}
		fmt.Fprintf(os.Stderr, "\n")
	}

	// Out of context statements
	if len(result.OutOfContextStatements) > 0 {
		fmt.Fprintf(os.Stderr, "Out of context statements: %d\n", len(result.OutOfContextStatements))
		for _, stmt := range result.OutOfContextStatements {
			fmt.Fprintf(os.Stderr, "  - %s\n", stmt.Statement)
			fmt.Fprintf(os.Stderr, "    Original context: %s\n", stmt.OriginalContext)
		}
		fmt.Fprintf(os.Stderr, "\n")
	}

	// Recommendations
	if len(result.Recommendations) > 0 {
		fmt.Fprintf(os.Stderr, "Recommendations:\n")
		for _, rec := range result.Recommendations {
			fmt.Fprintf(os.Stderr, "  - [%s] %s\n", rec.RecommendationType, rec.Description)
		}
		fmt.Fprintf(os.Stderr, "\n")
	}
}

func exitByStatus(status contracts.FactualCheckStatus) error {
	switch status {
	case contracts.FactualCheckStatusPass:
		fmt.Fprintf(os.Stderr, "Status: PASS - Artifact is ready for publishing\n")
		return nil
	case contracts.FactualCheckStatusFail:
		fmt.Fprintf(os.Stderr, "Status: FAIL - Critical factual issues detected\n")
		fmt.Fprintf(os.Stderr, "The artifact should NOT be published in its current form.\n")
		return errors.New("factual check failed")
	case contracts.FactualCheckStatusReview:
		fmt.Fprintf(os.Stderr, "Status: REVIEW - Non-critical issues detected\n")
		fmt.Fprintf(os.Stderr, "Human review is recommended before publishing.\n")
		return errors.New("factual check requires review")
	default:
		return fmt.Errorf("unknown status: %s", status)
	}
}
