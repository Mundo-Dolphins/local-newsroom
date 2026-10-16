package publication

import (
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// apprID is a synthetic but well-formed approval ID for fixtures.
func apprID(s string) ApprovalID {
	return ApprovalID(deriveID(ApprovalIDPrefix, testTime, s))
}

func TestApprovalIDValidate(t *testing.T) {
	if err := ApprovalID("nope").Validate(); err == nil {
		t.Error("a wrong prefix must be rejected")
	}
	if err := ApprovalID(ApprovalIDPrefix).Validate(); err == nil {
		t.Error("an empty body must be rejected")
	}
	if err := apprID("a").Validate(); err != nil {
		t.Errorf("a derived approval ID should validate: %v", err)
	}
}

func TestNewApprovalIDDerivation(t *testing.T) {
	d := digestOf(briefA)
	a := NewApprovalID(pubID("p"), "javi", d, tAt(1))
	b := NewApprovalID(pubID("p"), "javi", d, tAt(1))
	if a != b {
		t.Error("same inputs must derive the same approval ID")
	}
	if c := NewApprovalID(pubID("p"), "other", d, tAt(1)); c == a {
		t.Error("different principals must derive different IDs")
	}
	if c := NewApprovalID(pubID("p"), "javi", digestOf(briefB), tAt(1)); c == a {
		t.Error("different digests must derive different IDs")
	}
}

// baseApproval returns a valid granted approval.
func baseApproval() *PublicationApproval {
	return NewApproval(pubID("plan-a"), "javi", ApprovalMethodCLI, digestOf(briefA), nil, "approved", tAt(10))
}

func TestNewApproval(t *testing.T) {
	a := baseApproval()
	if err := a.Validate(); err != nil {
		t.Fatalf("a well-formed approval must validate: %v", err)
	}
	if a.Status != ApprovalStatusGranted {
		t.Errorf("new approvals must be granted, got %q", a.Status)
	}
	if a.InvalidatedAt != nil {
		t.Error("a new approval must not be invalidated")
	}

	// Free text is redacted on the way in.
	secret := testToken
	a2 := NewApproval(pubID("p"), "javi "+secret, ApprovalMethodCLI, digestOf(briefA), nil, secret, tAt(10))
	if !strings.Contains(a2.ApprovedBy, run.RedactedPlaceholder) {
		t.Errorf("secret in approved_by must be redacted, got %q", a2.ApprovedBy)
	}
	if !strings.Contains(a2.Note, run.RedactedPlaceholder) {
		t.Errorf("secret in note must be redacted, got %q", a2.Note)
	}
}

func TestApprovalMethodsAndStatuses(t *testing.T) {
	for _, m := range AllApprovalMethods {
		if !m.Valid() {
			t.Errorf("%q should be a valid method", m)
		}
	}
	if ApprovalMethod("weird").Valid() {
		t.Error("unknown methods must not be valid")
	}
	for _, s := range AllApprovalStatuses {
		if !s.Valid() {
			t.Errorf("%q should be a valid status", s)
		}
	}
	if ApprovalStatus("weird").Valid() {
		t.Error("unknown statuses must not be valid")
	}
	if !ApprovalStatusStale.Closed() || !ApprovalStatusRevoked.Closed() {
		t.Error("stale and revoked approvals are closed")
	}
	if ApprovalStatusGranted.Closed() {
		t.Error("a granted approval is not closed")
	}
}

func TestApprovalMatchesDigest(t *testing.T) {
	a := baseApproval() // binds digestA
	if !a.MatchesDigest(string(digestOf(briefA))) {
		t.Error("the approval must match the digest it bound")
	}
	if a.MatchesDigest(string(digestOf(briefB))) {
		t.Error("the approval must not match different content")
	}
}

func TestApprovalCoversTarget(t *testing.T) {
	all := baseApproval() // empty scope
	if !all.CoversTarget(TargetID("any-target")) {
		t.Error("an empty scope must cover every target")
	}
	scoped := NewApproval(pubID("p"), "javi", ApprovalMethodCLI, digestOf(briefA),
		[]TargetID{"a", "b"}, "", tAt(10))
	if !scoped.CoversTarget(TargetID("a")) || !scoped.CoversTarget(TargetID("b")) {
		t.Error("a scoped approval must cover its listed targets")
	}
	if scoped.CoversTarget(TargetID("c")) {
		t.Error("a scoped approval must not cover unlisted targets")
	}
}

func TestApprovalEffectiveFor(t *testing.T) {
	a := NewApproval(pubID("p"), "javi", ApprovalMethodCLI, digestOf(briefA),
		[]TargetID{"a"}, "", tAt(10))
	if !a.EffectiveFor(string(digestOf(briefA)), TargetID("a")) {
		t.Error("a granted, matching, in-scope approval must be effective")
	}
	if a.EffectiveFor(string(digestOf(briefB)), TargetID("a")) {
		t.Error("a digest mismatch must make the approval ineffective")
	}
	if a.EffectiveFor(string(digestOf(briefA)), TargetID("b")) {
		t.Error("a scope mismatch must make the approval ineffective")
	}
	a.Invalidate(ApprovalStatusStale, "artifact changed", tAt(20))
	if a.EffectiveFor(string(digestOf(briefA)), TargetID("a")) {
		t.Error("a stale approval must never be effective")
	}
}

func TestApprovalInvalidate(t *testing.T) {
	a := baseApproval()
	a.Invalidate(ApprovalStatusStale, "artifact changed", tAt(20))
	if a.Status != ApprovalStatusStale || a.InvalidatedAt == nil || a.InvalidationReason == "" {
		t.Errorf("staleness must set status, time and reason: %+v", a)
	}
	if err := a.Validate(); err != nil {
		t.Errorf("a stale approval with its closure bookkeeping must validate: %v", err)
	}

	// Closing is idempotent-safe only for granted approvals; re-closing a
	// closed approval is a no-op that keeps the original closure.
	first := *a.InvalidatedAt
	oldReason := a.InvalidationReason
	a.Invalidate(ApprovalStatusRevoked, "ignored", tAt(30))
	if a.Status != ApprovalStatusStale {
		t.Errorf("re-invalidating a closed approval must be a no-op, got %q", a.Status)
	}
	if *a.InvalidatedAt != first || a.InvalidationReason != oldReason {
		t.Error("re-invalidating a closed approval must not rewrite its closure")
	}

	// An invalid status is a no-op.
	a2 := baseApproval()
	a2.Invalidate(ApprovalStatusGranted, "bogus", tAt(30))
	if a2.Status != ApprovalStatusGranted || a2.InvalidatedAt != nil {
		t.Error("invalidating with the granted status must be a no-op")
	}
}

func TestApprovalValidate(t *testing.T) {
	valid := baseApproval()
	if err := valid.Validate(); err != nil {
		t.Fatalf("base approval must validate: %v", err)
	}

	t.Run("nil approval", func(t *testing.T) {
		if err := (*PublicationApproval)(nil).Validate(); err == nil {
			t.Error("a nil approval must not validate")
		}
	})

	cases := map[string]struct {
		mutate func(a *PublicationApproval)
		substr string
	}{
		"missing id":         {func(a *PublicationApproval) { a.ID = "" }, "approval id"},
		"bad id":             {func(a *PublicationApproval) { a.ID = ApprovalID("nope") }, "must start with"},
		"missing plan id":    {func(a *PublicationApproval) { a.PlanID = "" }, "publication id"},
		"missing principal":  {func(a *PublicationApproval) { a.ApprovedBy = "" }, "approved_by is required"},
		"bad principal":      {func(a *PublicationApproval) { a.ApprovedBy = "bad principal!" }, "invalid"},
		"long principal":     {func(a *PublicationApproval) { a.ApprovedBy = strings.Repeat("j", maxApprovedByLen+1) }, "exceeds"},
		"secret principal":   {func(a *PublicationApproval) { a.ApprovedBy = "tok" + testToken }, "secret-looking"},
		"bad method":         {func(a *PublicationApproval) { a.Method = ApprovalMethod("voice") }, "unknown approval method"},
		"bad digest":         {func(a *PublicationApproval) { a.ArtifactDigest = Digest("sha256:zz") }, "digest"},
		"long note":          {func(a *PublicationApproval) { a.Note = strings.Repeat("n", maxApprovalNote+1) }, "exceeds"},
		"secret note":        {func(a *PublicationApproval) { a.Note = testToken }, "secret-looking"},
		"bad status":         {func(a *PublicationApproval) { a.Status = ApprovalStatus("maybe") }, "unknown approval status"},
		"missing granted at": {func(a *PublicationApproval) { a.GrantedAt = run.Timestamp{} }, "granted_at is required"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			a := *valid
			c.mutate(&a)
			err := a.Validate()
			if err == nil {
				t.Fatal("want a validation error")
			}
			if !strings.Contains(err.Error(), c.substr) {
				t.Errorf("error %q does not mention %q", err, c.substr)
			}
		})
	}

	t.Run("granted must not be invalidated", func(t *testing.T) {
		a := *valid
		ts := run.NewTimestamp(tAt(20))
		a.InvalidatedAt = &ts
		if err := a.Validate(); err == nil {
			t.Error("a granted approval with an invalidated_at must not validate")
		}
	})
	t.Run("stale requires closure bookkeeping", func(t *testing.T) {
		a := *valid
		a.Status = ApprovalStatusStale
		err := a.Validate()
		if err == nil || !strings.Contains(err.Error(), "invalidated_at") {
			t.Fatalf("a stale approval without closure must fail: %v", err)
		}
	})
	t.Run("revoked requires closure bookkeeping", func(t *testing.T) {
		a := *valid
		a.Status = ApprovalStatusRevoked
		a.InvalidationReason = ""
		err := a.Validate()
		if err == nil || !strings.Contains(err.Error(), "invalidation reason") {
			t.Fatalf("a revoked approval without a reason must fail: %v", err)
		}
	})
	t.Run("secret invalidation reason", func(t *testing.T) {
		a := *valid
		a.Status = ApprovalStatusRevoked
		ts := run.NewTimestamp(tAt(20))
		a.InvalidatedAt = &ts
		a.InvalidationReason = testToken
		if err := a.Validate(); err == nil || !strings.Contains(err.Error(), "secret-looking") {
			t.Fatalf("want a secret error: %v", err)
		}
	})
	t.Run("duplicate scope targets", func(t *testing.T) {
		a := *valid
		a.Targets = []TargetID{"x", "x"}
		if err := a.Validate(); err == nil || !strings.Contains(err.Error(), "twice") {
			t.Fatalf("duplicate scope must fail: %v", err)
		}
	})
	t.Run("multiple errors joined", func(t *testing.T) {
		a := *valid
		a.ID = ""
		a.Method = ApprovalMethod("nope")
		a.GrantedAt = run.Timestamp{}
		err := a.Validate()
		if err == nil {
			t.Fatal("want errors")
		}
		if !strings.Contains(err.Error(), "id") || !strings.Contains(err.Error(), "method") {
			t.Errorf("expected multiple joined errors, got %q", err)
		}
	})
}
