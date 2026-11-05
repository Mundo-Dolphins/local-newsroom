// Package editorialedit analyzes the difference between a run's generated
// output and the human-edited final version recorded for it (v0.7, issue
// #147, on top of the generated-vs-final pair from issue #146).
//
// The analysis has exactly two layers, in this order:
//
//  1. A deterministic text diff (internal/diff). The diff is the ground
//     truth: it is computed over the persisted, sanitized bytes of the two
//     documents, is preserved verbatim on every produced record, and requires
//     no model.
//  2. An optional classifier that assigns a change category (and a note) to
//     each diff hunk. The Classifier is an injected interface; a
//     structured-output LLM classifier (ClassifyLLM) and a deterministic
//     fake (for tests) are both supplied.
//
// Core rules:
//
//   - The deterministic diff is ALWAYS kept, no matter what the classifier
//     says — the classifier adds annotations, it never replaces evidence.
//   - The classifier is advisory and never factual truth: every result marks
//     Classified (whether a classifier ran) and Confidence, and a classifier
//     that fails or returns garbage degrades the analysis to the bare
//     deterministic diff rather than producing labels from nothing.
//   - A classification is only as good as its confidence: categories may be
//     combined with the raw diff by consumers, but the diff itself is the
//     only layer a consumer may trust unconditionally.
//
// Scope: the diff layer is format-agnostic (any text). Document-level
// signals (headline change, length, additions/removals) are computed from
// the text as well, so both articles (markdown) and threads (plain text)
// analyze through the same API.
package editorialedit
