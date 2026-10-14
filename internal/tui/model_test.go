package tui

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// strip removes ANSI style escapes so tests can assert on rendered text.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func strip(s string) string { return ansiRe.ReplaceAllString(s, "") }

// keyNameMsg builds a key message from the friendly names used by keyName.
func keyNameMsg(name string) tea.Msg {
	switch name {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(name)}
}

// apply drives the model through messages, ignoring the returned commands
// (tests inject the messages those commands would produce directly).
func apply(t *testing.T, m Model, msgs ...tea.Msg) Model {
	t.Helper()
	for _, msg := range msgs {
		next, _ := m.Update(msg)
		m = next
	}
	return m
}

// fakeService is a scripted Service for tests.
type fakeService struct {
	mu          sync.Mutex
	info        Info
	infoErr     error
	runs        []RunInfo
	listErr     error
	details     map[string]RunDetail
	detailErrs  map[string]error
	startCalls  []NewRunIntent
	startErr    error
	startID     string
	resumeCalls []string
	resumeErr   error
	retryCalls  [][2]string
	retryErr    error
	cancelCount int
	inFlight    string
	done        chan struct{}
}

func newFakeService() *fakeService {
	return &fakeService{
		details:    map[string]RunDetail{},
		detailErrs: map[string]error{},
		startID:    "run-new",
	}
}

func (f *fakeService) Info() (Info, error) { return f.info, f.infoErr }

func (f *fakeService) ListRuns(_ context.Context) ([]RunInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs, f.listErr
}

func (f *fakeService) RunDetail(_ context.Context, id string) (RunDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, exists := f.detailErrs[id]; exists && err != nil {
		return RunDetail{}, err
	}
	return f.details[id], nil
}

func (f *fakeService) setDetail(id string, d RunDetail) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.details[id] = d
}

func (f *fakeService) StartNewRun(_ context.Context, intent NewRunIntent) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls = append(f.startCalls, intent)
	if f.startErr != nil {
		return "", f.startErr
	}
	return f.startID, nil
}

func (f *fakeService) ResumeRun(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumeCalls = append(f.resumeCalls, id)
	if f.resumeErr != nil {
		return f.resumeErr
	}
	f.inFlight = id
	return nil
}

func (f *fakeService) RetryStage(_ context.Context, id, stage string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retryCalls = append(f.retryCalls, [2]string{id, stage})
	if f.retryErr != nil {
		return f.retryErr
	}
	f.inFlight = id
	return nil
}

func (f *fakeService) Cancel() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelCount++
}

func (f *fakeService) Done() <-chan struct{} { return f.done }

func (f *fakeService) InFlight() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inFlight
}

// newTestModel builds a model sized like a normal terminal.
func newTestModel(svc Service) Model {
	m := New(svc)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	return m
}

// keyR builds a printable (rune) key message.
func keyR(s string) tea.Msg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

// lipglossWidth is a test alias for the terminal width of a line.
func lipglossWidth(s string) int { return lipgloss.Width(s) }

// safeView runs View and converts a panic into an error (a rendering bug
// must fail the test cleanly rather than crash the whole binary).
func safeView(m Model) (out string, err error) {
	defer func() {
		if r := recover(); r != nil {
			out = ""
			err = fmt.Errorf("view panicked: %v", r)
		}
	}()
	out = m.View()
	return
}

// ---------------------------------------------------------------------------
// List view
// ---------------------------------------------------------------------------

func TestListRendersRunsAndNavigates(t *testing.T) {
	svc := newFakeService()
	svc.info = Info{WorkspaceRoot: "/ws"}
	svc.runs = []RunInfo{
		{ID: "run-3", Topic: "third", Status: "failed", UpdatedAt: time.Now()},
		{ID: "run-2", Topic: "second", Status: "running", UpdatedAt: time.Now()},
		{ID: "run-1", Topic: "first", Status: "succeeded", UpdatedAt: time.Now()},
	}
	m := newTestModel(svc)
	m = apply(t, m, listLoadedMsg{runs: svc.runs})

	v := strip(m.View())
	for _, want := range []string{"Recent runs (3)", "run-1", "run-2", "run-3"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
	// The newest run is selected by default (selection cursor present).
	if !strings.Contains(v, "▸") {
		t.Error("expected selection cursor in list view")
	}

	// Down moves the selection to the next (older) run.
	m = apply(t, m, keyNameMsg("down"))
	if m.runSel != 1 {
		t.Fatalf("runSel = %d, want 1", m.runSel)
	}
	if v := strip(m.View()); !strings.Contains(v, "▸") {
		t.Error("cursor missing after navigation")
	}

	// k moves up.
	m = apply(t, m, keyNameMsg("k"))
	if m.runSel != 0 {
		t.Fatalf("k from sel 1 should land on 0, got %d", m.runSel)
	}
	// One more k wraps to the last (oldest) row.
	m = apply(t, m, keyNameMsg("k"))
	if m.runSel != 2 {
		t.Fatalf("k should wrap to the last row, got sel %d", m.runSel)
	}
}

func TestListEmptyState(t *testing.T) {
	svc := newFakeService()
	m := newTestModel(svc)
	m = apply(t, m, listLoadedMsg{})
	v := strip(m.View())
	if !strings.Contains(v, "No runs yet") {
		t.Errorf("empty list should show the empty state, got:\n%s", v)
	}
	if !strings.Contains(v, "n to start") {
		t.Error("empty state should point at the new-run key")
	}
}

func TestListErrorState(t *testing.T) {
	svc := newFakeService()
	svc.listErr = errors.New("workspace unreadable")
	m := newTestModel(svc)
	m = apply(t, m, listLoadedMsg{err: errors.New("workspace unreadable")})
	v := strip(m.View())
	if !strings.Contains(v, "workspace unreadable") {
		t.Errorf("error state should show the error, got:\n%s", v)
	}
}

func TestListQuitQuitsWithoutConfirmation(t *testing.T) {
	svc := newFakeService()
	m := newTestModel(svc)
	_, cmd := m.Update(keyNameMsg("q"))
	if cmd == nil {
		t.Fatal("quitting an idle TUI should return the quit command")
	}
}

func TestListOpenRunLoadsDetail(t *testing.T) {
	svc := newFakeService()
	svc.runs = []RunInfo{{ID: "run-1", Topic: "one", Status: "succeeded"}}
	svc.setDetail("run-1", RunDetail{RunInfo: RunInfo{ID: "run-1", Status: "succeeded"}, FinalCheck: "pass"})
	m := newTestModel(svc)
	m = apply(t, m, listLoadedMsg{runs: svc.runs})

	m, cmd := m.Update(keyNameMsg("enter"))
	if cmd == nil {
		t.Fatal("opening a run should issue a detail load")
	}
	// Simulate the async detail load.
	m = apply(t, m, detailLoadedMsg{id: "run-1", detail: svc.details["run-1"]})
	if m.screen != screenRun {
		t.Fatalf("screen = %v, want run view", m.screen)
	}
	if !strings.Contains(strip(m.View()), "run-1") {
		t.Error("run view should show the run id")
	}
}

// ---------------------------------------------------------------------------
// New-run wizard
// ---------------------------------------------------------------------------

func testWizardService() *fakeService {
	svc := newFakeService()
	svc.info = Info{
		WorkspaceRoot:    "/ws",
		LLMConfigured:    true,
		LLMBaseURL:       "http://omlx:8080",
		LLMModel:         "qwen3-30b",
		SearchConfigured: true,
		SearchBaseURL:    "http://raspberrypi:8080",
		OutputFormat:     "thread",
		Profiles:         []ProfileOption{{Spec: "pub/javi"}, {Spec: "javi"}},
		ProfilesDefault:  "pub/javi",
	}
	return svc
}

func TestWizardPrefillsFromInfo(t *testing.T) {
	svc := testWizardService()
	m := newTestModel(svc)
	m = apply(t, m,
		infoMsg{info: svc.info},
		keyNameMsg("n"),
	)
	w := m.wiz
	if w.profile.current().label != "pub/javi" {
		t.Errorf("default profile not preselected: %q", w.profile.current().label)
	}
	if w.format.current().label != "thread" {
		t.Errorf("default format not preselected: %q", w.format.current().label)
	}
	if w.mode.current().label != "auto (search)" {
		t.Errorf("default mode not preselected: %q", w.mode.current().label)
	}
}

func TestWizardNavigationAndInput(t *testing.T) {
	svc := testWizardService()
	m := newTestModel(svc)
	m = apply(t, m, infoMsg{info: svc.info}, keyNameMsg("n"))

	// Type a topic.
	m = apply(t, m, keyR("hello topic"))
	if m.wiz.topic.buf != "hello topic" {
		t.Fatalf("topic = %q", m.wiz.topic.buf)
	}

	// Tab moves to profile; left/right cycles the selector.
	m = apply(t, m, keyNameMsg("tab"), keyNameMsg("left"))
	if m.wiz.field != 1 {
		t.Fatalf("field = %d, want 1 (profile)", m.wiz.field)
	}
	// Default is "pub/javi" (index 1); left once lands on "(none)".
	if got := m.wiz.profile.current().label; got != "(none)" {
		t.Errorf("after left: %q, want (none)", got)
	}

	// Shift+tab walks backwards and wraps around to the URLs field.
	m = apply(t, m, keyNameMsg("shift+tab"), keyNameMsg("shift+tab"), keyNameMsg("shift+tab"))
	if m.wiz.field != 4 {
		t.Fatalf("field = %d, want 4 (URLs) after wrap", m.wiz.field)
	}
	m = apply(t, m, keyR("https://ex.com/a https://ex.com/b"))
	if got := parseURLs(m.wiz.urls.buf); len(got) != 2 {
		t.Errorf("parseURLs = %v, want 2 urls", got)
	}

	// Enter advances from the last field only when valid; esc returns to the
	// list.
	m = apply(t, m, keyNameMsg("esc"))
	if m.screen != screenList {
		t.Fatalf("esc should return to the list, screen=%v", m.screen)
	}
}

func TestWizardValidationErrors(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(w *wizard)
		want   string
	}{
		{"empty topic", func(w *wizard) {}, "topic is required"},
		{"explicit without urls", func(w *wizard) {
			w.topic.buf = "t"
			for i, o := range w.mode.opts {
				if strings.HasPrefix(o.label, "explicit") {
					w.mode.sel = i
				}
			}
		}, "needs at least one URL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := testWizardService()
			m := newTestModel(svc)
			m = apply(t, m, infoMsg{info: svc.info}, keyNameMsg("n"))
			tc.mutate(&m.wiz)
			// Jump to the last field and press enter.
			m = apply(t, m,
				keyNameMsg("tab"), keyNameMsg("tab"), keyNameMsg("tab"), keyNameMsg("tab"), keyNameMsg("tab"),
				keyNameMsg("enter"))
			if !strings.Contains(m.wiz.err, tc.want) {
				t.Errorf("wizard error = %q, want it to contain %q", m.wiz.err, tc.want)
			}
			if m.screen != screenNew {
				t.Errorf("invalid intent must not leave the wizard, screen=%v", m.screen)
			}
		})
	}
}

func TestWizardAutoModeRequiresSearch(t *testing.T) {
	svc := testWizardService()
	svc.info.SearchConfigured = false
	m := newTestModel(svc)
	m = apply(t, m, infoMsg{info: svc.info}, keyNameMsg("n"))
	// auto is the first (disabled) mode option; force-select it and start.
	m.wiz.mode.sel = 0
	m.wiz.topic.buf = "some topic"
	m = apply(t, m,
		keyNameMsg("tab"), keyNameMsg("tab"), keyNameMsg("tab"), keyNameMsg("tab"), keyNameMsg("tab"),
		keyNameMsg("enter"))
	if !strings.Contains(m.wiz.err, "search") {
		t.Errorf("auto mode without search should explain why, got %q", m.wiz.err)
	}
}

func TestWizardStartRun(t *testing.T) {
	svc := testWizardService()
	m := newTestModel(svc)
	m = apply(t, m, infoMsg{info: svc.info}, keyNameMsg("n"))

	m.wiz.topic.buf = "Miami Dolphins preseason"
	m.wiz.mode.sel = 1 // explicit URLs
	m.wiz.urls.buf = "https://a.example https://b.example"

	// Advance through all fields; enter on the last starts the run.
	var cmd tea.Cmd
	for i := 0; i < len(wizardFields()); i++ {
		m, cmd = m.Update(keyNameMsg("enter"))
	}
	// The start command performs the service call.
	m = apply(t, m, cmd())
	if len(svc.startCalls) != 1 {
		t.Fatalf("StartNewRun called %d times, want 1", len(svc.startCalls))
	}
	intent := svc.startCalls[0]
	if intent.Topic != "Miami Dolphins preseason" {
		t.Errorf("topic = %q", intent.Topic)
	}
	if intent.Profile != "pub/javi" {
		t.Errorf("profile = %q", intent.Profile)
	}
	if intent.Format != "thread" {
		t.Errorf("format = %q", intent.Format)
	}
	if intent.SourceMode != ModeExplicit {
		t.Errorf("source mode = %q, want explicit", intent.SourceMode)
	}
	if len(intent.URLs) != 2 {
		t.Errorf("urls = %v", intent.URLs)
	}

	// The start result switches to the run view and arms the cycle.
	m = apply(t, m, startResultMsg{id: svc.startID})
	if m.screen != screenRun || !m.executing || m.activeID != svc.startID {
		t.Fatalf("after start: screen=%v executing=%v activeID=%q", m.screen, m.executing, m.activeID)
	}
}

func TestWizardStartServiceError(t *testing.T) {
	svc := testWizardService()
	svc.startErr = errors.New("llm endpoint unreachable")
	m := newTestModel(svc)
	m = apply(t, m, infoMsg{info: svc.info}, keyNameMsg("n"))
	m.wiz.topic.buf = "topic"
	m.wiz.mode.sel = 0 // auto (search configured)
	m = apply(t, m, startResultMsg{err: errors.New("llm endpoint unreachable")})
	if !strings.Contains(m.err, "llm endpoint unreachable") {
		t.Errorf("service error should be shown, got %q", m.err)
	}
	if m.screen != screenNew {
		t.Errorf("a failed start must keep the wizard open, screen=%v", m.screen)
	}
}

// ---------------------------------------------------------------------------
// Run view
// ---------------------------------------------------------------------------

func runningDetail(id string) RunDetail {
	return RunDetail{
		RunInfo: RunInfo{ID: id, Topic: "topic X", Status: "running"},
		Stages: []StageInfo{
			{Name: "discover_sources", Status: "succeeded"},
			{Name: "fetch_sources", Status: "running", StartedAt: time.Now().Add(-30 * time.Second)},
			{Name: "research", Status: "pending"},
		},
	}
}

func failedDetail(id string) RunDetail {
	d := runningDetail(id)
	d.Status = "failed"
	d.Reason = "research: LLM request failed"
	d.Stages[1].Status = "succeeded"
	d.Stages[2].Status = "failed"
	d.Stages[2].Error = "context deadline exceeded"
	d.Resumable = true
	d.Errors = []string{"stage research failed"}
	d.Warnings = []string{"1 source skipped: paywall"}
	return d
}

// openFromList is a test helper that mirrors the enter-on-list path.
func openFromList(t *testing.T, svc *fakeService, m Model, id string) Model {
	t.Helper()
	svc.mu.Lock()
	svc.runs = []RunInfo{{ID: id, Status: "running"}}
	svc.mu.Unlock()
	m = apply(t, m, listLoadedMsg{runs: []RunInfo{{ID: id, Status: "running"}}})
	m = apply(t, m, keyNameMsg("enter"))
	return m
}

func TestRunViewShowsStagesAndErrors(t *testing.T) {
	svc := newFakeService()
	svc.setDetail("run-f", failedDetail("run-f"))
	m := newTestModel(svc)
	m = openFromList(t, svc, m, "run-f")
	svc.mu.Lock()
	detail := svc.details["run-f"]
	svc.mu.Unlock()
	m = apply(t, m, detailLoadedMsg{id: "run-f", detail: detail})

	v := strip(m.View())
	for _, want := range []string{"research", "context deadline exceeded", "paywall", "failed"} {
		if !strings.Contains(v, want) {
			t.Errorf("run view missing %q:\n%s", want, v)
		}
	}
	if !strings.Contains(v, "warnings (1)") {
		t.Errorf("warnings header missing:\n%s", v)
	}
}

func TestRunViewProgressPollToTerminal(t *testing.T) {
	svc := newFakeService()
	svc.setDetail("run-1", runningDetail("run-1"))
	m := newTestModel(svc)
	m = openFromList(t, svc, m, "run-1")
	m.executing = true // this session started the run
	svc.mu.Lock()
	detail := svc.details["run-1"]
	svc.mu.Unlock()
	m = apply(t, m, detailLoadedMsg{id: "run-1", detail: detail})

	if m.maybePoll() == nil {
		t.Fatal("a poll must be scheduled while the run is in flight")
	}

	// The run reaches a terminal state; the next poll delivers the new
	// detail, after which polling stops.
	finished := runningDetail("run-1")
	finished.Status = "succeeded"
	finished.FinalCheck = "pass"
	svc.setDetail("run-1", finished)
	m = apply(t, m, refreshMsg{at: time.Now()})
	m = apply(t, m, detailLoadedMsg{id: "run-1", detail: finished})
	if m.maybePoll() != nil {
		t.Error("no poll should be scheduled once the run is terminal")
	}
	// The service cycle ends when its Done channel closes.
	m = apply(t, m, runFinishedMsg{id: "run-1"})
	if m.executing {
		t.Error("executing should clear after the cycle finishes")
	}
	if !strings.Contains(strip(m.View()), "succeeded") {
		t.Error("terminal run should show its final status")
	}
}

func TestRunViewPollsExternalRunningRun(t *testing.T) {
	// A run that is running but was not started by this session must still
	// be polled (so a run started elsewhere shows live progress).
	svc := newFakeService() // InFlight() == ""
	svc.setDetail("run-x", runningDetail("run-x"))
	m := newTestModel(svc)
	m = openFromList(t, svc, m, "run-x")
	svc.mu.Lock()
	detail := svc.details["run-x"]
	svc.mu.Unlock()
	m = apply(t, m, detailLoadedMsg{id: "run-x", detail: detail})
	if m.executing {
		t.Fatal("a run started elsewhere must not count as this session's cycle")
	}
	if m.maybePoll() == nil {
		t.Error("an externally running run must keep being polled")
	}
}

// fakeOpener records artifact opens instead of spawning an editor.
type fakeOpener struct {
	mu     sync.Mutex
	opened []string
}

func (o *fakeOpener) Open(path string) tea.Cmd {
	o.mu.Lock()
	o.opened = append(o.opened, path)
	o.mu.Unlock()
	return func() tea.Msg { return artifactOpenMsg{} }
}

// armRunView puts the model on the run screen observing id.
func armRunView(t *testing.T, svc *fakeService, m Model, id string, executing bool) Model {
	t.Helper()
	m = apply(t, m, keyNameMsg("n")) // to wizard...
	// ...and then straight to the run screen (bypassing the list).
	m.screen = screenRun
	m.activeID = id
	m.executing = executing
	m = apply(t, m, detailLoadedMsg{id: id, detail: svc.details[id]})
	return m
}

// ---------------------------------------------------------------------------
// Cancellation and quit confirmation
// ---------------------------------------------------------------------------

func TestCancelArmsConfirmation(t *testing.T) {
	svc := newFakeService()
	svc.setDetail("run-1", runningDetail("run-1"))
	m := newTestModel(svc)
	m = armRunView(t, svc, m, "run-1", true)

	m = apply(t, m, keyNameMsg("c"))
	if m.confirm == nil {
		t.Fatal("cancel of an executing run must ask first")
	}
	if !strings.Contains(strip(m.View()), "Cancel the running run") {
		t.Error("confirmation box should render the prompt")
	}

	// A neutral key keeps the prompt up and does nothing else.
	m = apply(t, m, keyNameMsg("x"))
	if m.confirm == nil {
		t.Fatal("neutral key must keep the confirmation prompt")
	}
	if svc.cancelCount != 0 {
		t.Error("neutral key must not cancel")
	}

	// n dismisses without cancelling.
	m = apply(t, m, keyNameMsg("n"))
	if m.confirm != nil {
		t.Fatal("n must dismiss the prompt")
	}
	if svc.cancelCount != 0 {
		t.Error("n must not cancel the run")
	}

	// Arm again and confirm with y.
	m = apply(t, m, keyNameMsg("c"), keyNameMsg("y"))
	if svc.cancelCount != 1 {
		t.Fatalf("cancelCount = %d, want 1", svc.cancelCount)
	}
	if m.executing {
		t.Error("executing must clear after a confirmed cancel")
	}
}

func TestCancelWhenNothingExecuting(t *testing.T) {
	svc := newFakeService()
	svc.setDetail("run-1", failedDetail("run-1"))
	m := newTestModel(svc)
	m = armRunView(t, svc, m, "run-1", false)
	m = apply(t, m, keyNameMsg("c"))
	if m.confirm != nil {
		t.Error("c with nothing executing must not open a confirmation")
	}
	if m.status != "nothing to cancel" {
		t.Errorf("status = %q, want 'nothing to cancel'", m.status)
	}
}

func TestQuitWhileRunningAsksAndConfirms(t *testing.T) {
	svc := newFakeService()
	svc.setDetail("run-1", runningDetail("run-1"))
	m := newTestModel(svc)
	m = armRunView(t, svc, m, "run-1", true)

	m = apply(t, m, keyNameMsg("q"))
	if m.confirm == nil || m.confirm.kind != confirmQuitRunning {
		t.Fatal("quitting an executing run must ask for confirmation")
	}
	// Cancel it and quit.
	_, cmd := m.Update(keyNameMsg("y"))
	if cmd == nil {
		t.Fatal("confirmed quit must return the quit command")
	}
	if svc.cancelCount != 1 {
		t.Error("confirmed quit must cancel the in-flight run")
	}
}

func TestQuitWhileRunningDismissed(t *testing.T) {
	svc := newFakeService()
	svc.setDetail("run-1", runningDetail("run-1"))
	m := newTestModel(svc)
	m = armRunView(t, svc, m, "run-1", true)
	m = apply(t, m, keyNameMsg("q"), keyNameMsg("n"))
	if m.confirm != nil {
		t.Fatal("n must dismiss the quit confirmation")
	}
	if svc.cancelCount != 0 {
		t.Error("dismissed quit must not cancel the run")
	}
	if m.screen != screenRun {
		t.Error("dismissed quit must stay on the run view")
	}
}

// ---------------------------------------------------------------------------
// Retry and resume
// ---------------------------------------------------------------------------

func TestRetryStageFromRunView(t *testing.T) {
	svc := newFakeService()
	svc.setDetail("run-f", failedDetail("run-f"))
	m := newTestModel(svc)
	m = armRunView(t, svc, m, "run-f", false)

	// Select the failed stage (index 2) and press t.
	m = apply(t, m, keyNameMsg("j"), keyNameMsg("j"))
	if m.selectedStage() != "research" {
		t.Fatalf("selectedStage = %q, want research", m.selectedStage())
	}
	var cmd tea.Cmd
	m, cmd = m.Update(keyNameMsg("t"))
	// The retry command performs the service call.
	m = apply(t, m, cmd())
	if len(svc.retryCalls) == 0 {
		t.Fatal("the retry command must call the service")
	}
	if len(svc.retryCalls) != 1 || svc.retryCalls[0][1] != "research" {
		t.Fatalf("retry calls = %v, want one retry of research", svc.retryCalls)
	}
	if !m.executing {
		t.Error("a retry must arm the in-flight cycle")
	}
}

func TestRetryWithoutStageSelection(t *testing.T) {
	svc := newFakeService()
	svc.setDetail("run-f", RunDetail{RunInfo: RunInfo{ID: "run-f", Status: "failed"}})
	m := newTestModel(svc)
	m = armRunView(t, svc, m, "run-f", false)
	apply(t, m, keyNameMsg("t"))
	if len(svc.retryCalls) != 0 {
		t.Error("t with no stages must not retry anything")
	}
}

func TestResumeFromRunView(t *testing.T) {
	svc := newFakeService()
	svc.setDetail("run-f", failedDetail("run-f"))
	m := newTestModel(svc)
	m = armRunView(t, svc, m, "run-f", false)

	var cmd tea.Cmd
	m, cmd = m.Update(keyNameMsg("r"))
	// The resume command performs the service call.
	m = apply(t, m, cmd())
	if len(svc.resumeCalls) != 1 || svc.resumeCalls[0] != "run-f" {
		t.Fatalf("resume calls = %v, want run-f", svc.resumeCalls)
	}
	// Simulate the resume cycle starting.
	m = apply(t, m, startResultMsg{id: "run-f"})
	if !m.executing {
		t.Error("resume must arm the in-flight cycle")
	}
}

func TestResumeNotResumableShowsReason(t *testing.T) {
	svc := newFakeService()
	d := failedDetail("run-f")
	d.Resumable = false
	d.ResumeReason = "run already succeeded"
	svc.setDetail("run-f", d)
	m := newTestModel(svc)
	m = armRunView(t, svc, m, "run-f", false)
	m = apply(t, m, keyNameMsg("r"))
	if len(svc.resumeCalls) != 0 {
		t.Error("a non-resumable run must not be resumed")
	}
	if !strings.Contains(m.status, "already succeeded") {
		t.Errorf("status = %q, want the resume reason", m.status)
	}
}

func TestResumeWhileRunningRefused(t *testing.T) {
	svc := newFakeService()
	svc.setDetail("run-1", runningDetail("run-1"))
	m := newTestModel(svc)
	m = armRunView(t, svc, m, "run-1", true)
	m = apply(t, m, keyNameMsg("r"))
	if len(svc.resumeCalls) != 0 {
		t.Error("r on a running run must not resume it")
	}
	if m.status != "run still in progress" {
		t.Errorf("status = %q", m.status)
	}
}

// ---------------------------------------------------------------------------
// Artifacts
// ---------------------------------------------------------------------------

func TestOpenArtifact(t *testing.T) {
	svc := newFakeService()
	d := failedDetail("run-f")
	d.Artifacts = []ArtifactInfo{
		{Kind: "rendered", Path: "/ws/run-f/artifacts/story.md", Size: 1234},
		{Kind: "dossier", Path: "/ws/run-f/artifacts/dossier.md", Size: 4321},
	}
	svc.setDetail("run-f", d)
	m := newTestModel(svc)
	op := &fakeOpener{}
	m.open = op
	m = armRunView(t, svc, m, "run-f", false)

	m, _ = m.Update(keyNameMsg("o"))
	m = apply(t, m, artifactOpenMsg{})
	if len(op.opened) != 1 || op.opened[0] != "/ws/run-f/artifacts/story.md" {
		t.Fatalf("opened = %v", op.opened)
	}
	// Second press cycles to the next artifact.
	m, _ = m.Update(keyNameMsg("o"))
	if len(op.opened) != 2 || op.opened[1] != "/ws/run-f/artifacts/dossier.md" {
		t.Fatalf("opened = %v, want cycling to the second artifact", op.opened)
	}
	if !strings.Contains(strip(m.View()), "story.md") {
		t.Error("artifact list should be visible in the run view")
	}
}

func TestOpenArtifactNoneYet(t *testing.T) {
	svc := newFakeService()
	svc.setDetail("run-1", runningDetail("run-1"))
	m := newTestModel(svc)
	m.open = &fakeOpener{}
	m = armRunView(t, svc, m, "run-1", true)
	m = apply(t, m, keyNameMsg("o"))
	if m.status != "no artifacts yet" {
		t.Errorf("status = %q", m.status)
	}
}

// ---------------------------------------------------------------------------
// Resize behavior
// ---------------------------------------------------------------------------

func TestResizeTruncatesLongLines(t *testing.T) {
	svc := newFakeService()
	long := strings.Repeat("a", 500)
	d := failedDetail("run-f")
	d.Topic = long
	svc.setDetail("run-f", d)
	m := newTestModel(svc)
	m = armRunView(t, svc, m, "run-f", false)

	// Shrink the terminal: the view must fit and truncate gracefully.
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 12})
	v := m.View()
	lines := strings.Split(v, "\n")
	for i, l := range lines {
		// lipgloss pads/wraps; the key property is no line is wildly wider
		// than the terminal.
		if lipglossWidth(l) > 44 {
			t.Fatalf("line %d exceeds the terminal width: %d > 44: %q", i, lipglossWidth(l), l)
		}
	}
	if !strings.Contains(strip(v), "…") {
		t.Error("overlong content should be truncated with an ellipsis")
	}
	// Grow it back.
	m = apply(t, m, tea.WindowSizeMsg{Width: 120, Height: 32})
	if m.width != 120 || m.height != 32 {
		t.Errorf("size = %dx%d", m.width, m.height)
	}
}

func TestViewSurvivesZeroSize(t *testing.T) {
	svc := newFakeService()
	m := newTestModel(svc)
	m.width, m.height = 0, 0
	if _, err := safeView(m); err != nil {
		t.Fatalf("View must not panic on a zero-sized terminal: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Detail load failures
// ---------------------------------------------------------------------------

func TestDetailLoadErrorShowsMessage(t *testing.T) {
	svc := newFakeService()
	m := newTestModel(svc)
	m = apply(t, m, keyNameMsg("enter"))
	m = apply(t, m, detailLoadedMsg{id: "missing", err: errors.New("run not found")})
	if !strings.Contains(m.err, "run not found") {
		t.Errorf("err = %q", m.err)
	}
}
