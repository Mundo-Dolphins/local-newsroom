package contracts

import (
	"strings"
	"testing"
	"time"
)

func testPlan() *ContentPlan {
	return &ContentPlan{
		StableID:            "plan-001",
		RecipeID:            "afc-east-weekly",
		RecipeHash:          strings.Repeat("a", 64),
		Format:              "thread",
		GeneratedAt:         time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC),
		InputVerificationID: "verify-001",
		Units: []PlanUnit{
			{ID: "unit-1", Role: "intro", ClaimIDs: []string{"claim-001"}, Required: true},
			{ID: "unit-2", Role: "entity", Entity: &PlanEntity{Kind: "team", Name: "Bills", Fields: map[string]string{"record": "4-2"}}, ClaimIDs: []string{"claim-002"}},
			{ID: "unit-3", Role: "summary", ClaimIDs: []string{"claim-001", "claim-002"}},
		},
	}
}

func testVerification() *VerificationResult {
	return &VerificationResult{
		StableID:       "verify-001",
		InputDossierID: "dossier-001",
		VerifiedAt:     time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC),
		VerificationStatuses: map[string]VerificationStatus{
			"claim-001": VerificationStatusSupported,
			"claim-002": VerificationStatusSupported,
			"claim-003": VerificationStatusContradicted,
		},
		ClaimDetails: map[string]ClaimVerificationDetails{
			"claim-001": {ClaimID: "claim-001", Statement: "a", VerificationStatus: VerificationStatusSupported, ConfidenceLevel: ConfidenceHigh},
			"claim-002": {ClaimID: "claim-002", Statement: "b", VerificationStatus: VerificationStatusSupported, ConfidenceLevel: ConfidenceHigh},
			"claim-003": {ClaimID: "claim-003", Statement: "c", VerificationStatus: VerificationStatusContradicted, ConfidenceLevel: ConfidenceMedium},
		},
	}
}

func TestContentPlanValidate(t *testing.T) {
	if err := testPlan().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestContentPlanValidateFailures(t *testing.T) {
	if err := (*ContentPlan)(nil).Validate(); err == nil {
		t.Error("nil plan must fail")
	}
	p := testPlan()
	p.StableID = ""
	if err := p.Validate(); err == nil {
		t.Error("missing stable id must fail")
	}
	p = testPlan()
	p.RecipeID = ""
	if err := p.Validate(); err == nil {
		t.Error("missing recipe id must fail")
	}
	p = testPlan()
	p.Units = nil
	if err := p.Validate(); err == nil {
		t.Error("empty units must fail")
	}
	p = testPlan()
	p.Units[0].ID = p.Units[1].ID // duplicate
	if err := p.Validate(); err == nil {
		t.Error("duplicate unit ids must fail")
	}
	p = testPlan()
	p.Units[0].ClaimIDs = []string{"bogus"}
	if err := p.Validate(); err == nil {
		t.Error("malformed claim id must fail")
	}
	p = testPlan()
	p.Units[1].Entity.Name = ""
	if err := p.Validate(); err == nil {
		t.Error("nameless entity must fail")
	}
}

func TestContentPlanCheckClaims(t *testing.T) {
	vres := testVerification()
	p := testPlan()
	// All cited claims are supported in the fixture.
	unsupported, err := p.CheckClaims(vres)
	if err != nil {
		t.Fatal(err)
	}
	if len(unsupported) != 0 {
		t.Fatalf("unsupported = %d", len(unsupported))
	}
	if p.HasUnsupported() {
		t.Error("plan should be fully supported")
	}
}

func TestContentPlanCheckClaimsUnsupported(t *testing.T) {
	vres := testVerification()
	p := testPlan()
	p.Units[2].ClaimIDs = []string{"claim-003"} // contradicted
	unsupported, err := p.CheckClaims(vres)
	if err != nil {
		t.Fatal(err)
	}
	if len(unsupported) != 1 || unsupported[0].ID != "unit-3" {
		t.Fatalf("unsupported = %+v", unsupported)
	}
	if !p.HasUnsupported() {
		t.Error("plan should report unsupported")
	}
}

func TestContentPlanCheckClaimsUnknownClaim(t *testing.T) {
	vres := testVerification()
	p := testPlan()
	p.Units[0].ClaimIDs = []string{"claim-999"}
	if _, err := p.CheckClaims(vres); err == nil {
		t.Fatal("citing a claim absent from the verification must fail")
	}
}

func TestContentPlanCheckClaimsEntityWithoutEvidence(t *testing.T) {
	vres := testVerification()
	p := testPlan()
	p.Units[1].ClaimIDs = nil // entity with no supporting claims = invented
	unsupported, err := p.CheckClaims(vres)
	if err != nil {
		t.Fatal(err)
	}
	if len(unsupported) != 1 || unsupported[0].ID != "unit-2" {
		t.Fatalf("entity unit without claims must be unsupported, got %+v", unsupported)
	}
}

func TestContentPlanCheckClaimsStaticWithoutEvidence(t *testing.T) {
	vres := testVerification()
	p := testPlan()
	p.Units[0].ClaimIDs = nil // static unit without claims is structural
	unsupported, err := p.CheckClaims(vres)
	if err != nil {
		t.Fatal(err)
	}
	if len(unsupported) != 0 {
		t.Fatalf("static unit without claims must stay supported, got %+v", unsupported)
	}
}

func TestContentPlanSummaryLine(t *testing.T) {
	line := testPlan().SummaryLine()
	if line != "3 units (1 per-entity) for recipe afc-east-weekly" {
		t.Fatalf("line = %q", line)
	}
	p := testPlan()
	p.Units[1].Entity = nil
	if got := p.SummaryLine(); got != "3 units for recipe afc-east-weekly" {
		t.Fatalf("line = %q", got)
	}
}

func TestPlanUnitFieldsString(t *testing.T) {
	u := PlanUnit{Fields: map[string]string{"b": "2", "a": "1"}}
	if got := u.FieldsString(); got != "a: 1\nb: 2" {
		t.Fatalf("fields = %q", got)
	}
	var empty PlanUnit
	if empty.FieldsString() != "" {
		t.Error("empty fields must render empty")
	}
}
