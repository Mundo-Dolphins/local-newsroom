package entitymemory

import (
	"context"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/memory"
)

func TestResolveByCanonicalID(t *testing.T) {
	s := newTestStore(t)
	seedStore(t, s)

	res := s.Resolve(Mention{Text: "player-jordan-rivers-nfl"})
	if res.Unresolved || len(res.Ambiguous) > 0 || res.Resolved == nil {
		t.Fatalf("id resolve: %+v", res)
	}
	if res.Resolved.ID != "player-jordan-rivers-nfl" {
		t.Fatalf("resolved %s, want the id-named entity", res.Resolved.ID)
	}
	if res.MatchedVia != TierStableID {
		t.Fatalf("matched via %s, want stable_id", res.MatchedVia)
	}
}

func TestResolveByCanonicalName(t *testing.T) {
	s := newTestStore(t)
	seedStore(t, s)
	s.WithNow(testNow)

	// Case- and whitespace-insensitive, but otherwise exact. The afcon
	// Rivers is stale (40 days); the tie-break picks the fresh NFL Rivers.
	res := s.Resolve(Mention{Text: "  JORDAN   Rivers "})
	if len(res.Ambiguous) != 0 || res.Resolved == nil {
		t.Fatalf("folded name resolve: %+v", res)
	}
	if res.Resolved.ID != "player-jordan-rivers-nfl" {
		t.Fatalf("resolved %s, want NFL Rivers", res.Resolved.ID)
	}
	if res.MatchedVia != TierCanonicalName {
		t.Fatalf("matched via %s, want canonical_name", res.MatchedVia)
	}

	// Fuzzy-adjacent text must NOT match: no fuzzy matching.
	if got := s.Resolve(Mention{Text: "Jordan River"}); !got.Unresolved {
		t.Fatalf("near-miss resolved: %+v", got)
	}
	if got := s.Resolve(Mention{Text: "jordan rivers n"}); !got.Unresolved {
		t.Fatalf("suffix near-miss resolved: %+v", got)
	}
	if got := s.Resolve(Mention{Text: ""}); !got.Unresolved {
		t.Fatalf("empty mention resolved: %+v", got)
	}
}

func TestResolveByAlias(t *testing.T) {
	s := newTestStore(t)
	seedStore(t, s)

	res := s.Resolve(Mention{Text: "JR"})
	if len(res.Ambiguous) != 0 || res.Resolved == nil {
		t.Fatalf("alias resolve: %+v", res)
	}
	if res.Resolved.ID != "player-jordan-rivers-nfl" || res.MatchedVia != TierAlias {
		t.Fatalf("alias resolved to %s via %s", res.Resolved.ID, res.MatchedVia)
	}

	// Case-insensitive alias.
	res = s.Resolve(Mention{Text: "coach dana"})
	if len(res.Ambiguous) != 0 || res.Resolved == nil {
		t.Fatalf("alias resolve (case): %+v", res)
	}
	if res.Resolved.ID != "coach-dana-whitfield" || res.MatchedVia != TierAlias {
		t.Fatalf("alias resolved to %s via %s", res.Resolved.ID, res.MatchedVia)
	}
}

func TestResolveSameNameAmbiguity(t *testing.T) {
	s := newTestStore(t)
	seedStore(t, s)
	ctx := context.Background()
	// Verify the afcon Rivers so BOTH same-name entities are fresh at
	// testNow: the tie-break (fresh beats stale) cannot break a two-way
	// freshness tie, so the ambiguity must surface.
	if err := s.Verify(ctx, testNow, "player-jordan-rivers-afcon", "verifier"); err != nil {
		t.Fatalf("verify afcon: %v", err)
	}
	s.WithNow(testNow)

	// "Jordan Rivers" is the canonical name of TWO different players, both
	// fresh. The ambiguity must surface; nothing is merged or guessed.
	res := s.Resolve(Mention{Text: "Jordan Rivers"})
	if res.Resolved != nil {
		t.Fatalf("same-name resolved to %s; ambiguity must be surfaced", res.Resolved.ID)
	}
	if len(res.Ambiguous) != 2 {
		t.Fatalf("ambiguous candidates = %d, want 2: %+v", len(res.Ambiguous), res.Ambiguous)
	}
	// Both candidates carried, deterministically ordered by ID.
	if res.Ambiguous[0].Entity.ID != "player-jordan-rivers-afcon" || res.Ambiguous[1].Entity.ID != "player-jordan-rivers-nfl" {
		t.Fatalf("candidate order: %s, %s", res.Ambiguous[0].Entity.ID, res.Ambiguous[1].Entity.ID)
	}
	for _, c := range res.Ambiguous {
		if c.MatchedVia != TierCanonicalName {
			t.Fatalf("candidate %s matched via %s, want canonical_name", c.Entity.ID, c.MatchedVia)
		}
	}

	// The two entities must remain separate in the store.
	if s.IDCount() != 5 {
		t.Fatalf("entities merged or lost: %d", s.IDCount())
	}
}

func TestResolveSameNameStaleAwareTieBreak(t *testing.T) {
	s := newTestStore(t)
	seedStore(t, s)
	s.WithNow(testNow) // reference time: canonical-name window is 30 days

	// At testNow the AFCON Rivers (verified 40 days ago) is past the
	// canonical-name window; the NFL Rivers (verified at testNow) is fresh.
	// Fresh memory beats stale memory: exactly one resolves.
	res := s.Resolve(Mention{Text: "Jordan Rivers"})
	if len(res.Ambiguous) != 0 || res.Resolved == nil {
		t.Fatalf("expected fresh tie-break: %+v", res)
	}
	if res.Resolved.ID != "player-jordan-rivers-nfl" {
		t.Fatalf("tie-break resolved to %s, want the fresh entity", res.Resolved.ID)
	}
	if res.Stale {
		t.Fatal("fresh winner reported stale")
	}

	// If both were verified at the same time, no tie-break is possible.
	bothFresh := newTestStore(t)
	seedStore(t, bothFresh)
	ctx := context.Background()
	if err := bothFresh.Verify(ctx, testNow, "player-jordan-rivers-afcon", "verifier"); err != nil {
		t.Fatalf("verify: %v", err)
	}
	bothFresh.WithNow(testNow)
	res = bothFresh.Resolve(Mention{Text: "Jordan Rivers"})
	if res.Resolved != nil || len(res.Ambiguous) != 2 {
		t.Fatalf("two-fresh same-name: resolved=%v ambiguous=%d, want 2-way ambiguity", res.Resolved, len(res.Ambiguous))
	}
}

func TestResolveTypeHint(t *testing.T) {
	s := newTestStore(t)
	seedStore(t, s)

	// A team mention that only hits a player-typed entity: the single match
	// with a mismatching type is a red flag, not a resolution.
	res := s.Resolve(Mention{Text: "Sharks", TypeHint: TypePlayer})
	if res.Resolved != nil {
		t.Fatalf("type mismatch resolved: %+v", res)
	}
	if len(res.Ambiguous) != 1 || res.Ambiguous[0].Entity.ID != "team-example-city-sharks" {
		t.Fatalf("type mismatch should surface the candidate: %+v", res.Ambiguous)
	}

	// Matching type hint resolves.
	res = s.Resolve(Mention{Text: "Sharks", TypeHint: TypeTeam})
	if len(res.Ambiguous) != 0 || res.Resolved == nil || res.Resolved.ID != "team-example-city-sharks" {
		t.Fatalf("matching hint resolve: %+v", res)
	}
}

func TestResolveStaleFlag(t *testing.T) {
	s := newTestStore(t)
	seedStore(t, s)

	// Never set a reference time: a verified entity is never "stale".
	res := s.Resolve(Mention{Text: "Priya Anand"})
	if res.Resolved == nil {
		t.Fatalf("no resolve: %+v", res)
	}
	if res.Stale {
		t.Fatal("verified entity reported stale without a reference time")
	}

	// With a reference time far in the future, even a verified entity is
	// stale (30-day canonical-name window exceeded) — but it still resolves
	// (memory is context, not evidence; staleness is reported, not hidden).
	s.WithNow(testNow.Add(40 * 24 * time.Hour))
	res = s.Resolve(Mention{Text: "Priya Anand"})
	if res.Resolved == nil {
		t.Fatalf("stale entity should still resolve: %+v", res)
	}
	if !res.Stale {
		t.Fatal("stale entity not flagged")
	}

	// A long-stale entity (verified 40 days ago) is reported stale when a
	// reference time is set. With no reference time (zero), a *never*
	// verified entity is always stale; a verified one is not judged.
	s.WithNow(testNow)
	res = s.Resolve(Mention{Text: "player-jordan-rivers-afcon"})
	if res.Resolved == nil || !res.Stale {
		t.Fatalf("stale resolve: %+v (stale=%v)", res, res.Stale)
	}

	// A never-verified entity is always stale, even without a reference time.
	mustCreateAllowingNameDups(t, s, &Entity{
		ID:            "player-never-verified",
		Type:          TypePlayer,
		CanonicalName: "Never Verified",
		Confidence:    memory.ConfidenceMedium,
		CreatedAt:     testNow,
		UpdatedAt:     testNow,
		// LastVerifiedAt left zero: never verified.
	})
	res = s.Resolve(Mention{Text: "Never Verified"})
	if res.Resolved == nil || !res.Stale {
		t.Fatalf("never-verified resolve: %+v (stale=%v)", res, res.Stale)
	}
}

func TestResolveMany(t *testing.T) {
	s := newTestStore(t)
	seedStore(t, s)
	ctx := context.Background()
	// Verify the afcon Rivers so both same-name entities are fresh: the
	// tie-break cannot break a two-way freshness tie, ambiguity stands.
	if err := s.Verify(ctx, testNow, "player-jordan-rivers-afcon", "verifier"); err != nil {
		t.Fatalf("verify afcon: %v", err)
	}
	s.WithNow(testNow)

	got := s.ResolveMany([]Mention{
		{Text: "Jordan Rivers"},
		{Text: "Sharks"},
		{Text: "Unknown Person"},
		{Text: "coach-dana-whitfield"},
	})
	if len(got) != 4 {
		t.Fatalf("got %d resolutions", len(got))
	}
	if len(got[0].Ambiguous) != 2 {
		t.Fatalf("batch[0] ambiguous=%d, want 2", len(got[0].Ambiguous))
	}
	if got[1].Resolved == nil || got[1].Resolved.ID != "team-example-city-sharks" {
		t.Fatalf("batch[1]: %+v", got[1])
	}
	if !got[2].Unresolved {
		t.Fatalf("batch[2]: %+v", got[2])
	}
	if got[3].Resolved == nil || got[3].MatchedVia != TierStableID {
		t.Fatalf("batch[3]: %+v", got[3])
	}
}
