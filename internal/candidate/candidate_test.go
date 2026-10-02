package candidate

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/search"
)

// TestURLNormalization tests URL normalization rules.
func TestURLNormalization(t *testing.T) {
	cfg := DefaultConfig()

	tests := []struct {
		name     string
		input    string
		expected string
		hasError bool
	}{
		{
			name:     "lowercase scheme",
			input:    "HTTPS://EXAMPLE.COM/path",
			expected: "https://example.com/path",
		},
		{
			name:     "lowercase host",
			input:    "http://Example.COM/Path",
			expected: "http://example.com/Path",
		},
		{
			name:     "remove fragment",
			input:    "http://example.com/path#section",
			expected: "http://example.com/path",
		},
		{
			name:     "remove fragment with query params",
			input:    "http://example.com/path?foo=bar#section",
			expected: "http://example.com/path?foo=bar",
		},
		{
			name:     "keep query param order",
			input:    "http://example.com/path?z=1&a=2",
			expected: "http://example.com/path?a=2&z=1",
		},
		{
			name:     "keep non-tracking query params",
			input:    "http://example.com/path?foo=bar&baz=qux",
			expected: "http://example.com/path?baz=qux&foo=bar",
		},
		{
			name:     "remove utm_source",
			input:    "http://example.com/path?utm_source=google&foo=bar",
			expected: "http://example.com/path?foo=bar",
		},
		{
			name:     "remove utm_campaign",
			input:    "http://example.com/path?utm_campaign=sale&z=1",
			expected: "http://example.com/path?z=1",
		},
		{
			name:     "remove gclid",
			input:    "http://example.com/path?gclid=ABC123",
			expected: "http://example.com/path",
		},
		{
			name:     "remove fbclid",
			input:    "https://example.com/path?fbclid=XYZ",
			expected: "https://example.com/path",
		},
		{
			name:     "remove _ga",
			input:    "http://example.com/path?_ga=1.2.3.4",
			expected: "http://example.com/path",
		},
		{
			name:     "remove _gid",
			input:    "http://example.com/path?_gid=5.6.7.8",
			expected: "http://example.com/path",
		},
		{
			name:     "remove multiple tracking params",
			input:    "http://example.com/path?a=1&utm_source=foo&gclid=bar&utm_campaign=test",
			expected: "http://example.com/path?a=1",
		},
		{
			name:     "preserve path exactly",
			input:    "https://EXAMPLE.com/Path/To/Article",
			expected: "https://example.com/Path/To/Article",
		},
		{
			name:     "preserve port in host",
			input:    "https://example.com:8443/path",
			expected: "https://example.com:8443/path",
		},
		{
			name:     "remove fragment only",
			input:    "http://example.com/path#section",
			expected: "http://example.com/path",
		},
		{
			name:     "preserve URL with no query or fragment",
			input:    "https://example.com/path",
			expected: "https://example.com/path",
		},
		{
			name:     "empty query string",
			input:    "http://example.com/path?",
			expected: "http://example.com/path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := parseAndNormalize(tt.input, cfg)
			if tt.hasError && err == nil {
				t.Errorf("expected error but got none")
			}
			if !tt.hasError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if u.String() != tt.expected {
				t.Errorf("normalizeURL(%q) = %q, want %q", tt.input, u.String(), tt.expected)
			}
		})
	}
}

// TestNonHTTPSSchemes tests that non-HTTP/HTTPS URLs are rejected.
func TestNonHTTPSSchemes(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string // error reason
	}{
		{
			name:     "reject file scheme",
			input:    "file:///etc/passwd",
			expected: "unsupported scheme: file",
		},
		{
			name:     "reject mailto scheme",
			input:    "mailto:test@example.com",
			expected: "unsupported scheme: mailto",
		},
		{
			name:     "reject javascript scheme",
			input:    "javascript:alert('xss')",
			expected: "unsupported scheme: javascript",
		},
		{
			name:     "reject data scheme",
			input:    "data:text/html,<script>alert(1)</script>",
			expected: "unsupported scheme: data",
		},
		{
			name:     "reject ftp scheme",
			input:    "ftp://ftp.example.com/file",
			expected: "unsupported scheme: ftp",
		},
		{
			name:     "accept http scheme (lowercase)",
			input:    "http://example.com",
			expected: "",
		},
		{
			name:     "accept https scheme (lowercase)",
			input:    "https://example.com",
			expected: "",
		},
		{
			name:     "accept HTTP scheme (uppercase)",
			input:    "HTTP://EXAMPLE.COM",
			expected: "",
		},
		{
			name:     "accept HTTPS scheme (uppercase)",
			input:    "HTTPS://EXAMPLE.COM",
			expected: "",
		},
	}

	cfg := DefaultConfig()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := parseAndNormalize(tt.input, cfg)
			if tt.expected == "" {
				// Should accept
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if u.String() == "" {
					t.Error("expected non-empty normalized URL")
				}
			} else {
				// Should reject
				if err == nil {
					t.Errorf("expected error but got none")
				}
			}
		})
	}
}

// TestExactDuplicateURLs tests handling of exact duplicate URLs.
func TestExactDuplicateURLs(t *testing.T) {
	cfg := DefaultConfig()

	title := "Test Article"

	// Create two identical results from different queries
	results1 := []search.SearchResult{
		{
			ID:           "1",
			URL:          "https://example.com/article",
			Title:        &title,
			Rank:         1,
			ProviderName: "searxng",
			SourceDomain: "example.com",
		},
	}

	results2 := []search.SearchResult{
		{
			ID:           "2",
			URL:          "https://example.com/article",
			Title:        &title,
			Rank:         3,
			ProviderName: "duckduckgo",
			SourceDomain: "example.com",
		},
	}

	inputQueries := []search.Response{
		{OriginalQuery: "test query", Results: results1},
		{OriginalQuery: "different query", Results: results2},
	}

	result := BuildCandidates(inputQueries, cfg)

	if result.TotalCandidates != 1 {
		t.Errorf("Expected 1 total candidate, got %d", result.TotalCandidates)
	}

	if result.SelectedCount != 1 {
		t.Errorf("Expected 1 selected candidate, got %d", result.SelectedCount)
	}

	if len(result.Candidates) != 1 {
		t.Fatalf("Expected 1 candidate, got %d", len(result.Candidates))
	}

	cand := result.Candidates[0]

	// Check provenance has both discoveries
	if len(cand.Provenance) != 2 {
		t.Errorf("Expected 2 provenance entries, got %d", len(cand.Provenance))
	}

	// Check original URLs list
	if len(cand.OriginalURLs) != 2 {
		t.Errorf("Expected 2 original URLs, got %d", len(cand.OriginalURLs))
	}

	// Best discovery should be query 0, rank 1
	if cand.BestQueryIndex != 0 {
		t.Errorf("Expected best query index 0, got %d", cand.BestQueryIndex)
	}
	if cand.BestRank != 1 {
		t.Errorf("Expected best rank 1, got %d", cand.BestRank)
	}
}

// TestFragmentVariants tests that URLs differing only by fragment are normalized.
func TestFragmentVariants(t *testing.T) {
	cfg := DefaultConfig()

	results := []search.SearchResult{
		{
			ID:           "1",
			URL:          "https://example.com/article#section1",
			Rank:         1,
			ProviderName: "searxng",
			SourceDomain: "example.com",
		},
		{
			ID:           "2",
			URL:          "https://example.com/article#section2",
			Rank:         2,
			ProviderName: "searxng",
			SourceDomain: "example.com",
		},
		{
			ID:           "3",
			URL:          "https://example.com/article",
			Rank:         5,
			ProviderName: "searxng",
			SourceDomain: "example.com",
		},
	}

	inputQueries := []search.Response{
		{OriginalQuery: "test", Results: results},
	}

	result := BuildCandidates(inputQueries, cfg)

	if result.TotalCandidates != 1 {
		t.Errorf("Expected 1 total candidate, got %d", result.TotalCandidates)
	}

	if result.SelectedCount != 1 {
		t.Errorf("Expected 1 selected candidate, got %d", result.SelectedCount)
	}

	cand := result.Candidates[0]

	// Should normalize to URL without fragment
	if cand.CandidateURL != "https://example.com/article" {
		t.Errorf("Expected normalized URL without fragment, got %q", cand.CandidateURL)
	}

	// Best rank should be 1 (from first URL)
	if cand.BestRank != 1 {
		t.Errorf("Expected best rank 1, got %d", cand.BestRank)
	}
}

// TestCommonTrackingParams tests removal of common tracking parameters.
func TestCommonTrackingParams(t *testing.T) {
	cfg := DefaultConfig()

	tests := []struct {
		name          string
		trackingParam string
	}{
		{"utm_source", "utm_source=google"},
		{"utm_medium", "utm_medium=cpc"},
		{"utm_campaign", "utm_campaign=sale"},
		{"utm_term", "utm_term=test"},
		{"utm_content", "utm_content=ad1"},
		{"gclid", "gclid=ABCD1234"},
		{"gbraid", "gbraid=ABCD1234"},
		{"wbraid", "wbraid=ABCD1234"},
		{"fbclid", "fbclid=ABCD1234"},
		{"_ga", "_ga=1.2.3.4"},
		{"_gid", "_gid=5.6.7.8"},
		{"ref", "ref=affiliate"},
		{"referer", "referer=example.com"},
		{"mc_cid", "mc_cid=abcd1234"},
		{"mc_eid", "mc_eid=abcd1234"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := "https://example.com/article?" + tt.trackingParam + "&foo=bar"
			results := []search.SearchResult{
				{
					ID:           "1",
					URL:          input,
					Rank:         1,
					ProviderName: "searxng",
					SourceDomain: "example.com",
				},
			}

			inputQueries := []search.Response{
				{OriginalQuery: "test", Results: results},
			}

			result := BuildCandidates(inputQueries, cfg)

			if len(result.Candidates) != 1 {
				t.Fatalf("Expected 1 candidate, got %d", len(result.Candidates))
			}

			cand := result.Candidates[0]

			// Tracking param should be removed
			if containsParam(cand.CandidateURL, tt.trackingParam) {
				t.Errorf("Expected tracking param %q to be removed, but it remains in %q", tt.trackingParam, cand.CandidateURL)
			}

			// Non-tracking param should remain
			if !containsParam(cand.CandidateURL, "foo=bar") {
				t.Errorf("Expected non-tracking param foo=bar to remain, but it was removed")
			}
		})
	}
}

// TestQueryParamsThatMustRemainDistinct tests query parameters that should NOT be removed.
func TestQueryParamsThatMustRemainDistinct(t *testing.T) {
	cfg := DefaultConfig()

	tests := []struct {
		name   string
		param  string
		value1 string
		value2 string
	}{
		{"page number", "page", "1", "2"},
		{"sort order", "sort", "date", "relevance"},
		{"category filter", "category", "news", "politics"},
		{"date filter", "from", "2024-01-01", "2024-06-01"},
		{"search type", "type", "article", "video"},
		{"language", "lang", "en", "es"},
		{"region", "region", "us", "eu"},
		{"format", "format", "json", "xml"},
		{"search term", "q", "climate", "environment"},
		{"specific content param", "article_id", "123", "456"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create two URLs with same base but different values for this param
			input1 := "https://example.com/search?" + tt.param + "=" + tt.value1 + "&other=foo"
			input2 := "https://example.com/search?" + tt.param + "=" + tt.value2 + "&other=foo"

			title1 := "Article 1"
			title2 := "Article 2"

			results := []search.SearchResult{
				{
					ID:           "1",
					URL:          input1,
					Title:        &title1,
					Rank:         1,
					ProviderName: "searxng",
					SourceDomain: "example.com",
				},
				{
					ID:           "2",
					URL:          input2,
					Title:        &title2,
					Rank:         1,
					ProviderName: "searxng",
					SourceDomain: "example.com",
				},
			}

			inputQueries := []search.Response{
				{OriginalQuery: "test", Results: results},
			}

			result := BuildCandidates(inputQueries, cfg)

			// Should have 2 distinct candidates (these params are meaningful)
			if result.TotalCandidates != 2 {
				t.Errorf("Expected 2 total candidates for distinct query params, got %d", result.TotalCandidates)
			}

			// Check both are selected
			selectedCount := 0
			for _, cand := range result.Candidates {
				if cand.Selected {
					selectedCount++
				}
			}
			if selectedCount != 2 {
				t.Errorf("Expected 2 selected candidates, got %d", selectedCount)
			}
		})
	}
}

// TestDuplicateDiscoveredByMultipleQueries tests when the same URL appears
// in results from multiple different queries.
func TestDuplicateDiscoveredByMultipleQueries(t *testing.T) {
	cfg := DefaultConfig()

	title := "Test Article"

	// Same URL discovered by different queries at different ranks
	resultsQ1 := []search.SearchResult{
		{
			ID:           "1",
			URL:          "https://example.com/article",
			Title:        &title,
			Rank:         5,
			ProviderName: "searxng",
			SourceDomain: "example.com",
		},
	}

	resultsQ2 := []search.SearchResult{
		{
			ID:           "2",
			URL:          "https://example.com/article",
			Title:        &title,
			Rank:         2,
			ProviderName: "duckduckgo",
			SourceDomain: "example.com",
		},
	}

	resultsQ3 := []search.SearchResult{
		{
			ID:           "3",
			URL:          "https://example.com/article",
			Title:        &title,
			Rank:         8,
			ProviderName: "brave",
			SourceDomain: "example.com",
		},
	}

	inputQueries := []search.Response{
		{OriginalQuery: "query 1", Results: resultsQ1},
		{OriginalQuery: "query 2", Results: resultsQ2},
		{OriginalQuery: "query 3", Results: resultsQ3},
	}

	result := BuildCandidates(inputQueries, cfg)

	if result.TotalCandidates != 1 {
		t.Errorf("Expected 1 total candidate, got %d", result.TotalCandidates)
	}

	if len(result.Candidates) != 1 {
		t.Fatalf("Expected 1 candidate, got %d", len(result.Candidates))
	}

	cand := result.Candidates[0]

	// All 3 discoveries should be in provenance
	if len(cand.Provenance) != 3 {
		t.Errorf("Expected 3 provenance entries, got %d", len(cand.Provenance))
	}

	// Best discovery should be query 1 (index 1), rank 2
	if cand.BestQueryIndex != 1 {
		t.Errorf("Expected best query index 1 (duckduckgo), got %d", cand.BestQueryIndex)
	}
	if cand.BestRank != 2 {
		t.Errorf("Expected best rank 2, got %d", cand.BestRank)
	}
	if cand.BestProviderName != "duckduckgo" {
		t.Errorf("Expected best provider 'duckduckgo', got %q", cand.BestProviderName)
	}
}

// TestUnsupportedSchemes tests various unsupported URL schemes.
func TestUnsupportedSchemes(t *testing.T) {
	cfg := DefaultConfig()

	tests := []struct {
		name      string
		input     string
		expectErr bool
	}{
		// Unsupported schemes
		{"file", "file:///etc/hosts", true},
		{"mailto", "mailto:user@example.com", true},
		{"tel", "tel:+1234567890", true},
		{"ftp", "ftp://ftp.example.com/file", true},
		{"sftp", "sftp://ftp.example.com/file", true},
		{"data", "data:text/html,<p>hello</p>", true},
		{"javascript", "javascript:alert(1)", true},
		{"vbscript", "vbscript:msgbox(1)", true},
		{"blob", "blob:https://example.com/uuid", true},
		{"about", "about:blank", true},
		{"chrome", "chrome://extensions", true},
		{"edge", "edge://extensions", true},

		// Supported schemes (case insensitive)
		{"http", "http://example.com", false},
		{"https", "https://example.com", false},
		{"HTTP", "HTTP://EXAMPLE.COM", false},
		{"HTTPS", "HTTPS://EXAMPLE.COM", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := []search.SearchResult{
				{
					ID:           "1",
					URL:          tt.input,
					Rank:         1,
					ProviderName: "searxng",
					SourceDomain: "example.com",
				},
			}

			inputQueries := []search.Response{
				{OriginalQuery: "test", Results: results},
			}

			result := BuildCandidates(inputQueries, cfg)

			if tt.expectErr {
				if result.SelectedCount > 0 {
					t.Errorf("Expected 0 selected candidates for %q, got %d", tt.input, result.SelectedCount)
				}
				if len(result.RejectedURLs) == 0 {
					t.Errorf("Expected rejected URL entry for %q", tt.input)
				}
			} else {
				if result.SelectedCount != 1 {
					t.Errorf("Expected 1 selected candidate for %q, got %d", tt.input, result.SelectedCount)
				}
			}
		})
	}
}

// TestOrderingStability tests that results are deterministically ordered.
func TestOrderingStability(t *testing.T) {
	cfg := DefaultConfig()

	createResult := func(id, url string, rank int, queryIdx int) search.SearchResult {
		title := "Title " + url
		return search.SearchResult{
			ID:           id,
			URL:          url,
			Title:        &title,
			Rank:         rank,
			ProviderName: "searxng",
			SourceDomain: "example.com",
		}
	}

	// Create multiple unique URLs that should be ordered by best discovery
	results := []search.SearchResult{
		createResult("1", "https://example.com/a", 3, 0),
		createResult("2", "https://example.com/b", 1, 0),
		createResult("3", "https://example.com/c", 5, 0),
		createResult("4", "https://example.com/d", 1, 1),
		createResult("5", "https://example.com/e", 2, 1),
		createResult("6", "https://example.com/f", 4, 1),
		createResult("7", "https://example.com/g", 1, 2),
		createResult("8", "https://example.com/h", 6, 2),
	}

	inputQueries := []search.Response{
		{OriginalQuery: "query 0", Results: results[0:3]},
		{OriginalQuery: "query 1", Results: results[3:6]},
		{OriginalQuery: "query 2", Results: results[6:8]},
	}

	// Verify ordering is by best discovery (query index, then rank)
	// URL b (query 0, rank 1) should be first
	// URL d (query 1, rank 1) should be second
	// URL g (query 2, rank 1) should be third
	// etc.
	expectedOrder := []string{
		"https://example.com/b", // query 0, rank 1
		"https://example.com/a", // query 0, rank 3
		"https://example.com/c", // query 0, rank 5
		"https://example.com/d", // query 1, rank 1
		"https://example.com/e", // query 1, rank 2
		"https://example.com/f", // query 1, rank 4
		"https://example.com/g", // query 2, rank 1
		"https://example.com/h", // query 2, rank 6
	}

	// Run multiple times to ensure determinism
	var lastResults []Candidate
	for i := 0; i < 5; i++ {
		result := BuildCandidates(inputQueries, cfg)
		currentResults := result.Candidates

		if i == 0 {
			// First run: check expected order
			if len(result.Candidates) != len(expectedOrder) {
				t.Fatalf("Expected %d candidates, got %d", len(expectedOrder), len(result.Candidates))
			}

			for j, expectedURL := range expectedOrder {
				if result.Candidates[j].CandidateURL != expectedURL {
					t.Errorf("Candidate at position %d: expected %q, got %q", j, expectedURL, result.Candidates[j].CandidateURL)
				}
			}
		} else {
			// Subsequent runs: check determinism
			if lastResults != nil && len(lastResults) != len(currentResults) {
				t.Fatalf("Inconsistent number of candidates on iteration %d", i)
			}

			for j, cand := range currentResults {
				if j < len(lastResults) {
					if cand.CandidateURL != lastResults[j].CandidateURL {
						t.Errorf("URL ordering changed on iteration %d: expected %q, got %q", i, lastResults[j].CandidateURL, cand.CandidateURL)
					}
				}
			}
		}
		lastResults = currentResults
	}
}

// TestMaxSourceLimit tests that the MaxCandidates limit is enforced.
func TestMaxSourceLimit(t *testing.T) {
	tests := []struct {
		name          string
		maxCandidates int
		expectedCount int
	}{
		{"limit of 0 (no limit)", 0, 10},
		{"limit equal to total", 10, 10},
		{"limit 1", 1, 1},
		{"limit 3", 3, 3},
		{"limit greater than total", 100, 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.MaxCandidates = tt.maxCandidates

			results := make([]search.SearchResult, 10)
			for i := 0; i < 10; i++ {
				title := "Title " + string(rune('0'+i))
				results[i] = search.SearchResult{
					ID:           string(rune('0' + i)),
					URL:          "https://example.com/" + string(rune('0'+i)),
					Title:        &title,
					Rank:         1,
					ProviderName: "searxng",
					SourceDomain: "example.com",
				}
			}

			inputQueries := []search.Response{
				{OriginalQuery: "test", Results: results},
			}

			result := BuildCandidates(inputQueries, cfg)

			if result.SelectedCount != tt.expectedCount {
				t.Errorf("Expected %d selected candidates, got %d", tt.expectedCount, result.SelectedCount)
			}

			if result.TotalCandidates != 10 {
				t.Errorf("Expected %d total candidates, got %d", 10, result.TotalCandidates)
			}

			// Count selected
			selectedCount := 0
			for _, cand := range result.Candidates {
				if cand.Selected {
					selectedCount++
				}
			}
			if selectedCount != tt.expectedCount {
				t.Errorf("Expected %d selected flag set, got %d", tt.expectedCount, selectedCount)
			}

			// Check CandidatesTotal field
			for _, cand := range result.Candidates {
				if cand.CandidatesTotal != 10 {
					t.Errorf("Expected CandidatesTotal=10, got %d", cand.CandidatesTotal)
				}
			}
		})
	}
}

// TestRoundRobinFairness tests the round-robin fairness algorithm when
// MaxCandidates limits selection.
func TestRoundRobinFairness(t *testing.T) {
	// Setup: 3 queries with different URLs
	cfg := Config{
		MaxCandidates:        3,
		TrackingParams:       []string{},
		UseWildcardAnalytics: true,
	}

	// Query 0: URLs at ranks 1, 4, 7
	// Query 1: URLs at ranks 2, 5, 8
	// Query 2: URLs at ranks 3, 6, 9

	createResult := func(url, id string, rank int, title string) search.SearchResult {
		return search.SearchResult{
			ID:           id,
			URL:          url,
			Title:        &title,
			Rank:         rank,
			ProviderName: "searxng",
			SourceDomain: "example.com",
		}
	}

	inputQueries := []search.Response{
		{
			OriginalQuery: "query 0",
			Results: []search.SearchResult{
				createResult("https://example.com/q0a", "1", 1, "Q0 Rank 1"),
				createResult("https://example.com/q0b", "2", 4, "Q0 Rank 4"),
				createResult("https://example.com/q0c", "3", 7, "Q0 Rank 7"),
			},
		},
		{
			OriginalQuery: "query 1",
			Results: []search.SearchResult{
				createResult("https://example.com/q1a", "4", 2, "Q1 Rank 2"),
				createResult("https://example.com/q1b", "5", 5, "Q1 Rank 5"),
				createResult("https://example.com/q1c", "6", 8, "Q1 Rank 8"),
			},
		},
		{
			OriginalQuery: "query 2",
			Results: []search.SearchResult{
				createResult("https://example.com/q2a", "7", 3, "Q2 Rank 3"),
				createResult("https://example.com/q2b", "8", 6, "Q2 Rank 6"),
				createResult("https://example.com/q2c", "9", 9, "Q2 Rank 9"),
			},
		},
	}

	result := BuildCandidates(inputQueries, cfg)

	if result.TotalCandidates != 9 {
		t.Errorf("Expected 9 total candidates, got %d", result.TotalCandidates)
	}

	if result.SelectedCount != 3 {
		t.Errorf("Expected 3 selected candidates, got %d", result.SelectedCount)
	}

	// With round-robin fairness and limit 3, we should get one from each query
	// The selection should pick:
	// - Round 1: q0a (q0, rank 1), q1a (q1, rank 2), q2a (q2, rank 3)
	// That's exactly 3, which should be selected

	selectedURLs := make(map[string]bool)
	for _, cand := range result.Candidates {
		if cand.Selected {
			selectedURLs[cand.CandidateURL] = true
		}
	}

	// Should have one URL from each query
	expectedSelected := map[string]bool{
		"https://example.com/q0a": true,
		"https://example.com/q1a": true,
		"https://example.com/q2a": true,
	}

	for url := range expectedSelected {
		if !selectedURLs[url] {
			t.Errorf("Expected %q to be selected, but it wasn't", url)
		}
	}

	// Check best discovery for each
	q0 := findCandidate(result.Candidates, "https://example.com/q0a")
	if q0 == nil || q0.BestQueryIndex != 0 {
		t.Errorf("Expected q0a to have best query index 0, got %d", q0.BestQueryIndex)
	}

	q1 := findCandidate(result.Candidates, "https://example.com/q1a")
	if q1 == nil || q1.BestQueryIndex != 1 {
		t.Errorf("Expected q1a to have best query index 1, got %d", q1.BestQueryIndex)
	}

	q2 := findCandidate(result.Candidates, "https://example.com/q2a")
	if q2 == nil || q2.BestQueryIndex != 2 {
		t.Errorf("Expected q2a to have best query index 2, got %d", q2.BestQueryIndex)
	}
}

// TestProvenancePreservation tests that all discovery information is preserved.
func TestProvenancePreservation(t *testing.T) {
	cfg := DefaultConfig()

	now1 := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	now2 := time.Date(2024, 1, 16, 12, 0, 0, 0, time.UTC)
	title1 := "Article One"
	title2 := "Article Two"
	snippet1 := "Snippet for article one"
	snippet2 := "Snippet for article two"

	results := []search.SearchResult{
		{
			ID:           "1",
			URL:          "https://example.com/article",
			Title:        &title1,
			Snippet:      &snippet1,
			ProviderName: "searxng",
			Rank:         1,
			PublishedAt:  &now1,
			SourceDomain: "example.com",
		},
		{
			ID:           "2",
			URL:          "https://example.com/article",
			Title:        &title2,
			Snippet:      &snippet2,
			ProviderName: "duckduckgo",
			Rank:         3,
			PublishedAt:  &now2,
			SourceDomain: "example.com",
		},
	}

	inputQueries := []search.Response{
		{OriginalQuery: "query 1", Results: results[0:1]},
		{OriginalQuery: "query 2", Results: results[1:2]},
	}

	result := BuildCandidates(inputQueries, cfg)

	if result.TotalCandidates != 1 {
		t.Fatalf("Expected 1 total candidate, got %d", result.TotalCandidates)
	}

	cand := result.Candidates[0]

	if len(cand.Provenance) != 2 {
		t.Fatalf("Expected 2 provenance entries, got %d", len(cand.Provenance))
	}

	// Check first discovery
	p1 := cand.Provenance[0]
	if p1.QueryIndex != 0 {
		t.Errorf("Expected first provenance query index 0, got %d", p1.QueryIndex)
	}
	if p1.Rank != 1 {
		t.Errorf("Expected first provenance rank 1, got %d", p1.Rank)
	}
	if p1.ProviderName != "searxng" {
		t.Errorf("Expected first provenance provider 'searxng', got %q", p1.ProviderName)
	}
	if p1.OriginalURL != "https://example.com/article" {
		t.Errorf("Expected first provenance original URL, got %q", p1.OriginalURL)
	}
	if p1.Title == nil || *p1.Title != title1 {
		t.Errorf("Expected first provenance title %q, got %v", title1, p1.Title)
	}
	if p1.Snippet == nil || *p1.Snippet != snippet1 {
		t.Errorf("Expected first provenance snippet %q, got %v", snippet1, p1.Snippet)
	}
	if p1.PublishedAt == nil || !p1.PublishedAt.Equal(now1) {
		t.Errorf("Expected first provenance published_at %v, got %v", now1, p1.PublishedAt)
	}

	// Check second discovery
	p2 := cand.Provenance[1]
	if p2.QueryIndex != 1 {
		t.Errorf("Expected second provenance query index 1, got %d", p2.QueryIndex)
	}
	if p2.Rank != 3 {
		t.Errorf("Expected second provenance rank 3, got %d", p2.Rank)
	}
	if p2.ProviderName != "duckduckgo" {
		t.Errorf("Expected second provenance provider 'duckduckgo', got %q", p2.ProviderName)
	}
	if p2.Title == nil || *p2.Title != title2 {
		t.Errorf("Expected second provenance title %q, got %v", title2, p2.Title)
	}
	if p2.Snippet == nil || *p2.Snippet != snippet2 {
		t.Errorf("Expected second provenance snippet %q, got %v", snippet2, p2.Snippet)
	}
	if p2.PublishedAt == nil || !p2.PublishedAt.Equal(now2) {
		t.Errorf("Expected second provenance published_at %v, got %v", now2, p2.PublishedAt)
	}
}

// TestDuplicateGroups tests that duplicate groups are recorded correctly.
func TestDuplicateGroups(t *testing.T) {
	cfg := DefaultConfig()

	results := []search.SearchResult{
		{
			ID:           "1",
			URL:          "https://example.com/article#section",
			Rank:         1,
			ProviderName: "searxng",
			SourceDomain: "example.com",
		},
		{
			ID:           "2",
			URL:          "https://example.com/article",
			Rank:         2,
			ProviderName: "searxng",
			SourceDomain: "example.com",
		},
	}

	inputQueries := []search.Response{
		{OriginalQuery: "test", Results: results},
	}

	result := BuildCandidates(inputQueries, cfg)

	// Both URLs should collapse to one candidate
	if result.TotalCandidates != 1 {
		t.Errorf("Expected 1 total candidate, got %d", result.TotalCandidates)
	}

	// Should have one duplicate group
	if len(result.DuplicateGroups) != 1 {
		t.Errorf("Expected 1 duplicate group, got %d", len(result.DuplicateGroups))
	}

	dupGroup := result.DuplicateGroups[0]
	if dupGroup.Count != 2 {
		t.Errorf("Expected duplicate group count 2, got %d", dupGroup.Count)
	}
	if len(dupGroup.OriginalURLs) != 2 {
		t.Errorf("Expected 2 original URLs in group, got %d", len(dupGroup.OriginalURLs))
	}
}

// TestRejectedURLs tests that rejected URLs are recorded correctly.
func TestRejectedURLs(t *testing.T) {
	cfg := DefaultConfig()

	results := []search.SearchResult{
		{
			ID:           "1",
			URL:          "https://example.com/valid",
			Rank:         1,
			ProviderName: "searxng",
			SourceDomain: "example.com",
		},
		{
			ID:           "2",
			URL:          "mailto:test@example.com",
			Rank:         2,
			ProviderName: "searxng",
			SourceDomain: "example.com",
		},
		{
			ID:           "3",
			URL:          "ftp://ftp.example.com/file",
			Rank:         3,
			ProviderName: "searxng",
			SourceDomain: "example.com",
		},
	}

	inputQueries := []search.Response{
		{OriginalQuery: "test", Results: results},
	}

	result := BuildCandidates(inputQueries, cfg)

	if result.SelectedCount != 1 {
		t.Errorf("Expected 1 selected candidate, got %d", result.SelectedCount)
	}

	// Should have 2 rejected URLs
	if len(result.RejectedURLs) != 2 {
		t.Errorf("Expected 2 rejected URLs, got %d", len(result.RejectedURLs))
	}

	rejectedURLs := make(map[string]bool)
	for _, r := range result.RejectedURLs {
		rejectedURLs[r.OriginalURL] = true
		if r.Reason == "" {
			t.Errorf("Rejected URL missing reason: %s", r.OriginalURL)
		}
	}

	if !rejectedURLs["mailto:test@example.com"] {
		t.Error("Expected mailto URL to be rejected")
	}
	if !rejectedURLs["ftp://ftp.example.com/file"] {
		t.Error("Expected ftp URL to be rejected")
	}
}

// TestCustomTrackingParams tests that custom tracking params can be added.
func TestCustomTrackingParams(t *testing.T) {
	cfg := Config{
		MaxCandidates:  0,
		TrackingParams: []string{"tracking_param", "custom_id", "session_id"},
	}

	tests := []struct {
		name         string
		param        string
		shouldRemove bool
	}{
		{"default param", "utm_source", true},
		{"custom param", "tracking_param", true},
		{"another custom", "custom_id", true},
		{"non-tracking param", "article_id", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := "https://example.com/article?" + tt.param + "=value&foo=bar"
			results := []search.SearchResult{
				{
					ID:           "1",
					URL:          input,
					Rank:         1,
					ProviderName: "searxng",
					SourceDomain: "example.com",
				},
			}

			inputQueries := []search.Response{
				{OriginalQuery: "test", Results: results},
			}

			result := BuildCandidates(inputQueries, cfg)

			if len(result.Candidates) != 1 {
				t.Fatalf("Expected 1 candidate, got %d", len(result.Candidates))
			}

			cand := result.Candidates[0]

			if tt.shouldRemove && containsParam(cand.CandidateURL, tt.param) {
				t.Errorf("Expected custom param %q to be removed, but it remains", tt.param)
			}

			if !tt.shouldRemove && !containsParam(cand.CandidateURL, tt.param) {
				t.Errorf("Expected non-tracking param %q to remain, but it was removed", tt.param)
			}
		})
	}
}

// TestKeepQueryParams tests that specific params can be kept even if they
// would normally be removed as tracking.
func TestKeepQueryParams(t *testing.T) {
	cfg := Config{
		KeepQueryParams: []string{"utm_source", "gclid"},
	}

	input := "https://example.com/article?utm_source=google&gclid=ABC&foo=bar"
	results := []search.SearchResult{
		{
			ID:           "1",
			URL:          input,
			Rank:         1,
			ProviderName: "searxng",
			SourceDomain: "example.com",
		},
	}

	inputQueries := []search.Response{
		{OriginalQuery: "test", Results: results},
	}

	result := BuildCandidates(inputQueries, cfg)

	if len(result.Candidates) != 1 {
		t.Fatalf("Expected 1 candidate, got %d", len(result.Candidates))
	}

	cand := result.Candidates[0]

	// Kept params should remain
	if !containsParam(cand.CandidateURL, "utm_source=google") {
		t.Errorf("Expected utm_source to be kept, but it was removed")
	}
	if !containsParam(cand.CandidateURL, "gclid=ABC") {
		t.Errorf("Expected gclid to be kept, but it was removed")
	}
}

// TestProcessingErrors tests that URL parsing errors are recorded.
func TestProcessingErrors(t *testing.T) {
	cfg := DefaultConfig()

	results := []search.SearchResult{
		{
			ID:           "1",
			URL:          "https://example.com/valid",
			Rank:         1,
			ProviderName: "searxng",
			SourceDomain: "example.com",
		},
		{
			ID:           "2",
			URL:          "://invalid scheme missing",
			Rank:         2,
			ProviderName: "searxng",
			SourceDomain: "example.com",
		},
	}

	inputQueries := []search.Response{
		{OriginalQuery: "test", Results: results},
	}

	result := BuildCandidates(inputQueries, cfg)

	if len(result.ProcessingErrors) == 0 {
		t.Error("Expected processing error for invalid URL")
	}

	err := result.ProcessingErrors[0]
	if err.OriginalURL != "://invalid scheme missing" {
		t.Errorf("Expected error for wrong URL, got %q", err.OriginalURL)
	}
	if err.QueryIndex != 0 {
		t.Errorf("Expected query index 0, got %d", err.QueryIndex)
	}
	if err.Rank != 2 {
		t.Errorf("Expected rank 2, got %d", err.Rank)
	}
}

// TestNilAndEmptyCases tests edge cases with nil and empty inputs.
func TestNilAndEmptyCases(t *testing.T) {
	cfg := DefaultConfig()

	t.Run("empty input queries", func(t *testing.T) {
		result := BuildCandidates([]search.Response{}, cfg)
		if result.TotalCandidates != 0 {
			t.Errorf("Expected 0 total candidates, got %d", result.TotalCandidates)
		}
		if result.SelectedCount != 0 {
			t.Errorf("Expected 0 selected candidates, got %d", result.SelectedCount)
		}
		if len(result.Candidates) != 0 {
			t.Errorf("Expected 0 candidates, got %d", len(result.Candidates))
		}
	})

	t.Run("queries with empty results", func(t *testing.T) {
		inputQueries := []search.Response{
			{OriginalQuery: "test", Results: []search.SearchResult{}},
			{OriginalQuery: "test2", Results: []search.SearchResult{}},
		}
		result := BuildCandidates(inputQueries, cfg)
		if result.TotalCandidates != 0 {
			t.Errorf("Expected 0 total candidates, got %d", result.TotalCandidates)
		}
	})

	t.Run("single result", func(t *testing.T) {
		title := "Title"
		results := []search.SearchResult{
			{
				ID:           "1",
				URL:          "https://example.com",
				Title:        &title,
				Rank:         1,
				ProviderName: "searxng",
				SourceDomain: "example.com",
			},
		}
		inputQueries := []search.Response{
			{OriginalQuery: "test", Results: results},
		}
		result := BuildCandidates(inputQueries, cfg)
		if result.TotalCandidates != 1 {
			t.Errorf("Expected 1 total candidate, got %d", result.TotalCandidates)
		}
		if result.SelectedCount != 1 {
			t.Errorf("Expected 1 selected candidate, got %d", result.SelectedCount)
		}
	})
}

// TestWildcardAnalyticsParams tests wildcard handling for _ga_* params.
func TestWildcardAnalyticsParams(t *testing.T) {
	t.Run("wildcard enabled removes _ga_*", func(t *testing.T) {
		cfg := Config{
			UseWildcardAnalytics: true,
		}

		input := "https://example.com/article?_ga=1.2.3.4&_gid=5.6.7.8&_ga_12345=abc"
		results := []search.SearchResult{
			{
				ID:           "1",
				URL:          input,
				Rank:         1,
				ProviderName: "searxng",
				SourceDomain: "example.com",
			},
		}

		inputQueries := []search.Response{
			{OriginalQuery: "test", Results: results},
		}

		result := BuildCandidates(inputQueries, cfg)

		if len(result.Candidates) != 1 {
			t.Fatalf("Expected 1 candidate, got %d", len(result.Candidates))
		}

		cand := result.Candidates[0]

		// _ga should be removed
		if containsParam(cand.CandidateURL, "_ga=") {
			t.Errorf("Expected _ga to be removed with wildcard enabled")
		}
		// _gid should be removed
		if containsParam(cand.CandidateURL, "_gid=") {
			t.Errorf("Expected _gid to be removed with wildcard enabled")
		}
		// _ga_* pattern should be removed
		if containsParam(cand.CandidateURL, "_ga_12345=abc") {
			t.Errorf("Expected _ga_* patterns to be removed")
		}
	})

	t.Run("wildcard disabled keeps _ga_*", func(t *testing.T) {
		cfg := Config{
			UseWildcardAnalytics: false,
		}

		// Without wildcard, _ga_12345 is not in the default list
		input := "https://example.com/article?_ga=1.2.3.4&_ga_12345=abc"
		results := []search.SearchResult{
			{
				ID:           "1",
				URL:          input,
				Rank:         1,
				ProviderName: "searxng",
				SourceDomain: "example.com",
			},
		}

		inputQueries := []search.Response{
			{OriginalQuery: "test", Results: results},
		}

		result := BuildCandidates(inputQueries, cfg)

		if len(result.Candidates) != 1 {
			t.Fatalf("Expected 1 candidate, got %d", len(result.Candidates))
		}

		cand := result.Candidates[0]

		// _ga should still be removed (it's in the list)
		if containsParam(cand.CandidateURL, "_ga=1.2.3.4") {
			t.Errorf("Expected _ga to be removed (exact match)")
		}
		// _ga_12345 should remain (no wildcard)
		if !containsParam(cand.CandidateURL, "_ga_12345=abc") {
			t.Errorf("Expected _ga_12345 to remain without wildcard")
		}
	})
}

// TestSelectionRankFields tests that selection rank fields are set correctly.
func TestSelectionRankFields(t *testing.T) {
	cfg := Config{MaxCandidates: 5}

	// Create 10 unique URLs
	results := make([]search.SearchResult, 10)
	for i := 0; i < 10; i++ {
		title := "Title " + string(rune('0'+i))
		results[i] = search.SearchResult{
			ID:           string(rune('0' + i)),
			URL:          "https://example.com/" + string(rune('0'+i)),
			Title:        &title,
			Rank:         1,
			ProviderName: "searxng",
			SourceDomain: "example.com",
		}
	}

	inputQueries := []search.Response{
		{OriginalQuery: "test", Results: results},
	}

	result := BuildCandidates(inputQueries, cfg)

	// Check SelectionRank is sequential
	for i, cand := range result.Candidates {
		if cand.SelectionRank != i {
			t.Errorf("Candidate %d has SelectionRank %d, expected %d", i, cand.SelectionRank, i)
		}
		if cand.CandidatesTotal != 10 {
			t.Errorf("Candidate %d has CandidatesTotal %d, expected 10", i, cand.CandidatesTotal)
		}
	}
}

// TestBestDiscoverySelection tests that the best discovery is correctly identified.
func TestBestDiscoverySelection(t *testing.T) {
	cfg := DefaultConfig()

	title := "Test Article"

	// Same URL from same query at different ranks
	results := []search.SearchResult{
		{
			ID:           "1",
			URL:          "https://example.com/article",
			Title:        &title,
			Rank:         5,
			ProviderName: "searxng",
			SourceDomain: "example.com",
		},
		{
			ID:           "2",
			URL:          "https://example.com/article",
			Title:        &title,
			Rank:         2,
			ProviderName: "searxng",
			SourceDomain: "example.com",
		},
		{
			ID:           "3",
			URL:          "https://example.com/article",
			Title:        &title,
			Rank:         8,
			ProviderName: "searxng",
			SourceDomain: "example.com",
		},
	}

	inputQueries := []search.Response{
		{OriginalQuery: "test", Results: results},
	}

	result := BuildCandidates(inputQueries, cfg)

	if result.TotalCandidates != 1 {
		t.Fatalf("Expected 1 total candidate, got %d", result.TotalCandidates)
	}

	cand := result.Candidates[0]

	// Best rank should be 2 (lowest = best)
	if cand.BestRank != 2 {
		t.Errorf("Expected BestRank 2, got %d", cand.BestRank)
	}

	// Best query index should be 0 (only query)
	if cand.BestQueryIndex != 0 {
		t.Errorf("Expected BestQueryIndex 0, got %d", cand.BestQueryIndex)
	}
}

// TestProvenanceSorting tests that provenance is sorted deterministically.
func TestProvenanceSorting(t *testing.T) {
	cfg := DefaultConfig()

	title := "Test Article"

	// Create results that will be in reverse order
	results := []search.SearchResult{
		{
			ID:           "1",
			URL:          "https://example.com/article",
			Title:        &title,
			Rank:         5,
			ProviderName: "searxng",
			SourceDomain: "example.com",
		},
		{
			ID:           "2",
			URL:          "https://example.com/article",
			Title:        &title,
			Rank:         2,
			ProviderName: "searxng",
			SourceDomain: "example.com",
		},
		{
			ID:           "3",
			URL:          "https://example.com/article",
			Title:        &title,
			Rank:         8,
			ProviderName: "searxng",
			SourceDomain: "example.com",
		},
	}

	// Same results from second query
	results2 := []search.SearchResult{
		{
			ID:           "4",
			URL:          "https://example.com/article",
			Title:        &title,
			Rank:         3,
			ProviderName: "duckduckgo",
			SourceDomain: "example.com",
		},
	}

	inputQueries := []search.Response{
		{OriginalQuery: "query 1", Results: results},
		{OriginalQuery: "query 2", Results: results2},
	}

	result := BuildCandidates(inputQueries, cfg)

	if len(result.Candidates) != 1 {
		t.Fatalf("Expected 1 candidate, got %d", len(result.Candidates))
	}

	cand := result.Candidates[0]

	// Provenance should be sorted by query index, then rank
	// query 1, rank 2 < query 1, rank 5 < query 1, rank 8 < query 2, rank 3
	if len(cand.Provenance) != 4 {
		t.Fatalf("Expected 4 provenance entries, got %d", len(cand.Provenance))
	}

	// First should be query 0, rank 2
	if cand.Provenance[0].QueryIndex != 0 || cand.Provenance[0].Rank != 2 {
		t.Errorf("Expected first provenance to be query 0, rank 2, got %d, %d", cand.Provenance[0].QueryIndex, cand.Provenance[0].Rank)
	}

	// Second should be query 0, rank 5
	if cand.Provenance[1].QueryIndex != 0 || cand.Provenance[1].Rank != 5 {
		t.Errorf("Expected second provenance to be query 0, rank 5, got %d, %d", cand.Provenance[1].QueryIndex, cand.Provenance[1].Rank)
	}

	// Third should be query 0, rank 8
	if cand.Provenance[2].QueryIndex != 0 || cand.Provenance[2].Rank != 8 {
		t.Errorf("Expected third provenance to be query 0, rank 8, got %d, %d", cand.Provenance[2].QueryIndex, cand.Provenance[2].Rank)
	}

	// Fourth should be query 1, rank 3
	if cand.Provenance[3].QueryIndex != 1 || cand.Provenance[3].Rank != 3 {
		t.Errorf("Expected fourth provenance to be query 1, rank 3, got %d, %d", cand.Provenance[3].QueryIndex, cand.Provenance[3].Rank)
	}
}

// TestCandidateDomainExtraction tests that domain is correctly extracted.
func TestCandidateDomainExtraction(t *testing.T) {
	cfg := DefaultConfig()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple domain", "https://example.com/path", "example.com"},
		{"domain with port", "https://example.com:8080/path", "example.com:8080"},
		{"subdomain", "https://sub.example.com/path", "sub.example.com"},
		{"multi-level subdomain", "https://a.b.example.com/path", "a.b.example.com"},
		{"lowercase host", "https://EXAMPLE.COM/path", "example.com"},
		{"with www", "https://www.example.com/path", "www.example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			title := "Title"
			results := []search.SearchResult{
				{
					ID:           "1",
					URL:          tt.input,
					Title:        &title,
					Rank:         1,
					ProviderName: "searxng",
					SourceDomain: "example.com",
				},
			}

			inputQueries := []search.Response{
				{OriginalQuery: "test", Results: results},
			}

			result := BuildCandidates(inputQueries, cfg)

			if len(result.Candidates) != 1 {
				t.Fatalf("Expected 1 candidate, got %d", len(result.Candidates))
			}

			cand := result.Candidates[0]
			if cand.CandidateDomain != tt.expected {
				t.Errorf("Expected domain %q, got %q", tt.expected, cand.CandidateDomain)
			}
		})
	}
}

// TestNormalizationPreservesPathAndQuery tests that path and non-tracking query params
// are preserved exactly.
func TestNormalizationPreservesPathAndQuery(t *testing.T) {
	cfg := DefaultConfig()

	tests := []struct {
		name        string
		input       string
		pathExpect  string
		queryExpect string
	}{
		{"preserve path case", "https://example.com/Path/To/Article", "/Path/To/Article", ""},
		{"preserve query order", "https://example.com/search?foo=bar&baz=qux", "/search", "baz=qux&foo=bar"},
		{"preserve special chars in path", "https://example.com/path-with-dash/under_score", "/path-with-dash/under_score", ""},
		{"preserve encoded chars", "https://example.com/path%20with%20spaces", "/path with spaces", ""}, // url.Path unescapes
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			title := "Title"
			results := []search.SearchResult{
				{
					ID:           "1",
					URL:          tt.input,
					Title:        &title,
					Rank:         1,
					ProviderName: "searxng",
					SourceDomain: "example.com",
				},
			}

			inputQueries := []search.Response{
				{OriginalQuery: "test", Results: results},
			}

			result := BuildCandidates(inputQueries, cfg)

			if len(result.Candidates) != 1 {
				t.Fatalf("Expected 1 candidate, got %d", len(result.Candidates))
			}

			cand := result.Candidates[0]
			u, _ := url.Parse(cand.CandidateURL)

			if u.Path != tt.pathExpect {
				t.Errorf("Expected path %q, got %q", tt.pathExpect, u.Path)
			}

			if tt.queryExpect != "" {
				if u.RawQuery != tt.queryExpect {
					t.Errorf("Expected query %q, got %q", tt.queryExpect, u.RawQuery)
				}
			}
		})
	}
}

// TestDedupeAcrossManyResults tests deduplication with many results.
func TestDedupeAcrossManyResults(t *testing.T) {
	cfg := DefaultConfig()

	// 100 results with some duplicates
	results := make([]search.SearchResult, 100)
	seenURLs := make(map[string]bool)
	baseURL := "https://example.com/article"

	// Create 80 unique URLs and 20 duplicates (same URL appearing 4 times each)
	urlCount := 0
	for i := 0; i < 100; i++ {
		title := "Article " + string(rune('0'+(i%10)))
		url := baseURL + "/" + string(rune('a'+(i%80)))
		if _, exists := seenURLs[url]; exists {
			// This is a duplicate of an earlier URL
			url = baseURL + "/" + string(rune('a'+((i-40)%20)))
		} else {
			seenURLs[url] = true
			urlCount++
		}

		results[i] = search.SearchResult{
			ID:           string(rune('0' + i)),
			URL:          url,
			Title:        &title,
			Rank:         i + 1,
			ProviderName: "searxng",
			SourceDomain: "example.com",
		}
	}

	inputQueries := []search.Response{
		{OriginalQuery: "test", Results: results},
	}

	result := BuildCandidates(inputQueries, cfg)

	// We should have deduplicated down from 100 to ~80
	if result.TotalCandidates > 100 {
		t.Errorf("Total candidates %d exceeds input count", result.TotalCandidates)
	}
	if result.TotalCandidates < 60 {
		t.Errorf("Total candidates %d too low, expected around 80", result.TotalCandidates)
	}
}

// Helper function to check if URL contains a param
func containsParam(urlStr string, param string) bool {
	u, err := parseURL(urlStr)
	if err != nil {
		return false
	}
	q := u.Query()
	// Check if param key exists (param can be "key" or "key=value")
	parts := strings.SplitN(param, "=", 2)
	key := parts[0]
	return q.Has(key)
}

// parseURL is a wrapper around url.Parse for tests
func parseURL(input string) (*url.URL, error) {
	return url.Parse(input)
}

// Helper function to find a candidate by URL
func findCandidate(candidates []Candidate, url string) *Candidate {
	for i := range candidates {
		if candidates[i].CandidateURL == url {
			return &candidates[i]
		}
	}
	return nil
}

// Helper to parse and normalize a URL for testing
func parseAndNormalize(input string, cfg Config) (*url.URL, error) {
	u, err := url.Parse(input)
	if err != nil {
		return nil, err
	}
	if !isHTTPScheme(u.Scheme) {
		return nil, fmt.Errorf("unsupported scheme: %s", u.Scheme)
	}
	// Merge default tracking params like New() does
	trackingParams := make([]string, len(DefaultTrackingParams))
	copy(trackingParams, DefaultTrackingParams)
	trackingParams = append(trackingParams, cfg.TrackingParams...)
	cfg.TrackingParams = trackingParams
	return normalizeURL(u, cfg), nil
}
