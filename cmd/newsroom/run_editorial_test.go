package main

// End-to-end tests for the external editorial bundle on the run command.
//
// Every test builds a private bundle in a temp directory (a stand-in for a
// separately checked-out repository) and drives the real command or plan
// functions. The bundle content is deliberately distinctive ("PRIVATE
// BUNDLE TEXT", a handle, a public URL) so the tests can assert the
// privacy contract: resolved non-secret metadata (name, bundle path,
// hash) may land in run artifacts, but no private content may.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/config"
	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/orchestrator"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/runner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workspace"
)

// privateStyleText is the recognizable private content of the test bundle.
const privateStyleText = "PRIVATE BUNDLE STYLE TEXT"

// privateAuthorText is the recognizable private content of the author profile.
const privateAuthorText = "PRIVATE AUTHOR VOICE TEXT"

// privateHandle is the recognizable Bluesky handle of the test preset.
const privateHandle = "@privatebundle"

// privateURL is the recognizable Hugo base URL of the test preset.
const privateURL = "https://www.private-bundle.example"

// writeBundleFile writes one bundle file, creating parent directories.
func writeBundleFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// fullEditorialBundle writes a complete private bundle:
//
//	root/
//	  common/style.md, common/facts-policy.md
//	  authors/javi/style.md, authors/javi2/style.md
//	  publications/mundodolphins/{publication.yaml, hugo.yaml, bluesky.yaml}
//	  publications/other/publication.yaml  (language: de)
//
// and returns its root.
func fullEditorialBundle(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeBundleFile(t, filepath.Join(root, "common", "style.md"), "## Style\n"+privateStyleText+"\n")
	writeBundleFile(t, filepath.Join(root, "common", "facts-policy.md"), "Verify all claims against sources.\n")
	writeBundleFile(t, filepath.Join(root, "authors", "javi", "style.md"), "## Voice\n"+privateAuthorText+"\n")
	writeBundleFile(t, filepath.Join(root, "authors", "javi2", "style.md"), "## Voice\nSecond author voice.\n")
	writeBundleFile(t, filepath.Join(root, "publications", "mundodolphins", "publication.yaml"),
		"profile: javi\nlanguage: es\nrenderer: thread\nhugo: hugo.yaml\nbluesky: bluesky.yaml\n")
	writeBundleFile(t, filepath.Join(root, "publications", "mundodolphins", "hugo.yaml"),
		"destination: sites/mundodolphins\nbase_url: "+privateURL+"\n")
	writeBundleFile(t, filepath.Join(root, "publications", "mundodolphins", "bluesky.yaml"),
		"handle: \""+privateHandle+"\"\napi_key:\n  env: BLUESKY_API_KEY\n")
	writeBundleFile(t, filepath.Join(root, "publications", "other", "publication.yaml"), "language: de\n")
	return root
}

// TestRunWithPublicationPreset runs the full command with a publication
// preset selected from an external bundle and asserts: the preset's
// language/renderer/profile apply where no higher layer did, the manifest
// records the non-secret identity (name, bundle path, hash), the run
// succeeds and renders, and no private bundle content leaks into the
// manifest.
func TestRunWithPublicationPreset(t *testing.T) {
	setHermeticEnv(t)
	h := newRunHarness(t, "pass")
	bundle := fullEditorialBundle(t)

	// No --language and no --format: the preset supplies both (es, thread).
	h.setThreadResponses() // the writer requests a thread (preset renderer)
	args := []string{
		"run",
		"--topic", "Miami Dolphins rookie",
		"--llm-base-url", h.llmSrv.URL,
		"--llm-api-key", "test-key",
		"--llm-model", "test-model",
		"--workspace-root", h.workspace,
		"--url", h.articleURLs[0],
		"--url", h.articleURLs[1],
		"--publication", "mundodolphins",
		"--bundle-root", bundle,
	}
	if err := runCommand(t, args...); err != nil {
		t.Fatalf("run with publication should succeed: %v", err)
	}

	m := h.loadManifest(t)
	if m.Status != run.RunStatusSucceeded {
		t.Fatalf("manifest status = %s, want succeeded", m.Status)
	}

	// The publication identity is recorded: name, bundle path, hash.
	if m.Publication == nil {
		t.Fatal("manifest must record the selected publication")
	}
	if m.Publication.ID != "mundodolphins" {
		t.Errorf("publication ID = %q", m.Publication.ID)
	}
	if m.Publication.BundlePath != bundle {
		t.Errorf("bundle path = %q, want %q", m.Publication.BundlePath, bundle)
	}
	if len(m.Publication.Hash) != 64 {
		t.Errorf("publication hash = %q, want 64 hex chars", m.Publication.Hash)
	}

	// The preset's profile reference resolved to the bundle's author.
	if m.Profile == nil || m.Profile.ID != "javi" {
		t.Errorf("profile = %+v, want ID javi (from the preset)", m.Profile)
	}

	// The preset supplied the language and the renderer.
	if v, _ := m.Config.Get(orchestrator.SettingRunLanguage); v != "es" {
		t.Errorf("run.language = %q, want es (preset)", v)
	}
	if v, _ := m.Config.Get(orchestrator.SettingRunFormat); v != "thread" {
		t.Errorf("run.format = %q, want thread (preset)", v)
	}
	if v, _ := m.Config.Get(orchestrator.SettingRunPublication); v != "mundodolphins" {
		t.Errorf("run.publication = %q, want mundodolphins", v)
	}

	// The run rendered the preset's output shape.
	runDir := h.findRunDir(t)
	if !fileExistsPath(filepath.Join(runDir, "output", "thread.txt")) {
		t.Error("missing rendered thread output (the preset renderer must apply)")
	}

	// Privacy: the manifest on disk carries identity, never content.
	data, err := os.ReadFile(filepath.Join(runDir, "manifest.json"))
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	manifestJSON := string(data)
	for _, private := range []string{privateStyleText, privateAuthorText, privateHandle, privateURL} {
		if strings.Contains(manifestJSON, private) {
			t.Errorf("manifest leaks private bundle content %q", private)
		}
	}
	if !strings.Contains(manifestJSON, "mundodolphins") {
		t.Error("manifest must record the publication name")
	}
}

// TestRunWithPublicationPrecedence: explicit operator layers (flags and
// environment) beat the preset, which in turn beats built-in defaults.
func TestRunWithPublicationPrecedence(t *testing.T) {
	t.Run("flags beat the preset", func(t *testing.T) {
		setHermeticEnv(t)
		h := newRunHarness(t, "pass")
		bundle := fullEditorialBundle(t)
		args := []string{
			"run",
			"--topic", "Miami Dolphins rookie",
			"--llm-base-url", h.llmSrv.URL,
			"--llm-api-key", "test-key",
			"--llm-model", "test-model",
			"--workspace-root", h.workspace,
			"--url", h.articleURLs[0],
			"--language", "en", // preset says es
			"--format", "article",
			"--profile", "javi2", // preset says javi
			"--publication", "mundodolphins",
			"--bundle-root", bundle,
		}
		if err := runCommand(t, args...); err != nil {
			t.Fatalf("run should succeed: %v", err)
		}
		m := h.loadManifest(t)
		if v, _ := m.Config.Get(orchestrator.SettingRunLanguage); v != "en" {
			t.Errorf("run.language = %q, want en (flag beats preset)", v)
		}
		if v, _ := m.Config.Get(orchestrator.SettingRunFormat); v != "article" {
			t.Errorf("run.format = %q, want article (flag beats preset)", v)
		}
		if m.Profile == nil || m.Profile.ID != "javi2" {
			t.Errorf("profile = %+v, want javi2 (flag beats preset)", m.Profile)
		}
	})

	t.Run("environment beats the preset", func(t *testing.T) {
		setHermeticEnv(t)
		h := newRunHarness(t, "pass")
		bundle := fullEditorialBundle(t)
		t.Setenv("NEWSROOM_LANGUAGE", "fr") // preset says es
		h.setThreadResponses()              // preset renderer still applies (no flag/env)
		args := []string{
			"run",
			"--topic", "Miami Dolphins rookie",
			"--llm-base-url", h.llmSrv.URL,
			"--llm-api-key", "test-key",
			"--llm-model", "test-model",
			"--workspace-root", h.workspace,
			"--url", h.articleURLs[0],
			"--publication", "mundodolphins",
			"--bundle-root", bundle,
		}
		if err := runCommand(t, args...); err != nil {
			t.Fatalf("run should succeed: %v", err)
		}
		m := h.loadManifest(t)
		if v, _ := m.Config.Get(orchestrator.SettingRunLanguage); v != "fr" {
			t.Errorf("run.language = %q, want fr (env beats preset)", v)
		}
		if v, _ := m.Config.Get(orchestrator.SettingRunFormat); v != "thread" {
			t.Errorf("run.format = %q, want thread (preset)", v)
		}
	})
}

// TestRunPublicationRequiresBundleRoot: a selected publication without a
// bundle root is a clean configuration error.
func TestRunPublicationRequiresBundleRoot(t *testing.T) {
	setHermeticEnv(t)
	h := newRunHarness(t, "pass")
	args := append(h.baseArgs(),
		"--url", h.articleURLs[0],
		"--publication", "mundodolphins",
	)
	err := runCommand(t, args...)
	if err == nil {
		t.Fatal("a publication without a bundle root must fail")
	}
	if !strings.Contains(err.Error(), "bundle root") {
		t.Errorf("error should point at the bundle root: %v", err)
	}
}

// TestRunPublicationBundleRootTraversal: a bundle root with ".." segments
// is rejected by the setting validator.
func TestRunPublicationBundleRootTraversal(t *testing.T) {
	setHermeticEnv(t)
	h := newRunHarness(t, "pass")
	args := append(h.baseArgs(),
		"--url", h.articleURLs[0],
		"--bundle-root", "../outside",
	)
	err := runCommand(t, args...)
	if err == nil {
		t.Fatal("a '..' bundle root must fail")
	}
	if !strings.Contains(err.Error(), "..") {
		t.Errorf("error should name the traversal: %v", err)
	}
}

// TestRunPublicationLiteralSecret: a literal credential in a bundle file
// fails the run and never appears in the error.
func TestRunPublicationLiteralSecret(t *testing.T) {
	setHermeticEnv(t)
	h := newRunHarness(t, "pass")
	secret := "super-secret-bluesky-credential"
	bundle := t.TempDir()
	writeBundleFile(t, filepath.Join(bundle, "publications", "p", "publication.yaml"), "bluesky: bluesky.yaml\n")
	writeBundleFile(t, filepath.Join(bundle, "publications", "p", "bluesky.yaml"),
		"handle: \""+privateHandle+"\"\napi_key: "+secret+"\n")

	args := append(h.baseArgs(),
		"--url", h.articleURLs[0],
		"--publication", "p",
		"--bundle-root", bundle,
	)
	err := runCommand(t, args...)
	if err == nil {
		t.Fatal("a literal credential in a bundle file must fail")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error must not echo the secret: %v", err)
	}
}

// TestRunPublicationEnvSelection: the same selection through the
// environment variables (no flags).
func TestRunPublicationEnvSelection(t *testing.T) {
	setHermeticEnv(t)
	h := newRunHarness(t, "pass")
	bundle := fullEditorialBundle(t)
	t.Setenv("NEWSROOM_PUBLICATION", "other")
	t.Setenv("NEWSROOM_EDITORIAL_BUNDLE", bundle)

	args := []string{
		"run",
		"--topic", "Miami Dolphins rookie",
		"--llm-base-url", h.llmSrv.URL,
		"--llm-api-key", "test-key",
		"--llm-model", "test-model",
		"--workspace-root", h.workspace,
		"--url", h.articleURLs[0],
	}
	if err := runCommand(t, args...); err != nil {
		t.Fatalf("run with env-selected publication should succeed: %v", err)
	}
	m := h.loadManifest(t)
	if m.Publication == nil || m.Publication.ID != "other" {
		t.Fatalf("publication = %+v, want other", m.Publication)
	}
	if v, _ := m.Config.Get(orchestrator.SettingRunLanguage); v != "de" {
		t.Errorf("run.language = %q, want de (preset via env selection)", v)
	}
}

// TestRunResumeWithPublication: a run that recorded a publication resumes
// with the RECORDED preset even when the current configuration has moved on
// (a different publication flag, a different language flag): the manifest
// and the run's own snapshot are the source of truth.
func TestRunResumeWithPublication(t *testing.T) {
	setHermeticEnv(t)
	h := newRunHarness(t, "fail")
	bundle := fullEditorialBundle(t)

	// Thread writer (the preset renders a thread) and a failing check.
	h.llm.setRules(
		llmResponseRule{marker: markerFinalChecker, response: runCheckJSON("fail")},
		llmResponseRule{marker: markerWriter, response: runArtifactJSON(contracts.ArtifactTypeThread)},
		llmResponseRule{marker: markerVerifier, response: runVerificationJSON(contracts.VerificationStatusSupported)},
		llmResponseRule{marker: markerResearcher, response: runDossierJSON(h.articleURLs)},
		llmResponseRule{marker: markerPlanner, response: runPlanJSON()},
	)
	args := []string{
		"run",
		"--topic", "Miami Dolphins rookie",
		"--llm-base-url", h.llmSrv.URL,
		"--llm-api-key", "test-key",
		"--llm-model", "test-model",
		"--workspace-root", h.workspace,
		"--url", h.articleURLs[0],
		"--url", h.articleURLs[1],
		"--publication", "mundodolphins",
		"--bundle-root", bundle,
	}
	if err := runCommand(t, args...); err == nil {
		t.Fatal("first run should fail (check verdict: fail)")
	}
	m := h.loadManifest(t)
	id := m.ID
	if m.Publication == nil || m.Publication.ID != "mundodolphins" {
		t.Fatalf("first run must record the publication: %+v", m.Publication)
	}

	// Resume with the current configuration pointed elsewhere (a different
	// publication, a different language and format): the recorded
	// publication must win, the run must complete in the recorded shape
	// (thread output from the recorded snapshot format), and the writer
	// must use the recorded language (es), not the --language flag.
	h.setCheckVerdict("pass")
	h.llm.setRules(
		llmResponseRule{marker: markerFinalChecker, response: runCheckJSON("pass")},
		llmResponseRule{marker: markerWriter, response: runArtifactJSON(contracts.ArtifactTypeThread)},
		llmResponseRule{marker: markerVerifier, response: runVerificationJSON(contracts.VerificationStatusSupported)},
		llmResponseRule{marker: markerResearcher, response: runDossierJSON(h.articleURLs)},
		llmResponseRule{marker: markerPlanner, response: runPlanJSON()},
	)
	err := runCommand(t,
		"run", "--resume", string(id),
		"--llm-base-url", h.llmSrv.URL,
		"--llm-api-key", "test-key",
		"--llm-model", "test-model",
		"--workspace-root", h.workspace,
		"--publication", "other",
		"--bundle-root", bundle,
		"--language", "en",
		"--format", "article",
	)
	if err != nil {
		t.Fatalf("resume should succeed: %v", err)
	}

	m = h.loadManifest(t)
	if m.Status != run.RunStatusSucceeded {
		t.Fatalf("resumed status = %s, want succeeded", m.Status)
	}
	// The manifest still carries the original publication (never the
	// --publication "other" passed on resume).
	if m.Publication == nil || m.Publication.ID != "mundodolphins" {
		t.Errorf("publication after resume = %+v, want mundodolphins", m.Publication)
	}
	// The recorded format (thread) still shaped the output, despite
	// --format article on resume.
	runDir := h.findRunDir(t)
	if !fileExistsPath(filepath.Join(runDir, "output", "thread.txt")) {
		t.Error("resumed run must render the recorded thread output")
	}
}

// TestPlanRunNewPresetLanguageInWriterPrompt is the plan-level twin of the
// preset tests: it drives planRun directly and inspects the writer prompt
// the plan builds, asserting the preset's language reaches the LLM prompt.
func TestPlanRunNewPresetLanguageInWriterPrompt(t *testing.T) {
	setHermeticEnv(t)
	oldAppConfig := appConfig
	appConfig = config.New(nil, "")
	t.Cleanup(func() { appConfig = oldAppConfig })

	srv, prompts := newPlanUnitLLMServer(t)
	bundle := fullEditorialBundle(t)

	root := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatalf("creating workspace store: %v", err)
	}

	// The zero-value resolution inside planRun starts from the hermetic
	// environment: language and format fall to the built-in defaults, so
	// the preset's values are free to apply; the bundle and publication
	// arrive through the override map.
	plan, err := planRun(context.Background(), runIntent{
		Topic: "plan publication unit test",
		URLs:  []string{"https://example.com/a"},
		Workspace: run.Workspace{
			Name: "default",
			Path: root,
		},
	}, map[string]config.Override{
		config.SettingLLMBaseURL:           config.Override{Value: srv.URL, Changed: true},
		config.SettingLLMModel:             config.Override{Value: "unit-model", Changed: true},
		config.SettingLLMAPIKey:            config.Override{Value: "unit-key", Changed: true},
		config.SettingEditorialBundle:      config.Override{Value: bundle, Changed: true},
		config.SettingEditorialPublication: config.Override{Value: "mundodolphins", Changed: true},
	})
	if err != nil {
		t.Fatalf("planRun: %v", err)
	}

	// The preset must have produced its identity on the manifest.
	if plan.Manifest.Publication == nil || plan.Manifest.Publication.ID != "mundodolphins" {
		t.Fatalf("manifest publication = %+v, want mundodolphins", plan.Manifest.Publication)
	}
	if v, _ := plan.Manifest.Config.Get(orchestrator.SettingRunLanguage); v != "es" {
		t.Errorf("run.language = %q, want es (preset)", v)
	}
	if v, _ := plan.Manifest.Config.Get(orchestrator.SettingRunFormat); v != "thread" {
		t.Errorf("run.format = %q, want thread (preset)", v)
	}
	if plan.Manifest.Profile == nil || plan.Manifest.Profile.ID != "javi" {
		t.Errorf("profile = %+v, want javi (preset profile reference)", plan.Manifest.Profile)
	}

	// Create the plan's run in the store (planRun does not persist the
	// manifest itself — the command layer does), then persist the
	// verification input where the write stage reads it.
	if _, err := store.CreateRun(plan.Manifest); err != nil {
		t.Fatalf("creating run: %v", err)
	}
	data, err := json.Marshal(planUnitVerification())
	if err != nil {
		t.Fatalf("marshaling verification fixture: %v", err)
	}
	ref := run.ArtifactRef{
		Kind:       run.ArtifactKindVerification,
		ID:         "unit-verification",
		Stage:      run.StageVerify,
		Path:       run.ArtifactPathVerification,
		ProducedAt: run.NewTimestamp(time.Now().UTC()),
	}
	if _, err := store.SaveArtifact(plan.Manifest.ID, ref, data); err != nil {
		t.Fatalf("saving verification artifact: %v", err)
	}

	var writeFn runner.StageFunc
	for _, s := range plan.Orch.BuildStages(plan.Manifest) {
		if s.Name == run.StageWrite {
			writeFn = s.Fn
		}
	}
	if writeFn == nil {
		t.Fatal("no write stage in the plan")
	}
	if _, err := writeFn(context.Background(), &runner.StageInput{
		RunID:    plan.Manifest.ID,
		Stage:    run.StageWrite,
		Manifest: plan.Manifest,
		Store:    store,
	}); err != nil {
		t.Fatalf("executing write stage: %v", err)
	}

	got := prompts()
	for _, p := range got {
		if strings.Contains(p, "language: es") {
			return
		}
	}
	t.Errorf("writer prompt must request the preset language es; got %d prompt(s), none matching", len(got))
}
