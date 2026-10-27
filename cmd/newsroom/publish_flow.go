package main

// The v0.6 publication flow shared by the two surfaces: the `publish` CLI
// command (publish_cmd.go) and the TUI publication screen (tui_publish.go).
//
// The engine and the orchestrator (internal/publish) hold ALL publication
// business logic: planning, the readiness gate, approvals, cycle
// semantics, receipts, and remediation. This file owns only the surface
// wiring both callers need in common:
//
//   - resolving the editorial preset (the CLI passes its --publication /
//     --bundle-root flags; the TUI passes none, so the run's recorded
//     preset or the configured default applies),
//   - the approving principal,
//   - the readiness evaluation,
//   - the plan set: one plan per destination, resolved from the run's
//     existing plans for the same content, created only where needed,
//     with the zero-side-effect dry-run rehearsal,
//   - the publication cycle: canonical order, a failed destination stops
//     the rest, plus the documented exit-code mapping,
//   - the plan→view projection both presentations render.
//
// Nothing here touches a terminal: the CLI's text/JSON printers and the
// TUI's DTO mapping are surface-specific and live in their own files.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/config"
	"github.com/Mundo-Dolphins/local-newsroom/internal/editorial"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publish"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publish/presets"
	"github.com/Mundo-Dolphins/local-newsroom/internal/review"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// publisherFactory builds the executable publishers for the preset's
// destinations. Production: the real adapters (git publish, Bluesky API).
// Tests substitute fakes, so the CLI and the TUI are verified without a
// checkout or a network.
var publisherFactory presets.Factory = presets.DefaultFactory

// planKind records where one plan in the report came from.
type planKind int

const (
	// kindNew: created by this invocation.
	kindNew planKind = iota
	// kindReused: created by an earlier invocation for the same content
	// (a re-preview must not churn plan IDs).
	kindReused
	// kindAlreadyPublished: the same content fully published before;
	// the plan is shown as an idempotent no-op.
	kindAlreadyPublished
)

func (k planKind) label() string {
	switch k {
	case kindReused:
		return "reused from a previous preview"
	case kindAlreadyPublished:
		return "already published (idempotent no-op)"
	default:
		return "new plan"
	}
}

// planView pairs one plan with its destination, its preview, and (after
// the cycle) its summary, the unit every presentation path renders.
type planView struct {
	kind  planKind
	plan  *publication.Plan
	dest  *presets.Destination
	pv    *publish.ApprovalSummary
	pvErr error
	sum   *publish.Summary
}

// destinationForPlan finds the preset destination that owns a plan's first
// target (presets are one plan per destination).
func destinationForPlan(pub *presets.Preset, plan *publication.Plan) *presets.Destination {
	if pub == nil || len(plan.Targets) == 0 {
		return nil
	}
	for _, d := range pub.Destinations {
		if string(d.Target.ID) == string(plan.Targets[0].ID) {
			return d
		}
	}
	return nil
}

// resolvePublicationPreset resolves the editorial preset a publication uses
// for a run.
//
// Precedence: the explicit name (the CLI's --publication flag; the TUI
// passes none), then the preset the run recorded (a run started with a
// publication keeps its preset for the whole lifecycle), then the
// configuration chain (editorial.publication + editorial.bundle_root).
//
// It returns (nil, "", nil) when no preset is selected: that is not an
// error (a run without a preset simply has nothing to publish, which the
// caller reports).
func resolvePublicationPreset(m *run.Manifest, flagName, flagRoot string) (*editorial.Preset, string, error) {
	name := strings.TrimSpace(flagName)
	if name == "" && m.Publication != nil {
		name = m.Publication.ID
	}
	if name == "" {
		if v, _, err := appConfig.GetString(config.SettingEditorialPublication, config.Override{}); err == nil {
			name = v
		}
	}
	if name == "" {
		return nil, "", nil
	}
	root := strings.TrimSpace(flagRoot)
	if root == "" && m.Publication != nil {
		root = m.Publication.BundlePath
	}
	if root == "" {
		v, _, err := appConfig.GetString(config.SettingEditorialBundle, config.Override{})
		if err == nil {
			root = v
		}
	}
	if root == "" {
		return nil, "", fmt.Errorf("publication %q requires an editorial bundle root: pass --bundle-root or set editorial.bundle_root", name)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, "", fmt.Errorf("resolving the editorial bundle root: %v", err)
	}
	loader, err := editorial.NewLoader(editorial.Config{Root: abs})
	if err != nil {
		return nil, "", fmt.Errorf("loading the editorial bundle: %v", err)
	}
	preset, err := loader.LoadPreset(name)
	if err != nil {
		return nil, "", fmt.Errorf("publication preset %q: %v", name, err)
	}
	return preset, loader.Root(), nil
}

// resolveApprover returns the approving principal: an explicit value, else
// the current OS user.
func resolveApprover(flag string) (string, error) {
	if a := strings.TrimSpace(flag); a != "" {
		return a, nil
	}
	if u, err := user.Current(); err == nil && strings.TrimSpace(u.Username) != "" {
		return strings.TrimSpace(u.Username), nil
	}
	if name := strings.TrimSpace(os.Getenv("USER")); name != "" {
		return name, nil
	}
	if name := strings.TrimSpace(os.Getenv("LOGNAME")); name != "" {
		return name, nil
	}
	return "", fmt.Errorf("no approver identity could be determined: pass --approver <id>")
}

// publishReadiness evaluates the pre-publication gate.
//
// With an existing plan it reads the plan's content file (the baseline the
// plan was built from); for a new publication it evaluates the first
// preparable destination's canonical content from memory; with no content
// at all it reports the gate from the manifest alone (never ready).
func publishReadiness(orch *publish.Orchestrator, m *run.Manifest, bundle *review.Bundle, pub *presets.Preset, existing *publication.Plan) (bool, string, error) {
	if existing != nil {
		rc, err := orch.Ready(context.Background(), publish.CreatePlanRequest{ArtifactPath: existing.Artifact.Path})
		if err != nil {
			return false, "", err
		}
		if rc.ContentMissing {
			return false, "not ready: " + rc.Reason, nil
		}
		return rc.Ready, rc.Reason, nil
	}
	if pub != nil {
		for _, d := range pub.Destinations {
			if d.ContentErr != nil || d.Content == nil {
				continue
			}
			gate := publish.BuildGateEvidence(m, bundle, d.Content, time.Now())
			ready, reason := publish.GateReady(gate)
			return ready, reason, nil
		}
	}
	gate := publish.BuildGateEvidence(m, bundle, nil, time.Now())
	ready, reason := publish.GateReady(gate)
	return ready, reason, nil
}

// planForContent resolves which plan covers a destination's current
// canonical content: the run's plan for the same content path and digest.
//
//   - a fully succeeded plan for those bytes wins (kindAlreadyPublished:
//     an idempotent no-op, never re-published);
//   - otherwise a reusable plan (planned, approved, publishing, failed,
//     partially succeeded) is returned (kindReused);
//   - otherwise the content has no plan yet (kindNew, nil plan).
//
// Stale content — an existing plan whose digest no longer matches — is not
// reused: republishing changed bytes is a new plan with a fresh approval.
func planForContent(eng *publish.Engine, id run.RunID, d *presets.Destination) (*publication.Plan, planKind, error) {
	ids, err := eng.ListPlanIDs(id)
	if err != nil {
		return nil, kindNew, fmt.Errorf("listing existing plans for run %s: %w", id, err)
	}
	wantDigest := string(d.Digest)
	var reuse, published *publication.Plan
	for _, pid := range ids {
		plan, err := eng.Plan(id, pid)
		if err != nil {
			continue // an unreadable plan document never blocks a new plan
		}
		if plan.Artifact.Path != d.ContentPath() {
			continue
		}
		if !plan.Artifact.Digest.Matches(wantDigest) {
			continue // stale content: not reusable for the current bytes
		}
		switch {
		case plan.Status == publication.StatusSucceeded:
			published = plan
		case !plan.Status.Terminal() || plan.Status == publication.StatusFailed || plan.Status == publication.StatusPartiallySucceeded:
			reuse = plan
		}
	}
	switch {
	case published != nil:
		return published, kindAlreadyPublished, nil
	case reuse != nil:
		return reuse, kindReused, nil
	}
	return nil, kindNew, nil
}

// selectUsableDestinations filters the selected preset destinations by
// whether their content could be prepared for the run:
//
//   - an explicitly selected destination whose content is broken is a hard
//     error (the operator asked for it);
//   - an unselected broken destination is a warning and is excluded.
//
// The CLI maps the error to its documented exit code; the TUI reports it
// in the screen's state.
func selectUsableDestinations(pub *presets.Preset, selected []*presets.Destination, targets []string, id run.RunID, onWarn func(format string, args ...any)) ([]*presets.Destination, error) {
	usable := make([]*presets.Destination, 0, len(selected))
	for _, d := range selected {
		if d.ContentErr != nil {
			if targetExplicitlySelected(targets, d) {
				return nil, fmt.Errorf("target %s cannot be prepared for run %s: %v", d.Target.ID, id, d.ContentErr)
			}
			onWarn("target %s is not available for run %s: %v (excluded)", d.Target.ID, id, d.ContentErr)
			continue
		}
		usable = append(usable, d)
	}
	return usable, nil
}

// targetExplicitlySelected reports whether the destination was named by a
// target selector (a key or a target ID).
func targetExplicitlySelected(targets []string, d *presets.Destination) bool {
	for _, t := range targets {
		if t == d.Key || t == string(d.Target.ID) {
			return true
		}
	}
	return false
}

// preparePlanSet builds the plan set for a run: for each usable
// destination it persists the canonical content, resolves the run's
// existing plan for those bytes (reuse / already published / new), creates
// the plan only where the content has none yet, and — when preview is
// true — runs the zero-side-effect dry-run rehearsal (a rehearsal problem
// is a warning, never a failure).
//
// A not-ready run must not be passed here with create: the gate runs
// first (see the callers). The CLI passes create=true (its first
// invocation prepares the plan); the TUI prepares on entry and reads the
// persisted plans afterwards.
func preparePlanSet(ctx context.Context, eng *publish.Engine, orch *publish.Orchestrator, id run.RunID, bundle *review.Bundle, pub *presets.Preset, usable []*presets.Destination, create, preview bool, onWarn func(format string, args ...any)) ([]planView, error) {
	views := make([]planView, 0, len(usable))
	for _, d := range usable {
		if create {
			if _, err := eng.SaveContent(id, d.ContentName, d.Content); err != nil {
				return nil, fmt.Errorf("persisting the canonical content for target %s: %w", d.Target.ID, err)
			}
		}
		plan, kind, err := planForContent(eng, id, d)
		if err != nil {
			return nil, err
		}
		if create && kind == kindNew {
			req := publish.CreatePlanRequest{
				ArtifactPath: d.ContentPath(),
				Targets:      []publish.TargetSpec{d.Target},
			}
			plan, err = eng.CreatePlan(ctx, id, req, bundle)
			if err != nil {
				return nil, fmt.Errorf("preparing the plan for target %s: %w", d.Target.ID, err)
			}
		}
		views = append(views, planView{kind: kind, plan: plan, dest: d})
	}
	if preview {
		for i := range views {
			v := &views[i]
			if v.kind == kindAlreadyPublished {
				continue // terminal: no rehearsal is re-run against it
			}
			if _, err := bindPlanPublishers(orch, pub, v.dest, v.plan); err != nil {
				onWarn("publisher for plan %s: %v (preview may be incomplete)", v.plan.ID, err)
			}
			v.pv, v.pvErr = orch.Preview(ctx, v.plan.ID)
			// The preview records its rehearsals on the persisted plan where
			// the state allows; re-read it so the views (and the CLI reports
			// built from them) see the recorded dry runs.
			if v.pvErr == nil {
				if fresh, ferr := eng.Plan(id, v.plan.ID); ferr == nil {
					v.plan = fresh
				}
			}
		}
	}
	return views, nil
}

// bindPlanPublishers builds the publishers for one plan's targets through
// the preset's factory and binds them on the orchestrator. An error means
// the plan cannot publish (no preset, or the adapter refused to
// construct); callers that only preview degrade it to a warning instead.
func bindPlanPublishers(orch *publish.Orchestrator, pub *presets.Preset, dest *presets.Destination, plan *publication.Plan) (map[publication.TargetID]publish.Publisher, error) {
	if pub == nil || dest == nil {
		return nil, fmt.Errorf("the publication preset is not available: without it the plan's publisher cannot be configured (select the preset with --publication/--bundle-root)")
	}
	if len(plan.Targets) == 0 {
		return nil, fmt.Errorf("plan %s has no targets", plan.ID)
	}
	pubs, err := pub.Publishers(context.Background(), []*publication.Plan{plan})
	if err != nil {
		return nil, err
	}
	m := make(map[publication.TargetID]publish.Publisher, len(plan.Targets))
	for _, t := range plan.Targets {
		m[t.ID] = pubs[0]
	}
	orch.SetPublishers(m)
	return m, nil
}

// planApproved reports whether the plan holds an effective approval for
// every one of its targets: granted, bound to the plan's artifact digest,
// and scoped to the target. It mirrors the engine's per-target authority
// check (Approval.EffectiveFor) without re-reading the artifact bytes.
func planApproved(plan *publication.Plan) bool {
	if plan == nil || len(plan.Targets) == 0 {
		return false
	}
	digest := string(plan.Artifact.Digest)
	for _, t := range plan.Targets {
		ok := false
		for i := len(plan.Approvals) - 1; i >= 0; i-- {
			if plan.Approvals[i].EffectiveFor(digest, t.ID) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// runPublicationCycle runs the publication cycle over the plan set: it
// binds each plan's publishers and publishes the plans in canonical order
// (site before thread), stopping at the first failure, interruption, or
// incomplete plan — it never publishes ahead of a failed destination.
//
// onPublish, when non-nil, is called after each plan's publish attempt
// (the CLI prints the per-plan result; the TUI ignores it and re-projects
// the state). When the cycle finishes — however it ended — every view's
// plan copy is refreshed from the engine's persisted document, the
// authoritative post-cycle state.
//
// The returned code is the documented CLI exit code (0 = the set fully
// succeeded); the TUI treats it as a hint, not a contract.
func runPublicationCycle(ctx context.Context, eng *publish.Engine, orch *publish.Orchestrator, pub *presets.Preset, id run.RunID, views []planView, onPublish func(v *planView, sum *publish.Summary, err error)) (int, error) {
	code := 0
	var stopErr error
	for i := range views {
		v := &views[i]
		if v.kind == kindAlreadyPublished {
			continue
		}
		if _, berr := bindPlanPublishers(orch, pub, v.dest, v.plan); berr != nil {
			code = 2
			stopErr = fmt.Errorf("plan %s cannot be published: %v", v.plan.ID, berr)
			if onPublish != nil {
				onPublish(v, nil, stopErr)
			}
			break
		}
		var err error
		v.sum, err = orch.Publish(ctx, v.plan.ID)
		if onPublish != nil {
			onPublish(v, v.sum, err)
		}
		if ctx.Err() != nil || (v.sum != nil && v.sum.Interrupted) {
			code = 7
			// The engine reports the interruption as a domain error; carry it
			// so a caller (the TUI) can surface why the cycle stopped. The CLI
			// message is unchanged: cycleExitMessage keys off the summary.
			if err != nil {
				stopErr = err
			}
			break
		}
		if err != nil {
			code = publishErrorExitCode(err, v.sum)
			stopErr = err
			break
		}
		if v.sum != nil && (v.sum.Status == publication.StatusFailed || v.sum.Status == publication.StatusPartiallySucceeded) {
			code = 6
			break
		}
	}
	// The engine's persisted documents are the authoritative post-cycle
	// state; refresh the plan copies.
	for i := range views {
		if views[i].plan == nil {
			continue
		}
		if p2, perr := eng.Plan(id, views[i].plan.ID); perr == nil {
			views[i].plan = p2
		}
	}
	return code, stopErr
}

// cycleExitCode maps a publication cycle outcome to the documented exit
// code.
func cycleExitCode(ctx context.Context, sum *publish.Summary, err error) int {
	if ctx.Err() != nil || (sum != nil && sum.Interrupted) {
		return 7
	}
	if errors.Is(err, publish.ErrNotApproved) || errors.Is(err, publish.ErrStaleApproval) ||
		errors.Is(err, publish.ErrArtifactChanged) || errors.Is(err, publish.ErrPlanCancelled) {
		return 5
	}
	if errors.Is(err, publish.ErrPublisherNotBound) || errors.Is(err, publish.ErrPlanInvalid) ||
		errors.Is(err, publish.ErrArtifactMissing) || errors.Is(err, publish.ErrGateFailed) {
		return 2
	}
	if sum != nil {
		switch sum.Status {
		case publication.StatusSucceeded, publication.StatusSkipped:
			return 0
		case publication.StatusFailed, publication.StatusPartiallySucceeded:
			return 6
		case publication.StatusPublishing:
			return 7
		}
	}
	if err != nil {
		return 1
	}
	return 0
}

// publishErrorExitCode maps a publish error to an exit code, defaulting an
// in-cycle failure to 6 (partial: resume it).
func publishErrorExitCode(err error, sum *publish.Summary) int {
	if errors.Is(err, publish.ErrNotApproved) || errors.Is(err, publish.ErrStaleApproval) ||
		errors.Is(err, publish.ErrArtifactChanged) || errors.Is(err, publish.ErrPlanCancelled) {
		return 5
	}
	if errors.Is(err, publish.ErrPublisherNotBound) || errors.Is(err, publish.ErrPlanInvalid) ||
		errors.Is(err, publish.ErrArtifactMissing) || errors.Is(err, publish.ErrGateFailed) {
		return 2
	}
	if errors.Is(err, publish.ErrInterrupted) {
		return 7
	}
	if sum != nil && (sum.Status == publication.StatusFailed || sum.Status == publication.StatusPartiallySucceeded) {
		return 6
	}
	return 6
}

// cycleExitMessage summarizes a non-zero cycle outcome for the error line.
func cycleExitMessage(id run.RunID, sum *publish.Summary, err error) string {
	status := ""
	if sum != nil {
		status = string(sum.Status)
	}
	switch {
	case sum != nil && sum.Interrupted:
		return "publication was interrupted: resume with 'newsroom publish " + string(id) + " --resume'"
	case err != nil:
		// A specific cycle error (stale approval, changed artifact, gate
		// drift, cancelled plan) wins over the generic status summary: it
		// names the exact reason and the exact recovery step, which the
		// status line alone would hide.
		return err.Error()
	case status == "failed" || status == "partially_succeeded":
		return "publication is incomplete (a target failed): resume with 'newsroom publish " + string(id) + " --resume' (already-published targets keep their receipts and are not re-published)"
	}
	return "publication failed"
}
