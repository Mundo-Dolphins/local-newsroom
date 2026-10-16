package publication

import "testing"

func TestStatusValues(t *testing.T) {
	// Issue #114 requires at least these eight states.
	required := []string{
		"planned", "approved", "publishing", "succeeded",
		"failed", "partially_succeeded", "cancelled", "skipped",
	}
	got := map[PublicationStatus]bool{}
	for _, s := range AllStatuses {
		if !s.Valid() {
			t.Errorf("status %q is in AllStatuses but does not validate", s)
		}
		got[s] = true
	}
	for _, name := range required {
		if !got[PublicationStatus(name)] {
			t.Errorf("required status %q is missing from AllStatuses", name)
		}
	}
	if len(AllStatuses) != len(got) {
		t.Errorf("AllStatuses has duplicates: %v", AllStatuses)
	}
}

func TestStatusValid(t *testing.T) {
	for _, s := range AllStatuses {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []PublicationStatus{"", "planning", "PUBLISHED", "done", "ok", "succeed"} {
		if s.Valid() {
			t.Errorf("%q should not be valid", s)
		}
	}
}

func TestStatusTerminal(t *testing.T) {
	terminal := map[PublicationStatus]bool{
		StatusSucceeded:          true,
		StatusFailed:             true,
		StatusPartiallySucceeded: true,
		StatusCancelled:          true,
		StatusSkipped:            true,
		StatusPlanned:            false,
		StatusApproved:           false,
		StatusPublishing:         false,
	}
	for s, want := range terminal {
		if got := s.Terminal(); got != want {
			t.Errorf("%q.Terminal() = %t, want %t", s, got, want)
		}
	}
	if PublicationStatus("nope").Terminal() {
		t.Error("an unknown status must not be terminal")
	}
}

func TestValidPlanTransition(t *testing.T) {
	cases := []struct {
		from, to PublicationStatus
		want     bool
	}{
		// planned
		{StatusPlanned, StatusApproved, true},
		{StatusPlanned, StatusCancelled, true},
		{StatusPlanned, StatusSkipped, true},
		{StatusPlanned, StatusPublishing, false},
		{StatusPlanned, StatusSucceeded, false},
		{StatusPlanned, StatusPlanned, false},
		// approved
		{StatusApproved, StatusPublishing, true},
		{StatusApproved, StatusPlanned, true}, // approval revoked or stale
		{StatusApproved, StatusCancelled, true},
		{StatusApproved, StatusSkipped, true},
		{StatusApproved, StatusSucceeded, false},
		// publishing
		{StatusPublishing, StatusSucceeded, true},
		{StatusPublishing, StatusFailed, true},
		{StatusPublishing, StatusPartiallySucceeded, true},
		{StatusPublishing, StatusCancelled, true},
		{StatusPublishing, StatusSkipped, true},
		{StatusPublishing, StatusPlanned, false},
		{StatusPublishing, StatusApproved, false},
		{StatusPublishing, StatusPublishing, false},
		// retry paths
		{StatusFailed, StatusPublishing, true},
		{StatusPartiallySucceeded, StatusPublishing, true},
		// terminal states never transition
		{StatusSucceeded, StatusPublishing, false},
		{StatusSucceeded, StatusPlanned, false},
		{StatusFailed, StatusSucceeded, false},
		{StatusCancelled, StatusPublishing, false},
		{StatusSkipped, StatusPlanned, false},
		// unknown statuses
		{PublicationStatus("???"), StatusApproved, false},
		{StatusPlanned, PublicationStatus("???"), false},
	}
	for _, c := range cases {
		if got := ValidPlanTransition(c.from, c.to); got != c.want {
			t.Errorf("ValidPlanTransition(%q, %q) = %t, want %t", c.from, c.to, got, c.want)
		}
	}
}

func TestValidTargetTransition(t *testing.T) {
	cases := []struct {
		from, to PublicationStatus
		want     bool
	}{
		{StatusPlanned, StatusPublishing, true},
		{StatusPlanned, StatusSkipped, true},
		{StatusPlanned, StatusCancelled, true},
		{StatusPlanned, StatusApproved, false},  // targets are never approved
		{StatusPlanned, StatusSucceeded, false}, // must go through publishing
		{StatusPublishing, StatusSucceeded, true},
		{StatusPublishing, StatusFailed, true},
		{StatusPublishing, StatusPartiallySucceeded, true},
		{StatusPublishing, StatusCancelled, true},
		{StatusPublishing, StatusSkipped, false}, // an in-flight target is aborted, not skipped
		{StatusFailed, StatusPublishing, true},
		{StatusPartiallySucceeded, StatusPublishing, true},
		{StatusSucceeded, StatusPublishing, false}, // finished targets are never re-executed
		{StatusCancelled, StatusPublishing, false},
		{StatusSkipped, StatusPublishing, false},
		{PublicationStatus("???"), StatusPublishing, false},
	}
	for _, c := range cases {
		if got := ValidTargetTransition(c.from, c.to); got != c.want {
			t.Errorf("ValidTargetTransition(%q, %q) = %t, want %t", c.from, c.to, got, c.want)
		}
	}
}
