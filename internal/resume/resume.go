// Package resume implements resumable and retryable execution of newsroom
// runs on top of the persisted run manifest and stage artifacts.
//
// A run is the unit of recovery: its manifest (internal/run) records every
// stage's status and attempt history, and its artifacts live in the run
// directory (internal/workspace). When a run stops — a stage failed, the run
// was cancelled, or the process died mid-stage — the durable record says
// exactly how far the work got, and this package continues from there:
//
//	Resume(ctx, store, runID, stages, opts)
//	    Continue an interrupted run. Every stage that already finished
//	    (succeeded or skipped) is REUSED — never re-executed — and every stage
//	    that did not finish (failed, cancelled, interrupted, or never started)
//	    runs. A run that already succeeded is a no-op.
//
//	RetryStage(ctx, store, runID, stage, stages, opts)
//	    Explicitly re-run a selected stage and every stage that depends on it
//	    (transitively, per the explicit stage dependency graph in
//	    internal/run). Completed upstream stages are reused; every dependent
//	    downstream stage is invalidated first, so stale downstream output is
//	    never presented as current.
//
// Safety rules:
//
//   - Before resuming, every artifact of every completed stage is verified
//     to exist and be readable in the run directory; a missing or unreadable
//     artifact, or a reference on a stage that did not complete, makes the
//     run unresumable (ErrMissingArtifact / ErrInconsistentRun) and nothing
//     is touched.
//   - A corrupt or invalid manifest cannot be resumed (ErrCorruptManifest).
//   - A stage invalidated for a re-run keeps its previous attempt history
//     (bounded by run.MaxAttemptsPerStage); its stale artifact references are
//     removed from the manifest.
//   - Runs are resumed in place: the same run ID, the same manifest, with
//     the full history preserved. Re-running a job from scratch remains a new
//     run with a new ID.
//
// This package performs no LLM calls and knows nothing about concrete stage
// implementations: executors are injected (runner.Stage) and persistence
// goes through the runner.Store interface (implemented by
// workspace.Store), so tests substitute fakes and temporary workspaces.
// It adds no CLI or TUI behavior.
package resume

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/runner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workspace"
)

// Errors returned by the resume layer.
var (
	// ErrCorruptManifest indicates that the persisted manifest cannot be
	// parsed or fails validation; the run cannot be resumed.
	ErrCorruptManifest = errors.New("resume: run manifest is corrupt or invalid")

	// ErrMissingArtifact indicates that an artifact recorded for a completed
	// stage is absent or unreadable in the run directory; resuming is
	// refused rather than re-deriving upstream work silently.
	ErrMissingArtifact = errors.New("resume: an artifact of a completed stage is missing or unreadable")

	// ErrInconsistentRun indicates a manifest whose stage states and artifact
	// references cannot coexist (e.g. artifacts on a stage that did not
	// complete); resuming is refused.
	ErrInconsistentRun = errors.New("resume: run state is inconsistent")
)

// Options configures a Resume or RetryStage call.
type Options struct {
	// Clock supplies timestamps; it defaults to time.Now (UTC). Tests inject
	// a fixed or advancing clock for deterministic manifests.
	Clock runner.Clock
}

// Report is the outcome of Analyze: a read-only assessment of where a
// persisted run stands and what a resume would do. It never mutates the run.
type Report struct {
	// RunID identifies the assessed run.
	RunID run.RunID

	// RunDir is the absolute path of the run directory.
	RunDir string

	// RunStatus is the run's current lifecycle status.
	RunStatus run.RunStatus

	// Resumable reports whether the run can be resumed or explicitly retried.
	// A succeeded run is resumable (its resume is a no-op). When false,
	// Reason explains why.
	Resumable bool

	// Reason is a human-readable explanation of why the run is NOT resumable
	// ("" when Resumable).
	Reason string

	// Complete lists the stages that finished successfully with intact
	// artifacts (in plan order).
	Complete []run.StageName

	// Skipped lists the stages that were deliberately skipped (in plan
	// order).
	Skipped []run.StageName

	// ToRun lists the stages a Resume would execute (failed, cancelled,
	// interrupted, or never started — in plan order).
	ToRun []run.StageName
}

// Result summarizes a Resume or RetryStage cycle.
type Result struct {
	// RunID identifies the run.
	RunID run.RunID

	// RunDir is the absolute path of the run directory.
	RunDir string

	// Manifest is the final manifest state (the in-memory object persisted at
	// the end of the cycle).
	Manifest *run.Manifest

	// Status is the run status after the cycle (succeeded, failed, or
	// cancelled).
	Status run.RunStatus

	// Reused lists the stages whose previous cycle's output was kept (they
	// were succeeded or skipped and were not re-executed), in plan order.
	Reused []run.StageName

	// Executed lists the stages handed to the runner in this cycle, in plan
	// order (they succeeded, failed, or were skipped again depending on the
	// executors they were given).
	Executed []run.StageName

	// Err is nil when the run completed successfully; otherwise it explains
	// why the cycle stopped. The durable record of the failure is the
	// manifest on disk, and the run remains resumable again.
	Err error
}

// Analyze inspects a persisted run and reports which stages are complete and
// which a resume would run, without changing anything.
//
// It is the read-only half of "given a run ID, determine which stages are
// complete and which can be resumed". The assessment is conservative: if
// any artifact of a completed stage cannot be read back, the run is reported
// as not resumable (a resume would either reuse stale output or re-derive
// upstream work without knowing).
//
// Errors and reports are distinguished: an unreadable or invalid manifest is
// a hard error (ErrCorruptManifest, or the store's ErrRunNotFound); a
// readable run whose artifacts are not intact is reported as Resumable=false
// with a Reason, since its stage states are still trustworthy for display.
func Analyze(store runner.Store, id run.RunID) (*Report, error) {
	if store == nil {
		return nil, errors.New("resume: store is required")
	}
	m, dir, err := store.LoadManifest(id)
	if err != nil {
		if !errors.Is(err, workspace.ErrRunNotFound) {
			err = fmt.Errorf("%w: %v", ErrCorruptManifest, err)
		}
		return nil, err
	}
	rep := &Report{
		RunID:     id,
		RunDir:    dir,
		RunStatus: m.Status,
		Resumable: true,
	}
	for _, s := range m.Stages {
		switch s.Status {
		case run.StageStatusSucceeded:
			// A completed stage only counts as complete if its artifacts
			// survive a read-back; otherwise the run is unresumable.
			if problem := stageArtifactProblem(m, dir, s.Name); problem == nil {
				rep.Complete = append(rep.Complete, s.Name)
			} else {
				rep.Resumable = false
				rep.Reason = problem.Error()
			}
		case run.StageStatusSkipped:
			rep.Skipped = append(rep.Skipped, s.Name)
		default:
			rep.ToRun = append(rep.ToRun, s.Name)
		}
	}
	if rep.Resumable {
		if problem := checkArtifacts(m, dir); problem != nil {
			rep.Resumable = false
			rep.Reason = problem.Error()
		}
	}
	return rep, nil
}

// Resume continues a failed, cancelled, or interrupted (crashed) run from its
// persisted state, executing every stage that has not yet finished and
// reusing every stage that did.
//
// The stage configuration must cover the run's whole plan (one runner.Stage
// per planned stage, in plan order): executors are needed for every stage
// that will run, and a skip reason for every stage that should be skipped
// again (e.g. a follow-up stage the budget rules out).
//
// Behavior by current run state:
//
//   - succeeded: no-op — the result reports the run as succeeded, no stage is
//     executed, and the manifest on disk is left byte-identical.
//   - failed / cancelled: the run is reopened (Manifest.Reopen), and every
//     stage that did not finish in the previous cycle is executed in plan
//     order; every completed stage is validated and reused.
//   - running (a process died mid-stage): the dangling running stage is
//     interrupted, and the run continues from there.
//   - pending (a process died before the run started): the run starts.
//
// When the run stops again (a retried stage failed, a cancellation was
// requested, ...), the manifest on disk records the new state, and the run
// can be resumed once more.
func Resume(ctx context.Context, store runner.Store, id run.RunID, stages []runner.Stage, opts Options) (*Result, error) {
	return execute(ctx, store, id, stages, opts, "")
}

// RetryStage explicitly re-runs the given stage and every stage that depends
// on it (transitively, per the explicit stage dependency graph), from a run
// in ANY terminal state — including a fully succeeded one.
//
// The target and every dependent downstream stage are invalidated before the
// cycle starts: their previous outputs are stale by definition, their
// statuses reset to pending, and their artifact references removed from the
// manifest (their attempt history is preserved). Completed stages that are
// NOT downstream of the target are reused without re-execution.
//
// The stage must be part of the run's plan. The stage configuration rules
// are the same as for Resume.
func RetryStage(ctx context.Context, store runner.Store, id run.RunID, stage run.StageName, stages []runner.Stage, opts Options) (*Result, error) {
	if !stage.Valid() {
		return nil, fmt.Errorf("resume: unknown stage %q", stage)
	}
	return execute(ctx, store, id, stages, opts, stage)
}

// execute is the shared implementation of Resume and RetryStage.
//
// retryTarget is the stage to invalidate-and-rerun ("" for Resume: no
// explicit origin, completed stages are simply reused).
func execute(ctx context.Context, store runner.Store, id run.RunID, stages []runner.Stage, opts Options, retryTarget run.StageName) (*Result, error) {
	if store == nil {
		return nil, errors.New("resume: store is required")
	}
	if opts.Clock == nil {
		opts.Clock = func() time.Time { return time.Now().UTC() }
	}

	m, runDir, err := loadAndVerify(store, id)
	if err != nil {
		return nil, err
	}
	if err := runner.ValidateStageConfig(m, stages); err != nil {
		return nil, fmt.Errorf("resume %s: %w", id, err)
	}

	res := &Result{RunID: id, RunDir: runDir, Manifest: m}

	// A resume of a run that already succeeded is a no-op: nothing is
	// re-executed, nothing is reopened, and the manifest on disk is not
	// touched. (An explicit retry of a stage on a succeeded run is NOT a
	// no-op: it reopens the run and invalidates the target's downstream.)
	// Any non-succeeded run — including a degenerate failed run whose plan is
	// already fully complete — has work to do: it is reopened and, if every
	// stage is already terminal, simply completed.
	if retryTarget == "" && m.Status == run.RunStatusSucceeded {
		res.Status = m.Status
		res.Reused = allStageNames(m)
		return res, nil
	}

	// Prepare the manifest for the cycle.
	now := opts.Clock()

	// Terminal runs must be reactivated; a reopened run keeps its full
	// history and gets a running status the runner can work on.
	if m.Status.Terminal() {
		if err := m.Reopen(now); err != nil {
			res.Err = fmt.Errorf("resume %s: %w", id, err)
			return res, res.Err
		}
	}

	// A retry origin invalidates the target and every dependent downstream
	// stage (stale outputs must not survive the re-run).
	if retryTarget != "" {
		if err := m.InvalidateStage(retryTarget, now); err != nil {
			res.Err = fmt.Errorf("resume %s: invalidating %s: %w", id, retryTarget, err)
			return res, res.Err
		}
	}

	// A process that died mid-stage leaves a dangling running attempt. End it
	// (as cancelled) before anything else: the resume retries it, and no
	// persisted manifest ever claims a stage is running when it is not.
	for i := range m.Stages {
		if m.Stages[i].Status != run.StageStatusRunning {
			continue
		}
		if err := m.CancelStage(m.Stages[i].Name, now); err != nil {
			res.Err = fmt.Errorf("resume %s: interrupting %s: %w", id, m.Stages[i].Name, err)
			return res, res.Err
		}
	}

	// Degenerate but recoverable: the run is terminal, yet every stage is
	// already complete (a process died between the last stage succeeding
	// and the run being recorded as such). With nothing left to do, the
	// reopened run is completed in place instead of being left stranded.
	nothingLeft := true
	for i := range m.Stages {
		if m.Stages[i].Status != run.StageStatusSucceeded && m.Stages[i].Status != run.StageStatusSkipped {
			nothingLeft = false
			break
		}
	}
	// (Only a reopened run can reach this point with every stage terminal:
	// pending runs have pending stages, and a persisted running run must hold
	// a pending or running stage, or it would have failed validation on load.)
	if nothingLeft {
		if err := m.Succeed(now); err != nil {
			res.Err = fmt.Errorf("resume %s: %w", id, err)
			return res, res.Err
		}
		if err := persistPrepared(store, m); err != nil {
			res.Err = fmt.Errorf("resume %s: %w", id, err)
			return res, res.Err
		}
		res.Status = m.Status
		res.Reused = allStageNames(m)
		return res, nil
	}

	if err := persistPrepared(store, m); err != nil {
		res.Err = fmt.Errorf("resume %s: %w", id, err)
		return res, res.Err
	}

	// What this cycle does: everything not already finished.
	for i := range m.Stages {
		switch m.Stages[i].Status {
		case run.StageStatusSucceeded, run.StageStatusSkipped:
			res.Reused = append(res.Reused, m.Stages[i].Name)
		default:
			res.Executed = append(res.Executed, m.Stages[i].Name)
		}
	}

	// Execute: the runner in resume mode skips exactly the stages the report
	// says were reused, and reuses the same manifest and run directory.
	// Execute: the runner in resume mode skips exactly the stages the report
	// says were reused, and reuses the same manifest and run directory.
	_, rerr := runner.Run(ctx, runner.Config{
		Manifest: m,
		Store:    store,
		Stages:   stages,
		Clock:    opts.Clock,
		Resume:   true,
	})
	res.Manifest = m
	res.Status = m.Status
	res.Err = rerr
	return res, rerr
}

// allStageNames returns the planned stage names in plan order.
func allStageNames(m *run.Manifest) []run.StageName {
	names := make([]run.StageName, len(m.Stages))
	for i, s := range m.Stages {
		names[i] = s.Name
	}
	return names
}

// loadAndVerify loads the persisted manifest of a run and verifies the
// integrity of its artifacts, so that a resume never builds on stale or
// missing upstream output.
//
// The checks:
//
//   - the manifest must parse and pass full validation (a torn or invalid
//     document yields ErrCorruptManifest; a missing run yields the store's
//     ErrRunNotFound);
//   - every artifact reference must name a stage that succeeded: a stage
//     that did not complete must not claim artifacts (ErrInconsistentRun);
//   - every artifact reference of a succeeded stage must have a path and the
//     file must exist and be readable (ErrMissingArtifact). An unreferenceable
//     (pathless) artifact cannot be verified and therefore blocks the resume
//     (fail closed).
func loadAndVerify(store runner.Store, id run.RunID) (*run.Manifest, string, error) {
	m, dir, err := store.LoadManifest(id)
	if err != nil {
		if errors.Is(err, workspace.ErrRunNotFound) {
			return nil, "", err
		}
		return nil, "", fmt.Errorf("%w: %v", ErrCorruptManifest, err)
	}
	if problem := checkArtifacts(m, dir); problem != nil {
		return nil, "", problem
	}
	return m, dir, nil
}

// checkArtifacts returns a descriptive error (wrapping ErrMissingArtifact or
// ErrInconsistentRun) when the manifest's artifacts are not intact, or nil
// when everything a completed stage recorded is present and readable.
func checkArtifacts(m *run.Manifest, dir string) error {
	status := make(map[run.StageName]run.StageStatus, len(m.Stages))
	for _, s := range m.Stages {
		status[s.Name] = s.Status
	}
	for _, a := range m.Artifacts {
		if a.Stage != "" {
			if _, planned := status[a.Stage]; !planned {
				return fmt.Errorf("%w: artifact %q references stage %q which is not part of the run", ErrInconsistentRun, a.ID, a.Stage)
			}
		}
	}
	for _, s := range m.Stages {
		if problem := stageArtifactProblem(m, dir, s.Name); problem != nil {
			return problem
		}
	}
	// Artifacts without a stage are verified on their own.
	for _, a := range m.Artifacts {
		if a.Stage != "" {
			continue
		}
		if a.Path == "" {
			return fmt.Errorf("%w: artifact %q has no stored path; its content cannot be verified", ErrMissingArtifact, a.ID)
		}
		info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(a.Path)))
		if err != nil || info.IsDir() {
			return fmt.Errorf("%w: artifact %q is not readable at %s (%v)", ErrMissingArtifact, a.ID, a.Path, err)
		}
	}
	return nil
}

// stageArtifactProblem reports the first integrity problem with the artifacts
// recorded for a stage, or nil when they are intact. A stage that did not
// succeed must not hold artifacts at all; such a reference is a problem.
func stageArtifactProblem(m *run.Manifest, dir string, stage run.StageName) error {
	st, _ := stageStatusOf(m, stage)
	for _, a := range m.Artifacts {
		if a.Stage != stage {
			continue
		}
		if st != run.StageStatusSucceeded {
			return fmt.Errorf("%w: artifact %q is recorded for stage %q which is %s, not succeeded", ErrInconsistentRun, a.ID, stage, st)
		}
		if a.Path == "" {
			return fmt.Errorf("%w: artifact %q of stage %q has no stored path; its content cannot be verified", ErrMissingArtifact, a.ID, stage)
		}
		info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(a.Path)))
		if err != nil || info.IsDir() {
			return fmt.Errorf("%w: artifact %q of stage %q is not readable at %s", ErrMissingArtifact, a.ID, stage, a.Path)
		}
	}
	return nil
}

// stageStatusOf returns the status of a planned stage ("" when absent).
func stageStatusOf(m *run.Manifest, stage run.StageName) (run.StageStatus, bool) {
	for _, s := range m.Stages {
		if s.Name == stage {
			return s.Status, true
		}
	}
	return "", false
}

// persistPrepared saves the prepared manifest before the cycle starts, so the
// reopened/invalidated state is durable even if the process dies before the
// first stage runs.
func persistPrepared(store runner.Store, m *run.Manifest) error {
	if _, err := store.SaveManifest(m); err != nil {
		return fmt.Errorf("persisting prepared manifest: %w", err)
	}
	return nil
}
