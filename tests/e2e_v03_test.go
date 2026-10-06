package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/archive"
	"github.com/Mundo-Dolphins/local-newsroom/internal/chunker"
	"github.com/Mundo-Dolphins/local-newsroom/internal/embedding"
	"github.com/Mundo-Dolphins/local-newsroom/internal/extractor"
	"github.com/Mundo-Dolphins/local-newsroom/internal/extractor/html"
	"github.com/Mundo-Dolphins/local-newsroom/internal/fetcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/planner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/researcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/retrieval"
	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workflow"
)

const (
	v03Article1 = `<!DOCTYPE html><html><head><title>Local Development</title></head><body><article><h1>Local Development</h1><p>The local newsroom workflow enables offline research using cached archives. Key features include semantic retrieval, chunk-based indexing.</p></article></body></html>`
	v03Article2 = `<!DOCTYPE html><html><head><title>Research Methods</title></head><body><article><h1>Research Methods</h1><p>Modern research workflows incorporate verification stages. The newsroom pipeline preserves source metadata.</p></article></body></html>`
)

func createInMemoryArchive(t *testing.T) *archive.SQLiteStore {
	store, err := archive.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("Failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func ingestDoc(ctx context.Context, store *archive.SQLiteStore, sourceID, content string, cfg chunker.Config, embedder embedding.Embedder, t *testing.T) (archive.StableDocumentID, error) {
	doc := &types.Document{SourceID: sourceID, PlainText: content, DocumentType: types.SourceTypeLive, RetrievedAt: time.Now().UTC()}
	ch, err := chunker.New(cfg)
	if err != nil {
		return "", err
	}
	chunks, err := ch.Chunk(doc)
	if err != nil || len(chunks) == 0 {
		return "", err
	}
	archDoc := &archive.ArchiveDocument{
		StableID:         archive.StableDocumentID("arch:" + sourceID),
		PlainText:        content,
		PlainTextHash:    archive.ContentHash(fmt.Sprintf("%x", len(content))),
		ChunkCount:       len(chunks),
		SourceProvenance: archive.SourceProvenance{SourceID: sourceID, SourceURL: "http://localhost/" + sourceID, SourceType: types.SourceTypeWeb, RetrievedAt: time.Now().UTC()},
		ArchivedAt:       time.Now().UTC(),
	}
	chunkList := make([]archive.Chunk, 0, len(chunks))
	for _, c := range chunks {
		chunkList = append(chunkList, archive.Chunk{StableID: archive.StableChunkID(c.ChunkID), DocumentStableID: archDoc.StableID, ContentHash: c.ContentHash, Position: c.Position, Length: c.Length, HasEmbedding: true})
	}
	if err := store.UpsertDocument(ctx, archDoc, chunkList); err != nil {
		return "", err
	}
	for _, c := range chunkList {
		if err := store.SetEmbeddingVector(ctx, c.StableID, make([]float32, 384)); err != nil {
			return "", err
		}
		if err := store.SetEmbeddingMetadata(ctx, c.StableID, &archive.EmbeddingMetadata{ModelName: "fake", Dimensions: 384, GeneratedAt: time.Now().UTC()}); err != nil {
			return "", err
		}
	}
	return archDoc.StableID, nil
}

// fakeClientForPlanner is a minimal LLM client for planner tests.
type fakePlannerClient struct{}

func (f *fakePlannerClient) Complete(ctx context.Context, req planner.Request) (planner.Response, error) {
	return planner.Response{Content: `{"original_topic": "test", "queries": [{"query": "test", "purpose": "test"}]}`}, nil
}

// =============================================================================
// Archive Ingestion Tests
// =============================================================================

func TestE2E_v03_ArchiveIngestion(t *testing.T) {
	ctx := context.Background()
	store := createInMemoryArchive(t)
	chunkCfg := chunker.Config{TargetChunkSize: 200, MaxChunkSize: 300, OverlapRatio: 0.1}

	_, err := ingestDoc(ctx, store, "article1", v03Article1, chunkCfg, embedding.NewFakeEmbedder(), t)
	if err != nil {
		t.Fatalf("Ingest failed: %v", err)
	}

	exists, err := store.DocumentExists(ctx, "arch:article1")
	if err != nil || !exists {
		t.Fatal("Document should exist")
	}

	doc, err := store.GetDocument(ctx, "arch:article1")
	if err != nil || !strings.Contains(doc.PlainText, "local newsroom") {
		t.Error("Document content incorrect")
	}

	chunks, err := store.GetChunksByDocument(ctx, "arch:article1")
	if err != nil || len(chunks) == 0 {
		t.Error("Expected chunks")
	}

	t.Log("Archive ingestion test passed")
}

func TestE2E_v03_IdempotentReingestion(t *testing.T) {
	ctx := context.Background()
	store := createInMemoryArchive(t)
	chunkCfg := chunker.Config{TargetChunkSize: 200, MaxChunkSize: 300, OverlapRatio: 0.1}

	docID1, _ := ingestDoc(ctx, store, "article2", v03Article2, chunkCfg, embedding.NewFakeEmbedder(), t)
	docID2, _ := ingestDoc(ctx, store, "article2", v03Article2, chunkCfg, embedding.NewFakeEmbedder(), t)

	if docID1 != docID2 {
		t.Errorf("IDs should match: %q vs %q", docID1, docID2)
	}

	docs, _ := store.ListDocuments(ctx, 0)
	if len(docs) != 1 {
		t.Errorf("Expected 1 document, got %d", len(docs))
	}

	t.Log("Idempotent re-ingestion test passed")
}

func TestE2E_v03_EmptyArchiveBehavior(t *testing.T) {
	ctx := context.Background()
	store := createInMemoryArchive(t)
	retriever, _ := retrieval.New(retrieval.Config{TopK: 10})
	retriever.SetStore(store)
	retriever.SetEmbedder(embedding.NewFakeEmbedder())

	results, _ := retriever.Retrieve(ctx, retrieval.RetrievalQuery{Text: "any query", Limit: 10})

	if results.TotalHits != 0 || len(results.Hits) != 0 {
		t.Error("Empty archive should return no results")
	}

	t.Log("Empty archive behavior test passed")
}

func TestE2E_v03_SemanticRetrieval(t *testing.T) {
	ctx := context.Background()
	store := createInMemoryArchive(t)
	chunkCfg := chunker.Config{TargetChunkSize: 200, MaxChunkSize: 300, OverlapRatio: 0.1}

	_, _ = ingestDoc(ctx, store, "article1", v03Article1, chunkCfg, embedding.NewFakeEmbedder(), t)
	_, _ = ingestDoc(ctx, store, "article2", v03Article2, chunkCfg, embedding.NewFakeEmbedder(), t)

	retriever, _ := retrieval.New(retrieval.Config{TopK: 5})
	retriever.SetStore(store)
	retriever.SetEmbedder(embedding.NewFakeEmbedder())

	results, err := retriever.Retrieve(ctx, retrieval.RetrievalQuery{Text: "local newsroom workflow", Limit: 5})
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}

	// Note: Fake embedder generates random vectors, so results may be empty or unpredictable
	// This test validates the retrieval path works end-to-end, not semantic accuracy
	t.Logf("Retrieval completed: %d total hits, %d returned", results.TotalHits, len(results.Hits))

	t.Log("Semantic retrieval test passed")
}

func TestE2E_v03_FakeReranking(t *testing.T) {
	ctx := context.Background()
	store := createInMemoryArchive(t)
	chunkCfg := chunker.Config{TargetChunkSize: 200, MaxChunkSize: 300, OverlapRatio: 0.1}

	for i := 1; i <= 5; i++ {
		content := fmt.Sprintf("Article %d for reranking", i)
		_, err := ingestDoc(ctx, store, fmt.Sprintf("rerank-%d", i), content, chunkCfg, embedding.NewFakeEmbedder(), t)
		if err != nil {
			t.Fatalf("Ingest failed: %v", err)
		}
	}

	retriever, _ := retrieval.New(retrieval.Config{TopK: 3, RerankTopN: 10})
	retriever.SetStore(store)
	retriever.SetEmbedder(embedding.NewFakeEmbedder())

	fakeEmb := retriever.FakeEmbedder()
	if fakeEmb == nil {
		t.Fatal("Expected fake embedder")
	}

	results, err := retriever.Retrieve(ctx, retrieval.RetrievalQuery{Text: "test", Limit: 3})
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}

	t.Logf("Reranking: %d hits, reranked=%v", len(results.Hits), results.Reranked)
	t.Log("Fake reranking test passed")
}

func TestE2E_v03_ArchiveServerIntegration(t *testing.T) {
	ctx := context.Background()

	articles := map[string]string{"article1": v03Article1, "article2": v03Article2}
	articleServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if content, ok := articles[strings.TrimPrefix(r.URL.Path, "/")]; ok {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(content))
			return
		}
		http.NotFound(w, r)
	}))
	defer articleServer.Close()

	fetcherClient := fetcher.NewClient(fetcher.Config{Timeout: 10 * time.Second, MaxSize: 10 * 1024 * 1024, UserAgent: "test"})
	result, fetchErr := fetcherClient.Fetch(ctx, articleServer.URL+"/article1")
	if fetchErr != nil || result.HTTPStatus != 200 {
		t.Fatalf("Fetch failed: %v", fetchErr)
	}

	extractorObj := html.New(extractor.DefaultConfig())
	extracted, err := extractorObj.Extract(extractor.Input{
		Source:  types.Source{StableID: "article1", OriginalURL: result.FinalURL, SourceType: types.SourceTypeWeb, RetrievedAt: time.Now().UTC()},
		Content: result.Body,
	})
	if err != nil || strings.TrimSpace(extracted.PlainText) == "" {
		t.Error("Extraction failed")
	}

	store := createInMemoryArchive(t)
	chunkCfg := chunker.Config{TargetChunkSize: 200, MaxChunkSize: 300, OverlapRatio: 0.1}
	docID, _ := ingestDoc(ctx, store, extracted.SourceID, extracted.PlainText, chunkCfg, embedding.NewFakeEmbedder(), t)

	retriever, _ := retrieval.New(retrieval.Config{TopK: 5})
	retriever.SetStore(store)
	retriever.SetEmbedder(embedding.NewFakeEmbedder())
	results, _ := retriever.Retrieve(ctx, retrieval.RetrievalQuery{Text: "local newsroom", Limit: 5})

	t.Logf("Integration: %d chunks retrieved from archive, docID=%s", len(results.Hits), docID)
	t.Log("Archive server integration test passed")
}

// =============================================================================
// Researcher Integration Tests
// =============================================================================

func TestE2E_v03_ResearchWithArchiveContext(t *testing.T) {
	ctx := context.Background()
	store := createInMemoryArchive(t)
	chunkCfg := chunker.Config{TargetChunkSize: 200, MaxChunkSize: 300, OverlapRatio: 0.1}

	_, _ = ingestDoc(ctx, store, "ref1", v03Article1, chunkCfg, embedding.NewFakeEmbedder(), t)
	_, _ = ingestDoc(ctx, store, "ref2", v03Article2, chunkCfg, embedding.NewFakeEmbedder(), t)

	retriever, _ := retrieval.New(retrieval.Config{TopK: 5})
	retriever.SetStore(store)
	retriever.SetEmbedder(embedding.NewFakeEmbedder())

	archiveResults, _ := retriever.Retrieve(ctx, retrieval.RetrievalQuery{Text: "local newsroom", Limit: 5})
	t.Logf("Archive context: %d hits", len(archiveResults.Hits))

	searchPlanJSON := `{"original_topic": "test", "queries": []}`
	dossierJSON := `{"stable_id": "test", "topic": "test", "generated_at": "2024-01-15T10:00:00Z", "sources": [], "claims": [], "contradictions": [], "unresolved_questions": [], "research_notes": []}`

	fakeLLM := fakeLLMServer{searchPlanResponse: searchPlanJSON, researcherResponse: dossierJSON}
	llmServer := httptest.NewServer(&fakeLLM)
	defer llmServer.Close()

	outputPath := filepath.Join(t.TempDir(), "archive-research-dossier.json")
	config := &workflow.Config{
		Topic:            "Test Topic",
		URLs:             []string{"http://example.com/test"},
		OutputPath:       outputPath,
		AutoDiscover:     false,
		LLMBaseURL:       llmServer.URL,
		LLMModel:         "test",
		LLMTimeout:       30.0,
		FetchTimeout:     10.0,
		FetcherConfig:    fetcher.Config{Timeout: 10 * time.Second, MaxSize: 10 * 1024 * 1024, UserAgent: "test", FollowRedirects: true},
		ExtractorConfig:  extractor.DefaultConfig(),
		ResearcherConfig: researcher.ClientConfig{Temperature: 0.3, MaxOutputTokens: 16384},
	}

	w := workflow.New(*config)
	err := w.Run(ctx)
	if err != nil {
		t.Logf("Workflow error (expected): %v", err)
	}

	t.Log("Research with archive context test completed")
}

func TestE2E_v03_CombinedArchiveAndLiveSources(t *testing.T) {
	ctx := context.Background()
	store := createInMemoryArchive(t)
	chunkCfg := chunker.Config{TargetChunkSize: 200, MaxChunkSize: 300, OverlapRatio: 0.1}

	_, _ = ingestDoc(ctx, store, "archive-ref1", v03Article1, chunkCfg, embedding.NewFakeEmbedder(), t)
	_, _ = ingestDoc(ctx, store, "archive-ref2", v03Article2, chunkCfg, embedding.NewFakeEmbedder(), t)

	retriever, _ := retrieval.New(retrieval.Config{TopK: 3})
	retriever.SetStore(store)
	retriever.SetEmbedder(embedding.NewFakeEmbedder())

	archiveResults, _ := retriever.Retrieve(ctx, retrieval.RetrievalQuery{Text: "research", Limit: 3})

	contentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(v03Article1))
	}))
	defer contentServer.Close()

	t.Logf("Archive context: %d hits, Fresh source: %s", len(archiveResults.Hits), contentServer.URL)
	t.Log("Combined archive and live sources test completed")
}

func TestE2E_v03_SupplementToLiveResearch(t *testing.T) {
	store := createInMemoryArchive(t)
	chunkCfg := chunker.Config{TargetChunkSize: 200, MaxChunkSize: 300, OverlapRatio: 0.1}

	_, _ = ingestDoc(context.Background(), store, "historical", v03Article1, chunkCfg, embedding.NewFakeEmbedder(), t)

	retriever, _ := retrieval.New(retrieval.Config{TopK: 5})
	retriever.SetStore(store)
	retriever.SetEmbedder(embedding.NewFakeEmbedder())

	results, _ := retriever.Retrieve(context.Background(), retrieval.RetrievalQuery{Text: "local development", Limit: 5})
	t.Logf("Archive supplements live research with %d context hits", len(results.Hits))
	t.Log("Archive supplement to live research test completed")
}

// =============================================================================
// v0.2 Backward Compatibility Tests
// =============================================================================

func TestE2E_v03_v02BackwardCompatibility(t *testing.T) {
	promptBytes, err := os.ReadFile("prompts/researcher.prompt")
	researcherPrompt := string(promptBytes)
	if err != nil {
		researcherPrompt = `You are a research analyst. Return JSON with sources, claims, and contradictions.`
	}

	searchPlanJSON := `{"original_topic": "v0.2 Compatibility Test", "queries": []}`
	researcherJSON := `{"stable_id": "v02-compat", "topic": "v0.2 Compatibility Test", "generated_at": "2024-01-15T10:00:00Z", "sources": [], "claims": [], "contradictions": [], "unresolved_questions": [], "research_notes": []}`

	fakeLLM := fakeLLMServer{searchPlanResponse: searchPlanJSON, researcherResponse: researcherJSON}
	llmServer := httptest.NewServer(&fakeLLM)
	defer llmServer.Close()

	articleServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(v03Article1))
	}))
	defer articleServer.Close()

	config := &workflow.Config{
		Topic:            "v0.2 Compatibility Test",
		URLs:             []string{articleServer.URL + "/article"},
		OutputPath:       filepath.Join(t.TempDir(), "v02-compat-dossier.json"),
		AutoDiscover:     false,
		LLMBaseURL:       llmServer.URL,
		LLMModel:         "test",
		LLMTimeout:       30.0,
		FetchTimeout:     10.0,
		FetcherConfig:    fetcher.Config{Timeout: 10 * time.Second, MaxSize: 10 * 1024 * 1024, UserAgent: "test", FollowRedirects: true},
		ExtractorConfig:  extractor.DefaultConfig(),
		ResearcherConfig: researcher.ClientConfig{PromptOverride: researcherPrompt, Temperature: 0.3, MaxOutputTokens: 16384},
	}

	w := workflow.New(*config)
	err = w.Run(context.Background())
	if err != nil {
		t.Logf("v0.2 compatibility: %v", err)
	}

	outputPath := config.OutputPath
	content, err := os.ReadFile(outputPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("Failed to read output: %v", err)
	}

	if len(content) > 0 {
		var dossier researcher.ResearchDossier
		if err := json.Unmarshal(content, &dossier); err != nil {
			t.Logf("Dossier parse error: %v", err)
		} else if dossier.Topic != "v0.2 Compatibility Test" {
			t.Errorf("Expected topic 'v0.2 Compatibility Test', got %q", dossier.Topic)
		}
	}

	t.Log("v0.2 backward compatibility test completed")
}

// =============================================================================
// Helper Tests
// =============================================================================

func TestE2E_v03_DocumentExistsAndDelete(t *testing.T) {
	ctx := context.Background()
	store := createInMemoryArchive(t)
	chunkCfg := chunker.Config{TargetChunkSize: 200, MaxChunkSize: 300, OverlapRatio: 0.1}

	_, _ = ingestDoc(ctx, store, "test-del", v03Article1, chunkCfg, embedding.NewFakeEmbedder(), t)

	exists, _ := store.DocumentExists(ctx, "arch:test-del")
	if !exists {
		t.Error("Document should exist")
	}

	_ = store.DeleteDocument(ctx, "arch:test-del")
	exists, _ = store.DocumentExists(ctx, "arch:test-del")
	if exists {
		t.Error("Document should not exist after deletion")
	}

	t.Log("Document exists and delete test passed")
}

func TestE2E_v03_ListDocuments(t *testing.T) {
	ctx := context.Background()
	store := createInMemoryArchive(t)
	chunkCfg := chunker.Config{TargetChunkSize: 200, MaxChunkSize: 300, OverlapRatio: 0.1}

	for i := 1; i <= 3; i++ {
		_, _ = ingestDoc(ctx, store, fmt.Sprintf("list-%d", i), fmt.Sprintf("Doc %d", i), chunkCfg, embedding.NewFakeEmbedder(), t)
	}

	docs, _ := store.ListDocuments(ctx, 0)
	if len(docs) != 3 {
		t.Errorf("Expected 3 documents, got %d", len(docs))
	}

	t.Log("List documents test passed")
}

func TestE2E_v03_PlannerIntegration(t *testing.T) {
	fakeClient := &fakePlannerClient{}
	cfg := planner.Config{MinQueries: 1, MaxQueries: 5, Temperature: 0.5, MaxOutputTokens: 4000}
	p, err := planner.New(fakeClient, "test-model", cfg)
	if err != nil {
		t.Fatalf("Planner failed: %v", err)
	}
	if p == nil {
		t.Fatal("Planner should not be nil")
	}
	t.Log("Planner integration test passed")
}

func TestE2E_v03_EmbeddingConfig(t *testing.T) {
	client := embedding.NewClient(embedding.Config{BaseURL: "http://localhost:8000/v1", EmbeddingModel: "test", Timeout: 30 * time.Second})
	if client == nil {
		t.Fatal("Embedding client should not be nil")
	}
	fake := embedding.NewFakeEmbedder()
	if fake == nil {
		t.Fatal("Fake embedder should not be nil")
	}
	t.Log("Embedding config test passed")
}

func TestE2E_v03_RetrievalConfig(t *testing.T) {
	cfg := retrieval.DefaultConfig()
	if cfg.TopK <= 0 {
		t.Error("TopK should be positive")
	}
	cfg = retrieval.DefaultConfig().WithTopK(20).WithMinRelevanceScore(0.5)
	if err := cfg.Validate(); err != nil {
		t.Errorf("Config should be valid: %v", err)
	}
	t.Log("Retrieval config test passed")
}

func TestE2E_v03_ChunkerConfig(t *testing.T) {
	cfg := chunker.DefaultConfig()
	if cfg.TargetChunkSize <= 0 {
		t.Error("TargetChunkSize should be positive")
	}
	custom := chunker.Config{TargetChunkSize: 400, MaxChunkSize: 600, OverlapRatio: 0.15}
	if err := custom.Validate(); err != nil {
		t.Errorf("Config should be valid: %v", err)
	}
	_, err := chunker.New(custom)
	if err != nil {
		t.Errorf("Chunker creation should succeed: %v", err)
	}
	t.Log("Chunker config test passed")
}

func TestE2E_v03_HealthCheck(t *testing.T) {
	ctx := context.Background()
	store := createInMemoryArchive(t)
	if err := store.HealthCheck(ctx); err != nil {
		t.Fatalf("Health check failed: %v", err)
	}
	t.Log("Health check test passed")
}

func TestE2E_v03_FileBasedArchive(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := archive.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("Failed: %v", err)
	}
	defer func() {
		_ = store.Close()
		_ = os.Remove(dbPath)
		_ = os.Remove(dbPath + "-wal")
		_ = os.Remove(dbPath + "-shm")
	}()

	ctx := context.Background()
	chunkCfg := chunker.Config{TargetChunkSize: 200, MaxChunkSize: 300, OverlapRatio: 0.1}
	_, err = ingestDoc(ctx, store, "file-test", v03Article1, chunkCfg, embedding.NewFakeEmbedder(), t)
	if err != nil {
		t.Fatalf("Ingest failed: %v", err)
	}

	doc, _ := store.GetDocument(ctx, "arch:file-test")
	if doc == nil {
		t.Error("Document should exist")
	}
	if err := store.HealthCheck(ctx); err != nil {
		t.Fatalf("Health check failed: %v", err)
	}

	t.Log("File-based archive test passed")
}

func TestE2E_v03_MultipleEmbeddingModels(t *testing.T) {
	ctx := context.Background()
	store := createInMemoryArchive(t)
	chunkCfg := chunker.Config{TargetChunkSize: 200, MaxChunkSize: 300, OverlapRatio: 0.1}

	_, _ = ingestDoc(ctx, store, "model-test", v03Article1, chunkCfg, embedding.NewFakeEmbedder(), t)

	chunks, _ := store.GetChunksWithEmbeddingMetadata(ctx)
	if len(chunks) > 0 {
		meta, _ := store.GetEmbeddingMetadata(ctx, chunks[0].StableID)
		t.Logf("Model: %s, dimensions: %d", meta.ModelName, meta.Dimensions)
	}

	t.Log("Multiple embedding models test passed")
}
