package feedback

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/editorialedit"
	"github.com/Mundo-Dolphins/local-newsroom/internal/memory"
	"github.com/Mundo-Dolphins/local-newsroom/internal/memorystore"
)

// Store persists editorial change analyses as editorial feedback and
// correction records in a v0.7 memory store.

// maxDiffText is the bound on diff text embedded in a record payload; the
// unbounded document bytes already live in the run's final/ documents.
const maxDiffText = 16384

// Store writes analyses into a memorystore.Store.
//
// A Store is safe for concurrent use: it holds only an immutable store and
// clock.
type Store struct {
	store memorystore.Store
	now   func() time.Time
}

// NewStore builds a Store over s. now (when non-nil) is the clock used for
// record timestamps; nil uses the real clock.
func NewStore(s memorystore.Store, now func() time.Time) *Store {
	if s == nil {
		panic("feedback: NewStore with a nil memorystore.Store")
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Store{store: s, now: now}
}

// Source identifies the provenance of an analyzed artifact pair: the run and
// the two documents it was computed from.
type Source struct {
	// RunID names the run the pair belongs to (the manifest run id).
	RunID string

	// Recipe is the run's recipe name (the affected recipe: the one whose
	// render produced the generated document, when known).
	Recipe string

	// Profile is the run's profile (the affected profile), when the analysis
	// should attribute to the profile rather than the recipe.
	Profile string

	// GeneratedPath / FinalPath are the run-relative paths of the two
	// documents (final/generated/... and final/...).
	GeneratedPath string
	FinalPath     string

	// GeneratorDigest / FinalDigest are the SHA-256 digests of the two
	// persisted document bytes (the run records the same digests in its
	// manifest; they let a future pass match a record back to its pair).
	GeneratorDigest string
	FinalDigest     string

	// Editor is the identity recorded as the editor of the final document
	// ("" = unknown).
	Editor string
}

// StoreAnalysis stores one analysis result.
//
// It returns one record per classified diff hunk (kind editorial_feedback),
// plus one correction record (kind correction) when the analysis reports a
// high-confidence fact-changing classification, plus one run-level summary
// feedback record (kind editorial_feedback, scope the recipe or profile) so
// the analysis is queryable even for unclassified or identical pairs.
//
// Records are content-addressed (memorystore.DeriveStableID), so re-running
// the same analysis (same run, same unedited documents) derives identical
// records and upserting is idempotent. All fields cross the store's
// sanitization gate before writing; a sanitization failure fails closed.
//
// now overrides the clock for a deterministic record timestamp (tests).
func (s *Store) StoreAnalysis(ctx context.Context, res *editorialedit.Result, src Source, now time.Time) (ids []string, err error) {
	if res == nil {
		return nil, fmt.Errorf("feedback: nil analysis result")
	}
	if now.IsZero() {
		now = s.now()
	}
	now = now.UTC()

	var records []*memory.MemoryRecord
	records = append(records, s.summaryRecord(res, src, now)...)
	records = append(records, s.hunkRecords(res, src, now)...)
	records = append(records, s.correctionRecords(res, src, now)...)

	for _, rec := range records {
		id := memorystore.DeriveStableID(rec)
		rec.StableID = id
		if err := s.store.Write(ctx, rec); err != nil {
			return nil, fmt.Errorf("feedback: store record (kind %s): %w", rec.Kind, err)
		}
		ids = append(ids, string(id))
	}
	return ids, nil
}

// scopeOf picks the record scope: the profile when the analysis is
// attributed to one, else the recipe, else the newsroom (a run without
// either is still worth recording).
func scopeOf(src Source) memory.MemoryScope {
	switch {
	case src.Profile != "":
		return memory.ScopeProfile
	case src.Recipe != "":
		return memory.ScopeRecipe
	default:
		return memory.ScopeNewsroom
	}
}

// subjectOf picks the record subject: the affected document.
func subjectOf(res *editorialedit.Result, src Source) memory.EntityRef {
	doc := res.AfterName
	if doc == "" {
		doc = res.BeforeName
	}
	if doc == "" {
		doc = "unknown"
	}
	return memory.EntityRef{Kind: "document", Name: doc}
}

// provenanceOf builds the record's provenance: the run and both documents
// as provenance sources, plus the editor as the user correction.
func provenanceOf(res *editorialedit.Result, src Source, now time.Time) memory.Provenance {
	p := memory.Provenance{}
	if src.RunID != "" {
		p.Sources = append(p.Sources, memory.MemorySource{
			SourceType:  "run",
			SourceID:    src.RunID,
			RetrievedAt: now,
		})
	}
	if src.GeneratedPath != "" {
		p.Sources = append(p.Sources, memory.MemorySource{
			SourceType:  "generated_document",
			SourceID:    src.GeneratedPath,
			RetrievedAt: now,
		})
	}
	if src.FinalPath != "" {
		p.Sources = append(p.Sources, memory.MemorySource{
			SourceType:  "final_document",
			SourceID:    src.FinalPath,
			RetrievedAt: now,
		})
	}
	if src.Editor != "" {
		p.UserCorrected = true
		p.CorrectedBy = src.Editor
		p.CorrectedAt = now
	}
	return p
}

// confidenceFromScore maps an advisory classifier confidence in [0,1] onto
// the memory confidence ladder.
//
// A classifier label is a hypothesis, never verified knowledge: it maps to
// at most ConfidenceMedium. The "high" ladder rung is reserved for
// verified facts (entity memory) and human-asserted corrections, so a
// model label cannot reach it through this path.
func confidenceFromScore(v float64) memory.ConfidenceLevel {
	// The ladder is low/medium/high. A classifier label is a hypothesis,
	// never verified knowledge: it maps to at most ConfidenceMedium. The
	// low rung covers weak-confidence labels (the classifier was guessing);
	// a label that specific deserves at least that.
	switch {
	case v >= 0.4:
		return memory.ConfidenceMedium
	default:
		return memory.ConfidenceLow
	}
}

// hunkRecords builds one editorial_feedback record per classified diff hunk.
//
// An unclassified analysis produces no hunk records (the summary record
// still stores the pair's existence and its diff) — the ticket's
// "feedback records with before, after, category, ..." apply to classified
// hunks, and an unclassified diff is captured at the summary level instead
// of fabricating category-less per-hunk records.
func (s *Store) hunkRecords(res *editorialedit.Result, src Source, now time.Time) []*memory.MemoryRecord {
	if !res.Classified {
		return nil
	}
	var out []*memory.MemoryRecord
	for i, cl := range res.Classifications {
		if len(cl.Categories) == 0 || (len(cl.Categories) == 1 && cl.Categories[0] == editorialedit.CategoryNone) {
			continue // the classifier found no meaningful change in this hunk
		}
		rec := memory.NewMemoryRecord(now)
		rec.Kind = memory.KindEditorialFeedback
		rec.Scope = scopeOf(src)
		rec.Subject = subjectOf(res, src)
		rec.Field = fmt.Sprintf("hunk-%d", i)
		rec.Confidence = confidenceFromScore(cl.Confidence)
		rec.Volatility = memory.VolatileStatic
		rec.Freshness = memory.Freshness{LastVerifiedAt: now} // an edit is a fact about what happened
		rec.Expiration = memory.Expiration{InvalidationStatus: memory.InvalidationActive}
		rec.Provenance = provenanceOf(res, src, now)

		err := rec.EncodeValue(memory.FeedbackRecord{
			Feedback:  hunkFeedbackText(i, res, cl),
			GivenBy:   editorOrClassifier(res, "analysis"),
			GivenAt:   now,
			Recurring: false, // recurrence is decided by pattern detection (#148), not by one observation
		}, now)
		if err != nil {
			// EncodeValue only fails on a zero time; unreachable with a
			// real now. Surface it rather than writing a broken record.
			panic(fmt.Sprintf("feedback: encode hunk %d: %v", i, err))
		}
		out = append(out, rec)
	}
	return out
}

// hunkFeedbackText renders the structured, human-readable payload text of a
// classified hunk: what changed (before/after), what it was labeled, and how
// confidently. The diff itself is the ground truth the text embeds.
func hunkFeedbackText(i int, res *editorialedit.Result, cl editorialedit.Classification) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Editorial change %d (categories: %s; classifier: %s; confidence: %.2f)",
		i+1, joinCategories(cl.Categories), orUnknown(res.Classifier), cl.Confidence)
	if cl.Note != "" {
		b.WriteString("; " + cl.Note)
	}
	b.WriteString(".\n")
	b.WriteString("Deterministic diff (preserved):\n")
	b.WriteString(truncateDiff(res.DiffTextForHunk(i)))
	return b.String()
}

// correctionRecords builds the correction record(s) of an analysis: one per
// high-confidence, fact-changing classification.
//
// This is the ticket's "correction classification" half: the record says
// what the generated text said (before), what the final says instead
// (after), and that a human corrected a fact. Its Confidence is the memory
// ladder's top rung: a human edit of a published artifact IS the correction,
// and the record's kind is correction — the classifier only selects which
// fact-changing hunks are important enough to record (confidence gate).
func (s *Store) correctionRecords(res *editorialedit.Result, src Source, now time.Time) []*memory.MemoryRecord {
	if !res.Classified || !res.HighConfidence {
		return nil
	}
	factualCats := res.FactualCategories()
	if len(factualCats) == 0 {
		return nil
	}
	var out []*memory.MemoryRecord
	for i, cl := range res.Classifications {
		if !editorialedit.HasFactualChange(cl.Categories) || cl.Confidence < editorialedit.ConfidenceHighLabel {
			continue
		}
		rec := memory.NewMemoryRecord(now)
		rec.Kind = memory.KindCorrection
		rec.Scope = scopeOf(src)
		rec.Subject = subjectOf(res, src)
		rec.Field = fmt.Sprintf("hunk-%d", i)
		rec.Confidence = memory.ConfidenceHigh // a human correction of a published artifact
		rec.Volatility = memory.VolatileStatic
		rec.Freshness = memory.Freshness{LastVerifiedAt: now}
		rec.Expiration = memory.Expiration{InvalidationStatus: memory.InvalidationActive}
		rec.Provenance = provenanceOf(res, src, now)

		err := rec.EncodeValue(memory.CorrectionRecord{
			OriginalClaim:  truncExcerpt(res.Before),
			CorrectedClaim: truncExcerpt(res.After),
			Reason:         joinCategories(cl.Categories) + ": human edit of the final document",
			CorrectedBy:    src.Editor,
			CorrectedAt:    now,
		}, now)
		if err != nil {
			panic(fmt.Sprintf("feedback: encode correction for hunk %d: %v", i, err))
		}
		out = append(out, rec)
	}
	return out
}

// summaryRecord builds the run-level summary record of an analysis: one
// editorial_feedback record (scope recipe/profile) carrying the pair-level
// facts (identical? classified? diff stats; bounded diff text).
//
// It is produced for EVERY analyzed pair — including identical and
// unclassified ones — so that "this run's generated and final documents
// were analyzed, and here is how they differ" is always recoverable from
// the memory store.
func (s *Store) summaryRecord(res *editorialedit.Result, src Source, now time.Time) []*memory.MemoryRecord {
	rec := memory.NewMemoryRecord(now)
	rec.Kind = memory.KindEditorialFeedback
	rec.Scope = scopeOf(src)
	rec.Subject = subjectOf(res, src)
	rec.Field = "summary"
	switch {
	case !res.Classified:
		rec.Confidence = memory.ConfidenceHigh // deterministic facts about the pair
	default:
		rec.Confidence = memory.ConfidenceMedium
	}
	rec.Volatility = memory.VolatileStatic
	rec.Freshness = memory.Freshness{LastVerifiedAt: now}
	rec.Expiration = memory.Expiration{InvalidationStatus: memory.InvalidationActive}
	rec.Provenance = provenanceOf(res, src, now)

	err := rec.EncodeValue(memory.FeedbackRecord{
		Feedback:  summaryFeedbackText(res),
		GivenBy:   editorOrClassifier(res, "analysis"),
		GivenAt:   now,
		Recurring: false,
	}, now)
	if err != nil {
		panic(fmt.Sprintf("feedback: encode summary: %v", err))
	}
	return []*memory.MemoryRecord{rec}
}

// summaryFeedbackText renders the pair-level summary.
func summaryFeedbackText(res *editorialedit.Result) string {
	var b strings.Builder
	if res.Identical {
		b.WriteString("Generated and final documents are identical (no editorial change).")
		return b.String()
	}
	fmt.Fprintf(&b, "Editorial diff of the final document: %d lines added, %d lines deleted",
		res.AddedLines, res.DeletedLines)
	if res.HeadlineChanged {
		b.WriteString(", headline changed")
	}
	fmt.Fprintf(&b, " (%d → %d words)", res.BeforeWords, res.AfterWords)
	switch {
	case res.Classified && res.FactualChange:
		fmt.Fprintf(&b, "; classified by %s; contains factual changes", orUnknown(res.Classifier))
	case res.Classified:
		fmt.Fprintf(&b, "; classified by %s; no factual changes", orUnknown(res.Classifier))
	default:
		b.WriteString("; deterministic diff only (no classifier ran)")
	}
	b.WriteString(".\nDeterministic diff (preserved):\n")
	b.WriteString(truncateDiff(res.DiffText))
	return b.String()
}

// editorOrClassifier picks the FeedbackRecord's GivenBy: the editor when the
// pair names one (the human who made the change), else the classifier name,
// else the fallback.
func editorOrClassifier(res *editorialedit.Result, fallback string) string {
	if res.Classified && res.Classifier != "" {
		return res.Classifier
	}
	return fallback
}

// joinCategories renders categories as "a, b, c" (canonical order, from the
// normalized slice).
func joinCategories(cs []editorialedit.Category) string {
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = string(c)
	}
	return strings.Join(parts, ", ")
}

// orUnknown renders a possibly-empty name as itself or "unknown".
func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// truncateDiff bounds diff text embedded in a payload.
func truncateDiff(s string) string {
	if len(s) <= maxDiffText {
		return s
	}
	return s[:maxDiffText] + "\n[…diff truncated]"
}

// truncExcerpt bounds a document excerpt on a correction record.
func truncExcerpt(s string) string {
	if len(s) <= 4096 {
		return s
	}
	return s[:4096] + "\n[…excerpt truncated]"
}
