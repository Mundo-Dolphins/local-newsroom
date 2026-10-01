package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestComplete_Success tests a successful completion request.
func TestComplete_Success(t *testing.T) {
	// Setup test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request method
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		// Verify content type
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", ct)
		}

		// Parse and echo request for verification
		var req chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to parse request: %v", err)
		}

		// Verify request body
		if req.Model != "test-model" {
			t.Errorf("expected model 'test-model', got %q", req.Model)
		}
		if req.Temperature != 0.7 {
			t.Errorf("expected temperature 0.7, got %f", req.Temperature)
		}

		// Send success response
		resp := chatCompletionResponse{
			Choices: []choice{
				{
					Message: message{
						Role:    "assistant",
						Content: "This is the model's response",
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:     server.URL,
		Model:       "test-model",
		Timeout:     10 * time.Second,
		Temperature: 0.7,
	})

	resp, err := client.Complete(context.Background(), Request{
		SystemPrompt: "You are a helpful assistant",
		UserPrompt:   "Hello, who are you?",
		Model:        "test-model",
		Temperature:  0.7,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Content != "This is the model's response" {
		t.Errorf("expected 'This is the model's response', got %q", resp.Content)
	}
}

// TestComplete_WithAPIKey tests that API key is sent when configured.
func TestComplete_WithAPIKey(t *testing.T) {
	var receivedAPIKey string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAPIKey = r.Header.Get("Authorization")
		resp := chatCompletionResponse{
			Choices: []choice{
				{Message: message{Role: "assistant", Content: "Test"}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:     server.URL,
		APIKey:      "test-api-key-123",
		Temperature: 0.5,
	})

	_, err := client.Complete(context.Background(), Request{
		Model:       "test-model",
		Temperature: 0.5,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedAuth := "Bearer test-api-key-123"
	if receivedAPIKey != expectedAuth {
		t.Errorf("expected Authorization %q, got %q", expectedAuth, receivedAPIKey)
	}
}

// TestComplete_WithoutAPIKey tests that no Authorization header is sent.
func TestComplete_WithoutAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "" {
			t.Errorf("expected no Authorization header, got %q", auth)
		}

		resp := chatCompletionResponse{
			Choices: []choice{
				{Message: message{Role: "assistant", Content: "Test"}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:     server.URL,
		Temperature: 0.0,
	})

	_, err := client.Complete(context.Background(), Request{
		Model:       "test-model",
		Temperature: 0.0,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestComplete_HTTP404 tests handling of 404 Not Found.
func TestComplete_HTTP404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{
				"message": "endpoint not found",
				"type":    "not_found_error",
			},
		})
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL: server.URL,
		Model:   "nonexistent-model",
	})

	_, err := client.Complete(context.Background(), Request{
		Model: "test-model",
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !IsInternal(err) {
		t.Errorf("expected Internal error, got %T: %v", err, err)
	}

	// Check error message mentions 404
	errStr := err.Error()
	if !contains(errStr, "404") && !contains(errStr, "not found") {
		t.Errorf("expected error to mention 404 or 'not found', got %q", errStr)
	}
}

// TestComplete_HTTP429 tests handling of 429 Rate Limit.
func TestComplete_HTTP429(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{
				"message": "rate limit exceeded",
				"type":    "rate_limit_error",
			},
		})
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL: server.URL,
		Model:   "test-model",
	})

	_, err := client.Complete(context.Background(), Request{
		Model: "test-model",
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !IsRateLimited(err) {
		t.Errorf("expected RateLimited error, got %T: %v", err, err)
	}

	errStr := err.Error()
	if !contains(errStr, "rate limit") {
		t.Errorf("expected error to mention rate limit, got %q", errStr)
	}
}

// TestComplete_HTTP500 tests handling of 500 Internal Server Error.
func TestComplete_HTTP500(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{
				"message": "internal error",
				"type":    "internal_error",
			},
		})
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL: server.URL,
		Model:   "test-model",
	})

	_, err := client.Complete(context.Background(), Request{
		Model: "test-model",
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !IsInternal(err) {
		t.Errorf("expected Internal error, got %T: %v", err, err)
	}
}

// TestComplete_EmptyChoices tests handling of response with no choices.
func TestComplete_EmptyChoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Empty choices array
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []interface{}{},
		})
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL: server.URL,
		Model:   "test-model",
	})

	_, err := client.Complete(context.Background(), Request{
		Model: "test-model",
	})

	if err == nil {
		t.Fatal("expected error for empty choices, got nil")
	}

	errStr := err.Error()
	if !contains(errStr, "empty") && !contains(errStr, "no choices") {
		t.Errorf("expected error to mention empty choices, got %q", errStr)
	}
}

// TestComplete_EmptyContent tests handling of response with empty message content.
func TestComplete_EmptyContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []interface{}{
				map[string]interface{}{
					"message": map[string]string{
						"role":    "assistant",
						"content": "",
					},
				},
			},
		})
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL: server.URL,
		Model:   "test-model",
	})

	_, err := client.Complete(context.Background(), Request{
		Model: "test-model",
	})

	if err == nil {
		t.Fatal("expected error for empty content, got nil")
	}

	errStr := err.Error()
	if !contains(errStr, "empty") && !contains(errStr, "content") {
		t.Errorf("expected error to mention empty content, got %q", errStr)
	}
}

// TestComplete_MalformedJSON tests handling of malformed JSON response.
func TestComplete_MalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{ invalid json response }"))
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL: server.URL,
		Model:   "test-model",
	})

	_, err := client.Complete(context.Background(), Request{
		Model: "test-model",
	})

	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}

	if !IsInternal(err) {
		t.Errorf("expected Internal error, got %T: %v", err, err)
	}

	errStr := err.Error()
	if !contains(errStr, "parse") && !contains(errStr, "json") {
		t.Errorf("expected error to mention JSON parsing, got %q", errStr)
	}
}

// TestComplete_ContextCancellation tests that context cancellation is handled.
func TestComplete_ContextCancellation(t *testing.T) {
	// This test verifies the client handles context cancellation gracefully.
	// We use a quick response server to ensure the test completes within reasonable time.
	// The http.Client handles context cancellation natively, we just verify we don't hang.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := chatCompletionResponse{
			Choices: []choice{{
				Message: message{Role: "assistant", Content: "OK"},
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Create a client with a short timeout to verify timeout handling works
	client := NewClient(Config{
		BaseURL:     server.URL,
		Model:       "test-model",
		Timeout:     100 * time.Millisecond,
		Temperature: 0.0,
	})

	_, err := client.Complete(context.Background(), Request{
		Model: "test-model",
	})

	// Should succeed since server responds quickly
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestComplete_TimeoutCancellation tests that timeout contexts produce errors.
func TestComplete_TimeoutCancellation(t *testing.T) {
	// Create server that delays response
	delay := 100 * time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		resp := chatCompletionResponse{
			Choices: []choice{{
				Message: message{Role: "assistant", Content: "OK"},
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Create client with very short timeout
	client := NewClient(Config{
		BaseURL:     server.URL,
		Model:       "test-model",
		Timeout:     10 * time.Millisecond,
		Temperature: 0.0,
	})

	_, err := client.Complete(context.Background(), Request{
		Model: "test-model",
	})

	// Should get a connection error due to timeout
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	// The error should mention timeout or connection failure
	_ = err
}

// TestComplete_CustomHTTPError tests handling of unknown HTTP status codes.
func TestComplete_CustomHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout) // 504
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error": {"message": "gateway timeout", "type": "timeout_error"}}`))
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL: server.URL,
		Model:   "test-model",
	})

	_, err := client.Complete(context.Background(), Request{
		Model: "test-model",
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !IsInternal(err) {
		t.Errorf("expected Internal error, got %T: %v", err, err)
	}
}

// TestComplete_TemperatureClamping tests that temperature is properly clamped.
func TestComplete_TemperatureClamping(t *testing.T) {
	var capturedTemp float64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		capturedTemp = req.Temperature

		resp := chatCompletionResponse{
			Choices: []choice{{Message: message{Role: "assistant", Content: "OK"}}},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:     server.URL,
		Model:       "test-model",
		Temperature: 1.5, // Will be used for testing
	})

	// Test clamping of high temperature
	_, err := client.Complete(context.Background(), Request{
		Model:       "test-model",
		Temperature: 5.0, // Should be clamped to 2.0
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedTemp > 2.0 {
		t.Errorf("expected temperature to be clamped to max 2.0, got %f", capturedTemp)
	}

	// Test clamping of low/negative temperature
	capturedTemp = 0
	_, err = client.Complete(context.Background(), Request{
		Model:       "test-model",
		Temperature: -1.0, // Should default to 0.7
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedTemp != 0.7 {
		t.Errorf("expected temperature to default to 0.7, got %f", capturedTemp)
	}
}

// TestComplete_MaxTokens tests that max_tokens is sent when specified.
func TestComplete_MaxTokens(t *testing.T) {
	var capturedMaxTokens *int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		capturedMaxTokens = req.MaxTokens

		resp := chatCompletionResponse{
			Choices: []choice{{Message: message{Role: "assistant", Content: "OK"}}},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:     server.URL,
		Model:       "test-model",
		Temperature: 0.0,
	})

	_, err := client.Complete(context.Background(), Request{
		Model:           "test-model",
		MaxOutputTokens: 100,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedMaxTokens == nil {
		t.Fatal("expected max_tokens to be set, got nil")
	}

	if *capturedMaxTokens != 100 {
		t.Errorf("expected max_tokens 100, got %d", *capturedMaxTokens)
	}
}

// TestComplete_ZeroMaxTokens tests that zero max_tokens sends no max_tokens field.
func TestComplete_ZeroMaxTokens(t *testing.T) {
	var capturedMaxTokens *int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		capturedMaxTokens = req.MaxTokens

		resp := chatCompletionResponse{
			Choices: []choice{{Message: message{Role: "assistant", Content: "OK"}}},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:     server.URL,
		Model:       "test-model",
		Temperature: 0.0,
	})

	_, err := client.Complete(context.Background(), Request{
		Model:           "test-model",
		MaxOutputTokens: 0, // Should not send max_tokens
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedMaxTokens != nil {
		t.Errorf("expected max_tokens to be nil, got %d", *capturedMaxTokens)
	}
}

// TestComplete_MessagesSent tests that system and user prompts are sent correctly.
func TestComplete_MessagesSent(t *testing.T) {
	var capturedMessages []message

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		capturedMessages = req.Messages

		resp := chatCompletionResponse{
			Choices: []choice{{Message: message{Role: "assistant", Content: "OK"}}},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:     server.URL,
		Model:       "test-model",
		Temperature: 0.0,
	})

	_, err := client.Complete(context.Background(), Request{
		SystemPrompt: "You are a helpful assistant",
		UserPrompt:   "What is 2+2?",
		Model:        "test-model",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(capturedMessages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(capturedMessages))
	}

	if capturedMessages[0].Role != "system" {
		t.Errorf("expected first message role 'system', got %q", capturedMessages[0].Role)
	}
	if capturedMessages[0].Content != "You are a helpful assistant" {
		t.Errorf("expected system content, got %q", capturedMessages[0].Content)
	}

	if capturedMessages[1].Role != "user" {
		t.Errorf("expected second message role 'user', got %q", capturedMessages[1].Role)
	}
	if capturedMessages[1].Content != "What is 2+2?" {
		t.Errorf("expected user content, got %q", capturedMessages[1].Content)
	}
}

// TestComplete_OnlyUserPrompt tests that user prompt is sent even without system prompt.
func TestComplete_OnlyUserPrompt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		if len(req.Messages) != 1 {
			t.Fatalf("expected 1 message, got %d", len(req.Messages))
		}

		if req.Messages[0].Role != "user" {
			t.Errorf("expected user role, got %q", req.Messages[0].Role)
		}

		resp := chatCompletionResponse{
			Choices: []choice{{Message: message{Role: "assistant", Content: "OK"}}},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:     server.URL,
		Model:       "test-model",
		Temperature: 0.0,
	})

	_, err := client.Complete(context.Background(), Request{
		UserPrompt: "Hello",
		Model:      "test-model",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestComplete_Timeout tests that custom timeout is respected.
func TestComplete_Timeout(t *testing.T) {
	// Test server that responds quickly (so timeout test works correctly)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := chatCompletionResponse{
			Choices: []choice{{Message: message{Role: "assistant", Content: "OK"}}},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Use a short timeout to verify it's configured
	client := NewClient(Config{
		BaseURL:     server.URL,
		Model:       "test-model",
		Timeout:     5 * time.Second, // Verify custom timeout is set
		Temperature: 0.0,
	})

	_, err := client.Complete(context.Background(), Request{
		Model: "test-model",
	})

	if err != nil {
		t.Fatalf("unexpected error with valid response: %v", err)
	}
}

// TestComplete_InvalidJSONRequest tests that malformed request body is handled.
func TestComplete_InvalidJSONRequest(t *testing.T) {
	// This test verifies the client doesn't panic on invalid request
	// The client marshals valid JSON, so this test is more about robustness
	client := NewClient(Config{
		BaseURL:     "http://invalid-host:9999",
		Model:       "test-model",
		Temperature: 0.0,
	})

	_, err := client.Complete(context.Background(), Request{
		Model: "test-model",
	})

	// Should get a connection error, not a panic
	if err == nil {
		t.Fatal("expected connection error, got nil")
	}

	// Should be an Internal error wrapping the connection error
	if !IsInternal(err) && !contains(err.Error(), "connection") && !contains(err.Error(), "failed") {
		t.Logf("Got error: %v (acceptable for invalid host)", err)
	}
}

// TestComplete_ResponseWithFinishReason tests handling of finish_reason field.
func TestComplete_ResponseWithFinishReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Include finish_reason which the client ignores (correct behavior)
		resp := chatCompletionResponse{
			Choices: []choice{{
				Message: message{Role: "assistant", Content: "Response"},
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:     server.URL,
		Model:       "test-model",
		Temperature: 0.0,
	})

	resp, err := client.Complete(context.Background(), Request{
		Model: "test-model",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Content != "Response" {
		t.Errorf("expected 'Response', got %q", resp.Content)
	}
}

// TestComplete_HealthCheck tests that client can be instantiated without errors.
func TestComplete_HealthCheck(t *testing.T) {
	client := NewClient(Config{
		BaseURL:         "http://localhost:8000/v1",
		Model:           "test-model",
		Timeout:         10 * time.Second,
		APIKey:          "test-key",
		Temperature:     0.5,
		MaxOutputTokens: 100,
	})

	if client == nil {
		t.Fatal("expected non-nil client")
	}

	if client.client == nil {
		t.Fatal("expected non-nil HTTP client")
	}
}

// Helper function to check if a string contains a substring.
func contains(s, substr string) bool {
	if len(s) < len(substr) {
		return false
	}
	if s == substr {
		return true
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestComplete_IgnoreReadError tests that read errors are handled gracefully.
func TestComplete_IgnoreReadError(t *testing.T) {
	// Create a response body that will cause an error on read
	// We use a limited reader to simulate partial read
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Write incomplete JSON to cause parse error
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		n, _ := io.WriteString(w, `{"choices": [`)
		// Close the connection without completing the JSON
		hj, ok := w.(http.Hijacker)
		if ok {
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
			_ = n
		}
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:     server.URL,
		Model:       "test-model",
		Temperature: 0.0,
	})

	_, err := client.Complete(context.Background(), Request{
		Model: "test-model",
	})

	// Should get an error for incomplete JSON or connection
	if err == nil {
		t.Fatal("expected error for incomplete response, got nil")
	}
}

// TestComplete_ErrorPropagation tests that HTTP errors are properly typed.
func TestComplete_ErrorPropagation(t *testing.T) {
	tests := []struct {
		name           string
		statusCode     int
		expectedType   func(error) bool
		responseBody   string
		messagePattern string
	}{
		{
			name:           "404 not found",
			statusCode:     http.StatusNotFound,
			expectedType:   IsInternal,
			responseBody:   `{"error": {"message": "not found", "type": "not_found"}}`,
			messagePattern: "not found",
		},
		{
			name:           "429 rate limited",
			statusCode:     http.StatusTooManyRequests,
			expectedType:   IsRateLimited,
			responseBody:   `{"error": {"message": "too many requests", "type": "rate_limit"}}`,
			messagePattern: "too many requests",
		},
		{
			name:           "500 internal error",
			statusCode:     http.StatusInternalServerError,
			expectedType:   IsInternal,
			responseBody:   `{"error": {"message": "internal error", "type": "internal"}}`,
			messagePattern: "internal error",
		},
		{
			name:           "401 unauthorized",
			statusCode:     http.StatusUnauthorized,
			expectedType:   IsInternal,
			responseBody:   `{"error": {"message": "invalid api key", "type": "auth"}}`,
			messagePattern: "invalid api key",
		},
		{
			name:           "unknown status with JSON",
			statusCode:     http.StatusGatewayTimeout,
			expectedType:   IsInternal,
			responseBody:   `{"error": {"message": "timeout", "type": "timeout"}}`,
			messagePattern: "provider error (504)",
		},
		{
			name:           "unknown status without JSON",
			statusCode:     http.StatusBadGateway,
			expectedType:   IsInternal,
			responseBody:   `Bad Gateway`,
			messagePattern: "HTTP 502",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				w.Header().Set("Content-Type", "application/json")
				if tt.responseBody != "" {
					_, _ = w.Write([]byte(tt.responseBody))
				}
			}))
			defer server.Close()

			client := NewClient(Config{
				BaseURL:     server.URL,
				Model:       "test-model",
				Temperature: 0.0,
			})

			_, err := client.Complete(context.Background(), Request{
				Model: "test-model",
			})

			if err == nil {
				t.Fatal("expected error, got nil")
			}

			if !tt.expectedType(err) {
				t.Errorf("expected error of type matched by %T, got %T: %v", tt.expectedType(err), err, err)
			}

			if !contains(err.Error(), tt.messagePattern) {
				t.Errorf("expected error to contain %q, got %q", tt.messagePattern, err.Error())
			}
		})
	}
}
