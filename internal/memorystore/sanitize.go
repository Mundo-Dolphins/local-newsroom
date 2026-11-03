package memorystore

import (
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/memory"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// Sanitizer is the secret-detection gate that records must pass before they
// are persisted.
//
// The production implementation (ProductionSanitizer) delegates to the
// run.Sanitizer redaction boundary: the same detectors the workspace store
// uses (exact values + pattern detectors) are applied to every text region
// of a record, with a fail-closed gate afterwards.
type Sanitizer interface {
	// Check inspects (and, for the free-form value payload, redacts) the
	// record's text regions.
	//
	// It returns a SecretDetectedError naming the first region that still
	// contains a detected secret after redaction, or nil when the record is
	// clean. Implementations must never include the secret itself in the
	// returned error.
	Check(rec *memory.MemoryRecord) error
}

// ProductionSanitizer is the default Sanitizer: it binds the run package's
// redaction boundary (exact values + pattern detectors) to the record's text
// regions.
//
// A zero-value ProductionSanitizer is valid: it performs pattern-only
// detection (no exact values).
type ProductionSanitizer struct {
	inner *run.Sanitizer
}

// NewProductionSanitizer builds a ProductionSanitizer with the given exact
// secret values (typically the resolved API keys).
func NewProductionSanitizer(exactValues ...string) *ProductionSanitizer {
	return &ProductionSanitizer{inner: run.NewSanitizer(exactValues...)}
}

// base returns the underlying run.Sanitizer (nil-safe).
func (s *ProductionSanitizer) base() *run.Sanitizer {
	if s == nil {
		return &run.Sanitizer{}
	}
	return s.inner
}

// Check implements Sanitizer.
//
// The free-form value payload is redacted in place (redaction is idempotent
// and the sanitized copy is a faithful copy of the record); every other
// region is gate-only: a detected secret there refuses the whole record
// fail-closed rather than silently rewriting the document. A region that is
// not clean after redaction produces a SecretDetectedError naming the
// region — never the secret.
func (s *ProductionSanitizer) Check(rec *memory.MemoryRecord) error {
	if rec == nil {
		return nil
	}
	sz := s.base()

	// Value payload: redact field by field, then gate the re-encoded form.
	if err := s.redactValue(rec, sz); err != nil {
		return err
	}
	if !sz.CleanText(string(rec.Value)) {
		return SecretDetectedError{ID: rec.StableID, Field: "value"}
	}
	for _, region := range []struct {
		name string
		text *string
	}{
		{"field", &rec.Field},
		{"provenance.verified_by", &rec.Provenance.VerifiedBy},
		{"provenance.corrected_by", &rec.Provenance.CorrectedBy},
		{"expiration.reason", &rec.Expiration.InvalidationReason},
		{"expiration.superseded_by", &rec.Expiration.SupersededBy},
	} {
		if !sz.CleanText(*region.text) {
			return SecretDetectedError{ID: rec.StableID, Field: region.name}
		}
	}
	for i := range rec.Provenance.Sources {
		src := &rec.Provenance.Sources[i]
		for _, region := range []struct {
			name string
			text *string
		}{
			{"provenance.sources.source_id", &src.SourceID},
			{"provenance.sources.title", &src.Title},
			{"provenance.sources.note", &src.Note},
		} {
			if !sz.CleanText(*region.text) {
				return SecretDetectedError{ID: rec.StableID, Field: region.name + "[" + itoa(i) + "]"}
			}
		}
	}
	return nil
}

// redactValue applies the redaction boundary to every string region of the
// record's kind-typed value payload, in place.
//
// Redaction is a no-op for clean content, so this step is harmless for the
// common case; it is what makes the persistence boundary consistent with the
// workspace store (redact, then gate).
func (s *ProductionSanitizer) redactValue(rec *memory.MemoryRecord, sz *run.Sanitizer) error {
	v, err := rec.DecodeValue()
	if err != nil {
		// The invalid-record gate rejects this record anyway; treat an
		// undecodable payload as raw text.
		redacted, _ := sz.Sanitize(string(rec.Value))
		rec.Value = []byte(redacted)
		return nil
	}
	switch val := v.(type) {
	case memory.FactualValue:
		val.Statement, _ = sz.Sanitize(val.Statement)
		for k := range val.Data {
			val.Data[k], _ = sz.Sanitize(val.Data[k])
		}
		return rec.EncodeValue(val, rec.UpdatedAt)
	case memory.EntityValue:
		for k := range val.Fields {
			val.Fields[k], _ = sz.Sanitize(val.Fields[k])
		}
		for i := range val.Relationships {
			val.Relationships[i].Note, _ = sz.Sanitize(val.Relationships[i].Note)
		}
		return rec.EncodeValue(val, rec.UpdatedAt)
	case memory.FeedbackRecord:
		val.Feedback, _ = sz.Sanitize(val.Feedback)
		for k := range val.Preference {
			val.Preference[k], _ = sz.Sanitize(val.Preference[k])
		}
		return rec.EncodeValue(val, rec.UpdatedAt)
	case memory.CorrectionRecord:
		val.OriginalClaim, _ = sz.Sanitize(val.OriginalClaim)
		val.CorrectedClaim, _ = sz.Sanitize(val.CorrectedClaim)
		val.Reason, _ = sz.Sanitize(val.Reason)
		return rec.EncodeValue(val, rec.UpdatedAt)
	case memory.SuggestionRecord:
		val.Proposal, _ = sz.Sanitize(val.Proposal)
		for k := range val.ProposedChange {
			val.ProposedChange[k], _ = sz.Sanitize(val.ProposedChange[k])
		}
		return rec.EncodeValue(val, rec.UpdatedAt)
	case memory.RecipeExecutionRecord:
		for i := range val.Lessons {
			val.Lessons[i], _ = sz.Sanitize(val.Lessons[i])
		}
		for i := range val.Failures {
			val.Failures[i].Message, _ = sz.Sanitize(val.Failures[i].Message)
		}
		return rec.EncodeValue(val, rec.UpdatedAt)
	}
	return fmt.Errorf("unhandled memory value type %T", v)
}

var _ = time.Now
var _ Sanitizer = (*ProductionSanitizer)(nil)
