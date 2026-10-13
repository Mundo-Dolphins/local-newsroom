package orchestrator

// Unit tests for the production adapters (adapters.go). The adapters are the
// thin seam between the orchestrator's stage-service interfaces and the
// real components, so they are tested with the real components plus local
// fakes for the external services (HTTP LLM, HTTP sources, search provider).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/archiverev"
	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/extractor"
	"github.com/Mundo-Dolphins/local-newsroom/internal/extractor/html"
	"github.com/Mundo-Dolphins/local-newsroom/internal/fetcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/followup"
	"github.com/Mundo-Dolphins/local-newsroom/internal/llm"
	"github.com/Mundo-Dolphins/local-newsroom/internal/planner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/researcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/search"
	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
	"github.com/Mundo-Dolphins/local-newsroom/internal/writer"
)

// ---------------------------------------------------------------------------
// Pure helper functions
// ---------------------------------------------------------------------------

func TestRankRelevance(t *testing.T) {
	cases := []struct {
		rank int
		want float64
	}{
		{0, 1.0}, // floored to rank 1
		{1, 1.0},
		{2, 0.85},
		{8, 0.0},  // 1.0 - 7*0.15 = -0.05 -> floored to 0
		{7, 0.10}, // 1.0 - 6*0.15
		{100, 0.0},
	}
	for _, c := range cases {
		got := rankRelevance(c.rank)
		if got < c.want-1e-9 || got > c.want+1e-9 {
			t.Errorf("rankRelevance(%d) = %v, want %v", c.rank, got, c.want)
		}
	}
}

func TestTimeRangePreset(t *testing.T) {
	if got := timeRangePreset(nil); got != "" {
		t.Errorf("nil hint = %q, want empty", got)
	}
	if got := timeRangePreset(&planner.TimeRangeHint{StartDate: "nope", EndDate: "also-nope"}); got != "" {
		t.Errorf("unparseable dates = %q, want empty", got)
	}
	now := time.Now()
	day := func(offset int) string { return now.AddDate(0, 0, offset).Format("2006-01-02") }
	cases := []struct {
		hint *planner.TimeRangeHint
		want string
	}{
		{&planner.TimeRangeHint{StartDate: day(-1), EndDate: day(0)}, "day"},
		{&planner.TimeRangeHint{StartDate: day(-7), EndDate: day(0)}, "week"},
		{&planner.TimeRangeHint{StartDate: day(-31), EndDate: day(0)}, "month"},
		{&planner.TimeRangeHint{StartDate: day(-365), EndDate: day(0)}, "year"},
		{&planner.TimeRangeHint{StartDate: day(-366), EndDate: day(0)}, "year"},
		{&planner.TimeRangeHint{EndDate: day(0)}, "day"},       // no start: last 24h
		{&planner.TimeRangeHint{StartDate: day(-30)}, "month"}, // no end: now
	}
	for i, c := range cases {
		if got := timeRangePreset(c.hint); got != c.want {
			t.Errorf("case %d: timeRangePreset(%v) = %q, want %q", i, c.hint, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Fetch adapter (real fetcher client, local article server)
// ---------------------------------------------------------------------------

func TestFetchAdapter(t *testing.T) {
	articles := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><head><title>One</title></head><body><p>Body text.</p></body></html>"))
	}))
	defer articles.Close()

	a := NewFetchAdapter(fetcher.NewDefaultClient())
	results := a.FetchMany(context.Background(), []string{articles.URL + "/one", articles.URL + "/missing"})

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	ok := results[0]
	if !ok.OK || ok.Status != 200 || ok.URL != articles.URL+"/one" {
		t.Errorf("ok source = %+v", ok)
	}
	if len(ok.RawBody) == 0 || ok.ContentType == "" {
		t.Errorf("ok source should carry body and content type: %+v", ok)
	}
	if ok.RetrievedAt.IsZero() {
		t.Error("ok source missing retrieved_at")
	}
	bad := results[1]
	if bad.OK {
		t.Errorf("missing source should not be OK: %+v", bad)
	}
	if bad.Error == "" {
		t.Error("missing source should carry an error message")
	}
}

// ---------------------------------------------------------------------------
// Extract adapter (real html extractor)
// ---------------------------------------------------------------------------

func TestExtractAdapter(t *testing.T) {
	a := NewExtractAdapter(html.New(extractor.Config{}))
	src := FetchedSource{
		URL:         "http://x/one",
		ContentType: "text/html",
		Status:      200,
		OK:          true,
		RawBody:     []byte("<html><head><title>One</title></head><body><p>Hello world.</p></body></html>"),
	}
	doc, err := a.Extract(context.Background(), src)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if doc.SourceID != "http://x/one" {
		t.Errorf("document source id = %q, want the URL", doc.SourceID)
	}
	if len(doc.PlainText) == 0 {
		t.Error("document should carry extracted text")
	}

	// Empty body must produce an error, not a document.
	bad := FetchedSource{URL: "http://x/empty", ContentType: "text/html", Status: 200, OK: true}
	if _, err := a.Extract(context.Background(), bad); err == nil {
		t.Error("empty body should fail extraction")
	}
}

// ---------------------------------------------------------------------------
// Planner client bridge (real llm client, local OpenAI-compatible server)
// ---------------------------------------------------------------------------

// fakeOpenAIServer is a minimal /chat/completions server.
func fakeOpenAIServer(t *testing.T, content string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"role": "assistant", "content": content}},
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPlannerClientBridge(t *testing.T) {
	srv := fakeOpenAIServer(t, `{"original_topic":"t","queries":[{"query":"q1","purpose":"primary"}]}`)
	client := llm.NewClient(llm.Config{BaseURL: srv.URL, APIKey: "k", Timeout: 5 * time.Second})
	bridge := NewPlannerClient(client, "default-model")
	resp, err := bridge.Complete(context.Background(), planner.Request{
		SystemPrompt: "sys",
		UserPrompt:   "user",
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Content == "" {
		t.Error("bridge should return the model content")
	}
}

// ---------------------------------------------------------------------------
// Write adapter (real writer, canned LLM)
// ---------------------------------------------------------------------------

func TestWriteAdapter(t *testing.T) {
	vres := &contracts.VerificationResult{
		StableID:   "ver-1",
		VerifiedAt: time.Now().UTC(),
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-1": contracts.VerificationStatusSupported,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-1": {ClaimID: "claim-1", Statement: "S", VerificationStatus: contracts.VerificationStatusSupported, ConfidenceLevel: contracts.ConfidenceHigh},
		},
	}
	artifactJSON := `{"stable_id":"art-1","artifact_type":"article","title":"T","body":"B","claim_references":{"claim-1":{"claim_id":"claim-1","usage_type":"core"}},"generation_metadata":{"input_verification_id":"ver-1","generated_at":"2025-06-15T12:00:00Z"}}`
	srv := fakeOpenAIServer(t, artifactJSON)
	client := llm.NewClient(llm.Config{BaseURL: srv.URL, APIKey: "k", Timeout: 5 * time.Second})
	w, err := writer.New(client, "m", writer.WriterConfig{
		PromptOverride: "You are a writer. Respond with JSON only.",
		ArtifactType:   string(contracts.ArtifactTypeArticle),
		TimeNow:        func() time.Time { return time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	a := NewWriteAdapter(w)
	out, err := a.Generate(context.Background(), vres)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if out.Title != "T" || out.Body != "B" {
		t.Errorf("artifact = %+v", out)
	}
}

// ---------------------------------------------------------------------------
// Archive adapter
// ---------------------------------------------------------------------------

func TestArchiveAdapterDisabledStore(t *testing.T) {
	a := NewArchiveAdapter(archiverev.New(archiverev.Config{})) // nil store
	res, err := a.Retrieve(context.Background(), "topic", []string{"http://x"})
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if res.HasArchive {
		t.Error("a store-less client must not claim archive hits")
	}
}

// ---------------------------------------------------------------------------
// Follow-up service adapters
// ---------------------------------------------------------------------------

func TestFollowUpSearcher(t *testing.T) {
	t1 := "Title one"
	s2 := "Snippet two"
	results := []search.SearchResult{
		{URL: "http://a", Title: &t1, Rank: 1},
		{URL: "http://b", Snippet: &s2, Rank: 2},
	}
	s := followUpSearcher{provider: &fakeSearchProvider{res: &search.Response{Results: results}}}
	res, err := s.Search(context.Background(), planner.SearchQuery{Query: "q"}, 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Hits) != 2 {
		t.Fatalf("got %d hits, want 2", len(res.Hits))
	}
	if res.Hits[0].Relevance != 1.0 || res.Hits[1].Relevance != 0.85 {
		t.Errorf("relevance mapping wrong: %+v", res.Hits)
	}
	if res.Hits[0].Title != "Title one" || res.Hits[1].Snippet != "Snippet two" {
		t.Errorf("pointer fields not flattened: %+v", res.Hits)
	}
	if res.TotalResults != 2 || res.Query != "q" {
		t.Errorf("result envelope wrong: %+v", res)
	}
	if res.ElapsedTime < 0 {
		t.Error("elapsed time must not be negative")
	}
}

func TestFollowUpSearcherPropagatesProviderError(t *testing.T) {
	s := followUpSearcher{provider: &fakeSearchProvider{err: context.DeadlineExceeded}}
	if _, err := s.Search(context.Background(), planner.SearchQuery{Query: "q"}, 10); err == nil {
		t.Error("provider error should propagate")
	}
}

func TestFollowUpFetcher(t *testing.T) {
	f := followUpFetcher{fetcher: fetcherStub{}}
	res, err := f.Fetch(context.Background(), "http://a")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if string(res.Body) != "body-a" || res.HTTPStatus != 200 || res.ContentType != "text/html" {
		t.Errorf("result = %+v", res)
	}

	// A failing fetch must surface the source error.
	fBad := followUpFetcher{fetcher: fetcherStub{fail: true}}
	if _, err := fBad.Fetch(context.Background(), "http://bad"); err == nil {
		t.Error("failed fetch should return an error")
	}
}

// fetcherStub is a SourceFetcher with one canned result.
type fetcherStub struct{ fail bool }

func (f fetcherStub) FetchMany(_ context.Context, urls []string) []FetchedSource {
	if len(urls) != 1 {
		panic("single-URL stub")
	}
	if f.fail {
		return []FetchedSource{{URL: urls[0], OK: false, Error: "boom"}}
	}
	return []FetchedSource{{URL: urls[0], OK: true, Status: 200, ContentType: "text/html", RawBody: []byte("body-a"), RetrievedAt: time.Now()}}
}

func TestFollowUpExtractor(t *testing.T) {
	e := followUpExtractor{extractor: NewExtractAdapter(html.New(extractor.Config{}))}
	fr := followup.FetchResult{URL: "http://a", HTTPStatus: 200, ContentType: "text/html", Body: []byte("<html><body><p>Hi.</p></body></html>")}
	doc, err := e.Extract(context.Background(), fr)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if doc.SourceID != "http://a" {
		t.Errorf("source id = %q", doc.SourceID)
	}
}

func TestFollowUpResearcher(t *testing.T) {
	docs := []types.Document{{SourceID: "http://a", PlainText: "text"}}
	r := followUpResearcher{research: researchStub{}}
	dossier, err := r.Generate(context.Background(), "topic", docs)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if dossier == nil || len(dossier.Claims) != 1 {
		t.Errorf("dossier = %+v", dossier)
	}
}

// researchStub is a ResearchService with one canned claim.
type researchStub struct{}

func (researchStub) Generate(_ context.Context, topic string, _ []types.Document) (*researcher.ResearchDossier, error) {
	return &researcher.ResearchDossier{
		StableID:    "dossier-x",
		Topic:       topic,
		GeneratedAt: time.Now(),
		Claims: []researcher.Claim{{
			ID:        "claim-x",
			Statement: "S",
		}},
	}, nil
}

// TestFollowUpServiceRun exercises the fully wired follow-up service (real
// followup engine) on a dossier whose claims are all supported: the engine
// has nothing to resolve and returns without new documents.
func TestFollowUpServiceRun(t *testing.T) {
	engine := followup.New(followup.FollowUpConfig{
		MaxRounds:          1,
		MaxQueriesPerClaim: 3,
		MaxQueriesPerRound: 10,
		MaxSourcesPerRound: 2,
		SourceBudget:       5,
	})
	planJSON := `{"original_topic":"t","queries":[{"query":"q1","purpose":"primary"}]}`
	srv := fakeOpenAIServer(t, planJSON)
	client := llm.NewClient(llm.Config{BaseURL: srv.URL, APIKey: "k", Timeout: 5 * time.Second})

	plannerService, err := planner.New(NewPlannerClient(client, "m"), "m", planner.Config{})
	if err != nil {
		t.Fatalf("planner: %v", err)
	}
	svc := NewFollowUpService(
		engine,
		plannerService,
		&fakeSearchProvider{res: &search.Response{Results: []search.SearchResult{{URL: "http://a", Rank: 1}}}},
		fetcherStub{},
		NewExtractAdapter(html.New(extractor.Config{})),
		researchStub{},
	)
	dossier := &researcher.ResearchDossier{
		StableID:    "dossier-x",
		Topic:       "topic",
		GeneratedAt: time.Now(),
		Claims: []researcher.Claim{{
			ID:         "claim-1",
			Statement:  "S",
			Confidence: researcher.ConfidenceHigh,
		}},
	}
	verification := &contracts.VerificationResult{
		StableID:   "ver-1",
		VerifiedAt: time.Now(),
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-1": contracts.VerificationStatusSupported,
		},
	}
	res, err := svc.Run(context.Background(), dossier, verification)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res == nil {
		t.Fatal("nil follow-up result")
	}
}
