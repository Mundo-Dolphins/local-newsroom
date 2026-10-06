// Package retrieval defines the Reranker abstraction and implementations.
//
// This file defines the Reranker interface that enables optional reranking
// of semantic retrieval candidates. Implementations can be:
//   - FakeReranker for testing
//   - oMLXReranker for production oMLX /v1/rerank
//   - Custom implementations (e.g., BM25, cross-encoder)
//
// # Usage Pattern
//
// The typical usage pattern is:
//
//	// Create reranker
//	reranker := retrieval.NewReranker(retrieval.RerankerConfig{
//	    BaseURL: "http://localhost:8000/v1",
//	    Model:   "bge-reranker",
//	})
//
//	// Create semantic retriever with reranking
//	retriever := retrieval.NewSemanticRetriever(retrieval.Config{
//	    TopK:      10,
//	    RerankTopN: 50,  // Retrieve 50, then rerank to 10
//	    Reranker:  reranker,
//	})
//
//	results, err := retriever.Retrieve(ctx, query)
//
// # Error Handling
//
// Rerankers should return typed errors:
//   - RateLimited: when rate limited
//   - InvalidResponse: when response is malformed
//   - Internal: for other errors
//
// The semantic retriever treats rerank failures as non-fatal - it falls
// back to semantic ranking if reranking is unavailable.
package retrieval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/embedding"
)

// Reranker is the interface for reranking retrieval candidates.
//
// Rerankers take a query and a list of text chunks, and return
// re-ranked results with relevance scores. The reranking is typically
// more accurate than semantic similarity but slower.
//
// Implementations may use:
//   - Cross-encoder models (e.g., BGE-Reranker)
//   - Dense retrieval models
//   - Lexical matching (BM25)
//   - Hybrid approaches
//
// # Contract
//
// The Reranker interface guarantees:
//   - Context cancellation is respected
//   - Input/output slice lengths match (one score per chunk)
//   - Scores are in range [0, 1] (higher is better)
//   - Invalid responses return typed errors
//
// # Fail-Safe Behavior
//
// The semantic retriever treats rerank failures as non-fatal:
//   - If Rerank() returns an error, falls back to semantic ranking
//   - The Result.Reranked flag indicates whether reranking was applied
//   - Retrieval continues even if reranking is disabled/unavailable
type Reranker interface {
	// Rerank re-scores the given chunks against the query.
	//
	// Parameters:
	//   - ctx: context for cancellation and timeouts
	//   - req: rerank request containing query, chunks, and context
	//
	// Returns:
	//   - Results: list of rerank results, one per input chunk
	//   - bool: true if reranking was successful
	//   - error: error if reranking failed
	//
	// Error types:
	//   - *embedding.Error: typed errors for RateLimited, InvalidResponse, Internal
	//   - context.DeadlineExceeded: if context deadline exceeded
	//   - context.Canceled: if context was canceled
	//
	// The Results slice has the same length as the Input.Chunks slice.
	Rerank(ctx context.Context, req rerankRequest) ([]rerankResult, bool, error)
}

// rerankRequest represents a rerank request.
type rerankRequest struct {
	// Query is the search query text.
	Query string `json:"query"`

	// Chunks is the list of text chunks to rerank.
	Chunks []string `json:"chunks"`

	// Context is optional additional context for reranking.
	// This could be user history, conversation context, etc.
	Context string `json:"context,omitempty"`
}

// rerankResult represents a single rerank result.
type rerankResult struct {
	// ChunkID is the stable chunk ID (for mapping back to original chunk).
	ChunkID string `json:"chunk_id"`

	// Score is the rerank score (0 to 1, higher is better).
	Score float64 `json:"score"`

	// Index is the original position in the input list.
	// Used for maintaining order when mapping results back.
	Index int `json:"index"`
}

// FakeReranker is a fake Reranker implementation for testing.
//
// It assigns deterministic scores based on text similarity to the query.
// This allows testing reranking logic without external dependencies.
type FakeReranker struct {
	// Scores can be pre-configured for specific queries and indices.
	// Format: query -> map[chunkIndex] -> score
	Scores map[string]map[int]float64

	// DefaultScore is the score to use when no pre-configured score exists.
	// Default: 0.5
	DefaultScore float64

	// Error is an optional error to return for all requests.
	Error error

	// ChunkIDs allows specifying custom chunk IDs for rerank results.
	// If set, these IDs are used instead of generating "chunk-N" IDs.
	ChunkIDs []string
}

// NewFakeReranker creates a new FakeReranker.
func NewFakeReranker() *FakeReranker {
	return &FakeReranker{
		Scores:       make(map[string]map[int]float64),
		DefaultScore: 0.5,
	}
}

// Rerank implements Reranker.Rerank.
func (r *FakeReranker) Rerank(ctx context.Context, req rerankRequest) ([]rerankResult, bool, error) {
	// Check for configured error
	if r.Error != nil {
		return nil, false, r.Error
	}

	// Check context cancellation
	select {
	case <-ctx.Done():
		return nil, false, ctx.Err()
	default:
	}

	// Determine chunk IDs to use
	chunkIDs := r.ChunkIDs
	if len(chunkIDs) == 0 {
		chunkIDs = make([]string, len(req.Chunks))
		for i := range chunkIDs {
			chunkIDs[i] = fmt.Sprintf("chunk-%d", i)
		}
	}

	// Build results
	results := make([]rerankResult, 0, len(req.Chunks))
	for i, chunk := range req.Chunks {
		_ = chunk // unused but needed for context
		score := r.DefaultScore

		// Check for pre-configured score
		if queryScores, ok := r.Scores[req.Query]; ok {
			if s, ok := queryScores[i]; ok {
				score = s
			}
		}

		// Ensure score is in valid range
		if score < 0 {
			score = 0
		}
		if score > 1 {
			score = 1
		}

		results = append(results, rerankResult{
			ChunkID: chunkIDs[i],
			Score:   score,
			Index:   i,
		})
	}

	return results, true, nil
}

// SetScore sets a pre-configured score for a specific query and chunk index.
func (r *FakeReranker) SetScore(query string, chunkIndex int, score float64) {
	if r.Scores[query] == nil {
		r.Scores[query] = make(map[int]float64)
	}
	r.Scores[query][chunkIndex] = score
}

// oMLXReranker is an oMLX reranker implementation using /v1/rerank.
//
// The oMLX reranking endpoint follows the OpenAI-compatible format:
//
//	POST /v1/rerank
//	{
//	    "model": "bge-reranker",
//	    "query": "search query",
//	    "documents": ["chunk 1", "chunk 2", ...]
//	}
//
// Response:
//
//	{
//	    "results": [
//	        {"index": 0, "relevance_score": 0.95, "id": "chunk-0"},
//	        {"index": 1, "relevance_score": 0.87, "id": "chunk-1"},
//	    ]
//	}
type oMLXReranker struct {
	config   RerankerConfig
	client   *http.Client
	endpoint string
}

// RerankerConfig holds configuration for oMLX reranker.
type RerankerConfig struct {
	// BaseURL is the base URL of the oMLX API.
	BaseURL string

	// Model is the reranker model to use.
	Model string

	// Timeout is the maximum duration for a single request.
	Timeout time.Duration

	// APIKey is an optional API key for authentication.
	APIKey string
}

// NewRerankerConfig returns a new RerankerConfig with default values.
func NewRerankerConfig() RerankerConfig {
	return RerankerConfig{
		BaseURL: "http://localhost:8000/v1",
		Model:   "bge-reranker",
		Timeout: 30 * time.Second,
	}
}

// WithBaseURL returns a new config with BaseURL set.
func (c RerankerConfig) WithBaseURL(url string) RerankerConfig {
	c.BaseURL = url
	return c
}

// WithModel returns a new config with Model set.
func (c RerankerConfig) WithModel(model string) RerankerConfig {
	c.Model = model
	return c
}

// WithTimeout returns a new config with Timeout set.
func (c RerankerConfig) WithTimeout(t time.Duration) RerankerConfig {
	c.Timeout = t
	return c
}

// WithAPIKey returns a new config with APIKey set.
func (c RerankerConfig) WithAPIKey(key string) RerankerConfig {
	c.APIKey = key
	return c
}

// NewReranker creates a new oMLX reranker with the given configuration.
func NewReranker(cfg RerankerConfig) (Reranker, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}

	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}

	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultTimeout
	}

	return &oMLXReranker{
		config:   cfg,
		client:   &http.Client{Timeout: cfg.Timeout},
		endpoint: cfg.BaseURL + "/rerank",
	}, nil
}

// DefaultBaseURL is the default oMLX API base URL.
const DefaultBaseURL = "http://localhost:8000/v1"

// DefaultModel is the default reranker model.
const DefaultModel = "bge-reranker"

// DefaultTimeout is the default request timeout.
const DefaultTimeout = 30 * time.Second

// Rerank implements Reranker.Rerank for oMLXReranker.
func (r *oMLXReranker) Rerank(ctx context.Context, req rerankRequest) ([]rerankResult, bool, error) {
	// Prepare request body
	rerankReq := oMLXRerankRequest{
		Model:     r.config.Model,
		Query:     req.Query,
		Documents: req.Chunks,
	}

	body, err := json.Marshal(rerankReq)
	if err != nil {
		return nil, false, embedding.Internal("failed to marshal request: " + err.Error())
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, false, embedding.Internal("failed to create request: " + err.Error())
	}

	// Set headers
	httpReq.Header.Set("Content-Type", "application/json")
	if r.config.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+r.config.APIKey)
	}

	// Execute request
	resp, err := r.client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, embedding.ContextCanceled("request canceled: " + ctx.Err().Error())
		}
		return nil, false, embedding.Internal("connection failed: " + err.Error())
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			// Log but don't fail on close error
			log.Printf("warning: failed to close response body: %v", cerr)
		}
	}()

	// Handle non-2xx responses
	if resp.StatusCode != http.StatusOK {
		return nil, false, r.handleHTTPError(resp)
	}

	// Read and parse response
	var rerankResp oMLXRerankResponse
	if err := json.NewDecoder(resp.Body).Decode(&rerankResp); err != nil {
		return nil, false, embedding.InvalidResponse("failed to parse response: " + err.Error())
	}

	// Build results
	results := make([]rerankResult, 0, len(req.Chunks))
	for _, result := range rerankResp.Results {
		results = append(results, rerankResult{
			ChunkID: result.ID,
			Score:   result.RelevanceScore,
			Index:   result.Index,
		})
	}

	return results, true, nil
}

// handleHTTPError parses the HTTP error response and returns an appropriate error.
func (r *oMLXReranker) handleHTTPError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)

	// Try to parse provider-specific error
	var apiErr struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &apiErr) == nil && apiErr.Error.Message != "" {
		msg := apiErr.Error.Message
		switch resp.StatusCode {
		case http.StatusTooManyRequests:
			return embedding.RateLimited(msg)
		case http.StatusInternalServerError:
			return embedding.Internal("provider error: " + msg)
		case http.StatusUnauthorized:
			return embedding.Internal("unauthorized: " + msg)
		case http.StatusNotFound:
			return embedding.Internal("endpoint not found: " + msg)
		default:
			return embedding.Internal(fmt.Sprintf("provider error (%d): %s", resp.StatusCode, msg))
		}
	}

	// Fallback to generic errors based on status code
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		return embedding.RateLimited("rate limit exceeded")
	case http.StatusInternalServerError:
		return embedding.Internal("internal server error")
	case http.StatusUnauthorized:
		return embedding.Internal("unauthorized")
	case http.StatusNotFound:
		return embedding.Internal("endpoint not found")
	default:
		errMsg := fmt.Sprintf("HTTP %d", resp.StatusCode)
		if len(body) > 0 {
			errMsg += ": " + string(body)
		}
		return embedding.Internal(errMsg)
	}
}

// oMLXRerankRequest represents the request body for /rerank.
type oMLXRerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
}

// oMLXRerankResult represents a single result in the /rerank response.
type oMLXRerankResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
	ID             string  `json:"id,omitempty"`
}

// oMLXRerankResponse represents the /rerank response.
type oMLXRerankResponse struct {
	Object  string             `json:"object"`
	Model   string             `json:"model"`
	Results []oMLXRerankResult `json:"results"`
}
