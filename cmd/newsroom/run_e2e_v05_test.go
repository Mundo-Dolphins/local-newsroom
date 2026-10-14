package main

// v0.5 end-to-end tests (issue #97) — the first genuinely usable workflow,
// verified fully offline.
//
// These tests drive the real `newsroom run` command end to end through the
// complete chain:
//
//	config -> create run -> discovery/fetch -> research -> verify ->
//	write -> final check -> render -> persist artifacts -> review bundle ->
//	reload run -> resume/retry
//
// Every external dependency is faked locally:
//
//   - LLM: a marker-routed fake OpenAI-compatible /chat/completions server
//     (planner, researcher, verifier, writer, final checker).
//   - Search: a fake SearXNG endpoint returning canned results.
//   - Web: a local HTTP server serving fixture articles.
//   - Workspace: a temp directory per test.
//
// No real oMLX, SearXNG, or web access happens anywhere in this file.
//
// Scenarios covered:
//
//  1. Successful article run (explicit sources + profile)          [Test 1]
//  2. Successful Bluesky thread run (discovery via fake SearXNG)   [Test 2]
//  3. Final factual check failure fails the run                    [Test 3]
//  4. Interrupted run (SIGINT) resumes from where it stopped       [Test 4]
//  5. Failed stage re-attempts on resume, upstream stages reused   [Test 5]
//  6. Configuration precedence: CLI flag > env > file > default    [Test 6]
//  7. No secret values are ever persisted in the run workspace     [Test 7]
//  8. Persisted artifacts survive a workspace reload               [Test 8]
//
// The v0.1-v0.4 commands (research, verify, write, final-check, archive,
// config, review, TUI) remain covered by the pre-existing test files.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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
	"github.com/Mundo-Dolphins/local-newsroom/internal/search"
	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workspace"
)

// ---------------------------------------------------------------------------
// Fixture content
// ---------------------------------------------------------------------------

const (
	v05Topic     = "City budget approval and renewable energy growth"
	v05ClaimOne  = "The city council approved the annual budget on January 10, 2024."
	v05ClaimTwo  = "Local renewable energy capacity grew 15 percent in 2023."
	v05BaseTime  = "2024-01-15T12:00:00Z"
	v05FakeModel = "e2e-fake-model"
)

// v05Article1 is the first local article served to the fetch stage.
var v05Article1 = `<!DOCTYPE html>
<html lang="en">
<head><meta charset="utf-8"><title>City Council Approves 2.4 Billion Budget</title></head>
<body>
<article>
  <h1>City Council Approves 2.4 Billion Budget</h1>
  <p>The city council approved the annual budget on January 10, 2024.</p>
  <p>The approved budget totals 2.4 billion dollars.</p>
  <p>Officials said the budget prioritizes infrastructure and schools.</p>
</article>
</body>
</html>`

// v05Article2 is the second local article.
var v05Article2 = `<!DOCTYPE html>
<html lang="en">
<head><meta charset="utf-8"><title>Local Renewable Energy Capacity Grew 15 Percent</title></head>
<body>
<article>
  <h1>Local Renewable Energy Capacity Grew 15 Percent in 2023</h1>
  <p>Local renewable energy capacity grew 15 percent in 2023.</p>
  <p>Solar installations accounted for most of the growth.</p>
</article>
</body>
</html>`

// ---------------------------------------------------------------------------
// Fake LLM (marker-routed, with failure and hang simulation)
// ---------------------------------------------------------------------------

// Stable markers: one per system prompt, chosen so that each of the five
// LLM-backed stages is unambiguously identifiable from the request.
const (
	v05MarkerPlanner  = "You are a search query planner"
	v05MarkerResearch = "You are a research analyst"
	v05MarkerVerify   = "You are a verification analyst"
	v05MarkerWrite    = "You are the Writer stage"
	v05MarkerCheck    = "You are the Final Factual Checker"
)

// v05fakeLLM is a fake OpenAI-compatible chat completions endpoint.
//
// Requests are classified by the marker in their system prompt, so one
// server answers all five LLM-backed pipeline stages. It records every call
// (for attempt assertions) and can be reconfigured between runs of the same
// test (resume scenarios) via the Set* methods.
type v05fakeLLM struct {
	t *testing.T

	mu         sync.Mutex
	calls      map[string]int
	responses  map[string]string
	failures   map[string]int
	hangMarker string
	release    chan struct{}
	closed     bool
}

func newV05FakeLLM(t *testing.T, responses map[string]string) *v05fakeLLM {
	f := &v05fakeLLM{
		t:         t,
		calls:     map[string]int{},
		responses: responses,
		failures:  map[string]int{},
		release:   make(chan struct{}),
	}
	// Ensure the in-flight (hung) request can never block test cleanup.
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

// SetResponse changes the canned response of a stage (between runs).
func (f *v05fakeLLM) SetResponse(marker, response string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses[marker] = response
}

// SetFailures makes the next n calls of a stage return HTTP 500.
func (f *v05fakeLLM) SetFailures(marker string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n < 0 {
		n = 0
	}
	f.failures[marker] = n
}

// SetHang makes calls of the given stage block until Release is called.
// Used by the interrupt test: the signal must land while the stage is
// genuinely in flight.
func (f *v05fakeLLM) SetHang(marker string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hangMarker = marker
}

// Release unblocks a hung request and clears the hang.
func (f *v05fakeLLM) Release() {
	f.mu.Lock()
	if !f.closed {
		f.closed = true
		close(f.release)
	}
	f.hangMarker = ""
	f.mu.Unlock()
}

// CallCount returns how many times a stage was called so far.
func (f *v05fakeLLM) CallCount(marker string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[marker]
}

// classify maps a chat request to the pipeline stage that issued it.
func classifyV05Request(req struct {
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}) string {
	for _, m := range req.Messages {
		if m.Role != "system" {
			continue
		}
		switch {
		case strings.Contains(m.Content, v05MarkerPlanner):
			return v05MarkerPlanner
		case strings.Contains(m.Content, v05MarkerResearch):
			return v05MarkerResearch
		case strings.Contains(m.Content, v05MarkerVerify):
			return v05MarkerVerify
		case strings.Contains(m.Content, v05MarkerWrite):
			return v05MarkerWrite
		case strings.Contains(m.Content, v05MarkerCheck):
			return v05MarkerCheck
		default:
			return "unrecognized"
		}
	}
	return "unrecognized"
}

// ServeHTTP implements http.Handler.
func (f *v05fakeLLM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	marker := classifyV05Request(req)

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
		_, _ = w.Write([]byte(`{"error":{"message":"fake LLM failure (configured by test)","type":"fake"}}`))
		return
	}
	if hanging {
		// Block until the test releases the in-flight request (or the
		// request context is cancelled, which aborts the write below).
		<-f.release
	}
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"fake LLM: no canned response for stage","type":"fake"}}`))
		return
	}

	out, _ := json.Marshal(map[string]interface{}{
		"id":      "chatcmpl-v05-e2e",
		"object":  "chat.completion",
		"created": 1700000000,
		"model":   v05FakeModel,
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"message":       map[string]string{"role": "assistant", "content": resp},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30},
	})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

// ---------------------------------------------------------------------------
// Fake SearXNG
// ---------------------------------------------------------------------------

// v05fakeSearxng is a SearXNG-compatible search endpoint that returns the
// same canned results for every query.
type v05fakeSearxng struct {
	results []search.SearchResult
}

func newV05FakeSearxng(urls []string) *v05fakeSearxng {
	f := &v05fakeSearxng{}
	published := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	for i, u := range urls {
		title := "Fixture " + strconv.Itoa(i+1)
		snippet := "Canned result for " + u
		f.results = append(f.results, search.SearchResult{
			ID:           "v05-result-" + strconv.Itoa(i),
			URL:          u,
			Title:        &title,
			Snippet:      &snippet,
			ProviderName: "v05-fake-searxng",
			Rank:         i,
			PublishedAt:  &published,
			SourceDomain: "127.0.0.1",
		})
	}
	return f
}

func (f *v05fakeSearxng) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	out, _ := json.Marshal(map[string]interface{}{
		"query":             r.URL.Query().Get("q"),
		"results":           f.results,
		"number_of_results": 0,
	})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

// ---------------------------------------------------------------------------
// Canned LLM responses
//
// Built from the real contract types and marshaled, so the canned JSON is
// guaranteed to round-trip through the same parsers the stages use.
// ---------------------------------------------------------------------------

func v05plannerJSON(topic string) string {
	plan := struct {
		OriginalTopic string `json:"original_topic"`
		Queries       []struct {
			Query   string `json:"query"`
			Purpose string `json:"purpose"`
		} `json:"queries"`
	}{OriginalTopic: topic}
	plan.Queries = append(plan.Queries,
		struct {
			Query   string `json:"query"`
			Purpose string `json:"purpose"`
		}{Query: topic + " city council budget", Purpose: "Find budget coverage"},
		struct {
			Query   string `json:"query"`
			Purpose string `json:"purpose"`
		}{Query: topic + " renewable energy", Purpose: "Find energy coverage"},
	)
	b, _ := json.Marshal(plan)
	return string(b)
}

func v05dossierJSON(topic string, urls []string) string {
	now, _ := time.Parse(time.RFC3339, v05BaseTime)
	sources := make([]researcher.SourceReference, len(urls))
	for i, u := range urls {
		sources[i] = researcher.SourceReference{
			StableID:    u,
			OriginalURL: u,
			SourceType:  types.SourceTypeWeb,
			RetrievedAt: now,
			Title:       "Fixture Article " + strconv.Itoa(i+1),
		}
	}
	dossier := researcher.ResearchDossier{
		StableID:    "dossier-v05-001",
		Topic:       topic,
		GeneratedAt: now,
		Sources:     sources,
		Claims: []researcher.Claim{
			{
				ID:         "claim-1",
				Statement:  v05ClaimOne,
				Confidence: researcher.ConfidenceHigh,
				Evidence: []researcher.Evidence{
					{SourceID: urls[0], Excerpts: []researcher.Excerpt{{Text: v05ClaimOne, StartOffset: 0, EndOffset: len(v05ClaimOne)}}},
				},
			},
			{
				ID:         "claim-2",
				Statement:  v05ClaimTwo,
				Confidence: researcher.ConfidenceHigh,
				Evidence: []researcher.Evidence{
					{SourceID: urls[1], Excerpts: []researcher.Excerpt{{Text: v05ClaimTwo, StartOffset: 0, EndOffset: len(v05ClaimTwo)}}},
				},
			},
		},
		Contradictions: []researcher.Contradiction{},
		ResearchNotes:  []researcher.ResearchNote{},
	}
	b, _ := json.Marshal(dossier)
	return string(b)
}

func v05verificationJSON() string {
	now, _ := time.Parse(time.RFC3339, v05BaseTime)
	result := contracts.VerificationResult{
		StableID:       "verification-v05-001",
		InputDossierID: "dossier-v05-001",
		VerifiedAt:     now,
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-1": contracts.VerificationStatusSupported,
			"claim-2": contracts.VerificationStatusSupported,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-1": {
				ClaimID:            "claim-1",
				Statement:          v05ClaimOne,
				VerificationStatus: contracts.VerificationStatusSupported,
				ConfidenceLevel:    contracts.ConfidenceHigh,
				VerificationNotes:  "Both fixture sources agree; the claim is directly supported.",
			},
			"claim-2": {
				ClaimID:            "claim-2",
				Statement:          v05ClaimTwo,
				VerificationStatus: contracts.VerificationStatusSupported,
				ConfidenceLevel:    contracts.ConfidenceHigh,
				VerificationNotes:  "Directly supported by the energy source.",
			},
		},
		Contradictions:      []contracts.VerifiedContradiction{},
		VerificationSummary: "Both claims are directly supported by source evidence.",
		QualityScore:        90.0,
	}
	b, _ := json.Marshal(result)
	return string(b)
}

func v05articleJSON() string {
	now, _ := time.Parse(time.RFC3339, v05BaseTime)
	artifact := contracts.EditorialArtifact{
		StableID:     "artifact-v05-001",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "Council Approves 2.4 Billion Budget as Renewable Capacity Grows 15 Percent",
		Subtitle:     "City budget and local energy milestones land in the same week",
		Body:         v05ClaimOne + " " + v05ClaimTwo + " Solar installations accounted for most of the growth.",
		Sections: []contracts.Section{
			{Order: 0, Title: "The budget", Body: v05ClaimOne, ClaimIDs: []string{"claim-1"}},
		},
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {ClaimID: "claim-1", UsageType: contracts.ClaimUsageCore, Paraphrased: false},
			"claim-2": {ClaimID: "claim-2", UsageType: contracts.ClaimUsageCore, Paraphrased: false},
		},
		SourceReferences: map[string]contracts.SourceUsage{
			"fixture-source-1": {SourceID: "fixture-source-1", UsageCount: 1, CitationText: "City Council Approves 2.4 Billion Budget (fixture)"},
			"fixture-source-2": {SourceID: "fixture-source-2", UsageCount: 1, CitationText: "Local Renewable Energy Capacity Grew 15 Percent (fixture)"},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			InputVerificationID: "verification-v05-001",
			GeneratedAt:         now,
		},
	}
	b, _ := json.Marshal(artifact)
	return string(b)
}

func v05threadJSON() string {
	now, _ := time.Parse(time.RFC3339, v05BaseTime)
	artifact := contracts.EditorialArtifact{
		StableID:     "artifact-v05-001",
		ArtifactType: contracts.ArtifactTypeThread,
		Title:        "Budget + renewables in one week",
		Posts: []contracts.Post{
			{Order: 0, Body: v05ClaimOne + " (1/2)", ClaimIDs: []string{"claim-1"}},
			{Order: 1, Body: v05ClaimTwo + " (2/2)", ClaimIDs: []string{"claim-2"}},
		},
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {ClaimID: "claim-1", UsageType: contracts.ClaimUsageCore, Paraphrased: false},
			"claim-2": {ClaimID: "claim-2", UsageType: contracts.ClaimUsageCore, Paraphrased: false},
		},
		SourceReferences: map[string]contracts.SourceUsage{
			"fixture-source-1": {SourceID: "fixture-source-1", UsageCount: 1, CitationText: "City Council Approves 2.4 Billion Budget (fixture)"},
			"fixture-source-2": {SourceID: "fixture-source-2", UsageCount: 1, CitationText: "Local Renewable Energy Capacity Grew 15 Percent (fixture)"},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			InputVerificationID: "verification-v05-001",
			GeneratedAt:         now,
		},
	}
	b, _ := json.Marshal(artifact)
	return string(b)
}

// v05checkResultJSON builds a final-check response. The final checker
// recomputes the overall status from the parsed findings (high/critical
// severities fail, medium forces a review), so the severity carries the
// intended outcome.
func v05checkResultJSON(status contracts.FactualCheckStatus) string {
	now, _ := time.Parse(time.RFC3339, v05BaseTime)
	result := contracts.FactualCheckResult{
		StableID:        "check-v05-001",
		InputArtifactID: "artifact-v05-001",
		CheckedAt:       now,
		ClaimIDs:        []string{"claim-1", "claim-2"},
		ClaimVerificationStatus: map[string]contracts.VerificationStatus{
			"claim-1": contracts.VerificationStatusSupported,
			"claim-2": contracts.VerificationStatusSupported,
		},
		OverallStatus:   status,
		ConfidenceLevel: contracts.ConfidenceHigh,
		Summary:         "v0.5 e2e final check",
	}
	if status == contracts.FactualCheckStatusFail {
		result.UnsupportedStatements = []contracts.UnsupportedStatement{
			{
				Location:      contracts.StatementLocation{LocationType: contracts.LocationTypeBody, Index: 0},
				Statement:     "The council met on a day no source reports",
				StatementType: contracts.UnsupportedStatementNewFacts,
				Severity:      contracts.WarningSeverityHigh,
				ClaimIDs:      []string{"claim-1"},
			},
		}
		result.FactualFindings = []contracts.FactualFinding{
			{
				FindingID:       "finding-v05-001",
				FindingType:     contracts.FindingTypeError,
				Statement:       "The artifact introduces facts not present in the verification result",
				Severity:        contracts.WarningSeverityHigh,
				Category:        contracts.FindingCategoryClaim,
				RelatedClaimIDs: []string{"claim-1"},
			},
		}
	}
	b, _ := json.Marshal(result)
	return string(b)
}

// ---------------------------------------------------------------------------
// Scenario environment
// ---------------------------------------------------------------------------

// v05envOpts selects what the scenario environment contains.
type v05envOpts struct {
	// articlePaths are the fixture article paths served by the fake article
	// server (their indices pick the fixture content).
	articlePaths []string

	// useSearch adds the fake SearXNG and passes --search-base-url (the
	// planner LLM then discovers the articles).
	useSearch bool

	// format is the --format flag value; empty leaves resolution to the
	// config chain (tests that assert config-driven formats use this).
	format string

	// profile adds a profile root with common/ and authors/javi/ and passes
	// --profile javi --profile-root <root>.
	profile bool

	// llmAPIKey, when non-empty, is exported as OMLX_API_KEY (and may be
	// referenced from the config file — the realistic operator path).
	llmAPIKey string

	// configYAML, when non-empty, is written to <root>/newsroom.yaml and
	// selected via NEWSROOM_CONFIG.
	configYAML string
}

// v05env is the fully local environment of one scenario.
type v05env struct {
	t           *testing.T
	root        string // hermetic CWD (contains prompts/)
	wsRoot      string // workspace root (temp)
	llm         *v05fakeLLM
	llmURL      string
	searxngURL  string
	format      string            // the --format flag value (may be empty)
	urls        []string          // absolute URLs of the fixture articles
	articles    map[string]string // the fake article server's content map
	profileRoot string            // set when opts.profile
	apiKey      string
	configPath  string
	llmModel    string // "" omits the --llm-model flag (config resolves it)
}

// newV05Env sets up the hermetic environment and all fake services.
//
// The fake LLM is preloaded with successful canned responses for every stage
// of the pipeline; tests override or fail individual stages as needed.
func newV05Env(t *testing.T, opts v05envOpts) *v05env {
	t.Helper()
	setHermeticEnv(t) // pins all newsroom env vars, empties HOME, chdirs

	env := &v05env{t: t, root: v05cwd(t), apiKey: opts.llmAPIKey, format: opts.format, llmModel: v05FakeModel}

	// Workspace root: a fresh temp directory outside the hermetic CWD so the
	// CWD-relative config lookup (./.newsroom.yaml) never finds a file.
	env.wsRoot = filepath.Join(t.TempDir(), "newsroom-ws")

	// Fake article server. The map is a reference type, so the handler keeps
	// serving it and tests can mutate entries (e.g. plant a secret).
	articles := map[string]string{}
	for i, p := range opts.articlePaths {
		articles[p] = pickArticle(i)
	}
	env.articles = articles
	articleSrv := newFakeArticleServer(t, articles)
	env.urls = nil
	for _, p := range opts.articlePaths {
		env.urls = append(env.urls, articleSrv.URL+"/"+p)
	}

	// Fake LLM with successful canned responses for the whole pipeline.
	// (The planner response only matters in discovery mode, but loading it
	// always keeps the call accounting uniform.)
	responses := map[string]string{
		v05MarkerPlanner:  v05plannerJSON(v05Topic),
		v05MarkerResearch: v05dossierJSON(v05Topic, env.urls),
		v05MarkerVerify:   v05verificationJSON(),
		v05MarkerWrite:    v05articleJSON(),
		v05MarkerCheck:    v05checkResultJSON(contracts.FactualCheckStatusPass),
	}
	env.llm = newV05FakeLLM(t, responses)
	llmSrv := httptest.NewServer(env.llm)
	t.Cleanup(llmSrv.Close)
	env.llmURL = llmSrv.URL

	// Fake SearXNG (when requested).
	if opts.useSearch {
		searxngSrv := httptest.NewServer(newV05FakeSearxng(env.urls))
		t.Cleanup(searxngSrv.Close)
		env.searxngURL = searxngSrv.URL
	}

	// Editorial profile (when requested): <root>/common/* + <root>/authors/javi/*
	if opts.profile {
		env.profileRoot = filepath.Join(env.root, "profiles")
		mustWriteFile(t, filepath.Join(env.profileRoot, "common", "style.md"),
			"Write with a calm, factual voice. Keep paragraphs short.\n")
		mustWriteFile(t, filepath.Join(env.profileRoot, "common", "facts-policy.md"),
			"Only state facts that a source directly supports.\n")
		mustWriteFile(t, filepath.Join(env.profileRoot, "authors", "javi", "style.md"),
			"Prefer concrete numbers over abstractions.\n")
	}

	// Config file (when requested).
	if opts.configYAML != "" {
		env.configPath = filepath.Join(env.root, "newsroom.yaml")
		mustWriteFile(t, env.configPath, opts.configYAML)
		t.Setenv("NEWSROOM_CONFIG", env.configPath)
	}
	if opts.llmAPIKey != "" {
		t.Setenv("OMLX_API_KEY", opts.llmAPIKey)
	}
	return env
}

// v05cwd returns the current working directory (the hermetic root after
// setHermeticEnv).
func v05cwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return wd
}

// taintArticle rewrites the served article at url so its body contains the
// given token. The fake article server serves from the env's map by
// reference, so the next fetch of that URL returns the tainted HTML.
func (e *v05env) taintArticle(t *testing.T, url, token string) {
	t.Helper()
	path := strings.TrimPrefix(url, articleServerBase(t, url))
	path = strings.TrimPrefix(path, "/")
	original, ok := e.articles[path]
	if !ok {
		t.Fatalf("no served article at path %q", path)
	}
	// Plant the token as an ordinary paragraph in the article body.
	e.articles[path] = strings.Replace(original, "</article>",
		fmt.Sprintf("<p>Internal note: the deployment token is %s and must never be stored.</p>\n</article>", token), 1)
}

// articleServerBase is the server origin of a fixture URL.
func articleServerBase(t *testing.T, u string) string {
	t.Helper()
	i := strings.Index(u, "://")
	if i < 0 {
		return ""
	}
	rest := u[i+3:]
	if j := strings.IndexAny(rest, "/"); j >= 0 {
		return u[:i+3+j]
	}
	return u
}

// ---------------------------------------------------------------------------
// Command argument builders
// ---------------------------------------------------------------------------

// newRunArgs builds the arguments for a new run. dropURLs removes the
// explicit --url flags (for discovery-mode runs); extra appends overrides.
func (e *v05env) newRunArgs(dropURLs bool, extra ...string) []string {
	args := []string{"run", "--topic", v05Topic, "--workspace-root", e.wsRoot}
	args = append(args, "--llm-base-url", e.llmURL)
	if e.llmModel != "" {
		args = append(args, "--llm-model", e.llmModel)
	}
	if !dropURLs {
		for _, u := range e.urls {
			args = append(args, "--url", u)
		}
	}
	if e.searxngURL != "" {
		args = append(args, "--search-base-url", e.searxngURL)
	}
	if e.format != "" {
		args = append(args, "--format", e.format)
	}
	if e.apiKey != "" {
		args = append(args, "--llm-api-key", e.apiKey)
	}
	if e.profileRoot != "" {
		args = append(args, "--profile", "javi", "--profile-root", e.profileRoot)
	}
	return append(args, extra...)
}

// resumeArgs builds the `newsroom run --resume <id> ...` arguments.
func (e *v05env) resumeArgs(runID string) []string {
	args := []string{"run", "--resume", runID, "--workspace-root", e.wsRoot}
	args = append(args, "--llm-base-url", e.llmURL)
	if e.llmModel != "" {
		args = append(args, "--llm-model", e.llmModel)
	}
	if e.searxngURL != "" {
		args = append(args, "--search-base-url", e.searxngURL)
	}
	if e.format != "" {
		args = append(args, "--format", e.format)
	}
	if e.apiKey != "" {
		args = append(args, "--llm-api-key", e.apiKey)
	}
	if e.profileRoot != "" {
		args = append(args, "--profile", "javi", "--profile-root", e.profileRoot)
	}
	return args
}

// ---------------------------------------------------------------------------
// Workspace / manifest inspection helpers
// ---------------------------------------------------------------------------

func mustExistFile(t *testing.T, path string) []byte {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file %s to exist: %v", path, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return data
}

func v05MustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("expected file %s to NOT exist", path)
	}
}

// v05RunDir returns the directory of one run inside the workspace.
func v05RunDir(wsRoot, runID string) string {
	return filepath.Join(wsRoot, "runs", runID)
}

// v05LoadManifest loads the persisted manifest of a run.
func v05LoadManifest(t *testing.T, wsRoot, runID string) *run.Manifest {
	t.Helper()
	store, err := workspace.NewStore(wsRoot)
	if err != nil {
		t.Fatalf("creating workspace store: %v", err)
	}
	m, _, err := store.LoadManifest(run.RunID(runID))
	if err != nil {
		t.Fatalf("loading manifest %s: %v", runID, err)
	}
	return m
}

// v05StageStatus returns the status and attempt count of a stage.
func v05StageStatus(t *testing.T, m *run.Manifest, name run.StageName) (run.StageStatus, int) {
	t.Helper()
	for i := range m.Stages {
		if m.Stages[i].Name == name {
			return m.Stages[i].Status, len(m.Stages[i].Attempts)
		}
	}
	t.Fatalf("stage %q not found in manifest %s", name, m.ID)
	return "", 0
}

// v05FirstRunID returns the single run ID inside a workspace.
func v05FirstRunID(t *testing.T, wsRoot string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(wsRoot, "runs"))
	if err != nil {
		t.Fatalf("listing runs in %s: %v", wsRoot, err)
	}
	if len(entries) == 0 {
		t.Fatalf("no runs found in %s", wsRoot)
	}
	return entries[0].Name()
}

// listRunIDs returns every run ID inside a workspace, in directory order.
func listRunIDs(t *testing.T, wsRoot string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(wsRoot, "runs"))
	if err != nil {
		t.Fatalf("listing runs in %s: %v", wsRoot, err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// v05WaitStage polls the persisted manifest until the given stage reaches the
// wanted status. It is the synchronization point of the interrupt test: the
// signal must only be sent once the stage is provably in flight.
func v05WaitStage(t *testing.T, wsRoot, runID string, name run.StageName, want run.StageStatus) *run.Manifest {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(filepath.Join(v05RunDir(wsRoot, runID), "manifest.json"))
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
	t.Fatalf("timed out waiting for stage %s to reach %s in run %s", name, want, runID)
	return nil
}

// v05WaitRunDir polls until at least one run directory exists (the goroutine
// that starts the run may not have created the workspace layout yet).
func v05WaitRunDir(t *testing.T, wsRoot string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(filepath.Join(wsRoot, "runs"))
		if err == nil && len(entries) > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for a run directory in %s", wsRoot)
}

// v05WalkWorkspaceFiles reads every regular file under root, keyed by the
// path relative to root.
func v05WalkWorkspaceFiles(t *testing.T, root string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		out[strings.TrimPrefix(path, root+string(filepath.Separator))] = data
		return nil
	})
	return out
}

// v05AssertNoSecret asserts that no file in the workspace contains the secret
// value (and returns the number of files inspected, for logging).
func v05AssertNoSecret(t *testing.T, wsRoot, secret string) int {
	t.Helper()
	files := v05WalkWorkspaceFiles(t, wsRoot)
	n := 0
	for path, data := range files {
		n++
		if bytes.Contains(data, []byte(secret)) {
			t.Errorf("secret value found in persisted file %s — the workspace must never contain raw credentials", path)
		}
	}
	return n
}

// v05CountRedactionSidecars counts *.redaction.json sidecars under root.
func v05CountRedactionSidecars(t *testing.T, wsRoot string) int {
	t.Helper()
	n := 0
	_ = filepath.WalkDir(wsRoot, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".redaction.json") {
			n++
		}
		return nil
	})
	return n
}

// pickArticle returns the fixture article for index i.
func pickArticle(i int) string {
	if i%2 == 0 {
		return v05Article1
	}
	return v05Article2
}

// ---------------------------------------------------------------------------
// Test 1: Successful article run (explicit sources + profile)
// ---------------------------------------------------------------------------

// TestE2E_v05_ArticleRunSucceeds drives the complete chain for an explicit
// source-mode article run and asserts every durable artifact.
func TestE2E_v05_ArticleRunSucceeds(t *testing.T) {
	env := newV05Env(t, v05envOpts{
		articlePaths: []string{"article1", "article2"},
		format:       "article",
		profile:      true,
	})

	err := runCommand(t, env.newRunArgs(false)...)
	if err != nil {
		t.Fatalf("newsroom run failed: %v", err)
	}

	runID := v05FirstRunID(t, env.wsRoot)
	m := v05LoadManifest(t, env.wsRoot, runID)

	// The run reached the terminal success state with every canonical stage
	// accounted for: the eight active stages succeeded, follow_up skipped.
	if m.Status != run.RunStatusSucceeded {
		t.Fatalf("run status = %s, want succeeded", m.Status)
	}
	wantStatus := map[run.StageName]run.StageStatus{
		run.StageDiscovery:  run.StageStatusSucceeded,
		run.StageFetch:      run.StageStatusSucceeded,
		run.StageExtract:    run.StageStatusSucceeded,
		run.StageResearch:   run.StageStatusSucceeded,
		run.StageVerify:     run.StageStatusSucceeded,
		run.StageFollowUp:   run.StageStatusSkipped,
		run.StageWrite:      run.StageStatusSucceeded,
		run.StageFinalCheck: run.StageStatusSucceeded,
		run.StageRender:     run.StageStatusSucceeded,
	}
	for name, want := range wantStatus {
		got, _ := v05StageStatus(t, m, name)
		if got != want {
			t.Errorf("stage %s status = %s, want %s", name, got, want)
		}
	}

	// Model metadata records the (fake) endpoint and model used.
	if m.Model.Model != v05FakeModel {
		t.Errorf("model = %q, want %q", m.Model.Model, v05FakeModel)
	}
	if !strings.Contains(m.Model.Endpoint, env.llmURL) {
		t.Errorf("model endpoint %q does not contain the LLM base URL %q", m.Model.Endpoint, env.llmURL)
	}

	// Every LLM-backed stage was exercised exactly once; the planner never
	// runs in explicit mode.
	for _, marker := range []string{v05MarkerResearch, v05MarkerVerify, v05MarkerWrite, v05MarkerCheck} {
		if n := env.llm.CallCount(marker); n != 1 {
			t.Errorf("stage %q was called %d times, want 1", marker, n)
		}
	}
	if n := env.llm.CallCount(v05MarkerPlanner); n != 0 {
		t.Errorf("planner was called %d times in explicit mode, want 0", n)
	}

	// Canonical artifact files exist in the run directory.
	runDir := v05RunDir(env.wsRoot, runID)
	for _, rel := range []string{
		"manifest.json",
		"sources/index.json",
		"fetch/index.json",
		"documents/index.json",
		"research/index.json",
		"research/dossier.json",
		"verify/verification.json",
		"write/artifact.json",
		"final_check/result.json",
		"output/article.md",
		"review.md",
	} {
		mustExistFile(t, filepath.Join(runDir, rel))
	}

	// The rendered article contains the verified content and its source
	// citations.
	article := string(mustExistFile(t, filepath.Join(runDir, "output", "article.md")))
	for _, want := range []string{v05ClaimOne, v05ClaimTwo, "Sources"} {
		if !strings.Contains(article, want) {
			t.Errorf("article output missing %q", want)
		}
	}

	// The profile is recorded on the manifest (id + content hash).
	if m.Profile == nil {
		t.Fatal("profile not recorded on the manifest")
	}
	if m.Profile.ID != "javi" || m.Profile.Hash == "" {
		t.Errorf("profile = %+v, want id javi with a content hash", m.Profile)
	}

	// The run summary printed to stdout names the output file.
	store, err := workspace.NewStore(env.wsRoot)
	if err != nil {
		t.Fatalf("creating store for summary: %v", err)
	}
	sumOut := captureStdout(t, func() {
		printRunSummary(store, m)
	})
	if !strings.Contains(sumOut, "article.md") {
		t.Errorf("run summary does not mention the article output:\n%s", sumOut)
	}
}

// ---------------------------------------------------------------------------
// Test 2: Successful Bluesky thread run via discovery (fake SearXNG)
// ---------------------------------------------------------------------------

// TestE2E_v05_ThreadDiscoveryRunSucceeds exercises the discovered source mode
// (planner -> SearXNG -> candidate selection) and thread rendering.
func TestE2E_v05_ThreadDiscoveryRunSucceeds(t *testing.T) {
	env := newV05Env(t, v05envOpts{
		articlePaths: []string{"article1", "article2"},
		useSearch:    true,
		format:       "thread",
	})

	// Explicit-mode URLs are not passed: discovery is the source.
	// The writer must answer with a THREAD artifact for this run.
	env.llm.SetResponse(v05MarkerWrite, v05threadJSON())
	err := runCommand(t, env.newRunArgs(true)...)
	if err != nil {
		t.Fatalf("newsroom run (discovery) failed: %v", err)
	}

	runID := v05FirstRunID(t, env.wsRoot)
	m := v05LoadManifest(t, env.wsRoot, runID)

	if m.Status != run.RunStatusSucceeded {
		t.Fatalf("run status = %s, want succeeded", m.Status)
	}
	if m.SourceMode != run.SourceModeDiscovered {
		t.Fatalf("source mode = %s, want discovered", m.SourceMode)
	}

	// The discovery stage ran the planner and the fake search.
	if n := env.llm.CallCount(v05MarkerPlanner); n == 0 {
		t.Error("planner was not called in discovered mode")
	}

	// The discovered candidates are recorded in the sources index (the
	// provider client canonicalizes the provider name to "searxng").
	sources := string(mustExistFile(t, filepath.Join(v05RunDir(env.wsRoot, runID), "sources", "index.json")))
	if !strings.Contains(sources, "searxng") {
		t.Error("sources index does not record the SearXNG candidates")
	}

	// Thread output: the rendered posts exist, and the article file does not.
	runDir := v05RunDir(env.wsRoot, runID)
	thread := string(mustExistFile(t, filepath.Join(runDir, "output", "thread.txt")))
	// The thread renderer may drop sentence-ending periods when splitting,
	// so assert on the claim text without its trailing period.
	for _, want := range []string{strings.TrimSuffix(v05ClaimOne, "."), strings.TrimSuffix(v05ClaimTwo, ".")} {
		if !strings.Contains(thread, want) {
			t.Errorf("thread output missing %q:\n%s", want, thread)
		}
	}
	// Every rendered post honors the Bluesky character budget.
	for i, post := range strings.Split(strings.TrimSpace(thread), "\n\n") {
		if len(post) > 300 {
			t.Errorf("rendered post %d is %d chars, exceeds the 300-char Bluesky budget", i, len(post))
		}
	}
	v05MustNotExist(t, filepath.Join(runDir, "output", "article.md"))
}

// ---------------------------------------------------------------------------
// Test 3: Final factual check failure fails the run
// ---------------------------------------------------------------------------

// TestE2E_v05_FinalCheckFailure asserts that a non-pass final check fails the
// run: no output is rendered, the run is resumable, and the user is pointed
// at the resume command.
func TestE2E_v05_FinalCheckFailure(t *testing.T) {
	env := newV05Env(t, v05envOpts{
		articlePaths: []string{"article1", "article2"},
		format:       "article",
	})
	env.llm.SetResponse(v05MarkerCheck, v05checkResultJSON(contracts.FactualCheckStatusFail))

	err := runCommand(t, env.newRunArgs(false)...)
	if err == nil {
		t.Fatal("run succeeded despite a failing final factual check")
	}
	if !strings.Contains(err.Error(), "--resume") {
		t.Errorf("failure error does not point at the resume path: %v", err)
	}

	runID := v05FirstRunID(t, env.wsRoot)
	m := v05LoadManifest(t, env.wsRoot, runID)
	if m.Status != run.RunStatusFailed {
		t.Fatalf("run status = %s, want failed", m.Status)
	}
	checkStatus, _ := v05StageStatus(t, m, run.StageFinalCheck)
	if checkStatus != run.StageStatusFailed {
		t.Fatalf("final_check status = %s, want failed", checkStatus)
	}
	if m.Result.FailedStage == nil || *m.Result.FailedStage != run.StageFinalCheck {
		t.Errorf("result.FailedStage = %v, want final_check", m.Result.FailedStage)
	}
	renderStatus, _ := v05StageStatus(t, m, run.StageRender)
	if renderStatus != run.StageStatusPending {
		t.Errorf("render status = %s, want pending (never reached)", renderStatus)
	}

	// The check record is kept on disk for inspection...
	mustExistFile(t, filepath.Join(v05RunDir(env.wsRoot, runID), "final_check", "result.json"))
	// ...but no usable output exists.
	v05MustNotExist(t, filepath.Join(v05RunDir(env.wsRoot, runID), "output", "article.md"))
}

// ---------------------------------------------------------------------------
// Test 4: Interrupted run resumes (SIGINT)
// ---------------------------------------------------------------------------

// TestE2E_v05_InterruptedRunResumes sends SIGINT to the running test process
// while the research stage is in flight (the fake LLM hangs on that call),
// asserts the run is recorded cancelled with the in-flight stage cancelled,
// then resumes the run and asserts it completes, re-attempting only the
// unfinished stages.
func TestE2E_v05_InterruptedRunResumes(t *testing.T) {
	if testing.Short() {
		t.Skip("interrupt test sends a real signal to the test process")
	}
	env := newV05Env(t, v05envOpts{
		articlePaths: []string{"article1", "article2"},
		format:       "article",
	})

	// The research LLM call blocks until released, so the run is
	// deterministically in flight when the signal is sent.
	env.llm.SetHang(v05MarkerResearch)

	done := make(chan error, 1)
	go func() {
		done <- runCommand(t, env.newRunArgs(false)...)
	}()

	// Wait until the manifest provably shows research running, then
	// interrupt.
	v05WaitRunDir(t, env.wsRoot)
	runID := v05FirstRunID(t, env.wsRoot)
	v05WaitStage(t, env.wsRoot, runID, run.StageResearch, run.StageStatusRunning)
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("sending SIGINT: %v", err)
	}

	err := <-done
	if err == nil {
		t.Fatal("run returned success after SIGINT, want a cancellation error")
	}
	if !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("cancellation error = %q, want it to mention cancellation", err)
	}
	env.llm.Release() // unblock the in-flight handler goroutine

	// The persisted manifest records the cancellation precisely.
	m := v05LoadManifest(t, env.wsRoot, runID)
	if m.Status != run.RunStatusCancelled {
		t.Fatalf("run status = %s, want cancelled", m.Status)
	}
	if st, _ := v05StageStatus(t, m, run.StageResearch); st != run.StageStatusCancelled {
		t.Errorf("research stage status = %s, want cancelled", st)
	}
	for _, name := range []run.StageName{run.StageDiscovery, run.StageFetch, run.StageExtract} {
		if st, _ := v05StageStatus(t, m, name); st != run.StageStatusSucceeded {
			t.Errorf("stage %s status = %s, want succeeded (upstream of the interrupt)", name, st)
		}
	}

	// Resume: the completed stages are reused, the rest run to completion.
	if err := runCommand(t, env.resumeArgs(runID)...); err != nil {
		t.Fatalf("resuming run failed: %v", err)
	}
	m = v05LoadManifest(t, env.wsRoot, runID)
	if m.Status != run.RunStatusSucceeded {
		t.Fatalf("resumed run status = %s, want succeeded", m.Status)
	}
	if st, attempts := v05StageStatus(t, m, run.StageResearch); st != run.StageStatusSucceeded || attempts != 2 {
		t.Errorf("research after resume: status=%s attempts=%d, want succeeded/2", st, attempts)
	}
	// Upstream stages were not re-run: their attempt count is unchanged.
	if _, attempts := v05StageStatus(t, m, run.StageDiscovery); attempts != 1 {
		t.Errorf("discovery attempts = %d after resume, want 1 (reused, not re-run)", attempts)
	}
	for _, name := range []run.StageName{run.StageVerify, run.StageWrite, run.StageFinalCheck, run.StageRender} {
		if st, _ := v05StageStatus(t, m, name); st != run.StageStatusSucceeded {
			t.Errorf("stage %s after resume = %s, want succeeded", name, st)
		}
	}
	mustExistFile(t, filepath.Join(v05RunDir(env.wsRoot, runID), "output", "article.md"))
	mustExistFile(t, filepath.Join(v05RunDir(env.wsRoot, runID), "review.md"))
}

// ---------------------------------------------------------------------------
// Test 5: Failed stage retries on resume
// ---------------------------------------------------------------------------

// TestE2E_v05_FailedStageRetriesOnResume fails the verify stage (HTTP 500)
// on the first attempt, then resumes with a healthy LLM: the failed stage
// re-attempts, completed stages are reused, and the run completes.
func TestE2E_v05_FailedStageRetriesOnResume(t *testing.T) {
	env := newV05Env(t, v05envOpts{
		articlePaths: []string{"article1", "article2"},
		format:       "article",
	})

	// First run: the verifier LLM call fails with a 500.
	env.llm.SetFailures(v05MarkerVerify, 1)
	err := runCommand(t, env.newRunArgs(false)...)
	if err == nil {
		t.Fatal("run succeeded despite a failed verify stage")
	}
	runID := v05FirstRunID(t, env.wsRoot)
	m := v05LoadManifest(t, env.wsRoot, runID)
	if m.Status != run.RunStatusFailed {
		t.Fatalf("run status = %s, want failed", m.Status)
	}
	if st, attempts := v05StageStatus(t, m, run.StageVerify); st != run.StageStatusFailed || attempts != 1 {
		t.Fatalf("verify stage = %s with %d attempts, want failed/1", st, attempts)
	}
	if m.Result.FailedStage == nil || *m.Result.FailedStage != run.StageVerify {
		t.Errorf("result.FailedStage = %v, want verify", m.Result.FailedStage)
	}

	// Second run: resume with the LLM healthy.
	env.llm.SetFailures(v05MarkerVerify, 0)
	if err := runCommand(t, env.resumeArgs(runID)...); err != nil {
		t.Fatalf("resuming run failed: %v", err)
	}

	m = v05LoadManifest(t, env.wsRoot, runID)
	if m.Status != run.RunStatusSucceeded {
		t.Fatalf("resumed run status = %s, want succeeded", m.Status)
	}
	if st, attempts := v05StageStatus(t, m, run.StageVerify); st != run.StageStatusSucceeded || attempts != 2 {
		t.Errorf("verify after resume = %s with %d attempts, want succeeded/2", st, attempts)
	}
	// The stages before the failure completed and must be reused, not
	// re-run: research keeps exactly one attempt (no new LLM call).
	if _, attempts := v05StageStatus(t, m, run.StageResearch); attempts != 1 {
		t.Errorf("research attempts = %d after resume, want 1 (reused)", attempts)
	}
	if n := env.llm.CallCount(v05MarkerResearch); n != 1 {
		t.Errorf("research LLM called %d times, want 1 (stage reused on resume)", n)
	}
	if n := env.llm.CallCount(v05MarkerVerify); n != 2 {
		t.Errorf("verify LLM called %d times, want 2 (failed attempt + retried attempt)", n)
	}
	mustExistFile(t, filepath.Join(v05RunDir(env.wsRoot, runID), "output", "article.md"))
}

// ---------------------------------------------------------------------------
// Test 6: Configuration precedence (CLI flag > env > file > default)
// ---------------------------------------------------------------------------

// TestE2E_v05_ConfigPrecedence proves all four precedence layers with the
// real resolution code:
//
//   - `config show` shows the winning layer for the env-vs-file and default
//     cases (the config command has no per-setting flags).
//   - a real run proves the flag layer: the flag beats env and file for both
//     the model (manifest Model) and the output format (the rendered file),
//     while a flag-free run falls through to env, then file.
func TestE2E_v05_ConfigPrecedence(t *testing.T) {
	// --- Part A: config show across layers -------------------------------
	setHermeticEnv(t)
	configPath := filepath.Join(v05cwd(t), "newsroom.yaml")
	mustWriteFile(t, configPath, "llm:\n  model: file-model\n")
	t.Setenv("NEWSROOM_CONFIG", configPath)
	t.Setenv("OMLX_MODEL", "env-model")

	out := captureStdout(t, func() {
		if err := runCommand(t, "config", "show", "--format", "json"); err != nil {
			t.Fatalf("config show: %v", err)
		}
	})
	entries := parseConfigShowEntries(t, out)
	if got := entryValue(entries, "llm.model"); got != "env-model" {
		t.Errorf("llm.model with env+file set = %q, want env-model (env beats file)", got)
	}
	if got := entrySource(entries, "llm.model"); got != "env:OMLX_MODEL" {
		t.Errorf("llm.model source = %q, want env:OMLX_MODEL", got)
	}

	// Env unset: the file layer wins.
	t.Setenv("OMLX_MODEL", "")
	out = captureStdout(t, func() {
		if err := runCommand(t, "config", "show", "--format", "json"); err != nil {
			t.Fatalf("config show: %v", err)
		}
	})
	entries = parseConfigShowEntries(t, out)
	if got := entryValue(entries, "llm.model"); got != "file-model" {
		t.Errorf("llm.model with file only = %q, want file-model", got)
	}

	// No file, no env: the built-in default layer (llm.model has no default).
	t.Setenv("NEWSROOM_CONFIG", "")
	out = captureStdout(t, func() {
		if err := runCommand(t, "config", "show", "--format", "json"); err != nil {
			t.Fatalf("config show: %v", err)
		}
	})
	entries = parseConfigShowEntries(t, out)
	if got := entrySource(entries, "llm.model"); got != "unset" {
		t.Errorf("llm.model with nothing set: source = %q, want unset", got)
	}

	// --- Part B: config validate ------------------------------------------
	// A literal credential in the file is rejected.
	badPath := filepath.Join(v05cwd(t), "newsroom-bad.yaml")
	mustWriteFile(t, badPath, "llm:\n  api_key: sk-literal-should-not-be-in-file\n")
	t.Setenv("NEWSROOM_CONFIG", badPath)
	if err := runCommand(t, "config", "validate"); err == nil {
		t.Fatal("config validate accepted a literal secret in the config file")
	} else if !strings.Contains(err.Error(), "secret") {
		t.Errorf("config validate error = %q, want a secret-related error", err)
	}

	// A proper env reference validates.
	goodPath := filepath.Join(v05cwd(t), "newsroom-good.yaml")
	mustWriteFile(t, goodPath, "llm:\n  model: m1\n  api_key:\n    env: OMLX_API_KEY\n")
	t.Setenv("NEWSROOM_CONFIG", goodPath)
	out = captureStdout(t, func() {
		if err := runCommand(t, "config", "validate"); err != nil {
			t.Fatalf("config validate (env reference): %v", err)
		}
	})
	if !strings.Contains(out, "is valid") {
		t.Errorf("config validate output = %q, want it to report the file is valid", out)
	}

	// --- Part C: the flag layer, proven through a real run ----------------
	// The file says thread output + file-model; the env says env-model; the
	// flags say flag-model + article. The run must use the flags for both,
	// and the manifest snapshot must record them.
	env := newV05Env(t, v05envOpts{
		articlePaths: []string{"article1", "article2"},
		configYAML:   "defaults:\n  output_format: thread\nllm:\n  model: file-model\n",
	})
	t.Setenv("OMLX_MODEL", "env-model")

	if err := runCommand(t, env.newRunArgs(false, "--llm-model", "flag-model", "--format", "article")...); err != nil {
		t.Fatalf("run with flag overrides failed: %v", err)
	}
	runID := v05FirstRunID(t, env.wsRoot)
	m := v05LoadManifest(t, env.wsRoot, runID)
	if m.Model.Model != "flag-model" {
		t.Errorf("manifest model = %q, want flag-model (flag beats env and file)", m.Model.Model)
	}
	if v, ok := m.Config.Get("defaults.output_format"); !ok || v != "article" {
		t.Errorf("config snapshot defaults.output_format = %q (present=%v), want article (flag wins)", v, ok)
	}
	runDir := v05RunDir(env.wsRoot, runID)
	mustExistFile(t, filepath.Join(runDir, "output", "article.md"))
	v05MustNotExist(t, filepath.Join(runDir, "output", "thread.txt"))

	// Same environment, no format/model flags: the env model wins and the
	// file's output format wins. (Omit the --llm-model flag entirely so the
	// config chain can resolve the model from the environment.)
	env.llmModel = ""
	if err := runCommand(t, env.newRunArgs(false, "--topic", "second run topic")...); err != nil {
		t.Fatalf("flag-free run failed: %v", err)
	}
	runIDs := listRunIDs(t, env.wsRoot)
	if len(runIDs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runIDs))
	}
	// The directory order is lexicographic, not chronological: pick the
	// flag-free run by its topic.
	var m2 *run.Manifest
	for _, id := range runIDs {
		m := v05LoadManifest(t, env.wsRoot, id)
		if m.Topic == "second run topic" {
			m2 = m
			break
		}
	}
	if m2 == nil {
		t.Fatal("could not find the flag-free run (topic 'second run topic')")
	}
	if m2.Model.Model != "env-model" {
		t.Errorf("flag-free manifest model = %q, want env-model (env beats file)", m2.Model.Model)
	}
	if v, _ := m2.Config.Get("defaults.output_format"); v != "thread" {
		t.Errorf("flag-free config snapshot output_format = %q, want thread (file wins)", v)
	}
	mustExistFile(t, filepath.Join(v05RunDir(env.wsRoot, string(m2.ID)), "output", "thread.txt"))
	v05MustNotExist(t, filepath.Join(v05RunDir(env.wsRoot, string(m2.ID)), "output", "article.md"))
}

// configShowEntry mirrors the JSON shape of `config show --format json`.
type configShowEntry struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Source string `json:"source"`
}

func parseConfigShowEntries(t *testing.T, out string) []configShowEntry {
	t.Helper()
	var report struct {
		ConfigFile string            `json:"config_file"`
		Entries    []configShowEntry `json:"entries"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("parsing config show JSON: %v", err)
	}
	return report.Entries
}

func entryValue(entries []configShowEntry, key string) string {
	for _, e := range entries {
		if e.Key == key {
			return e.Value
		}
	}
	return "<missing>"
}

func entrySource(entries []configShowEntry, key string) string {
	for _, e := range entries {
		if e.Key == key {
			return e.Source
		}
	}
	return "<missing>"
}

// ---------------------------------------------------------------------------
// Test 7: No secrets in the persisted workspace
// ---------------------------------------------------------------------------

// TestE2E_v05_NoSecretsInWorkspace makes a realistic credential-leak scenario:
// the LLM API key is configured (env referenced from the config file), and
// the fetched web page itself contains a high-entropy token. After the run,
// NO file in the workspace may contain either value.
func TestE2E_v05_NoSecretsInWorkspace(t *testing.T) {
	const llmKey = "sk-omlx-0123456789abcdef0123456789abcdef0123"
	const pageToken = "ghp_7f8e9d0c1b2a39485766554433221100ffeeddccbbaa99"

	env := newV05Env(t, v05envOpts{
		articlePaths: []string{"article1", "article2"},
		format:       "article",
		llmAPIKey:    llmKey,
		configYAML:   "llm:\n  api_key:\n    env: OMLX_API_KEY\n",
	})

	// Plant the token inside the second article before the run fetches it.
	env.taintArticle(t, env.urls[1], pageToken)

	if err := runCommand(t, env.newRunArgs(false)...); err != nil {
		t.Fatalf("newsroom run failed: %v", err)
	}

	// No persisted file may contain the LLM credential or the page token.
	nFiles := v05AssertNoSecret(t, env.wsRoot, llmKey)
	v05AssertNoSecret(t, env.wsRoot, pageToken)
	t.Logf("inspected %d persisted files; neither secret appears in any of them", nFiles)

	// The redaction actually happened: the raw fetch of the tainted page was
	// sanitized on write, and a redaction sidecar records what was hidden.
	if n := v05CountRedactionSidecars(t, env.wsRoot); n == 0 {
		t.Error("expected at least one .redaction.json sidecar (the tainted fetch body must be sanitized)")
	}
}

// ---------------------------------------------------------------------------
// Test 8: Persisted artifacts survive a reload
// ---------------------------------------------------------------------------

// TestE2E_v05_ArtifactsSurviveReload runs a full pipeline, closes the
// workspace, and re-opens it from disk: every artifact must load back with
// byte-identical content (atomic writes, no drift), and ListRuns must see the
// run again.
func TestE2E_v05_ArtifactsSurviveReload(t *testing.T) {
	env := newV05Env(t, v05envOpts{
		articlePaths: []string{"article1", "article2"},
		format:       "article",
	})

	if err := runCommand(t, env.newRunArgs(false)...); err != nil {
		t.Fatalf("newsroom run failed: %v", err)
	}
	runID := v05FirstRunID(t, env.wsRoot)

	// Snapshot every file of the run directory as written.
	first := v05WalkWorkspaceFiles(t, v05RunDir(env.wsRoot, runID))
	if len(first) == 0 {
		t.Fatal("run directory is empty; nothing to reload")
	}

	// Reload through a fresh store (the way a later resume would).
	store, err := workspace.NewStore(env.wsRoot)
	if err != nil {
		t.Fatalf("re-opening workspace: %v", err)
	}
	ids, err := store.ListRuns()
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(ids) != 1 || string(ids[0]) != runID {
		t.Fatalf("ListRuns after reload = %v, want [%s]", ids, runID)
	}
	m, _, err := store.LoadManifest(run.RunID(runID))
	if err != nil {
		t.Fatalf("LoadManifest after reload: %v", err)
	}
	if m.Status != run.RunStatusSucceeded {
		t.Fatalf("reloaded manifest status = %s, want succeeded", m.Status)
	}

	// Every manifest-referenced artifact loads back byte-identically.
	checked := 0
	for _, ref := range m.Artifacts {
		if ref.Path == "" {
			continue
		}
		reloaded, err := store.LoadArtifact(run.RunID(runID), ref.Path)
		if err != nil {
			t.Errorf("LoadArtifact(%s) after reload: %v", ref.Path, err)
			continue
		}
		original := first[ref.Path]
		if original == nil {
			// Fall back to the on-disk file (the walk keys are relative).
			original, _ = os.ReadFile(filepath.Join(v05RunDir(env.wsRoot, runID), ref.Path))
		}
		if !bytes.Equal(reloaded, original) {
			t.Errorf("artifact %s changed on reload (%d vs %d bytes)", ref.Path, len(reloaded), len(original))
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no manifest-referenced artifacts were checked")
	}
	t.Logf("reloaded %d artifacts byte-identically from the re-opened workspace", checked)
}
