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
| `redact.go` | `RedactSecrets`, `LooksLikeSecret`, `IsSecretKey`, `RedactedPlaceholder` |

Related documents:

- [`contracts-v0.4.md`](contracts-v0.4.md) — per-stage data contracts (unchanged in v0.5)
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
10. [State Transitions](#10-state-transitions)
11. [Serialization and Versioning](#11-serialization-and-versioning)
12. [Redaction](#12-redaction)
13. [Validation Rules](#13-validation-rules)
14. [Schema File](#14-schema-file)

---

## 1. Design Goals

1. **Reproducible.** The manifest records everything needed to explain a run: workspace, topic, source mode, profile, model/endpoint, redacted config, stage plan, attempts, artifacts, and timing.
2. **Inspectable.** One JSON document, one run. Stable IDs and deterministic stage names make runs queryable and diffable.
3. **Resumable (later).** The manifest is the source of truth for run state. v0.5 ships the contract and in-memory lifecycle only; filesystem persistence and resume logic build on it in v0.6.
4. **Honest.** Error messages, warnings, and config values are redacted; credentials never appear in the manifest.
5. **Not every run runs everything.** A run plans an ordered subset of the canonical stages; the plan is part of the manifest.
6. **Versioned.** `schema_version` is a semantic version string. Consumers reject unknown major versions and ignore unknown fields within a major.

Out of scope for v0.5: loading configuration files, the TUI, filesystem storage, and resume logic.

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

Valid run transitions:

| From \ To | pending | running | succeeded | failed | cancelled |
|---|---|---|---|---|---|
| pending | — | ✓ | ✗ | ✗ | ✓ |
| running | ✗ | ✗ | ✓ | ✓ | ✓ |
| succeeded / failed / cancelled | ✗ | ✗ | ✗ | ✗ | ✗ |

A terminal run is immutable: every mutator rejects changes once the run is terminal.

### Stage status

| Status | Meaning |
|---|---|
| `pending` | planned, not started |
| `skipped` | explicitly skipped with a recorded reason |
| `running` | an attempt is in flight |
| `succeeded` | completed successfully |
| `failed` | the latest attempt failed (retryable) |
| `cancelled` | stopped while running |

`succeeded`, `failed`, `skipped`, and `cancelled` are terminal for the stage. `skipped` is terminal and **distinct** from `pending`: a skipped stage was a deliberate decision, a pending stage simply has not been reached.

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
- The plan is preserved verbatim in the manifest — a run that skips `discovery` still *plans* it (as a `skipped` record) or omits it entirely; consumers must never assume every run executes every stage.

### StageRecord

| Field | JSON | Notes |
|---|---|---|
| `Name` | `name` | Canonical stage name. |
| `Status` | `status` | See [Status Model](#3-status-model). |
| `SkipReason` | `skip_reason,omitempty` | Required when `skipped`; must be empty otherwise. |
| `Attempts` | `attempts,omitempty` | Chronological history, numbered 1..N. Empty for `pending` and `skipped`. |

Consistency: the stage status must agree with the attempt history — a `pending` or `skipped` stage has no attempts; a non-terminal stage has its latest attempt `running`; a terminal stage's status equals its latest attempt's status (except `skipped`).

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
- `Set` redacts: a value under a secret-like key (see [Redaction](#12-redaction)) is stored as `[REDACTED]`; any value containing a secret-like pattern is redacted regardless of its key. An empty value removes the key.
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
| `StartStage(name, now)` | `pending → running` (first attempt) or `failed → running` (retry, new attempt number). The run must be `running`. |
| `SkipStage(name, now, reason)` | `pending → skipped` with the recorded reason. The run must be `running`. |
| `SucceedStage(name, now)` | latest attempt → `succeeded`. |
| `FailStage(name, now, code, message)` | latest attempt → `failed` with a redacted error; stage → `failed`; run-level error recorded. |
| `CancelStage(name, now)` | latest attempt → `cancelled`. |
| `AddArtifact(ref)` | Records an artifact (validated, redacted path checked). |
| `AddWarning(code, message, now)` | Appends a redacted warning. |
| `AddError(code, message, now)` | Appends a redacted run-level error. |

Guarantees:

- **No terminal overwrites.** Every mutator rejects calls on a terminal run (`ErrManifestNotActive`).
- **No time travel.** `updated_at` is monotonic; a `now` earlier than the current `updated_at` is rejected (`ErrTimestampRegression`).
- **Redaction at the door.** Every user-supplied string (skip reason, error message, warning, config value, endpoint) passes `RedactSecrets` before it is stored.

---

## 10. State Transitions

### Run

```
pending ──Start──▶ running ──Succeed──▶ succeeded
                     │  └────Fail─────▶ failed
                     │  └───Cancel────▶ cancelled
pending ──Cancel──▶ cancelled
```

### Stage

```
pending ──StartStage──▶ running ──SucceedStage──▶ succeeded
   │  └───────────────────┴──FailStage──────────▶ failed ──StartStage (retry)──▶ running
   │                                             └──CancelStage────────────────▶ cancelled
   └──SkipStage──▶ skipped
running ──CancelStage (on run cancel)──▶ cancelled
```

`succeeded`, `skipped`, and `cancelled` stages are terminal and cannot be retried; only `failed` stages retry. The rules are explicit in `ValidRunTransition`, `ValidStageTransition`, and `ValidAttemptTransition` in `status.go`, with the full matrix covered by `status_test.go`.

---

## 11. Serialization and Versioning

- Wire format: JSON. `MarshalManifest` produces compact 2-space-indented JSON; `UnmarshalManifest` parses and runs the full `Validate()`.
- `Timestamp` serializes as RFC 3339 with up to nanosecond precision (trailing zeros trimmed; zero values marshal as `null`).
- `schema_version` is a semantic version string (`0.5.0`).
  - `UnmarshalManifest` and `Validate` accept only the current major (`0`); other majors yield `ErrUnsupportedSchemaVersion`.
  - Unknown *fields* within the current major are ignored (forward compatibility across 0.x patches); unknown *enum values* are rejected.
- Round trips are lossless: `unmarshal(marshal(m)) == m`, and re-marshaling is byte-stable (covered by `roundtrip_test.go`).

Future schema changes: bump the patch/minor and update the JSON Schema and this document; bump the major only when a documented consumer rule changes incompatibly.

---

## 12. Redaction

The manifest must be shareable. `redact.go` provides:

- `RedactedPlaceholder` = `[REDACTED]` — the only representation of a secret. The placeholder is idempotent: it never matches the detectors, so redacting twice is a no-op.
- `IsSecretKey(key)` — true for keys whose normalized form (separators as spaces, camelCase split) ends in a secret-like word: `key`, `token`, `secret`, `password`, `passwd`, `credential`, `apikey`, `auth`, `cookie`, or forms like `access_token` / `api_key` / `session_token`.
- `RedactSecrets(s)` replaces:
  - `Authorization: Bearer <token>` headers;
  - assignments to secret-like keys (`token = abc…`, `apiKey: "…"`);
  - URL userinfo (`https://user:pass@host`);
  - secret-like URL query parameters (`?api_key=…`);
  - long high-entropy token runs (≥ 32 chars, ≥ 3 character classes, Shannon entropy ≥ 4.5) — without redacting ordinary prose or hex IDs.
- `LooksLikeSecret(s)` — `RedactSecrets(s) != s`. Used to validate stored strings.
- `NewModelMetadata` redacts endpoints before storing them.

---

## 13. Validation Rules

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

## 14. Schema File

`schemas/run-manifest.json` (draft-07) mirrors the structure above: required fields, enum values, ID patterns, and timestamp formats. It documents and sanity-checks the wire format; the Go `Validate()` remains the authoritative gate, since JSON Schema cannot express the cross-field consistency rules (run↔stage↔attempt, outcome↔status, timestamp ordering).
