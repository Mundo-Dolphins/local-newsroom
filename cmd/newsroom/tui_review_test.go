package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/config"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

var tuiReviewTime = time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)

func tuiAt(i int) time.Time { return tuiReviewTime.Add(time.Duration(i) * time.Second) }

// TestTuiAppAttachReviewBundle covers the TUI-side attachment: a run that a
// TUI cycle finishes in a terminal state gets the same review.md as a CLI
// run; a run still in flight does not.
func TestTuiAppAttachReviewBundle(t *testing.T) {
	setHermeticEnv(t)
	swapAppConfig(t, config.New(nil, ""))
	isolateTuiWorkspace(t)
	app, err := newTuiApp()
	if err != nil {
		t.Fatalf("newTuiApp: %v", err)
	}
	ws, err := app.workspace()
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}

	// A failed run (first stage failed, run failed) must get a bundle.
	m, err := run.NewManifest(run.NewManifestParams{
		Workspace:  ws,
		Topic:      "TUI failed run",
		SourceMode: run.SourceModeExplicit,
		Model:      run.NewModelMetadata("fake", "test-model", "http://127.0.0.1:1/v1"),
		StartedAt:  tuiAt(0),
	})
	if err != nil {
		t.Fatalf("NewManifest: %v", err)
	}
	if _, err := app.store.CreateRun(m); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := m.Start(tuiAt(1)); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := m.StartStage(run.StageDiscovery, tuiAt(2)); err != nil {
		t.Fatalf("start stage: %v", err)
	}
	if err := m.FailStage(run.StageDiscovery, tuiAt(3), "search.error", "search exploded"); err != nil {
		t.Fatalf("fail stage: %v", err)
	}
	if err := m.Fail(tuiAt(3), "search.error", "search exploded"); err != nil {
		t.Fatalf("fail: %v", err)
	}
	if _, err := app.store.SaveManifest(m); err != nil {
		t.Fatalf("save: %v", err)
	}

	app.attachReviewBundle(m.ID)
	data, err := os.ReadFile(filepath.Join(m.Workspace.Path, "runs", string(m.ID), "review.md"))
	if err != nil {
		t.Fatalf("a terminal TUI run must get review.md: %v", err)
	}
	if !strings.Contains(string(data), "**NOT READY**") {
		t.Fatal("a failed TUI run must render NOT READY")
	}
	if strings.Contains(string(data), "**READY**") {
		t.Fatal("a failed TUI run must not render READY")
	}
	if !strings.Contains(string(data), "search exploded") {
		t.Fatal("the bundle should surface the stage error")
	}

	// A run still in flight must not get a bundle.
	running, err := run.NewManifest(run.NewManifestParams{
		Workspace:  ws,
		Topic:      "TUI running run",
		SourceMode: run.SourceModeExplicit,
		Model:      run.NewModelMetadata("fake", "test-model", "http://127.0.0.1:1/v1"),
		StartedAt:  tuiAt(0),
	})
	if err != nil {
		t.Fatalf("NewManifest: %v", err)
	}
	if _, err := app.store.CreateRun(running); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := running.Start(tuiAt(1)); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := running.StartStage(run.StageDiscovery, tuiAt(2)); err != nil {
		t.Fatalf("start stage: %v", err)
	}
	if _, err := app.store.SaveManifest(running); err != nil {
		t.Fatalf("save: %v", err)
	}
	app.attachReviewBundle(running.ID)
	if _, err := os.Stat(filepath.Join(m.Workspace.Path, "runs", string(running.ID), "review.md")); !os.IsNotExist(err) {
		t.Fatalf("a running run must not get review.md (err=%v)", err)
	}

	// An unknown run ID is a no-op, never a crash.
	app.attachReviewBundle(run.NewRunID(ws.Name, "ghost", tuiAt(0)))
}
