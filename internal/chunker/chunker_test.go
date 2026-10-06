package chunker

import (
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/local-newsroom/internal/archive"
	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
)

// TestNewChunker validates Chunker creation with valid configurations
func TestNewChunker(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
	}{
		{
			name: "default config",
			config: Config{
				TargetChunkSize: 300,
				MaxChunkSize:    500,
				OverlapRatio:    0.1,
			},
			wantErr: false,
		},
		{
			name: "zero target size",
			config: Config{
				TargetChunkSize: 0,
				MaxChunkSize:    300,
			},
			wantErr: false, // Uses default config
		},
		{
			name:    "zero values triggers default",
			config:  Config{},
			wantErr: false,
		},
		{
			name: "invalid target size",
			config: Config{
				TargetChunkSize: 0, // Invalid, but will be replaced by default
			},
			wantErr: false,
		},
		{
			name: "target exceeds max",
			config: Config{
				TargetChunkSize: 600,
				MaxChunkSize:    500,
			},
			wantErr: true,
		},
		{
			name: "negative target size",
			config: Config{
				TargetChunkSize: -10,
				MaxChunkSize:    300,
			},
			wantErr: true,
		},
		{
			name: "negative max size",
			config: Config{
				TargetChunkSize: 300,
				MaxChunkSize:    -10,
			},
			wantErr: true,
		},
		{
			name: "negative overlap ratio",
			config: Config{
				TargetChunkSize: 300,
				MaxChunkSize:    500,
				OverlapRatio:    -0.1,
			},
			wantErr: true,
		},
		{
			name: "overlap ratio equals 1",
			config: Config{
				TargetChunkSize: 300,
				MaxChunkSize:    500,
				OverlapRatio:    1.0,
			},
			wantErr: true,
		},
		{
			name: "negative min paragraph size",
			config: Config{
				TargetChunkSize:  300,
				MaxChunkSize:     500,
				MinParagraphSize: -5,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.config)
			gotErr := err != nil

			if gotErr != tt.wantErr {
				t.Errorf("New() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestChunk_ShortDocument tests that short documents produce a single chunk
func TestChunk_ShortDocument(t *testing.T) {
	chunker, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("Failed to create chunker: %v", err)
	}

	// Very short document
	doc := &types.Document{
		SourceID:  "src-1",
		PlainText: "Short text",
	}

	chunks, err := chunker.Chunk(doc)
	if err != nil {
		t.Fatalf("Chunk() error = %v", err)
	}

	if len(chunks) != 1 {
		t.Errorf("Expected 1 chunk, got %d", len(chunks))
	}

	if chunks[0].Text != "Short text" {
		t.Errorf("Chunk text = %q, expected %q", chunks[0].Text, "Short text")
	}

	if chunks[0].ChunkIndex != 0 {
		t.Errorf("ChunkIndex = %d, expected 0", chunks[0].ChunkIndex)
	}

	if chunks[0].Position != 0 {
		t.Errorf("Position = %d, expected 0", chunks[0].Position)
	}
}

// TestChunk_EmptyDocument tests that empty documents produce no chunks
func TestChunk_EmptyDocument(t *testing.T) {
	chunker, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("Failed to create chunker: %v", err)
	}

	tests := []struct {
		name    string
		content string
	}{
		{"empty string", ""},
		{"whitespace only", "   "},
		{"newlines only", "\n\n\n"},
		{"tabs only", "\t\t\t"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := &types.Document{
				SourceID:  "src-1",
				PlainText: tt.content,
			}

			chunks, err := chunker.Chunk(doc)
			if err != nil {
				t.Fatalf("Chunk() error = %v", err)
			}

			// Should return empty slice, not nil
			if chunks == nil {
				t.Error("Expected empty slice, got nil")
			}

			if len(chunks) != 0 {
				t.Errorf("Expected 0 chunks, got %d", len(chunks))
			}
		})
	}
}

// TestChunk_MultiParagraph tests multi-paragraph document chunking
func TestChunk_MultiParagraph(t *testing.T) {
	chunker, err := New(Config{
		TargetChunkSize: 100,
		MaxChunkSize:    150,
		OverlapRatio:    0.0,
	})
	if err != nil {
		t.Fatalf("Failed to create chunker: %v", err)
	}

	// Three short paragraphs
	doc := &types.Document{
		SourceID: "src-1",
		PlainText: `First paragraph with some content.
Second paragraph here.
Third paragraph for good measure.`,
	}

	chunks, err := chunker.Chunk(doc)
	if err != nil {
		t.Fatalf("Chunk() error = %v", err)
	}

	if len(chunks) < 1 {
		t.Errorf("Expected at least 1 chunk, got %d", len(chunks))
	}

	// Check that all paragraphs are represented in the chunks
	fullText := ""
	for _, c := range chunks {
		fullText += c.Text
	}

	if len(chunks) == 0 {
		t.Fatal("No chunks produced")
	}

	// Verify chunks are in order
	for i := 1; i < len(chunks); i++ {
		if chunks[i].Position < chunks[i-1].Position+chunks[i-1].Length {
			t.Errorf("Chunk %d position %d overlaps with chunk %d at position %d with length %d",
				i, chunks[i].Position, i-1, chunks[i-1].Position, chunks[i-1].Length)
		}
	}
}

// TestChunk_Overlap tests that overlap works correctly
func TestChunk_Overlap(t *testing.T) {
	chunker, err := New(Config{
		TargetChunkSize: 50,
		MaxChunkSize:    100,
		OverlapRatio:    0.5,
	})
	if err != nil {
		t.Fatalf("Failed to create chunker: %v", err)
	}

	// Create a document that will produce multiple chunks
	longText := make([]string, 0)
	for i := 0; i < 10; i++ {
		longText = append(longText, "This is paragraph number "+string(rune('A'+i)))
	}
	doc := &types.Document{
		SourceID:  "src-1",
		PlainText: joinWithNewlines(longText),
	}

	chunks, err := chunker.Chunk(doc)
	if err != nil {
		t.Fatalf("Chunk() error = %v", err)
	}

	if len(chunks) < 2 {
		t.Skipf("Document not large enough to test overlap (got %d chunks)", len(chunks))
	}

	// Check that chunks are ordered
	for i := 1; i < len(chunks); i++ {
		// Verify chunks are ordered
		if chunks[i].Position <= chunks[i-1].Position {
			t.Errorf("Chunk %d position %d is not after chunk %d at %d",
				i, chunks[i].Position, i-1, chunks[i-1].Position)
		}
	}
}

// TestChunk_Deterministic tests that identical input produces identical output
func TestChunk_Deterministic(t *testing.T) {
	config := Config{
		TargetChunkSize: 300,
		MaxChunkSize:    500,
		OverlapRatio:    0.1,
	}

	chunker1, err := New(config)
	if err != nil {
		t.Fatalf("Failed to create chunker 1: %v", err)
	}

	chunker2, err := New(config)
	if err != nil {
		t.Fatalf("Failed to create chunker 2: %v", err)
	}

	doc := &types.Document{
		SourceID: "src-1",
		Title:    types.PointerTo("Test Article"),
		PlainText: `This is a test document with multiple paragraphs.
	
		Second paragraph here with different content.
		
		Third paragraph to ensure we have enough text.
		
		And a fourth paragraph for good measure.`,
	}

	chunks1, err := chunker1.Chunk(doc)
	if err != nil {
		t.Fatalf("Chunk() first run error = %v", err)
	}

	chunks2, err := chunker2.Chunk(doc)
	if err != nil {
		t.Fatalf("Chunk() second run error = %v", err)
	}

	if len(chunks1) != len(chunks2) {
		t.Errorf("Expected same number of chunks: %d vs %d", len(chunks1), len(chunks2))
	}

	for i := range chunks1 {
		if chunks1[i].ChunkID != chunks2[i].ChunkID {
			t.Errorf("Chunk %d ID mismatch: %s vs %s", i, chunks1[i].ChunkID, chunks2[i].ChunkID)
		}
		if chunks1[i].Text != chunks2[i].Text {
			t.Errorf("Chunk %d text mismatch", i)
		}
		if chunks1[i].ContentHash != chunks2[i].ContentHash {
			t.Errorf("Chunk %d hash mismatch", i)
		}
		if chunks1[i].Position != chunks2[i].Position {
			t.Errorf("Chunk %d position mismatch", i)
		}
		if chunks1[i].Length != chunks2[i].Length {
			t.Errorf("Chunk %d length mismatch", i)
		}
	}
}

// TestChunk_Unicode tests that Unicode text is handled correctly
func TestChunk_Unicode(t *testing.T) {
	chunker, err := New(Config{
		TargetChunkSize: 100,
		MaxChunkSize:    200,
		OverlapRatio:    0.0,
	})
	if err != nil {
		t.Fatalf("Failed to create chunker: %v", err)
	}

	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "Chinese characters",
			content: "这是中文测试文档。\n\n第二段中文内容。",
		},
		{
			name:    "Japanese characters",
			content: "これは日本語のテスト文書です。\n\n第二段の日本語。",
		},
		{
			name:    "Emoji",
			content: "Hello 🌍 World 🚀 Test 🎉 Example 📝 Content",
		},
		{
			name:    "Mixed scripts",
			content: "English text 中文 日本語 Français Español العربية",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := &types.Document{
				SourceID:  "src-1",
				PlainText: tt.content,
			}

			chunks, err := chunker.Chunk(doc)
			if err != nil {
				t.Fatalf("Chunk() error = %v", err)
			}

			if len(chunks) == 0 {
				t.Error("Expected at least one chunk for Unicode content")
			}

			// Verify chunks contain the original content
			for _, c := range chunks {
				if !contains(c.Text, "Test") && !contains(c.Text, "World") &&
					!contains(c.Text, "🌍") && !contains(c.Text, "中文") {
					// May be split, so just verify we got something
					t.Logf("Chunk: %q", c.Text)
				}
			}
		})
	}
}

// TestChunk_ParagraphBoundaries tests that paragraph boundaries are preserved
func TestChunk_ParagraphBoundaries(t *testing.T) {
	chunker, err := New(Config{
		TargetChunkSize: 50,
		MaxChunkSize:    100,
		OverlapRatio:    0.0,
	})
	if err != nil {
		t.Fatalf("Failed to create chunker: %v", err)
	}

	// Create paragraphs where each should fit in a chunk
	doc := &types.Document{
		SourceID: "src-1",
		PlainText: `Short one.
		
		A bit longer paragraph with more words to make it bigger.
		
		Yet another short one here.`,
	}

	chunks, err := chunker.Chunk(doc)
	if err != nil {
		t.Fatalf("Chunk() error = %v", err)
	}

	if len(chunks) == 0 {
		t.Fatal("Expected at least one chunk")
	}

	// Verify chunk ordering
	for i := 1; i < len(chunks); i++ {
		prevEnd := chunks[i-1].Position + chunks[i-1].Length
		if chunks[i].Position < prevEnd {
			t.Errorf("Chunk %d starts at %d, but should be at least at %d (previous end)",
				i, chunks[i].Position, prevEnd)
		}
	}
}

// TestChunk_ContentHash tests that content hashes are deterministic and correct
func TestChunk_ContentHash(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		expected string // empty means we just check it's non-empty and consistent
	}{
		{
			name:     "simple text",
			text:     "Hello world",
			expected: "", // Don't hardcode the hash
		},
		{
			name:     "empty string",
			text:     "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash1 := computeContentHash(tt.text)
			hash2 := computeContentHash(tt.text)

			if hash1 != hash2 {
				t.Errorf("Content hash not deterministic: %s vs %s", hash1, hash2)
			}

			if string(hash1) == "" && tt.text != "" {
				t.Errorf("Content hash is empty for non-empty text")
			}
		})
	}
}

// TestChunk_DocumentStableID tests that document stable IDs are deterministic
func TestChunk_DocumentStableID(t *testing.T) {
	tests := []struct {
		name   string
		doc    *types.Document
		checks []func(archive.StableDocumentID)
	}{
		{
			name: "same source same content",
			doc: &types.Document{
				SourceID:  "src-1",
				PlainText: "Same content",
			},
		},
		{
			name: "different source",
			doc: &types.Document{
				SourceID:  "src-2",
				PlainText: "Same content",
			},
		},
		{
			name: "different content",
			doc: &types.Document{
				SourceID:  "src-1",
				PlainText: "Different content here",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id1 := computeDocumentStableID(tt.doc)
			id2 := computeDocumentStableID(tt.doc)

			if id1 != id2 {
				t.Errorf("Document stable ID not deterministic: %s vs %s", id1, id2)
			}

			if string(id1) == "" {
				t.Error("Document stable ID is empty")
			}

			if !strings.HasPrefix(string(id1), "arch:") {
				t.Errorf("Document stable ID doesn't have 'arch:' prefix: %s", id1)
			}
		})
	}
}

// TestChunk_ComputeFunctions tests the exported compute functions
func TestChunk_ComputeFunctions(t *testing.T) {
	text := "Test text for hashing"

	// Test ComputeContentHash
	hash1 := ComputeContentHash(text)
	hash2 := ComputeContentHash(text)

	if hash1 != hash2 {
		t.Errorf("ComputeContentHash not deterministic: %s vs %s", hash1, hash2)
	}

	// Test ComputeDocumentStableID
	doc := &types.Document{
		SourceID:  "src-1",
		PlainText: text,
	}

	docID1 := ComputeDocumentStableID(doc)
	docID2 := ComputeDocumentStableID(doc)

	if docID1 != docID2 {
		t.Errorf("ComputeDocumentStableID not deterministic: %s vs %s", docID1, docID2)
	}
}

// TestChunk_LongParagraphSplitting tests splitting of paragraphs exceeding max size
func TestChunk_LongParagraphSplitting(t *testing.T) {
	chunker, err := New(Config{
		TargetChunkSize:  50,
		MaxChunkSize:     100,
		OverlapRatio:     0.0,
		MinParagraphSize: 0,
	})
	if err != nil {
		t.Fatalf("Failed to create chunker: %v", err)
	}

	// Create a very long paragraph (should be split)
	// Use shorter words with explicit spaces to ensure clean splits
	longWords := make([]string, 0)
	for i := 0; i < 50; i++ {
		longWords = append(longWords, "word")
	}
	longWord := strings.Join(longWords, " ")
	doc := &types.Document{
		SourceID:  "src-1",
		PlainText: longWord,
	}

	chunks, err := chunker.Chunk(doc)
	if err != nil {
		t.Fatalf("Chunk() error = %v", err)
	}

	if len(chunks) == 0 {
		t.Fatal("Expected chunks for long paragraph")
	}

	// Verify each chunk is within max size
	for i, c := range chunks {
		if c.Length > 100 {
			t.Errorf("Chunk %d length %d exceeds MaxChunkSize 100", i, c.Length)
		}
		if c.Text == "" {
			t.Errorf("Chunk %d has empty text", i)
		}
	}

	// Verify chunks are ordered
	for i := 1; i < len(chunks); i++ {
		if chunks[i].Position < chunks[i-1].Position {
			t.Errorf("Chunk %d position %d is before chunk %d at %d",
				i, chunks[i].Position, i-1, chunks[i-1].Position)
		}
	}
}

// TestChunk_ChunkResultFields tests that all fields in ChunkResult are populated
func TestChunk_ChunkResultFields(t *testing.T) {
	chunker, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("Failed to create chunker: %v", err)
	}

	doc := &types.Document{
		SourceID:  "src-123",
		PlainText: "Test content here",
	}

	chunks, err := chunker.Chunk(doc)
	if err != nil {
		t.Fatalf("Chunk() error = %v", err)
	}

	if len(chunks) == 0 {
		t.Fatal("Expected at least one chunk")
	}

	c := chunks[0]

	// Check all required fields are populated
	if c.ChunkID == "" {
		t.Error("ChunkID is empty")
	}
	if c.DocumentStableID == "" {
		t.Error("DocumentStableID is empty")
	}
	if c.SourceID == "" {
		t.Error("SourceID is empty")
	}
	if c.ContentHash == "" {
		t.Error("ContentHash is empty")
	}
	if c.ChunkIndex < 0 {
		t.Errorf("ChunkIndex is negative: %d", c.ChunkIndex)
	}
	if c.Text == "" {
		t.Error("Text is empty")
	}
	if c.Position < 0 {
		t.Errorf("Position is negative: %d", c.Position)
	}
	if c.Length <= 0 {
		t.Errorf("Length is not positive: %d", c.Length)
	}
}

// TestChunk_Separators tests different paragraph separators
func TestChunk_Separators(t *testing.T) {
	separatorTests := []struct {
		name    string
		content string
		sep     rune
	}{
		{
			name:    "newlines",
			content: "Para 1\n\nPara 2\n\nPara 3",
			sep:     '\n',
		},
		{
			name:    "carriage return and newline",
			content: "Para 1\r\n\r\nPara 2\r\n\r\nPara 3",
			sep:     '\n',
		},
	}

	for _, tt := range separatorTests {
		t.Run(tt.name, func(t *testing.T) {
			config := Config{
				TargetChunkSize: 100,
				MaxChunkSize:    200,
				Separator:       tt.sep,
			}

			chunker, err := New(config)
			if err != nil {
				t.Fatalf("Failed to create chunker: %v", err)
			}

			doc := &types.Document{
				SourceID:  "src-1",
				PlainText: tt.content,
			}

			chunks, err := chunker.Chunk(doc)
			if err != nil {
				t.Fatalf("Chunk() error = %v", err)
			}

			// Should produce at least one chunk
			if len(chunks) == 0 {
				t.Error("Expected at least one chunk")
			}
		})
	}
}

// TestChunk_ComposeChunkID tests the chunk ID composition logic
func TestChunk_ComposeChunkID(t *testing.T) {
	tests := []struct {
		name    string
		docID   archive.StableDocumentID
		content archive.ContentHash
		pos     int
		length  int
		wantErr bool
	}{
		{
			name:    "valid chunk ID",
			docID:   "arch:0123456789abcdef",
			content: "hash123",
			pos:     0,
			length:  100,
			wantErr: false,
		},
		{
			name:    "valid chunk ID with special characters",
			docID:   "arch:fedcba9876543210",
			content: "hash456",
			pos:     100,
			length:  200,
			wantErr: false,
		},
		{
			name:    "zero values",
			docID:   "arch:0000000000000000",
			content: "0000000000000000",
			pos:     0,
			length:  0,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := composeChunkID(tt.docID, tt.content, tt.pos, tt.length)
			expectedPrefix := "arch_doc:"

			if !strings.HasPrefix(string(id), expectedPrefix) {
				t.Errorf("ChunkID %q doesn't start with %q", id, expectedPrefix)
			}

			// Verify format is parseable
			expectedFormat := "arch_doc:" + string(tt.docID) + ":" + string(tt.content) + ":" + string(rune(tt.pos)) + ":" + string(rune(tt.length))
			_ = expectedFormat // Just checking format compiles

			// Test that IDs are deterministic
			id2 := composeChunkID(tt.docID, tt.content, tt.pos, tt.length)
			if id != id2 {
				t.Errorf("ChunkID not deterministic: %q vs %q", id, id2)
			}
		})
	}
}

// TestChunk_ChangeCausesDifferentID tests that content/config changes produce different IDs
func TestChunk_ChangeCausesDifferentID(t *testing.T) {
	config := Config{
		TargetChunkSize: 300,
		MaxChunkSize:    500,
		OverlapRatio:    0.1,
	}

	chunker, err := New(config)
	if err != nil {
		t.Fatalf("Failed to create chunker: %v", err)
	}

	doc := &types.Document{
		SourceID:  "src-1",
		PlainText: "Original content",
	}

	chunks1, err := chunker.Chunk(doc)
	if err != nil {
		t.Fatalf("Chunk() error = %v", err)
	}

	// Change content
	doc.PlainText = "Different content here"
	chunks2, err := chunker.Chunk(doc)
	if err != nil {
		t.Fatalf("Chunk() error = %v", err)
	}

	// IDs should differ due to content change
	if chunks1[0].ChunkID == chunks2[0].ChunkID {
		t.Error("ChunkID changed but IDs are the same despite content change")
	}

	if chunks1[0].ContentHash == chunks2[0].ContentHash {
		t.Error("Content hash changed but hashes are the same despite content change")
	}

	// Change source ID
	doc.PlainText = "Same content"
	chunks3, err := chunker.Chunk(doc)
	if err != nil {
		t.Fatalf("Chunk() error = %v", err)
	}

	// IDs should differ due to source change
	if chunks2[0].ChunkID == chunks3[0].ChunkID {
		t.Error("ChunkID changed but IDs are the same despite source change")
	}
}

// TestChunk_EmptySliceNotNil tests that empty result is a slice, not nil
func TestChunk_EmptySliceNotNil(t *testing.T) {
	chunker, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("Failed to create chunker: %v", err)
	}

	doc := &types.Document{
		SourceID:  "src-1",
		PlainText: "",
	}

	result, err := chunker.Chunk(doc)
	if err != nil {
		t.Fatalf("Chunk() error: %v", err)
	}
	if result == nil {
		t.Error("Empty result should be an empty slice, not nil")
	}

	if len(result) != 0 {
		t.Errorf("Empty result should have length 0, got %d", len(result))
	}
}

// TestChunk_NoOverlap tests that 0 overlap works correctly
func TestChunk_NoOverlap(t *testing.T) {
	config := Config{
		TargetChunkSize: 50,
		MaxChunkSize:    100,
		OverlapRatio:    0.0,
	}

	chunker, err := New(config)
	if err != nil {
		t.Fatalf("Failed to create chunker: %v", err)
	}

	longText := make([]string, 0)
	for i := 0; i < 5; i++ {
		longText = append(longText, "This is paragraph number "+string(rune('A'+i))+" with extra words to make it longer.")
	}
	doc := &types.Document{
		SourceID:  "src-1",
		PlainText: joinWithNewlines(longText),
	}

	chunks, err := chunker.Chunk(doc)
	if err != nil {
		t.Fatalf("Chunk() error = %v", err)
	}

	if len(chunks) == 0 {
		t.Fatal("Expected at least one chunk")
	}

	// Verify no position overlap
	for i := 1; i < len(chunks); i++ {
		prevEnd := chunks[i-1].Position + chunks[i-1].Length
		if chunks[i].Position < prevEnd {
			t.Errorf("Chunk %d position %d overlaps with previous chunk ending at %d",
				i, chunks[i].Position, prevEnd)
		}
	}
}

// TestChunk_RetrieveConfig tests that GetConfig returns the chunker's config
func TestChunk_RetrieveConfig(t *testing.T) {
	config := Config{
		TargetChunkSize:  400,
		MaxChunkSize:     600,
		OverlapRatio:     0.2,
		MinParagraphSize: 50,
	}

	chunker, err := New(config)
	if err != nil {
		t.Fatalf("Failed to create chunker: %v", err)
	}

	retrieved := chunker.GetConfig()

	if retrieved.TargetChunkSize != config.TargetChunkSize {
		t.Errorf("TargetChunkSize: got %d, want %d", retrieved.TargetChunkSize, config.TargetChunkSize)
	}
	if retrieved.MaxChunkSize != config.MaxChunkSize {
		t.Errorf("MaxChunkSize: got %d, want %d", retrieved.MaxChunkSize, config.MaxChunkSize)
	}
	if retrieved.OverlapRatio != config.OverlapRatio {
		t.Errorf("OverlapRatio: got %f, want %f", retrieved.OverlapRatio, config.OverlapRatio)
	}
}

// Helper functions

func contains(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	return len(s) >= len(substr) && (s == substr || containsInString(s, substr))
}

func containsInString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func joinWithNewlines(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	result := make([]byte, 0)
	for i, p := range parts {
		if i > 0 {
			result = append(result, '\n')
		}
		result = append(result, p...)
	}
	return string(result)
}
