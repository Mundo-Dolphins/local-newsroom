# Local Newsroom TUI — Controlled Publication (v0.6)

Version: v0.6 (TUI publication review and explicit approval)
Status: Implemented in `internal/tui` + `cmd/newsroom`
Updated: 2026-07-22

v0.6 adds a **publication review screen** to the TUI: a finished, READY run
can be reviewed in the terminal, and its publication can be approved and
published with explicit, deliberate confirmation. It reuses the v0.6
publication engine and orchestrator (`internal/publish`,
`internal/publication`) end to end — the TUI adds a presentation and a
confirmation flow, **no publication logic**.

Related documents:

- [`tui-v0.5.md`](tui-v0.5.md) — the base TUI (screens, keys, boundary)
- [`publication-orchestrator.md`](publication-orchestrator.md) — the engine
  the screen drives (plans, gates, approvals, cycles, summaries)
- [`contracts-v0.6.md`](contracts-v0.6.md) — the plan / approval / receipt
  contracts the screen renders

```
┌─────────────────────────────────────────────────────────────────┐
│  run detail  ──p──▶  publication review (v0.6)                  │
│                                                                 │
│   gate verdict · preset · final check                            │
│   per destination:  plan · baseline (path, size, digest)        │
│                       previews · approvals · progress            │
│                       receipts · remediation                     │
│                                                                 │
│   a  approve & publish   (confirmation modal, never direct)      │
│   r  resume (failed / interrupted)  c  cancel (in flight)        │
│              │                                                  │
│              ▼                                                  │
│   tuiApp : tui.PublicationService                               │
│     · PublicationState  (projection, prepare/preview opts)       │
│     · ApprovePublication / ResumePublication / CancelPublication │
│              │  (shared glue: publish_flow.go)                   │
│              ▼                                                  │
│   publish.Engine · publish.Orchestrator · editorial presets      │
└─────────────────────────────────────────────────────────────────┘
```

---

## 1. Entry

From the **run detail** of a terminal (succeeded / failed / cancelled) run,
`p` opens the publication review for that run:

| Key | Action |
|---|---|
| `p` | Open the publication review (terminal runs only; shown in the run view's key line) |

The run list itself never publishes: a run that is still executing cannot be
opened for publication at all, and there is no publish key on the list.

## 2. The review screen

The screen shows, top to bottom:

1. **Gate verdict** — ready, or the exact reason it is not (a failing final
   check, a missing preset, a broken destination). A not-ready run shows the
   verdict and stops there: no plans, no previews, no approval action.
2. **Preset** — the resolved editorial preset (name + hash), so the run's
   publication always shows *which* preset and *where* it points.
3. **Final check** — the v0.5 final-check status with its findings.
4. **One block per destination** (one plan per preset destination):
   - the plan: kind (new / reused / already published), status, destination,
   - the content baseline: canonical path, size, digest — the exact bytes
     the approval will bind to,
   - per-target previews (rehearsals): status, one line of details, issues,
   - approval state: approver, method, standing (granted / stale / revoked),
     and whether the approval still covers the current content,
   - progress while publishing: per-target attempts, then receipts
     (external URI + record id) on success,
   - remediation steps when the cycle ended unfinished.

### Keys

| Key | Action |
|---|---|
| `j` / `k` or `↓` / `↑` | Select a plan (wraps) |
| `a` | **Approve & publish** — opens the confirmation modal first |
| `r` | Resume — for failed / partially-succeeded / interrupted plans whose approval is still valid; confirmation modal first |
| `c` | Cancel the in-flight cycle (confirmation modal) — the cycle stops, the state is persisted and stays resumable |
| `v` | Toggle verbose (full preview/remediation text) |
| `esc` | Back to the run detail |
| `q` | Quit the TUI (asks first if a publication cycle is in flight) |
| `?` | Toggle the key reference overlay |
| `ctrl+l` | Redraw |

### Phases

| Phase | What you see |
|---|---|
| Review | The layout above; `a`/`r` disabled with a reason whenever the state does not allow them |
| Confirm | The confirmation modal names the exact action (approve & publish / resume / cancel) |
| Running | Per-target progress while the cycle executes; the screen polls the projection on the usual 2-second cadence |
| Done | Final state with receipts or remediation; `esc` back, `r`/`a` re-enabled when the state allows |

## 3. The safety model

The screen is shaped so that **nothing publishes by accident**:

- **No one-key publish.** `a` and `r` always open a confirmation modal that
  names the action; `y`/`n`/`esc` as everywhere else. There is no path from
  the run list, a keypress, or a render pass to a publication.
- **Ready runs only.** The readiness gate (final check + preset +
  destinations) is evaluated before any plan exists; a not-ready run gets
  nothing and cannot be approved.
- **The exact bytes.** The confirmation and the approval both bind to the
  plan's recorded artifact digest. Approving re-reads the content: if the
  canonical bytes changed after the preview, the approval is refused
  fail-closed (stale content is shown, never published).
- **Recorded as the TUI.** Approvals are persisted with
  `method: tui` (contract `contracts-v0.6.md`), the current user as the
  principal, and a note — the plan document, not the terminal, is the audit
  record.
- **Credentials never appear.** Every value crossing into the screen is
  display-safe: destinations, digests, receipts, and redacted errors only.
- **Interrupted is resumable.** Cancelling (or a crash) leaves the plan in
  `publishing` with an interrupt marker; `r` resumes exactly the remainder
  under the still-valid approval. Nothing published is ever re-published.
- **One cycle at a time.** A publication cycle and a run cycle are mutually
  exclusive in a session: a second intent while one is in flight is refused
  with a visible reason.

## 4. How it works (no duplicated logic)

The presentation layer (`internal/tui`) contains no publication business
logic, exactly like the rest of the v0.5 TUI:

| Piece | Location |
|---|---|
| `tui.PublicationService` interface + DTOs (`PublicationState`, `PublicationPlan`, `PublicationTarget`, `PublicationRemediation`, `PublicationOptions`) | `internal/tui/publication.go` |
| screen state machine (`pubPhase`, confirmations, polling, key semantics) | `internal/tui/model.go` |
| rendering | `internal/tui/view.go` |
| the concrete service: real engine/orchestrator/presets behind the interface | `cmd/newsroom/tui_publish.go` |
| shared glue used by **both** the CLI and the TUI: plan kinds, preset resolution, readiness, plan preparation, publisher binding, approval, the canonical cycle, exit semantics | `cmd/newsroom/publish_flow.go` |

The TUI's service implementation:

- **Projection** (`PublicationState`): resolves the preset (the run's
  recorded publication or the configured default — the TUI has no
  publication flags), builds one plan view per usable destination, and
  enriches the persisted plans with pure summaries and a live content
  freshness check (a plain read — the 2-second poll never mutates state).
  `PublicationOptions{Prepare, Preview}`: entering the screen prepares the
  plans and rehearses the previews; polling and done-reloads are read-only.
- **Approval** (`ApprovePublication`): re-rehearses each pending plan (the
  orchestrator's staleness gate), and only when every gate passes records
  the approvals (`method: tui`, bound to the plan's digest) and starts the
  cycle.
- **Resume** (`ResumePublication`): requires a still-valid approval on every
  unfinished plan; the engine republishes only what has no receipt.
- **Cancel** (`CancelPublication`): cancels the cycle's context; the engine
  persists the interrupt and the screen reflects the resumable state.

The CLI's `newsroom publish` and the TUI share the glue in
`publish_flow.go`, so the CLI's exit-code contract is unchanged and the two
surfaces can never drift in *how* a plan is prepared, approved, and
published — only in *how* the user confirms it (prompt vs. modal) and
renders the result (JSON vs. live screen).

## 5. Limitations

- **One preset per run** — the TUI publishes the preset's full destination
  list; destination selection is a CLI flag (`--target`), not a TUI control.
- **Polling cadence** — progress and receipts appear a few seconds after
  they happen (same 2-second cadence as the run views).
- **One session, one cycle** — publish and run cycles are mutually
  exclusive; the second is refused until the first finishes.
- **No content editing** — the TUI previews and publishes the canonical
  bytes; changing them is `newsroom publish` territory (a new baseline and
  a new approval).

## 6. Code map

| File | Contents |
|---|---|
| `internal/tui/publication.go` | `PublicationService` (embeds `Service`), `PublicationOptions`, the state/plan/target/remediation DTOs |
| `internal/tui/service.go` | `RunDetail` gains the publication summary fields (the run view's one-line status) |
| `internal/tui/model.go` | `screenPublish`, the phase machine, the five pub confirmation kinds, pub messages/commands, `handlePublishKey`, entry guard |
| `internal/tui/view.go` | `renderPublish` (gate, preset, final check, plan blocks, previews, approvals, progress, receipts, remediation) + the confirm-line budget |
| `internal/tui/publication_test.go` | state-machine tests with a fake `PublicationService`: entry, projection, approve/confirm, blocked approvals, progress, cancel, resume, resize, navigation |
| `cmd/newsroom/publish_flow.go` | the shared v0.6 glue: `preparePlanSet`, `bindPlanPublishers`, `planApproved`, `runPublicationCycle`, `cycleExitCode`, … (used by CLI + TUI) |
| `cmd/newsroom/publish_cmd.go` | `newsroom publish` — the CLI surface over the shared glue (exit-code contract unchanged) |
| `cmd/newsroom/tui_publish.go` | `tuiApp`'s `PublicationService` implementation: `publicationServices`, projection, approve/resume/cancel, the in-flight publication cycle |
| `cmd/newsroom/tui_publish_test.go` | app-level tests against the real engine with fake publishers: ready/not-ready/no-preset/stale projections, approval + idempotency, partial failure + resume, cancel + resume |

## 7. Testing

Two levels, no terminal required:

- **Presentation (`internal/tui`)** — `Model.Update` is driven with
  synthetic messages against a fake `PublicationService`; the suite covers
  entry rules, the confirmation flow (a neutral key never publishes),
  blocked actions, progress, cancellation, resumption, resize, and
  navigation.
- **App layer (`cmd/newsroom`)** — hermetic workspaces with the canonical
  stages, a real editorial preset, and fake publishers in place of
  git/Bluesky: the full projection matrix, the explicit approval bound to
  the exact digest with `method: tui`, idempotency, a transient failure
  with resume, and cancellation of a deliberately slow cycle.
