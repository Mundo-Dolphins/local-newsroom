package entitymemory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/memory"
)

func TestUpdateAttributesVerifiedBeatsStaleMemory(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.Create(ctx, testNow, &Entity{
		ID: "player-u", Type: TypePlayer, CanonicalName: "U",
		Attributes: []Attribute{{
			Key: "team", Value: "Old Team",
			Volatility:     memory.VolatileMedium,
			StaleAfter:     7 * 24 * time.Hour,
			LastVerifiedAt: testNow.Add(-30 * 24 * time.Hour), // stale
			Sources:        []memory.MemorySource{webSource("https://example.com/old", testNow.Add(-30*24*time.Hour))},
		}},
		Confidence:     memory.ConfidenceHigh,
		LastVerifiedAt: testNow.Add(-30 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Even an unverified source claim replaces stale memory.
	err := s.UpdateAttributes(ctx, testNow, "player-u", SourceClaim{
		Key:    "team",
		Value:  "New Team",
		Source: webSource("https://example.com/new", testNow),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	e, _ := s.Get("player-u")
	got := foundAttribute(e, "team")
	if got == nil || got.Value != "New Team" {
		t.Fatalf("stale memory not replaced: %+v", got)
	}
	if got.LastVerifiedAt != testNow || len(got.Sources) != 1 || got.Sources[0].SourceID != "https://example.com/new" {
		t.Fatalf("provenance/freshness not updated: %+v", got)
	}
}

func TestUpdateAttributesVerifiedBeatsFreshMemory(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.Create(ctx, testNow, &Entity{
		ID: "player-v", Type: TypePlayer, CanonicalName: "V",
		Attributes: []Attribute{{
			Key: "team", Value: "Current Team",
			Volatility: memory.VolatileMedium, StaleAfter: 7 * 24 * time.Hour,
			LastVerifiedAt: testNow, // fresh
			Sources:        []memory.MemorySource{webSource("https://example.com/current", testNow)},
		}},
		Confidence:     memory.ConfidenceHigh,
		LastVerifiedAt: testNow,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// A verified claim, even older than the stored verification, is current
	// source data: it wins.
	err := s.UpdateAttributes(ctx, testNow, "player-v", SourceClaim{
		Key:      "team",
		Value:    "Transferred Team",
		Source:   webSource("https://example.com/transfer", testNow.Add(-1*time.Hour)),
		Verified: true,
	})
	if err != nil {
		t.Fatalf("verified update: %v", err)
	}
	e, _ := s.Get("player-v")
	got := foundAttribute(e, "team")
	if got == nil || got.Value != "Transferred Team" {
		t.Fatalf("verified source data lost: %+v", got)
	}
}

func TestUpdateAttributesConflictNeverOverwrites(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.Create(ctx, testNow, &Entity{
		ID: "player-w", Type: TypePlayer, CanonicalName: "W",
		Attributes: []Attribute{{
			Key: "team", Value: "Verified Team",
			Volatility: memory.VolatileMedium, StaleAfter: 7 * 24 * time.Hour,
			LastVerifiedAt: testNow, // fresh
			Sources:        []memory.MemorySource{webSource("https://example.com/verified", testNow)},
		}},
		Confidence:     memory.ConfidenceHigh,
		LastVerifiedAt: testNow,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// An unverified claim disagreeing with fresh, verified memory: conflict.
	// Memory is never overwritten silently.
	err := s.UpdateAttributes(ctx, testNow, "player-w", SourceClaim{
		Key:      "team",
		Value:    "Rumored Team",
		Source:   webSource("https://example.com/rumor", testNow),
		Verified: false,
	})
	if !IsConflict(err) {
		t.Fatalf("want conflict, got %v", err)
	}
	var c *ErrConflict
	if !errors.As(err, &c) {
		t.Fatalf("error not *ErrConflict: %T %v", err, err)
	}
	if c.Key != "team" || c.Existing != "Verified Team" || c.New != "Rumored Team" {
		t.Fatalf("conflict details: %+v", c)
	}
	if !strings.Contains(err.Error(), "Verified Team") {
		t.Fatalf("error message must surface the existing value: %v", err)
	}

	// The stored value is untouched.
	e, _ := s.Get("player-w")
	got := foundAttribute(e, "team")
	if got == nil || got.Value != "Verified Team" {
		t.Fatalf("fresh memory was overwritten: %+v", got)
	}

	// A conflicting batch is all-or-nothing: no claim from it is applied.
	if err := s.Create(ctx, testNow, &Entity{
		ID: "player-x", Type: TypePlayer, CanonicalName: "X",
		Attributes: []Attribute{{
			Key: "age", Value: "25",
			Volatility:     memory.VolatileLow,
			StaleAfter:     30 * 24 * time.Hour,
			LastVerifiedAt: testNow,
			Sources:        []memory.MemorySource{webSource("https://example.com/x", testNow)},
		}},
		Confidence:     memory.ConfidenceHigh,
		LastVerifiedAt: testNow,
	}); err != nil {
		t.Fatalf("create x: %v", err)
	}
	err = s.UpdateAttributes(ctx, testNow, "player-x",
		SourceClaim{Key: "age", Value: "26", Source: webSource("https://example.com/x2", testNow)},
		SourceClaim{Key: "college", Value: "State U", Source: webSource("https://example.com/x3", testNow)},
	)
	if !IsConflict(err) {
		t.Fatalf("want conflict: %v", err)
	}
	e, _ = s.Get("player-x")
	if got := foundAttribute(e, "college"); got != nil {
		t.Fatalf("non-conflicting claim applied despite batch conflict: %+v", got)
	}
	if got := foundAttribute(e, "age"); got == nil || got.Value != "25" {
		t.Fatalf("attribute state after rejected batch: %+v", got)
	}
}

func TestUpdateAttributesAddsNewAttribute(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.Create(ctx, testNow, &Entity{
		ID: "player-n", Type: TypePlayer, CanonicalName: "N",
		Attributes: []Attribute{{
			Key: "position", Value: "WR",
			Volatility:     memory.VolatileStatic,
			LastVerifiedAt: testNow,
			Sources:        []memory.MemorySource{webSource("https://example.com/n", testNow)},
		}},
		Confidence:     memory.ConfidenceHigh,
		LastVerifiedAt: testNow,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	err := s.UpdateAttributes(ctx, testNow, "player-n", SourceClaim{
		Key:        "college",
		Value:      "State U",
		Source:     webSource("https://example.com/college", testNow),
		Volatility: memory.VolatileStatic,
	})
	if err != nil {
		t.Fatalf("new attribute: %v", err)
	}
	e, _ := s.Get("player-n")
	if len(e.Attributes) != 2 {
		t.Fatalf("attributes: %d, want 2", len(e.Attributes))
	}
	got := foundAttribute(e, "college")
	if got == nil || got.Value != "State U" || got.Volatility != memory.VolatileStatic || got.LastVerifiedAt != testNow {
		t.Fatalf("new attribute: %+v", got)
	}
	// Key lookup is case-insensitive.
	if foundAttribute(e, "COLLEGE") != foundAttribute(e, "college") {
		t.Fatal("case-insensitive key lookup failed")
	}
}

func TestUpdateAttributesUnknownEntity(t *testing.T) {
	s := newTestStore(t)
	err := s.UpdateAttributes(context.Background(), testNow, "nope",
		SourceClaim{Key: "a", Value: "b", Source: webSource("x", testNow)})
	if !errors.Is(err, ErrEntityNotFound) {
		t.Fatalf("want ErrEntityNotFound, got %v", err)
	}
}

func TestUpdateAttributesMalformedClaim(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.Create(ctx, testNow, &Entity{
		ID: "player-m", Type: TypePlayer, CanonicalName: "M",
		Confidence:     memory.ConfidenceHigh,
		LastVerifiedAt: testNow,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Empty key / empty value / bad source are all reported.
	err := s.UpdateAttributes(ctx, testNow, "player-m",
		SourceClaim{Value: "v", Source: webSource("x", testNow)},
		SourceClaim{Key: "k", Source: webSource("x", testNow)},
		SourceClaim{Key: "k", Value: "v", Source: memory.MemorySource{SourceType: "web"}},
	)
	if err == nil {
		t.Fatal("malformed claims accepted")
	}
	e, _ := s.Get("player-m")
	if len(e.Attributes) != 0 {
		t.Fatalf("malformed claims persisted: %+v", e.Attributes)
	}
}

func TestAddAlias(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedStore(t, s)

	// New alias works and is immediately resolvable.
	if err := s.AddAlias(ctx, testNow, "team-example-city-sharks", "EC Sharks"); err != nil {
		t.Fatalf("add alias: %v", err)
	}
	res := s.Resolve(Mention{Text: "EC Sharks"})
	if res.Resolved == nil || res.Resolved.ID != "team-example-city-sharks" || res.MatchedVia != TierAlias {
		t.Fatalf("alias not resolvable: %+v", res)
	}

	// Idempotent re-add.
	if err := s.AddAlias(ctx, testNow, "team-example-city-sharks", "ec sharks"); err != nil {
		t.Fatalf("idempotent add: %v", err)
	}

	// The canonical name cannot be "added" as an alias.
	if err := s.AddAlias(ctx, testNow, "team-example-city-sharks", "Example City Sharks"); err == nil {
		t.Fatal("adding canonical name as alias succeeded")
	}

	// Stealing a name from another entity is refused; the other entity keeps
	// it and the two stay separate.
	err := s.AddAlias(ctx, testNow, "player-jordan-rivers-nfl", "Sharks")
	if !errors.Is(err, ErrDuplicateName) {
		t.Fatalf("cross-entity alias: %v, want ErrDuplicateName", err)
	}
	if _, err := s.Get("team-example-city-sharks"); err != nil {
		t.Fatalf("other entity damaged: %v", err)
	}
	team := s.Resolve(Mention{Text: "Sharks"})
	if team.Resolved == nil || team.Resolved.ID != "team-example-city-sharks" {
		t.Fatalf("other entity's alias lost: %+v", team)
	}
	nfl := s.Resolve(Mention{Text: "player-jordan-rivers-nfl"})
	if nfl.Resolved == nil || len(nfl.Resolved.Aliases) != 1 {
		t.Fatalf("alias was added anyway: %+v", nfl.Resolved.Aliases)
	}
}

func TestRemoveAlias(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedStore(t, s)

	if err := s.RemoveAlias(ctx, testNow, "coach-dana-whitfield", "Coach Dana"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if res := s.Resolve(Mention{Text: "Coach Dana"}); !res.Unresolved {
		t.Fatalf("removed alias still resolves: %+v", res)
	}
	if err := s.RemoveAlias(ctx, testNow, "coach-dana-whitfield", "Coach Dana"); err == nil {
		t.Fatal("removing a non-alias succeeded")
	}
	// The entity is intact otherwise (one alias remains after removal).
	e, err := s.Get("coach-dana-whitfield")
	if err != nil || len(e.Aliases) != 1 || e.Aliases[0] != "D. Whitfield" {
		t.Fatalf("entity after remove: %v, aliases=%v", e, e.Aliases)
	}
}

func TestVerify(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedStore(t, s)

	// The AFCON Rivers has a stale identity; verify it.
	if err := s.Verify(ctx, testNow, "player-jordan-rivers-afcon", "verifier-run-7",
		webSource("https://example.com/verify", testNow)); err != nil {
		t.Fatalf("verify: %v", err)
	}
	e, _ := s.Get("player-jordan-rivers-afcon")
	if e.LastVerifiedAt != testNow || e.VerifiedBy != "verifier-run-7" {
		t.Fatalf("verify stamps: %v / %s", e.LastVerifiedAt, e.VerifiedBy)
	}

	// Now BOTH same-name players are fresh at testNow: the ambiguity must
	// stand (fresh memory does not silently win a two-way tie).
	s.WithNow(testNow)
	res := s.Resolve(Mention{Text: "Jordan Rivers"})
	if res.Resolved != nil || len(res.Ambiguous) != 2 {
		t.Fatalf("post-verify resolve: resolved=%v ambiguous=%d, want two-way ambiguity", res.Resolved, len(res.Ambiguous))
	}

	if err := s.Verify(ctx, testNow, "nope", "v"); !errors.Is(err, ErrEntityNotFound) {
		t.Fatalf("verify unknown: %v", err)
	}
}
