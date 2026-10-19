# Git Publication Package (v0.6)

`internal/gitpub` is the Git publication adapter. It turns a set of staged
site files (for example the content file
[`internal/hugo`](hugo-publication.md) writes) into a **local commit on a
dedicated branch** inside an existing checkout, and returns a
`PublicationReceipt` naming the commit SHA and the branch — with no push,
no merge, and no force operation of any kind. The engine stops at a
reviewable local change; the human inspects (`git log`, `git diff`) and
pushes by their own decision.

It closes [Mundo-Dolphins/local-newsroom#117](https://github.com/Mundo-Dolphins/local-newsroom/issues/117).

## What It Is / What It Is Not

| Is | Is not |
|---|---|
| A concrete *committing* adapter: approved files → staged bytes → one commit on a dedicated branch, receipt with commit SHA + branch | A deployer: no push, no fetch, no merge, no rebase, no reset, no clean, no force, no branch deletion or rewind |
| A tight boundary around the installed `git` executable: fixed argv, isolated environment, hooks disabled, documented exit codes | A re-implementation of Git: it asks git for state (status, rev-parse, diff) and verifies git's own output after the commit |
| Plan-aware: it produces `internal/publication` receipts (external record ID = commit SHA, branch in metadata, digest-bound content) and dry-run records | A plan orchestrator: it consumes a validated request, it does not drive plan state transitions |
| Format-agnostic: it writes paths + bytes; a Hugo publisher passes the staged content file | A Hugo package: it does not import `internal/hugo` (the caller bridges them) |

## The Publication

```
checkout (the site's git repository)
└── content/news/20260711-slug.md      ← the staged article (path + exact bytes)
```

```
master ──────────────────────────────┐
                                     │
        publication/site ◄───────────┘   (created or reused; the commit lands here)
```

One `Publish` call:

1. validates the request (IDs, file bounds, containment, branch name, commit
   message) and the repository state (clean tree, attached HEAD, identity) —
   in a dry run, blockers are *reported* in `Result.Issues` instead of
   failing;
2. creates (or switches to) the dedicated branch;
3. writes each file atomically (temp + fsync + chmod 0644 + rename; created
   directories are 0755);
4. stages **exactly** the requested files (`git add -- ':(literal)…'`);
5. refuses to commit if the index holds staged changes outside those files
   (it never unstages work the operator staged);
6. commits with the rendered message and the resolved identity — or, when
   the index is byte-identical to the branch (an exact retry), skips the
   commit and points the receipt at the existing one;
7. verifies the commit: its changed-path set equals exactly the staged files,
   every committed blob is byte-identical to the approved content, and the
   commit sits directly on the pre-publication HEAD;
8. returns the `Result` (and builds a `PublicationReceipt` from it).

The working tree is left **on the publication branch**: the commit is a
reviewable local change, and the operator — not the engine — pushes.

## Configuration Surface

`gitpub.Config` (validated by `New`; all defaults applied):

| Field | Default | Meaning |
|---|---|---|
| `RepoRoot` | required | The existing checkout, or a directory inside it (e.g. the Hugo project root when the site is a subdirectory). Must be inside a git working tree; resolved against the git toplevel |
| `GitPath` | `"git"` | The git executable (must be ≥ 2.34: literal pathspecs) |
| `TargetDir` | `"."` | Directory, relative to `RepoRoot`, where files land (e.g. `"content"`); proven inside the checkout, symlinks resolved |
| `Branch` | `publication/<target-ID>` | The dedicated publication branch; checked against `git check-ref-format --branch` |
| `CommitMessage` | built-in template | Go `text/template` over `{PlanID, RunID, TargetID, Title, Branch, BaseBranch, FileCount, Files}`; must render non-empty, ≤ 8 KiB |
| `CommittedName` / `CommittedEmail` | `""` → repo-local config | The committer identity, applied via per-invocation `-c` flags (never persisted); with both empty, the repository-local `user.name`/`user.email` is read, and a missing identity is an error |
| `AllowedDirtyPaths` | `{}` | The explicit safe policy for a non-clean tree: checkout-relative paths (exact or directory prefix) the operator expects to be dirty. With no entries, **any** dirty state fails. `.` (allow everything) is rejected |

## Request and Result

`gitpub.Request`: `PlanID` and `TargetID` (validated plan-contract
identifiers), `Title` (commit message; falls back to the first file's base
name), `RunID`, `Files` (1–64 files, path relative to `TargetDir` + exact
bytes, each ≤ 4 MiB, unique, contained in the checkout), and `DryRun`.

`gitpub.Result`: `CheckoutRoot`, `BaseBranch` (where the run started),
`Branch`, `BranchExisted` / `BranchCreated`, `CommitSHA`, `CommitMessage`,
`CommitTime` (the commit's own timestamp), per-file detail (`Relative`,
`Absolute`, `Digest`, `OnDisk`, `Unchanged`, `AtHead`), `Skipped` (an
idempotent retry found the exact content already committed), `WouldSkip`
(dry-run prediction of that), `RemainingDirty` (allowed-policy paths that
are still dirty), `Issues` (dry-run blockers), and `Digest` — the canonical
digest of the file contents NUL-joined in path order (for a single file,
exactly the file's own digest, so a plan rebaselined to the staged file
binds).

```go
pub, err := gitpub.New(ctx, cfg)   // validates config + checkout, no mutation
res, err := pub.Publish(ctx, req)  // real run, or req.DryRun = true
rec, err := res.Receipt(planID, targetID) // commit SHA + branch, digest-bound
dr   := res.ToDryRunResult(at)     // plan-contract rehearsal record
defer pub.Close()
```

## Safety Model

Fail closed, at every boundary:

- **Working tree.** Must be clean. The only exception is the explicit
  `AllowedDirtyPaths` policy; every other modified/staged/untracked/renamed
  path fails the run (`ErrDirtyTree` names the offenders) before anything is
  written. Dirty state is re-checked after the branch switch.
- **Staged index.** Entries staged by someone else are refused at commit
  time (`ErrStagedForeign` names them) — unrelated content never rides
  along — and are left exactly as found (never unstaged, never committed).
- **Files.** Only the requested paths are written (atomic) and staged
  (`git add -- ':(literal)path'` — literal pathspecs so a generated file
  named `:top.md` or `--x` is taken exactly literally). Paths are validated
  (relative, no NUL/backslash, no traversal) and proven inside the checkout
  **with symlinks resolved**, both at config time (target dir) and request
  time (every file).
- **Identity and config.** No git config file is written anywhere. Every git
  process runs with an isolated environment: empty global config, system
  config disabled, no `GIT_DIR`/`GIT_WORK_TREE` inheritance, hooks disabled
  (`-c core.hooksPath=<empty dir>` on every call — no hook of any kind can
  execute), no terminal prompts. The committer identity is per-invocation
  (`-c user.name / user.email`) or read from the repository-local config;
  the operator's global identity is never consulted and never leaked into a
  commit.
- **Verification.** After the commit, the adapter re-reads git's own
  output: the changed-path set must equal exactly the staged publication
  paths (renames are diffed with `--no-renames`, so any foreign path shows
  up), every committed blob must be byte-identical to the approved content
  (a clean/smudge filter the site repo defines — which *does* run, with the
  inherited PATH, exactly as in the operator's own git — that alters bytes
  fails `ErrVerification`), and the commit must sit directly on the
  pre-publication HEAD.
- **Remotes.** No push, fetch, or remote operation exists in the adapter;
  the working tree ends on the publication branch for human review.

Git is invoked with a fixed argv (no shell), a bounded and redacted error
surface, and an allowlisted set of subcommands (`--version`, `rev-parse`,
`symbolic-ref`, `status`, `check-ref-format`, `config`, `checkout`, `add`,
`diff`, `commit`, `show`, `cat-file`, `log`).

## Idempotency

Retrying the same publication rewrites byte-identical files, stages them,
and finds an index that is already equal to the publication branch's HEAD:
**no new commit is created** (`Result.Skipped`), the receipt points at the
existing commit with its original timestamp, and the retry re-derives the
same receipt ID — history is never duplicated. Changed content produces a
fresh commit on the same branch.

## Errors

`ErrInvalidConfig`, `ErrNotAGitRepo`, `ErrTargetOutsideRepo`,
`ErrDirtyTree` (`*DirtyTreeError` with offenders), `ErrDetachedHead`,
`ErrNoIdentity`, `ErrInvalidRequest`, `ErrBranch`, `ErrGitCommand`
(redacted, bounded), `ErrStagedForeign` (`*ForeignStagedError` with paths),
`ErrVerification`, `ErrDryRunReceipt`. All match via `errors.Is`.

## Tests

`internal/gitpub` is covered by a suite over **temporary real git
repositories** (`git init` in `t.TempDir`; fixtures run git through the same
config isolation so a machine identity never leaks in): clean-repo publish
(branch, exact commit content, bytes, perms, receipt, **no push to a bare
remote**, clean tree after), dirty-unrelated failure with no side effects,
the allowed-dirty policy (file and directory granularity), foreign staged
refusal, idempotent retry + changed content, dry-run (no mutation of any
kind, skip prediction, blocker issues), detached HEAD, missing identity,
command failure (missing and fake git binaries, version floor), unsafe paths
(traversal, backslash, absolute, dot, NUL; symlink file and target-dir
escape), branch reuse, unborn repo (root commit), Unicode paths, a
repo-local tamper filter caught by post-commit verification, receipt field
contract, and plumbing unit tests (porcelain `-z` parsing, C-unquoting,
version parsing, path validation, message rendering). Package coverage is
tracked against the repo's 80% floor via `make check` / `make coverage`.
