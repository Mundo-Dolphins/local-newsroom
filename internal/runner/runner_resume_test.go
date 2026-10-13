package runner_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/runner"
)

// countedStages returns the five core stages with call counters, for asserting
// exactly which stages a cycle executes.
func countedStages() (stages []runner.Stage, calls map[run.StageName]*int) {
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
		stages = append(stages, runner.Stage{
			Name: d.name,
			Fn: func(ctx context.Context, in *runner.StageInput) (*runner.StageOutput, error) {
				*calls[d.name]++
				return &runner.StageOutput{Files: []runner.StageFile{{Path: d.path, Kind: d.kind, ID: d.id, Data: []byte(`{}`)}}}, nil
			},
		})
	}
	return stages, calls
}

// runPlan is the core editorial chain.
var runPlan = []run.StageName{run.StageResearch, run.StageVerify, run.StageWrite, run.StageFinalCheck, run.StageRender}

// TestRunResumePendingStarts: a run that was claimed but never started (a
// crash between the manifest write and the start) resumes by starting and
// running the whole plan.
func TestRunResumePendingStarts(t *testing.T) {
	store := newStore(t)
	m := newTestManifest(t)
	m.Stages = make([]run.StageRecord, len(runPlan))
	for i, name := range runPlan {
		m.Stages[i] = run.StageRecord{Name: name, Status: run.StageStatusPending}
	}
	require.NoError(t, m.Validate())

	_, err := store.CreateRun(m)
	require.NoError(t, err)

	// Reload as a restarted process would.
	loaded, _, err := store.LoadManifest(m.ID)
	require.NoError(t, err)
	require.Equal(t, run.RunStatusPending, loaded.Status)

	stages, calls := countedStages()
	res, err := runner.Run(context.Background(), runner.Config{Manifest: loaded, Store: store, Stages: stages, Clock: fixedClock(), Resume: true})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, run.RunStatusSucceeded, res.Status)
	for name, n := range calls {
		assert.Equal(t, 1, *n, "stage %s should have run once", name)
	}
}

// TestRunResumeRunningTakesOverDanglingStage: a crash mid-stage leaves the
// stage running on disk. Resume mode executes the dangling attempt in place
// (no second attempt is opened), reuses the completed prefix, and finishes the
// run.
func TestRunResumeRunningTakesOverDanglingStage(t *testing.T) {
	store := newStore(t)
	m := newTestManifest(t)
	m.Stages = make([]run.StageRecord, len(runPlan))
	for i, name := range runPlan {
		m.Stages[i] = run.StageRecord{Name: name, Status: run.StageStatusPending}
	}
	require.NoError(t, m.Validate())

	// Claim, start, complete research, then crash inside verify: verify is
	// running, the run is running.
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
	require.Equal(t, run.RunStatusRunning, loaded.Status)

	stages, calls := countedStages()
	res, err := runner.Run(context.Background(), runner.Config{Manifest: loaded, Store: store, Stages: stages, Clock: clockFrom(baseTime.Add(5 * time.Second)), Resume: true})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, run.RunStatusSucceeded, res.Status)
	assert.Equal(t, 0, *calls[run.StageResearch], "the completed prefix must not be re-executed")
	assert.Equal(t, 1, *calls[run.StageVerify], "the dangling attempt is taken over, not re-opened")
	assert.Equal(t, 1, *calls[run.StageWrite])

	final, _, err := store.LoadManifest(m.ID)
	require.NoError(t, err)
	verify := final.Stages[1]
	require.Len(t, verify.Attempts, 1, "taking over the dangling attempt must not add a new one")
	assert.Equal(t, run.StageStatusSucceeded, verify.Attempts[0].Status)
}

// clockFrom is an advancing clock starting at a given time (used when a
// hand-built prefix already consumed earlier timestamps: manifest timestamps
// may never regress).
func clockFrom(start time.Time) runner.Clock {
	t := start
	return func() time.Time {
		t = t.Add(time.Second)
		return t
	}
}

// TestRunResumeRefusesTerminal: resume mode refuses a terminal manifest; the
// resume layer reopens it before calling the runner.
func TestRunResumeRefusesTerminal(t *testing.T) {
	store := newStore(t)
	m := newTestManifest(t)
	m.Stages = make([]run.StageRecord, len(runPlan))
	for i, name := range runPlan {
		m.Stages[i] = run.StageRecord{Name: name, Status: run.StageStatusPending}
	}
	require.NoError(t, m.Validate())

	_, err := store.CreateRun(m)
	require.NoError(t, err)
	require.NoError(t, m.Start(baseTime.Add(1*time.Second)))
	_, err = store.SaveManifest(m)
	require.NoError(t, err)
	require.NoError(t, m.Fail(baseTime.Add(2*time.Second), "run_error", "simulated crash before any stage"))
	_, err = store.SaveManifest(m)
	require.NoError(t, err)

	loaded, _, err := store.LoadManifest(m.ID)
	require.NoError(t, err)
	require.Equal(t, run.RunStatusFailed, loaded.Status)

	stages, _ := countedStages()
	res, err := runner.Run(context.Background(), runner.Config{Manifest: loaded, Store: store, Stages: stages, Clock: fixedClock(), Resume: true})
	require.Error(t, err)
	require.NotNil(t, res)
	assert.Contains(t, err.Error(), "Reopen")
}

// TestValidateStageConfigEnforcesPlanOrder: the exported stage-config check
// requires one stage entry per planned stage, in plan order.
func TestValidateStageConfigEnforcesPlanOrder(t *testing.T) {
	m := newTestManifest(t)
	m.Stages = make([]run.StageRecord, len(runPlan))
	for i, name := range runPlan {
		m.Stages[i] = run.StageRecord{Name: name, Status: run.StageStatusPending}
	}
	require.NoError(t, m.Validate())

	stages, _ := countedStages()

	require.NoError(t, runner.ValidateStageConfig(m, stages))

	// A config missing a stage is rejected.
	short := stages[:4]
	assert.Error(t, runner.ValidateStageConfig(m, short))

	// A config in the wrong order is rejected.
	ordered := make([]runner.Stage, len(stages))
	ordered[0] = stages[1] // verify before research
	ordered[1] = stages[0]
	assert.Error(t, runner.ValidateStageConfig(m, ordered))

	// A config naming an unknown stage is rejected.
	unknown := append(append([]runner.Stage{}, stages...), runner.Stage{Name: run.StageName("bogus")})
	assert.Error(t, runner.ValidateStageConfig(m, unknown))

	// Skipped stages may carry a reason and no executor.
	skippable := make([]runner.Stage, len(stages))
	copy(skippable, stages)
	skippable[1] = runner.Stage{Name: run.StageVerify, SkipReason: "already verified"}
	assert.NoError(t, runner.ValidateStageConfig(m, skippable))
}
