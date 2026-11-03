package entitymemory

import (
	"context"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/memory"
	"github.com/Mundo-Dolphins/local-newsroom/internal/memorystore"
)

// testNow is the reference "current time" for the whole test package.
var testNow = time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

// newTestStore returns a Store over an in-memory v0.7 store.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	fake := memorystore.NewFakeStore()
	s := NewStore(fake)
	if err := s.LoadError(); err != nil {
		t.Fatalf("store load: %v", err)
	}
	return s
}

// webSource returns a valid web MemorySource for tests.
func webSource(id string, at time.Time) memory.MemorySource {
	return memory.MemorySource{
		SourceType:  "web",
		SourceID:    id,
		RetrievedAt: at,
	}
}

// mustCreate adds an entity via the public Create (all safety rules apply).
func mustCreate(t *testing.T, s *Store, e *Entity) {
	t.Helper()
	if err := s.Create(context.Background(), testNow, e); err != nil {
		t.Fatalf("create %s: %v", e.ID, err)
	}
}

// mustCreateAllowingNameDups adds an entity while skipping the cross-entity
// name-uniqueness check (everything else — invariants, ID uniqueness,
// persistence — still applies).
//
// Real stores can contain two entities with the same name: external writers
// or legacy imports are not bound by the same rules as Create. The
// same-name ambiguity fixtures rely on this to exercise the ambiguity and
// tie-break paths.
func mustCreateAllowingNameDups(t *testing.T, s *Store, e *Entity) {
	t.Helper()
	if err := s.createAllowingNameDups(context.Background(), testNow, e); err != nil {
		t.Fatalf("create %s: %v", e.ID, err)
	}
}

// seedStore builds the standard fixture set: two players who share a name
// (the classic same-name ambiguity) plus a coach, a team, and a reporter.
func seedStore(t *testing.T, s *Store) {
	t.Helper()

	mustCreateAllowingNameDups(t, s, &Entity{
		ID:            "player-jordan-rivers-nfl",
		Type:          TypePlayer,
		CanonicalName: "Jordan Rivers",
		Aliases:       []string{"JR"},
		Attributes: []Attribute{{
			Key:            "team",
			Value:          "Example City Sharks",
			Volatility:     memory.VolatileMedium,
			StaleAfter:     7 * 24 * time.Hour,
			LastVerifiedAt: testNow,
			Sources:        []memory.MemorySource{webSource("https://example.com/rivers-nfl", testNow)},
		}},
		Confidence:     memory.ConfidenceHigh,
		LastVerifiedAt: testNow,
	})

	mustCreateAllowingNameDups(t, s, &Entity{
		ID:            "player-jordan-rivers-afcon",
		Type:          TypePlayer,
		CanonicalName: "Jordan Rivers",
		Aliases:       []string{"JR-9"},
		Attributes: []Attribute{{
			Key:            "team",
			Value:          "Riverside FC",
			Volatility:     memory.VolatileMedium,
			StaleAfter:     7 * 24 * time.Hour,
			LastVerifiedAt: testNow.Add(-40 * 24 * time.Hour), // stale
			Sources:        []memory.MemorySource{webSource("https://example.com/rivers-afcon", testNow.Add(-40*24*time.Hour))},
		}},
		Confidence: memory.ConfidenceMedium,
		// Identity never verified: the classic stale/ambiguous fixture.
		// Attributes carry their own freshness; the identity stamp is what
		// the tier-level staleness window judges.
		// LastVerifiedAt deliberately left zero.
	})

	mustCreate(t, s, &Entity{
		ID:            "coach-dana-whitfield",
		Type:          TypeCoach,
		CanonicalName: "Dana Whitfield",
		Aliases:       []string{"Coach Dana", "D. Whitfield"},
		Attributes: []Attribute{{
			Key:            "position",
			Value:          "head coach",
			Volatility:     memory.VolatileLow,
			LastVerifiedAt: testNow,
			Sources:        []memory.MemorySource{webSource("https://example.com/whitfield", testNow)},
		}},
		Confidence:     memory.ConfidenceHigh,
		LastVerifiedAt: testNow,
	})

	mustCreate(t, s, &Entity{
		ID:            "team-example-city-sharks",
		Type:          TypeTeam,
		CanonicalName: "Example City Sharks",
		Aliases:       []string{"Sharks"},
		Attributes: []Attribute{{
			Key:            "league",
			Value:          "Example Football League",
			Volatility:     memory.VolatileStatic,
			LastVerifiedAt: testNow,
			Sources:        []memory.MemorySource{webSource("https://example.com/sharks", testNow)},
		}},
		Confidence:     memory.ConfidenceHigh,
		LastVerifiedAt: testNow,
	})

	mustCreate(t, s, &Entity{
		ID:             "reporter-priya-anand",
		Type:           TypeReporter,
		CanonicalName:  "Priya Anand",
		Confidence:     memory.ConfidenceMedium,
		LastVerifiedAt: testNow,
	})
}
