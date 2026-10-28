package recipes

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Conservative identifier shapes.
var (
	// idRe: kebab-case identifiers (they must also equal the file name).
	idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	// inputNameRe: the config key character set, so resolved inputs can be
	// mirrored in the run config snapshot (run.SetSnapshotValue rules).
	inputNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)
	// versionRe: free-form bounded version strings (semver-like).
	versionRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	// kindRe: entity kind labels (lowercase semantic words).
	kindRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	// fieldNameRe: semantic field names.
	fieldNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	// roleRe: blueprint unit roles.
	roleRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	// domainRe: a domain or domain suffix (".nfl.com").
	domainRe = regexp.MustCompile(`^\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
)

// invalidRecipeError is returned for structurally invalid recipes. Every
// validation failure carries the field it concerns so authors fix the file
// (or the run fails fast with an actionable message).
func invalid(field, format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf("%s: %s", field, fmt.Sprintf(format, args...)))
}

// Validate checks the full structural contract of a recipe document (schema
// version, identifiers, bounds, cross-field rules, and the whole-document
// requirements: a title, and at least one constraint). It is the check a
// RUN executes — the merged document must be complete.
func (r *Recipe) Validate() error { return r.validate(true) }

// ValidateLayer checks one layer file in isolation: everything Validate
// checks except the whole-document requirements. An override file may set
// only the fields it wants to change, so a title-less or constraint-less
// half-declaration is legal on its own — the merged document must be whole
// (the loader re-runs Validate on the merge result).
func (r *Recipe) ValidateLayer() error { return r.validate(false) }

func (r *Recipe) validate(strict bool) error {
	if r == nil {
		return invalid("recipe", "recipe is nil")
	}
	if r.Schema != SchemaVersion {
		return invalid("schema", "unsupported recipe schema %q (want %q)", r.Schema, SchemaVersion)
	}
	if !idRe.MatchString(r.ID) {
		return invalid("id", "must be kebab-case ([a-z0-9][a-z0-9-]*, max 64 chars), got %q", r.ID)
	}
	if strict && strings.TrimSpace(r.Title) == "" {
		return invalid("title", "title is required")
	}
	if len(r.Title) > MaxTitle {
		return invalid("title", "exceeds %d characters", MaxTitle)
	}
	if r.Version != "" && !versionRe.MatchString(r.Version) {
		return invalid("version", "must be a bounded identifier, got %q", r.Version)
	}
	switch r.OutputFormat {
	case "", "article", "thread", "hybrid":
	default:
		return invalid("output_format", "must be one of article|thread|hybrid, got %q", r.OutputFormat)
	}
	if len(r.Description) > MaxDescription {
		return invalid("description", "exceeds %d characters", MaxDescription)
	}
	if err := validateInputs(r.ID, r.Inputs); err != nil {
		return err
	}
	if r.Source != nil {
		if err := validateSourcePolicy(r.ID, r.Source); err != nil {
			return err
		}
	}
	if r.Research != nil {
		if err := validateResearch(r.ID, r.Research); err != nil {
			return err
		}
	}
	if r.Blueprint != nil {
		if err := validateBlueprint(r.ID, r.Research, r.Blueprint); err != nil {
			return err
		}
	}
	if err := validateExamples(r.ID, r.Examples); err != nil {
		return err
	}
	// A merged recipe must carry at least one constraint to be a recipe at
	// all (a layer file may declare none, because it inherits the rest).
	if strict && len(r.Inputs) == 0 && r.Source == nil && r.Research == nil && r.Blueprint == nil && r.Examples == nil && r.OutputFormat == "" {
		return invalid("recipe", "recipe declares no inputs, source policy, research spec, blueprint, examples, or output format")
	}
	return nil
}

func validateInputs(id string, inputs []Input) error {
	seen := make(map[string]bool, len(inputs))
	for i, in := range inputs {
		if !inputNameRe.MatchString(in.Name) {
			return invalid(fmt.Sprintf("inputs[%d].name", i), "must be a config-key identifier, got %q", in.Name)
		}
		if seen[in.Name] {
			return invalid("inputs", "duplicate input name %q", in.Name)
		}
		seen[in.Name] = true
		if err := validateInputDef(id, in); err != nil {
			return err
		}
	}
	return nil
}

// validateInputDef checks one input definition (type, constraints, default).
func validateInputDef(id string, in Input) error {
	name := in.Name
	switch in.Type {
	case InputTypeString, InputTypeInt, InputTypeDate, InputTypeWeek, InputTypeURL:
	default:
		return invalid("inputs."+name+".type", "must be one of string|int|date|week|url, got %q", in.Type)
	}
	if in.Type == InputTypeString {
		if len(in.Choices) > 32 {
			return invalid("inputs."+name+".choices", "at most 32 choices")
		}
	}
	if in.Pattern != "" {
		if in.Type != InputTypeString {
			return invalid("inputs."+name+".pattern", "patterns are only supported for string inputs")
		}
		if _, err := regexp.Compile(in.Pattern); err != nil {
			return invalid("inputs."+name+".pattern", "invalid regular expression: %v", err)
		}
	}
	if in.Min != nil && in.Max != nil && *in.Min > *in.Max {
		return invalid("inputs."+name, "min %d exceeds max %d", *in.Min, *in.Max)
	}
	// Defaults must pass the same validation as values.
	if in.Default != "" {
		if _, err := coerceValue(in, in.Default); err != nil {
			return invalid("inputs."+name+".default", "%s", err.Error())
		}
	}
	if in.Required && in.Default != "" {
		// Allowed (required-with-fallback) but pointless; keep it legal.
		_ = in.Required
	}
	return nil
}

func validateSourcePolicy(id string, p *SourcePolicy) error {
	switch p.ExplicitURLs {
	case "", "required", "optional", "forbidden":
	default:
		return invalid("source_policy.explicit_urls", "must be one of required|optional|forbidden, got %q", p.ExplicitURLs)
	}
	if len(p.PreferredDomains) > MaxDomainList {
		return invalid("source_policy.preferred_domains", "at most %d domains", MaxDomainList)
	}
	for _, d := range p.PreferredDomains {
		if !domainRe.MatchString(d) {
			return invalid("source_policy.preferred_domains", "invalid domain %q (use e.g. \"en.nfl.com\" or \".nfl.com\")", d)
		}
	}
	if len(p.RequiredDomains) > MaxDomainList {
		return invalid("source_policy.required_domains", "at most %d domains", MaxDomainList)
	}
	for _, d := range p.RequiredDomains {
		if !domainRe.MatchString(d) {
			return invalid("source_policy.required_domains", "invalid domain %q (use e.g. \"en.nfl.com\" or \".nfl.com\")", d)
		}
	}
	if len(p.QueryHints) > EngineMaxQueries {
		return invalid("source_policy.query_hints", "at most %d hints", EngineMaxQueries)
	}
	for _, h := range p.QueryHints {
		if len(h) > MaxQueryHint {
			return invalid("source_policy.query_hints", "hint exceeds %d characters: %q", MaxQueryHint, h)
		}
		if err := scanInstructionText("source_policy.query_hints", h); err != nil {
			return err
		}
	}
	if p.MaxQueries < 0 || p.MaxQueries > EngineMaxQueries {
		return invalid("source_policy.max_queries", "must be 0..%d (engine ceiling), got %d", EngineMaxQueries, p.MaxQueries)
	}
	if p.MaxSources < 0 || p.MaxSources > EngineMaxSources {
		return invalid("source_policy.max_sources", "must be 0..%d (engine ceiling), got %d", EngineMaxSources, p.MaxSources)
	}
	for _, t := range []string{p.DateFrom, p.DateTo} {
		if len(t) > 64 {
			return invalid("source_policy.date_from/date_to", "exceeds 64 characters")
		}
	}
	return nil
}

func validateResearch(id string, rs *ResearchSpec) error {
	if len(rs.Instructions) > MaxTextField {
		return invalid("research.instructions", "exceeds %d characters", MaxTextField)
	}
	if err := scanInstructionText("research.instructions", rs.Instructions); err != nil {
		return err
	}
	if len(rs.RequiredFacts) > 20 {
		return invalid("research.required_facts", "at most 20 facts")
	}
	for _, f := range rs.RequiredFacts {
		if len(f) > MaxFactText {
			return invalid("research.required_facts", "a fact exceeds %d characters: %q", MaxFactText, f)
		}
		if err := scanInstructionText("research.required_facts", f); err != nil {
			return err
		}
	}
	if rs.Entity != nil {
		if !kindRe.MatchString(rs.Entity.Kind) {
			return invalid("research.entity.kind", "must be a lowercase label, got %q", rs.Entity.Kind)
		}
		if len(rs.Entity.NameField) > 64 {
			return invalid("research.entity.name_field", "exceeds 64 characters")
		}
		if rs.Entity.MinCount < 0 || rs.Entity.MinCount > EngineMaxSources {
			return invalid("research.entity.min_count", "must be 0..%d, got %d", EngineMaxSources, rs.Entity.MinCount)
		}
		if rs.Entity.MaxCount < 0 || rs.Entity.MaxCount > EngineMaxSources {
			return invalid("research.entity.max_count", "must be 0..%d, got %d", EngineMaxSources, rs.Entity.MaxCount)
		}
		if rs.Entity.MinCount > 0 && rs.Entity.MaxCount > 0 && rs.Entity.MinCount > rs.Entity.MaxCount {
			return invalid("research.entity", "min_count %d exceeds max_count %d", rs.Entity.MinCount, rs.Entity.MaxCount)
		}
		if err := validateFieldNames("research.entity.fields", rs.Entity.Fields); err != nil {
			return err
		}
		if rs.Entity.GroupBy != "" {
			found := false
			for _, f := range rs.Entity.Fields {
				if f == rs.Entity.GroupBy {
					found = true
				}
			}
			if !found {
				return invalid("research.entity.group_by", "must be one of the entity fields, got %q", rs.Entity.GroupBy)
			}
		}
	}
	return nil
}

func validateBlueprint(id string, rs *ResearchSpec, bp *Blueprint) error {
	switch bp.MissingPolicy {
	case "", MissingWarn, MissingFail:
	default:
		return invalid("blueprint.missing_policy", "must be warn|fail, got %q", bp.MissingPolicy)
	}
	if len(bp.Units) == 0 || len(bp.Units) > MaxUnits {
		return invalid("blueprint.units", "must declare 1..%d units, got %d", MaxUnits, len(bp.Units))
	}
	for i, u := range bp.Units {
		if !roleRe.MatchString(u.Role) {
			return invalid(fmt.Sprintf("blueprint.units[%d].role", i), "must be a lowercase label, got %q", u.Role)
		}
		if len(u.Title) > MaxQueryHint {
			return invalid(fmt.Sprintf("blueprint.units[%d].title", i), "exceeds %d characters", MaxQueryHint)
		}
		if len(u.Prompt) > MaxTextField {
			return invalid(fmt.Sprintf("blueprint.units[%d].prompt", i), "exceeds %d characters", MaxTextField)
		}
		if err := scanInstructionText(fmt.Sprintf("blueprint.units[%d].prompt", i), u.Prompt); err != nil {
			return err
		}
		if u.Repeat != "" {
			if rs == nil || rs.Entity == nil {
				return invalid(fmt.Sprintf("blueprint.units[%d].repeat", i), "repeats entities but the recipe declares no research.entity")
			}
			if u.Repeat != rs.Entity.Kind {
				return invalid(fmt.Sprintf("blueprint.units[%d].repeat", i), "repeats %q but the entity kind is %q", u.Repeat, rs.Entity.Kind)
			}
			if err := validateFieldNames(fmt.Sprintf("blueprint.units[%d].fields", i), u.Fields); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateFieldNames(field string, fields []string) error {
	if len(fields) > MaxEntityField {
		return invalid(field, "at most %d fields", MaxEntityField)
	}
	for _, f := range fields {
		if !fieldNameRe.MatchString(f) {
			return invalid(field, "invalid field name %q", f)
		}
	}
	return nil
}

func validateExamples(id string, examples []string) error {
	if len(examples) > 8 {
		return invalid("examples", "at most 8 examples")
	}
	for _, e := range examples {
		if e == "" || strings.Contains(e, "\x00") {
			return invalid("examples", "empty example path")
		}
		if filepathIsAbs(e) || containsDotDot(e) {
			return invalid("examples", "path must be base-relative without \"..\" segments: %q", e)
		}
		if len(e) > MaxRelPathLen {
			return invalid("examples", "path exceeds %d characters: %q", MaxRelPathLen, e)
		}
	}
	return nil
}

// safeFile reads a base-relative file with the same containment rules as the
// editorial bundle loader: relative, bounded, inside the root, size-capped.
func safeFile(root, rel string, maxBytes int64) ([]byte, error) {
	if rel == "" || filepathIsAbs(rel) || containsDotDot(rel) {
		return nil, fmt.Errorf("%w: %s", ErrTraversal, rel)
	}
	rel = filepath.Clean(rel)
	if len(rel) > MaxRelPathLen {
		return nil, fmt.Errorf("%w: %s exceeds %d characters", ErrTraversal, rel, MaxRelPathLen)
	}
	abs := filepath.Join(root, rel)
	prefix := root + string(filepath.Separator)
	if !strings.HasPrefix(abs, prefix) && abs != root {
		return nil, fmt.Errorf("%w: %s", ErrTraversal, rel)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%w: %s is a directory", ErrNotFound, rel)
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("%w: %s (size %d, limit %d)", ErrTooLarge, rel, info.Size(), maxBytes)
	}
	data, err := os.ReadFile(abs) // #nosec G304 -- validated containment above
	if err != nil {
		return nil, err
	}
	return data, nil
}

// dateValue reports whether v parses as a date input (YYYY-MM-DD or
// RFC 3339).
func dateValue(v string) bool {
	v = strings.TrimSpace(v)
	if _, err := time.Parse("2006-01-02", v); err == nil {
		return true
	}
	if _, err := time.Parse(time.RFC3339, v); err == nil {
		return true
	}
	return false
}

// urlValue reports whether v is an absolute http(s) URL.
func urlValue(v string) bool {
	u, err := url.Parse(strings.TrimSpace(v))
	if err != nil {
		return false
	}
	if u.Host == "" || u.Path == "" && u.Hostname() == "" {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// filepathIsAbs/containsDotDot duplicate (intentionally) the small path
// helpers of the editorial/profiles loaders so this package stays
// self-contained.
func filepathIsAbs(p string) bool {
	return strings.HasPrefix(p, "/") || (len(p) > 1 && p[1] == ':')
}

func containsDotDot(p string) bool {
	for _, segment := range strings.FieldsFunc(p, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if segment == ".." {
			return true
		}
	}
	return false
}
