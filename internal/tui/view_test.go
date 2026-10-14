package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbletea"
)

// ---------------------------------------------------------------------------
// Pure text helpers
// ---------------------------------------------------------------------------

func TestTruncate(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Errorf("short string must pass through: %q", got)
	}
	got := truncate(strings.Repeat("a", 50), 10)
	if lipglossWidth(got) != 10 || !strings.HasSuffix(got, "…") {
		t.Errorf("truncate = %q (width %d), want 10 cells ending with …", got, lipglossWidth(got))
	}
	if got := truncate("abcdefgh", 0); got != "" {
		t.Errorf("zero width must truncate to empty: %q", got)
	}
	// Wide (CJK) characters count as two cells.
	wide := strings.Repeat("中", 10) // 20 cells
	got = truncate(wide, 6)
	if lipglossWidth(got) > 6 {
		t.Errorf("wide truncate = %q (width %d), must fit in 6", got, lipglossWidth(got))
	}
	// Very small widths degrade gracefully (still within the budget).
	if got := truncate(strings.Repeat("a", 10), 3); lipglossWidth(got) > 3 {
		t.Errorf("tiny width = %q (width %d), must fit in 3", got, lipglossWidth(got))
	}
}

func TestWrapWidth(t *testing.T) {
	lines := wrapWidth("aaaa bbbb cccc dddd", 8)
	for _, l := range lines {
		if lipglossWidth(l) > 8 {
			t.Errorf("line wider than the budget: %q", l)
		}
	}
	// No content is lost: comparing rune runs (spaces may sit at break
	// points that did not exist in the source).
	compact := strings.Join(lines, "")
	if strings.ReplaceAll(compact, " ", "") != "aaaabbbbccccdddd" {
		t.Errorf("wrap lost or duplicated content: %q", compact)
	}
	// A single very long word is hard-broken without losing runes.
	lines = wrapWidth(strings.Repeat("x", 30), 10)
	total := 0
	for _, l := range lines {
		total += len([]rune(l))
	}
	if total != 30 {
		t.Errorf("hard break lost content: %v", lines)
	}
}

func TestClipLines(t *testing.T) {
	lines := clipLines([]string{"a", "bbb", "ccccc", "dd"}, 10, 2)
	if got := strings.Count(lines, "\n"); got != 1 {
		t.Errorf("clipLines height: %q (want 2 lines)", lines)
	}
	for _, l := range strings.Split(lines, "\n") {
		if lipglossWidth(l) > 10 {
			t.Errorf("clipLines width: %q", l)
		}
	}
	// Empty height is clamped, not negative.
	if got := clipLines([]string{"x"}, 10, 0); got != "x" {
		t.Errorf("zero height: %q", got)
	}
}

func TestLeftRight(t *testing.T) {
	got := leftRight("left", "right", 50)
	if !strings.Contains(got, "left") || !strings.Contains(got, "right") {
		t.Errorf("leftRight = %q", got)
	}
	if lipglossWidth(got) != 50 {
		t.Errorf("leftRight width = %d, want 50", lipglossWidth(got))
	}
	// Too narrow: keep the left, truncated.
	got = leftRight(strings.Repeat("l", 40), "r", 20)
	if lipglossWidth(got) > 20 {
		t.Errorf("narrow leftRight overflow: %q (%d)", got, lipglossWidth(got))
	}
	if strings.Contains(got, "r") {
		t.Errorf("the right side must drop when there is no room: %q", got)
	}
}

func TestLastNCountDonePadStatus(t *testing.T) {
	if got := lastN([]string{"1", "2", "3", "4"}, 2); strings.Join(got, "") != "34" {
		t.Errorf("lastN = %v", got)
	}
	if got := lastN([]string{"1"}, 3); strings.Join(got, "") != "1" {
		t.Errorf("lastN short = %v", got)
	}
	stages := []StageInfo{{Status: "succeeded"}, {Status: "skipped"}, {Status: "running"}}
	if got := countDone(stages); got != 2 {
		t.Errorf("countDone = %d, want 2", got)
	}
	if got := padStatus("running", 9); lipglossWidth(got) != 9 {
		t.Errorf("padStatus = %q", got)
	}
}

func TestHumanSize(t *testing.T) {
	cases := map[int64]string{
		-1:              "",
		0:               "0 B",
		512:             "512 B",
		2048:            "2.0 KB",
		5 * 1024:        "5.0 KB",
		1126400:         "1.1 MB",
		5 * 1024 * 1024: "5.0 MB",
	}
	for in, want := range cases {
		if got := humanSize(in); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatScore(t *testing.T) {
	if got := formatScore(85.0); got != "85" {
		t.Errorf("formatScore(85.0) = %q", got)
	}
	if got := formatScore(85.4); got != "85.4" {
		t.Errorf("formatScore(85.4) = %q", got)
	}
	if got := formatScore(99.9); got != "99.9" {
		t.Errorf("formatScore(99.9) = %q", got)
	}
}

func TestAgo(t *testing.T) {
	now := time.Now()
	if got := ago(now, now); got != "0s ago" {
		t.Errorf("ago(0) = %q", got)
	}
	if got := ago(now.Add(-5*time.Minute), now); got != "5m ago" {
		t.Errorf("ago(5m) = %q", got)
	}
	if got := ago(now.Add(-2*time.Hour), now); got != "2h ago" {
		t.Errorf("ago(2h) = %q", got)
	}
	if got := ago(now.Add(-72*time.Hour), now); got != now.Add(-72*time.Hour).Format("Jan 2 15:04") {
		t.Errorf("ago(3d) = %q, want the long date", got)
	}
	if got := ago(time.Time{}, now); got != "" {
		t.Errorf("ago(zero) = %q, want empty", got)
	}
}

func TestIndentString(t *testing.T) {
	if got := indentString("line", 4); got != "    line" {
		t.Errorf("indentString = %q", got)
	}
	if got := indentString("line", 0); got != "line" {
		t.Errorf("indentString 0 = %q", got)
	}
}

// ---------------------------------------------------------------------------
// View rendering
// ---------------------------------------------------------------------------

// newViewModel builds a fully sized model for view tests.
func newViewModel(t *testing.T) Model {
	t.Helper()
	m := New(newFakeService())
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	return m
}

func TestViewRenderNew(t *testing.T) {
	m := newViewModel(t)
	m.info = Info{
		WorkspaceRoot:    "/ws",
		LLMConfigured:    true,
		LLMBaseURL:       "http://omlx:8080",
		LLMModel:         "qwen3-30b",
		SearchConfigured: true,
		SearchBaseURL:    "http://raspberrypi:8080",
		OutputFormat:     "thread",
		Profiles:         []ProfileOption{{Spec: "pub/javi"}},
		ProfilesDefault:  "pub/javi",
	}
	m.buildWizardProfile()
	m.screen = screenNew
	m.wiz = m.freshWizard()
	m.wiz.topic.buf = "Miami Dolphins"
	m.wiz.urls.buf = "https://a.example"

	v := strip(m.View())
	for _, want := range []string{
		"http://omlx:8080", "qwen3-30b", "http://raspberrypi:8080",
		"topic", "profile", "format", "source mode", "URLs", "follow-up",
		"Miami Dolphins", "pub/javi", "thread", "auto (search)",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("wizard view missing %q:\n%s", want, v)
		}
	}
}

func TestViewRenderNewUnconfigured(t *testing.T) {
	m := newViewModel(t)
	m.info = Info{} // nothing configured
	m.screen = screenNew
	m.wiz = m.freshWizard()
	v := strip(m.View())
	if !strings.Contains(v, "not configured") {
		t.Errorf("unconfigured endpoints must be shown:\n%s", v)
	}
}

func TestViewRenderHelp(t *testing.T) {
	m := newViewModel(t)
	m.help = true
	v := strip(m.View())
	for _, want := range []string{"Keyboard", "Recent runs", "New run", "Run view", "cancel", "retry"} {
		if !strings.Contains(v, want) {
			t.Errorf("help overlay missing %q:\n%s", want, v)
		}
	}
}

func TestViewRenderSummary(t *testing.T) {
	m := newViewModel(t)
	m.width, m.height = 140, 32
	m = apply(t, m, tea.WindowSizeMsg{Width: 140, Height: 32})
	m.screen = screenRun
	m.detail = RunDetail{
		RunInfo:          RunInfo{Status: "succeeded"},
		SourcesAttempted: 5,
		SourcesFetched:   3,
		Documents:        3,
		ClaimsTotal:      7,
		ClaimsSupported:  6,
		ClaimsUnresolved: 1,
		QualityScore:     func() *float64 { f := 85.0; return &f }(),
		FinalCheck:       "pass",
		Findings:         2,
	}
	v := strip(m.View())
	for _, want := range []string{
		"sources 3/5", "3 documents", "claims 7 (6 supported), 1 unresolved",
		"quality 85", "final check pass (2 findings)",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("summary missing %q:\n%s", want, v)
		}
	}
	// Empty summary renders nothing.
	m.detail = RunDetail{RunInfo: RunInfo{ID: "run-x", Status: "succeeded"}}
	if v := strip(m.View()); strings.Contains(v, "summary:") {
		t.Errorf("no summary line for an empty run:\n%s", v)
	}
}

func TestViewRenderLongVerbose(t *testing.T) {
	m := newViewModel(t)
	m.screen = screenRun
	m.detail = RunDetail{
		RunInfo:  RunInfo{ID: "run-x", Status: "failed"},
		Reason:   "boom",
		Warnings: []string{strings.Repeat("w", 300)},
	}
	// Non-verbose: single truncated line.
	v := strip(m.View())
	for _, line := range strings.Split(v, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "⚠") && lipglossWidth(line) > 100 {
			t.Errorf("non-verbose warning must stay on one line: %d cells", lipglossWidth(line))
		}
	}
	// Verbose: wrapped.
	m.verbose = true
	if v := strip(m.View()); strings.Count(v, "www") < 2 {
		t.Error("verbose mode should wrap the long warning across lines")
	}
}

func TestViewStatusAndErrorLines(t *testing.T) {
	m := newViewModel(t)
	m.status = "run started"
	m.err = "workspace unreadable"
	v := strip(m.View())
	// The error line wins over the status line.
	if !strings.Contains(v, "workspace unreadable") {
		t.Errorf("error line missing:\n%s", v)
	}
	if strings.Contains(v, "run started") {
		t.Errorf("status line must yield to the error line:\n%s", v)
	}
}

func TestViewKeyLinesPerScreen(t *testing.T) {
	m := newViewModel(t)
	if v := strip(m.View()); !strings.Contains(v, "q quit") {
		t.Errorf("list key line missing quit hint:\n%s", v)
	}
	m.screen = screenRun
	m.executing = true
	if v := strip(m.View()); !strings.Contains(v, "c cancel") {
		t.Errorf("running key line missing cancel hint:\n%s", v)
	}
	m.executing = false
	if v := strip(m.View()); !strings.Contains(v, "o open artifact") {
		t.Errorf("terminal key line missing artifact hint:\n%s", v)
	}
}

// ---------------------------------------------------------------------------
// Entry (text cursor) behavior
// ---------------------------------------------------------------------------

func TestEntryCursorEditing(t *testing.T) {
	e := entry{bound: 10}
	e.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("abc")})
	if e.buf != "abc" || e.cur != 3 {
		t.Fatalf("after typing: buf=%q cur=%d", e.buf, e.cur)
	}
	// Left twice, then insert mid-string: "abc" -> "aXYbc", cursor after the
	// inserted text.
	e.key(tea.KeyMsg{Type: tea.KeyLeft})
	e.key(tea.KeyMsg{Type: tea.KeyLeft})
	e.insert("XY")
	if e.buf != "aXYbc" || e.cur != 3 {
		t.Fatalf("insert: buf=%q cur=%d, want aXYbc/3", e.buf, e.cur)
	}
	// Backspace removes the character before the cursor.
	e.key(tea.KeyMsg{Type: tea.KeyBackspace})
	if e.buf != "aXbc" || e.cur != 2 {
		t.Errorf("backspace: %q/%d, want aXbc/2", e.buf, e.cur)
	}
	// Delete removes the character at the cursor.
	e.key(tea.KeyMsg{Type: tea.KeyDelete})
	if e.buf != "aXc" {
		t.Errorf("delete: %q, want aXc", e.buf)
	}
	// Home/end.
	e.key(tea.KeyMsg{Type: tea.KeyHome})
	if e.cur != 0 {
		t.Errorf("home: cur=%d", e.cur)
	}
	e.key(tea.KeyMsg{Type: tea.KeyEnd})
	if e.cur != 3 {
		t.Errorf("end: cur=%d", e.cur)
	}
	// Non-printable runes are ignored.
	e.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{0x1b}})
	if e.buf != "aXc" {
		t.Errorf("control rune must be ignored: %q, want aXc", e.buf)
	}
}

func TestEntryBound(t *testing.T) {
	e := entry{bound: 3}
	e.append('a')
	e.append('b')
	e.append('c')
	e.append('d') // at the bound: refused
	if e.buf != "abc" {
		t.Errorf("bound: %q, want abc (appends at the bound are refused)", e.buf)
	}
	// An unbounded entry grows freely.
	f := entry{}
	f.insert(strings.Repeat("z", 100))
	if f.width() != 100 {
		t.Errorf("unbounded width = %d", f.width())
	}
}

func TestSelectorMoveAndCurrent(t *testing.T) {
	s := &selector{opts: []option{{label: "a", enabled: true}, {label: "b", enabled: true}, {label: "c", enabled: true}}, sel: 1}
	s.move(1)
	if s.current().label != "c" {
		t.Errorf("move(1): %q", s.current().label)
	}
	s.move(1) // wraps
	if s.current().label != "a" {
		t.Errorf("wrap: %q", s.current().label)
	}
	s.move(-1) // wraps backwards
	if s.current().label != "c" {
		t.Errorf("backwards wrap: %q", s.current().label)
	}
	// move skips disabled options.
	d := &selector{opts: []option{{label: "a", enabled: true}, {label: "b"}, {label: "c", enabled: true}}, sel: 0}
	d.move(1)
	if d.current().label != "c" {
		t.Errorf("move must skip the disabled option: %q", d.current().label)
	}
	// Empty selector is inert.
	empty := &selector{}
	empty.move(1)
	if empty.current().label != "" {
		t.Errorf("empty selector: %q", empty.current().label)
	}
}

// ---------------------------------------------------------------------------
// Commands
// ---------------------------------------------------------------------------

func TestInitCommandsProduceMessages(t *testing.T) {
	svc := newFakeService()
	svc.info = Info{WorkspaceRoot: "/ws"}
	svc.runs = []RunInfo{{ID: "run-1", Status: "succeeded"}}
	m := New(svc)
	// Init batches loadInfo + refreshList; v1 delivers the batch as a
	// BatchMsg whose children are run one at a time.
	var delivered []tea.Msg
	switch msg := m.Init()().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			if c != nil {
				delivered = append(delivered, c())
			}
		}
	default:
		delivered = append(delivered, m.Init()())
	}
	for _, d := range delivered {
		m = apply(t, m, d)
	}
	if m.info.WorkspaceRoot != "/ws" {
		t.Errorf("info = %+v, want the service info", m.info)
	}
	if len(m.runs) != 1 {
		t.Errorf("runs = %v, want the one from the fake", m.runs)
	}
}

func TestLoadInfoCmdError(t *testing.T) {
	svc := newFakeService()
	svc.infoErr = fmt.Errorf("config broken")
	m := New(svc)
	m = apply(t, m, loadInfoCmd(svc)())
	if !strings.Contains(m.err, "config broken") {
		t.Errorf("err = %q", m.err)
	}
}

func TestFinishRunMessage(t *testing.T) {
	svc := newFakeService()
	done := make(chan struct{})
	close(done)
	svc.done = done
	m := New(svc)
	m = apply(t, m, finishRunForTest(m, "run-1")())
	// A finished cycle must not keep polling.
	if m.maybePoll() != nil {
		t.Error("finished cycle must not schedule a poll")
	}
}

func TestClearScreenCmd(t *testing.T) {
	msg := clearScreenCmd()()
	if msg == nil {
		t.Fatal("clearScreenCmd must produce a message")
	}
}

// finishRunForTest is a test alias (finishRun is a method with a value
// receiver, so it is called directly in tests through this wrapper).
func finishRunForTest(m Model, id string) tea.Cmd { return m.finishRun(id) }
