// Package publish implements the v0.6 persistence and idempotent-resume
// engine for controlled publication: it persists publication plans
// (internal/publication) as versioned documents in the run directory,
// tracks them on the run manifest (schema 0.6.0, publication_state), and
// drives publication cycles so that external side effects are never
// blindly repeated.
//
// Publication is a post-run state, not a pipeline stage. The run's
// canonical layout (v0.5) gains one new directory:
//
//	<run dir>/
//	  manifest.json            ← gains an optional publication_state summary
//	  publication/
//	    plans/<plan-id>.json   ← versioned plan documents (schema 0.6.0 of
//	    │                        internal/publication), one file per plan;
//	    │                        the run keeps its full plan history
//	    content/<name>         ← approved content files a plan is baselined
//	                           to (e.g. the canonical thread text)
//
// # Invariants
//
// Every state change the engine makes is persisted immediately after it
// happens (atomic store writes through the workspace sanitization boundary),
// so the invariants below hold across process crashes, restarts, and
// repeated invocations:
//
//   - A target that succeeded is never executed again; its receipt is the
//     proof, and the engine skips it (Plan.AuthorizesPublish refuses it).
//   - A changed artifact digest invalidates previous approvals and cannot
//     reuse an old receipt (Plan.AuthorizesPublish and SucceedTarget bind
//     the publisher's output digest to the plan's artifact digest).
//   - A crashed mid-cycle plan is resumed by aborting its dangling in-flight
//     targets, then re-executing only the targets that did not finish.
//   - A publisher is invoked for a target only after Plan.AuthorizesPublish
//     passes: an explicit human approval, bound to the exact artifact
//     digest, and still matching the bytes about to go out.
//   - A corrupted or tampered plan document fails closed: it is never
//     executed and never "recovered" by re-pushing content.
//   - No bytes ever reach disk without passing the store's sanitization
//     boundary, exactly as for every other persisted run file.
//
// # Usage
//
// The Orchestrator is the primary entry point: it drives the full pipeline
// (ready check, plan, dry-run/preview, approval, publish, summary) for one
// run and presents every human-facing projection as data — it has no
// interactive UI, and the approval is a domain input:
//
//	store := workspace.New(root, sanitizer)          // implements publish.Store
//	eng := publish.New(store)
//	var bundle review.Bundle                         // the run's review evidence
//	orch := publish.NewOrchestrator(eng, runID, &bundle)
//	orch.SetPublishers(map[publication.TargetID]publish.Publisher{
//		"site":   publish.NewGitPublisher(gitPub, planID, "site", path, title, runID),
//		"thread": publish.NewBlueskyPublisher(blueskyPub, planID, "thread"),
//	})
//	ready, _   := orch.Ready(ctx, req)               // the pre-publication gate
//	plan, _    := orch.Prepare(ctx, req)             // the plan (reused if active)
//	screen, _  := orch.Preview(ctx, plan.ID)         // the approval screen's data
//	// ... the presenting surface collects the human decision ...
//	plan, _, _ = orch.Approve(ctx, plan.ID, publish.Approval{ApprovedBy: "editor"})
//	summary, _ := orch.Publish(ctx, plan.ID)         // the final summary
//
// The Engine is the layer underneath: CreatePlan, ApproveWith, Publish, and
// the state mutations (RecordDryRun, RevokeApproval, Cancel, ...) for
// callers that want the plan lifecycle directly. workspace.Store satisfies
// Store, so real callers and tests share one implementation of the
// persistence boundary. Concrete publishers are bound per target: the
// Bluesky and git bridges (NewBlueskyPublisher, NewGitPublisher) adapt the
// existing adapters and also implement Previewer, so the approval screen
// gets the adapter's own facts (branch, path, post count) from the preview;
// a PublisherFunc is the escape hatch for anything else.
package publish
