package recipes

import (
	"regexp"
	"strings"
)

// Template rendering for recipe strings.
//
// Recipe text (query hints, search windows, unit titles) may reference
// values via {placeholder} templates. Rendering is deliberately simple and
// data-only: {name} and {name.field} lookups against a caller-supplied map.
// There is NO general template engine — no conditionals, no loops, no
// function calls — so a recipe cannot express logic, only substitutions.
// Unresolved placeholders are left in place verbatim (a search query with a
// leftover "{date}" is odd but harmless; an LLM reading it sees the intent).

var templateRe = regexp.MustCompile(`\{[a-zA-Z0-9_.]+\}`)

// RenderTemplate substitutes {placeholders} in tpl using vars. Placeholders
// whose key is absent are left unchanged.
func RenderTemplate(tpl string, vars map[string]string) string {
	if tpl == "" {
		return ""
	}
	return templateRe.ReplaceAllStringFunc(tpl, func(m string) string {
		key := strings.Trim(m, "{}")
		if v, ok := vars[key]; ok && v != "" {
			return v
		}
		return m
	})
}

// InputVars renders vars from resolved inputs: {name} for each input.
func (r *Recipe) InputVars(resolved []ResolvedInput) map[string]string {
	vars := make(map[string]string, len(resolved)*2)
	for _, in := range resolved {
		vars[in.Name] = in.Value
	}
	return vars
}

// EntityVars renders vars for one plan entity: {entity} (the display name),
// {entity.name}, and {entity.<field>} (mirrored as {<kind>.<field>}).
func EntityVars(kind, name string, fields map[string]string) map[string]string {
	vars := map[string]string{"entity": name, "entity.name": name}
	for k, v := range fields {
		vars[kind+"."+k] = v
		vars["entity."+k] = v
	}
	return vars
}
