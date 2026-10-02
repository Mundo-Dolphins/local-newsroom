// Package search defines domain contracts for generic web search operations.
//
// This package provides a provider-neutral abstraction for discovering
// candidate sources from a research topic. It is designed to:
//
//   - Be implementation-agnostic (e.g., SearXNG, other engines, or mocks)
//   - Preserve provenance for each discovered URL
//   - Be easy to fake in unit tests
//   - Not expose provider-specific response types
//
// The contracts are JSON-serializable to support later serialization needs.
//
// # Provider Interface
//
// Implementations of the Provider interface can be injected into callers.
// A Provider is responsible for executing a search query and returning
// ranked results with provenance information.
//
// Example:
//
//	type Provider interface {
//	    Search(ctx context.Context, req Request) (Response, error)
//	}
//
// # Request Design
//
// The Request type captures all parameters needed to perform a web search.
// All fields are optional except Query. Time range values are provider-neutral
// strings (e.g., "2024-01-01..2024-12-31" or "last_week").
//
// # Result Design
//
// Each SearchResult represents a candidate URL with enough metadata to:
// - Display in UIs (title, snippet)
// - Explain why it was discovered (provider, rank, query)
// - Track discovery provenance (id, providerName)
//
// # Response Design
//
// The Response aggregates results and includes optional provider-level
// metadata for diagnostics without coupling to specific providers.
//
// # Concurrency
//
// Callers may implement concurrent searches by calling Provider.Search
// concurrently with separate Provider instances or shared instances if
// the implementation is safe.
package search

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// Provider implements web search operations.
//
// A Provider is responsible for:
//   - Executing a search query with the given parameters
//   - Returning ranked results with provenance
//   - Preserving enough metadata to explain discoveries
//
// Implementations must be safe for concurrent use unless documented otherwise.
// Callers may inject mock implementations for testing.
type Provider interface {
	// Search executes a web search and returns ranked results.
	//
	// The context may be used for cancellation and timeout.
	// If the context is cancelled, Search should return an error wrapping
	// context.Canceled or context.DeadlineExceeded.
	//
	// The Request.Query field must not be empty; implementations may return
	// an error if the query is empty or invalid.
	//
	// Results are expected to be sorted by relevance (index 0 = most relevant).
	// The Provider should return as many results as the Limit specifies,
	// up to what the underlying search engine supports.
	Search(ctx context.Context, req Request) (Response, error)
}

// Request specifies parameters for a web search.
//
// Only Query is required. All other fields are optional and may be
// left at zero values to use provider defaults.
//
// TimeRange values are provider-neutral strings. Each provider is
// responsible for interpreting these values. Examples:
//
//   - "last_hour", "last_day", "last_week", "last_month", "last_year"
//   - "2024-01-01..2024-12-31" (ISO 8601 range)
//   - "2024" (year-only)
//
// Language is expected to be an RFC 5646 language tag (e.g., "en", "en-US", "es").
type Request struct {
	// Query is the search query text.
	// This field is required and must not be empty.
	Query string `json:"query"`

	// Language specifies the preferred language for search results.
	// This is an RFC 5646 language tag (e.g., "en", "en-US", "es", "fr-FR").
	// If empty, the provider chooses a default.
	Language string `json:"language,omitempty"`

	// TimeRange specifies a time range for search results.
	// This is a provider-neutral string; each provider interprets its format.
	// Common formats include:
	//   - Relative: "last_hour", "last_day", "last_week", "last_month", "last_year"
	//   - Absolute range: "2024-01-01..2024-12-31"
	//   - Year-only: "2024"
	// If empty, no time filtering is applied.
	TimeRange string `json:"time_range,omitempty"`

	// Limit specifies the maximum number of results to return.
	// Values below 1 are treated as 1; values above the provider's maximum
	// are capped at that maximum. The default is 10 if unspecified.
	Limit int `json:"limit,omitempty"`
}

// Validate checks that the Request has required fields populated.
//
// It returns an error if Query is empty.
func (r Request) Validate() error {
	if r.Query == "" {
		return &ValidationError{Field: "query", Message: "query cannot be empty"}
	}
	return nil
}

// Response contains the results of a search operation.
//
// Results are sorted by relevance (index 0 = most relevant).
// The original query and provider metadata are preserved for diagnostics
// and provenance.
type Response struct {
	// OriginalQuery is the exact query text from the Request.
	// This enables round-trip verification and UI display.
	OriginalQuery string `json:"original_query"`

	// Results contains the search results, sorted by relevance.
	// Results is never nil (may be empty slice if no results).
	Results []SearchResult `json:"results"`

	// ProviderInfo contains optional metadata about the search operation
	// that may be useful for diagnostics, billing, or monitoring.
	// This does not expose internal provider structures.
	ProviderInfo map[string]string `json:"provider_info,omitempty"`
}

// SearchResult represents a single discovered URL from a search.
//
// Each result preserves provenance through:
//   - ID: A stable identifier for this result (provider-specific)
//   - ProviderName: The name of the search provider/engine
//   - Query: The query that produced this result
//   - Rank: The position of this result in the result list
//
// Optional fields allow graceful handling of missing metadata:
//   - Title may be nil if not available
//   - Snippet may be nil if not available
//   - PublishedAt may be nil if not reliably supplied
type SearchResult struct {
	// ID is a stable identifier for this result from the provider.
	// This is provider-specific and may be a URL hash, document ID, etc.
	// It is unique within the result set but not necessarily globally.
	ID string `json:"id"`

	// URL is the discovered URL. This is the primary field for
	// downstream processing (fetching, extraction, verification).
	URL string `json:"url"`

	// Title is the result title or headline.
	// If nil, the title is unknown (provider did not provide one).
	Title *string `json:"title,omitempty"`

	// Snippet is a brief summary or excerpt of the result content.
	// If nil, no snippet is available.
	Snippet *string `json:"snippet,omitempty"`

	// ProviderName identifies the search provider or engine that
	// returned this result. This preserves provenance for later explanation.
	// Example: "searxng", "google", "duckduckgo"
	ProviderName string `json:"provider_name"`

	// Rank is the position of this result in the result list (1-indexed).
	// Rank 1 = most relevant.
	Rank int `json:"rank"`

	// PublishedAt is the publication date when reliably available.
	// This should be parsed from discoverable metadata (e.g., OpenGraph,
	// schema.org, or explicit date field in search result).
	// If nil, the publication date is unknown or unreliable.
	PublishedAt *time.Time `json:"published_at,omitempty"`

	// SourceDomain is the domain extracted from the URL.
	// This is computed from the URL field for convenience.
	SourceDomain string `json:"source_domain"`
}

// ValidationError represents a validation error for Request or other types.
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Error implements the error interface.
func (e *ValidationError) Error() string {
	return "validation error: " + e.Message
}

// Unwrap implements errors.Unwrap for composability.
func (e *ValidationError) Unwrap() error {
	return nil
}

// FilterResultsByDomain returns a copy of results filtered to only those
// from the specified domain(s).
//
// The domains list uses case-insensitive comparison. A domain match includes
// all subdomains (e.g., "example.com" matches "news.example.com").
//
// If domains is empty, all results are returned (no filtering).
func FilterResultsByDomain(results []SearchResult, domains []string) []SearchResult {
	if len(domains) == 0 {
		return results
	}

	domainSet := make(map[string]bool, len(domains))
	for _, d := range domains {
		domainSet[toLower(d)] = true
	}

	var filtered []SearchResult
	for _, r := range results {
		if matchesDomain(toLower(r.SourceDomain), domainSet) {
			filtered = append(filtered, r)
		}
	}

	return filtered
}

// matchesDomain checks if domain matches any in domainSet, including subdomains.
// A match occurs if:
//   - domain equals a key in domainSet, or
//   - domain ends with ".<key>" where key is in domainSet
func matchesDomain(domain string, domainSet map[string]bool) bool {
	if domainSet[domain] {
		return true
	}
	// Check subdomains: domain ends with ".<key>"
	for key := range domainSet {
		if strings.HasSuffix(domain, "."+key) {
			return true
		}
	}
	return false
}

// toLower converts a string to lowercase.
func toLower(s string) string {
	if s == "" {
		return ""
	}
	// Use strings.ToLower for full RFC 4697 compatibility
	return strings.ToLower(s)
}

// SearchResultJSON is a helper type for JSON marshaling that preserves the
// ability to distinguish between missing fields and explicitly null fields.
type SearchResultJSON struct {
	ID           string     `json:"id"`
	URL          string     `json:"url"`
	Title        *string    `json:"title,omitempty"`
	Snippet      *string    `json:"snippet,omitempty"`
	ProviderName string     `json:"provider_name"`
	Rank         int        `json:"rank"`
	PublishedAt  *time.Time `json:"published_at,omitempty"`
	SourceDomain string     `json:"source_domain"`
}

// MarshalJSON implements json.Marshaler for SearchResult.
func (r SearchResult) MarshalJSON() ([]byte, error) {
	return json.Marshal(SearchResultJSON(r))
}

// UnmarshalJSON implements json.Unmarshaler for SearchResult.
func (r *SearchResult) UnmarshalJSON(data []byte) error {
	var aux SearchResultJSON
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*r = SearchResult(aux)
	return nil
}

// DefaultLimit is the default search limit when Request.Limit is not set.
const DefaultLimit = 10
