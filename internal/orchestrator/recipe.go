package orchestrator

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/discovery"
	"github.com/Mundo-Dolphins/local-newsroom/internal/planner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/recipes"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/runner"
)

// resolvedRecipePath is the canonical run-directory path of the persisted
// resolved recipe (canonical definition lives in the run package).
const resolvedRecipePath = run.ArtifactPathResolvedRecipe

// hasRecipe reports whether this orchestrator executes a recipe run.
func (o *Orchestrator) hasRecipe() bool {
	return o.cfg.Recipe != nil
}

// recipeInputVars renders template variables from the recipe's resolved run
// inputs (nil for standard runs).
func (o *Orchestrator) recipeInputVars() map[string]string {
	if !o.hasRecipe() {
		return nil
	}
	return o.cfg.Recipe.Recipe.InputVars(o.cfg.RecipeInputs)
}

// persistResolvedRecipe writes the resolved recipe to the run directory.
//
// The discovery stage is the first stage of every run, so it is the stage
// that persists the document: the research and write stages read their
// recipe parameters from the orchestrator configuration (baked at run
// creation), and a RESUMED run re-loads this file in the command layer
// (hash-checked against the manifest) — the recipe a run starts with is the
// recipe it finishes with.
func (o *Orchestrator) persistResolvedRecipe(in *runner.StageInput) (runner.StageFile, error) {
	return o.stageFile(run.ArtifactKindCustom, resolvedRecipePath, o.cfg.Recipe)
}

// recipeSourcePolicy returns the recipe's source policy (nil for standard
// runs or recipes without one).
func (o *Orchestrator) recipeSourcePolicy() *recipes.SourcePolicy {
	if !o.hasRecipe() {
		return nil
	}
	return o.cfg.Recipe.Recipe.Source
}

// discoverRecipeRun is the discovery call for recipe runs: the recipe's
// rendered query hints seed discovery (deterministic research for recurring
// formats) and its date window becomes the search time hint.
func (o *Orchestrator) discoverRecipeRun(ctx context.Context, topic, language string, opts []discovery.Option) (*discovery.DiscoveryResult, error) {
	return o.cfg.Discovery.Discover(ctx, topic, language, o.recipeTimeRangeHint(), opts...)
}

// defaultDiscoverRun is the discovery call for standard runs (byte-identical
// pre-recipe behavior: no options, no time hint from a recipe).
func (o *Orchestrator) defaultDiscoverRun(ctx context.Context, topic, language string) (*discovery.DiscoveryResult, error) {
	return o.cfg.Discovery.Discover(ctx, topic, language, nil)
}

// discoveryRecipeOptions derives discovery options from the recipe's source
// policy: its rendered query hints replace the LLM planner for the run
// (deterministic discovery for recurring formats) while the engine still
// bounds queries and candidates.
func (o *Orchestrator) discoveryRecipeOptions() []discovery.Option {
	if !o.hasRecipe() {
		return nil
	}
	sp := o.cfg.Recipe.Recipe.Source
	if sp == nil || len(sp.QueryHints) == 0 {
		return nil
	}
	vars := o.recipeInputVars()
	qs := make([]string, 0, len(sp.QueryHints))
	for _, h := range sp.QueryHints {
		qs = append(qs, recipes.RenderTemplate(h, vars))
	}
	return []discovery.Option{discovery.WithSeedQueries(qs...)}
}

// recipeTimeRangeHint renders the recipe's search window (date_from /
// date_to, templates over the resolved inputs) into the planner's time
// range hint. Inputs of type "date" are validated at resolution, so the
// rendered values are ISO dates.
func (o *Orchestrator) recipeTimeRangeHint() *planner.TimeRangeHint {
	if !o.hasRecipe() {
		return nil
	}
	sp := o.cfg.Recipe.Recipe.Source
	if sp == nil || (sp.DateFrom == "" && sp.DateTo == "") {
		return nil
	}
	vars := o.recipeInputVars()
	from := recipes.RenderTemplate(sp.DateFrom, vars)
	to := recipes.RenderTemplate(sp.DateTo, vars)
	if from == "" && to == "" {
		return nil
	}
	return &planner.TimeRangeHint{StartDate: from, EndDate: to}
}

// recipeMaxSources narrows the run's source budget with the recipe's
// max_sources (a recipe can narrow the engine's bound, never widen it).
func (o *Orchestrator) recipeMaxSources(max int) int {
	if !o.hasRecipe() {
		return max
	}
	sp := o.cfg.Recipe.Recipe.Source
	if sp == nil || sp.MaxSources <= 0 {
		return max
	}
	if max <= 0 {
		max = DefaultMaxSources
	}
	if sp.MaxSources < max {
		max = sp.MaxSources
	}
	return max
}

// preferredFirst orders candidates so those on preferred domains come first
// (stable: discovery rank is preserved inside each group).
func preferredFirst(cands []discovery.Candidate, preferred []string) []discovery.Candidate {
	if len(preferred) == 0 {
		return cands
	}
	out := make([]discovery.Candidate, len(cands))
	copy(out, cands)
	sort.SliceStable(out, func(i, j int) bool {
		return isPreferredDomain(out[i].CandidateDomain, preferred) && !isPreferredDomain(out[j].CandidateDomain, preferred)
	})
	return out
}

// requiredDomainMatch reports whether u's domain satisfies one required
// domain (suffix match, case-insensitive; ".nfl.com" matches all of it).
func requiredDomainMatch(u string, required []string) bool {
	if len(required) == 0 {
		return true
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return false // unparseable: the policy cannot verify it → fail closed
	}
	host := strings.ToLower(parsed.Hostname())
	for _, d := range required {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		if !strings.HasPrefix(d, ".") {
			d = "." + d
		}
		if host == strings.TrimPrefix(d, ".") || strings.HasSuffix(host, d) {
			return true
		}
	}
	return false
}

// isPreferredDomain reports whether host matches one preferred domain
// (suffix match, case-insensitive).
func isPreferredDomain(host string, preferred []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, d := range preferred {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		if !strings.HasPrefix(d, ".") {
			d = "." + d
		}
		if host == strings.TrimPrefix(d, ".") || strings.HasSuffix(host, d) {
			return true
		}
	}
	return false
}

// applyRequiredDomains keeps only URLs that satisfy the recipe's required
// domains. A run whose recipe mandates domains that none of its sources
// satisfy cannot proceed (fail-closed: the policy is never silently
// ignored).
func applyRequiredDomains(selected []string, required []string) ([]string, error) {
	if len(required) == 0 {
		return selected, nil
	}
	out := make([]string, 0, len(selected))
	for _, u := range selected {
		if requiredDomainMatch(u, required) {
			out = append(out, u)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the recipe requires sources from %s, but none of the %d selected source(s) come from those domains",
			strings.Join(required, ", "), len(selected))
	}
	return out, nil
}

// enforceContentPlan checks the written artifact against the content plan
// deterministically:
//
//   - a REQUIRED unit whose evidence is unsupported (the engine's claim
//     check) triggers the recipe's missing policy (warn or fail) — the
//     engine never fabricates the missing unit;
//   - a REQUIRED unit the artifact does not contain (its entity name, or
//     its rendered title, appears nowhere in the output text) triggers the
//     same policy.
//
// Non-required units are advisory (warnings only).
func enforceContentPlan(plan *contracts.ContentPlan, artifact *contracts.EditorialArtifact, vres *contracts.VerificationResult, policy string) ([]runner.Warning, error) {
	if plan == nil {
		return nil, nil
	}
	if _, err := plan.CheckClaims(vres); err != nil {
		return nil, err
	}

	body := artifactText(artifact)
	var warnings []runner.Warning
	var failures []string

	for _, u := range plan.Units {
		if !u.Required {
			continue
		}
		if !u.Supported {
			if policy == recipes.MissingFail {
				failures = append(failures, fmt.Sprintf("required unit %s (%s) is not supported by the verified claims (policy: fail)", u.ID, u.Role))
			} else {
				warnings = append(warnings, runner.Warning{
					Code:    "plan.unit_unsupported",
					Message: fmt.Sprintf("required unit %s (%s) is not supported by the verified claims; written as unverified (policy: warn)", u.ID, u.Role),
				})
			}
			continue
		}
		if !planUnitPresent(u, body) {
			if policy == recipes.MissingFail {
				failures = append(failures, fmt.Sprintf("required unit %s (%s) is missing from the output (policy: fail)", u.ID, u.Role))
			} else {
				warnings = append(warnings, runner.Warning{
					Code:    "plan.unit_missing",
					Message: fmt.Sprintf("required unit %s (%s) does not appear in the output (policy: warn)", u.ID, u.Role),
				})
			}
		}
	}

	if len(failures) > 0 {
		return warnings, fmt.Errorf("content plan enforcement failed: %s", strings.Join(failures, "; "))
	}
	return warnings, nil
}

// planUnitPresent reports whether the artifact's text contains the unit:
// entity units are matched by entity name (lowercased), static units by
// their rendered title when it has content.
func planUnitPresent(u contracts.PlanUnit, bodyLower string) bool {
	if u.Entity != nil && u.Entity.Name != "" {
		return strings.Contains(bodyLower, strings.ToLower(u.Entity.Name))
	}
	if u.Title != "" {
		return strings.Contains(bodyLower, strings.ToLower(u.Title))
	}
	// No name, no title: the role label (if present) is the only signal.
	return u.Role == "" || strings.Contains(bodyLower, strings.ToLower(u.Role))
}

// artifactText concatenates every text surface of an artifact (title, body,
// section titles/bodies, post bodies) for plan-coverage checks.
func artifactText(a *contracts.EditorialArtifact) string {
	var b strings.Builder
	if a == nil {
		return ""
	}
	b.WriteString(a.Title)
	b.WriteString("\n")
	b.WriteString(a.Subtitle)
	b.WriteString("\n")
	b.WriteString(a.Body)
	for _, s := range a.Sections {
		b.WriteString(s.Title)
		b.WriteString("\n")
		b.WriteString(s.Body)
		b.WriteString("\n")
	}
	for _, p := range a.Posts {
		b.WriteString(p.Body)
		b.WriteString("\n")
	}
	return strings.ToLower(b.String())
}
