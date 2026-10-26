package main

// Tests for the TUI's concrete publication service (v0.6, #122).
//
// They drive the REAL publication stack hermetically: a fresh workspace,
// the canonical stages, a real editorial preset (the "demo" bundle of the
// publish tests), and fake publishers in place of git/Bluesky. They cover
// the projection (ready / not-ready / no preset / stale content), the
// explicit approval (bound to the exact digest, method "tui"), the full
// cycle (receipts, idempotency), the partial failure + resume, the
// cancellation of an in-flight cycle, and the mutual exclusion with a
// running run cycle.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/config"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publish"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publish/presets"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/tui"
)

// newTuiPublishEnv builds the publish test environment (see
// newPublishEnv) and a tuiApp wired to it: the app's config points the
// editorial preset at the fixture bundle, and the tui flags point at the
// fixture workspace.
func newTuiPublishEnv(t *testing.T, kind string, notReady bool) (*tuiApp, *publishEnv) {
	t.Helper()
	env := newPublishEnv(t, kind, notReady)

	cfgRoot := t.TempDir()
	cfgFile := filepath.Join(cfgRoot, "config.yaml")
	content := fmt.Sprintf(`
editorial:
  bundle_root: %s
  publication: demo
`, env.bundleRoot)
	if err := os.WriteFile(cfgFile, []byte(content), 0o644); err != nil {
		t.Fatalf("writing the fixture config: %v", err)
	}
	f, err := config.Load(cfgFile)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	swapAppConfig(t, config.New(f, cfgFile))

	oldWS, oldRoot := tf.workspace, tf.workspaceRoot
	tf.workspace = "testws"
	tf.workspaceRoot = env.wsRoot
	t.Cleanup(func() { tf.workspace, tf.workspaceRoot = oldWS, oldRoot })

	app, err := newTuiApp()
	if err != nil {
		t.Fatalf("newTuiApp: %v", err)
	}
	return app, env
}

// newTuiPublishEnvNoPreset is like newTuiPublishEnv but the config selects
// no publication preset (the run also recorded none).
func newTuiPublishEnvNoPreset(t *testing.T, kind string) (*tuiApp, *publishEnv) {
	t.Helper()
	app, env := newTuiPublishEnv(t, kind, false)
	cfgRoot := t.TempDir()
	cfgFile := filepath.Join(cfgRoot, "config.yaml")
	if err := os.WriteFile(cfgFile, []byte("# no editorial section\n"), 0o644); err != nil {
		t.Fatalf("writing the config: %v", err)
	}
	f, err := config.Load(cfgFile)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	swapAppConfig(t, config.New(f, cfgFile))
	return app, env
}

// waitPub waits for the app's in-flight publication cycle to finish.
func waitPub(t *testing.T, app *tuiApp) {
	t.Helper()
	done := app.PublicationDone()
	if done == nil {
		t.Fatal("no in-flight publication to wait for")
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the publication cycle did not finish within 30s")
	}
}

// waitForPubInFlight polls until the publication cycle is in flight.
func waitForPubInFlight(t *testing.T, app *tuiApp) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if app.PublicationInFlight() != "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the publication cycle never went in flight")
}

// blockingPublisher blocks Publish until the context is cancelled (a slow
// external API), so a cycle can be observed in flight and cancelled.
type blockingPublisher struct {
	block bool
	plan  publication.PublicationID
}

func (b *blockingPublisher) Publish(ctx context.Context, content []byte, _ *publication.PublicationReceipt) (*publication.PublicationReceipt, error) {
	if b.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return publication.NewReceipt(b.plan, "site", "fake", "https://example.com", "rec-1",
		publication.DigestOf(content), "idem-1", "test/0.6", nil, publishFixtureTime), nil
}

func (b *blockingPublisher) Preview(_ context.Context, content []byte, _ *publication.PublicationReceipt) (*publish.Preview, error) {
	return &publish.Preview{OK: true, Details: "ok"}, nil
}

// ---------------------------------------------------------------------------
// The projection
// ---------------------------------------------------------------------------

func TestTuiPublicationStateReadyRun(t *testing.T) {
	app, env := newTuiPublishEnv(t, "article", false)
	ctx := context.Background()

	st, err := app.PublicationState(ctx, env.runID, tui.PublicationOptions{Prepare: true, Preview: true})
	if err != nil {
		t.Fatalf("PublicationState: %v", err)
	}
	if !st.Ready {
		t.Fatalf("Ready = false (%s), want ready", st.ReadyReason)
	}
	if st.RunStatus != "succeeded" {
		t.Errorf("RunStatus = %q", st.RunStatus)
	}
	if st.PresetName != "demo" {
		t.Errorf("PresetName = %q, want demo", st.PresetName)
	}
	if st.FinalCheck != "pass" {
		t.Errorf("FinalCheck = %q, want pass", st.FinalCheck)
	}
	// An article run has both destinations available.
	if len(st.Plans) != 2 {
		t.Fatalf("plans = %d, want 2 (hugo + bluesky): %+v", len(st.Plans), st.Plans)
	}
	for _, p := range st.Plans {
		if p.Status != "planned" {
			t.Errorf("plan %s status = %q, want planned", p.PlanID, p.Status)
		}
		if p.ArtifactDigest == "" || p.ArtifactPath == "" {
			t.Errorf("plan %s: artifact baseline missing: %+v", p.PlanID, p)
		}
		if len(p.Targets) != 1 {
			t.Errorf("plan %s: targets = %d, want 1", p.PlanID, len(p.Targets))
		}
		if p.Targets[0].PreviewOK == nil || !*p.Targets[0].PreviewOK {
			t.Errorf("plan %s: preview not ok: %+v", p.PlanID, p.Targets[0])
		}
	}
	if !st.Approvable {
		t.Errorf("a ready run with plans must be approvable (blocker %q)", st.ApproveBlocker)
	}
	if st.Resumable {
		t.Errorf("nothing failed, so nothing must be resumable (blocker %q)", st.ResumeBlocker)
	}

	// The plans are persisted: a fresh read-only projection sees them.
	// again, err := ...
	st2, err := app.PublicationState(ctx, env.runID, tui.PublicationOptions{})
	if err != nil {
		t.Fatalf("read-only projection: %v", err)
	}
	if len(st2.Plans) != 2 {
		t.Errorf("read projection plans = %d, want 2", len(st2.Plans))
	}
}

func TestTuiPublicationStateNotReadyRun(t *testing.T) {
	app, env := newPublishEnvOnly(t, "article", true)
	st, err := app.PublicationState(context.Background(), env.runID, tui.PublicationOptions{Prepare: true, Preview: true})
	if err != nil {
		t.Fatalf("PublicationState: %v", err)
	}
	if st.Ready {
		t.Fatal("a failing final check must not be ready")
	}
	if st.Approvable {
		t.Error("a not-ready run must not be approvable")
	}
	if st.ApproveBlocker == "" {
		t.Error("a blocked approval must carry a reason")
	}
	if len(st.Plans) != 0 {
		t.Errorf("a not-ready run must get no plans (a plan persists the gate): %d", len(st.Plans))
	}
	if err := app.ApprovePublication(context.Background(), env.runID); err == nil {
		t.Error("approving a not-ready run must be refused")
	}
}

func TestTuiPublicationStateNoPreset(t *testing.T) {
	app, env := newTuiPublishEnvNoPreset(t, "article")
	st, err := app.PublicationState(context.Background(), env.runID, tui.PublicationOptions{Prepare: true, Preview: true})
	if err != nil {
		t.Fatalf("PublicationState: %v", err)
	}
	if st.PresetName != "" {
		t.Errorf("PresetName = %q, want empty", st.PresetName)
	}
	if st.NoPresetReason == "" {
		t.Error("a run without a preset must explain why")
	}
	if st.Approvable {
		t.Error("without a preset nothing must be approvable")
	}
	if len(st.Plans) != 0 {
		t.Errorf("plans = %d, want 0", len(st.Plans))
	}
	if err := app.ApprovePublication(context.Background(), env.runID); err == nil {
		t.Error("approving without a preset must be refused")
	}
}

func TestTuiPublicationStateUnknownRun(t *testing.T) {
	app, _ := newTuiPublishEnv(t, "article", false)
	if _, err := app.PublicationState(context.Background(), "run-00000000-0000-0000-0000-000000000000", tui.PublicationOptions{}); err == nil {
		t.Error("an unknown run must produce an error")
	}
}

// ---------------------------------------------------------------------------
// The explicit approval + full cycle
// ---------------------------------------------------------------------------

func TestTuiApprovePublishesAndIsIdempotent(t *testing.T) {
	app, env := newTuiPublishEnv(t, "article", false)
	ctx := context.Background()

	if _, err := app.PublicationState(ctx, env.runID, tui.PublicationOptions{Prepare: true, Preview: true}); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := app.ApprovePublication(ctx, env.runID); err != nil {
		t.Fatalf("ApprovePublication: %v", err)
	}
	waitPub(t, app)

	// Both fakes published exactly once; both plans ended succeeded with
	// receipts.
	if env.fakes["hugo"].Calls() != 1 || env.fakes["bluesky"].Calls() != 1 {
		t.Errorf("publisher calls = hugo:%d bluesky:%d, want 1 each", env.fakes["hugo"].Calls(), env.fakes["bluesky"].Calls())
	}
	st, err := app.PublicationState(ctx, env.runID, tui.PublicationOptions{})
	if err != nil {
		t.Fatalf("final projection: %v", err)
	}
	for _, p := range st.Plans {
		if p.Status != "succeeded" {
			t.Errorf("plan %s (%s) status = %q, want succeeded", p.PlanID, p.Key, p.Status)
		}
		if p.Targets[0].ReceiptURI == "" {
			t.Errorf("plan %s: no receipt URI", p.PlanID)
		}
		if p.ApprovalMethod != "tui" {
			t.Errorf("plan %s: approval method = %q, want tui", p.PlanID, p.ApprovalMethod)
		}
		if !p.ApprovalEffective {
			t.Errorf("plan %s: the approval must be effective for the published bytes", p.PlanID)
		}
	}
	if st.Approvable {
		t.Error("fully published plans must not be approvable again")
	}
	// Idempotent refusal: no new approval, no new publication.
	if err := app.ApprovePublication(ctx, env.runID); err == nil {
		t.Error("a second approval of fully published content must be refused")
	}
	if env.fakes["hugo"].Calls() != 1 || env.fakes["bluesky"].Calls() != 1 {
		t.Error("the refused second approval must not publish again")
	}

	// The approval record itself: bound to the exact artifact digest,
	// method tui.
	id := run.RunID(env.runID)
	eng := publish.New(app.store)
	for _, pid := range mustListPlanIDs(t, eng, id) {
		plan, err := eng.Plan(id, pid)
		if err != nil {
			t.Fatalf("plan %s: %v", pid, err)
		}
		n := len(plan.Approvals)
		if n != 1 {
			t.Fatalf("plan %s: approvals = %d, want 1", pid, n)
		}
		a := plan.Approvals[0]
		if a.Method != publication.ApprovalMethodTUI {
			t.Errorf("plan %s: method = %q, want tui", pid, a.Method)
		}
		if !a.ArtifactDigest.Matches(string(plan.Artifact.Digest)) {
			t.Errorf("plan %s: the approval is not bound to the plan's artifact digest", pid)
		}
	}
}

func mustListPlanIDs(t *testing.T, eng *publish.Engine, id run.RunID) []publication.PublicationID {
	t.Helper()
	ids, err := eng.ListPlanIDs(id)
	if err != nil {
		t.Fatalf("ListPlanIDs: %v", err)
	}
	return ids
}

func TestTuiApproveRefusedWhenContentStale(t *testing.T) {
	app, env := newTuiPublishEnv(t, "article", false)
	ctx := context.Background()

	if _, err := app.PublicationState(ctx, env.runID, tui.PublicationOptions{Prepare: true, Preview: true}); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	// Change the canonical content after the plan baseline was recorded:
	// the approval must fail closed (and the projection must say stale).
	st, err := app.PublicationState(ctx, env.runID, tui.PublicationOptions{})
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	var victim *tui.PublicationPlan
	for i := range st.Plans {
		if st.Plans[i].ArtifactPath != "" {
			victim = &st.Plans[i]
			break
		}
	}
	if victim == nil {
		t.Fatal("no plan with an artifact path to tamper with")
	}
	name := strings.TrimPrefix(victim.ArtifactPath, run.PublicationContentDir+"/")
	if name == victim.ArtifactPath {
		t.Fatalf("artifact path %q is not under %s", victim.ArtifactPath, run.PublicationContentDir)
	}
	eng := publish.New(app.store)
	if _, err := eng.SaveContent(run.RunID(env.runID), name, []byte("tampered content\n")); err != nil {
		t.Fatalf("SaveContent: %v", err)
	}
	// A fresh projection reflects the tamper (projections are snapshots).
	st, err = app.PublicationState(ctx, env.runID, tui.PublicationOptions{})
	if err != nil {
		t.Fatalf("projection after tamper: %v", err)
	}
	stale := false
	for _, p := range st.Plans {
		if p.ArtifactPath == victim.ArtifactPath && p.ContentStale {
			stale = true
		}
	}
	if !stale {
		t.Error("the tampered content must be reported as stale in the projection")
	}
	err = app.ApprovePublication(ctx, env.runID)
	if err == nil {
		t.Fatal("approving changed content must fail closed")
	}
	if !strings.Contains(err.Error(), "changed") && !strings.Contains(err.Error(), "stale") {
		t.Errorf("refusal = %q, want it to name the staleness", err)
	}
	if app.PublicationInFlight() != "" {
		t.Error("a refused approval must not start a cycle")
	}
}

func TestTuiApproveRefusedWhileRunInFlight(t *testing.T) {
	app, env := newTuiPublishEnv(t, "article", false)
	app.mu.Lock()
	app.runningID = "run-other"
	app.mu.Unlock()
	t.Cleanup(func() {
		app.mu.Lock()
		app.runningID = ""
		app.mu.Unlock()
	})
	if err := app.ApprovePublication(context.Background(), env.runID); err == nil {
		t.Error("approval must be refused while a run cycle is in flight")
	}
}

// ---------------------------------------------------------------------------
// Partial failure + resume
// ---------------------------------------------------------------------------

func TestTuiPartialFailureAndResume(t *testing.T) {
	app, env := newTuiPublishEnv(t, "article", false)
	ctx := context.Background()

	// The Bluesky API refuses exactly the first publish (a transient
	// failure), then succeeds.
	env.fakes["bluesky"].failOnce = true

	if _, err := app.PublicationState(ctx, env.runID, tui.PublicationOptions{Prepare: true, Preview: true}); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := app.ApprovePublication(ctx, env.runID); err != nil {
		t.Fatalf("ApprovePublication: %v", err)
	}
	waitPub(t, app)

	// Hugo succeeded; the thread plan failed with a remediation hint.
	st, err := app.PublicationState(ctx, env.runID, tui.PublicationOptions{})
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	var bsky, hugo *tui.PublicationPlan
	for i := range st.Plans {
		switch st.Plans[i].Key {
		case "bluesky":
			bsky = &st.Plans[i]
		case "hugo":
			hugo = &st.Plans[i]
		}
	}
	if hugo == nil || bsky == nil {
		t.Fatalf("expected both plans, got %+v", st.Plans)
	}
	if hugo.Status != "succeeded" {
		t.Fatalf("hugo status = %q, want succeeded", hugo.Status)
	}
	if bsky.Status != "failed" {
		t.Fatalf("bluesky status = %q, want failed", bsky.Status)
	}
	if bsky.Targets[0].Error == "" {
		t.Error("the failed target must carry its error")
	}
	if len(bsky.Remediation) == 0 {
		t.Error("a failed cycle must carry remediation steps")
	}
	if !st.Resumable {
		t.Fatalf("a failed plan with an approval must be resumable (blocker %q)", st.ResumeBlocker)
	}

	// The transient failure clears; resume under the existing approval.
	env.fakes["bluesky"].failOnce = false
	if err := app.ResumePublication(ctx, env.runID); err != nil {
		t.Fatalf("ResumePublication: %v", err)
	}
	waitPub(t, app)

	if env.fakes["hugo"].Calls() != 1 {
		t.Errorf("hugo calls = %d, want 1 (a published site is never re-published)", env.fakes["hugo"].Calls())
	}
	if env.fakes["bluesky"].Calls() != 2 {
		t.Errorf("bluesky calls = %d, want 2 (one failed attempt + the resume)", env.fakes["bluesky"].Calls())
	}
	st, err = app.PublicationState(ctx, env.runID, tui.PublicationOptions{})
	if err != nil {
		t.Fatalf("final projection: %v", err)
	}
	for _, p := range st.Plans {
		if p.Status != "succeeded" {
			t.Errorf("plan %s (%s) status = %q, want succeeded after resume", p.PlanID, p.Key, p.Status)
		}
	}
	if st.Resumable {
		t.Error("nothing failed after the resume")
	}
	// A resume of a fully succeeded run is refused.
	if err := app.ResumePublication(ctx, env.runID); err == nil {
		t.Error("resuming with nothing failed must be refused")
	}
}

// ---------------------------------------------------------------------------
// Cancellation of the in-flight cycle
// ---------------------------------------------------------------------------

func TestTuiCancelInFlightPublicationAndResume(t *testing.T) {
	app, env := newTuiPublishEnv(t, "article", false)
	ctx := context.Background()

	// Hugo blocks until the cycle is cancelled; the fakes take over for
	// the resume.
	bpub := &blockingPublisher{block: true}
	oldFactory := publisherFactory
	publisherFactory = func(_ context.Context, d *presets.Destination, planID publication.PublicationID) (publish.Publisher, error) {
		if d.Key == "hugo" {
			bpub.plan = planID
			return bpub, nil
		}
		f := env.fakes[d.Key]
		f.planID = planID
		return f, nil
	}
	t.Cleanup(func() { publisherFactory = oldFactory })

	if _, err := app.PublicationState(ctx, env.runID, tui.PublicationOptions{Prepare: true, Preview: true}); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := app.ApprovePublication(ctx, env.runID); err != nil {
		t.Fatalf("ApprovePublication: %v", err)
	}
	waitForPubInFlight(t, app)

	// Cancel the in-flight cycle.
	app.CancelPublication()
	waitPub(t, app)
	if app.PublicationInFlight() != "" {
		t.Error("after cancel + done the in-flight state must be clear")
	}
	if err := app.PublicationCycleError(); err == "" {
		t.Error("a cancelled cycle must record why it stopped")
	} else if !strings.Contains(err, "cancel") && !strings.Contains(err, "interrupt") {
		t.Errorf("cycle stop reason = %q, want it to name the cancellation", err)
	}

	// The state is persisted and resumable (the approval survived the
	// interruption; the content is unchanged).
	st, err := app.PublicationState(ctx, env.runID, tui.PublicationOptions{})
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	if !st.Resumable {
		t.Fatalf("an interrupted cycle must stay resumable (blocker %q)", st.ResumeBlocker)
	}

	// The resume runs with fast publishers.
	bpub.block = false
	if err := app.ResumePublication(ctx, env.runID); err != nil {
		t.Fatalf("ResumePublication: %v", err)
	}
	waitPub(t, app)
	st, err = app.PublicationState(ctx, env.runID, tui.PublicationOptions{})
	if err != nil {
		t.Fatalf("final projection: %v", err)
	}
	for _, p := range st.Plans {
		if p.Status != "succeeded" {
			t.Errorf("plan %s (%s) status = %q, want succeeded after resume", p.PlanID, p.Key, p.Status)
		}
	}
}

// newPublishEnvOnly builds the fixture environment and a bare tuiApp (no
// editorial preset in the config) for gate tests.
func newPublishEnvOnly(t *testing.T, kind string, notReady bool) (*tuiApp, *publishEnv) {
	t.Helper()
	app, env := newTuiPublishEnv(t, kind, notReady)
	return app, env
}
