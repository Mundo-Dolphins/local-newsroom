package tests

// v0.5 real-binary end-to-end tests (issue #97).
//
// These tests build the actual `newsroom` binary and drive it as an external
// process against fully local fakes (OpenAI-compatible LLM + fixture article
// server). They are the external-process complement to the in-process
// scenario suite in cmd/newsroom/run_e2e_v05_test.go: together they prove
// the complete chain
//
//	config -> create run -> discovery/fetch -> research -> verify -> write ->
//	final check -> render -> persist artifacts -> review bundle -> reload ->
//	resume/retry
//
// runs for a real operator — including the two lifecycle paths that only
// make sense across a process boundary:
//
//  - `newsroom run` completing successfully offline (exit 0, artifacts on
//    disk, review bundle printed), and
//  - an interrupted run (SIGINT while a stage is in flight) being resumed
//    with `newsroom run --resume <id>` and completing.
//
// No real oMLX, SearXNG, or web access happens anywhere in this file.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/researcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
)

// ---------------------------------------------------------------------------
// Binary build (once per test binary)
// ---------------------------------------------------------------------------

var (
	binOnce sync.Once
	binPath string
	binErr  error
	binDir  string // temp dir holding the binary; left in place for later tests
)

// buildBinary builds ./cmd/newsroom once and returns the path to the binary.
func buildBinary(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		root := cmdPath()
		if root == "" {
			binErr = fmt.Errorf("module root unavailable")
			return
		}
		dir, err := os.MkdirTemp("", "newsroom-v05-bin-")
		if err != nil {
			binErr = err
			return
		}
		binDir = dir
		binPath = filepath.Join(dir, productName)
		cmd := exec.Command("go", "build", "-o", binPath, "./cmd/newsroom")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			binErr = fmt.Errorf("go build: %v: %s", err, out)
			return
		}
	})
	if binErr != nil {
		t.Fatalf("building the newsroom binary: %v", binErr)
	}
	return binPath
}

// ---------------------------------------------------------------------------
// Fakes (self-contained; the same design as the cmd/newsroom suite)
// ---------------------------------------------------------------------------

const (
	bTopic     = "City budget approval and renewable energy growth"
	bClaimOne  = "The city council approved the annual budget on January 10, 2024."
	bClaimTwo  = "Local renewable energy capacity grew 15 percent in 2023."
	bBaseTime  = "2024-01-15T12:00:00Z"
	bFakeModel = "bin-fake-model"

	bMarkerPlanner  = "You are a search query planner"
	bMarkerResearch = "You are a research analyst"
	bMarkerVerify   = "You are a verification analyst"
	bMarkerWrite    = "You are the Writer stage"
	bMarkerCheck    = "You are the Final Factual Checker"
)

// bFakeLLM is a marker-routed fake OpenAI-compatible /chat/completions
// endpoint with failure and hang simulation (for cross-process SIGINT).
type bFakeLLM struct {
	mu         sync.Mutex
	calls      map[string]int
	responses  map[string]string
	failures   map[string]int
	hangMarker string
	release    chan struct{}
	closed     bool
}

func newBFakeLLM(t *testing.T) *bFakeLLM {
	f := &bFakeLLM{
		calls:     map[string]int{},
		responses: map[string]string{},
		failures:  map[string]int{},
		release:   make(chan struct{}),
	}
	t.Cleanup(func() {
		f.mu.Lock()
		if !f.closed {
			f.closed = true
			close(f.release)
		}
		f.mu.Unlock()
	})
	return f
}

func (f *bFakeLLM) SetResponse(marker, response string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses[marker] = response
}

func (f *bFakeLLM) SetFailures(marker string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[marker] = n
}

func (f *bFakeLLM) SetHang(marker string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hangMarker = marker
}

func (f *bFakeLLM) Release() {
	f.mu.Lock()
	if !f.closed {
		f.closed = true
		close(f.release)
	}
	f.hangMarker = ""
	f.mu.Unlock()
}

func (f *bFakeLLM) CallCount(marker string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[marker]
}

func (f *bFakeLLM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":{"message":"unreadable body","type":"fake"}}`, http.StatusBadRequest)
		return
	}
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, `{"error":{"message":"invalid request body","type":"fake"}}`, http.StatusBadRequest)
		return
	}
	marker := "unrecognized"
	for _, m := range req.Messages {
		if m.Role != "system" {
			continue
		}
		switch {
		case strings.Contains(m.Content, bMarkerPlanner):
			marker = bMarkerPlanner
		case strings.Contains(m.Content, bMarkerResearch):
			marker = bMarkerResearch
		case strings.Contains(m.Content, bMarkerVerify):
			marker = bMarkerVerify
		case strings.Contains(m.Content, bMarkerWrite):
			marker = bMarkerWrite
		case strings.Contains(m.Content, bMarkerCheck):
			marker = bMarkerCheck
		}
	}

	f.mu.Lock()
	f.calls[marker]++
	fail := f.failures[marker]
	if fail > 0 {
		f.failures[marker] = fail - 1
	}
	hanging := marker == f.hangMarker
	resp, ok := f.responses[marker]
	f.mu.Unlock()

	if fail > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"fake LLM failure","type":"fake"}}`))
		return
	}
	if hanging {
		<-f.release
	}
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"no canned response","type":"fake"}}`))
		return
	}
	out, _ := json.Marshal(map[string]interface{}{
		"id":      "chatcmpl-v05-bin",
		"object":  "chat.completion",
		"created": 1700000000,
		"model":   bFakeModel,
		"choices": []map[string]interface{}{
			{"index": 0, "message": map[string]string{"role": "assistant", "content": resp}, "finish_reason": "stop"},
		},
		"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30},
	})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

// bCanned loads the fake with successful responses for the whole pipeline.
func (f *bFakeLLM) CannedFor(urls []string) {
	now, _ := time.Parse(time.RFC3339, bBaseTime)

	plan := struct {
		OriginalTopic string `json:"original_topic"`
		Queries       []struct {
			Query   string `json:"query"`
			Purpose string `json:"purpose"`
		} `json:"queries"`
	}{OriginalTopic: bTopic}
	plan.Queries = append(plan.Queries, struct {
		Query   string `json:"query"`
		Purpose string `json:"purpose"`
	}{Query: bTopic, Purpose: "find coverage"})
	pj, _ := json.Marshal(plan)
	f.SetResponse(bMarkerPlanner, string(pj))

	sources := make([]researcher.SourceReference, len(urls))
	for i, u := range urls {
		sources[i] = researcher.SourceReference{
			StableID: u, OriginalURL: u, SourceType: types.SourceTypeWeb,
			RetrievedAt: now, Title: "Fixture Article " + strconv.Itoa(i+1),
		}
	}
	dossier := researcher.ResearchDossier{
		StableID: "b-dossier-1", Topic: bTopic, GeneratedAt: now, Sources: sources,
		Claims: []researcher.Claim{
			{ID: "claim-1", Statement: bClaimOne, Confidence: researcher.ConfidenceHigh,
				Evidence: []researcher.Evidence{{SourceID: urls[0], Excerpts: []researcher.Excerpt{{Text: bClaimOne}}}}},
			{ID: "claim-2", Statement: bClaimTwo, Confidence: researcher.ConfidenceHigh,
				Evidence: []researcher.Evidence{{SourceID: urls[1], Excerpts: []researcher.Excerpt{{Text: bClaimTwo}}}}},
		},
	}
	dj, _ := json.Marshal(dossier)
	f.SetResponse(bMarkerResearch, string(dj))

	verification := contracts.VerificationResult{
		StableID: "b-verification-1", InputDossierID: "b-dossier-1", VerifiedAt: now,
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-1": contracts.VerificationStatusSupported,
			"claim-2": contracts.VerificationStatusSupported,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-1": {ClaimID: "claim-1", Statement: bClaimOne,
				VerificationStatus: contracts.VerificationStatusSupported,
				ConfidenceLevel:    contracts.ConfidenceHigh, VerificationNotes: "supported"},
			"claim-2": {ClaimID: "claim-2", Statement: bClaimTwo,
				VerificationStatus: contracts.VerificationStatusSupported,
				ConfidenceLevel:    contracts.ConfidenceHigh, VerificationNotes: "supported"},
		},
		Contradictions:      []contracts.VerifiedContradiction{},
		VerificationSummary: "Both claims supported.",
		QualityScore:        90.0,
	}
	vj, _ := json.Marshal(verification)
	f.SetResponse(bMarkerVerify, string(vj))

	artifact := contracts.EditorialArtifact{
		StableID: "b-artifact-1", ArtifactType: contracts.ArtifactTypeArticle,
		Title:    "Council Approves Budget as Renewable Capacity Grows",
		Subtitle: "Budget and energy milestones land in the same week",
		Body:     bClaimOne + " " + bClaimTwo,
		Sections: []contracts.Section{
			{Order: 0, Title: "The budget", Body: bClaimOne, ClaimIDs: []string{"claim-1"}},
		},
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {ClaimID: "claim-1", UsageType: contracts.ClaimUsageCore},
			"claim-2": {ClaimID: "claim-2", UsageType: contracts.ClaimUsageCore},
		},
		SourceReferences: map[string]contracts.SourceUsage{},
		GenerationMetadata: contracts.GenerationMetadata{
			InputVerificationID: "b-verification-1", GeneratedAt: now,
		},
	}
	aj, _ := json.Marshal(artifact)
	f.SetResponse(bMarkerWrite, string(aj))

	check := contracts.FactualCheckResult{
		StableID: "b-check-1", InputArtifactID: "b-artifact-1", CheckedAt: now,
		ClaimIDs: []string{"claim-1", "claim-2"},
		ClaimVerificationStatus: map[string]contracts.VerificationStatus{
			"claim-1": contracts.VerificationStatusSupported,
			"claim-2": contracts.VerificationStatusSupported,
		},
		OverallStatus:   contracts.FactualCheckStatusPass,
		ConfidenceLevel: contracts.ConfidenceHigh,
		Summary:         "no factual issues found",
	}
	cj, _ := json.Marshal(check)
	f.SetResponse(bMarkerCheck, string(cj))
}

// ---------------------------------------------------------------------------
// Process execution helpers
// ---------------------------------------------------------------------------

// bEnv is the external environment one binary scenario runs against.
type bEnv struct {
	t           *testing.T
	binary      string
	workDir     string // child CWD: isolated, empty (no config file lookup hits)
	wsRoot      string
	llm         *bFakeLLM
	llmURL      string
	articleURLs []string
}

// newBEnv builds the scenario environment (fakes + isolated child env).
func newBEnv(t *testing.T) *bEnv {
	t.Helper()
	e := &bEnv{t: t, binary: buildBinary(t)}
	e.workDir = t.TempDir() // child CWD: no config file, no workspace in it
	e.wsRoot = t.TempDir()

	e.llm = newBFakeLLM(t)
	llmSrv := httptest.NewServer(e.llm)
	t.Cleanup(llmSrv.Close)
	e.llmURL = llmSrv.URL

	// Fake article server: serves the fixture HTML by path.
	articles := map[string]string{
		"article1": `<html><head><title>City Council Approves 2.4 Billion Budget</title></head><body><article><h1>City Council Approves 2.4 Billion Budget</h1><p>` + bClaimOne + `</p><p>The approved budget totals 2.4 billion dollars.</p></article></body></html>`,
		"article2": `<html><head><title>Local Renewable Energy Capacity Grew 15 Percent</title></head><body><article><h1>Local Renewable Energy Capacity Grew 15 Percent</h1><p>` + bClaimTwo + `</p><p>Solar installations accounted for most of the growth.</p></article></body></html>`,
	}
	articleSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := articles[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(articleSrv.Close)
	for _, p := range []string{"article1", "article2"} {
		e.articleURLs = append(e.articleURLs, articleSrv.URL+"/"+p)
	}
	e.llm.CannedFor(e.articleURLs)
	return e
}

// childEnv builds the environment for the child process: hermetic HOME, no
// newsroom config, no credentials.
func (e *bEnv) childEnv() []string {
	home := e.t.TempDir()
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"TMPDIR=" + os.TempDir(),
		"USER=" + os.Getenv("USER"),
		"SHELL=" + os.Getenv("SHELL"),
	}
}

// runBinary executes the built binary with the given arguments and returns
// its combined output and exit status.
func (e *bEnv) runBinary(args ...string) (string, error) {
	e.t.Helper()
	cmd := exec.Command(e.binary, args...)
	cmd.Dir = e.workDir
	cmd.Env = e.childEnv()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runArgs builds `run` arguments for a fresh run.
func (e *bEnv) runArgs() []string {
	args := []string{"run", "--topic", bTopic, "--workspace-root", e.wsRoot,
		"--llm-base-url", e.llmURL, "--llm-model", bFakeModel, "--format", "article"}
	for _, u := range e.articleURLs {
		args = append(args, "--url", u)
	}
	return args
}

// resumeArgs builds `run --resume <id>` arguments.
func (e *bEnv) resumeArgs(runID string) []string {
	args := []string{"run", "--resume", runID, "--workspace-root", e.wsRoot,
		"--llm-base-url", e.llmURL, "--llm-model", bFakeModel, "--format", "article"}
	return args
}

// bRunDir returns the directory of one run inside the workspace.
func (e *bEnv) runDir(runID string) string {
	return filepath.Join(e.wsRoot, "runs", runID)
}

// bWaitRunDir polls until at least one run directory exists.
func (e *bEnv) waitRunDir() string {
	e.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(filepath.Join(e.wsRoot, "runs"))
		if err == nil && len(entries) > 0 {
			return entries[0].Name()
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatalf("timed out waiting for a run directory in %s", e.wsRoot)
	return ""
}

// bWaitStage polls the persisted manifest until a stage reaches a status.
func (e *bEnv) waitStage(runID string, name run.StageName, want run.StageStatus) *run.Manifest {
	e.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(filepath.Join(e.runDir(runID), "manifest.json"))
		if err == nil {
			var m run.Manifest
			if json.Unmarshal(data, &m) == nil {
				for i := range m.Stages {
					if m.Stages[i].Name == name && m.Stages[i].Status == want {
						return &m
					}
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("timed out waiting for stage %s to reach %s", name, want)
	return nil
}

func bLoadManifest(t *testing.T, wsRoot, runID string) *run.Manifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(wsRoot, "runs", runID, "manifest.json"))
	if err != nil {
		t.Fatalf("reading manifest %s: %v", runID, err)
	}
	var m run.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parsing manifest %s: %v", runID, err)
	}
	return &m
}

// ---------------------------------------------------------------------------
// Scenario 1: a full offline run of the real binary succeeds
// ---------------------------------------------------------------------------

// TestE2E_v05_BinaryRunCompletesOffline runs the real binary through the
// complete chain (explicit sources) and asserts on the durable results:
// exit 0, succeeded manifest, rendered output, and the review bundle.
func TestE2E_v05_BinaryRunCompletesOffline(t *testing.T) {
	e := newBEnv(t)

	out, err := e.runBinary(e.runArgs()...)
	if err != nil {
		t.Fatalf("newsroom run exited with an error: %v\noutput:\n%s", err, out)
	}

	runID := e.waitRunDir()
	m := bLoadManifest(t, e.wsRoot, runID)
	if m.Status != run.RunStatusSucceeded {
		t.Fatalf("manifest status = %s, want succeeded", m.Status)
	}

	// The rendered article and the review bundle exist on disk.
	article := mustReadFile(t, filepath.Join(e.runDir(runID), "output", "article.md"))
	if !strings.Contains(article, strings.TrimSuffix(bClaimOne, ".")) {
		t.Errorf("article output missing the verified claim:\n%s", article)
	}
	bundle := mustReadFile(t, filepath.Join(e.runDir(runID), "review.md"))
	if !strings.Contains(bundle, "Final factual check") {
		t.Errorf("review bundle missing the final check section:\n%s", bundle)
	}

	// The operator-facing summary is printed and names the output.
	if !strings.Contains(out, "article.md") {
		t.Errorf("run summary does not mention the article output:\n%s", out)
	}
	if !strings.Contains(out, "succeeded") {
		t.Errorf("run summary does not report success:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// Scenario 2: an interrupted binary run resumes and completes
// ---------------------------------------------------------------------------

// TestE2E_v05_BinaryInterruptAndResume starts the real binary, lets the
// research stage go in flight (fake LLM hangs), sends SIGINT to the child
// process, asserts the cancellation on disk, then resumes the run in a new
// process and asserts it completes, re-attempting only the unfinished stages.
func TestE2E_v05_BinaryInterruptAndResume(t *testing.T) {
	if testing.Short() {
		t.Skip("interrupt test sends a real signal to the child process")
	}
	e := newBEnv(t)
	e.llm.SetHang(bMarkerResearch)

	cmd := exec.Command(e.binary, e.runArgs()...)
	cmd.Dir = e.workDir
	cmd.Env = e.childEnv()
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the binary: %v", err)
	}

	// Wait for the run to be in flight at the research stage, then
	// interrupt the child process the way Ctrl+C would.
	runID := e.waitRunDir()
	e.waitStage(runID, run.StageResearch, run.StageStatusRunning)
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("sending SIGINT to the child: %v", err)
	}
	doneCh := make(chan error, 1)
	go func() { doneCh <- cmd.Wait() }()
	select {
	case waitErr := <-doneCh:
		if waitErr == nil {
			t.Log("run exited 0 after SIGINT (graceful); continuing")
		} else {
			t.Logf("run exited non-zero after SIGINT, as expected: %v", waitErr)
		}
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("child process did not exit after SIGINT within 60s")
	}
	e.llm.Release()

	// The persisted manifest records the cancellation precisely.
	m := bLoadManifest(t, e.wsRoot, runID)
	if m.Status != run.RunStatusCancelled {
		t.Fatalf("manifest status = %s, want cancelled", m.Status)
	}
	research, _ := bStageStatus(t, m, run.StageResearch)
	if research != run.StageStatusCancelled {
		t.Errorf("research stage status = %s, want cancelled", research)
	}
	// Resume in a fresh process: healthy LLM, same flags.
	resumeOut, err := e.runBinary(e.resumeArgs(runID)...)
	if err != nil {
		t.Fatalf("newsroom run --resume failed: %v\noutput:\n%s", err, resumeOut)
	}
	m = bLoadManifest(t, e.wsRoot, runID)
	if m.Status != run.RunStatusSucceeded {
		t.Fatalf("resumed manifest status = %s, want succeeded", m.Status)
	}
	if research, attempts := bStageStatus(t, m, run.StageResearch); research != run.StageStatusSucceeded || attempts != 2 {
		t.Errorf("research after resume: status=%s attempts=%d, want succeeded/2", research, attempts)
	}
	if _, attempts := bStageStatus(t, m, run.StageDiscovery); attempts != 1 {
		t.Errorf("discovery attempts = %d after resume, want 1 (reused, not re-run)", attempts)
	}
	article := mustReadFile(t, filepath.Join(e.runDir(runID), "output", "article.md"))
	if !strings.Contains(article, strings.TrimSuffix(bClaimOne, ".")) {
		t.Errorf("resumed run article missing the verified claim:\n%s", article)
	}
	if !strings.Contains(resumeOut, "succeeded") {
		t.Errorf("resume summary does not report success:\n%s", resumeOut)
	}
}

// bStageStatus returns the status and attempt count of a stage.
func bStageStatus(t *testing.T, m *run.Manifest, name run.StageName) (run.StageStatus, int) {
	t.Helper()
	for i := range m.Stages {
		if m.Stages[i].Name == name {
			return m.Stages[i].Status, len(m.Stages[i].Attempts)
		}
	}
	t.Fatalf("stage %q not found in manifest %s", name, m.ID)
	return "", 0
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}
