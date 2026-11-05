package editorialedit

import (
	"context"
	"errors"
	"strings"
	"time"
)

var errClassifierContract = errors.New("classifier returned a classification count that does not match the units")

// Analyze analyzes the change between the generated (before) and the
// human-edited final (after) text of a recorded artifact pair.
//
// The deterministic diff is computed unconditionally — it is the ground
// truth and is preserved in the result whether or not classification
// succeeds. When classifier is non-nil, each hunk is classified: a
// classifier error (or a unit it fails to label) degrades that unit to the
// bare diff (Classified stays true for the units that succeeded, and the
// error is returned alongside a best-effort result when at least the diff
// itself is intact — callers that want all-or-nothing semantics can check
// the error).
//
// beforeText and afterText are the persisted, sanitized document bytes
// (the run's final/generated and final documents); names identify them for
// display and records. now timestamps the classification (UTC).
func Analyze(
	ctx context.Context,
	classifier Classifier,
	beforeName, beforeText, afterName, afterText string,
	now time.Time,
) (*Result, error) {
	now = zeroReplacer(now)

	d := Diff(beforeText, afterText)

	res := &Result{
		BeforeName:      beforeName,
		AfterName:       afterName,
		Before:          truncate(beforeText),
		After:           truncate(afterText),
		Identical:       d.Identical(),
		DiffText:        strings.TrimSpace(d.String()),
		AddedLines:      d.AddedLines,
		DeletedLines:    d.DeletedLines,
		diff:            d,
		BeforeDigest:    digest(beforeText),
		AfterDigest:     digest(afterText),
		HeadlineChanged: headlineOf(beforeText) != headlineOf(afterText),
		BeforeWords:     wordCount(beforeText),
		AfterWords:      wordCount(afterText),
	}
	if d.Identical() {
		res.DiffText = ""
		return res, nil
	}

	if classifier == nil {
		return res, nil
	}

	units := unitsForHunks(d)
	classifications, err := classifier.Classify(ctx, units)
	if err != nil {
		// Advisory-layer failure: return the deterministic result and the
		// error together. Classified stays false: a partially or wholly
		// failed classifier must not look like a successful one, and a
		// consumer that ignores errors reads an unclassified (safe) result.
		return res, err
	}
	if len(classifications) != len(units) {
		return res, errClassifierContract
	}
	for i := range classifications {
		_ = i
	}

	res.Classified = true
	res.Classifier = nameOf(classifier)
	// A zero now is replaced with the current time (an unhelpful record is
	// worse than a stamped one).
	res.ClassifiedAt = now
	res.Classifications = sanitizeClassifications(classifications, len(units))
	for _, c := range res.Classifications {
		if !HasFactualChange(c.Categories) {
			continue
		}
		res.FactualChange = true
		if c.Confidence >= ConfidenceHighLabel {
			res.HighConfidence = true
		}
	}
	return res, nil
}

// unitsForHunks builds the classification units of a diff: one per hunk, in
// diff order, with before/after excerpts (context and the side each changed
// line belongs to).
func unitsForHunks(d DiffDoc) []ClassificationUnit {
	units := make([]ClassificationUnit, len(d.Hunks))
	for i, h := range d.Hunks {
		var before, after strings.Builder
		for _, l := range h.Lines {
			switch l.Change {
			case ChangeAdd:
				writeExcerptLine(&after, l)
			default: // context and del are on the before side
				writeExcerptLine(&before, l)
			}
		}
		units[i] = ClassificationUnit{
			Kind:          UnitHunk,
			Index:         i,
			Text:          h.Text(),
			BeforeExcerpt: before.String(),
			AfterExcerpt:  after.String(),
		}
	}
	return units
}

// writeExcerptLine appends one excerpt line with a trailing newline.
func writeExcerptLine(b *strings.Builder, l Line) {
	b.WriteString(l.Content)
	b.WriteByte('\n')
}

// sanitizeClassifications brings classifier output into the contract: one
// classification per unit, in order, each with normalized categories and a
// confidence clamped to [0,1]. A unit the classifier left empty becomes an
// explicit CategoryNone classification (the classifier said "I cannot
// classify this" rather than "this means X").
func sanitizeClassifications(in []Classification, units int) []Classification {
	out := make([]Classification, units)
	for i := 0; i < units; i++ {
		src := Classification{}
		if i < len(in) {
			src = in[i]
		}
		normalized, _ := NormalizeCategories(nil)
		if src.Categories != nil {
			normalized, _ = NormalizeCategories(stringSlice(src.Categories))
		}
		if len(normalized) == 0 {
			normalized = []Category{CategoryNone}
		}
		out[i] = Classification{
			Categories: normalized,
			Note:       strings.TrimSpace(src.Note),
			Confidence: clamp01(src.Confidence),
		}
	}
	return out
}

// stringSlice converts []Category to []string for NormalizeCategories.
func stringSlice(cs []Category) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = string(c)
	}
	return out
}

// clamp01 bounds a confidence to [0,1] (a classifier that reports 1.5 or
// -0.2 gets the nearest edge; a zero-value/NaN-free float is left alone).
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// NamedClassifier is an optional interface a Classifier may implement to
// name itself on results (a classifier that does not implement it is
// recorded under "" — the result still carries Classified = true).
type NamedClassifier interface {
	Name() string
}

// nameOf returns a classifier's self-declared name, or "" when it does not
// implement NamedClassifier.
func nameOf(c Classifier) string {
	if n, ok := c.(NamedClassifier); ok {
		return n.Name()
	}
	return ""
}

// zeroReplacer normalizes a possibly-zero timestamp to the current time.
//
// It is a standalone function (not a method on Result) so tests can exercise
// the zero-time rule in isolation; the analyzer applies it to the result's
// classification timestamp.
func zeroReplacer(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t.UTC()
}
