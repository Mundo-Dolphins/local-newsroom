package runner_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/runner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workspace"
)

// faultStore wraps a workspace store and injects persistence failures, so
// tests can drive the paths that only run when a write to disk fails (a full
// disk, a broken permission, ...). Reads always go through to the real store.
type faultStore struct {
	*workspace.Store
	failCreate   bool
	failSave     bool
	failArtifact bool
}

func (f *faultStore) CreateRun(m *run.Manifest) (string, error) {
	if f.failCreate {
		return "", errors.New("injected: cannot create run directory")
	}
	return f.Store.CreateRun(m)
}

func (f *faultStore) SaveManifest(m *run.Manifest) (string, error) {
	if f.failSave {
		return "", errors.New("injected: cannot write manifest")
	}
	return f.Store.SaveManifest(m)
}

func (f *faultStore) SaveArtifact(id run.RunID, ref run.ArtifactRef, data []byte) (string, error) {
	if f.failArtifact {
		return "", errors.New("injected: cannot write artifact " + ref.ID)
	}
	return f.Store.SaveArtifact(id, ref, data)
}

// alwaysFailing returns a stage whose executions always fail, counting calls.
func alwaysFailing(name run.StageName, calls *int) runner.Stage {
	return runner.Stage{
		Name: name,
		Fn: func(ctx context.Context, in *runner.StageInput) (*runner.StageOutput, error) {
			*calls++
			return nil, errors.New("simulated persistent failure")
		},
	}
}

// loadManifest reloads a persisted manifest from the workspace.
func loadManifest(t *testing.T, store runner.Store, id run.RunID) *run.Manifest {
	t.Helper()
	m, _, err := store.LoadManifest(id)
	require.NoError(t, err)
	return m
}

// statusOf returns the status of the given stage in the manifest.
func statusOf(m *run.Manifest, name run.StageName) run.StageStatus {
	for _, s := range m.Stages {
		if s.Name == name {
			return s.Status
		}
	}
	return ""
}

// TestRunFailsWhenArtifactSaveFails: a stage that produced output but cannot
// persist it fails the run; the stage is recorded failed, and the downstream
// stages never start.
func TestRunFailsWhenArtifactSaveFails(t *testing.T) {
	store := &faultStore{Store: newStore(t), failArtifact: true}
	m := newTestManifest(t)
	m.Stages = []run.StageRecord{
		{Name: run.StageResearch, Status: run.StageStatusPending},
		{Name: run.StageVerify, Status: run.StageStatusPending},
	}
	require.NoError(t, m.Validate())

	stages, calls := countedStages()
	// Only the first two stages are planned: drop the rest from the config.
	stages = stages[:2]

	res, err := runner.Run(context.Background(), runner.Config{Manifest: m, Store: store, Stages: stages, Clock: clockFrom(baseTime)})
	require.Error(t, err)
	require.NotNil(t, res)
	assert.Equal(t, run.RunStatusFailed, res.Status)
	assert.Equal(t, 1, *calls[run.StageResearch])
	assert.Equal(t, 0, *calls[run.StageVerify])

	loaded := loadManifest(t, store, m.ID)
	assert.Equal(t, run.StageStatusFailed, statusOf(loaded, run.StageResearch))
	assert.Equal(t, run.StageStatusPending, statusOf(loaded, run.StageVerify))
	require.Len(t, loaded.Stages[0].Attempts, 1)
	assert.Equal(t, "stage_failed", loaded.Stages[0].Attempts[0].Error.Code)
}

// TestRunFailsWhenManifestSaveFails: if the started run cannot be persisted,
// the run fails closed; the on-disk record is left in its last durable state
// (pending) — a resumed run can then start from there.
func TestRunFailsWhenManifestSaveFails(t *testing.T) {
	store := &faultStore{Store: newStore(t), failSave: true}
	m := newTestManifest(t)
	m.Stages = []run.StageRecord{
		{Name: run.StageResearch, Status: run.StageStatusPending},
		{Name: run.StageVerify, Status: run.StageStatusPending},
	}
	require.NoError(t, m.Validate())

	stages, calls := countedStages()
	stages = stages[:2]

	res, err := runner.Run(context.Background(), runner.Config{Manifest: m, Store: store, Stages: stages, Clock: clockFrom(baseTime)})
	require.Error(t, err)
	require.NotNil(t, res)
	assert.Equal(t, run.RunStatusFailed, res.Status)
	for _, n := range calls {
		assert.Equal(t, 0, *n)
	}
	// Every save failed: the durable record is still the pending manifest
	// that CreateRun wrote.
	loaded := loadManifest(t, store, m.ID)
	assert.Equal(t, run.RunStatusPending, loaded.Status)
}

// TestRunCreateFailure: a run directory that cannot be claimed fails before
// anything runs.
func TestRunCreateFailure(t *testing.T) {
	store := &faultStore{Store: newStore(t), failCreate: true}
	m := newTestManifest(t)
	stages, calls := countedStages()

	res, err := runner.Run(context.Background(), runner.Config{Manifest: m, Store: store, Stages: stages, Clock: clockFrom(baseTime)})
	require.Error(t, err)
	require.NotNil(t, res)
	assert.Empty(t, res.Status, "the run never reached a lifecycle state")
	for _, n := range calls {
		assert.Equal(t, 0, *n)
	}
}

// TestRunCancelledBeforeFirstStage: an already-cancelled context stops the
// run before the first stage starts; nothing executes.
func TestRunCancelledBeforeFirstStage(t *testing.T) {
	store := newStore(t)
	m := newTestManifest(t)
	m.Stages = []run.StageRecord{
		{Name: run.StageResearch, Status: run.StageStatusPending},
		{Name: run.StageVerify, Status: run.StageStatusPending},
	}
	require.NoError(t, m.Validate())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	stages, calls := countedStages()
	stages = stages[:2]

	res, err := runner.Run(ctx, runner.Config{Manifest: m, Store: store, Stages: stages, Clock: clockFrom(baseTime)})
	require.Error(t, err)
	require.NotNil(t, res)
	assert.Equal(t, run.RunStatusCancelled, res.Status)
	for _, n := range calls {
		assert.Equal(t, 0, *n)
	}
	loaded := loadManifest(t, store, m.ID)
	assert.Equal(t, run.RunStatusCancelled, loaded.Status)
}

// TestResumeSkipsUnstartedStage: a crashed run whose next stage is planned to
// be skipped (executor absent, reason recorded) resumes by re-applying the
// skip — never by executing something that has no executor.
func TestResumeSkipsUnstartedStage(t *testing.T) {
	store := newStore(t)
	m := newTestManifest(t)
	m.Stages = []run.StageRecord{
		{Name: run.StageResearch, Status: run.StageStatusPending},
		{Name: run.StageVerify, Status: run.StageStatusPending},
	}
	require.NoError(t, m.Validate())

	// Crash after research: research succeeded, verify still pending.
	_, err := store.CreateRun(m)
	require.NoError(t, err)
	require.NoError(t, m.Start(baseTime.Add(1*time.Second)))
	_, err = store.SaveManifest(m)
	require.NoError(t, err)
	require.NoError(t, m.StartStage(run.StageResearch, baseTime.Add(2*time.Second)))
	_, err = store.SaveManifest(m)
	require.NoError(t, err)
	require.NoError(t, m.SucceedStage(run.StageResearch, baseTime.Add(3*time.Second)))
	_, err = store.SaveManifest(m)
	require.NoError(t, err)

	loaded, _, err := store.LoadManifest(m.ID)
	require.NoError(t, err)

	stages, calls := countedStages()
	stages = stages[:2]
	stages[1] = runner.Stage{Name: run.StageVerify, SkipReason: "no unresolved claims"}

	res, err := runner.Run(context.Background(), runner.Config{Manifest: loaded, Store: store, Stages: stages, Clock: clockFrom(baseTime.Add(4 * time.Second)), Resume: true})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, run.RunStatusSucceeded, res.Status)
	assert.Equal(t, 0, *calls[run.StageResearch])
	assert.Equal(t, 0, *calls[run.StageVerify])

	loaded = loadManifest(t, store, m.ID)
	assert.Equal(t, run.StageStatusSkipped, statusOf(loaded, run.StageVerify))
	assert.NotEmpty(t, loaded.Stages[1].SkipReason)
}

// TestResumeTakeoverFailure: taking over a dangling attempt that fails again
// fails the run (the same attempt, not a new one); the completed prefix stays
// intact.
func TestResumeTakeoverFailure(t *testing.T) {
	store := newStore(t)
	m := newTestManifest(t)
	m.Stages = []run.StageRecord{
		{Name: run.StageResearch, Status: run.StageStatusPending},
		{Name: run.StageVerify, Status: run.StageStatusPending},
	}
	require.NoError(t, m.Validate())

	// Crash inside verify: research succeeded, verify running.
	_, err := store.CreateRun(m)
	require.NoError(t, err)
	require.NoError(t, m.Start(baseTime.Add(1*time.Second)))
	_, err = store.SaveManifest(m)
	require.NoError(t, err)
	require.NoError(t, m.StartStage(run.StageResearch, baseTime.Add(2*time.Second)))
	_, err = store.SaveManifest(m)
	require.NoError(t, err)
	require.NoError(t, m.SucceedStage(run.StageResearch, baseTime.Add(3*time.Second)))
	_, err = store.SaveManifest(m)
	require.NoError(t, err)
	require.NoError(t, m.StartStage(run.StageVerify, baseTime.Add(4*time.Second)))
	_, err = store.SaveManifest(m)
	require.NoError(t, err)

	loaded, _, err := store.LoadManifest(m.ID)
	require.NoError(t, err)

	verifyCalls := 0
	stages, _ := countedStages()
	stages = stages[:2]
	stages[1] = alwaysFailing(run.StageVerify, &verifyCalls)

	res, err := runner.Run(context.Background(), runner.Config{Manifest: loaded, Store: store, Stages: stages, Clock: clockFrom(baseTime.Add(5 * time.Second)), Resume: true})
	require.Error(t, err)
	require.NotNil(t, res)
	assert.Equal(t, run.RunStatusFailed, res.Status)
	assert.Equal(t, 1, verifyCalls, "the dangling attempt is executed in place")

	loaded = loadManifest(t, store, m.ID)
	assert.Equal(t, run.RunStatusFailed, loaded.Status)
	assert.Equal(t, run.StageStatusFailed, statusOf(loaded, run.StageVerify))
	require.Len(t, loaded.Stages[1].Attempts, 1, "no second attempt may be opened")
	assert.Equal(t, run.StageStatusSucceeded, statusOf(loaded, run.StageResearch))
}
