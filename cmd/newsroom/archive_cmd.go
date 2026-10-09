// Package main implements the newsroom archive commands for archiving and
// retrieving content from a local semantic search index.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/Mundo-Dolphins/local-newsroom/internal/archive"
	"github.com/Mundo-Dolphins/local-newsroom/internal/chunker"
	"github.com/Mundo-Dolphins/local-newsroom/internal/config"
	"github.com/Mundo-Dolphins/local-newsroom/internal/embedding"
	"github.com/Mundo-Dolphins/local-newsroom/internal/extractor"
	html_extractor "github.com/Mundo-Dolphins/local-newsroom/internal/extractor/html"
	"github.com/Mundo-Dolphins/local-newsroom/internal/fetcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/retrieval"
	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
	"github.com/spf13/cobra"
)

// archiveCmd is the "newsroom archive" parent command.
var archiveCmd = &cobra.Command{
	Use:   "archive",
	Short: "Manage the local news archive",
	Long: `Manage the local news archive for semantic search and retrieval.

This command provides three main operations:

  add       - Archive URLs to the local index
  search    - Search archived content semantically
  stats     - Display archive statistics`,
}

// addCmd is the "newsroom archive add" subcommand.
var addCmd = &cobra.Command{
	Use:   "add [flags]",
	Short: "Add URLs to the archive",
	Long: `Add URLs to the local archive index for semantic search.

The archive pipeline:
  URL -> fetcher -> extractor -> chunker -> embeddings -> SQLite

Supports adding one or multiple URLs. Repeated ingestion of the same
content is idempotent - unchanged content is not re-archived.

Examples:

  # Add a single URL
  newsroom archive add --url https://example.com/article

  # Add multiple URLs
  newsroom archive add \
    --url https://example.com/article1 \
    --url https://example.com/article2 \
    --url https://example.com/article3

  # Add from a file (one URL per line)
  newsroom archive add --urls-file urls.txt

  # With custom configuration
  newsroom archive add --url https://example.com/article \\
    --db-path ./archive.db \\
    --embedding-base-url http://localhost:8000/v1 \\
    --embedding-model all-MiniLM-L6-v2`,
	RunE: doArchiveAdd,
}

// searchCmd is the "newsroom archive search" subcommand.
var searchCmd = &cobra.Command{
	Use:   "search [flags]",
	Short: "Search archived content",
	Long: `Search archived content semantically.

Performs a semantic search over the local archive using embedding-based
similarity. Optionally applies reranking for improved accuracy.

Examples:

  # Basic search
  newsroom archive search --query "Miami Dolphins 2024 defensive coordinator"

  # Search with custom parameters
  newsroom archive search --query "Super Bowl predictions" \\
    --db-path ./archive.db \\
    --top-k 20 \\
    --format json

  # Search from stdin
  echo "AI regulations 2024" | newsroom archive search --from-stdin`,
	RunE: doArchiveSearch,
}

// statsCmd is the "newsroom archive stats" subcommand.
var statsCmd = &cobra.Command{
	Use:   "stats [flags]",
	Short: "Display archive statistics",
	Long: `Display statistics about the local archive.

Shows counts of documents, chunks, and embeddings, along with configuration
information. Lightweight operation that doesn't require exhaustive analysis.

Examples:

  # Show archive stats
  newsroom archive stats

  # Show archive stats with custom database
  newsroom archive stats --db-path ./archive.db

  # Output as JSON
  newsroom archive stats --format json`,
	RunE: doArchiveStats,
}

// archiveFlags holds flags shared across archive subcommands.
type archiveFlags struct {
	dbPath           string
	embeddingBaseURL string
	embeddingModel   string
	embeddingAPIKey  string
	embeddingTimeout float64
	topK             int
	rerankEnabled    bool
	rerankModel      string
	format           string // "text" or "json"
}

// addFlags holds flags specific to the add subcommand.
type addFlags struct {
	urls            []string
	urlsFile        string
	fetchTimeout    float64
	maxSize         int64
	maxWords        int
	targetChunkSize int
	maxChunkSize    int
	overlapRatio    float64
	parallelFetch   int
	progress        bool
}

// searchFlags holds flags specific to the search subcommand.
type searchFlags struct {
	query     string
	fromStdin bool
}

// Shared archive flags
var aFlags archiveFlags

// Add flags
var aaFlags addFlags

// Search flags
var asFlags searchFlags

func init() {
	// Shared archive flags
	archiveCmd.PersistentFlags().StringVar(&aFlags.dbPath, "db-path", getDefaultArchivePath(), "Path to the archive SQLite database")
	archiveCmd.PersistentFlags().StringVar(&aFlags.embeddingBaseURL, "embedding-base-url", getEnvOrDefault("EMBEDDING_BASE_URL", "http://localhost:8000/v1"), "Base URL for embedding API")
	archiveCmd.PersistentFlags().StringVar(&aFlags.embeddingModel, "embedding-model", getEnvOrDefault("EMBEDDING_MODEL", "all-MiniLM-L6-v2"), "Embedding model name")
	archiveCmd.PersistentFlags().StringVar(&aFlags.embeddingAPIKey, "embedding-api-key", getEnvOrDefault("EMBEDDING_API_KEY", ""), "API key for embedding API authentication")
	archiveCmd.PersistentFlags().Float64Var(&aFlags.embeddingTimeout, "embedding-timeout", 30, "Embedding API timeout in seconds")
	archiveCmd.PersistentFlags().IntVar(&aFlags.topK, "top-k", 10, "Top-K results for search")
	archiveCmd.PersistentFlags().BoolVar(&aFlags.rerankEnabled, "rerank-enabled", false, "Enable reranking for search")
	archiveCmd.PersistentFlags().StringVar(&aFlags.rerankModel, "rerank-model", getEnvOrDefault("RERANK_MODEL", "bge-reranker"), "Reranker model name")
	archiveCmd.PersistentFlags().StringVar(&aFlags.format, "format", "text", "Output format: 'text' or 'json'")

	// Add subcommand flags
	addCmd.Flags().StringSliceVarP(&aaFlags.urls, "url", "u", nil, "URL to archive (can be specified multiple times)")
	addCmd.Flags().StringVarP(&aaFlags.urlsFile, "urls-file", "f", "", "File containing URLs to archive (one per line)")
	addCmd.Flags().Float64Var(&aaFlags.fetchTimeout, "fetch-timeout", 30, "HTTP fetch timeout in seconds")
	addCmd.Flags().Int64Var(&aaFlags.maxSize, "max-size", 10*1024*1024, "Maximum response size in bytes")
	addCmd.Flags().IntVar(&aaFlags.maxWords, "max-words", 50000, "Maximum words per document")
	addCmd.Flags().IntVar(&aaFlags.targetChunkSize, "target-chunk-size", 300, "Target chunk size in characters")
	addCmd.Flags().IntVar(&aaFlags.maxChunkSize, "max-chunk-size", 500, "Maximum chunk size in characters")
	addCmd.Flags().Float64Var(&aaFlags.overlapRatio, "overlap-ratio", 0.1, "Overlap ratio between chunks (0.0 to 0.99)")
	addCmd.Flags().IntVar(&aaFlags.parallelFetch, "parallel-fetch", 5, "Number of parallel fetch operations")
	addCmd.Flags().BoolVar(&aaFlags.progress, "progress", true, "Show progress during archiving")

	// Search subcommand flags
	searchCmd.Flags().StringVarP(&asFlags.query, "query", "q", "", "Search query text")
	searchCmd.Flags().BoolVar(&asFlags.fromStdin, "from-stdin", false, "Read query from stdin")

	// Add subcommands to archive command
	archiveCmd.AddCommand(addCmd)
	archiveCmd.AddCommand(searchCmd)
	archiveCmd.AddCommand(statsCmd)

	// Add archive command to root
	rootCmd.AddCommand(archiveCmd)
}

// doArchiveAdd implements the "archive add" command.
func doArchiveAdd(cmd *cobra.Command, args []string) error {
	// Resolve archive settings (CLI > env > config file > built-in defaults)
	// into the shared flag structs that the pipeline helpers read from.
	if err := resolveArchiveSettings(cmd, true); err != nil {
		return err
	}

	// Collect URLs from flags and file
	urls := aaFlags.urls
	if aaFlags.urlsFile != "" {
		fileURLs, err := readURLsFromFile(aaFlags.urlsFile)
		if err != nil {
			return fmt.Errorf("failed to read URLs from file: %w", err)
		}
		urls = append(urls, fileURLs...)
	}

	if len(urls) == 0 {
		return errors.New("no URLs provided; use --url or --urls-file")
	}

	// Set up context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		if aaFlags.progress {
			fmt.Fprintln(os.Stderr, "\nCancelling...")
		}
		cancel()
	}()

	// Create pipeline components
	store, err := archive.NewSQLiteStore(aFlags.dbPath)
	if err != nil {
		return fmt.Errorf("failed to create archive store: %w", err)
	}
	defer store.Close() //nolint:errcheck

	fetchClient := fetcher.NewClient(fetcher.Config{
		Timeout:   time.Duration(aaFlags.fetchTimeout) * time.Second,
		MaxSize:   aaFlags.maxSize,
		UserAgent: "local-newsroom-archive/0.0.1",
	})

	extractorClient := html_extractor.New(extractor.DefaultConfig())

	chunkConfig := chunker.Config{
		TargetChunkSize: aaFlags.targetChunkSize,
		MaxChunkSize:    aaFlags.maxChunkSize,
		OverlapRatio:    aaFlags.overlapRatio,
	}
	chunkerClient, err := chunker.New(chunkConfig)
	if err != nil {
		return fmt.Errorf("failed to create chunker: %w", err)
	}

	embedConfig := embedding.Config{
		BaseURL:        aFlags.embeddingBaseURL,
		EmbeddingModel: aFlags.embeddingModel,
		Timeout:        time.Duration(aFlags.embeddingTimeout) * time.Second,
		APIKey:         aFlags.embeddingAPIKey,
	}
	embedder := embedding.NewClient(embedConfig)

	// Add documents in parallel
	results, errs := addURLs(ctx, fetchClient, extractorClient, chunkerClient, embedder, store, urls, aaFlags.parallelFetch, aaFlags.progress)

	// Report results
	if aaFlags.progress {
		fmt.Printf("\n%d URLs processed, %d succeeded, %d failed\n",
			len(urls), len(results), len(errs))
	}

	// Print errors for failed URLs
	if len(errs) > 0 {
		fmt.Fprintln(os.Stderr, "\nFailed URLs:")
		for _, err := range errs {
			fmt.Fprintf(os.Stderr, "  - %s: %v\n", err.URL, err.Err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%d URL(s) failed to archive", len(errs))
	}

	return nil
}

// addURLs adds multiple URLs in parallel.
func addURLs(ctx context.Context, fetchClient *fetcher.Client, extractorClient *html_extractor.Extractor,
	chunkerClient *chunker.Chunker, embedder embedding.Embedder, store *archive.SQLiteStore,
	urls []string, parallel int, progress bool) ([]AddResult, []AddError) {

	results := make([]AddResult, 0, len(urls))
	errs := make([]AddError, 0)

	// Limit parallelism
	ch := make(chan string, len(urls))
	for _, url := range urls {
		ch <- url
	}
	close(ch)

	var wg sync.WaitGroup
	sem := make(chan struct{}, parallel)

	for url := range ch {
		sem <- struct{}{}
		wg.Add(1)
		go func(u string) {
			defer func() { <-sem }()
			defer wg.Done()

			result, err := addURL(ctx, fetchClient, extractorClient, chunkerClient, embedder, store, u)
			if err != nil {
				errs = append(errs, AddError{URL: u, Err: err})
				return
			}
			results = append(results, result)

			if progress {
				fmt.Printf("✓ Archived: %s (%d chunks)\n", truncateURL(u, 60), result.ChunkCount)
			}
		}(url)
	}

	wg.Wait()
	return results, errs
}

// addURL adds a single URL to the archive.
func addURL(ctx context.Context, fetchClient *fetcher.Client, extractorClient *html_extractor.Extractor,
	chunkerClient *chunker.Chunker, embedder embedding.Embedder, store *archive.SQLiteStore,
	url string) (AddResult, error) {

	// Fetch
	fetchResult, fetchErr := fetchClient.Fetch(ctx, url)
	if fetchErr != nil {
		return AddResult{}, fmt.Errorf("fetch failed: %w", fetchErr)
	}

	// Extract
	extractInput := extractor.Input{
		Source: types.Source{
			StableID:    generateSourceID(url),
			OriginalURL: url,
			SourceType:  types.SourceTypeWeb,
		},
		Content: fetchResult.Body,
	}

	doc, err := extractorClient.Extract(extractInput)
	if err != nil {
		return AddResult{}, fmt.Errorf("extraction failed: %w", err)
	}

	if doc == nil || strings.TrimSpace(doc.PlainText) == "" {
		return AddResult{}, errors.New("no extractable content")
	}

	if aaFlags.progress && len(url) > 0 {
		fmt.Printf("  Extracted: %s (%d words)\n", url, countWords(doc.PlainText))
	}

	// Chunk
	chunks, err := chunkerClient.Chunk(doc)
	if err != nil {
		return AddResult{}, fmt.Errorf("chunking failed: %w", err)
	}

	if len(chunks) == 0 {
		return AddResult{}, errors.New("no chunks produced")
	}

	if aaFlags.progress && len(url) > 0 {
		fmt.Printf("  Chunked: %d chunks\n", len(chunks))
	}

	// Create archive document
	archiveDoc := &archive.ArchiveDocument{
		StableID:      chunker.ComputeDocumentStableID(doc),
		PlainText:     doc.PlainText,
		PlainTextHash: chunker.ComputeContentHash(doc.PlainText),
		ChunkCount:    len(chunks),
		SourceProvenance: archive.SourceProvenance{
			SourceID:           extractInput.Source.StableID,
			SourceURL:          extractInput.Source.OriginalURL,
			SourceType:         extractInput.Source.SourceType,
			RetrievedAt:        fetchResult.RetrievedAt,
			ExtractedAt:        time.Now().UTC(),
			ExtractionMetadata: buildExtractionMetadata(doc),
		},
		ArchivedAt: time.Now().UTC(),
		Metadata:   map[string]string{"original_url": url},
	}

	// Create chunks
	archiveChunks := make([]archive.Chunk, len(chunks))
	for i, c := range chunks {
		archiveChunks[i] = archive.Chunk{
			StableID:         c.ChunkID,
			DocumentStableID: c.DocumentStableID,
			ContentHash:      c.ContentHash,
			Position:         c.Position,
			Length:           c.Length,
			HasEmbedding:     true,
		}
	}

	// Upsert document
	if err := store.UpsertDocument(ctx, archiveDoc, archiveChunks); err != nil {
		return AddResult{}, fmt.Errorf("failed to store document: %w", err)
	}

	if aaFlags.progress && len(url) > 0 {
		fmt.Printf("  Stored: document with %d chunks\n", len(archiveChunks))
	}

	// Embed chunks
	if err := embedChunks(ctx, embedder, store, archiveChunks, doc.PlainText); err != nil {
		return AddResult{}, fmt.Errorf("embedding failed: %w", err)
	}

	if aaFlags.progress && len(url) > 0 {
		fmt.Printf("  Embedded: %d chunks\n", len(archiveChunks))
	}

	return AddResult{
		URL:        url,
		StableID:   string(archiveDoc.StableID),
		ChunkCount: len(archiveChunks),
	}, nil
}

// embedChunks embeds all chunks and stores the vectors.
func embedChunks(ctx context.Context, embedder embedding.Embedder, store *archive.SQLiteStore,
	chunks []archive.Chunk, docText string) error {

	// Group chunks into batches
	const batchSize = 10
	for i := 0; i < len(chunks); i += batchSize {
		end := i + batchSize
		if end > len(chunks) {
			end = len(chunks)
		}

		batch := chunks[i:end]
		if err := embedChunksBatch(ctx, embedder, store, batch, docText); err != nil {
			return err
		}
	}

	return nil
}

// embedChunksBatch embeds a batch of chunks.
func embedChunksBatch(ctx context.Context, embedder embedding.Embedder, store *archive.SQLiteStore,
	chunks []archive.Chunk, docText string) error {

	// Extract chunk text from document
	chunkTexts := make([]string, len(chunks))
	for i, c := range chunks {
		if c.Position >= 0 && c.Position < len(docText) {
			end := c.Position + c.Length
			if end > len(docText) {
				end = len(docText)
			}
			chunkTexts[i] = docText[c.Position:end]
		} else {
			chunkTexts[i] = ""
		}
	}

	// Batch embed
	embeddings, err := embedder.Embed(ctx, embedding.Request{
		Model:      aFlags.embeddingModel,
		Inputs:     chunkTexts,
		Dimensions: 0,
	})
	if err != nil {
		return err
	}

	// Store vectors and metadata
	now := time.Now().UTC()
	for i, emb := range embeddings {
		chunkID := chunks[i].StableID

		if err := store.SetEmbeddingVector(ctx, chunkID, emb.Vector); err != nil {
			return fmt.Errorf("failed to store embedding vector: %w", err)
		}

		meta := &archive.EmbeddingMetadata{
			ModelName:   archive.EmbeddingModelName(aFlags.embeddingModel),
			Dimensions:  archive.EmbeddingDimensions(len(emb.Vector)),
			GeneratedAt: now,
		}
		if err := store.SetEmbeddingMetadata(ctx, chunkID, meta); err != nil {
			return fmt.Errorf("failed to store embedding metadata: %w", err)
		}
	}

	return nil
}

// doArchiveSearch implements the "archive search" command.
func doArchiveSearch(cmd *cobra.Command, args []string) error {
	// Resolve archive settings (CLI > env > config file > built-in defaults).
	if err := resolveArchiveSettings(cmd, false); err != nil {
		return err
	}

	// Get query from flag or stdin
	query := asFlags.query
	if asFlags.fromStdin {
		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			query = strings.TrimSpace(scanner.Text())
		}
		if query == "" && asFlags.fromStdin {
			return errors.New("no query provided; use --query or ensure stdin has content")
		}
	}

	if query == "" {
		return errors.New("no query provided; use --query or --from-stdin")
	}

	// Open store
	store, err := archive.NewSQLiteStore(aFlags.dbPath)
	if err != nil {
		return fmt.Errorf("failed to create archive store: %w", err)
	}
	defer store.Close() //nolint:errcheck

	// Create embedder
	embedConfig := embedding.Config{
		BaseURL:        aFlags.embeddingBaseURL,
		EmbeddingModel: aFlags.embeddingModel,
		Timeout:        time.Duration(aFlags.embeddingTimeout) * time.Second,
		APIKey:         aFlags.embeddingAPIKey,
	}
	embedder := embedding.NewClient(embedConfig)

	// Create retriever
	cfg := retrieval.Config{
		TopK:              aFlags.topK,
		EmbeddingProvider: embedder,
	}
	retriever, err := retrieval.New(cfg)
	if err != nil {
		return fmt.Errorf("failed to create retriever: %w", err)
	}
	retriever.SetStore(store)

	// Perform search
	ctx := context.Background()
	queryResult := retrieval.RetrievalQuery{
		Text:   query,
		Target: archive.RetrievalTargetChunk,
		Limit:  aFlags.topK,
	}

	result, err := retriever.Retrieve(ctx, queryResult)
	if err != nil {
		return fmt.Errorf("search failed: %w", err)
	}

	// Output results
	if aFlags.format == "json" {
		return outputSearchResultsJSON(result)
	}

	return outputSearchResultsText(result)
}

// outputSearchResultsText outputs search results in human-readable format.
func outputSearchResultsText(result *retrieval.Result) error {
	if len(result.Hits) == 0 {
		fmt.Println("No results found.")
		return nil
	}

	fmt.Printf("Found %d result(s) for: %q\n\n", len(result.Hits), result.Query.Text)

	for i, hit := range result.Hits {
		fmt.Printf("=== Result %d ===\n", i+1)

		if hit.Document != nil {
			fmt.Printf("URL: %s\n", truncateURL(hit.SourceProvenance.SourceURL, 80))
			fmt.Printf("Source: %s\n", hit.SourceProvenance.SourceID)
			fmt.Printf("Archived: %s\n", hit.Document.ArchivedAt.Format("2006-01-02 15:04:05"))
		}

		fmt.Printf("Relevance: %.4f\n", hit.RelevanceScore)

		// Show chunk content with context
		content := hit.ChunkContent
		if len(content) > 200 {
			content = "..." + content[maxInt(0, len(content)-200):]
		}
		fmt.Printf("Content: %s\n", content)

		fmt.Println()
	}

	return nil
}

// outputSearchResultsJSON outputs search results in JSON format.
func outputSearchResultsJSON(result *retrieval.Result) error {
	type HitResult struct {
		ChunkID        string  `json:"chunk_id"`
		SourceURL      string  `json:"source_url"`
		SourceID       string  `json:"source_id"`
		RelevanceScore float64 `json:"relevance_score"`
		ChunkContent   string  `json:"chunk_content"`
		ArchivedAt     string  `json:"archived_at,omitempty"`
	}

	type SearchResultJSON struct {
		Query     string      `json:"query"`
		TotalHits int         `json:"total_hits"`
		Reranked  bool        `json:"reranked"`
		Results   []HitResult `json:"results"`
	}

	output := SearchResultJSON{
		Query:     result.Query.Text,
		TotalHits: result.TotalHits,
		Reranked:  result.Reranked,
	}

	for _, hit := range result.Hits {
		hitJSON := HitResult{
			ChunkID:        string(hit.Chunk.StableID),
			SourceURL:      hit.SourceProvenance.SourceURL,
			SourceID:       hit.SourceProvenance.SourceID,
			RelevanceScore: hit.RelevanceScore,
			ChunkContent:   hit.ChunkContent,
		}

		if hit.Document != nil {
			hitJSON.ArchivedAt = hit.Document.ArchivedAt.Format(time.RFC3339)
		}

		output.Results = append(output.Results, hitJSON)
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}

// doArchiveStats implements the "archive stats" command.
func doArchiveStats(cmd *cobra.Command, args []string) error {
	// Resolve archive settings (CLI > env > config file > built-in defaults).
	if err := resolveArchiveSettings(cmd, false); err != nil {
		return err
	}

	store, err := archive.NewSQLiteStore(aFlags.dbPath)
	if err != nil {
		return fmt.Errorf("failed to create archive store: %w", err)
	}
	defer store.Close() //nolint:errcheck

	// Gather stats
	docs, err := store.ListDocuments(context.Background(), 0)
	if err != nil {
		return fmt.Errorf("failed to list documents: %w", err)
	}

	chunks, err := store.GetChunksWithEmbeddingMetadata(context.Background())
	if err != nil {
		return fmt.Errorf("failed to get chunks with embeddings: %w", err)
	}

	// Collect model information
	models := make(map[string]bool)
	for _, chunk := range chunks {
		meta, err := store.GetEmbeddingMetadata(context.Background(), chunk.StableID)
		if err != nil || meta == nil {
			continue
		}
		models[string(meta.ModelName)] = true
	}

	stats := ArchiveStats{
		DocumentCount:       len(docs),
		ChunkCount:          0,
		ChunksWithEmbedding: len(chunks),
		Models:              make([]string, 0, len(models)),
		DatabasePath:        aFlags.dbPath,
		ArchivedAt:          time.Now().UTC(),
	}

	// Calculate total chunks
	for _, doc := range docs {
		stats.ChunkCount += doc.ChunkCount
	}

	// Collect model names
	for model := range models {
		stats.Models = append(stats.Models, model)
	}
	sort.Strings(stats.Models)

	// Output
	if aFlags.format == "json" {
		return outputStatsJSON(stats)
	}

	return outputStatsText(stats)
}

// outputStatsText outputs archive statistics in human-readable format.
func outputStatsText(stats ArchiveStats) error {
	fmt.Println("Archive Statistics")
	fmt.Println("==================")
	fmt.Println()
	fmt.Printf("Database: %s\n", stats.DatabasePath)
	fmt.Println()
	fmt.Println("Counts:")
	fmt.Printf("  Documents:       %d\n", stats.DocumentCount)
	fmt.Printf("  Chunks:          %d\n", stats.ChunkCount)
	fmt.Printf("  With Embedding:  %d\n", stats.ChunksWithEmbedding)
	fmt.Println()
	fmt.Println("Models:")
	if len(stats.Models) == 0 {
		fmt.Println("  (none)")
	} else {
		for _, model := range stats.Models {
			fmt.Printf("  - %s\n", model)
		}
	}
	fmt.Println()
	fmt.Printf("Report generated: %s\n", time.Now().UTC().Format(time.RFC3339))
	return nil
}

// outputStatsJSON outputs archive statistics in JSON format.
func outputStatsJSON(stats ArchiveStats) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(stats)
}

// resolveArchiveSettings resolves the archive-related settings through the
// full precedence chain (CLI flags > environment variables > config file >
// built-in defaults) and materializes the winners into the shared flag
// structs (aFlags, and aaFlags when includeAdd is set), which the pipeline
// helpers below read from.
func resolveArchiveSettings(cmd *cobra.Command, includeAdd bool) error {
	dbPath, _, err := appConfig.GetString(config.SettingArchiveDBPath, flagOverride(cmd, "db-path"))
	if err != nil {
		return err
	}
	if dbPath == "" {
		dbPath = "./archive.db"
	}

	embBaseURL, _, err := appConfig.GetString(config.SettingEmbeddingBaseURL, flagOverride(cmd, "embedding-base-url"))
	if err != nil {
		return err
	}
	embModel, _, err := appConfig.GetString(config.SettingEmbeddingModel, flagOverride(cmd, "embedding-model"))
	if err != nil {
		return err
	}
	embAPIKey, _, err := appConfig.GetString(config.SettingEmbeddingAPIKey, flagOverride(cmd, "embedding-api-key"))
	if err != nil {
		return err
	}
	embTimeout, _, err := appConfig.GetFloat(config.SettingEmbeddingTimeout, flagOverride(cmd, "embedding-timeout"))
	if err != nil {
		return err
	}
	topK, _, err := appConfig.GetInt(config.SettingArchiveTopK, flagOverride(cmd, "top-k"))
	if err != nil {
		return err
	}
	rerankEnabled, _, err := appConfig.GetBool(config.SettingRerankEnabled, flagOverride(cmd, "rerank-enabled"))
	if err != nil {
		return err
	}
	rerankModel, _, err := appConfig.GetString(config.SettingRerankModel, flagOverride(cmd, "rerank-model"))
	if err != nil {
		return err
	}
	format, _, err := appConfig.GetString(config.SettingArchiveFormat, flagOverride(cmd, "format"))
	if err != nil {
		return err
	}
	if format == "" {
		format = "text"
	}

	aFlags.dbPath = dbPath
	aFlags.embeddingBaseURL = embBaseURL
	aFlags.embeddingModel = embModel
	aFlags.embeddingAPIKey = embAPIKey
	aFlags.embeddingTimeout = embTimeout
	aFlags.topK = topK
	aFlags.rerankEnabled = rerankEnabled
	aFlags.rerankModel = rerankModel
	aFlags.format = format

	if !includeAdd {
		return nil
	}

	fetchTimeout, _, err := appConfig.GetFloat(config.SettingFetchTimeout, flagOverride(cmd, "fetch-timeout"))
	if err != nil {
		return err
	}
	fetchMaxSize, _, err := appConfig.GetInt(config.SettingFetchMaxSize, flagOverride(cmd, "max-size"))
	if err != nil {
		return err
	}
	fetchMaxWords, _, err := appConfig.GetInt(config.SettingFetchMaxWords, flagOverride(cmd, "max-words"))
	if err != nil {
		return err
	}
	chunkTarget, _, err := appConfig.GetInt(config.SettingChunkTargetSize, flagOverride(cmd, "target-chunk-size"))
	if err != nil {
		return err
	}
	chunkMax, _, err := appConfig.GetInt(config.SettingChunkMaxSize, flagOverride(cmd, "max-chunk-size"))
	if err != nil {
		return err
	}
	overlap, _, err := appConfig.GetFloat(config.SettingChunkOverlapRatio, flagOverride(cmd, "overlap-ratio"))
	if err != nil {
		return err
	}
	parallel, _, err := appConfig.GetInt(config.SettingFetchParallel, flagOverride(cmd, "parallel-fetch"))
	if err != nil {
		return err
	}

	aaFlags.fetchTimeout = fetchTimeout
	aaFlags.maxSize = int64(fetchMaxSize)
	aaFlags.maxWords = fetchMaxWords
	aaFlags.targetChunkSize = chunkTarget
	aaFlags.maxChunkSize = chunkMax
	aaFlags.overlapRatio = overlap
	aaFlags.parallelFetch = parallel
	return nil
}

// Helper functions

// readURLsFromFile reads URLs from a file (one per line).
func readURLsFromFile(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	var urls []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			urls = append(urls, line)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return urls, nil
}

// truncateURL truncates a URL to the specified maximum length.
func truncateURL(url string, maxLen int) string {
	if len(url) <= maxLen {
		return url
	}
	return url[:maxLen-3] + "..."
}

// countWords counts words in a string.
func countWords(s string) int {
	count := 0
	inWord := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			inWord = false
		} else if !inWord {
			count++
			inWord = true
		}
	}
	return count
}

// generateSourceID generates a stable source ID from a URL.
func generateSourceID(url string) string {
	return "src:" + url
}

// getEnvOrDefault returns env var value or default.
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getDefaultArchivePath returns the default archive database path.
func getDefaultArchivePath() string {
	if path := os.Getenv("ARCHIVE_DB_PATH"); path != "" {
		return path
	}
	return "./archive.db"
}

// maxInt returns the maximum of two integers.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// buildExtractionMetadata builds metadata map from a document.
func buildExtractionMetadata(doc *types.Document) map[string]string {
	if doc == nil {
		return nil
	}

	metadata := map[string]string{
		"extractor":  "go-readability",
		"word_count": fmt.Sprintf("%d", countWords(doc.PlainText)),
		"char_count": fmt.Sprintf("%d", len(doc.PlainText)),
	}

	if doc.Title != nil {
		metadata["title"] = *doc.Title
	}

	if doc.Author != nil {
		metadata["author"] = *doc.Author
	}

	if doc.PublishedAt != nil {
		metadata["published_at"] = doc.PublishedAt.Format(time.RFC3339)
	}

	if doc.ExtractionMetadata != nil {
		for k, v := range doc.ExtractionMetadata {
			metadata[k] = v
		}
	}

	return metadata
}

// ArchiveStats represents archive statistics.
type ArchiveStats struct {
	DocumentCount       int       `json:"document_count"`
	ChunkCount          int       `json:"chunk_count"`
	ChunksWithEmbedding int       `json:"chunks_with_embedding"`
	Models              []string  `json:"models"`
	DatabasePath        string    `json:"database_path"`
	ArchivedAt          time.Time `json:"archived_at"`
}

// AddResult represents the result of archiving a single URL.
type AddResult struct {
	URL        string `json:"url"`
	StableID   string `json:"stable_id"`
	ChunkCount int    `json:"chunk_count"`
}

// AddError represents a failure when archiving a single URL.
type AddError struct {
	URL string `json:"url"`
	Err error  `json:"error"`
}
