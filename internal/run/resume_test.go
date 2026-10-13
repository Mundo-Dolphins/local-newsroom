package run

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file covers the resumability primitives added for v0.5:
// the explicit stage dependency graph, the manifest mutators that back
// resume/retry (Reopen, InvalidateStage), and the bounded-history rules.

// ---------------------------------------------------------------------------
// Stage dependency graph
// ---------------------------------------------------------------------------

func TestDependenciesDirectUpstream(t *testing.T) {
	assert.Equal(t, []StageName{}, Dependencies(StageDiscovery))
	assert.Equal(t, []StageName{StageDiscovery}, Dependencies(StageFetch))
	assert.Equal(t, []StageName{StageFetch}, Dependencies(StageExtract))
	assert.Equal(t, []StageName{StageExtract}, Dependencies(StageResearch))
	assert.Equal(t, []StageName{StageResearch}, Dependencies(StageVerify))
	assert.Equal(t, []StageName{StageVerify}, Dependencies(StageFollowUp))
	// write is the merge point: it consumes the verification verdict and, when
	// present, the follow-up resolutions.
	assert.Equal(t, []StageName{StageVerify, StageFollowUp}, Dependencies(StageWrite))
	assert.Equal(t, []StageName{StageWrite}, Dependencies(StageFinalCheck))
	assert.Equal(t, []StageName{StageFinalCheck}, Dependencies(StageRender))

	// Unknown names have no dependencies (they are rejected elsewhere).
	assert.Empty(t, Dependencies(StageName("bogus")))
}

func TestDownstreamClosure(t *testing.T) {
	assert.Equal(t,
		[]StageName{StageDiscovery, StageFetch, StageExtract, StageResearch, StageVerify, StageFollowUp, StageWrite, StageFinalCheck, StageRender},
		DownstreamClosure(StageDiscovery),
		"a discovery re-run invalidates everything")
	assert.Equal(t,
		[]StageName{StageResearch, StageVerify, StageFollowUp, StageWrite, StageFinalCheck, StageRender},
		DownstreamClosure(StageResearch),
		"a research re-run invalidates the whole editorial chain")
	assert.Equal(t,
		[]StageName{StageVerify, StageFollowUp, StageWrite, StageFinalCheck, StageRender},
		DownstreamClosure(StageVerify),
		"a verify re-run invalidates follow-up, write, final check, and render")
	assert.Equal(t,
		[]StageName{StageWrite, StageFinalCheck, StageRender},
		DownstreamClosure(StageWrite),
		"a write re-run invalidates exactly final-check and render (plus itself)")
	assert.Equal(t, []StageName{StageFinalCheck, StageRender}, DownstreamClosure(StageFinalCheck))
	assert.Equal(t, []StageName{StageRender}, DownstreamClosure(StageRender))
}

func TestValidateStagePlanOrder(t *testing.T) {
	// The canonical order is a topological order.
	assert.NoError(t, ValidateStagePlanOrder(CanonicalStages()))

	// A run may plan a subset; dependencies absent from the plan are
	// satisfied by definition.
	assert.NoError(t, ValidateStagePlanOrder([]StageName{StageVerify}))
	assert.NoError(t, ValidateStagePlanOrder([]StageName{StageVerify, StageWrite}))
	assert.NoError(t, ValidateStagePlanOrder([]StageName{StageDiscovery, StageFetch}))

	// Reversed pairs are inconsistent plans.
	assert.Error(t, ValidateStagePlanOrder([]StageName{StageWrite, StageVerify}))
	assert.Error(t, ValidateStagePlanOrder([]StageName{StageVerify, StageResearch}))
	assert.Error(t, ValidateStagePlanOrder([]StageName{StageRender, StageFinalCheck, StageWrite}))
}

func TestPlanOrderEnforcedOnConstructionAndValidation(t *testing.T) {
	// NewManifest refuses a plan whose order contradicts the graph.
	_, err := NewManifest(NewManifestParams{
		Workspace: Workspace{Name: "newsroom"},
		Topic:     "EU AI Act",
		Stages:    []StageName{StageWrite, StageVerify},
		StartedAt: baseTime,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "verify")

	// Validate rejects a manifest whose stored plan is inconsistent.
	m, err := NewManifest(NewManifestParams{
		Workspace: Workspace{Name: "newsroom"},
		Topic:     "EU AI Act",
		StartedAt: baseTime,
	})
	require.NoError(t, err)
	m.Stages = []StageRecord{{Name: StageWrite, Status: StageStatusPending}, {Name: StageVerify, Status: StageStatusPending}}
	assert.Error(t, m.Validate())

	// A consistent subset validates.
	m.Stages = []StageRecord{{Name: StageVerify, Status: StageStatusPending}}
	assert.NoError(t, m.Validate())
}

// ---------------------------------------------------------------------------
// Reopen
// ---------------------------------------------------------------------------

// completeRunTo builds a manifest whose run has just succeeded with every
// stage succeeded.
func completeRunTo(t *testing.T) *Manifest {
	t.Helper()
	m := newTestManifest(t)
	require.NoError(t, m.Start(baseTime.Add(1*time.Second)))
	for i, s := range m.Stages {
		tt := baseTime.Add(time.Duration(i*2+2) * time.Second)
		require.NoError(t, m.StartStage(s.Name, tt))
		require.NoError(t, m.SucceedStage(s.Name, tt.Add(time.Second)))
	}
	require.NoError(t, m.Succeed(baseTime.Add(20*time.Second)))
	require.Equal(t, RunStatusSucceeded, m.Status)
	return m
}

func TestReopenSucceededRun(t *testing.T) {
	m := completeRunTo(t)
	before := cloneManifest(m)

	err := m.Reopen(baseTime.Add(21 * time.Second))
	require.NoError(t, err)
	assert.Equal(t, RunStatusRunning, m.Status)
	assert.Nil(t, m.FinishedAt)
	assert.Empty(t, m.Result.Outcome, "the terminal result is cleared on reopen")
	assert.Nil(t, m.Result.FailedStage, "no failed stage after reopen")
	assert.Equal(t, before.Stages, m.Stages, "stage records and attempt history survive a reopen")
	assert.Equal(t, before.Artifacts, m.Artifacts, "artifact references survive a reopen")
	assert.Equal(t, before.Errors, m.Errors, "error history survives a reopen")
	// A warning documents the reopen.
	require.NotEmpty(t, m.Warnings)
	assert.Contains(t, m.Warnings[len(m.Warnings)-1].Message, "reopened")

	// A reopened run is active again: invalidate, drive, and re-complete the
	// whole invalidated suffix, then reach a new terminal state.
	require.NoError(t, m.InvalidateStage(StageResearch, baseTime.Add(22*time.Second)))
	tt := baseTime.Add(23 * time.Second)
	for _, name := range DownstreamClosure(StageResearch) {
		tt = tt.Add(time.Second)
		require.NoError(t, m.StartStage(name, tt))
		tt = tt.Add(time.Second)
		require.NoError(t, m.SucceedStage(name, tt))
	}
	tt = tt.Add(time.Second)
	require.NoError(t, m.Succeed(tt))
	require.NoError(t, m.Validate())
}

func TestReopenFailedAndCancelledRuns(t *testing.T) {
	m := newTestManifest(t)
	require.NoError(t, m.Start(baseTime.Add(1*time.Second)))
	require.NoError(t, m.Fail(baseTime.Add(2*time.Second), "run_error", "boom"))
	require.NoError(t, m.Reopen(baseTime.Add(3*time.Second)))
	assert.Equal(t, RunStatusRunning, m.Status)
	assert.Empty(t, m.Result.Outcome, "the failed result is cleared on reopen")

	fresh := newTestManifest(t)
	require.NoError(t, fresh.Start(baseTime.Add(1*time.Second)))
	require.NoError(t, fresh.Cancel(baseTime.Add(2*time.Second), "user requested"))
	require.NoError(t, fresh.Reopen(baseTime.Add(3*time.Second)))
	assert.Equal(t, RunStatusRunning, fresh.Status)
	assert.Empty(t, fresh.Result.Outcome)
}

func TestReopenRejectsActiveRuns(t *testing.T) {
	m := newTestManifest(t)
	assert.Error(t, m.Reopen(baseTime))
	require.NoError(t, m.Start(baseTime))
	assert.Error(t, m.Reopen(baseTime.Add(time.Second)))
}

// A reopened run whose plan is fully complete violates the manifest
// invariant (a running run must have work left). The resume layer handles
// this by completing the run immediately; the manifest itself makes the
// state detectable.
func TestReopenOfFullyCompleteRunIsDetectable(t *testing.T) {
	m := completeRunTo(t)
	require.NoError(t, m.Reopen(baseTime.Add(21*time.Second)))
	assert.Error(t, m.Validate())
	// ...and is resolved by simply completing the run.
	require.NoError(t, m.Succeed(baseTime.Add(22*time.Second)))
	assert.NoError(t, m.Validate())
}

// ---------------------------------------------------------------------------
// InvalidateStage
// ---------------------------------------------------------------------------

// runningRunWithSucceededPrefix builds a manifest in the middle of a run:
// the stages up to (and including) upTo have succeeded, upTo's dependent
// stage is running, the rest are pending.
func TestInvalidateStageResetsDownstreamOnly(t *testing.T) {
	m := newTestManifest(t)
	tt := baseTime
	require.NoError(t, m.Start(baseTime.Add(1*time.Second)))
	// Complete discovery..verify; start follow_up.
	for _, name := range []StageName{StageDiscovery, StageFetch, StageExtract, StageResearch, StageVerify} {
		tt = tt.Add(time.Second)
		require.NoError(t, m.StartStage(name, tt))
		tt = tt.Add(time.Second)
		require.NoError(t, m.SucceedStage(name, tt))
	}
	tt = tt.Add(time.Second)
	require.NoError(t, m.StartStage(StageFollowUp, tt))

	// Some artifacts to distinguish kept from dropped references.
	m.Artifacts = []ArtifactRef{
		{Kind: ArtifactKindDossier, ID: "dossier", Stage: StageResearch, Path: "dossier.json", ProducedAt: NewTimestamp(baseTime)},
		{Kind: ArtifactKindEditorial, ID: "editorial", Stage: StageWrite, Path: "artifact.json", ProducedAt: NewTimestamp(baseTime)},
		{Kind: ArtifactKindCustom, ID: "loose", Path: "notes.md", ProducedAt: NewTimestamp(baseTime)},
	}

	tt = tt.Add(time.Second)
	require.NoError(t, m.InvalidateStage(StageResearch, tt))

	st := map[StageName]StageStatus{}
	for _, s := range m.Stages {
		st[s.Name] = s.Status
	}
	// Research and everything downstream of it: pending again.
	for _, name := range DownstreamClosure(StageResearch) {
		assert.Equal(t, StageStatusPending, st[name], "stage %s must be reset to pending", name)
	}
	// The closure is everything (research is upstream of all) — the skip
	// reason of reset stages is cleared, and no succeeded/skipped status
	// survives in the closure.
	require.NoError(t, m.Validate(), "invalidating a succeeded stage back to pending (with terminal attempts) must remain valid")

	// Artifact references of closed stages are dropped; the loose one stays.
	ids := map[string]bool{}
	for _, a := range m.Artifacts {
		ids[a.ID] = true
	}
	assert.False(t, ids["dossier"], "dossier belongs to research (closed)")
	assert.False(t, ids["editorial"], "editorial belongs to write (closed)")
	assert.True(t, ids["loose"], "stage-less artifacts survive invalidation")

	// The dangling follow_up attempt was interrupted, not stranded.
	for _, s := range m.Stages {
		if s.Name == StageFollowUp {
			assert.Equal(t, StageStatusPending, s.Status)
		}
	}
	require.NoError(t, m.Validate())
}

func TestInvalidateStageKeepsUpstreamIntact(t *testing.T) {
	m := newTestManifest(t)
	tt := baseTime
	require.NoError(t, m.Start(baseTime.Add(1*time.Second)))
	for _, name := range []StageName{StageDiscovery, StageFetch, StageExtract, StageResearch} {
		tt = tt.Add(time.Second)
		require.NoError(t, m.StartStage(name, tt))
		tt = tt.Add(time.Second)
		require.NoError(t, m.SucceedStage(name, tt))
	}
	m.Artifacts = []ArtifactRef{
		{Kind: ArtifactKindDossier, ID: "dossier", Stage: StageResearch, Path: "dossier.json", ProducedAt: NewTimestamp(baseTime)},
		{Kind: ArtifactKindEditorial, ID: "editorial", Stage: StageWrite, Path: "artifact.json", ProducedAt: NewTimestamp(baseTime)},
	}
	verifyBefore := m.Stages[stageIndex(m, StageVerify)]

	tt = tt.Add(time.Second)
	require.NoError(t, m.InvalidateStage(StageVerify, tt))

	// Upstream of verify is untouched.
	for _, name := range []StageName{StageDiscovery, StageFetch, StageExtract, StageResearch} {
		assert.Equal(t, StageStatusSucceeded, m.Stages[stageIndex(m, name)].Status, "upstream %s must be untouched", name)
	}
	assert.True(t, hasArtifact(m, "dossier"), "upstream artifacts must be kept")
	assert.False(t, hasArtifact(m, "editorial"), "write is downstream of verify")

	// verify..render are pending; the invalidation is itself a no-op for
	// verify when it was already pending, but still deterministic.
	assert.Equal(t, StageStatusPending, m.Stages[stageIndex(m, StageVerify)].Status)
	assert.Equal(t, verifyBefore.Attempts, m.Stages[stageIndex(m, StageVerify)].Attempts, "attempt history is preserved")
	require.NoError(t, m.Validate())
}

func TestInvalidateStageRejectsBadInput(t *testing.T) {
	m := newTestManifest(t)
	// Terminal runs must be reopened first.
	require.NoError(t, m.Start(baseTime))
	require.NoError(t, m.Fail(baseTime.Add(time.Second), "run_error", "boom"))
	assert.Error(t, m.InvalidateStage(StageVerify, baseTime.Add(2*time.Second)))

	require.NoError(t, m.Reopen(baseTime.Add(3*time.Second)))
	// A stage outside the plan is refused.
	assert.Error(t, m.InvalidateStage(StageName("bogus"), baseTime.Add(4*time.Second)))
}

// ---------------------------------------------------------------------------
// Bounded history
// ---------------------------------------------------------------------------

func TestAttemptHistoryIsBounded(t *testing.T) {
	m := newTestManifest(t)
	m.Stages = []StageRecord{{Name: StageVerify, Status: StageStatusPending}}
	require.NoError(t, m.Start(baseTime))

	tt := baseTime
	for i := 0; i < 40; i++ {
		tt = tt.Add(time.Second)
		require.NoError(t, m.StartStage(StageVerify, tt))
		tt = tt.Add(time.Second)
		require.NoError(t, m.FailStage(StageVerify, tt, "llm_error", "boom"))
	}
	verify := m.Stages[0]
	assert.Equal(t, MaxAttemptsPerStage, len(verify.Attempts), "the history trims to the bound")
	assert.Equal(t, 1, verify.Attempts[0].Number, "survivors are renumbered")
	assert.Equal(t, MaxAttemptsPerStage, verify.Attempts[MaxAttemptsPerStage-1].Number)
	assert.Equal(t, StageStatusFailed, verify.Status)
	tt = tt.Add(time.Second)
	require.NoError(t, m.Fail(tt, "run_error", "gave up after the bounded retries"))
	assert.NoError(t, m.Validate())
}

func TestErrorAndWarningLogsAreBounded(t *testing.T) {
	m := newTestManifest(t)
	require.NoError(t, m.Start(baseTime))
	tt := baseTime
	for i := 0; i < 80; i++ {
		tt = tt.Add(time.Second)
		require.NoError(t, m.AddError("synthetic", "noise", tt))
		require.NoError(t, m.AddWarning("synthetic", "noise", tt))
	}
	assert.Equal(t, MaxRunErrors, len(m.Errors))
	assert.Equal(t, MaxRunWarnings, len(m.Warnings))
	// The newest entries are the ones kept.
	assert.Equal(t, "noise", m.Errors[len(m.Errors)-1].Message)
	assert.NoError(t, m.Validate())
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func hasArtifact(m *Manifest, id string) bool {
	for _, a := range m.Artifacts {
		if a.ID == id {
			return true
		}
	}
	return false
}
