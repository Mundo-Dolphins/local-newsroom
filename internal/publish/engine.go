package publish

import (
	"context"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// Engine drives idempotent publication cycles over the persisted publication
// plans of a run.
//
// The engine owns the persistence discipline: every state change is written
// through the store before the engine proceeds, so any point at which the
// process dies leaves a plan document whose status, target statuses,
// attempts, and receipts consistently describe what the outside world holds
// to the degree the publishers were able to prove it.
type Engine struct {
	store Store
	now   func() time.Time
}

// New creates an engine over a workspace store (workspace.Store satisfies
// Store). The wall clock supplies transition timestamps.
func New(store Store) *Engine {
	return &Engine{store: store, now: func() time.Time { return time.Now().UTC() }}
}

// NewWithClock creates an engine with an injected clock (tests use a
// deterministic one; the production engine is New).
func NewWithClock(store Store, now func() time.Time) *Engine {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Engine{store: store, now: now}
}

// Options selects the plan a Publish call operates on.
type Options struct {
	// PlanID names the plan explicitly. Empty means the run's latest plan
	// (the manifest summary, else the newest plan document on disk) — the
	// resolution every repeated "publish" command goes through, which is
	// what makes the command idempotent.
	PlanID publication.PublicationID
}

// Publish runs (or resumes) one publication cycle for a plan.
//
// The cycle is idempotent:
//
//   - a plan whose targets all succeeded (plan succeeded) is returned
//     unchanged, with no publisher invoked — a repeated publish command is
//     a no-op that reports the existing receipts;
//   - a failed or partially succeeded plan is retried: only the targets
//     that did not succeed are re-executed, each with its existing receipt
//     as the resume state (a partially posted thread posts only the
//     remaining posts; a git commit whose content is already committed is
//     re-confirmed, not duplicated);
//   - a plan stuck in publishing (crashed mid-cycle) first has its dangling
//     in-flight targets aborted, then continues with the rest;
//   - a plan with no effective approval (planned, or a stale/revoked
//     approval) is refused before any publisher is invoked.
//
// Before any target runs, the engine verifies the plan's content file still
// matches the plan's recorded artifact digest: a content file that changed
// after the baseline fails closed (ErrArtifactChanged) instead of
// publishing unapproved bytes, and a missing content file fails closed too
// (ErrArtifactMissing).
//
// Publishers are bound per target ID; a target that must execute but has no
// bound publisher fails the call before any state change (the bind-then-
// retry the error names). Publishers never see more than the approved
// artifact bytes plus the target's prior receipt.
//
// The plan document and the manifest's publication_state summary are
// persisted after every state change, so the plan on disk always reflects
// the last completed transition — including a crash between two of them.
func (e *Engine) Publish(ctx context.Context, runID run.RunID, publishers map[publication.TargetID]Publisher, opts Options) (*publication.Plan, error) {
	pctx, err := e.loadPlanContext(runID, opts.PlanID)
	if err != nil {
		return nil, err
	}
	m, plan := pctx.manifest, pctx.plan

	// --- Pre-flight: what would be published is exactly what was approved.
	content, err := e.store.LoadArtifact(runID, plan.Artifact.Path)
	if err != nil {
		if isNotExist(err) {
			return nil, fmt.Errorf("%w: %s (plan %s references it but it is gone from the run directory)", ErrArtifactMissing, plan.Artifact.Path, plan.ID)
		}
		return nil, fmt.Errorf("%w: cannot read %s: %v", ErrArtifactMissing, plan.Artifact.Path, err)
	}
	currentDigest := publication.DigestOf(content)
	if !plan.Artifact.Digest.Matches(string(currentDigest)) {
		return nil, fmt.Errorf("%w: %s currently hashes to %s but the plan baseline is %s; re-baseline and re-approve (RefreshArtifact) before publishing again",
			ErrArtifactChanged, plan.Artifact.Path, currentDigest, plan.Artifact.Digest)
	}

	// --- Plan-level state gates (before any target is touched).
	switch plan.Status {
	case publication.StatusSucceeded, publication.StatusSkipped:
		// Terminal, everything completed (or everything skipped): the cycle
		// is already done. Nothing is executed; the plan with its receipts
		// is the answer. This is the "repeated publish command" case.
		return plan, nil
	case publication.StatusCancelled:
		return nil, fmt.Errorf("%w: %s; re-publishing the artifact requires a new plan with a new approval", ErrPlanCancelled, plan.ID)
	case publication.StatusPlanned:
		if plan.EffectiveApproval() == nil {
			return nil, fmt.Errorf("%w: %s; a human must approve the current artifact first", ErrNotApproved, plan.ID)
		}
	}

	// An effective approval must cover the content about to go out, and the
	// recorded gate must still pass: the last line of defense before a
	// publisher is invoked (Plan.AuthorizesPublish re-checks per target).
	if plan.EffectiveApproval() == nil {
		if len(plan.Approvals) > 0 {
			return nil, fmt.Errorf("%w: %s (the recorded approval no longer matches the current artifact %s)", ErrStaleApproval, plan.ID, plan.Artifact.Digest)
		}
		return nil, fmt.Errorf("%w: %s has no approval", ErrNotApproved, plan.ID)
	}
	if !plan.Gate.Passing() {
		_, why := GateReady(plan.Gate)
		return nil, fmt.Errorf("%w: %s: %s", ErrGateFailed, plan.ID, why)
	}

	at := e.now()

	// --- Every target that will execute in this cycle needs a bound
	// publisher, checked BEFORE any state change so a configuration gap
	// cannot strand the cycle half-run. A target executes when it is
	// planned, failed, or partially succeeded; terminal targets never run
	// and dangling (publishing) targets are aborted, not re-executed, so
	// they need no binding on the resume path.
	var missing []string
	for _, t := range plan.Targets {
		switch t.Status {
		case publication.StatusPlanned, publication.StatusFailed, publication.StatusPartiallySucceeded:
			if _, ok := publishers[t.ID]; !ok {
				missing = append(missing, string(t.ID))
			}
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: %s (targets: %v)", ErrPublisherNotBound, plan.ID, missing)
	}

	// --- Open the cycle.
	switch plan.Status {
	case publication.StatusApproved:
		if err := plan.StartPublishing(at); err != nil {
			return nil, fmt.Errorf("starting publication cycle for %s: %w", plan.ID, err)
		}
	case publication.StatusFailed, publication.StatusPartiallySucceeded:
		if err := plan.Retry(at); err != nil {
			return nil, fmt.Errorf("retrying publication cycle for %s: %w", plan.ID, err)
		}
	case publication.StatusPublishing:
		// Crashed mid-cycle: every in-flight target is dangling — its
		// publisher call never returned, so the engine cannot know whether
		// (or how far) the external effect happened. The crash-recovery
		// rule: end the dangling attempts as cancelled (persisted), then
		// re-execute the targets that did not finish.
		for i := range plan.Targets {
			if plan.Targets[i].Status == publication.StatusPublishing {
				if err := plan.AbortTarget(plan.Targets[i].ID, at); err != nil {
					return nil, fmt.Errorf("aborting dangling target %q of %s: %w", plan.Targets[i].ID, plan.ID, err)
				}
			}
		}
	default:
		// Statuses handled above and terminal-but-not-done (cannot occur:
		// RefreshStatus only leaves non-terminal statuses for non-terminal
		// targets; a terminal plan with active targets is invalid and was
		// rejected by loadPlan's validation).
		return nil, fmt.Errorf("%w: plan %s is in state %q; refusing to act on a plan whose state cannot be interpreted", ErrPlanInvalid, plan.ID, plan.Status)
	}
	if err := e.persistPlan(m, plan); err != nil {
		return nil, err
	}

	// --- Execute the cycle, in declaration order, persisting after every
	// target transition.
	for i := range plan.Targets {
		if err := ctx.Err(); err != nil {
			return e.finishCycleAborted(m, plan, err)
		}
		if err := e.executeTarget(ctx, m, plan, &plan.Targets[i], content, currentDigest, publishers, at); err != nil {
			return nil, err
		}
	}

	// --- Close the cycle: aggregate the target states into the plan status.
	if err := plan.RefreshStatus(at); err != nil {
		return nil, fmt.Errorf("finalizing publication status of %s: %w", plan.ID, err)
	}
	if err := e.persistPlan(m, plan); err != nil {
		return nil, err
	}
	return plan, nil
}

// executeTarget runs one target of the current cycle.
//
// Terminal targets (succeeded, skipped, cancelled) are never executed — a
// successful external action is never repeated; only planned, failed, and
// partially_succeeded targets run. The target's dependencies are resolved
// first: a target whose dependency did not succeed in this cycle does not
// execute (its input was never produced).
func (e *Engine) executeTarget(ctx context.Context, m *run.Manifest, plan *publication.Plan, t *publication.PublicationTarget, content []byte, currentDigest publication.Digest, publishers map[publication.TargetID]Publisher, at time.Time) error {
	// Target already finished (a previous cycle, or this one): its receipt
	// is the proof; skip it entirely.
	switch t.Status {
	case publication.StatusSucceeded, publication.StatusSkipped, publication.StatusCancelled:
		return nil
	}

	// Dependencies: every target this one lists must have finished before
	// it may start. (A target only references targets declared before it
	// — the plan validator enforces existence and acyclicity — so by the
	// time the loop reaches this target, its dependencies have already
	// resolved in this cycle.)
	for _, dep := range t.DependsOn {
		d, ok := plan.Target(dep)
		if !ok {
			return fmt.Errorf("%w: target %q depends on unknown target %q", ErrPlanInvalid, t.ID, dep)
		}
		if d.Status == publication.StatusPublishing {
			// Dangling in-flight dependency (crash artifact): end it, then
			// treat it as not-done.
			if err := plan.AbortTarget(dep, at); err != nil {
				return fmt.Errorf("aborting dangling dependency %q: %w", dep, err)
			}
			if perr := e.persistPlan(m, plan); perr != nil {
				return perr
			}
			d, _ = plan.Target(dep)
		}
		switch d.Status {
		case publication.StatusSucceeded, publication.StatusSkipped:
			// done (skipped dependencies are allowed: the dependent simply
			// proceeds without their effect — the skip was deliberate)
		default:
			// The dependent cannot run: it never produced its input. The
			// target machine has no planned->failed transition, so the
			// block is recorded through a start-and-immediate-fail: the
			// attempt history shows exactly why, and the target stays
			// retryable (a later cycle runs it once its dependency has
			// succeeded).
			if err := plan.StartTarget(t.ID, at); err != nil {
				return fmt.Errorf("blocking target %q of %s on an incomplete dependency: %w", t.ID, plan.ID, err)
			}
			if err := e.failTarget(m, plan, t, at, "dependency-incomplete",
				fmt.Sprintf("dependency %q did not complete (status %s)", dep, d.Status)); err != nil {
				return err
			}
			return nil
		}
	}

	// Publishers may be partial-success aware, but the plan still requires
	// the approval binding for the exact target before any external call.
	if err := plan.AuthorizesPublish(t.ID, string(currentDigest)); err != nil {
		// A still-active target that cannot be authorized (stale approval
		// edge, gate drift) is recorded as failed rather than silently
		// skipped: the cycle ends with the target unfulfilled, visible in
		// the persisted record.
		code := "authorization-failed"
		if !plan.Gate.Passing() {
			code = "gate-not-passing"
		}
		return e.failTarget(m, plan, t, at, code, run.RedactSecrets(err.Error()))
	}

	if err := plan.StartTarget(t.ID, at); err != nil {
		return fmt.Errorf("starting target %q of %s: %w", t.ID, plan.ID, err)
	}
	if err := e.persistPlan(m, plan); err != nil {
		return err
	}

	// The external side effect: exactly the approved bytes, plus whatever
	// the target proved in a previous attempt.
	receipt, perr := publishers[t.ID].Publish(ctx, content, t.Receipt)

	var persistErr error
	switch {
	case perr == nil && receipt == nil:
		// The publisher reported success but proved nothing: a success
		// without a receipt is not a success. Record the failure so the
		// record says exactly what happened.
		persistErr = e.failTarget(m, plan, t, at, "no-receipt", "the publisher reported success without a receipt")
	case perr == nil:
		if err := plan.SucceedTarget(t.ID, receipt, at); err != nil {
			// The publisher claims success but the receipt is unacceptable
			// (invalid, or its content digest differs from the approved
			// bytes): the target must not be marked succeeded. It is
			// recorded as failed and the receipt discarded; the plan's
			// warning tells a human exactly why.
			persistErr = e.failTarget(m, plan, t, at, "receipt-mismatch", run.RedactSecrets(err.Error()))
			if werr := plan.AddWarning("receipt-rejected",
				fmt.Sprintf("target %q: the publisher's receipt was rejected (%s); the target was recorded as failed", t.ID, run.RedactSecrets(err.Error())), at); werr != nil {
				return fmt.Errorf("recording receipt-rejection warning: %w", werr)
			}
		}
	case receipt != nil:
		// The publisher confirmed a partial effect and returned an error:
		// record what it proved so the next cycle resumes from it.
		if err := plan.PartialTarget(t.ID, receipt, at); err != nil {
			// The partial receipt did not validate (digest mismatch, invalid
			// form): the target fails; the next cycle retries from scratch.
			persistErr = e.failTarget(m, plan, t, at, "publish-interrupted", run.RedactSecrets(perr.Error()))
		}
	default:
		// Nothing was confirmed: the target fails and is retried from
		// scratch next cycle.
		persistErr = e.failTarget(m, plan, t, at, "publish-failed", run.RedactSecrets(perr.Error()))
	}
	if persistErr != nil {
		return persistErr
	}
	return e.persistPlan(m, plan)
}

// finishCycleAborted ends a cycle that was interrupted before completion
// (context cancellation or a state-recording failure): dangling in-flight
// targets are aborted, the plan is left in publishing with a warning, and
// the persisted plan is the resume point.
func (e *Engine) finishCycleAborted(m *run.Manifest, plan *publication.Plan, cause error) (*publication.Plan, error) {
	at := e.now()
	for i := range plan.Targets {
		if plan.Targets[i].Status == publication.StatusPublishing {
			if aerr := plan.AbortTarget(plan.Targets[i].ID, at); aerr != nil {
				return nil, fmt.Errorf("aborting target %q after interruption: %w", plan.Targets[i].ID, aerr)
			}
		}
	}
	if werr := plan.AddWarning("cycle-interrupted", run.RedactSecrets(cause.Error()), at); werr != nil {
		return nil, fmt.Errorf("recording interruption warning: %w", werr)
	}
	if perr := e.persistPlan(m, plan); perr != nil {
		return nil, perr
	}
	return plan, fmt.Errorf("publication cycle for %s interrupted: %w (in-flight targets aborted; resume by calling Publish again)", plan.ID, cause)
}

// failTarget records a target failure with the plan and manifest persisted,
// and returns nil (a recorded failure is a valid, resumable state).
func (e *Engine) failTarget(m *run.Manifest, plan *publication.Plan, t *publication.PublicationTarget, at time.Time, code, message string) error {
	if err := plan.FailTarget(t.ID, code, message, at); err != nil {
		return fmt.Errorf("recording failure for target %q of %s: %w", t.ID, plan.ID, err)
	}
	return e.persistPlan(m, plan)
}
