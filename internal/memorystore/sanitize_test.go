package memorystore

import (
	"context"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/local-newsroom/internal/memory"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// sentinelKey is a bearer-context token that the pattern detectors must catch
// (40 chars of high-entropy token material in a bearer context, plus the
// token-run detector on its own).
const sentinelKey = "omlx-bearer-" + "A1b2C3d4E5f6A1b2C3d4E5f6A1b2C3d4E5f6"

// secretRecord builds a record whose value payload contains the sentinel key
// in a bearer context.
func secretRecord(t *testing.T) *memory.MemoryRecord {
	rec := buildRecord(t, memory.KindFactual, "")
	rec.StableID = memory.MemoryStableID("mem-secret-1")
	var fv memory.FactualValue
	decoded, err := rec.DecodeValue()
	if err != nil {
		panic(err)
	}
	fv = decoded.(memory.FactualValue)
	fv.Statement = "The endpoint is Authorization: Bearer " + sentinelKey +
		" as noted in the source."
	if err := rec.EncodeValue(fv, testNow); err != nil {
		panic(err)
	}
	return rec
}

// TestProductionSanitizerExactRefusal proves the fail-closed gate: content
// that still matches the detectors after redaction is refused, and the
// error names the region without including the secret.
func TestProductionSanitizerExactRefusal(t *testing.T) {
	sz := NewProductionSanitizer(sentinelKey)
	// Gate-only region: a secret in a source ID is not redacted (redaction
	// only touches the free-form value payload); it is detected after the
	// redaction pass and the whole record is refused fail-closed. The error
	// names the region, never the secret.
	rec := secretRecord(t)
	rec.Provenance.Sources[0].SourceID = "https://example.com?key=" + sentinelKey
	err := sz.Check(rec)
	if err == nil {
		t.Fatal("Check accepted a record with a secret in a gate-only region")
	}
	if !IsSecretDetected(err) {
		t.Fatalf("error type = %T (%v), want SecretDetectedError", err, err)
	}
	if strings.Contains(err.Error(), sentinelKey) {
		t.Fatal("error message includes the secret value")
	}
	if !strings.Contains(rec.Provenance.Sources[0].SourceID, sentinelKey) {
		t.Error("gate-only region was mutated instead of refused")
	}
}

// TestProductionSanitizerRedactsCleanContent proves that a record whose
// secret is fully redactable is accepted (the redacted copy is persisted,
// never the raw secret).
func TestProductionSanitizerRedactsCleanContent(t *testing.T) {
	sz := NewProductionSanitizer(sentinelKey)
	rec := buildRecord(t, memory.KindFactual, "")
	rec.StableID = memory.MemoryStableID("mem-redact-1")
	v, _ := rec.DecodeValue()
	fv := v.(memory.FactualValue)
	fv.Statement = "Rotated key " + sentinelKey + " was published in the article."
	if err := rec.EncodeValue(fv, testNow); err != nil {
		t.Fatal(err)
	}

	if err := sz.Check(rec); err != nil {
		t.Fatalf("Check: %v (redaction should have cleared the content)", err)
	}
	if strings.Contains(string(rec.Value), sentinelKey) {
		t.Fatal("value payload still contains the secret after Check")
	}
	if !strings.Contains(string(rec.Value), run.RedactedPlaceholder) {
		t.Error("expected the redaction placeholder in the sanitized payload")
	}
}

// TestStoreRefusesSecretOnWrite proves the store-level gate: a
// TestStoreRedactsSecretOnWrite proves the store-level gate: a secret that
// is detected (an exact value) is redacted, never persisted raw, and the
// sanitized form round-trips through the store.
func TestStoreRedactsSecretOnWrite(t *testing.T) {
	store, err := NewSQLiteStore(":memory:", "",
		WithSanitizer(NewProductionSanitizer(sentinelKey)),
		WithWAL(false))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	rec := secretRecord(t)
	if err := store.Write(ctx, rec); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := store.Get(ctx, rec.StableID)
	if err != nil || got == nil {
		t.Fatalf("Get: %v %v", got, err)
	}
	if strings.Contains(string(got.Value), sentinelKey) {
		t.Fatal("persisted value contains the raw secret")
	}
	if !strings.Contains(string(got.Value), run.RedactedPlaceholder) {
		t.Error("persisted value does not carry the redaction placeholder")
	}
}

// TestStoreSecretSweepRoundTrip proves the secret never lands on disk: write
// a redactable record, close, re-open, and verify the sanitized form.
func TestStoreSecretSweepRoundTrip(t *testing.T) {
	dir := t.TempDir()
	key := "swept-" + strings.Repeat("Z9y8", 12) + "x" // 40+ char high-entropy token

	mkRec := func() *memory.MemoryRecord {
		rec := buildRecord(t, memory.KindFactual, "")
		rec.StableID = memory.MemoryStableID("mem-sweep-1")
		v, _ := rec.DecodeValue()
		fv := v.(memory.FactualValue)
		fv.Statement = "The feed header carried " + key + " once."
		if err := rec.EncodeValue(fv, testNow); err != nil {
			panic(err)
		}
		return rec
	}

	s1, err := NewSQLiteStore(dir, DefaultDBName,
		WithSanitizer(NewProductionSanitizer(key)),
		WithWAL(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.Write(context.Background(), mkRec()); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := NewSQLiteStore(dir, DefaultDBName,
		WithSanitizer(NewProductionSanitizer(key)),
		WithWAL(false))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()

	got, err := s2.Get(context.Background(), "mem-sweep-1")
	if err != nil || got == nil {
		t.Fatalf("Get after re-open: %v %v", got, err)
	}
	if strings.Contains(string(got.Value), key) {
		t.Fatal("persisted value still contains the secret after re-open")
	}
	if !strings.Contains(string(got.Value), run.RedactedPlaceholder) {
		t.Error("persisted value does not carry the redaction placeholder")
	}
}

// TestPatternOnlyDetectionCoversURLCredentials covers the pattern detectors
// (no exact values configured): URL userinfo credentials are caught.
func TestPatternOnlyDetectionCoversURLCredentials(t *testing.T) {
	sz := NewProductionSanitizer() // pattern-only
	rec := buildRecord(t, memory.KindFactual, "")
	rec.StableID = memory.MemoryStableID("mem-urlcred-1")
	rec.Field = "Fetched https://user:Sup3rs3cretT0k3nValue12345@example.com/feed"
	if err := rec.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := sz.Check(rec); err == nil {
		t.Fatal("URL userinfo credentials were not detected")
	}
}

// TestPatternOnlyDetectionCoversPEM covers private-key blocks.
func TestPatternOnlyDetectionCoversPEM(t *testing.T) {
	sz := NewProductionSanitizer() // pattern-only
	rec := buildRecord(t, memory.KindFactual, "")
	rec.StableID = memory.MemoryStableID("mem-pem-1")
	rec.Field = "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA7\n-----END RSA PRIVATE KEY-----"
	if err := rec.Validate(); err != nil {
		t.Fatal(err)
	}
	checkErr := sz.Check(rec)
	if checkErr == nil {
		t.Fatal("PEM private key block in a field was not detected")
	}
	// The error must not include the key material.
	if strings.Contains(checkErr.Error(), "MIIEpAIBAAKCAQEA7") {
		t.Error("error includes the key material")
	}
}

// TestCleanRecordPasses proves a normal record is unaffected by the gate.
func TestCleanRecordPasses(t *testing.T) {
	sz := NewProductionSanitizer("some-other-key")
	rec := buildRecord(t, memory.KindFactual, "")
	if err := sz.Check(rec); err != nil {
		t.Fatalf("clean record refused: %v", err)
	}
	if string(rec.Value) == "" {
		t.Error("value payload was emptied by the gate")
	}
}
