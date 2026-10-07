// Package writer implements the Writer stage in the newsroom pipeline.
//
// The Writer takes a VerificationResult and produces an EditorialArtifact that:
// - Uses only verified claims with "supported" status as facts
// - Frames uncertain claims with appropriate uncertainty language
// - Excludes contradicted claims from being stated as true
// - Preserves factual accuracy and claim references
// - Returns a format-neutral structured artifact (not Hugo/Bluesky rendering)
//
// The Writer receives:
// - VerificationResult: Contains verified claims with their status
// - Profile/Instructions: Contains language, tone, format constraints
// - Any injected style guidelines (kept separate from engine logic)
//
// Core rules:
// 1. Never introduce factual information absent from the dossier
// 2. Do not present contradicted/insufficient claims as established facts
// 3. Frame uncertain material explicitly as uncertain
// 4. Preserve names, dates, scores, statistics exactly
// 5. Do not invent quotations or context
// 6. Do not add opinion unless explicitly allowed by profile
package writer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/llm"
)

// Writer produces an EditorialArtifact from a VerificationResult.
//
// The Writer:
// - Reads claims with their verification status
// - Uses only supported claims as established facts
// - Frames uncertain claims with appropriate language
// - Excludes contradicted claims from being stated as true
// - Preserves claim references and provenance
// - Returns a structured JSON artifact (format-neutral)
type Writer struct {
	client llm.Client
	model  string
	config WriterConfig
	tmpl   *template.Template
	prompt string
}

// WriterConfig holds configuration for the Writer.
type WriterConfig struct {
	// Temperature controls randomness in generation (0.0 = deterministic).
	// Recommended: 0.2-0.4 for factual writing.
	Temperature float64

	// MaxOutputTokens limits the response size.
	// An article with sections typically needs 8k-20k tokens.
	MaxOutputTokens int

	// Language is the language to write in (e.g., "en", "es", "fr").
	Language string

	// Tone is the writing tone (e.g., "neutral", "analytical", "conversational").
	Tone string

	// ArtifactType is the desired output format ("article", "thread", "hybrid").
	ArtifactType string

	// FormatConstraints contains additional format/style constraints.
	FormatConstraints []string

	// PromptOverride allows customizing the system prompt for testing or special cases.
	// If empty, uses the writer.prompt file from prompts/writer.prompt.
	PromptOverride string

	// TimeNow is used to get the current time for generated_at timestamps.
	// For testing, this can be overridden to return a fixed time.
	TimeNow func() time.Time

	// IDGenerator provides deterministic ID generation for testing.
	IDGenerator contracts.IDGenerator
}

// New creates a new Writer with the given LLM client.
//
// The prompt template is loaded from prompts/writer.prompt via go:embed
// unless PromptOverride is specified in config.
func New(client llm.Client, model string, config WriterConfig) (*Writer, error) {
	if client == nil {
		return nil, errors.New("llm.Client must not be nil")
	}

	// Load the prompt template
	prompt := config.PromptOverride
	if prompt == "" {
		// Read prompt from root prompts directory
		promptBytes, err := os.ReadFile(filepath.Join("..", "..", "prompts", "writer.prompt"))
		if err != nil {
			return nil, fmt.Errorf("failed to read prompt template: %w", err)
		}
		prompt = string(promptBytes)
	}

	// Parse the template
	tmpl, err := template.New("writer").Parse(prompt)
	if err != nil {
		return nil, fmt.Errorf("failed to parse prompt template: %w", err)
	}

	// Set default time function
	if config.TimeNow == nil {
		config.TimeNow = time.Now
	}

	// Set default ID generator
	if config.IDGenerator == nil {
		config.IDGenerator = contracts.NewUUIDGenerator()
	}

	return &Writer{
		client: client,
		model:  model,
		config: config,
		tmpl:   tmpl,
		prompt: prompt,
	}, nil
}

// Generate produces a validated EditorialArtifact from a VerificationResult and profile instructions.
//
// Parameters:
//   - ctx: Context for cancellation and timeouts
//   - verificationResult: The VerificationResult containing verified claims
//   - profileInstructions: Map of profile/injection keys (language, tone, format, etc.)
//
// Returns:
//   - *contracts.EditorialArtifact: The validated artifact
//   - error: If generation or validation fails
//
// The Writer will:
//  1. Build a prompt with verification results and profile instructions
//  2. Send the prompt to the LLM client
//  3. Parse and validate the JSON response
//  4. Validate the artifact structure and claim references
//  5. Return the validated artifact
func (w *Writer) Generate(ctx context.Context, verificationResult *contracts.VerificationResult, profileInstructions map[string]string) (*contracts.EditorialArtifact, error) {
	if verificationResult == nil {
		return nil, errors.New("verificationResult cannot be nil")
	}

	if err := verificationResult.Validate(); err != nil {
		return nil, fmt.Errorf("invalid verification result: %w", err)
	}

	// Validate profile instructions
	if profileInstructions == nil {
		profileInstructions = make(map[string]string)
	}

	// Build user prompt with verification data and profile instructions
	userPrompt := buildUserPrompt(verificationResult, profileInstructions, w.config)

	// Create the LLM request
	req := llm.Request{
		SystemPrompt:    w.prompt,
		UserPrompt:      userPrompt,
		Model:           w.model,
		Temperature:     w.config.Temperature,
		MaxOutputTokens: w.config.MaxOutputTokens,
	}

	// Get the LLM response
	resp, err := w.client.Complete(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("llm completion failed: %w", err)
	}

	// Parse and validate the response
	artifact, err := parseAndValidateResponse(resp.Content, verificationResult)
	if err != nil {
		return nil, fmt.Errorf("failed to parse or validate artifact: %w", err)
	}

	// Validate the artifact references against the verification result
	if err := w.validateClaimReferences(artifact, verificationResult); err != nil {
		return nil, fmt.Errorf("artifact claim reference validation failed: %w", err)
	}

	return artifact, nil
}

// buildUserPrompt constructs the user prompt from verification result and profile instructions.
func buildUserPrompt(verificationResult *contracts.VerificationResult, profileInstructions map[string]string, config WriterConfig) string {
	var sb strings.Builder

	// Add profile/instructions section
	sb.WriteString("=== PROFILE/INSTRUCTIONS ===\n\n")

	// Default values if not specified
	lang := profileInstructions["language"]
	if lang == "" {
		lang = config.Language
		if lang == "" {
			lang = "en"
		}
	}
	fmt.Fprintf(&sb, "language: %s\n", lang)

	tone := profileInstructions["tone"]
	if tone == "" {
		tone = config.Tone
		if tone == "" {
			tone = "neutral"
		}
	}
	fmt.Fprintf(&sb, "tone: %s\n", tone)

	artifactType := profileInstructions["artifact_type"]
	if artifactType == "" {
		artifactType = config.ArtifactType
		if artifactType == "" {
			artifactType = "article"
		}
	}
	fmt.Fprintf(&sb, "artifact_type: %s\n", artifactType)

	// Add format constraints
	if len(config.FormatConstraints) > 0 || profileInstructions["format_constraints"] != "" {
		sb.WriteString("format_constraints:\n")
		for _, c := range config.FormatConstraints {
			fmt.Fprintf(&sb, "  - %s\n", c)
		}
	}
	if fc := profileInstructions["format_constraints"]; fc != "" {
		for _, line := range strings.Split(fc, "\n") {
			if strings.TrimSpace(line) != "" {
				fmt.Fprintf(&sb, "  - %s\n", strings.TrimSpace(line))
			}
		}
	}

	// Add tone and style constraints from profile
	for k, v := range profileInstructions {
		if k == "language" || k == "tone" || k == "artifact_type" || k == "format_constraints" {
			continue
		}
		fmt.Fprintf(&sb, "%s: %s\n", k, v)
	}

	sb.WriteString("\n=== END PROFILE/INSTRUCTIONS ===\n\n")

	// Add verification results section
	sb.WriteString("=== VERIFICATION RESULTS ===\n\n")
	fmt.Fprintf(&sb, "Verification ID: %s\n", verificationResult.StableID)
	fmt.Fprintf(&sb, "Input Dossier ID: %s\n", verificationResult.InputDossierID)
	fmt.Fprintf(&sb, "Quality Score: %.1f\n", verificationResult.QualityScore)
	fmt.Fprintf(&sb, "Verification Summary: %s\n\n", verificationResult.VerificationSummary)

	// List supported claims
	supportedClaims := verificationResult.GetSupportedClaims()
	if len(supportedClaims) > 0 {
		sb.WriteString("SUPPORTED CLAIMS (can be stated as facts):\n")
		for _, claimID := range supportedClaims {
			details := verificationResult.ClaimDetails[claimID]
			fmt.Fprintf(&sb, "  %s: %s\n", claimID, details.Statement)
		}
		sb.WriteString("\n")
	}

	// List uncertain claims
	uncertainClaims := verificationResult.GetUncertainClaims()
	if len(uncertainClaims) > 0 {
		sb.WriteString("UNCERTAIN CLAIMS (must use uncertainty language):\n")
		for _, claimID := range uncertainClaims {
			details := verificationResult.ClaimDetails[claimID]
			fmt.Fprintf(&sb, "  %s: %s\n    (Note: Present this with phrases like 'sources suggest' or 'according to')\n", claimID, details.Statement)
		}
		sb.WriteString("\n")
	}

	// List contradicted claims
	contradictedClaims := verificationResult.GetContradictedClaims()
	if len(contradictedClaims) > 0 {
		sb.WriteString("CONTRADICTED CLAIMS (do not present as facts):\n")
		for _, claimID := range contradictedClaims {
			details := verificationResult.ClaimDetails[claimID]
			fmt.Fprintf(&sb, "  %s: %s\n    (Note: Do not state this as true. Either omit or present as a dispute.)\n", claimID, details.Statement)
		}
		sb.WriteString("\n")
	}

	// List insufficient evidence claims
	insufficientClaims := verificationResult.GetInsufficientEvidenceClaims()
	if len(insufficientClaims) > 0 {
		sb.WriteString("INSUFFICIENT EVIDENCE CLAIMS (do not present as facts):\n")
		for _, claimID := range insufficientClaims {
			details := verificationResult.ClaimDetails[claimID]
			fmt.Fprintf(&sb, "  %s: %s\n    (Note: Frame as unverified claim.)\n", claimID, details.Statement)
		}
		sb.WriteString("\n")
	}

	sb.WriteString("=== END VERIFICATION RESULTS ===\n\n")

	// Add instruction to return JSON artifact
	sb.WriteString("Return a JSON EditorialArtifact following the format specified in the system prompt.\n")
	sb.WriteString("DO NOT include markdown code fences. Return ONLY the JSON artifact.\n")

	return sb.String()
}

// parseAndValidateResponse parses the LLM response and performs validation.
func parseAndValidateResponse(content string, verificationResult *contracts.VerificationResult) (*contracts.EditorialArtifact, error) {
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
	var artifact contracts.EditorialArtifact
	if err := json.Unmarshal([]byte(content), &artifact); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	// Validate required top-level fields
	if artifact.StableID == "" {
		return nil, errors.New("missing required field: stable_id")
	}

	if artifact.ArtifactType == "" {
		return nil, errors.New("missing required field: artifact_type")
	}

	// Validate artifact type
	switch artifact.ArtifactType {
	case contracts.ArtifactTypeArticle, contracts.ArtifactTypeThread, contracts.ArtifactTypeHybrid:
		// Valid types
	default:
		return nil, fmt.Errorf("invalid artifact_type: %q (must be 'article', 'thread', or 'hybrid')", artifact.ArtifactType)
	}

	// Validate timestamps are valid
	if artifact.GenerationMetadata.GeneratedAt.IsZero() {
		return nil, errors.New("generation_metadata.generated_at is zero value")
	}

	if artifact.GenerationMetadata.InputVerificationID == "" {
		return nil, errors.New("missing required field: generation_metadata.input_verification_id")
	}

	return &artifact, nil
}

// validateClaimReferences validates that all claim references exist in the verification result
// and that contradicted/insufficient claims are not being used as facts.
func (w *Writer) validateClaimReferences(artifact *contracts.EditorialArtifact, verificationResult *contracts.VerificationResult) error {
	// Build set of valid claim IDs and their statuses
	validClaimIDs := make(map[string]contracts.VerificationStatus)
	for claimID, status := range verificationResult.VerificationStatuses {
		validClaimIDs[claimID] = status
	}

	// Validate that all references in the artifact are to valid claim IDs
	for claimID := range artifact.ClaimReferences {
		if _, exists := validClaimIDs[claimID]; !exists {
			return fmt.Errorf("claim reference %q not found in verification result", claimID)
		}
	}

	// Check that sections/posts only reference valid claims
	for i, section := range artifact.Sections {
		for _, claimID := range section.ClaimIDs {
			if _, exists := validClaimIDs[claimID]; !exists {
				return fmt.Errorf("section %d references unknown claim %q", i, claimID)
			}
		}
	}

	for i, post := range artifact.Posts {
		for _, claimID := range post.ClaimIDs {
			if _, exists := validClaimIDs[claimID]; !exists {
				return fmt.Errorf("post %d references unknown claim %q", i, claimID)
			}
		}
	}

	// Add warnings for contradicted/uncertain claims that are referenced
	for claimID, usage := range artifact.ClaimReferences {
		status := validClaimIDs[claimID]

		// Add warning if contradicted claims are used (regardless of usage type)
		if status == contracts.VerificationStatusContradicted {
			artifact.Warnings = append(artifact.Warnings, contracts.Warning{
				WarningType:     contracts.WarningTypeUncertainFacts,
				Severity:        contracts.WarningSeverityHigh,
				Message:         fmt.Sprintf("Claim %q has contradicted status and should not be stated as fact", claimID),
				RelatedClaimIDs: []string{claimID},
				SuggestedAction: "Review and either omit or reframe as a disputed claim",
			})
		}

		// Add warning if uncertain claims are used as core facts
		if status == contracts.VerificationStatusUncertain && usage.UsageType == contracts.ClaimUsageCore {
			artifact.Warnings = append(artifact.Warnings, contracts.Warning{
				WarningType:     contracts.WarningTypeUncertainFacts,
				Severity:        contracts.WarningSeverityMedium,
				Message:         fmt.Sprintf("Core claim %q has uncertain status - verify uncertainty language is used", claimID),
				RelatedClaimIDs: []string{claimID},
				SuggestedAction: "Ensure the text frames this as uncertain",
			})
		}

		// Add warning if insufficient evidence claims are used
		if status == contracts.VerificationStatusInsufficientEvidence {
			artifact.Warnings = append(artifact.Warnings, contracts.Warning{
				WarningType:     contracts.WarningTypeUnverifiedClaim,
				Severity:        contracts.WarningSeverityMedium,
				Message:         fmt.Sprintf("Claim %q has insufficient evidence - verify appropriate framing", claimID),
				RelatedClaimIDs: []string{claimID},
				SuggestedAction: "Ensure this is framed as unverified",
			})
		}
	}

	return nil
}

// GetSupportedClaims extracts claim IDs marked as "supported" from the verification result.
func GetSupportedClaims(verificationResult *contracts.VerificationResult) []string {
	return verificationResult.GetSupportedClaims()
}

// GetContradictedClaims extracts claim IDs marked as "contradicted" from the verification result.
func GetContradictedClaims(verificationResult *contracts.VerificationResult) []string {
	return verificationResult.GetContradictedClaims()
}

// GetUncertainClaims extracts claim IDs marked as "uncertain" from the verification result.
func GetUncertainClaims(verificationResult *contracts.VerificationResult) []string {
	return verificationResult.GetUncertainClaims()
}

// GetInsufficientEvidenceClaims extracts claim IDs marked as "insufficient_evidence" from the verification result.
func GetInsufficientEvidenceClaims(verificationResult *contracts.VerificationResult) []string {
	return verificationResult.GetInsufficientEvidenceClaims()
}

// ============================================================================
// Helper Types
// ============================================================================

// validateClaimID checks if a claim ID matches expected pattern.
var claimIDPattern = regexp.MustCompile(`^claim-\d+$`)

func validateClaimID(claimID string) bool {
	return claimIDPattern.MatchString(claimID)
}

// ValidateClaimID verifies that a claim ID is in valid format.
func ValidateClaimID(claimID string) bool {
	return validateClaimID(claimID)
}
