// Package archive provides the SQLite implementation of the Store interface.
//
// This implementation provides:
//   - Versioned schema migrations
//   - Foreign key constraints
//   - Transactional document+chunk ingestion
//   - Idempotent upsert operations
//   - Portable vector storage (JSON-encoded float32 arrays)
//   - Configurable database path
//
// Vector storage format:
//   - Vectors are stored as JSON-encoded []float32 in a BLOB column
//   - This avoids dependencies on SQLite vector extensions
//   - Compatible with v0.3 requirements for portable storage
//
// Idempotency rules:
//   - CreateDocument fails if document exists (use UpsertDocument for upserts)
//   - UpsertDocument replaces document and all chunks atomically
//   - Re-ingesting unchanged content produces identical IDs (hash-based)
//   - Chunk IDs include content hash, so changed content gets new chunks
//
// Example usage:
//
//	store, err := archive.NewSQLiteStore("/path/to/archive.db")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer store.Close()
//
//	err = store.UpsertDocument(ctx, doc, chunks)
//	if err != nil {
//	    log.Fatal(err)
//	}
package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
	_ "modernc.org/sqlite"
)

// SQLiteStore is a Store implementation backed by SQLite.
//
// It supports:
//   - Atomic document+chunk ingestion via transactions
//   - Foreign key constraints for referential integrity
//   - Idempotent upsert operations
//   - Portable vector storage without external extensions
//   - Configurable database path
//
// The store creates the database file if it doesn't exist and runs
// migrations automatically on first connection.
type SQLiteStore struct {
	db       *sql.DB
	dbPath   string
	migrated bool
}

// Ensure SQLiteStore implements Store interface
var _ Store = (*SQLiteStore)(nil)

// NewSQLiteStore creates a new SQLite-backed Store.
//
// The database file is created if it doesn't exist. The store runs
// migrations automatically on first connection.
//
// Parameters:
//   - dbPath: Path to the SQLite database file. Use ":memory:" for in-memory database.
//
// Returns:
//   - A configured SQLiteStore with migrations applied
//   - An error if the database cannot be opened or migrations fail
//
// Example:
//
//	store, err := archive.NewSQLiteStore("./archive.db")
//	if err != nil {
//	    log.Fatal(err)
//	}
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	store := &SQLiteStore{
		dbPath: dbPath,
	}

	if err := store.open(); err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	return store, nil
}

// NewSQLiteStoreWithConfig creates a new SQLite-backed Store with options.
//
// Parameters:
//   - dbPath: Path to the SQLite database file
//   - opts: Optional configuration options
//
// Returns:
//   - A configured SQLiteStore with migrations applied
//   - An error if the database cannot be opened or migrations fail
func NewSQLiteStoreWithConfig(dbPath string, opts ...StoreOption) (*SQLiteStore, error) {
	config := defaultStoreConfig()
	for _, opt := range opts {
		opt(&config)
	}

	store := &SQLiteStore{
		dbPath: dbPath,
	}

	if err := store.openWithConfig(config); err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	return store, nil
}

// StoreOption is a configuration option for SQLiteStore.
type StoreOption func(*storeConfig)

// storeConfig holds optional configuration for SQLiteStore.
type storeConfig struct {
	// MaxOpenConns is the maximum number of open connections to the database.
	// Default: 2 (sufficient for single-writer pattern)
	MaxOpenConns int

	// MaxIdleConns is the maximum number of idle connections.
	// Default: 1
	MaxIdleConns int

	// ConnMaxLifetime is the maximum amount of time a connection may be reused.
	// Default: 0 (no limit)
	ConnMaxLifetime time.Duration

	// EnableWal enables WAL mode for better concurrency.
	// Default: true
	EnableWal bool
}

func defaultStoreConfig() storeConfig {
	return storeConfig{
		MaxOpenConns:  2,
		MaxIdleConns:  1,
		ConnMaxLifetime: 0,
		EnableWal:     true,
	}
}

func (c *storeConfig) apply(db *sql.DB) error {
	db.SetMaxOpenConns(c.MaxOpenConns)
	db.SetMaxIdleConns(c.MaxIdleConns)
	if c.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(c.ConnMaxLifetime)
	}
	return nil
}

// WithMaxOpenConns sets the maximum number of open database connections.
func WithMaxOpenConns(n int) StoreOption {
	return func(c *storeConfig) {
		c.MaxOpenConns = n
	}
}

// WithMaxIdleConns sets the maximum number of idle database connections.
func WithMaxIdleConns(n int) StoreOption {
	return func(c *storeConfig) {
		c.MaxIdleConns = n
	}
}

// WithConnMaxLifetime sets the maximum lifetime for database connections.
func WithConnMaxLifetime(d time.Duration) StoreOption {
	return func(c *storeConfig) {
		c.ConnMaxLifetime = d
	}
}

// WithWAL disables WAL mode (false) or enables it (true).
func WithWAL(enable bool) StoreOption {
	return func(c *storeConfig) {
		c.EnableWal = enable
	}
}

// open opens the database connection and runs migrations.
func (s *SQLiteStore) open() error {
	return s.openWithConfig(defaultStoreConfig())
}

// openWithConfig opens the database with custom configuration.
func (s *SQLiteStore) openWithConfig(config storeConfig) error {
	var err error
	s.db, err = sql.Open("sqlite", s.dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	if err := config.apply(s.db); err != nil {
		s.db.Close()
		return err
	}

	// Enable foreign keys
	_, err = s.db.Exec("PRAGMA foreign_keys = ON")
	if err != nil {
		s.db.Close()
		return fmt.Errorf("failed to enable foreign keys: %w", err)
	}

	// Enable WAL mode if requested
	if config.EnableWal {
		_, err = s.db.Exec("PRAGMA journal_mode = WAL")
		if err != nil {
			s.db.Close()
			return fmt.Errorf("failed to enable WAL mode: %w", err)
		}
	}

	// Set busy timeout (30 seconds for concurrent access)
	_, err = s.db.Exec("PRAGMA busy_timeout = 30000")
	if err != nil {
		s.db.Close()
		return fmt.Errorf("failed to set busy timeout: %w", err)
	}

	// Enable writable temporary tables for better concurrent access
	_, err = s.db.Exec("PRAGMA temp_store = MEMORY")
	if err != nil {
		s.db.Close()
		return fmt.Errorf("failed to set temp_store: %w", err)
	}

	// Run migrations
	if err := s.runMigrations(); err != nil {
		s.db.Close()
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	return nil
}

// runMigrations applies pending schema migrations.
func (s *SQLiteStore) runMigrations() error {
	if s.migrated {
		return nil
	}

	// Create migrations table if it doesn't exist
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create migrations table: %w", err)
	}

	// Get current schema version
	var currentVersion int
	err = s.db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&currentVersion)
	if err != nil {
		return fmt.Errorf("failed to get current schema version: %w", err)
	}

	// Load and apply migrations
	migrations, err := s.loadMigrations()
	if err != nil {
		return fmt.Errorf("failed to load migrations: %w", err)
	}

	for _, migration := range migrations {
		if migration.Version <= currentVersion {
			continue
		}

		if err := s.applyMigration(migration); err != nil {
			return fmt.Errorf("failed to apply migration %d: %w", migration.Version, err)
		}
	}

	s.migrated = true
	return nil
}

// migration represents a single migration file.
type migration struct {
	Version int
	Path    string
	Content string
}

// loadMigrations loads all migration files from the migrations directory.
func (s *SQLiteStore) loadMigrations() ([]migration, error) {
	var migrations []migration

	// Try different migration directory paths
	migrationPaths := []string{
		"internal/archive/migrations",
		"./internal/archive/migrations",
		"migrations",
	}

	var migrationDir string
	for _, path := range migrationPaths {
		// Check if the path exists
		if _, err := os.Stat(path); err == nil {
			migrationDir = path
			break
		}
	}

	if migrationDir == "" {
		return nil, errors.New("migrations directory not found")
	}

	// Read directory entries using os package
	entries, err := os.ReadDir(migrationDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read migrations directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}

		// Parse version from filename (e.g., "001_initial_schema.sql" -> 1)
		parts := strings.Split(name, "_")
		if len(parts) < 1 {
			continue
		}

		versionStr := strings.TrimPrefix(parts[0], "0")
		version, err := strconv.Atoi(versionStr)
		if err != nil {
			continue
		}

		// Read migration content
		path := filepath.Join(migrationDir, name)
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("failed to read migration file %s: %w", name, err)
		}

		migrations = append(migrations, migration{
			Version: version,
			Path:    path,
			Content: string(content),
		})
	}

	// Sort by version
	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})

	return migrations, nil
}

// applyMigration applies a single migration.
func (s *SQLiteStore) applyMigration(m migration) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	// Run migration SQL
	_, err = tx.Exec(m.Content)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to execute migration: %w", err)
	}

	// Record migration
	_, err = tx.Exec(
		"INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)",
		m.Version,
		time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to record migration: %w", err)
	}

	return tx.Commit()
}

// Close closes the database connection.
func (s *SQLiteStore) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// Helper functions for vector storage

// encodeVector encodes a float32 slice as base64-encoded JSON.
func encodeVector(v []float32) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal vector: %w", err)
	}
	return data, nil
}

// decodeVector decodes a base64-encoded JSON vector.
func decodeVector(data []byte) ([]float32, error) {
	var v []float32
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("failed to unmarshal vector: %w", err)
	}
	return v, nil
}

// Time formatting constants
const timeFormat = "2006-01-02 15:04:05.999999999"
const timeFormatCompact = "2006-01-02T15:04:05.999999999Z07:00"

func nowUTC() string {
	return time.Now().UTC().Format(timeFormat)
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(timeFormat, s)
}

func parseTimeCompact(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, s)
}

func parseSourceType(s string) types.SourceType {
	if s == "" {
		return types.SourceTypeUnknown
	}
	return types.SourceType(s)
}

// ===============================================
// Document Operations
// ===============================================

// CreateDocument implements Store.CreateDocument.
func (s *SQLiteStore) CreateDocument(ctx context.Context, doc *ArchiveDocument, chunks []Chunk) error {
	if doc == nil {
		return errors.New("document cannot be nil")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Check if document already exists
	var exists int
	err = tx.QueryRow("SELECT COUNT(*) FROM documents WHERE stable_id = ?", doc.StableID).Scan(&exists)
	if err != nil {
		return fmt.Errorf("failed to check document existence: %w", err)
	}
	if exists > 0 {
		return &DocumentExistsError{StableID: doc.StableID}
	}

	// Insert document
	_, err = tx.Exec(`
		INSERT INTO documents (
			stable_id, plain_text, plain_text_hash, chunk_count,
			source_id, source_url, source_type, retrieved_at, extracted_at,
			archived_at, metadata, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, doc.StableID, doc.PlainText, doc.PlainTextHash, doc.ChunkCount,
		doc.SourceProvenance.SourceID,
		doc.SourceProvenance.SourceURL,
		string(doc.SourceProvenance.SourceType),
		doc.SourceProvenance.RetrievedAt.UTC().Format(timeFormat),
		doc.SourceProvenance.ExtractedAt.UTC().Format(timeFormat),
		doc.ArchivedAt.UTC().Format(timeFormat),
		s.toJson(doc.Metadata),
		nowUTC(),
		nowUTC(),
	)
	if err != nil {
		return fmt.Errorf("failed to insert document: %w", err)
	}

	// Insert chunks
	for _, chunk := range chunks {
		// Use document's stable ID for the chunk's document reference
		// (allows tests to omit DocumentStableID in chunk structs)
		chunkDocID := chunk.DocumentStableID
		if chunkDocID == "" {
			chunkDocID = doc.StableID
		}

		_, err = tx.Exec(`
			INSERT INTO chunks (
				stable_id, document_stable_id, content_hash, position, length,
				has_embedding, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, chunk.StableID, chunkDocID, chunk.ContentHash,
			chunk.Position, chunk.Length,
			boolToInt(chunk.HasEmbedding),
			nowUTC(),
			nowUTC(),
		)
		if err != nil {
			return fmt.Errorf("failed to insert chunk %s: %w", chunk.StableID, err)
		}
	}
	return tx.Commit()
}

// UpsertDocument implements Store.UpsertDocument.
func (s *SQLiteStore) UpsertDocument(ctx context.Context, doc *ArchiveDocument, chunks []Chunk) error {
	if doc == nil {
		return errors.New("document cannot be nil")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Upsert document
	_, err = tx.Exec(`
		INSERT INTO documents (
			stable_id, plain_text, plain_text_hash, chunk_count,
			source_id, source_url, source_type, retrieved_at, extracted_at,
			archived_at, metadata, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(stable_id) DO UPDATE SET
			plain_text = excluded.plain_text,
			plain_text_hash = excluded.plain_text_hash,
			chunk_count = excluded.chunk_count,
			source_id = excluded.source_id,
			source_url = excluded.source_url,
			source_type = excluded.source_type,
			retrieved_at = excluded.retrieved_at,
			extracted_at = excluded.extracted_at,
			archived_at = excluded.archived_at,
			metadata = excluded.metadata,
			updated_at = excluded.updated_at
	`, doc.StableID, doc.PlainText, doc.PlainTextHash, doc.ChunkCount,
		doc.SourceProvenance.SourceID,
		doc.SourceProvenance.SourceURL,
		string(doc.SourceProvenance.SourceType),
		doc.SourceProvenance.RetrievedAt.UTC().Format(timeFormat),
		doc.SourceProvenance.ExtractedAt.UTC().Format(timeFormat),
		doc.ArchivedAt.UTC().Format(timeFormat),
		s.toJson(doc.Metadata),
		nowUTC(),
		nowUTC(),
	)
	if err != nil {
		return fmt.Errorf("failed to upsert document: %w", err)
	}

	// Delete existing chunks for this document
	_, err = tx.Exec("DELETE FROM chunks WHERE document_stable_id = ?", doc.StableID)
	if err != nil {
		return fmt.Errorf("failed to delete existing chunks: %w", err)
	}

	// Insert new chunks
	for _, chunk := range chunks {
		// Use document's stable ID for the chunk's document reference
		// (allows tests to omit DocumentStableID in chunk structs)
		chunkDocID := chunk.DocumentStableID
		if chunkDocID == "" {
			chunkDocID = doc.StableID
		}

		_, err = tx.Exec(`
			INSERT INTO chunks (
				stable_id, document_stable_id, content_hash, position, length,
				has_embedding, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, chunk.StableID, chunkDocID, chunk.ContentHash,
			chunk.Position, chunk.Length,
			boolToInt(chunk.HasEmbedding),
			nowUTC(),
			nowUTC(),
		)
		if err != nil {
			return fmt.Errorf("failed to insert chunk %s: %w", chunk.StableID, err)
		}
	}
	return tx.Commit()
}

// GetDocument implements Store.GetDocument.
func (s *SQLiteStore) GetDocument(ctx context.Context, id StableDocumentID) (*ArchiveDocument, error) {
	var (
		plainText        string
		plainTextHash    string
		chunkCount       int
		sourceID         string
		sourceURL        sql.NullString
		sourceType       string
		retrievedAt      string
		extractedAt      string
		archivedAt       string
		metadata         string
	)

	err := s.db.QueryRow(`
		SELECT
			plain_text, plain_text_hash, chunk_count,
			source_id, source_url, source_type,
			retrieved_at, extracted_at, archived_at,
			metadata
		FROM documents WHERE stable_id = ?
	`, id).Scan(&plainText, &plainTextHash, &chunkCount, &sourceID, &sourceURL, &sourceType, &retrievedAt, &extractedAt, &archivedAt, &metadata)

	if err == sql.ErrNoRows {
		return nil, DocumentNotFoundError{StableID: id}
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get document: %w", err)
	}

	// Parse time values
	retrievedAtTime, err := parseTime(retrievedAt)
	if err != nil {
		retrievedAtTime = time.Time{}
	}
	extractedAtTime, err := parseTime(extractedAt)
	if err != nil {
		extractedAtTime = time.Time{}
	}
	archivedAtTime, err := parseTime(archivedAt)
	if err != nil {
		archivedAtTime = time.Time{}
	}

	// Parse metadata
	var docMetadata map[string]string
	if metadata != "" {
		json.Unmarshal([]byte(metadata), &docMetadata)
	}

	return &ArchiveDocument{
		StableID:      id,
		PlainText:     plainText,
		PlainTextHash: ContentHash(plainTextHash),
		ChunkCount:    chunkCount,
		SourceProvenance: SourceProvenance{
			SourceID:    sourceID,
			SourceURL:   sourceURL.String,
			SourceType:  parseSourceType(sourceType),
			RetrievedAt: retrievedAtTime,
			ExtractedAt: extractedAtTime,
			ExtractionMetadata: docMetadata,
		},
		ArchivedAt: archivedAtTime,
		Metadata:   docMetadata,
	}, nil
}

// DeleteDocument implements Store.DeleteDocument.
func (s *SQLiteStore) DeleteDocument(ctx context.Context, id StableDocumentID) error {
	result, err := s.db.Exec("DELETE FROM documents WHERE stable_id = ?", id)
	if err != nil {
		return fmt.Errorf("failed to delete document: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to count deleted rows: %w", err)
	}
	if rows == 0 {
		return DocumentNotFoundError{StableID: id}
	}

	return nil
}

// ListDocuments implements Store.ListDocuments.
func (s *SQLiteStore) ListDocuments(ctx context.Context, limit int) ([]*ArchiveDocument, error) {
	query := `
		SELECT
			stable_id, plain_text, plain_text_hash, chunk_count,
			source_id, source_url, source_type,
			retrieved_at, extracted_at, archived_at,
			metadata
		FROM documents
		ORDER BY archived_at ASC
	`

	if limit > 0 {
		query = query + fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := s.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to list documents: %w", err)
	}
	defer rows.Close()

	var docs []*ArchiveDocument
	for rows.Next() {
		var (
			stableID       string
			plainText      string
			plainTextHash  string
			chunkCount     int
			sourceID       string
			sourceURL      sql.NullString
			sourceType     string
			retrievedAt    string
			extractedAt    string
			archivedAt     string
			metadata       string
		)

		if err := rows.Scan(&stableID, &plainText, &plainTextHash, &chunkCount, &sourceID, &sourceURL, &sourceType, &retrievedAt, &extractedAt, &archivedAt, &metadata); err != nil {
			return nil, fmt.Errorf("failed to scan document: %w", err)
		}

		// Parse time values
		retrievedAtTime, _ := parseTime(retrievedAt)
		extractedAtTime, _ := parseTime(extractedAt)
		archivedAtTime, _ := parseTime(archivedAt)

		// Parse metadata
		var docMetadata map[string]string
		if metadata != "" {
			json.Unmarshal([]byte(metadata), &docMetadata)
		}

		doc := &ArchiveDocument{
			StableID:      StableDocumentID(stableID),
			PlainText:     plainText,
			PlainTextHash: ContentHash(plainTextHash),
			ChunkCount:    chunkCount,
			SourceProvenance: SourceProvenance{
				SourceID:    sourceID,
				SourceURL:   sourceURL.String,
				SourceType:  parseSourceType(sourceType),
				RetrievedAt: retrievedAtTime,
				ExtractedAt: extractedAtTime,
				ExtractionMetadata: docMetadata,
			},
			ArchivedAt: archivedAtTime,
			Metadata:   docMetadata,
		}
		docs = append(docs, doc)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating documents: %w", err)
	}

	return docs, nil
}

// DocumentExists implements Store.DocumentExists.
func (s *SQLiteStore) DocumentExists(ctx context.Context, id StableDocumentID) (bool, error) {
	var exists int
	err := s.db.QueryRow("SELECT COUNT(*) FROM documents WHERE stable_id = ?", id).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check document existence: %w", err)
	}
	return exists > 0, nil
}

// ===============================================
// Chunk Operations
// ===============================================

// GetChunk implements Store.GetChunk.
func (s *SQLiteStore) GetChunk(ctx context.Context, id StableChunkID) (*Chunk, error) {
	var (
		docID       string
		contentHash string
		position    int
		length      int
		hasEmbed    int
	)

	err := s.db.QueryRow(`
		SELECT document_stable_id, content_hash, position, length, has_embedding
		FROM chunks WHERE stable_id = ?
	`, id).Scan(&docID, &contentHash, &position, &length, &hasEmbed)

	if err == sql.ErrNoRows {
		return nil, ChunkNotFoundError{StableID: id}
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get chunk: %w", err)
	}

	return &Chunk{
		StableID:         id,
		DocumentStableID: StableDocumentID(docID),
		ContentHash:      ContentHash(contentHash),
		Position:         position,
		Length:           length,
		HasEmbedding:     hasEmbed != 0,
	}, nil
}

// GetChunksByDocument implements Store.GetChunksByDocument.
func (s *SQLiteStore) GetChunksByDocument(ctx context.Context, docID StableDocumentID) ([]Chunk, error) {
	rows, err := s.db.Query(`
		SELECT stable_id, content_hash, position, length, has_embedding
		FROM chunks
		WHERE document_stable_id = ?
		ORDER BY position ASC
	`, docID)
	if err != nil {
		return nil, fmt.Errorf("failed to get chunks: %w", err)
	}
	defer rows.Close()

	var chunks []Chunk
	for rows.Next() {
		var contentHash string
		var hasEmbed int
		var chunk Chunk

		if err := rows.Scan(&chunk.StableID, &contentHash, &chunk.Position, &chunk.Length, &hasEmbed); err != nil {
			return nil, fmt.Errorf("failed to scan chunk: %w", err)
		}

		chunk.DocumentStableID = docID
		chunk.ContentHash = ContentHash(contentHash)
		chunk.HasEmbedding = hasEmbed != 0

		chunks = append(chunks, chunk)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating chunks: %w", err)
	}

	return chunks, nil
}

// DeleteChunk implements Store.DeleteChunk.
func (s *SQLiteStore) DeleteChunk(ctx context.Context, id StableChunkID) error {
	// Delete embedding metadata and vectors first
	_, err := s.db.Exec("DELETE FROM embedding_metadata WHERE chunk_id = ?", id)
	if err != nil {
		return fmt.Errorf("failed to delete embedding metadata: %w", err)
	}

	_, err = s.db.Exec("DELETE FROM embedding_vectors WHERE chunk_id = ?", id)
	if err != nil {
		return fmt.Errorf("failed to delete embedding vector: %w", err)
	}

	// Delete chunk
	result, err := s.db.Exec("DELETE FROM chunks WHERE stable_id = ?", id)
	if err != nil {
		return fmt.Errorf("failed to delete chunk: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to count deleted rows: %w", err)
	}
	if rows == 0 {
		return ChunkNotFoundError{StableID: id}
	}

	return nil
}

// ===============================================
// Embedding Operations
// ===============================================

// GetEmbeddingMetadata implements Store.GetEmbeddingMetadata.
func (s *SQLiteStore) GetEmbeddingMetadata(ctx context.Context, chunkID StableChunkID) (*EmbeddingMetadata, error) {
	var (
		modelName   string
		dimensions  int
		version     sql.NullString
		generatedAt string
	)

	err := s.db.QueryRow(`
		SELECT model_name, dimensions, version, generated_at
		FROM embedding_metadata
		WHERE chunk_id = ?
	`, chunkID).Scan(&modelName, &dimensions, &version, &generatedAt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get embedding metadata: %w", err)
	}

	// Check if chunk exists
	var chunkExists int
	err = s.db.QueryRow("SELECT COUNT(*) FROM chunks WHERE stable_id = ?", chunkID).Scan(&chunkExists)
	if err != nil {
		return nil, fmt.Errorf("failed to check chunk existence: %w", err)
	}
	if chunkExists == 0 {
		return nil, ChunkNotFoundError{StableID: chunkID}
	}

	generatedAtTime, err := parseTime(generatedAt)
	if err != nil {
		generatedAtTime = time.Time{}
	}

	meta := &EmbeddingMetadata{
		ModelName:   EmbeddingModelName(modelName),
		Dimensions:  EmbeddingDimensions(dimensions),
		Version:     version.String,
		GeneratedAt: generatedAtTime,
	}

	return meta, nil
}

// SetEmbeddingMetadata implements Store.SetEmbeddingMetadata.
func (s *SQLiteStore) SetEmbeddingMetadata(ctx context.Context, chunkID StableChunkID, metadata *EmbeddingMetadata) error {
	// Check if chunk exists
	var chunkExists int
	err := s.db.QueryRow("SELECT COUNT(*) FROM chunks WHERE stable_id = ?", chunkID).Scan(&chunkExists)
	if err != nil {
		return fmt.Errorf("failed to check chunk existence: %w", err)
	}
	if chunkExists == 0 {
		return ChunkNotFoundError{StableID: chunkID}
	}

	now := nowUTC()

	if metadata == nil {
		// Delete existing metadata
		_, err = s.db.Exec("DELETE FROM embedding_metadata WHERE chunk_id = ?", chunkID)
		if err != nil {
			return fmt.Errorf("failed to delete embedding metadata: %w", err)
		}
		// Also delete the vector
		_, err = s.db.Exec("DELETE FROM embedding_vectors WHERE chunk_id = ?", chunkID)
		if err != nil {
			return fmt.Errorf("failed to delete embedding vector: %w", err)
		}
		// Also delete the vector
		_, err = s.db.Exec("DELETE FROM embedding_vectors WHERE chunk_id = ?", chunkID)
		if err != nil {
			return fmt.Errorf("failed to delete embedding vector: %w", err)
		}
		// Mark chunk as not having embedding
		_, err = s.db.Exec("UPDATE chunks SET has_embedding = 0 WHERE stable_id = ?", chunkID)
		if err != nil {
			return fmt.Errorf("failed to update chunk embedding flag: %w", err)
		}
		return nil
	}

	_, err = s.db.Exec(`
		INSERT INTO embedding_metadata (
			chunk_id, model_name, dimensions, version, generated_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(chunk_id) DO UPDATE SET
			model_name = excluded.model_name,
			dimensions = excluded.dimensions,
			version = excluded.version,
			generated_at = excluded.generated_at,
			updated_at = excluded.updated_at
	`, chunkID, string(metadata.ModelName), metadata.Dimensions, metadata.Version,
		metadata.GeneratedAt.UTC().Format(timeFormat),
		now,
		now,
	)
	if err != nil {
		return fmt.Errorf("failed to upsert embedding metadata: %w", err)
	}

	// Mark chunk as having embedding
	_, err = s.db.Exec(`
		UPDATE chunks SET has_embedding = 1, updated_at = ? WHERE stable_id = ?
	`, now, chunkID)
	if err != nil {
		return fmt.Errorf("failed to update chunk embedding flag: %w", err)
	}

	return nil
}

// GetChunksWithEmbeddingMetadata implements Store.GetChunksWithEmbeddingMetadata.
func (s *SQLiteStore) GetChunksWithEmbeddingMetadata(ctx context.Context) ([]Chunk, error) {
	rows, err := s.db.Query(`
		SELECT c.stable_id, c.document_stable_id, c.content_hash, c.position, c.length
		FROM chunks c
		INNER JOIN embedding_metadata em ON c.stable_id = em.chunk_id
	`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get chunks with embeddings: %w", err)
	}
	defer rows.Close()

	var chunks []Chunk
	for rows.Next() {
		var contentHash string
		var chunk Chunk

		if err := rows.Scan(&chunk.StableID, &chunk.DocumentStableID, &contentHash, &chunk.Position, &chunk.Length); err != nil {
			return nil, fmt.Errorf("failed to scan chunk: %w", err)
		}

		chunk.ContentHash = ContentHash(contentHash)
		chunk.HasEmbedding = true

		chunks = append(chunks, chunk)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating chunks: %w", err)
	}

	return chunks, nil
}

// GetEmbeddingVector retrieves the embedding vector for a chunk.
//
// This is a helper method for internal use. The Store interface exposes
// embeddings through the EmbeddingMetadata, not the raw vectors.
func (s *SQLiteStore) GetEmbeddingVector(ctx context.Context, chunkID StableChunkID) ([]float32, error) {
	var vectorBlob []byte

	err := s.db.QueryRow("SELECT vector_blob FROM embedding_vectors WHERE chunk_id = ?", chunkID).Scan(&vectorBlob)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get embedding vector: %w", err)
	}

	return decodeVector(vectorBlob)
}

// SetEmbeddingVector stores the embedding vector for a chunk.
//
// This is a helper method for internal use. Callers should prefer
// SetEmbeddingMetadata, which handles both metadata and vectors.
func (s *SQLiteStore) SetEmbeddingVector(ctx context.Context, chunkID StableChunkID, vector []float32) error {
	data, err := encodeVector(vector)
	if err != nil {
		return fmt.Errorf("failed to encode vector: %w", err)
	}

	now := nowUTC()

	_, err = s.db.Exec(`
		INSERT INTO embedding_vectors (chunk_id, vector_blob, created_at, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(chunk_id) DO UPDATE SET
			vector_blob = excluded.vector_blob,
			updated_at = excluded.updated_at
	`, chunkID, data, now, now)
	if err != nil {
		return fmt.Errorf("failed to upsert embedding vector: %w", err)
	}

	return nil
}

// ===============================================
// Retrieval Operations
// ===============================================

// Retrieve implements Store.Retrieve.
func (s *SQLiteStore) Retrieve(ctx context.Context, query RetrievalQuery) (*RetrieveResult, error) {
	var hits []RetrievalHit

	// Default limit
	limit := query.Limit
	if limit <= 0 {
		limit = 10
	}

	// Simple text-based retrieval (placeholder for semantic search)
	if query.Text != "" {
		// Build query for keyword matching
		queryPattern := "%" + strings.ToLower(query.Text) + "%"

		rows, err := s.db.Query(`
			SELECT
				d.stable_id,
				d.plain_text,
				d.source_id,
				d.source_url,
				d.source_type,
				d.retrieved_at,
				d.archived_at
			FROM documents d
			WHERE LOWER(d.plain_text) LIKE ?
			ORDER BY d.archived_at DESC
			LIMIT ?
		`, queryPattern, limit)
		if err != nil {
			return nil, fmt.Errorf("failed to execute retrieval query: %w", err)
		}
		for rows.Next() {
			var (
				stableID    string
				plainText   string
				sourceID    string
				sourceURL   sql.NullString
				sourceType  string
				retrievedAt string
				archivedAt  string
			)

			if err := rows.Scan(&stableID, &plainText, &sourceID, &sourceURL, &sourceType, &retrievedAt, &archivedAt); err != nil {
				return nil, fmt.Errorf("failed to scan result: %w", err)
			}

			retrievedAtTime, _ := parseTime(retrievedAt)

			hit := RetrievalHit{
				TargetType:       RetrievalTargetDocument,
				DocumentStableID: StableDocumentID(stableID),
				SourceID:         sourceID,
				SourceURL:        sourceURL.String,
				SourceType:       parseSourceType(sourceType),
				RetrievedAt:      retrievedAtTime,
				RelevanceScore:   0.8, // Simple score for keyword match
				DocumentContent:  &plainText,
			}
			hits = append(hits, hit)
		}
	}

	// Sort by relevance score descending
	sort.Slice(hits, func(i, j int) bool {
		return hits[i].RelevanceScore > hits[j].RelevanceScore
	})

	return &RetrieveResult{
		Hits:      hits,
		TotalHits: len(hits),
		Query:     query,
	}, nil
}

// HealthCheck implements Store.HealthCheck.
func (s *SQLiteStore) HealthCheck(ctx context.Context) error {
	if s.db == nil {
		return errors.New("database is not initialized")
	}

	err := s.db.Ping()
	if err != nil {
		return fmt.Errorf("database ping failed: %w", err)
	}

	// Run a simple query to verify database is accessible
	var version string
	err = s.db.QueryRow("SELECT sqlite_version()").Scan(&version)
	if err != nil {
		return fmt.Errorf("failed to query database: %w", err)
	}

	if version == "" {
		return errors.New("database returned no version")
	}

	return nil
}

// Helper functions

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *SQLiteStore) toJson(m map[string]string) string {
	if m == nil || len(m) == 0 {
		return ""
	}
	data, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(data)
}

// ===============================================
// Transaction helper
// ===============================================

// BeginTx begins a new transaction.
func (s *SQLiteStore) BeginTx(ctx context.Context) (*sql.Tx, error) {
	return s.db.BeginTx(ctx, nil)
}
