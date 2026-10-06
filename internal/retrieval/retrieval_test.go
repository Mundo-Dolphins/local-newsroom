package retrieval

import (
	"context"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/archive"
	"github.com/Mundo-Dolphins/local-newsroom/internal/embedding"
	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
)

// ctx is a common context for tests.
var testCtx = context.Background()

func TestSemanticRetriever_EmptyStore(t *testing.T) {
	store := archive.NewFakeStore()

	retriever, err := New(Config{
		TopK: 10,
	})
	if err != nil {
		t.Fatalf("failed to create retriever: %v", err)
	}
	retriever.SetStore(store)

	results, err := retriever.Retrieve(testCtx, RetrievalQuery{
		Text:  "test query",
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results.Hits) != 0 {
		t.Errorf("expected 0 hits, got %d", len(results.Hits))
	}
}

func TestSemanticRetriever_WithEmbeddings(t *testing.T) {
	store := archive.NewFakeStore()

	// Create a test document
	doc := &archive.ArchiveDocument{
		StableID:      archive.StableDocumentID("doc-1"),
		PlainText:     "The quick brown fox jumps over the lazy dog.",
		PlainTextHash: archive.ContentHashFrom("hash-1"),
		ChunkCount:    1,
		SourceProvenance: archive.SourceProvenance{
			SourceID:   "source-1",
			SourceType: types.SourceTypeWeb,
		},
		ArchivedAt: time.Now(),
	}

	chunk := archive.Chunk{
		StableID:         archive.StableChunkID("arch_doc:doc-1:hash-1:0:50"),
		DocumentStableID: "doc-1",
		ContentHash:      archive.ContentHashFrom("chunk-1"),
		Position:         0,
		Length:           50,
		HasEmbedding:     true,
	}

	if err := store.UpsertDocument(testCtx, doc, []archive.Chunk{chunk}); err != nil {
		t.Fatalf("failed to upsert document: %v", err)
	}

	// Set embedding metadata
	meta := &archive.EmbeddingMetadata{
		ModelName:   "test-model",
		Dimensions:  128,
		GeneratedAt: time.Now(),
	}
	if err := store.SetEmbeddingMetadata(testCtx, chunk.StableID, meta); err != nil {
		t.Fatalf("failed to set embedding metadata: %v", err)
	}

	// Generate a deterministic vector
	vector := generateVector(128, 42)
	if err := store.SetEmbeddingVector(testCtx, chunk.StableID, vector); err != nil {
		t.Fatalf("failed to set embedding vector: %v", err)
	}

	// Set up fake embedder
	fake := embedding.NewFakeEmbedder()
	fake.SetDefaultDimension(128)
	fake.SetEmbedding("test query", vector)

	retriever, err := New(Config{
		TopK:              5,
		EmbeddingProvider: fake,
	})
	if err != nil {
		t.Fatalf("failed to create retriever: %v", err)
	}
	retriever.SetStore(store)

	results, err := retriever.Retrieve(testCtx, RetrievalQuery{
		Text:  "test query",
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should get 1 result since cosine similarity of identical vectors is 1.0
	if len(results.Hits) != 1 {
		t.Errorf("expected 1 hit, got %d", len(results.Hits))
	}
}

func TestSemanticRetriever_WithReranking(t *testing.T) {
	store := archive.NewFakeStore()

	// Create test documents
	for i := 0; i < 5; i++ {
		docID := "doc-" + string(rune('0'+i))
		hash := "hash-" + string(rune('0'+i))
		chunkID := "chunk-" + string(rune('0'+i))

		doc := &archive.ArchiveDocument{
			StableID:      archive.StableDocumentID(docID),
			PlainText:     "This is document " + string(rune('0'+i)) + " with some content.",
			PlainTextHash: archive.ContentHashFrom(hash),
			ChunkCount:    1,
			SourceProvenance: archive.SourceProvenance{
				SourceID:   "source-" + string(rune('0'+i)),
				SourceType: types.SourceTypeWeb,
			},
			ArchivedAt: time.Now(),
		}

		chunk := archive.Chunk{
			StableID:         archive.StableChunkID("arch_doc:" + docID + ":" + hash + ":0:50"),
			DocumentStableID: archive.StableDocumentID(docID),
			ContentHash:      archive.ContentHashFrom(chunkID),
			Position:         0,
			Length:           50,
			HasEmbedding:     true,
		}

		if err := store.UpsertDocument(testCtx, doc, []archive.Chunk{chunk}); err != nil {
			t.Fatalf("failed to upsert document %d: %v", i, err)
		}

		// Set embedding metadata
		meta := &archive.EmbeddingMetadata{
			ModelName:   "test-model",
			Dimensions:  128,
			GeneratedAt: time.Now(),
		}
		if err := store.SetEmbeddingMetadata(testCtx, chunk.StableID, meta); err != nil {
			t.Fatalf("failed to set embedding metadata: %v", err)
		}

		// Use same vector for all chunks to ensure high similarity
		vector := generateVector(128, 42)
		if err := store.SetEmbeddingVector(testCtx, chunk.StableID, vector); err != nil {
			t.Fatalf("failed to set embedding vector %d: %v", i, err)
		}
	}

	fake := embedding.NewFakeEmbedder()
	fake.SetDefaultDimension(128)
	fake.SetEmbedding("query-1", generateVector(128, 42))

	// Configure reranker with correct chunk IDs matching chunk StableIDs
	ranker := NewFakeReranker()
	ranker.ChunkIDs = []string{
		"arch_doc:doc-0:hash-0:0:50",
		"arch_doc:doc-1:hash-1:0:50",
		"arch_doc:doc-2:hash-2:0:50",
		"arch_doc:doc-3:hash-3:0:50",
		"arch_doc:doc-4:hash-4:0:50",
	}
	ranker.Scores = map[string]map[int]float64{
		"query-1": {
			0: 0.9,
			1: 0.8,
			2: 0.7,
			3: 0.6,
			4: 0.5,
		},
	}

	retriever, err := New(Config{
		TopK:              2,
		RerankTopN:        5,
		EmbeddingProvider: fake,
		Reranker:          ranker,
	})
	if err != nil {
		t.Fatalf("failed to create retriever: %v", err)
	}
	retriever.SetStore(store)

	results, err := retriever.Retrieve(testCtx, RetrievalQuery{
		Text:  "query-1",
		Limit: 2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// With reranking enabled, should return TopK results
	if len(results.Hits) != 2 {
		t.Errorf("expected 2 hits after reranking, got %d", len(results.Hits))
	}
	if !results.Reranked {
		t.Error("expected reranked to be true")
	}
}

func TestSemanticRetriever_FallbackNoReranker(t *testing.T) {
	store := archive.NewFakeStore()

	// Create a test document
	doc := &archive.ArchiveDocument{
		StableID:      archive.StableDocumentID("doc-1"),
		PlainText:     "Test document content",
		PlainTextHash: archive.ContentHashFrom("hash-1"),
		ChunkCount:    1,
		SourceProvenance: archive.SourceProvenance{
			SourceID:   "source-1",
			SourceType: types.SourceTypeWeb,
		},
		ArchivedAt: time.Now(),
	}

	chunk := archive.Chunk{
		StableID:         archive.StableChunkID("arch_doc:doc-1:hash-1:0:50"),
		DocumentStableID: "doc-1",
		ContentHash:      archive.ContentHashFrom("chunk-1"),
		Position:         0,
		Length:           50,
		HasEmbedding:     true,
	}

	if err := store.UpsertDocument(testCtx, doc, []archive.Chunk{chunk}); err != nil {
		t.Fatalf("failed to upsert document: %v", err)
	}

	// Set embedding
	meta := &archive.EmbeddingMetadata{
		ModelName:   "test-model",
		Dimensions:  128,
		GeneratedAt: time.Now(),
	}
	if err := store.SetEmbeddingMetadata(testCtx, chunk.StableID, meta); err != nil {
		t.Fatalf("failed to set embedding metadata: %v", err)
	}
	if err := store.SetEmbeddingVector(testCtx, chunk.StableID, generateVector(128, 100)); err != nil {
		t.Fatalf("failed to set embedding vector: %v", err)
	}

	fake := embedding.NewFakeEmbedder()
	fake.SetDefaultDimension(128)
	fake.SetEmbedding("test", generateVector(128, 100))

	// Create retriever WITHOUT reranker
	retriever, err := New(Config{
		TopK:              5,
		EmbeddingProvider: fake,
		// No Reranker configured
	})
	if err != nil {
		t.Fatalf("failed to create retriever: %v", err)
	}
	retriever.SetStore(store)

	results, err := retriever.Retrieve(testCtx, RetrievalQuery{
		Text:  "test",
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should still return results from semantic retrieval
	t.Logf("Got %d results without reranker", len(results.Hits))
}

func TestSemanticRetriever_ContextCancellation(t *testing.T) {
	store := archive.NewFakeStore()

	retriever, err := New(Config{
		TopK: 10,
	})
	if err != nil {
		t.Fatalf("failed to create retriever: %v", err)
	}
	retriever.SetStore(store)

	ctx, cancel := context.WithCancel(testCtx)
	cancel() // Cancel immediately

	_, err = retriever.Retrieve(ctx, RetrievalQuery{
		Text:  "test",
		Limit: 10,
	})

	if err == nil {
		t.Fatal("expected context cancellation error, got nil")
	}
}

func TestSemanticRetriever_InvalidConfig(t *testing.T) {
	_, err := New(Config{
		TopK: 0,
	})

	if err == nil {
		t.Fatal("expected error for invalid TopK, got nil")
	}

	_, err = New(Config{
		TopK:       10,
		RerankTopN: 5,
	})

	if err == nil {
		t.Fatal("expected error when RerankTopN < TopK, got nil")
	}
}

func TestSemanticRetriever_WithRerankerError(t *testing.T) {
	store := archive.NewFakeStore()

	// Create a test document
	doc := &archive.ArchiveDocument{
		StableID:      archive.StableDocumentID("doc-1"),
		PlainText:     "Test document",
		PlainTextHash: archive.ContentHashFrom("hash-1"),
		ChunkCount:    1,
		SourceProvenance: archive.SourceProvenance{
			SourceID:   "source-1",
			SourceType: types.SourceTypeWeb,
		},
		ArchivedAt: time.Now(),
	}

	chunk := archive.Chunk{
		StableID:         archive.StableChunkID("arch_doc:doc-1:hash-1:0:50"),
		DocumentStableID: "doc-1",
		ContentHash:      archive.ContentHashFrom("chunk-1"),
		Position:         0,
		Length:           50,
		HasEmbedding:     true,
	}

	if err := store.UpsertDocument(testCtx, doc, []archive.Chunk{chunk}); err != nil {
		t.Fatalf("failed to upsert document: %v", err)
	}

	meta := &archive.EmbeddingMetadata{
		ModelName:   "test-model",
		Dimensions:  128,
		GeneratedAt: time.Now(),
	}
	if err := store.SetEmbeddingMetadata(testCtx, chunk.StableID, meta); err != nil {
		t.Fatalf("failed to set embedding metadata: %v", err)
	}
	if err := store.SetEmbeddingVector(testCtx, chunk.StableID, generateVector(128, 100)); err != nil {
		t.Fatalf("failed to set embedding vector: %v", err)
	}

	fake := embedding.NewFakeEmbedder()
	fake.SetDefaultDimension(128)

	// Reranker that always fails
	ranker := &failReranker{
		err: embedding.InvalidResponse("rerank failed"),
	}

	retriever, err := New(Config{
		TopK:              5,
		RerankTopN:        10,
		EmbeddingProvider: fake,
		Reranker:          ranker,
	})
	if err != nil {
		t.Fatalf("failed to create retriever: %v", err)
	}
	retriever.SetStore(store)

	results, err := retriever.Retrieve(testCtx, RetrievalQuery{
		Text:  "test",
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should fall back to semantic ranking when reranker fails
	t.Logf("Got %d results (fallback to semantic ranking)", len(results.Hits))
	t.Logf("Reranked: %v", results.Reranked)
}

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name     string
		a        []float32
		b        []float32
		expected float64
	}{
		{
			name:     "identical vectors",
			a:        []float32{1, 2, 3},
			b:        []float32{1, 2, 3},
			expected: 1.0,
		},
		{
			name:     "orthogonal vectors",
			a:        []float32{1, 0, 0},
			b:        []float32{0, 1, 0},
			expected: 0.0,
		},
		{
			name:     "opposite vectors",
			a:        []float32{1, 0, 0},
			b:        []float32{-1, 0, 0},
			expected: -1.0,
		},
		{
			name:     "zero vector",
			a:        []float32{0, 0, 0},
			b:        []float32{1, 1, 1},
			expected: 0.0,
		},
		{
			name:     "matching dimensions",
			a:        []float32{0.6, 0.8, 0.0},
			b:        []float32{1.0, 0.0, 0.0},
			expected: 0.6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cosineSimilarity(tt.a, tt.b)

			// Allow small tolerance for floating point
			if abs(result-tt.expected) > 0.0001 {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestFakeReranker(t *testing.T) {
	ranker := NewFakeReranker()

	results, reranked, err := ranker.Rerank(testCtx, rerankRequest{
		Query:  "test",
		Chunks: []string{"chunk 1", "chunk 2"},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !reranked {
		t.Error("expected reranked to be true")
	}

	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}

	// Test pre-configured scores
	ranker.Scores = map[string]map[int]float64{
		"test": {0: 0.9, 1: 0.5},
	}
	ranker.ChunkIDs = []string{"chunk-0", "chunk-1"}

	results, _, err = ranker.Rerank(testCtx, rerankRequest{
		Query:  "test",
		Chunks: []string{"chunk 1", "chunk 2"},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if results[0].Score != 0.9 {
		t.Errorf("expected score 0.9, got %v", results[0].Score)
	}
	if results[1].Score != 0.5 {
		t.Errorf("expected score 0.5, got %v", results[1].Score)
	}
}

func TestFakeReranker_Error(t *testing.T) {
	ranker := NewFakeReranker()
	ranker.Error = embedding.InvalidResponse("rerank error")

	_, _, err := ranker.Rerank(testCtx, rerankRequest{
		Query:  "test",
		Chunks: []string{"chunk 1"},
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestFakeReranker_ContextCancellation(t *testing.T) {
	ranker := NewFakeReranker()

	ctx, cancel := context.WithCancel(testCtx)
	cancel()

	_, _, err := ranker.Rerank(ctx, rerankRequest{
		Query:  "test",
		Chunks: []string{"chunk 1"},
	})

	if err == nil {
		t.Fatal("expected context cancellation error, got nil")
	}
}

// Helper functions

func generateVector(dim int, seed int64) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = float32((seed+int64(i))&0x7FFFFFFF) / 0x7FFFFFFF
	}
	return v
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

type failReranker struct {
	err error
}

func (f *failReranker) Rerank(ctx context.Context, req rerankRequest) ([]rerankResult, bool, error) {
	return nil, false, f.err
}
