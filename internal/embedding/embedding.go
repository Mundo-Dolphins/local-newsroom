// Package embedding provides a minimal abstraction for text embedding.
//
// This package defines contracts only. It does not implement any network
// communication or make HTTP calls. Implementations should be injected
// via dependency injection.
//
// The Embedder interface supports:
//   - Context cancellation via context.Context
//   - Single or batch input texts
//   - Model selection
//   - Dimension specification when supported
//   - Error propagation for failures
//
// Example usage:
//
//	type fakeEmbedder struct{ ... }
//	func (f *fakeEmbedder) Embed(ctx context.Context, req Request) ([]Embedding, error) { ... }
//	embedder := &fakeEmbedder{}
//	embeddings, err := embedder.Embed(ctx, embedding.Request{
//		Model:   "test-model",
//		Inputs:  []string{"Hello", "World"},
//	})
package embedding

import (
	"context"
	"fmt"
	"math"
)

// Embedder is the interface for generating embeddings from text.
//
// Implementations may be real providers (e.g., oMLX, OpenAI) or fakes for testing.
// The interface must be satisfied by injecting concrete implementations.
type Embedder interface {
	// Embed generates embeddings for the given input texts.
	//
	// It returns a slice of Embedding results, one for each input.
	// The context can be used to cancel the request asynchronously.
	//
	// Errors are returned for:
	//   - Context cancellation or timeout
	//   - Network failures
	//   - Invalid responses (wrong count, empty vectors, etc.)
	//   - Non-finite vector values
	//
	// The returned slice length equals the input slice length.
	// Each Embedding.Vector is a float32 slice.
	Embed(ctx context.Context, req Request) ([]Embedding, error)
}

// Request represents an embedding request.
type Request struct {
	// Model is the name of the embedding model to use.
	// The interpretation is provider-specific.
	Model string

	// Inputs is the list of texts to embed.
	// Must not be empty.
	Inputs []string

	// Dimensions is an optional dimension specification.
	// Set to a positive value when the provider supports explicit dimension control.
	// Zero or unset means use the provider's default.
	Dimensions int
}

// Embedding represents an embedding vector with metadata.
type Embedding struct {
	// Index is the position of this embedding in the original input list.
	// Zero-based index.
	Index int

	// Vector is the embedding vector as float32 values.
	// Never nil or empty.
	Vector []float32

	// ModelUsed is the model name that generated this embedding.
	// May be empty if the provider doesn't return it.
	ModelUsed string
}

// ValidateVector validates an embedding vector and returns an error if invalid.
//
// Checks performed:
//   - Vector is not nil
//   - Vector is not empty
//   - All values are finite (not NaN or Inf)
//   - Consistent dimension across vectors
func ValidateVector(vector []float32, index int) error {
	if vector == nil {
		return fmt.Errorf("embedding[%d]: vector is nil", index)
	}
	if len(vector) == 0 {
		return fmt.Errorf("embedding[%d]: vector is empty", index)
	}
	for i, v := range vector {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return fmt.Errorf("embedding[%d][%d]: non-finite value %v", index, i, v)
		}
	}
	return nil
}

// ValidateBatch validates a batch of embeddings.
//
// Checks that:
//   - All embeddings have valid vectors
//   - All embeddings have the same dimension
//   - Embedding indices match their position in the batch
func ValidateBatch(embeddings []Embedding) error {
	if len(embeddings) == 0 {
		return nil
	}

	// Check all vectors are valid and dimensions match
	expectedDim := -1
	for i, emb := range embeddings {
		// Check index
		if emb.Index != i {
			return fmt.Errorf("embedding[%d]: index mismatch, expected %d", i, i)
		}

		// Check vector validity
		if err := ValidateVector(emb.Vector, i); err != nil {
			return err
		}

		// Check dimension consistency
		if expectedDim == -1 {
			expectedDim = len(emb.Vector)
		} else if len(emb.Vector) != expectedDim {
			return fmt.Errorf("embedding[%d]: dimension mismatch, expected %d, got %d",
				i, expectedDim, len(emb.Vector))
		}
	}

	return nil
}

// Dimension returns the vector dimension of an embedding.
func (e Embedding) Dimension() int {
	return len(e.Vector)
}

// Error is a typed error for embedding-specific failures.
//
// It provides structured error information that can be inspected by callers.
type Error struct {
	// Code is a machine-readable error code.
	// Common codes:
	//   - ContextCanceled: context was canceled before completion
	//   - RateLimited: rate limit exceeded
	//   - Internal: provider-side internal error
	//   - InvalidResponse: response from provider is malformed
	Code string

	// Message is a human-readable description of the error.
	Message string
}

func (e Error) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

// ContextCanceled returns an error indicating the operation was canceled.
func ContextCanceled(msg string) Error {
	return Error{Code: "ContextCanceled", Message: msg}
}

// RateLimited returns an error indicating a rate limit was exceeded.
func RateLimited(msg string) Error {
	return Error{Code: "RateLimited", Message: msg}
}

// InvalidResponse returns an error indicating the provider returned an invalid response.
func InvalidResponse(msg string) Error {
	return Error{Code: "InvalidResponse", Message: msg}
}

// Internal returns an error indicating an internal provider error.
func Internal(msg string) Error {
	return Error{Code: "Internal", Message: msg}
}

// IsRateLimited checks if an error is a rate limit error.
func IsRateLimited(err error) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(Error); ok {
		return e.Code == "RateLimited"
	}
	return false
}

// IsInvalidResponse checks if an error is an invalid response error.
func IsInvalidResponse(err error) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(Error); ok {
		return e.Code == "InvalidResponse"
	}
	return false
}

// IsInternal checks if an error is an internal provider error.
func IsInternal(err error) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(Error); ok {
		return e.Code == "Internal"
	}
	return false
}
