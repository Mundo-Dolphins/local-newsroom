package memory

import (
	"errors"
	"testing"
)

func TestNilReceivers(t *testing.T) {
	var m *MemoryRecord
	if _, err := m.DecodeValue(); err == nil {
		t.Error("DecodeValue on nil record must fail")
	}
	if err := m.EncodeValue(FactualValue{Statement: "x"}, testNow); err == nil {
		t.Error("EncodeValue on nil record must fail")
	}
	if err := m.Validate(); err == nil {
		t.Error("Validate on nil record must fail")
	}
	if err := AsVerifiedEvidence(m); err == nil {
		t.Error("AsVerifiedEvidence on nil record must fail")
	} else if !errors.Is(err, ErrMemoryNotEvidence) {
		t.Errorf("got %v, want ErrMemoryNotEvidence", err)
	}
}

func TestDecodeValueErrorPaths(t *testing.T) {
	tests := []struct {
		kind MemoryKind
		raw  string
	}{
		{KindFactual, `{`},            // invalid JSON
		{KindFactual, `42`},           // non-object for factual
		{KindEntity, `42`},            // non-object for entity
		{KindEditorialFeedback, `42`}, // non-object for feedback
		{KindCorrection, `42`},        // non-object for correction
		{KindSuggestion, `42`},        // non-object for suggestion
		{KindRecipeExecution, `42`},   // non-object for recipe execution
	}
	for i, tc := range tests {
		m := NewMemoryRecord(testNow)
		m.StableID = "mem-decode"
		m.Kind = tc.kind
		m.Value = []byte(tc.raw)
		if _, err := m.DecodeValue(); err == nil {
			t.Errorf("case %d (%s %s): expected decode failure", i, tc.kind, tc.raw)
		} else if !errors.Is(err, ErrKindValueMismatch) {
			t.Errorf("case %d: got %v, want ErrKindValueMismatch", i, err)
		}
	}
}

func TestDecodeUnknownKind(t *testing.T) {
	m := NewMemoryRecord(testNow)
	m.StableID = "mem-decode"
	m.Kind = "bogus"
	m.Value = []byte(`{}`)
	if _, err := m.DecodeValue(); err == nil {
		t.Error("decoding a record with an unknown kind must fail")
	} else if !errors.Is(err, ErrKindValueMismatch) {
		t.Errorf("got %v, want ErrKindValueMismatch", err)
	}
}

func TestEncodeValueNilAndUnsupported(t *testing.T) {
	m := NewMemoryRecord(testNow)
	m.StableID = "mem-enc"
	m.Kind = KindFactual
	if err := m.EncodeValue(nil, testNow); err == nil {
		t.Error("encoding nil must fail")
	}
	if err := m.EncodeValue("a plain string", testNow); err == nil {
		t.Error("encoding an unsupported type must fail")
	} else if !errors.Is(err, ErrKindValueMismatch) {
		t.Errorf("got %v, want ErrKindValueMismatch", err)
	}
}

func TestCollectionMarshalNil(t *testing.T) {
	var col *MemoryCollection
	if _, err := col.Marshal(); err == nil {
		t.Error("Marshal on nil collection must fail")
	}
}

func TestSubjectValidateBranches(t *testing.T) {
	// A zero Subject (no kind, no name) is "unpopulated" and passes.
	if err := factualRecord().Validate(); err != nil {
		t.Fatalf("record without subject must pass: %v", err)
	}
	// A partially populated subject fails its own Validate, surfaced via the
	// record.
	m := factualRecord()
	m.Subject = EntityRef{Name: "name only"}
	if err := m.Validate(); err == nil {
		t.Error("record with a kind-less subject must fail")
	}
	m.Subject = EntityRef{Kind: "person"}
	if err := m.Validate(); err == nil {
		t.Error("record with a name-less subject must fail")
	}
	m.Subject = EntityRef{Kind: "person", Name: "Full"}
	if err := m.Validate(); err != nil {
		t.Errorf("record with a valid subject must pass: %v", err)
	}
}

func TestEmptyObjectValue(t *testing.T) {
	z := factualRecord()
	z.Value = []byte(`{}`)
	if err := z.Validate(); err != nil {
		t.Errorf("record with an empty-object value must pass: %v", err)
	}
}

func TestCollectionValidateBranches(t *testing.T) {
	noVersion := NewCollection()
	noVersion.SchemaVersion = ""
	if err := noVersion.Validate(); err == nil {
		t.Error("collection without schema version must fail")
	}
	badSemver := NewCollection()
	badSemver.SchemaVersion = "nonsense"
	if err := badSemver.Validate(); err == nil {
		t.Error("collection with non-semantic version must fail")
	}
	foreign := NewCollection()
	foreign.SchemaVersion = "9.1.0"
	if err := foreign.Validate(); err == nil {
		t.Error("collection with foreign major must fail")
	}
	broken := factualRecord()
	broken.StableID = ""
	if err := NewCollection(broken).Validate(); err == nil {
		t.Error("collection containing an invalid record must fail")
	}
	nilRec := NewCollection()
	nilRec.Records = []*MemoryRecord{nil}
	if err := nilRec.Validate(); err == nil {
		t.Error("collection containing a nil record must fail")
	}
}

func TestProvenanceValidateBranches(t *testing.T) {
	if err := (Provenance{}).Validate(); err != nil {
		t.Errorf("zero provenance must pass: %v", err)
	}
	noID := Provenance{Sources: []MemorySource{{SourceType: "web", RetrievedAt: testNow}}}
	if err := noID.Validate(); err == nil {
		t.Error("source without ID must fail")
	}
	noRetrieved := Provenance{Sources: []MemorySource{{SourceType: "web", SourceID: "x"}}}
	if err := noRetrieved.Validate(); err == nil {
		t.Error("source without retrieved_at must fail")
	}
	noNo := Provenance{UserCorrected: true, CorrectedAt: testNow}
	if err := noNo.Validate(); err == nil {
		t.Error("correction without corrected_by must fail")
	}
	noWhen := Provenance{UserCorrected: true, CorrectedBy: "e"}
	if err := noWhen.Validate(); err == nil {
		t.Error("correction without corrected_at must fail")
	}
	oneBad := Provenance{Sources: []MemorySource{
		{SourceType: "web", SourceID: "ok", RetrievedAt: testNow},
		{SourceID: "missing type"},
	}}
	if err := oneBad.Validate(); err == nil {
		t.Error("a single bad source among many must fail")
	}
}

func TestConfidenceLevelValidation(t *testing.T) {
	m := factualRecord()
	m.Confidence = "maybe"
	if err := m.Validate(); err == nil {
		t.Error("record with unknown confidence must fail")
	}
}
