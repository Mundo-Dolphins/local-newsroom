// Package contracts defines the generic v0.4 contracts used by the Verifier,
// Writer, and final factual-check stages.
//
// These contracts form the stable interfaces between pipeline stages:
//
//	Researcher → ResearchDossier → Verifier → VerificationResult
//	VerificationResult → Writer → EditorialArtifact
//	EditorialArtifact → FinalFactualCheck → FactualCheckResult
//
// Design principles:
//   - Format-neutral: No Bluesky/Hugo/rendering details
//   - Serializable: All types implement JSON serialization
//   - Explicit enums: Status fields use explicit enum types, not free-form strings
//   - Provenance preservation: Dossier claim IDs tracked through all stages
//   - Uncertainty support: Contradictory/uncertain findings explicitly representable
//   - Testability: Each stage's contracts enable independent unit testing
//
// This package is intentionally minimal and domain-agnostic.
package contracts

import (
	"errors"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/researcher"
)

// VerificationResult represents the output of the Verifier stage.
//
// The Verifier takes a ResearchDossier and produces a VerificationResult that:
// - Assesses each claim's verifiability status
// - Documents supporting and conflicting evidence
// - Tracks uncertainty and contradictions explicitly
// - Preserves provenance via stable claim IDs
//
// VerificationStatus enumerates how thoroughly a claim has been verified:
//   - Supported: Multiple reliable sources confirm the claim
//   - Contradicted: Evidence shows the claim is false or misleading
//   - Uncertain: Evidence is inconclusive or conflicting
//   - InsufficientEvidence: Claim could not be verified due to lack of evidence
//
// All fields are JSON-serializable. The result must pass Validate() before use.
type VerificationResult struct {
	// StableID uniquely identifies this verification run.
	// Should be deterministic given the input dossier and verifier parameters.
	StableID string `json:"stable_id"`

	// InputDossierID references the ResearchDossier.stable_id that was verified.
	// Enables traceability back to the original research.
	InputDossierID string `json:"input_dossier_id"`

	// VerifiedAt is the UTC timestamp when verification was completed.
	VerifiedAt time.Time `json:"verified_at"`

	// VerificationStatuses maps claim IDs to their verification status.
	// Key: claim.ID from ResearchDossier.Claims
	// Value: VerificationStatus enum
	VerificationStatuses map[string]VerificationStatus `json:"verification_statuses"`

	// ClaimDetails provides detailed verification findings for each claim.
	// Key: claim.ID from ResearchDossier.Claims
	ClaimDetails map[string]ClaimVerificationDetails `json:"claim_details"`

	// Contradictions documents contradictions discovered or confirmed during verification.
	// These may originate from the ResearchDossier or be newly identified.
	Contradictions []VerifiedContradiction `json:"contradictions,omitempty"`

	// VerificationNotes captures high-level observations from the verification process.
	VerificationNotes []VerificationNote `json:"verification_notes,omitempty"`

	// VerificationSummary is a human-readable summary of verification findings.
	VerificationSummary string `json:"verification_summary,omitempty"`

	// QualityScore is a numeric score (0-100) representing overall verification quality.
	// Higher scores indicate more claims are well-supported with reliable evidence.
	QualityScore float64 `json:"quality_score"`
}

// Validate performs structural validation on VerificationResult.
func (v *VerificationResult) Validate() error {
	var errs []error

	// Validate claim IDs are unique in VerificationStatuses
	statusClaimIDs := make(map[string]bool)
	for claimID := range v.VerificationStatuses {
		if statusClaimIDs[claimID] {
			errs = append(errs, fmt.Errorf("duplicate claim ID in verification_statuses: %q", claimID))
		}
		statusClaimIDs[claimID] = true
	}

	// Validate claim IDs are unique in ClaimDetails
	detailsClaimIDs := make(map[string]bool)
	for claimID := range v.ClaimDetails {
		if detailsClaimIDs[claimID] {
			errs = append(errs, fmt.Errorf("duplicate claim ID in claim_details: %q", claimID))
		}
		detailsClaimIDs[claimID] = true
	}

	// Validate VerificationStatuses and ClaimDetails use the same claim IDs
	if len(v.VerificationStatuses) != len(v.ClaimDetails) {
		errs = append(errs, fmt.Errorf("verification_statuses (%d) and claim_details (%d) have different claim counts",
			len(v.VerificationStatuses), len(v.ClaimDetails)))
	}

	for claimID := range v.VerificationStatuses {
		if _, exists := v.ClaimDetails[claimID]; !exists {
			errs = append(errs, fmt.Errorf("claim_id %q in verification_statuses has no corresponding claim_details", claimID))
		}
	}

	// Validate contradiction claim IDs
	contradictionClaimIDs := make(map[string]bool)
	for _, con := range v.Contradictions {
		if contradictionClaimIDs[con.ID] {
			errs = append(errs, fmt.Errorf("duplicate contradiction ID: %q", con.ID))
		}
		contradictionClaimIDs[con.ID] = true

		for _, claimID := range con.ClaimIDs {
			if !statusClaimIDs[claimID] && !detailsClaimIDs[claimID] {
				errs = append(errs, fmt.Errorf("contradiction %q references unknown claim %q", con.ID, claimID))
			}
		}
	}

	// Validate verification note claim references
	for _, note := range v.VerificationNotes {
		for _, claimID := range note.RelatedClaimIDs {
			if !statusClaimIDs[claimID] && !detailsClaimIDs[claimID] {
				errs = append(errs, fmt.Errorf("verification_note %q references unknown claim %q", note.ID, claimID))
			}
		}
	}

	// Validate quality score is in range
	if v.QualityScore < 0 || v.QualityScore > 100 {
		errs = append(errs, fmt.Errorf("quality_score %v is out of range [0, 100]", v.QualityScore))
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	return nil
}

// VerificationStatus indicates the verification outcome for a claim.
//
// These statuses are explicit and should not be free-form strings:
//
//   - Supported: Claim is confirmed by reliable evidence
//   - Contradicted: Evidence shows claim is false or misleading
//   - Uncertain: Evidence is inconclusive or conflicting
//   - InsufficientEvidence: Claim could not be verified
type VerificationStatus string

const (
	// VerificationStatusSupported indicates the claim is confirmed by
	// multiple reliable sources with consistent evidence.
	VerificationStatusSupported VerificationStatus = "supported"

	// VerificationStatusContradicted indicates the claim is shown to be
	// false, misleading, or contradicted by reliable evidence.
	VerificationStatusContradicted VerificationStatus = "contradicted"

	// VerificationStatusUncertain indicates the evidence is inconclusive,
	// conflicting, or of insufficient quality to make a determination.
	VerificationStatusUncertain VerificationStatus = "uncertain"

	// VerificationStatusInsufficientEvidence indicates the claim could not
	// be verified due to lack of accessible or reliable evidence.
	VerificationStatusInsufficientEvidence VerificationStatus = "insufficient_evidence"
)

// ClaimVerificationDetails contains detailed findings for a single verified claim.
type ClaimVerificationDetails struct {
	// ClaimID is the stable ID of the claim being verified.
	ClaimID string `json:"claim_id"`

	// Statement preserves the original claim statement for reference.
	Statement string `json:"statement"`

	// VerificationStatus is the verification outcome for this claim.
	VerificationStatus VerificationStatus `json:"verification_status"`

	// ConfidenceLevel indicates the verifier's confidence in the assessment.
	ConfidenceLevel ConfidenceLevel `json:"confidence_level,omitempty"`

	// SupportingEvidence lists source IDs and excerpts that support the claim.
	SupportingEvidence []EvidenceReference `json:"supporting_evidence,omitempty"`

	// ConflictingEvidence lists source IDs and excerpts that contradict the claim.
	ConflictingEvidence []EvidenceReference `json:"conflicting_evidence,omitempty"`

	// VerificationNotes capture the verifier's reasoning and methodology.
	VerificationNotes string `json:"verification_notes,omitempty"`

	// Limitations captures constraints that affected verification quality.
	Limitations string `json:"limitations,omitempty"`
}

// EvidenceReference links to evidence from a source.
type EvidenceReference struct {
	// SourceID is the stable ID of the source.
	SourceID string `json:"source_id"`

	// Excerpt is the specific evidence excerpt.
	Excerpt string `json:"excerpt"`

	// EvidenceContext explains how the excerpt relates to the claim.
	EvidenceContext string `json:"evidence_context,omitempty"`

	// OriginalClaimID (optional) references the ResearchDossier claim ID
	// if this evidence originated from a claim's evidence chain.
	OriginalClaimID string `json:"original_claim_id,omitempty"`
}

// VerifiedContradiction documents a contradiction between claims.
type VerifiedContradiction struct {
	// ID is a unique identifier for this contradiction record.
	ID string `json:"id"`

	// ClaimIDs are the claim IDs involved in this contradiction.
	ClaimIDs []string `json:"claim_ids"`

	// Description explains why this is a contradiction.
	Description string `json:"description"`

	// Severity indicates priority for resolution.
	Severity researcher.Severity `json:"severity,omitempty"`

	// ResolutionStatus indicates the current state of resolution.
	ResolutionStatus researcher.ResolutionStatus `json:"resolution_status,omitempty"`

	// ResolutionNotes capture any attempted or pending resolution.
	ResolutionNotes string `json:"resolution_notes,omitempty"`
}

// VerificationNote captures a high-level observation from the verification process.
type VerificationNote struct {
	// ID is a unique identifier for this note.
	ID string `json:"id"`

	// NoteType categorizes the type of note.
	NoteType VerificationNoteType `json:"note_type,omitempty"`

	// Content is the note content.
	Content string `json:"content"`

	// RelatedClaimIDs are claim IDs this note relates to.
	RelatedClaimIDs []string `json:"related_claim_ids,omitempty"`

	// CreatedAt is when this note was created (UTC).
	CreatedAt time.Time `json:"created_at"`
}

// VerificationNoteType categorizes verification notes.
type VerificationNoteType string

const (
	// VerificationNoteMethodology notes about the verification methodology.
	VerificationNoteMethodology VerificationNoteType = "methodology"

	// VerificationNoteFinding a significant finding discovered during verification.
	VerificationNoteFinding VerificationNoteType = "finding"

	// VerificationNoteWarning a warning or concern about verification quality.
	VerificationNoteWarning VerificationNoteType = "warning"

	// VerificationNoteTODO a verification task that needs follow-up.
	VerificationNoteTODO VerificationNoteType = "todo"
)

// ExtractClaimIDs returns all claim IDs that appear in this verification result.
func (v *VerificationResult) ExtractClaimIDs() []string {
	ids := make([]string, 0, len(v.VerificationStatuses))
	for claimID := range v.VerificationStatuses {
		ids = append(ids, claimID)
	}
	return ids
}

// GetSupportedClaims returns claims that have been verified as supported.
func (v *VerificationResult) GetSupportedClaims() []string {
	var result []string
	for claimID, status := range v.VerificationStatuses {
		if status == VerificationStatusSupported {
			result = append(result, claimID)
		}
	}
	return result
}

// GetContradictedClaims returns claims that have been verified as contradicted.
func (v *VerificationResult) GetContradictedClaims() []string {
	var result []string
	for claimID, status := range v.VerificationStatuses {
		if status == VerificationStatusContradicted {
			result = append(result, claimID)
		}
	}
	return result
}

// GetUncertainClaims returns claims with uncertain verification status.
func (v *VerificationResult) GetUncertainClaims() []string {
	var result []string
	for claimID, status := range v.VerificationStatuses {
		if status == VerificationStatusUncertain {
			result = append(result, claimID)
		}
	}
	return result
}

// CalculateQualityScore computes a quality score (0-100) based on verification results.
// Score weights:
//   - Supported claims: +20 points each (max 60)
//   - Uncertain claims: +5 points each (max 20)
//   - Contradicted claims: +5 points each (max 10)
//   - Insufficient evidence: 0 points
func (v *VerificationResult) CalculateQualityScore() float64 {
	if len(v.VerificationStatuses) == 0 {
		return 0
	}

	total := 0.0
	supportedCount := 0
	uncertainCount := 0
	contradictedCount := 0

	for _, status := range v.VerificationStatuses {
		switch status {
		case VerificationStatusSupported:
			supportedCount++
		case VerificationStatusUncertain:
			uncertainCount++
		case VerificationStatusContradicted:
			contradictedCount++
		}
	}

	// Supported claims are most valuable (up to 60 points)
	supportedScore := float64(supportedCount) / float64(len(v.VerificationStatuses)) * 60

	// Uncertain claims show some verification effort (up to 20 points)
	uncertainScore := float64(uncertainCount) / float64(len(v.VerificationStatuses)) * 20

	// Contradicted claims show active verification (up to 10 points)
	contradictedScore := float64(contradictedCount) / float64(len(v.VerificationStatuses)) * 10

	// Handle insufficient evidence (0 points contribution)
	insufficientCount := len(v.VerificationStatuses) - supportedCount - uncertainCount - contradictedCount
	if insufficientCount > 0 {
		insufficientPenalty := float64(insufficientCount) / float64(len(v.VerificationStatuses)) * 10
		total = supportedScore + uncertainScore + contradictedScore - insufficientPenalty
	} else {
		total = supportedScore + uncertainScore + contradictedScore
	}

	if total < 0 {
		total = 0
	}
	if total > 100 {
		total = 100
	}

	return total
}
