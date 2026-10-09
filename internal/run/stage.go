package run

import (
	"fmt"
)

// StageName enumerates the standard newsroom pipeline stages.
//
// The set is deterministic and versioned with the manifest schema; new stages
// added in a future schema major version will appear here as new constants.
//
// A run does NOT execute every stage: each stage record carries its own
// status, and stages that are not needed for a given run are skipped (or
// absent from the plan entirely).
type StageName string

// Standard stage names, in canonical pipeline order.
const (
	// StageDiscovery finds candidate source URLs from the topic.
	StageDiscovery StageName = "discovery"

	// StageFetch retrieves raw content from the selected source URLs.
	StageFetch StageName = "fetch"

	// StageExtract normalizes fetched content into plain-text documents.
	StageExtract StageName = "extract"

	// StageResearch synthesizes documents into a research dossier.
	StageResearch StageName = "research"

	// StageVerify assesses the verifiability of each dossier claim.
	StageVerify StageName = "verify"

	// StageFollowUp performs bounded follow-up research for unresolved claims.
	StageFollowUp StageName = "follow_up"

	// StageWrite transforms verified claims into an editorial artifact.
	StageWrite StageName = "write"

	// StageFinalCheck is the final factual gate before publication.
	StageFinalCheck StageName = "final_check"

	// StageRender produces the final rendered output artifacts.
	StageRender StageName = "render"
)

// AllStageNames lists every standard stage name.
var AllStageNames = []StageName{
	StageDiscovery,
	StageFetch,
	StageExtract,
	StageResearch,
	StageVerify,
	StageFollowUp,
	StageWrite,
	StageFinalCheck,
	StageRender,
}

// CanonicalStages returns the standard pipeline in its canonical order.
//
// This is the default stage plan used by NewManifest; a run may legitimately
// plan a subset, and every planned stage may be skipped at execution time.
func CanonicalStages() []StageName {
	stages := make([]StageName, len(AllStageNames))
	copy(stages, AllStageNames)
	return stages
}

// Valid reports whether the stage name is a known standard stage.
func (n StageName) Valid() bool {
	for _, v := range AllStageNames {
		if n == v {
			return true
		}
	}
	return false
}

// String returns the string representation of the stage name.
func (n StageName) String() string {
	return string(n)
}

// StageAttempt records one execution of a stage.
//
// Attempts are numbered 1..N in chronological order. Only the latest attempt
// determines the stage's current status: a stage may fail and be retried, so
// a failed attempt is always followed by a newer one when a retry starts.
type StageAttempt struct {
	// Number is the 1-based, strictly increasing attempt index within the stage.
	Number int `json:"number"`

	// Status is the attempt outcome. Valid values: running, succeeded,
	// failed, cancelled (never pending or skipped).
	Status StageStatus `json:"status"`

	// StartedAt is when the attempt began (UTC).
	StartedAt Timestamp `json:"started_at"`

	// FinishedAt is when the attempt reached its terminal state.
	// Nil while the attempt is running.
	FinishedAt *Timestamp `json:"finished_at,omitempty"`

	// Error summarizes why the attempt failed.
	// Required when Status is failed; redacted before recording.
	Error *ErrorSummary `json:"error,omitempty"`
}

// Validate performs structural validation on a stage attempt.
func (a StageAttempt) Validate() error {
	if a.Number < 1 {
		return fmt.Errorf("attempt number must be >= 1, got %d", a.Number)
	}
	if !ValidAttemptStatus(a.Status) {
		return fmt.Errorf("invalid attempt status %q: attempts must be running, succeeded, failed, or cancelled", a.Status)
	}
	if a.StartedAt.IsZero() {
		return fmt.Errorf("attempt %d: started_at is required", a.Number)
	}
	if a.Status.Terminal() {
		if a.FinishedAt == nil || a.FinishedAt.IsZero() {
			return fmt.Errorf("attempt %d: terminal attempt requires finished_at", a.Number)
		}
		if a.FinishedAt.Before(a.StartedAt) {
			return fmt.Errorf("attempt %d: finished_at is earlier than started_at", a.Number)
		}
	}
	if a.Status == StageStatusRunning && a.FinishedAt != nil && !a.FinishedAt.IsZero() {
		return fmt.Errorf("attempt %d: running attempt must not have finished_at", a.Number)
	}
	if a.Status == StageStatusFailed && a.Error == nil {
		return fmt.Errorf("attempt %d: failed attempt requires an error summary", a.Number)
	}
	if a.Status == StageStatusFailed && a.Error != nil {
		if err := a.Error.Validate(); err != nil {
			return fmt.Errorf("attempt %d: %w", a.Number, err)
		}
	}
	if a.Error != nil && a.Status != StageStatusFailed {
		return fmt.Errorf("attempt %d: error summary is only valid on failed attempts", a.Number)
	}
	return nil
}

// StageRecord is the manifest entry for one pipeline stage within a run.
//
// A record captures the stage's current status, the reason it was skipped
// (when skipped), and the full attempt history. Stage records never carry
// stage inputs or outputs directly: artifacts are referenced at the run
// level via ArtifactRef.
type StageRecord struct {
	// Name is the stage this record tracks.
	Name StageName `json:"name"`

	// Status is the stage's current lifecycle status.
	Status StageStatus `json:"status"`

	// SkipReason explains why the stage was skipped.
	// Required when Status is skipped; must be empty otherwise.
	SkipReason string `json:"skip_reason,omitempty"`

	// Attempts is the chronological attempt history (1..N).
	// Empty for pending and skipped stages.
	Attempts []StageAttempt `json:"attempts,omitempty"`
}

// Validate performs structural validation on a stage record, including the
// consistency between the record status and its attempt history.
func (s StageRecord) Validate() error {
	if !s.Name.Valid() {
		return fmt.Errorf("stage %q: unknown stage name", s.Name)
	}
	if !s.Status.Valid() {
		return fmt.Errorf("stage %q: invalid status %q", s.Name, s.Status)
	}

	for i, a := range s.Attempts {
		if a.Number != i+1 {
			return fmt.Errorf("stage %q: attempt numbers must be sequential 1..N (got %d at position %d)", s.Name, a.Number, i+1)
		}
		if err := a.Validate(); err != nil {
			return fmt.Errorf("stage %q: %w", s.Name, err)
		}
	}

	// Status <-> attempt-history consistency.
	switch s.Status {
	case StageStatusPending, StageStatusSkipped:
		if len(s.Attempts) > 0 {
			return fmt.Errorf("stage %q: status %q must have no attempts", s.Name, s.Status)
		}
	default:
		if len(s.Attempts) == 0 {
			return fmt.Errorf("stage %q: status %q requires at least one attempt", s.Name, s.Status)
		}
		last := s.Attempts[len(s.Attempts)-1]
		if last.Status != s.Status {
			return fmt.Errorf("stage %q: status %q does not match last attempt status %q", s.Name, s.Status, last.Status)
		}
	}

	if s.Status == StageStatusSkipped {
		if s.SkipReason == "" {
			return fmt.Errorf("stage %q: skipped stage requires a skip_reason", s.Name)
		}
	} else if s.SkipReason != "" {
		return fmt.Errorf("stage %q: skip_reason is only valid on skipped stages", s.Name)
	}
	return nil
}
