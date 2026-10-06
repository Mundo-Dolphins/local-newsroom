package archive

import (
	"context"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
)

func TestStableChunkID_Composition(t *testing.T) {
	docID := StableDocumentID("doc-123")
	contentHash := ContentHash("abc123")
	pos := 5
	length := 100

	expectedID := StableChunkID("arch_doc:doc-123:abc123:5:100")
	actualID := composeChunkID(docID, contentHash, pos, length)

	if actualID != expectedID {
		t.Errorf("composeChunkID returned %q, expected %q", actualID, expectedID)
	}
}

func TestComposeChunkID_VariousPositions(t *testing.T) {
	docID := StableDocumentID("test-doc")
	contentHash := ContentHash("test-hash")

	tests := []struct {
		pos, length int
		expected    string
	}{
		{0, 100, "arch_doc:test-doc:test-hash:0:100"},
		{100, 200, "arch_doc:test-doc:test-hash:100:200"},
		{1000, 500, "arch_doc:test-doc:test-hash:1000:500"},
		{999999, 1, "arch_doc:test-doc:test-hash:999999:1"},
	}

	for _, tt := range tests {
		id := composeChunkID(docID, contentHash, tt.pos, tt.length)
		if id != StableChunkID(tt.expected) {
			t.Errorf("composeChunkID returned %q for pos=%d, length=%d, expected %q",
				id, tt.pos, tt.length, tt.expected)
		}
	}
}

func TestEmbeddingDimensions_Validate(t *testing.T) {
	tests := []struct {
		dims EmbeddingDimensions
		err  bool
	}{
		{384, false},
		{768, false},
		{1024, false},
		{0, true},
		{-1, true},
		{1536, false},
	}

	for _, tt := range tests {
		err := tt.dims.Validate()
		hasErr := err != nil
		if hasErr != tt.err {
			t.Errorf("EmbeddingDimensions(%d).Validate() = err=%v, expected err=%v",
				tt.dims, err, tt.err)
		}
	}
}

func TestFakeStore_New(t *testing.T) {
	store := NewFakeStore()
	if store == nil {
		t.Fatal("NewFakeStore() returned nil")
	}

	if store.DocumentCount() != 0 {
		t.Errorf("NewFakeStore() has DocumentCount=%d, expected 0", store.DocumentCount())
	}

	if store.ChunkCount() != 0 {
		t.Errorf("NewFakeStore() has ChunkCount=%d, expected 0", store.ChunkCount())
	}
}

func TestFakeStore_CreateDocument(t *testing.T) {
	ctx := context.Background()
	store := NewFakeStore()

	doc := &ArchiveDocument{
		StableID:      "doc-1",
		PlainText:     "Test document content",
		PlainTextHash: "hash-1",
		ChunkCount:    1,
		SourceProvenance: SourceProvenance{
			SourceID:    "source-1",
			SourceURL:   "https://example.com/article",
			SourceType:  types.SourceTypeWeb,
			RetrievedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			ExtractedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		ArchivedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	chunks := []Chunk{
		{
			StableID:         "arch_doc:doc-1:hash-1:0:100",
			DocumentStableID: "doc-1",
			ContentHash:      "chunk-1",
			Position:         0,
			Length:           100,
			HasEmbedding:     false,
		},
	}

	err := store.CreateDocument(ctx, doc, chunks)
	if err != nil {
		t.Fatalf("CreateDocument failed: %v", err)
	}

	if store.DocumentCount() != 1 {
		t.Errorf("DocumentCount=%d, expected 1", store.DocumentCount())
	}

	if store.ChunkCount() != 1 {
		t.Errorf("ChunkCount=%d, expected 1", store.ChunkCount())
	}
}

func TestFakeStore_CreateDocument_Duplicate(t *testing.T) {
	ctx := context.Background()
	store := NewFakeStore()

	doc := &ArchiveDocument{
		StableID:      "doc-1",
		PlainText:     "Test",
		PlainTextHash: "hash-1",
		ChunkCount:    0,
		SourceProvenance: SourceProvenance{
			SourceID:    "source-1",
			SourceType:  types.SourceTypeWeb,
			RetrievedAt: time.Now(),
			ExtractedAt: time.Now(),
		},
		ArchivedAt: time.Now(),
	}

	// First create should succeed
	err := store.CreateDocument(ctx, doc, []Chunk{})
	if err != nil {
		t.Fatalf("First CreateDocument failed: %v", err)
	}

	// Second create should fail with DocumentExistsError
	err = store.CreateDocument(ctx, doc, []Chunk{})
	if !IsDocumentExistsError(err) {
		t.Errorf("CreateDocument for existing doc returned error %v, expected DocumentExistsError", err)
	}
}

func TestFakeStore_UpsertDocument(t *testing.T) {
	ctx := context.Background()
	store := NewFakeStore()

	doc1 := &ArchiveDocument{
		StableID:      "doc-1",
		PlainText:     "First version",
		PlainTextHash: "hash-v1",
		ChunkCount:    1,
		SourceProvenance: SourceProvenance{
			SourceID:    "source-1",
			SourceType:  types.SourceTypeWeb,
			RetrievedAt: time.Now(),
			ExtractedAt: time.Now(),
		},
		ArchivedAt: time.Now(),
	}

	doc2 := &ArchiveDocument{
		StableID:      "doc-1",
		PlainText:     "Second version (upserted)",
		PlainTextHash: "hash-v2",
		ChunkCount:    1,
		SourceProvenance: SourceProvenance{
			SourceID:    "source-1",
			SourceType:  types.SourceTypeWeb,
			RetrievedAt: time.Now(),
			ExtractedAt: time.Now(),
		},
		ArchivedAt: time.Now(),
	}

	// First upsert
	err := store.UpsertDocument(ctx, doc1, []Chunk{
		{
			StableID:         "arch_doc:doc-1:hash-v1:0:10",
			DocumentStableID: "doc-1",
			ContentHash:      "chunk-1",
			Position:         0,
			Length:           10,
			HasEmbedding:     false,
		},
	})
	if err != nil {
		t.Fatalf("First UpsertDocument failed: %v", err)
	}

	// Upsert with same ID but different content
	err = store.UpsertDocument(ctx, doc2, []Chunk{
		{
			StableID:         "arch_doc:doc-1:hash-v2:0:10",
			DocumentStableID: "doc-1",
			ContentHash:      "chunk-2",
			Position:         0,
			Length:           10,
			HasEmbedding:     true,
		},
	})
	if err != nil {
		t.Fatalf("Second UpsertDocument failed: %v", err)
	}

	// Verify the document was updated
	retrieved, err := store.GetDocument(ctx, "doc-1")
	if err != nil {
		t.Fatalf("GetDocument failed: %v", err)
	}

	if retrieved.PlainText != doc2.PlainText {
		t.Errorf("GetDocument returned PlainText=%q, expected %q", retrieved.PlainText, doc2.PlainText)
	}
}

func TestFakeStore_GetDocument(t *testing.T) {
	ctx := context.Background()
	store := NewFakeStore()

	doc := &ArchiveDocument{
		StableID:      "doc-1",
		PlainText:     "Test",
		PlainTextHash: "hash-1",
		ChunkCount:    0,
		SourceProvenance: SourceProvenance{
			SourceID:    "source-1",
			SourceType:  types.SourceTypeWeb,
			RetrievedAt: time.Now(),
			ExtractedAt: time.Now(),
		},
		ArchivedAt: time.Now(),
	}

	err := store.UpsertDocument(ctx, doc, []Chunk{})
	if err != nil {
		t.Fatalf("UpsertDocument failed: %v", err)
	}

	retrieved, err := store.GetDocument(ctx, "doc-1")
	if err != nil {
		t.Fatalf("GetDocument failed: %v", err)
	}

	if retrieved.StableID != "doc-1" {
		t.Errorf("GetDocument returned StableID=%q, expected %q", retrieved.StableID, "doc-1")
	}

	// Get non-existent document
	_, err = store.GetDocument(ctx, "non-existent")
	if !IsDocumentNotFoundError(err) {
		t.Errorf("GetDocument for non-existent doc returned error %v, expected DocumentNotFoundError", err)
	}
}

func TestFakeStore_DeleteDocument(t *testing.T) {
	ctx := context.Background()
	store := NewFakeStore()

	doc := &ArchiveDocument{
		StableID:      "doc-1",
		PlainText:     "Test",
		PlainTextHash: "hash-1",
		ChunkCount:    1,
		SourceProvenance: SourceProvenance{
			SourceID:    "source-1",
			SourceType:  types.SourceTypeWeb,
			RetrievedAt: time.Now(),
			ExtractedAt: time.Now(),
		},
		ArchivedAt: time.Now(),
	}

	chunks := []Chunk{
		{
			StableID:         "arch_doc:doc-1:hash-1:0:10",
			DocumentStableID: "doc-1",
			ContentHash:      "chunk-1",
			Position:         0,
			Length:           10,
			HasEmbedding:     false,
		},
	}

	err := store.UpsertDocument(ctx, doc, chunks)
	if err != nil {
		t.Fatalf("UpsertDocument failed: %v", err)
	}

	err = store.DeleteDocument(ctx, "doc-1")
	if err != nil {
		t.Fatalf("DeleteDocument failed: %v", err)
	}

	if store.DocumentCount() != 0 {
		t.Errorf("DocumentCount=%d, expected 0", store.DocumentCount())
	}

	if store.ChunkCount() != 0 {
		t.Errorf("ChunkCount=%d, expected 0", store.ChunkCount())
	}

	// Delete non-existent document
	err = store.DeleteDocument(ctx, "non-existent")
	if !IsDocumentNotFoundError(err) {
		t.Errorf("DeleteDocument for non-existent doc returned error %v, expected DocumentNotFoundError", err)
	}
}

func TestFakeStore_ListDocuments(t *testing.T) {
	ctx := context.Background()
	store := NewFakeStore()

	now := time.Now()

	docs := []*ArchiveDocument{
		{
			StableID:      "doc-3",
			PlainText:     "Third",
			PlainTextHash: "hash-3",
			ChunkCount:    0,
			SourceProvenance: SourceProvenance{
				SourceID:    "source-1",
				SourceType:  types.SourceTypeWeb,
				RetrievedAt: now,
				ExtractedAt: now,
			},
			ArchivedAt: now.Add(2 * time.Hour),
		},
		{
			StableID:      "doc-1",
			PlainText:     "First",
			PlainTextHash: "hash-1",
			ChunkCount:    0,
			SourceProvenance: SourceProvenance{
				SourceID:    "source-1",
				SourceType:  types.SourceTypeWeb,
				RetrievedAt: now,
				ExtractedAt: now,
			},
			ArchivedAt: now,
		},
		{
			StableID:      "doc-2",
			PlainText:     "Second",
			PlainTextHash: "hash-2",
			ChunkCount:    0,
			SourceProvenance: SourceProvenance{
				SourceID:    "source-1",
				SourceType:  types.SourceTypeWeb,
				RetrievedAt: now,
				ExtractedAt: now,
			},
			ArchivedAt: now.Add(1 * time.Hour),
		},
	}

	for _, doc := range docs {
		err := store.UpsertDocument(ctx, doc, []Chunk{})
		if err != nil {
			t.Fatalf("UpsertDocument for doc-%s failed: %v", doc.StableID, err)
		}
	}

	allDocs, err := store.ListDocuments(ctx, 0)
	if err != nil {
		t.Fatalf("ListDocuments failed: %v", err)
	}

	if len(allDocs) != 3 {
		t.Errorf("ListDocuments returned %d docs, expected 3", len(allDocs))
	}

	// Check ordering (oldest first)
	if allDocs[0].StableID != "doc-1" {
		t.Errorf("First document is %q, expected doc-1", allDocs[0].StableID)
	}
	if allDocs[1].StableID != "doc-2" {
		t.Errorf("Second document is %q, expected doc-2", allDocs[1].StableID)
	}
	if allDocs[2].StableID != "doc-3" {
		t.Errorf("Third document is %q, expected doc-3", allDocs[2].StableID)
	}

	// Test with limit
	limitedDocs, err := store.ListDocuments(ctx, 2)
	if err != nil {
		t.Fatalf("ListDocuments with limit failed: %v", err)
	}

	if len(limitedDocs) != 2 {
		t.Errorf("ListDocuments with limit=2 returned %d docs, expected 2", len(limitedDocs))
	}
}

func TestFakeStore_GetChunksByDocument(t *testing.T) {
	ctx := context.Background()
	store := NewFakeStore()

	doc := &ArchiveDocument{
		StableID:      "doc-1",
		PlainText:     "Content here",
		PlainTextHash: "hash-1",
		ChunkCount:    3,
		SourceProvenance: SourceProvenance{
			SourceID:    "source-1",
			SourceType:  types.SourceTypeWeb,
			RetrievedAt: time.Now(),
			ExtractedAt: time.Now(),
		},
		ArchivedAt: time.Now(),
	}

	chunks := []Chunk{
		{
			StableID:         "arch_doc:doc-1:hash-1:0:50",
			DocumentStableID: "doc-1",
			ContentHash:      "chunk-1",
			Position:         0,
			Length:           50,
			HasEmbedding:     true,
		},
		{
			StableID:         "arch_doc:doc-1:hash-1:50:75",
			DocumentStableID: "doc-1",
			ContentHash:      "chunk-2",
			Position:         50,
			Length:           75,
			HasEmbedding:     true,
		},
		{
			StableID:         "arch_doc:doc-1:hash-1:125:25",
			DocumentStableID: "doc-1",
			ContentHash:      "chunk-3",
			Position:         125,
			Length:           25,
			HasEmbedding:     false,
		},
	}

	err := store.UpsertDocument(ctx, doc, chunks)
	if err != nil {
		t.Fatalf("UpsertDocument failed: %v", err)
	}

	retrievedChunks, err := store.GetChunksByDocument(ctx, "doc-1")
	if err != nil {
		t.Fatalf("GetChunksByDocument failed: %v", err)
	}

	if len(retrievedChunks) != 3 {
		t.Errorf("GetChunksByDocument returned %d chunks, expected 3", len(retrievedChunks))
	}

	// Check ordering by position
	expectedPositions := []int{0, 50, 125}
	for i, expectedPos := range expectedPositions {
		if retrievedChunks[i].Position != expectedPos {
			t.Errorf("Chunk %d has Position=%d, expected %d", i, retrievedChunks[i].Position, expectedPos)
		}
	}

	// Get chunks for non-existent document
	_, err = store.GetChunksByDocument(ctx, "non-existent")
	if err != nil {
		t.Errorf("GetChunksByDocument for non-existent doc returned error %v, expected nil", err)
	}
}

func TestFakeStore_GetEmbeddingMetadata(t *testing.T) {
	ctx := context.Background()
	store := NewFakeStore()

	doc := &ArchiveDocument{
		StableID:      "doc-1",
		PlainText:     "Test",
		PlainTextHash: "hash-1",
		ChunkCount:    1,
		SourceProvenance: SourceProvenance{
			SourceID:    "source-1",
			SourceType:  types.SourceTypeWeb,
			RetrievedAt: time.Now(),
			ExtractedAt: time.Now(),
		},
		ArchivedAt: time.Now(),
	}

	chunkID := StableChunkID("arch_doc:doc-1:hash-1:0:10")
	chunks := []Chunk{{
		StableID:         chunkID,
		DocumentStableID: "doc-1",
		ContentHash:      "chunk-1",
		Position:         0,
		Length:           10,
		HasEmbedding:     false,
	}}

	err := store.UpsertDocument(ctx, doc, chunks)
	if err != nil {
		t.Fatalf("UpsertDocument failed: %v", err)
	}

	// Initially no embedding metadata
	meta, err := store.GetEmbeddingMetadata(ctx, chunkID)
	if err != nil {
		t.Fatalf("GetEmbeddingMetadata failed: %v", err)
	}
	if meta != nil {
		t.Errorf("GetEmbeddingMetadata returned %v, expected nil", meta)
	}

	// Set embedding metadata
	metadata := &EmbeddingMetadata{
		ModelName:   "xlm-roberta-base",
		Dimensions:  768,
		Version:     "v1.0",
		GeneratedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	err = store.SetEmbeddingMetadata(ctx, chunkID, metadata)
	if err != nil {
		t.Fatalf("SetEmbeddingMetadata failed: %v", err)
	}

	// Retrieve metadata
	meta, err = store.GetEmbeddingMetadata(ctx, chunkID)
	if err != nil {
		t.Fatalf("GetEmbeddingMetadata after set failed: %v", err)
	}

	if meta == nil {
		t.Fatal("GetEmbeddingMetadata returned nil after set")
	}

	if meta.ModelName != metadata.ModelName {
		t.Errorf("ModelName=%q, expected %q", meta.ModelName, metadata.ModelName)
	}
	if meta.Dimensions != metadata.Dimensions {
		t.Errorf("Dimensions=%d, expected %d", meta.Dimensions, metadata.Dimensions)
	}
	if meta.Version != metadata.Version {
		t.Errorf("Version=%q, expected %q", meta.Version, metadata.Version)
	}

	// Metadata is copied, not referenced
	meta.ModelName = "modified"
	if metadata.ModelName != "xlm-roberta-base" {
		t.Error("Metadata was not copied; external modification affected stored metadata")
	}

	// Delete by setting nil
	err = store.SetEmbeddingMetadata(ctx, chunkID, nil)
	if err != nil {
		t.Fatalf("SetEmbeddingMetadata with nil failed: %v", err)
	}

	meta, err = store.GetEmbeddingMetadata(ctx, chunkID)
	if err != nil {
		t.Fatalf("GetEmbeddingMetadata after delete failed: %v", err)
	}
	if meta != nil {
		t.Errorf("GetEmbeddingMetadata after nil set returned %v, expected nil", meta)
	}
}

func TestFakeStore_GetChunksWithEmbeddingMetadata(t *testing.T) {
	ctx := context.Background()
	store := NewFakeStore()

	doc := &ArchiveDocument{
		StableID:      "doc-1",
		PlainText:     "Test",
		PlainTextHash: "hash-1",
		ChunkCount:    2,
		SourceProvenance: SourceProvenance{
			SourceID:    "source-1",
			SourceType:  types.SourceTypeWeb,
			RetrievedAt: time.Now(),
			ExtractedAt: time.Now(),
		},
		ArchivedAt: time.Now(),
	}

	chunks := []Chunk{
		{
			StableID:         "arch_doc:doc-1:hash-1:0:50",
			DocumentStableID: "doc-1",
			ContentHash:      "chunk-1",
			Position:         0,
			Length:           50,
			HasEmbedding:     false,
		},
		{
			StableID:         "arch_doc:doc-1:hash-1:50:50",
			DocumentStableID: "doc-1",
			ContentHash:      "chunk-2",
			Position:         50,
			Length:           50,
			HasEmbedding:     false,
		},
	}

	err := store.UpsertDocument(ctx, doc, chunks)
	if err != nil {
		t.Fatalf("UpsertDocument failed: %v", err)
	}

	// Initially no chunks with embeddings
	withEmbeddings, err := store.GetChunksWithEmbeddingMetadata(ctx)
	if err != nil {
		t.Fatalf("GetChunksWithEmbeddingMetadata failed: %v", err)
	}
	if len(withEmbeddings) != 0 {
		t.Errorf("GetChunksWithEmbeddingMetadata returned %d chunks, expected 0", len(withEmbeddings))
	}

	// Add embeddings to one chunk
	err = store.SetEmbeddingMetadata(ctx, StableChunkID("arch_doc:doc-1:hash-1:0:50"), &EmbeddingMetadata{
		ModelName:  "xlm-roberta-base",
		Dimensions: 768,
	})
	if err != nil {
		t.Fatalf("SetEmbeddingMetadata failed: %v", err)
	}

	withEmbeddings, err = store.GetChunksWithEmbeddingMetadata(ctx)
	if err != nil {
		t.Fatalf("GetChunksWithEmbeddingMetadata after set failed: %v", err)
	}
	if len(withEmbeddings) != 1 {
		t.Errorf("GetChunksWithEmbeddingMetadata returned %d chunks, expected 1", len(withEmbeddings))
	}
}

func TestFakeStore_DeleteChunk(t *testing.T) {
	ctx := context.Background()
	store := NewFakeStore()

	doc := &ArchiveDocument{
		StableID:      "doc-1",
		PlainText:     "Test",
		PlainTextHash: "hash-1",
		ChunkCount:    1,
		SourceProvenance: SourceProvenance{
			SourceID:    "source-1",
			SourceType:  types.SourceTypeWeb,
			RetrievedAt: time.Now(),
			ExtractedAt: time.Now(),
		},
		ArchivedAt: time.Now(),
	}

	chunkID := StableChunkID("arch_doc:doc-1:hash-1:0:10")
	chunks := []Chunk{{
		StableID:         chunkID,
		DocumentStableID: "doc-1",
		ContentHash:      "chunk-1",
		Position:         0,
		Length:           10,
		HasEmbedding:     false,
	}}

	err := store.UpsertDocument(ctx, doc, chunks)
	if err != nil {
		t.Fatalf("UpsertDocument failed: %v", err)
	}

	err = store.DeleteChunk(ctx, chunkID)
	if err != nil {
		t.Fatalf("DeleteChunk failed: %v", err)
	}

	if store.ChunkCount() != 0 {
		t.Errorf("ChunkCount=%d, expected 0", store.ChunkCount())
	}

	// Delete non-existent chunk
	err = store.DeleteChunk(ctx, "non-existent")
	if !IsChunkNotFoundError(err) {
		t.Errorf("DeleteChunk for non-existent chunk returned error %v, expected ChunkNotFoundError", err)
	}
}

func TestFakeStore_DocumentExists(t *testing.T) {
	ctx := context.Background()
	store := NewFakeStore()

	// Initially no documents
	exists, err := store.DocumentExists(ctx, "doc-1")
	if err != nil {
		t.Fatalf("DocumentExists failed: %v", err)
	}
	if exists {
		t.Error("DocumentExists returned true for non-existent document")
	}

	doc := &ArchiveDocument{
		StableID:      "doc-1",
		PlainText:     "Test",
		PlainTextHash: "hash-1",
		ChunkCount:    0,
		SourceProvenance: SourceProvenance{
			SourceID:    "source-1",
			SourceType:  types.SourceTypeWeb,
			RetrievedAt: time.Now(),
			ExtractedAt: time.Now(),
		},
		ArchivedAt: time.Now(),
	}

	err = store.UpsertDocument(ctx, doc, []Chunk{})
	if err != nil {
		t.Fatalf("UpsertDocument failed: %v", err)
	}

	exists, err = store.DocumentExists(ctx, "doc-1")
	if err != nil {
		t.Fatalf("DocumentExists failed: %v", err)
	}
	if !exists {
		t.Error("DocumentExists returned false for existing document")
	}
}

func TestFakeStore_Retrieve(t *testing.T) {
	ctx := context.Background()
	store := NewFakeStore()

	doc := &ArchiveDocument{
		StableID:      "doc-1",
		PlainText:     "The quick brown fox jumps over the lazy dog",
		PlainTextHash: "hash-1",
		ChunkCount:    1,
		SourceProvenance: SourceProvenance{
			SourceID:    "source-1",
			SourceURL:   "https://example.com/article",
			SourceType:  types.SourceTypeWeb,
			RetrievedAt: time.Now(),
			ExtractedAt: time.Now(),
		},
		ArchivedAt: time.Now(),
	}

	err := store.UpsertDocument(ctx, doc, []Chunk{{
		StableID:         "arch_doc:doc-1:hash-1:0:50",
		DocumentStableID: "doc-1",
		ContentHash:      "chunk-1",
		Position:         0,
		Length:           50,
		HasEmbedding:     false,
	}})
	if err != nil {
		t.Fatalf("UpsertDocument failed: %v", err)
	}

	query := RetrievalQuery{
		Text:      "quick brown fox",
		Target:    RetrievalTargetDocument,
		Limit:     10,
		Embedding: nil,
	}

	result, err := store.Retrieve(ctx, query)
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}

	if len(result.Hits) != 1 {
		t.Errorf("Retrieve returned %d hits, expected 1", len(result.Hits))
	}

	if result.Hits[0].RelevanceScore != 1.0 {
		t.Errorf("RelevanceScore=%f, expected 1.0", result.Hits[0].RelevanceScore)
	}

	if result.Hits[0].DocumentContent == nil {
		t.Error("DocumentContent is nil")
	} else if *result.Hits[0].DocumentContent != doc.PlainText {
		t.Errorf("DocumentContent=%q, expected %q", *result.Hits[0].DocumentContent, doc.PlainText)
	}

	// Query that doesn't match
	query.Text = "nonexistent text"
	result, err = store.Retrieve(ctx, query)
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}

	if len(result.Hits) != 0 {
		t.Errorf("Retrieve returned %d hits for non-matching query, expected 0", len(result.Hits))
	}
}

func TestFakeStore_RetrieveFixedHits(t *testing.T) {
	ctx := context.Background()
	store := NewFakeStore()

	fixedHits := []RetrievalHit{
		{
			TargetType:       RetrievalTargetDocument,
			DocumentStableID: "doc-1",
			SourceID:         "source-1",
			RelevanceScore:   0.95,
		},
		{
			TargetType:       RetrievalTargetDocument,
			DocumentStableID: "doc-2",
			SourceID:         "source-1",
			RelevanceScore:   0.85,
		},
	}

	store.SetRetrieveHits(fixedHits)

	query := RetrievalQuery{
		Text:   "test query",
		Target: RetrievalTargetDocument,
	}

	result, err := store.Retrieve(ctx, query)
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}

	if len(result.Hits) != 2 {
		t.Errorf("Retrieve returned %d hits, expected 2", len(result.Hits))
	}

	// Check ordering by relevance score
	if result.Hits[0].RelevanceScore != 0.95 {
		t.Errorf("First hit has RelevanceScore=%f, expected 0.95", result.Hits[0].RelevanceScore)
	}

	// Apply limit
	query.Limit = 1
	result, err = store.Retrieve(ctx, query)
	if err != nil {
		t.Fatalf("Retrieve with limit failed: %v", err)
	}

	if len(result.Hits) != 1 {
		t.Errorf("Retrieve with limit=1 returned %d hits, expected 1", len(result.Hits))
	}

	store.ClearRetrieveHits()
}

func TestFakeStore_Retriever_ContextCancellation(t *testing.T) {
	ctx := context.Background()
	store := NewFakeStore()

	// Fake store doesn't support context cancellation, but the call should complete
	err := store.HealthCheck(ctx)
	if err != nil {
		t.Errorf("HealthCheck failed: %v", err)
	}
}

func TestFakeStore_HealthCheck(t *testing.T) {
	ctx := context.Background()
	store := NewFakeStore()

	err := store.HealthCheck(ctx)
	if err != nil {
		t.Errorf("HealthCheck returned error: %v", err)
	}
}

func TestFakeStore_SetError(t *testing.T) {
	ctx := context.Background()
	store := NewFakeStore()

	// Configure error
	store.SetError("GetDocument", &DocumentNotFoundError{StableID: "doc-1"})

	// The SetError is used internally, so we just verify it doesn't panic
	store.SetError("GetDocument", &DocumentNotFoundError{StableID: "doc-1"})
	store.SetError("GetDocument", nil)
	store.ClearErrors()

	// Verify error is set
	_, err := store.GetDocument(ctx, "doc-1")
	if !IsDocumentNotFoundError(err) {
		t.Errorf("After ClearErrors, GetDocument returned %v, expected DocumentNotFoundError", err)
	}
}

func TestFakeStore_GetDocuments(t *testing.T) {
	ctx := context.Background()
	store := NewFakeStore()

	doc1 := &ArchiveDocument{
		StableID:      "doc-1",
		PlainText:     "First",
		PlainTextHash: "hash-1",
		ChunkCount:    0,
		SourceProvenance: SourceProvenance{
			SourceID:    "source-1",
			SourceType:  types.SourceTypeWeb,
			RetrievedAt: time.Now(),
			ExtractedAt: time.Now(),
		},
		ArchivedAt: time.Now(),
	}
	doc2 := &ArchiveDocument{
		StableID:      "doc-2",
		PlainText:     "Second",
		PlainTextHash: "hash-2",
		ChunkCount:    0,
		SourceProvenance: SourceProvenance{
			SourceID:    "source-1",
			SourceType:  types.SourceTypeWeb,
			RetrievedAt: time.Now(),
			ExtractedAt: time.Now(),
		},
		ArchivedAt: time.Now().Add(time.Hour),
	}

	_ = store.UpsertDocument(ctx, doc1, []Chunk{})
	_ = store.UpsertDocument(ctx, doc2, []Chunk{})

	docs := store.GetDocuments()
	if len(docs) != 2 {
		t.Errorf("GetDocuments returned %d docs, expected 2", len(docs))
	}
}

func TestFakeStore_GetChunks(t *testing.T) {
	ctx := context.Background()
	store := NewFakeStore()

	doc := &ArchiveDocument{
		StableID:      "doc-1",
		PlainText:     "Test",
		PlainTextHash: "hash-1",
		ChunkCount:    2,
		SourceProvenance: SourceProvenance{
			SourceID:    "source-1",
			SourceType:  types.SourceTypeWeb,
			RetrievedAt: time.Now(),
			ExtractedAt: time.Now(),
		},
		ArchivedAt: time.Now(),
	}

	chunks := []Chunk{
		{StableID: "chunk-1", DocumentStableID: "doc-1", ContentHash: "h1", Position: 0, Length: 50},
		{StableID: "chunk-2", DocumentStableID: "doc-1", ContentHash: "h2", Position: 50, Length: 50},
	}

	_ = store.UpsertDocument(ctx, doc, chunks)

	allChunks := store.GetChunks()
	if len(allChunks) != 2 {
		t.Errorf("GetChunks returned %d chunks, expected 2", len(allChunks))
	}
}

func TestFakeStore_EmptyRetrieve(t *testing.T) {
	ctx := context.Background()
	store := NewFakeStore()

	query := RetrievalQuery{
		Text:      "",
		Target:    RetrievalTargetDocument,
		Limit:     10,
		Embedding: nil,
	}

	result, err := store.Retrieve(ctx, query)
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}

	if len(result.Hits) != 0 {
		t.Errorf("Retrieve with empty text returned %d hits, expected 0", len(result.Hits))
	}
}

func TestFakeStore_RetrieveWithLimit(t *testing.T) {
	ctx := context.Background()
	store := NewFakeStore()

	// Add multiple documents
	for i := 1; i <= 5; i++ {
		doc := &ArchiveDocument{
			StableID:      StableDocumentID("doc-" + string(rune('0'+i))),
			PlainText:     "Test content " + string(rune('0'+i)),
			PlainTextHash: ContentHash("hash-" + string(rune('0'+i))),
			ChunkCount:    1,
			SourceProvenance: SourceProvenance{
				SourceID:    "source-1",
				SourceType:  types.SourceTypeWeb,
				RetrievedAt: time.Now(),
				ExtractedAt: time.Now(),
			},
			ArchivedAt: time.Now(),
		}
		_ = store.UpsertDocument(ctx, doc, []Chunk{{
			StableID:         StableChunkID("arch_doc:doc-" + string(rune('0'+i)) + ":hash-" + string(rune('0'+i)) + ":0:20"),
			DocumentStableID: StableDocumentID("doc-" + string(rune('0'+i))),
			ContentHash:      "chunk-1",
			Position:         0,
			Length:           20,
			HasEmbedding:     false,
		}})
	}

	query := RetrievalQuery{
		Text:      "content",
		Target:    RetrievalTargetDocument,
		Limit:     3,
		Embedding: nil,
	}

	result, err := store.Retrieve(ctx, query)
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}

	if len(result.Hits) != 3 {
		t.Errorf("Retrieve with limit=3 returned %d hits, expected 3", len(result.Hits))
	}
}

func TestParseChunkID(t *testing.T) {
	tests := []struct {
		name        string
		chunkID     StableChunkID
		expectDocID StableDocumentID
		expectHash  ContentHash
		expectPos   int
		expectLen   int
		expectError bool
	}{
		{
			name:        "valid chunk ID",
			chunkID:     "arch_doc:doc-1:hash-abc:100:200",
			expectDocID: "doc-1",
			expectHash:  "hash-abc",
			expectPos:   100,
			expectLen:   200,
			expectError: false,
		},
		{
			name:        "valid chunk ID with special characters",
			chunkID:     "arch_doc:doc_123:hash_xyz:0:999999",
			expectDocID: "doc_123",
			expectHash:  "hash_xyz",
			expectPos:   0,
			expectLen:   999999,
			expectError: false,
		},
		{
			name:        "invalid format - missing parts",
			chunkID:     "arch_doc:doc-1:hash-1",
			expectError: true,
		},
		{
			name:        "invalid format - wrong prefix",
			chunkID:     "doc:arch:doc-1:hash-1:100:200",
			expectError: true,
		},
		{
			name:        "invalid position",
			chunkID:     "arch_doc:doc-1:hash-1:abc:200",
			expectError: true,
		},
		{
			name:        "invalid length",
			chunkID:     "arch_doc:doc-1:hash-1:100:def",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			docID, contentHash, pos, length, err := parseChunkID(tt.chunkID)

			if tt.expectError {
				if err == nil {
					t.Errorf("parseChunkID returned no error, expected error")
				}
				return
			}

			if err != nil {
				t.Fatalf("parseChunkID returned unexpected error: %v", err)
			}

			if docID != tt.expectDocID {
				t.Errorf("docID=%q, expected %q", docID, tt.expectDocID)
			}
			if contentHash != tt.expectHash {
				t.Errorf("contentHash=%q, expected %q", contentHash, tt.expectHash)
			}
			if pos != tt.expectPos {
				t.Errorf("pos=%d, expected %d", pos, tt.expectPos)
			}
			if length != tt.expectLen {
				t.Errorf("length=%d, expected %d", length, tt.expectLen)
			}
		})
	}
}
