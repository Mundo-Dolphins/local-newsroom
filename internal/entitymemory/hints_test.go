package entitymemory

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/memory"
)

// seedHintStore builds a Store over a fake backing store and seeds it with
// the given entities (created at testNow for deterministic freshness).
func seedHintStore(t *testing.T, entities ...*Entity) *Store {
	t.Helper()
	s := newTestStore(t)
	for _, e := range entities {
		if err := s.Create(context.Background(), testNow, e); err != nil {
			t.Fatalf("Create(%s): %v", e.ID, err)
		}
	}
	return s
}

// seedHintStoreAt builds a Store seeded with entities created at the given
// reference time, and sets the store's Now to the same time so freshness
// judgments are consistent.
func seedHintStoreAt(t *testing.T, ref time.Time, entities ...*Entity) *Store {
	t.Helper()
	s := newTestStore(t)
	for _, e := range entities {
		if err := s.Create(context.Background(), ref, e); err != nil {
			t.Fatalf("Create(%s): %v", e.ID, err)
		}
	}
	return s.WithNow(ref)
}


// TestBuildPromptBlockNoEntities verifies the block is empty when no entity
// matches the mentions.
func TestBuildPromptBlockNoEntities(t *testing.T) {
	s := seedHintStore(t, &Entity{
		ID:            "player-kane",
		CanonicalName: "Harry Kane",
		Type:          TypePlayer,
		Confidence:    memory.ConfidenceMedium,
	})
	block := s.BuildPromptBlock([]Mention{{Text: "someone else"}}, time.Now().UTC(), DefaultBound)
	if len(block.Entities) != 0 {
		t.Errorf("expected no entities, got %d", len(block.Entities))
	}
}

// TestBuildPromptBlockFraming verifies the rendered block carries the
// mandatory "context, never evidence" framing.
func TestRenderPlannerHintsFraming(t *testing.T) {
	now := time.Now().UTC()
	s := seedHintStore(t, &Entity{
		ID:            "player-kane",
		CanonicalName: "Harry Kane",
		Type:          TypePlayer,
		Confidence:    memory.ConfidenceMedium,
		Aliases:       []string{"Kane"},
	})
	out := s.RenderPlannerHints([]Mention{{Text: "Kane"}}, now, DefaultBound)
	if !strings.Contains(out, "HINTS FROM EDITORIAL MEMORY") {
		t.Errorf("missing framing header:\n%s", out)
	}
	if !strings.Contains(out, "CONTEXT, NEVER EVIDENCE") {
		t.Errorf("missing context-never-evidence rule:\n%s", out)
	}
	if !strings.Contains(out, "Harry Kane") {
		t.Errorf("missing entity name:\n%s", out)
	}
}

// TestRenderPlannerHintsNoValues verifies the planner block never shows
// attribute values (the strongest no-values-by-default form).
func TestRenderPlannerHintsNoValues(t *testing.T) {
	ref := time.Now().UTC()
	old := ref.AddDate(0, -3, 0)
	s := seedHintStoreAt(t, ref, &Entity{
		ID:            "team-bayern",
		CanonicalName: "Bayern Munich",
		Type:          TypeTeam,
		Confidence:    memory.ConfidenceMedium,
		Attributes: []Attribute{
			{Key: "manager", Value: "Xabi Alonso", Volatility: memory.VolatileHigh, StaleAfter: 24*time.Hour, LastVerifiedAt: old},
		},
	})
	// Use the same reference time the entity was created with so staleness
	// is judged consistently (the store's zero-time WithNow disables it).
	out := s.WithNow(ref).RenderPlannerHints([]Mention{{Text: "Bayern Munich"}}, ref, DefaultBound)
	if strings.Contains(out, "Xabi Alonso") {
		t.Errorf("planner block leaked a stale attribute value:\n%s", out)
	}
	if !strings.Contains(out, "manager") {
		t.Errorf("planner block missing the stale attribute key:\n%s", out)
	}
}

// TestRenderResearcherHintsStaleValues verifies the researcher block shows
// stale values, each labeled for re-verification.
func TestRenderResearcherHintsStaleValues(t *testing.T) {
	ref := time.Now().UTC()
	old := ref.AddDate(0, -3, 0)
	s := seedHintStoreAt(t, ref, &Entity{
		ID:            "team-bayern",
		CanonicalName: "Bayern Munich",
		Type:          TypeTeam,
		Confidence:    memory.ConfidenceMedium,
		Attributes: []Attribute{
			{Key: "manager", Value: "Xabi Alonso", Volatility: memory.VolatileHigh, StaleAfter: 24*time.Hour, LastVerifiedAt: old},
		},
	})
	out := s.WithNow(ref).RenderResearcherHints([]Mention{{Text: "Bayern Munich"}}, ref, DefaultBound)
	if !strings.Contains(out, "Xabi Alonso") {
		t.Errorf("researcher block missing the stale value:\n%s", out)
	}
	if !strings.Contains(out, "CONTEXT, NEVER EVIDENCE") {
		t.Errorf("researcher block missing framing:\n%s", out)
	}
}

// TestBuildPromptBlockBound verifies the bound caps the entity count.
func TestBuildPromptBlockBound(t *testing.T) {
	now := time.Now().UTC()
	es := make([]*Entity, 12)
	mentions := make([]Mention, 12)
	for i := 0; i < 12; i++ {
		name := "Person " + string(rune('a'+i))
		es[i] = &Entity{ID: "p" + string(rune('a'+i)), CanonicalName: name, Type: TypePerson, Confidence: memory.ConfidenceMedium}
		mentions[i] = Mention{Text: name}
	}
	s := seedHintStore(t, es...)
	block := s.BuildPromptBlock(mentions, now, 3)
	if len(block.Entities) != 3 {
		t.Errorf("bound 3 not enforced, got %d entities", len(block.Entities))
	}
	// A bound above DefaultBound is clamped to DefaultBound.
	block = s.BuildPromptBlock(mentions, now, 99)
	if len(block.Entities) != DefaultBound {
		t.Errorf("bound above DefaultBound not clamped, got %d (want %d)", len(block.Entities), DefaultBound)
	}
}

// TestBuildPromptBlockAliasLimit verifies aliases are deduped and bounded.
func TestBuildPromptBlockAliasLimit(t *testing.T) {
	s := seedHintStore(t, &Entity{
		ID:            "player-x",
		CanonicalName: "Some Player",
		Type:          TypePlayer,
		Confidence:    memory.ConfidenceMedium,
		Aliases:       []string{"a1", "a2", "a3", "a4", "a5", "a6"},
	})
	block := s.BuildPromptBlock([]Mention{{Text: "Some Player"}}, time.Time{}, 0)
	if len(block.Entities) != 1 {
		t.Fatalf("expected 1 entity, got %d", len(block.Entities))
	}
	if got := len(block.Entities[0].Aliases); got > AliasLimit {
		t.Errorf("alias count %d exceeds AliasLimit %d", got, AliasLimit)
	}
}

// TestResolveMentionsDedup verifies ResolveMentions returns distinct
// entities and their IDs, with unresolved mentions as nil.
func TestResolveMentionsDedup(t *testing.T) {
	s := seedHintStore(t, &Entity{
		ID:            "player-kane",
		CanonicalName: "Harry Kane",
		Aliases:       []string{"Kane"},
		Confidence:    memory.ConfidenceMedium,
		Type:          TypePlayer,
	})
	resolved, ids := s.ResolveMentions([]Mention{
		{Text: "Harry Kane"}, // canonical name
		{Text: "Kane"},       // alias -> same entity (dedup)
		{Text: "unknown"},    // unresolved -> nil
	})
	if len(resolved) != 3 {
		t.Fatalf("expected 3 resolutions, got %d", len(resolved))
	}
	if resolved[0] == nil || resolved[0].ID != "player-kane" {
		t.Errorf("first mention did not resolve")
	}
	if resolved[1] == nil || resolved[1].ID != "player-kane" {
		t.Errorf("alias mention did not resolve")
	}
	if resolved[2] != nil {
		t.Errorf("unknown mention should be nil, got %v", resolved[2])
	}
	// IDs must be deduped to a single entry.
	if len(ids) != 1 || ids[0] != "player-kane" {
		t.Errorf("expected [player-kane], got %v", ids)
	}
}


// ProjectTopicMentions derives the mention set for a topic: the topic itself
// plus the longest words (>= 3 chars), capped at 8 total. It is the public
// form of the orchestrator's private helper, exposed here for testing.
func ProjectTopicMentions(topic string) []Mention {
	mentions := []Mention{{Text: strings.TrimSpace(topic)}}
	fields := strings.Fields(topic)
	sort.SliceStable(fields, func(i, j int) bool { return len(fields[i]) > len(fields[j]) })
	seen := map[string]bool{strings.ToLower(strings.TrimSpace(topic)): true}
	for _, f := range fields {
		if len(f) < 3 {
			continue
		}
		key := strings.ToLower(f)
		if seen[key] {
			continue
		}
		seen[key] = true
		mentions = append(mentions, Mention{Text: f})
		if len(mentions) >= 8 {
			break
		}
	}
	return mentions
}

// TestProjectTopicMentions verifies the topic mention projection is
// deterministic: the topic phrase first, then the longest words.
func TestProjectTopicMentions(t *testing.T) {
	m := ProjectTopicMentions("Kane to Bayern Munich")
	if m[0].Text != "Kane to Bayern Munich" {
		t.Errorf("first mention must be the topic, got %q", m[0].Text)
	}
	if m[1].Text != "Bayern" && m[1].Text != "Munich" {
		t.Errorf("longest word expected second, got %q", m[1].Text)
	}
	for _, m2 := range m {
		if m2.Text == "to" {
			t.Errorf("short word 'to' should be excluded, got %v", m)
		}
	}
}
