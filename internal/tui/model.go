package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbletea"
)

// screen identifies the active view.
type screen int

const (
	screenList screen = iota
	screenNew
	screenRun
	screenPublish // the controlled-publication review screen (v0.6)
)

// confirmKind classifies a pending confirmation.
type confirmKind int

const (
	confirmQuitRunning confirmKind = iota
	confirmCancel
	// v0.6 publication confirmations (screenPublish).
	confirmApprovePub // approve & publish the actionable plans
	confirmResumePub  // resume a failed / interrupted cycle
	confirmCancelPub  // cancel the in-flight publication cycle
	confirmPubBack    // cancel the cycle and go back to the run view
	confirmQuitPub    // cancel the cycle and quit the app
)

// confirm is a modal yes/no prompt layered over the current screen.
type confirm struct {
	kind   confirmKind
	prompt string
	detail string // optional second line ("" for a single-line prompt)
}

// pubPhase is the phase of the publication screen (screenPublish).
type pubPhase int

const (
	// pubReview shows the plans, previews, and publication state; the
	// approve (a) and resume (r) surface.
	pubReview pubPhase = iota
	// pubConfirm shows the explicit confirmation modal; a cycle starts
	// only from here on a deliberate y.
	pubConfirm
	// pubRunning: a publication cycle is in flight (live polling).
	pubRunning
	// pubDone: the cycle ended; receipts and remediation are shown.
	pubDone
)

// wizard is the "new run" form state.
type wizard struct {
	topic        entry
	recipe       selector // (none) + the available recipes (v0.6)
	recipeInputs entry    // "name=value" pairs for the selected recipe
	profile      selector
	format       selector
	mode         selector
	urls         entry
	followUp     bool
	followUpOn   bool // whether follow-up is available (search configured)
	field        int  // focused field, 0..7
	err          string
}

// option is one selectable entry with an availability flag.
type option struct {
	label   string
	enabled bool
	Detail  string
}

// selector is a single-choice list widget.
type selector struct {
	opts []option
	sel  int
}

func (s *selector) move(delta int) {
	if len(s.opts) == 0 {
		return
	}
	for i := 0; i < len(s.opts); i++ {
		s.sel = (s.sel + delta + len(s.opts)) % len(s.opts)
		if s.opts[s.sel].enabled {
			return
		}
	}
}

// current returns the selected option (zero option when none).
func (s selector) current() option {
	if s.sel < 0 || s.sel >= len(s.opts) {
		return option{}
	}
	return s.opts[s.sel]
}

// entry is a single-line text input with a rune cursor.
type entry struct {
	buf   string
	cur   int // rune index
	bound int // maximum runes (0 = unbounded)
}

// runes returns the buffer as a rune slice.
func (e entry) runes() []rune { return []rune(e.buf) }

// width returns the visible width of the buffer.
func (e entry) width() int { return len([]rune(e.buf)) }

// append adds a printable run at the cursor.
func (e *entry) append(r rune) {
	rs := e.runes()
	if e.bound > 0 && len(rs) >= e.bound {
		return
	}
	rs = append(rs[:e.cur], append([]rune{r}, rs[e.cur:]...)...)
	e.buf = string(rs)
	e.cur = e.cur + 1
}

// insert adds a string at the cursor.
func (e *entry) insert(s string) {
	for _, r := range s {
		e.append(r)
	}
}

// key handles one key press; it reports whether the key was consumed.
func (e *entry) key(msg tea.KeyMsg) bool {
	switch msg.Type {
	case tea.KeyBackspace:
		if e.cur > 0 {
			rs := e.runes()
			rs = append(rs[:e.cur-1], rs[e.cur:]...)
			e.buf = string(rs)
			e.cur--
		}
		return true
	case tea.KeyDelete:
		rs := e.runes()
		if e.cur < len(rs) {
			rs = append(rs[:e.cur], rs[e.cur+1:]...)
			e.buf = string(rs)
		}
		return true
	case tea.KeyLeft:
		if e.cur > 0 {
			e.cur--
		}
		return true
	case tea.KeyRight:
		if e.cur < len(e.runes()) {
			e.cur++
		}
		return true
	case tea.KeyHome:
		e.cur = 0
		return true
	case tea.KeyEnd:
		e.cur = len(e.runes())
		return true
	case tea.KeyRunes:
		for _, r := range msg.Runes {
			if r < 0x20 || r == 0x7f {
				continue // non-printable
			}
			e.append(r)
		}
		return true
	}
	return false
}

// artifactOpenMsg reports the outcome of an external file open.
type artifactOpenMsg struct{ err error }

// Messages produced by the TUI's own commands.

type (
	infoMsg struct {
		info Info
		err  error
	}
	listLoadedMsg struct {
		runs []RunInfo
		err  error
	}
	detailLoadedMsg struct {
		id     string
		detail RunDetail
		err    error
	}
	startResultMsg struct {
		id  string
		err error
	}
	runFinishedMsg struct{ id string }
	refreshMsg     struct{ at time.Time } // progress poll
	animMsg        struct{ at time.Time } // spinner animation

	// v0.6 publication messages (screenPublish).
	pubLoadedMsg struct {
		runID string
		state *PublicationState
		err   error
	}
	pubStartedMsg struct {
		runID string
		err   error
	}
	pubFinishedMsg struct{}
	pubRefreshMsg  struct{ at time.Time } // live poll tick while a cycle runs
)

// Model is the TUI state machine.
type Model struct {
	// svc is the application capability (see Service).
	svc Service
	// open opens artifact files in an external editor (injectable).
	open Opener

	width  int
	height int
	help   bool

	screen screen
	status string // transient info line ("" when none)
	err    string // last error, cleared on the next action

	info Info

	// List state.
	runs   []RunInfo
	runSel int

	// Wizard state (screenNew).
	wiz wizard

	// Run state (screenRun).
	activeID  string
	detail    RunDetail
	stageSel  int
	artCycle  int
	verbose   bool
	frame     int
	executing bool // a run/resume/retry cycle is active for activeID

	// Publication state (screenPublish).
	pubSvc   PublicationService // nil: publication is not available
	pubRun   string             // the run under publication review
	pubState *PublicationState  // latest projection (nil while loading)
	pubSel   int                // selected plan index
	pubPhase pubPhase
	pubErr   string // last projection/approval error ("" when none)

	// confirm is a modal prompt layered over the current screen.
	confirm *confirm
}

// Opener opens one artifact file for the user. The default implementation
// runs the external editor; tests substitute a recorder.
type Opener interface {
	Open(path string) tea.Cmd
}

// execOpener opens files with $EDITOR (falling back to "less").
type execOpener struct{}

// Open implements Opener.
func (execOpener) Open(path string) tea.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "less"
	}
	// #nosec G204,G702 -- execs the operator's own $EDITOR (never a shell, argument
	// vector) with a local workspace path; this is the documented "open artifact"
	// feature of a single-user local TUI.
	return tea.ExecProcess(exec.Command(editor, path), func(err error) tea.Msg {
		return artifactOpenMsg{err: err}
	})
}

// New creates a Model bound to the given service.
// New creates a Model bound to the given service.
func New(svc Service) Model {
	return Model{
		svc:    svc,
		open:   execOpener{},
		width:  80,
		height: 24,
		screen: screenList,
	}
}

// NewWithPublication creates a Model bound to the service plus the
// controlled-publication surface (v0.6). pub is nil when the application
// does not offer publication (the run view then offers no entry point).
func NewWithPublication(svc Service, pub PublicationService) Model {
	m := New(svc)
	m.pubSvc = pub
	return m
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return tea.Batch(loadInfoCmd(m.svc), m.refreshList())
}

// clearScreenCmd returns the command that asks the program to redraw.
func clearScreenCmd() tea.Cmd {
	return func() tea.Msg { return tea.ClearScreen() }
}

// loadInfoCmd builds the command that fetches the environment info.
func loadInfoCmd(svc Service) tea.Cmd {
	return func() tea.Msg {
		info, err := svc.Info()
		return infoMsg{info: info, err: err}
	}
}

// refreshList returns the command that (re)loads the run list.
func (m Model) refreshList() tea.Cmd {
	return func() tea.Msg {
		runs, err := m.svc.ListRuns(context.Background())
		return listLoadedMsg{runs: runs, err: err}
	}
}

// refreshDetail returns the command that (re)loads the detail of id.
func (m Model) refreshDetail(id string) tea.Cmd {
	return func() tea.Msg {
		detail, err := m.svc.RunDetail(context.Background(), id)
		return detailLoadedMsg{id: id, detail: detail, err: err}
	}
}

// finishRun returns the command that watches the in-flight cycle.
func (m Model) finishRun(id string) tea.Cmd {
	done := m.svc.Done()
	return func() tea.Msg {
		if done == nil {
			return runFinishedMsg{id: id}
		}
		select {
		case <-done:
			return runFinishedMsg{id: id}
		case <-time.After(3 * time.Second):
			return runFinishedMsg{id: id}
		}
	}
}

// loadPublicationState returns the command that projects the publication
// picture of runID (read-only: it may prepare plans and previews, never
// publish).
func (m Model) loadPublicationState(runID string, opts PublicationOptions) tea.Cmd {
	return func() tea.Msg {
		state, err := m.pubSvc.PublicationState(context.Background(), runID, opts)
		return pubLoadedMsg{runID: runID, state: state, err: err}
	}
}

// approvePubCmd returns the command that performs the explicit approval &
// publish intent (the synchronous part; the cycle runs asynchronously).
func (m Model) approvePubCmd(runID string) tea.Cmd {
	return func() tea.Msg {
		err := m.pubSvc.ApprovePublication(context.Background(), runID)
		return pubStartedMsg{runID: runID, err: err}
	}
}

// resumePubCmd returns the command that performs the explicit resume
// intent.
func (m Model) resumePubCmd(runID string) tea.Cmd {
	return func() tea.Msg {
		err := m.pubSvc.ResumePublication(context.Background(), runID)
		return pubStartedMsg{runID: runID, err: err}
	}
}

// watchPubDone returns the command that watches the in-flight publication
// cycle (mirrors finishRun, with a long safety timeout for a cycle that
// never signals).
func (m Model) watchPubDone() tea.Cmd {
	done := m.pubSvc.PublicationDone()
	return func() tea.Msg {
		if done == nil {
			return pubFinishedMsg{}
		}
		select {
		case <-done:
			return pubFinishedMsg{}
		case <-time.After(10 * time.Minute):
			return pubFinishedMsg{}
		}
	}
}

// startCycle arms the in-flight cycle bookkeeping for id.
func (m *Model) startCycle(id string, note string) {
	m.activeID = id
	m.stageSel = 0
	m.artCycle = 0
	m.executing = true
	m.status = note
	m.err = ""
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case infoMsg:
		if msg.err == nil {
			m.info = msg.info
			m.wiz.followUpOn = msg.info.SearchConfigured
			m.buildWizardProfile()
		} else {
			m.err = "loading configuration: " + msg.err.Error()
		}
		return m, nil

	case listLoadedMsg:
		m.runs = msg.runs
		if m.runSel >= len(m.runs) {
			m.runSel = max(0, len(m.runs)-1)
		}
		if msg.err != nil {
			m.err = "loading runs: " + msg.err.Error()
		}
		return m, nil

	case detailLoadedMsg:
		if msg.err != nil {
			m.err = "loading run: " + msg.err.Error()
			return m, m.maybePoll()
		}
		m.detail = msg.detail
		m.clampSelections()
		return m, m.maybePoll()

	case startResultMsg:
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.screen = screenRun
		m.startCycle(msg.id, "run started")
		return m, tea.Batch(m.refreshDetail(msg.id), m.finishRun(msg.id))

	case runFinishedMsg:
		m.executing = false
		return m, m.refreshDetail(msg.id)

	case pubLoadedMsg:
		if msg.runID != m.pubRun || m.screen != screenPublish {
			return m, nil // stale projection (the screen was left in the meantime)
		}
		m.pubErr = ""
		if msg.err != nil {
			m.pubErr = msg.err.Error()
			return m, m.maybePollPub()
		}
		m.pubState = msg.state
		m.clampPubSelection()
		if m.pubPhase == pubRunning && m.pubSvc.PublicationInFlight() == "" {
			m.pubPhase = pubDone
			m.status = "publication cycle finished"
		}
		return m, m.maybePollPub()

	case pubStartedMsg:
		if msg.runID != m.pubRun || m.screen != screenPublish {
			return m, nil
		}
		m.confirm = nil
		if msg.err != nil {
			// The synchronous part failed before any cycle: back to review.
			m.pubPhase = pubReview
			m.pubErr = msg.err.Error()
			return m, m.loadPublicationState(m.pubRun, PublicationOptions{Preview: true})
		}
		m.pubPhase = pubRunning
		m.status = "publishing…"
		return m, tea.Batch(m.loadPublicationState(m.pubRun, PublicationOptions{}), m.maybePollPub(), m.watchPubDone())

	case pubFinishedMsg:
		if m.pubRun == "" || m.screen != screenPublish {
			return m, nil
		}
		m.pubPhase = pubDone
		m.status = "publication cycle finished"
		return m, m.loadPublicationState(m.pubRun, PublicationOptions{})

	case pubRefreshMsg:
		if m.screen != screenPublish || m.pubPhase != pubRunning {
			return m, nil
		}
		return m, tea.Batch(m.loadPublicationState(m.pubRun, PublicationOptions{}), m.maybePollPub())

	case refreshMsg:
		return m, m.maybePoll()

	case animMsg:
		m.frame++
		switch {
		case m.screen == screenRun:
			return m, m.animate()
		case m.screen == screenPublish && (m.pubState == nil || m.pubPhase == pubRunning):
			return m, m.animate()
		}
		return m, nil

	case artifactOpenMsg:
		if msg.err != nil {
			m.err = "opening file: " + msg.err.Error()
		} else {
			m.status = "editor closed"
		}
		return m, nil
	}
	return m, nil
}

// maybePoll schedules the next progress poll while the run being observed
// has not reached a terminal state: either a cycle this session started
// (executing) or a run whose persisted manifest says it is running (for
// example a run started elsewhere that the user selected).
func (m Model) maybePoll() tea.Cmd {
	if m.screen != screenRun || isTerminal(m.detail.Status) {
		return nil
	}
	if m.executing || m.detail.Status == "running" {
		return tea.Batch(tea.Tick(1*time.Second, func(t time.Time) tea.Msg { return refreshMsg{at: t} }), m.animate())
	}
	return nil
}

// animate schedules the next spinner frame.
func (m Model) animate() tea.Cmd {
	return tea.Tick(200*time.Millisecond, func(t time.Time) tea.Msg { return animMsg{at: t} })
}

// maybePollPub schedules the live refresh and spinner while the publication
// screen needs them: while the initial projection is still loading, or while
// a publication cycle is in flight.
func (m Model) maybePollPub() tea.Cmd {
	if m.screen != screenPublish {
		return nil
	}
	if m.pubState == nil || m.pubPhase == pubRunning {
		return tea.Batch(tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return pubRefreshMsg{at: t} }), m.animate())
	}
	return nil
}

// clampSelections keeps the selection indexes valid after a detail refresh.
func (m *Model) clampSelections() {
	if m.stageSel >= len(m.detail.Stages) {
		m.stageSel = max(0, len(m.detail.Stages)-1)
	}
	if m.stageSel < 0 {
		m.stageSel = 0
	}
}

// clampPubSelection keeps the plan selection valid after a state refresh.
func (m *Model) clampPubSelection() {
	if m.pubState == nil {
		return
	}
	if m.pubSel >= len(m.pubState.Plans) {
		m.pubSel = max(0, len(m.pubState.Plans)-1)
	}
	if m.pubSel < 0 {
		m.pubSel = 0
	}
}

// isTerminal reports whether a run status string is a terminal one.
func isTerminal(status string) bool {
	switch status {
	case "succeeded", "failed", "cancelled":
		return true
	}
	return false
}

// keyName maps a key message to a short switch-friendly name. Arrow and
// other named keys map to their names; printable keys map to their string.
func keyName(msg tea.KeyMsg) string {
	switch msg.Type {
	case tea.KeyUp:
		return "up"
	case tea.KeyDown:
		return "down"
	case tea.KeyLeft:
		return "left"
	case tea.KeyRight:
		return "right"
	}
	return msg.String()
}

// handleKey routes key presses to the active screen (or the confirmation
// overlay, which wins when present).
func (m Model) handleKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	if m.confirm != nil {
		return m.handleConfirmKey(msg)
	}
	switch {
	case msg.Type == tea.KeyCtrlL:
		return m, clearScreenCmd()
	case keyName(msg) == "?":
		m.help = !m.help
		return m, nil
	}
	switch m.screen {
	case screenList:
		return m.handleListKey(msg)
	case screenNew:
		return m.handleWizardKey(msg)
	case screenRun:
		return m.handleRunKey(msg)
	case screenPublish:
		return m.handlePublishKey(msg)
	}
	return m, nil
}

// handleConfirmKey implements the modal prompt.
func (m Model) handleConfirmKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	c := m.confirm
	m.confirm = nil
	switch keyName(msg) {
	case "y", "Y":
		switch c.kind {
		case confirmQuitRunning:
			m.svc.Cancel()
			m.executing = false
			return m, tea.Quit
		case confirmCancel:
			m.svc.Cancel()
			m.executing = false // the cancel is confirmed; quit must not ask again
			m.status = "cancelling run…"
			return m, tea.Batch(m.finishRun(m.activeID), m.maybePoll())
		case confirmApprovePub:
			m.status = "preparing publication plans…"
			return m, m.approvePubCmd(m.pubRun)
		case confirmResumePub:
			m.status = "resuming publication…"
			return m, m.resumePubCmd(m.pubRun)
		case confirmCancelPub:
			m.pubSvc.CancelPublication()
			m.status = "cancelling publication…"
			return m, tea.Batch(m.watchPubDone(), m.maybePollPub())
		case confirmPubBack:
			m.pubSvc.CancelPublication()
			m.status = "cancelling publication…"
			return m.backToRunDetail()
		case confirmQuitPub:
			m.pubSvc.CancelPublication()
			return m, tea.Quit
		}
	case "n", "N", "esc":
		if c.kind == confirmQuitRunning {
			m.status = ""
		} else if c.kind >= confirmApprovePub {
			m.status = "dismissed — nothing changed"
		} else {
			m.status = "cancel dismissed"
		}
		return m, nil
	}
	// Anything else: keep prompting.
	m.confirm = c
	return m, nil
}

// requestCancel arms the cancel confirmation when a run is executing,
// otherwise it reports that there is nothing to cancel.
func (m Model) requestCancel() (Model, tea.Cmd) {
	if m.executing {
		m.confirm = &confirm{kind: confirmCancel, prompt: "Cancel the running run? It will be resumable later."}
		m.status = ""
		return m, nil
	}
	m.status = "nothing to cancel"
	return m, nil
}

// requestQuit quits immediately, or asks for confirmation when a run is
// executing in this session.
func (m Model) requestQuit() (Model, tea.Cmd) {
	if m.executing {
		m.confirm = &confirm{kind: confirmQuitRunning, prompt: "A run is in progress. Cancel it and quit?"}
		return m, nil
	}
	return m, tea.Quit
}

// handleListKey implements the recent-runs screen keys.
func (m Model) handleListKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch keyName(msg) {
	case "q", "ctrl+c", "esc":
		return m.requestQuit()
	case "j", "down":
		if len(m.runs) == 0 {
			return m, nil
		}
		m.runSel = (m.runSel + 1) % len(m.runs)
		m.err = ""
		return m, nil
	case "k", "up":
		if len(m.runs) == 0 {
			return m, nil
		}
		m.runSel = (m.runSel - 1 + len(m.runs)) % len(m.runs)
		m.err = ""
		return m, nil
	case "n":
		return m.gotoNewRun()
	case "enter":
		if len(m.runs) == 0 {
			return m, nil
		}
		return m.openRun(m.runs[m.runSel].ID, "selected")
	case "r":
		if len(m.runs) == 0 {
			return m, nil
		}
		return m.requestResume(m.runs[m.runSel].ID)
	}
	return m, nil
}

// gotoNewRun resets the wizard and switches to it.
func (m Model) gotoNewRun() (Model, tea.Cmd) {
	m.screen = screenNew
	m.wiz = m.freshWizard()
	m.err = ""
	m.status = ""
	return m, nil
}

// freshWizard builds the wizard prefilled from the environment info.
func (m Model) freshWizard() wizard {
	w := wizard{
		topic: entry{bound: 1000},
		recipe: selector{opts: []option{
			{label: "(none)", enabled: true},
		}},
		recipeInputs: entry{bound: 500},
		profile: selector{opts: []option{
			{label: "(none)", enabled: true},
		}},
		format: selector{opts: []option{
			{label: "article", enabled: true},
			{label: "thread", enabled: true},
			{label: "hybrid", enabled: true},
		}},
		mode: selector{opts: []option{
			{label: "auto (search)", enabled: m.info.SearchConfigured},
			{label: "explicit URLs", enabled: true},
			{label: "supplement (URLs + search)", enabled: m.info.SearchConfigured},
		}},
		urls:       entry{bound: 2000},
		followUpOn: m.info.SearchConfigured,
	}
	// Prefill the recipe selector from the available recipes.
	m.buildWizardRecipes()
	// Prefill the selectors from the configured defaults when known.
	for i, p := range m.info.Profiles {
		w.profile.opts = append(w.profile.opts, option{
			label:   p.Spec,
			enabled: true,
			Detail:  p.Detail,
		})
		if p.Spec == m.info.ProfilesDefault {
			w.profile.sel = i + 1 // index 0 is "(none)"
		}
	}
	w.format.sel = m.formatIndex(m.info.OutputFormat)
	return w
}

// formatIndex maps a configured format name to the wizard selector index.
func (m Model) formatIndex(name string) int {
	switch strings.ToLower(name) {
	case "thread":
		return 1
	case "hybrid":
		return 2
	case "article", "":
		return 0
	}
	return 0
}

// buildWizardProfile syncs the wizard's profile selector with the
// environment info (the info and wizard creation can race, so this is
// idempotent) and re-selects the configured default profile.
func (m *Model) buildWizardProfile() {
	for _, p := range m.info.Profiles {
		found := false
		for _, o := range m.wiz.profile.opts {
			if o.label == p.Spec {
				found = true
				break
			}
		}
		if !found {
			m.wiz.profile.opts = append(m.wiz.profile.opts, option{label: p.Spec, enabled: true, Detail: p.Detail})
		}
	}
	if m.wiz.profile.sel >= len(m.wiz.profile.opts) {
		m.wiz.profile.sel = 0
	}
	if m.info.ProfilesDefault == "" {
		return
	}
	for i, o := range m.wiz.profile.opts {
		if o.label == m.info.ProfilesDefault {
			m.wiz.profile.sel = i
			return
		}
	}
}

// buildWizardRecipes syncs the wizard's recipe selector with the
// environment info (the info and wizard creation can race, so this is
// idempotent).
func (m *Model) buildWizardRecipes() {
	for _, r := range m.info.Recipes {
		id := r.ID
		if id == "" {
			continue
		}
		found := false
		for _, o := range m.wiz.recipe.opts {
			if o.label == id {
				found = true
				break
			}
		}
		if !found {
			title := r.Title
			if title == "" {
				title = id
			}
			m.wiz.recipe.opts = append(m.wiz.recipe.opts, option{label: id, enabled: true, Detail: title + " (" + r.Detail + ")"})
		}
	}
	if m.wiz.recipe.sel >= len(m.wiz.recipe.opts) {
		m.wiz.recipe.sel = 0
	}
}

// openRun switches to the run screen and starts observing id.
func (m Model) openRun(id, note string) (Model, tea.Cmd) {
	m.activeID = id
	m.screen = screenRun
	m.detail = RunDetail{RunInfo: RunInfo{ID: id}}
	m.stageSel = 0
	m.artCycle = 0
	m.executing = m.svc.InFlight() == id
	m.status = note
	m.err = ""
	return m, tea.Batch(m.refreshDetail(id), m.maybePoll(), m.animate())
}

// requestResume asks the service to resume id and observes it.
func (m Model) requestResume(id string) (Model, tea.Cmd) {
	m.status = "resuming " + id + "…"
	m.err = ""
	cmd := func() tea.Msg {
		err := m.svc.ResumeRun(context.Background(), id)
		return startResultMsg{id: id, err: err}
	}
	return m, cmd
}

// requestRetry asks the service to retry one stage of id.
func (m Model) requestRetry(id, stage string) (Model, tea.Cmd) {
	m.status = "retrying stage " + stage + "…"
	m.err = ""
	cmd := func() tea.Msg {
		err := m.svc.RetryStage(context.Background(), id, stage)
		return startResultMsg{id: id, err: err}
	}
	return m, cmd
}

// wizardFields returns the field labels in focus order.
func wizardFields() []string {
	return []string{"topic", "recipe", "inputs", "profile", "format", "source mode", "URLs", "follow-up"}
}

// handleWizardKey implements the new-run form keys.
func (m Model) handleWizardKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	w := &m.wiz
	switch keyName(msg) {
	case "esc":
		m.screen = screenList
		m.status = ""
		return m, m.refreshList()
	case "ctrl+c":
		// q is reserved for typing in text fields; ctrl+c quits.
		return m.requestQuit()
	case "tab", "down":
		w.field = (w.field + 1) % len(wizardFields())
		w.err = ""
		return m, nil
	case "shift+tab", "up":
		w.field = (w.field - 1 + len(wizardFields())) % len(wizardFields())
		w.err = ""
		return m, nil
	case "enter":
		return m.advanceWizard()
	}

	// Field-local keys.
	switch w.field {
	case 0:
		if w.topic.key(msg) {
			return m, nil
		}
	case 1:
		switch keyName(msg) {
		case "left", "h":
			w.recipe.move(-1)
			return m, nil
		case "right", "l":
			w.recipe.move(1)
			return m, nil
		}
	case 2:
		if w.recipeInputs.key(msg) {
			return m, nil
		}
	case 3:
		switch keyName(msg) {
		case "left", "h":
			w.profile.move(-1)
			return m, nil
		case "right", "l":
			w.profile.move(1)
			return m, nil
		}
	case 4:
		switch keyName(msg) {
		case "left", "h":
			w.format.move(-1)
			return m, nil
		case "right", "l":
			w.format.move(1)
			return m, nil
		}
	case 5:
		switch keyName(msg) {
		case "left", "h":
			w.mode.move(-1)
			return m, nil
		case "right", "l":
			w.mode.move(1)
			return m, nil
		}
	case 6:
		if w.urls.key(msg) {
			return m, nil
		}
	case 7:
		switch msg.String() {
		case "y", "n", " ", "enter":
			w.followUp = !w.followUp
			return m, nil
		}
	}
	return m, nil
}

// advanceWizard moves to the next field or starts the run on the last one.
func (m Model) advanceWizard() (Model, tea.Cmd) {
	w := &m.wiz
	w.err = ""
	if w.field < len(wizardFields())-1 {
		w.field++
		return m, nil
	}
	intent := m.wizardIntent()
	if kind := intent.Valid(); kind != "" {
		w.err = intentError(kind)
		return m, nil
	}
	if kind := m.wizardServiceError(intent); kind != "" {
		w.err = kind
		return m, nil
	}
	m.status = "starting run…"
	return m, startNewRunCmd(m.svc, intent)
}

// wizardIntent assembles the run intent from the wizard state.
func (m Model) wizardIntent() NewRunIntent {
	w := m.wiz
	return NewRunIntent{
		Topic:        strings.TrimSpace(w.topic.buf),
		Recipe:       recipeIDFromLabel(w.recipe.current().label),
		RecipeInputs: parseRecipeInputsRaw(w.recipeInputs.buf),
		Profile:      w.profile.current().label,
		Format:       w.format.current().label,
		SourceMode:   sourceModeFromLabel(w.mode.current().label),
		URLs:         parseURLs(w.urls.buf),
		FollowUp:     w.followUp && w.followUpOn,
	}
}

// recipeIDFromLabel maps the wizard's recipe labels to intent values
// ("(none)" means no recipe).
func recipeIDFromLabel(label string) string {
	if label == "" || label == "(none)" {
		return ""
	}
	return label
}

// parseRecipeInputsRaw splits a "name=value name2=value2" input line into a
// map (duplicates keep the last value). Malformed pairs are dropped; the
// run's recipe input resolution reports what is unknown or untyped.
func parseRecipeInputsRaw(raw string) map[string]string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ' ' || r == ',' || r == '\t'
	})
	m := make(map[string]string)
	for _, f := range fields {
		parts := strings.SplitN(strings.TrimSpace(f), "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
			continue
		}
		m[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// wizardServiceError maps intent fields the service will also validate
// (search availability, disabled mode) into display errors.
func (m Model) wizardServiceError(intent NewRunIntent) string {
	switch intent.SourceMode {
	case ModeAuto, ModeSupplement:
		if !m.info.SearchConfigured {
			return "automatic sources require a search endpoint (search.base_url is not configured)"
		}
	}
	if intent.FollowUp && !m.info.SearchConfigured {
		return "follow-up requires a search endpoint (search.base_url is not configured)"
	}
	return ""
}

// intentError renders a validation failure for display.
func intentError(kind errorKind) string {
	switch kind {
	case errKindTopic:
		return "a topic is required"
	case errKindURLs:
		return "this source mode needs at least one URL"
	case errKindMode:
		return "invalid source mode"
	}
	return "invalid run intent"
}

// sourceModeFromLabel maps the wizard's mode labels to intent values.
func sourceModeFromLabel(label string) string {
	switch {
	case strings.HasPrefix(label, "auto"):
		return ModeAuto
	case strings.HasPrefix(label, "explicit"):
		return ModeExplicit
	case strings.HasPrefix(label, "supplement"):
		return ModeSupplement
	}
	return ModeAuto
}

// parseURLs splits a user-typed URL list (space or comma separated).
func parseURLs(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ' ' || r == ',' || r == '\t'
	})
	out := make([]string, 0, len(fields))
	seen := make(map[string]bool, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// startNewRunCmd starts a new run and reports the result.
func startNewRunCmd(svc Service, intent NewRunIntent) tea.Cmd {
	return func() tea.Msg {
		id, err := svc.StartNewRun(context.Background(), intent)
		return startResultMsg{id: id, err: err}
	}
}

// handleRunKey implements the run screen keys.
func (m Model) handleRunKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	id := m.activeID
	switch keyName(msg) {
	case "q", "esc", "ctrl+c":
		return m.requestQuit()
	case "c":
		return m.requestCancel()
	case "r":
		if !isTerminal(m.detail.Status) {
			m.status = "run still in progress"
			return m, nil
		}
		if !m.detail.Resumable {
			m.status = "not resumable: " + m.detail.ResumeReason
			return m, nil
		}
		return m.requestResume(id)
	case "p":
		if m.executing || !isTerminal(m.detail.Status) {
			m.status = "the run must finish before it can be published"
			return m, nil
		}
		if m.pubSvc == nil {
			m.status = "publication is not available in this environment"
			return m, nil
		}
		return m.gotoPublish(id)
	case "t":
		stage := m.selectedStage()
		if stage == "" {
			m.status = "no stage selected to retry"
			return m, nil
		}
		return m.requestRetry(id, stage)
	case "o":
		if len(m.detail.Artifacts) == 0 {
			m.status = "no artifacts yet"
			return m, nil
		}
		a := m.detail.Artifacts[m.artCycle%len(m.detail.Artifacts)]
		m.artCycle++
		m.status = "opening " + a.Path
		return m, m.open.Open(a.Path)
	case "v":
		m.verbose = !m.verbose
		return m, nil
	case "j", "down":
		if len(m.detail.Stages) == 0 {
			return m, nil
		}
		m.stageSel = (m.stageSel + 1) % len(m.detail.Stages)
		return m, nil
	case "k", "up":
		if len(m.detail.Stages) == 0 {
			return m, nil
		}
		m.stageSel = (m.stageSel - 1 + len(m.detail.Stages)) % len(m.detail.Stages)
		return m, nil
	}
	return m, nil
}

// selectedStage returns the name of the currently selected stage ("" when
// none is selected or the stage cannot be retried).
func (m Model) selectedStage() string {
	if m.stageSel < 0 || m.stageSel >= len(m.detail.Stages) {
		return ""
	}
	return m.detail.Stages[m.stageSel].Name
}

// ---------------------------------------------------------------------------
// Publication screen (v0.6, #122)
// ---------------------------------------------------------------------------

// gotoPublish enters the publication review screen for id. The entry
// projection prepares the run's plans (a gate-checked, non-publishing state
// change) and re-runs the dry-run previews.
func (m Model) gotoPublish(id string) (Model, tea.Cmd) {
	m.activeID = id
	m.pubRun = id
	m.pubState = nil
	m.pubErr = ""
	m.pubSel = 0
	m.pubPhase = pubReview
	m.screen = screenPublish
	m.status = ""
	m.err = ""
	return m, tea.Batch(m.loadPublicationState(id, PublicationOptions{Prepare: true, Preview: true}), m.maybePollPub())
}

// backToRunDetail leaves the publication screen back to the run view.
func (m Model) backToRunDetail() (Model, tea.Cmd) {
	id := m.activeID
	m.screen = screenRun
	m.pubRun = ""
	m.pubState = nil
	m.pubErr = ""
	m.pubSel = 0
	m.pubPhase = pubReview
	m.status = ""
	m.err = ""
	return m, m.refreshDetail(id)
}

// handlePublishKey implements the publication screen keys. The global keys
// (ctrl+l, ?) and the confirmation modal are handled before this.
func (m Model) handlePublishKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch keyName(msg) {
	case "ctrl+c":
		if m.pubPhase == pubRunning {
			return m.requestPubQuit()
		}
		return m.requestQuit()
	}
	switch m.pubPhase {
	case pubRunning:
		switch keyName(msg) {
		case "c":
			return m.requestPubCancel()
		case "q", "esc":
			if keyName(msg) == "q" {
				return m.requestPubQuit()
			}
			return m.requestPubBack()
		}
		return m, nil
	}
	// pubReview and pubDone.
	switch keyName(msg) {
	case "q":
		return m.requestPubQuit()
	case "esc":
		return m.backToRunDetail()
	case "j", "down", "k", "up":
		if m.pubState == nil || len(m.pubState.Plans) == 0 {
			return m, nil
		}
		delta := 1
		if keyName(msg) == "k" || keyName(msg) == "up" {
			delta = -1
		}
		n := len(m.pubState.Plans)
		m.pubSel = (m.pubSel + delta + n) % n
		return m, nil
	case "a":
		return m.requestPubApprove()
	case "r":
		return m.requestPubResume()
	case "v":
		m.verbose = !m.verbose
		return m, nil
	}
	return m, nil
}

// requestPubApprove arms the explicit approval & publish confirmation when
// the state is approvable, otherwise it reports the blocker.
func (m Model) requestPubApprove() (Model, tea.Cmd) {
	if m.pubState == nil {
		m.status = "no publication state loaded yet"
		return m, nil
	}
	if !m.pubState.Approvable {
		m.status = "cannot approve: " + m.pubState.ApproveBlocker
		return m, nil
	}
	n := actionablePlans(m.pubState)
	m.confirm = &confirm{
		kind:   confirmApprovePub,
		prompt: fmt.Sprintf("Approve and publish %d plan(s) for run %s?", n, shortRunID(m.pubRun)),
		detail: "This performs external side effects (site commit, thread post). Nothing publishes until you press y.",
	}
	m.status = ""
	return m, nil
}

// requestPubResume arms the explicit resume confirmation when a resumable
// plan exists, otherwise it reports the blocker.
func (m Model) requestPubResume() (Model, tea.Cmd) {
	if m.pubState == nil {
		m.status = "no publication state loaded yet"
		return m, nil
	}
	if !m.pubState.Resumable {
		m.status = "nothing to resume: " + m.pubState.ResumeBlocker
		return m, nil
	}
	m.confirm = &confirm{
		kind:   confirmResumePub,
		prompt: "Resume the failed publication?",
		detail: "Already-published targets keep their receipts and are not re-published; the remaining targets re-run under their existing approvals.",
	}
	m.status = ""
	return m, nil
}

// requestPubCancel arms the cancel confirmation for the in-flight cycle.
func (m Model) requestPubCancel() (Model, tea.Cmd) {
	if m.pubPhase != pubRunning {
		m.status = "nothing to cancel"
		return m, nil
	}
	m.confirm = &confirm{
		kind:   confirmCancelPub,
		prompt: "Cancel the in-flight publication?",
		detail: "In-flight targets are recorded as cancelled; the state stays resumable.",
	}
	m.status = ""
	return m, nil
}

// requestPubQuit quits, or asks first (and cancels on y) while a
// publication cycle is in flight.
func (m Model) requestPubQuit() (Model, tea.Cmd) {
	if m.pubPhase == pubRunning {
		m.confirm = &confirm{
			kind:   confirmQuitPub,
			prompt: "A publication is in progress. Cancel it and quit?",
			detail: "The in-flight targets are recorded as cancelled; resume them later.",
		}
		return m, nil
	}
	return m, tea.Quit
}

// requestPubBack goes back to the run view, or asks first (and cancels on
// y) while a publication cycle is in flight.
func (m Model) requestPubBack() (Model, tea.Cmd) {
	if m.pubPhase == pubRunning {
		m.confirm = &confirm{
			kind:   confirmPubBack,
			prompt: "A publication is in progress. Cancel it and go back to the run?",
			detail: "The in-flight targets are recorded as cancelled; resume them later.",
		}
		return m, nil
	}
	return m.backToRunDetail()
}

// actionablePlans counts the plans the approval action would cover (the
// non-terminal ones: an already-succeeded plan is an idempotent no-op).
func actionablePlans(st *PublicationState) int {
	n := 0
	for i := range st.Plans {
		p := &st.Plans[i]
		switch p.Status {
		case "planned", "approved", "publishing", "failed", "partially_succeeded":
			n++
		}
	}
	return n
}

// shortRunID abbreviates a run ID for prompt strings.
func shortRunID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12] + "…"
}
