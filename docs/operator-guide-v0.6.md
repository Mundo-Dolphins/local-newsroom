# Local Newsroom v0.6 — Operator Guide (Offline-First Site & Social Publication)

Version: v0.6
Status: release guide, verified end-to-end offline
Audience: the person who runs the newsroom and ships its output

This guide walks a human operator through everything v0.6 adds: taking a
READY run and publishing it to a local Hugo site (via a local Git commit on
a dedicated branch) and to Bluesky (via the AT Protocol), with explicit
approval, auditable receipts, and safe resume after partial failure.

Read it top to bottom once; afterwards it is a checklist.

Companion documents: [contracts-v0.6.md](contracts-v0.6.md) (the plan/approval/receipt
contract), [hugo-publication.md](hugo-publication.md) (the staging adapter),
[git-publication.md](git-publication.md) (the committing adapter),
[bluesky-publication.md](bluesky-publication.md) (the social publisher),
[publication-orchestrator.md](publication-orchestrator.md) (the engine).

---

## 1. What v0.6 changes

v0.5 produced a reviewed, deterministic, *local* article or thread. v0.6
adds the first external side effects the newsroom will ever perform:

1. **Plan** — after a READY run, `newsroom publish <run-id>` resolves the
   run's publication preset (from your private editorial bundle) into one
   plan per destination, baselines the exact bytes to a content digest, and
   dry-runs every target locally.
2. **Approve** — nothing is published without an explicit, persisted,
   digest-bound approval. Interactively you answer a yes prompt; in
   automation you confirm the exact artifact digest with `--digest`.
3. **Publish** — the engine commits the staged article to a dedicated branch
   of your Hugo checkout and posts the thread to Bluesky, in order,
   recording a receipt for each external effect.
4. **Resume** — if the network dies halfway, `newsroom publish <run-id>
   --resume` re-executes only what did not finish. Already-published posts
   are never re-posted; already-committed files are never re-committed.

What v0.6 deliberately does **not** do (non-goals):

- It **never pushes, merges, or rewrites** your Git history. The commit is
  local, on a `publication/<target>` branch; reviewing, pushing, and
  merging that branch is *your* job with your normal Git workflow.
- It never runs `hugo server`, never builds the site, and never deploys it.
- It never runs a cron, scheduler, or webhook. Publication is a command you
  type.
- It never clones or pulls the editorial bundle or the site repository.
  Both are plain local directories you keep current yourself.
- It does not edit your content. The bytes it publishes are the deterministic
  re-render of the run's final, check-passed write artifact.

The one-sentence mental model: **the newsroom prepares, rehearses, and asks;
you decide; it acts exactly once per decision; you ship.**

---

## 2. One-time setup

### 2.1 The editorial bundle (your private repository)

A plain local directory — usually a separately checked-out private Git
repository. The engine reads it exactly as it is on disk; it never touches
its remote.

```
<your-bundle>/
  common/
    style.md                 optional: publication-wide style content
    facts-policy.md          optional
  authors/
    javi/
      style.md               optional: author profile content
  publications/
    mundodolphins/
      publication.yaml       the publication preset (required)
      hugo.yaml              optional: Hugo site destination
      bluesky.yaml           optional: Bluesky account destination
```

`publications/mundodolphins/publication.yaml`:

```yaml
profile: javi            # optional: bundle profile (author or dir/author)
language: en             # optional: output language (preset default only)
renderer: hybrid         # optional: article | thread | hybrid
hugo: hugo.yaml          # optional: destination config file (this directory)
bluesky: bluesky.yaml    # optional: destination config file
```

A preset with **no `hugo`** file publishes only to Bluesky; with **no
`bluesky`** file, only to the Hugo site; with both, to both.

> The profile files are optional — a bundle with only `publications/`
> works and runs on built-in defaults. Add them when your voice and style
> notes are ready.

### 2.2 The Hugo destination (a local Git checkout of the site)

You need a **local checkout of your site repository** with a working Git
identity:

```bash
cd ~/sites/mundodolphins
git config user.name "Javi Alvarez"
git config user.email "javi@example.com"   # lands in the commit
git status --porcelain                     # must be clean at publish time
```

`publications/mundodolphins/hugo.yaml`:

```yaml
destination: ~/sites/mundodolphins   # the checkout (absolute, or relative
                                      # to the bundle root — keep it absolute)
base_url: https://www.mundodolphins.es
layout: posts                        # optional: content layout name
drafts: true                         # recommended: publish as draft
```

How the Hugo half works:

- The run's rendered article is staged into the checkout as
  `content/<path>.md` — deterministic path (slug + run date), safe front
  matter, atomic write, never a silent overwrite. Set `drafts: true` (as
  above) to stage as a Hugo draft: the safer habit, since the commit lands
  on a branch you review before merging anyway.
- The Git adapter then requires a **clean tree** (untracked files count as
  dirty unless they are explicitly allowed), stages *only* the exact staged
  file, commits on branch `publication/site` (from the checkout's current
  branch), verifies the commit byte-for-byte, and **stops**.
- The commit message records what was committed, the branch, the run ID,
  and the plan ID — your later `git log` is the audit trail.

If the tree is dirty, publication of that target fails safely (exit 6)
and everything else in the cycle is reported; clean the tree and resume.

### 2.3 The Bluesky destination (an app password, via environment)

1. In your Bluesky account settings, create an **app password** (a
   long-lived posting credential — never use your main account password).
2. Put it in an environment variable, e.g. in your shell profile:

   ```bash
   export BLUESKY_APP_PASSWORD="bsky_app_..."
   ```

3. Reference it by **name only** in the bundle:

   `publications/mundodolphins/bluesky.yaml`:

   ```yaml
   handle: "@mundodolphins"        # quoted: "@" is a reserved YAML indicator
   api_endpoint: https://bsky.social   # optional: default when omitted
   api_key:
     env: BLUESKY_APP_PASSWORD    # env reference required; a literal value
                                  # in this file is a load-time error
   ```

The engine fails closed if the named variable is unset when a publisher is
constructed, and the credential never appears in the plan, the receipts,
the run record, the logs, or any error message — those surfaces are
secret-scanned on the way out. The receipt persists only the account's
`did` fingerprint (`did_sha`), the post URIs, and their CIDs.

> Use a normal `bsky.social` account. Accounts whose identifier is
> `did:key` cannot be persisted in a resumable receipt and are rejected
> for resumable publication.

### 2.4 Pointing the newsroom at the bundle

Any of these (flag beats environment beats config file):

```yaml
# ~/.newsroom.yaml (or $NEWSROOM_CONFIG)
editorial:
  bundle_root: ~/editorial-bundle
  publication: mundodolphins
```

```bash
export NEWSROOM_EDITORIAL_BUNDLE=~/editorial-bundle
export NEWSROOM_PUBLICATION=mundodolphins
# or per invocation:
newsroom run --topic "..." --bundle-root ~/editorial-bundle --publication mundodolphins
```

---

## 3. The daily workflow

### 3.1 Run

```bash
newsroom run --topic "City budget approval and renewable energy growth"
```

As in v0.5, the run must end **READY**: succeeded run, passing final
factual check, rendered output present. The run manifest records the
preset's identity (name, bundle path, hash of the resolved non-secret
values) — private content never does.

### 3.2 Preview (safe — nothing happens)

```bash
newsroom publish <run-id>
```

This is the first thing every publish invocation does: it prepares one plan
per destination, baselines the canonical content (the deterministic
re-render of the run's final write artifact — the run's artifact is the
source of truth, the content files under `publication/content/` are its
snapshot), and dry-runs each target locally with **zero network calls**:

- **Hugo target**: where the file would be staged, its full content digest,
  draft status, and the site URL.
- **Bluesky target**: the post count, per-post lengths, and validation —
  before a single byte goes anywhere.

The preview prints each plan's **artifact digest** (`sha256:…`). Remember
that number: it is what approval binds to. Exit 0.

### 3.3 Approve and publish

**Interactive** (a terminal, you at the keyboard):

```bash
newsroom publish <run-id> --approve
```

Per plan you are shown what will go out and where, and asked `yes/no?`.
A "no" stops that plan (exit 5); the cycle never guesses.

**Non-interactive** (a script, CI-like automation):

```bash
newsroom publish <run-id> --approve --digest sha256:93e9…
```

When stdin is not a terminal, each plan must be confirmed with the **exact**
digest from the preview — one `--digest` per plan, in plan order (Hugo
first, then Bluesky). A mismatched or missing digest is a refusal (exit 5),
never a prompt.

On approval the engine, in order, per plan:

1. Re-reads the baselined content from disk and re-hashes it; any drift
   from the approval's digest fails closed (exit 5) — what is approved is
   exactly what goes out.
2. **Hugo**: stages into the checkout, requires a clean tree, commits on
   `publication/site`, verifies the commit byte-for-byte, records a
   receipt (commit SHA, branch, file list, content digest).
3. **Bluesky**: one session, posts the thread root, then each reply with
   root/parent references; records a receipt with every post URI + CID.

A fully successful invocation exits 0.

### 3.4 Exit codes (the contract)

| Code | Meaning | What you do |
|---|---|---|
| 0 | Preview complete, or the publication fully succeeded | nothing |
| 1 | Unexpected internal error | inspect the output; this is a bug |
| 2 | Usage/configuration error (bad run, unknown target, missing publisher config) | fix the flag or config |
| 3 | The run is **not READY** (failed run, failing final check, missing output) | fix the run first; no plan, no side effects |
| 4 | Nothing to publish (no preset, or no preparable destination) | select `--publication` / fix content prep |
| 5 | Approval missing, declined, or **stale** | approve with `--approve` (and `--digest` non-interactively) |
| 6 | The cycle ran but at least one target failed | `--resume` after fixing the cause |
| 7 | Interrupted (Ctrl-C) mid-cycle | `--resume` |
| 8 | Run or plan not found | check the run ID |

For automation, `--format json` emits exactly one JSON object on stdout
(run, readiness, preset, mode, per-plan status/digest/receipts, exit code)
so scripts can drive the flow: preview → extract digests → approve →
verify.

---

## 4. Partial failure and resume

The realistic failures, and exactly what the newsroom does about them:

**Bluesky dies mid-thread** (rate limit, network drop, 5xx on post 3 of 5):
the target ends `partially_succeeded` — the receipt proves posts 1–2 with
their URIs/CIDs. The plan is not complete (a partially satisfied plan is
*failed* in the plan aggregate: something was not fully done), and the
invocation exits 6.

```bash
newsroom publish <run-id> --resume
```

Resume re-runs the latest plan: finished targets (succeeded Hugo, etc.) are
**never re-executed**; the bluesky target re-logs in, reads its own
receipt, sees posts 1–2 already exist, and publishes **only posts 3–5**,
re-attaching the correct root/parent chain. Post 1 stays the single root —
no duplicate thread.

**Git dirty tree** (someone left `wip.md` in the site checkout): the Hugo
target fails before staging anything (nothing half-committed), the plan is
incomplete, exit 6. Delete the file, `--resume`, done — the resume commits
cleanly because the receipt-based idempotency also covers the Git side: a
target that already committed is never committed twice.

**Interrupted** (you press Ctrl-C, or the process dies between state
transitions): in-flight targets are persisted as aborted/cancelled — the
engine cannot know whether an external effect happened, so it never retries
a *dangling* target; you inspect and resume deliberately. Exit 7.

After a successful resume the plans are terminal and the next section
applies.

---

## 5. Repeated publication is a no-op

```bash
newsroom publish <run-id> --resume        # again
```

A resumed plan whose targets are all terminal is re-reported with its
receipts and **nothing is executed**: no new commit, no new posts, no new
session. Running `newsroom publish <run-id>` on a run whose content already
has a fully succeeded plan says so explicitly ("all plans are already
published") and exits 0. This is what makes the command safe to run in a
loop and safe to re-run by a confused human.

To publish the *same run's content again after it was already published*
is not supported on purpose: same bytes, same receipts = already done.
A **new run** is a new artifact with a new digest and a new plan — that is
the path for re-publishing.

---

## 6. Stale approvals and changed content

The plan's approval is bound to a **content digest**. If the bytes the plan
would publish ever stop matching that digest — you edit the baselined
content file, the run's artifact is re-rendered differently, anything —
the engine fails closed (exit 5, "re-baseline and re-approve") instead of
publishing something a human did not approve.

Recovery is a single command:

```bash
newsroom publish <run-id>
```

A fresh preview re-baselines from the run's canonical content and, when the
bytes changed, creates a **new plan** for them (the stale plan stays in the
run's history for audit) and asks for a new approval. Then approve and
publish as in 3.3. Automation mirrors this: detect exit 5, re-preview,
re-approve with the new `--digest`, publish.

---

## 7. Reading the receipts

Everything the newsroom did is in one file per plan, inside the run:

```
<workspace>/runs/<run-id>/publication/plans/<plan-id>.json
```

Each plan document contains:

- `artifact` — path + `sha256` digest of exactly what was approved;
- `gate` — the readiness evidence at planning time (run status, final
  check, review state, checked-at);
- `targets[]` — per destination: status, the recorded **dry run**
  (outcome + digest + details), the **receipt**, and the full **attempt
  history** (every cycle, with timestamps and errors, redacted);
- `approvals[]` — who approved (principal, method `cli`/`tui`, timestamp,
  the digest the approval covered);
- warnings and a terminal reason on completion.

The receipts carry the external identifiers you diff against the real
world: the Git **commit SHA** + branch + file list + content digest; the
Bluesky **post URIs** + CIDs + account fingerprint. No credentials, ever.

The run manifest stores only a publication *summary* (the latest plan's ID,
path, and document hash, its status, and target counts); the plan
documents are the source of truth.

---

## 8. Shipping the site (the part that stays yours)

After a successful Hugo publish:

```bash
cd ~/sites/mundodolphins
git log publication/site            # the newsroom's commit: message, files
git diff main...publication/site    # review it like any other branch
git checkout main && git merge --no-ff publication/site
git push
```

The newsroom never pushes and never merges — by design, so that a
credential-holding pipeline can never ship to your site unreviewed. The
commit message already says what to review (title, branch, run, plan,
file list). Until you merge, the site's published state is untouched.

Bluesky needs no shipping step: the posts are live once posted, and the
receipt tells you exactly which records to look at (or delete, if you
change your mind).

---

## 9. The security model, in one paragraph

Publication is fail-closed at every layer: the run must be READY or
nothing happens (exit 3); the bytes must hash to the approved digest or
nothing happens (exit 5); the plan must be digest-gated and approval-bound
before any external call; the Git adapter runs a fixed argv with a
minimal environment (no shell, no hooks, no remote commands, clean tree
required, byte-verified commit) and the Bluesky client speaks only the
AT Protocol endpoints you configured; every error, receipt, and log line
passes a secret redactor; and every persisted document is validated
credential-free before it is written. The only secret anywhere is an
environment variable your shell holds, referenced by name.

## 10. Quick reference

```bash
newsroom run --topic "..."                       # 1. produce (v0.5)
newsroom publish <run-id>                        # 2. plan + preview (safe)
newsroom publish <run-id> --approve              # 3. approve (prompt) or
newsroom publish <run-id> --approve --digest sha256:…   #   (automation)
newsroom publish <run-id> --resume               # 4. recover / no-op
newsroom publish <run-id> --target hugo          # limit to one destination
newsroom publish <run-id> --format json          # machine-readable report
# then: git review + merge + push the publication/site branch (yours)
```
