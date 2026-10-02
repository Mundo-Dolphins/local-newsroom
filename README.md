# local-newsroom

Local-first AI newsroom for researching, verifying and writing content with
local LLMs.

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

### Discovery Limits

Automatic discovery has built-in limits to prevent excessive resource usage:

| Limit | Default | Range | Description |
|-------|---------|-------|-------------|
| Max search queries | 5 | 1-10 | Maximum number of SearXNG queries |
| Results per query | 10 | 1-100 | Maximum results per query |
| Max sources | 0 (unlimited) | 0-N | Maximum candidate URLs returned |

These limits are configurable via `--search-max-queries`, `--search-results-per-query`,
and `--search-max-sources` flags.

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
`~/go/bin`) with pinned versions controlled in the Makefile:

```bash
make tools
```

- **golangci-lint**: v2.14.0
- **gosec**: v2.27.0

To update tools, change the `GOLANGCI_LINT_VERSION` and `GOSSEC_VERSION`
variables in the Makefile and run `make tools`.

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
