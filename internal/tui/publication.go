package tui

import "context"

// This file defines the TUI's controlled-publication surface (v0.6, #122).
//
// The TUI is a presentation layer: the concrete service (cmd/newsroom)
// owns every publication decision — the readiness gate, the preset, the
// plans, the approval record, and the publish cycle. The TUI projects that
// state through the read-only PublicationState DTO, renders it, and calls
// the explicit intent methods below after the user's deliberate
// confirmation. Nothing in this package performs publication work, and no
// publication credential crosses this boundary.

// PublicationOptions selects what the PublicationState projection does.
type PublicationOptions struct {
	// Prepare creates the run's publication plans from its publication
	// preset (a gate-checked state change: persistence plus plan
	// documents, never a publication). Entry into the publication screen
	// requests it; live refreshes while a cycle runs do not.
	Prepare bool

	// Preview re-runs the per-target dry-run rehearsal for every
	// actionable plan, so the user sees the exact bytes that would be
	// sent and any rehearsal refusal.
	Preview bool
}

// PublicationState is the full projection of one run's publication
// picture: readiness, preset, and one entry per destination plan (site
// before thread, in the preset's canonical order).
type PublicationState struct {
	// RunID, Topic, and RunStatus identify the run under review.
	RunID     string
	Topic     string
	RunStatus string

	// Ready is the pre-publication gate verdict (run succeeded + final
	// check passed + review READY) evaluated against the run's current
	// content; ReadyReason explains a refusal.
	Ready       bool
	ReadyReason string

	// FinalCheck is the run's final factual check verdict
	// (pass / fail / review; "" when the stage was not reached);
	// Findings is its actionable finding count.
	FinalCheck string
	Findings   int

	// PresetName and PresetHash identify the resolved publication preset
	// ("" / NoPresetReason when the run has no usable preset).
	PresetName     string
	PresetHash     string
	NoPresetReason string

	// Approvable drives the approve & publish action; ApproveBlocker
	// explains why it is unavailable.
	Approvable     bool
	ApproveBlocker string

	// Resumable drives the resume action (a failed or interrupted plan
	// that still has an effective approval); ResumeBlocker explains why
	// it is unavailable.
	Resumable     bool
	ResumeBlocker string

	// Interrupted is true when the run's latest publication cycle ended
	// in an interruption (the state is persisted and resumable).
	Interrupted bool

	// Plans is the per-destination plan set; empty when the run has
	// nothing planned (not ready, no preset, or none usable).
	Plans []PublicationPlan
}

// PublicationPlan is one plan (one canonical content, one destination)
// for the run: its persisted state, its latest rehearsal, and the cycle's
// receipts and remediation.
type PublicationPlan struct {
	// PlanID and Status describe the persisted plan document
	// ("" / "none" when no plan exists for the destination yet).
	PlanID string
	Status string

	// Kind is the plan's provenance for this projection: "new plan",
	// "reused from a previous preview", or
	// "already published (idempotent no-op)".
	Kind string

	// Key is the preset's destination key for this plan ("hugo" or
	// "bluesky"; "" when the plan exists but its destination is unknown).
	Key string

	// Destination is the target's destination (site URL, handle, ...).
	Destination string

	// Artifact facts ("" / zero when the plan has no baseline yet).
	ArtifactPath    string
	ArtifactDigest  string
	ArtifactSize    int64
	ArtifactVersion string
	// ContentStale is true when the on-disk content no longer matches
	// the plan's baseline digest (approval and publication would be
	// refused until the plan is re-baselined).
	ContentStale bool
	// ContentUnavailable is true when the baseline content file is gone.
	ContentUnavailable bool

	// Facts are the destination's display-only, non-secret facts (branch,
	// path, post count, site URL, handle, ...).
	Facts map[string]string

	// Approval is the plan's latest approval ("" fields when none);
	// ApprovalEffective is the engine's fail-closed verdict that the
	// approval covers the current content.
	ApprovalBy        string
	ApprovalMethod    string
	ApprovalStatus    string
	ApprovalNote      string
	ApprovalEffective bool

	// Interrupted is true when the plan's last cycle was interrupted
	// (context cancellation); Reason is the plan's terminal reason.
	Interrupted bool
	Reason      string

	// Targets are the plan's targets (exactly one per destination) with
	// their latest state, rehearsal, receipt, and failure.
	Targets []PublicationTarget

	// Warnings and Errors are the engine's redacted diagnostics.
	Warnings []string
	Errors   []string

	// Remediation are the manual follow-up steps after a failed cycle
	// (empty when the cycle is clean or still running).
	Remediation []PublicationRemediation
}

// PublicationTarget is one destination target of a plan with its latest
// rehearsal (Preview*), receipt, and failure state.
type PublicationTarget struct {
	ID          string
	Name        string
	Type        string
	Provider    string
	Destination string
	DependsOn   []string

	// Status is the target's persisted state (planned / publishing /
	// succeeded / failed / ...); SkipReason explains a skipped target.
	Status     string
	SkipReason string
	Attempts   int

	// PreviewOK is nil when no rehearsal was possible; otherwise it is
	// the dry-run outcome (details and issues already redacted).
	PreviewOK       *bool
	PreviewDetails  string
	PreviewMetadata map[string]string
	PreviewIssues   []string

	// ReceiptURI / ReceiptRecordID are the publisher's receipt
	// ("" / zero when the target never succeeded).
	ReceiptURI      string
	ReceiptRecordID string

	// Error is the target's latest failure ("" when none).
	Error string
}

// PublicationRemediation is one manual step the operator should take
// after a failed or interrupted cycle.
type PublicationRemediation struct {
	Kind     string // dry_run_issue | receipt_missing | ...
	TargetID string
	Action   string // short imperative instruction
	Detail   string // redacted elaboration
	URI      string
	RecordID string
}

// PublicationService is the TUI's controlled-publication surface: a
// Service (the v0.5 capability) plus the v0.6 publication methods. The
// concrete service in cmd/newsroom implements both; tests provide a fake.
//
// PublicationState is a read-only projection (it may prepare plans and
// previews, never publish). ApprovePublication and ResumePublication
// perform the explicit intents only after the user's deliberate
// confirmation in the TUI; they record or reuse the approval, then run
// the publication cycle under the service's lifecycle (the in-flight
// bookkeeping exposed by CancelPublication, PublicationDone, and
// PublicationInFlight).
type PublicationService interface {
	Service

	// PublicationState projects the run's publication picture.
	PublicationState(ctx context.Context, runID string, opts PublicationOptions) (*PublicationState, error)

	// ApprovePublication approves every actionable plan that lacks an
	// effective approval (the user just confirmed in the TUI) and starts
	// the publication cycle for the run. It returns the synchronous part
	// of the work (resolution, preparation, approval recording) as an
	// error; the cycle itself runs asynchronously.
	ApprovePublication(ctx context.Context, runID string) error

	// ResumePublication re-runs the run's failed or interrupted plans
	// under their existing approvals. It refuses (returning an error)
	// when a resumable plan lacks an effective approval: re-approval is
	// a deliberate act the user must give through ApprovePublication.
	ResumePublication(ctx context.Context, runID string) error

	// CancelPublication stops the in-flight publication cycle (the
	// engine records the interruption; the state stays resumable).
	CancelPublication()

	// PublicationDone is closed when the in-flight cycle ends; nil when
	// no cycle is running.
	PublicationDone() <-chan struct{}

	// PublicationInFlight is the run ID of the in-flight publication
	// cycle ("" when none).
	PublicationInFlight() string
}
