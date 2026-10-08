// Package renderers provides deterministic renderers for EditorialArtifact
// that do not require LLM calls.
package renderers

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
)

const (
	// BlueskyMaxChars is the maximum character count for a single Bluesky post.
	BlueskyMaxChars = 300

	// DefaultReferenceSectionTitle is the default title for the source/reference section.
	DefaultReferenceSectionTitle = "Sources"
)

// RendererError indicates a rendering failure.
type RendererError struct {
	Op            string // Operation that failed
	Err           error
	Deterministic bool // If true, the error is deterministic (input data error, not LLM)
}

func (e *RendererError) Error() string {
	return fmt.Sprintf("%s: %v", e.Op, e.Err)
}

func (e *RendererError) Unwrap() error {
	return e.Err
}

// IsRendererError reports whether e is a RendererError.
func IsRendererError(err error) bool {
	var re *RendererError
	return errors.As(err, &re)
}

// MarkdownRenderer renders EditorialArtifact to Markdown format.
type MarkdownRenderer struct {
	// IncludeSourceSection controls whether to include a sources/references section.
	IncludeSourceSection bool

	// SourceSectionTitle is the title for the sources section.
	SourceSectionTitle string

	// IncludeMetadataHeader controls whether to include generation metadata at the top.
	IncludeMetadataHeader bool
}

// NewMarkdownRenderer creates a new MarkdownRenderer with default settings.
func NewMarkdownRenderer() *MarkdownRenderer {
	return &MarkdownRenderer{
		IncludeSourceSection:  true,
		SourceSectionTitle:    DefaultReferenceSectionTitle,
		IncludeMetadataHeader: false,
	}
}

// SetIncludeSourceSection sets whether to include the sources section.
func (r *MarkdownRenderer) SetIncludeSourceSection(include bool) *MarkdownRenderer {
	r.IncludeSourceSection = include
	return r
}

// SetSourceSectionTitle sets the title for the sources section.
func (r *MarkdownRenderer) SetSourceSectionTitle(title string) *MarkdownRenderer {
	if title != "" {
		r.SourceSectionTitle = title
	}
	return r
}

// SetIncludeMetadataHeader sets whether to include generation metadata.
func (r *MarkdownRenderer) SetIncludeMetadataHeader(include bool) *MarkdownRenderer {
	r.IncludeMetadataHeader = include
	return r
}

// Render converts an EditorialArtifact to Markdown.
func (r *MarkdownRenderer) Render(artifact *contracts.EditorialArtifact) (string, error) {
	if artifact == nil {
		return "", &RendererError{Op: "Render", Err: errors.New("artifact is nil"), Deterministic: true}
	}

	if err := artifact.Validate(); err != nil {
		return "", &RendererError{Op: "Render", Err: fmt.Errorf("invalid artifact: %w", err), Deterministic: true}
	}

	var sb strings.Builder

	// Optional metadata header
	if r.IncludeMetadataHeader && artifact.GenerationMetadata.InputVerificationID != "" {
		sb.WriteString("---\n")
		fmt.Fprintf(&sb, "verification_id: %s\n", artifact.GenerationMetadata.InputVerificationID)
		fmt.Fprintf(&sb, "generated_at: %s\n", artifact.GenerationMetadata.GeneratedAt.UTC().Format(time.RFC3339))
		sb.WriteString("---\n\n")
	}

	// Title
	if artifact.Title != "" {
		fmt.Fprintf(&sb, "# %s\n\n", artifact.Title)
	}

	// Subtitle
	if artifact.Subtitle != "" {
		sb.WriteString(artifact.Subtitle)
		sb.WriteString("\n\n")
	}

	// Body text (for article and hybrid artifacts)
	if artifact.Body != "" {
		sb.WriteString(artifact.Body)
		sb.WriteString("\n\n")
	}

	// Sections (sorted by Order for deterministic output)
	if len(artifact.Sections) > 0 {
		sections := make([]contracts.Section, len(artifact.Sections))
		copy(sections, artifact.Sections)
		// Sort by Order field
		for i := 0; i < len(sections); i++ {
			for j := i + 1; j < len(sections); j++ {
				if sections[j].Order < sections[i].Order {
					sections[i], sections[j] = sections[j], sections[i]
				}
			}
		}
		for _, section := range sections {
			fmt.Fprintf(&sb, "## %s\n\n", section.Title)
			sb.WriteString(section.Body)
			sb.WriteString("\n\n")
		}
	}

	// Sources/References section
	if r.IncludeSourceSection && len(artifact.SourceReferences) > 0 {
		fmt.Fprintf(&sb, "## %s\n\n", r.SourceSectionTitle)

		// Collect and sort sources for deterministic output
		sourceIDs := make([]string, 0, len(artifact.SourceReferences))
		for id := range artifact.SourceReferences {
			sourceIDs = append(sourceIDs, id)
		}
		sortedSourceIDs := sortStrings(sourceIDs)

		for _, sourceID := range sortedSourceIDs {
			usage := artifact.SourceReferences[sourceID]
			citationText := usage.CitationText
			if citationText == "" {
				citationText = fmt.Sprintf("[Source: %s]", sourceID)
			}
			fmt.Fprintf(&sb, "- %s\n", citationText)
		}
		sb.WriteString("\n")
	}

	// Warnings section if there are any
	if len(artifact.Warnings) > 0 {
		sb.WriteString("## Warnings\n\n")
		for _, warning := range artifact.Warnings {
			fmt.Fprintf(&sb, "- **[%s]** %s\n", warning.WarningType, warning.Message)
			if len(warning.RelatedClaimIDs) > 0 {
				fmt.Fprintf(&sb, "  - Related claims: %s\n", strings.Join(sortStrings(warning.RelatedClaimIDs), ", "))
			}
		}
		sb.WriteString("\n")
	}

	return sb.String(), nil
}

// RenderToMarkdown is a convenience function that renders an EditorialArtifact
// to Markdown with default settings.
func RenderToMarkdown(artifact *contracts.EditorialArtifact) (string, error) {
	return NewMarkdownRenderer().Render(artifact)
}

// ThreadRenderer renders EditorialArtifact to a Bluesky thread format.
type ThreadRenderer struct {
	// MaxCharsPerPost is the maximum character count per post.
	// Defaults to BlueskyMaxChars (300).
	MaxCharsPerPost int

	// IncludeClaimReferences controls whether claim IDs are appended to posts.
	IncludeClaimReferences bool

	// ClaimRefFormat is the format for claim references.
	// Uses [CIDs: c1,c2,c3] format by default.
	ClaimRefFormat string
}

// NewThreadRenderer creates a new ThreadRenderer with default settings.
func NewThreadRenderer() *ThreadRenderer {
	return &ThreadRenderer{
		MaxCharsPerPost:        BlueskyMaxChars,
		IncludeClaimReferences: false,
		ClaimRefFormat:         "[CIDs: %s]",
	}
}

// SetMaxCharsPerPost sets the maximum character count per post.
func (r *ThreadRenderer) SetMaxCharsPerPost(max int) *ThreadRenderer {
	if max > 0 {
		r.MaxCharsPerPost = max
	}
	return r
}

// SetIncludeClaimReferences controls whether claim IDs are appended to posts.
func (r *ThreadRenderer) SetIncludeClaimReferences(include bool) *ThreadRenderer {
	r.IncludeClaimReferences = include
	return r
}

// SetClaimRefFormat sets the format for claim references.
// Format string should contain %s placeholder for comma-separated claim IDs.
func (r *ThreadRenderer) SetClaimRefFormat(format string) *ThreadRenderer {
	if format != "" {
		r.ClaimRefFormat = format
	}
	return r
}

// Render converts an EditorialArtifact to a Bluesky thread (slice of posts).
// Each post is guaranteed to be <= MaxCharsPerPost characters.
func (r *ThreadRenderer) Render(artifact *contracts.EditorialArtifact) ([]string, error) {
	if artifact == nil {
		return nil, &RendererError{Op: "Render", Err: errors.New("artifact is nil"), Deterministic: true}
	}

	if err := artifact.Validate(); err != nil {
		return nil, &RendererError{Op: "Render", Err: fmt.Errorf("invalid artifact: %w", err), Deterministic: true}
	}

	switch artifact.ArtifactType {
	case contracts.ArtifactTypeThread, contracts.ArtifactTypeHybrid:
		return r.renderFromPosts(artifact)
	case contracts.ArtifactTypeArticle:
		return r.renderFromBody(artifact)
	default:
		return nil, &RendererError{
			Op:            "Render",
			Err:           fmt.Errorf("unsupported artifact type: %s", artifact.ArtifactType),
			Deterministic: true,
		}
	}
}

// renderFromPosts splits posts from the Posts field for Thread/Hybrid artifacts.
func (r *ThreadRenderer) renderFromPosts(artifact *contracts.EditorialArtifact) ([]string, error) {
	var result []string

	// Sort posts by Order for deterministic output
	posts := make([]contracts.Post, len(artifact.Posts))
	copy(posts, artifact.Posts)
	for i := 0; i < len(posts); i++ {
		for j := i + 1; j < len(posts); j++ {
			if posts[j].Order < posts[i].Order {
				posts[i], posts[j] = posts[j], posts[i]
			}
		}
	}

	for i, post := range posts {
		postContents, err := r.splitPost(post.Body, post.ClaimIDs)
		if err != nil {
			if isIndivisibleSegmentError(err) {
				return result, &RendererError{
					Op:            "Render",
					Err:           fmt.Errorf("post %d contains indivisible segment: %w", i, err),
					Deterministic: true,
				}
			}
			return result, &RendererError{
				Op:            "Render",
				Err:           fmt.Errorf("splitting post %d: %w", i, err),
				Deterministic: true,
			}
		}
		result = append(result, postContents...)
	}

	return result, nil
}

// renderFromBody creates a thread from the Body field for Article artifacts.
func (r *ThreadRenderer) renderFromBody(artifact *contracts.EditorialArtifact) ([]string, error) {
	if artifact.Body == "" && len(artifact.Sections) == 0 {
		return nil, &RendererError{
			Op:            "Render",
			Err:           errors.New("article has no body or sections"),
			Deterministic: true,
		}
	}

	var fullContent string

	// Start with title if present
	if artifact.Title != "" {
		fullContent = artifact.Title
	}

	// Add body
	if artifact.Body != "" {
		if fullContent != "" {
			fullContent += "\n\n" + artifact.Body
		} else {
			fullContent = artifact.Body
		}
	}

	// Add sections (sorted by Order for deterministic output)
	if len(artifact.Sections) > 0 {
		sections := make([]contracts.Section, len(artifact.Sections))
		copy(sections, artifact.Sections)
		// Sort by Order field
		for i := 0; i < len(sections); i++ {
			for j := i + 1; j < len(sections); j++ {
				if sections[j].Order < sections[i].Order {
					sections[i], sections[j] = sections[j], sections[i]
				}
			}
		}
		for _, section := range sections {
			if fullContent != "" {
				fullContent += "\n\n"
			}
			fullContent += fmt.Sprintf("## %s\n\n%s", section.Title, section.Body)
		}
	}

	// Collect all claim IDs from sections for metadata
	var allClaimIDs []string
	for _, section := range artifact.Sections {
		allClaimIDs = append(allClaimIDs, section.ClaimIDs...)
	}

	// Also get claim IDs from claim references
	for claimID := range artifact.ClaimReferences {
		allClaimIDs = append(allClaimIDs, claimID)
	}

	// Remove duplicates and sort
	uniqueClaimIDs := uniqueAndSortStrings(allClaimIDs)

	return r.splitPost(fullContent, uniqueClaimIDs)
}

// splitPost splits a single post body into chunks that fit within the character limit.
// It returns an error if an indivisible segment cannot fit (i.e., a single sentence
// or word exceeds the limit).
func (r *ThreadRenderer) splitPost(body string, claimIDs []string) ([]string, error) {
	if body == "" {
		return nil, nil
	}

	// Clean up whitespace
	body = cleanWhitespace(body)

	// Calculate the claim reference suffix if included
	claimRefSuffix := ""
	if r.IncludeClaimReferences && len(claimIDs) > 0 {
		claimRefSuffix = " " + r.formatClaimReferences(claimIDs)
	}

	// Split by paragraphs first
	paragraphs := splitByParagraphs(body)

	if len(paragraphs) == 0 {
		return nil, nil
	}

	var result []string
	var currentPost strings.Builder

	for _, para := range paragraphs {
		if para == "" {
			continue
		}

		// Get the full content with claim ref
		paraContent := para + claimRefSuffix

		if len(paraContent) <= r.MaxCharsPerPost {
			// Paragraph fits with claim ref
			if currentPost.Len() > 0 && currentPost.Len()+len(paraContent)+1 <= r.MaxCharsPerPost {
				currentPost.WriteString(" ")
				currentPost.WriteString(para)
			} else if currentPost.Len() == 0 {
				currentPost.WriteString(para)
			} else {
				// Current post is full, save it and start new one
				if currentPost.Len() > 0 {
					result = append(result, currentPost.String()+claimRefSuffix)
				}
				result = append(result, paraContent)
				currentPost.Reset()
			}
		} else {
			// Paragraph exceeds limit, try splitting by sentence
			sentences := splitBySentences(para)
			splitCompleted := false

			for _, sentence := range sentences {
				if sentence == "" {
					continue
				}

				sentenceContent := sentence + claimRefSuffix

				if len(sentenceContent) > r.MaxCharsPerPost {
					// Sentence exceeds limit, try splitting by boundaries
					subParts := splitByBoundaries(sentence)
					splitFound := false

					for _, subPart := range subParts {
						if subPart == "" {
							continue
						}
						subPartContent := subPart + claimRefSuffix

						if len(subPartContent) > r.MaxCharsPerPost {
							// Even this part is too long - try without claim ref
							subPartOnly := subPart
							if len(subPartOnly) <= r.MaxCharsPerPost {
								if currentPost.Len() > 0 && currentPost.Len()+len(subPartOnly)+1 <= r.MaxCharsPerPost {
									currentPost.WriteString(" ")
									currentPost.WriteString(subPart)
								} else if currentPost.Len() == 0 {
									currentPost.WriteString(subPart)
								} else {
									result = append(result, currentPost.String()+claimRefSuffix)
									result = append(result, subPartOnly)
									currentPost.Reset()
								}
								splitFound = true
								break
							}
							continue
						}

						if currentPost.Len() > 0 && currentPost.Len()+len(subPartContent)+1 <= r.MaxCharsPerPost {
							currentPost.WriteString(" ")
							currentPost.WriteString(subPart)
						} else if currentPost.Len()+len(subPartContent) <= r.MaxCharsPerPost {
							currentPost.WriteString(subPart)
						} else if currentPost.Len() == 0 {
							// This is an indivisible segment that doesn't fit
							return result, &IndivisibleSegmentError{
								Segment:       subPart,
								MaxChars:      r.MaxCharsPerPost,
								ActualLength:  len(subPart),
								Deterministic: true,
							}
						} else {
							// Save current and start new
							result = append(result, currentPost.String()+claimRefSuffix)
							result = append(result, subPartContent)
							currentPost.Reset()
						}
						splitFound = true
						break
					}

					if !splitFound {
						return result, &IndivisibleSegmentError{
							Segment:       sentence,
							MaxChars:      r.MaxCharsPerPost,
							ActualLength:  len(sentence),
							Deterministic: true,
						}
					}
				} else {
					// Sentence fits
					if currentPost.Len() > 0 && currentPost.Len()+len(sentenceContent)+1 <= r.MaxCharsPerPost {
						currentPost.WriteString(" ")
						currentPost.WriteString(sentence)
					} else if currentPost.Len() == 0 {
						currentPost.WriteString(sentence)
					} else {
						// Current post is full, save it and start new one
						result = append(result, currentPost.String()+claimRefSuffix)
						result = append(result, sentenceContent)
						currentPost.Reset()
					}
				}
				splitCompleted = true
			}

			if !splitCompleted {
				// No sentences found, return error
				return result, &IndivisibleSegmentError{
					Segment:       para,
					MaxChars:      r.MaxCharsPerPost,
					ActualLength:  len(para),
					Deterministic: true,
				}
			}
		}
	}

	// Don't forget the last post
	if currentPost.Len() > 0 {
		result = append(result, currentPost.String()+claimRefSuffix)
	}

	return result, nil
}

// formatClaimReferences formats claim IDs for inclusion in posts.
func (r *ThreadRenderer) formatClaimReferences(claimIDs []string) string {
	if len(claimIDs) == 0 {
		return ""
	}

	// Sort claim IDs for deterministic output
	sorted := sortStrings(claimIDs)
	return fmt.Sprintf(r.ClaimRefFormat, strings.Join(sorted, ","))
}

// cleanWhitespace normalizes whitespace in text.
func cleanWhitespace(s string) string {
	// Replace multiple whitespace with single space
	var builder strings.Builder
	inSpace := false
	for _, ch := range s {
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' {
			if !inSpace {
				builder.WriteRune(' ')
				inSpace = true
			}
		} else {
			builder.WriteRune(ch)
			inSpace = false
		}
	}
	result := strings.TrimSpace(builder.String())
	return result
}

// splitByParagraphs splits text by blank lines.
func splitByParagraphs(text string) []string {
	var paragraphs []string
	var current strings.Builder

	lines := strings.Split(text, "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			if current.Len() > 0 {
				paragraphs = append(paragraphs, strings.TrimSpace(current.String()))
				current.Reset()
			}
		} else {
			if current.Len() > 0 {
				current.WriteString(" ")
			}
			current.WriteString(strings.TrimSpace(line))
		}
	}

	if current.Len() > 0 {
		paragraphs = append(paragraphs, strings.TrimSpace(current.String()))
	}

	return paragraphs
}

// splitBySentences splits text by sentence boundaries (. ! ?).
func splitBySentences(text string) []string {
	var sentences []string
	var current strings.Builder
	inQuotes := false

	for i := 0; i < len(text); i++ {
		ch := text[i]

		if ch == '"' || ch == '\'' || ch == '«' || ch == '»' {
			inQuotes = !inQuotes
			current.WriteByte(ch)
			continue
		}

		if ch == '.' || ch == '!' || ch == '?' {
			// Don't split on ...
			if i+2 < len(text) && text[i+1] == '.' && text[i+2] == '.' {
				current.WriteByte(ch)
				i += 2
				continue
			}

			// Check if it's an abbreviation (e.g., Dr., Mr., etc.)
			isAbbreviation := isAbbreviation(text, i)
			if !isAbbreviation {
				sentences = append(sentences, strings.TrimSpace(current.String()))
				current.Reset()
				// Skip spaces after the sentence
				for i+1 < len(text) && (text[i+1] == ' ' || text[i+1] == '\n') {
					i++
				}
				continue
			}
		}

		current.WriteByte(ch)
	}

	if current.Len() > 0 {
		sentences = append(sentences, strings.TrimSpace(current.String()))
	}

	return sentences
}

// isAbbreviation checks if the text at position is likely an abbreviation.
func isAbbreviation(text string, pos int) bool {
	// Common abbreviations
	abbreviations := []string{
		"Dr.", "Mr.", "Mrs.", "Ms.", "Prof.", "Sr.", "Jr.", "vs.", "etc.",
		"e.g.", "i.e.", "vs", "al.", "et al.", "cf.", "Fig.", "fig.",
		"Vol.", "vol.", "pp.", "p.", "No.", "no.", "Inc.", "Ltd.",
		"Co.", "Corp.", "St.", "ave.", "Ave.", "Blvd.", "rd.", "Rd.",
		"Jan.", "Feb.", "Mar.", "Apr.", "Jun.", "Jul.", "Aug.", "Sep.",
		"Sept.", "Oct.", "Nov.", "Dec.",
	}

	// Check backwards from position to find abbreviation
	for _, abbrev := range abbreviations {
		if strings.HasSuffix(text[:pos+1], abbrev) {
			return true
		}
	}

	return false
}

// splitByBoundaries splits text by natural boundaries (comma, semicolon, hyphen, etc.).
func splitByBoundaries(text string) []string {
	// These are split points to try when a sentence is still too long
	splitPoints := []rune{',', ';', ':', '-', '—', '–', '(', ')'}
	var result []string
	var current strings.Builder
	lastSplit := 0

	for i, ch := range text {
		for _, sp := range splitPoints {
			if ch == sp {
				part := strings.TrimSpace(text[lastSplit:i])
				if part != "" {
					result = append(result, part)
				}
				lastSplit = i + 1
				current.Reset()
			}
		}
	}

	// Add remaining text
	remaining := strings.TrimSpace(text[lastSplit:])
	if remaining != "" {
		result = append(result, remaining)
	}

	return result
}

// IndivisibleSegmentError indicates a segment that cannot be split to fit within the limit.
type IndivisibleSegmentError struct {
	Segment       string
	MaxChars      int
	ActualLength  int
	Deterministic bool
}

func (e *IndivisibleSegmentError) Error() string {
	return fmt.Sprintf("segment of %d characters cannot fit within %d characters", e.ActualLength, e.MaxChars)
}

func (e *IndivisibleSegmentError) Unwrap() error {
	return errors.New(e.Error())
}

// IsIndivisibleSegmentError reports whether e is an IndivisibleSegmentError.
func IsIndivisibleSegmentError(err error) bool {
	var ie *IndivisibleSegmentError
	return errors.As(err, &ie)
}

func isIndivisibleSegmentError(err error) bool {
	return IsIndivisibleSegmentError(err)
}

// uniqueAndSortStrings returns a sorted slice with duplicates removed.
func uniqueAndSortStrings(input []string) []string {
	seen := make(map[string]bool)
	var result []string

	for _, s := range input {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}

	return sortStrings(result)
}

// sortStrings returns a sorted copy of the input slice.
func sortStrings(input []string) []string {
	result := make([]string, len(input))
	copy(result, input)
	for i := 0; i < len(result); i++ {
		for j := i + 1; j < len(result); j++ {
			if result[j] < result[i] {
				result[i], result[j] = result[j], result[i]
			}
		}
	}
	return result
}

// ThreadRenderer represents a deprecated alias for ThreadRenderer.
// Kept for backwards compatibility - use ThreadRenderer instead.
type ThreadRendererV1 = ThreadRenderer

// RenderThread is a convenience function for rendering threads.
func RenderThread(renderer *ThreadRenderer, artifact *contracts.EditorialArtifact) ([]string, error) {
	return renderer.Render(artifact)
}
