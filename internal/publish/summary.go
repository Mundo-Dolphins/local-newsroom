package publish

import (
	"fmt"
	"sort"

	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// Summary is the projection of a publication plan into its outcome: what
// happened per target (with its receipts), where the plan stands, and —
// for anything that did not fully complete — what a human must do next.
//
// It is pure data: the presentation layer (CLI, TUI, or an operator reading
// the persisted plan document) renders it. Two properties distinguish it
// from the plan itself:
//
//   - Remediation is explicit. Every unfinished target (or an interrupted
//     cycle) produces one or more manual steps, and the steps are the only
//     "fix" the system offers. The orchestrator never executes remediation,
//     and it has no deletion capability at all: content that is already
//     public stays public. A later failure is never rolled back by deleting
//     what an earlier success published; the receipts name what exists
//     where, and the remediation tells a human how to verify, complete, or
//     (their own decision, by their own tools) take it down.
//   - Interrupted is explicit. A cycle that stopped mid-flight (a
//     cancelled context, a crash) is not a target failure: the plan is
//     resumable, and the summary says so with the cause.
type Summary struct {
	// RunID / Topic / Workspace identify the run the publication belongs
	// to (the "run/topic" every report leads with).
	RunID     run.RunID
	Topic     string
	Workspace string

	// PlanID and Status are the plan's identity and lifecycle state.
	PlanID publication.PublicationID
	Status publication.PublicationStatus

	// Interrupted is true when the plan's last cycle was interrupted
	// before completion (context cancellation or a state-recording
	// failure): the plan is in publishing, its in-flight targets were
	// aborted, and a further Publish resumes it.
	Interrupted bool

	// Reason is the plan's terminal reason, or the interruption cause
	// (when Interrupted). "" otherwise.
	Reason string

	// Artifact is the plan's content identity (path, digest, size): what
	// the publication covers.
	Artifact publication.ArtifactIdentity

	// Targets is the per-target state in declaration order: status,
	// receipt, error, attempt count.
	Targets []TargetSummary

	// Remediation is the ordered list of manual steps a human should take
	// (empty when the plan fully succeeded with nothing pending).
	Remediation []Remediation

	// Warnings and Errors are the plan's own logs (already redacted).
	Warnings []run.Warning
	Errors   []run.ErrorSummary
}

// RemediationKind classifies one manual step.
type RemediationKind string

// Remediation kinds, in the vocabulary the summary uses:
//
//   - verify: the external state of an interrupted or partially proven
//     action is unproven; a human must check the destination (and leave
//     anything they find in place) before anything else;
//   - complete: some of the action is already public; re-running publish
//     finishes the remainder (resuming from the receipt), and the public
//     part stays;
//   - retry: the action failed with nothing proven (or was blocked on a
//     dependency); re-running publish retries it safely from scratch;
//   - push: a local git commit exists on a dedicated branch; pushing and
//     merging it is a user-controlled step outside the engine (the engine
//     never pushes, merges, or rewrites history);
//   - replan: the plan ended without completing its targets; publishing
//     the artifact again is a new plan with a fresh approval.
const (
	RemediationVerify   RemediationKind = "verify"
	RemediationComplete RemediationKind = "complete"
	RemediationRetry    RemediationKind = "retry"
	RemediationPush     RemediationKind = "push"
	RemediationReplan   RemediationKind = "replan"
)

// Remediation is one manual step for a human. The fields are display
// material: the presentation layer renders them, and nothing in the
// engine or orchestrator executes them.
type Remediation struct {
	// Kind is the class of the step (verify, complete, retry, push,
	// replan).
	Kind RemediationKind

	// TargetID names the target the step is about ("" for plan-level
	// steps).
	TargetID publication.TargetID

	// Action is one imperative sentence: what the human does.
	Action string

	// Detail carries the references the step needs: external record
	// identifiers, the content digest, the branch, the dependency that
	// blocked the target. Always redacted.
	Detail string

	// URI is the external location to check, when one is known (a
	// published post URL, a page URL). "" otherwise.
	URI string

	// RecordID is the platform's own record identifier to check (a commit
	// SHA, a post record ID). "" otherwise.
	RecordID string
}

// Summarize projects a persisted plan into a Summary. It is a pure
// projection: it changes no state, and it works for every plan status —
// including a crashed mid-cycle plan (the resume report a restarted process
// shows before the human decides).
func Summarize(m *run.Manifest, plan *publication.Plan) *Summary {
	s := &Summary{
		RunID:     plan.RunID,
		Topic:     m.Topic,
		Workspace: plan.Workspace,
		PlanID:    plan.ID,
		Status:    plan.Status,
		Artifact:  plan.Artifact,
		Targets:   make([]TargetSummary, 0, len(plan.Targets)),
	}
	if m.Topic == "" {
		s.Topic = string(m.ID)
	}

	// The plan's own interrupt marker (the cycle-interrupted warning)
	// carries the cause; a plan in publishing with that warning was
	// interrupted, not merely in progress.
	if plan.Status == publication.StatusPublishing {
		for _, w := range plan.Warnings {
			if w.Code == "cycle-interrupted" {
				s.Interrupted = true
				s.Reason = w.Message
			}
		}
	}
	if plan.Status.Terminal() && plan.TerminalReason != "" {
		s.Reason = plan.TerminalReason
	}
	s.Warnings = append([]run.Warning(nil), plan.Warnings...)
	s.Errors = append([]run.ErrorSummary(nil), plan.Errors...)

	for _, t := range plan.Targets {
		ts := TargetSummary{
			ID:          t.ID,
			Name:        t.Name,
			Type:        t.Type,
			Provider:    t.Provider,
			Destination: t.Destination,
			DependsOn:   append([]publication.TargetID(nil), t.DependsOn...),
			Status:      t.Status,
			SkipReason:  t.SkipReason,
			Receipt:     t.Receipt,
			Error:       t.Error,
			Attempts:    len(t.Attempts),
		}
		if t.DryRun != nil {
			ts.Preview = &Preview{
				OK:      t.DryRun.Outcome == publication.DryRunOutcomeOK,
				Details: t.DryRun.Details,
				Issues:  t.DryRun.Issues,
			}
		}
		s.Targets = append(s.Targets, ts)
	}

	s.Remediation = remediationFor(m, plan)
	return s
}

// remediationFor derives the manual steps from the persisted plan state.
//
// The rules (one rule per unfinished shape; succeeded-and-skipped targets
// produce no steps):
//
//   - a cancelled target (an interrupted attempt, or a human cancel) has an
//     unproven external outcome: verify the destination, and keep whatever
//     is found — nothing is deleted;
//   - a partially succeeded target already holds public records: re-run
//     publish to complete the remainder from the receipt, keep the public
//     part;
//   - a failed target with no proven receipt: re-run publish; nothing was
//     proven published, so the retry starts clean;
//   - a failed target whose error is an incomplete dependency: the
//     dependency runs first — fix it, then retry;
//   - a failed target that was blocked on authorization or the gate: the
//     fix is a fresh approval / a READY run, not a re-run;
//   - a succeeded git target: the commit is local; pushing and merging is
//     the user's own step (the engine never pushes);
//   - a partially completed or interrupted plan: one plan-level step
//     stating that the published part stays public and the rest resumes
//     with a re-run.
func remediationFor(m *run.Manifest, plan *publication.Plan) []Remediation {
	var out []Remediation

	addTarget := func(id publication.TargetID, kind RemediationKind, action, detail, uri, record string) {
		out = append(out, Remediation{
			Kind:     kind,
			TargetID: id,
			Action:   run.RedactSecrets(action),
			Detail:   run.RedactSecrets(detail),
			URI:      uri,
			RecordID: record,
		})
	}

	for _, t := range plan.Targets {
		switch t.Status {
		case publication.StatusCancelled:
			uri, record := "", ""
			if t.Receipt != nil {
				uri, record = t.Receipt.ExternalURI, t.Receipt.ExternalRecordID
			}
			action := fmt.Sprintf("verify destination %q of target %q: the attempt was interrupted and its external outcome is unproven",
				t.Destination, t.ID)
			detail := "check the destination manually for a partial publication; if something is live, leave it in place (the engine never deletes public content) and re-run publish only after reconciling"
			if uri != "" || record != "" {
				action = fmt.Sprintf("verify target %q: the attempt was interrupted with a partially proven receipt", t.ID)
				detail = fmt.Sprintf("a prior attempt proved record %s; verify it at %s, keep it, and re-run publish to resume the remainder", recordOr("<none>", record), uriOr("<the destination>", uri))
			}
			addTarget(t.ID, RemediationVerify, action, detail, uri, record)

		case publication.StatusPartiallySucceeded:
			uri, record := "", ""
			if t.Receipt != nil {
				uri, record = t.Receipt.ExternalURI, t.Receipt.ExternalRecordID
			}
			detail := fmt.Sprintf("the persisted receipt proves %s (digest %s); re-run publish to complete the remainder from that receipt — the published records stay exactly where they are",
				recordOr("<nothing>", record), t.Receipt.ContentDigest)
			if t.Receipt == nil {
				detail = "re-run publish to complete the target; the published part stays exactly where it is"
			}
			addTarget(t.ID, RemediationComplete,
				fmt.Sprintf("complete target %q: part of it is already public", t.ID),
				detail, uri, record)

		case publication.StatusFailed:
			switch {
			case t.Error != nil && t.Error.Code == "dependency-incomplete":
				addTarget(t.ID, RemediationRetry,
					fmt.Sprintf("target %q was blocked: its dependency did not complete", t.ID),
					fmt.Sprintf("the recorded failure is %q; complete the dependency, then re-run publish (this target never ran, so nothing to clean up)", t.Error.Message), "", "")

				// A failed dependency chain: point at the failed input.
				for _, dep := range t.DependsOn {
					if d, ok := plan.Target(dep); ok && (d.Status == publication.StatusFailed || d.Status == publication.StatusCancelled) {
						addTarget(dep, RemediationRetry,
							fmt.Sprintf("fix target %q before %q can run", dep, t.ID),
							"it is the incomplete dependency; once it succeeds, a re-run publish executes the dependent", "", "")
					}
				}
			case t.Error != nil && (t.Error.Code == "authorization-failed" || t.Error.Code == "gate-not-passing"):
				addTarget(t.ID, RemediationReplan,
					fmt.Sprintf("target %q could not be authorized", t.ID),
					fmt.Sprintf("the recorded failure is %q; restore a passing gate (run succeeded, final check pass, review READY) and a fresh approval bound to the current artifact, then re-run", t.Error.Message), "", "")
			case t.Receipt != nil:
				addTarget(t.ID, RemediationComplete,
					fmt.Sprintf("reconcile target %q: the last attempt failed after an earlier attempt proved %s", t.ID, t.Receipt.ExternalRecordID),
					fmt.Sprintf("verify the proven record (digest %s) is still what you want public; it stays in place — re-run publish to resume from it", t.Receipt.ContentDigest),
					t.Receipt.ExternalURI, t.Receipt.ExternalRecordID)
			default:
				addTarget(t.ID, RemediationRetry,
					fmt.Sprintf("re-run publish: target %q failed with nothing proven published", t.ID),
					"the retry starts from scratch; there is no external state to clean up, and nothing was published that could be lost", "", "")
			}

		case publication.StatusSucceeded:
			// Local git commits need their user-controlled push/merge. The
			// engine stops at the commit: pushing, merging, or force
			// anything else is outside it, and the summary names the step
			// a human must take for the site to actually update.
			if t.Provider == "git" && t.Receipt != nil && t.Receipt.ExternalRecordID != "" {
				branch := ""
				if t.Receipt.Metadata != nil {
					branch = t.Receipt.Metadata["branch"]
				}
				detail := fmt.Sprintf("commit %s%s", t.Receipt.ExternalRecordID, branchSuffix(branch))
				if t.Destination != "" {
					detail += "; the destination is " + t.Destination
				}
				addTarget(t.ID, RemediationPush,
					fmt.Sprintf("push or merge %s manually: the commit is local to the checkout", t.ID),
					detail,
					"", t.Receipt.ExternalRecordID)
			}

		default: // succeeded is handled above; skipped needs nothing.
		}
	}

	// Plan-level steps: what the whole cycle means for a human.
	switch plan.Status {
	case publication.StatusPartiallySucceeded, publication.StatusFailed:
		done, pending := 0, 0
		for _, t := range plan.Targets {
			if t.Status == publication.StatusSucceeded {
				done++
			} else if t.Status != publication.StatusSkipped {
				pending++
			}
		}
		out = append(out, Remediation{
			Kind:   RemediationComplete,
			Action: fmt.Sprintf("%d of %d targets are public; %d still need attention", done, len(plan.Targets), pending),
			Detail: "the published part stays exactly where it is (no automatic rollback or deletion); take the per-target steps above, then re-run publish to resume the remainder",
		})
	case publication.StatusCancelled:
		out = append(out, Remediation{
			Kind:   RemediationReplan,
			Action: "the plan was cancelled by a human",
			Detail: fmt.Sprintf("reason: %s; anything published before the cancellation stays live, and re-publishing the artifact requires a new plan with a fresh approval", orDash(plan.TerminalReason)),
		})
	case publication.StatusPublishing:
		if plan.EffectiveApproval() != nil {
			out = append(out, Remediation{
				Kind:   RemediationRetry,
				Action: "the publication cycle is in progress (or was interrupted mid-cycle)",
				Detail: "re-run publish with a healthy context to resume: succeeded targets are never re-executed, interrupted ones must be verified first",
			})
		}
	}
	return out
}

// branchSuffix renders the branch part of a push step (" (branch <name>)").
func branchSuffix(branch string) string {
	if branch == "" {
		return ""
	}
	return fmt.Sprintf(" on branch %q", branch)
}

// recordOr returns v when non-empty, else the placeholder.
func recordOr(placeholder, v string) string {
	if v == "" {
		return placeholder
	}
	return v
}

// uriOr returns v when non-empty, else the placeholder.
func uriOr(placeholder, v string) string {
	if v == "" {
		return placeholder
	}
	return v
}

// orDash renders a possibly-empty string for a report sentence.
func orDash(v string) string {
	if v == "" {
		return "(no reason recorded)"
	}
	return v
}

// sortedRemediation orders remediation steps deterministically for display:
// by target declaration order is lost once steps span targets and plan
// level, so sort by (kind rank, target ID): the most time-sensitive
// (verify) first, then complete, retry, push, replan.
//
// (Exported for the presentation layer, which may want the canonical order.)
func SortedRemediation(rs []Remediation) []Remediation {
	kindRank := map[RemediationKind]int{
		RemediationVerify:   0,
		RemediationComplete: 1,
		RemediationRetry:    2,
		RemediationPush:     3,
		RemediationReplan:   4,
	}
	out := make([]Remediation, len(rs))
	copy(out, rs)
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := kindRank[out[i].Kind], kindRank[out[j].Kind]
		if ri != rj {
			return ri < rj
		}
		return out[i].TargetID < out[j].TargetID
	})
	return out
}
