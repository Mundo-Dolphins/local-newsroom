// Package chunker provides deterministic chunking of normalized Document content
// for embedding and retrieval.
//
// This package implements a simple, configurable chunking strategy that:
//   - Operates on already-normalized plain text
//   - Preserves paragraph boundaries where practical
//   - Uses configurable target/max size and overlap
//   - Never splits into empty chunks
//   - Assigns deterministic chunk IDs and order
//   - Preserves document/source provenance
//   - Produces identical chunks for identical input/config
//   - Avoids dependence on an LLM or tokenizer service
//
// The chunking algorithm:
//  1. Splits text into paragraphs (blank-line-separated)
//  2. If a paragraph fits within target size, use it as a chunk
//  3. If a paragraph is too long, split it at word boundaries to fit
//  4. Apply overlap between consecutive chunks
//  5. Compute deterministic chunk IDs from document ID, content hash, position, and length
//
// Usage example:
//
//	config := chunker.NewConfig()
//	config.TargetChunkSize = 200
//	config.MaxChunkSize = 300
//	config.OverlapRatio = 0.1
//
//	chunkerInstance := chunker.New(config)
//	chunks, err := chunkerInstance.Chunk(document)
//
//	chunks can then be used with archive.Store.UpsertDocument()
//
// The chunk ID format is: "arch_doc:<document_id>:<content_hash>:<position>:<length>"
package chunker

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"unicode"

	"github.com/Mundo-Dolphins/local-newsroom/internal/archive"
	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
)

// Config holds the chunking configuration parameters.
type Config struct {
	// TargetChunkSize is the target number of runes for each chunk.
	// Chunks will aim for this size but may be smaller or larger depending
	// on paragraph boundaries and overlap.
	TargetChunkSize int

	// MaxChunkSize is the maximum number of runes allowed in a chunk.
	// Even if paragraphs exceed this, chunks will not exceed this limit.
	MaxChunkSize int

	// OverlapRatio is the fraction of each chunk that overlaps with the previous chunk.
	// Valid values are 0.0 to 0.99. A value of 0.1 means 10% overlap.
	// If 0, no overlap is applied.
	OverlapRatio float64

	// MinParagraphSize is the minimum number of runes a paragraph should have
	// before it's considered for splitting. Paragraphs smaller than this are
	// kept intact even if they would otherwise be merged.
	MinParagraphSize int

	// Separator is the character used to separate paragraphs during splitting.
	// Typically a newline, but can be customized.
	Separator rune
}

// Validate validates the configuration and returns an error if invalid.
func (c *Config) Validate() error {
	if c.TargetChunkSize <= 0 {
		return fmt.Errorf("TargetChunkSize must be positive, got %d", c.TargetChunkSize)
	}
	if c.MaxChunkSize <= 0 {
		return fmt.Errorf("MaxChunkSize must be positive, got %d", c.MaxChunkSize)
	}
	if c.TargetChunkSize > c.MaxChunkSize {
		return fmt.Errorf("TargetChunkSize %d cannot exceed MaxChunkSize %d",
			c.TargetChunkSize, c.MaxChunkSize)
	}
	if c.OverlapRatio < 0 || c.OverlapRatio >= 1 {
		return fmt.Errorf("OverlapRatio must be in [0, 1), got %f", c.OverlapRatio)
	}
	if c.MinParagraphSize < 0 {
		return fmt.Errorf("MinParagraphSize must be non-negative, got %d", c.MinParagraphSize)
	}
	return nil
}

// Chunker implements deterministic chunking of Document content.
type Chunker struct {
	config Config
}

// New creates a new Chunker with the given configuration.
// If config is zero-valued, DefaultConfig() is used.
func New(config Config) (*Chunker, error) {
	if config.TargetChunkSize == 0 {
		config = DefaultConfig()
	}

	if err := config.Validate(); err != nil {
		return nil, err
	}

	return &Chunker{config: config}, nil
}

// DefaultConfig returns sensible default chunking parameters.
func DefaultConfig() Config {
	return Config{
		TargetChunkSize:  300,
		MaxChunkSize:     500,
		OverlapRatio:     0.1,
		MinParagraphSize: 20,
		Separator:        '\n',
	}
}

// ChunkResult represents a single chunk of document content with all metadata.
type ChunkResult struct {
	// ChunkID is the deterministic stable ID for this chunk.
	// Format: "arch_doc:<document_id>:<content_hash>:<position>:<length>"
	ChunkID archive.StableChunkID

	// DocumentStableID is the parent document's stable ID.
	DocumentStableID archive.StableDocumentID

	// SourceID is the original source stable ID from the document.
	SourceID string

	// SourceURL is the original source URL for provenance.
	SourceURL string

	// ChunkIndex is the zero-based index of this chunk within the document.
	ChunkIndex int

	// Text is the chunked content.
	Text string

	// ContentHash is the SHA-256 hash of the chunk text.
	ContentHash archive.ContentHash

	// Position is the starting character offset of this chunk in the original document.
	Position int

	// Length is the length of this chunk in characters.
	Length int

	// ParagraphStart indicates the paragraph index where this chunk begins.
	// If a chunk spans multiple paragraphs, this is the first paragraph index.
	ParagraphStart int

	// ParagraphEnd indicates the paragraph index where this chunk ends.
	// If a chunk spans multiple paragraphs, this is the last paragraph index.
	ParagraphEnd int
}

// Chunk splits a document into chunks according to the configured strategy.
// Returns an empty slice (not nil) if the document is empty.
func (c *Chunker) Chunk(doc *types.Document) ([]ChunkResult, error) {
	plainText := doc.PlainText

	// Handle empty or whitespace-only documents
	if strings.TrimSpace(plainText) == "" {
		return []ChunkResult{}, nil
	}

	// Derive document stable ID from source ID and content hash
	docID := computeDocumentStableID(doc)

	// Split into paragraphs
	paragraphs := splitIntoParagraphs(plainText, c.config.Separator)

	if len(paragraphs) == 0 {
		return []ChunkResult{}, nil
	}

	// Chunk the paragraphs
	return c.chunkParagraphs(paragraphs, docID, doc, plainText)
}

// splitIntoParagraphs splits text into paragraphs by blank lines.
func splitIntoParagraphs(text string, sep rune) []string {
	// Split by the separator (typically newline)
	parts := strings.Split(text, string(sep))

	paragraphs := make([]string, 0)
	var current strings.Builder

	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			// Blank line - finalize current paragraph if non-empty
			if current.Len() > 0 {
				paragraphs = append(paragraphs, strings.TrimSpace(current.String()))
				current.Reset()
			}
		} else {
			if current.Len() > 0 {
				// Not the start, add a space separator
				current.WriteRune(' ')
			}
			current.WriteString(part)
		}
	}

	// Don't forget the last paragraph
	if current.Len() > 0 {
		paragraphs = append(paragraphs, strings.TrimSpace(current.String()))
	}

	return paragraphs
}

// chunkParagraphs implements the main chunking algorithm.
func (c *Chunker) chunkParagraphs(paragraphs []string, docID archive.StableDocumentID, doc *types.Document, fullText string) ([]ChunkResult, error) {
	// Compute overlap size
	overlapSize := 0
	if c.config.OverlapRatio > 0 {
		overlapSize = int(float64(c.config.TargetChunkSize) * c.config.OverlapRatio)
	}

	var chunks []ChunkResult
	chunkIndex := 0

	// Current position in the document
	var currentChunk strings.Builder
	currentChunkLen := 0
	paragraphStart := -1
	paragraphEnd := -1
	currentDocPos := 0

	for i, para := range paragraphs {
		paraLen := len(para)
		paraStartPos := findParagraphStartPos(paragraphs, i, fullText)

		// If we have content in the current chunk
		if currentChunkLen > 0 {
			// Check if adding this paragraph exceeds max size
			if currentChunkLen+paraLen > c.config.MaxChunkSize {
				// Flush current chunk
				if currentChunkLen > 0 {
					chunkText := currentChunk.String()
					chunkResult := c.createChunkResult(chunkIndex, chunkText, docID, doc,
						currentDocPos, currentChunkLen, paragraphStart, paragraphEnd)
					chunks = append(chunks, chunkResult)
					chunkIndex++

					// Start new chunk with overlap if configured
					currentChunk.Reset()
					currentChunkLen = 0

					// Apply overlap: copy from end of previous chunk
					if overlapSize > 0 && len(chunkText) > overlapSize {
						overlapText := chunkText[len(chunkText)-overlapSize:]
						currentChunk.WriteString(overlapText)
						currentChunkLen = overlapSize
						currentDocPos = currentDocPos + len(chunkText) - overlapSize
					} else {
						currentDocPos = paraStartPos
					}
				}

				paragraphStart = i
				paragraphEnd = i
			} else {
				// Continue current chunk
				if i > 0 {
					// Add space before appending paragraph
					if currentChunkLen > 0 {
						currentChunk.WriteString(" ")
						currentChunkLen++
					}
				}
				currentChunk.WriteString(para)
				currentChunkLen += paraLen
				if paragraphEnd < i {
					paragraphEnd = i
				}
			}
		} else {
			// Start fresh chunk with this paragraph
			currentChunk.WriteString(para)
			currentChunkLen = paraLen
			paragraphStart = i
			paragraphEnd = i
		}

		// If paragraph exceeds max size, it needs splitting
		if paraLen > c.config.MaxChunkSize {
			// Flush current chunk first if it has content and fits in max size
			if currentChunkLen > 0 && currentChunkLen < c.config.MaxChunkSize {
				chunkText := currentChunk.String()
				chunkResult := c.createChunkResult(chunkIndex, chunkText, docID, doc,
					currentDocPos, currentChunkLen, paragraphStart, paragraphEnd)
				chunks = append(chunks, chunkResult)
				chunkIndex++

				currentChunk.Reset()
				currentChunkLen = 0
			} else if currentChunkLen > 0 {
				// Current chunk has content but is >= max size - this shouldn't happen
				// unless a previous paragraph was too long, so just reset
				currentChunk.Reset()
				currentChunkLen = 0
			}

			// Split the long paragraph
			splitChunks := c.splitLongParagraph(para, paraStartPos, docID, doc, chunkIndex)
			chunks = append(chunks, splitChunks...)
			chunkIndex += len(splitChunks)

			// Set current chunk position for potential next paragraph
			if len(splitChunks) > 0 {
				lastChunk := splitChunks[len(splitChunks)-1]
				currentDocPos = lastChunk.Position + lastChunk.Length
			}
		}
	}

	// Don't forget the last chunk
	if currentChunkLen > 0 {
		chunkText := currentChunk.String()
		chunkResult := c.createChunkResult(chunkIndex, chunkText, docID, doc,
			currentDocPos, currentChunkLen, paragraphStart, paragraphEnd)
		chunks = append(chunks, chunkResult)
	}

	return chunks, nil
}

// findParagraphStartPos finds the starting position of a paragraph in the full text.
func findParagraphStartPos(paragraphs []string, index int, fullText string) int {
	if index >= len(paragraphs) {
		return 0
	}

	// For the first paragraph, start at 0
	if index == 0 {
		return 0
	}

	// Search backwards from the start of this paragraph to find where it starts
	// This is a simplified approach - in practice, we track this during splitting
	// For now, use a heuristic based on previous paragraph lengths
	pos := 0
	for i := 0; i < index; i++ {
		pos += len(paragraphs[i]) + 1 // +1 for the newline separator
	}
	return pos
}

// splitLongParagraph splits a paragraph that exceeds max chunk size.
func (c *Chunker) splitLongParagraph(para string, startPos int, docID archive.StableDocumentID, doc *types.Document, chunkIndex int) []ChunkResult {
	var chunks []ChunkResult

	text := para
	pos := startPos

	// Process until no more content exceeds max size
	// Note: After overlap is prepended in chunkParagraphs, the next chunk
	// will fit within max size because we're splitting at max size.
	for len(text) > c.config.MaxChunkSize {
		// Find the nearest word boundary at or before MaxChunkSize
		splitPos := c.config.MaxChunkSize
		if splitPos > len(text) {
			splitPos = len(text)
		}

		// Look for a word boundary (space) going backwards from splitPos
		for splitPos > 0 && !unicode.IsSpace(rune(text[splitPos-1])) {
			splitPos--
		}

		// If no word boundary found, force split at max size
		if splitPos == 0 {
			splitPos = c.config.MaxChunkSize
		}

		// Extract chunk
		chunkText := text[:splitPos]

		// Calculate remaining text for next chunk
		remaining := text[splitPos:]

		// Create and store this chunk
		chunkResult := c.createChunkResult(chunkIndex, chunkText, docID, doc, pos, len(chunkText), -1, -1)
		chunks = append(chunks, chunkResult)
		chunkIndex++

		// Update position for next chunk
		pos = pos + len(chunkText)
		text = remaining
	}

	// Add remaining text if any
	if len(text) > 0 {
		chunkResult := c.createChunkResult(chunkIndex, text, docID, doc, pos, len(text), -1, -1)
		chunks = append(chunks, chunkResult)
	}

	return chunks
}

// createChunkResult creates a ChunkResult with all metadata.
func (c *Chunker) createChunkResult(chunkIndex int, text string, docID archive.StableDocumentID, doc *types.Document,
	position, length int, paraStart, paraEnd int) ChunkResult {

	contentHash := computeContentHash(text)

	chunkID := composeChunkID(docID, contentHash, position, length)

	return ChunkResult{
		ChunkID:          chunkID,
		DocumentStableID: docID,
		SourceID:         doc.SourceID,
		SourceURL: func() string {
			if doc.CanonicalURL != nil {
				return *doc.CanonicalURL
			}
			return ""
		}(),
		ChunkIndex:     chunkIndex,
		Text:           text,
		ContentHash:    contentHash,
		Position:       position,
		Length:         length,
		ParagraphStart: paraStart,
		ParagraphEnd:   paraEnd,
	}
}

// computeDocumentStableID computes a stable document ID from the document's source
// and content. This ensures idempotent document identity across re-indexing.
func computeDocumentStableID(doc *types.Document) archive.StableDocumentID {
	// Combine source ID and content hash for document identity
	contentHash := computeContentHash(doc.PlainText)
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%s", doc.SourceID, contentHash)))
	return archive.StableDocumentID(fmt.Sprintf("arch:%x", hash[:8]))
}

// computeContentHash computes the SHA-256 hash of text content.
func computeContentHash(text string) archive.ContentHash {
	hash := sha256.Sum256([]byte(text))
	return archive.ContentHash(fmt.Sprintf("%x", hash[:16]))
}

// composeChunkID constructs a StableChunkID from document ID, content hash, position, and length.
//
// Format: "arch_doc:<document_id>:<content_hash>:<position>:<length>"
//
// This ensures deterministic chunk IDs that are consistent across re-indexing.
func composeChunkID(docID archive.StableDocumentID, contentHash archive.ContentHash, position, length int) archive.StableChunkID {
	return archive.StableChunkID(fmt.Sprintf(
		"arch_doc:%s:%s:%d:%d",
		docID, contentHash, position, length,
	))
}

// ComputeDocumentStableID returns the document stable ID for use in archive operations.
func ComputeDocumentStableID(doc *types.Document) archive.StableDocumentID {
	return computeDocumentStableID(doc)
}

// ComputeContentHash returns the content hash for use in chunk IDs.
func ComputeContentHash(text string) archive.ContentHash {
	return computeContentHash(text)
}

// GetConfig returns the chunker's configuration.
func (c *Chunker) GetConfig() Config {
	return c.config
}
