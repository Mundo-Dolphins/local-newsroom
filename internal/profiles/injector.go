package profiles

import (
	"fmt"
	"strings"
)

// Injector provides a way to inject profile instructions into prompts or contexts.
//
// This interface allows the writer and other components to consume profile data
// in a structured way.
type Injector interface {
	// InjectWriterInstructions returns the complete writer instruction set
	// derived from the loaded profile.
	InjectWriterInstructions() string

	// InjectFactRules returns only the factual safety rules.
	InjectFactRules() string

	// InjectStyleRules returns only the style constraints.
	InjectStyleRules() string

	// InjectVocabularyRules returns only the vocabulary constraints.
	InjectVocabularyRules() string

	// InjectOutputFormat returns only the output format instructions.
	InjectOutputFormat() string

	// GetExamples returns the available examples.
	GetExamples() map[string]string

	// GetProfile returns the complete profile.
	GetProfile() Profile
}

// profileInjector implements Injector using a Profile.
type profileInjector struct {
	profile Profile
}

// NewInjector creates a new injector from a loaded profile.
func NewInjector(profile Profile) Injector {
	return &profileInjector{profile: profile}
}

// InjectWriterInstructions returns the complete instruction set for the writer stage.
//
// The instructions are structured to clearly separate:
// 1. Core factual safety rules (immutable)
// 2. Style constraints (merge of common + author)
// 3. Output format (if provided)
// 4. Examples (if available)
func (i *profileInjector) InjectWriterInstructions() string {
	var sections []string

	// Core facts - always included first
	sections = append(sections, i.InjectFactRules())

	// Style rules
	if i.profile.Style != "" {
		sections = append(sections, i.InjectStyleRules())
	}

	// Output format (if overridden or loaded)
	if i.profile.OutputFormat != "" {
		sections = append(sections, i.InjectOutputFormat())
	}

	// Examples (if available)
	if len(i.profile.Examples) > 0 {
		sections = append(sections, i.InjectExamples())
	}

	return strings.Join(sections, "\n\n---\n\n")
}

// InjectFactRules returns only the factual safety rules.
func (i *profileInjector) InjectFactRules() string {
	return fmt.Sprintf(`## Factual Safety Rules

%s`, i.profile.CoreFactsPolicy)
}

// InjectStyleRules returns only the style constraints.
func (i *profileInjector) InjectStyleRules() string {
	return fmt.Sprintf(`## Writing Style

%s`, i.profile.Style)
}

// InjectVocabularyRules returns only the vocabulary constraints.
func (i *profileInjector) InjectVocabularyRules() string {
	return fmt.Sprintf(`## Vocabulary Constraints

%s`, i.profile.Vocabulary)
}

// InjectOutputFormat returns only the output format instructions.
func (i *profileInjector) InjectOutputFormat() string {
	return fmt.Sprintf(`## Output Format Instructions

%s`, i.profile.OutputFormat)
}

// InjectExamples returns examples section if available.
func (i *profileInjector) InjectExamples() string {
	var sb strings.Builder

	sb.WriteString("## Examples\n\n")
	sb.WriteString("The following examples illustrate desired output format:\n\n")

	for path, content := range i.profile.Examples {
		fmt.Fprintf(&sb, "### %s\n\n", path)
		sb.WriteString("```\n")
		sb.WriteString(content)
		sb.WriteString("\n```\n\n")
	}

	return sb.String()
}

// GetExamples returns the available examples.
func (i *profileInjector) GetExamples() map[string]string {
	return i.profile.Examples
}

// GetProfile returns the complete profile.
func (i *profileInjector) GetProfile() Profile {
	return i.profile
}

// CompositeInjector allows injecting multiple profile sources.
//
// This is useful when you want to layer profiles from different sources
// (e.g., a base profile + per-topic overrides).
type CompositeInjector struct {
	injectors []Injector
}

// NewCompositeInjector creates a new composite injector from multiple sources.
//
// Later injectors take precedence over earlier ones for style/content,
// but core facts are always preserved.
func NewCompositeInjector(injectors ...Injector) *CompositeInjector {
	return &CompositeInjector{
		injectors: injectors,
	}
}

// InjectWriterInstructions injects from all sources, merging appropriately.
func (c *CompositeInjector) InjectWriterInstructions() string {
	var coreFacts []string
	var styles []string
	var outputFormats []string
	var allExamples map[string]string

	// Collect from all injectors
	for _, inj := range c.injectors {
		profile := inj.GetProfile()

		// Core facts are always preserved (don't merge, just collect)
		coreFacts = append(coreFacts, profile.CoreFactsPolicy)

		// Style is merged (later refines earlier)
		if profile.Style != "" {
			styles = append(styles, profile.Style)
		}

		// Output format - last one wins
		if profile.OutputFormat != "" {
			outputFormats = append(outputFormats, profile.OutputFormat)
		}

		// Examples are merged (later takes precedence on conflicts)
		for k, v := range profile.Examples {
			if allExamples == nil {
				allExamples = make(map[string]string)
			}
			allExamples[k] = v
		}
	}

	// Build final output
	var sections []string

	// Core facts (deduplicated)
	uniqueCore := deduplicateStrings(coreFacts)
	sections = append(sections, fmt.Sprintf("## Factual Safety Rules\n\n%s", strings.Join(uniqueCore, "\n\n---\n\n")))

	// Merged style
	if len(styles) > 0 {
		sections = append(sections, fmt.Sprintf("## Writing Style\n\n%s", strings.Join(styles, "\n\n---\n\n")))
	}

	// Last output format
	if len(outputFormats) > 0 {
		sections = append(sections, fmt.Sprintf("## Output Format Instructions\n\n%s", outputFormats[len(outputFormats)-1]))
	}

	// Examples
	if len(allExamples) > 0 {
		var sb strings.Builder
		sb.WriteString("## Examples\n\n")
		sb.WriteString("The following examples illustrate desired output format:\n\n")

		for path, content := range allExamples {
			fmt.Fprintf(&sb, "### %s\n\n", path)
			sb.WriteString("```\n")
			sb.WriteString(content)
			sb.WriteString("\n```\n\n")
		}

		sections = append(sections, sb.String())
	}

	return strings.Join(sections, "\n\n---\n\n")
}

// InjectFactRules injects core facts from all sources.
func (c *CompositeInjector) InjectFactRules() string {
	var coreFacts []string
	for _, inj := range c.injectors {
		coreFacts = append(coreFacts, inj.GetProfile().CoreFactsPolicy)
	}
	// Deduplicate identical core facts
	uniqueCore := deduplicateStrings(coreFacts)
	return fmt.Sprintf("## Factual Safety Rules\n\n%s", strings.Join(uniqueCore, "\n\n---\n\n"))
}

// InjectStyleRules injects merged style from all sources.
func (c *CompositeInjector) InjectStyleRules() string {
	var styles []string
	for _, inj := range c.injectors {
		if inj.GetProfile().Style != "" {
			styles = append(styles, inj.GetProfile().Style)
		}
	}
	if len(styles) == 0 {
		return ""
	}
	return fmt.Sprintf("## Writing Style\n\n%s", strings.Join(styles, "\n\n---\n\n"))
}

// InjectVocabularyRules injects merged vocabulary from all sources.
func (c *CompositeInjector) InjectVocabularyRules() string {
	var vocabularies []string
	for _, inj := range c.injectors {
		if inj.GetProfile().Vocabulary != "" {
			vocabularies = append(vocabularies, inj.GetProfile().Vocabulary)
		}
	}
	if len(vocabularies) == 0 {
		return ""
	}
	return fmt.Sprintf("## Vocabulary Constraints\n\n%s", strings.Join(vocabularies, "\n\n---\n\n"))
}

// InjectOutputFormat injects the last output format.
func (c *CompositeInjector) InjectOutputFormat() string {
	var formats []string
	for _, inj := range c.injectors {
		if inj.GetProfile().OutputFormat != "" {
			formats = append(formats, inj.GetProfile().OutputFormat)
		}
	}
	if len(formats) == 0 {
		return ""
	}
	return formats[len(formats)-1]
}

// GetExamples returns merged examples.
func (c *CompositeInjector) GetExamples() map[string]string {
	allExamples := make(map[string]string)
	for _, inj := range c.injectors {
		for k, v := range inj.GetExamples() {
			allExamples[k] = v
		}
	}
	return allExamples
}

// GetProfile returns a merged profile from all sources.
func (c *CompositeInjector) GetProfile() Profile {
	var coreFacts []string
	var styles []string
	var outputFormats []string
	var allExamples map[string]string

	for _, inj := range c.injectors {
		profile := inj.GetProfile()
		coreFacts = append(coreFacts, profile.CoreFactsPolicy)
		if profile.Style != "" {
			styles = append(styles, profile.Style)
		}
		if profile.OutputFormat != "" {
			outputFormats = append(outputFormats, profile.OutputFormat)
		}
		for k, v := range profile.Examples {
			if allExamples == nil {
				allExamples = make(map[string]string)
			}
			allExamples[k] = v
		}
	}

	// Deduplicate and merge
	return Profile{
		CoreFactsPolicy: strings.Join(deduplicateStrings(coreFacts), "\n\n---\n\n"),
		Style:           strings.Join(styles, "\n\n---\n\n"),
		Vocabulary:      "", // Collecting vocabulary separately would require another pass
		OutputFormat:    lastNonEmpty(outputFormats),
		Examples:        allExamples,
	}
}

// Helper: deduplicate strings while preserving order.
func deduplicateStrings(s []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(s))
	for _, str := range s {
		trimmed := strings.TrimSpace(str)
		if trimmed != "" && !seen[trimmed] {
			seen[trimmed] = true
			result = append(result, str)
		}
	}
	return result
}

// Helper: return last non-empty string.
func lastNonEmpty(s []string) string {
	for i := len(s) - 1; i >= 0; i-- {
		if strings.TrimSpace(s[i]) != "" {
			return s[i]
		}
	}
	return ""
}
