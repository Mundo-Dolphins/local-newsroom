package tui

// Tests for the v0.6 controlled-publication screen (issue #122).
//
// A fakePublicationService scripts the PublicationService surface; the
// tests cover the full journey (non-ready run, preview, approve, progress,
// partial failure, resume, cancel) plus terminal resize and navigation.
// Nothing here performs publication work — that is the fake's scripted
// state, mirroring the app in cmd/newsroom.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/bubbletea"
)

// fakePublicationService implements PublicationService on top of a scripted
// fakeService: it records every intent and returns the configured state.
type fakePublicationService struct {
	*fakeService
	mu           sync.Mutex
	state        *PublicationState
	stateErr     error
	stateOpts    []PublicationOptions
	approveCalls []string
	approveErr   error
	resumeCalls  []string
	resumeErr    error
	cancelCount  int
	inFlight     string
	pubDone      chan struct{}
}

func newFakePublicationService() *fakePublicationService {
	return &fakePublicationService{
		fakeService: newFakeService(),
		pubDone:     make(chan struct{}),
	}
}

func (f *fakePublicationService) setState(st *PublicationState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = st
}

func (f *fakePublicationService) PublicationState(_ context.Context, runID string, opts PublicationOptions) (*PublicationState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stateOpts = append(f.stateOpts, opts)
	if f.stateErr != nil {
		return nil, f.stateErr
	}
	if f.state != nil && f.state.RunID != runID {
		return nil, errors.New("unknown run")
	}
	return f.state, nil
}

func (f *fakePublicationService) ApprovePublication(_ context.Context, runID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.approveCalls = append(f.approveCalls, runID)
	if f.approveErr != nil {
		return f.approveErr
	}
	f.inFlight = runID // the cycle is now in flight
	return nil
}

func (f *fakePublicationService) ResumePublication(_ context.Context, runID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumeCalls = append(f.resumeCalls, runID)
	if f.resumeErr != nil {
		return f.resumeErr
	}
	f.inFlight = runID
	return nil
}

func (f *fakePublicationService) CancelPublication() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelCount++
	f.inFlight = ""
}

func (f *fakePublicationService) PublicationDone() <-chan struct{} {
	return f.pubDone
}

func (f *fakePublicationService) PublicationInFlight() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inFlight
}

// pubStateFixture is a realistic projection: a ready run with the editorial
// preset, two destination plans (Hugo planned + previewed, Bluesky failed
// with remediation), one approval on record, and a partial failure.
func pubStateFixture() *PublicationState {
	previewOK := true
	previewNo := false
	return &PublicationState{
		RunID:          "run-20260721-001",
		Topic:          "Dolphins preseason report",
		RunStatus:      "succeeded",
		Ready:          true,
		FinalCheck:     "pass",
		Findings:       1,
		PresetName:     "pub/javi",
		PresetHash:     "hashhashhashhashhash",
		Approvable:     true,
		Resumable:      true,
		Interrupted:    false,
		ApproveBlocker: "",
		ResumeBlocker:  "",
		Plans: []PublicationPlan{
			{
				PlanID:         "20260721-0001-a1",
				Status:         "planned",
				Kind:           "new plan",
				Key:            "hugo",
				Destination:    "https://dolphins.example/2026/07/21/dolphins-preseason.html",
				ArtifactPath:   "publication/content/hugo.html",
				ArtifactDigest: "aaaabbbbccccddddeeeeffff0000111122223333444455556666777788889999aaaa",
				ArtifactSize:   48211,
				Facts:          map[string]string{"branch": "main", "path": "content/posts"},
				Targets: []PublicationTarget{
					{
						ID:             "hugo-site",
						Name:           "Hugo site",
						Type:           "git",
						Provider:       "git",
						Destination:    "https://dolphins.example",
						Status:         "planned",
						PreviewOK:      &previewOK,
						PreviewDetails: "page rendered (2 posts)",
					},
				},
			},
			{
				PlanID:         "20260721-0002-b2",
				Status:         "failed",
				Kind:           "reused from a previous preview",
				Key:            "bluesky",
				Destination:    "@dolphins",
				ArtifactPath:   "publication/content/bluesky.json",
				ArtifactDigest: "bbbbccccddeeffff0000111122223333444455556666777788889999aaaabbbb",
				ArtifactSize:   9123,
				Facts:          map[string]string{"handle": "@dolphins", "posts": "4"},
				ApprovalBy:     "javi",
				ApprovalMethod: "tui",
				ApprovalStatus: "granted",
				Targets: []PublicationTarget{
					{
						ID:             "bluesky-thread",
						Name:           "Bluesky thread",
						Type:           "bluesky",
						Provider:       "bluesky",
						Destination:    "@dolphins",
						Status:         "failed",
						PreviewOK:      &previewNo,
						PreviewDetails: "4 posts staged",
						PreviewIssues:  []string{"rate limit budget exceeded (simulated)"},
						Error:          "bluesky: post 3 rejected (simulated rate limit)",
						Attempts:       2,
					},
				},
				Remediation: []PublicationRemediation{
					{
						Kind:     "dry_run_issue",
						TargetID: "bluesky-thread",
						Action:   "wait for the rate limit budget to reset",
						Detail:   "or raise the preset's interval",
					},
				},
			},
		},
	}
}

// newPubModel builds a publication-enabled model sized like a normal
// terminal.
func newPubModel(svc *fakePublicationService) Model {
	m := NewWithPublication(svc, svc)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	return m
}

// armPubPut puts the model on the publication screen observing the
// fixture run with a loaded state (the entry projection is simulated).
func armPubPut(t *testing.T, svc *fakePublicationService, m Model) Model {
	t.Helper()
	svc.setState(pubStateFixture())
	m, _ = m.gotoPublish("run-20260721-001")
	// Drive the entry load command directly (batch expansion is the
	// program's job, not the model's).
	m = apply(t, m, m.loadPublicationState("run-20260721-001", PublicationOptions{Prepare: true, Preview: true})())
	if m.screen != screenPublish {
		t.Fatalf("screen = %v, want publish", m.screen)
	}
	return m
}

// ---------------------------------------------------------------------------
// Entry from the run view
// ---------------------------------------------------------------------------

func TestPublishEntryFromTerminalRun(t *testing.T) {
	svc := newFakePublicationService()
	svc.setDetail("run-1", RunDetail{
		RunInfo:           RunInfo{ID: "run-1", Topic: "t", Status: "succeeded"},
		PublicationStatus: "none",
	})
	m := newPubModel(svc)
	m = apply(t, m,
		listLoadedMsg{runs: []RunInfo{{ID: "run-1", Status: "succeeded"}}},
		keyNameMsg("enter"),
		detailLoadedMsg{id: "run-1", detail: svc.details["run-1"]})

	// p asks the service to prepare + preview the plans.
	m, _ = m.Update(keyR("p"))
	if m.screen != screenPublish {
		t.Fatalf("p should enter the publication screen, got %v", m.screen)
	}
	// Drive the entry load directly (batch expansion is the program's job).
	m = apply(t, m, m.loadPublicationState("run-1", PublicationOptions{Prepare: true, Preview: true})())
	svc.mu.Lock()
	if len(svc.stateOpts) != 1 || !svc.stateOpts[0].Prepare || !svc.stateOpts[0].Preview {
		t.Errorf("entry projection opts = %+v, want {Prepare:true Preview:true}", svc.stateOpts)
	}
	svc.mu.Unlock()

	// Deliver the projection.
	m = apply(t, m, pubLoadedMsg{runID: "run-1", state: pubStateFixture()})
	if m.screen != screenPublish {
		t.Fatalf("after load: screen = %v", m.screen)
	}
	v := strip(m.View())
	for _, want := range []string{
		"publication", "ready for publication", "pub/javi", "final check pass",
		"hugo", "bluesky", "Hugo site", "Bluesky thread",
		"digest", "preview", "approval", "fix",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("publish view missing %q:\n%s", want, v)
		}
	}
	// The run view key line offers p for terminal runs.
	m = apply(t, m, keyNameMsg("esc"))
	if m.screen != screenRun {
		t.Fatalf("esc should return to the run view, got %v", m.screen)
	}
	if !strings.Contains(strip(m.View()), "p publish") {
		t.Error("run view key line should offer p publish")
	}
}

func TestPublishEntryRefusedWhileExecuting(t *testing.T) {
	svc := newFakePublicationService()
	svc.setDetail("run-1", runningDetail("run-1"))
	m := newPubModel(svc)
	m = armRunView(t, svc.fakeService, m, "run-1", true)
	m = apply(t, m, keyR("p"))
	if m.screen != screenRun {
		t.Fatalf("p on an executing run must not enter the publish screen, got %v", m.screen)
	}
	if !strings.Contains(m.status, "finish before") {
		t.Errorf("status = %q", m.status)
	}
	if len(svc.stateOpts) != 0 {
		t.Error("no projection must be requested for a running run")
	}
}

func TestPublishEntryWithoutPublicationService(t *testing.T) {
	svc := newFakeService()
	svc.setDetail("run-1", RunDetail{RunInfo: RunInfo{ID: "run-1", Status: "succeeded"}})
	m := New(svc)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	m = apply(t, m,
		listLoadedMsg{runs: []RunInfo{{ID: "run-1", Status: "succeeded"}}},
		keyNameMsg("enter"),
		detailLoadedMsg{id: "run-1", detail: svc.details["run-1"]})
	m = apply(t, m, keyR("p"))
	if m.screen != screenRun {
		t.Fatalf("without a publication service the screen must stay run, got %v", m.screen)
	}
	if !strings.Contains(m.status, "not available") {
		t.Errorf("status = %q", m.status)
	}
}

func TestPublishProjectionErrorShown(t *testing.T) {
	svc := newFakePublicationService()
	m := newPubModel(svc)
	m, _ = m.gotoPublish("run-1")
	m = apply(t, m, pubLoadedMsg{runID: "run-1", err: errors.New("publication preset unreadable")})
	if !strings.Contains(strip(m.View()), "publication preset unreadable") {
		t.Errorf("projection error must be shown, got:\n%s", strip(m.View()))
	}
	if m.pubState != nil {
		t.Error("an errored projection must not leave a stale state behind")
	}
}

// ---------------------------------------------------------------------------
// Approve & publish
// ---------------------------------------------------------------------------

func TestPublishApproveRequiresExplicitConfirmation(t *testing.T) {
	svc := newFakePublicationService()
	m := newPubModel(svc)
	m = armPubPut(t, svc, m)

	// a asks first — nothing is called.
	m = apply(t, m, keyR("a"))
	if m.confirm == nil || m.confirm.kind != confirmApprovePub {
		t.Fatalf("a must arm the approval confirmation, got %+v", m.confirm)
	}
	if !strings.Contains(strip(m.View()), "Approve and publish") {
		t.Error("confirmation box must state the action")
	}
	if !strings.Contains(strip(m.View()), "y") {
		t.Error("confirmation box must show the confirm key")
	}
	if len(svc.approveCalls) != 0 {
		t.Fatal("no approval may be recorded before the user confirms")
	}

	// A neutral key keeps the prompt.
	m = apply(t, m, keyR("x"))
	if m.confirm == nil {
		t.Fatal("a neutral key must keep the confirmation up")
	}

	// n dismisses it.
	m = apply(t, m, keyR("n"))
	if m.confirm != nil {
		t.Fatal("n must dismiss the confirmation")
	}
	if len(svc.approveCalls) != 0 {
		t.Fatal("a dismissed approval must not approve anything")
	}
	if !strings.Contains(m.status, "dismissed") {
		t.Errorf("status = %q", m.status)
	}
}

func TestPublishApproveFlowToCompletion(t *testing.T) {
	svc := newFakePublicationService()
	m := newPubModel(svc)
	m = armPubPut(t, svc, m)

	m = apply(t, m, keyR("a"))
	var cmd tea.Cmd
	m, cmd = m.Update(keyR("y"))
	m = apply(t, m, cmd()) // the approve intent
	if len(svc.approveCalls) != 1 || svc.approveCalls[0] != "run-20260721-001" {
		t.Fatalf("approve calls = %v", svc.approveCalls)
	}
	// The cycle is now in flight.
	svc.mu.Lock()
	finished := svc.pubDone
	svc.mu.Unlock()
	close(finished)
	m = apply(t, m, pubStartedMsg{runID: "run-20260721-001"})
	if m.pubPhase != pubRunning {
		t.Fatalf("phase = %v, want running", m.pubPhase)
	}
	if m.maybePollPub() == nil {
		t.Error("an in-flight cycle must keep the screen refreshing")
	}
	// The user closes the cycle; the screen settles on the final state.
	m, cmd = m.Update(pubFinishedMsg{})
	if m.pubPhase != pubDone {
		t.Fatalf("phase = %v after done, want done", m.pubPhase)
	}
	if m.maybePollPub() != nil {
		t.Error("a settled screen must stop refreshing")
	}
	// The done reload re-projects the state (no prepare, no preview).
	m = apply(t, m, cmd())
	svc.mu.Lock()
	last := svc.stateOpts[len(svc.stateOpts)-1]
	svc.mu.Unlock()
	if last.Prepare || last.Preview {
		t.Errorf("done reload opts = %+v, want read-only", last)
	}
}

func TestPublishApproveBlockedShowsReason(t *testing.T) {
	svc := newFakePublicationService()
	m := newPubModel(svc)
	m = armPubPut(t, svc, m)
	st := pubStateFixture()
	st.Approvable = false
	st.ApproveBlocker = "the run is not ready for publication: final check failed"
	m = apply(t, m, pubLoadedMsg{runID: "run-20260721-001", state: st})
	m = apply(t, m, keyR("a"))
	if m.confirm != nil {
		t.Fatal("a blocked approval must not arm a confirmation")
	}
	if !strings.Contains(m.status, "final check failed") {
		t.Errorf("status = %q", m.status)
	}
	if len(svc.approveCalls) != 0 {
		t.Fatal("a blocked approval must not be recorded")
	}
	// The not-ready state also renders its gate verdict.
	v := strip(m.View())
	if !strings.Contains(v, "not ready for publication") {
		t.Errorf("gate verdict missing:\n%s", v)
	}
}

func TestPublishApproveServiceErrorStaysOnScreen(t *testing.T) {
	svc := newFakePublicationService()
	svc.approveErr = errors.New("content changed after the plan was created")
	m := newPubModel(svc)
	m = armPubPut(t, svc, m)
	m = apply(t, m, keyR("a"))
	var cmd tea.Cmd
	m, cmd = m.Update(keyR("y"))
	m = apply(t, m, cmd())
	m = apply(t, m, pubStartedMsg{runID: "run-20260721-001", err: svc.approveErr})
	if m.pubPhase != pubReview {
		t.Fatalf("a refused approval must return to the review phase, got %v", m.pubPhase)
	}
	if !strings.Contains(strip(m.View()), "content changed") {
		t.Errorf("the approval refusal must be visible, got:\n%s", strip(m.View()))
	}
}

// ---------------------------------------------------------------------------
// Resume, cancel, quit, back
// ---------------------------------------------------------------------------

func TestPublishResumeFlow(t *testing.T) {
	svc := newFakePublicationService()
	m := newPubModel(svc)
	m = armPubPut(t, svc, m)

	m = apply(t, m, keyR("r"))
	if m.confirm == nil || m.confirm.kind != confirmResumePub {
		t.Fatalf("r must arm the resume confirmation, got %+v", m.confirm)
	}
	var cmd tea.Cmd
	m, cmd = m.Update(keyR("y"))
	m = apply(t, m, cmd())
	if len(svc.resumeCalls) != 1 {
		t.Fatalf("resume calls = %v", svc.resumeCalls)
	}
	if m.pubPhase != pubRunning {
		t.Fatalf("phase = %v, want running", m.pubPhase)
	}
}

func TestPublishResumeBlockedShowsReason(t *testing.T) {
	svc := newFakePublicationService()
	m := newPubModel(svc)
	m = armPubPut(t, svc, m)
	st := pubStateFixture()
	st.Resumable = false
	st.ResumeBlocker = "plan 20260721-0002-b2 holds no effective approval"
	m = apply(t, m, pubLoadedMsg{runID: "run-20260721-001", state: st})
	m = apply(t, m, keyR("r"))
	if m.confirm != nil {
		t.Fatal("a blocked resume must not arm a confirmation")
	}
	if !strings.Contains(m.status, "no effective approval") {
		t.Errorf("status = %q", m.status)
	}
	if len(svc.resumeCalls) != 0 {
		t.Fatal("a blocked resume must not start a cycle")
	}
}

func TestPublishCancelWhileRunning(t *testing.T) {
	svc := newFakePublicationService()
	m := newPubModel(svc)
	m = armPubPut(t, svc, m)
	svc.mu.Lock()
	finished := svc.pubDone
	svc.mu.Unlock()
	close(finished)
	m = apply(t, m, pubStartedMsg{runID: "run-20260721-001"}) // in flight

	// c asks first.
	m = apply(t, m, keyR("c"))
	if m.confirm == nil || m.confirm.kind != confirmCancelPub {
		t.Fatalf("c must arm the cancel confirmation, got %+v", m.confirm)
	}
	if svc.cancelCount != 0 {
		t.Fatal("the cancel must not run before confirmation")
	}
	// Confirm.
	m = apply(t, m, keyR("y"))
	if svc.cancelCount != 1 {
		t.Fatalf("cancelCount = %d, want 1", svc.cancelCount)
	}
	if !strings.Contains(m.status, "cancelling") {
		t.Errorf("status = %q", m.status)
	}
}

func TestPublishCancelDismissed(t *testing.T) {
	svc := newFakePublicationService()
	m := newPubModel(svc)
	m = armPubPut(t, svc, m)
	svc.mu.Lock()
	finished := svc.pubDone
	svc.mu.Unlock()
	close(finished)
	m = apply(t, m, pubStartedMsg{runID: "run-20260721-001"})
	m = apply(t, m, keyR("c"), keyR("n"))
	if svc.cancelCount != 0 {
		t.Fatal("a dismissed cancel must not cancel")
	}
	if m.confirm != nil {
		t.Fatal("n must dismiss the prompt")
	}
}

func TestPublishQuitWhileRunningAsks(t *testing.T) {
	svc := newFakePublicationService()
	m := newPubModel(svc)
	m = armPubPut(t, svc, m)
	svc.mu.Lock()
	finished := svc.pubDone
	svc.mu.Unlock()
	close(finished)
	m = apply(t, m, pubStartedMsg{runID: "run-20260721-001"})

	m = apply(t, m, keyR("q"))
	if m.confirm == nil || m.confirm.kind != confirmQuitPub {
		t.Fatalf("q while publishing must ask first, got %+v", m.confirm)
	}
	_, cmd := m.Update(keyR("y"))
	if cmd == nil {
		t.Fatal("confirmed quit must return the quit command")
	}
	if svc.cancelCount != 1 {
		t.Error("a confirmed quit must cancel the in-flight publication")
	}
}

func TestPublishQuitIdleQuitsWithoutConfirmation(t *testing.T) {
	svc := newFakePublicationService()
	m := newPubModel(svc)
	m = armPubPut(t, svc, m)
	_, cmd := m.Update(keyR("q"))
	if cmd == nil {
		t.Fatal("q on a settled publish screen must quit without a prompt")
	}
	if svc.cancelCount != 0 {
		t.Error("quitting a settled screen must not cancel anything")
	}
}

func TestPublishBackWhileRunningAsks(t *testing.T) {
	svc := newFakePublicationService()
	m := newPubModel(svc)
	m = armPubPut(t, svc, m)
	svc.mu.Lock()
	finished := svc.pubDone
	svc.mu.Unlock()
	close(finished)
	m = apply(t, m, pubStartedMsg{runID: "run-20260721-001"})

	m = apply(t, m, keyNameMsg("esc"))
	if m.confirm == nil || m.confirm.kind != confirmPubBack {
		t.Fatalf("esc while publishing must ask first, got %+v", m.confirm)
	}
	m = apply(t, m, keyR("y"))
	if m.screen != screenRun {
		t.Fatalf("confirmed back must return to the run view, got %v", m.screen)
	}
	if svc.cancelCount != 1 {
		t.Error("a confirmed back must cancel the in-flight publication")
	}
}

func TestPublishBackIdleGoesDirectly(t *testing.T) {
	svc := newFakePublicationService()
	m := newPubModel(svc)
	m = armPubPut(t, svc, m)
	m = apply(t, m, keyNameMsg("esc"))
	if m.screen != screenRun {
		t.Fatalf("esc on a settled screen must go back without a prompt, got %v", m.screen)
	}
}

// ---------------------------------------------------------------------------
// Navigation and layout
// ---------------------------------------------------------------------------

func TestPublishPlanNavigation(t *testing.T) {
	svc := newFakePublicationService()
	m := newPubModel(svc)
	m = armPubPut(t, svc, m)

	m = apply(t, m, keyNameMsg("j"))
	if m.pubSel != 1 {
		t.Fatalf("j should select plan 1, got %d", m.pubSel)
	}
	// Wrap to the first.
	m = apply(t, m, keyNameMsg("j"))
	if m.pubSel != 0 {
		t.Fatalf("j should wrap to plan 0, got %d", m.pubSel)
	}
	// k wraps to the last.
	m = apply(t, m, keyNameMsg("k"))
	if m.pubSel != 1 {
		t.Fatalf("k should wrap to the last plan, got %d", m.pubSel)
	}
}

func TestPublishViewFitsSmallTerminal(t *testing.T) {
	svc := newFakePublicationService()
	m := newPubModel(svc)
	m = armPubPut(t, svc, m)

	m = apply(t, m, tea.WindowSizeMsg{Width: 50, Height: 14})
	v := m.View()
	lines := strings.Split(v, "\n")
	if len(lines) > 14 {
		t.Fatalf("view has %d lines, want at most the terminal height", len(lines))
	}
	for i, l := range lines {
		if w := lipglossWidth(l); w > 52 {
			t.Fatalf("line %d exceeds the terminal width: %d: %q", i, w, l)
		}
	}
	// Grow it back: everything is visible again.
	m = apply(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if !strings.Contains(strip(m.View()), "rate limit") {
		t.Error("the remediation step should be visible at a larger size")
	}
}

func TestPublishHelpShowsPublicationKeys(t *testing.T) {
	svc := newFakePublicationService()
	m := newPubModel(svc)
	m = armPubPut(t, svc, m)
	m = apply(t, m, keyR("?"))
	if !m.help {
		t.Fatal("? must toggle the help")
	}
	v := strip(m.View())
	if !strings.Contains(v, "approve & publish") {
		t.Errorf("help should document the approve key:\n%s", v)
	}
}

func TestPublishLoadingSpinnerThenContent(t *testing.T) {
	svc := newFakePublicationService()
	m := newPubModel(svc)
	m, _ = m.gotoPublish("run-1")
	if m.maybePollPub() == nil {
		t.Fatal("while the projection loads, the screen must animate")
	}
	if !strings.Contains(strip(m.View()), "loading") {
		t.Error("a loading screen must say it is loading")
	}
	m = apply(t, m, pubLoadedMsg{runID: "run-1", state: pubStateFixture()})
	if m.maybePollPub() != nil {
		t.Error("a settled review screen must not keep polling")
	}
}

func TestRunViewPublicationSummaryLine(t *testing.T) {
	svc := newFakeService()
	d := failedDetail("run-p")
	d.Status = "succeeded"
	d.Reason = ""
	d.PublicationStatus = "partially_succeeded"
	d.PublicationTargets = 2
	d.PublicationSucceeded = 1
	d.PublicationFailed = 1
	svc.setDetail("run-p", d)
	m := newTestModel(svc)
	m = armRunView(t, svc, m, "run-p", false)
	v := strip(m.View())
	if !strings.Contains(v, "publication partially_succeeded") {
		t.Errorf("summary line missing publication status:\n%s", v)
	}
	if !strings.Contains(v, "1/2 targets ok") {
		t.Errorf("summary line missing target counts:\n%s", v)
	}
	if !strings.Contains(v, "1 failed") {
		t.Errorf("summary line missing failed count:\n%s", v)
	}
}
