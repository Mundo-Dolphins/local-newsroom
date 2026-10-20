# v0.6 Publication Orchestrator

Issue [#120](https://github.com/Mundo-Dolphins/local-newsroom/issues/120)
closes the loop between the v0.5 run pipeline and the external world. The
contract ([contracts-v0.6.md](contracts-v0.6.md)) defines the plan document;
the persistence layer (#119) defines the engine; the adapters
([git-publication.md](git-publication.md),
[bluesky-publication.md](bluesky-publication.md)) define what a target can
do. This document defines the **orchestrator**: the component that runs the
whole pipeline for a run, and the safety rules that keep it from ever
publishing by accident.

```
ready check ──► plan ──► dry-run/preview ──► human approval ──► execute ──► summary
   (gate)      (engine)   (publish.Previewer)   (domain input)   (engine)    (projection)
```

The orchestrator lives in `internal/publish` (package `publish`, type
`Orchestrator`): it wraps the engine's plan / approve / publish calls and
projects the plan document into the two human-facing data structures —
`ApprovalSummary` (what a human must review) and `Summary` (what happened).

## UI independence

The orchestrator has **no interactive UI**. It never prompts, never renders,
never reads or writes a terminal. Everything it produces is plain data; the
one decision it needs — the approval — comes back in as a domain value
(`publish.Approval`): who decided, on which surface, which digest they
confirmed, and their own words. The presenting surface (the `newsroom
publish` CLI in #121, the TUI in #122, or an operator with a pen) collects
that decision and calls `Approve`.

Because there is no UI event the orchestrator accepts, publication can never
fire accidentally from one: a stray keypress, a rendered button, or a TUI
render path cannot publish — only an explicit, validated, persisted approval
of the exact bytes can. The same safe pipeline therefore works from any
surface without the surface understanding publication.

## The pipeline

### 1. Ready check (`Orchestrator.Ready`)

Evaluates the pre-publication gate from the current on-disk content: the
run succeeded, the final factual check passed, and the review bundle reports
READY — the same verdict the rendered review bundle shows a human
(`review.Readiness`). A missing artifact file is reported as not-ready
(`ContentMissing`), not as an error. `Ready` performs no state change.

### 2. Plan (`Orchestrator.Prepare`)

Creates the plan for the requested artifact and targets — or returns the
run's existing active (non-terminal) plan for the same artifact unchanged,
so a repeated prepare never fragments one pending approval into two
competing documents. A run whose latest plan already ended prepares a fresh
plan: publishing again is a new plan with a new approval, per the contract.
Preparation fails closed (`ErrGateFailed`) unless the run is READY.

### 3. Dry-run preview (`Orchestrator.Preview`)

Runs each target's publisher rehearsal and returns the `ApprovalSummary`.
The approval screen shows, at minimum, everything the contract requires:

| Shown on the approval screen | Carried by |
|---|---|
| run and topic | `RunID`, `Topic` |
| the artifact's exact content hash | `Digest` (the plan's artifact digest) |
| every destination | `Targets[].Destination` (+ type, provider, dependency) |
| the exact Hugo/Git branch and path | `Targets[].Preview.Metadata["branch"] / ["path"]` (the git bridge) |
| the exact Bluesky post count | `Targets[].Preview.Metadata["post_count"]` (the bluesky bridge) |
| the dry-run preview | `Targets[].Preview` (verdict, report, issues) |
| the warnings | `Warnings` |
| the recorded approvals (and which one governs) | `Approvals[]` (+ `Effective`) |

The previews are **recomputed at preview time** (never served stale) and
recorded on the plan document where the plan's state allows (planned or
approved, content matching the baseline): the persisted `DryRunResult` is
the audit trail of what each adapter validated. A rehearsal failure (a
dirty tree, an oversized post, a malformed thread) is a *reported* rehearsal
— `Preview.OK = false` with its issues — not an error and not a veto:
surfacing the problem and deciding whether to proceed is the human's call.
A rehearsal that cannot run at all (adapter down) is a warning, and a
publisher that is not bound at all is reported before anything can run.
The adapters' dry runs have zero side effects: the Bluesky rehearsal is
local validation with no network; the git rehearsal writes, stages,
switches, and commits nothing.

### 4. Approval (`Orchestrator.Approve`)

Records the human's decision, as data. Before the engine persists it, the
orchestrator re-verifies the binding: the approver identity is present, the
optional target scope names real targets, and the digest the human confirmed
matches the plan's artifact digest (approving different bytes than the plan
shows is not an approval of this plan — `ErrStaleApproval`). The engine
then re-reads the content, re-gathers the gate evidence from the same
bytes, records the approval bound to the exact digest on disk, and
persists the plan. The returned summary reflects the post-approval state.

### 5. Publish (`Orchestrator.Publish`)

Runs the engine's publication cycle for the plan (or resumes one):
dependencies in declaration order, a receipt persisted immediately after
every successful external action, the context observed between actions, and
the plan persisted after every state transition. The orchestrator adds the
report: a `Summary` on completion, and — on a refusal or an interruption —
the same `Summary` of the persisted state together with the error, so a
caller that was refused or cancelled still sees where the run stands.

A successfully published plan publishes nothing further: a repeated
`Publish` is an exact no-op that returns the existing receipts. Nothing that
already holds a receipt is ever re-executed, and a target whose content the
adapter already committed or posted is confirmed, not duplicated.

## Partial failure, interruption, and what a human does next

The `Summary` is a pure projection of the persisted plan, and it is the
remediation surface. Its rules:

- **Never delete.** The orchestrator has no deletion capability at all, and
  the remediation it offers never proposes deleting what is already public.
  A later failure is not rolled back by taking down an earlier success:
  the receipts name what exists where (external URIs and record IDs, the
  content digest), and the steps tell a human how to verify, complete, or —
  their own decision, by their own tools — take something down.
- **`verify`** — an interrupted attempt (a cancelled context, a crash) left
  an unproven external outcome: check the destination manually and keep
  what is found.
- **`complete`** — some of the action is already public (a partially posted
  thread): re-run publish; it resumes from the receipt and the public part
  stays exactly where it is.
- **`retry`** — the action failed with nothing proven: re-run publish; the
  retry starts from scratch, so there is no external state to clean up.
- **`push`** — a git target succeeded: the commit exists on the dedicated
  branch, locally. Pushing and merging it is a **user-controlled step
  outside the engine** (the engine never pushes, merges, or rewrites
  history) — the summary names the commit and the branch, and the human
  does it. This is the dependency the ticket describes: the engine commits,
  the human pushes/merges, and only then does the downstream social target
  make sense — while the dependency graph itself stays explicit and
  opt-in: not every target depends on every other one.
- **`replan`** — the plan ended without completing its targets, or was
  cancelled: publishing the artifact again is a new plan with a fresh
  approval.

An interrupted cycle is explicit in the summary (`Interrupted = true`, the
cause in `Reason`): the plan is left in `publishing` with its in-flight
targets aborted, it is the resume point, and a further `Publish` with a
healthy context resumes it — succeeded targets stay succeeded, interrupted
ones must be verified first.

## Dependency semantics

Targets execute in declaration order under their explicit `DependsOn`
edges. The orchestrator does not assume any target is dependent on another
one: the standard newsroom shape is the site commit (a local, engine-side
effect) followed by the social post, with the optional user-controlled
push/merge *between* them — and a target with no dependencies runs
whenever the cycle runs, whatever its independent siblings do. A target
whose dependency did not complete is blocked (recorded as failed with
`dependency-incomplete`, never executed), and the summary points at the
dependency that must be fixed.

## API at a glance

```go
orch := publish.NewOrchestrator(eng, runID, bundle)
orch.SetPublishers(map[publication.TargetID]publish.Publisher{
    "site":   publish.NewGitPublisher(gitPub, planID, "site", relPath, title, runID),
    "thread": publish.NewBlueskyPublisher(blueskyPub, planID, "thread"),
})

ready, _    := orch.Ready(ctx, req)        // gate: run+final check+review
plan, _     := orch.Prepare(ctx, req)      // plan (reused while active)
screen, _   := orch.Preview(ctx, plan.ID)  // ApprovalSummary for the surface
// ... the surface presents screen and collects the decision ...
plan, _, _  = orch.Approve(ctx, plan.ID, publish.Approval{
    ApprovedBy: "editor@example.com",
    Method:     publication.ApprovalMethodCLI,
    Note:       "looks right",
    DigestConfirmed: screen.Digest,        // or "" for the plan's digest
})
summary, err := orch.Publish(ctx, plan.ID) // Summary; err ≠ nil ⇒ refused/interrupted
summary, _   = orch.Summary(ctx, plan.ID)  // pure projection, no execution
```

The real adapters implement `publish.Previewer`, so the approval screen's
adapter-specific facts (branch, path, post count) come from the adapters
themselves — the orchestrator is provider-agnostic by construction.

## What the orchestrator is not

- **Not a pipeline stage.** Publication remains a post-run state on the
  v0.5 stage graph; the orchestrator is invoked after the run finished and
  the review bundle exists.
- **Not a UI.** No prompts, no rendering, no TTY. Presentation is a separate
  concern (#121 CLI, #122 TUI) that consumes the two projections.
- **Not a rollback engine.** It has no deletion capability and its
  remediation never proposes one. External content outlives failures;
  humans reconcile it.
- **Not a scheduler.** It does not push, merge, or retry on timers. Every
  action is a call from a program with the plan's approval intact; the
  push/merge half of the site story is the user's own step.

## Tests

`internal/publish` (orchestrator suite, all publishers faked with
world-like semantics — accumulating, never deleting): the full pipeline
including duplicate-invocation safety; refusal without a passing final
check; refusal without an approval; stale-approval and
artifact-changed fail-closed behavior; ordered and independent targets;
partial failure with receipt-bound resume across a process restart;
cancellation mid-cycle with safe resume; dependency cascade; preview
facts (adapter metadata, persisted dry runs, unbound/unpreviewable
targets, failed rehearsals surfaced without vetoing the human); and the
real adapter bridges' rehearsals (Bluesky: zero network; git: zero
mutation, dirty-tree blockers, idempotent-skip prediction).
