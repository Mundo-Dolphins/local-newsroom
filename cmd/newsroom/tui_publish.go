package main

// The TUI publication screen (v0.6, issue #122) — the newsroom app's
// implementation of the tui.PublicationService interface.
//
// The TUI package renders and tracks the screen; this file wires it to the
// real publication stack (engine, orchestrator, editorial preset) through
// the same shared flow the `publish` command uses (publish_flow.go). The
// TUI adds no publication logic of its own:
//
//   - PublicationState projects the run, the readiness gate, the preset,
//     and the plan set (with rehearsals, receipts, and remediation) into
//     the display-safe DTO;
//   - ApprovePublication validates and records an explicit TUI approval
//     for the actionable plans (bound to the exact artifact digest), then
//     starts the publication cycle as the app's in-flight publication;
//   - ResumePublication re-runs a failed or interrupted plan under its
//     existing effective approvals;
//   - CancelPublication aborts the in-flight cycle (the engine persists
//     the interrupted state; a later resume finishes the remainder);
//   - PublicationDone / PublicationInFlight let the TUI poller know when
//     the state changed.
//
// A stray TUI render path cannot publish: only an explicit, validated,
// persisted approval of the exact bytes authorizes a cycle.

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/editorial"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publish"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publish/presets"
	"github.com/Mundo-Dolphins/local-newsroom/internal/review"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/tui"
)

// publicationServices is the resolved publication stack for one run: the
// engine, the orchestrator, the editorial preset bridge, the readiness
// determination, and the plan views. The TUI publication screen renders a
// projection of it (publicationStateOf).
type publicationServices struct {
	id             run.RunID
	eng            *publish.Engine
	orch           *publish.Orchestrator
	m              *run.Manifest
	bundle         *review.Bundle
	preset         *editorial.Preset // nil when the run has no publication preset
	bundleRoot     string
	pub            *presets.Preset // nil when no (usable) preset
	noPresetReason string          // why there is no preset ("" when there is one)
	views          []planView
	warnings       []string
	ready          bool
	readyReason    string
}

// newPublicationServices resolves the publication stack for a run.
//
// There are no command-line flags in the TUI: the preset comes from the
// run's recorded publication or the configured default, and the target set
// is the preset's full destination list.
//
// With opts.Prepare the canonical content is persisted and any missing
// plan is created — only for a READY run (the gate is evaluated first; a
// not-ready run never gets a plan). With opts.Preview the zero-side-effect
// rehearsal runs. Otherwise the views are read from the run's persisted
// plans (their rehearsals, receipts, and errors as recorded on the plan).
func (a *tuiApp) newPublicationServices(ctx context.Context, id run.RunID, opts tui.PublicationOptions) (*publicationServices, error) {
	m, _, err := a.store.LoadManifest(id)
	if err != nil {
		return nil, fmt.Errorf("loading run %s: %w", id, err)
	}
	bundle, err := review.Build(a.store, m)
	if err != nil {
		return nil, fmt.Errorf("building the review bundle for run %s: %w", id, err)
	}
	eng := publish.New(a.store)
	orch := publish.NewOrchestrator(eng, id, bundle)

	svcs := &publicationServices{id: id, eng: eng, orch: orch, m: m, bundle: bundle}
	onWarn := func(format string, args ...any) {
		svcs.warnings = append(svcs.warnings, fmt.Sprintf(format, args...))
	}

	preset, root, err := resolvePublicationPreset(m, "", "")
	if err != nil {
		svcs.noPresetReason = err.Error()
	} else if preset == nil {
		svcs.noPresetReason = "no publication preset is selected for this run (editorial.publication is unset and the run recorded none)"
	} else {
		svcs.preset, svcs.bundleRoot = preset, root
		pub, perr := presets.New(preset, root, id, a.store, presets.WithFactory(publisherFactory))
		if perr != nil {
			svcs.noPresetReason = fmt.Sprintf("publication preset %q: %v", preset.Name, perr)
		} else {
			svcs.pub = pub
		}
	}

	var existing *publication.Plan
	if !opts.Prepare {
		if p, perr := eng.LatestPlan(id); perr == nil {
			existing = p
		}
	}
	ready, reason, err := publishReadiness(orch, m, bundle, svcs.pub, existing)
	if err != nil {
		return nil, fmt.Errorf("evaluating readiness for run %s: %w", id, err)
	}
	svcs.ready, svcs.readyReason = ready, reason

	if svcs.pub == nil {
		return svcs, nil
	}

	selected, err := svcs.pub.Select()
	if err != nil {
		return nil, fmt.Errorf("selecting preset destinations: %w", err)
	}
	usable, err := selectUsableDestinations(svcs.pub, selected, nil, id, onWarn)
	if err != nil {
		return nil, fmt.Errorf("%v", err)
	}
	if len(usable) == 0 {
		return svcs, nil
	}

	if opts.Prepare {
		if !ready {
			return svcs, nil // not-ready runs stop before any plan exists
		}
		svcs.views, err = preparePlanSet(ctx, eng, orch, id, bundle, svcs.pub, usable, true, opts.Preview, onWarn)
		if err != nil {
			return nil, fmt.Errorf("preparing the publication plan: %w", err)
		}
		return svcs, nil
	}

	// The read path projects the run's plans for the current content; a
	// destination whose content no longer matches any plan holds only its
	// latest plan (stale content: resumable or replannable, never hidden).
	for _, d := range usable {
		plan, kind, err := planForContent(eng, id, d)
		if err != nil {
			return nil, err
		}
		if plan == nil {
			plan, err = latestPlanForDestination(eng, id, d.ContentPath())
			if err != nil {
				return nil, err
			}
			if plan == nil {
				continue
			}
			kind = kindReused
		}
		v := planView{kind: kind, plan: plan, dest: d}
		// The summary is pure (no publication, no state change): it adds
		// the engine's remediation steps for unfinished plans, which the
		// persisted plan document does not carry.
		if s, serr := orch.Summary(ctx, v.plan.ID); serr == nil {
			v.sum = s
		}
		svcs.views = append(svcs.views, v)
	}
	return svcs, nil
}

// latestPlanForDestination finds the newest plan for a destination's
// content path regardless of digest (the read view keeps showing the run's
// plans even when the content changed after they were built).
func latestPlanForDestination(eng *publish.Engine, id run.RunID, path string) (*publication.Plan, error) {
	ids, err := eng.ListPlanIDs(id)
	if err != nil {
		return nil, err
	}
	var latest *publication.Plan
	for _, pid := range ids {
		p, err := eng.Plan(id, pid)
		if err != nil {
			continue
		}
		if p.Artifact.Path != path {
			continue
		}
		if latest == nil || p.UpdatedAt.Before(latest.UpdatedAt) {
			latest = p
		}
	}
	return latest, nil
}

// publicationStateOf projects the resolved publication stack into the TUI's
// display DTO.
func (a *tuiApp) publicationStateOf(svcs *publicationServices) *tui.PublicationState {
	m := svcs.m
	st := &tui.PublicationState{
		RunID:          string(m.ID),
		Topic:          m.Topic,
		RunStatus:      string(m.Status),
		Ready:          svcs.ready,
		ReadyReason:    svcs.readyReason,
		FinalCheck:     string(svcs.bundle.FinalCheckStatus),
		Findings:       len(svcs.bundle.Findings),
		NoPresetReason: svcs.noPresetReason,
	}
	if svcs.pub != nil {
		st.PresetName = svcs.pub.Name
		st.PresetHash = svcs.pub.Hash
	}

	for i := range svcs.views {
		st.Plans = append(st.Plans, a.planViewToDTO(&svcs.views[i]))
	}

	// Approvable: a READY run with a preset and at least one plan that
	// still needs an approval.
	if svcs.ready && svcs.pub != nil && svcs.noPresetReason == "" {
		n := 0
		for i := range svcs.views {
			v := &svcs.views[i]
			if v.kind == kindAlreadyPublished {
				continue
			}
			if !planApproved(v.plan) {
				n++
			}
		}
		switch {
		case len(svcs.views) == 0:
			st.ApproveBlocker = "nothing to publish: no destination of the preset can be prepared for this run"
		case n == 0:
			st.ApproveBlocker = "every plan already holds an effective approval or is fully published: nothing new to approve"
		default:
			st.Approvable = true
		}
	} else {
		switch {
		case svcs.noPresetReason != "":
			st.ApproveBlocker = "no publication preset: " + svcs.noPresetReason
		case !svcs.ready:
			st.ApproveBlocker = "the run is not ready for publication: " + svcs.readyReason
		}
	}

	// Resumable: at least one failed, partially succeeded, or interrupted
	// plan — and every such plan must hold an effective approval (a resume
	// is not a new publication: it never grants a new approval).
	resumable := false
	for i := range svcs.views {
		v := &svcs.views[i]
		switch v.plan.Status {
		case publication.StatusFailed, publication.StatusPartiallySucceeded, publication.StatusPublishing:
			resumable = true
			if !planApproved(v.plan) {
				st.ResumeBlocker = fmt.Sprintf("plan %s holds no effective approval (artifact %s): approve it first", v.plan.ID, v.plan.Artifact.Digest)
			}
		}
	}
	if !resumable {
		st.ResumeBlocker = ""
	}
	st.Resumable = resumable

	for i := range st.Plans {
		if st.Plans[i].Interrupted {
			st.Interrupted = true
			break
		}
	}
	return st
}

// planViewToDTO projects one plan view into the TUI's publication-plan
// DTO.
//
// The persisted plan document is the source of truth; when the view holds
// a preview or a cycle summary it enriches the mapping (fresh rehearsals,
// the interruption marker, and the remediation steps).
func (a *tuiApp) planViewToDTO(v *planView) tui.PublicationPlan {
	p := v.plan
	out := tui.PublicationPlan{
		PlanID:         string(p.ID),
		Status:         string(p.Status),
		Kind:           v.kind.label(),
		ArtifactPath:   p.Artifact.Path,
		ArtifactDigest: string(p.Artifact.Digest),
		ArtifactSize:   p.Artifact.Size,
		Reason:         p.TerminalReason,
	}
	if v.dest != nil {
		out.Key = v.dest.Key
		if v.dest.Target.Destination != "" {
			out.Destination = v.dest.Target.Destination
		}
		out.Facts = v.dest.Facts
	}
	if len(p.Approvals) > 0 {
		la := &p.Approvals[len(p.Approvals)-1]
		out.ApprovalBy = la.ApprovedBy
		out.ApprovalMethod = string(la.Method)
		out.ApprovalStatus = string(la.Status)
		out.ApprovalNote = la.Note
	}
	out.ApprovalEffective = p.EffectiveApproval() != nil
	if !p.Status.Terminal() {
		// Freshness is checked live against the disk (a plain content read,
		// no rehearsal, no state change): a plan whose content changed since
		// its baseline must say so, whether or not a rehearsal ran.
		out.ContentStale, out.ContentUnavailable = a.contentFreshness(p)
	}
	for i := range p.Targets {
		out.Targets = append(out.Targets, publicationTargetToDTO(&p.Targets[i], nil))
	}
	for _, w := range p.Warnings {
		out.Warnings = append(out.Warnings, warningText(w))
	}
	for _, e := range p.Errors {
		out.Errors = append(out.Errors, errorText(e))
	}
	if p.Status == publication.StatusPublishing {
		// The plan's own interrupt marker carries the cause.
		for _, w := range p.Warnings {
			if w.Code == "cycle-interrupted" {
				out.Interrupted = true
				if out.Reason == "" {
					out.Reason = w.Message
				}
				break
			}
		}
	}
	if v.sum != nil {
		out.Status = string(v.sum.Status)
		out.Interrupted = v.sum.Interrupted
		if v.sum.Reason != "" {
			out.Reason = v.sum.Reason
		}
		for i := range p.Targets {
			if ts := targetSummaryFor(v.sum, p.Targets[i].ID); ts != nil {
				out.Targets[i] = publicationTargetToDTO(&p.Targets[i], ts)
			}
		}
		for _, r := range v.sum.Remediation {
			out.Remediation = append(out.Remediation, tui.PublicationRemediation{
				Kind:     string(r.Kind),
				TargetID: string(r.TargetID),
				Action:   r.Action,
				Detail:   r.Detail,
				URI:      r.URI,
				RecordID: r.RecordID,
			})
		}
		for _, w := range v.sum.Warnings {
			out.Warnings = append(out.Warnings, warningText(w))
		}
		for _, e := range v.sum.Errors {
			out.Errors = append(out.Errors, errorText(e))
		}
	}
	return out
}

// contentFreshness checks the plan's baseline content against what is on
// disk right now (a plain read, no rehearsal, no state change) and reports
// (stale, unavailable).
func (a *tuiApp) contentFreshness(p *publication.Plan) (bool, bool) {
	data, err := a.store.LoadArtifact(p.RunID, p.Artifact.Path)
	if err != nil {
		return false, true
	}
	return !p.Artifact.Digest.Matches(string(publication.DigestOf(data))), false
}

// targetSummaryFor finds the summary entry for one target.
func targetSummaryFor(sum *publish.Summary, id publication.TargetID) *publish.TargetSummary {
	for i := range sum.Targets {
		if sum.Targets[i].ID == id {
			return &sum.Targets[i]
		}
	}
	return nil
}

// publicationTargetToDTO projects one target into the TUI's target DTO:
// the persisted target record first, enriched by the summary when one is
// available (it carries the publisher's rehearsal metadata and the
// engine's per-target state).
func publicationTargetToDTO(t *publication.PublicationTarget, s *publish.TargetSummary) tui.PublicationTarget {
	out := tui.PublicationTarget{
		ID:          string(t.ID),
		Name:        t.Name,
		Type:        string(t.Type),
		Provider:    t.Provider,
		Destination: t.Destination,
		Status:      string(t.Status),
		SkipReason:  t.SkipReason,
		Attempts:    len(t.Attempts),
	}
	if t.DryRun != nil {
		ok := t.DryRun.Outcome == publication.DryRunOutcomeOK
		out.PreviewOK = &ok
		out.PreviewDetails = t.DryRun.Details
		for _, issue := range t.DryRun.Issues {
			out.PreviewIssues = append(out.PreviewIssues, issue.Message)
		}
	}
	if t.Receipt != nil {
		out.ReceiptURI = t.Receipt.ExternalURI
		out.ReceiptRecordID = t.Receipt.ExternalRecordID
	}
	if t.Error != nil {
		out.Error = errorText(*t.Error)
	}
	if s != nil {
		if s.Status != "" {
			out.Status = string(s.Status)
		}
		if s.SkipReason != "" {
			out.SkipReason = s.SkipReason
		}
		if s.Preview != nil {
			ok := s.Preview.OK
			out.PreviewOK = &ok
			if s.Preview.Details != "" {
				out.PreviewDetails = s.Preview.Details
			}
			if s.Preview.Metadata != nil {
				out.PreviewMetadata = s.Preview.Metadata
			}
			for _, issue := range s.Preview.Issues {
				out.PreviewIssues = append(out.PreviewIssues, issue.Message)
			}
		}
		if s.Receipt != nil {
			out.ReceiptURI = s.Receipt.ExternalURI
			out.ReceiptRecordID = s.Receipt.ExternalRecordID
		}
		if s.Error != nil {
			out.Error = errorText(*s.Error)
		}
		if s.Attempts > 0 {
			out.Attempts = s.Attempts
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// tui.PublicationService implementation
// ---------------------------------------------------------------------------

// PublicationState implements tui.PublicationService.
func (a *tuiApp) PublicationState(ctx context.Context, runID string, opts tui.PublicationOptions) (*tui.PublicationState, error) {
	id := run.RunID(runID)
	svcs, err := a.newPublicationServices(ctx, id, opts)
	if err != nil {
		return nil, err
	}
	return a.publicationStateOf(svcs), nil
}

// ApprovePublication implements tui.PublicationService.
//
// It runs synchronously (fast, local work) and refuses — with a specific
// reason — when anything blocks a valid approval: a not-ready run, a
// missing preset, changed content, or a run/publication cycle already in
// flight. When everything passes it records an explicit TUI approval for
// every actionable plan (bound to the exact artifact digest) and starts
// the publication cycle asynchronously.
func (a *tuiApp) ApprovePublication(ctx context.Context, runID string) error {
	a.mu.Lock()
	if a.runningID != "" {
		a.mu.Unlock()
		return fmt.Errorf("a run is already in progress (%s): wait for it to finish first", a.runningID)
	}
	if a.pubRunningID != "" {
		a.mu.Unlock()
		return fmt.Errorf("a publication is already in progress for run %s: wait for it to finish or cancel it", a.pubRunningID)
	}
	a.mu.Unlock()

	id := run.RunID(runID)
	approver, err := resolveApprover("")
	if err != nil {
		return err
	}

	svcs, err := a.newPublicationServices(ctx, id, tui.PublicationOptions{})
	if err != nil {
		return fmt.Errorf("resolving the publication for run %s: %w", id, err)
	}
	if svcs.noPresetReason != "" {
		return fmt.Errorf("nothing to publish: %s", svcs.noPresetReason)
	}
	if !svcs.ready {
		return fmt.Errorf("run %s is not ready for publication: %s", id, svcs.readyReason)
	}

	// Validate and approve every actionable plan against the current
	// on-disk bytes: the engine re-reads the artifact and refuses (fail
	// closed) when it no longer matches the plan's baseline.
	type pendingApproval struct {
		planID publication.PublicationID
		digest publication.Digest
		name   string
	}
	var pending []pendingApproval
	for i := range svcs.views {
		v := &svcs.views[i]
		if v.kind == kindAlreadyPublished {
			continue
		}
		if planApproved(v.plan) {
			continue // an effective approval is already on record
		}
		pv, err := svcs.orch.Preview(ctx, v.plan.ID)
		if err != nil {
			return fmt.Errorf("preparing approval for plan %s: %w", v.plan.ID, err)
		}
		switch {
		case pv.ContentUnavailable:
			return fmt.Errorf("plan %s cannot be approved: its content %s is missing from the run directory", v.plan.ID, pv.Artifact.Path)
		case pv.ContentStale:
			return fmt.Errorf("plan %s cannot be approved: the content changed after the plan was created (recorded %s, on disk now %s): re-preview and approve the new content", v.plan.ID, v.plan.Artifact.Digest, pv.Digest)
		}
		pending = append(pending, pendingApproval{
			planID: v.plan.ID,
			digest: v.plan.Artifact.Digest,
			name:   destName(v.dest),
		})
	}
	if len(pending) == 0 {
		return fmt.Errorf("there is nothing new to approve: every plan already holds an effective approval or is fully published")
	}
	for _, p := range pending {
		_, _, err := svcs.orch.Approve(ctx, p.planID, publish.Approval{
			ApprovedBy:      approver,
			Method:          publication.ApprovalMethodTUI,
			Note:            "approved in the newsroom TUI",
			DigestConfirmed: p.digest,
		})
		if err != nil {
			return fmt.Errorf("approving plan %s (%s): %w", p.planID, p.name, err)
		}
	}

	return a.beginPubCycle(ctx, id, svcs)
}

// ResumePublication implements tui.PublicationService.
//
// It re-runs the publication cycle for the run's failed, partially
// succeeded, or interrupted plans — under the approvals they already hold
// (a resume is not a new publication, so it never grants a new approval;
// a plan without an effective approval is reported as a blocker).
func (a *tuiApp) ResumePublication(ctx context.Context, runID string) error {
	a.mu.Lock()
	if a.runningID != "" {
		a.mu.Unlock()
		return fmt.Errorf("a run is already in progress (%s): wait for it to finish first", a.runningID)
	}
	if a.pubRunningID != "" {
		a.mu.Unlock()
		return fmt.Errorf("a publication is already in progress for run %s: wait for it to finish or cancel it", a.pubRunningID)
	}
	a.mu.Unlock()

	id := run.RunID(runID)
	svcs, err := a.newPublicationServices(ctx, id, tui.PublicationOptions{})
	if err != nil {
		return fmt.Errorf("resolving the publication for run %s: %w", id, err)
	}
	if svcs.noPresetReason != "" {
		return fmt.Errorf("nothing to publish: %s", svcs.noPresetReason)
	}

	n := 0
	for i := range svcs.views {
		v := &svcs.views[i]
		switch v.plan.Status {
		case publication.StatusFailed, publication.StatusPartiallySucceeded, publication.StatusPublishing:
			if !planApproved(v.plan) {
				return fmt.Errorf("plan %s cannot resume: it holds no effective approval (artifact %s) — approve it first", v.plan.ID, v.plan.Artifact.Digest)
			}
			n++
		}
	}
	if n == 0 {
		return fmt.Errorf("there is no failed or interrupted plan to resume")
	}

	return a.beginPubCycle(ctx, id, svcs)
}

// CancelPublication implements tui.PublicationService.
func (a *tuiApp) CancelPublication() {
	a.mu.Lock()
	cancel := a.cancelPub
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// PublicationDone implements tui.PublicationService.
func (a *tuiApp) PublicationDone() <-chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pubDone
}

// PublicationInFlight implements tui.PublicationService.
func (a *tuiApp) PublicationInFlight() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pubRunningID
}

// beginPubCycle starts a publication cycle as the app's single in-flight
// publication (the mirror of beginCycle for run cycles). The cycle runs in
// a goroutine against a fresh cancellable context; pubDone closes when it
// reaches its terminal state.
func (a *tuiApp) beginPubCycle(ctx context.Context, id run.RunID, svcs *publicationServices) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pubRunningID != "" {
		return fmt.Errorf("a publication is already in progress for run %s: wait for it to finish or cancel it first", a.pubRunningID)
	}
	if a.runningID != "" {
		return fmt.Errorf("a run is already in progress (%s): wait for it to finish first", a.runningID)
	}
	pubCtx, cancel := context.WithCancel(ctx)
	a.pubCtx = pubCtx
	a.cancelPub = cancel
	a.pubDone = make(chan struct{})
	a.pubRunningID = string(id)
	a.pubErr = ""
	go func() {
		defer close(a.pubDone)
		defer cancel()
		if _, cerr := runPublicationCycle(pubCtx, svcs.eng, svcs.orch, svcs.pub, id, svcs.views, nil); cerr != nil {
			a.mu.Lock()
			a.pubErr = run.RedactSecrets(cerr.Error())
			a.mu.Unlock()
		}
		a.mu.Lock()
		if a.pubRunningID == string(id) {
			a.pubRunningID = ""
		}
		a.mu.Unlock()
	}()
	return nil
}

// PublicationCycleError reports why the last in-flight publication cycle
// stopped (redacted), or "" when it completed cleanly. The screen's plan
// blocks show the same outcome from the persisted state; this is the
// cycle-level cause (a refusal, an interruption, a cancelled context).
func (a *tuiApp) PublicationCycleError() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pubErr
}

// waitInFlightPublication cancels an in-flight publication (if any) and
// waits for it to persist its terminal state, bounded by runCycleGrace.
func (a *tuiApp) waitInFlightPublication() {
	a.mu.Lock()
	cancel, done, id := a.cancelPub, a.pubDone, a.pubRunningID
	a.mu.Unlock()
	if id == "" {
		return
	}
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(runCycleGrace):
		fmt.Fprintf(os.Stderr, "warning: publication for run %s was cancelled but did not finish within %s; its state is persisted in the workspace and can be resumed\n", id, runCycleGrace)
	}
}
