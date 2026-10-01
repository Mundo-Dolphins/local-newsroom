// Package llm provides a minimal abstraction for local LLM chat completion.
//
// This package defines contracts only. It does not implement any network
// communication or make HTTP calls. Implementations should be injected
// via dependency injection.
//
// The Client interface supports:
//   - Context cancellation via context.Context
//   - System and user prompts
//   - Model selection
//   - Temperature control
//   - Optional maximum output tokens
//   - Response text content
//   - Error propagation for failures
//
// Example usage:
//
//	type fakeClient struct{ ... }
//	func (f *fakeClient) Complete(ctx context.Context, req llm.Request) (llm.Response, error) { ... }
//	client := &fakeClient{}
//	resp, err := client.Complete(ctx, llm.Request{
//		SystemPrompt: "You are helpful",
//		UserPrompt:   "Hello",
//		Model:        "test-model",
//		Temperature:  0.7,
//	})
package llm

import "context"

// Client is the interface for chat completion with an LLM.
//
// Implementations may be real providers (e.g., oMLX, OpenAI) or fakes for testing.
// The interface must be satisfied by injecting concrete implementations.
type Client interface {
	// Complete performs a chat completion request.
	//
	// It returns the response content or an error if the request fails.
	// The context can be used to cancel the request asynchronously.
	Complete(ctx context.Context, req Request) (Response, error)
}

// Request represents a chat completion request.
//
// All fields are provider-specific; only SystemPrompt, UserPrompt, Model,
// Temperature, and MaxOutputTokens are used by the core API.
type Request struct {
	// SystemPrompt is the system-level instruction for the model.
	SystemPrompt string

	// UserPrompt is the user's input message.
	UserPrompt string

	// Model is the name of the model to use.
	// The interpretation is provider-specific.
	Model string

	// Temperature controls randomness in generation (0.0 to 1.0+).
	// Lower values are more deterministic; higher values are more creative.
	Temperature float64

	// MaxOutputTokens is the maximum number of tokens to generate.
	// Zero means no limit (provider-specific default applies).
	MaxOutputTokens int
}

// Response represents a chat completion response.
//
// Only the Content field is used by callers. Additional metadata
// (e.g., token counts, finish reason) should remain private to providers.
type Response struct {
	// Content is the generated text from the model.
	Content string
}

// Error is a typed error for LLM-specific failures.
//
// It provides structured error information that can be inspected by callers.
type Error struct {
	// Code is a machine-readable error code.
	// Common codes:
	//   - ContextCanceled: context was canceled before completion
	//   - RateLimited: rate limit exceeded
	//   - Internal: provider-side internal error
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

// Internal returns an error indicating an internal provider error.
func Internal(msg string) Error {
	return Error{Code: "Internal", Message: msg}
}
