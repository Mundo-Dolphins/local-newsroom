package discovery

import (
	"strings"

	"github.com/Mundo-Dolphins/local-newsroom/internal/planner"
)

// Options customize a single [Discovery.Discover] call (v0.6 editorial
// recipes).
//
// An Option may only NARROW discovery behavior (budgets, explicit queries):
// it can never loosen a configured bound. When no Option is passed the
// pipeline is byte-identical to the pre-recipe behavior.
type Option func(*callOptions)

// callOptions is the resolved option set for one Discover call.
type callOptions struct {
	// seedQueries, when non-empty, replaces the LLM planner: these queries
	// (rendered from the recipe's seed query templates with the run's
	// inputs) are executed directly. They are still bounded by the
	// configured MaxSearchQueries (truncated, never extended).
	seedQueries []string

	// memoryHints is the optional editorial-memory hint block appended to
	// the planner prompt (issue #145). It is context, never evidence: the
	// block may guide query formulation (its own framing says so) but the
	// queries always target live sources, so the plan can only ever
	// contribute evidence through the normal search/fetch/verify path. An
	// empty value leaves the planner prompt byte-identical to the
	// pre-memory behavior.
	memoryHints string
}

// WithSeedQueries provides an explicit list of search queries, bypassing the
// LLM planner. Used by recipe runs whose research specification declares
// deterministic seed queries.
func WithSeedQueries(queries ...string) Option {
	return func(o *callOptions) {
		o.seedQueries = append(o.seedQueries, queries...)
	}
}

// WithMemoryHints provides the rendered editorial-memory hint block for the
// discovery call (issue #145). It is passed to the planner as optional
// context — the block's own framing states that hints must never be reported
// as fact — and is recorded on the resulting plan (SearchPlan.MemoryHints)
// for provenance. An empty value disables the projection entirely, keeping
// the planner prompt byte-identical to the pre-memory behavior.
func WithMemoryHints(hints string) Option {
	return func(o *callOptions) {
		o.memoryHints = strings.TrimSpace(hints)
	}
}

// seedPlan builds a search plan directly from seed queries (the planner is
// bypassed). The queries are bounded by the configured MaxSearchQueries:
// the budget only narrows. Empty or duplicate queries are dropped.
func seedPlan(topic, language string, hint *planner.TimeRangeHint, queries []string, maxQueries int) *planner.SearchPlan {
	if maxQueries < 1 {
		maxQueries = 10
	}
	seen := make(map[string]bool, len(queries))
	out := make([]planner.SearchQuery, 0, len(queries))
	for _, q := range queries {
		q = strings.TrimSpace(q)
		if q == "" || seen[q] {
			continue
		}
		seen[q] = true
		out = append(out, planner.SearchQuery{
			Query:     q,
			Purpose:   "recipe seed query",
			TimeRange: hint,
		})
		if len(out) >= maxQueries {
			break
		}
	}
	return &planner.SearchPlan{
		OriginalTopic: topic,
		Queries:       out,
		Language:      language,
		TimeRangeHint: hint,
	}
}
