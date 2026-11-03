package memorystore

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/memory"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// TestRedactValueByKind proves that every kind's free-form payload is
// redacted (in place) by the sanitizer: a secret in each kind's string
// regions is replaced by the placeholder and the record stays valid.
func TestRedactValueByKind(t *testing.T) {
	sz := NewProductionSanitizer(sentinelKey)

	t.Run("entity", func(t *testing.T) {
		rec := buildRecord(t, memory.KindEntity, "")
		rec.StableID = memory.MemoryStableID("mem-rv-entity")
		v, err := rec.DecodeValue()
		if err != nil {
			t.Fatal(err)
		}
		ev := v.(memory.EntityValue)
		ev.Fields = map[string]string{}
		ev.Fields["api_token"] = sentinelKey
		ev.Relationships = []memory.EntityRelationship{{Type: "member_of", Target: "team-x", Note: "key " + sentinelKey}}
		if err := rec.EncodeValue(ev, testNow); err != nil {
			t.Fatal(err)
		}
		if err := sz.redactValue(rec, sz.base()); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(rec.Value), sentinelKey) {
			t.Error("entity payload still contains the secret after redaction")
		}
		if !strings.Contains(string(rec.Value), run.RedactedPlaceholder) {
			t.Errorf("entity payload not redacted: %s", rec.Value)
		}
	})

	t.Run("feedback", func(t *testing.T) {
		rec := buildRecord(t, memory.KindEditorialFeedback, "")
		rec.StableID = memory.MemoryStableID("mem-rv-feedback")
		v, err := rec.DecodeValue()
		if err != nil {
			t.Fatal(err)
		}
		fv := v.(memory.FeedbackRecord)
		fv.Feedback = "Use " + sentinelKey + " carefully."
		fv.Preference = map[string]string{}
		fv.Preference["tone"] = sentinelKey
		if err := sz.redactValue(rec, sz.base()); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(rec.Value), sentinelKey) {
			t.Error("feedback payload still contains the secret after redaction")
		}
	})

	t.Run("correction", func(t *testing.T) {
		rec := buildRecord(t, memory.KindCorrection, "")
		rec.StableID = memory.MemoryStableID("mem-rv-correction")
		v, err := rec.DecodeValue()
		if err != nil {
			t.Fatal(err)
		}
		cv := v.(memory.CorrectionRecord)
		cv.OriginalClaim = "the key is " + sentinelKey
		cv.Reason = "leaked " + sentinelKey
		if err := sz.redactValue(rec, sz.base()); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(rec.Value), sentinelKey) {
			t.Error("correction payload still contains the secret after redaction")
		}
	})

	t.Run("suggestion", func(t *testing.T) {
		rec := buildRecord(t, memory.KindSuggestion, "")
		rec.StableID = memory.MemoryStableID("mem-rv-suggestion")
		v, err := rec.DecodeValue()
		if err != nil {
			t.Fatal(err)
		}
		sv := v.(memory.SuggestionRecord)
		sv.Proposal = "add " + sentinelKey + " to the config"
		sv.ProposedChange = map[string]string{}
		sv.ProposedChange["env"] = sentinelKey
		if err := sz.redactValue(rec, sz.base()); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(rec.Value), sentinelKey) {
			t.Error("suggestion payload still contains the secret after redaction")
		}
	})

	t.Run("recipeExecution", func(t *testing.T) {
		rec := buildRecord(t, memory.KindRecipeExecution, "")
		rec.StableID = memory.MemoryStableID("mem-rv-recipe")
		v, err := rec.DecodeValue()
		if err != nil {
			t.Fatal(err)
		}
		rv := v.(memory.RecipeExecutionRecord)
		rv.Lessons = []string{"leak: " + sentinelKey}
		rv.Failures = []memory.RecipeFailure{{Stage: "fetch", Message: "token " + sentinelKey + " rejected"}}
		if err := sz.redactValue(rec, sz.base()); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(rec.Value), sentinelKey) {
			t.Error("recipe execution payload still contains the secret after redaction")
		}
	})

	t.Run("undecodablePayload", func(t *testing.T) {
		// A payload that cannot be decoded into the kind's type is redacted
		// as raw text (the invalid-record gate would reject it anyway).
		rec := buildRecord(t, memory.KindFactual, "")
		rec.Value = []byte(`{"statement": "contains ` + sentinelKey + `"}`)
		if err := sz.redactValue(rec, sz.base()); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(rec.Value), sentinelKey) {
			t.Error("raw payload still contains the secret after redaction")
		}
	})
}

// TestSanitizerGateRegions proves that a secret in each gate-only region
// (field, provenance, expiration) refuses the record fail-closed, without
// mutating the region.
func TestSanitizerGateRegions(t *testing.T) {
	sz := NewProductionSanitizer(sentinelKey)
	regions := []struct {
		name   string
		mutate func(*memory.MemoryRecord) *string
	}{
		{"field", func(r *memory.MemoryRecord) *string { r.Field = sentinelKey; return &r.Field }},
		{"verified_by", func(r *memory.MemoryRecord) *string {
			r.Provenance.VerifiedBy = sentinelKey
			return &r.Provenance.VerifiedBy
		}},
		{"corrected_by", func(r *memory.MemoryRecord) *string {
			r.Provenance.CorrectedBy = sentinelKey
			return &r.Provenance.CorrectedBy
		}},
		{"expiration.reason", func(r *memory.MemoryRecord) *string {
			r.Expiration.InvalidationReason = sentinelKey
			return &r.Expiration.InvalidationReason
		}},
		{"superseded_by", func(r *memory.MemoryRecord) *string {
			r.Expiration.SupersededBy = sentinelKey
			return &r.Expiration.SupersededBy
		}},
		{"source_id", func(r *memory.MemoryRecord) *string {
			r.Provenance.Sources[0].SourceID = sentinelKey
			return &r.Provenance.Sources[0].SourceID
		}},
		{"provenance.sources.title", func(r *memory.MemoryRecord) *string {
			r.Provenance.Sources[0].Title = sentinelKey
			return &r.Provenance.Sources[0].Title
		}},
		{"provenance.sources.note", func(r *memory.MemoryRecord) *string {
			r.Provenance.Sources[0].Note = sentinelKey
			return &r.Provenance.Sources[0].Note
		}},
	}
	for _, tc := range regions {
		t.Run(tc.name, func(t *testing.T) {
			rec := buildRecord(t, memory.KindFactual, "")
			region := tc.mutate(rec)
			err := sz.Check(rec)
			if !IsSecretDetected(err) {
				t.Fatalf("Check on %s: err = %v, want SecretDetected", tc.name, err)
			}
			if !strings.Contains(err.Error(), tc.name) {
				t.Errorf("error does not name the region: %v", err)
			}
			if !strings.Contains(*region, sentinelKey) {
				t.Error("gate-only region was mutated")
			}
		})
	}
}

// TestNilRecordCheck proves Check(nil) is a no-op (stores call validate
// first and refuse nil records).
func TestNilRecordCheck(t *testing.T) {
	sz := NewProductionSanitizer("k")
	if err := sz.Check(nil); err != nil {
		t.Errorf("Check(nil) = %v, want nil", err)
	}
}

// TestParseTimeRounding covers the storage timestamp format's nanosecond
// rounding and parse fallbacks.
func TestParseTimeRounding(t *testing.T) {
	if got := formatTime(time.Time{}); got != "" {
		t.Errorf("formatTime(zero) = %q, want empty", got)
	}
	nanos := time.Date(2026, 7, 21, 12, 0, 0, 123456789, time.UTC)
	s := formatTime(nanos)
	back := parseTime(s)
	// The storage format keeps nanoseconds, so the round trip is exact
	// (or within the format's sub-nanosecond precision).
	if back.IsZero() {
		t.Fatal("parseTime(formatTime(t)) is zero")
	}
	delta := back.Sub(nanos)
	if delta < 0 {
		delta = -delta
	}
	if delta > time.Microsecond {
		t.Errorf("round trip error = %v, want < 1µs (got %s)", delta, s)
	}
	if got := parseTime("not-a-time"); !got.IsZero() {
		t.Errorf("parseTime(garbage) = %v, want zero", got)
	}
	if got := parseTime(""); !got.IsZero() {
		t.Errorf("parseTime(empty) = %v, want zero", got)
	}
}

// TestSQLiteConfigOptions exercises the connection-pool options.
func TestSQLiteConfigOptions(t *testing.T) {
	s, err := NewSQLiteStore(":memory:", "",
		WithWAL(false),
		WithMaxOpenConns(1),
		WithMaxIdleConns(1),
		WithConnMaxLifetime(time.Minute),
	)
	if err != nil {
		t.Fatalf("open with pool options: %v", err)
	}
	defer func() { _ = s.Close() }()

	if got := s.db.Stats(); got.MaxOpenConnections != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1", got.MaxOpenConnections)
	}
}

// TestNewSQLiteStoreAt covers the explicit-path constructor.
func TestNewSQLiteStoreAt(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSQLiteStoreAt(dir+"/custom.db", WithWAL(false))
	if err != nil {
		t.Fatalf("NewSQLiteStoreAt: %v", err)
	}
	defer func() { _ = s.Close() }()
	if s.dbPath != dir+"/custom.db" {
		t.Errorf("dbPath = %q", s.dbPath)
	}
}

// TestPathResolution covers the dbPath rules (absolute, relative, in-memory).
func TestPathResolution(t *testing.T) {
	dir := t.TempDir()

	// Relative name anchored to the root.
	p, err := dbPath(dir, "memory.db")
	if err != nil {
		t.Fatal(err)
	}
	if p != dir+"/memory.db" {
		t.Errorf("relative resolution = %q", p)
	}

	// Absolute path is cleaned against an existing parent.
	sub := dir + "/sub"
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	abs, err := dbPath(dir, dir+"/./sub/db.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if abs != sub+"/db.sqlite" {
		t.Errorf("absolute resolution = %q, want %q", abs, sub+"/db.sqlite")
	}

	// In-memory escapes.
	if p, err := dbPath("/whatever", ""); err != nil || p != ":memory:" {
		t.Errorf("empty name = %q, %v", p, err)
	}
	if p, err := dbPath(":memory:", "x.db"); err != nil || p != ":memory:" {
		t.Errorf("in-memory root = %q, %v", p, err)
	}

	// Missing parent directory is refused.
	if _, err := dbPath(dir+"/nope", "db.sqlite"); err == nil {
		t.Error("missing parent directory must fail")
	}
	// Parent that is a file is refused.
	fileParent := dir + "/regular-file"
	if err := os.WriteFile(fileParent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := dbPath(fileParent, "db.sqlite"); err == nil {
		t.Error("non-directory parent must fail")
	}
	// Symlinked database path is refused.
	if err := os.WriteFile(dir+"/real.db", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := dir + "/link.db"
	if err := os.Symlink(dir+"/real.db", link); err != nil {
		t.Skip("symlinks unsupported")
	}
	if _, err := dbPath(dir, "link.db"); err == nil {
		t.Error("symlinked database path must fail")
	}
}

// TestEnsurePermissions covers the 0600 enforcement on file and sidecars.
func TestEnsurePermissions(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/db.sqlite"
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensurePermissions(p); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != 0o600 {
		t.Errorf("permissions = %o, want 0600", got)
	}
	// Missing file is a no-op (sidecars may not exist yet).
	if err := ensurePermissions(dir + "/missing"); err != nil {
		t.Errorf("ensurePermissions on missing file: %v", err)
	}
	// In-memory is a no-op.
	if err := ensurePermissions(":memory:"); err != nil {
		t.Errorf("ensurePermissions(:memory:): %v", err)
	}
}

// TestOpenDatabaseWAL verifies the WAL pragma round trip and that sidecars
// get private permissions.
func TestOpenWAL(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/db.sqlite"
	s, err := NewSQLiteStoreAt(dbPath, WithWAL(true))
	if err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
	// Force activity so WAL/SHM sidecars exist, then verify permissions.
	if _, err := s.db.Exec("CREATE TABLE wal_probe(x); INSERT INTO wal_probe VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_ = ensurePermissions(dbPath)
	st, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != 0o600 {
		t.Errorf("db permissions = %o, want 0600", got)
	}
}
