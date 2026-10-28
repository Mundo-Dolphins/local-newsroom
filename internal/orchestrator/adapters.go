package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
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
// Production adapters
//
// Thin converters between the orchestrator's stage-service shapes and the
// production components. No editorial logic lives here: each adapter
// translates inputs, calls the wrapped component, and translates outputs.
// ---------------------------------------------------------------------------

// NewFetchAdapter adapts the production fetcher client to the orchestrator's
// SourceFetcher.
func NewFetchAdapter(client *fetcher.Client) SourceFetcher {
	return fetchAdapter{client: client}
}

type fetchAdapter struct {
	client *fetcher.Client
}

func (a fetchAdapter) FetchMany(ctx context.Context, urls []string) []FetchedSource {
	items := a.client.FetchMany(ctx, urls)
	out := make([]FetchedSource, 0, len(items))
	for _, item := range items {
		src := FetchedSource{URL: item.URL, OK: item.Success}
		if item.Result != nil {
			src.Status = item.Result.HTTPStatus
			src.ContentType = item.Result.ContentType
			src.RawBody = item.Result.Body
			src.RetrievedAt = item.Result.RetrievedAt
		}
		if item.Error != nil {
			src.Error = item.Error.Error()
		}
		out = append(out, src)
	}
	return out
}

// NewArchiveAdapter adapts the archiverev client to the orchestrator's
// ArchiveService.
func NewArchiveAdapter(client *archiverev.Client) ArchiveService {
	return archiveAdapter{client: client}
}

type archiveAdapter struct {
	client *archiverev.Client
}

func (a archiveAdapter) Retrieve(ctx context.Context, topic string, liveURLs []string) (*archiverev.RetrieveResult, error) {
	return a.client.Retrieve(ctx, topic, liveURLs)
}

// NewExtractAdapter adapts the production HTML extractor to the
// orchestrator's SourceExtractor: it builds the extractor's input from the
// fetched source (source metadata plus raw body) and returns the normalized
// document.
func NewExtractAdapter(x *html.Extractor) SourceExtractor {
	return extractAdapter{extractor: x}
}

type extractAdapter struct {
	extractor *html.Extractor
}

func (a extractAdapter) Extract(_ context.Context, src FetchedSource) (*types.Document, error) {
	doc, err := a.extractor.Extract(extractor.Input{
		Source: types.Source{
			StableID:    src.URL,
			OriginalURL: src.URL,
			SourceType:  types.SourceTypeWeb,
			RetrievedAt: src.RetrievedAt,
			Metadata:    map[string]string{"content_type": src.ContentType, "http_status": strconv.Itoa(src.Status)},
		},
		Content: src.RawBody,
	})
	if err != nil {
		return nil, err
	}
	// Keep the retrieval identity: the document's source ID is the URL it
	// came from, so stages (and resumes) can trace documents to sources.
	if doc == nil {
		return nil, errors.New("extractor returned no document")
	}
	doc.SourceID = src.URL
	return doc, nil
}

// NewFollowUpService wires the follow-up loop (followup.FollowUp) with its
// per-round dependencies, returning the orchestrator's FollowUpService.
//
// The search provider is adapted from the search package's shape to the
// follow-up package's shape. Provider hits carry no native relevance score,
// so the adapter derives one from the provider's rank (rank 1 = 1.0, decaying
// 0.15 per rank step), letting the follow-up budget's minimum-relevance rule
// bound each round to the provider's top results.
func NewFollowUpService(
	engine *followup.FollowUp,
	plannerService Planner,
	searchProvider search.Provider,
	fetcherService SourceFetcher,
	extractorService SourceExtractor,
	researchService ResearchService,
) FollowUpService {
	return followUpService{
		engine:     engine,
		planner:    plannerAdapter{planner: plannerService},
		searcher:   followUpSearcher{provider: searchProvider},
		fetcher:    followUpFetcher{fetcher: fetcherService},
		extractor:  followUpExtractor{extractor: extractorService},
		researcher: followUpResearcher{research: researchService},
	}
}

type followUpService struct {
	engine     *followup.FollowUp
	planner    followup.Planner
	searcher   followup.Searcher
	fetcher    followup.Fetcher
	extractor  followup.Extractor
	researcher followup.ResearcherDef
}

func (s followUpService) Run(ctx context.Context, dossier *researcher.ResearchDossier, verification *contracts.VerificationResult) (*followup.FollowUpResult, error) {
	return s.engine.Run(ctx, dossier, verification, s.planner, s.searcher, s.fetcher, s.extractor, s.researcher)
}

// plannerAdapter implements followup.Planner over the orchestrator's Planner
// (identical Generate shape).
type plannerAdapter struct {
	planner Planner
}

func (a plannerAdapter) Generate(ctx context.Context, topic string, language string, hint *planner.TimeRangeHint) (*planner.SearchPlan, error) {
	return a.planner.Generate(ctx, topic, language, hint)
}

// followUpSearcher adapts a search.Provider to the follow-up Searcher.
type followUpSearcher struct {
	provider search.Provider
}

func (s followUpSearcher) Search(ctx context.Context, q planner.SearchQuery, _ int) (followup.SearchResult, error) {
	start := time.Now()
	req := search.Request{
		Query:     q.Query,
		Language:  q.Language,
		TimeRange: timeRangePreset(q.TimeRange),
		Limit:     10,
	}
	resp, err := s.provider.Search(ctx, req)
	if err != nil {
		return followup.SearchResult{}, err
	}
	hits := make([]followup.SearchHit, 0, len(resp.Results))
	for _, r := range resp.Results {
		h := followup.SearchHit{
			URL:         r.URL,
			Relevance:   rankRelevance(r.Rank),
			PublishedAt: r.PublishedAt,
		}
		if r.Title != nil {
			h.Title = *r.Title
		}
		if r.Snippet != nil {
			h.Snippet = *r.Snippet
		}
		hits = append(hits, h)
	}
	return followup.SearchResult{
		Query:        q.Query,
		Hits:         hits,
		TotalResults: len(hits),
		ElapsedTime:  time.Since(start),
	}, nil
}

// timeRangePreset converts a planner time-range hint into the SearXNG
// time_range preset. An empty hint yields no preset (no recency filter).
func timeRangePreset(hint *planner.TimeRangeHint) string {
	if hint == nil {
		return ""
	}
	layout := "2006-01-02"
	start, err1 := time.Parse(layout, hint.StartDate)
	end, err2 := time.Parse(layout, hint.EndDate)
	if err1 != nil && err2 != nil {
		return ""
	}
	if err1 != nil {
		start = time.Now().Add(-24 * time.Hour)
	}
	if err2 != nil {
		end = time.Now()
	}
	switch days := int(end.Sub(start).Hours() / 24); {
	case days <= 1:
		return "day"
	case days <= 7:
		return "week"
	case days <= 31:
		return "month"
	default:
		return "year"
	}
}

// rankRelevance derives a deterministic relevance score from a provider rank:
// rank 1 = 1.0, decaying 0.15 per rank step, floored at 0.
func rankRelevance(rank int) float64 {
	if rank < 1 {
		rank = 1
	}
	v := 1.0 - 0.15*float64(rank-1)
	if v < 0 {
		v = 0
	}
	return v
}

// followUpFetcher adapts the orchestrator's batch fetcher to the follow-up's
// single-URL fetcher.
type followUpFetcher struct {
	fetcher SourceFetcher
}

func (f followUpFetcher) Fetch(ctx context.Context, u string) (followup.FetchResult, error) {
	results := f.fetcher.FetchMany(ctx, []string{u})
	if len(results) != 1 {
		return followup.FetchResult{}, fmt.Errorf("follow-up fetch: expected exactly one result, got %d", len(results))
	}
	src := results[0]
	if !src.OK {
		return followup.FetchResult{URL: u}, fmt.Errorf("fetching %s: %s", u, src.Error)
	}
	return followup.FetchResult{
		URL:         u,
		Body:        src.RawBody,
		ContentType: src.ContentType,
		HTTPStatus:  src.Status,
		Headers:     http.Header{},
		RetrievedAt: src.RetrievedAt,
	}, nil
}

// followUpExtractor adapts the orchestrator's extractor to the follow-up's
// extractor.
type followUpExtractor struct {
	extractor SourceExtractor
}

func (e followUpExtractor) Extract(ctx context.Context, fr followup.FetchResult) (*types.Document, error) {
	return e.extractor.Extract(ctx, FetchedSource{
		URL:         fr.URL,
		Status:      fr.HTTPStatus,
		ContentType: fr.ContentType,
		OK:          true,
		RawBody:     fr.Body,
		RetrievedAt: fr.RetrievedAt,
	})
}

// followUpResearcher implements followup.ResearcherDef over the
// orchestrator's ResearchService (identical Generate shape).
type followUpResearcher struct {
	research ResearchService
}

func (r followUpResearcher) Generate(ctx context.Context, topic string, documents []types.Document) (*researcher.ResearchDossier, error) {
	return r.research.Generate(ctx, topic, documents)
}

// NewWriteAdapter adapts the production writer to the orchestrator's
// WriteService. The editorial profile (when loaded) is already baked into the
// writer's configuration, so the adapter simply delegates. The adapter also
// implements PlannedWriteService (v0.6 editorial recipes): the writer's own
// GenerateWithPlan receives the recipe's content plan.
func NewWriteAdapter(w *writer.Writer) WriteService {
	return writeAdapter{writer: w}
}

type writeAdapter struct {
	writer *writer.Writer
}

func (a writeAdapter) Generate(ctx context.Context, verification *contracts.VerificationResult) (*contracts.EditorialArtifact, error) {
	return a.writer.Generate(ctx, verification, nil)
}

// GenerateWithPlan implements PlannedWriteService.
func (a writeAdapter) GenerateWithPlan(ctx context.Context, verification *contracts.VerificationResult, plan *contracts.ContentPlan) (*contracts.EditorialArtifact, error) {
	return a.writer.GenerateWithPlan(ctx, verification, plan, nil)
}

// NewPlannerClient bridges the shared LLM client to the planner's own Client
// interface: the planner package defines its own request and response types
// (same fields, different package), so the shared llm.Client must be adapted.
func NewPlannerClient(client llm.Client, model string) planner.Client {
	return llmPlannerAdapter{client: client, model: model}
}

type llmPlannerAdapter struct {
	client llm.Client
	model  string
}

func (a llmPlannerAdapter) Complete(ctx context.Context, req planner.Request) (planner.Response, error) {
	if req.Model == "" {
		req.Model = a.model
	}
	llmResp, err := a.client.Complete(ctx, llm.Request{
		SystemPrompt:    req.SystemPrompt,
		UserPrompt:      req.UserPrompt,
		Model:           req.Model,
		Temperature:     req.Temperature,
		MaxOutputTokens: req.MaxOutputTokens,
	})
	if err != nil {
		return planner.Response{}, err
	}
	return planner.Response{Content: llmResp.Content}, nil
}
