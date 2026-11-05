package memory

import (
	"fmt"
	"strings"
	"time"
)

// FeedbackRecord is the Value payload for KindEditorialFeedback records.
//
// Editorial feedback captures what a human (or a review tooling acting on a
// human's behalf) said about the newsroom's output. Feedback is a signal for
// future behavior, never an instruction that bypasses the pipeline: it
// should be applied as a preference, weighed by the editor, not executed
// blindly.
type FeedbackRecord struct {
	// TargetKind is what the feedback is about: "artifact", "profile",
	// "recipe", "run", or another consumer-defined label.
	TargetKind string `json:"target_kind,omitempty"`

	// TargetID is the stable ID of the thing the feedback is about.
	TargetID string `json:"target_id,omitempty"`

	// Feedback is the feedback itself, in the human's own words.
	Feedback string `json:"feedback"`

	// GivenBy is the identifier of the person (or role) who gave the
	// feedback.
	GivenBy string `json:"given_by"`

	// GivenAt is when the feedback was given (UTC).
	GivenAt time.Time `json:"given_at"`

	// Recurring, when true, marks this as a recurring preference rather than
	// a one-off remark. Recurring feedback typically carries VolatileStatic
	// until superseded.
	Recurring bool `json:"recurring,omitempty"`

	// Preference is an optional structured distillation of the feedback into
	// a machine-checkable preference, e.g. {"tone": "concise"} or
	// {"avoid": "hedging phrases"}.
	Preference map[string]string `json:"preference,omitempty"`
}

// Validate checks the payload's invariants.
func (v FeedbackRecord) Validate() error {
	if v.Feedback == "" {
		return fmt.Errorf("feedback record: feedback text is required")
	}
	if v.GivenBy == "" {
		return fmt.Errorf("feedback record: given_by is required")
	}
	if v.GivenAt.IsZero() {
		return fmt.Errorf("feedback record: given_at is required")
	}
	return nil
}

// CorrectionRecord is the Value payload for KindCorrection records.
//
// A correction record documents that a previously published or persisted
// claim was wrong, what the corrected form is, and why. Corrections are
// the primary input for invalidating factual memories: the persistence
// layer (v0.7+) is expected to supersede or invalidate the memories whose
// provenance traces to the original wrong claim.
type CorrectionRecord struct {
	// OriginalClaim is the claim as it was (verbatim where possible).
	OriginalClaim string `json:"original_claim"`

	// CorrectedClaim is the claim as it should read.
	CorrectedClaim string `json:"corrected_claim"`

	// Reason is why the correction was made.
	Reason string `json:"reason"`

	// CorrectedBy is the identifier of the person who issued the correction.
	CorrectedBy string `json:"corrected_by"`

	// CorrectedAt is when the correction was issued (UTC).
	CorrectedAt time.Time `json:"corrected_at"`

	// AffectedRunID is the run that produced the original claim, if known.
	AffectedRunID string `json:"affected_run_id,omitempty"`

	// AffectedMemoryIDs lists the stable IDs of memory records the correction
	// is expected to supersede or invalidate.
	AffectedMemoryIDs []string `json:"affected_memory_ids,omitempty"`
}

// Validate checks the payload's invariants.
func (v CorrectionRecord) Validate() error {
	if v.OriginalClaim == "" {
		return fmt.Errorf("correction record: original_claim is required")
	}
	if v.CorrectedClaim == "" {
		return fmt.Errorf("correction record: corrected_claim is required")
	}
	if v.CorrectedBy == "" {
		return fmt.Errorf("correction record: corrected_by is required")
	}
	if v.CorrectedAt.IsZero() {
		return fmt.Errorf("correction record: corrected_at is required")
	}
	return nil
}

// SuggestionRecord is the Value payload for KindSuggestion records.
//
// Suggestions are proposals to refine profiles or recipes — from the LLM,
// from observed feedback, or from humans. A suggestion is never an
// application: applying it is an explicit, separately approved act.
type SuggestionRecord struct {
	// SuggestionKind is what is being suggested: "profile" or "recipe"
	// (or another consumer-defined label).
	SuggestionKind string `json:"suggestion_kind"`

	// TargetID is the stable ID of the profile or recipe being refined.
	TargetID string `json:"target_id,omitempty"`

	// Proposal is the suggested change, in plain language.
	Proposal string `json:"proposal"`

	// ProposedChange is an optional structured representation of the change,
	// e.g. {"field": "voice", "from": "...", "to": "..."}.
	ProposedChange map[string]string `json:"proposed_change,omitempty"`

	// Status is the review state: "proposed", "approved", "rejected", or
	// "applied". New suggestions start "proposed".
	Status SuggestionStatus `json:"status"`

	// ReviewedBy / ReviewedAt record the human decision, required once the
	// status leaves "proposed".
	ReviewedBy string    `json:"reviewed_by,omitempty"`
	ReviewedAt time.Time `json:"reviewed_at,omitempty"`
}

// SuggestionStatus enumerates the lifecycle of a suggestion.
type SuggestionStatus string

const (
	// SuggestionProposed is the initial state.
	SuggestionProposed SuggestionStatus = "proposed"

	// SuggestionApproved: a human approved it; it is still not applied.
	SuggestionApproved SuggestionStatus = "approved"

	// SuggestionRejected: a human declined it.
	SuggestionRejected SuggestionStatus = "rejected"

	// SuggestionApplied: the change was applied to the target profile/recipe.
	SuggestionApplied SuggestionStatus = "applied"
)

// AllSuggestionStatuses lists every recognized SuggestionStatus.
var AllSuggestionStatuses = []SuggestionStatus{
	SuggestionProposed,
	SuggestionApproved,
	SuggestionRejected,
	SuggestionApplied,
}

// ParseSuggestionStatus converts a string into a SuggestionStatus.
func ParseSuggestionStatus(s string) (SuggestionStatus, error) {
	st := SuggestionStatus(strings.TrimSpace(s))
	for _, valid := range AllSuggestionStatuses {
		if st == valid {
			return st, nil
		}
	}
	return "", fmt.Errorf("unknown suggestion status %q", s)
}

// Validate checks the payload's invariants.
func (v SuggestionRecord) Validate() error {
	if v.SuggestionKind == "" {
		return fmt.Errorf("suggestion record: suggestion_kind is required")
	}
	if v.Proposal == "" {
		return fmt.Errorf("suggestion record: proposal is required")
	}
	if _, err := ParseSuggestionStatus(string(v.Status)); err != nil {
		return err
	}
	if v.Status != SuggestionProposed {
		if v.ReviewedBy == "" {
			return fmt.Errorf("suggestion record: reviewed_by is required when status is %q", v.Status)
		}
		if v.ReviewedAt.IsZero() {
			return fmt.Errorf("suggestion record: reviewed_at is required when status is %q", v.Status)
		}
	}
	return nil
}

// RecipeExecutionRecord is the Value payload for KindRecipeExecution records.
//
// Recipe execution memory tracks how a recipe performed when run: outcome,
// what broke, and lessons worth remembering. It informs future planning
// (skip flaky stages, re-order steps) but never substitutes for actually
// running the recipe.
type RecipeExecutionRecord struct {
	// RecipeID is the stable ID of the recipe that was executed.
	RecipeID string `json:"recipe_id"`

	// RunID is the run that executed the recipe, if known.
	RunID string `json:"run_id,omitempty"`

	// Outcome is the execution result: "success", "partial", or "failure".
	Outcome RecipeOutcome `json:"outcome"`

	// ExecutedAt is when the execution finished (UTC).
	ExecutedAt time.Time `json:"executed_at"`

	// Lessons is a distilled list of durable takeaways from the execution.
	Lessons []string `json:"lessons,omitempty"`

	// Failures lists stage-level failures observed during the run, if the
	// outcome is partial or failure.
	Failures []RecipeFailure `json:"failures,omitempty"`
}

// RecipeOutcome enumerates recipe execution results.
type RecipeOutcome string

const (
	// RecipeOutcomeSuccess: every stage completed.
	RecipeOutcomeSuccess RecipeOutcome = "success"

	// RecipeOutcomePartial: some stages completed, some failed.
	RecipeOutcomePartial RecipeOutcome = "partial"

	// RecipeOutcomeFailure: the run did not produce a usable artifact.
	RecipeOutcomeFailure RecipeOutcome = "failure"
)

// AllRecipeOutcomes lists every recognized RecipeOutcome.
var AllRecipeOutcomes = []RecipeOutcome{
	RecipeOutcomeSuccess,
	RecipeOutcomePartial,
	RecipeOutcomeFailure,
}

// ParseRecipeOutcome converts a string into a RecipeOutcome.
func ParseRecipeOutcome(s string) (RecipeOutcome, error) {
	o := RecipeOutcome(strings.TrimSpace(s))
	for _, valid := range AllRecipeOutcomes {
		if o == valid {
			return o, nil
		}
	}
	return "", fmt.Errorf("unknown recipe outcome %q", s)
}

// RecipeFailure is one stage-level failure observed in an execution.
type RecipeFailure struct {
	// Stage is the name of the recipe stage that failed.
	Stage string `json:"stage"`

	// Message is the failure reason, free-text.
	Message string `json:"message"`

	// Recovered indicates whether the run continued past this failure.
	Recovered bool `json:"recovered,omitempty"`
}

// Validate checks the payload's invariants.
func (v RecipeExecutionRecord) Validate() error {
	if v.RecipeID == "" {
		return fmt.Errorf("recipe execution record: recipe_id is required")
	}
	if _, err := ParseRecipeOutcome(string(v.Outcome)); err != nil {
		return err
	}
	if v.ExecutedAt.IsZero() {
		return fmt.Errorf("recipe execution record: executed_at is required")
	}
	for _, f := range v.Failures {
		if f.Stage == "" {
			return fmt.Errorf("recipe execution record: failure stage is required")
		}
	}
	return nil
}

// PatternOccurrence represents one occurrence of a detected pattern.
type PatternOccurrence struct {
	// RecordID is the stable ID of the editorial feedback record.
	RecordID string `json:"record_id"`

	// Feedback is the full feedback text from the record.
	Feedback string `json:"feedback"`

	// Author is the editor who gave the feedback (GivenBy field).
	Author string `json:"author"`

	// GivenAt is when the feedback was given (UTC).
	GivenAt time.Time `json:"given_at"`

	// SubjectKind is the kind of subject (recipe, profile, entity, etc.).
	SubjectKind string `json:"subject_kind"`

	// SubjectName is the name of the subject.
	SubjectName string `json:"subject_name"`

	// Category is the editorial change category.
	Category string `json:"category"`

	// Field is the record's field (e.g., "hunk-0", "summary").
	Field string `json:"field"`
}

// PatternRecord is the Value payload for KindEditorialPattern records.
//
// A pattern record represents a detected repeated editorial feedback pattern.
// Patterns are advisory: they summarize recurring issues but don't bypass
// the verification pipeline or modify profiles automatically.
type PatternRecord struct {
	// Category is the editorial change category for this pattern.
	Category string `json:"category"`

	// GroupKey identifies the grouping (author/profile/recipe).
	GroupKey string `json:"group_key"`

	// MinOccurrences is the minimum occurrences threshold used for detection.
	MinOccurrences int `json:"min_occurrences"`

	// Occurrences is the list of individual occurrences that form this pattern.
	Occurrences []PatternOccurrence `json:"occurrences"`

	// SampleFeedback is a sample feedback text from the pattern.
	SampleFeedback string `json:"sample_feedback"`

	// Notes is an optional human-readable description of the pattern.
	Notes string `json:"notes,omitempty"`
}

// Validate checks the payload's invariants.
func (v PatternRecord) Validate() error {
	if v.Category == "" {
		return fmt.Errorf("pattern record: category is required")
	}
	if v.GroupKey == "" {
		return fmt.Errorf("pattern record: group_key is required")
	}
	if v.MinOccurrences <= 0 {
		return fmt.Errorf("pattern record: min_occurrences must be positive")
	}
	if len(v.Occurrences) == 0 {
		return fmt.Errorf("pattern record: at least one occurrence is required")
	}
	if v.SampleFeedback == "" {
		return fmt.Errorf("pattern record: sample_feedback is required")
	}
	return nil
}
