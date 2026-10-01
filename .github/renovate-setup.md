# Renovate Setup

This repository uses Renovate to automatically keep toolchain versions synchronized.

## Configuration Files

| File | Purpose |
|------|---------|
| `.github/renovate.json5` | Renovate configuration |
| `.github/workflows/renovate.yml` | Scheduled Renovate workflow |

## What Renovate Manages

### Makefile Tool Versions
| Variable | Location | Datasource | Package Name |
|----------|----------|------------|--------------|
| `GOLANGCI_LINT_VERSION` | `Makefile` | `github-releases` | `golangci/golangci-lint` |
| `GOSSEC_VERSION` | `Makefile` | `github-releases` | `securego/gosec` |

### Go Version Synchronization
| Variable | Location | Datasource | Package Name |
|----------|----------|------------|--------------|
| `GO_VERSION` | `.github/workflows/ci.yml` | `golang-version` | `golang` |

> **Note**: The Go version in `go.mod` is managed by Dependabot (via `gomod` ecosystem). Renovate manages the `GO_VERSION` in `ci.yml` to ensure both stay synchronized. Updates will be grouped together.

### GitHub Actions
GitHub Actions packages (`actions/checkout`, `actions/cache`, `actions/setup-go`, etc.) are managed by **Dependabot** via the `github-actions` ecosystem. Renovate is excluded from these to avoid duplicate PRs.

## Ownership Split

| Ecosystem | Managed By |
|-----------|------------|
| `go.mod` (Go modules) | Dependabot |
| `go.sum` (Go checksums) | Dependabot |
| GitHub Actions packages | Dependabot |
| Makefile tool versions | Renovate |
| `GO_VERSION` in `ci.yml` | Renovate |

This split ensures clear ownership and prevents duplicate PRs.

## Scheduling

- **Automatic runs**: Every Sunday at 02:00 UTC
- **Manual runs**: Use the "Run Workflow" button in GitHub Actions

## Required Secrets

### `RENOVATE_TOKEN` (Optional)

A repository secret is required if the default `GITHUB_TOKEN` permissions are insufficient for Renovate to create/update its own PRs and branches.

If not set, Renovate uses `github.token` (the default GitHub workflow token), which has:
- **Read**: Repository contents, Actions
- **Write**: Repository contents, Pull requests, Actions, Contents (create PRs)

For most repositories, the default token is sufficient. If you need more control or have permission restrictions:

1. Create a Personal Access Token (PAT) with these minimal scopes:
   - `repo` (full control of private repositories)
   
2. Add it as a repository secret:
   ```bash
   echo "RENOVATE_TOKEN=$PAT" | gcloud secrets create renovate-token --data-file=-
   gcloud secrets add-iam-policy-binding renovate-token --member="user:YOUR-GITHUB-EMAIL" --role="roles/secretmanager.secretAccessor"
   ```

## Validation

To validate the Renovate configuration locally:

```bash
# Install Renovate CLI
npm install -g @renovatebot/renovate

# Validate config
renovate config validate .github/renovate.json5
```

## Verification (After Deployment)

After the Renovate workflow runs, verify that:
1. A PR is created for Makefile tool version updates
2. A PR is created for `GO_VERSION` updates in `ci.yml` (grouped with `go.mod` updates)
3. GitHub Actions packages are NOT being updated by Renovate (they should be handled by Dependabot)

To check what Renovate would update, you can manually trigger a dry run:

```bash
# In GitHub Actions, set dryRun: "true" in renovate.yml
# Then click "Run Workflow"
# Check the logs for detected dependencies
```

## Troubleshooting

### Duplicate PRs from both Dependabot and Renovate

If you see duplicate PRs:
1. Check `.github/renovate.json5` for `ignorePaths`
2. Ensure Renovate's `ignorePaths` includes paths managed by Dependabot
3. Consider disabling one bot's ecosystem coverage

### Renovate action failing

1. Check `RENOVATE_TOKEN` is set if required
2. Verify the workflow has required permissions
3. Check GitHub Actions logs for specific error messages

## Manually Triggering Renovate

To manually trigger Renovate:
1. Go to GitHub Actions tab
2. Click "Run workflow" on the "Renovate" workflow
3. (Optional) Select base branch
4. Click "Run workflow"

## Notes

- Renovate PRs follow the pattern `toolchain: ...`
- Go version updates are grouped and have the prefix `go:` (via Renovate grouping)
- Makefile tool updates have the prefix `toolchain:`
- Renovate does NOT enable auto-merge
