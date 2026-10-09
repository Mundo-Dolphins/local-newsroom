package run

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWorkspaceValidateEdgeCases covers the remaining validation branches.
func TestWorkspaceValidateEdgeCases(t *testing.T) {
	valid := []Workspace{
		{Name: "newsroom"},
		{Name: "a"},
		{Name: "my.ws"},
		{Name: strings.Repeat("a", 64)},
		{Name: "newsroom", Path: "workspace/newsroom"},
	}
	for _, w := range valid {
		assert.NoError(t, w.Validate(), "%+v", w)
	}

	invalid := []Workspace{
		{Name: ""},
		{Name: ".hidden"},
		{Name: strings.Repeat("a", 65)},
		{Name: "newsroom", Path: "workspace/../etc"},
		{Name: "newsroom", Path: `workspace\..\etc`},
		{Name: "newsroom", Path: ".."},
		{Name: "newsroom", Path: "a/.."},
	}
	for _, w := range invalid {
		assert.Error(t, w.Validate(), "%+v", w)
	}
}

func TestContainsTraversal(t *testing.T) {
	traversing := []string{"..", "a/..", "a/../b", "a\\..\\b", "/..", "x/../y", "..a/../b"}
	for _, p := range traversing {
		assert.True(t, containsTraversal(p), "%q", p)
	}
	// Dots that are not a full ".." segment do not count.
	safe := []string{"", "a", "a/b/c", "a..b", "..hidden", "workspace/newsroom"}
	for _, p := range safe {
		assert.False(t, containsTraversal(p), "%q", p)
	}
}

func TestProfileValidate(t *testing.T) {
	assert.NoError(t, (Profile{ID: "mint"}).Validate())
	assert.NoError(t, (Profile{ID: "mint", SourcePath: "profiles/mint", Hash: "a3f9c81d2b4e6f708192a3b4c5d6e7f8"}).Validate())

	assert.Error(t, (Profile{}).Validate(), "empty id")
	assert.Error(t, (Profile{ID: "bad id!"}).Validate(), "invalid id characters")
	assert.Error(t, (Profile{ID: "mint", Hash: "ABCDEF01"}).Validate(), "uppercase hash")
	assert.Error(t, (Profile{ID: "mint", Hash: "abc"}).Validate(), "short hash")
	assert.Error(t, (Profile{ID: "mint", SourcePath: "../profiles/mint"}).Validate(), "traversal")
}

func TestModelMetadataEdgeCases(t *testing.T) {
	assert.Error(t, (ModelMetadata{Provider: strings.Repeat("a", 129)}).Validate(), "provider too long")
	assert.Error(t, (ModelMetadata{Model: strings.Repeat("a", 129)}).Validate(), "model too long")
	assert.Error(t, (ModelMetadata{Endpoint: "http://" + strings.Repeat("a", 129)}).Validate(), "endpoint too long")
	assert.Error(t, (ModelMetadata{Provider: "omlx_token=abc123def456"}).Validate(), "secret-looking provider")
	assert.Error(t, (ModelMetadata{Model: "api_key: abc123def456"}).Validate(), "secret-looking model")
}

func TestEnumTables(t *testing.T) {
	// RunStatus.Terminal.
	runTerminal := map[RunStatus]bool{
		RunStatusPending:   false,
		RunStatusRunning:   false,
		RunStatusSucceeded: true,
		RunStatusFailed:    true,
		RunStatusCancelled: true,
		RunStatus(""):      false,
	}
	for s, want := range runTerminal {
		assert.Equal(t, want, s.Terminal(), "RunStatus(%q)", s)
	}

	// SourceMode.Valid.
	assert.True(t, SourceModeExplicit.Valid())
	assert.True(t, SourceModeDiscovered.Valid())
	assert.True(t, SourceModeSupplement.Valid())
	assert.True(t, SourceModeUnknown.Valid())
	assert.False(t, SourceMode("web").Valid())
	assert.Equal(t, "explicit", SourceModeExplicit.String())

	// Outcome.Terminal.
	assert.False(t, Outcome("").Terminal())
	assert.True(t, OutcomeSucceeded.Terminal())
	assert.True(t, OutcomeFailed.Terminal())
	assert.True(t, OutcomeCancelled.Terminal())

	// ArtifactKind.Valid.
	for _, k := range AllArtifactKinds {
		assert.True(t, k.Valid(), "%q", k)
	}
	assert.False(t, ArtifactKind("scraped").Valid())
}

func TestErrorSummaryFrom(t *testing.T) {
	s := ErrorSummaryFrom("boom", nil, baseTime)
	assert.Empty(t, s)

	s = ErrorSummaryFrom("llm_error", errors.New("request failed: Bearer abc123def456 expired"), baseTime)
	assert.Equal(t, "llm_error", s.Code)
	assert.NotContains(t, s.Message, "abc123def456")
	assert.Contains(t, s.Message, RedactedPlaceholder)
	assert.NoError(t, s.Validate())
}

func TestConfigSnapshotValidateEdgeCases(t *testing.T) {
	assert.NoError(t, (&ConfigSnapshot{}).Validate(), "empty snapshot is valid")
	assert.NoError(t, (&ConfigSnapshot{Values: map[string]string{}}).Validate())

	badKey := &ConfigSnapshot{Values: map[string]string{"bad key!": "x"}}
	assert.Error(t, badKey.Validate())

	secretValue := &ConfigSnapshot{Values: map[string]string{"omlx_model": "token = abc123def456"}}
	assert.Error(t, secretValue.Validate())
}

func TestResultValidateEdgeCases(t *testing.T) {
	assert.Error(t, (Result{Outcome: OutcomeFailed, Reason: strings.Repeat("x", 8193)}).Validate(), "reason too long")
	assert.Error(t, (Result{Outcome: OutcomeFailed, Reason: "api_key: abc123def456"}).Validate(), "secret in reason")

	badStage := StageName("scrape")
	assert.Error(t, (Result{Outcome: OutcomeFailed, Reason: "x", FailedStage: &badStage}).Validate(), "unknown failed_stage")

	assert.NoError(t, (Result{Outcome: OutcomeFailed, Reason: "x", FailedStage: ptr(StageWrite)}).Validate())
}

func TestSchemaMajorEdgeCases(t *testing.T) {
	valid := map[string]int{
		"0.5.0":   0,
		"1.2.3":   1,
		"10.0.1":  10,
		" 2.3.4 ": 2,
	}
	for v, want := range valid {
		got, err := SchemaMajor(v)
		assert.NoError(t, err, "%q", v)
		assert.Equal(t, want, got, "%q", v)
	}

	for _, v := range []string{"", "1", "1.2", "1.2.3.4", "1.x.3", "v1.2.3", "1.2."} {
		_, err := SchemaMajor(v)
		assert.Error(t, err, "%q", v)
	}
}

func TestTimestampEdgeCases(t *testing.T) {
	// Now is a recent, non-zero UTC timestamp.
	n := Now()
	assert.False(t, n.IsZero())
	assert.WithinDuration(t, time.Now().UTC(), n.Time(), time.Minute)

	// Zero renders as the empty string.
	assert.Empty(t, Timestamp{}.String())

	// Non-string input is rejected.
	var ts Timestamp
	err := ts.UnmarshalJSON([]byte(`42`))
	assert.Error(t, err)

	// Malformed strings are rejected.
	err = ts.UnmarshalJSON([]byte(`"not-a-time"`))
	assert.Error(t, err)

	// Empty string and null both yield a zero timestamp.
	require.NoError(t, ts.UnmarshalJSON([]byte(`""`)))
	assert.True(t, ts.IsZero())
	require.NoError(t, ts.UnmarshalJSON([]byte(`null`)))
	assert.True(t, ts.IsZero())

	// Non-UTC offsets are normalized to UTC.
	require.NoError(t, ts.UnmarshalJSON([]byte(`"2024-01-15T14:30:00+02:00"`)))
	assert.Equal(t, "2024-01-15T12:30:00Z", ts.String())
}

func TestRedactURLInTextEdgeCases(t *testing.T) {
	// Trailing punctuation must not be swallowed into the URL.
	out := RedactSecrets("see https://host.example/path?api_key=abc123def456. done")
	assert.Contains(t, out, "[REDACTED]")
	assert.NotContains(t, out, "abc123def456")
	assert.True(t, strings.HasSuffix(out, "done"), "trailing prose must survive: %q", out)

	// Non-secret query parameters survive.
	out = RedactSecrets("https://host.example/path?next=abc123def456&ok=1")
	assert.Contains(t, out, "next=abc123def456")

	// An unparseable URL is left alone (non-secret query survives).
	out = RedactSecrets("https://?next=abc123def456")
	assert.Contains(t, out, "next=abc123def456")

	// A userinfo-only URL is redacted even with a trailing comma.
	out = RedactSecrets("GET https://user:pass1234@host.example/x, then stop")
	assert.Contains(t, out, "[REDACTED]")
	assert.NotContains(t, out, "pass1234")
}

func TestShannonEntropyEdgeCases(t *testing.T) {
	assert.InDelta(t, 0.0, shannonEntropy("aaaa"), 1e-9)
	assert.InDelta(t, 1.0, shannonEntropy("ab"), 1e-9)
	assert.InDelta(t, 2.0, shannonEntropy("abcd"), 1e-9)
}

// TestManifestValidateConsistencyBranches exercises the cross-field branches
// not reached by the happy-path scenarios.
func TestManifestValidateConsistencyBranches(t *testing.T) {
	cases := []struct {
		name string
		mut  func(m *Manifest)
	}{
		{
			"topic too long",
			func(m *Manifest) { m.Topic = strings.Repeat("t", 1001) },
		},
		{
			"pending run with a running stage",
			func(m *Manifest) {
				m.Stages[0].Status = StageStatusRunning
				m.Stages[0].Attempts = []StageAttempt{{Number: 1, Status: StageStatusRunning, StartedAt: ts(1)}}
			},
		},
		{
			"running run with all stages terminal",
			func(m *Manifest) {
				m.Status = RunStatusRunning
				for i := range m.Stages {
					m.Stages[i].Status = StageStatusSucceeded
					m.Stages[i].Attempts = []StageAttempt{{
						Number: 1, Status: StageStatusSucceeded, StartedAt: ts(1), FinishedAt: ptr(ts(2)),
					}}
				}
			},
		},
		{
			"failed run with a running stage",
			func(m *Manifest) {
				m.Status = RunStatusFailed
				m.FinishedAt = ptr(ts(1))
				m.Result = Result{Outcome: OutcomeFailed, Reason: "x"}
				m.Stages[0].Status = StageStatusRunning
				m.Stages[0].Attempts = []StageAttempt{{Number: 1, Status: StageStatusRunning, StartedAt: ts(1)}}
			},
		},
		{
			"artifact references a stage not planned",
			func(m *Manifest) {
				// Plan only fetch/research, then reference verify.
				m.Stages = []StageRecord{
					{Name: StageFetch, Status: StageStatusPending},
					{Name: StageResearch, Status: StageStatusPending},
				}
				m.Artifacts = []ArtifactRef{{
					Kind: ArtifactKindDossier, ID: "d-1", Stage: StageVerify, ProducedAt: ts(1),
				}}
			},
		},
		{
			"zero finished_at when set",
			func(m *Manifest) {
				m.Status = RunStatusFailed
				m.Result = Result{Outcome: OutcomeFailed, Reason: "x"}
				m.FinishedAt = ptr(Timestamp{})
			},
		},
		{
			"finished_at earlier than started_at",
			func(m *Manifest) {
				m.Status = RunStatusFailed
				m.Result = Result{Outcome: OutcomeFailed, Reason: "x"}
				m.FinishedAt = ptr(ts(-1))
				m.UpdatedAt = ts(-1)
			},
		},
		{
			"updated_at earlier than started_at",
			func(m *Manifest) { m.UpdatedAt = ts(-1) },
		},
		{
			"missing started_at",
			func(m *Manifest) { m.StartedAt = Timestamp{} },
		},
		{
			"missing updated_at",
			func(m *Manifest) { m.UpdatedAt = Timestamp{} },
		},
		{
			"succeeded run with a failed outcome",
			func(m *Manifest) {
				m.Status = RunStatusSucceeded
				m.FinishedAt = ptr(ts(1))
				m.Result = Result{Outcome: OutcomeFailed, Reason: "x"}
			},
		},
		{
			"failed run with a succeeded outcome",
			func(m *Manifest) {
				m.Status = RunStatusFailed
				m.FinishedAt = ptr(ts(1))
				m.Result = Result{Outcome: OutcomeSucceeded}
			},
		},
		{
			"cancelled run with a failed outcome",
			func(m *Manifest) {
				m.Status = RunStatusCancelled
				m.FinishedAt = ptr(ts(1))
				m.Result = Result{Outcome: OutcomeFailed, Reason: "x"}
			},
		},
		{
			"artifact path traversal",
			func(m *Manifest) {
				m.Artifacts = []ArtifactRef{{
					Kind: ArtifactKindSource, ID: "a-1", Stage: StageFetch,
					Path: "../outside.md", ProducedAt: ts(1),
				}}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := cloneManifest(newTestManifest(t))
			tc.mut(m)
			assert.Error(t, m.Validate())
		})
	}
}

// TestManifestValidateMultipleErrors checks that validation reports all
// problems, not just the first.
func TestManifestValidateMultipleErrors(t *testing.T) {
	m := cloneManifest(newTestManifest(t))
	m.Status = RunStatus("exploded")
	m.Topic = ""
	m.SourceMode = SourceMode("web")
	m.SchemaVersion = "bogus"
	err := m.Validate()
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "status")
	assert.Contains(t, msg, "topic")
	assert.Contains(t, msg, "source mode")
	assert.Contains(t, msg, "schema_version")
}

func TestFailWithRunningStages(t *testing.T) {
	m := newTestManifest(t)
	require.NoError(t, m.Start(at(1)))
	require.NoError(t, m.StartStage(StageFetch, at(2)))
	require.NoError(t, m.StartStage(StageExtract, at(3)))

	require.NoError(t, m.Fail(at(10), "config_error", "the model endpoint is unreachable"))

	assert.Equal(t, RunStatusFailed, m.Status)
	// Every in-flight stage is failed with the run-level summary.
	for _, name := range []StageName{StageFetch, StageExtract} {
		rec := &m.Stages[stageIndex(m, name)]
		assert.Equal(t, StageStatusFailed, rec.Status, "%s", name)
		require.NotEmpty(t, rec.Attempts)
		assert.Equal(t, StageStatusFailed, rec.Attempts[len(rec.Attempts)-1].Status)
	}
	// Never-started stages stay pending.
	assert.Equal(t, StageStatusPending, m.Stages[stageIndex(m, StageWrite)].Status)
	assert.Len(t, m.Errors, 2, "one summary per killed stage")
	require.NotNil(t, m.Result.FailedStage)
	assert.Equal(t, StageExtract, *m.Result.FailedStage, "the last failed stage is the origin")
	assert.NoError(t, m.Validate())

	// A failure with an empty message is rejected.
	assert.Error(t, m.Fail(at(11), "x", ""))
}

func TestStartStagePromotesRunToRunning(t *testing.T) {
	m := newTestManifest(t)
	assert.Equal(t, RunStatusPending, m.Status)
	require.NoError(t, m.StartStage(StageFetch, at(1)))
	assert.Equal(t, RunStatusRunning, m.Status)
	assert.NoError(t, m.Validate())
}

func TestCancelStage(t *testing.T) {
	m := newTestManifest(t)
	require.NoError(t, m.Start(at(1)))
	require.NoError(t, m.StartStage(StageFetch, at(2)))

	// Cancelling a non-running stage is invalid.
	assert.Error(t, m.CancelStage(StageResearch, at(3)))

	require.NoError(t, m.CancelStage(StageFetch, at(4)))
	rec := &m.Stages[stageIndex(m, StageFetch)]
	assert.Equal(t, StageStatusCancelled, rec.Status)
	require.NotEmpty(t, rec.Attempts)
	assert.Equal(t, StageStatusCancelled, rec.Attempts[0].Status)
	require.NotNil(t, rec.Attempts[0].FinishedAt)
	assert.NoError(t, m.Validate())
}

func TestFinishStageAttemptEdgeCases(t *testing.T) {
	m := newTestManifest(t)
	require.NoError(t, m.Start(at(1)))

	// A tampered record: status running but the last attempt already ended.
	m.Stages[stageIndex(m, StageFetch)] = StageRecord{
		Name:   StageFetch,
		Status: StageStatusRunning,
		Attempts: []StageAttempt{{
			Number: 1, Status: StageStatusSucceeded, StartedAt: ts(2), FinishedAt: ptr(ts(3)),
		}},
	}
	assert.Error(t, m.SucceedStage(StageFetch, at(4)))

	// A tampered record: status succeeded but no attempts at all.
	m.Stages[stageIndex(m, StageExtract)] = StageRecord{Name: StageExtract, Status: StageStatusSucceeded}
	assert.Error(t, m.SucceedStage(StageExtract, at(5)))

	// Succeeding a skipped stage is an invalid transition.
	require.NoError(t, m.SkipStage(StageDiscovery, at(6), "test"))
	assert.Error(t, m.SucceedStage(StageDiscovery, at(7)))
}

func TestMutatorEdgeCases(t *testing.T) {
	m := newTestManifest(t)
	require.NoError(t, m.Start(at(1)))

	// FailStage requires a message.
	require.NoError(t, m.StartStage(StageFetch, at(2)))
	assert.Error(t, m.FailStage(StageFetch, at(3), "code", ""))
	// FailStage rejects an invalid summary code.
	assert.Error(t, m.FailStage(StageFetch, at(3), "Bad Code", "message"))

	// SkipStage unknown stage / on a terminal run.
	assert.Error(t, m.SkipStage(StageName("scrape"), at(4), "x"))
	require.NoError(t, m.SucceedStage(StageFetch, at(5)))
	require.NoError(t, m.Cancel(at(6), "stop"))
	assert.Error(t, m.SkipStage(StageResearch, at(7), "x"), "terminal run is immutable")

	// AddError and StageStatusOf.
	fresh := newTestManifest(t)
	require.NoError(t, fresh.Start(at(1)))
	require.NoError(t, fresh.AddError("llm_error", "request timed out", at(2)))
	assert.Len(t, fresh.Errors, 1)
	assert.Equal(t, "llm_error", fresh.Errors[0].Code)
	assert.Error(t, fresh.AddError("Bad Code", "x", at(3)))

	status, err := fresh.StageStatusOf(StageFetch)
	require.NoError(t, err)
	assert.Equal(t, StageStatusPending, status)
	_, err = fresh.StageStatusOf(StageName("scrape"))
	assert.Error(t, err)

	// AddWarning timestamp regression.
	require.NoError(t, fresh.AddWarning("w", "hello", at(10)))
	assert.Error(t, fresh.AddWarning("w", "backwards", at(5)))

	// AddArtifact timestamp regression and invalid id.
	require.NoError(t, fresh.StartStage(StageFetch, at(11)))
	assert.Error(t, fresh.AddArtifact(ArtifactRef{
		Kind: ArtifactKindSource, ID: "x", Stage: StageFetch, ProducedAt: ts(3),
	}), "produced_at regresses")
	assert.Error(t, fresh.AddArtifact(ArtifactRef{
		Kind: ArtifactKindSource, ID: "bad id!", Stage: StageFetch, ProducedAt: ts(12),
	}), "invalid artifact id")

	// Fail on a terminal run is rejected even with a valid message.
	assert.Error(t, m.Fail(at(7), "code", "msg"))
}

func TestStageAttemptValidateEdgeCases(t *testing.T) {
	// A running attempt may not carry a finished_at.
	a := StageAttempt{
		Number: 1, Status: StageStatusRunning,
		StartedAt: ts(1), FinishedAt: ptr(ts(2)),
	}
	assert.Error(t, a.Validate())

	// A cancelled attempt needs no error summary.
	a = StageAttempt{Number: 1, Status: StageStatusCancelled, StartedAt: ts(1), FinishedAt: ptr(ts(2))}
	assert.NoError(t, a.Validate())
}

func TestStageRecordValidateEdgeCases(t *testing.T) {
	// Terminal status with no attempts.
	s := StageRecord{Name: StageFetch, Status: StageStatusSucceeded}
	assert.Error(t, s.Validate())

	// Status disagrees with the last attempt.
	s = StageRecord{
		Name:   StageFetch,
		Status: StageStatusSucceeded,
		Attempts: []StageAttempt{{
			Number: 1, Status: StageStatusFailed, StartedAt: ts(1), FinishedAt: ptr(ts(2)),
			Error: &ErrorSummary{Code: "x", Message: "y", OccurredAt: ts(2)},
		}},
	}
	assert.Error(t, s.Validate())

	// A pending stage with attempts is invalid.
	s = StageRecord{
		Name:     StageFetch,
		Status:   StageStatusPending,
		Attempts: []StageAttempt{{Number: 1, Status: StageStatusRunning, StartedAt: ts(1)}},
	}
	assert.Error(t, s.Validate())
}

func TestMarshalManifestNil(t *testing.T) {
	_, err := MarshalManifest(nil)
	assert.Error(t, err)
}

func TestRemainingBranches(t *testing.T) {
	// ModelMetadata: endpoint with a secret in the query string (raw struct,
	// bypassing the redacting constructor).
	bad := ModelMetadata{Provider: "p", Model: "m", Endpoint: "http://localhost:8000/v1?api_key=abc123def456"}
	assert.Error(t, bad.Validate(), "secret-looking endpoint must be rejected")

	// ArtifactKind.String and ArtifactRef kind/produced_at branches.
	assert.Equal(t, "source", ArtifactKindSource.String())
	assert.Error(t, (ArtifactRef{Kind: "scraped", ID: "a1"}).Validate())
	assert.Error(t, (ArtifactRef{Kind: ArtifactKindSource, ID: "a1"}).Validate())

	// NewManifest: topic too long.
	_, err := NewManifest(NewManifestParams{Workspace: Workspace{Name: "local"}, Topic: strings.Repeat("t", maxTopicLen+1)})
	assert.ErrorContains(t, err, "topic")

	// touch: zero time is rejected.
	m := newTestManifest(t)
	assert.Error(t, m.Start(time.Time{}))

	// StartStage: restarting a running stage; timestamp regression.
	m = newTestManifest(t)
	require.NoError(t, m.Start(at(1)))
	require.NoError(t, m.StartStage(StageFetch, at(2)))
	assert.Error(t, m.StartStage(StageFetch, at(3)), "restart of a running stage")
	assert.Error(t, m.StartStage(StageExtract, at(1)), "timestamp regression")

	// Succeed/Cancel on a terminal run.
	m = newTestManifest(t)
	require.NoError(t, m.Start(at(1)))
	require.NoError(t, m.Fail(at(2), "boom", "x"))
	assert.Error(t, m.Succeed(at(3)))
	assert.Error(t, m.Cancel(at(3), "x"))

	// Fail: invalid summary code; running stage with no attempts.
	m = newTestManifest(t)
	require.NoError(t, m.Start(at(1)))
	require.NoError(t, m.StartStage(StageFetch, at(2)))
	assert.Error(t, m.Fail(at(3), "Bad Code", "message"), "invalid failure summary")

	m = newTestManifest(t)
	require.NoError(t, m.Start(at(1)))
	m.Stages[stageIndex(m, StageFetch)] = StageRecord{Name: StageFetch, Status: StageStatusRunning}
	assert.NoError(t, m.Fail(at(2), "boom", "x"), "running stage without attempts is skipped")

	// Cancel: running stage with no attempts.
	m = newTestManifest(t)
	require.NoError(t, m.Start(at(1)))
	m.Stages[stageIndex(m, StageFetch)] = StageRecord{Name: StageFetch, Status: StageStatusRunning}
	require.NoError(t, m.Cancel(at(2), "stop"))

	// finishStageAttempt: terminal run, unknown stage, timestamp regression,
	// and a running record with no attempts.
	m = newTestManifest(t)
	require.NoError(t, m.Start(at(1)))
	require.NoError(t, m.Fail(at(2), "boom", "x"))
	assert.Error(t, m.SucceedStage(StageFetch, at(3)), "terminal run is immutable")

	m = newTestManifest(t)
	require.NoError(t, m.Start(at(1)))
	assert.Error(t, m.SucceedStage(StageName("scrape"), at(2)), "unknown stage")
	assert.Error(t, m.CancelStage(StageFetch, at(0)), "timestamp regression")
	m.Stages[stageIndex(m, StageFetch)] = StageRecord{Name: StageFetch, Status: StageStatusRunning}
	assert.Error(t, m.SucceedStage(StageFetch, at(2)), "running record has no attempts")

	// FailStage on a skipped stage (no running attempt).
	m = newTestManifest(t)
	require.NoError(t, m.Start(at(1)))
	require.NoError(t, m.SkipStage(StageFetch, at(2), "no url"))
	assert.Error(t, m.FailStage(StageFetch, at(3), "code", "message"))

	// AddArtifact: terminal run; valid stage that is not part of the plan.
	m = newTestManifest(t)
	require.NoError(t, m.Start(at(1)))
	require.NoError(t, m.Fail(at(2), "boom", "x"))
	assert.Error(t, m.AddArtifact(ArtifactRef{Kind: ArtifactKindDocument, ID: "d1", Stage: StageFetch, Path: "out/d.md", ProducedAt: ts(3)}))

	sub := cloneManifest(newTestManifest(t))
	sub.Stages = []StageRecord{{Name: StageFetch, Status: StageStatusPending}}
	require.NoError(t, sub.Start(at(1)))
	err = sub.AddArtifact(ArtifactRef{Kind: ArtifactKindDocument, ID: "d1", Stage: StageExtract, Path: "out/x.md", ProducedAt: ts(2)})
	assert.ErrorContains(t, err, "not part of this run")

	// AddWarning: invalid code, timestamp regression, terminal run.
	m = newTestManifest(t)
	require.NoError(t, m.Start(at(1)))
	assert.Error(t, m.AddWarning("Not A Code", "x", at(2)))
	m = newTestManifest(t)
	require.NoError(t, m.Start(at(5)))
	assert.Error(t, m.AddWarning("w", "x", at(2)), "timestamp regression")
	m = newTestManifest(t)
	require.NoError(t, m.Start(at(1)))
	require.NoError(t, m.Cancel(at(2), "stop"))
	assert.Error(t, m.AddWarning("w", "x", at(3)), "terminal run is immutable")

	// AddError: terminal run, timestamp regression.
	m = newTestManifest(t)
	require.NoError(t, m.Start(at(1)))
	require.NoError(t, m.Cancel(at(2), "stop"))
	assert.Error(t, m.AddError("code", "x", at(3)), "terminal run is immutable")
	m = newTestManifest(t)
	require.NoError(t, m.Start(at(5)))
	assert.Error(t, m.AddError("code", "x", at(2)), "timestamp regression")

	// UnmarshalManifest: non-semantic schema version.
	_, err = UnmarshalManifest([]byte(`{"schema_version":"bogus"}`))
	assert.ErrorIs(t, err, ErrUnsupportedSchemaVersion)

	// StageAttempt: failed attempt whose error summary is invalid.
	a := StageAttempt{
		Number: 1, Status: StageStatusFailed, StartedAt: ts(1), FinishedAt: ptr(ts(2)),
		Error: &ErrorSummary{Code: "Bad Code", Message: "x", OccurredAt: ts(2)},
	}
	assert.Error(t, a.Validate())

	// StageRecord: history contains an invalid attempt.
	rec := StageRecord{
		Name: StageFetch, Status: StageStatusRunning,
		Attempts: []StageAttempt{{Number: 1, Status: StageStatusPending, StartedAt: ts(1)}},
	}
	assert.Error(t, rec.Validate())

	// Timestamp: invalid JSON.
	var got Timestamp
	assert.Error(t, got.UnmarshalJSON([]byte("not-json")))

	// Note validation: missing/oversized message, zero timestamp.
	assert.Error(t, NewWarning("w", "", at(1)).Validate())
	assert.Error(t, NewWarning("w", strings.Repeat("x", maxNoteLen+1), at(1)).Validate())
	assert.Error(t, NewWarning("w", "x", time.Time{}).Validate())

	// ConfigSnapshot: oversized value.
	snap := &ConfigSnapshot{}
	require.NoError(t, snap.Set("llm_base_url", strings.Repeat("v", maxConfigValueLen+1)))
	assert.Error(t, snap.Validate())

	// shannonEntropy: empty input.
	assert.Equal(t, 0.0, shannonEntropy(""))

	// RedactSecrets: a short secret query value that the assignment pass
	// leaves alone (below its minimum length) is still caught by the URL
	// query pass.
	assert.Equal(t,
		"http://api.example.com/v1?api_key="+RedactedPlaceholder,
		RedactSecrets("http://api.example.com/v1?api_key=abc"))
}

func TestManifestValidateTampering(t *testing.T) {
	// A fully successful run, used as the base for single-field tampering.
	m := newTestManifest(t)
	require.NoError(t, m.Start(at(1)))
	for i, st := range m.Stages {
		require.NoError(t, m.StartStage(st.Name, at(2+i)))
		require.NoError(t, m.SucceedStage(st.Name, at(3+i)))
	}
	require.NoError(t, m.AddArtifact(ArtifactRef{Kind: ArtifactKindDocument, ID: "d1", Stage: StageWrite, Path: "out/d.md", ProducedAt: ts(3 + len(m.Stages))}))
	require.NoError(t, m.Succeed(at(4+len(m.Stages))))
	require.NoError(t, m.Validate())

	clone := func() *Manifest { return cloneManifest(m) }

	bad := clone()
	bad.SchemaVersion = ""
	assert.ErrorContains(t, bad.Validate(), "schema_version")

	bad = clone()
	bad.ID = RunID("not a valid id")
	assert.Error(t, bad.Validate())

	bad = clone()
	bad.Profile = &Profile{ID: "Bad ID!"}
	assert.Error(t, bad.Validate())

	bad = clone()
	bad.Model = ModelMetadata{Provider: strings.Repeat("p", maxModelFieldLen+1)}
	assert.Error(t, bad.Validate())

	bad = clone()
	bad.Config = ConfigSnapshot{Values: map[string]string{"bad key!": "x"}}
	assert.Error(t, bad.Validate())

	bad = clone()
	bad.Stages[0].Name = StageName("scrape")
	assert.ErrorContains(t, bad.Validate(), "unknown stage name")

	bad = clone()
	bad.Artifacts = append(bad.Artifacts, bad.Artifacts[0])
	assert.ErrorContains(t, bad.Validate(), "duplicate artifact")

	bad = clone()
	bad.Errors = append(bad.Errors, ErrorSummary{Code: "Bad Code", Message: "x", OccurredAt: ts(4)})
	assert.ErrorContains(t, bad.Validate(), "errors[0]")
}

func TestTerminalAndRegressionBranches(t *testing.T) {
	// Succeed on an already succeeded run reaches the run transition check.
	m := newTestManifest(t)
	require.NoError(t, m.Start(at(1)))
	for i, st := range m.Stages {
		require.NoError(t, m.StartStage(st.Name, at(2+i)))
		require.NoError(t, m.SucceedStage(st.Name, at(3+i)))
	}
	require.NoError(t, m.Succeed(at(4+len(m.Stages))))
	assert.Error(t, m.Succeed(at(5+len(m.Stages))), "cannot succeed an already succeeded run")

	// finishStageAttempt timestamp regression (stage running, clock goes back).
	m = newTestManifest(t)
	require.NoError(t, m.Start(at(1)))
	require.NoError(t, m.StartStage(StageFetch, at(5)))
	assert.Error(t, m.SucceedStage(StageFetch, at(4)), "timestamp regression")
}

func TestRedactQueryPairs(t *testing.T) {
	// No matching keys: pairs preserved byte-for-byte, raw returned unchanged.
	in := "a=1&b=2"
	assert.Equal(t, in, redactQueryPairs(in, map[string]bool{"api_key": true}))

	// Already-redacted value: left untouched (idempotence).
	already := "api_key=" + RedactedPlaceholder + "&x=1"
	assert.Equal(t, already, redactQueryPairs(already, map[string]bool{"api_key": true}))

	// Unescapable key name is skipped; the real match is still redacted.
	got := redactQueryPairs("api_%zz=1&api_key=abc", map[string]bool{"api_key": true})
	assert.Equal(t, "api_%zz=1&api_key="+RedactedPlaceholder, got)

	// Flag-style pair without a value.
	assert.Equal(t, "api_key="+RedactedPlaceholder, redactQueryPairs("api_key", map[string]bool{"api_key": true}))
}
