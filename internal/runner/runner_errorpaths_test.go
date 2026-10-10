package runner_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/runner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workspace"
)

// A manifest that is already terminal (a fully executed run) must be left
// completely untouched: the run cannot be (re)started, the failure cannot be
// recorded on a terminal run, and the manifest on disk must not change.
func TestRunTerminalManifestLeavesDiskUnchanged(t *testing.T) {
	store := newStore(t)

	m := newTestManifest(t)
	plan := run.CanonicalStages()
	if err := m.Start(baseTime); err != nil {
		t.Fatal(err)
	}
	for i, name := range plan {
		at := baseTime.Add(time.Duration(i+1) * time.Second)
		if err := m.StartStage(name, at); err != nil {
			t.Fatal(err)
		}
		if err := m.SucceedStage(name, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Succeed(baseTime.Add(10 * time.Second)); err != nil {
		t.Fatalf("Succeed: %v", err)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("terminal manifest is invalid: %v", err)
	}

	res, err := mustRun(t, context.Background(), m, store, demoStages(), fixedClock())
	if err == nil {
		t.Fatal("expected an error for a terminal manifest")
	}
	if res.Status != run.RunStatusSucceeded {
		t.Errorf("status = %s, want succeeded (the terminal run is left alone)", res.Status)
	}

	loaded, _, err := store.LoadManifest(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != run.RunStatusSucceeded {
		t.Errorf("on-disk status = %s, want succeeded", loaded.Status)
	}
}

// Stage errors that report a cancelled or deadline-exceeded context are
// treated as run cancellation even when the context itself is still live.
func TestRunStageReportsContextErrorsAsCancellation(t *testing.T) {
	reported := []struct {
		name string
		err  error
	}{
		{"context.Canceled", context.Canceled},
		{"wrapped DeadlineExceeded", fmt.Errorf("llm call: %w", context.DeadlineExceeded)},
	}
	for _, tc := range reported {
		t.Run(tc.name, func(t *testing.T) {
			store := newStore(t)
			m := newTestManifest(t)
			stages := demoStages()
			stages[0].Fn = func(ctx context.Context, in *runner.StageInput) (*runner.StageOutput, error) {
				return nil, tc.err
			}

			res, err := mustRun(t, context.Background(), m, store, stages, fixedClock())
			if err == nil {
				t.Fatal("expected the run to stop")
			}
			if res.Status != run.RunStatusCancelled {
				t.Fatalf("status = %s, want cancelled", res.Status)
			}
			loaded, _, err := store.LoadManifest(m.ID)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Status != run.RunStatusCancelled {
				t.Errorf("on-disk status = %s, want cancelled", loaded.Status)
			}
			if loaded.Stages[0].Status != run.StageStatusCancelled {
				t.Errorf("stage 0 = %s, want cancelled", loaded.Stages[0].Status)
			}
			if loaded.Stages[1].Status != run.StageStatusPending {
				t.Errorf("stage 1 = %s, want pending", loaded.Stages[1].Status)
			}
		})
	}
}

// A stage that cancels the context mid-flight must be treated as cancelled:
// the runner does not persist a stage output produced while the run is being
// cancelled, cancels the stage in the final snapshot, and never starts the
// next stage.
func TestRunStageCancelsContextBeforeNextStage(t *testing.T) {
	store := newStore(t)
	m := newTestManifest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stages := demoStages()
	stages[0].Fn = func(ctx context.Context, in *runner.StageInput) (*runner.StageOutput, error) {
		cancel()
		return &runner.StageOutput{
			Files: []runner.StageFile{{Path: "discovery.json", Kind: run.ArtifactKindCustom, ID: "discovery", Data: []byte(`{}`)}},
		}, nil
	}

	res, err := mustRun(t, ctx, m, store, stages, fixedClock())
	if err == nil {
		t.Fatal("expected the run to be cancelled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if res.Status != run.RunStatusCancelled {
		t.Fatalf("status = %s, want cancelled", res.Status)
	}

	loaded, _, err := store.LoadManifest(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != run.RunStatusCancelled {
		t.Errorf("on-disk status = %s, want cancelled", loaded.Status)
	}
	if loaded.Stages[0].Status != run.StageStatusCancelled {
		t.Errorf("stage 0 = %s, want cancelled (output not persisted on cancellation)", loaded.Stages[0].Status)
	}
	if loaded.Stages[1].Status != run.StageStatusPending {
		t.Errorf("stage 1 = %s, want pending (never started)", loaded.Stages[1].Status)
	}
	// The stage ran (and its context died) before persistStageOutput, so its
	// output was never written.
	if _, err := store.LoadArtifact(m.ID, "discovery.json"); err == nil {
		t.Error("stage 0 artifact was persisted despite cancellation")
	}
}

// The run directory vanishing mid-run (moved by the stage) makes persistence
// fail; the run must fail cleanly and the last successfully persisted
// snapshot (the stage start) must remain readable where the directory went.
func TestRunLostRunDirFailsRunWithReadableLastState(t *testing.T) {
	store := newStore(t)
	m := newTestManifest(t)

	runDir := filepath.Join(store.RunsDir(), m.ID.String())
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	moved := runDir + ".moved"

	stages := demoStages()
	stages[0].Fn = func(ctx context.Context, in *runner.StageInput) (*runner.StageOutput, error) {
		if err := os.Rename(in.RunDir, moved); err != nil {
			t.Errorf("moving the run dir: %v", err)
		}
		return &runner.StageOutput{
			Files: []runner.StageFile{{Path: "discovery.json", Kind: run.ArtifactKindCustom, ID: "discovery", Data: []byte(`{}`)}},
		}, nil
	}

	res, err := mustRun(t, context.Background(), m, store, stages, fixedClock())
	if err == nil {
		t.Fatal("expected the run to fail")
	}
	if res.Status != run.RunStatusFailed {
		t.Fatalf("status = %s, want failed", res.Status)
	}

	data, err := os.ReadFile(filepath.Join(moved, workspace.ManifestFileName))
	if err != nil {
		t.Fatalf("last snapshot unreadable: %v", err)
	}
	snap, err := run.UnmarshalManifest(data)
	if err != nil {
		t.Fatalf("unmarshaling last snapshot: %v", err)
	}
	if snap.Status != run.RunStatusRunning {
		t.Errorf("snapshot status = %s, want running (crashed mid-run)", snap.Status)
	}
	if snap.Stages[0].Status != run.StageStatusRunning {
		t.Errorf("snapshot stage 0 = %s, want running (crashed mid-stage)", snap.Stages[0].Status)
	}
}

// A context that is already cancelled when Run is called must cancel the run
// before the first stage starts: the run is persisted as cancelled, every
// stage remains pending, and nothing executes.
func TestRunPreCancelledContextCancelsBeforeFirstStage(t *testing.T) {
	store := newStore(t)
	m := newTestManifest(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before the run starts

	res, err := mustRun(t, ctx, m, store, demoStages(), fixedClock())
	if err == nil {
		t.Fatal("expected the run to be cancelled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if res.Status != run.RunStatusCancelled {
		t.Fatalf("status = %s, want cancelled", res.Status)
	}

	loaded, _, err := store.LoadManifest(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != run.RunStatusCancelled {
		t.Errorf("on-disk status = %s, want cancelled", loaded.Status)
	}
	for i, s := range loaded.Stages {
		if s.Status != run.StageStatusPending {
			t.Errorf("stage %d = %s, want pending (never started)", i, s.Status)
		}
	}
}

// A nil clock falls back to the wall clock; the run must complete normally.
func TestRunNilClockUsesWallClock(t *testing.T) {
	store := newStore(t)
	m := newTestManifest(t)

	res, err := runner.Run(context.Background(), runner.Config{
		Manifest: m, Store: store, Stages: demoStages(), // Clock: nil
	})
	if res == nil {
		t.Fatalf("Run returned nil result (err=%v)", err)
	}
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != run.RunStatusSucceeded {
		t.Errorf("status = %s, want succeeded", res.Status)
	}
	if res.Manifest.UpdatedAt.IsZero() {
		t.Error("updated_at is zero with the wall clock")
	}
}

// An empty stage plan (which the manifest contract allows even though
// NewManifest falls back to the canonical plan) completes the run as soon as
// it starts.
func TestRunEmptyPlanSucceedsImmediately(t *testing.T) {
	store := newStore(t)
	m := newTestManifest(t)
	m.Stages = nil // bypass the NewManifest canonical fallback

	res, err := mustRun(t, context.Background(), m, store, nil, fixedClock())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != run.RunStatusSucceeded {
		t.Fatalf("status = %s, want succeeded", res.Status)
	}
	loaded, _, err := store.LoadManifest(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != run.RunStatusSucceeded || loaded.FinishedAt == nil {
		t.Errorf("on-disk terminal state = %s/%v, want succeeded with finished_at", loaded.Status, loaded.FinishedAt)
	}
}

// A valid stage name in the wrong plan position must be rejected.
func TestRunStageNameMismatch(t *testing.T) {
	store := newStore(t)
	m := newTestManifest(t)
	bad := demoStages()
	plan := run.CanonicalStages()
	bad[0].Name = plan[1] // valid name, wrong position
	if _, err := runner.Run(context.Background(), runner.Config{Manifest: m, Store: store, Stages: bad}); err == nil {
		t.Error("valid name in the wrong position accepted")
	}
}

// Warnings that fail the manifest's note validation (an invalid code) must
// fail the stage rather than corrupt the manifest; an empty message is
// ignored.
func TestRunInvalidWarningFailsStage(t *testing.T) {
	store := newStore(t)
	m := newTestManifest(t)

	stages := demoStages()
	stages[0].Fn = func(ctx context.Context, in *runner.StageInput) (*runner.StageOutput, error) {
		return &runner.StageOutput{
			Files: []runner.StageFile{{Path: "discovery.json", Kind: run.ArtifactKindCustom, ID: "discovery", Data: []byte(`{}`)}},
			Warnings: []runner.Warning{
				{Code: "", Message: ""},           // empty: ignored
				{Code: "BAD CODE!", Message: "x"}, // invalid code: rejected
			},
		}, nil
	}

	res, err := mustRun(t, context.Background(), m, store, stages, fixedClock())
	if err == nil {
		t.Fatal("expected the run to fail on an invalid warning")
	}
	if res.Status != run.RunStatusFailed {
		t.Fatalf("status = %s, want failed", res.Status)
	}
	loaded, _, err := store.LoadManifest(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Stages[0].Status != run.StageStatusFailed {
		t.Errorf("stage 0 = %s, want failed", loaded.Stages[0].Status)
	}
}
