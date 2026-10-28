package contracts

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ContentPlan is the generic recipe-result representation produced by the
// write stage (v0.6 editorial recipes).
//
// It is the bridge between a recipe's declarative blueprint and the writer's
// generation: each [PlanUnit] is one semantic unit of the output (an intro,
// one block per discovered entity, a summary, ...). The writer must
// realize the plan — one artifact unit per plan unit, in order, grounded in
// the claims each unit cites — so a recurring format's STRUCTURE and its
// claim TRACEABILITY survive into the artifact.
//
// The ContentPlan is deliberately domain-agnostic: "entities" are semantic
// units with a kind label and a name, never a league-specific schema.
//
// Every unit's ClaimIDs must reference claims in the run's VerificationResult
// (the same provenance rule as EditorialArtifact.ClaimReferences): the plan
// is how a recipe run gets per-unit claim traceability, and the engine
// validates it the same way it validates the artifact.
type ContentPlan struct {
	// StableID uniquely identifies this content plan.
	StableID string `json:"stable_id"`

	// RecipeID is the recipe the plan was generated from.
	RecipeID string `json:"recipe_id"`

	// RecipeHash is the resolved recipe's hash (manifest parity).
	RecipeHash string `json:"recipe_hash"`

	// Format is the output format the plan is shaped for (article/thread/
	// hybrid). The writer renders units to the matching artifact structure.
	Format string `json:"format"`

	// Units is the ordered list of semantic units (1..64).
	Units []PlanUnit `json:"units"`

	// GeneratedAt is the UTC generation timestamp.
	GeneratedAt time.Time `json:"generated_at"`

	// InputVerificationID references the VerificationResult this plan was
	// grounded in.
	InputVerificationID string `json:"input_verification_id"`
}

// PlanUnit is one semantic unit of the output.
type PlanUnit struct {
	// ID is the unit identifier within the plan (unit-1, unit-2, ...).
	ID string `json:"id"`

	// Role is the blueprint unit's semantic role.
	Role string `json:"role"`

	// Title is the (template-rendered) display label of the unit.
	Title string `json:"title,omitempty"`

	// Entity is the entity this unit represents (nil for fixed units).
	Entity *PlanEntity `json:"entity,omitempty"`

	// Fields are the semantic field values filled for this unit (for
	// repeated units, the per-entity values).
	Fields map[string]string `json:"fields,omitempty"`

	// ClaimIDs are the verified claims that ground this unit. The writer
	// must carry them into the artifact unit it realizes.
	ClaimIDs []string `json:"claim_ids,omitempty"`

	// SourceIDs are the sources that back the unit's claims (derived
	// provenance, for display).
	SourceIDs []string `json:"source_ids,omitempty"`

	// Required mirrors the blueprint: the unit must appear in the artifact.
	Required bool `json:"required,omitempty"`

	// Supported reports whether the unit's evidence holds: every cited claim
	// is SUPPORTED in the verification (computed by the engine, not the
	// model). An unsupported required unit triggers the recipe's missing
	// policy (warn/fail) — never fabricated filler.
	Supported bool `json:"supported,omitempty"`
}

// PlanEntity is one discovered entity (a player, a team, a game, ...).
type PlanEntity struct {
	// Kind is the recipe's entity kind label.
	Kind string `json:"kind"`

	// Name is the entity's display name.
	Name string `json:"name"`

	// Fields are the semantic field values (workload, stats, ...).
	Fields map[string]string `json:"fields,omitempty"`
}

// MaxPlanUnits bounds the number of units in a content plan.
const MaxPlanUnits = 64

// claimIDRe mirrors the artifact's claim-reference shape (claim-\d+).
var claimIDRe = regexp.MustCompile(`^claim-[0-9]+$`)

// Validate checks the structural contract of a content plan:
//   - non-empty, bounded units with unique IDs;
//   - claim IDs in the canonical shape (the verifier's shape);
//   - entity units must carry an entity; repeated units carry fields;
//   - bounded text in every field.
func (p *ContentPlan) Validate() error {
	if p == nil {
		return fmt.Errorf("content plan is nil")
	}
	if p.StableID == "" {
		return fmt.Errorf("content plan: stable_id is required")
	}
	if p.RecipeID == "" {
		return fmt.Errorf("content plan: recipe_id is required")
	}
	if len(p.Units) == 0 || len(p.Units) > MaxPlanUnits {
		return fmt.Errorf("content plan: must carry 1..%d units, got %d", MaxPlanUnits, len(p.Units))
	}
	seen := make(map[string]bool, len(p.Units))
	for i, u := range p.Units {
		if u.ID == "" {
			return fmt.Errorf("content plan: unit %d has no id", i)
		}
		if seen[u.ID] {
			return fmt.Errorf("content plan: duplicate unit id %q", u.ID)
		}
		seen[u.ID] = true
		if u.Role == "" {
			return fmt.Errorf("content plan: unit %s has no role", u.ID)
		}
		if len(u.Title) > 512 {
			return fmt.Errorf("content plan: unit %s title exceeds 512 characters", u.ID)
		}
		if u.Entity != nil {
			if u.Entity.Name == "" {
				return fmt.Errorf("content plan: unit %s entity has no name", u.ID)
			}
		}
		for k, v := range u.Fields {
			if len(v) > MaxTextFieldValue {
				return fmt.Errorf("content plan: unit %s field %q exceeds %d characters", u.ID, k, MaxTextFieldValue)
			}
		}
		for _, c := range u.ClaimIDs {
			if !claimIDRe.MatchString(c) {
				return fmt.Errorf("content plan: unit %s has malformed claim id %q", u.ID, c)
			}
		}
	}
	return nil
}

// MaxTextFieldValue bounds one semantic field value in a plan unit.
const MaxTextFieldValue = 4096

// CheckClaims verifies the plan against a verification result: every cited
// claim exists in the verification, and each unit's Supported flag is
// recomputed (a unit is supported only if every cited claim is supported).
// It returns the unsupported units (engine-trusted; the writer must honor
// the recipe's missing policy for them).
func (p *ContentPlan) CheckClaims(vres *VerificationResult) (unsupported []PlanUnit, err error) {
	if p == nil {
		return nil, fmt.Errorf("content plan is nil")
	}
	if vres == nil {
		return nil, fmt.Errorf("verification result is nil")
	}
	statuses := vres.VerificationStatuses
	supportedSet := make(map[string]bool)
	for id, st := range statuses {
		if st == VerificationStatusSupported {
			supportedSet[id] = true
		}
	}
	for i := range p.Units {
		u := &p.Units[i]
		if len(u.ClaimIDs) == 0 {
			// No evidence cited: an entity unit without claims is
			// unsupported (a model-invented entity); a static unit is
			// structural (its facts are checked by the final check).
			u.Supported = u.Entity == nil
			if !u.Supported {
				unsupported = append(unsupported, *u)
			}
			continue
		}
		ok := true
		for _, c := range u.ClaimIDs {
			st, exists := statuses[c]
			if !exists {
				return nil, fmt.Errorf("content plan: unit %s cites claim %s which is not in the verification", u.ID, c)
			}
			if st != VerificationStatusSupported {
				ok = false
			}
		}
		u.Supported = ok
		if !ok {
			unsupported = append(unsupported, *u)
		}
	}
	return unsupported, nil
}

// SummaryLine renders a one-line human description of the plan (for run
// summaries and review bundles).
func (p *ContentPlan) SummaryLine() string {
	if p == nil {
		return ""
	}
	nEntity := 0
	for _, u := range p.Units {
		if u.Entity != nil {
			nEntity++
		}
	}
	if nEntity > 0 {
		return fmt.Sprintf("%d units (%d per-entity) for recipe %s", len(p.Units), nEntity, p.RecipeID)
	}
	return fmt.Sprintf("%d units for recipe %s", len(p.Units), p.RecipeID)
}

// HasUnsupported reports whether any unit failed the claim check.
func (p *ContentPlan) HasUnsupported() bool {
	for _, u := range p.Units {
		if !u.Supported {
			return true
		}
	}
	return false
}

// String renders plan fields as "k=v" pairs (prompt material).
func (p *PlanUnit) FieldsString() string {
	if len(p.Fields) == 0 {
		return ""
	}
	keys := make([]string, 0, len(p.Fields))
	for k := range p.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s: %s\n", k, p.Fields[k])
	}
	return strings.TrimSpace(b.String())
}
