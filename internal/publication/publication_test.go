package publication

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// simpleTargets is the deterministic two-target fixture behind the plan
// lifecycle tests.
func simpleTargets() []PublicationTarget {
	main := NewTarget("main", "Main channel", TargetTypePost, "bluesky",
		"at://com.example/main", nil)
	secondary := NewTarget("secondary", "Secondary channel", TargetTypePage, "hugo",
		"sites/news/dolphins-preview", []TargetID{"main"})
	return []PublicationTarget{main, secondary}
}

// planA is the canonical test plan: the briefA artifact, a passing gate,
// and the two fixture targets.
func planA(t *testing.T) *Plan {
	t.Helper()
	p, err := NewPlan(NewPlanParams{
		RunID:     testRunID,
		Workspace: "feed",
		Gate:      passingGate(briefA),
		Artifact:  artifact(briefA),
		Targets:   simpleTargets(),
		CreatedAt: testTime,
	})
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	return p
}

// approvedPlan returns planA with a granted, digest-bound approval covering
// both targets.
func approvedPlan(t *testing.T, at time.Time) *Plan {
	t.Helper()
	p := planA(t)
	a := NewApproval(p.ID, "editor", ApprovalMethodManual, p.Artifact.Digest,
		[]TargetID{"main", "secondary"}, "looks good", testTime)
	if err := p.Approve(a, at); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	return p
}

func TestNewPlan(t *testing.T) {
	plan := planA(t)

	if err := plan.Validate(); err != nil {
		t.Fatalf("a freshly created plan must validate: %v", err)
	}
	if plan.Status != StatusPlanned {
		t.Errorf("new plan status = %q, want planned", plan.Status)
	}
	if !strings.HasPrefix(string(plan.ID), "pub-") {
		t.Errorf("plan ID %q must start with pub-", plan.ID)
	}
	if plan.RunID != testRunID {
		t.Errorf("run id = %q, want %q", plan.RunID, testRunID)
	}
	if !plan.Gate.Passing() {
		t.Error("the plan's gate evidence must be passing")
	}
	if !plan.Gate.ArtifactDigest.Matches(string(plan.Artifact.Digest)) {
		t.Error("gate evidence must bind the artifact digest")
	}
	if len(plan.Targets) != 2 {
		t.Fatalf("targets = %d, want 2", len(plan.Targets))
	}
	for _, tgt := range plan.Targets {
		if tgt.Status != StatusPlanned {
			t.Errorf("target %q status = %q, want planned (new plans start clean)", tgt.ID, tgt.Status)
		}
	}
	if plan.FinishedAt != nil {
		t.Error("a planned plan must not have a finished_at")
	}
	if plan.CreatedAt.IsZero() || plan.UpdatedAt.IsZero() {
		t.Error("created_at and updated_at must be set")
	}
	// A zero CreatedAt defaults to the current time.
	zp, err := NewPlan(NewPlanParams{
		RunID: testRunID, Gate: passingGate(briefA), Artifact: artifact(briefA),
		Targets: simpleTargets(), CreatedAt: time.Time{},
	})
	if err != nil {
		t.Fatalf("NewPlan with a zero CreatedAt: %v", err)
	}
	if zp.CreatedAt.IsZero() {
		t.Error("a zero CreatedAt must default to the current time")
	}
	if plan.UpdatedAt.Before(plan.CreatedAt) {
		t.Error("updated_at must not precede created_at")
	}
}

// caller-side non-initial target states are discarded by NewPlan.
func TestNewPlanDiscardsTargetStates(t *testing.T) {
	dirty := mainTarget()
	dirty.Status = StatusSucceeded
	dirty.Receipt = baseReceipt()
	p, err := NewPlan(NewPlanParams{
		RunID: testRunID, Gate: passingGate(briefA), Artifact: artifact(briefA),
		Targets:   []PublicationTarget{dirty},
		CreatedAt: testTime,
	})
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	if p.Targets[0].Status != StatusPlanned || p.Targets[0].Receipt != nil {
		t.Errorf("a new plan must normalize targets to the initial state: %v", p.Targets[0])
	}
}

func TestNewPlanRejections(t *testing.T) {
	base := func() NewPlanParams {
		return NewPlanParams{
			RunID: testRunID, Gate: passingGate(briefA), Artifact: artifact(briefA),
			Targets:   []PublicationTarget{mainTarget()},
			CreatedAt: testTime,
		}
	}
	cases := map[string]struct {
		mutate func(p *NewPlanParams)
		substr string
	}{
		"no run id":            {func(p *NewPlanParams) { p.RunID = "" }, "run ID is required"},
		"bad run id":           {func(p *NewPlanParams) { p.RunID = run.RunID("nope") }, "invalid run reference"},
		"no artifact":          {func(p *NewPlanParams) { p.Artifact = ArtifactIdentity{} }, "invalid artifact"},
		"bad artifact":         {func(p *NewPlanParams) { p.Artifact = artifact(briefA); p.Artifact.Digest = Digest("zz") }, "invalid artifact"},
		"gate digest mismatch": {func(p *NewPlanParams) { p.Gate.ArtifactDigest = Digest("sha256:" + strings.Repeat("0", 64)) }, "does not match"},
		"gate not passing (final check failed)": {
			func(p *NewPlanParams) { p.Gate.FinalCheck = contracts.FactualCheckStatusFail },
			"gate",
		},
		"gate not passing (review not ready)": {func(p *NewPlanParams) { p.Gate.ReviewReady = false }, "gate"},
		"gate not passing (run failed)":       {func(p *NewPlanParams) { p.Gate.RunStatus = run.RunStatusFailed }, "gate"},
		"no targets":                          {func(p *NewPlanParams) { p.Targets = nil }, "at least one"},
		"duplicate targets": {
			func(p *NewPlanParams) { p.Targets = []PublicationTarget{mainTarget(), mainTarget()} },
			"duplicate",
		},
		"cycle": {
			func(p *NewPlanParams) {
				a := NewTarget("a", "", TargetTypePost, "bluesky", "d", []TargetID{"b"})
				b := NewTarget("b", "", TargetTypePost, "bluesky", "d", []TargetID{"a"})
				p.Targets = []PublicationTarget{a, b}
			},
			"cycle",
		},
		"too many targets": {
			func(p *NewPlanParams) {
				targets := make([]PublicationTarget, MaxTargetsPerPlan+1)
				for i := range targets {
					targets[i] = NewTarget(fmt.Sprintf("t%d", i), "", TargetTypePost, "bluesky", "d", nil)
				}
				p.Targets = targets
			},
			"exceeding the bound",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := base()
			c.mutate(&p)
			_, err := NewPlan(p)
			if err == nil || !strings.Contains(err.Error(), c.substr) {
				t.Errorf("%q: got %v, want error mentioning %q", name, err, c.substr)
			}
		})
	}
}

func TestNewPublicationID(t *testing.T) {
	idA := NewPublicationID(testRunID, artifact(briefA), []TargetID{"a", "b"}, testTime)
	idB := NewPublicationID(testRunID, artifact(briefA), []TargetID{"a", "b"}, testTime)
	if idA != idB {
		t.Error("identical inputs must derive the identical plan ID (stable re-creation)")
	}
	if !strings.HasPrefix(string(idA), "pub-") {
		t.Errorf("plan ID %q must start with pub-", idA)
	}
	idC := NewPublicationID(testRunID, artifact(briefA), []TargetID{"a"}, testTime)
	if idC == idA {
		t.Error("different target sets must derive different plan IDs")
	}
	other := NewArtifactIdentity("brief", run.ArtifactKindRendered, "out/b.html", []byte(briefB), "v1")
	idD := NewPublicationID(testRunID, other, []TargetID{"a", "b"}, testTime)
	if idD == idA {
		t.Error("different artifact digests must derive different plan IDs")
	}
	if err := idA.Validate(); err != nil {
		t.Errorf("derived plan ID must validate: %v", err)
	}
	if err := PublicationID("").Validate(); err == nil {
		t.Error("an empty plan ID must not validate")
	}
}

func TestApprove(t *testing.T) {
	p := planA(t)
	digest := string(p.Artifact.Digest)

	// happy path
	a := NewApproval(p.ID, "editor", ApprovalMethodManual, Digest(digest),
		[]TargetID{"main", "secondary"}, "looks good", testTime)
	if err := p.Approve(a, tAt(10)); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if p.Status != StatusApproved {
		t.Errorf("plan status = %q, want approved", p.Status)
	}
	if len(p.Approvals) != 1 || !p.EffectiveApproval().EffectiveFor(digest, "main") {
		t.Errorf("the approval must be recorded and effective: %+v", p.Approvals)
	}
	if !a.GrantedAt.Equal(run.NewTimestamp(tAt(10))) {
		t.Error("the approval must be normalized to the approval time")
	}

	// an approval bound to different bytes cannot approve the plan.
	stale := NewApproval(p.ID, "editor", ApprovalMethodManual, Digest(digestOf(briefB)),
		[]TargetID{"main"}, "", testTime)
	if err := p.Approve(stale, tAt(11)); err == nil || !strings.Contains(err.Error(), "binds digest") {
		t.Errorf("stale approval must be rejected: %v", err)
	}
	if len(p.Approvals) != 1 {
		t.Error("a rejected approval must not be recorded")
	}

	// an approval from another plan cannot be attached.
	other := NewApproval(otherPlanID(), "editor", ApprovalMethodManual, Digest(digest),
		[]TargetID{"main"}, "", testTime)
	if err := p.Approve(other, tAt(12)); err == nil || !strings.Contains(err.Error(), "belongs to plan") {
		t.Errorf("foreign approval must be rejected: %v", err)
	}

	// a revoked (closed) approval cannot approve.
	revoked := NewApproval(p.ID, "editor", ApprovalMethodManual, Digest(digest),
		[]TargetID{"main"}, "", testTime)
	revoked.Invalidate(ApprovalStatusRevoked, "changed my mind", testTime)
	if err := p.Approve(revoked, tAt(13)); err == nil || !strings.Contains(err.Error(), "must be granted") {
		t.Errorf("a closed approval must not approve: %v", err)
	}

	// a scope naming an unknown target is rejected.
	unknown := NewApproval(p.ID, "editor2", ApprovalMethodManual, Digest(digest),
		[]TargetID{"nope"}, "", testTime)
	if err := p.Approve(unknown, tAt(14)); err == nil || !strings.Contains(err.Error(), "unknown target") {
		t.Errorf("unknown target scope must be rejected: %v", err)
	}

	// duplicate approval IDs are rejected.
	dup := NewApproval(p.ID, "editor", ApprovalMethodManual, Digest(digest),
		[]TargetID{"main"}, "re-approve", tAt(15))
	dup.ID = p.Approvals[0].ID
	if err := p.Approve(dup, tAt(16)); err == nil || !strings.Contains(err.Error(), "duplicate approval") {
		t.Errorf("duplicate approval id must be rejected: %v", err)
	}
}

func otherPlanID() PublicationID {
	return NewPublicationID(testRunID, artifact(briefA), []TargetID{"zz"}, testTime)
}

func TestRevokeApproval(t *testing.T) {
	p := approvedPlan(t, tAt(10))
	id := p.Approvals[0].ID

	// missing reason.
	if err := p.RevokeApproval(id, tAt(11), ""); err == nil {
		t.Error("a revocation without a reason must fail")
	}
	if err := p.RevokeApproval(id, tAt(11), "too risky"); err != nil {
		t.Fatalf("RevokeApproval: %v", err)
	}
	if p.Approvals[0].Status != ApprovalStatusRevoked {
		t.Errorf("approval status = %q, want revoked", p.Approvals[0].Status)
	}
	if p.EffectiveApproval() != nil {
		t.Error("no effective approval may remain after the only one is revoked")
	}
	if p.Status != StatusPlanned {
		t.Errorf("an approved plan without an effective approval must fall back to planned, got %q", p.Status)
	}

	// revoking a closed approval is a rejection, not a double-close.
	if err := p.RevokeApproval(id, tAt(12), "again"); err == nil || !strings.Contains(err.Error(), "only granted") {
		t.Errorf("re-revocation must be rejected: %v", err)
	}
	// unknown approval id.
	if err := p.RevokeApproval(ApprovalID("appr-unknown"), tAt(13), "x"); err == nil {
		t.Error("revoking an unknown approval must fail")
	}
}

func TestApproveRequiresMatchingGate(t *testing.T) {
	p := planA(t)
	// The artifact rebaselined without a gate re-gather: the recorded gate
	// binds the old bytes and must block approval.
	other := artifact(briefB) // same reference, new bytes
	p.Artifact = other
	a := NewApproval(p.ID, "editor", ApprovalMethodManual, Digest(digestOf(briefB)),
		[]TargetID{"main", "secondary"}, "", testTime)
	if err := p.Approve(a, tAt(10)); err == nil || !strings.Contains(err.Error(), "re-gather") {
		t.Errorf("approval against stale gate evidence must be rejected: %v", err)
	}
	// Re-gather the gate for the current bytes, then approval succeeds.
	gate := passingGate(briefB)
	if err := p.SetGate(gate, tAt(9)); err != nil {
		t.Fatalf("SetGate: %v", err)
	}
	if err := p.Approve(a, tAt(10)); err != nil {
		t.Fatalf("approval after gate re-gather: %v", err)
	}
}

func TestSetGate(t *testing.T) {
	p := planA(t)
	failing := passingGate(briefA)
	failing.FinalCheck = contracts.FactualCheckStatusFail
	if err := p.SetGate(failing, tAt(10)); err == nil {
		t.Error("a non-passing gate must not be accepted")
	}
	// a digest mismatch is rejected
	other := passingGate(briefA)
	other.ArtifactDigest = Digest(digestOf(briefB))
	if err := p.SetGate(other, tAt(10)); err == nil {
		t.Error("a gate binding different bytes must not be accepted")
	}
	// a matching passing gate is accepted
	if err := p.SetGate(passingGate(briefA), tAt(10)); err != nil {
		t.Errorf("a passing, matching gate must be accepted: %v", err)
	}
}

func TestRebaselineArtifact(t *testing.T) {
	p := approvedPlan(t, tAt(10))
	approvalID := p.Approvals[0].ID

	// same content: no-op for approvals.
	same := artifact(briefA)
	if err := p.RebaselineArtifact(same, tAt(11)); err != nil {
		t.Fatalf("rebaseline to identical bytes: %v", err)
	}
	if p.Approvals[0].Status != ApprovalStatusGranted {
		t.Error("re-baselining to identical bytes must not invalidate the approval")
	}

	// new bytes (same reference): approval goes stale, plan falls back.
	edited := artifact(briefB)
	if err := p.RebaselineArtifact(edited, tAt(12)); err != nil {
		t.Fatalf("RebaselineArtifact: %v", err)
	}
	if !p.Artifact.Digest.Matches(string(digestOf(briefB))) {
		t.Errorf("the plan artifact must move to the new digest: got %q", p.Artifact.Digest)
	}
	if p.Approvals[0].ID != approvalID || p.Approvals[0].Status != ApprovalStatusStale {
		t.Errorf("the stale approval must be invalidated in place, got id=%q status=%q", p.Approvals[0].ID, p.Approvals[0].Status)
	}
	if p.EffectiveApproval() != nil {
		t.Error("no approval may be effective for the new bytes")
	}
	if p.Status != StatusPlanned {
		t.Errorf("an approved plan whose approval went stale must fall back to planned, got %q", p.Status)
	}
	if len(p.Warnings) == 0 || !strings.Contains(p.Warnings[len(p.Warnings)-1].Message, "content changed") {
		t.Errorf("a rebaseline to new bytes must be recorded as a warning: %+v", p.Warnings)
	}

	// the artifact reference must not change.
	moved := artifact(briefB)
	moved.Path = "out/elsewhere.html"
	if err := p.RebaselineArtifact(moved, tAt(13)); err == nil || !strings.Contains(err.Error(), "reference") {
		t.Errorf("a reference change must be rejected: %v", err)
	}

	// terminal plans are immutable.
	terminal := approvedPlan(t, tAt(1))
	_ = terminal.StartPublishing(tAt(2))
	_ = terminal.StartTarget("main", tAt(3))
	_ = terminal.StartTarget("secondary", tAt(3))
	_ = terminal.FailTarget("main", "net", "down", tAt(4))
	_ = terminal.SucceedTarget("secondary", receiptFor(terminal, "main", tAt(4)), tAt(5))
	if err := terminal.RefreshStatus(tAt(6)); err != nil {
		t.Fatalf("RefreshStatus: %v", err)
	}
	if terminal.Status != StatusPartiallySucceeded {
		t.Fatalf("expected a partially succeeded plan, got %q", terminal.Status)
	}
	if err := terminal.RebaselineArtifact(edited, tAt(7)); err == nil {
		t.Error("a terminal plan must not be rebaselined")
	}
}

// TestReapprovalAfterRebaseline is the canonical "content changed after
// approval" round trip: rebaseline -> stale -> re-gate -> re-approve ->
// publish the new bytes.
func TestReapprovalAfterRebaseline(t *testing.T) {
	p := approvedPlan(t, tAt(10))
	edited := artifact(briefB)
	if err := p.RebaselineArtifact(edited, tAt(11)); err != nil {
		t.Fatalf("rebaseline: %v", err)
	}
	// Stale: nothing may be published.
	if err := p.AuthorizesPublish("main", string(digestOf(briefB))); err == nil {
		t.Fatal("a plan with no effective approval must not authorize publication")
	}
	// The old approval cannot approve the new bytes; a fresh one must.
	fresh := NewApproval(p.ID, "editor", ApprovalMethodManual, Digest(digestOf(briefB)),
		[]TargetID{"main", "secondary"}, "approved the edit", testTime)
	gate := passingGate(briefB)
	if err := p.SetGate(gate, tAt(12)); err != nil {
		t.Fatalf("SetGate: %v", err)
	}
	if err := p.Approve(fresh, tAt(13)); err != nil {
		t.Fatalf("re-approval of the new content: %v", err)
	}
	if p.Status != StatusApproved {
		t.Errorf("re-approved plan must be approved, got %q", p.Status)
	}
	// The old approval is still in the history (stale), the new one governs.
	if got := p.EffectiveApproval(); got.ID != fresh.ID {
		t.Errorf("the most recent effective approval must govern, got %q", got.ID)
	}
	if err := p.AuthorizesPublish("main", string(digestOf(briefB))); err != nil {
		t.Errorf("after re-approval, the new bytes must publish: %v", err)
	}
}

func TestStartPublishingAndRetry(t *testing.T) {
	p := planA(t)
	if err := p.StartPublishing(tAt(10)); err == nil {
		t.Error("a planned plan cannot start a cycle")
	}
	_ = p.Approve(newApprovalFor(p, "editor", "main", "secondary"), tAt(10))
	if err := p.StartPublishing(tAt(11)); err != nil {
		t.Fatalf("StartPublishing: %v", err)
	}
	if p.Status != StatusPublishing {
		t.Errorf("status = %q, want publishing", p.Status)
	}
	if p.FinishedAt != nil {
		t.Error("a publishing plan must not have a finished_at")
	}
	// retrying from approved/publishing is invalid
	if err := p.Retry(tAt(12)); err == nil {
		t.Error("Retry requires a failed or partially succeeded plan")
	}
}

func TestFullCycleSuccess(t *testing.T) {
	p := approvedPlan(t, tAt(10))
	if err := p.StartPublishing(tAt(11)); err != nil {
		t.Fatalf("StartPublishing: %v", err)
	}
	// a target cannot start outside a cycle
	p2 := approvedPlan(t, tAt(10))
	if err := p2.StartTarget("main", tAt(11)); err == nil {
		t.Error("StartTarget requires a publishing cycle")
	}

	if err := p.StartTarget("main", tAt(12)); err != nil {
		t.Fatalf("StartTarget: %v", err)
	}
	if err := p.AuthorizesPublish("main", string(p.Artifact.Digest)); err == nil {
		t.Error("a target with an in-flight attempt must not be authorized again")
	}
	if err := p.SucceedTarget("main", receiptFor(p, "main", tAt(13)), tAt(14)); err != nil {
		t.Fatalf("SucceedTarget: %v", err)
	}
	tgt, _ := p.Target("main")
	if tgt.Status != StatusSucceeded || tgt.Receipt == nil {
		t.Errorf("target must be succeeded with a receipt: %q, receipt %v", tgt.Status, tgt.Receipt)
	}
	if len(tgt.Attempts) != 1 || tgt.Attempts[0].Status != AttemptStatusSucceeded {
		t.Errorf("the attempt must be recorded as succeeded: %+v", tgt.Attempts)
	}

	if err := p.StartTarget("secondary", tAt(15)); err != nil {
		t.Fatalf("StartTarget secondary: %v", err)
	}
	if err := p.SucceedTarget("secondary", receiptFor(p, "secondary", tAt(16)), tAt(17)); err != nil {
		t.Fatalf("SucceedTarget secondary: %v", err)
	}
	if err := p.RefreshStatus(tAt(18)); err != nil {
		t.Fatalf("RefreshStatus: %v", err)
	}
	if p.Status != StatusSucceeded {
		t.Errorf("all targets succeeded: plan must be succeeded, got %q", p.Status)
	}
	if p.FinishedAt == nil || !p.FinishedAt.Equal(run.NewTimestamp(tAt(18))) {
		t.Error("a terminal plan must have a finished_at")
	}
	if p.TerminalReason == "" {
		t.Error("a terminal plan must record why it ended")
	}

	// terminal plans are immutable: no more mutations.
	if err := p.StartPublishing(tAt(19)); err == nil {
		t.Error("a terminal plan cannot start a cycle")
	}
	if err := p.Cancel(tAt(20), "why"); err == nil {
		t.Error("a terminal plan cannot be cancelled")
	}
	if err := p.AddError("x", "y", tAt(20)); err == nil {
		t.Error("a terminal plan cannot record new errors")
	}
}

// A receipt for different bytes must be rejected: the publisher must send
// exactly what was approved.
func TestSucceedTargetDigestBinding(t *testing.T) {
	p := approvedPlan(t, tAt(10))
	_ = p.StartPublishing(tAt(11))
	_ = p.StartTarget("main", tAt(12))
	wrong := receiptFor(p, "main", tAt(13))
	wrong.ContentDigest = digestOf(briefB)
	err := p.SucceedTarget("main", wrong, tAt(14))
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Errorf("a receipt for different bytes must be rejected: %v", err)
	}
	if tgt, _ := p.Target("main"); tgt.Status != StatusPublishing {
		t.Errorf("a rejected receipt must not change the target, got %q", tgt.Status)
	}
}

// Failure and partial success, retry, and the idempotency rule.
func TestCycleFailurePartialRetry(t *testing.T) {
	p := approvedPlan(t, tAt(10))
	_ = p.StartPublishing(tAt(11))
	_ = p.StartTarget("main", tAt(12))
	_ = p.StartTarget("secondary", tAt(12))
	if err := p.FailTarget("main", "net", "unreachable", tAt(13)); err != nil {
		t.Fatalf("FailTarget: %v", err)
	}
	if err := p.PartialTarget("secondary", receiptFor(p, "secondary", tAt(13)), tAt(14)); err != nil {
		t.Fatalf("PartialTarget: %v", err)
	}
	if err := p.RefreshStatus(tAt(15)); err != nil {
		t.Fatalf("RefreshStatus: %v", err)
	}
	// No target fully succeeded, so the plan fails (partial external progress
	// is recorded on the targets and their receipts, not as a plan success).
	if p.Status != StatusFailed {
		t.Errorf("no target published: plan = %q, want failed", p.Status)
	}

	// A mixed outcome: one full success plus one failure is a partial
	// plan success.
	p2 := approvedPlan(t, tAt(10))
	_ = p2.StartPublishing(tAt(11))
	_ = p2.StartTarget("main", tAt(12))
	_ = p2.StartTarget("secondary", tAt(12))
	_ = p2.FailTarget("main", "net", "down", tAt(13))
	_ = p2.SucceedTarget("secondary", receiptFor(p2, "secondary", tAt(13)), tAt(14))
	if err := p2.RefreshStatus(tAt(15)); err != nil {
		t.Fatalf("RefreshStatus: %v", err)
	}
	if p2.Status != StatusPartiallySucceeded {
		t.Errorf("one published and one failed: plan = %q, want partially_succeeded", p2.Status)
	}

	// Retry resumes the failed plan; the failed target starts a new attempt,
	// and the history keeps both.
	if err := p.Retry(tAt(16)); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if p.Status != StatusPublishing {
		t.Errorf("a retried plan must be publishing, got %q", p.Status)
	}
	if err := p.StartTarget("main", tAt(17)); err != nil {
		t.Errorf("a failed target must be retryable: %v", err)
	}
	main, _ := p.Target("main")
	if len(main.Attempts) != 2 || main.Attempts[1].Number != 2 {
		t.Errorf("retry attempts must be numbered consecutively: %+v", main.Attempts)
	}
	// the partially succeeded target is also retryable
	if err := p.StartTarget("secondary", tAt(18)); err != nil {
		t.Errorf("a partially succeeded target must be retryable: %v", err)
	}
	secondary, _ := p.Target("secondary")
	if len(secondary.Attempts) != 2 {
		t.Errorf("the retried target must carry its attempt history, got %d attempts", len(secondary.Attempts))
	}
	// and nothing may be re-run from a fully succeeded target: a succeeded
	// target is never re-executed.
	if err := p2.Retry(tAt(16)); err != nil {
		t.Fatalf("Retry on a partially succeeded plan: %v", err)
	}
	if err := p2.StartTarget("secondary", tAt(17)); err == nil {
		t.Error("a succeeded target must never be re-executed")
	}
}

func TestSkipTargetAggregation(t *testing.T) {
	// everything skipped -> the plan skips.
	p := approvedPlan(t, tAt(10))
	_ = p.StartPublishing(tAt(11))
	if err := p.SkipTarget("main", "provider suspended", tAt(12)); err != nil {
		t.Fatalf("SkipTarget: %v", err)
	}
	if err := p.SkipTarget("secondary", "provider suspended", tAt(12)); err != nil {
		t.Fatalf("SkipTarget secondary: %v", err)
	}
	if err := p.RefreshStatus(tAt(13)); err != nil {
		t.Fatalf("RefreshStatus: %v", err)
	}
	if p.Status != StatusSkipped {
		t.Errorf("all targets skipped: plan = %q, want skipped", p.Status)
	}

	// one skipped, one published -> the plan succeeds (a deliberate skip is
	// not a failure).
	p2 := approvedPlan(t, tAt(10))
	_ = p2.StartPublishing(tAt(11))
	if err := p2.SkipTarget("secondary", "optional channel down", tAt(12)); err != nil {
		t.Fatalf("SkipTarget: %v", err)
	}
	if err := p2.StartTarget("main", tAt(13)); err != nil {
		t.Fatalf("StartTarget: %v", err)
	}
	if err := p2.SucceedTarget("main", receiptFor(p2, "main", tAt(14)), tAt(15)); err != nil {
		t.Fatalf("SucceedTarget: %v", err)
	}
	if err := p2.RefreshStatus(tAt(16)); err != nil {
		t.Fatalf("RefreshStatus: %v", err)
	}
	if p2.Status != StatusSucceeded {
		t.Errorf("a published target with a deliberately skipped one: plan = %q, want succeeded", p2.Status)
	}

	// a skipped target cannot be started or authorized.
	if err := p2.StartTarget("secondary", tAt(17)); err == nil {
		t.Error("a skipped target must not be startable")
	}
	if err := p2.AuthorizesPublish("secondary", string(p2.Artifact.Digest)); err == nil {
		t.Error("a skipped target must not be authorized")
	}

	// skipping without a reason fails; an in-flight target cannot be skipped.
	p3 := approvedPlan(t, tAt(10))
	_ = p3.StartPublishing(tAt(11))
	if err := p3.SkipTarget("main", "", tAt(12)); err == nil {
		t.Error("a skip without a reason must fail")
	}
	_ = p3.StartTarget("main", tAt(13))
	if err := p3.SkipTarget("main", "x", tAt(14)); err == nil {
		t.Error("an in-flight target must not be skippable (it is aborted, not skipped)")
	}
}

func TestAbortTargetAndCancel(t *testing.T) {
	p := approvedPlan(t, tAt(10))
	_ = p.StartPublishing(tAt(11))
	_ = p.StartTarget("main", tAt(12))
	// a target that is not in flight cannot be aborted.
	if err := p.AbortTarget("secondary", tAt(13)); err == nil {
		t.Error("aborting a non-publishing target must fail")
	}
	if err := p.AbortTarget("main", tAt(14)); err != nil {
		t.Fatalf("AbortTarget: %v", err)
	}
	main, _ := p.Target("main")
	if main.Status != StatusCancelled {
		t.Errorf("an aborted target must be cancelled, got %q", main.Status)
	}
	if len(main.Attempts) != 1 || main.Attempts[0].Status != AttemptStatusCancelled {
		t.Errorf("the aborted attempt must be cancelled: %+v", main.Attempts)
	}

	// cancellation records the reason and aborts in-flight targets.
	_ = p.StartTarget("secondary", tAt(15))
	if err := p.Cancel(tAt(16), "editor withdrew the brief"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if p.Status != StatusCancelled {
		t.Errorf("status = %q, want cancelled", p.Status)
	}
	if p.TerminalReason != "editor withdrew the brief" {
		t.Errorf("terminal reason = %q", p.TerminalReason)
	}
	secondary, _ := p.Target("secondary")
	if secondary.Status != StatusCancelled {
		t.Errorf("an in-flight target must be aborted on cancel, got %q", secondary.Status)
	}
	if p.FinishedAt == nil {
		t.Error("a cancelled plan must have a finished_at")
	}

	// a secret in the reason is redacted, not persisted as-is.
	p2 := approvedPlan(t, tAt(10))
	if err := p2.Cancel(tAt(11), "secret: "+testToken); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if !strings.Contains(p2.TerminalReason, run.RedactedPlaceholder) {
		t.Errorf("a secret cancellation reason must be redacted: %q", p2.TerminalReason)
	}
	// and a token-looking reason that survives redaction is rejected.
	p3 := approvedPlan(t, tAt(10))
	if err := p3.Cancel(tAt(11), "x"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
}

func TestCancelEmptyReason(t *testing.T) {
	p := planA(t)
	if err := p.Cancel(tAt(10), ""); err == nil {
		t.Error("cancellation requires a reason")
	}
}

// TestAuthorizesPublish is the core authorization matrix.
func TestAuthorizesPublish(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(p *Plan)
		target TargetID
		digest string
		wantOK bool
		substr string
	}{
		{"happy path", nil, "main", "", true, ""},
		{
			"nil plan", nil, "main", "", false, "nil",
		},
		{
			"planned plan is not approved",
			func(p *Plan) {
				p.Status = StatusPlanned
				p.Approvals = nil
			},
			"main", "", false, "requires an approved plan",
		},
		{
			"stale content", nil, "main", string(digestOf(briefB)), false, "stale",
		},
		{
			"gate not passing",
			func(p *Plan) { p.Gate.FinalCheck = contracts.FactualCheckStatusFail },
			"main", "", false, "gate evidence is not passing",
		},
		{
			"gate digest mismatch",
			func(p *Plan) { p.Gate.ArtifactDigest = Digest(digestOf(briefB)) },
			"main", "", false, "binds digest",
		},
		{
			"no approval",
			func(p *Plan) { p.Approvals = nil },
			"main", "", false, "no effective approval",
		},
		{
			"approval outside the target scope",
			func(p *Plan) {
				p.Approvals = p.Approvals[:1]
				p.Approvals[0].Targets = []TargetID{"other"}
			},
			"main", "", false, "no effective approval",
		},
		{
			"already published",
			func(p *Plan) {
				t := &p.Targets[0]
				t.Status = StatusSucceeded
				t.Receipt = baseReceipt()
			},
			"main", "", false, "already published",
		},
		{
			"retry after failure is allowed",
			func(p *Plan) {
				t := &p.Targets[0]
				t.Status = StatusFailed
			},
			"main", "", true, "",
		},
		{
			"retry after partial success is allowed",
			func(p *Plan) {
				t := &p.Targets[0]
				t.Status = StatusPartiallySucceeded
			},
			"main", "", true, "",
		},
		{
			"skipped target is out of scope",
			func(p *Plan) {
				t := &p.Targets[0]
				t.Status = StatusSkipped
			},
			"main", "", false, "skipped",
		},
		{
			"cancelled target is out of scope",
			func(p *Plan) {
				t := &p.Targets[0]
				t.Status = StatusCancelled
			},
			"main", "", false, "cancelled",
		},
		{
			"in-flight target is busy",
			func(p *Plan) {
				t := &p.Targets[0]
				t.Status = StatusPublishing
			},
			"main", "", false, "in flight",
		},
		{
			"unknown target", nil, "nope", "", false, "no target",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.name == "nil plan" {
				var p *Plan
				if err := p.AuthorizesPublish(c.target, c.digest); err == nil || !strings.Contains(err.Error(), c.substr) {
					t.Fatalf("got %v, want error mentioning %q", err, c.substr)
				}
				return
			}
			p := approvedPlan(t, tAt(10))
			if c.mutate != nil {
				c.mutate(p)
			}
			digest := c.digest
			if digest == "" {
				digest = string(p.Artifact.Digest)
			}
			err := p.AuthorizesPublish(c.target, digest)
			if c.wantOK {
				if err != nil {
					t.Fatalf("want authorization, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want rejection, got authorization")
			}
			if !strings.Contains(err.Error(), c.substr) {
				t.Errorf("error %q does not mention %q", err, c.substr)
			}
		})
	}
}

// TestRecordDryRun: rehearsals record without side effects, and only for
// the plan's current artifact.
func TestRecordDryRun(t *testing.T) {
	p := planA(t)
	ok := NewDryRunResult(tAt(5), DryRunOutcomeOK, Digest(digestOf(briefA)), "would post 3 posts", nil)
	if err := p.RecordDryRun("main", ok, tAt(6)); err != nil {
		t.Fatalf("RecordDryRun: %v", err)
	}
	if p.Status != StatusPlanned {
		t.Error("a dry run must not change the plan status")
	}
	tgt, _ := p.Target("main")
	if tgt.DryRun == nil || tgt.DryRun.Outcome != DryRunOutcomeOK {
		t.Errorf("the dry run must be stored on the target: %+v", tgt.DryRun)
	}

	// a rehearsal of different bytes proves nothing.
	wrong := NewDryRunResult(tAt(5), DryRunOutcomeOK, Digest(digestOf(briefB)), "x", nil)
	if err := p.RecordDryRun("main", wrong, tAt(7)); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Errorf("a dry run for different bytes must be rejected: %v", err)
	}
	// a terminal plan cannot record more.
	terminal := approvedPlan(t, tAt(1))
	_ = terminal.StartPublishing(tAt(2))
	_ = terminal.StartTarget("main", tAt(3))
	_ = terminal.SucceedTarget("main", receiptFor(terminal, "main", tAt(4)), tAt(5))
	_ = terminal.SucceedTarget("secondary", receiptFor(terminal, "secondary", tAt(5)), tAt(6))
	_ = terminal.RefreshStatus(tAt(7))
	if err := terminal.RecordDryRun("secondary", ok, tAt(8)); err == nil {
		t.Error("a terminal plan must not record dry runs")
	}
}

func TestRefreshStatusIncompleteCycle(t *testing.T) {
	p := approvedPlan(t, tAt(10))
	_ = p.StartPublishing(tAt(11))
	_ = p.StartTarget("main", tAt(12))
	_ = p.SucceedTarget("main", receiptFor(p, "main", tAt(13)), tAt(14))
	// one target still active (planned): the cycle is not complete.
	if err := p.RefreshStatus(tAt(15)); err != nil {
		t.Fatalf("RefreshStatus on an incomplete cycle: %v", err)
	}
	if p.Status != StatusPublishing {
		t.Errorf("an incomplete cycle must keep the plan publishing, got %q", p.Status)
	}
	if p.FinishedAt != nil {
		t.Error("an incomplete cycle must not set a finished_at")
	}
}

// TestPlanValidate breaks every structural invariant one at a time.
// clonePlan copies the plan and its slice headers so subtest mutations
// cannot leak between cases (shallow copies share the targets slice).
func clonePlan(p *Plan) *Plan {
	c := *p
	c.Targets = append([]PublicationTarget{}, p.Targets...)
	c.Approvals = append([]PublicationApproval{}, p.Approvals...)
	c.Errors = append([]run.ErrorSummary{}, p.Errors...)
	c.Warnings = append([]run.Warning{}, p.Warnings...)
	return &c
}

func TestPlanValidate(t *testing.T) {
	valid := approvedPlan(t, tAt(10))
	if err := valid.Validate(); err != nil {
		t.Fatalf("a working plan must validate: %v", err)
	}

	cases := map[string]struct {
		mutate func(p *Plan)
		substr string
	}{
		"nil":                       {nil, ""},
		"no schema":                 {func(p *Plan) { p.SchemaVersion = "" }, "schema_version"},
		"bad schema format":         {func(p *Plan) { p.SchemaVersion = "garbage" }, "semantic version"},
		"unsupported major":         {func(p *Plan) { p.SchemaVersion = "9.9.9" }, "unsupported"},
		"zero id":                   {func(p *Plan) { p.ID = "" }, "publication id"},
		"zero run id":               {func(p *Plan) { p.RunID = "" }, "run_id"},
		"missing workspace is fine": {func(p *Plan) { p.Workspace = "" }, ""},
		"bad workspace":             {func(p *Plan) { p.Workspace = "Bad Workspace" }, "workspace"},
		"bad status":                {func(p *Plan) { p.Status = PublicationStatus("maybe") }, "invalid plan status"},
		"zero artifact":             {func(p *Plan) { p.Artifact = ArtifactIdentity{} }, "artifact"},
		"zero gate":                 {func(p *Plan) { p.Gate = GateEvidence{} }, "gate"},
		"gate digest mismatch":      {func(p *Plan) { p.Gate.ArtifactDigest = Digest(digestOf(briefB)) }, "does not match"},
		"no targets":                {func(p *Plan) { p.Targets = nil }, "at least one"},
		"duplicate targets":         {func(p *Plan) { p.Targets = append(p.Targets, p.Targets[0]) }, "duplicate"},
		"dependency cycle":          {func(p *Plan) { p.Targets[0].DependsOn = []TargetID{"secondary"} }, "cycle"},
		"transplanted receipt": {func(p *Plan) {
			at := tAt(30)
			a := NewAttempt(1, at)
			a.Status = AttemptStatusSucceeded
			ts := run.NewTimestamp(at)
			a.FinishedAt = &ts
			a.Receipt = NewReceipt(p.ID, "main", "bluesky",
				"https://bsky.app/profile/dolphins.example/post/1", "3luchk2f5zrq4c5n3b2v7xjy6h8d9m0a1k4e6t7u8i9o0p1q2r3s",
				Digest(digestOf(briefB)), "idem-7f3a", "bluesky-publisher/0.6.0", nil, at)
			tgt := &p.Targets[0]
			tgt.Status = StatusSucceeded
			tgt.Receipt = a.Receipt
			tgt.Attempts = []PublicationAttempt{a}
		}, "must prove the approved content"},
		"failed target keeps its matching resume receipt": {func(p *Plan) {
			at := tAt(30)
			a := NewAttempt(1, at)
			a.Status = AttemptStatusFailed
			ts := run.NewTimestamp(at)
			a.FinishedAt = &ts
			e := errSum("net", "unreachable", at)
			a.Error = e
			a.Receipt = receiptFor(p, "main", at)
			tgt := &p.Targets[0]
			tgt.Status = StatusFailed
			tgt.Error = e
			tgt.Receipt = a.Receipt
			tgt.Attempts = []PublicationAttempt{a}
			sec := &p.Targets[1]
			sec.Status = StatusSkipped
			sec.SkipReason = "not needed"
			p.Status = StatusFailed
			p.FinishedAt = &ts
			p.TerminalReason = "one target failed"
		}, ""},
		"approved without approval":     {func(p *Plan) { p.Approvals = nil }, "no effective approval"},
		"stale approval counts as none": {func(p *Plan) { p.Approvals[0].Status = ApprovalStatusStale }, "no effective approval"},
		"secret in terminal reason":     {func(p *Plan) { p.TerminalReason = testToken }, "secret-looking"},
		"long terminal reason":          {func(p *Plan) { p.TerminalReason = strings.Repeat("r", maxNoteLen+1) }, "exceeds"},
		"zero created":                  {func(p *Plan) { p.CreatedAt = run.Timestamp{} }, "created_at"},
		"updated before created": {
			func(p *Plan) { p.UpdatedAt = run.NewTimestamp(testTime.Add(-1 * time.Hour)) },
			"earlier",
		},
		"terminal without finished at": {
			func(p *Plan) { p.Status = StatusSucceeded; p.FinishedAt = nil },
			"finished_at",
		},
		"active with finished at": {
			func(p *Plan) { p.Status = StatusPlanned; ts := run.NewTimestamp(testTime); p.FinishedAt = &ts },
			"finished_at",
		},
		"too many errors": {
			func(p *Plan) {
				p.Errors = make([]run.ErrorSummary, MaxPlanErrors+1)
				for i := range p.Errors {
					p.Errors[i] = run.NewErrorSummary("x", "x", testTime)
				}
			},
			"error log",
		},
		"too many warnings": {
			func(p *Plan) {
				p.Warnings = make([]run.Warning, MaxPlanWarnings+1)
				for i := range p.Warnings {
					p.Warnings[i] = run.NewWarning("x", "x", testTime)
				}
			},
			"warning log",
		},
		"too many approvals": {
			func(p *Plan) {
				p.Approvals = make([]PublicationApproval, MaxApprovalsPerPlan+1)
				for i := range p.Approvals {
					a := NewApproval(p.ID, "editor", ApprovalMethodManual, Digest(digestOf(briefA)),
						[]TargetID{"main"}, "", testTime)
					a.ID = ApprovalID(fmt.Sprintf("appr-%08d-%012d", i, i))
					a.GrantedAt = run.NewTimestamp(testTime)
					p.Approvals[i] = *a
				}
			},
			"exceeding the bound",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if c.mutate == nil {
				if err := (*Plan)(nil).Validate(); err == nil {
					t.Error("a nil plan must not validate")
				}
				return
			}
			p := clonePlan(valid)
			c.mutate(p)
			err := p.Validate()
			if c.substr == "" {
				if err != nil {
					t.Errorf("%q: want a valid plan, got %v", name, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.substr) {
				t.Errorf("%q: got %v, want an error mentioning %q", name, err, c.substr)
			}
		})
	}
}

// TestPlanJSONRoundTrip is the serialization contract: marshal -> unmarshal
// -> still valid, still identical in meaning.
func TestPlanJSONRoundTrip(t *testing.T) {
	p := approvedPlan(t, tAt(10))
	_ = p.StartPublishing(tAt(11))
	_ = p.StartTarget("main", tAt(12))
	_ = p.FailTarget("main", "net", "bluesky 503", tAt(13))

	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	decoded, err := UnmarshalPlan(data)
	if err != nil {
		t.Fatalf("UnmarshalPlan: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("the decoded plan must validate: %v", err)
	}
	if decoded.ID != p.ID || decoded.RunID != p.RunID || decoded.Status != p.Status {
		t.Error("core identity must survive the round trip")
	}
	if decoded.Artifact.Digest != p.Artifact.Digest || decoded.Gate.ArtifactDigest != p.Gate.ArtifactDigest {
		t.Error("digests must survive the round trip byte for byte")
	}
	if decoded.SchemaVersion != SchemaVersion {
		t.Errorf("schema version = %q, want %q", decoded.SchemaVersion, SchemaVersion)
	}
	if !strings.Contains(string(decoded.Artifact.Digest), "sha256:") {
		t.Error("the canonical digest form must survive serialization")
	}

	// an unsupported major version is rejected with a sentinel error.
	decoded.SchemaVersion = "9.9.9"
	data2, err := json.Marshal(&decoded)
	if err != nil {
		t.Fatalf("re-Marshal: %v", err)
	}
	if _, err := UnmarshalPlan(data2); !errors.Is(err, ErrUnsupportedSchemaVersion) {
		t.Errorf("unmarshaling a foreign major version must surface ErrUnsupportedSchemaVersion, got %v", err)
	}
}

// TestPlanLogBounds: the plan logs are bounded in place, oldest first.
func TestPlanLogBounds(t *testing.T) {
	p := planA(t)
	for i := 0; i < MaxPlanErrors+10; i++ {
		if err := p.AddError("test", fmt.Sprintf("error %d", i), tAt(100+i)); err != nil {
			t.Fatalf("AddError: %v", err)
		}
	}
	if len(p.Errors) != MaxPlanErrors {
		t.Errorf("error log = %d entries, want the bound %d", len(p.Errors), MaxPlanErrors)
	}
	// the survivors are the most recent entries (oldest dropped).
	if !strings.Contains(p.Errors[len(p.Errors)-1].Message, fmt.Sprintf("error %d", MaxPlanErrors+9)) {
		t.Error("the newest error must survive trimming")
	}
	if strings.Contains(p.Errors[0].Message, "error 0") {
		t.Error("the oldest error must be dropped")
	}

	p2 := planA(t)
	for i := 0; i < MaxPlanWarnings+5; i++ {
		if err := p2.AddWarning("test", fmt.Sprintf("warning %d", i), tAt(100+i)); err != nil {
			t.Fatalf("AddWarning: %v", err)
		}
	}
	if len(p2.Warnings) != MaxPlanWarnings {
		t.Errorf("warning log = %d entries, want the bound %d", len(p2.Warnings), MaxPlanWarnings)
	}
}

// TestApprovalHistoryTrim: the bounded approval history keeps the most
// recent approvals.
func TestApprovalHistoryTrim(t *testing.T) {
	p := planA(t)
	digest := digestOf(briefA)
	for i := 0; i < MaxApprovalsPerPlan+3; i++ {
		a := NewApproval(p.ID, "editor", ApprovalMethodManual, Digest(digest),
			[]TargetID{"main"}, fmt.Sprintf("approval %d", i), testTime)
		a.ID = ApprovalID(fmt.Sprintf("appr-%08d-%012d", i, i))
		if err := p.Approve(a, tAt(100+i)); err != nil {
			t.Fatalf("Approve #%d: %v", i, err)
		}
	}
	if len(p.Approvals) != MaxApprovalsPerPlan {
		t.Errorf("approval history = %d, want the bound %d", len(p.Approvals), MaxApprovalsPerPlan)
	}
	if !strings.Contains(p.Approvals[len(p.Approvals)-1].Note, "approval") {
		t.Error("the most recent approval must survive trimming")
	}
}

// TestTimestampRegression: no mutator accepts a backwards clock.
func TestTimestampRegression(t *testing.T) {
	fresh := func() *Plan {
		p := approvedPlan(t, tAt(100))
		return p
	}
	cases := map[string]func(p *Plan) error{
		"approve":     func(p *Plan) error { return p.Approve(newApprovalFor(p, "editor", "main", "secondary"), tAt(50)) },
		"revoke":      func(p *Plan) error { return p.RevokeApproval(p.Approvals[0].ID, tAt(50), "x") },
		"rebaseline":  func(p *Plan) error { return p.RebaselineArtifact(artifact(briefA), tAt(50)) },
		"set gate":    func(p *Plan) error { return p.SetGate(passingGate(briefA), tAt(50)) },
		"start cycle": func(p *Plan) error { return p.StartPublishing(tAt(50)) },
		"skip target": func(p *Plan) error { return p.SkipTarget("main", "x", tAt(50)) },
		"add warning": func(p *Plan) error { return p.AddWarning("x", "y", tAt(50)) },
		"add error":   func(p *Plan) error { return p.AddError("x", "y", tAt(50)) },
		"refresh":     func(p *Plan) error { return p.RefreshStatus(tAt(50)) },
		"record dry run": func(p *Plan) error {
			return p.RecordDryRun("main", NewDryRunResult(tAt(50), DryRunOutcomeOK, Digest(digestOf(briefA)), "x", nil), tAt(50))
		},
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			err := fn(fresh())
			if err == nil || !strings.Contains(err.Error(), "regression") {
				t.Errorf("%q: got %v, want a timestamp regression error", name, err)
			}
		})
	}
}

// TestGateEvidenceValidate covers the gate's own invariants.
func TestGateEvidenceValidate(t *testing.T) {
	g := passingGate(briefA)
	if err := g.Validate(); err != nil {
		t.Fatalf("a passing gate must validate: %v", err)
	}
	cases := map[string]struct {
		mutate func(g *GateEvidence)
		substr string
	}{
		"failed run":       {func(g *GateEvidence) { g.RunStatus = run.RunStatusFailed }, "run must have succeeded"},
		"running run":      {func(g *GateEvidence) { g.RunStatus = run.RunStatusRunning }, "run must have succeeded"},
		"failed check":     {func(g *GateEvidence) { g.FinalCheck = contracts.FactualCheckStatusFail }, "must be pass"},
		"review check":     {func(g *GateEvidence) { g.FinalCheck = contracts.FactualCheckStatusReview }, "must be pass"},
		"not ready":        {func(g *GateEvidence) { g.ReviewReady = false }, "must be READY"},
		"bad digest":       {func(g *GateEvidence) { g.ArtifactDigest = Digest("sha256:zz") }, "digest"},
		"zero time":        {func(g *GateEvidence) { g.CheckedAt = run.Timestamp{} }, "checked_at"},
		"secret in note":   {func(g *GateEvidence) { g.Note = testToken }, "secret-looking"},
		"long note":        {func(g *GateEvidence) { g.Note = strings.Repeat("n", maxNoteLen+1) }, "exceeds"},
		"unknown run stat": {func(g *GateEvidence) { g.RunStatus = run.RunStatus("weird") }, "unknown run status"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			g2 := g
			c.mutate(&g2)
			err := g2.Validate()
			if err == nil || !strings.Contains(err.Error(), c.substr) {
				t.Errorf("%q: got %v, want an error mentioning %q", name, err, c.substr)
			}
		})
	}
}

// TestArtifactIdentityValidate covers the artifact's invariants.
func TestArtifactIdentityValidate(t *testing.T) {
	a := artifact(briefA)
	if err := a.Validate(); err != nil {
		t.Fatalf("a working artifact must validate: %v", err)
	}
	cases := map[string]struct {
		mutate func(a *ArtifactIdentity)
		substr string
	}{
		"no id":        {func(a *ArtifactIdentity) { a.ID = "" }, "artifact ID"},
		"bad id":       {func(a *ArtifactIdentity) { a.ID = "art 001" }, "artifact ID"},
		"unknown kind": {func(a *ArtifactIdentity) { a.Kind = run.ArtifactKind("weird") }, "unknown kind"},
		"path traversal": {
			func(a *ArtifactIdentity) { a.Path = "../etc/passwd" },
			"path",
		},
		"long path":      {func(a *ArtifactIdentity) { a.Path = strings.Repeat("x", 513) }, "path"},
		"negative size":  {func(a *ArtifactIdentity) { a.Size = -1 }, "size"},
		"zero digest":    {func(a *ArtifactIdentity) { a.Digest = "" }, "digest"},
		"bad digest":     {func(a *ArtifactIdentity) { a.Digest = Digest("sha256:zz") }, "digest"},
		"long version":   {func(a *ArtifactIdentity) { a.Version = strings.Repeat("v", maxArtifactVersionLen+1) }, "version"},
		"secret version": {func(a *ArtifactIdentity) { a.Version = testToken }, "secret-looking"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			a2 := a
			c.mutate(&a2)
			err := a2.Validate()
			if err == nil || !strings.Contains(err.Error(), c.substr) {
				t.Errorf("%q: got %v, want an error mentioning %q", name, err, c.substr)
			}
		})
	}
}
