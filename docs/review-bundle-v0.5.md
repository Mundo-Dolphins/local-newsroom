# Human Review Bundle (v0.5)

Version: v0.5
Status: Implemented in `internal/review` + `cmd/newsroom`
Updated: 2026-07-11

For every run that reaches a terminal state — succeeded, failed, or
cancelled — local-newsroom produces a single human-readable document,
`review.md`, inside the run directory: the **review bundle**. It is the
document a person reads to answer the questions the machine summary cannot:

- What did this run work on, and what did it produce?
- What is the state of every claim (supported / contradicted / unresolved)?
- What did the final factual check conclude, and what does it flag?
- Is the output **ready to use — and under what conditions**?

The bundle is a **deterministic projection of the run's persisted record**.
It performs no LLM calls, re-derives nothing, and never invents facts: every
number and quote in the document came from the run's manifest or a stage
artifact the run wrote. Re-running it always yields the same bytes.

---

## 1. Where it appears

```
$ newsroom run --topic "Miami Dolphins rookie" --url ... --format article
creating run run-20260711T100000Z-... in workspace "default" (...)
<run summary to stdout>
review bundle: /path/to/.newsroom/runs/run-20260711T100000Z-.../review.md   # stderr
```

Two surfaces, one document:

1. **Attached automatically** after `newsroom run` (and `--resume`) whenever
   the run ends. A failure to build or write the bundle is a stderr warning —
   it never changes the run's outcome, because the run's own record is the
   source of truth and the bundle is a convenience view over it.
2. **`newsroom review <run-id>`** prints the bundle to stdout and refreshes
   the on-disk `review.md` (`--no-save` prints without writing). It works for
   any run in the workspace — terminal or still in flight (a running run
   renders as a partial review).

```bash
newsroom review run-20260711T100000Z-abcdef01
newsroom review run-20260711T100000Z-abcdef01 --workspace-root .newsroom
newsroom review run-20260711T100000Z-abcdef01 --no-save
```

`review.md` is a **run-directory file, not a manifest artifact**: it is never
referenced in the manifest's `artifacts` array. A failed or cancelled run
cannot carry new artifact references (stages that did not succeed produce no
artifacts), yet exactly those runs are when a human most needs the document.
The file lives in the run directory, is listed in every review's "Evidence
files" section, and disappears with the run directory.

## 2. Document layout

```
# Run Review: <topic>

> **Status: SUCCEEDED** — the final factual check passed; the output below is
> ready for local use.                       ← the verdict line (see §4)

| | |
|---|---|
| **Run ID** | `run-...` |
| **Workspace** | default |
| **Source mode** | explicit |
| **Duration** / **Finished** | ... |
| **Profile** | name (hash …) — path |
| **Model** | provider / model @ `endpoint` |
| **Output format** | article |
| **Quality threshold** | 60 |
| **Run directory** | `/abs/path` |

## Unresolved issues          ← ALWAYS first, always prominent
   severity-ranked table (critical / high / medium), then:
   - contradicted claims, one per line with its recorded reason
   - unresolved claims (uncertain / insufficient evidence)
   - run warnings
   - bundle integrity notes (see §5)

## Pipeline                    stage table: status, attempts, skip/error notes
## Sources                     mode, candidates, queries, per-URL fetch outcomes
## Research dossier            claim count + the sources with titles and links
## Claims & verification       per-status counts, quality score, verifier summary
## Follow-up research          rounds, queries, sources, resolved / still open
## Final factual check         overall status, confidence, findings, raw
                               categories, recommendations
## Editorial artifact          title, type, claim references, warnings
## Output artifacts            the readiness statement + rendered files
                               with on-disk presence
## Evidence files              every canonical file, with on-disk presence
## Run errors                  the run's error log (when non-empty)

*Generated deterministically by local-newsroom v0.5 (no LLM calls).
Regenerate with `newsroom review <run-id>`.*
```

Stages the run never reached (failed/cancelled runs) simply omit their
sections; the pipeline table still shows where the run stopped and why.

## 3. What the bundle reads

| Section | Source (run-relative path) |
|---|---|
| identity, profile, model, config, stages, logs | `manifest.json` |
| Sources | `sources/index.json` |
| fetch outcomes | `fetch/index.json` |
| documents | `documents/index.json` |
| Research dossier | `research/dossier.json` |
| Claims & verification | `verify/verification.json` |
| Follow-up | `follow_up/result.json` (or the stage's skip reason) |
| Final factual check | `final_check/result.json` |
| Editorial artifact | `write/artifact.json` |
| Output files | manifest references of kind `rendered` + on-disk presence |

The paths come from the canonical constants in `internal/run`
(`ArtifactPathSourcesIndex`, …, `ArtifactPathFinalCheck`), shared with the
orchestrator that writes them, so producer and consumer cannot drift. A
missing file is *expected* after a failed stage and is never an error: it
just means "not reached".

## 4. The readiness rule

The output section states exactly one verdict:

| Run status | Final check | Verdict |
|---|---|---|
| succeeded | `pass` (and rendered files present) | **READY** — use locally; nothing was published |
| succeeded | `fail` / `review` / missing | **NOT READY** — with the reason |
| failed / cancelled / not finished | any | **NOT READY** — with the reason |

The rule is deliberately one-way: a run is described as ready **only** when
it succeeded *and* its final factual check passed. A succeeded run whose
check file was deleted after the fact, a run whose check result is missing,
or a failed run all render NOT READY. This is the v0.5 invariant that a run
which failed its final check must never be presented as finished product.

## 5. Integrity notes

If a stage is recorded as **succeeded** but its canonical file is missing
from the run directory, the bundle records a prominent **bundle integrity
note** (a `medium` issue row plus a dedicated subsection) instead of failing:
the workspace may have been pruned or partially copied, and the reviewer
must know the evidence is gone. Similarly, a stage file that exists but does
not parse produces a note naming the file.

## 6. Determinism and security

- **Deterministic rendering.** No timestamps of the *generation*, no map
  iteration order in the output (claim listings are sorted by ID, sources by
  stable ID), no randomness. `Render()` is a pure function of the bundle.
- **No secrets.** Every free-text value the document prints passes
  `run.RedactSecrets` (idempotent: the placeholder never matches the
  detectors), and the file is written through the workspace store's
  sanitization boundary — the same single boundary every other run file
  crosses (exact configured values + pattern detectors, fail closed,
  redaction sidecar). Content that already entered through that boundary is
  therefore redacted at most once; the render-time pass is a backstop.
- **No LLM.** The bundle reads JSON files and formats them. It cannot call a
  model, a search provider, or the network.

## 7. Relationship to the run summary

`newsroom run` prints a compact machine-oriented summary to stdout
(`orchestrator.BuildSummary`: counts, timings, artifact paths). The review
bundle complements it: the summary says *what happened*; the bundle says
*what it means for a human* — the claims that worry, the findings, and the
verdict. The two are independent: the summary goes to stdout at run end,
the bundle goes to the filesystem.

## 8. Implementation map

| Piece | Location |
|---|---|
| bundle model + `Build` (store reader) | `internal/review/review.go` |
| `Render` (Markdown) + readiness rule | `internal/review/render.go` |
| `Save` (run-directory persistence) | `internal/review/save.go` |
| canonical artifact paths | `internal/run/stage_paths.go` |
| auto-attach after `run` / `--resume` | `cmd/newsroom/run_cmd.go` (`writeReviewBundle`) |
| `newsroom review` | `cmd/newsroom/review_cmd.go` |
| tests | `internal/review/*_test.go`, `cmd/newsroom/review_cmd_test.go` |

The `review` package imports the `run` contract, the stage contracts
(`contracts`, `researcher`, `followup`, `planner`, `discovery`) and the
`runner.Store` interface only — never the orchestrator — so the bundle is
also the natural future reader for archive or tooling consumers.

## 9. Tests

- **Unit** (`internal/review`): full-pipeline fixtures on the real workspace
  store — a succeeded run, a run that failed at the final check, a run that
  failed at research, a cancelled run; integrity notes for missing or
  corrupted stage files; `Save` (including a store configured with an exact
  secret, asserting the file on disk is clean); the readiness matrix;
  escaping/truncation/secret-redaction; byte-identical re-rendering.
- **CLI** (`cmd/newsroom`): end-to-end runs (fakes for LLM/search only)
  asserting the run attaches a `review.md` that is never manifest-referenced,
  that a failed check's bundle renders NOT READY with the finding, and that
  `newsroom review` prints the identical document.

---

Related documents:

- [`contracts-v0.5.md`](contracts-v0.5.md) — the run manifest contract
  (including the redaction boundary the bundle crosses on the way out)
- [`v04-editorial-pipeline.md`](v04-editorial-pipeline.md) — the stages the
  bundle summarizes
- [`tui-v0.5.md`](tui-v0.5.md) — the TUI, which renders the same manifest
