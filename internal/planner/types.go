// Package planner implements a search query planner that converts a research topic
// into a structured set of web-search queries.
//
// The planner:
// - Uses a prompt template stored in prompts/search-planner.prompt
// - Depends on the generic llm.Client interface
// - Parses and validates model responses before accepting them
// - Returns errors when model output cannot be validated
// - Is unit-testable with a fake LLM client
//
// This package is intentionally separate from the researcher because query
// planning happens before any source documents exist.
package planner

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// SearchPlan represents the validated output of the search query planner.
//
// It contains a bounded set of complementary search queries designed to
// discover authoritative sources for a research topic.
type SearchPlan struct {
	// OriginalTopic is the research topic that was planned.
	OriginalTopic string `json:"original_topic"`

	// Queries is the ordered list of search queries to execute.
	// Minimum: 1 query. Maximum: maxQueriesConfigured (default 5, configurable 1-10).
	Queries []SearchQuery `json:"queries"`

	// Language is the optional 2-letter ISO language code (e.g., "en", "es", "fr").
	// If provided, it indicates the preferred language for search results.
	Language string `json:"language,omitempty"`

	// TimeRangeHint is an optional time-range hint for search recency.
	TimeRangeHint *TimeRangeHint `json:"time_range_hint,omitempty"`
}

// SearchQuery represents a single search query in the plan.
type SearchQuery struct {
	// Query is the actual search query text.
	Query string `json:"query"`

	// Purpose explains why this query is included in the plan.
	Purpose string `json:"purpose"`

	// Language is an optional query-specific language override.
	// If empty, use the plan-level Language.
	Language string `json:"language,omitempty"`

	// TimeRange is an optional query-specific time-range override.
	TimeRange *TimeRangeHint `json:"time_range,omitempty"`
}

// TimeRangeHint represents a date range for search queries.
// Both fields are optional; at least one should typically be set.
type TimeRangeHint struct {
	// StartDate is the start date in ISO 8601 format (YYYY-MM-DD or YYYY-MM-DDTHH:MM:SSZ).
	StartDate string `json:"start_date,omitempty"`

	// EndDate is the end date in ISO 8601 format (YYYY-MM-DD or YYYY-MM-DDTHH:MM:SSZ).
	EndDate string `json:"end_date,omitempty"`
}

// Config holds configuration for the planner.
type Config struct {
	// MinQueries is the minimum number of queries to generate.
	// Default: 1, Valid range: 1-10
	MinQueries int

	// MaxQueries is the maximum number of queries to generate.
	// Default: 5, Valid range: 1-10
	MaxQueries int

	// Temperature controls randomness in generation (0.0 = deterministic).
	// Recommended: 0.3-0.7 for structured query planning.
	Temperature float64

	// MaxOutputTokens limits the response size.
	// A search plan with 3-5 queries typically needs 2k-5k tokens.
	MaxOutputTokens int

	// PromptOverride allows customizing the system prompt for testing or special cases.
	// If empty, uses prompts/search-planner.prompt.
	PromptOverride string
}

// DefaultConfig returns a sensible default configuration.
func DefaultConfig() Config {
	return Config{
		MinQueries:      1,
		MaxQueries:      5,
		Temperature:     0.5,
		MaxOutputTokens: 4000,
	}
}

// Error represents a planner-specific error.
type Error struct {
	// Code is a machine-readable error code.
	Code string

	// Message is a human-readable description.
	Message string
}

func (e Error) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

// Common error codes.
var (
	ErrEmptyTopic       = Error{Code: "empty_topic", Message: "topic cannot be empty"}
	ErrEmptyQueries     = Error{Code: "empty_queries", Message: "no queries in the plan"}
	ErrQueryEmpty       = Error{Code: "query_empty", Message: "search query cannot be empty"}
	ErrPurposeEmpty     = Error{Code: "purpose_empty", Message: "query purpose cannot be empty"}
	ErrTooFewQueries    = Error{Code: "too_few_queries", Message: "query count below minimum"}
	ErrTooManyQueries   = Error{Code: "too_many_queries", Message: "query count exceeds maximum"}
	ErrInvalidJSON      = Error{Code: "invalid_json", Message: "model output is not valid JSON"}
	ErrInvalidStructure = Error{Code: "invalid_structure", Message: "model output has invalid structure"}
)

// normalizeQuery normalizes a search query for duplicate detection.
// Normalization steps:
// 1. Lowercase the query
// 2. Replace multiple whitespace with single space
// 3. Strip common query operators prefixes (site:, intitle:, inurl:, etc.)
// 4. Trim whitespace
// 5. Strip trailing punctuation common in queries
// Returns the normalized query string.
func normalizeQuery(q string) string {
	s := strings.ToLower(q)

	// Normalize whitespace
	s = strings.Join(strings.Fields(s), " ")

	// Strip common query operators (but keep the rest)
	operators := []string{
		"site:", "intitle:", "inurl:", "filetype:", "file:", "language:",
		"lang:", "location:", "loc:", "after:", "before:", "from:", "to:",
		"has:", "notin:", "-inurl:", "-site:",
	}
	for _, op := range operators {
		if strings.HasPrefix(s, op) {
			// Find the rest of the query after the operator's value
			rest := strings.TrimSpace(strings.TrimPrefix(s, op))
			// Find first space in the rest - everything before that is the operator value
			spaceIdx := strings.Index(rest, " ")
			if spaceIdx > 0 {
				// Keep everything after the operator's value
				s = rest[spaceIdx+1:]
			} else {
				// Only the operator value was present
				s = ""
			}
			break
		}
	}

	// Strip trailing punctuation common in queries
	s = strings.TrimRight(s, ".,;:!?\"")

	return strings.TrimSpace(s)
}

// deduplicateQueries removes duplicate queries based on normalized form.
// Keeps the first occurrence of each unique normalized query.
// Returns a slice of queries with duplicates removed.
func deduplicateQueries(queries []SearchQuery) []SearchQuery {
	seen := make(map[string]bool)
	result := make([]SearchQuery, 0, len(queries))

	for _, q := range queries {
		normalized := normalizeQuery(q.Query)
		if !seen[normalized] {
			seen[normalized] = true
			result = append(result, q)
		}
	}

	return result
}

// Validate checks that the SearchPlan has a valid structure.
// Returns an error if validation fails.
func (p *SearchPlan) Validate() error {
	if strings.TrimSpace(p.OriginalTopic) == "" {
		return ErrEmptyTopic
	}

	if len(p.Queries) == 0 {
		return ErrEmptyQueries
	}

	for i, q := range p.Queries {
		if strings.TrimSpace(q.Query) == "" {
			return fmt.Errorf("%w: query[%d]", ErrQueryEmpty, i)
		}
		if strings.TrimSpace(q.Purpose) == "" {
			return fmt.Errorf("%w: query[%d]", ErrPurposeEmpty, i)
		}
	}

	return nil
}

// ValidateAgainstConfig checks that the plan respects the configured bounds.
// Returns an error if the query count is outside [MinQueries, MaxQueries].
func (p *SearchPlan) ValidateAgainstConfig(cfg Config) error {
	if len(p.Queries) < cfg.MinQueries {
		return fmt.Errorf("%w: got %d, minimum is %d", ErrTooFewQueries, len(p.Queries), cfg.MinQueries)
	}
	if len(p.Queries) > cfg.MaxQueries {
		return fmt.Errorf("%w: got %d, maximum is %d", ErrTooManyQueries, len(p.Queries), cfg.MaxQueries)
	}
	return nil
}

// MarshalJSON implements custom JSON marshaling for TimeRangeHint.
func (tr *TimeRangeHint) MarshalJSON() ([]byte, error) {
	if tr == nil {
		return []byte("null"), nil
	}

	type Alias TimeRangeHint
	return json.Marshal(&struct {
		*Alias
	}{
		Alias: (*Alias)(tr),
	})
}

// UnmarshalJSON implements custom JSON unmarshaling for TimeRangeHint.
func (tr *TimeRangeHint) UnmarshalJSON(data []byte) error {
	type Alias TimeRangeHint
	aux := &struct {
		*Alias
	}{
		Alias: (*Alias)(tr),
	}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	return nil
}

// ValidateStartTime checks if the start date is valid if set.
func (tr *TimeRangeHint) ValidateStartTime() error {
	if tr == nil || tr.StartDate == "" {
		return nil
	}

	// Accept both date (YYYY-MM-DD) and datetime (YYYY-MM-DDTHH:MM:SSZ) formats
	if !isValidDate(tr.StartDate) {
		return fmt.Errorf("invalid start_date format: %s", tr.StartDate)
	}
	return nil
}

// ValidateEndTime checks if the end date is valid if set.
func (tr *TimeRangeHint) ValidateEndTime() error {
	if tr == nil || tr.EndDate == "" {
		return nil
	}

	// Accept both date (YYYY-MM-DD) and datetime (YYYY-MM-DDTHH:MM:SSZ) formats
	if !isValidDate(tr.EndDate) {
		return fmt.Errorf("invalid end_date format: %s", tr.EndDate)
	}
	return nil
}

// isValidDate checks if a string is a valid date or datetime in ISO 8601 format.
func isValidDate(s string) bool {
	// Try date format: YYYY-MM-DD
	if tryParseDate(s) {
		return true
	}

	// Try datetime format: YYYY-MM-DDTHH:MM:SSZ
	return tryParseDateTime(s)
}

// tryParseDate attempts to parse as YYYY-MM-DD.
func tryParseDate(s string) bool {
	// Basic format check for YYYY-MM-DD
	parts := strings.Split(s, "-")
	if len(parts) != 3 {
		return false
	}

	year := parts[0]
	month := parts[1]
	day := parts[2]

	if len(year) != 4 || len(month) != 2 || len(day) != 2 {
		return false
	}

	// Basic range check (not exhaustive)
	for _, d := range []string{year, month, day} {
		for _, c := range d {
			if c < '0' || c > '9' {
				return false
			}
		}
	}

	// Very basic sanity check
	if month < "01" || month > "12" {
		return false
	}
	if day < "01" || day > "31" {
		return false
	}

	return true
}

// tryParseDateTime attempts to parse as YYYY-MM-DDTHH:MM:SSZ.
func tryParseDateTime(s string) bool {
	// Basic format check
	if len(s) < 19 {
		return false
	}

	// Check format at specific positions
	if s[4] != '-' || s[7] != '-' || s[10] != 'T' || s[13] != ':' || s[16] != ':' {
		return false
	}

	// Allow 'Z' or digits or timezone signs (+, -)
	last := s[len(s)-1]
	isASCII := last >= '0' && last <= '9' || last == '+' || last == '-' || last == 'Z'
	if !isASCII {
		return false
	}

	// Try to parse as URL-encoded if needed
	if strings.Contains(s, "%") {
		decoded, err := url.QueryUnescape(s)
		if err != nil {
			return false
		}
		if !isValidDate(decoded) && !isValidDate(decoded[:19]) {
			return tryParseDateTime(decoded)
		}
		return true
	}

	return true
}
