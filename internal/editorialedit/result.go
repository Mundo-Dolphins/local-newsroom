package editorialedit

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// Result is the output of an editorial change analysis: the deterministic
// diff (ground truth) plus, when a classifier ran, its advisory labels.
//
// The invariant this structure enforces by shape: the diff fields are
// always present when a change exists; the classification fields are
// explicit about their own provenance (Classified is false when no
// classifier ran, and Confidence distinguishes a confident label from a
// guess). A consumer that trusts only the diff gets the truth; a consumer
// that also trusts the labels must check Classified and weigh Confidence.

// maxTextLen bounds the before/after text copied onto a result, so a huge
// document cannot bloat a downstream record that embeds one. The full text
// of the pair already lives in the run's final/ documents; the result
// embeds only a bounded excerpt.
const maxTextLen = 8192

// ConfidenceHighLabel is the confidence a label needs to be trusted as a
// signal for learning (issue #148 consumes it); below it, a label is noise.
// It is a policy constant of this package, exposed for consumers.
const ConfidenceHighLabel = 0.7

// Result is one generated-vs-final change analysis.
type Result struct {
	// BeforeName / AfterName identify the two documents (the run-relative
	// paths of the final/generated pair, when known; "" otherwise).
	BeforeName string
	AfterName  string

	// Before and After are the full texts when each fits within
	// maxTextLen; otherwise a bounded prefix (the analysis was computed on
	// the full texts; only the embedding is truncated).
	Before string
	After  string

	// BeforeDigest / AfterDigest are the SHA-256 digests (hex) of the two
	// full texts; they pin the record back to the exact persisted pair
	// without embedding the whole document.
	BeforeDigest string
	AfterDigest  string

	// diff is the structured diff behind DiffText (kept for hunk-level
	// access such as DiffTextForHunk; not serialized by Result consumers).
	diff DiffDoc

	// Identical is true when the two texts are byte-identical (no change
	// at all; no hunks, no labels).
	Identical bool

	// DiffText is the rendered unified diff of the change ("" when
	// identical). This is the preserved deterministic diff: it survives
	// regardless of what any classifier does.
	DiffText string

	// AddedLines / DeletedLines summarize the diff in line counts.
	AddedLines   int
	DeletedLines int

	// HeadlineChanged is the document-level signal: the first non-empty
	// markdown heading (or first non-empty line, for non-markdown text)
	// differs between the documents.
	HeadlineChanged bool

	// BeforeWords / AfterWords are word counts, the document-level
	// length/verbosity signal.
	BeforeWords int
	AfterWords  int

	// Classified is true when a classifier ran and produced the
	// Classifications below (false = deterministic-only analysis).
	Classified bool

	// Classifier is the name of the classifier that produced the
	// Classifications ("" when Classified is false).
	Classifier string

	// ClassifiedAt is when the classification ran (UTC).
	ClassifiedAt time.Time

	// Classifications carries one advisory classification per diff hunk, in
	// diff order (len == len(Hunks) when Classified).
	Classifications []Classification

	// FactualChange is the precomputed ticket-critical distinction: true
	// when any classification carries a fact-changing category (see
	// HasFactualChange). Without a classifier it is always false — a
	// deterministic-only analysis never asserts that a change is factual.
	FactualChange bool

	// HighConfidence is true when at least one classification both carries
	// a fact-changing category and meets ConfidenceHighLabel.
	HighConfidence bool
}

// hunks holds the (unexported) structured diff behind a Result so the
// classified units can be rebuilt for a re-classification without re-reading
// the documents.
var _ = struct{}{}

// DiffTextForHunk renders the i-th diff hunk of the analysis ("" when the
// index is out of range or the pair is identical). Records that store one
// hunk embed this text, preserving the deterministic diff per hunk.
func (r *Result) DiffTextForHunk(i int) string {
	if i < 0 || i >= len(r.diff.Hunks) {
		return ""
	}
	return r.diff.Hunks[i].Text()
}

// digest returns the hex SHA-256 digest of s.
func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// FactualCategories reports the fact-changing categories present across all
// classifications (normalized, canonical order).
func (r *Result) FactualCategories() []Category {
	seen := make(map[Category]bool)
	var out []Category
	for _, c := range r.Classifications {
		if !HasFactualChange(c.Categories) {
			continue
		}
		for _, cat := range c.Categories {
			if !Contains(HasFactualCategories(), cat) {
				continue
			}
			if !seen[cat] {
				seen[cat] = true
				out = append(out, cat)
			}
		}
	}
	return out
}

// HasFactualCategories lists the categories that count as fact-changing
// (the ones HasFactualChange keys on).
func HasFactualCategories() []Category {
	return []Category{
		CategoryFactualCorrection,
		CategoryAttributionCorrection,
		CategoryUnsupportedCertainty,
	}
}

// truncate bounds s to maxTextLen (with an ellipsis marker when it had to).
func truncate(s string) string {
	if len(s) <= maxTextLen {
		return s
	}
	return s[:maxTextLen] + "\n[…truncated]"
}

// headlineOf returns the first non-empty line of text; for markdown text it
// prefers the first markdown heading line. This is the deterministic
// stand-in for "the headline" used by the HeadlineChanged signal: articles
// carry a "# ..." first line, threads carry their opening line.
func headlineOf(text string) string {
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		return t
	}
	return ""
}

// wordCount counts whitespace-separated words.
func wordCount(text string) int {
	return len(strings.Fields(text))
}
