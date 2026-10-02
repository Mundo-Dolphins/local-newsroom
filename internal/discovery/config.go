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
	"errors"
	"fmt"

	"github.com/Mundo-Dolphins/local-newsroom/internal/candidate"
	"github.com/Mundo-Dolphins/local-newsroom/internal/planner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/search"
)

// Config holds configuration for discovery.
//
// It enforces three key budgets:
//  1. Search query budget: Maximum number of queries to execute
//  2. Results-per-query budget: Maximum results consumed per query
//  3. Candidate budget: Maximum output candidate URLs
//
// Config is validated at creation time; invalid configs cause NewDiscovery to fail.
type Config struct {
	// MaxSearchQueries limits the maximum number of search queries to execute.
	// If 0, defaults to planner.DefaultConfig().MaxQueries (5).
	// Valid range: 1-10.
	MaxSearchQueries int

	// ResultsPerQuery limits the maximum results consumed per search query.
	// This prevents excessive result consumption from a single query.
	// If 0, uses search.DefaultLimit (10).
	// Valid range: 1-100.
	ResultsPerQuery int

	// MaxCandidates limits the maximum number of candidate URLs to return.
	// If 0, no limit is applied (all deduplicated results are returned).
	// The candidate selector's round-robin fairness logic applies.
	MaxCandidates int

	// CandidateConfig configures the deterministic candidate selector.
	// If nil, uses candidate.DefaultConfig().
	CandidateConfig *candidate.Config

	// EnableDuplicateReporting enables tracking of duplicate URL groups
	// in the discovery result diagnostics.
	EnableDuplicateReporting bool
}

// DefaultConfig returns sensible defaults for discovery.
func DefaultConfig() Config {
	return Config{
		MaxSearchQueries:         5,
		ResultsPerQuery:          search.DefaultLimit,
		MaxCandidates:            0, // No limit
		CandidateConfig:          nil,
		EnableDuplicateReporting: false,
	}
}

// Validate validates the discovery configuration.
// Returns an error if any field is invalid.
func (c Config) Validate() error {
	// Validate MaxSearchQueries
	if c.MaxSearchQueries < 1 {
		return errors.New("MaxSearchQueries must be >= 1")
	}
	if c.MaxSearchQueries > 10 {
		return errors.New("MaxSearchQueries must be <= 10")
	}

	// Validate ResultsPerQuery
	if c.ResultsPerQuery < 1 {
		return errors.New("ResultsPerQuery must be >= 1")
	}
	if c.ResultsPerQuery > 100 {
		return errors.New("ResultsPerQuery must be <= 100")
	}

	// Validate MaxCandidates (0 = unlimited, otherwise positive)
	if c.MaxCandidates < 0 {
		return errors.New("MaxCandidates must be >= 0")
	}

	// Validate CandidateConfig
	if c.CandidateConfig != nil {
		if err := validateCandidateConfig(c.CandidateConfig); err != nil {
			return fmt.Errorf("invalid CandidateConfig: %w", err)
		}
	}

	return nil
}

// validateCandidateConfig performs additional validation on candidate config.
func validateCandidateConfig(cfg *candidate.Config) error {
	if cfg.MaxCandidates < 0 {
		return errors.New("CandidateConfig.MaxCandidates must be >= 0")
	}
	return nil
}

// MergePlannerConfig returns a planner.Config that respects MaxSearchQueries.
//
// If the planner's MaxQueries is within bounds, it's used. Otherwise,
// MaxSearchQueries is applied.
func (c Config) MergePlannerConfig(plannerCfg planner.Config) planner.Config {
	// If planner has custom MaxQueries, respect it if within discovery bounds
	queries := plannerCfg.MaxQueries
	if queries == 0 {
		// Default: use discovery's MaxSearchQueries
		queries = c.MaxSearchQueries
	} else if queries > c.MaxSearchQueries {
		// Planner wants more queries than discovery allows; cap it
		queries = c.MaxSearchQueries
	}
	plannerCfg.MaxQueries = queries
	return plannerCfg
}

// ToSearchRequest transforms a SearchQuery into a search.Request.
func (c Config) ToSearchRequest(query planner.SearchQuery) search.Request {
	req := search.Request{
		Query: query.Query,
		Limit: c.ResultsPerQuery,
	}

	if query.Language != "" {
		req.Language = query.Language
	}

	if query.TimeRange != nil {
		var timeRange string
		if query.TimeRange.StartDate != "" && query.TimeRange.EndDate != "" {
			timeRange = query.TimeRange.StartDate + ".." + query.TimeRange.EndDate
		} else if query.TimeRange.StartDate != "" {
			timeRange = "from:" + query.TimeRange.StartDate
		} else if query.TimeRange.EndDate != "" {
			timeRange = "to:" + query.TimeRange.EndDate
		}
		req.TimeRange = timeRange
	}

	return req
}
