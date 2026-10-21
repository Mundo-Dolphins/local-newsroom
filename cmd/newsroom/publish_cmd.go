package main

// v0.6 publication CLI (issue #121): newsroom publish.
//
// This is the presentation layer over the v0.6 publication pipeline:
//
//	internal/publish (durable engine + UI-independent orchestrator)
//	internal/publish/presets (editorial preset -> destinations)
//
// The command owns exactly three things:
//
//  1. Safe UX. The first invocation shows the publication plan and the
//     per-target preview (dry run) and stops. Nothing is published without
//     an explicit --approve, and approval is never inferred from a TTY or
//     from stdin alone: interactively, a prompt must be answered "yes";
//     non-interactively, the exact artifact digest must be confirmed with
//     --digest (one per plan).
//  2. Destination and plan selection (--target, --plan, --resume).
//  3. Presentation (text or JSON) of the engine's projections: readiness,
//     artifact hashes, destinations, previews, approvals, receipts, and
//     remediation.
//
// No Hugo, git, or Bluesky business logic lives here: that stays in the
// adapters and the engine, and the executable publishers are built through
// the presets package's factory, which tests substitute with fakes.
//
// Exit codes (also in the command's help):
//
//	0  preview/dry-run complete, or the publication fully succeeded
//	1  unexpected internal error
//	2  usage or configuration error (bad run ID, unknown target, missing
//	   preset or publisher configuration)
//	3  the run is not ready for publication (gate: status/final-check/review)
//	4  nothing to publish (no preset, or no preparable destination)
//	5  approval missing, declined, or stale
//	6  the publication ran but at least one target failed (resume it)
//	7  interrupted (Ctrl-C) mid-cycle (resume it)
//	8  run or plan not found

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/config"
	"github.com/Mundo-Dolphins/local-newsroom/internal/editorial"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publish"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publish/presets"
	"github.com/Mundo-Dolphins/local-newsroom/internal/review"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workspace"
	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// Command definition
// ---------------------------------------------------------------------------

var publishCmd = &cobra.Command{
	Use:   "publish <run-id>",
	Short: "publish a finished run to the destinations of its publication preset",
	Long: `Publish a finished run to the destinations declared by its publication preset.

The pipeline is: preview (dry run) -> explicit approval -> publish.

  newsroom publish <run-id>
      Show the publication plan: readiness, artifact hash, destinations,
      per-target dry-run previews, warnings, and previous receipts.
      Nothing is published. This is the default behavior; --dry-run is the
      same preview, made explicit for scripts.

  newsroom publish <run-id> --approve
      Preview, then publish after explicit approval. At a terminal you are
      prompted per plan (yes is required); without a terminal you must
      confirm the exact artifact digest: --approve --digest <digest>
      (one --digest per plan, in plan order).

  newsroom publish <run-id> --target hugo --target bluesky
      Prepare/publish a subset of the preset's destinations (keys: hugo,
      bluesky; or target IDs: site, thread).

  newsroom publish <run-id> --resume
      Resume the run's latest plan: re-execute the failed or interrupted
      targets. Targets that already succeeded are proven by their receipts
      and are never re-published. A completed plan is a no-op; a plan that
      was never approved is refused until approved.

  newsroom publish <run-id> --plan <plan-id>
      Operate on a specific plan instead of the run's latest (preview it;
      with --resume or --approve, run its cycle).

Publishing is idempotent per plan, and the engine never deletes anything
it published: a target that failed after another succeeded is reported with
both receipts and the remediation steps for a human.

Exit codes:
  0  preview complete, or publication fully succeeded
  1  unexpected internal error
  2  usage or configuration error (bad run ID, unknown target, missing preset)
  3  the run is not ready for publication (status / final check / review)
  4  nothing to publish (no preset, or no preparable destination)
  5  approval missing, declined, or stale
  6  the publication ran but at least one target failed (resume it)
  7  interrupted mid-cycle (resume it)
  8  run or plan not found`,
	Args: cobra.ExactArgs(1),
	RunE: doPublish,
}

// publishFlags carries the publish command's flag values.
var publishFlags struct {
	dryRun        bool
	approve       bool
	resume        bool
	targets       []string
	digests       []string
	approver      string
	plan          string
	format        string
	workspaceRoot string
	bundleRoot    string
	publication   string
}

// publisherFactory builds the executable publishers for the preset's
// destinations. Production: the real adapters (git publish, Bluesky API).
// Tests substitute fakes, so the CLI is verified without a checkout or a
// network.
var publisherFactory presets.Factory = presets.DefaultFactory

// publishPrompt is the input of the interactive approval prompt. It is a
// variable (not a package global of a concrete type) so tests can drive
// the prompt deterministically with a buffer.
var publishPrompt io.Reader = os.Stdin

// defaultPublishTTY reports whether stdin is a terminal.
func defaultPublishTTY() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// publishTTY decides whether a prompt is offered. In production it is the
// real TTY check; tests pin it deterministically. The presence of a TTY
// (or of stdin) never implies approval: it only decides how approval is
// requested.
var publishTTY = defaultPublishTTY

func init() {
	f := publishCmd.Flags()
	f.BoolVar(&publishFlags.dryRun, "dry-run", false, "preview only: the plan and the per-target dry run, with no approval and no publication (the default behavior, made explicit)")
	f.BoolVar(&publishFlags.approve, "approve", false, "explicitly approve the previewed plan(s) and publish (interactive: a yes prompt per plan; non-interactive: requires --digest per plan)")
	f.BoolVar(&publishFlags.resume, "resume", false, "resume an existing plan (re-execute its failed or interrupted targets) instead of preparing new plans")
	f.StringArrayVar(&publishFlags.targets, "target", nil, "limit the publication to preset destinations: a key (hugo, bluesky) or a target ID (site, thread); repeatable (default: all)")
	f.StringArrayVar(&publishFlags.digests, "digest", nil, "the exact artifact digest an approval confirms (sha256:<64 hex>); repeatable, one per plan, in plan order (required with --approve when stdin is not a terminal)")
	f.StringVar(&publishFlags.approver, "approver", "", "the approving principal recorded on the approval (default: the current OS user)")
	f.StringVar(&publishFlags.plan, "plan", "", "the plan ID to operate on instead of the run's latest plan")
	f.StringVar(&publishFlags.format, "format", "text", "output format: text or json")
	f.StringVar(&publishFlags.workspaceRoot, "workspace-root", ".newsroom", "workspace root directory")
	f.StringVar(&publishFlags.bundleRoot, "bundle-root", "", "editorial bundle directory (default: editorial.bundle_root from the configuration chain)")
	f.StringVar(&publishFlags.publication, "publication", "", "publication preset in the editorial bundle (default: the preset the run recorded, else editorial.publication from the configuration chain)")

	rootCmd.AddCommand(publishCmd)
}

// exitError is a command error carrying a specific process exit code (see
// the package documentation for the table). main() maps it; the v0.5
// commands return plain errors and keep their historical exit code 1.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func exitf(code int, format string, args ...any) error {
	return &exitError{code: code, err: fmt.Errorf(format, args...)}
}

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

// ---------------------------------------------------------------------------
// Command handler
// ---------------------------------------------------------------------------

// doPublish implements newsroom publish.
func doPublish(cmd *cobra.Command, args []string) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if v := strings.ToLower(strings.TrimSpace(publishFlags.format)); v != "text" && v != "json" {
		return exitf(2, "invalid --format %q: use text or json", publishFlags.format)
	}
	id := run.RunID(strings.TrimSpace(args[0]))
	if err := id.Validate(); err != nil {
		return exitf(2, "invalid run ID %q: %v", args[0], err)
	}

	// --- Workspace and run ------------------------------------------------
	root, err := filepath.Abs(publishFlags.workspaceRoot)
	if err != nil {
		return fmt.Errorf("resolving workspace root: %w", err)
	}
	// The publish command resolves no run-level settings, so the sanitizer
	// is seeded from the current configuration only.
	store, err := workspace.NewStoreWithSanitizer(root, run.NewSanitizer(appConfig.SecretValues(nil)...))
	if err != nil {
		return fmt.Errorf("creating workspace store at %s: %w", root, err)
	}

	m, _, err := store.LoadManifest(id)
	if err != nil {
		if errors.Is(err, workspace.ErrRunNotFound) {
			if jsonMode() {
				pubReportPrint(pubReport{Run: pubRun{ID: string(id)}, Mode: "publish", ExitCode: 8, Error: fmt.Sprintf("run %s not found in workspace %s", id, root)})
			}
			return exitf(8, "run %s not found in workspace %s", id, root)
		}
		return fmt.Errorf("loading run %s: %w", id, err)
	}

	bundle, err := review.Build(store, m)
	if err != nil {
		return fmt.Errorf("building the review bundle for run %s: %w", id, err)
	}

	eng := publish.New(store)
	orch := publish.NewOrchestrator(eng, id, bundle)

	// --- Publication preset ------------------------------------------------
	preset, bundleRoot, err := resolvePublishPreset(m)
	if err != nil {
		emitJSON(m, false, "", nil, "publish", 2, nil, false, err.Error())
		return exitf(2, "%v", err)
	}
	var pub *presets.Preset
	if preset != nil {
		pub, err = presets.New(preset, bundleRoot, id, store, presets.WithFactory(publisherFactory))
		if err != nil {
			emitJSON(m, false, "", nil, "publish", 2, nil, false, err.Error())
			return exitf(2, "%v", err)
		}
	}

	// --- Readiness (always reported) ----------------------------------------
	var existing *publication.Plan
	if publishFlags.plan != "" || publishFlags.resume {
		existing, err = resolvePublishPlan(eng, id)
		if err != nil {
			return exitf(8, "%v", err)
		}
	}
	ready, readyReason, err := publishReadiness(orch, m, bundle, pub, existing)
	if err != nil {
		return fmt.Errorf("%w", err)
	}

	if existing != nil {
		return doPublishExistingPlan(ctx, eng, orch, m, pub, existing, ready, readyReason, id)
	}
	return doPublishNew(ctx, eng, orch, m, pub, ready, readyReason, id, bundle)
}

// resolvePublishPreset resolves the publication preset for a publish
// invocation.
//
// Precedence: --publication flag, then the preset the run recorded (a run
// started with a publication keeps its preset for the whole lifecycle),
// then the configuration chain (editorial.publication + editorial.bundle_root).
//
// It returns (nil, "", nil) when no preset is selected: that is not an
// error here (a run without a preset simply has nothing to publish, which
// the caller reports as exit 4).
func resolvePublishPreset(m *run.Manifest) (*editorial.Preset, string, error) {
	name := strings.TrimSpace(publishFlags.publication)
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
	root := strings.TrimSpace(publishFlags.bundleRoot)
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

// resolvePublishPlan resolves the plan a publish invocation operates on:
// the run's latest plan by default, or the one named with --plan.
func resolvePublishPlan(eng *publish.Engine, id run.RunID) (*publication.Plan, error) {
	if strings.TrimSpace(publishFlags.plan) != "" {
		p, err := eng.Plan(id, publication.PublicationID(publishFlags.plan))
		if err != nil {
			return nil, fmt.Errorf("plan %s not found for run %s: %v", publishFlags.plan, id, err)
		}
		return p, nil
	}
	p, err := eng.LatestPlan(id)
	if err != nil {
		return nil, fmt.Errorf("run %s has no publication plan to resume: %v (run 'newsroom publish %s' to prepare one)", id, err, id)
	}
	return p, nil
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

// ---------------------------------------------------------------------------
// Existing-plan path (--plan / --resume)
// ---------------------------------------------------------------------------

// doPublishExistingPlan operates on an existing plan: preview it always;
// run its publication cycle only when explicitly requested (--resume or
// --approve).
func doPublishExistingPlan(ctx context.Context, eng *publish.Engine, orch *publish.Orchestrator, m *run.Manifest, pub *presets.Preset, plan *publication.Plan, ready bool, readyReason string, id run.RunID) error {
	dest := destinationForPlan(pub, plan)

	// --target, combined with an existing plan, must name one of the
	// plan's targets: the plan's target set is fixed at creation.
	if len(publishFlags.targets) > 0 {
		ok, err := targetsCoverPlan(publishFlags.targets, plan, dest)
		if err != nil {
			return exitf(2, "%v", err)
		}
		if !ok {
			return exitf(2, "--target %s does not match plan %s (plan targets: %s)", strings.Join(publishFlags.targets, ", "), plan.ID, planTargetIDs(plan))
		}
	}

	switch plan.Status {
	case publication.StatusSucceeded, publication.StatusSkipped:
		// Already complete: a resume of a completed plan is a no-op that
		// re-reports its receipts.
		printExistingPlan(m, pub, &planView{plan: plan, dest: dest}, ready, readyReason, true)
		emitJSON(m, ready, readyReason, pub, "resume", 0, []planView{{plan: plan, dest: dest}}, true, "")
		return nil
	case publication.StatusCancelled:
		msg := fmt.Sprintf("plan %s was cancelled and will not resume: publish again to prepare a new plan (newsroom publish %s)", plan.ID, id)
		emitJSON(m, ready, readyReason, pub, "resume", 5, []planView{{plan: plan, dest: dest}}, false, msg)
		return exitf(5, "plan %s was cancelled and will not resume: publish again to prepare a new plan (newsroom publish %s)", plan.ID, id)
	}

	if !ready {
		printNotReady(m, readyReason)
		msg := fmt.Sprintf("run %s is not ready for publication: %s", id, readyReason)
		emitJSON(m, ready, readyReason, pub, "resume", 3, []planView{{plan: plan, dest: dest}}, false, msg)
		return exitf(3, "run %s is not ready for publication: %s", id, readyReason)
	}

	// Preview the plan (the rehearsal is re-run fresh against what is on
	// disk now and recorded on the plan for audit). Publisher-construction
	// problems degrade the preview to a warning, they do not fail it.
	pv, pvErr := orch.Preview(ctx, plan.ID)
	view := &planView{plan: plan, dest: dest, pv: pv, pvErr: pvErr}
	printExistingPlan(m, pub, view, ready, readyReason, false)
	if pvErr != nil {
		warnf("preview: %v", pvErr)
	}

	runCycle := publishFlags.resume || publishFlags.approve
	if !runCycle {
		printExistingPlanNextSteps(id, plan)
		emitJSON(m, ready, readyReason, pub, "preview", 0, []planView{*view}, false, "")
		return nil
	}

	// Approval must be in force: granted and digest-bound.
	if !planApproved(plan) {
		if !publishFlags.approve {
			msg := fmt.Sprintf("plan %s has no effective approval (status %s, artifact %s): run 'newsroom publish %s --resume --approve %s' to approve and publish",
				plan.ID, plan.Status, plan.Artifact.Digest, id, digestApprovalHint())
			emitJSON(m, ready, readyReason, pub, "resume", 5, []planView{*view}, false, msg)
			return exitf(5, "plan %s has no effective approval (status %s, artifact %s): run 'newsroom publish %s --resume --approve %s' to approve and publish",
				plan.ID, plan.Status, plan.Artifact.Digest, id, digestApprovalHint())
		}
		if err := approvePlan(ctx, orch, plan, dest, 0); err != nil {
			emitJSON(m, ready, readyReason, pub, "resume", 5, []planView{*view}, false, err.Error())
			return err
		}
		plan2, err := eng.Plan(id, plan.ID)
		if err != nil {
			return fmt.Errorf("reloading plan %s after approval: %v", plan.ID, err)
		}
		plan = plan2
		dest = destinationForPlan(pub, plan)
		view.plan, view.dest = plan, dest
	}

	// Bind the publisher and run the cycle.
	if _, err := bindPlanPublishers(orch, pub, dest, plan); err != nil {
		msg := fmt.Sprintf("plan %s cannot be published: %v", plan.ID, err)
		emitJSON(m, ready, readyReason, pub, "publish", 2, []planView{*view}, false, msg)
		return exitf(2, "plan %s cannot be published: %v", plan.ID, err)
	}

	sum, err := orch.Publish(ctx, plan.ID)
	view.sum = sum
	// Reload the plan: the engine's persisted document is the authoritative
	// post-cycle state (the loaded copy predates the approval/cycle).
	if plan2, perr := eng.Plan(id, plan.ID); perr == nil {
		plan = plan2
		view.plan = plan
	}
	code := cycleExitCode(ctx, sum, err)
	printCycleResult(plan, dest, view.sum)
	published := code == 0
	msg := ""
	if !published {
		msg = cycleExitMessage(id, sum, err)
	}
	emitJSON(m, ready, readyReason, pub, "publish", code, []planView{*view}, published, msg)
	if code != 0 {
		return exitf(code, "%s", msg)
	}
	return nil
}

// targetsCoverPlan checks --target values against an existing plan's
// targets: every flag value must name the plan's destination key or one of
// its target IDs, and at least one must match.
func targetsCoverPlan(targets []string, plan *publication.Plan, dest *presets.Destination) (bool, error) {
	valid := make([]string, 0, len(plan.Targets)+1)
	for _, t := range plan.Targets {
		valid = append(valid, string(t.ID))
	}
	if dest != nil {
		valid = append(valid, dest.Key)
	}
	valid = dedupeStrings(valid)
	matched := false
	for _, t := range targets {
		ok := false
		for _, v := range valid {
			if v == t {
				ok = true
			}
		}
		if !ok {
			return false, fmt.Errorf("unknown target %q for plan %s: valid targets are %s", t, plan.ID, strings.Join(valid, ", "))
		}
		matched = true
	}
	return matched, nil
}

// ---------------------------------------------------------------------------
// New-plan path (prepare per destination, preview, approve, publish)
// ---------------------------------------------------------------------------

// planForContent resolves the existing plans of the run for the
// destination's canonical content (the same content path):
//
//   - kindAlreadyPublished when the newest plan for this exact digest
//     succeeded — publishing these bytes again would duplicate the external
//     effect, so the destination is reported as a no-op;
//   - kindReused when the newest plan for this digest is still actionable
//     (planned, approved, publishing, failed, or partially succeeded) — it
//     is reused instead of churning a new plan ID;
//   - (nil, kindNew) when the run holds no plan for this content.
//
// Plans on the same path with a different digest (stale: the content
// changed after they were created) are never reused for the new bytes.
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

// doPublishNew prepares one plan per selected destination, previews each
// one, and — only with --approve — approves and publishes them in
// canonical order (site before thread).
func doPublishNew(ctx context.Context, eng *publish.Engine, orch *publish.Orchestrator, m *run.Manifest, pub *presets.Preset, ready bool, readyReason string, id run.RunID, bundle *review.Bundle) error {
	if pub == nil {
		msg := fmt.Sprintf("nothing to publish: run %s has no publication preset (select one with --publication or editorial.publication, and an editorial bundle with --bundle-root or editorial.bundle_root)", id)
		emitJSON(m, ready, readyReason, pub, "preview", 4, nil, false, msg)
		return exitf(4, "nothing to publish: run %s has no publication preset (select one with --publication or editorial.publication, and an editorial bundle with --bundle-root or editorial.bundle_root)", id)
	}

	selected, err := pub.Select(publishFlags.targets...)
	if err != nil {
		return exitf(2, "%v", err)
	}

	// Destinations whose content could not be prepared for this run are
	// reported. An explicitly selected broken destination is a hard error;
	// an unselected one is a warning and is simply excluded.
	var usable []*presets.Destination
	for _, d := range selected {
		if d.ContentErr != nil {
			if targetExplicitlySelected(publishFlags.targets, d) {
				return exitf(4, "target %s cannot be prepared for run %s: %v", d.Target.ID, id, d.ContentErr)
			}
			warnf("target %s is not available for run %s: %v (excluded)", d.Target.ID, id, d.ContentErr)
			continue
		}
		usable = append(usable, d)
	}
	if len(usable) == 0 {
		return exitf(4, "nothing to publish: no destination of preset %q can be prepared for run %s", pub.Name, id)
	}

	// Not-ready runs stop before any plan exists.
	if !ready {
		printNotReady(m, readyReason)
		msg := fmt.Sprintf("run %s is not ready for publication: %s", id, readyReason)
		emitJSON(m, ready, readyReason, pub, "preview", 3, nil, false, msg)
		return exitf(3, "run %s is not ready for publication: %s", id, readyReason)
	}

	// Persist the canonical content (the plan baselines to the bytes on
	// disk), then resolve one plan per destination: reuse the run's existing
	// plan for the same content, report already-published content as a
	// no-op, and create a new plan only where the content has no plan yet.
	views := make([]planView, 0, len(usable))
	for _, d := range usable {
		if _, err := eng.SaveContent(id, d.ContentName, d.Content); err != nil {
			return fmt.Errorf("persisting the canonical content for target %s: %w", d.Target.ID, err)
		}
		plan, kind, err := planForContent(eng, id, d)
		if err != nil {
			return exitf(1, "%v", err)
		}
		if kind == kindNew {
			req := publish.CreatePlanRequest{
				ArtifactPath: d.ContentPath(),
				Targets:      []publish.TargetSpec{d.Target},
			}
			plan, err = eng.CreatePlan(ctx, id, req, bundle)
			if err != nil {
				return fmt.Errorf("preparing the plan for target %s: %w", d.Target.ID, err)
			}
		}
		views = append(views, planView{kind: kind, plan: plan, dest: d})
	}

	// Preview each actionable plan (zero external side effects; the
	// rehearsal is recorded on the plan for audit). An already-published
	// plan is terminal: no rehearsal is re-run against it. Publisher-
	// construction problems degrade the preview to a warning, they do not
	// fail the invocation.
	for i := range views {
		if views[i].kind == kindAlreadyPublished {
			continue
		}
		if _, err := bindPlanPublishers(orch, pub, views[i].dest, views[i].plan); err != nil {
			warnf("publisher for plan %s: %v (preview may be incomplete)", views[i].plan.ID, err)
		}
		views[i].pv, views[i].pvErr = orch.Preview(ctx, views[i].plan.ID)
	}

	printNewPreview(m, pub, ready, views)

	if !publishFlags.approve {
		if actionableCount(views) == 0 {
			fmt.Println("All plans are already published: this invocation is an idempotent no-op.")
		} else {
			printPreviewNextSteps(id, views)
		}
		emitJSON(m, ready, readyReason, pub, "preview", 0, views, false, "")
		return nil
	}

	// --- Approval (per plan; a refusal stops the invocation) --------------
	for i := range views {
		v := &views[i]
		if v.kind == kindAlreadyPublished {
			continue
		}
		if planApproved(v.plan) {
			if !jsonMode() {
				fmt.Printf("Plan %s already holds an effective approval (artifact %s)\n", v.plan.ID, v.plan.Artifact.Digest)
			}
			continue
		}
		if err := approvePlan(ctx, orch, v.plan, v.dest, i); err != nil {
			emitJSON(m, ready, readyReason, pub, "publish", 5, views, false, err.Error())
			return err
		}
	}

	// --- Publish (canonical order; a failed destination stops the rest) ---
	code := 0
	for i := range views {
		v := &views[i]
		if v.kind == kindAlreadyPublished {
			continue
		}
		if _, err := bindPlanPublishers(orch, pub, v.dest, v.plan); err != nil {
			code = 2
			printCycleResult(v.plan, v.dest, nil)
			emitJSON(m, ready, readyReason, pub, "publish", code, views, false, err.Error())
			return exitf(code, "plan %s cannot be published: %v", v.plan.ID, err)
		}
		v.sum, err = orch.Publish(ctx, v.plan.ID)
		printCycleResult(v.plan, v.dest, v.sum)
		if ctx.Err() != nil || (v.sum != nil && v.sum.Interrupted) {
			code = 7
			break
		}
		if err != nil {
			code = publishErrorExitCode(err, v.sum)
			break // stop: never publish ahead of a failed destination
		}
		if v.sum != nil && (v.sum.Status == publication.StatusFailed || v.sum.Status == publication.StatusPartiallySucceeded) {
			code = 6
			break
		}
	}

	msg := ""
	published := false
	// The engine's persisted documents are the authoritative post-cycle
	// state; refresh the report's plan copies before presenting.
	for i := range views {
		if p2, perr := eng.Plan(id, views[i].plan.ID); perr == nil {
			views[i].plan = p2
		}
	}
	if code == 0 {
		published = true
	} else {
		msg = cycleExitMessage(id, views[len(views)-1].sum, nil)
	}
	emitJSON(m, ready, readyReason, pub, "publish", code, views, published, msg)
	if code != 0 {
		return exitf(code, "%s", msg)
	}
	return nil
}

// targetExplicitlySelected reports whether the destination was named by a
// --target flag (as a key or a target ID).
func targetExplicitlySelected(targets []string, d *presets.Destination) bool {
	for _, t := range targets {
		if t == d.Key || t == string(d.Target.ID) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Shared cycle / approval helpers
// ---------------------------------------------------------------------------

// bindPlanPublishers builds the publishers for one plan's targets through
// the preset's factory and binds them on the orchestrator. An error means
// the plan cannot publish (no preset, or the adapter refused to
// construct); callers printing previews degrade it to a warning instead.
func bindPlanPublishers(orch *publish.Orchestrator, pub *presets.Preset, dest *presets.Destination, plan *publication.Plan) (map[publication.TargetID]publish.Publisher, error) {
	if pub == nil || dest == nil {
		return nil, errors.New("the publication preset is not available: without it the plan's publisher cannot be configured (select the preset with --publication/--bundle-root)")
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

// approvePlan obtains the explicit approval for one plan.
//
// Interactive (stdin is a terminal): the plan and its artifact digest are
// shown and a "yes" is required. Non-interactive: the exact artifact digest
// must be confirmed with --digest (paired positionally to the plan). The
// mere presence of a terminal or stdin never implies approval.
func approvePlan(ctx context.Context, orch *publish.Orchestrator, plan *publication.Plan, dest *presets.Destination, index int) error {
	approver, err := resolveApprover()
	if err != nil {
		return exitf(5, "%v", err)
	}
	digest := plan.Artifact.Digest

	if isInteractive() {
		if !jsonMode() {
			fmt.Printf("Approve plan %s (%s -> %s, artifact %s)? [y/N] ", plan.ID, destName(dest), destTargetLabel(dest), digest)
		}
		line, _ := bufio.NewReader(publishPrompt).ReadString('\n')
		answer := strings.ToLower(strings.TrimSpace(line))
		if answer != "y" && answer != "yes" {
			return exitf(5, "approval declined for plan %s: nothing was published (answer 'yes' to approve)", plan.ID)
		}
		_, _, err = orch.Approve(ctx, plan.ID, publish.Approval{
			ApprovedBy:      approver,
			Method:          publication.ApprovalMethodCLI,
			Note:            "approved at the newsroom publish prompt",
			DigestConfirmed: digest,
		})
	} else {
		if len(publishFlags.digests) < 1 {
			return exitf(5, "plan %s requires explicit digest confirmation: pass --digest %s (one --digest per plan, in plan order)", plan.ID, digest)
		}
		var got publication.Digest
		if len(publishFlags.digests) == 1 {
			got = publication.Digest(strings.TrimSpace(publishFlags.digests[0]))
		} else if index >= len(publishFlags.digests) {
			return exitf(5, "plan %s requires explicit digest confirmation: pass --digest %s (one --digest per plan, in plan order)", plan.ID, digest)
		} else {
			got = publication.Digest(strings.TrimSpace(publishFlags.digests[index]))
		}
		if !got.Matches(string(digest)) {
			return exitf(5, "digest mismatch for plan %s: --digest %s does not match the plan's artifact %s (approval binds to the exact bytes: re-preview and confirm the current digest)", plan.ID, got, digest)
		}
		_, _, err = orch.Approve(ctx, plan.ID, publish.Approval{
			ApprovedBy:      approver,
			Method:          publication.ApprovalMethodCLI,
			Note:            "approved non-interactively via --approve --digest",
			DigestConfirmed: got,
		})
	}
	if err != nil {
		return exitf(5, "approval for plan %s: %v", plan.ID, err)
	}
	if !jsonMode() {
		fmt.Printf("Plan %s approved by %s (artifact %s)\n", plan.ID, approver, digest)
	}
	return nil
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

// resolveApprover returns the approving principal: --approver, else the
// current OS user.
func resolveApprover() (string, error) {
	if a := strings.TrimSpace(publishFlags.approver); a != "" {
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
	return "", errors.New("no approver identity could be determined: pass --approver <id>")
}

// isInteractive reports whether stdin is a terminal — the condition under
// which a prompt is offered. Presence alone never implies approval.
func isInteractive() bool {
	return publishTTY()
}

// digestApprovalHint explains the approval requirement for the current
// interface.
func digestApprovalHint() string {
	if isInteractive() {
		return "(you will be prompted per plan)"
	}
	return "(non-interactive: --digest <artifact digest> is required)"
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
	case status == "failed" || status == "partially_succeeded":
		return "publication is incomplete (a target failed): resume with 'newsroom publish " + string(id) + " --resume' (already-published targets keep their receipts and are not re-published)"
	case err != nil:
		return err.Error()
	}
	return "publication failed"
}

// ---------------------------------------------------------------------------
// Presentation: text
// ---------------------------------------------------------------------------

func jsonMode() bool {
	return strings.ToLower(strings.TrimSpace(publishFlags.format)) == "json"
}

// emitJSON builds and prints the JSON report (when --format json) for a
// terminal state of the invocation. views are the plans the invocation
// touched (empty before any plan exists); published records whether the
// invocation published anything; errMsg is the terminal error message.
func emitJSON(m *run.Manifest, ready bool, readyReason string, pub *presets.Preset, mode string, code int, views []planView, published bool, errMsg string) {
	if !jsonMode() {
		return
	}
	r := pubReport{
		Run:       pubRunOf(m),
		Ready:     &pubReady{Ready: ready, Reason: readyReason},
		Mode:      mode,
		Published: published,
		ExitCode:  code,
		Error:     errMsg,
	}
	if pub != nil {
		r.Preset = &pubPreset{Name: pub.Name, Hash: pub.Hash, BundleRoot: pub.BundleRoot}
	}
	for i := range views {
		r.Plans = append(r.Plans, pubReportPlan(views[i].plan, views[i].dest, views[i].sum))
	}
	pubReportPrint(r)
}

func warnf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[newsroom publish] "+format+"\n", args...)
}

func displayTopic(m *run.Manifest) string {
	if m.Topic != "" {
		return m.Topic
	}
	return "(no topic)"
}

func destName(dest *presets.Destination) string {
	if dest == nil {
		return "unknown destination"
	}
	return dest.Key
}

func destTargetLabel(dest *presets.Destination) string {
	if dest == nil {
		return ""
	}
	s := string(dest.Target.ID)
	if dest.Target.Destination != "" {
		s += " -> " + dest.Target.Destination
	}
	return s
}

func readyLabel(ready bool, reason string) string {
	if ready {
		return "yes (run succeeded, final check passed, review READY)"
	}
	return "no — " + reason
}

func planTargetIDs(plan *publication.Plan) string {
	ids := make([]string, 0, len(plan.Targets))
	for _, t := range plan.Targets {
		ids = append(ids, string(t.ID))
	}
	return strings.Join(ids, ", ")
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := []string{}
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

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

// printRunHeader prints the run identity block every report leads with.
func printRunHeader(m *run.Manifest, pub *presets.Preset, ready bool, readyReason string) {
	fmt.Printf("Run %s\n", m.ID)
	fmt.Printf("  Topic:  %s\n", displayTopic(m))
	fmt.Printf("  Status: %s\n", m.Status)
	if pub != nil {
		fmt.Printf("  Preset: %s (hash %s)\n", pub.Name, shortHash(pub.Hash))
	}
	fmt.Printf("  Ready:  %s\n", readyLabel(ready, readyReason))
	fmt.Println()
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12] + "…"
	}
	return h
}

// printNotReady explains why the run cannot be published yet.
func printNotReady(m *run.Manifest, reason string) {
	fmt.Printf("Run %s\n", m.ID)
	fmt.Printf("  Topic:  %s\n", displayTopic(m))
	fmt.Printf("  Status: %s\n", m.Status)
	fmt.Printf("  Ready:  no — %s\n", reason)
	fmt.Println()
	fmt.Println("Publication is refused: the run is not ready for publication.")
	fmt.Printf("Inspect the verdict with: newsroom review %s\n", m.ID)
}

// previewVerdict renders a target summary's rehearsal verdict.
func previewVerdict(t publish.TargetSummary) string {
	switch {
	case t.Preview == nil:
		return "no rehearsal (publisher unavailable)"
	case t.Preview.OK:
		return "ok"
	default:
		return "blocked"
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// printPlanBlock renders one plan: its target, artifact, destination facts,
// rehearsal, approval, receipts, and warnings.
func printPlanBlock(v *planView) {
	plan := v.plan
	suffix := ""
	if v.kind != kindNew {
		suffix = " — " + v.kind.label()
	}
	fmt.Printf("Plan %s — %s (status %s)%s\n", plan.ID, destName(v.dest), plan.Status, suffix)
	for _, t := range plan.Targets {
		destLine := t.Destination
		if destLine == "" {
			destLine = "-"
		}
		fmt.Printf("  target:   %s (%s/%s -> %s)\n", t.ID, t.Type, t.Provider, destLine)
	}
	fmt.Printf("  artifact: %s\n", plan.Artifact.Path)
	fmt.Printf("  digest:   %s\n", plan.Artifact.Digest)
	if v.dest != nil && len(v.dest.Facts) > 0 {
		for _, k := range sortedKeys(v.dest.Facts) {
			fmt.Printf("  %-11s %s\n", k+":", v.dest.Facts[k])
		}
	}
	if v.pv != nil {
		for _, t := range v.pv.Targets {
			fmt.Printf("  preview:  %s — %s\n", t.Name, previewVerdict(t))
			if t.Preview != nil {
				if t.Preview.Details != "" {
					fmt.Printf("             %s\n", t.Preview.Details)
				}
				for _, k := range sortedKeys(t.Preview.Metadata) {
					fmt.Printf("             %s: %s\n", k, t.Preview.Metadata[k])
				}
				for _, issue := range t.Preview.Issues {
					fmt.Printf("             issue: %s\n", issue.Message)
				}
			}
		}
	}
	if v.pvErr != nil {
		fmt.Printf("  preview:  unavailable (%v)\n", v.pvErr)
	}
	if len(plan.Approvals) > 0 {
		a := plan.Approvals[len(plan.Approvals)-1]
		fmt.Printf("  approval: %s by %s (%s)\n", a.ID, a.ApprovedBy, a.Status)
	} else {
		fmt.Println("  approval: none")
	}
	printPlanReceipts(plan)
	for _, w := range plan.Warnings {
		fmt.Printf("  warning:  %s\n", w.Message)
	}
	for _, e := range plan.Errors {
		fmt.Printf("  error:    %s\n", e.Message)
	}
	fmt.Println()
}

// printPlanReceipts prints the plan's existing receipts (the "previous
// publication receipts" every report shows).
func printPlanReceipts(plan *publication.Plan) {
	for _, t := range plan.Targets {
		if t.Receipt == nil {
			continue
		}
		r := t.Receipt
		line := fmt.Sprintf("  receipt:  %s", t.ID)
		if r.ExternalURI != "" {
			line += fmt.Sprintf(" — %s", r.ExternalURI)
		}
		if r.ExternalRecordID != "" {
			line += fmt.Sprintf(" (record %s)", r.ExternalRecordID)
		}
		fmt.Println(line)
	}
}

// printNewPreview renders the prepared plans with their per-target previews.
func printNewPreview(m *run.Manifest, pub *presets.Preset, ready bool, views []planView) {
	if jsonMode() {
		return
	}
	printRunHeader(m, pub, ready, "")
	mode := "publication plan (preview)"
	if publishFlags.dryRun {
		mode = "dry run"
	}
	fmt.Printf("%s — %d plan(s), no external side effects\n\n", mode, len(views))
	for i := range views {
		printPlanBlock(&views[i])
	}
}

// printPreviewNextSteps tells the operator exactly how to proceed.
func printPreviewNextSteps(id run.RunID, views []planView) {
	if jsonMode() {
		return
	}
	fmt.Println("No external side effects were performed.")
	fmt.Printf("To publish:  newsroom publish %s --approve %s\n", id, digestApprovalHint())
	for _, v := range views {
		if v.kind == kindAlreadyPublished {
			continue
		}
		fmt.Printf("  digest:   %s  (plan %s)\n", v.plan.Artifact.Digest, v.plan.ID)
	}
	if !isInteractive() {
		fmt.Println("Non-interactive: confirm each artifact digest with --digest <digest> (one per plan).")
	}
}

// actionableCount counts the views that still do something (everything else
// is an already-published no-op).
func actionableCount(views []planView) int {
	n := 0
	for _, v := range views {
		if v.kind != kindAlreadyPublished {
			n++
		}
	}
	return n
}

// printExistingPlan renders the report for an existing plan.
func printExistingPlan(m *run.Manifest, pub *presets.Preset, view *planView, ready bool, readyReason string, completedNoop bool) {
	if jsonMode() {
		return
	}
	printRunHeader(m, pub, ready, readyReason)
	if completedNoop {
		fmt.Printf("Plan %s is already %s — nothing to do.\n\n", view.plan.ID, view.plan.Status)
		printPlanReceipts(view.plan)
		fmt.Println()
		return
	}
	printPlanBlock(view)
}

// printExistingPlanNextSteps tells the operator how to run the plan's cycle.
func printExistingPlanNextSteps(id run.RunID, plan *publication.Plan) {
	if jsonMode() {
		return
	}
	fmt.Println("No external side effects were performed.")
	switch {
	case !planApproved(plan):
		fmt.Printf("The plan has no effective approval (artifact %s).\n", plan.Artifact.Digest)
		fmt.Printf("To publish: newsroom publish %s --resume --approve %s\n", id, digestApprovalHint())
	default:
		fmt.Printf("The plan is approved (artifact %s).\n", plan.Artifact.Digest)
		fmt.Printf("To publish/resume: newsroom publish %s --resume\n", id)
	}
}

// printCycleResult renders the outcome of a publication cycle for one plan.
func printCycleResult(plan *publication.Plan, dest *presets.Destination, sum *publish.Summary) {
	if jsonMode() {
		return
	}
	fmt.Printf("Publishing plan %s (%s)\n", plan.ID, destName(dest))
	if sum == nil {
		fmt.Println("  (no summary)")
		return
	}
	if sum.Interrupted {
		fmt.Printf("  interrupted: %s (resumable)\n", sum.Reason)
	}
	for _, t := range sum.Targets {
		line := fmt.Sprintf("  target %s: %s", t.ID, t.Status)
		if t.Receipt != nil {
			if t.Receipt.ExternalURI != "" {
				line += fmt.Sprintf(" — %s", t.Receipt.ExternalURI)
			} else if t.Receipt.ExternalRecordID != "" {
				line += fmt.Sprintf(" — record %s", t.Receipt.ExternalRecordID)
			}
		}
		if t.Error != nil {
			line += fmt.Sprintf(" — error: %s", t.Error.Message)
		}
		fmt.Println(line)
	}
	for _, r := range sum.Remediation {
		line := fmt.Sprintf("  remediation [%s]: %s", r.Kind, r.Action)
		if r.URI != "" {
			line += fmt.Sprintf(" (%s)", r.URI)
		}
		fmt.Println(line)
	}
	for _, w := range sum.Warnings {
		fmt.Printf("  warning: %s\n", w.Message)
	}
}
