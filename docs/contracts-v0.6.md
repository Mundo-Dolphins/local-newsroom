# Local Newsroom Publication Contracts (v0.6)

Version: v0.6 (Publication domain contract)
Status: Implemented in `internal/publication` (domain) and `internal/publish` (persistence + engine, issue #119)
Updated: 2026-07-21

This document defines the v0.6 publication contract: the machine-readable **publication plan** and its companions (targets, approvals, attempts, receipts, dry runs) that control the first external side effects the newsroom can perform.

The contract is implemented in the [`internal/publication`](../internal/publication/) package:

| File | Contents |
|---|---|
| `publication.go` | `SchemaVersion`, `ErrUnsupportedSchemaVersion`, `PublicationID`, `NewPlan`, `Plan`, `ArtifactIdentity`, `GateEvidence`, the plan state machine (`Approve`, `RevokeApproval`, `RebaselineArtifact`, `SetGate`, `StartPublishing`, `Retry`, `Cancel`, `SkipTarget`, `AbortTarget`, `StartTarget`, `SucceedTarget`, `FailTarget`, `PartialTarget`, `RecordDryRun`, `RefreshStatus`), `AuthorizesPublish`, `Validate`, `UnmarshalPlan`, log bounds |
| `status.go` | `PublicationStatus` (8 states), `ValidPlanTransition`, `ValidTargetTransition` |
| `digest.go` | `Digest` (`sha256:<64 hex>`), `DigestOf`, `Valid`, `Matches`, `deriveID` |
| `target.go` | `TargetID`, `TargetType`, `PublicationTarget`, `DryRunOutcome`, `DryRunIssue`, `DryRunResult`, `validateTargetGraph` (acyclicity), `validateCredentialFree` |
| `approval.go` | `ApprovalID`, `ApprovalMethod`, `ApprovalStatus`, `PublicationApproval`, `EffectiveFor`, `Invalidate` |
| `attempt.go` | `AttemptStatus`, `PublicationAttempt`, `NewAttempt`, attempt-history invariants |
| `receipt.go` | `ReceiptID`, `PublicationReceipt`, `validateAbsoluteURL`, `validateURLValue` |

Related documents:

- [`contracts-v0.5.md`](contracts-v0.5.md) — the run manifest this contract layers on top of
- [`contracts-v0.4.md`](contracts-v0.4.md) — per-stage data contracts
- [`review-bundle-v0.5.md`](review-bundle-v0.5.md) — the READY review state the publication gate requires

---

## Table of Contents

1. [Design Goals](#1-design-goals)
2. [The Core Rule](#2-the-core-rule)
3. [The Digest: One Binding Primitive](#3-the-digest-one-binding-primitive)
4. [Stable Identifiers](#4-stable-identifiers)
5. [Status Model](#5-status-model)
6. [The Publication Plan](#6-the-publication-plan)
7. [Gate Evidence](#7-gate-evidence)
8. [Artifact Identity](#8-artifact-identity)
9. [Targets and Dependencies](#9-targets-and-dependencies)
10. [Dry Runs](#10-dry-runs)
11. [Approvals](#11-approvals)
12. [Attempts](#12-attempts)
13. [Receipts and External Identifiers](#13-receipts-and-external-identifiers)
14. [The Authorization Gate](#14-the-authorization-gate)
15. [Lifecycle API](#15-lifecycle-api)
16. [Partial Failure and Retry](#16-partial-failure-and-retry)
17. [Serialization and Versioning](#17-serialization-and-versioning)
18. [Redaction: No Credentials, Ever](#18-redaction-no-credentials-ever)
19. [What This Contract Does Not Do](#19-what-this-contract-does-not-do)
20. [Persistence and Idempotent Resume/Retry](#20-persistence-and-idempotent-resumetry)

---

## 1. Design Goals

1. **Explicit.** Publication is never a side effect of a successful run. It requires an explicit plan, an explicit human approval, and an explicit publisher invocation — three separate, persisted decisions.
2. **Auditable.** One JSON document per plan, versioned by `schema_version`. Every state change is recorded with a timestamp; the full history (approvals, attempts, receipts) travels inside the plan.
3. **Resumable.** A crashed or partially failed publication cycle restarts from the persisted plan: finished targets keep their receipts and are never re-executed; unfinished targets retry with a new attempt number.
4. **Idempotent.** Stable, derived IDs (`pub-…`, `appr-…`, `rcpt-…`) make re-creation and reconciliation deterministic. Receipts carry the publisher's external record ID so an external world can be diffed against the local record.
5. **Digest-bound.** The artifact is referenced by a content hash, never by content. The gate, the plan, the approval, the receipt, and the publisher's invocation must all agree on the exact same digest. Changing the artifact breaks the chain by construction, not by convention.
6. **Secret-free.** No field of any contract may hold a credential. Free text is redacted on construction, re-checked on `Validate()`, and URL-valued fields reject live `user:password@` material.

## 2. The Core Rule

A completed newsroom run **MUST NOT** publish automatically just because rendering succeeded. Publication of a run's artifact requires, in order:

1. a **persisted run** (`internal/run` manifest, referenced by `Plan.RunID`);
2. a **final factual check that passed** (`GateEvidence.FinalCheck`);
3. a **READY artifact/review state** (`GateEvidence.ReviewReady`);
4. an **explicit publication plan** (`Plan`, one artifact, one or more targets);
5. an **explicit human approval** bound to the exact artifact content (`PublicationApproval`);
6. a **publisher invocation** that first passes `AuthorizesPublish` (section 14).

`NewPlan` enforces rules 1–3: a plan cannot be created for an artifact whose gate evidence is not passing. Rule 5 is enforced by `Approve` and re-checked at every invocation by `AuthorizesPublish`. Rule 6 is the single authorization gate the publisher must pass before any external side effect.

## 3. The Digest: One Binding Primitive

```go
type Digest string // canonical form: "sha256:" + 64 lowercase hex chars
```

`DigestOf(bytes)` computes the canonical form. Every content-bearing decision in the contract compares digests with **exact, case-sensitive string equality** (`Digest.Matches`):

| Site | Binding |
|---|---|
| `GateEvidence.ArtifactDigest` | must equal the plan's artifact digest |
| `Plan.Artifact.Digest` | the baseline all other sites agree with |
| `PublicationApproval.ArtifactDigest` | the exact bytes the human approved |
| `DryRunResult.ContentDigest` | the bytes the rehearsal actually exercised |
| `PublicationReceipt.ContentDigest` | the bytes actually sent |
| `AuthorizesPublish(…, currentDigest)` | the bytes about to go out, computed by the caller |

The contract stores references and hashes, **never rendered content**: a plan for a 200 KB rendered page is a few KB of JSON.

## 4. Stable Identifiers

| ID | Prefix | Derived from |
|---|---|---|
| `PublicationID` | `pub-` | run ID + artifact digest + target IDs + creation time |
| `TargetID` | — (operator slug) | assigned by the operator, e.g. `bluesky-thread` |
| `ApprovalID` | `appr-` | plan ID + approver + digest + grant time |
| `ReceiptID` | `rcpt-` | plan ID + target ID + external record ID + publish time |

Derived IDs are `prefix-<UTC timestamp>-<12 hex chars of SHA-256 over the inputs>`: re-creating the same decision yields the same ID (idempotent resume, crash-safe reconciliation), while any changed input yields a different one. All IDs validate as `prefix-<1..128 of [A-Za-z0-9._-]>`.

## 5. Status Model

`PublicationStatus` is the shared lifecycle enumeration (8 states):

```
planned, approved, publishing, succeeded, failed,
partially_succeeded, cancelled, skipped
```

The same values are used at three levels, with different rules:

**Plan** (`ValidPlanTransition`):

```
planned  -> approved | cancelled | skipped
approved -> publishing | planned | cancelled | skipped   (approved -> planned: approval revoked or stale)
publishing -> any terminal state                          (cycle finished; RefreshStatus aggregates)
failed | partially_succeeded -> publishing               (retry / resume)
succeeded, cancelled, skipped                            (terminal: never transition)
```

**Target** (`ValidTargetTransition`):

```
planned -> publishing | skipped | cancelled
publishing -> succeeded | failed | partially_succeeded | cancelled
failed | partially_succeeded -> publishing               (retry)
succeeded, cancelled, skipped                            (terminal)
```

A target is never `approved`: approval is a plan-level act; the approval's scope may name targets.

**Attempt** (`AttemptStatus`, independent): `running, succeeded, failed, cancelled, partially_succeeded`.

**Approval** (`ApprovalStatus`, independent): `granted, stale, revoked`.

## 6. The Publication Plan

```json
{
  "schema_version": "0.6.0",
  "id": "pub-20260711T100000-78426f27eaf4",
  "run_id": "run-feed-20260711T100000-…",
  "workspace": "feed",
  "status": "approved",
  "artifact": {
    "id": "art-001",
    "kind": "rendered",
    "path": "output/dolphins-preview.md",
    "digest": "sha256:4513…",
    "size": 197,
    "version": "render/v1"
  },
  "gate": {
    "run_status": "succeeded",
    "final_check": "pass",
    "review_ready": true,
    "artifact_digest": "sha256:4513…",
    "checked_at": "2026-07-11T10:00:00Z",
    "note": "final check passed; review READY"
  },
  "targets": [
    { "id": "bluesky-thread", "name": "Dolphins thread", "type": "post",
      "provider": "bluesky", "destination": "at://com.example/…",
      "status": "planned", "depends_on": [] },
    { "id": "hugo-home", "name": "Site page", "type": "page",
      "provider": "hugo", "destination": "sites/news/dolphins-preview",
      "status": "planned", "depends_on": ["bluesky-thread"] }
  ],
  "approvals": [ { "id": "appr-…", "approved_by": "editor", "method": "manual",
                   "artifact_digest": "sha256:4513…", "targets": ["bluesky-thread"],
                   "status": "granted", "granted_at": "2026-07-11T10:00:10Z" } ],
  "created_at": "2026-07-11T10:00:00Z",
  "updated_at": "2026-07-11T10:00:10Z"
}
```

The plan is the **unit of publication persistence**: one JSON document, loaded and saved by later orchestrator tooling (v0.6.x). `UnmarshalPlan` rejects unsupported major versions and any document that fails `Validate`.

`NewPlan` accepts: a valid run reference, a validated artifact identity, passing gate evidence bound to that digest, and at least one (≤ 32) target with a unique-ID, cycle-free dependency graph. Caller-set target states are discarded — a new plan always starts clean.

## 7. Gate Evidence

`GateEvidence` is the point-in-time proof, gathered from the persisted run record (manifest, final-check result, review bundle), that the artifact is READY to publish:

| Field | Requirement |
|---|---|
| `run_status` | must be `succeeded` |
| `final_check` | must be `pass` |
| `review_ready` | must be `true` |
| `artifact_digest` | must equal the plan's artifact digest |
| `checked_at` | required; the snapshot time |
| `note` | optional, redacted, bounded |

`GateEvidence.Validate` enforces all of the above (a non-passing gate cannot exist inside a plan document), and `GateEvidence.Passing()` exposes the same rule for use in the constructors (`NewPlan`, `SetGate`). After the artifact is rebaselined the evidence is stale and `SetGate` must record fresh evidence gathered from the current bytes.

## 8. Artifact Identity

`ArtifactIdentity` is a reference to the artifact in the run directory:

- `id` / `kind` / `path` — the manifest artifact (path is relative, `..` segments rejected);
- `digest` — SHA-256 of the persisted bytes, recorded **before** publishing;
- `size` — byte length (audit convenience; no content stored);
- `version` — optional content version tag (e.g. renderer version).

The reference is stable across rebaselining; only `digest`, `size`, and `version` may change, which is what makes `RebaselineArtifact` well-defined.

## 9. Targets and Dependencies

A `PublicationTarget` is one publication destination:

- `id` — operator-assigned slug (unique within the plan);
- `name` — human-facing label (redacted, bounded);
- `type` — `post | page | repository | custom` (format-neutral; the contract does not know about Bluesky or Hugo);
- `provider` — the publisher name (e.g. `bluesky`, `hugo`);
- `destination` — target-specific location: a URL, an atproto URI, a repo path, a handle. Must be non-empty and credential-free (section 18);
- `depends_on` — IDs of targets that must complete first;
- lifecycle fields: `status`, `skip_reason`, `error`, `receipt`, `attempts`, `dry_run`.

`validateTargetGraph` enforces: unique target IDs, dependencies on known targets, no duplicate dependencies, and **no cycles** (DFS). Dependency ordering is the plan's publication order: a publisher processes a target only after its dependencies have finished — and `AuthorizesPublish` additionally refuses a target whose dependency has not finished, so the order survives a persisted, reloaded plan.

## 10. Dry Runs

`DryRunResult` records a rehearsal for one target:

- `performed_at` — required;
- `outcome` — `ok | failed`;
- `content_digest` — must equal the plan's artifact digest (a rehearsal of different bytes proves nothing);
- `details` — redacted, bounded;
- `issues` — bounded list of `{code, message}` for a failed rehearsal.

A dry run has **no external side effects and changes no status** — the plan may be `planned` or `approved`. It is the operator's preview of what publication would do, recorded for the audit trail.

## 11. Approvals

`PublicationApproval` is the explicit human decision, and the heart of the safety model:

- `approved_by` — the human principal;
- `method` — `cli | tui | manual | custom`;
- `artifact_digest` — **the exact bytes approved**;
- `targets` — scope (empty = every target in the plan);
- `status` — `granted | stale | revoked`;
- `granted_at` / `invalidated_at` / `invalidation_reason`.

An approval is **effective** (`EffectiveFor(digest, target)`) only while `granted` **and** its digest equals the plan's *current* artifact digest **and** its scope covers the target. This is a checked property, not a flag:

- `RebaselineArtifact` (content changed) mechanically sets every non-matching approval to `stale`;
- `RevokeApproval` sets it to `revoked` (a closed approval can never be re-closed or re-granted);
- if an approved plan loses its last effective approval, it falls back to `planned`.

A plan may carry up to 8 approvals (bounded history, oldest dropped); the most recent effective one governs. `AuthorizesPublish` re-checks the effective-approval invariant at **every** invocation, so a tampered or stale document can never authorize publication.

**Stale approval cannot authorize modified content** — acceptance criterion: after a rebaseline, `AuthorizesPublish` fails for every target until a human re-approves the new bytes.

## 12. Attempts

`PublicationAttempt` is one execution of a target: a 1-based strictly increasing `number`, a `running → terminal` status, `started_at` / `finished_at`, and — for the terminal outcome — an `error` summary (failed) or a `receipt` (succeeded / partially succeeded).

History invariants: a `planned`/`skipped` target carries no attempts; a `publishing` target's last attempt is `running`; a `succeeded` target's last attempt is `succeeded`; history is bounded (16 attempts, oldest dropped and renumbered). These invariants are what make a plan **crash-resumable**: reload it, and the attempt history tells the publisher exactly where the last cycle died.

## 13. Receipts and External Identifiers

`PublicationReceipt` is the external proof a publisher returns:

| Field | Rule |
|---|---|
| `external_uri` | optional; must be an absolute **http(s)** URL; a redacted `user:[REDACTED]@` is accepted, a live credential is rejected |
| `external_record_id` | the platform's own ID (commit SHA, post record ID); bounded |
| `content_digest` | **must equal the approved artifact digest** — a receipt for different bytes is rejected |
| `idempotency_key` | the client dedup token, when the platform has one |
| `client_version` | the publisher implementation's version (audit trail) |
| `metadata` | ≤ 32 bounded, redacted `key: value` pairs (never credentials) |
| `published_at` | required |

Receipts reference the external world by URI/record ID and are themselves digest-bound: a `SucceedTarget` with a receipt for different bytes is an error, because it would record that the publisher sent content the human never approved.

## 14. The Authorization Gate

`AuthorizesPublish(target, currentDigest)` is the single check a publisher must pass before any external side effect. It returns `nil` only when **all** of:

1. the plan is `approved` or in a resumed `publishing` cycle;
2. the recorded gate evidence is passing and still binds the plan's artifact;
3. the target exists in the plan;
4. an effective approval exists for the target (granted + exact digest + in scope);
5. `currentDigest` — the bytes actually about to go out, computed by the caller — equals the plan's recorded artifact digest (the staleness check);
6. the target is in a state that permits (re)publication: `planned` (first time), `failed`, or `partially_succeeded` (retry).

Everything else is a diagnostic error: a finished target ("already published — re-publishing requires a new plan"), a skipped or cancelled target, an in-flight target (an attempt is running), a stale digest, or a missing approval. Publishers receive one authoritative answer per invocation and one reason when the answer is no.

## 15. Lifecycle API

All mutators validate timestamps against the plan's clock (a backwards timestamp is rejected — plans never record a regressing history) and refuse to touch a terminal plan (a finished plan is an immutable record, exactly like a finished run manifest).

| Method | Effect |
|---|---|
| `Approve(approval, at)` | records a granted, digest-bound approval; plan → `approved` |
| `RevokeApproval(id, at, reason)` | closes a granted approval; approved plan with no effective approval → `planned` |
| `SetGate(gate, at)` | re-records passing gate evidence bound to the current artifact |
| `RebaselineArtifact(artifact, at)` | moves the artifact to new bytes (same reference); stales non-matching approvals; approved plan without an effective approval → `planned`; records a warning |
| `StartPublishing(at)` | `approved → publishing` (the explicit cycle start; the only point a publisher may act) |
| `Retry(at)` | `failed`/`partially_succeeded → publishing` (resume) |
| `Cancel(at, reason)` | any active state → `cancelled`; in-flight targets are aborted; terminal reason recorded redacted |
| `SkipTarget(id, reason, at)` | a planned target → `skipped` (deliberate decision, with reason) |
| `AbortTarget(id, at)` | crash recovery: an in-flight target → `cancelled`, its running attempt ended |
| `StartTarget(id, at)` | within a cycle: a (re)startable target → `publishing`, new running attempt appended |
| `SucceedTarget(id, receipt, at)` | ends the running attempt as succeeded; stores the digest-checked receipt |
| `FailTarget(id, code, message, at)` | ends the attempt as failed; records the (redacted) error on the target and in the plan log |
| `PartialTarget(id, receipt, at)` | ends the attempt as partially succeeded; the target remains retryable |
| `RecordDryRun(id, result, at)` | stores a digest-bound rehearsal; no status change |
| `RefreshStatus(at)` | after a cycle: when every target is terminal, aggregates and records the plan's terminal state + reason |

## 16. Partial Failure and Retry

`RefreshStatus` is the only way a plan reaches a terminal aggregated state:

| Target outcomes (all terminal) | Plan state |
|---|---|
| every target succeeded (or skipped, with at least one succeeded) | `succeeded` |
| ≥ 1 succeeded and ≥ 1 failed / partially succeeded / cancelled | `partially_succeeded` |
| no target succeeded (skipped targets don't count) | `failed` |
| every target skipped | `skipped` |

With any target still active the plan keeps its current status — the cycle is not complete, and the document persists in that honest state.

Retry then resumes the plan (`Retry` → `publishing`), and the publisher re-executes **only** the targets that did not finish:

- a `succeeded` target is never re-executed (its receipt is the dedup proof);
- a `failed` target starts a new, consecutively numbered attempt;
- a `partially_succeeded` target retries, and the publisher is expected to deduplicate against the existing receipt's external record IDs.

The terminal record and the retry history are both durable inside the same plan document — the audit trail of a partial publication is complete.

## 17. Serialization and Versioning

- `SchemaVersion` is `"0.6.0"`; `schema_version` is a semantic version string on every plan document.
- `UnmarshalPlan` is the parse boundary: it rejects a missing or malformed version, rejects an **unsupported major version** with `ErrUnsupportedSchemaVersion`, and runs the full `Validate` (so an invalid document cannot be loaded, only rejected).
- Unknown fields inside a major version are ignored (forward compatibility with future 0.x schemas); all fields this version knows must be present and valid.
- Timestamps serialize as RFC 3339 (UTC, second precision) via `run.Timestamp`; zero timestamps marshal as `null`.
- `Validate` is idempotent and deterministic: it re-checks every cross-field invariant (section 6, 11, 14 and the per-contract rules), including redaction of free text, so a document loaded from disk is trusted only after `Validate` passes.

## 18. Redaction: No Credentials, Ever

The contract is secret-free by construction, with defense in depth:

1. **Construction** — every free-text field (notes, reasons, details, metadata values) is passed through `run.RedactSecrets` in its constructor or mutator.
2. **Validation** — `Validate` re-runs `run.LooksLikeSecret` on the same fields, so a hand-edited or deserialized document containing a detected secret fails validation instead of loading.
3. **URLs** — receipt external URIs must be absolute http(s) URLs; their userinfo is checked before parsing: a live `user:password@` is a credential and rejected, the redaction placeholder is accepted (Go's `url.Parse` cannot re-parse the placeholder's brackets, so the check runs on the raw string first). Target destinations that are URLs get the same credential rule; non-URL destinations (handles, paths, atproto URIs) get the secret-material check.
4. **Digests are safe to record** — a canonical `sha256:<64 hex>` digest uses two character classes and never trips the secret detectors, so the binding primitive itself is auditable in plaintext.

A plan, approval, attempt, or receipt that would carry a credential fails validation — credentials do not cross the contract boundary in any direction.

## 19. What This Contract Does Not Do

This issue (Mundo-Dolphins/local-newsroom#114) defines the **domain contract only**:

- **No publisher implementations.** Nothing here talks to Bluesky, Hugo, Git, or any external API. The publisher is a later v0.6 component that *consumes* `AuthorizesPublish` and *produces* receipts. (The Hugo static-site *staging adapter* — rendered article to `content/` file, plan-agnostic, no receipts — has landed separately as `internal/hugo`; see [hugo-publication.md](hugo-publication.md). The Git *committing adapter* — staged files to a local commit on a dedicated branch, producing receipts with commit SHA + branch, never pushing — has landed separately as `internal/gitpub`; see [git-publication.md](git-publication.md). The Bluesky *social publisher* — approved posts to a thread over the AT Protocol, producing receipts with the post AT URIs, resumable and dry-run capable — has landed separately as `internal/bluesky` on the generic `internal/social` publisher types; see [bluesky-publication.md](bluesky-publication.md).)
- **No filesystem or network I/O.** The `internal/publication` package is pure serializable documents and state machines, mirroring `internal/run`. Persistence, the engine, and the publisher adapters are the `internal/publish` layer added by issue #119 (section 20).
- **No content storage.** Plans reference artifacts by digest; rendered bytes stay in the run directory.

The contract is complete when: a run cannot publish on its own; a human approval to exact bytes is the only thing that can; every external effect leaves a durable, secret-free, digest-bound record; and a failed half-way cycle can be resumed without double-publishing.

## 20. Persistence and Idempotent Resume/Retry

Issue [Mundo-Dolphins/local-newsroom#119](https://github.com/Mundo-Dolphins/local-newsroom/issues/119) wires this contract into the run/workspace persistence layer, implemented in [`internal/publish`](../internal/publish/). Publication is a **post-run state**, not a pipeline stage: the v0.5 stage graph is untouched, the run must be `succeeded` and the review bundle `READY`, and the plan document is the durable record of the publication itself.

### Run directory layout

```
runs/<run-id>/
  manifest.json                 # run manifest, schema 0.6.0 (gains publication_state)
  sources/ fetch/ documents/ research/ verify/ final_check/ output/   # v0.5 trees, unchanged
  publication/
    plans/<plan-id>.json        # the complete plan document (this contract, schema 0.6.0)
    content/<name>              # derived publication content (e.g. the canonical NUL-joined thread)
```

The plan document is persisted through the workspace store's artifact path (the single sanitization boundary) and is referenced — never embedded — from the manifest's new optional `publication_state` summary: plan reference (`plan_id`, `plan_path`), `plan_hash`, plan status, and target counts. See [contracts-v0.5.md §12](contracts-v0.5.md#12-serialization-and-versioning): manifests written at `0.5.0` load unchanged with `publication_state` absent.

### What is persisted where

| Persisted data | Where | Boundary |
|---|---|---|
| plan (targets, gate, artifact identity, status, warnings) | `publication/plans/<plan-id>.json` | sanitized; hashed after read-back |
| approval records (method, digest-bound, status) | inside the plan document | redacted free text; validated on load |
| approved artifact hash (digest) | plan artifact identity + each approval | `sha256:<64 hex>` |
| per-target status, attempts (full history) | plan targets | validated on load |
| receipts (external URIs/record IDs, content digest) | plan targets | absolute-URL rule; digest must match the plan |
| partial success + retry information | plan status + attempt history | `RefreshStatus` aggregation |
| timestamps | plan, attempts, manifest summary | monotonic; regressions rejected |
| plan summary for the run | manifest `publication_state` | sanitized with the manifest |

### Read-back discipline (why hashes stay honest)

Sanitization can change bytes on the way to disk, so every persisted document is **re-read and digested after writing**: `plan_hash` is the digest of the sanitized bytes actually on disk, and the approval/plan chain is validated against that digest. On load, the engine cross-checks the persisted plan document against the manifest's recorded `plan_hash`: a mismatch means the document changed after the manifest was written, and the load fails closed (`ErrPlanInvalid`) rather than trusting either side.

### Idempotent resume

`publish.Engine.Publish` is safe to call any number of times over the same store — including across process restarts. Per target, the persisted status decides what a new cycle does:

| Target status at resume | What a new cycle does |
|---|---|
| `succeeded` | never touched — the receipt is the dedup proof; no external call |
| `skipped` | never touched — the skip was a deliberate decision |
| `planned` | executed (only if the plan is authorized) or blocked on an incomplete dependency |
| `failed` | re-executed with a new attempt number; a previously recorded receipt is passed to the publisher as prior progress |
| `partially_succeeded` | re-executed; the partial receipt's external record IDs are passed so the publisher deduplicates what it already did (a half-posted thread is completed, never reposted) |
| `publishing` (dangling — the process died mid-call) | aborted to `cancelled` — the external outcome is unproven, so the target is never blindly re-executed within this plan; re-publishing is a new plan |

The engine persists the plan after every state transition, so a crash leaves a document that validates and resumes honestly. Publisher outcomes map to state: a receipt with no error succeeds the target; a receipt *with* an error records partial success (resumable); an error without a receipt fails the target; and a receipt that fails validation (wrong content digest, malformed) is discarded and the target fails — a success without matching proof is not a success.

### Sanitization at the publication boundary

Every plan, receipt, reason, and content byte crosses the same single workspace sanitization boundary as v0.5 content. One classification rule is specific to the canonical thread form: NUL-separated content that is text (each NUL segment valid UTF-8) is sanitized per segment with the full pattern detectors, and a segment that is still dirty after sanitization fails the write (fail closed) — because the store's binary class would otherwise skip the detectors on NUL-joined bytes and let a leaked token reach disk. The bytes persisted are the sanitized bytes, and the plan baselines exactly those.

### Publisher adapters

`internal/publish` ships the two digest-binding bridges the CLI wires up (issue #121): `NewBlueskyPublisher` (splits the canonical NUL-joined thread content into posts, hands the plan's prior receipt to `internal/social`, and returns a receipt over the real post URIs) and `NewGitPublisher` (one file, one commit; an already-committed identical file is skipped, so a retried cycle never duplicates the local commit).
