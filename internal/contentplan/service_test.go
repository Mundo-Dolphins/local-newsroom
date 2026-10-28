package contentplan

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/llm"
	"github.com/Mundo-Dolphins/local-newsroom/internal/recipes"
)

// fakeClient is a scripted llm.Client for planner tests.
type fakeClient struct {
	calls   int
	content string
	err     error
	lastReq llm.Request
}

func (f *fakeClient) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	f.calls++
	f.lastReq = req
	if f.err != nil {
		return llm.Response{}, f.err
	}
	return llm.Response{Content: f.content}, nil
}

func fixedNow() Config {
	return Config{Temperature: 0.1, TimeNow: func() time.Time { return time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC) }}
}

func recipeWithBlueprint() *recipes.Resolved {
	r := &recipes.Recipe{
		Schema:       recipes.SchemaVersion,
		ID:           "team-weekly",
		Title:        "Team Weekly",
		OutputFormat: "thread",
		Research: &recipes.ResearchSpec{
			Instructions: "Research the teams.",
			Entity:       &recipes.EntitySpec{Kind: "team", Fields: []string{"record"}, MinCount: 1, MaxCount: 3},
		},
		Blueprint: &recipes.Blueprint{
			Units: []recipes.BlueprintUnit{
				{Role: "intro", Title: "Intro", Required: true},
				{Role: "entity", Repeat: "team", Title: "{entity.name}", Required: true, Fields: []string{"record"}},
				{Role: "summary", Required: false},
			},
		},
	}
	return &recipes.Resolved{Recipe: r, Hash: strings.Repeat("b", 64)}
}

func staticRecipe() *recipes.Resolved {
	r := &recipes.Recipe{
		Schema:       recipes.SchemaVersion,
		ID:           "static-feat",
		Title:        "Static Feature",
		OutputFormat: "article",
		Blueprint: &recipes.Blueprint{
			Units: []recipes.BlueprintUnit{
				{Role: "intro", Title: "Week {week}", Required: true},
				{Role: "summary"},
			},
		},
		Inputs: []recipes.Input{{Name: "week", Type: recipes.InputTypeWeek, Required: true}},
	}
	return &recipes.Resolved{Recipe: r, Hash: strings.Repeat("c", 64)}
}

func verificationWithTeams() *contracts.VerificationResult {
	return &contracts.VerificationResult{
		StableID:       "verify-1",
		VerifiedAt:     time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC),
		InputDossierID: "dossier-1",
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-1": contracts.VerificationStatusSupported,
			"claim-2": contracts.VerificationStatusSupported,
			"claim-3": contracts.VerificationStatusContradicted,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-1": {ClaimID: "claim-1", Statement: "Bills are 4-2", VerificationStatus: contracts.VerificationStatusSupported, ConfidenceLevel: contracts.ConfidenceHigh},
			"claim-2": {ClaimID: "claim-2", Statement: "Dolphins are 3-3", VerificationStatus: contracts.VerificationStatusSupported, ConfidenceLevel: contracts.ConfidenceHigh},
			"claim-3": {ClaimID: "claim-3", Statement: "Texans are 10-0", VerificationStatus: contracts.VerificationStatusContradicted, ConfidenceLevel: contracts.ConfidenceMedium},
		},
	}
}

func TestPlanNoBlueprint(t *testing.T) {
	r := &recipes.Resolved{Recipe: &recipes.Recipe{Schema: recipes.SchemaVersion, ID: "x", Title: "X"}}
	cl := &fakeClient{}
	p, err := New(cl, "m", fixedNow())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := p.Plan(context.Background(), r, verificationWithTeams(), "thread", nil)
	if err != nil || plan != nil {
		t.Fatalf("no blueprint must yield (nil, nil), got (%v, %v)", plan, err)
	}
	if cl.calls != 0 {
		t.Fatal("no LLM call expected without a blueprint")
	}
}

func TestPlanStaticBlueprintIsDeterministic(t *testing.T) {
	cl := &fakeClient{}
	p, err := New(cl, "m", fixedNow())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := p.Plan(context.Background(), staticRecipe(), verificationWithTeams(), "article", staticInputs())
	if err != nil {
		t.Fatal(err)
	}
	if plan == nil {
		t.Fatal("plan is nil")
	}
	if cl.calls != 0 {
		t.Fatal("static blueprint must not call the LLM")
	}
	if len(plan.Units) != 2 {
		t.Fatalf("units = %d", len(plan.Units))
	}
	if plan.Units[0].Title != "Week 7" {
		t.Fatalf("title = %q (input template must render)", plan.Units[0].Title)
	}
	if !plan.Units[0].Required {
		t.Error("required flag not propagated")
	}
	if plan.StableID != "plan-static-feat" || plan.RecipeID != "static-feat" || plan.Format != "article" {
		t.Fatalf("identity = %+v", plan)
	}
	if !plan.Units[0].Supported {
		t.Error("static unit without claims must be supported (structural)")
	}
}

func staticInputs() []recipes.ResolvedInput {
	return []recipes.ResolvedInput{{Input: recipes.Input{Name: "week", Type: recipes.InputTypeWeek}, Value: "7"}}
}

const entityJSON = `{
  "entities": [
    {"name": "Bills", "fields": {"record": "4-2"}, "claim_ids": ["claim-1"]},
    {"name": "Dolphins", "fields": {"record": "3-3"}, "claim_ids": ["claim-2"]},
    {"name": "Texans", "fields": {"record": "10-0"}, "claim_ids": ["claim-3"]},
    {"name": "Ghosts", "fields": {"record": "9-9"}, "claim_ids": ["claim-1", "claim-2"]}
  ]
}`

func TestPlanEntityExtraction(t *testing.T) {
	cl := &fakeClient{content: entityJSON}
	p, err := New(cl, "m", fixedNow())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := p.Plan(context.Background(), recipeWithBlueprint(), verificationWithTeams(), "thread", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cl.calls != 1 {
		t.Fatalf("llm calls = %d", cl.calls)
	}
	// intro + 2 grounded entities (Texans dropped: only contradicted
	// evidence) + summary = 4 units; Ghosts beyond max_count=3... the
	// cap applies to grounded entities only (2 grounded, cap 3) → both kept.
	if len(plan.Units) != 5 {
		t.Fatalf("units = %d (want intro + 2 entity + ghosts + summary = 5): %+v", len(plan.Units), plan.Units)
	}
	// Entity unit titles render from the entity name.
	if plan.Units[1].Title != "Bills" || plan.Units[1].Entity.Name != "Bills" {
		t.Fatalf("unit 1 = %+v", plan.Units[1])
	}
	if plan.Units[2].Title != "Dolphins" {
		t.Fatalf("unit 2 = %+v", plan.Units[2])
	}
	// Ghosts cited only supported claims → kept, fully supported.
	if plan.Units[3].Entity.Name != "Ghosts" {
		t.Fatalf("unit 3 = %+v", plan.Units[3])
	}
	if !plan.Units[3].Supported {
		t.Error("ghosts should be supported (cites supported claims)")
	}
	if plan.Units[4].Role != "summary" {
		t.Fatalf("unit 4 = %+v", plan.Units[4])
	}
}

func TestPlanMaxCountClamp(t *testing.T) {
	cl := &fakeClient{content: entityJSON}
	p, err := New(cl, "m", fixedNow())
	if err != nil {
		t.Fatal(err)
	}
	res := recipeWithBlueprint()
	res.Recipe.Research.Entity.MaxCount = 1
	plan, err := p.Plan(context.Background(), res, verificationWithTeams(), "thread", nil)
	if err != nil {
		t.Fatal(err)
	}
	entityUnits := 0
	for _, u := range plan.Units {
		if u.Entity != nil {
			entityUnits++
		}
	}
	if entityUnits != 1 {
		t.Fatalf("entity units = %d, want 1 (max_count clamp)", entityUnits)
	}
}

func TestPlanDropsUngroundedEntities(t *testing.T) {
	cl := &fakeClient{content: `{"entities":[{"name":"Phantom","fields":{"record":"x"},"claim_ids":["claim-3","claim-44"]}]}`}
	p, _ := New(cl, "m", fixedNow())
	plan, err := p.Plan(context.Background(), recipeWithBlueprint(), verificationWithTeams(), "thread", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range plan.Units {
		if u.Entity != nil {
			t.Fatalf("entity %q survived without supported claims", u.Entity.Name)
		}
	}
	if len(plan.Units) != 2 { // intro + summary only
		t.Fatalf("units = %d", len(plan.Units))
	}
}

func TestPlanLLMFailureFailsClosed(t *testing.T) {
	cl := &fakeClient{err: errors.New("boom")}
	p, _ := New(cl, "m", fixedNow())
	if _, err := p.Plan(context.Background(), recipeWithBlueprint(), verificationWithTeams(), "thread", nil); err == nil {
		t.Fatal("LLM failure must fail the plan (never fabricate entities)")
	}
}

func TestPlanUnparseableJSONFailsClosed(t *testing.T) {
	cl := &fakeClient{content: "not json at all"}
	p, _ := New(cl, "m", fixedNow())
	if _, err := p.Plan(context.Background(), recipeWithBlueprint(), verificationWithTeams(), "thread", nil); err == nil {
		t.Fatal("unparseable model output must fail the plan")
	}
}

func TestNewRequiresClient(t *testing.T) {
	if _, err := New(nil, "m", fixedNow()); err == nil {
		t.Fatal("nil client must be rejected")
	}
}

func TestPlanRequiresVerification(t *testing.T) {
	cl := &fakeClient{}
	p, _ := New(cl, "m", fixedNow())
	if _, err := p.Plan(context.Background(), staticRecipe(), nil, "article", nil); err == nil {
		t.Fatal("nil verification must be rejected when a blueprint exists")
	}
}

func TestBuildPromptCarriesClaimsAndRecipeText(t *testing.T) {
	res := recipeWithBlueprint()
	vres := verificationWithTeams()
	prompt := (&Planner{prompt: "p", cfg: fixedNow()}).buildPrompt(res, vres, staticInputs())
	for _, want := range []string{"team-weekly", "kind: team", "max_count: 3", "Bills are 4-2", "10-0"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if !strings.Contains(prompt, "=== VERIFIED CLAIMS ===") {
		t.Error("prompt must delimit verified claims")
	}
}
