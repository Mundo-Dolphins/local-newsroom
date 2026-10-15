# local-newsroom — development and quality-gate targets
#
# Usage:
#     make help            Show this list of targets
#     make fmt             gofmt the whole module
#     make vet             Run go vet over ./...
#     make test            Run the full Go test suite
#     make coverage        Run tests with coverage, enforce COVER_MIN total coverage
#     make lint            Run golangci-lint over ./...
#     make security        Run gosec over ./...
#     make build           Build the newsroom CLI into dist/
#     make check           Run every quality gate (fmt vet coverage lint security build)
#     make clean           Remove build and coverage artifacts
#

TOOLCHAIN ?= ~/go/bin

GO := go

# Pinned tool versions (control upgrades via Makefile, not CI)

# renovate: datasource=github-releases depName=golangci/golangci-lint versioning=loose
GOLANGCI_LINT_VERSION = v2.14.0

# gosec is rebuilt from tools/gosec (see tools/gosec/doc.go): the pinned
# release there bundles an x/tools too old for the Go 1.27 build cache, so
# the wrapper forces a newer x/tools. Bump the version in tools/gosec/go.mod
# and run `make gosec-sync`.
GOSSEC_VERSION = v2.29.0

GOLANGCI_LINT = $(TOOLCHAIN)/golangci-lint
GOSSEC = $(TOOLCHAIN)/gosec
GOSSEC_WRAPPER = tools/gosec

DIST_DIR = $(CURDIR)/dist
COVERAGE_OUT = $(CURDIR)/coverage.out

# Minimum required total (repository-wide) test coverage, in percent.
# Supports decimals, e.g. COVER_MIN=82.5. Enforced by `make coverage`.
COVER_MIN ?= 80

.PHONY: help fmt vet test coverage lint security build check tools gosec-sync clean
.PHONY: $(GOLANGCI_LINT) $(GOSSEC)

# ---- Dependency bootstrap (kept separate from validation targets) ------------

tools: $(GOLANGCI_LINT) $(GOSSEC)

$(GOLANGCI_LINT):
	@mkdir -p $(TOOLCHAIN); \
	CURRENT_VERSION=$$($(GOLANGCI_LINT) --version 2>/dev/null | sed -n 's/^golangci-lint has version \([^ ]*\).*/\1/p'); \
	if [ "$$CURRENT_VERSION" != "$(GOLANGCI_LINT_VERSION)" ]; then \
		echo "+ $(GOLANGCI_LINT) ($(GOLANGCI_LINT_VERSION))"; \
		GOBIN=$(TOOLCHAIN) $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION); \
	else \
		echo "+ $(GOLANGCI_LINT) already at $(GOLANGCI_LINT_VERSION)"; \
	fi

$(GOSSEC):
	@mkdir -p $(TOOLCHAIN); \
	CURRENT_VERSION=$$($(GOSSEC) -version 2>/dev/null | sed -n 's/^Version: //p'); \
	if [ "$$CURRENT_VERSION" != "$(GOSSEC_VERSION)" ]; then \
		echo "+ $(GOSSEC) (built $(GOSSEC_VERSION) from $(GOSSEC_WRAPPER))"; \
		cd $(GOSSEC_WRAPPER) && $(GO) build -ldflags "-X main.Version=$(GOSSEC_VERSION)" -o $(GOSSEC) .; \
	else \
		echo "+ $(GOSSEC) already at $(GOSSEC_VERSION)"; \
	fi

# ---- Formatting and vet -------------------------------------------------------

fmt:
	@$(GO) fmt ./...

vet:
	@$(GO) vet ./...

# ---- Tests --------------------------------------------------------------------

test:
	@$(GO) test ./...

coverage:
	@$(GO) test -coverprofile="$(COVERAGE_OUT)" ./...
	@echo "Coverage report generated: $(COVERAGE_OUT)"
	@COVERAGE_PCT=$$(LC_ALL=C $(GO) tool cover -func="$(COVERAGE_OUT)" | LC_ALL=C awk '/^total:/ {gsub(/%/, "", $$3); print $$3}'); \
	if [ -z "$$COVERAGE_PCT" ]; then \
		echo "ERROR: could not determine total coverage from $(COVERAGE_OUT)"; \
		exit 1; \
	fi; \
	REQUIRED_PCT=$$(LC_ALL=C awk -v min="$(COVER_MIN)" 'BEGIN { printf "%.1f", min }'); \
	echo "Total coverage: $${COVERAGE_PCT}%"; \
	echo "Required minimum: $${REQUIRED_PCT}%"; \
	if LC_ALL=C awk -v pct="$$COVERAGE_PCT" -v min="$(COVER_MIN)" 'BEGIN { exit !(pct + 0 >= min + 0) }'; then \
		echo "Coverage requirement satisfied"; \
	else \
		echo "ERROR: coverage is below the required threshold"; \
		exit 1; \
	fi
	@echo "View with: go tool cover -func=$(COVERAGE_OUT)"

lint: $(GOLANGCI_LINT)
	@$(GOLANGCI_LINT) run --timeout 15m ./...

security: $(GOSSEC)
	@echo "Running gosec..."; \
	$(GOSSEC) ./...; \
	EXIT_CODE=$$?; \
	if [ $$EXIT_CODE -ne 0 ]; then \
		echo "gosec found security issues (exit $$EXIT_CODE)"; \
		exit 1; \
	else \
		echo "No security issues found."; \
	fi

# Regenerate the vendored gosec cmd files in tools/gosec after bumping the
# pinned version in tools/gosec/go.mod.
gosec-sync:
	@sh $(GOSSEC_WRAPPER)/sync.sh

# ---- Build ---------------------------------------------------------------------

build:
	@mkdir -p $(DIST_DIR)
	@$(GO) build -o $(DIST_DIR)/newsroom ./cmd/newsroom

# ---- Quality gate ---------------------------------------------------------------

check: fmt vet coverage lint security build

# ---- Help ------------------------------------------------------------------------

help:
	@echo "Available targets:"
	@echo "  help       - Show this help message"
	@echo "  tools      - Install golangci-lint and gosec into $(TOOLCHAIN)"
	@echo "  fmt        - Format Go code with gofmt"
	@echo "  vet        - Run go vet over all packages"
	@echo "  test       - Run the full Go test suite"
	@echo "  coverage   - Run tests with coverage, enforce COVER_MIN threshold"
	@echo "  lint       - Run golangci-lint over all packages"
	@echo "  security   - Run gosec over all packages"
	@echo "  build      - Build the newsroom CLI into dist/"
	@echo "  check      - Run all quality gates (fmt vet coverage lint security build)"
	@echo "  clean      - Remove build and coverage artifacts"

# ---- Clean ----------------------------------------------------------------------

clean:
	@rm -rf "$(DIST_DIR)"
	@rm -f "$(COVERAGE_OUT)"
	@echo "Cleaned build artifacts"
