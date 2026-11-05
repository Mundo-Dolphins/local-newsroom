package editorialedit

import (
	"strings"
	"testing"
	"time"
)

// testNow is the fixed clock for deterministic tests.
var testNow = time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

// Diff produces the canonical hunks for a pair of documents.

func TestDiffIdentical(t *testing.T) {
	d := Diff("a\nb\nc\n", "a\nb\nc\n")
	if !d.Identical() {
		t.Fatalf("identical documents must yield an empty diff, got %d hunks", len(d.Hunks))
	}
	if d.String() != "" {
		t.Fatalf("identical documents must render empty, got %q", d.String())
	}
	if d.AddedLines != 0 || d.DeletedLines != 0 {
		t.Fatalf("identical documents have no changes, got +%d -%d", d.AddedLines, d.DeletedLines)
	}
}

func TestDiffEmptyVsEmpty(t *testing.T) {
	d := Diff("", "")
	if !d.Identical() {
		t.Fatal("empty vs empty must be identical")
	}
}

func TestDiffEmptyBefore(t *testing.T) {
	d := Diff("", "x\ny\n")
	if d.Identical() {
		t.Fatal("empty before must differ")
	}
	if len(d.Hunks) != 1 {
		t.Fatalf("want 1 hunk, got %d", len(d.Hunks))
	}
	if d.AddedLines != 2 || d.DeletedLines != 0 {
		t.Fatalf("want +2 -0, got +%d -%d", d.AddedLines, d.DeletedLines)
	}
	if d.Hunks[0].StartAfter != 1 {
		t.Fatalf("hunk must start at after line 1, got %d", d.Hunks[0].StartAfter)
	}
}

func TestDiffSingleLineEdit(t *testing.T) {
	before := "l1\nl2\nl3\n"
	after := "l1\nL2\nl3\n"
	d := Diff(before, after)
	if d.Identical() {
		t.Fatal("a changed line must produce a hunk")
	}
	if len(d.Hunks) != 1 {
		t.Fatalf("want 1 hunk, got %d", len(d.Hunks))
	}
	h := d.Hunks[0]
	if h.Deleted != 1 || h.Added != 1 {
		t.Fatalf("want 1 del + 1 add, got %d del + %d add", h.Deleted, h.Added)
	}
	// Deletions precede additions within a block (the unified convention),
	// surrounded by context.
	want := " l1\n-l2\n+L2\n l3"
	if got := h.Text(); got != want {
		t.Fatalf("hunk text mismatch:\n got %q\nwant %q", got, want)
	}
	if h.StartBefore != 1 || h.StartAfter != 1 {
		t.Fatalf("hunk must start at line 1 of both documents, got %d/%d", h.StartBefore, h.StartAfter)
	}
}

func TestDiffDeletedLine(t *testing.T) {
	d := Diff("a\nb\nc\n", "a\nc\n")
	if len(d.Hunks) != 1 {
		t.Fatalf("want 1 hunk, got %d", len(d.Hunks))
	}
	if d.AddedLines != 0 || d.DeletedLines != 1 {
		t.Fatalf("want +0 -1, got +%d -%d", d.AddedLines, d.DeletedLines)
	}
}

func TestDiffAddedLine(t *testing.T) {
	d := Diff("a\nc\n", "a\nb\nc\n")
	if d.AddedLines != 1 || d.DeletedLines != 0 {
		t.Fatalf("want +1 -0, got +%d -%d", d.AddedLines, d.DeletedLines)
	}
}

func TestDiffHunksMergeWithinContextWindow(t *testing.T) {
	// Two single-line edits separated by exactly 2 context lines (= 2*1,
	// the merge threshold with a 1-line pad: a gap of <= 2 context lines
	// merges because the hunk padding would otherwise overlap).
	before := "l1\nx1\na1\na2\nx2\n"
	after := "l1\nX1\na1\na2\nX2\n"
	d := Diff(before, after)
	if len(d.Hunks) != 1 {
		t.Fatalf("changes 2 context lines apart must share a hunk (unified rule), got %d hunks", len(d.Hunks))
	}
}

func TestDiffHunksSplitBeyondContextWindow(t *testing.T) {
	// Two edits separated by 7 context lines (> 2*3) form two hunks.
	before := "l1\nx1\na1\na2\na3\na4\na5\na6\na7\nx2\n"
	after := "l1\nX1\na1\na2\na3\na4\na5\na6\na7\nX2\n"
	d := Diff(before, after)
	if len(d.Hunks) != 2 {
		t.Fatalf("changes 7 context lines apart must form 2 hunks, got %d", len(d.Hunks))
	}
	// First hunk: one lead context line (l1), then the edit.
	if d.Hunks[0].StartBefore != 1 {
		t.Fatalf("first hunk lead context is line 1, got %d", d.Hunks[0].StartBefore)
	}
	// Second hunk: its first line is the single lead context line (a7, line
	// 9); the edit itself is on line 10.
	if d.Hunks[1].StartBefore != 9 {
		t.Fatalf("second hunk lead context is line 9, got %d", d.Hunks[1].StartBefore)
	}
	// The second hunk's first line is context; its start positions point at
	// the lead context lines (unified convention: hunk starts with context).
}

func TestDiffContextPadding(t *testing.T) {
	// A change with 5 context lines below keeps only 3.
	before := "h\nx\nb1\nb2\nb3\nb4\nb5\n"
	after := "h\nX\nb1\nb2\nb3\nb4\nb5\n"
	d := Diff(before, after)
	if len(d.Hunks) != 1 {
		t.Fatalf("want 1 hunk, got %d", len(d.Hunks))
	}
	h := d.Hunks[0]
	if len(h.Lines) != 4 { // " h" lead context, "-x", "+X", " b1" (1 of 5 kept)
		t.Fatalf("want 4 lines (1 lead ctx + 1 del + 1 add + 1 trail ctx), got %d: %q", len(h.Lines), h.Text())
	}
}

func TestDiffTrailingNewlineInsensitivity(t *testing.T) {
	// A trailing newline does not create a phantom changed line.
	d := Diff("a\nb", "a\nb\n")
	if d.AddedLines != 0 || d.DeletedLines != 0 {
		t.Fatalf("trailing newline difference must not count as a change, got +%d -%d", d.AddedLines, d.DeletedLines)
	}
}

func TestDiffHunkCountStability(t *testing.T) {
	// Determinism: repeated calls give byte-identical output.
	before := "h\nl1\nl2\nl3\nl4\nl5\n"
	after := "H\nl1\nL2\nl3\nl4\nextra\n"
	a := Diff(before, after)
	b := Diff(before, after)
	if a.String() != b.String() {
		t.Fatal("Diff must be deterministic")
	}
}

func TestDiffMultiLineChangeBlock(t *testing.T) {
	// A changed block of 2 lines: both deletions then both additions.
	before := "a\nb\nc\nd\ne\n"
	after := "a\nB1\nB2\nd\ne\n"
	d := Diff(before, after)
	if len(d.Hunks) != 1 {
		t.Fatalf("want 1 hunk, got %d", len(d.Hunks))
	}
	want := " a\n-b\n-c\n+B1\n+B2\n d"
	if got := d.Hunks[0].Text(); got != want {
		t.Fatalf("hunk text mismatch:\n got %q\nwant %q", got, want)
	}
}

func TestDiffStringRender(t *testing.T) {
	before := "a\nb\n"
	after := "a\nB\n"
	d := Diff(before, after)
	if !strings.Contains(d.String(), "-b") || !strings.Contains(d.String(), "+B") {
		t.Fatalf("rendered diff must contain the changed lines: %q", d.String())
	}
}
