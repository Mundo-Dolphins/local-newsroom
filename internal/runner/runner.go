// Package runner executes a newsroom run end-to-end, persisting every state
// change and stage output through a workspace.Store.
//
// The runner is the bridge between the run manifest contract (internal/run)
// and filesystem persistence (internal/workspace). Stages are injected as
// plain functions, so the runner is fully testable offline and independent
// of any concrete stage implementation, the CLI, or a TUI.
//
// Execution protocol:
//
//  1. the run directory is created and the initial manifest persisted
//     (workspace.Store.CreateRun);
//  2. the run is started and the manifest re-persisted;
//  3. each planned stage, in manifest order:
//     - skipped stages are recorded with their reason and the manifest is
//     saved;
//     - the stage is marked running (new attempt) and the manifest is saved;
//     - the stage executes; its output files are persisted as artifacts
//     inside the run directory and referenced in the manifest;
//     - the stage is marked succeeded (the final planned stage also completes
//     the run in the same snapshot) or failed, and the manifest is saved;
//  4. the run is marked succeeded, failed, or cancelled and the manifest is
//     saved as the final durable record.
//
// Because every persistence step is atomic in the store, the manifest on disk
// always reflects the last *completed* state change: a run whose process is
// terminated mid-execution is left readable, at exactly the state its last
// save captured.
//
// Resume mode (Config.Resume): the same Run entry point can continue an
// already-started run instead of creating a new one. The run directory and
// manifest already exist (claimed by the original run), a terminal run must
// have been reactivated (Manifest.Reopen) by the caller first, and stages
// that already finished (succeeded or skipped) are not re-executed — the
// expensive work of a previous cycle is reused. The resume layer
// (internal/resume) loads and validates the persisted state, invalidates
// what an upstream retry makes stale, and hands the prepared manifest to the
// runner in this mode.
package runner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// Clock returns the current time. Tests inject a fixed or advancing clock for
// deterministic manifests.
type Clock func() time.Time

// StageFunc executes one pipeline stage.
type StageFunc func(ctx context.Context, in *StageInput) (*StageOutput, error)

// StageInput carries what a stage execution can access.
type StageInput struct {
	// RunID identifies the executing run.
	RunID run.RunID

	// Stage is the stage being executed.
	Stage run.StageName

	// Manifest is the live run manifest. Stages may read it (topic,
	// configuration, plan) but must not mutate it: the runner owns every
	// manifest state change and each change is persisted immediately.
	Manifest *run.Manifest

	// Store is the workspace store; stages may use it to read earlier stage
	// artifacts (e.g. a dossier consumed by the verify stage) via
	// Store.LoadArtifact.
	Store Store

	// RunDir is the absolute path of the run directory.
	RunDir string
}

// StageFile is one output file produced by a stage.
type StageFile struct {
	// Path is the file path relative to the run directory (e.g.
	// "dossier.json" or "output/article.md").
	Path string

	// Data is the file content.
	Data []byte

	// Kind classifies the artifact in the run manifest.
	Kind run.ArtifactKind

	// ID is the stable artifact ID recorded in the manifest.
	ID string
}

// Warning is a non-fatal note produced by a stage; it is recorded on the
// manifest (redacted) without failing the stage.
type Warning struct {
	Code    string
	Message string
}

// StageOutput is what a stage execution produced.
//
// A nil *StageOutput is a valid, empty output (the stage produced no files).
type StageOutput struct {
	// Files are the outputs to persist as artifacts inside the run directory.
	Files []StageFile

	// Warnings are non-fatal notes to record on the manifest.
	Warnings []Warning
}

// Stage pairs a planned stage with its executor.
type Stage struct {
	// Name is the canonical stage name (must match the manifest plan).
	Name run.StageName

	// Fn executes the stage. When Fn is nil the stage is skipped.
	Fn StageFunc

	// SkipReason is recorded when the stage is skipped. It is required when
	// Fn is nil and ignored when Fn is non-nil.
	SkipReason string
}

// Store is the persistence boundary the runner requires from the workspace.
//
// The concrete implementation is workspace.Store (the filesystem store); the
// runner depends only on this interface, so tests can substitute fakes and
// fault-injecting wrappers (a failing disk, a broken permission) without
// touching the real store. Sanitizer is part of the contract because the
// runner sanitizes every failure message before it is recorded.
type Store interface {
	// CreateRun claims the run directory and persists the initial manifest.
	CreateRun(m *run.Manifest) (string, error)
	// SaveManifest persists the manifest and returns its path.
	SaveManifest(m *run.Manifest) (string, error)
	// LoadManifest loads a persisted manifest and its run directory.
	LoadManifest(id run.RunID) (*run.Manifest, string, error)
	// RunDir returns the run directory for an ID.
	RunDir(id run.RunID) (string, error)
	// SaveArtifact persists one stage output file inside the run directory.
	SaveArtifact(id run.RunID, ref run.ArtifactRef, data []byte) (string, error)
	// LoadArtifact reads a persisted artifact back (stages read earlier
	// stage outputs through it).
	LoadArtifact(id run.RunID, relPath string) ([]byte, error)
	// Sanitizer is the byte-level sanitization policy of the store.
	Sanitizer() *run.Sanitizer
}

// Config configures one run.
type Config struct {
	// Manifest is the run manifest. Its stage plan is the authoritative
	// order: Config.Stages must cover the planned stages exactly, in order.
	Manifest *run.Manifest

	// Store is the workspace the run is persisted into.
	Store Store

	// Stages pairs each planned stage with its executor (or skip reason).
	Stages []Stage

	// Clock supplies timestamps; it defaults to time.Now (UTC).
	Clock Clock

	// Resume selects resume mode for an already-started run:
	//
	//   - the run directory is NOT re-claimed (CreateRun is skipped; the
	//     directory must already exist);
	//   - a run that is still pending is started as usual; a run that is
	//     already running is taken over in place (the manifest was persisted
	//     by the resume layer before Run was called);
	//   - stages whose current status is succeeded or skipped are NOT
	//     re-executed: their output from the previous cycle is reused;
	//   - a stage left running by a crashed cycle is executed against its
	//     dangling attempt instead of starting a second one;
	//   - a terminal manifest is refused: terminal runs must be reopened
	//     (Manifest.Reopen) and stale stages invalidated
	//     (Manifest.InvalidateStage) by the resume layer before calling Run.
	//
	// After the stage loop, if the run is still running (every stage ended
	// up succeeded or skipped, e.g. because the final planned stage was
	// already complete), the run is completed in the same snapshot as a
	// fresh run.
	Resume bool
}

// Result summarizes a finished run, including failed and cancelled ones.
type Result struct {
	// RunID identifies the run.
	RunID run.RunID

	// RunDir is the absolute path of the run directory ("" when the run
	// directory could not be created).
	RunDir string

	// Manifest is the final manifest state.
	Manifest *run.Manifest

	// Status is the terminal run status (succeeded, failed, or cancelled).
	// Zero when the run could not be started at all.
	Status run.RunStatus

	// Err is nil when the run succeeded; otherwise it explains why the run
	// stopped. The durable record of the failure is the manifest on disk.
	Err error
}

// Run executes the configured run to completion (success, failure, or
// cancellation), persisting the manifest after every state change and every
// stage output.
//
// It always returns a *Result describing the terminal state. The returned
// error is nil only for a successful run; for failed or cancelled runs the
// error explains the stop, and the run directory contains the final manifest
// plus every artifact produced before the stop.
func Run(ctx context.Context, cfg Config) (*Result, error) {
	if cfg.Manifest == nil {
		return nil, errors.New("runner: manifest is required")
	}
	if cfg.Store == nil {
		return nil, errors.New("runner: store is required")
	}
	if cfg.Clock == nil {
		cfg.Clock = func() time.Time { return time.Now().UTC() }
	}

	m := cfg.Manifest
	res := &Result{RunID: m.ID, Manifest: m}

	if err := m.Validate(); err != nil {
		res.Err = err
		return res, fmt.Errorf("runner: invalid manifest: %w", err)
	}
	if err := ValidateStageConfig(m, cfg.Stages); err != nil {
		res.Err = err
		return res, err
	}
	if cfg.Resume && m.Status.Terminal() {
		res.Status = m.Status
		res.Err = fmt.Errorf("runner: cannot resume a terminal run in %s state; reopen it (Manifest.Reopen) and invalidate stale stages first", m.Status)
		return res, res.Err
	}

	runDir := ""
	var err error
	if cfg.Resume {
		// The run directory was claimed by the original run; resume reuses
		// it and its persisted manifest.
		runDir, err = cfg.Store.RunDir(m.ID)
		if err != nil {
			res.Err = err
			return res, fmt.Errorf("runner: resuming run: %w", err)
		}
		if m.Status == run.RunStatusPending {
			// A run that was claimed but never started (a crash between the
			// manifest write and the start) starts like a fresh run.
			if err := m.Start(cfg.Clock()); err != nil {
				res.Status = m.Status
				res.Err = err
				return res, fmt.Errorf("runner: starting resumed run: %w", err)
			}
			if err := persistManifest(cfg); err != nil {
				return failRun(cfg, res, "run_error", err)
			}
		}
	} else {
		runDir, err = cfg.Store.CreateRun(m)
		if err != nil {
			res.Err = err
			return res, fmt.Errorf("runner: creating run: %w", err)
		}
		if err := m.Start(cfg.Clock()); err != nil {
			res.Status = m.Status
			res.Err = err
			return res, fmt.Errorf("runner: starting run: %w", err)
		}
		if err := persistManifest(cfg); err != nil {
			return failRun(cfg, res, "run_error", err)
		}
	}
	res.RunDir = runDir

	for i := range cfg.Stages {
		st := &cfg.Stages[i]
		isLast := i == len(cfg.Stages)-1

		// A cancelled context stops the run before the stage starts.
		if ctx.Err() != nil {
			return cancelRun(ctx, cfg, res, run.StageName(""))
		}

		if cfg.Resume {
			status, err := m.StageStatusOf(st.Name)
			if err != nil {
				return failRun(cfg, res, "run_error", err)
			}
			switch status {
			case run.StageStatusSucceeded, run.StageStatusSkipped:
				// Completed in a previous cycle: reuse the persisted output
				// and do not re-execute (the rule that keeps resumed runs
				// from paying twice for expensive stages).
				continue
			case run.StageStatusRunning:
				// A dangling attempt left by a crashed cycle: execute against
				// the existing attempt instead of starting a second one.
				out, err := executeStage(ctx, cfg, st, runDir)
				if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return cancelRun(ctx, cfg, res, st.Name)
				}
				if err != nil {
					return failRun(cfg, res, classifyCode(err), err)
				}
				if err := persistStageOutput(cfg, st, out, isLast); err != nil {
					return failRun(cfg, res, classifyCode(err), err)
				}
				continue
			}
		}

		if st.Fn == nil {
			if err := m.SkipStage(st.Name, cfg.Clock(), st.SkipReason); err != nil {
				return failRun(cfg, res, "run_error", err)
			}
			// The manifest model has no "running" state in which every stage
			// is terminal: complete the run in the same persisted snapshot.
			if isLast {
				if err := m.Succeed(cfg.Clock()); err != nil {
					return failRun(cfg, res, "run_error", err)
				}
			}
			if err := persistManifest(cfg); err != nil {
				return failRun(cfg, res, "run_error", err)
			}
			continue
		}

		if err := startStage(cfg, st); err != nil {
			return failRun(cfg, res, "stage_start_failed", err)
		}
		if ctx.Err() != nil {
			return cancelRun(ctx, cfg, res, st.Name)
		}

		out, err := executeStage(ctx, cfg, st, runDir)
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return cancelRun(ctx, cfg, res, st.Name)
		}
		if err != nil {
			return failRun(cfg, res, classifyCode(err), err)
		}
		if err := persistStageOutput(cfg, st, out, isLast); err != nil {
			return failRun(cfg, res, classifyCode(err), err)
		}
	}

	// An empty stage plan completes the run as soon as it starts.
	if len(cfg.Stages) == 0 {
		if err := m.Succeed(cfg.Clock()); err != nil {
			return failRun(cfg, res, "run_error", err)
		}
		if err := persistManifest(cfg); err != nil {
			return failRun(cfg, res, "run_error", err)
		}
	}
	// Resume: if every planned stage ended up succeeded or skipped (in
	// particular when the final planned stage was already complete before
	// this cycle), complete the run now, as a fresh run would have.
	if cfg.Resume && m.Status == run.RunStatusRunning {
		if err := m.Succeed(cfg.Clock()); err != nil {
			return failRun(cfg, res, "run_error", err)
		}
		if err := persistManifest(cfg); err != nil {
			return failRun(cfg, res, "run_error", err)
		}
	}
	res.Status = m.Status
	return res, nil
}

// startStage records the stage attempt on the manifest and persists it before
// the stage executes, so a crash mid-stage leaves the stage as "running".
func startStage(cfg Config, st *Stage) (err error) {
	if err := cfg.Manifest.StartStage(st.Name, cfg.Clock()); err != nil {
		return fmt.Errorf("starting stage %s: %w", st.Name, err)
	}
	if err := persistManifest(cfg); err != nil {
		return fmt.Errorf("persisting stage start %s: %w", st.Name, err)
	}
	return nil
}

// executeStage calls the stage function, converting a panic into an error so
// a misbehaving stage cannot take down the process without leaving a failed
// run behind.
func executeStage(ctx context.Context, cfg Config, st *Stage, runDir string) (out *StageOutput, err error) {
	defer func() {
		if r := recover(); r != nil {
			out = nil
			err = fmt.Errorf("stage %s panicked: %v", st.Name, r)
		}
	}()
	return st.Fn(ctx, &StageInput{
		RunID:    cfg.Manifest.ID,
		Stage:    st.Name,
		Manifest: cfg.Manifest,
		Store:    cfg.Store,
		RunDir:   runDir,
	})
}

// persistStageOutput stores the stage's files as artifacts, records them (and
// any warnings) on the manifest, marks the stage succeeded, and saves the
// manifest. When isLast is set (the final planned stage), the run itself is
// marked succeeded in the same persisted snapshot: the manifest model has no
// "running" state in which every stage is terminal.
func persistStageOutput(cfg Config, st *Stage, out *StageOutput, isLast bool) error {
	m := cfg.Manifest
	now := cfg.Clock()

	if out != nil {
		for i := range out.Warnings {
			w := out.Warnings[i]
			if w.Message == "" {
				continue
			}
			// Sanitize stage-provided warning text the same way as failure
			// messages before it reaches the manifest.
			w.Message, _ = cfg.Store.Sanitizer().Sanitize(w.Message)
			if w.Message == "" {
				continue
			}
			if err := m.AddWarning(w.Code, w.Message, now); err != nil {
				return fmt.Errorf("recording warning for stage %s: %w", st.Name, err)
			}
		}
		for i := range out.Files {
			f := out.Files[i]
			ref := run.ArtifactRef{
				Kind:       f.Kind,
				ID:         f.ID,
				Stage:      st.Name,
				Path:       f.Path,
				ProducedAt: run.NewTimestamp(now),
			}
			rel, err := cfg.Store.SaveArtifact(m.ID, ref, f.Data)
			if err != nil {
				return fmt.Errorf("persisting artifact %s for stage %s: %w", f.Path, st.Name, err)
			}
			ref.Path = rel
			if err := m.AddArtifact(ref); err != nil {
				return fmt.Errorf("recording artifact %s for stage %s: %w", f.ID, st.Name, err)
			}
		}
	}
	if err := m.SucceedStage(st.Name, now); err != nil {
		return fmt.Errorf("completing stage %s: %w", st.Name, err)
	}
	if isLast {
		if err := m.Succeed(now); err != nil {
			return fmt.Errorf("completing run: %w", err)
		}
	}
	return persistManifest(cfg)
}

// persistManifest atomically saves the current manifest state.
func persistManifest(cfg Config) error {
	if _, err := cfg.Store.SaveManifest(cfg.Manifest); err != nil {
		return fmt.Errorf("saving manifest: %w", err)
	}
	return nil
}

// failRun terminates the run with a blocking failure (any stage still
// running is failed with the same summary, per the run contract), and
// persists the final manifest.
func failRun(cfg Config, res *Result, code string, err error) (*Result, error) {
	m := cfg.Manifest
	now := cfg.Clock()
	// Defense in depth: the failure message is sanitized with the store's
	// sanitizer (exact configured values plus pattern detectors) before it
	// is recorded or reported, so a stage error that quotes a credential
	// never reaches the manifest or the returned error unredacted. The
	// store's byte-level gate is the fail-closed backstop.
	message, _ := cfg.Store.Sanitizer().Sanitize(run.RedactSecrets(err.Error()))

	if err := m.Fail(now, code, message); err != nil {
		// The run is not in a state that can fail (e.g. already terminal
		// from an earlier step). Persist whatever state exists and report.
		_ = persistManifest(cfg)
		res.Status = m.Status
		res.Err = fmt.Errorf("runner: cannot record run failure: %w", err)
		return res, res.Err
	}
	_ = persistManifest(cfg)
	res.Status = m.Status
	res.Err = fmt.Errorf("%s: %s", code, message)
	return res, res.Err
}

// cancelRun cancels the run (and the stage, when one is mid-flight) and
// persists the final manifest.
func cancelRun(ctx context.Context, cfg Config, res *Result, stageName run.StageName) (*Result, error) {
	m := cfg.Manifest
	now := cfg.Clock()

	if stageName != "" {
		_ = m.CancelStage(stageName, now)
	}
	reason := "run cancelled"
	if ctx.Err() != nil {
		reason = "run cancelled: " + ctx.Err().Error()
	}
	reason, _ = cfg.Store.Sanitizer().Sanitize(reason)
	if err := m.Cancel(now, reason); err != nil {
		_ = persistManifest(cfg)
		res.Status = m.Status
		res.Err = fmt.Errorf("runner: cannot record run cancellation: %w", err)
		return res, res.Err
	}
	_ = persistManifest(cfg)
	res.Status = m.Status
	res.Err = ctx.Err()
	if res.Err == nil {
		res.Err = errors.New("run cancelled")
	}
	return res, res.Err
}

// ValidateStageConfig checks that the stage executors line up with the
// manifest's stage plan: one entry per planned stage, in plan order, with an
// executor or a skip reason.
func ValidateStageConfig(m *run.Manifest, stages []Stage) error {
	if len(stages) != len(m.Stages) {
		return fmt.Errorf("runner: expected %d stage executors (one per planned stage), got %d", len(m.Stages), len(stages))
	}
	for i, st := range stages {
		if !st.Name.Valid() {
			return fmt.Errorf("runner: stage %d has invalid name %q", i, st.Name)
		}
		if st.Name != m.Stages[i].Name {
			return fmt.Errorf("runner: stage %d is %q but the manifest plans %q at that position", i, st.Name, m.Stages[i].Name)
		}
		if st.Fn == nil && st.SkipReason == "" {
			return fmt.Errorf("runner: stage %q has no executor and no skip reason", st.Name)
		}
	}
	return nil
}

// classifyCode maps an error to a stable manifest error code.
func classifyCode(err error) string {
	if err == nil {
		return "stage_failed"
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "context_cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "context_deadline"
	default:
		return "stage_failed"
	}
}
