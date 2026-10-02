package discovery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/candidate"
	"github.com/Mundo-Dolphins/local-newsroom/internal/planner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/search"
)

// =============================================================================
// Fake Test Dependencies
// =============================================================================

// fakePlanner implements planner for testing.
type fakePlanner struct {
	searchPlan   *planner.SearchPlan
	shouldError  bool
	errorMessage string
}

// NewFakePlanner creates a fake planner with a fixed search plan.
func NewFakePlanner(plan *planner.SearchPlan) *fakePlanner {
	return &fakePlanner{
		searchPlan:  plan,
		shouldError: false,
	}
}

// WithError configures the planner to return an error.
func (f *fakePlanner) WithError(err error) *fakePlanner {
	f.shouldError = true
	f.errorMessage = err.Error()
	return f
}

// Generate implements Planner.
func (f *fakePlanner) Generate(ctx context.Context, topic string, language string, timeRangeHint *planner.TimeRangeHint) (*planner.SearchPlan, error) {
	if f.shouldError {
		return nil, errors.New(f.errorMessage)
	}
	return f.searchPlan, nil
}

// fakeSearchProvider implements search.Provider for testing.
type fakeSearchProvider struct {
	results   []search.Response
	error     error
	shouldErr bool
}

// NewFakeProvider creates a fake provider with fixed results.
func NewFakeProvider(results []search.Response) *fakeSearchProvider {
	return &fakeSearchProvider{
		results: results,
		error:   nil,
	}
}

// WithError configures the provider to return an error.
func (f *fakeSearchProvider) WithError(err error) *fakeSearchProvider {
	f.shouldErr = true
	f.error = err
	return f
}

// Search implements search.Provider.
func (f *fakeSearchProvider) Search(ctx context.Context, req search.Request) (search.Response, error) {
	if f.shouldErr {
		return search.Response{}, f.error
	}

	// Return the first response that matches the query, or empty
	for _, resp := range f.results {
		if resp.OriginalQuery == req.Query {
			return resp, nil
		}
	}
	// Fallback: return a default response
	return search.Response{
		OriginalQuery: req.Query,
		Results:       []search.SearchResult{},
		ProviderInfo:  map[string]string{"provider": "fake"},
	}, nil
}

// =============================================================================
// Helper Functions
// =============================================================================

func buildSearchResult(url, provider string, rank int) search.SearchResult {
	title := "Sample Article"
	snippet := "A sample article snippet"
	published, _ := time.Parse(time.RFC3339, "2024-01-15T10:00:00Z")

	return search.SearchResult{
		ID:           fmt.Sprintf("result-%d", rank),
		URL:          url,
		Title:        &title,
		Snippet:      &snippet,
		ProviderName: provider,
		Rank:         rank,
		PublishedAt:  &published,
		SourceDomain: "example.com",
	}
}

func buildFakeSearchPlan(topics ...string) *planner.SearchPlan {
	queries := make([]planner.SearchQuery, len(topics))
	for i, t := range topics {
		queries[i] = planner.SearchQuery{
			Query:   t,
			Purpose: fmt.Sprintf("Search for %s", t),
		}
	}

	return &planner.SearchPlan{
		OriginalTopic: "Test Topic",
		Queries:       queries,
	}
}

// =============================================================================
// Test Cases
// =============================================================================

func TestDiscover_NormalMultiQuery(t *testing.T) {
	// Create a search plan with 3 queries
	plan := buildFakeSearchPlan(
		"European AI regulations 2024",
		"EU artificial intelligence act overview",
		"EU AI Act official source",
	)

	// Create search responses with different results
	responses := []search.Response{
		{
			OriginalQuery: "European AI regulations 2024",
			Results: []search.SearchResult{
				buildSearchResult("https://example.com/regulation", "searxng", 1),
				buildSearchResult("https://example.org/news", "searxng", 2),
			},
			ProviderInfo: map[string]string{"provider": "searxng"},
		},
		{
			OriginalQuery: "EU artificial intelligence act overview",
			Results: []search.SearchResult{
				buildSearchResult("https://europa.eu/ai-act", "searxng", 1),
				buildSearchResult("https://example.com/regulation", "searxng", 3), // Duplicate!
			},
			ProviderInfo: map[string]string{"provider": "searxng"},
		},
		{
			OriginalQuery: "EU AI Act official source",
			Results: []search.SearchResult{
				buildSearchResult("https://europa.eu/official", "searxng", 1),
			},
			ProviderInfo: map[string]string{"provider": "searxng"},
		},
	}

	disc := NewDiscovery(
		NewFakePlanner(plan),
		NewFakeProvider(responses),
		DefaultConfig(),
	)

	result, err := disc.Discover(context.Background(), "European AI regulations", "en", nil)

	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	// Should have candidates
	if len(result.Candidates) == 0 {
		t.Fatal("Expected candidates, got none")
	}

	// Should have 3 planned queries
	if len(result.PlannedQueries) != 3 {
		t.Fatalf("Expected 3 planned queries, got %d", len(result.PlannedQueries))
	}

	// Should have 3 query results
	if len(result.QueryResults) != 3 {
		t.Fatalf("Expected 3 query results, got %d", len(result.QueryResults))
	}

	// All queries should succeed
	for i, qr := range result.QueryResults {
		if qr.Status != QueryStatusSuccess {
			t.Errorf("Query %d: expected success, got %s", i, qr.Status)
		}
	}

	// Candidates should be ordered by query index, then rank
	expectedOrder := []string{
		"https://example.com/regulation", // Query 0, rank 1 (best)
		"https://example.org/news",       // Query 0, rank 2
		"https://europa.eu/ai-act",       // Query 1, rank 1
		"https://europa.eu/official",     // Query 2, rank 1
	}

	if len(result.Candidates) != len(expectedOrder) {
		t.Errorf("Expected %d candidates, got %d", len(expectedOrder), len(result.Candidates))
	}

	for i, expURL := range expectedOrder {
		if i >= len(result.Candidates) {
			break
		}
		if result.Candidates[i].CandidateURL != expURL {
			t.Errorf("Candidate %d: expected %s, got %s", i, expURL, result.Candidates[i].CandidateURL)
		}
	}
}

func TestDiscover_DuplicateURLsAcrossQueries(t *testing.T) {
	plan := buildFakeSearchPlan("test query 1", "test query 2")

	// Same URL appears in both queries at different ranks
	responses := []search.Response{
		{
			OriginalQuery: "test query 1",
			Results: []search.SearchResult{
				buildSearchResult("https://example.com/article", "searxng", 2),
				buildSearchResult("https://other.com/other", "searxng", 1),
			},
		},
		{
			OriginalQuery: "test query 2",
			Results: []search.SearchResult{
				buildSearchResult("https://example.com/article", "searxng", 1), // Same URL, better rank
				buildSearchResult("https://another.com/another", "searxng", 1),
			},
		},
	}

	disc := NewDiscovery(
		NewFakePlanner(plan),
		NewFakeProvider(responses),
		DefaultConfig(),
	)

	result, err := disc.Discover(context.Background(), "test topic", "en", nil)

	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	// Should deduplicate to 3 unique candidates
	if len(result.Candidates) != 3 {
		t.Errorf("Expected 3 candidates after deduplication, got %d", len(result.Candidates))
	}

	// The deduplicated candidate should have the best rank (1, not 2)
	// and be attributed to query 1 (where it had rank 1)
	for _, c := range result.Candidates {
		if c.CandidateURL == "https://example.com/article" {
			if c.BestRank != 1 {
				t.Errorf("Expected best rank 1, got %d", c.BestRank)
			}
			if c.BestQueryIndex != 1 {
				t.Errorf("Expected best query index 1, got %d", c.BestQueryIndex)
			}
			if len(c.Provenance) != 2 {
				t.Errorf("Expected 2 provenance entries, got %d", len(c.Provenance))
			}
		}
	}
}

func TestDiscover_QueryFailureWithOthersSuccess(t *testing.T) {
	plan := buildFakeSearchPlan("query 1", "query 2", "query 3")

	// Query 2 will fail, but 1 and 3 will succeed
	provider := NewFakeProvider([]search.Response{})
	provider.WithError(errors.New("network timeout"))

	// Create responses manually - first query succeeds, second fails, third succeeds
	allResponses := []search.Response{
		{
			OriginalQuery: "query 1",
			Results: []search.SearchResult{
				buildSearchResult("https://example.com/result1", "searxng", 1),
			},
		},
		{
			OriginalQuery: "query 3",
			Results: []search.SearchResult{
				buildSearchResult("https://example.com/result3", "searxng", 1),
			},
		},
	}

	// We need a provider that fails on query 2
	// Use a custom provider that tracks call index
	customProvider := &customFailureProvider{
		responses: allResponses,
		failOn:    1, // Fail on the second call (query 2)
	}

	disc := NewDiscovery(
		NewFakePlanner(plan),
		customProvider,
		DefaultConfig(),
	)

	result, err := disc.Discover(context.Background(), "test topic", "en", nil)

	if err != nil {
		t.Fatalf("Discover should succeed with partial failures: %v", err)
	}

	// Should return candidates from successful queries
	if len(result.Candidates) == 0 {
		t.Fatal("Expected candidates from successful queries")
	}

	// Should have 3 query results
	if len(result.QueryResults) != 3 {
		t.Fatalf("Expected 3 query results, got %d", len(result.QueryResults))
	}

	// Query results should reflect failure
	query2Result := result.QueryResults[1]
	if query2Result.Status != QueryStatusFailed {
		t.Errorf("Query 2 should be failed, got %s", query2Result.Status)
	}

	// Should have diagnostics about partial failure
	hasPartialWarning := false
	for _, diag := range result.Diagnostics {
		if diag.Code == "partial_discovery" {
			hasPartialWarning = true
			break
		}
	}
	if !hasPartialWarning {
		t.Error("Expected partial_discovery diagnostic")
	}
}

// customFailureProvider allows controlling which calls fail
type customFailureProvider struct {
	responses []search.Response
	callIndex int
	failOn    int
}

func (p *customFailureProvider) Search(ctx context.Context, req search.Request) (search.Response, error) {
	p.callIndex++

	if p.callIndex == p.failOn+1 {
		return search.Response{}, errors.New("simulated network failure")
	}

	// Find matching response
	for _, resp := range p.responses {
		if resp.OriginalQuery == req.Query {
			return resp, nil
		}
	}
	return search.Response{}, errors.New("no response found")
}

func TestDiscover_AllQueriesFail(t *testing.T) {
	plan := buildFakeSearchPlan("query 1", "query 2")

	// Provider fails on both queries
	provider := NewFakeProvider([]search.Response{})
	provider.WithError(errors.New("all queries failed"))

	disc := NewDiscovery(
		NewFakePlanner(plan),
		provider,
		DefaultConfig(),
	)

	_, err := disc.Discover(context.Background(), "test topic", "en", nil)

	// Should fail
	if err == nil {
		t.Fatal("Expected discovery to fail when all queries fail")
	}

	// Check error type
	de := IsDiscoveryError(err)
	if de == nil {
		t.Fatalf("Expected DiscoveryError, got %T", err)
	}

	if de.Code != "discovery_failed" {
		t.Errorf("Expected 'discovery_failed' code, got %s", de.Code)
	}

	// Should have query results showing failures in the error
	if len(de.QueryResults) != 2 {
		t.Fatalf("Expected 2 query results in error, got %d", len(de.QueryResults))
	}

	for i, qr := range de.QueryResults {
		if qr.Status != QueryStatusFailed {
			t.Errorf("Query %d should be failed, got %s", i, qr.Status)
		}
	}
}

func TestDiscover_EmptyResults(t *testing.T) {
	plan := buildFakeSearchPlan("test query")

	// Provider returns empty results
	provider := NewFakeProvider([]search.Response{
		{
			OriginalQuery: "test query",
			Results:       []search.SearchResult{},
		},
	})

	disc := NewDiscovery(
		NewFakePlanner(plan),
		provider,
		DefaultConfig(),
	)

	result, err := disc.Discover(context.Background(), "test topic", "en", nil)

	// Empty results is not an error - just returns empty candidates
	if err != nil {
		t.Errorf("Empty results should not be an error, got: %v", err)
	}

	if len(result.Candidates) != 0 {
		t.Errorf("Expected 0 candidates, got %d", len(result.Candidates))
	}
}

func TestDiscover_ConfiguredLimits(t *testing.T) {
	plan := buildFakeSearchPlan(
		"query 1", "query 2", "query 3", "query 4", "query 5", "query 6",
	)

	// Create many results to test limits
	var responses []search.Response
	for i := 1; i <= 6; i++ {
		url := fmt.Sprintf("https://example.com/result%d", i)
		responses = append(responses, search.Response{
			OriginalQuery: fmt.Sprintf("query %d", i),
			Results: []search.SearchResult{
				buildSearchResult(url, "searxng", 1),
				buildSearchResult(url+"-2", "searxng", 2),
				buildSearchResult(url+"-3", "searxng", 3),
			},
		})
	}

	// Set max queries = 3, max results per query = 2, max candidates = 4
	config := Config{
		MaxSearchQueries:         3,
		ResultsPerQuery:          2,
		MaxCandidates:            4,
		EnableDuplicateReporting: true,
	}

	disc := NewDiscovery(
		NewFakePlanner(plan),
		NewFakeProvider(responses),
		config,
	)

	result, err := disc.Discover(context.Background(), "test topic", "en", nil)

	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	// Should have exactly max queries results (3 successful + 1 cancelled for limit)
	// When max is 3, we process 0,1,2 successfully, then break on 3 with a 'limit reached' message
	expectedQueryResults := 4 // 3 successful + 1 limit notice
	if len(result.QueryResults) != expectedQueryResults {
		t.Errorf("Expected %d query results (3 max + 1 limit notice), got %d", expectedQueryResults, len(result.QueryResults))
	}

	// Should have max 4 candidates
	if len(result.Candidates) > 4 {
		t.Errorf("Expected max 4 candidates, got %d", len(result.Candidates))
	}

	// Check that limit notice appears in query results
	foundLimitNotice := false
	for _, qr := range result.QueryResults {
		if strings.Contains(qr.Error, "max queries limit reached") {
			foundLimitNotice = true
			break
		}
	}
	if !foundLimitNotice {
		t.Error("Expected 'max queries limit reached' message in query results")
	}
}

func TestDiscover_ContextCancellation(t *testing.T) {
	plan := buildFakeSearchPlan(
		"fast query 1",
		"fast query 2",
		"fast query 3",
	)

	// Provider that returns results immediately
	responses := []search.Response{
		{OriginalQuery: "fast query 1", Results: []search.SearchResult{buildSearchResult("https://example.com/1", "provider", 1)}},
		{OriginalQuery: "fast query 2", Results: []search.SearchResult{buildSearchResult("https://example.com/2", "provider", 1)}},
		{OriginalQuery: "fast query 3", Results: []search.SearchResult{buildSearchResult("https://example.com/3", "provider", 1)}},
	}
	customProvider := NewFakeProvider(responses)

	disc := NewDiscovery(
		NewFakePlanner(plan),
		customProvider,
		DefaultConfig(),
	)

	// Create a cancellable context
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel immediately before discovery starts
	cancel()

	_, err := disc.Discover(ctx, "test topic", "en", nil)

	// Should get cancellation error
	if err == nil {
		t.Fatal("Expected cancellation error")
	}

	de := IsDiscoveryError(err)
	if de == nil {
		t.Fatalf("Expected DiscoveryError, got %T", err)
	}

	if de.Code != "discovery_cancelled" {
		t.Errorf("Expected 'discovery_cancelled' code, got %s", de.Code)
	}

	// When cancelled at the start, we should have query results showing failure
	// (queries didn't run because context was already cancelled)
	if len(de.QueryResults) == 0 {
		t.Error("Expected some query results in error before cancellation")
	}
}

func TestDiscover_PlannerFailure(t *testing.T) {
	// Planner that always fails
	fakePlanner := &fakePlanner{
		shouldError:  true,
		errorMessage: "LLM model unavailable",
	}

	provider := NewFakeProvider([]search.Response{})

	disc := NewDiscovery(
		fakePlanner,
		provider,
		DefaultConfig(),
	)

	_, err := disc.Discover(context.Background(), "test topic", "en", nil)

	// Should fail with planner error
	if err == nil {
		t.Fatal("Expected discovery failure from planner")
	}

	de := IsDiscoveryError(err)
	if de == nil {
		t.Fatalf("Expected DiscoveryError, got %T", err)
	}

	if de.Code != "planner_failed" {
		t.Errorf("Expected 'planner_failed' code, got %s", de.Code)
	}

	// When planner fails, result may be nil
	// The error contains diagnostics about the failure
	if de != nil && de.Diagnostics != nil {
		// Diagnostics should contain the planner error
		foundError := false
		for _, diag := range de.Diagnostics {
			if diag.Level == DiagnosticLevelError {
				foundError = true
				break
			}
		}
		if !foundError {
			t.Error("Expected error diagnostic in planner failure")
		}
	}
}

func TestDiscover_ProvenancePreservation(t *testing.T) {
	plan := buildFakeSearchPlan("test query")

	// Create result with specific metadata
	title := "Test Article Title"
	snippet := "Test snippet text"
	published, _ := time.Parse(time.RFC3339, "2024-06-15T12:00:00Z")

	result := search.SearchResult{
		ID:           "test-id",
		URL:          "https://example.com/article",
		Title:        &title,
		Snippet:      &snippet,
		ProviderName: "test-provider",
		Rank:         1,
		PublishedAt:  &published,
		SourceDomain: "example.com",
	}

	responses := []search.Response{
		{
			OriginalQuery: "test query",
			Results:       []search.SearchResult{result},
		},
	}

	disc := NewDiscovery(
		NewFakePlanner(plan),
		NewFakeProvider(responses),
		DefaultConfig(),
	)

	resultObj, err := disc.Discover(context.Background(), "test topic", "en", nil)

	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	if len(resultObj.Candidates) != 1 {
		t.Fatal("Expected 1 candidate")
	}

	candidate := resultObj.Candidates[0]

	// Check provenance
	if len(candidate.Provenance) != 1 {
		t.Fatalf("Expected 1 provenance entry, got %d", len(candidate.Provenance))
	}

	prov := candidate.Provenance[0]
	if prov.QueryIndex != 0 {
		t.Errorf("Expected query index 0, got %d", prov.QueryIndex)
	}
	if prov.Rank != 1 {
		t.Errorf("Expected rank 1, got %d", prov.Rank)
	}
	if prov.ProviderName != "test-provider" {
		t.Errorf("Expected provider 'test-provider', got %s", prov.ProviderName)
	}
	if prov.Title == nil || *prov.Title != title {
		t.Errorf("Expected title '%s', got %v", title, prov.Title)
	}
	if prov.Snippet == nil || *prov.Snippet != snippet {
		t.Errorf("Expected snippet '%s', got %v", snippet, prov.Snippet)
	}
	if prov.PublishedAt == nil || !prov.PublishedAt.Equal(published) {
		t.Errorf("Expected published time %v, got %v", published, prov.PublishedAt)
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
	}{
		{
			name: "valid config",
			config: Config{
				MaxSearchQueries: 5,
				ResultsPerQuery:  10,
				MaxCandidates:    0,
			},
			wantErr: false,
		},
		{
			name: "invalid max queries too low",
			config: Config{
				MaxSearchQueries: 0,
				ResultsPerQuery:  10,
				MaxCandidates:    0,
			},
			wantErr: true,
		},
		{
			name: "invalid max queries too high",
			config: Config{
				MaxSearchQueries: 11,
				ResultsPerQuery:  10,
				MaxCandidates:    0,
			},
			wantErr: true,
		},
		{
			name: "invalid results per query",
			config: Config{
				MaxSearchQueries: 5,
				ResultsPerQuery:  0,
				MaxCandidates:    0,
			},
			wantErr: true,
		},
		{
			name: "invalid results per query too high",
			config: Config{
				MaxSearchQueries: 5,
				ResultsPerQuery:  101,
				MaxCandidates:    0,
			},
			wantErr: true,
		},
		{
			name: "invalid max candidates negative",
			config: Config{
				MaxSearchQueries: 5,
				ResultsPerQuery:  10,
				MaxCandidates:    -1,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNewDiscoveryValidation(t *testing.T) {
	plan := buildFakeSearchPlan("test")

	t.Run("nil planner panics", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("Expected panic for nil planner")
			}
		}()
		NewDiscovery(nil, NewFakeProvider(nil), DefaultConfig())
	})

	t.Run("nil provider panics", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("Expected panic for nil provider")
			}
		}()
		NewDiscovery(
			NewFakePlanner(plan),
			nil,
			DefaultConfig(),
		)
	})

	t.Run("invalid config panics", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("Expected panic for invalid config")
			}
		}()
		NewDiscovery(
			NewFakePlanner(plan),
			NewFakeProvider(nil),
			Config{MaxSearchQueries: 0}, // Invalid
		)
	})
}

func TestBuildCandidates(t *testing.T) {

	// Create candidate with provenance
	title := "Test"
	prov := candidate.Discovery{
		QueryIndex:   0,
		Rank:         1,
		ProviderName: "test",
		OriginalURL:  "https://example.com",
		Title:        &title,
		SourceDomain: "example.com",
	}

	cand := candidate.Candidate{
		CandidateURL:     "https://example.com",
		CandidateDomain:  "example.com",
		OriginalURLs:     []string{"https://example.com"},
		Provenance:       []candidate.Discovery{prov},
		BestRank:         1,
		BestQueryIndex:   0,
		BestProviderName: "test",
		Selected:         true,
	}

	candidates := buildCandidates(
		[]candidate.Candidate{cand},
		[]planner.SearchQuery{{Query: "test", Purpose: "test purpose"}},
	)

	if len(candidates) != 1 {
		t.Fatal("Expected 1 candidate")
	}

	c := candidates[0]
	if c.BestQueryIndex != 0 {
		t.Errorf("Expected best query index 0, got %d", c.BestQueryIndex)
	}
	if len(c.Provenance) != 1 {
		t.Fatal("Expected 1 provenance")
	}
	if c.Provenance[0].QueryPurpose != "test purpose" {
		t.Errorf("Expected query purpose 'test purpose', got %s", c.Provenance[0].QueryPurpose)
	}
}

func TestQueryResultStatus(t *testing.T) {
	tests := []struct {
		status QueryStatus
		want   string
	}{
		{QueryStatusSuccess, "success"},
		{QueryStatusPartial, "partial"},
		{QueryStatusFailed, "failed"},
		{QueryStatusCancelled, "cancelled"},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if string(tt.status) != tt.want {
				t.Errorf("Status %v, want %s", tt.status, tt.want)
			}
		})
	}
}

func TestDiscover_WithCandidateConfig(t *testing.T) {
	plan := buildFakeSearchPlan("test query")

	// Create candidates with duplicates
	title := "Test"
	url1 := "https://example.com/article"
	url2 := "https://example.com/article?utm_source=test"

	result1 := search.SearchResult{
		ID:           "1",
		URL:          url1,
		Title:        &title,
		ProviderName: "test",
		Rank:         1,
		SourceDomain: "example.com",
	}
	result2 := search.SearchResult{
		ID:           "2",
		URL:          url2,
		Title:        &title,
		ProviderName: "test",
		Rank:         2,
		SourceDomain: "example.com",
	}

	res := []search.Response{{
		OriginalQuery: "test query",
		Results:       []search.SearchResult{result1, result2},
	}}

	// Test with CandidateConfig set
	cfg := candidate.Config{
		MaxCandidates:        0,
		UseWildcardAnalytics: true,
	}

	config := Config{
		MaxSearchQueries:         5,
		ResultsPerQuery:          search.DefaultLimit,
		MaxCandidates:            10,
		CandidateConfig:          &cfg,
		EnableDuplicateReporting: true,
	}

	disc := NewDiscovery(
		NewFakePlanner(plan),
		NewFakeProvider(res),
		config,
	)

	result, err := disc.Discover(context.Background(), "test topic", "en", nil)

	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	// Should have 1 candidate (duplicates collapsed)
	if len(result.Candidates) != 1 {
		t.Errorf("Expected 1 candidate (duplicates collapsed), got %d", len(result.Candidates))
	}

	// Should have duplicate reporting diagnostic
	hasDupReport := false
	for _, diag := range result.Diagnostics {
		if diag.Code == "duplicates_found" {
			hasDupReport = true
			break
		}
	}
	if !hasDupReport {
		t.Error("Expected duplicates_found diagnostic with EnableDuplicateReporting=true")
	}
}

func TestConfig_MergePlannerConfig(t *testing.T) {
	config := Config{
		MaxSearchQueries: 3,
		ResultsPerQuery:  10,
		MaxCandidates:    0,
	}

	plannerCfg := planner.Config{
		MinQueries:      1,
		MaxQueries:      5,
		Temperature:     0.5,
		MaxOutputTokens: 4000,
	}

	merged := config.MergePlannerConfig(plannerCfg)

	// Planner wants 5 but discovery only allows 3
	if merged.MaxQueries != 3 {
		t.Errorf("Expected MaxQueries=3 (capped by discovery), got %d", merged.MaxQueries)
	}

	// Test when planner has lower value
	plannerCfg2 := planner.Config{
		MinQueries:      1,
		MaxQueries:      2, // Lower than discovery
		Temperature:     0.5,
		MaxOutputTokens: 4000,
	}
	merged2 := config.MergePlannerConfig(plannerCfg2)
	if merged2.MaxQueries != 2 {
		t.Errorf("Expected MaxQueries=2 (from planner), got %d", merged2.MaxQueries)
	}
}

func TestConfig_ToSearchRequest(t *testing.T) {
	config := Config{
		MaxSearchQueries:         5,
		ResultsPerQuery:          20,
		MaxCandidates:            10,
		EnableDuplicateReporting: false,
	}

	query := planner.SearchQuery{
		Query:    "test query",
		Purpose:  "test purpose",
		Language: "en",
		TimeRange: &planner.TimeRangeHint{
			StartDate: "2024-01-01",
			EndDate:   "2024-12-31",
		},
	}

	req := config.ToSearchRequest(query)

	if req.Query != "test query" {
		t.Errorf("Expected query 'test query', got %q", req.Query)
	}
	if req.Limit != 20 {
		t.Errorf("Expected limit 20, got %d", req.Limit)
	}
	if req.Language != "en" {
		t.Errorf("Expected language 'en', got %q", req.Language)
	}
	if req.TimeRange != "2024-01-01..2024-12-31" {
		t.Errorf("Expected time_range '2024-01-01..2024-12-31', got %q", req.TimeRange)
	}
}

func TestConfig_ValidateInvalidCandidateConfig(t *testing.T) {
	config := Config{
		MaxSearchQueries:         5,
		ResultsPerQuery:          10,
		MaxCandidates:            10,
		EnableDuplicateReporting: false,
		CandidateConfig: &candidate.Config{
			MaxCandidates: -1, // Invalid
		},
	}

	err := config.Validate()
	if err == nil {
		t.Error("Expected validation error for invalid CandidateConfig")
	}
}

func TestBuildCandidates_NoQueryMatches(t *testing.T) {

	// Create candidate with query index that doesn't match plan
	prov := candidate.Discovery{
		QueryIndex:   99, // Index that doesn't exist
		Rank:         1,
		ProviderName: "test",
		OriginalURL:  "https://example.com",
		SourceDomain: "example.com",
	}

	cand := candidate.Candidate{
		CandidateURL:     "https://example.com",
		CandidateDomain:  "example.com",
		OriginalURLs:     []string{"https://example.com"},
		Provenance:       []candidate.Discovery{prov},
		BestRank:         1,
		BestQueryIndex:   99,
		BestProviderName: "test",
		Selected:         true,
	}

	candidates := buildCandidates(
		[]candidate.Candidate{cand},
		[]planner.SearchQuery{{Query: "test", Purpose: "test purpose"}},
	)

	if len(candidates) != 1 {
		t.Fatal("Expected 1 candidate")
	}

	// Query purpose should be empty since index doesn't match
	if candidates[0].Provenance[0].QueryPurpose != "" {
		t.Errorf("Expected empty query purpose for out-of-range index, got %q", candidates[0].Provenance[0].QueryPurpose)
	}
}
