package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/config"
	"github.com/Mundo-Dolphins/local-newsroom/internal/profiles"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/tui"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workspace"
)

// TestTuiCommandRequiresTTY verifies the TUI refuses to start without an
// interactive terminal (so piped/CI invocations fail fast with a clear
// message instead of wedging in raw mode).
func TestTuiCommandRequiresTTY(t *testing.T) {
	setHermeticEnv(t)
	oldWS, oldRoot := tf.workspace, tf.workspaceRoot
	t.Cleanup(func() { tf.workspace, tf.workspaceRoot = oldWS, oldRoot })

	// Replace stdin with a pipe: never a terminal, regardless of whether the
	// test itself runs inside one.
	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldStdin; _ = w.Close(); _ = r.Close() })

	err = runCommand(t, "tui", "--workspace-root", t.TempDir())
	if err == nil {
		t.Fatal("tui without a TTY must fail")
	}
	if !strings.Contains(err.Error(), "interactive terminal") {
		t.Errorf("error = %q, want it to mention the interactive terminal", err)
	}
	if !strings.Contains(err.Error(), "newsroom run") {
		t.Error("the error should point at the non-interactive alternative")
	}
}

// buildFailedRun creates a persisted run in the workspace that failed its
// final factual check (with a rendered artifact from the write stage, one
// warning, and one run-level error).
func buildFailedRun(t *testing.T, store *workspace.Store, root string) run.RunID {
	t.Helper()
	m, err := run.NewManifest(run.NewManifestParams{
		Workspace:  run.Workspace{Name: "default", Path: root},
		Topic:      "Dolphins roster moves",
		SourceMode: run.SourceModeExplicit,
		Stages:     run.CanonicalStages(),
	})
	if err != nil {
		t.Fatalf("NewManifest: %v", err)
	}
	if _, err := store.CreateRun(m); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	id := m.ID

	stages := run.CanonicalStages()
	now := time.Now()
	if err := m.Start(now); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// discovery..verify: run to success.
	for _, name := range stages[:5] {
		if err := m.StartStage(name, now); err != nil {
			t.Fatalf("StartStage(%s): %v", name, err)
		}
		now = now.Add(time.Second)
		if err := m.SucceedStage(name, now); err != nil {
			t.Fatalf("SucceedStage(%s): %v", name, err)
		}
	}
	// follow_up: skipped (no search configured).
	if err := m.SkipStage(stages[5], now.Add(time.Second), "search not configured"); err != nil {
		t.Fatalf("SkipStage(%s): %v", stages[5], err)
	}
	// write: runs, produces the rendered artifact.
	if err := m.StartStage(stages[6], now.Add(2*time.Second)); err != nil {
		t.Fatalf("StartStage(%s): %v", stages[6], err)
	}
	now = now.Add(3 * time.Second)
	ref := run.ArtifactRef{
		Kind:       run.ArtifactKindRendered,
		ID:         "story.md",
		Stage:      stages[6],
		Path:       "artifacts/story.md",
		ProducedAt: run.NewTimestamp(now),
	}
	if err := m.AddArtifact(ref); err != nil {
		t.Fatalf("AddArtifact: %v", err)
	}
	if err := m.SucceedStage(stages[6], now); err != nil {
		t.Fatalf("SucceedStage(%s): %v", stages[6], err)
	}
	if err := m.AddWarning("slow_stage", "verify took a while", now.Add(time.Second)); err != nil {
		t.Fatalf("AddWarning: %v", err)
	}
	// final_check: running when the run fails.
	if err := m.StartStage(stages[7], now.Add(2*time.Second)); err != nil {
		t.Fatalf("StartStage(%s): %v", stages[7], err)
	}
	if err := m.Fail(now.Add(3*time.Second), "final_check_failed", "2 factual findings"); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	if _, err := store.SaveArtifact(id, ref, []byte("# Story\n\nBody text.\n")); err != nil {
		t.Fatalf("SaveArtifact: %v", err)
	}
	if _, err := store.SaveManifest(m); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
	return id
}

// TestTuiDTOMapping verifies the manifest → TUI DTO mapping against a real
// workspace store (list rows and detail rows).
func TestTuiDTOMapping(t *testing.T) {
	setHermeticEnv(t)
	root := t.TempDir()
	san := run.NewSanitizer()
	store, err := workspace.NewStoreWithSanitizer(root, san)
	if err != nil {
		t.Fatalf("NewStoreWithSanitizer: %v", err)
	}
	id := buildFailedRun(t, store, root)

	app := &tuiApp{
		cfg:           config.New(nil, ""),
		workspaceRoot: root,
		workspaceName: "default",
		store:         store,
	}

	infos, err := app.ListRuns(context.Background())
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("ListRuns returned %d runs, want 1", len(infos))
	}
	info := infos[0]
	if info.ID != string(id) {
		t.Errorf("ID = %q, want %q", info.ID, id)
	}
	if info.Status != "failed" {
		t.Errorf("Status = %q, want failed", info.Status)
	}
	if info.Topic != "Dolphins roster moves" {
		t.Errorf("Topic = %q", info.Topic)
	}
	if info.Errors != 1 {
		t.Errorf("Errors = %d, want 1", info.Errors)
	}
	if info.Warnings != 1 {
		t.Errorf("Warnings = %d, want 1", info.Warnings)
	}
	if info.Artifacts != 1 {
		t.Errorf("Artifacts = %d, want 1", info.Artifacts)
	}

	d, err := app.RunDetail(context.Background(), string(id))
	if err != nil {
		t.Fatalf("RunDetail: %v", err)
	}
	if d.Status != "failed" {
		t.Errorf("detail status = %q", d.Status)
	}
	if d.FailedStage != "final_check" {
		t.Errorf("FailedStage = %q, want final_check", d.FailedStage)
	}
	if len(d.Stages) != len(run.CanonicalStages()) {
		t.Fatalf("detail stages = %d, want %d", len(d.Stages), len(run.CanonicalStages()))
	}
	if d.Stages[0].Status != "succeeded" {
		t.Errorf("stage[0] status = %q, want succeeded", d.Stages[0].Status)
	}
	if d.Stages[5].Status != "skipped" || d.Stages[5].SkipReason == "" {
		t.Errorf("stage[5] = %q/%q, want skipped with a reason", d.Stages[5].Status, d.Stages[5].SkipReason)
	}
	if d.Stages[7].Status != "failed" || !strings.Contains(d.Stages[7].Error, "findings") {
		t.Errorf("stage[7] = %q/%q, want failed with the failure message", d.Stages[7].Status, d.Stages[7].Error)
	}
	if len(d.Warnings) != 1 || !strings.Contains(d.Warnings[0], "slow_stage") {
		t.Errorf("warnings = %v", d.Warnings)
	}
	if len(d.Errors) != 1 || !strings.Contains(d.Errors[0], "findings") {
		t.Errorf("errors = %v, want the run-level error", d.Errors)
	}
	if len(d.Artifacts) != 1 {
		t.Fatalf("artifacts = %v, want 1", d.Artifacts)
	}
	art := d.Artifacts[0]
	if !filepath.IsAbs(art.Path) || !strings.HasSuffix(art.Path, "artifacts/story.md") {
		t.Errorf("artifact path = %q, want an absolute path in the run dir", art.Path)
	}
	if art.Size <= 0 {
		t.Errorf("artifact size = %d, want the on-disk size", art.Size)
	}
	if !d.Resumable {
		t.Errorf("a failed run should be resumable (reason %q)", d.ResumeReason)
	}
}

// TestTuiDTOMappingSucceededRun checks the terminal-success mapping and the
// run summary numbers.
func TestTuiDTOMappingSucceededRun(t *testing.T) {
	setHermeticEnv(t)
	root := t.TempDir()
	store, err := workspace.NewStoreWithSanitizer(root, run.NewSanitizer())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	m, err := run.NewManifest(run.NewManifestParams{
		Workspace:  run.Workspace{Name: "default", Path: root},
		Topic:      "A smooth run",
		SourceMode: run.SourceModeExplicit,
		Stages:     run.CanonicalStages(),
	})
	if err != nil {
		t.Fatalf("NewManifest: %v", err)
	}
	if _, err := store.CreateRun(m); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	id := m.ID
	now := time.Now()
	if err := m.Start(now); err != nil {
		t.Fatalf("Start: %v", err)
	}
	for _, s := range m.Stages {
		if err := m.StartStage(s.Name, now); err != nil {
			t.Fatalf("StartStage(%s): %v", s.Name, err)
		}
		now = now.Add(time.Second)
		if err := m.SucceedStage(s.Name, now); err != nil {
			t.Fatalf("SucceedStage(%s): %v", s.Name, err)
		}
	}
	if err := m.Succeed(now.Add(time.Second)); err != nil {
		t.Fatalf("Succeed: %v", err)
	}
	if _, err := store.SaveManifest(m); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}

	app := &tuiApp{
		cfg:           config.New(nil, ""),
		workspaceRoot: root,
		workspaceName: "default",
		store:         store,
	}
	d, err := app.RunDetail(context.Background(), string(id))
	if err != nil {
		t.Fatalf("RunDetail: %v", err)
	}
	if d.Status != "succeeded" {
		t.Errorf("status = %q", d.Status)
	}
	for i, s := range d.Stages {
		if s.Status != "succeeded" {
			t.Errorf("stage %d (%s) status = %q, want succeeded", i, s.Name, s.Status)
		}
	}
}

// TestListProfiles walks a synthetic profiles tree.
func TestListProfiles(t *testing.T) {
	root := t.TempDir()
	mk := func(p string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("# x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk(filepath.Join(root, profiles.AuthorsDir, "javi", "style.md"))
	mk(filepath.Join(root, "pub1", profiles.AuthorsDir, "ana", "style.md"))
	mk(filepath.Join(root, "pub1", profiles.AuthorsDir, "bob", "style.md"))
	mk(filepath.Join(root, "pub2", profiles.AuthorsDir, "ana", "style.md"))
	mk(filepath.Join(root, "notauth", "style.md")) // a plain directory, no authors

	got, err := listProfiles(root)
	if err != nil {
		t.Fatalf("listProfiles: %v", err)
	}
	var specs []string
	for _, p := range got {
		specs = append(specs, p.Spec)
	}
	want := []string{"javi", "pub1/ana", "pub1/bob", "pub2/ana"}
	if strings.Join(specs, ",") != strings.Join(want, ",") {
		t.Errorf("profiles = %v, want %v", specs, want)
	}
}

// tfworkspaceDefault returns the declared default of the --workspace-root flag.
func tfworkspaceDefault() string {
	if f := tuiCmd.Flags().Lookup("workspace-root"); f != nil {
		return f.DefValue
	}
	return ".newsroom"
}

// isolateTuiWorkspace points the tui command flags at a throwaway workspace
// root (and restores them afterwards) so newTuiApp never touches the
// repository checkout.
func isolateTuiWorkspace(t *testing.T) {
	t.Helper()
	old := tf.workspaceRoot
	tf.workspaceRoot = t.TempDir()
	t.Cleanup(func() { tf.workspaceRoot = old })
}

// =============================================================================
// tuiApp service tests
// =============================================================================

// swapAppConfig installs a custom appConfig for the duration of the test.
func swapAppConfig(t *testing.T, c *config.Config) {
	t.Helper()
	old := appConfig
	appConfig = c
	t.Cleanup(func() { appConfig = old })
}

// TestTuiAppInfo resolves endpoints, format, and the profile selector from a
// real config file.
func TestTuiAppInfo(t *testing.T) {
	setHermeticEnv(t)
	root := t.TempDir()

	profRoot := filepath.Join(root, "profiles")
	mustMkdir := func(p string) {
		t.Helper()
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustMkdir(filepath.Join(profRoot, "authors", "javi"))
	mustMkdir(filepath.Join(profRoot, "pub1", "authors", "ana"))
	if err := os.WriteFile(filepath.Join(profRoot, "authors", "javi", "style.md"), []byte("# j"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profRoot, "pub1", "authors", "ana", "style.md"), []byte("# a"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfgFile := filepath.Join(root, "config.yaml")
	content := fmt.Sprintf(`
llm:
  base_url: http://omlx.example:8080/v1
  model: qwen3-30b
  api_key:
    env: OMLX_API_KEY

search:
  base_url: http://searx.example:8080

defaults:
  output_format: thread

profiles:
  root: %s
  name: pub1/ana
`, strings.ReplaceAll(profRoot, `\`, `/`))
	if err := os.WriteFile(cfgFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := config.Load(cfgFile)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	swapAppConfig(t, config.New(f, cfgFile))

	isolateTuiWorkspace(t)
	app, err := newTuiApp()
	if err != nil {
		t.Fatalf("newTuiApp: %v", err)
	}
	info, err := app.Info()
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.LLMBaseURL != "http://omlx.example:8080/v1" || info.LLMModel != "qwen3-30b" {
		t.Errorf("llm = %q/%q", info.LLMBaseURL, info.LLMModel)
	}
	if !info.LLMConfigured {
		t.Error("LLMConfigured = false, want true")
	}
	if info.SearchBaseURL != "http://searx.example:8080" || !info.SearchConfigured {
		t.Errorf("search = %q configured=%v", info.SearchBaseURL, info.SearchConfigured)
	}
	if info.OutputFormat != "thread" {
		t.Errorf("OutputFormat = %q, want thread", info.OutputFormat)
	}
	if info.ProfilesDefault != "pub1/ana" {
		t.Errorf("ProfilesDefault = %q, want pub1/ana", info.ProfilesDefault)
	}
	var specs []string
	for _, p := range info.Profiles {
		specs = append(specs, p.Spec)
	}
	if strings.Join(specs, ",") != "javi,pub1/ana" {
		t.Errorf("profiles = %v, want [javi pub1/ana]", specs)
	}
}

// TestTuiAppStartNewRunValidation covers the pre-flight errors raised before
// any pipeline work starts.
func TestTuiAppStartNewRunValidation(t *testing.T) {
	setHermeticEnv(t)
	swapAppConfig(t, config.New(nil, ""))
	isolateTuiWorkspace(t)
	app, err := newTuiApp()
	if err != nil {
		t.Fatalf("newTuiApp: %v", err)
	}

	// Explicit mode with no URLs cannot start.
	_, err = app.StartNewRun(context.Background(), tui.NewRunIntent{
		Topic: "x", SourceMode: "explicit",
	})
	if err == nil || !strings.Contains(err.Error(), "source") {
		t.Errorf("explicit without URLs: err = %v, want a source error", err)
	}
	// Auto mode without a search endpoint cannot start.
	_, err = app.StartNewRun(context.Background(), tui.NewRunIntent{
		Topic: "x", SourceMode: "auto",
	})
	if err == nil || !strings.Contains(err.Error(), "search") {
		t.Errorf("auto without search: err = %v, want a search error", err)
	}
	// No cycle must have been started.
	if got := app.InFlight(); got != "" {
		t.Errorf("InFlight = %q, want empty after validation failures", got)
	}
}

// TestTuiAppResumeAndRetryErrors covers cheap error paths of the resume and
// retry service methods.
func TestTuiAppResumeAndRetryErrors(t *testing.T) {
	setHermeticEnv(t)
	swapAppConfig(t, config.New(nil, ""))
	isolateTuiWorkspace(t)
	app, err := newTuiApp()
	if err != nil {
		t.Fatalf("newTuiApp: %v", err)
	}

	err = app.ResumeRun(context.Background(), "run-does-not-exist")
	if err == nil {
		t.Error("resuming a missing run must fail")
	}
	err = app.RetryStage(context.Background(), "run-does-not-exist", "write")
	if err == nil {
		t.Error("retrying a missing run must fail")
	}
}

// TestTuiAppBeginCycleLifecycle exercises the single-flight cycle management
// (begin, cancel, done, in-flight reporting, graceful drain) without the
// pipeline: the work function is a stub.
func TestTuiAppBeginCycleLifecycle(t *testing.T) {
	setHermeticEnv(t)
	swapAppConfig(t, config.New(nil, ""))
	isolateTuiWorkspace(t)
	app, err := newTuiApp()
	if err != nil {
		t.Fatalf("newTuiApp: %v", err)
	}

	m, err := run.NewManifest(run.NewManifestParams{
		Workspace:  run.Workspace{Name: "default", Path: app.workspaceRoot},
		Topic:      "stub",
		SourceMode: run.SourceModeExplicit,
		Stages:     run.CanonicalStages(),
	})
	if err != nil {
		t.Fatalf("NewManifest: %v", err)
	}
	plan := &runPlan{Manifest: m}

	// Work blocks until the cycle context is cancelled (simulates a long
	// pipeline waiting to be cancelled).
	err = app.beginCycle(context.Background(), plan, func() (string, bool, error) {
		<-app.runCtx.Done()
		return string(m.ID), true, nil
	})
	if err != nil {
		t.Fatalf("beginCycle: %v", err)
	}
	if got := app.InFlight(); got != string(m.ID) {
		t.Fatalf("InFlight = %q, want the run id", got)
	}
	// A second cycle must be refused while one is in flight.
	if err := app.beginCycle(context.Background(), plan, func() (string, bool, error) {
		return "x", true, nil
	}); err == nil {
		t.Error("a second beginCycle while one is running must be refused")
	}
	// Cancel and drain.
	app.Cancel()
	deadline := time.After(10 * time.Second)
	select {
	case <-app.Done():
	case <-deadline:
		t.Fatal("the cycle did not finish after cancellation")
	}
	app.waitInFlight()
	if got := app.InFlight(); got != "" {
		t.Errorf("InFlight = %q after completion, want empty", got)
	}
}

// TestTuiAppFullRun drives a complete run through the TUI service layer with
// local fakes (no real LLM, no real search): explicit sources, a fake
// article server, a fake LLM, and a passing final check.
func TestTuiAppFullRun(t *testing.T) {
	setHermeticEnv(t)
	h := newRunHarness(t, "pass")

	swapAppConfig(t, config.New(nil, ""))
	tf.workspaceRoot = h.workspace
	t.Cleanup(func() { tf.workspaceRoot = tfworkspaceDefault() })
	app, err := newTuiApp()
	if err != nil {
		t.Fatalf("newTuiApp: %v", err)
	}
	app.overrides = map[string]config.Override{
		config.SettingLLMBaseURL:   {Value: h.llmSrv.URL, Changed: true},
		config.SettingLLMModel:     {Value: "test-model", Changed: true},
		config.SettingLLMAPIKey:    {Value: "test-key", Changed: true},
		config.SettingLLMTimeout:   {Value: "30", Changed: true},
		config.SettingFetchTimeout: {Value: "30", Changed: true},
	}

	id, err := app.StartNewRun(context.Background(), tui.NewRunIntent{
		Topic:      "Miami Dolphins rookie",
		Format:     "article",
		SourceMode: "explicit",
		URLs:       h.articleURLs,
	})
	if err != nil {
		t.Fatalf("StartNewRun: %v", err)
	}
	deadline := time.After(120 * time.Second)
	select {
	case <-app.Done():
	case <-deadline:
		t.Fatalf("the run did not finish within the deadline (in flight: %q)", app.InFlight())
	}
	if got := app.InFlight(); got != "" {
		t.Errorf("InFlight = %q after completion", got)
	}
	d, err := app.RunDetail(context.Background(), id)
	if err != nil {
		t.Fatalf("RunDetail: %v", err)
	}
	if d.Status != "succeeded" {
		t.Fatalf("status = %q (%s), want succeeded", d.Status, d.Reason)
	}
	if d.FinalCheck != "pass" {
		t.Errorf("FinalCheck = %q, want pass", d.FinalCheck)
	}
	var rendered int
	for _, a := range d.Artifacts {
		if strings.HasSuffix(a.Path, ".md") {
			rendered++
		}
	}
	if rendered == 0 {
		t.Errorf("no rendered artifact among %d artifacts", len(d.Artifacts))
	}
	if len(d.Stages) != len(run.CanonicalStages()) {
		t.Errorf("stages = %d, want %d", len(d.Stages), len(run.CanonicalStages()))
	}
}

// TestSourceModeFromString maps wizard labels to manifest source modes.
func TestSourceModeFromString(t *testing.T) {
	cases := map[string]run.SourceMode{
		"auto (search)":       run.SourceModeDiscovered,
		"discovered (search)": run.SourceModeDiscovered,
		"explicit":            run.SourceModeExplicit,
		"supplement":          run.SourceModeSupplement,
		"":                    run.SourceModeDiscovered,
	}
	for in, want := range cases {
		if got := sourceModeFromString(in); got != want {
			t.Errorf("sourceModeFromString(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestWarningAndErrorText renders run-level warning/error summaries.
func TestWarningAndErrorText(t *testing.T) {
	if got := warningText(run.Warning{Code: "slow_stage", Message: "verify took a while"}); got != "slow_stage: verify took a while" {
		t.Errorf("warningText = %q", got)
	}
	if got := warningText(run.Warning{Code: "", Message: "just a note"}); got != "just a note" {
		t.Errorf("warningText (no code) = %q", got)
	}
	if got := errorText(run.ErrorSummary{Code: "final_check_failed", Message: "2 findings"}); got != "final_check_failed: 2 findings" {
		t.Errorf("errorText = %q", got)
	}
	if got := errorText(run.ErrorSummary{Code: "", Message: "boom"}); got != "boom" {
		t.Errorf("errorText (no code) = %q", got)
	}
}
