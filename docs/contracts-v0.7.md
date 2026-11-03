# Local Newsroom Editorial Memory Contracts (v0.7)

Version: v0.7 (Editorial memory domain contract, issue #141)
Status: Implemented in `internal/memory` (contracts, validation, serialization)
Updated: 2026-07-21

This document defines the v0.7 editorial-memory contract: the generic, versioned structures for **persistent editorial memory** — the first-class subsystem through which the newsroom remembers what it learned while operating.

The contract is implemented in the [`internal/memory`](../internal/memory/) package:

| File | Contents |
|---|---|
| `memory.go` | Package documentation (core principle: memory is context, not evidence) |
| `version.go` | `SchemaVersion` (`0.7.0`), `SchemaMajorCurrent`, `ErrUnsupportedSchemaVersion`, `ErrMemoryNotEvidence`, `SchemaMajor` |
| `kinds.go` | `MemoryKind` (6 kinds) + `AllKinds`, `MemoryScope` (7 scopes) + `AllScopes`, parsers, `MemorySource` |
| `volatility.go` | `Volatility` (5 bands) + `AllVolatilities`, `Freshness` (`IsStale`), `Expiration` + `InvalidationStatus` (`IsExpired`), `EntityRef` |
| `provenance.go` | `Provenance`, `ConfidenceLevel` (3 levels) |
| `record.go` | `MemoryStableID` (`mem-`), `MemoryRecord` envelope, `NewMemoryRecord`, `EncodeValue`/`DecodeValue`, `Validate`, `IsUsable`, `ErrKindValueMismatch` |
| `values.go` | `FactualValue`, `EntityValue`, `EntityRelationship` |
| `records.go` | `FeedbackRecord`, `CorrectionRecord`, `SuggestionRecord` + `SuggestionStatus`, `RecipeExecutionRecord` + `RecipeOutcome`, `RecipeFailure` |
| `collection.go` | `MemoryCollection`, `Marshal`, `UnmarshalMemoryCollection` |
| `evidence.go` | `AsVerifiedEvidence` (always returns `ErrMemoryNotEvidence`) |

Related documents:

- [`contracts-v0.6.md`](contracts-v0.6.md) — the publication plan memory sits alongside
- [`contracts-v0.5.md`](contracts-v0.5.md) — the run manifest memory references via IDs
- [`contracts-v0.4.md`](contracts-v0.4.md) — the verification pipeline memory must never bypass

---

## Table of Contents

1. [Design Goals](#1-design-goals)
2. [The Core Rule: Memory Is Context, Not Evidence](#2-the-core-rule-memory-is-context-not-evidence)
3. [Memory Taxonomy (MemoryKind)](#3-memory-taxonomy-memorykind)
4. [Scope (MemoryScope)](#4-scope-memoryscope)
5. [The MemoryRecord Envelope](#5-the-memoryrecord-envelope)
6. [Value Payloads](#6-value-payloads)
7. [Provenance](#7-provenance)
8. [Confidence](#8-confidence)
9. [Volatility, Freshness, and Expiration](#9-volatility-freshness-and-expiration)
10. [Entity References](#10-entity-references)
11. [Invariants](#11-invariants)
12. [Serialization and Versioning](#12-serialization-and-versioning)
13. [What This Contract Does Not Do](#13-what-this-contract-does-not-do)

---

## 1. Design Goals

1. **Separate from the archive.** The archive/RAG layer (`internal/archive`) answers "what do we know about the world, and where does each passage come from?" Memory answers "what did *this newsroom* learn while operating?" The two stores, their IDs, and their schemas are independent. Memory never reuses archive types, and archive retrieval never returns memory as if it were a source document.
2. **Generic and publication-agnostic.** No Bluesky, Hugo, or outlet details. A memory record speaks in entities, scopes, and kinds that any publication configuration can interpret.
3. **Explicit time model.** Volatility (how fast a fact changes in the world), freshness (when the record was last verified), and expiration (when the record stops being usable) are three explicit, orthogonal fields — never an implicit timestamp.
4. **Complete provenance for facts.** Every factual memory must be able to answer: where did this come from? when was it last verified? what source type supported it? was it user-corrected?
5. **Safe by construction.** The taxonomy cannot be mistaken for verified evidence: the Go type system routes values per kind, `Validate` enforces per-kind invariants, and `AsVerifiedEvidence` refuses by construction.

## 2. The Core Rule: Memory Is Context, Not Evidence

> A memory record is **contextual knowledge**, not automatically factual evidence.

A record may legitimately be used to:

- **resolve entities** (aliases, roles, disambiguation);
- **avoid repeated discovery work** (known feed URLs, known page structure);
- **prioritize verification** (facts with high volatility get verified first);
- **suggest sources** (what has worked before for this subject);
- **identify recurring editorial preferences** (style, tone, depth).

It must **never** silently bypass the normal sourcing/verification pipeline. A claim that should appear in an artifact must earn a verified status through `internal/researcher` → `internal/contracts.VerificationResult`; a memory record can *point at* a claim, never *be* the evidence for it.

Enforcement:

| Site | Mechanism |
|---|---|
| `MemoryRecord` (any kind) | `AsVerifiedEvidence(m)` returns `ErrMemoryNotEvidence` unconditionally |
| `FactualValue` | Documents itself as a claim to verify, not a verified claim |
| Persistence layer (v0.7+) | Expected to filter memory out of any retrieval that feeds `VerificationResult` |

The archive keeps its own provenance model (source URL, retrieval time, document hash). Memory provenance is additive, not a substitute: a memory may *cite* a source, but the source itself remains the evidence.

## 3. Memory Taxonomy (MemoryKind)

Six kinds, in canonical order:

| Kind | About | Value type | Provenance sources required |
|---|---|---|---|
| `entity` | Identity facts about a real-world entity: aliases, roles, relationships | `EntityValue` | no |
| `factual` | Claims about the world the newsroom believes to be true | `FactualValue` | **yes** (≥ 1 source + freshness stamp) |
| `editorial_feedback` | Human corrections/preferences about the newsroom's own output | `FeedbackRecord` | no |
| `recipe_execution` | How a recipe performed when run: outcomes, failures, lessons | `RecipeExecutionRecord` | no |
| `correction` | Corrections to previously published/persisted content | `CorrectionRecord` | no (but must record who/when) |
| `suggestion` | Proposals to refine profiles or recipes; never auto-applied | `SuggestionRecord` | no |

Kinds are orthogonal to subjects: the *subject* of a record is carried by `EntityRef` and `Scope`; the *kind* says what the record is operationally about.

## 4. Scope (MemoryScope)

Where a record applies, from narrowest to widest:

| Scope | Applies to |
|---|---|
| `run` | a single newsroom run |
| `source` | a single source (URL, feed, document) |
| `entity` | a single entity (see `Subject`) |
| `profile` | an author/editorial profile |
| `recipe` | a specific recipe |
| `publication` | a publication channel or outlet |
| `newsroom` | newsroom-wide, any subject matter |

## 5. The MemoryRecord Envelope

One document shape carries every kind:

```go
type MemoryRecord struct {
    SchemaVersion string                    // "0.7.0"
    StableID      MemoryStableID            // "mem-…"
    Kind          MemoryKind
    Scope         MemoryScope
    Subject       EntityRef                 // optional
    Field         string                    // optional sub-field, e.g. "current_team"
    Value         json.RawMessage           // kind-typed payload
    Provenance    Provenance
    Confidence    ConfidenceLevel
    Volatility    Volatility
    Freshness     Freshness
    Expiration    Expiration
    CreatedAt     time.Time                 // UTC
    UpdatedAt     time.Time                 // UTC
}
```

`Value` is a `json.RawMessage` so all kinds share one envelope; `DecodeValue`/`EncodeValue` are the only way in and out, and both enforce the kind↔value mapping (mismatch → `ErrKindValueMismatch`). The mapping:

| Kind | Value type |
|---|---|
| `factual` | `FactualValue` |
| `entity` | `EntityValue` |
| `editorial_feedback` | `FeedbackRecord` |
| `correction` | `CorrectionRecord` |
| `suggestion` | `SuggestionRecord` |
| `recipe_execution` | `RecipeExecutionRecord` |

## 6. Value Payloads

- **`FactualValue`** — `statement` (required) + optional structured `data` bag. A claim to verify, never a verified claim.
- **`EntityValue`** — `fields` (open attribute bag) + `relationships` (typed edges to other entities). Relationship `type`/`target` are both required.
- **`FeedbackRecord`** — `feedback` text (verbatim), `given_by`, `given_at` (all required); optional `recurring` flag and structured `preference` distillation. Recurring feedback is a preference to weigh, never an instruction to execute blindly.
- **`CorrectionRecord`** — `original_claim`, `corrected_claim`, `corrected_by`, `corrected_at` (all required); optional `affected_run_id` and `affected_memory_ids` so the persistence layer knows which factual memories to supersede or invalidate.
- **`SuggestionRecord`** — `suggestion_kind`, `proposal`, `status` (`proposed` → `approved` → `applied` | `rejected`); once the status leaves `proposed`, `reviewed_by`/`reviewed_at` are required. Applying a suggestion is an explicit, separately approved act.
- **`RecipeExecutionRecord`** — `recipe_id`, `outcome` (`success` | `partial` | `failure`), `executed_at` (required); optional `run_id`, distilled `lessons`, and stage-level `failures`.

## 7. Provenance

```go
type Provenance struct {
    Sources       []MemorySource // kind + id + title + retrieved_at + note
    DerivedFrom   []string       // records/artifacts this was distilled from
    VerifiedBy    string         // verification run/component that last confirmed
    UserCorrected bool
    CorrectedBy   string
    CorrectedAt   time.Time
}
```

`MemorySource.SourceType` is a free-form, publication-agnostic tag — `web`, `rss`, `json`, `csv`, `pdf`, `live`, `archive` (the usual evidence types), plus `user`, `llm`, `inferred` for non-web origins.

Every **factual** record must carry at least one source and a `Freshness.LastVerifiedAt`, so the four provenance questions of the ticket are always answerable:

| Question | Field |
|---|---|
| Where did this come from? | `Provenance.Sources` (+ `DerivedFrom`) |
| When was it last verified? | `Freshness.LastVerifiedAt` |
| What source type supported it? | `Provenance.Sources[*].SourceType` |
| Was it user-corrected? | `Provenance.UserCorrected` / `CorrectedBy` / `CorrectedAt` |

When `UserCorrected` is true, `CorrectedBy` and `CorrectedAt` are required.

## 8. Confidence

Three levels, mirroring the verification vocabulary but measuring *support for contextual knowledge*:

| Level | Meaning |
|---|---|
| `high` | multiple independent sources, or a direct human statement |
| `medium` | a single reasonably reliable source, or corroborated indirect evidence |
| `low` | weak, single, or possibly circular evidence |

Memory confidence never upgrades a record into verified evidence; verification confidence lives in `internal/contracts`.

## 9. Volatility, Freshness, and Expiration

Three orthogonal time properties:

- **`Volatility`** — how fast the underlying fact changes in the world. Five bands, most durable to least: `static`, `low`, `medium`, `high`, `ephemeral`.
- **`Freshness`** — `last_verified_at` (when the record was last confirmed) + `stale_after` (window after which re-verification is required; zero = volatility band alone guides consumers). `IsStale(t)`: never-verified is stale.
- **`Expiration`** — `expires_at` (absolute) + `invalidation_status` (`active` | `superseded` | `invalidated`) + `superseded_by` + `invalidation_reason`. `IsExpired(t)` gates usability; superseded/invalidated records remain valid documents (audit trail) but are not usable.

Staleness and expiry are deliberately distinct: a stale record may be *re-verified*; an expired record is done.

Worked examples from the ticket:

| Fact | Volatility |
|---|---|
| draft year | `static` |
| college | `low` |
| current team | `high` |
| injury status | `ephemeral` |
| author style preference | `static` (persistent until superseded) |

## 10. Entity References

```go
type EntityRef struct {
    Kind        string   // "person", "team", … (conventional, free-form)
    Name        string   // required
    CanonicalID string   // consumer-namespace ID; empty = name-only
    Aliases     []string
}
```

`EntityRef` intentionally does not inherit any specific entity model: the newsroom resolves it against its own entity store, a dossier, or nothing. `Matches` compares canonical IDs only (non-empty and equal); name/alias resolution is a consumer concern, not a contract concern.

## 11. Invariants

`MemoryRecord.Validate` enforces (all errors joined, not short-circuited):

1. `schema_version` is a semantic version this package understands (foreign major → `ErrUnsupportedSchemaVersion`).
2. `stable_id` is non-empty and carries the `mem-` prefix.
3. `kind`, `scope`, `confidence`, `volatility`, and `invalidation_status` are recognized enum values.
4. `value` is present and decodes into the type its kind requires (`ErrKindValueMismatch` otherwise).
5. `provenance` invariants hold (source fields; correction fields when `user_corrected`).
6. **Factual records**: ≥ 1 provenance source and a non-zero `freshness.last_verified_at`.
7. Superseded records carry `superseded_by`.
8. Timestamps: `created_at` ≤ `updated_at`; `corrected_at` ≤ `updated_at`; `last_verified_at` ≤ `updated_at` (whenever set).

`MemoryCollection.Validate` adds: collection schema version understood; every record valid; stable IDs unique within the collection.

## 12. Serialization and Versioning

- Every document (record, collection) carries `schema_version`, stamped to `0.7.0` on write.
- `MemoryCollection.Marshal` → canonical JSON. `UnmarshalMemoryCollection` **rejects** documents whose MAJOR version is not `0` (`ErrUnsupportedSchemaVersion`); MINOR/PATCH differences are accepted.
- Same versioning policy as the publication plan (`internal/publication`) and the run manifest (`internal/run`): independently versioned, semver-gated, round-trip tested.
- IDs follow the `mem-` prefix convention; persistence-layer derivation is out of scope.

## 13. What This Contract Does Not Do

- **No persistence.** No storage, indexing, retrieval, or migration logic — that is a follow-up ticket. The contracts are storage-neutral by design.
- **No evidence.** Memory is never a source of verified claims, and nothing here plugs into `VerificationResult`.
- **No policy.** The contracts express what a record *is*; whether a consumer *should* use a stale-but-active record of a given volatility is a consumer decision.
- **No entity store.** `EntityRef` is a reference, not an entity registry.
