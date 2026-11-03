package memorystore

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/memory"
)

func TestDeriveStableIDDeterministic(t *testing.T) {
	a := buildRecord(t, memory.KindFactual, "")
	b := buildRecord(t, memory.KindFactual, "")
	if a.StableID != b.StableID {
		t.Errorf("same content derived different IDs: %s vs %s", a.StableID, b.StableID)
	}
}

func TestDeriveStableIDPrefix(t *testing.T) {
	rec := buildRecord(t, memory.KindFactual, "")
	if len(string(rec.StableID)) < len(memory.MemoryStableIDPrefix) ||
		string(rec.StableID)[:len(memory.MemoryStableIDPrefix)] != memory.MemoryStableIDPrefix {
		t.Errorf("derived ID %q does not carry the mem- prefix", rec.StableID)
	}
}

func TestDeriveStableIDDiffersByKind(t *testing.T) {
	fact := buildRecord(t, memory.KindFactual, "")
	entity := buildRecord(t, memory.KindEntity, "")
	if fact.StableID == entity.StableID {
		t.Error("different kinds must not share an ID")
	}
}

func TestDeriveStableIDDiffersByContent(t *testing.T) {
	a := buildRecord(t, memory.KindFactual, "")
	b := buildRecord(t, memory.KindFactual, "")
	if err := b.EncodeValue(memory.FactualValue{
		Statement: "A completely different claim.",
		Data:      map[string]string{"team": "Other Team"},
	}, testNow); err != nil {
		t.Fatal(err)
	}
	b.StableID = DeriveStableID(b)
	if a.StableID == b.StableID {
		t.Error("different content must not share an ID")
	}
}

func TestDeriveStableIDStableAcrossLifecycleChanges(t *testing.T) {
	rec := buildRecord(t, memory.KindFactual, "")
	before := DeriveStableID(rec)

	// Re-verification changes freshness/updated_at but not the content digest
	// anchor... (freshness is part of the digest: re-verification yields a
	// new ID, which is correct — the content did change). What must be stable
	// is lifecycle transitions alone.
	rec.Expiration.InvalidationStatus = memory.InvalidationSuperseded
	rec.Expiration.SupersededBy = "mem-other"
	rec.Expiration.InvalidationReason = "re-verified"
	rec.UpdatedAt = testNow.Add(time.Hour)

	after := DeriveStableID(rec)
	if before != after {
		t.Errorf("lifecycle change altered the ID: %s -> %s", before, after)
	}
}

func TestDeriveStableIDStableAcrossTimestamps(t *testing.T) {
	rec := buildRecord(t, memory.KindFactual, "")
	before := DeriveStableID(rec)
	rec.CreatedAt = testNow.Add(-time.Hour)
	rec.UpdatedAt = testNow.Add(time.Hour)
	if after := DeriveStableID(rec); before != after {
		t.Errorf("timestamp change altered the ID: %s -> %s", before, after)
	}
}

func TestDeriveStableIDNil(t *testing.T) {
	var rec *memory.MemoryRecord
	if id := DeriveStableID(rec); id != "" {
		t.Errorf("nil record must derive an empty ID, got %q", id)
	}
}

func TestDeriveStableIDCanonicalJSON(t *testing.T) {
	// Two records whose value JSON differs only in key order must derive
	// the same ID (canonical re-marshal), and malformed raw JSON falls back
	// to the raw bytes.
	a := buildRecord(t, memory.KindEntity, "")
	if id1 := DeriveStableID(a); id1 == "" {
		t.Fatal("derived ID is empty")
	}
	raw1 := []byte(`{"b":2,"a":1}`)
	raw2 := []byte(`{"a":1,"b":2}`)
	if c1, c2 := canonicalRawMessage(raw1), canonicalRawMessage(raw2); string(c1) != string(c2) {
		t.Errorf("canonical forms differ: %s vs %s", c1, c2)
	}
	// Unparseable JSON is passed through verbatim.
	raw := json.RawMessage(`{not valid json`) //nolint:goconst
	if got := canonicalRawMessage(raw); string(got) != string(raw) {
		t.Errorf("malformed raw message was altered: %s", got)
	}
	// Empty raw message becomes nil.
	if got := canonicalRawMessage(json.RawMessage("   ")); got != nil {
		t.Errorf("whitespace-only raw message = %v, want nil", got)
	}
}
