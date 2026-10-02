# local-newsroom

Local-first AI newsroom for researching, verifying and writing content with
local LLMs.

## Research Command

The `newsroom research` command fetches URLs, extracts content, and generates
a validated research dossier:

```bash
newsroom research \
  --topic "Miami Dolphins defensive changes" \
  --url https://example.com/article-1 \
  --url https://example.com/article-2 \
  --output dossier.json
```

### Flags

| Flag | Required | Description |
|------|----------|-------------|
| `--topic`, `-t` | Yes | Research topic |
| `--url`, `-u` | Yes | URL to fetch (can be specified multiple times) |
| `--output`, `-o` | No | Output path for dossier JSON (default: `dossier.json`) |
| `--llm-base-url` | No | LLM API base URL (default: `OMLX_BASE_URL` env var) |
| `--llm-model` | No | LLM model name (default: `OMLX_MODEL` env var) |
| `--llm-api-key` | No | LLM API key for authentication (default: `OMLX_API_KEY` env var) |
| `--llm-timeout` | No | LLM request timeout in seconds (default: 120) |
| `--fetch-timeout` | No | HTTP fetch timeout in seconds (default: 30) |
| `--max-size` | No | Maximum response size in bytes (default: 10MB) |
| `--max-words` | No | Maximum words per document (default: 50000) |
| `--prompt-file` | No | Path to custom prompt file |

### Environment Variables

| Variable | Description |
|----------|-------------|
| `OMLX_BASE_URL` | LLM API base URL (e.g., `http://localhost:8000/v1`) |
| `OMLX_MODEL` | LLM model name (e.g., `llama3.1:8b`) |
| `OMLX_API_KEY` | LLM API key for authentication (optional, required by some oMLX instances) |

### Authentication

The `newsroom research` command supports authentication for oMLX instances that require an API key. The API key can be provided via:

1. **Environment variable** (`OMLX_API_KEY`):

```bash
export OMLX_BASE_URL="http://mac-studio:8000/v1"
export OMLX_MODEL="Qwen3.8-27B-8bit"
export OMLX_API_KEY="your-api-key-here"

newsroom research \
  --topic "Example research topic" \
  --url "https://example.com"
```

2. **CLI flag** (`--llm-api-key`):

```bash
newsroom research \
  --llm-api-key "your-api-key-here" \
  --topic "Example research topic" \
  --url "https://example.com"
```

**Precedence**: The CLI flag takes precedence over the environment variable. If neither is provided, the request is sent without authentication (suitable for oMLX instances that do not require an API key).

### Example: Authenticated oMLX Server

For a fully authenticated workflow, set all LLM configuration:

```bash
export OMLX_BASE_URL="http://mac-studio:8000/v1"
export OMLX_MODEL="Qwen3.8-27B-8bit"
export OMLX_API_KEY="..."

newsroom research \
  --topic "Example research topic" \
  --url "https://example.com"
```

Or use the CLI flag for the API key only:

```bash
newsroom research \
  --llm-api-key "..." \
  --llm-base-url "http://mac-studio:8000/v1" \
  --llm-model "Qwen3.8-27B-8bit" \
  --topic "Example research topic" \
  --url "https://example.com"
```

**Note**: The API key is sent as `Authorization: Bearer <key>` in HTTP requests and is never logged or included in error messages.

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

- **golangci-lint**: v2.1.2
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
