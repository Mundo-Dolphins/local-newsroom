// Package discovery implements an automatic source-discovery service that
// orchestrates search query planning, execution, and candidate selection.
//
// It combines:
//   - A pluggable search query planner (e.g., LLM-based)
//   - A pluggable search provider (e.g., SearXNG or mock)
//   - A deterministic candidate selector (deduplication + ranking)
//
// The service:
//   - Enforces configurable budgets (max queries, results per query, max candidates)
//   - Tolerates partial query failures
//   - Preserves search provenance for each discovered candidate
//   - Respects context cancellation
//
// # Example Usage
//
//	// Create discovery service with injectable dependencies
//	disc := discovery.NewDiscovery(
//		planner.New(plannerClient, "local-llm", planner.DefaultConfig()),
//		provider,
//		discovery.DefaultConfig(),
//	)
//
//	// Run discovery
//	result, err := disc.Discover(ctx, "European AI regulations 2024", "en", nil)
//	if err != nil {
//	    // Handle discovery failure
//	}
//
//	// Use candidates for fetching
//	for _, c := range result.Candidates {
//	    fmt.Println(c.CandidateURL)
//	}
//
// # Partial Failures
//
// If some search queries fail but others succeed, discovery returns:
//   - The collected candidates from successful queries
//   - Diagnostics indicating which queries failed
//   - Warnings about partial discovery
//
// If ALL queries fail, discovery returns an error with detailed diagnostics.
package discovery

import (
	"context"
	"fmt"
	"strings"

	"github.com/Mundo-Dolphins/local-newsroom/internal/candidate"
	"github.com/Mundo-Dolphins/local-newsroom/internal/planner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/search"
)

// Planner is the interface for search query planning.
//
// Implementations generate a SearchPlan from a research topic.
// The planner interface is intentionally minimal to allow easy mocking.
type Planner interface {
	// Generate produces a validated SearchPlan from a research topic.
	//
	// Parameters:
	//   - ctx: Context for cancellation and timeouts
	//   - topic: The research topic to plan queries for
	//   - language: Optional 2-letter ISO language code
	//   - timeRangeHint: Optional time-range hint for search recency
	//
	// Returns:
	//   - *planner.SearchPlan: The validated search plan
	//   - error: If generation or validation fails
	Generate(ctx context.Context, topic string, language string, timeRangeHint *planner.TimeRangeHint) (*planner.SearchPlan, error)
}

// Discovery orchestrates source discovery from a research topic.
//
// It combines:
//  1. Query planning (LLM-based or alternative)
//  2. Search execution (SearXNG or mock)
//  3. Candidate selection (deterministic deduplication)
//
// Dependencies are injected for testability:
//   - Planner: Generates search queries from topic
//   - search.Provider: Executes search queries
//
// Discovery respects context cancellation and enforces configured budgets.
type Discovery struct {
	planner      Planner
	provider     search.Provider
	config       Config
	candidateCfg candidate.Config
}

// NewDiscovery creates a new Discovery instance with the given dependencies.
//
// All parameters must be non-nil; for placeholder implementations, use mocks.
// Config is validated and will cause a panic if invalid.
func NewDiscovery(planner Planner, provider search.Provider, config Config) *Discovery {
	if planner == nil {
		panic("planner must not be nil")
	}
	if provider == nil {
		panic("provider must not be nil")
	}

	if err := config.Validate(); err != nil {
		panic(fmt.Sprintf("invalid discovery config: %v", err))
	}

	// Merge configurations
	cfg := config
	if cfg.CandidateConfig != nil {
		cfg.CandidateConfig = MergeCandidateConfig(cfg.CandidateConfig, cfg.MaxCandidates)
	} else {
		defaultCandidateCfg := candidate.DefaultConfig()
		defaultCandidateCfg.MaxCandidates = cfg.MaxCandidates
		cfg.CandidateConfig = &defaultCandidateCfg
	}

	return &Discovery{
		planner:      planner,
		provider:     provider,
		config:       config,
		candidateCfg: *cfg.CandidateConfig,
	}
}

// MergeCandidateConfig merges candidate configuration with discovery limits.
func MergeCandidateConfig(base *candidate.Config, maxCandidates int) *candidate.Config {
	if base == nil {
		base = &candidate.Config{}
	}
	if maxCandidates > 0 && base.MaxCandidates == 0 {
		base.MaxCandidates = maxCandidates
	}
	return base
}

// Discover runs the full discovery pipeline from topic to candidate URLs.
//
// The pipeline:
//  1. Plans search queries using the planner
//  2. Executes each query (tolerating partial failures)
//  3. Aggregates and deduplicates results
//  4. Selects top candidates respecting budgets
//
// Parameters:
//   - ctx: Context for cancellation and timeouts
//   - topic: The research topic
//   - language: Optional 2-letter ISO language code
//   - timeRangeHint: Optional time-range hint
//
// Returns:
//   - *DiscoveryResult: The discovery result with candidates and diagnostics
//   - error: If discovery fails (e.g., all queries fail)
//
// Partial failures:
//   - If some queries fail but others succeed: returns candidates + diagnostics
//   - If all queries fail: returns error with query status
//
// Context cancellation:
//   - If context is cancelled, returns error with ErrDiscoveryCancelled
func (d *Discovery) Discover(ctx context.Context, topic string, language string, timeRangeHint *planner.TimeRangeHint) (*DiscoveryResult, error) {
	// Step 1: Generate search plan
	plan, err := d.planner.Generate(ctx, topic, language, timeRangeHint)
	if err != nil {
		return nil, &DiscoveryError{
			Code:    "planner_failed",
			Message: "failed to generate search plan: " + err.Error(),
			QueryResults: []QueryResult{{
				QueryIndex: -1,
				QueryText:  topic,
				Status:     QueryStatusFailed,
				Error:      err.Error(),
			}},
			Diagnostics: []Diagnostic{{
				Level:   DiagnosticLevelError,
				Code:    "planner_error",
				Message: err.Error(),
			}},
		}
	}

	// Check for empty plan
	if len(plan.Queries) == 0 {
		return nil, &DiscoveryError{
			Code:    "no_queries",
			Message: "planner generated no search queries",
		}
	}

	// Step 2: Execute all queries (tolerating partial failures)
	queryResults := d.executeQueries(ctx, plan)

	// Check if all queries failed
	hasSuccess := false
	for _, qr := range queryResults {
		if qr.Status == QueryStatusSuccess || qr.Status == QueryStatusPartial {
			hasSuccess = true
			break
		}
	}

	// Count successful/failed queries
	var successfulCount, failedCount int
	for _, qr := range queryResults {
		switch qr.Status {
		case QueryStatusSuccess, QueryStatusPartial:
			successfulCount++
		case QueryStatusFailed, QueryStatusCancelled:
			failedCount++
		}
	}

	// Step 3: Collect results from successful queries
	var successfulResponses []search.Response
	for _, qr := range queryResults {
		if (qr.Status == QueryStatusSuccess || qr.Status == QueryStatusPartial) && len(qr.Results) > 0 {
			successfulResponses = append(successfulResponses, search.Response{
				OriginalQuery: qr.QueryText,
				Results:       qr.Results,
				ProviderInfo:  qr.Metadata,
			})
		}
	}

	// Step 4: Aggregate and deduplicate results
	aggResult := candidate.New(d.candidateCfg).Aggregate(successfulResponses)

	// Step 5: Select top candidates
	candidatesResult := candidate.New(d.candidateCfg).Select(aggResult)

	// Step 6: Build candidates for return
	candidates := buildCandidates(candidatesResult.Candidates, plan.Queries)

	// Step 7: Build diagnostics
	diagnostics := d.buildDiagnostics(queryResults, candidatesResult, plan.Queries)

	// Check for context cancellation FIRST (before declaring failure)
	if ctx.Err() != nil {
		return nil, &DiscoveryError{
			Code:         "discovery_cancelled",
			Message:      "discovery was cancelled",
			QueryResults: queryResults,
			Diagnostics:  diagnostics,
		}
	}

	// Step 8: Check for discovery failure (no candidates at all)
	if len(candidates) == 0 && len(successfulResponses) == 0 {
		// Only error if we have no successful queries
		if !hasSuccess {
			return nil, &DiscoveryError{
				Code:         "discovery_failed",
				Message:      "all search queries failed; no usable sources discovered",
				QueryResults: queryResults,
				Diagnostics:  diagnostics,
			}
		}
		// Otherwise, empty but not an error (possible with aggressive filtering)
	}

	// Build result with duplicate reporting if enabled
	var totalDiscovered int
	if d.config.EnableDuplicateReporting {
		totalDiscovered = candidatesResult.TotalCandidates
	}

	return &DiscoveryResult{
		Candidates:              candidates,
		PlannedQueries:          plan.Queries,
		QueryResults:            queryResults,
		Diagnostics:             diagnostics,
		TotalDiscoveredURLs:     totalDiscovered,
		TotalCandidatesReturned: len(candidates),
		FailedQueryCount:        failedCount,
		SuccessfulQueryCount:    successfulCount,
	}, nil
}

// executeQueries runs all queries in the plan, tolerating partial failures.
// Returns query results for all queries, including failed ones.
func (d *Discovery) executeQueries(ctx context.Context, plan *planner.SearchPlan) []QueryResult {
	results := make([]QueryResult, 0, len(plan.Queries))

	for i, query := range plan.Queries {
		// Check for context cancellation before each query
		if ctx.Err() != nil {
			results = append(results, QueryResult{
				QueryIndex:   i,
				QueryText:    query.Query,
				Status:       QueryStatusCancelled,
				Error:        fmt.Sprintf("context cancelled: %v", ctx.Err()),
				ProviderName: d.getProviderName(),
			})
			break
		}

		// Apply max queries limit (early exit to respect budget)
		if i >= d.config.MaxSearchQueries {
			results = append(results, QueryResult{
				QueryIndex: i,
				QueryText:  query.Query,
				Status:     QueryStatusCancelled,
				Error:      fmt.Sprintf("max queries limit reached (%d)", d.config.MaxSearchQueries),
			})
			break
		}

		// Build search request
		searchReq := d.config.ToSearchRequest(query)

		// Execute search
		resp, err := d.provider.Search(ctx, searchReq)

		if err != nil {
			// Check for context cancellation
			if ctx.Err() != nil {
				results = append(results, QueryResult{
					QueryIndex:   i,
					QueryText:    query.Query,
					Status:       QueryStatusCancelled,
					Error:        fmt.Sprintf("context cancelled: %v", ctx.Err()),
					ProviderName: "unknown",
				})
				break
			}

			// Record failed query
			results = append(results, QueryResult{
				QueryIndex:      i,
				QueryText:       query.Query,
				Status:          QueryStatusFailed,
				ResultsReturned: 0,
				Error:           err.Error(),
				ProviderName:    d.getProviderName(),
			})
			continue
		}

		// Check for context cancellation after successful response
		if ctx.Err() != nil {
			results = append(results, QueryResult{
				QueryIndex:      i,
				QueryText:       query.Query,
				Status:          QueryStatusCancelled,
				ResultsReturned: len(resp.Results),
				ProviderName:    d.getProviderName(),
			})
			break
		}

		// Record successful query
		results = append(results, QueryResult{
			QueryIndex:      i,
			QueryText:       query.Query,
			Status:          QueryStatusSuccess,
			ResultsReturned: len(resp.Results),
			Results:         resp.Results,
			ProviderName:    d.getProviderName(),
			Metadata:        resp.ProviderInfo,
		})
	}

	return results
}

// getProviderName returns the provider name for diagnostics.
// It attempts to detect the provider type, falling back to a generic name.
func (d *Discovery) getProviderName() string {
	// Use the response's original query to infer provider
	// This is a heuristic - in tests, responses typically come with ProviderName set
	// The actual provider name would ideally come from a config field or interface method
	return "provider"
}

// buildDiagnostics creates diagnostic messages from query results.
func (d *Discovery) buildDiagnostics(queryResults []QueryResult, candidatesResult *candidate.CandidatesResult, queries []planner.SearchQuery) []Diagnostic {
	var diags []Diagnostic

	// Track failed queries for diagnostics
	var failedQueries []int
	var successfulQueries []int

	for _, qr := range queryResults {
		switch qr.Status {
		case QueryStatusFailed:
			failedQueries = append(failedQueries, qr.QueryIndex)
			diags = append(diags, Diagnostic{
				Level:      DiagnosticLevelError,
				Code:       "query_failed",
				Message:    fmt.Sprintf("Query %d failed: %s", qr.QueryIndex, qr.Error),
				QueryIndex: qr.QueryIndex,
			})
		case QueryStatusCancelled:
			failedQueries = append(failedQueries, qr.QueryIndex)
			diags = append(diags, Diagnostic{
				Level:      DiagnosticLevelWarning,
				Code:       "query_cancelled",
				Message:    fmt.Sprintf("Query %d was cancelled: %s", qr.QueryIndex, qr.Error),
				QueryIndex: qr.QueryIndex,
			})
		case QueryStatusSuccess:
			successfulQueries = append(successfulQueries, qr.QueryIndex)
		}
	}

	// Add summary diagnostics
	if len(failedQueries) > 0 && len(successfulQueries) > 0 {
		// Partial failure - some succeeded, some failed
		var failedText []string
		for _, idx := range failedQueries {
			if idx < 0 {
				failedText = append(failedText, "plan generation")
			} else {
				var purpose string
				if idx < len(queries) {
					purpose = fmt.Sprintf(" (\"%s\")", queries[idx].Purpose)
				}
				failedText = append(failedText, fmt.Sprintf("query %d%s", idx, purpose))
			}
		}
		diags = append(diags, Diagnostic{
			Level:   DiagnosticLevelWarning,
			Code:    "partial_discovery",
			Message: fmt.Sprintf("Partial discovery: %d failed (%s), %d succeeded", len(failedQueries), strings.Join(failedText, ", "), len(successfulQueries)),
		})
	} else if len(failedQueries) > 0 {
		// All failed
		diags = append(diags, Diagnostic{
			Level:   DiagnosticLevelError,
			Code:    "full_discovery_failed",
			Message: fmt.Sprintf("Discovery failed: all %d queries failed", len(failedQueries)),
		})
	}

	// Add budget enforcement diagnostics
	if d.config.MaxSearchQueries > 0 && len(queryResults) < len(queries) {
		diags = append(diags, Diagnostic{
			Level:   DiagnosticLevelInfo,
			Code:    "queries_limited",
			Message: fmt.Sprintf("Query execution limited to %d (plan had %d)", d.config.MaxSearchQueries, len(queries)),
		})
	}

	if d.config.MaxCandidates > 0 && candidatesResult != nil {
		if candidatesResult.TotalCandidates > d.config.MaxCandidates {
			diags = append(diags, Diagnostic{
				Level:   DiagnosticLevelInfo,
				Code:    "candidates_limited",
				Message: fmt.Sprintf("Candidate selection limited to %d (discovered %d)", d.config.MaxCandidates, candidatesResult.TotalCandidates),
			})
		}
	}

	// Add duplicate reporting
	if d.config.EnableDuplicateReporting && candidatesResult != nil {
		if len(candidatesResult.DuplicateGroups) > 0 {
			diags = append(diags, Diagnostic{
				Level:   DiagnosticLevelInfo,
				Code:    "duplicates_found",
				Message: fmt.Sprintf("Found %d duplicate URL groups that were collapsed", len(candidatesResult.DuplicateGroups)),
				Details: map[string]interface{}{
					"total_original_urls": candidatesResult.TotalCandidates,
					"unique_candidates":   len(candidatesResult.Candidates),
				},
			})
		}
	}

	// Add rejected URL info
	if candidatesResult != nil && len(candidatesResult.RejectedURLs) > 0 {
		rejectionReasons := make(map[string]int)
		for _, ru := range candidatesResult.RejectedURLs {
			rejectionReasons[ru.Reason]++
		}
		var reasonTexts []string
		for reason, count := range rejectionReasons {
			reasonTexts = append(reasonTexts, fmt.Sprintf("%d %s", count, reason))
		}
		diags = append(diags, Diagnostic{
			Level:   DiagnosticLevelInfo,
			Code:    "urls_rejected",
			Message: fmt.Sprintf("Rejected %d URLs during selection: %s", len(candidatesResult.RejectedURLs), strings.Join(reasonTexts, ", ")),
		})
	}

	return diags
}

// buildCandidates converts candidate.Candidate to discovery.Candidate.
func buildCandidates(candidates []candidate.Candidate, queries []planner.SearchQuery) []Candidate {
	result := make([]Candidate, 0, len(candidates))

	for _, c := range candidates {
		provenance := make([]CandidateProvenance, 0, len(c.Provenance))
		for _, p := range c.Provenance {
			// Find the query purpose for this query index
			var purpose string
			if p.QueryIndex >= 0 && p.QueryIndex < len(queries) {
				purpose = queries[p.QueryIndex].Purpose
			}

			provenance = append(provenance, CandidateProvenance{
				QueryIndex:   p.QueryIndex,
				QueryPurpose: purpose,
				Rank:         p.Rank,
				ProviderName: p.ProviderName,
				Title:        p.Title,
				Snippet:      p.Snippet,
				PublishedAt:  p.PublishedAt,
				SourceDomain: p.SourceDomain,
			})
		}

		result = append(result, Candidate{
			CandidateURL:     c.CandidateURL,
			CandidateDomain:  c.CandidateDomain,
			Provenance:       provenance,
			BestRank:         c.BestRank,
			BestQueryIndex:   c.BestQueryIndex,
			BestProviderName: c.BestProviderName,
			Selected:         c.Selected,
		})
	}

	return result
}
