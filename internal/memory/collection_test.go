package memory

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestCollectionMarshalUnmarshalRoundTrip(t *testing.T) {
	col := NewCollection(factualRecord(), entityRecord(), entityFeedbackRecord(),
		correctionRecord(), suggestionRecord(), recipeRecord())

	data, err := col.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// The document carries the schema version.
	var envelope struct {
		SchemaVersion string            `json:"schema_version"`
		Records       []json.RawMessage `json:"records"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.SchemaVersion != SchemaVersion {
		t.Errorf("schema_version = %q, want %q", envelope.SchemaVersion, SchemaVersion)
	}
	if len(envelope.Records) != 6 {
		t.Fatalf("records = %d, want 6", len(envelope.Records))
	}

	got, err := UnmarshalMemoryCollection(data)
	if err != nil {
		t.Fatalf("UnmarshalMemoryCollection: %v", err)
	}
	if len(got.Records) != 6 {
		t.Fatalf("records = %d, want 6", len(got.Records))
	}
	if got.Records[0].StableID != "mem-fact-1" {
		t.Errorf("first record id = %q, want mem-fact-1", got.Records[0].StableID)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("round-tripped collection failed validation: %v", err)
	}
}

func TestCollectionStableAcrossMarshal(t *testing.T) {
	col := NewCollection(factualRecord())
	a, err := col.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	b, err := col.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	ja, jb := map[string]any{}, map[string]any{}
	if err := json.Unmarshal(a, &ja); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &jb); err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Error("Marshal must be deterministic for the same input")
	}
}

func TestUnmarshalForeignMajorVersion(t *testing.T) {
	col := NewCollection(factualRecord())
	data, err := col.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	foreign, _ := json.Marshal("9.0.0")
	env["schema_version"] = foreign
	foreignData, _ := json.Marshal(env)

	_, err = UnmarshalMemoryCollection(foreignData)
	if !errors.Is(err, ErrUnsupportedSchemaVersion) {
		t.Errorf("expected ErrUnsupportedSchemaVersion, got %v", err)
	}
}

func TestUnmarshalMinorVersionAccepted(t *testing.T) {
	col := NewCollection(factualRecord())
	data, err := col.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	bumped, _ := json.Marshal("0.8.0")
	env["schema_version"] = bumped
	bumpedData, _ := json.Marshal(env)

	if _, err := UnmarshalMemoryCollection(bumpedData); err != nil {
		t.Errorf("minor version bump must be accepted: %v", err)
	}
}

func TestUnmarshalMissingOrBadVersion(t *testing.T) {
	col := NewCollection(factualRecord())
	data, err := col.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}

	delete(env, "schema_version")
	noVersion, _ := json.Marshal(env)
	if _, err := UnmarshalMemoryCollection(noVersion); err == nil {
		t.Error("missing schema_version must fail")
	}

	garbage, _ := json.Marshal("garbage")
	env["schema_version"] = garbage
	bad, _ := json.Marshal(env)
	if _, err := UnmarshalMemoryCollection(bad); err == nil {
		t.Error("non-semantic schema_version must fail")
	}
}

func TestUnmarshalInvalidJSON(t *testing.T) {
	if _, err := UnmarshalMemoryCollection([]byte("{not json")); err == nil {
		t.Error("invalid JSON must fail")
	}
}

func TestCollectionValidateDuplicateIDs(t *testing.T) {
	a := factualRecord()
	b := factualRecord() // same stable id
	if err := NewCollection(a, b).Validate(); err == nil {
		t.Error("duplicate stable IDs must fail validation")
	}
}

func TestCollectionValidateFlagsBrokenRecord(t *testing.T) {
	a := factualRecord()
	b := factualRecord()
	b.SchemaVersion = "broken"
	if err := NewCollection(a, b).Validate(); err == nil {
		t.Error("collection with a broken record must fail validation")
	}
}

func TestCollectionValidateForeignMajorVersion(t *testing.T) {
	col := NewCollection(factualRecord())
	col.SchemaVersion = "3.0.0"
	if err := col.Validate(); err == nil {
		t.Error("collection with a foreign major version must fail validation")
	}
}

func TestCollectionValidateNilAndEmpty(t *testing.T) {
	if err := NewCollection().Validate(); err != nil {
		t.Errorf("empty collection must pass: %v", err)
	}
	var nilCol *MemoryCollection
	if err := nilCol.Validate(); err == nil {
		t.Error("nil collection must fail validation")
	}
}

func TestCollectionNilValidate(t *testing.T) {
	if err := NewCollection().Validate(); err != nil {
		t.Errorf("empty collection must pass: %v", err)
	}
}
