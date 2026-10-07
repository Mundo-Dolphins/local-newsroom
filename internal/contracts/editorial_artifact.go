package contracts

import (
	"errors"
	"fmt"
	"time"
)

// EditorialArtifact represents the output of the Writer stage.
//
// The Writer takes a VerificationResult and produces an EditorialArtifact that:
// - Preserves all claim references from verified claims
// - Organizes content into structured sections or posts
// - Tracks provenance from ResearchDossier → VerificationResult → EditorialArtifact
// - Remains format-neutral (does not encode Bluesky thread structure or Hugo front matter)
//
// The artifact can represent:
//   - Long-form articles (single body with sections)
//   - Multi-post threads (ordered list of posts)
//   - Hybrid formats (combined body and posts)
//
// All field values are explicitly traceable to verified claim IDs. Unverified
// claims must not be used in the artifact body.
type EditorialArtifact struct {
	// StableID uniquely identifies this artifact.
	// Should be deterministic given the verification result and writer parameters.
	StableID string `json:"stable_id"`

	// ArtifactType indicates the format of this artifact.
	ArtifactType ArtifactType `json:"artifact_type"`

	// Title is the headline/title when applicable.
	Title string `json:"title,omitempty"`

	// Subtitle is an optional subtitle or lead-in.
	Subtitle string `json:"subtitle,omitempty"`

	// Body contains the main article content as plain text.
	// Use sections for long-form articles.
	Body string `json:"body,omitempty"`

	// Sections defines the ordered section structure for long-form content.
	// Relevant for Article and ArticleWithThreads types.
	Sections []Section `json:"sections,omitempty"`

	// Posts defines the ordered posts for thread content.
	// Relevant for Thread and Hybrid types.
	Posts []Post `json:"posts,omitempty"`

	// ClaimReferences maps claim IDs to their usage in the artifact.
	// Key: claim.ID (from VerificationResult.ClaimDetails)
	// Value: ClaimUsage record
	ClaimReferences map[string]ClaimUsage `json:"claim_references"`

	// SourceReferences maps source IDs to their usage in the artifact.
	// Key: source.stable_id (from VerificationResult.ClaimDetails.SupportingEvidence)
	// Value: SourceUsage record
	SourceReferences map[string]SourceUsage `json:"source_references,omitempty"`

	// GenerationMetadata contains metadata about artifact generation.
	GenerationMetadata GenerationMetadata `json:"generation_metadata"`

	// Warnings captures limitations and cautions about the artifact.
	Warnings []Warning `json:"warnings,omitempty"`
}

// ArtifactType indicates the format of the editorial artifact.
//
// These types support long-form articles, social threads, and hybrid formats:
//   - Article: Single long-form body with sections
//   - Thread: Ordered list of short-form posts
//   - Hybrid: Both body and posts (e.g., summary article + tweet thread)
type ArtifactType string

const (
	// ArtifactTypeArticle represents a long-form article with sections.
	// Use Body and Sections fields.
	ArtifactTypeArticle ArtifactType = "article"

	// ArtifactTypeThread represents a multi-post thread (e.g., Twitter/X or Bluesky).
	// Use Posts field.
	ArtifactTypeThread ArtifactType = "thread"

	// ArtifactTypeHybrid represents both body content and a thread.
	// Use Body, Sections, and Posts fields (e.g., article summary + tweet thread).
	ArtifactTypeHybrid ArtifactType = "hybrid"
)

// Section represents a named section within an article.
type Section struct {
	// Order indicates the display order (0-indexed).
	Order int `json:"order"`

	// Title is the section heading.
	Title string `json:"title"`

	// Body contains the section content.
	Body string `json:"body"`

	// ClaimIDs lists the claim IDs referenced in this section.
	ClaimIDs []string `json:"claim_ids,omitempty"`
}

// Post represents a single post in a thread.
type Post struct {
	// Order indicates the display order (0-indexed).
	Order int `json:"order"`

	// Body contains the post content.
	Body string `json:"body"`

	// ClaimIDs lists the claim IDs referenced in this post.
	ClaimIDs []string `json:"claim_ids,omitempty"`

	// MediaReferences (optional) lists media elements referenced in the post.
	MediaReferences []MediaReference `json:"media_references,omitempty"`
}

// MediaReference links to media content in a post.
type MediaReference struct {
	// MediaType indicates the type of media.
	MediaType MediaType `json:"media_type"`

	// SourceID is the source/stable ID if the media originated from a source.
	SourceID string `json:"source_id,omitempty"`

	// Caption is optional alt text or caption.
	Caption string `json:"caption,omitempty"`
}

// MediaType indicates the type of media.
type MediaType string

const (
	// MediaTypeImage indicates an image.
	MediaTypeImage MediaType = "image"

	// MediaTypeVideo indicates a video.
	MediaTypeVideo MediaType = "video"

	// MediaTypeChart indicates a chart or data visualization.
	MediaTypeChart MediaType = "chart"

	// MediaTypeDocument indicates a linked document.
	MediaTypeDocument MediaType = "document"
)

// ClaimUsage documents how a verified claim is used in the artifact.
type ClaimUsage struct {
	// ClaimID is the stable ID of the verified claim.
	ClaimID string `json:"claim_id"`

	// UsageType indicates how the claim is used.
	UsageType ClaimUsageType `json:"usage_type"`

	// UsageContext describes the specific context of usage.
	UsageContext string `json:"usage_context,omitempty"`

	// Locations lists where the claim appears (section/post indices).
	Locations []ClaimLocation `json:"locations,omitempty"`

	// Paraphrased indicates whether the claim statement was paraphrased
	// rather than quoted verbatim.
	Paraphrased bool `json:"paraphrased"`
}

// ClaimUsageType indicates how a verified claim is utilized.
type ClaimUsageType string

const (
	// ClaimUsageCore indicates the claim is a central point of the artifact.
	ClaimUsageCore ClaimUsageType = "core"

	// ClaimUsageSupporting indicates the claim supports another point.
	ClaimUsageSupporting ClaimUsageType = "supporting"

	// ClaimUsageBackground indicates the claim provides context or background.
	ClaimUsageBackground ClaimUsageType = "background"

	// ClaimUsageCounterpoint indicates the claim presents a counterpoint or alternative view.
	ClaimUsageCounterpoint ClaimUsageType = "counterpoint"
)

// ClaimLocation indicates where a claim is used in the artifact.
type ClaimLocation struct {
	// LocationType indicates the type of location.
	LocationType LocationType `json:"location_type"`

	// Index is the 0-indexed position within its category.
	Index int `json:"index"`

	// SectionName is the section name when LocationType is Section.
	SectionName string `json:"section_name,omitempty"`

	// PostIndex is the post index when LocationType is Post.
	PostIndex int `json:"post_index,omitempty"`
}

// LocationType indicates the type of location for a claim.
type LocationType string

const (
	// LocationTypeBody indicates a location in the main body.
	LocationTypeBody LocationType = "body"

	// LocationTypeSection indicates a location in a section.
	LocationTypeSection LocationType = "section"

	// LocationTypePost indicates a location in a thread post.
	LocationTypePost LocationType = "post"
)

// SourceUsage documents how a source is used in the artifact.
type SourceUsage struct {
	// SourceID is the stable ID of the source.
	SourceID string `json:"source_id"`

	// UsageCount is the number of times this source is referenced.
	UsageCount int `json:"usage_count"`

	// CitationStyle indicates how the source should be cited.
	CitationStyle CitationStyle `json:"citation_style,omitempty"`

	// CitationText is the formatted citation text.
	CitationText string `json:"citation_text,omitempty"`
}

// CitationStyle indicates how a source should be cited.
type CitationStyle string

const (
	// CitationStyleInline indicates inline citation (e.g., "Smith, 2024").
	CitationStyleInline CitationStyle = "inline"

	// CitationStyleFootnote indicates footnote citation.
	CitationStyleFootnote CitationStyle = "footnote"

	// CitationStyleLink indicates hyperlinked citation.
	CitationStyleLink CitationStyle = "link"
)

// GenerationMetadata contains metadata about how the artifact was generated.
type GenerationMetadata struct {
	// InputVerificationID references the VerificationResult.stable_id.
	InputVerificationID string `json:"input_verification_id"`

	// GeneratedAt is when this artifact was generated (UTC).
	GeneratedAt time.Time `json:"generated_at"`

	// WriterParameters captures parameters used by the writer.
	WriterParameters map[string]string `json:"writer_parameters,omitempty"`

	// ModelID is the model ID used for generation, if applicable.
	ModelID string `json:"model_id,omitempty"`

	// TokenUsage captures token counts if available.
	TokenUsage *TokenUsage `json:"token_usage,omitempty"`
}

// TokenUsage captures token consumption during generation.
type TokenUsage struct {
	// PromptTokens is the number of tokens in the prompt.
	PromptTokens int `json:"prompt_tokens"`

	// CompletionTokens is the number of tokens in the completion.
	CompletionTokens int `json:"completion_tokens"`

	// TotalTokens is the total token count.
	TotalTokens int `json:"total_tokens"`
}

// Warning captures a limitation or caution about the artifact.
type Warning struct {
	// WarningType categorizes the warning.
	WarningType WarningType `json:"warning_type"`

	// Severity indicates the warning severity.
	Severity WarningSeverity `json:"severity"`

	// Message is the warning message.
	Message string `json:"message"`

	// RelatedClaimIDs are claim IDs related to this warning.
	RelatedClaimIDs []string `json:"related_claim_ids,omitempty"`

	// SuggestedAction (optional) suggests how to address the warning.
	SuggestedAction string `json:"suggested_action,omitempty"`
}

// WarningType categorizes artifact warnings.
type WarningType string

const (
	// WarningTypeUnverifiedClaim indicates a claim lacks verification.
	WarningTypeUnverifiedClaim WarningType = "unverified_claim"

	// WarningTypeConflictingClaims indicates conflicting claims are present.
	WarningTypeConflictingClaims WarningType = "conflicting_claims"

	// WarningTypeLowConfidence indicates low-confidence claims are featured.
	WarningTypeLowConfidence WarningType = "low_confidence"

	// WarningTypeInsufficientEvidence indicates insufficient supporting evidence.
	WarningTypeInsufficientEvidence WarningType = "insufficient_evidence"

	// WarningTypeOutOfRange indicates content exceeds format limits.
	WarningTypeOutOfRange WarningType = "out_of_range"

	// WarningTypeMissingReferences indicates missing source references.
	WarningTypeMissingReferences WarningType = "missing_references"

	// WarningTypeUncertainFacts indicates uncertain facts are presented as certain.
	WarningTypeUncertainFacts WarningType = "uncertain_facts"
)

// WarningSeverity indicates the severity of a warning.
type WarningSeverity string

const (
	// WarningSeverityLow is a minor concern that doesn't block publication.
	WarningSeverityLow WarningSeverity = "low"

	// WarningSeverityMedium is a moderate concern that should be addressed.
	WarningSeverityMedium WarningSeverity = "medium"

	// WarningSeverityHigh is a significant concern that requires resolution.
	WarningSeverityHigh WarningSeverity = "high"

	// WarningSeverityCritical blocks publication until resolved.
	WarningSeverityCritical WarningSeverity = "critical"
)

// Validate performs structural validation on EditorialArtifact.
func (a *EditorialArtifact) Validate() error {
	var errs []error

	// Validate artifact type consistency
	switch a.ArtifactType {
	case ArtifactTypeArticle:
		if a.Body == "" && len(a.Sections) == 0 {
			errs = append(errs, errors.New("article artifact must have body or sections"))
		}
		if len(a.Posts) > 0 {
			errs = append(errs, errors.New("article artifact should not have posts"))
		}
	case ArtifactTypeThread:
		if len(a.Posts) == 0 {
			errs = append(errs, errors.New("thread artifact must have at least one post"))
		}
	case ArtifactTypeHybrid:
		if a.Body == "" && len(a.Sections) == 0 {
			errs = append(errs, errors.New("hybrid artifact must have body or sections"))
		}
		if len(a.Posts) == 0 {
			errs = append(errs, errors.New("hybrid artifact must have at least one post"))
		}
	default:
		errs = append(errs, fmt.Errorf("unknown artifact type: %q", a.ArtifactType))
	}

	// Validate claim references
	claimRefIDs := make(map[string]bool)
	for claimID := range a.ClaimReferences {
		if claimRefIDs[claimID] {
			errs = append(errs, fmt.Errorf("duplicate claim reference ID: %q", claimID))
		}
		claimRefIDs[claimID] = true
	}

	// Validate sections reference valid claim IDs
	for i, section := range a.Sections {
		for _, claimID := range section.ClaimIDs {
			if !claimRefIDs[claimID] {
				errs = append(errs, fmt.Errorf("section %d references unknown claim %q", i, claimID))
			}
		}
	}

	// Validate posts reference valid claim IDs
	for i, post := range a.Posts {
		for _, claimID := range post.ClaimIDs {
			if !claimRefIDs[claimID] {
				errs = append(errs, fmt.Errorf("post %d references unknown claim %q", i, claimID))
			}
		}
	}

	// Validate sources reference valid sources
	sourceRefIDs := make(map[string]bool)
	for sourceID := range a.SourceReferences {
		if sourceRefIDs[sourceID] {
			errs = append(errs, fmt.Errorf("duplicate source reference ID: %q", sourceID))
		}
		sourceRefIDs[sourceID] = true
	}

	// Validate warnings
	warningTypes := make(map[WarningType]bool)
	for _, warning := range a.Warnings {
		if warningTypes[warning.WarningType] {
			errs = append(errs, fmt.Errorf("duplicate warning type: %q", warning.WarningType))
		}
		warningTypes[warning.WarningType] = true
	}

	// Validate token usage if present
	if a.GenerationMetadata.TokenUsage != nil {
		if a.GenerationMetadata.TokenUsage.TotalTokens < a.GenerationMetadata.TokenUsage.PromptTokens {
			errs = append(errs, errors.New("total_tokens must be >= prompt_tokens"))
		}
		if a.GenerationMetadata.TokenUsage.TotalTokens < a.GenerationMetadata.TokenUsage.CompletionTokens {
			errs = append(errs, errors.New("total_tokens must be >= completion_tokens"))
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	return nil
}

// ExtractClaimIDs returns all claim IDs referenced in this artifact.
func (a *EditorialArtifact) ExtractClaimIDs() []string {
	ids := make([]string, 0, len(a.ClaimReferences))
	for claimID := range a.ClaimReferences {
		ids = append(ids, claimID)
	}
	return ids
}

// GetCoreClaims returns claim IDs marked as core usage.
func (a *EditorialArtifact) GetCoreClaims() []string {
	var result []string
	for claimID, usage := range a.ClaimReferences {
		if usage.UsageType == ClaimUsageCore {
			result = append(result, claimID)
		}
	}
	return result
}

// HasUnverifiedClaims returns true if the artifact claims to use unverified claims.
func (a *EditorialArtifact) HasUnverifiedClaims() bool {
	for _, usage := range a.ClaimReferences {
		if usage.UsageType == ClaimUsageCore && usage.Paraphrased && usage.UsageContext == "" {
			return true
		}
	}
	return false
}

// GetWarningsBySeverity returns warnings filtered by severity.
func (a *EditorialArtifact) GetWarningsBySeverity(severity WarningSeverity) []Warning {
	var result []Warning
	for _, w := range a.Warnings {
		if w.Severity == severity {
			result = append(result, w)
		}
	}
	return result
}

// HasCriticalWarnings returns true if there are any critical-severity warnings.
func (a *EditorialArtifact) HasCriticalWarnings() bool {
	for _, w := range a.Warnings {
		if w.Severity == WarningSeverityCritical {
			return true
		}
	}
	return false
}

// ValidateSectionOrder ensures sections are in ascending order.
func (a *EditorialArtifact) ValidateSectionOrder() error {
	lastOrder := -1
	for i, section := range a.Sections {
		if section.Order <= lastOrder {
			return fmt.Errorf("sections are not in ascending order: section %d has order %d (expected > %d)",
				i, section.Order, lastOrder)
		}
		lastOrder = section.Order
	}
	return nil
}

// ValidatePostOrder ensures posts are in ascending order.
func (a *EditorialArtifact) ValidatePostOrder() error {
	lastOrder := -1
	for i, post := range a.Posts {
		if post.Order <= lastOrder {
			return fmt.Errorf("posts are not in ascending order: post %d has order %d (expected > %d)",
				i, post.Order, lastOrder)
		}
		lastOrder = post.Order
	}
	return nil
}

// SectionNames returns a slice of all section names.
func (a *EditorialArtifact) SectionNames() []string {
	names := make([]string, len(a.Sections))
	for i, s := range a.Sections {
		names[i] = s.Title
	}
	return names
}

// PostContents returns a slice of all post bodies.
func (a *EditorialArtifact) PostContents() []string {
	contents := make([]string, len(a.Posts))
	for i, p := range a.Posts {
		contents[i] = p.Body
	}
	return contents
}

// HasSections returns true if the artifact has sections defined.
func (a *EditorialArtifact) HasSections() bool {
	return len(a.Sections) > 0
}

// HasPosts returns true if the artifact has posts defined.
func (a *EditorialArtifact) HasPosts() bool {
	return len(a.Posts) > 0
}

// GetClaimReference returns the claim usage for a specific claim ID.
func (a *EditorialArtifact) GetClaimReference(claimID string) (ClaimUsage, bool) {
	usage, exists := a.ClaimReferences[claimID]
	return usage, exists
}

// GetAllClaimIDs returns all unique claim IDs from sections and posts.
func (a *EditorialArtifact) GetAllClaimIDs() []string {
	seen := make(map[string]bool)
	ids := make([]string, 0)

	for _, section := range a.Sections {
		for _, claimID := range section.ClaimIDs {
			if !seen[claimID] {
				seen[claimID] = true
				ids = append(ids, claimID)
			}
		}
	}

	for _, post := range a.Posts {
		for _, claimID := range post.ClaimIDs {
			if !seen[claimID] {
				seen[claimID] = true
				ids = append(ids, claimID)
			}
		}
	}

	return ids
}

// MergeClaimsForPost merges claim IDs from a list of sections/posts into a single set.
func MergeClaimIDs(sections []Section, posts []Post) []string {
	seen := make(map[string]bool)
	ids := make([]string, 0)

	for _, s := range sections {
		for _, claimID := range s.ClaimIDs {
			if !seen[claimID] {
				seen[claimID] = true
				ids = append(ids, claimID)
			}
		}
	}

	for _, p := range posts {
		for _, claimID := range p.ClaimIDs {
			if !seen[claimID] {
				seen[claimID] = true
				ids = append(ids, claimID)
			}
		}
	}

	return ids
}

// ValidateClaimReferences asserts that all claim references exist in ClaimIDs arrays.
// This is a consistency check between ClaimReferences and actual ClaimIDs in sections/posts.
func (a *EditorialArtifact) ValidateClaimReferences() error {
	allClaimIDs := a.GetAllClaimIDs()
	seen := make(map[string]bool)

	for _, id := range allClaimIDs {
		seen[id] = true
	}

	for claimID := range a.ClaimReferences {
		if !seen[claimID] {
			return fmt.Errorf("claim_reference %q exists but claim ID not found in sections or posts", claimID)
		}
	}

	// No validation needed here - claims in sections/posts but not in ClaimReferences
	// are allowed (warnings should be used instead)

	return nil
}
