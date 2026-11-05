package patterns

import (
	"context"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/memory"
	"github.com/Mundo-Dolphins/local-newsroom/internal/memorystore"
)

var testNow = time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

func newTestStore(t *testing.T) memorystore.Store {
	t.Helper()
	s := memorystore.NewFakeStore()
	return s
}

// buildFeedbackRecord creates a properly-initialized editorial feedback record.
func buildFeedbackRecord(t *testing.T, stableID string, feedback string, author string, subjectKind string, subjectName string) *memory.MemoryRecord {
	t.Helper()
	rec := memory.NewMemoryRecord(testNow)
	rec.StableID = memory.MemoryStableID("mem-" + stableID)
	rec.Kind = memory.KindEditorialFeedback
	rec.Scope = memory.ScopeNewsroom
	rec.Subject = memory.EntityRef{Kind: subjectKind, Name: subjectName}
	rec.Field = "hunk-0"
	rec.Confidence = memory.ConfidenceMedium
	rec.Volatility = memory.VolatileLow
	rec.Freshness = memory.Freshness{LastVerifiedAt: testNow}
	rec.Provenance = memory.Provenance{
		Sources: []memory.MemorySource{{
			SourceType:  "run",
			SourceID:    "test-run",
			RetrievedAt: testNow,
		}},
	}

	fb := memory.FeedbackRecord{
		Feedback:  feedback,
		GivenBy:   author,
		GivenAt:   testNow,
		Recurring: false,
	}

	if err := rec.EncodeValue(fb, testNow); err != nil {
		t.Fatalf("encode feedback: %v", err)
	}

	return rec
}

// TestDetectPatternsNoOccurrences: empty store returns no patterns.
func TestDetectPatternsNoOccurrences(t *testing.T) {
	store := newTestStore(t)
	p := NewStore(store, func() time.Time { return testNow })

	config := Config{
		MinOccurrences: 2,
		GroupBy:        GroupByAuthor,
	}

	patterns, err := p.DetectPatterns(context.Background(), config)
	if err != nil {
		t.Fatalf("DetectPatterns failed: %v", err)
	}

	if len(patterns) != 0 {
		t.Fatalf("expected no patterns, got %d", len(patterns))
	}
}

// TestDetectPatternsNotEnoughOccurrences: below threshold returns no patterns.
func TestDetectPatternsNotEnoughOccurrences(t *testing.T) {
	store := newTestStore(t)
	p := NewStore(store, func() time.Time { return testNow })

	// Create a feedback record
	rec := buildFeedbackRecord(t, "fb-1", "corrected factual error", "editor-1", "recipe", "test-recipe")

	if err := store.Write(context.Background(), rec); err != nil {
		t.Fatalf("write record: %v", err)
	}

	config := Config{
		MinOccurrences: 2,
		GroupBy:        GroupByAuthor,
	}

	patterns, err := p.DetectPatterns(context.Background(), config)
	if err != nil {
		t.Fatalf("DetectPatterns failed: %v", err)
	}

	if len(patterns) != 0 {
		t.Fatalf("expected no patterns (below threshold), got %d", len(patterns))
	}
}

// TestDetectPatternsSinglePattern: one pattern above threshold.
func TestDetectPatternsSinglePattern(t *testing.T) {
	store := newTestStore(t)
	p := NewStore(store, func() time.Time { return testNow })

	fbText := "factual correction: changed year from 2025 to 2026"

	// Create multiple feedback records with same content
	for i := 0; i < 3; i++ {
		rec := buildFeedbackRecord(t, "fb-"+string(rune('1'+i)), fbText, "editor-1", "recipe", "test-recipe")

		if err := store.Write(context.Background(), rec); err != nil {
			t.Fatalf("write record %d: %v", i, err)
		}
	}

	config := Config{
		MinOccurrences: 2,
		GroupBy:        GroupByAuthor,
	}

	patterns, err := p.DetectPatterns(context.Background(), config)
	if err != nil {
		t.Fatalf("DetectPatterns failed: %v", err)
	}

	if len(patterns) != 1 {
		t.Fatalf("expected 1 pattern, got %d", len(patterns))
	}

	// Verify pattern content
	pat := patterns[0]
	if pat.Kind != memory.KindEditorialPattern {
		t.Fatalf("expected KindEditorialPattern, got %s", pat.Kind)
	}

	decoded, err := pat.DecodeValue()
	if err != nil {
		t.Fatalf("decode pattern: %v", err)
	}

	if pr, ok := decoded.(memory.PatternRecord); ok {
		if len(pr.Occurrences) != 3 {
			t.Fatalf("expected 3 occurrences, got %d", len(pr.Occurrences))
		}
		if pr.GroupKey != "editor-1" {
			t.Fatalf("expected group key 'editor-1', got %s", pr.GroupKey)
		}
	} else {
		t.Fatalf("expected PatternRecord, got %T", decoded)
	}
}

// TestDetectPatternsMultipleGroups: multiple groups produce multiple patterns.
func TestDetectPatternsMultipleGroups(t *testing.T) {
	store := newTestStore(t)
	p := NewStore(store, func() time.Time { return testNow })

	// Create feedback records for two different authors
	for author := 1; author <= 2; author++ {
		fb := "factual correction by author " + string(rune('1'+author))

		for i := 0; i < 2; i++ {
			rec := buildFeedbackRecord(t,
				"fb-"+string(rune('a'+author-1))+string(rune('1'+i)),
				fb,
				"editor-"+string(rune('1'+author)),
				"recipe", "test-recipe")

			if err := store.Write(context.Background(), rec); err != nil {
				t.Fatalf("write record author %d: %v", author, err)
			}
		}
	}

	config := Config{
		MinOccurrences: 2,
		GroupBy:        GroupByAuthor,
	}

	patterns, err := p.DetectPatterns(context.Background(), config)
	if err != nil {
		t.Fatalf("DetectPatterns failed: %v", err)
	}

	if len(patterns) != 2 {
		t.Fatalf("expected 2 patterns (one per author), got %d", len(patterns))
	}
}

// TestDetectPatternsByCategory: filter by category.
func TestDetectPatternsByCategory(t *testing.T) {
	store := newTestStore(t)
	p := NewStore(store, func() time.Time { return testNow })

	fb1 := "factual_correction: year changed"
	fb2 := "tone_style: make it more concise"

	rec1 := buildFeedbackRecord(t, "fb-1", fb1, "editor-1", "recipe", "test-recipe")
	if err := store.Write(context.Background(), rec1); err != nil {
		t.Fatalf("write record 1: %v", err)
	}

	rec2 := buildFeedbackRecord(t, "fb-2", fb2, "editor-1", "recipe", "test-recipe")
	if err := store.Write(context.Background(), rec2); err != nil {
		t.Fatalf("write record 2: %v", err)
	}

	// Filter by factual_correction category only
	config := Config{
		MinOccurrences: 2,
		GroupBy:        GroupByAuthor,
		Category:       "factual_correction",
	}

	patterns, err := p.DetectPatterns(context.Background(), config)
	if err != nil {
		t.Fatalf("DetectPatterns failed: %v", err)
	}

	if len(patterns) != 0 {
		t.Fatalf("expected 0 patterns (factual_correction only, only 1 occurrence), got %d", len(patterns))
	}
}

// TestDetectPatternsTimeWindow: respect time window.
func TestDetectPatternsTimeWindow(t *testing.T) {
	store := newTestStore(t)
	p := NewStore(store, func() time.Time { return testNow })

	// Create feedback records with old timestamp
	sampleTime := testNow.Add(-24 * time.Hour)
	fb := "old factual correction"

	for i := 0; i < 3; i++ {
		rec := buildFeedbackRecord(t, "old-fb-"+string(rune('1'+i)), fb, "editor-1", "recipe", "test-recipe")
		rec.CreatedAt = sampleTime
		rec.UpdatedAt = sampleTime
		rec.Freshness = memory.Freshness{LastVerifiedAt: sampleTime}

		if err := store.Write(context.Background(), rec); err != nil {
			t.Fatalf("write record %d: %v", i, err)
		}
	}

	// Set time window to 1 hour (should exclude old records)
	config := Config{
		MinOccurrences: 2,
		GroupBy:        GroupByAuthor,
		TimeWindow:     1 * time.Hour,
	}

	patterns, err := p.DetectPatterns(context.Background(), config)
	if err != nil {
		t.Fatalf("DetectPatterns failed: %v", err)
	}

	if len(patterns) != 0 {
		t.Fatalf("expected no patterns (outside time window), got %d", len(patterns))
	}
}

// TestDetectPatternsByRecipe: group by recipe.
func TestDetectPatternsByRecipe(t *testing.T) {
	store := newTestStore(t)
	p := NewStore(store, func() time.Time { return testNow })

	// Create feedback records for different recipes
	for recipeID := 1; recipeID <= 2; recipeID++ {
		fb := "factual correction for recipe " + string(rune('1'+recipeID))

		for i := 0; i < 2; i++ {
			rec := buildFeedbackRecord(t,
				"fb-recipe"+string(rune('1'+recipeID))+string(rune('1'+i)),
				fb,
				"editor-1",
				"recipe", "recipe-"+string(rune('1'+recipeID)))

			if err := store.Write(context.Background(), rec); err != nil {
				t.Fatalf("write record recipe %d: %v", recipeID, err)
			}
		}
	}

	config := Config{
		MinOccurrences: 2,
		GroupBy:        GroupByRecipe,
	}

	patterns, err := p.DetectPatterns(context.Background(), config)
	if err != nil {
		t.Fatalf("DetectPatterns failed: %v", err)
	}

	if len(patterns) != 2 {
		t.Fatalf("expected 2 patterns (one per recipe), got %d", len(patterns))
	}
}

// TestDetectPatternsIdempotent: running detection twice produces same results.
func TestDetectPatternsIdempotent(t *testing.T) {
	store := newTestStore(t)
	p := NewStore(store, func() time.Time { return testNow })

	fb := "factual correction"

	for i := 0; i < 3; i++ {
		rec := buildFeedbackRecord(t, "fb-"+string(rune('1'+i)), fb, "editor-1", "recipe", "test-recipe")

		if err := store.Write(context.Background(), rec); err != nil {
			t.Fatalf("write record %d: %v", i, err)
		}
	}

	config := Config{
		MinOccurrences: 2,
		GroupBy:        GroupByAuthor,
	}

	// First run
	patterns1, err := p.DetectPatterns(context.Background(), config)
	if err != nil {
		t.Fatalf("first DetectPatterns failed: %v", err)
	}

	// Second run
	patterns2, err := p.DetectPatterns(context.Background(), config)
	if err != nil {
		t.Fatalf("second DetectPatterns failed: %v", err)
	}

	if len(patterns1) != len(patterns2) {
		t.Fatalf("pattern count differs: %d vs %d", len(patterns1), len(patterns2))
	}

	// Check that the pattern IDs are the same
	if patterns1[0].StableID != patterns2[0].StableID {
		t.Fatalf("pattern IDs differ: %s vs %s", patterns1[0].StableID, patterns2[0].StableID)
	}
}
