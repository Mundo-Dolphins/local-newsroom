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
//	store := workspace.New(root, sanitizer)          // implements publish.Store
//	eng := publish.New(store)
//	plan, _ := eng.CreatePlan(ctx, runID, req, bundle)   // plan + gate
//	plan, _ = eng.Approve(ctx, runID, plan.ID, "human", bundle)
//	plan, _ = eng.Publish(ctx, runID, publishers, publish.Options{})
//
// workspace.Store satisfies Store, so real callers and tests share one
// implementation of the persistence boundary. Concrete publishers are
// bound per target: the Bluesky and git bridges (NewBlueskyPublisher,
// NewGitPublisher) adapt the existing adapters; a PublisherFunc is the
// escape hatch for anything else.
package publish
