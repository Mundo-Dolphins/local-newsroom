package searxng

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/search"
)

// fakeJSONEncoder wraps http.ResponseWriter to ignore JSON encoding errors.
// This is a test helper to avoid errcheck complaints about ignored errors
type fakeJSONEncoder struct {
	http.ResponseWriter
}

func (w *fakeJSONEncoder) Write(data []byte) (int, error) {
	return w.ResponseWriter.Write(data)
}

func (w *fakeJSONEncoder) WriteHeader(statusCode int) {
	w.ResponseWriter.WriteHeader(statusCode)
}

func encodeJSON(w http.ResponseWriter, v interface{}) {
	e := json.NewEncoder(&fakeJSONEncoder{w})
	_ = e.Encode(v) // ignore encoding errors in tests
}

// TestNewProvider_validConfig tests that a valid configuration creates a provider.
func TestNewProvider_validConfig(t *testing.T) {
	config := &Config{
		BaseURL: "http://localhost:8080",
	}

	provider, err := NewProvider(config)
	if err != nil {
		t.Fatalf("NewProvider failed with valid config: %v", err)
	}

	if provider == nil {
		t.Fatal("expected non-nil provider")
	}
}

// TestNewProvider_invalidConfig tests that invalid configurations are rejected.
func TestNewProvider_invalidConfig(t *testing.T) {
	tests := []struct {
		name   string
		config *Config
		check  func(error) bool
	}{
		{
			name:   "empty base URL",
			config: &Config{},
			check: func(err error) bool {
				return err != nil && strings.Contains(err.Error(), "base_url is required")
			},
		},
		{
			name: "invalid URL format",
			config: &Config{
				BaseURL: "not-a-url",
			},
			check: func(err error) bool {
				return err != nil && strings.Contains(err.Error(), "base_url")
			},
		},
		{
			name: "invalid safe_search",
			config: &Config{
				BaseURL:    "http://localhost:8080",
				SafeSearch: 5,
			},
			check: func(err error) bool {
				return err != nil && strings.Contains(err.Error(), "safe_search")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewProvider(tt.config)
			if !tt.check(err) {
				t.Fatalf("expected validation error, got: %v", err)
			}
		})
	}
}

// TestProvider_Search_success tests a successful search request.
func TestProvider_Search_success(t *testing.T) {
	// Create a test server that returns a valid SearXNG JSON response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify method
		if r.Method != http.MethodGet {
			t.Errorf("expected GET request, got: %s", r.Method)
		}

		// Verify query parameters
		q := r.URL.Query()
		if q.Get("q") != "test query" {
			t.Errorf("expected query 'test query', got: %q", q.Get("q"))
		}
		if q.Get("format") != "json" {
			t.Errorf("expected format=json, got: %q", q.Get("format"))
		}

		// Return a valid SearXNG response
		response := SearXNGResponse{
			Results: []SearXNGResult{
				{
					ID:          "1",
					URL:         "https://example.com/article-1",
					Title:       "Test Article 1",
					Content:     "This is the first test article.",
					Engine:      "duckduckgo",
					PublishDate: "2024-01-15T10:30:00Z",
				},
				{
					ID:      "2",
					URL:     "https://example.org/article-2",
					Title:   "Test Article 2",
					Content: "This is the second test article.",
					Engine:  "google",
				},
			},
			Metadata: &SearXNGMetadata{
				Version: "1.0.0",
			},
		}

		w.Header().Set("Content-Type", "application/json")
		encodeJSON(w, response)
	}))
	defer server.Close()

	provider, err := NewProvider(&Config{BaseURL: server.URL + "/"})
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx := context.Background()
	req := search.Request{
		Query:     "test query",
		Language:  "en",
		TimeRange: "last_week",
		Limit:     10,
	}

	resp, err := provider.Search(ctx, req)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if resp.OriginalQuery != "test query" {
		t.Errorf("expected OriginalQuery='test query', got: %q", resp.OriginalQuery)
	}

	if len(resp.Results) != 2 {
		t.Errorf("expected 2 results, got: %d", len(resp.Results))
	}

	// Verify first result
	if resp.Results[0].ID != "1" {
		t.Errorf("expected first result ID '1', got: %q", resp.Results[0].ID)
	}
	if resp.Results[0].URL != "https://example.com/article-1" {
		t.Errorf("expected first result URL, got: %q", resp.Results[0].URL)
	}
	if resp.Results[0].Rank != 1 {
		t.Errorf("expected first result rank 1, got: %d", resp.Results[0].Rank)
	}
	if resp.Results[0].ProviderName != "searxng" {
		t.Errorf("expected ProviderName 'searxng', got: %q", resp.Results[0].ProviderName)
	}
	if resp.Results[0].SourceDomain != "example.com" {
		t.Errorf("expected SourceDomain 'example.com', got: %q", resp.Results[0].SourceDomain)
	}

	// Verify published date was parsed
	if resp.Results[0].PublishedAt == nil {
		t.Error("expected PublishedAt to be parsed")
	} else if !resp.Results[0].PublishedAt.Equal(time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)) {
		t.Errorf("unexpected PublishedAt: %v", *resp.Results[0].PublishedAt)
	}

	// Verify provider info
	if resp.ProviderInfo == nil {
		t.Error("expected ProviderInfo to be set")
	} else if resp.ProviderInfo["engine_version"] != "1.0.0" {
		t.Errorf("unexpected engine_version: %q", resp.ProviderInfo["engine_version"])
	}
}

// TestProvider_Search_withLanguage tests language parameter.
func TestProvider_Search_withLanguage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("language") != "es-ES" {
			t.Errorf("expected language=es-ES, got: %q", q.Get("language"))
		}

		w.Header().Set("Content-Type", "application/json")
		encodeJSON(w, SearXNGResponse{Results: []SearXNGResult{}})
	}))
	defer server.Close()

	provider, err := NewProvider(&Config{BaseURL: server.URL + "/"})
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx := context.Background()
	req := search.Request{
		Query:    "test",
		Language: "es-ES",
	}

	_, err = provider.Search(ctx, req)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
}

// TestProvider_Search_withTimeRange tests time_range parameter.
func TestProvider_Search_withTimeRange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("time_range") != "last_day" {
			t.Errorf("expected time_range=last_day, got: %q", q.Get("time_range"))
		}

		w.Header().Set("Content-Type", "application/json")
		encodeJSON(w, SearXNGResponse{Results: []SearXNGResult{}})
	}))
	defer server.Close()

	provider, err := NewProvider(&Config{BaseURL: server.URL + "/"})
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx := context.Background()
	req := search.Request{
		Query:     "test",
		TimeRange: "last_day",
	}

	_, err = provider.Search(ctx, req)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
}

// TestProvider_Search_withLimit tests limit parameter (pagination).
func TestProvider_Search_withLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		// SearXNG uses pageno for pagination
		if q.Get("pageno") != "1" {
			t.Errorf("expected pageno=1, got: %q", q.Get("pageno"))
		}

		w.Header().Set("Content-Type", "application/json")
		encodeJSON(w, SearXNGResponse{Results: []SearXNGResult{}})
	}))
	defer server.Close()

	provider, err := NewProvider(&Config{BaseURL: server.URL + "/"})
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx := context.Background()
	req := search.Request{
		Query: "test",
		Limit: 10,
	}

	_, err = provider.Search(ctx, req)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
}

// TestProvider_Search_emptyResults tests empty result set.
func TestProvider_Search_emptyResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		encodeJSON(w, SearXNGResponse{Results: []SearXNGResult{}})
	}))
	defer server.Close()

	provider, err := NewProvider(&Config{BaseURL: server.URL + "/"})
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx := context.Background()
	req := search.Request{Query: "no results here"}

	resp, err := provider.Search(ctx, req)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(resp.Results) != 0 {
		t.Errorf("expected 0 results, got: %d", len(resp.Results))
	}
}

// TestProvider_Search_malformedJSON tests malformed JSON response.
func TestProvider_Search_malformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not valid json"))
	}))
	defer server.Close()

	provider, err := NewProvider(&Config{BaseURL: server.URL + "/"})
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx := context.Background()
	req := search.Request{Query: "test"}

	_, err = provider.Search(ctx, req)
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
	if !errors.Is(err, ErrMalformedJSON) {
		t.Errorf("expected ErrMalformedJSON, got: %v", err)
	}
}

// TestProvider_Search_non2xxResponse tests non-2xx HTTP responses.
func TestProvider_Search_non2xxResponse(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		expect403  bool
	}{
		{"400 Bad Request", http.StatusBadRequest, false},
		{"403 Forbidden", http.StatusForbidden, true},
		{"404 Not Found", http.StatusNotFound, false},
		{"500 Internal Server Error", http.StatusInternalServerError, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte("error"))
			}))
			defer server.Close()

			provider, err := NewProvider(&Config{BaseURL: server.URL + "/"})
			if err != nil {
				t.Fatalf("Failed to create provider: %v", err)
			}

			ctx := context.Background()
			req := search.Request{Query: "test"}

			_, err = provider.Search(ctx, req)
			if err == nil {
				t.Fatal("expected error for non-2xx status, got nil")
			}

			searchErr, ok := err.(*SearchError)
			if !ok {
				t.Fatalf("expected *SearchError, got: %T", err)
			}

			if searchErr.Status != tt.statusCode {
				t.Errorf("expected status %d, got: %d", tt.statusCode, searchErr.Status)
			}

			if tt.expect403 {
				if !strings.Contains(searchErr.Message, "JSON output may not be enabled") {
					t.Errorf("403 error should mention JSON output issue, got: %q", searchErr.Message)
				}
			}
		})
	}
}

// TestProvider_Search_contextCancellation tests context cancellation.
func TestProvider_Search_contextCancellation(t *testing.T) {
	// Create a server that never responds
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Don't respond - let the context timeout/cancel
		select {}
	}))
	defer server.Close()

	provider, err := NewProvider(&Config{BaseURL: server.URL + "/"})
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel immediately to simulate a cancelled request
	cancel()

	req := search.Request{Query: "test"}

	_, err = provider.Search(ctx, req)
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

// TestProvider_Search_contextDeadline tests context deadline exceeded.
func TestProvider_Search_contextDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sleep longer than the context deadline
		time.Sleep(10 * time.Second)
	}))
	defer server.Close()

	provider, err := NewProvider(&Config{BaseURL: server.URL + "/"})
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	req := search.Request{Query: "test"}

	_, err = provider.Search(ctx, req)
	if err == nil {
		t.Fatal("expected error for context deadline, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context.DeadlineExceeded, got: %v", err)
	}
}

// TestProvider_Search_responseTooLarge tests response size limit.
func TestProvider_Search_responseTooLarge(t *testing.T) {
	// Create a response larger than MaxResponseSize
	largeContent := strings.Repeat("x", 2*1024*1024) // 2MB

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := SearXNGResponse{
			Results: []SearXNGResult{{
				URL:     "https://example.com",
				Title:   "Test",
				Content: largeContent,
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		encodeJSON(w, response)
	}))
	defer server.Close()

	// Configure with a smaller max response size
	config := &Config{
		BaseURL:         server.URL + "/",
		MaxResponseSize: 1024, // 1KB
	}

	provider, err := NewProvider(config)
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx := context.Background()
	req := search.Request{Query: "test"}

	_, err = provider.Search(ctx, req)
	if err == nil {
		t.Fatal("expected error for oversized response, got nil")
	}
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("expected ErrResponseTooLarge, got: %v", err)
	}
}

// TestProvider_Search_validationError tests validation of request.
func TestProvider_Search_validationError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Should not be reached if validation fails
		t.Error("server should not be called when request validation fails")
	}))
	defer server.Close()

	provider, err := NewProvider(&Config{BaseURL: server.URL + "/"})
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx := context.Background()
	req := search.Request{Query: ""} // Empty query is invalid

	_, err = provider.Search(ctx, req)
	if err == nil {
		t.Fatal("expected validation error for empty query, got nil")
	}
	if !strings.Contains(err.Error(), "query cannot be empty") {
		t.Errorf("expected query validation error, got: %v", err)
	}
}

// TestProvider_Search_queryParameterEncoding tests that query is properly URL-encoded.
func TestProvider_Search_queryParameterEncoding(t *testing.T) {
	capturedURL := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedURL = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		encodeJSON(w, SearXNGResponse{Results: []SearXNGResult{}})
	}))
	defer server.Close()

	provider, err := NewProvider(&Config{BaseURL: server.URL + "/"})
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx := context.Background()

	// Test with special characters that need encoding
	testQueries := []string{
		"hello world",
		"test+query",
		"test&query",
		"test=query",
		"test?query",
	}

	for _, query := range testQueries {
		capturedURL = ""
		req := search.Request{Query: query}
		_, err := provider.Search(ctx, req)
		if err != nil {
			t.Fatalf("Search failed for query %q: %v", query, err)
		}

		// URL should be properly encoded
		if !strings.Contains(capturedURL, "q=") {
			t.Errorf("expected 'q=' parameter in URL for query %q, got: %q", query, capturedURL)
		}
	}
}

// TestProvider_Search_userAgent tests that User-Agent header is set.
func TestProvider_Search_userAgent(t *testing.T) {
	capturedUserAgent := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUserAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		encodeJSON(w, SearXNGResponse{Results: []SearXNGResult{}})
	}))
	defer server.Close()

	provider, err := NewProvider(&Config{BaseURL: server.URL + "/"})
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx := context.Background()
	req := search.Request{Query: "test"}

	_, err = provider.Search(ctx, req)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if capturedUserAgent == "" {
		t.Error("expected User-Agent header to be set")
	}
}

// TestProvider_Search_apiKey tests API key authentication.
func TestProvider_Search_apiKey(t *testing.T) {
	capturedAPIKey := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAPIKey = r.Header.Get("X-API-Key")
		w.Header().Set("Content-Type", "application/json")
		encodeJSON(w, SearXNGResponse{Results: []SearXNGResult{}})
	}))
	defer server.Close()

	config := &Config{
		BaseURL: server.URL + "/",
		APIKey:  "secret-api-key-123",
	}

	provider, err := NewProvider(config)
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx := context.Background()
	req := search.Request{Query: "test"}

	_, err = provider.Search(ctx, req)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if capturedAPIKey != "secret-api-key-123" {
		t.Errorf("expected API key 'secret-api-key-123', got: %q", capturedAPIKey)
	}
}

// TestProvider_Search_customAPIHeader tests custom API header name.
func TestProvider_Search_customAPIHeader(t *testing.T) {
	capturedHeader := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		encodeJSON(w, SearXNGResponse{Results: []SearXNGResult{}})
	}))
	defer server.Close()

	config := &Config{
		BaseURL:   server.URL + "/",
		APIKey:    "bearer-token",
		APIHeader: "Authorization",
	}

	provider, err := NewProvider(config)
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx := context.Background()
	req := search.Request{Query: "test"}

	_, err = provider.Search(ctx, req)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if capturedHeader != "bearer-token" {
		t.Errorf("expected header 'bearer-token', got: %q", capturedHeader)
	}
}

// TestProvider_Search_preservesOrder tests that result order is preserved.
func TestProvider_Search_preservesOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := SearXNGResponse{
			Results: []SearXNGResult{
				{ID: "1", URL: "https://a.com", Title: "First"},
				{ID: "2", URL: "https://b.com", Title: "Second"},
				{ID: "3", URL: "https://c.com", Title: "Third"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		encodeJSON(w, response)
	}))
	defer server.Close()

	provider, err := NewProvider(&Config{BaseURL: server.URL + "/"})
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx := context.Background()
	req := search.Request{Query: "test"}

	resp, err := provider.Search(ctx, req)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(resp.Results) != 3 {
		t.Fatalf("expected 3 results, got: %d", len(resp.Results))
	}

	if resp.Results[0].ID != "1" {
		t.Errorf("expected first result ID '1', got: %q", resp.Results[0].ID)
	}
	if resp.Results[1].ID != "2" {
		t.Errorf("expected second result ID '2', got: %q", resp.Results[1].ID)
	}
	if resp.Results[2].ID != "3" {
		t.Errorf("expected third result ID '3', got: %q", resp.Results[2].ID)
	}

	// Verify ranks are sequential
	for i, result := range resp.Results {
		if result.Rank != i+1 {
			t.Errorf("expected rank %d, got: %d", i+1, result.Rank)
		}
	}
}

// TestProvider_Search_defaultTimeout tests default HTTP timeout.
func TestProvider_Search_defaultTimeout(t *testing.T) {
	// Just verify that the client is created with default timeout
	config := &Config{
		BaseURL: "http://localhost:8080",
	}

	provider, err := NewProvider(config)
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	if provider.client == nil {
		t.Fatal("expected non-nil client")
	}

	// Timeout should be set (default 30s)
	if provider.client.Timeout == 0 {
		t.Error("expected client timeout to be set")
	}
}

// TestProvider_Search_customUserAgent tests custom User-Agent.
func TestProvider_Search_customUserAgent(t *testing.T) {
	capturedUserAgent := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUserAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		encodeJSON(w, SearXNGResponse{Results: []SearXNGResult{}})
	}))
	defer server.Close()

	config := &Config{
		BaseURL:   server.URL + "/",
		UserAgent: "CustomBot/1.0",
	}

	provider, err := NewProvider(config)
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx := context.Background()
	req := search.Request{Query: "test"}

	_, err = provider.Search(ctx, req)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if capturedUserAgent != "CustomBot/1.0" {
		t.Errorf("expected User-Agent 'CustomBot/1.0', got: %q", capturedUserAgent)
	}
}

// TestProvider_Search_baseURLTrailingSlash tests base URL normalization.
func TestProvider_Search_baseURLTrailingSlash(t *testing.T) {
	capturedPath := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		encodeJSON(w, SearXNGResponse{Results: []SearXNGResult{}})
	}))
	defer server.Close()

	// Test with trailing slash
	provider, err := NewProvider(&Config{BaseURL: server.URL + "/"})
	if err != nil {
		t.Fatalf("Failed to create provider with trailing slash: %v", err)
	}

	ctx := context.Background()
	req := search.Request{Query: "test"}
	_, err = provider.Search(ctx, req)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if !strings.Contains(capturedPath, "/search") {
		t.Errorf("expected path to contain '/search', got: %q", capturedPath)
	}
}

// TestProvider_Search_limitMinValue tests minimum limit value.
func TestProvider_Search_limitMinValue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		encodeJSON(w, SearXNGResponse{Results: []SearXNGResult{}})
	}))
	defer server.Close()

	provider, err := NewProvider(&Config{BaseURL: server.URL + "/"})
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx := context.Background()

	tests := []struct {
		limit int
		name  string
	}{
		{0, "zero limit"},
		{-1, "negative limit"},
		{1, "minimum valid limit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := search.Request{Query: "test", Limit: tt.limit}
			_, err := provider.Search(ctx, req)
			if err != nil {
				t.Fatalf("Search failed for %s: %v", tt.name, err)
			}
		})
	}
}

// TestProvider_Search_safesearchParam tests safesearch parameter.
func TestProvider_Search_safesearchParam(t *testing.T) {
	tests := []struct {
		name        string
		safeSearch  int
		expectParam bool
	}{
		{"safe search off", 0, true},
		{"safe search moderate", 1, true},
		{"safe search strict", 2, true},
		{"default (not set)", -1, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				hasParam := q.Has("safesearch")
				if tt.expectParam {
					if !hasParam {
						t.Errorf("expected safesearch parameter")
					} else {
						expected := strconv.Itoa(tt.safeSearch)
						if q.Get("safesearch") != expected {
							t.Errorf("expected safesearch=%s, got: %s", expected, q.Get("safesearch"))
						}
					}
				} else {
					if hasParam {
						t.Error("expected no safesearch parameter")
					}
				}

				w.Header().Set("Content-Type", "application/json")
				encodeJSON(w, SearXNGResponse{Results: []SearXNGResult{}})
			}))
			defer server.Close()

			config := &Config{
				BaseURL:    server.URL + "/",
				SafeSearch: tt.safeSearch,
			}

			provider, err := NewProvider(config)
			if err != nil {
				t.Fatalf("Failed to create provider: %v", err)
			}

			ctx := context.Background()
			req := search.Request{Query: "test"}

			_, err = provider.Search(ctx, req)
			if err != nil {
				t.Fatalf("Search failed: %v", err)
			}
		})
	}
}

// BenchmarkProvider_Search benchmarks the search performance.
func BenchmarkProvider_Search(b *testing.B) {
	// This benchmark uses a minimal mock response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		encodeJSON(w, SearXNGResponse{
			Results: []SearXNGResult{
				{ID: "1", URL: "https://example.com", Title: "Test", Content: "Test content"},
			},
		})
	}))
	defer server.Close()

	provider, err := NewProvider(&Config{BaseURL: server.URL + "/"})
	if err != nil {
		b.Fatalf("Failed to create provider: %v", err)
	}

	ctx := context.Background()
	req := search.Request{Query: "benchmark query"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := provider.Search(ctx, req)
		if err != nil {
			b.Fatalf("Search failed: %v", err)
		}
	}
}

// TestConvertResult_variousFields tests conversion of various SearXNG result fields.
func TestConvertResult_variousFields(t *testing.T) {
	tests := []struct {
		name   string
		result SearXNGResult
		check  func(search.SearchResult) bool
	}{
		{
			name: "full result",
			result: SearXNGResult{
				ID:          "1",
				URL:         "https://example.com/article",
				Title:       "Test Title",
				Content:     "Test content",
				PublishDate: "2024-01-15T10:30:00Z",
			},
			check: func(r search.SearchResult) bool {
				return r.Title != nil &&
					*r.Title == "Test Title" &&
					r.Snippet != nil &&
					*r.Snippet == "Test content" &&
					r.SourceDomain == "example.com"
			},
		},
		{
			name: "empty title",
			result: SearXNGResult{
				ID:    "1",
				URL:   "https://example.com",
				Title: "",
			},
			check: func(r search.SearchResult) bool {
				return r.Title == nil && r.Snippet == nil
			},
		},
		{
			name: "with source domain from URL",
			result: SearXNGResult{
				ID:    "1",
				URL:   "https://subdomain.example.com/path",
				Title: "Test",
			},
			check: func(r search.SearchResult) bool {
				return r.SourceDomain == "subdomain.example.com"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertResult(tt.result, 1)
			if !tt.check(result) {
				t.Errorf("conversion failed: %+v", result)
			}
		})
	}
}

// TestParseTime_variousFormats tests parsing various time formats.
func TestParseTime_variousFormats(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		expectErr bool
	}{
		{
			name:      "RFC3339",
			input:     "2024-01-15T10:30:00Z",
			expectErr: false,
		},
		{
			name:      "RFC3339Nano",
			input:     "2024-01-15T10:30:00.123456789Z",
			expectErr: false,
		},
		{
			name:      "date only",
			input:     "2024-01-15",
			expectErr: false,
		},
		{
			name:      "invalid format",
			input:     "not a date",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseTime(tt.input)
			if (err != nil) != tt.expectErr {
				t.Errorf("expected error %v, got: %v", tt.expectErr, err)
			}
		})
	}
}

// TestSearchError_ErrorFormat tests SearchError error format.
func TestSearchError_ErrorFormat(t *testing.T) {
	err := &SearchError{
		Status:  403,
		Message: "test error message",
	}

	errStr := err.Error()
	if !strings.HasPrefix(errStr, "search error (HTTP 403):") {
		t.Errorf("unexpected error format: %q", errStr)
	}
	if !strings.Contains(errStr, "test error message") {
		t.Errorf("error message not included: %q", errStr)
	}
}

// TestSearchError_Is tests SearchError.Is method.
func TestSearchError_Is(t *testing.T) {
	targetErr := &SearchError{Status: 500, Message: "test"}

	tests := []struct {
		name  string
		err   error
		isErr bool
	}{
		{"same type", targetErr, true},
		{"different type", errors.New("not a SearchError"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var searchErr *SearchError
			asOk := errors.As(tt.err, &searchErr)
			if asOk != tt.isErr {
				t.Errorf("expected errors.As to return %v, got: %v", tt.isErr, asOk)
			}
		})
	}
}

// TestProvider_JSONSerialization verifies that response can be serialized.
func TestProvider_JSONSerialization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := SearXNGResponse{
			Results: []SearXNGResult{
				{
					ID:      "1",
					URL:     "https://example.com",
					Title:   "Test Article",
					Content: "Test content.",
				},
			},
			Metadata: &SearXNGMetadata{
				Version: "1.0.0",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		encodeJSON(w, response)
	}))
	defer server.Close()

	provider, err := NewProvider(&Config{BaseURL: server.URL + "/"})
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx := context.Background()
	req := search.Request{Query: "test"}

	resp, err := provider.Search(ctx, req)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	// Verify JSON serialization works
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Failed to marshal response: %v", err)
	}

	// Verify it can be unmarshaled back
	var resp2 search.Response
	if err := json.Unmarshal(data, &resp2); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if len(resp2.Results) != 1 {
		t.Fatalf("expected 1 result after round-trip, got: %d", len(resp2.Results))
	}
}
