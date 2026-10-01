// Package html provides HTML content extraction capabilities.
//
// This extractor processes HTML content and extracts:
// - Document title
// - Main content text (excluding scripts, styles, navigation)
// - Canonical URL
// - Publication metadata (author, publication date)
//
// The extractor uses pure Go (golang.org/x/net/html) for parsing and avoids
// network calls or LLM invocations.
package html

import (
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"time"

	xhtml "golang.org/x/net/html"

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

	// Parse HTML
	doc, err := xhtml.Parse(strings.NewReader(string(input.Content)))
	if err != nil {
		return nil, &extractor.ExtractionError{
			Code:      "parse_error",
			Message:   fmt.Sprintf("failed to parse HTML: %v", err),
			SourceURL: input.Source.OriginalURL,
		}
	}

	// Extract content and metadata
	content := e.extractContent(doc, input.Source.OriginalURL)
	if content == "" {
		return nil, &extractor.ExtractionError{
			Code:      "empty_content",
			Message:   "no extractable content found in HTML",
			SourceURL: input.Source.OriginalURL,
		}
	}

	// Extract metadata (title, author, published date)
	title, canonicalURL, publishedAt, author := e.extractMetadata(doc, input.Source.OriginalURL)

	// Build document
	now := time.Now().UTC()
	docResult := &types.Document{
		SourceID: input.Source.StableID,
		CanonicalURL: func() *string {
			if canonicalURL != "" {
				return types.PointerTo(canonicalURL)
			}
			return nil
		}(),
		Title: func() *string {
			if title != "" {
				return types.PointerTo(title)
			}
			return nil
		}(),
		PlainText:   content,
		Author:      author,
		PublishedAt: publishedAt,
		RetrievedAt: now,
		ExtractionMetadata: map[string]string{
			"extractor":  "html-extractor-v1",
			"word_count": fmt.Sprintf("%d", countWords(content)),
			"char_count": fmt.Sprintf("%d", len(content)),
		},
	}

	// Apply length limits if set
	if e.config.MaxPlainTextLength > 0 && len(docResult.PlainText) > e.config.MaxPlainTextLength {
		docResult.PlainText = docResult.PlainText[:e.config.MaxPlainTextLength]
	}

	return docResult, nil
}

// extractContent processes the HTML document and extracts meaningful content.
func (e *Extractor) extractContent(doc *xhtml.Node, sourceURL string) string {
	// First, try to find the main content element
	mainNode := e.findMainContent(doc)

	var builder strings.Builder

	if mainNode != nil {
		// Extract from main element
		e.extractNodeText(mainNode, &builder)
	} else {
		// Fallback: extract from body, excluding noise elements
		bodyNode := e.findBody(doc)
		if bodyNode != nil {
			e.extractBodyExcludingNoise(bodyNode, &builder)
		}
	}

	text := strings.TrimSpace(builder.String())

	// Clean up excessive whitespace
	text = e.normalizeWhitespace(text)

	// Apply word count limit if configured
	if e.config.MaxWordCount > 0 {
		words := strings.Split(text, " ")
		if len(words) > e.config.MaxWordCount {
			text = strings.Join(words[:e.config.MaxWordCount], " ")
			// Trim to last complete word
			if idx := strings.LastIndex(text, " "); idx > 0 {
				text = text[:idx]
			}
		}
	}

	return text
}

// findMainContent searches for the main content element in the document.
func (e *Extractor) findMainContent(doc *xhtml.Node) *xhtml.Node {
	// Priority 1: <main> element
	mainNode := e.findElementByTag(doc, "main")
	if mainNode != nil && !e.isNoiseElement(mainNode) {
		return mainNode
	}

	// Priority 2: Article elements
	articleNodes := e.findElementsByTagName(doc, "article")
	if len(articleNodes) > 0 {
		// Return the first non-noise article
		for _, node := range articleNodes {
			if !e.isNoiseElement(node) {
				return node
			}
		}
		// If all are noise, return the first one (might still have useful content)
		return articleNodes[0]
	}

	// Priority 3: Elements with ID "content", "main", "article", etc.
	contentIDs := []string{"content", "main", "article", "post", "body-content", "post-content"}
	for _, id := range contentIDs {
		node := e.findElementByID(doc, id)
		if node != nil && !e.isNoiseElement(node) {
			return node
		}
	}

	// Priority 4: Elements with class containing "content", "article", etc.
	// Use more specific class patterns to avoid false positives
	contentClasses := []string{"main-content", "article-content", "post-content", "entry-content", "content-wrapper", "main", "article"}
	for _, class := range contentClasses {
		node := e.findElementByClass(doc, class)
		if node != nil && !e.isNoiseElement(node) {
			return node
		}
	}

	return nil
}

// findBody returns the <body> element.
func (e *Extractor) findBody(doc *xhtml.Node) *xhtml.Node {
	return e.findElementByTag(doc, "body")
}

// extractBodyExcludingNoise extracts text from the body while excluding
// navigation, scripts, styles, and other noise elements.
func (e *Extractor) extractBodyExcludingNoise(body *xhtml.Node, builder *strings.Builder) {
	e.walkBodyExcludingNoise(body, builder)
}

// walkBodyExcludingNoise recursively walks the body tree, skipping noise.
func (e *Extractor) walkBodyExcludingNoise(node *xhtml.Node, builder *strings.Builder) {
	switch node.Type {
	case xhtml.ElementNode:
		if e.shouldSkipElement(node) {
			return
		}

		// Extract text from this element's children
		// extractNodeText recursively handles all descendants
		e.extractNodeText(node, builder)

		// Note: We don't need to walk children separately here
		// because extractNodeText already handles recursion

	case xhtml.TextNode:
		text := strings.TrimSpace(node.Data)
		if text != "" {
			// Add newline before text if builder is not empty
			if builder.Len() > 0 {
				builder.WriteString("\n\n")
			}
			builder.WriteString(text)
		}
	}
}

// extractNodeText extracts all text from a node and its descendants.
func (e *Extractor) extractNodeText(node *xhtml.Node, builder *strings.Builder) {
	if node.Type != xhtml.ElementNode {
		return
	}

	for child := node.FirstChild; child != nil; child = child.NextSibling {
		// Skip noise elements
		if e.shouldSkipElement(child) {
			continue
		}

		switch child.Type {
		case xhtml.TextNode:
			text := strings.TrimSpace(child.Data)
			if text != "" {
				// Add paragraph break for significant text blocks
				if builder.Len() > 0 {
					// Check if previous content ends with double newline
					endsWithDoubleNewline := strings.HasSuffix(builder.String(), "\n\n")
					if !endsWithDoubleNewline {
						builder.WriteString(" ")
					}
				}
				builder.WriteString(text)
			}
		case xhtml.ElementNode:
			// Recursively extract from child elements
			e.extractNodeText(child, builder)
		}
	}
}

// shouldSkipElement returns true if the element should be skipped during extraction.
func (e *Extractor) shouldSkipElement(node *xhtml.Node) bool {
	// Skip script and style elements
	switch node.DataAtom.String() {
	case "script", "style", "noscript", "iframe", "object", "embed", "svg", "math":
		return true
	}

	// Skip by ID patterns
	id := e.getAttributeValue(node, "id")
	if e.isNoiseID(id) {
		return true
	}

	// Skip by class patterns
	classes := e.getAttributeValue(node, "class")
	return e.isNoiseClass(classes)
}

// isNoiseElement returns true if the element is considered noise.
func (e *Extractor) isNoiseElement(node *xhtml.Node) bool {
	// Check by tag
	switch node.DataAtom.String() {
	case "nav", "footer", "header", "aside", "form", "blockquote", "hr":
		return false // Sometimes useful, check further
	case "script", "style", "noscript", "iframe", "object", "embed", "svg", "math", "template", "comment":
		return true
	}

	// Check by ID
	id := e.getAttributeValue(node, "id")
	if id != "" && e.isNoiseID(id) {
		return true
	}

	// Check by class
	classes := e.getAttributeValue(node, "class")
	if classes != "" && e.isNoiseClass(classes) {
		return true
	}

	return false
}

// isNoiseID checks if an element ID indicates it's noise.
func (e *Extractor) isNoiseID(id string) bool {
	noiseIDs := []string{
		"navigation", "nav", "menu", "sidebar", "ad", "advert", "banner",
		"footer", "footer-nav", "cookie", "privacy", "terms", "legal",
		"search", "search-box", "login", "sign-in", "auth", "comments",
		"related", "recommended", "share", "social", "share-buttons",
		"breadcrumb", "breadcrumbs", "tags", "category", "taxonomy",
	}
	idLower := strings.ToLower(id)
	for _, noise := range noiseIDs {
		if idLower == noise || strings.Contains(idLower, noise) {
			return true
		}
	}
	return false
}

// isNoiseClass checks if an element class indicates it's noise.
func (e *Extractor) isNoiseClass(classes string) bool {
	noiseClasses := []string{
		"nav", "navigation", "menu", "sidebar", "ad", "advert", "advertisement",
		"banner", "advertisement-container", "footer", "footer-nav",
		"cookie", "cookie-banner", "privacy", "legal", "legal-notice",
		"search", "search-box", "search-form", "login", "signin", "sign-in",
		"auth", "authentication", "comments", "comment-section",
		"related", "recommended", "sharing", "share-buttons", "share-tools",
		"social", "social-media", "social-share", "widget", "sidebar-widget",
		"breadcrumb", "breadcrumbs", "tags", "taxonomy", "filter", "filterable",
		"promo", "promotional", "sponsored", "native-ad", "native",
		"info-box", "infobox", "talk-page", "disclaimer", "notice", "warning",
	}
	classesLower := strings.ToLower(classes)
	for _, noise := range noiseClasses {
		if strings.Contains(classesLower, noise) {
			return true
		}
	}
	return false
}

// normalizeWhitespace normalizes whitespace in text content.
func (e *Extractor) normalizeWhitespace(text string) string {
	// Replace multiple spaces/tabs with single space
	spaceRe := regexp.MustCompile(`[ \t]+`)
	text = spaceRe.ReplaceAllString(text, " ")

	// Replace multiple newlines with double newline
	newlineRe := regexp.MustCompile(`[\n\r]+`)
	text = newlineRe.ReplaceAllString(text, "\n")

	// Trim leading/trailing whitespace from each line
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	text = strings.Join(lines, "\n")

	// Remove empty lines at start/end
	text = strings.Trim(text, "\n")

	// Remove multiple consecutive blank lines
	blankRe := regexp.MustCompile(`\n{3,}`)
	text = blankRe.ReplaceAllString(text, "\n\n")

	return text
}

// findElementByTag finds the first element with the given tag name.
func (e *Extractor) findElementByTag(doc *xhtml.Node, tag string) *xhtml.Node {
	return e.findElementByFunc(doc, func(n *xhtml.Node) bool {
		return n.Type == xhtml.ElementNode && strings.EqualFold(n.Data, tag)
	})
}

// findElementsByTagName finds all elements with the given tag name.
func (e *Extractor) findElementsByTagName(doc *xhtml.Node, tag string) []*xhtml.Node {
	var results []*xhtml.Node
	e.walkElementByFunc(doc, func(n *xhtml.Node) bool {
		return n.Type == xhtml.ElementNode && strings.EqualFold(n.Data, tag)
	}, func(n *xhtml.Node) {
		results = append(results, n)
	})
	return results
}

// findElementByID finds the first element with the given ID.
func (e *Extractor) findElementByID(doc *xhtml.Node, id string) *xhtml.Node {
	for child := doc.FirstChild; child != nil; child = child.NextSibling {
		node := e.findElementByFunc(child, func(n *xhtml.Node) bool {
			return n.Type == xhtml.ElementNode && e.getAttributeValue(n, "id") == id
		})
		if node != nil {
			return node
		}
	}
	return nil
}

// findElementByClass finds the first element with the given class.
func (e *Extractor) findElementByClass(doc *xhtml.Node, class string) *xhtml.Node {
	for child := doc.FirstChild; child != nil; child = child.NextSibling {
		node := e.findElementByFunc(child, func(n *xhtml.Node) bool {
			if n.Type != xhtml.ElementNode {
				return false
			}
			nodeClass := e.getAttributeValue(n, "class")
			return strings.Contains(strings.ToLower(nodeClass), strings.ToLower(class))
		})
		if node != nil {
			return node
		}
	}
	return nil
}

// findElementByFunc finds the first element matching the predicate.
func (e *Extractor) findElementByFunc(root *xhtml.Node, pred func(*xhtml.Node) bool) *xhtml.Node {
	var result *xhtml.Node
	e.walkElementByFunc(root, pred, func(n *xhtml.Node) {
		if result == nil {
			result = n
		}
	})
	return result
}

// walkElementByFunc walks the tree, calling visited for each matching node.
func (e *Extractor) walkElementByFunc(root *xhtml.Node, pred func(*xhtml.Node) bool, visited func(*xhtml.Node)) {
	e.walkForPredicate(root, pred, visited)
}

// walkForPredicate walks the tree for the given predicate and visitor.
func (e *Extractor) walkForPredicate(node *xhtml.Node, pred func(*xhtml.Node) bool, visited func(*xhtml.Node)) {
	if node == nil {
		return
	}

	if pred(node) {
		visited(node)
	}

	for child := node.FirstChild; child != nil; child = child.NextSibling {
		e.walkForPredicate(child, pred, visited)
	}
}

// getAttributeValue returns the value of an attribute.
func (e *Extractor) getAttributeValue(node *xhtml.Node, attr string) string {
	for _, a := range node.Attr {
		if strings.EqualFold(a.Key, attr) {
			return a.Val
		}
	}
	return ""
}

// getTextContent extracts all text content from an element.
func (e *Extractor) getTextContent(node *xhtml.Node) string {
	var builder strings.Builder
	e.extractTextRecursive(node, &builder)
	return builder.String()
}

// extractTextRecursive recursively extracts text from a node tree.
func (e *Extractor) extractTextRecursive(node *xhtml.Node, builder *strings.Builder) {
	if node == nil {
		return
	}

	switch node.Type {
	case xhtml.TextNode:
		builder.WriteString(node.Data)
	case xhtml.ElementNode:
		// Skip script, style, and other non-visible elements
		if node.Data == "script" || node.Data == "style" || node.Data == "noscript" {
			return
		}
		// Continue with children
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			e.extractTextRecursive(child, builder)
		}
	}
}

// resolveURL resolves a relative URL against a base URL.
func (e *Extractor) resolveURL(baseURL, relativeURL string) string {
	if strings.HasPrefix(relativeURL, "http://") || strings.HasPrefix(relativeURL, "https://") {
		return relativeURL
	}

	// Parse base URL
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return relativeURL
	}

	// Handle special cases
	if relativeURL == "" {
		return parsed.String()
	}
	if strings.HasPrefix(relativeURL, "#") {
		// Fragment only
		base := *parsed
		base.Fragment = relativeURL[1:]
		return base.String()
	}
	if strings.HasPrefix(relativeURL, "?") {
		// Query only
		base := *parsed
		base.RawQuery = relativeURL[1:]
		return base.String()
	}

	// Relative URL - resolve against base
	absolute := parsed.ResolveReference(&url.URL{Path: relativeURL})
	return absolute.String()
}

// countWords counts the number of words in text.
func countWords(text string) int {
	if text == "" {
		return 0
	}
	return len(strings.Fields(text))
}

// layout is a helper for parsing date strings.
func layout(s string) (time.Time, bool) {
	formats := []string{
		time.RFC3339, time.RFC3339Nano,
		time.RFC1123, time.RFC1123Z, time.RFC822, time.RFC822Z,
		"2006-01-02", "2006-01-02 15:04:05", "2006-01-02T15:04:05",
		"January 2, 2006", "Jan 2, 2006", "2 January 2006",
		"2006-01-02T15:04:05Z07:00", "2006-01-02T15:04:05-07:00",
	}

	s = strings.TrimSpace(s)

	for _, format := range formats {
		t, err := time.Parse(format, s)
		if err == nil {
			return t, true
		}
	}

	return time.Time{}, false
}

// extractMetadata extracts title, canonical URL, author, and published date.
func (e *Extractor) extractMetadata(doc *xhtml.Node, sourceURL string) (string, string, *time.Time, *string) {
	// Extract title
	title := e.extractTitle(doc)

	// Extract canonical URL
	canonicalURL := e.extractCanonicalURL(doc, sourceURL)

	// Extract author and publication date
	author, publishedAt := e.extractArticleMetadata(doc)

	return title, canonicalURL, publishedAt, author
}

// extractTitle extracts the document title from various sources.
func (e *Extractor) extractTitle(doc *xhtml.Node) string {
	// Priority 1: <title> element
	titleNode := e.findElementByTag(doc, "title")
	if titleNode != nil {
		title := e.getTextContent(titleNode)
		title = html.UnescapeString(strings.TrimSpace(title))
		if len(title) > e.config.MaxTitleLength {
			title = title[:e.config.MaxTitleLength]
		}
		if title != "" {
			return title
		}
	}

	// Priority 2: Open Graph title
	ogTitle := e.extractMetaProperty(doc, "property", "og:title")
	if ogTitle != "" {
		ogTitle = html.UnescapeString(strings.TrimSpace(ogTitle))
		return ogTitle
	}

	// Priority 3: Twitter Card title
	twitterTitle := e.extractMetaAttribute(doc, "name", "twitter:title")
	if twitterTitle != "" {
		twitterTitle = html.UnescapeString(strings.TrimSpace(twitterTitle))
		return twitterTitle
	}

	// Priority 4: H1 element (if single or prominent)
	h1Nodes := e.findElementsByTagName(doc, "h1")
	if len(h1Nodes) == 1 {
		h1Text := strings.TrimSpace(e.getTextContent(h1Nodes[0]))
		h1Text = html.UnescapeString(h1Text)
		if h1Text != "" && len(h1Text) <= e.config.MaxTitleLength {
			return h1Text
		}
	}

	// Priority 5: meta description (fallback)
	description := e.extractMetaAttribute(doc, "name", "description")
	if description != "" {
		description = html.UnescapeString(strings.TrimSpace(description))
		return description
	}

	return ""
}

// extractCanonicalURL extracts the canonical URL from the document.
func (e *Extractor) extractCanonicalURL(doc *xhtml.Node, sourceURL string) string {
	// Priority 1: <link rel="canonical">
	link := e.findElementByTag(doc, "link")
	if link != nil {
		for _, a := range link.Attr {
			if strings.EqualFold(a.Key, "rel") && strings.EqualFold(a.Val, "canonical") {
				href := e.getAttributeValue(link, "href")
				if href != "" {
					return e.resolveURL(sourceURL, href)
				}
			}
		}
	}

	return ""
}

// extractArticleMetadata extracts author and publication date.
func (e *Extractor) extractArticleMetadata(doc *xhtml.Node) (*string, *time.Time) {
	var author string
	var publishedAt time.Time
	var hasPublished bool

	// Try microdata/metadata sources
	author = e.extractMetaAttribute(doc, "name", "author")
	if author == "" {
		author = e.extractMetaAttribute(doc, "name", "Authors")
	}
	if author == "" {
		author = e.extractMetaProperty(doc, "property", "og:article:author")
	}
	if author == "" {
		author = e.extractMetaAttribute(doc, "name", "twitter:creator")
	}

	if author != "" {
		author = strings.TrimSpace(html.UnescapeString(author))
	}

	// Try to extract publication date
	date := e.extractDate(doc)
	if date != (time.Time{}) {
		hasPublished = true
		publishedAt = date
	}

	var authorPtr *string
	if author != "" {
		authorPtr = types.PointerTo(author)
	}

	var publishedPtr *time.Time
	if hasPublished {
		publishedPtr = &publishedAt
	}

	return authorPtr, publishedPtr
}

// extractDate extracts publication date from various sources.
func (e *Extractor) extractDate(doc *xhtml.Node) time.Time {
	layout := func(s string) (time.Time, bool) {
		t, err := time.Parse(time.RFC3339, s)
		if err == nil {
			return t, true
		}
		t, err = time.Parse(time.RFC3339Nano, s)
		if err == nil {
			return t, true
		}
		// Common date formats
		formats := []string{
			time.RFC1123, time.RFC1123Z, time.RFC822, time.RFC822Z,
			"2006-01-02", "2006-01-02 15:04:05", "2006-01-02T15:04:05",
			"January 2, 2006", "Jan 2, 2006",
		}
		for _, format := range formats {
			t, err = time.Parse(format, s)
			if err == nil {
				return t, true
			}
		}
		return time.Time{}, false
	}

	// Priority 1: time element with datetime
	timeEl := e.findElementByTag(doc, "time")
	if timeEl != nil {
		for _, a := range timeEl.Attr {
			if strings.EqualFold(a.Key, "datetime") {
				t, ok := layout(a.Val)
				if ok {
					return t
				}
			}
		}
	}

	// Priority 2: meta property article:published_time
	if prop := e.extractMetaProperty(doc, "property", "article:published_time"); prop != "" {
		if t, ok := layout(prop); ok {
			return t
		}
	}

	// Priority 3: meta name article:published_time
	if meta := e.extractMetaAttribute(doc, "name", "article:published_time"); meta != "" {
		if t, ok := layout(meta); ok {
			return t
		}
	}

	// Priority 4: meta property article:modified_time
	if prop := e.extractMetaProperty(doc, "property", "article:modified_time"); prop != "" {
		if t, ok := layout(prop); ok {
			return t
		}
	}

	// Priority 5: PublishedDate in schema.org script
	schemaScript := e.findElementByFunc(doc, func(n *xhtml.Node) bool {
		return n.Type == xhtml.ElementNode && strings.EqualFold(n.Data, "script") &&
			strings.Contains(n.DataAtom.String(), "application/ld+json")
	})
	if schemaScript != nil {
		if date := e.extractJSONSchemaDate(schemaScript); !date.IsZero() {
			return date
		}
	}

	// Priority 6: rel="datePublished" or rel="date" links
	links := e.findElementsByTagName(doc, "link")
	for _, link := range links {
		for _, a := range link.Attr {
			if strings.EqualFold(a.Key, "rel") && (strings.EqualFold(a.Val, "datePublished") || strings.EqualFold(a.Val, "date")) {
				href := e.getAttributeValue(link, "href")
				if href != "" {
					// Try parsing URL as date (sometimes URLs contain dates)
					if t, ok := layout(href); ok {
						return t
					}
				}
			}
		}
	}

	return time.Time{}
}

// extractJSONSchemaDate extracts date from a JSON-LD script.
func (e *Extractor) extractJSONSchemaDate(script *xhtml.Node) time.Time {
	var builder strings.Builder
	for child := script.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == xhtml.TextNode {
			builder.WriteString(child.Data)
		}
	}

	content := builder.String()

	// Find datePublished, dateModified, or date in the JSON
	datePatterns := []string{"datePublished", "dateModified", "date", "publishedAt", "published_date"}

	for _, pattern := range datePatterns {
		patternRe := regexp.MustCompile(`"` + pattern + `"\s*:\s*"([^"]+)"`)
		matches := patternRe.FindStringSubmatch(content)
		if len(matches) > 1 {
			if t, ok := layout(matches[1]); ok {
				return t
			}
		}
	}

	return time.Time{}
}

// extractMetaProperty extracts a meta tag by property.
func (e *Extractor) extractMetaProperty(doc *xhtml.Node, attrName, propName string) string {
	metaNodes := e.findElementsByTagName(doc, "meta")
	for _, meta := range metaNodes {
		for _, a := range meta.Attr {
			if strings.EqualFold(a.Key, attrName) && strings.EqualFold(a.Val, propName) {
				return e.getAttributeValue(meta, "content")
			}
		}
	}
	return ""
}

// extractMetaAttribute extracts a meta tag by name.
func (e *Extractor) extractMetaAttribute(doc *xhtml.Node, attrName, nameVal string) string {
	metaNodes := e.findElementsByTagName(doc, "meta")
	for _, meta := range metaNodes {
		for _, a := range meta.Attr {
			if strings.EqualFold(a.Key, attrName) && strings.EqualFold(a.Val, nameVal) {
				return e.getAttributeValue(meta, "content")
			}
		}
	}
	return ""
}
