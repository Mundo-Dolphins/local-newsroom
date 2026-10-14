package orchestrator

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/archiverev"
	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/discovery"
	"github.com/Mundo-Dolphins/local-newsroom/internal/followup"
	"github.com/Mundo-Dolphins/local-newsroom/internal/planner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/renderers"
	"github.com/Mundo-Dolphins/local-newsroom/internal/researcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/runner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/search"
	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
	"github.com/Mundo-Dolphins/local-newsroom/internal/verifier"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workspace"
)

// testTime is a fixed timestamp for deterministic assertions.
var testTime = time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

// =============================================================================
// Fakes
// =============================================================================

type fakePlanner struct {
	plan  *planner.SearchPlan
	err   error
	calls int
}

func (f *fakePlanner) Generate(_ context.Context, topic, language string, _ *planner.TimeRangeHint) (*planner.SearchPlan, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if f.plan != nil {
		return f.plan, nil
	}
	return &planner.SearchPlan{
		OriginalTopic: topic,
		Language:      language,
		Queries:       []planner.SearchQuery{{Query: topic, Purpose: "primary"}},
	}, nil
}

type fakeDiscovery struct {
	res   *discovery.DiscoveryResult
	err   error
	calls int
}

func (f *fakeDiscovery) Discover(_ context.Context, _ string, _ string, _ *planner.TimeRangeHint) (*discovery.DiscoveryResult, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.res, nil
}

type fakeSearchProvider struct {
	res   *search.Response
	err   error
	calls int
}

func (f *fakeSearchProvider) Search(_ context.Context, req search.Request) (search.Response, error) {
	f.calls++
	if f.err != nil {
		return search.Response{}, f.err
	}
	if f.res != nil {
		return *f.res, nil
	}
	return search.Response{OriginalQuery: req.Query, Results: []search.SearchResult{}}, nil
}

type fakeFetcher struct {
	// bodyFor maps URL to body; URLs not listed and not in fail fail.
	bodyFor map[string]string
	fail    map[string]string
	// block makes FetchMany wait on the context before returning, for
	// cancellation tests.
	block bool
	calls int
}

func (f *fakeFetcher) FetchMany(ctx context.Context, urls []string) []FetchedSource {
	f.calls++
	if f.block {
		<-ctx.Done()
	}
	out := make([]FetchedSource, 0, len(urls))
	for _, u := range urls {
		src := FetchedSource{URL: u, Status: 200, RetrievedAt: testTime}
		if err, ok := f.fail[u]; ok {
			src.OK = false
			src.Error = err
			src.Status = 500
		} else if body, ok := f.bodyFor[u]; ok {
			src.OK = true
			src.ContentType = "text/html"
			src.RawBody = []byte(body)
		} else {
			src.OK = false
			src.Error = "no fake response"
		}
		out = append(out, src)
	}
	return out
}

type fakeExtractor struct{ calls int }

func (f *fakeExtractor) Extract(_ context.Context, src FetchedSource) (*types.Document, error) {
	f.calls++
	title := "Title of " + src.URL
	return &types.Document{
		SourceID:     src.URL,
		DocumentType: types.SourceTypeWeb,
		CanonicalURL: &src.URL,
		Title:        &title,
		PlainText:    strings.TrimSpace(string(src.RawBody)),
		RetrievedAt:  src.RetrievedAt,
	}, nil
}

type fakeResearch struct {
	dossier *researcher.ResearchDossier
	err     error
	// docsSeen records the documents the stage handed over (for assertions on
	// archive augmentation).
	docsSeen *[]string
	calls    int
}

func (f *fakeResearch) Generate(_ context.Context, _ string, docs []types.Document) (*researcher.ResearchDossier, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if f.docsSeen != nil {
		ids := make([]string, len(docs))
		for i, d := range docs {
			ids[i] = d.SourceID
		}
		*f.docsSeen = ids
	}
	return f.dossier, nil
}

type fakeArchive struct {
	res   *archiverev.RetrieveResult
	err   error
	calls int
}

func (f *fakeArchive) Retrieve(_ context.Context, _ string, _ []string) (*archiverev.RetrieveResult, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.res, nil
}

type fakeVerify struct {
	res   *contracts.VerificationResult
	err   error
	calls int
}

func (f *fakeVerify) Verify(_ context.Context, _ verifier.Input) (*contracts.VerificationResult, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.res, nil
}

type fakeFollowUp struct {
	res   *followup.FollowUpResult
	err   error
	calls int
}

func (f *fakeFollowUp) Run(_ context.Context, _ *researcher.ResearchDossier, _ *contracts.VerificationResult) (*followup.FollowUpResult, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if f.res != nil {
		return f.res, nil
	}
	return &followup.FollowUpResult{RoundNumber: 1, UnresolvedClaimsAfter: []string{}}, nil
}

type fakeWrite struct {
	artifact *contracts.EditorialArtifact
	err      error
	calls    int
}

func (f *fakeWrite) Generate(_ context.Context, _ *contracts.VerificationResult) (*contracts.EditorialArtifact, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.artifact, nil
}

type fakeCheck struct {
	status contracts.FactualCheckStatus
	err    error
	calls  int
}

func (f *fakeCheck) Check(_ context.Context, artifact *contracts.EditorialArtifact, _ *contracts.VerificationResult) (*contracts.FactualCheckResult, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if f.status == "" {
		f.status = contracts.FactualCheckStatusPass
	}
	return &contracts.FactualCheckResult{
		StableID:                "check-fake-001",
		InputArtifactID:         artifact.StableID,
		CheckedAt:               testTime,
		ClaimIDs:                []string{},
		ClaimVerificationStatus: map[string]contracts.VerificationStatus{},
		OverallStatus:           f.status,
		ConfidenceLevel:         contracts.ConfidenceHigh,
		Summary:                 "fake check",
		FactualFindings:         []contracts.FactualFinding{},
	}, nil
}

// =============================================================================
// Fixtures and helpers
// =============================================================================

// articleURLs are the deterministic explicit source URLs for tests.
var articleURLs = []string{
	"https://example.com/alpha",
	"https://example.com/beta",
}

func newTestStore(t *testing.T) *workspace.Store {
	t.Helper()
	root := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatalf("creating workspace store: %v", err)
	}
	return store
}

// testManifest builds a fresh pending manifest for tests. The run directory
// is claimed by the runner itself (store.CreateRun), which runs once inside
// o.Run — pre-creating it here would collide with that claim.
func testManifest(t *testing.T, _ *workspace.Store, topic, mode string) *run.Manifest {
	t.Helper()
	m, err := run.NewManifest(run.NewManifestParams{
		Workspace:  run.Workspace{Name: "test"},
		Topic:      topic,
		SourceMode: run.SourceMode(mode),
		Config:     &run.ConfigSnapshot{Values: map[string]string{}},
		Stages:     run.CanonicalStages(),
		StartedAt:  testTime,
	})
	if err != nil {
		t.Fatalf("creating manifest: %v", err)
	}
	return m
}

// testDocument builds a minimal live document for a URL.
func testDocument(url string) types.Document {
	return types.Document{
		SourceID:     url,
		DocumentType: types.SourceTypeWeb,
		CanonicalURL: &url,
		PlainText:    "Body text for " + url,
		RetrievedAt:  testTime,
	}
}

// testDossier builds a dossier over the given document URLs with one supported
// claim per document.
func testDossier(topic string, urls ...string) *researcher.ResearchDossier {
	sources := make([]researcher.SourceReference, 0, len(urls))
	claims := make([]researcher.Claim, 0, len(urls))
	for i, u := range urls {
		sources = append(sources, researcher.SourceReference{
			StableID:    u,
			OriginalURL: u,
			SourceType:  types.SourceTypeWeb,
			RetrievedAt: testTime,
		})
		claims = append(claims, researcher.Claim{
			ID:         "c" + string(rune('1'+i)),
			Statement:  "Claim from " + u,
			Confidence: researcher.ConfidenceHigh,
			Evidence: []researcher.Evidence{{
				SourceID: u,
				Excerpts: []researcher.Excerpt{{Text: "text", StartOffset: 0, EndOffset: 4}},
			}},
		})
	}
	return &researcher.ResearchDossier{
		StableID:    "dossier-test-001",
		Topic:       topic,
		GeneratedAt: testTime,
		Sources:     sources,
		Claims:      claims,
	}
}

// testVerification builds a verification result that fully supports every
// claim in the dossier.
func testVerification(dossier *researcher.ResearchDossier) *contracts.VerificationResult {
	statuses := make(map[string]contracts.VerificationStatus, len(dossier.Claims))
	details := make(map[string]contracts.ClaimVerificationDetails, len(dossier.Claims))
	for _, c := range dossier.Claims {
		statuses[c.ID] = contracts.VerificationStatusSupported
		details[c.ID] = contracts.ClaimVerificationDetails{
			ClaimID:            c.ID,
			Statement:          c.Statement,
			VerificationStatus: contracts.VerificationStatusSupported,
			ConfidenceLevel:    contracts.ConfidenceHigh,
		}
	}
	return &contracts.VerificationResult{
		StableID:             "ver-test-001",
		InputDossierID:       dossier.StableID,
		VerifiedAt:           testTime,
		VerificationStatuses: statuses,
		ClaimDetails:         details,
		Contradictions:       []contracts.VerifiedContradiction{},
		QualityScore:         90,
	}
}

// testArtifact builds a valid article artifact.
func testArtifact() *contracts.EditorialArtifact {
	return &contracts.EditorialArtifact{
		StableID:     "art-test-001",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "Test Story Title",
		Body:         "This is the test story body. It says something factual.",
		ClaimReferences: map[string]contracts.ClaimUsage{
			"c1": {ClaimID: "c1", UsageType: contracts.ClaimUsageCore},
		},
		SourceReferences: map[string]contracts.SourceUsage{},
		GenerationMetadata: contracts.GenerationMetadata{
			InputVerificationID: "ver-test-001",
			GeneratedAt:         testTime,
		},
	}
}

// newOrchestrator wires a full fake stack. Fields left at zero/nil values
// disable the corresponding optional stage.
func newOrchestrator(t *testing.T, store *workspace.Store, cfg Config) (*Orchestrator, *testServices) {
	t.Helper()
	cfg.Store = store
	if cfg.Markdown == nil {
		cfg.Markdown = renderers.NewMarkdownRenderer()
	}
	if cfg.Thread == nil {
		cfg.Thread = renderers.NewThreadRenderer()
	}
	if cfg.Fetcher == nil {
		cfg.Fetcher = &fakeFetcher{bodyFor: map[string]string{}}
	}
	if cfg.Extractor == nil {
		cfg.Extractor = &fakeExtractor{}
	}
	if cfg.Research == nil {
		cfg.Research = &fakeResearch{}
	}
	if cfg.Verify == nil {
		cfg.Verify = &fakeVerify{}
	}
	if cfg.Write == nil {
		cfg.Write = &fakeWrite{}
	}
	if cfg.Check == nil {
		cfg.Check = &fakeCheck{}
	}
	o, err := New(cfg)
	if err != nil {
		t.Fatalf("building orchestrator: %v", err)
	}
	svcs := &testServices{}
	if f, ok := cfg.Fetcher.(*fakeFetcher); ok {
		svcs.fetcher = f
	}
	if f, ok := cfg.Research.(*fakeResearch); ok {
		svcs.research = f
	}
	if f, ok := cfg.Verify.(*fakeVerify); ok {
		svcs.verify = f
	}
	if f, ok := cfg.Write.(*fakeWrite); ok {
		svcs.write = f
	}
	if f, ok := cfg.Check.(*fakeCheck); ok {
		svcs.check = f
	}
	if f, ok := cfg.FollowUp.(*fakeFollowUp); ok {
		svcs.followUp = f
	}
	if f, ok := cfg.Archive.(*fakeArchive); ok {
		svcs.archive = f
	}
	return o, svcs
}

type testServices struct {
	fetcher  *fakeFetcher
	research *fakeResearch
	verify   *fakeVerify
	write    *fakeWrite
	check    *fakeCheck
	followUp *fakeFollowUp
	archive  *fakeArchive
}

// runTestManifest sets the run-level snapshot values the stages resolve.
func runTestManifest(m *run.Manifest, format string, followUp bool, urls []string) {
	if err := m.Config.Set(SettingRunFormat, format); err != nil {
		panic(err)
	}
	if followUp {
		_ = m.Config.Set(SettingRunFollowUp, "true")
	} else {
		_ = m.Config.Set(SettingRunFollowUp, "false")
	}
	if len(urls) > 0 {
		data, err := jsonMarshalIndent(urls)
		if err == nil {
			_ = m.Config.Set(SettingRunExplicitURLs, strings.Trim(string(data), " \n"))
		}
	}
}

// fileExists returns true when the file exists under the run directory.
func fileExists(t *testing.T, store *workspace.Store, id run.RunID, rel string) bool {
	t.Helper()
	data, err := store.LoadArtifact(id, rel)
	if err != nil {
		return false
	}
	return len(data) > 0
}

// stageStatus returns the status of a planned stage.
func stageStatus(t *testing.T, m *run.Manifest, name run.StageName) run.StageStatus {
	t.Helper()
	for _, s := range m.Stages {
		if s.Name == name {
			return s.Status
		}
	}
	t.Fatalf("stage %s not in plan", name)
	return ""
}

// =============================================================================
// Tests
// =============================================================================

// TestRunExplicitHappyPath runs the full plan over explicit sources with a
// passing final check and asserts the complete durable record.
func TestRunExplicitHappyPath(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	m := testManifest(t, store, "Test Topic", string(run.SourceModeExplicit))
	runTestManifest(m, FormatArticle, false, articleURLs)

	body := "<html><head><title>Alpha</title></head><body><p>Alpha content.</p></body></html>"
	o, svcs := newOrchestrator(t, store, Config{
		Format:       FormatArticle,
		ExplicitURLs: articleURLs,
		Fetcher: &fakeFetcher{bodyFor: map[string]string{
			articleURLs[0]: body,
			articleURLs[1]: "<html><body><p>Beta content.</p></body></html>",
		}},
		Research: &fakeResearch{dossier: testDossier("Test Topic", articleURLs[0], articleURLs[1])},
		Verify:   &fakeVerify{},
		Write:    &fakeWrite{artifact: testArtifact()},
		Check:    &fakeCheck{},
	})
	svcs.verify.res = testVerification(svcs.research.dossier)

	res, err := o.Run(ctx, m, false)
	if err != nil {
		t.Fatalf("run should succeed: %v", err)
	}
	if res.Status != run.RunStatusSucceeded {
		t.Fatalf("status = %s, want succeeded (err=%v)", res.Status, res.Err)
	}

	// Every stage ran (follow-up was disabled for the run).
	for _, name := range []run.StageName{
		run.StageDiscovery, run.StageFetch, run.StageExtract, run.StageResearch,
		run.StageVerify, run.StageWrite, run.StageFinalCheck, run.StageRender,
	} {
		if got := stageStatus(t, res.Manifest, name); got != run.StageStatusSucceeded {
			t.Errorf("stage %s status = %s, want succeeded", name, got)
		}
	}
	if got := stageStatus(t, res.Manifest, run.StageFollowUp); got != run.StageStatusSkipped {
		t.Errorf("follow-up stage status = %s, want skipped", got)
	}

	// The durable record is complete.
	for _, rel := range []string{
		sourcesIndexPath, fetchIndexPath, documentsIndexPath,
		dossierPath, researchIndexPath, verificationPath,
		artifactPath, finalCheckPath,
	} {
		if !fileExists(t, store, m.ID, rel) {
			t.Errorf("missing persisted artifact %s", rel)
		}
	}
	// Rendered output.
	rendered := 0
	for _, a := range res.Manifest.Artifacts {
		if a.Kind == run.ArtifactKindRendered {
			rendered++
		}
	}
	if rendered == 0 {
		t.Error("no rendered artifacts recorded")
	}
	if !fileExists(t, store, m.ID, "output/article.md") {
		t.Error("missing rendered article")
	}
	if svcs.write.calls != 1 {
		t.Errorf("write called %d times, want 1", svcs.write.calls)
	}

	// The final check passed and is recorded on the manifest.
	var check contracts.FactualCheckResult
	if data, err := store.LoadArtifact(m.ID, finalCheckPath); err != nil {
		t.Fatalf("loading check: %v", err)
	} else if err := jsonUnmarshal(data, &check); err != nil {
		t.Fatalf("parsing check: %v", err)
	} else if check.OverallStatus != contracts.FactualCheckStatusPass {
		t.Errorf("check status = %s, want pass", check.OverallStatus)
	}
}

// TestRunFinalCheckFailFailsTheRun asserts the gating rule: a non-pass final
// check fails the run and no output is rendered, while the check record is
// kept on disk for inspection.
func TestRunFinalCheckFailFailsTheRun(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	m := testManifest(t, store, "Test Topic", string(run.SourceModeExplicit))
	runTestManifest(m, FormatArticle, false, articleURLs)

	o, svcs := newOrchestrator(t, store, Config{
		Format:       FormatArticle,
		ExplicitURLs: articleURLs,
		Fetcher:      &fakeFetcher{bodyFor: map[string]string{articleURLs[0]: "<html><body><p>x</p></body></html>"}},
		Research:     &fakeResearch{dossier: testDossier("Test Topic", articleURLs[0])},
		Verify:       &fakeVerify{},
		Write:        &fakeWrite{artifact: testArtifact()},
		Check:        &fakeCheck{status: contracts.FactualCheckStatusFail},
	})
	svcs.verify.res = testVerification(svcs.research.dossier)

	res, err := o.Run(ctx, m, false)
	if err == nil {
		t.Fatal("run should fail when the final check fails")
	}
	if res.Status != run.RunStatusFailed {
		t.Fatalf("status = %s, want failed", res.Status)
	}
	if got := stageStatus(t, res.Manifest, run.StageFinalCheck); got != run.StageStatusFailed {
		t.Errorf("final_check status = %s, want failed", got)
	}
	if got := stageStatus(t, res.Manifest, run.StageRender); got == run.StageStatusSucceeded {
		t.Error("render must not run after a failed final check")
	}
	if fileExists(t, store, m.ID, "output/article.md") {
		t.Error("no rendered output may exist after a failed final check")
	}
	// The check record is kept on disk even though the stage failed.
	if !fileExists(t, store, m.ID, finalCheckPath) {
		t.Error("the failed check record should be kept on disk for inspection")
	}
	// ... but it is NOT recorded on the manifest (the manifest forbids
	// artifacts on a failed stage, and the resume layer enforces that).
	for _, a := range res.Manifest.Artifacts {
		if a.Stage == run.StageFinalCheck {
			t.Error("failed final_check stage must not hold manifest artifact references")
		}
	}
}

// TestRunFinalCheckReviewFailsTheRun: "needs review" is not usable output.
func TestRunFinalCheckReviewFailsTheRun(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	m := testManifest(t, store, "Test Topic", string(run.SourceModeExplicit))
	runTestManifest(m, FormatArticle, false, articleURLs[:1])

	o, svcs := newOrchestrator(t, store, Config{
		Format:       FormatArticle,
		ExplicitURLs: articleURLs[:1],
		Fetcher:      &fakeFetcher{bodyFor: map[string]string{articleURLs[0]: "<html><body><p>x</p></body></html>"}},
		Research:     &fakeResearch{dossier: testDossier("Test Topic", articleURLs[0])},
		Verify:       &fakeVerify{},
		Write:        &fakeWrite{artifact: testArtifact()},
		Check:        &fakeCheck{status: contracts.FactualCheckStatusReview},
	})
	svcs.verify.res = testVerification(svcs.research.dossier)

	res, err := o.Run(ctx, m, false)
	if err == nil {
		t.Fatal("run should fail on a review status")
	}
	if res.Status != run.RunStatusFailed {
		t.Fatalf("status = %s, want failed", res.Status)
	}
	if fileExists(t, store, m.ID, "output/article.md") {
		t.Error("no rendered output may exist after a review final check")
	}
}

// TestResumeAfterFailedCheck re-runs a run that failed at the final check:
// the completed stages are reused (never re-executed) and the re-checked run
// completes with rendered output.
func TestResumeAfterFailedCheck(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	m := testManifest(t, store, "Test Topic", string(run.SourceModeExplicit))
	runTestManifest(m, FormatArticle, false, articleURLs[:1])

	mk := func(checkStatus contracts.FactualCheckStatus) (*Orchestrator, *testServices) {
		research := &fakeResearch{dossier: testDossier("Test Topic", articleURLs[0])}
		verify := &fakeVerify{}
		verify.res = testVerification(research.dossier)
		o, svcs := newOrchestrator(t, store, Config{
			Format:       FormatArticle,
			ExplicitURLs: articleURLs[:1],
			Fetcher:      &fakeFetcher{bodyFor: map[string]string{articleURLs[0]: "<html><body><p>x</p></body></html>"}},
			Research:     research,
			Verify:       verify,
			Write:        &fakeWrite{artifact: testArtifact()},
			Check:        &fakeCheck{status: checkStatus},
		})
		return o, svcs
	}

	// First cycle: fails at the final check.
	o1, _ := mk(contracts.FactualCheckStatusFail)
	res1, err := o1.Run(ctx, m, false)
	if res1.Status != run.RunStatusFailed {
		t.Fatalf("first run status = %s, want failed (%v)", res1.Status, err)
	}

	// Second cycle: the same run is resumed with a passing check.
	o2, svcs2 := mk(contracts.FactualCheckStatusPass)
	res2, err := o2.ResumeID(ctx, m.ID)
	if err != nil {
		t.Fatalf("resume should succeed: %v", err)
	}
	if res2.Status != run.RunStatusSucceeded {
		t.Fatalf("resumed run status = %s, want succeeded (%v)", res2.Status, res2.Err)
	}
	// The expensive upstream stages were NOT re-executed.
	if svcs2.research.calls != 0 {
		t.Errorf("research re-executed on resume (calls=%d); completed stages must be reused", svcs2.research.calls)
	}
	if svcs2.write == nil || svcs2.write.calls != 0 {
		t.Errorf("write re-executed on resume (calls=%v); it already succeeded", svcs2.write)
	}
	if svcs2.check == nil || svcs2.check.calls != 1 {
		t.Errorf("check should run exactly once on resume (calls=%v)", svcs2.check)
	}
	if !fileExists(t, store, m.ID, "output/article.md") {
		t.Error("resumed run must produce rendered output")
	}
}

// TestResumeSucceededIsNoOp resuming a succeeded run changes nothing.
func TestResumeSucceededIsNoOp(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	m := testManifest(t, store, "Test Topic", string(run.SourceModeExplicit))
	runTestManifest(m, FormatArticle, false, articleURLs[:1])

	research := &fakeResearch{dossier: testDossier("Test Topic", articleURLs[0])}
	verify := &fakeVerify{}
	verify.res = testVerification(research.dossier)
	o, _ := newOrchestrator(t, store, Config{
		Format:       FormatArticle,
		ExplicitURLs: articleURLs[:1],
		Fetcher:      &fakeFetcher{bodyFor: map[string]string{articleURLs[0]: "<html><body><p>x</p></body></html>"}},
		Research:     research,
		Verify:       verify,
		Write:        &fakeWrite{artifact: testArtifact()},
		Check:        &fakeCheck{},
	})
	if res, err := o.Run(ctx, m, false); err != nil || res.Status != run.RunStatusSucceeded {
		t.Fatalf("first run: status=%s err=%v", res.Status, err)
	}
	if research.calls != 1 {
		t.Fatalf("research calls = %d, want 1", research.calls)
	}

	if _, err := o.ResumeID(ctx, m.ID); err != nil {
		t.Fatalf("resuming a succeeded run should be a no-op, got %v", err)
	}
	if research.calls != 1 {
		t.Errorf("research re-executed on no-op resume (calls=%d)", research.calls)
	}
}

// TestRunCancelledRecordsCancelledState cancels mid-fetch and asserts the
// durable cancelled state.
func TestRunCancelledRecordsCancelledState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := newTestStore(t)
	m := testManifest(t, store, "Test Topic", string(run.SourceModeExplicit))
	runTestManifest(m, FormatArticle, false, articleURLs[:1])

	o, _ := newOrchestrator(t, store, Config{
		Format:       FormatArticle,
		ExplicitURLs: articleURLs[:1],
		Fetcher:      &fakeFetcher{block: true},
		Research:     &fakeResearch{dossier: testDossier("Test Topic", articleURLs[0])},
		Verify:       &fakeVerify{},
		Write:        &fakeWrite{artifact: testArtifact()},
		Check:        &fakeCheck{},
	})
	cancel() // cancel before the run starts

	res, err := o.Run(ctx, m, false)
	if err == nil {
		t.Fatal("run should fail on cancellation")
	}
	if res.Status != run.RunStatusCancelled {
		t.Fatalf("status = %s, want cancelled", res.Status)
	}
}

// TestRunDiscoveredMode selects candidates from discovery and fetches them.
func TestRunDiscoveredMode(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	m := testManifest(t, store, "Test Topic", string(run.SourceModeDiscovered))
	runTestManifest(m, FormatArticle, false, nil)

	cands := make([]discovery.Candidate, 0, 3)
	for i, u := range []string{"https://a.example/1", "https://b.example/2", "https://c.example/3"} {
		cands = append(cands, discovery.Candidate{CandidateURL: u, CandidateDomain: "example", BestRank: i + 1})
	}
	o, svcs := newOrchestrator(t, store, Config{
		Format:     FormatArticle,
		MaxSources: 2, // bound: only the two best candidates
		Discovery:  &fakeDiscovery{res: &discovery.DiscoveryResult{Candidates: cands}},
		Fetcher:    &fakeFetcher{bodyFor: map[string]string{"https://a.example/1": "<html><body><p>one</p></body></html>", "https://b.example/2": "<html><body><p>two</p></body></html>"}},
		Research:   &fakeResearch{dossier: testDossier("Test Topic", "https://a.example/1", "https://b.example/2")},
		Verify:     &fakeVerify{},
		Write:      &fakeWrite{artifact: testArtifact()},
		Check:      &fakeCheck{},
	})
	svcs.verify.res = testVerification(svcs.research.dossier)

	res, err := o.Run(ctx, m, false)
	if err != nil {
		t.Fatalf("run should succeed: %v (status=%s)", err, res.Status)
	}
	// The sources index selected exactly the two best candidates.
	var idx sourcesIndex
	data, err := store.LoadArtifact(m.ID, sourcesIndexPath)
	if err != nil {
		t.Fatalf("loading sources index: %v", err)
	}
	if err := jsonUnmarshal(data, &idx); err != nil {
		t.Fatalf("parsing sources index: %v", err)
	}
	if len(idx.Selected) != 2 {
		t.Fatalf("selected %d sources, want 2 (max-sources bound): %v", len(idx.Selected), idx.Selected)
	}
	if idx.Selected[0] != "https://a.example/1" {
		t.Errorf("first selected = %s, want the best candidate", idx.Selected[0])
	}
}

// TestRunArchiveAugmentsResearch adds archived documents to the research
// input and persists them.
func TestRunArchiveAugmentsResearch(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	m := testManifest(t, store, "Test Topic", string(run.SourceModeExplicit))
	runTestManifest(m, FormatArticle, false, articleURLs[:1])

	archived := testDocument("https://archive.example/old")
	archived.DocumentType = types.SourceTypeArchive
	docsSeen := []string{}
	o, _ := newOrchestrator(t, store, Config{
		Format:       FormatArticle,
		ExplicitURLs: articleURLs[:1],
		Fetcher:      &fakeFetcher{bodyFor: map[string]string{articleURLs[0]: "<html><body><p>x</p></body></html>"}},
		Research:     &fakeResearch{dossier: testDossier("Test Topic", articleURLs[0], "https://archive.example/old"), docsSeen: &docsSeen},
		Archive:      &fakeArchive{res: &archiverev.RetrieveResult{Documents: []types.Document{archived}}},
		Verify:       &fakeVerify{},
		Write:        &fakeWrite{artifact: testArtifact()},
		Check:        &fakeCheck{},
	})
	// The verify fake needs the full claim set the dossier carries.
	o.cfg.Verify.(*fakeVerify).res = testVerification(o.cfg.Research.(*fakeResearch).dossier)

	res, err := o.Run(ctx, m, false)
	if err != nil {
		t.Fatalf("run should succeed: %v", err)
	}
	if res.Status != run.RunStatusSucceeded {
		t.Fatalf("status = %s", res.Status)
	}
	// Research saw the live document plus the archived one.
	want := []string{articleURLs[0], "https://archive.example/old"}
	if len(docsSeen) != 2 || docsSeen[0] != want[0] || docsSeen[1] != want[1] {
		t.Errorf("research documents = %v, want %v", docsSeen, want)
	}
	// The archived document is persisted under the research stage.
	found := false
	for _, a := range res.Manifest.Artifacts {
		if strings.Contains(a.Path, "research/archive-") {
			found = true
		}
	}
	if !found {
		t.Error("archived document was not persisted")
	}
}

// TestRunArchiveFailureNeverFailsTheRun: archive problems degrade to a
// warning; the run continues on live sources.
func TestRunArchiveFailureNeverFailsTheRun(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	m := testManifest(t, store, "Test Topic", string(run.SourceModeExplicit))
	runTestManifest(m, FormatArticle, false, articleURLs[:1])

	o, _ := newOrchestrator(t, store, Config{
		Format:       FormatArticle,
		ExplicitURLs: articleURLs[:1],
		Fetcher:      &fakeFetcher{bodyFor: map[string]string{articleURLs[0]: "<html><body><p>x</p></body></html>"}},
		Research:     &fakeResearch{dossier: testDossier("Test Topic", articleURLs[0])},
		Archive:      &fakeArchive{err: os.ErrNotExist},
		Verify:       &fakeVerify{},
		Write:        &fakeWrite{artifact: testArtifact()},
		Check:        &fakeCheck{},
	})
	o.cfg.Verify.(*fakeVerify).res = testVerification(o.cfg.Research.(*fakeResearch).dossier)

	res, err := o.Run(ctx, m, false)
	if err != nil {
		t.Fatalf("archive failure must not fail the run: %v", err)
	}
	if res.Status != run.RunStatusSucceeded {
		t.Fatalf("status = %s", res.Status)
	}
}

// TestRunFollowUpRunsWhenEnabled executes the follow-up stage and persists
// its result.
func TestRunFollowUpRunsWhenEnabled(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	m := testManifest(t, store, "Test Topic", string(run.SourceModeDiscovered))
	runTestManifest(m, FormatArticle, true, nil)

	dossier := testDossier("Test Topic", "https://a.example/1")
	o, svcs := newOrchestrator(t, store, Config{
		Format:    FormatArticle,
		Discovery: &fakeDiscovery{res: &discovery.DiscoveryResult{Candidates: []discovery.Candidate{{CandidateURL: "https://a.example/1", BestRank: 1}}}},
		Fetcher:   &fakeFetcher{bodyFor: map[string]string{"https://a.example/1": "<html><body><p>x</p></body></html>"}},
		Research:  &fakeResearch{dossier: dossier},
		Verify:    &fakeVerify{},
		FollowUp:  &fakeFollowUp{},
		Write:     &fakeWrite{artifact: testArtifact()},
		Check:     &fakeCheck{},
	})
	svcs.verify.res = testVerification(dossier)

	res, err := o.Run(ctx, m, false)
	if err != nil {
		t.Fatalf("run should succeed: %v", err)
	}
	if got := stageStatus(t, res.Manifest, run.StageFollowUp); got != run.StageStatusSucceeded {
		t.Errorf("follow-up status = %s, want succeeded", got)
	}
	if !fileExists(t, store, m.ID, followUpResultPath) {
		t.Error("follow-up result was not persisted")
	}
	if svcs.followUp.calls != 1 {
		t.Errorf("follow-up called %d times, want 1", svcs.followUp.calls)
	}
}

// TestBuildStagesSnapshotWinsOverConfig: a run snapshot with follow-up
// disabled skips the stage even when the orchestrator has a follow-up service
// configured (the resume determinism rule).
func TestBuildStagesSnapshotWinsOverConfig(t *testing.T) {
	store := newTestStore(t)
	m := testManifest(t, store, "Test Topic", string(run.SourceModeExplicit))
	runTestManifest(m, FormatArticle, false, articleURLs[:1])

	// The manifest says follow-up is disabled, but the orchestrator carries a
	// follow-up service (as a resumed run would if the user re-passed the flag).
	o, _ := newOrchestrator(t, store, Config{
		Format:    FormatArticle,
		Discovery: &fakeDiscovery{},
		FollowUp:  &fakeFollowUp{},
	})
	stages := o.BuildStages(m)
	var fu runner.Stage
	for _, st := range stages {
		if st.Name == run.StageFollowUp {
			fu = st
		}
	}
	if fu.Fn != nil {
		t.Error("follow-up stage must be skipped when the run snapshot disables it")
	}
	if fu.SkipReason == "" {
		t.Error("skipped follow-up stage must carry a skip reason")
	}
}

// TestBuildStagesSnapshotEnablesFollowUp: the opposite — snapshot enabled,
// no service configured, still runs (the stage executor will fail cleanly).
func TestBuildStagesSnapshotEnablesFollowUp(t *testing.T) {
	store := newTestStore(t)
	m := testManifest(t, store, "Test Topic", string(run.SourceModeExplicit))
	runTestManifest(m, FormatArticle, true, articleURLs[:1])

	o, _ := newOrchestrator(t, store, Config{Format: FormatArticle})
	var fu runner.Stage
	for _, st := range o.BuildStages(m) {
		if st.Name == run.StageFollowUp {
			fu = st
		}
	}
	if fu.Fn == nil {
		t.Error("follow-up stage must run when the run snapshot enables it")
	}
}

// TestRunRenderRefusesWithoutPassingCheck is the second, independent gate:
// the render stage refuses to produce output when the persisted final check
// result is not a pass.
func TestRunRenderRefusesWithoutPassingCheck(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	m := testManifest(t, store, "Test Topic", string(run.SourceModeExplicit))
	runTestManifest(m, FormatArticle, false, articleURLs[:1])

	o, _ := newOrchestrator(t, store, Config{Format: FormatArticle})
	id := m.ID
	if _, err := store.CreateRun(m); err != nil {
		t.Fatalf("claiming run: %v", err)
	}
	runDir, err := store.RunDir(id)
	if err != nil {
		t.Fatalf("run dir: %v", err)
	}

	// Hand-place a FAILED check result and an artifact, then call the render
	// executor directly (the double-safety path a normal run never reaches).
	check := contracts.FactualCheckResult{
		StableID:                "check-x",
		CheckedAt:               testTime,
		OverallStatus:           contracts.FactualCheckStatusFail,
		ConfidenceLevel:         contracts.ConfidenceHigh,
		ClaimVerificationStatus: map[string]contracts.VerificationStatus{},
	}
	art := testArtifact()
	if err := putArtifact(t, store, id, finalCheckPath, check); err != nil {
		t.Fatal(err)
	}
	if err := putArtifact(t, store, id, artifactPath, art); err != nil {
		t.Fatal(err)
	}
	in := &runner.StageInput{RunID: id, Stage: run.StageRender, Manifest: m, Store: store, RunDir: runDir}
	if _, err := o.runRender(ctx, in); err == nil {
		t.Fatal("render must refuse when the persisted final check is a fail")
	} else if !strings.Contains(err.Error(), "refused") {
		t.Errorf("render error = %v, want the refusal message", err)
	}
}

// putArtifact persists a value under a run-relative path for gate tests.
func putArtifact(t *testing.T, store *workspace.Store, id run.RunID, rel string, v any) error {
	t.Helper()
	data, err := jsonMarshalIndent(v)
	if err != nil {
		return err
	}
	gateID := "gate-" + strings.NewReplacer("/", "-", ".", "-").Replace(rel)
	ref := run.ArtifactRef{Kind: run.ArtifactKindCustom, ID: gateID, Stage: "", Path: rel, ProducedAt: run.NewTimestamp(testTime)}
	if _, err := store.SaveArtifact(id, ref, data); err != nil {
		return err
	}
	return nil
}

// TestBuildSummaryFailedRun assembles the summary of a failed run: the final
// check status is reported and no output paths exist.
func TestBuildSummaryFailedRun(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	m := testManifest(t, store, "Miami Dolphins roster changes", string(run.SourceModeExplicit))
	runTestManifest(m, FormatArticle, false, articleURLs)

	o, svcs := newOrchestrator(t, store, Config{
		Format:       FormatArticle,
		ExplicitURLs: articleURLs,
		Fetcher: &fakeFetcher{
			bodyFor: map[string]string{articleURLs[0]: "<html><body><p>a</p></body></html>"},
			fail:    map[string]string{articleURLs[1]: "connection refused"},
		},
		Research: &fakeResearch{dossier: testDossier("t", articleURLs[0])},
		Verify:   &fakeVerify{},
		Write:    &fakeWrite{artifact: testArtifact()},
		Check:    &fakeCheck{status: contracts.FactualCheckStatusFail},
	})
	svcs.verify.res = testVerification(svcs.research.dossier)

	res, _ := o.Run(ctx, m, false)
	s, err := BuildSummary(store, res.Manifest)
	if err != nil {
		t.Fatalf("building summary: %v", err)
	}
	if s.Status != run.RunStatusFailed {
		t.Errorf("summary status = %s, want failed", s.Status)
	}
	if s.FinalCheck != string(contracts.FactualCheckStatusFail) {
		t.Errorf("summary final check = %q, want fail", s.FinalCheck)
	}
	if len(s.OutputPaths) != 0 {
		t.Errorf("summary output paths = %v, want none", s.OutputPaths)
	}
	if s.SourcesAttempted != 2 || s.SourcesFetched != 1 {
		t.Errorf("summary sources = %d/%d, want 1/2", s.SourcesFetched, s.SourcesAttempted)
	}
	// The printed summary must not claim output exists.
	str := s.String()
	if strings.Contains(str, "Output:") {
		t.Errorf("summary text claims output:\n%s", str)
	}
	if !strings.Contains(str, "fail") {
		t.Errorf("summary text should mention the failed check:\n%s", str)
	}
}

// TestBuildSummarySucceededRun covers the happy-path summary fields.
func TestBuildSummarySucceededRun(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	m := testManifest(t, store, "Miami Dolphins roster changes", string(run.SourceModeExplicit))
	runTestManifest(m, FormatHybrid, false, articleURLs)

	o, svcs := newOrchestrator(t, store, Config{
		Format:       FormatHybrid,
		ExplicitURLs: articleURLs,
		Fetcher: &fakeFetcher{bodyFor: map[string]string{
			articleURLs[0]: "<html><body><p>a</p></body></html>",
			articleURLs[1]: "<html><body><p>b</p></body></html>",
		}},
		Research: &fakeResearch{dossier: testDossier("t", articleURLs[0], articleURLs[1])},
		Verify:   &fakeVerify{},
		Write:    &fakeWrite{artifact: testArtifact()},
		Check:    &fakeCheck{},
	})
	svcs.verify.res = testVerification(svcs.research.dossier)

	res, err := o.Run(ctx, m, false)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	s, err := BuildSummary(store, res.Manifest)
	if err != nil {
		t.Fatalf("building summary: %v", err)
	}
	if s.Status != run.RunStatusSucceeded {
		t.Errorf("status = %s", s.Status)
	}
	if s.FinalCheck != string(contracts.FactualCheckStatusPass) {
		t.Errorf("final check = %q", s.FinalCheck)
	}
	// Hybrid format renders both outputs.
	if len(s.OutputPaths) != 2 {
		t.Errorf("output paths = %v, want 2 (hybrid renders article + thread)", s.OutputPaths)
	}
	if s.SourcesFetched != 2 || s.DocumentsExtracted != 2 {
		t.Errorf("sources=%d docs=%d, want 2/2", s.SourcesFetched, s.DocumentsExtracted)
	}
	if s.ClaimsTotal != 2 || s.ClaimsSupported != 2 || s.ClaimsUnresolved != 0 {
		t.Errorf("claims total/supported/unresolved = %d/%d/%d, want 2/2/0", s.ClaimsTotal, s.ClaimsSupported, s.ClaimsUnresolved)
	}
}

// TestRunThreadFormat renders a thread instead of an article.
func TestRunThreadFormat(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	m := testManifest(t, store, "Test Topic", string(run.SourceModeExplicit))
	runTestManifest(m, FormatThread, false, articleURLs[:1])

	artifact := testArtifact()
	artifact.ArtifactType = contracts.ArtifactTypeThread
	artifact.Posts = []contracts.Post{
		{Order: 0, Body: "First post of the thread."},
		{Order: 1, Body: "Second post of the thread."},
	}
	o, _ := newOrchestrator(t, store, Config{
		Format:       FormatThread,
		ExplicitURLs: articleURLs[:1],
		Fetcher:      &fakeFetcher{bodyFor: map[string]string{articleURLs[0]: "<html><body><p>x</p></body></html>"}},
		Research:     &fakeResearch{dossier: testDossier("Test Topic", articleURLs[0])},
		Verify:       &fakeVerify{},
		Write:        &fakeWrite{artifact: artifact},
		Check:        &fakeCheck{},
	})
	o.cfg.Verify.(*fakeVerify).res = testVerification(o.cfg.Research.(*fakeResearch).dossier)

	res, err := o.Run(ctx, m, false)
	if err != nil {
		t.Fatalf("run: %v (status=%s)", err, res.Status)
	}
	if !fileExists(t, store, m.ID, "output/thread.txt") {
		t.Error("thread output missing")
	}
	if fileExists(t, store, m.ID, "output/article.md") {
		t.Error("article output must not exist for a thread run")
	}
}

// TestNewValidation enforces the orchestrator's configuration contract.
func TestNewValidation(t *testing.T) {
	store := newTestStore(t)
	base := func() Config {
		return Config{
			Store:    store,
			Research: &fakeResearch{},
			Verify:   &fakeVerify{},
			Write:    &fakeWrite{},
			Check:    &fakeCheck{},
			Markdown: renderers.NewMarkdownRenderer(),
			Thread:   renderers.NewThreadRenderer(),
		}
	}
	if _, err := New(base()); err != nil {
		t.Errorf("a fully wired config must be accepted: %v", err)
	}
	c := base()
	c.Store = nil
	if _, err := New(c); err == nil {
		t.Error("missing store must be rejected")
	}
	c = base()
	c.Check = nil
	if _, err := New(c); err == nil {
		t.Error("missing check service must be rejected")
	}
	c = base()
	c.Format = "bogus"
	if _, err := New(c); err == nil {
		t.Error("unsupported format must be rejected")
	}
}

// TestFollowUpRequiresDiscovery: a follow-up service without discovery is a
// misconfiguration (follow-up research needs to find new sources).
func TestFollowUpRequiresDiscovery(t *testing.T) {
	store := newTestStore(t)
	cfg := Config{
		Store:    store,
		Research: &fakeResearch{},
		Verify:   &fakeVerify{},
		Write:    &fakeWrite{},
		Check:    &fakeCheck{},
		Markdown: renderers.NewMarkdownRenderer(),
		Thread:   renderers.NewThreadRenderer(),
		FollowUp: &fakeFollowUp{},
	}
	if _, err := New(cfg); err == nil {
		t.Error("follow-up without discovery must be rejected")
	}
}

// TestFetchFailureWarnings a partially failing fetch records warnings and
// continues with the successful sources.
func TestFetchFailureWarnings(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	m := testManifest(t, store, "Test Topic", string(run.SourceModeExplicit))
	runTestManifest(m, FormatArticle, false, articleURLs)

	o, _ := newOrchestrator(t, store, Config{
		Format:       FormatArticle,
		ExplicitURLs: articleURLs,
		Fetcher: &fakeFetcher{
			bodyFor: map[string]string{articleURLs[0]: "<html><body><p>a</p></body></html>"},
			fail:    map[string]string{articleURLs[1]: "connection refused"},
		},
		Research: &fakeResearch{dossier: testDossier("Test Topic", articleURLs[0])},
		Verify:   &fakeVerify{},
		Write:    &fakeWrite{artifact: testArtifact()},
		Check:    &fakeCheck{},
	})
	o.cfg.Verify.(*fakeVerify).res = testVerification(o.cfg.Research.(*fakeResearch).dossier)

	res, err := o.Run(ctx, m, false)
	if err != nil {
		t.Fatalf("run should succeed with one failed source: %v", err)
	}
	var hasWarning bool
	for _, w := range res.Manifest.Warnings {
		if w.Code == "source.failed" {
			hasWarning = true
		}
	}
	if !hasWarning {
		t.Error("expected a source.failed warning for the unreachable URL")
	}
}

// TestFetchAllFailFailsTheRun: with no documents at all, the run must fail.
func TestFetchAllFailFailsTheRun(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	m := testManifest(t, store, "Test Topic", string(run.SourceModeExplicit))
	runTestManifest(m, FormatArticle, false, articleURLs[:1])

	o, _ := newOrchestrator(t, store, Config{
		Format:       FormatArticle,
		ExplicitURLs: articleURLs[:1],
		Fetcher:      &fakeFetcher{fail: map[string]string{articleURLs[0]: "connection refused"}},
		Research:     &fakeResearch{dossier: testDossier("Test Topic", articleURLs[0])},
		Verify:       &fakeVerify{},
		Write:        &fakeWrite{artifact: testArtifact()},
		Check:        &fakeCheck{},
	})

	res, err := o.Run(ctx, m, false)
	if err == nil {
		t.Fatal("run should fail when every source fails")
	}
	if res.Status != run.RunStatusFailed {
		t.Fatalf("status = %s, want failed", res.Status)
	}
	if got := stageStatus(t, res.Manifest, run.StageFetch); got != run.StageStatusFailed {
		t.Errorf("fetch status = %s, want failed", got)
	}
}

// TestRunSupplementMode merges explicit URLs with discovered candidates.
func TestRunSupplementMode(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	m := testManifest(t, store, "Test Topic", string(run.SourceModeSupplement))
	runTestManifest(m, FormatArticle, false, articleURLs[:1])

	o, _ := newOrchestrator(t, store, Config{
		Format:       FormatArticle,
		ExplicitURLs: articleURLs[:1],
		Discovery:    &fakeDiscovery{res: &discovery.DiscoveryResult{Candidates: []discovery.Candidate{{CandidateURL: "https://found.example/x", BestRank: 1}}}},
		Fetcher: &fakeFetcher{bodyFor: map[string]string{
			articleURLs[0]:            "<html><body><p>a</p></body></html>",
			"https://found.example/x": "<html><body><p>f</p></body></html>",
		}},
		Research: &fakeResearch{dossier: testDossier("Test Topic", articleURLs[0], "https://found.example/x")},
		Verify:   &fakeVerify{},
		Write:    &fakeWrite{artifact: testArtifact()},
		Check:    &fakeCheck{},
	})
	o.cfg.Verify.(*fakeVerify).res = testVerification(o.cfg.Research.(*fakeResearch).dossier)

	res, err := o.Run(ctx, m, false)
	if err != nil {
		t.Fatalf("run: %v (status=%s)", err, res.Status)
	}
	var idx sourcesIndex
	data, err := store.LoadArtifact(m.ID, sourcesIndexPath)
	if err != nil {
		t.Fatalf("sources index: %v", err)
	}
	if err := jsonUnmarshal(data, &idx); err != nil {
		t.Fatalf("parse sources index: %v", err)
	}
	// Explicit first, then discovered.
	if len(idx.Selected) != 2 || idx.Selected[0] != articleURLs[0] || idx.Selected[1] != "https://found.example/x" {
		t.Errorf("selected = %v, want [explicit, discovered]", idx.Selected)
	}
}
