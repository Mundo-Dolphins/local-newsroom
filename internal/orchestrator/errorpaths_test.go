package orchestrator

// Error-path and snapshot-fallback tests for the orchestration plumbing:
// the per-run parameter helpers, stage-file helpers, and the small adapters
// that the happy-path run tests never push into their failure branches.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Mundo-Dolphins/local-newsroom/internal/config"
	"github.com/Mundo-Dolphins/local-newsroom/internal/planner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/runner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/search"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workspace"
)

// testStore creates a throwaway workspace store.
func testStore(t *testing.T) *workspace.Store {
	t.Helper()
	store, err := workspace.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return store
}

// snapshotManifest builds a manifest carrying explicit snapshot values.
func snapshotManifest(t *testing.T, values map[string]string) *run.Manifest {
	t.Helper()
	m, err := run.NewManifest(run.NewManifestParams{
		Workspace:  run.Workspace{Name: "test"},
		Topic:      "topic",
		SourceMode: run.SourceModeExplicit,
		Config:     &run.ConfigSnapshot{Values: values},
		Stages:     run.CanonicalStages(),
		StartedAt:  testTime,
	})
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	return m
}

func TestRunParamSnapshotAndFallback(t *testing.T) {
	o, _ := newOrchestrator(t, testStore(t), Config{})
	in := &runner.StageInput{Manifest: snapshotManifest(t, map[string]string{
		SettingRunLanguage: "es",
	})}
	if got := o.runParam(in, "run.language", "en"); got != "es" {
		t.Errorf("runParam with snapshot = %q, want es", got)
	}
	if got := o.runParam(in, "run.missing", "en"); got != "en" {
		t.Errorf("runParam without snapshot = %q, want the fallback", got)
	}
}

func TestLanguageFromRunPrecedence(t *testing.T) {
	o, _ := newOrchestrator(t, testStore(t), Config{})
	// Snapshot run.language wins...
	in := &runner.StageInput{Manifest: snapshotManifest(t, map[string]string{
		SettingRunLanguage:             "es",
		config.SettingDefaultsLanguage: "en",
	})}
	if got := o.languageFromRun(in); got != "es" {
		t.Errorf("language = %q, want es (run snapshot wins)", got)
	}
	// ...then defaults.language...
	in2 := &runner.StageInput{Manifest: snapshotManifest(t, map[string]string{
		config.SettingDefaultsLanguage: "en",
	})}
	if got := o.languageFromRun(in2); got != "en" {
		t.Errorf("language = %q, want en (defaults layer)", got)
	}
	// ...and nothing else (Language is not a run parameter).
	in3 := &runner.StageInput{Manifest: snapshotManifest(t, nil)}
	if got := o.languageFromRun(in3); got != "" {
		t.Errorf("language = %q, want empty", got)
	}
}

func TestExplicitURLsFromRunFallbacks(t *testing.T) {
	o, _ := newOrchestrator(t, testStore(t), Config{ExplicitURLs: []string{"http://fallback"}})
	// Valid JSON array from the snapshot.
	in := &runner.StageInput{Manifest: snapshotManifest(t, map[string]string{
		SettingRunExplicitURLs: `["http://a","http://b"]`,
	})}
	if got := o.explicitURLsFromRun(in); len(got) != 2 || got[0] != "http://a" {
		t.Errorf("urls = %v", got)
	}
	// Invalid JSON falls back to the config.
	in2 := &runner.StageInput{Manifest: snapshotManifest(t, map[string]string{
		SettingRunExplicitURLs: `{not-json`,
	})}
	if got := o.explicitURLsFromRun(in2); len(got) != 1 || got[0] != "http://fallback" {
		t.Errorf("fallback urls = %v", got)
	}
}

func TestArchiveTopKFromRun(t *testing.T) {
	o, _ := newOrchestrator(t, testStore(t), Config{ArchiveTopK: 5})
	in := &runner.StageInput{Manifest: snapshotManifest(t, map[string]string{
		SettingRunArchiveTopK: "3",
	})}
	if got := o.archiveTopK(in); got != 3 {
		t.Errorf("topK = %d, want 3", got)
	}
	// A corrupt value falls back to the config.
	in2 := &runner.StageInput{Manifest: snapshotManifest(t, map[string]string{
		SettingRunArchiveTopK: "lots",
	})}
	if got := o.archiveTopK(in2); got != 5 {
		t.Errorf("fallback topK = %d, want 5", got)
	}
}

func TestArchiveEnabledForRun(t *testing.T) {
	oWith, _ := newOrchestrator(t, testStore(t), Config{Archive: &fakeArchive{}})
	oWithout, _ := newOrchestrator(t, testStore(t), Config{})

	enabled := &runner.StageInput{Manifest: snapshotManifest(t, map[string]string{
		SettingRunArchiveEnabled: "true",
	})}
	if !oWith.archiveEnabledForRun(enabled) {
		t.Error("snapshot true must enable the archive")
	}
	if !oWithout.archiveEnabledForRun(enabled) {
		t.Error("snapshot true must report the archive enabled (usage sites still require the service)")
	}
	disabled := &runner.StageInput{Manifest: snapshotManifest(t, map[string]string{
		SettingRunArchiveEnabled: "false",
	})}
	if oWith.archiveEnabledForRun(disabled) {
		t.Error("snapshot false must disable the archive")
	}
	// Corrupt value falls back to service presence.
	corrupt := &runner.StageInput{Manifest: snapshotManifest(t, map[string]string{
		SettingRunArchiveEnabled: "maybe",
	})}
	if !oWith.archiveEnabledForRun(corrupt) {
		t.Error("corrupt snapshot value must fall back to service presence")
	}
	if oWithout.archiveEnabledForRun(corrupt) {
		t.Error("no service and corrupt snapshot must mean disabled")
	}
}

func TestMaxSourcesFromRun(t *testing.T) {
	o, _ := newOrchestrator(t, testStore(t), Config{MaxSources: 7})
	in := &runner.StageInput{Manifest: snapshotManifest(t, map[string]string{
		SettingRunMaxSources: "4",
	})}
	if got := o.maxSourcesFromRun(in); got != 4 {
		t.Errorf("maxSources = %d, want 4", got)
	}
	in2 := &runner.StageInput{Manifest: snapshotManifest(t, map[string]string{
		SettingRunMaxSources: "many",
	})}
	if got := o.maxSourcesFromRun(in2); got != 7 {
		t.Errorf("fallback maxSources = %d, want 7", got)
	}
}

func TestFollowUpEnabledFor(t *testing.T) {
	o, _ := newOrchestrator(t, testStore(t), Config{})
	enabled := snapshotManifest(t, map[string]string{SettingRunFollowUp: "true"})
	if !o.followUpEnabledFor(enabled) {
		t.Error("snapshot true must enable follow-up")
	}
	disabled := snapshotManifest(t, map[string]string{SettingRunFollowUp: "false"})
	if o.followUpEnabledFor(disabled) {
		t.Error("snapshot false must disable follow-up")
	}
	if o.followUpEnabledFor(snapshotManifest(t, nil)) {
		t.Error("absent snapshot must keep the construction-time default (disabled)")
	}
	// A configured follow-up service is the default when the snapshot is silent.
	oWith, _ := newOrchestrator(t, testStore(t), Config{FollowUp: &fakeFollowUp{}, Discovery: &fakeDiscovery{}})
	if !oWith.followUpEnabledFor(snapshotManifest(t, nil)) {
		t.Error("a configured follow-up service must be enabled by default")
	}
}

func TestStageFileMarshalFailure(t *testing.T) {
	o, _ := newOrchestrator(t, testStore(t), Config{})
	_, err := o.stageFile(run.ArtifactKindCustom, "x.json", make(chan int))
	if err == nil {
		t.Fatal("marshaling a channel must fail")
	}
}

func TestLoadStageJSONErrors(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	runDir := filepath.Join(root, "runs", "run-x")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	in := &runner.StageInput{RunID: "run-x", Manifest: snapshotManifest(t, nil), Store: store, RunDir: runDir}

	// Missing artifact.
	if _, err := loadStageJSON[map[string]string](in, "missing/index.json"); err == nil {
		t.Error("missing artifact must error")
	}
	// Malformed JSON.
	if err := os.MkdirAll(filepath.Join(runDir, "files", "bad"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "files", "bad", "x.json"), []byte("{not"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadStageJSON[map[string]string](in, filepath.Join("bad", "x.json")); err == nil {
		t.Error("malformed artifact must error")
	}
}

func TestPlannerAdapterGenerate(t *testing.T) {
	a := plannerAdapter{planner: &fakePlanner{}}
	plan, err := a.Generate(context.Background(), "topic", "en", nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if plan.OriginalTopic != "topic" {
		t.Errorf("plan = %+v", plan)
	}

	// Planner failures propagate.
	b := plannerAdapter{planner: &fakePlanner{err: context.DeadlineExceeded}}
	if _, err := b.Generate(context.Background(), "topic", "en", nil); err == nil {
		t.Error("planner error must propagate")
	}
}

// TestResumeID returns the run ID for a stored run and fails for a missing
// one.
func TestResumeID(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	m := testManifest(t, store, "topic", "explicit")
	if _, err := store.CreateRun(m); err != nil {
		t.Fatalf("creating run: %v", err)
	}
	o, _ := newOrchestrator(t, store, Config{})
	res, _ := o.ResumeID(context.Background(), m.ID)
	if res == nil || res.RunID != m.ID {
		t.Errorf("ResumeID result = %+v, want run id %q", res, m.ID)
	}
	if _, err := o.ResumeID(context.Background(), "nope"); err == nil {
		t.Error("missing run must error")
	}
}

// TestFollowUpSearcherZeroRank guards the rank-relevance floor used by the
// follow-up searcher when a provider does not supply ranks.
func TestFollowUpSearcherUnrankedHits(t *testing.T) {
	t1 := "T"
	p := &fakeSearchProvider{res: &search.Response{Results: []search.SearchResult{{URL: "http://u", Title: &t1}}}}
	s := followUpSearcher{provider: p}
	res, err := s.Search(context.Background(), planner.SearchQuery{Query: "q"}, 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Hits) != 1 || res.Hits[0].Relevance != 1.0 {
		t.Errorf("unranked hit must default to rank 1 relevance: %+v", res.Hits)
	}
}
