// Package memorystore provides the SQLite implementation of the Store
// interface for the v0.7 editorial-memory subsystem.
//
// SQLiteStore provides:
//   - Versioned schema migrations (embedded SQL, applied in transactions)
//   - Atomic writes: every mutating operation runs in a single transaction
//   - Safe concurrent local use: WAL mode, busy timeout, bounded pool
//   - Deterministic IDs: content-derived stable IDs (DeriveStableID)
//   - Secret-safe persistence: a sanitization gate before every write
//   - Path containment and private file permissions
//
// Example usage:
//
//	store, err := memorystore.NewSQLiteStore("/path/to/workspace", "memory.db")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer store.Close()
//
//	err = store.Write(ctx, record)
package memorystore

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/memory"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// currentSchemaVersion is the highest migration known to this binary.
const currentSchemaVersion = 2

// timeFormat is the fixed storage format for timestamps.
const timeFormat = "2006-01-02T15:04:05.999999999"

func nowUTC() string {
	return time.Now().UTC().Format(timeFormat)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(timeFormat)
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(timeFormat, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// SQLiteStore is the Store implementation backed by SQLite.
type SQLiteStore struct {
	db        *sql.DB
	dbPath    string
	sanitizer Sanitizer
}

// Ensure SQLiteStore implements Store
var _ Store = (*SQLiteStore)(nil)

// StoreOption configures a SQLiteStore.
type StoreOption func(*storeConfig)

// storeConfig holds optional configuration for SQLiteStore.
type storeConfig struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	EnableWal       bool
	Sanitizer       Sanitizer
}

func defaultStoreConfig() storeConfig {
	return storeConfig{
		MaxOpenConns:    2,
		MaxIdleConns:    1,
		ConnMaxLifetime: 0,
		EnableWal:       true,
	}
}

// WithMaxOpenConns sets the maximum number of open database connections.
func WithMaxOpenConns(n int) StoreOption {
	return func(c *storeConfig) { c.MaxOpenConns = n }
}

// WithMaxIdleConns sets the maximum number of idle database connections.
func WithMaxIdleConns(n int) StoreOption {
	return func(c *storeConfig) { c.MaxIdleConns = n }
}

// WithConnMaxLifetime sets the maximum lifetime for database connections.
func WithConnMaxLifetime(d time.Duration) StoreOption {
	return func(c *storeConfig) { c.ConnMaxLifetime = d }
}

// WithWAL enables (true) or disables (false) WAL mode.
func WithWAL(enable bool) StoreOption {
	return func(c *storeConfig) { c.EnableWal = enable }
}

// WithSanitizer sets the sanitization gate applied before every write.
//
// The default (no option) is a pattern-only ProductionSanitizer: records
// are still gated, with pattern detection only. Pass
// NewProductionSanitizer(keys...) to add exact values, or a custom
// Sanitizer for tests.
func WithSanitizer(s Sanitizer) StoreOption {
	return func(c *storeConfig) { c.Sanitizer = s }
}

// NewSQLiteStore creates a SQLite-backed Store.
//
// Parameters:
//   - root: the directory that contains the database (must exist). Pass
//     ":memory:" for an in-memory database (tests).
//   - name: the file name inside root (default "memory.db"). Pass
//     ":memory:" for an in-memory database, or omit (empty) for the
//     default name.
//   - opts: optional configuration (WithSanitizer, WithWAL, ...).
//
// The database file is created with private (0600) permissions. Migrations
// are applied automatically on open, in transactions, and are idempotent.
func NewSQLiteStore(root, name string, opts ...StoreOption) (*SQLiteStore, error) {
	if name == "" {
		name = DefaultDBName
	}
	dbFile, err := dbPath(root, name)
	if err != nil {
		return nil, fmt.Errorf("invalid memory database path: %w", err)
	}

	config := defaultStoreConfig()
	for _, opt := range opts {
		opt(&config)
	}
	if config.Sanitizer == nil {
		config.Sanitizer = NewProductionSanitizer()
	}

	store := &SQLiteStore{
		dbPath:    dbFile,
		sanitizer: config.Sanitizer,
	}

	if err := store.openWithConfig(config); err != nil {
		return nil, fmt.Errorf("failed to open memory database: %w", err)
	}

	return store, nil
}

// NewSQLiteStoreAt is a convenience constructor taking a single full path to
// the database file (or ":memory:").
func NewSQLiteStoreAt(path string, opts ...StoreOption) (*SQLiteStore, error) {
	root, name := splitPath(path)
	return NewSQLiteStore(root, name, opts...)
}

// splitPath splits a full path into its parent directory and base name,
// preserving the ":memory:" sentinel.
func splitPath(path string) (root, name string) {
	if path == ":memory:" || path == "" {
		return ":memory:", path
	}
	return filepath.Dir(path), filepath.Base(path)
}

// openWithConfig opens the database and applies configuration + migrations.
func (s *SQLiteStore) openWithConfig(config storeConfig) error {
	var err error
	s.db, err = sql.Open("sqlite", s.dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}

	s.db.SetMaxOpenConns(config.MaxOpenConns)
	s.db.SetMaxIdleConns(config.MaxIdleConns)
	if config.ConnMaxLifetime > 0 {
		s.db.SetConnMaxLifetime(config.ConnMaxLifetime)
	}

	if _, err := s.db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		_ = s.db.Close()
		return fmt.Errorf("enable foreign keys: %w", err)
	}
	if config.EnableWal {
		if _, err := s.db.Exec("PRAGMA journal_mode = WAL"); err != nil {
			_ = s.db.Close()
			return fmt.Errorf("enable WAL mode: %w", err)
		}
	}
	if _, err := s.db.Exec("PRAGMA busy_timeout = 30000"); err != nil {
		_ = s.db.Close()
		return fmt.Errorf("set busy timeout: %w", err)
	}
	if _, err := s.db.Exec("PRAGMA temp_store = MEMORY"); err != nil {
		_ = s.db.Close()
		return fmt.Errorf("set temp store: %w", err)
	}

	if err := s.runMigrations(); err != nil {
		_ = s.db.Close()
		return fmt.Errorf("run migrations: %w", err)
	}

	// Private permissions on the file (best effort, non-fatal).
	if err := ensurePermissions(s.dbPath); err != nil {
		// Reported, not fatal: the file may not exist yet for :memory:
		// or the platform may restrict chmod.
		_ = err
	}

	return nil
}

// migration is a single embedded schema migration.
type migration struct {
	Version int
	Name    string
	Content string
}

// runMigrations applies all pending schema migrations.
//
// Each migration runs in its own transaction together with its
// bookkeeping row, so a failed migration never leaves a half-applied
// schema recorded as applied.
func (s *SQLiteStore) runMigrations() error {
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	var currentVersion int
	if err := s.db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&currentVersion); err != nil {
		return fmt.Errorf("read current schema version: %w", err)
	}

	migrations, err := loadMigrations()
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}

	for _, m := range migrations {
		if m.Version <= currentVersion {
			continue
		}
		if err := s.applyMigration(m); err != nil {
			return fmt.Errorf("apply migration %d (%s): %w", m.Version, m.Name, err)
		}
	}
	return nil
}

// loadMigrations reads the embedded migration files, ordered by version.
func loadMigrations() ([]migration, error) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations directory: %w", err)
	}
	var migrations []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		prefix := strings.SplitN(e.Name(), "_", 2)[0]
		version, err := strconv.Atoi(strings.TrimLeft(prefix, "0"))
		if err != nil || version == 0 {
			continue // not a numbered migration
		}
		data, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", e.Name(), err)
		}
		migrations = append(migrations, migration{Version: version, Name: e.Name(), Content: stripSQLComments(string(data))})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	return migrations, nil
}

// stripSQLComments removes -- line comments from a migration body.
//
// modernc.org/sqlite's driver runs Exec with a single prepared statement,
// which does not tolerate leading comment lines before a statement; the
// archive store avoids this by hard-coding its migration list, but we strip
// comments instead so the embedded files can stay self-documenting.
// Single-quoted string literals are preserved verbatim.
func stripSQLComments(content string) string {
	var b strings.Builder
	inString := false
	lineStart := true
	for i := 0; i < len(content); i++ {
		c := content[i]
		if inString {
			b.WriteByte(c)
			if c == '\'' {
				inString = false
			}
			lineStart = false
			continue
		}
		if c == '\'' {
			inString = true
			b.WriteByte(c)
			continue
		}
		if lineStart && c == '-' && i+1 < len(content) && content[i+1] == '-' {
			for i < len(content) && content[i] != '\n' {
				i++
			}
			if i < len(content) {
				b.WriteByte('\n')
			}
			lineStart = true
			continue
		}
		b.WriteByte(c)
		lineStart = c == '\n'
	}
	return b.String()
}

// applyMigration applies a single migration inside a transaction that also
// records the applied version.
//
// A migration may contain multiple statements; they are executed in
// sequence within the transaction (comment lines are stripped at load time
// — see stripSQLComments).
func (s *SQLiteStore) applyMigration(m migration) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	rollback := func(cause error) error {
		_ = tx.Rollback()
		return cause
	}

	for _, stmt := range splitStatements(m.Content) {
		if _, err := tx.Exec(stmt); err != nil {
			return rollback(fmt.Errorf("execute statement: %w", err))
		}
	}
	if _, err := tx.Exec(
		"INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)",
		m.Version, time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		return rollback(fmt.Errorf("record migration: %w", err))
	}
	return tx.Commit()
}

// splitStatements splits a migration body into individual non-empty
// statements (semicolons outside string literals).
func splitStatements(content string) []string {
	var stmts []string
	var b strings.Builder
	inString := false
	for i := 0; i < len(content); i++ {
		c := content[i]
		if c == '\'' {
			inString = !inString
			b.WriteByte(c)
			continue
		}
		if c == ';' && !inString {
			stmts = append(stmts, b.String())
			b.Reset()
			continue
		}
		b.WriteByte(c)
	}
	if strings.TrimSpace(b.String()) != "" {
		stmts = append(stmts, b.String())
	}
	out := stmts[:0]
	for _, s := range stmts {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// Close closes the database connection.
func (s *SQLiteStore) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// SchemaVersion returns the applied schema version of the backing store.
func (s *SQLiteStore) SchemaVersion(ctx context.Context) (int, error) {
	var version int
	if err := s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return version, nil
}

// ---- Row mapping ---------------------------------------------------------

// memoryRow is the flat row shape of the memories table.
type memoryRow struct {
	StableID    string
	Kind        string
	Scope       string
	SubjectKind string
	SubjectName string
	SubjectID   string
	SubjectAli  string
	Field       string
	Value       string
	Sources     string
	DerivedFrom string
	VerifiedBy  string
	UserCorr    int
	CorrBy      string
	CorrAt      string
	Confidence  string
	Volatility  string
	LastVerif   string
	StaleAfter  int64
	ExpiresAt   string
	Status      string
	SupersedBy  string
	Reason      string
	DerivedHead string
	SuggStatus  string
	ContentHash string
	CreatedAt   string
	UpdatedAt   string
}

// scanMemory scans a row into a memoryRow. Callers must select columns in
// this exact order.
func scanMemory(scanner interface{ Scan(...any) error }) (memoryRow, error) {
	var r memoryRow
	err := scanner.Scan(
		&r.StableID, &r.Kind, &r.Scope,
		&r.SubjectKind, &r.SubjectName, &r.SubjectID, &r.SubjectAli,
		&r.Field, &r.Value,
		&r.Sources, &r.DerivedFrom, &r.VerifiedBy, &r.UserCorr, &r.CorrBy, &r.CorrAt,
		&r.Confidence, &r.Volatility, &r.LastVerif, &r.StaleAfter,
		&r.ExpiresAt, &r.Status, &r.SupersedBy, &r.Reason,
		&r.DerivedHead, &r.SuggStatus,
		&r.ContentHash, &r.CreatedAt, &r.UpdatedAt,
	)
	return r, err
}

// memoryColumns is the SELECT column list matching scanMemory.
const memoryColumns = `
	stable_id, kind, scope,
	subject_kind, subject_name, subject_canonical_id, subject_aliases,
	field, value,
	sources, derived_from, verified_by, user_corrected, corrected_by, corrected_at,
	confidence, volatility, last_verified_at, stale_after_ns,
	expires_at, invalidation_status, superseded_by, invalidation_reason,
	derived_head, suggestion_status,
	content_hash, created_at, updated_at`

// recordFromRow reconstitutes a memory record from its row.
func recordFromRow(r memoryRow) *memory.MemoryRecord {
	rec := &memory.MemoryRecord{
		SchemaVersion: memory.SchemaVersion,
		StableID:      memory.MemoryStableID(r.StableID),
		Kind:          memory.MemoryKind(r.Kind),
		Scope:         memory.MemoryScope(r.Scope),
		Subject: memory.EntityRef{
			Kind:        r.SubjectKind,
			Name:        r.SubjectName,
			CanonicalID: r.SubjectID,
		},
		Field:      r.Field,
		Value:      []byte(r.Value),
		Confidence: memory.ConfidenceLevel(r.Confidence),
		Volatility: memory.Volatility(r.Volatility),
		Freshness: memory.Freshness{
			LastVerifiedAt: parseTime(r.LastVerif),
			StaleAfter:     time.Duration(r.StaleAfter),
		},
		Expiration: memory.Expiration{
			ExpiresAt:          parseTime(r.ExpiresAt),
			InvalidationStatus: memory.InvalidationStatus(r.Status),
			SupersededBy:       r.SupersedBy,
			InvalidationReason: r.Reason,
		},
		Provenance: memory.Provenance{
			VerifiedBy:    r.VerifiedBy,
			UserCorrected: r.UserCorr != 0,
			CorrectedBy:   r.CorrBy,
			CorrectedAt:   parseTime(r.CorrAt),
		},
		CreatedAt: parseTime(r.CreatedAt),
		UpdatedAt: parseTime(r.UpdatedAt),
	}
	if len(r.SubjectAli) > 0 {
		_ = json.Unmarshal([]byte(r.SubjectAli), &rec.Subject.Aliases)
	}
	if len(r.Sources) > 0 {
		_ = json.Unmarshal([]byte(r.Sources), &rec.Provenance.Sources)
	}
	if len(r.DerivedFrom) > 0 {
		_ = json.Unmarshal([]byte(r.DerivedFrom), &rec.Provenance.DerivedFrom)
	}
	return rec
}

// rowFromRecord flattens a record into its row representation.
func rowFromRecord(rec *memory.MemoryRecord) memoryRow {
	r := memoryRow{
		StableID:    string(rec.StableID),
		Kind:        string(rec.Kind),
		Scope:       string(rec.Scope),
		Field:       rec.Field,
		Value:       string(rec.Value),
		VerifiedBy:  rec.Provenance.VerifiedBy,
		Confidence:  string(rec.Confidence),
		Volatility:  string(rec.Volatility),
		ContentHash: string(DeriveStableID(rec)),
		CreatedAt:   formatTime(rec.CreatedAt),
		UpdatedAt:   formatTime(rec.UpdatedAt),
	}
	r.SubjectKind = rec.Subject.Kind
	r.SubjectName = rec.Subject.Name
	r.SubjectID = rec.Subject.CanonicalID
	if len(rec.Subject.Aliases) > 0 {
		b, _ := json.Marshal(rec.Subject.Aliases)
		r.SubjectAli = string(b)
	}
	if len(rec.Provenance.Sources) > 0 {
		b, _ := json.Marshal(rec.Provenance.Sources)
		r.Sources = string(b)
	}
	if len(rec.Provenance.DerivedFrom) > 0 {
		b, _ := json.Marshal(rec.Provenance.DerivedFrom)
		r.DerivedFrom = string(b)
	}
	if rec.Provenance.UserCorrected {
		r.UserCorr = 1
	}
	r.CorrBy = rec.Provenance.CorrectedBy
	r.CorrAt = formatTime(rec.Provenance.CorrectedAt)
	r.LastVerif = formatTime(rec.Freshness.LastVerifiedAt)
	r.StaleAfter = rec.Freshness.StaleAfter.Nanoseconds()
	r.ExpiresAt = formatTime(rec.Expiration.ExpiresAt)
	r.Status = string(rec.Expiration.InvalidationStatus)
	r.SupersedBy = rec.Expiration.SupersededBy
	r.Reason = rec.Expiration.InvalidationReason
	if len(rec.Provenance.DerivedFrom) > 0 {
		r.DerivedHead = rec.Provenance.DerivedFrom[len(rec.Provenance.DerivedFrom)-1]
	}
	if rec.Kind == memory.KindSuggestion {
		if v, err := rec.DecodeValue(); err == nil {
			r.SuggStatus = string(v.(memory.SuggestionRecord).Status)
		}
	}
	return r
}

// ---- Store implementation ------------------------------------------------

// Get retrieves a record by its stable ID. Returns (nil, nil) if absent.
func (s *SQLiteStore) Get(ctx context.Context, id memory.MemoryStableID) (*memory.MemoryRecord, error) {
	row, err := scanMemory(s.db.QueryRowContext(ctx, "SELECT "+memoryColumns+" FROM memories WHERE stable_id = ?", string(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get memory %s: %w", id, err)
	}
	return recordFromRow(row), nil
}

// Delete removes a record. Returns NotFoundError if absent.
func (s *SQLiteStore) Delete(ctx context.Context, id memory.MemoryStableID) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM memories WHERE stable_id = ?", string(id))
	if err != nil {
		return fmt.Errorf("delete memory %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return NotFoundError{ID: id}
	}
	return nil
}

// Count returns the number of stored records.
func (s *SQLiteStore) Count(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM memories").Scan(&n); err != nil {
		return 0, fmt.Errorf("count memories: %w", err)
	}
	return n, nil
}

// Write inserts or replaces a record (idempotent for identical content).
func (s *SQLiteStore) Write(ctx context.Context, rec *memory.MemoryRecord) error {
	if err := s.prepareForWrite(rec); err != nil {
		return err
	}
	row := rowFromRecord(rec)
	now := nowUTC()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO memories (
			stable_id, schema_version, kind, scope,
			subject_kind, subject_name, subject_canonical_id, subject_aliases,
			field, value, sources, derived_from, verified_by, user_corrected,
			corrected_by, corrected_at, confidence, volatility,
			last_verified_at, stale_after_ns, expires_at, invalidation_status,
			superseded_by, invalidation_reason, derived_head, suggestion_status,
			content_hash, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(stable_id) DO UPDATE SET
			schema_version = excluded.schema_version,
			kind = excluded.kind,
			scope = excluded.scope,
			subject_kind = excluded.subject_kind,
			subject_name = excluded.subject_name,
			subject_canonical_id = excluded.subject_canonical_id,
			subject_aliases = excluded.subject_aliases,
			field = excluded.field,
			value = excluded.value,
			sources = excluded.sources,
			derived_from = excluded.derived_from,
			verified_by = excluded.verified_by,
			user_corrected = excluded.user_corrected,
			corrected_by = excluded.corrected_by,
			corrected_at = excluded.corrected_at,
			confidence = excluded.confidence,
			volatility = excluded.volatility,
			last_verified_at = excluded.last_verified_at,
			stale_after_ns = excluded.stale_after_ns,
			expires_at = excluded.expires_at,
			invalidation_status = excluded.invalidation_status,
			superseded_by = excluded.superseded_by,
			invalidation_reason = excluded.invalidation_reason,
			derived_head = excluded.derived_head,
			suggestion_status = excluded.suggestion_status,
			content_hash = excluded.content_hash,
			updated_at = excluded.updated_at
	`,
		row.StableID, memory.SchemaVersion, row.Kind, row.Scope,
		row.SubjectKind, row.SubjectName, row.SubjectID, row.SubjectAli,
		row.Field, row.Value, row.Sources, row.DerivedFrom, row.VerifiedBy, row.UserCorr,
		row.CorrBy, row.CorrAt, row.Confidence, row.Volatility,
		row.LastVerif, row.StaleAfter, row.ExpiresAt, row.Status,
		row.SupersedBy, row.Reason, row.DerivedHead, row.SuggStatus,
		row.ContentHash, row.CreatedAt, now,
	)
	if err != nil {
		return fmt.Errorf("write memory %s: %w", rec.StableID, err)
	}
	return nil
}

// Create inserts a record that must not exist yet.
func (s *SQLiteStore) Create(ctx context.Context, rec *memory.MemoryRecord) error {
	if err := s.prepareForWrite(rec); err != nil {
		return err
	}
	var exists int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM memories WHERE stable_id = ?", string(rec.StableID)).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check memory %s: %w", rec.StableID, err)
	}
	if exists > 0 {
		return AlreadyExistsError{ID: rec.StableID}
	}
	return s.Write(ctx, rec)
}

// prepareForWrite runs the pre-write gates: validation + sanitization.
//
// Sanitization may redact the free-form value payload in place (idempotent);
// any region that is not clean afterwards fails the write.
func (s *SQLiteStore) prepareForWrite(rec *memory.MemoryRecord) error {
	if err := validateRecord(rec); err != nil {
		return err
	}
	if s.sanitizer != nil {
		if err := s.sanitizer.Check(rec); err != nil {
			return err
		}
	}
	return nil
}

// Query implements filtered retrieval.
func (s *SQLiteStore) Query(ctx context.Context, q Query) ([]*memory.MemoryRecord, error) {
	var conds []string
	var args []any

	if q.Kind != "" {
		conds = append(conds, "kind = ?")
		args = append(args, string(q.Kind))
	}
	if q.Scope != "" {
		conds = append(conds, "scope = ?")
		args = append(args, string(q.Scope))
	}
	if q.SubjectKind != "" {
		conds = append(conds, "subject_kind = ?")
		args = append(args, q.SubjectKind)
	}
	if q.SubjectName != "" {
		conds = append(conds, "subject_name = ?")
		args = append(args, q.SubjectName)
	}
	if q.SubjectID != "" {
		conds = append(conds, "subject_canonical_id = ?")
		args = append(args, q.SubjectID)
	}
	if q.Field != "" {
		conds = append(conds, "field = ?")
		args = append(args, q.Field)
	}

	switch q.Status {
	case StatusActive:
		conds = append(conds, "invalidation_status = 'active'")
		// Absolute expiry: a record whose expires_at is in the past is not
		// usable and must not appear in active queries (the record-level
		// IsUsable gate is re-applied per record below).
		conds = append(conds, "(expires_at = '' OR expires_at > ?)")
		args = append(args, time.Now().UTC().Format(timeFormat))
	case StatusAny:
		// no filter
	default:
		return nil, fmt.Errorf("unknown query status %q", q.Status)
	}

	if !q.StaleAt.IsZero() {
		// Staleness is per-record (each record has its own window), so the
		// SQL pre-filter only drops records whose window provably excludes
		// the query time; the exact per-record check happens in Go.
		// Records from the far future are never stale.
		t := q.StaleAt
		if t.Year() < 3000 {
			conds = append(conds, "last_verified_at <= ?")
			args = append(args, t.Format(timeFormat))
		}
	}

	query := "SELECT " + memoryColumns + " FROM memories"
	if len(conds) > 0 {
		query += " WHERE " + strings.Join(conds, " AND ")
	}
	query += " ORDER BY stable_id ASC"
	if q.Limit > 0 {
		// q.Limit is an int formatted with Itoa; the result is a numeric
		// literal and cannot inject SQL.
		query += " LIMIT " + strconv.Itoa(q.Limit) // #nosec G202 -- q.Limit is an int, not user input
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query memories: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []*memory.MemoryRecord
	for rows.Next() {
		r, err := scanMemory(rows)
		if err != nil {
			return nil, fmt.Errorf("scan memory: %w", err)
		}
		rec := recordFromRow(r)
		// The stale window is per-record, so the coarse SQL filter above
		// is refined here with the record's own Freshness.
		if !q.StaleAt.IsZero() && !rec.Freshness.IsStale(q.StaleAt) {
			continue
		}
		// Active queries return only usable records (lifecycle + expiry).
		if q.Status == StatusActive && !rec.IsUsable(time.Now().UTC()) {
			continue
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate memories: %w", err)
	}
	return out, nil
}

// Supersede marks an old record as superseded by newID.
func (s *SQLiteStore) Supersede(ctx context.Context, oldID, newID, reason string) (*memory.MemoryRecord, error) {
	old := memory.MemoryStableID(oldID)
	if newID == "" {
		return nil, SupersedeError{ID: old, Reason: "new ID is empty"}
	}
	return s.transition(ctx, old, memory.InvalidationSuperseded, newID, reason)
}

// Invalidate marks an active record as invalidated.
func (s *SQLiteStore) Invalidate(ctx context.Context, id memory.MemoryStableID, reason string) (*memory.MemoryRecord, error) {
	return s.transition(ctx, id, memory.InvalidationInvalidated, "", reason)
}

// transition applies a lifecycle transition to a record inside a
// transaction (read-check-update is atomic).
func (s *SQLiteStore) transition(
	ctx context.Context,
	id memory.MemoryStableID,
	to memory.InvalidationStatus,
	target, reason string,
) (*memory.MemoryRecord, error) {
	var err error
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	row, err := scanMemory(tx.QueryRowContext(ctx, "SELECT "+memoryColumns+" FROM memories WHERE stable_id = ?", string(id)))
	if errors.Is(err, sql.ErrNoRows) {
		err = NotFoundError{ID: id}
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("read memory %s: %w", id, err)
	}

	rec := recordFromRow(row)
	if rec.Expiration.InvalidationStatus != memory.InvalidationActive {
		why := "record is " + string(rec.Expiration.InvalidationStatus) + ", not active"
		if to == memory.InvalidationSuperseded {
			err = SupersedeError{ID: id, Reason: why}
		} else {
			err = InvalidateError{ID: id, Reason: why}
		}
		return nil, err
	}

	now := time.Now().UTC()
	rec.Expiration.InvalidationStatus = to
	rec.Expiration.SupersededBy = target
	rec.Expiration.InvalidationReason = reason
	rec.UpdatedAt = now

	row = rowFromRecord(rec)
	_, err = tx.ExecContext(ctx, `
		UPDATE memories SET
			schema_version = ?, kind = ?, scope = ?,
			subject_kind = ?, subject_name = ?, subject_canonical_id = ?, subject_aliases = ?,
			field = ?, value = ?, sources = ?, derived_from = ?, verified_by = ?,
			user_corrected = ?, corrected_by = ?, corrected_at = ?,
			confidence = ?, volatility = ?, last_verified_at = ?, stale_after_ns = ?,
			expires_at = ?, invalidation_status = ?, superseded_by = ?, invalidation_reason = ?,
			derived_head = ?, suggestion_status = ?, content_hash = ?,
			created_at = ?, updated_at = ?
		WHERE stable_id = ?
	`,
		memory.SchemaVersion, row.Kind, row.Scope,
		row.SubjectKind, row.SubjectName, row.SubjectID, row.SubjectAli,
		row.Field, row.Value, row.Sources, row.DerivedFrom, row.VerifiedBy, row.UserCorr,
		row.CorrBy, row.CorrAt, row.Confidence, row.Volatility,
		row.LastVerif, row.StaleAfter, row.ExpiresAt, row.Status,
		row.SupersedBy, row.Reason, row.DerivedHead, row.SuggStatus,
		row.ContentHash, row.CreatedAt, formatTime(now),
		string(id),
	)
	if err != nil {
		return nil, fmt.Errorf("update memory %s: %w", id, err)
	}
	err = tx.Commit()
	if err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	return rec, nil
}
