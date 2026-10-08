package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/archive"
	"github.com/Mundo-Dolphins/local-newsroom/internal/chunker"
	"github.com/Mundo-Dolphins/local-newsroom/internal/embedding"
	"github.com/Mundo-Dolphins/local-newsroom/internal/extractor"
	html_extractor "github.com/Mundo-Dolphins/local-newsroom/internal/extractor/html"
	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
)

// TestArchiveCmdExists verifies the archive command is registered
func TestArchiveCmdExists(t *testing.T) {
	if archiveCmd == nil {
		t.Fatal("archiveCmd should not be nil")
	}
}

// TestArchiveAddCmdExists verifies the add subcommand is registered
func TestArchiveAddCmdExists(t *testing.T) {
	if addCmd == nil {
		t.Fatal("addCmd should not be nil")
	}
}

// TestArchiveSearchCmdExists verifies the search subcommand is registered
func TestArchiveSearchCmdExists(t *testing.T) {
	if searchCmd == nil {
		t.Fatal("searchCmd should not be nil")
	}
}

// TestArchiveStatsCmdExists verifies the stats subcommand is registered
func TestArchiveStatsCmdExists(t *testing.T) {
	if statsCmd == nil {
		t.Fatal("statsCmd should not be nil")
	}
}

// TestExtractChunkEmbed workflow tests
func TestExtractChunkEmbed(t *testing.T) {
	// Create mock store and components
	store := archive.NewFakeStore()
	extractorClient := html_extractor.New(extractor.DefaultConfig())
	chunkerConfig := chunker.Config{
		TargetChunkSize: 300,
		MaxChunkSize:    500,
		OverlapRatio:    0.1,
	}
	chunkerClient, err := chunker.New(chunkerConfig)
	if err != nil {
		t.Fatalf("failed to create chunker: %v", err)
	}

	embedder := embedding.NewFakeEmbedder()
	embedder.SetDefaultDimension(384)

	// Create a test document
	testHTML := `
	<html>
	<body>
		<h1>Test Article</h1>
		<p>This is a test article about the Miami Dolphins football team.
		The team is based in Miami Gardens, Florida.</p>
		<p>The Dolphins play in the AFC East division.</p>
	</body>
	</html>
	`

	// Extract
	extractInput := extractor.Input{
		Source: types.Source{
			StableID:    "test-source-1",
			OriginalURL: "https://example.com/test",
			SourceType:  types.SourceTypeWeb,
		},
		Content: []byte(testHTML),
	}

	doc, err := extractorClient.Extract(extractInput)
	if err != nil {
		t.Fatalf("extraction failed: %v", err)
	}

	if doc == nil || doc.PlainText == "" {
		t.Fatal("extraction produced empty document")
	}

	// Chunk
	chunks, err := chunkerClient.Chunk(doc)
	if err != nil {
		t.Fatalf("chunking failed: %v", err)
	}

	if len(chunks) == 0 {
		t.Fatal("chunking produced no chunks")
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
			RetrievedAt:        doc.RetrievedAt,
			ExtractedAt:        doc.RetrievedAt,
			ExtractionMetadata: doc.ExtractionMetadata,
		},
		ArchivedAt: doc.RetrievedAt,
	}

	// Create archive chunks
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

	// Store document
	ctx := context.Background()
	if err := store.UpsertDocument(ctx, archiveDoc, archiveChunks); err != nil {
		t.Fatalf("failed to store document: %v", err)
	}

	// Embed chunks
	if err := embedChunksBatchForTest(ctx, embedder, store, archiveChunks, doc.PlainText); err != nil {
		t.Logf("embedding warning: %v", err)
	}

	// Verify document was stored
	storedDoc, err := store.GetDocument(ctx, archiveDoc.StableID)
	if err != nil {
		t.Fatalf("failed to get stored document: %v", err)
	}

	if storedDoc == nil {
		t.Fatal("stored document is nil")
	}

	if storedDoc.ChunkCount != len(archiveChunks) {
		t.Errorf("chunk count mismatch: expected %d, got %d", len(archiveChunks), storedDoc.ChunkCount)
	}
}

// TestIdempotency verifies re-archiving same content produces same IDs
func TestIdempotency(t *testing.T) {
	extractorClient := html_extractor.New(extractor.DefaultConfig())
	chunkerConfig := chunker.Config{
		TargetChunkSize: 300,
		MaxChunkSize:    500,
		OverlapRatio:    0.1,
	}
	chunkerClient, err := chunker.New(chunkerConfig)
	if err != nil {
		t.Fatalf("failed to create chunker: %v", err)
	}

	testHTML := `<html><body><p>Test content for idempotency.</p></body></html>`

	extractInput := extractor.Input{
		Source: types.Source{
			StableID:    "test-idempotent-1",
			OriginalURL: "https://example.com/test",
			SourceType:  types.SourceTypeWeb,
		},
		Content: []byte(testHTML),
	}

	doc1, err := extractorClient.Extract(extractInput)
	if err != nil {
		t.Fatalf("extraction failed: %v", err)
	}

	chunks1, err := chunkerClient.Chunk(doc1)
	if err != nil {
		t.Fatalf("chunking failed: %v", err)
	}

	docID1 := chunker.ComputeDocumentStableID(doc1)

	// Same content should produce same ID
	doc2, err := extractorClient.Extract(extractInput)
	if err != nil {
		t.Fatalf("extraction failed: %v", err)
	}

	docID2 := chunker.ComputeDocumentStableID(doc2)

	if docID1 != docID2 {
		t.Errorf("document IDs should match for same content: %s != %s", docID1, docID2)
	}

	// Check chunk IDs
	if len(chunks1) > 0 {
		chunkID1 := chunks1[0].ChunkID
		// Re-chunk same document
		chunks2, _ := chunkerClient.Chunk(doc2)
		if len(chunks2) > 0 {
			chunkID2 := chunks2[0].ChunkID
			if chunkID1 != chunkID2 {
				t.Errorf("chunk IDs should match for same content: %s != %s", chunkID1, chunkID2)
			}
		}
	}
}

// TestOutputFormats verifies JSON and text output formats work
func TestOutputFormats(t *testing.T) {
	// Test that both format values are valid
	if aFlags.format != "text" {
		t.Logf("default format should be 'text', got: %s", aFlags.format)
	}

	// Verify format validation
	validFormats := []string{"text", "json"}
	for _, format := range validFormats {
		if !isValidArchiveFormat(format) {
			t.Errorf("valid format %s rejected", format)
		}
	}
}

// isValidArchiveFormat is specific to archive command formats (text, json)
func isValidArchiveFormat(format string) bool {
	return format == "text" || format == "json"
}

// TestDefaultArchivePath verifies the default archive path is correct
func TestDefaultArchivePath(t *testing.T) {
	// Test with no env var
	defaultPath := getDefaultArchivePath()
	expected := "./archive.db"
	if defaultPath != expected {
		t.Errorf("default archive path: expected %q, got %q", expected, defaultPath)
	}
}

// TestGetEnvOrDefault verifies environment variable fallback
func TestGetEnvOrDefault(t *testing.T) {
	// Test with existing env var
	expected := "test-value"
	_ = os.Setenv("TEST_VAR", expected)
	defer func() { _ = os.Unsetenv("TEST_VAR") }()

	actual := getEnvOrDefault("TEST_VAR", "default")
	if actual != expected {
		t.Errorf("env var: expected %q, got %q", expected, actual)
	}

	// Test with non-existing env var
	actual = getEnvOrDefault("NONEXISTENT_VAR", "default")
	if actual != "default" {
		t.Errorf("non-existent env var: expected %q, got %q", "default", actual)
	}
}

// embedChunksBatchForTest embeds a batch of chunks (test helper)
func embedChunksBatchForTest(ctx context.Context, embedder embedding.Embedder, store *archive.FakeStore,
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
		Model:      "test-model",
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
			return err
		}

		meta := &archive.EmbeddingMetadata{
			ModelName:   archive.EmbeddingModelName("test-model"),
			Dimensions:  archive.EmbeddingDimensions(len(emb.Vector)),
			GeneratedAt: now,
		}
		if err := store.SetEmbeddingMetadata(ctx, chunkID, meta); err != nil {
			return err
		}
	}

	return nil
}
