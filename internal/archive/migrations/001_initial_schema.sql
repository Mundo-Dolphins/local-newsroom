-- Schema version: 1
-- Created: v0.3 initial archive schema

-- Documents table: stores normalized archived documents
CREATE TABLE IF NOT EXISTS documents (
    stable_id TEXT PRIMARY KEY,
    plain_text TEXT NOT NULL,
    plain_text_hash TEXT NOT NULL,
    chunk_count INTEGER NOT NULL DEFAULT 0,
    source_id TEXT NOT NULL,
    source_url TEXT,
    source_type TEXT NOT NULL,
    retrieved_at TEXT NOT NULL,
    extracted_at TEXT NOT NULL,
    archived_at TEXT NOT NULL,
    metadata TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- Documents index on plain_text_hash for change detection
CREATE INDEX IF NOT EXISTS idx_documents_hash ON documents(plain_text_hash);

-- Chunks table: stores document chunks with stable identities
CREATE TABLE IF NOT EXISTS chunks (
    stable_id TEXT PRIMARY KEY,
    document_stable_id TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    position INTEGER NOT NULL,
    length INTEGER NOT NULL,
    has_embedding INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (document_stable_id) REFERENCES documents(stable_id) ON DELETE CASCADE
);

-- Chunks index for efficient document-based lookups
CREATE INDEX IF NOT EXISTS idx_chunks_doc_id ON chunks(document_stable_id);
CREATE INDEX IF NOT EXISTS idx_chunks_position ON chunks(document_stable_id, position);

-- Embedding metadata table: tracks embedding configuration per chunk
CREATE TABLE IF NOT EXISTS embedding_metadata (
    chunk_id TEXT PRIMARY KEY,
    model_name TEXT NOT NULL,
    dimensions INTEGER NOT NULL,
    version TEXT,
    generated_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (chunk_id) REFERENCES chunks(stable_id) ON DELETE CASCADE
);

-- Embedding vectors table: stores actual vector data as JSON
CREATE TABLE IF NOT EXISTS embedding_vectors (
    chunk_id TEXT PRIMARY KEY,
    vector_blob BLOB NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (chunk_id) REFERENCES chunks(stable_id) ON DELETE CASCADE
);

-- Index for finding chunks with embeddings
CREATE INDEX IF NOT EXISTS idx_embedding_metadata_has_embedding 
    ON chunks(has_embedding) WHERE has_embedding = 1;

-- Index for retrieval by date range
CREATE INDEX IF NOT EXISTS idx_documents_archived_at ON documents(archived_at);
CREATE INDEX IF NOT EXISTS idx_documents_retrieved_at ON documents(retrieved_at);

-- Migration tracking table
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL
);
