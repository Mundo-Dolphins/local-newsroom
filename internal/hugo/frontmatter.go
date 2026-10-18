package hugo

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// fmField is one ordered front matter entry.
type fmField struct {
	key  string
	node *yaml.Node
}

// reservedValueRe matches strings that other YAML parsers may resolve to a
// non-string tag even though yaml.v3 would keep them plain: YAML 1.1
// boolean words (yes/no/on/off/y/n — Hugo's historical yaml.v2-based parser
// resolves these as booleans), YAML 1.1 radix integers, and YAML
// timestamp-shaped values. Such values are explicitly single-quoted so that
// every parser reads them as the exact string written.
var reservedValueRe = regexp.MustCompile(`(?i)^(?:y|n|yes|no|on|off|true|false|null|~|0[xob][0-9a-z]+)$`)

// yamlStringNode builds a !!str scalar node. The explicit tag plus the
// emitter's own safety analysis guarantee round-trip-correct quoting;
// reservedValueRe additionally forces a quote for values that yaml.v3
// considers plain but older YAML 1.1 parsers would retag (e.g. "yes").
func yamlStringNode(v string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
	if reservedValueRe.MatchString(v) {
		n.Style = yaml.SingleQuotedStyle
	}
	return n
}

func yamlBoolNode(v bool) *yaml.Node {
	value := "false"
	if v {
		value = "true"
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: value}
}

func yamlStringListNode(values []string) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	for _, v := range values {
		seq.Content = append(seq.Content, yamlStringNode(v))
	}
	return seq
}

// yamlMappingNode builds a mapping node from pairs, sorted by key for
// deterministic output.
func yamlMappingNode(pairs [][2]string) *yaml.Node {
	sorted := make([][2]string, len(pairs))
	copy(sorted, pairs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i][0] < sorted[j][0] })
	m := &yaml.Node{Kind: yaml.MappingNode}
	for _, p := range sorted {
		m.Content = append(m.Content, yamlStringNode(p[0]), yamlStringNode(p[1]))
	}
	return m
}

// buildFields assembles the front matter in canonical order:
//
//	title, date, author, description, canonical, source, tags, categories,
//	draft, newsroom (provenance, only when enabled), then config Defaults
//	(sorted, and only for fields no earlier source supplied).
//
// The resolved value of every field is also returned, for the required-field
// check and for diagnostics.
func (p *Publisher) buildFields(a Article, rel string, draft bool) ([]fmField, map[string]string, error) {
	var fields []fmField
	resolved := make(map[string]string)
	add := func(key string, node *yaml.Node, resolvedValue string) {
		fields = append(fields, fmField{key: key, node: node})
		resolved[key] = resolvedValue
	}

	add("title", yamlStringNode(a.Title), a.Title)
	dateStr := a.Date.Format(p.cfg.DateLayout)
	add("date", yamlStringNode(dateStr), dateStr)

	if a.Author != "" {
		add("author", yamlStringNode(a.Author), a.Author)
	} else if p.cfg.Author != "" {
		add("author", yamlStringNode(p.cfg.Author), p.cfg.Author)
	}
	if a.Summary != "" {
		add("description", yamlStringNode(a.Summary), a.Summary)
	}
	if p.cfg.BaseURL != "" {
		canonical := strings.TrimRight(p.cfg.BaseURL, "/") + "/" + rel
		add("canonical", yamlStringNode(canonical), canonical)
	}
	if a.SourceURL != "" {
		add("source", yamlStringNode(a.SourceURL), a.SourceURL)
	}
	if len(a.Tags) > 0 {
		add("tags", yamlStringListNode(a.Tags), strings.Join(a.Tags, ","))
	}
	if len(a.Categories) > 0 {
		add("categories", yamlStringListNode(a.Categories), strings.Join(a.Categories, ","))
	}
	add("draft", yamlBoolNode(draft), fmt.Sprintf("%t", draft))

	if p.cfg.IncludeProvenance {
		var pairs [][2]string
		if a.RunID != "" {
			pairs = append(pairs, [2]string{"run", a.RunID})
		}
		if a.Digest != "" {
			pairs = append(pairs, [2]string{"digest", a.Digest})
		}
		if len(pairs) > 0 {
			add("newsroom", yamlMappingNode(pairs), a.RunID)
		}
	}

	for _, key := range sortedKeys(p.cfg.Defaults) {
		if _, exists := resolved[key]; exists {
			continue // an explicit source already supplied the field
		}
		add(key, yamlStringNode(p.cfg.Defaults[key]), p.cfg.Defaults[key])
	}

	if err := p.checkRequired(resolved); err != nil {
		return nil, nil, err
	}
	return fields, resolved, nil
}

// checkRequired verifies that every config-required field (title and date
// are always present by construction) resolved to a non-empty value.
func (p *Publisher) checkRequired(resolved map[string]string) error {
	var missing []string
	for _, name := range sortedKeys2(p.cfgRequired()) {
		if strings.TrimSpace(resolved[name]) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: required front matter field(s) %s are missing or empty (supply them on the article or via config defaults)",
			ErrInvalidArticle, strings.Join(missing, ", "))
	}
	return nil
}

// cfgRequired returns the config's required fields (including the
// engine-mandatory title and date) in sorted order.
func (p *Publisher) cfgRequired() []string {
	names := make(map[string]bool, len(p.cfg.Required)+2)
	names["title"] = true
	names["date"] = true
	for _, n := range p.cfg.Required {
		names[n] = true
	}
	out := make([]string, 0, len(names))
	for n := range names {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedKeys2(ss []string) []string {
	out := make([]string, len(ss))
	copy(out, ss)
	sort.Strings(out)
	return out
}

// marshalFrontMatter serializes the fields for the configured front matter
// format and returns the bytes of the front matter block body (without the
// "---" fences). The output is deterministic: canonical field order, sorted
// map keys, stable quoting.
func marshalFrontMatter(format FrontMatterFormat, fields []fmField) ([]byte, error) {
	switch format {
	case FormatYAML, "":
		mapping := &yaml.Node{Kind: yaml.MappingNode}
		for _, f := range fields {
			key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: f.key}
			mapping.Content = append(mapping.Content, key, f.node)
		}
		var sb strings.Builder
		enc := yaml.NewEncoder(&sb)
		enc.SetIndent(2)
		if err := enc.Encode(mapping); err != nil {
			return nil, fmt.Errorf("marshaling front matter: %w", err)
		}
		if err := enc.Close(); err != nil {
			return nil, fmt.Errorf("marshaling front matter: %w", err)
		}
		return []byte(sb.String()), nil
	case FormatTOML, FormatJSON:
		return nil, fmt.Errorf("%w: %q is not implemented yet; use %q", ErrUnsupportedFormat, format, FormatYAML)
	default:
		return nil, fmt.Errorf("%w: unknown front matter format %q", ErrInvalidConfig, format)
	}
}
