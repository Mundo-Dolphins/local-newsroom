package entitymemory

import (
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/memory"
)

// freshnessNow is the reference time for freshness tests. Using a separate
// variable (rather than testNow) keeps the tests self-documenting.
var freshnessNow = testNow

// TestFreshnessStatusByBand walks each volatility band with the default
// policy and asserts the expected trichotomy.
func TestFreshnessStatusByBand(t *testing.T) {
	s := newTestStore(t)
	mustCreate(t, s, &Entity{
		ID:            "player-freshness-test",
		Type:          TypePlayer,
		CanonicalName: "Freshness Test",
		Attributes: []Attribute{
			{Key: "draftYear", Value: "2021", Volatility: memory.VolatileStatic,
				LastVerifiedAt: freshnessNow.Add(-100 * 24 * time.Hour),
				Sources:        []memory.MemorySource{webSource("s1", freshnessNow)}},
			{Key: "college", Value: "Example U", Volatility: memory.VolatileLow,
				LastVerifiedAt: freshnessNow.Add(-5 * 24 * time.Hour),
				Sources:        []memory.MemorySource{webSource("s2", freshnessNow)}},
			{Key: "collegeFresh", Value: "Example U", Volatility: memory.VolatileLow,
				StaleAfter:     7 * 24 * time.Hour,
				LastVerifiedAt: freshnessNow.Add(-8 * 24 * time.Hour),
				Sources:        []memory.MemorySource{webSource("s3", freshnessNow)}},
		},
		Confidence:     memory.ConfidenceHigh,
		LastVerifiedAt: freshnessNow,
	})

	// The store must expose a freshness policy (default) without error.
	if s.FreshnessPolicy().StalenessWindow(memory.VolatileHigh) != 24*time.Hour {
		t.Error("store should default to the default policy")
	}

	// draftYear (static, verified 100d ago): no routine staleness for static
	// band, and no absolute expiry in the default policy => fresh.
	if got, _ := s.FreshnessStatus("player-freshness-test", "draftYear", freshnessNow); got != memory.StatusFresh {
		t.Errorf("draftYear status = %s, want fresh", got)
	}
	// college (low band, 5d old): inside the 90d low window => fresh.
	if got, _ := s.FreshnessStatus("player-freshness-test", "college", freshnessNow); got != memory.StatusFresh {
		t.Errorf("college status = %s, want fresh", got)
	}
	// collegeFresh: own 7d window, 8d old => stale (own window wins).
	if got, _ := s.FreshnessStatus("player-freshness-test", "collegeFresh", freshnessNow); got != memory.StatusStale {
		t.Errorf("collegeFresh status = %s, want stale", got)
	}
}

// TestFreshnessStatusStaleAfterAdvance pins the stale transition: an
// attribute verified just inside its window is fresh; after the reference
// time advances past the window it is stale; past the band lifetime it is
// expired.
func TestFreshnessStatusStaleAfterAdvance(t *testing.T) {
	s := newTestStore(t)
	mustCreate(t, s, &Entity{
		ID:            "player-stale-advance",
		Type:          TypePlayer,
		CanonicalName: "Stale Advance",
		Attributes: []Attribute{
			{Key: "team", Value: "Example City Sharks", Volatility: memory.VolatileHigh,
				// 24h window: verified 1h before the reference time.
				LastVerifiedAt: freshnessNow.Add(-1 * time.Hour),
				Sources:        []memory.MemorySource{webSource("s1", freshnessNow)}},
		},
		Confidence:     memory.ConfidenceHigh,
		LastVerifiedAt: freshnessNow,
	})

	// At the reference time the attribute is 1h old (inside the 24h window)
	// => fresh.
	if got, _ := s.FreshnessStatus("player-stale-advance", "team", freshnessNow); got != memory.StatusFresh {
		t.Fatalf("status at reference = %s, want fresh", got)
	}

	// Advance the reference 25h: the attribute is 26h old, past the 24h
	// window but still inside the 30d band lifetime => stale.
	adv := freshnessNow.Add(25 * time.Hour)
	if got, _ := s.FreshnessStatus("player-stale-advance", "team", adv); got != memory.StatusStale {
		t.Errorf("status at +25h = %s, want stale", got)
	}

	// Advance well past the 30d band lifetime => expired dominates stale.
	adv2 := freshnessNow.Add(31 * 24 * time.Hour)
	if got, _ := s.FreshnessStatus("player-stale-advance", "team", adv2); got != memory.StatusExpired {
		t.Errorf("status at +31d = %s, want expired", got)
	}
}

// TestAttributeFreshnessesAndStaleAttributes covers the bulk queries.
func TestAttributeFreshnessesAndStaleAttributes(t *testing.T) {
	s := newTestStore(t)
	mustCreate(t, s, &Entity{
		ID:            "player-bulk-freshness",
		Type:          TypePlayer,
		CanonicalName: "Bulk Freshness",
		Attributes: []Attribute{
			{Key: "draftYear", Value: "2021", Volatility: memory.VolatileStatic,
				LastVerifiedAt: freshnessNow,
				Sources:        []memory.MemorySource{webSource("s1", freshnessNow)}},
			{Key: "team", Value: "Example City Sharks", Volatility: memory.VolatileHigh,
				LastVerifiedAt: freshnessNow.Add(-48 * time.Hour), // past 24h window
				Sources:        []memory.MemorySource{webSource("s2", freshnessNow)}},
		},
		Confidence:     memory.ConfidenceHigh,
		LastVerifiedAt: freshnessNow,
	})

	all, err := s.AttributeFreshnesses("player-bulk-freshness", freshnessNow)
	if err != nil {
		t.Fatalf("AttributeFreshnesses: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("len = %d, want 2", len(all))
	}
	// Ordered by key: draftYear < team.
	if all[0].Key != "draftYear" || all[1].Key != "team" {
		t.Fatalf("order: %s, %s", all[0].Key, all[1].Key)
	}
	if all[0].Status != memory.StatusFresh {
		t.Errorf("draftYear = %s, want fresh", all[0].Status)
	}
	if all[1].Status != memory.StatusStale {
		t.Errorf("team = %s, want stale", all[1].Status)
	}

	stale, err := s.StaleAttributes("player-bulk-freshness", freshnessNow)
	if err != nil {
		t.Fatalf("StaleAttributes: %v", err)
	}
	if len(stale) != 1 || stale[0].Key != "team" {
		t.Fatalf("stale = %v, want [team]", stale)
	}
}

// TestFreshnessStatusErrors covers the error paths.
func TestFreshnessStatusErrors(t *testing.T) {
	s := newTestStore(t)
	mustCreate(t, s, &Entity{
		ID:            "player-err",
		Type:          TypePlayer,
		CanonicalName: "Err Player",
		Attributes: []Attribute{
			{Key: "team", Value: "X", Volatility: memory.VolatileHigh,
				LastVerifiedAt: freshnessNow,
				Sources:        []memory.MemorySource{webSource("s1", freshnessNow)}},
		},
		Confidence:     memory.ConfidenceHigh,
		LastVerifiedAt: freshnessNow,
	})

	if _, err := s.FreshnessStatus("unknown", "team", freshnessNow); err != ErrEntityNotFound {
		t.Errorf("unknown entity: err = %v, want ErrEntityNotFound", err)
	}
	if _, err := s.FreshnessStatus("player-err", "nope", freshnessNow); err != ErrAttributeNotFound {
		t.Errorf("unknown attribute: err = %v, want ErrAttributeNotFound", err)
	}
	if _, err := s.AttributeFreshnesses("unknown", freshnessNow); err != ErrEntityNotFound {
		t.Errorf("unknown entity bulk: err = %v, want ErrEntityNotFound", err)
	}
	if _, err := s.StaleAttributes("unknown", freshnessNow); err != ErrEntityNotFound {
		t.Errorf("unknown entity stale: err = %v, want ErrEntityNotFound", err)
	}
}

// TestSetFreshnessPolicyCustom verifies that a custom policy changes the
// assessment (a narrow band window makes an otherwise-fresh attribute stale).
func TestSetFreshnessPolicyCustom(t *testing.T) {
	s := newTestStore(t)
	mustCreate(t, s, &Entity{
		ID:            "player-custom-policy",
		Type:          TypePlayer,
		CanonicalName: "Custom Policy",
		Attributes: []Attribute{
			{Key: "team", Value: "Example City Sharks", Volatility: memory.VolatileHigh,
				LastVerifiedAt: freshnessNow.Add(-1 * time.Hour),
				Sources:        []memory.MemorySource{webSource("s1", freshnessNow)}},
		},
		Confidence:     memory.ConfidenceHigh,
		LastVerifiedAt: freshnessNow,
	})

	// Under the default policy (24h high window) the attribute is fresh.
	if got, _ := s.FreshnessStatus("player-custom-policy", "team", freshnessNow); got != memory.StatusFresh {
		t.Fatalf("default policy status = %s, want fresh", got)
	}

	// Install a custom policy with a 30-minute high window.
	p, err := memory.NewFreshnessPolicy(
		map[memory.Volatility]time.Duration{
			memory.VolatileStatic:    0,
			memory.VolatileLow:       90 * 24 * time.Hour,
			memory.VolatileMedium:    14 * 24 * time.Hour,
			memory.VolatileHigh:      30 * time.Minute,
			memory.VolatileEphemeral: 1 * time.Hour,
		},
		map[memory.Volatility]time.Duration{
			memory.VolatileLow:       365 * 24 * time.Hour,
			memory.VolatileMedium:    180 * 24 * time.Hour,
			memory.VolatileHigh:      30 * 24 * time.Hour,
			memory.VolatileEphemeral: 2 * 24 * time.Hour,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.SetFreshnessPolicy(p)
	// The store must now report the custom policy.
	if got := s.FreshnessPolicy().StalenessWindow(memory.VolatileHigh); got != 30*time.Minute {
		t.Fatalf("FreshnessPolicy().StalenessWindow(high) = %v, want 30m", got)
	}
	// 1h > 30min => stale under the custom policy.
	if got, _ := s.FreshnessStatus("player-custom-policy", "team", freshnessNow); got != memory.StatusStale {
		t.Errorf("custom policy status = %s, want stale", got)
	}
}

// TestFreshnessDeterministicUnderFakeClock pins the attribute-level
// determinism contract: same attribute, same time, same status; advancing
// the reference time changes the status predictably.
func TestFreshnessDeterministicUnderFakeClock(t *testing.T) {
	s := newTestStore(t)
	mustCreate(t, s, &Entity{
		ID:            "player-det",
		Type:          TypePlayer,
		CanonicalName: "Deterministic",
		Attributes: []Attribute{
			{Key: "team", Value: "Example City Sharks", Volatility: memory.VolatileMedium,
				LastVerifiedAt: freshnessNow.Add(-1 * 24 * time.Hour),
				Sources:        []memory.MemorySource{webSource("s1", freshnessNow)}},
		},
		Confidence:     memory.ConfidenceHigh,
		LastVerifiedAt: freshnessNow,
	})

	// The same (attribute, time) must give the same status on every call.
	s1, _ := s.FreshnessStatus("player-det", "team", freshnessNow)
	s2, _ := s.FreshnessStatus("player-det", "team", freshnessNow)
	if s1 != s2 {
		t.Fatalf("not deterministic: %s vs %s", s1, s2)
	}
	// 1d < 14d medium window => fresh.
	if s1 != memory.StatusFresh {
		t.Fatalf("status = %s, want fresh", s1)
	}

	// Advance 15 days: past the 14d window, inside the 180d lifetime => stale.
	adv := freshnessNow.Add(15 * 24 * time.Hour)
	if got, _ := s.FreshnessStatus("player-det", "team", adv); got != memory.StatusStale {
		t.Errorf("status at +15d = %s, want stale", got)
	}

	// Advance past the 180d lifetime => expired.
	adv2 := freshnessNow.Add(181 * 24 * time.Hour)
	if got, _ := s.FreshnessStatus("player-det", "team", adv2); got != memory.StatusExpired {
		t.Errorf("status at +181d = %s, want expired", got)
	}
}
