# Editorial Recipes (v0.6)

**Status**: implemented for v0.6 — [issue #138](https://github.com/Mundo-Dolphins/local-newsroom/issues/138).

Editorial recipes make recurring authored formats **reusable without code**. A
recipe is a YAML document owned by the profile tree that declares *what a
recurring run should produce* — its topic template, input parameters, output
shape, source policy, research guidance, and the output blueprint the article
or thread must implement. Runs are triggered from the CLI or the TUI:

```console
$ newsroom recipes list --profile-root profiles
$ newsroom run --recipe afc-east-weekly --input week=7
```

A run with `--recipe` behaves exactly like any other run — same stages, same
factual gates, same durable record — with three differences: its topic and
format may derive from the recipe, the research and writing stages receive the
recipe's guidance as *advisory prompt content*, and the write stage computes
and enforces the recipe's output blueprint deterministically.

## 1. The recipe is data, never code

A recipe file is a declarative document. The engine **parses, validates,
merges, and constrains** it; it never executes it:

- No `exec`, no shell, no arbitrary template substitution — free text renders
  into prompts and stage parameters only through bounded variable expansion
  over the recipe's own declared, validated inputs.
- Core factual-safety rules (claim validation, the final factual check) live
  in engine code. Recipe text is injected into prompts inside explicit
  delimiters marked *untrusted content*, and an empty recipe leaves every
  prompt byte-identical to the standard flow.
- Instruction text (research instructions, blueprint prompts, query hints,
  facts) is scanned for prompt-injection/override phrases and fails the load
  when found.

A run's behavior differs from a standard run only where a recipe *declares* a
difference, and every difference is clamped by engine ceilings.

## 2. Where recipes live

```
<profiles root>/
  common/
    recipes/
      <id>.yaml            # the base document
  authors/
    <author>/
      recipes/
        <id>.yaml          # optional author override
      examples/            # (worked examples referenced by a recipe)
```

When a run selects an editorial **publication** preset, the recipes root is
the bundle root (`editorial.bundle_root`) instead of the profiles root — the
same layout rule the profile loader uses, so a bundle carries its own recipes.

### Layer merge

Resolution merges at most two layers, **common → author**:

| Field | Merge rule |
|---|---|
| `schema`, `id` | must match exactly in both layers |
| `title`, `version`, `output_format`, `description` | author value wins when set |
| `inputs` | merged by name; author wins per input |
| `source_policy` | field-level: author value wins per field |
| `research.instructions` | concatenated (common, then author) |
| `research.required_facts` | concatenated |
| `research.entity` | author wins when present |
| `blueprint.units` | author units replace common units wholesale |
| `blueprint.missing_policy` | author value wins when set |
| `examples` | union of both layers |

An override file may set only the fields it wants to change: layer files
validate in isolation (structure, bounds, safety) but the *merged* document
must be whole (a title, at least one constraint) — the loader re-validates
the merge, so a combination neither layer was guilty of alone still fails.

The resolved document is deterministic: its SHA-256 (canonical JSON) is
recorded in the run manifest, and the full document is persisted in the run
directory — a resumed run always executes the recipe it started with.

## 3. Document reference

```yaml
schema: "0.6.0"                # required; exactly "0.6.0"
id: afc-east-weekly            # required; kebab-case; must equal the file name
title: AFC East Weekly         # required in the merged document (max 200 chars)
version: "1.0.0"               # optional; bounded identifier
output_format: article         # optional; article | thread | hybrid
description: Weekly recap...   # optional; max 1000 chars

inputs:                        # 1..16 declared inputs
  - name: week                 # config-key identifier (mirrors a run config key)
    type: week                 # string | int | date | week | url
    required: true
    min: 1                     # int/week: bounds
    max: 17
    choices: [a, b]            # string: allowed values (max 32)
    pattern: "^..."            # string: regular expression
    default: "7"               # must itself validate (coerced like a value)
    description: ...           # free text, bounded

source_policy:                 # optional; every field only narrows the engine
  explicit_urls: optional      # required | optional | forbidden (enforced at run planning)
  preferred_domains: [.nfl.com]      # candidates on these domains sort first
  required_domains: [.espn.com]      # sources must come from one of these (fail closed)
  query_hints: [...]           # max 10; replace the LLM planner for the run
  max_queries: 8               # 0..10 (engine ceiling 10)
  max_sources: 10              # 0..50 (engine ceiling 50); clamps the run's budget
  date_from: "2025-09-15"      # rendered over inputs → the search time window
  date_to: "2025-09-21"
  allow_enrichment: false      # archive retrieval off by default for recipes

research:
  instructions: >              # rendered over inputs; advisory prompt text
    Cover the Week {week} results for each AFC East team.
  required_facts:              # max 20; advisory "facts to cover where evidence supports them"
    - Each team's Week {week} result
  entity:                      # optional; enables per-entity blueprint units
    kind: team                 # lowercase label
    name_field: name
    fields: [name, record, key_players]
    group_by: record           # must be one of fields
    min_count: 1               # 0..50 (engine ceilings)
    max_count: 5

blueprint:                     # optional; the output the artifact must implement
  missing_policy: warn         # warn | fail — what a missing/unsupported REQUIRED unit does
  units:                       # 1..8 units
    - role: intro              # lowercase label
      title: "Week {week} recap"
      prompt: ...              # advisory per-unit writing guidance
      required: true
    - role: entity
      repeat: team             # expands per extracted entity (must equal research.entity.kind)
      title: "{entity.name} — {entity.record}"
      fields: [name, record]

examples:                      # 0..8 base-relative paths; content becomes writing guidance
  - examples/afc-east-week-2.md
```

All free-text fields have conservative length caps; files are capped at 64KB;
unknown YAML fields fail the load (`KnownFields`); literal secrets fail the
load (the shared `config.ValidateSecretKeys` scan); example paths must be
base-relative without `..`.

### Run inputs

`--input name=value` supplies values. Resolution rules: explicit values win
over defaults; every *required* input must resolve (the run fails before it
starts); values are type- and constraint-checked (int/week bounds, date and
url shapes, string choices/patterns); inputs are the *only* way a run affects
a recipe — values can never disable a constraint, a domain policy, or a
factual gate. Resolved values are bounded (≤32 entries, ≤8192 chars each) and
are recorded in the manifest so a resume re-validates them against the
recovered recipe.

## 4. How a recipe run executes

| Stage | Recipe effect |
|---|---|
| planning (cmd) | topic derives from the recipe when `--topic` is empty (title + sorted inputs); format derives from `output_format` when `--format` is empty; `explicit_urls: forbidden` rejects `--url` flags; the bounded reference lands on the manifest |
| discovery | the resolved recipe is persisted first (`recipe/resolved.json`); `query_hints` (rendered) seed discovery and **replace the LLM planner** — the engine still bounds queries/candidates; the `date_from`/`date_to` window becomes the search time hint; preferred domains reorder candidates; a smaller `max_sources` clamps the budget; `required_domains` fail the stage when no selected source matches |
| research | the researcher prompt gains a delimited, marked-untrusted "RECIPE RESEARCH INSTRUCTIONS" section (instructions + required facts); empty guidance → byte-identical prompt |
| content plan | the recipe's blueprint expands into a `ContentPlan` with per-unit claim traceability: static units as declared, `repeat: <kind>` units resolved against the verified dossier's entities (entity fields extracted by one LLM pass, then **re-validated against the verification** — the model's self-assessment is never trusted: unsupported entities are dropped, counts clamped to `min/max_count`, each unit paired with the claims its evidence supports). Persisted at `write/content-plan.json`. |
| write | the writer prompt gains the (advisory) recipe guidance and the content plan as the units to implement, in order; required units appear in the output or the `missing_policy` applies (warn or fail) — the engine never fabricates content to satisfy a unit |
| final check / render / review / summary | standard gates unchanged; the review bundle and run summary surface the recipe id (+ hash) where a recipe ran |

The **content plan** is the general result of a recipe: it is the engine's
deterministic expansion of a blueprint, with every unit traceable to the
verified claims that support it.

## 5. Run record

```
runs/<run-id>/
  manifest.json            # gains the optional "recipe" reference (identity + hash + inputs)
  recipe/
    resolved.json          # the full resolved recipe document (discovery stage output)
  write/
    content-plan.json      # the computed content plan (write stage output)
  ...                      # v0.5 trees, unchanged
```

Manifest schema stays `0.6.0`; the `recipe` property is optional with
`omitempty` — every existing manifest loads and renders unchanged. Resume
discipline: a resumed run re-loads the persisted resolved recipe (hash must
match the manifest) or re-resolves from the recorded recipes root (hash must
match the manifest; a changed file fails the resume closed).

## 6. CLI & TUI

```console
$ newsroom recipes list  [--profile-root DIR] [--author NAME] [--publication ID] [--bundle-root DIR]
$ newsroom recipes show <id>  [same flags]
$ newsroom run --recipe <id> [--recipe-author NAME] [--input name=value ...]
```

- `recipes list` shows the merged summaries (title, version, format, layer,
  inputs with required markers) and how to run one.
- `recipes show` prints the resolved (merged) YAML exactly as the engine
  executes it, with a short hash.
- `run --recipe` with `--url`; the recipe's `explicit_urls` stance decides
  whether that is legal.
- TUI: the new-run wizard gains a recipe selector (id + `name=value` inputs,
  last duplicate wins); topic/format fields prefill from the selected recipe
  and remain editable. A run without a recipe is byte-identical to before.

## 7. Backward compatibility

- No `--recipe` → zero behavior change: no recipe loads, no prompt section
  renders, no plan computes, no manifest field serializes.
- Everything additive and optional; the standard writer/discovery/researcher
  flows are unchanged (new config fields default to zero values).
- Pre-existing workspaces, archives, and manifests load unchanged.

**Non-goals of this issue**: no Mundo Dolphins-specific recipe ships in the
repository (the tree is generic; real recipes live in user profile bundles);
no web-app UI; no automatic publication wiring; no per-recipe approval
policies (the publication contract owns approvals).
