package publish

import (
	"context"
	"fmt"

	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
	"github.com/Mundo-Dolphins/local-newsroom/internal/review"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// Orchestrator drives the v0.6 publication pipeline for one run:
//
//	ready check → plan → dry-run/preview → approval → publish → summary
//
// It is the layer between the durable engine (which owns state, receipts,
// and persistence) and the program that needs a finished publication: the
// orchestrator assembles the human-facing projections (ApprovalSummary,
// Summary) and wraps the engine's plan / approve / publish calls in the
// pipeline order the ticket specifies.
//
// UI INDEPENDENCE (a hard requirement of the contract): the orchestrator
// has no interactive UI. It never prompts, never opens a TUI, and never
// reads or writes a terminal — everything it produces is plain data, and
// the one human decision it needs (the approval) comes back in as an
// ApprovalInput value that the presenting surface constructed. The same
// safe pipeline therefore works from a CLI, a TUI, or an operator with a
// pen — and because there is no UI event the orchestrator accepts,
// publication can never fire accidentally from one: the approver
// identity, the surface, the confirmed digest, and the words are all
// explicit arguments.
type Orchestrator struct {
	eng    *Engine
	runID  run.RunID
	bundle *review.Bundle

	// pubs is the per-target publisher binding: the executable adapters
	// keyed by the plan's target IDs.
	pubs map[publication.TargetID]Publisher
}

// New creates an orchestrator for one run. bundle is the review evidence
// for that run (its status and final check); a nil bundle is treated as
// empty evidence, which the pre-publication gate reports as not-ready —
// an orchestrator with no review evidence can publish nothing.
func NewOrchestrator(eng *Engine, runID run.RunID, bundle *review.Bundle) *Orchestrator {
	if bundle == nil {
		bundle = &review.Bundle{}
	}
	return &Orchestrator{eng: eng, runID: runID, bundle: bundle}
}

// SetPublishers binds the executable publishers for the run's targets,
// keyed by the plan's target IDs. Bridges from NewBlueskyPublisher /
// NewGitPublisher (or test fakes) are the usual values.
func (o *Orchestrator) SetPublishers(pubs map[publication.TargetID]Publisher) {
	o.pubs = pubs
}

// ReadyCheck is the pre-publication gate result for the run: whether the
// artifact may proceed to planning, and why not, in the same vocabulary
// the rendered review bundle shows.
type ReadyCheck struct {
	// Ready is true only when the run succeeded, the final factual check
	// is pass, and the review bundle reports READY.
	Ready bool

	// Reason is the human-readable determination for not-ready cases (the
	// same sentence the rendered review bundle shows). "" when ready.
	Reason string

	// ContentMissing is true when the requested artifact file is absent
	// from the run directory: a distinct not-ready cause (there is
	// nothing to plan or approve) rather than a gate failure.
	ContentMissing bool

	// Gate is the evidence gathered from the current on-disk content (the
	// zero value when the content could not be read).
	Gate publication.GateEvidence
}

// Prepare builds the publication plan for the requested artifact and
// targets — or, when the run already holds an active (non-terminal) plan
// for the same artifact, returns that plan unchanged.
//
// The reuse rule keeps a repeated "prepare" from fragmenting one pending
// approval into two competing plan documents: the plan a human is looking
// at is the plan they will approve. A run whose latest plan already ended
// (succeeded, failed, or cancelled) prepares a fresh plan: publishing
// again is a new plan with a new approval, per the contract.
//
// Prepare fails closed (ErrGateFailed) unless the run is READY — the same
// verdict the rendered review bundle shows a human — so a plan can exist
// only for output a human has already been told is ready.
func (o *Orchestrator) Prepare(ctx context.Context, req CreatePlanRequest) (*publication.Plan, error) {
	if latest, err := o.eng.LatestPlan(o.runID); err == nil {
		if !latest.Status.Terminal() && latest.Artifact.Path == req.ArtifactPath {
			return latest, nil
		}
	}
	return o.eng.CreatePlan(ctx, o.runID, req, o.bundle)
}

// Ready evaluates the pre-publication gate for the requested artifact:
// run succeeded + final check pass + review READY, read from the current
// on-disk content. It performs no state change. A missing content file is
// reported as not-ready (ContentMissing), not as an error: callers use
// this to refuse the pipeline before any planning happens.
func (o *Orchestrator) Ready(ctx context.Context, req CreatePlanRequest) (*ReadyCheck, error) {
	m, _, err := o.eng.store.LoadManifest(o.runID)
	if err != nil {
		return nil, fmt.Errorf("loading run %s: %w", o.runID, err)
	}
	check := &ReadyCheck{}
	content, err := o.eng.store.LoadArtifact(o.runID, req.ArtifactPath)
	if err != nil {
		if isNotExist(err) {
			check.ContentMissing = true
			check.Reason = fmt.Sprintf("the requested artifact %s is missing from the run directory", req.ArtifactPath)
			return check, nil
		}
		return nil, fmt.Errorf("%w: %s: %v", ErrArtifactMissing, req.ArtifactPath, err)
	}
	check.Gate = BuildGateEvidence(m, o.bundle, content, o.eng.now())
	check.Ready, check.Reason = GateReady(check.Gate)
	return check, nil
}

// ApprovalSummary is the machine-readable projection of everything a human
// must review before approving publication. It is data for a presenting
// surface, not a presentation: the contract requires the approval screen
// to show, at minimum, the run and topic, the artifact's exact content
// hash, every destination, the exact Hugo path/branch or Bluesky post
// count, the dry-run preview, and the warnings — and this struct carries
// all of it, adapter-agnostically.
type ApprovalSummary struct {
	RunID     run.RunID
	Topic     string
	Workspace string

	PlanID     publication.PublicationID
	PlanStatus publication.PublicationStatus

	// Artifact is the plan's content identity (path, digest, size): what
	// the publication would cover.
	Artifact publication.ArtifactIdentity

	// Digest is the exact content hash the approval will bind to: the
	// plan's artifact digest (the approval the engine records is bound to
	// the bytes on disk, which must equal this for approval to succeed).
	Digest publication.Digest

	// ContentStale is true when the on-disk content no longer matches
	// the plan's recorded baseline: approval will be refused until the
	// artifact is re-baselined and re-approved.
	ContentStale bool

	// ContentUnavailable is true when the artifact file is absent from
	// the run directory: previews cannot run and approval will be
	// refused.
	ContentUnavailable bool

	// Gate is the last recorded gate evidence; GateReady / GateReason
	// are its determination at that moment.
	Gate       publication.GateEvidence
	GateReady  bool
	GateReason string

	// Targets lists every target with its destination, dependencies,
	// current state, and (when its publisher can rehearse) the dry-run
	// preview carrying the adapter's own facts.
	Targets []TargetSummary

	// Approvals lists the approvals recorded on the plan; Effective marks
	// the one currently in force for the plan's artifact digest.
	Approvals []ApprovalInfo

	// Warnings is the plan's warning log plus the warnings the preview
	// pass added (unavailable previews, stale content, missing bindings).
	Warnings []run.Warning
}

// TargetSummary describes one target on the approval screen: its
// destination, its place in the dependency graph, its current state, and
// the latest rehearsal.
type TargetSummary struct {
	ID          publication.TargetID
	Name        string
	Type        publication.TargetType
	Provider    string
	Destination string
	DependsOn   []publication.TargetID
	Status      publication.PublicationStatus
	SkipReason  string

	// Preview is the latest dry-run rehearsal for the target (nil when
	// its publisher cannot preview — the warnings explain why).
	Preview *Preview

	// Receipt and Error are the target's last proven state, if any.
	Receipt  *publication.PublicationReceipt
	Error    *run.ErrorSummary
	Attempts int
}

// ApprovalInfo is one recorded approval on the plan, with whether it is
// the one currently in force for the plan's artifact digest.
type ApprovalInfo struct {
	Approval  publication.PublicationApproval
	Effective bool
}

// Approval is the domain record of a human approval decision: the only
// input the orchestrator needs from a human. The surface that presented
// the ApprovalSummary constructs this value — the principal, the surface,
// the words, the scope, and the digest the human confirmed — and passes
// it to Approve. The orchestrator validates and records it; it does not
// prompt.
type Approval struct {
	// ApprovedBy is the human principal (required; validated and
	// redacted by the publication model).
	ApprovedBy string

	// Method is the surface the approval came from (cli | tui | manual |
	// custom; empty is recorded as cli).
	Method publication.ApprovalMethod

	// Note is the human's own words (redacted on recording).
	Note string

	// Targets optionally narrows the approval to specific targets (empty
	// = every target of the plan). Names must exist in the plan.
	Targets []publication.TargetID

	// DigestConfirmed is the content digest the human saw and confirmed.
	// Empty means "the plan's current artifact digest"; any non-empty
	// value that does not match the plan's artifact digest is refused —
	// approving bytes different from the plan's is not an approval of
	// this plan.
	DigestConfirmed publication.Digest
}

// buildApprovalSummary projects a plan into the approval-facing data: it
// re-reads the artifact, recomputes each target's dry-run rehearsal via
// the publishers' Previewers (a fresh rehearsal, so the approval screen
// reflects what is on disk now, not what it was at preview time), records
// the rehearsals on the plan document where the plan's state allows, and
// lists the approvals with their current effectiveness.
func (o *Orchestrator) buildApprovalSummary(ctx context.Context, plan *publication.Plan) (*ApprovalSummary, error) {
	m, err := o.manifest()
	if err != nil {
		return nil, err
	}
	sum := &ApprovalSummary{
		RunID:      plan.RunID,
		Topic:      m.Topic,
		Workspace:  plan.Workspace,
		PlanID:     plan.ID,
		PlanStatus: plan.Status,
		Artifact:   plan.Artifact,
		Digest:     plan.Artifact.Digest,
		Gate:       plan.Gate,
		Warnings:   append([]run.Warning(nil), plan.Warnings...),
		Approvals:  make([]ApprovalInfo, 0, len(plan.Approvals)),
		Targets:    make([]TargetSummary, 0, len(plan.Targets)),
	}
	if m.Topic == "" {
		sum.Topic = string(m.ID)
	}
	sum.GateReady, sum.GateReason = GateReady(plan.Gate)

	content, err := o.eng.store.LoadArtifact(o.runID, plan.Artifact.Path)
	if err != nil {
		if isNotExist(err) {
			sum.ContentUnavailable = true
			o.addWarning(sum, "artifact-missing", fmt.Sprintf("the artifact %s is missing from the run directory; previews cannot run and approval will be refused", plan.Artifact.Path))
			return sum, nil
		}
		return nil, fmt.Errorf("%w: %s: %v", ErrArtifactMissing, plan.Artifact.Path, err)
	}

	currentDigest := publication.DigestOf(content)
	if !plan.Artifact.Digest.Matches(string(currentDigest)) {
		sum.ContentStale = true
		o.addWarning(sum, "artifact-stale", fmt.Sprintf("the content on disk (%s) no longer matches the plan baseline (%s); re-baseline the artifact and approve the current content", currentDigest, plan.Artifact.Digest))
	}

	// Rehearse every target that can rehearse.
	previews := make(map[publication.TargetID]*Preview)
	for i := range plan.Targets {
		t := &plan.Targets[i]
		pv, ok := o.pubs[t.ID].(Previewer)
		if !ok {
			if _, bound := o.pubs[t.ID]; !bound {
				o.addWarning(sum, "no-publisher-bound", fmt.Sprintf("target %q: no publisher is bound; it cannot preview and its publication will be refused until one is bound", t.ID))
			} else {
				o.addWarning(sum, "no-preview", fmt.Sprintf("target %q: the publisher does not preview; approve on the remaining evidence", t.ID))
			}
			continue
		}
		p, perr := pv.Preview(ctx, content, t.Receipt)
		if perr != nil {
			o.addWarning(sum, "preview-unavailable", fmt.Sprintf("target %q: no preview available (%s)", t.ID, run.RedactSecrets(perr.Error())))
			continue
		}
		previews[t.ID] = p
	}

	// Record the rehearsals where the plan's state allows (planned or
	// approved, and the content matching the plan baseline): the
	// persisted dry run is the audit record of what the adapter
	// validated. A stale or late-stage plan keeps its preview in the
	// summary only (a rehearsal of different bytes proves nothing about
	// the recorded content, and terminal plans are not updated).
	recordable := !sum.ContentStale && !sum.ContentUnavailable &&
		(plan.Status == publication.StatusPlanned || plan.Status == publication.StatusApproved)
	if recordable {
		for i := range plan.Targets {
			id := plan.Targets[i].ID
			p, ok := previews[id]
			if !ok {
				continue
			}
			res := publication.NewDryRunResult(o.eng.now(), outcomeOf(p), currentDigest, p.Details, p.Issues)
			if _, rerr := o.eng.RecordDryRun(ctx, o.runID, plan.ID, id, res); rerr != nil {
				o.addWarning(sum, "dry-run-unrecorded", fmt.Sprintf("target %q: the rehearsal ran but could not be recorded: %s", id, run.RedactSecrets(rerr.Error())))
			}
		}
	}

	for i := range plan.Targets {
		t := &plan.Targets[i]
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
		if ts.Preview = previews[t.ID]; ts.Preview == nil && t.DryRun != nil {
			// Fall back to the last persisted rehearsal.
			ts.Preview = &Preview{OK: t.DryRun.Outcome == publication.DryRunOutcomeOK, Details: t.DryRun.Details, Issues: t.DryRun.Issues}
		}
		sum.Targets = append(sum.Targets, ts)
	}

	for i := range plan.Approvals {
		a := &plan.Approvals[i]
		sum.Approvals = append(sum.Approvals, ApprovalInfo{
			Approval:  *a,
			Effective: a == plan.EffectiveApproval(),
		})
	}
	return sum, nil
}

// addWarning appends a warning (redacted, timestamped) to a summary.
func (o *Orchestrator) addWarning(sum *ApprovalSummary, code, message string) {
	sum.Warnings = append(sum.Warnings, run.NewWarning(code, run.RedactSecrets(message), o.eng.now()))
}

// outcomeOf projects a preview's success flag to the plan model's dry run
// outcome, keeping the issue<->outcome invariant (a failed rehearsal with
// no issues gets a synthetic one so the record stays valid).
func outcomeOf(p *Preview) publication.DryRunOutcome {
	if p == nil {
		return publication.DryRunOutcomeOK
	}
	if p.OK {
		return publication.DryRunOutcomeOK
	}
	if len(p.Issues) == 0 {
		msg := p.Details
		if msg == "" {
			msg = "the rehearsal reported failure without details"
		}
		p = &Preview{OK: false, Details: p.Details, Issues: []publication.DryRunIssue{publication.NewDryRunIssue("dry-run-failed", msg)}}
	}
	return publication.DryRunOutcomeFailed
}

// Preview runs the dry-run rehearsal for every previewable target of the
// plan (or the run's latest plan when planID is "") and returns the
// ApprovalSummary a human must review before approving: the run and topic,
// the exact artifact digest, every destination with its dependency and
// rehearsal, the recorded approvals, and the warnings.
//
// A dry run has no external side effect: each publisher validates and
// reports only. The rehearsals are recorded on the plan document where
// its state allows, so the persisted plan shows exactly what was
// validated. A plan whose content has drifted still previews — with
// ContentStale set and a warning — because the human must see what would
// happen; the subsequent approval is what fails closed.
func (o *Orchestrator) Preview(ctx context.Context, planID publication.PublicationID) (*ApprovalSummary, error) {
	plan, err := o.resolvePlan(planID)
	if err != nil {
		return nil, err
	}
	return o.buildApprovalSummary(ctx, plan)
}

// Approve records the human's decision on the plan (or the run's latest
// plan when planID is ""). It is the pipeline step between the preview and
// publication, and it is deliberately a data call: the surface that showed
// the ApprovalSummary constructs the Approval and passes it in.
//
// Before the engine records the decision, the orchestrator re-verifies the
// binding: the principal is present, the scope names real targets, and the
// digest the human confirmed matches the plan's artifact digest (else the
// human approved different bytes than the plan shows — not an approval of
// this plan). The engine then re-reads the content, re-gathers the gate,
// records the approval bound to the exact bytes on disk, and persists the
// plan. The returned summary reflects the post-approval state.
func (o *Orchestrator) Approve(ctx context.Context, planID publication.PublicationID, in Approval) (*publication.Plan, *ApprovalSummary, error) {
	plan, err := o.resolvePlan(planID)
	if err != nil {
		return nil, nil, err
	}
	if in.ApprovedBy == "" {
		return nil, nil, fmt.Errorf("an approver identity is required to approve plan %s", plan.ID)
	}
	if in.DigestConfirmed != "" && !in.DigestConfirmed.Matches(string(plan.Artifact.Digest)) {
		return nil, nil, fmt.Errorf("%w: the approval confirms digest %s but the plan's artifact is %s; re-preview and approve the content the plan actually shows", ErrStaleApproval, in.DigestConfirmed, plan.Artifact.Digest)
	}
	for _, tid := range in.Targets {
		if _, ok := plan.Target(tid); !ok {
			return nil, nil, fmt.Errorf("approval scope names a target not in plan %s: %q", plan.ID, tid)
		}
	}

	p, err := o.eng.ApproveWith(ctx, o.runID, plan.ID, in.ApprovedBy, in.Method, in.Note, in.Targets, o.bundle)
	if err != nil {
		return nil, nil, err
	}
	sum, err := o.buildApprovalSummary(ctx, p)
	if err != nil {
		return p, nil, err
	}
	return p, sum, nil
}

// Publish runs the publication cycle for the plan (or resumes one — see
// Engine.Publish) and returns the summary of what happened.
//
// The cycle is the engine's: explicit dependencies are respected, a
// receipt is persisted immediately after every successful external action,
// and the context is observed between actions. What the orchestrator adds
// is the report: on completion, a Summary of every target with its
// receipts and its remediation; on a refusal or an interruption, the same
// Summary of the persisted state together with the error, so a caller that
// was refused or cancelled can still see where the run stands.
//
// A successfully published plan publishes nothing further: a repeated
// Publish is an exact no-op that returns the existing receipts (duplicate
// invocations are safe — nothing that already holds a receipt is ever
// re-executed).
func (o *Orchestrator) Publish(ctx context.Context, planID publication.PublicationID) (*Summary, error) {
	m, err := o.manifest()
	if err != nil {
		return nil, err
	}
	plan, err := o.eng.Publish(ctx, o.runID, o.pubs, Options{PlanID: planID})
	if err == nil {
		return Summarize(m, plan), nil
	}
	p, perr := o.resolvePlan(planID)
	if perr != nil {
		return nil, err
	}
	return Summarize(m, p), err
}

// Summary projects the plan's current persisted state into a Summary
// without running anything: the report a restarted process shows before
// deciding whether to resume, and the final report after a completed
// cycle.
func (o *Orchestrator) Summary(ctx context.Context, planID publication.PublicationID) (*Summary, error) {
	plan, err := o.resolvePlan(planID)
	if err != nil {
		return nil, err
	}
	m, err := o.manifest()
	if err != nil {
		return nil, err
	}
	return Summarize(m, plan), nil
}

// resolvePlan selects the plan an operation acts on: the named plan, or
// the run's latest plan when the ID is empty.
func (o *Orchestrator) resolvePlan(planID publication.PublicationID) (*publication.Plan, error) {
	if planID == "" {
		return o.eng.LatestPlan(o.runID)
	}
	return o.eng.Plan(o.runID, planID)
}

// manifest loads the run manifest (for the topic the reports lead with).
func (o *Orchestrator) manifest() (*run.Manifest, error) {
	m, _, err := o.eng.store.LoadManifest(o.runID)
	if err != nil {
		return nil, fmt.Errorf("loading run %s: %w", o.runID, err)
	}
	return m, nil
}
