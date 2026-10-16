package publication

// PublicationStatus enumerates the lifecycle states of a controlled
// publication.
//
// The same status set is used at two levels:
//   - plan level: the state of the whole publication plan (one run, one
//     artifact, one or more targets);
//   - target level: the state of one publication target within the plan.
//
// The plan lifecycle is:
//
//	planned -> approved -> publishing -> succeeded
//	                              -> failed
//	                              -> partially_succeeded
//	                              -> cancelled
//	                              -> skipped (every target deliberately skipped)
//	planned ----------> cancelled | skipped
//	approved ---------> planned (approval revoked or stale)
//	approved ---------> cancelled | skipped
//	failed -------------> publishing (retry / resume)
//	partially_succeeded -> publishing (retry the remainder)
//
// The target lifecycle is:
//
//	planned -> publishing -> succeeded | failed | partially_succeeded | cancelled
//	planned -------------> skipped | cancelled
//	failed ---------------> publishing (retry)
//	partially_succeeded -> publishing (retry the remainder)
//
// The retry transitions (failed / partially_succeeded -> publishing) are the
// resumability path: a crashed or partially failed publication cycle restarts
// from the plan and re-executes only the targets that did not finish, so a
// target that already succeeded is never published twice.
//
// Apart from those retries, terminal states never transition again: a
// finished plan is a historical record, exactly like a finished run manifest
// (internal/run). Publishing the same artifact again — even after a full
// failure — is a new plan with a new publication ID.
type PublicationStatus string

// Status values.
const (
	// StatusPlanned means the plan (or target) has been created but no
	// human approval exists yet. Nothing may be published.
	StatusPlanned PublicationStatus = "planned"

	// StatusApproved means a human approval, bound to the exact artifact
	// digest, is in force. Publication may begin.
	StatusApproved PublicationStatus = "approved"

	// StatusPublishing means a publication cycle is in progress: at least
	// one target is executing or a cycle has been started.
	StatusPublishing PublicationStatus = "publishing"

	// StatusSucceeded means the plan published everything it set out to
	// publish (every target succeeded or was deliberately skipped).
	StatusSucceeded PublicationStatus = "succeeded"

	// StatusFailed means the plan finished with no target succeeding
	// (every target failed or was cancelled).
	StatusFailed PublicationStatus = "failed"

	// StatusPartiallySucceeded means the plan finished with at least one
	// target succeeded and at least one target failed, partially succeeded,
	// or was cancelled.
	StatusPartiallySucceeded PublicationStatus = "partially_succeeded"

	// StatusCancelled means the plan was stopped on request before
	// completion. Targets that never started keep their pre-cancellation
	// status; in-flight targets are recorded as cancelled.
	StatusCancelled PublicationStatus = "cancelled"

	// StatusSkipped means the plan (or a target) was deliberately not
	// published, with a recorded reason.
	StatusSkipped PublicationStatus = "skipped"
)

// AllStatuses lists every valid publication status in lifecycle order.
var AllStatuses = []PublicationStatus{
	StatusPlanned,
	StatusApproved,
	StatusPublishing,
	StatusSucceeded,
	StatusFailed,
	StatusPartiallySucceeded,
	StatusCancelled,
	StatusSkipped,
}

// Valid reports whether the status is a known value.
func (s PublicationStatus) Valid() bool {
	for _, v := range AllStatuses {
		if s == v {
			return true
		}
	}
	return false
}

// Terminal reports whether the status is a final one.
//
// At the plan level a terminal plan is immutable: it is a durable record of
// what was published where, and the only way to publish again is a new plan.
func (s PublicationStatus) Terminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusPartiallySucceeded, StatusCancelled, StatusSkipped:
		return true
	}
	return false
}

// String returns the string representation of the status.
func (s PublicationStatus) String() string {
	return string(s)
}

// ValidPlanTransition reports whether a publication plan may move from one
// status to another.
//
// Valid transitions:
//   - planned -> approved | cancelled | skipped
//   - approved -> publishing | planned | cancelled | skipped
//   - publishing -> every terminal state (the cycle finished; when every
//     target was deliberately skipped, the plan aggregates to skipped)
//   - failed -> publishing (retry the targets that did not finish)
//   - partially_succeeded -> publishing (retry the remainder)
//
// The approved -> planned transition is the bookkeeping behind an approval
// being revoked or going stale: without an effective approval the plan falls
// back to its pre-approval state.
func ValidPlanTransition(from, to PublicationStatus) bool {
	switch from {
	case StatusPlanned:
		return to == StatusApproved || to == StatusCancelled || to == StatusSkipped
	case StatusApproved:
		return to == StatusPublishing || to == StatusPlanned || to == StatusCancelled || to == StatusSkipped
	case StatusPublishing:
		return to.Terminal()
	case StatusFailed, StatusPartiallySucceeded:
		return to == StatusPublishing
	default:
		return false // succeeded / cancelled / skipped are terminal
	}
}

// ValidTargetTransition reports whether a publication target may move from
// one status to another.
//
// Valid transitions:
//   - planned -> publishing | skipped | cancelled
//   - publishing -> succeeded | failed | partially_succeeded | cancelled
//   - failed -> publishing (retry)
//   - partially_succeeded -> publishing (retry the remainder)
//
// A target is never "approved": approval is a plan-level act (a human
// approves the plan; the approval's scope may list individual targets).
func ValidTargetTransition(from, to PublicationStatus) bool {
	switch from {
	case StatusPlanned:
		return to == StatusPublishing || to == StatusSkipped || to == StatusCancelled
	case StatusPublishing:
		return to == StatusSucceeded || to == StatusFailed || to == StatusPartiallySucceeded || to == StatusCancelled
	case StatusFailed, StatusPartiallySucceeded:
		return to == StatusPublishing
	default:
		return false // succeeded / cancelled / skipped are terminal
	}
}
