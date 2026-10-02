// Package candidate implements deterministic search-result normalization and
// candidate selection for the local-newsroom project.
//
// This package provides a pure deterministic layer that combines results from
// multiple search queries into a bounded unique list of HTTP/HTTPS URL candidates
// while retaining discovery provenance.
//
// # Design Principles
//
//   - No external dependencies: Works without LLMs, fetchers, or external APIs
//   - Deterministic: Given the same input, always produces the same output
//   - Conservative: Removes only well-known tracking parameters and normalizes
//     URL scheme/host casing; does not collapse potentially meaningful query params
//   - Provenance-preserving: Tracks which queries and ranks discovered each URL
//   - Fair: Uses round-robin-style selection when multiple queries find the same URL
//
// # URL Normalization Rules
//
//  1. Reject non-HTTP/HTTPS URLs (e.g., file:, mailto:, javascript:, data:)
//  2. Normalize scheme to lowercase (HTTPS -> https)
//  3. Normalize host to lowercase (EXAMPLE.COM -> example.com)
//  4. Remove URL fragments (everything after #)
//  5. Remove well-known tracking parameters (see trackingParams in config)
//  6. Preserve the path and all non-tracking query parameters exactly
//  7. Do NOT modify query parameter order (deterministic output, not canonical)
//  8. Do NOT follow redirects (existing fetcher handles this)
//
// # Candidate Selection Algorithm
//
// The selection algorithm is:
//
//  1. Collect all search results with their provenance (query index, rank, provider, etc.)
//  2. Normalize each URL
//  3. Filter to only HTTP/HTTPS URLs
//  4. Group by normalized URL
//  5. For each group, select the "best" candidate using:
//     a. Lowest rank (earlier in result list = more relevant)
//     b. Lowest query index (earlier query = higher priority)
//     c. Tie-breaker: earlier discovery (lower provenance list position)
//  6. Enforce maximum candidate limit using fair round-robin across queries
//  7. Return selected candidates in query-order, then rank-order
//
// # Provenance Preservation
//
// Each candidate retains:
//
//   - CandidateURLs: All original URLs that normalized to this candidate
//   - Provenance: For each discovery, query index, rank, provider, title, snippet, published_at
//   - SelectionRank: Position after duplicate collapse
//   - BestRank: The best rank discovered for this URL
//   - BestQueryIndex: The query that discovered it with the best rank
//
// This provenance enables later attachment to Source.Metadata without changing
// the dossier contract.
package candidate

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/search"
)

// DefaultTrackingParams is a conservative set of common tracking parameters.
// These parameters typically identify the tracking source/session rather than
// distinct content, so they can be safely removed for deduplication.
//
// Known tracking parameters:
// - utm_* (Google Campaign Manager)
// - gclid (Google Ads)
// - fbclid (Facebook Ads)
// - referral_id, referer (referral tracking)
// - _ga, _ga_*, _gid (Google Analytics)
// - mc_cid (Mailchimp Campaign ID)
// - pmid (Product Management IDs)
// - ecommerce parameters: pid, mkt_site, scid
var DefaultTrackingParams = []string{
	"utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content",
	"gclid", "gbraid", "wbraid",
	"fbclid",
	"ref", "referer", "referrer", "referral_id",
	"_ga", "_ga_*", "_gid", "_gat", // Google Analytics (wildcards expanded)
	"mc_cid", "mc_eid",
	"pid", "mkt_site", "scid",
	"ei", "eiav", "eiab", // Google autocomplete/analytics
	"eiurl", "eiit", // Google related searches
	"ncid", "ncnt", // Twitter tweet/CTR tracking
	"twclid", "li_fat_id", // LinkedIn tracking
	"dw_origin", // DigitalWombat tracking
	// Note: "from" and "src" removed - commonly used for date/content filtering
	"roas", "dpid", "dcmp", // TikTok/Drop tracking
	"aff_id", "affiliate_id", "affid", "affiliateid", // Affiliate tracking
	"iid", "instl", // Installation tracking
	"sig", "ssign", "signature", // Adobe Analytics
	"third-party", "thirdparty", // Generic third-party
}

// Config holds configuration for candidate selection.
type Config struct {
	// MaxCandidates limits the maximum number of candidate sources.
	// If 0 or negative, no limit is applied.
	MaxCandidates int

	// TrackingParams lists additional tracking parameter names to remove.
	// DefaultTrackingParams is always used first; these are appended.
	TrackingParams []string

	// UseWildcardAnalytics enables wildcard matching for Google Analytics
	// parameters (e.g., _ga_ABC matches _ga). Default: true
	UseWildcardAnalytics bool

	// KeepQueryParams allows keeping specific query parameters that would
	// normally be stripped as tracking. Used for exceptions.
	KeepQueryParams []string
}

// DefaultConfig returns a sensible default configuration.
func DefaultConfig() Config {
	return Config{
		MaxCandidates:        0, // No limit by default
		TrackingParams:       []string{},
		UseWildcardAnalytics: true,
		KeepQueryParams:      []string{},
	}
}

// Candidate represents a unique URL candidate after normalization and duplicate collapse.
//
// Each candidate preserves provenance from all sources that discovered it,
// including query index, result rank, provider, and original metadata.
//
// Provenance enables:
//   - Traceability: Know which queries found which sources
//   - Ranking explanation: Understand why a source was selected
//   - Fairness: Track distribution across queries
//   - Audit: Debug why certain sources were or weren't selected
type Candidate struct {
	// CandidateURL is the normalized, deduplicated URL.
	CandidateURL string `json:"candidate_url"`

	// CandidateDomain is the normalized domain extracted from CandidateURL.
	CandidateDomain string `json:"candidate_domain"`

	// OriginalURLs lists all original URLs that normalized to this candidate.
	// Each entry preserves the original tracking parameters, fragments (removed), etc.
	OriginalURLs []string `json:"original_urls"`

	// Provenance lists all discovery events for this candidate.
	// Each discovery includes which query found it, at what rank, and metadata.
	Provenance []Discovery `json:"provenance"`

	// SelectionRank is the position of this candidate in the final ordered list
	// after deduplication and ranking. Range: [0, len(Candidates)-1].
	SelectionRank int `json:"selection_rank"`

	// BestRank is the lowest (best) result rank discovered for this URL across all queries.
	// Lower values = higher relevance. Range: [1, highest_result_rank].
	BestRank int `json:"best_rank"`

	// BestQueryIndex is the query index (0-indexed) that discovered this URL with the best rank.
	// This helps explain why this candidate was prioritized.
	BestQueryIndex int `json:"best_query_index"`

	// BestProviderName is the provider that returned this URL with the best rank.
	BestProviderName string `json:"best_provider_name"`

	// Selected indicates whether this candidate was selected for fetching
	// (when MaxCandidates is configured and exceeded).
	Selected bool `json:"selected"`

	// CandidatesTotal is the total number of candidates before selection filtering.
	CandidatesTotal int `json:"candidates_total"`
}

// Discovery represents a single discovery of a URL from a search result.
//
// Multiple discoveries may exist for the same URL if it appeared in:
// - Multiple search results within one query
// - Multiple queries
// - Multiple providers (if multiple search backends are used)
type Discovery struct {
	// QueryIndex is the 0-based index of the search query that discovered this result.
	// Query index 0 = first query, index 1 = second query, etc.
	QueryIndex int `json:"query_index"`

	// Rank is the 1-based position of this result in the search result list.
	// Rank 1 = most relevant for that query.
	Rank int `json:"rank"`

	// ProviderName is the search provider that returned this result.
	ProviderName string `json:"provider_name"`

	// OriginalURL is the exact URL as returned by the provider.
	OriginalURL string `json:"original_url"`

	// Title is the result title from the search result.
	// If nil, the title was not available.
	Title *string `json:"title,omitempty"`

	// Snippet is the result snippet/excerpt from the search result.
	// If nil, the snippet was not available.
	Snippet *string `json:"snippet,omitempty"`

	// PublishedAt is the publication date when available.
	// If nil, the publication date was not available.
	PublishedAt *time.Time `json:"published_at,omitempty"`

	// SourceDomain is the domain extracted from the URL.
	SourceDomain string `json:"source_domain"`
}

// CandidatesResult represents the complete output of the selection process.
type CandidatesResult struct {
	// Candidates is the ordered list of selected candidates.
	Candidates []Candidate `json:"candidates"`

	// TotalCandidates is the total number of candidates before selection filtering.
	TotalCandidates int `json:"total_candidates"`

	// SelectedCount is the number of candidates that passed selection filtering.
	SelectedCount int `json:"selected_count"`

	// RejectedURLs lists URLs that were rejected (non-HTTP/S, invalid, etc.).
	RejectedURLs []RejectedURL `json:"rejected_urls,omitempty"`

	// DuplicateGroups groups URLs by their normalized form, showing which originals
	// were collapsed into which candidate.
	DuplicateGroups []DuplicateGroup `json:"duplicate_groups,omitempty"`

	// ProcessingErrors lists any errors that occurred during processing
	// (e.g., invalid URLs, parsing errors). These don't prevent selection
	// of other URLs.
	ProcessingErrors []ProcessingError `json:"processing_errors,omitempty"`
}

// RejectedURL represents a URL that was rejected during processing.
type RejectedURL struct {
	// OriginalURL is the URL that was rejected.
	OriginalURL string `json:"original_url"`

	// Reason is a human-readable explanation of why it was rejected.
	// Examples: "unsupported scheme: file", "invalid URL format"
	Reason string `json:"reason"`

	// Provenance lists which query/rank discovered this rejected URL.
	Provenance []Discovery `json:"provenance,omitempty"`
}

// DuplicateGroup shows how multiple URLs were collapsed into one candidate.
type DuplicateGroup struct {
	// NormalizedURL is the normalized form of the group.
	NormalizedURL string `json:"normalized_url"`

	// OriginalURLs lists all original URLs in this group.
	OriginalURLs []string `json:"original_urls"`

	// Count is the number of URLs in this group.
	Count int `json:"count"`
}

// ProcessingError represents an error that occurred during candidate selection.
type ProcessingError struct {
	// OriginalURL is the URL that caused the error.
	OriginalURL string `json:"original_url"`

	// Error is the error message.
	Error string `json:"error"`

	// QueryIndex is the query index where this error occurred.
	QueryIndex int `json:"query_index"`

	// Rank is the result rank where this error occurred.
	Rank int `json:"rank"`

	// ProviderName is the provider that returned this URL.
	ProviderName string `json:"provider_name"`
}

// ResultAggregator collects all search results and provides the deduplication
// and selection functionality.
type ResultAggregator struct {
	config Config
}

// New creates a new ResultAggregator with the given configuration.
func New(cfg Config) *ResultAggregator {
	if cfg.MaxCandidates < 0 {
		cfg.MaxCandidates = 0 // No limit
	}

	// Merge tracking params
	trackingParams := make([]string, len(DefaultTrackingParams))
	copy(trackingParams, DefaultTrackingParams)
	trackingParams = append(trackingParams, cfg.TrackingParams...)
	cfg.TrackingParams = trackingParams

	return &ResultAggregator{
		config: cfg,
	}
}

// AggregationResult holds intermediate aggregation state.
type AggregationResult struct {
	// AllURLs maps normalized URL -> list of all URLs that normalized to it
	AllURLs map[string][]string

	// ProvenanceByURL maps normalized URL -> list of all discoveries
	ProvenanceByURL map[string][]Discovery

	// RejectedURLs lists URLs that were rejected
	RejectedURLs []RejectedURL

	// Errors lists processing errors
	Errors []ProcessingError

	// DuplicateGroups records which URLs were collapsed
	DuplicateGroups []DuplicateGroup
}

// Aggregate collects all search results and performs deduplication,
// URL normalization, and provenance collection.
//
// inputQueries is a slice of SearchResponses, each representing results from
// one search query. The index of each response corresponds to its QueryIndex.
//
// Returns aggregation results that can be further processed with Select().
func (a *ResultAggregator) Aggregate(inputQueries []search.Response) *AggregationResult {
	result := &AggregationResult{
		AllURLs:         make(map[string][]string),
		ProvenanceByURL: make(map[string][]Discovery),
		RejectedURLs:    []RejectedURL{},
		Errors:          []ProcessingError{},
		DuplicateGroups: []DuplicateGroup{},
	}

	for queryIdx, resp := range inputQueries {
		for _, searchResult := range resp.Results {
			originalURL := searchResult.URL

			// Parse the URL
			parsed, err := url.Parse(originalURL)
			if err != nil {
				result.Errors = append(result.Errors, ProcessingError{
					OriginalURL:  originalURL,
					Error:        fmt.Sprintf("invalid URL: %v", err),
					QueryIndex:   queryIdx,
					Rank:         searchResult.Rank,
					ProviderName: searchResult.ProviderName,
				})
				continue
			}

			// Check scheme
			if !isHTTPScheme(parsed.Scheme) {
				result.RejectedURLs = append(result.RejectedURLs, RejectedURL{
					OriginalURL: originalURL,
					Reason:      fmt.Sprintf("unsupported scheme: %s", parsed.Scheme),
					Provenance: []Discovery{{
						QueryIndex:   queryIdx,
						Rank:         searchResult.Rank,
						ProviderName: searchResult.ProviderName,
						OriginalURL:  originalURL,
						Title:        searchResult.Title,
						Snippet:      searchResult.Snippet,
						PublishedAt:  searchResult.PublishedAt,
						SourceDomain: searchResult.SourceDomain,
					}},
				})
				continue
			}

			// Normalize the URL
			normalized := normalizeURL(parsed, a.config)
			normalizedStr := normalized.String()

			// Track original URL
			result.AllURLs[normalizedStr] = append(result.AllURLs[normalizedStr], originalURL)

			// Track provenance
			result.ProvenanceByURL[normalizedStr] = append(result.ProvenanceByURL[normalizedStr], Discovery{
				QueryIndex:   queryIdx,
				Rank:         searchResult.Rank,
				ProviderName: searchResult.ProviderName,
				OriginalURL:  originalURL,
				Title:        searchResult.Title,
				Snippet:      searchResult.Snippet,
				PublishedAt:  searchResult.PublishedAt,
				SourceDomain: searchResult.SourceDomain,
			})
		}
	}

	// Record duplicate groups
	for normalizedURL, originals := range result.AllURLs {
		if len(originals) > 1 {
			result.DuplicateGroups = append(result.DuplicateGroups, DuplicateGroup{
				NormalizedURL: normalizedURL,
				OriginalURLs:  originals,
				Count:         len(originals),
			})
		}
	}

	return result
}

// Select takes aggregation results and produces the final candidate list,
// applying ranking, selection, and ordering logic.
//
// Returns the final CandidatesResult with ordered, selected candidates.
func (a *ResultAggregator) Select(aggregated *AggregationResult) *CandidatesResult {
	if len(aggregated.AllURLs) == 0 {
		return &CandidatesResult{
			Candidates:       []Candidate{},
			TotalCandidates:  0,
			SelectedCount:    0,
			RejectedURLs:     aggregated.RejectedURLs,
			DuplicateGroups:  aggregated.DuplicateGroups,
			ProcessingErrors: aggregated.Errors,
		}
	}

	// Build candidates from aggregated data
	var allCandidates []Candidate
	for normalizedURL, provenanceList := range aggregated.ProvenanceByURL {
		originals := aggregated.AllURLs[normalizedURL]

		// Sort provenance by query index, then rank (for determinism)
		sort.SliceStable(provenanceList, func(i, j int) bool {
			if provenanceList[i].QueryIndex != provenanceList[j].QueryIndex {
				return provenanceList[i].QueryIndex < provenanceList[j].QueryIndex
			}
			return provenanceList[i].Rank < provenanceList[j].Rank
		})

		// Find the best discovery (lowest rank, lowest query index)
		best := bestDiscovery(provenanceList)

		// Extract domain from normalized URL
		parsed, _ := url.Parse(normalizedURL)
		domain := ""
		if parsed != nil {
			domain = strings.ToLower(parsed.Host)
		}

		// Create candidate
		cand := Candidate{
			CandidateURL:     normalizedURL,
			CandidateDomain:  domain,
			OriginalURLs:     originals,
			Provenance:       provenanceList,
			SelectionRank:    0, // Will be set later
			BestRank:         best.Rank,
			BestQueryIndex:   best.QueryIndex,
			BestProviderName: best.ProviderName,
			Selected:         true, // Will be set later if limit applies
			CandidatesTotal:  len(aggregated.AllURLs),
		}

		allCandidates = append(allCandidates, cand)
	}

	// Sort candidates: primary by best discovery (query index, then rank)
	sort.SliceStable(allCandidates, func(i, j int) bool {
		// Primary: lower best query index
		if allCandidates[i].BestQueryIndex != allCandidates[j].BestQueryIndex {
			return allCandidates[i].BestQueryIndex < allCandidates[j].BestQueryIndex
		}
		// Secondary: lower best rank
		return allCandidates[i].BestRank < allCandidates[j].BestRank
	})

	// Set selection rank
	for i := range allCandidates {
		allCandidates[i].SelectionRank = i
	}

	totalCandidates := len(allCandidates)

	// Apply max candidates limit with fair round-robin
	if a.config.MaxCandidates > 0 && totalCandidates > a.config.MaxCandidates {
		candidates := a.applySelectionWithFairness(allCandidates, a.config.MaxCandidates)
		return &CandidatesResult{
			Candidates:       candidates,
			TotalCandidates:  totalCandidates,
			SelectedCount:    len(candidates),
			RejectedURLs:     aggregated.RejectedURLs,
			DuplicateGroups:  aggregated.DuplicateGroups,
			ProcessingErrors: aggregated.Errors,
		}
	}

	return &CandidatesResult{
		Candidates:       allCandidates,
		TotalCandidates:  totalCandidates,
		SelectedCount:    totalCandidates,
		RejectedURLs:     aggregated.RejectedURLs,
		DuplicateGroups:  aggregated.DuplicateGroups,
		ProcessingErrors: aggregated.Errors,
	}
}

// bestDiscovery finds the "best" discovery from a list, using:
// 1. Lowest rank (most relevant result)
// 2. Lowest query index (earlier query wins ties)
// 3. First occurrence in the provenance list (deterministic tie-breaker)
func bestDiscovery(provenance []Discovery) Discovery {
	if len(provenance) == 0 {
		return Discovery{}
	}

	best := provenance[0]

	for i := 1; i < len(provenance); i++ {
		disc := provenance[i]
		// Lower rank is better
		if disc.Rank < best.Rank {
			best = disc
			continue
		}
		// Same rank: lower query index is better
		if disc.Rank == best.Rank && disc.QueryIndex < best.QueryIndex {
			best = disc
		}
	}

	return best
}

// applySelectionWithFairness selects candidates using a fair round-robin approach
// across queries to avoid one query consuming the entire selection budget.
//
// The algorithm:
// 1. Group candidates by their best discovery's query index
// 2. Round-robin from each group until the limit is reached
//
// This ensures proportional representation across queries.
func (a *ResultAggregator) applySelectionWithFairness(candidates []Candidate, maxCount int) []Candidate {
	if len(candidates) <= maxCount {
		return candidates
	}

	// Group candidates by best query index
	queryGroups := make(map[int][]Candidate)
	for _, cand := range candidates {
		queryGroups[cand.BestQueryIndex] = append(queryGroups[cand.BestQueryIndex], cand)
	}

	// Get sorted query indices for determinism
	queryIndices := make([]int, 0, len(queryGroups))
	for qi := range queryGroups {
		queryIndices = append(queryIndices, qi)
	}
	sort.Ints(queryIndices)

	// Round-robin selection
	var selected []Candidate
	selectedSet := make(map[string]bool) // Track selected by URL

	// Keep selecting one from each group in round-robin until we hit the limit
	round := 0
	for {
		for _, qi := range queryIndices {
			group := queryGroups[qi]
			if len(group) <= round {
				continue
			}

			cand := group[round]
			if !selectedSet[cand.CandidateURL] {
				cand.Selected = true
				selected = append(selected, cand)
				selectedSet[cand.CandidateURL] = true

				if len(selected) >= maxCount {
					break
				}
			}
		}

		// Check if we've collected enough
		if len(selected) >= maxCount {
			break
		}

		// Check if we've exhausted all groups
		hasMore := false
		for _, group := range queryGroups {
			if len(group) > round {
				hasMore = true
				break
			}
		}
		if !hasMore {
			break
		}

		round++
	}

	// Sort final selection by selection rank for consistency
	sort.SliceStable(selected, func(i, j int) bool {
		if selected[i].BestQueryIndex != selected[j].BestQueryIndex {
			return selected[i].BestQueryIndex < selected[j].BestQueryIndex
		}
		return selected[i].BestRank < selected[j].BestRank
	})

	// Ensure max count
	if len(selected) > maxCount {
		selected = selected[:maxCount]
	}

	return selected
}

// isHTTPScheme checks if the scheme is HTTP or HTTPS.
func isHTTPScheme(scheme string) bool {
	return strings.EqualFold(scheme, "http") || strings.EqualFold(scheme, "https")
}

// normalizeURL normalizes a URL according to the rules:
// 1. Lowercase scheme
// 2. Lowercase host
// 3. Remove fragment
// 4. Remove tracking query parameters
//
// Does NOT:
// - Reorder query parameters
// - Remove non-tracking query parameters
// - Follow redirects
func normalizeURL(u *url.URL, cfg Config) *url.URL {
	// Parse the URL fresh to normalize query strings properly
	// This handles edge cases like bare "?" at end of URL
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Host)
	path := u.Path
	params := u.Query()

	// Remove tracking params
	for key := range params {
		if shouldRemoveParam(key, cfg) {
			params.Del(key)
		}
	}

	// Build normalized URL
	normalized := &url.URL{
		Scheme:   scheme,
		Host:     host,
		Path:     path,
		Fragment: "", // Remove fragment
	}

	// Only set RawQuery if there are params
	if len(params) > 0 {
		normalized.RawQuery = params.Encode()
	}

	return normalized
}

// shouldRemoveParam checks if a query parameter should be removed as tracking.
//
// It handles:
// - Exact matches
// - Wildcard analytics parameters (if enabled)
// - Explicitly kept parameters override removal
func shouldRemoveParam(paramName string, cfg Config) bool {
	// Check if explicitly kept
	for _, keep := range cfg.KeepQueryParams {
		if strings.EqualFold(paramName, keep) {
			return false
		}
	}

	// Check exact match against tracking params
	for _, tracking := range cfg.TrackingParams {
		if strings.EqualFold(paramName, tracking) {
			return true
		}
	}

	// Handle wildcard analytics parameters (_ga_*)
	if cfg.UseWildcardAnalytics && strings.HasPrefix(paramName, "_ga_") {
		// _ga_* pattern matches _ga_ followed by anything
		// But _ga (without suffix) is separate
		return true
	}

	// Check for _ga style patterns (without wildcard)
	if cfg.UseWildcardAnalytics {
		for _, tracking := range cfg.TrackingParams {
			if strings.HasPrefix(tracking, "_ga_") && len(tracking) > 4 {
				// This is a wildcard pattern like _ga_*, check if paramName starts with the base
				base := strings.TrimSuffix(tracking, "*")
				if len(base) > 3 && strings.HasPrefix(paramName, base) {
					return true
				}
			}
		}
	}

	return false
}

// BuildCandidates is a convenience function that performs both aggregation
// and selection in one call.
//
// It is equivalent to:
//
//	agg := New(cfg)
//	agg.Aggregate(inputQueries)
//	agg.Select(aggregated)
func BuildCandidates(inputQueries []search.Response, cfg Config) *CandidatesResult {
	agg := New(cfg)
	aggregated := agg.Aggregate(inputQueries)
	return agg.Select(aggregated)
}
