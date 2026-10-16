package publication

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// ApprovalID is the stable identifier of a publication approval.
//
// Format: "appr-<timestamp>-<12 hex chars>", derived from the plan, the
// approving principal, the approved artifact digest, and the grant time.
// Two approvals of the same artifact by different principals (or at
// different times) always derive different IDs, so the approval history is
// unambiguous in the audit record.
type ApprovalID string

// ApprovalIDPrefix is the required prefix of every approval ID.
const ApprovalIDPrefix = "appr-"

// NewApprovalID derives a stable approval ID.
func NewApprovalID(planID PublicationID, approvedBy string, digest Digest, grantedAt time.Time) ApprovalID {
	return ApprovalID(deriveID(ApprovalIDPrefix, grantedAt, string(planID), approvedBy, string(digest)))
}

// Validate checks that the approval ID is well-formed.
func (id ApprovalID) Validate() error {
	return validateIDValue("approval id", string(id), ApprovalIDPrefix)
}

// String returns the string representation of the approval ID.
func (id ApprovalID) String() string {
	return string(id)
}

// ApprovalMethod indicates how the human approval was given.
//
// The method is an audit detail: it records the surface the human used,
// without encoding any provider specifics.
type ApprovalMethod string

// Approval method values.
const (
	// ApprovalMethodCLI means the approval was given at the command line.
	ApprovalMethodCLI ApprovalMethod = "cli"

	// ApprovalMethodTUI means the approval was given in the terminal UI.
	ApprovalMethodTUI ApprovalMethod = "tui"

	// ApprovalMethodManual means an offline sign-off (the principal
	// approved by other means and the operator recorded it).
	ApprovalMethodManual ApprovalMethod = "manual"

	// ApprovalMethodCustom marks a non-standard approval surface.
	ApprovalMethodCustom ApprovalMethod = "custom"
)

// AllApprovalMethods lists every valid approval method.
var AllApprovalMethods = []ApprovalMethod{
	ApprovalMethodCLI,
	ApprovalMethodTUI,
	ApprovalMethodManual,
	ApprovalMethodCustom,
}

// Valid reports whether the approval method is a known value.
func (m ApprovalMethod) Valid() bool {
	for _, v := range AllApprovalMethods {
		if m == v {
			return true
		}
	}
	return false
}

// String returns the string representation of the approval method.
func (m ApprovalMethod) String() string {
	return string(m)
}

// ApprovalStatus indicates the standing of an approval.
//
// An approval is granted by a human and closes in one of two ways:
//   - revoked: the human (or operator on their behalf) withdrew it;
//   - stale: the artifact it bound to changed. Staleness is not a judgment;
//     it is a mechanical consequence of the digest no longer matching, and
//     it is the single rule that makes "changing an artifact after approval
//     invalidates approval" true.
type ApprovalStatus string

// Approval status values.
const (
	// ApprovalStatusGranted means the approval is in force and may
	// authorize publication of the artifact digest it bound to.
	ApprovalStatusGranted ApprovalStatus = "granted"

	// ApprovalStatusStale means the artifact changed after the grant: the
	// approved digest no longer matches the plan's current artifact digest.
	// A stale approval authorizes nothing until the human approves the new
	// content.
	ApprovalStatusStale ApprovalStatus = "stale"

	// ApprovalStatusRevoked means the approval was withdrawn on request.
	ApprovalStatusRevoked ApprovalStatus = "revoked"
)

// AllApprovalStatuses lists every valid approval status.
var AllApprovalStatuses = []ApprovalStatus{
	ApprovalStatusGranted,
	ApprovalStatusStale,
	ApprovalStatusRevoked,
}

// Valid reports whether the approval status is a known value.
func (s ApprovalStatus) Valid() bool {
	for _, v := range AllApprovalStatuses {
		if s == v {
			return true
		}
	}
	return false
}

// Closed reports whether the approval is no longer in force.
func (s ApprovalStatus) Closed() bool {
	return s == ApprovalStatusStale || s == ApprovalStatusRevoked
}

// String returns the string representation of the approval status.
func (s ApprovalStatus) String() string {
	return string(s)
}

// Approval constants.
const (
	maxApprovedByLen = 128
	maxApprovalNote  = 1024
)

// approvedByRe allows conservative principal identifiers: display names,
// user IDs, or email addresses.
var approvedByRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._@-]{0,127}$`)

// PublicationApproval is the explicit human authorization to publish.
//
// The approval is the v0.6 gate between a machine-prepared plan and any
// external side effect. Its binding is content-level, not document-level:
// the approval records the exact artifact digest the human saw. An approval
// is effective only while that digest still matches the plan's current
// artifact digest — so an artifact that changes after the grant (re-render,
// edit, resumed run) mechanically voids the approval.
//
// SECURITY: the approval carries no credentials. ApprovedBy is a principal
// identifier (redacted, pattern-checked); the method is an enum; and no
// token, cookie, or session material may ever appear in it.
type PublicationApproval struct {
	// ID is the stable approval identifier (see NewApprovalID).
	ID ApprovalID `json:"id"`

	// PlanID references the owning publication plan.
	PlanID PublicationID `json:"plan_id"`

	// ApprovedBy identifies the human principal who approved.
	ApprovedBy string `json:"approved_by"`

	// Method records how the approval was given.
	Method ApprovalMethod `json:"method"`

	// ArtifactDigest is the exact content digest the human approved.
	// Publication is authorized only for content hashing to this digest.
	ArtifactDigest Digest `json:"artifact_digest"`

	// Targets scopes the approval to specific targets. Empty means every
	// target in the plan.
	Targets []TargetID `json:"targets,omitempty"`

	// Note is an optional redacted annotation left by the approver.
	Note string `json:"note,omitempty"`

	// Status is the standing of the approval (granted / stale / revoked).
	Status ApprovalStatus `json:"status"`

	// GrantedAt is when the approval was given (UTC).
	GrantedAt run.Timestamp `json:"granted_at"`

	// InvalidatedAt is when the approval went stale or was revoked (UTC).
	// Nil while granted.
	InvalidatedAt *run.Timestamp `json:"invalidated_at,omitempty"`

	// InvalidationReason explains a stale or revoked approval (redacted).
	InvalidationReason string `json:"invalidation_reason,omitempty"`
}

// NewApproval builds a granted approval, redacting every free-text value.
//
// The digest must be the digest of the exact artifact content being
// approved; Plan is stamped on the approval, and the ID is derived.
func NewApproval(planID PublicationID, approvedBy string, method ApprovalMethod, digest Digest, targets []TargetID, note string, grantedAt time.Time) *PublicationApproval {
	return &PublicationApproval{
		ID:             NewApprovalID(planID, approvedBy, digest, grantedAt),
		PlanID:         planID,
		ApprovedBy:     run.RedactSecrets(approvedBy),
		Method:         method,
		ArtifactDigest: digest,
		Targets:        append([]TargetID(nil), targets...),
		Note:           run.RedactSecrets(note),
		Status:         ApprovalStatusGranted,
		GrantedAt:      run.NewTimestamp(grantedAt),
	}
}

// MatchesDigest reports whether the approval binds to the given artifact
// digest.
func (a *PublicationApproval) MatchesDigest(digest string) bool {
	return a.ArtifactDigest.Matches(digest)
}

// CoversTarget reports whether the approval's scope includes target.
// An empty scope covers every target.
func (a *PublicationApproval) CoversTarget(target TargetID) bool {
	if len(a.Targets) == 0 {
		return true
	}
	return slices.Contains(a.Targets, target)
}

// EffectiveFor reports whether the approval is currently in force for the
// given artifact digest and target: granted, digest-bound, and in scope.
func (a *PublicationApproval) EffectiveFor(digest string, target TargetID) bool {
	return a.Status == ApprovalStatusGranted && a.MatchesDigest(digest) && a.CoversTarget(target)
}

// Invalidate marks the approval stale or revoked, recording when and why.
// It is the single bookkeeping path behind both "the artifact changed"
// (stale) and "the human withdrew consent" (revoked).
//
// Only granted approvals can be closed: re-invalidating an already-closed
// approval is a no-op, so the original closure (status, time, reason) is
// never overwritten.
func (a *PublicationApproval) Invalidate(status ApprovalStatus, reason string, at time.Time) {
	if status != ApprovalStatusStale && status != ApprovalStatusRevoked {
		return
	}
	if a.Status != ApprovalStatusGranted {
		return
	}
	a.Status = status
	ts := run.NewTimestamp(at)
	a.InvalidatedAt = &ts
	a.InvalidationReason = run.RedactSecrets(reason)
}

// Validate checks the approval: identifiers, principal, method, digest,
// scope, note, and the invalidated-state invariants (a closed approval
// records when and why it closed).
func (a *PublicationApproval) Validate() error {
	if a == nil {
		return errors.New("publication approval is nil")
	}
	var errs []error
	addf := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if err := a.ID.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := a.PlanID.Validate(); err != nil {
		errs = append(errs, err)
	}
	if a.ApprovedBy == "" {
		errs = append(errs, errors.New("approved_by is required"))
	} else {
		if len(a.ApprovedBy) > maxApprovedByLen {
			addf("approved_by exceeds %d characters", maxApprovedByLen)
		}
		if !approvedByRe.MatchString(a.ApprovedBy) {
			addf("approved_by %q is invalid: use letters, digits, '.', '_', '@', '-' (1-128 chars)", a.ApprovedBy)
		}
		if run.LooksLikeSecret(a.ApprovedBy) {
			addf("approved_by contains secret-looking material; redact it before recording")
		}
	}
	if !a.Method.Valid() {
		addf("unknown approval method %q", a.Method)
	}
	if !a.ArtifactDigest.Valid() {
		errs = append(errs, fmt.Errorf("artifact digest %q is not a valid %s digest", a.ArtifactDigest, DigestAlgorithm))
	}

	seen := make(map[TargetID]bool, len(a.Targets))
	for _, target := range a.Targets {
		if err := target.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("approval scope: %w", err))
		}
		if seen[target] {
			addf("approval scope lists target %q twice", target)
		}
		seen[target] = true
	}

	if len(a.Note) > maxApprovalNote {
		addf("approval note exceeds %d characters", maxApprovalNote)
	}
	if run.LooksLikeSecret(a.Note) {
		addf("approval note contains secret-looking material; redact it before recording")
	}
	if !a.Status.Valid() {
		addf("unknown approval status %q", a.Status)
	}
	if a.GrantedAt.IsZero() {
		errs = append(errs, errors.New("granted_at is required"))
	}
	switch a.Status {
	case ApprovalStatusGranted:
		if a.InvalidatedAt != nil && !a.InvalidatedAt.IsZero() {
			addf("granted approval must not have an invalidated_at")
		}
	case ApprovalStatusStale, ApprovalStatusRevoked:
		if a.InvalidatedAt == nil || a.InvalidatedAt.IsZero() {
			addf("%s approval requires an invalidated_at", a.Status)
		}
		if a.InvalidationReason == "" {
			addf("%s approval requires an invalidation reason", a.Status)
		} else if run.LooksLikeSecret(a.InvalidationReason) {
			addf("invalidation reason contains secret-looking material; redact it before recording")
		}
	}
	return errors.Join(errs...)
}
