package llm

import (
	"context"
	"errors"
	"testing"
)

// TestClientBehavior demonstrates a fake Client implementation that can be
// injected for unit testing. This proves the abstraction is testable.
func TestClientBehavior(t *testing.T) {
	t.Run("returns content successfully", func(t *testing.T) {
		client := &fakeClient{
			response: Response{Content: "Test response"},
		}

		resp, err := client.Complete(context.Background(), Request{
			Model:       "test-model",
			Temperature: 0.7,
		})

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Content != "Test response" {
			t.Errorf("expected 'Test response', got %q", resp.Content)
		}
	})

	t.Run("returns error on failure", func(t *testing.T) {
		client := &fakeClient{
			err: Internal("provider error"),
		}

		_, err := client.Complete(context.Background(), Request{
			Model: "test-model",
		})

		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("respects context cancellation", func(t *testing.T) {
		client := &fakeClient{
			response: Response{Content: "Will not return"},
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		_, err := client.Complete(ctx, Request{Model: "test-model"})

		if err == nil {
			t.Fatal("expected context cancellation error, got nil")
		}
		if !IsContextCanceled(err) {
			t.Errorf("expected ContextCanceled error, got %T: %v", err, err)
		}
	})

	t.Run("context is canceled within client", func(t *testing.T) {
		client := &fakeClient{
			shouldCancel: true,
		}

		_, err := client.Complete(context.Background(), Request{Model: "test-model"})

		if err == nil {
			t.Fatal("expected context cancellation error, got nil")
		}
		if !IsContextCanceled(err) {
			t.Errorf("expected ContextCanceled error, got %T: %v", err, err)
		}
	})

	t.Run("uses request parameters", func(t *testing.T) {
		var capturedRequest Request
		client := &fakeClient{
			captureRequest: func(r Request) {
				capturedRequest = r
			},
			response: Response{Content: "Captured"},
		}

		_, _ = client.Complete(context.Background(), Request{
			SystemPrompt:    "system",
			UserPrompt:      "user",
			Model:           "model-x",
			Temperature:     0.9,
			MaxOutputTokens: 100,
		})

		if capturedRequest.SystemPrompt != "system" {
			t.Errorf("captured SystemPrompt %q, want %q", capturedRequest.SystemPrompt, "system")
		}
		if capturedRequest.UserPrompt != "user" {
			t.Errorf("captured UserPrompt %q, want %q", capturedRequest.UserPrompt, "user")
		}
		if capturedRequest.Model != "model-x" {
			t.Errorf("captured Model %q, want %q", capturedRequest.Model, "model-x")
		}
		if capturedRequest.Temperature != 0.9 {
			t.Errorf("captured Temperature %f, want %f", capturedRequest.Temperature, 0.9)
		}
		if capturedRequest.MaxOutputTokens != 100 {
			t.Errorf("captured MaxOutputTokens %d, want %d", capturedRequest.MaxOutputTokens, 100)
		}
	})
}

// TestErrorPropagation tests that error types are properly propagated.
func TestErrorPropagation(t *testing.T) {
	t.Run("context canceled error", func(t *testing.T) {
		err := ContextCanceled("deadline exceeded")
		got := err.Error()
		want := "ContextCanceled: deadline exceeded"
		if got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
		if !IsContextCanceled(err) {
			t.Errorf("IsContextCanceled(%v) = false, want true", err)
		}
	})

	t.Run("rate limited error", func(t *testing.T) {
		err := RateLimited("too many requests")
		got := err.Error()
		want := "RateLimited: too many requests"
		if got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	})

	t.Run("internal error", func(t *testing.T) {
		err := Internal("connection reset")
		got := err.Error()
		want := "Internal: connection reset"
		if got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	})

	t.Run("standard errors are not matched by specific checks", func(t *testing.T) {
		standardErr := errors.New("standard error")
		if IsContextCanceled(standardErr) {
			t.Error("IsContextCanceled(standard error) = true, want false")
		}
		if errors.Is(standardErr, ContextCanceled("test")) {
			t.Error("errors.Is(standard error, ContextCanceled) = true, want false")
		}
	})

	t.Run("context canceled errors match IsContextCanceled", func(t *testing.T) {
		err := ContextCanceled("test")
		if !IsContextCanceled(err) {
			t.Error("IsContextCanceled(ContextCanceled) = false, want true")
		}
	})
}

// TestEmptyResponse tests behavior with empty responses.
func TestEmptyResponse(t *testing.T) {
	client := &fakeClient{
		response: Response{Content: ""},
	}

	resp, err := client.Complete(context.Background(), Request{Model: "empty-model"})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "" {
		t.Errorf("expected empty content, got %q", resp.Content)
	}
}

// TestZeroTemperature tests that zero temperature is handled.
func TestZeroTemperature(t *testing.T) {
	client := &fakeClient{
		response: Response{Content: "Deterministic output"},
	}

	_, err := client.Complete(context.Background(), Request{
		Model:       "zero-temp-model",
		Temperature: 0.0,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestZeroMaxTokens tests that zero MaxOutputTokens (no limit) is handled.
func TestZeroMaxTokens(t *testing.T) {
	client := &fakeClient{
		response: Response{Content: "No limit"},
	}

	_, err := client.Complete(context.Background(), Request{
		Model:           "no-limit-model",
		MaxOutputTokens: 0,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// fakeClient is a test double that implements the Client interface.
// It demonstrates how providers can be injected via dependency injection.
type fakeClient struct {
	response       Response
	err            error
	captureRequest func(Request)
	shouldCancel   bool
}

// Complete implements the Client interface for testing.
func (f *fakeClient) Complete(ctx context.Context, req Request) (Response, error) {
	if f.captureRequest != nil {
		f.captureRequest(req)
	}
	if f.shouldCancel {
		return Response{}, ContextCanceled("canceled during test")
	}
	// Check context cancellation
	select {
	case <-ctx.Done():
		return Response{}, ContextCanceled("context canceled")
	default:
	}
	if f.err != nil {
		return Response{}, f.err
	}
	return f.response, nil
}

// IsContextCanceled checks if an error is a context cancellation error.
func IsContextCanceled(err error) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(Error); ok {
		return e.Code == "ContextCanceled"
	}
	return false
}
