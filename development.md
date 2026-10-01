# Development Guide - Mundo-Dolphins Local Newsroom

This document outlines the development workflow, dependency management, and automated processes for this project.

## Table of Contents

- [Overview](#overview)
- [Development Workflow](#development-workflow)
- [Dependency Management](#dependency-management)
  - [Go Modules](#go-modules)
  - [GitHub Actions](#github-actions)
  - [Auto-Merge Configuration](#auto-merge-configuration)
- [Code Quality](#code-quality)
- [Testing](#testing)
- [Releasing](#releasing)

---

## Overview

Mundo-Dolphins Local Newsroom is a command-line newsroom tool written in Go. This project uses automated dependency updates to ensure security patches are applied promptly.

---

## Development Workflow

### Branch Structure

```
main (default branch)
├── feature/*    - New features
├── bugfix/*     - Bug fixes
└── dependabot/* - Managed by Dependabot
```

### Commit Conventions

Use semantic commit prefixes:

```bash
git commit -m "feat: add new command"
git commit -m "fix: resolve panic issue"
git commit -m "docs: update README"
```

---

## Dependency Management

### Go Modules

The project uses Go modules for dependency management. Dependencies are defined in:

- `go.mod` - Module definition and direct dependencies
- `go.sum` - Verified checksums for all dependencies

**To update dependencies manually:**

```bash
# Check for updates
go list -m -u all

# Update specific module
go get <module-name>@latest

# Update all direct dependencies
go get -u ./...

# Tidy up dependencies
go mod tidy
```

### GitHub Actions

CI workflows use GitHub Actions for automated testing and quality gates. Actions are defined in:

- `.github/workflows/ci.yml` - Main CI workflow

**Actions used:**
- `actions/checkout@v4` - Repository checkout
- `actions/cache@v4` - Build tool caching
- `actions/setup-go@v5` - Go environment setup
- `actions/upload-artifact@v4` - Coverage report upload

### Auto-Merge Configuration

#### How It Works

This project automatically merges dependency update PRs created by Dependabot to ensure security patches are applied without manual review.

**Auto-merge applies to:**
- ✅ Dependabot PRs (`dependabot[bot]`)
- ✅ Renovate PRs (`renovate[bot]`)
- ✅ GitHub Actions bot PRs with renovate refs

**Merge strategy:**
- Squash merge (all commits → single commit)
- Auto-applies on PR readiness

#### Manual Activation/Deactivation

To temporarily disable auto-merge for the repository:

```bash
# Disable auto-merge for the entire repository
curl -X DELETE \
  -H "Authorization: token $GITHUB_TOKEN" \
  https://api.github.com/repos/Mundo-Dolphins/local-newsroom/automated_security_fixes
```

To re-enable:

```bash
curl -X POST \
  -H "Authorization: token $GITHUB_TOKEN" \
  https://api.github.com/repos/Mundo-Dolphins/local-newsroom/automated_security_fixes
```

#### Workflow Configuration

Auto-merge is configured in `.github/workflows/auto-merge.yml`. The workflow:

1. Triggers on Dependabot/renovate PR events
2. Validates PR is ready for review
3. Performs squash merge using `gh pr merge --auto --squash`
4. Uses hardened runners for security

#### Exclusion Rules

If you need to prevent a specific PR from auto-merging:

**Via GitHub UI:**
1. Navigate to the PR
2. Click "Merge PR" button
3. Toggle "Disable auto-merge"

**Via API:**
```bash
curl -X POST \
  -H "Authorization: token $GITHUB_TOKEN" \
  https://api.github.com/repos/Mundo-Dolphins/local-newsroom/pulls/123/automerge
```

#### Monitoring Auto-Merge Activity

View auto-merge status:

```bash
# Get PR auto-merge status
gh pr view 123 --json automatedPullRequestMergeStatus
```

Check recent auto-merges:

```bash
# View PRs merged by Dependabot
gh pr list --author dependabot[bot] --state all
```

---

## Code Quality

Quality gates are defined in the Makefile. Running `make check` executes all quality tools.

### Available Commands

```bash
make check          # Run all quality gates
make lint           # Run golangci-lint
make security       # Run gosec security scanner
make coverage       # Generate coverage report
make tools          # Download/build quality tools
```

### Tools Configuration

| Tool | Version | Purpose |
|------|---------|---------|
| golangci-lint | (from Makefile) | Linter suite |
| gosec | (from Makefile) | Security scanner |

---

## Testing

### Running Tests

```bash
# Run all tests
go test ./...

# Run with coverage
go test -cover ./...

# Run with verbose output
go test -v ./...
```

### Coverage Report

Coverage reports are generated during CI and available as artifacts after successful runs.

---

## Releasing

### Version Bump Process

1. Update `go.mod` with new module version
2. Create and merge PR for application changes
3. Release process handled via Dependabot for dependencies only

### Tagging

After merging changes to main, tag the release:

```bash
git tag v1.0.0
git push origin v1.0.0
```

---

## Troubleshooting

### Auto-Merge Issues

**PR stuck in review:**
- Auto-merge only triggers on `ready_for_review` state
- Ensure all status checks pass (CI, lint, coverage)
- Request review to mark as ready

**Dependabot not creating PRs:**
- Check `.github/dependabot.yml` configuration
- Verify schedule runs (check GitHub Actions tab)
- Look for ignored dependencies in config

### Go Module Conflicts

```bash
# List outdated modules
go list -u -m all

# Fix dependency conflicts
go mod tidy && go mod verify
```

---

## Security

### Vulnerability Scanning

Automated scanning runs on every PR:
- `gosec` scans Go code for security issues
- Vulnerability updates are handled by Dependabot

### Dependabot Security

| Feature | Configuration |
|---------|--------------|
| Update frequency | Weekly (Sundays at 02:00 UTC) |
| Open PR limit | 5 per ecosystem |
| Auto-merge | Enabled for all Dependabot PRs |
| Labels | `dependencies`, `go`, `github-actions` |

---

## References

- [GitHub Dependabot Documentation](https://docs.github.com/en/code-security/dependabot/dependabot-version-updates/configuring-dependabot-version-updates)
- [GitHub Auto-Merge Documentation](https://docs.github.com/en/pull-requests/collaborating-with-pull-requests/incorporating-changes-from-a-pull-request/configuring-auto-merge-for-your-repository)
- [Go Modules](https://go.dev/doc/modules/getting-started)

---

## Quick Reference

### Command Cheat Sheet

| Command | Description |
|---------|-------------|
| `go mod tidy` | Clean up dependencies |
| `go list -u -m all` | Check outdated modules |
| `make check` | Run all quality gates |
| `go test ./...` | Run tests |
| `gh pr merge --auto --squash` | Manually enable auto-merge |

### CI Status Checks

Before creating a PR, ensure:
- ✅ All status checks pass
- ✅ `dependabot[bot]` can create PRs
- ✅ `auto-merge` workflow is enabled
- ✅ No security vulnerabilities in go.mod
