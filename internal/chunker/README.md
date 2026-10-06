# Chunker Package

Deterministic chunking of normalized `types.Document` content for embedding and retrieval.

## Overview

The chunker package implements a simple, configurable chunking strategy that:

- Operates on already-normalized plain text
- Preserves paragraph boundaries where practical
- Uses configurable target/max size and overlap
- Never splits into empty chunks
- Assigns deterministic chunk IDs and order
- Preserves document/source provenance
- Produces identical chunks for identical input/config
- Avoids dependence on an LLM or tokenizer service

## Algorithm

### Chunk ID Algorithm

Chunk IDs are deterministically derived from multiple components:

```
arch_doc:<document_id>:<content_hash>:<position>:<length>
```

**Components:**

1. **Prefix**: `arch_doc:` - Identifies this as an archived document chunk
2. **Document ID**: `arch:<8-char-hash>` - Computed from source ID and document content hash
3. **Content Hash**: `<16-char-hash>` - First 16 chars of SHA-256 hash of chunk text
4. **Position**: Zero-based character offset in the original document
5. **Length**: Length of chunk in characters

**Determinism guarantees:**
- Changing document content changes the document ID
- Changing chunk content changes the content hash
- Same input/config always produces identical IDs

### Chunking Strategy

1. **Paragraph splitting**: Text is split by blank lines (default separator: `\n`)
2. **Target size**: Chunks aim for `Config.TargetChunkSize` runes
3. **Max size constraint**: No chunk exceeds `Config.MaxChunkSize`
4. **Overlap**: Consecutive chunks share `Config.OverlapRatio` of their content
5. **Word boundary splitting**: Long paragraphs are split at word boundaries

### Position Tracking

Each chunk tracks:
- `Position`: Starting character offset in the original document
- `Length`: Length in characters
- `ParagraphStart/End`: Which paragraphs the chunk spans

## Configuration

```go
config := chunker.Config{
    TargetChunkSize:  300,   // Target size for each chunk
    MaxChunkSize:     500,   // Hard maximum
    OverlapRatio:     0.1,   // 10% overlap between chunks
    MinParagraphSize: 20,    // Min size before splitting
    Separator:        '\n',  // Paragraph separator
}

chunker, err := chunker.New(config)
chunks, err := chunker.Chunk(document)
```

## Usage

```go
import "github.com/Mundo-Dolphins/local-newsroom/internal/chunker"

// Create chunker with default config
chunker, err := chunker.New(chunker.DefaultConfig())

// Chunk a document
doc := &types.Document{
    SourceID:  "source-123",
    PlainText: "The document content...",
    CanonicalURL: types.PointerTo("https://example.com/article"),
}

chunks, err := chunker.Chunk(doc)
// Result: []ChunkResult with all metadata

// Convert to archive.Chunk format for storage
for i, c := range chunks {
    archiveChunk := archive.Chunk{
        StableID:         c.ChunkID,
        DocumentStableID: c.DocumentStableID,
        ContentHash:      c.ContentHash,
        Position:         c.Position,
        Length:           c.Length,
        HasEmbedding:     false,
    }
    // Use with archive.Store.UpsertDocument()
}
```

## Testing

```bash
# Run chunker tests
go test ./internal/chunker/... -v

# Run with coverage
go test ./internal/chunker/... -coverprofile=coverage.out -v

# Check specific test cases
go test ./internal/chunker/... -run TestChunk_Deterministic -v
go test ./internal/chunker/... -run TestChunk_Unicode -v
go test ./internal/chunker/... -run TestChunk_EmptyDocument -v
```

## API Reference

### Types

- **Config**: Chunking configuration parameters
- **Chunker**: Main chunking implementation
- **ChunkResult**: Output of chunking with full metadata

### Functions

- **New(config)**: Create a new Chunker
- **DefaultConfig()**: Return sensible defaults
- **Chunk(doc)**: Main chunking method
- **ComputeDocumentStableID(doc)**: Compute document ID
- **ComputeContentHash(text)**: Compute content hash
- **GetConfig()**: Return chunker's configuration

### ChunkResult Fields

| Field | Type | Description |
|-------|------|-------------|
| ChunkID | StableChunkID | Deterministic chunk identifier |
| DocumentStableID | StableDocumentID | Parent document ID |
| SourceID | string | Original source ID |
| SourceURL | string | Canonical URL for provenance |
| ChunkIndex | int | Zero-based index in document |
| Text | string | Chunked content |
| ContentHash | ContentHash | SHA-256 hash of text |
| Position | int | Starting offset in document |
| Length | int | Length in characters |
| ParagraphStart | int | First paragraph index |
| ParagraphEnd | int | Last paragraph index |
