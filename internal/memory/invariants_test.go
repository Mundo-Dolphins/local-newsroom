package memory

import (
	"testing"
	"time"
)

// TestMemoryIsNotEvidence encodes the subsystem's core principle: a factual
// memory record is contextual knowledge, and consuming it as verified
// evidence is a contract-level error.
func TestMemoryIsNotEvidence(t *testing.T) {
	if ErrMemoryNotEvidence == nil {
		t.Fatal("ErrMemoryNotEvidence must be defined")
	}
	err := ErrMemoryNotEvidence
	if err.Error() == "" {
		t.Error("ErrMemoryNotEvidence must carry a message")
	}
}

// TestFactualMemoryNotDossierEvidence checks the separation from the
// research/verification pipeline: a factual memory's provenance can be
// complete and fresh, yet the record is still not a VerificationStatus and
// cannot stand in for dossier evidence.
func TestFactualMemoryNotDossierEvidence(t *testing.T) {
	m := factualRecord()
	if err := m.Validate(); err != nil {
		t.Fatalf("precondition: %v", err)
	}
	if len(m.Provenance.Sources) == 0 {
		t.Fatal("precondition: factual memory must carry provenance sources")
	}
	if !m.Freshness.LastVerifiedAt.Equal(testNow) {
		t.Fatal("precondition: freshness stamp present")
	}

	// Even fully provenanced, the record must never be cast to a verified
	// claim. The subsystem surfaces the refusal explicitly.
	if err := AsVerifiedEvidence(m); err == nil {
		t.Fatal("a memory record must not be convertible to verified evidence")
	} else if err != ErrMemoryNotEvidence {
		t.Errorf("got %v, want ErrMemoryNotEvidence", err)
	}
}

func TestVolatilityExamplesFromTicket(t *testing.T) {
	// The ticket's worked examples: draft year static, college low,
	// current team high, injury status ephemeral, author preference static.
	examples := []struct {
		field string
		vol   Volatility
	}{
		{"draft_year", VolatileStatic},
		{"college", VolatileLow},
		{"current_team", VolatileHigh},
		{"injury_status", VolatileEphemeral},
		{"author_style_preference", VolatileStatic},
	}
	for _, ex := range examples {
		m := NewMemoryRecord(testNow)
		m.StableID = MemoryStableID("mem-vol-" + ex.field)
		m.Kind = KindFactual
		m.Scope = ScopeEntity
		m.Subject = EntityRef{Kind: "person", Name: "Jordan Rivers"}
		m.Field = ex.field
		m.Confidence = ConfidenceMedium
		m.Volatility = ex.vol
		m.Freshness = Freshness{LastVerifiedAt: testNow, StaleAfter: time.Hour}
		m.Provenance = Provenance{
			Sources: []MemorySource{{SourceType: "web", SourceID: "https://example.com", RetrievedAt: testNow}},
		}
		if err := m.EncodeValue(FactualValue{Statement: ex.field + " fact"}, testNow); err != nil {
			t.Fatalf("%s: %v", ex.field, err)
		}
		if err := m.Validate(); err != nil {
			t.Errorf("%s with volatility %s must validate: %v", ex.field, ex.vol, err)
		}
	}
}

// TestSupersededPreferenceStillValid checks that "persistent until
// superseded" is representable: a recurring style preference stays active
// and usable, and remains valid (but unusable) once superseded.
func TestSupersededPreferenceStillValid(t *testing.T) {
	pref := entityFeedbackRecord()
	if !pref.IsUsable(testNow) {
		t.Fatal("precondition: recurring preference must be usable")
	}
	if pref.Volatility != VolatileStatic {
		t.Fatalf("recurring preference volatility = %q, want static", pref.Volatility)
	}

	pref.Expiration = Expiration{
		InvalidationStatus: InvalidationSuperseded,
		SupersededBy:       "mem-fb-2",
		InvalidationReason: "editor relaxed the headline length rule",
	}
	if err := pref.Validate(); err != nil {
		t.Errorf("superseded preference must still validate: %v", err)
	}
	if pref.IsUsable(testNow) {
		t.Error("superseded preference must not be usable")
	}
}
