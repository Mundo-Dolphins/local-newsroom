package editorialedit

import (
	"sort"
	"strings"
)

// The editorial change categories (issue #147).
//
// A category names the kind of editorial meaning in a chunk of a generated-
// vs-final diff. The list is the ticket's minimum set; it is open (Category
// is a string) so new categories can be added without a schema change, but
// every label this package produces is one of the canonical values below.
//
// Categories are advisory labels, not a taxonomy of truth: a single hunk
// often carries several (a reworded sentence that also fixes a name is at
// once vocabulary and factual correction). Results therefore carry a slice
// of categories per classified unit, and a "none" (no meaningful change)
// value so a classifier can say "no category" instead of guessing.

// Category is the classification label for a classified diff unit.
type Category string

// Canonical editorial change categories, in presentation order.
const (
	// CategoryFactualCorrection: the edit corrects a factual statement
	// (a value, date, name, number, or claim the generated text got wrong).
	// Factual corrections are the load-bearing category for feedback
	// learning: consumers must be able to distinguish them from style.
	CategoryFactualCorrection Category = "factual_correction"

	// CategoryAttributionCorrection: the edit fixes who said or did what
	// (a misattributed quote, number, or action).
	CategoryAttributionCorrection Category = "attribution_correction"

	// CategoryUnsupportedCertainty: the edit weakens or qualifies a claim
	// the generated text stated more certainly than its evidence supports
	// (removing hedges in the other direction is the same category).
	CategoryUnsupportedCertainty Category = "unsupported_certainty"

	// CategoryToneStyle: the edit changes how something is said, not what
	// it says (tone, register, emphasis).
	CategoryToneStyle Category = "tone_style"

	// CategoryVocabularyTranslation: the edit swaps words or phrasings of
	// equivalent meaning (word choice, localization, simplification).
	CategoryVocabularyTranslation Category = "vocabulary_translation"

	// CategoryStructureOrder: the edit reorders or restructures material
	// (sections moved, paragraphs re-sequenced, list vs prose).
	CategoryStructureOrder Category = "structure_order"

	// CategoryOmission: the edit removes generated material.
	CategoryOmission Category = "omission"

	// CategoryAddition: the edit adds material the generated output lacked.
	CategoryAddition Category = "addition"

	// CategoryLengthVerbosity: the edit changes overall length or density
	// without a single focal change (compression or expansion).
	CategoryLengthVerbosity Category = "length_verbosity"

	// CategoryHeadlineTitle: the edit changes the headline or title.
	CategoryHeadlineTitle Category = "headline_title"

	// CategoryFormatting: the edit changes markup or layout (headers,
	// lists, emphasis, spacing) without changing content.
	CategoryFormatting Category = "formatting"

	// CategoryThreadSplitMerge: the edit re-partitions a thread (posts
	// split, merged, or re-ordered between posts).
	CategoryThreadSplitMerge Category = "thread_split_merge"

	// CategoryNone: the unit carries no meaningful editorial change
	// (whitespace, redaction noise, or a classifier judgment of
	// "unclassifiable").
	CategoryNone Category = "none"
)

// AllCategories lists every recognized Category in canonical order.
var AllCategories = []Category{
	CategoryFactualCorrection,
	CategoryAttributionCorrection,
	CategoryUnsupportedCertainty,
	CategoryToneStyle,
	CategoryVocabularyTranslation,
	CategoryStructureOrder,
	CategoryOmission,
	CategoryAddition,
	CategoryLengthVerbosity,
	CategoryHeadlineTitle,
	CategoryFormatting,
	CategoryThreadSplitMerge,
	CategoryNone,
}

// ParseCategory normalizes and validates a category label.
//
// It is case-insensitive and trims whitespace. It returns CategoryNone for
// the empty string and the "none"/"unclassified" spellings; any other
// unrecognized label is an error (callers that receive free-form labels from
// a classifier should map unknowns to CategoryNone rather than storing
// them, so downstream filters never see invented taxonomy).
func ParseCategory(s string) (Category, error) {
	c := Category(strings.ToLower(strings.TrimSpace(s)))
	switch c {
	case "":
		return CategoryNone, nil
	}
	for _, valid := range AllCategories {
		if c == valid {
			return valid, nil
		}
	}
	if c == "unclassified" {
		return CategoryNone, nil
	}
	return "", ErrUnknownCategory{Label: s}
}

// ErrUnknownCategory is returned by ParseCategory for a label that is not a
// recognized category (and not an alias of CategoryNone).
type ErrUnknownCategory struct {
	// Label is the unrecognized input (trimmed of surrounding whitespace
	// only; never a secret — it comes from the analyzer's own output).
	Label string
}

func (e ErrUnknownCategory) Error() string {
	return "unknown editorial change category " + quote(e.Label)
}

func quote(s string) string {
	return `"` + s + `"`
}

// NormalizeCategories reduces a raw label slice (as produced by a
// classifier) to canonical, de-duplicated, in-canonical-order categories.
//
// Unknown labels are dropped and reported through the returned (separate)
// slice, never silently mapped: the caller decides whether an unclassifiable
// unit becomes CategoryNone.
func NormalizeCategories(raw []string) (categories []Category, unknown []string) {
	seen := make(map[Category]bool, len(raw))
	for _, r := range raw {
		c, err := ParseCategory(r)
		if err != nil {
			if s := strings.TrimSpace(r); s != "" {
				unknown = append(unknown, s)
			}
			continue
		}
		if c == CategoryNone {
			// An explicit "none" (the classifier found no meaningful
			// change) is recorded as an empty category set; the analyzer
			// renders it as none where a label is expected.
			continue
		}
		if !seen[c] {
			seen[c] = true
			categories = append(categories, c)
		}
	}
	sort.Slice(categories, func(i, j int) bool {
		return categoryRank(categories[i]) < categoryRank(categories[j])
	})
	return categories, unknown
}

// categoryRank returns the canonical presentation order of a recognized
// category (AllCategories order); unrecognized labels sort last by name.
func categoryRank(c Category) int {
	for i, v := range AllCategories {
		if v == c {
			return i
		}
	}
	return len(AllCategories)
}

// Contains reports whether cs (already normalized) holds c.
func Contains(cs []Category, c Category) bool {
	for _, v := range cs {
		if v == c {
			return true
		}
	}
	return false
}

// HasFactualChange reports whether the category set contains any category
// that changes what the document asserts about the world (factual
// correction, attribution correction, or weakened/added certainty).
//
// This is the distinction the ticket requires: "factual corrections
// distinguished from style edits." Style edits (tone, vocabulary,
// formatting, structure, length, wording) do not change assertions.
func HasFactualChange(cs []Category) bool {
	return Contains(cs, CategoryFactualCorrection) ||
		Contains(cs, CategoryAttributionCorrection) ||
		Contains(cs, CategoryUnsupportedCertainty)
}
