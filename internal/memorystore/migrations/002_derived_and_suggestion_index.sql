-- Schema version: 2
-- Created: v0.7.1 — derived-source and suggestion indexing
--
-- Backward compatible: adds columns with defaults and a partial index.
-- Existing 001 databases upgrade in place; no data is rewritten.

-- The most recent correction a record derives from, for quick audit lookups
-- ("which correction invalidated this fact?"). Kept as plain text: it is a
-- convenience index, not source of truth (the full list lives in
-- derived_from).
ALTER TABLE memories ADD COLUMN derived_head TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_memories_derived_head ON memories(derived_head);

-- Suggestions have a lifecycle status inside their value payload; indexing
-- the common "open suggestions" query avoids scanning the JSON.
ALTER TABLE memories ADD COLUMN suggestion_status TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_memories_suggestion_status ON memories(suggestion_status)
    WHERE suggestion_status = 'proposed' OR suggestion_status = 'approved';
