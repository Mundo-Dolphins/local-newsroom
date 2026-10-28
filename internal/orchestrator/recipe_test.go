package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/discovery"
	"github.com/Mundo-Dolphins/local-newsroom/internal/planner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/recipes"
	"github.com/Mundo-Dolphins/local-newsroom/internal/search"
)

func recipeForOrch() *recipes.Resolved {
	r := &recipes.Recipe{
		Schema:       recipes.SchemaVersion,
		ID:           "team-weekly",
		Title:        "Team Weekly",
		OutputFormat: "thread",
		Inputs:       []recipes.Input{{Name: "week", Type: recipes.InputTypeWeek, Required: true}},
		Source: &recipes.SourcePolicy{
			ExplicitURLs:     "optional",
			PreferredDomains: []string{".nfl.com"},
			RequiredDomains:  []string{".nfl.com"},
			QueryHints:       []string{"{week} nfl team news"},
			MaxQueries:       3,
			MaxSources:       5,
			DateFrom:         "{date}",
		},
		Research: &recipes.ResearchSpec{
			Instructions: "Research the teams.",
			Entity:       &recipes.EntitySpec{Kind: "team", Fields: []string{"record"}, MaxCount: 4},
		},
		Blueprint: &recipes.Blueprint{
			MissingPolicy: recipes.MissingWarn,
			Units: []recipes.BlueprintUnit{
				{Role: "intro", Title: "Week {week}", Required: true},
				{Role: "entity", Repeat: "team", Title: "{entity.name}", Required: true},
			},
		},
	}
	return &recipes.Resolved{Recipe: r, Root: "/tmp", Hash: "deadbeef"}
}

func orchWithRecipe() *Orchestrator {
	return &Orchestrator{cfg: Config{
		Recipe:       recipeForOrch(),
		RecipeInputs: []recipes.ResolvedInput{{Input: recipes.Input{Name: "week", Type: recipes.InputTypeWeek}, Value: "7"}},
	}}
}

func TestHasRecipe(t *testing.T) {
	o := orchWithRecipe()
	if !o.hasRecipe() {
		t.Fatal("hasRecipe should be true")
	}
	standard := &Orchestrator{cfg: Config{}}
	if standard.hasRecipe() {
		t.Fatal("standard run must have no recipe")
	}
}

func TestRecipeInputVars(t *testing.T) {
	o := orchWithRecipe()
	vars := o.recipeInputVars()
	if vars["week"] != "7" {
		t.Fatalf("vars = %v", vars)
	}
	if (&Orchestrator{cfg: Config{}}).recipeInputVars() != nil {
		t.Fatal("standard run must have no input vars")
	}
}

// recordingPlanner fails loudly if invoked: with recipe seed queries the
// LLM planner must be bypassed entirely.
type recordingPlanner struct{ calls int }

func (p *recordingPlanner) Generate(ctx context.Context, topic string, language string, hint *planner.TimeRangeHint) (*planner.SearchPlan, error) {
	p.calls++
	return &planner.SearchPlan{OriginalTopic: topic, Language: language,
		Queries: []planner.SearchQuery{{Query: "planned", Purpose: "planned"}}}, nil
}

// staticSearchProvider returns one fixed result for every query.
type staticSearchProvider struct{}

func (staticSearchProvider) Search(ctx context.Context, req search.Request) (search.Response, error) {
	return search.Response{
		OriginalQuery: req.Query,
		Results:       []search.SearchResult{{URL: "https://nfl.com/" + req.Query, ID: req.Query}},
	}, nil
}

func TestDiscoveryRecipeOptionsSeedQueries(t *testing.T) {
	o := orchWithRecipe()
	opts := o.discoveryRecipeOptions()
	if len(opts) != 1 {
		t.Fatalf("opts = %d, want 1 (seed queries)", len(opts))
	}
	plan := &recordingPlanner{}
	disc := discovery.NewDiscovery(plan, staticSearchProvider{}, discovery.DefaultConfig())
	res, err := disc.Discover(context.Background(), "topic", "en", o.recipeTimeRangeHint(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	if plan.calls != 0 {
		t.Fatalf("LLM planner called %d times; recipe hints must seed discovery deterministically", plan.calls)
	}
	if len(res.PlannedQueries) != 1 || res.PlannedQueries[0].Query != "7 nfl team news" {
		t.Fatalf("planned = %+v", res.PlannedQueries)
	}
	// The time range hint from the recipe must ride along.
	if len(res.QueryResults) != 1 {
		t.Fatalf("query results = %d", len(res.QueryResults))
	}
}

func TestDiscoveryRecipeOptionsNoHints(t *testing.T) {
	o := orchWithRecipe()
	o.cfg.Recipe.Recipe.Source.QueryHints = nil
	if opts := o.discoveryRecipeOptions(); opts != nil {
		t.Fatalf("no hints → no options, got %d", len(opts))
	}
}

func TestRecipeTimeRangeHint(t *testing.T) {
	o := orchWithRecipe()
	o.cfg.RecipeInputs = append(o.cfg.RecipeInputs, recipes.ResolvedInput{Input: recipes.Input{Name: "date", Type: recipes.InputTypeDate}, Value: "2025-10-01"})
	hint := o.recipeTimeRangeHint()
	if hint == nil || hint.StartDate != "2025-10-01" {
		t.Fatalf("hint = %+v", hint)
	}
	standard := &Orchestrator{cfg: Config{}}
	if standard.recipeTimeRangeHint() != nil {
		t.Fatal("standard run must have no hint")
	}
}

func TestRecipeMaxSourcesNarrowsOnly(t *testing.T) {
	o := orchWithRecipe() // recipe max_sources = 5
	if got := o.recipeMaxSources(12); got != 5 {
		t.Fatalf("recipeMaxSources(12) = %d, want 5 (narrow)", got)
	}
	if got := o.recipeMaxSources(3); got != 3 {
		t.Fatalf("recipeMaxSources(3) = %d, want 3 (never widen)", got)
	}
	if got := o.recipeMaxSources(0); got != 5 {
		t.Fatalf("recipeMaxSources(0) = %d, want 5 (engine default then recipe)", got)
	}
	if got := (&Orchestrator{cfg: Config{}}).recipeMaxSources(8); got != 8 {
		t.Fatalf("standard run must keep its budget, got %d", got)
	}
}

func TestPreferredFirstStable(t *testing.T) {
	cands := []discovery.Candidate{
		{CandidateURL: "https://other.com/1", CandidateDomain: "other.com"},
		{CandidateURL: "https://nfl.com/2", CandidateDomain: "nfl.com"},
		{CandidateURL: "https://third.com/3", CandidateDomain: "third.com"},
		{CandidateURL: "https://espn.nfl.com/4", CandidateDomain: "espn.nfl.com"},
	}
	out := preferredFirst(cands, []string{".nfl.com"})
	if out[0].CandidateDomain != "nfl.com" || out[1].CandidateDomain != "espn.nfl.com" {
		t.Fatalf("preferred not first: %v %v", out[0].CandidateDomain, out[1].CandidateDomain)
	}
	// Rank inside the group preserved, and the original slice untouched.
	if cands[0].CandidateDomain != "other.com" {
		t.Fatal("input slice mutated")
	}
	if len(out) != 4 {
		t.Fatalf("lost candidates: %d", len(out))
	}
}

func TestApplyRequiredDomains(t *testing.T) {
	selected := []string{
		"https://nfl.com/a",
		"https://other.com/b",
		"https://TEAM.NFL.COM/c", // case-insensitive
	}
	out, err := applyRequiredDomains(selected, []string{".nfl.com"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("out = %v", out)
	}
	// None match → fail closed.
	if _, err := applyRequiredDomains([]string{"https://other.com/b"}, []string{".nfl.com"}); err == nil {
		t.Fatal("no matching sources must fail the run")
	}
	// No required domains → unchanged.
	out, err = applyRequiredDomains(selected, nil)
	if err != nil || len(out) != 3 {
		t.Fatalf("no policy must pass through: %v %v", out, err)
	}
}

func TestRequiredDomainMatchSuffix(t *testing.T) {
	if !requiredDomainMatch("https://www.nfl.com/x", []string{".nfl.com"}) {
		t.Error("suffix must match")
	}
	if requiredDomainMatch("https://notnfl.com/x", []string{".nfl.com"}) {
		t.Error("unrelated host must not match")
	}
	if requiredDomainMatch("https://bad", []string{".nfl.com"}) {
		t.Error("unparseable URL must not match")
	}
}

func vresForPlan() *contracts.VerificationResult {
	return &contracts.VerificationResult{
		StableID:       "verify-1",
		InputDossierID: "dossier-1",
		VerifiedAt:     time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC),
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-1": contracts.VerificationStatusSupported,
			"claim-2": contracts.VerificationStatusContradicted,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-1": {ClaimID: "claim-1", Statement: "a", VerificationStatus: contracts.VerificationStatusSupported, ConfidenceLevel: contracts.ConfidenceHigh},
			"claim-2": {ClaimID: "claim-2", Statement: "b", VerificationStatus: contracts.VerificationStatusContradicted, ConfidenceLevel: contracts.ConfidenceMedium},
		},
	}
}

func contentPlanForTest() *contracts.ContentPlan {
	return &contracts.ContentPlan{
		StableID: "plan-1", RecipeID: "team-weekly", Format: "thread",
		Units: []contracts.PlanUnit{
			{ID: "unit-1", Role: "intro", Title: "Week 7", Required: true, Supported: true, ClaimIDs: []string{"claim-1"}},
			{ID: "unit-2", Role: "entity", Title: "Bills", Entity: &contracts.PlanEntity{Kind: "team", Name: "Bills"}, Required: true, Supported: true, ClaimIDs: []string{"claim-1"}},
			{ID: "unit-3", Role: "entity", Title: "Phantom", Entity: &contracts.PlanEntity{Kind: "team", Name: "Phantom"}, Required: true, Supported: false, ClaimIDs: []string{"claim-2"}},
		},
	}
}

func TestEnforceContentPlanWarnPolicy(t *testing.T) {
	vres := vresForPlan()
	plan := contentPlanForTest()
	artifact := &contracts.EditorialArtifact{
		Title: "Week 7",
		Posts: []contracts.Post{{Body: "Bills report: 4-2."}},
	}
	warnings, err := enforceContentPlan(plan, artifact, vres, recipes.MissingWarn)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || warnings[0].Code != "plan.unit_unsupported" {
		t.Fatalf("warnings = %+v", warnings)
	}
}

func TestEnforceContentPlanFailPolicy(t *testing.T) {
	vres := vresForPlan()
	plan := contentPlanForTest()
	artifact := &contracts.EditorialArtifact{
		Posts: []contracts.Post{{Body: "Bills report: 4-2."}},
	}
	_, err := enforceContentPlan(plan, artifact, vres, recipes.MissingFail)
	if err == nil {
		t.Fatal("fail policy must fail the run")
	}
}

func TestEnforceContentPlanMissingUnit(t *testing.T) {
	vres := vresForPlan()
	plan := contentPlanForTest()
	// Supported entity unit whose name never appears in the output.
	plan.Units[2].ClaimIDs = []string{"claim-1"} // now supported
	artifact := &contracts.EditorialArtifact{Title: "Week 7", Posts: []contracts.Post{{Body: "Bills 4-2."}}}
	warnings, err := enforceContentPlan(plan, artifact, vres, recipes.MissingWarn)
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]bool{}
	for _, w := range warnings {
		codes[w.Code] = true
	}
	if !codes["plan.unit_missing"] {
		t.Fatalf("missing unit must warn, got %+v", warnings)
	}
}

func TestEnforceContentPlanNil(t *testing.T) {
	warnings, err := enforceContentPlan(nil, nil, nil, recipes.MissingWarn)
	if err != nil || warnings != nil {
		t.Fatalf("nil plan must be a no-op: %v %v", warnings, err)
	}
}

func TestArtifactTextCoversSurfaces(t *testing.T) {
	a := &contracts.EditorialArtifact{
		Title:    "Head",
		Subtitle: "Sub",
		Body:     "main",
		Sections: []contracts.Section{{Title: "S1", Body: "body1"}},
		Posts:    []contracts.Post{{Body: "post1"}},
	}
	text := artifactText(a)
	for _, want := range []string{"head", "sub", "main", "s1", "body1", "post1"} {
		if !contains(text, want) {
			t.Fatalf("artifact text missing %q: %q", want, text)
		}
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
