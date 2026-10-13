package resume_test

import (
	"context"
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

// TestAnalyzeStates: the read-only assessment across every interesting shape
// of a persisted run.
func TestAnalyzeStates(t *testing.T) {
	t.Run("succeeded-intact", func(t *testing.T) {
		store := newStore(t)
		m := newManifest(t)
		stages, _ := newStages(t)
		_, err := runOnce(t, m, store, stages, newAdvancingClock(baseTime))
		require.NoError(t, err)

		rep, err := resume.Analyze(store, m.ID)
		require.NoError(t, err)
		assert.True(t, rep.Resumable)
		assert.Equal(t, run.RunStatusSucceeded, rep.RunStatus)
		assert.ElementsMatch(t, corePlan, rep.Complete)
		assert.Empty(t, rep.ToRun)
		assert.NotEmpty(t, rep.RunDir)
	})

	t.Run("failed", func(t *testing.T) {
		store := newStore(t)
		m := newManifest(t)
		stages, _ := newStages(t)
		stages[1] = alwaysFailing(run.StageVerify, new(int))
		_, err := runOnce(t, m, store, stages, newAdvancingClock(baseTime))
		require.Error(t, err)

		rep, err := resume.Analyze(store, m.ID)
		require.NoError(t, err)
		assert.True(t, rep.Resumable)
		assert.Equal(t, run.RunStatusFailed, rep.RunStatus)
		_, ok := containsName(rep.ToRun, run.StageVerify)
		assert.True(t, ok)
		_, ok = containsName(rep.Complete, run.StageResearch)
		assert.True(t, ok)
	})

	t.Run("interrupted-dangling-running-stage", func(t *testing.T) {
		store := newStore(t)
		m := newManifest(t)
		_, err := store.CreateRun(m)
		require.NoError(t, err)
		require.NoError(t, m.Start(baseTime.Add(time.Second)))
		_, err = store.SaveManifest(m)
		require.NoError(t, err)
		tt := baseTime
		for _, name := range corePlan {
			tt = tt.Add(time.Second)
			require.NoError(t, m.StartStage(name, tt))
			_, err = store.SaveManifest(m)
			require.NoError(t, err)
			if name == run.StageVerify {
				// Crash mid-verify: the stage is left running.
				return
			}
			tt = tt.Add(time.Second)
			require.NoError(t, m.SucceedStage(name, tt))
			_, err := store.SaveManifest(m)
			require.NoError(t, err)
		}
		t.Fatal("unreachable")
	})

	t.Run("missing-stage-artifact", func(t *testing.T) {
		store := newStore(t)
		m := newManifest(t)
		stages, _ := newStages(t)
		res, err := runOnce(t, m, store, stages, newAdvancingClock(baseTime))
		require.NoError(t, err)
		// Delete a completed stage's artifact from disk.
		require.NoError(t, os.Remove(filepath.Join(res.RunDir, "dossier.json")))

		rep, err := resume.Analyze(store, m.ID)
		require.NoError(t, err)
		assert.False(t, rep.Resumable)
		assert.NotEmpty(t, rep.Reason)
		_, ok := containsName(rep.Complete, run.StageResearch)
		assert.False(t, ok, "a stage with a missing artifact is not complete")
	})

	t.Run("missing-stageless-artifact", func(t *testing.T) {
		store := newStore(t)
		m := newManifest(t)
		stages, _ := newStages(t)
		// A run-level artifact (no owning stage) added before the run starts.
		require.NoError(t, m.AddArtifact(run.ArtifactRef{
			ID:         "run-notes",
			Kind:       run.ArtifactKindCustom,
			Path:       "notes.md",
			ProducedAt: run.NewTimestamp(baseTime.Add(time.Second)),
		}))
		res, err := runOnce(t, m, store, stages, newAdvancingClock(baseTime))
		require.NoError(t, err)
		_, err = store.SaveArtifact(m.ID, run.ArtifactRef{ID: "run-notes", Kind: run.ArtifactKindCustom, Path: "notes.md", ProducedAt: run.NewTimestamp(baseTime.Add(time.Second))}, []byte("notes"))
		require.NoError(t, err)
		require.NoError(t, os.Remove(filepath.Join(res.RunDir, "notes.md")))

		rep, err := resume.Analyze(store, m.ID)
		require.NoError(t, err)
		assert.False(t, rep.Resumable, "a missing stageless artifact must block resume")
		assert.Contains(t, rep.Reason, "run-notes")
	})

	t.Run("corrupt-manifest", func(t *testing.T) {
		store := newStore(t)
		m := newManifest(t)
		_, err := store.CreateRun(m)
		require.NoError(t, err)
		writeManifestRaw(t, store, m.ID, []byte("garbage{"))

		_, err = resume.Analyze(store, m.ID)
		require.Error(t, err)
		assert.True(t, errors.Is(err, resume.ErrCorruptManifest), "got: %v", err)
	})

	t.Run("unknown-run", func(t *testing.T) {
		store := newStore(t)
		_, err := resume.Analyze(store, run.RunID("run-19700101T000000Z-000000000000"))
		require.Error(t, err)
		assert.True(t, errors.Is(err, workspace.ErrRunNotFound), "got: %v", err)
	})

	t.Run("nil-store", func(t *testing.T) {
		_, err := resume.Analyze(nil, run.RunID("run-19700101T000000Z-000000000000"))
		require.Error(t, err)
	})
}

// TestResumeStagelessArtifacts: run-level artifacts (no owning stage) are
// verified like any other artifact — present they are harmless, missing they
// refuse the resume before anything executes.
func TestResumeStagelessArtifacts(t *testing.T) {
	build := func() (*workspace.Store, *run.Manifest, []runner.Stage) {
		store := newStore(t)
		m := newManifest(t)
		require.NoError(t, m.AddArtifact(run.ArtifactRef{
			ID:         "run-notes",
			Kind:       run.ArtifactKindCustom,
			Path:       "notes.md",
			ProducedAt: run.NewTimestamp(baseTime.Add(time.Second)),
		}))
		stages, _ := newStages(t)
		stages[1] = flakyOnce(run.StageVerify, new(int), "verification.json", run.ArtifactKindVerification, "verification")
		_, err := runOnce(t, m, store, stages, newAdvancingClock(baseTime))
		require.Error(t, err) // verify failed on its first attempt; the run fails
		_, err = store.SaveArtifact(m.ID, run.ArtifactRef{ID: "run-notes", Kind: run.ArtifactKindCustom, Path: "notes.md", ProducedAt: run.NewTimestamp(baseTime.Add(time.Second))}, []byte("notes"))
		require.NoError(t, err)
		return store, m, stages
	}

	t.Run("present", func(t *testing.T) {
		store, m, stages := build()
		res, err := resume.Resume(context.Background(), store, m.ID, stages, resume.Options{Clock: newAdvancingClock(baseTime.Add(60 * time.Second))})
		require.NoError(t, err)
		assert.Equal(t, run.RunStatusSucceeded, res.Status)
		_, ok := containsName(res.Reused, run.StageResearch)
		assert.True(t, ok)
		// The run-level artifact is untouched by the resume.
		data, err := os.ReadFile(filepath.Join(res.RunDir, "notes.md"))
		require.NoError(t, err)
		assert.Equal(t, []byte("notes"), data)
	})

	t.Run("missing", func(t *testing.T) {
		store, m, stages := build()
		require.NoError(t, os.Remove(filepath.Join(store.RunsDir(), m.ID.String(), "notes.md")))
		_, err := resume.Resume(context.Background(), store, m.ID, stages, resume.Options{Clock: newAdvancingClock(baseTime.Add(60 * time.Second))})
		require.Error(t, err)
		assert.True(t, errors.Is(err, resume.ErrMissingArtifact), "got: %v", err)
	})
}

// TestResumeFailsWhenPreparedPersistFails: if the reopened manifest cannot be
// made durable before the cycle starts, the resume aborts without executing a
// single stage.
func TestResumeFailsWhenPreparedPersistFails(t *testing.T) {
	inner := newStore(t)
	m := newManifest(t)
	stages, calls := newStages(t)
	stages[1] = alwaysFailing(run.StageVerify, calls[run.StageVerify])
	_, err := runOnce(t, m, inner, stages, newAdvancingClock(baseTime))
	require.Error(t, err)

	store := &faultStore{Store: inner, failSave: true}
	res, err := resume.Resume(context.Background(), store, m.ID, stages, resume.Options{Clock: newAdvancingClock(baseTime.Add(60 * time.Second))})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "persisting prepared manifest")
	require.NotNil(t, res)
	// Nothing executed: the failed verify keeps its single attempt, and the
	// downstream stages never ran.
	assert.Equal(t, 1, *calls[run.StageVerify])
	assert.Equal(t, 0, *calls[run.StageWrite])
	// The manifest on disk is untouched: still failed, render still pending.
	loaded := loadReloaded(t, inner, m.ID)
	assert.Equal(t, run.RunStatusFailed, loaded.Status)
}

// TestSelfHealFailsWhenPersistFails: the same fail-closed rule applies to the
// degenerate self-heal path (a failed run whose plan is already complete): if
// the completed state cannot be persisted, the resume reports an error.
func TestSelfHealFailsWhenPersistFails(t *testing.T) {
	inner := newStore(t)
	m := newManifest(t)
	_, err := inner.CreateRun(m)
	require.NoError(t, err)
	require.NoError(t, m.Start(baseTime.Add(time.Second)))
	_, err = inner.SaveManifest(m)
	require.NoError(t, err)
	tt := baseTime
	for i, name := range corePlan {
		tt = tt.Add(time.Second)
		require.NoError(t, m.StartStage(name, tt))
		_, err := inner.SaveManifest(m)
		require.NoError(t, err)
		tt = tt.Add(time.Second)
		require.NoError(t, m.SucceedStage(name, tt))
		if i < len(corePlan)-1 {
			_, err := inner.SaveManifest(m)
			require.NoError(t, err)
		}
	}
	tt = tt.Add(time.Second)
	require.NoError(t, m.Fail(tt, "run_error", "crash recorded after the last stage finished"))
	_, err = inner.SaveManifest(m)
	require.NoError(t, err)

	store := &faultStore{Store: inner, failSave: true}
	stages, calls := newStages(t)
	_, err = resume.Resume(context.Background(), store, m.ID, stages, resume.Options{Clock: newAdvancingClock(tt.Add(time.Second))})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "persisting prepared manifest")
	for name, n := range calls {
		assert.Equal(t, 0, *n, "stage %s must not be re-executed", name)
	}
	loaded := loadReloaded(t, inner, m.ID)
	assert.Equal(t, run.RunStatusFailed, loaded.Status)
}

// TestResumeNilStore rejects a nil store before touching anything.
func TestResumeNilStore(t *testing.T) {
	stages, _ := newStages(t)
	_, err := resume.Resume(context.Background(), nil, run.RunID("run-19700101T000000Z-000000000000"), stages, resume.Options{})
	require.Error(t, err)
}
