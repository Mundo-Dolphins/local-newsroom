package memory

import (
	"fmt"
	"strings"
	"time"
)

// ConfidenceLevel indicates how strongly a memory record is supported by its
// provenance.
//
// This is deliberately distinct from the verification confidence in
// internal/contracts: memory confidence measures support for *contextual
// knowledge*, and it never upgrades a record into verified evidence.
type ConfidenceLevel string

const (
	// ConfidenceHigh: multiple independent sources, or a direct human
	// statement, support the record.
	ConfidenceHigh ConfidenceLevel = "high"

	// ConfidenceMedium: a single reasonably reliable source, or corroborated
	// indirect evidence.
	ConfidenceMedium ConfidenceLevel = "medium"

	// ConfidenceLow: weak, single, or possibly circular evidence.
	ConfidenceLow ConfidenceLevel = "low"
)

// AllConfidenceLevels lists every recognized ConfidenceLevel.
var AllConfidenceLevels = []ConfidenceLevel{
	ConfidenceLow,
	ConfidenceMedium,
	ConfidenceHigh,
}

// ParseConfidenceLevel converts a string into a ConfidenceLevel.
func ParseConfidenceLevel(s string) (ConfidenceLevel, error) {
	c := ConfidenceLevel(strings.TrimSpace(s))
	for _, valid := range AllConfidenceLevels {
		if c == valid {
			return c, nil
		}
	}
	return "", fmt.Errorf("unknown confidence level %q", s)
}

// Provenance answers the questions every factual memory must be able to
// answer:
//
//   - where did this come from?       (Sources + DerivedFrom)
//   - when was it last verified?      (Freshness.LastVerifiedAt)
//   - what source type supported it?  (Sources[*].SourceType)
//   - was it user-corrected?          (UserCorrected + CorrectedBy)
type Provenance struct {
	// Sources lists the sources the record is based on. Factual records
	// require at least one source; other kinds may carry zero or more.
	Sources []MemorySource `json:"sources,omitempty"`

	// DerivedFrom lists the stable IDs of records or run artifacts from which
	// this record was distilled (e.g. a suggestion derived from a correction).
	DerivedFrom []string `json:"derived_from,omitempty"`

	// VerifiedBy is the identifier of the verification process or component
	// that last confirmed the record (e.g. a verifier run ID).
	VerifiedBy string `json:"verified_by,omitempty"`

	// UserCorrected is true when a human corrected the record's content.
	UserCorrected bool `json:"user_corrected,omitempty"`

	// CorrectedBy is the identifier of the person who corrected the record,
	// set when UserCorrected is true.
	CorrectedBy string `json:"corrected_by,omitempty"`

	// CorrectedAt is when the human correction was made, set when
	// UserCorrected is true.
	CorrectedAt time.Time `json:"corrected_at,omitempty"`
}

// Validate checks provenance invariants.
func (p Provenance) Validate() error {
	for i, s := range p.Sources {
		if err := s.Validate(); err != nil {
			return fmt.Errorf("provenance: source[%d]: %w", i, err)
		}
	}
	if p.UserCorrected {
		if p.CorrectedBy == "" {
			return fmt.Errorf("provenance: corrected_by is required when user_corrected is true")
		}
		if p.CorrectedAt.IsZero() {
			return fmt.Errorf("provenance: corrected_at is required when user_corrected is true")
		}
	}
	return nil
}
