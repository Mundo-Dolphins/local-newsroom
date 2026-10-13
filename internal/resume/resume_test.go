package resume_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Mundo-Dolphins/local-newsroom/internal/resume"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/runner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workspace"
)

// baseTime is the fixed timestamp for deterministic run IDs.
var baseTime = time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)

// corePlan is the canonical editorial chain from the v0.5 pipeline:
// research -> verify -> write -> final_check -> render.
var corePlan = []run.StageName{run.StageResearch, run.StageVerify, run.StageWrite, run.StageFinalCheck, run.StageRender}

// newAdvancingClock returns a clock whose calls advance by one second each.
// The same instance must be shared across every execution cycle of one run
// (fresh run, resume, retries): persisted manifest timestamps may never
// regress, and the fixed history guarantees no clock skew.
func newAdvancingClock(start time.Time) runner.Clock {
	t := start
	return func() time.Time {
		t = t.Add(time.Second)
		return t
	}
}

// fakeStage builds an offline stage executor with a call counter and
// deterministic artifact output.
type fakeStage struct {
	name  run.StageName
	calls *int
	path  string
	kind  run.ArtifactKind
	id    string
}

func (f fakeStage) stage() runner.Stage {
	return runner.Stage{
		Name: f.name,
		Fn: func(ctx context.Context, in *runner.StageInput) (*runner.StageOutput, error) {
			*f.calls++
			body := `{"stage":"` + f.name.String() + `","call":` + itoa(*f.calls) + `}`
			return &runner.StageOutput{
				Files: []runner.StageFile{{Path: f.path, Kind: f.kind, ID: f.id, Data: []byte(body)}},
			}, nil
		},
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

// flakyOnce wraps a stage so its first execution fails and later ones succeed.
// Failures increment the same counter, so tests can assert exactly how many
// times a stage ran in total.
func flakyOnce(name run.StageName, calls *int, path string, kind run.ArtifactKind, id string) runner.Stage {
	return runner.Stage{
		Name: name,
		Fn: func(ctx context.Context, in *runner.StageInput) (*runner.StageOutput, error) {
			*calls++
			if *calls == 1 {
				return nil, errors.New("simulated transient failure")
			}
			body := `{"stage":"` + name.String() + `","call":` + itoa(*calls) + `}`
			return &runner.StageOutput{
				Files: []runner.StageFile{{Path: path, Kind: kind, ID: id, Data: []byte(body)}},
			}, nil
		},
	}
}

// alwaysFailing returns a stage whose executions always fail.
func alwaysFailing(name run.StageName, calls *int) runner.Stage {
	return runner.Stage{
		Name: name,
		Fn: func(ctx context.Context, in *runner.StageInput) (*runner.StageOutput, error) {
			*calls++
			return nil, errors.New("simulated persistent failure")
		},
	}
}

// newStore builds a workspace store in a temp directory.
func newStore(t *testing.T) *workspace.Store {
	t.Helper()
	store, err := workspace.NewStore(t.TempDir())
	require.NoError(t, err)
	return store
}

// newManifest builds a pending manifest with a deterministic ID and the given
// plan (the core chain by default).
func newManifest(t *testing.T, plan ...run.StageName) *run.Manifest {
	t.Helper()
	if len(plan) == 0 {
		plan = corePlan
	}
	m, err := run.NewManifest(run.NewManifestParams{
		Workspace: run.Workspace{Name: "testws"},
		Topic:     "EU AI Act enters into force",
		Stages:    plan,
		StartedAt: baseTime,
	})
	require.NoError(t, err)
	return m
}

// newStages builds the five core stages with fresh call counters.
func newStages(t *testing.T) (stages []runner.Stage, calls map[run.StageName]*int) {
	t.Helper()
	calls = map[run.StageName]*int{}
	defs := []struct {
		name run.StageName
		path string
		kind run.ArtifactKind
		id   string
	}{
		{run.StageResearch, "dossier.json", run.ArtifactKindDossier, "dossier"},
		{run.StageVerify, "verification.json", run.ArtifactKindVerification, "verification"},
		{run.StageWrite, "artifact.json", run.ArtifactKindEditorial, "editorial"},
		{run.StageFinalCheck, "final-check.json", run.ArtifactKindFactualCheck, "final-check"},
		{run.StageRender, "output/article.md", run.ArtifactKindRendered, "article"},
	}
	for _, d := range defs {
		c := 0
		calls[d.name] = &c
		stages = append(stages, fakeStage{name: d.name, calls: calls[d.name], path: d.path, kind: d.kind, id: d.id}.stage())
	}
	return stages, calls
}

// runOnce executes a fresh run with the runner (not a resume).
func runOnce(t *testing.T, m *run.Manifest, store *workspace.Store, stages []runner.Stage, clk runner.Clock) (*runner.Result, error) {
	t.Helper()
	return runner.Run(context.Background(), runner.Config{Manifest: m, Store: store, Stages: stages, Clock: clk})
}

// loadReloaded reopens the workspace (as a restarted process would) and loads
// the persisted manifest.
func loadReloaded(t *testing.T, store *workspace.Store, id run.RunID) *run.Manifest {
	t.Helper()
	loaded, err := workspace.NewStore(store.Root())
	require.NoError(t, err)
	m, _, err := loaded.LoadManifest(id)
	require.NoError(t, err)
	return m
}

// manifestFile returns the path of the persisted manifest.
func manifestFile(t *testing.T, store *workspace.Store, id run.RunID) string {
	t.Helper()
	return filepath.Join(store.RunsDir(), id.String(), workspace.ManifestFileName)
}

// statusOf returns the persisted status of a stage.
func statusOf(t *testing.T, m *run.Manifest, name run.StageName) run.StageStatus {
	t.Helper()
	for _, s := range m.Stages {
		if s.Name == name {
			return s.Status
		}
	}
	t.Fatalf("stage %s not in manifest", name)
	return ""
}

// stageIndex returns the index of a stage in the manifest's plan.
func stageIndex(t *testing.T, m *run.Manifest, name run.StageName) int {
	t.Helper()
	for i, s := range m.Stages {
		if s.Name == name {
			return i
		}
	}
	t.Fatalf("stage %s not in manifest", name)
	return -1
}

// artifactExists reports whether a relative artifact path exists in the run
// directory.
func artifactExists(t *testing.T, runDir, rel string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(runDir, filepath.FromSlash(rel)))
	return err == nil
}

// writeManifestRaw writes bytes directly to the manifest file, bypassing the
// store's validation — this is how tests simulate torn, corrupt, or
// hand-assembled manifests on disk.
func writeManifestRaw(t *testing.T, store *workspace.Store, id run.RunID, data []byte) {
	t.Helper()
	require.NoError(t, os.WriteFile(manifestFile(t, store, id), data, 0o600))
}

// containsName reports whether names contains n.
func containsName(names []run.StageName, n run.StageName) (run.StageName, bool) {
	for _, x := range names {
		if x == n {
			return x, true
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// Resume
// ---------------------------------------------------------------------------

// TestResumeAfterResearch: a run that completed research and failed at verify
// resumes by re-executing verify and the rest, reusing research — the
// expensive upstream stage is never paid for twice.
func TestResumeAfterResearch(t *testing.T) {
	store := newStore(t)
	m := newManifest(t)
	clk := newAdvancingClock(baseTime)
	stages, calls := newStages(t)
	stages[1] = flakyOnce(run.StageVerify, calls[run.StageVerify], "verification.json", run.ArtifactKindVerification, "verification")

	// First cycle: verify fails, so the run fails after research.
	res, err := runOnce(t, m, store, stages, clk)
	require.Error(t, err)
	require.NotNil(t, res)
	assert.Equal(t, run.RunStatusFailed, res.Status)
	assert.Equal(t, 1, *calls[run.StageResearch])
	assert.Equal(t, 1, *calls[run.StageVerify])
	assert.Equal(t, 0, *calls[run.StageWrite])

	// The persisted record: research succeeded, verify failed, rest pending.
	loaded := loadReloaded(t, store, m.ID)
	assert.Equal(t, run.RunStatusFailed, loaded.Status)
	assert.Equal(t, run.StageStatusSucceeded, statusOf(t, loaded, run.StageResearch))
	assert.Equal(t, run.StageStatusFailed, statusOf(t, loaded, run.StageVerify))
	assert.Equal(t, run.StageStatusPending, statusOf(t, loaded, run.StageWrite))
	assert.True(t, artifactExists(t, res.RunDir, "dossier.json"))

	// Resume: the flaky verifier succeeds on its second attempt.
	res2, err := resume.Resume(context.Background(), store, m.ID, stages, resume.Options{Clock: clk})
	require.NoError(t, err)
	require.NotNil(t, res2)
	assert.Equal(t, run.RunStatusSucceeded, res2.Status)

	// Research ran exactly once in total: the resume reused its output.
	assert.Equal(t, 1, *calls[run.StageResearch], "resuming must not re-run completed stages")
	assert.Equal(t, 2, *calls[run.StageVerify], "the failed stage must be retried")
	assert.Equal(t, 1, *calls[run.StageWrite])
	assert.Equal(t, 1, *calls[run.StageFinalCheck])
	assert.Equal(t, 1, *calls[run.StageRender])

	// The durable record completed in place, under the same run ID, keeping
	// the failed verify attempt in history.
	loaded = loadReloaded(t, store, m.ID)
	assert.Equal(t, run.RunStatusSucceeded, loaded.Status)
	assert.Equal(t, run.StageStatusSucceeded, statusOf(t, loaded, run.StageVerify))
	assert.Len(t, loaded.Stages[1].Attempts, 2, "verify must keep its failed attempt in history")
	for _, p := range []string{"dossier.json", "verification.json", "artifact.json", "final-check.json", "output/article.md"} {
		assert.True(t, artifactExists(t, res2.RunDir, p), "missing artifact %s after resume", p)
	}
}

// TestResumeInterruptedRun: a process death mid-stage leaves the stage
// running on disk; the resume recovers the interrupted stage and continues,
// never losing the completed prefix.
func TestResumeInterruptedRun(t *testing.T) {
	store := newStore(t)
	m := newManifest(t)

	// Simulate the first cycle up to a hard crash: claim the run, start it,
	// complete research, start verify — then the process dies (no further
	// persistence), leaving verify running and the run running.
	_, err := store.CreateRun(m)
	require.NoError(t, err)
	require.NoError(t, m.Start(baseTime.Add(1*time.Second)))
	_, err = store.SaveManifest(m)
	require.NoError(t, err)
	require.NoError(t, m.StartStage(run.StageResearch, baseTime.Add(2*time.Second)))
	_, err = store.SaveManifest(m)
	require.NoError(t, err)
	ref := run.ArtifactRef{
		Kind: run.ArtifactKindDossier, ID: "dossier", Stage: run.StageResearch,
		Path: "dossier.json", ProducedAt: run.NewTimestamp(baseTime.Add(3 * time.Second)),
	}
	_, err = store.SaveArtifact(m.ID, ref, []byte(`{"claims":[]}`))
	require.NoError(t, err)
	require.NoError(t, m.AddArtifact(ref))
	require.NoError(t, m.SucceedStage(run.StageResearch, baseTime.Add(4*time.Second)))
	_, err = store.SaveManifest(m)
	require.NoError(t, err)
	require.NoError(t, m.StartStage(run.StageVerify, baseTime.Add(5*time.Second)))
	_, err = store.SaveManifest(m)
	require.NoError(t, err)

	loaded, _, err := store.LoadManifest(m.ID)
	require.NoError(t, err)
	assert.Equal(t, run.RunStatusRunning, loaded.Status, "the crashed run is still running on disk")
	assert.Equal(t, run.StageStatusRunning, statusOf(t, loaded, run.StageVerify))

	// The resume must continue: interrupt the dangling verify, re-run it and
	// the rest, and complete the run.
	stages, calls := newStages(t)
	res, err := resume.Resume(context.Background(), store, m.ID, stages, resume.Options{Clock: newAdvancingClock(baseTime.Add(6 * time.Second))})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, run.RunStatusSucceeded, res.Status)
	assert.Equal(t, 0, *calls[run.StageResearch], "research completed in the crashed cycle and must be reused")
	assert.Equal(t, 1, *calls[run.StageVerify])
	assert.Equal(t, 1, *calls[run.StageWrite])

	loaded = loadReloaded(t, store, m.ID)
	assert.Equal(t, run.RunStatusSucceeded, loaded.Status)
	verify := loaded.Stages[stageIndex(t, loaded, run.StageVerify)]
	require.Len(t, verify.Attempts, 2, "the interrupted attempt must remain in history")
	assert.Equal(t, run.StageStatusCancelled, verify.Attempts[0].Status)
	assert.Equal(t, run.StageStatusSucceeded, verify.Attempts[1].Status)
}

// TestResumeSucceededRunIsNoOp: resuming a completed run executes nothing and
// touches nothing on disk.
func TestResumeSucceededRunIsNoOp(t *testing.T) {
	store := newStore(t)
	m := newManifest(t)
	clk := newAdvancingClock(baseTime)
	stages, calls := newStages(t)
	_, err := runOnce(t, m, store, stages, clk)
	require.NoError(t, err)

	before, err := os.ReadFile(manifestFile(t, store, m.ID))
	require.NoError(t, err)
	beforeCalls := map[run.StageName]int{}
	for k, v := range calls {
		beforeCalls[k] = *v
	}

	res, err := resume.Resume(context.Background(), store, m.ID, stages, resume.Options{Clock: clk})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, run.RunStatusSucceeded, res.Status)
	assert.Empty(t, res.Executed, "a no-op resume must execute nothing")
	for k, v := range beforeCalls {
		assert.Equal(t, v, *calls[k], "stage %s was re-executed on a no-op resume", k)
	}

	after, err := os.ReadFile(manifestFile(t, store, m.ID))
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "a no-op resume must leave the manifest byte-identical")

	rep, err := resume.Analyze(store, m.ID)
	require.NoError(t, err)
	assert.True(t, rep.Resumable)
	assert.Equal(t, run.RunStatusSucceeded, rep.RunStatus)
	assert.Empty(t, rep.ToRun)
	assert.Len(t, rep.Complete, 5)
	assert.Empty(t, rep.Skipped)
}

// ---------------------------------------------------------------------------
// Retry
// ---------------------------------------------------------------------------

// TestRetryVerifierInvalidatesDownstream: explicitly retrying the verifier on
// a SUCCESSFUL run re-runs the verifier and every dependent downstream stage
// (write, final_check, render), while the independent upstream stage
// (research) is reused. Stale downstream output is invalidated, never
// silently kept.
func TestRetryVerifierInvalidatesDownstream(t *testing.T) {
	store := newStore(t)
	m := newManifest(t)
	clk := newAdvancingClock(baseTime)
	stages, calls := newStages(t)
	_, err := runOnce(t, m, store, stages, clk)
	require.NoError(t, err)
	require.Equal(t, run.RunStatusSucceeded, loadReloaded(t, store, m.ID).Status)

	res, err := resume.RetryStage(context.Background(), store, m.ID, run.StageVerify, stages, resume.Options{Clock: clk})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, run.RunStatusSucceeded, res.Status)
	assert.Equal(t, []run.StageName{run.StageVerify, run.StageWrite, run.StageFinalCheck, run.StageRender}, res.Executed)
	assert.Equal(t, []run.StageName{run.StageResearch}, res.Reused)

	// Research (upstream of the retry origin) is reused; the verifier and
	// everything downstream of it runs again.
	assert.Equal(t, 1, *calls[run.StageResearch], "upstream of the retry must be reused")
	assert.Equal(t, 2, *calls[run.StageVerify])
	assert.Equal(t, 2, *calls[run.StageWrite])
	assert.Equal(t, 2, *calls[run.StageFinalCheck])
	assert.Equal(t, 2, *calls[run.StageRender])

	// The history of the retried stages is preserved: every stage in the
	// invalidation set keeps its earlier successful attempt.
	loaded := loadReloaded(t, store, m.ID)
	for _, name := range []run.StageName{run.StageVerify, run.StageWrite, run.StageFinalCheck, run.StageRender} {
		rec := loaded.Stages[stageIndex(t, loaded, name)]
		assert.GreaterOrEqual(t, len(rec.Attempts), 2, "stage %s must keep its previous attempt history", name)
		assert.Equal(t, run.StageStatusSucceeded, rec.Status)
	}
	assert.Len(t, loaded.Stages[stageIndex(t, loaded, run.StageResearch)].Attempts, 1, "research must not be re-attempted")

	// The rendered output reflects the new cycle (call 2, not call 1): stale
	// downstream output does not survive a retry.
	data, err := store.LoadArtifact(m.ID, "output/article.md")
	require.NoError(t, err)
	assert.Contains(t, string(data), `"call":2`, "stale downstream output must not survive a retry")
}

// TestRetryWriterInvalidatesFinalCheckAndRender: retrying the writer must
// invalidate exactly the stages that consume its output — final_check and
// render — and no further.
func TestRetryWriterInvalidatesFinalCheckAndRender(t *testing.T) {
	store := newStore(t)
	m := newManifest(t)
	clk := newAdvancingClock(baseTime)
	stages, calls := newStages(t)
	_, err := runOnce(t, m, store, stages, clk)
	require.NoError(t, err)

	res, err := resume.RetryStage(context.Background(), store, m.ID, run.StageWrite, stages, resume.Options{Clock: clk})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, run.RunStatusSucceeded, res.Status)

	assert.Equal(t, 1, *calls[run.StageResearch], "stages above the retry origin must be reused")
	assert.Equal(t, 1, *calls[run.StageVerify], "stages above the retry origin must be reused")
	assert.Equal(t, 2, *calls[run.StageWrite])
	assert.Equal(t, 2, *calls[run.StageFinalCheck], "final_check consumes write output and must be re-run")
	assert.Equal(t, 2, *calls[run.StageRender], "render consumes the final artifact and must be re-run")
}

// TestRetryFailedRun: retrying a stage of a FAILED run reopens the run and
// completes it when the retried stage succeeds.
func TestRetryFailedRun(t *testing.T) {
	store := newStore(t)
	m := newManifest(t)
	clk := newAdvancingClock(baseTime)
	stages, calls := newStages(t)
	stages[2] = flakyOnce(run.StageWrite, calls[run.StageWrite], "artifact.json", run.ArtifactKindEditorial, "editorial")

	_, err := runOnce(t, m, store, stages, clk)
	require.Error(t, err)
	assert.Equal(t, run.RunStatusFailed, loadReloaded(t, store, m.ID).Status)

	res, err := resume.RetryStage(context.Background(), store, m.ID, run.StageWrite, stages, resume.Options{Clock: clk})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, run.RunStatusSucceeded, res.Status)
	assert.Equal(t, 2, *calls[run.StageWrite])
	assert.Equal(t, 1, *calls[run.StageVerify], "upstream stages are reused even on a failed run")
	loaded := loadReloaded(t, store, m.ID)
	assert.Equal(t, run.RunStatusSucceeded, loaded.Status)
	assert.Len(t, loaded.Stages[stageIndex(t, loaded, run.StageWrite)].Attempts, 2)
}

// ---------------------------------------------------------------------------
// Integrity: missing, corrupt, and inconsistent runs cannot be resumed
// ---------------------------------------------------------------------------

// TestResumeRefusesMissingArtifact: a completed stage whose artifact vanished
// from the run directory makes the run unresumable; nothing is touched.
func TestResumeRefusesMissingArtifact(t *testing.T) {
	store := newStore(t)
	m := newManifest(t)
	stages, _ := newStages(t)
	res, err := runOnce(t, m, store, stages, newAdvancingClock(baseTime))
	require.NoError(t, err)

	require.True(t, artifactExists(t, res.RunDir, "dossier.json"))
	require.NoError(t, os.Remove(filepath.Join(res.RunDir, "dossier.json")))

	before, err := os.ReadFile(manifestFile(t, store, m.ID))
	require.NoError(t, err)

	_, err = resume.Resume(context.Background(), store, m.ID, stages, resume.Options{Clock: newAdvancingClock(baseTime)})
	require.Error(t, err)
	assert.True(t, errors.Is(err, resume.ErrMissingArtifact), "got: %v", err)
	assert.Contains(t, err.Error(), "dossier")

	after, err := os.ReadFile(manifestFile(t, store, m.ID))
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "a refused resume must leave the manifest untouched")

	// Analyze reports the same problem as a non-resumable run (read-only).
	rep, err := resume.Analyze(store, m.ID)
	require.NoError(t, err)
	assert.False(t, rep.Resumable)
	assert.Contains(t, rep.Reason, "dossier")
	_, inComplete := containsName(rep.Complete, run.StageResearch)
	assert.False(t, inComplete, "a stage with a missing artifact is not complete")
}

// TestResumeRefusesCorruptManifest: an unreadable or invalid manifest cannot
// be resumed; the error is classified as corrupt.
func TestResumeRefusesCorruptManifest(t *testing.T) {
	t.Run("unparseable", func(t *testing.T) {
		store := newStore(t)
		m := newManifest(t)
		stages, _ := newStages(t)
		_, err := runOnce(t, m, store, stages, newAdvancingClock(baseTime))
		require.NoError(t, err)

		writeManifestRaw(t, store, m.ID, []byte("{ not json"))

		_, err = resume.Resume(context.Background(), store, m.ID, stages, resume.Options{Clock: newAdvancingClock(baseTime)})
		require.Error(t, err)
		assert.True(t, errors.Is(err, resume.ErrCorruptManifest), "got: %v", err)

		// Analyze cannot classify a corrupt manifest: it is a hard error, not
		// a report.
		_, err = resume.Analyze(store, m.ID)
		require.Error(t, err)
		assert.True(t, errors.Is(err, resume.ErrCorruptManifest), "got: %v", err)
	})

	t.Run("invalid stage status", func(t *testing.T) {
		store := newStore(t)
		m := newManifest(t)
		stages, _ := newStages(t)
		_, err := runOnce(t, m, store, stages, newAdvancingClock(baseTime))
		require.NoError(t, err)

		data, err := os.ReadFile(manifestFile(t, store, m.ID))
		require.NoError(t, err)
		var raw run.Manifest
		require.NoError(t, json.Unmarshal(data, &raw))
		raw.Stages[stageIndex(t, &raw, run.StageVerify)].Status = run.StageStatus("corrupted")
		out, err := run.MarshalManifest(&raw)
		require.NoError(t, err)
		writeManifestRaw(t, store, m.ID, out)

		_, err = resume.Resume(context.Background(), store, m.ID, stages, resume.Options{Clock: newAdvancingClock(baseTime)})
		require.Error(t, err)
		assert.True(t, errors.Is(err, resume.ErrCorruptManifest), "got: %v", err)
	})
}

// TestResumeRefusesInconsistentRun: a manifest whose artifact references do
// not match the stage states (a stage that failed yet claims an artifact) is
// internally inconsistent and unresumable.
func TestResumeRefusesInconsistentRun(t *testing.T) {
	store := newStore(t)
	m := newManifest(t)
	stages, _ := newStages(t)
	_, err := runOnce(t, m, store, stages, newAdvancingClock(baseTime))
	require.NoError(t, err)

	data, err := os.ReadFile(manifestFile(t, store, m.ID))
	require.NoError(t, err)
	var raw run.Manifest
	require.NoError(t, json.Unmarshal(data, &raw))
	// Re-derive a failed run in which the render stage failed but still
	// claims its artifact: every schema-level rule holds, yet the artifact
	// references are inconsistent with the stage states.
	raw.Status = run.RunStatusFailed
	now := run.NewTimestamp(raw.FinishedAt.Time().Add(time.Second))
	renderFailed := run.StageRender
	raw.Result = run.Result{Outcome: run.OutcomeFailed, Reason: "render never recovered", FailedStage: &renderFailed}
	render := raw.Stages[stageIndex(t, &raw, run.StageRender)]
	render.Status = run.StageStatusFailed
	last := &render.Attempts[len(render.Attempts)-1]
	last.Status = run.StageStatusFailed
	last.Error = &run.ErrorSummary{
		Code:       "stage_failed",
		Message:    "simulated",
		OccurredAt: now,
	}
	raw.Stages[stageIndex(t, &raw, run.StageRender)] = render
	require.NoError(t, raw.Validate())
	out, err := run.MarshalManifest(&raw)
	require.NoError(t, err)
	writeManifestRaw(t, store, m.ID, out)

	_, err = resume.Resume(context.Background(), store, m.ID, stages, resume.Options{Clock: newAdvancingClock(baseTime)})
	require.Error(t, err)
	assert.True(t, errors.Is(err, resume.ErrInconsistentRun), "got: %v", err)
	assert.Contains(t, err.Error(), "render")
}

// TestResumeUnknownRun: resuming a run ID the workspace does not know yields
// the store's not-found error.
func TestResumeUnknownRun(t *testing.T) {
	store := newStore(t)
	stages, _ := newStages(t)
	_, err := resume.Resume(context.Background(), store, run.RunID("run-19700101T000000Z-000000000000"), stages, resume.Options{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, workspace.ErrRunNotFound), "got: %v", err)
}

// TestResumeSelfHealsFailedRunWithCompletePlan: a failed run whose stages
// are all already complete (a crash that recorded the failure after the last
// stage finished) resumes by re-opening and completing in place, executing
// nothing.
func TestResumeSelfHealsFailedRunWithCompletePlan(t *testing.T) {
	store := newStore(t)
	m := newManifest(t)

	_, err := store.CreateRun(m)
	require.NoError(t, err)
	require.NoError(t, m.Start(baseTime.Add(1*time.Second)))
	_, err = store.SaveManifest(m)
	require.NoError(t, err)
	tt := baseTime
	for i, name := range corePlan {
		tt = tt.Add(time.Second)
		require.NoError(t, m.StartStage(name, tt))
		_, err = store.SaveManifest(m)
		require.NoError(t, err)
		tt = tt.Add(time.Second)
		require.NoError(t, m.SucceedStage(name, tt))
		// The final stage is not persisted on its own: a run that is still
		// running with every stage complete is not a valid manifest state.
		// (The crash happens right before Succeed; only the failure is
		// durable.)
		if i < len(corePlan)-1 {
			_, err = store.SaveManifest(m)
			require.NoError(t, err)
		}
	}
	tt = tt.Add(time.Second)
	require.NoError(t, m.Fail(tt, "run_error", "crash recorded after the last stage finished"))
	_, err = store.SaveManifest(m)
	require.NoError(t, err)

	stages, calls := newStages(t)
	res, err := resume.Resume(context.Background(), store, m.ID, stages, resume.Options{Clock: newAdvancingClock(tt.Add(time.Second))})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, run.RunStatusSucceeded, res.Status)
	for name, n := range calls {
		assert.Equal(t, 0, *n, "stage %s already completed; nothing may be re-executed", name)
	}

	loaded := loadReloaded(t, store, m.ID)
	assert.Equal(t, run.RunStatusSucceeded, loaded.Status)
	assert.Equal(t, run.OutcomeSucceeded, loaded.Result.Outcome)
}

// ---------------------------------------------------------------------------
// Optional / skipped stages
// ---------------------------------------------------------------------------

// TestSkippedStagesSurviveResumeAndRetry: skipped (optional) stages are kept
// on resume, and a retry that invalidates a skipped downstream stage
// re-applies the skip instead of running it.
func TestSkippedStagesSurviveResumeAndRetry(t *testing.T) {
	store := newStore(t)
	m := newManifest(t, run.CanonicalStages()...)
	clk := newAdvancingClock(baseTime)
	stages := []runner.Stage{}
	calls := map[run.StageName]*int{}
	defs := []struct {
		name       run.StageName
		path       string
		kind       run.ArtifactKind
		id         string
		skipReason string
	}{
		{run.StageDiscovery, "discovery.json", run.ArtifactKindCustom, "discovery", "explicit URLs provided"},
		{run.StageFetch, "sources/source-1.json", run.ArtifactKindSource, "source-1", ""},
		{run.StageExtract, "documents/doc-1.md", run.ArtifactKindDocument, "doc-1", ""},
		{run.StageResearch, "dossier.json", run.ArtifactKindDossier, "dossier", ""},
		{run.StageVerify, "verification.json", run.ArtifactKindVerification, "verification", ""},
		{run.StageFollowUp, "", run.ArtifactKindCustom, "", "no unresolved claims"},
		{run.StageWrite, "artifact.json", run.ArtifactKindEditorial, "editorial", ""},
		{run.StageFinalCheck, "final-check.json", run.ArtifactKindFactualCheck, "final-check", ""},
		{run.StageRender, "output/article.md", run.ArtifactKindRendered, "article", ""},
	}
	for _, d := range defs {
		c := 0
		calls[d.name] = &c
		if d.skipReason != "" {
			stages = append(stages, runner.Stage{Name: d.name, SkipReason: d.skipReason})
			continue
		}
		stages = append(stages, fakeStage{name: d.name, calls: calls[d.name], path: d.path, kind: d.kind, id: d.id}.stage())
	}
	// The first cycle fails at final_check.
	stages[stageIndex(t, m, run.StageFinalCheck)] = alwaysFailing(run.StageFinalCheck, calls[run.StageFinalCheck])

	_, err := runOnce(t, m, store, stages, clk)
	require.Error(t, err)
	loaded := loadReloaded(t, store, m.ID)
	assert.Equal(t, run.StageStatusSkipped, statusOf(t, loaded, run.StageDiscovery))
	assert.Equal(t, run.StageStatusSkipped, statusOf(t, loaded, run.StageFollowUp))
	assert.Equal(t, run.StageStatusFailed, statusOf(t, loaded, run.StageFinalCheck))

	// Resume: the final-check stage works now, so the run completes; the
	// skipped stages stay skipped (never re-run) and only the failed stage
	// and its unfinished dependents execute.
	stages[stageIndex(t, m, run.StageFinalCheck)] = fakeStage{
		name: run.StageFinalCheck, calls: calls[run.StageFinalCheck],
		path: "final-check.json", kind: run.ArtifactKindFactualCheck, id: "final-check",
	}.stage()
	res2, err := resume.Resume(context.Background(), store, m.ID, stages, resume.Options{Clock: clk})
	require.NoError(t, err)
	require.NotNil(t, res2)
	assert.Equal(t, run.RunStatusSucceeded, res2.Status)
	loaded = loadReloaded(t, store, m.ID)
	assert.Equal(t, run.StageStatusSkipped, statusOf(t, loaded, run.StageDiscovery))
	assert.Equal(t, run.StageStatusSkipped, statusOf(t, loaded, run.StageFollowUp))
	assert.Equal(t, 0, *calls[run.StageDiscovery], "a skipped stage must not be re-executed on resume")
	assert.Equal(t, 0, *calls[run.StageFollowUp], "a skipped stage must not be re-executed on resume")
	assert.Equal(t, 2, *calls[run.StageFinalCheck])
	assert.Equal(t, 1, *calls[run.StageRender])

	// Retry the verifier: it re-runs, and the skipped follow_up (a dependent
	// downstream stage) is invalidated and re-skipped, not executed.
	res3, err := resume.RetryStage(context.Background(), store, m.ID, run.StageVerify, stages, resume.Options{Clock: clk})
	require.NoError(t, err)
	require.NotNil(t, res3)
	assert.Equal(t, run.RunStatusSucceeded, res3.Status)
	loaded = loadReloaded(t, store, m.ID)
	assert.Equal(t, run.StageStatusSkipped, statusOf(t, loaded, run.StageFollowUp))
	assert.Equal(t, "no unresolved claims", loaded.Stages[stageIndex(t, loaded, run.StageFollowUp)].SkipReason)
	assert.Equal(t, 2, *calls[run.StageVerify])
	assert.Equal(t, 0, *calls[run.StageFollowUp], "a skipped stage re-planned by a retry must be skipped again")
	assert.Equal(t, 2, *calls[run.StageWrite])
	assert.Equal(t, 1, *calls[run.StageResearch], "research is upstream of verify and must be reused")
}

// TestRetryAbsentOrInvalidStage: retrying a stage that is not part of the
// plan (or an unknown name) is refused before anything is touched.
func TestRetryAbsentOrInvalidStage(t *testing.T) {
	store := newStore(t)
	m := newManifest(t)
	clk := newAdvancingClock(baseTime)
	stages, _ := newStages(t)
	_, err := runOnce(t, m, store, stages, clk)
	require.NoError(t, err)
	before, err := os.ReadFile(manifestFile(t, store, m.ID))
	require.NoError(t, err)

	_, err = resume.RetryStage(context.Background(), store, m.ID, run.StageDiscovery, stages, resume.Options{Clock: clk})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "discovery")

	_, err = resume.RetryStage(context.Background(), store, m.ID, run.StageName("bogus"), stages, resume.Options{Clock: clk})
	require.Error(t, err)

	after, err := os.ReadFile(manifestFile(t, store, m.ID))
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "a refused retry must leave the manifest untouched")
}

// ---------------------------------------------------------------------------
// Bounded history
// ---------------------------------------------------------------------------

// TestBoundedHistoryAcrossRetries: repeated retries of a failing stage keep
// the persisted manifest bounded — the attempt history and the run error log
// never grow without limit, attempt numbers stay sequential, and the newest
// attempts are the ones kept.
func TestBoundedHistoryAcrossRetries(t *testing.T) {
	store := newStore(t)
	m := newManifest(t)
	clk := newAdvancingClock(baseTime)
	stages, calls := newStages(t)
	stages[1] = alwaysFailing(run.StageVerify, calls[run.StageVerify])

	_, err := runOnce(t, m, store, stages, clk)
	require.Error(t, err)

	// Retry the verifier 40 times (each cycle fails again) — far beyond the
	// attempt and error-log bounds.
	for i := 0; i < 40; i++ {
		_, err := resume.RetryStage(context.Background(), store, m.ID, run.StageVerify, stages, resume.Options{Clock: clk})
		require.Error(t, err, "retry %d must fail again", i)
	}

	loaded := loadReloaded(t, store, m.ID)
	verify := loaded.Stages[stageIndex(t, loaded, run.StageVerify)]
	// 1 initial attempt + 40 retries = 41 attempts; the bounded history keeps
	// the most recent 32, renumbered 1..32 — the newest attempts survive.
	assert.Equal(t, run.MaxAttemptsPerStage, len(verify.Attempts), "attempt history must be trimmed to the bound")
	assert.Equal(t, 1, verify.Attempts[0].Number, "survivors must be renumbered")
	assert.Equal(t, run.MaxAttemptsPerStage, verify.Attempts[len(verify.Attempts)-1].Number)
	assert.Equal(t, run.StageStatusFailed, verify.Status)
	assert.LessOrEqual(t, len(loaded.Errors), run.MaxRunErrors, "run error log must stay bounded")
	assert.LessOrEqual(t, len(loaded.Warnings), run.MaxRunWarnings, "run warning log must stay bounded")

	// And the run remains resumable: one final successful retry completes it.
	stages[1] = fakeStage{name: run.StageVerify, calls: calls[run.StageVerify], path: "verification.json", kind: run.ArtifactKindVerification, id: "verification"}.stage()
	res, err := resume.Resume(context.Background(), store, m.ID, stages, resume.Options{Clock: clk})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, run.RunStatusSucceeded, res.Status)
	loaded = loadReloaded(t, store, m.ID)
	verify = loaded.Stages[stageIndex(t, loaded, run.StageVerify)]
	assert.LessOrEqual(t, len(verify.Attempts), run.MaxAttemptsPerStage)
	assert.Equal(t, run.MaxAttemptsPerStage, verify.Attempts[len(verify.Attempts)-1].Number)
	assert.Equal(t, run.StageStatusSucceeded, verify.Status)
}
