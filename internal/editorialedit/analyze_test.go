package editorialedit

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// The generated-vs-final documents used throughout the analyzer tests.
const (
	genArticle = `# Kane Signs for Bayern

The transfer was confirmed by both clubs on Monday. Kane scored 41 goals in
his first season.

### Background

Kane's move ends a long pursuit.
`

	finalArticle = `# Kane Signs for Bayern

The transfer was confirmed by both clubs on Monday. Kane scored 40 goals in
his first season.

### Background

Kane's move ends a long pursuit. Bayern confirmed the signing in a statement.
`
)

// threadPair: a generated thread and its edited final (post boundary
// changed + a reworded line), used for the thread-support acceptance check.
const (
	genThread = `First post here.
Second line.

Post two content.
`
	finalThread = `First post, here.
Second line.

Post two content.
Post three.
`
)

func TestAnalyzeDeterministicOnly(t *testing.T) {
	res, err := Analyze(context.Background(), nil, "generated.txt", genArticle, "final.txt", finalArticle, testNow)
	if err != nil {
		t.Fatalf("deterministic analysis must not fail: %v", err)
	}
	if res.Identical {
		t.Fatal("the documents differ; Identical must be false")
	}
	if res.Classified {
		t.Fatal("no classifier => Classified must be false")
	}
	if res.DiffText == "" {
		t.Fatal("the deterministic diff must be preserved even without a classifier")
	}
	// The factual number change must appear in the diff.
	if !strings.Contains(res.DiffText, "41") || !strings.Contains(res.DiffText, "40") {
		t.Fatalf("diff must contain the changed number: %q", res.DiffText)
	}
	if res.AddedLines != 2 || res.DeletedLines != 2 {
		t.Fatalf("want +2 -2, got +%d -%d", res.AddedLines, res.DeletedLines)
	}
	if !res.HeadlineChanged == false {
		// headlines are equal here
		t.Fatal("headlines are unchanged; HeadlineChanged must be false")
	}
	if res.BeforeWords == 0 || res.AfterWords == 0 {
		t.Fatal("word counts must be computed")
	}
	if res.BeforeDigest == "" || res.AfterDigest == "" {
		t.Fatal("digests must be recorded")
	}
}

func TestAnalyzeIdentical(t *testing.T) {
	res, err := Analyze(context.Background(), NewFakeClassifier(Classification{Categories: []Category{CategoryToneStyle}}), "g", genArticle, "f", genArticle, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Identical {
		t.Fatal("identical documents must report Identical")
	}
	if res.Classified {
		t.Fatal("an identical pair must not be classified")
	}
	if res.DiffText != "" {
		t.Fatal("no change => no diff text")
	}
}

func TestAnalyzeWithFakeClassifier(t *testing.T) {
	fake := NewFakeClassifier(Classification{
		Categories: []Category{CategoryFactualCorrection, CategoryVocabularyTranslation},
		Note:       "the editor corrected the goal total",
		Confidence: 0.9,
	})
	res, err := Analyze(context.Background(), fake, "generated.txt", genArticle, "final.txt", finalArticle, testNow)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if !res.Classified {
		t.Fatal("a successful classifier must mark the result classified")
	}
	if res.Classifier != "fake" {
		t.Fatalf("classifier name = %q, want fake", res.Classifier)
	}
	if !res.FactualChange {
		t.Fatal("a factual_correction label must set FactualChange")
	}
	if !res.HighConfidence {
		t.Fatal("a high-confidence factual label must set HighConfidence")
	}
	if res.ClassifiedAt != testNow {
		t.Fatalf("ClassifiedAt = %v, want %v", res.ClassifiedAt, testNow)
	}
	if len(res.Classifications) == 0 {
		t.Fatal("each hunk must carry a classification")
	}
	for _, cl := range res.Classifications {
		if len(cl.Categories) == 0 {
			t.Fatal("a classification must carry at least one (possibly none) category")
		}
	}
}

func TestAnalyzeClassifierFailsDegradesToDiff(t *testing.T) {
	fake := NewFakeClassifier(Classification{})
	fake.Err = errors.New("model unavailable")
	res, err := Analyze(context.Background(), fake, "g", genArticle, "f", finalArticle, testNow)
	if err == nil {
		t.Fatal("a failing classifier must surface an error")
	}
	if res == nil {
		t.Fatal("the deterministic result must still be returned")
	}
	if res.Classified {
		t.Fatal("a failed classifier must not leave Classified true")
	}
	if res.DiffText == "" {
		t.Fatal("the deterministic diff must survive the classifier failure")
	}
	if res.FactualChange {
		t.Fatal("without a classifier, no factual assertion may be made")
	}
}

func TestAnalyzeClassifierCountMismatch(t *testing.T) {
	// A classifier that labels fewer units than it was given violates the
	// contract; the analyzer must reject the labels (error) while keeping
	// the diff. (Two hunks, one label: the stub returns a single
	// classification for the two units.)
	fake := &countingStub{inner: NewFakeClassifier(Classification{Categories: []Category{CategoryToneStyle}})}
	twoHunksBefore := "l1\nx1\nc1\nc2\nc3\nc4\nc5\nc6\nc7\nx2\n"
	twoHunksAfter := "l1\nX1\nc1\nc2\nc3\nc4\nc5\nc6\nc7\nX2\n"
	res, err := Analyze(context.Background(), fake, "g", twoHunksBefore, "f", twoHunksAfter, testNow)
	if err == nil {
		t.Fatal("a count mismatch must be an error")
	}
	if res.Classified || len(res.Classifications) != 0 {
		t.Fatal("mismatched labels must not enter the result")
	}
	if res.DiffText == "" {
		t.Fatal("the diff must survive")
	}
}

// countingStub returns a fixed number of classifications (a contract
// violation when it differs from the unit count) — it always returns one.
type countingStub struct {
	inner *FakeClassifier
}

func (s *countingStub) Classify(ctx context.Context, units []ClassificationUnit) ([]Classification, error) {
	ans, err := s.inner.Classify(ctx, units)
	if err != nil {
		return nil, err
	}
	if len(ans) > 1 {
		ans = ans[:1]
	}
	return ans, nil
}

func (s *countingStub) Name() string { return s.inner.Name() }

// TestAnalyzeTruncatesHugeTexts: the result embeds bounded excerpts while the
// digests cover the full texts.
func TestAnalyzeTruncatesHugeTexts(t *testing.T) {
	before := strings.Repeat("word ", 5000) + "\n"
	after := strings.Repeat("Word ", 5000) + "\n"
	res, err := Analyze(context.Background(), nil, "g", before, "f", after, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Before) >= len(before) || len(res.After) >= len(after) {
		t.Fatal("oversized texts must be truncated on the result")
	}
	if res.BeforeDigest == res.AfterDigest {
		t.Fatal("digests must reflect the full (differing) texts")
	}
}

// TestAnalyzeThreadPair: threads (plain text, post boundaries on blank
// lines) analyze through the same API as articles.
func TestAnalyzeThreadPair(t *testing.T) {
	res, err := Analyze(context.Background(), nil, "thread.txt", genThread, "thread-final.txt", finalThread, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if res.Identical {
		t.Fatal("the thread changed")
	}
	if !strings.Contains(res.DiffText, "First post") {
		t.Fatalf("thread diff must contain the changed post content: %q", res.DiffText)
	}
}

// TestAnalyzeDeterminism: identical inputs, identical results.
func TestAnalyzeDeterminism(t *testing.T) {
	a, _ := Analyze(context.Background(), nil, "g", genArticle, "f", finalArticle, testNow)
	b, _ := Analyze(context.Background(), nil, "g", genArticle, "f", finalArticle, testNow)
	if a.DiffText != b.DiffText || a.AddedLines != b.AddedLines || a.DeletedLines != b.DeletedLines ||
		a.BeforeDigest != b.BeforeDigest || a.AfterDigest != b.AfterDigest {
		t.Fatal("the analysis must be a pure function of its inputs")
	}
}

// TestAnalyzeZeroTimeDefaultsToNow: a zero now is replaced with the current
// time (a zero-value result timestamp must never be recorded).
func TestAnalyzeZeroTimeDefaultsToNow(t *testing.T) {
	_ = zeroReplacer(time.Time{})
	res, _ := Analyze(context.Background(), NewFakeClassifier(Classification{Categories: []Category{CategoryToneStyle}}), "g", genArticle, "f", finalArticle, time.Time{})
	if res.ClassifiedAt.IsZero() {
		t.Fatal("a zero timestamp must be replaced with the current time")
	}
}
