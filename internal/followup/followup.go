package followup

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	pl "github.com/Mundo-Dolphins/local-newsroom/internal/planner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/researcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
)

// FollowUp implements a bounded verification-driven follow-up research loop.
//
// The follow-up process:
// 1. Identify unresolved claims from verification result
// 2. Generate targeted search queries for each unresolved claim
// 3. Execute search/fetch/extract for new sources
// 4. Run researcher to supplement findings
// 5. Merge new findings into the original dossier
// 6. Return follow-up results with audit trail
//
// The loop is strictly bounded by:
// - Max rounds (default: 1)
// - Max queries per claim/round
// - Max sources per round/budget
// - Context cancellation
//
// Only claims with statuses uncertain, insufficient_evidence, or contradicted
// trigger follow-up research. Supported claims are never searched.
type FollowUp struct {
	config FollowUpConfig
}

// ResearcherDef is the interface for the researcher component.
type ResearcherDef interface {
	Generate(ctx context.Context, topic string, documents []types.Document) (*researcher.ResearchDossier, error)
}

// New creates a new FollowUp instance with the given configuration.
func New(config FollowUpConfig) *FollowUp {
	if config.TimeNow == nil {
		config.TimeNow = time.Now
	}
	return &FollowUp{config: config}
}

// Run executes the bounded follow-up research loop.
//
// Parameters:
//   - ctx: Context for cancellation and timeouts
//   - originalDossier: The original ResearchDossier from initial research
//   - verificationResult: The VerificationResult identifying unresolved claims
//   - planner: Search query planner for generating targeted queries
//   - searcher: Search service for executing queries
//   - fetcher: Fetcher for retrieving search results
//   - extractor: Extractor for processing fetched content
//   - researcherService: Researcher for synthesizing new findings
//
// Returns:
//   - *FollowUpResult: Results of the follow-up loop
//   - error: If execution fails
//
// The follow-up loop:
// 1. Identifies unresolved claims
// 2. Runs bounded rounds of targeted research
// 3. Merges findings into the original dossier
// 4. Returns comprehensive results with audit trail
//
// Stop conditions:
// - All claims resolved (verified as supported)
// - Max rounds exhausted
// - Source budget exceeded
// - Context cancelled
// - Search/fetch/research failures (recorded as warnings, not failures)
func (f *FollowUp) Run(
	ctx context.Context,
	originalDossier *researcher.ResearchDossier,
	verificationResult *contracts.VerificationResult,
	planner Planner,
	searcher Searcher,
	fetcher Fetcher,
	extractor Extractor,
	researcherService ResearcherDef,
) (*FollowUpResult, error) {
	if originalDossier == nil {
		return nil, &FollowUpError{
			Type:    "validation",
			Message: "original dossier cannot be nil",
		}
	}

	if verificationResult == nil {
		return nil, &FollowUpError{
			Type:    "validation",
			Message: "verification result cannot be nil",
		}
	}

	// Validate config
	if err := f.validateConfig(); err != nil {
		return nil, &FollowUpError{
			Type:    "config_error",
			Message: err.Error(),
		}
	}

	// Apply defaults
	config := f.applyDefaults()

	// Start tracking time
	startTime := config.TimeNow()

	// Identify unresolved claims
	unresolved := GetUnresolvedClaimIDs(verificationResult)
	if len(unresolved) == 0 {
		// No unresolved claims - nothing to follow up on
		return &FollowUpResult{
			RoundNumber:           0,
			AddedClaims:           nil,
			AddedSources:          nil,
			UnresolvedClaimsAfter: nil,
			ResolvedClaims:        nil,
			AddedClaimIDs:         nil,
			AddedSourceIDs:        nil,
			Warnings:              nil,
			Errors:                nil,
			QueryCount:            0,
			FetchedCount:          0,
			ProcessingTime:        config.TimeNow().Sub(startTime),
			RoundAudit:            RoundAudit{},
		}, nil
	}

	// Initialize tracking
	var allAddedClaims []researcher.Claim
	var allAddedSources []researcher.SourceReference
	var allResolvedClaims []string
	var totalQueryCount int
	var totalFetchedCount int
	var roundAudits []RoundAudit
	var allWarnings []string
	var allErrors []string

	// Run bounded follow-up rounds
	for round := 1; round <= config.MaxRounds; round++ {
		// Check context
		select {
		case <-ctx.Done():
			return f.createPartialResult(
				round-1,
				allAddedClaims,
				allAddedSources,
				allResolvedClaims,
				unresolved,
				totalQueryCount,
				totalFetchedCount,
				roundAudits,
				allWarnings,
				allErrors,
				ctx.Err(),
			), nil
		default:
		}

		// Check if all claims are resolved
		if !f.hasUnresolvedClaims(verificationResult, unresolved) {
			break
		}

		// Execute one round of follow-up
		roundResult, err := f.executeRound(
			ctx,
			round,
			unresolved,
			verificationResult,
			originalDossier,
			planner,
			searcher,
			fetcher,
			extractor,
			researcherService,
			config,
		)
		if err != nil {
			// Record error but continue (non-fatal)
			allErrors = append(allErrors, fmt.Sprintf("Round %d error: %v", round, err))
			allWarnings = append(allWarnings, fmt.Sprintf("Round %d failed, continuing with partial results", round))

			// If all rounds failed, return what we have
			if round >= config.MaxRounds {
				return f.createPartialResult(
					round,
					allAddedClaims,
					allAddedSources,
					allResolvedClaims,
					unresolved,
					totalQueryCount,
					totalFetchedCount,
					roundAudits,
					allWarnings,
					allErrors,
					err,
				), nil
			}
			continue
		}

		// Track round results
		allAddedClaims = append(allAddedClaims, roundResult.AddedClaims...)
		allAddedSources = append(allAddedSources, roundResult.AddedSources...)
		allResolvedClaims = append(allResolvedClaims, roundResult.ResolvedClaims...)
		totalQueryCount += roundResult.QueryCount
		totalFetchedCount += roundResult.FetchedCount
		roundAudits = append(roundAudits, roundResult.RoundAudit)
		allWarnings = append(allWarnings, roundResult.Warnings...)
		allErrors = append(allErrors, roundResult.Errors...)

		// Update unresolved list for next round
		unresolved = roundResult.UnresolvedClaimsAfter

		// Check if we've made progress
		if len(unresolved) == 0 {
			break
		}

		// Check source budget
		totalSources := len(originalDossier.Sources) + len(allAddedSources)
		if config.SourceBudget > 0 && totalSources >= config.SourceBudget {
			allWarnings = append(allWarnings, fmt.Sprintf("Source budget exhausted (%d sources)", config.SourceBudget))
			break
		}
	}

	// Calculate final unresolved count
	finalUnresolved := unresolved
	if len(finalUnresolved) == 0 {
		finalUnresolved = GetUnresolvedClaimIDs(verificationResult)
	}

	return &FollowUpResult{
		RoundNumber:           len(roundAudits),
		AddedClaims:           DeduplicateClaims(allAddedClaims),
		AddedSources:          DeduplicateSourceURLs(allAddedSources),
		UnresolvedClaimsAfter: finalUnresolved,
		ResolvedClaims:        DeduplicateStrings(allResolvedClaims),
		AddedClaimIDs:         f.extractClaimIDs(allAddedClaims),
		AddedSourceIDs:        f.extractSourceIDs(allAddedSources),
		Warnings:              DeduplicateStrings(allWarnings),
		Errors:                DeduplicateStrings(allErrors),
		QueryCount:            totalQueryCount,
		FetchedCount:          totalFetchedCount,
		ProcessingTime:        config.TimeNow().Sub(startTime),
		RoundAudit:            RoundAudit{StartRound: startTime}, // Final audit is partial
	}, nil
}

// validateConfig checks that the configuration is valid.
func (f *FollowUp) validateConfig() error {
	if f.config.MaxRounds < 0 {
		return errors.New("max rounds cannot be negative")
	}
	if f.config.MaxQueriesPerClaim < 1 {
		return errors.New("max queries per claim must be at least 1")
	}
	if f.config.MaxQueriesPerRound < 1 {
		return errors.New("max queries per round must be at least 1")
	}
	if f.config.MaxQueriesPerClaim > f.config.MaxQueriesPerRound {
		return errors.New("max queries per claim cannot exceed max queries per round")
	}
	if f.config.MaxSourcesPerRound < 1 {
		return errors.New("max sources per round must be at least 1")
	}
	if f.config.SourceBudget < 1 {
		return errors.New("source budget must be at least 1")
	}
	if f.config.MinSourceRelevance < 0 || f.config.MinSourceRelevance > 1 {
		return errors.New("min source relevance must be between 0 and 1")
	}
	if f.config.RetryAttempts < 0 {
		return errors.New("retry attempts cannot be negative")
	}

	return nil
}

// applyDefaults applies default values to unspecified configuration.
func (f *FollowUp) applyDefaults() FollowUpConfig {
	config := f.config

	if config.MaxRounds == 0 {
		config.MaxRounds = 1 // Default: one additional round
	}
	if config.MaxQueriesPerClaim == 0 {
		config.MaxQueriesPerClaim = 3
	}
	if config.MaxQueriesPerRound == 0 {
		config.MaxQueriesPerRound = 10
	}
	if config.MaxSourcesPerRound == 0 {
		config.MaxSourcesPerRound = 20
	}
	if config.SourceBudget == 0 {
		config.SourceBudget = 100
	}
	if config.MinSourceRelevance == 0 {
		config.MinSourceRelevance = 0.5
	}
	if config.RetryAttempts == 0 {
		config.RetryAttempts = 1
	}

	return config
}

// hasUnresolvedClaims checks if any claims in the unresolved list are still
// unresolved in the verification result.
func (f *FollowUp) hasUnresolvedClaims(result *contracts.VerificationResult, claimIDs []string) bool {
	if result == nil || len(claimIDs) == 0 {
		return false
	}

	for _, claimID := range claimIDs {
		if IsClaimUnresolved(result, claimID) {
			return true
		}
	}

	return false
}

// executeRound executes a single round of follow-up research.
func (f *FollowUp) executeRound(
	ctx context.Context,
	roundNumber int,
	unresolved []string,
	verificationResult *contracts.VerificationResult,
	originalDossier *researcher.ResearchDossier,
	planner Planner,
	searcher Searcher,
	fetcher Fetcher,
	extractor Extractor,
	researcherService ResearcherDef,
	config FollowUpConfig,
) (*FollowUpResult, error) {
	startRound := config.TimeNow()

	var queriesGenerated []SearchQuerySpec
	var sourcesFetched []string
	var claimsAdded []researcher.Claim
	var sourcesAdded []researcher.SourceReference
	var resolvedClaims []string
	var roundWarnings []string
	var roundErrors []error
	var queryCount int
	var fetchedCount int

	// Step 1: Generate targeted queries for unresolved claims
	for _, claimID := range unresolved {
		// Generate queries for this claim
		queryHint := &pl.TimeRangeHint{}
		plan, err := planner.Generate(ctx, claimID, "en", queryHint)
		if err != nil {
			roundErrors = append(roundErrors, fmt.Errorf("generate queries for claim %s: %w", claimID, err))
			roundWarnings = append(roundWarnings, fmt.Sprintf("Failed to generate queries for claim %s", claimID))
			continue
		}

		// Apply query limits
		queries := plan.Queries
		if len(queries) > config.MaxQueriesPerClaim {
			queries = queries[:config.MaxQueriesPerClaim]
		}

		for _, q := range queries {
			queriesGenerated = append(queriesGenerated, SearchQuerySpec{
				Query:     q.Query,
				ClaimID:   claimID,
				Purpose:   q.Purpose,
				TimeRange: queryHint,
			})
		}
		queryCount += len(queries)

		// Check round-level query limit
		if queryCount >= config.MaxQueriesPerRound {
			break
		}
	}

	// Limit total queries per round
	if queryCount > config.MaxQueriesPerRound {
		queriesGenerated = queriesGenerated[:config.MaxQueriesPerRound]
		queryCount = config.MaxQueriesPerRound
	}

	// Step 2: Execute searches
	var searchHits []SearchHit
	for _, spec := range queriesGenerated {
		query := pl.SearchQuery{
			Query:   spec.Query,
			Purpose: spec.Purpose,
		}

		hitCount := 0
		for attempt := 0; attempt <= config.RetryAttempts; attempt++ {
			result, err := searcher.Search(ctx, query, config.RetryAttempts)
			if err != nil {
				if attempt < config.RetryAttempts {
					continue // Retry
				}
				roundErrors = append(roundErrors, fmt.Errorf("search for query %q: %w", query.Query, err))
				roundWarnings = append(roundWarnings, fmt.Sprintf("Search failed for query: %s", query.Query))
				continue
			}

			// Filter by relevance
			for _, hit := range result.Hits {
				if hit.Relevance >= config.MinSourceRelevance {
					searchHits = append(searchHits, hit)
					hitCount++
				}
			}
			break
		}
	}

	// Step 3: Fetch sources
	for i, hit := range searchHits {
		if i >= config.MaxSourcesPerRound {
			break
		}

		fetchResult, err := fetcher.Fetch(ctx, hit.URL)
		if err != nil {
			roundErrors = append(roundErrors, fmt.Errorf("fetch %s: %w", hit.URL, err))
			roundWarnings = append(roundWarnings, fmt.Sprintf("Failed to fetch source: %s", hit.URL))
			continue
		}

		sourcesFetched = append(sourcesFetched, hit.URL)
		fetchedCount++

		// Step 4: Extract content
		doc, err := extractor.Extract(ctx, fetchResult)
		if err != nil {
			roundErrors = append(roundErrors, fmt.Errorf("extract from %s: %w", hit.URL, err))
			roundWarnings = append(roundWarnings, fmt.Sprintf("Failed to extract from source: %s", hit.URL))
			continue
		}

		// Step 5: Run researcher to generate findings
		dossier, err := researcherService.Generate(ctx, doc.SourceID, []types.Document{*doc})
		if err != nil {
			roundErrors = append(roundErrors, fmt.Errorf("research for %s: %w", hit.URL, err))
			roundWarnings = append(roundWarnings, fmt.Sprintf("Research failed for source: %s", hit.URL))
			continue
		}

		// Add new claims
		claimsAdded = append(claimsAdded, dossier.Claims...)

		// Add new sources
		sourcesAdded = append(sourcesAdded, dossier.Sources...)
	}

	// Deduplicate
	claimsAdded = DeduplicateClaims(claimsAdded)
	sourcesAdded = DeduplicateSourceURLs(sourcesAdded)

	endRound := config.TimeNow()

	return f.roundResult(
		roundNumber,
		startRound,
		endRound,
		unresolved,
		queriesGenerated,
		sourcesFetched,
		claimsAdded,
		sourcesAdded,
		resolvedClaims,
		roundWarnings,
		roundErrors,
		queryCount,
		fetchedCount,
	), nil
}

// deduplicateStrings removes duplicate strings from a slice.
func deduplicateStrings(s []string) []string {
	seen := make(map[string]bool)
	var result []string

	for _, item := range s {
		if !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}

	return result
}

// createPartialResult creates a result from partial execution.
func (f *FollowUp) createPartialResult(
	roundNumber int,
	allAddedClaims []researcher.Claim,
	allAddedSources []researcher.SourceReference,
	allResolvedClaims []string,
	unresolved []string,
	totalQueryCount int,
	totalFetchedCount int,
	roundAudits []RoundAudit,
	allWarnings []string,
	allErrors []string,
	ctxErr error,
) *FollowUpResult {
	return &FollowUpResult{
		RoundNumber:           roundNumber,
		AddedClaims:           DeduplicateClaims(allAddedClaims),
		AddedSources:          DeduplicateSourceURLs(allAddedSources),
		UnresolvedClaimsAfter: unresolved,
		ResolvedClaims:        deduplicateStrings(allResolvedClaims),
		AddedClaimIDs:         f.extractClaimIDs(allAddedClaims),
		AddedSourceIDs:        f.extractSourceIDs(allAddedSources),
		Warnings:              DeduplicateStrings(allWarnings),
		Errors:                DeduplicateStrings(allErrors),
		QueryCount:            totalQueryCount,
		FetchedCount:          totalFetchedCount,
		ProcessingTime:        time.Duration(0), // Set appropriately
		RoundAudit:            RoundAudit{},
	}
}

// roundResult creates a result for a single round.
func (f *FollowUp) roundResult(
	roundNumber int,
	startRound, endRound time.Time,
	unresolved []string,
	queriesGenerated []SearchQuerySpec,
	sourcesFetched []string,
	claimsAdded []researcher.Claim,
	sourcesAdded []researcher.SourceReference,
	resolvedClaims []string,
	warnings []string,
	errorList []error,
	queryCount int,
	fetchedCount int,
) *FollowUpResult {
	errorStrings := make([]string, len(errorList))
	for i, err := range errorList {
		errorStrings[i] = err.Error()
	}

	return &FollowUpResult{
		RoundNumber:           roundNumber,
		AddedClaims:           claimsAdded,
		AddedSources:          sourcesAdded,
		UnresolvedClaimsAfter: unresolved,
		ResolvedClaims:        resolvedClaims,
		AddedClaimIDs:         f.extractClaimIDs(claimsAdded),
		AddedSourceIDs:        f.extractSourceIDs(sourcesAdded),
		Warnings:              warnings,
		Errors:                errorStrings,
		QueryCount:            queryCount,
		FetchedCount:          fetchedCount,
		ProcessingTime:        endRound.Sub(startRound),
		RoundAudit: RoundAudit{
			StartRound:       startRound,
			EndRound:         endRound,
			QueriesGenerated: queriesGenerated,
			SourcesFetched:   sourcesFetched,
			ClaimsAdded:      f.extractClaimIDs(claimsAdded),
			SourceIDsAdded:   f.extractSourceIDs(sourcesAdded),
		},
	}
}

// extractClaimIDs extracts claim IDs from a list of claims.
func (f *FollowUp) extractClaimIDs(claims []researcher.Claim) []string {
	ids := make([]string, 0, len(claims))
	for _, c := range claims {
		ids = append(ids, c.ID)
	}
	return ids
}

// extractSourceIDs extracts source IDs from a list of sources.
func (f *FollowUp) extractSourceIDs(sources []researcher.SourceReference) []string {
	var ids []string
	for _, s := range sources {
		ids = append(ids, s.StableID)
	}
	return ids
}
