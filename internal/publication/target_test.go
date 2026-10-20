package publication

import (
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

func TestTargetIDValidate(t *testing.T) {
	valid := []TargetID{"bluesky-thread", "hugo_article", "site.pages-1", "A", "a29.-_"}
	for _, id := range valid {
		if err := id.Validate(); err != nil {
			t.Errorf("%q should validate: %v", id, err)
		}
	}
	invalid := []TargetID{
		"",
		"-leading-dash",
		".leading-dot",
		"_leading-underscore",
		"has space",
		"has/slash",
		"café",
		TargetID(strings.Repeat("x", 200)),
	}
	for _, id := range invalid {
		if err := id.Validate(); err == nil {
			t.Errorf("%q should not validate", id)
		}
	}
}

func TestTargetTypeAndProvider(t *testing.T) {
	for _, typ := range AllTargetTypes {
		if !typ.Valid() {
			t.Errorf("%q should be a valid target type", typ)
		}
	}
	if TargetType("blog").Valid() {
		t.Error("unknown target types must not be valid")
	}

	// Provider rules are enforced through NewTarget + Validate.
	good := NewTarget("t", "", TargetTypePost, "bluesky", "at://com.example/post", nil)
	if err := good.Validate(); err != nil {
		t.Errorf("a well-formed target should validate: %v", err)
	}
	bad := NewTarget("t", "", TargetTypePost, "Bluesky!", "dest", nil)
	if err := bad.Validate(); err == nil {
		t.Error("an uppercase provider with punctuation should not validate")
	}
	empty := NewTarget("t", "", TargetTypePost, "", "dest", nil)
	if err := empty.Validate(); err == nil {
		t.Error("an empty provider should not validate")
	}
}

func TestDryRunOutcomes(t *testing.T) {
	for _, o := range AllDryRunOutcomes {
		if !o.Valid() {
			t.Errorf("%q should be a valid outcome", o)
		}
	}
	if DryRunOutcome("maybe").Valid() {
		t.Error("unknown outcomes must not be valid")
	}
}

func TestNewTarget(t *testing.T) {
	deps := []TargetID{"x"}
	tgt := NewTarget("t1", "name "+testToken, TargetTypePost, "bluesky", "dest "+testToken, deps)
	if tgt.Status != StatusPlanned {
		t.Errorf("new targets must be planned, got %q", tgt.Status)
	}
	if !strings.Contains(tgt.Name, run.RedactedPlaceholder) {
		t.Errorf("secret in the name must be redacted: %q", tgt.Name)
	}
	if !strings.Contains(tgt.Destination, run.RedactedPlaceholder) {
		t.Errorf("secret in the destination must be redacted: %q", tgt.Destination)
	}
	// The dependency slice is copied, not aliased to the caller's.
	deps[0] = TargetID("mutated")
	if got := tgt.Dependencies(); len(got) != 1 || got[0] != TargetID("x") {
		t.Errorf("modifying the caller's slice must not affect the target, got %v", got)
	}
}

func TestDryRunIssue(t *testing.T) {
	i := NewDryRunIssue("over_char_limit", "limit "+testToken)
	if !strings.Contains(i.Message, run.RedactedPlaceholder) {
		t.Errorf("secret in the issue message must be redacted: %q", i.Message)
	}
	if err := i.Validate(); err != nil {
		t.Errorf("a redacted issue must validate: %v", err)
	}
}

func TestDryRunResultValidate(t *testing.T) {
	ok := NewDryRunResult(tAt(5), DryRunOutcomeOK, digestOf(briefA), "ok", nil)
	if err := ok.Validate(); err != nil {
		t.Fatalf("an ok dry run must validate: %v", err)
	}
	failed := NewDryRunResult(tAt(5), DryRunOutcomeFailed, digestOf(briefA), "blocked",
		[]DryRunIssue{NewDryRunIssue("unreachable", "provider unreachable")})
	if err := failed.Validate(); err != nil {
		t.Fatalf("a failed dry run with issues must validate: %v", err)
	}

	cases := map[string]struct {
		mutate func(r *DryRunResult)
		substr string
	}{
		"nil":                   {},
		"zero time":             {func(r *DryRunResult) { r.PerformedAt = run.Timestamp{} }, "performed_at"},
		"bad outcome":           {func(r *DryRunResult) { r.Outcome = DryRunOutcome("maybe") }, "unknown dry run outcome"},
		"bad digest":            {func(r *DryRunResult) { r.ContentDigest = Digest("sha256:zz") }, "digest"},
		"ok with issues":        {func(r *DryRunResult) { r.Outcome = DryRunOutcomeOK; r.Issues = []DryRunIssue{NewDryRunIssue("x", "y")} }, "outcome is ok"},
		"failed without issues": {func(r *DryRunResult) { r.Outcome = DryRunOutcomeFailed; r.Issues = nil }, "at least one issue"},
		"secret details":        {func(r *DryRunResult) { r.Details = testToken }, "secret-looking"},
		"long details":          {func(r *DryRunResult) { r.Details = strings.Repeat("d", maxDryRunDetails+1) }, "exceed"},
		"too many issues": {func(r *DryRunResult) {
			r.Outcome = DryRunOutcomeFailed
			r.Issues = make([]DryRunIssue, MaxIssuesPerDryRun+1)
			for i := range r.Issues {
				r.Issues[i] = NewDryRunIssue("x", "issue")
			}
		}, "exceeding the bound"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if c.mutate == nil {
				if err := (*DryRunResult)(nil).Validate(); err == nil {
					t.Error("a nil dry run must not validate")
				}
				return
			}
			r := *ok
			c.mutate(&r)
			err := r.Validate()
			if err == nil {
				t.Fatalf("%q: want a validation error, got none", name)
			}
			if !strings.Contains(err.Error(), c.substr) {
				t.Errorf("%q: error %q does not mention %q", name, err, c.substr)
			}
		})
	}
}

// baseTarget returns a valid, planned target.
func baseTarget() PublicationTarget {
	return NewTarget("bluesky-thread", "Dolphins thread", TargetTypePost, "bluesky",
		"at://com.example/app/bsky.feed.post/dolphins-1", nil)
}

func TestTargetValidate(t *testing.T) {
	valid := baseTarget()
	if err := valid.Validate(); err != nil {
		t.Fatalf("base target must validate: %v", err)
	}

	cases := map[string]struct {
		mutate func(t *PublicationTarget)
		substr string
	}{
		"nil":                    {nil, ""},
		"missing id":             {func(t *PublicationTarget) { t.ID = "" }, "required"},
		"bad id":                 {func(t *PublicationTarget) { t.ID = TargetID("-x") }, "invalid"},
		"long name":              {func(t *PublicationTarget) { t.Name = strings.Repeat("n", maxTargetNameLen+1) }, "exceeds"},
		"secret name":            {func(t *PublicationTarget) { t.Name = testToken }, "secret-looking"},
		"bad type":               {func(t *PublicationTarget) { t.Type = TargetType("blog") }, "unknown type"},
		"bad provider":           {func(t *PublicationTarget) { t.Provider = "Bad!" }, "provider"},
		"empty destination":      {func(t *PublicationTarget) { t.Destination = "" }, "destination"},
		"credential destination": {func(t *PublicationTarget) { t.Destination = "https://user:pass@example.com/" }, "credentials"},
		"redacted destination is accepted": {func(t *PublicationTarget) {
			t.Destination = "https://user:" + run.RedactedPlaceholder + "@example.com/"
		}, ""},
		"bad status":                 {func(t *PublicationTarget) { t.Status = PublicationStatus("maybe") }, "unknown status"},
		"skipped without reason":     {func(t *PublicationTarget) { t.Status = StatusSkipped }, "no skip reason"},
		"skipped with secret reason": {func(t *PublicationTarget) { t.Status = StatusSkipped; t.SkipReason = testToken }, "secret-looking"},
		"failed without error":       {func(t *PublicationTarget) { t.Status = StatusFailed }, "no error summary"},
		"succeeded without receipt":  {func(t *PublicationTarget) { t.Status = StatusSucceeded }, "no receipt"},
		"planned with a receipt":     {func(t *PublicationTarget) { t.Receipt = baseReceipt() }, "external proof"},
		"failed keeps its resume receipt": {func(t *PublicationTarget) {
			ts := run.NewTimestamp(tAt(1))
			e := runError()
			a := PublicationAttempt{Number: 1, Status: AttemptStatusFailed, StartedAt: ts, FinishedAt: &ts, Error: e, Receipt: baseReceipt()}
			t.Status = StatusFailed
			t.Error = e
			t.Receipt = baseReceipt()
			t.Attempts = []PublicationAttempt{a}
		}, ""},
		"failed with a malformed carried receipt": {func(t *PublicationTarget) {
			t.Status = StatusFailed
			t.Error = runError()
			r := baseReceipt()
			r.ContentDigest = Digest("garbage")
			t.Receipt = r
		}, "invalid receipt"},
		"planned with attempts": {func(t *PublicationTarget) { t.Attempts = []PublicationAttempt{NewAttempt(1, tAt(1))} }, "carries"},
		"long skip reason":      {func(t *PublicationTarget) { t.Status = StatusSkipped; t.SkipReason = strings.Repeat("s", maxNoteLen+1) }, "exceeds"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if c.mutate == nil {
				if err := (*PublicationTarget)(nil).Validate(); err == nil {
					t.Error("a nil target must not validate")
				}
				return
			}
			tgt := valid
			c.mutate(&tgt)
			err := tgt.Validate()
			if c.substr == "" {
				if err != nil {
					t.Errorf("%q: want a valid target, got %v", name, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.substr) {
				t.Errorf("%q: error %q does not mention %q", name, err, c.substr)
			}
		})
	}
}

func runError() *run.ErrorSummary {
	e := run.NewErrorSummary("net", "provider unreachable", tAt(1))
	return &e
}

func TestValidateTargetGraph(t *testing.T) {
	ok := []PublicationTarget{
		NewTarget("a", "", TargetTypePost, "bluesky", "d", nil),
		NewTarget("b", "", TargetTypePost, "bluesky", "d", []TargetID{"a"}),
		NewTarget("c", "", TargetTypePage, "hugo", "d", []TargetID{"a", "b"}),
	}
	if err := validateTargetGraph(ok); err != nil {
		t.Errorf("a diamond graph must be acyclic: %v", err)
	}

	if err := validateTargetGraph([]PublicationTarget{
		NewTarget("a", "", TargetTypePost, "bluesky", "d", []TargetID{"a"}),
	}); err == nil {
		t.Error("a self-dependency must be rejected")
	}
	if err := validateTargetGraph([]PublicationTarget{
		NewTarget("a", "", TargetTypePost, "bluesky", "d", nil),
		NewTarget("a", "", TargetTypePost, "bluesky", "d", nil),
	}); err == nil {
		t.Error("duplicate target IDs must be rejected")
	}
	if err := validateTargetGraph([]PublicationTarget{
		NewTarget("a", "", TargetTypePost, "bluesky", "d", []TargetID{"missing"}),
	}); err == nil {
		t.Error("a dependency on an unknown target must be rejected")
	}
	if err := validateTargetGraph([]PublicationTarget{
		NewTarget("a", "", TargetTypePost, "bluesky", "d", []TargetID{"b", "b"}),
		NewTarget("b", "", TargetTypePost, "bluesky", "d", nil),
	}); err == nil {
		t.Error("a duplicate dependency must be rejected")
	}
	if err := validateTargetGraph([]PublicationTarget{
		NewTarget("a", "", TargetTypePost, "bluesky", "d", []TargetID{"b"}),
		NewTarget("b", "", TargetTypePost, "bluesky", "d", []TargetID{"a"}),
	}); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("a two-node cycle must be rejected: %v", err)
	}
	if err := validateTargetGraph([]PublicationTarget{
		NewTarget("a", "", TargetTypePost, "bluesky", "d", []TargetID{"b"}),
		NewTarget("b", "", TargetTypePost, "bluesky", "d", []TargetID{"c"}),
		NewTarget("c", "", TargetTypePost, "bluesky", "d", []TargetID{"a"}),
	}); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("a three-node cycle must be rejected: %v", err)
	}
}

func TestAttemptValidate(t *testing.T) {
	ok := NewAttempt(1, tAt(1))
	ok.Status = AttemptStatusFailed
	finished := run.NewTimestamp(tAt(5))
	ok.FinishedAt = &finished
	ok.Error = errSum("net", "unreachable", tAt(5))
	if err := ok.Validate(); err != nil {
		t.Fatalf("a valid failed attempt must validate: %v", err)
	}

	cases := map[string]struct {
		mutate func(a *PublicationAttempt)
		substr string
	}{
		"number zero":          {func(a *PublicationAttempt) { a.Number = 0 }, ">= 1"},
		"bad status":           {func(a *PublicationAttempt) { a.Status = AttemptStatus("maybe") }, "unknown status"},
		"zero start":           {func(a *PublicationAttempt) { a.StartedAt = run.Timestamp{} }, "started_at"},
		"running with end":     {func(a *PublicationAttempt) { a.Status = AttemptStatusRunning; a.FinishedAt = &finished }, "running but has a finished_at"},
		"failed no error":      {func(a *PublicationAttempt) { a.Status = AttemptStatusFailed; a.Error = nil }, "requires an error summary"},
		"succeeded no receipt": {func(a *PublicationAttempt) { a.Status = AttemptStatusSucceeded; a.Error = nil; a.Receipt = nil }, "requires a receipt"},
		"finished before start": {
			func(a *PublicationAttempt) {
				early := run.NewTimestamp(tAt(0))
				a.FinishedAt = &early
			},
			"before it started",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			a := ok
			c.mutate(&a)
			if err := a.Validate(); err == nil || !strings.Contains(err.Error(), c.substr) {
				t.Errorf("%q: got %v, want an error mentioning %q", name, err, c.substr)
			}
		})
	}
}

func TestTrimAttempts(t *testing.T) {
	attempts := make([]PublicationAttempt, MaxAttemptsPerTarget+5)
	for i := range attempts {
		a := NewAttempt(i+1, tAt(i+1))
		a.Status = AttemptStatusFailed
		f := run.NewTimestamp(tAt(i + 100))
		a.FinishedAt = &f
		a.Error = errSum("net", "x", tAt(i+100))
		attempts[i] = a
	}
	trimAttempts(&attempts)
	if len(attempts) != MaxAttemptsPerTarget {
		t.Fatalf("after trimming: got %d attempts, want %d", len(attempts), MaxAttemptsPerTarget)
	}
	if attempts[0].Number != 1 || attempts[len(attempts)-1].Number != MaxAttemptsPerTarget {
		t.Errorf("survivors must be renumbered 1..%d, got first=%d last=%d",
			MaxAttemptsPerTarget, attempts[0].Number, attempts[len(attempts)-1].Number)
	}
	// The oldest survivors must be the most recent original attempts.
	if attempts[0].StartedAt.Equal(run.NewTimestamp(tAt(1))) {
		t.Error("the oldest (trimmed) attempt must be dropped")
	}
	// Under the bound, nothing changes.
	small := []PublicationAttempt{NewAttempt(1, tAt(1)), NewAttempt(2, tAt(2))}
	trimAttempts(&small)
	if len(small) != 2 {
		t.Error("trimming must not touch histories under the bound")
	}
}

func TestValidateAttemptHistory(t *testing.T) {
	running := NewAttempt(1, tAt(1))

	// planned / skipped: no attempts at all.
	if err := validateAttemptHistory("t", StatusPlanned, []PublicationAttempt{running}); err == nil {
		t.Error("a planned target must not carry attempts")
	}
	if err := validateAttemptHistory("t", StatusSkipped, []PublicationAttempt{running}); err == nil {
		t.Error("a skipped target must not carry attempts")
	}
	// publishing: exactly a running latest attempt.
	if err := validateAttemptHistory("t", StatusPublishing, nil); err == nil {
		t.Error("a publishing target must have a running attempt")
	}
	if err := validateAttemptHistory("t", StatusPublishing, []PublicationAttempt{running}); err != nil {
		t.Errorf("a publishing target with a running attempt must be consistent: %v", err)
	}
	finishedAttempt := NewAttempt(1, tAt(1))
	finishedAttempt.Status = AttemptStatusFailed
	f := run.NewTimestamp(tAt(5))
	finishedAttempt.FinishedAt = &f
	finishedAttempt.Error = errSum("net", "x", tAt(5))
	if err := validateAttemptHistory("t", StatusPublishing, []PublicationAttempt{finishedAttempt}); err == nil {
		t.Error("a publishing target must not end on a finished attempt")
	}
	// succeeded: latest must be succeeded.
	if err := validateAttemptHistory("t", StatusSucceeded, nil); err == nil {
		t.Error("a succeeded target must have an attempt")
	}
	if err := validateAttemptHistory("t", StatusSucceeded, []PublicationAttempt{finishedAttempt}); err == nil {
		t.Error("a succeeded target must not end on a failed attempt")
	}
	// non-consecutive numbering must be rejected.
	renamed := finishedAttempt
	renamed.Number = 3
	if err := validateAttemptHistory("t", StatusFailed, []PublicationAttempt{renamed}); err == nil {
		t.Error("attempts must be numbered from 1")
	}
	// over the bound must be rejected.
	many := make([]PublicationAttempt, MaxAttemptsPerTarget+1)
	for i := range many {
		a := NewAttempt(i+1, tAt(i))
		a.Status = AttemptStatusFailed
		f := run.NewTimestamp(tAt(i + 100))
		a.FinishedAt = &f
		a.Error = errSum("net", "x", tAt(i+100))
		many[i] = a
	}
	if err := validateAttemptHistory("t", StatusFailed, many); err == nil {
		t.Error("over the attempt bound must be rejected")
	}
}
