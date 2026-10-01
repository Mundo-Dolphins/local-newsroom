# local-newsroom

Local-first AI newsroom for researching, verifying and writing content with
local LLMs.

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
