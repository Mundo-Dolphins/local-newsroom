package publish_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publish"
	"github.com/Mundo-Dolphins/local-newsroom/internal/review"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// articlePath is the run-relative path of the rendered article.
const articlePath = "output/article.md"

// threadContentPath is the run-relative path of the derived thread content.
const threadContentPath = "publication/content/thread.txt"

// mustCreatePlan creates a single-target plan on the rendered article.
func (h *harness) mustCreatePlan(t *testing.T, artifactPath string, targets ...publish.TargetSpec) *publication.Plan {
	t.Helper()
	req := publish.CreatePlanRequest{
		ArtifactPath: artifactPath,
		ArtifactID:   "article",
		Targets:      targets,
	}
	if len(targets) == 0 {
		req.Targets = []publish.TargetSpec{{ID: "site", Type: publication.TargetTypePage, Provider: "git", Destination: "https://example.com"}}
	}
	plan, err := h.eng.CreatePlan(context.Background(), h.runID, req, h.bundle)
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	return plan
}

// mustApprove approves the current content of a plan.
func (h *harness) mustApprove(t *testing.T, planID publication.PublicationID) *publication.Plan {
	t.Helper()
	plan, err := h.eng.Approve(context.Background(), h.runID, planID, "operator@example.com", h.bundle)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	return plan
}

// reloadedEngine builds a fresh engine over the same store and the same
// continuing clock — the process-restart simulation (state comes from disk,
// not memory; time keeps moving forward, so the manifest-freshness rule
// never trips on it).
func (h *harness) reloadedEngine() *publish.Engine {
	return publish.NewWithClock(h.store, h.clock)
}

// ---------------------------------------------------------------------------
// Scenario: no previous publication
// ---------------------------------------------------------------------------

func TestNoPreviousPublication(t *testing.T) {
	h := newHarness(t)

	has, err := h.eng.HasPlans(h.runID)
	if err != nil {
		t.Fatalf("HasPlans: %v", err)
	}
	if has {
		t.Fatal("HasPlans = true for a run that never published")
	}
	if _, err := h.eng.LatestPlan(h.runID); !errors.Is(err, publish.ErrPlanNotFound) {
		t.Fatalf("LatestPlan err = %v, want ErrPlanNotFound", err)
	}
	ids, err := h.eng.ListPlanIDs(h.runID)
	if err != nil {
		t.Fatalf("ListPlanIDs: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("ListPlanIDs = %v, want none", ids)
	}

	_, err = h.eng.Publish(context.Background(), h.runID, map[publication.TargetID]publish.Publisher{}, publish.Options{})
	if !errors.Is(err, publish.ErrPlanNotFound) {
		t.Fatalf("Publish err = %v, want ErrPlanNotFound", err)
	}

	sum, err := h.eng.Summary(h.runID)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if sum != nil {
		t.Fatalf("manifest publication_state = %+v, want nil", sum)
	}
}

// ---------------------------------------------------------------------------
// Scenario: complete success + repeated publish command is a no-op
// ---------------------------------------------------------------------------

func TestCompleteSuccessAndRepeatedPublish(t *testing.T) {
	h := newHarness(t)

	plan := h.mustCreatePlan(t, articlePath,
		publish.TargetSpec{ID: "site", Type: publication.TargetTypePage, Provider: "git", Destination: "https://example.com"},
		publish.TargetSpec{ID: "thread", Type: publication.TargetTypePost, Provider: "bluesky", Destination: "dolphins.example"},
	)
	h.mustApprove(t, plan.ID)

	sitePub := newFakePublisher(plan.ID, "site")
	threadPub := newFakePublisher(plan.ID, "thread")
	pubs := map[publication.TargetID]publish.Publisher{
		"site":   sitePub,
		"thread": threadPub,
	}

	done := h.mustPublish(t, pubs, publish.Options{})
	if done.Status != publication.StatusSucceeded {
		t.Fatalf("plan status = %s, want succeeded", done.Status)
	}
	for _, id := range []publication.TargetID{"site", "thread"} {
		tt := target(t, done, id)
		if tt.Status != publication.StatusSucceeded {
			t.Errorf("target %s status = %s, want succeeded", id, tt.Status)
		}
		if tt.Receipt == nil || tt.Receipt.ExternalRecordID == "" {
			t.Errorf("target %s has no receipt/record ID", id)
		}
		if sitePub.callCount() != 1 {
			t.Errorf("publisher call count = %d, want exactly 1 (no blind repetition)", sitePub.callCount())
		}
	}

	// The manifest summary must describe the completed publication.
	sum, err := h.eng.Summary(h.runID)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if sum == nil {
		t.Fatal("manifest publication_state is nil after a publication")
	}
	if sum.Status != run.PublicationStatusSucceeded || sum.Targets != 2 || sum.Succeeded != 2 || sum.Failed != 0 {
		t.Fatalf("summary = %+v, want succeeded 2/2/0", sum)
	}
	if sum.PlanID != string(plan.ID) || sum.PlanPath != publish.PlanPath(plan.ID) {
		t.Fatalf("summary plan ref = %s @ %s, want %s @ %s", sum.PlanID, sum.PlanPath, plan.ID, publish.PlanPath(plan.ID))
	}
	if sum.PlanHash == "" {
		t.Fatal("summary plan_hash is empty")
	}

	// Repeated publish command: zero further external calls, same receipts.
	beforeSite, beforeThread := sitePub.callCount(), threadPub.callCount()
	again, err := h.eng.Publish(context.Background(), h.runID, pubs, publish.Options{})
	if err != nil {
		t.Fatalf("repeated Publish: %v", err)
	}
	if again.Status != publication.StatusSucceeded {
		t.Fatalf("repeated publish plan status = %s, want succeeded", again.Status)
	}
	if sitePub.callCount() != beforeSite || threadPub.callCount() != beforeThread {
		t.Fatalf("repeated publish invoked publishers (site %d->%d, thread %d->%d) — successful external actions must never be repeated",
			beforeSite, sitePub.callCount(), beforeThread, threadPub.callCount())
	}
	if got, want := target(t, again, "site").Receipt.ID, target(t, done, "site").Receipt.ID; got != want {
		t.Fatalf("receipt ID changed across the no-op publish: %s != %s", got, want)
	}

	// And it is reported identically from a fresh engine (restart).
	re := h.reloadedEngine()
	latest, err := re.LatestPlan(h.runID)
	if err != nil {
		t.Fatalf("LatestPlan after restart: %v", err)
	}
	if latest.Status != publication.StatusSucceeded {
		t.Fatalf("reloaded plan status = %s, want succeeded", latest.Status)
	}
	if _, err := re.Publish(context.Background(), h.runID, pubs, publish.Options{}); err != nil {
		t.Fatalf("repeated Publish after restart: %v", err)
	}
	if sitePub.callCount() != beforeSite {
		t.Fatal("restart publish re-invoked the publisher")
	}
}

// ---------------------------------------------------------------------------
// Scenario: partial target failure, then retry runs only the remainder
// ---------------------------------------------------------------------------

func TestPartialTargetFailureAndRetry(t *testing.T) {
	h := newHarness(t)

	plan := h.mustCreatePlan(t, articlePath,
		publish.TargetSpec{ID: "site", Type: publication.TargetTypePage, Provider: "git", Destination: "https://example.com"},
		publish.TargetSpec{ID: "thread", Type: publication.TargetTypePost, Provider: "bluesky", Destination: "dolphins.example"},
	)
	h.mustApprove(t, plan.ID)

	// First cycle: the site succeeds, the thread fails.
	sitePub := newFakePublisher(plan.ID, "site")
	threadPub := newFakePublisher(plan.ID, "thread",
		outcome{err: errors.New("the platform is unavailable")},
	)
	pubs := map[publication.TargetID]publish.Publisher{"site": sitePub, "thread": threadPub}

	first := h.mustPublish(t, pubs, publish.Options{})
	if first.Status != publication.StatusPartiallySucceeded {
		t.Fatalf("plan status = %s, want partially_succeeded", first.Status)
	}
	if target(t, first, "site").Status != publication.StatusSucceeded {
		t.Fatal("site target should be succeeded")
	}
	if tt := target(t, first, "thread"); tt.Status != publication.StatusFailed || tt.Error == nil {
		t.Fatalf("thread target = %s (err %v), want failed with an error summary", tt.Status, tt.Error)
	}

	sum, _ := h.eng.Summary(h.runID)
	if sum == nil || sum.Status != run.PublicationStatusPartiallySucceeded || sum.Succeeded != 1 || sum.Failed != 1 {
		t.Fatalf("summary = %+v, want partially_succeeded 1/1", sum)
	}

	// Second cycle (a fresh engine: process restart semantics): the site is
	// NOT re-executed; only the failed thread runs.
	re := h.reloadedEngine()
	reThread := newFakePublisher(plan.ID, "thread") // succeeds now
	rePubs := map[publication.TargetID]publish.Publisher{
		"site":   newFakePublisher(plan.ID, "site"),
		"thread": reThread,
	}
	second, err := re.Publish(context.Background(), h.runID, rePubs, publish.Options{})
	if err != nil {
		t.Fatalf("retry Publish: %v", err)
	}
	if second.Status != publication.StatusSucceeded {
		t.Fatalf("plan status after retry = %s, want succeeded", second.Status)
	}
	if got := reThread.callCount(); got != 1 {
		t.Fatalf("retry executed the thread %d times, want exactly 1", got)
	}
	if got := rePubs["site"].(*fakePublisher).callCount(); got != 0 {
		t.Fatalf("retry re-executed the already-succeeded site target %d time(s) — successful external actions must never be repeated", got)
	}
	if tt := target(t, second, "thread"); tt.Status != publication.StatusSucceeded || tt.Receipt == nil {
		t.Fatalf("thread after retry = %s (receipt %v), want succeeded with a receipt", tt.Status, tt.Receipt)
	}
}

// ---------------------------------------------------------------------------
// Scenario: a thread partially posted mid-cycle resumes from the receipt
// (the classic "crash mid-thread" — a real partial, not a crash)
// ---------------------------------------------------------------------------

func TestPartialThreadResumesFromReceipt(t *testing.T) {
	h := newHarness(t)

	// Derive the canonical thread content and persist it through the engine.
	content, err := publish.ThreadContent(threadPosts...)
	if err != nil {
		t.Fatalf("ThreadContent: %v", err)
	}
	if _, err := h.eng.SaveContent(h.runID, "thread.txt", content); err != nil {
		t.Fatalf("SaveContent: %v", err)
	}

	plan := h.mustCreatePlan(t, threadContentPath,
		publish.TargetSpec{ID: "thread", Type: publication.TargetTypePost, Provider: "bluesky", Destination: "dolphins.example"},
	)
	h.mustApprove(t, plan.ID)

	// First cycle: the platform dies after the first post of three — the
	// publisher confirms a partial with a resumable receipt and an error.
	partial := publication.NewReceipt(
		plan.ID, "thread", "bluesky",
		"https://bsky.social/profile/dolphins.example", "at://dolphins.example/app.bsky.feed.post/r1",
		publication.DigestOf(content),
		"", "newsroom-bluesky/0.6",
		map[string]string{"post_1": "at://dolphins.example/app.bsky.feed.post/r1|cid1"},
		time.Now().UTC(),
	)
	pub := newFakePublisher(plan.ID, "thread",
		outcome{receipt: partial, err: errors.New("post 2 of 3 failed")},
	)
	first := h.mustPublish(t, map[publication.TargetID]publish.Publisher{"thread": pub}, publish.Options{})
	if first.Status != publication.StatusFailed {
		t.Fatalf("plan status = %s, want failed (nothing fully succeeded)", first.Status)
	}
	tt := target(t, first, "thread")
	if tt.Status != publication.StatusPartiallySucceeded {
		t.Fatalf("target status = %s, want partially_succeeded", tt.Status)
	}
	if tt.Receipt == nil || tt.Receipt.ExternalRecordID == "" {
		t.Fatal("a partially posted thread must persist its resumable receipt")
	}

	// Restart: the same thread target is re-executed — and the publisher
	// receives the partial receipt as its resume state.
	re := h.reloadedEngine()
	rePub := newFakePublisher(plan.ID, "thread") // completes the thread now
	if _, err := re.Publish(context.Background(), h.runID, map[publication.TargetID]publish.Publisher{"thread": rePub}, publish.Options{}); err != nil {
		t.Fatalf("resume Publish: %v", err)
	}
	if rePub.callCount() != 1 {
		t.Fatalf("resume executed the thread %d times, want 1", rePub.callCount())
	}
	if got := rePub.callPrior(0); got == nil || got.ExternalRecordID != "at://dolphins.example/app.bsky.feed.post/r1" {
		t.Fatalf("resume call prior receipt = %v, want the persisted partial (post_1 proven)", got)
	}
	plan2, err := re.LatestPlan(h.runID)
	if err != nil {
		t.Fatalf("LatestPlan: %v", err)
	}
	if plan2.Status != publication.StatusSucceeded {
		t.Fatalf("plan after resume = %s, want succeeded", plan2.Status)
	}
	// The resume receipt must still digest the approved content.
	final := target(t, plan2, "thread").Receipt
	if final == nil || !final.ContentDigest.Matches(string(publication.DigestOf(content))) {
		t.Fatalf("final receipt digest = %v, want the approved content digest", final)
	}
}

// ---------------------------------------------------------------------------
// Scenario: crash mid-cycle (dangling in-flight target) resumes safely
// ---------------------------------------------------------------------------

func TestCrashMidCycleAbortsDanglingAndResumes(t *testing.T) {
	h := newHarness(t)

	plan := h.mustCreatePlan(t, articlePath,
		publish.TargetSpec{ID: "site", Type: publication.TargetTypePage, Provider: "git", Destination: "https://example.com"},
		publish.TargetSpec{ID: "thread", Type: publication.TargetTypePost, Provider: "bluesky", Destination: "dolphins.example"},
	)
	// Simulate the in-flight moment of a crashed cycle: the plan is
	// publishing, the site target is mid-call (running attempt persisted),
	// the thread has not started. The process dies here — the external
	// outcome of the site call is unknown. The crash timestamp comes from
	// the shared continuing clock, after every transition already recorded.
	// (mustApprove returns the reloaded plan document, so the mutation
	// below happens on the approved object, not the pre-approval one.)
	plan = h.mustApprove(t, plan.ID)
	at := h.clock()
	if err := plan.StartPublishing(at); err != nil {
		t.Fatalf("StartPublishing: %v", err)
	}
	if err := plan.StartTarget("site", at); err != nil {
		t.Fatalf("StartTarget: %v", err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("crashed-state plan invalid: %v", err)
	}
	h.persistCrashedPlan(t, plan)

	// The restarted process resumes: the dangling site attempt is aborted
	// (cancelled — its external outcome is unproven, so it is never
	// blindly re-executed), and the thread — never started — runs.
	re := h.reloadedEngine()
	sitePub := newFakePublisher(plan.ID, "site")
	threadPub := newFakePublisher(plan.ID, "thread")
	resumed, err := re.Publish(context.Background(), h.runID,
		map[publication.TargetID]publish.Publisher{"site": sitePub, "thread": threadPub},
		publish.Options{})
	if err != nil {
		t.Fatalf("resume Publish: %v", err)
	}
	if got := sitePub.callCount(); got != 0 {
		t.Fatalf("the dangling site target was re-executed %d time(s) — a crash leaves an unknown external state and must not be blindly repeated", got)
	}
	if got := threadPub.callCount(); got != 1 {
		t.Fatalf("the never-started thread target ran %d times, want 1", got)
	}
	if tt := target(t, resumed, "site"); tt.Status != publication.StatusCancelled {
		t.Fatalf("dangling site target = %s, want cancelled (crash bookkeeping)", tt.Status)
	}
	if tt := target(t, resumed, "thread"); tt.Status != publication.StatusSucceeded {
		t.Fatalf("thread target = %s, want succeeded", tt.Status)
	}
	if resumed.Status != publication.StatusPartiallySucceeded {
		t.Fatalf("plan status = %s, want partially_succeeded (1 published, 1 aborted)", resumed.Status)
	}
}

// ---------------------------------------------------------------------------
// Scenario: the artifact changed after approval — publication fails closed
// ---------------------------------------------------------------------------

func TestArtifactModifiedAfterApprovalFailsClosed(t *testing.T) {
	h := newHarness(t)

	plan := h.mustCreatePlan(t, articlePath)
	approved := h.mustApprove(t, plan.ID)
	approvedDigest := string(approved.Artifact.Digest)

	pub := newFakePublisher(approved.ID, "site")
	pubs := map[publication.TargetID]publish.Publisher{"site": pub}

	// The content file changes on disk after the approval was granted.
	changed := []byte(articleContent + "\n-- a sentence appears after approval --\n")
	if _, err := h.store.SaveArtifact(h.runID, run.ArtifactRef{
		Kind: run.ArtifactKindRendered, ID: "article", Path: articlePath,
		ProducedAt: run.NewTimestamp(baseTime.Add(1 * time.Hour)),
	}, changed); err != nil {
		t.Fatalf("SaveArtifact: %v", err)
	}

	_, err := h.eng.Publish(context.Background(), h.runID, pubs, publish.Options{})
	if !errors.Is(err, publish.ErrArtifactChanged) {
		t.Fatalf("Publish err = %v, want ErrArtifactChanged", err)
	}
	if got := pub.callCount(); got != 0 {
		t.Fatalf("publisher was invoked %d time(s) for modified content — a stale approval must never publish", got)
	}
	// The approval is now mechanically stale (the recorded digest no longer
	// matches the on-disk content the plan would read).
	if plan.EffectiveApproval() != nil {
		t.Log("note: the in-memory plan still shows the old baseline; the persisted plan's approval binding is what fails the gate")
	}
	if !strings.Contains(approvedDigest, "sha256:") {
		t.Fatalf("approved digest %q is not canonical", approvedDigest)
	}
}

// ---------------------------------------------------------------------------
// Scenario: a changed artifact after a re-baseline forces a fresh approval
// ---------------------------------------------------------------------------

func TestRefreshArtifactInvalidatesApproval(t *testing.T) {
	h := newHarness(t)

	plan := h.mustCreatePlan(t, articlePath)
	approved := h.mustApprove(t, plan.ID)

	// New content: re-baseline + re-gate in one call.
	changed := []byte(articleContent + "\nUpdated edition.\n")
	newPlan, err := h.eng.RefreshArtifact(context.Background(), h.runID, approved.ID, changed, h.bundle)
	if err != nil {
		t.Fatalf("RefreshArtifact: %v", err)
	}
	if newPlan.Artifact.Digest.Matches(string(approved.Artifact.Digest)) {
		t.Fatal("rebaseline did not change the digest")
	}
	// The old approval must be invalidated as stale in the same document.
	if newPlan.EffectiveApproval() != nil {
		t.Fatalf("approval still effective after the content changed — it must be stale")
	}
	if len(newPlan.Approvals) == 0 || newPlan.Approvals[len(newPlan.Approvals)-1].Status != publication.ApprovalStatusStale {
		t.Fatalf("last approval = %v, want stale", newPlan.Approvals[len(newPlan.Approvals)-1].Status)
	}

	// Publishing the re-baselined plan before a fresh approval is refused,
	// and the old receipt-less approval cannot authorize the new bytes.
	pub := newFakePublisher(newPlan.ID, "site")
	_, err = h.eng.Publish(context.Background(), h.runID, map[publication.TargetID]publish.Publisher{"site": pub}, publish.Options{})
	if !errors.Is(err, publish.ErrStaleApproval) && !errors.Is(err, publish.ErrNotApproved) {
		t.Fatalf("Publish err = %v, want a stale-approval refusal", err)
	}
	if got := pub.callCount(); got != 0 {
		t.Fatalf("publisher invoked %d time(s) with a stale approval", got)
	}

	// And the file must now hold the re-baselined bytes (the engine
	// persisted them), so a fresh approval binds to exactly them.
	onDisk, err := h.store.LoadArtifact(h.runID, newPlan.Artifact.Path)
	if err != nil {
		t.Fatalf("LoadArtifact: %v", err)
	}
	if string(onDisk) != string(changed) {
		t.Fatal("the re-baselined content was not persisted to the plan's path")
	}

	reapproved := h.mustApprove(t, newPlan.ID)
	if string(reapproved.Artifact.Digest) != string(publication.DigestOf(changed)) {
		t.Fatal("fresh approval does not bind to the new content digest")
	}
}

// ---------------------------------------------------------------------------
// Scenario: a corrupted receipt is rejected — never reused, never trusted
// ---------------------------------------------------------------------------

func TestCorruptedReceiptFailsClosed(t *testing.T) {
	h := newHarness(t)

	plan := h.mustCreatePlan(t, articlePath)
	approved := h.mustApprove(t, plan.ID)
	content, err := h.store.LoadArtifact(h.runID, approved.Artifact.Path)
	if err != nil {
		t.Fatalf("LoadArtifact: %v", err)
	}

	// The publisher claims success, but its receipt digests DIFFERENT bytes
	// than what was approved (corrupted or lying receipt): the engine must
	// refuse to mark the target succeeded and must not persist the receipt.
	liar := newFakePublisher(approved.ID, "site",
		outcome{
			receipt: publication.NewReceipt(
				approved.ID, "site", "git", "https://example.com", "deadbeef",
				publication.DigestOf([]byte("some other bytes entirely")),
				"", "test", nil, time.Now().UTC(),
			),
			err: nil,  // "success"
			raw: true, // keep the wrong digest: this is the corrupted receipt
		},
	)
	_, err = h.eng.Publish(context.Background(), h.runID, map[publication.TargetID]publish.Publisher{"site": liar}, publish.Options{})
	if err != nil {
		t.Fatalf("Publish returned a hard error %v (the recorded failure is the expected outcome)", err)
	}

	reloaded, err := h.reloadedEngine().LatestPlan(h.runID)
	if err != nil {
		t.Fatalf("LatestPlan: %v", err)
	}
	tt := target(t, reloaded, "site")
	if tt.Status == publication.StatusSucceeded {
		t.Fatal("a target with a digest-mismatched receipt was marked succeeded — the approved bytes were not what the receipt proves")
	}
	if tt.Receipt != nil {
		t.Fatalf("the rejected receipt was persisted on the target: %+v", tt.Receipt)
	}
	if tt.Status != publication.StatusFailed {
		t.Fatalf("target = %s, want failed", tt.Status)
	}
	if reloaded.Status != publication.StatusFailed {
		t.Fatalf("plan = %s, want failed", reloaded.Status)
	}
	// The plan-level warning names the rejection for a human.
	found := false
	for _, w := range reloaded.Warnings {
		if w.Code == "receipt-rejected" {
			found = true
		}
	}
	if !found {
		t.Fatal("no receipt-rejected warning was recorded on the plan")
	}
	// The corrupt receipt is not reusable: a retry re-derives from scratch
	// (the next call must receive prior == nil).
	rePub := newFakePublisher(approved.ID, "site") // honest now
	if _, err := h.reloadedEngine().Publish(context.Background(), h.runID, map[publication.TargetID]publish.Publisher{"site": rePub}, publish.Options{}); err != nil {
		t.Fatalf("retry after rejected receipt: %v", err)
	}
	if rePub.callPrior(0) != nil {
		t.Fatal("a retry received the rejected receipt as its resume state")
	}
	if got := string(publication.DigestOf(rePub.callContent(0))); got != string(publication.DigestOf(content)) {
		t.Fatalf("retry sent %s, want the approved bytes", got)
	}
}

// ---------------------------------------------------------------------------
// Scenario: a corrupted plan document fails closed
// ---------------------------------------------------------------------------

func TestCorruptedPlanDocumentFailsClosed(t *testing.T) {
	h := newHarness(t)
	plan := h.mustCreatePlan(t, articlePath)
	h.mustApprove(t, plan.ID)

	// A publisher that would publish — to prove the document, not the
	// publisher, is what stops the cycle.
	pub := newFakePublisher(plan.ID, "site")

	// Overwrite the plan document with garbage (a torn write).
	runDir, err := h.store.RunDir(h.runID)
	if err != nil {
		t.Fatalf("RunDir: %v", err)
	}
	planFile := filepath.Join(runDir, publish.PlanPath(plan.ID))
	if err := os.WriteFile(planFile, []byte("{ this is not json"), 0o600); err != nil {
		t.Fatalf("corrupting plan file: %v", err)
	}

	_, err = h.eng.Publish(context.Background(), h.runID, map[publication.TargetID]publish.Publisher{"site": pub}, publish.Options{})
	if !errors.Is(err, publish.ErrPlanInvalid) {
		t.Fatalf("Publish err = %v, want ErrPlanInvalid (corrupted plan must fail closed)", err)
	}
	if got := pub.callCount(); got != 0 {
		t.Fatalf("the publisher ran %d time(s) against a corrupted plan", got)
	}
}

// ---------------------------------------------------------------------------
// Scenario: an old v0.5 run (no publication fields) loads and publishes
// ---------------------------------------------------------------------------

func TestOldV05RunLoadsAndPublishes(t *testing.T) {
	h := newHarness(t)

	// Strip the v0.6 field from the persisted manifest: this is exactly
	// what a 0.5.0 manifest on disk looks like.
	m, _, err := h.store.LoadManifest(h.runID)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	m.PublicationState = nil
	if _, err := h.store.SaveManifest(m); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}

	// A 0.5.0 document (no publication_state) must load without the field.
	loaded, _, err := h.store.LoadManifest(h.runID)
	if err != nil {
		t.Fatalf("a v0.5-style manifest must load: %v", err)
	}
	if loaded.PublicationState != nil {
		t.Fatalf("v0.5 manifest loaded with a publication_state: %+v", loaded.PublicationState)
	}
	if err := loaded.Validate(); err != nil {
		t.Fatalf("v0.5 manifest must validate: %v", err)
	}

	has, err := h.eng.HasPlans(h.runID)
	if err != nil {
		t.Fatalf("HasPlans: %v", err)
	}
	if has {
		t.Fatal("HasPlans for a v0.5 run = true, want false")
	}

	// ... and publication works on it: the first plan creates the field.
	plan := h.mustCreatePlan(t, articlePath)
	h.mustApprove(t, plan.ID)
	if _, err := h.eng.Publish(context.Background(), h.runID,
		map[publication.TargetID]publish.Publisher{"site": newFakePublisher(plan.ID, "site")},
		publish.Options{}); err != nil {
		t.Fatalf("Publish on a v0.5-origin run: %v", err)
	}
	sum, err := h.eng.Summary(h.runID)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if sum == nil || sum.PlanID != string(plan.ID) {
		t.Fatalf("after the first publication the manifest must carry the summary: %+v", sum)
	}
	// And the whole document still round-trips as major-0.
	final, _, err := h.store.LoadManifest(h.runID)
	if err != nil {
		t.Fatalf("final LoadManifest: %v", err)
	}
	if final.PublicationState == nil {
		t.Fatal("the publication_state field was lost on reload")
	}
}

// ---------------------------------------------------------------------------
// Scenario: secrets never reach the persisted publication state
// ---------------------------------------------------------------------------

func TestSecretSweepOnPersistedPublicationState(t *testing.T) {
	h := newHarness(t)

	// A thread whose second post carries a secret-looking token.
	content, err := publish.ThreadContent("EU AI Act enters into force.",
		"Token leaked: "+testSecret+" apologies.")
	if err != nil {
		t.Fatalf("ThreadContent: %v", err)
	}
	if _, err := h.eng.SaveContent(h.runID, "thread.txt", content); err != nil {
		t.Fatalf("SaveContent: %v", err)
	}

	plan := h.mustCreatePlan(t, threadContentPath,
		publish.TargetSpec{ID: "thread", Type: publication.TargetTypePost, Provider: "bluesky", Destination: "dolphins.example"},
	)
	h.mustApprove(t, plan.ID)

	// A secret in a free-text reason must be redacted, not persisted.
	if _, err := h.eng.SkipTarget(context.Background(), h.runID, plan.ID, "thread",
		"do not post — the draft contains "+testSecret); err != nil {
		t.Fatalf("SkipTarget: %v", err)
	}
	// Complete the cycle (all targets skipped -> skipped plan).
	if _, err := h.eng.Publish(context.Background(), h.runID,
		map[publication.TargetID]publish.Publisher{"thread": newFakePublisher(plan.ID, "thread")},
		publish.Options{}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	runDir, err := h.store.RunDir(h.runID)
	if err != nil {
		t.Fatalf("RunDir: %v", err)
	}
	var sawRedaction bool
	err = filepath.WalkDir(runDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), testSecret) {
			t.Errorf("the secret %q reached disk in %s", testSecret, p)
			return nil
		}
		if strings.Contains(string(data), "[REDACTED]") {
			sawRedaction = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking run dir: %v", err)
	}
	if !sawRedaction {
		t.Fatal("no [REDACTED] marker was found: the secret may have been silently dropped without the documented redaction")
	}
}

// ---------------------------------------------------------------------------
// Approval and gate edge cases
// ---------------------------------------------------------------------------

func TestPublishWithoutApprovalRefused(t *testing.T) {
	h := newHarness(t)
	plan := h.mustCreatePlan(t, articlePath) // created, NOT approved

	pub := newFakePublisher(plan.ID, "site")
	_, err := h.eng.Publish(context.Background(), h.runID, map[publication.TargetID]publish.Publisher{"site": pub}, publish.Options{})
	if !errors.Is(err, publish.ErrNotApproved) {
		t.Fatalf("Publish err = %v, want ErrNotApproved", err)
	}
	if got := pub.callCount(); got != 0 {
		t.Fatalf("publisher ran %d time(s) on an unapproved plan", got)
	}
}

func TestPublishOnCancelledPlanRefused(t *testing.T) {
	h := newHarness(t)
	plan := h.mustCreatePlan(t, articlePath)
	h.mustApprove(t, plan.ID)
	if _, err := h.eng.Cancel(context.Background(), h.runID, plan.ID, "editor decided against it"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	pub := newFakePublisher(plan.ID, "site")
	_, err := h.eng.Publish(context.Background(), h.runID, map[publication.TargetID]publish.Publisher{"site": pub}, publish.Options{})
	if !errors.Is(err, publish.ErrPlanCancelled) {
		t.Fatalf("Publish err = %v, want ErrPlanCancelled", err)
	}
	if got := pub.callCount(); got != 0 {
		t.Fatalf("a cancelled plan ran a publisher %d time(s)", got)
	}
}

func TestPublishWithMissingPublisherRefusedBeforeStateChange(t *testing.T) {
	h := newHarness(t)
	plan := h.mustCreatePlan(t, articlePath,
		publish.TargetSpec{ID: "site", Type: publication.TargetTypePage, Provider: "git", Destination: "https://example.com"},
		publish.TargetSpec{ID: "thread", Type: publication.TargetTypePost, Provider: "bluesky", Destination: "dolphins.example"},
	)
	h.mustApprove(t, plan.ID)

	// Only the site has a publisher bound.
	_, err := h.eng.Publish(context.Background(), h.runID,
		map[publication.TargetID]publish.Publisher{"site": newFakePublisher(plan.ID, "site")},
		publish.Options{})
	if !errors.Is(err, publish.ErrPublisherNotBound) {
		t.Fatalf("Publish err = %v, want ErrPublisherNotBound", err)
	}

	// No state change: the plan is still approved, no target started.
	reloaded, _ := h.reloadedEngine().LatestPlan(h.runID)
	if reloaded.Status != publication.StatusApproved {
		t.Fatalf("plan status = %s, want approved (the missing binding must not start the cycle)", reloaded.Status)
	}
	for i := range reloaded.Targets {
		if reloaded.Targets[i].Status != publication.StatusPlanned {
			t.Fatalf("target %s = %s, want planned (nothing started)", reloaded.Targets[i].ID, reloaded.Targets[i].Status)
		}
	}
}

func TestGateFailsForNotReadyRun(t *testing.T) {
	// A run whose final check FAILED is not READY: no plan can be created.
	h := newHarness(t)
	// Flip the persisted final check to a failure and refresh the bundle.
	if _, err := h.store.SaveArtifact(h.runID, run.ArtifactRef{
		Kind: run.ArtifactKindFactualCheck, ID: "final-check", Path: "final_check/result.json",
		ProducedAt: run.NewTimestamp(baseTime.Add(1 * time.Hour)),
	}, []byte(`{"overall_status":"fail"}`)); err != nil {
		t.Fatalf("SaveArtifact: %v", err)
	}
	m, _, err := h.store.LoadManifest(h.runID)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	bundle, err := review.Build(h.store, m)
	if err != nil {
		t.Fatalf("review.Build: %v", err)
	}
	if ready, _ := review.Readiness(bundle); ready {
		t.Fatal("a run with a failing final check must not be READY")
	}

	_, err = h.eng.CreatePlan(context.Background(), h.runID, publish.CreatePlanRequest{
		ArtifactPath: articlePath,
		Targets:      []publish.TargetSpec{{ID: "site", Type: publication.TargetTypePage, Provider: "git", Destination: "https://example.com"}},
	}, bundle)
	if !errors.Is(err, publish.ErrGateFailed) {
		t.Fatalf("CreatePlan err = %v, want ErrGateFailed", err)
	}
}

// ---------------------------------------------------------------------------
// Dependencies: a failed input target prevents its dependents
// ---------------------------------------------------------------------------

func TestDependencyFailurePreventsDependent(t *testing.T) {
	h := newHarness(t)
	plan := h.mustCreatePlan(t, articlePath,
		publish.TargetSpec{ID: "site", Type: publication.TargetTypePage, Provider: "git", Destination: "https://example.com"},
		publish.TargetSpec{ID: "push", Type: publication.TargetTypePage, Provider: "git", Destination: "https://example.com", DependsOn: []publication.TargetID{"site"}},
	)
	plan = h.mustApprove(t, plan.ID)

	sitePub := newFakePublisher(plan.ID, "site", outcome{err: errors.New("the repository is read-only")})
	pushPub := newFakePublisher(plan.ID, "push")
	plan = h.mustPublish(t, map[publication.TargetID]publish.Publisher{"site": sitePub, "push": pushPub}, publish.Options{})

	if got := pushPub.callCount(); got != 0 {
		t.Fatalf("the dependent push target ran %d time(s) although its dependency failed", got)
	}
	if tt := target(t, plan, "push"); tt.Status != publication.StatusFailed || tt.Error == nil || tt.Error.Code != "dependency-incomplete" {
		t.Fatalf("push target = %s (code %q), want failed/dependency-incomplete", tt.Status, errCode(tt.Error))
	}

	// Retry with a healthy site: the push target now runs exactly once.
	re := h.reloadedEngine()
	reSite := newFakePublisher(plan.ID, "site")
	rePush := newFakePublisher(plan.ID, "push")
	var err error
	_, err = re.Publish(context.Background(), h.runID,
		map[publication.TargetID]publish.Publisher{"site": reSite, "push": rePush},
		publish.Options{})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := rePush.callCount(); got != 1 {
		t.Fatalf("push ran %d times on the retry, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// Manifest summary discipline
// ---------------------------------------------------------------------------

func TestManifestSummaryTracksPlanThroughLifecycle(t *testing.T) {
	h := newHarness(t)
	plan := h.mustCreatePlan(t, articlePath)

	sum, _ := h.eng.Summary(h.runID)
	if sum == nil || sum.Status != run.PublicationStatusPlanned {
		t.Fatalf("after creation summary = %+v, want planned", sum)
	}
	hashAfterCreate := sum.PlanHash
	if hashAfterCreate == "" {
		t.Fatal("plan hash is empty after creation")
	}

	h.mustApprove(t, plan.ID)
	sum, _ = h.eng.Summary(h.runID)
	if sum == nil || sum.Status != run.PublicationStatusApproved {
		t.Fatalf("after approval summary = %+v, want approved", sum)
	}

	pub := newFakePublisher(plan.ID, "site", outcome{err: errors.New("network down")})
	if _, err := h.eng.Publish(context.Background(), h.runID, map[publication.TargetID]publish.Publisher{"site": pub}, publish.Options{}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	sum, _ = h.eng.Summary(h.runID)
	if sum == nil || sum.Status != run.PublicationStatusFailed || sum.Failed != 1 {
		t.Fatalf("after a failed cycle summary = %+v, want failed/1", sum)
	}
}

// ---------------------------------------------------------------------------
// Dry runs are recorded without side effects
// ---------------------------------------------------------------------------

func TestDryRunRecordedWithoutStatusChange(t *testing.T) {
	h := newHarness(t)
	plan := h.mustCreatePlan(t, articlePath)
	content, err := h.store.LoadArtifact(h.runID, plan.Artifact.Path)
	if err != nil {
		t.Fatalf("LoadArtifact: %v", err)
	}

	dr := publication.NewDryRunResult(baseTime.Add(2*time.Hour), publication.DryRunOutcomeOK, publication.DigestOf(content), "1 file", nil)
	if dr == nil {
		t.Fatal("NewDryRunResult returned nil")
	}
	if _, err := h.eng.RecordDryRun(context.Background(), h.runID, plan.ID, "site", dr); err != nil {
		t.Fatalf("RecordDryRun: %v", err)
	}
	reloaded, _ := h.reloadedEngine().LatestPlan(h.runID)
	if reloaded.Status != publication.StatusPlanned {
		t.Fatalf("a dry run changed the plan status to %s — it must stay planned", reloaded.Status)
	}
	if tt := target(t, reloaded, "site"); tt.DryRun == nil {
		t.Fatal("the dry run result was not persisted on the target")
	}
}

// ---------------------------------------------------------------------------
// Termination discipline: a terminal plan is an immutable record
// ---------------------------------------------------------------------------

func TestTerminalPlanCannotBeMutated(t *testing.T) {
	h := newHarness(t)
	plan := h.mustCreatePlan(t, articlePath)
	h.mustApprove(t, plan.ID)
	h.mustPublish(t, map[publication.TargetID]publish.Publisher{"site": newFakePublisher(plan.ID, "site")}, publish.Options{})

	// A finished plan cannot be re-approved, re-baselined, or cancelled.
	if _, err := h.eng.Approve(context.Background(), h.runID, plan.ID, "other@example.com", h.bundle); err == nil {
		t.Fatal("a succeeded (terminal) plan accepted a re-approval")
	}
	if _, err := h.eng.Cancel(context.Background(), h.runID, plan.ID, "change of mind"); err == nil {
		t.Fatal("a succeeded (terminal) plan accepted a cancellation")
	}
	if _, err := h.eng.RefreshArtifact(context.Background(), h.runID, plan.ID, []byte("new bytes"), h.bundle); err == nil {
		t.Fatal("a succeeded (terminal) plan accepted a re-baseline")
	}
}
