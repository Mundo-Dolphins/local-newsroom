package memory

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func factualRecord() *MemoryRecord {
	m := NewMemoryRecord(testNow)
	m.StableID = "mem-fact-1"
	m.Kind = KindFactual
	m.Scope = ScopeEntity
	m.Subject = EntityRef{Kind: "person", Name: "Jordan Rivers"}
	m.Field = "current_team"
	m.Confidence = ConfidenceMedium
	m.Volatility = VolatileHigh
	m.Freshness = Freshness{LastVerifiedAt: testNow, StaleAfter: 72 * time.Hour}
	m.Provenance = Provenance{
		Sources: []MemorySource{{
			SourceType:  "web",
			SourceID:    "https://example.com/rivers",
			RetrievedAt: testNow,
		}},
	}
	fv := FactualValue{Statement: "Jordan Rivers plays for the Example City Sharks.", Data: map[string]string{"team": "Example City Sharks"}}
	if err := m.EncodeValue(fv, testNow); err != nil {
		panic(err)
	}
	return m
}

func TestNewMemoryRecordDefaults(t *testing.T) {
	m := NewMemoryRecord(time.Date(2026, 7, 21, 10, 30, 0, 0, time.Local))
	if m.SchemaVersion != SchemaVersion {
		t.Errorf("schema version = %q, want %q", m.SchemaVersion, SchemaVersion)
	}
	if m.Expiration.InvalidationStatus != InvalidationActive {
		t.Errorf("default invalidation status = %q, want active", m.Expiration.InvalidationStatus)
	}
	if m.CreatedAt.Location() != time.UTC {
		t.Errorf("created_at must be normalized to UTC, got %v", m.CreatedAt.Location())
	}
	if !m.IsUsable(testNow) {
		t.Error("freshly created record must be usable")
	}
	var nilRec *MemoryRecord
	if nilRec.IsUsable(testNow) {
		t.Error("nil record must not be usable")
	}
}

func TestValueRoundTripByKind(t *testing.T) {
	m := factualRecord()
	got, err := m.DecodeValue()
	if err != nil {
		t.Fatalf("DecodeValue: %v", err)
	}
	fv, ok := got.(FactualValue)
	if !ok {
		t.Fatalf("factual record decoded to %T", got)
	}
	if fv.Statement == "" {
		t.Error("statement lost in round trip")
	}
}

func TestEncodeValueKindMismatch(t *testing.T) {
	m := factualRecord()
	if err := m.EncodeValue(FeedbackRecord{Feedback: "x", GivenBy: "e", GivenAt: testNow}, testNow); err == nil {
		t.Error("encoding a feedback into a factual record must fail")
	} else if !errors.Is(err, ErrKindValueMismatch) {
		t.Errorf("expected ErrKindValueMismatch, got %v", err)
	}
	if err := m.EncodeValue(42, testNow); err == nil {
		t.Error("encoding an unsupported type must fail")
	}
}

func TestEncodeValueUpdatesTimestamp(t *testing.T) {
	m := factualRecord()
	later := testNow.Add(time.Hour)
	if err := m.EncodeValue(FactualValue{Statement: "updated statement"}, later); err != nil {
		t.Fatal(err)
	}
	if !m.UpdatedAt.Equal(later) {
		t.Errorf("updated_at = %v, want %v", m.UpdatedAt, later)
	}
	if m.UpdatedAt.Location() != time.UTC && later.Location() == time.UTC {
		t.Error("updated_at must be normalized to UTC")
	}
}

func TestRecordValidate(t *testing.T) {
	if err := factualRecord().Validate(); err != nil {
		t.Fatalf("well-formed factual record failed: %v", err)
	}

	zero := &MemoryRecord{}
	if err := zero.Validate(); err == nil {
		t.Error("zero-value record must fail")
	}
}

func TestRecordValidateFailureMatrix(t *testing.T) {
	build := func(mutate func(*MemoryRecord)) *MemoryRecord {
		m := factualRecord()
		mutate(m)
		return m
	}
	tests := []struct {
		name    string
		mutate  func(*MemoryRecord)
		wantSub string
	}{
		{"missing schema version", func(m *MemoryRecord) { m.SchemaVersion = "" }, "schema_version"},
		{"non-semantic schema version", func(m *MemoryRecord) { m.SchemaVersion = "garbage" }, "semantic"},
		{"foreign major version", func(m *MemoryRecord) { m.SchemaVersion = "9.0.0" }, "unsupported"},
		{"missing stable id", func(m *MemoryRecord) { m.StableID = "" }, "stable_id"},
		{"stable id without prefix", func(m *MemoryRecord) { m.StableID = "doc-1" }, "prefix"},
		{"bad kind", func(m *MemoryRecord) { m.Kind = "bogus" }, "kind"},
		{"bad scope", func(m *MemoryRecord) { m.Scope = "galaxy" }, "scope"},
		{"bad confidence", func(m *MemoryRecord) { m.Confidence = "maybe" }, "confidence"},
		{"bad volatility", func(m *MemoryRecord) { m.Volatility = "sometimes" }, "volatility"},
		{"bad subject", func(m *MemoryRecord) { m.Subject = EntityRef{Name: "no kind"} }, "kind is required"},
		{"missing value", func(m *MemoryRecord) { m.Value = nil }, "value"},
		{"empty expiration status", func(m *MemoryRecord) { m.Expiration = Expiration{} }, "invalidation_status"},
		{"superseded without target", func(m *MemoryRecord) { m.Expiration = Expiration{InvalidationStatus: InvalidationSuperseded} }, "superseded_by"},
		{"created after updated", func(m *MemoryRecord) { m.CreatedAt = testNow.Add(2 * time.Hour); m.UpdatedAt = testNow }, "created_at"},
		{"corrected after updated", func(m *MemoryRecord) { m.Provenance.CorrectedAt = testNow.Add(2 * time.Hour) }, "corrected_at"},
		{"verified after updated", func(m *MemoryRecord) { m.Freshness.LastVerifiedAt = testNow.Add(2 * time.Hour) }, "last_verified_at"},
		{"nil record", nil, "nil"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var m *MemoryRecord
			if tc.mutate != nil {
				m = build(tc.mutate)
			}
			err := m.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not mention %q", err, tc.wantSub)
			}
		})
	}
}

func TestFactualRecordRequiresProvenanceAndFreshness(t *testing.T) {
	noSource := factualRecord()
	noSource.Provenance.Sources = nil
	err := noSource.Validate()
	if err == nil || !strings.Contains(err.Error(), "provenance source") {
		t.Errorf("factual record without sources must fail, got %v", err)
	}

	noFresh := factualRecord()
	noFresh.Freshness = Freshness{}
	err = noFresh.Validate()
	if err == nil || !strings.Contains(err.Error(), "last_verified_at") {
		t.Errorf("factual record without freshness must fail, got %v", err)
	}

	// Non-factual kinds may have zero provenance.
	fb := NewMemoryRecord(testNow)
	fb.StableID = "mem-fb-1"
	fb.Kind = KindEditorialFeedback
	fb.Scope = ScopeNewsroom
	fb.Confidence = ConfidenceHigh
	fb.Volatility = VolatileStatic
	fb.Freshness = Freshness{}
	if err := fb.EncodeValue(FeedbackRecord{Feedback: "keep it short", GivenBy: "editor-1", GivenAt: testNow, Recurring: true}, testNow); err != nil {
		t.Fatal(err)
	}
	if err := fb.Validate(); err != nil {
		t.Errorf("feedback record without provenance must pass, got %v", err)
	}
}

func TestIsUsable(t *testing.T) {
	m := factualRecord()
	if !m.IsUsable(testNow) {
		t.Error("active record must be usable")
	}
	m.Expiration.ExpiresAt = testNow.Add(-time.Hour)
	if m.IsUsable(testNow) {
		t.Error("expired record must not be usable")
	}
	m2 := factualRecord()
	m2.Expiration = Expiration{InvalidationStatus: InvalidationInvalidated}
	if m2.IsUsable(testNow) {
		t.Error("invalidated record must not be usable")
	}
}
