// Package memory defines the generic domain contracts for the v0.7
// editorial-memory subsystem.
//
// Memory is a first-class subsystem, deliberately separate from the
// archive/RAG store (internal/archive): the archive answers "what do we know
// about the world and where does each passage come from?", while memory
// answers "what did *this newsroom* learn while operating?".
//
// Core principle: a MemoryRecord is contextual knowledge, not automatically
// factual evidence. A record may help resolve entities, avoid repeated
// discovery work, prioritize verification, suggest sources, or surface
// recurring editorial preferences. It must never silently bypass the normal
// sourcing/verification pipeline: memory cannot be mistaken for verified
// dossier evidence, and a memory-backed claim still has to earn a verified
// status through the standard contracts (internal/contracts).
//
// These contracts are publication-agnostic and persistence-neutral: they
// specify versioned, JSON-serializable structures with explicit
// volatility/freshness/expiry fields and invariant checks. Persistence
// (storage, indexing, retrieval) is a separate concern and is intentionally
// out of scope for v0.7.
package memory
