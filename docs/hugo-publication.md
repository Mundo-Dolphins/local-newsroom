# Hugo Publication Package (v0.6)

`internal/hugo` is the generic Hugo / static-site publication adapter. It turns
one **READY rendered article** (the Markdown artifact a run already produced
and approved) into a **site-ready content file** inside a Hugo project:
deterministic path, safe front matter, and staged bytes — with no Git push,
no network call, and no LLM or Writer re-invocation. Staging only: the
package never deploys.

It closes [Mundo-Dolphins/local-newsroom#116](https://github.com/Mundo-Dolphins/local-newsroom/issues/116).

## What It Is / What It Is Not

| Is | Is not |
|---|---|
| A concrete *staging* adapter: rendered article → `content/<section>/<file>.md` inside a Hugo tree | A publisher in the v0.6 plan-contract sense: it does not consume `AuthorizesPublish` and does not produce receipts (that wiring is a later v0.6 component) |
| Plan-agnostic: it imports only the standard library, `gopkg.in/yaml.v3`, and `internal/run` (secret detection) | A contract dependency: it does not import `internal/publication` or `internal/contracts` |
| Deterministic and idempotent: same input → same bytes, same digest, same path | A renderer or a writer: the body arrives already rendered |
| A filesystem boundary with containment, symlink, overwrite, and atomicity guarantees | A deployer: no Git, no HTTP, no remote of any kind |

Mundo Dolphins–specific conventions (section names, date styles, tag vocabularies) live in the
[editorial bundle's publication presets](../README.md), not in this engine.

## The Staged File

```
content/news/20260711-los-dolphins-ganan-el-super-bowl-increíble.md
```

```markdown
---
title: "Los Dolphins ganan el Super Bowl — ¡Increíble!"
date: "2026-07-11"
author: "Javi T."
description: "Una noche para el recuerdo en Miami."
canonical: "https://mundodolphins.es/news/20260711-los-dolphins-ganan-el-super-bowl-increíble.md"
source: "https://www.example.com/2026/07/11/super-bowl"
tags:
  - dolphins
  - super-bowl
draft: true
---

# Los Dolphins ganan el Super Bowl — ¡Increíble!

El equipo de Miami celebró una noche histórica.
```

Front matter is YAML with a canonical field order — `title`, `date`, `author`,
`description`, `canonical`, `source`, `tags`, `categories`, `draft`, optional
`newsroom` provenance block, then config `Defaults` (sorted) — followed by a
blank line and the rendered body **verbatim** (a trailing newline is
normalized; the body is trusted to have crossed the run's sanitization
boundary).

## Configuration Surface

`hugo.Config` (validated by `New`; all defaults applied):

| Field | Default | Meaning |
|---|---|---|
| `Root` | required | Hugo project root (must exist, be a directory; symlink-resolved) |
| `ContentDir` | `"content"` | Content directory inside the root (relative, no traversal) |
| `SectionTemplate` | `""` | `text/template` over `{Slug, Title, Date}`; rendered into a validated relative section path |
| `FilenamePattern` | `"{{.Slug}}.md"` | Template for the filename; must render a single `.md` segment |
| `FrontMatterFormat` | `"yaml"` | `yaml` is implemented; `toml`/`json` are reserved and rejected |
| `DateLayout` | `"2006-01-02"` | Go layout for the front-matter `date`; must round-trip a probe date |
| `Draft` | `nil` → `true` | Explicit draft state; the engine **defaults to draft** unless a preset/plan says otherwise (`WithDraft(false)` publishes live) |
| `Author` | `""` | Author fallback when the article has none |
| `BaseURL` | `""` | Site base URL; when set, a `canonical` field is derived from it and the relative path |
| `IncludeProvenance` | `false` | Record `run` ID + source `digest` in a non-public `newsroom` block |
| `Defaults` | `{}` | Extra string front-matter fields applied only when no other source supplies them |
| `Required` | `{}` | Field names that must resolve non-empty (title and date are always required) |

## Article Input

`hugo.Article`: `Title` (required), `Body` (required; the rendered Markdown),
`Date` (required — a zero date is refused, so output can never depend on the
wall clock), `Author`, `Summary`, `SourceURL`, `Tags`, `Categories`, and the
optional provenance pair `RunID` / `Digest` (canonical `sha256:<hex>`).
Metadata is trimmed and secret-checked on the way in; the body is exempt (it
is the output of the run's redaction boundary).

## Guarantees

- **Deterministic slug and path.** `Slugify` is Unicode-aware (accented and
  CJK titles keep their characters), collapses separators, truncates long
  slugs on a word boundary with a 12-hex suffix, and falls back to
  `slug-<16 hex>` for titles with no letters or digits. The same article
  always renders the same path and the same bytes; re-staging identical
  content is a no-op (`Result.Unchanged`).
- **Path safety.** Rendered section/filename output is validated (relative,
  no NUL/backslash, no dot segments, filename a single segment) and the final
  path is proven — with symlinks resolved — to stay inside the Hugo root.
  Templates are additionally probed at construction, so a static traversal
  fails in `New`, and a hostile *title* fails in `Stage`.
- **Never silently overwritten.** A different existing target is refused
  (`ErrExists`, carrying both digests); replacement requires
  `Options.Overwrite`.
- **Atomic writes.** Same-directory temp file, write, fsync, chmod 0600,
  rename; created directories are 0700. A failure at any step leaves the
  previous file intact and no temp residue.
- **Correct front-matter escaping.** Scalar quoting is delegated to
  `yaml.v3`'s emitter (explicit `!!str` tags); values that older YAML 1.1
  parsers would re-tag (`yes`, `on`, `0x…`, …) are force-single-quoted so
  every parser era reads exactly what was written.
- **Secrets.** Config and article metadata that look like credentials fail
  closed (`ErrSecret`); the error shows the redacted value, never the raw
  one. Files and directories carry no secret material.
- **Dry runs.** `DryRun` performs the full render and validation and returns
  the target path, a ≤ 4 KiB rune-safe preview, and the content digest —
  without writing anything (no file, no directory). An existing target is
  reported via `Result.Exists` / `Result.Unchanged`, never as an error.

## API

```go
pub, err := hugo.New(cfg)          // validates config, parses + probes templates
res, err := pub.DryRun(article)    // rehearsal: path, preview, digest; no write
res, err := pub.Stage(article, hugo.Options{Overwrite: true})
// res: Path, Relative, Slug, Date, Draft, Content, Digest, Preview,
//      Exists, Unchanged, Created, Updated
```

`Render` exposes the pure conversion (no filesystem mutation besides a
read-only existence probe).

## Tests

`internal/hugo` is covered by three suites over temporary Hugo trees
(`hugo_test.go` end-to-end: creation, determinism/idempotence, Unicode
titles, duplicates/overwrite, traversal, symlink escape, dry run,
draft/publish, hostile front-matter values, provenance, canonical URL,
defaults/required, date conventions, atomic-failure, plus package-level
helpers; `slug_test.go`, `frontmatter_test.go`, `unit_test.go` unit).
Coverage of the package is tracked against the repo's 80% floor via
`make check` / `make coverage`.
