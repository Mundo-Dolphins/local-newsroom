// Package finalchecker implements the final factual-check stage.
//
// The FinalFactualChecker verifies that the EditorialArtifact does not introduce
// or alter factual content relative to the verified claims in the VerificationResult.
//
// Responsibilities:
//   - Detect unknown claim references
//   - Detect factual statements not traceable to verified claims
//   - Detect contradicted/insufficient claims presented as facts
//   - Detect changed statistics/numbers/dates/names
//   - Detect invented quotations
//   - Detect overstatement of uncertain claims
//
// Approach:
//  1. Run deterministic validation first (claim ID existence, reference consistency)
//  2. Use LLM for prose-level factual comparison with narrow scope
//  3. Return structured findings without automatic rewriting
//
// This stage runs after Writer and before publishing.
package finalchecker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/llm"
)

// FinalFactualChecker verifies factual consistency between an EditorialArtifact
// and its source VerificationResult.
//
// The checker performs:
// 1. Deterministic validation of claim references and structures
// 2. LLM-based prose comparison for factual consistency
// 3. Structured findings output without automatic repair
type FinalFactualChecker struct {
	client llm.Client
	model  string
	config CheckerConfig
	prompt string
}

// CheckerConfig holds configuration for the FinalFactualChecker.
type CheckerConfig struct {
	// Temperature controls randomness in LLM comparisons (0.0 = deterministic).
	// Recommended: 0.1-0.3 for factual checking to reduce hallucinations.
	Temperature float64

	// MaxOutputTokens limits the LLM response size.
	// Finding summaries typically need 2k-5k tokens.
	MaxOutputTokens int

	// Language is the language to analyze in (e.g., "en", "es", "fr").
	// Used for language-specific detection (e.g., uncertainty phrases).
	Language string

	// PromptOverride allows customizing the system prompt for testing or special cases.
	// If empty, uses prompts/final-checker.prompt.
	PromptOverride string

	// TimeNow is used to get the current time for checked_at timestamps.
	// For testing, this can be overridden to return a fixed time.
	TimeNow func() time.Time

	// IDGenerator provides deterministic ID generation for testing.
	IDGenerator contracts.IDGenerator
}

// New creates a new FinalFactualChecker with the given LLM client.
//
// The prompt template is loaded from prompts/final-checker.prompt via go:embed
// unless PromptOverride is specified in config.
func New(client llm.Client, model string, config CheckerConfig) (*FinalFactualChecker, error) {
	if client == nil {
		return nil, errors.New("llm.Client must not be nil")
	}

	// Load the prompt template
	prompt := config.PromptOverride
	if prompt == "" {
		// Read prompt from root prompts directory
		promptBytes, err := os.ReadFile(filepath.Join("..", "..", "prompts", "final-checker.prompt"))
		if err != nil {
			return nil, fmt.Errorf("failed to read prompt template: %w", err)
		}
		prompt = string(promptBytes)
	}

	// Set default time function
	if config.TimeNow == nil {
		config.TimeNow = time.Now
	}

	// Set default ID generator
	if config.IDGenerator == nil {
		config.IDGenerator = contracts.NewUUIDGenerator()
	}

	return &FinalFactualChecker{
		client: client,
		model:  model,
		config: config,
		prompt: prompt,
	}, nil
}

// Check performs the final factual verification on an EditorialArtifact.
//
// Parameters:
//   - ctx: Context for cancellation and timeouts
//   - artifact: The EditorialArtifact to check (must have been validated)
//   - verificationResult: The VerificationResult containing source claims (must have been validated)
//
// Returns:
//   - *contracts.FactualCheckResult: The validation result with findings
//   - error: If checking fails (not if factual issues are found - those are in the result)
//
// The checker performs:
// 1. Deterministic validation of claim references and structures
// 2. LLM-based prose comparison for factual consistency
// 3. Structured findings output
//
// The result's OverallStatus indicates pass/fail/review status.
func (c *FinalFactualChecker) Check(ctx context.Context, artifact *contracts.EditorialArtifact, verificationResult *contracts.VerificationResult) (*contracts.FactualCheckResult, error) {
	if artifact == nil {
		return nil, errors.New("artifact cannot be nil")
	}

	if verificationResult == nil {
		return nil, errors.New("verificationResult cannot be nil")
	}

	if err := artifact.Validate(); err != nil {
		return nil, fmt.Errorf("artifact validation failed: %w", err)
	}

	if err := verificationResult.Validate(); err != nil {
		return nil, fmt.Errorf("verification result validation failed: %w", err)
	}

	// Run deterministic validation first
	deterministicIssues := c.runDeterministicChecks(artifact, verificationResult)

	// Prepare LLM comparison
	llmResult, err := c.runLLMComparison(ctx, artifact, verificationResult)
	if err != nil {
		// If LLM fails, we still return deterministic findings if any
		if len(deterministicIssues) > 0 {
			return c.buildResultFromDeterministicIssues(deterministicIssues, artifact, verificationResult), nil
		}
		return nil, fmt.Errorf("LLM comparison failed: %w", err)
	}

	// Merge deterministic and LLM findings
	result := c.mergeFindings(deterministicIssues, llmResult, artifact, verificationResult)

	// Validate the result
	if err := result.Validate(); err != nil {
		return nil, fmt.Errorf("factual check result validation failed: %w", err)
	}

	return result, nil
}

// runDeterministicChecks performs non-LLM validation that can be done deterministically.
func (c *FinalFactualChecker) runDeterministicChecks(artifact *contracts.EditorialArtifact, verificationResult *contracts.VerificationResult) []deterministicIssue {
	var issues []deterministicIssue

	// Build set of valid claim IDs and their statuses
	validClaimIDs := make(map[string]contracts.VerificationStatus)
	for claimID, status := range verificationResult.VerificationStatuses {
		validClaimIDs[claimID] = status
	}

	// Check 1: All claim IDs in claim_references must exist in verification result
	for claimID := range artifact.ClaimReferences {
		if _, exists := validClaimIDs[claimID]; !exists {
			issues = append(issues, deterministicIssue{
				claimID:   claimID,
				issueType: deterministicIssueUnknownClaim,
				severity:  contracts.WarningSeverityCritical,
				message:   fmt.Sprintf("Claim reference %q not found in verification result", claimID),
			})
		}
	}

	// Check 2: All claim IDs in sections must exist in verification result
	for i, section := range artifact.Sections {
		for _, claimID := range section.ClaimIDs {
			if _, exists := validClaimIDs[claimID]; !exists {
				issues = append(issues, deterministicIssue{
					claimID:   claimID,
					issueType: deterministicIssueUnknownClaim,
					severity:  contracts.WarningSeverityCritical,
					message:   fmt.Sprintf("Section %d references unknown claim %q", i, claimID),
					location:  fmt.Sprintf("section[%d]", i),
				})
			}
		}
	}

	// Check 3: All claim IDs in posts must exist in verification result
	for i, post := range artifact.Posts {
		for _, claimID := range post.ClaimIDs {
			if _, exists := validClaimIDs[claimID]; !exists {
				issues = append(issues, deterministicIssue{
					claimID:   claimID,
					issueType: deterministicIssueUnknownClaim,
					severity:  contracts.WarningSeverityCritical,
					message:   fmt.Sprintf("Post %d references unknown claim %q", i, claimID),
					location:  fmt.Sprintf("post[%d]", i),
				})
			}
		}
	}

	// Check 4: Contradicted claims should not be used as core
	for claimID, usage := range artifact.ClaimReferences {
		status := validClaimIDs[claimID]
		if status == contracts.VerificationStatusContradicted && usage.UsageType == contracts.ClaimUsageCore {
			issues = append(issues, deterministicIssue{
				claimID:   claimID,
				issueType: deterministicIssueContradictedAsFact,
				severity:  contracts.WarningSeverityCritical,
				message:   fmt.Sprintf("Contradicted claim %q used as core fact", claimID),
			})
		}

		// Check 5: Insufficient evidence claims should not be used as core
		if status == contracts.VerificationStatusInsufficientEvidence && usage.UsageType == contracts.ClaimUsageCore {
			issues = append(issues, deterministicIssue{
				claimID:   claimID,
				issueType: deterministicIssueUnverifiedAsFact,
				severity:  contracts.WarningSeverityHigh,
				message:   fmt.Sprintf("Insufficient evidence claim %q used as core fact", claimID),
			})
		}
	}

	return issues
}

// runLLMComparison sends the artifact and verification result to the LLM for prose-level comparison.
func (c *FinalFactualChecker) runLLMComparison(ctx context.Context, artifact *contracts.EditorialArtifact, verificationResult *contracts.VerificationResult) (*contracts.FactualCheckResult, error) {
	// Build the user prompt with verification data and artifact content
	userPrompt := buildUserPrompt(artifact, verificationResult, c.config)

	// Create the LLM request
	req := llm.Request{
		SystemPrompt:    c.prompt,
		UserPrompt:      userPrompt,
		Model:           c.model,
		Temperature:     c.config.Temperature,
		MaxOutputTokens: c.config.MaxOutputTokens,
	}

	// Get the LLM response
	resp, err := c.client.Complete(ctx, req)
	if err != nil {
		return nil, err
	}

	// Parse and validate the response
	result, err := parseFactualCheckResponse(resp.Content)
	if err != nil {
		return nil, fmt.Errorf("failed to parse factual check response: %w", err)
	}

	return result, nil
}

// buildUserPrompt constructs the user prompt for the LLM comparison.
func buildUserPrompt(artifact *contracts.EditorialArtifact, verificationResult *contracts.VerificationResult, config CheckerConfig) string {
	var sb strings.Builder

	// Add verification results summary
	sb.WriteString("=== VERIFICATION RESULTS SUMMARY ===\n\n")
	fmt.Fprintf(&sb, "Verification ID: %s\n", verificationResult.StableID)
	fmt.Fprintf(&sb, "Input Dossier ID: %s\n", verificationResult.InputDossierID)
	fmt.Fprintf(&sb, "Total claims: %d\n\n", len(verificationResult.VerificationStatuses))

	// List each verified claim with its status
	sb.WriteString("VERIFIED CLAIMS:\n")
	for claimID, status := range verificationResult.VerificationStatuses {
		details := verificationResult.ClaimDetails[claimID]
		fmt.Fprintf(&sb, "\n[%s] %s\n", status, claimID)
		fmt.Fprintf(&sb, "Statement: %s\n", details.Statement)

		// Add supporting evidence if available
		if len(details.SupportingEvidence) > 0 {
			sb.WriteString("Supporting evidence:\n")
			for _, ev := range details.SupportingEvidence {
				fmt.Fprintf(&sb, "  - Source %s: %s\n", ev.SourceID, ev.Excerpt)
			}
		}
	}
	sb.WriteString("\n")

	// Add editorial artifact content
	sb.WriteString("=== EDITORIAL ARTIFACT CONTENT ===\n\n")
	fmt.Fprintf(&sb, "Artifact ID: %s\n", artifact.StableID)
	fmt.Fprintf(&sb, "Artifact Type: %s\n", artifact.ArtifactType)
	fmt.Fprintf(&sb, "Title: %s\n\n", artifact.Title)

	// Add body if present
	if artifact.Body != "" {
		sb.WriteString("BODY:\n")
		sb.WriteString(artifact.Body)
		sb.WriteString("\n\n")
	}

	// Add sections
	if len(artifact.Sections) > 0 {
		sb.WriteString("SECTIONS:\n")
		for _, section := range artifact.Sections {
			fmt.Fprintf(&sb, "\n[Section %d: %s]\n", section.Order, section.Title)
			sb.WriteString(section.Body)
			if len(section.ClaimIDs) > 0 {
				fmt.Fprintf(&sb, "  Claim refs: %s\n", strings.Join(section.ClaimIDs, ", "))
			}
		}
		sb.WriteString("\n")
	}

	// Add posts
	if len(artifact.Posts) > 0 {
		sb.WriteString("POSTS:\n")
		for _, post := range artifact.Posts {
			fmt.Fprintf(&sb, "\n[Post %d]\n", post.Order)
			sb.WriteString(post.Body)
			if len(post.ClaimIDs) > 0 {
				fmt.Fprintf(&sb, "  Claim refs: %s\n", strings.Join(post.ClaimIDs, ", "))
			}
		}
		sb.WriteString("\n")
	}

	// Add claim references summary
	if len(artifact.ClaimReferences) > 0 {
		sb.WriteString("CLAIM REFERENCES:\n")
		for claimID, usage := range artifact.ClaimReferences {
			fmt.Fprintf(&sb, "\n%s: usage=%s, paraphrased=%v, locations=%v\n",
				claimID, usage.UsageType, usage.Paraphrased, usage.Locations)
		}
		sb.WriteString("\n")
	}

	// Add instruction to compare
	sb.WriteString("=== COMPARISON TASK ===\n\n")
	sb.WriteString("Compare the artifact content against the verified claims.\n")
	sb.WriteString("Detect: unknown claim refs, altered facts (numbers/dates/names), uncertain claims overstated,\n")
	sb.WriteString("contradicted claims as facts, insufficient evidence as facts, invented quotations.\n")
	sb.WriteString("Return JSON factual check result.\n")

	return sb.String()
}

// parseFactualCheckResponse parses the LLM response into a FactualCheckResult.
func parseFactualCheckResponse(content string) (*contracts.FactualCheckResult, error) {
	if content == "" {
		return nil, errors.New("empty model response")
	}

	// Trim whitespace and remove markdown code blocks if present
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```JSON")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	if content == "" {
		return nil, errors.New("model response contained only whitespace or markdown formatting")
	}

	// Parse JSON
	var result contracts.FactualCheckResult
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	// Validate basic structure
	if result.StableID == "" {
		return nil, errors.New("missing required field: stable_id")
	}

	if result.InputArtifactID == "" {
		return nil, errors.New("missing required field: input_artifact_id")
	}

	if result.CheckedAt.IsZero() {
		return nil, errors.New("missing required field: checked_at")
	}

	if result.OverallStatus == "" {
		return nil, errors.New("missing required field: overall_status")
	}

	switch result.OverallStatus {
	case contracts.FactualCheckStatusPass, contracts.FactualCheckStatusFail, contracts.FactualCheckStatusReview:
		// Valid
	default:
		return nil, fmt.Errorf("invalid overall_status: %q", result.OverallStatus)
	}

	if result.ConfidenceLevel == "" {
		return nil, errors.New("missing required field: confidence_level")
	}

	switch result.ConfidenceLevel {
	case contracts.ConfidenceHigh, contracts.ConfidenceMedium, contracts.ConfidenceLow:
		// Valid
	default:
		return nil, fmt.Errorf("invalid confidence_level: %q", result.ConfidenceLevel)
	}

	return &result, nil
}

// deterministicIssue represents an issue found by deterministic validation.
type deterministicIssue struct {
	claimID   string
	issueType deterministicIssueType
	severity  contracts.WarningSeverity
	message   string
	location  string
}

type deterministicIssueType string

const (
	deterministicIssueUnknownClaim       deterministicIssueType = "unknown_claim"
	deterministicIssueContradictedAsFact deterministicIssueType = "contradicted_as_fact"
	deterministicIssueUnverifiedAsFact   deterministicIssueType = "unverified_as_fact"
)

// buildResultFromDeterministicIssues creates a FactualCheckResult from deterministic issues.
func (c *FinalFactualChecker) buildResultFromDeterministicIssues(issues []deterministicIssue, artifact *contracts.EditorialArtifact, verificationResult *contracts.VerificationResult) *contracts.FactualCheckResult {
	baseTime := c.config.TimeNow()
	claimIDs := make([]string, 0, len(verificationResult.VerificationStatuses))
	claimStatuses := make(map[string]contracts.VerificationStatus)
	for claimID, status := range verificationResult.VerificationStatuses {
		claimIDs = append(claimIDs, claimID)
		claimStatuses[claimID] = status
	}

	var findings []contracts.FactualFinding
	var unsupportedStatements []contracts.UnsupportedStatement

	for _, issue := range issues {
		finding := contracts.FactualFinding{
			FindingID:       c.config.IDGenerator.GenerateIDWithPrefix("finding"),
			Statement:       issue.message,
			Severity:        issue.severity,
			Category:        contracts.FindingCategoryClaim,
			RelatedClaimIDs: []string{issue.claimID},
			FindingType:     contracts.FindingTypeError,
		}

		// Add evidence based on issue type
		switch issue.issueType {
		case deterministicIssueUnknownClaim:
			finding.Evidence = fmt.Sprintf("Claim reference %q exists in artifact but not in verification result", issue.claimID)
		case deterministicIssueContradictedAsFact:
			finding.Evidence = "Contradicted claim used with usage_type=core"
		case deterministicIssueUnverifiedAsFact:
			finding.Evidence = "Insufficient evidence claim used with usage_type=core"
		}

		findings = append(findings, finding)

		// Add unsupported statement for unknown claims
		if issue.issueType == deterministicIssueUnknownClaim {
			unsupportedStatements = append(unsupportedStatements, contracts.UnsupportedStatement{
				Location: contracts.StatementLocation{
					LocationType: contracts.LocationTypeBody,
					Index:        -1,
				},
				Statement:       issue.message,
				StatementType:   contracts.UnsupportedStatementNewFacts,
				Severity:        issue.severity,
				EvidenceMissing: "claim does not exist in verification result",
				ClaimIDs:        []string{issue.claimID},
			})
		}
	}

	// Determine overall status
	overallStatus := contracts.FactualCheckStatusPass
	for _, finding := range findings {
		if finding.Severity == contracts.WarningSeverityCritical || finding.FindingType == contracts.FindingTypeError {
			overallStatus = contracts.FactualCheckStatusFail
			break
		}
	}

	if overallStatus == contracts.FactualCheckStatusPass && len(findings) > 0 {
		overallStatus = contracts.FactualCheckStatusReview
	}

	// Generate recommendations
	var recommendations []contracts.Recommendation
	for _, finding := range findings {
		rec := contracts.Recommendation{
			RecommendationID:   c.config.IDGenerator.GenerateIDWithPrefix("rec"),
			Description:        "Review and address: " + finding.Statement,
			Priority:           recPriorityFromSeverity(finding.Severity),
			RecommendationType: recTypeFromFindingType(finding.FindingType),
			RelatedFindingIDs:  []string{finding.FindingID},
		}
		recommendations = append(recommendations, rec)
	}

	// Build summary
	summary := fmt.Sprintf("Final factual check of artifact %s completed.\n", artifact.StableID)
	summary += fmt.Sprintf("Overall status: %s\n", overallStatus)
	summary += fmt.Sprintf("Claims checked: %d\n", len(claimIDs))
	if len(unsupportedStatements) > 0 {
		summary += fmt.Sprintf("Unsupported statements: %d\n", len(unsupportedStatements))
	}
	if len(findings) > 0 {
		summary += fmt.Sprintf("Findings: %d\n", len(findings))
	}

	return &contracts.FactualCheckResult{
		StableID:                c.config.IDGenerator.GenerateIDWithPrefix("checker"),
		InputArtifactID:         artifact.StableID,
		CheckedAt:               baseTime,
		ClaimIDs:                claimIDs,
		ClaimVerificationStatus: claimStatuses,
		UnsupportedStatements:   unsupportedStatements,
		FactualFindings:         findings,
		OverallStatus:           overallStatus,
		ConfidenceLevel:         contracts.ConfidenceHigh,
		Summary:                 summary,
		Recommendations:         recommendations,
	}
}

// mergeFindings combines deterministic and LLM findings into a single result.
func (c *FinalFactualChecker) mergeFindings(deterministicIssues []deterministicIssue, llmResult *contracts.FactualCheckResult,
	artifact *contracts.EditorialArtifact, verificationResult *contracts.VerificationResult) *contracts.FactualCheckResult {

	baseTime := c.config.TimeNow()
	claimIDs := make([]string, 0, len(verificationResult.VerificationStatuses))
	claimStatuses := make(map[string]contracts.VerificationStatus)
	for claimID, status := range verificationResult.VerificationStatuses {
		claimIDs = append(claimIDs, claimID)
		claimStatuses[claimID] = status
	}

	// Collect all findings from both sources
	allFindings := make([]contracts.FactualFinding, 0, len(llmResult.FactualFindings)+len(deterministicIssues))

	// Add deterministic findings
	for _, issue := range deterministicIssues {
		finding := contracts.FactualFinding{
			FindingID:       c.config.IDGenerator.GenerateIDWithPrefix("finding-det"),
			Statement:       issue.message,
			Evidence:        c.buildDeterministicEvidence(issue),
			Severity:        issue.severity,
			Category:        contracts.FindingCategoryClaim,
			RelatedClaimIDs: []string{issue.claimID},
			FindingType:     contracts.FindingTypeError,
		}
		allFindings = append(allFindings, finding)
	}

	// Add LLM findings
	allFindings = append(allFindings, llmResult.FactualFindings...)

	// Collect all unsupported statements
	allUnsupported := append([]contracts.UnsupportedStatement{}, llmResult.UnsupportedStatements...)

	// Collect all altered details
	allAltered := append([]contracts.AlteredDetail{}, llmResult.AlteredDetails...)

	// Determine overall status
	overallStatus := c.determineOverallStatus(allFindings, allUnsupported, allAltered)

	// Generate recommendations
	recommendations := c.buildRecommendations(allFindings, overallStatus)

	// Build summary
	summary := fmt.Sprintf("Final factual check of artifact %s completed.\n", artifact.StableID)
	summary += fmt.Sprintf("Overall status: %s\n", overallStatus)
	summary += fmt.Sprintf("Confidence: %s\n", llmResult.ConfidenceLevel)
	summary += fmt.Sprintf("Claims checked: %d\n", len(claimIDs))
	if len(allUnsupported) > 0 {
		summary += fmt.Sprintf("Unsupported statements: %d\n", len(allUnsupported))
	}
	if len(allAltered) > 0 {
		summary += fmt.Sprintf("Altered details: %d\n", len(allAltered))
	}
	if len(allFindings) > 0 {
		summary += fmt.Sprintf("Findings: %d\n", len(allFindings))
	}

	return &contracts.FactualCheckResult{
		StableID:                c.config.IDGenerator.GenerateIDWithPrefix("checker"),
		InputArtifactID:         artifact.StableID,
		CheckedAt:               baseTime,
		ClaimIDs:                claimIDs,
		ClaimVerificationStatus: claimStatuses,
		UnsupportedStatements:   allUnsupported,
		AlteredDetails:          allAltered,
		FactualFindings:         allFindings,
		OverallStatus:           overallStatus,
		ConfidenceLevel:         llmResult.ConfidenceLevel,
		Summary:                 summary,
		Recommendations:         recommendations,
	}
}

// buildDeterministicEvidence constructs evidence description for deterministic findings.
func (c *FinalFactualChecker) buildDeterministicEvidence(issue deterministicIssue) string {
	switch issue.issueType {
	case deterministicIssueUnknownClaim:
		return fmt.Sprintf("Claim reference %q exists in artifact but not in verification result", issue.claimID)
	case deterministicIssueContradictedAsFact:
		return "Contradicted claim used with usage_type=core"
	case deterministicIssueUnverifiedAsFact:
		return "Insufficient evidence claim used with usage_type=core"
	default:
		return "Deterministic validation issue"
	}
}

// determineOverallStatus determines the overall status based on findings.
func (c *FinalFactualChecker) determineOverallStatus(findings []contracts.FactualFinding, unsupported []contracts.UnsupportedStatement, altered []contracts.AlteredDetail) contracts.FactualCheckStatus {
	// Check for critical issues that block publication
	hasCritical := false
	for _, finding := range findings {
		if finding.Severity == contracts.WarningSeverityCritical && finding.FindingType == contracts.FindingTypeError {
			hasCritical = true
			break
		}
	}

	// Check unsupported statements for critical or high issues
	for _, stmt := range unsupported {
		if stmt.Severity == contracts.WarningSeverityCritical || stmt.Severity == contracts.WarningSeverityHigh {
			hasCritical = true
			break
		}
	}

	// Check altered details for critical issues
	for _, detail := range altered {
		if detail.Significance == contracts.AlterationSignificanceCritical {
			hasCritical = true
			break
		}
	}

	if hasCritical {
		return contracts.FactualCheckStatusFail
	}

	// Check for high severity findings (should also block publication)
	hasHigh := false
	for _, finding := range findings {
		if finding.Severity == contracts.WarningSeverityHigh {
			hasHigh = true
			break
		}
	}

	if hasHigh {
		return contracts.FactualCheckStatusFail
	}

	// Check for medium issues that suggest review
	hasMedium := false
	for _, finding := range findings {
		if finding.Severity == contracts.WarningSeverityMedium {
			hasMedium = true
			break
		}
	}

	if hasMedium {
		return contracts.FactualCheckStatusReview
	}

	return contracts.FactualCheckStatusPass
}

// buildRecommendations creates recommendations based on findings.
func (c *FinalFactualChecker) buildRecommendations(findings []contracts.FactualFinding, status contracts.FactualCheckStatus) []contracts.Recommendation {
	var recommendations []contracts.Recommendation

	for _, finding := range findings {
		rec := contracts.Recommendation{
			RecommendationID:   c.config.IDGenerator.GenerateIDWithPrefix("rec"),
			Description:        "Review and address: " + finding.Statement,
			Priority:           recPriorityFromSeverity(finding.Severity),
			RecommendationType: recTypeFromFindingType(finding.FindingType),
			RelatedFindingIDs:  []string{finding.FindingID},
			EffortEstimate:     recEffortFromPriority(recPriorityFromSeverity(finding.Severity)),
		}
		recommendations = append(recommendations, rec)
	}

	return recommendations
}

// recPriorityFromSeverity maps severity to recommendation priority.
func recPriorityFromSeverity(severity contracts.WarningSeverity) contracts.RecommendationPriority {
	switch severity {
	case contracts.WarningSeverityCritical:
		return contracts.RecommendationPriorityCritical
	case contracts.WarningSeverityHigh:
		return contracts.RecommendationPriorityHigh
	case contracts.WarningSeverityMedium:
		return contracts.RecommendationPriorityMedium
	default:
		return contracts.RecommendationPriorityLow
	}
}

// recTypeFromFindingType maps finding type to recommendation type.
func recTypeFromFindingType(findingType contracts.FindingType) contracts.RecommendationType {
	switch findingType {
	case contracts.FindingTypeError:
		return contracts.RecommendationTypeFix
	case contracts.FindingTypeConcern:
		return contracts.RecommendationTypeClarify
	default:
		return contracts.RecommendationTypeReview
	}
}

// recEffortFromPriority maps priority to effort estimate.
func recEffortFromPriority(priority contracts.RecommendationPriority) contracts.RecommendationEffort {
	switch priority {
	case contracts.RecommendationPriorityCritical:
		return contracts.RecommendationEffortSubstantial
	case contracts.RecommendationPriorityHigh:
		return contracts.RecommendationEffortModerate
	default:
		return contracts.RecommendationEffortQuick
	}
}

// ExtractSupportedClaims returns claim IDs with "supported" status from a verification result.
func ExtractSupportedClaims(verificationResult *contracts.VerificationResult) []string {
	return verificationResult.GetSupportedClaims()
}

// ExtractClaimIDsFromArtifact returns all claim IDs referenced in an artifact.
func ExtractClaimIDsFromArtifact(artifact *contracts.EditorialArtifact) []string {
	return artifact.ExtractClaimIDs()
}

// AreClaimIDsConsistent checks if artifact claim IDs exist in verification result.
func AreClaimIDsConsistent(artifact *contracts.EditorialArtifact, verificationResult *contracts.VerificationResult) bool {
	validClaimIDs := make(map[string]bool)
	for claimID := range verificationResult.VerificationStatuses {
		validClaimIDs[claimID] = true
	}

	for _, claimID := range ExtractClaimIDsFromArtifact(artifact) {
		if !validClaimIDs[claimID] {
			return false
		}
	}

	return true
}

// GetVerifiedClaimStatement returns the statement text for a claim ID.
func GetVerifiedClaimStatement(verificationResult *contracts.VerificationResult, claimID string) (string, bool) {
	details, exists := verificationResult.ClaimDetails[claimID]
	return details.Statement, exists
}

// GetVerifiedClaimStatus returns the verification status for a claim ID.
func GetVerifiedClaimStatus(verificationResult *contracts.VerificationResult, claimID string) (contracts.VerificationStatus, bool) {
	status, exists := verificationResult.VerificationStatuses[claimID]
	return status, exists
}

// HasUnsupportedStatements returns true if the factual check result has critical unsupported statements.
func HasUnsupportedStatements(result *contracts.FactualCheckResult) bool {
	return len(result.UnsupportedStatements) > 0
}

// HasAlteredDetails returns true if the factual check result has altered details.
func HasAlteredDetails(result *contracts.FactualCheckResult) bool {
	return len(result.AlteredDetails) > 0
}

// GetCriticalFindings returns findings with critical severity.
func GetCriticalFindings(result *contracts.FactualCheckResult) []contracts.FactualFinding {
	var critical []contracts.FactualFinding
	for _, f := range result.FactualFindings {
		if f.Severity == contracts.WarningSeverityCritical {
			critical = append(critical, f)
		}
	}
	return critical
}

// GetCriticalAlteredDetails returns altered details with critical significance.
func GetCriticalAlteredDetails(result *contracts.FactualCheckResult) []contracts.AlteredDetail {
	var critical []contracts.AlteredDetail
	for _, a := range result.AlteredDetails {
		if a.Significance == contracts.AlterationSignificanceCritical {
			critical = append(critical, a)
		}
	}
	return critical
}

// SortClaimIDs sorts claim IDs alphabetically.
func SortClaimIDs(ids []string) {
	sort.Strings(ids)
}

// UniqueClaimIDs returns a slice with duplicate claim IDs removed.
func UniqueClaimIDs(ids []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			result = append(result, id)
		}
	}
	return result
}

// ClaimsPresentInBoth returns claim IDs that appear in both slices.
func ClaimsPresentInBoth(a, b []string) []string {
	seen := make(map[string]bool)
	for _, id := range a {
		seen[id] = true
	}

	result := make([]string, 0)
	for _, id := range b {
		if seen[id] {
			result = append(result, id)
		}
	}

	return result
}

// NumberDetection regex for detecting numbers in text.
var numberDetection = regexp.MustCompile(`\b\d+(?:\.\d+)?(?:%|ms|s|min|h)?\b`)

// ExtractNumbersFromText extracts all numbers from a text string.
func ExtractNumbersFromText(text string) []string {
	return numberDetection.FindAllString(text, -1)
}

// NormalizeNumber compares two numbers accounting for format variations.
func NormalizeNumber(num string) string {
	// Remove common suffixes for comparison
	cleaned := strings.ToLower(strings.TrimSpace(num))
	cleaned = strings.ReplaceAll(cleaned, "%", "")
	cleaned = strings.ReplaceAll(cleaned, "ms", "")
	cleaned = strings.ReplaceAll(cleaned, "s", "")
	cleaned = strings.ReplaceAll(cleaned, "min", "")
	cleaned = strings.ReplaceAll(cleaned, "h", "")
	return cleaned
}

// NumberComparisonResult indicates the result of a number comparison.
type NumberComparisonResult struct {
	Original string
	Altered  string
	Match    bool
}

// CompareNumbers compares two numbers accounting for format variations.
func CompareNumbers(original, altered string) NumberComparisonResult {
	origNorm := NormalizeNumber(original)
	altNorm := NormalizeNumber(altered)
	return NumberComparisonResult{
		Original: original,
		Altered:  altered,
		Match:    origNorm == altNorm,
	}
}

// DateDetection regex for detecting dates in text.
var dateDetection = regexp.MustCompile(`\b\d{4}[-/]\d{1,2}[-/]\d{1,2}\b|\b(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)[a-z]*\s+\d{1,2},?\s+\d{4}\b`)

// ExtractDatesFromText extracts dates from text.
func ExtractDatesFromText(text string) []string {
	return dateDetection.FindAllString(text, -1)
}

// NameDetection regex for detecting potential names (capitalized words).
var nameDetection = regexp.MustCompile(`\b[A-Z][a-z]+(?:\s+[A-Z][a-z]+)*\b`)

// ExtractNamesFromText extracts potential names from text.
func ExtractNamesFromText(text string) []string {
	names := nameDetection.FindAllString(text, -1)
	// Filter out common words
	filtered := make([]string, 0, len(names))
	for _, name := range names {
		if !isCommonWord(name) {
			filtered = append(filtered, name)
		}
	}
	return filtered
}

// isCommonWord checks if a word is a common word unlikely to be a name.
func isCommonWord(word string) bool {
	common := map[string]bool{
		"The": true, "And": true, "Of": true, "A": true, "An": true,
		"Results": true, "Report": true, "Figure": true, "Data": true,
		"System": true, "Program": true, "File": true, "Page": true,
	}
	return common[word]
}

// BuildDetermisticFinding creates a factual finding from deterministic validation.
func BuildDetermisticFinding(claimID, message string, severity contracts.WarningSeverity) contracts.FactualFinding {
	return contracts.FactualFinding{
		FindingID:       contracts.NewUUIDGenerator().GenerateIDWithPrefix("finding"),
		Statement:       message,
		Evidence:        fmt.Sprintf("Deterministic check: claim reference %q", claimID),
		Severity:        severity,
		Category:        contracts.FindingCategoryClaim,
		RelatedClaimIDs: []string{claimID},
		FindingType:     contracts.FindingTypeError,
	}
}

// CalculateVerificationCoverage computes the percentage of verified claims used in the artifact.
func CalculateVerificationCoverage(artifact *contracts.EditorialArtifact, verificationResult *contracts.VerificationResult) float64 {
	usedClaims := make(map[string]bool)
	for claimID := range artifact.ClaimReferences {
		usedClaims[claimID] = true
	}

	totalClaims := len(verificationResult.VerificationStatuses)
	if totalClaims == 0 {
		return 0
	}

	return float64(len(usedClaims)) / float64(totalClaims) * 100
}

// GetClaimUsageLocations returns the locations where a specific claim is used.
func GetClaimUsageLocations(artifact *contracts.EditorialArtifact, claimID string) []contracts.ClaimLocation {
	if usage, exists := artifact.ClaimReferences[claimID]; exists {
		return usage.Locations
	}
	return nil
}

// ContentMerger combines content from multiple sections/posts.
func ContentMerger(sections []contracts.Section, posts []contracts.Post) string {
	var result strings.Builder

	for _, section := range sections {
		result.WriteString(section.Body)
		result.WriteString("\n\n")
	}

	for _, post := range posts {
		result.WriteString(post.Body)
		result.WriteString("\n\n")
	}

	return strings.TrimSpace(result.String())
}

// BuildComparisonPrompt creates a prompt for comparing artifact content against verified claims.
func BuildComparisonPrompt(artifact *contracts.EditorialArtifact, verificationResult *contracts.VerificationResult) string {
	var sb strings.Builder

	sb.WriteString("Compare artifact against verified claims:\n\n")

	sb.WriteString("VERIFIED CLAIMS:\n")
	for claimID, status := range verificationResult.VerificationStatuses {
		details := verificationResult.ClaimDetails[claimID]
		fmt.Fprintf(&sb, "\n%s [%s]: %s\n", claimID, status, details.Statement)
	}

	sb.WriteString("\n\nARTIFACT CONTENT:\n")
	fmt.Fprintf(&sb, "ID: %s\n", artifact.StableID)

	if artifact.Body != "" {
		fmt.Fprintf(&sb, "Body: %s\n", artifact.Body)
	}

	for _, section := range artifact.Sections {
		fmt.Fprintf(&sb, "Section %d: %s\n", section.Order, section.Body)
	}

	for _, post := range artifact.Posts {
		fmt.Fprintf(&sb, "Post %d: %s\n", post.Order, post.Body)
	}

	return sb.String()
}

// ValidateAgainstClaims performs basic validation without LLM.
func ValidateAgainstClaims(artifact *contracts.EditorialArtifact, verificationResult *contracts.VerificationResult) []contracts.FactualFinding {
	var findings []contracts.FactualFinding

	// Build set of valid claim IDs
	validClaimIDs := make(map[string]bool)
	for claimID := range verificationResult.VerificationStatuses {
		validClaimIDs[claimID] = true
	}

	// Check all referenced claim IDs exist
	for claimID := range artifact.ClaimReferences {
		if !validClaimIDs[claimID] {
			finding := contracts.FactualFinding{
				FindingID:       contracts.NewUUIDGenerator().GenerateIDWithPrefix("finding"),
				Statement:       fmt.Sprintf("Unknown claim reference: %s", claimID),
				Evidence:        "Claim ID not found in verification result",
				Severity:        contracts.WarningSeverityCritical,
				Category:        contracts.FindingCategoryClaim,
				RelatedClaimIDs: []string{claimID},
				FindingType:     contracts.FindingTypeError,
			}
			findings = append(findings, finding)
		}
	}

	// Check contradicted claims not used as core
	for claimID, usage := range artifact.ClaimReferences {
		status := verificationResult.VerificationStatuses[claimID]
		if status == contracts.VerificationStatusContradicted && usage.UsageType == contracts.ClaimUsageCore {
			finding := contracts.FactualFinding{
				FindingID:       contracts.NewUUIDGenerator().GenerateIDWithPrefix("finding"),
				Statement:       fmt.Sprintf("Contradicted claim used as core: %s", claimID),
				Evidence:        fmt.Sprintf("Status: %s, Usage: %s", status, usage.UsageType),
				Severity:        contracts.WarningSeverityCritical,
				Category:        contracts.FindingCategoryClaim,
				RelatedClaimIDs: []string{claimID},
				FindingType:     contracts.FindingTypeError,
			}
			findings = append(findings, finding)
		}
	}

	return findings
}

// GenerateStableID creates a deterministic stable ID for a check run.
func GenerateStableID(artifactID, runType string, counter int) string {
	return fmt.Sprintf("checker-%s-%s-%d", artifactID, runType, counter)
}

// CheckRequiresLLM determines if LLM comparison is needed.
// Returns true if artifact has prose content that cannot be validated deterministically.
func CheckRequiresLLM(artifact *contracts.EditorialArtifact) bool {
	if artifact.Body != "" {
		return true
	}

	if len(artifact.Sections) > 0 {
		for _, s := range artifact.Sections {
			if s.Body != "" {
				return true
			}
		}
	}

	if len(artifact.Posts) > 0 {
		for _, p := range artifact.Posts {
			if p.Body != "" {
				return true
			}
		}
	}

	return false
}

// TestConstants for testing.
const (
	TestBaseTime  = "2024-01-15T12:00:00Z"
	TestModel     = "test-model"
	TestTemp      = 0.1
	TestMaxTokens = 5000
)
