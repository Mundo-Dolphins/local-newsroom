package runner_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/runner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workspace"
)

// baseTime is the fixed timestamp for deterministic run IDs.
var baseTime = time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

// secretValue is a string that the redaction detectors must catch.
const secretValue = "token = deadbeefdeadbeefdeadbeef1234"

// fixedClock returns an advancing clock: every call is 1s after the last, so
// no persisted manifest can be "older" than the one before it.
func fixedClock() runner.Clock {
	t := baseTime
	return func() time.Time {
		t = t.Add(time.Second)
		return t
	}
}

// newTestManifest builds a valid pending manifest with a deterministic ID.
func newTestManifest(t *testing.T) *run.Manifest {
	t.Helper()
	m, err := run.NewManifest(run.NewManifestParams{
		Workspace: run.Workspace{Name: "testws"},
		Topic:     "EU AI Act enters into force",
		StartedAt: baseTime,
	})
	if err != nil {
		t.Fatalf("NewManifest: %v", err)
	}
	return m
}

// newStore builds a workspace store in a temp directory.
func newStore(t *testing.T) *workspace.Store {
	t.Helper()
	store, err := workspace.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

// demoStages returns the canonical nine stages with offline executors that
// produce the standard artifacts.
func demoStages() []runner.Stage {
	write := func(files ...runner.StageFile) runner.StageFunc {
		return func(ctx context.Context, in *runner.StageInput) (*runner.StageOutput, error) {
			return &runner.StageOutput{Files: files}, nil
		}
	}
	plan := run.CanonicalStages()
	return []runner.Stage{
		{Name: plan[0], Fn: write(
			runner.StageFile{Path: "search-plan.json", Kind: run.ArtifactKindCustom, ID: "search-plan", Data: []byte(`{"queries":["EU AI Act"]}`)},
			runner.StageFile{Path: "discovery.json", Kind: run.ArtifactKindCustom, ID: "discovery", Data: []byte(`{"candidates":["https://example.com/a"]}`)},
		)},
		{Name: plan[1], Fn: write(
			runner.StageFile{Path: "sources/source-1.json", Kind: run.ArtifactKindSource, ID: "source-1", Data: []byte(`{"url":"https://example.com/a"}`)},
		)},
		{Name: plan[2], Fn: write(
			runner.StageFile{Path: "documents/doc-1.md", Kind: run.ArtifactKindDocument, ID: "doc-1", Data: []byte("# Article text\n")},
		)},
		{Name: plan[3], Fn: write(
			runner.StageFile{Path: "dossier.json", Kind: run.ArtifactKindDossier, ID: "dossier", Data: []byte(`{"claims":[{"text":"` + secretValue + `"}]}`)},
		)},
		{Name: plan[4], Fn: write(
			runner.StageFile{Path: "verification.json", Kind: run.ArtifactKindVerification, ID: "verification", Data: []byte(`{"verdict":"verified"}`)},
		)},
		{Name: plan[5], SkipReason: "no unresolved claims"},
		{Name: plan[6], Fn: write(
			runner.StageFile{Path: "artifact.json", Kind: run.ArtifactKindEditorial, ID: "editorial", Data: []byte(`{"headline":"EU AI Act takes effect"}`)},
		)},
		{Name: plan[7], Fn: write(
			runner.StageFile{Path: "final-check.json", Kind: run.ArtifactKindFactualCheck, ID: "final-check", Data: []byte(`{"ok":true}`)},
		)},
		{Name: plan[8], Fn: write(
			runner.StageFile{Path: "output/article.md", Kind: run.ArtifactKindRendered, ID: "article", Data: []byte("# EU AI Act takes effect\n")},
			runner.StageFile{Path: "output/bluesky-thread.json", Kind: run.ArtifactKindRendered, ID: "bluesky-thread", Data: []byte(`[{"text":"thread"}]`)},
		)},
	}
}

// mustRun executes the run and fails the test on a setup error.
func mustRun(t *testing.T, ctx context.Context, m *run.Manifest, store *workspace.Store, stages []runner.Stage, clock runner.Clock) (*runner.Result, error) {
	t.Helper()
	res, err := runner.Run(ctx, runner.Config{Manifest: m, Store: store, Stages: stages, Clock: clock})
	if res == nil {
		t.Fatalf("Run returned nil result (err=%v)", err)
	}
	return res, err
}

// walkRunDir visits every file under the run directory.
func walkRunDir(t *testing.T, runDir string, fn func(path string, data []byte)) {
	t.Helper()
	err := filepath.WalkDir(runDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		fn(p, data)
		return nil
	})
	if err != nil {
		t.Fatalf("walking run dir: %v", err)
	}
}

func TestRunFullRunPersistsInspectableArtifacts(t *testing.T) {
	store := newStore(t)
	m := newTestManifest(t)

	res, err := mustRun(t, context.Background(), m, store, demoStages(), fixedClock())
	if err != nil {
		t.Fatalf("full run: %v", err)
	}
	if res.Status != run.RunStatusSucceeded {
		t.Fatalf("status = %s, want succeeded", res.Status)
	}

	// The run directory matches the v0.5 layout.
	wantDir := filepath.Join(store.Root(), workspace.RunsDirName, m.ID.String())
	if res.RunDir != wantDir {
		t.Errorf("RunDir = %q, want %q", res.RunDir, wantDir)
	}

	// Every expected artifact file exists on disk.
	for _, p := range []string{
		workspace.ManifestFileName,
		"search-plan.json", "discovery.json",
		"sources/source-1.json", "documents/doc-1.md",
		"dossier.json", "verification.json",
		"artifact.json", "final-check.json",
		"output/article.md", "output/bluesky-thread.json",
	} {
		if _, err := os.Stat(filepath.Join(res.RunDir, filepath.FromSlash(p))); err != nil {
			t.Errorf("missing file %s: %v", p, err)
		}
	}

	// The on-disk manifest (reopened, as after a process restart) shows the
	// full terminal state.
	reopened := newStoreIn(t, store)
	loaded, _, err := reopened.LoadManifest(m.ID)
	if err != nil {
		t.Fatalf("LoadManifest after run: %v", err)
	}
	if loaded.Status != run.RunStatusSucceeded {
		t.Errorf("on-disk status = %s, want succeeded", loaded.Status)
	}
	if loaded.Result.Outcome != run.OutcomeSucceeded {
		t.Errorf("on-disk outcome = %s", loaded.Result.Outcome)
	}
	if loaded.Result.FailedStage != nil {
		t.Errorf("failed stage = %v, want none", *loaded.Result.FailedStage)
	}
	if loaded.FinishedAt == nil {
		t.Error("finished_at is nil on a succeeded run")
	}
	got := map[run.StageName]run.StageStatus{}
	for _, s := range loaded.Stages {
		got[s.Name] = s.Status
	}
	plan := run.CanonicalStages()
	for i, name := range plan {
		want := run.StageStatusSucceeded
		if name == plan[5] { // follow_up
			want = run.StageStatusSkipped
		}
		if got[name] != want {
			t.Errorf("stage %s = %s, want %s (index %d)", name, got[name], want, i)
		}
	}
	if len(loaded.Artifacts) != 10 {
		t.Errorf("artifacts = %d, want 10", len(loaded.Artifacts))
	}
	for _, a := range loaded.Artifacts {
		if _, err := reopened.LoadArtifact(m.ID, a.Path); err != nil {
			t.Errorf("artifact %s (%s) unreadable: %v", a.ID, a.Path, err)
		}
	}

	// No secret may appear anywhere in the run tree.
	walkRunDir(t, res.RunDir, func(path string, data []byte) {
		if strings.Contains(string(data), "deadbeefdeadbeefdeadbeef1234") {
			t.Errorf("secret found in %s", path)
		}
	})

	// Safe permissions across the tree.
	err = filepath.WalkDir(res.RunDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Stat(p)
		if err != nil {
			return err
		}
		wantPerm := os.FileMode(0o600)
		if d.IsDir() {
			wantPerm = 0o700
		}
		if info.Mode().Perm() != wantPerm {
			t.Errorf("permissions %s = %v, want %v", p, info.Mode().Perm(), wantPerm)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// newStoreIn reopens a store on the same root (simulating a process restart).
func newStoreIn(t *testing.T, existing *workspace.Store) *workspace.Store {
	t.Helper()
	store, err := workspace.NewStore(existing.Root())
	if err != nil {
		t.Fatalf("NewStore(reopen): %v", err)
	}
	return store
}

func TestRunFailedStageKeepsDiagnosticsAndEarlierArtifacts(t *testing.T) {
	store := newStore(t)
	m := newTestManifest(t)

	stages := demoStages()
	plan := run.CanonicalStages()
	stages[4].Fn = func(ctx context.Context, in *runner.StageInput) (*runner.StageOutput, error) {
		return nil, errors.New("verify call failed: " + secretValue)
	}

	res, err := mustRun(t, context.Background(), m, store, stages, fixedClock())
	if err == nil {
		t.Fatal("expected the run to fail")
	}
	if res.Status != run.RunStatusFailed {
		t.Fatalf("status = %s, want failed", res.Status)
	}

	reopened := newStoreIn(t, store)
	loaded, _, err := reopened.LoadManifest(m.ID)
	if err != nil {
		t.Fatalf("LoadManifest after failed run: %v", err)
	}
	if loaded.Status != run.RunStatusFailed {
		t.Errorf("on-disk status = %s, want failed", loaded.Status)
	}
	if loaded.Result.Outcome != run.OutcomeFailed {
		t.Errorf("on-disk outcome = %s", loaded.Result.Outcome)
	}
	if loaded.Result.FailedStage == nil || *loaded.Result.FailedStage != plan[4] {
		t.Errorf("failed stage = %v, want %s", loaded.Result.FailedStage, plan[4])
	}
	if len(loaded.Errors) == 0 {
		t.Fatal("failed run lost its diagnostics")
	}
	if strings.Contains(loaded.Errors[0].Message, "deadbeefdeadbeefdeadbeef1234") {
		t.Errorf("diagnostics contain the raw secret: %s", loaded.Errors[0].Message)
	}
	if !strings.Contains(loaded.Errors[0].Message, run.RedactedPlaceholder) {
		t.Errorf("diagnostics were not redacted: %s", loaded.Errors[0].Message)
	}

	// Stage states: earlier succeeded, verify failed, later untouched.
	got := map[run.StageName]run.StageStatus{}
	for _, s := range loaded.Stages {
		got[s.Name] = s.Status
	}
	for i, name := range plan {
		var want run.StageStatus
		switch {
		case i < 4:
			want = run.StageStatusSucceeded
		case i == 4:
			want = run.StageStatusFailed
		default:
			want = run.StageStatusPending
		}
		if got[name] != want {
			t.Errorf("stage %s = %s, want %s", name, got[name], want)
		}
	}

	// Earlier artifacts survive; later ones do not exist.
	if _, err := reopened.LoadArtifact(m.ID, "dossier.json"); err != nil {
		t.Errorf("pre-failure dossier lost: %v", err)
	}
	if _, err := os.Stat(filepath.Join(res.RunDir, "output", "article.md")); !errors.Is(err, os.ErrNotExist) {
		t.Error("post-failure stage produced a file it should not have")
	}
	if _, err := os.Stat(filepath.Join(res.RunDir, "artifact.json")); !errors.Is(err, os.ErrNotExist) {
		t.Error("post-failure artifact.json exists")
	}
}

func TestRunDuplicateRunIDIsRefused(t *testing.T) {
	store := newStore(t)

	// Two manifests with identical derivation inputs get the same run ID.
	m1 := newTestManifest(t)
	m2 := newTestManifest(t)
	if m1.ID != m2.ID {
		t.Fatalf("expected identical IDs, got %s and %s", m1.ID, m2.ID)
	}

	if _, err := mustRun(t, context.Background(), m1, store, demoStages(), fixedClock()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	manifestFile := filepath.Join(store.RunsDir(), m1.ID.String(), workspace.ManifestFileName)
	before, err := os.ReadFile(manifestFile)
	if err != nil {
		t.Fatal(err)
	}

	res, err := mustRun(t, context.Background(), m2, store, demoStages(), fixedClock())
	if !errors.Is(err, workspace.ErrRunExists) {
		t.Fatalf("second run: err = %v, want ErrRunExists", err)
	}
	if res.RunDir != "" {
		t.Errorf("duplicate run claimed a directory: %q", res.RunDir)
	}
	after, err := os.ReadFile(manifestFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("duplicate run attempt modified the existing manifest")
	}
}

func TestRunCancelledContextLeavesCancelledRun(t *testing.T) {
	store := newStore(t)
	m := newTestManifest(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stages := demoStages()
	plan := run.CanonicalStages()
	// The research stage blocks until the context is cancelled. A channel
	// lets the test wait until the run has entered this stage (so every
	// earlier stage is already persisted) before cancelling: deterministic.
	reached := make(chan struct{})
	stages[3].Fn = func(ctx context.Context, in *runner.StageInput) (*runner.StageOutput, error) {
		close(reached)
		<-ctx.Done()
		return nil, ctx.Err()
	}

	resCh := make(chan *runner.Result, 1)
	go func() {
		res, _ := runner.Run(ctx, runner.Config{Manifest: m, Store: store, Stages: stages, Clock: fixedClock()})
		resCh <- res
	}()

	<-reached // the run is inside the blocking stage
	cancel()

	res := <-resCh // the in-flight run finished persisting its final state
	if res.Err == nil {
		t.Fatal("expected the run to be cancelled")
	}
	if res.Status != run.RunStatusCancelled {
		t.Fatalf("status = %s, want cancelled", res.Status)
	}

	reopened := newStoreIn(t, store)
	loaded, _, err := reopened.LoadManifest(m.ID)
	if err != nil {
		t.Fatalf("LoadManifest after cancel: %v", err)
	}
	if loaded.Status != run.RunStatusCancelled {
		t.Errorf("on-disk status = %s, want cancelled", loaded.Status)
	}
	got := map[run.StageName]run.StageStatus{}
	for _, s := range loaded.Stages {
		got[s.Name] = s.Status
	}
	if got[plan[3]] != run.StageStatusCancelled {
		t.Errorf("research stage = %s, want cancelled", got[plan[3]])
	}
	// Every stage before the blocking one completed and persisted.
	for _, name := range plan[:3] {
		if got[name] != run.StageStatusSucceeded {
			t.Errorf("stage %s = %s, want succeeded", name, got[name])
		}
	}
	// Every stage after the cancelled one is untouched.
	for _, name := range plan[4:] {
		if got[name] != run.StageStatusPending {
			t.Errorf("stage %s = %s, want pending", name, got[name])
		}
	}
	// The extract stage's artifact persisted before the cancellation.
	if _, err := reopened.LoadArtifact(m.ID, "documents/doc-1.md"); err != nil {
		t.Errorf("pre-cancel artifact lost: %v", err)
	}
	// The cancelled stage produced no artifact.
	if _, err := os.Stat(filepath.Join(res.RunDir, "dossier.json")); !errors.Is(err, os.ErrNotExist) {
		t.Error("cancelled stage left an artifact behind")
	}
}

func TestRunPanickingStageFailsRunWithoutCrash(t *testing.T) {
	store := newStore(t)
	m := newTestManifest(t)

	stages := demoStages()
	stages[3].Fn = func(ctx context.Context, in *runner.StageInput) (*runner.StageOutput, error) {
		panic("model exploded")
	}

	res, err := mustRun(t, context.Background(), m, store, stages, fixedClock())
	if err == nil {
		t.Fatal("expected the run to fail")
	}
	if res.Status != run.RunStatusFailed {
		t.Fatalf("status = %s, want failed", res.Status)
	}
	reopened := newStoreIn(t, store)
	loaded, _, err := reopened.LoadManifest(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != run.RunStatusFailed {
		t.Errorf("on-disk status = %s, want failed", loaded.Status)
	}
}

func TestRunConfigErrors(t *testing.T) {
	store := newStore(t)
	m := newTestManifest(t)
	ctx := context.Background()

	if _, err := runner.Run(ctx, runner.Config{Store: store, Stages: demoStages()}); err == nil {
		t.Error("nil manifest accepted")
	}
	if _, err := runner.Run(ctx, runner.Config{Manifest: m, Stages: demoStages()}); err == nil {
		t.Error("nil store accepted")
	}
	if _, err := runner.Run(ctx, runner.Config{Manifest: m, Store: store, Stages: demoStages()[:5]}); err == nil {
		t.Error("short stage list accepted")
	}
	bad := demoStages()
	bad[0].Name = run.StageName("bogus")
	if _, err := runner.Run(ctx, runner.Config{Manifest: m, Store: store, Stages: bad}); err == nil {
		t.Error("mismatched stage name accepted")
	}
	noReason := demoStages()
	noReason[5].SkipReason = ""
	if _, err := runner.Run(ctx, runner.Config{Manifest: m, Store: store, Stages: noReason}); err == nil {
		t.Error("skip without reason accepted")
	}
}

func TestRunWarningRecordedOnManifest(t *testing.T) {
	store := newStore(t)
	m := newTestManifest(t)

	stages := demoStages()
	stages[1].Fn = func(ctx context.Context, in *runner.StageInput) (*runner.StageOutput, error) {
		return &runner.StageOutput{
			Files:    []runner.StageFile{{Path: "sources/source-1.json", Kind: run.ArtifactKindSource, ID: "source-1", Data: []byte(`{}`)}},
			Warnings: []runner.Warning{{Code: "partial_fetch", Message: "one source timed out"}},
		}, nil
	}

	if _, err := mustRun(t, context.Background(), m, store, stages, fixedClock()); err != nil {
		t.Fatalf("run: %v", err)
	}
	reopened := newStoreIn(t, store)
	loaded, _, err := reopened.LoadManifest(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Warnings) != 1 || loaded.Warnings[0].Code != "partial_fetch" {
		t.Errorf("warnings = %+v, want one partial_fetch", loaded.Warnings)
	}
}
