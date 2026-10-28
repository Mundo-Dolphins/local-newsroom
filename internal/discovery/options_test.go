package discovery

import (
	"context"
	"errors"
	"testing"

	"github.com/Mundo-Dolphins/local-newsroom/internal/planner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/search"
)

func TestSeedPlanBoundsToMaxQueries(t *testing.T) {
	qs := []string{"one", "two", "three", "four"}
	p := seedPlan("topic", "en", nil, qs, 2)
	if len(p.Queries) != 2 {
		t.Fatalf("queries = %d, want 2 (budget narrows)", len(p.Queries))
	}
	if p.OriginalTopic != "topic" || p.Language != "en" {
		t.Fatalf("plan identity = %+v", p)
	}
}

func TestSeedPlanDedupesAndTrims(t *testing.T) {
	p := seedPlan("t", "en", nil, []string{" a ", "a", "", "b"}, 10)
	if len(p.Queries) != 2 {
		t.Fatalf("queries = %+v, want [a b]", p.Queries)
	}
	if p.Queries[0].Query != "a" {
		t.Fatalf("query = %q", p.Queries[0].Query)
	}
}

func TestSeedPlanZeroMaxUsesDefault(t *testing.T) {
	p := seedPlan("t", "en", nil, []string{"a", "b"}, 0)
	if len(p.Queries) != 2 {
		t.Fatalf("queries = %d", len(p.Queries))
	}
}

func TestSeedPlanCarriesTimeRange(t *testing.T) {
	hint := &planner.TimeRangeHint{StartDate: "2025-09-29", EndDate: "2025-10-05"}
	p := seedPlan("t", "en", hint, []string{"a"}, 10)
	if p.Queries[0].TimeRange != hint {
		t.Fatal("time range not carried")
	}
	if p.TimeRangeHint != hint {
		t.Fatal("plan time range not set")
	}
}

func TestWithSeedQueriesAccumulates(t *testing.T) {
	var co callOptions
	WithSeedQueries("a", "b")(&co)
	WithSeedQueries("c")(&co)
	if len(co.seedQueries) != 3 {
		t.Fatalf("seedQueries = %v", co.seedQueries)
	}
}

// plannerCallCount records whether the LLM planner was invoked.
type countingPlanner struct {
	plan   *planner.SearchPlan
	calls  int
	failed bool
}

func (p *countingPlanner) Generate(ctx context.Context, topic string, language string, hint *planner.TimeRangeHint) (*planner.SearchPlan, error) {
	p.calls++
	if p.failed {
		return nil, errors.New("planner should not be called with seed queries")
	}
	return p.plan, nil
}

func TestDiscoverWithSeedQueriesBypassesPlanner(t *testing.T) {
	plan := &planner.SearchPlan{
		OriginalTopic: "planner topic",
		Queries:       []planner.SearchQuery{{Query: "planner-query", Purpose: "planned"}},
		Language:      "en",
	}
	counted := &countingPlanner{plan: plan}
	provider := NewFakeProvider([]search.Response{
		{
			Results: []search.SearchResult{{URL: "https://a.example.com/1"}},
		},
	})
	d := NewDiscovery(counted, provider, DefaultConfig())
	res, err := d.Discover(context.Background(), "topic", "en", nil, WithSeedQueries("seed-1", "seed-1"))
	if err != nil {
		t.Fatal(err)
	}
	if counted.calls != 0 {
		t.Fatalf("planner called %d times; seed queries must bypass it", counted.calls)
	}
	if res == nil || len(res.PlannedQueries) != 1 {
		t.Fatalf("planned queries = %+v", res.PlannedQueries)
	}
	if res.PlannedQueries[0].Query != "seed-1" {
		t.Fatalf("query = %q", res.PlannedQueries[0].Query)
	}
}

func TestDiscoverWithoutOptionsUsesPlanner(t *testing.T) {
	plan := &planner.SearchPlan{
		OriginalTopic: "planner topic",
		Queries:       []planner.SearchQuery{{Query: "planner-query", Purpose: "planned"}},
		Language:      "en",
	}
	counted := &countingPlanner{plan: plan}
	provider := NewFakeProvider([]search.Response{
		{
			Results: []search.SearchResult{{URL: "https://a.example.com/1"}},
		},
	})
	d := NewDiscovery(counted, provider, DefaultConfig())
	res, err := d.Discover(context.Background(), "topic", "en", nil)
	if err != nil {
		t.Fatal(err)
	}
	if counted.calls != 1 {
		t.Fatalf("planner calls = %d, want 1 (standard path unchanged)", counted.calls)
	}
	if len(res.PlannedQueries) != 1 || res.PlannedQueries[0].Query != "planner-query" {
		t.Fatalf("planned = %+v", res.PlannedQueries)
	}
}
