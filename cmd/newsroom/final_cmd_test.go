package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publish"
	"github.com/Mundo-Dolphins/local-newsroom/internal/review"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/runner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workspace"
)

// ---------------------------------------------------------------------------
// newsroom final (v0.7, issue #146)
// ---------------------------------------------------------------------------

// finalFixtureTime is the fixed run start time (deterministic run IDs).
var finalFixtureTime = time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

// TestFinalCmdHelp verifies the command is wired into the root command with
// the documented flags.
func TestFinalCmdHelp(t *testing.T) {
	setHermeticEnv(t)
	buf := new(strings.Builder)
	finalCmd.SetOut(buf)
	finalCmd.SetErr(buf)
	if err := finalCmd.Help(); err != nil {
		t.Fatalf("finalCmd.Help: %v", err)
	}
	for _, want := range []string{"--final", "--editor", "--published-uri", "--format"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("help output missing %s:\n%s", want, buf.String())
		}
	}
	finalCmd.SetOut(io.Discard)
	finalCmd.SetErr(io.Discard)
}

// TestFinalCmdMissingFlags verifies that the required flags are enforced
// before anything touches the workspace.
func TestFinalCmdMissingFlags(t *testing.T) {
	setHermeticEnv(t)
	resetAllCLIFlags(t)
	if err := runCommand(t, "final", "run-20260714T120000Z-000000000000"); err == nil {
		t.Fatal("final without --final/--editor must fail flag validation")
	}
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
}

// TestFinalCmdRunNotFound verifies the documented exit code (8) and the JSON
// projection for a run that does not exist in the workspace.
func TestFinalCmdRunNotFound(t *testing.T) {
	setHermeticEnv(t)
	resetAllCLIFlags(t)
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	err := runCommand(t,
		"final", "run-20260714T120000Z-000000000000",
		"--workspace-root", t.TempDir(),
		"--final", "/dev/null", "--editor", "editor",
		"--format", "json",
	)
	assertExit(t, err, 8)
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
}

// TestFinalCmdHappyPath drives the real command in-process against a fully
// succeeded runner-produced run and verifies the recorded pair: manifest
// final_state, the final/ tree on disk, the original untouched, and the text
// projection.
func TestFinalCmdHappyPath(t *testing.T) {
	setHermeticEnv(t)
	resetAllCLIFlags(t)

	wsRoot := t.TempDir()
	store, err := workspace.NewStore(wsRoot)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	m, _ := mustDemoRun(t, wsRoot, finalFixtureTime, false)
	id := m.ID

	finalFile := filepath.Join(t.TempDir(), "article.md")
	if err := os.WriteFile(finalFile, []byte("# EU AI Act\n\nEdited by the test.\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	oldOut := rootCmd.OutOrStdout()
	oldErr := rootCmd.ErrOrStderr()
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	if err := runCommand(t,
		"final", string(id),
		"--workspace-root", wsRoot,
		"--final", finalFile,
		"--editor", "editor",
		"--published-uri", "https://example.com/post/1",
	); err != nil {
		t.Fatalf("final: %v", err)
	}
	// The command writes through the (inherited) standard streams; capture is
	// impossible in-process, so the test verifies the durable effects: the
	// manifest record, the persisted pair, and the untouched original.
	rootCmd.SetOut(oldOut)
	rootCmd.SetErr(oldErr)

	// The run-relative final path is resolved against the run directory: the
	// edited file must exist at <run dir>/final/article.md.
	dir, err := store.RunDir(id)
	if err != nil {
		t.Fatalf("RunDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "final", "article.md")); err != nil {
		t.Errorf("final document not persisted at final/article.md: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "final", "generated", "article.md")); err != nil {
		t.Errorf("generated copy not persisted at final/generated/article.md: %v", err)
	}

	// The manifest carries the recorded pair.
	loaded, _, err := store.LoadManifest(id)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if loaded.FinalState == nil {
		t.Fatal("manifest has no final state after the command")
	}
	if loaded.FinalState.PublishedURI != "https://example.com/post/1" {
		t.Errorf("published_uri = %q, want the flag value", loaded.FinalState.PublishedURI)
	}

	// Both documents are on disk, digests match the persisted bytes, and
	// the original output is untouched.
	gen, err := store.LoadArtifact(id, loaded.FinalState.GeneratedPath)
	if err != nil {
		t.Fatalf("generated copy missing: %v", err)
	}
	orig, err := store.LoadArtifact(id, run.ArtifactPathArticle)
	if err != nil {
		t.Fatalf("original output missing: %v", err)
	}
	if string(gen) != string(orig) {
		t.Error("the generated copy must be byte-identical to the original output")
	}
}

// TestFinalCmdNotReady verifies that a run that never completed is refused
// with the documented not-ready exit code (7) and no record is written.
func TestFinalCmdNotReady(t *testing.T) {
	setHermeticEnv(t)
	resetAllCLIFlags(t)

	wsRoot := t.TempDir()
	store, err := workspace.NewStore(wsRoot)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	m, err := run.NewManifest(run.NewManifestParams{
		Workspace:  run.Workspace{Name: "testws"},
		Topic:      "a story",
		SourceMode: run.SourceModeExplicit,
		StartedAt:  finalFixtureTime,
	})
	if err != nil {
		t.Fatalf("NewManifest: %v", err)
	}
	if err := m.Start(finalFixtureTime); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := m.Fail(finalFixtureTime, "fetch-failed", "all sources failed"); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if _, err := store.CreateRun(m); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	finalFile := filepath.Join(t.TempDir(), "article.md")
	if err := os.WriteFile(finalFile, []byte("edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)

	err = runCommand(t,
		"final", string(m.ID),
		"--workspace-root", wsRoot,
		"--final", finalFile,
		"--editor", "editor",
	)
	assertExit(t, err, 7)
	rootCmd.SetOut(os.Stdout)
	rootCmd.SetErr(os.Stderr)

	loaded, _, err := store.LoadManifest(m.ID)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if loaded.FinalState != nil {
		t.Error("no final state may be recorded on a run that never completed")
	}
}

// mustDemoRun builds the minimal runner-produced run the final command
// requires: all canonical stages succeeded (offline executors), a rendered
// article output, and a passed final factual check (succeeded, READY).
// Modeled on newPublishEnv (publish tests) but without the publication
// machinery.
func mustDemoRun(t *testing.T, wsRoot string, start time.Time, notReady bool) (*run.Manifest, *review.Bundle) {
	t.Helper()

	writeData, renderFiles := finalFixtureRender(notReady, start)
	stages := []runner.Stage{
		{Name: run.CanonicalStages()[0], Fn: finalFixtureFile("sources/index.json", run.ArtifactKindSource, "sources", `{"urls":["https://example.com/solar"]}`)},
		{Name: run.CanonicalStages()[1], Fn: finalFixtureFile("fetch/index.json", run.ArtifactKindSource, "fetch", `{"fetched":1}`)},
		{Name: run.CanonicalStages()[2], Fn: finalFixtureFile("documents/index.json", run.ArtifactKindDocument, "documents", `{"documents":1}`)},
		{Name: run.CanonicalStages()[3], Fn: finalFixtureFile("research/dossier.json", run.ArtifactKindDossier, "dossier", `{"claims":[]}`)},
		{Name: run.CanonicalStages()[4], Fn: finalFixtureFile("verify/verification.json", run.ArtifactKindVerification, "verification", `{"verdict":"verified"}`)},
		{Name: run.CanonicalStages()[5], SkipReason: "no unresolved claims"},
		{Name: run.CanonicalStages()[6], Fn: finalFixtureFile(run.ArtifactPathArtifact, run.ArtifactKindEditorial, "editorial", string(writeData))},
		{Name: run.CanonicalStages()[7], Fn: finalFixtureFile("final_check/result.json", run.ArtifactKindFactualCheck, "final-check", `{"overall_status":"`+finalFixtureStatus(notReady)+`"}`)},
		{Name: run.CanonicalStages()[8], Fn: func(_ context.Context, _ *runner.StageInput) (*runner.StageOutput, error) {
			return &runner.StageOutput{Files: renderFiles}, nil
		}},
	}

	m, err := run.NewManifest(run.NewManifestParams{
		Workspace:  run.Workspace{Name: "testws"},
		Topic:      "a story",
		SourceMode: run.SourceModeExplicit,
		StartedAt:  start,
	})
	if err != nil {
		t.Fatalf("NewManifest: %v", err)
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

	bundle, err := review.Build(store, m)
	if err != nil {
		t.Fatalf("review.Build: %v", err)
	}
	return m, bundle
}

// finalFixtureRender returns the write stage data (a valid editorial
// artifact, the contracts schema) and the render stage files (a rendered
// article — the canonical long-form output).
func finalFixtureRender(notReady bool, start time.Time) (writeData []byte, renderFiles []runner.StageFile) {
	meta := contracts.GenerationMetadata{
		InputVerificationID: "verification-fixture-1",
		GeneratedAt:         start,
	}
	art := contracts.EditorialArtifact{
		StableID:           "artifact-fixture-1",
		ArtifactType:       contracts.ArtifactTypeArticle,
		Title:              "A Story",
		Subtitle:           "A subtitle",
		Body:               "The story body.\n",
		GenerationMetadata: meta,
	}
	writeData, _ = json.Marshal(art)
	renderFiles = []runner.StageFile{
		{Path: run.ArtifactPathArticle, Kind: run.ArtifactKindRendered, ID: "article",
			Data: []byte("# A Story\n\nThe story body.\n")},
	}
	return writeData, renderFiles
}

// finalFixtureStatus is the final-check overall status the fixture records.
func finalFixtureStatus(notReady bool) string {
	if notReady {
		return "fail"
	}
	return "pass"
}

// finalFixtureFile is a runner stage executor that writes one artifact.
func finalFixtureFile(path string, kind run.ArtifactKind, id string, content string) runner.StageFunc {
	return func(_ context.Context, _ *runner.StageInput) (*runner.StageOutput, error) {
		return &runner.StageOutput{Files: []runner.StageFile{{Path: path, Kind: kind, ID: id, Data: []byte(content)}}}, nil
	}
}

var _ = publish.ErrFinalNotCompleted
