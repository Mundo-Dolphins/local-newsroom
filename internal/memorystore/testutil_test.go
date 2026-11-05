package memorystore

import (
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/memory"
)

var testNow = time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

// buildRecord returns a valid record of the given kind, with a caller-chosen
// or derived stable ID.
func buildRecord(t *testing.T, kind memory.MemoryKind, id memory.MemoryStableID) *memory.MemoryRecord {
	t.Helper()
	rec := memory.NewMemoryRecord(testNow)
	rec.Kind = kind
	rec.Scope = memory.ScopeNewsroom
	rec.Confidence = memory.ConfidenceMedium
	rec.Volatility = memory.VolatileLow
	switch kind {
	case memory.KindFactual:
		rec.Subject = memory.EntityRef{Kind: "person", Name: "Jordan Rivers"}
		rec.Field = "current_team"
		rec.Freshness = memory.Freshness{LastVerifiedAt: testNow, StaleAfter: 72 * time.Hour}
		rec.Provenance = memory.Provenance{
			Sources: []memory.MemorySource{{
				SourceType:  "web",
				SourceID:    "https://example.com/rivers",
				RetrievedAt: testNow,
			}},
		}
		err := rec.EncodeValue(memory.FactualValue{
			Statement: "Jordan Rivers plays for the Example City Sharks.",
			Data:      map[string]string{"team": "Example City Sharks"},
		}, testNow)
		if err != nil {
			t.Fatalf("encode factual: %v", err)
		}
	case memory.KindEntity:
		rec.Subject = memory.EntityRef{Kind: "team", Name: "Example City Sharks", CanonicalID: "team-sharks"}
		err := rec.EncodeValue(memory.EntityValue{
			Fields: map[string]string{"founded": "1990"},
		}, testNow)
		if err != nil {
			t.Fatalf("encode entity: %v", err)
		}
	case memory.KindEditorialFeedback:
		rec.Scope = memory.ScopeProfile
		err := rec.EncodeValue(memory.FeedbackRecord{
			Feedback:  "Keep the ledes short.",
			GivenBy:   "editor",
			GivenAt:   testNow,
			Recurring: true,
		}, testNow)
		if err != nil {
			t.Fatalf("encode feedback: %v", err)
		}
	case memory.KindCorrection:
		err := rec.EncodeValue(memory.CorrectionRecord{
			OriginalClaim:  "Rivers is with the Sharks.",
			CorrectedClaim: "Rivers is with the Bay Orcas.",
			Reason:         "roster change",
			CorrectedBy:    "editor",
			CorrectedAt:    testNow,
		}, testNow)
		if err != nil {
			t.Fatalf("encode correction: %v", err)
		}
	case memory.KindEditorialPattern:
		err := rec.EncodeValue(memory.PatternRecord{
			Category:       "factual_correction",
			GroupKey:       "editor-1",
			MinOccurrences: 2,
			Occurrences: []memory.PatternOccurrence{{
				RecordID:    "mem-fb-1",
				Feedback:    "factual correction",
				Author:      "editor-1",
				GivenAt:     testNow,
				SubjectKind: "recipe",
				SubjectName: "test-recipe",
				Category:    "factual_correction",
				Field:       "hunk-0",
			}},
			SampleFeedback: "factual correction",
			Notes:          "Test pattern",
		}, testNow)
		if err != nil {
			t.Fatalf("encode pattern: %v", err)
		}
	case memory.KindSuggestion:
		rec.Scope = memory.ScopeRecipe
		err := rec.EncodeValue(memory.SuggestionRecord{
			SuggestionKind: "recipe",
			Proposal:       "Add a preflight check for feed availability.",
			Status:         memory.SuggestionProposed,
		}, testNow)
		if err != nil {
			t.Fatalf("encode suggestion: %v", err)
		}
	case memory.KindRecipeExecution:
		rec.Scope = memory.ScopeRecipe
		err := rec.EncodeValue(memory.RecipeExecutionRecord{
			RecipeID:   "afc-east-weekly",
			Outcome:    memory.RecipeOutcomeSuccess,
			ExecutedAt: testNow,
			Lessons:    []string{"feed fetches are slow; raise the timeout"},
		}, testNow)
		if err != nil {
			t.Fatalf("encode recipe execution: %v", err)
		}
	default:
		t.Fatalf("unhandled kind %q", kind)
	}
	if id == "" {
		id = DeriveStableID(rec)
	}
	rec.StableID = id
	if err := rec.Validate(); err != nil {
		t.Fatalf("test record invalid: %v", err)
	}
	return rec
}

var allKinds = memory.AllKinds

func TestBuildRecordValid(t *testing.T) {
	for _, k := range allKinds {
		rec := buildRecord(t, k, "")
		if rec.StableID == "" {
			t.Fatalf("kind %s: derived ID is empty", k)
		}
	}
}
