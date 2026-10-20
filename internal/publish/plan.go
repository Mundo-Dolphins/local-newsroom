package publish

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
	"github.com/Mundo-Dolphins/local-newsroom/internal/review"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// TargetSpec describes one publication target at plan creation.
//
// Provider and Destination are opaque to the plan: the contract requires
// them to be credential-free identifiers (a handle, a site URL, a
// repository path). The adapter-specific binding that a Publisher actually
// needs (checkout, branch, commit identity) lives with the publisher, never
// in the plan document.
type TargetSpec struct {
	ID          publication.TargetID
	Name        string
	Type        publication.TargetType
	Provider    string
	Destination string
	DependsOn   []publication.TargetID
}

// CreatePlanRequest requests a new publication plan for a run.
//
// The plan is baselined to the bytes of one content file in the run
// directory (ArtifactPath) — the file whose bytes a publisher will send,
// exactly. For a rendered article that is output/article.md; for a thread
// it is the canonical thread content (publication/content/<name>, produced
// with ThreadContent + SaveContent), because the NUL-joined form is the
// one a Bluesky publisher digests.
type CreatePlanRequest struct {
	// ArtifactID identifies the content (letters/digits/'.'/'-'/'_').
	// Empty: derived from the file name.
	ArtifactID string

	// ArtifactKind classifies the content; defaults to rendered.
	ArtifactKind run.ArtifactKind

	// ArtifactPath is the run-relative path of the content file (required).
	ArtifactPath string

	// ArtifactVersion is an optional content version tag (e.g. the renderer
	// version).
	ArtifactVersion string

	// Targets are the publication destinations (at least one, declared in
	// execution order: a target's dependencies must appear before it).
	Targets []TargetSpec
}

// CreatePlan gathers the gate evidence, baselines the plan to the current
// content bytes, persists the plan document, and records it on the
// manifest's publication_state summary.
//
// It fails closed (ErrGateFailed) unless the run is READY: the run
// succeeded, the final factual check passed, and the review bundle reports
// readiness — the same verdict review.Readiness shows a human. A plan can
// exist only for an artifact that is ready; that is the v0.6 core rule
// (no automatic publication) made structural.
func (e *Engine) CreatePlan(ctx context.Context, runID run.RunID, req CreatePlanRequest, bundle *review.Bundle) (*publication.Plan, error) {
	m, _, err := e.store.LoadManifest(runID)
	if err != nil {
		return nil, fmt.Errorf("loading manifest for run %s: %w", runID, err)
	}
	if m.ID != runID {
		return nil, fmt.Errorf("run ID mismatch: the manifest of %s is for %s", runID, m.ID)
	}

	kind := req.ArtifactKind
	if kind == "" {
		kind = run.ArtifactKindRendered
	}
	if kind == run.ArtifactKind("") || !kind.Valid() {
		kind = run.ArtifactKindRendered
	}
	artifactID := req.ArtifactID
	if artifactID == "" {
		artifactID = path.Base(req.ArtifactPath)
	}

	content, err := e.loadContent(runID, req.ArtifactPath)
	if err != nil {
		return nil, err
	}

	at := e.now()
	gate := BuildGateEvidence(m, bundle, content, at)
	if !gate.Passing() {
		_, why := GateReady(gate)
		return nil, fmt.Errorf("%w: the run is not READY for publication: %s", ErrGateFailed, why)
	}

	identity := publication.NewArtifactIdentity(artifactID, kind, req.ArtifactPath, content, req.ArtifactVersion)
	targets := make([]publication.PublicationTarget, 0, len(req.Targets))
	for _, ts := range req.Targets {
		targets = append(targets, publication.PublicationTarget{
			ID:          ts.ID,
			Name:        ts.Name,
			Type:        ts.Type,
			Provider:    ts.Provider,
			Destination: ts.Destination,
			DependsOn:   ts.DependsOn,
		})
	}

	plan, err := publication.NewPlan(publication.NewPlanParams{
		RunID:     m.ID,
		Workspace: m.Workspace.Name,
		Gate:      gate,
		Artifact:  identity,
		Targets:   targets,
		CreatedAt: at,
	})
	if err != nil {
		if strings.Contains(err.Error(), "gate") {
			return nil, fmt.Errorf("%w: %v", ErrGateFailed, err)
		}
		return nil, fmt.Errorf("creating publication plan: %w", err)
	}
	if err := e.persistPlan(m, plan); err != nil {
		return nil, err
	}
	return plan, nil
}

// loadContent reads the plan's content file through the store (missing
// files fail closed with ErrArtifactMissing).
func (e *Engine) loadContent(runID run.RunID, relPath string) ([]byte, error) {
	if relPath == "" {
		return nil, fmt.Errorf("%w: no artifact path named in the request", ErrArtifactMissing)
	}
	data, err := e.store.LoadArtifact(runID, relPath)
	if err != nil {
		if isNotExist(err) {
			return nil, fmt.Errorf("%w: %s (run %s)", ErrArtifactMissing, relPath, runID)
		}
		return nil, fmt.Errorf("reading content %s: %w", relPath, err)
	}
	return data, nil
}

// SaveContent persists derived publication content (for example the
// canonical NUL-joined thread text) into the run's publication/content
// directory, through the same sanitization boundary as every other run
// file.
//
// Derived content exists because some plan artifacts are not run stage
// outputs: a thread plan must be baselined to the canonical thread bytes
// (ThreadContent), which the render stage does not write as a file.
//
// The canonical thread form is NUL-joined, and the workspace store classifies
// NUL-bearing bytes as binary (images, databases), a class in which only
// configured exact values are matched and the pattern detectors never run.
// That would let a token leaked into a post reach disk unredacted, so
// NUL-separated content that is text is sanitized per segment here (and
// refused fail-closed when it stays dirty) before it crosses the store
// boundary. The bytes persisted — the plan's baseline — are exactly the
// sanitized bytes.
func (e *Engine) SaveContent(runID run.RunID, name string, content []byte) (string, error) {
	rel := path.Join(run.PublicationContentDir, name)
	ref := run.ArtifactRef{
		Kind:       run.ArtifactKindRendered,
		ID:         "publication-content-" + strings.TrimPrefix(path.Base(name), path.Ext(path.Base(name))),
		Stage:      "",
		Path:       rel,
		ProducedAt: run.NewTimestamp(e.now()),
	}
	if err := ref.Validate(); err != nil {
		return "", fmt.Errorf("invalid content reference for %s: %w", rel, err)
	}
	content, err := e.sanitizeSeparatedContent(name, content)
	if err != nil {
		return "", fmt.Errorf("persisting %s: %w", rel, err)
	}
	return e.store.SaveArtifact(runID, ref, content)
}

// sanitizeSeparatedContent applies the full text sanitization to
// NUL-separated content (the canonical thread form): each segment is
// sanitized individually, and a segment that is still dirty after
// sanitization fails the write (fail closed). The NUL structure is
// preserved in the returned bytes.
//
// Content without NUL separators, or whose segments are not text (binary
// blobs), is returned unchanged: the store's own classification and
// boundary apply to it as they do to every other artifact.
func (e *Engine) sanitizeSeparatedContent(name string, content []byte) ([]byte, error) {
	if bytes.IndexByte(content, 0) < 0 {
		// No NUL separators: ordinary text or binary; the store's own
		// classification and boundary apply unchanged.
		return content, nil
	}
	segs := bytes.Split(content, []byte{0})
	for _, seg := range segs {
		if !utf8.Valid(seg) {
			// Not text (a binary blob that happens to contain NULs): leave
			// it to the store's binary path.
			return content, nil
		}
	}
	san := e.store.Sanitizer()
	out := make([]byte, 0, len(content))
	for i, seg := range segs {
		text, _ := san.Sanitize(string(seg))
		if !san.CleanText(text) {
			return nil, fmt.Errorf("segment %d of %s is still secret-looking after sanitization; derived content is not persisted (fail closed)", i+1, name)
		}
		if i > 0 {
			out = append(out, 0)
		}
		out = append(out, text...)
	}
	return out, nil
}

// Approve records an explicit human approval of the plan's current artifact
// content, re-gathering the gate evidence from the same bytes, and
// transitions the plan to approved.
//
// The content file is re-read (and its digest recomputed) at approval time:
// an approval always binds to what is on disk when the human signs, and a
// content file that changed since the plan was created forces a re-baseline
// first (RefreshArtifact) rather than silently approving different bytes
// than the plan shows.
func (e *Engine) Approve(ctx context.Context, runID run.RunID, planID publication.PublicationID, approvedBy string, bundle *review.Bundle) (*publication.Plan, error) {
	if approvedBy == "" {
		return nil, fmt.Errorf("an approver identity is required to approve plan %s", planID)
	}
	pctx, err := e.loadPlanContext(runID, planID)
	if err != nil {
		return nil, err
	}
	m, plan := pctx.manifest, pctx.plan
	if plan.Status.Terminal() {
		return nil, fmt.Errorf("%w: %s is %s; terminal plans cannot be approved (publishing again is a new plan)", ErrPlanCancelled, plan.ID, plan.Status)
	}

	at := e.now()
	content, err := e.loadContent(runID, plan.Artifact.Path)
	if err != nil {
		return nil, err
	}
	currentDigest := publication.DigestOf(content)
	if !plan.Artifact.Digest.Matches(string(currentDigest)) {
		if len(plan.Approvals) > 0 {
			return nil, fmt.Errorf("%w: %s (artifact %s changed from %s to %s after the approval was granted)",
				ErrStaleApproval, plan.ID, plan.Artifact.Path, plan.Artifact.Digest, currentDigest)
		}
		return nil, fmt.Errorf("%w: %s changed from %s to %s; re-baseline with RefreshArtifact and approve the current content",
			ErrArtifactChanged, plan.Artifact.Path, plan.Artifact.Digest, currentDigest)
	}

	gate := BuildGateEvidence(m, bundle, content, at)
	if !gate.Passing() {
		_, why := GateReady(gate)
		return nil, fmt.Errorf("%w: %s: %s", ErrGateFailed, plan.ID, why)
	}
	if err := plan.SetGate(gate, at); err != nil {
		return nil, fmt.Errorf("recording gate evidence for %s: %w", plan.ID, err)
	}

	approval := publication.NewApproval(plan.ID, approvedBy, publication.ApprovalMethodCLI, currentDigest, nil, "", at)
	if err := plan.Approve(approval, at); err != nil {
		return nil, fmt.Errorf("approving %s: %w", plan.ID, err)
	}
	if err := e.persistPlan(m, plan); err != nil {
		return nil, err
	}
	return plan, nil
}

// RevokeApproval withdraws a granted approval (a human changes their mind
// before the cycle runs).
func (e *Engine) RevokeApproval(ctx context.Context, runID run.RunID, planID publication.PublicationID, approvalID publication.ApprovalID, reason string) (*publication.Plan, error) {
	pctx, err := e.loadPlanContext(runID, planID)
	if err != nil {
		return nil, err
	}
	m, plan := pctx.manifest, pctx.plan
	at := e.now()
	if err := plan.RevokeApproval(approvalID, at, reason); err != nil {
		return nil, fmt.Errorf("revoking approval %s of %s: %w", approvalID, plan.ID, err)
	}
	if err := e.persistPlan(m, plan); err != nil {
		return nil, err
	}
	return plan, nil
}

// Cancel stops the plan on human request: in-flight targets are aborted,
// the plan is cancelled (terminal), and nothing further will run from it.
// Re-publishing the same artifact is a new plan.
func (e *Engine) Cancel(ctx context.Context, runID run.RunID, planID publication.PublicationID, reason string) (*publication.Plan, error) {
	pctx, err := e.loadPlanContext(runID, planID)
	if err != nil {
		return nil, err
	}
	m, plan := pctx.manifest, pctx.plan
	at := e.now()
	if err := plan.Cancel(at, reason); err != nil {
		return nil, fmt.Errorf("cancelling %s: %w", plan.ID, err)
	}
	if err := e.persistPlan(m, plan); err != nil {
		return nil, err
	}
	return plan, nil
}

// SkipTarget records a deliberate decision not to publish one target (for
// example: "publish the article, but skip the thread").
func (e *Engine) SkipTarget(ctx context.Context, runID run.RunID, planID publication.PublicationID, targetID publication.TargetID, reason string) (*publication.Plan, error) {
	pctx, err := e.loadPlanContext(runID, planID)
	if err != nil {
		return nil, err
	}
	m, plan := pctx.manifest, pctx.plan
	at := e.now()
	if err := plan.SkipTarget(targetID, reason, at); err != nil {
		return nil, fmt.Errorf("skipping target %q of %s: %w", targetID, plan.ID, err)
	}
	if err := e.persistPlan(m, plan); err != nil {
		return nil, err
	}
	return plan, nil
}

// RefreshArtifact re-baselines an active plan to new content bytes and
// re-gathers the gate evidence from them: the recovery path for "the
// artifact changed after the plan was baselined (or approved)".
//
// The re-baseline follows the contract: the artifact reference (id, kind,
// path) is unchanged — only the digest moves — and every approval bound to
// the old digest is invalidated as stale in the same persisted document.
// The plan (or its approvals) no longer authorizes publication until a
// human approves the new content (Approve).
//
// newContent is the exact replacement byte content; it is persisted to the
// plan's content path (through the sanitization boundary) and the plan's
// artifact identity is rebaselined to its digest.
func (e *Engine) RefreshArtifact(ctx context.Context, runID run.RunID, planID publication.PublicationID, newContent []byte, bundle *review.Bundle) (*publication.Plan, error) {
	pctx, err := e.loadPlanContext(runID, planID)
	if err != nil {
		return nil, err
	}
	m, plan := pctx.manifest, pctx.plan
	if plan.Status.Terminal() {
		return nil, fmt.Errorf("%w: %s is %s; terminal plans are immutable historical records (publishing the changed artifact is a new plan)", ErrPlanCancelled, plan.ID, plan.Status)
	}
	if newContent == nil {
		return nil, fmt.Errorf("new content is required to re-baseline plan %s", planID)
	}

	at := e.now()

	// Persist the new content first: the run directory must hold the bytes
	// the plan is about to reference (the engine re-reads the file for its
	// digest checks, and the resume path does too). NUL-separated content
	// (a re-baselined thread) gets the same per-segment sanitization as
	// SaveContent: the store's binary class would skip the pattern
	// detectors on it, and the re-baseline is where a token can sneak in.
	newContent, err = e.sanitizeSeparatedContent(plan.Artifact.Path, newContent)
	if err != nil {
		return nil, fmt.Errorf("persisting re-baselined content for %s: %w", planID, err)
	}
	if _, err := e.store.SaveArtifact(runID, run.ArtifactRef{
		Kind:       plan.Artifact.Kind,
		ID:         plan.Artifact.ID,
		Path:       plan.Artifact.Path,
		ProducedAt: run.NewTimestamp(at),
	}, newContent); err != nil {
		return nil, fmt.Errorf("persisting re-baselined content for %s: %w", planID, err)
	}
	// The baseline must digest the bytes that actually landed on disk (the
	// store may sanitize on the way down, exactly as for the plan document),
	// so re-read the file and baseline from the read-back.
	persisted, err := e.store.LoadArtifact(runID, plan.Artifact.Path)
	if err != nil {
		return nil, fmt.Errorf("%w: re-baselined content of %s cannot be read back: %v", ErrPlanInvalid, planID, err)
	}
	newIdentity := publication.NewArtifactIdentity(plan.Artifact.ID, plan.Artifact.Kind, plan.Artifact.Path, persisted, plan.Artifact.Version)
	if err := plan.RebaselineArtifact(newIdentity, at); err != nil {
		return nil, fmt.Errorf("re-baselining %s: %w", planID, err)
	}
	// Re-gather the gate from the same persisted bytes the plan now
	// baselines: the gate's artifact digest must keep matching the plan's,
	// or AuthorizesPublish would fail closed for reasons the re-baseline
	// caused itself.
	gate := BuildGateEvidence(m, bundle, persisted, at)
	if !gate.Passing() {
		_, why := GateReady(gate)
		return nil, fmt.Errorf("%w: after the re-baseline the run is not READY for publication: %s", ErrGateFailed, why)
	}
	if err := plan.SetGate(gate, at); err != nil {
		return nil, fmt.Errorf("re-recording gate evidence for %s: %w", planID, err)
	}
	if err := e.persistPlan(m, plan); err != nil {
		return nil, err
	}
	return plan, nil
}

// RecordDryRun stores a rehearsal result on one target (planned or
// approved plans only; a dry run never changes the plan status).
func (e *Engine) RecordDryRun(ctx context.Context, runID run.RunID, planID publication.PublicationID, targetID publication.TargetID, result *publication.DryRunResult) (*publication.Plan, error) {
	pctx, err := e.loadPlanContext(runID, planID)
	if err != nil {
		return nil, err
	}
	m, plan := pctx.manifest, pctx.plan
	at := e.now()
	if err := plan.RecordDryRun(targetID, result, at); err != nil {
		return nil, fmt.Errorf("recording dry run for target %q of %s: %w", targetID, plan.ID, err)
	}
	if err := e.persistPlan(m, plan); err != nil {
		return nil, err
	}
	return plan, nil
}

// Plan loads and returns one persisted plan of a run (failing closed with
// ErrPlanInvalid on a corrupt or tampered document).
func (e *Engine) Plan(runID run.RunID, planID publication.PublicationID) (*publication.Plan, error) {
	return e.loadPlan(runID, planID)
}

// LatestPlan returns the run's latest persisted plan (the manifest summary,
// else the newest plan document). It is the resolution target of a bare
// "publish" command and of the review/TUI publication flows.
func (e *Engine) LatestPlan(runID run.RunID) (*publication.Plan, error) {
	m, _, err := e.store.LoadManifest(runID)
	if err != nil {
		return nil, fmt.Errorf("loading manifest for run %s: %w", runID, err)
	}
	id, err := e.resolvePlanID(m, "")
	if err != nil {
		return nil, err
	}
	return e.loadPlan(runID, id)
}

// ListPlanIDs lists the run's persisted plan IDs in derivation order.
func (e *Engine) ListPlanIDs(runID run.RunID) ([]publication.PublicationID, error) {
	return e.listPlanIDs(runID)
}

// HasPlans reports whether the run has any persisted publication plans.
//
// This is the "no previous publication" check: for a run it was never
// needed, it is false (and every planning operation is the first).
func (e *Engine) HasPlans(runID run.RunID) (bool, error) {
	ids, err := e.listPlanIDs(runID)
	if err != nil {
		return false, err
	}
	return len(ids) > 0, nil
}

// Summary returns the manifest's publication_state summary (nil when the
// run has none) without loading the plan document.
func (e *Engine) Summary(runID run.RunID) (*run.PublicationState, error) {
	m, _, err := e.store.LoadManifest(runID)
	if err != nil {
		return nil, fmt.Errorf("loading manifest for run %s: %w", runID, err)
	}
	return m.PublicationState, nil
}
