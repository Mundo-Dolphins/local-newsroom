package memorystore

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/Mundo-Dolphins/local-newsroom/internal/memory"
	_ "modernc.org/sqlite"
)

// openTempStore creates a SQLite store backed by a file in a fresh temp
// directory, returning the store and the directory.
func openTempStore(t *testing.T, opts ...StoreOption) (*SQLiteStore, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := NewSQLiteStore(dir, DefaultDBName, opts...)
	if err != nil {
		t.Fatalf("open temp store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dir
}

func TestOpenCreatesFileWithPrivatePermissions(t *testing.T) {
	dir := t.TempDir()
	_ = os.Chmod(dir, 0o700)
	s, err := NewSQLiteStore(dir, DefaultDBName)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = s.Close() }()

	st, err := os.Stat(filepath.Join(dir, DefaultDBName))
	if err != nil {
		t.Fatalf("database file was not created: %v", err)
	}
	if got := st.Mode().Perm(); got != 0o600 {
		t.Errorf("database permissions = %o, want 0600", got)
	}
}

func TestOpenRefusesMissingDirectory(t *testing.T) {
	_, err := NewSQLiteStore(filepath.Join(t.TempDir(), "does-not-exist"), "memory.db")
	if err == nil {
		t.Fatal("opening a database in a missing directory must fail")
	}
}

func TestOpenRefusesUnusableFile(t *testing.T) {
	// A database path that is an existing directory must be refused.
	dir := t.TempDir()
	if _, err := NewSQLiteStore(dir, "."); err == nil {
		t.Fatal("opening a database whose resolved path is the root directory must fail")
	}
	// A path that is a non-regular entry (a symlink to a file) is also
	// refused: the open must be able to create WAL/SHM sidecars atomically
	// without following links into another user's file.
	fileDir := t.TempDir()
	target := filepath.Join(fileDir, "target")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(fileDir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported here: %v", err)
	}
	if _, err := NewSQLiteStore(fileDir, "link"); err == nil {
		t.Fatal("opening a database through a symlink must fail")
	}
}

func TestInMemoryDatabase(t *testing.T) {
	s, err := NewSQLiteStore(":memory:", "")
	if err != nil {
		t.Fatalf("open :memory: store: %v", err)
	}
	defer func() { _ = s.Close() }()
	if s.dbPath != ":memory:" {
		t.Errorf("dbPath = %q, want :memory:", s.dbPath)
	}
}

func TestMigrationsRecorded(t *testing.T) {
	s, _ := openTempStore(t)
	ctx := context.Background()
	version, err := s.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version != currentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", version, currentSchemaVersion)
	}
}

func TestMigrationsIdempotent(t *testing.T) {
	dir := t.TempDir()
	s1, err := NewSQLiteStore(dir, DefaultDBName)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}
	// Re-open: migrations must not re-apply or fail.
	s2, err := NewSQLiteStore(dir, DefaultDBName)
	if err != nil {
		t.Fatalf("re-open after migrations: %v", err)
	}
	defer func() { _ = s2.Close() }()
}

func TestUpgradeFromV1ToV2(t *testing.T) {
	// Simulate a v0.7.0 database (migration 001 only) and re-open it with
	// the current binary (migrations 001+002): the 002 ALTER statements
	// must apply cleanly and the store must be fully usable.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, DefaultDBName)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	m001, err := migrationsFS.ReadFile("migrations/001_initial_schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(m001)); err != nil {
		t.Fatalf("apply v1 schema: %v", err)
	}
	if _, err := db.Exec(
		"INSERT INTO schema_migrations (version, applied_at) VALUES (1, '2026-07-21T12:00:00Z')",
	); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Re-open with the full binary: migration 002 must apply.
	s, err := NewSQLiteStore(dir, DefaultDBName)
	if err != nil {
		t.Fatalf("open upgraded database: %v", err)
	}
	defer func() { _ = s.Close() }()

	version, err := s.SchemaVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if version != currentSchemaVersion {
		t.Fatalf("schema version after upgrade = %d, want %d", version, currentSchemaVersion)
	}

	// The store must be fully usable after the upgrade.
	rec := buildRecord(t, memory.KindFactual, "")
	if err := s.Write(context.Background(), rec); err != nil {
		t.Fatalf("write after upgrade: %v", err)
	}
	got, err := s.Get(context.Background(), rec.StableID)
	if err != nil || got == nil {
		t.Fatalf("get after upgrade: rec=%v err=%v", got, err)
	}
}
