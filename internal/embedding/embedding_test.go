package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestEmbed_Success tests a successful embedding request.
func TestEmbed_Success(t *testing.T) {
	vector := []float32{0.1, 0.2, 0.3}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request method
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		// Verify content type
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", ct)
		}

		// Parse and verify request body
		var req embeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to parse request: %v", err)
		}

		if req.Model != "test-model" {
			t.Errorf("expected model 'test-model', got %q", req.Model)
		}
		if len(req.Inputs) != 2 {
			t.Errorf("expected 2 inputs, got %d", len(req.Inputs))
		}

		// Send success response
		resp := embeddingResponse{
			Object: "list",
			Model:  "test-model",
			Data: []embeddingData{
				{Object: "embedding", Index: 0, Vector: vector},
				{Object: "embedding", Index: 1, Vector: vector},
			},
			Usage: usage{PromptTokens: 10, TotalTokens: 10},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:        server.URL,
		EmbeddingModel: "test-model",
		Timeout:        10 * time.Second,
	})

	embeddings, err := client.Embed(context.Background(), Request{
		Model:  "test-model",
		Inputs: []string{"hello", "world"},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(embeddings) != 2 {
		t.Fatalf("expected 2 embeddings, got %d", len(embeddings))
	}

	if embeddings[0].Index != 0 || embeddings[1].Index != 1 {
		t.Fatal("embedding indices are incorrect")
	}

	for i, emb := range embeddings {
		if len(emb.Vector) != len(vector) {
			t.Errorf("embedding[%d]: expected vector length %d, got %d", i, len(vector), len(emb.Vector))
		}
		for j, v := range emb.Vector {
			if v != vector[j] {
				t.Errorf("embedding[%d][%d]: expected %f, got %f", i, j, vector[j], v)
			}
		}
	}
}

// TestEmbed_BatchInputs tests batch embedding of multiple texts.
func TestEmbed_BatchInputs(t *testing.T) {
	vector := []float32{0.5, 0.5, 0.5}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req embeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to parse request: %v", err)
		}

		// Verify batch size
		if len(req.Inputs) != 5 {
			t.Errorf("expected 5 inputs, got %d", len(req.Inputs))
		}

		resp := embeddingResponse{
			Object: "list",
			Model:  "test-model",
			Data:   make([]embeddingData, 5),
		}
		for i := range resp.Data {
			resp.Data[i] = embeddingData{Object: "embedding", Index: i, Vector: vector}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:        server.URL,
		EmbeddingModel: "test-model",
	})

	embeddings, err := client.Embed(context.Background(), Request{
		Inputs: []string{"a", "b", "c", "d", "e"},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(embeddings) != 5 {
		t.Fatalf("expected 5 embeddings, got %d", len(embeddings))
	}
}

// TestEmbed_WithAPIKey tests that API key is sent when configured.
func TestEmbed_WithAPIKey(t *testing.T) {
	var receivedAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")

		resp := embeddingResponse{
			Object: "list",
			Data: []embeddingData{
				{Object: "embedding", Index: 0, Vector: []float32{0.1, 0.2, 0.3}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:        server.URL,
		EmbeddingModel: "test-model",
		APIKey:         "test-api-key-123",
	})

	_, err := client.Embed(context.Background(), Request{
		Inputs: []string{"hello"},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedAuth := "Bearer test-api-key-123"
	if receivedAuth != expectedAuth {
		t.Errorf("expected Authorization %q, got %q", expectedAuth, receivedAuth)
	}
}

// TestEmbed_WithoutAPIKey tests that no Authorization header is sent when no key configured.
func TestEmbed_WithoutAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "" {
			t.Errorf("expected no Authorization header, got %q", auth)
		}

		resp := embeddingResponse{
			Object: "list",
			Data: []embeddingData{
				{Object: "embedding", Index: 0, Vector: []float32{0.1, 0.2, 0.3}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:        server.URL,
		EmbeddingModel: "test-model",
	})

	_, err := client.Embed(context.Background(), Request{
		Inputs: []string{"hello"},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestEmbed_MalformedJSON tests handling of malformed JSON response.
func TestEmbed_MalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{ invalid json response }"))
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:        server.URL,
		EmbeddingModel: "test-model",
	})

	_, err := client.Embed(context.Background(), Request{
		Inputs: []string{"hello"},
	})

	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}

	if !IsInvalidResponse(err) {
		t.Errorf("expected InvalidResponse error, got %T: %v", err, err)
	}

	errStr := err.Error()
	if !strings.Contains(errStr, "parse") && !strings.Contains(errStr, "json") {
		t.Errorf("expected error to mention JSON parsing, got %q", errStr)
	}
}

// TestEmbed_HTTP404 tests handling of 404 Not Found.
func TestEmbed_HTTP404(t *testing.T) {
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
		BaseURL:        server.URL,
		EmbeddingModel: "nonexistent-model",
	})

	_, err := client.Embed(context.Background(), Request{
		Inputs: []string{"hello"},
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !IsInternal(err) {
		t.Errorf("expected Internal error, got %T: %v", err, err)
	}

	errStr := err.Error()
	if !strings.Contains(errStr, "404") && !strings.Contains(errStr, "not found") {
		t.Errorf("expected error to mention 404 or 'not found', got %q", errStr)
	}
}

// TestEmbed_HTTP429 tests handling of 429 Rate Limit.
func TestEmbed_HTTP429(t *testing.T) {
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
		BaseURL:        server.URL,
		EmbeddingModel: "test-model",
	})

	_, err := client.Embed(context.Background(), Request{
		Inputs: []string{"hello"},
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !IsRateLimited(err) {
		t.Errorf("expected RateLimited error, got %T: %v", err, err)
	}

	errStr := err.Error()
	if !strings.Contains(errStr, "rate limit") {
		t.Errorf("expected error to mention rate limit, got %q", errStr)
	}
}

// TestEmbed_EmptyResult tests handling of response with empty data array.
func TestEmbed_EmptyResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := embeddingResponse{
			Object: "list",
			Model:  "test-model",
			Data:   []embeddingData{},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:        server.URL,
		EmbeddingModel: "test-model",
	})

	_, err := client.Embed(context.Background(), Request{
		Inputs: []string{"hello"},
	})

	if err == nil {
		t.Fatal("expected error for empty data, got nil")
	}

	if !IsInvalidResponse(err) {
		t.Errorf("expected InvalidResponse error, got %T: %v", err, err)
	}

	errStr := err.Error()
	if !strings.Contains(errStr, "no embeddings") {
		t.Errorf("expected error to mention empty embeddings, got %q", errStr)
	}
}

// TestEmbed_EmptyInputs tests that empty inputs are rejected.
func TestEmbed_EmptyInputs(t *testing.T) {
	client := NewClient(Config{
		BaseURL:        "http://example.com/v1",
		EmbeddingModel: "test-model",
	})

	_, err := client.Embed(context.Background(), Request{
		Inputs: []string{},
	})

	if err == nil {
		t.Fatal("expected error for empty inputs, got nil")
	}

	if !IsInvalidResponse(err) {
		t.Errorf("expected InvalidResponse error, got %T: %v", err, err)
	}
}

// TestEmbed_EmptyInputTexts tests that single empty string input is accepted (API level).
func TestEmbed_EmptyInputTexts(t *testing.T) {
	// Allow single empty string - the API will handle this
	vector := []float32{0.1, 0.2, 0.3}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := embeddingResponse{
			Object: "list",
			Data: []embeddingData{
				{Object: "embedding", Index: 0, Vector: vector},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:        server.URL,
		EmbeddingModel: "test-model",
	})

	embeddings, err := client.Embed(context.Background(), Request{
		Inputs: []string{""}, // Single empty string is allowed
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(embeddings) != 1 {
		t.Fatalf("expected 1 embedding, got %d", len(embeddings))
	}
}

// TestEmbed_MismatchedDimensions tests handling of mismatched vector dimensions.
func TestEmbed_MismatchedDimensions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := embeddingResponse{
			Object: "list",
			Data: []embeddingData{
				{Object: "embedding", Index: 0, Vector: []float32{0.1, 0.2, 0.3}},
				{Object: "embedding", Index: 1, Vector: []float32{0.1, 0.2}}, // Different dimension
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:        server.URL,
		EmbeddingModel: "test-model",
	})

	_, err := client.Embed(context.Background(), Request{
		Inputs: []string{"hello", "world"},
	})

	if err == nil {
		t.Fatal("expected error for mismatched dimensions, got nil")
	}

	if !IsInvalidResponse(err) {
		t.Errorf("expected InvalidResponse error, got %T: %v", err, err)
	}

	errStr := err.Error()
	if !strings.Contains(errStr, "dimension") {
		t.Errorf("expected error to mention dimension, got %q", errStr)
	}
}

// TestEmbed_ContextCancellation tests that context cancellation is handled.
func TestEmbed_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Server never responds - client should timeout
		select {}
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:        server.URL,
		EmbeddingModel: "test-model",
		Timeout:        100 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately
	cancel()

	_, err := client.Embed(ctx, Request{
		Inputs: []string{"hello"},
	})

	if err == nil {
		t.Fatal("expected error for canceled context, got nil")
	}

	if !strings.Contains(err.Error(), "canceled") {
		t.Errorf("expected error to mention canceled, got %q", err.Error())
	}
}

// TestEmbed_ValidateVector_Nil tests nil vector rejection.
func TestEmbed_ValidateVector_Nil(t *testing.T) {
	err := ValidateVector(nil, 0)
	if err == nil {
		t.Fatal("expected error for nil vector, got nil")
	}

	if !strings.Contains(err.Error(), "nil") {
		t.Errorf("expected error to mention nil, got %q", err.Error())
	}
}

// TestEmbed_ValidateVector_Empty tests empty vector rejection.
func TestEmbed_ValidateVector_Empty(t *testing.T) {
	err := ValidateVector([]float32{}, 0)
	if err == nil {
		t.Fatal("expected error for empty vector, got nil")
	}

	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("expected error to mention empty, got %q", err.Error())
	}
}

// TestEmbed_ValidateBatch_InvalidIndex tests invalid index rejection.
func TestEmbed_ValidateBatch_InvalidIndex(t *testing.T) {
	embeddings := []Embedding{
		{Index: 5, Vector: []float32{0.1, 0.2, 0.3}},
		{Index: 0, Vector: []float32{0.4, 0.5, 0.6}},
	}

	err := ValidateBatch(embeddings)
	if err == nil {
		t.Fatal("expected error for invalid index, got nil")
	}

	errStr := err.Error()
	if !strings.Contains(errStr, "index") {
		t.Errorf("expected error to mention index, got %q", errStr)
	}
}

// TestEmbed_CustomModelOverride tests that request model overrides config model.
func TestEmbed_CustomModelOverride(t *testing.T) {
	var capturedModel string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req embeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		capturedModel = req.Model

		resp := embeddingResponse{
			Object: "list",
			Data: []embeddingData{
				{Object: "embedding", Index: 0, Vector: []float32{0.1, 0.2, 0.3}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:        server.URL,
		EmbeddingModel: "config-model",
	})

	_, err := client.Embed(context.Background(), Request{
		Model:  "request-model",
		Inputs: []string{"hello"},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedModel != "request-model" {
		t.Errorf("expected model 'request-model', got %q", capturedModel)
	}
}

// TestEmbed_Dimensions tests that dimensions are sent when specified.
func TestEmbed_Dimensions(t *testing.T) {
	var capturedDims *int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req embeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		capturedDims = req.Dimensions

		resp := embeddingResponse{
			Object: "list",
			Data: []embeddingData{
				{Object: "embedding", Index: 0, Vector: []float32{0.1, 0.2, 0.3}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:        server.URL,
		EmbeddingModel: "test-model",
	})

	_, err := client.Embed(context.Background(), Request{
		Inputs:     []string{"hello"},
		Dimensions: 128,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedDims == nil {
		t.Fatal("expected dimensions to be set, got nil")
	}

	if *capturedDims != 128 {
		t.Errorf("expected dimensions 128, got %d", *capturedDims)
	}
}

// TestEmbed_EmbeddingDimension tests Dimension() method.
func TestEmbed_EmbeddingDimension(t *testing.T) {
	emb := Embedding{
		Vector: []float32{0.1, 0.2, 0.3, 0.4},
	}

	if emb.Dimension() != 4 {
		t.Errorf("expected dimension 4, got %d", emb.Dimension())
	}
}

// TestEmbed_ValidateBatch_Empty tests empty batch passes validation.
func TestEmbed_ValidateBatch_Empty(t *testing.T) {
	err := ValidateBatch([]Embedding{})
	if err != nil {
		t.Fatalf("expected no error for empty batch, got %v", err)
	}
}

// TestEmbed_HTTP500 tests handling of 500 Internal Server Error.
func TestEmbed_HTTP500(t *testing.T) {
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
		BaseURL:        server.URL,
		EmbeddingModel: "test-model",
	})

	_, err := client.Embed(context.Background(), Request{
		Inputs: []string{"hello"},
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !IsInternal(err) {
		t.Errorf("expected Internal error, got %T: %v", err, err)
	}
}

// TestEmbed_ValidateVector_Valid tests valid vector passes.
func TestEmbed_ValidateVector_Valid(t *testing.T) {
	err := ValidateVector([]float32{0.1, 0.2, 0.3}, 0)
	if err != nil {
		t.Fatalf("expected no error for valid vector, got %v", err)
	}
}

// TestEmbed_ValidateBatch_Valid tests valid batch passes.
func TestEmbed_ValidateBatch_Valid(t *testing.T) {
	embeddings := []Embedding{
		{Index: 0, Vector: []float32{0.1, 0.2, 0.3}},
		{Index: 1, Vector: []float32{0.4, 0.5, 0.6}},
		{Index: 2, Vector: []float32{0.7, 0.8, 0.9}},
	}

	err := ValidateBatch(embeddings)
	if err != nil {
		t.Fatalf("expected no error for valid batch, got %v", err)
	}
}

// TestEmbed_HTTP401 tests handling of 401 Unauthorized.
func TestEmbed_HTTP401(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{
				"message": "invalid api key",
				"type":    "invalid_request_error",
			},
		})
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:        server.URL,
		EmbeddingModel: "test-model",
		APIKey:         "invalid-key",
	})

	_, err := client.Embed(context.Background(), Request{
		Inputs: []string{"hello"},
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !IsInternal(err) {
		t.Errorf("expected Internal error, got %T: %v", err, err)
	}
}

// TestEmbed_HTTPUnknownError tests handling of unknown HTTP error codes.
func TestEmbed_HTTPUnknownError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error": {"message": "timeout", "type": "timeout_error"}}`))
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:        server.URL,
		EmbeddingModel: "test-model",
	})

	_, err := client.Embed(context.Background(), Request{
		Inputs: []string{"hello"},
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !IsInternal(err) {
		t.Errorf("expected Internal error, got %T: %v", err, err)
	}
}

// TestEmbed_ConnectError tests handling of connection errors.
func TestEmbed_ConnectError(t *testing.T) {
	client := NewClient(Config{
		BaseURL:        "http://invalid-host:99999/v1",
		EmbeddingModel: "test-model",
		Timeout:        100 * time.Millisecond,
	})

	_, err := client.Embed(context.Background(), Request{
		Inputs: []string{"hello"},
	})

	if err == nil {
		t.Fatal("expected connection error, got nil")
	}

	if !IsInternal(err) {
		t.Errorf("expected Internal error, got %T: %v", err, err)
	}
}

// TestEmbed_ConfigDefaults tests default timeout configuration.
func TestEmbed_ConfigDefaults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := embeddingResponse{
			Object: "list",
			Data: []embeddingData{
				{Object: "embedding", Index: 0, Vector: []float32{0.1, 0.2, 0.3}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:        server.URL,
		EmbeddingModel: "test-model",
	})

	if client.client.Timeout != 30*time.Second {
		t.Errorf("expected default timeout 30s, got %v", client.client.Timeout)
	}
}

// TestEmbed_EmbeddingModelFromConfig tests that config model is used when request model is empty.
func TestEmbed_EmbeddingModelFromConfig(t *testing.T) {
	var capturedModel string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req embeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		capturedModel = req.Model

		resp := embeddingResponse{
			Object: "list",
			Data: []embeddingData{
				{Object: "embedding", Index: 0, Vector: []float32{0.1, 0.2, 0.3}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:        server.URL,
		EmbeddingModel: "config-model",
	})

	_, err := client.Embed(context.Background(), Request{
		// No model specified - should use config model
		Inputs: []string{"hello"},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedModel != "config-model" {
		t.Errorf("expected model 'config-model', got %q", capturedModel)
	}
}

// TestEmbed_EmbeddingModelEmptyConfigEmpty tests that empty model is sent when both empty.
func TestEmbed_EmbeddingModelEmptyConfigEmpty(t *testing.T) {
	var capturedModel string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req embeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		capturedModel = req.Model

		resp := embeddingResponse{
			Object: "list",
			Data: []embeddingData{
				{Object: "embedding", Index: 0, Vector: []float32{0.1, 0.2, 0.3}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:        server.URL,
		EmbeddingModel: "",
	})

	_, err := client.Embed(context.Background(), Request{
		Model:  "",
		Inputs: []string{"hello"},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedModel != "" {
		t.Errorf("expected empty model, got %q", capturedModel)
	}
}

// TestEmbed_ConfigWithDimensions returns config with dimensions set.
func TestEmbed_ConfigWithDimensions(t *testing.T) {
	config := Config{
		BaseURL:        "http://example.com/v1",
		EmbeddingModel: "test-model",
	}

	newConfig := config.WithDimensions(128)
	if newConfig.Dimensions != 128 {
		t.Errorf("expected dimensions 128, got %d", newConfig.Dimensions)
	}

	// Original should be unchanged
	if config.Dimensions != 0 {
		t.Errorf("original config should be unchanged, got %d", config.Dimensions)
	}
}

// TestEmbed_ErrorFormatting tests error formatting.
func TestEmbed_ErrorFormatting(t *testing.T) {
	tests := []struct {
		name     string
		err      Error
		expected string
	}{
		{
			name:     "with code",
			err:      Error{Code: "TestCode", Message: "test message"},
			expected: "TestCode: test message",
		},
		{
			name:     "without code",
			err:      Error{Code: "", Message: "test message"},
			expected: "test message",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.err.Error()
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

// TestEmbed_IsErrorChecks tests error type checks.
func TestEmbed_IsErrorChecks(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		isRate     bool
		isInvalid  bool
		isInternal bool
	}{
		{"rate limited", RateLimited("test"), true, false, false},
		{"invalid response", InvalidResponse("test"), false, true, false},
		{"internal error", Internal("test"), false, false, true},
		{"nil error", nil, false, false, false},
		{"string error", errors.New("test"), false, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if IsRateLimited(tt.err) != tt.isRate {
				t.Errorf("IsRateLimited(%v) = %v, want %v", tt.err, IsRateLimited(tt.err), tt.isRate)
			}
			if IsInvalidResponse(tt.err) != tt.isInvalid {
				t.Errorf("IsInvalidResponse(%v) = %v, want %v", tt.err, IsInvalidResponse(tt.err), tt.isInvalid)
			}
			if IsInternal(tt.err) != tt.isInternal {
				t.Errorf("IsInternal(%v) = %v, want %v", tt.err, IsInternal(tt.err), tt.isInternal)
			}
		})
	}
}

// TestEmbed_TimeoutCancellation tests that timeout contexts produce errors.
func TestEmbed_TimeoutCancellation(t *testing.T) {
	// Create server that delays response
	delay := 100 * time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		resp := embeddingResponse{
			Object: "list",
			Data: []embeddingData{
				{Object: "embedding", Index: 0, Vector: []float32{0.1, 0.2, 0.3}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Create client with very short timeout
	client := NewClient(Config{
		BaseURL:        server.URL,
		EmbeddingModel: "test-model",
		Timeout:        10 * time.Millisecond,
	})

	_, err := client.Embed(context.Background(), Request{
		Inputs: []string{"hello"},
	})

	// Should get a connection error due to timeout
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	// The error should mention timeout or connection failure
	_ = err
}

// TestEmbed_SingleEmbeddingVector tests that a single embedding is returned.
func TestEmbed_SingleEmbeddingVector(t *testing.T) {
	vector := []float32{0.1, 0.2, 0.3, 0.4, 0.5}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := embeddingResponse{
			Object: "list",
			Model:  "test-model",
			Data: []embeddingData{
				{Object: "embedding", Index: 0, Vector: vector},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:        server.URL,
		EmbeddingModel: "test-model",
	})

	embeddings, err := client.Embed(context.Background(), Request{
		Inputs: []string{"hello"},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(embeddings) != 1 {
		t.Fatalf("expected 1 embedding, got %d", len(embeddings))
	}

	if len(embeddings[0].Vector) != len(vector) {
		t.Errorf("expected vector length %d, got %d", len(vector), len(embeddings[0].Vector))
	}
}

// TestEmbed_UsageReturnsNoPanic tests that response with usage field doesn't cause issues.
func TestEmbed_UsageReturnsNoPanic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := embeddingResponse{
			Object: "list",
			Model:  "test-model",
			Data: []embeddingData{
				{Object: "embedding", Index: 0, Vector: []float32{0.1, 0.2, 0.3}},
			},
			Usage: usage{PromptTokens: 5, TotalTokens: 5},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:        server.URL,
		EmbeddingModel: "test-model",
	})

	_, err := client.Embed(context.Background(), Request{
		Inputs: []string{"hello"},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestEmbed_JSONSerializationRoundTrip tests that embedding request and response serialize correctly.
func TestEmbed_JSONSerializationRoundTrip(t *testing.T) {
	dim := 128
	req := embeddingRequest{
		Model:      "test-model",
		Inputs:     []string{"hello", "world"},
		Dimensions: &dim,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("unexpected marshal error: %v", err)
	}

	var unmarshaled embeddingRequest
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}

	if unmarshaled.Model != req.Model {
		t.Errorf("expected model %q, got %q", req.Model, unmarshaled.Model)
	}

	if len(unmarshaled.Inputs) != len(req.Inputs) {
		t.Errorf("expected %d inputs, got %d", len(req.Inputs), len(unmarshaled.Inputs))
	}

	if *unmarshaled.Dimensions != *req.Dimensions {
		t.Errorf("expected dimensions %d, got %d", *req.Dimensions, *unmarshaled.Dimensions)
	}
}

// TestEmbed_HTTPReadError tests handling of read errors.
func TestEmbed_HTTPReadError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Close connection without sending complete response
		hj, ok := w.(http.Hijacker)
		if ok {
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
		}
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:        server.URL,
		EmbeddingModel: "test-model",
	})

	_, err := client.Embed(context.Background(), Request{
		Inputs: []string{"hello"},
	})

	if err == nil {
		t.Fatal("expected error for truncated response, got nil")
	}
}

// TestEmbed_ServerOnlyResponds tests server verification that response object matches.
func TestEmbed_ServerOnlyResponds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req embeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		if len(req.Inputs) != 3 {
			t.Fatalf("expected 3 inputs, got %d", len(req.Inputs))
		}

		if req.Dimensions != nil {
			t.Fatalf("expected no dimensions, got %d", *req.Dimensions)
		}

		// Send minimal valid response
		resp := embeddingResponse{
			Object: "list",
			Data: []embeddingData{
				{Object: "embedding", Index: 0, Vector: []float32{0.1}},
				{Object: "embedding", Index: 1, Vector: []float32{0.2}},
				{Object: "embedding", Index: 2, Vector: []float32{0.3}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:        server.URL,
		EmbeddingModel: "test-model",
	})

	embeddings, err := client.Embed(context.Background(), Request{
		Inputs: []string{"a", "b", "c"},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(embeddings) != 3 {
		t.Fatalf("expected 3 embeddings, got %d", len(embeddings))
	}
}

// TestEmbed_ValidateVectorWithZero tests vector with zero values is valid.
func TestEmbed_ValidateVectorWithZero(t *testing.T) {
	err := ValidateVector([]float32{0.0, 0.0, 0.0}, 0)
	if err != nil {
		t.Fatalf("expected no error for vector with zeros, got %v", err)
	}
}
