// Package verifier implements the Verifier stage that evaluates claims
// in a ResearchDossier against existing evidence.
//
// The Verifier:
// - Takes a validated ResearchDossier as input
// - Evaluates each claim using only existing evidence
// - Returns a VerificationResult with structured status assessments
// - Does not create new facts or modify the dossier
//
// The Verifier is intentionally separate from the Researcher to maintain
// clear stage boundaries in the pipeline.
package verifier

import (
	"fmt"
	"slices"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/researcher"
)

// ValidationRule represents a validation rule that can be applied to
// contracts.VerificationResult during post-processing.
type ValidationRule func(v *contracts.VerificationResult) error

// ValidationChain is a sequence of validation rules that are applied
// in order to a contracts.VerificationResult.
type ValidationChain []ValidationRule

// Add appends a validation rule to the chain.
func (c *ValidationChain) Add(rule ValidationRule) {
	*c = append(*c, rule)
}

// Validate applies all rules in the chain to the given result.
func (c ValidationChain) Validate(v *contracts.VerificationResult) error {
	for i, rule := range c {
		if err := rule(v); err != nil {
			return fmt.Errorf("rule[%d]: %w", i, err)
		}
	}
	return nil
}

// NewValidationChain creates a default validation chain with all standard
// validation rules for contracts.VerificationResult.
func NewValidationChain() ValidationChain {
	var chain ValidationChain
	chain.Add(ValidateNoDuplicateClaims)
	chain.Add(ValidateValidStatusValues)
	chain.Add(ValidateValidConfidenceValues)
	chain.Add(ValidateQualityScoreRange)
	chain.Add(ValidateClaimDetailsConsistency)
	return chain
}

// ValidateNoDuplicateClaims ensures no claim IDs appear more than once
// in verification_statuses or claim_details.
func ValidateNoDuplicateClaims(v *contracts.VerificationResult) error {
	seenStatus := make(map[string]bool)
	seenDetails := make(map[string]bool)

	for claimID := range v.VerificationStatuses {
		if seenStatus[claimID] {
			return fmt.Errorf("duplicate claim ID in verification_statuses: %q", claimID)
		}
		seenStatus[claimID] = true
	}

	for claimID := range v.ClaimDetails {
		if seenDetails[claimID] {
			return fmt.Errorf("duplicate claim ID in claim_details: %q", claimID)
		}
		seenDetails[claimID] = true
	}

	// Verify the sets are identical
	if len(seenStatus) != len(seenDetails) {
		return fmt.Errorf("verification_statuses and claim_details have different claim counts: %d vs %d",
			len(seenStatus), len(seenDetails))
	}

	for claimID := range seenStatus {
		if !seenDetails[claimID] {
			return fmt.Errorf("claim_id %q in verification_statuses has no corresponding claim_details", claimID)
		}
	}

	return nil
}

// ValidateValidStatusValues ensures all verification_statuses values
// are valid enum values from contracts.VerificationStatus.
func ValidateValidStatusValues(v *contracts.VerificationResult) error {
	validStatuses := map[contracts.VerificationStatus]bool{
		contracts.VerificationStatusSupported:            true,
		contracts.VerificationStatusContradicted:         true,
		contracts.VerificationStatusUncertain:            true,
		contracts.VerificationStatusInsufficientEvidence: true,
	}

	for claimID, status := range v.VerificationStatuses {
		if !validStatuses[status] {
			return fmt.Errorf("invalid verification_status for claim %q: %q", claimID, status)
		}
	}

	return nil
}

// ValidateValidConfidenceValues ensures all confidence_level values
// are valid enum values from contracts.ConfidenceLevel.
func ValidateValidConfidenceValues(v *contracts.VerificationResult) error {
	validConfidences := map[contracts.ConfidenceLevel]bool{
		contracts.ConfidenceHigh:   true,
		contracts.ConfidenceMedium: true,
		contracts.ConfidenceLow:    true,
	}

	for claimID, details := range v.ClaimDetails {
		if details.ConfidenceLevel != "" && !validConfidences[details.ConfidenceLevel] {
			return fmt.Errorf("invalid confidence_level for claim %q: %q", claimID, details.ConfidenceLevel)
		}
	}

	return nil
}

// ValidateQualityScoreRange ensures quality_score is between 0 and 100.
func ValidateQualityScoreRange(v *contracts.VerificationResult) error {
	if v.QualityScore < 0 || v.QualityScore > 100 {
		return fmt.Errorf("quality_score %v is out of range [0, 100]", v.QualityScore)
	}
	return nil
}

// ValidateClaimDetailsConsistency ensures claim_details entries match
// the corresponding verification_statuses entries.
func ValidateClaimDetailsConsistency(v *contracts.VerificationResult) error {
	for claimID, status := range v.VerificationStatuses {
		details, exists := v.ClaimDetails[claimID]
		if !exists {
			return fmt.Errorf("claim_id %q in verification_statuses missing from claim_details", claimID)
		}
		if details.VerificationStatus != status {
			return fmt.Errorf("claim_id %q: verification_status %q in statuses does not match %q in claim_details",
				claimID, status, details.VerificationStatus)
		}
	}
	return nil
}

// ValidateResult is a convenience wrapper that applies all standard
// validation rules to a contracts.VerificationResult.
func ValidateResult(v *contracts.VerificationResult) error {
	return NewValidationChain().Validate(v)
}

// Input represents the complete input data for verification.
// It contains the ResearchDossier and verification parameters.
type Input struct {
	// Dossier is the validated ResearchDossier to verify.
	Dossier *researcher.ResearchDossier

	// Parameters are the verification configuration.
	Parameters VerificationParameters
}

// VerificationParameters holds configuration for a verification run.
type VerificationParameters struct {
	// VerifierID is a unique identifier for this verification run.
	VerifierID string

	// VerificationDate is when the verification was performed.
	VerificationDate time.Time

	// QualityThreshold is the minimum quality score required for
	// downstream processing (0-100). Used for decision-making,
	// not for the VerificationResult itself.
	QualityThreshold float64
}

// EvidenceIndex provides fast lookup of evidence by source ID.
type EvidenceIndex struct {
	// BySource maps source ID to slice of EvidenceEntry.
	BySource map[string][]EvidenceEntry

	// AllClaims is a slice of all claim IDs that have evidence.
	AllClaims []string
}

// EvidenceEntry represents a single piece of evidence.
type EvidenceEntry struct {
	// ClaimID is the claim this evidence supports.
	ClaimID string

	// SourceID is the source providing the evidence.
	SourceID string

	// Excerpts are the actual excerpt texts.
	Excerpts []string

	// Context explains how this evidence relates to the claim.
	Context string
}

// BuildEvidenceIndex constructs an index from a ResearchDossier's claims
// and evidence for efficient lookup during verification.
func BuildEvidenceIndex(dossier *researcher.ResearchDossier) *EvidenceIndex {
	idx := &EvidenceIndex{
		BySource:  make(map[string][]EvidenceEntry),
		AllClaims: make([]string, 0, len(dossier.Claims)),
	}

	for _, claim := range dossier.Claims {
		for _, evidence := range claim.Evidence {
			entry := EvidenceEntry{
				ClaimID:  claim.ID,
				SourceID: evidence.SourceID,
				Excerpts: make([]string, len(excerptsToText(evidence.Excerpts))),
				Context:  evidence.ClaimContext,
			}
			copy(entry.Excerpts, excerptsToText(evidence.Excerpts))
			idx.BySource[evidence.SourceID] = append(idx.BySource[evidence.SourceID], entry)
		}
		if len(claim.Evidence) > 0 {
			idx.AllClaims = append(idx.AllClaims, claim.ID)
		}
	}

	return idx
}

// excerptsToText extracts the text field from all excerpts.
func excerptsToText(excerpts []researcher.Excerpt) []string {
	result := make([]string, len(excerpts))
	for i, e := range excerpts {
		result[i] = e.Text
	}
	return result
}

// FindEvidenceForClaim returns all evidence entries for a specific claim.
func (e *EvidenceIndex) FindEvidenceForClaim(claimID string) []EvidenceEntry {
	var result []EvidenceEntry
	for _, entries := range e.BySource {
		for _, entry := range entries {
			if entry.ClaimID == claimID {
				result = append(result, entry)
			}
		}
	}
	return result
}

// GetClaimIDsFromVerification extracts claim IDs from a VerificationResult.
func GetClaimIDsFromVerification(result *contracts.VerificationResult) []string {
	var ids []string
	for claimID := range result.VerificationStatuses {
		ids = append(ids, claimID)
	}
	return ids
}

// GetSourceIDsFromVerification extracts source IDs from a VerificationResult.
func GetSourceIDsFromVerification(result *contracts.VerificationResult) []string {
	seen := make(map[string]bool)
	var ids []string

	for _, details := range result.ClaimDetails {
		for _, e := range details.SupportingEvidence {
			if !seen[e.SourceID] {
				seen[e.SourceID] = true
				ids = append(ids, e.SourceID)
			}
		}
		for _, e := range details.ConflictingEvidence {
			if !seen[e.SourceID] {
				seen[e.SourceID] = true
				ids = append(ids, e.SourceID)
			}
		}
	}

	return ids
}

// AreClaimIDsSubset checks if claimIDs are all contained in allowedIDs.
func AreClaimIDsSubset(claimIDs, allowedIDs []string) bool {
	allowedMap := make(map[string]bool)
	for _, id := range allowedIDs {
		allowedMap[id] = true
	}

	for _, id := range claimIDs {
		if !allowedMap[id] {
			return false
		}
	}
	return true
}

// AreClaimIDsEqual checks if two claim ID slices contain the same IDs.
func AreClaimIDsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	return slices.ContainsFunc(a, func(id string) bool {
		return slices.Contains(b, id)
	})
}

// ClaimIDsFromDossier returns all claim IDs from a ResearchDossier.
func ClaimIDsFromDossier(dossier *researcher.ResearchDossier) []string {
	result := make([]string, len(dossier.Claims))
	for i, c := range dossier.Claims {
		result[i] = c.ID
	}
	return result
}

// SourceIDsFromDossier returns all source IDs from a ResearchDossier.
func SourceIDsFromDossier(dossier *researcher.ResearchDossier) []string {
	result := make([]string, len(dossier.Sources))
	for i, s := range dossier.Sources {
		result[i] = s.StableID
	}
	return result
}

// FindClaimInDossier returns the claim with the given ID, or nil if not found.
func FindClaimInDossier(dossier *researcher.ResearchDossier, claimID string) *researcher.Claim {
	for i := range dossier.Claims {
		if dossier.Claims[i].ID == claimID {
			return &dossier.Claims[i]
		}
	}
	return nil
}

// FindSourceInDossier returns the source with the given ID, or nil if not found.
func FindSourceInDossier(dossier *researcher.ResearchDossier, sourceID string) *researcher.SourceReference {
	for i := range dossier.Sources {
		if dossier.Sources[i].StableID == sourceID {
			return &dossier.Sources[i]
		}
	}
	return nil
}

// FindContradictionInDossier returns the contradiction with the given ID, or nil if not found.
func FindContradictionInDossier(dossier *researcher.ResearchDossier, contradictionID string) *researcher.Contradiction {
	for i := range dossier.Contradictions {
		if dossier.Contradictions[i].ID == contradictionID {
			return &dossier.Contradictions[i]
		}
	}
	return nil
}

// ClaimIDsInContradictions returns claim IDs that are referenced in the
// contradictions array.
func ClaimIDsInContradictions(dossier *researcher.ResearchDossier) []string {
	seen := make(map[string]bool)
	var result []string
	for _, c := range dossier.Contradictions {
		for _, claimID := range c.ClaimIDs {
			if !seen[claimID] {
				seen[claimID] = true
				result = append(result, claimID)
			}
		}
	}
	return result
}

// GetClaimIDsNotInDossier finds claim IDs that appear in a reference
// (e.g., verification result) but not in the dossier.
func GetClaimIDsNotInDossier(claimIDs []string, dossier *researcher.ResearchDossier) []string {
	dossierIDs := make(map[string]bool)
	for _, c := range dossier.Claims {
		dossierIDs[c.ID] = true
	}

	var missing []string
	for _, id := range claimIDs {
		if !dossierIDs[id] {
			missing = append(missing, id)
		}
	}

	return missing
}

// GetSourceIDsNotInDossier finds source IDs that appear in a reference
// but not in the dossier.
func GetSourceIDsNotInDossier(sourceIDs []string, dossier *researcher.ResearchDossier) []string {
	dossierIDs := make(map[string]bool)
	for _, s := range dossier.Sources {
		dossierIDs[s.StableID] = true
	}

	var missing []string
	for _, id := range sourceIDs {
		if !dossierIDs[id] {
			missing = append(missing, id)
		}
	}

	return missing
}
