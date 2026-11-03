package memory

import (
	"testing"
	"time"
)

var testNow = time.Date(2026, 7, 21, 10, 30, 0, 0, time.UTC)

func TestEntityRefValidateBranches(t *testing.T) {
	if err := (EntityRef{}).Validate(); err == nil {
		t.Error("empty entity ref must fail")
	}
	if err := (EntityRef{Kind: "person"}).Validate(); err == nil {
		t.Error("entity ref without name must fail")
	}
	if err := (EntityRef{Kind: "person", Name: "x"}).Validate(); err != nil {
		t.Errorf("minimal entity ref must pass: %v", err)
	}
}

func TestParseMemoryKindUnknown(t *testing.T) {
	for _, k := range AllKinds {
		if _, err := ParseMemoryKind(string(k)); err != nil {
			t.Errorf("ParseMemoryKind(%q) failed: %v", k, err)
		}
	}
	if _, err := ParseMemoryKind("bogus"); err == nil {
		t.Error("ParseMemoryKind(bogus) should fail")
	}
	if _, err := ParseMemoryKind(" ENTITY "); err == nil {
		t.Error("ParseMemoryKind with surrounding spaces should fail (case-sensitive taxonomy)")
	}
}

func TestParseMemoryScope(t *testing.T) {
	for _, s := range AllScopes {
		if _, err := ParseMemoryScope(string(s)); err != nil {
			t.Errorf("ParseMemoryScope(%q) failed: %v", s, err)
		}
	}
	if _, err := ParseMemoryScope("bogus"); err == nil {
		t.Error("ParseMemoryScope(bogus) should fail")
	}
}

func TestParseVolatility(t *testing.T) {
	for _, v := range AllVolatilities {
		if _, err := ParseVolatility(string(v)); err != nil {
			t.Errorf("ParseVolatility(%q) failed: %v", v, err)
		}
	}
	if _, err := ParseVolatility("sometimes"); err == nil {
		t.Error("ParseVolatility(sometimes) should fail")
	}
}

func TestParseConfidenceLevel(t *testing.T) {
	for _, c := range AllConfidenceLevels {
		if _, err := ParseConfidenceLevel(string(c)); err != nil {
			t.Errorf("ParseConfidenceLevel(%q) failed: %v", c, err)
		}
	}
	if _, err := ParseConfidenceLevel("sure"); err == nil {
		t.Error("ParseConfidenceLevel(sure) should fail")
	}
}

func TestParseInvalidationStatus(t *testing.T) {
	for _, s := range AllInvalidationStatuses {
		if _, err := ParseInvalidationStatus(string(s)); err != nil {
			t.Errorf("ParseInvalidationStatus(%q) failed: %v", s, err)
		}
	}
	if _, err := ParseInvalidationStatus("zombie"); err == nil {
		t.Error("ParseInvalidationStatus(zombie) should fail")
	}
}

func TestParseSuggestionStatus(t *testing.T) {
	for _, s := range AllSuggestionStatuses {
		if _, err := ParseSuggestionStatus(string(s)); err != nil {
			t.Errorf("ParseSuggestionStatus(%q) failed: %v", s, err)
		}
	}
	if _, err := ParseSuggestionStatus("maybe"); err == nil {
		t.Error("ParseSuggestionStatus(maybe) should fail")
	}
}

func TestParseRecipeOutcome(t *testing.T) {
	for _, o := range AllRecipeOutcomes {
		if _, err := ParseRecipeOutcome(string(o)); err != nil {
			t.Errorf("ParseRecipeOutcome(%q) failed: %v", o, err)
		}
	}
	if _, err := ParseRecipeOutcome("crashed"); err == nil {
		t.Error("ParseRecipeOutcome(crashed) should fail")
	}
}

func TestVolatilityDurations(t *testing.T) {
	if !VolatileStatic.IsMoreDurableThan(VolatileHigh) {
		t.Error("static must be more durable than high")
	}
	if !VolatileLow.IsMoreDurableThan(VolatileEphemeral) {
		t.Error("low must be more durable than ephemeral")
	}
	if VolatileHigh.IsMoreDurableThan(VolatileStatic) {
		t.Error("high must not be more durable than static")
	}
	if !VolatileMedium.IsMoreDurableThan(VolatileMedium) {
		t.Error("a band is at least as durable as itself")
	}
	if VolatileHigh.IsMoreDurableThan("bogus") {
		t.Error("comparison with an unknown volatility must be false")
	}
}

func TestFreshnessStaleness(t *testing.T) {
	f := Freshness{LastVerifiedAt: testNow, StaleAfter: time.Hour}
	if f.IsStale(testNow) {
		t.Error("just-verified record must not be stale")
	}
	if !f.IsStale(testNow.Add(2 * time.Hour)) {
		t.Error("record past its staleness window must be stale")
	}

	never := Freshness{}
	if !never.IsStale(testNow) {
		t.Error("never-verified record must be stale for verifying consumers")
	}

	noWindow := Freshness{LastVerifiedAt: testNow.Add(-365 * 24 * time.Hour)}
	if noWindow.IsStale(testNow) {
		t.Error("a verified record with no staleness window must not be stale")
	}
}

func TestExpiration(t *testing.T) {
	active := Expiration{InvalidationStatus: InvalidationActive}
	if active.IsExpired(testNow) {
		t.Error("active record with no expiry must not be expired")
	}

	expired := Expiration{InvalidationStatus: InvalidationActive, ExpiresAt: testNow.Add(-time.Hour)}
	if !expired.IsExpired(testNow) {
		t.Error("record past expires_at must be expired")
	}

	future := Expiration{InvalidationStatus: InvalidationActive, ExpiresAt: testNow.Add(time.Hour)}
	if future.IsExpired(testNow) {
		t.Error("record before expires_at must not be expired")
	}

	superseded := Expiration{InvalidationStatus: InvalidationSuperseded, SupersededBy: "mem-2"}
	if !superseded.IsExpired(testNow) {
		t.Error("superseded record must be expired")
	}

	invalidated := Expiration{InvalidationStatus: InvalidationInvalidated}
	if !invalidated.IsExpired(testNow) {
		t.Error("invalidated record must be expired")
	}
}

func TestEntityRef(t *testing.T) {
	ref := EntityRef{Kind: "person", Name: "Javi", CanonicalID: "ent-1", Aliases: []string{"javi"}}
	if err := ref.Validate(); err != nil {
		t.Fatalf("valid entity ref failed: %v", err)
	}
	if err := (EntityRef{Name: "Javi"}).Validate(); err == nil {
		t.Error("entity ref without kind must fail")
	}
	if err := (EntityRef{Kind: "person"}).Validate(); err == nil {
		t.Error("entity ref without name must fail")
	}

	other := EntityRef{Kind: "person", Name: "Javi", CanonicalID: "ent-1"}
	if !ref.Matches(other) {
		t.Error("refs with equal canonical IDs must match")
	}
	named := EntityRef{Kind: "person", Name: "Javi"}
	if named.Matches(other) {
		t.Error("a name-only ref must not match on name")
	}
}

func TestMemorySourceValidate(t *testing.T) {
	valid := MemorySource{SourceType: "web", SourceID: "https://example.com/a", RetrievedAt: testNow}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid source failed: %v", err)
	}
	if err := (MemorySource{SourceID: "x", RetrievedAt: testNow}).Validate(); err == nil {
		t.Error("source without type must fail")
	}
	if err := (MemorySource{SourceType: "web", RetrievedAt: testNow}).Validate(); err == nil {
		t.Error("source without ID must fail")
	}
	if err := (MemorySource{SourceType: "web", SourceID: "x"}).Validate(); err == nil {
		t.Error("source without retrieved_at must fail")
	}
}

func TestProvenanceValidate(t *testing.T) {
	valid := Provenance{Sources: []MemorySource{{SourceType: "web", SourceID: "u", RetrievedAt: testNow}}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid provenance failed: %v", err)
	}

	empty := Provenance{}
	if err := empty.Validate(); err != nil {
		t.Errorf("zero provenance must be valid (kinds without sources are allowed): %v", err)
	}

	corrected := Provenance{UserCorrected: true, CorrectedAt: testNow}
	if err := corrected.Validate(); err == nil {
		t.Error("correction without corrected_by must fail")
	}
	full := Provenance{UserCorrected: true, CorrectedBy: "editor-1", CorrectedAt: testNow}
	if err := full.Validate(); err != nil {
		t.Errorf("fully-populated correction must pass: %v", err)
	}

	badSrc := Provenance{Sources: []MemorySource{{SourceID: "no-type"}}}
	if err := badSrc.Validate(); err == nil {
		t.Error("provenance with an invalid source must fail")
	}

	wrongRetrieved := Provenance{UserCorrected: true, CorrectedBy: "editor-1"}
	if err := wrongRetrieved.Validate(); err == nil {
		t.Error("correction without corrected_at must fail")
	}

	badSource2 := Provenance{Sources: []MemorySource{{SourceType: "web"}, {SourceID: "ok", RetrievedAt: testNow}}}
	if err := badSource2.Validate(); err == nil {
		t.Error("a single bad source among many must fail")
	}
}
