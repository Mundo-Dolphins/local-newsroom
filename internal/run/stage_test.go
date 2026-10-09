package run

import (
	"testing"
	"time"
)

var baseTime = time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)

func at(offset int) time.Time {
	return baseTime.Add(time.Duration(offset) * time.Second)
}

func ts(offset int) Timestamp {
	return NewTimestamp(at(offset))
}

func ptr[T any](v T) *T { return &v }

func TestStageAttemptValidate(t *testing.T) {
	valid := StageAttempt{
		Number:     1,
		Status:     StageStatusSucceeded,
		StartedAt:  ts(0),
		FinishedAt: ptr(ts(10)),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid attempt: %v", err)
	}

	running := StageAttempt{
		Number:    1,
		Status:    StageStatusRunning,
		StartedAt: ts(0),
	}
	if err := running.Validate(); err != nil {
		t.Fatalf("valid running attempt: %v", err)
	}

	failed := StageAttempt{
		Number:     1,
		Status:     StageStatusFailed,
		StartedAt:  ts(0),
		FinishedAt: ptr(ts(5)),
		Error:      &ErrorSummary{Code: "fetch_error", Message: "connection refused", OccurredAt: ts(5)},
	}
	if err := failed.Validate(); err != nil {
		t.Fatalf("valid failed attempt: %v", err)
	}

	cases := []struct {
		name    string
		attempt StageAttempt
	}{
		{"zero number", StageAttempt{Number: 0, Status: StageStatusRunning, StartedAt: ts(0)}},
		{"negative number", StageAttempt{Number: -1, Status: StageStatusRunning, StartedAt: ts(0)}},
		{"pending status", StageAttempt{Number: 1, Status: StageStatusPending, StartedAt: ts(0)}},
		{"skipped status", StageAttempt{Number: 1, Status: StageStatusSkipped, StartedAt: ts(0)}},
		{"unknown status", StageAttempt{Number: 1, Status: "done", StartedAt: ts(0)}},
		{"missing started", StageAttempt{Number: 1, Status: StageStatusRunning}},
		{"terminal without finished", StageAttempt{Number: 1, Status: StageStatusSucceeded, StartedAt: ts(0)}},
		{"finished before start", StageAttempt{Number: 1, Status: StageStatusSucceeded, StartedAt: ts(10), FinishedAt: ptr(ts(5))}},
		{"running with finished", StageAttempt{Number: 1, Status: StageStatusRunning, StartedAt: ts(0), FinishedAt: ptr(ts(5))}},
		{"failed without error", StageAttempt{Number: 1, Status: StageStatusFailed, StartedAt: ts(0), FinishedAt: ptr(ts(5))}},
		{"succeeded with error", StageAttempt{Number: 1, Status: StageStatusSucceeded, StartedAt: ts(0), FinishedAt: ptr(ts(5)), Error: &ErrorSummary{Message: "x", OccurredAt: ts(5)}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.attempt.Validate(); err == nil {
				t.Errorf("Validate() = nil, want error for %s", tc.name)
			}
		})
	}
}

func TestStageRecordValidate(t *testing.T) {
	validSkipped := StageRecord{Name: StageDiscovery, Status: StageStatusSkipped, SkipReason: "explicit URLs provided"}
	if err := validSkipped.Validate(); err != nil {
		t.Fatalf("valid skipped stage: %v", err)
	}

	validPending := StageRecord{Name: StageFetch, Status: StageStatusPending}
	if err := validPending.Validate(); err != nil {
		t.Fatalf("valid pending stage: %v", err)
	}

	validRetried := StageRecord{
		Name:   StageFetch,
		Status: StageStatusSucceeded,
		Attempts: []StageAttempt{
			{Number: 1, Status: StageStatusFailed, StartedAt: ts(0), FinishedAt: ptr(ts(5)), Error: &ErrorSummary{Message: "timeout", OccurredAt: ts(5)}},
			{Number: 2, Status: StageStatusSucceeded, StartedAt: ts(6), FinishedAt: ptr(ts(9))},
		},
	}
	if err := validRetried.Validate(); err != nil {
		t.Fatalf("valid retried stage: %v", err)
	}

	cases := []struct {
		name  string
		stage StageRecord
	}{
		{"unknown stage", StageRecord{Name: "scrape", Status: StageStatusPending}},
		{"invalid status", StageRecord{Name: StageFetch, Status: "bogus"}},
		{"running without attempts", StageRecord{Name: StageFetch, Status: StageStatusRunning}},
		{"succeeded without attempts", StageRecord{Name: StageFetch, Status: StageStatusSucceeded}},
		{"failed without attempts", StageRecord{Name: StageFetch, Status: StageStatusFailed}},
		{"pending with attempts", StageRecord{Name: StageFetch, Status: StageStatusPending, Attempts: []StageAttempt{{Number: 1, Status: StageStatusRunning, StartedAt: ts(0)}}}},
		{"skipped with attempts", StageRecord{Name: StageFetch, Status: StageStatusSkipped, SkipReason: "x", Attempts: []StageAttempt{{Number: 1, Status: StageStatusFailed, StartedAt: ts(0), FinishedAt: ptr(ts(1)), Error: &ErrorSummary{Message: "e", OccurredAt: ts(1)}}}}},
		{"status mismatch with last attempt", StageRecord{
			Name: StageFetch, Status: StageStatusSucceeded,
			Attempts: []StageAttempt{{Number: 1, Status: StageStatusFailed, StartedAt: ts(0), FinishedAt: ptr(ts(1)), Error: &ErrorSummary{Message: "e", OccurredAt: ts(1)}}},
		}},
		{"non-sequential attempts", StageRecord{
			Name:   StageFetch,
			Status: StageStatusSucceeded,
			Attempts: []StageAttempt{
				{Number: 1, Status: StageStatusFailed, StartedAt: ts(0), FinishedAt: ptr(ts(1)), Error: &ErrorSummary{Message: "e", OccurredAt: ts(1)}},
				{Number: 5, Status: StageStatusSucceeded, StartedAt: ts(2), FinishedAt: ptr(ts(3))},
			},
		}},
		{"skipped without reason", StageRecord{Name: StageDiscovery, Status: StageStatusSkipped}},
		{"skip reason on non-skipped", StageRecord{Name: StageFetch, Status: StageStatusPending, SkipReason: "why"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.stage.Validate(); err == nil {
				t.Errorf("Validate() = nil, want error for %s", tc.name)
			}
		})
	}
}
