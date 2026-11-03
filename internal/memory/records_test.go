package memory

import (
	"testing"
	"time"
)

func entityRecord() *MemoryRecord {
	m := NewMemoryRecord(testNow)
	m.StableID = "mem-ent-1"
	m.Kind = KindEntity
	m.Scope = ScopeEntity
	m.Subject = EntityRef{Kind: "person", Name: "Jordan Rivers", Aliases: []string{"JR"}}
	m.Confidence = ConfidenceHigh
	m.Volatility = VolatileLow
	if err := m.EncodeValue(EntityValue{
		Fields: map[string]string{
			"draft_year": "2023",
			"college":    "Example State University",
		},
		Relationships: []EntityRelationship{{Type: "plays_for", Target: "Example City Sharks"}},
	}, testNow); err != nil {
		panic(err)
	}
	return m
}

func entityFeedbackRecord() *MemoryRecord {
	m := NewMemoryRecord(testNow)
	m.StableID = "mem-fb-1"
	m.Kind = KindEditorialFeedback
	m.Scope = ScopeNewsroom
	m.Confidence = ConfidenceHigh
	m.Volatility = VolatileStatic
	if err := m.EncodeValue(FeedbackRecord{
		Feedback:  "Keep headlines under ten words; no clickbait.",
		GivenBy:   "editor-1",
		GivenAt:   testNow,
		Recurring: true,
		Preference: map[string]string{
			"headline_max_words": "10",
			"clickbait":          "avoid",
		},
	}, testNow); err != nil {
		panic(err)
	}
	return m
}

func correctionRecord() *MemoryRecord {
	m := NewMemoryRecord(testNow)
	m.StableID = "mem-corr-1"
	m.Kind = KindCorrection
	m.Scope = ScopePublication
	m.Subject = EntityRef{Kind: "person", Name: "Jordan Rivers"}
	m.Confidence = ConfidenceHigh
	m.Volatility = VolatileStatic
	m.Provenance = Provenance{
		UserCorrected: true,
		CorrectedBy:   "editor-1",
		CorrectedAt:   testNow,
	}
	cr := CorrectionRecord{
		OriginalClaim:     "Jordan Rivers was drafted 14th overall.",
		CorrectedClaim:    "Jordan Rivers was drafted 4th overall.",
		Reason:            "Correct draft position per official announcement.",
		CorrectedBy:       "editor-1",
		CorrectedAt:       testNow,
		AffectedRunID:     "run-2026-07-21-001",
		AffectedMemoryIDs: []string{"mem-fact-1"},
	}
	if err := m.EncodeValue(cr, testNow); err != nil {
		panic(err)
	}
	return m
}

func suggestionRecord() *MemoryRecord {
	m := NewMemoryRecord(testNow)
	m.StableID = "mem-sugg-1"
	m.Kind = KindSuggestion
	m.Scope = ScopeProfile
	m.Confidence = ConfidenceMedium
	m.Volatility = VolatileStatic
	sugg := SuggestionRecord{
		SuggestionKind: "profile",
		TargetID:       "profile-casino",
		Proposal:       "Adopt the editor's short-headline preference as a style rule.",
		ProposedChange: map[string]string{"headline_max_words": "10"},
		Status:         SuggestionProposed,
	}
	if err := m.EncodeValue(sugg, testNow); err != nil {
		panic(err)
	}
	return m
}

func recipeRecord() *MemoryRecord {
	m := NewMemoryRecord(testNow)
	m.StableID = "mem-recipe-1"
	m.Kind = KindRecipeExecution
	m.Scope = ScopeRecipe
	m.Confidence = ConfidenceMedium
	m.Volatility = VolatileLow
	exec := RecipeExecutionRecord{
		RecipeID:   "recipe-daily-brief",
		RunID:      "run-2026-07-21-001",
		Outcome:    RecipeOutcomePartial,
		ExecutedAt: testNow,
		Lessons:    []string{"The verification stage timed out on large dossiers; chunk before verifying."},
		Failures:   []RecipeFailure{{Stage: "verify", Message: "context window exceeded", Recovered: true}},
	}
	if err := m.EncodeValue(exec, testNow); err != nil {
		panic(err)
	}
	return m
}

func TestKindValuePayloadsRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		rec  *MemoryRecord
		want any
	}{
		{"factual", factualRecord(), FactualValue{}},
		{"entity", entityRecord(), EntityValue{}},
		{"editorial feedback", entityFeedbackRecord(), FeedbackRecord{}},
		{"correction", correctionRecord(), CorrectionRecord{}},
		{"suggestion", suggestionRecord(), SuggestionRecord{}},
		{"recipe execution", recipeRecord(), RecipeExecutionRecord{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.rec.DecodeValue()
			if err != nil {
				t.Fatalf("DecodeValue: %v", err)
			}
			if gotType := typeOf(got); gotType != typeOf(tc.want) {
				t.Errorf("decoded %s, want %s", gotType, typeOf(tc.want))
			}
			if err := tc.rec.Validate(); err != nil {
				t.Errorf("record failed validation: %v", err)
			}
		})
	}
}

func typeOf(v any) string {
	t := 0
	switch v.(type) {
	case FactualValue:
		t = 1
	case EntityValue:
		t = 2
	case FeedbackRecord:
		t = 3
	case CorrectionRecord:
		t = 4
	case SuggestionRecord:
		t = 5
	case RecipeExecutionRecord:
		t = 6
	}
	return string(rune('a' + t))
}

func TestPayloadValidate(t *testing.T) {
	if err := (FactualValue{Data: map[string]string{"x": "y"}}).Validate(); err == nil {
		t.Error("factual value without statement must fail")
	}
	if err := (FactualValue{Statement: "ok"}).Validate(); err != nil {
		t.Errorf("valid factual value failed: %v", err)
	}

	if err := (EntityValue{Relationships: []EntityRelationship{{Type: "plays_for"}}}).Validate(); err == nil {
		t.Error("relationship without target must fail")
	}
	if err := (EntityValue{Relationships: []EntityRelationship{{Target: "x"}}}).Validate(); err == nil {
		t.Error("relationship without type must fail")
	}
	if err := (EntityValue{}).Validate(); err != nil {
		t.Errorf("empty entity value must pass: %v", err)
	}

	fb := FeedbackRecord{Recurring: true, Preference: map[string]string{"tone": "concise"}}
	if err := fb.Validate(); err == nil {
		t.Error("empty feedback must fail")
	}
	fb = FeedbackRecord{GivenBy: "e", GivenAt: testNow}
	if err := fb.Validate(); err == nil {
		t.Error("feedback without text must fail")
	}
	fb = FeedbackRecord{Feedback: "x", GivenAt: testNow}
	if err := fb.Validate(); err == nil {
		t.Error("feedback without given_by must fail")
	}
	fb = FeedbackRecord{Feedback: "x", GivenBy: "e"}
	if err := fb.Validate(); err == nil {
		t.Error("feedback without given_at must fail")
	}

	corr := CorrectionRecord{}
	if err := corr.Validate(); err == nil {
		t.Error("empty correction must fail")
	}
	corr = CorrectionRecord{OriginalClaim: "a", CorrectedClaim: "b", CorrectedAt: testNow}
	if err := corr.Validate(); err == nil {
		t.Error("correction without corrected_by must fail")
	}
	corr = CorrectionRecord{OriginalClaim: "a", CorrectedClaim: "b", CorrectedBy: "e"}
	if err := corr.Validate(); err == nil {
		t.Error("correction without corrected_at must fail")
	}
	corr = CorrectionRecord{CorrectedClaim: "b", CorrectedBy: "e", CorrectedAt: testNow}
	if err := corr.Validate(); err == nil {
		t.Error("correction without original claim must fail")
	}
	corr = CorrectionRecord{OriginalClaim: "a", CorrectedBy: "e", CorrectedAt: testNow}
	if err := corr.Validate(); err == nil {
		t.Error("correction without corrected claim must fail")
	}
	corr = CorrectionRecord{OriginalClaim: "a", CorrectedClaim: "b", CorrectedBy: "e", CorrectedAt: testNow}
	if err := corr.Validate(); err != nil {
		t.Errorf("valid correction failed: %v", err)
	}

	sugg := SuggestionRecord{SuggestionKind: "profile", Proposal: "x", Status: SuggestionApproved}
	if err := sugg.Validate(); err == nil {
		t.Error("approved suggestion without reviewed_by must fail")
	}
	sugg.ReviewedBy = "editor-1"
	if err := sugg.Validate(); err == nil {
		t.Error("approved suggestion without reviewed_at must fail")
	}
	sugg.ReviewedAt = testNow
	if err := sugg.Validate(); err != nil {
		t.Errorf("fully reviewed suggestion must pass: %v", err)
	}
	sugg = SuggestionRecord{Status: SuggestionProposed}
	if err := sugg.Validate(); err == nil {
		t.Error("suggestion without kind must fail")
	}
	sugg = SuggestionRecord{SuggestionKind: "profile", Status: SuggestionProposed}
	if err := sugg.Validate(); err == nil {
		t.Error("suggestion without proposal must fail")
	}
	sugg = SuggestionRecord{SuggestionKind: "profile", Proposal: "x", Status: "maybe"}
	if err := sugg.Validate(); err == nil {
		t.Error("suggestion with unknown status must fail")
	}

	exec := RecipeExecutionRecord{RecipeID: "r"}
	if err := exec.Validate(); err == nil {
		t.Error("execution without outcome must fail")
	}
	exec.Outcome = RecipeOutcomeFailure
	if err := exec.Validate(); err == nil {
		t.Error("execution without executed_at must fail")
	}
	exec.ExecutedAt = testNow
	if err := exec.Validate(); err != nil {
		t.Errorf("valid execution must pass: %v", err)
	}
	noID := RecipeExecutionRecord{Outcome: RecipeOutcomeSuccess, ExecutedAt: testNow}
	if err := noID.Validate(); err == nil {
		t.Error("execution without recipe_id must fail")
	}
	badFail := RecipeExecutionRecord{RecipeID: "r", Outcome: RecipeOutcomeFailure, ExecutedAt: testNow, Failures: []RecipeFailure{{Message: "no stage"}}}
	if err := badFail.Validate(); err == nil {
		t.Error("failure without stage must fail")
	}
	badOutcome := RecipeExecutionRecord{RecipeID: "r", Outcome: "exploded", ExecutedAt: testNow}
	if err := badOutcome.Validate(); err == nil {
		t.Error("execution with unknown outcome must fail")
	}
}

func TestCorrectionInvalidatesFactualMemory(t *testing.T) {
	fact := factualRecord()
	if !fact.IsUsable(testNow) {
		t.Fatal("precondition: factual record must be usable")
	}

	corr := correctionRecord()
	got, err := corr.DecodeValue()
	if err != nil {
		t.Fatal(err)
	}
	cr, ok := got.(CorrectionRecord)
	if !ok {
		t.Fatalf("decoded %T", got)
	}

	// Simulate the persistence-layer lifecycle: a correction to a claim
	// supersedes the factual memories it lists; the record is retained for
	// audit but no longer usable.
	for _, id := range cr.AffectedMemoryIDs {
		if id != string(fact.StableID) {
			continue
		}
		fact.Expiration = Expiration{
			InvalidationStatus: InvalidationSuperseded,
			SupersededBy:       "mem-fact-2",
			InvalidationReason: "superseded by correction mem-corr-1",
		}
	}

	if fact.IsUsable(testNow) {
		t.Error("superseded factual record must no longer be usable")
	}
	if err := fact.Validate(); err != nil {
		t.Errorf("superseded record must still validate: %v", err)
	}

	// The replacement carries the user correction in provenance.
	replacement := factualRecord()
	replacement.StableID = "mem-fact-2"
	replacement.UpdatedAt = testNow.Add(time.Hour)
	replacement.Freshness.LastVerifiedAt = testNow.Add(time.Hour)
	replacement.Provenance = Provenance{
		UserCorrected: true,
		CorrectedBy:   "editor-1",
		CorrectedAt:   testNow,
		Sources:       []MemorySource{{SourceType: "user", SourceID: "editor-1", RetrievedAt: testNow}},
	}
	if err := replacement.Validate(); err != nil {
		t.Errorf("user-corrected replacement must validate: %v", err)
	}
	if !replacement.Provenance.UserCorrected || replacement.Provenance.CorrectedBy != "editor-1" {
		t.Error("replacement must record the user correction")
	}
}

func TestFactualValueStatement(t *testing.T) {
	m := factualRecord()
	fv, err := m.DecodeValue()
	if err != nil {
		t.Fatal(err)
	}
	fv2, ok := fv.(FactualValue)
	if !ok {
		t.Fatalf("decoded %T", fv)
	}
	if fv2.Statement == "" {
		t.Error("statement must survive")
	}
}

func TestEncodeValueNilValue(t *testing.T) {
	m := NewMemoryRecord(testNow)
	m.StableID = "mem-enc"
	m.Kind = KindFactual
	if err := m.EncodeValue(FactualValue{}, testNow); err != nil {
		t.Errorf("encoding a zero payload must pass: %v", err)
	}
}
