// Package extractor provides content extraction abstractions for converting
// fetched content into normalized, readable documents.
//
// The extraction abstraction separates content parsing from HTTP fetching,
// allowing the same extraction logic to be applied to local fixtures,
// cached content, or live fetches.
package extractor

import (
	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
)

// ExtractionError represents a failure during content extraction.
type ExtractionError struct {
	// Code identifies the type of extraction failure.
	Code string

	// Message provides a human-readable description of the error.
	Message string

	// SourceURL is the URL that was being processed when the error occurred.
	SourceURL string
}

// Error implements the error interface.
func (e *ExtractionError) Error() string {
	if e.SourceURL != "" {
		return "extraction error (URL: " + e.SourceURL + "): " + e.Message
	}
	return "extraction error: " + e.Message
}

// IsOversized returns true if the extraction failed due to content being too large.
func (e *ExtractionError) IsOversized() bool {
	return e.Code == "oversized"
}

// IsParseError returns true if the extraction failed due to malformed content.
func (e *ExtractionError) IsParseError() bool {
	return e.Code == "parse_error"
}

// IsEmpty returns true if the extraction found no usable content.
func (e *ExtractionError) IsEmpty() bool {
	return e.Code == "empty_content"
}

// Input represents the input to an extractor.
//
// It combines source metadata (for provenance) with raw content to be extracted.
// This allows the same extraction logic to work with:
// - Local HTML fixtures
// - Cached responses
// - Live fetcher responses
type Input struct {
	// Source contains the metadata about the source.
	Source types.Source

	// Content is the raw content bytes to be extracted.
	// For HTML content, this would be the response body.
	Content []byte
}

// Extractor defines the interface for extracting normalized content from sources.
//
// Implementations should:
// - Extract meaningful text content (excluding scripts, styles, navigation)
// - Preserve paragraph boundaries where practical
// - Extract title, canonical URL, and publication metadata when available
// - Return deterministic results for identical input
// - Not make network calls or invoke LLMs
type Extractor interface {
	// Extract processes the given input and returns a normalized Document.
	//
	// Returns:
	// - A normalized Document with extracted content
	// - An ExtractionError if extraction fails
	//
	// If the input cannot produce a meaningful document (e.g., empty content,
	// malformed HTML, or no extractable content), returns a non-nil error.
	Extract(input Input) (*types.Document, error)
}

// Config holds configuration options for extractors.
type Config struct {
	// MaxTitleLength limits the maximum title length in characters.
	// Defaults to 1000 characters if not set.
	MaxTitleLength int

	// MaxPlainTextLength limits the maximum plain text length in characters.
	// Defaults to 1MB (1048576) if not set.
	MaxPlainTextLength int

	// MaxWordCount limits the maximum word count for extracted content.
	// Defaults to 50000 words if not set.
	MaxWordCount int

	// DateLayouts specifies additional layouts to try when parsing dates.
	// If empty, common layouts are tried in order.
	DateLayouts []string

	// PreserveWhitespace controls whether to preserve leading/trailing whitespace
	// within text blocks. Defaults to true.
	PreserveWhitespace bool
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		MaxTitleLength:     1000,
		MaxPlainTextLength: 1024 * 1024, // 1MB
		MaxWordCount:       50000,
		DateLayouts:        nil, // Use built-in defaults
		PreserveWhitespace: true,
	}
}

// Validate ensures the config has valid values and applies defaults where needed.
func (c *Config) Validate() {
	if c.MaxTitleLength <= 0 {
		c.MaxTitleLength = 1000
	}
	if c.MaxPlainTextLength <= 0 {
		c.MaxPlainTextLength = 1024 * 1024
	}
	if c.MaxWordCount <= 0 {
		c.MaxWordCount = 50000
	}
	if c.DateLayouts == nil {
		c.DateLayouts = []string{}
	}
}
