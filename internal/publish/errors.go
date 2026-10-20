package publish

import "errors"

// Sentinel errors reported by the publish engine.
//
// Every error means the engine changed no external state and left the
// persisted plan in a state that explains itself; the recovery path is
// named in each error's construction site.
var (
	// ErrPlanNotFound indicates the requested plan does not exist for the
	// run (no publication_state on the manifest and no plan document on
	// disk).
	ErrPlanNotFound = errors.New("publication plan not found")

	// ErrPlanInvalid indicates a persisted plan document is corrupt,
	// tampered, or written with a schema major version this build cannot
	// read. The plan fails closed: it is refused, not repaired, and its
	// targets are never executed. Re-baseline the artifact (or create a
	// new plan) once the content issue is understood.
	ErrPlanInvalid = errors.New("persisted publication plan is invalid")

	// ErrNotApproved indicates the plan has no effective human approval
	// covering its current artifact: it was never approved, or an approval
	// was revoked, or a rebaseline invalidated it. Publication is refused
	// until a human approves the current artifact content.
	ErrNotApproved = errors.New("publication plan is not approved")

	// ErrStaleApproval indicates an approval is recorded but no longer
	// matches the plan's current artifact digest (the content changed
	// after the approval was granted and the approval was invalidated).
	// Recovery is explicit: re-baseline plus a fresh approval
	// (RefreshArtifact), never a silent re-approval.
	ErrStaleApproval = errors.New("approval is stale: the artifact changed after it was granted")

	// ErrGateFailed indicates the recorded pre-publication gate evidence
	// is not passing (the run is not succeeded, the final factual check
	// did not pass, or the review state is not READY). Nothing may be
	// published for this plan until the run itself is fixed and the gate
	// re-gathered.
	ErrGateFailed = errors.New("publication gate evidence is not passing")

	// ErrArtifactChanged indicates the content file on disk no longer
	// matches the plan's recorded artifact digest: the approved bytes
	// would no longer be what is published. Publication fails closed;
	// re-baseline the artifact and re-approve (RefreshArtifact).
	ErrArtifactChanged = errors.New("approved artifact content changed after the plan baseline")

	// ErrArtifactMissing indicates the plan's content file is absent from
	// the run directory, so the engine cannot verify what would be
	// published. Publication fails closed.
	ErrArtifactMissing = errors.New("publication artifact file is missing from the run directory")

	// ErrPlanCancelled indicates the plan was cancelled by a human and
	// will not resume: cancellation is terminal. Re-publishing the same
	// artifact is a new plan with a new approval.
	ErrPlanCancelled = errors.New("publication plan is cancelled")

	// ErrPublisherNotBound indicates a target that must execute has no
	// publisher bound for it. The cycle is refused before any state
	// change: binding the publisher and retrying is the fix.
	ErrPublisherNotBound = errors.New("no publisher is bound for a target that must be published")

	// ErrInterrupted indicates the publication cycle was interrupted before
	// it completed (a cancelled context, or a state-recording failure): the
	// persisted plan is left in publishing with its in-flight targets
	// aborted, and it is the resume point — call Publish again with a
	// healthy context to resume. The interruption itself is not a target
	// failure: whatever external effects the interrupted calls produced are
	// unproven, so the orchestrator surfaces them as manual verification,
	// never as an automatic rollback.
	ErrInterrupted = errors.New("publication cycle was interrupted before completion")
)
