package publish_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publish"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// tamperContent overwrites the plan's content file through the store (the
// "the artifact changed on disk" simulation).
func tamperContent(t *testing.T, h *harness, data []byte) {
	t.Helper()
	if _, err := h.store.SaveArtifact(h.runID, run.ArtifactRef{
		Kind:       run.ArtifactKindRendered,
		ID:         "article.md",
		Path:       "output/article.md",
		ProducedAt: run.NewTimestamp(time.Now().UTC()),
	}, data); err != nil {
		t.Fatalf("tamperContent: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Plan-level API: the engine methods that shape a plan before (or instead
// of) the publish cycle. Every branch is exercised, happy and fail-closed.
// ---------------------------------------------------------------------------

// TestCreatePlanDefaults pins the request defaults: an empty kind and an
// empty artifact ID are filled from the content path, and an invalid kind
// falls back to rendered rather than failing the plan.
func TestCreatePlanDefaults(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	plan, err := h.eng.CreatePlan(ctx, h.runID, publish.CreatePlanRequest{
		ArtifactPath: "output/article.md",
		Targets: []publish.TargetSpec{
			{ID: "site", Type: publication.TargetTypePage, Provider: "git", Destination: "https://example.com"},
		},
	}, h.bundle)
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	if plan.Artifact.Kind != run.ArtifactKindRendered {
		t.Errorf("kind = %q, want the rendered default", plan.Artifact.Kind)
	}
	if plan.Artifact.ID != "article.md" {
		t.Errorf("artifact id = %q, want the content file name", plan.Artifact.ID)
	}

	// An invalid kind falls back to the default instead of failing.
	plan2, err := h.eng.CreatePlan(ctx, h.runID, publish.CreatePlanRequest{
		ArtifactID:   "article.md",
		ArtifactKind: run.ArtifactKind("bogus-kind"),
		ArtifactPath: "output/article.md",
		Targets: []publish.TargetSpec{
			{ID: "site", Type: publication.TargetTypePage, Provider: "git", Destination: "https://example.com"},
		},
	}, h.bundle)
	if err != nil {
		t.Fatalf("CreatePlan (invalid kind): %v", err)
	}
	if plan2.Artifact.Kind != run.ArtifactKindRendered {
		t.Errorf("invalid kind fell back to %q, want rendered", plan2.Artifact.Kind)
	}
}

// TestCreatePlanMissingContent pins the fail-closed read of the content
// file: a plan cannot be baselined to bytes that are not there.
func TestCreatePlanMissingContent(t *testing.T) {
	h := newHarness(t)
	_, err := h.eng.CreatePlan(context.Background(), h.runID, publish.CreatePlanRequest{
		ArtifactID:   "ghost.md",
		ArtifactPath: "output/does-not-exist.md",
		Targets: []publish.TargetSpec{
			{ID: "site", Type: publication.TargetTypePage, Provider: "git", Destination: "https://example.com"},
		},
	}, h.bundle)
	if err == nil {
		t.Fatal("CreatePlan with a missing content file must fail")
	}
}

// TestApproveFailClosed branches of Approve.
func TestApproveFailClosed(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	plan := h.mustCreatePlan(t, articlePath,
		publish.TargetSpec{ID: "site", Type: publication.TargetTypePage, Provider: "git", Destination: "https://example.com"},
	)

	// No approver identity.
	if _, err := h.eng.Approve(ctx, h.runID, plan.ID, "", h.bundle); err == nil {
		t.Error("Approve with an empty approver must fail")
	}

	// Unknown plan.
	if _, err := h.eng.Approve(ctx, h.runID, testPlanID(), "editor", h.bundle); !errors.Is(err, publish.ErrPlanNotFound) {
		t.Errorf("Approve of an unknown plan = %v, want ErrPlanNotFound", err)
	}

	// Content changed after the plan was baselined but before any approval:
	// ErrArtifactChanged (re-baseline first).
	tamperContent(t, h, []byte("tampered bytes"))
	if _, err := h.eng.Approve(ctx, h.runID, plan.ID, "editor", h.bundle); !errors.Is(err, publish.ErrArtifactChanged) {
		t.Errorf("Approve after an unapproved content change = %v, want ErrArtifactChanged", err)
	}
}

// TestApproveStaleApproval pins the stale-approval branch: content changed
// AFTER the approval was granted.
func TestApproveStaleApproval(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	plan := h.mustCreatePlan(t, articlePath,
		publish.TargetSpec{ID: "site", Type: publication.TargetTypePage, Provider: "git", Destination: "https://example.com"},
	)
	h.mustApprove(t, plan.ID)

	// The content file changes after the approval.
	tamperContent(t, h, []byte("tampered bytes"))
	_, err := h.eng.Approve(ctx, h.runID, plan.ID, "editor", h.bundle)
	if !errors.Is(err, publish.ErrStaleApproval) {
		t.Fatalf("Approve after a post-approval content change = %v, want ErrStaleApproval", err)
	}
}

// TestApproveTerminal pins that a terminal plan can never be approved.
func TestApproveTerminal(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	plan := h.mustCreatePlan(t, articlePath,
		publish.TargetSpec{ID: "site", Type: publication.TargetTypePage, Provider: "git", Destination: "https://example.com"},
	)
	if _, err := h.eng.Cancel(ctx, h.runID, plan.ID, "no more"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if _, err := h.eng.Approve(ctx, h.runID, plan.ID, "editor", h.bundle); !errors.Is(err, publish.ErrPlanCancelled) {
		t.Fatalf("Approve of a cancelled plan = %v, want ErrPlanCancelled", err)
	}
}

// TestRevokeApproval covers the full branch set: a granted approval is
// revoked (the plan no longer authorizes), a bogus ID fails, and a terminal
// plan refuses.
func TestRevokeApproval(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	plan := h.mustCreatePlan(t, articlePath,
		publish.TargetSpec{ID: "site", Type: publication.TargetTypePage, Provider: "git", Destination: "https://example.com"},
	)
	approved := h.mustApprove(t, plan.ID)
	if len(approved.Approvals) != 1 {
		t.Fatalf("approvals = %d, want 1", len(approved.Approvals))
	}

	// A bogus approval ID fails without touching the plan.
	if _, err := h.eng.RevokeApproval(ctx, h.runID, plan.ID, publication.ApprovalID(testPlanID().String()), "changed my mind"); err == nil {
		t.Error("RevokeApproval with an unknown approval ID must fail")
	}
	// The real approval is revoked.
	revoked, err := h.eng.RevokeApproval(ctx, h.runID, plan.ID, approved.Approvals[0].ID, "the source retracted the story")
	if err != nil {
		t.Fatalf("RevokeApproval: %v", err)
	}
	if revoked.Status != publication.StatusPlanned {
		t.Errorf("plan status after revocation = %s, want planned", revoked.Status)
	}
	// The plan no longer authorizes publication.
	fake := newFakePublisher(plan.ID, "site")
	if _, err := h.eng.Publish(ctx, h.runID, map[publication.TargetID]publish.Publisher{"site": fake}, publish.Options{}); !errors.Is(err, publish.ErrNotApproved) {
		t.Fatalf("Publish after revocation = %v, want ErrNotApproved", err)
	}
	if fake.callCount() != 0 {
		t.Fatal("the publisher ran after the approval was revoked")
	}

	// A terminal plan refuses further approval surgery.
	if _, err := h.eng.Cancel(ctx, h.runID, plan.ID, "done for good"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if _, err := h.eng.RevokeApproval(ctx, h.runID, plan.ID, approved.Approvals[0].ID, "too late"); err == nil {
		t.Error("RevokeApproval on a terminal plan must fail")
	}
}

// TestCancelAndSkipBranches: cancellation and target skipping, happy and
// fail-closed.
func TestCancelAndSkipBranches(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	plan := h.mustCreatePlan(t, articlePath,
		publish.TargetSpec{ID: "site", Type: publication.TargetTypePage, Provider: "git", Destination: "https://example.com"},
		publish.TargetSpec{ID: "thread", Type: publication.TargetTypePost, Provider: "bluesky", Destination: "dolphins.example"},
	)
	plan = h.mustApprove(t, plan.ID)

	// A missing reason fails; a bogus target fails; the real skip lands.
	if _, err := h.eng.SkipTarget(ctx, h.runID, plan.ID, "thread", ""); err == nil {
		t.Error("SkipTarget without a reason must fail")
	}
	if _, err := h.eng.SkipTarget(ctx, h.runID, plan.ID, "ghost", "no"); err == nil {
		t.Error("SkipTarget of an unknown target must fail")
	}
	skipped, err := h.eng.SkipTarget(ctx, h.runID, plan.ID, "thread", "bluesky is down this week")
	if err != nil {
		t.Fatalf("SkipTarget: %v", err)
	}
	if tt := target(t, skipped, "thread"); tt.Status != publication.StatusSkipped {
		t.Errorf("skipped target status = %s", tt.Status)
	}

	// Cancellation with a reason lands; a second cancellation (terminal) fails.
	cancelled, err := h.eng.Cancel(ctx, h.runID, plan.ID, "pulling the run")
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if cancelled.Status != publication.StatusCancelled {
		t.Errorf("cancelled plan status = %s", cancelled.Status)
	}
	if _, err := h.eng.Cancel(ctx, h.runID, plan.ID, "again"); err == nil {
		t.Error("Cancel of a terminal plan must fail")
	}
	// Cancellation without a reason fails.
	plan2 := h.mustCreatePlan(t, articlePath,
		publish.TargetSpec{ID: "site", Type: publication.TargetTypePage, Provider: "git", Destination: "https://example.com"},
	)
	if _, err := h.eng.Cancel(ctx, h.runID, plan2.ID, ""); err == nil {
		t.Error("Cancel without a reason must fail")
	}
}

// TestRefreshArtifactBranches: the fail-closed branches of the re-baseline.
func TestRefreshArtifactBranches(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	plan := h.mustCreatePlan(t, articlePath,
		publish.TargetSpec{ID: "site", Type: publication.TargetTypePage, Provider: "git", Destination: "https://example.com"},
	)

	// nil content fails.
	if _, err := h.eng.RefreshArtifact(ctx, h.runID, plan.ID, nil, h.bundle); err == nil {
		t.Error("RefreshArtifact with nil content must fail")
	}
	// A terminal plan refuses re-baselining.
	if _, err := h.eng.Cancel(ctx, h.runID, plan.ID, "done"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if _, err := h.eng.RefreshArtifact(ctx, h.runID, plan.ID, []byte("new"), h.bundle); !errors.Is(err, publish.ErrPlanCancelled) {
		t.Fatalf("RefreshArtifact of a terminal plan = %v, want ErrPlanCancelled", err)
	}
	// An unknown plan fails.
	if _, err := h.eng.RefreshArtifact(ctx, h.runID, testPlanID(), []byte("new"), h.bundle); !errors.Is(err, publish.ErrPlanNotFound) {
		t.Fatalf("RefreshArtifact of an unknown plan = %v, want ErrPlanNotFound", err)
	}
}

// TestRecordDryRunBranches: dry runs record on planned/approved plans only.
func TestRecordDryRunBranches(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	plan := h.mustCreatePlan(t, articlePath,
		publish.TargetSpec{ID: "site", Type: publication.TargetTypePage, Provider: "git", Destination: "https://example.com"},
	)

	// nil result fails; a bogus plan fails.
	if _, err := h.eng.RecordDryRun(ctx, h.runID, plan.ID, "site", nil); err == nil {
		t.Error("RecordDryRun with a nil result must fail")
	}
	if _, err := h.eng.RecordDryRun(ctx, h.runID, testPlanID(), "site", &publication.DryRunResult{}); !errors.Is(err, publish.ErrPlanNotFound) {
		t.Fatalf("RecordDryRun on an unknown plan = %v, want ErrPlanNotFound", err)
	}
	// On a terminal plan it fails (dry runs are pre-cycle only).
	if _, err := h.eng.Cancel(ctx, h.runID, plan.ID, "done"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if _, err := h.eng.RecordDryRun(ctx, h.runID, plan.ID, "site", &publication.DryRunResult{}); err == nil {
		t.Error("RecordDryRun on a terminal plan must fail")
	}
}

// TestPlanInspection: the read-only surface (Plan, LatestPlan, ListPlanIDs,
// HasPlans, Summary) over a multi-plan run.
func TestPlanInspection(t *testing.T) {
	h := newHarness(t)
	specs := []publish.TargetSpec{
		{ID: "site", Type: publication.TargetTypePage, Provider: "git", Destination: "https://example.com"},
	}
	first := h.mustCreatePlan(t, articlePath, specs[0])
	second := h.mustCreatePlan(t, articlePath, specs[0])

	// HasPlans.
	if ok, err := h.eng.HasPlans(h.runID); err != nil || !ok {
		t.Fatalf("HasPlans = %v, %v; want true, nil", ok, err)
	}
	// ListPlanIDs: both, in creation order.
	ids, err := h.eng.ListPlanIDs(h.runID)
	if err != nil {
		t.Fatalf("ListPlanIDs: %v", err)
	}
	if len(ids) != 2 || ids[0] != first.ID || ids[1] != second.ID {
		t.Fatalf("ListPlanIDs = %v, want [%s %s]", ids, first.ID, second.ID)
	}
	// Plan by ID returns the right document.
	got, err := h.eng.Plan(h.runID, second.ID)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got.ID != second.ID {
		t.Fatalf("Plan(%s) returned %s", second.ID, got.ID)
	}
	// A bogus ID fails closed.
	if _, err := h.eng.Plan(h.runID, testPlanID()); !errors.Is(err, publish.ErrPlanNotFound) {
		t.Fatalf("Plan(bogus) = %v, want ErrPlanNotFound", err)
	}
	// LatestPlan is the most recent plan on disk.
	latest, err := h.eng.LatestPlan(h.runID)
	if err != nil {
		t.Fatalf("LatestPlan: %v", err)
	}
	if latest.ID != second.ID {
		t.Fatalf("LatestPlan = %s, want %s", latest.ID, second.ID)
	}
	// Summary reflects the manifest's view of the latest plan.
	sum, err := h.eng.Summary(h.runID)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if sum == nil || sum.PlanID != string(second.ID) {
		t.Fatalf("Summary = %+v, want plan %s", sum, second.ID)
	}

	// A run with no plans at all.
	h2 := newHarness(t)
	if ok, err := h2.eng.HasPlans(h2.runID); err != nil || ok {
		t.Fatalf("HasPlans on an empty run = %v, %v; want false, nil", ok, err)
	}
	if _, err := h2.eng.LatestPlan(h2.runID); !errors.Is(err, publish.ErrPlanNotFound) {
		t.Fatalf("LatestPlan on an empty run = %v, want ErrPlanNotFound", err)
	}
	if _, err := h2.eng.Summary(h2.runID); err != nil {
		t.Fatalf("Summary on an empty run: %v", err)
	}
	// (An empty run simply has no summary: nil summary, no error.)
}
