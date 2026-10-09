package run

// RunStatus enumerates the lifecycle states of a newsroom run.
//
// The lifecycle is:
//
//	pending -> running -> succeeded
//	                 -> failed
//	                 -> cancelled
//	pending -----------> cancelled   (a run may be cancelled before it starts)
//
// Terminal states (succeeded, failed, cancelled) never transition again
// within a single manifest.
type RunStatus string

// Run status values.
const (
	// RunStatusPending means the run has been created but not started.
	RunStatusPending RunStatus = "pending"

	// RunStatusRunning means the run is currently executing stages.
	RunStatusRunning RunStatus = "running"

	// RunStatusSucceeded means every planned stage finished (succeeded or
	// skipped) and the run completed its editorial job.
	RunStatusSucceeded RunStatus = "succeeded"

	// RunStatusFailed means the run stopped because a failure blocked the
	// editorial job. Stages that never ran remain pending.
	RunStatusFailed RunStatus = "failed"

	// RunStatusCancelled means the run was stopped on request before
	// completion. Stages that never ran remain pending.
	RunStatusCancelled RunStatus = "cancelled"
)

// AllRunStatuses lists every valid run status in lifecycle order.
var AllRunStatuses = []RunStatus{
	RunStatusPending,
	RunStatusRunning,
	RunStatusSucceeded,
	RunStatusFailed,
	RunStatusCancelled,
}

// Valid reports whether the run status is a known value.
func (s RunStatus) Valid() bool {
	for _, v := range AllRunStatuses {
		if s == v {
			return true
		}
	}
	return false
}

// Terminal reports whether the run reached a final state.
func (s RunStatus) Terminal() bool {
	return s == RunStatusSucceeded || s == RunStatusFailed || s == RunStatusCancelled
}

// String returns the string representation of the status.
func (s RunStatus) String() string {
	return string(s)
}

// ValidRunTransition reports whether a run may move from one status to
// another.
//
// Valid transitions:
//   - pending -> running | cancelled
//   - running -> succeeded | failed | cancelled
//
// Terminal states never transition.
func ValidRunTransition(from, to RunStatus) bool {
	switch from {
	case RunStatusPending:
		return to == RunStatusRunning || to == RunStatusCancelled
	case RunStatusRunning:
		return to.Terminal()
	default:
		return false
	}
}

// StageStatus enumerates the lifecycle states of a single pipeline stage.
//
// Unlike a run, a stage may be skipped (planned but not needed) or retried
// (failed -> running starts a new attempt). Succeeded and skipped stages are
// terminal: a stage that produced its output is never re-run within the same
// run, and a stage that was deliberately skipped stays skipped (re-running it
// belongs to a new run, which is a resume/plan-mutation concern outside the
// v0.5 contract).
type StageStatus string

// Stage status values.
const (
	// StageStatusPending means the stage is planned but has not started.
	StageStatusPending StageStatus = "pending"

	// StageStatusSkipped means the stage was planned but is not needed for
	// this run (e.g. discovery skipped because explicit URLs were provided).
	StageStatusSkipped StageStatus = "skipped"

	// StageStatusRunning means the stage is executing its current attempt.
	StageStatusRunning StageStatus = "running"

	// StageStatusSucceeded means the stage completed its latest attempt and
	// produced its outputs.
	StageStatusSucceeded StageStatus = "succeeded"

	// StageStatusFailed means the stage's latest attempt failed. A new
	// attempt may be started (failed -> running).
	StageStatusFailed StageStatus = "failed"

	// StageStatusCancelled means the stage's latest attempt was stopped
	// before completion (e.g. the run was cancelled).
	StageStatusCancelled StageStatus = "cancelled"
)

// AllStageStatuses lists every valid stage status.
var AllStageStatuses = []StageStatus{
	StageStatusPending,
	StageStatusSkipped,
	StageStatusRunning,
	StageStatusSucceeded,
	StageStatusFailed,
	StageStatusCancelled,
}

// Valid reports whether the stage status is a known value.
func (s StageStatus) Valid() bool {
	for _, v := range AllStageStatuses {
		if s == v {
			return true
		}
	}
	return false
}

// Terminal reports whether the status is a final state. All terminal
// statuses have no transitions out of them, with one documented exception:
// a failed stage may be retried (failed -> running), which starts a new
// attempt. See ValidStageTransition.
func (s StageStatus) Terminal() bool {
	return s == StageStatusSucceeded || s == StageStatusFailed || s == StageStatusSkipped || s == StageStatusCancelled
}

// String returns the string representation of the status.
func (s StageStatus) String() string {
	return string(s)
}

// ValidStageTransition reports whether a stage may move from one status to
// another within the same run.
//
// Valid transitions:
//   - pending -> running | skipped | cancelled
//   - running -> succeeded | failed | cancelled
//   - failed  -> running (retry; starts a new attempt)
//
// Succeeded, skipped, and cancelled stages are terminal.
func ValidStageTransition(from, to StageStatus) bool {
	switch from {
	case StageStatusPending:
		return to == StageStatusRunning || to == StageStatusSkipped || to == StageStatusCancelled
	case StageStatusRunning:
		// Skipped is a terminal *state* but not reachable mid-flight: a stage
		// that started may only succeed, fail, or be cancelled.
		return to == StageStatusSucceeded || to == StageStatusFailed || to == StageStatusCancelled
	case StageStatusFailed:
		return to == StageStatusRunning
	default:
		return false
	}
}

// ValidAttemptStatus reports whether a status may appear on a StageAttempt.
//
// An attempt exists only once a stage has started running, so pending and
// skipped are not valid attempt statuses.
func ValidAttemptStatus(s StageStatus) bool {
	return s == StageStatusRunning || s == StageStatusSucceeded || s == StageStatusFailed || s == StageStatusCancelled
}

// ValidAttemptTransition reports whether an attempt may move from one status
// to another. An attempt runs to a single terminal outcome; there are no
// transitions out of terminal states (a retry is a new attempt).
func ValidAttemptTransition(from, to StageStatus) bool {
	if from != StageStatusRunning {
		return false
	}
	return to == StageStatusSucceeded || to == StageStatusFailed || to == StageStatusCancelled
}
