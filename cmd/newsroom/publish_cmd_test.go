// Tests for newsroom publish (v0.6, issue #121).
//
// The tests drive the real command in-process (runCommand) against a real
// workspace store, a real runner-produced run (ready or not, through the
// canonical stages), a real editorial bundle on disk, and a fake publisher
// injected through the presets factory — so the full CLI path (flags,
// preset resolution, readiness, plan reuse, approval, cycle, presentation)
// is verified without a checkout, a network, or an LLM.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publish"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publish/presets"
	"github.com/Mundo-Dolphins/local-newsroom/internal/review"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/runner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workspace"
)

// publishFixtureTime is the fixed run start time (deterministic run IDs).
var publishFixtureTime = time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)

// ---------------------------------------------------------------------------
// Fake publisher
// ---------------------------------------------------------------------------

// fakePublisher is a deterministic stand-in for the git/Bluesky adapters:
// it records every Publish call and the exact content bytes it received.
type fakePublisher struct {
	targetID publication.TargetID
	planID   publication.PublicationID
	uri      string
	fail     bool // fail every publish
	failOnce bool // fail exactly the first publish

	mu       sync.Mutex
	calls    int
	contents [][]byte
}

func (f *fakePublisher) Publish(_ context.Context, content []byte, _ *publication.PublicationReceipt) (*publication.PublicationReceipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.contents = append(f.contents, append([]byte(nil), content...))
	if f.fail || (f.failOnce && f.calls == 1) {
		return nil, fmt.Errorf("fake external failure (target %s)", f.targetID)
	}
	return publication.NewReceipt(f.planID, f.targetID, "fake", f.uri,
		fmt.Sprintf("rec-%d", f.calls), publication.DigestOf(content),
		fmt.Sprintf("idem-%d", f.calls), "test/0.6", nil, publishFixtureTime), nil
}

func (f *fakePublisher) Preview(_ context.Context, content []byte, _ *publication.PublicationReceipt) (*publish.Preview, error) {
	return &publish.Preview{
		OK:       true,
		Details:  fmt.Sprintf("fake rehearsal: %d bytes", len(content)),
		Metadata: map[string]string{"fake": "yes"},
	}, nil
}

func (f *fakePublisher) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// ---------------------------------------------------------------------------
// Environment: workspace + bundle + one runner-produced run + fakes
// ---------------------------------------------------------------------------

type publishEnv struct {
	wsRoot     string
	bundleRoot string
	runID      string
	fakes      map[string]*fakePublisher
}

// newPublishEnv builds the publish test environment:
//
//   - a hermetic process environment (setHermeticEnv),
//   - an empty workspace root,
//   - an editorial bundle with one preset ("demo") and a local site root,
//   - one fully succeeded run through the canonical stages, with a VALID
//     write artifact (the bridge parses it) and rendered output,
//   - fake publishers injected for every preset destination,
//   - a pinned non-interactive approval interface.
//
// kind selects the run's content: "article" (article run: hugo available,
// thread derived from the body) or "thread" (thread run: hugo unavailable,
// thread from the posts).
//
// notReady flips the final factual check to fail (the run is otherwise
// succeeded), for gate tests.
func newPublishEnv(t *testing.T, kind string, notReady bool) *publishEnv {
	t.Helper()
	setHermeticEnv(t)

	wsRoot := t.TempDir()
	bundleRoot := t.TempDir()

	demoDir := filepath.Join(bundleRoot, "publications", "demo")
	mustWriteFile(t, filepath.Join(demoDir, "publication.yaml"),
		"schema_version: 1\n"+
			"profile: newsroom\n"+
			"language: en\n"+
			"renderer: "+kind+"\n"+
			"hugo: hugo.yaml\n"+
			"bluesky: bluesky.yaml\n")
	mustWriteFile(t, filepath.Join(demoDir, "hugo.yaml"),
		"destination: site\n"+
			"base_url: https://example.com\n")
	mustWriteFile(t, filepath.Join(demoDir, "bluesky.yaml"),
		"handle: \"@demo\"\n"+
			"api_endpoint: https://public.api.bsky.app\n"+
			"api_key:\n"+
			"  env: FAKED_BSKY_KEY\n")
	if err := os.MkdirAll(filepath.Join(bundleRoot, "site", "content"), 0o755); err != nil {
		t.Fatalf("creating the fixture site: %v", err)
	}

	// The run: all canonical stages succeeded (offline executors).
	m, err := run.NewManifest(run.NewManifestParams{
		Workspace:  run.Workspace{Name: "testws"},
		Topic:      "City approves rooftop solar program",
		SourceMode: run.SourceModeExplicit,
		StartedAt:  publishFixtureTime,
	})
	if err != nil {
		t.Fatalf("NewManifest: %v", err)
	}

	writeData, renderFiles := fixtureRender(kind, notReady)
	stages := []runner.Stage{
		{Name: run.CanonicalStages()[0], Fn: fixtureWriteFile("sources/index.json", run.ArtifactKindSource, "sources", `{"urls":["https://example.com/solar"]}`)},
		{Name: run.CanonicalStages()[1], Fn: fixtureWriteFile("fetch/index.json", run.ArtifactKindSource, "fetch", `{"fetched":1}`)},
		{Name: run.CanonicalStages()[2], Fn: fixtureWriteFile("documents/index.json", run.ArtifactKindDocument, "documents", `{"documents":1}`)},
		{Name: run.CanonicalStages()[3], Fn: fixtureWriteFile("research/dossier.json", run.ArtifactKindDossier, "dossier", `{"claims":[]}`)},
		{Name: run.CanonicalStages()[4], Fn: fixtureWriteFile("verify/verification.json", run.ArtifactKindVerification, "verification", `{"verdict":"verified"}`)},
		{Name: run.CanonicalStages()[5], SkipReason: "no unresolved claims"},
		{Name: run.CanonicalStages()[6], Fn: fixtureWriteFile(run.ArtifactPathArtifact, run.ArtifactKindEditorial, "editorial", string(writeData))},
		{Name: run.CanonicalStages()[7], Fn: fixtureWriteFile("final_check/result.json", run.ArtifactKindFactualCheck, "final-check", `{"overall_status":"`+finalCheckStatus(notReady)+`"}`)},
		{Name: run.CanonicalStages()[8], Fn: fixtureWriteFiles(renderFiles...)},
	}

	store, err := workspace.NewStore(wsRoot)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	res, err := runner.Run(context.Background(), runner.Config{Manifest: m, Store: store, Stages: stages})
	if err != nil {
		t.Fatalf("runner.Run: %v", err)
	}
	if res.Status != run.RunStatusSucceeded {
		t.Fatalf("run status = %s, want succeeded", res.Status)
	}

	// The fixture must produce exactly the readiness the test wants.
	bundle, err := review.Build(store, m)
	if err != nil {
		t.Fatalf("review.Build: %v", err)
	}
	ready, reason := review.Readiness(bundle)
	if ready != !notReady {
		t.Fatalf("fixture readiness = %v (%s), want %v", ready, reason, !notReady)
	}

	fakes := map[string]*fakePublisher{
		"hugo":    {targetID: "site", uri: "https://example.com/solar"},
		"bluesky": {targetID: "thread", uri: "https://bsky.app/demo/thread"},
	}
	injectFakePublishers(t, fakes)
	pinPublishInterface(t, false, "")

	return &publishEnv{wsRoot: wsRoot, bundleRoot: bundleRoot, runID: string(m.ID), fakes: fakes}
}

// fixtureRender returns the write stage data (a VALID EditorialArtifact)
// and the render stage files for the kind.
func fixtureRender(kind string, notReady bool) (writeData []byte, renderFiles []runner.StageFile) {
	meta := contracts.GenerationMetadata{
		InputVerificationID: "verification-fixture-1",
		GeneratedAt:         publishFixtureTime,
	}
	switch kind {
	case "thread":
		art := contracts.EditorialArtifact{
			StableID:           "artifact-fixture-1",
			ArtifactType:       contracts.ArtifactTypeThread,
			Title:              "City approves rooftop solar program",
			Posts:              []contracts.Post{{Order: 0, Body: "The city approved a rooftop solar program on Monday. (1/2)"}},
			GenerationMetadata: meta,
		}
		writeData, _ = json.Marshal(art)
		renderFiles = []runner.StageFile{
			{Path: run.ArtifactPathThread, Kind: run.ArtifactKindRendered, ID: "thread", Data: []byte("The city approved a rooftop solar program on Monday. (1/2)")},
		}
	default: // article
		art := contracts.EditorialArtifact{
			StableID:     "artifact-fixture-1",
			ArtifactType: contracts.ArtifactTypeArticle,
			Title:        "City Approves Rooftop Solar Program",
			Subtitle:     "Sunlight enters the budget",
			Body: "The city approved a rooftop solar program on Monday. " +
				"Rooftop panels will power three hundred homes by the end of the year.",
			GenerationMetadata: meta,
		}
		writeData, _ = json.Marshal(art)
		renderFiles = []runner.StageFile{
			{Path: run.ArtifactPathArticle, Kind: run.ArtifactKindRendered, ID: "article",
				Data: []byte("# City Approves Rooftop Solar Program\n\nThe city approved a rooftop solar program on Monday. Rooftop panels will power three hundred homes by the end of the year.\n")},
			{Path: run.ArtifactPathThread, Kind: run.ArtifactKindRendered, ID: "thread", Data: []byte("The city approved a rooftop solar program on Monday.")},
		}
	}
	return writeData, renderFiles
}

func finalCheckStatus(notReady bool) string {
	if notReady {
		return "fail"
	}
	return "pass"
}

func fixtureWriteFile(path string, kind run.ArtifactKind, id string, data string) runner.StageFunc {
	return fixtureWriteFiles(runner.StageFile{Path: path, Kind: kind, ID: id, Data: []byte(data)})
}

func fixtureWriteFiles(files ...runner.StageFile) runner.StageFunc {
	return func(_ context.Context, _ *runner.StageInput) (*runner.StageOutput, error) {
		return &runner.StageOutput{Files: files}, nil
	}
}

// ---------------------------------------------------------------------------
// Injection seams + assertion helpers
// ---------------------------------------------------------------------------

// injectFakePublishers swaps the publisher factory for per-destination fakes
// and restores the production default when the test ends.
func injectFakePublishers(t *testing.T, fakes map[string]*fakePublisher) {
	t.Helper()
	t.Cleanup(func() { publisherFactory = presets.DefaultFactory })
	publisherFactory = func(_ context.Context, d *presets.Destination, planID publication.PublicationID) (publish.Publisher, error) {
		f := fakes[d.Key]
		if f == nil {
			return nil, fmt.Errorf("no fake publisher registered for destination %q", d.Key)
		}
		f.planID = planID
		return f, nil
	}
}

// pinPublishInterface pins the approval interface deterministically:
// interactive = a prompt is offered and answered with `answer`; non-
// interactive = no prompt is ever offered (and stdin reads see EOF).
func pinPublishInterface(t *testing.T, interactive bool, answer string) {
	t.Helper()
	t.Cleanup(func() {
		publishTTY = defaultPublishTTY
		publishPrompt = os.Stdin
	})
	publishTTY = func() bool { return interactive }
	publishPrompt = strings.NewReader(answer)
}

// publishExitCode extracts the documented exit code of a command error.
func publishExitCode(err error) int {
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	if err == nil {
		return 0
	}
	return 1
}

func assertExit(t *testing.T, err error, want int) {
	t.Helper()
	if got := publishExitCode(err); got != want {
		t.Fatalf("exit code = %d, want %d (error: %v)", got, want, err)
	}
}

// args builds the canonical publish invocation for the environment.
func (e *publishEnv) args(extra ...string) []string {
	return append([]string{
		"publish", e.runID,
		"--workspace-root", e.wsRoot,
		"--bundle-root", e.bundleRoot,
		"--publication", "demo",
	}, extra...)
}

// planDigests returns the artifact digest of every plan of the run, in plan
// creation order, addressed by the plan's target ID.
func (e *publishEnv) planDigests(t *testing.T) map[string]string {
	t.Helper()
	store, err := workspace.NewStore(e.wsRoot)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	eng := publish.New(store)
	id := run.RunID(e.runID)
	ids, err := eng.ListPlanIDs(id)
	if err != nil {
		t.Fatalf("ListPlanIDs: %v", err)
	}
	out := map[string]string{}
	for _, pid := range ids {
		plan, err := eng.Plan(id, pid)
		if err != nil {
			t.Fatalf("Plan(%s): %v", pid, err)
		}
		if len(plan.Targets) > 0 {
			out[string(plan.Targets[0].ID)] = string(plan.Artifact.Digest)
		}
	}
	return out
}

// preview runs the plain preview invocation (which materializes the plans
// on disk for later --approve/--resume invocations) and returns the
// per-target plan digests — the same flow an operator follows: preview,
// read the digest, then approve.
func (e *publishEnv) preview(t *testing.T, extra ...string) map[string]string {
	t.Helper()
	captureStdout(t, func() {
		assertExit(t, runCommand(t, e.args(extra...)...), 0)
	})
	return e.planDigests(t)
}

// latestPlanStatus returns the on-disk status of the run's latest plan.
func (e *publishEnv) latestPlanStatus(t *testing.T) string {
	t.Helper()
	store, err := workspace.NewStore(e.wsRoot)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	plan, err := publish.New(store).LatestPlan(run.RunID(e.runID))
	if err != nil {
		t.Fatalf("LatestPlan: %v", err)
	}
	return string(plan.Status)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestPublishPreview_NoSideEffects: the default invocation previews the
// publication plan and stops — plans and content are persisted (for audit
// and resume), but no publisher action happens.
func TestPublishPreview_NoSideEffects(t *testing.T) {
	env := newPublishEnv(t, "article", false)

	out := captureStdout(t, func() {
		assertExit(t, runCommand(t, env.args()...), 0)
	})

	for _, want := range []string{
		"Run " + env.runID,
		"City approves rooftop solar program",
		"Ready:", "yes",
		"plan (preview)",
		"digest:",
		"No external side effects",
		"To publish:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("preview output missing %q:\n%s", want, out)
		}
	}
	if env.fakes["hugo"].Calls() != 0 || env.fakes["bluesky"].Calls() != 0 {
		t.Fatalf("preview must not publish: hugo=%d bluesky=%d", env.fakes["hugo"].Calls(), env.fakes["bluesky"].Calls())
	}

	// Both plans (site + thread) are persisted with the run.
	digests := env.planDigests(t)
	if len(digests) != 2 {
		t.Fatalf("expected 2 persisted plans (site, thread), got %v", digests)
	}
	if digests["site"] == "" || digests["thread"] == "" {
		t.Fatalf("plan digests: %v", digests)
	}

	// The canonical content files are persisted for the engine.
	store, _ := workspace.NewStore(env.wsRoot)
	for _, name := range []string{"hugo", "bluesky"} {
		_ = name
	}
	ids, _ := publish.New(store).ListPlanIDs(run.RunID(env.runID))
	for _, pid := range ids {
		plan, _ := publish.New(store).Plan(run.RunID(env.runID), pid)
		if plan.Artifact.Digest == "" {
			t.Fatalf("plan %s has no artifact digest", pid)
		}
	}
}

// TestPublishDryRun_Explicit: --dry-run is the same preview, made explicit.
func TestPublishDryRun_Explicit(t *testing.T) {
	env := newPublishEnv(t, "article", false)

	out := captureStdout(t, func() {
		assertExit(t, runCommand(t, env.args("--dry-run")...), 0)
	})
	if !strings.Contains(out, "dry run") {
		t.Errorf("dry run output missing the mode label:\n%s", out)
	}
	if env.fakes["hugo"].Calls() != 0 || env.fakes["bluesky"].Calls() != 0 {
		t.Fatal("dry-run must not publish")
	}
}

// TestPublishPreview_Repeated_IsIdempotent: previewing again does not churn
// plan IDs — the same plans are reused for the same content.
func TestPublishPreview_Repeated_IsIdempotent(t *testing.T) {
	env := newPublishEnv(t, "article", false)

	assertExit(t, runCommand(t, env.args()...), 0)
	first := env.planDigests(t)

	assertExit(t, runCommand(t, env.args()...), 0)
	second := env.planDigests(t)

	if len(second) != 2 {
		t.Fatalf("after re-preview: %d plans, want 2 (no ID churn): %v", len(second), second)
	}
	for k, v := range first {
		if second[k] != v {
			t.Fatalf("plan %s changed on re-preview: %s -> %s", k, v, second[k])
		}
	}
}

// TestPublishApproveNonInteractive_MissingDigest: --approve without a TTY
// and without --digest is refused (approval is never inferred).
func TestPublishApproveNonInteractive_MissingDigest(t *testing.T) {
	env := newPublishEnv(t, "article", false)

	err := runCommand(t, env.args("--approve")...)
	assertExit(t, err, 5)
	if err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("error should mention the digest requirement: %v", err)
	}
	if env.fakes["hugo"].Calls() != 0 || env.fakes["bluesky"].Calls() != 0 {
		t.Fatal("nothing may be published without approval")
	}
}

// TestPublishApproveNonInteractive_WrongDigest: a digest that does not
// match the plan's artifact is refused (stale approval protection).
func TestPublishApproveNonInteractive_WrongDigest(t *testing.T) {
	env := newPublishEnv(t, "article", false)

	err := runCommand(t, env.args("--approve", "--digest", "sha256:0000000000000000000000000000000000000000000000000000000000000000")...)
	assertExit(t, err, 5)
	if err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("error should report the digest mismatch: %v", err)
	}
	if env.fakes["hugo"].Calls() != 0 || env.fakes["bluesky"].Calls() != 0 {
		t.Fatal("nothing may be published on a digest mismatch")
	}
}

// TestPublishApproveNonInteractive_MatchingDigest: --approve with the exact
// artifact digest publishes the destination (here: only the site).
func TestPublishApproveNonInteractive_MatchingDigest(t *testing.T) {
	env := newPublishEnv(t, "article", false)
	digests := env.preview(t)

	out := captureStdout(t, func() {
		assertExit(t, runCommand(t, env.args(
			"--target", "hugo",
			"--approve",
			"--digest", digests["site"],
		)...), 0)
	})
	if env.fakes["hugo"].Calls() != 1 {
		t.Fatalf("hugo publisher called %d times, want 1", env.fakes["hugo"].Calls())
	}
	if env.fakes["bluesky"].Calls() != 0 {
		t.Fatalf("bluesky must not be published (not selected): %d calls", env.fakes["bluesky"].Calls())
	}
	if !strings.Contains(out, "https://example.com/solar") {
		t.Errorf("result should show the published URL:\n%s", out)
	}
	if env.latestPlanStatus(t) != "succeeded" {
		t.Fatalf("plan status = %s, want succeeded", env.latestPlanStatus(t))
	}
}

// TestPublishApproveInteractive_Yes: at a terminal the operator is
// prompted and a "yes" approves and publishes.
func TestPublishApproveInteractive_Yes(t *testing.T) {
	env := newPublishEnv(t, "article", false)
	pinPublishInterface(t, true, "yes\n")
	digests := env.planDigests(t)

	out := captureStdout(t, func() {
		assertExit(t, runCommand(t, env.args(
			"--target", "hugo",
			"--approve",
			"--digest", digests["site"],
		)...), 0)
	})
	if !strings.Contains(out, "Approve plan") {
		t.Errorf("interactive prompt expected:\n%s", out)
	}
	if env.fakes["hugo"].Calls() != 1 {
		t.Fatalf("hugo publisher called %d times, want 1", env.fakes["hugo"].Calls())
	}
}

// TestPublishApproveInteractive_No: a "no" at the prompt declines; nothing
// is published and the exit code is the documented 5.
func TestPublishApproveInteractive_No(t *testing.T) {
	env := newPublishEnv(t, "article", false)
	pinPublishInterface(t, true, "no\n")

	captureStdout(t, func() {
		err := runCommand(t, env.args(
			"--target", "hugo",
			"--approve",
			"--digest", env.planDigests(t)["site"],
		)...)
		assertExit(t, err, 5)
	})
	if env.fakes["hugo"].Calls() != 0 {
		t.Fatalf("declined approval must not publish: %d calls", env.fakes["hugo"].Calls())
	}
}

// TestPublishNotReady_Exit3: a run whose final factual check failed is not
// ready; no plan is prepared, nothing publishes.
func TestPublishNotReady_Exit3(t *testing.T) {
	env := newPublishEnv(t, "article", true)

	out := captureStdout(t, func() {
		assertExit(t, runCommand(t, env.args()...), 3)
	})
	if !strings.Contains(out, "not ready") {
		t.Errorf("not-ready output expected:\n%s", out)
	}
	if len(env.planDigests(t)) != 0 {
		t.Fatal("a not-ready run must not get plans")
	}
	if env.fakes["hugo"].Calls() != 0 || env.fakes["bluesky"].Calls() != 0 {
		t.Fatal("a not-ready run must not publish")
	}
}

// TestPublishNoPreset_Exit4: a ready run with no selected preset has
// nothing to publish.
func TestPublishNoPreset_Exit4(t *testing.T) {
	env := newPublishEnv(t, "article", false)
	// The same run, but with no preset selected (hermetic config: unset).
	err := runCommand(t, "publish", env.runID, "--workspace-root", env.wsRoot)
	assertExit(t, err, 4)
	if env.fakes["hugo"].Calls() != 0 || env.fakes["bluesky"].Calls() != 0 {
		t.Fatal("nothing may be published without a preset")
	}
}

// TestPublishUnknownTarget_Exit2: --target names a destination that the
// preset does not have: a hard usage error.
func TestPublishUnknownTarget_Exit2(t *testing.T) {
	env := newPublishEnv(t, "article", false)
	err := runCommand(t, env.args("--target", "mastodon")...)
	assertExit(t, err, 2)
	if err == nil || !strings.Contains(err.Error(), "mastodon") {
		t.Fatalf("error should name the unknown target: %v", err)
	}
}

// TestPublishTargetFilter: --target selects a subset of the destinations.
func TestPublishTargetFilter(t *testing.T) {
	env := newPublishEnv(t, "article", false)

	captureStdout(t, func() {
		assertExit(t, runCommand(t, env.args("--target", "bluesky")...), 0)
	})
	digests := env.planDigests(t)
	if len(digests) != 1 || digests["thread"] == "" {
		t.Fatalf("--target bluesky must prepare only the thread plan, got %v", digests)
	}
}

// TestPublishBadRunID_Exit2 and missing run -> exit 8.
func TestPublishBadOrMissingRun(t *testing.T) {
	env := newPublishEnv(t, "article", false)

	err := runCommand(t, "publish", "not a run id", "--workspace-root", env.wsRoot)
	assertExit(t, err, 2)

	err = runCommand(t, "publish", "run-1111111111111111", "--workspace-root", env.wsRoot)
	assertExit(t, err, 8)
}

// TestPublishPartialFailure_Resume: the site publishes, the thread fails
// (exit 6); --resume re-executes only the failed plan — the site is never
// re-published, the thread goes out on the retry.
func TestPublishPartialFailure_Resume(t *testing.T) {
	env := newPublishEnv(t, "article", false)
	env.fakes["bluesky"].failOnce = true
	digests := env.preview(t)

	captureStdout(t, func() {
		err := runCommand(t, env.args(
			"--approve",
			"--digest", digests["site"],
			"--digest", digests["thread"],
		)...)
		assertExit(t, err, 6)
	})
	if env.fakes["hugo"].Calls() != 1 {
		t.Fatalf("site should have published once: %d", env.fakes["hugo"].Calls())
	}
	if env.fakes["bluesky"].Calls() != 1 {
		t.Fatalf("thread should have failed once: %d", env.fakes["bluesky"].Calls())
	}

	// Resume: the latest plan is the failed thread plan. It still holds its
	// valid approval, so --resume publishes it; the fake now succeeds.
	out := captureStdout(t, func() {
		assertExit(t, runCommand(t, env.args("--resume")...), 0)
	})
	if env.fakes["hugo"].Calls() != 1 {
		t.Fatalf("resume must not re-publish the already-succeeded site: %d calls", env.fakes["hugo"].Calls())
	}
	if env.fakes["bluesky"].Calls() != 2 {
		t.Fatalf("resume must retry the failed thread exactly once: %d calls", env.fakes["bluesky"].Calls())
	}
	if !strings.Contains(out, "https://bsky.app/demo/thread") {
		t.Errorf("resume output should show the thread receipt:\n%s", out)
	}
	if env.latestPlanStatus(t) != "succeeded" {
		t.Fatalf("latest plan status = %s, want succeeded", env.latestPlanStatus(t))
	}
}

// TestPublishResume_CompletedPlanIsNoOp: resuming a fully succeeded plan
// does nothing (no re-publication) and exits 0.
func TestPublishResume_CompletedPlanIsNoOp(t *testing.T) {
	env := newPublishEnv(t, "article", false)
	digests := env.preview(t)

	captureStdout(t, func() {
		assertExit(t, runCommand(t, env.args(
			"--target", "hugo", "--approve", "--digest", digests["site"],
		)...), 0)
	})
	callsAfterPublish := env.fakes["hugo"].Calls()

	out := captureStdout(t, func() {
		assertExit(t, runCommand(t, env.args("--resume")...), 0)
	})
	if env.fakes["hugo"].Calls() != callsAfterPublish {
		t.Fatalf("resuming a completed plan must not publish: %d -> %d calls", callsAfterPublish, env.fakes["hugo"].Calls())
	}
	if !strings.Contains(out, "already") {
		t.Errorf("resume of a completed plan should say so:\n%s", out)
	}
}

// TestPublishResume_CancelledPlan_Exit5: a cancelled plan never resumes.
func TestPublishResume_CancelledPlan_Exit5(t *testing.T) {
	env := newPublishEnv(t, "article", false)

	captureStdout(t, func() {
		assertExit(t, runCommand(t, env.args("--target", "hugo")...), 0) // prepare + preview
	})

	// Cancel the plan through the engine (the durable state a crashed or
	// abandoned run leaves behind).
	store, err := workspace.NewStore(env.wsRoot)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	eng := publish.New(store)
	ids, err := eng.ListPlanIDs(run.RunID(env.runID))
	if err != nil || len(ids) != 1 {
		t.Fatalf("expected exactly 1 plan, got %d (%v)", len(ids), err)
	}
	if _, err := eng.Cancel(context.Background(), run.RunID(env.runID), ids[0], "abandoned by test"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	err = runCommand(t, env.args("--resume")...)
	assertExit(t, err, 5)
	if env.fakes["hugo"].Calls() != 0 {
		t.Fatalf("a cancelled plan must not publish: %d calls", env.fakes["hugo"].Calls())
	}
}

// TestPublishAlreadyPublished_NoOp: after a full publication, a plain
// `publish` of the same content reports the idempotent no-op and performs
// no external action.
func TestPublishAlreadyPublished_NoOp(t *testing.T) {
	env := newPublishEnv(t, "article", false)
	digests := env.preview(t)

	captureStdout(t, func() {
		assertExit(t, runCommand(t, env.args(
			"--approve", "--digest", digests["site"], "--digest", digests["thread"],
		)...), 0)
	})
	siteCalls := env.fakes["hugo"].Calls()
	threadCalls := env.fakes["bluesky"].Calls()

	out := captureStdout(t, func() {
		assertExit(t, runCommand(t, env.args()...), 0)
	})
	if env.fakes["hugo"].Calls() != siteCalls || env.fakes["bluesky"].Calls() != threadCalls {
		t.Fatalf("already-published content must not be re-published: hugo %d->%d, bluesky %d->%d",
			siteCalls, env.fakes["hugo"].Calls(), threadCalls, env.fakes["bluesky"].Calls())
	}
	if !strings.Contains(out, "already published") {
		t.Errorf("output should report the no-op:\n%s", out)
	}
}

// captureOutput captures os.Stdout and os.Stderr together for fn (warnings
// go to stderr; a test asserting on them needs both streams).
func captureOutput(t *testing.T, fn func()) (out, errbuf string) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	or, ow, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	er, ew, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout, os.Stderr = ow, ew
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()
	fn()
	_ = ow.Close()
	_ = ew.Close()
	var b1, b2 strings.Builder
	buf := make([]byte, 32*1024)
	for {
		n, err := or.Read(buf)
		b1.Write(buf[:n])
		if err != nil {
			break
		}
	}
	for {
		n, err := er.Read(buf)
		b2.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return b1.String(), b2.String()
}

// TestPublishThreadRun_HugoUnavailable: on a thread run the site
// destination cannot be prepared (no rendered article) and is reported as
// a warning; the thread publishes on its own.
func TestPublishThreadRun_HugoUnavailable(t *testing.T) {
	env := newPublishEnv(t, "thread", false)

	out, errOut := captureOutput(t, func() {
		assertExit(t, runCommand(t, env.args()...), 0)
	})
	if !strings.Contains(errOut, "not available") {
		t.Errorf("the unavailable hugo destination should be warned (stderr):\n%s\nstdout:\n%s", errOut, out)
	}
	digests := env.planDigests(t)
	if len(digests) != 1 || digests["thread"] == "" {
		t.Fatalf("a thread run prepares only the thread plan, got %v", digests)
	}

	captureStdout(t, func() {
		assertExit(t, runCommand(t, env.args("--approve", "--digest", digests["thread"])...), 0)
	})
	if env.fakes["bluesky"].Calls() != 1 {
		t.Fatalf("thread should publish once: %d", env.fakes["bluesky"].Calls())
	}
	if env.fakes["hugo"].Calls() != 0 {
		t.Fatalf("hugo must never publish on a thread run: %d", env.fakes["hugo"].Calls())
	}
}

// TestPublishRunRecordedPreset: the preset a run recorded at start is used
// when no flag or config selects one.
func TestPublishRunRecordedPreset(t *testing.T) {
	setHermeticEnv(t)
	wsRoot := t.TempDir()
	bundleRoot := t.TempDir()

	// Build the bundle (same as the env helper).
	demoDir := filepath.Join(bundleRoot, "publications", "demo")
	mustWriteFile(t, filepath.Join(demoDir, "publication.yaml"),
		"schema_version: 1\nrenderer: article\nhugo: hugo.yaml\nbluesky: bluesky.yaml\n")
	mustWriteFile(t, filepath.Join(demoDir, "hugo.yaml"), "destination: site\nbase_url: https://example.com\n")
	mustWriteFile(t, filepath.Join(demoDir, "bluesky.yaml"),
		"handle: \"@demo\"\napi_endpoint: https://public.api.bsky.app\napi_key:\n  env: FAKED_BSKY_KEY\n")
	if err := os.MkdirAll(filepath.Join(bundleRoot, "site", "content"), 0o755); err != nil {
		t.Fatal(err)
	}

	// ... and the run records the preset.
	m, err := run.NewManifest(run.NewManifestParams{
		Workspace:   run.Workspace{Name: "testws"},
		Topic:       "Run recorded the preset",
		SourceMode:  run.SourceModeExplicit,
		StartedAt:   publishFixtureTime,
		Publication: &run.Publication{ID: "demo", BundlePath: bundleRoot},
	})
	if err != nil {
		t.Fatalf("NewManifest: %v", err)
	}
	writeData, renderFiles := fixtureRender("article", false)
	stages := []runner.Stage{
		{Name: run.CanonicalStages()[0], Fn: fixtureWriteFile("sources/index.json", run.ArtifactKindSource, "sources", `{}`)},
		{Name: run.CanonicalStages()[1], Fn: fixtureWriteFile("fetch/index.json", run.ArtifactKindSource, "fetch", `{}`)},
		{Name: run.CanonicalStages()[2], Fn: fixtureWriteFile("documents/index.json", run.ArtifactKindDocument, "documents", `{}`)},
		{Name: run.CanonicalStages()[3], Fn: fixtureWriteFile("research/dossier.json", run.ArtifactKindDossier, "dossier", `{}`)},
		{Name: run.CanonicalStages()[4], Fn: fixtureWriteFile("verify/verification.json", run.ArtifactKindVerification, "verification", `{}`)},
		{Name: run.CanonicalStages()[5], SkipReason: "no unresolved claims"},
		{Name: run.CanonicalStages()[6], Fn: fixtureWriteFile(run.ArtifactPathArtifact, run.ArtifactKindEditorial, "editorial", string(writeData))},
		{Name: run.CanonicalStages()[7], Fn: fixtureWriteFile("final_check/result.json", run.ArtifactKindFactualCheck, "final-check", `{"overall_status":"pass"}`)},
		{Name: run.CanonicalStages()[8], Fn: fixtureWriteFiles(renderFiles...)},
	}
	store, err := workspace.NewStore(wsRoot)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := runner.Run(context.Background(), runner.Config{Manifest: m, Store: store, Stages: stages}); err != nil {
		t.Fatalf("runner.Run: %v", err)
	}

	fakes := map[string]*fakePublisher{
		"hugo":    {targetID: "site", uri: "https://example.com/x"},
		"bluesky": {targetID: "thread", uri: "https://bsky.app/demo/x"},
	}
	injectFakePublishers(t, fakes)
	pinPublishInterface(t, false, "")

	// No --publication, no --bundle-root: the run's recorded preset wins.
	captureStdout(t, func() {
		assertExit(t, runCommand(t, "publish", string(m.ID), "--workspace-root", wsRoot), 0)
	})
	if len(env0PlanDigests(t, wsRoot, string(m.ID))) != 2 {
		t.Fatal("the recorded preset should have produced both plans")
	}
}

// env0PlanDigests is planDigests without a publishEnv (for the standalone
// run-recorded test above).
func env0PlanDigests(t *testing.T, wsRoot, runID string) map[string]string {
	t.Helper()
	store, err := workspace.NewStore(wsRoot)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ids, err := publish.New(store).ListPlanIDs(run.RunID(runID))
	if err != nil {
		t.Fatalf("ListPlanIDs: %v", err)
	}
	out := map[string]string{}
	for _, pid := range ids {
		plan, err := publish.New(store).Plan(run.RunID(runID), pid)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if len(plan.Targets) > 0 {
			out[string(plan.Targets[0].ID)] = string(plan.Artifact.Digest)
		}
	}
	return out
}

// TestPublishJSONOutput: --format json emits the machine-readable report.
func TestPublishJSONOutput(t *testing.T) {
	env := newPublishEnv(t, "article", false)
	digests := env.preview(t)

	out := captureStdout(t, func() {
		assertExit(t, runCommand(t, env.args("--format", "json",
			"--approve", "--digest", digests["site"], "--digest", digests["thread"],
		)...), 0)
	})

	var rep struct {
		Run struct {
			ID     string `json:"id"`
			Topic  string `json:"topic"`
			Status string `json:"status"`
		} `json:"run"`
		Ready *struct {
			Ready  bool   `json:"ready"`
			Reason string `json:"reason"`
		} `json:"ready"`
		Preset *struct {
			Name string `json:"name"`
		} `json:"preset"`
		Mode      string `json:"mode"`
		Published bool   `json:"published"`
		ExitCode  int    `json:"exit_code"`
		Plans     []struct {
			ID          string `json:"id"`
			Destination string `json:"destination"`
			Status      string `json:"status"`
			Artifact    struct {
				Digest string `json:"digest"`
			} `json:"artifact"`
			Targets []struct {
				ID      string          `json:"id"`
				Status  string          `json:"status"`
				Receipt json.RawMessage `json:"receipt"`
			} `json:"targets"`
		} `json:"plans"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("json output did not parse: %v\n%s", err, out)
	}
	if rep.Run.ID != env.runID || rep.Run.Status != "succeeded" {
		t.Errorf("run block = %+v", rep.Run)
	}
	if rep.Ready == nil || !rep.Ready.Ready {
		t.Errorf("ready = %+v, want ready", rep.Ready)
	}
	if rep.Preset == nil || rep.Preset.Name != "demo" {
		t.Errorf("preset = %+v, want demo", rep.Preset)
	}
	if rep.Mode != "publish" || !rep.Published || rep.ExitCode != 0 {
		t.Errorf("mode/published/exit = %s/%v/%d, want publish/true/0", rep.Mode, rep.Published, rep.ExitCode)
	}
	if len(rep.Plans) != 2 {
		t.Fatalf("plans = %d, want 2", len(rep.Plans))
	}
	for _, p := range rep.Plans {
		if p.Status != "succeeded" {
			t.Errorf("plan %s status = %s, want succeeded", p.ID, p.Status)
		}
		if len(p.Targets) != 1 || p.Targets[0].Status != "succeeded" {
			t.Errorf("plan %s targets = %+v, want one succeeded", p.ID, p.Targets)
		}
		if len(p.Targets[0].Receipt) == 0 {
			t.Errorf("plan %s target has no receipt in the JSON report", p.ID)
		}
		if p.Destination == "" {
			t.Errorf("plan %s has no destination in the JSON report", p.ID)
		}
	}
}
