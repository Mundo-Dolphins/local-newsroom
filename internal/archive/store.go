// Package archive defines domain contracts for the local historical archive and RAG layer.
//
// This package provides storage-neutral contracts for:
//   - Archiving normalized newsroom documents with stable identities
//   - Chunking documents with deterministic, position-based identifiers
//   - Storing embedding metadata without exposing provider response types
//   - Retrieval queries and results with provenance
//
// Key design principles:
//   - Stable IDs: Document and chunk IDs persist across re-indexing operations
//   - Provider abstraction: Embedding metadata captures model info without coupling to providers
//   - Provenance preservation: All archive entities link back to original sources
//   - Testability: Simple interfaces suitable for mocking in tests
//
// The archive layer is intentionally neutral: it does not specify storage (SQLite, etc.),
// embedding providers (oMLX, etc.), or chunking strategies. These are implementation
// details that can be swapped without affecting the domain contracts.
//
// Example usage:
//
//	store := archive.NewSQLiteStore(db) // or a fake for tests
//	doc := &archive.ArchiveDocument{
//		StableID:      computeDocID(source),
//		SourceID:      source.StableID,
//		PlainTextHash: computeHash(doc.PlainText),
//	}
//	err := store.UpsertDocument(ctx, doc, chunks)
package archive

import "context"

// Store is the interface for interacting with the archive.
//
// This interface provides all operations needed to:
//   - Store and retrieve documents and chunks
//   - Track embedding metadata per chunk
//   - Perform retrieval queries (semantic or otherwise)
//
// Implementations should:
//   - Be concurrency-safe if used by multiple goroutines
//   - Preserve document stability (same content -> same ID)
//   - Support upsert operations for idempotent indexing
//   - Not require embeddings for basic operations
//
// A fake implementation (archive.FakeStore) is provided for testing.
type Store interface {
	// Document operations ----------------------------------------------------

	// CreateDocument inserts a new document and its chunks.
	//
	// Returns an error if a document with the same StableID already exists.
	// Use UpsertDocument for idempotent operations.
	//
	// The chunks slice must contain chunks for this document, with
	// StableID values derived from the document's StableID and content.
	CreateDocument(ctx context.Context, doc *ArchiveDocument, chunks []Chunk) error

	// UpsertDocument inserts or updates a document and its chunks.
	//
	// If a document with the same StableID exists, it is replaced along with
	// its chunks. This is useful for idempotent re-indexing operations.
	//
	// The chunks slice must contain all chunks for this document.
	UpsertDocument(ctx context.Context, doc *ArchiveDocument, chunks []Chunk) error

	// GetDocument retrieves a document by its stable ID.
	//
	// Returns nil, nil if the document does not exist.
	// Returns nil, DocumentNotFoundError if the document does not exist.
	GetDocument(ctx context.Context, id StableDocumentID) (*ArchiveDocument, error)

	// DeleteDocument removes a document and all its chunks.
	//
	// Returns DocumentNotFoundError if the document does not exist.
	// Note: DeleteChunk can also be used to remove individual chunks.
	DeleteDocument(ctx context.Context, id StableDocumentID) error

	// ListDocuments retrieves all documents in the archive.
	//
	// The returned slice is ordered by ArchivedAt ascending (oldest first).
	// The Limit parameter can be used to restrict the number of results.
	// If Limit is zero or negative, all documents are returned.
	ListDocuments(ctx context.Context, limit int) ([]*ArchiveDocument, error)

	// DocumentExists checks if a document exists in the archive.
	DocumentExists(ctx context.Context, id StableDocumentID) (bool, error)

	// Chunk operations --------------------------------------------------------

	// GetChunk retrieves a chunk by its stable ID.
	//
	// Returns nil, nil if the chunk does not exist.
	// Returns nil, ChunkNotFoundError if the chunk does not exist.
	GetChunk(ctx context.Context, id StableChunkID) (*Chunk, error)

	// GetChunksByDocument retrieves all chunks for a document.
	//
	// Chunks are returned in Position order (ascending).
	GetChunksByDocument(ctx context.Context, docID StableDocumentID) ([]Chunk, error)

	// DeleteChunk removes a single chunk.
	//
	// Returns ChunkNotFoundError if the chunk does not exist.
	// Note: This does not update the document's ChunkCount; that must be done
	// separately if needed.
	DeleteChunk(ctx context.Context, id StableChunkID) error

	// Embedding operations ----------------------------------------------------

	// GetEmbeddingMetadata retrieves the embedding metadata for a chunk.
	//
	// Returns nil, nil if the chunk has no embedding metadata.
	// Returns nil, ChunkNotFoundError if the chunk does not exist.
	GetEmbeddingMetadata(ctx context.Context, chunkID StableChunkID) (*EmbeddingMetadata, error)

	// SetEmbeddingMetadata associates embedding metadata with a chunk.
	//
	// If metadata for the chunk already exists, it is replaced.
	SetEmbeddingMetadata(ctx context.Context, chunkID StableChunkID, metadata *EmbeddingMetadata) error

	// GetChunksWithEmbeddingMetadata retrieves all chunks that have embedding metadata.
	//
	// This is useful for identifying chunks that have been embedded and for
	// tracking embedding progress.
	GetChunksWithEmbeddingMetadata(ctx context.Context) ([]Chunk, error)

	// Retrieval operations ----------------------------------------------------

	// Retrieve performs a retrieval query against the archive.
	//
	// The query can target either documents or chunks. The Store implementation
	// is free to use any retrieval strategy:
	//   - Semantic search (using embeddings)
	//   - Keyword search (using text indexing)
	//   - Hybrid search (combining both)
	//
	// The query can optionally include pre-computed embeddings. If the query's
	// Text field is provided but EmbeddingMetadata is nil, the Store may
	// need to compute embeddings (depending on implementation).
	//
	// The returned hits are ordered by RelevanceScore descending (highest first).
	// The number of hits returned is bounded by the query's Limit.
	Retrieve(ctx context.Context, query RetrievalQuery) (*RetrieveResult, error)

	// HealthCheck performs a health check on the store.
	//
	// This is intended for monitoring and operational purposes. It should return
	// an error if the store is in an unhealthy state, or nil if healthy.
	//
	// Implementations may perform lightweight checks (e.g., database connection).
	HealthCheck(ctx context.Context) error
}

// DocumentNotFoundError is returned when a requested document does not exist.
type DocumentNotFoundError struct {
	StableID StableDocumentID
}

func (e DocumentNotFoundError) Error() string {
	return "document not found: " + string(e.StableID)
}

// IsDocumentNotFoundError checks if an error is a DocumentNotFoundError.
func IsDocumentNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	_, ok := err.(DocumentNotFoundError)
	return ok
}

// ChunkNotFoundError is returned when a requested chunk does not exist.
type ChunkNotFoundError struct {
	StableID StableChunkID
}

func (e ChunkNotFoundError) Error() string {
	return "chunk not found: " + string(e.StableID)
}

// IsChunkNotFoundError checks if an error is a ChunkNotFoundError.
func IsChunkNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	_, ok := err.(ChunkNotFoundError)
	return ok
}
