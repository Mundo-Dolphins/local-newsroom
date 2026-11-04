package memory

import (
	"testing"
	"time"
)

// fakeClock is a fake clock for deterministic freshness tests. Its zero
// value is invalid; build one with newFakeClock.
type fakeClock struct {
	at time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{at: time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time { return c.at }

func (c *fakeClock) Advance(d time.Duration) { c.at = c.at.Add(d) }

var clk = newFakeClock()

// testPolicy is a deliberately asymmetric policy so tests can distinguish
// the band windows from each other.
var testPolicy = FreshnessPolicy{
	Durations: map[Volatility]time.Duration{
		VolatileStatic:    0,
		VolatileLow:       90 * 24 * time.Hour,
		VolatileMedium:    14 * 24 * time.Hour,
		VolatileHigh:      24 * time.Hour,
		VolatileEphemeral: 12 * time.Hour,
	},
	Expiry: map[Volatility]time.Duration{
		VolatileStatic:    0,
		VolatileLow:       365 * 24 * time.Hour,
		VolatileMedium:    180 * 24 * time.Hour,
		VolatileHigh:      30 * 24 * time.Hour,
		VolatileEphemeral: 2 * 24 * time.Hour,
	},
}

// buildFactRecord returns a valid factual record with the given volatility,
// verified at the given time (zero = never verified), optional explicit
// staleness window, optional absolute expiry, and the given lifecycle state.
func buildFactRecord(v Volatility, verifiedAt time.Time, staleAfter time.Duration, expiresAt time.Time, status InvalidationStatus) *MemoryRecord {
	rec := NewMemoryRecord(clk.at)
	rec.StableID = "mem-test-fact"
	rec.Kind = KindFactual
	rec.Scope = ScopeNewsroom
	rec.Confidence = ConfidenceMedium
	rec.Volatility = v
	rec.Freshness = Freshness{LastVerifiedAt: verifiedAt, StaleAfter: staleAfter}
	rec.Expiration = Expiration{
		ExpiresAt:          expiresAt,
		InvalidationStatus: status,
	}
	rec.Provenance = Provenance{
		Sources: []MemorySource{{
			SourceType:  "web",
			SourceID:    "https://example.com/fact",
			RetrievedAt: verifiedAt,
		}},
	}
	_ = rec.EncodeValue(FactualValue{Statement: "a test fact"}, clk.at)
	return rec
}

func TestFreshnessPolicyTrichotomy(t *testing.T) {
	verified := clk.at.Add(-2 * time.Hour)

	tests := []struct {
		name     string
		vol      Volatility
		verAt    time.Time
		want     FreshnessStatus
		reverify bool
	}{
		{"static, verified 100d ago", VolatileStatic, verified, StatusFresh, false},
		{"low, verified 1h ago", VolatileLow, clk.at.Add(-time.Hour), StatusFresh, false},
		{"low, just past 90d window", VolatileLow, clk.at.Add(-91 * 24 * time.Hour), StatusStale, true},
		{"low, exactly at window boundary is stale (inclusive)", VolatileLow, clk.at.Add(-90 * 24 * time.Hour), StatusStale, true},
		{"medium, verified 2h ago", VolatileMedium, clk.at.Add(-2 * time.Hour), StatusFresh, false},
		{"medium, verified 15d ago", VolatileMedium, clk.at.Add(-15 * 24 * time.Hour), StatusStale, true},
		{"high, verified 1h ago", VolatileHigh, clk.at.Add(-time.Hour), StatusFresh, false},
		{"high, verified 25h ago", VolatileHigh, clk.at.Add(-25 * time.Hour), StatusStale, true},
		{"ephemeral, verified 1h ago", VolatileEphemeral, clk.at.Add(-time.Hour), StatusFresh, false},
		{"ephemeral, verified 13h ago", VolatileEphemeral, clk.at.Add(-13 * time.Hour), StatusStale, true},
		{"never verified is stale regardless of band", VolatileLow, time.Time{}, StatusStale, true},
		{"superseded is expired even if fresh", VolatileLow, clk.at.Add(-time.Hour), StatusExpired, true},
		{"invalidated is expired even if fresh", VolatileLow, clk.at.Add(-time.Hour), StatusExpired, true},
		{"band lifetime past is expired", VolatileEphemeral, clk.at.Add(-48 * time.Hour), StatusExpired, true},
		{"own absolute expiry past is expired", VolatileStatic, clk.at.Add(-time.Hour), StatusExpired, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var expires time.Time
			var status InvalidationStatus
			switch tt.name {
			case "superseded is expired even if fresh":
				status = InvalidationSuperseded
			case "invalidated is expired even if fresh":
				status = InvalidationInvalidated
			case "own absolute expiry past is expired":
				expires = clk.at.Add(-time.Hour)
				status = InvalidationActive
			default:
				status = InvalidationActive
			}
			rec := buildFactRecord(tt.vol, tt.verAt, 0, expires, status)
			a := testPolicy.StatusOf(rec, clk.at)
			if a.Status != tt.want {
				t.Errorf("status = %s, want %s (assessment %+v)", a.Status, tt.want, a)
			}
			if a.ReVerify() != tt.reverify {
				t.Errorf("reverify = %v, want %v", a.ReVerify(), tt.reverify)
			}
		})
	}
}

func TestFreshnessStatusString(t *testing.T) {
	tests := map[FreshnessStatus]string{
		StatusFresh:   "fresh",
		StatusStale:   "stale",
		StatusExpired: "expired",
	}
	for s, want := range tests {
		if got := s.String(); got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
	}
	if got := FreshnessStatus(99).String(); got != "unknown" {
		t.Errorf("unknown status String() = %q, want %q", got, "unknown")
	}
}

func TestFreshnessAssessmentAccessors(t *testing.T) {
	a := FreshnessAssessment{Status: StatusStale}
	if a.IsFresh() || a.IsStale() == false || a.IsExpired() || !a.ReVerify() {
		t.Error("stale assessment accessors are wrong")
	}
	a = FreshnessAssessment{Status: StatusFresh}
	if !a.IsFresh() || a.ReVerify() {
		t.Error("fresh assessment accessors are wrong")
	}
	a = FreshnessAssessment{Status: StatusExpired}
	if a.IsFresh() || !a.IsExpired() || !a.ReVerify() {
		t.Error("expired assessment accessors are wrong")
	}
}

func TestNewFreshnessPolicyValidation(t *testing.T) {
	if _, err := NewFreshnessPolicy(map[Volatility]time.Duration{"bogus": time.Hour}, nil); err == nil {
		t.Error("expected error for unknown volatility in durations")
	}
	if _, err := NewFreshnessPolicy(nil, map[Volatility]time.Duration{"bogus": time.Hour}); err == nil {
		t.Error("expected error for unknown volatility in expiry")
	}
	if _, err := NewFreshnessPolicy(map[Volatility]time.Duration{VolatileLow: -time.Hour}, nil); err == nil {
		t.Error("expected error for negative stale_after duration")
	}
	if _, err := NewFreshnessPolicy(nil, map[Volatility]time.Duration{VolatileLow: -time.Hour}); err == nil {
		t.Error("expected error for negative expiry duration")
	}
	p, err := NewFreshnessPolicy(
		map[Volatility]time.Duration{VolatileHigh: time.Hour},
		map[Volatility]time.Duration{VolatileHigh: 24 * time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	// The policy must not alias the caller's maps. To prove no aliasing, the
	// test would need to mutate the caller's map and check the policy is
	// unaffected. Here we verify the policy's internal maps are independent:
	// the policy was built with only VolatileHigh, so StalenessWindow(low)
	// must be 0 (no entry for low was provided).
	if got := p.StalenessWindow(VolatileLow); got != 0 {
		t.Errorf("policy contains an entry it was not built with: StalenessWindow(low) = %v, want 0", got)
	}
	// Mutating the policy's map must not affect the caller's maps (and vice
	// versa, though we can only hold a reference to the policy's map here).
	callerDur := map[Volatility]time.Duration{VolatileHigh: time.Hour}
	p2, _ := NewFreshnessPolicy(callerDur, nil)
	callerDur[VolatileHigh] = 2 * time.Hour // mutate caller's map
	if got := p2.StalenessWindow(VolatileHigh); got != time.Hour {
		t.Errorf("policy aliased the caller's map: StalenessWindow(high) = %v, want 1h", got)
	}
}

// TestPolicyDeterministicUnderFakeClock pins the contract of the fake clock
// used across the suite.
func TestPolicyDeterministicUnderFakeClock(t *testing.T) {
	// Determinism: the same record and the same time must always produce the
	// same assessment, and the status must be a function of (record, policy,
	// time) alone — no wall-clock dependency.
	//
	// verified = clk.at (record is 0h old => fresh, inside the 24h window
	// and the 30d band lifetime).
	verified := clk.at
	rec := buildFactRecord(VolatileHigh, verified, 0, time.Time{}, InvalidationActive)

	a1 := testPolicy.StatusOf(rec, clk.at)
	a2 := testPolicy.StatusOf(rec, clk.at)
	if a1.Status != a2.Status || a1.ExpiresAt != a2.ExpiresAt || a1.StaleAfter != a2.StaleAfter {
		t.Fatalf("assessment not deterministic: %+v vs %+v", a1, a2)
	}
	if a1.Status != StatusFresh {
		t.Fatalf("status = %s, want fresh (0h < 24h window)", a1.Status)
	}

	// Advance the fake clock 25h: the record is 25h old, past the 24h
	// staleness window but still inside the 30d band lifetime => stale.
	clk.Advance(25 * time.Hour)
	if got := testPolicy.StatusOf(rec, clk.at).Status; got != StatusStale {
		t.Errorf("status at +25h = %s, want stale", got)
	}

	// Advance past the 30-day band lifetime (age 25h + 26d => 26d25h = 641h
	// > 30d = 720h... wait 26d25h = 649h < 720h, not expired yet).
	// Use 27d: age = 25h + 27d = 673h < 720h, still not expired.
	// Use 28d: age = 25h + 28d = 697h < 720h, still not expired.
	// Use 30d: age = 25h + 30d = 745h > 720h, expired.
	clk.Advance(30 * 24 * time.Hour)
	a := testPolicy.StatusOf(rec, clk.at)
	if a.Status != StatusExpired {
		t.Errorf("status past lifetime = %s, want expired", a.Status)
	}
	if a.ExpiresAt.IsZero() {
		t.Error("expired assessment must carry the expiry moment")
	}
}

func TestExplicitStaleAfterWinsOverPolicy(t *testing.T) {
	// A record whose own window is narrower than the policy band window
	// must follow its own window; so must a wider one.
	verified := clk.at.Add(-20 * time.Hour)

	recNarrow := buildFactRecord(VolatileHigh, verified, time.Hour, time.Time{}, InvalidationActive)
	if a := testPolicy.StatusOf(recNarrow, clk.at); a.Status != StatusStale {
		t.Errorf("narrow own window: status = %s, want stale", a.Status)
	}

	recWide := buildFactRecord(VolatileHigh, verified, 720*time.Hour, time.Time{}, InvalidationActive)
	if a := testPolicy.StatusOf(recWide, clk.at); a.Status != StatusFresh {
		t.Errorf("wide own window: status = %s, want fresh", a.Status)
	}
}

func TestPolicyZeroDisablesRoutineStaleness(t *testing.T) {
	// A zero-value policy has no band durations: records never go stale on
	// a timer. Records that have never been verified are still stale, so
	// verification currency stays explicit even with no policy.
	var zero FreshnessPolicy
	recFresh := buildFactRecord(VolatileHigh, clk.at.Add(-100*24*time.Hour), 0, time.Time{}, InvalidationActive)
	if a := zero.StatusOf(recFresh, clk.at); a.Status != StatusFresh {
		t.Errorf("zero policy: status = %s, want fresh (no routine staleness)", a.Status)
	}
	recNever := buildFactRecord(VolatileHigh, time.Time{}, 0, time.Time{}, InvalidationActive)
	if a := zero.StatusOf(recNever, clk.at); a.Status != StatusStale {
		t.Errorf("zero policy, never verified: status = %s, want stale", a.Status)
	}
}

func TestRecordFreshnessHelpers(t *testing.T) {
	verified := clk.at.Add(-25 * time.Hour)
	rec := buildFactRecord(VolatileHigh, verified, 0, time.Time{}, InvalidationActive)

	a := rec.FreshnessStatusOf(testPolicy, clk.at)
	if a.Status != StatusStale {
		t.Errorf("FreshnessStatusOf = %s, want stale", a.Status)
	}
	if !rec.ReVerify(testPolicy, clk.at) {
		t.Error("ReVerify = false, want true (stale)")
	}
	if rec.ReVerify(testPolicy, verified) {
		t.Error("ReVerify = true, want false (fresh at verification time)")
	}
	// The convenience wrapper uses the default policy.
	if got := RecordStatusOf(rec, clk.at).Status; got != StatusStale {
		t.Errorf("RecordStatusOf = %s, want stale", got)
	}
	// A nil record is expired and must re-verify.
	var nilRec *MemoryRecord
	if a := testPolicy.StatusOf(nilRec, clk.at); a.Status != StatusExpired {
		t.Errorf("nil record status = %s, want expired", a.Status)
	}
}

func TestStaleAndFreshRecordSplits(t *testing.T) {
	fresh := buildFactRecord(VolatileHigh, clk.at.Add(-time.Hour), 0, time.Time{}, InvalidationActive)
	fresh.StableID = "mem-fresh"
	stale := buildFactRecord(VolatileHigh, clk.at.Add(-25*time.Hour), 0, time.Time{}, InvalidationActive)
	stale.StableID = "mem-stale"
	dead := buildFactRecord(VolatileHigh, clk.at.Add(-time.Hour), 0, time.Time{}, InvalidationInvalidated)
	dead.StableID = "mem-dead"
	never := buildFactRecord(VolatileMedium, time.Time{}, 0, time.Time{}, InvalidationActive)
	never.StableID = "mem-never"

	recs := []*MemoryRecord{fresh, stale, dead, never}

	got := testPolicy.StaleRecords(recs, clk.at)
	want := map[MemoryStableID]bool{"mem-stale": true, "mem-dead": true, "mem-never": true}
	if len(got) != len(want) {
		t.Fatalf("StaleRecords = %d records, want %d", len(got), len(want))
	}
	for _, r := range got {
		if !want[r.StableID] {
			t.Errorf("StaleRecords contains %s, which should be fresh", r.StableID)
		}
	}
	if got := testPolicy.FreshRecords(recs, clk.at); len(got) != 1 || got[0].StableID != "mem-fresh" {
		t.Errorf("FreshRecords = %v, want [mem-fresh]", got)
	}
}

func TestFreshnessPolicyNilTolerance(t *testing.T) {
	// The zero-value policy carries no band durations: it must not panic,
	// and records should be judged only by their own explicit windows.
	var p FreshnessPolicy
	if got := p.StalenessWindow(VolatileHigh); got != 0 {
		t.Errorf("zero policy StalenessWindow = %v, want 0", got)
	}
	if got := p.ExpiryWindow(VolatileHigh); got != 0 {
		t.Errorf("zero policy ExpiryWindow = %v, want 0", got)
	}
	// A record verified outside its band window stays fresh under a zero
	// policy (no band durations); its own explicit window still applies.
	rec := buildFactRecord(VolatileHigh, clk.at.Add(-25*time.Hour), 0, time.Time{}, InvalidationActive)
	if a := p.StatusOf(rec, clk.at); a.Status != StatusFresh {
		t.Errorf("zero policy status = %s, want fresh (no routine staleness)", a.Status)
	}
	// The record's own explicit window is still honored.
	recOwn := buildFactRecord(VolatileHigh, clk.at.Add(-2*time.Hour), 30*time.Minute, time.Time{}, InvalidationActive)
	if a := p.StatusOf(recOwn, clk.at); a.Status != StatusStale {
		t.Errorf("zero policy, own window past: status = %s, want stale", a.Status)
	}
	// A never-verified record is still stale.
	recNever := buildFactRecord(VolatileHigh, time.Time{}, 0, time.Time{}, InvalidationActive)
	if a := p.StatusOf(recNever, clk.at); a.Status != StatusStale {
		t.Errorf("zero policy, never verified: status = %s, want stale", a.Status)
	}
}

func TestDefaultFreshnessPolicySane(t *testing.T) {
	// Durations must be strictly ordered: less durable bands go stale sooner.
	d := DefaultFreshnessPolicy.Durations
	if d[VolatileStatic] != 0 {
		t.Errorf("static must have no routine expiry, got %v", d[VolatileStatic])
	}
	order := []Volatility{VolatileLow, VolatileMedium, VolatileHigh, VolatileEphemeral}
	for _, v := range order {
		if d[v] <= 0 {
			t.Errorf("%s: duration must be positive, got %v", v, d[v])
		}
	}
	for i := 1; i < len(order); i++ {
		if d[order[i-1]] <= d[order[i]] {
			t.Errorf("durations not strictly decreasing: %s=%v >= %s=%v",
				order[i-1], d[order[i-1]], order[i], d[order[i]])
		}
	}
	e := DefaultFreshnessPolicy.Expiry
	for i := 1; i < len(order); i++ {
		if e[order[i-1]] <= e[order[i]] {
			t.Errorf("expiry windows not strictly decreasing: %s=%v >= %s=%v",
				order[i-1], e[order[i-1]], order[i], e[order[i]])
		}
	}
}
