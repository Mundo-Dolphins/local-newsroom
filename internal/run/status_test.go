package run

import (
	"testing"
)

func TestRunStatusValid(t *testing.T) {
	valid := map[RunStatus]bool{
		RunStatusPending:   true,
		RunStatusRunning:   true,
		RunStatusSucceeded: true,
		RunStatusFailed:    true,
		RunStatusCancelled: true,
	}
	for s, want := range valid {
		if got := s.Valid(); got != want {
			t.Errorf("RunStatus(%q).Valid() = %v, want %v", s, got, want)
		}
	}
	invalid := []RunStatus{"", "PENDING", "done", "succeed", "running2", "in-progress"}
	for _, s := range invalid {
		if s.Valid() {
			t.Errorf("RunStatus(%q).Valid() = true, want false", s)
		}
	}
}

func TestRunStatusTerminal(t *testing.T) {
	cases := map[RunStatus]bool{
		RunStatusPending:   false,
		RunStatusRunning:   false,
		RunStatusSucceeded: true,
		RunStatusFailed:    true,
		RunStatusCancelled: true,
	}
	for s, want := range cases {
		if got := s.Terminal(); got != want {
			t.Errorf("RunStatus(%q).Terminal() = %v, want %v", s, got, want)
		}
	}
}

func TestStageStatusValid(t *testing.T) {
	valid := map[StageStatus]bool{
		StageStatusPending:   true,
		StageStatusSkipped:   true,
		StageStatusRunning:   true,
		StageStatusSucceeded: true,
		StageStatusFailed:    true,
		StageStatusCancelled: true,
	}
	for s, want := range valid {
		if got := s.Valid(); got != want {
			t.Errorf("StageStatus(%q).Valid() = %v, want %v", s, got, want)
		}
	}
	invalid := []StageStatus{"", "PENDING", "skipped!", "done", "success"}
	for _, s := range invalid {
		if s.Valid() {
			t.Errorf("StageStatus(%q).Valid() = true, want false", s)
		}
	}
}

func TestStageStatusTerminal(t *testing.T) {
	cases := map[StageStatus]bool{
		StageStatusPending:   false,
		StageStatusSkipped:   true, // deliberately skipped: never re-run within this run
		StageStatusRunning:   false,
		StageStatusSucceeded: true,
		StageStatusFailed:    true, // terminal *state*, but retry is explicitly allowed
		StageStatusCancelled: true,
	}
	for s, want := range cases {
		if got := s.Terminal(); got != want {
			t.Errorf("StageStatus(%q).Terminal() = %v, want %v", s, got, want)
		}
	}
}

// TestValidRunTransition exercises the complete 5x5 run transition table.
func TestValidRunTransition(t *testing.T) {
	statuses := AllRunStatuses
	for _, from := range statuses {
		for _, to := range statuses {
			var want bool
			switch from {
			case RunStatusPending:
				want = to == RunStatusRunning || to == RunStatusCancelled
			case RunStatusRunning:
				want = to == RunStatusSucceeded || to == RunStatusFailed || to == RunStatusCancelled
			default:
				want = false
			}
			if got := ValidRunTransition(from, to); got != want {
				t.Errorf("ValidRunTransition(%q, %q) = %v, want %v", from, to, got, want)
			}
		}
	}
	if ValidRunTransition(RunStatusPending, RunStatusSucceeded) {
		t.Error("a pending run must not jump straight to succeeded")
	}
	if ValidRunTransition(RunStatusFailed, RunStatusRunning) {
		t.Error("a failed run must not restart (resume is a new run)")
	}
}

// TestValidStageTransition exercises the complete 6x6 stage transition table.
func TestValidStageTransition(t *testing.T) {
	statuses := AllStageStatuses
	for _, from := range statuses {
		for _, to := range statuses {
			var want bool
			switch from {
			case StageStatusPending:
				want = to == StageStatusRunning || to == StageStatusSkipped || to == StageStatusCancelled
			case StageStatusRunning:
				want = to == StageStatusSucceeded || to == StageStatusFailed || to == StageStatusCancelled
			case StageStatusFailed:
				want = to == StageStatusRunning
			default:
				want = false
			}
			if got := ValidStageTransition(from, to); got != want {
				t.Errorf("ValidStageTransition(%q, %q) = %v, want %v", from, to, got, want)
			}
		}
	}

	// Key invariants called out by the contract.
	if !ValidStageTransition(StageStatusPending, StageStatusSkipped) {
		t.Error("pending -> skipped must be valid")
	}
	if !ValidStageTransition(StageStatusFailed, StageStatusRunning) {
		t.Error("failed -> running (retry) must be valid")
	}
	if ValidStageTransition(StageStatusSucceeded, StageStatusRunning) {
		t.Error("succeeded stages are terminal: re-run must be rejected")
	}
	if ValidStageTransition(StageStatusSkipped, StageStatusRunning) {
		t.Error("skipped stages stay skipped within a run")
	}
	if ValidStageTransition(StageStatusRunning, StageStatusSkipped) {
		t.Error("a running stage cannot be skipped mid-flight")
	}
	if ValidStageTransition(StageStatusPending, StageStatusSucceeded) {
		t.Error("a stage cannot succeed without running")
	}
}

func TestValidAttemptStatus(t *testing.T) {
	cases := map[StageStatus]bool{
		StageStatusPending:   false,
		StageStatusSkipped:   false,
		StageStatusRunning:   true,
		StageStatusSucceeded: true,
		StageStatusFailed:    true,
		StageStatusCancelled: true,
	}
	for s, want := range cases {
		if got := ValidAttemptStatus(s); got != want {
			t.Errorf("ValidAttemptStatus(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestValidAttemptTransition(t *testing.T) {
	statuses := AllStageStatuses
	for _, from := range statuses {
		for _, to := range statuses {
			want := from == StageStatusRunning && (to == StageStatusSucceeded || to == StageStatusFailed || to == StageStatusCancelled)
			if got := ValidAttemptTransition(from, to); got != want {
				t.Errorf("ValidAttemptTransition(%q, %q) = %v, want %v", from, to, got, want)
			}
		}
	}
	if ValidAttemptTransition(StageStatusFailed, StageStatusRunning) {
		t.Error("an individual attempt never retries; retry is a new attempt")
	}
}

func TestSourceModeValid(t *testing.T) {
	valid := map[SourceMode]bool{
		SourceModeExplicit:   true,
		SourceModeDiscovered: true,
		SourceModeSupplement: true,
		SourceModeUnknown:    true,
	}
	for m, want := range valid {
		if got := m.Valid(); got != want {
			t.Errorf("SourceMode(%q).Valid() = %v, want %v", m, got, want)
		}
	}
	invalid := []SourceMode{"", "explicit!", "auto", "web"}
	for _, m := range invalid {
		if m.Valid() {
			t.Errorf("SourceMode(%q).Valid() = true, want false", m)
		}
	}
}

func TestStageNameValid(t *testing.T) {
	for _, name := range AllStageNames {
		if !name.Valid() {
			t.Errorf("StageName(%q).Valid() = false, want true", name)
		}
	}
	invalid := []StageName{"", "validate", "scrape", "DISCOVERY", "fetch-extra", "final-check"}
	for _, name := range invalid {
		if name.Valid() {
			t.Errorf("StageName(%q).Valid() = true, want false", name)
		}
	}
}

func TestCanonicalStages(t *testing.T) {
	got := CanonicalStages()
	want := []StageName{
		StageDiscovery, StageFetch, StageExtract, StageResearch,
		StageVerify, StageFollowUp, StageWrite, StageFinalCheck, StageRender,
	}
	if len(got) != len(want) {
		t.Fatalf("CanonicalStages() length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("CanonicalStages()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// The returned slice must be a copy: mutating it must not affect the package.
	got[0] = "tampered"
	if CanonicalStages()[0] != StageDiscovery {
		t.Error("CanonicalStages() must return a copy")
	}
}
