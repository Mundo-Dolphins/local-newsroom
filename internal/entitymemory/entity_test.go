package entitymemory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/memory"
	"github.com/Mundo-Dolphins/local-newsroom/internal/memorystore"
)

func TestParseEntityType(t *testing.T) {
	cases := []struct {
		in   string
		want EntityType
		err  bool
	}{
		{"player", TypePlayer, false},
		{"  PLAYER ", TypePlayer, false},
		{"Team", TypeTeam, false},
		{"coach", TypeCoach, false},
		{"reporter", TypeReporter, false},
		{"person", TypePerson, false},
		{"organization", TypeOrganization, false},
		{"franchise", "", true},
		{"", "", true},
	}
	for _, c := range cases {
		got, err := ParseEntityType(c.in)
		if c.err {
			if err == nil {
				t.Errorf("ParseEntityType(%q) = %q, want error", c.in, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("ParseEntityType(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

func TestEntityValidate(t *testing.T) {
	valid := func() *Entity {
		return &Entity{
			ID:             "player-x",
			Type:           TypePlayer,
			CanonicalName:  "X",
			Confidence:     memory.ConfidenceHigh,
			CreatedAt:      testNow,
			UpdatedAt:      testNow,
			LastVerifiedAt: testNow,
		}
	}

	if err := (valid()).Validate(); err != nil {
		t.Fatalf("valid entity: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(e *Entity)
	}{
		{"missing id", func(e *Entity) { e.ID = "" }},
		{"missing name", func(e *Entity) { e.CanonicalName = "" }},
		{"bad type", func(e *Entity) { e.Type = "franchise" }},
		{"bad confidence", func(e *Entity) { e.Confidence = "very" }},
		{"duplicate alias", func(e *Entity) { e.Aliases = []string{"x", "X"} }},
		{"empty alias", func(e *Entity) { e.Aliases = []string{"  "} }},
		{"alias equals name", func(e *Entity) { e.Aliases = []string{"X "} }},
		{"duplicate attr key", func(e *Entity) {
			e.Attributes = []Attribute{
				{Key: "team", Value: "a", Volatility: memory.VolatileStatic},
				{Key: "TEAM", Value: "b", Volatility: memory.VolatileStatic},
			}
		}},
		{"attr missing volatility", func(e *Entity) {
			e.Attributes = []Attribute{{Key: "team", Value: "a"}}
		}},
		{"attr missing value", func(e *Entity) {
			e.Attributes = []Attribute{{Key: "team", Volatility: memory.VolatileStatic}}
		}},
		{"stale window without stamp", func(e *Entity) {
			e.Attributes = []Attribute{{Key: "team", Value: "a", Volatility: memory.VolatileStatic, StaleAfter: time.Hour}}
		}},
		{"negative stale window", func(e *Entity) {
			e.Attributes = []Attribute{{Key: "team", Value: "a", Volatility: memory.VolatileStatic, StaleAfter: -time.Hour, LastVerifiedAt: testNow}}
		}},
		{"bad attr source", func(e *Entity) {
			e.Attributes = []Attribute{{Key: "team", Value: "a", Volatility: memory.VolatileStatic,
				Sources: []memory.MemorySource{{SourceType: "web"}}}}
		}},
		{"created after updated", func(e *Entity) { e.CreatedAt = testNow.Add(time.Hour) }},
		{"verified after updated", func(e *Entity) { e.LastVerifiedAt = testNow.Add(time.Hour) }},
		{"nil", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := valid()
			if c.mutate == nil {
				var nilEnt *Entity
				if err := nilEnt.Validate(); err == nil {
					t.Fatal("nil entity validated, want error")
				}
				return
			}
			c.mutate(e)
			if err := e.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestCreateAndGet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	e := &Entity{
		ID:            "player-a",
		Type:          TypePlayer,
		CanonicalName: "A B",
		Attributes: []Attribute{{
			Key:            "position",
			Value:          "WR",
			Volatility:     memory.VolatileStatic,
			LastVerifiedAt: testNow,
			Sources:        []memory.MemorySource{webSource("https://example.com/a", testNow)},
		}},
		Confidence:     memory.ConfidenceHigh,
		LastVerifiedAt: testNow,
	}
	if err := s.Create(ctx, testNow, e); err != nil {
		t.Fatalf("create: %v", err)
	}
	if !s.Has("player-a") || s.IDCount() != 1 {
		t.Fatalf("Has/IDCount = %v/%d, want true/1", s.Has("player-a"), s.IDCount())
	}
	got, err := s.Get("player-a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.CanonicalName != "A B" || len(got.Attributes) != 1 || got.Attributes[0].Value != "WR" {
		t.Fatalf("round-trip: %+v", got)
	}
	if got.CreatedAt != testNow || got.UpdatedAt != testNow {
		t.Fatalf("timestamps: created=%v updated=%v", got.CreatedAt, got.UpdatedAt)
	}

	if _, err := s.Get("nope"); !errors.Is(err, ErrEntityNotFound) {
		t.Fatalf("Get missing = %v, want ErrEntityNotFound", err)
	}

	// Duplicate ID is refused.
	if err := s.Create(ctx, testNow, e); !errors.Is(err, ErrEntityExists) {
		t.Fatalf("duplicate create = %v, want ErrEntityExists", err)
	}

	// Invalid entity is refused and not registered.
	bad := &Entity{Type: TypePlayer} // no id/name
	if err := s.Create(ctx, testNow, bad); err == nil {
		t.Fatal("invalid create succeeded")
	}
	if s.IDCount() != 1 {
		t.Fatalf("invalid create changed count: %d", s.IDCount())
	}
}

func TestCreateRefusesDuplicateNames(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.Create(ctx, testNow, &Entity{
		ID: "player-1", Type: TypePlayer, CanonicalName: "Sam Rivers",
		Confidence: memory.ConfidenceHigh, LastVerifiedAt: testNow,
	}); err != nil {
		t.Fatalf("create 1: %v", err)
	}

	// Same canonical name, different entity: refused, not merged.
	err := s.Create(ctx, testNow, &Entity{
		ID: "player-2", Type: TypePlayer, CanonicalName: "Sam Rivers",
		Confidence: memory.ConfidenceHigh, LastVerifiedAt: testNow,
	})
	if !errors.Is(err, ErrDuplicateName) {
		t.Fatalf("same-name create = %v, want ErrDuplicateName", err)
	}

	// Same name claimed as an alias of a third entity: refused too.
	err = s.Create(ctx, testNow, &Entity{
		ID: "player-3", Type: TypePlayer, CanonicalName: "Different Name",
		Aliases:    []string{"Sam Rivers"},
		Confidence: memory.ConfidenceHigh, LastVerifiedAt: testNow,
	})
	if !errors.Is(err, ErrDuplicateName) {
		t.Fatalf("alias-conflict create = %v, want ErrDuplicateName", err)
	}

	// Distinct names succeed.
	if err := s.Create(ctx, testNow, &Entity{
		ID: "player-4", Type: TypePlayer, CanonicalName: "Sam Rivers Jr.",
		Confidence: memory.ConfidenceHigh, LastVerifiedAt: testNow,
	}); err != nil {
		t.Fatalf("distinct-name create: %v", err)
	}
	// The two "Sam Rivers-ish" entities are kept separate.
	if s.IDCount() != 2 {
		t.Fatalf("count = %d, want 2", s.IDCount())
	}
}

func TestEntityPersistenceRoundTrip(t *testing.T) {
	fake := memorystore.NewFakeStore()
	s := NewStore(fake)
	ctx := context.Background()

	e := &Entity{
		ID:            "player-round",
		Type:          TypePlayer,
		CanonicalName: "Round Player",
		Aliases:       []string{"RP", "R. Player"},
		Attributes: []Attribute{
			{
				Key: "team", Value: "Team A",
				Volatility: memory.VolatileMedium, StaleAfter: 7 * 24 * time.Hour,
				LastVerifiedAt: testNow,
				Sources:        []memory.MemorySource{webSource("https://example.com/rp", testNow)},
			},
			{
				Key: "college", Value: "State U",
				Volatility:     memory.VolatileStatic,
				LastVerifiedAt: testNow,
				Sources:        []memory.MemorySource{webSource("https://example.com/rp-college", testNow)},
			},
		},
		Confidence:     memory.ConfidenceHigh,
		VerifiedBy:     "verifier-run-1",
		LastVerifiedAt: testNow,
	}
	if err := s.Create(ctx, testNow, e); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Open a fresh Store over the same backing store (simulates a new run).
	s2 := NewStore(fake)
	got, err := s2.Get("player-round")
	if err != nil {
		t.Fatalf("reload get: %v", err)
	}
	if got.Type != TypePlayer || got.CanonicalName != "Round Player" {
		t.Fatalf("identity: %+v", got)
	}
	if len(got.Aliases) != 2 || got.Aliases[0] != "RP" {
		t.Fatalf("aliases: %v", got.Aliases)
	}
	if len(got.Attributes) != 2 {
		t.Fatalf("attributes: %d", len(got.Attributes))
	}
	team := foundAttribute(got, "team")
	if team == nil || team.Value != "Team A" || team.Volatility != memory.VolatileMedium || team.StaleAfter != 7*24*time.Hour {
		t.Fatalf("team attr: %+v", team)
	}
	if len(team.Sources) != 1 || team.Sources[0].SourceID != "https://example.com/rp" {
		t.Fatalf("per-field provenance lost: %+v", team.Sources)
	}
	if got.LastVerifiedAt != testNow || got.VerifiedBy != "verifier-run-1" {
		t.Fatalf("fresh stamp: %v / %s", got.LastVerifiedAt, got.VerifiedBy)
	}

	// The record in the backing store is a v0.7 KindEntity record.
	recs, err := fake.Query(ctx, memorystore.Query{Kind: memory.KindEntity})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("backing store holds %d records, want 1", len(recs))
	}
	if recs[0].Scope != memory.ScopeEntity {
		t.Fatalf("scope = %s, want entity", recs[0].Scope)
	}
}

func foundAttribute(e *Entity, key string) *Attribute {
	for i := range e.Attributes {
		if normKey(e.Attributes[i].Key) == normKey(key) {
			return &e.Attributes[i]
		}
	}
	return nil
}
