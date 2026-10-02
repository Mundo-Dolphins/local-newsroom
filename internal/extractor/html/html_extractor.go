// Package html provides HTML content extraction capabilities using go-readability.
//
// This extractor processes HTML content and extracts:
// - Document title
// - Main content text (excluding scripts, styles, navigation, ads)
// - Canonical URL (from page or canonical tag)
// - Publication metadata (author, publication date)
//
// The extractor uses go-readability for content detection and clean text extraction.
package html

import (
	"bytes"
	"fmt"
	"net/url"
	"strings"
	"time"

	"codeberg.org/readeck/go-readability/v2"
	"golang.org/x/net/html"

	"github.com/Mundo-Dolphins/local-newsroom/internal/extractor"
	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
)

// Extractor implements the extractor.Interface for HTML content.
type Extractor struct {
	config extractor.Config
}

// New creates a new HTML extractor with the given configuration.
// If config is zero-valued, DefaultConfig is used.
func New(config extractor.Config) *Extractor {
	config.Validate()
	return &Extractor{
		config: config,
	}
}

// Extract implements the extractor.Interface.
func (e *Extractor) Extract(input extractor.Input) (*types.Document, error) {
	// Validate content is present
	if len(input.Content) == 0 {
		return nil, &extractor.ExtractionError{
			Code:      "empty_content",
			Message:   "content is empty",
			SourceURL: input.Source.OriginalURL,
		}
	}

	// Parse the final/base URL
	finalURL, err := url.Parse(input.Source.OriginalURL)
	if err != nil {
		return nil, &extractor.ExtractionError{
			Code:      "invalid_url",
			Message:   fmt.Sprintf("failed to parse URL: %v", err),
			SourceURL: input.Source.OriginalURL,
		}
	}

	// Parse HTML first using golang.org/x/net/html (tolerant parser)
	doc, err := html.Parse(strings.NewReader(string(input.Content)))
	if err != nil {
		return nil, &extractor.ExtractionError{
			Code:      "parse_error",
			Message:   fmt.Sprintf("failed to parse HTML: %v", err),
			SourceURL: input.Source.OriginalURL,
		}
	}

	// Use go-readability to extract the main article content
	article, err := readability.FromDocument(doc, finalURL)
	if err != nil {
		return nil, &extractor.ExtractionError{
			Code:      "readability_error",
			Message:   fmt.Sprintf("go-readability failed: %v", err),
			SourceURL: input.Source.OriginalURL,
		}
	}

	// Extract plain text using go-readability's RenderText
	var textBuf bytes.Buffer
	if err := article.RenderText(&textBuf); err != nil {
		return nil, &extractor.ExtractionError{
			Code:      "render_error",
			Message:   fmt.Sprintf("failed to render text: %v", err),
			SourceURL: input.Source.OriginalURL,
		}
	}

	text := strings.TrimSpace(textBuf.String())

	// Check that we have meaningful content
	if text == "" {
		return nil, &extractor.ExtractionError{
			Code:      "empty_content",
			Message:   "no extractable content found in HTML",
			SourceURL: input.Source.OriginalURL,
		}
	}

	// Apply word count limit if configured
	if e.config.MaxWordCount > 0 {
		words := strings.Fields(text)
		if len(words) > e.config.MaxWordCount {
			text = strings.Join(words[:e.config.MaxWordCount], " ")
			// Trim to last complete word
			if idx := strings.LastIndex(text, " "); idx > 0 {
				text = text[:idx]
			}
		}
	}

	// Extract metadata
	title := article.Title()
	byline := article.Byline()
	publishedTime, pubErr := article.PublishedTime()
	modifiedTime, modErr := article.ModifiedTime()

	// Build document
	now := time.Now().UTC()
	docResult := &types.Document{
		SourceID: input.Source.StableID,
		// CanonicalURL: go-readability doesn't expose canonical directly;
		// we could extract it from the original document if needed.
		Title: func() *string {
			if title != "" {
				return types.PointerTo(title)
			}
			return nil
		}(),
		PlainText:          text,
		Author:             e.extractAuthor(byline),
		PublishedAt:        e.extractPublishedAt(publishedTime, pubErr, modifiedTime, modErr),
		RetrievedAt:        now,
		ExtractionMetadata: e.buildExtractionMetadata(article, publishedTime, pubErr, modifiedTime, modErr, text),
	}

	// Apply length limits if set
	if e.config.MaxPlainTextLength > 0 && len(docResult.PlainText) > e.config.MaxPlainTextLength {
		docResult.PlainText = docResult.PlainText[:e.config.MaxPlainTextLength]
	}

	return docResult, nil
}

// extractAuthor extracts and cleans the author from the byline.
func (e *Extractor) extractAuthor(byline string) *string {
	if byline == "" {
		return nil
	}

	// Clean up common patterns: "By John Doe", "By: John Doe", etc.
	author := strings.TrimSpace(byline)
	lower := strings.ToLower(author)

	// Remove "By" or "By:" prefixes
	if strings.HasPrefix(lower, "by:") {
		author = strings.TrimSpace(strings.TrimPrefix(lower, "by:"))
	} else if strings.HasPrefix(lower, "by ") {
		author = strings.TrimSpace(strings.TrimPrefix(lower, "by "))
	}

	if author == "" {
		return nil
	}

	return types.PointerTo(author)
}

// extractPublishedAt extracts the publication time, preferring published_time over modified_time.
func (e *Extractor) extractPublishedAt(published time.Time, pubErr error, modified time.Time, modErr error) *time.Time {
	// Prefer published time if available and valid
	if pubErr == nil && !published.IsZero() {
		return &published
	}

	// Fall back to modified time if available and valid
	if modErr == nil && !modified.IsZero() {
		return &modified
	}

	return nil
}

// buildExtractionMetadata records extraction provenance and useful metadata.
func (e *Extractor) buildExtractionMetadata(article readability.Article, published time.Time, pubErr error, modified time.Time, modErr error, plainText string) map[string]string {
	metadata := map[string]string{
		"extractor":        "go-readability",
		"extractor_module": "codeberg.org/readeck/go-readability/v2",
		"word_count":       fmt.Sprintf("%d", countWords(plainText)),
		"char_count":       fmt.Sprintf("%d", len(plainText)),
	}

	// Add language if detected
	if lang := article.Language(); lang != "" {
		metadata["language"] = lang
	}

	// Add site name if detected
	if site := article.SiteName(); site != "" {
		metadata["site_name"] = site
	}

	// Add excerpt if available (short summary)
	if excerpt := article.Excerpt(); excerpt != "" {
		// Limit excerpt length in metadata
		if len(excerpt) > 500 {
			excerpt = excerpt[:500]
		}
		metadata["excerpt"] = excerpt
	}

	// Add timestamps if available
	if pubErr == nil && !published.IsZero() {
		metadata["published_time"] = published.Format(time.RFC3339)
	}
	if modErr == nil && !modified.IsZero() {
		metadata["modified_time"] = modified.Format(time.RFC3339)
	}

	return metadata
}

// countWords counts the number of words in text.
func countWords(text string) int {
	if text == "" {
		return 0
	}
	return len(strings.Fields(text))
}
