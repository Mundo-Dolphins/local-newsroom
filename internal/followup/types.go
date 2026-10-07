// Package followup implements a bounded verification-driven follow-up research loop.
//
// This package provides:
// - A bounded loop that performs targeted follow-up research for unresolved claims
// - Configurable limits on rounds, queries, and source budget
// - Dossier merging that preserves existing claim/source IDs
// - Audit trail tracking for each follow-up round
// - Failure semantics that preserve original verification state
//
// The loop is designed to be strictly bounded:
// - Default maximum follow-up rounds: 1 (configurable)
// - Configurable maximum queries per unresolved claim/topic
// - Configurable source budget
// - Context cancellation support
// - Stop conditions: no unresolved claims remain or limits exhausted
//
// Only claims with statuses uncertain, insufficient_evidence, or contradicted
// may trigger follow-up research. Supported claims never trigger searching.
//
// Merge rules:
// - Existing claim IDs are preserved where valid
// - New sources/evidence are added deterministically
// - Duplicate URLs/source content is avoided
// - Contradictory evidence is never silently overwritten
// - An audit trail of follow-up rounds is maintained
//
// Failure semantics:
// - Original verification state is preserved on failure
// - A warning is recorded
// - Claims are never pretentified as verified on failure
package followup

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/planner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/researcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
)

// FollowUpConfig configures the bounded follow-up research loop.
type FollowUpConfig struct {
	// MaxRounds is the maximum number of follow-up rounds allowed.
	// Default: 1 (a single additional verification cycle).
	MaxRounds int

	// MaxQueriesPerClaim is the maximum number of search queries to generate
	// per unresolved claim in each round.
	// Default: 3.
	MaxQueriesPerClaim int

	// MaxQueriesPerRound is the maximum total search queries across all
	// unresolved claims in a single round.
	// Default: 10.
	MaxQueriesPerRound int

	// MaxSourcesPerRound is the maximum number of new sources to fetch
	// and process per follow-up round.
	// Default: 20.
	MaxSourcesPerRound int

	// SourceBudget is the maximum total number of sources allowed
	// across all rounds combined.
	// Default: 100.
	SourceBudget int

	// MinSourceRelevance is the minimum relevance score (0-1) for search
	// results to be considered for fetching.
	// Default: 0.5.
	MinSourceRelevance float64

	// TimeNow is used to get the current time for generated_at timestamps.
	// For testing, this can be overridden to return a fixed time.
	TimeNow func() time.Time

	// RetryAttempts is the number of retry attempts for failed searches.
	// Default: 1 (no retry).
	RetryAttempts int
}

// FollowUpResult contains the results of a follow-up research round.
type FollowUpResult struct {
	// RoundNumber is the 1-indexed round number (1, 2, 3, ...).
	RoundNumber int

	// AddedClaims contains new claims discovered during this round.
	AddedClaims []researcher.Claim

	// AddedSources contains new sources discovered during this round.
	AddedSources []researcher.SourceReference

	// UnresolvedClaimsAfter lists claim IDs still unresolved after this round.
	UnresolvedClaimsAfter []string

	// ResolvedClaims lists claim IDs that were resolved during this round.
	ResolvedClaims []string

	// AddedClaimIDs lists the IDs of new claims added.
	AddedClaimIDs []string

	// AddedSourceIDs lists the stable IDs of new sources added.
	AddedSourceIDs []string

	// Warnings contains any warnings encountered during this round.
	Warnings []string

	// Errors contains any errors encountered during this round.
	Errors []string

	// QueryCount is the number of search queries executed in this round.
	QueryCount int

	// FetchedCount is the number of sources fetched in this round.
	FetchedCount int

	// ProcessingTime is the time taken to process this round.
	ProcessingTime time.Duration

	// RoundAudit captures audit information for this round.
	RoundAudit RoundAudit
}

// RoundAudit provides detailed audit information for a follow-up round.
type RoundAudit struct {
	// StartRound is when this round started.
	StartRound time.Time

	// EndRound is when this round completed.
	EndRound time.Time

	// QueriesGenerated lists the search queries generated.
	QueriesGenerated []SearchQuerySpec

	// SourcesFetched lists the URLs fetched.
	SourcesFetched []string

	// ClaimsAdded lists the new claim IDs added.
	ClaimsAdded []string

	// SourceIDsAdded lists the source IDs added.
	SourceIDsAdded []string
}

// SearchQuerySpec represents a single search query with its purpose.
type SearchQuerySpec struct {
	// Query is the search query text.
	Query string

	// ClaimID is the claim ID this query is investigating.
	ClaimID string

	// Purpose describes why this query was generated.
	Purpose string

	// TimeRange is an optional time range for the search.
	TimeRange *planner.TimeRangeHint
}

// FollowUpError represents a structured error in the follow-up process.
type FollowUpError struct {
	// Type is the error category.
	Type string

	// Message is a human-readable description.
	Message string

	// ClaimID is the claim ID involved, if applicable.
	ClaimID string

	// RoundNumber is the round number when the error occurred.
	RoundNumber int

	// Cause is the underlying error.
	Cause error
}

func (e *FollowUpError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s (round %d): %s: %v", e.Type, e.RoundNumber, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s (round %d): %s", e.Type, e.RoundNumber, e.Message)
}

func (e *FollowUpError) Unwrap() error {
	return e.Cause
}

// GetUnresolvedClaimIDs returns claim IDs that need follow-up based on
// their verification status.
//
// Only claims with statuses uncertain, insufficient_evidence, or contradicted
// are returned. Supported claims are never included.
func GetUnresolvedClaimIDs(result *contracts.VerificationResult) []string {
	if result == nil || len(result.VerificationStatuses) == 0 {
		return nil
	}

	var unresolved []string
	for claimID, status := range result.VerificationStatuses {
		if status == contracts.VerificationStatusUncertain ||
			status == contracts.VerificationStatusInsufficientEvidence ||
			status == contracts.VerificationStatusContradicted {
			unresolved = append(unresolved, claimID)
		}
	}

	// Sort for deterministic ordering
	sort.Strings(unresolved)
	return unresolved
}

// GetResolvedClaimIDs returns claim IDs that have been verified as supported.
func GetResolvedClaimIDs(result *contracts.VerificationResult) []string {
	if result == nil {
		return nil
	}

	var resolved []string
	for claimID, status := range result.VerificationStatuses {
		if status == contracts.VerificationStatusSupported {
			resolved = append(resolved, claimID)
		}
	}

	sort.Strings(resolved)
	return resolved
}

// IsClaimSupported checks if a specific claim has been verified as supported.
func IsClaimSupported(result *contracts.VerificationResult, claimID string) bool {
	if result == nil {
		return false
	}

	status, ok := result.VerificationStatuses[claimID]
	return ok && status == contracts.VerificationStatusSupported
}

// IsClaimUnresolved checks if a specific claim needs follow-up research.
func IsClaimUnresolved(result *contracts.VerificationResult, claimID string) bool {
	if result == nil {
		return false
	}

	status, ok := result.VerificationStatuses[claimID]
	return ok && (status == contracts.VerificationStatusUncertain ||
		status == contracts.VerificationStatusInsufficientEvidence ||
		status == contracts.VerificationStatusContradicted)
}

// GroupUnresolvedByClaim groups search queries by claim ID.
func GroupUnresolvedByClaim(unresolved []string, queries map[string][]planner.SearchQuery) map[string][]planner.SearchQuery {
	grouped := make(map[string][]planner.SearchQuery)
	for _, claimID := range unresolved {
		if queries, ok := queries[claimID]; ok {
			grouped[claimID] = queries
		}
	}
	return grouped
}

// DeduplicateSourceURLs removes duplicate source URLs, keeping the first occurrence.
func DeduplicateSourceURLs(sources []researcher.SourceReference) []researcher.SourceReference {
	seen := make(map[string]bool)
	var result []researcher.SourceReference

	for _, s := range sources {
		if !seen[s.StableID] && !seen[s.OriginalURL] {
			seen[s.StableID] = true
			seen[s.OriginalURL] = true
			result = append(result, s)
		}
	}

	return result
}

// MergeSourceReferences adds new sources to existing ones, avoiding duplicates.
func MergeSourceReferences(existing, newSources []researcher.SourceReference) []researcher.SourceReference {
	existingIDs := make(map[string]bool)
	for _, s := range existing {
		existingIDs[s.StableID] = true
		existingIDs[s.OriginalURL] = true
	}

	var merged []researcher.SourceReference
	merged = append(merged, existing...)

	for _, s := range newSources {
		if !existingIDs[s.StableID] && !existingIDs[s.OriginalURL] {
			merged = append(merged, s)
		}
	}

	return merged
}

// DeduplicateClaims removes duplicate claims by ID, keeping the first occurrence.
func DeduplicateClaims(claims []researcher.Claim) []researcher.Claim {
	seen := make(map[string]bool)
	var result []researcher.Claim

	for _, c := range claims {
		if !seen[c.ID] {
			seen[c.ID] = true
			result = append(result, c)
		}
	}

	return result
}

// MergeClaimEvidence adds evidence from new claims to existing claims.
func MergeClaimEvidence(existingClaims []researcher.Claim, newClaims []researcher.Claim) []researcher.Claim {
	existingMap := make(map[string]*researcher.Claim)
	var result []researcher.Claim

	for i := range existingClaims {
		c := existingClaims[i]
		existingMap[c.ID] = &c
		result = append(result, c)
	}

	for _, newClaim := range newClaims {
		if existing, ok := existingMap[newClaim.ID]; ok {
			for _, evidence := range newClaim.Evidence {
				exists := false
				for _, existingEvid := range existing.Evidence {
					if existingEvid.SourceID == evidence.SourceID &&
						len(existingEvid.Excerpts) > 0 &&
						len(evidence.Excerpts) > 0 &&
						existingEvid.Excerpts[0].Text == evidence.Excerpts[0].Text {
						exists = true
						break
					}
				}
				if !exists {
					existing.Evidence = append(existing.Evidence, evidence)
				}
			}
		} else {
			result = append(result, newClaim)
		}
	}

	return result
}

// SourceIDsEqual checks if two slices of source IDs are equal.
func SourceIDsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	aCopy := make([]string, len(a))
	copy(aCopy, a)
	bCopy := make([]string, len(b))
	copy(bCopy, b)

	sort.Strings(aCopy)
	sort.Strings(bCopy)

	return slices.Equal(aCopy, bCopy)
}

// HasConflictingEvidence checks if any claim in a dossier has evidence that
// contradicts another claim.
func HasConflictingEvidence(dossier *researcher.ResearchDossier) bool {
	if dossier == nil || len(dossier.Claims) == 0 {
		return false
	}

	claimMap := make(map[string]*researcher.Claim)
	for i := range dossier.Claims {
		claimMap[dossier.Claims[i].ID] = &dossier.Claims[i]
	}

	for _, c := range dossier.Claims {
		if len(c.SourcesContradicted) > 0 {
			return true
		}

		for _, relatedID := range c.SourcesContradicted {
			if _, ok := claimMap[relatedID]; ok {
				return true
			}
		}
	}

	return len(dossier.Contradictions) > 0
}

// UpdateVerificationNotes adds a new verification note to a list.
func UpdateVerificationNotes(notes []contracts.VerificationNote, newNote contracts.VerificationNote) []contracts.VerificationNote {
	for _, n := range notes {
		if n.ID == newNote.ID && n.Content == newNote.Content {
			return notes
		}
	}

	return append(notes, newNote)
}

// CreateWarningNote creates a verification note documenting a warning.
func CreateWarningNote(noteID, content string, relatedClaimIDs []string, now time.Time) contracts.VerificationNote {
	return contracts.VerificationNote{
		ID:              noteID,
		NoteType:        contracts.VerificationNoteWarning,
		Content:         content,
		RelatedClaimIDs: relatedClaimIDs,
		CreatedAt:       now,
	}
}

// CreateFindingNote creates a verification note documenting a significant finding.
func CreateFindingNote(noteID, content string, relatedClaimIDs []string, now time.Time) contracts.VerificationNote {
	return contracts.VerificationNote{
		ID:              noteID,
		NoteType:        contracts.VerificationNoteFinding,
		Content:         content,
		RelatedClaimIDs: relatedClaimIDs,
		CreatedAt:       now,
	}
}

// CreateTodoNote creates a verification note for follow-up tasks.
func CreateTodoNote(noteID, content string, relatedClaimIDs []string, now time.Time) contracts.VerificationNote {
	return contracts.VerificationNote{
		ID:              noteID,
		NoteType:        contracts.VerificationNoteTODO,
		Content:         content,
		RelatedClaimIDs: relatedClaimIDs,
		CreatedAt:       now,
	}
}

// MergeVerificationNotes combines verification notes from two results.
func MergeVerificationNotes(existing, newNotes []contracts.VerificationNote) []contracts.VerificationNote {
	if len(existing) == 0 {
		return newNotes
	}
	if len(newNotes) == 0 {
		return existing
	}

	seen := make(map[string]bool)
	result := make([]contracts.VerificationNote, len(existing))
	copy(result, existing)

	for _, n := range newNotes {
		if !seen[n.ID] {
			result = append(result, n)
			seen[n.ID] = true
		}
	}

	return result
}

// CountUnresolved returns the number of unresolved claims in a verification result.
func CountUnresolved(result *contracts.VerificationResult) int {
	if result == nil {
		return 0
	}

	count := 0
	for _, status := range result.VerificationStatuses {
		if status == contracts.VerificationStatusUncertain ||
			status == contracts.VerificationStatusInsufficientEvidence ||
			status == contracts.VerificationStatusContradicted {
			count++
		}
	}

	return count
}

// CountSupported returns the number of supported claims in a verification result.
func CountSupported(result *contracts.VerificationResult) int {
	if result == nil {
		return 0
	}

	count := 0
	for _, status := range result.VerificationStatuses {
		if status == contracts.VerificationStatusSupported {
			count++
		}
	}

	return count
}

// GetVerificationStatus returns the verification status for a claim.
func GetVerificationStatus(result *contracts.VerificationResult, claimID string) contracts.VerificationStatus {
	if result == nil {
		return ""
	}

	return result.VerificationStatuses[claimID]
}

// ReportUnresolvedClaims logs information about unresolved claims.
func ReportUnresolvedClaims(unresolved []string, result *contracts.VerificationResult) {
	if len(unresolved) == 0 {
		return
	}

	fmt.Printf("Unresolved claims (%d):\n", len(unresolved))
	for _, claimID := range unresolved {
		status := ""
		if result != nil {
			if s, ok := result.VerificationStatuses[claimID]; ok {
				status = fmt.Sprintf(" [%s]", s)
			}
		}
		fmt.Printf("  - %s%s\n", claimID, status)
	}
}

// HealthReport provides a summary of verification and resolution health.
type HealthReport struct {
	// TotalClaims is the total number of claims in the verification result.
	TotalClaims int

	// SupportedClaims is the number of verified supported claims.
	SupportedClaims int

	// ContradictedClaims is the number of contradicted claims.
	ContradictedClaims int

	// UncertainClaims is the number of uncertain claims.
	UncertainClaims int

	// InsufficientEvidenceClaims is the number of claims with insufficient evidence.
	InsufficientEvidenceClaims int

	// ResolutionRate is the percentage of claims that are resolved.
	ResolutionRate float64

	// QualityScore is the quality score from the verification result.
	QualityScore float64

	// NeedsFollowUp indicates if follow-up research is recommended.
	NeedsFollowUp bool

	// Recommendation provides guidance on next steps.
	Recommendation string
}

// GenerateHealthReport creates a health report from a verification result.
func GenerateHealthReport(result *contracts.VerificationResult) *HealthReport {
	if result == nil {
		return &HealthReport{
			TotalClaims:    0,
			ResolutionRate: 0,
			QualityScore:   0,
			NeedsFollowUp:  false,
			Recommendation: "No verification result available",
		}
	}

	total := len(result.VerificationStatuses)
	supported := CountSupported(result)
	contradicted := len(result.GetContradictedClaims())
	uncertain := len(result.GetUncertainClaims())

	unsupported := total - supported - contradicted - uncertain

	resolutionRate := CalculateResolutionRate(result)
	needsFollowUp := unsupported+uncertain+contradicted > 0

	var recommendation string
	if !needsFollowUp {
		recommendation = "All claims resolved - proceed to writer stage"
	} else if resolutionRate >= 80 {
		recommendation = "Most claims resolved - consider light follow-up on remaining issues"
	} else if resolutionRate >= 50 {
		recommendation = "Half of claims unresolved - follow-up research recommended"
	} else {
		recommendation = "Most claims unresolved - significant follow-up research needed"
	}

	return &HealthReport{
		TotalClaims:                total,
		SupportedClaims:            supported,
		ContradictedClaims:         contradicted,
		UncertainClaims:            uncertain,
		InsufficientEvidenceClaims: unsupported,
		ResolutionRate:             resolutionRate,
		QualityScore:               result.QualityScore,
		NeedsFollowUp:              needsFollowUp,
		Recommendation:             recommendation,
	}
}

// CalculateResolutionRate computes the percentage of claims that are resolved.
func CalculateResolutionRate(result *contracts.VerificationResult) float64 {
	if result == nil || len(result.VerificationStatuses) == 0 {
		return 0
	}

	supported := CountSupported(result)
	total := len(result.VerificationStatuses)

	return float64(supported) / float64(total) * 100
}

// Planner defines the interface for search query planning.
type Planner interface {
	Generate(ctx context.Context, topic string, language string, timeRangeHint *planner.TimeRangeHint) (*planner.SearchPlan, error)
}

// Searcher defines the interface for search services.
type Searcher interface {
	Search(ctx context.Context, query planner.SearchQuery, retryAttempts int) (SearchResult, error)
}

// SearchResult represents the results of a search query.
type SearchResult struct {
	Query        string
	Hits         []SearchHit
	TotalResults int
	ElapsedTime  time.Duration
}

// SearchHit represents a single search result hit.
type SearchHit struct {
	URL         string
	Title       string
	Snippet     string
	Relevance   float64
	PublishedAt *time.Time
}

// Fetcher defines the interface for fetching web content.
type Fetcher interface {
	Fetch(ctx context.Context, url string) (FetchResult, error)
}

// FetchResult represents the result of fetching a URL.
type FetchResult struct {
	URL         string
	Body        []byte
	ContentType string
	HTTPStatus  int
	Headers     http.Header
	RetrievedAt time.Time
}

// Extractor defines the interface for extracting document content.
type Extractor interface {
	Extract(ctx context.Context, fetchResult FetchResult) (*types.Document, error)
}

// DeduplicateStrings removes duplicate strings from a slice.
func DeduplicateStrings(s []string) []string {
	seen := make(map[string]bool)
	var result []string

	for _, item := range s {
		if !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}

	return result
}
