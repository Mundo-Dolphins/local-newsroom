# Local Newsroom TUI (v0.5)

Version: v0.5 (Keyboard-first terminal interface)
Status: Implemented in `internal/tui` + `cmd/newsroom`
Updated: 2026-07-22

The TUI is a lightweight, keyboard-first terminal interface for running and
watching newsroom pipelines. It is a **presentation layer only**: every
pipeline decision (stage order, source discovery, profile loading, retry
semantics) lives in the existing `run`/`orchestrator`/`resume` services. The
TUI polls those services and renders the results; it contains no pipeline
business logic.

```
┌─────────────────────────────────────────────────────────────────┐
│  newsroom tui                                                   │
│                                                                 │
│   cmd/newsroom (tuiCmd, tuiApp)          internal/tui           │
│   ┌──────────────────────────┐           ┌───────────────────┐  │
│   │ tuiApp : tui.Service     │──renders──▶│ Model (state)     │  │
│   │  · Info/ListRuns/Detail  │◀──keys────│   list/run/wizard │  │
│   │  · StartNewRun/Resume    │           │ View (lipgloss)   │  │
│   │  · RetryStage/Cancel     │           └───────────────────┘  │
│   └──────────┬───────────────┘                                  │
│              │ planRun (shared wiring with `newsroom run`)      │
│              ▼                                                   │
│   orchestrator · run · resume · workspace · profiles · llm       │
└─────────────────────────────────────────────────────────────────┘
```

Related documents:

- [`contracts-v0.5.md`](contracts-v0.5.md) — run manifest contract (what the
  TUI renders)
- [`v04-editorial-pipeline.md`](v04-editorial-pipeline.md) — pipeline and
  stages

---

## 1. Quick start

```bash
newsroom tui                 # recent runs for the default workspace
newsroom tui --workspace myproject
newsroom tui --workspace-root /path/to/.newsroom
```

The TUI requires an interactive terminal. Piped or redirected invocations
fail fast with a pointer to `newsroom run` (the non-interactive equivalent),
so CI scripts and cron jobs are unaffected.

On startup the TUI reads the same configuration chain as every other command
(`--config` > environment > config file > defaults) and shows the resolved
LLM and search endpoints in the header — so you always see which oMLX and
SearXNG instances a run will hit.

## 2. Screens

### 2.1 Recent runs (default screen)

One row per run, newest first: status badge, topic, source-mode tag, warning
and error counts, artifact count, and age. `enter` opens the run detail; `r`
resumes a resumable (failed/cancelled/interrupted) run.

| Key | Action |
|---|---|
| `j` / `k` or `↓` / `↑` | Select a run |
| `enter` | Open the selected run |
| `n` | New run (wizard) |
| `r` | Resume the selected run |
| `q`, `esc`, `ctrl+c` | Quit (asks first if a run is in progress) |
| `?` | Toggle the key reference overlay |
| `ctrl+l` | Redraw |

### 2.2 New run wizard

Fields: **topic** (text) → **profile** (selector) → **format** (article /
thread / hybrid) → **source mode** (auto / explicit URLs / supplement) →
**URLs** (text, one per line or space-separated) → **follow-up** (toggle).

- Selectors are prefilled from the configuration (`defaults.output_format`,
  `profiles.name`) and the discovered profile tree.
- Search-dependent modes (auto, supplement) are disabled in the selector when
  no search endpoint is configured — with a hint instead of a start-time
  error.
- `enter` advances to the next field; on the last field it validates and
  starts the run. Validation errors appear inline (e.g. *explicit mode needs
  at least one URL*).
- Text fields support `left`/`right`/`home`/`end`/`backspace`/`delete` and
  paste.

| Key | Action |
|---|---|
| `tab` / `shift+tab` or `↓` / `↑` | Next / previous field |
| `enter` | Advance; on the last field, start the run |
| typing | Edit the focused field |
| `left`/`right` or `h`/`l` | Cycle selector options |
| `y` / `n` / `space` | Toggle follow-up |
| `esc` | Back to the recent-runs list |
| `ctrl+c` | Quit (asks first if a run is in progress) |

### 2.3 Run detail

The run's nine canonical stages with status, duration, and (when verbose)
error/warning detail; the run-level warnings and errors; the artifact list;
and, for finished runs, the pipeline summary (sources fetched, documents,
claims, quality score, final-check verdict).

| Key | Action |
|---|---|
| `j` / `k` or `↓` / `↑` | Select a stage |
| `t` | Retry the selected stage (invalidates stale downstream stages; the run resumes from it) |
| `r` | Resume the run (failed / cancelled / interrupted runs) |
| `c` | Cancel the run (asks for confirmation; the run is recorded as cancelled and stays resumable) |
| `o` | Open an artifact in `$EDITOR` (cycles through the list) |
| `v` | Toggle verbose (full error/warning text instead of one-line summaries) |
| `q`, `esc`, `ctrl+c` | Quit (asks first if the run is still executing) |
| `?` | Toggle the key reference overlay |

Confirmation prompts (`y`/`n`) are modal: `y` confirms, `n`/`esc` dismisses.

## 3. How it works

### 3.1 Presentation boundary

`internal/tui` depends only on:

| Dependency | Role |
|---|---|
| the `tui.Service` interface | all data and actions the UI needs |
| bubbletea | event loop, terminal, alt screen |
| lipgloss | rendering |

The concrete implementation of `tui.Service` is `tuiApp` in
`cmd/newsroom/tui_cmd.go`. It is the only place that knows about the real
`run`/`orchestrator`/`workspace`/`profiles` types; everything it returns is a
plain DTO (`Info`, `RunInfo`, `RunDetail`, …) with no imported pipeline
types. This keeps `internal/tui` unit-testable with a fake service and an
`os/exec`-free fake opener, and keeps the pipeline test suite independent of
the UI.

`cmd/newsroom/run_plan.go` hosts `planRun` — the single pipeline-assembly
point shared by `newsroom run` (CLI) and `tuiApp` (TUI): intent → config
resolution → profile load → store → orchestrator. Adding a new pipeline
wiring concern touches `planRun` once, not twice.

### 3.2 One run at a time

The TUI enforces a single in-flight cycle per session (`tuiApp.beginCycle`):
starting, resuming, or retrying while a cycle is active returns an error the
UI shows inline. The run executes in a goroutine with its own cancellable
context; the UI polls `RunDetail` on a fixed interval (a few seconds) while
the run is not terminal — polling is deliberately simple and matches the
manifest's role as the single source of truth, with no WebSocket or push
channel.

### 3.3 Cancellation and graceful exit

- `c` (cancel) asks for confirmation, then cancels the run's context. The
  orchestrator records the run **cancelled** in the manifest (never "lost");
  the run stays resumable, and the TUI keeps polling until the manifest flips
  to a terminal state.
- `q` while a run is executing asks first. After you confirm, the process
  cancels the in-flight context and waits (bounded grace period) for the
  runner to persist the terminal state before exiting, so the manifest is
  always consistent on disk.
- Quitting while nothing runs is instant.

### 3.4 Artifacts

`o` opens artifacts through a pluggable opener: `$EDITOR` by default
(`$VISUAL` override honored), run in a `tea.ExecProcess` so the TUI suspends
raw mode while the editor is open. Paths are absolute run-directory paths;
the TUI never renders file *contents* (artifacts stay outside the terminal
view; raw LLM output is never shown either).

## 4. Remote usage

The TUI makes no network calls of its own. It talks to the workspace on local
disk and — through the pipeline exactly like `newsroom run` — to the
configured endpoints:

| Endpoint | Default | Purpose |
|---|---|---|
| oMLX (`llm.base_url`) | — | planner / researcher / verifier / writer / final-check LLM calls |
| SearXNG (`search.base_url`) | — | discovery and follow-up search |

Running the TUI on a MacBook while the services live elsewhere works by
pointing the shared config (or environment) at the remote endpoints — e.g.
tunnel the oMLX port (`ssh -L 8080:omlx-host:8080` or a tailscale IP) and set
`llm.base_url` / `SEARXNG_BASE_URL` accordingly. The header shows the
resolved endpoints, so a misconfigured remote target is visible before you
start anything. Secrets (API keys) are never displayed: the header shows
configured/not-configured plus the non-secret URL and model name.

## 5. Flags

| Flag | Default | Description |
|---|---|---|
| `--workspace` | `default` | Workspace name whose runs are shown |
| `--workspace-root` | `.newsroom` | Workspace root directory |
| `--config` | (lookup) | Persistent: explicit config file path |

The TUI has deliberately **no** `--llm-*`, `--search-*`, or `--profile`
flags: a run started from the TUI resolves everything from the configuration
chain, identical to `newsroom run` without flag overrides. The wizard replaces
per-run flags with the profile selector, format, source mode, and URLs.

## 6. Relationship to `newsroom run`

| | `newsroom run` | `newsroom tui` |
|---|---|---|
| Mode | one shot, non-interactive | interactive session |
| Output | summary to stdout | live stage view |
| Per-run overrides | full flag set | configuration chain only |
| Resume / retry | `--resume` / `--retry-stage` flags | `r` / `t` keys |
| Live progress | none (run to completion) | polling while in flight |
| Use case | scripts, CI, automation | daily desk work |

Both entry points share `planRun`, so pipeline behavior — stage execution,
warnings, artifacts, manifest contents — is bit-identical for the same
inputs.

## 7. Limitations

- **Polling cadence:** stage transitions appear a few seconds after they
  happen, not instantly.
- **No concurrent runs in one session:** the second start/resume/retry while
  a cycle is active is refused by design.
- **One workspace per session:** switch with a new `--workspace` invocation.
- **Editor required for artifacts:** `$EDITOR`/`$VISUAL` must point at a
  real interactive editor.
- **No in-terminal file preview:** the TUI opens artifacts in the editor
  rather than rendering them, keeping the terminal view stable and the
  pipeline's raw output out of the default view.

## 8. Code map

| File | Contents |
|---|---|
| `internal/tui/doc.go` | package overview and the presentation-layer boundary |
| `internal/tui/service.go` | `Service` interface + DTOs (`Info`, `RunInfo`, `RunDetail`, `NewRunIntent`, `ArtifactInfo`, source-mode labels) |
| `internal/tui/model.go` | state machine: screens, wizard, confirmations, key handling, polling and animation scheduling |
| `internal/tui/view.go` | lipgloss renderers + display-width-safe text helpers (`truncate`, `wrapWidth`, `clipLines`, …) |
| `internal/tui/model_test.go` | state/update tests with a fake `Service` and fake opener (no terminal) |
| `internal/tui/view_test.go` | rendering and text-helper tests |
| `cmd/newsroom/tui_cmd.go` | `tui` cobra command, `tuiApp` (concrete `Service`), TTY guard, tea program wiring, graceful shutdown |
| `cmd/newsroom/tui_cmd_test.go` | TTY guard, DTO mapping, service-method tests (including a full fake-pipeline run) |
| `cmd/newsroom/run_plan.go` | shared `planRun` wiring used by both `run` and `tui` |

## 9. Testing

The TUI's state machine is exercised without a terminal: tests drive
`Model.Update` directly with synthetic `tea.Msg`s against a fake `Service`
and a fake opener, asserting on the returned model and the service calls.
The command layer is tested through the real cobra command (TTY guard, DTO
mapping against a real workspace store, service error paths, and one full
run through the orchestrator against in-test HTTP fakes).
