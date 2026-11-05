package editorialedit

import "testing"

func TestParseCategoryCanonical(t *testing.T) {
	for _, c := range AllCategories {
		got, err := ParseCategory(string(c))
		if err != nil {
			t.Fatalf("ParseCategory(%q): %v", c, err)
		}
		if got != c {
			t.Fatalf("ParseCategory(%q) = %q, want %q", c, got, c)
		}
	}
}

func TestParseCaseInsensitive(t *testing.T) {
	got, err := ParseCategory("  Factual_Correction ")
	if err != nil {
		t.Fatalf("case/whitespace must not matter: %v", err)
	}
	if got != CategoryFactualCorrection {
		t.Fatalf("got %q, want %q", got, CategoryFactualCorrection)
	}
}

func TestParseNoneAliases(t *testing.T) {
	for _, alias := range []string{"", "none", "NONE", "unclassified"} {
		got, err := ParseCategory(alias)
		if err != nil {
			t.Fatalf("ParseCategory(%q): %v", alias, err)
		}
		if got != CategoryNone {
			t.Fatalf("ParseCategory(%q) = %q, want none", alias, got)
		}
	}
}

func TestParseUnknownCategory(t *testing.T) {
	_, err := ParseCategory("purple_prose")
	if err == nil {
		t.Fatal("an invented category must not parse")
	}
}

func TestNormalizeCategoriesDedupOrder(t *testing.T) {
	cats, unknown := NormalizeCategories([]string{
		"tone_style", "factual_correction", "tone_style", "Factual_Correction", "vocabulary_translation",
	})
	if len(unknown) != 0 {
		t.Fatalf("no unknowns expected, got %v", unknown)
	}
	want := []Category{CategoryFactualCorrection, CategoryToneStyle, CategoryVocabularyTranslation}
	if len(cats) != len(want) {
		t.Fatalf("got %v, want %v", cats, want)
	}
	for i := range want {
		if cats[i] != want[i] {
			t.Fatalf("position %d: got %q, want %q", i, cats[i], want[i])
		}
	}
}

func TestNormalizeCategoriesUnknownSeparate(t *testing.T) {
	cats, unknown := NormalizeCategories([]string{"tone_style", "purple_prose", "  "})
	if len(cats) != 1 || cats[0] != CategoryToneStyle {
		t.Fatalf("recognized labels must survive: %v", cats)
	}
	if len(unknown) != 1 || unknown[0] != "purple_prose" {
		t.Fatalf("unknowns must be reported separately: %v", unknown)
	}
}

func TestHasFactualChange(t *testing.T) {
	factual := [][]Category{
		{CategoryFactualCorrection},
		{CategoryAttributionCorrection},
		{CategoryUnsupportedCertainty},
		{CategoryFactualCorrection, CategoryToneStyle},
	}
	for _, cs := range factual {
		if !HasFactualChange(cs) {
			t.Fatalf("must be factual: %v", cs)
		}
	}
	style := [][]Category{
		{CategoryToneStyle},
		{CategoryVocabularyTranslation, CategoryFormatting},
		{CategoryStructureOrder},
		{CategoryNone},
		{},
	}
	for _, cs := range style {
		if HasFactualChange(cs) {
			t.Fatalf("must not be factual (style distinction): %v", cs)
		}
	}
}

func TestContains(t *testing.T) {
	cs := []Category{CategoryToneStyle, CategoryOmission}
	if !Contains(cs, CategoryOmission) {
		t.Fatal("Contains must find omission")
	}
	if Contains(cs, CategoryAddition) {
		t.Fatal("Contains must not find addition")
	}
}
