package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbletea"
	isatty "github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/local-newsroom/internal/config"
	"github.com/Mundo-Dolphins/local-newsroom/internal/orchestrator"
	"github.com/Mundo-Dolphins/local-newsroom/internal/profiles"
	"github.com/Mundo-Dolphins/local-newsroom/internal/resume"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/tui"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workspace"
)

// tuiCmd is the "newsroom tui" subcommand: the keyboard-first terminal
// interface for daily use.
//
// It is a presentation layer over the same run/workflow services the `run`
// command uses (see run_plan.go for the shared wiring): it shows runs,
// stages, warnings, and artifacts, and starts, resumes, retries, and
// cancels runs through the application layer — it contains no pipeline
// logic of its own.
var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Open the keyboard-first terminal interface",
	Long: `Open the newsroom terminal interface for daily use:

  • list and inspect recent runs (stages, warnings, errors, artifacts)
  • start a new run: topic, profile, output format, source mode, URLs
  • watch stage progress of the running run
  • resume failed or interrupted runs, retry a stage, cancel with
    confirmation, and open rendered artifacts in $EDITOR

The TUI runs locally and uses the configured remote endpoints (oMLX for the
LLM, SearXNG for search) exactly as the ` + "`run`" + ` command does; it starts
no additional daemon. Run ` + "`newsroom run`" + ` for the non-interactive
equivalent.`,
	Example: `  newsroom tui
  newsroom tui --workspace-root ~/.local/share/newsroom --workspace main`,
	RunE: doTui,
}

// tuiFlags holds the flags for the tui command.
type tuiFlags struct {
	workspace     string
	workspaceRoot string
}

// tf are the tui command flags.
var tf tuiFlags

func init() {
	f := tuiCmd.Flags()
	f.StringVar(&tf.workspace, "workspace", "default", "workspace name whose runs are shown")
	f.StringVar(&tf.workspaceRoot, "workspace-root", ".newsroom", "workspace root directory")
	rootCmd.AddCommand(tuiCmd)
}

// runCycleGrace bounds how long the command waits for an in-flight run to
// persist its final state after the TUI exits.
const runCycleGrace = 20 * time.Second

// tuiApp is the tui.Service implementation for the newsroom application:
// it wires the TUI to the real run, workspace, orchestrator, resume, and
// configuration services. All pipeline logic lives behind the methods; the
// TUI package only calls them and renders the results.
type tuiApp struct {
	cfg           *config.Config
	workspaceRoot string // absolute
	workspaceName string
	overrides     map[string]config.Override
	store         *workspace.Store

	mu        sync.Mutex
	runCtx    context.Context
	cancelRun context.CancelFunc
	runDone   chan struct{} // closed when the current cycle reaches its terminal state
	runningID string
}

// newTuiApp resolves the configuration and opens the workspace store.
func newTuiApp() (*tuiApp, error) {
	root, err := filepath.Abs(tf.workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving workspace root: %w", err)
	}
	name := tf.workspace
	if name == "" {
		name = "default"
	}
	ws := run.Workspace{Name: name, Path: root}
	if err := ws.Validate(); err != nil {
		return nil, fmt.Errorf("invalid workspace name %q: %w", name, err)
	}
	store, err := workspace.NewStoreWithSanitizer(root, run.NewSanitizer(appConfig.SecretValues(nil)...))
	if err != nil {
		return nil, fmt.Errorf("opening workspace: %w", err)
	}
	return &tuiApp{
		cfg:           appConfig,
		workspaceRoot: root,
		workspaceName: name,
		store:         store,
	}, nil
}

// Info implements tui.Service.
func (a *tuiApp) Info() (tui.Info, error) {
	info := tui.Info{WorkspaceRoot: a.workspaceRoot}
	if v, _, err := a.cfg.GetString(config.SettingLLMBaseURL, config.Override{}); err == nil {
		info.LLMBaseURL = v
	}
	if v, _, err := a.cfg.GetString(config.SettingLLMModel, config.Override{}); err == nil {
		info.LLMModel = v
	}
	info.LLMConfigured = info.LLMBaseURL != "" && info.LLMModel != ""
	if v, _, err := a.cfg.GetString(config.SettingSearchBaseURL, config.Override{}); err == nil {
		info.SearchBaseURL = v
	}
	info.SearchConfigured = info.SearchBaseURL != ""
	if v, _, err := a.cfg.GetString(config.SettingOutputFormat, config.Override{}); err == nil {
		info.OutputFormat = v
	}
	if v, source, err := a.cfg.Resolve(config.SettingProfilesName, config.Override{}); err == nil && source != "default" && source != "" {
		info.ProfilesDefault = v
	}
	profilesRoot, _, _ := a.cfg.GetString(config.SettingProfilesRoot, config.Override{})
	if profilesRoot != "" {
		profs, err := listProfiles(profilesRoot)
		if err != nil {
			return info, fmt.Errorf("listing profiles: %w", err)
		}
		info.Profiles = profs
	}
	return info, nil
}

// listProfiles walks the profiles root and returns the selectable profile
// specs: authors directly under the root ("<author>") and, for each
// publication directory, the authors under it ("<publication>/<author>").
func listProfiles(root string) ([]tui.ProfileOption, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	out := make([]tui.ProfileOption, 0, 8)
	addAuthor := func(spec, detail string) {
		dir := filepath.Join(abs, profiles.AuthorsDir, spec)
		if spec == "" {
			return
		}
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			return
		}
		out = append(out, tui.ProfileOption{Spec: spec, Detail: detail})
	}

	rootAuthors, err := os.ReadDir(filepath.Join(abs, profiles.AuthorsDir))
	if err == nil {
		for _, e := range rootAuthors {
			if !e.IsDir() {
				continue
			}
			addAuthor(e.Name(), abs)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Spec < out[j].Spec })

	pubs, err := os.ReadDir(abs)
	if err == nil {
		for _, e := range pubs {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || e.Name() == profiles.AuthorsDir {
				continue
			}
			authors, err := os.ReadDir(filepath.Join(abs, e.Name(), profiles.AuthorsDir))
			if err != nil {
				continue
			}
			for _, au := range authors {
				if !au.IsDir() {
					continue
				}
				out = append(out, tui.ProfileOption{
					Spec:   e.Name() + "/" + au.Name(),
					Detail: filepath.Join(abs, e.Name()),
				})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Spec < out[j].Spec })
	return out, nil
}

// ListRuns implements tui.Service.
func (a *tuiApp) ListRuns(ctx context.Context) ([]tui.RunInfo, error) {
	_ = ctx
	ids, err := a.store.ListRuns()
	if err != nil {
		return nil, err
	}
	infos := make([]tui.RunInfo, 0, len(ids))
	for _, id := range ids {
		m, _, err := a.store.LoadManifest(id)
		if err != nil {
			continue // an unreadable manifest is not shown in the list
		}
		infos = append(infos, a.manifestToInfo(m))
	}
	sort.SliceStable(infos, func(i, j int) bool {
		return infos[i].UpdatedAt.After(infos[j].UpdatedAt)
	})
	return infos, nil
}

// RunDetail implements tui.Service.
func (a *tuiApp) RunDetail(ctx context.Context, id string) (tui.RunDetail, error) {
	_ = ctx
	m, _, err := a.store.LoadManifest(run.RunID(id))
	if err != nil {
		return tui.RunDetail{}, err
	}
	return a.manifestToDetail(m)
}

// StartNewRun implements tui.Service.
func (a *tuiApp) StartNewRun(ctx context.Context, intent tui.NewRunIntent) (string, error) {
	ws, err := a.workspace()
	if err != nil {
		return "", err
	}
	ri := runIntent{
		Topic:            strings.TrimSpace(intent.Topic),
		ProfileSpec:      intent.Profile,
		Format:           intent.Format,
		SourceMode:       sourceModeFromString(intent.SourceMode),
		URLs:             intent.URLs,
		FollowUp:         intent.FollowUp,
		Workspace:        ws,
		QualityThreshold: 0,
	}
	plan, err := planRun(ctx, ri, a.overrides)
	if err != nil {
		return "", err
	}
	if err := a.beginCycle(ctx, plan, func() (string, bool, error) {
		res, runErr := plan.Orch.Run(a.runCtx, plan.Manifest, false)
		if res == nil || res.Manifest == nil {
			return "", false, runErr
		}
		return string(res.Manifest.ID), res.Status == run.RunStatusSucceeded, runErr
	}); err != nil {
		return "", err
	}
	return string(plan.Manifest.ID), nil
}

// ResumeRun implements tui.Service.
func (a *tuiApp) ResumeRun(ctx context.Context, id string) error {
	ws, err := a.workspace()
	if err != nil {
		return err
	}
	// A resume re-loads the profile through the same chain as a new run; no
	// explicit spec is given here (the TUI has no profile flag).
	ri := runIntent{
		ResumeID:  run.RunID(id),
		Workspace: ws,
	}
	plan, err := planRun(ctx, ri, a.overrides)
	if err != nil {
		return err
	}
	return a.beginCycle(ctx, plan, func() (string, bool, error) {
		res, runErr := plan.Orch.ResumeID(a.runCtx, plan.Manifest.ID)
		if res == nil || res.Manifest == nil {
			return "", false, runErr
		}
		return string(res.Manifest.ID), res.Status == run.RunStatusSucceeded, runErr
	})
}

// RetryStage implements tui.Service.
func (a *tuiApp) RetryStage(ctx context.Context, id, stage string) error {
	ws, err := a.workspace()
	if err != nil {
		return err
	}
	// A retry re-loads the profile through the same chain as a new run; no
	// explicit spec is given here (the TUI has no profile flag).
	ri := runIntent{
		ResumeID:  run.RunID(id),
		Workspace: ws,
	}
	plan, err := planRun(ctx, ri, a.overrides)
	if err != nil {
		return err
	}
	if !run.StageName(stage).Valid() {
		return fmt.Errorf("unknown stage %q", stage)
	}
	return a.beginCycle(ctx, plan, func() (string, bool, error) {
		res, runErr := resume.RetryStage(a.runCtx, plan.Store, plan.Manifest.ID, run.StageName(stage), plan.Orch.BuildStages(plan.Manifest), resume.Options{})
		if res == nil || res.Manifest == nil {
			return "", false, runErr
		}
		return string(res.Manifest.ID), res.Status == run.RunStatusSucceeded, runErr
	})
}

// workspace returns the run workspace for a new action.
func (a *tuiApp) workspace() (run.Workspace, error) {
	ws := run.Workspace{Name: a.workspaceName, Path: a.workspaceRoot}
	if err := ws.Validate(); err != nil {
		return ws, err
	}
	return ws, nil
}

// beginCycle starts a run cycle (new run, resume, or retry) as the app's
// single in-flight cycle. The work function runs in a goroutine against a
// fresh cancellable context; runDone closes when it reaches a terminal
// state.
func (a *tuiApp) beginCycle(ctx context.Context, plan *runPlan, work func() (string, bool, error)) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.runningID != "" {
		return fmt.Errorf("a run is already in progress (%s): wait for it to finish or cancel it first", a.runningID)
	}
	runCtx, cancel := context.WithCancel(ctx)
	a.runCtx = runCtx
	a.cancelRun = cancel
	a.runDone = make(chan struct{})
	a.runningID = string(plan.Manifest.ID)
	go func() {
		defer close(a.runDone)
		defer cancel()
		id, ok, _ := work()
		a.mu.Lock()
		if a.runningID == id || id == "" {
			a.runningID = ""
		}
		a.mu.Unlock()
		_ = ok
	}()
	return nil
}

// Cancel implements tui.Service.
func (a *tuiApp) Cancel() {
	a.mu.Lock()
	cancel := a.cancelRun
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Done implements tui.Service.
func (a *tuiApp) Done() <-chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.runDone
}

// InFlight implements tui.Service.
func (a *tuiApp) InFlight() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.runningID
}

// waitInFlight cancels the in-flight cycle (if any) and waits for it to
// persist its terminal state, bounded by runCycleGrace.
func (a *tuiApp) waitInFlight() {
	a.mu.Lock()
	cancel, done, id := a.cancelRun, a.runDone, a.runningID
	a.mu.Unlock()
	if id == "" {
		return
	}
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(runCycleGrace):
		fmt.Fprintf(os.Stderr, "warning: run %s was cancelled but did not finish within %s; its state is persisted in the workspace\n", id, runCycleGrace)
	}
}

// sourceModeFromString maps a TUI source-mode label to the run source mode.
func sourceModeFromString(s string) run.SourceMode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case tui.ModeAuto, "discovered", "":
		return run.SourceModeDiscovered
	case tui.ModeExplicit:
		return run.SourceModeExplicit
	case tui.ModeSupplement:
		return run.SourceModeSupplement
	}
	return run.SourceModeDiscovered
}

// doTui opens the terminal interface.
func doTui(cmd *cobra.Command, _ []string) error {
	if !isatty.IsTerminal(os.Stdin.Fd()) || !isatty.IsTerminal(os.Stdout.Fd()) {
		return errors.New("the tui requires an interactive terminal; use `newsroom run` for non-interactive execution")
	}
	app, err := newTuiApp()
	if err != nil {
		return err
	}
	program := tea.NewProgram(&teaAdapter{m: tui.New(app)}, tea.WithAltScreen(), tea.WithContext(cmd.Context()))
	if _, err := program.Run(); err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	// Stop any in-flight run and give the runner a bounded grace period to
	// persist the final (cancelled) state before the process exits.
	app.waitInFlight()
	return nil
}

// ---------------------------------------------------------------------------
// Manifest → DTO mapping (presentation assembly; no pipeline logic)
// ---------------------------------------------------------------------------

// manifestToInfo maps a manifest to the one-line list DTO.
func (a *tuiApp) manifestToInfo(m *run.Manifest) tui.RunInfo {
	profile := ""
	if m.Profile != nil {
		profile = m.Profile.ID
	}
	info := tui.RunInfo{
		ID:         string(m.ID),
		Topic:      m.Topic,
		Status:     string(m.Status),
		SourceMode: string(m.SourceMode),
		Profile:    profile,
		Model:      m.Model.Model,
		StartedAt:  m.StartedAt.Time(),
		UpdatedAt:  m.UpdatedAt.Time(),
		Warnings:   len(m.Warnings),
		Errors:     len(m.Errors),
		Artifacts:  len(m.Artifacts),
	}
	return info
}

// manifestToDetail maps a manifest (and its run directory) to the full
// detail DTO.
func (a *tuiApp) manifestToDetail(m *run.Manifest) (tui.RunDetail, error) {
	d := tui.RunDetail{RunInfo: a.manifestToInfo(m)}
	d.Reason = m.Result.Reason
	if m.Result.FailedStage != nil {
		d.FailedStage = string(*m.Result.FailedStage)
	}

	for _, s := range m.Stages {
		si := tui.StageInfo{
			Name:       string(s.Name),
			Status:     string(s.Status),
			SkipReason: s.SkipReason,
			Attempts:   len(s.Attempts),
		}
		if n := len(s.Attempts); n > 0 {
			last := s.Attempts[n-1]
			si.StartedAt = last.StartedAt.Time()
			if last.FinishedAt != nil {
				si.FinishedAt = last.FinishedAt.Time()
			}
			if last.Error != nil {
				si.Error = errorText(*last.Error)
			}
		}
		d.Stages = append(d.Stages, si)
	}

	for _, w := range m.Warnings {
		d.Warnings = append(d.Warnings, warningText(w))
	}
	for _, e := range m.Errors {
		d.Errors = append(d.Errors, errorText(e))
	}

	if dir, err := a.store.RunDir(m.ID); err == nil {
		for _, art := range m.Artifacts {
			if art.Path == "" {
				continue
			}
			full := filepath.Join(dir, filepath.FromSlash(art.Path))
			ai := tui.ArtifactInfo{Kind: string(art.Kind), Path: full, Size: -1}
			if st, err := os.Stat(full); err == nil {
				ai.Size = st.Size()
			}
			d.Artifacts = append(d.Artifacts, ai)
		}
	}

	// Resumability (read-only assessment of the persisted state).
	if report, err := resume.Analyze(a.store, m.ID); err == nil {
		d.Resumable = report.Resumable
		d.ResumeReason = report.Reason
	} else {
		d.Resumable = false
		d.ResumeReason = err.Error()
	}

	// Summary numbers (stages that were never reached contribute nothing).
	if s, err := orchestrator.BuildSummary(a.store, m); err == nil {
		d.SourcesAttempted = s.SourcesAttempted
		d.SourcesFetched = s.SourcesFetched
		d.Documents = s.DocumentsExtracted
		d.ClaimsTotal = s.ClaimsTotal
		d.ClaimsSupported = s.ClaimsSupported
		d.ClaimsUnresolved = s.ClaimsUnresolved
		d.QualityScore = s.QualityScore
		d.FinalCheck = s.FinalCheck
		d.Findings = s.Findings
	}
	return d, nil
}

// warningText renders one warning for display (code + message).
func warningText(w run.Warning) string {
	if w.Code == "" {
		return w.Message
	}
	return w.Code + ": " + w.Message
}

// errorText renders one error summary for display (code + message).
func errorText(e run.ErrorSummary) string {
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

// teaAdapter adapts tui.Model to the bubbletea tea.Model interface.
//
// The TUI's Update returns the concrete Model (which keeps unit tests
// trivially able to assert on the full state); bubbletea's tea.Model
// interface requires an interface-typed return, so the program receives this
// thin adapter, which re-wraps the Model on every update.
type teaAdapter struct{ m tui.Model }

// Init implements tea.Model.
func (a *teaAdapter) Init() tea.Cmd { return a.m.Init() }

// Update implements tea.Model.
func (a *teaAdapter) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	nm, cmd := a.m.Update(msg)
	return &teaAdapter{m: nm}, cmd
}

// View implements tea.Model.
func (a *teaAdapter) View() string { return a.m.View() }
