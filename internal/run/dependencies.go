package run

import (
	"fmt"
	"sort"
)

// stageDependencies is the explicit dependency graph of the canonical
// pipeline. It records, for every standard stage, the stages whose outputs it
// consumes.
//
// Dependencies are data, not string ordering: they are the single source of
// truth for what an upstream retry must invalidate and what a resume must
// validate before a stage runs. The canonical pipeline order (AllStageNames)
// is one topological order of this graph, and every run plan is required to
// be a topological order as well (see ValidateStagePlanOrder), so the plan
// order and the graph can never silently disagree.
//
// The data flow (v0.4/v0.5 pipeline):
//
//	discovery  finds candidate source URLs
//	fetch      consumes the URLs (discovered or user-provided)
//	extract    consumes fetched content, produces documents
//	research   consumes documents, produces the dossier
//	verify     consumes the dossier, produces the verification result
//	follow_up  consumes the verification result (unresolved claims); optional
//	write      consumes the verification result (+ follow-up resolutions)
//	final_check consumes the editorial artifact
//	render     consumes the final checked editorial artifact
var stageDependencies = map[StageName][]StageName{
	StageDiscovery:  {},
	StageFetch:      {StageDiscovery},
	StageExtract:    {StageFetch},
	StageResearch:   {StageExtract},
	StageVerify:     {StageResearch},
	StageFollowUp:   {StageVerify},
	StageWrite:      {StageVerify, StageFollowUp},
	StageFinalCheck: {StageWrite},
	StageRender:     {StageFinalCheck},
}

// Dependencies returns the direct upstream stages of stage (the stages whose
// outputs it consumes), in canonical order. Stages with no upstream return an
// empty, non-nil slice; an unknown stage name returns an empty slice as well
// (the graph only covers the standard stages, and unknown names are rejected
// by the rest of the contract).
func Dependencies(stage StageName) []StageName {
	deps := make([]StageName, 0, len(stageDependencies[stage]))
	deps = append(deps, stageDependencies[stage]...)
	sort.Slice(deps, func(i, j int) bool {
		return canonicalIndex(deps[i]) < canonicalIndex(deps[j])
	})
	return deps
}

// DownstreamClosure returns stage itself plus every standard stage that
// (transitively) depends on it, in canonical order.
//
// This is the invalidation set of an upstream retry: when stage re-runs, its
// outputs change, so every stage that consumes those outputs directly or
// indirectly is stale and must not present its previous output as current.
func DownstreamClosure(stage StageName) []StageName {
	seen := map[StageName]bool{stage: true}
	frontier := []StageName{stage}
	for len(frontier) > 0 {
		next := make([]StageName, 0, len(frontier))
		for _, s := range frontier {
			for _, dep := range AllStageNames {
				if seen[dep] {
					continue
				}
				if dependsOn(dep, s) {
					seen[dep] = true
					next = append(next, dep)
				}
			}
		}
		frontier = next
	}
	out := make([]StageName, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		return canonicalIndex(out[i]) < canonicalIndex(out[j])
	})
	return out
}

// dependsOn reports whether a directly depends on b (b appears in a's direct
// dependencies).
func dependsOn(a, b StageName) bool {
	for _, d := range stageDependencies[a] {
		if d == b {
			return true
		}
	}
	return false
}

// canonicalIndex returns a stage's position in AllStageNames for deterministic
// ordering; unknown names sort last.
func canonicalIndex(s StageName) int {
	for i, name := range AllStageNames {
		if name == s {
			return i
		}
	}
	return len(AllStageNames)
}

// ValidateStagePlanOrder checks that a stage plan is a topological order of
// the dependency graph: every stage's dependencies must appear EARLIER in
// the plan (dependencies absent from the plan are satisfied by definition —
// a run may plan any subset of the canonical stages).
//
// This is what keeps "explicit dependencies" and "plan order" honest: a run
// executes its plan in order, so a plan whose order contradicts the graph
// would execute a stage before its inputs exist. Such a plan is inconsistent
// and is rejected both by NewManifest and by Manifest.Validate (which is why
// it can never be persisted or loaded).
func ValidateStagePlanOrder(names []StageName) error {
	position := make(map[StageName]int, len(names))
	for i, n := range names {
		position[n] = i
	}
	for i, n := range names {
		for _, dep := range Dependencies(n) {
			j, planned := position[dep]
			if planned && j > i {
				return fmt.Errorf("stage %q depends on %q, which does not precede it in the plan; re-plan the run so dependencies run first", n, dep)
			}
		}
	}
	return nil
}
