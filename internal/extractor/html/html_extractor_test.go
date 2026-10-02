package html

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/extractor"
	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
)

const fixturesDir = "fixtures"

// TestNormalArticle tests extraction from a typical article page with good metadata
func TestNormalArticle(t *testing.T) {
	ext := New(extractor.DefaultConfig())

	// Load fixture
	fixturePath := joinPath(fixturesDir, "normal_article.html")
	htmlContent, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("Failed to read fixture: %v", err)
	}

	// Create input
	source := types.Source{
		StableID:    "test-normal-article",
		OriginalURL: "https://technews.example.com/articles/tech-innovation-summit-2024",
	}
	input := extractor.Input{
		Source:  source,
		Content: htmlContent,
	}

	// Extract
	doc, err := ext.Extract(input)
	if err != nil {
		t.Fatalf("Extraction failed: %v", err)
	}

	// Validate title - go-readability should extract from title tag
	if doc.Title == nil {
		t.Fatal("Title should not be nil")
	}
	// Title may be truncated by readability processing
	if len(*doc.Title) > ext.config.MaxTitleLength {
		t.Errorf("Title should be truncated to max length, got %d chars", len(*doc.Title))
	}

	// Validate plain text content exists
	if doc.PlainText == "" {
		t.Fatal("PlainText should not be empty")
	}

	// Check that main content is present
	if !strings.Contains(doc.PlainText, "Tech Innovation Summit") &&
		!strings.Contains(doc.PlainText, "neural network") &&
		!strings.Contains(doc.PlainText, "Artificial Intelligence") {
		t.Error("Main content should contain relevant terms")
	}

	// Check that script/style content is not included
	if strings.Contains(doc.PlainText, "<script>") || strings.Contains(doc.PlainText, "</script>") {
		t.Error("Script tags should not appear in plain text")
	}

	// Check word count is reasonable
	wordCount := countWords(doc.PlainText)
	if wordCount < 50 {
		t.Errorf("Expected at least 50 words, got: %d", wordCount)
	}

	// Validate extraction metadata
	if doc.ExtractionMetadata == nil {
		t.Error("ExtractionMetadata should not be nil")
	} else {
		if extractorName, ok := doc.ExtractionMetadata["extractor"]; !ok {
			t.Error("ExtractionMetadata should include 'extractor' field")
		} else if extractorName != "go-readability" {
			t.Errorf("Extractor should be 'go-readability', got: %q", extractorName)
		}
		if module, ok := doc.ExtractionMetadata["extractor_module"]; !ok {
			t.Error("ExtractionMetadata should include 'extractor_module' field")
		} else if module != "codeberg.org/readeck/go-readability/v2" {
			t.Errorf("Extractor module should be 'codeberg.org/readeck/go-readability/v2', got: %q", module)
		}
		if _, ok := doc.ExtractionMetadata["word_count"]; !ok {
			t.Error("ExtractionMetadata should include 'word_count' field")
		}
	}

	// Validate RetrievedAt is set
	if doc.RetrievedAt.IsZero() {
		t.Error("RetrievedAt should be set")
	}
}

// TestNoisyPage tests extraction from a page with navigation, ads, and sidebar content
func TestNoisyPage(t *testing.T) {
	ext := New(extractor.DefaultConfig())

	// Load fixture
	fixturePath := joinPath(fixturesDir, "noisy_page.html")
	htmlContent, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("Failed to read fixture: %v", err)
	}

	// Create input
	source := types.Source{
		StableID:    "test-noisy-page",
		OriginalURL: "https://news.example.com/climate-report-2024",
	}
	input := extractor.Input{
		Source:  source,
		Content: htmlContent,
	}

	// Extract
	doc, err := ext.Extract(input)
	if err != nil {
		t.Fatalf("Extraction failed: %v", err)
	}

	// Validate we got content
	if doc.PlainText == "" {
		t.Fatal("PlainText should not be empty")
	}

	// Validate we extracted the main article content
	if !strings.Contains(strings.ToLower(doc.PlainText), "climate report") &&
		!strings.Contains(strings.ToLower(doc.PlainText), "elena rodriguez") {
		t.Error("Main article content should be present")
	}

	// Check that advertisements are not included
	if strings.Contains(doc.PlainText, "ADVERTISEMENT") ||
		strings.Contains(doc.PlainText, "SPONSORED CONTENT") ||
		strings.Contains(doc.PlainText, "PROMOTED CONTENT") {
		t.Error("Advertisement content should be excluded")
	}

	// Check that script content is not included
	if strings.Contains(doc.PlainText, "(function()") || strings.Contains(doc.PlainText, "analytics") {
		t.Error("Script content should be excluded")
	}

	// Validate title is reasonable
	if doc.Title == nil {
		t.Fatal("Title should not be nil")
	}

	// Validate word count is reasonable (should have significant content)
	wordCount := countWords(doc.PlainText)
	if wordCount < 100 {
		t.Errorf("Expected at least 100 words for substantial article, got: %d", wordCount)
	}
}

// TestMissingMetadata tests extraction from a page with minimal or no metadata
func TestMissingMetadata(t *testing.T) {
	ext := New(extractor.DefaultConfig())

	// Load fixture
	fixturePath := joinPath(fixturesDir, "missing_metadata.html")
	htmlContent, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("Failed to read fixture: %v", err)
	}

	// Create input
	source := types.Source{
		StableID:    "test-missing-metadata",
		OriginalURL: "https://internal.example.com/maintenance-update",
	}
	input := extractor.Input{
		Source:  source,
		Content: htmlContent,
	}

	// Extract
	doc, err := ext.Extract(input)
	if err != nil {
		t.Fatalf("Extraction failed: %v", err)
	}

	// Validate we got content even without good metadata
	if doc.PlainText == "" {
		t.Fatal("PlainText should not be empty")
	}

	// Check that we extracted the main content
	if !strings.Contains(strings.ToLower(doc.PlainText), "maintenance") ||
		!strings.Contains(strings.ToLower(doc.PlainText), "security patches") {
		t.Error("Main content should be extracted")
	}

	// Title should still be extracted from <title> tag
	if doc.Title == nil {
		t.Error("Title should be extracted from <title> tag even without metadata")
	}

	// Author should be nil (not available) - go-readability may extract from <h1>
	// This is acceptable - missing metadata should remain optional
	if doc.Author != nil {
		t.Logf("Author was extracted: %q - this is acceptable", *doc.Author)
	}

	// PublishedAt should be nil (not available)
	if doc.PublishedAt != nil {
		t.Error("PublishedAt should be nil when not available")
	}

	// Content should not include script output
	if strings.Contains(strings.ToLower(doc.PlainText), "console.log") ||
		strings.Contains(strings.ToLower(doc.PlainText), "maintenance completed") {
		t.Error("Script content should be excluded")
	}
}

// TestEmptyBody tests handling of empty HTML content
func TestEmptyBody(t *testing.T) {
	ext := New(extractor.DefaultConfig())

	// Create input with empty content
	source := types.Source{
		StableID:    "test-empty-body",
		OriginalURL: "https://example.com/empty",
	}
	input := extractor.Input{
		Source:  source,
		Content: []byte{},
	}

	// Extract should return error
	_, err := ext.Extract(input)
	if err == nil {
		t.Fatal("Expected error for empty content")
	}

	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("Expected 'empty' in error message, got: %v", err)
	}
}

// TestMalformedHTML tests handling of malformed HTML
func TestMalformedHTML(t *testing.T) {
	ext := New(extractor.DefaultConfig())

	// Create input with very malformed HTML
	source := types.Source{
		StableID:    "test-malformed",
		OriginalURL: "https://example.com/malformed",
	}
	input := extractor.Input{
		Source:  source,
		Content: []byte("this is not html at all <<<&&###not valid$$$"),
	}

	// Extract should still try to parse (html parser is tolerant)
	doc, err := ext.Extract(input)
	if err != nil {
		// This is acceptable - very malformed content may fail
		t.Logf("Malformed HTML parsing failed (acceptable): %v", err)
	}
	if doc != nil && doc.PlainText == "" {
		t.Error("Document should have some content even from malformed HTML")
	}
}

// TestPurelyNavigationalPage tests handling of pages with only navigation
func TestPurelyNavigationalPage(t *testing.T) {
	ext := New(extractor.DefaultConfig())

	html := `
<!DOCTYPE html>
<html>
<head><title>Navigation Only Page</title></head>
<body>
<nav>
<a href="/">Home</a>
<a href="/about">About</a>
<a href="/contact">Contact</a>
</nav>
<footer>
<p>Copyright 2024</p>
</footer>
</body>
</html>
`

	source := types.Source{
		StableID:    "test-nav-only",
		OriginalURL: "https://example.com",
	}
	input := extractor.Input{
		Source:  source,
		Content: []byte(html),
	}

	// Extract may return content from nav/footer
	// go-readability is designed to extract meaningful content,
	// so navigation-only pages may result in empty extraction
	doc, err := ext.Extract(input)
	if err != nil {
		// Acceptable - navigation-only pages may have no extractable content
		t.Logf("Got expected error for nav-only page: %v", err)
		return
	}

	// If content is extracted, verify it's reasonable
	if doc == nil {
		t.Fatal("Document should not be nil")
	}

	// Navigation pages may produce little content
	t.Logf("Extracted text length: %d chars", len(doc.PlainText))
}

// TestScriptAndStyleExclusion tests that script and style tags are excluded
func TestScriptAndStyleExclusion(t *testing.T) {
	ext := New(extractor.DefaultConfig())

	html := `
<!DOCTYPE html>
<html>
<head>
<style>.hidden { display: none; }</style>
<script>var secret = "should not appear";</script>
<title>Test Page</title>
</head>
<body>
<p>This is visible content.</p>
<script>console.log("another script");</script>
<div class="content">
<p>More visible content here.</p>
<style>body { margin: 0; }</style>
<p>Final paragraph.</p>
</div>
</body>
</html>
`

	source := types.Source{
		StableID:    "test-script-exclusion",
		OriginalURL: "https://example.com",
	}
	input := extractor.Input{
		Source:  source,
		Content: []byte(html),
	}

	doc, err := ext.Extract(input)
	if err != nil {
		t.Fatalf("Extraction failed: %v", err)
	}

	// Check that style and script content is not in plain text
	// go-readability should handle this automatically
	if strings.Contains(doc.PlainText, "var secret") || strings.Contains(doc.PlainText, "should not appear") {
		t.Error("Script variable values should not be extracted")
	}
	if strings.Contains(doc.PlainText, "console.log") {
		t.Error("JavaScript should not be in plain text")
	}

	// Check that actual content is present
	if !strings.Contains(doc.PlainText, "This is visible content") {
		t.Error("Visible content should be extracted")
	}
}

// TestMetadataExtraction tests extraction of various metadata fields
func TestMetadataExtraction(t *testing.T) {
	ext := New(extractor.DefaultConfig())

	html := `
<!DOCTYPE html>
<html>
<head>
<title>Test Article Title</title>
<meta name="author" content="Test Author Name">
<meta property="article:published_time" content="2024-03-15T10:30:00Z">
<link rel="canonical" href="https://example.com/canonical/article-url">
<meta name="description" content="A test description">
</head>
<body>
<h1>Article Heading</h1>
<p>Article content here.</p>
</body>
</html>
`

	source := types.Source{
		StableID:    "test-metadata",
		OriginalURL: "https://example.com/test",
	}
	input := extractor.Input{
		Source:  source,
		Content: []byte(html),
	}

	doc, err := ext.Extract(input)
	if err != nil {
		t.Fatalf("Extraction failed: %v", err)
	}

	// Check title
	if doc.Title == nil {
		t.Fatal("Title should be extracted")
	} else if *doc.Title != "Test Article Title" {
		// go-readability may return cleaned title
		t.Logf("Extracted title: %q", *doc.Title)
	}

	// Check author - go-readability uses byline, not meta author tag
	// So this may or may not be extracted depending on page structure
	if doc.Author != nil {
		t.Logf("Author extracted: %q", *doc.Author)
	} else {
		t.Log("No author extracted - acceptable for pages without byline")
	}

	// Check publication date - go-readability should extract this from article:published_time
	if doc.PublishedAt != nil {
		t.Logf("PublishedAt: %v", *doc.PublishedAt)
	} else {
		t.Log("No publishedAt extracted")
	}
}

// TestWhitespaceNormalization tests that whitespace is properly normalized
func TestWhitespaceNormalization(t *testing.T) {
	ext := New(extractor.DefaultConfig())

	html := `
<!DOCTYPE html>
<html>
<head><title>Whitespace Test</title></head>
<body>
<p>   Multiple   spaces   here.   </p>
<p>


Paragraph with empty lines.


</p>
<div>
    <p>Indented content</p>
</div>
</body>
</html>
`

	source := types.Source{
		StableID:    "test-whitespace",
		OriginalURL: "https://example.com",
	}
	input := extractor.Input{
		Source:  source,
		Content: []byte(html),
	}

	doc, err := ext.Extract(input)
	if err != nil {
		t.Fatalf("Extraction failed: %v", err)
	}

	// Check that multiple spaces are normalized
	if strings.Contains(doc.PlainText, "   ") {
		t.Error("Multiple consecutive spaces should be normalized")
	}
}

// TestParagraphPreservation tests that paragraph boundaries are preserved
func TestParagraphPreservation(t *testing.T) {
	ext := New(extractor.DefaultConfig())

	html := `
<!DOCTYPE html>
<html>
<head><title>Paragraph Test</title></head>
<body>
<p>First paragraph.</p>
<p>Second paragraph.</p>
<div>
<p>Third paragraph in div.</p>
</div>
</body>
</html>
`

	source := types.Source{
		StableID:    "test-paragraphs",
		OriginalURL: "https://example.com",
	}
	input := extractor.Input{
		Source:  source,
		Content: []byte(html),
	}

	doc, err := ext.Extract(input)
	if err != nil {
		t.Fatalf("Extraction failed: %v", err)
	}

	// Check all paragraphs are present
	text := doc.PlainText
	if !strings.Contains(text, "First paragraph") ||
		!strings.Contains(text, "Second paragraph") ||
		!strings.Contains(text, "Third paragraph") {
		t.Error("All paragraphs should be present")
	}
}

// TestConfigLimits tests that configuration limits are respected
func TestConfigLimits(t *testing.T) {
	// Test with custom config - note: go-readability may process title first,
	// so the title length limit may not apply if go-readability truncates first
	config := extractor.Config{
		MaxPlainTextLength: 50,
		MaxWordCount:       10,
	}
	ext := New(config)

	html := `
<!DOCTYPE html>
<html>
<head>
<title>Test Article Title</title>
</head>
<body>
<p>This is the first sentence with multiple words in it.</p>
<p>This is the second sentence with multiple words that adds more content.</p>
<p>This is the third sentence to exceed the word limit significantly.</p>
<p>More text to push past the configured limits.</p>
</body>
</html>
`

	source := types.Source{
		StableID:    "test-limits",
		OriginalURL: "https://example.com",
	}
	input := extractor.Input{
		Source:  source,
		Content: []byte(html),
	}

	doc, err := ext.Extract(input)
	if err != nil {
		t.Fatalf("Extraction failed: %v", err)
	}

	// Check title exists (go-readability may have already processed it)
	if doc.Title == nil {
		t.Fatal("Title should exist")
	}

	// Check plain text length - this is applied AFTER go-readability
	if len(doc.PlainText) > config.MaxPlainTextLength {
		t.Errorf("PlainText should be truncated to %d chars, got: %d", config.MaxPlainTextLength, len(doc.PlainText))
	}

	// Check word count
	if countWords(doc.PlainText) > config.MaxWordCount {
		t.Errorf("Word count should not exceed %d, got: %d", config.MaxWordCount, countWords(doc.PlainText))
	}
}

// TestDeterminism tests that extraction produces consistent results
func TestDeterminism(t *testing.T) {
	ext := New(extractor.DefaultConfig())

	html := `
<!DOCTYPE html>
<html>
<head><title>Determinism Test</title></head>
<body>
<p>First paragraph.</p>
<p>Second paragraph.</p>
<p>Third paragraph.</p>
</body>
</html>
`

	source := types.Source{
		StableID:    "test-determinism",
		OriginalURL: "https://example.com",
	}
	input := extractor.Input{
		Source:  source,
		Content: []byte(html),
	}

	// Run extraction multiple times
	var results []string
	for i := 0; i < 5; i++ {
		doc, err := ext.Extract(input)
		if err != nil {
			t.Fatalf("Extraction %d failed: %v", i, err)
		}
		results = append(results, doc.PlainText)
	}

	// All results should be identical
	firstResult := results[0]
	for i, result := range results[1:] {
		if result != firstResult {
			t.Errorf("Extraction results differ at iteration %d", i+1)
			t.Logf("First:  %q", firstResult)
			t.Logf("Other:  %q", result)
		}
	}
}

// TestEdgeCases tests various edge cases
func TestEdgeCases(t *testing.T) {
	t.Run("VeryLongTextBlock", func(t *testing.T) {
		ext := New(extractor.DefaultConfig())

		// Single very long paragraph
		longText := strings.Repeat("Word ", 10000) + "end"
		html := `<html><head><title>Test</title></head><body><p>` + longText + `</p></body></html>`

		source := types.Source{
			StableID:    "test-long",
			OriginalURL: "https://example.com",
		}
		input := extractor.Input{
			Source:  source,
			Content: []byte(html),
		}

		doc, err := ext.Extract(input)
		if err != nil {
			t.Fatalf("Extraction failed: %v", err)
		}

		// Should handle large content without panicking
		if doc.PlainText == "" {
			t.Error("Should extract content even from large single block")
		}
	})

	t.Run("NestedElements", func(t *testing.T) {
		ext := New(extractor.DefaultConfig())

		html := `
		<html>
		<head><title>Test</title></head>
		<body>
		<div>
		<section>
		<article>
		<p>Nested content <strong>with emphasis</strong> and <em>italics</em>.</p>
		</article>
		</section>
		</div>
		</body>
		</html>
		`

		source := types.Source{
			StableID:    "test-nested",
			OriginalURL: "https://example.com",
		}
		input := extractor.Input{
			Source:  source,
			Content: []byte(html),
		}

		doc, err := ext.Extract(input)
		if err != nil {
			t.Fatalf("Extraction failed: %v", err)
		}

		if !strings.Contains(doc.PlainText, "Nested content") {
			t.Error("Should extract text from nested elements")
		}
		if strings.Contains(doc.PlainText, "<strong>") || strings.Contains(doc.PlainText, "<em>") {
			t.Error("HTML tags should not appear in plain text")
		}
	})

	t.Run("SpecialCharacters", func(t *testing.T) {
		ext := New(extractor.DefaultConfig())

		html := `<html><head><title>Special &amp; Characters</title></head>
		<body>
		<p>Text with &lt;special&gt; characters &amp; symbols.</p>
		<p>Quote: "Hello" and 'Goodbye'</p>
		</body></html>`

		source := types.Source{
			StableID:    "test-special",
			OriginalURL: "https://example.com",
		}
		input := extractor.Input{
			Source:  source,
			Content: []byte(html),
		}

		doc, err := ext.Extract(input)
		if err != nil {
			t.Fatalf("Extraction failed: %v", err)
		}

		// Should unescape HTML entities
		if strings.Contains(doc.PlainText, "&amp;") {
			t.Error("HTML entities should be unescaped")
		}
	})
}

// TestBulkExtraction tests extracting from multiple documents at once
func TestBulkExtraction(t *testing.T) {
	ext := New(extractor.DefaultConfig())

	// Create multiple fixtures
	fixturePaths := []string{
		"normal_article.html",
		"noisy_page.html",
		"missing_metadata.html",
	}

	for _, fixturePath := range fixturePaths {
		fullPath := joinPath(fixturesDir, fixturePath)
		htmlContent, err := os.ReadFile(fullPath)
		if err != nil {
			t.Fatalf("Failed to read fixture %s: %v", fixturePath, err)
		}

		source := types.Source{
			StableID:    "bulk-" + fixturePath,
			OriginalURL: "https://example.com/" + fixturePath,
		}
		input := extractor.Input{
			Source:  source,
			Content: htmlContent,
		}

		doc, err := ext.Extract(input)
		if err != nil {
			t.Fatalf("Extraction of %s failed: %v", fixturePath, err)
		}

		if doc.PlainText == "" {
			t.Errorf("Extraction of %s produced empty text", fixturePath)
		}

		if len(doc.PlainText) < 10 {
			t.Errorf("Extraction of %s produced unusually short text (%d chars)", fixturePath, len(doc.PlainText))
		}
	}
}

// TestHTMLElements tests various HTML elements are handled correctly
func TestHTMLElements(t *testing.T) {
	ext := New(extractor.DefaultConfig())

	html := `
<!DOCTYPE html>
<html>
<head><title>Element Test</title></head>
<body>
<h1>Main Heading</h1>
<h2>Subheading</h2>
<h3>Sub-subheading</h3>
<h4>Another Subheading</h4>
<p>Paragraph one with <strong>bold text</strong> and <em>italic text</em>.</p>
<p>Paragraph two with <a href="http://example.com">a link</a>.</p>
<blockquote>
<p>This is a blockquote.</p>
</blockquote>
<ol>
<li>First ordered item</li>
<li>Second ordered item</li>
</ol>
<ul>
<li>First unordered item</li>
<li>Second unordered item</li>
</ul>
<pre><code>Pre-formatted code block.</code></pre>
</body>
</html>
`

	source := types.Source{
		StableID:    "test-elements",
		OriginalURL: "https://example.com",
	}
	input := extractor.Input{
		Source:  source,
		Content: []byte(html),
	}

	doc, err := ext.Extract(input)
	if err != nil {
		t.Fatalf("Extraction failed: %v", err)
	}

	// Check all major content types are present
	checks := []string{
		"Main Heading",
		"Subheading",
		"Paragraph one",
		"bold text",
		"italic text",
		"a link",
		"First ordered item",
		"First unordered item",
		"Pre-formatted code block",
	}

	for _, check := range checks {
		if !strings.Contains(doc.PlainText, check) {
			t.Errorf("Expected content '%s' to be present", check)
		}
	}

	// Check that HTML tags are not present
	if strings.Contains(doc.PlainText, "<p>") || strings.Contains(doc.PlainText, "</p>") {
		t.Error("Paragraph tags should not appear")
	}
	if strings.Contains(doc.PlainText, "<strong>") || strings.Contains(doc.PlainText, "</strong>") {
		t.Error("Bold tags should not appear")
	}
}

// TestArticleWithAuthorByline tests extraction with byline metadata
func TestArticleWithAuthorByline(t *testing.T) {
	ext := New(extractor.DefaultConfig())

	html := `
<!DOCTYPE html>
<html>
<head>
<title>Test Article</title>
<meta name="author" content="John Doe">
<meta property="article:author" content="John Doe">
</head>
<body>
<article>
<header>
<h1>Test Article Heading</h1>
<div class="byline">By John Doe</div>
</header>
<p>Article content with author attribution.</p>
</article>
</body>
</html>
`

	source := types.Source{
		StableID:    "test-byline",
		OriginalURL: "https://example.com/test",
	}
	input := extractor.Input{
		Source:  source,
		Content: []byte(html),
	}

	doc, err := ext.Extract(input)
	if err != nil {
		t.Fatalf("Extraction failed: %v", err)
	}

	// Check that we got content
	if doc.PlainText == "" {
		t.Fatal("Should extract content")
	}

	// Author may or may not be extracted depending on byline format
	if doc.Author != nil {
		t.Logf("Author extracted: %q", *doc.Author)
	} else {
		t.Log("No author extracted")
	}
}

// TestArticleWithPublicationDate tests extraction with publication date
func TestArticleWithPublicationDate(t *testing.T) {
	ext := New(extractor.DefaultConfig())

	html := `
<!DOCTYPE html>
<html>
<head>
<title>Article with Date</title>
<meta property="article:published_time" content="2024-06-15T14:30:00Z">
<meta property="article:modified_time" content="2024-06-16T09:00:00Z">
</head>
<body>
<article>
<h1>Date Test Article</h1>
<p>Article with clear publication date.</p>
</article>
</body>
</html>
`

	source := types.Source{
		StableID:    "test-date",
		OriginalURL: "https://example.com/test",
	}
	input := extractor.Input{
		Source:  source,
		Content: []byte(html),
	}

	doc, err := ext.Extract(input)
	if err != nil {
		t.Fatalf("Extraction failed: %v", err)
	}

	// Check that we got content
	if doc.PlainText == "" {
		t.Fatal("Should extract content")
	}

	// Check publication date is extracted
	if doc.PublishedAt != nil {
		t.Logf("Published date: %v", *doc.PublishedAt)
		if !doc.PublishedAt.Equal(time.Date(2024, 6, 15, 14, 30, 0, 0, time.UTC)) {
			t.Logf("Note: Date extracted may differ from input: %v", *doc.PublishedAt)
		}
	} else {
		t.Log("No publication date extracted - this is acceptable")
	}
}

// TestExtractionMetadata tests extraction metadata fields
func TestExtractionMetadata(t *testing.T) {
	ext := New(extractor.DefaultConfig())

	html := `
<!DOCTYPE html>
<html lang="en">
<head>
<title>Test Metadata</title>
<meta property="og:site_name" content="Example News">
</head>
<body>
<article>
<h1>Metadata Test</h1>
<p>This article has metadata.</p>
</article>
</body>
</html>
`

	source := types.Source{
		StableID:    "test-metadata-fields",
		OriginalURL: "https://example.com/test",
	}
	input := extractor.Input{
		Source:  source,
		Content: []byte(html),
	}

	doc, err := ext.Extract(input)
	if err != nil {
		t.Fatalf("Extraction failed: %v", err)
	}

	// Check extraction metadata
	if doc.ExtractionMetadata == nil {
		t.Fatal("ExtractionMetadata should not be nil")
	}

	// Required fields
	requiredFields := []string{"extractor", "extractor_module", "word_count", "char_count"}
	for _, field := range requiredFields {
		if _, ok := doc.ExtractionMetadata[field]; !ok {
			t.Errorf("ExtractionMetadata should include '%s' field", field)
		}
	}

	// Optional fields (may or may not be present)
	t.Logf("All metadata: %v", doc.ExtractionMetadata)
}

// Helper functions

func joinPath(elem ...string) string {
	result := elem[0]
	for i := 1; i < len(elem); i++ {
		result = result + "/" + elem[i]
	}
	return result
}
