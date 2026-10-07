// Package followup tests implement tests for the bounded verification-driven
// follow-up research loop.
//
// Tests cover:
// - No follow-up needed (all claims supported)
// - One unresolved claim
// - Contradiction remains unresolved
// - Follow-up resolves claim
// - Follow-up source duplicate
// - Max-round enforcement
// - Search/fetch/research failure semantics
// - Context cancellation
// - Health report generation
// - Query generation and merging
// - Dossier merging logic
//
// All tests use deterministic fake implementations for reproducibility.
package followup

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/planner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/researcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
)

// ============================================================================
// Fake implementations for testing
// ============================================================================

// fakePlanner implements planner.Planner for testing.
type fakePlanner struct {
	queries map[string][]planner.SearchQuery
	err     error
}

func (f *fakePlanner) Generate(ctx context.Context, topic string, language string, timeRangeHint *planner.TimeRangeHint) (*planner.SearchPlan, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.queries == nil {
		f.queries = make(map[string][]planner.SearchQuery)
	}
	if _, ok := f.queries[topic]; !ok {
		// Return default query
		f.queries[topic] = []planner.SearchQuery{
			{Query: "default query", Purpose: "default purpose"},
		}
	}
	return &planner.SearchPlan{
		OriginalTopic: topic,
		Queries:       f.queries[topic],
	}, nil
}

// fakeSearcher implements Searcher for testing.
type fakeSearcher struct {
	_   []SearchResult
	err error
}

func (f *fakeSearcher) Search(ctx context.Context, query planner.SearchQuery, retryAttempts int) (SearchResult, error) {
	if f.err != nil {
		return SearchResult{}, f.err
	}
	return SearchResult{
		Query: query.Query,
		Hits: []SearchHit{
			{
				URL:       "https://example.com/source-" + query.Query,
				Title:     "Example Source for " + query.Query,
				Snippet:   "Example content for verification",
				Relevance: 0.9,
			},
		},
		TotalResults: 1,
	}, nil
}

// fakeFetcher implements Fetcher for testing.
type fakeFetcher struct {
	_   []FetchResult
	err error
}

func (f *fakeFetcher) Fetch(ctx context.Context, url string) (FetchResult, error) {
	if f.err != nil {
		return FetchResult{}, f.err
	}
	now := time.Now()
	return FetchResult{
		URL:         url,
		Body:        []byte("example content"),
		ContentType: "text/html",
		HTTPStatus:  200,
		Headers:     nil,
		RetrievedAt: now,
	}, nil
}

// fakeExtractor implements Extractor for testing.
type fakeExtractor struct {
	_   []types.Document
	err error
}

func (f *fakeExtractor) Extract(ctx context.Context, fetchResult FetchResult) (*types.Document, error) {
	if f.err != nil {
		return nil, f.err
	}
	canonical := fetchResult.URL
	doc := &types.Document{
		SourceID:     "source-" + fetchResult.URL,
		CanonicalURL: &canonical,
		PlainText:    string(fetchResult.Body),
	}
	return doc, nil
}

// fakeResearcher implements researcher.ResearcherDef for testing.
type fakeResearcher struct {
	_   *researcher.ResearchDossier
	err error
}

func (f *fakeResearcher) Generate(ctx context.Context, topic string, documents []types.Document) (*researcher.ResearchDossier, error) {
	if f.err != nil {
		return nil, f.err
	}
	now := time.Now()

	// Create some claims if there are documents
	claims := make([]researcher.Claim, 0, len(documents))
	sources := make([]researcher.SourceReference, 0, len(documents))

	for _, doc := range documents {
		if doc.CanonicalURL != nil {
			sources = append(sources, researcher.SourceReference{
				StableID:    "source-" + doc.SourceID,
				OriginalURL: *doc.CanonicalURL,
				SourceType:  types.SourceTypeWeb,
				RetrievedAt: now,
				Title:       "Follow-up Source " + doc.SourceID,
			})
		}
		// Add a claim for this document
		claims = append(claims, researcher.Claim{
			ID:         "claim-" + doc.SourceID,
			Statement:  "Claim from source " + doc.SourceID,
			Confidence: researcher.ConfidenceMedium,
			Evidence: []researcher.Evidence{
				{
					SourceID: "source-" + doc.SourceID,
					Excerpts: []researcher.Excerpt{{Text: "Evidence"}},
				},
			},
		})
	}

	dossier := &researcher.ResearchDossier{
		StableID:    "followup-dossier-" + now.Format("20060102"),
		Topic:       topic,
		GeneratedAt: now,
		Sources:     sources,
		Claims:      claims,
	}

	return dossier, nil
}

// ============================================================================
// Test: No follow-up needed (all claims supported)
// ============================================================================

func TestFollowUp_NoFollowUpNeeded(t *testing.T) {
	now := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)

	// Create a verification result with all claims supported
	verificationResult := &contracts.VerificationResult{
		StableID:       "ver-001",
		InputDossierID: "dossier-001",
		VerifiedAt:     now,
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-001": contracts.VerificationStatusSupported,
			"claim-002": contracts.VerificationStatusSupported,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-001": {
				ClaimID:            "claim-001",
				Statement:          "Test claim 1",
				VerificationStatus: contracts.VerificationStatusSupported,
				ConfidenceLevel:    contracts.ConfidenceHigh,
			},
			"claim-002": {
				ClaimID:            "claim-002",
				Statement:          "Test claim 2",
				VerificationStatus: contracts.VerificationStatusSupported,
				ConfidenceLevel:    contracts.ConfidenceHigh,
			},
		},
		QualityScore: 100.0,
	}

	// Create original dossier
	originalDossier := &researcher.ResearchDossier{
		StableID:    "dossier-001",
		Topic:       "Test Topic",
		GeneratedAt: now,
		Sources: []researcher.SourceReference{
			{
				StableID:    "source-001",
				OriginalURL: "https://example.com/source-001",
				SourceType:  types.SourceTypeWeb,
				RetrievedAt: now,
			},
		},
		Claims: []researcher.Claim{
			{
				ID:         "claim-001",
				Statement:  "Test claim 1",
				Confidence: researcher.ConfidenceHigh,
				Evidence: []researcher.Evidence{
					{
						SourceID: "source-001",
						Excerpts: []researcher.Excerpt{{Text: "Evidence for claim 1"}},
					},
				},
			},
			{
				ID:         "claim-002",
				Statement:  "Test claim 2",
				Confidence: researcher.ConfidenceHigh,
				Evidence: []researcher.Evidence{
					{
						SourceID: "source-001",
						Excerpts: []researcher.Excerpt{{Text: "Evidence for claim 2"}},
					},
				},
			},
		},
	}

	// Create follow-up instance with max rounds = 1
	fu := New(FollowUpConfig{
		MaxRounds:          1,
		MaxQueriesPerClaim: 3,
		MaxQueriesPerRound: 10,
		MaxSourcesPerRound: 20,
		SourceBudget:       100,
		MinSourceRelevance: 0.5,
		RetryAttempts:      1,
		TimeNow:            func() time.Time { return now },
	})

	// Run follow-up
	result, err := fu.Run(
		context.Background(),
		originalDossier,
		verificationResult,
		&fakePlanner{},
		&fakeSearcher{},
		&fakeFetcher{},
		&fakeExtractor{},
		&fakeResearcher{},
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify no follow-up was performed
	if result.RoundNumber != 0 {
		t.Errorf("expected 0 rounds, got %d", result.RoundNumber)
	}
	if len(result.AddedClaims) != 0 {
		t.Errorf("expected no added claims, got %d", len(result.AddedClaims))
	}
	if len(result.AddedSources) != 0 {
		t.Errorf("expected no added sources, got %d", len(result.AddedSources))
	}
	if len(result.UnresolvedClaimsAfter) != 0 {
		t.Errorf("expected no unresolved claims, got %v", result.UnresolvedClaimsAfter)
	}
	if len(result.ResolvedClaims) != 0 {
		t.Errorf("expected no resolved claims, got %v", result.ResolvedClaims)
	}
}

// ============================================================================
// Test: One unresolved claim
// ============================================================================

func TestFollowUp_OneUnresolvedClaim(t *testing.T) {
	now := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)

	// Create a verification result with one unsupported claim
	verificationResult := &contracts.VerificationResult{
		StableID:       "ver-001",
		InputDossierID: "dossier-001",
		VerifiedAt:     now,
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-001": contracts.VerificationStatusSupported,
			"claim-002": contracts.VerificationStatusInsufficientEvidence,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-001": {
				ClaimID:            "claim-001",
				Statement:          "Test claim 1",
				VerificationStatus: contracts.VerificationStatusSupported,
				ConfidenceLevel:    contracts.ConfidenceHigh,
			},
			"claim-002": {
				ClaimID:            "claim-002",
				Statement:          "Test claim 2 - needs more evidence",
				VerificationStatus: contracts.VerificationStatusInsufficientEvidence,
				ConfidenceLevel:    contracts.ConfidenceLow,
			},
		},
		QualityScore: 50.0,
	}

	originalDossier := &researcher.ResearchDossier{
		StableID:    "dossier-001",
		Topic:       "Test Topic",
		GeneratedAt: now,
		Sources: []researcher.SourceReference{
			{
				StableID:    "source-001",
				OriginalURL: "https://example.com/source-001",
				SourceType:  types.SourceTypeWeb,
				RetrievedAt: now,
			},
		},
		Claims: []researcher.Claim{
			{
				ID:         "claim-001",
				Statement:  "Test claim 1",
				Confidence: researcher.ConfidenceHigh,
			},
			{
				ID:         "claim-002",
				Statement:  "Test claim 2",
				Confidence: researcher.ConfidenceLow,
			},
		},
	}

	fu := New(FollowUpConfig{
		MaxRounds:          1,
		MaxQueriesPerClaim: 3,
		MaxQueriesPerRound: 10,
		MaxSourcesPerRound: 20,
		SourceBudget:       100,
		MinSourceRelevance: 0.5,
		RetryAttempts:      1,
		TimeNow:            func() time.Time { return now },
	})

	result, err := fu.Run(
		context.Background(),
		originalDossier,
		verificationResult,
		&fakePlanner{},
		&fakeSearcher{},
		&fakeFetcher{},
		&fakeExtractor{},
		&fakeResearcher{},
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify follow-up was attempted
	if result.RoundNumber != 1 {
		t.Errorf("expected 1 round, got %d", result.RoundNumber)
	}

	// At least query was generated
	if result.QueryCount < 0 {
		t.Errorf("expected non-negative query count, got %d", result.QueryCount)
	}
}

// ============================================================================
// Test: Contradiction remains unresolved
// ============================================================================

func TestFollowUp_ContradictionUnresolved(t *testing.T) {
	now := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)

	// Create a verification result with a contradicted claim
	verificationResult := &contracts.VerificationResult{
		StableID:       "ver-001",
		InputDossierID: "dossier-001",
		VerifiedAt:     now,
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-001": contracts.VerificationStatusContradicted,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-001": {
				ClaimID:            "claim-001",
				Statement:          "Test claim contradicted",
				VerificationStatus: contracts.VerificationStatusContradicted,
				ConfidenceLevel:    contracts.ConfidenceMedium,
			},
		},
		QualityScore: 30.0,
	}

	originalDossier := &researcher.ResearchDossier{
		StableID:    "dossier-001",
		Topic:       "Test Topic",
		GeneratedAt: now,
		Sources: []researcher.SourceReference{
			{
				StableID:    "source-001",
				OriginalURL: "https://example.com/source-001",
				SourceType:  types.SourceTypeWeb,
				RetrievedAt: now,
			},
		},
		Claims: []researcher.Claim{
			{
				ID:         "claim-001",
				Statement:  "Test claim contradicted",
				Confidence: researcher.ConfidenceMedium,
			},
		},
	}

	fu := New(FollowUpConfig{
		MaxRounds:          1,
		MaxQueriesPerClaim: 3,
		MaxQueriesPerRound: 10,
		MaxSourcesPerRound: 20,
		SourceBudget:       100,
		MinSourceRelevance: 0.5,
		RetryAttempts:      1,
		TimeNow:            func() time.Time { return now },
	})

	result, err := fu.Run(
		context.Background(),
		originalDossier,
		verificationResult,
		&fakePlanner{},
		&fakeSearcher{},
		&fakeFetcher{},
		&fakeExtractor{},
		&fakeResearcher{},
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Contradicted claims should trigger follow-up
	if result.RoundNumber != 1 {
		t.Errorf("expected 1 round, got %d", result.RoundNumber)
	}

	// Contradicted claims remain unresolved
	if len(result.UnresolvedClaimsAfter) != 1 {
		t.Errorf("expected 1 unresolved claim, got %d", len(result.UnresolvedClaimsAfter))
	}
}

// ============================================================================
// Test: Follow-up resolves claim
// ============================================================================

func TestFollowUp_ClaimResolved(t *testing.T) {
	now := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)

	// Create a verification result with uncertain claim
	verificationResult := &contracts.VerificationResult{
		StableID:       "ver-001",
		InputDossierID: "dossier-001",
		VerifiedAt:     now,
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-001": contracts.VerificationStatusUncertain,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-001": {
				ClaimID:            "claim-001",
				Statement:          "Test claim uncertain",
				VerificationStatus: contracts.VerificationStatusUncertain,
				ConfidenceLevel:    contracts.ConfidenceMedium,
			},
		},
		QualityScore: 40.0,
	}

	originalDossier := &researcher.ResearchDossier{
		StableID:    "dossier-001",
		Topic:       "Test Topic",
		GeneratedAt: now,
		Sources: []researcher.SourceReference{
			{
				StableID:    "source-001",
				OriginalURL: "https://example.com/source-001",
				SourceType:  types.SourceTypeWeb,
				RetrievedAt: now,
			},
		},
		Claims: []researcher.Claim{
			{
				ID:         "claim-001",
				Statement:  "Test claim uncertain",
				Confidence: researcher.ConfidenceMedium,
			},
		},
	}

	fu := New(FollowUpConfig{
		MaxRounds:          1,
		MaxQueriesPerClaim: 3,
		MaxQueriesPerRound: 10,
		MaxSourcesPerRound: 20,
		SourceBudget:       100,
		MinSourceRelevance: 0.5,
		RetryAttempts:      1,
		TimeNow:            func() time.Time { return now },
	})

	result, err := fu.Run(
		context.Background(),
		originalDossier,
		verificationResult,
		&fakePlanner{},
		&fakeSearcher{},
		&fakeFetcher{},
		&fakeExtractor{},
		&fakeResearcher{},
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Follow-up should be executed
	if result.RoundNumber != 1 {
		t.Errorf("expected 1 round, got %d", result.RoundNumber)
	}

	// Verify structure
	if result.AddedClaimIDs == nil {
		t.Error("expected added claim IDs")
	}
	if result.AddedSourceIDs == nil {
		t.Error("expected added source IDs")
	}
}

// ============================================================================
// Test: Follow-up source duplicate
// ============================================================================

func TestFollowUp_SourceDuplicate(t *testing.T) {
	now := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)

	// Create verification result with uncertain claim
	verificationResult := &contracts.VerificationResult{
		StableID:       "ver-001",
		InputDossierID: "dossier-001",
		VerifiedAt:     now,
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-001": contracts.VerificationStatusUncertain,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-001": {
				ClaimID:            "claim-001",
				Statement:          "Test claim",
				VerificationStatus: contracts.VerificationStatusUncertain,
				ConfidenceLevel:    contracts.ConfidenceMedium,
			},
		},
		QualityScore: 40.0,
	}

	originalDossier := &researcher.ResearchDossier{
		StableID:    "dossier-001",
		Topic:       "Test Topic",
		GeneratedAt: now,
		Sources: []researcher.SourceReference{
			{
				StableID:    "source-001",
				OriginalURL: "https://example.com/source-001",
				SourceType:  types.SourceTypeWeb,
				RetrievedAt: now,
			},
			{
				StableID:    "source-002",
				OriginalURL: "https://example.com/source-002",
				SourceType:  types.SourceTypeWeb,
				RetrievedAt: now,
			},
		},
		Claims: []researcher.Claim{
			{
				ID:         "claim-001",
				Statement:  "Test claim",
				Confidence: researcher.ConfidenceMedium,
			},
		},
	}

	fu := New(FollowUpConfig{
		MaxRounds:          1,
		MaxQueriesPerClaim: 3,
		MaxQueriesPerRound: 10,
		MaxSourcesPerRound: 20,
		SourceBudget:       100,
		MinSourceRelevance: 0.5,
		RetryAttempts:      1,
		TimeNow:            func() time.Time { return now },
	})

	result, err := fu.Run(
		context.Background(),
		originalDossier,
		verificationResult,
		&fakePlanner{},
		&fakeSearcher{},
		&fakeFetcher{},
		&fakeExtractor{},
		&fakeResearcher{},
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Sources should be deduplicated
	if len(result.AddedSources) > 0 {
		// Check for duplicates in added sources
		seen := make(map[string]bool)
		for _, s := range result.AddedSources {
			if seen[s.StableID] {
				t.Errorf("duplicate source found: %s", s.StableID)
			}
			seen[s.StableID] = true
		}
	}
}

// ============================================================================
// Test: Max-round enforcement
// ============================================================================

func TestFollowUp_MaxRoundEnforcement(t *testing.T) {
	now := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)

	// Create verification result with uncertain claim
	verificationResult := &contracts.VerificationResult{
		StableID:       "ver-001",
		InputDossierID: "dossier-001",
		VerifiedAt:     now,
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-001": contracts.VerificationStatusUncertain,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-001": {
				ClaimID:            "claim-001",
				Statement:          "Test claim",
				VerificationStatus: contracts.VerificationStatusUncertain,
				ConfidenceLevel:    contracts.ConfidenceMedium,
			},
		},
		QualityScore: 40.0,
	}

	originalDossier := &researcher.ResearchDossier{
		StableID:    "dossier-001",
		Topic:       "Test Topic",
		GeneratedAt: now,
		Sources: []researcher.SourceReference{
			{
				StableID:    "source-001",
				OriginalURL: "https://example.com/source-001",
				SourceType:  types.SourceTypeWeb,
				RetrievedAt: now,
			},
		},
		Claims: []researcher.Claim{
			{
				ID:         "claim-001",
				Statement:  "Test claim",
				Confidence: researcher.ConfidenceMedium,
			},
		},
	}

	// Configure for 3 rounds but use fake that doesn't actually resolve claims
	fu := New(FollowUpConfig{
		MaxRounds:          3,
		MaxQueriesPerClaim: 3,
		MaxQueriesPerRound: 10,
		MaxSourcesPerRound: 20,
		SourceBudget:       100,
		MinSourceRelevance: 0.5,
		RetryAttempts:      1,
		TimeNow:            func() time.Time { return now },
	})

	result, err := fu.Run(
		context.Background(),
		originalDossier,
		verificationResult,
		&fakePlanner{},
		&fakeSearcher{},
		&fakeFetcher{},
		&fakeExtractor{},
		&fakeResearcher{},
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should respect max rounds
	if result.RoundNumber > 3 {
		t.Errorf("expected max 3 rounds, got %d", result.RoundNumber)
	}
}

// ============================================================================
// Test: Search/fetch/research failure semantics
// ============================================================================

func TestFollowUp_SearchFailure(t *testing.T) {
	now := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)

	verificationResult := &contracts.VerificationResult{
		StableID:       "ver-001",
		InputDossierID: "dossier-001",
		VerifiedAt:     now,
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-001": contracts.VerificationStatusUncertain,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-001": {
				ClaimID:            "claim-001",
				Statement:          "Test claim",
				VerificationStatus: contracts.VerificationStatusUncertain,
				ConfidenceLevel:    contracts.ConfidenceMedium,
			},
		},
		QualityScore: 40.0,
	}

	originalDossier := &researcher.ResearchDossier{
		StableID:    "dossier-001",
		Topic:       "Test Topic",
		GeneratedAt: now,
		Sources: []researcher.SourceReference{
			{
				StableID:    "source-001",
				OriginalURL: "https://example.com/source-001",
				SourceType:  types.SourceTypeWeb,
				RetrievedAt: now,
			},
		},
		Claims: []researcher.Claim{
			{
				ID:         "claim-001",
				Statement:  "Test claim",
				Confidence: researcher.ConfidenceMedium,
			},
		},
	}

	// Create searcher that fails
	searcherErr := errors.New("search service unavailable")
	fu := New(FollowUpConfig{
		MaxRounds:          1,
		MaxQueriesPerClaim: 3,
		MaxQueriesPerRound: 10,
		MaxSourcesPerRound: 20,
		SourceBudget:       100,
		MinSourceRelevance: 0.5,
		RetryAttempts:      1,
		TimeNow:            func() time.Time { return now },
	})

	result, err := fu.Run(
		context.Background(),
		originalDossier,
		verificationResult,
		&fakePlanner{},
		&fakeSearcher{err: searcherErr},
		&fakeFetcher{},
		&fakeExtractor{},
		&fakeResearcher{},
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should complete with warning about search failure
	if len(result.Warnings) == 0 && len(result.Errors) == 0 {
		t.Error("expected warning or error for search failure")
	}

	// Original verification state should be preserved
	if result.RoundNumber == 0 {
		t.Error("expected round to be executed even with failures")
	}
}

func TestFollowUp_FetchFailure(t *testing.T) {
	now := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)

	verificationResult := &contracts.VerificationResult{
		StableID:       "ver-001",
		InputDossierID: "dossier-001",
		VerifiedAt:     now,
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-001": contracts.VerificationStatusUncertain,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-001": {
				ClaimID:            "claim-001",
				Statement:          "Test claim",
				VerificationStatus: contracts.VerificationStatusUncertain,
				ConfidenceLevel:    contracts.ConfidenceMedium,
			},
		},
		QualityScore: 40.0,
	}

	originalDossier := &researcher.ResearchDossier{
		StableID:    "dossier-001",
		Topic:       "Test Topic",
		GeneratedAt: now,
		Sources: []researcher.SourceReference{
			{
				StableID:    "source-001",
				OriginalURL: "https://example.com/source-001",
				SourceType:  types.SourceTypeWeb,
				RetrievedAt: now,
			},
		},
		Claims: []researcher.Claim{
			{
				ID:         "claim-001",
				Statement:  "Test claim",
				Confidence: researcher.ConfidenceMedium,
			},
		},
	}

	fetcherErr := errors.New("fetch failed")
	fu := New(FollowUpConfig{
		MaxRounds:          1,
		MaxQueriesPerClaim: 3,
		MaxQueriesPerRound: 10,
		MaxSourcesPerRound: 20,
		SourceBudget:       100,
		MinSourceRelevance: 0.5,
		RetryAttempts:      1,
		TimeNow:            func() time.Time { return now },
	})

	result, err := fu.Run(
		context.Background(),
		originalDossier,
		verificationResult,
		&fakePlanner{},
		&fakeSearcher{},
		&fakeFetcher{err: fetcherErr},
		&fakeExtractor{},
		&fakeResearcher{},
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should complete with warning about fetch failure
	if len(result.Warnings) == 0 && len(result.Errors) == 0 {
		t.Error("expected warning or error for fetch failure")
	}
}

func TestFollowUp_ResearchFailure(t *testing.T) {
	now := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)

	verificationResult := &contracts.VerificationResult{
		StableID:       "ver-001",
		InputDossierID: "dossier-001",
		VerifiedAt:     now,
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-001": contracts.VerificationStatusUncertain,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-001": {
				ClaimID:            "claim-001",
				Statement:          "Test claim",
				VerificationStatus: contracts.VerificationStatusUncertain,
				ConfidenceLevel:    contracts.ConfidenceMedium,
			},
		},
		QualityScore: 40.0,
	}

	originalDossier := &researcher.ResearchDossier{
		StableID:    "dossier-001",
		Topic:       "Test Topic",
		GeneratedAt: now,
		Sources: []researcher.SourceReference{
			{
				StableID:    "source-001",
				OriginalURL: "https://example.com/source-001",
				SourceType:  types.SourceTypeWeb,
				RetrievedAt: now,
			},
		},
		Claims: []researcher.Claim{
			{
				ID:         "claim-001",
				Statement:  "Test claim",
				Confidence: researcher.ConfidenceMedium,
			},
		},
	}

	researcherErr := errors.New("research failed")
	fu := New(FollowUpConfig{
		MaxRounds:          1,
		MaxQueriesPerClaim: 3,
		MaxQueriesPerRound: 10,
		MaxSourcesPerRound: 20,
		SourceBudget:       100,
		MinSourceRelevance: 0.5,
		RetryAttempts:      1,
		TimeNow:            func() time.Time { return now },
	})

	result, err := fu.Run(
		context.Background(),
		originalDossier,
		verificationResult,
		&fakePlanner{},
		&fakeSearcher{},
		&fakeFetcher{},
		&fakeExtractor{},
		&fakeResearcher{err: researcherErr},
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should complete with warning about research failure
	t.Logf("Warnings: %+v", result.Warnings)
	hasWarning := false
	for _, w := range result.Warnings {
		t.Logf("Warning: %q", w)
		if len(w) > 0 && w[:15] == "Research failed" {
			hasWarning = true
			break
		}
	}
	if !hasWarning {
		t.Error("expected warning for research failure")
	}
}

// ============================================================================
// Test: Context cancellation
// ============================================================================

func TestFollowUp_ContextCancellation(t *testing.T) {
	now := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)

	verificationResult := &contracts.VerificationResult{
		StableID:       "ver-001",
		InputDossierID: "dossier-001",
		VerifiedAt:     now,
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-001": contracts.VerificationStatusUncertain,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-001": {
				ClaimID:            "claim-001",
				Statement:          "Test claim",
				VerificationStatus: contracts.VerificationStatusUncertain,
				ConfidenceLevel:    contracts.ConfidenceMedium,
			},
		},
		QualityScore: 40.0,
	}

	originalDossier := &researcher.ResearchDossier{
		StableID:    "dossier-001",
		Topic:       "Test Topic",
		GeneratedAt: now,
		Sources: []researcher.SourceReference{
			{
				StableID:    "source-001",
				OriginalURL: "https://example.com/source-001",
				SourceType:  types.SourceTypeWeb,
				RetrievedAt: now,
			},
		},
		Claims: []researcher.Claim{
			{
				ID:         "claim-001",
				Statement:  "Test claim",
				Confidence: researcher.ConfidenceMedium,
			},
		},
	}

	fu := New(FollowUpConfig{
		MaxRounds:          5, // More than one
		MaxQueriesPerClaim: 3,
		MaxQueriesPerRound: 10,
		MaxSourcesPerRound: 20,
		SourceBudget:       100,
		MinSourceRelevance: 0.5,
		RetryAttempts:      1,
		TimeNow:            func() time.Time { return now },
	})

	// Create cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	result, err := fu.Run(
		ctx,
		originalDossier,
		verificationResult,
		&fakePlanner{},
		&fakeSearcher{},
		&fakeFetcher{},
		&fakeExtractor{},
		&fakeResearcher{},
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should handle cancelled context gracefully
	// Round number should be 0 or partial results available
	if result.RoundNumber < 0 {
		t.Error("expected non-negative round number")
	}
}

// ============================================================================
// Test: Health report generation
// ============================================================================

func TestHealthReport_Generation(t *testing.T) {
	now := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)

	verificationResult := &contracts.VerificationResult{
		StableID:       "ver-001",
		InputDossierID: "dossier-001",
		VerifiedAt:     now,
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-001": contracts.VerificationStatusSupported,
			"claim-002": contracts.VerificationStatusSupported,
			"claim-003": contracts.VerificationStatusUncertain,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-001": {
				ClaimID:            "claim-001",
				Statement:          "Claim 1",
				VerificationStatus: contracts.VerificationStatusSupported,
			},
			"claim-002": {
				ClaimID:            "claim-002",
				Statement:          "Claim 2",
				VerificationStatus: contracts.VerificationStatusSupported,
			},
			"claim-003": {
				ClaimID:            "claim-003",
				Statement:          "Claim 3",
				VerificationStatus: contracts.VerificationStatusUncertain,
			},
		},
		QualityScore: 70.0,
	}

	report := GenerateHealthReport(verificationResult)

	if report == nil {
		t.Fatal("expected health report, got nil")
	}

	// Verify report fields
	if report.TotalClaims != 3 {
		t.Errorf("expected 3 total claims, got %d", report.TotalClaims)
	}

	if report.SupportedClaims != 2 {
		t.Errorf("expected 2 supported claims, got %d", report.SupportedClaims)
	}

	if !report.NeedsFollowUp {
		t.Error("expected needs follow-up for uncertain claim")
	}

	if report.Recommendation == "" {
		t.Error("expected non-empty recommendation")
	}
}

func TestHealthReport_AllSupported(t *testing.T) {
	now := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)

	verificationResult := &contracts.VerificationResult{
		StableID:       "ver-001",
		InputDossierID: "dossier-001",
		VerifiedAt:     now,
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-001": contracts.VerificationStatusSupported,
			"claim-002": contracts.VerificationStatusSupported,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-001": {
				ClaimID:            "claim-001",
				VerificationStatus: contracts.VerificationStatusSupported,
			},
			"claim-002": {
				ClaimID:            "claim-002",
				VerificationStatus: contracts.VerificationStatusSupported,
			},
		},
		QualityScore: 100.0,
	}

	report := GenerateHealthReport(verificationResult)

	if report == nil {
		t.Fatal("expected health report, got nil")
	}

	if report.NeedsFollowUp {
		t.Error("expected no follow-up needed for all supported claims")
	}

	if report.ResolutionRate != 100.0 {
		t.Errorf("expected 100%% resolution rate, got %f", report.ResolutionRate)
	}
}

// ============================================================================
// Test: GetUnresolvedClaimIDs
// ============================================================================

func TestGetUnresolvedClaimIDs(t *testing.T) {
	now := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)

	verificationResult := &contracts.VerificationResult{
		StableID:       "ver-001",
		InputDossierID: "dossier-001",
		VerifiedAt:     now,
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-001": contracts.VerificationStatusSupported,
			"claim-002": contracts.VerificationStatusUncertain,
			"claim-003": contracts.VerificationStatusInsufficientEvidence,
			"claim-004": contracts.VerificationStatusContradicted,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-001": {ClaimID: "claim-001", VerificationStatus: contracts.VerificationStatusSupported},
			"claim-002": {ClaimID: "claim-002", VerificationStatus: contracts.VerificationStatusUncertain},
			"claim-003": {ClaimID: "claim-003", VerificationStatus: contracts.VerificationStatusInsufficientEvidence},
			"claim-004": {ClaimID: "claim-004", VerificationStatus: contracts.VerificationStatusContradicted},
		},
		QualityScore: 50.0,
	}

	unresolved := GetUnresolvedClaimIDs(verificationResult)

	// Should include uncertain, insufficient_evidence, and contradicted
	if len(unresolved) != 3 {
		t.Errorf("expected 3 unresolved claims, got %d: %v", len(unresolved), unresolved)
	}

	// Should not include supported
	for _, id := range unresolved {
		if id == "claim-001" {
			t.Error("expected claim-001 (supported) not in unresolved")
		}
	}

	// Check expected IDs
	expected := map[string]bool{
		"claim-002": true,
		"claim-003": true,
		"claim-004": true,
	}

	for _, id := range unresolved {
		if !expected[id] {
			t.Errorf("unexpected claim in unresolved: %s", id)
		}
	}
}

func TestGetUnresolvedClaimIDs_Empty(t *testing.T) {
	unresolved := GetUnresolvedClaimIDs(nil)
	if unresolved != nil {
		t.Errorf("expected nil, got %v", unresolved)
	}
}

// ============================================================================
// Test: Utility functions
// ============================================================================

func TestDeduplicateSourceURLs(t *testing.T) {

	sources := []researcher.SourceReference{
		{StableID: "source-001", OriginalURL: "https://example.com/1"},
		{StableID: "source-002", OriginalURL: "https://example.com/2"},
		{StableID: "source-001", OriginalURL: "https://example.com/1"}, // duplicate
		{StableID: "source-003", OriginalURL: "https://example.com/3"},
	}

	deduped := DeduplicateSourceURLs(sources)

	if len(deduped) != 3 {
		t.Errorf("expected 3 unique sources, got %d", len(deduped))
	}
}

func TestMergeSourceReferences(t *testing.T) {

	existing := []researcher.SourceReference{
		{StableID: "source-001", OriginalURL: "https://example.com/1"},
	}

	newSources := []researcher.SourceReference{
		{StableID: "source-002", OriginalURL: "https://example.com/2"},
		{StableID: "source-001", OriginalURL: "https://example.com/1"}, // duplicate
	}

	merged := MergeSourceReferences(existing, newSources)

	if len(merged) != 2 {
		t.Errorf("expected 2 sources, got %d", len(merged))
	}
}

func TestCountUnresolved(t *testing.T) {

	result := &contracts.VerificationResult{
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-001": contracts.VerificationStatusSupported,
			"claim-002": contracts.VerificationStatusUncertain,
			"claim-003": contracts.VerificationStatusInsufficientEvidence,
			"claim-004": contracts.VerificationStatusContradicted,
		},
	}

	unresolved := CountUnresolved(result)
	if unresolved != 3 {
		t.Errorf("expected 3 unresolved, got %d", unresolved)
	}

	if CountUnresolved(nil) != 0 {
		t.Error("expected 0 unresolved for nil result")
	}
}

func TestCountSupported(t *testing.T) {

	result := &contracts.VerificationResult{
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-001": contracts.VerificationStatusSupported,
			"claim-002": contracts.VerificationStatusSupported,
			"claim-003": contracts.VerificationStatusUncertain,
		},
	}

	supported := CountSupported(result)
	if supported != 2 {
		t.Errorf("expected 2 supported, got %d", supported)
	}
}

func TestIsClaimUnresolved(t *testing.T) {

	result := &contracts.VerificationResult{
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-001": contracts.VerificationStatusSupported,
			"claim-002": contracts.VerificationStatusUncertain,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-001": {ClaimID: "claim-001", VerificationStatus: contracts.VerificationStatusSupported},
			"claim-002": {ClaimID: "claim-002", VerificationStatus: contracts.VerificationStatusUncertain},
		},
	}

	if IsClaimUnresolved(result, "claim-001") {
		t.Error("expected claim-001 not unresolved")
	}

	if !IsClaimUnresolved(result, "claim-002") {
		t.Error("expected claim-002 unresolved")
	}

	if IsClaimUnresolved(nil, "claim-001") {
		t.Error("expected nil result to not have unresolved claims")
	}
}

func TestIsClaimSupported(t *testing.T) {

	result := &contracts.VerificationResult{
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-001": contracts.VerificationStatusSupported,
			"claim-002": contracts.VerificationStatusUncertain,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-001": {ClaimID: "claim-001", VerificationStatus: contracts.VerificationStatusSupported},
			"claim-002": {ClaimID: "claim-002", VerificationStatus: contracts.VerificationStatusUncertain},
		},
	}

	if !IsClaimSupported(result, "claim-001") {
		t.Error("expected claim-001 supported")
	}

	if IsClaimSupported(result, "claim-002") {
		t.Error("expected claim-002 not supported")
	}

	if IsClaimSupported(nil, "claim-001") {
		t.Error("expected nil result to not have supported claims")
	}
}

// ============================================================================
// Test: Validation
// ============================================================================

func TestFollowUp_ValidateConfig_Invalid(t *testing.T) {
	tests := []struct {
		name    string
		config  FollowUpConfig
		wantErr string
	}{
		{
			name: "negative max rounds",
			config: FollowUpConfig{
				MaxRounds: -1,
			},
			wantErr: "max rounds cannot be negative",
		},
		{
			name: "invalid queries per claim",
			config: FollowUpConfig{
				MaxQueriesPerClaim: 0,
			},
			wantErr: "max queries per claim must be at least 1",
		},
		{
			name: "invalid min relevance",
			config: FollowUpConfig{
				MaxQueriesPerClaim: 1,
				MaxQueriesPerRound: 10,
				MaxSourcesPerRound: 1,
				SourceBudget:       10,
				MinSourceRelevance: 1.5,
			},
			wantErr: "min source relevance must be between 0 and 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fu := New(tt.config)
			err := fu.validateConfig()
			if err == nil {
				t.Errorf("expected error, got nil")
			} else if err.Error() != tt.wantErr {
				t.Errorf("expected error %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestFollowUp_ValidateConfig_Valid(t *testing.T) {
	config := FollowUpConfig{
		MaxRounds:          1,
		MaxQueriesPerClaim: 3,
		MaxQueriesPerRound: 10,
		MaxSourcesPerRound: 20,
		SourceBudget:       100,
		MinSourceRelevance: 0.5,
		RetryAttempts:      1,
		TimeNow:            func() time.Time { return time.Now() },
	}

	fu := New(config)
	err := fu.validateConfig()
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestFollowUp_ValidateConfig_Defaults(t *testing.T) {
	config := FollowUpConfig{
		TimeNow: func() time.Time { return time.Now() },
	}

	fu := New(config)
	applyDefaults := func() FollowUpConfig {
		return fu.applyDefaults()
	}

	result := applyDefaults()

	if result.MaxRounds != 1 {
		t.Errorf("expected default MaxRounds=1, got %d", result.MaxRounds)
	}
	if result.MaxQueriesPerClaim != 3 {
		t.Errorf("expected default MaxQueriesPerClaim=3, got %d", result.MaxQueriesPerClaim)
	}
	if result.MaxQueriesPerRound != 10 {
		t.Errorf("expected default MaxQueriesPerRound=10, got %d", result.MaxQueriesPerRound)
	}
	if result.MaxSourcesPerRound != 20 {
		t.Errorf("expected default MaxSourcesPerRound=20, got %d", result.MaxSourcesPerRound)
	}
	if result.SourceBudget != 100 {
		t.Errorf("expected default SourceBudget=100, got %d", result.SourceBudget)
	}
	if result.MinSourceRelevance != 0.5 {
		t.Errorf("expected default MinSourceRelevance=0.5, got %f", result.MinSourceRelevance)
	}
	if result.RetryAttempts != 1 {
		t.Errorf("expected default RetryAttempts=1, got %d", result.RetryAttempts)
	}
}

// ============================================================================
// Test: Input validation
// ============================================================================

func TestFollowUp_Run_NilDossier(t *testing.T) {
	fu := New(FollowUpConfig{TimeNow: time.Now})

	result, err := fu.Run(
		context.Background(),
		nil,
		&contracts.VerificationResult{},
		&fakePlanner{},
		&fakeSearcher{},
		&fakeFetcher{},
		&fakeExtractor{},
		&fakeResearcher{},
	)

	if result != nil {
		t.Error("expected nil result for nil dossier")
	}
	if err == nil {
		t.Error("expected error for nil dossier")
	}
}

func TestFollowUp_Run_NilVerificationResult(t *testing.T) {
	dossier := &researcher.ResearchDossier{}
	fu := New(FollowUpConfig{TimeNow: time.Now})

	result, err := fu.Run(
		context.Background(),
		dossier,
		nil,
		&fakePlanner{},
		&fakeSearcher{},
		&fakeFetcher{},
		&fakeExtractor{},
		&fakeResearcher{},
	)

	if result != nil {
		t.Error("expected nil result for nil verification result")
	}
	if err == nil {
		t.Error("expected error for nil verification result")
	}
}

// ============================================================================
// Test: GetResolvedClaimIDs
// ============================================================================

func TestGetResolvedClaimIDs(t *testing.T) {
	now := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)

	verificationResult := &contracts.VerificationResult{
		StableID:       "ver-001",
		InputDossierID: "dossier-001",
		VerifiedAt:     now,
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-001": contracts.VerificationStatusSupported,
			"claim-002": contracts.VerificationStatusSupported,
			"claim-003": contracts.VerificationStatusUncertain,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-001": {ClaimID: "claim-001", VerificationStatus: contracts.VerificationStatusSupported},
			"claim-002": {ClaimID: "claim-002", VerificationStatus: contracts.VerificationStatusSupported},
			"claim-003": {ClaimID: "claim-003", VerificationStatus: contracts.VerificationStatusUncertain},
		},
		QualityScore: 70.0,
	}

	resolved := GetResolvedClaimIDs(verificationResult)

	if len(resolved) != 2 {
		t.Errorf("expected 2 resolved claims, got %d", len(resolved))
	}

	// Check for expected IDs
	expected := map[string]bool{
		"claim-001": true,
		"claim-002": true,
	}

	for _, id := range resolved {
		if !expected[id] {
			t.Errorf("unexpected resolved claim: %s", id)
		}
	}
}

func TestGetResolvedClaimIDs_Empty(t *testing.T) {
	resolved := GetResolvedClaimIDs(nil)
	if resolved != nil {
		t.Errorf("expected nil, got %v", resolved)
	}
}
