package main

// Shared in-process fakes for CLI integration tests.
//
// The pre-existing CLI tests drive the binary through `go run`, whose
// subprocess coverage is not attributed to this package. These fakes let the
// do* handlers run *inside* the test binary against local HTTP servers, so
// the command implementations and the internal packages they orchestrate are
// both exercised (and counted) by `go test -coverprofile ./...`.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/archive"
	"github.com/Mundo-Dolphins/local-newsroom/internal/chunker"
	"github.com/Mundo-Dolphins/local-newsroom/internal/search"
	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// cliEnvVars lists every environment variable the CLI resolves. Tests pin
// all of these to "" (the config layer treats empty as unset) so resolution
// starts from a known, hermetic state.
var cliEnvVars = []string{
	"OMLX_BASE_URL", "OMLX_MODEL", "OMLX_API_KEY", "OMLX_TIMEOUT",
	"SEARXNG_BASE_URL", "SEARXNG_SEARCH_LANGUAGE", "SEARXNG_SEARCH_TIME_RANGE",
	"SEARXNG_MAX_QUERIES", "SEARXNG_RESULTS_PER_QUERY", "SEARXNG_MAX_SOURCES",
	"SEARXNG_API_KEY", "SEARXNG_API_HEADER",
	"EMBEDDING_BASE_URL", "EMBEDDING_MODEL", "EMBEDDING_API_KEY", "EMBEDDING_TIMEOUT",
	"RERANK_ENABLED", "RERANK_MODEL",
	"ARCHIVE_DB_PATH", "ARCHIVE_TOP_K", "ARCHIVE_FORMAT",
	"FETCH_TIMEOUT", "FETCH_MAX_SIZE", "FETCH_MAX_WORDS", "FETCH_PARALLEL",
	"CHUNK_TARGET_SIZE", "CHUNK_MAX_SIZE", "CHUNK_OVERLAP_RATIO",
	"NEWSROOM_PROFILES_ROOT", "NEWSROOM_PROFILE_NAME",
	"NEWSROOM_LANGUAGE", "NEWSROOM_OUTPUT_FORMAT", "NEWSROOM_TEMPERATURE",
	"NEWSROOM_MAX_TOKENS", "NEWSROOM_TONE", "NEWSROOM_CONFIG",
}

// setHermeticEnv pins every newsroom-related environment variable to "" (the
// config layer treats empty values as unset), points HOME at an empty
// directory, and moves the test working directory into a temp dir so
// CWD-relative config lookups (./.newsroom.yaml) find nothing.
//
// The temp dir is laid out like the repository root (<root>/cmd/newsroom is
// the new CWD) with the root prompts/ directory copied in, because the
// writer and final-checker stages read ../../prompts/*.prompt relative to the
// current working directory at runtime.
func setHermeticEnv(t *testing.T) {
	t.Helper()
	for _, k := range cliEnvVars {
		t.Setenv(k, "")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")

	// The test binary starts in the package source dir (cmd/newsroom), so the
	// repository prompts live at ../../prompts *before* we chdir.
	srcPrompts, err := filepath.Abs(filepath.Join("..", "..", "prompts"))
	if err != nil {
		t.Fatalf("resolve source prompts dir: %v", err)
	}
	root := t.TempDir()
	dstPrompts := filepath.Join(root, "prompts")
	if err := copyDir(srcPrompts, dstPrompts); err != nil {
		t.Fatalf("copy prompts into hermetic root: %v", err)
	}
	cwd := filepath.Join(root, "cmd", "newsroom")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("create hermetic cwd: %v", err)
	}
	t.Chdir(cwd)
}

// copyDir copies regular files from src into dst (one level deep).
func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	names, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, name := range names {
		if name.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, name.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, name.Name()), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// resetFlags returns every flag on the given commands (including inherited
// persistent flags) to its default value and clears the "changed" bit. Flag
// values are bound to package-level globals that persist across tests within
// one process, so every test must start from a clean slate.
func resetFlags(t *testing.T, cmds ...*cobra.Command) {
	t.Helper()
	reset := func(set *pflag.FlagSet) {
		set.VisitAll(func(f *pflag.Flag) {
			if err := f.Value.Set(f.DefValue); err == nil {
				f.Changed = false
			}
		})
	}
	for _, c := range cmds {
		if c == nil {
			continue
		}
		reset(c.Flags())
		reset(c.PersistentFlags())
		reset(c.LocalFlags())
	}
}

// resetAllCLIFlags resets the flags of every command the integration tests
// drive, plus the root (which owns the persistent --config flag).
func resetAllCLIFlags(t *testing.T) {
	t.Helper()
	resetFlags(t, rootCmd, researchCmd, verifyCmd, writeCmd, finalCheckCmd,
		archiveCmd, addCmd, searchCmd, statsCmd, configCmd, configShowCmd, configValidateCmd, helpCmd)

	// pflag slice flags (e.g. --url) accumulate values on repeated Set() calls
	// once a flag has been changed, so DefValue resets do not clear them.
	// Zero the bound variable and the Changed bit so each test starts clean.
	rFlags.urls = nil
	if f := researchCmd.Flags().Lookup("url"); f != nil {
		f.Changed = false
	}
	aaFlags.urls = nil
	if f := addCmd.Flags().Lookup("url"); f != nil {
		f.Changed = false
	}
}

// runCommand drives the CLI exactly the way main() does: full command path
// through rootCmd.Execute. Cobra re-dispatches subcommand Execute() calls to
// the root (dropping SetArgs in the process), so tests must route through
// the root command with the subcommand name as the first arg.
func runCommand(t *testing.T, args ...string) error {
	t.Helper()
	resetAllCLIFlags(t)
	rootCmd.SetArgs(args)
	return rootCmd.Execute()
}

// fakeLLMServer is an OpenAI-compatible /chat/completions server that returns
// canned JSON based on a marker in the combined request messages.
type fakeLLMServer struct {
	responses []llmResponseRule
	fallback  string
}

// llmResponseRule matches a substring of the concatenated request messages
// (system + user) and selects the canned response.
type llmResponseRule struct {
	marker   string
	response string
}

// newFakeLLMServer builds the fake with ordered rules (first match wins) and
// a fallback used when no rule matches.
func newFakeLLMServer(t *testing.T, rules []llmResponseRule, fallback string) *httptest.Server {
	t.Helper()
	srv := &fakeLLMServer{responses: rules, fallback: fallback}
	s := httptest.NewServer(srv)
	t.Cleanup(s.Close)
	return s
}

// ServeHTTP implements http.Handler.
func (s *fakeLLMServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

	var combined strings.Builder
	for _, m := range req.Messages {
		combined.WriteString(m.Content)
		combined.WriteString(" ")
	}
	text := combined.String()

	response := s.fallback
	for _, rule := range s.responses {
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

// fakeEmbeddingServer is an OpenAI-compatible /embeddings server that returns
// a fixed vector for every input, making similarity results deterministic.
func newFakeEmbeddingServer(t *testing.T, dim int) *httptest.Server {
	t.Helper()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Inputs []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		vector := make([]float32, dim)
		if dim > 0 {
			vector[0] = 1
		}
		data := make([]map[string]interface{}, len(req.Inputs))
		for i := range req.Inputs {
			data[i] = map[string]interface{}{
				"object":    "embedding",
				"index":     i,
				"embedding": vector,
			}
		}
		resp := map[string]interface{}{
			"object": "list",
			"model":  "fake-embed",
			"data":   data,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	return s
}

// newFakeArticleServer serves the given path->HTML map (paths without a
// leading slash); unknown paths get 404.
func newFakeArticleServer(t *testing.T, articles map[string]string) *httptest.Server {
	t.Helper()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if content, ok := articles[path]; ok {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(content))
			return
		}
		http.NotFound(w, r)
	})
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	return s
}

// fakeSearxngResult is one canned SearXNG result.
type fakeSearxngResult struct {
	url     string
	title   string
	snippet string
	rank    int
}

// newFakeSearxngServer returns a SearXNG-compatible search endpoint. Queries
// are matched against the fixture keys with a bidirectional substring test.
func newFakeSearxngServer(t *testing.T, fixtures map[string][]fakeSearxngResult) *httptest.Server {
	t.Helper()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("q")

		var fixtures_ []fakeSearxngResult
		for k, v := range fixtures {
			if strings.Contains(query, k) || strings.Contains(k, query) {
				fixtures_ = v
				break
			}
		}

		results := make([]search.SearchResult, 0, len(fixtures_))
		for i, f := range fixtures_ {
			title := f.title
			snippet := f.snippet
			published := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
			domain := "127.0.0.1"
			results = append(results, search.SearchResult{
				ID:           "result-" + strconv.Itoa(i),
				URL:          f.url,
				Title:        &title,
				Snippet:      &snippet,
				ProviderName: "fake-searxng",
				Rank:         f.rank,
				PublishedAt:  &published,
				SourceDomain: domain,
			})
		}

		resp := search.Response{
			OriginalQuery: query,
			Results:       results,
			ProviderInfo:  map[string]string{"provider": "fake-searxng"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	return s
}

// seedArchiveStore creates (or opens) the archive store at dbPath and inserts
// one document with embedded chunks, mirroring what `archive add` produces.
func seedArchiveStore(t *testing.T, dbPath string, plainText string) *archive.SQLiteStore {
	t.Helper()
	store, err := archive.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to open archive store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const docID = "doc-cli-seed-1"
	chunkLen := len(plainText)
	chunks := []archive.Chunk{
		{
			StableID:         archive.StableChunkID("chunk-cli-seed-1"),
			DocumentStableID: archive.StableDocumentID(docID),
			ContentHash:      archive.ContentHash(chunker.ComputeContentHash(plainText)),
			Position:         0,
			Length:           chunkLen,
			HasEmbedding:     true,
		},
	}
	doc := &archive.ArchiveDocument{
		StableID:      archive.StableDocumentID(docID),
		PlainText:     plainText,
		PlainTextHash: archive.ContentHash(chunker.ComputeContentHash(plainText)),
		ChunkCount:    len(chunks),
		SourceProvenance: archive.SourceProvenance{
			SourceID:           "src-cli-seed-1",
			SourceURL:          "https://example.com/seed",
			SourceType:         types.SourceTypeWeb,
			RetrievedAt:        time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			ExtractedAt:        time.Date(2024, 1, 15, 10, 0, 1, 0, time.UTC),
			ExtractionMetadata: map[string]string{"extractor": "test-seed"},
		},
		ArchivedAt: time.Date(2024, 1, 15, 10, 0, 2, 0, time.UTC),
		Metadata:   map[string]string{"original_url": "https://example.com/seed"},
	}

	ctx := context.Background()
	if err := store.UpsertDocument(ctx, doc, chunks); err != nil {
		t.Fatalf("failed to seed document: %v", err)
	}
	if err := store.SetEmbeddingVector(ctx, chunks[0].StableID, []float32{1, 0, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatalf("failed to seed embedding vector: %v", err)
	}
	meta := &archive.EmbeddingMetadata{
		ModelName:   "fake-embed",
		Dimensions:  8,
		GeneratedAt: time.Date(2024, 1, 15, 10, 0, 3, 0, time.UTC),
	}
	if err := store.SetEmbeddingMetadata(ctx, chunks[0].StableID, meta); err != nil {
		t.Fatalf("failed to seed embedding metadata: %v", err)
	}
	return store
}

// mustWriteFile writes a file in a test, failing the test on error.
func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("failed to create dir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}
}
