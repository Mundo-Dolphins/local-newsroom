# First Usable Workflow (v0.5)

Version: v0.5 (first usable release)
Status: Operator guide
Related: [`contracts-v0.5.md`](contracts-v0.5.md) · [`tui-v0.5.md`](tui-v0.5.md) · [`review-bundle-v0.5.md`](review-bundle-v0.5.md) · [`examples/newsroom.example.yaml`](../examples/newsroom.example.yaml)

This is the operator's path from a fresh machine to a finished, reviewed
article — with the Mac Studio running oMLX, a Raspberry Pi running SearXNG,
and the `newsroom` CLI (or its TUI) doing the work. Every step below is what
you actually type; hostnames, ports, model names and paths are
**placeholders** — replace them with your own.

The complete chain one `newsroom run` drives:

```
config → create run → discovery/fetch → research → verify → write →
final check → render → persist artifacts → review bundle
                                     (resume/retry connects here)
```

---

## 1. Configure the Mac Studio (oMLX)

On the Mac Studio, serve your model through oMLX's OpenAI-compatible API and
note the endpoint. From any machine that can reach it, the config points at
the Studio's **hostname or LAN IP** — `localhost` only works on the Studio
itself.

```
http://mac-studio:11434/v1        # <your-mac-studio>:<omlx-port>/v1
model: llama3.1:70b               # whatever model oMLX is serving
```

## 2. Configure the Raspberry Pi (SearXNG)

Run a SearXNG instance on the Pi and expose its JSON API to your machine.

```
http://raspi-search:8080          # <your-pi>:<searxng-port>
```

## 3. Configure the workspace and archive

The config file is one YAML document, kept in one of the standard locations
(first existing file wins): `--config` flag, `$NEWSROOM_CONFIG`,
`./.newsroom.yaml` (or `.yml`), `$XDG_CONFIG_HOME/newsroom/config.yaml`
(or `~/.config/newsroom/config.yaml`), `~/.newsroom.yaml`.

```bash
cp examples/newsroom.example.yaml ~/.newsroom.yaml
$EDITOR ~/.newsroom.yaml
```

Edit the endpoints from steps 1–2 in:

```yaml
llm:
  base_url: http://mac-studio:11434/v1   # step 1
  model: llama3.1:70b
  api_key:
    env: OMLX_API_KEY                    # the key itself lives ONLY in your environment

search:
  base_url: http://raspi-search:8080     # step 2
  language: en
  max_sources: 20
```

**Secrets never go in the file.** Key fields reference the environment
variable that holds the credential; a literal key in the file is rejected by
`newsroom config validate`. Export the variables once in your shell profile:

```bash
export OMLX_API_KEY="<your-omlx-key>"
```

Workspace and archive layout:

- **Run workspace** — every run lives under
  `--workspace-root` (default `./.newsroom` in your current directory) as
  `runs/<run-id>/…`. Keep one workspace per project and always resume from
  the same root: `--workspace-root /data/newsroom`.
- **Archive** — the local SQLite archive is `archive.db` (config
  `archive.db_path`); use `newsroom archive` to store and search it.
- **Profiles** — config `profiles.root` (default `./profiles`) is the root
  of the editorial profile tree; see step 4.

Verify the whole chain resolves before your first run:

```bash
newsroom config validate
newsroom config show          # per-value source column: flag / env / file / default
```

## 4. Configure a profile

A profile is a directory of writing instructions. The loader merges, in
order: the built-in defaults, the profile's `common/` files, then the
author's files under `authors/<author>/`.

```bash
mkdir -p profiles/common profiles/authors/javi
```

```markdown
<!-- profiles/common/style.md -->
Write with a calm, factual voice. Keep paragraphs short.
```

```markdown
<!-- profiles/common/facts-policy.md -->
Only state facts that a source directly supports.
Do not add background details that are not verified.
```

```markdown
<!-- profiles/authors/javi/style.md -->
Prefer concrete numbers over abstractions.
```

`newsroom run --profile javi` loads `common/` plus `authors/javi/`;
`--profile md/javi` (two segments) switches the profile *root* to
`<profiles-root>/md` and then loads the same shape inside it. Every run
records the profile id and a content hash in its manifest, so you always
know which profile produced a given output.

## 5. Launch a run from the CLI

```bash
newsroom run \
  --topic "Miami Dolphins rookie draft pick" \
  --workspace-root /data/newsroom \
  --profile javi \
  --format article
```

What happens: the run is created and its manifest written to the workspace;
the pipeline then runs discovery (SearXNG via the planner), fetch, extract,
research, verify, write, final check, and render — each stage persisted as it
completes. You get a one-screen summary on stdout and the review bundle path
on stderr:

```
creating run run-20260722T093000Z-7f3c1ab9d2e4 in workspace "default" (...)
Run:         run-20260722T093000Z-7f3c1ab9d2e4
Status:      succeeded
Topic:       Miami Dolphins rookie draft pick [discovered]
Sources:     18 of 20 fetched; 18 documents extracted
Claims:      14 supported, 3 unresolved of 17 (quality 81)
Final check: pass
Output:      /data/newsroom/runs/run-…/output/article.md
Workspace:   /data/newsroom/runs/run-…
review bundle: /data/newsroom/runs/run-…/review.md
```

Useful variations:

```bash
# Known sources instead of discovery (skips SearXNG entirely)
newsroom run --topic "..." --url https://example.com/a --url https://example.com/b

# Bluesky thread output (each rendered post ≤ 300 chars)
newsroom run --topic "..." --format thread

# Point at a non-default config
newsroom --config /data/newsroom/project.yaml run --topic "..."
```

If a stage fails, the command exits non-zero, the run is recorded `failed`
with the failing stage marked, and the error points you at the resume command
(step 8).

## 6. Launch a run from the TUI

```bash
newsroom tui --workspace-root /data/newsroom
```

The TUI is the same pipeline with a keyboard interface (it holds no separate
logic — it drives the same `run`/`resume` services):

- **`n`** — new run: topic, sources/flags wizard, then start
- **`r`** — resume the selected run
- **`↵`** — run detail: live stage list with per-stage status, attempts,
  and error/summary lines
- **`c`** — cancel the in-flight run (the manifest records `cancelled`)
- **`q`** — quit

The header shows the resolved LLM and search endpoints, so you see before
you start which oMLX and SearXNG the run will hit. The TUI needs an
interactive terminal; for scripts and cron, use `newsroom run`.

## 7. Review the output

Open two files in the run directory:

```bash
open /data/newsroom/runs/run-…/review.md       # the review bundle
open /data/newsroom/runs/run-…/output/article.md
```

The **review bundle** is a deterministic, LLM-free projection of the run's
record — read it top to bottom:

- **Pipeline** — every stage, its status, attempts, and error/summary text
- **Sources** — what was discovered/fetched, and per-source errors
- **Research dossier** — the claims the sources produced, with confidence
- **Claims & verification** — each claim's verdict: supported /
  contradicted / unresolved, with notes and any contradictions
- **Final factual check** — what the checker flagged, and the overall
  status (`pass` / `fail` / `review`)
- **Output artifacts** — the rendered output and a ready-to-use verdict

Read the **final check verdict** first: `pass` means the output is ready to
use as rendered; `review` means it is defensible but carries flagged
statements; `fail` means do not use it as-is (fix the claims and resume —
step 8). Then skim the unresolved/contradicted claims — they tell you what
the story *cannot* currently claim.

To re-print the bundle for any run (it refreshes `review.md` deterministically):

```bash
newsroom review run-… --workspace-root /data/newsroom
```

## 8. Resume or retry a failed run

Everything a run did is on disk, so recovery is a command, not a
re-investigation:

```bash
newsroom run --resume run-20260722T093000Z-7f3c1ab9d2e4 --workspace-root /data/newsroom
```

Resume semantics:

- **Completed stages are reused** — their artifacts are read back and their
  LLM calls are not re-run.
- **Everything after the failure is re-executed** — the failed (or
  interrupted) stage gets a new attempt, recorded in the manifest's attempt
  history.
- **Ctrl+C mid-run** cancels the in-flight stage; the manifest ends
  `cancelled` and the same `--resume` command picks the run up from there.

Typical repair loop: a final-check `fail` lists the offending statements in
`review.md` (Final factual check) and in `final_check/result.json`. Fix the
underlying facts in your sources — or accept a narrower claim set — then
resume; only the final stages run again. The same works for a model
outage mid-research: resume once oMLX is back and the run continues from the
interrupted stage.

---

## Quick reference

| Task | Command |
| --- | --- |
| Check config | `newsroom config validate` / `newsroom config show` |
| New article | `newsroom run --topic "…" --format article --profile javi` |
| New thread | `newsroom run --topic "…" --format thread --profile javi` |
| From known URLs | `newsroom run --topic "…" --url https://… --url https://…` |
| TUI | `newsroom tui --workspace-root /data/newsroom` |
| Read results | `newsroom review <run-id>` (or open `review.md`) |
| Fail/interrupted | `newsroom run --resume <run-id>` |
| Archive a doc | `newsroom archive add --url https://…` |
| Search archive | `newsroom archive search "query"` |

Every setting resolves as **CLI flag > environment variable > config file >
built-in default**, and no credential is ever written to the workspace:
resolved keys are redacted from every persisted file, with a
`*.redaction.json` sidecar documenting any redaction that happened.
