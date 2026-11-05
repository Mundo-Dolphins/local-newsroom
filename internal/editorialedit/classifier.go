package editorialedit

import "context"

// The classification layer: an advisory labeler for deterministic diff
// hunks (issue #147).
//
// A Classifier assigns a category (and an optional free-text note) to each
// classified unit of a diff. It is the ONLY component of the whole package
// that is allowed to be non-deterministic, and the contract says so
// explicitly:
//
//   - The deterministic diff is the ground truth and is always preserved,
//     with or without a Classifier (see Result).
//   - A Classifier's output is a hypothesis. Results carry Classified (did
//     a classifier actually run) and Confidence, and consumers must weigh
//     labels accordingly — a classifier label is never factual truth.
//   - A Classifier that errors is a failure of the advisory layer only:
//     the analysis degrades to the bare deterministic diff. The Analyze
//     entry point implements that degradation.

// UnitKind names what a classified unit is a classification of.
type UnitKind string

const (
	// UnitHunk classifies one diff hunk (the default and only unit kind the
	// Analyze entry point produces).
	UnitHunk UnitKind = "hunk"

	// UnitDocument classifies the whole document change (available to
	// classifiers that summarize instead of enumerating; the document-level
	// signals in Result precompute the cheap deterministic facts a such a
	// summary would need).
	UnitDocument UnitKind = "document"
)

// ClassificationUnit is the input a Classifier receives for one unit.
type ClassificationUnit struct {
	// Kind is what the unit is (hunk or document).
	Kind UnitKind

	// Index is the unit's 0-based position in the analyzed diff (hunks are
	// 0..len-1; the document unit is 0).
	Index int

	// Text is the unit's rendered unified-diff text: for a hunk, the hunk
	// itself; for the document, every hunk joined.
	Text string

	// BeforeExcerpt is the before-side lines of the unit (a hunk's context
	// and deleted lines, in order), for classifiers that read the original
	// material.
	BeforeExcerpt string

	// AfterExcerpt is the after-side lines of the unit (a hunk's context
	// and added lines, in order).
	AfterExcerpt string
}

// Classification is the output of a Classifier for one unit.
type Classification struct {
	// Categories is the normalized, de-duplicated set of categories for
	// the unit (empty = the unit was not classifiable; the analyzer records
	// CategoryNone for it).
	Categories []Category

	// Note is a short, human-readable rationale. It is free text: a
	// classifier that cannot justify its labels should keep it empty rather
	// than inventing reasons.
	Note string

	// Confidence is the classifier's self-assessed confidence in its own
	// label, in [0,1]. Below the consumer's threshold a label should be
	// treated as noise. (The classifier's honesty is the best we can do;
	// Confidence is a hint, not a measurement.)
	Confidence float64
}

// Classifier labels classification units of a diff.
//
// Implementations must be safe for concurrent use (analyzer code does not
// lock around calls) and must treat ctx cancellation as fatal: a canceled
// classifier leaves the analysis unfinished.
type Classifier interface {
	// Classify labels the given units of one diff. It returns exactly one
	// Classification per unit, in order. An error means the advisory layer
	// failed; the analyzer degrades to the bare diff.
	Classify(ctx context.Context, units []ClassificationUnit) ([]Classification, error)
}
