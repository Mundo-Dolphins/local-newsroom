package run

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestManifest creates a manifest for a realistic newsroom run: an
// explicit-source run over the full canonical pipeline.
func newTestManifest(t *testing.T) *Manifest {
	t.Helper()
	config := &ConfigSnapshot{}
	require.NoError(t, config.Set("omlx_base_url", "http://localhost:8000/v1"))
	require.NoError(t, config.Set("omlx_model", "llama3.1:8b"))
	require.NoError(t, config.Set("omlx_api_key", "sk-a-very-long-raw-secret-0123456789"))

	m, err := NewManifest(NewManifestParams{
		Workspace:  Workspace{Name: "mundodolphins", Path: "workspace/mundodolphins"},
		Topic:      "EU AI Act enters into force",
		SourceMode: SourceModeExplicit,
		Profile:    &Profile{ID: "mint", Hash: "a3f9c81d2b4e6f708192a3b4c5d6e7f8"},
		Model:      NewModelMetadata("omlx", "llama3.1:8b", "http://localhost:8000/v1"),
		Config:     config,
		StartedAt:  baseTime,
	})
	require.NoError(t, err)
	return m
}

func TestNewManifest(t *testing.T) {
	m := newTestManifest(t)

	assert.Equal(t, SchemaVersion, m.SchemaVersion)
	assert.True(t, strings.HasPrefix(string(m.ID), RunIDPrefix))
	assert.Equal(t, RunStatusPending, m.Status)
	assert.Equal(t, SourceModeExplicit, m.SourceMode)
	assert.Len(t, m.Stages, len(CanonicalStages()))
	for _, s := range m.Stages {
		assert.Equal(t, StageStatusPending, s.Status)
		assert.Empty(t, s.Attempts)
	}
	assert.Nil(t, m.FinishedAt)
	assert.Empty(t, m.Result.Outcome)
	assert.Equal(t, m.StartedAt, m.UpdatedAt)
	assert.NoError(t, m.Validate())
}

func TestNewManifestDeterministicID(t *testing.T) {
	params := NewManifestParams{
		Workspace: Workspace{Name: "newsroom"},
		Topic:     "EU AI Act",
		StartedAt: baseTime,
	}
	m1, err1 := NewManifest(params)
	m2, err2 := NewManifest(params)
	require.NoError(t, err1)
	require.NoError(t, err2)
	assert.Equal(t, m1.ID, m2.ID, "same workspace+topic+start must derive the same ID")

	other, err3 := NewManifest(NewManifestParams{
		Workspace: Workspace{Name: "newsroom"},
		Topic:     "A different topic",
		StartedAt: baseTime,
	})
	require.NoError(t, err3)
	assert.NotEqual(t, m1.ID, other.ID)
}

func TestNewManifestValidation(t *testing.T) {
	cases := []struct {
		name   string
		params NewManifestParams
	}{
		{"empty topic", NewManifestParams{Workspace: Workspace{Name: "w"}}},
		{"empty workspace name", NewManifestParams{Topic: "t"}},
		{"bad workspace name", NewManifestParams{Workspace: Workspace{Name: "../evil"}, Topic: "t"}},
		{"bad source mode", NewManifestParams{Workspace: Workspace{Name: "w"}, Topic: "t", SourceMode: "web"}},
		{"duplicate stage", NewManifestParams{
			Workspace: Workspace{Name: "w"}, Topic: "t",
			Stages: []StageName{StageFetch, StageFetch},
		}},
		{"unknown stage", NewManifestParams{
			Workspace: Workspace{Name: "w"}, Topic: "t",
			Stages: []StageName{StageFetch, "scrape"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewManifest(tc.params)
			assert.Error(t, err)
		})
	}

	// An explicit subset of stages is allowed (not every run runs everything).
	m, err := NewManifest(NewManifestParams{
		Workspace: Workspace{Name: "w"},
		Topic:     "t",
		Stages:    []StageName{StageResearch, StageWrite, StageRender},
	})
	require.NoError(t, err)
	assert.Len(t, m.Stages, 3)
}

func TestRunIDValidate(t *testing.T) {
	id := NewRunID("newsroom", "EU AI Act", baseTime)
	assert.NoError(t, id.Validate())

	cases := []struct {
		name string
		id   RunID
	}{
		{"empty", ""},
		{"no prefix", RunID("runx-abc123")},
		{"bad charset", RunID("run-hello world")},
		{"too long", RunID("run-" + strings.Repeat("a", 129))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Error(t, tc.id.Validate())
		})
	}
	// A 128-char body is the boundary and must be accepted.
	assert.NoError(t, RunID("run-"+strings.Repeat("a", 128)).Validate())
}

// TestManifestLifecycleSuccess walks the full happy path and checks the final
// manifest state.
func TestManifestLifecycleSuccess(t *testing.T) {
	m := newTestManifest(t)

	require.NoError(t, m.Start(at(1)))
	assert.Equal(t, RunStatusRunning, m.Status)

	require.NoError(t, m.SkipStage(StageDiscovery, at(2), "explicit URLs provided"))
	require.NoError(t, m.SkipStage(StageFollowUp, at(3), "will not follow up"))

	require.NoError(t, m.StartStage(StageFetch, at(4)))
	require.NoError(t, m.AddArtifact(ArtifactRef{
		Kind: ArtifactKindSource, ID: "source-1", Stage: StageFetch, ProducedAt: ts(10),
	}))
	require.NoError(t, m.SucceedStage(StageFetch, at(10)))

	require.NoError(t, m.StartStage(StageExtract, at(11)))
	require.NoError(t, m.AddArtifact(ArtifactRef{
		Kind: ArtifactKindDocument, ID: "doc-1", Stage: StageExtract, ProducedAt: ts(15),
	}))
	require.NoError(t, m.SucceedStage(StageExtract, at(15)))

	require.NoError(t, m.StartStage(StageResearch, at(16)))
	require.NoError(t, m.AddArtifact(ArtifactRef{
		Kind: ArtifactKindDossier, ID: "dossier-1", Stage: StageResearch, ProducedAt: ts(40),
	}))
	require.NoError(t, m.SucceedStage(StageResearch, at(40)))

	require.NoError(t, m.StartStage(StageVerify, at(41)))
	require.NoError(t, m.AddWarning("slow_stage", "research took longer than expected", at(45)))
	require.NoError(t, m.AddArtifact(ArtifactRef{
		Kind: ArtifactKindVerification, ID: "verification-1", Stage: StageVerify, ProducedAt: ts(50),
	}))
	require.NoError(t, m.SucceedStage(StageVerify, at(50)))

	require.NoError(t, m.StartStage(StageWrite, at(51)))
	require.NoError(t, m.AddArtifact(ArtifactRef{
		Kind: ArtifactKindEditorial, ID: "editorial-1", Stage: StageWrite, ProducedAt: ts(80),
	}))
	require.NoError(t, m.SucceedStage(StageWrite, at(80)))

	require.NoError(t, m.StartStage(StageFinalCheck, at(81)))
	require.NoError(t, m.AddArtifact(ArtifactRef{
		Kind: ArtifactKindFactualCheck, ID: "check-1", Stage: StageFinalCheck, ProducedAt: ts(90),
	}))
	require.NoError(t, m.SucceedStage(StageFinalCheck, at(90)))

	require.NoError(t, m.StartStage(StageRender, at(91)))
	require.NoError(t, m.AddArtifact(ArtifactRef{
		Kind: ArtifactKindRendered, ID: "render-1", Stage: StageRender, ProducedAt: ts(100),
	}))
	require.NoError(t, m.SucceedStage(StageRender, at(100)))

	require.NoError(t, m.Succeed(at(101)))

	assert.Equal(t, RunStatusSucceeded, m.Status)
	assert.Equal(t, OutcomeSucceeded, m.Result.Outcome)
	assert.NotNil(t, m.FinishedAt)
	assert.Equal(t, ts(101), *m.FinishedAt)
	assert.Len(t, m.Artifacts, 7)
	assert.Len(t, m.Warnings, 1)
	assert.Empty(t, m.Errors)
	assert.NoError(t, m.Validate())

	statuses := map[StageName]StageStatus{}
	for _, s := range m.Stages {
		statuses[s.Name] = s.Status
	}
	assert.Equal(t, StageStatusSkipped, statuses[StageDiscovery])
	assert.Equal(t, StageStatusSucceeded, statuses[StageFetch])
	assert.Equal(t, StageStatusSkipped, statuses[StageFollowUp])
}

// TestManifestLifecycleRetryAndFailure exercises the retry-on-failure path and
// a run-level failure.
func TestManifestLifecycleRetryAndFailure(t *testing.T) {
	m := newTestManifest(t)

	require.NoError(t, m.Start(at(1)))
	require.NoError(t, m.SkipStage(StageDiscovery, at(2), "explicit URLs provided"))

	// First fetch attempt fails.
	require.NoError(t, m.StartStage(StageFetch, at(3)))
	require.NoError(t, m.FailStage(StageFetch, at(8), "fetch_error", "GET failed: Bearer abc123def456 expired"))
	assert.Equal(t, StageStatusFailed, m.Stages[stageIndex(m, StageFetch)].Status)
	assert.Len(t, m.Stages[stageIndex(m, StageFetch)].Attempts, 1)
	assert.Len(t, m.Errors, 1)
	assert.NotContains(t, m.Errors[0].Message, "abc123def456", "the failure message must be redacted")
	assert.Contains(t, m.Errors[0].Message, RedactedPlaceholder)

	// Retry with a second attempt; it succeeds.
	require.NoError(t, m.StartStage(StageFetch, at(9)))
	fetch := &m.Stages[stageIndex(m, StageFetch)]
	assert.Len(t, fetch.Attempts, 2)
	assert.Equal(t, 1, fetch.Attempts[0].Number)
	assert.Equal(t, 2, fetch.Attempts[1].Number)
	require.NoError(t, m.SucceedStage(StageFetch, at(12)))
	assert.Equal(t, StageStatusSucceeded, fetch.Status)

	// Research fails and the run fails.
	require.NoError(t, m.StartStage(StageResearch, at(13)))
	require.NoError(t, m.FailStage(StageResearch, at(20), "llm_error", "model request timed out"))
	require.NoError(t, m.Fail(at(21), "run_failed", "research could not be completed"))

	assert.Equal(t, RunStatusFailed, m.Status)
	assert.Equal(t, OutcomeFailed, m.Result.Outcome)
	assert.Equal(t, "research could not be completed", m.Result.Reason)
	require.NotNil(t, m.Result.FailedStage)
	assert.Equal(t, StageResearch, *m.Result.FailedStage)
	assert.NotNil(t, m.FinishedAt)

	// Never-started stages stay pending; a failed run may have pending stages.
	for _, name := range []StageName{StageVerify, StageWrite, StageFinalCheck, StageRender} {
		assert.Equal(t, StageStatusPending, m.Stages[stageIndex(m, name)].Status)
	}
	assert.NoError(t, m.Validate())

	// Terminal runs are immutable.
	assert.Error(t, m.StartStage(StageWrite, at(22)))
	assert.Error(t, m.Start(at(23)))
	assert.Error(t, m.Succeed(at(24)))
	assert.Error(t, m.AddWarning("late", "too late", at(25)))
}

func TestManifestLifecycleCancel(t *testing.T) {
	m := newTestManifest(t)

	require.NoError(t, m.Start(at(1)))
	require.NoError(t, m.StartStage(StageFetch, at(2)))
	require.NoError(t, m.Cancel(at(5), "user requested stop"))

	assert.Equal(t, RunStatusCancelled, m.Status)
	assert.Equal(t, OutcomeCancelled, m.Result.Outcome)
	assert.Equal(t, "user requested stop", m.Result.Reason)
	assert.Equal(t, StageStatusCancelled, m.Stages[stageIndex(m, StageFetch)].Status)
	assert.Equal(t, StageStatusCancelled, m.Stages[stageIndex(m, StageFetch)].Attempts[0].Status)
	assert.NotNil(t, m.FinishedAt)
	assert.NoError(t, m.Validate())

	// A pending run may be cancelled before it starts.
	fresh := newTestManifest(t)
	require.NoError(t, fresh.Cancel(at(1), "removed from queue"))
	assert.Equal(t, RunStatusCancelled, fresh.Status)
	assert.NotNil(t, fresh.FinishedAt)
	for _, s := range fresh.Stages {
		assert.Equal(t, StageStatusPending, s.Status)
	}
	assert.NoError(t, fresh.Validate())
}

func TestManifestRejectsInvalidTransitions(t *testing.T) {
	m := newTestManifest(t)

	// Stage-level invalid transitions.
	assert.Error(t, m.SucceedStage(StageFetch, at(1)), "cannot succeed a pending stage")
	assert.Error(t, m.StartStage(StageName("scrape"), at(1)), "unknown stage")
	assert.Error(t, m.SkipStage(StageDiscovery, at(1), ""), "skip requires a reason")

	// Run-level invalid transitions from pending.
	assert.Error(t, m.Succeed(at(1)), "a pending run cannot succeed")
	assert.Error(t, m.Fail(at(1), "code", "msg"), "a pending run cannot fail")

	// Start the run, then reject repeated/illegal transitions.
	require.NoError(t, m.Start(at(2)))
	assert.Error(t, m.Start(at(3)), "running -> running is invalid")
	assert.Error(t, m.Succeed(at(4)), "running run cannot succeed with pending stages")

	// Skip from running is invalid.
	require.NoError(t, m.StartStage(StageFetch, at(5)))
	assert.Error(t, m.SkipStage(StageFetch, at(6), "no"), "cannot skip a running stage")

	// Duplicate start is invalid.
	require.NoError(t, m.Cancel(at(7), "user requested stop"))
	assert.Error(t, m.Cancel(at(8), "again"), "a cancelled run cannot be cancelled again")
}

func TestManifestRejectsTimestampRegression(t *testing.T) {
	m := newTestManifest(t)
	require.NoError(t, m.Start(at(100)))
	assert.Error(t, m.SkipStage(StageDiscovery, at(50), "backwards"), "timestamps must not regress")
}

func TestManifestSucceedRequiresFullPlan(t *testing.T) {
	m := newTestManifest(t)
	require.NoError(t, m.Start(at(1)))
	// Skip everything except research, then complete research only.
	for _, name := range []StageName{
		StageDiscovery, StageFetch, StageExtract, StageVerify, StageFollowUp, StageWrite, StageFinalCheck, StageRender,
	} {
		require.NoError(t, m.SkipStage(name, at(2), "test"))
	}
	assert.Error(t, m.Succeed(at(3)), "pending research must block success")

	require.NoError(t, m.StartStage(StageResearch, at(4)))
	require.NoError(t, m.SucceedStage(StageResearch, at(5)))
	require.NoError(t, m.Succeed(at(6)))
	assert.Equal(t, RunStatusSucceeded, m.Status)
}

func TestManifestArtifactRules(t *testing.T) {
	m := newTestManifest(t)
	require.NoError(t, m.Start(at(1)))

	assert.Error(t, m.AddArtifact(ArtifactRef{
		Kind: ArtifactKindSource, ID: "x", Stage: "scrape", ProducedAt: ts(2),
	}), "unknown stage reference")
	assert.Error(t, m.AddArtifact(ArtifactRef{
		Kind: ArtifactKindSource, ID: "x", Stage: StageFetch, ProducedAt: ts(2),
	}), "stage is still pending")

	require.NoError(t, m.SkipStage(StageDiscovery, at(2), "explicit"))
	assert.Error(t, m.AddArtifact(ArtifactRef{
		Kind: ArtifactKindSource, ID: "x", Stage: StageDiscovery, ProducedAt: ts(3),
	}), "skipped stage produces no artifacts")

	require.NoError(t, m.StartStage(StageFetch, at(4)))
	ref := ArtifactRef{Kind: ArtifactKindSource, ID: "src-1", Stage: StageFetch, ProducedAt: ts(5)}
	require.NoError(t, m.AddArtifact(ref))
	assert.Error(t, m.AddArtifact(ref), "duplicate artifact id")
}

func TestConfigSnapshot(t *testing.T) {
	s := &ConfigSnapshot{}
	require.NoError(t, s.Set("omlx_base_url", "http://localhost:8000/v1"))
	require.NoError(t, s.Set("omlx_model", "llama3.1:8b"))
	require.NoError(t, s.Set("omlx_api_key", "sk-a-very-long-raw-secret-0123456789"))
	require.NoError(t, s.Set("omlx_api_key", ""))

	val, ok := s.Get("omlx_model")
	assert.True(t, ok)
	assert.Equal(t, "llama3.1:8b", val)
	_, ok = s.Get("omlx_api_key")
	assert.False(t, ok, "an empty value must remove the key")
	assert.Equal(t, []string{"omlx_base_url", "omlx_model"}, s.Keys())
	assert.NoError(t, s.Validate())

	// Secret-like keys must end up redacted, never raw.
	s2 := &ConfigSnapshot{}
	require.NoError(t, s2.Set("api_key", "sk-a-very-long-raw-secret-0123456789"))
	assert.Equal(t, RedactedPlaceholder, s2.Values["api_key"])
	assert.NoError(t, s2.Validate())

	// A non-secret key whose value looks like a secret must also be redacted.
	s3 := &ConfigSnapshot{}
	require.NoError(t, s3.Set("note", "token = abc123def456"))
	assert.Equal(t, "token = [REDACTED]", s3.Values["note"])
	assert.NoError(t, s3.Validate())

	// Invalid keys are rejected.
	s4 := &ConfigSnapshot{}
	assert.Error(t, s4.Set("bad key!", "x"))
	assert.Error(t, s4.Set("", "x"))

	// A manually injected raw secret under a secret-like key must fail validation.
	bad := ConfigSnapshot{Values: map[string]string{"api_key": "sk-a-very-long-raw-secret-0123456789"}}
	assert.Error(t, bad.Validate())
}

func TestModelMetadataValidate(t *testing.T) {
	valid := ModelMetadata{Provider: "omlx", Model: "llama3.1:8b", Endpoint: "http://localhost:8000/v1"}
	assert.NoError(t, valid.Validate())

	credentialEndpoint := ModelMetadata{Provider: "omlx", Model: "m", Endpoint: "http://user:pass123@localhost:8000/v1"}
	assert.Error(t, credentialEndpoint.Validate(), "real credentials must be rejected")

	notAURL := ModelMetadata{Endpoint: "not-a-url"}
	assert.Error(t, notAURL.Validate())

	wrongScheme := ModelMetadata{Endpoint: "ftp://host:8000/v1"}
	assert.Error(t, wrongScheme.Validate())

	redacted := NewModelMetadata("omlx", "m", "http://user:pass123@localhost:8000/v1")
	assert.NoError(t, redacted.Validate(), "a redacted endpoint must validate")
}

func TestResultValidate(t *testing.T) {
	assert.NoError(t, Result{}.Validate(), "in-progress result is valid")
	assert.NoError(t, (Result{Outcome: OutcomeSucceeded}).Validate())
	assert.Error(t, (Result{Outcome: OutcomeFailed}).Validate(), "failed requires a reason")
	assert.Error(t, (Result{Outcome: "exploded"}).Validate())

	stage := StageWrite
	assert.Error(t, (Result{Outcome: OutcomeCancelled, FailedStage: &stage}).Validate(), "failed_stage only for failed results")
}

// cloneManifest deep-copies a manifest for structural tampering tests: the
// slice headers and their elements are copied so mutating the clone never
// touches the original.
func cloneManifest(m *Manifest) *Manifest {
	c := *m
	c.Stages = make([]StageRecord, len(m.Stages))
	for i := range m.Stages {
		c.Stages[i] = m.Stages[i]
		c.Stages[i].Attempts = append([]StageAttempt(nil), m.Stages[i].Attempts...)
	}
	c.Artifacts = append([]ArtifactRef(nil), m.Artifacts...)
	c.Warnings = append([]Warning(nil), m.Warnings...)
	c.Errors = append([]ErrorSummary(nil), m.Errors...)
	if m.FinishedAt != nil {
		fa := *m.FinishedAt
		c.FinishedAt = &fa
	}
	if m.Profile != nil {
		p := *m.Profile
		c.Profile = &p
	}
	return &c
}

func TestManifestValidateStructural(t *testing.T) {
	m := newTestManifest(t)
	require.NoError(t, m.Validate())

	// A nil manifest is invalid.
	assert.Error(t, (*Manifest)(nil).Validate())

	// Unknown enum values are rejected.
	bad := cloneManifest(m)
	bad.Status = "exploded"
	assert.Error(t, bad.Validate())
	bad = cloneManifest(m)
	bad.SourceMode = "web"
	assert.Error(t, bad.Validate())
	bad = cloneManifest(m)
	bad.Stages[0].Status = "exploded"
	assert.Error(t, bad.Validate())

	// A succeeded run with a failed stage is inconsistent.
	bad = cloneManifest(m)
	bad.Status = RunStatusSucceeded
	bad.Stages[0].Status = StageStatusFailed
	bad.Result.Outcome = OutcomeSucceeded
	assert.Error(t, bad.Validate())

	// A running run with a terminal outcome is inconsistent.
	bad = cloneManifest(m)
	bad.Status = RunStatusRunning
	bad.Result.Outcome = OutcomeFailed
	assert.Error(t, bad.Validate())

	// Terminal run without finished_at is inconsistent.
	bad = cloneManifest(m)
	bad.Status = RunStatusFailed
	bad.FinishedAt = nil
	bad.Result = Result{Outcome: OutcomeFailed, Reason: "x"}
	assert.Error(t, bad.Validate())

	// In-progress run with finished_at is inconsistent.
	bad = cloneManifest(m)
	bad.FinishedAt = ptr(ts(1))
	assert.Error(t, bad.Validate())

	// A failed result must name a stage that actually failed.
	bad = cloneManifest(m)
	bad.Result = Result{Outcome: OutcomeFailed, Reason: "x", FailedStage: ptr(StageWrite)}
	bad.Status = RunStatusFailed
	bad.FinishedAt = ptr(ts(1))
	assert.Error(t, bad.Validate())

	// Duplicate stage names are rejected.
	bad = cloneManifest(m)
	bad.Stages = append(bad.Stages, StageRecord{Name: StageFetch, Status: StageStatusPending})
	assert.Error(t, bad.Validate())

	// A warning containing a raw secret is rejected.
	bad = cloneManifest(m)
	bad.Warnings = append(bad.Warnings, Warning{Code: "w", Message: "token = abc123def456", OccurredAt: ts(1)})
	assert.Error(t, bad.Validate())
}

func TestManifestValidateSchemaVersion(t *testing.T) {
	m := newTestManifest(t)
	require.NoError(t, m.Validate())

	m.SchemaVersion = "1.0.0"
	err := m.Validate()
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUnsupportedSchemaVersion))

	m.SchemaVersion = "not-a-version"
	assert.Error(t, m.Validate())
}

func stageIndex(m *Manifest, name StageName) int {
	for i := range m.Stages {
		if m.Stages[i].Name == name {
			return i
		}
	}
	return -1
}
