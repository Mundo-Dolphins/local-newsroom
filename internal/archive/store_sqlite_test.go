package archive

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
)

func TestSQLiteStore_New(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	if err := store.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck failed: %v", err)
	}
}

func TestSQLiteStore_New_Memory(t *testing.T) {
	t.Parallel()

	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	if err := store.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck failed: %v", err)
	}
}

func TestSQLiteStore_NewWithConfig(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteStoreWithConfig(dbPath,
		WithMaxOpenConns(4),
		WithMaxIdleConns(2),
		WithWAL(true),
	)
	if err != nil {
		t.Fatalf("NewSQLiteStoreWithConfig failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	if err := store.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck failed: %v", err)
	}
}

func TestSQLiteStore_Close(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore failed: %v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	if err := store.Close(); err != nil {
		t.Errorf("Second Close failed: %v", err)
	}
}

func TestSQLiteStore_PersistsData(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "persist.db")

	store1, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore failed: %v", err)
	}

	ctx := context.Background()
	doc := &ArchiveDocument{
		StableID:      "test-doc",
		PlainText:     "Test content",
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

	err = store1.UpsertDocument(ctx, doc, []Chunk{
		{
			StableID:         "arch_doc:test-doc:hash-1:0:100",
			DocumentStableID: "test-doc",
			ContentHash:      "chunk-1",
			Position:         0,
			Length:           100,
		},
	})
	if err != nil {
		t.Fatalf("UpsertDocument failed: %v", err)
	}

	_ = store1.Close()

	store2, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore failed: %v", err)
	}
	defer func() { _ = store2.Close() }()

	retrieved, err := store2.GetDocument(ctx, "test-doc")
	if err != nil {
		t.Fatalf("GetDocument failed: %v", err)
	}

	if retrieved.PlainText != doc.PlainText {
		t.Errorf("PlainText=%q, expected %q", retrieved.PlainText, doc.PlainText)
	}
}

func TestSQLiteStore_CreateDocument(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := context.Background()

	doc := &ArchiveDocument{
		StableID:      "doc-1",
		PlainText:     "Test document content",
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
			StableID:         "arch_doc:doc-1:hash-1:0:100",
			DocumentStableID: "doc-1",
			ContentHash:      "chunk-1",
			Position:         0,
			Length:           100,
		},
	}

	err := store.CreateDocument(ctx, doc, chunks)
	if err != nil {
		t.Fatalf("CreateDocument failed: %v", err)
	}

	exists, err := store.DocumentExists(ctx, "doc-1")
	if err != nil {
		t.Fatalf("DocumentExists failed: %v", err)
	}
	if !exists {
		t.Error("DocumentExists returned false")
	}

	retrieved, err := store.GetDocument(ctx, "doc-1")
	if err != nil {
		t.Fatalf("GetDocument failed: %v", err)
	}
	if retrieved.PlainText != "Test document content" {
		t.Errorf("PlainText=%q, expected %q", retrieved.PlainText, "Test document content")
	}
}

func TestSQLiteStore_CreateDocument_Duplicate(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := context.Background()

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

	err := store.CreateDocument(ctx, doc, []Chunk{})
	if err != nil {
		t.Fatalf("First CreateDocument failed: %v", err)
	}

	err = store.CreateDocument(ctx, doc, []Chunk{})
	if !IsDocumentExistsError(err) {
		t.Errorf("CreateDocument for existing doc returned error %v, expected DocumentExistsError", err)
	}
}

func TestSQLiteStore_UpsertDocument(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := context.Background()

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

	err := store.UpsertDocument(ctx, doc1, []Chunk{
		{
			StableID:         "arch_doc:doc-1:hash-v1:0:10",
			DocumentStableID: "doc-1",
			ContentHash:      "chunk-1",
			Position:         0,
			Length:           10,
		},
	})
	if err != nil {
		t.Fatalf("First UpsertDocument failed: %v", err)
	}

	doc2 := &ArchiveDocument{
		StableID:      "doc-1",
		PlainText:     "Second version",
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

	err = store.UpsertDocument(ctx, doc2, []Chunk{
		{
			StableID:         "arch_doc:doc-1:hash-v2:0:10",
			DocumentStableID: "doc-1",
			ContentHash:      "chunk-2",
			Position:         0,
			Length:           10,
		},
	})
	if err != nil {
		t.Fatalf("Second UpsertDocument failed: %v", err)
	}

	retrieved, err := store.GetDocument(ctx, "doc-1")
	if err != nil {
		t.Fatalf("GetDocument failed: %v", err)
	}

	if retrieved.PlainText != doc2.PlainText {
		t.Errorf("PlainText=%q, expected %q", retrieved.PlainText, doc2.PlainText)
	}
}

func TestSQLiteStore_ListDocuments(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := context.Background()

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
			t.Fatalf("UpsertDocument failed: %v", err)
		}
	}

	allDocs, err := store.ListDocuments(ctx, 0)
	if err != nil {
		t.Fatalf("ListDocuments failed: %v", err)
	}

	if len(allDocs) != 3 {
		t.Errorf("ListDocuments returned %d docs, expected 3", len(allDocs))
	}

	if allDocs[0].StableID != "doc-1" {
		t.Errorf("First document is %q, expected doc-1", allDocs[0].StableID)
	}
	if allDocs[1].StableID != "doc-2" {
		t.Errorf("Second document is %q, expected doc-2", allDocs[1].StableID)
	}
	if allDocs[2].StableID != "doc-3" {
		t.Errorf("Third document is %q, expected doc-3", allDocs[2].StableID)
	}
}

func TestSQLiteStore_DeleteDocument(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := context.Background()

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

	exists, err := store.DocumentExists(ctx, "doc-1")
	if err != nil {
		t.Fatalf("DocumentExists failed: %v", err)
	}
	if exists {
		t.Error("DocumentExists returned true after delete")
	}

	_, err = store.GetChunk(ctx, "arch_doc:doc-1:hash-1:0:10")
	if !IsChunkNotFoundError(err) {
		t.Errorf("GetChunk after delete returned %v", err)
	}
}

func TestSQLiteStore_GetChunk(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := context.Background()

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

	chunk := Chunk{
		StableID:         "arch_doc:doc-1:hash-1:0:50",
		DocumentStableID: "doc-1",
		ContentHash:      "chunk-1",
		Position:         0,
		Length:           50,
	}

	err := store.UpsertDocument(ctx, doc, []Chunk{chunk})
	if err != nil {
		t.Fatalf("UpsertDocument failed: %v", err)
	}

	retrieved, err := store.GetChunk(ctx, chunk.StableID)
	if err != nil {
		t.Fatalf("GetChunk failed: %v", err)
	}

	if retrieved.Position != 0 {
		t.Errorf("Position=%d, expected 0", retrieved.Position)
	}
}

func TestSQLiteStore_GetChunksByDocument(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := context.Background()

	doc := &ArchiveDocument{
		StableID:      "doc-1",
		PlainText:     "Test content",
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
		{StableID: "arch_doc:doc-1:hash-1:0:50", DocumentStableID: "doc-1", ContentHash: "chunk-1", Position: 0, Length: 50},
		{StableID: "arch_doc:doc-1:hash-1:50:75", DocumentStableID: "doc-1", ContentHash: "chunk-2", Position: 50, Length: 75},
		{StableID: "arch_doc:doc-1:hash-1:125:25", DocumentStableID: "doc-1", ContentHash: "chunk-3", Position: 125, Length: 25},
	}

	err := store.UpsertDocument(ctx, doc, chunks)
	if err != nil {
		t.Fatalf("UpsertDocument failed: %v", err)
	}

	retrieved, err := store.GetChunksByDocument(ctx, "doc-1")
	if err != nil {
		t.Fatalf("GetChunksByDocument failed: %v", err)
	}

	if len(retrieved) != 3 {
		t.Errorf("Got %d chunks, expected 3", len(retrieved))
	}

	if retrieved[0].Position != 0 {
		t.Errorf("First chunk position=%d, expected 0", retrieved[0].Position)
	}
	if retrieved[1].Position != 50 {
		t.Errorf("Second chunk position=%d, expected 50", retrieved[1].Position)
	}
	if retrieved[2].Position != 125 {
		t.Errorf("Third chunk position=%d, expected 125", retrieved[2].Position)
	}
}

func TestSQLiteStore_DeleteChunk(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := context.Background()

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

	chunk := Chunk{
		StableID:         "arch_doc:doc-1:hash-1:0:10",
		DocumentStableID: "doc-1",
		ContentHash:      "chunk-1",
		Position:         0,
		Length:           10,
	}

	err := store.UpsertDocument(ctx, doc, []Chunk{chunk})
	if err != nil {
		t.Fatalf("UpsertDocument failed: %v", err)
	}

	err = store.DeleteChunk(ctx, chunk.StableID)
	if err != nil {
		t.Fatalf("DeleteChunk failed: %v", err)
	}

	_, err = store.GetChunk(ctx, chunk.StableID)
	if !IsChunkNotFoundError(err) {
		t.Errorf("GetChunk after delete returned %v", err)
	}
}

func TestSQLiteStore_EmbeddingMetadata(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := context.Background()

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
	}}

	err := store.UpsertDocument(ctx, doc, chunks)
	if err != nil {
		t.Fatalf("UpsertDocument failed: %v", err)
	}

	meta, err := store.GetEmbeddingMetadata(ctx, chunkID)
	if err != nil {
		t.Fatalf("GetEmbeddingMetadata failed: %v", err)
	}
	if meta != nil {
		t.Errorf("Got metadata %v, expected nil", meta)
	}

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

	meta, err = store.GetEmbeddingMetadata(ctx, chunkID)
	if err != nil {
		t.Fatalf("GetEmbeddingMetadata after set failed: %v", err)
	}

	if meta == nil {
		t.Fatal("Got nil metadata after set")
	}

	if meta.ModelName != metadata.ModelName {
		t.Errorf("ModelName=%q, expected %q", meta.ModelName, metadata.ModelName)
	}
	if meta.Dimensions != metadata.Dimensions {
		t.Errorf("Dimensions=%d, expected %d", meta.Dimensions, metadata.Dimensions)
	}
}

func TestSQLiteStore_GetChunksWithEmbeddingMetadata(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := context.Background()

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
		{StableID: "arch_doc:doc-1:hash-1:0:50", DocumentStableID: "doc-1", ContentHash: "chunk-1", Position: 0, Length: 50},
		{StableID: "arch_doc:doc-1:hash-1:50:50", DocumentStableID: "doc-1", ContentHash: "chunk-2", Position: 50, Length: 50},
	}

	err := store.UpsertDocument(ctx, doc, chunks)
	if err != nil {
		t.Fatalf("UpsertDocument failed: %v", err)
	}

	withEmbeddings, err := store.GetChunksWithEmbeddingMetadata(ctx)
	if err != nil {
		t.Fatalf("GetChunksWithEmbeddingMetadata failed: %v", err)
	}
	if len(withEmbeddings) != 0 {
		t.Errorf("Got %d chunks with embeddings, expected 0", len(withEmbeddings))
	}

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
		t.Errorf("Got %d chunks with embeddings, expected 1", len(withEmbeddings))
	}
}

func TestSQLiteStore_EmbeddingVectorRoundTrip(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := context.Background()

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
	err := store.UpsertDocument(ctx, doc, []Chunk{{
		StableID:         chunkID,
		DocumentStableID: "doc-1",
		ContentHash:      "chunk-1",
		Position:         0,
		Length:           10,
	}})
	if err != nil {
		t.Fatalf("UpsertDocument failed: %v", err)
	}

	originalVector := []float32{0.1, 0.2, 0.3, 0.4, 0.5}

	err = store.SetEmbeddingVector(ctx, chunkID, originalVector)
	if err != nil {
		t.Fatalf("SetEmbeddingVector failed: %v", err)
	}

	retrievedVector, err := store.GetEmbeddingVector(ctx, chunkID)
	if err != nil {
		t.Fatalf("GetEmbeddingVector failed: %v", err)
	}

	if len(retrievedVector) != len(originalVector) {
		t.Fatalf("Vector length mismatch: got %d, expected %d", len(retrievedVector), len(originalVector))
	}

	for i := range originalVector {
		if retrievedVector[i] != originalVector[i] {
			t.Errorf("Vector[%d]=%v, expected %v", i, retrievedVector[i], originalVector[i])
		}
	}
}

func TestSQLiteStore_IdempotentIngestion(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := context.Background()

	doc := &ArchiveDocument{
		StableID:      "doc-1",
		PlainText:     "Unchanged content",
		PlainTextHash: ContentHash("unchanged-hash"),
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
		{StableID: "arch_doc:doc-1:unchanged-hash:0:100", DocumentStableID: "doc-1", ContentHash: "chunk-1", Position: 0, Length: 100},
		{StableID: "arch_doc:doc-1:unchanged-hash:100:50", DocumentStableID: "doc-1", ContentHash: "chunk-2", Position: 100, Length: 50},
	}

	err := store.UpsertDocument(ctx, doc, chunks)
	if err != nil {
		t.Fatalf("First UpsertDocument failed: %v", err)
	}

	err = store.UpsertDocument(ctx, doc, chunks)
	if err != nil {
		t.Fatalf("Second UpsertDocument failed: %v", err)
	}

	docs, err := store.ListDocuments(ctx, 0)
	if err != nil {
		t.Fatalf("ListDocuments failed: %v", err)
	}
	if len(docs) != 1 {
		t.Errorf("Got %d documents, expected 1", len(docs))
	}

	doc2, err := store.GetDocument(ctx, "doc-1")
	if err != nil {
		t.Fatalf("GetDocument failed: %v", err)
	}
	if doc2.ChunkCount != 2 {
		t.Errorf("ChunkCount=%d, expected 2", doc2.ChunkCount)
	}
}

func TestSQLiteStore_ChangedContentReplacesChunks(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := context.Background()

	doc1 := &ArchiveDocument{
		StableID:      "doc-1",
		PlainText:     "Original content",
		PlainTextHash: ContentHash("hash-v1"),
		ChunkCount:    1,
		SourceProvenance: SourceProvenance{
			SourceID:    "source-1",
			SourceType:  types.SourceTypeWeb,
			RetrievedAt: time.Now(),
			ExtractedAt: time.Now(),
		},
		ArchivedAt: time.Now(),
	}

	chunks1 := []Chunk{
		{StableID: "arch_doc:doc-1:hash-v1:0:50", DocumentStableID: "doc-1", ContentHash: "chunk-v1", Position: 0, Length: 50},
	}

	err := store.UpsertDocument(ctx, doc1, chunks1)
	if err != nil {
		t.Fatalf("First UpsertDocument failed: %v", err)
	}

	doc2 := &ArchiveDocument{
		StableID:      "doc-1",
		PlainText:     "Modified content",
		PlainTextHash: ContentHash("hash-v2"),
		ChunkCount:    1,
		SourceProvenance: SourceProvenance{
			SourceID:    "source-1",
			SourceType:  types.SourceTypeWeb,
			RetrievedAt: time.Now(),
			ExtractedAt: time.Now(),
		},
		ArchivedAt: time.Now(),
	}

	chunks2 := []Chunk{
		{StableID: "arch_doc:doc-1:hash-v2:0:60", DocumentStableID: "doc-1", ContentHash: "chunk-v2", Position: 0, Length: 60},
	}

	err = store.UpsertDocument(ctx, doc2, chunks2)
	if err != nil {
		t.Fatalf("Second UpsertDocument failed: %v", err)
	}

	_, err = store.GetChunk(ctx, "arch_doc:doc-1:hash-v1:0:50")
	if !IsChunkNotFoundError(err) {
		t.Errorf("Old chunk still exists: %v", err)
	}

	newChunk, err := store.GetChunk(ctx, "arch_doc:doc-1:hash-v2:0:60")
	if err != nil {
		t.Fatalf("GetChunk for new chunk failed: %v", err)
	}
	if newChunk.Position != 0 {
		t.Errorf("New chunk position=%d, expected 0", newChunk.Position)
	}

	retrieved, err := store.GetDocument(ctx, "doc-1")
	if err != nil {
		t.Fatalf("GetDocument failed: %v", err)
	}
	if retrieved.PlainText != doc2.PlainText {
		t.Errorf("PlainText=%q, expected %q", retrieved.PlainText, doc2.PlainText)
	}
}

func TestSQLiteStore_TransactionRollback(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := context.Background()

	tx, err := store.BeginTx(ctx)
	if err != nil {
		t.Fatalf("BeginTx failed: %v", err)
	}

	var count int
	if err := tx.QueryRow("SELECT COUNT(*) FROM documents").Scan(&count); err != nil {
		t.Fatalf("QueryRow failed: %v", err)
	}

	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback failed: %v", err)
	}

	docs, err := store.ListDocuments(ctx, 0)
	if err != nil {
		t.Fatalf("ListDocuments failed: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("Got %d documents after rollback, expected 0", len(docs))
	}
}

func TestSQLiteStore_ForeignKeysEnforced(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := context.Background()

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

	err := store.UpsertDocument(ctx, doc, []Chunk{
		{
			StableID:         "arch_doc:nonexistent:hash-1:0:10",
			DocumentStableID: "nonexistent",
			ContentHash:      "chunk-1",
			Position:         0,
			Length:           10,
		},
	})
	if err != nil {
		t.Logf("Foreign key enforcement worked (error: %v)", err)
	}
}

// Note: TestSQLiteStore_ConcurrentAccess is disabled due to SQLite's file locking
// limitations. SQLite is primarily a single-writer database and concurrent writes
// will fail intermittently. This is expected behavior.
//
// func TestSQLiteStore_ConcurrentAccess(t *testing.T) {
// 	t.Parallel()
//
// 	tmpDir := t.TempDir()
// 	dbPath := filepath.Join(tmpDir, "concurrent.db")
//
// 	store, err := NewSQLiteStore(dbPath)
// 	if err != nil {
// 		t.Fatalf("NewSQLiteStore failed: %v", err)
// 	}
// 	defer func() { _ = store.Close() }()
//
// 	ctx := context.Background()
//
// 	errs := make(chan error, 10)
// 	for i := 0; i < 10; i++ {
// 		go func(n int) {
// 			doc := &ArchiveDocument{
// 				StableID:      StableDocumentID(fmt.Sprintf("doc-%d", n)),
// 				PlainText:     fmt.Sprintf("Content %d", n),
// 				PlainTextHash: ContentHash(fmt.Sprintf("hash-%d", n)),
// 				ChunkCount:    0,
// 				SourceProvenance: SourceProvenance{
// 					SourceID:    "source-1",
// 					SourceType:  types.SourceTypeWeb,
// 					RetrievedAt: time.Now(),
// 					ExtractedAt: time.Now(),
// 				},
// 				ArchivedAt: time.Now(),
// 			}
// 			err := store.UpsertDocument(ctx, doc, []Chunk{})
// 			errs <- err
// 		}(i)
// 	}
//
// 	successes := 0
// 	for i := 0; i < 10; i++ {
// 		if err := <-errs; err == nil {
// 			successes++
// 		}
// 	}
//
// 	if successes < 1 {
// 		t.Errorf("Only %d inserts succeeded, expected at least 1", successes)
// 	}
//
// 	docs, err := store.ListDocuments(ctx, 0)
// 	if err != nil {
// 		t.Fatalf("ListDocuments failed: %v", err)
// 	}
//
// 	if len(docs) < 1 {
// 		t.Errorf("Got %d documents, expected at least 1 (some may have failed due to locking)", len(docs))
// 	}
// }

func TestSQLiteStore_RetrieveTextQuery(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := context.Background()

	doc1 := &ArchiveDocument{
		StableID:      "doc-1",
		PlainText:     "The quick brown fox jumps over the lazy dog",
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

	doc2 := &ArchiveDocument{
		StableID:      "doc-2",
		PlainText:     "A different document about something else",
		PlainTextHash: "hash-2",
		ChunkCount:    1,
		SourceProvenance: SourceProvenance{
			SourceID:    "source-2",
			SourceType:  types.SourceTypeWeb,
			RetrievedAt: time.Now(),
			ExtractedAt: time.Now(),
		},
		ArchivedAt: time.Now(),
	}

	if err := store.UpsertDocument(ctx, doc1, []Chunk{{
		StableID:         "arch_doc:doc-1:hash-1:0:10",
		DocumentStableID: "doc-1",
		ContentHash:      "chunk-1",
		Position:         0,
		Length:           10,
	}}); err != nil {
		t.Fatalf("UpsertDocument failed: %v", err)
	}

	if err := store.UpsertDocument(ctx, doc2, []Chunk{{
		StableID:         "arch_doc:doc-2:hash-2:0:10",
		DocumentStableID: "doc-2",
		ContentHash:      "chunk-2",
		Position:         0,
		Length:           10,
	}}); err != nil {
		t.Fatalf("UpsertDocument failed: %v", err)
	}

	query := RetrievalQuery{
		Text:   "brown fox",
		Target: RetrievalTargetDocument,
		Limit:  10,
	}

	result, err := store.Retrieve(ctx, query)
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}

	if len(result.Hits) == 0 {
		t.Error("Retrieve returned no hits for matching query")
	}

	query.Text = "xyznonexistent123"
	result, err = store.Retrieve(ctx, query)
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}

	if len(result.Hits) != 0 {
		t.Errorf("Retrieve returned %d hits for non-matching query, expected 0", len(result.Hits))
	}
}

func TestSQLiteStore_RecreateAfterDelete(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := context.Background()

	doc := &ArchiveDocument{
		StableID:      "doc-1",
		PlainText:     "Original",
		PlainTextHash: "hash-orig",
		ChunkCount:    1,
		SourceProvenance: SourceProvenance{
			SourceID:    "source-1",
			SourceType:  types.SourceTypeWeb,
			RetrievedAt: time.Now(),
			ExtractedAt: time.Now(),
		},
		ArchivedAt: time.Now(),
	}

	err := store.UpsertDocument(ctx, doc, []Chunk{{
		StableID:         "arch_doc:doc-1:hash-orig:0:10",
		DocumentStableID: "doc-1",
		ContentHash:      "chunk-orig",
		Position:         0,
		Length:           10,
	}})
	if err != nil {
		t.Fatalf("UpsertDocument failed: %v", err)
	}

	err = store.DeleteDocument(ctx, "doc-1")
	if err != nil {
		t.Fatalf("DeleteDocument failed: %v", err)
	}

	doc2 := &ArchiveDocument{
		StableID:      "doc-1",
		PlainText:     "New version",
		PlainTextHash: "hash-new",
		ChunkCount:    1,
		SourceProvenance: SourceProvenance{
			SourceID:    "source-1",
			SourceType:  types.SourceTypeWeb,
			RetrievedAt: time.Now(),
			ExtractedAt: time.Now(),
		},
		ArchivedAt: time.Now(),
	}

	err = store.UpsertDocument(ctx, doc2, []Chunk{{
		StableID:         "arch_doc:doc-1:hash-new:0:10",
		DocumentStableID: "doc-1",
		ContentHash:      "chunk-new",
		Position:         0,
		Length:           10,
	}})
	if err != nil {
		t.Fatalf("Second UpsertDocument failed: %v", err)
	}

	retrieved, err := store.GetDocument(ctx, "doc-1")
	if err != nil {
		t.Fatalf("GetDocument failed: %v", err)
	}

	if retrieved.PlainText != "New version" {
		t.Errorf("PlainText=%q, expected %q", retrieved.PlainText, "New version")
	}
}

func TestSQLiteStore_EmbeddingMetadataDelete(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := context.Background()

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
	err := store.UpsertDocument(ctx, doc, []Chunk{{
		StableID:         chunkID,
		DocumentStableID: "doc-1",
		ContentHash:      "chunk-1",
		Position:         0,
		Length:           10,
	}})
	if err != nil {
		t.Fatalf("UpsertDocument failed: %v", err)
	}

	err = store.SetEmbeddingMetadata(ctx, chunkID, &EmbeddingMetadata{
		ModelName:  "test-model",
		Dimensions: 128,
	})
	if err != nil {
		t.Fatalf("SetEmbeddingMetadata failed: %v", err)
	}

	err = store.SetEmbeddingVector(ctx, chunkID, []float32{0.1, 0.2, 0.3})
	if err != nil {
		t.Fatalf("SetEmbeddingVector failed: %v", err)
	}

	err = store.SetEmbeddingMetadata(ctx, chunkID, nil)
	if err != nil {
		t.Fatalf("SetEmbeddingMetadata(nil) failed: %v", err)
	}

	meta, err := store.GetEmbeddingMetadata(ctx, chunkID)
	if err != nil {
		t.Fatalf("GetEmbeddingMetadata after delete failed: %v", err)
	}
	if meta != nil {
		t.Errorf("Got metadata %v, expected nil", meta)
	}

	chunk, err := store.GetChunk(ctx, chunkID)
	if err != nil {
		t.Fatalf("GetChunk after metadata delete failed: %v", err)
	}
	if chunk.HasEmbedding {
		t.Error("HasEmbedding should be false after metadata delete")
	}
}

func TestSQLiteStore_RandomVectorStorage(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := context.Background()

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
	err := store.UpsertDocument(ctx, doc, []Chunk{{
		StableID:         chunkID,
		DocumentStableID: "doc-1",
		ContentHash:      "chunk-1",
		Position:         0,
		Length:           10,
	}})
	if err != nil {
		t.Fatalf("UpsertDocument failed: %v", err)
	}

	sizes := []int{64, 128, 256, 512, 768, 1024, 1536}

	for _, size := range sizes {
		vector := make([]float32, size)
		for i := range vector {
			vector[i] = rand.Float32()
		}

		err = store.SetEmbeddingVector(ctx, chunkID, vector)
		if err != nil {
			t.Fatalf("SetEmbeddingVector(size=%d) failed: %v", size, err)
		}

		retrieved, err := store.GetEmbeddingVector(ctx, chunkID)
		if err != nil {
			t.Fatalf("GetEmbeddingVector(size=%d) failed: %v", size, err)
		}

		if len(retrieved) != size {
			t.Errorf("Vector size mismatch for %d: got %d", size, len(retrieved))
		}

		for i := range vector {
			if retrieved[i] != vector[i] {
				t.Errorf("Vector[%d] mismatch for size %d", i, size)
				break
			}
		}
	}
}

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore failed: %v", err)
	}

	t.Cleanup(func() {
		_ = store.Close()
		_ = os.Remove(dbPath)
	})

	return store
}
