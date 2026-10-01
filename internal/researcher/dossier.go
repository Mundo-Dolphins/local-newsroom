// Package researcher defines the ResearchDossier contract for the output of the
// Researcher component.
//
// The ResearchDossier is a factual, traceable, and reusable output that:
// - Preserves source provenance for verification
// - Links claims to evidence via stable IDs
// - Explicitly represents uncertainty and contradictions
// - Separates publishable facts from internal research notes
//
// This contract is independent from Mundo Dolphins' writing style and is
// designed to be consumed by the Verifier and Writer stages.
package researcher

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
)

// ResearchDossier is the output contract of the Researcher component.
//
// It contains factual findings with full provenance, evidence links,
// contradictions, and research notes. The dossier is:
// - Factual: Contains only claims that have been researched
// - Traceable: Every claim links to specific source excerpts
// - Reusable: Stable IDs enable Verifier and Writer to reference specific claims
// - Style-independent: No formatting or Mundo Dolphins writing style
//
// The researcher must ensure:
// - All claims have evidence unless is_unsupported is explicitly true
// - Sources preserve full provenance (URL, type, timestamp, fetch status)
// - Contradictions are documented when conflicting information is found
// - Uncertainty is captured via confidence levels and notes
type ResearchDossier struct {
	// StableID is a deterministic unique identifier for this dossier.
	// Should be generated based on topic and generation parameters.
	StableID string `json:"stable_id"`

	// Topic is the subject of the research.
	Topic string `json:"topic"`

	// GeneratedAt is the UTC timestamp when this dossier was generated.
	GeneratedAt time.Time `json:"generated_at"`

	// Sources contains all sources consulted during research with full provenance.
	Sources []SourceReference `json:"sources"`

	// Claims contains factual claims derived from research.
	// Each claim has a stable ID for reference by other stages.
	Claims []Claim `json:"claims"`

	// Contradictions documents conflicts between claims or sources.
	Contradictions []Contradiction `json:"contradictions,omitempty"`

	// UnresolvedQuestions captures questions identified but not answered.
	UnresolvedQuestions []UnresolvedQuestion `json:"unresolved_questions,omitempty"`

	// ResearchNotes contains non-publishable internal notes and observations.
	ResearchNotes []ResearchNote `json:"research_notes,omitempty"`
}

// Validate performs structural validation on the ResearchDossier.
// It checks that:
// - All claims with empty evidence have is_unsupported = true
// - All IDs are unique within their respective arrays
// - Evidence source_ids reference actual sources in the dossier
// - Contradiction claim_ids reference actual claims
// - Claim source_contradicted references are valid claim IDs
func (d *ResearchDossier) Validate() error {
	var errs []error

	// Validate claim IDs are unique
	claimIDs := make(map[string]bool)
	for _, c := range d.Claims {
		if claimIDs[c.ID] {
			errs = append(errs, fmt.Errorf("duplicate claim ID: %s", c.ID))
		}
		claimIDs[c.ID] = true
	}

	// Validate source IDs are unique
	sourceIDs := make(map[string]bool)
	for _, s := range d.Sources {
		if sourceIDs[s.StableID] {
			errs = append(errs, fmt.Errorf("duplicate source ID: %s", s.StableID))
		}
		sourceIDs[s.StableID] = true
	}

	// Validate contradictions IDs are unique
	contradictionIDs := make(map[string]bool)
	for _, con := range d.Contradictions {
		if contradictionIDs[con.ID] {
			errs = append(errs, fmt.Errorf("duplicate contradiction ID: %s", con.ID))
		}
		contradictionIDs[con.ID] = true
	}

	// Validate unresolved question IDs are unique
	questionIDs := make(map[string]bool)
	for _, q := range d.UnresolvedQuestions {
		if questionIDs[q.ID] {
			errs = append(errs, fmt.Errorf("duplicate unresolved question ID: %s", q.ID))
		}
		questionIDs[q.ID] = true
	}

	// Validate research note IDs are unique
	notesIDs := make(map[string]bool)
	for _, n := range d.ResearchNotes {
		if notesIDs[n.ID] {
			errs = append(errs, fmt.Errorf("duplicate research note ID: %s", n.ID))
		}
		notesIDs[n.ID] = true
	}

	// Validate each claim
	for _, c := range d.Claims {
		// Claims without evidence must be explicitly unsupported
		if len(c.Evidence) == 0 && !c.IsUnsupported {
			errs = append(errs, fmt.Errorf("claim %q has no evidence and is not marked as unsupported", c.ID))
		}

		// If unsupported is true but evidence exists, that's unusual but not invalid
		if c.IsUnsupported && len(c.Evidence) > 0 {
			// This is allowed - a claim can be marked unsupported but still have weak/contradictory evidence
		}

		// Validate evidence source references
		for _, e := range c.Evidence {
			if !sourceIDs[e.SourceID] {
				errs = append(errs, fmt.Errorf("claim %q references unknown source %q in evidence", c.ID, e.SourceID))
			}
		}

		// Validate claim IDs referenced in sources_contradicted
		for _, contradictedID := range c.SourcesContradicted {
			if !claimIDs[contradictedID] {
				errs = append(errs, fmt.Errorf("claim %q references unknown claim %q in sources_contradicted", c.ID, contradictedID))
			}
		}

		// Validate confidence is set if uncertainty_notes is present
		if c.UncertaintyNotes != "" && c.Confidence == "" {
			errs = append(errs, fmt.Errorf("claim %q has uncertainty_notes but no confidence level", c.ID))
		}
	}

	// Validate contradictions reference valid claims
	for _, con := range d.Contradictions {
		for _, claimID := range con.ClaimIDs {
			if !claimIDs[claimID] {
				errs = append(errs, fmt.Errorf("contradiction %q references unknown claim %q", con.ID, claimID))
			}
		}
	}

	// Validate unresolved questions reference valid claims
	for _, q := range d.UnresolvedQuestions {
		for _, claimID := range q.RelatedClaimIDs {
			if !claimIDs[claimID] {
				errs = append(errs, fmt.Errorf("unresolved question %q references unknown claim %q", q.ID, claimID))
			}
		}
	}

	// Validate research notes reference valid claims and sources
	for _, n := range d.ResearchNotes {
		for _, claimID := range n.RelatedClaimIDs {
			if !claimIDs[claimID] {
				errs = append(errs, fmt.Errorf("research note %q references unknown claim %q", n.ID, claimID))
			}
		}
		for _, sourceID := range n.RelatedSourceIDs {
			if !sourceIDs[sourceID] {
				errs = append(errs, fmt.Errorf("research note %q references unknown source %q", n.ID, sourceID))
			}
		}
	}

	// Validate that contradictions reference at least 2 claims
	for _, con := range d.Contradictions {
		if len(con.ClaimIDs) < 2 {
			errs = append(errs, fmt.Errorf("contradiction %q must reference at least 2 claims", con.ID))
		}
	}

	// Validate no circular contradictions (simple check: no claim contradicts itself)
	for _, c := range d.Claims {
		if slices.Contains(c.SourcesContradicted, c.ID) {
			errs = append(errs, fmt.Errorf("claim %q cannot contradict itself", c.ID))
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	return nil
}

// SourceReference represents a source consulted during research, preserving full provenance.
type SourceReference struct {
	// StableID matches the Source.stable_id from the fetcher.
	StableID string `json:"stable_id"`

	// OriginalURL is the URL from which the source was retrieved.
	OriginalURL string `json:"original_url"`

	// SourceType indicates the type of content source.
	SourceType types.SourceType `json:"source_type"`

	// RetrievedAt is when this source was fetched (UTC).
	RetrievedAt time.Time `json:"retrieved_at"`

	// Title is the document title, if available.
	Title string `json:"title,omitempty"`

	// Author is the document author, if identified.
	Author string `json:"author,omitempty"`

	// PublishedAt is the document publication timestamp, if available.
	PublishedAt *time.Time `json:"published_at,omitempty"`

	// FetchStatus contains HTTP fetch status if available.
	FetchStatus *FetchStatus `json:"fetch_status,omitempty"`

	// Metadata contains additional metadata from the source.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// FromSource creates a SourceReference from a types.Source, copying all provenance
// information. This is the canonical way to populate the Sources array from
// the fetcher's output.
func (r *SourceReference) FromSource(s types.Source) {
	r.StableID = s.StableID
	r.OriginalURL = s.OriginalURL
	r.SourceType = s.SourceType
	r.RetrievedAt = s.RetrievedAt

	if s.FetchStatus != nil {
		httpStatus := s.FetchStatus.HTTPStatus
		contentType := s.FetchStatus.ContentType
		contentLength := s.FetchStatus.ContentLength
		fetchError := s.FetchStatus.FetchError

		r.FetchStatus = &FetchStatus{
			HTTPStatus:    httpStatus,
			ContentType:   contentType,
			ContentLength: contentLength,
			FetchError:    fetchError,
		}
	}

	if len(s.Metadata) > 0 {
		r.Metadata = make(map[string]string, len(s.Metadata))
		for k, v := range s.Metadata {
			r.Metadata[k] = v
		}
	}
}

// FetchStatus captures the result of an HTTP fetch, matching the pattern
// from types.FetchStatus for consistency.
type FetchStatus struct {
	// HTTPStatus is the HTTP status code returned, if available.
	HTTPStatus *int `json:"http_status,omitempty"`

	// ContentType is the Content-Type header value, if available.
	ContentType *string `json:"content_type,omitempty"`

	// ContentLength is the Content-Length header value in bytes, if available.
	ContentLength *int64 `json:"content_length,omitempty"`

	// FetchError is the error message if the fetch failed.
	FetchError *string `json:"fetch_error,omitempty"`
}

// Claim represents a factual statement derived from research.
//
// Claims are the core factual assertions in the research. Each claim has:
// - A stable ID for reference by Verifier and Writer stages
// - A clear, verifiable statement
// - Evidence linking it to specific source excerpts
// - Confidence level and uncertainty notes
// - Optional reference to contradicted claims
//
// Claims may be marked as unsupported if they are being tracked without
// supporting evidence (e.g., claims identified but not yet verified).
type Claim struct {
	// ID is a unique identifier for this claim (e.g., claim-001).
	// This ID must be stable across revisions for reference by other stages.
	ID string `json:"id"`

	// Statement is the factual statement being claimed.
	// Should be concise, verifiable, and independent of writing style.
	Statement string `json:"statement"`

	// Confidence indicates the researcher's confidence level in this claim.
	// High confidence means strong supporting evidence from reliable sources.
	Confidence ConfidenceLevel `json:"confidence,omitempty"`

	// UncertaintyNotes captures reasons for reduced confidence or limitations.
	UncertaintyNotes string `json:"uncertainty_notes,omitempty"`

	// Evidence links this claim to supporting source material.
	// Must be non-empty unless IsUnsupported is true.
	Evidence []Evidence `json:"evidence"`

	// IsUnsupported indicates this claim is being tracked without evidence.
	// This must be true if Evidence is empty.
	IsUnsupported bool `json:"is_unsupported"`

	// SourcesContradicted lists claim IDs that this claim contradicts.
	SourcesContradicted []string `json:"sources_contradicted,omitempty"`
}

// Evidence links a claim to supporting source material.
type Evidence struct {
	// SourceID is the stable ID of the source providing evidence.
	SourceID string `json:"source_id"`

	// Excerpts are direct quotes or paraphrases from the source.
	Excerpts []Excerpt `json:"excerpts"`

	// ClaimContext explains how this excerpt supports the claim.
	ClaimContext string `json:"claim_context,omitempty"`
}

// Excerpt is a direct excerpt from a source with location information.
type Excerpt struct {
	// Text is the exact excerpt text.
	Text string `json:"text"`

	// StartOffset is the start byte offset in the source's plain_text.
	StartOffset int `json:"start_offset"`

	// EndOffset is the end byte offset in the source's plain_text.
	EndOffset int `json:"end_offset"`

	// ContextBefore is optional context text before the excerpt.
	ContextBefore string `json:"context_before,omitempty"`

	// ContextAfter is optional context text after the excerpt.
	ContextAfter string `json:"context_after,omitempty"`
}

// ConfidenceLevel represents the researcher's confidence in a claim.
type ConfidenceLevel string

const (
	// ConfidenceHigh indicates strong supporting evidence from multiple reliable
	// sources with consistent information.
	ConfidenceHigh ConfidenceLevel = "high"

	// ConfidenceMedium indicates reasonable evidence but with some limitations
	// (single source, moderate reliability, some ambiguity).
	ConfidenceMedium ConfidenceLevel = "medium"

	// ConfidenceLow indicates weak evidence (single weak source, highly
	// ambiguous, or conflicting information that couldn't be resolved).
	ConfidenceLow ConfidenceLevel = "low"
)

// Contradiction documents a conflict between claims or sources.
type Contradiction struct {
	// ID is a unique identifier for this contradiction record.
	ID string `json:"id"`

	// ClaimIDs are the claim IDs involved in this contradiction.
	ClaimIDs []string `json:"claim_ids"`

	// Description explains why this is a contradiction.
	Description string `json:"description"`

	// ResolutionStatus indicates the current state of resolution.
	ResolutionStatus ResolutionStatus `json:"resolution_status,omitempty"`

	// ResolutionNotes capture any attempted or pending resolution.
	ResolutionNotes string `json:"resolution_notes,omitempty"`

	// Severity indicates priority for resolution.
	Severity Severity `json:"severity,omitempty"`
}

// ResolutionStatus indicates whether a contradiction has been resolved.
type ResolutionStatus string

const (
	// ResolutionUnresolved means the contradiction is still being investigated.
	ResolutionUnresolved ResolutionStatus = "unresolved"

	// ResolutionPartial means some aspects are resolved but others remain.
	ResolutionPartial ResolutionStatus = "partial"

	// ResolutionResolved means the contradiction has been explained or reconciled.
	ResolutionResolved ResolutionStatus = "resolved"
)

// Severity indicates how urgently a contradiction should be addressed.
type Severity string

const (
	// SeverityLow means the contradiction doesn't significantly impact findings.
	SeverityLow Severity = "low"

	// SeverityMedium means the contradiction should be addressed but isn't critical.
	SeverityMedium Severity = "medium"

	// SeverityHigh means the contradiction significantly affects findings quality.
	SeverityHigh Severity = "high"

	// SeverityCritical means the contradiction must be resolved for reliable output.
	SeverityCritical Severity = "critical"
)

// UnresolvedQuestion captures a question identified during research but not answered.
type UnresolvedQuestion struct {
	// ID is a unique identifier for this question.
	ID string `json:"id"`

	// Question is what was asked but not answered.
	Question string `json:"question"`

	// WhyUnresolved explains why this question could not be answered.
	WhyUnresolved string `json:"why_unresolved"`

	// Priority indicates urgency for future investigation.
	Priority Severity `json:"priority,omitempty"`

	// RelatedClaimIDs are claims that depend on this question being answered.
	RelatedClaimIDs []string `json:"related_claim_ids,omitempty"`
}

// ResearchNote contains non-publishable internal notes and observations.
//
// Research notes are intentionally separate from claims because they may contain:
// - Methodological notes about how research was conducted
// - Observations that don't meet the threshold for a formal claim
// - TODO items for future research
// - Warnings about potential issues
// - Internal thinking that shouldn't appear in final output
type ResearchNote struct {
	// ID is a unique identifier for this note.
	ID string `json:"id"`

	// Content is the note content.
	Content string `json:"content"`

	// NoteType categorizes the type of note.
	NoteType NoteType `json:"note_type,omitempty"`

	// RelatedClaimIDs are claim IDs this note relates to.
	RelatedClaimIDs []string `json:"related_claim_ids,omitempty"`

	// RelatedSourceIDs are source IDs this note relates to.
	RelatedSourceIDs []string `json:"related_source_ids,omitempty"`

	// CreatedAt is when this note was created (UTC).
	CreatedAt time.Time `json:"created_at,omitempty"`
}

// NoteType categorizes research notes.
type NoteType string

const (
	// NoteMethodology notes about the research methodology or approach.
	NoteMethodology NoteType = "methodology"

	// NoteObservation factual observations that didn't become claims.
	NoteObservation NoteType = "observation"

	// NoteTODO items that need follow-up or further research.
	NoteTODO NoteType = "todo"

	// NoteInternal internal notes not for publication.
	NoteInternal NoteType = "internal"

	// NoteWarning warnings about potential issues or concerns.
	NoteWarning NoteType = "warning"
)

// ExtractClaimIDs returns all claim IDs from the dossier for indexing.
func (d *ResearchDossier) ExtractClaimIDs() []string {
	ids := make([]string, 0, len(d.Claims))
	for _, c := range d.Claims {
		ids = append(ids, c.ID)
	}
	return ids
}

// ExtractSourceIDs returns all source IDs from the dossier for indexing.
func (d *ResearchDossier) ExtractSourceIDs() []string {
	ids := make([]string, 0, len(d.Sources))
	for _, s := range d.Sources {
		ids = append(ids, s.StableID)
	}
	return ids
}

// SortClaimsByConfidence sorts claims by confidence level (high to low),
// with stable ordering for equal confidence using claim ID.
func (d *ResearchDossier) SortClaimsByConfidence() {
	confidenceOrder := map[ConfidenceLevel]int{
		ConfidenceHigh:   0,
		ConfidenceMedium: 1,
		ConfidenceLow:    2,
	}

	sort.SliceStable(d.Claims, func(i, j int) bool {
		conv, convKnown := confidenceOrder[d.Claims[i].Confidence]
		convj, convjKnown := confidenceOrder[d.Claims[j].Confidence]

		if convKnown && convjKnown {
			if conv != convj {
				return conv < convj
			}
		}

		// Stable sort by ID for equal confidence
		return d.Claims[i].ID < d.Claims[j].ID
	})
}

// FilterClaimsByConfidence returns claims that meet or exceed the given confidence threshold.
func (d *ResearchDossier) FilterClaimsByConfidence(threshold ConfidenceLevel) []Claim {
	var result []Claim
	for _, c := range d.Claims {
		cOrder, cKnown := confidenceOrderReverse[c.Confidence]
		tOrder, tKnown := confidenceOrderReverse[threshold]

		if cKnown && tKnown && cOrder >= tOrder {
			result = append(result, c)
		}
	}

	return result
}

// Reverse map for confidence filtering (defined after main map)
var confidenceOrderReverse = func() map[ConfidenceLevel]int {
	result := make(map[ConfidenceLevel]int)
	for k, v := range map[ConfidenceLevel]int{
		ConfidenceLow:    0,
		ConfidenceMedium: 1,
		ConfidenceHigh:   2,
	} {
		result[k] = v
	}
	return result
}()
