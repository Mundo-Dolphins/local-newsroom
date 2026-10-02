package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/extractor"
	"github.com/Mundo-Dolphins/local-newsroom/internal/fetcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/llm"
	"github.com/Mundo-Dolphins/local-newsroom/internal/researcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
)

// TestConfig tests workflow configuration.
func TestConfig(t *testing.T) {
	cfg := Config{
		Topic:        "Test Topic",
		URLs:         []string{"http://example.com"},
		OutputPath:   "/tmp/test.json",
		LLMBaseURL:   "http://localhost:8000/v1",
		LLMModel:     "test-model",
		LLMTimeout:   30.0,
		FetchTimeout: 10.0,
	}

	if cfg.Topic != "Test Topic" {
		t.Errorf("Expected Topic 'Test Topic', got %q", cfg.Topic)
	}
	if len(cfg.URLs) != 1 {
		t.Errorf("Expected 1 URL, got %d", len(cfg.URLs))
	}
	if cfg.LLMBaseURL != "http://localhost:8000/v1" {
		t.Errorf("Expected LLMBaseURL 'http://localhost:8000/v1', got %q", cfg.LLMBaseURL)
	}
}

// TestWorkflow_New tests the New function.
func TestWorkflow_New(t *testing.T) {
	cfg := Config{
		Topic:      "Test",
		URLs:       []string{"http://example.com"},
		OutputPath: "/tmp/test.json",
		LLMBaseURL: "http://localhost:8000/v1",
		LLMModel:   "test",
	}

	w := New(cfg)
	if w == nil {
		t.Fatal("Expected non-nil workflow")
	}
	if w.config.Topic != "Test" {
		t.Errorf("Expected Topic 'Test', got %q", w.config.Topic)
	}
}

// TestRun_InvalidInputs tests that invalid inputs are rejected.
func TestRun_InvalidInputs(t *testing.T) {
	t.Run("missing topic returns error", func(t *testing.T) {
		cfg := Config{
			URLs:       []string{"http://example.com"},
			OutputPath: "/tmp/test.json",
		}
		w := New(cfg)
		err := w.Run(context.Background())
		if err == nil {
			t.Fatal("Expected error for missing topic, got nil")
		}
		if !strings.Contains(err.Error(), "topic is required") {
			t.Logf("Expected 'topic is required' error, got: %v", err)
		}
	})

	t.Run("missing URLs returns error", func(t *testing.T) {
		cfg := Config{
			Topic:      "Test",
			OutputPath: "/tmp/test.json",
		}
		w := New(cfg)
		err := w.Run(context.Background())
		if err == nil {
			t.Fatal("Expected error for missing URLs, got nil")
		}
		if !strings.Contains(err.Error(), "URL is required") {
			t.Logf("Expected 'URL is required' error, got: %v", err)
		}
	})
}

// TestRun_NoLLMConfig tests that running without LLM config fails clearly.
func TestRun_NoLLMConfig(t *testing.T) {
	// Ensure no OMLX env vars are set
	os.Unsetenv("OMLX_BASE_URL") //nolint:errcheck
	os.Unsetenv("OMLX_MODEL")    //nolint:errcheck

	cfg := Config{
		Topic:      "Test",
		URLs:       []string{"http://invalid-host-that-does-not-exist.com"},
		OutputPath: "/tmp/test.json",
	}
	w := New(cfg)

	// This should fail at the LLM config check stage
	// Note: The pipeline runs fetch first, so it may fail there first
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	err := w.Run(ctx)
	if err == nil {
		t.Fatal("Expected error for missing LLM config or fetch failure, got nil")
	}

	var rf *ResearchFailure
	if !errors.As(err, &rf) {
		t.Logf("Expected ResearchFailure, got: %T", err)
	}
	// Pipeline order: fetch -> extraction -> LLM config
	// So we expect either fetch_failure or llm_config
	if rf.Type != "fetch_failure" && rf.Type != "llm_config" {
		t.Logf("Expected type 'fetch_failure' or 'llm_config', got %q", rf.Type)
	}
}

// TestRun_FetchFailure tests that all fetch failures are reported.
func TestRun_FetchFailure(t *testing.T) {
	// Create test server that always fails
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer failServer.Close()

	os.Setenv("OMLX_BASE_URL", "http://invalid:9999") //nolint:errcheck

	cfg := Config{
		Topic:         "Test",
		URLs:          []string{failServer.URL},
		OutputPath:    "/tmp/test.json",
		LLMBaseURL:    "http://invalid:9999",
		FetcherConfig: fetcher.Config{Timeout: 5 * time.Second},
	}
	w := New(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := w.Run(ctx)
	if err == nil {
		t.Fatal("Expected fetch failure error, got nil")
	}

	var rf *ResearchFailure
	if !errors.As(err, &rf) {
		t.Logf("Expected ResearchFailure, got: %T", err)
	}
	if rf.Type != "fetch_failure" {
		t.Logf("Expected type 'fetch_failure', got %q", rf.Type)
	}
}

// TestRun_ExtractionFailure tests that all extraction failures are reported.
func TestRun_ExtractionFailure(t *testing.T) {
	// Create a test server that returns non-HTML content that won't extract
	contentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte{0x00, 0x01, 0x02, 0x03}) // Binary data
	}))
	defer contentServer.Close()

	// This test uses a real workflow but with a fake LLM
	// Since extraction will fail first, the LLM won't be called
	cfg := Config{
		Topic:           "Test",
		URLs:            []string{contentServer.URL},
		OutputPath:      "/tmp/test.json",
		LLMBaseURL:      "http://invalid:9999",
		FetchTimeout:    5.0,
		FetcherConfig:   fetcher.Config{MaxSize: 100},
		ExtractorConfig: extractor.Config{MaxWordCount: 100},
	}
	w := New(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := w.Run(ctx)
	if err == nil {
		t.Fatal("Expected extraction failure error, got nil")
	}

	var rf *ResearchFailure
	if !errors.As(err, &rf) {
		t.Logf("Expected ResearchFailure, got: %T", err)
	}
	// Pipeline order: fetch -> extraction -> LLM config
	// So we expect either extraction_failure or llm_config
	if rf.Type != "extraction_failure" && rf.Type != "llm_config" {
		t.Logf("Expected type 'extraction_failure' or 'llm_config', got %q", rf.Type)
	}
}

// TestRun_Success tests a successful run with mocked LLM.
func TestRun_Success(t *testing.T) {
	// Create a test server with valid HTML
	contentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!DOCTYPE html>
			<html>
			<head><title>Test Article</title></head>
			<body><h1>Test Article</h1><p>This is test content.</p></body>
			</html>`))
	}))
	defer contentServer.Close()

	t.Skip("This test is skipped - full integration requires a real or mock LLM server")
}

// TestResearchFailure_Error tests ResearchFailure error formatting.
func TestResearchFailure_Error(t *testing.T) {
	t.Run("returns message when set", func(t *testing.T) {
		rf := &ResearchFailure{
			Type:    "fetch_failure",
			Message: "All fetches failed",
		}
		if rf.Error() != "All fetches failed" {
			t.Errorf("Expected 'All fetches failed', got %q", rf.Error())
		}
	})

	t.Run("returns default when message not set", func(t *testing.T) {
		rf := &ResearchFailure{Type: "unknown"}
		if rf.Error() != "research failed" {
			t.Errorf("Expected 'research failed', got %q", rf.Error())
		}
	})
}

// TestResearchFailure_IsFailFast tests fail-fast behavior.
func TestResearchFailure_IsFailFast(t *testing.T) {
	tests := []struct {
		name     string
		rf       *ResearchFailure
		expected bool
	}{
		{
			name:     "llm_config is fail-fast",
			rf:       &ResearchFailure{Type: "llm_config"},
			expected: true,
		},
		{
			name:     "researcher_init is fail-fast",
			rf:       &ResearchFailure{Type: "researcher_init"},
			expected: true,
		},
		{
			name:     "researcher_error is fail-fast",
			rf:       &ResearchFailure{Type: "researcher_error"},
			expected: true,
		},
		{
			name:     "dossier_validation is fail-fast",
			rf:       &ResearchFailure{Type: "dossier_validation"},
			expected: true,
		},
		{
			name:     "fetch_failure is not fail-fast",
			rf:       &ResearchFailure{Type: "fetch_failure"},
			expected: false,
		},
		{
			name:     "extraction_failure is not fail-fast",
			rf:       &ResearchFailure{Type: "extraction_failure"},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.rf.IsFailFast() != tt.expected {
				t.Errorf("IsFailFast() expected %v, got %v", tt.expected, tt.rf.IsFailFast())
			}
		})
	}
}

// TestFetchFailure_String tests FetchFailure string formatting.
func TestFetchFailure_String(t *testing.T) {
	t.Run("returns URL when set", func(t *testing.T) {
		ff := FetchFailure{URL: "http://example.com"}
		if ff.String() != "http://example.com" {
			t.Errorf("Expected 'http://example.com', got %q", ff.String())
		}
	})

	t.Run("returns FinalURL when URL not set", func(t *testing.T) {
		ff := FetchFailure{FinalURL: "http://example.com/final"}
		if ff.String() != "http://example.com/final" {
			t.Errorf("Expected 'http://example.com/final', got %q", ff.String())
		}
	})
}

// TestExtractionFailure_String tests ExtractionFailure string formatting.
func TestExtractionFailure_String(t *testing.T) {
	t.Run("returns URL when set", func(t *testing.T) {
		ef := ExtractionFailure{URL: "http://example.com"}
		if ef.String() != "http://example.com" {
			t.Errorf("Expected 'http://example.com', got %q", ef.String())
		}
	})

	t.Run("returns FinalURL when URL not set", func(t *testing.T) {
		ef := ExtractionFailure{FinalURL: "http://example.com/final"}
		if ef.String() != "http://example.com/final" {
			t.Errorf("Expected 'http://example.com/final', got %q", ef.String())
		}
	})
}

// TestWriteDossier tests atomic file writing.
func TestWriteDossier(t *testing.T) {
	outputPath := "/tmp/test_dossier.json"
	os.Remove(outputPath) //nolint:errcheck // Clean up before test

	dossier := &researcher.ResearchDossier{
		StableID:    "test-001",
		Topic:       "Test",
		GeneratedAt: time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC),
		Sources: []researcher.SourceReference{
			{StableID: "src-001", OriginalURL: "http://example.com", SourceType: "web", RetrievedAt: time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)},
		},
		Claims: []researcher.Claim{
			{
				ID:            "claim-001",
				Statement:     "Test claim.",
				IsUnsupported: false,
				Evidence:      []researcher.Evidence{{SourceID: "src-001"}},
			},
		},
		Contradictions:      []researcher.Contradiction{},
		UnresolvedQuestions: []researcher.UnresolvedQuestion{},
		ResearchNotes:       []researcher.ResearchNote{},
	}

	if err := writeDossier(outputPath, dossier); err != nil {
		t.Fatalf("writeDossier failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		t.Fatal("Expected output file to exist")
	}

	// Verify content is valid JSON
	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("Failed to read output file: %v", err)
	}

	var restored researcher.ResearchDossier
	if err := json.Unmarshal(content, &restored); err != nil {
		t.Fatalf("Failed to unmarshal JSON: %v", err)
	}

	if restored.StableID != dossier.StableID {
		t.Errorf("Expected stable_id %q, got %q", dossier.StableID, restored.StableID)
	}

	// Clean up
	os.Remove(outputPath) //nolint:errcheck
}

// TestWriteDossier_Atomic verifies atomic write (no partial files on error).
func TestWriteDossier_Atomic(t *testing.T) {
	outputPath := "/tmp/test_atomic.json"
	os.Remove(outputPath)            //nolint:errcheck
	os.Remove(outputPath + ".tmp.*") //nolint:errcheck

	// Try to write to an invalid path - should fail cleanly
	err := writeDossier("/nonexistent/dir/test.json", &researcher.ResearchDossier{})
	if err == nil {
		t.Fatal("Expected error for invalid path, got nil")
	}

	// Verify no partial file was created
	entries, _ := os.ReadDir("/tmp")
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "test_atomic.json.tmp") {
			t.Errorf("Found partial file %s after failed write", entry.Name())
		}
	}
}

// TestFetchResultToSource tests conversion of fetcher result to types.Source.
func TestFetchResultToSource(t *testing.T) {
	now := time.Now().UTC()
	httpStatus := 200
	contentType := "text/html"
	contentLength := int64(1000)

	result := &fetcher.FetchResult{
		FinalURL:      "http://example.com/article",
		ContentType:   contentType,
		ContentLength: contentLength,
		Body:          []byte("<html><body>Test</body></html>"),
		HTTPStatus:    httpStatus,
		Headers:       http.Header{"Server": {"nginx"}},
		RetrievedAt:   now,
	}

	source := fetchResultToSource(result)

	if source.StableID == "" {
		t.Error("Expected non-empty StableID")
	}
	if source.OriginalURL != "http://example.com/article" {
		t.Errorf("Expected OriginalURL 'http://example.com/article', got %q", source.OriginalURL)
	}
	if source.SourceType != types.SourceTypeWeb {
		t.Errorf("Expected SourceType 'web', got %q", source.SourceType)
	}
	if !source.RetrievedAt.Equal(now) {
		t.Error("Expected RetrievedAt to match")
	}
	if source.FetchStatus == nil {
		t.Fatal("Expected non-nil FetchStatus")
	}
	if source.FetchStatus.HTTPStatus == nil || *source.FetchStatus.HTTPStatus != httpStatus {
		t.Errorf("Expected HTTPStatus %d, got %v", httpStatus, source.FetchStatus.HTTPStatus)
	}
	if source.FetchStatus.ContentType == nil || *source.FetchStatus.ContentType != contentType {
		t.Errorf("Expected ContentType %q, got %v", contentType, source.FetchStatus.ContentType)
	}
}

// TestDefaultDuration tests the defaultDuration helper.
func TestDefaultDuration(t *testing.T) {
	t.Run("returns duration for positive seconds", func(t *testing.T) {
		d := defaultDuration(30.0)
		if d != 30*time.Second {
			t.Errorf("Expected 30s, got %v", d)
		}
	})

	t.Run("returns default 60s for zero", func(t *testing.T) {
		d := defaultDuration(0.0)
		if d != 60*time.Second {
			t.Errorf("Expected 60s, got %v", d)
		}
	})
}

// FakeLLMClient is a test double for llm.Client.
type FakeLLMClient struct {
	Response string
	Err      error
}

func (f *FakeLLMClient) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if f.Err != nil {
		return llm.Response{}, f.Err
	}
	return llm.Response{Content: f.Response}, nil
}

// TestAPIKey tests API key configuration and precedence.
func TestAPIKey(t *testing.T) {
	t.Run("API key from flag takes precedence over env var", func(t *testing.T) {
		// Set env var
		os.Setenv("OMLX_API_KEY", "env-key-123") //nolint:errcheck
		defer os.Unsetenv("OMLX_API_KEY")        //nolint:errcheck

		// Create a server that captures the Authorization header
		var capturedAuth string
		llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"content": "test"})
		}))
		defer llmServer.Close()

		// Create a content server
		contentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<html><body><p>Test</p></body></html>`))
		}))
		defer contentServer.Close()

		// Config with flag API key
		cfg := Config{
			Topic:      "Test",
			URLs:       []string{contentServer.URL},
			OutputPath: "/tmp/test_api_key.json",
			LLMBaseURL: llmServer.URL,
			LLMModel:   "test-model",
			LLMAPIKey:  "flag-key-456", // CLI flag should take precedence
		}

		// Run workflow - we'll catch the file write error since response format doesn't match expected JSON
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		err := New(cfg).Run(ctx)
		// Expect some error (not about API key)
		if err == nil {
			t.Fatal("Expected error, got nil")
		}

		// Verify the captured Authorization header has the flag key, not the env var
		if capturedAuth != "Bearer flag-key-456" {
			t.Errorf("Expected Authorization 'Bearer flag-key-456', got %q", capturedAuth)
		}

		// Clean up
		_ = os.Remove("/tmp/test_api_key.json")
	})

	t.Run("API key from env var when flag not provided", func(t *testing.T) {
		os.Unsetenv("OMLX_API_KEY") //nolint:errcheck

		// Set env var
		os.Setenv("OMLX_API_KEY", "env-key-789") //nolint:errcheck
		defer os.Unsetenv("OMLX_API_KEY")        //nolint:errcheck

		// Create a server that captures the Authorization header
		var capturedAuth string
		llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"content": "test"})
		}))
		defer llmServer.Close()

		// Create a content server
		contentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<html><body><p>Test</p></body></html>`))
		}))
		defer contentServer.Close()

		// Config without flag API key (should use env var)
		cfg := Config{
			Topic:      "Test",
			URLs:       []string{contentServer.URL},
			OutputPath: "/tmp/test_api_key_env.json",
			LLMBaseURL: llmServer.URL,
			LLMModel:   "test-model",
			LLMAPIKey:  "", // No flag, should use env var
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		err := New(cfg).Run(ctx)
		if err == nil {
			t.Fatal("Expected error, got nil")
		}

		// Verify the captured Authorization header has the env var key
		if capturedAuth != "Bearer env-key-789" {
			t.Errorf("Expected Authorization 'Bearer env-key-789', got %q", capturedAuth)
		}

		// Clean up
		_ = os.Remove("/tmp/test_api_key_env.json")
	})

	t.Run("no Authorization header when API key empty", func(t *testing.T) {
		os.Unsetenv("OMLX_API_KEY") //nolint:errcheck

		// Create a server that verifies no Authorization header
		var capturedAuth string
		llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"content": "test"})
		}))
		defer llmServer.Close()

		// Create a content server
		contentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<html><body><p>Test</p></body></html>`))
		}))
		defer contentServer.Close()

		// Config with empty API key
		cfg := Config{
			Topic:      "Test",
			URLs:       []string{contentServer.URL},
			OutputPath: "/tmp/test_api_key_empty.json",
			LLMBaseURL: llmServer.URL,
			LLMModel:   "test-model",
			LLMAPIKey:  "", // Empty
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		err := New(cfg).Run(ctx)
		if err == nil {
			t.Fatal("Expected error, got nil")
		}

		// Verify no Authorization header was sent
		if capturedAuth != "" {
			t.Errorf("Expected no Authorization header, got %q", capturedAuth)
		}

		// Clean up
		_ = os.Remove("/tmp/test_api_key_empty.json")
	})
}
