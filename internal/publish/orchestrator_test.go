package publish_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publish"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// ---------------------------------------------------------------------------
// Fakes with real-world properties.
//
// The fake world accumulates everything a publisher publishes and NEVER
// deletes it — the external-state property the partial-failure, resume,
// and cancellation stories must preserve: a later failure, a crash, or a
// cancelled context never rolls back what is already public.
// ---------------------------------------------------------------------------

// worldRecord is one externally published item in the fake world.
type worldRecord struct {
	target string
	record string
}

// world is the shared fake external state.
type world struct {
	mu      sync.Mutex
	records []worldRecord
}

func newWorld() *world { return &world{} }

// add records one published item.
func (w *world) add(target, record string) {
	w.mu.Lock()
	w.records = append(w.records, worldRecord{target: target, record: record})
	w.mu.Unlock()
}

// recordsFor returns one target's records in publication order.
func (w *world) recordsFor(target string) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for _, r := range w.records {
		if r.target == target {
			out = append(out, r.record)
		}
	}
	return out
}

// all returns every record in publication order.
func (w *world) all() []worldRecord {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]worldRecord(nil), w.records...)
}

// total is the number of records in the world.
func (w *world) total() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.records)
}

// snapshot returns the world's records as a flat list (for asserting that
// nothing published is ever deleted or reordered).
func (w *world) snapshot() []string {
	out := make([]string, 0, w.total())
	for _, r := range w.all() {
		out = append(out, r.target+"/"+r.record)
	}
	return out
}

// worldCall scripts one call of a worldPublisher.
type worldCall struct {
	// maxPosts is how many posts the call may make; -1 means all
	// remaining.
	maxPosts int
	// fail makes the call fail (returning its partial receipt and an
	// error) once it has made maxPosts posts.
	fail bool
}

// worldPublisher is a scriptable Publisher with the properties of the
// real adapters: it publishes one post at a time into the shared world,
// it resumes from a prior receipt (posts the receipt already proves are
// never re-posted), and it implements Previewer (a rehearsal with zero
// side effect) so the approval-screen facts are testable at this level.
type worldPublisher struct {
	planID   publication.PublicationID
	targetID publication.TargetID
	world    *world
	posts    []string

	previewOK   bool
	previewErr  error
	previewMeta map[string]string

	mu     sync.Mutex
	script []worldCall
	calls  int

	// cancelAfter, when > 0 and the call is the first one, triggers the
	// cancellation after that many posts and returns the partial receipt
	// together with the context error — the shape of a real adapter call
	// whose network request dies mid-thread.
	cancelAfter int
	cancel      context.CancelFunc
}

var (
	_ publish.Publisher = (*worldPublisher)(nil)
	_ publish.Previewer = (*worldPublisher)(nil)
)

func newWorldPublisher(w *world, planID publication.PublicationID, targetID publication.TargetID, posts ...string) *worldPublisher {
	return &worldPublisher{
		world:       w,
		planID:      planID,
		targetID:    targetID,
		posts:       posts,
		previewOK:   true,
		previewMeta: map[string]string{"post_count": strconv.Itoa(len(posts))},
	}
}

// withScript appends scripted calls.
func (p *worldPublisher) withScript(calls ...worldCall) *worldPublisher {
	p.mu.Lock()
	p.script = append(p.script, calls...)
	p.mu.Unlock()
	return p
}

// withCancel configures the mid-call cancellation.
func (p *worldPublisher) withCancel(n int, cancel context.CancelFunc) *worldPublisher {
	p.cancelAfter = n
	p.cancel = cancel
	return p
}

func (p *worldPublisher) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// recordFor is the external record ID a post gets in the world.
func (p *worldPublisher) recordFor(i int) string {
	return fmt.Sprintf("rec-%s-%d", p.targetID, i)
}

// buildReceipt builds the receipt for the posts proven so far (in order).
// The digest is the approved content's digest (the receipt proves the
// content); the proven prefix rides in metadata, the way the real
// social adapters carry per-post state.
func (p *worldPublisher) buildReceipt(content []byte, proven int) *publication.PublicationReceipt {
	record := ""
	if proven > 0 {
		record = p.recordFor(proven - 1)
	}
	return publication.NewReceipt(
		p.planID,
		p.targetID,
		"world",
		"https://world.example/"+string(p.targetID),
		record,
		publication.DigestOf(content),
		"",
		"world-publisher/0.6",
		map[string]string{"posts_proven": strconv.Itoa(proven)},
		time.Now().UTC(),
	)
}

// Publish implements publish.Publisher.
func (p *worldPublisher) Publish(ctx context.Context, content []byte, prior *publication.PublicationReceipt) (*publication.PublicationReceipt, error) {
	p.mu.Lock()
	n := p.calls
	p.calls++
	script := worldCall{maxPosts: -1}
	if n < len(p.script) {
		script = p.script[n]
	}
	p.mu.Unlock()

	proven := 0
	if prior != nil {
		if v, err := strconv.Atoi(prior.Metadata["posts_proven"]); err == nil {
			proven = v
		}
	}

	i := proven
	for i < len(p.posts) {
		if p.cancelAfter > 0 && n == 0 && i-proven >= p.cancelAfter {
			p.cancel()
			if i == proven {
				return nil, ctx.Err()
			}
			return p.buildReceipt(content, i), ctx.Err()
		}
		if err := ctx.Err(); err != nil {
			if i == proven {
				return nil, err
			}
			return p.buildReceipt(content, i), err
		}
		if script.maxPosts >= 0 && i-proven >= script.maxPosts {
			break
		}
		p.world.add(string(p.targetID), p.recordFor(i))
		i++
	}

	if i == len(p.posts) && !script.fail {
		return p.buildReceipt(content, i), nil
	}
	if i == proven {
		// Nothing was proven: a clean failure with no receipt to resume
		// from (the shape of a real failure before the first write).
		return nil, fmt.Errorf("the platform failed before publishing anything")
	}
	return p.buildReceipt(content, i), fmt.Errorf("the platform failed after %d of %d posts", i-proven, len(p.posts))
}

// Preview implements publish.Previewer.
func (p *worldPublisher) Preview(_ context.Context, _ []byte, _ *publication.PublicationReceipt) (*publish.Preview, error) {
	p.mu.Lock()
	ok, err, meta := p.previewOK, p.previewErr, p.previewMeta
	p.mu.Unlock()
	if err != nil {
		return nil, err
	}
	issues := []publication.DryRunIssue{}
	if !ok {
		issues = []publication.DryRunIssue{{Code: "fake-blocker", Message: "the rehearsal found a problem"}}
	}
	return &publish.Preview{
		OK:       ok,
		Details:  fmt.Sprintf("the world would receive %d item(s)", len(p.posts)),
		Issues:   issues,
		Metadata: meta,
	}, nil
}

// ---------------------------------------------------------------------------
// Shared fixtures.
// ---------------------------------------------------------------------------

// saveThreadContent persists the canonical three-post thread through the
// engine (publication/content/thread.txt) and returns its run-relative
// path.
func saveThreadContent(t *testing.T, h *harness) string {
	t.Helper()
	content, err := publish.ThreadContent(threadPosts...)
	if err != nil {
		t.Fatalf("ThreadContent: %v", err)
	}
	if _, err := h.eng.SaveContent(h.runID, "thread.txt", content); err != nil {
		t.Fatalf("SaveContent: %v", err)
	}
	return threadContentPath
}

// threadArtifactReq is the two-target plan request the orchestrator tests
// share: the site page, then the thread that depends on it (the ticket's
// dependency chain: the commit stage precedes the social post; the push
// between them is the user's own step, outside the engine).
func threadArtifactReq() publish.CreatePlanRequest {
	return publish.CreatePlanRequest{
		ArtifactPath:    threadContentPath,
		ArtifactID:      "thread",
		ArtifactVersion: "render/v1",
		Targets: []publish.TargetSpec{
			{ID: "site", Name: "Site", Type: publication.TargetTypePage, Provider: "git", Destination: "https://example.com"},
			{ID: "thread", Name: "Thread", Type: publication.TargetTypePost, Provider: "bluesky", Destination: "dolphins.example", DependsOn: []publication.TargetID{"site"}},
		},
	}
}

// independentThreadArtifactReq is the same content with no dependency
// between the two targets.
func independentThreadArtifactReq() publish.CreatePlanRequest {
	req := threadArtifactReq()
	req.Targets[1].DependsOn = nil
	return req
}

// newWorldsFor builds the two fakes bound to the plan and the shared world.
func newWorldsFor(t *testing.T, planID publication.PublicationID) (*world, map[publication.TargetID]publish.Publisher) {
	t.Helper()
	w := newWorld()
	return w, map[publication.TargetID]publish.Publisher{
		"site":   newWorldPublisher(w, planID, "site", "site-deployment"),
		"thread": newWorldPublisher(w, planID, "thread", threadPosts...),
	}
}

// approveViaOrchestrator approves through the orchestrator with a domain
// Approval (the CLI-style default: cli method, no note, full scope).
func approveViaOrchestrator(t *testing.T, orch *publish.Orchestrator, planID publication.PublicationID) {
	t.Helper()
	_, _, err := orch.Approve(context.Background(), planID, publish.Approval{ApprovedBy: "editor@example.com"})
	if err != nil {
		t.Fatalf("orchestrator Approve: %v", err)
	}
}

// wantRemediationKinds maps target ID -> the kinds of the remediation
// steps recorded for it (plan-level steps have an empty target).
func wantRemediationKinds(s *publish.Summary) map[publication.TargetID][]publish.RemediationKind {
	out := make(map[publication.TargetID][]publish.RemediationKind)
	for _, r := range s.Remediation {
		out[r.TargetID] = append(out[r.TargetID], r.Kind)
	}
	return out
}

// hasKind reports whether the remediation of target contains kind.
func hasKind(s *publish.Summary, target publication.TargetID, kind publish.RemediationKind) bool {
	for _, r := range s.Remediation {
		if r.TargetID == target && r.Kind == kind {
			return true
		}
	}
	return false
}

// assertNoDeletion verifies that every record present in before is still
// present (in order) in the world now: nothing published was rolled back.
func assertNoDeletion(t *testing.T, w *world, before []string) {
	t.Helper()
	now := w.snapshot()
	if len(now) < len(before) {
		t.Fatalf("the world shrank from %d to %d records — published content was deleted: %v", len(before), len(now), now)
	}
	for i, b := range before {
		if now[i] != b {
			t.Fatalf("world record %d changed from %q to %q — published content was altered: %v", i, b, now[i], now)
		}
	}
}

// ---------------------------------------------------------------------------
// Scenario: the full pipeline, end to end, from the orchestrator.
// ---------------------------------------------------------------------------

func TestOrchestratorFullPipeline(t *testing.T) {
	h := newHarness(t)
	saveThreadContent(t, h)

	orch := publish.NewOrchestrator(h.eng, h.runID, h.bundle)
	ctx := context.Background()
	req := threadArtifactReq()

	// 1. Ready: the gate passes for the READY run.
	ready, err := orch.Ready(ctx, req)
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if !ready.Ready {
		t.Fatalf("Ready = false for a READY run: %s", ready.Reason)
	}

	// 2. Plan: prepared once, reused on a second prepare.
	plan, err := orch.Prepare(ctx, req)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if plan.Status != publication.StatusPlanned {
		t.Fatalf("new plan status = %s, want planned", plan.Status)
	}
	again, err := orch.Prepare(ctx, req)
	if err != nil || again.ID != plan.ID {
		t.Fatalf("a repeated Prepare must return the same active plan (got %s, err %v)", again.ID, err)
	}
	ids, err := h.eng.ListPlanIDs(h.runID)
	if err != nil || len(ids) != 1 {
		t.Fatalf("ListPlanIDs = %v (err %v), want exactly one plan", ids, err)
	}

	// 3. Preview: the approval screen's data.
	w, pubs := newWorldsFor(t, plan.ID)
	sitePub := pubs["site"].(*worldPublisher)
	sitePub.previewMeta = map[string]string{
		"branch": "newsroom/publish",
		"path":   "content/news/20260714-ai-act.md",
	}
	threadPub := pubs["thread"].(*worldPublisher)
	orch.SetPublishers(pubs)

	approval, err := orch.Preview(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if approval.RunID != h.runID {
		t.Fatalf("summary run %s, want %s", approval.RunID, h.runID)
	}
	if approval.Topic != h.manifest.Topic {
		t.Fatalf("summary topic %q, want %q", approval.Topic, h.manifest.Topic)
	}
	if !approval.Digest.Matches(string(plan.Artifact.Digest)) {
		t.Fatalf("summary digest %s, want the plan's artifact digest %s", approval.Digest, plan.Artifact.Digest)
	}
	if approval.ContentStale || approval.ContentUnavailable {
		t.Fatalf("summary reports stale/unavailable content without any change: stale=%v unavailable=%v", approval.ContentStale, approval.ContentUnavailable)
	}
	if !approval.GateReady {
		t.Fatalf("gate ready = false on the approval screen for a READY run: %s", approval.GateReason)
	}
	if len(approval.Targets) != 2 {
		t.Fatalf("summary has %d targets, want 2", len(approval.Targets))
	}
	siteTS, threadTS := approval.Targets[0], approval.Targets[1]
	if siteTS.Destination != "https://example.com" || threadTS.Destination != "dolphins.example" {
		t.Fatalf("destinations = %q / %q, want the plan's destinations", siteTS.Destination, threadTS.Destination)
	}
	if len(threadTS.DependsOn) != 1 || threadTS.DependsOn[0] != "site" {
		t.Fatalf("thread depends_on = %v, want [site]", threadTS.DependsOn)
	}
	if siteTS.Preview == nil || siteTS.Preview.Metadata["branch"] != "newsroom/publish" || siteTS.Preview.Metadata["path"] != "content/news/20260714-ai-act.md" {
		t.Fatalf("site preview lacks the exact branch/path: %+v", siteTS.Preview)
	}
	if threadTS.Preview == nil || threadTS.Preview.Metadata["post_count"] != "3" {
		t.Fatalf("thread preview lacks the exact post count: %+v", threadTS.Preview)
	}
	if len(approval.Warnings) != 0 {
		t.Fatalf("unexpected warnings on a clean preview: %v", approval.Warnings)
	}
	if len(approval.Approvals) != 0 {
		t.Fatalf("fresh plan shows %d approvals, want none", len(approval.Approvals))
	}
	// The rehearsals are persisted on the plan document (the audit trail).
	reloaded, err := h.reloadedEngine().Plan(h.runID, plan.ID)
	if err != nil {
		t.Fatalf("Plan after preview: %v", err)
	}
	for _, tt := range reloaded.Targets {
		if tt.DryRun == nil || tt.DryRun.Outcome != publication.DryRunOutcomeOK {
			t.Fatalf("target %q: dry run not recorded as ok after preview: %+v", tt.ID, tt.DryRun)
		}
	}

	// 4. Publish before the approval exists: refused, nothing published.
	if _, err := orch.Publish(ctx, plan.ID); !errors.Is(err, publish.ErrNotApproved) {
		t.Fatalf("Publish without approval err = %v, want ErrNotApproved", err)
	}
	if w.total() != 0 {
		t.Fatalf("the world received %d record(s) before any approval: %v", w.total(), w.all())
	}

	// 5. Approval: a domain input from the surface that showed the screen.
	approved, approval2, err := orch.Approve(ctx, plan.ID, publish.Approval{
		ApprovedBy: "editor@example.com",
		Method:     publication.ApprovalMethodTUI,
		Note:       "looks right",
	})
	if err != nil {
		t.Fatalf("orchestrator Approve: %v", err)
	}
	if approved.Status != publication.StatusApproved {
		t.Fatalf("plan status after approval = %s, want approved", approved.Status)
	}
	if len(approval2.Approvals) != 1 || !approval2.Approvals[0].Effective {
		t.Fatalf("post-approval summary approvals = %+v, want one effective", approval2.Approvals)
	}
	if got := approval2.Approvals[0].Approval.ApprovedBy; got != "editor@example.com" {
		t.Fatalf("approval recorded for %q, want editor@example.com", got)
	}

	// 6. Publish: the cycle runs the dependency order, persists receipts,
	//    and reports the final summary.
	summary, err := orch.Publish(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if summary.Status != publication.StatusSucceeded {
		t.Fatalf("summary status = %s, want succeeded", summary.Status)
	}
	if summary.Topic != h.manifest.Topic || summary.PlanID != plan.ID {
		t.Fatalf("summary identity = %s/%s, want %s/%s", summary.Topic, summary.PlanID, h.manifest.Topic, plan.ID)
	}
	if summary.RunID != h.runID {
		t.Fatalf("summary run %s, want %s", summary.RunID, h.runID)
	}
	if !approval.Digest.Matches(string(summary.Artifact.Digest)) {
		t.Fatalf("summary artifact digest %s, want the approved %s", summary.Artifact.Digest, approval.Digest)
	}
	for _, ts := range summary.Targets {
		if ts.Status != publication.StatusSucceeded || ts.Receipt == nil {
			t.Fatalf("target %q: status %s receipt %v, want succeeded with a receipt", ts.ID, ts.Status, ts.Receipt)
		}
	}
	// The exact dependency order in the external world: the site commit
	// first, then the three posts in thread order.
	all := w.all()
	if len(all) != 4 {
		t.Fatalf("world has %d records, want 4: %v", len(all), all)
	}
	wantSeq := []string{"site/rec-site-0", "thread/rec-thread-0", "thread/rec-thread-1", "thread/rec-thread-2"}
	for i, want := range wantSeq {
		got := all[i].target + "/" + all[i].record
		if got != want {
			t.Fatalf("world sequence[%d] = %s, want %s (order: %v)", i, got, want, all)
		}
	}
	// Final summary manual next steps: the local commit needs its
	// user-controlled push; the live thread needs none.
	if !hasKind(summary, "site", publish.RemediationPush) {
		t.Fatalf("succeeded git target lacks its push remediation: %+v", summary.Remediation)
	}
	if hasKind(summary, "thread", publish.RemediationPush) || len(wantRemediationKinds(summary)["thread"]) != 0 {
		t.Fatalf("succeeded social target should have no remediation: %+v", summary.Remediation)
	}

	// 7. Duplicate invocation: publish again → an exact no-op.
	before := w.snapshot()
	callsSite, callsThread := sitePub.callCount(), threadPub.callCount()
	summary2, err := orch.Publish(ctx, plan.ID)
	if err != nil {
		t.Fatalf("repeat Publish: %v", err)
	}
	if summary2.Status != publication.StatusSucceeded {
		t.Fatalf("repeat Publish summary status = %s, want succeeded", summary2.Status)
	}
	if got := w.total(); got != len(before) {
		t.Fatalf("repeat Publish published %d new record(s): %v", got-len(before), w.all())
	}
	assertNoDeletion(t, w, before)
	if got := sitePub.callCount() + threadPub.callCount(); got != callsSite+callsThread {
		t.Fatalf("repeat Publish re-invoked publishers (site %d, thread %d; were %d, %d)",
			sitePub.callCount(), threadPub.callCount(), callsSite, callsThread)
	}

	// 8. Prepare after the plan ended: a new plan — and a new approval is
	//    required before it may publish.
	plan3, err := orch.Prepare(ctx, req)
	if err != nil {
		t.Fatalf("Prepare after terminal plan: %v", err)
	}
	if plan3.ID == plan.ID {
		t.Fatal("a terminal plan must not be re-prepared into itself; re-publication is a new plan")
	}
	if _, err := orch.Publish(ctx, plan3.ID); !errors.Is(err, publish.ErrNotApproved) {
		t.Fatalf("Publish of the fresh (unapproved) plan err = %v, want ErrNotApproved", err)
	}
	assertNoDeletion(t, w, before)
	if ids, _ := h.eng.ListPlanIDs(h.runID); len(ids) != 2 {
		t.Fatalf("plan count = %d, want 2 (the ended plan + the new one)", len(ids))
	}
}

// ---------------------------------------------------------------------------
// Scenario: the pre-publication gate refuses a run whose final check did
// not pass (and a run with no review evidence at all).
// ---------------------------------------------------------------------------

func TestOrchestratorRefusesWithoutFinalCheckPass(t *testing.T) {
	cases := []struct {
		name   string
		status contracts.FactualCheckStatus
	}{
		{"final check failed", contracts.FactualCheckStatusFail},
		{"final check needs review", contracts.FactualCheckStatusReview},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			saveThreadContent(t, h)
			b := *h.bundle
			b.FinalCheckStatus = tc.status

			orch := publish.NewOrchestrator(h.eng, h.runID, &b)
			ctx := context.Background()
			req := threadArtifactReq()

			ready, err := orch.Ready(ctx, req)
			if err != nil {
				t.Fatalf("Ready: %v", err)
			}
			if ready.Ready {
				t.Fatal("Ready = true although the final factual check did not pass")
			}
			if !strings.Contains(ready.Reason, "final factual check") {
				t.Fatalf("not-ready reason %q does not name the failing gate", ready.Reason)
			}

			if _, err := orch.Prepare(ctx, req); !errors.Is(err, publish.ErrGateFailed) {
				t.Fatalf("Prepare err = %v, want ErrGateFailed", err)
			}
			if has, _ := h.eng.HasPlans(h.runID); has {
				t.Fatal("a plan was created for a run that is not READY")
			}
			w := newWorld()
			orch.SetPublishers(map[publication.TargetID]publish.Publisher{
				"site":   newWorldPublisher(w, testPlanID(), "site", "site-deployment"),
				"thread": newWorldPublisher(w, testPlanID(), "thread", threadPosts...),
			})
			if _, err := orch.Publish(ctx, ""); err == nil {
				t.Fatal("Publish without a plan succeeded")
			} else if w.total() != 0 {
				t.Fatalf("the world received records although no plan exists: %v", w.all())
			}
		})
	}

	t.Run("no review evidence at all", func(t *testing.T) {
		h := newHarness(t)
		saveThreadContent(t, h)
		// A nil bundle is treated as empty evidence: not ready, not a crash.
		orch := publish.NewOrchestrator(h.eng, h.runID, nil)
		ready, err := orch.Ready(context.Background(), threadArtifactReq())
		if err != nil {
			t.Fatalf("Ready: %v", err)
		}
		if ready.Ready {
			t.Fatal("Ready = true with no review evidence")
		}
		if _, err := orch.Prepare(context.Background(), threadArtifactReq()); !errors.Is(err, publish.ErrGateFailed) {
			t.Fatalf("Prepare err = %v, want ErrGateFailed", err)
		}
	})
}

// ---------------------------------------------------------------------------
// Scenario: publication without an approval never happens.
// ---------------------------------------------------------------------------

func TestOrchestratorRefusesWithoutApproval(t *testing.T) {
	h := newHarness(t)
	saveThreadContent(t, h)

	orch := publish.NewOrchestrator(h.eng, h.runID, h.bundle)
	ctx := context.Background()
	req := threadArtifactReq()

	plan, err := orch.Prepare(ctx, req)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	w, pubs := newWorldsFor(t, plan.ID)
	orch.SetPublishers(pubs)

	if _, err := orch.Publish(ctx, plan.ID); !errors.Is(err, publish.ErrNotApproved) {
		t.Fatalf("Publish err = %v, want ErrNotApproved", err)
	}
	if w.total() != 0 {
		t.Fatalf("world records = %d before any approval, want 0", w.total())
	}
	// The persisted plan is untouched: still planned, no attempts.
	reloaded, err := h.reloadedEngine().Plan(h.runID, plan.ID)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if reloaded.Status != publication.StatusPlanned {
		t.Fatalf("plan status = %s after a refused publish, want planned", reloaded.Status)
	}
	// The orchestrator's projection of the same state.
	summary, err := orch.Summary(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.Status != publication.StatusPlanned || summary.Interrupted {
		t.Fatalf("summary = %s interrupted=%v, want planned / not interrupted", summary.Status, summary.Interrupted)
	}
}

// ---------------------------------------------------------------------------
// Scenario: an approval that confirms different bytes than the plan shows
// is refused (and an approval the human never gave cannot happen: the
// approver identity is required).
// ---------------------------------------------------------------------------

func TestOrchestratorApprovalBinding(t *testing.T) {
	h := newHarness(t)
	saveThreadContent(t, h)

	orch := publish.NewOrchestrator(h.eng, h.runID, h.bundle)
	ctx := context.Background()
	req := threadArtifactReq()

	plan, err := orch.Prepare(ctx, req)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	w, pubs := newWorldsFor(t, plan.ID)
	orch.SetPublishers(pubs)

	// Wrong digest confirmed: refused before the engine even sees it.
	other := publication.DigestOf([]byte("different bytes than the plan shows"))
	_, _, err = orch.Approve(ctx, plan.ID, publish.Approval{ApprovedBy: "editor@example.com", DigestConfirmed: other})
	if !errors.Is(err, publish.ErrStaleApproval) {
		t.Fatalf("Approve with a mismatched confirmed digest err = %v, want ErrStaleApproval", err)
	}

	// Unknown scope target: refused.
	_, _, err = orch.Approve(ctx, plan.ID, publish.Approval{ApprovedBy: "editor@example.com", Targets: []publication.TargetID{"nope"}})
	if err == nil {
		t.Fatal("Approve with an unknown scope target succeeded, want refusal")
	}

	// No approver identity: refused.
	_, _, err = orch.Approve(ctx, plan.ID, publish.Approval{})
	if err == nil {
		t.Fatal("Approve with an empty approver succeeded, want refusal")
	}

	// Nothing was recorded and nothing can publish.
	reloaded, err := h.reloadedEngine().Plan(h.runID, plan.ID)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(reloaded.Approvals) != 0 {
		t.Fatalf("%d approvals recorded after refused approvals, want 0", len(reloaded.Approvals))
	}
	if _, err := orch.Publish(ctx, plan.ID); !errors.Is(err, publish.ErrNotApproved) {
		t.Fatalf("Publish err = %v, want ErrNotApproved", err)
	}
	if w.total() != 0 {
		t.Fatalf("world records = %d, want 0", w.total())
	}
}

// ---------------------------------------------------------------------------
// Scenario: the artifact changes after the approval was granted — the
// approval screen shows the drift, and the cycle refuses before any
// publisher runs.
// ---------------------------------------------------------------------------

func TestOrchestratorContentChangedAfterApprovalFailsClosed(t *testing.T) {
	h := newHarness(t)
	saveThreadContent(t, h)

	orch := publish.NewOrchestrator(h.eng, h.runID, h.bundle)
	ctx := context.Background()
	req := threadArtifactReq()

	plan, err := orch.Prepare(ctx, req)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	approveViaOrchestrator(t, orch, plan.ID)
	w, pubs := newWorldsFor(t, plan.ID)
	orch.SetPublishers(pubs)

	// The artifact file is modified after the approval.
	changed := []byte("# EU AI Act\n\nA sentence appears after the approval.\n")
	if _, err := h.store.SaveArtifact(h.runID, run.ArtifactRef{
		Kind:       run.ArtifactKindRendered,
		ID:         "thread",
		Path:       threadContentPath,
		ProducedAt: run.NewTimestamp(baseTime.Add(time.Hour)),
	}, changed); err != nil {
		t.Fatalf("SaveArtifact: %v", err)
	}

	// The approval screen shows the drift.
	approval, err := orch.Preview(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if !approval.ContentStale {
		t.Fatal("the approval screen does not flag the stale artifact")
	}
	foundStale := false
	for _, warn := range approval.Warnings {
		if warn.Code == "artifact-stale" {
			foundStale = true
		}
	}
	if !foundStale {
		t.Fatalf("no artifact-stale warning on the approval screen: %v", approval.Warnings)
	}

	// The cycle fails closed before any publisher call.
	summary, err := orch.Publish(ctx, plan.ID)
	if !errors.Is(err, publish.ErrArtifactChanged) {
		t.Fatalf("Publish err = %v, want ErrArtifactChanged", err)
	}
	if w.total() != 0 {
		t.Fatalf("world records = %d for modified content — unapproved bytes must never publish: %v", w.total(), w.all())
	}
	for _, pub := range pubs {
		if pub.(*worldPublisher).callCount() != 0 {
			t.Fatal("a publisher ran for modified content")
		}
	}
	if summary == nil || summary.Status != publication.StatusApproved {
		t.Fatalf("summary after refusal = %+v, want the untouched approved plan", summary)
	}
}

// ---------------------------------------------------------------------------
// Scenario: independent targets — one fails, the other still runs; the
// published part stays public, the failed part is resumable.
// ---------------------------------------------------------------------------

func TestOrchestratorIndependentTargetsPartialFailure(t *testing.T) {
	h := newHarness(t)
	saveThreadContent(t, h)

	orch := publish.NewOrchestrator(h.eng, h.runID, h.bundle)
	ctx := context.Background()
	req := independentThreadArtifactReq()

	plan, err := orch.Prepare(ctx, req)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	approveViaOrchestrator(t, orch, plan.ID)

	w := newWorld()
	site := newWorldPublisher(w, plan.ID, "site", "site-deployment").withScript(worldCall{maxPosts: 0, fail: true})
	thread := newWorldPublisher(w, plan.ID, "thread", threadPosts...)
	orch.SetPublishers(map[publication.TargetID]publish.Publisher{"site": site, "thread": thread})

	summary, err := orch.Publish(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if summary.Status != publication.StatusPartiallySucceeded {
		t.Fatalf("summary status = %s, want partially_succeeded", summary.Status)
	}
	if got := w.recordsFor("thread"); len(got) != 3 {
		t.Fatalf("independent thread target did not run to completion: %v", got)
	}
	if got := w.recordsFor("site"); len(got) != 0 {
		t.Fatalf("the failing site target left records: %v", got)
	}
	// The failed part is resumable; the public part stays public.
	if !hasKind(summary, "site", publish.RemediationRetry) {
		t.Fatalf("failed target lacks a retry remediation: %+v", summary.Remediation)
	}
	for _, ts := range summary.Targets {
		if ts.ID == "thread" && (ts.Status != publication.StatusSucceeded || ts.Receipt == nil) {
			t.Fatalf("succeeded thread in summary: %s / %v", ts.Status, ts.Receipt)
		}
	}
	// Resume: only the failed target runs again; the live thread is
	// untouched.
	before := w.snapshot()
	summary2, err := orch.Publish(ctx, plan.ID)
	if err != nil {
		t.Fatalf("resume Publish: %v", err)
	}
	if summary2.Status != publication.StatusSucceeded {
		t.Fatalf("plan after resume = %s, want succeeded", summary2.Status)
	}
	if site.callCount() != 2 {
		t.Fatalf("failed target was re-executed %d times total, want 2 (one per cycle)", site.callCount())
	}
	if thread.callCount() != 1 {
		t.Fatalf("the already-succeeded thread was re-executed %d times, want 1", thread.callCount())
	}
	assertNoDeletion(t, w, before)
	if got := len(w.recordsFor("thread")); got != 3 {
		t.Fatalf("thread records after resume = %d, want still 3 (no re-posting)", got)
	}
}

// ---------------------------------------------------------------------------
// Scenario: a partially proven thread — the receipt resumes it, the public
// part is never deleted, and nothing is re-posted. Survives a restart.
// ---------------------------------------------------------------------------

func TestOrchestratorPartialThreadResumesAcrossRestart(t *testing.T) {
	h := newHarness(t)
	saveThreadContent(t, h)

	orch := publish.NewOrchestrator(h.eng, h.runID, h.bundle)
	ctx := context.Background()
	req := independentThreadArtifactReq()

	plan, err := orch.Prepare(ctx, req)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	approveViaOrchestrator(t, orch, plan.ID)

	w := newWorld()
	site := newWorldPublisher(w, plan.ID, "site", "site-deployment")
	thread := newWorldPublisher(w, plan.ID, "thread", threadPosts...).
		withScript(worldCall{maxPosts: 2, fail: true}) // the platform dies after post 2 of 3
	orch.SetPublishers(map[publication.TargetID]publish.Publisher{"site": site, "thread": thread})

	summary, err := orch.Publish(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if summary.Status != publication.StatusPartiallySucceeded {
		t.Fatalf("summary status = %s, want partially_succeeded (one target fully up, one two-thirds)", summary.Status)
	}
	if got := w.recordsFor("thread"); len(got) != 2 {
		t.Fatalf("thread records = %v, want 2 (the proven prefix)", got)
	}
	if !hasKind(summary, "thread", publish.RemediationComplete) {
		t.Fatalf("partially public thread lacks a complete remediation: %+v", summary.Remediation)
	}
	for _, r := range summary.Remediation {
		if strings.Contains(strings.ToLower(r.Action+r.Detail), "delete") {
			t.Fatalf("remediation proposes deleting public content: %q", r.Action+r.Detail)
		}
	}
	before := w.snapshot()

	// Restart: a fresh engine and orchestrator over the same store.
	eng2 := h.reloadedEngine()
	orch2 := publish.NewOrchestrator(eng2, h.runID, h.bundle)
	resumeThread := newWorldPublisher(w, plan.ID, "thread", threadPosts...) // completes the remainder
	orch2.SetPublishers(map[publication.TargetID]publish.Publisher{
		"site":   newWorldPublisher(w, plan.ID, "site", "site-deployment"),
		"thread": resumeThread,
	})

	summary2, err := orch2.Publish(context.Background(), plan.ID)
	if err != nil {
		t.Fatalf("resume Publish after restart: %v", err)
	}
	if summary2.Status != publication.StatusSucceeded {
		t.Fatalf("plan after resume = %s, want succeeded", summary2.Status)
	}
	if got := w.recordsFor("thread"); len(got) != 3 {
		t.Fatalf("thread records after resume = %v, want 3", got)
	}
	// Exactly the proven prefix is kept, one new post appended: no
	// duplication, no deletion.
	want := []string{"thread/rec-thread-0", "thread/rec-thread-1", "thread/rec-thread-2"}
	got := w.recordsFor("thread")
	for i, wantRec := range want {
		if got[i] != strings.TrimPrefix(wantRec, "thread/") {
			t.Fatalf("thread record %d = %s, want %s (sequence %v)", i, got[i], strings.TrimPrefix(wantRec, "thread/"), got)
		}
	}
	if resumeThread.callCount() != 1 {
		t.Fatalf("resumed thread executed %d times, want 1", resumeThread.callCount())
	}
	assertNoDeletion(t, w, before)
	// The resume call received the persisted partial receipt as its state.
	final, err := eng2.Plan(h.runID, plan.ID)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	tt, _ := final.Target("thread")
	if tt.Receipt == nil || tt.Receipt.Metadata["posts_proven"] != "3" {
		t.Fatalf("final thread receipt = %+v, want posts_proven 3", tt.Receipt)
	}
}

// ---------------------------------------------------------------------------
// Scenario: a cancelled context mid-cycle — the plan is left resumable,
// the partially proven work is verified (never deleted), and the resume
// completes without duplication.
// ---------------------------------------------------------------------------

func TestOrchestratorCancellationMidCycle(t *testing.T) {
	h := newHarness(t)
	saveThreadContent(t, h)

	orch := publish.NewOrchestrator(h.eng, h.runID, h.bundle)
	req := independentThreadArtifactReq()
	// The thread runs first so its cancellation stops the cycle.
	req.Targets[0], req.Targets[1] = req.Targets[1], req.Targets[0]

	plan, err := orch.Prepare(context.Background(), req)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	approveViaOrchestrator(t, orch, plan.ID)

	w := newWorld()
	thread := newWorldPublisher(w, plan.ID, "thread", threadPosts...).withCancel(1, nil)
	site := newWorldPublisher(w, plan.ID, "site", "site-deployment")
	orch.SetPublishers(map[publication.TargetID]publish.Publisher{"thread": thread, "site": site})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() //nolint:errcheck -- best-effort cleanup
	thread.cancel = cancel

	summary, err := orch.Publish(ctx, plan.ID)
	if !errors.Is(err, publish.ErrInterrupted) {
		t.Fatalf("Publish err = %v, want ErrInterrupted", err)
	}
	if summary == nil {
		t.Fatal("an interrupted Publish must still return the plan's summary")
	}
	if summary.Status != publication.StatusPublishing || !summary.Interrupted {
		t.Fatalf("summary = %s interrupted=%v, want publishing / interrupted", summary.Status, summary.Interrupted)
	}
	// The one post that went up before the cancellation stays up.
	if got := w.recordsFor("thread"); len(got) != 1 {
		t.Fatalf("thread records after cancellation = %v, want exactly the one posted before the cancel", got)
	}
	if got := w.recordsFor("site"); len(got) != 0 {
		t.Fatalf("the never-started site target left records: %v", got)
	}
	// The interrupted cycle's remediation: verify what is out there, keep
	// it, and resume.
	kinds := wantRemediationKinds(summary)
	if len(kinds["thread"]) == 0 || kinds["thread"][0] != publish.RemediationComplete {
		t.Fatalf("interrupted thread remediation = %v, want a complete step (its public part stays)", kinds["thread"])
	}
	for _, r := range summary.Remediation {
		if strings.Contains(strings.ToLower(r.Action+r.Detail), "delete") {
			t.Fatalf("remediation proposes deleting public content: %q", r.Action+r.Detail)
		}
	}
	before := w.snapshot()

	// Resume with a healthy context (a restarted process).
	eng2 := h.reloadedEngine()
	orch2 := publish.NewOrchestrator(eng2, h.runID, h.bundle)
	orch2.SetPublishers(map[publication.TargetID]publish.Publisher{
		"thread": newWorldPublisher(w, plan.ID, "thread", threadPosts...),
		"site":   newWorldPublisher(w, plan.ID, "site", "site-deployment"),
	})
	summary2, err := orch2.Publish(context.Background(), plan.ID)
	if err != nil {
		t.Fatalf("resume Publish: %v", err)
	}
	if summary2.Status != publication.StatusSucceeded {
		t.Fatalf("plan after resume = %s, want succeeded", summary2.Status)
	}
	if got := w.recordsFor("thread"); len(got) != 3 {
		t.Fatalf("thread records after resume = %v, want the proven one plus the two remaining (no duplication)", got)
	}
	assertNoDeletion(t, w, before)
}

// ---------------------------------------------------------------------------
// Scenario: dependency cascade — a failed dependency blocks the dependent
// (which never runs) and the summary points at the fix.
// ---------------------------------------------------------------------------

func TestOrchestratorDependencyCascade(t *testing.T) {
	h := newHarness(t)
	saveThreadContent(t, h)

	orch := publish.NewOrchestrator(h.eng, h.runID, h.bundle)
	ctx := context.Background()
	req := threadArtifactReq()

	plan, err := orch.Prepare(ctx, req)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	approveViaOrchestrator(t, orch, plan.ID)

	w := newWorld()
	site := newWorldPublisher(w, plan.ID, "site", "site-deployment").withScript(worldCall{maxPosts: 0, fail: true})
	thread := newWorldPublisher(w, plan.ID, "thread", threadPosts...)
	orch.SetPublishers(map[publication.TargetID]publish.Publisher{"site": site, "thread": thread})

	summary, err := orch.Publish(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if summary.Status != publication.StatusFailed {
		t.Fatalf("summary status = %s, want failed (nothing succeeded)", summary.Status)
	}
	if thread.callCount() != 0 {
		t.Fatalf("the dependent thread ran %d times although its dependency failed", thread.callCount())
	}
	if w.total() != 0 {
		t.Fatalf("world records = %v although nothing completed", w.all())
	}
	if !hasKind(summary, "site", publish.RemediationRetry) {
		t.Fatalf("failed dependency lacks a retry remediation: %+v", summary.Remediation)
	}
	// The dependent is blocked, and the summary says exactly why.
	for _, ts := range summary.Targets {
		if ts.ID == "thread" {
			if ts.Status != publication.StatusFailed {
				t.Fatalf("dependent thread status = %s, want failed (blocked)", ts.Status)
			}
			if ts.Error == nil || ts.Error.Code != "dependency-incomplete" {
				t.Fatalf("dependent thread error = %+v, want dependency-incomplete", ts.Error)
			}
		}
	}
	foundDepFix := false
	for _, r := range summary.Remediation {
		if r.TargetID == "site" && r.Kind == publish.RemediationRetry && strings.Contains(r.Detail, "dependency") {
			foundDepFix = true
		}
	}
	if !foundDepFix {
		t.Fatalf("no remediation naming the incomplete dependency: %+v", summary.Remediation)
	}
}

// ---------------------------------------------------------------------------
// Scenario: preview facts — a failed rehearsal is surfaced (and persisted),
// a publisher that cannot preview or is unbound is reported, and the human
// decision (approve anyway) is still a human decision.
// ---------------------------------------------------------------------------

func TestOrchestratorPreviewFacts(t *testing.T) {
	h := newHarness(t)
	saveThreadContent(t, h)

	orch := publish.NewOrchestrator(h.eng, h.runID, h.bundle)
	ctx := context.Background()
	req := independentThreadArtifactReq()
	req.Targets = append(req.Targets, publish.TargetSpec{
		ID: "repo", Name: "Repo", Type: publication.TargetTypeRepository, Provider: "git", Destination: "https://git.example.com/site",
	})

	plan, err := orch.Prepare(ctx, req)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	w := newWorld()
	site := newWorldPublisher(w, plan.ID, "site", "site-deployment")
	site.previewOK = false // the rehearsal found a blocker
	thread := newWorldPublisher(w, plan.ID, "thread", threadPosts...)
	orch.SetPublishers(map[publication.TargetID]publish.Publisher{"site": site, "thread": thread}) // repo unbound

	approval, err := orch.Preview(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	var siteTS, threadTS, repoTS *publish.TargetSummary
	for i := range approval.Targets {
		switch approval.Targets[i].ID {
		case "site":
			siteTS = &approval.Targets[i]
		case "thread":
			threadTS = &approval.Targets[i]
		case "repo":
			repoTS = &approval.Targets[i]
		}
	}
	if siteTS == nil || threadTS == nil || repoTS == nil {
		t.Fatalf("summary targets = %+v, want all three", approval.Targets)
	}
	// The failed rehearsal is shown, with its issues.
	if siteTS.Preview == nil || siteTS.Preview.OK {
		t.Fatalf("site preview = %+v, want a failed rehearsal", siteTS.Preview)
	}
	if len(siteTS.Preview.Issues) == 0 {
		t.Fatal("a failed rehearsal must list its issues")
	}
	// The healthy rehearsal is shown too.
	if threadTS.Preview == nil || !threadTS.Preview.OK {
		t.Fatalf("thread preview = %+v, want ok", threadTS.Preview)
	}
	// The unbound publisher is reported, and its screen slot is empty.
	if repoTS.Preview != nil {
		t.Fatalf("unbound target shows a preview: %+v", repoTS.Preview)
	}
	unbound := false
	for _, warn := range approval.Warnings {
		if warn.Code == "no-publisher-bound" && strings.Contains(warn.Message, "repo") {
			unbound = true
		}
	}
	if !unbound {
		t.Fatalf("no unbound-publisher warning for repo: %v", approval.Warnings)
	}

	// The failed rehearsal is persisted on the plan document — and the
	// healthy one, for contrast.
	reloaded, err := h.reloadedEngine().Plan(h.runID, plan.ID)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	threadT, _ := reloaded.Target("thread")
	if threadT.DryRun == nil || threadT.DryRun.Outcome != publication.DryRunOutcomeOK {
		t.Fatalf("thread dry run = %+v, want recorded ok", threadT.DryRun)
	}
	siteT, _ := reloaded.Target("site")
	if siteT.DryRun == nil || siteT.DryRun.Outcome != publication.DryRunOutcomeFailed {
		t.Fatalf("site dry run = %+v, want recorded failed", siteT.DryRun)
	}

	// A failed rehearsal does not mechanically revoke the human's
	// decision: approving is the human's call, with the issues on screen.
	_, _, err = orch.Approve(ctx, plan.ID, publish.Approval{ApprovedBy: "editor@example.com"})
	if err != nil {
		t.Fatalf("Approve after a failed dry run: %v (a failed rehearsal surfaces; it does not silently veto)", err)
	}

	// And a rehearsal that cannot run at all is a warning, not a crash.
	thread.previewErr = errors.New("preview service unreachable")
	approval2, err := orch.Preview(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	unavailable := false
	for _, warn := range approval2.Warnings {
		if warn.Code == "preview-unavailable" && strings.Contains(warn.Message, "thread") {
			unavailable = true
		}
	}
	if !unavailable {
		t.Fatalf("no preview-unavailable warning for thread: %v", approval2.Warnings)
	}
}

// ---------------------------------------------------------------------------
// Scenario: an unbound publisher refuses the cycle before any state change
// or any publisher call.
// ---------------------------------------------------------------------------

func TestOrchestratorUnboundPublisherRefusesBeforeStateChange(t *testing.T) {
	h := newHarness(t)
	saveThreadContent(t, h)

	orch := publish.NewOrchestrator(h.eng, h.runID, h.bundle)
	ctx := context.Background()
	req := threadArtifactReq()

	plan, err := orch.Prepare(ctx, req)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	approveViaOrchestrator(t, orch, plan.ID)

	w := newWorld()
	// Only one of the two executing targets is bound.
	site := newWorldPublisher(w, plan.ID, "site", "site-deployment")
	orch.SetPublishers(map[publication.TargetID]publish.Publisher{"site": site})

	if _, err := orch.Publish(ctx, plan.ID); !errors.Is(err, publish.ErrPublisherNotBound) {
		t.Fatalf("Publish err = %v, want ErrPublisherNotBound", err)
	}
	if site.callCount() != 0 {
		t.Fatal("the bound publisher ran although the cycle must refuse before any state change")
	}
	if w.total() != 0 {
		t.Fatalf("world records = %v", w.all())
	}
	reloaded, err := h.reloadedEngine().Plan(h.runID, plan.ID)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if reloaded.Status != publication.StatusApproved {
		t.Fatalf("plan status = %s after a refused cycle, want still approved", reloaded.Status)
	}
}
