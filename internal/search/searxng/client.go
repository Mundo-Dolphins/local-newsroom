// Package searxng provides a SearXNG adapter implementing the search.Provider interface.
//
// This package implements a client for the SearXNG JSON Search API.
// It abstracts away HTTP details, JSON parsing, and error handling,
// presenting a clean Provider interface to callers.
//
// # Configuration
//
// Configuration is provided via Config which supports:
//   - base URL (required)
//   - HTTP timeout
//   - max response size
//   - user agent
//   - optional API key authentication
//
// # Usage
//
//	provider := searxng.NewProvider(config)
//	results, err := provider.Search(ctx, search.Request{
//		Query: "search query",
//		Limit: 10,
//		Language: "en",
//		TimeRange: "last_week",
//	})
package searxng

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/search"
)

var (
	// ErrInvalidQuery is returned when the query is invalid.
	ErrInvalidQuery = errors.New("invalid query")

	// ErrResponseTooLarge is returned when the response exceeds MaxResponseSize.
	ErrResponseTooLarge = errors.New("response size exceeds maximum allowed")

	// ErrMalformedJSON is returned when JSON parsing fails.
	ErrMalformedJSON = errors.New("malformed JSON response")

	// ErrSearchFailed is returned when the search API returns an error.
	ErrSearchFailed = errors.New("search request failed")
)

// Provider implements search.Provider using SearXNG.
type Provider struct {
	client  *http.Client
	config  *Config
	baseURL string
}

// NewProvider creates a new SearXNG provider with the given configuration.
// It returns an error if the configuration is invalid.
func NewProvider(config *Config) (*Provider, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	// Apply defaults
	if config.HTTPTimeout <= 0 {
		config.HTTPTimeout = defaultTimeout
	}
	if config.MaxResponseSize <= 0 {
		config.MaxResponseSize = defaultMaxResponseSize
	}
	if config.APIKey != "" && config.APIHeader == "" {
		config.APIHeader = "X-API-Key"
	}

	// Parse and validate the base URL, ensuring it ends with "/"
	baseURL := config.BaseURL
	if !strings.HasSuffix(baseURL, "/") {
		baseURL += "/"
	}

	provider := &Provider{
		client:  config.BuildHTTPClient(),
		config:  config,
		baseURL: baseURL,
	}

	return provider, nil
}

// Search implements search.Provider.Search by calling the SearXNG JSON API.
func (p *Provider) Search(ctx context.Context, req search.Request) (search.Response, error) {
	// Validate the request
	if err := req.Validate(); err != nil {
		return search.Response{}, fmt.Errorf("invalid request: %w", err)
	}

	// Build the search query URL
	queryURL, err := p.buildQueryURL(req)
	if err != nil {
		return search.Response{}, fmt.Errorf("failed to build query URL: %w", err)
	}

	// Execute the request
	resp, err := p.executeQuery(ctx, queryURL)
	if err != nil {
		return search.Response{}, err
	}
	defer func() {
		_ = resp.Body.Close() // ignore errors in body close
	}()

	// Parse the response into results
	results, providerInfo, err := p.parseResponse(resp.Body)
	if err != nil {
		return search.Response{}, err
	}

	return search.Response{
		OriginalQuery: req.Query,
		Results:       results,
		ProviderInfo:  providerInfo,
	}, nil
}

// buildQueryURL constructs the SearXNG search URL with all parameters.
func (p *Provider) buildQueryURL(req search.Request) (string, error) {
	base, err := parseURL(p.baseURL + "search")
	if err != nil {
		return "", fmt.Errorf("invalid base URL: %w", err)
	}

	q := base.Query()

	// Required query parameter
	q.Set("q", req.Query)

	// Always use JSON format
	q.Set("format", "json")

	// Optional parameters
	if req.Language != "" {
		q.Set("language", req.Language)
	}

	if req.TimeRange != "" {
		// SearXNG uses "time_range" parameter
		q.Set("time_range", req.TimeRange)
	}

	// SearXNG uses "pageno" for pagination (1-indexed)
	if req.Limit > 0 {
		// Calculate page number (SearXNG uses results_per_page default of 10)
		pageNo := (req.Limit + 9) / 10
		if pageNo < 1 {
			pageNo = 1
		}
		q.Set("pageno", strconv.Itoa(pageNo))
	}

	// If the config has a safesearch setting, use it
	if p.config.SafeSearch >= 0 {
		q.Set("safesearch", strconv.Itoa(p.config.SafeSearch))
	}

	// Apply query parameters to the URL
	base.RawQuery = q.Encode()
	return base.String(), nil
}

// executeQuery performs the HTTP request with context and response size limits.
func (p *Provider) executeQuery(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Add API key header if configured
	if p.config.APIKey != "" && p.config.APIHeader != "" {
		req.Header.Set(p.config.APIHeader, p.config.APIKey)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		// Check if this was a context-related error
		if ctx.Err() != nil {
			return nil, fmt.Errorf("request cancelled: %w", ctx.Err())
		}
		return nil, fmt.Errorf("request failed: %w", err)
	}

	// Check for non-2xx status codes
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		statusText := http.StatusText(resp.StatusCode)
		if resp.StatusCode == http.StatusForbidden {
			return nil, &SearchError{
				Status: resp.StatusCode,
				Message: fmt.Sprintf("SearXNG returned %d %s (JSON output may not be enabled; check instance settings.yml)",
					resp.StatusCode, statusText),
			}
		}
		return nil, &SearchError{
			Status:  resp.StatusCode,
			Message: fmt.Sprintf("SearXNG returned %d %s", resp.StatusCode, statusText),
		}
	}

	// Limit response body size to prevent memory exhaustion
	limitReader := io.LimitReader(resp.Body, p.config.MaxResponseSize)

	// Read and validate the full response
	body, err := io.ReadAll(limitReader)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Check if we hit the limit
	if int64(len(body)) == p.config.MaxResponseSize {
		// Try to read one more byte to see if there's more data
		_, err := resp.Body.Read(make([]byte, 1))
		if err == nil {
			// Successfully read more data after hitting the limit
			return nil, ErrResponseTooLarge
		}
		// If err != nil, the response was exactly at the limit (not too large), proceed
	}

	// Create a new response with limited body
	resp.Body = io.NopCloser(bytes.NewReader(body))

	return resp, nil
}

// parseResponse reads and parses the SearXNG JSON response.
func (p *Provider) parseResponse(body io.Reader) ([]search.SearchResult, map[string]string, error) {
	var rawResponse SearXNGResponse
	if err := json.NewDecoder(body).Decode(&rawResponse); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrMalformedJSON, err)
	}

	if len(rawResponse.Results) == 0 {
		return []search.SearchResult{}, map[string]string{}, nil
	}

	// Convert SearXNG results to search.SearchResult
	results := make([]search.SearchResult, 0, len(rawResponse.Results))
	for i, raw := range rawResponse.Results {
		result := convertResult(raw, i+1)
		results = append(results, result)
	}

	// Extract provider metadata
	providerInfo := extractEngineInfo(rawResponse.Metadata)

	return results, providerInfo, nil
}

// convertResult transforms a SearXNG raw result into a search.SearchResult.
func convertResult(raw SearXNGResult, rank int) search.SearchResult {
	// Extract source domain from URL
	sourceDomain := ""
	if raw.URL != "" {
		if parsed, err := parseURL(raw.URL); err == nil {
			sourceDomain = parsed.Host
		}
	}

	result := search.SearchResult{
		ID:           raw.ID,
		URL:          raw.URL,
		Title:        toStringPtr(raw.Title),
		Snippet:      toStringPtr(raw.Content),
		ProviderName: "searxng",
		Rank:         rank,
		SourceDomain: sourceDomain,
	}

	// Parse publication date if available
	if raw.PublishDate != "" {
		if parsed, err := parseTime(raw.PublishDate); err == nil {
			result.PublishedAt = &parsed
		}
	}

	return result
}

// toStringPtr converts a string to *string.
func toStringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// parseTime parses various time formats used by SearXNG.
func parseTime(s string) (time.Time, error) {
	// Try RFC3339 first
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}

	// Try RFC3339Nano
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}

	// Try ISO 8601 date only
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}

	return time.Time{}, fmt.Errorf("unknown time format: %s", s)
}

// parseURL parses a URL string.
func parseURL(s string) (*url.URL, error) {
	// url.Parse can handle strings without scheme
	return url.Parse(s)
}

// SearXNGResponse represents the JSON structure returned by SearXNG.
// Reference: https://docs.searxng.org/dev/search_api.html
type SearXNGResponse struct {
	Results             []SearXNGResult  `json:"results"`
	Infoboxes           []interface{}    `json:"infoboxes,omitempty"`
	Suggestions         []string         `json:"suggestions,omitempty"`
	UnresponsiveEngines []string         `json:"unresponsive_engines,omitempty"`
	Queries             []interface{}    `json:"queries,omitempty"`
	Metadata            *SearXNGMetadata `json:"metadata,omitempty"`
	Pageno              int              `json:"pageno,omitempty"`
}

// SearXNGResult represents a single search result from SearXNG.
type SearXNGResult struct {
	ID          string `json:"id,omitempty"`
	URL         string `json:"url"`
	Title       string `json:"title"`
	Content     string `json:"content"`
	Engine      string `json:"engine"`
	PublishDate string `json:"publishedAt,omitempty"`
	Template    string `json:"template,omitempty"`
	Category    string `json:"category,omitempty"`
	Positions   []int  `json:"positions,omitempty"`
	Lang        string `json:"lang,omitempty"`
	Thumbs      string `json:"thumbs,omitempty"`
	Grade       string `json:"grade,omitempty"`
	// OpenData is a special case for structured data results
	OpenData interface{} `json:"openData,omitempty"`
}

// SearXNGMetadata contains metadata about the SearXNG instance.
type SearXNGMetadata struct {
	Host      string  `json:"host,omitempty"`
	Name      string  `json:"name,omitempty"`
	Version   string  `json:"version,omitempty"`
	Website   string  `json:"website,omitempty"`
	LegalUrl  string  `json:"legal_url,omitempty"`
	PublicURL string  `json:"public_url,omitempty"`
	Provider  string  `json:"provider,omitempty"`
	Queries   int     `json:"queries,omitempty"`
	Engines   string  `json:"engines,omitempty"`
	CSP       string  `json:"csp,omitempty"`
	Docker    bool    `json:"docker,omitempty"`
	Instance  string  `json:"instance,omitempty"`
	Server    string  `json:"server,omitempty"`
	Time      float64 `json:"time,omitempty"`
	TotalTime float64 `json:"total_time,omitempty"`
}

// extractEngineInfo extracts engine provider info from metadata.
func extractEngineInfo(metadata *SearXNGMetadata) map[string]string {
	if metadata == nil {
		return nil
	}

	info := map[string]string{
		"engine": "searxng",
	}

	if metadata.Version != "" {
		info["engine_version"] = metadata.Version
	}
	if metadata.Name != "" {
		info["instance_name"] = metadata.Name
	}
	if metadata.Host != "" || metadata.PublicURL != "" {
		info["instance_url"] = metadata.PublicURL
		if info["instance_url"] == "" {
			info["instance_url"] = metadata.Host
		}
	}
	if metadata.Time > 0 {
		info["response_time_ms"] = fmt.Sprintf("%.0f", metadata.Time*1000)
	}
	if metadata.Queries > 0 {
		info["total_queries"] = strconv.Itoa(metadata.Queries)
	}

	return info
}

// SearchError represents an error returned by SearXNG.
type SearchError struct {
	Status  int
	Message string
}

func (e *SearchError) Error() string {
	return fmt.Sprintf("search error (HTTP %d): %s", e.Status, e.Message)
}

func (e *SearchError) Is(target error) bool {
	_, ok := target.(*SearchError)
	return ok
}

// Compile-time check that Provider implements search.Provider.
var _ search.Provider = (*Provider)(nil)
