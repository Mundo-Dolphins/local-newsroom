package feedback

import (
	"context"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/editorialedit"
	"github.com/Mundo-Dolphins/local-newsroom/internal/memory"
	"github.com/Mundo-Dolphins/local-newsroom/internal/memorystore"
)

var testNow = time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

func newTestStore(t *testing.T) memorystore.Store {
	t.Helper()
	s := memorystore.NewFakeStore()
	return s
}

// analysisFrom builds a minimal-but-real analysis result for the store under
// test (a two-hunk document, one factual correction, one style edit).
func analysisFrom() *editorialedit.Result {
	before := "alpha\nfact one said 2025\ngamma\ndelta\nepsilon\nzeta\neta\ntheta\niota\n"
	after := "alpha\nfact one said 2026\ngamma\ndelta\nepsilon\nzeta\neta\ntheta\ntone edit\n"
	// Simulate a successful advisory classification (one label per hunk).
	fake := &stubClassifier{}
	res, err := editorialedit.Analyze(context.Background(), fake, "gen", before, "fin", after, testNow)
	if err != nil {
		panic(err)
	}
	return res
}

// stubClassifier labels each hunk in order: a factual correction (high
// confidence) for the first, a tone/style edit (lower) for the rest.
type stubClassifier struct{}

func (c *stubClassifier) Classify(ctx context.Context, units []editorialedit.ClassificationUnit) ([]editorialedit.Classification, error) {
	out := make([]editorialedit.Classification, len(units))
	for i := range out {
		if i == 0 {
			out[i] = editorialedit.Classification{
				Categories: []editorialedit.Category{editorialedit.CategoryFactualCorrection},
				Confidence: 0.9,
				Note:       "the year was wrong",
			}
		} else {
			out[i] = editorialedit.Classification{
				Categories: []editorialedit.Category{editorialedit.CategoryToneStyle},
				Confidence: 0.6,
			}
		}
	}
	return out, nil
}

func (c *stubClassifier) Name() string { return "stub" }

// TestStoreAnalysisPersistsRecords: the analysis produces a summary record
// plus one hunk record plus one correction record, all retrievable by their
// derived stable ids.
func TestStoreAnalysisPersistsRecords(t *testing.T) {
	s := newTestStore(t)
	fs := NewStore(s, func() time.Time { return testNow })
	res := analysisFrom()
	src := Source{RunID: "run-1", Recipe: "news", GeneratedPath: "final/generated/x.md", FinalPath: "final/x.md", Editor: "javi"}

	ids, err := fs.StoreAnalysis(context.Background(), res, src, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) == 0 {
		t.Fatal("at least the summary record must be written")
	}
	for _, id := range ids {
		rec, err := s.Get(context.Background(), memory.MemoryStableID(id))
		if err != nil {
			t.Fatalf("record %s not readable: %v", id, err)
		}
		if rec.StableID != memory.MemoryStableID(id) {
			t.Fatalf("stable id mismatch: %s != %s", rec.StableID, id)
		}
	}
}

// TestStoreAnalysisIdempotent: storing the same analysis twice writes the
// same content-addressed records (upsert, not duplication).
func TestStoreAnalysisIdempotent(t *testing.T) {
	s := newTestStore(t)
	fs := NewStore(s, func() time.Time { return testNow })
	res := analysisFrom()
	src := Source{RunID: "run-1", Recipe: "news"}

	first, err := fs.StoreAnalysis(context.Background(), res, src, testNow)
	if err != nil {
		t.Fatal(err)
	}
	second, err := fs.StoreAnalysis(context.Background(), res, src, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(second) {
		t.Fatalf("re-run must derive the same record count: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("re-run must be idempotent: record %d %s vs %s", i, first[i], second[i])
		}
	}
}

// TestStoreAnalysisIdenticalPair: an identical pair still produces a summary
// record (no hunk or correction records).
func TestStoreAnalysisIdenticalPair(t *testing.T) {
	s := newTestStore(t)
	fs := NewStore(s, func() time.Time { return testNow })
	res, err := editorialedit.Analyze(context.Background(), nil, "g", "same\n", "f", "same\n", testNow)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := fs.StoreAnalysis(context.Background(), res, Source{RunID: "run-2"}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Fatalf("an identical pair yields exactly the summary record, got %d", len(ids))
	}
}

// TestStoreAnalysisClassifierFailure: a classifier that failed still stores
// the deterministic diff (summary only) — the failure never blocks
// persistence.
func TestStoreAnalysisClassifierFailure(t *testing.T) {
	s := newTestStore(t)
	fs := NewStore(s, func() time.Time { return testNow })
	before := "a\nx\nb\n"
	after := "a\nX\nb\n"
	failing := &failingClassifier{}
	res, _ := editorialedit.Analyze(context.Background(), failing, "g", before, "f", after, testNow)
	ids, err := fs.StoreAnalysis(context.Background(), res, Source{RunID: "run-3", Recipe: "r"}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Fatalf("an unclassified pair yields exactly the summary record, got %d", len(ids))
	}
}

type failingClassifier struct{}

func (c *failingClassifier) Classify(ctx context.Context, units []editorialedit.ClassificationUnit) ([]editorialedit.Classification, error) {
	return nil, context.DeadlineExceeded
}
func (c *failingClassifier) Name() string { return "failing" }
