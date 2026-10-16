package publication

import (
	"errors"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// AttemptStatus enumerates the outcome of one execution of a publication
// target by a publisher.
//
// An attempt is one call into a publisher for one target. It is the unit of
// resumability: a crashed cycle leaves a dangling running attempt, and a
// resume first ends that attempt (cancelled) before starting a fresh one.
type AttemptStatus string

// Attempt status values.
const (
	// AttemptStatusRunning means the attempt is in flight.
	AttemptStatusRunning AttemptStatus = "running"

	// AttemptStatusSucceeded means the publisher confirmed the full
	// external publication and returned a receipt.
	AttemptStatusSucceeded AttemptStatus = "succeeded"

	// AttemptStatusFailed means the publisher reported a failure (or the
	// cycle was interrupted in a way that counts as a failure).
	AttemptStatusFailed AttemptStatus = "failed"

	// AttemptStatusCancelled means the attempt was stopped on request, or
	// ended as the recovery bookkeeping of a crashed cycle.
	AttemptStatusCancelled AttemptStatus = "cancelled"

	// AttemptStatusPartiallySucceeded means the publisher confirmed a
	// partial external publication (e.g. 8 of 10 thread posts posted).
	// The target remains eligible for retry so the remainder can be
	// published without re-posting what already went out.
	AttemptStatusPartiallySucceeded AttemptStatus = "partially_succeeded"
)

// AllAttemptStatuses lists every valid attempt status.
var AllAttemptStatuses = []AttemptStatus{
	AttemptStatusRunning,
	AttemptStatusSucceeded,
	AttemptStatusFailed,
	AttemptStatusCancelled,
	AttemptStatusPartiallySucceeded,
}

// Valid reports whether the attempt status is a known value.
func (s AttemptStatus) Valid() bool {
	for _, v := range AllAttemptStatuses {
		if s == v {
			return true
		}
	}
	return false
}

// Terminal reports whether the attempt reached a final outcome.
func (s AttemptStatus) Terminal() bool {
	return s != AttemptStatusRunning
}

// String returns the string representation of the attempt status.
func (s AttemptStatus) String() string {
	return string(s)
}

// MaxAttemptsPerTarget bounds the per-target attempt history.
//
// A target may be retried across many publication cycles, but each target
// keeps only the most recent attempts: when the bound is exceeded, the
// oldest attempt is dropped and the history renumbered 1..N (trimAttempts).
// Earlier receipts remain inspectable through the target's current receipt
// and the plan-level error/warning logs.
const MaxAttemptsPerTarget = 16

// PublicationAttempt records one execution of a publication target.
//
// Attempts are numbered 1..N in chronological order. Only the latest attempt
// determines the target's current status: a failed target that is retried
// always has a newer, running attempt.
type PublicationAttempt struct {
	// Number is the 1-based, strictly increasing attempt index within the
	// target.
	Number int `json:"number"`

	// Status is the attempt outcome.
	Status AttemptStatus `json:"status"`

	// StartedAt is when the attempt began (UTC).
	StartedAt run.Timestamp `json:"started_at"`

	// FinishedAt is when the attempt ended (UTC). Nil while running.
	FinishedAt *run.Timestamp `json:"finished_at,omitempty"`

	// Error is the failure summary when the attempt failed (redacted).
	Error *run.ErrorSummary `json:"error,omitempty"`

	// Receipt is the external proof of publication when the attempt
	// succeeded (or partially succeeded, for the portion confirmed).
	Receipt *PublicationReceipt `json:"receipt,omitempty"`
}

// NewAttempt creates a new running attempt.
func NewAttempt(number int, at time.Time) PublicationAttempt {
	return PublicationAttempt{
		Number:    number,
		Status:    AttemptStatusRunning,
		StartedAt: run.NewTimestamp(at),
	}
}

// Validate checks the attempt: numbering, status, timestamp ordering, and
// the terminal-state invariants (a failed attempt carries its error, a
// succeeded attempt carries its receipt).
func (a PublicationAttempt) Validate() error {
	if a.Number < 1 {
		return fmt.Errorf("attempt number must be >= 1 (got %d)", a.Number)
	}
	if !a.Status.Valid() {
		return fmt.Errorf("attempt %d: unknown status %q", a.Number, a.Status)
	}
	if a.StartedAt.IsZero() {
		return fmt.Errorf("attempt %d: started_at is required", a.Number)
	}
	switch {
	case a.Status == AttemptStatusRunning:
		if a.FinishedAt != nil && !a.FinishedAt.IsZero() {
			return fmt.Errorf("attempt %d is running but has a finished_at", a.Number)
		}
	case a.FinishedAt == nil || a.FinishedAt.IsZero():
		return fmt.Errorf("attempt %d is %s but has no finished_at", a.Number, a.Status)
	default:
		if a.FinishedAt.Before(a.StartedAt) {
			return fmt.Errorf("attempt %d finished before it started", a.Number)
		}
	}
	switch a.Status {
	case AttemptStatusFailed:
		if a.Error == nil {
			return fmt.Errorf("failed attempt %d requires an error summary", a.Number)
		}
		if err := a.Error.Validate(); err != nil {
			return fmt.Errorf("attempt %d: invalid error summary: %w", a.Number, err)
		}
	case AttemptStatusSucceeded:
		if a.Receipt == nil {
			return fmt.Errorf("succeeded attempt %d requires a receipt", a.Number)
		}
		if err := a.Receipt.Validate(); err != nil {
			return fmt.Errorf("attempt %d: invalid receipt: %w", a.Number, err)
		}
	case AttemptStatusPartiallySucceeded:
		if a.Receipt != nil {
			if err := a.Receipt.Validate(); err != nil {
				return fmt.Errorf("attempt %d: invalid receipt: %w", a.Number, err)
			}
		}
	}
	return nil
}

// trimAttempts enforces the per-target attempt-history bound in place: the
// oldest attempts are dropped and the survivors renumbered 1..N, so the
// history always holds the most recent MaxAttemptsPerTarget attempts.
func trimAttempts(attempts *[]PublicationAttempt) {
	if len(*attempts) <= MaxAttemptsPerTarget {
		return
	}
	drop := len(*attempts) - MaxAttemptsPerTarget
	survivors := (*attempts)[drop:]
	for i := range survivors {
		survivors[i].Number = i + 1
	}
	*attempts = survivors
}

// validateAttemptHistory checks the full attempt history of a target:
// strictly increasing numbers starting at 1, the bound, and — when the
// target is in a state that implies an attempt — the latest attempt's status
// matching the target status.
func validateAttemptHistory(targetID TargetID, status PublicationStatus, attempts []PublicationAttempt) error {
	if len(attempts) > MaxAttemptsPerTarget {
		return fmt.Errorf("target %q: %d attempts exceed the bound of %d", targetID, len(attempts), MaxAttemptsPerTarget)
	}
	for i, a := range attempts {
		if a.Number != i+1 {
			return fmt.Errorf("target %q: attempt history must be numbered 1..%d in order (found %d at index %d)",
				targetID, len(attempts), a.Number, i)
		}
		if err := a.Validate(); err != nil {
			return fmt.Errorf("target %q: %w", targetID, err)
		}
	}
	switch status {
	case StatusPlanned, StatusSkipped:
		// A planned target has never been executed, and a skipped target
		// only ever transitions from planned: neither carries attempts.
		if len(attempts) > 0 {
			return fmt.Errorf("target %q is %s but carries %d attempt(s)", targetID, status, len(attempts))
		}
	case StatusPublishing:
		if len(attempts) == 0 {
			return fmt.Errorf("target %q is publishing but has no running attempt", targetID)
		}
		last := attempts[len(attempts)-1]
		if last.Status != AttemptStatusRunning {
			return fmt.Errorf("target %q is publishing but its latest attempt is %q", targetID, last.Status)
		}
	case StatusSucceeded:
		if len(attempts) == 0 || attempts[len(attempts)-1].Status != AttemptStatusSucceeded {
			return fmt.Errorf("target %q is succeeded but its latest attempt is not succeeded", targetID)
		}
	case StatusFailed:
		if len(attempts) == 0 || attempts[len(attempts)-1].Status != AttemptStatusFailed {
			return fmt.Errorf("target %q is failed but its latest attempt is not failed", targetID)
		}
	case StatusPartiallySucceeded:
		if len(attempts) == 0 || attempts[len(attempts)-1].Status != AttemptStatusPartiallySucceeded {
			return fmt.Errorf("target %q is partially succeeded but its latest attempt is not partially succeeded", targetID)
		}
	case StatusCancelled:
		if len(attempts) == 0 || attempts[len(attempts)-1].Status != AttemptStatusCancelled {
			return fmt.Errorf("target %q is cancelled but its latest attempt is not cancelled", targetID)
		}
	}
	return nil
}

// newErrorSummary builds a validated, redacted error summary for an attempt
// or plan error log entry.
func newErrorSummary(code, message string, at time.Time) (run.ErrorSummary, error) {
	if message == "" {
		return run.ErrorSummary{}, errors.New("error message is required")
	}
	summary := run.NewErrorSummary(code, message, at)
	if err := summary.Validate(); err != nil {
		return run.ErrorSummary{}, err
	}
	return summary, nil
}
