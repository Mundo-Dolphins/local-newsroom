package archiverev

import (
	"context"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/archive"
	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
)

// TestArchiverev_LiveSourcesOnly tests that workflow works with live sources only
// (archive retrieval disabled or no archive store configured).
func TestArchiverev_LiveSourcesOnly(t *testing.T) {
	ctx := context.Background()

	// Create client without archive store
	client := New(Config{})

	result, err := client.Retrieve(ctx, "test topic", []string{})
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}

	if result.HasArchive {
		t.Error("Expected HasArchive to be false when no archive store")
	}

	if len(result.Documents) != 0 {
		t.Errorf("Expected 0 documents, got %d", len(result.Documents))
	}
}

// TestArchiverev_ArchiveSupplement tests archive retrieval supplementing live sources.
func TestArchiverev_ArchiveSupplement(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	// Create fake archive store with sample data
	store := archive.NewFakeStore()

	doc := &archive.ArchiveDocument{
		StableID:      archive.StableDocumentID("arch-doc-1"),
		PlainText:     "Local election results show 65% turnout. Voter registration increased by 12% this cycle.",
		PlainTextHash: archive.ContentHash("hash-1"),
		ChunkCount:    1,
		SourceProvenance: archive.SourceProvenance{
			SourceID:    "source-1",
			SourceURL:   "https://archive.example.com/election-2023",
			SourceType:  types.SourceTypeWeb,
			RetrievedAt: now.AddDate(0, -6, 0),
			ExtractedAt: now.AddDate(0, -6, 0),
		},
		ArchivedAt: now.AddDate(0, -6, 0),
	}

	chunks := []archive.Chunk{
		{
			StableID:         archive.StableChunkID("arch_doc:arch-doc-1:hash-1:0:100"),
			DocumentStableID: archive.StableDocumentID("arch-doc-1"),
			ContentHash:      archive.ContentHash("chunk-1"),
			Position:         0,
			Length:           100,
			HasEmbedding:     false,
		},
	}

	if err := store.UpsertDocument(ctx, doc, chunks); err != nil {
		t.Fatalf("Failed to upsert document: %v", err)
	}

	// Configure retrieval client with the store
	client := New(Config{
		ArchiveStore: store,
	})

	// Perform retrieval
	result, err := client.Retrieve(ctx, "election results", []string{})
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}

	if !result.HasArchive {
		t.Error("Expected HasArchive to be true when archive data found")
	}

	if len(result.Documents) == 0 {
		t.Error("Expected at least one document from archive")
	}

	// Verify document structure
	retrievedDoc := result.Documents[0]
	if retrievedDoc.SourceID == "" {
		t.Error("Expected non-empty SourceID")
	}
	if retrievedDoc.PlainText == "" {
		t.Error("Expected non-empty PlainText")
	}
	if retrievedDoc.ExtractionMetadata == nil {
		t.Error("Expected non-empty ExtractionMetadata")
	}
	if retrievedDoc.ExtractionMetadata["source"] != "archive" {
		t.Errorf("Expected source=archive in metadata, got %q", retrievedDoc.ExtractionMetadata["source"])
	}
}

// TestArchiverev_ArchiveOnly tests archive-only retrieval when no live sources.
func TestArchiverev_ArchiveOnly(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	// Create fake archive store with sample data
	store := archive.NewFakeStore()

	doc := &archive.ArchiveDocument{
		StableID:      archive.StableDocumentID("arch-doc-1"),
		PlainText:     "City council approved new budget allocation for park renovation.",
		PlainTextHash: archive.ContentHash("hash-1"),
		ChunkCount:    1,
		SourceProvenance: archive.SourceProvenance{
			SourceID:    "source-1",
			SourceURL:   "https://archive.example.com/budget-2023",
			SourceType:  types.SourceTypeWeb,
			RetrievedAt: now.AddDate(0, -3, 0),
			ExtractedAt: now.AddDate(0, -3, 0),
		},
		ArchivedAt: now.AddDate(0, -3, 0),
	}

	chunks := []archive.Chunk{
		{
			StableID:         archive.StableChunkID("arch_doc:arch-doc-1:hash-1:0:80"),
			DocumentStableID: archive.StableDocumentID("arch-doc-1"),
			ContentHash:      archive.ContentHash("chunk-1"),
			Position:         0,
			Length:           80,
			HasEmbedding:     false,
		},
	}

	if err := store.UpsertDocument(ctx, doc, chunks); err != nil {
		t.Fatalf("Failed to upsert document: %v", err)
	}

	client := New(Config{
		ArchiveStore: store,
	})

	// Perform retrieval with no live URLs
	// Query must match the archived text (the fake store does simple text matching)
	result, err := client.Retrieve(ctx, "budget allocation", []string{})
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}

	if !result.HasArchive {
		t.Error("Expected HasArchive to be true")
	}

	if len(result.Documents) == 0 {
		t.Error("Expected documents from archive-only retrieval")
	}
}

// TestArchiverev_DuplicateSourceURL tests deduplication when archive and live sources overlap.
func TestArchiverev_DuplicateSourceURL(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	// Create fake archive store
	store := archive.NewFakeStore()

	doc := &archive.ArchiveDocument{
		StableID:      archive.StableDocumentID("arch-doc-1"),
		PlainText:     "This is archived content from the same URL.",
		PlainTextHash: archive.ContentHash("hash-1"),
		ChunkCount:    1,
		SourceProvenance: archive.SourceProvenance{
			SourceID:    "source-1",
			SourceURL:   "https://example.com/article",
			SourceType:  types.SourceTypeWeb,
			RetrievedAt: now.AddDate(0, -1, 0),
			ExtractedAt: now.AddDate(0, -1, 0),
		},
		ArchivedAt: now.AddDate(0, -1, 0),
	}

	chunks := []archive.Chunk{
		{
			StableID:         archive.StableChunkID("arch_doc:arch-doc-1:hash-1:0:50"),
			DocumentStableID: archive.StableDocumentID("arch-doc-1"),
			ContentHash:      archive.ContentHash("chunk-1"),
			Position:         0,
			Length:           50,
			HasEmbedding:     false,
		},
	}

	if err := store.UpsertDocument(ctx, doc, chunks); err != nil {
		t.Fatalf("Failed to upsert document: %v", err)
	}

	// Add another document with different URL
	doc2 := &archive.ArchiveDocument{
		StableID:      archive.StableDocumentID("arch-doc-2"),
		PlainText:     "This is from a different URL.",
		PlainTextHash: archive.ContentHash("hash-2"),
		ChunkCount:    1,
		SourceProvenance: archive.SourceProvenance{
			SourceID:    "source-2",
			SourceURL:   "https://different.example.com/article",
			SourceType:  types.SourceTypeWeb,
			RetrievedAt: now.AddDate(0, -1, 0),
			ExtractedAt: now.AddDate(0, -1, 0),
		},
		ArchivedAt: now.AddDate(0, -1, 0),
	}

	chunks2 := []archive.Chunk{
		{
			StableID:         archive.StableChunkID("arch_doc:arch-doc-2:hash-2:0:50"),
			DocumentStableID: archive.StableDocumentID("arch-doc-2"),
			ContentHash:      archive.ContentHash("chunk-2"),
			Position:         0,
			Length:           50,
			HasEmbedding:     false,
		},
	}

	if err := store.UpsertDocument(ctx, doc2, chunks2); err != nil {
		t.Fatalf("Failed to upsert document: %v", err)
	}

	// Configure client with deduplication enabled
	client := New(Config{
		ArchiveStore:     store,
		DeduplicateByURL: true,
	})

	// Perform retrieval with live URLs that include the first archive URL
	liveURLs := []string{
		"https://example.com/article", // This should be deduplicated
	}

	// Use query that matches the archived content
	result, err := client.Retrieve(ctx, "different URL", liveURLs)
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}

	// Should only have the second document (first was deduplicated)
	if len(result.Documents) != 1 {
		t.Errorf("Expected 1 document after deduplication, got %d", len(result.Documents))
	}

	docURL := ""
	if result.Documents[0].CanonicalURL != nil {
		docURL = *result.Documents[0].CanonicalURL
	}
	if docURL != "https://different.example.com/article" {
		t.Errorf("Expected only different.example.com URL after deduplication, got %q", docURL)
	}
}

// TestArchiverev_ArchiveRetrievalFailure tests graceful failure when archive query fails.
func TestArchiverev_ArchiveRetrievalFailure(t *testing.T) {
	ctx := context.Background()

	// Create a fake store that fails on retrieval
	store := archive.NewFakeStore()
	store.SetError("Retrieve", &archive.DocumentNotFoundError{StableID: "test"})

	client := New(Config{
		ArchiveStore: store,
	})

	// Perform retrieval - should return error
	result, err := client.Retrieve(ctx, "test", []string{})
	if err == nil {
		t.Error("Expected error from failed archive retrieval, got nil")
	}

	if result != nil {
		t.Error("Expected nil result on error, got non-nil")
	}
}

// TestArchiverev_BudgetLimits tests that max sources and max chunks budgets are enforced.
func TestArchiverev_BudgetLimits(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	// Create fake archive store with multiple documents
	store := archive.NewFakeStore()

	for i := 1; i <= 10; i++ {
		doc := &archive.ArchiveDocument{
			StableID:      archive.StableDocumentID("arch-doc-" + string(rune('0'+i))),
			PlainText:     "Document " + string(rune('0'+i)) + " content.",
			PlainTextHash: archive.ContentHash("hash-" + string(rune('0'+i))),
			ChunkCount:    1,
			SourceProvenance: archive.SourceProvenance{
				SourceID:    "source-" + string(rune('0'+i)),
				SourceURL:   "https://example" + string(rune('0'+i)) + ".com",
				SourceType:  types.SourceTypeWeb,
				RetrievedAt: now,
				ExtractedAt: now,
			},
			ArchivedAt: now,
		}

		chunks := []archive.Chunk{
			{
				StableID:         archive.StableChunkID("arch_doc:arch-doc-" + string(rune('0'+i)) + ":hash-" + string(rune('0'+i)) + ":0:50"),
				DocumentStableID: archive.StableDocumentID("arch-doc-" + string(rune('0'+i))),
				ContentHash:      archive.ContentHash("chunk-1"),
				Position:         0,
				Length:           50,
				HasEmbedding:     false,
			},
		}

		if err := store.UpsertDocument(ctx, doc, chunks); err != nil {
			t.Fatalf("Failed to upsert document: %v", err)
		}
	}

	// Test max sources budget
	client := New(Config{
		ArchiveStore:       store,
		MaxArchivedSources: 3, // Should only get 3 sources
	})

	// Use query matching archived content
	result, err := client.Retrieve(ctx, "Document", []string{})
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}

	if len(result.Documents) > 3 {
		t.Errorf("Expected at most 3 documents (max sources=3), got %d", len(result.Documents))
	}

	if len(result.SourceIDs) > 3 {
		t.Errorf("Expected at most 3 source IDs, got %d", len(result.SourceIDs))
	}

	// Test max chunks budget
	client2 := New(Config{
		ArchiveStore:       store,
		MaxArchivedChunks:  5,  // Should only get 5 chunks
		MaxArchivedSources: 10, // Don't limit sources
	})

	result2, err := client2.Retrieve(ctx, "Document", []string{})
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}

	if len(result2.Documents) > 5 {
		t.Errorf("Expected at most 5 documents (max chunks=5), got %d", len(result2.Documents))
	}

	// Test max chunk size budget
	client3 := New(Config{
		ArchiveStore:       store,
		MaxArchivedSources: 10,
		MaxArchivedChunks:  10,
		MaxChunkSize:       20, // Should truncate long documents
	})

	result3, err := client3.Retrieve(ctx, "Document", []string{})
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}

	for _, doc := range result3.Documents {
		if len(doc.PlainText) > 20 {
			t.Errorf("Document PlainText length %d exceeds max 20", len(doc.PlainText))
		}
	}
}

// TestArchiverev_BudgetChecker tests the BudgetChecker functionality.
func TestArchiverev_BudgetChecker(t *testing.T) {
	bc := NewBudgetChecker(5, 1000) // Max 5 sources, 1000 chars

	// Initial state
	if !bc.CanAddSource() {
		t.Error("Expected CanAddSource() to be true initially")
	}
	if !bc.CanAddContext() {
		t.Error("Expected CanAddContext() to be true initially")
	}

	// Add sources
	bc.AddSource()
	bc.AddSource()
	if bc.SourceCount() != 2 {
		t.Errorf("Expected SourceCount=2, got %d", bc.SourceCount())
	}

	// Reach source limit
	bc.AddSource()
	bc.AddSource()
	bc.AddSource()
	if bc.CanAddSource() {
		t.Error("Expected CanAddSource() to be false after reaching limit")
	}

	// Add context
	bc.AddContext(500)
	if bc.ContextSize() != 500 {
		t.Errorf("Expected ContextSize=500, got %d", bc.ContextSize())
	}

	bc.AddContext(600)
	if bc.CanAddContext() {
		t.Error("Expected CanAddContext() to be false after reaching limit")
	}
}

// TestArchiverev_NoMatchingQuery tests retrieval with no matching query.
func TestArchiverev_NoMatchingQuery(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	// Create fake archive store with sample data
	store := archive.NewFakeStore()

	doc := &archive.ArchiveDocument{
		StableID:      archive.StableDocumentID("arch-doc-1"),
		PlainText:     "This is about unrelated topic X.",
		PlainTextHash: archive.ContentHash("hash-1"),
		ChunkCount:    1,
		SourceProvenance: archive.SourceProvenance{
			SourceID:    "source-1",
			SourceURL:   "https://example.com/unrelated",
			SourceType:  types.SourceTypeWeb,
			RetrievedAt: now,
			ExtractedAt: now,
		},
		ArchivedAt: now,
	}

	chunks := []archive.Chunk{
		{
			StableID:         archive.StableChunkID("arch_doc:arch-doc-1:hash-1:0:30"),
			DocumentStableID: archive.StableDocumentID("arch-doc-1"),
			ContentHash:      archive.ContentHash("chunk-1"),
			Position:         0,
			Length:           30,
			HasEmbedding:     false,
		},
	}

	if err := store.UpsertDocument(ctx, doc, chunks); err != nil {
		t.Fatalf("Failed to upsert document: %v", err)
	}

	client := New(Config{
		ArchiveStore: store,
	})

	// Query for something not in archive
	result, err := client.Retrieve(ctx, "xyz non-matching topic", []string{})
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}

	if result.HasArchive {
		t.Error("Expected HasArchive=false when no matching documents found")
	}

	if len(result.Documents) != 0 {
		t.Errorf("Expected 0 documents for non-matching query, got %d", len(result.Documents))
	}
}

// TestArchiverev_DefaultConfigValues tests default values for configuration.
func TestArchiverev_DefaultConfigValues(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	// Create archive store with 10 documents (to test defaults)
	store := archive.NewFakeStore()

	for i := 0; i < 10; i++ {
		doc := &archive.ArchiveDocument{
			StableID:      archive.StableDocumentID("arch-doc-" + string(rune('0'+i))),
			PlainText:     "Content for document " + string(rune('0'+i)),
			PlainTextHash: archive.ContentHash("hash-" + string(rune('0'+i))),
			ChunkCount:    1,
			SourceProvenance: archive.SourceProvenance{
				SourceID:    "source-" + string(rune('0'+i)),
				SourceURL:   "https://example" + string(rune('0'+i)) + ".com",
				SourceType:  types.SourceTypeWeb,
				RetrievedAt: now,
				ExtractedAt: now,
			},
			ArchivedAt: now,
		}

		chunks := []archive.Chunk{
			{
				StableID:         archive.StableChunkID("arch_doc:arch-doc-" + string(rune('0'+i)) + ":hash-" + string(rune('0'+i)) + ":0:50"),
				DocumentStableID: archive.StableDocumentID("arch-doc-" + string(rune('0'+i))),
				ContentHash:      archive.ContentHash("chunk-1"),
				Position:         0,
				Length:           50,
				HasEmbedding:     false,
			},
		}

		if err := store.UpsertDocument(ctx, doc, chunks); err != nil {
			t.Fatalf("Failed to upsert document: %v", err)
		}
	}

	// Use default config (no explicit limits)
	client := New(Config{
		ArchiveStore: store,
	})

	result, err := client.Retrieve(ctx, "Document", []string{})
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}

	// Default MaxArchivedSources is 5
	if len(result.Documents) > 5 {
		t.Errorf("Expected max 5 documents with default config, got %d", len(result.Documents))
	}

	// Default MaxArchivedChunks is 20
	// With 1 doc per source and 5 sources, we should get 5 chunks
	if len(result.Documents) > 20 {
		t.Errorf("Expected max 20 documents (chunks) with default config, got %d", len(result.Documents))
	}
}

// TestArchiverev_ExtractChunkText tests the extractChunkText helper.
func TestArchiverev_ExtractChunkText(t *testing.T) {
	client := New(Config{})

	// Test with simple document
	docText := "This is the first sentence. This is the second sentence."
	chunk := archive.Chunk{
		Position: 0,
		Length:   50,
	}

	chunkText := client.extractChunkText(docText, chunk)
	if chunkText != docText {
		t.Errorf("Expected chunkText to be full docText (simple impl), got %q", chunkText)
	}
}
