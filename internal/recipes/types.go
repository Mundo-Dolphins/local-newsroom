// Package recipes implements the declarative editorial recipe contract (v0.6).
//
// A recipe is a profile-owned YAML document that presets a recurring authored
// format (a weekly feature, a thread series, a recurring column). A recipe
// constrains what research runs and what writing produces — never how the
// engine enforces factual safety:
//
//   - recipes contain no executable code and are never interpolated into a
//     shell or a template engine; they are declarative presets parsed by the
//     engine (typed, size-bounded, path-contained, secret-scanned);
//   - a recipe is NOT a plugin: the stage set, the quality gates, and the
//     final factual check are engine-owned and cannot be replaced or
//     disabled by a recipe;
//   - explicit run inputs (--input / --url) supply VALUES for a recipe's
//     declared inputs only; they cannot disable engine factual-safety
//     constraints or loosen the engine's source-policy floor.
//
// # Ownership and precedence
//
// Recipes live in the editorial/profile base tree, in two layers:
//
//	common/recipes/<id>.yaml            (shared recipes)
//	authors/<author>/recipes/<id>.yaml  (author layer: overrides/specializes)
//
// Resolution order is: engine recipe schema and safety checks, then the
// common layer, then the author layer for the run's author, then explicit
// run inputs (values only). See [Loader.Resolve] for the merge rules.
//
// # Contract
//
// A [Recipe] declares: identity (id, title, version, output format), typed
// inputs (name, type, required/default, constraints), a source policy
// (URL rules, domain preferences, search budgets), research instructions
// (bounded text plus an optional per-entity spec), an output blueprint
// (ordered semantic units, including per-entity repeated units), and
// optional example references. Every field is validated at load time; an
// invalid recipe fails the run before any stage executes.
package recipes

import (
	"sort"
	"strings"
)

// SchemaVersion is the recipe document schema version.
//
// Documents declare their schema explicitly and a loader only accepts this
// exact version: a half-understood contract is worse than an error.
const SchemaVersion = "0.6.0"

// Directory names inside the recipe base tree (the profiles base layout:
// <root>/common and <root>/authors/<author>).
const (
	// CommonDir holds shared material: <root>/common.
	CommonDir = "common"
	// AuthorsDir holds per-author material: <root>/authors/<author>.
	AuthorsDir = "authors"
	// RecipesDir is the recipes directory inside each layer base:
	// <root>/common/recipes and <root>/authors/<author>/recipes.
	RecipesDir = "recipes"
)

// Layer names recorded on a resolved recipe. The author layer is
// "author:<name>".
const (
	// LayerCommon is the shared recipe layer.
	LayerCommon = "common"
	// layerAuthorPrefix introduces the author layer name ("author:<name>").
	layerAuthorPrefix = "author:"
)

// File and text size bounds. A recipe is a small declarative document;
// generous bounds keep hostile or accidental blobs out of prompts and
// manifests while leaving room for real instruction text.
const (
	// MaxFileBytes bounds a single recipe file.
	MaxFileBytes = 64 * 1024

	// MaxRelPathLen bounds recipe-relative paths (examples).
	MaxRelPathLen = 256

	// MaxExampleFileBytes bounds one referenced example file. This mirrors
	// the profiles loader's example limit: examples are prompt material.
	MaxExampleFileBytes = 50 * 1024

	// MaxExampleTotalLines bounds the combined line count of all referenced
	// examples (profiles loader parity).
	MaxExampleTotalLines = 1000

	// MaxTextField bounds free-form instruction/prompt text fields.
	MaxTextField = 8192

	// MaxTitle, MaxDescription, MaxVersion bound identity fields.
	MaxTitle       = 120
	MaxDescription = 512
	MaxVersion     = 64

	// MaxFactText bounds one required-fact bullet.
	MaxFactText = 256

	// MaxDomainList bounds the domain lists of a source policy.
	MaxDomainList = 20

	// MaxQueryHint bounds one search query hint template.
	MaxQueryHint = 256

	// MaxUnits and MaxEntityFields bound the blueprint/entity structures.
	MaxUnits       = 32
	MaxEntityField = 32

	// EngineMaxQueries is the engine's hard ceiling on planned search
	// queries (planner config range). Recipe budgets clamp to it.
	EngineMaxQueries = 10

	// EngineMaxSources is the engine's hard ceiling on sources a run may
	// select. Recipe budgets clamp to it (a recipe can narrow, never
	// widen, the engine floor beyond this).
	EngineMaxSources = 50
)

// Recipe is the declarative contract of one editorial recipe.
//
// All fields are optional except Schema and ID (and Title); an empty recipe
// is still a valid *format preset* (output_format plus blueprint) with no
// inputs or source policy — but a recipe with no inputs, no source policy,
// and no blueprint carries nothing and is rejected.
type Recipe struct {
	// Schema is the document schema version; must equal [SchemaVersion].
	Schema string `yaml:"schema" json:"schema"`

	// ID is the recipe identifier. It must match the file name (without
	// extension) and uses a conservative identifier: [a-z0-9][a-z0-9-]*.
	ID string `yaml:"id" json:"id"`

	// Title is the human name of the format (used for run topic derivation
	// and display).
	Title string `yaml:"title" json:"title"`

	// Version is the recipe's own version string (free-form, bounded).
	Version string `yaml:"version,omitempty" json:"version,omitempty"`

	// OutputFormat presets the run output format: "article", "thread", or
	// "hybrid". An explicit --format flag or --hybrid/--article/--thread
	// run flags still win over the recipe preset.
	OutputFormat string `yaml:"output_format,omitempty" json:"output_format,omitempty"`

	// Description is a short human summary (shown by `newsroom recipes`).
	Description string `yaml:"description,omitempty" json:"description,omitempty"`

	// Inputs are the typed, validated run inputs the recipe declares.
	Inputs []Input `yaml:"inputs,omitempty" json:"inputs,omitempty"`

	// Source constrains how sources are obtained (see [SourcePolicy]).
	Source *SourcePolicy `yaml:"source_policy,omitempty" json:"source_policy,omitempty"`

	// Research constrains the research stage (see [ResearchSpec]).
	Research *ResearchSpec `yaml:"research,omitempty" json:"research,omitempty"`

	// Blueprint declares the ordered semantic units the output must carry
	// (see [Blueprint]).
	Blueprint *Blueprint `yaml:"blueprint,omitempty" json:"blueprint,omitempty"`

	// Examples are base-relative paths to example documents appended to the
	// writer prompt (profile-example size limits apply).
	Examples []string `yaml:"examples,omitempty" json:"examples,omitempty"`
}

// Input is one typed, validated recipe input.
type Input struct {
	// Name identifies the input for --input name=value. It uses the config
	// key character set so resolved inputs can be mirrored in the run's
	// config snapshot.
	Name string `yaml:"name" json:"name"`

	// Type is one of: string, int, date, week, url.
	Type string `yaml:"type" json:"type"`

	// Required marks the input as mandatory (no default).
	Required bool `yaml:"required" json:"required"`

	// Default is used when no value is provided (validated like a value).
	Default string `yaml:"default,omitempty" json:"default,omitempty"`

	// Description is the human help text.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`

	// Choices constrains string values to an enum.
	Choices []string `yaml:"choices,omitempty" json:"choices,omitempty"`

	// Pattern is a Go regular expression the value must match (string only).
	Pattern string `yaml:"pattern,omitempty" json:"pattern,omitempty"`

	// Min/Max: for int/week, numeric bounds; for string, length bounds.
	// For week the defaults are 1..17 when unset.
	Min *int `yaml:"min,omitempty" json:"min,omitempty"`
	Max *int `yaml:"max,omitempty" json:"max,omitempty"`
}

// Input types.
const (
	InputTypeString = "string"
	InputTypeInt    = "int"
	// InputTypeDate accepts YYYY-MM-DD or full RFC 3339 timestamps.
	InputTypeDate = "date"
	// InputTypeWeek is an integer week number (default range 1..17).
	InputTypeWeek = "week"
	// InputTypeURL accepts a single absolute http(s) URL.
	InputTypeURL = "url"
)

// SourcePolicy constrains how a run obtains sources. Every field only ADDS
// constraints; the engine's own fetch/security controls are untouched and
// budgets clamp to the engine ceilings ([EngineMaxQueries],
// [EngineMaxSources]).
type SourcePolicy struct {
	// ExplicitURLs is "required", "optional" (default), or "forbidden":
	// whether user-supplied --url values are accepted for the run.
	ExplicitURLs string `yaml:"explicit_urls,omitempty" json:"explicit_urls,omitempty"`

	// PreferredDomains sort candidates from these domains to the front.
	PreferredDomains []string `yaml:"preferred_domains,omitempty" json:"preferred_domains,omitempty"`

	// RequiredDomains restrict selected candidates to these domains
	// (suffix match, case-insensitive; ".nfl.com" matches all of it).
	RequiredDomains []string `yaml:"required_domains,omitempty" json:"required_domains,omitempty"`

	// QueryHints are search query templates ("{week} nfl schedule") rendered
	// against the resolved inputs; they seed discovery alongside any
	// planner-generated queries.
	QueryHints []string `yaml:"query_hints,omitempty" json:"query_hints,omitempty"`

	// MaxQueries bounds the planned search queries (clamped to
	// [EngineMaxQueries]).
	MaxQueries int `yaml:"max_queries,omitempty" json:"max_queries,omitempty"`

	// MaxSources bounds the selected sources (clamped to
	// [EngineMaxSources]).
	MaxSources int `yaml:"max_sources,omitempty" json:"max_sources,omitempty"`

	// AllowEnrichment (nil = engine default) gates the follow-up enrichment
	// stage for the run. A recipe can request enrichment; it can only be
	// used when search is actually configured (engine gate unchanged).
	AllowEnrichment *bool `yaml:"allow_enrichment,omitempty" json:"allow_enrichment,omitempty"`

	// DateFrom/DateTo are search window templates ("{date}") rendered
	// against the resolved inputs.
	DateFrom string `yaml:"date_from,omitempty" json:"date_from,omitempty"`
	DateTo   string `yaml:"date_to,omitempty" json:"date_to,omitempty"`
}

// ResearchSpec constrains the research stage: bounded instruction text,
// required facts, and an optional per-entity discovery spec. None of it can
// change what the verifier and final check enforce — it only steers which
// facts the researcher extracts.
type ResearchSpec struct {
	// Instructions is bounded text appended to the researcher prompt
	// (injection-scanned; the engine's core rules are not overridable).
	Instructions string `yaml:"instructions,omitempty" json:"instructions,omitempty"`

	// RequiredFacts are per-item fact requirements ("one stat per entity").
	RequiredFacts []string `yaml:"required_facts,omitempty" json:"required_facts,omitempty"`

	// Entity is the per-entity discovery spec (nil = no entity model).
	Entity *EntitySpec `yaml:"entity,omitempty" json:"entity,omitempty"`
}

// EntitySpec declares the kind of thing a recipe researches per-unit
// (a player, a team, a game, a draft pick...). The engine stays
// domain-agnostic: Kind is a semantic label the LLM reasons with, and the
// only engine behavior it drives is unit cardinality.
type EntitySpec struct {
	// Kind is the semantic entity label (e.g. "player").
	Kind string `yaml:"kind" json:"kind"`

	// NameField is the entity field holding its display name
	// (default "name").
	NameField string `yaml:"name_field,omitempty" json:"name_field,omitempty"`

	// Fields are the semantic fields expected per entity.
	Fields []string `yaml:"fields,omitempty" json:"fields,omitempty"`

	// GroupBy is an optional grouping field (must be one of Fields).
	GroupBy string `yaml:"group_by,omitempty" json:"group_by,omitempty"`

	// MinCount is the minimum number of entities the run should surface
	// (0 = no minimum). MaxCount 0 = engine default bound.
	MinCount int `yaml:"min_count,omitempty" json:"min_count,omitempty"`
	MaxCount int `yaml:"max_count,omitempty" json:"max_count,omitempty"`
}

// Blueprint declares the ordered semantic units the output must carry.
// A unit is a semantic block (intro, one per entity, summary, ...) that the
// writer must realize with its own structure (posts, sections) — the engine
// never hardcodes a format's sections.
type Blueprint struct {
	// MissingPolicy is "warn" (default) or "fail": what happens when a
	// required unit cannot be supported by the verified evidence. The
	// engine never fabricates content to satisfy a unit.
	MissingPolicy string `yaml:"missing_policy,omitempty" json:"missing_policy,omitempty"`

	// Units is the ordered list of units (1..[MaxUnits]).
	Units []BlueprintUnit `yaml:"units,omitempty" json:"units,omitempty"`
}

// Missing policy values.
const (
	MissingWarn = "warn"
	MissingFail = "fail"
)

// BlueprintUnit is one semantic unit of the output.
type BlueprintUnit struct {
	// Role is the unit's semantic role ("intro", "entity", "summary", ...).
	Role string `yaml:"role" json:"role"`

	// Title is a display label template; {input} and {entity.field}
	// placeholders are rendered against the resolved plan.
	Title string `yaml:"title,omitempty" json:"title,omitempty"`

	// Prompt is unit-specific writing instruction text (bounded, scanned).
	Prompt string `yaml:"prompt,omitempty" json:"prompt,omitempty"`

	// Required marks the unit mandatory when the run has a blueprint.
	Required bool `yaml:"required" json:"required"`

	// Repeat names the entity kind this unit repeats over (one unit per
	// discovered entity). It must equal the recipe's Research.Entity.Kind
	// when set.
	Repeat string `yaml:"repeat,omitempty" json:"repeat,omitempty"`

	// Fields are the semantic fields a repeated unit must fill for each
	// entity (e.g. workload stats).
	Fields []string `yaml:"fields,omitempty" json:"fields,omitempty"`
}

// Resolved is a recipe after layer merging — the single, deterministic
// document a run executes. Its hash is recorded in the run manifest and the
// full document is persisted in the run directory, so a resumed run always
// uses the recipe it started with.
type Resolved struct {
	// Recipe is the merged contract.
	Recipe *Recipe `json:"recipe"`

	// Layers are the base-relative files that contributed, common first.
	Layers []string `json:"layers"`

	// Root is the absolute base tree the recipes were loaded from.
	Root string `json:"root"`

	// Author is the author layer used ("" = common only).
	Author string `json:"author,omitempty"`

	// Hash is the SHA-256 hex digest of the merged recipe (canonical JSON).
	Hash string `json:"hash"`

	// Examples are the example documents resolved to base-relative paths
	// (validated: containment + size + line limits) and their content.
	Examples []Example `json:"examples,omitempty"`
}

// Example is one referenced example document with its (size-checked) content.
type Example struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Lines    int    `json:"lines"`
	Bytes    int    `json:"bytes"`
	HasImage bool   `json:"has_image,omitempty"`
}

// Summary describes a recipe for listing purposes (no full body).
type Summary struct {
	// ID is the recipe identifier.
	ID string `json:"id"`
	// Title is the human name.
	Title string `json:"title"`
	// Description is the short summary.
	Description string `json:"description,omitempty"`
	// Version is the recipe version ("" if unset).
	Version string `json:"version,omitempty"`
	// OutputFormat is the preset output format ("" if unset).
	OutputFormat string `json:"output_format,omitempty"`
	// Layer is which layer the summary came from (common / author:<name>).
	Layer string `json:"layer"`
	// Inputs lists the declared inputs (name/type/required).
	Inputs []Input `json:"inputs,omitempty"`
}

// ResolvedInput is one declared input with its resolved run value.
type ResolvedInput struct {
	Input
	// Value is the resolved (defaulted or provided) value.
	Value string `json:"value"`
	// Provided reports whether the run supplied a value (vs default).
	Provided bool `json:"provided"`
}

// Topic derives a deterministic run topic for a recipe run that did not
// specify one: the recipe title plus the resolved inputs (sorted). Recipes
// make runs topic-optional, not the other way around.
func (r *Recipe) Topic(resolved []ResolvedInput) string {
	base := r.Title
	if base == "" {
		base = r.ID
	}
	if len(resolved) == 0 {
		return base
	}
	sorted := make([]ResolvedInput, len(resolved))
	copy(sorted, resolved)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	var parts []string
	for _, in := range sorted {
		parts = append(parts, in.Name+"="+in.Value)
	}
	return base + " — " + strings.Join(parts, ", ")
}
