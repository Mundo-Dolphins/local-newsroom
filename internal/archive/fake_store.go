package archive

import (
	"context"
	"sort"
	"sync"
)

// FakeStore is an in-memory implementation of Store for testing.
//
// It provides deterministic behavior suitable for unit tests and supports:
//   - Full CRUD operations on documents and chunks
//   - Embedding metadata tracking
//   - Basic retrieval (relevance scoring based on text matching)
//   - Configurable behavior via errors and mocks
//
// Usage example:
//
//	store := archive.NewFakeStore()
//	err := store.UpsertDocument(ctx, &ArchiveDocument{
//		StableID: "doc-1",
//		PlainText: "Hello world",
//		PlainTextHash: "hash-1",
//		SourceProvenance: SourceProvenance{
//			SourceID: "source-1",
//			SourceType: types.SourceTypeWeb,
//		},
//	}, []Chunk{
//		{
//			StableID: "arch_doc:doc-1:hash-1:0:100",
//			DocumentStableID: "doc-1",
//			ContentHash: "chunk-1",
//			Position: 0,
//			Length: 100,
//		},
//	})
//
// GetDocument:
//
//	doc, err := store.GetDocument(ctx, "doc-1")
func NewFakeStore() *FakeStore {
	return &FakeStore{
		documents:         make(map[StableDocumentID]*ArchiveDocument),
		chunks:            make(map[StableChunkID]*Chunk),
		chunksByDocument:  make(map[StableDocumentID][]StableChunkID),
		embeddingMetadata: make(map[StableChunkID]*EmbeddingMetadata),
		errors:            make(map[string]error),
		documentNotFound:  false,
		chunkNotFound:     false,
	}
}

// FakeStore implements Store in-memory for testing.
type FakeStore struct {
	mu sync.RWMutex

	documents         map[StableDocumentID]*ArchiveDocument
	chunks            map[StableChunkID]*Chunk
	chunksByDocument  map[StableDocumentID][]StableChunkID
	embeddingMetadata map[StableChunkID]*EmbeddingMetadata

	// errors configures which operations to fail
	errors map[string]error

	// documentNotFound forces GetDocument to return not found
	documentNotFound bool

	// chunkNotFound forces GetChunk to return not found
	chunkNotFound bool

	// retrieveHits is a fixed set of hits for retrieval (for deterministic tests)
	// If nil, retrieval uses simple text matching
	retrieveHits []RetrievalHit
}

// SetError configures the store to return an error for a specific operation.
//
// The operation parameter should be one of:
//   - "CreateDocument" - errors for CreateDocument
//   - "UpsertDocument" - errors for UpsertDocument
//   - "GetDocument" - errors for GetDocument
//   - "DeleteDocument" - errors for DeleteDocument
//   - "ListDocuments" - errors for ListDocuments
//   - "GetChunk" - errors for GetChunk
//   - "DeleteChunk" - errors for DeleteChunk
//   - "GetEmbeddingMetadata" - errors for GetEmbeddingMetadata
//   - "SetEmbeddingMetadata" - errors for SetEmbeddingMetadata
//   - "GetChunksWithEmbeddingMetadata" - errors for GetChunksWithEmbeddingMetadata
//   - "Retrieve" - errors for Retrieve
//   - "HealthCheck" - errors for HealthCheck
func (f *FakeStore) SetError(operation string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errors == nil {
		f.errors = make(map[string]error)
	}
	f.errors[operation] = err
}

// ClearErrors removes all configured errors.
func (f *FakeStore) ClearErrors() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errors = make(map[string]error)
}

// SetDocumentNotFound configures GetDocument to return DocumentNotFoundError.
func (f *FakeStore) SetDocumentNotFound(flag bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.documentNotFound = flag
}

// SetChunkNotFound configures GetChunk to return ChunkNotFoundError.
func (f *FakeStore) SetChunkNotFound(flag bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chunkNotFound = flag
}

// SetRetrieveHits configures Retrieve to return fixed hits.
//
// This is useful for deterministic testing of retrieval logic without
// implementing actual relevance scoring.
func (f *FakeStore) SetRetrieveHits(hits []RetrievalHit) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retrieveHits = hits
}

// ClearRetrieveHits clears any fixed retrieval hits, returning to text matching.
func (f *FakeStore) ClearRetrieveHits() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retrieveHits = nil
}

// GetDocuments returns all documents (for testing inspection).
// This is a testing helper, not part of the Store interface.
func (f *FakeStore) GetDocuments() []*ArchiveDocument {
	f.mu.RLock()
	defer f.mu.RUnlock()

	docs := make([]*ArchiveDocument, 0, len(f.documents))
	for _, doc := range f.documents {
		docs = append(docs, doc)
	}

	// Sort by ArchivedAt
	sort.Slice(docs, func(i, j int) bool {
		return docs[i].ArchivedAt.Before(docs[j].ArchivedAt)
	})

	return docs
}

// GetChunks returns all chunks (for testing inspection).
func (f *FakeStore) GetChunks() []Chunk {
	f.mu.RLock()
	defer f.mu.RUnlock()

	chunks := make([]Chunk, 0, len(f.chunks))
	for _, chunk := range f.chunks {
		chunks = append(chunks, *chunk)
	}
	return chunks
}

// GetEmbeddingMetadataMap returns the embedding metadata map (for testing inspection).
func (f *FakeStore) GetEmbeddingMetadataMap() map[StableChunkID]*EmbeddingMetadata {
	f.mu.RLock()
	defer f.mu.RUnlock()

	// Return a copy
	m := make(map[StableChunkID]*EmbeddingMetadata, len(f.embeddingMetadata))
	for k, v := range f.embeddingMetadata {
		m[k] = v
	}
	return m
}

// DocumentCount returns the number of documents in the store.
func (f *FakeStore) DocumentCount() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.documents)
}

// ChunkCount returns the number of chunks in the store.
func (f *FakeStore) ChunkCount() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.chunks)
}

// CreateDocument implements Store.CreateDocument.
func (f *FakeStore) CreateDocument(ctx context.Context, doc *ArchiveDocument, chunks []Chunk) error {
	if f.shouldFail("CreateDocument") {
		return f.errors["CreateDocument"]
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if _, exists := f.documents[doc.StableID]; exists {
		return &DocumentExistsError{StableID: doc.StableID}
	}

	f.documents[doc.StableID] = doc

	f.chunksByDocument[doc.StableID] = make([]StableChunkID, len(chunks))
	for i, chunk := range chunks {
		f.chunks[chunk.StableID] = &chunk
		f.chunksByDocument[doc.StableID][i] = chunk.StableID
	}

	return nil
}

// UpsertDocument implements Store.UpsertDocument.
func (f *FakeStore) UpsertDocument(ctx context.Context, doc *ArchiveDocument, chunks []Chunk) error {
	if f.shouldFail("UpsertDocument") {
		return f.errors["UpsertDocument"]
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.documents[doc.StableID] = doc

	f.chunksByDocument[doc.StableID] = make([]StableChunkID, len(chunks))
	for i, chunk := range chunks {
		f.chunks[chunk.StableID] = &chunk
		f.chunksByDocument[doc.StableID][i] = chunk.StableID
	}

	return nil
}

// GetDocument implements Store.GetDocument.
func (f *FakeStore) GetDocument(ctx context.Context, id StableDocumentID) (*ArchiveDocument, error) {
	if f.shouldFail("GetDocument") {
		return nil, f.errors["GetDocument"]
	}

	f.mu.RLock()
	defer f.mu.RUnlock()

	if f.documentNotFound {
		return nil, DocumentNotFoundError{StableID: id}
	}

	doc, exists := f.documents[id]
	if !exists {
		return nil, DocumentNotFoundError{StableID: id}
	}

	return doc, nil
}

// DeleteDocument implements Store.DeleteDocument.
func (f *FakeStore) DeleteDocument(ctx context.Context, id StableDocumentID) error {
	if f.shouldFail("DeleteDocument") {
		return f.errors["DeleteDocument"]
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if _, exists := f.documents[id]; !exists {
		return DocumentNotFoundError{StableID: id}
	}

	// Delete chunks for this document
	if chunkIDs, ok := f.chunksByDocument[id]; ok {
		for _, chunkID := range chunkIDs {
			delete(f.chunks, chunkID)
			delete(f.embeddingMetadata, chunkID)
		}
		delete(f.chunksByDocument, id)
	}

	delete(f.documents, id)
	return nil
}

// ListDocuments implements Store.ListDocuments.
func (f *FakeStore) ListDocuments(ctx context.Context, limit int) ([]*ArchiveDocument, error) {
	if f.shouldFail("ListDocuments") {
		return nil, f.errors["ListDocuments"]
	}

	f.mu.RLock()
	defer f.mu.RUnlock()

	docs := make([]*ArchiveDocument, 0, len(f.documents))
	for _, doc := range f.documents {
		docs = append(docs, doc)
	}

	// Sort by ArchivedAt
	sort.Slice(docs, func(i, j int) bool {
		return docs[i].ArchivedAt.Before(docs[j].ArchivedAt)
	})

	// Apply limit
	if limit > 0 && limit < len(docs) {
		docs = docs[:limit]
	}

	return docs, nil
}

// DocumentExists implements Store.DocumentExists.
func (f *FakeStore) DocumentExists(ctx context.Context, id StableDocumentID) (bool, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	_, exists := f.documents[id]
	return exists, nil
}

// GetChunk implements Store.GetChunk.
func (f *FakeStore) GetChunk(ctx context.Context, id StableChunkID) (*Chunk, error) {
	if f.shouldFail("GetChunk") {
		return nil, f.errors["GetChunk"]
	}

	f.mu.RLock()
	defer f.mu.RUnlock()

	if f.chunkNotFound {
		return nil, ChunkNotFoundError{StableID: id}
	}

	chunk, exists := f.chunks[id]
	if !exists {
		return nil, ChunkNotFoundError{StableID: id}
	}

	return chunk, nil
}

// GetChunksByDocument implements Store.GetChunksByDocument.
func (f *FakeStore) GetChunksByDocument(ctx context.Context, docID StableDocumentID) ([]Chunk, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	chunkIDs, ok := f.chunksByDocument[docID]
	if !ok {
		return nil, nil
	}

	chunks := make([]Chunk, 0, len(chunkIDs))
	for _, chunkID := range chunkIDs {
		if chunk, exists := f.chunks[chunkID]; exists {
			chunks = append(chunks, *chunk)
		}
	}

	// Sort by Position
	sort.Slice(chunks, func(i, j int) bool {
		return chunks[i].Position < chunks[j].Position
	})

	return chunks, nil
}

// DeleteChunk implements Store.DeleteChunk.
func (f *FakeStore) DeleteChunk(ctx context.Context, id StableChunkID) error {
	if f.shouldFail("DeleteChunk") {
		return f.errors["DeleteChunk"]
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if _, exists := f.chunks[id]; !exists {
		return ChunkNotFoundError{StableID: id}
	}

	delete(f.chunks, id)
	delete(f.embeddingMetadata, id)

	return nil
}

// GetEmbeddingMetadata implements Store.GetEmbeddingMetadata.
func (f *FakeStore) GetEmbeddingMetadata(ctx context.Context, chunkID StableChunkID) (*EmbeddingMetadata, error) {
	if f.shouldFail("GetEmbeddingMetadata") {
		return nil, f.errors["GetEmbeddingMetadata"]
	}

	f.mu.RLock()
	defer f.mu.RUnlock()

	if _, exists := f.chunks[chunkID]; !exists {
		return nil, ChunkNotFoundError{StableID: chunkID}
	}

	metadata, exists := f.embeddingMetadata[chunkID]
	if !exists {
		return nil, nil
	}

	// Return a copy to prevent mutation
	metaCopy := *metadata
	return &metaCopy, nil
}

// SetEmbeddingMetadata implements Store.SetEmbeddingMetadata.
func (f *FakeStore) SetEmbeddingMetadata(ctx context.Context, chunkID StableChunkID, metadata *EmbeddingMetadata) error {
	if f.shouldFail("SetEmbeddingMetadata") {
		return f.errors["SetEmbeddingMetadata"]
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if _, exists := f.chunks[chunkID]; !exists {
		return ChunkNotFoundError{StableID: chunkID}
	}

	if metadata == nil {
		delete(f.embeddingMetadata, chunkID)
		return nil
	}

	f.embeddingMetadata[chunkID] = &EmbeddingMetadata{
		ModelName:   metadata.ModelName,
		Dimensions:  metadata.Dimensions,
		Version:     metadata.Version,
		GeneratedAt: metadata.GeneratedAt,
	}

	return nil
}

// GetChunksWithEmbeddingMetadata implements Store.GetChunksWithEmbeddingMetadata.
func (f *FakeStore) GetChunksWithEmbeddingMetadata(ctx context.Context) ([]Chunk, error) {
	if f.shouldFail("GetChunksWithEmbeddingMetadata") {
		return nil, f.errors["GetChunksWithEmbeddingMetadata"]
	}

	f.mu.RLock()
	defer f.mu.RUnlock()

	chunks := make([]Chunk, 0)
	for chunkID := range f.embeddingMetadata {
		if chunk, exists := f.chunks[chunkID]; exists {
			chunks = append(chunks, *chunk)
		}
	}

	return chunks, nil
}

// Retrieve implements Store.Retrieve.
func (f *FakeStore) Retrieve(ctx context.Context, query RetrievalQuery) (*RetrieveResult, error) {
	if f.shouldFail("Retrieve") {
		return nil, f.errors["Retrieve"]
	}

	f.mu.RLock()
	defer f.mu.RUnlock()

	if f.retrieveHits != nil {
		// Return fixed hits
		hits := make([]RetrievalHit, len(f.retrieveHits))
		copy(hits, f.retrieveHits)

		// Apply limit
		if query.Limit > 0 && query.Limit < len(hits) {
			hits = hits[:query.Limit]
		}

		return &RetrieveResult{
			Hits:      hits,
			TotalHits: len(f.retrieveHits),
			Query:     query,
		}, nil
	}

	// Simple text matching: find documents where query text appears in PlainText
	// This is a minimal implementation for testing retrieval logic
	allHits := make([]RetrievalHit, 0)

	for _, doc := range f.documents {
		if query.Text == "" {
			continue
		}

		if containsIgnoreCase(doc.PlainText, query.Text) {
			hit := RetrievalHit{
				TargetType:       RetrievalTargetDocument,
				DocumentStableID: doc.StableID,
				SourceID:         doc.SourceProvenance.SourceID,
				SourceURL:        doc.SourceProvenance.SourceURL,
				SourceType:       doc.SourceProvenance.SourceType,
				RetrievedAt:      doc.SourceProvenance.RetrievedAt,
				RelevanceScore:   1.0, // Perfect match
				DocumentContent:  &doc.PlainText,
			}
			allHits = append(allHits, hit)
		}
	}

	// Sort by relevance score descending
	sort.Slice(allHits, func(i, j int) bool {
		return allHits[i].RelevanceScore > allHits[j].RelevanceScore
	})

	// Apply limit
	hits := allHits
	if query.Limit > 0 && query.Limit < len(hits) {
		hits = hits[:query.Limit]
	}

	return &RetrieveResult{
		Hits:      hits,
		TotalHits: len(allHits),
		Query:     query,
	}, nil
}

// HealthCheck implements Store.HealthCheck.
func (f *FakeStore) HealthCheck(ctx context.Context) error {
	// Fake store is always healthy
	return nil
}

// shouldFail returns true if the operation should fail and the error to return.
func (f *FakeStore) shouldFail(operation string) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	err, ok := f.errors[operation]
	return ok && err != nil
}

// DocumentExistsError is returned when attempting to create a document that already exists.
type DocumentExistsError struct {
	StableID StableDocumentID
}

func (e DocumentExistsError) Error() string {
	return "document already exists: " + string(e.StableID)
}

// IsDocumentExistsError checks if an error is a DocumentExistsError.
func IsDocumentExistsError(err error) bool {
	if err == nil {
		return false
	}
	_, ok := err.(DocumentExistsError)
	if ok {
		return true
	}
	_, ok = err.(*DocumentExistsError)
	return ok
}

// containsIgnoreCase checks if the haystack contains the needle (case-insensitive).
func containsIgnoreCase(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		(haystack == needle ||
			(len(haystack) > 0 && containsCaseInsensitive(haystack, needle)))
}

func containsCaseInsensitive(haystack, needle string) bool {
	haystackLower := toLowerCase(haystack)
	needleLower := toLowerCase(needle)
	return contains(haystackLower, needleLower)
}

func toLowerCase(s string) string {
	result := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			result[i] = c + 32
		} else {
			result[i] = c
		}
	}
	return string(result)
}

func contains(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// CompiledError is an error that was wrapped when returned from FakeStore operations.
// This is used internally to track error wrapping.
type CompiledError struct {
	Op   string
	Err  error
	Wrap bool
}

func (e *CompiledError) Error() string {
	if e.Wrap && e.Err != nil {
		return e.Op + ": " + e.Err.Error()
	}
	return e.Op
}

func (e *CompiledError) Unwrap() error {
	return e.Err
}
