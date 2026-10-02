package workflow

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/discovery"
	"github.com/Mundo-Dolphins/local-newsroom/internal/extractor"
	"github.com/Mundo-Dolphins/local-newsroom/internal/fetcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/planner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/search"
)

// =============================================================================
// Fake Test Dependencies
// =============================================================================

// fakeDiscoveryService implements discoveryService for testing.
type fakeDiscoveryService struct {
	candidates []discovery.Candidate
	shouldErr  bool
	errMsg     string
}

func newFakeDiscoveryService(candidates []discovery.Candidate) *fakeDiscoveryService {
	return &fakeDiscoveryService{
		candidates: candidates,
		shouldErr:  false,
	}
}

func (f *fakeDiscoveryService) WithError(err error) *fakeDiscoveryService {
	f.shouldErr = true
	f.errMsg = err.Error()
	return f
}

func (f *fakeDiscoveryService) Discover(ctx context.Context, topic string, language string, timeRangeHint *planner.TimeRangeHint) (*discovery.DiscoveryResult, error) {
	if f.shouldErr {
		// Return error with query results showing failure
		return &discovery.DiscoveryResult{
			Candidates:     nil,
			PlannedQueries: []planner.SearchQuery{{Query: "test", Purpose: "test"}},
			QueryResults: []discovery.QueryResult{
				{
					QueryIndex:      0,
					QueryText:       "test query",
					Status:          discovery.QueryStatusFailed,
					ResultsReturned: 0,
					Error:           f.errMsg,
				},
			},
			Diagnostics: []discovery.Diagnostic{
				{
					Level:   discovery.DiagnosticLevelError,
					Code:    "discovery_error",
					Message: f.errMsg,
				},
			},
			TotalDiscoveredURLs:     0,
			TotalCandidatesReturned: 0,
			FailedQueryCount:        1,
			SuccessfulQueryCount:    0,
		}, &discovery.DiscoveryError{
			Code:    "discovery_failed",
			Message: f.errMsg,
			QueryResults: []discovery.QueryResult{
				{
					QueryIndex:      0,
					QueryText:       "test query",
					Status:          discovery.QueryStatusFailed,
					ResultsReturned: 0,
					Error:           f.errMsg,
				},
			},
			Diagnostics: []discovery.Diagnostic{
				{
					Level:   discovery.DiagnosticLevelError,
					Code:    "discovery_error",
					Message: f.errMsg,
				},
			},
		}
	}

	// Build a discovery result with candidates
	queryResults := make([]discovery.QueryResult, 0)
	if len(f.candidates) > 0 {
		// First query index in provenance becomes the query result
		qr := discovery.QueryResult{
			QueryIndex:      0,
			QueryText:       "test query",
			Status:          discovery.QueryStatusSuccess,
			ResultsReturned: len(f.candidates),
		}
		queryResults = append(queryResults, qr)
	}

	return &discovery.DiscoveryResult{
		Candidates:              f.candidates,
		PlannedQueries:          []planner.SearchQuery{{Query: "test", Purpose: "test"}},
		QueryResults:            queryResults,
		Diagnostics:             nil,
		TotalDiscoveredURLs:     len(f.candidates),
		TotalCandidatesReturned: len(f.candidates),
		FailedQueryCount:        0,
		SuccessfulQueryCount:    1,
	}, nil
}

// fakeSearchProvider implements search.Provider for testing.
type fakeSearchProvider struct {
	results []search.Response
	err     error
	should  bool
}

func newFakeSearchProvider(results []search.Response) *fakeSearchProvider {
	return &fakeSearchProvider{
		results: results,
		should:  false,
	}
}

func (f *fakeSearchProvider) WithError(err error) *fakeSearchProvider {
	f.should = true
	f.err = err
	return f
}

func (f *fakeSearchProvider) Search(ctx context.Context, req search.Request) (search.Response, error) {
	if f.should {
		return search.Response{}, f.err
	}

	// Return the first response or empty
	if len(f.results) > 0 {
		return f.results[0], nil
	}
	return search.Response{}, errors.New("no results")
}

// fakePlanner implements discovery.Planner for testing.
type fakePlanner struct {
	plan *planner.SearchPlan
	err  error
}

func newFakePlanner(plan *planner.SearchPlan) *fakePlanner {
	return &fakePlanner{
		plan: plan,
		err:  nil,
	}
}

func (f *fakePlanner) WithError(err error) *fakePlanner {
	f.err = err
	return f
}

func (f *fakePlanner) Generate(ctx context.Context, topic string, language string, timeRangeHint *planner.TimeRangeHint) (*planner.SearchPlan, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.plan, nil
}

// =============================================================================
// Helper Functions
// =============================================================================

func buildCandidate(url, domain string, rank int) discovery.Candidate {
	title := "Sample Article"
	published, _ := time.Parse(time.RFC3339, "2024-01-15T10:00:00Z")

	return discovery.Candidate{
		CandidateURL:    url,
		CandidateDomain: domain,
		Provenance: []discovery.CandidateProvenance{
			{
				QueryIndex:   0,
				QueryPurpose: "test purpose",
				Rank:         rank,
				ProviderName: "test-provider",
				Title:        &title,
				Snippet:      &title,
				PublishedAt:  &published,
				SourceDomain: domain,
			},
		},
		BestRank:         rank,
		BestQueryIndex:   0,
		BestProviderName: "test-provider",
		Selected:         true,
	}
}

// =============================================================================
// Test Cases
// =============================================================================

func TestAutoDiscover_Only(t *testing.T) {
	// Test that AutoDiscover with no URLs discovers sources and fetches them

	// Create test server with valid HTML
	contentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<html><body><p>Test content</p></body></html>`))
	}))
	defer contentServer.Close()

	// Set up fake discovery service
	candidates := []discovery.Candidate{
		buildCandidate(contentServer.URL, "example.com", 1),
	}

	cfg := Config{
		Topic:            "Test Discovery",
		AutoDiscover:     true,
		DiscoveryConfig:  discovery.DefaultConfig(),
		DiscoveryService: newFakeDiscoveryService(candidates),
		OutputPath:       "/tmp/test_discovery_only.json",
		LLMBaseURL:       "http://invalid:9999", // Won't be reached if discovery works
		FetcherConfig:    fetcher.Config{Timeout: 5 * time.Second},
		ExtractorConfig:  extractor.Config{MaxWordCount: 100},
	}

	w := New(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := w.Run(ctx)
	// We expect LLM config error since there's no real LLM, but discovery+fetch should succeed
	if err == nil {
		t.Fatal("Expected error for missing LLM, got nil")
	}

	var rf *ResearchFailure
	if !errors.As(err, &rf) {
		t.Fatalf("Expected ResearchFailure, got %T", err)
	}

	// Should fail at LLM config, not discovery or fetch
	if rf.Type == "discovery_failure" {
		t.Errorf("Discovery should have succeeded, got discovery_failure")
	}
	if rf.Type == "fetch_failure" {
		t.Errorf("Fetch should have succeeded, got fetch_failure")
	}
}

func TestAutoDiscover_NoCandidates(t *testing.T) {
	// Test that AutoDiscover with zero candidates fails clearly

	cfg := Config{
		Topic:            "Test Discovery",
		AutoDiscover:     true,
		DiscoveryConfig:  discovery.DefaultConfig(),
		DiscoveryService: newFakeDiscoveryService([]discovery.Candidate{}),
		OutputPath:       "/tmp/test_no_candidates.json",
		LLMBaseURL:       "http://invalid:9999",
	}

	w := New(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := w.Run(ctx)
	if err == nil {
		t.Fatal("Expected discovery failure with zero candidates")
	}

	var rf *ResearchFailure
	if !errors.As(err, &rf) {
		t.Fatalf("Expected ResearchFailure, got %T", err)
	}

	if rf.Type != "discovery_failure" {
		t.Errorf("Expected discovery_failure, got %s", rf.Type)
	}

	if rf.Message != "no source candidates discovered" {
		t.Errorf("Expected 'no source candidates discovered', got %q", rf.Message)
	}
}

func TestAutoDiscover_DiscoveryFailure(t *testing.T) {
	// Test that AutoDiscover fails when discovery service returns an error

	cfg := Config{
		Topic:            "Test Discovery",
		AutoDiscover:     true,
		DiscoveryConfig:  discovery.DefaultConfig(),
		DiscoveryService: newFakeDiscoveryService(nil).WithError(errors.New("planner not available")),
		OutputPath:       "/tmp/test_discovery_error.json",
		LLMBaseURL:       "http://invalid:9999",
	}

	w := New(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := w.Run(ctx)
	if err == nil {
		t.Fatal("Expected discovery failure")
	}

	var rf *ResearchFailure
	if !errors.As(err, &rf) {
		t.Fatalf("Expected ResearchFailure, got %T", err)
	}

	if rf.Type != "discovery_failure" {
		t.Errorf("Expected discovery_failure, got %s", rf.Type)
	}

	// Should have query results showing planner failure
	if len(rf.DiscoveryQueryErr) == 0 {
		t.Error("Expected query results in discovery failure")
	}
}

func TestExplicitMode(t *testing.T) {
	// Test that explicit URLs mode still works (v0.1 behavior)

	// Create test server with valid HTML
	contentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<html><body><p>Test content</p></body></html>`))
	}))
	defer contentServer.Close()

	cfg := Config{
		Topic:      "Test Explicit",
		URLs:       []string{contentServer.URL},
		OutputPath: "/tmp/test_explicit.json",
		LLMBaseURL: "http://invalid:9999",
	}

	w := New(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := w.Run(ctx)
	if err == nil {
		t.Fatal("Expected error for missing LLM")
	}

	var rf *ResearchFailure
	if !errors.As(err, &rf) {
		t.Fatalf("Expected ResearchFailure, got %T", err)
	}

	// Should fail at LLM config, not fetch
	if rf.Type == "fetch_failure" {
		t.Errorf("Fetch should have succeeded, got fetch_failure")
	}
}

func TestSupplementMode_Merge(t *testing.T) {
	// Test that supplement mode merges explicit URLs with discovered URLs

	// Create test servers
	explicitServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<html><body><p>Explicit content</p></body></html>`))
	}))
	defer explicitServer.Close()

	discoveredServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<html><body><p>Discovered content</p></body></html>`))
	}))
	defer discoveredServer.Close()

	// Create fake discovery service that returns the discovered URL
	candidates := []discovery.Candidate{
		buildCandidate(discoveredServer.URL, "discovered.com", 1),
	}

	cfg := Config{
		Topic:            "Test Supplement",
		URLs:             []string{explicitServer.URL},
		AutoDiscover:     true,
		SupplementMode:   true,
		DiscoveryConfig:  discovery.DefaultConfig(),
		DiscoveryService: newFakeDiscoveryService(candidates),
		OutputPath:       "/tmp/test_supplement.json",
		LLMBaseURL:       "http://invalid:9999",
	}

	w := New(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := w.Run(ctx)
	if err == nil {
		t.Fatal("Expected error for missing LLM")
	}

	var rf *ResearchFailure
	if !errors.As(err, &rf) {
		t.Fatalf("Expected ResearchFailure, got %T", err)
	}

	// Should fail at LLM config, meaning both fetch and extraction succeeded
	if rf.Type == "fetch_failure" {
		t.Errorf("Fetch should have succeeded, got fetch_failure")
	}
	if rf.Type == "discovery_failure" {
		t.Errorf("Discovery should have succeeded, got discovery_failure")
	}
}

func TestSupplementMode_DiscovyFailureContinues(t *testing.T) {
	// Test that supplement mode continues with explicit URLs when discovery fails

	// Create test server for explicit URL
	explicitServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<html><body><p>Explicit content</p></body></html>`))
	}))
	defer explicitServer.Close()

	cfg := Config{
		Topic:            "Test Supplement Failure",
		URLs:             []string{explicitServer.URL},
		AutoDiscover:     true,
		SupplementMode:   true,
		DiscoveryConfig:  discovery.DefaultConfig(),
		DiscoveryService: newFakeDiscoveryService(nil).WithError(errors.New("discovery unavailable")),
		OutputPath:       "/tmp/test_supplement_error.json",
		LLMBaseURL:       "http://invalid:9999",
	}

	w := New(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := w.Run(ctx)
	if err == nil {
		t.Fatal("Expected error for missing LLM")
	}

	var rf *ResearchFailure
	if !errors.As(err, &rf) {
		t.Fatalf("Expected ResearchFailure, got %T", err)
	}

	// Should fail at LLM config, meaning explicit URLs were used despite discovery failure
	if rf.Type == "fetch_failure" {
		t.Errorf("Fetch should have succeeded with explicit URLs, got fetch_failure")
	}
}

func TestConfigValidation(t *testing.T) {
	// Test that AutoDiscover=true with URLs but SupplementMode=false fails

	cfg := Config{
		Topic:            "Test",
		URLs:             []string{"http://example.com"},
		AutoDiscover:     true,
		SupplementMode:   false,
		DiscoveryConfig:  discovery.DefaultConfig(),
		DiscoveryService: newFakeDiscoveryService([]discovery.Candidate{}),
	}

	w := New(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := w.Run(ctx)
	if err == nil {
		t.Fatal("Expected config error for invalid combination")
	}

	var rf *ResearchFailure
	if !errors.As(err, &rf) {
		t.Fatalf("Expected ResearchFailure, got %T", err)
	}

	if rf.Type != "config_error" {
		t.Errorf("Expected config_error, got %s", rf.Type)
	}
}

func TestMergeURLs(t *testing.T) {
	tests := []struct {
		name          string
		explicit      []string
		discovered    []string
		expectedCount int
		expectedFirst string
	}{
		{
			name:          "disjoint sets",
			explicit:      []string{"http://explicit.com/1", "http://explicit.com/2"},
			discovered:    []string{"http://discovered.com/1"},
			expectedCount: 3,
			expectedFirst: "http://explicit.com/1",
		},
		{
			name:          "overlapping URLs (case insensitive)",
			explicit:      []string{"http://Example.com/1"},
			discovered:    []string{"http://example.com/1", "http://example.com/2"},
			expectedCount: 2,
			expectedFirst: "http://Example.com/1",
		},
		{
			name:          "all discovered are duplicates",
			explicit:      []string{"http://a.com"},
			discovered:    []string{"http://a.com", "http://A.COM"},
			expectedCount: 1,
			expectedFirst: "http://a.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mergeURLs(tt.explicit, tt.discovered)

			if len(result) != tt.expectedCount {
				t.Errorf("Expected %d URLs, got %d", tt.expectedCount, len(result))
			}

			if len(result) > 0 && result[0] != tt.expectedFirst {
				t.Errorf("Expected first URL %q, got %q", tt.expectedFirst, result[0])
			}

			// Verify no duplicates (case-insensitive)
			seen := make(map[string]bool)
			for _, url := range result {
				normalized := strings.ToLower(url)
				if seen[normalized] {
					t.Errorf("Duplicate URL found: %s", url)
				}
				seen[normalized] = true
			}
		})
	}
}

func TestExtractCandidateURLs(t *testing.T) {
	candidates := []discovery.Candidate{
		{CandidateURL: "http://example.com/1", CandidateDomain: "example.com"},
		{CandidateURL: "http://example.com/2", CandidateDomain: "example.com"},
	}

	urls := extractCandidateURLs(candidates)

	if len(urls) != 2 {
		t.Fatalf("Expected 2 URLs, got %d", len(urls))
	}

	if urls[0] != "http://example.com/1" || urls[1] != "http://example.com/2" {
		t.Errorf("Unexpected URLs: %v", urls)
	}
}

func TestNewDiscoveryService(t *testing.T) {
	// Test that NewDiscoveryService creates a valid discovery service
	plan := &planner.SearchPlan{
		OriginalTopic: "test",
		Queries:       []planner.SearchQuery{{Query: "test"}},
	}

	planner := newFakePlanner(plan)
	provider := newFakeSearchProvider([]search.Response{
		{
			OriginalQuery: "test",
			Results: []search.SearchResult{
				{
					URL:          "http://example.com",
					Title:        stringPtr("Test"),
					ProviderName: "test",
					Rank:         1,
					SourceDomain: "example.com",
				},
			},
		},
	})

	cfg := discovery.DefaultConfig()
	cfg.MaxCandidates = 5

	disc := NewDiscoveryService(planner, provider, cfg)

	if disc == nil {
		t.Fatal("Expected non-nil discovery service")
	}

	// Test the discovery service
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := disc.Discover(ctx, "test", "", nil)
	if err != nil {
		t.Fatalf("Discovery failed: %v", err)
	}

	if len(result.Candidates) == 0 {
		t.Fatal("Expected candidates")
	}

	if result.Candidates[0].CandidateURL != "http://example.com" {
		t.Errorf("Expected URL 'http://example.com', got %q", result.Candidates[0].CandidateURL)
	}
}

// stringPtr returns a pointer to a string
func stringPtr(s string) *string {
	return &s
}

// TestResearchFailureTypes tests that different failure types are categorized correctly.
func TestResearchFailureTypes(t *testing.T) {
	tests := []struct {
		name     string
		rf       *ResearchFailure
		expected bool
	}{
		{
			name:     "discovery_failure is fail-fast",
			rf:       &ResearchFailure{Type: "discovery_failure"},
			expected: true,
		},
		{
			name:     "config_error is fail-fast",
			rf:       &ResearchFailure{Type: "config_error"},
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

// TestSourceProvenance tests that source provenance is preserved from discovery.
func TestSourceProvenance(t *testing.T) {
	// Create a discovery result with provenance metadata
	providerName := "searxng"
	queryPurpose := "news search"
	title := "Article Title"
	snippet := "Article snippet"
	published, _ := time.Parse(time.RFC3339, "2024-01-15T10:00:00Z")

	prov := discovery.CandidateProvenance{
		QueryIndex:   0,
		QueryPurpose: queryPurpose,
		Rank:         1,
		ProviderName: providerName,
		Title:        &title,
		Snippet:      &snippet,
		PublishedAt:  &published,
		SourceDomain: "news.example.com",
	}

	candidates := []discovery.Candidate{
		{
			CandidateURL:     "http://news.example.com/article",
			CandidateDomain:  "news.example.com",
			Provenance:       []discovery.CandidateProvenance{prov},
			BestRank:         1,
			BestQueryIndex:   0,
			BestProviderName: providerName,
			Selected:         true,
		},
	}

	// Verify the candidate has proper provenance
	if len(candidates[0].Provenance) != 1 {
		t.Fatalf("Expected 1 provenance entry, got %d", len(candidates[0].Provenance))
	}

	orig := candidates[0].Provenance[0]
	if orig.QueryPurpose != queryPurpose {
		t.Errorf("Expected query purpose %q, got %q", queryPurpose, orig.QueryPurpose)
	}
	if orig.ProviderName != providerName {
		t.Errorf("Expected provider %q, got %q", providerName, orig.ProviderName)
	}
	if orig.Title == nil || *orig.Title != title {
		t.Errorf("Expected title %q, got %v", title, orig.Title)
	}
}

// TestDiscoveryServiceInterface tests that discoveryService interface is properly implemented.
func TestDiscoveryServiceInterface(t *testing.T) {
	// This test verifies that the fake discovery service implements the interface
	var _ discoveryService = (*fakeDiscoveryService)(nil)

	// Also verify discovery.Discovery implements the interface
	plan := &planner.SearchPlan{
		OriginalTopic: "test",
		Queries:       []planner.SearchQuery{{Query: "test"}},
	}
	planner := newFakePlanner(plan)
	provider := newFakeSearchProvider(nil)

	cfg := discovery.DefaultConfig()
	disc := NewDiscoveryService(planner, provider, cfg)

	var _ discoveryService = disc
}
