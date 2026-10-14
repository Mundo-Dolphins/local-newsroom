# Local Newsroom Run Manifest Contracts (v0.5)

Version: v0.5 (Run & Workspace persistence contract)
Status: Implemented in `internal/run`
Updated: 2026-07-21

This document defines the v0.5 persistence contract: the machine-readable **run manifest** that records one complete newsroom pipeline run, so that every run is reproducible, inspectable, and resumable.

The contract is implemented in the [`internal/run`](../internal/run/) package:

| File | Contents |
|---|---|
| `manifest.go` | `Manifest`, `RunID`, `Workspace`, `SourceMode`, `Profile`, `ModelMetadata`, `ConfigSnapshot`, `Warning`, `ErrorSummary`, `ArtifactRef`, `Result`, lifecycle mutators, `Validate`, `MarshalManifest` / `UnmarshalManifest` |
| `status.go` | `RunStatus`, `StageStatus`, `AttemptStatus` rules, canonical stage list, transition validators |
| `stage.go` | `StageRecord`, `StageAttempt` + validation |
| `timestamp.go` | `Timestamp` (UTC, RFC 3339 nano) |
| `redact.go` | `RedactSecrets`, `RedactSecretsCounted`, `LooksLikeSecret`, `IsSecretKey`, `RedactedPlaceholder` |
| `sanitizer.go` | `Sanitizer`, `NewSanitizer` — the single redaction boundary (exact configured values + pattern detectors) |

Related documents:

- [`contracts-v0.4.md`](contracts-v0.4.md) — per-stage data contracts (unchanged in v0.5)
- [`tui-v0.5.md`](tui-v0.5.md) — the keyboard-first TUI that renders these manifests
- [`workflow-v0.2.md`](workflow-v0.2.md) — pipeline and stages
- [`pipeline-flow.md`](pipeline-flow.md) — pipeline flow overview
- [`JSON-Schema-Validation-Strategy.md`](JSON-Schema-Validation-Strategy.md) — why JSON Schema is not the primary gate
- `schemas/run-manifest.json` — JSON Schema (draft-07) of the manifest document
- `schemas/examples/run-manifest-example.json` — worked example (validated by the test suite)

---

## Table of Contents

1. [Design Goals](#1-design-goals)
2. [Run Identity](#2-run-identity)
3. [Status Model](#3-status-model)
4. [Manifest Structure](#4-manifest-structure)
5. [Stage Records and Attempts](#5-stage-records-and-attempts)
6. [Artifacts, Warnings, and Errors](#6-artifacts-warnings-and-errors)
7. [Configuration Snapshot](#7-configuration-snapshot)
8. [Model and Endpoint Metadata](#8-model-and-endpoint-metadata)
9. [Lifecycle API](#9-lifecycle-api)
10. [Stage Dependencies and Resumability](#10-stage-dependencies-and-resumability)
11. [State Transitions](#11-state-transitions)
12. [Serialization and Versioning](#12-serialization-and-versioning)
13. [Redaction](#13-redaction)
14. [Validation Rules](#14-validation-rules)
15. [Schema File](#15-schema-file)

---

## 1. Design Goals

1. **Reproducible.** The manifest records everything needed to explain a run: workspace, topic, source mode, profile, model/endpoint, redacted config, stage plan, attempts, artifacts, and timing.
2. **Inspectable.** One JSON document, one run. Stable IDs and deterministic stage names make runs queryable and diffable.
3. **Resumable.** The manifest is the source of truth for run state, and v0.5 ships the full stack on top of it: the workspace store persists the manifest and stage artifacts, and the resume layer (`internal/resume`) continues interrupted runs and explicitly retries stages, reusing completed work and invalidating stale downstream output. See [Stage Dependencies and Resumability](#10-stage-dependencies-and-resumability).
4. **Honest, and secret-free at every persistence boundary.** Error messages, warnings, and config values are redacted; credentials never appear in the manifest. local-newsroom never intentionally persists credentials: the workspace store (the persistence layer) sanitizes every byte that crosses the persistence boundary — every artifact of every kind, source and document content included, and the manifest — before it reaches disk. This policy deliberately overrides byte-identical source preservation: a source or document that contains a detected secret is persisted redacted, with redaction metadata, and source fidelity may be reduced by redaction. See [Redaction]((#13-redaction)) for the boundary contract.
5. **Not every run runs everything.** A run plans an ordered subset of the canonical stages; the plan is part of the manifest.
6. **Versioned.** `schema_version` is a semantic version string. Consumers reject unknown major versions and ignore unknown fields within a major.

Out of scope for v0.5: loading configuration files and the TUI. Filesystem persistence (the workspace store) and resume/retry are in scope; CLI presentation of resume/retry is a separate task.

---

## 2. Run Identity

### RunID

```
run-<yyyymmddThhmmss>Z-<12-hex digest>
```

- Prefix: `run-` (constant `RunIDPrefix`).
- The time part is the run's `started_at` rendered as `yyyymmddThhmmss` in UTC.
- The digest is the first 12 hex characters of `SHA-256("<workspace name>\n<topic>\n<started-at-UTC>)` — deterministic for identical inputs.
- Constraint: `^run-[a-zA-Z0-9._-]{1,128}$`.

`NewRunID(workspace, topic, startedAt)` derives the ID; `NewManifest` calls it.

### Workspace

```go
type Workspace struct {
    Name string `json:"name"`             // required, [a-zA-Z0-9][a-zA-Z0-9._-]{0,63}
    Path string `json:"path,omitempty"`   // optional directory, no path traversal
}
```

The workspace name is also its directory name on disk (matching v0.4 workspace conventions). Paths must not traverse (`..` segments are rejected).

---

## 3. Status Model

### Run status

| Status | Meaning | Terminal? |
|---|---|---|
| `pending` | created, not started | no |
| `running` | executing | no |
| `succeeded` | completed, all planned stages succeeded or were skipped | yes |
| `failed` | a stage failed and the run was stopped | yes |
| `cancelled` | stopped by request before completion | yes |

Valid run transitions (matrix in `ValidRunTransition`, `status.go`):

| From \ To | pending | running | succeeded | failed | cancelled |
|---|---|---|---|---|---|
| pending | — | ✓ | ✗ | ✗ | ✓ |
| running | ✗ | ✗ | ✓ | ✓ | ✓ |
| succeeded / failed / cancelled | ✗ | ✓ (Reopen) | ✗ | ✗ | ✗ |

A terminal run is immutable **except for `Reopen`**: `Reopen` is the single, deliberate path back from a terminal state to `running`. It clears the terminal result (outcome, reason, failed stage, `finished_at`), records a run-level warning, and preserves everything else — every stage record, attempt history, artifact reference, and log entry. `Reopen` never re-runs anything by itself; the resume layer (or an explicit retry) decides what runs next. See [Stage Dependencies and Resumability](#10-stage-dependencies-and-resumability).

### Stage status

| Status | Meaning |
|---|---|
| `pending` | planned, not started |
| `skipped` | explicitly skipped with a recorded reason |
| `running` | an attempt is in flight |
| `succeeded` | completed successfully |
| `failed` | the latest attempt failed (retryable) |
| `cancelled` | stopped while running (retryable as an interrupted attempt) |

`succeeded`, `skipped`, `failed`, and `cancelled` are terminal for the stage **as a lifecycle transition**: `failed` and `cancelled` stages may start a new attempt (`→ running`) — that is how a failed stage is retried and how an interrupted stage is recovered. `succeeded` and `skipped` stages cannot start a new attempt through the normal lifecycle; they are only pulled back to `pending` by `InvalidateStage` (an out-of-band reset used when an upstream stage re-runs, see [Stage Dependencies and Resumability](#10-stage-dependencies-and-resumability)).

`skipped` is terminal and **distinct** from `pending`: a skipped stage was a deliberate decision, a pending stage simply has not been reached.

### Attempt status

| Status | Meaning |
|---|---|
| `running` | attempt in flight |
| `succeeded` | attempt succeeded |
| `failed` | attempt failed |
| `cancelled` | attempt stopped by run cancellation |

Attempts are never `pending` or `skipped`: skipping is a stage-level decision, while an attempt is one concrete execution.

---

## 4. Manifest Structure

```json
{
  "schema_version": "0.5.0",
  "id": "run-20240115T120000Z-4a367f7b8ca6",
  "workspace": { "name": "mundodolphins", "path": "workspace/mundodolphins" },
  "status": "succeeded",
  "topic": "EU AI Act enters into force",
  "source_mode": "explicit",
  "profile": { "id": "mint", "source_path": "profiles/mint", "hash": "a3f9c81d…f7f8" },
  "model": { "provider": "omlx", "model": "llama3.1:8b", "endpoint": "http://localhost:8000/v1" },
  "config": {
    "values": {
      "omlx_api_key": "[REDACTED]",
      "omlx_base_url": "http://localhost:8000/v1",
      "omlx_model": "llama3.1:8b"
    }
  },
  "stages": [
    { "name": "discovery", "status": "skipped", "skip_reason": "explicit URLs provided" },
    {
      "name": "fetch",
      "status": "succeeded",
      "attempts": [
        {
          "number": 1,
          "status": "succeeded",
          "started_at": "2024-01-15T12:00:03Z",
          "finished_at": "2024-01-15T12:00:10Z"
        }
      ]
    }
  ],
  "artifacts": [
    {
      "kind": "source",
      "id": "source-bbva-ai-act",
      "stage": "fetch",
      "path": "workspace/mundodolphins/runs/2024-01-15/sources/source-bbva-ai-act.md",
      "produced_at": "2024-01-15T12:00:10Z"
    }
  ],
  "warnings": [
    { "code": "partial_discovery", "message": "one source was unreachable; continuing with the rest", "occurred_at": "2024-01-15T12:00:45Z" }
  ],
  "errors": [],
  "result": { "outcome": "succeeded" },
  "started_at": "2024-01-15T12:00:00Z",
  "updated_at": "2024-01-15T12:02:11Z",
  "finished_at": "2024-01-15T12:02:11Z"
}
```

| Field | Type | Notes |
|---|---|---|
| `schema_version` | string | `0.5.0` for this contract. Semantic version; consumers gate on the major. |
| `id` | string | `RunID`, see [Run Identity](#2-run-identity). |
| `workspace` | object | Name + optional path. |
| `status` | enum | Current run status. |
| `topic` | string | 1–1000 chars, non-whitespace. |
| `source_mode` | enum | `explicit`, `discovered`, `supplement`, `unknown` (default). |
| `profile` | object, optional | Profile id + optional source path and content hash. |
| `model` | object | Provider, model, endpoint. Built via `NewModelMetadata`, which redacts endpoint credentials. |
| `config` | object | Redacted `map[string]string` snapshot. |
| `stages` | array | Planned stages, in pipeline order. No duplicates. |
| `artifacts` | array, optional | Run-level artifact references. |
| `warnings` | array, optional | Non-fatal warnings. |
| `errors` | array, optional | Run-level error summaries (stage errors live on attempts). |
| `result` | object | Terminal result: outcome, reason, optional `failed_stage`. Empty while in progress. |
| `started_at` | timestamp | Set at creation (defaults to "now" if not provided). |
| `updated_at` | timestamp | Monotonic; updated on every lifecycle event. |
| `finished_at` | timestamp, nullable | Set exactly when the run reaches a terminal status. |

A complete worked example is [`schemas/examples/run-manifest-example.json`](schemas/examples/run-manifest-example.json).

### Source mode

| Value | Meaning |
|---|---|
| `explicit` | The user supplied the URLs to fetch. |
| `discovered` | Sources were found by the discovery stage. |
| `supplement` | A mixed run supplementing previously discovered sources (v0.3 semantics). |
| `unknown` | Default; the mode is not recorded. |

---

## 5. Stage Records and Attempts

### Stage plan

`CanonicalStages()` returns the nine pipeline stages in order:

```
discovery → fetch → extract → research → verify → follow_up → write → final_check → render
```

A run plans an **ordered subset** of these stages:

- `NewManifest` defaults to the full canonical pipeline; pass `Stages` to plan a subset.
- Stage names must be valid canonical names; duplicates are rejected.
- **The plan must be a topological order of the stage dependency graph** (see [Stage Dependencies and Resumability](#10-stage-dependencies-and-resumability)): every stage's dependencies, when present in the plan, must appear earlier. `NewManifest` and `Manifest.Validate` both enforce this, so an inconsistent plan can never be persisted or loaded.
- The plan is preserved verbatim in the manifest — a run that skips `discovery` still *plans* it (as a `skipped` record) or omits it entirely; consumers must never assume every run executes every stage.

### StageRecord

| Field | JSON | Notes |
|---|---|---|
| `Name` | `name` | Canonical stage name. |
| `Status` | `status` | See [Status Model](#3-status-model). |
| `SkipReason` | `skip_reason,omitempty` | Required when `skipped`; must be empty otherwise. |
| `Attempts` | `attempts,omitempty` | Chronological history, numbered 1..N (bounded, see below). |

Consistency: the stage status must agree with the attempt history — a `skipped` stage has no attempts; a `running` stage's latest attempt is `running`; a terminal stage's status equals its latest attempt's status (except `skipped`). A `pending` stage may carry **terminal** attempt history: after `InvalidateStage` resets a previously completed stage, its past attempts are preserved (see below), and the stage simply gets a new attempt when it next starts. A `pending` stage never carries a `running` attempt. **Bounded history:** the manifest keeps only the most recent `MaxAttemptsPerStage` (32) attempts per stage — the oldest are dropped and the survivors renumbered 1..N — and run-level `errors`/`warnings` are bounded at `MaxRunErrors`/`MaxRunWarnings` (64 each, newest kept). A stage may therefore be retried indefinitely across resumes without the manifest growing without limit.

### StageAttempt

| Field | JSON | Notes |
|---|---|---|
| `Number` | `number` | 1-based, strictly sequential. |
| `Status` | `status` | `running`, `succeeded`, `failed`, `cancelled`. |
| `StartedAt` | `started_at` | Always set. |
| `FinishedAt` | `finished_at,omitempty` | Required for terminal attempts; never set while running. Must not precede `StartedAt`. |
| `Error` | `error,omitempty` | Required when `failed`; only valid on failed attempts. Redacted. |

### Retry semantics

Failing a stage does **not** terminate the run. A failed stage may be retried: starting the stage again appends a new attempt. The stage's current status is always the status of its **latest** attempt, so the manifest faithfully records the full history (`failed` attempt 1, `running` attempt 2, …).

Two retry shapes exist in v0.5, and they invalidate different things:

- **Resume retry (implicit).** Continuing a failed/interrupted run re-runs every stage whose latest attempt is `failed`, `cancelled`, or a dangling `running`; every `succeeded`/`skipped` stage is reused as-is. No completed stage is re-executed, so nothing downstream is invalidated — their inputs have not changed.
- **Explicit stage retry.** Re-running a stage the user selects (including a stage that already `succeeded`) makes its output stale, so the stage and **every stage that depends on it, transitively** (per the dependency graph) is reset to `pending` first — attempt history preserved, artifact references dropped. Stages that do not depend on the retried stage are reused. This is what stops a retried stage from silently reusing stale downstream output.

---

## 6. Artifacts, Warnings, and Errors

### ArtifactRef

```go
type ArtifactRef struct {
    Kind       ArtifactKind `json:"kind"`               // source, document, dossier, verification, editorial, factual_check, rendered, other
    ID         string       `json:"id"`                 // unique within the run
    Stage      StageName    `json:"stage"`              // stage that produced it
    Path       string       `json:"path,omitempty"`     // relative path, no traversal
    ProducedAt Timestamp    `json:"produced_at"`
}
```

Rules:

- IDs are unique per run.
- The referenced stage must be planned and must not be `pending` or `skipped` (it cannot have produced anything).
- Paths must be non-traversing. The manifest references artifacts by path — it never embeds content.
- **Content is sanitized at the persistence boundary.** Artifact *content* is not part of the manifest contract, but it is persisted by the workspace store, which sanitizes every artifact — including fetched `source` and `document` content — before writing. A source or document that contained a detected secret is persisted redacted, with a redaction metadata sidecar (`<path>.redaction.json` recording `redacted` and `redaction_count`) next to it. There is no verbatim exception. Readers of persisted content (after a restart or not) always observe the sanitized representation; exact-byte reconstruction of redacted content is deliberately impossible.

### Warning and ErrorSummary

Both carry `{ code, message, occurred_at }`:

- `code`: stable machine-readable identifier (e.g. `partial_discovery`, `fetch_error`, `run_failed`).
- `message`: human-readable, **redacted** — validated with `LooksLikeSecret` on every mutation and on `Validate()`.
- `occurred_at`: when it was recorded.

Run-level `Errors` record failures that stop the run; stage-level failures live on the failed attempt.

---

## 7. Configuration Snapshot

```go
type ConfigSnapshot struct {
    Values map[string]string `json:"values,omitempty"`
}

func (s *ConfigSnapshot) Set(key, value string) error
func (s *ConfigSnapshot) Get(key string) (string, bool)
func (s *ConfigSnapshot) Validate() error
```

- Keys: `^[a-zA-Z0-9][a-zA-Z0-9._-]*$` (dot/underscore/dash segments, e.g. `omlx_base_url`, `omlx.model`).
- `Set` redacts: a value under a secret-like key (see [Redaction]((#13-redaction))) is stored as `[REDACTED]`; any value containing a secret-like pattern is redacted regardless of its key. An empty value removes the key.
- `Validate()` rejects any value that still looks like a secret.

The snapshot captures the configuration **at run start** so a run can be explained without re-reading (mutable) config files.

---

## 8. Model and Endpoint Metadata

```go
type ModelMetadata struct {
    Provider string `json:"provider,omitempty"`
    Model    string `json:"model,omitempty"`
    Endpoint string `json:"endpoint,omitempty"`
}

func NewModelMetadata(provider, model, endpoint string) ModelMetadata
```

- `NewModelMetadata` redacts the endpoint (userinfo and `*_token` query parameters) — credentials are never recorded, only the redacted endpoint is.
- `Validate()` requires an `http`/`https` endpoint without embedded credentials; an endpoint containing the redaction placeholder passes (it was redacted at construction).
- Model names are recorded as-is (they are not secrets) so runs are explainable.

---

## 9. Lifecycle API

All mutators take `now time.Time` (normalized to UTC) and return an error on illegal operations. They mutate the in-memory manifest; `MarshalManifest` serializes it.

| Method | Effect |
|---|---|
| `Start(now)` | `pending → running`. |
| `Succeed(now)` | `running → succeeded`; requires every planned stage to be `succeeded` or `skipped`. |
| `Fail(now, code, message)` | `running → failed`; records a redacted run-level error, cancels in-flight stages as failed, and names the failing stage in `result`. |
| `Cancel(now, reason)` | `pending|running → cancelled`; in-flight stages become `cancelled`. |
| `StartStage(name, now)` | `pending → running` (first attempt) or `failed`/`cancelled` → `running` (retry or recovery of an interrupted attempt — new attempt number). The run must be active. |
| `SkipStage(name, now, reason)` | `pending → skipped` with the recorded reason. The run must be active. |
| `SucceedStage(name, now)` | latest attempt → `succeeded`. |
| `FailStage(name, now, code, message)` | latest attempt → `failed` with a redacted error; stage → `failed`; run-level error recorded. |
| `CancelStage(name, now)` | latest attempt → `cancelled`; in-flight or dangling attempt ends. |
| `Reopen(now)` | `succeeded`/`failed`/`cancelled` → `running` (the only path out of a terminal state). Clears the terminal result, records a `reopened` warning, preserves all stage records, attempts, artifacts, and logs. |
| `InvalidateStage(name, now)` | Out-of-band reset (not a lifecycle transition): `name` and every transitive downstream stage in the plan are pulled back to `pending` (succeeded/skipped stages reset, running stages interrupted first, failed/pending stages left as-is); their artifact references are removed from the run while attempt history is preserved. The run must be active (reopen a terminal run first). See [Stage Dependencies and Resumability](#10-stage-dependencies-and-resumability). |
| `AddArtifact(ref)` | Records an artifact (validated, redacted path checked). |
| `AddWarning(code, message, now)` | Appends a redacted warning (log bounded). |
| `AddError(code, message, now)` | Appends a redacted run-level error (log bounded). |

Guarantees:

- **No terminal overwrites.** Every mutator except `Reopen` rejects calls on a terminal run (`ErrManifestNotActive`). `Reopen` is the deliberate, auditable exception (it records a warning).
- **No time travel.** `updated_at` is monotonic; a `now` earlier than the current `updated_at` is rejected (`ErrTimestampRegression`).
- **Redaction at the door.** Every user-supplied string (skip reason, error message, warning, config value, endpoint) passes `RedactSecrets` before it is stored.
- **Bounded logs.** Attempt history and the run error/warning logs are trimmed to their bounds (see [Stage Records and Attempts](#5-stage-records-and-attempts)) on every mutation, so repeated retries cannot grow the manifest without limit.

---

## 10. Stage Dependencies and Resumability

### The explicit dependency graph

Stage dependencies are **data, not string ordering**. `dependencies.go` records, for every standard stage, the stages whose outputs it consumes:

| Stage | Depends on |
|---|---|
| `discovery` | — |
| `fetch` | `discovery` |
| `extract` | `fetch` |
| `research` | `extract` |
| `verify` | `research` |
| `follow_up` | `verify` |
| `write` | `verify`, `follow_up` |
| `final_check` | `write` |
| `render` | `final_check` |

API:

- `Dependencies(stage)` — the direct upstream stages, in canonical order.
- `DownstreamClosure(stage)` — the stage itself plus every stage that (transitively) depends on it, in canonical order. This is the **invalidation set** of a retry of `stage`.
- `ValidateStagePlanOrder(names)` — a plan is consistent iff it is a topological order of the graph (dependencies absent from the plan are satisfied by definition: a run may plan any subset, e.g. skipping `discovery` for explicit URLs). Enforced by `NewManifest` and `Manifest.Validate`, so the plan order and the graph can never silently disagree; the canonical order is one topological order.

### Persistence and layering

- `internal/workspace` — the filesystem store: run directories (`<root>/runs/<run-id>/`), the manifest file, and stage artifacts, all behind the sanitization boundary. `CreateRun` claims a run directory; `SaveManifest`/`LoadManifest` persist and reload the contract object; `SaveArtifact`/`LoadArtifact` persist stage outputs (content sanitized, redaction sidecars as documented in [Redaction](#13-redaction)).
- `internal/runner` — the execution engine. Given a manifest, a store, and per-stage executors (injected, one per planned stage, in plan order), it runs the plan, persisting the manifest after every lifecycle change so a crash leaves a coherent durable state. In **resume mode** (`Config.Resume`) it reuses the existing run directory, reuses every `succeeded`/`skipped` stage, takes over a dangling `running` attempt, and refuses terminal manifests (the resume layer reopens those first).
- `internal/resume` — the recovery policy on top of the two: `Analyze` (read-only assessment), `Resume` (continue an interrupted run), `RetryStage` (explicit re-run of a stage and its dependents). This package performs no LLM calls and knows nothing about concrete stage implementations; it is fully testable with fake executors and a temporary workspace.

### Resume: continue an interrupted run

Given a run ID, `Resume` loads the persisted manifest and verifies it before executing anything:

1. **Manifest integrity.** The manifest must parse and pass full validation; a torn or invalid document is refused (`ErrCorruptManifest`), as is an unknown run ID.
2. **Artifact integrity (fail closed).** Every artifact of every `succeeded` stage must exist and be readable in the run directory; a missing or unreadable artifact refuses the resume (`ErrMissingArtifact`), and a reference on a stage that did not complete, or a pathless reference, is an inconsistency (`ErrInconsistentRun`). Nothing is re-executed and nothing on disk is touched when verification fails.

It then prepares the manifest (persisted before the first stage runs, so a second crash loses no state):

- A terminal run is **reopened** (`Reopen`).
- Every dangling `running` stage (a process that died mid-stage) is interrupted as `cancelled` — a resume never strands an open attempt, and no persisted manifest ever claims a stage is running when it is not.

It executes every stage that is not `succeeded`/`skipped` (failed, cancelled, or never started), in plan order, and reuses the rest. A `succeeded` run resumes as a **no-op**: nothing executes, nothing on disk changes. A failed run whose plan is somehow already fully complete **self-heals**: reopened, with nothing left to do, it is completed in place.

When the run stops again, the manifest records the new state and the run can be resumed once more.

### Retry: explicit re-run of a stage

`RetryStage(run, stage)` works on a run in **any** terminal state (including a fully `succeeded` one):

1. The run is reopened (unless already active).
2. The target stage and its **downstream closure** (per the graph) are invalidated: reset to `pending`, skip reasons cleared, artifact references dropped, attempt history **preserved** (bounded). A dangling `running` stage in the closure is interrupted first.
3. The target and every dependent downstream stage execute; every stage not in the closure is reused without re-execution.

Consequences, all covered by the resume test suite:

- Retrying `verify` re-runs `verify`, `follow_up`, `write`, `final_check`, `render`; `research` is untouched.
- Retrying `write` re-runs exactly `write`, `final_check`, `render` — the ticket's stale-downstream case.
- Skipped stages in the closure are re-skipped (their recorded reason re-applied), never silently executed.

### Bounded history and preserved attempts

Previous attempt metadata is never destroyed: retrying a stage appends to its attempt history, and invalidation keeps it. The manifest stays bounded: per-stage attempt history is trimmed to the 32 most recent attempts (survivors renumbered 1..N), and run-level error/warning logs to the 64 newest entries. Repeated retries of a failing stage therefore converge to a constant-size manifest that always ends with the newest attempt.

---

## 11. State Transitions

### Run

```
pending ──Start──▶ running ──Succeed──▶ succeeded
                     │  └────Fail─────▶ failed
                     │  └───Cancel────▶ cancelled
pending ──Cancel──▶ cancelled
succeeded/failed/cancelled ──Reopen──▶ running   (the only path out of terminal)
```

### Stage

```
pending ──StartStage──▶ running ──SucceedStage──▶ succeeded
   │  └───────────────────┴──FailStage──────────▶ failed ──StartStage (retry)──▶ running
   │                                             └──CancelStage────────────────▶ cancelled ──StartStage (recovery)──▶ running
   └──SkipStage──▶ skipped
running ──CancelStage (on run cancel)──▶ cancelled
succeeded/skipped ──InvalidateStage──▶ pending   (out-of-band reset on upstream retry; not a lifecycle transition)
```

`failed` and `cancelled` stages retry through the normal lifecycle (`→ running`): a failed stage is retried by resuming its run, and a `cancelled` stage (an interrupted or deliberately stopped attempt) is recovered the same way. `succeeded` and `skipped` stages cannot start a new attempt at all; only `InvalidateStage` pulls them back to `pending`, and only as part of invalidating an upstream stage's downstream closure before a retry presents fresh output. The rules are explicit in `ValidRunTransition`, `ValidStageTransition`, and `ValidAttemptTransition` in `status.go`, with the full matrix covered by `status_test.go`.

---

## 12. Serialization and Versioning

- Wire format: JSON. `MarshalManifest` produces compact 2-space-indented JSON; `UnmarshalManifest` parses and runs the full `Validate()`.
- `Timestamp` serializes as RFC 3339 with up to nanosecond precision (trailing zeros trimmed; zero values marshal as `null`).
- `schema_version` is a semantic version string (`0.5.0`).
  - `UnmarshalManifest` and `Validate` accept only the current major (`0`); other majors yield `ErrUnsupportedSchemaVersion`.
  - Unknown *fields* within the current major are ignored (forward compatibility across 0.x patches); unknown *enum values* are rejected.
- Round trips are lossless: `unmarshal(marshal(m)) == m`, and re-marshaling is byte-stable (covered by `roundtrip_test.go`).

Future schema changes: bump the patch/minor and update the JSON Schema and this document; bump the major only when a documented consumer rule changes incompatibly.

---

## 13. Redaction

Redaction has two layers: the **pattern detectors** in `redact.go` and the **
exact-value sanitizer** in `sanitizer.go` (`Sanitizer` / `NewSanitizer`).
`redact.go` provides:

- `RedactedPlaceholder` = `[REDACTED]` — the only representation of a secret. The placeholder is idempotent: it never matches the detectors, so redacting twice is a no-op.
- `IsSecretKey(key)` — true for keys whose normalized form (separators as spaces, camelCase split) ends in a secret-like word: `key`, `token`, `secret`, `password`, `passwd`, `credential`, `apikey`, `auth`, `cookie`, or forms like `access_token` / `api_key` / `session_token`.
- `RedactSecrets(s)` replaces:
  - `Authorization: Bearer <token>` headers;
  - `Authorization: Basic <token>` values;
  - PEM private-key blocks (`-----BEGIN [RSA |EC |…]PRIVATE KEY-----` … `-----END … PRIVATE KEY-----`, whole block — public keys are untouched);
  - assignments to secret-like keys (`token = abc…`, `apiKey: "…"`);
  - URL userinfo (`https://user:pass@host`);
  - secret-like URL query parameters (`?api_key=…`);
  - long high-entropy token runs (≥ 32 chars, ≥ 3 character classes, Shannon entropy ≥ 4.5) — without redacting ordinary prose or hex IDs. The run class excludes `/` (a path separator): a filesystem path must not read as one token, while slash-bearing credentials in their realistic forms (Bearer, assignments, URL queries, PEM) are covered by the dedicated detectors.
- `RedactSecretsCounted(s)` — `RedactSecrets` plus the number of replacements, for redaction records. The count is stable: applying the function to its own output performs zero further replacements.
- `LooksLikeSecret(s)` — `RedactSecrets(s) != s`. Used to validate stored strings (topic, config values, notes, endpoints).
- `NewModelMetadata` redacts endpoints before storing them.

`sanitizer.go` provides the **Sanitizer**, the single redaction boundary
that data must cross before it is persisted:

- `NewSanitizer(values...)` — registers exact secret values known at runtime
  (the resolved `OMLX_API_KEY`, `SEARXNG_API_KEY`, `EMBEDDING_API_KEY`, via
  `config.Config.SecretValues`). A nil or zero-value `*Sanitizer` is the
  pattern-only default.
- `Sanitize(text)` / `SanitizeBytes(data)` — exact-value replacement (the
  only detection that applies to binary content) followed by the pattern
  detectors (text content only), returning sanitized output and a replacement
  count. The result never contains the original secret values, and redaction
  is idempotent.
- `CleanText` / `CleanBytes` — the final gate after sanitization: content
  that is not clean is a sanitization failure and must not be written
  (fail closed).

### The persistence boundary (workspace store)

The workspace store is the final security boundary for all persisted run
data, and it enforces the policy itself — callers never have to remember to
redact their own structures:

- **Every artifact** — manifests, search plans, discovery results, fetched
  and normalized source/document content, dossiers, verification results,
  editorial artifacts, final-check results, review bundles, rendered output,
  warnings/errors, attempt history, custom artifacts — is sanitized by the
  store's `run.Sanitizer` before any byte reaches disk (temporary files
  included). There is no verbatim exception for source or document content.
- When sanitization changes content, a sidecar file
  (`<path>.redaction.json`) records `redacted: true` and the replacement
  count — metadata only, never the redacted values. A clean write removes
  any stale sidecar, so a sidecar's presence always means "the persisted
  copy is a redacted representation".
- **Fail closed.** If the sanitized bytes still contain a detected secret,
  nothing is written: the store returns `workspace.ErrSanitization`, whose
  message names the failure without including any secret material.
- **Binary content** (not valid UTF-8, or containing NUL bytes) receives
  exact-value byte replacement only; the pattern detectors are defined for
  text, so binary blobs without a configured exact value pass through
  unchanged — the only content class that may cross the boundary unmodified.
- **The manifest** is validated in full (free-text fields are pattern-checked
  individually) and additionally gated byte-for-byte against every configured
  exact secret value; a manifest that still contains one is refused
  (`ErrManifestInvalid`) and never silently rewritten. The gate uses exact
  values rather than the pattern heuristics because patterns can
  false-positive on benign structured strings (filesystem paths, generated
  run IDs).
- **Verification/reload contract.** During an active run, in-memory originals
  remain available to the producing stage. Stages and tools that read
  persisted content (`LoadArtifact`, after a restart or not) always observe
  the sanitized representation. Exact-byte reconstruction of redacted content
  after a restart is deliberately impossible — the accepted trade-off of the
  no-secret persistence guarantee, in place of byte-identical raw source
  fidelity.

---

## 14. Validation Rules

`Manifest.Validate()` (and the nested `Validate`s) enforce, in summary:

1. Schema version is the current major; all enums are valid.
2. Run ID, workspace, topic, and source mode are well-formed.
3. Stage plan: valid canonical names, no duplicates.
4. Run↔stage consistency:
   - `pending` run ⇒ every stage `pending`.
   - `running` run ⇒ at least one stage `pending` or `running`, none `succeeded` required; in-flight stages have a `running` latest attempt.
   - `succeeded` run ⇒ every stage `succeeded` or `skipped`.
   - `failed` / `cancelled` run ⇒ no stage `running`.
5. Stage↔attempt consistency (see [Stage Records](#5-stage-records-and-attempts)), including attempt numbering and terminal times.
6. Result: `outcome` empty unless the run is terminal; `outcome` matches the run status; `failed` requires a reason; `failed_stage` only on `failed` runs and must actually be failed.
7. Artifacts: unique IDs; stage planned and not pending/skipped; non-traversing paths.
8. Warnings/errors: well-formed codes/times; messages contain no secret-like patterns.
9. Config values contain no secret-like values.
10. Timestamps: `started_at ≤ updated_at`; terminal runs have `finished_at ≥ updated_at`; no field is later than `updated_at`.

---

## 15. Schema File

`schemas/run-manifest.json` (draft-07) mirrors the structure above: required fields, enum values, ID patterns, and timestamp formats. It documents and sanity-checks the wire format; the Go `Validate()` remains the authoritative gate, since JSON Schema cannot express the cross-field consistency rules (run↔stage↔attempt, outcome↔status, timestamp ordering).
