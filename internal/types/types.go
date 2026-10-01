// Package types defines the domain contracts used to represent input sources
// and normalized documents extracted from them.
//
// These types form the interface between the fetcher and researcher components.
// They are designed to preserve provenance for factual verification while
// remaining JSON-serializable.
package types

import (
	"time"
)

// SourceType enumerates the kind of source from which content was retrieved.
// These are domain-neutral and stable across iterations.
type SourceType string

const (
	// SourceTypeWeb indicates a public-facing website or blog.
	SourceTypeWeb SourceType = "web"
	// SourceTypeRSS indicates an RSS or Atom feed.
	SourceTypeRSS SourceType = "rss"
	// SourceTypeJSON indicates a structured JSON API or data feed.
	SourceTypeJSON SourceType = "json"
	// SourceTypeCSV indicates a CSV data file or feed.
	SourceTypeCSV SourceType = "csv"
	// SourceTypePDF indicates a PDF document.
	SourceTypePDF SourceType = "pdf"
	// SourceTypeUnknown indicates the source type could not be determined.
	SourceTypeUnknown SourceType = "unknown"
)

// Source represents an input source that was retrieved from the internet.
//
// Provenance is preserved through:
// - StableID: A deterministic identifier for this specific retrieval
// - OriginalURL: The URL where content was found
// - SourceType: The type of content source (web, rss, json, etc.)
// - RetrievedAt: When this source was fetched (UTC)
// - Metadata: Extra context from the source's response (headers, status, etc.)
//
// Source is intended to be the output of the fetcher and input to the
// document parser. It preserves the raw retrieval context but does not
// contain raw HTML (the researcher should never see raw HTML).
type Source struct {
	// StableID is a deterministic identifier for this specific retrieval.
	// It should be derived from the OriginalURL and RetrievedAt (e.g., URL hash
	// concatenated with timestamp). The fetcher is responsible for computing
	// this to avoid duplicates.
	StableID string `json:"stable_id"`

	// OriginalURL is the URL from which the source was retrieved.
	OriginalURL string `json:"original_url"`

	// SourceType indicates the type of content source.
	SourceType SourceType `json:"source_type"`

	// RetrievedAt is the UTC timestamp when this source was fetched.
	RetrievedAt time.Time `json:"retrieved_at"`

	// FetchStatus indicates whether the fetch was successful and any HTTP
	// status code returned. If nil, the status is unknown (e.g., client-side
	// error before response).
	FetchStatus *FetchStatus `json:"fetch_status,omitempty"`

	// Metadata contains extra provenance information from the source response.
	// This may include HTTP headers, content length, or other contextual data
	// useful for verification. Fields should only be populated when known.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// FetchStatus captures the result of an HTTP fetch.
//
// All fields are optional because some errors may occur before a response
// is received (e.g., network timeout, DNS failure).
type FetchStatus struct {
	// HTTPStatus is the HTTP status code returned (e.g., 200, 404).
	// May be nil if no HTTP response was received.
	HTTPStatus *int `json:"http_status,omitempty"`

	// ContentType is the Content-Type header value from the response.
	// May be nil if not available.
	ContentType *string `json:"content_type,omitempty"`

	// ContentLength is the Content-Length header value in bytes.
	// May be nil if not available or if the response has no defined length.
	ContentLength *int64 `json:"content_length,omitempty"`

	// FetchError is the error message if the fetch failed.
	// If nil, the fetch was successful (though HTTPStatus may indicate a client/server error).
	FetchError *string `json:"fetch_error,omitempty"`
}

// Document represents normalized text extracted from a Source.
//
// The Document contract is designed so that the researcher never sees raw HTML.
// All content is cleaned to plain text, and optional fields use pointer types
// to distinguish between "unknown" and "empty" values:
//   - Pointer type (e.g., *string): The value is unknown/not available
//   - Pointer type with nil: The value is explicitly empty
//   - Pointer type with non-nil: The value is explicitly known and populated
//
// This distinction is critical for provenance: the researcher can tell whether
// a field was unavailable or explicitly empty.
type Document struct {
	// SourceID is the StableID of the Source from which this document was extracted.
	// This provides a traceable link back to the original retrieval.
	SourceID string `json:"source_id"`

	// CanonicalURL is the canonical URL for this document, if known.
	// This may differ from the Source.OriginalURL due to redirects, URL
	// normalization, or publisher-provided canonical tags.
	// If nil, the canonical URL is unknown (the document's origin is its SourceID).
	CanonicalURL *string `json:"canonical_url,omitempty"`

	// Title is the document title, cleaned to plain text.
	// This should be the primary title (e.g., <title> tag for HTML, headline for news).
	// If nil, the title is unknown (not because no title was found, but because
	// the source did not provide or expose title information).
	// If a title was found but is explicitly empty string, use PointerTo("").
	Title *string `json:"title,omitempty"`

	// PlainText is the document content as cleaned plain text, with all HTML
	// tags, scripts, styles, and other markup removed. Line breaks and
	// paragraph structure should be preserved where meaningful.
	PlainText string `json:"plain_text"`

	// Author is the document author, if identified.
	// If nil, the author is unknown (not because no author was found, but because
	// the source did not provide or expose author information).
	// If an author was found but is explicitly "Unknown" or similar, use PointerTo("Unknown").
	Author *string `json:"author,omitempty"`

	// PublishedAt is the document's publication timestamp, if available.
	// This should be extracted from metadata (e.g., article-date, datePublished).
	// If nil, the publication timestamp is unknown.
	PublishedAt *time.Time `json:"published_at,omitempty"`

	// RetrievedAt is the UTC timestamp when this document was normalized.
	// This may differ from Source.RetrievedAt if normalization occurred later.
	RetrievedAt time.Time `json:"retrieved_at"`

	// ExtractionMetadata captures provenance details about the extraction process.
	// This includes things like the extraction method used, word count,
	// or any validation flags set during cleaning.
	ExtractionMetadata map[string]string `json:"extraction_metadata,omitempty"`
}

// PointerTo returns a pointer to the given value, useful for constructing
// Document fields that need to distinguish between unknown and empty.
func PointerTo[T any](v T) *T {
	return &v
}

// FetchStatusJSON is a helper type for JSON marshaling that preserves the
// ability to distinguish between missing fields and explicitly null fields.
type FetchStatusJSON struct {
	HTTPStatus    *int    `json:"http_status,omitempty"`
	ContentType   *string `json:"content_type,omitempty"`
	ContentLength *int64  `json:"content_length,omitempty"`
	FetchError    *string `json:"fetch_error,omitempty"`
}

// DocumentJSON is a helper type for JSON marshaling that preserves the
// ability to distinguish between missing fields and explicitly null fields.
type DocumentJSON struct {
	CanonicalURL       *string           `json:"canonical_url,omitempty"`
	Title              *string           `json:"title,omitempty"`
	PlainText          string            `json:"plain_text"`
	Author             *string           `json:"author,omitempty"`
	PublishedAt        *time.Time        `json:"published_at,omitempty"`
	RetrievedAt        time.Time         `json:"retrieved_at"`
	ExtractionMetadata map[string]string `json:"extraction_metadata,omitempty"`
}
