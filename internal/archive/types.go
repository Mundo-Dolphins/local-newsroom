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

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
)

// StableDocumentID represents a stable, storage-neutral identifier for an archived document.
//
// This ID is derived from document content and source provenance, not from an ingestion run
// or database row ID. It ensures that:
//   - The same document, when re-indexed, receives the same ID
//   - Document identity is decoupled from storage implementation details
//   - Documents can be identified and updated without re-fetching
//
// StableDocumentID is a type alias for string, intentionally simple to avoid unnecessary
// abstractions while providing type safety.
type StableDocumentID string

// StableChunkID represents a stable, storage-neutral identifier for a document chunk.
//
// The chunk ID is deterministically derived from:
//   - The parent document's StableDocumentID
//   - The chunk's content hash (ensuring content changes yield new IDs)
//   - The chunk's position within the document
//
// This guarantees that:
//   - Chunks from the same document content are always identified consistently
//   - Content changes produce new chunk IDs
//   - Chunk position is part of the identity for ordered retrieval
//
// Format: "arch_doc:<document_id>:<content_hash>:<position>:<length>"
//
// This is a type alias for string, intentionally simple while providing type safety.
type StableChunkID string

// ContentHash represents a cryptographic hash of document content.
//
// Used for:
//   - Detecting content changes (re-indexing when hash differs)
//   - Deriving StableChunkID values
//   - Validating data integrity
//
// The hash algorithm is implementation-specific; common choices are SHA-256 or MD5.
// Type is a string alias for type safety.
type ContentHash string

// ContentHashFrom creates a ContentHash from a string.
// This is a helper for constructing ContentHash values in tests and initialization.
func ContentHashFrom(s string) ContentHash {
	return ContentHash(s)
}

// EmbeddingModelName is a domain-neutral identifier for an embedding model.
//
// Examples: "xlm-roberta-base", "all-MiniLM-L6-v2", "local-8d"
//
// This does not expose the embedding provider's internal model representation,
// allowing the archive to remain provider-agnostic.
type EmbeddingModelName string

// EmbeddingDimensions represents the dimensionality of an embedding vector.
//
// Valid values are positive integers (e.g., 384, 768, 1024, 1536).
type EmbeddingDimensions int

// Validate returns an error if the dimensions are invalid.
func (d EmbeddingDimensions) Validate() error {
	if d <= 0 {
		return fmt.Errorf("invalid embedding dimensions: %d (must be positive)", d)
	}
	return nil
}

// EmbeddingMetadata captures information about an embedding without exposing
// the embedding provider's response types or implementation details.
//
// This structure is stored alongside chunks and documents to:
//   - Track which model was used for embedding
//   - Validate that embeddings are compatible (same dimensions)
//   - Enable future model migrations or updates
//
// The embedding vectors themselves are stored separately and are not part of this type.
type EmbeddingMetadata struct {
	// ModelName is the identifier of the embedding model used.
	// Examples: "xlm-roberta-base", "all-MiniLM-L6-v2"
	ModelName EmbeddingModelName `json:"model_name"`

	// Dimensions is the number of dimensions in the embedding vector.
	// All chunks with this metadata must have embeddings of this length.
	Dimensions EmbeddingDimensions `json:"dimensions"`

	// Version is an optional semantic version of the embedding pipeline.
	// Used for tracking embedding generation changes independent of model changes.
	// May be empty if not tracked.
	Version string `json:"version,omitempty"`

	// GeneratedAt is the UTC timestamp when the embedding was generated.
	GeneratedAt time.Time `json:"generated_at"`
}

// SourceProvenance captures provenance information for an archived document,
// linking back to the original source and extraction process.
//
// This embeds types.Source where practical to preserve the original retrieval
// context while adding archive-specific provenance fields.
type SourceProvenance struct {
	// SourceID is the StableID from the original types.Source.
	// This provides a traceable link to the fetcher's output.
	SourceID string `json:"source_id"`

	// SourceURL is the OriginalURL from the source.
	// Included for convenience; SourceID is the primary link.
	SourceURL string `json:"source_url"`

	// SourceType is the type of content source.
	SourceType types.SourceType `json:"source_type"`

	// RetrievedAt is when the source was fetched.
	RetrievedAt time.Time `json:"retrieved_at"`

	// ExtractedAt is when the document was extracted and archived.
	// This may differ from RetrievedAt if there's a delay.
	ExtractedAt time.Time `json:"extracted_at"`

	// ExtractionMetadata contains extra provenance from the extraction process.
	// Mirrors types.Document.ExtractionMetadata for completeness.
	ExtractionMetadata map[string]string `json:"extraction_metadata,omitempty"`
}

// ArchiveDocument represents a normalized document stored in the archive.
//
// This is the primary entity for the archive, representing a single normalized
// document with stable identity, source provenance, and content versioning.
//
// Key characteristics:
//   - StableID uniquely identifies this document across re-indexing operations
//   - PlainTextHash allows detecting content changes
//   - SourceProvenance links back to the original source
//   - ChunkCount tracks the number of chunks this document is divided into
//
// The archive does not store the full document content in the ArchiveDocument;
// content is stored in chunks. This type is used as the document-level entity
// for cataloging and metadata.
type ArchiveDocument struct {
	// StableID is the unique, stable identifier for this document.
	// Generated from content and source to ensure idempotency across re-indexing.
	StableID StableDocumentID `json:"stable_id"`

	// PlainText is the document content as plain text.
	// Stored here for convenience; in a real system, this might be in chunks only.
	PlainText string `json:"plain_text"`

	// PlainTextHash is the content hash of PlainText.
	// Used to detect content changes and drive re-indexing decisions.
	PlainTextHash ContentHash `json:"plain_text_hash"`

	// ChunkCount is the number of chunks this document has been divided into.
	ChunkCount int `json:"chunk_count"`

	// SourceProvenance contains provenance information linking to the original source.
	SourceProvenance SourceProvenance `json:"source_provenance"`

	// ArchivedAt is the UTC timestamp when this document was first archived.
	ArchivedAt time.Time `json:"archived_at"`

	// Metadata is optional, implementation-specific metadata.
	// Use sparingly and document any semantics in code or documentation.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Chunk represents a segment of an archived document.
//
// Chunks are the fundamental unit for embedding and retrieval. Each chunk:
//   - Has a stable ID derived from document ID, content hash, and position
//   - Tracks its position within the parent document
//   - Has a length in tokens/characters for context window management
//   - Can have optional embedding metadata and vector data stored alongside it
//
// Note: The chunk text itself is not stored in this type; it is part of the
// parent document's content. The type focuses on chunk identity and metadata.
type Chunk struct {
	// StableID is the unique, stable identifier for this chunk.
	// Format: "arch_doc:<document_id>:<content_hash>:<position>:<length>"
	StableID StableChunkID `json:"stable_id"`

	// DocumentStableID is the parent document's stable ID.
	// Used for indexing and querying without requiring a join.
	DocumentStableID StableDocumentID `json:"document_stable_id"`

	// ContentHash is the hash of this chunk's content.
	// Used to detect content changes in the chunk specifically.
	ContentHash ContentHash `json:"content_hash"`

	// Position is the zero-based starting index of this chunk within the document.
	// Used for ordering chunks and reconstructing document order.
	Position int `json:"position"`

	// Length is the length of this chunk in tokens or characters.
	// The interpretation depends on the chunking strategy.
	Length int `json:"length"`

	// HasEmbedding indicates whether this chunk has an associated embedding.
	// Set to false if embeddings are optional or lazily generated.
	HasEmbedding bool `json:"has_embedding"`
}

// RetrievalQuery represents a query for retrieving archived documents or chunks.
//
// The query structure is storage-neutral, allowing the same interface to work
// with different storage backends (SQLite, etc.) and retrieval strategies
// (semantic, keyword, hybrid).
type RetrievalQuery struct {
	// Text is the query text.
	// Used for semantic retrieval (embedded) or keyword matching.
	Text string `json:"text"`

	// Target specifies what the query should retrieve.
	Target RetrievalTarget `json:"target"`

	// Limit is the maximum number of results to return.
	// If zero or negative, a system default is applied.
	Limit int `json:"limit,omitempty"`

	// Filters are optional constraints on the result set.
	// Implementation-specific; currently empty.
	Filters RetrievalFilters `json:"filters,omitempty"`

	// Embedding is an optional pre-computed embedding for the query text.
	// If provided, semantic search uses this directly without re-embedding.
	// Use case: pre-computed query embeddings for performance.
	// Note: The field itself is nil if not provided; the type is stored elsewhere.
	//
	// This field intentionally does not embed the raw embedding vector type
	// to avoid coupling to embedding providers. Callers must use methods
	// on the Store that accept embeddings as []float32 directly.
	//
	// For now, this is a placeholder to be filled by embedding metadata if needed.
	// Storage implementations should accept embeddings separately in the interface.
	Embedding *EmbeddingMetadata `json:"embedding,omitempty"`
}

// RetrievalTarget specifies what type of result the query should return.
type RetrievalTarget string

const (
	// RetrievalTargetDocument returns entire documents (aggregating chunk results).
	RetrievalTargetDocument RetrievalTarget = "document"

	// RetrievalTargetChunk returns individual chunks.
	RetrievalTargetChunk RetrievalTarget = "chunk"
)

// RetrievalFilters represents optional constraints on retrieval results.
//
// Currently defined to provide a forward-compatible interface.
// Storage implementations may support additional filters not listed here.
type RetrievalFilters struct {
	// SourceIDs filters by source stable IDs.
	// Only results from these sources are returned.
	// If nil or empty, no filtering is applied.
	SourceIDs []string `json:"source_ids,omitempty"`

	// DateRange filters by date range.
	// Only results within this range are returned.
	// If nil, no date filtering is applied.
	DateRange *DateRange `json:"date_range,omitempty"`

	// MinRelevanceScore sets a minimum relevance score threshold.
	// Results below this score are excluded.
	// If nil or negative, no threshold is applied.
	MinRelevanceScore *float64 `json:"min_relevance_score,omitempty"`
}

// DateRange specifies an inclusive date range for filtering results.
type DateRange struct {
	// Start is the inclusive start date (UTC).
	// If nil, no lower bound is applied.
	Start *time.Time `json:"start,omitempty"`

	// End is the inclusive end date (UTC).
	// If nil, no upper bound is applied.
	End *time.Time `json:"end,omitempty"`
}

// RetrievalHit represents a single result from a retrieval query.
//
// Each hit includes:
//   - The relevant document or chunk (depending on RetrievalTarget)
//   - A similarity/relevance score
//   - Full provenance for factual verification
//   - Position context for chunk-level results
type RetrievalHit struct {
	// TargetType indicates whether this hit contains a document or chunk.
	TargetType RetrievalTarget `json:"target_type"`

	// DocumentStableID is the stable ID of the document containing this hit.
	// Always present, even for chunk-level results.
	DocumentStableID StableDocumentID `json:"document_stable_id"`

	// SourceID is the original source stable ID for provenance.
	SourceID string `json:"source_id"`

	// SourceURL is the original source URL for verification.
	SourceURL string `json:"source_url"`

	// SourceType is the type of the original source.
	SourceType types.SourceType `json:"source_type"`

	// RetrievedAt is when the source was originally fetched.
	RetrievedAt time.Time `json:"retrieved_at"`

	// RelevanceScore is the similarity or relevance score for this result.
	// Higher scores indicate better matches.
	// The exact interpretation depends on the retrieval method (cosine similarity, etc.).
	RelevanceScore float64 `json:"relevance_score"`

	// DocumentContent is the full document content for document-level hits.
	// Nil for chunk-level hits.
	DocumentContent *string `json:"document_content,omitempty"`

	// ChunkPosition is the position of the matching chunk within the document.
	// Only populated for chunk-level hits.
	ChunkPosition *int `json:"chunk_position,omitempty"`

	// ChunkContent is the content of the matching chunk for chunk-level hits.
	// Nil for document-level hits.
	ChunkContent *string `json:"chunk_content,omitempty"`
}

// RetrieveResult represents the complete result of a retrieval query.
type RetrieveResult struct {
	// Hits is the list of retrieval hits, ordered by relevance score (descending).
	Hits []RetrievalHit `json:"hits"`

	// TotalHits is the total number of hits matching the query (before limit).
	// May be greater than len(Hits) if limit was applied.
	TotalHits int `json:"total_hits"`

	// Query is the query that produced this result.
	Query RetrievalQuery `json:"query"`
}

// DocumentVersionError indicates that a document's content has changed.
//
// This is returned when:
//   - A document is found but its PlainTextHash differs from expected
//   - The caller needs to decide whether to update or skip the document
type DocumentVersionError struct {
	// StableID is the document's stable ID.
	StableID StableDocumentID

	// ExpectedHash is the hash the caller expected.
	ExpectedHash ContentHash

	// ActualHash is the hash found in the archive.
	ActualHash ContentHash
}

func (e DocumentVersionError) Error() string {
	return fmt.Sprintf("document version mismatch: %s (expected %s, got %s)",
		e.StableID, e.ExpectedHash, e.ActualHash)
}

// IsDocumentVersionError checks if an error is a DocumentVersionError.
func IsDocumentVersionError(err error) bool {
	if err == nil {
		return false
	}
	_, ok := err.(DocumentVersionError)
	return ok
}

// composeChunkID constructs a StableChunkID from document ID, content hash, position, and length.
//
// Format: "arch_doc:<document_id>:<content_hash>:<position>:<length>"
//
// This function ensures that chunk IDs are deterministic and consistent across
// re-indexing operations. Changing any component (document ID, content hash,
// position, or length) will produce a different chunk ID.
func composeChunkID(docID StableDocumentID, contentHash ContentHash, position, length int) StableChunkID {
	return StableChunkID(fmt.Sprintf(
		"arch_doc:%s:%s:%d:%d",
		docID, contentHash, position, length,
	))
}

// parseChunkID parses a StableChunkID into its components.
//
// Expected format: "arch_doc:<document_id>:<content_hash>:<position>:<length>"
//
// Returns an error if the chunk ID format is invalid.
func parseChunkID(id StableChunkID) (StableDocumentID, ContentHash, int, int, error) {
	return parseChunkIDInternal(string(id))
}

// ParseChunkIDInternal is an exported version for testing that parses a StableChunkID string.
//
// Expected format: "arch_doc:<document_id>:<content_hash>:<position>:<length>"
//
// Returns an error if the chunk ID format is invalid.
func ParseChunkIDInternal(id string) (StableDocumentID, ContentHash, int, int, error) {
	return parseChunkIDInternal(id)
}

func parseChunkIDInternal(id string) (StableDocumentID, ContentHash, int, int, error) {
	parts := strings.Split(string(id), ":")
	if len(parts) != 5 || parts[0] != "arch_doc" {
		return "", "", 0, 0, fmt.Errorf("invalid chunk ID format: %s", id)
	}

	position, err := strconv.Atoi(parts[3])
	if err != nil {
		return "", "", 0, 0, fmt.Errorf("invalid position in chunk ID: %s", id)
	}

	length, err := strconv.Atoi(parts[4])
	if err != nil {
		return "", "", 0, 0, fmt.Errorf("invalid length in chunk ID: %s", id)
	}

	return StableDocumentID(parts[1]), ContentHash(parts[2]), position, length, nil
}
