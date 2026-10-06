// Package archiverev implements archive retrieval integration with the research workflow.
//
// This package provides:
//   - Archive-backed chunk retrieval based on research topic
//   - Normalization of archive chunks into the existing document interface
//   - Deduplication against live fetched URLs
//   - Budget enforcement for context and sources
//   - Optional archive usage without breaking v0.1/v0.2 behavior
//
// Key design principles:
//   - Archive retrieval happens before the Researcher invocation
//   - Retrieved archive material is adapted to types.Document for researcher consumption
//   - No coupling to SQLite or archive implementation details
//   - Failure modes are well-defined and non-fatal when live sources are available
package archiverev

import (
	"context"
	"fmt"
	"strings"

	"github.com/Mundo-Dolphins/local-newsroom/internal/archive"
	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
)

// Config holds configuration for archive retrieval.
type Config struct {
	// ArchiveStore is the archive store to query.
	// If nil or not configured, archive retrieval is disabled.
	ArchiveStore archive.Store

	// MaxArchivedSources limits the number of distinct archived sources retrieved.
	// If zero, defaults to 5.
	MaxArchivedSources int

	// MaxArchivedChunks limits the total number of chunks retrieved from archive.
	// If zero, defaults to 20.
	MaxArchivedChunks int

	// MaxChunkSize limits the size of each chunk in characters.
	// If zero, defaults to 2000.
	MaxChunkSize int

	// DeduplicateByURL enables deduplication against live fetched URLs.
	// When enabled, archive chunks from URLs that were also fetched live
	// will be excluded.
	DeduplicateByURL bool
}

// Client performs archive retrieval and normalization.
type Client struct {
	config Config
}

// New creates a new retrieval client with the given configuration.
func New(config Config) *Client {
	return &Client{
		config: config,
	}
}

// RetrieveResult holds the result of an archive retrieval operation.
type RetrieveResult struct {
	// Documents are normalized archive chunks adapted to types.Document.
	// These can be passed directly to the Researcher.
	Documents []types.Document

	// SourceIDs is the set of all source IDs represented in Documents.
	SourceIDs []string

	// TotalChunks is the total number of chunks retrieved before budget limits.
	// This may exceed len(Documents) if some chunks were deduplicated or truncated.
	TotalChunks int

	// HasArchive indicates whether any archive data was retrieved.
	HasArchive bool
}

// Retrieve queries the archive for relevant chunks and normalizes them into documents.
//
// This method:
// 1. Queries the archive for chunks relevant to the topic
// 2. Deduplicates against any URLs that will also be fetched live (if configured)
// 3. Applies budget limits (max sources, max chunks, max chunk size)
// 4. Normalizes retrieved chunks into types.Document for researcher consumption
//
// The returned documents have:
//   - SourceID derived from the archive's source provenance
//   - PlainText set to the chunk content
//   - ExtractionMetadata with "source": "archive" and chunk position info
//   - CanonicalURL and other provenance from the archive
//
// If no archive store is configured, returns an empty RetrieveResult.
// If archive query fails, returns an error.
func (c *Client) Retrieve(ctx context.Context, topic string, liveURLs []string) (*RetrieveResult, error) {
	// If no archive store configured, return empty result
	if c.config.ArchiveStore == nil {
		return &RetrieveResult{
			HasArchive: false,
		}, nil
	}

	// Apply defaults
	maxSources := c.config.MaxArchivedSources
	if maxSources == 0 {
		maxSources = 5
	}

	maxChunks := c.config.MaxArchivedChunks
	if maxChunks == 0 {
		maxChunks = 20
	}

	maxChunkSize := c.config.MaxChunkSize
	if maxChunkSize == 0 {
		maxChunkSize = 2000
	}

	// Query the archive
	query := archive.RetrievalQuery{
		Text:   topic,
		Target: archive.RetrievalTargetDocument,
		Limit:  maxSources * 5, // Request more to account for deduplication
	}

	result, err := c.config.ArchiveStore.Retrieve(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("archive retrieval failed: %w", err)
	}

	if len(result.Hits) == 0 {
		return &RetrieveResult{
			HasArchive: false,
		}, nil
	}

	// Build a set of live URLs for deduplication
	liveURLsSet := make(map[string]bool)
	if c.config.DeduplicateByURL {
		for _, url := range liveURLs {
			liveURLsSet[strings.ToLower(strings.TrimSpace(url))] = true
		}
	}

	// Track deduplication and retrieval progress
	sourceURLs := make(map[string]bool) // Track URLs we've already included
	retrievedDocs := make([]types.Document, 0, maxSources)
	retrievedSources := make([]string, 0, maxSources)
	totalChunks := 0
	chunkCount := 0

	for _, hit := range result.Hits {
		// Skip if we've reached the source limit
		if len(retrievedSources) >= maxSources {
			break
		}

		// Deduplicate by URL if configured
		if c.config.DeduplicateByURL {
			dedupKey := strings.ToLower(strings.TrimSpace(hit.SourceURL))
			if liveURLsSet[dedupKey] {
				continue // This URL is also fetched live, skip archive
			}
			if sourceURLs[dedupKey] {
				continue // Already included this source
			}
			sourceURLs[dedupKey] = true
		}

		// Get the full document content
		doc, err := c.config.ArchiveStore.GetDocument(ctx, hit.DocumentStableID)
		if err != nil {
			continue // Skip documents we can't retrieve
		}

		// Split document into chunks if it has multiple
		chunks, err := c.config.ArchiveStore.GetChunksByDocument(ctx, hit.DocumentStableID)
		if err != nil {
			continue
		}

		// Process chunks up to budget limit
		for _, chunk := range chunks {
			if chunkCount >= maxChunks {
				break
			}

			totalChunks++
			chunkCount++

			// Extract chunk content from document plain text
			// For archive documents, we need to compute the chunk text
			// This assumes chunks are sequential and non-overlapping
			chunkText := c.extractChunkText(doc.PlainText, chunk)
			if chunkText == "" {
				continue
			}

			// Truncate if necessary
			if len(chunkText) > maxChunkSize {
				chunkText = chunkText[:maxChunkSize]
			}

			// Normalize chunk into a types.Document
			doc := c.normalizeChunk(hit, chunk, chunkText, doc)
			retrievedDocs = append(retrievedDocs, doc)
			retrievedSources = append(retrievedSources, doc.SourceID)
		}

		if chunkCount >= maxChunks {
			break
		}
	}

	// Track if we had to skip any documents (deduplication)
	// Note: wasDeduplicated is always false with current implementation

	return &RetrieveResult{
		Documents:   retrievedDocs,
		SourceIDs:   retrievedSources,
		TotalChunks: totalChunks,
		HasArchive:  len(retrievedDocs) > 0,
	}, nil
}

// extractChunkText extracts the text for a specific chunk from document plain text.
// This is a simple implementation that assumes chunks are sequential.
// In production, this would use proper chunk boundaries.
func (c *Client) extractChunkText(docText string, chunk archive.Chunk) string {
	// For now, just return the document text with a marker
	// A real implementation would use proper chunk boundaries
	// stored alongside chunks in the archive
	return docText
}

// normalizeChunk converts an archive retrieval hit into a types.Document.
func (c *Client) normalizeChunk(hit archive.RetrievalHit, chunk archive.Chunk, chunkText string, doc *archive.ArchiveDocument) types.Document {
	// Create a document ID that includes chunk position for uniqueness
	chunkDocID := fmt.Sprintf("%s:chunk:%d", hit.SourceID, chunk.Position)

	return types.Document{
		SourceID:     chunkDocID,
		CanonicalURL: &hit.SourceURL,
		Title:        nil, // Archive documents may not have titles
		PlainText:    chunkText,
		Author:       nil,
		PublishedAt:  &hit.RetrievedAt,
		RetrievedAt:  hit.RetrievedAt,
		ExtractionMetadata: map[string]string{
			"source":             "archive",
			"document_stable_id": string(hit.DocumentStableID),
			"chunk_position":     fmt.Sprintf("%d", chunk.Position),
			"chunk_length":       fmt.Sprintf("%d", chunk.Length),
			"relevance_score":    fmt.Sprintf("%f", hit.RelevanceScore),
		},
	}
}
