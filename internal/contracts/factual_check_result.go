package contracts

import (
	"errors"
	"fmt"
	"time"
)

// FactualCheckResult represents the output of the final factual-check stage.
//
// The final factual check takes an EditorialArtifact and verifies:
// - All claims referenced are actually verified (not unverified or uncertain)
// - No unsupported statements appear in the content
// - Numbers, dates, and names are consistent with source evidence
// - No factual distortions or out-of-context statements
//
// This is the final gate before publication. The PassStatus must be Pass
// for the artifact to proceed to publishing.
type FactualCheckResult struct {
	// StableID uniquely identifies this factual check run.
	StableID string `json:"stable_id"`

	// InputArtifactID references the EditorialArtifact.stable_id that was checked.
	InputArtifactID string `json:"input_artifact_id"`

	// CheckedAt is the UTC timestamp when the factual check was completed.
	CheckedAt time.Time `json:"checked_at"`

	// ClaimIDs lists all claim IDs that the artifact claims to reference.
	ClaimIDs []string `json:"claim_ids"`

	// ClaimVerificationStatus maps claim IDs to their verification status.
	// This should match VerificationResult.VerificationStatuses for traceability.
	ClaimVerificationStatus map[string]VerificationStatus `json:"claim_verification_status"`

	// UnsupportedStatements lists factual statements found without verification.
	UnsupportedStatements []UnsupportedStatement `json:"unsupported_statements,omitempty"`

	// AlteredDetails lists numbers, dates, or names that differ from source evidence.
	AlteredDetails []AlteredDetail `json:"altered_details,omitempty"`

	// OutOfContextStatements lists statements found to be used out of context.
	OutOfContextStatements []OutOfContextStatement `json:"out_of_context_statements,omitempty"`

	// FactualFindings lists actionable findings from the check.
	FactualFindings []FactualFinding `json:"factual_findings,omitempty"`

	// OverallStatus is the pass/fail determination.
	OverallStatus FactualCheckStatus `json:"overall_status"`

	// ConfidenceLevel indicates the checker's confidence in the assessment.
	ConfidenceLevel ConfidenceLevel `json:"confidence_level"`

	// Summary is a human-readable summary of findings.
	Summary string `json:"summary,omitempty"`

	// Recommendations lists recommended actions.
	Recommendations []Recommendation `json:"recommendations,omitempty"`
}

// FactualCheckStatus indicates whether the artifact passed the factual check.
//
// These statuses determine whether the artifact can proceed to publication:
//   - Pass: All factual claims are verified and supported
//   - Fail: Critical factual issues detected that block publication
//   - Review: Non-critical issues detected; human review recommended
type FactualCheckStatus string

const (
	// FactualCheckStatusPass indicates all factual claims are verified and supported.
	// The artifact can proceed to publication.
	FactualCheckStatusPass FactualCheckStatus = "pass"

	// FactualCheckStatusFail indicates critical factual issues that block publication.
	// The artifact must be revised or discarded.
	FactualCheckStatusFail FactualCheckStatus = "fail"

	// FactualCheckStatusReview indicates non-critical issues detected.
	// Human review is recommended before publication.
	FactualCheckStatusReview FactualCheckStatus = "review"
)

// UnsupportedStatement represents a factual statement found without verification.
type UnsupportedStatement struct {
	// Location indicates where the unsupported statement was found.
	Location StatementLocation `json:"location"`

	// Statement is the unsupported statement text.
	Statement string `json:"statement"`

	// StatementType categorizes the type of unsupported statement.
	StatementType UnsupportedStatementType `json:"statement_type"`

	// Severity indicates the severity of this finding.
	Severity WarningSeverity `json:"severity"`

	// EvidenceMissing describes what evidence is needed.
	EvidenceMissing string `json:"evidence_missing,omitempty"`

	// SuggestedRevision provides suggested revision.
	SuggestedRevision string `json:"suggested_revision,omitempty"`

	// ClaimIDs are claim IDs that this statement might relate to.
	// This enables traceability when checking for unsupported statements.
	ClaimIDs []string `json:"claim_ids,omitempty"`
}

// StatementLocation indicates where a statement was found.
type StatementLocation struct {
	// LocationType is the type of location.
	LocationType LocationType `json:"location_type"`

	// Index is the 0-indexed position within its category.
	Index int `json:"index"`

	// SectionName is the section name (for section locations).
	SectionName string `json:"section_name,omitempty"`

	// PostIndex is the post index (for post locations).
	PostIndex int `json:"post_index,omitempty"`

	// BodyOffset is the byte offset in the body (for body locations).
	BodyOffset int `json:"body_offset,omitempty"`
}

// UnsupportedStatementType categorizes unsupported statements.
type UnsupportedStatementType string

const (
	// UnsupportedStatementNewFacts indicates entirely new facts not in verified claims.
	UnsupportedStatementNewFacts UnsupportedStatementType = "new_facts"

	// UnsupportedStatementInference indicates an inference not supported by verified claims.
	UnsupportedStatementInference UnsupportedStatementType = "inference"

	// UnsupportedStatementAssumption indicates an assumption presented as fact.
	UnsupportedStatementAssumption UnsupportedStatementType = "assumption"

	// UnsupportedStatementOpinion indicates opinion presented as fact.
	UnsupportedStatementOpinion UnsupportedStatementType = "opinion"

	// UnsupportedStatementConjecture indicates conjecture or speculation presented as fact.
	UnsupportedStatementConjecture UnsupportedStatementType = "conjecture"
)

// AlteredDetail represents a detail (number, date, name) that was altered from the source.
type AlteredDetail struct {
	// Location indicates where the altered detail was found.
	Location StatementLocation `json:"location"`

	// DetailType categorizes the type of altered detail.
	DetailType AlteredDetailType `json:"detail_type"`

	// ClaimIDs are the claim IDs associated with this detail.
	ClaimIDs []string `json:"claim_ids,omitempty"`

	// OriginalValue is the value in the source.
	OriginalValue string `json:"original_value"`

	// AlteredImageValue is the value in the artifact.
	AlteredImageValue string `json:"altered_value"`

	// Significance indicates how significant the alteration is.
	Significance AlterationSignificance `json:"significance"`

	// IsError indicates whether this is likely an unintentional error.
	IsError bool `json:"is_error"`

	// SuggestedRevision provides suggested correction.
	SuggestedRevision string `json:"suggested_revision,omitempty"`
}

// AlteredDetailType categorizes the type of altered detail.
type AlteredDetailType string

const (
	// AlteredDetailTypeNumber indicates a numeric value was altered.
	AlteredDetailTypeNumber AlteredDetailType = "number"

	// AlteredDetailTypeDate indicates a date was altered.
	AlteredDetailTypeDate AlteredDetailType = "date"

	// AlteredDetailTypeName indicates a name (person, organization, place) was altered.
	AlteredDetailTypeName AlteredDetailType = "name"

	// AlteredDetailTypePercentage indicates a percentage was altered.
	AlteredDetailTypePercentage AlteredDetailType = "percentage"

	// AlteredDetailTypeRatio indicates a ratio or fraction was altered.
	AlteredDetailTypeRatio AlteredDetailType = "ratio"
)

// AlterationSignificance indicates how significant an alteration is.
type AlterationSignificance string

const (
	// AlterationSignificanceTrivial indicates a minor, non-factual alteration.
	AlterationSignificanceTrivial AlterationSignificance = "trivial"

	// AlterationSignificanceModerate indicates a moderate factual alteration.
	AlterationSignificanceModerate AlterationSignificance = "moderate"

	// AlterationSignificanceCritical indicates a significant factual alteration.
	AlterationSignificanceCritical AlterationSignificance = "critical"
)

// OutOfContextStatement represents a statement found to be used out of context.
type OutOfContextStatement struct {
	// Location indicates where the out-of-context statement was found.
	Location StatementLocation `json:"location"`

	// Statement is the statement used out of context.
	Statement string `json:"statement"`

	// OriginalContext describes the original context in the source.
	OriginalContext string `json:"original_context"`

	// Misrepresentation describes how the context change affects meaning.
	Misrepresentation string `json:"misrepresentation"`

	// Severity indicates the severity of this finding.
	Severity WarningSeverity `json:"severity"`

	// SuggestedRevision provides suggested correction.
	SuggestedRevision string `json:"suggested_revision,omitempty"`
}

// FactualFinding represents a specific finding from the factual check.
type FactualFinding struct {
	// FindingID is a unique identifier for this finding.
	FindingID string `json:"finding_id"`

	// FindingType categorizes the type of finding.
	FindingType FindingType `json:"finding_type"`

	// Statement is the finding description.
	Statement string `json:"statement"`

	// Evidence is the evidence supporting this finding.
	Evidence string `json:"evidence,omitempty"`

	// Severity indicates the severity of this finding.
	Severity WarningSeverity `json:"severity"`

	// Category categorizes the finding.
	Category FindingCategory `json:"category"`

	// RelatedClaimIDs are claim IDs related to this finding.
	RelatedClaimIDs []string `json:"related_claim_ids,omitempty"`
}

// FindingType categorizes factual findings.
type FindingType string

const (
	// FindingTypeSupport indicates a positive finding (claim is well-supported).
	FindingTypeSupport FindingType = "support"

	// FindingTypeConcern indicates a concern that doesn't block publication.
	FindingTypeConcern FindingType = "concern"

	// FindingTypeError indicates a factual error that must be corrected.
	FindingTypeError FindingType = "error"
)

// FindingCategory categorizes findings by their nature.
type FindingCategory string

const (
	// FindingCategoryClaim indicates the finding relates to a specific claim.
	FindingCategoryClaim FindingCategory = "claim"

	// FindingCategoryEvidence indicates the finding relates to source evidence.
	FindingCategoryEvidence FindingCategory = "evidence"

	// FindingCategoryConsistency indicates the finding relates to internal consistency.
	FindingCategoryConsistency FindingCategory = "consistency"

	// FindingCategoryCitation indicates the finding relates to citations.
	FindingCategoryCitation FindingCategory = "citation"

	// FindingCategoryAccuracy indicates the finding relates to factual accuracy.
	FindingCategoryAccuracy FindingCategory = "accuracy"
)

// Recommendation represents an action to address a finding.
type Recommendation struct {
	// RecommendationID is a unique identifier for this recommendation.
	RecommendationID string `json:"recommendation_id"`

	// RecommendationType categorizes the type of recommendation.
	RecommendationType RecommendationType `json:"recommendation_type"`

	// Description describes the recommended action.
	Description string `json:"description"`

	// Priority indicates the priority of this recommendation.
	Priority RecommendationPriority `json:"priority"`

	// RelatedFindingIDs are finding IDs this recommendation addresses.
	RelatedFindingIDs []string `json:"related_finding_ids,omitempty"`

	// EffortEstimate provides effort estimate for the recommendation.
	EffortEstimate RecommendationEffort `json:"effort_estimate,omitempty"`
}

// RecommendationType categorizes recommendations.
type RecommendationType string

const (
	// RecommendationTypeFix indicates a factual correction is needed.
	RecommendationTypeFix RecommendationType = "fix"

	// RecommendationTypeRemove indicates content should be removed.
	RecommendationTypeRemove RecommendationType = "remove"

	// RecommendationTypeAdd indicates missing information should be added.
	RecommendationTypeAdd RecommendationType = "add"

	// RecommendationTypeClarify indicates ambiguity should be clarified.
	RecommendationTypeClarify RecommendationType = "clarify"

	// RecommendationTypeVerify indicates additional verification is needed.
	RecommendationTypeVerify RecommendationType = "verify"

	// RecommendationTypeReview indicates human review is needed.
	RecommendationTypeReview RecommendationType = "review"
)

// RecommendationPriority indicates the priority of a recommendation.
type RecommendationPriority string

const (
	// RecommendationPriorityLow is low priority.
	RecommendationPriorityLow RecommendationPriority = "low"

	// RecommendationPriorityMedium is medium priority.
	RecommendationPriorityMedium RecommendationPriority = "medium"

	// RecommendationPriorityHigh is high priority.
	RecommendationPriorityHigh RecommendationPriority = "high"

	// RecommendationPriorityCritical is critical priority.
	RecommendationPriorityCritical RecommendationPriority = "critical"
)

// RecommendationEffort estimates the effort required.
type RecommendationEffort string

const (
	// RecommendationEffortQuick is a quick fix (minutes).
	RecommendationEffortQuick RecommendationEffort = "quick"

	// RecommendationEffortModerate is moderate effort (hours).
	RecommendationEffortModerate RecommendationEffort = "moderate"

	// RecommendationEffortSubstantial is substantial effort (days).
	RecommendationEffortSubstantial RecommendationEffort = "substantial"

	// RecommendationEffortSignificant is significant effort (weeks).
	RecommendationEffortSignificant RecommendationEffort = "significant"
)

// Validate performs structural validation on FactualCheckResult.
func (f *FactualCheckResult) Validate() error {
	var errs []error

	// Validate claim IDs and verification status mapping consistency
	if len(f.ClaimIDs) != len(f.ClaimVerificationStatus) {
		errs = append(errs, fmt.Errorf("claim_ids (%d) and claim_verification_status (%d) have different lengths",
			len(f.ClaimIDs), len(f.ClaimVerificationStatus)))
	}

	// Verify all claim IDs exist in verification status
	for _, claimID := range f.ClaimIDs {
		if _, exists := f.ClaimVerificationStatus[claimID]; !exists {
			errs = append(errs, fmt.Errorf("claim_id %q in claim_ids has no verification status", claimID))
		}
	}

	// Validation of only verified claims being used should be done via warnings

	// Validate status is valid
	switch f.OverallStatus {
	case FactualCheckStatusPass, FactualCheckStatusFail, FactualCheckStatusReview:
		// Valid
	default:
		errs = append(errs, fmt.Errorf("invalid overall_status: %q", f.OverallStatus))
	}

	// Validate confidence level is valid
	switch f.ConfidenceLevel {
	case ConfidenceHigh, ConfidenceMedium, ConfidenceLow:
		// Valid
	default:
		errs = append(errs, fmt.Errorf("invalid confidence_level: %q", f.ConfidenceLevel))
	}

	// Validate unsupported statements
	for i, stmt := range f.UnsupportedStatements {
		switch stmt.StatementType {
		case UnsupportedStatementNewFacts, UnsupportedStatementInference,
			UnsupportedStatementAssumption, UnsupportedStatementOpinion,
			UnsupportedStatementConjecture:
			// Valid
		default:
			errs = append(errs, fmt.Errorf("unsupported_statement %d has invalid statement_type: %q", i, stmt.StatementType))
		}
	}

	// Validate altered details
	for i, detail := range f.AlteredDetails {
		switch detail.DetailType {
		case AlteredDetailTypeNumber, AlteredDetailTypeDate, AlteredDetailTypeName,
			AlteredDetailTypePercentage, AlteredDetailTypeRatio:
			// Valid
		default:
			errs = append(errs, fmt.Errorf("altered_detail %d has invalid detail_type: %q", i, detail.DetailType))
		}
		switch detail.Significance {
		case AlterationSignificanceTrivial, AlterationSignificanceModerate, AlterationSignificanceCritical:
			// Valid
		default:
			errs = append(errs, fmt.Errorf("altered_detail %d has invalid significance: %q", i, detail.Significance))
		}
	}

	// Validate factual findings
	for i, finding := range f.FactualFindings {
		switch finding.FindingType {
		case FindingTypeSupport, FindingTypeConcern, FindingTypeError:
			// Valid
		default:
			errs = append(errs, fmt.Errorf("factual_finding %d has invalid finding_type: %q", i, finding.FindingType))
		}
		switch finding.Category {
		case FindingCategoryClaim, FindingCategoryEvidence, FindingCategoryConsistency,
			FindingCategoryCitation, FindingCategoryAccuracy:
			// Valid
		default:
			errs = append(errs, fmt.Errorf("factual_finding %d has invalid category: %q", i, finding.Category))
		}
	}

	// Validate recommendations
	for i, rec := range f.Recommendations {
		switch rec.RecommendationType {
		case RecommendationTypeFix, RecommendationTypeRemove, RecommendationTypeAdd,
			RecommendationTypeClarify, RecommendationTypeVerify, RecommendationTypeReview:
			// Valid
		default:
			errs = append(errs, fmt.Errorf("recommendation %d has invalid recommendation_type: %q", i, rec.RecommendationType))
		}
		switch rec.Priority {
		case RecommendationPriorityLow, RecommendationPriorityMedium, RecommendationPriorityHigh, RecommendationPriorityCritical:
			// Valid
		default:
			errs = append(errs, fmt.Errorf("recommendation %d has invalid priority: %q", i, rec.Priority))
		}
	}

	// Cross-validation: If Pass, there should be no critical unsupported statements or errors
	if f.OverallStatus == FactualCheckStatusPass {
		for _, stmt := range f.UnsupportedStatements {
			if stmt.Severity == WarningSeverityCritical {
				errs = append(errs, fmt.Errorf("pass status but has critical unsupported statement at %v", stmt.Location))
			}
		}
		for _, finding := range f.FactualFindings {
			if finding.Severity == WarningSeverityCritical && finding.FindingType == FindingTypeError {
				errs = append(errs, fmt.Errorf("pass status but has critical error finding: %s", finding.Statement))
			}
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	return nil
}

// GetUnsupportedClaimIDs returns claim IDs associated with unsupported statements.
func (f *FactualCheckResult) GetUnsupportedClaimIDs() []string {
	seen := make(map[string]bool)
	var ids []string

	for _, stmt := range f.UnsupportedStatements {
		for _, claimID := range stmt.ClaimIDs {
			if !seen[claimID] {
				seen[claimID] = true
				ids = append(ids, claimID)
			}
		}
	}

	return ids
}

// GetCriticalIssues returns all critical-severity issues.
func (f *FactualCheckResult) GetCriticalIssues() []string {
	var issues []string

	for _, stmt := range f.UnsupportedStatements {
		if stmt.Severity == WarningSeverityCritical {
			issues = append(issues, fmt.Sprintf("Critical unsupported statement: %s", stmt.Statement))
		}
	}

	for _, detail := range f.AlteredDetails {
		if detail.Significance == AlterationSignificanceCritical {
			issues = append(issues, fmt.Sprintf("Critical altered detail: %s → %s", detail.OriginalValue, detail.AlteredImageValue))
		}
	}

	for _, finding := range f.FactualFindings {
		if finding.Severity == WarningSeverityCritical && finding.FindingType == FindingTypeError {
			issues = append(issues, fmt.Sprintf("Critical finding: %s", finding.Statement))
		}
	}

	return issues
}

// GetRecommendationsByPriority returns recommendations filtered by priority.
func (f *FactualCheckResult) GetRecommendationsByPriority(priority RecommendationPriority) []Recommendation {
	var result []Recommendation
	for _, rec := range f.Recommendations {
		if rec.Priority == priority {
			result = append(result, rec)
		}
	}
	return result
}

// GetCriticalRecommendations returns all critical or high priority recommendations.
func (f *FactualCheckResult) GetCriticalRecommendations() []Recommendation {
	var result []Recommendation
	for _, rec := range f.Recommendations {
		if rec.Priority == RecommendationPriorityCritical || rec.Priority == RecommendationPriorityHigh {
			result = append(result, rec)
		}
	}
	return result
}

// PassRequirements checks if the result meets pass criteria.
func (f *FactualCheckResult) PassRequirements() bool {
	// Check for no critical unsupported statements
	for _, stmt := range f.UnsupportedStatements {
		if stmt.Severity == WarningSeverityCritical || stmt.Severity == WarningSeverityHigh {
			return false
		}
	}

	// Check for no critical altered details
	for _, detail := range f.AlteredDetails {
		if detail.Significance == AlterationSignificanceCritical {
			return false
		}
	}

	// Check for no critical findings of type Error
	for _, finding := range f.FactualFindings {
		if finding.FindingType == FindingTypeError && finding.Severity == WarningSeverityCritical {
			return false
		}
	}

	return true
}

// DetermineStatus determines the overall status based on findings.
func (f *FactualCheckResult) DetermineStatus() FactualCheckStatus {
	if !f.PassRequirements() {
		return FactualCheckStatusFail
	}

	// Check for any warnings that suggest review
	hasMediumWarnings := false
	for _, stmt := range f.UnsupportedStatements {
		if stmt.Severity == WarningSeverityMedium {
			hasMediumWarnings = true
		}
	}
	for _, finding := range f.FactualFindings {
		if finding.Severity == WarningSeverityMedium {
			hasMediumWarnings = true
		}
	}

	if hasMediumWarnings {
		return FactualCheckStatusReview
	}

	return FactualCheckStatusPass
}

// SummaryText generates a human-readable summary.
func (f *FactualCheckResult) SummaryText() string {
	var summary string

	summary += fmt.Sprintf("Factual check of artifact %s completed.\n\n", f.InputArtifactID)

	summary += fmt.Sprintf("Overall status: %s\n", f.OverallStatus)
	summary += fmt.Sprintf("Confidence: %s\n\n", f.ConfidenceLevel)

	summary += fmt.Sprintf("Claims checked: %d\n", len(f.ClaimIDs))

	if len(f.UnsupportedStatements) > 0 {
		summary += fmt.Sprintf("Unsupported statements: %d\n", len(f.UnsupportedStatements))
	}

	if len(f.AlteredDetails) > 0 {
		summary += fmt.Sprintf("Altered details: %d\n", len(f.AlteredDetails))
	}

	if len(f.OutOfContextStatements) > 0 {
		summary += fmt.Sprintf("Out-of-context statements: %d\n", len(f.OutOfContextStatements))
	}

	if len(f.Recommendations) > 0 {
		summary += fmt.Sprintf("Recommendations: %d\n", len(f.Recommendations))
	}

	return summary
}
