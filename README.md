# local-newsroom

Local-first AI newsroom for researching, verifying and writing content with
local LLMs.

## First usable workflow

From a fresh machine to a reviewed article in eight steps: configure your
oMLX endpoint (Mac Studio) and SearXNG endpoint (Raspberry Pi) in one config
file, set your workspace/archive, create a writing profile, then launch with
`newsroom run --topic "..."` or the TUI. Every run persists its full record
to the workspace, is resumable after failure or interruption
(`newsroom run --resume <id>`), and finishes with a human review bundle
(`review.md`).

Full operator guide: [`docs/first-usable-workflow.md`](docs/first-usable-workflow.md)

## Configuration

All commands share a single YAML configuration file (v0.5+). Set it up once
and every command (research, write, verify, final-check, archive, ...) uses
the same model endpoints and credentials:

```bash
cp examples/newsroom.example.yaml ~/.newsroom.yaml
$EDITOR ~/.newsroom.yaml
newsroom config validate   # check the file
newsroom config show       # print the effective configuration
```

The CLI looks for the config file in this order (first existing file wins):

1. `--config /path/to/file.yaml` flag on any command (explicit path)
2. `$NEWSROOM_CONFIG` environment variable
3. `./.newsroom.yaml` or `./.newsroom.yml` in the current directory
4. `$XDG_CONFIG_HOME/newsroom/config.yaml` (or `~/.config/newsroom/config.yaml`)
5. `~/.newsroom.yaml`

If none exists the CLI runs on environment variables and built-in defaults
(see the per-command sections below) and prints a one-line notice.

### Precedence

For every setting, the first layer that provides a value wins:

```
CLI flag  >  environment variable  >  config file  >  built-in default
```

For example `--format json` beats `ARCHIVE_FORMAT=text`, which beats
`archive.format` in the config file, which beats the built-in `text`.

### Secrets

API keys are **never stored in the config file**. Key fields only hold a
reference to the environment variable that contains the credential:

```yaml
llm:
  base_url: http://localhost:11434/v1
  model: llama3.1:70b
  api_key:
    env: OMLX_API_KEY   # the key itself lives only in the environment
```

`newsroom config show` and run manifests always print `[REDACTED]` for secret
values, even if the referenced variable is set. A config file containing a
literal key value is rejected.

local-newsroom **never intentionally persists credentials**. Every file a run
persists to the workspace — manifests, search plans, discovery results,
fetched source pages and normalized documents, dossiers, verification
results, editorial artifacts, final-check results, review bundles, rendered
output, warnings/errors, and attempt history — is sanitized by a single
redaction boundary in the workspace store *before* it reaches disk: resolved
API keys are replaced exactly wherever they appear, and pattern detectors
(bearer tokens, basic-auth tokens, PEM private-key blocks, assignments to
secret-like keys, URL credentials, high-entropy token runs) cover the rest.
If a fetched page or document contains a detected secret, the persisted copy
is redacted and a sidecar (`<path>.redaction.json`) records the redaction
(`redacted: true` and the replacement count); source fidelity may therefore
be reduced by redaction, and a sanitized copy is all that exists on disk.
Sanitization fails closed: content that still contains a detected secret is
not written, and the error never includes the secret itself.

### The `config` command

```bash
newsroom config show                 # human-readable table with per-value sources
newsroom config show --format json   # machine-readable, secrets redacted
newsroom config validate             # exit non-zero on unknown keys or out-of-range values
```

The table shows, for every setting, the resolved value and where it came
from: a flag name, `env:NAME`, `config`, `default`, or `unset` (setting has
no value in any layer; the command then applies its own stage-specific
default, e.g. write temperature 0.3, final-check 0.1).

A full annotated example covering every section (llm, search, embedding,
rerank, archive, fetch, chunk, profiles, defaults) lives at
[examples/newsroom.example.yaml](examples/newsroom.example.yaml).

## Editorial bundle

The **editorial bundle** is how a *private* repository of editorial content
meets this public engine. It is a plain local directory — typically a
separately checked-out private repository — that supplies publication presets
and, optionally, editorial profile content. The engine consumes the directory
exactly as it is present: it never clones or pulls a Git repository and makes
no network access while loading a bundle.

```
<bundle-root>/
  common/                     optional: publication-wide profile content
    style.md
    facts-policy.md
  authors/                    optional: author profiles
    <author>/style.md
  publications/               optional: one directory per publication
    <name>/
      publication.yaml        the publication preset
      hugo.yaml               optional: Hugo destination config
      bluesky.yaml            optional: Bluesky destination config
```

A publication preset is a resolved, validated configuration reference:

```yaml
# publications/mundodolphins/publication.yaml
profile: javi            # the bundle profile to use (author or dir/author)
language: es             # output language
renderer: thread         # output format: article | thread | hybrid
hugo: hugo.yaml          # optional destination config file reference
bluesky: bluesky.yaml    # optional destination config file reference
```

```
# publications/mundodolphins/bluesky.yaml
handle: "@mundodolphins"        # quoted: "@" is a reserved YAML indicator
api_key:
  env: BLUESKY_API_KEY          # same env-reference-only secret rule as the config file
```

Configure the bundle in the newsroom config file, the environment, or
per-run flags (same precedence as every other setting):

```yaml
editorial:
  bundle_root: ~/editorial-bundle   # $NEWSROOM_EDITORIAL_BUNDLE
  publication: mundodolphins        # $NEWSROOM_PUBLICATION
```

```bash
newsroom run --topic "..." --bundle-root ~/editorial-bundle --publication mundodolphins
```

When a publication is selected:

- Its **language** and **renderer** apply only where no higher layer
  (flag, environment, config file) provided a value — the preset sits
  below the config file in the precedence chain.
- Its **profile** reference loads the profile from the *bundle* (falling
  back to `--profile` / `profiles.name` when given); with no reference at
  all, the bundle's `common/` profile is used.
- The run manifest records the preset's **identity and provenance only**:
  its name, the bundle path, and a hash of the resolved non-secret values.
  Private content — style text, author voice, handles, destination URLs —
  never lands in the run record, and a resumed run re-derives the recorded
  preset from the manifest.

Security rules for bundle files mirror the config file: credential-like
keys must hold an environment reference (a literal secret is a load-time
error), unknown keys are rejected, file references are plain names inside
the preset directory, and a path that would escape the bundle is refused.

The engine-side counterpart for a Hugo destination is the staging adapter in
`internal/hugo` ([docs/hugo-publication.md](docs/hugo-publication.md)): it
turns a READY rendered article into a site-ready content file inside a Hugo
tree — deterministic path, safe front matter, draft by default, explicit
overwrite, atomic writes, dry-run capable — and stages only (no Git, no
network). The bundle's `hugo.yaml` destination feeds it. The local-Git half
of the v0.6 story is the committing adapter in `internal/gitpub`
([docs/git-publication.md](docs/git-publication.md)): it takes those staged
files and commits them on a dedicated branch of the site's existing checkout
— clean-tree required, only the expected files staged, configurable commit
message, idempotent retries, dry-run, receipt with commit SHA + branch — and
stops there: it never pushes, merges, or rewrites history.

The remote half of the v0.6 story is the social publisher in
`internal/bluesky` ([docs/bluesky-publication.md](docs/bluesky-publication.md)),
backed by the generic `Publisher` types in `internal/social`: it publishes
approved posts to a Bluesky account as a thread over the official AT Protocol
xrpc surface (`createSession` + `createRecord`) — app password via
environment variable only, in-memory JWTs that rotate on expiry/401, grapheme
and byte validation before any write, deterministic link facets with byte
offsets, strong-ref reply chaining, resumable partial failures, dry-run with
zero network, and a digest-bound receipt naming the post AT URIs.

The glue that makes all of this safe across crashes is the publication
persistence layer in `internal/publish`
([docs/contracts-v0.6.md §20](docs/contracts-v0.6.md#20-persistence-and-idempotent-resumetry)):
the plan document is persisted under the run's `publication/` tree through
the workspace sanitization boundary, the run manifest gains a small
`publication_state` summary (manifest schema 0.6.0; v0.5 manifests load
unchanged), and the publish engine resumes a crashed or partially failed
cycle idempotently — a target that holds a receipt is never re-executed, a
dangling in-flight target is aborted rather than blindly retried, and a
changed artifact invalidates its approval by digest.

On top of the persistence layer sits the publication orchestrator in
`internal/publish` ([docs/publication-orchestrator.md](docs/publication-orchestrator.md)):
it drives the full pipeline for a run — ready check, plan, dry-run
preview, human approval, execution in dependency order, and the final
publication summary — and it is deliberately UI-independent: it never
prompts or renders anything, it presents the approval screen as data
(run/topic, artifact digest, destinations, exact branch/path or post
count, dry-run results, warnings) and receives the approval as a domain
value. A later failure never deletes what is already public: the summary
names what exists where and gives the manual next steps (verify, complete,
retry, push/merge the local commit, or re-approve a new plan).

## Research Command

The `newsroom research` command fetches URLs, extracts content, and generates
a validated research dossier. It supports multiple source discovery modes:

### Source Modes

The command supports three source modes:

1. **URL-only mode (v0.1)**: Specify explicit URLs with `--url`. At least one URL is required.

2. **Automatic discovery**: Configure SearXNG with `--search-base-url` to discover
   sources automatically based on the topic.

3. **Supplement mode**: Combine explicit URLs with automatic discovery using
   both `--url` and `--search-base-url`.

### Usage Examples

#### URL-only mode (backward compatible)

```bash
newsroom research \
  --topic "Miami Dolphins defensive changes" \
  --url https://example.com/article-1 \
  --url https://example.com/article-2 \
  --output dossier.json
```

#### Automatic discovery via SearXNG

```bash
# Using CLI flag
newsroom research \
  --topic "Miami Dolphins injury report Week 5" \
  --search-base-url http://raspberrypi:8080 \
  --output dossier.json

# Using environment variable
export SEARXNG_BASE_URL="http://raspberrypi:8080"
newsroom research \
  --topic "Miami Dolphins injury report Week 5" \
  --output dossier.json
```

#### Supplement mode (explicit URLs + discovered sources)

```bash
newsroom research \
  --topic "Sports news" \
  --url https://example.com/sports \
  --search-base-url http://raspberrypi:8080 \
  --search-max-queries 3 \
  --output report.json
```

### Flags

| Flag | Required | Description |
|------|----------|-------------|
| `--topic`, `-t` | Yes | Research topic |
| `--url`, `-u` | No (when using search) | URL to fetch (can be specified multiple times) |
| `--search-base-url` | No (when using URLs) | SearXNG base URL (default: `SEARXNG_BASE_URL` env var) |
| `--search-language` | No | Search language (RFC 5646 tag, e.g., `en`, `en-US`) |
| `--search-time-range` | No | Search time range (e.g., `last_week`, `last_month`) |
| `--search-max-queries` | No | Maximum search queries to execute (1-10, default: 5) |
| `--search-results-per-query` | No | Maximum results per search query (1-100, default: 10) |
| `--search-max-sources` | No | Maximum candidate sources to return (0 = unlimited, default: 0) |
| `--output`, `-o` | No | Output path for dossier JSON (default: `dossier.json`) |
| `--llm-base-url` | No | LLM API base URL (default: `OMLX_BASE_URL` env var) |
| `--llm-model` | No | LLM model name (default: `OMLX_MODEL` env var) |
| `--llm-api-key` | No | LLM API key for authentication (default: `OMLX_API_KEY` env var) |
| `--llm-timeout` | No | LLM request timeout in seconds (default: 120) |
| `--fetch-timeout` | No | HTTP fetch timeout in seconds (default: 30) |
| `--max-size` | No | Maximum response size in bytes (default: 10MB) |
| `--max-words` | No | Maximum words per document (default: 50000) |
| `--prompt-file` | No | Path to custom prompt file |

### Source Mode Semantics

| URLs Provided | Search Configured | Mode | Behavior |
|---------------|-------------------|------|----------|
| Yes | No | URL-only | Fetch and analyze only specified URLs (v0.1 behavior) |
| No | Yes | Automatic discovery | Discover sources via SearXNG, fetch and analyze |
| Yes | Yes | Supplement mode | Fetch explicit URLs first, then discover additional sources |

### Environment Variables

| Variable | Description |
|----------|-------------|
| `OMLX_BASE_URL` | LLM API base URL (e.g., `http://localhost:8000/v1`) |
| `OMLX_MODEL` | LLM model name (e.g., `llama3.1:8b`) |
| `OMLX_API_KEY` | LLM API key for authentication (optional) |
| `SEARXNG_BASE_URL` | SearXNG base URL (required for automatic discovery) |
| `SEARXNG_SEARCH_LANGUAGE` | Default search language (RFC 5646 tag) |
| `SEARXNG_SEARCH_TIME_RANGE` | Default search time range |
| `SEARXNG_API_KEY` | SearXNG API key (if instance requires authentication) |
| `SEARXNG_API_HEADER` | SearXNG API header name |

**Precedence**: CLI flags take precedence over environment variables, which
take precedence over hardcoded defaults.

### Authentication

#### oMLX (LLM)

The `newsroom research` command supports authentication for oMLX instances:

```bash
export OMLX_BASE_URL="http://mac-studio:8000/v1"
export OMLX_MODEL="Qwen3.8-27B-8bit"
export OMLX_API_KEY="your-api-key-here"

newsroom research \
  --topic "Example research topic" \
  --url "https://example.com"
```

Or use the CLI flag:

```bash
newsroom research \
  --llm-api-key "your-api-key-here" \
  --llm-base-url "http://mac-studio:8000/v1" \
  --llm-model "Qwen3.8-27B-8bit" \
  --topic "Example research topic" \
  --url "https://example.com"
```

**Security**: API keys are sent as `Authorization: Bearer <key>` and are never
logged or included in error messages.

#### SearXNG (Search)

SearXNG authentication is configured via environment variables:

```bash
export SEARXNG_BASE_URL="http://raspberrypi:8080"
export SEARXNG_API_KEY="your-searxng-api-key"
export SEARXNG_API_HEADER="X-API-Key"

newsroom research \
  --topic "Research topic" \
  --search-base-url "$SEARXNG_BASE_URL" \
  --output dossier.json
```

### SearXNG Configuration Requirements

For automatic discovery to work, the SearXNG instance must have **JSON output enabled**.
This is controlled by the `searchengines` configuration in your SearXNG `settings.yml`:

```yaml
search:
  formats:
    - json  # Enable JSON output
```

Without JSON output enabled, the search API may return HTTP 403 Forbidden.

### Source & Search Provenance

The pipeline tracks the complete provenance of all sources:

- **Search provenance**: Each source records which search query discovered it, preserving
  the query text and result ranking.
- **Source provenance**: When the same URL is discovered by multiple queries, the
  pipeline tracks which query found it first and maintains that as the primary
  discovery source.
- **Metadata**: Sources include fields like `originalURL`, `searchQuery`, `searchRank`,
  and `provenance` that enable audit trails and reproducibility.

### Discovery Limits

Automatic discovery has built-in limits to prevent excessive resource usage:

| Limit | Default | Range | Description |
|-------|---------|-------|-------------|
| Max search queries | 5 | 1-10 | Maximum number of SearXNG queries |
| Results per query | 10 | 1-100 | Maximum results per query |
| Max sources | 0 (unlimited) | 0-N | Maximum candidate URLs returned |

These limits are configurable via `--search-max-queries`, `--search-results-per-query`,
and `--search-max-sources` flags.

### Budget & Constraints

The system enforces budgets at multiple stages:

1. **Search budget**: Max queries × results per query limits total search results
2. **Fetch budget**: Timeout and size limits prevent resource exhaustion
3. **Extraction budget**: Word limits prevent processing excessively long documents
4. **Candidate selection**: Ranking and deduplication ensure high-quality sources

### Testing & Coverage

The project includes comprehensive end-to-end verification with **deterministic offline tests**:

- **20+ E2E tests** covering all research and writing scenarios (v0.3)
- **11 v0.4 pipeline tests** for complete offline E2E coverage
- **Fake LLM and SearXNG servers** for deterministic, reproducible testing
- **No external network access** - tests are fully offline and deterministic

Test scenarios include:
- Successful topic-only research
- Multiple search queries with overlapping URLs
- Partial failures (search/fetch/extraction)
- No-results failure handling
- URL-only fallback (v0.1 compatibility)
- Source/search provenance verification
- Candidate selection and ranking
- Concurrent fetch operations
- Determinism verification

### v0.4 E2E Pipeline (Offline Testing)

v0.4 introduces a complete offline end-to-end editorial pipeline with deterministic
fake LLMs for full testing without external dependencies:

**Pipeline Stages**:
```
ResearchDossier → Fake Verifier LLM → VerificationResult → 
Optional Follow-Up → Fake Writer LLM → EditorialArtifact → 
Final Checker → Renderer → article.md / Bluesky thread
```

**Key Features**:
- Fully offline E2E testing with fake deterministic LLMs
- No oMLX, SearXNG, or real web access required for tests
- Bluesky 300-character post invariant enforced
- Profile inheritance model for style/tone configuration
- Intermediate artifact round-trip preservation
- v0.3 RAG and research functionality unchanged

**Documentation**: See `docs/v04-editorial-pipeline.md` for complete architecture.

### Limitations

- **SearXNG required** for automatic discovery (URL-only mode works without it)
- **External LLM required** for planning and research (not supported for local inference)
- **Test constraints**: v0.4 E2E tests use fake servers; real integration requires live
  SearXNG and LLM endpoints
- **No automatic publication**: v0.4 saves artifacts as files only; no Hugo or Bluesky
  publishing automation (v0.5+)
- **Offline-first**: v0.4 tests use fake LLMs; real LLM integration requires v0.5+

Automatic discovery has built-in limits to prevent excessive resource usage:

| Limit | Default | Range | Description |
|-------|---------|-------|-------------|
| Max search queries | 5 | 1-10 | Maximum number of SearXNG queries |
| Results per query | 10 | 1-100 | Maximum results per query |
| Max sources | 0 (unlimited) | 0-N | Maximum candidate URLs returned |

These limits are configurable via `--search-max-queries`, `--search-results-per-query`,
and `--search-max-sources` flags.

## Archive Command

The `newsroom archive` command provides a local-first semantic search system for
news articles and web content. It archives URLs for later retrieval using embedding-based
similarity search.

### Commands

#### Add to Archive

Archive URLs to the local index. Supports incremental, idempotent ingestion.

```bash
# Add a single URL
newsroom archive add --url https://example.com/article

# Add multiple URLs
newsroom archive add \
  --url https://example.com/article1 \
  --url https://example.com/article2

# Add from a file (one URL per line)
newsroom archive add --urls-file urls.txt
```

The archive pipeline:
```
URL -> fetcher -> extractor -> chunker -> embeddings -> SQLite
```

Repeated ingestion of the same content is idempotent - unchanged content is not
re-archived. The command supports parallel fetching with configurable concurrency.

#### Semantic Search

Perform semantic search over archived content using embedding-based similarity.

```bash
# Basic search
newsroom archive search --query "Miami Dolphins defensive coordinator"

# Search with custom parameters
newsroom archive search \
  --query "Super Bowl predictions" \
  --top-k 20 \
  --format json

# Search from stdin
echo "AI regulations 2024" | newsroom archive search --from-stdin
```

Optionally enables reranking for improved accuracy using a cross-encoder model.

#### Archive Statistics

Display statistics about the local archive.

```bash
# Show archive stats
newsroom archive stats

# Show archive stats with custom database
newsroom archive stats --db-path ./archive.db

# Output as JSON
newsroom archive stats --format json
```

Shows counts of documents, chunks, and embeddings.

### Usage Examples

#### Basic archiving and search

```bash
# Archive news articles
newsroom archive add \
  --url https://example.com/dolphins-news \
  --url https://example.com/sports-update \
  --db-path ~/newsroom/archive.db

# Search later
newsroom archive search \
  --query "Dolphins playoff chances" \
  --db-path ~/newsroom/archive.db
```

#### Search from stdin

```bash
# Search using a query from another command
echo "$TOPIC" | newsroom archive search --from-stdin
```

### Flags

#### Global Flags (all archive commands)

| Flag | Default | Description |
|------|---------|-------------|
| `--db-path` | `./archive.db` | Path to the archive SQLite database |
| `--format` | `text` | Output format: `text` or `json` |
| `--embedding-base-url` | `http://localhost:8000/v1` | Embedding API base URL |
| `--embedding-model` | `all-MiniLM-L6-v2` | Embedding model name |
| `--embedding-api-key` | - | API key for embedding API authentication |
| `--embedding-timeout` | 30 | Embedding API timeout in seconds |
| `--rerank-enabled` | - | Enable reranking for search |
| `--rerank-model` | `bge-reranker` | Reranker model name |
| `--top-k` | 10 | Top-K results for search |
| `--version` | - | Print version and exit |

#### Archive Add Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--url`, `-u` | - | URL to archive (can be specified multiple times) |
| `--urls-file` | - | File containing URLs to archive (one per line) |
| `--parallel-fetch` | 5 | Number of parallel fetch operations |
| `--fetch-timeout` | 30 | HTTP fetch timeout in seconds |
| `--max-size` | 10MB | Maximum response size in bytes |
| `--max-words` | 50000 | Maximum words per document |
| `--target-chunk-size` | 300 | Target chunk size in characters |
| `--max-chunk-size` | 500 | Maximum chunk size in characters |
| `--overlap-ratio` | 0.1 | Overlap ratio between chunks (0.0 to 0.99) |
| `--progress` | true | Show progress during archiving |

#### Archive Search Flags

| Flag | Required | Description |
|------|----------|-------------|
| `--query`, `-q` | Yes | Search query text |
| `--from-stdin` | No | Read query from stdin |

### Environment Variables

| Variable | Description |
|----------|-------------|
| `ARCHIVE_DB_PATH` | Default database path (overrides `--db-path`) |
| `EMBEDDING_BASE_URL` | Embedding API base URL |
| `EMBEDDING_MODEL` | Embedding model name |
| `EMBEDDING_API_KEY` | API key for embedding API authentication |
| `OMLX_BASE_URL` | Base URL for oMLX embedding API |
| `OMLX_MODEL` | oMLX model name |
| `OMLX_API_KEY` | oMLX API key |

**Precedence**: CLI flags take precedence over environment variables, which
take precedence over hardcoded defaults.

### Architecture

```
┌─────────┐    ┌───────────┐    ┌─────────┐    ┌───────────┐    ┌─────────┐
│   URL   │───▶│  Fetcher  │───▶│ Extract │───▶│  Chunker  │───▶│  Embed  │
└─────────┘    └───────────┘    └─────────┘    └───────────┘    └─────────┘
                                                                            │
                                                                            ▼
                                                      ┌─────────────────────────┐
                                                      │        SQLite           │
                                                      │   (archive.db)          │
                                                      │                         │
                                                      │  ┌─────────────────┐    │
                                                      │  │ documents       │    │
                                                      │  │ chunks          │    │
                                                      │  │ embedding_vectors │  │
                                                      │  └─────────────────┘    │
                                                      └─────────────────────────┘
┌─────────┐    ┌───────────┐    ┌─────────┐    ┌───────────┐    ┌─────────┐
│  Query  │───▶│  Embed    │───▶│ Retriever│───▶│ Reranker │───▶│ Results │
└─────────┘    └───────────┘    └─────────┘    └───────────┘    └─────────┘
```

### Testing & Coverage

The archive command includes comprehensive unit tests:

- **Command registration**: Verifies all subcommands are registered
- **Workflow tests**: Test the extract-chunk-embed-store pipeline
- **Idempotency**: Verifies re-archiving same content produces same IDs
- **Output formats**: Tests JSON and text output formats

## TUI

`newsroom tui` opens a keyboard-first terminal interface for daily work with
the pipeline: browse recent runs, start new runs through a wizard (topic,
profile, format, source mode, URLs), watch stage progress live, cancel or
resume runs, retry a failed stage, and open artifacts in `$EDITOR`.

```bash
newsroom tui                 # recent runs for the default workspace
newsroom tui --workspace myproject
```

- **Keyboard-first:** `j/k` select, `enter` open, `n` new run, `r` resume,
  `t` retry stage, `c` cancel, `o` open artifact, `v` verbose, `?` key
  reference, `q` quit. Cancellation and quit-while-running ask for
  confirmation.
- **Presentation layer only:** the TUI polls the run/workflow services
  (`internal/tui` over a `Service` interface) and contains no pipeline logic;
  a run started from the TUI produces the same manifest as `newsroom run`.
- **Remote-friendly:** the TUI itself makes no network calls. Configure the
  shared config (or `OMLX_BASE_URL` / `SEARXNG_BASE_URL`) to point at
  wherever the LLM and search endpoints live — the header shows the resolved
  endpoints, so remote targets are visible before a run starts.
- **Terminal required:** piped invocations fail fast and point at
  `newsroom run`, so scripts and CI keep using the CLI.

Full reference: [`docs/tui-v0.5.md`](docs/tui-v0.5.md).

## Run Review

Every run that reaches a terminal state (succeeded, failed, or cancelled)
gets a `review.md` inside its run directory: a single, human-readable
document summarizing the topic, sources, claims and their verification
status, the follow-up research, the final factual check's findings, the
output files, and the run's profile/model — with a plain **READY** /
**NOT READY** verdict on the output.

A run that failed its final check is never described as ready.

```bash
newsroom review run-20260711T100000Z-abcdef01      # print + refresh review.md
newsroom review run-... --workspace-root .newsroom
newsroom review run-... --no-save                  # print only
```

The bundle is a deterministic projection of the run's persisted record: no
LLM calls, re-runnable, secret-free (every value passes the run package's
redaction and the workspace store's sanitization boundary), and never
referenced on the manifest (failed runs cannot carry artifacts, but they are
exactly when the document is needed).

Full reference: [`docs/review-bundle-v0.5.md`](docs/review-bundle-v0.5.md).

## Development commands

The project's common development and quality-gate commands live in the root
`Makefile`. Use `make help` to see the complete list of targets.

```bash
make help
```

The main gates used by CI are exposed through a single target:

```bash
make check
```

`check` runs formatting, `go vet`, the full test suite with a coverage floor of
80% (`make coverage` on its own), `golangci-lint` (`make lint`), `gosec`
(`make security`) and builds the CLI (`make build`).

Individual gates can be run on their own, e.g. `make test`, `make fmt`,
`make lint`, `make security` or `make clean`.

## Tools

`golangci-lint` and `gosec` are installed into `$(TOOLCHAIN)` (default
`~/go/bin`) with pinned versions:

```bash
make tools
```

- **golangci-lint**: v2.14.0 (installed from release, version pinned in the
  `Makefile`)
- **gosec**: v2.29.0, rebuilt from the `tools/gosec` wrapper module (the
  pinned release's bundled `golang.org/x/tools` is too old to read Go 1.27
  build caches; the wrapper forces a newer one — see
  `tools/gosec/doc.go`). The version is pinned in `tools/gosec/go.mod`; after
  bumping it, run `make gosec-sync`.

Dependency installation is never performed silently by the validation targets,
so `make check` will not fetch anything for you: run `make tools` first.

## Continuous Integration

This project uses GitHub Actions for automated quality gates. The workflow runs
on pull requests and pushes to `main`:

- **Branches**: `main`
- **Triggers**: pull requests and pushes
- **Coverage floor**: 80% (enforced by CI)

The CI workflow reuses the `Makefile` quality targets to ensure local and remote
checks stay aligned. See `.github/workflows/ci.yml` for the full workflow.

## Requirements

- Go 1.23 or newer
- `golangci-lint` and `gosec` installed via `make tools` (or already on
  `$(TOOLCHAIN)`)
