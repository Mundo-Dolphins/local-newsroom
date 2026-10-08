package profiles

import (
	"strings"
	"testing"
)

// TestProfileInjector_WriterInstructions tests complete writer instruction injection.
func TestProfileInjector_WriterInstructions(t *testing.T) {
	profile := Profile{
		CoreFactsPolicy: "## Facts\nVerify all claims.\n",
		Style:           "## Style\nWrite neutrally.\n",
		Vocabulary:      "## Vocab\nUse simple words.\n",
		OutputFormat:    "## Format\nReturn JSON.\n",
		Examples: map[string]string{
			"example1.json": `{"key": "value"}`,
		},
	}

	inj := NewInjector(profile)
	instructions := inj.InjectWriterInstructions()

	// Check all sections are present
	expectedSections := []string{
		"Factual Safety Rules",
		"Writing Style",
		"Output Format Instructions",
		"Examples",
	}

	for _, section := range expectedSections {
		if !strings.Contains(instructions, section) {
			t.Errorf("Instructions should contain section %q", section)
		}
	}

	// Check content is present
	if !strings.Contains(instructions, "Verify all claims") {
		t.Error("Instructions should contain core facts content")
	}
	if !strings.Contains(instructions, "Write neutrally") {
		t.Error("Instructions should contain style content")
	}
}

// TestProfileInjector_FactRules tests factual rules injection.
func TestProfileInjector_FactRules(t *testing.T) {
	profile := Profile{
		CoreFactsPolicy: "## Facts\nNever invent facts.\n",
	}

	inj := NewInjector(profile)
	rules := inj.InjectFactRules()

	if !strings.Contains(rules, "Factual Safety Rules") {
		t.Error("Should include section header")
	}
	if !strings.Contains(rules, "Never invent facts") {
		t.Error("Should include policy content")
	}
}

// TestProfileInjector_StyleRules tests style rules injection.
func TestProfileInjector_StyleRules(t *testing.T) {
	profile := Profile{
		Style: "## Style\nUse first-person.\n",
	}

	inj := NewInjector(profile)
	rules := inj.InjectStyleRules()

	if !strings.Contains(rules, "Writing Style") {
		t.Error("Should include section header")
	}
	if !strings.Contains(rules, "Use first-person") {
		t.Error("Should include content")
	}
}

// TestProfileInjector_EmptyProfile tests injection with empty profile.
func TestProfileInjector_EmptyProfile(t *testing.T) {
	profile := Profile{}

	inj := NewInjector(profile)

	// Should not panic
	instructions := inj.InjectWriterInstructions()
	if instructions == "" {
		t.Error("Should return something (empty section headers)")
	}

	facts := inj.InjectFactRules()
	if !strings.Contains(facts, "Factual Safety Rules") {
		t.Error("Core facts section should still be present")
	}
}

// TestProfileInjector_GetProfile tests GetProfile.
func TestProfileInjector_GetProfile(t *testing.T) {
	expected := Profile{
		CoreFactsPolicy: "test facts",
		Style:           "test style",
		Vocabulary:      "test vocab",
		OutputFormat:    "test format",
		Examples: map[string]string{
			"ex.json": "example",
		},
	}

	inj := NewInjector(expected)
	got := inj.GetProfile()

	if got.CoreFactsPolicy != expected.CoreFactsPolicy {
		t.Errorf("CoreFactsPolicy mismatch: got %q, want %q", got.CoreFactsPolicy, expected.CoreFactsPolicy)
	}
	if got.Style != expected.Style {
		t.Errorf("Style mismatch: got %q, want %q", got.Style, expected.Style)
	}
	if len(got.Examples) != len(expected.Examples) {
		t.Errorf("Examples count mismatch: got %d, want %d", len(got.Examples), len(expected.Examples))
	}
}

// TestProfileInjector_GetExamples tests GetExamples.
func TestProfileInjector_GetExamples(t *testing.T) {
	profile := Profile{
		Examples: map[string]string{
			"example1.json": "ex1",
			"example2.json": "ex2",
		},
	}

	inj := NewInjector(profile)
	examples := inj.GetExamples()

	if len(examples) != 2 {
		t.Errorf("Expected 2 examples, got %d", len(examples))
	}
	if examples["example1.json"] != "ex1" {
		t.Error("Example 1 not found")
	}
}

// TestProfileInjector_EmptyVocabulary tests empty vocabulary.
func TestProfileInjector_EmptyVocabulary(t *testing.T) {
	profile := Profile{
		Vocabulary: "",
	}

	inj := NewInjector(profile)
	rules := inj.InjectVocabularyRules()

	if !strings.Contains(rules, "Vocabulary Constraints") {
		t.Error("Should still include section header")
	}
}

// TestCompositeInjector_MultipleSources tests combining multiple injectors.
func TestCompositeInjector_MultipleSources(t *testing.T) {
	profile1 := Profile{
		CoreFactsPolicy: "## Core 1\nRule 1.\n",
		Style:           "## Style 1\nWrite professionally.\n",
		Vocabulary:      "## Vocab 1\nAvoid jargon.\n",
	}

	profile2 := Profile{
		CoreFactsPolicy: "## Core 2\nRule 2.\n",
		Style:           "## Style 2\nUse short sentences.\n",
		Examples: map[string]string{
			"composite.json": "combined",
		},
	}

	inj1 := NewInjector(profile1)
	inj2 := NewInjector(profile2)

	composite := NewCompositeInjector(inj1, inj2)

	// Check that core facts from both are included
	facts := composite.InjectFactRules()
	if !strings.Contains(facts, "Rule 1") || !strings.Contains(facts, "Rule 2") {
		t.Error("Should include facts from both sources")
	}

	// Check that styles are merged
	style := composite.InjectStyleRules()
	if !strings.Contains(style, "Write professionally") || !strings.Contains(style, "Use short sentences") {
		t.Error("Should include styles from both sources")
	}

	// Check examples are merged
	examples := composite.GetExamples()
	if len(examples) != 1 {
		t.Errorf("Expected 1 example, got %d", len(examples))
	}
}

// TestCompositeInjector_LastFormatWins tests that last output format wins.
func TestCompositeInjector_LastFormatWins(t *testing.T) {
	profile1 := Profile{
		OutputFormat: "Format 1",
	}
	profile2 := Profile{
		OutputFormat: "Format 2",
	}

	inj1 := NewInjector(profile1)
	inj2 := NewInjector(profile2)

	composite := NewCompositeInjector(inj1, inj2)

	// Check that last format wins
	got := composite.InjectOutputFormat()
	if got != "Format 2" {
		t.Errorf("OutputFormat = %q, want %q", got, "Format 2")
	}
}

// TestCompositeInjector_CoreFactsPreserved tests that core facts always preserved.
func TestCompositeInjector_CoreFactsPreserved(t *testing.T) {
	// Multiple injectors with different core facts
	profile1 := Profile{
		CoreFactsPolicy: "Never invent facts.",
	}
	profile2 := Profile{
		CoreFactsPolicy: "Verify all claims.",
	}

	inj1 := NewInjector(profile1)
	inj2 := NewInjector(profile2)

	composite := NewCompositeInjector(inj1, inj2)
	instructions := composite.InjectWriterInstructions()

	// Both core facts should be present
	if !strings.Contains(instructions, "Never invent facts") {
		t.Error("Core fact 1 should be preserved")
	}
	if !strings.Contains(instructions, "Verify all claims") {
		t.Error("Core fact 2 should be preserved")
	}
}

// TestCompositeInjector_GetProfile tests GetProfile on composite.
func TestCompositeInjector_GetProfile(t *testing.T) {
	profile1 := Profile{
		Style:      "Style 1",
		Vocabulary: "Vocab 1",
	}
	profile2 := Profile{
		Style: "Style 2",
	}

	inj1 := NewInjector(profile1)
	inj2 := NewInjector(profile2)

	composite := NewCompositeInjector(inj1, inj2)
	merged := composite.GetProfile()

	if merged.Style == "" {
		t.Error("Merged profile should contain style")
	}
}

// TestProfileInjector_ExampleFormatting tests example formatting in instructions.
func TestProfileInjector_ExampleFormatting(t *testing.T) {
	profile := Profile{
		Examples: map[string]string{
			"format.json": `{"format": "json"}`,
		},
	}

	inj := NewInjector(profile)
	instructions := inj.InjectWriterInstructions()

	// Check example section
	if !strings.Contains(instructions, "Examples") {
		t.Error("Should have Examples section")
	}
	if !strings.Contains(instructions, "format.json") {
		t.Error("Should include example path")
	}
	if !strings.Contains(instructions, "```\n") {
		t.Error("Should include code fence")
	}
	if !strings.Contains(instructions, `{"format": "json"}`) {
		t.Error("Should include example content")
	}
}

// TestProfileInjector_NoExamples tests with no examples.
func TestProfileInjector_NoExamples(t *testing.T) {
	profile := Profile{
		CoreFactsPolicy: "Facts",
		Style:           "Style",
		// No examples
	}

	inj := NewInjector(profile)
	instructions := inj.InjectWriterInstructions()

	// Should not have examples section
	if strings.Contains(instructions, "## Examples") {
		t.Error("Should not include examples section when empty")
	}
}

// TestProfileInjector_Ordering tests that sections are in correct order.
func TestProfileInjector_Ordering(t *testing.T) {
	profile := Profile{
		CoreFactsPolicy: "Facts policy",
		Style:           "Style guide",
		Vocabulary:      "Vocab rules",
		OutputFormat:    "Format rules",
	}

	inj := NewInjector(profile)
	instructions := inj.InjectWriterInstructions()

	// Check ordering: facts before style before format
	factsIdx := strings.Index(instructions, "Factual Safety Rules")
	styleIdx := strings.Index(instructions, "Writing Style")
	formatIdx := strings.Index(instructions, "Output Format")

	if factsIdx == -1 || styleIdx == -1 || formatIdx == -1 {
		t.Error("All sections should be present")
	}

	if factsIdx > styleIdx {
		t.Error("Facts should come before style")
	}
	if styleIdx > formatIdx {
		t.Error("Style should come before output format")
	}
}

// TestProfileInjector_Deduplication tests example file paths.
func TestProfileInjector_Deduplication(t *testing.T) {
	profile1 := Profile{
		CoreFactsPolicy: "Facts 1",
		Style:           "Style 1",
	}
	profile2 := Profile{
		CoreFactsPolicy: "Facts 1", // Duplicate
		Style:           "Style 2",
	}

	inj1 := NewInjector(profile1)
	inj2 := NewInjector(profile2)

	composite := NewCompositeInjector(inj1, inj2)
	facts := composite.InjectFactRules()

	// Should deduplicate identical core facts
	count := strings.Count(facts, "Facts 1")
	if count > 1 {
		t.Errorf("Duplicate facts should be deduplicated, found %d occurrences", count)
	}
}
