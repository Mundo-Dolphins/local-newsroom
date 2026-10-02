package discovery

import (
	"errors"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/planner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/search"
)

// DiscoveryResult represents the complete output of a discovery run.
//
// It contains:
//   - Selected candidates ready for fetching
//   - Query execution status for each planned query
//   - Diagnostics about partial failures and tracking
//
// The candidates are ordered by their best discovery (query index, then rank),
// with fair distribution across queries when limits apply.
type DiscoveryResult struct {
	// Candidates is the ordered list of selected source candidates.
	// Each candidate preserves provenance from the search queries that found it.
	Candidates []Candidate

	// PlannedQueries is the list of queries that were planned for discovery.
	PlannedQueries []planner.SearchQuery

	// QueryResults contains per-query execution status.
	// Index i corresponds to query PlannedQueries[i].
	QueryResults []QueryResult

	// Diagnostics contains warnings and informational messages about the discovery.
	// This includes partial failure information and tracking.
	Diagnostics []Diagnostic

	// TotalDiscoveredURLs is the total number of unique URLs discovered
	// before deduplication and selection.
	TotalDiscoveredURLs int

	// TotalCandidatesReturned is the number of candidates in the result.
	TotalCandidatesReturned int

	// FailedQueryCount is the number of queries that failed completely.
	FailedQueryCount int

	// SuccessfulQueryCount is the number of queries that produced usable results.
	SuccessfulQueryCount int
}

// QueryResult represents the outcome of a single search query execution.
type QueryResult struct {
	// QueryIndex is the 0-based index of the query in the plan.
	QueryIndex int `json:"query_index"`

	// QueryText is the search query that was executed.
	QueryText string `json:"query_text"`

	// Status indicates the outcome of the query execution.
	// One of: "success", "partial", "failed", "cancelled".
	Status QueryStatus `json:"status"`

	// ResultsReturned is the number of search results received.
	ResultsReturned int `json:"results_returned"`

	// Results contains the actual search results if the query succeeded.
	// Empty if the query failed or was cancelled.
	Results []search.SearchResult `json:"results,omitempty"`

	// Error is the error message if the query failed.
	// Only populated when Status is "failed" or "cancelled".
	Error string `json:"error,omitempty"`

	// ProviderName is the name of the search provider used.
	ProviderName string `json:"provider_name,omitempty"`

	// Metadata contains additional information about the query execution.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// QueryStatus represents the status of a query execution.
type QueryStatus string

const (
	// QueryStatusSuccess indicates the query completed successfully
	// with results.
	QueryStatusSuccess QueryStatus = "success"

	// QueryStatusPartial indicates the query completed but with
	// missing results or warnings (e.g., some engines didn't respond).
	QueryStatusPartial QueryStatus = "partial"

	// QueryStatusFailed indicates the query failed due to an error.
	// No results were returned.
	QueryStatusFailed QueryStatus = "failed"

	// QueryStatusCancelled indicates the query was cancelled
	// (context cancelled or deadline exceeded).
	QueryStatusCancelled QueryStatus = "cancelled"
)

// Diagnostic represents a message about the discovery process.
//
// Diagnostics are used to:
//   - Report warnings about partial failures
//   - Provide information about tracking (duplicates, rejections)
//   - Explain budget enforcement actions
type Diagnostic struct {
	// Level indicates the severity of the diagnostic.
	// One of: "info", "warning", "error".
	Level DiagnosticLevel `json:"level"`

	// Code is a machine-readable code for the diagnostic.
	Code string `json:"code"`

	// Message is a human-readable description.
	Message string `json:"message"`

	// Details contains additional structured information.
	Details map[string]interface{} `json:"details,omitempty"`

	// QueryIndex is the query index this diagnostic relates to, if applicable.
	QueryIndex int `json:"query_index,omitempty"`
}

// DiagnosticLevel represents the severity of a diagnostic message.
type DiagnosticLevel string

const (
	// DiagnosticLevelInfo is informational, no action needed.
	DiagnosticLevelInfo DiagnosticLevel = "info"

	// DiagnosticLevelWarning indicates a recoverable issue.
	DiagnosticLevelWarning DiagnosticLevel = "warning"

	// DiagnosticLevelError indicates a problem that may affect results.
	DiagnosticLevelError DiagnosticLevel = "error"
)

// Candidate represents a discovered URL ready for fetching.
//
// It wraps candidate.Candidate with additional discovery metadata.
type Candidate struct {
	// CandidateURL is the normalized, deduplicated URL.
	CandidateURL string `json:"candidate_url"`

	// CandidateDomain is the normalized domain.
	CandidateDomain string `json:"candidate_domain"`

	// Provenance lists all discovery events for this candidate.
	Provenance []CandidateProvenance `json:"provenance"`

	// BestRank is the lowest (best) result rank discovered for this URL.
	BestRank int `json:"best_rank"`

	// BestQueryIndex is the query index that discovered it with best rank.
	BestQueryIndex int `json:"best_query_index"`

	// BestProviderName is the provider that returned it with best rank.
	BestProviderName string `json:"best_provider_name"`

	// Selected indicates whether this candidate passed selection filtering
	// (when MaxCandidates is configured).
	Selected bool `json:"selected"`
}

// CandidateProvenance represents a single discovery event for a candidate.
type CandidateProvenance struct {
	// QueryIndex is the 0-based index of the search query.
	QueryIndex int `json:"query_index"`

	// QueryPurpose is the purpose of the search query.
	QueryPurpose string `json:"query_purpose"`

	// Rank is the 1-based position in the search results.
	Rank int `json:"rank"`

	// ProviderName is the search provider.
	ProviderName string `json:"provider_name"`

	// Title is the result title, if available.
	Title *string `json:"title,omitempty"`

	// Snippet is the result snippet, if available.
	Snippet *string `json:"snippet,omitempty"`

	// PublishedAt is the publication date, if available.
	PublishedAt *time.Time `json:"published_at,omitempty"`

	// SourceDomain is the domain extracted from the URL.
	SourceDomain string `json:"source_domain"`
}

// DiscoveryError represents a failure in the discovery process.
type DiscoveryError struct {
	// Code is a machine-readable error code.
	Code string `json:"code"`

	// Message is a human-readable description.
	Message string `json:"message"`

	// QueryResults contains the state of query execution at time of failure.
	// This can be used to understand partial failures.
	QueryResults []QueryResult `json:"query_results,omitempty"`

	// Diagnostics contains additional diagnostic information.
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

// Error implements the error interface.
func (e *DiscoveryError) Error() string {
	return e.Message
}

// IsDiscoveryError checks if an error is a DiscoveryError.
func IsDiscoveryError(err error) *DiscoveryError {
	var de *DiscoveryError
	if errors.As(err, &de) {
		return de
	}
	return nil
}

// Common discovery error codes.
var (
	// ErrDiscoveryFailed indicates all queries failed with no usable results.
	ErrDiscoveryFailed = &DiscoveryError{
		Code:    "discovery_failed",
		Message: "all search queries failed; no usable sources discovered",
	}

	// ErrDiscoveryCancelled indicates discovery was cancelled (context cancelled).
	ErrDiscoveryCancelled = &DiscoveryError{
		Code:    "discovery_cancelled",
		Message: "discovery was cancelled",
	}

	// ErrPlannerFailed indicates the search query planner failed.
	ErrPlannerFailed = &DiscoveryError{
		Code:    "planner_failed",
		Message: "failed to generate search plan",
	}

	// ErrNoQueries indicates no queries were generated for discovery.
	ErrNoQueries = &DiscoveryError{
		Code:    "no_queries",
		Message: "no search queries to execute",
	}

	// ErrEmptyResults indicates queries completed but produced no results.
	ErrEmptyResults = &DiscoveryError{
		Code:    "empty_results",
		Message: "search queries completed but produced no results",
	}
)
