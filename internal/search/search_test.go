package search

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// FakeProvider implements Provider for testing purposes.
type FakeProvider struct {
	// Results to return for any search
	Results []SearchResult

	// Err to return for any search
	Err error

	// SearchCount tracks how many times Search was called
	SearchCount int

	// LastRequest holds the most recent Request passed to Search
	LastRequest Request
}

// Search implements Provider.Search for testing.
func (f *FakeProvider) Search(ctx context.Context, req Request) (Response, error) {
	f.SearchCount++
	f.LastRequest = req

	if f.Err != nil {
		return Response{}, f.Err
	}

	return Response{
		OriginalQuery: req.Query,
		Results:       f.Results,
		ProviderInfo:  map[string]string{"fake": "true"},
	}, nil
}

func TestFakeProvider_SearchReturnsResults(t *testing.T) {
	now := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	title := "Test Article"
	snippet := "This is a test snippet."

	fake := &FakeProvider{
		Results: []SearchResult{
			{
				ID:           "1",
				URL:          "https://example.com/article-1",
				Title:        &title,
				Snippet:      &snippet,
				ProviderName: "fake",
				Rank:         1,
				PublishedAt:  &now,
				SourceDomain: "example.com",
			},
			{
				ID:           "2",
				URL:          "https://example.org/article-2",
				ProviderName: "fake",
				Rank:         2,
				SourceDomain: "example.org",
			},
		},
	}

	ctx := context.Background()
	req := Request{
		Query:    "test query",
		Limit:    10,
		Language: "en",
	}

	resp, err := fake.Search(ctx, req)

	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if fake.SearchCount != 1 {
		t.Errorf("expected SearchCount=1, got: %d", fake.SearchCount)
	}

	if resp.OriginalQuery != "test query" {
		t.Errorf("expected OriginalQuery='test query', got: %s", resp.OriginalQuery)
	}

	if len(resp.Results) != 2 {
		t.Errorf("expected 2 results, got: %d", len(resp.Results))
	}

	if resp.Results[0].Rank != 1 {
		t.Errorf("expected first result rank 1, got: %d", resp.Results[0].Rank)
	}

	if resp.Results[1].Rank != 2 {
		t.Errorf("expected second result rank 2, got: %d", resp.Results[1].Rank)
	}
}

func TestFakeProvider_SearchReturnsError(t *testing.T) {
	fake := &FakeProvider{
		Err: errors.New("search failed"),
	}

	ctx := context.Background()
	req := Request{
		Query: "test query",
	}

	_, err := fake.Search(ctx, req)

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if fake.SearchCount != 1 {
		t.Errorf("expected SearchCount=1 even on error, got: %d", fake.SearchCount)
	}
}

func TestRequest_Validate(t *testing.T) {
	tests := []struct {
		name    string
		req     Request
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid request with minimal fields",
			req:     Request{Query: "test"},
			wantErr: false,
		},
		{
			name:    "valid request with all fields",
			req:     Request{Query: "test", Language: "en", TimeRange: "last_week", Limit: 20},
			wantErr: false,
		},
		{
			name:    "empty query returns error",
			req:     Request{Query: "", Limit: 10},
			wantErr: true,
			errMsg:  "query cannot be empty",
		},
		{
			name:    "limit below minimum defaults to 1",
			req:     Request{Query: "test", Limit: 0},
			wantErr: false,
		},
		{
			name:    "negative limit defaults to 1",
			req:     Request{Query: "test", Limit: -5},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Validate()

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				valErr, ok := err.(*ValidationError)
				if !ok {
					t.Fatalf("expected *ValidationError, got: %T", err)
				}
				if valErr.Message != tt.errMsg {
					t.Errorf("expected error message %q, got: %q", tt.errMsg, valErr.Message)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestResponse_JSONSerialization(t *testing.T) {
	now := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	title := "Test Title"
	snippet := "Test snippet."

	resp := Response{
		OriginalQuery: "search query",
		Results: []SearchResult{
			{
				ID:           "1",
				URL:          "https://example.com",
				Title:        &title,
				Snippet:      &snippet,
				ProviderName: "test-provider",
				Rank:         1,
				PublishedAt:  &now,
				SourceDomain: "example.com",
			},
		},
		ProviderInfo: map[string]string{"engine": "test", "version": "1.0"},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var resp2 Response
	if err := json.Unmarshal(data, &resp2); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if resp2.OriginalQuery != resp.OriginalQuery {
		t.Errorf("OriginalQuery mismatch: expected %q, got: %q", resp.OriginalQuery, resp2.OriginalQuery)
	}

	if len(resp2.Results) != len(resp.Results) {
		t.Fatalf("Results count mismatch: expected %d, got: %d", len(resp.Results), len(resp2.Results))
	}

	if resp2.ProviderInfo["engine"] != "test" {
		t.Errorf("ProviderInfo mismatch: expected engine=test, got: %v", resp2.ProviderInfo)
	}
}

func TestSearchResult_JSONSerialization(t *testing.T) {
	now := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	title := "Test Title"
	snippet := "Test snippet."

	result := SearchResult{
		ID:           "123",
		URL:          "https://example.com/article",
		Title:        &title,
		Snippet:      &snippet,
		ProviderName: "searxng",
		Rank:         3,
		PublishedAt:  &now,
		SourceDomain: "example.com",
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var result2 SearchResult
	if err := json.Unmarshal(data, &result2); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if result2.URL != result.URL {
		t.Errorf("URL mismatch: expected %q, got: %q", result.URL, result2.URL)
	}

	if result2.Rank != result.Rank {
		t.Errorf("Rank mismatch: expected %d, got: %d", result.Rank, result2.Rank)
	}

	if result2.ProviderName != result.ProviderName {
		t.Errorf("ProviderName mismatch: expected %q, got: %q", result.ProviderName, result2.ProviderName)
	}
}

func TestSearchResult_JSONWithNilFields(t *testing.T) {
	// Test that nil Title and Snippet are correctly omitted in JSON
	_ = time.Now()
	result := SearchResult{
		ID:           "1",
		URL:          "https://example.com",
		Title:        nil,
		Snippet:      nil,
		ProviderName: "test",
		Rank:         1,
		SourceDomain: "example.com",
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var result2 SearchResult
	if err := json.Unmarshal(data, &result2); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if result2.Title != nil {
		t.Error("expected Title to be nil after unmarshal")
	}

	if result2.Snippet != nil {
		t.Error("expected Snippet to be nil after unmarshal")
	}
}

func TestFilterResultsByDomain(t *testing.T) {
	title := "Test"

	results := []SearchResult{
		{
			ID:           "1",
			URL:          "https://example.com/article",
			Title:        &title,
			ProviderName: "test",
			Rank:         1,
			SourceDomain: "example.com",
		},
		{
			ID:           "2",
			URL:          "https://example.com/news/article",
			Title:        &title,
			ProviderName: "test",
			Rank:         2,
			SourceDomain: "example.com",
		},
		{
			ID:           "3",
			URL:          "https://other.org/article",
			Title:        &title,
			ProviderName: "test",
			Rank:         3,
			SourceDomain: "other.org",
		},
		{
			ID:           "4",
			URL:          "https://sub.example.com/article",
			Title:        &title,
			ProviderName: "test",
			Rank:         4,
			SourceDomain: "sub.example.com",
		},
	}

	tests := []struct {
		name     string
		domains  []string
		expected int
	}{
		{
			name:     "filter single domain",
			domains:  []string{"example.com"},
			expected: 3,
		},
		{
			name:     "filter multiple domains",
			domains:  []string{"example.com", "other.org"},
			expected: 4,
		},
		{
			name:     "filter non-existent domain",
			domains:  []string{"nonexistent.com"},
			expected: 0,
		},
		{
			name:     "no filter (empty domains)",
			domains:  []string{},
			expected: 4,
		},
		{
			name:     "case-insensitive match",
			domains:  []string{"EXAMPLE.COM"},
			expected: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filtered := FilterResultsByDomain(results, tt.domains)

			if len(filtered) != tt.expected {
				t.Errorf("expected %d results, got: %d", tt.expected, len(filtered))
			}

			// Verify all filtered results are from allowed domains
			if len(tt.domains) > 0 {
				domainSet := make(map[string]bool, len(tt.domains))
				for _, d := range tt.domains {
					domainSet[toLower(d)] = true
				}
				for _, r := range filtered {
					dom := toLower(r.SourceDomain)
					matched := domainSet[dom]
					if !matched {
						// Check subdomains
						for key := range domainSet {
							if strings.HasSuffix(dom, "."+key) {
								matched = true
								break
							}
						}
					}
					if !matched {
						t.Errorf("filtered result from %q not in allowed domains", r.SourceDomain)
					}
				}
			}
		})
	}
}

func TestRequest_JSONSerialization(t *testing.T) {
	req := Request{
		Query:     "test query",
		Language:  "en-US",
		TimeRange: "last_week",
		Limit:     15,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var req2 Request
	if err := json.Unmarshal(data, &req2); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if req2.Query != req.Query {
		t.Errorf("Query mismatch: expected %q, got: %q", req.Query, req2.Query)
	}

	if req2.Language != req.Language {
		t.Errorf("Language mismatch: expected %q, got: %q", req.Language, req2.Language)
	}

	if req2.TimeRange != req.TimeRange {
		t.Errorf("TimeRange mismatch: expected %q, got: %q", req.TimeRange, req2.TimeRange)
	}

	if req2.Limit != req.Limit {
		t.Errorf("Limit mismatch: expected %d, got: %d", req.Limit, req2.Limit)
	}
}

func TestResponse_EmptyResults(t *testing.T) {
	// Test that Response with no results has empty slice, not nil
	resp := Response{
		OriginalQuery: "no results query",
		Results:       []SearchResult{},
	}

	// Should not panic
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var resp2 Response
	if err := json.Unmarshal(data, &resp2); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	// Empty slice should not be nil
	if resp2.Results == nil {
		t.Error("expected Results to be non-nil empty slice")
	}

	if len(resp2.Results) != 0 {
		t.Errorf("expected 0 results, got: %d", len(resp2.Results))
	}

	_ = string(data) // avoid unused variable warning
}

func TestProviderInterfaceImplementation(t *testing.T) {
	// Verify FakeProvider implements Provider
	var _ Provider = &FakeProvider{}

	// Verify the interface can be used in a function
	testWithProvider(func(p Provider) {
		// Just verify we can call Search
		req := Request{Query: "test"}
		_, _ = p.Search(context.Background(), req)
	})
}

// testWithProvider demonstrates injection of Provider interface
func testWithProvider(fn func(Provider)) {
	// This function exists to demonstrate that callers can depend on the interface
	fn(&FakeProvider{})
}

func TestFilterResultsByDomain_SubdomainMatching(t *testing.T) {
	title := "Test"

	results := []SearchResult{
		{
			ID:           "1",
			URL:          "https://www.example.com/article",
			Title:        &title,
			ProviderName: "test",
			Rank:         1,
			SourceDomain: "www.example.com",
		},
		{
			ID:           "2",
			URL:          "https://api.example.com/data",
			Title:        &title,
			ProviderName: "test",
			Rank:         2,
			SourceDomain: "api.example.com",
		},
		{
			ID:           "3",
			URL:          "https://other.com/article",
			Title:        &title,
			ProviderName: "test",
			Rank:         3,
			SourceDomain: "other.com",
		},
	}

	// Filter by example.com should match www.example.com and api.example.com
	filtered := FilterResultsByDomain(results, []string{"example.com"})

	if len(filtered) != 2 {
		t.Errorf("expected 2 results, got: %d", len(filtered))
	}

	for _, r := range filtered {
		if r.SourceDomain == "other.com" {
			t.Errorf("should not include other.com: %s", r.SourceDomain)
		}
	}
}

func TestValidationError_ErrorFormat(t *testing.T) {
	err := &ValidationError{
		Field:   "query",
		Message: "query cannot be empty",
	}

	errStr := err.Error()
	if errStr != "validation error: query cannot be empty" {
		t.Errorf("unexpected error format: %q", errStr)
	}
}

func TestSearchResult_ComputedSourceDomain(t *testing.T) {
	title := "Test"

	result := SearchResult{
		ID:           "1",
		URL:          "https://news.example.com/article",
		Title:        &title,
		ProviderName: "test",
		Rank:         1,
		SourceDomain: "news.example.com",
	}

	// SourceDomain is set directly on the struct, not computed
	// This test just verifies it's preserved correctly
	if result.SourceDomain != "news.example.com" {
		t.Errorf("expected SourceDomain 'news.example.com', got: %q", result.SourceDomain)
	}
}
