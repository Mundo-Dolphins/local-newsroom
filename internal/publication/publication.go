// Package publication defines the v0.6 contracts for controlled publication
// of READY editorial artifacts.
//
// v0.6 is the first version with external side effects. The core rule:
//
//	A completed newsroom run MUST NOT publish automatically just because
//	rendering succeeded.
//
// Publication therefore requires, in order: a persisted run (internal/run),
// a final factual check that passed, a READY artifact/review state, an
// explicit publication plan, an explicit human approval bound to the exact
// artifact content, and a publisher invocation gated on that approval.
//
// This package is the contract layer. Like internal/run it performs no
// filesystem I/O, no network I/O, and no LLM calls: it defines the
// versioned, serializable, secret-free documents (plan, target, approval,
// attempt, receipt) and the state machines over them, plus the single
// authorization check a publisher must pass before any external effect
// (Plan.AuthorizesPublish). Concrete publishers (Bluesky, Hugo, git — later
// v0.6 work) are the only things that may be invoked through it.
//
// Design principles:
//   - Explicit: every state change is a named, validated, timestamped
//     transition; nothing publishes implicitly.
//   - Auditable: stable derived IDs, provenance back to the run and the
//     artifact, bounded attempt/error/warning history, and receipts that
//     record exactly what the outside world holds.
//   - Resumable: a crashed or partially failed cycle restarts from the
//     persisted plan; targets that already succeeded are never re-executed.
//   - Idempotent: approvals bind to the exact artifact digest, receipts
//     carry external record IDs, and re-deriving an ID from the same inputs
//     yields the same ID.
//   - Secret-free: no field anywhere in these contracts holds a credential,
//     and every free-text value is redacted on the way in and re-checked on
//     validation and serialization (defense in depth, mirroring internal/run
//     and the workspace store's sanitizer boundary).
package publication

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// SchemaVersion is the version of the publication record schema implemented
// by this package.
//
// The version is semantic (MAJOR.MINOR.PATCH), independently versioned from
// the run manifest schema (internal/run). Consumers must reject documents
// whose MAJOR version they do not understand; MINOR/PATCH changes are
// backward compatible.
const SchemaVersion = "0.6.0"

// SchemaMajorCurrent is the schema major version understood by this package.
const SchemaMajorCurrent = 0

// ErrUnsupportedSchemaVersion indicates that a publication document was
// written with a schema major version this package cannot read.
var ErrUnsupportedSchemaVersion = errors.New("unsupported publication schema version")

// SchemaMajor extracts the major version from a semantic version string.
//
// A valid version has exactly three non-negative integer components,
// e.g. "0.6.0".
func SchemaMajor(version string) (int, error) {
	parts := strings.Split(strings.TrimSpace(version), ".")
	if len(parts) != 3 {
		return 0, fmt.Errorf("%q must have MAJOR.MINOR.PATCH form", version)
	}
	major := 0
	for i, part := range parts {
		if part == "" {
			return 0, fmt.Errorf("version %q has an empty component", version)
		}
		var n int
		for _, c := range part {
			if c < '0' || c > '9' {
				return 0, fmt.Errorf("version %q has a non-numeric component", version)
			}
			n = n*10 + int(c-'0')
		}
		if i == 0 {
			major = n
		}
	}
	return major, nil
}

// PublicationID is the stable identifier of a publication plan.
//
// Format: "pub-<timestamp>-<12 hex chars>", where the timestamp is the
// plan's created_at and the hash is derived from the originating run. A
// publication plan is fixed for its lifetime: its ID never changes as the
// plan progresses, and re-publishing the same run's artifact is a new plan
// with a new ID — never a mutation of an old one.
type PublicationID string

// PublicationIDPrefix is the required prefix of every publication ID.
const PublicationIDPrefix = "pub-"

// NewPublicationID derives a stable publication ID from the originating run,
// the planned artifact and the set of targets.
//
// The ID embeds the derivation timestamp (second resolution, UTC) and a
// short hash of the derivation inputs, mirroring the internal/run and
// internal/review ID scheme. Deriving it from the run plus the plan's
// content inputs (rather than the wall clock alone) keeps the ID stable
// across re-creation of an identical plan while remaining unique for
// genuinely different plans of the same run.
func NewPublicationID(runID run.RunID, artifact ArtifactIdentity, targetIDs []TargetID, createdAt time.Time) PublicationID {
	parts := []string{string(runID), string(artifact.Digest)}
	parts = append(parts, mapFunc(targetIDs, func(id TargetID) string { return string(id) })...)
	return PublicationID(deriveID(PublicationIDPrefix, createdAt, parts...))
}

// Validate checks that the publication ID is well-formed.
func (id PublicationID) Validate() error {
	return validateIDValue("publication id", string(id), PublicationIDPrefix)
}

// String returns the string representation of the publication ID.
func (id PublicationID) String() string {
	return string(id)
}

// maxNoteLen bounds plan-level free-text notes (reasons, gate notes).
const maxNoteLen = 8192

// codeRe allows conservative machine-readable codes (reasons, issues,
// errors).
var codeRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// workspaceNameRe mirrors the run manifest's workspace name rules: the plan
// repeats the workspace name for convenience of tooling that reads the
// publication document in isolation.
var workspaceNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// maxArtifactVersionLen bounds the artifact content version tag.
const maxArtifactVersionLen = 64

// artifactIDRe allows conservative artifact identifiers (mirrors the run
// manifest's artifact reference rules).
var artifactIDRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

// containsTraversal reports whether p contains a ".." path segment.
func containsTraversal(p string) bool {
	for _, segment := range strings.FieldsFunc(p, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if segment == ".." {
			return true
		}
	}
	return false
}

// ArtifactIdentity identifies the artifact a publication plan publishes,
// without copying its content.
//
// It is a reference plus a digest: the run manifest artifact it refers to
// (ID, kind, run-directory path) and the SHA-256 of the persisted bytes.
// The digest is recorded before publishing and is the binding point of the
// whole safety model — gate evidence, approval, and the publisher's
// invocation must all agree on it. Full rendered content stays in the run
// directory; publication documents never duplicate it.
type ArtifactIdentity struct {
	// ID is the stable ID of the artifact in the run manifest.
	ID string `json:"id"`

	// Kind classifies the artifact (typically "rendered"; the contract is
	// kind-neutral).
	Kind run.ArtifactKind `json:"kind"`

	// Path is the artifact's path relative to the run directory
	// (traceability; no content is stored here).
	Path string `json:"path,omitempty"`

	// Digest is the SHA-256 digest of the artifact bytes exactly as
	// persisted in the run directory, recorded before publishing.
	Digest Digest `json:"digest"`

	// Size is the content length in bytes (audit convenience; avoids
	// storing content).
	Size int64 `json:"size"`

	// Version is an optional content version tag (e.g. the renderer
	// version that produced the bytes), for reproducibility.
	Version string `json:"version,omitempty"`
}

// NewArtifactIdentity builds an artifact identity from its persisted bytes,
// computing the digest and size.
//
// data must be the exact bytes that will be published (the persisted run
// artifact). The content itself is not retained.
func NewArtifactIdentity(id string, kind run.ArtifactKind, path string, data []byte, version string) ArtifactIdentity {
	return ArtifactIdentity{
		ID:      id,
		Kind:    kind,
		Path:    path,
		Digest:  DigestOf(data),
		Size:    int64(len(data)),
		Version: version,
	}
}

// MatchesDigest reports whether the artifact identity binds to the given
// content digest.
func (a ArtifactIdentity) MatchesDigest(digest string) bool {
	return a.Digest.Matches(digest)
}

// Validate checks the artifact identity: reference shape, kind, path safety,
// digest form, and size.
func (a ArtifactIdentity) Validate() error {
	var errs []error
	addf := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}
	if a.ID == "" || !artifactIDRe.MatchString(a.ID) {
		errs = append(errs, errors.New("artifact ID is required and must use letters, digits, '.', '-', '_' (1-128 chars)"))
	}
	if !a.Kind.Valid() {
		addf("artifact %q: unknown kind %q", a.ID, a.Kind)
	}
	if a.Path != "" {
		if containsTraversal(a.Path) {
			addf("artifact %q: path must not contain '..' path segments", a.ID)
		}
		if len(a.Path) > 512 {
			addf("artifact %q: path exceeds 512 characters", a.ID)
		}
	}
	if !a.Digest.Valid() {
		addf("artifact %q: digest %q is not a valid %s digest", a.ID, a.Digest, DigestAlgorithm)
	}
	if a.Size < 0 {
		errs = append(errs, fmt.Errorf("artifact %q: size must be >= 0 (got %d)", a.ID, a.Size))
	}
	if a.Version != "" {
		if len(a.Version) > maxArtifactVersionLen {
			addf("artifact %q: version exceeds %d characters", a.ID, maxArtifactVersionLen)
		}
		if run.LooksLikeSecret(a.Version) {
			addf("artifact %q: version contains secret-looking material; redact it before recording", a.ID)
		}
	}
	return errors.Join(errs...)
}

// GateEvidence is the recorded proof that the run's output cleared the
// pre-publication gates at plan-creation time:
//
//  1. a persisted run that reached its terminal, successful state;
//  2. a final factual check that passed;
//  3. a READY artifact/review state.
//
// The evidence is a point-in-time snapshot taken from the run's persisted
// record (manifest, final-check result, review bundle). The *dynamic*
// staleness protection is the digest binding: the evidence records the
// artifact digest it was gathered for, and an approval or publisher
// invocation is only valid while the plan's artifact digest still matches
// it.
type GateEvidence struct {
	// RunStatus is the run's lifecycle status at the time the evidence was
	// gathered. Publication requires a succeeded run.
	RunStatus run.RunStatus `json:"run_status"`

	// FinalCheck is the final factual check's overall status. Publication
	// requires pass.
	FinalCheck contracts.FactualCheckStatus `json:"final_check"`

	// ReviewReady is the review bundle's readiness verdict (true only for
	// a succeeded run with a passing final check and present rendered
	// output).
	ReviewReady bool `json:"review_ready"`

	// ArtifactDigest is the digest of the artifact the evidence was
	// gathered for. It must equal the plan's artifact digest.
	ArtifactDigest Digest `json:"artifact_digest"`

	// CheckedAt is when the evidence was gathered (UTC).
	CheckedAt run.Timestamp `json:"checked_at"`

	// Note is an optional redacted annotation (e.g. which review verdict
	// line produced the READY state).
	Note string `json:"note,omitempty"`
}

// NewGateEvidence builds gate evidence, redacting its note.
func NewGateEvidence(runStatus run.RunStatus, finalCheck contracts.FactualCheckStatus, reviewReady bool, artifactDigest Digest, checkedAt time.Time, note string) GateEvidence {
	return GateEvidence{
		RunStatus:      runStatus,
		FinalCheck:     finalCheck,
		ReviewReady:    reviewReady,
		ArtifactDigest: artifactDigest,
		CheckedAt:      run.NewTimestamp(checkedAt),
		Note:           run.RedactSecrets(note),
	}
}

// Passing reports whether the evidence satisfies all three pre-publication
// gates: a succeeded run, a passing final factual check, and a READY
// review state.
func (g GateEvidence) Passing() bool {
	return g.RunStatus == run.RunStatusSucceeded &&
		g.FinalCheck == contracts.FactualCheckStatusPass &&
		g.ReviewReady
}

// Validate checks the gate evidence: known statuses, a valid digest, a
// timestamp, and a redacted note.
func (g GateEvidence) Validate() error {
	var errs []error
	addf := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}
	if !g.RunStatus.Valid() {
		addf("unknown run status %q in gate evidence", g.RunStatus)
	} else if g.RunStatus != run.RunStatusSucceeded {
		addf("gate evidence: the run must have succeeded (got %q)", g.RunStatus)
	}
	if g.FinalCheck != contracts.FactualCheckStatusPass && g.FinalCheck != contracts.FactualCheckStatusFail && g.FinalCheck != contracts.FactualCheckStatusReview {
		addf("unknown final check status %q in gate evidence", g.FinalCheck)
	} else if g.FinalCheck != contracts.FactualCheckStatusPass {
		addf("gate evidence: the final check must be pass (got %q)", g.FinalCheck)
	}
	if !g.ReviewReady {
		addf("gate evidence: the review must be READY")
	}
	if !g.ArtifactDigest.Valid() {
		addf("gate evidence digest %q is not a valid %s digest", g.ArtifactDigest, DigestAlgorithm)
	}
	if g.CheckedAt.IsZero() {
		errs = append(errs, errors.New("gate evidence: checked_at is required"))
	}
	if len(g.Note) > maxNoteLen {
		addf("gate evidence note exceeds %d characters", maxNoteLen)
	}
	if run.LooksLikeSecret(g.Note) {
		addf("gate evidence note contains secret-looking material; redact it before recording")
	}
	return errors.Join(errs...)
}

// Plan-level history bounds.
const (
	// MaxTargetsPerPlan bounds the number of targets in one plan.
	MaxTargetsPerPlan = 32

	// MaxApprovalsPerPlan bounds the per-plan approval history (oldest
	// approvals are dropped first when the bound is exceeded).
	MaxApprovalsPerPlan = 8

	// MaxPlanErrors bounds the plan-level error summary log.
	MaxPlanErrors = 32

	// MaxPlanWarnings bounds the plan-level warning log.
	MaxPlanWarnings = 32

	// MaxIssuesPerDryRun bounds the issue list of one dry run result.
	MaxIssuesPerDryRun = 32
)

// Plan is the explicit, versioned publication plan for one artifact of one
// run.
//
// A plan is the unit of publication persistence: one JSON document per
// plan, versioned by SchemaVersion. It names the originating run, the
// artifact (by reference and digest, never by content), the targets with
// their dependencies, the human approvals bound to the exact artifact
// digest, and the full state machine of what was published where.
//
// A plan is the only document a publisher may act on, and only after
// AuthorizesPublish has passed: plan approved, gate evidence passing, an
// effective approval for the exact current digest, and the target in the
// approval's scope.
type Plan struct {
	// SchemaVersion is the publication schema version.
	SchemaVersion string `json:"schema_version"`

	// ID is the stable publication identifier.
	ID PublicationID `json:"id"`

	// RunID references the originating newsroom run (provenance anchor).
	RunID run.RunID `json:"run_id"`

	// Workspace is the owning workspace name (convenience for readers of
	// the document in isolation).
	Workspace string `json:"workspace,omitempty"`

	// Status is the plan lifecycle state.
	Status PublicationStatus `json:"status"`

	// Artifact is the artifact being published (reference + digest).
	Artifact ArtifactIdentity `json:"artifact"`

	// Gate is the recorded pre-publication gate evidence (succeeded run,
	// passing final check, READY review).
	Gate GateEvidence `json:"gate"`

	// Targets are the publication destinations, in declaration order.
	// At least one is required; the count is bounded by MaxTargetsPerPlan.
	Targets []PublicationTarget `json:"targets"`

	// Approvals is the bounded approval history (most recent
	// MaxApprovalsPerPlan approvals). An approval authorizes publication
	// only while its digest still matches Artifact.Digest.
	Approvals []PublicationApproval `json:"approvals,omitempty"`

	// Warnings is the plan-level warning log (redacted, bounded).
	Warnings []run.Warning `json:"warnings,omitempty"`

	// Errors is the plan-level error summary log (redacted, bounded).
	Errors []run.ErrorSummary `json:"errors,omitempty"`

	// TerminalReason explains the plan's terminal state (redacted).
	TerminalReason string `json:"terminal_reason,omitempty"`

	// CreatedAt is when the plan was created (UTC).
	CreatedAt run.Timestamp `json:"created_at"`

	// UpdatedAt is when the plan was last modified (UTC).
	UpdatedAt run.Timestamp `json:"updated_at"`

	// FinishedAt is when the plan reached a terminal state. Nil while the
	// plan is active.
	FinishedAt *run.Timestamp `json:"finished_at,omitempty"`
}

// NewPlanParams configures NewPlan.
type NewPlanParams struct {
	// ID is optional; derived via NewPublicationID when empty.
	ID PublicationID

	// RunID references the persisted run (required).
	RunID run.RunID

	// Workspace is optional (the owning workspace name).
	Workspace string

	// Gate is the pre-publication gate evidence (required, and it must be
	// passing: a plan cannot exist for an artifact that is not ready).
	Gate GateEvidence

	// Artifact is the artifact to publish (required). Its digest must
	// equal Gate.ArtifactDigest.
	Artifact ArtifactIdentity

	// Targets are the publication targets (at least one). All start
	// planned; any non-initial state a caller set on them is discarded —
	// a new plan always starts clean.
	Targets []PublicationTarget

	// CreatedAt is optional; defaults to the current time.
	CreatedAt time.Time
}

// NewPlan creates a new, fully valid publication plan in the planned state.
//
// The plan is only creatable for a READY artifact: the gate evidence must
// be passing (succeeded run + passing final check + READY review) and must
// bind to the exact artifact digest. Everything else being in the initial
// state is enforced by validation.
func NewPlan(p NewPlanParams) (*Plan, error) {
	if p.RunID == "" {
		return nil, errors.New("run ID is required")
	}
	if err := p.RunID.Validate(); err != nil {
		return nil, fmt.Errorf("invalid run reference: %w", err)
	}
	if p.Workspace != "" && !workspaceNameRe.MatchString(p.Workspace) {
		return nil, fmt.Errorf("workspace name %q is invalid: use letters, digits, '.', '-', '_' (1-64 chars)", p.Workspace)
	}
	if !p.Gate.Passing() {
		return nil, errors.New("publication requires a passing gate: the run must be succeeded, the final factual check must be pass, and the review state must be READY")
	}
	if err := p.Gate.Validate(); err != nil {
		return nil, fmt.Errorf("invalid gate evidence: %w", err)
	}
	if err := p.Artifact.Validate(); err != nil {
		return nil, fmt.Errorf("invalid artifact: %w", err)
	}
	if !p.Gate.ArtifactDigest.Matches(string(p.Artifact.Digest)) {
		return nil, fmt.Errorf("gate evidence digest %q does not match artifact digest %q; gather the gate from the exact artifact bytes", p.Gate.ArtifactDigest, p.Artifact.Digest)
	}
	if len(p.Targets) < 1 {
		return nil, errors.New("a publication plan requires at least one target")
	}
	if len(p.Targets) > MaxTargetsPerPlan {
		return nil, fmt.Errorf("a publication plan holds %d targets, exceeding the bound of %d", len(p.Targets), MaxTargetsPerPlan)
	}
	for _, t := range p.Targets {
		if err := t.ID.Validate(); err != nil {
			return nil, fmt.Errorf("invalid target: %w", err)
		}
	}
	if err := validateTargetGraph(p.Targets); err != nil {
		return nil, fmt.Errorf("invalid targets: %w", err)
	}

	createdAt := p.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	id := p.ID
	if id == "" {
		targetIDs := mapFunc(p.Targets, func(t PublicationTarget) TargetID { return t.ID })
		id = NewPublicationID(p.RunID, p.Artifact, targetIDs, createdAt)
	}

	// A new plan always starts with every target in its initial state.
	targets := make([]PublicationTarget, len(p.Targets))
	for i := range p.Targets {
		t := p.Targets[i]
		t.Status = StatusPlanned
		t.SkipReason = ""
		t.DryRun = nil
		t.Receipt = nil
		t.Error = nil
		t.Attempts = nil
		targets[i] = t
	}

	ts := run.NewTimestamp(createdAt)
	plan := &Plan{
		SchemaVersion: SchemaVersion,
		ID:            id,
		RunID:         p.RunID,
		Workspace:     p.Workspace,
		Status:        StatusPlanned,
		Artifact:      p.Artifact,
		Gate:          p.Gate,
		Targets:       targets,
		CreatedAt:     ts,
		UpdatedAt:     ts,
	}
	if err := plan.Validate(); err != nil {
		return nil, fmt.Errorf("invalid publication plan: %w", err)
	}
	return plan, nil
}

// ---------------------------------------------------------------------------
// Plan helpers
// ---------------------------------------------------------------------------

// Target returns the target with the given ID.
func (p *Plan) Target(id TargetID) (*PublicationTarget, bool) {
	for i := range p.Targets {
		if p.Targets[i].ID == id {
			return &p.Targets[i], true
		}
	}
	return nil, false
}

// TargetIDs returns all target IDs in declaration order.
func (p *Plan) TargetIDs() []TargetID {
	ids := make([]TargetID, len(p.Targets))
	for i := range p.Targets {
		ids[i] = p.Targets[i].ID
	}
	return ids
}

// EffectiveApproval returns the most recent approval that is currently in
// force for the plan's artifact digest, or nil when none is.
//
// A plan may hold several approvals in its history (re-approvals after
// staleness); only the newest granted approval that still matches the
// current artifact digest is effective.
func (p *Plan) EffectiveApproval() *PublicationApproval {
	for i := len(p.Approvals) - 1; i >= 0; i-- {
		a := &p.Approvals[i]
		if a.Status == ApprovalStatusGranted && a.MatchesDigest(string(p.Artifact.Digest)) {
			return a
		}
	}
	return nil
}

// hasEffectiveApprovalFor reports whether any approval in force covers the
// target for the plan's current artifact digest.
func (p *Plan) hasEffectiveApprovalFor(target TargetID) bool {
	for i := len(p.Approvals) - 1; i >= 0; i-- {
		a := &p.Approvals[i]
		if a.EffectiveFor(string(p.Artifact.Digest), target) {
			return true
		}
	}
	return false
}

// AuthorizesPublish is the single authorization gate a publisher must pass
// before invoking any external side effect for a target.
//
// It returns nil (authorized) only when ALL of the following hold:
//
//  1. the plan is in a state that permits publication (approved, or a
//     resumed/publishing cycle);
//  2. the recorded gate evidence is passing (succeeded run, passing final
//     check, READY review) and still binds to the plan's artifact;
//  3. an effective human approval exists — granted, bound to the exact
//     artifact digest, and scoped to the target;
//  4. the content that will actually be sent (currentDigest, computed by
//     the caller from the exact bytes about to go out) still equals the
//     plan's recorded artifact digest.
//
// Rule 4 is the staleness check: an artifact that changed after approval
// (or after the plan baseline) produces a different digest, and no amount
// of re-serialization of the old approval can bridge the gap. A stale
// approval cannot authorize modified content — by construction.
func (p *Plan) AuthorizesPublish(target TargetID, currentDigest string) error {
	if p == nil {
		return errors.New("publication plan is nil")
	}
	if err := p.ID.Validate(); err != nil {
		return fmt.Errorf("publication plan ID is invalid: %w", err)
	}
	if !p.Status.Valid() {
		return fmt.Errorf("publication plan %s is not in a valid state (status %q)", p.ID, p.Status)
	}
	if p.Status != StatusApproved && p.Status != StatusPublishing {
		return fmt.Errorf("plan %s is %s; publication requires an approved plan (statuses: approved, publishing)", p.ID, p.Status)
	}
	if !p.Gate.Passing() {
		return fmt.Errorf("plan %s gate evidence is not passing (run %s, final check %s, review ready %t); the artifact is not READY",
			p.ID, p.Gate.RunStatus, p.Gate.FinalCheck, p.Gate.ReviewReady)
	}
	if !p.Gate.ArtifactDigest.Matches(string(p.Artifact.Digest)) {
		return fmt.Errorf("plan %s gate evidence binds digest %q but the artifact baseline is %q; re-gather the gate from the current artifact",
			p.ID, p.Gate.ArtifactDigest, p.Artifact.Digest)
	}
	t, ok := p.Target(target)
	if !ok {
		return fmt.Errorf("plan %s has no target %q", p.ID, target)
	}
	if !p.hasEffectiveApprovalFor(target) {
		return fmt.Errorf("plan %s has no effective approval for target %q (artifact digest %q); an explicit human approval bound to the exact artifact content is required",
			p.ID, target, p.Artifact.Digest)
	}
	if !p.Artifact.Digest.Matches(currentDigest) {
		return fmt.Errorf("artifact content changed after the plan baseline: current digest %q does not match approved digest %q; the approval is stale and the artifact must be re-approved before publishing",
			currentDigest, p.Artifact.Digest)
	}
	switch t.Status {
	case StatusPlanned:
		return nil // first publication of this target
	case StatusFailed, StatusPartiallySucceeded:
		return nil // retry is allowed; the publisher must deduplicate against the existing receipt
	case StatusSucceeded:
		receipt := t.Receipt
		if receipt != nil && receipt.ExternalRecordID != "" {
			return fmt.Errorf("target %q was already published (receipt %s, external record %q); re-publishing requires a new plan", target, receipt.ID, receipt.ExternalRecordID)
		}
		return fmt.Errorf("target %q was already published (receipt %s); re-publishing requires a new plan", target, t.Receipt.ID)
	case StatusSkipped:
		return fmt.Errorf("target %q was skipped (%s) and is not part of this publication", target, t.SkipReason)
	case StatusCancelled:
		return fmt.Errorf("target %q was cancelled and is not part of this publication cycle", target)
	default: // StatusPublishing: an attempt is already in flight for it.
		return fmt.Errorf("target %q already has a publication attempt in flight; end it (abort) before starting another", target)
	}
}

// ---------------------------------------------------------------------------
// Plan mutators (state machine)
// ---------------------------------------------------------------------------

// touch validates the timestamp against the plan clock and advances
// updated_at. It rejects timestamps earlier than the last update so plans
// can never record a backwards-moving history.
func (p *Plan) touch(at time.Time) error {
	ts := run.NewTimestamp(at)
	if ts.IsZero() {
		return errors.New("timestamp is required")
	}
	if ts.Before(p.UpdatedAt) {
		return fmt.Errorf("timestamp regression: %s is earlier than last update %s", ts, p.UpdatedAt)
	}
	p.UpdatedAt = ts
	return nil
}

// assertActive rejects mutation of terminal plans: a finished plan is a
// historical record of what was published where and must not be silently
// rewritten.
func (p *Plan) assertActive() error {
	if p.Status.Terminal() {
		return fmt.Errorf("plan %s is %s; terminal plans are immutable (publishing again requires a new plan)", p.ID, p.Status)
	}
	return nil
}

// Approve records an explicit human approval and moves the plan to
// approved.
//
// The approval must be granted, must bind to the plan's current artifact
// digest exactly, and its scope must reference existing targets. The plan's
// recorded gate evidence must still be passing and still match the plan's
// artifact digest — so a plan whose artifact was re-baselined (content
// changed) cannot be approved against stale gate evidence: the gates must
// be re-gathered first (SetGate).
//
// Repeated calls are allowed (e.g. re-approval after an approval went stale
// or was revoked); every approval is appended to the bounded history, and
// the most recent effective one governs.
func (p *Plan) Approve(a *PublicationApproval, at time.Time) error {
	if a == nil {
		return errors.New("approval is required")
	}
	if err := p.assertActive(); err != nil {
		return err
	}
	if p.Status != StatusPlanned && p.Status != StatusApproved {
		return fmt.Errorf("invalid plan transition: %q -> %q", p.Status, StatusApproved)
	}
	if err := a.Validate(); err != nil {
		return fmt.Errorf("invalid approval: %w", err)
	}
	if a.Status != ApprovalStatusGranted {
		return fmt.Errorf("approval must be granted (it is %q)", a.Status)
	}
	if a.PlanID != p.ID {
		return fmt.Errorf("approval %q belongs to plan %q, not %q", a.ID, a.PlanID, p.ID)
	}
	if !a.MatchesDigest(string(p.Artifact.Digest)) {
		return fmt.Errorf("approval binds digest %q but the plan's artifact is %q; approve the exact current artifact content", a.ArtifactDigest, p.Artifact.Digest)
	}
	if !p.Gate.Passing() {
		return errors.New("cannot approve: the plan's gate evidence is not passing (the artifact is not READY)")
	}
	if !p.Gate.ArtifactDigest.Matches(string(p.Artifact.Digest)) {
		return fmt.Errorf("cannot approve: the plan's gate evidence binds digest %q but the artifact baseline is %q; re-gather the gate from the current artifact (SetGate) first", p.Gate.ArtifactDigest, p.Artifact.Digest)
	}
	for _, existing := range p.Approvals {
		if existing.ID == a.ID {
			return fmt.Errorf("duplicate approval ID %q", a.ID)
		}
	}
	for _, target := range a.Targets {
		if _, ok := p.Target(target); !ok {
			return fmt.Errorf("approval scope references unknown target %q", target)
		}
	}
	// Normalize bookkeeping fields so the stored approval is canonical.
	a.PlanID = p.ID
	a.GrantedAt = run.NewTimestamp(at)

	p.Approvals = append(p.Approvals, *a)
	trimApprovals(&p.Approvals)
	if err := p.transitionPlan(StatusApproved, at); err != nil {
		p.Approvals = p.Approvals[:len(p.Approvals)-1] // roll back
		return err
	}
	return nil
}

// RevokeApproval withdraws a granted approval on request.
//
// The approval is recorded as revoked (with the reason). If the plan was
// approved and no other effective approval remains, the plan falls back to
// planned: an unapproved plan cannot stay approved.
func (p *Plan) RevokeApproval(id ApprovalID, at time.Time, reason string) error {
	if err := p.assertActive(); err != nil {
		return err
	}
	var approval *PublicationApproval
	for i := range p.Approvals {
		if p.Approvals[i].ID == id {
			approval = &p.Approvals[i]
			break
		}
	}
	if approval == nil {
		return fmt.Errorf("plan %s has no approval %q", p.ID, id)
	}
	if approval.Status != ApprovalStatusGranted {
		return fmt.Errorf("approval %q is %s; only granted approvals can be revoked", id, approval.Status)
	}
	if reason == "" {
		return errors.New("revocation reason is required")
	}
	approval.Invalidate(ApprovalStatusRevoked, reason, at)
	if err := p.touch(at); err != nil {
		return err
	}
	if p.Status == StatusApproved && p.EffectiveApproval() == nil {
		if err := p.transitionPlan(StatusPlanned, at); err != nil {
			return err
		}
	}
	return nil
}

// RebaselineArtifact updates the plan's artifact identity to new content
// (a re-rendered or edited artifact) and mechanically invalidates every
// approval that no longer matches.
//
// This is the contract-level implementation of "changing an artifact after
// approval invalidates approval": the digest binding is checked, not
// trusted. After a rebaseline to different bytes, no approval is effective
// until a human approves the new content, and the gate must be re-gathered
// to match (see Approve).
//
// The artifact reference (ID, kind, path) must be unchanged — only the
// content (digest, size, version) moves. The plan must be non-terminal.
func (p *Plan) RebaselineArtifact(artifact ArtifactIdentity, at time.Time) error {
	if err := p.assertActive(); err != nil {
		return err
	}
	if err := artifact.Validate(); err != nil {
		return fmt.Errorf("invalid artifact: %w", err)
	}
	if artifact.ID != p.Artifact.ID || artifact.Kind != p.Artifact.Kind || artifact.Path != p.Artifact.Path {
		return fmt.Errorf("rebaseline may only change the artifact content: the artifact reference (id %q, kind %q, path %q) must not change", p.Artifact.ID, p.Artifact.Kind, p.Artifact.Path)
	}
	oldDigest := p.Artifact.Digest
	if !oldDigest.Matches(string(artifact.Digest)) {
		// The content changed: every approval bound to the old digest is
		// stale. Staleness is a mechanical fact, not a judgment call.
		for i := range p.Approvals {
			a := &p.Approvals[i]
			if a.Status == ApprovalStatusGranted && !a.MatchesDigest(string(artifact.Digest)) {
				a.Invalidate(ApprovalStatusStale,
					fmt.Sprintf("artifact %s digest changed from %s to %s", p.Artifact.ID, oldDigest, artifact.Digest), at)
			}
		}
		msg := fmt.Sprintf("artifact %s content changed from %s to %s; approvals no longer match the current content", p.Artifact.ID, oldDigest, artifact.Digest)
		if err := p.AddWarning("artifact-rebaselined", msg, at); err != nil {
			return err
		}
	}
	p.Artifact = artifact
	if err := p.touch(at); err != nil {
		return err
	}
	if p.Status == StatusApproved && p.EffectiveApproval() == nil {
		if err := p.transitionPlan(StatusPlanned, at); err != nil {
			return err
		}
	}
	return nil
}

// SetGate re-records the pre-publication gate evidence for the plan's
// current artifact baseline.
//
// After a rebaseline, the original gate evidence (gathered for the old
// bytes) no longer describes what would be published; the run's gates must
// be re-gathered from the current record. The new evidence must be passing
// and must bind to the plan's current artifact digest.
func (p *Plan) SetGate(gate GateEvidence, at time.Time) error {
	if err := p.assertActive(); err != nil {
		return err
	}
	if !gate.Passing() {
		return errors.New("gate evidence must be passing (succeeded run, passing final check, READY review)")
	}
	if err := gate.Validate(); err != nil {
		return fmt.Errorf("invalid gate evidence: %w", err)
	}
	if !gate.ArtifactDigest.Matches(string(p.Artifact.Digest)) {
		return fmt.Errorf("gate evidence binds digest %q but the plan's artifact baseline is %q", gate.ArtifactDigest, p.Artifact.Digest)
	}
	p.Gate = gate
	return p.touch(at)
}

// StartPublishing begins a publication cycle: the plan moves from approved
// to publishing.
//
// A cycle is the window in which targets may be started. A plan that is
// approved but never cycled never publishes — the transition is explicit
// and is the point at which a publisher is permitted to act.
func (p *Plan) StartPublishing(at time.Time) error {
	if err := p.assertActive(); err != nil {
		return err
	}
	if p.Status != StatusApproved {
		return fmt.Errorf("invalid plan transition: %q -> %q (StartPublishing requires an approved plan)", p.Status, StatusPublishing)
	}
	return p.transitionPlan(StatusPublishing, at)
}

// Retry resumes a finished plan whose targets did not all finish: a failed
// or partially succeeded plan moves back to publishing so the targets that
// did not succeed can be retried in a new cycle.
//
// Targets that already succeeded keep their receipts and are never
// re-executed (see AuthorizesPublish); a fully succeeded plan cannot be
// retried — it is a terminal record, and publishing again is a new plan.
func (p *Plan) Retry(at time.Time) error {
	if p.Status != StatusFailed && p.Status != StatusPartiallySucceeded {
		return fmt.Errorf("invalid plan transition: %q -> %q (Retry requires a failed or partially succeeded plan)", p.Status, StatusPublishing)
	}
	return p.transitionPlan(StatusPublishing, at)
}

// Cancel stops the plan on request, from any active state.
//
// Targets in flight are recorded as cancelled (their running attempt is
// ended); targets that never started keep their status. The reason is
// required and redacted.
func (p *Plan) Cancel(at time.Time, reason string) error {
	if reason == "" {
		return errors.New("cancellation reason is required")
	}
	if err := p.assertActive(); err != nil {
		return err
	}
	if !ValidPlanTransition(p.Status, StatusCancelled) {
		return fmt.Errorf("invalid plan transition: %q -> %q", p.Status, StatusCancelled)
	}
	for i := range p.Targets {
		if p.Targets[i].Status == StatusPublishing {
			if err := p.AbortTarget(p.Targets[i].ID, at); err != nil {
				return fmt.Errorf("interrupting target %q during cancellation: %w", p.Targets[i].ID, err)
			}
		}
	}
	summary := run.RedactSecrets(reason)
	if len(summary) > maxNoteLen {
		return fmt.Errorf("cancellation reason exceeds %d characters", maxNoteLen)
	}
	if run.LooksLikeSecret(summary) {
		return fmt.Errorf("cancellation reason contains secret-looking material; redact it before recording")
	}
	if err := p.transitionPlan(StatusCancelled, at); err != nil {
		return err
	}
	p.TerminalReason = summary
	return nil
}

// transitionPlan moves the plan to a new status through the validated state
// machine, advancing updated_at and, for terminal states, recording
// finished_at.
func (p *Plan) transitionPlan(to PublicationStatus, at time.Time) error {
	if p.Status == to {
		return p.touch(at)
	}
	if !ValidPlanTransition(p.Status, to) {
		return fmt.Errorf("invalid plan transition: %q -> %q", p.Status, to)
	}
	if err := p.touch(at); err != nil {
		return err
	}
	p.Status = to
	if to.Terminal() {
		ts := run.NewTimestamp(at)
		p.FinishedAt = &ts
	} else {
		p.FinishedAt = nil
	}
	return nil
}

// SkipTarget records a deliberate decision not to publish one target.
//
// Valid from the planned state, in any active plan. A reason is required.
func (p *Plan) SkipTarget(id TargetID, reason string, at time.Time) error {
	if reason == "" {
		return fmt.Errorf("skip reason is required for target %q", id)
	}
	if err := p.assertActive(); err != nil {
		return err
	}
	t, ok := p.Target(id)
	if !ok {
		return fmt.Errorf("plan %s has no target %q", p.ID, id)
	}
	if !ValidTargetTransition(t.Status, StatusSkipped) {
		return fmt.Errorf("invalid target transition for %q: %q -> %q", id, t.Status, StatusSkipped)
	}
	t.Status = StatusSkipped
	t.SkipReason = run.RedactSecrets(reason)
	return p.touch(at)
}

// AbortTarget ends an in-flight target as cancelled.
//
// This is the crash-recovery bookkeeping: a target whose cycle was
// interrupted (process died, operator stop) is ended as cancelled before a
// new attempt can start. Publishers and resume tooling call it for each
// dangling target.
func (p *Plan) AbortTarget(id TargetID, at time.Time) error {
	if err := p.assertActive(); err != nil {
		return err
	}
	t, ok := p.Target(id)
	if !ok {
		return fmt.Errorf("plan %s has no target %q", p.ID, id)
	}
	if !ValidTargetTransition(t.Status, StatusCancelled) {
		return fmt.Errorf("invalid target transition for %q: %q -> %q", id, t.Status, StatusCancelled)
	}
	if err := p.finishTargetAttempt(t, AttemptStatusCancelled, at, nil, nil); err != nil {
		return fmt.Errorf("target %q: %w", id, err)
	}
	return p.touch(at)
}

// StartTarget starts (or restarts after failure or partial success) a
// target's publication, appending a new running attempt.
//
// The plan must be in a publishing cycle (StartPublishing or Retry). A
// target that already succeeded, was skipped, or was cancelled cannot be
// started again: that is the idempotency rule — a finished target is never
// re-executed within its plan.
func (p *Plan) StartTarget(id TargetID, at time.Time) error {
	if err := p.assertActive(); err != nil {
		return err
	}
	if p.Status != StatusPublishing {
		return fmt.Errorf("target %q cannot start: plan %s is %s (start a cycle with StartPublishing, or resume with Retry)", id, p.ID, p.Status)
	}
	t, ok := p.Target(id)
	if !ok {
		return fmt.Errorf("plan %s has no target %q", p.ID, id)
	}
	if !ValidTargetTransition(t.Status, StatusPublishing) {
		return fmt.Errorf("invalid target transition for %q: %q -> %q", id, t.Status, StatusPublishing)
	}
	if err := p.beginTargetAttempt(t, at); err != nil {
		return fmt.Errorf("target %q: %w", id, err)
	}
	return p.touch(at)
}

// SucceedTarget records the target's current attempt as succeeded, storing
// the publisher's receipt.
//
// The receipt is the external proof (URI / record ID / content digest). Its
// content digest must equal the plan's approved artifact digest: a receipt
// for different bytes means the publisher sent something the human did not
// approve, and it is rejected.
func (p *Plan) SucceedTarget(id TargetID, receipt *PublicationReceipt, at time.Time) error {
	if err := p.assertActive(); err != nil {
		return err
	}
	t, ok := p.Target(id)
	if !ok {
		return fmt.Errorf("plan %s has no target %q", p.ID, id)
	}
	if !ValidTargetTransition(t.Status, StatusSucceeded) {
		return fmt.Errorf("invalid target transition for %q: %q -> %q", id, t.Status, StatusSucceeded)
	}
	if receipt == nil {
		return fmt.Errorf("a succeeded target requires a receipt (target %q)", id)
	}
	if err := receipt.Validate(); err != nil {
		return fmt.Errorf("target %q: invalid receipt: %w", id, err)
	}
	if !receipt.ContentDigest.Matches(string(p.Artifact.Digest)) {
		return fmt.Errorf("target %q: receipt content digest %q does not match the approved artifact digest %q; a publisher must send exactly what was approved", id, receipt.ContentDigest, p.Artifact.Digest)
	}
	if err := p.finishTargetAttempt(t, AttemptStatusSucceeded, at, nil, receipt); err != nil {
		return fmt.Errorf("target %q: %w", id, err)
	}
	return p.touch(at)
}

// FailTarget records the target's current attempt as failed.
//
// The failure is recorded on the target (its error summary), on the
// attempt, and in the plan-level error log (redacted, bounded). The target
// remains retryable through StartTarget in a resumed cycle.
func (p *Plan) FailTarget(id TargetID, code, message string, at time.Time) error {
	if err := p.assertActive(); err != nil {
		return err
	}
	t, ok := p.Target(id)
	if !ok {
		return fmt.Errorf("plan %s has no target %q", p.ID, id)
	}
	if !ValidTargetTransition(t.Status, StatusFailed) {
		return fmt.Errorf("invalid target transition for %q: %q -> %q", id, t.Status, StatusFailed)
	}
	summary, err := newErrorSummary(code, message, at)
	if err != nil {
		return fmt.Errorf("target %q: %w", id, err)
	}
	if err := p.finishTargetAttempt(t, AttemptStatusFailed, at, &summary, nil); err != nil {
		return fmt.Errorf("target %q: %w", id, err)
	}
	p.Errors = append(p.Errors, summary)
	trimErrorLog(&p.Errors)
	return p.touch(at)
}

// PartialTarget records the target's current attempt as partially
// succeeded: the publisher confirmed some external effect (e.g. most of a
// thread posted) but not all of it.
//
// The target remains retryable: a resumed cycle re-executes it, and the
// publisher is expected to deduplicate against the receipt's external
// record IDs.
func (p *Plan) PartialTarget(id TargetID, receipt *PublicationReceipt, at time.Time) error {
	if err := p.assertActive(); err != nil {
		return err
	}
	t, ok := p.Target(id)
	if !ok {
		return fmt.Errorf("plan %s has no target %q", p.ID, id)
	}
	if !ValidTargetTransition(t.Status, StatusPartiallySucceeded) {
		return fmt.Errorf("invalid target transition for %q: %q -> %q", id, t.Status, StatusPartiallySucceeded)
	}
	if receipt != nil {
		if err := receipt.Validate(); err != nil {
			return fmt.Errorf("target %q: invalid receipt: %w", id, err)
		}
		if !receipt.ContentDigest.Matches(string(p.Artifact.Digest)) {
			return fmt.Errorf("target %q: receipt content digest %q does not match the approved artifact digest %q; a publisher must send exactly what was approved", id, receipt.ContentDigest, p.Artifact.Digest)
		}
	}
	if err := p.finishTargetAttempt(t, AttemptStatusPartiallySucceeded, at, nil, receipt); err != nil {
		return fmt.Errorf("target %q: %w", id, err)
	}
	return p.touch(at)
}

// RecordDryRun stores a rehearsal result on a target.
//
// A dry run has no external side effects and therefore does not change any
// status: the plan may be planned or approved. The result must have
// validated the plan's current artifact digest — a rehearsal of different
// bytes proves nothing about what would be published.
func (p *Plan) RecordDryRun(id TargetID, result *DryRunResult, at time.Time) error {
	if err := p.assertActive(); err != nil {
		return err
	}
	if p.Status != StatusPlanned && p.Status != StatusApproved {
		return fmt.Errorf("dry runs are only recorded before a publication cycle (plan %s is %s)", p.ID, p.Status)
	}
	if result == nil {
		return fmt.Errorf("dry run result is required for target %q", id)
	}
	if err := result.Validate(); err != nil {
		return fmt.Errorf("target %q: invalid dry run result: %w", id, err)
	}
	if !result.ContentDigest.Matches(string(p.Artifact.Digest)) {
		return fmt.Errorf("target %q: dry run validated digest %q but the plan's artifact is %q; the rehearsal proves nothing about the current content", id, result.ContentDigest, p.Artifact.Digest)
	}
	t, ok := p.Target(id)
	if !ok {
		return fmt.Errorf("plan %s has no target %q", p.ID, id)
	}
	result.PerformedAt = run.NewTimestamp(at)
	t.DryRun = result
	return p.touch(at)
}

// beginTargetAttempt appends a new running attempt to the target and moves
// it to publishing.
func (p *Plan) beginTargetAttempt(t *PublicationTarget, at time.Time) error {
	ts := run.NewTimestamp(at)
	t.Attempts = append(t.Attempts, PublicationAttempt{
		Number:    len(t.Attempts) + 1,
		Status:    AttemptStatusRunning,
		StartedAt: ts,
	})
	trimAttempts(&t.Attempts)
	t.Status = StatusPublishing
	return nil
}

// finishTargetAttempt ends the target's current running attempt with the
// given outcome, syncing the target status and (when applicable) its
// receipt / error.
func (p *Plan) finishTargetAttempt(t *PublicationTarget, status AttemptStatus, at time.Time, summary *run.ErrorSummary, receipt *PublicationReceipt) error {
	if len(t.Attempts) == 0 {
		return fmt.Errorf("target %q has no running attempt", t.ID)
	}
	last := &t.Attempts[len(t.Attempts)-1]
	if last.Status != AttemptStatusRunning {
		return fmt.Errorf("target %q: current attempt is %q, not running", t.ID, last.Status)
	}
	ts := run.NewTimestamp(at)
	last.Status = status
	last.FinishedAt = &ts
	if summary != nil {
		last.Error = summary
		t.Error = summary
	} else if status != AttemptStatusFailed {
		t.Error = nil
	}
	if receipt != nil {
		last.Receipt = receipt
		t.Receipt = receipt
	}
	switch status {
	case AttemptStatusSucceeded:
		t.Status = StatusSucceeded
	case AttemptStatusFailed:
		t.Status = StatusFailed
	case AttemptStatusPartiallySucceeded:
		t.Status = StatusPartiallySucceeded
	case AttemptStatusCancelled:
		t.Status = StatusCancelled
	default:
		t.Status = StatusPublishing
	}
	return nil
}

// RefreshStatus re-derives the plan's status from its targets after a
// publication cycle and records the terminal outcome when the cycle is
// complete.
//
// Aggregation (all targets terminal):
//
//   - every target succeeded (or skipped, with at least one succeeded):
//     the plan succeeds;
//   - at least one succeeded and at least one failed / partially succeeded
//     / cancelled: the plan is partially succeeded;
//   - no target succeeded: the plan fails (it skipped nothing worth
//     publishing);
//   - every target skipped: the plan is skipped.
//
// With any target still active, the plan keeps its current non-terminal
// status (the cycle is not complete). RefreshStatus is the only way a plan
// reaches a terminal aggregated state; callers persist after calling it so
// the durable record is always consistent.
func (p *Plan) RefreshStatus(at time.Time) error {
	if err := p.touch(at); err != nil {
		return err
	}
	allTerminal := true
	for _, t := range p.Targets {
		if !t.Status.Terminal() {
			allTerminal = false
			break
		}
	}
	if !allTerminal {
		// The cycle is not complete: keep the current status, but make
		// sure it is one a mid-cycle plan may hold.
		if p.Status.Terminal() {
			return fmt.Errorf("plan %s is %s but %d target(s) are still active; terminal plans require every target to be terminal", p.ID, p.Status, len(p.Targets))
		}
		return nil
	}

	succeeded, failed, skipped := 0, 0, 0
	for _, t := range p.Targets {
		switch t.Status {
		case StatusSucceeded:
			succeeded++
		case StatusSkipped:
			skipped++
		default: // failed, partially_succeeded, cancelled
			failed++
		}
	}

	var to PublicationStatus
	var reason string
	switch {
	case failed == 0 && succeeded > 0:
		to = StatusSucceeded
		reason = fmt.Sprintf("%d of %d target(s) published; %d skipped", succeeded, len(p.Targets), skipped)
	case failed == 0: // all skipped
		to = StatusSkipped
		reason = fmt.Sprintf("all %d target(s) skipped; nothing published", len(p.Targets))
	case succeeded > 0:
		to = StatusPartiallySucceeded
		reason = fmt.Sprintf("%d of %d target(s) published; %d did not complete; %d skipped", succeeded, len(p.Targets), failed, skipped)
	default:
		to = StatusFailed
		reason = fmt.Sprintf("no target published; %d target(s) failed/cancelled; %d skipped", failed, skipped)
	}
	if p.Status == to {
		p.TerminalReason = run.RedactSecrets(reason)
		return nil
	}
	if !ValidPlanTransition(p.Status, to) {
		return fmt.Errorf("invalid plan transition: %q -> %q", p.Status, to)
	}
	if err := p.transitionPlan(to, at); err != nil {
		return err
	}
	p.TerminalReason = run.RedactSecrets(reason)
	return nil
}

// AddWarning records a plan-level warning (redacted, log bounded).
func (p *Plan) AddWarning(code, message string, at time.Time) error {
	if err := p.assertActive(); err != nil {
		return err
	}
	w := run.NewWarning(code, message, at)
	if err := w.Validate(); err != nil {
		return fmt.Errorf("invalid warning: %w", err)
	}
	if err := p.touch(at); err != nil {
		return err
	}
	p.Warnings = append(p.Warnings, w)
	trimWarningLog(&p.Warnings)
	return nil
}

// AddError records a plan-level error summary (redacted, log bounded).
func (p *Plan) AddError(code, message string, at time.Time) error {
	if err := p.assertActive(); err != nil {
		return err
	}
	e := run.NewErrorSummary(code, message, at)
	if err := e.Validate(); err != nil {
		return fmt.Errorf("invalid error summary: %w", err)
	}
	if err := p.touch(at); err != nil {
		return err
	}
	p.Errors = append(p.Errors, e)
	trimErrorLog(&p.Errors)
	return nil
}

// ---------------------------------------------------------------------------
// History trimming (bounded, in place)
// ---------------------------------------------------------------------------

// trimApprovals enforces the per-plan approval history bound (oldest first).
func trimApprovals(approvals *[]PublicationApproval) {
	if len(*approvals) <= MaxApprovalsPerPlan {
		return
	}
	drop := len(*approvals) - MaxApprovalsPerPlan
	*approvals = (*approvals)[drop:]
}

// trimErrorLog enforces the plan error log bound in place (oldest first).
func trimErrorLog(ess *[]run.ErrorSummary) {
	if len(*ess) <= MaxPlanErrors {
		return
	}
	*ess = (*ess)[len(*ess)-MaxPlanErrors:]
}

// trimWarningLog enforces the plan warning log bound in place (oldest first).
func trimWarningLog(ws *[]run.Warning) {
	if len(*ws) <= MaxPlanWarnings {
		return
	}
	*ws = (*ws)[len(*ws)-MaxPlanWarnings:]
}

// ---------------------------------------------------------------------------
// Plan validation
// ---------------------------------------------------------------------------

// Validate performs full structural validation of the plan, including every
// cross-field invariant:
//
//   - schema version, IDs, workspace, and every sub-contract;
//   - gate evidence passes the digest binding;
//   - targets: count bounds, unique IDs, valid dependency graph (no cycles);
//   - approvals: bounds, unique IDs, scope references;
//   - status consistency between plan and targets;
//   - timestamp ordering and terminal-state bookkeeping.
func (p *Plan) Validate() error {
	if p == nil {
		return errors.New("publication plan is nil")
	}
	var errs []error
	addf := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if p.SchemaVersion == "" {
		errs = append(errs, errors.New("schema_version is required"))
	} else {
		major, err := SchemaMajor(p.SchemaVersion)
		if err != nil {
			errs = append(errs, fmt.Errorf("schema_version %q is not a semantic version: %v", p.SchemaVersion, err))
		} else if major != SchemaMajorCurrent {
			errs = append(errs, fmt.Errorf("%w: plan is %s, this version understands major %d", ErrUnsupportedSchemaVersion, p.SchemaVersion, SchemaMajorCurrent))
		}
	}

	if err := p.ID.Validate(); err != nil {
		errs = append(errs, err)
	}
	if p.RunID == "" {
		errs = append(errs, errors.New("run_id is required"))
	} else if err := p.RunID.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("run_id: %w", err))
	}
	if p.Workspace != "" && !workspaceNameRe.MatchString(p.Workspace) {
		addf("workspace name %q is invalid: use letters, digits, '.', '-', '_' (1-64 chars)", p.Workspace)
	}
	if !p.Status.Valid() {
		addf("invalid plan status %q", p.Status)
	}

	if err := p.Artifact.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := p.Gate.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("gate evidence: %w", err))
	}
	if !p.Gate.ArtifactDigest.Matches(string(p.Artifact.Digest)) {
		addf("gate evidence digest %q does not match artifact digest %q", p.Gate.ArtifactDigest, p.Artifact.Digest)
	}

	// Targets.
	if len(p.Targets) < 1 {
		errs = append(errs, errors.New("a plan requires at least one target"))
	}
	if len(p.Targets) > MaxTargetsPerPlan {
		addf("plan holds %d targets, exceeding the bound of %d", len(p.Targets), MaxTargetsPerPlan)
	}
	for i, t := range p.Targets {
		if err := t.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("targets[%d]: %w", i, err))
		}
		// Every receipt on a target — final, partial, or carried forward
		// as resume state — must prove exactly this plan's artifact bytes:
		// a receipt from different content is stale or transplanted and a
		// document that carries one fails closed on load (it is never
		// resumed from).
		if t.Receipt != nil && !t.Receipt.ContentDigest.Matches(string(p.Artifact.Digest)) {
			addf("target %q receipt digests %q but the plan's artifact is %q; a receipt must prove the approved content", t.ID, t.Receipt.ContentDigest, p.Artifact.Digest)
		}
	}
	if err := validateTargetGraph(p.Targets); err != nil {
		errs = append(errs, err)
	}

	// Approvals.
	if len(p.Approvals) > MaxApprovalsPerPlan {
		addf("plan holds %d approvals, exceeding the bound of %d", len(p.Approvals), MaxApprovalsPerPlan)
	}
	approvalIDs := make(map[ApprovalID]bool, len(p.Approvals))
	for i, a := range p.Approvals {
		if err := a.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("approvals[%d]: %w", i, err))
		}
		if approvalIDs[a.ID] {
			addf("duplicate approval ID %q", a.ID)
		}
		approvalIDs[a.ID] = true
		if a.PlanID != p.ID {
			addf("approval %q references plan %q, not this plan (%q)", a.ID, a.PlanID, p.ID)
		}
		for _, target := range a.Targets {
			if _, ok := p.Target(target); !ok {
				addf("approval %q scopes unknown target %q", a.ID, target)
			}
		}
	}

	// Status consistency: plan state versus target states.
	switch p.Status {
	case StatusPlanned:
		for _, t := range p.Targets {
			if t.Status != StatusPlanned && t.Status != StatusSkipped {
				addf("plan is %s but target %q is %q", p.Status, t.ID, t.Status)
			}
		}
	case StatusApproved:
		for _, t := range p.Targets {
			if t.Status != StatusPlanned && t.Status != StatusSkipped {
				addf("plan is %s but target %q is %q", p.Status, t.ID, t.Status)
			}
		}
		// An approved plan must carry the approval that authorized it: the
		// state machine never produces an approved plan without an
		// effective approval (revocation or staleness fall back to
		// planned), so one missing is a tampered document.
		if p.EffectiveApproval() == nil {
			addf("plan is approved but has no effective approval for artifact digest %q", p.Artifact.Digest)
		}
	case StatusPublishing:
		// A mid-cycle plan may hold any mix of target states, including
		// the transient "every target finished, aggregation pending" state
		// (resolved by RefreshStatus before persistence). Approval may be
		// absent: it can be revoked mid-cycle, and in-flight targets
		// legitimately finish after the revocation.
	case StatusSucceeded:
		if err := validatePlanAggregate(p.Targets, StatusSucceeded); err != nil {
			errs = append(errs, err)
		}
	case StatusFailed:
		if err := validatePlanAggregate(p.Targets, StatusFailed); err != nil {
			errs = append(errs, err)
		}
	case StatusPartiallySucceeded:
		if err := validatePlanAggregate(p.Targets, StatusPartiallySucceeded); err != nil {
			errs = append(errs, err)
		}
	case StatusCancelled:
		for _, t := range p.Targets {
			if t.Status == StatusPublishing {
				addf("plan is cancelled but target %q is still publishing", t.ID)
			}
		}
	case StatusSkipped:
		for _, t := range p.Targets {
			if t.Status != StatusSkipped {
				addf("plan is skipped but target %q is %q", t.ID, t.Status)
			}
		}
	}
	if p.Status.Terminal() && p.TerminalReason == "" {
		addf("terminal plan requires a terminal reason")
	} else if run.LooksLikeSecret(p.TerminalReason) {
		addf("terminal reason contains secret-looking material; redact it before recording")
	}
	if len(p.TerminalReason) > maxNoteLen {
		addf("terminal reason exceeds %d characters", maxNoteLen)
	}

	// Bounded logs.
	if len(p.Errors) > MaxPlanErrors {
		addf("plan error log holds %d entries, exceeding the bound of %d", len(p.Errors), MaxPlanErrors)
	}
	for i, e := range p.Errors {
		if err := e.Validate(); err != nil {
			addf("errors[%d]: %v", i, err)
		}
	}
	if len(p.Warnings) > MaxPlanWarnings {
		addf("plan warning log holds %d entries, exceeding the bound of %d", len(p.Warnings), MaxPlanWarnings)
	}
	for i, w := range p.Warnings {
		if err := w.Validate(); err != nil {
			addf("warnings[%d]: %v", i, err)
		}
	}

	// Timestamps.
	if p.CreatedAt.IsZero() {
		addf("created_at is required")
	}
	if p.UpdatedAt.IsZero() {
		addf("updated_at is required")
	} else if !p.CreatedAt.IsZero() && p.UpdatedAt.Before(p.CreatedAt) {
		addf("updated_at is earlier than created_at")
	}
	if p.FinishedAt != nil {
		if p.FinishedAt.IsZero() {
			addf("finished_at must not be zero when set")
		} else if !p.Status.Terminal() {
			addf("finished_at is set but the plan is still %s", p.Status)
		} else if !p.CreatedAt.IsZero() && p.FinishedAt.Before(p.CreatedAt) {
			addf("finished_at is earlier than created_at")
		}
	} else if p.Status.Terminal() {
		addf("terminal plan requires finished_at")
	}

	return errors.Join(errs...)
}

// validatePlanAggregate checks that the terminal plan status matches the
// terminal target outcomes it aggregates to.
func validatePlanAggregate(targets []PublicationTarget, want PublicationStatus) error {
	succeeded, failed, skipped := 0, 0, 0
	for _, t := range targets {
		if !t.Status.Terminal() {
			return fmt.Errorf("plan is %s but target %q is still %s; every target must be terminal", want, t.ID, t.Status)
		}
		switch t.Status {
		case StatusSucceeded:
			succeeded++
		case StatusSkipped:
			skipped++
		default:
			failed++
		}
	}
	switch want {
	case StatusSucceeded:
		if failed > 0 || succeeded == 0 {
			return fmt.Errorf("plan is succeeded but targets aggregate to %d succeeded, %d failed/cancelled, %d skipped", succeeded, failed, skipped)
		}
	case StatusFailed:
		if succeeded > 0 || failed == 0 {
			return fmt.Errorf("plan is failed but targets aggregate to %d succeeded, %d failed/cancelled, %d skipped", succeeded, failed, skipped)
		}
	case StatusPartiallySucceeded:
		if succeeded == 0 || failed == 0 {
			return fmt.Errorf("plan is partially succeeded but targets aggregate to %d succeeded, %d failed/cancelled, %d skipped", succeeded, failed, skipped)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// JSON I/O
// ---------------------------------------------------------------------------

// MarshalPlan serializes a plan to indented JSON.
//
// Before writing, the plan is validated (including every secret detector),
// so a document that could not be re-read is never emitted.
func MarshalPlan(p *Plan) ([]byte, error) {
	if p == nil {
		return nil, errors.New("publication plan is nil")
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("invalid publication plan: %w", err)
	}
	return json.MarshalIndent(p, "", "  ")
}

// UnmarshalPlan parses a plan from JSON and validates it.
//
// It rejects documents whose schema major version is not understood
// (ErrUnsupportedSchemaVersion) and any document that fails structural
// validation, including unknown enum values and un-redacted secret-looking
// values. Fields this version does not know about are ignored (forward
// compatibility with future 0.x schemas); all fields it does know must be
// present and valid.
func UnmarshalPlan(data []byte) (*Plan, error) {
	var p Plan
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("invalid publication plan JSON: %w", err)
	}
	if p.SchemaVersion == "" {
		return nil, fmt.Errorf("%w: schema_version is required", ErrUnsupportedSchemaVersion)
	}
	major, err := SchemaMajor(p.SchemaVersion)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupportedSchemaVersion, err)
	}
	if major != SchemaMajorCurrent {
		return nil, fmt.Errorf("%w: plan is %s, this version understands major %d", ErrUnsupportedSchemaVersion, p.SchemaVersion, SchemaMajorCurrent)
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("invalid publication plan: %w", err)
	}
	return &p, nil
}
