package recipes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// merge applies the documented precedence: the author layer overrides the
// common layer, field by field.
//
// Merge rules (deterministic, documented in docs/recipes-v0.6.md):
//   - identity fields (title, version, output_format, description): the
//     author value wins when set, else the common value;
//   - inputs: merged by name — an author input with the same name replaces
//     the common one; author-only and common-only inputs are both kept;
//   - source_policy: field-level — each author field wins when set (lists
//     and scalars replace; a zero-valued author field keeps the common one);
//   - research: instructions and required_facts CONCATENATE (common first,
//     then author) so an author recipe specializes without erasing shared
//     guidance; the entity spec and empty fields replace field-wise;
//   - blueprint: the author's units REPLACE the common ones wholesale
//     (structure is all-or-nothing — a partial structure merge would be
//     silently surprising); missing_policy and unit text fields follow the
//     field-level rule;
//   - examples: union (common first, then author), deduplicated.
//
// The author layer may declare only a SUBSET of the fields the common layer
// declares (that is how a specialization is written).
func merge(common, author *Recipe) *Recipe {
	if common == nil {
		return author
	}
	if author == nil {
		return common
	}
	out := *common
	if strings.TrimSpace(author.Title) != "" {
		out.Title = author.Title
	}
	if author.Version != "" {
		out.Version = author.Version
	}
	if author.OutputFormat != "" {
		out.OutputFormat = author.OutputFormat
	}
	if strings.TrimSpace(author.Description) != "" {
		out.Description = author.Description
	}

	// Inputs: merge by name (author wins per name).
	merged := make([]Input, 0, len(common.Inputs)+len(author.Inputs))
	authorByName := make(map[string]Input, len(author.Inputs))
	for _, in := range author.Inputs {
		authorByName[in.Name] = in
	}
	for _, in := range common.Inputs {
		if a, ok := authorByName[in.Name]; ok {
			merged = append(merged, a)
		} else {
			merged = append(merged, in)
		}
	}
	for _, in := range author.Inputs { // preserve author order for author-only
		found := false
		for _, m := range merged {
			if m.Name == in.Name {
				found = true
				break
			}
		}
		if !found {
			merged = append(merged, in)
		}
	}
	out.Inputs = merged

	// Source policy: field-level override.
	out.Source = mergeSourcePolicy(common.Source, author.Source)

	// Research: text fields concatenate, structure replaces.
	out.Research = mergeResearch(common.Research, author.Research)

	// Blueprint: units replace wholesale; policy is field-level.
	out.Blueprint = mergeBlueprint(common.Blueprint, author.Blueprint)

	// Examples: ordered union, deduplicated.
	seen := make(map[string]bool)
	ex := make([]string, 0, len(common.Examples)+len(author.Examples))
	for _, e := range append(append([]string{}, common.Examples...), author.Examples...) {
		if !seen[e] {
			seen[e] = true
			ex = append(ex, e)
		}
	}
	out.Examples = ex

	out.ID = common.ID // id is fixed by the file name; both layers must agree
	return &out
}

func mergeSourcePolicy(common, author *SourcePolicy) *SourcePolicy {
	if common == nil {
		return author
	}
	if author == nil {
		return common
	}
	out := *common
	if author.ExplicitURLs != "" {
		out.ExplicitURLs = author.ExplicitURLs
	}
	if len(author.PreferredDomains) > 0 {
		out.PreferredDomains = author.PreferredDomains
	}
	if len(author.RequiredDomains) > 0 {
		out.RequiredDomains = author.RequiredDomains
	}
	if len(author.QueryHints) > 0 {
		out.QueryHints = author.QueryHints
	}
	if author.MaxQueries > 0 {
		out.MaxQueries = author.MaxQueries
	}
	if author.MaxSources > 0 {
		out.MaxSources = author.MaxSources
	}
	if author.AllowEnrichment != nil {
		out.AllowEnrichment = author.AllowEnrichment
	}
	if author.DateFrom != "" {
		out.DateFrom = author.DateFrom
	}
	if author.DateTo != "" {
		out.DateTo = author.DateTo
	}
	return &out
}

func mergeResearch(common, author *ResearchSpec) *ResearchSpec {
	if common == nil {
		return author
	}
	if author == nil {
		return common
	}
	out := *common
	// Instructions concatenate (common first): the author layer specializes.
	if strings.TrimSpace(author.Instructions) != "" {
		out.Instructions = strings.TrimSpace(common.Instructions) + "\n\n" + strings.TrimSpace(author.Instructions)
	}
	if len(author.RequiredFacts) > 0 {
		out.RequiredFacts = append(append([]string{}, common.RequiredFacts...), author.RequiredFacts...)
	}
	if author.Entity != nil {
		out.Entity = author.Entity
	}
	return &out
}

func mergeBlueprint(common, author *Blueprint) *Blueprint {
	if common == nil {
		return author
	}
	if author == nil {
		return common
	}
	out := *common
	// Units replace wholesale (structure is all-or-nothing).
	if len(author.Units) > 0 {
		out.Units = author.Units
	}
	if author.MissingPolicy != "" {
		out.MissingPolicy = author.MissingPolicy
	}
	return &out
}

// Hash returns the SHA-256 hex digest of the recipe's canonical JSON
// encoding. It is the stable identity of a resolved recipe recorded in run
// manifests: any change to any field changes the hash.
func (r *Recipe) Hash() string {
	data, err := json.Marshal(r)
	if err != nil {
		// Unreachable for this struct (all fields are JSON-marshalable);
		// return a zero digest rather than panic in a validation path.
		return strings.Repeat("0", sha256SizeHex)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

const sha256SizeHex = 64
