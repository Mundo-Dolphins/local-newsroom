package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// MemoryStableID is a stable, storage-neutral identifier for a memory record.
//
// The prefix convention is "mem-". The rest of the ID is derived by the
// persistence layer; the contract only requires it to be non-empty and
// stable across re-derivations of the same record content.
type MemoryStableID string

// MemoryStableIDPrefix is the canonical prefix for memory record IDs.
const MemoryStableIDPrefix = "mem-"

// MemoryRecord is the generic envelope for every editorial memory record.
//
// A MemoryRecord is contextual knowledge, not automatically factual
// evidence. The Value field is kind-typed: for KindFactual it is a
// FactualValue, and the record-level invariants (Validate) enforce that
// factual records always carry provenance. All other kinds carry their own
// specific value structures.
type MemoryRecord struct {
	// SchemaVersion is the schema version of this document (see SchemaVersion).
	SchemaVersion string `json:"schema_version"`

	// StableID uniquely identifies the record.
	StableID MemoryStableID `json:"stable_id"`

	// Kind is the memory category (see MemoryKind).
	Kind MemoryKind `json:"kind"`

	// Scope is where the record applies (see MemoryScope).
	Scope MemoryScope `json:"scope"`

	// Subject is the entity the record is about, if any.
	Subject EntityRef `json:"subject,omitempty"`

	// Field is an optional sub-field name within the subject or scope,
	// e.g. "current_team" or "injury_status". It lets consumers distinguish
	// records about the same entity without encoding field names into the ID.
	Field string `json:"field,omitempty"`

	// Value is the kind-typed payload:
	//   - KindFactual: FactualValue
	//   - KindEditorialFeedback: FeedbackRecord
	//   - KindCorrection: CorrectionRecord
	//   - KindSuggestion: SuggestionRecord
	//   - KindRecipeExecution: RecipeExecutionRecord
	//   - KindEntity: EntityValue
	//
	// It is stored as json.RawMessage so records of every kind share one
	// document shape; decode with DecodeValue for the kind's Go type.
	Value json.RawMessage `json:"value"`

	// Provenance answers where the record came from, when it was last
	// verified, what supported it, and whether a user corrected it.
	Provenance Provenance `json:"provenance"`

	// Confidence in the record, per ConfidenceLevel.
	Confidence ConfidenceLevel `json:"confidence"`

	// Volatility is how fast the record is expected to change.
	Volatility Volatility `json:"volatility"`

	// Freshness tracks verification currency.
	Freshness Freshness `json:"freshness"`

	// Expiration tracks expiry and invalidation. Zero-valued fields mean the
	// record is active with no absolute expiry.
	Expiration Expiration `json:"expiration"`

	// CreatedAt is when the record was first written (UTC).
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt is when the record was last modified (UTC).
	UpdatedAt time.Time `json:"updated_at"`
}

// ErrKindValueMismatch indicates that a record's Value does not match what
// its Kind requires.
var ErrKindValueMismatch = errors.New("memory record value type does not match its kind")

// NewMemoryRecord constructs a MemoryRecord with schema version, zero
// freshness/expiration defaults, and consistent timestamps.
//
// Callers must populate StableID, Kind, Scope, and Value before use;
// Validate enforces the rest.
func NewMemoryRecord(now time.Time) *MemoryRecord {
	now = TimeToUTC(now)
	return &MemoryRecord{
		SchemaVersion: SchemaVersion,
		CreatedAt:     now,
		UpdatedAt:     now,
		Expiration:    Expiration{InvalidationStatus: InvalidationActive},
	}
}

// DecodeValue decodes the record's Value into the kind's concrete Go type.
//
// It returns ErrKindValueMismatch if the kind is unrecognized or if the
// payload cannot be decoded. Decoding never mutates the record.
func (m *MemoryRecord) DecodeValue() (any, error) {
	if m == nil {
		return nil, fmt.Errorf("nil memory record")
	}
	switch m.Kind {
	case KindFactual:
		var v FactualValue
		if err := json.Unmarshal(m.Value, &v); err != nil {
			return nil, fmt.Errorf("%w: factual: %v", ErrKindValueMismatch, err)
		}
		return v, nil
	case KindEditorialFeedback:
		var v FeedbackRecord
		if err := json.Unmarshal(m.Value, &v); err != nil {
			return nil, fmt.Errorf("%w: editorial_feedback: %v", ErrKindValueMismatch, err)
		}
		return v, nil
	case KindCorrection:
		var v CorrectionRecord
		if err := json.Unmarshal(m.Value, &v); err != nil {
			return nil, fmt.Errorf("%w: correction: %v", ErrKindValueMismatch, err)
		}
		return v, nil
	case KindSuggestion:
		var v SuggestionRecord
		if err := json.Unmarshal(m.Value, &v); err != nil {
			return nil, fmt.Errorf("%w: suggestion: %v", ErrKindValueMismatch, err)
		}
		return v, nil
	case KindRecipeExecution:
		var v RecipeExecutionRecord
		if err := json.Unmarshal(m.Value, &v); err != nil {
			return nil, fmt.Errorf("%w: recipe_execution: %v", ErrKindValueMismatch, err)
		}
		return v, nil
	case KindEntity:
		var v EntityValue
		if err := json.Unmarshal(m.Value, &v); err != nil {
			return nil, fmt.Errorf("%w: entity: %v", ErrKindValueMismatch, err)
		}
		return v, nil
	default:
		return nil, fmt.Errorf("%w: unknown kind %q", ErrKindValueMismatch, m.Kind)
	}
}

// EncodeValue marshals the kind's concrete Go type back into the record's
// Value field, updating UpdatedAt to now.
//
// v must be one of the kind's allowed types; a type/kind mismatch returns
// ErrKindValueMismatch and leaves the record unchanged.
func (m *MemoryRecord) EncodeValue(v any, now time.Time) error {
	if m == nil {
		return fmt.Errorf("nil memory record")
	}
	var ok bool
	switch v.(type) {
	case FactualValue:
		ok = m.Kind == KindFactual
	case FeedbackRecord:
		ok = m.Kind == KindEditorialFeedback
	case CorrectionRecord:
		ok = m.Kind == KindCorrection
	case SuggestionRecord:
		ok = m.Kind == KindSuggestion
	case RecipeExecutionRecord:
		ok = m.Kind == KindRecipeExecution
	case EntityValue:
		ok = m.Kind == KindEntity
	default:
		return fmt.Errorf("%w: unsupported value type %T", ErrKindValueMismatch, v)
	}
	if !ok {
		return fmt.Errorf("%w: kind %q does not accept %T", ErrKindValueMismatch, m.Kind, v)
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode memory value: %w", err)
	}
	m.Value = data
	m.UpdatedAt = TimeToUTC(now)
	return nil
}

// Validate enforces the record's invariants:
//
//   - schema version is a semantic version understood by this package;
//   - stable ID is non-empty and carries the mem- prefix;
//   - kind, scope, confidence, and volatility are recognized enums;
//   - every kind has a decodable Value of the right type;
//   - factual records carry at least one provenance source and a freshness
//     stamp;
//   - provenance invariants hold;
//   - timestamps are present and ordered.
func (m *MemoryRecord) Validate() error {
	if m == nil {
		return fmt.Errorf("memory record is nil")
	}
	var errs []error

	if m.SchemaVersion == "" {
		errs = append(errs, fmt.Errorf("schema_version is required"))
	} else {
		major, err := SchemaMajor(m.SchemaVersion)
		if err != nil {
			errs = append(errs, fmt.Errorf("schema_version %q is not a semantic version: %v", m.SchemaVersion, err))
		} else if major != SchemaMajorCurrent {
			errs = append(errs, fmt.Errorf("%w: record is %s, this version understands major %d", ErrUnsupportedSchemaVersion, m.SchemaVersion, SchemaMajorCurrent))
		}
	}

	if string(m.StableID) == "" {
		errs = append(errs, fmt.Errorf("stable_id is required"))
	} else if len(string(m.StableID)) < len(MemoryStableIDPrefix) || string(m.StableID)[:len(MemoryStableIDPrefix)] != MemoryStableIDPrefix {
		errs = append(errs, fmt.Errorf("stable_id %q must have prefix %q", m.StableID, MemoryStableIDPrefix))
	}

	if _, err := ParseMemoryKind(string(m.Kind)); err != nil {
		errs = append(errs, err)
	}
	if _, err := ParseMemoryScope(string(m.Scope)); err != nil {
		errs = append(errs, err)
	}
	if _, err := ParseConfidenceLevel(string(m.Confidence)); err != nil {
		errs = append(errs, err)
	}
	if _, err := ParseVolatility(string(m.Volatility)); err != nil {
		errs = append(errs, err)
	}

	if m.Subject.Kind != "" || m.Subject.Name != "" {
		if err := m.Subject.Validate(); err != nil {
			errs = append(errs, err)
		}
	}

	if len(m.Value) == 0 {
		errs = append(errs, fmt.Errorf("value is required"))
	} else {
		if _, err := m.DecodeValue(); err != nil {
			errs = append(errs, err)
		}
	}

	if err := m.Provenance.Validate(); err != nil {
		errs = append(errs, err)
	}

	// Factual records must be self-contained enough to be answered by
	// provenance questions: at least one source and a last-verification stamp.
	if m.Kind == KindFactual {
		if len(m.Provenance.Sources) == 0 {
			errs = append(errs, fmt.Errorf("factual memory requires at least one provenance source"))
		}
		if m.Freshness.LastVerifiedAt.IsZero() {
			errs = append(errs, fmt.Errorf("factual memory requires freshness.last_verified_at"))
		}
	}

	if m.Expiration.InvalidationStatus == "" {
		errs = append(errs, fmt.Errorf("expiration.invalidation_status is required"))
	} else if _, err := ParseInvalidationStatus(string(m.Expiration.InvalidationStatus)); err != nil {
		errs = append(errs, err)
	}
	if m.Expiration.InvalidationStatus == InvalidationSuperseded && m.Expiration.SupersededBy == "" {
		errs = append(errs, fmt.Errorf("superseded records require expiration.superseded_by"))
	}

	if m.CreatedAt.IsZero() {
		errs = append(errs, fmt.Errorf("created_at is required"))
	}
	if m.UpdatedAt.IsZero() {
		errs = append(errs, fmt.Errorf("updated_at is required"))
	}
	if m.CreatedAt.After(m.UpdatedAt) {
		errs = append(errs, fmt.Errorf("created_at %s must not be after updated_at %s", m.CreatedAt, m.UpdatedAt))
	}
	if m.Provenance.CorrectedAt.After(m.UpdatedAt) {
		errs = append(errs, fmt.Errorf("provenance.corrected_at %s must not be after updated_at %s", m.Provenance.CorrectedAt, m.UpdatedAt))
	}
	if m.Freshness.LastVerifiedAt.After(m.UpdatedAt) {
		errs = append(errs, fmt.Errorf("freshness.last_verified_at %s must not be after updated_at %s", m.Freshness.LastVerifiedAt, m.UpdatedAt))
	}

	return errors.Join(errs...)
}

// IsUsable reports whether the record is currently active and unexpired at
// time t. Stale-but-active records remain usable for consumers that are
// willing to tolerate staleness; usability gates only expiry/invalidation.
// A nil record is not usable.
func (m *MemoryRecord) IsUsable(t time.Time) bool {
	if m == nil {
		return false
	}
	return !m.Expiration.IsExpired(t)
}

// FreshnessStatusOf applies the policy to this record at time t and returns
// its freshness status (fresh, stale, or expired).
//
// Stale-but-active records remain usable (see IsUsable): staleness means
// the record must be re-verified before it is used as current factual
// context, not that it is suppressed. Expired records are not usable.
// A nil record is reported as expired.
func (m *MemoryRecord) FreshnessStatusOf(policy FreshnessPolicy, t time.Time) FreshnessAssessment {
	return policy.StatusOf(m, t)
}

// ReVerify reports whether the record must be re-verified before it is used
// as current factual context, under the policy at time t.
//
// Stale and expired records both re-verify: stale records may still guide
// research and prioritization, but must be confirmed against a source
// again before their values are presented as current.
// A nil record must be re-verified.
func (m *MemoryRecord) ReVerify(policy FreshnessPolicy, t time.Time) bool {
	return policy.StatusOf(m, t).ReVerify()
}

// TimeToUTC converts a time.Time to UTC if it is not already in UTC.
func TimeToUTC(t time.Time) time.Time {
	if t.Location() != time.UTC {
		return t.UTC()
	}
	return t
}
