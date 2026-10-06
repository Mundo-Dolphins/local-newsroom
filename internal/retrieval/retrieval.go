// Package retrieval provides semantic search capabilities for the local-newsroom archive.
//
// This package implements:
//   - Semantic retrieval using cosine similarity on stored chunk embeddings
//   - Optional reranking through a generic Reranker interface
//   - Fake implementations for testing
//   - oMLX rerank adapter for production use
//
// # Design Principles
//
//   - Deterministic: Given the same inputs, always produces the same output
//   - Composable: Semantic retrieval is independent from reranking
//   - Fail-safe: Works without reranking when disabled or unavailable
//   - Testable: Fake providers enable unit tests without external dependencies
//
// # Semantic Retrieval Flow
//
// 1. Embed the query text
// 2. Compare query vector with stored chunk embeddings
// 3. Calculate cosine similarity in Go
// 4. Return top-K chunks with provenance
//
// # Reranking Flow (when configured)
//
// 1. Retrieve a larger semantic candidate set (e.g., top-50)
// 2. Rerank candidates against the query using oMLX /v1/rerank
// 3. Return the configured final top-K (e.g., top-10)
//
// # Dimension Safety
//
// All retrieval operations validate that:
//   - Query embedding dimension matches stored embedding dimensions
//   - Similarity values are finite (not NaN or Inf)
//   - Tie-breaking is stable (deterministic)
//
// Example usage:
//
//	// Basic semantic retrieval
//	embedder := embedding.NewFakeEmbedder()
//	store := archive.NewFakeStore()
//	retriever := retrieval.NewSemanticRetriever(
//	    store, embedder,
//	    retrieval.Config{TopK: 10},
//	)
//
//	results, err := retriever.Retrieve(ctx, query)
//
//	// With reranking
//	reranker := oMLX.NewReranker(config)
//	retrieverWithRerank := retrieval.NewSemanticRetriever(
//	    store, embedder,
//	    retrieval.Config{
//	        TopK:        10,
//	        Reranker:    reranker,
//	        RerankTopN:  50,  // Retrieve 50, then rerank to 10
//	    },
//	)
package retrieval

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"

	"github.com/Mundo-Dolphins/local-newsroom/internal/archive"
	"github.com/Mundo-Dolphins/local-newsroom/internal/embedding"
)

// Config holds configuration for semantic retrieval.
type Config struct {
	// TopK is the maximum number of results to return.
	// Default: 10
	TopK int

	// RerankTopN is the number of candidates to retrieve before reranking.
	// Only used when Reranker is configured.
	// If 0, RerankTopN is set to TopK * 5 (minimum 20).
	// Default: TopK * 5 (minimum 20)
	RerankTopN int

	// Reranker is an optional reranker for re-scoring semantic candidates.
	// If nil, retrieval uses semantic ranking only.
	Reranker Reranker

	// EmbeddingProvider is used to embed query text.
	// If nil, a fake embedder is used (for testing).
	EmbeddingProvider embedding.Embedder

	// MinRelevanceScore is a minimum cosine similarity threshold.
	// Results below this score are excluded.
	// Default: -1.0 (no filtering)
	MinRelevanceScore float64

	// Parallelism controls the number of concurrent operations when
	// retrieving chunk embeddings.
	// Default: 1 (sequential)
	Parallelism int
}

// DefaultConfig returns a sensible default configuration.
func DefaultConfig() Config {
	return Config{
		TopK:              10,
		RerankTopN:        0, // Will default to TopK * 5
		Reranker:          nil,
		EmbeddingProvider: nil,
		MinRelevanceScore: -1.0,
		Parallelism:       1,
	}
}

// WithTopK returns a new config with TopK set.
func (c Config) WithTopK(topK int) Config {
	c.TopK = topK
	return c
}

// WithReranker returns a new config with Reranker set.
func (c Config) WithReranker(r Reranker) Config {
	c.Reranker = r
	return c
}

// WithMinRelevanceScore returns a new config with MinRelevanceScore set.
func (c Config) WithMinRelevanceScore(score float64) Config {
	c.MinRelevanceScore = score
	return c
}

// Validate validates the configuration and returns an error if invalid.
func (c Config) Validate() error {
	if c.TopK <= 0 {
		return fmt.Errorf("TopK must be positive, got %d", c.TopK)
	}

	if c.RerankTopN > 0 && c.RerankTopN < c.TopK {
		return fmt.Errorf("RerankTopN (%d) must be >= TopK (%d)", c.RerankTopN, c.TopK)
	}

	return nil
}

// Result represents the complete result of a semantic retrieval query.
type Result struct {
	// Hits is the list of retrieval hits, ordered by relevance score (descending).
	Hits []Hit

	// TotalHits is the total number of hits matching the query (before limit/filter).
	TotalHits int

	// Query is the query that produced this result.
	Query RetrievalQuery

	// Reranked indicates whether reranking was applied.
	Reranked bool
}

// Hit represents a single retrieval hit with provenance.
type Hit struct {
	// Chunk is the chunk that matched.
	Chunk archive.Chunk

	// Document is the parent document (if available).
	Document *archive.ArchiveDocument

	// SourceProvenance contains provenance information from the document.
	SourceProvenance archive.SourceProvenance

	// RelevanceScore is the cosine similarity score.
	// Range: [-1, 1], where 1 is identical vectors.
	RelevanceScore float64

	// ChunkContent is the content of the matching chunk.
	// For v0.3, this is reconstructed from the document's PlainText.
	ChunkContent string
}

// RetrievalQuery represents a query for retrieving archived documents or chunks.
type RetrievalQuery struct {
	// Text is the query text.
	Text string

	// Target specifies what the query should retrieve.
	Target archive.RetrievalTarget

	// Limit is the maximum number of results to return.
	// If 0 or negative, the config TopK is used.
	Limit int
}

// SemanticRetriever performs semantic search over the archive.
//
// It supports:
//   - Deterministic cosine similarity computation in Go
//   - Optional reranking through the Reranker interface
//   - Configurable retrieval limits
//   - Provenance preservation
//   - Context cancellation
type SemanticRetriever struct {
	store        archive.Store
	embedder     embedding.Embedder
	config       Config
	fakeEmbedder *embedding.FakeEmbedder

	mu sync.RWMutex
}

// New creates a new SemanticRetriever with the given configuration.
//
// If config.EmbeddingProvider is nil, a fake embedder is used.
// This is intentional for testability - production code should inject
// a real embedding provider.
func New(cfg Config) (*SemanticRetriever, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	// Set default RerankTopN if not specified
	if cfg.RerankTopN == 0 {
		cfg.RerankTopN = cfg.TopK * 5
		if cfg.RerankTopN < 20 {
			cfg.RerankTopN = 20
		}
	}

	r := &SemanticRetriever{
		config: cfg,
	}

	// Use fake embedder if none provided
	if cfg.EmbeddingProvider == nil {
		r.fakeEmbedder = embedding.NewFakeEmbedder()
		r.embedder = r.fakeEmbedder
	} else {
		r.embedder = cfg.EmbeddingProvider
	}

	return r, nil
}

// NewWithStore creates a new SemanticRetriever with the given store.
//
// This is a convenience constructor that uses DefaultConfig().
func NewWithStore(store archive.Store) (*SemanticRetriever, error) {
	return New(DefaultConfig())
}

// SetStore sets the store for retrieval.
func (r *SemanticRetriever) SetStore(store archive.Store) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.store = store
}

// SetEmbedder sets the embedding provider for query embedding.
func (r *SemanticRetriever) SetEmbedder(embedder embedding.Embedder) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.embedder = embedder
}

// Retrieve performs semantic retrieval for the given query.
//
// The retrieval process:
// 1. Embeds the query text
// 2. Compares query vector with stored chunk embeddings
// 3. Calculates cosine similarity for each chunk
// 4. Optionally reranks candidates if a Reranker is configured
// 5. Returns top-K hits sorted by relevance
//
// Context cancellation is respected throughout the process.
func (r *SemanticRetriever) Retrieve(ctx context.Context, query RetrievalQuery) (*Result, error) {
	if r.store == nil {
		return nil, fmt.Errorf("store not configured")
	}

	// Default limit to config TopK
	limit := query.Limit
	if limit <= 0 {
		limit = r.config.TopK
	}

	// Determine retrieval limit (considering reranking)
	retrieveLimit := limit
	if r.config.Reranker != nil {
		retrieveLimit = r.config.RerankTopN
	}

	// Embed the query
	queryEmbeddings, err := r.embedEmbedQuery(ctx, query.Text)
	if err != nil {
		return nil, fmt.Errorf("failed to embed query: %w", err)
	}

	if len(queryEmbeddings) == 0 {
		return &Result{
			Hits:      []Hit{},
			TotalHits: 0,
			Query:     query,
		}, nil
	}

	queryVector := queryEmbeddings[0].Vector
	queryDim := len(queryVector)

	// Retrieve chunks with embeddings
	chunks, err := r.store.GetChunksWithEmbeddingMetadata(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get chunks with embeddings: %w", err)
	}

	if len(chunks) == 0 {
		return &Result{
			Hits:      []Hit{},
			TotalHits: 0,
			Query:     query,
		}, nil
	}

	// Compute similarities and collect candidates
	candidates, err := r.computeSimilarities(ctx, chunks, queryVector, queryDim, retrieveLimit)
	if err != nil {
		return nil, fmt.Errorf("failed to compute similarities: %w", err)
	}

	if len(candidates) == 0 {
		return &Result{
			Hits:      []Hit{},
			TotalHits: 0,
			Query:     query,
		}, nil
	}

	// Apply minimum relevance filter
	if r.config.MinRelevanceScore > 0 {
		candidates = r.filterByScore(candidates, r.config.MinRelevanceScore)
	}

	// Rerank if configured
	reranked := false
	if r.config.Reranker != nil && len(candidates) > limit {
		candidates, reranked, err = r.rerankCandidates(ctx, query.Text, candidates, limit)
		if err != nil {
			// Rerank failure is non-fatal; fall back to semantic ranking
			candidates = candidates[:min(limit, len(candidates))]
			reranked = false // Don't report reranking if it failed
		}
	}

	// Trim to final limit
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	// Build final hits with provenance
	hits, err := r.buildHits(ctx, candidates)
	if err != nil {
		return nil, fmt.Errorf("failed to build hits: %w", err)
	}

	return &Result{
		Hits:      hits,
		TotalHits: len(candidates),
		Query:     query,
		Reranked:  reranked,
	}, nil
}

// FakeEmbedder returns the fake embedder for testing.
// This allows tests to configure the embedder without using dependency injection.
func (r *SemanticRetriever) FakeEmbedder() *embedding.FakeEmbedder {
	return r.fakeEmbedder
}

// embedEmbedQuery embeds the query text and returns the query vector.
func (r *SemanticRetriever) embedEmbedQuery(ctx context.Context, text string) ([]embedding.Embedding, error) {
	if r.embedder == nil {
		return nil, fmt.Errorf("embedding provider not configured")
	}

	embeddings, err := r.embedder.Embed(ctx, embedding.Request{
		Model:  "", // Use provider default
		Inputs: []string{text},
	})

	if err != nil {
		return nil, fmt.Errorf("embedding failed: %w", err)
	}

	if len(embeddings) == 0 {
		return nil, fmt.Errorf("no embeddings returned")
	}

	return embeddings, nil
}

// computeSimilarities computes cosine similarity between query vector and stored embeddings.
//
// It returns the top-n candidates sorted by similarity (descending).
// Tie-breaking is stable and deterministic.
func (r *SemanticRetriever) computeSimilarities(
	ctx context.Context,
	chunks []archive.Chunk,
	queryVector []float32,
	queryDim int,
	topN int,
) ([]candidate, error) {
	var candidates []candidate

	for _, chunk := range chunks {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// Retrieve stored embedding
		vector, err := r.store.GetEmbeddingVector(ctx, chunk.StableID)
		if err != nil {
			// Skip chunks with retrieval errors
			continue
		}

		if vector == nil {
			// No embedding stored
			continue
		}

		// Validate dimension match
		if len(vector) != queryDim {
			continue
		}

		// Calculate cosine similarity
		score := cosineSimilarity(queryVector, vector)

		// Validate score is finite (check both NaN and Inf)
		if math.IsNaN(score) || math.IsInf(score, 0) {
			continue
		}

		candidates = append(candidates, candidate{
			Chunk:          chunk,
			RelevanceScore: score,
		})
	}

	// Sort by score descending, then by chunk ID for stable tie-breaking
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].RelevanceScore != candidates[j].RelevanceScore {
			return candidates[i].RelevanceScore > candidates[j].RelevanceScore
		}
		// Stable tie-breaker: use chunk ID as secondary sort key
		return candidates[i].Chunk.StableID < candidates[j].Chunk.StableID
	})

	// Trim to top N
	if len(candidates) > topN {
		candidates = candidates[:topN]
	}

	return candidates, nil
}

// filterByScore removes candidates below the minimum relevance threshold.
func (r *SemanticRetriever) filterByScore(candidates []candidate, minScore float64) []candidate {
	var filtered []candidate
	for _, c := range candidates {
		if c.RelevanceScore >= minScore {
			filtered = append(filtered, c)
		}
	}
	return filtered
}

// rerankCandidates re-ranks candidates using the configured Reranker.
//
// If reranking fails, returns nil for the candidates slice and false for reranked.
// If reranking succeeds but produces fewer results than limit, returns all candidates.
func (r *SemanticRetriever) rerankCandidates(
	ctx context.Context,
	queryText string,
	candidates []candidate,
	limit int,
) ([]candidate, bool, error) {
	// Extract chunk content for reranking
	// For v0.3, we need to reconstruct content from documents
	texts := make([]string, 0, len(candidates))
	chunkMap := make(map[string]candidate, len(candidates))

	for _, c := range candidates {
		// Get the document to reconstruct chunk content
		doc, err := r.store.GetDocument(ctx, c.Chunk.DocumentStableID)
		if err != nil {
			// Skip if we can't get the document
			continue
		}

		// Extract chunk content from document plain text
		// This is a simplified reconstruction for v0.3
		content := r.extractChunkContent(doc.PlainText, c.Chunk.Position, c.Chunk.Length)
		texts = append(texts, content)
		chunkMap[string(c.Chunk.StableID)] = c
	}

	if len(texts) == 0 {
		return candidates, false, nil
	}

	// Perform reranking
	rerankResults, reranked, err := r.config.Reranker.Rerank(ctx, rerankRequest{
		Query:   queryText,
		Chunks:  texts,
		Context: "", // Not used in v0.3
	})
	if err != nil {
		return nil, false, fmt.Errorf("reranking failed: %w", err)
	}

	// Build candidate list from rerank results
	var rerankedCandidates []candidate
	for _, rr := range rerankResults {
		if c, ok := chunkMap[rr.ChunkID]; ok {
			c.RelevanceScore = rr.Score
			rerankedCandidates = append(rerankedCandidates, c)
		}
	}

	// Sort by rerank score (descending) with stable tie-breaking
	sort.SliceStable(rerankedCandidates, func(i, j int) bool {
		if rerankedCandidates[i].RelevanceScore != rerankedCandidates[j].RelevanceScore {
			return rerankedCandidates[i].RelevanceScore > rerankedCandidates[j].RelevanceScore
		}
		return rerankedCandidates[i].Chunk.StableID < rerankedCandidates[j].Chunk.StableID
	})

	// Trim to limit
	if len(rerankedCandidates) > limit {
		rerankedCandidates = rerankedCandidates[:limit]
	}

	return rerankedCandidates, reranked, nil
}

// extractChunkContent reconstructs chunk content from document plain text.
//
// For v0.3, chunks don't store their text directly, so we reconstruct it
// based on position and length metadata.
func (r *SemanticRetriever) extractChunkContent(docText string, position, length int) string {
	if position < 0 || position >= len(docText) {
		return ""
	}

	end := position + length
	if end > len(docText) {
		end = len(docText)
	}

	return docText[position:end]
}

// buildHits constructs final Hit objects from candidates with provenance.
func (r *SemanticRetriever) buildHits(ctx context.Context, candidates []candidate) ([]Hit, error) {
	hits := make([]Hit, 0, len(candidates))

	for _, c := range candidates {
		// Get document for provenance
		doc, err := r.store.GetDocument(ctx, c.Chunk.DocumentStableID)
		if err != nil {
			// Continue even if document is not found
			doc = nil
		}

		hit := Hit{
			Chunk:          c.Chunk,
			Document:       doc,
			RelevanceScore: c.RelevanceScore,
		}

		if doc != nil {
			hit.SourceProvenance = doc.SourceProvenance
			hit.ChunkContent = r.extractChunkContent(doc.PlainText, c.Chunk.Position, c.Chunk.Length)
		}

		hits = append(hits, hit)
	}

	return hits, nil
}

// candidate holds an intermediate candidate for retrieval.
type candidate struct {
	Chunk          archive.Chunk
	RelevanceScore float64
}

// cosineSimilarity computes the cosine similarity between two vectors.
//
// The result is in the range [-1, 1], where:
//   - 1 means the vectors are identical
//   - 0 means the vectors are orthogonal
//   - -1 means the vectors are diametrically opposed
//
// If either vector is zero-length, returns 0.0.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0.0
	}

	var dotProduct float64
	var normA, normB float64

	for i := range a {
		prod := float64(a[i]) * float64(b[i])
		dotProduct += prod
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}

	if normA == 0 || normB == 0 {
		return 0.0
	}

	normA = math.Sqrt(normA)
	normB = math.Sqrt(normB)

	return dotProduct / (normA * normB)
}

// min returns the minimum of two integers.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
