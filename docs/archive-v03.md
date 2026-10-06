# Local Archive/RAG System (v0.3)

The local archive provides offline-first semantic search capabilities for the newsroom workflow, enabling research using cached content without external dependencies.

## Overview

The archive system stores extracted content in SQLite with vector embeddings, supporting:

- **Offline operation**: All research can be performed against locally stored content
- **Semantic retrieval**: Cosine similarity-based search over chunked content
- **Provenance preservation**: Full source metadata for traceability
- **Idempotent ingestion**: Re-ingesting unchanged content produces identical IDs
- **No external vector DB**: Pure SQLite with JSON-encoded embeddings

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                     Local Archive (v0.3)                         │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌──────────────┐    ┌──────────────┐    ┌──────────────┐      │
│  │ SQLite Store │───▶│ Chunker      │───▶│ Embedder     │      │
│  │ (archive.db) │    │ (300 runes)  │    │ (oMLX/fake)  │      │
│  └──────────────┘    └──────────────┘    └──────────────┘      │
│         │                     │                  │               │
│         ▼                     ▼                  ▼               │
│  ┌──────────────────────────────────────────────────────┐      │
│  │              Documents & Chunks Table                 │      │
│  │  stable_id | plain_text | source_id | archived_at    │      │
│  └──────────────────────────────────────────────────────┘      │
│  ┌──────────────────────────────────────────────────────┐      │
│  │               Chunks Table                            │      │
│  │  chunk_id | doc_id | content_hash | position | length │      │
│  └──────────────────────────────────────────────────────┘      │
│  ┌──────────────────────────────────────────────────────┐      │
│  │         Embedding Vectors (JSON blobs)                │      │
│  │  chunk_id | model_name | dimensions | vector_blob    │      │
│  └──────────────────────────────────────────────────────┘      │
│                                                                  │
│  ┌──────────────────────────────────────────────────────┐      │
│  │           Semantic Retriever                          │      │
│  │  1. Embed query text                                  │      │
│  │  2. Compute cosine similarity                         │      │
│  │  3. Optional reranking                                │      │
│  │  4. Return top-K hits                                 │      │
│  └──────────────────────────────────────────────────────┘      │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

## Database Configuration

### Location and Setup

The archive database is stored as a SQLite file:

```bash
# Default location (in-memory for testing)
ARCHIVE_DB=:memory:

# Production: file-based database
ARCHIVE_DB=./archive.db
```

### Store Options

```go
store, err := archive.NewSQLiteStoreWithConfig(
    "./archive.db",
    archive.WithMaxOpenConns(2),     // Max concurrent connections
    archive.WithMaxIdleConns(1),     // Max idle connections
    archive.WithConnMaxLifetime(0),  // No connection lifetime limit
    archive.WithWAL(true),           // Enable WAL mode
)
```

### Schema

The database uses versioned migrations:

```sql
-- Documents table
CREATE TABLE documents (
    stable_id TEXT PRIMARY KEY,
    plain_text TEXT NOT NULL,
    plain_text_hash TEXT NOT NULL,
    chunk_count INTEGER NOT NULL,
    source_id TEXT NOT NULL,
    source_url TEXT,
    source_type TEXT NOT NULL,
    retrieved_at TEXT NOT NULL,
    extracted_at TEXT NOT NULL,
    archived_at TEXT NOT NULL,
    metadata TEXT,  -- JSON
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- Chunks table
CREATE TABLE chunks (
    stable_id TEXT PRIMARY KEY,
    document_stable_id TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    position INTEGER NOT NULL,
    length INTEGER NOT NULL,
    has_embedding INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (document_stable_id) REFERENCES documents(stable_id)
);

-- Embedding metadata
CREATE TABLE embedding_metadata (
    chunk_id TEXT PRIMARY KEY,
    model_name TEXT NOT NULL,
    dimensions INTEGER NOT NULL,
    version TEXT,
    generated_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (chunk_id) REFERENCES chunks(stable_id)
);

-- Embedding vectors (stored as JSON blobs)
CREATE TABLE embedding_vectors (
    chunk_id TEXT PRIMARY KEY,
    vector_blob BLOB NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (chunk_id) REFERENCES chunks(stable_id)
);
```

## Embedding Configuration

### oMLX Embeddings

Configure the oMLX embedding client:

```go
config := embedding.Config{
    BaseURL:         "http://localhost:8000/v1",  // oMLX endpoint
    EmbeddingModel:  "all-MiniLM-L6-v2",          // Model name
    Timeout:         30 * time.Second,
    EmbeddingAPIKey: "your-api-key",              // Optional
    Dimensions:      384,                         // Optional dimension override
}

client := embedding.NewClient(config)
embeddings, err := client.Embed(ctx, embedding.Request{
    Inputs: []string{"query text here"},
})
```

### Available Models

Common embedding models:

| Model | Dimensions | Use Case |
|-------|------------|----------|
| `all-MiniLM-L6-v2` | 384 | Fast, general purpose |
| `all-mpnet-base-v2` | 768 | Better accuracy |
| `bge-small-en-v1.5` | 384 | Good balance |
| `bge-large-en-v1.5` | 1024 | Highest accuracy |

### Fake Embedder (Testing)

For offline testing, use the fake embedder:

```go
embedder := embedding.NewFakeEmbedder()
embeddings, err := embedder.Embed(ctx, embedding.Request{
    Inputs: []string{"test query"},
})
```

The fake embedder returns deterministic random vectors of configurable dimension.

## Reranking (Optional)

Reranking provides a second-pass relevance scoring after semantic retrieval.

### oMLX Reranker

```go
rerankConfig := retrieval.OrganizerConfig{
    BaseURL:  "http://localhost:8000/v1",
    Model:    "cross-encoder/ms-marco-MiniLM-L-6-v2",
    Timeout:  30 * time.Second,
}

reranker := oMLX.NewReranker(rerankConfig)

retriever, err := retrieval.New(retrieval.Config{
    TopK:       10,      // Final results
    RerankTopN: 50,      // Candidates to rerank
    Reranker:   reranker,
})
```

### Fake Reranker (Testing)

```go
reranker := NewFakeReranker()
reranker.SetErrorRate(0.0)  // 0% failure rate for testing
```

## Workflow

### Ingestion Pipeline

```go
// 1. Fetch and extract content
fetchResult, _ := fetcherClient.Fetch(ctx, url)
extracted, _ := extractor.Extract(extractor.Input{
    Source:    source,
    Content:   fetchResult.Body,
})

// 2. Chunk the content
chunkCfg := chunker.Config{
    TargetChunkSize: 300,  // runes
    MaxChunkSize:    500,
    OverlapRatio:    0.1,
}
chunks, _ := chunker.New(chunkCfg).Chunk(extracted)

// 3. Create archive document
archDoc := &archive.ArchiveDocument{
    StableID:      archive.StableDocumentID("arch:" + source.ID),
    PlainText:     extracted.PlainText,
    PlainTextHash: archive.ContentHash(computeHash(extracted.PlainText)),
    ChunkCount:    len(chunks),
    SourceProvenance: archive.SourceProvenance{
        SourceID:   source.ID,
        SourceURL:  source.URL,
        SourceType: types.SourceTypeWeb,
        RetrievedAt: time.Now().UTC(),
        ExtractedAt: extracted.ExtractedAt,
    },
    ArchivedAt: time.Now().UTC(),
}

// 4. Convert and insert chunks
chunkList := make([]archive.Chunk, len(chunks))
for i, c := range chunks {
    chunkList[i] = archive.Chunk{
        StableID:         archive.StableChunkID(c.ChunkID),
        DocumentStableID: archDoc.StableID,
        ContentHash:      c.ContentHash,
        Position:         c.Position,
        Length:           c.Length,
        HasEmbedding:     true,
    }
}

// 5. Upsert to archive (idempotent)
if err := store.UpsertDocument(ctx, archDoc, chunkList); err != nil {
    log.Fatal(err)
}

// 6. Generate and store embeddings
for _, chunk := range chunkList {
    embeddings, _ := embedder.Embed(ctx, embedding.Request{
        Inputs: []string{chunk.GetText()},  // Reconstruct text from doc
    })
    store.SetEmbeddingVector(ctx, chunk.StableID, embeddings[0].Vector)
    store.SetEmbeddingMetadata(ctx, chunk.StableID, &archive.EmbeddingMetadata{
        ModelName:  "all-MiniLM-L6-v2",
        Dimensions: 384,
        GeneratedAt: time.Now().UTC(),
    })
}
```

### Retrieval Pipeline

```go
// 1. Create retriever
retriever, err := retrieval.New(retrieval.Config{
    TopK:              10,
    RerankTopN:        50,  // Optional
    Reranker:          reranker,  // Optional
    MinRelevanceScore: 0.0,
})

retriever.SetStore(store)
retriever.SetEmbedder(embedder)

// 2. Execute query
results, err := retriever.Retrieve(ctx, retrieval.RetrievalQuery{
    Text:  "local newsroom workflow",
    Limit: 10,
})

// 3. Process results
for _, hit := range results.Hits {
    fmt.Printf("Relevance: %.3f\n", hit.RelevanceScore)
    fmt.Printf("Source: %s\n", hit.SourceProvenance.SourceURL)
    fmt.Printf("Content: %s...\n", hit.ChunkContent[:100])
}
```

## Usage Examples

### Example 1: Basic Archive Operations

```bash
# Set environment variable for archive location
export ARCHIVE_DB="./newsroom-archive.db"

# Archive new content
newsroom archive add \
    --url https://example.com/article \
    --source-id article-001

# List archived documents
newsroom archive list --limit 20

# Search archive
newsroom archive search --query "local development"
```

### Example 2: Archive-Assisted Research

```go
// Retrieve background context from archive
archiveResults, _ := retriever.Retrieve(ctx, retrieval.RetrievalQuery{
    Text:  "research methodology",
    Limit: 5,
})

// Pass archive context to researcher
contextText := ""
for _, hit := range archiveResults.Hits {
    contextText += hit.ChunkContent + "\n"
}

// Researcher receives combined context
researcherPrompt := fmt.Sprintf(`
Background from archive:
%s

Topic: %s

Generate research dossier...
`, contextText, topic)
```

### Example 3: Archive Search via CLI

```bash
# Search for specific content
newsroom archive search \
    --db ./archive.db \
    --query "climate change" \
    --top-k 10

# Search with score threshold
newsroom archive search \
    --query "AI safety" \
    --min-score 0.7 \
    --top-k 5
```

## Source Limits and Constraints

### Size Limits

| Component | Limit | Notes |
|-----------|-------|-------|
| Document plain text | 10 MB | Configurable via extractor |
| Max words per doc | 50,000 | Configurable |
| Chunk size | 500 runes | Configurable |
| Embedding dimensions | 1024+ | Model-dependent |

### Source Limits

```go
config := workflow.Config{
    MaxSources: 100,     // Max sources per research
    MaxQueries: 5,       // Max search queries
    MaxResultsPerQuery: 10,
}
```

### Context Limits

The researcher prompt has token limits. Recommended context sizes:

| Limit | Chunks | Use Case |
|-------|--------|----------|
| 4k tokens | ~2-3 chunks | Brief context |
| 8k tokens | ~4-6 chunks | Standard |
| 16k tokens | ~8-12 chunks | Comprehensive |

## Current Limitations

### Scalability

**In-process vector scan**: The current implementation computes cosine similarity in-memory over all stored chunks.

```go
// Current: O(n) similarity computation per query
for _, chunk := range chunks {
    vector := store.GetEmbeddingVector(chunk.ID)
    score := cosineSimilarity(queryVector, vector)
}
```

**Impact**:
- ~1,000 chunks: Fast (<100ms)
- ~10,000 chunks: Moderate (100-500ms)
- ~100,000+ chunks: Slower (500ms+)

**Future improvements**:
- Integrate with vector-optimized SQLite (e.g., `sqlite-vss`)
- Add approximate nearest neighbor (ANN) index
- Consider external vector DB (e.g., Chroma, FAISS) for large archives

### Concurrent Writes

SQLite WAL mode supports concurrent reads, but writes are serialized. For high-throughput ingestion:

- Batch documents in transactions
- Consider sharding by topic/source type for parallel ingestion

### No Real-time Updates

Archives are append-first. Updates require:
1. Detect content changes (via hash comparison)
2. Delete old chunks
3. Insert updated chunks
4. Update document metadata

## Testing

### E2E Test Suite

Run the v0.3 E2E tests:

```bash
# All tests
go test -v ./tests/... -run E2E_v03

# Specific scenarios
go test -v ./tests/... -run "TestE2E_v03_ArchiveIngestion"
go test -v ./tests/... -run "TestE2E_v03_SemanticRetrieval"
go test -v ./tests/... -run "TestE2E_v03_ResearchWithArchiveContext"
```

### Test Coverage

The v0.3 test suite covers:

- ✅ Archive ingestion and chunking
- ✅ Idempotent re-ingestion
- ✅ Semantic retrieval
- ✅ Empty archive behavior
- ✅ Fake reranking
- ✅ Researcher with archive context
- ✅ Combined archive + live sources
- ✅ v0.2 backward compatibility
- ✅ File-based vs in-memory archives
- ✅ Health checks

## Integration Points

### With Discovery

```go
// Archive search can supplement source discovery
archiveContext, _ := retriever.Retrieve(ctx, retrieval.RetrievalQuery{
    Text:  topic,
    Limit: 3,
})

// Use archive context to refine search queries
plan, _ := planner.CreateSearchPlan(topic, archiveContext)
```

### With Researcher

```go
// Pass archive context to researcher
researcherClient := researcher.NewClient(config)
dossier, err := researcherClient.Research(ctx, researcher.Request{
    Topic:        topic,
    Sources:      liveSources,
    ArchiveContext: archiveContext,  // From semantic retrieval
})
```

### With Verification

```go
// Use archive for claim verification
claims, _ := verifier.VerifyClaims(dossier.Claims, archiveContext)
```

## Migration from v0.2

### Breaking Changes

None for existing research flows. Archive is an additive feature.

### New Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `ARCHIVE_DB` | `:memory:` | Path to archive database |
| `ARCHIVE_EMBED_MODEL` | `all-MiniLM-L6-v2` | Embedding model |
| `ARCHIVE_RERANK_ENABLED` | `false` | Enable reranking |

## Files

- `internal/archive/store_sqlite.go` - SQLite store implementation
- `internal/archive/types.go` - Type definitions
- `internal/archive/fake_store.go` - Fake store for testing
- `internal/archive/store_sqlite_test.go` - Unit tests
- `tests/e2e_v03_test.go` - E2E test suite

## See Also

- [README.md](../README.md) - Main documentation
- [development.md](../development.md) - Development workflow
- `tests/e2e_v02_test.go` - v0.2 E2E tests
- `internal/retrieval/retrieval.go` - Semantic retrieval logic
