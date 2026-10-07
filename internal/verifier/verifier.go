// Package verifier implements the Verifier stage that evaluates claims
// in a ResearchDossier against existing evidence.
//
// Architecture:
// - Verifier takes a validated ResearchDossier as input
// - Uses a generic llm.Client to evaluate claims
// - Returns a validated VerificationResult
// - Does not create new facts or modify the dossier
//
// Validation:
// - Strict validation of claim/source references
// - Rejects unknown claim IDs and source IDs
// - Rejects missing claim results
// - Rejects duplicate entries
// - Rejects invalid enum values
// - Rejects malformed JSON
package verifier

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/llm"
	"github.com/Mundo-Dolphins/local-newsroom/internal/researcher"
)

//go:embed prompts/*.prompt
var verifierPromptsFS embed.FS

// Verifier evaluates claims in a ResearchDossier and produces a
// VerificationResult with structured status assessments.
//
// The Verifier:
// - Takes a validated ResearchDossier as input
// - Evaluates each claim using only existing evidence
// - Returns a VerificationResult with status assessments
// - Does not create new facts or modify the dossier
//
// The Verifier is injectable and can be configured with different
// LLM clients for production or testing.
type Verifier struct {
	client llm.Client
	model  string
	config VerifierConfig
	tmpl   *template.Template
	prompt string
}

// VerifierConfig holds configuration for the Verifier.
type VerifierConfig struct {
	// Temperature controls randomness in generation (0.0 = deterministic).
	// Recommended: 0.0 for consistent verification.
	Temperature float64

	// MaxOutputTokens limits the response size.
	// A verification result typically needs 2k-8k tokens depending on dossier size.
	MaxOutputTokens int

	// PromptOverride allows customizing the system prompt for testing or special cases.
	// If empty, uses the verifier.prompt file from prompts/verifier.prompt.
	PromptOverride string

	// TimeNow is used to get the current time for verified_at timestamps.
	// For testing, this can be overridden to return a fixed time.
	TimeNow func() time.Time

	// ValidationChain is the chain of validation rules applied to
	// the VerificationResult before returning it. If nil, uses
	// the default validation chain.
	ValidationChain ValidationChain
}

// New creates a new Verifier with the given LLM client.
//
// The prompt template is loaded from prompts/verifier.prompt via go:embed
// unless PromptOverride is specified in config.
func New(client llm.Client, model string, config VerifierConfig) (*Verifier, error) {
	if client == nil {
		return nil, errors.New("llm.Client must not be nil")
	}

	// Load the prompt template
	prompt := config.PromptOverride
	if prompt == "" {
		promptBytes, err := verifierPromptsFS.ReadFile("prompts/verifier.prompt")
		if err != nil {
			return nil, fmt.Errorf("failed to read prompt template: %w", err)
		}
		prompt = string(promptBytes)
	}

	// Parse the template
	tmpl, err := template.New("verifier").Parse(prompt)
	if err != nil {
		return nil, fmt.Errorf("failed to parse prompt template: %w", err)
	}

	// Set default time function
	if config.TimeNow == nil {
		config.TimeNow = time.Now
	}

	// Set default validation chain
	if config.ValidationChain == nil {
		config.ValidationChain = NewValidationChain()
	}

	return &Verifier{
		client: client,
		model:  model,
		config: config,
		tmpl:   tmpl,
		prompt: prompt,
	}, nil
}

// Verify evaluates all claims in the ResearchDossier and returns
// a validated VerificationResult.
//
// Parameters:
//   - ctx: Context for cancellation and timeouts
//   - input: The input data containing the dossier and parameters
//
// Returns:
//   - *contracts.VerificationResult: The validated verification result
//   - error: If verification or validation fails
//
// The Verifier will:
//  1. Validate the input dossier structure
//  2. Build evidence indices for efficient lookup
//  3. Generate a verification prompt with the dossier and parameters
//  4. Send the prompt to the LLM client
//  5. Parse and validate the JSON response
//  6. Validate the verification result structure
//  7. Return the validated result
func (v *Verifier) Verify(ctx context.Context, input Input) (*contracts.VerificationResult, error) {
	if input.Dossier == nil {
		return nil, errors.New("dossier cannot be nil")
	}

	if input.Parameters.VerifierID == "" {
		return nil, errors.New("verification parameters must include verifier_id")
	}

	if input.Parameters.VerificationDate.IsZero() {
		return nil, errors.New("verification parameters must include verification_date")
	}

	// Validate input dossier has claims
	if len(input.Dossier.Claims) == 0 {
		return nil, errors.New("dossier must contain at least one claim to verify")
	}

	// Validate input dossier references are consistent
	if err := v.validateInput(input.Dossier); err != nil {
		return nil, fmt.Errorf("input validation failed: %w", err)
	}

	// Build evidence index for efficient lookup
	evidenceIndex := BuildEvidenceIndex(input.Dossier)

	// Build the verification prompt
	verificationPrompt := v.buildVerificationPrompt(input, evidenceIndex)

	// Create the LLM request
	req := llm.Request{
		SystemPrompt:    v.prompt,
		UserPrompt:      verificationPrompt,
		Model:           v.model,
		Temperature:     v.config.Temperature,
		MaxOutputTokens: v.config.MaxOutputTokens,
	}

	// Get the LLM response
	resp, err := v.client.Complete(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("llm completion failed: %w", err)
	}

	// Parse and validate the response
	result, err := v.parseAndValidateResponse(resp.Content, input)
	if err != nil {
		return nil, fmt.Errorf("failed to parse or validate verification result: %w", err)
	}

	// Apply validation chain
	if err := v.config.ValidationChain.Validate(result); err != nil {
		return nil, fmt.Errorf("verification result validation failed: %w", err)
	}

	return result, nil
}

// validateInput performs pre-verification validation on the input dossier.
func (v *Verifier) validateInput(dossier *researcher.ResearchDossier) error {
	// Validate claim IDs are unique
	seen := make(map[string]bool)
	for _, c := range dossier.Claims {
		if seen[c.ID] {
			return fmt.Errorf("dossier has duplicate claim ID: %q", c.ID)
		}
		seen[c.ID] = true
	}

	// Validate evidence source references
	sourceIDs := make(map[string]bool)
	for _, s := range dossier.Sources {
		sourceIDs[s.StableID] = true
	}

	for _, claim := range dossier.Claims {
		for _, evidence := range claim.Evidence {
			if !sourceIDs[evidence.SourceID] {
				return fmt.Errorf("claim %q references unknown source %q", claim.ID, evidence.SourceID)
			}
		}
	}

	return nil
}

// buildVerificationPrompt constructs the verification prompt from the input
// and evidence index.
func (v *Verifier) buildVerificationPrompt(input Input, evidenceIndex *EvidenceIndex) string {
	var buf bytes.Buffer

	// Render the template with the input data
	data := struct {
		Dossier *researcher.ResearchDossier
		Params  VerificationParameters
	}{
		Dossier: input.Dossier,
		Params:  input.Parameters,
	}

	if err := v.tmpl.Execute(&buf, data); err != nil {
		// This should never happen if the template is valid
		return fmt.Sprintf("Error generating verification prompt: %v", err)
	}

	return buf.String()
}

// parseAndValidateResponse parses the LLM response and performs validation.
func (v *Verifier) parseAndValidateResponse(content string, input Input) (*contracts.VerificationResult, error) {
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
	var result contracts.VerificationResult
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	// Validate required fields are present
	if result.StableID == "" {
		return nil, errors.New("missing required field: stable_id")
	}

	if result.InputDossierID != input.Dossier.StableID {
		return nil, fmt.Errorf("input_dossier_id mismatch: got %q, expected %q",
			result.InputDossierID, input.Dossier.StableID)
	}

	if result.VerifiedAt.IsZero() {
		return nil, errors.New("missing required field: verified_at")
	}

	// Validate that every claim has a verification result
	dossierClaimIDs := make(map[string]bool)
	for _, claim := range input.Dossier.Claims {
		dossierClaimIDs[claim.ID] = true
	}

	resultClaimIDs := make(map[string]bool)
	for claimID := range result.VerificationStatuses {
		resultClaimIDs[claimID] = true
	}

	// Check for missing claims
	for claimID := range dossierClaimIDs {
		if !resultClaimIDs[claimID] {
			return nil, fmt.Errorf("missing verification result for claim %q", claimID)
		}
	}

	// Check for unknown claims
	for claimID := range resultClaimIDs {
		if !dossierClaimIDs[claimID] {
			return nil, fmt.Errorf("unknown claim ID referenced in result: %q", claimID)
		}
	}

	// Validate verification statuses are valid enum values
	validStatuses := map[contracts.VerificationStatus]bool{
		contracts.VerificationStatusSupported:            true,
		contracts.VerificationStatusContradicted:         true,
		contracts.VerificationStatusUncertain:            true,
		contracts.VerificationStatusInsufficientEvidence: true,
	}

	for claimID, status := range result.VerificationStatuses {
		if !validStatuses[status] {
			return nil, fmt.Errorf("invalid verification_status for claim %q: %q", claimID, status)
		}
	}

	// Validate confidence levels
	validConfidences := map[contracts.ConfidenceLevel]bool{
		contracts.ConfidenceHigh:   true,
		contracts.ConfidenceMedium: true,
		contracts.ConfidenceLow:    true,
	}

	for claimID, details := range result.ClaimDetails {
		if details.ConfidenceLevel != "" && !validConfidences[details.ConfidenceLevel] {
			return nil, fmt.Errorf("invalid confidence_level for claim %q: %q", claimID, details.ConfidenceLevel)
		}
	}

	// Validate quality score is in range
	if result.QualityScore < 0 || result.QualityScore > 100 {
		return nil, fmt.Errorf("quality_score %v is out of range [0, 100]", result.QualityScore)
	}

	// Validate source IDs in evidence references
	for claimID, details := range result.ClaimDetails {
		allEvidence := append(details.SupportingEvidence, details.ConflictingEvidence...)
		for _, evidence := range allEvidence {
			if evidence.SourceID == "" {
				return nil, fmt.Errorf("empty source_id in evidence for claim %q", claimID)
			}
			if evidence.Excerpt == "" {
				return nil, fmt.Errorf("empty excerpt in evidence for claim %q", claimID)
			}
		}
	}

	// Validate contradiction references
	for _, contradiction := range result.Contradictions {
		if len(contradiction.ClaimIDs) == 0 {
			return nil, fmt.Errorf("contradiction %q has no claim_ids", contradiction.ID)
		}
		for _, claimID := range contradiction.ClaimIDs {
			if !dossierClaimIDs[claimID] {
				return nil, fmt.Errorf("contradiction %q references unknown claim %q", contradiction.ID, claimID)
			}
		}
	}

	return &result, nil
}
