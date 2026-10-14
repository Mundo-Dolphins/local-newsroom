package main

// End-to-end tests for the `newsroom run` command.
//
// Every test pins the environment (setHermeticEnv), drives the real cobra
// tree through runCommand, and replaces only the external services with
// local fakes:
//
//   - the LLM: an OpenAI-compatible /chat/completions server (newFakeLLMServer
//     pattern, made mutable so a run can be re-checked with a different
//     verdict on resume);
//   - the sources: a local HTTP article server;
//   - the search: a SearXNG-compatible endpoint.
//
// Everything else — the real fetcher, extractor, researcher, verifier,
// writer, final checker, renderers, orchestrator, runner, workspace, and
// resume layers — runs for real, so these tests cover the full `run`
// command from flag to rendered output.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/researcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
)

// (The stage prompt markers used to route the fake LLM's responses —
// markerPlanner, markerResearcherer, markerVerifier, markerWriter, and
// markerFinalCheckerer — are declared in cli_integration_test.go.)

// runTestTime is the fixed timestamp used across the run fixtures.
var runTestTime = time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

// =============================================================================
// Mutable fake LLM server
// =============================================================================

// runLLMServer is an OpenAI-compatible /chat/completions server whose rules
// can be changed between phases (so a failed run can be resumed with a
// different check verdict). First matching rule wins; the fallback is used
// when no rule matches.
type runLLMServer struct {
	mu       sync.Mutex
	rules    []llmResponseRule
	fallback string
	calls    int
}

func (s *runLLMServer) setRules(rules ...llmResponseRule) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = append([]llmResponseRule(nil), rules...)
}

func (s *runLLMServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++

	var combined strings.Builder
	for _, m := range req.Messages {
		combined.WriteString(m.Content)
		combined.WriteString(" ")
	}
	text := combined.String()

	response := s.fallback
	for _, rule := range s.rules {
		if strings.Contains(text, rule.marker) {
			response = rule.response
			break
		}
	}
	body, _ := json.Marshal(map[string]interface{}{
		"choices": []map[string]interface{}{
			{"message": map[string]string{"role": "assistant", "content": response}},
		},
	})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// =============================================================================
// Canned LLM responses (built from the real types so they are always valid)
// =============================================================================

// runDossierJSON builds the researcher's response: one claim supported by the
// first source.
func runDossierJSON(urls []string) string {
	sources := make([]researcher.SourceReference, 0, len(urls))
	for i, u := range urls {
		title := fmt.Sprintf("Source %d", i+1)
		sources = append(sources, researcher.SourceReference{
			StableID:    u,
			OriginalURL: u,
			SourceType:  types.SourceTypeWeb,
			RetrievedAt: runTestTime,
			Title:       title,
		})
	}
	claim := researcher.Claim{
		ID:         "claim-001",
		Statement:  "The Dolphins signed a rookie wide receiver this offseason.",
		Confidence: researcher.ConfidenceHigh,
		Evidence: []researcher.Evidence{{
			SourceID: urls[0],
			Excerpts: []researcher.Excerpt{{Text: "signed", StartOffset: 0, EndOffset: 6}},
		}},
	}
	d := researcher.ResearchDossier{
		StableID:    "dossier-run-001",
		Topic:       "Miami Dolphins rookie",
		GeneratedAt: runTestTime,
		Sources:     sources,
		Claims:      []researcher.Claim{claim},
	}
	b, _ := json.Marshal(d)
	return string(b)
}

// runVerificationJSON builds the verifier's response: the claim is supported.
func runVerificationJSON(status contracts.VerificationStatus) string {
	v := contracts.VerificationResult{
		StableID:       "ver-run-001",
		InputDossierID: "dossier-run-001",
		VerifiedAt:     runTestTime,
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-001": status,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-001": {
				ClaimID:            "claim-001",
				Statement:          "The Dolphins signed a rookie wide receiver this offseason.",
				VerificationStatus: status,
				ConfidenceLevel:    contracts.ConfidenceHigh,
			},
		},
		QualityScore: 90,
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// runArtifactJSON builds the writer's response: an article citing the claim.
func runArtifactJSON(artifactType contracts.ArtifactType) string {
	a := contracts.EditorialArtifact{
		StableID:     "art-run-001",
		ArtifactType: artifactType,
		Title:        "Dolphins sign rookie wide receiver",
		Body:         "The Dolphins signed a rookie wide receiver this offseason, per two team announcements.",
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-001": {ClaimID: "claim-001", UsageType: contracts.ClaimUsageCore},
		},
		SourceReferences: map[string]contracts.SourceUsage{},
		GenerationMetadata: contracts.GenerationMetadata{
			InputVerificationID: "ver-run-001",
			GeneratedAt:         runTestTime,
		},
	}
	if artifactType == contracts.ArtifactTypeThread || artifactType == contracts.ArtifactTypeHybrid {
		a.Posts = []contracts.Post{{Order: 0, Body: "The Dolphins signed a rookie wide receiver this offseason.", ClaimIDs: []string{"claim-001"}}}
	}
	b, _ := json.Marshal(a)
	return string(b)
}

// runCheckJSON builds the final checker's LLM response. With verdict "fail"
// it carries a critical finding; otherwise it is clean (no findings).
func runCheckJSON(verdict string) string {
	r := contracts.FactualCheckResult{
		StableID:        "check-run-001",
		InputArtifactID: "art-run-001",
		CheckedAt:       runTestTime,
		ClaimIDs:        []string{"claim-001"},
		ClaimVerificationStatus: map[string]contracts.VerificationStatus{
			"claim-001": contracts.VerificationStatusSupported,
		},
		OverallStatus:   contracts.FactualCheckStatusPass,
		ConfidenceLevel: contracts.ConfidenceHigh,
		Summary:         "No factual issues found.",
	}
	if verdict == "fail" {
		r.OverallStatus = contracts.FactualCheckStatusFail
		r.FactualFindings = []contracts.FactualFinding{{
			FindingID:       "finding-run-001",
			FindingType:     contracts.FindingTypeError,
			Statement:       "The body asserts a number that is absent from the verified claims.",
			Severity:        contracts.WarningSeverityCritical,
			Category:        contracts.FindingCategoryClaim,
			RelatedClaimIDs: []string{"claim-001"},
		}}
		r.Summary = "Critical factual issue detected."
	}
	b, _ := json.Marshal(r)
	return string(b)
}

// runPlanJSON builds the planner's response: one query that the fake
// SearXNG server knows how to answer.
func runPlanJSON() string {
	p := struct {
		OriginalTopic string `json:"original_topic"`
		Language      string `json:"language"`
		Queries       []struct {
			Query   string `json:"query"`
			Purpose string `json:"purpose"`
		} `json:"queries"`
	}{
		OriginalTopic: "Miami Dolphins rookie",
		Language:      "en",
		Queries: []struct {
			Query   string `json:"query"`
			Purpose string `json:"purpose"`
		}{{Query: "Miami Dolphins rookie wide receiver", Purpose: "primary"}},
	}
	b, _ := json.Marshal(p)
	return string(b)
}

// =============================================================================
// Test scaffolding
// =============================================================================

// runHarness wires the local fakes for a run test and returns the flag args
// (excluding --url/--search-base-url, which tests append per scenario).
type runHarness struct {
	llm         *runLLMServer // the mutable handler
	llmSrv      *httptest.Server
	articles    *httptest.Server
	articleURLs []string
	workspace   string
}

func newRunHarness(t *testing.T, checkVerdict string) *runHarness {
	t.Helper()

	articles := newFakeArticleServer(t, map[string]string{
		"alpha": "<html><head><title>Alpha</title></head><body><p>The team signed a rookie wide receiver.</p></body></html>",
		"beta":  "<html><head><title>Beta</title></head><body><p>Another outlet confirms the signing.</p></body></html>",
	})
	urls := []string{articles.URL + "/alpha", articles.URL + "/beta"}

	srv := &runLLMServer{
		rules: []llmResponseRule{
			{marker: markerFinalChecker, response: runCheckJSON(checkVerdict)},
			{marker: markerWriter, response: runArtifactJSON(contracts.ArtifactTypeArticle)},
			{marker: markerVerifier, response: runVerificationJSON(contracts.VerificationStatusSupported)},
			{marker: markerResearcher, response: runDossierJSON(urls)},
			{marker: markerPlanner, response: runPlanJSON()},
		},
		fallback: `{}`,
	}
	h := &runHarness{
		llm:         srv,
		articles:    articles,
		articleURLs: urls,
		workspace:   t.TempDir(),
	}
	h.llmSrv = httptest.NewServer(srv)
	t.Cleanup(h.llmSrv.Close)
	return h
}

// setThreadResponses makes the fake LLM return a thread artifact from the
// writer (matching --format thread).
func (h *runHarness) setThreadResponses() {
	h.llm.setRules(
		llmResponseRule{marker: markerFinalChecker, response: runCheckJSON("pass")},
		llmResponseRule{marker: markerWriter, response: runArtifactJSON(contracts.ArtifactTypeThread)},
		llmResponseRule{marker: markerVerifier, response: runVerificationJSON(contracts.VerificationStatusSupported)},
		llmResponseRule{marker: markerResearcher, response: runDossierJSON(h.articleURLs)},
		llmResponseRule{marker: markerPlanner, response: runPlanJSON()},
	)
}

// setCheckVerdict swaps the final-check verdict of the fake LLM (for resume
// tests: fail first, then pass).
func (h *runHarness) setCheckVerdict(verdict string) {
	h.llm.setRules(
		llmResponseRule{marker: markerFinalChecker, response: runCheckJSON(verdict)},
		llmResponseRule{marker: markerWriter, response: runArtifactJSON(contracts.ArtifactTypeArticle)},
		llmResponseRule{marker: markerVerifier, response: runVerificationJSON(contracts.VerificationStatusSupported)},
		llmResponseRule{marker: markerResearcher, response: runDossierJSON(h.articleURLs)},
		llmResponseRule{marker: markerPlanner, response: runPlanJSON()},
	)
}

// baseArgs are the flags every run test shares.
func (h *runHarness) baseArgs() []string {
	return []string{
		"run",
		"--topic", "Miami Dolphins rookie",
		"--format", "article",
		"--llm-base-url", h.llmSrv.URL,
		"--llm-api-key", "test-key",
		"--llm-model", "test-model",
		"--workspace-root", h.workspace,
	}
}

// findRunDir locates the single run directory under the workspace root.
func (h *runHarness) findRunDir(t *testing.T) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(h.workspace, "runs", "*", "manifest.json"))
	if err != nil {
		t.Fatalf("globbing runs: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly one run, found %d: %v", len(matches), matches)
	}
	return filepath.Dir(matches[0])
}

// loadManifest reads and unmarshals the run manifest on disk.
func (h *runHarness) loadManifest(t *testing.T) *run.Manifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(h.findRunDir(t), "manifest.json"))
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	m, err := run.UnmarshalManifest(data)
	if err != nil {
		t.Fatalf("unmarshaling manifest: %v", err)
	}
	return m
}

func fileExistsPath(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// =============================================================================
// Tests
// =============================================================================

// TestRunExplicitArticle runs the full command over explicit sources with a
// passing final check and asserts the durable record and the rendered output.
func TestRunExplicitArticle(t *testing.T) {
	setHermeticEnv(t)
	h := newRunHarness(t, "pass")

	args := append(h.baseArgs(),
		"--url", h.articleURLs[0],
		"--url", h.articleURLs[1],
	)
	if err := runCommand(t, args...); err != nil {
		t.Fatalf("run command should succeed: %v", err)
	}

	m := h.loadManifest(t)
	if m.Status != run.RunStatusSucceeded {
		t.Fatalf("manifest status = %s, want succeeded", m.Status)
	}
	if m.SourceMode != run.SourceModeExplicit {
		t.Errorf("source mode = %s, want explicit", m.SourceMode)
	}

	runDir := h.findRunDir(t)
	// Every stage persisted its artifacts.
	for _, rel := range []string{
		"sources/index.json", "fetch/index.json", "documents/index.json",
		"research/dossier.json", "verify/verification.json",
		"write/artifact.json", "output/article.md",
	} {
		if !fileExistsPath(filepath.Join(runDir, rel)) {
			t.Errorf("missing persisted artifact %s", rel)
		}
	}
	// The final check passed, so its record is on disk and in the manifest.
	if !fileExistsPath(filepath.Join(runDir, "final_check/result.json")) {
		t.Error("missing final check record")
	}
	found := false
	for _, a := range m.Artifacts {
		if a.Kind == run.ArtifactKindFactualCheck {
			found = true
		}
	}
	if !found {
		t.Error("final check artifact not recorded on the manifest")
	}
	// The rendered output contains the story.
	data, err := os.ReadFile(filepath.Join(runDir, "output", "article.md"))
	if err != nil {
		t.Fatalf("reading rendered article: %v", err)
	}
	if !strings.Contains(string(data), "Dolphins sign rookie wide receiver") {
		t.Errorf("rendered article missing the story title:\n%s", string(data))
	}
}

// TestRunFinalCheckFailFailsRun: a failed final check fails the run with a
// clear error and produces no rendered output, but keeps the check record.
func TestRunFinalCheckFailFailsRun(t *testing.T) {
	setHermeticEnv(t)
	h := newRunHarness(t, "fail")

	args := append(h.baseArgs(),
		"--url", h.articleURLs[0],
		"--url", h.articleURLs[1],
	)
	err := runCommand(t, args...)
	if err == nil {
		t.Fatal("run command should fail when the final check fails")
	}
	if !strings.Contains(err.Error(), "failed") {
		t.Errorf("error should say the run failed: %v", err)
	}

	m := h.loadManifest(t)
	if m.Status != run.RunStatusFailed {
		t.Fatalf("manifest status = %s, want failed", m.Status)
	}
	runDir := h.findRunDir(t)
	if fileExistsPath(filepath.Join(runDir, "output", "article.md")) {
		t.Error("no rendered output may exist after a failed final check")
	}
	if !fileExistsPath(filepath.Join(runDir, "final_check", "result.json")) {
		t.Error("the failed check record should be kept on disk")
	}
}

// TestRunResumeAfterFailedCheck: the run failed at the final check; flipping
// the check verdict and resuming completes the run and renders the output.
func TestRunResumeAfterFailedCheck(t *testing.T) {
	setHermeticEnv(t)
	h := newRunHarness(t, "fail")

	args := append(h.baseArgs(),
		"--url", h.articleURLs[0],
		"--url", h.articleURLs[1],
	)
	if err := runCommand(t, args...); err == nil {
		t.Fatal("first run should fail (check verdict: fail)")
	}
	m := h.loadManifest(t)
	if m.Status != run.RunStatusFailed {
		t.Fatalf("first run status = %s, want failed", m.Status)
	}
	id := m.ID

	// Flip the check verdict to pass and resume.
	h.setCheckVerdict("pass")
	err := runCommand(t, "run", "--resume", string(id),
		"--llm-base-url", h.llmSrv.URL,
		"--llm-api-key", "test-key",
		"--llm-model", "test-model",
		"--workspace-root", h.workspace,
	)
	if err != nil {
		t.Fatalf("resume should succeed: %v", err)
	}
	m = h.loadManifest(t)
	if m.Status != run.RunStatusSucceeded {
		t.Fatalf("resumed run status = %s, want succeeded", m.Status)
	}
	if !fileExistsPath(filepath.Join(h.findRunDir(t), "output", "article.md")) {
		t.Error("resumed run must produce rendered output")
	}
}

// TestRunDiscovered runs the full command in discovered mode: the fake
// SearXNG provides the candidate URLs, and the rest of the pipeline is real.
func TestRunDiscovered(t *testing.T) {
	setHermeticEnv(t)
	h := newRunHarness(t, "pass")

	searxng := newFakeSearxngServer(t, map[string][]fakeSearxngResult{
		"Miami": {
			{url: h.articleURLs[0], title: "Alpha", snippet: "s1", rank: 1},
			{url: h.articleURLs[1], title: "Beta", snippet: "s2", rank: 2},
		},
	})

	args := append(h.baseArgs(), "--search-base-url", searxng.URL)
	if err := runCommand(t, args...); err != nil {
		t.Fatalf("discovered run should succeed: %v", err)
	}
	m := h.loadManifest(t)
	if m.Status != run.RunStatusSucceeded {
		t.Fatalf("manifest status = %s, want succeeded", m.Status)
	}
	if m.SourceMode != run.SourceModeDiscovered {
		t.Errorf("source mode = %s, want discovered", m.SourceMode)
	}
	if !fileExistsPath(filepath.Join(h.findRunDir(t), "output", "article.md")) {
		t.Error("discovered run must produce rendered output")
	}
}

// TestRunRequiresSources: without URLs and without a search endpoint the
// command fails with a clear configuration error before creating anything.
func TestRunRequiresSources(t *testing.T) {
	setHermeticEnv(t)
	h := newRunHarness(t, "pass")
	err := runCommand(t, h.baseArgs()...)
	if err == nil {
		t.Fatal("run without sources should fail")
	}
	if !strings.Contains(err.Error(), "no sources") {
		t.Errorf("error should explain the missing sources: %v", err)
	}
	// Nothing was created.
	matches, _ := filepath.Glob(filepath.Join(h.workspace, "runs", "*", "manifest.json"))
	if len(matches) != 0 {
		t.Errorf("no run may be created for an invalid configuration: %v", matches)
	}
}

// TestRunRequiresTopicOrResume: no topic and no resume is a usage error.
func TestRunRequiresTopicOrResume(t *testing.T) {
	setHermeticEnv(t)
	err := runCommand(t, "run")
	if err == nil {
		t.Fatal("run without a topic should fail")
	}
	if !strings.Contains(err.Error(), "topic") {
		t.Errorf("error should mention the missing topic: %v", err)
	}
}

// TestRunResumeUnknownID: resuming a nonexistent run fails cleanly.
func TestRunResumeUnknownID(t *testing.T) {
	setHermeticEnv(t)
	h := newRunHarness(t, "pass")
	err := runCommand(t, "run", "--resume", "does-not-exist",
		"--llm-base-url", h.llmSrv.URL, "--llm-api-key", "test-key",
		"--workspace-root", h.workspace,
	)
	if err == nil {
		t.Fatal("resuming an unknown run should fail")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("error should name the run: %v", err)
	}
}

// TestRunProfileRequiresRoot: a profile without a profiles root is a clean
// configuration error.
func TestRunProfileRequiresRoot(t *testing.T) {
	setHermeticEnv(t)
	h := newRunHarness(t, "pass")
	err := runCommand(t,
		append(h.baseArgs(), "--url", h.articleURLs[0], "--profile", "mundodolphins/javi")...,
	)
	if err == nil {
		t.Fatal("a profile without a profiles root should fail")
	}
	if !strings.Contains(err.Error(), "profiles root") {
		t.Errorf("error should point at the missing profiles root: %v", err)
	}
}

// TestRunThreadFormat renders a thread artifact for --format thread.
func TestRunThreadFormat(t *testing.T) {
	setHermeticEnv(t)
	h := newRunHarness(t, "pass")

	args := append(h.baseArgs(),
		"--url", h.articleURLs[0],
		"--format", "thread",
	)
	// The writer must emit a thread artifact for a thread run.
	h.setThreadResponses()

	if err := runCommand(t, args...); err != nil {
		t.Fatalf("thread run should succeed: %v", err)
	}
	m := h.loadManifest(t)
	if m.Status != run.RunStatusSucceeded {
		t.Fatalf("manifest status = %s, want succeeded", m.Status)
	}
	runDir := h.findRunDir(t)
	if !fileExistsPath(filepath.Join(runDir, "output", "thread.txt")) {
		t.Error("thread output missing")
	}
	if fileExistsPath(filepath.Join(runDir, "output", "article.md")) {
		t.Error("article output must not exist for a thread run")
	}
}
