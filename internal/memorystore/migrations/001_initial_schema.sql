-- Schema version: 1
-- Created: v0.7 initial editorial memory schema (issue #142)
--
-- memory.db answers "what has the newsroom learned previously?" It is
-- strictly separate from archive.db ("what did source documents say?").
-- This schema deliberately does not store archive chunks or embeddings.

-- Memory records: one row per v0.7 memory record (all six kinds share the
-- same envelope; kind selects the value payload type).
CREATE TABLE IF NOT EXISTS memories (
    stable_id TEXT PRIMARY KEY,
    schema_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    scope TEXT NOT NULL,
    subject_kind TEXT NOT NULL DEFAULT '',
    subject_name TEXT NOT NULL DEFAULT '',
    subject_canonical_id TEXT NOT NULL DEFAULT '',
    subject_aliases TEXT NOT NULL DEFAULT '',   -- JSON array
    field TEXT NOT NULL DEFAULT '',
    value TEXT NOT NULL,                        -- kind-typed JSON payload
    sources TEXT NOT NULL,                      -- JSON array of provenance sources
    derived_from TEXT NOT NULL DEFAULT '',      -- JSON array
    verified_by TEXT NOT NULL DEFAULT '',
    user_corrected INTEGER NOT NULL DEFAULT 0,
    corrected_by TEXT NOT NULL DEFAULT '',
    corrected_at TEXT NOT NULL DEFAULT '',
    confidence TEXT NOT NULL,
    volatility TEXT NOT NULL,
    last_verified_at TEXT NOT NULL DEFAULT '',
    stale_after_ns INTEGER NOT NULL DEFAULT 0,
    expires_at TEXT NOT NULL DEFAULT '',
    invalidation_status TEXT NOT NULL,
    superseded_by TEXT NOT NULL DEFAULT '',
    invalidation_reason TEXT NOT NULL DEFAULT '',
    content_hash TEXT NOT NULL,                 -- deterministic content digest
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- Filtered retrieval: query by kind / scope / subject (entity kind, name,
-- canonical ID) / sub-field.
CREATE INDEX IF NOT EXISTS idx_memories_kind ON memories(kind);
CREATE INDEX IF NOT EXISTS idx_memories_scope ON memories(scope);
CREATE INDEX IF NOT EXISTS idx_memories_subject_kind ON memories(subject_kind);
CREATE INDEX IF NOT EXISTS idx_memories_subject_name ON memories(subject_name);
CREATE INDEX IF NOT EXISTS idx_memories_subject_id ON memories(subject_canonical_id);

-- Lifecycle queries: find active, stale, or expired memories.
CREATE INDEX IF NOT EXISTS idx_memories_status ON memories(invalidation_status);
CREATE INDEX IF NOT EXISTS idx_memories_updated_at ON memories(updated_at);
CREATE INDEX IF NOT EXISTS idx_memories_expires_at ON memories(expires_at);

-- Migration tracking table
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL
);
