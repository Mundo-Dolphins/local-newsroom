package publish_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/local-newsroom/internal/gitpub"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publish"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workspace"
)

// ---------------------------------------------------------------------------
// Error-branch coverage: a spy store that injects store failures, proving the
// engine fails closed (and says why) when the persistence boundary itself
// misbehaves.
// ---------------------------------------------------------------------------

// spyStore wraps a real workspace store and injects failures per method.
type spyStore struct {
	*workspace.Store

	failSaveArtifact    error
	failSaveManifest    error
	failLoadArtifact    error
	failLoadArtifactIf  func(relPath string) bool // returns true → fail that read with failLoadArtifactErr
	failLoadArtifactErr error
	failLoadManifest    error
	failListArtifacts   error
}

func (s *spyStore) SaveArtifact(id run.RunID, ref run.ArtifactRef, data []byte) (string, error) {
	if s.failSaveArtifact != nil {
		return "", s.failSaveArtifact
	}
	return s.Store.SaveArtifact(id, ref, data)
}

func (s *spyStore) SaveManifest(m *run.Manifest) (string, error) {
	if s.failSaveManifest != nil {
		return "", s.failSaveManifest
	}
	return s.Store.SaveManifest(m)
}

func (s *spyStore) LoadArtifact(id run.RunID, relPath string) ([]byte, error) {
	if s.failLoadArtifact != nil {
		return nil, s.failLoadArtifact
	}
	if s.failLoadArtifactIf != nil && s.failLoadArtifactIf(relPath) {
		return nil, s.failLoadArtifactErr
	}
	return s.Store.LoadArtifact(id, relPath)
}

func (s *spyStore) LoadManifest(id run.RunID) (*run.Manifest, string, error) {
	if s.failLoadManifest != nil {
		return nil, "", s.failLoadManifest
	}
	return s.Store.LoadManifest(id)
}

func (s *spyStore) ListArtifacts(id run.RunID, relDir string) ([]string, error) {
	if s.failListArtifacts != nil {
		return nil, s.failListArtifacts
	}
	return s.Store.ListArtifacts(id, relDir)
}

// sitePubs binds one fake publisher to the plan's single site target.
func sitePubs(plan *publication.Plan) map[publication.TargetID]publish.Publisher {
	return map[publication.TargetID]publish.Publisher{"site": newFakePublisher(plan.ID, "site")}
}

func TestEngineFailsClosedOnStoreErrors(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	plan := h.mustCreatePlan(t, articlePath,
		publish.TargetSpec{ID: "site", Type: publication.TargetTypePage, Provider: "git", Destination: "https://example.com"},
	)
	plan = h.mustApprove(t, plan.ID)

	// Pre-flight: the content file is gone (not-exist) → ErrArtifactMissing.
	eng := publish.NewWithClock(&spyStore{
		Store:               h.store,
		failLoadArtifactIf:  func(rel string) bool { return rel == articlePath },
		failLoadArtifactErr: os.ErrNotExist,
	}, h.clock)
	if _, err := eng.Publish(ctx, h.runID, sitePubs(plan), publish.Options{}); !errors.Is(err, publish.ErrArtifactMissing) {
		t.Errorf("Publish with a missing content file = %v, want ErrArtifactMissing", err)
	}

	// Pre-flight: a non-not-exist read failure → ErrArtifactMissing too.
	eng = publish.NewWithClock(&spyStore{
		Store:               h.store,
		failLoadArtifactIf:  func(rel string) bool { return rel == articlePath },
		failLoadArtifactErr: errors.New("disk on fire"),
	}, h.clock)
	if _, err := eng.Publish(ctx, h.runID, sitePubs(plan), publish.Options{}); !errors.Is(err, publish.ErrArtifactMissing) {
		t.Errorf("Publish with a read failure = %v, want ErrArtifactMissing", err)
	}

	// The manifest write failing must surface (the plan write landed; the
	// summary did not — the persisted plan is still the resume point).
	eng = publish.NewWithClock(&spyStore{Store: h.store, failSaveManifest: errors.New("manifest write refused")}, h.clock)
	if _, err := eng.Publish(ctx, h.runID, sitePubs(plan), publish.Options{}); err == nil || !strings.Contains(err.Error(), "manifest write refused") {
		t.Errorf("Publish with a failing manifest write = %v, want the store error", err)
	}

	// A failing artifact write stops plan creation (nothing half-written).
	h2 := newHarness(t)
	eng2 := publish.NewWithClock(&spyStore{Store: h2.store, failSaveArtifact: errors.New("content write refused")}, h2.clock)
	if _, err := eng2.CreatePlan(ctx, h2.runID, publish.CreatePlanRequest{
		ArtifactID:   "article.md",
		ArtifactPath: articlePath,
		Targets: []publish.TargetSpec{
			{ID: "site", Type: publication.TargetTypePage, Provider: "git", Destination: "https://example.com"},
		},
	}, h2.bundle); err == nil || !strings.Contains(err.Error(), "content write refused") {
		t.Errorf("CreatePlan with a failing write = %v, want the store error", err)
	}

	// A failing manifest read stops every plan operation.
	eng3 := publish.NewWithClock(&spyStore{Store: h2.store, failLoadManifest: errors.New("manifest read refused")}, h2.clock)
	if _, err := eng3.CreatePlan(ctx, h2.runID, publish.CreatePlanRequest{
		ArtifactID:   "article.md",
		ArtifactPath: articlePath,
		Targets:      []publish.TargetSpec{{ID: "site"}},
	}, h2.bundle); err == nil || !strings.Contains(err.Error(), "manifest read refused") {
		t.Errorf("CreatePlan with a failing manifest read = %v, want the store error", err)
	}
	if _, err := eng3.Plan(h2.runID, plan.ID); err == nil {
		t.Error("Plan with a failing manifest read must fail")
	}

	// Listing failures: a run with no plans at all (the newest-on-disk
	// resolution path) and the run-directory failure.
	eng4 := publish.NewWithClock(&spyStore{Store: h2.store, failListArtifacts: errors.New("list refused")}, h2.clock)
	if _, err := eng4.LatestPlan(h2.runID); err == nil || !strings.Contains(err.Error(), "list refused") {
		t.Errorf("LatestPlan with a failing list = %v, want the store error", err)
	}
	if _, err := eng4.ListPlanIDs(h2.runID); err == nil || !strings.Contains(err.Error(), "list refused") {
		t.Errorf("ListPlanIDs with a failing list = %v, want the store error", err)
	}
	if ok, err := eng4.HasPlans(h2.runID); err == nil || ok {
		t.Errorf("HasPlans with a failing list = %v, %v; want the store error", ok, err)
	}
}

// TestPublishRefusedAfterRebaseline: a re-baseline invalidates the approval
// and reverts the plan; publishing the rebaselined content without a fresh
// approval fails closed, and a fresh approval unblocks it.
func TestPublishRefusedAfterRebaseline(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	plan := h.mustCreatePlan(t, articlePath,
		publish.TargetSpec{ID: "site", Type: publication.TargetTypePage, Provider: "git", Destination: "https://example.com"},
	)
	h.mustApprove(t, plan.ID)

	// The artifact changes: the re-baseline invalidates the approval.
	if _, err := h.eng.RefreshArtifact(ctx, h.runID, plan.ID, []byte("revised article"), h.bundle); err != nil {
		t.Fatalf("RefreshArtifact: %v", err)
	}

	_, err := h.eng.Publish(ctx, h.runID, sitePubs(plan), publish.Options{})
	if !errors.Is(err, publish.ErrNotApproved) {
		t.Fatalf("Publish after a re-baseline = %v, want ErrNotApproved (the old approval no longer matches)", err)
	}

	// A fresh approval on the new artifact unblocks publication.
	plan = h.mustApprove(t, plan.ID)
	if _, err := h.eng.Publish(ctx, h.runID, sitePubs(plan), publish.Options{}); err != nil {
		t.Fatalf("Publish after re-approval: %v", err)
	}
}

// TestPublishContextCancelled pins the interruption path: a cancelled
// context ends the cycle with the dangling state aborted and persisted,
// and the next Publish resumes to completion.
func TestPublishContextCancelled(t *testing.T) {
	h := newHarness(t)
	plan := h.mustCreatePlan(t, articlePath,
		publish.TargetSpec{ID: "site", Type: publication.TargetTypePage, Provider: "git", Destination: "https://example.com"},
		publish.TargetSpec{ID: "thread", Type: publication.TargetTypePost, Provider: "bluesky", Destination: "dolphins.example"},
	)
	plan = h.mustApprove(t, plan.ID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The site publisher cancels the context while running and proves its
	// commit; the engine must stop before the thread runs.
	receiptFor := func(targetID publication.TargetID) *publication.PublicationReceipt {
		return publication.NewReceipt(
			plan.ID, targetID, "git", "https://example.com", "sha-1",
			publication.DigestOf([]byte(articleContent)), "", "test", nil, baseTime,
		)
	}
	site := publish.PublisherFunc(func(ctx context.Context, content []byte, _ *publication.PublicationReceipt) (*publication.PublicationReceipt, error) {
		cancel()
		return receiptFor("site"), nil
	})
	thread := newFakePublisher(plan.ID, "thread")

	_, err := h.eng.Publish(ctx, h.runID,
		map[publication.TargetID]publish.Publisher{"site": site, "thread": thread},
		publish.Options{})
	if err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("Publish after cancellation = %v, want an interruption error", err)
	}
	if thread.callCount() != 0 {
		t.Fatalf("the thread publisher ran %d time(s) after the cycle was cancelled", thread.callCount())
	}

	// The persisted plan is the resume point: a fresh context completes
	// the cycle, executing only the thread.
	re := h.reloadedEngine()
	reThread := newFakePublisher(plan.ID, "thread")
	final, err := re.Publish(context.Background(), h.runID,
		map[publication.TargetID]publish.Publisher{"site": newFakePublisher(plan.ID, "site"), "thread": reThread},
		publish.Options{})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got := reThread.callCount(); got != 1 {
		t.Fatalf("thread executed %d time(s) on resume, want 1", got)
	}
	if final.Status != publication.StatusSucceeded {
		t.Fatalf("resumed plan status = %s, want succeeded", final.Status)
	}
	if tt := target(t, final, "site"); tt.Status != publication.StatusSucceeded {
		t.Fatalf("site target on resume = %s, want succeeded (never re-executed)", tt.Status)
	}
}

// TestEngineConstructors: both constructors yield a usable engine.
func TestEngineConstructors(t *testing.T) {
	store := newStore(t)
	if e := publish.New(store); e == nil {
		t.Fatal("New returned nil")
	}
	if e := publish.NewWithClock(store, nil); e == nil {
		t.Fatal("NewWithClock(nil clock) returned nil")
	}
}

// TestThreadContentErrors / TestThreadPostsErrors: the canonical form's
// fail-closed edges.
func TestThreadContentErrors(t *testing.T) {
	if _, err := publish.ThreadContent(); err == nil {
		t.Error("ThreadContent with no posts must fail")
	}
	if _, err := publish.ThreadContent(""); err == nil {
		t.Error("ThreadContent with an empty post must fail")
	}
	if _, err := publish.ThreadContent("  \n "); err == nil {
		t.Error("ThreadContent with a whitespace-only post must fail")
	}
	if _, err := publish.ThreadContent("a\x00b"); err == nil {
		t.Error("ThreadContent with a NUL inside a post must fail")
	}
	if _, err := publish.ThreadContent("first", ""); err == nil {
		t.Error("ThreadContent with a trailing empty post must fail")
	}

	// A valid thread round-trips.
	content, err := publish.ThreadContent("first", "second")
	if err != nil {
		t.Fatalf("ThreadContent: %v", err)
	}
	posts, err := publish.ThreadPosts(content)
	if err != nil {
		t.Fatalf("ThreadPosts: %v", err)
	}
	if len(posts) != 2 || posts[0] != "first" || posts[1] != "second" {
		t.Fatalf("round trip = %v", posts)
	}
}

func TestThreadPostsErrors(t *testing.T) {
	if _, err := publish.ThreadPosts(nil); err == nil {
		t.Error("ThreadPosts(nil) must fail")
	}
	if _, err := publish.ThreadPosts([]byte("")); err == nil {
		t.Error("ThreadPosts(\"\") must fail")
	}
	if _, err := publish.ThreadPosts([]byte("a\x00")); err == nil {
		t.Error("ThreadPosts with a trailing NUL must fail")
	}
	if _, err := publish.ThreadPosts([]byte("\x00a")); err == nil {
		t.Error("ThreadPosts with a leading NUL must fail")
	}
	if _, err := publish.ThreadPosts([]byte("a\x00\x00b")); err == nil {
		t.Error("ThreadPosts with adjacent NULs must fail")
	}
}

// TestGateReadyBranches: the reason a human can act on.
func TestGateReadyBranches(t *testing.T) {
	digest := publication.DigestOf([]byte("x"))
	ts := run.NewTimestamp(baseTime)

	passing := publication.GateEvidence{
		RunStatus:      run.RunStatusSucceeded,
		FinalCheck:     "pass",
		ReviewReady:    true,
		ArtifactDigest: digest,
		CheckedAt:      ts,
	}
	if ok, why := publish.GateReady(passing); !ok || why != "" {
		t.Fatalf("GateReady(passing) = %v, %q; want true, \"\"", ok, why)
	}

	// Review not ready: the bundle's reason is the answer.
	notReady := passing
	notReady.ReviewReady = false
	notReady.Note = "output missing: output/article.md"
	if ok, why := publish.GateReady(notReady); ok || why != "output missing: output/article.md" {
		t.Fatalf("GateReady(not ready) = %v, %q", ok, why)
	}

	// Run failed (review ready): the generic reason names the evidence.
	failed := passing
	failed.RunStatus = run.RunStatusFailed
	if ok, why := publish.GateReady(failed); ok || !strings.Contains(why, "final check") {
		t.Fatalf("GateReady(failed run) = %v, %q", ok, why)
	}
}

// TestPlanOperationsFailClosedOnContentErrors: a failing content read must
// stop approval and re-baselining before any state change.
func TestPlanOperationsFailClosedOnContentErrors(t *testing.T) {
	h := newHarness(t)
	plan := h.mustCreatePlan(t, articlePath,
		publish.TargetSpec{ID: "site", Type: publication.TargetTypePage, Provider: "git", Destination: "https://example.com"},
	)
	ctx := context.Background()
	eng := publish.NewWithClock(&spyStore{
		Store:               h.store,
		failLoadArtifactIf:  func(rel string) bool { return rel == articlePath },
		failLoadArtifactErr: os.ErrNotExist,
	}, h.clock)

	if _, err := eng.Approve(ctx, h.runID, plan.ID, "op@example.com", h.bundle); err == nil {
		t.Error("Approve with an unreadable artifact must fail")
	}

	// The read-back of the re-baselined content failing must stop the
	// re-baseline before the plan document is touched.
	eng2 := publish.NewWithClock(&spyStore{
		Store:               h.store,
		failLoadArtifactIf:  func(rel string) bool { return rel == articlePath },
		failLoadArtifactErr: errors.New("read-back refused"),
	}, h.clock)
	if _, err := eng2.RefreshArtifact(ctx, h.runID, plan.ID, []byte("v2"), h.bundle); err == nil || !strings.Contains(err.Error(), "read-back refused") {
		t.Errorf("RefreshArtifact with a failing read-back = %v, want the store error", err)
	}
}

// TestAdapterErrorBranches: the two bridges' fail-closed edges.
func TestAdapterErrorBranches(t *testing.T) {
	// Bluesky: malformed thread content (a trailing NUL) is rejected
	// before any external call.
	fx := newAdapterFake(t)
	pub := fx.newPublisher(t)
	bridge := NewBlueskyPublisherForTest(pub, "pub-20260714T120300Z-1b2c3d4e5f60", "thread")
	if _, err := bridge.Publish(context.Background(), []byte("one\x00"), nil); err == nil {
		t.Fatal("the bluesky adapter must reject malformed thread content")
	}

	// Bluesky: every record write fails → no receipt, the error.
	fx2 := newAdapterFake(t)
	fx2.recordStatus = []int{500, 500}
	pub2 := fx2.newPublisher(t)
	bridge2 := NewBlueskyPublisherForTest(pub2, "pub-20260714T120300Z-1b2c3d4e5f60", "thread")
	content, _ := publish.ThreadContent("a", "b")
	if receipt, err := bridge2.Publish(context.Background(), content, nil); err == nil || receipt != nil {
		t.Fatalf("an all-failed thread must return (nil, err), got (%v, %v)", receipt, err)
	}

	// Git: a dirty working tree (an unexpected file) fails the run — no
	// receipt, the error.
	root := newTestRepo(t)
	gitpubPub, err := gitpub.New(context.Background(), gitpub.Config{
		RepoRoot:       root,
		TargetDir:      ".",
		Branch:         "newsroom/publish",
		CommittedName:  "Newsroom Bot",
		CommittedEmail: "newsroom-bot@example.com",
	})
	if err != nil {
		t.Fatalf("gitpub.New: %v", err)
	}
	defer gitpubPub.Close() //nolint:errcheck
	if err := os.WriteFile(filepath.Join(root, "stray.txt"), []byte("dirty"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitBridge := publish.NewGitPublisher(gitpubPub, "pub-20260714T120300Z-1b2c3d4e5f60", "site",
		"content/a.md", "A", "run-20260714T120000Z-000000000000")
	if receipt, err := gitBridge.Publish(context.Background(), []byte("# A\n"), nil); err == nil || receipt != nil {
		t.Fatalf("a dirty tree must fail the git adapter, got (%v, %v)", receipt, err)
	}
}
