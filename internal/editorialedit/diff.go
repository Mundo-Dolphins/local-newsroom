package editorialedit

import "strings"

// A deterministic line-based diff of two text documents.
//
// The diff is the ground-truth layer of editorial change analysis (issue
// #147): it is pure text in, text out, requires no model, and is byte-stable
// across runs — the same inputs always produce the same DiffDoc. No
// classification, no opinion: a hunk says what changed, not what it means.
//
// The algorithm is a longest-common-subsequence over lines (an O(n·m) DP
// table). It is deliberately the simple, exact variant rather than Myers'
// O((N+M)D) algorithm: run artifacts and edited finals are small (articles
// and threads run to a few hundred lines), and a stable, exact line diff is
// more valuable for a feedback-learning system than maximum diff
// compactness for large files.
//
// Conventions (unified diff):
//   - context lines (" ") carry both line numbers; additions ("+") and
//     deletions ("-") carry the one side they exist in;
//   - within a changed block, deletions precede additions (the standard
//     unified convention; the tie rule in the LCS walk enforces it);
//   - a hunk is a maximal change region padded with up to 3 context lines on
//     each side; two changes closer than 3 context lines share a hunk.

// LineChange classifies one line of a unified diff.
type LineChange string

const (
	// ChangeContext is an unchanged line (present in both documents).
	ChangeContext LineChange = "context"

	// ChangeAdd is a line only in the "after" document (added by the edit).
	ChangeAdd LineChange = "add"

	// ChangeDel is a line only in the "before" document (removed by the edit).
	ChangeDel LineChange = "del"
)

// AllLineChanges lists every recognized LineChange.
var AllLineChanges = []LineChange{ChangeContext, ChangeAdd, ChangeDel}

// Valid reports whether the change kind is a recognized value.
func (c LineChange) Valid() bool {
	for _, v := range AllLineChanges {
		if c == v {
			return true
		}
	}
	return false
}

// Line is one line of a unified diff.
type Line struct {
	// Change is the kind of this line (context, add, or del).
	Change LineChange
	// Content is the line text without its trailing newline.
	Content string
	// BeforeLine is the 1-based line number in the before document (set for
	// context and del lines).
	BeforeLine int
	// AfterLine is the 1-based line number in the after document (set for
	// context and add lines).
	AfterLine int
}

// Text renders the line in unified-diff syntax (" ", "+", or "-").
func (l Line) Text() string {
	var prefix string
	switch l.Change {
	case ChangeAdd:
		prefix = "+"
	case ChangeDel:
		prefix = "-"
	default:
		prefix = " "
	}
	return prefix + l.Content
}

// Hunk is a maximal run of diff lines that includes at least one change
// (add or del), padded on both sides by up to 3 context lines where the
// documents have them.
type Hunk struct {
	// Lines is the hunk in unified-diff order (context, del, add, ...).
	Lines []Line
	// StartBefore is the 1-based before-document line of the hunk's first
	// line (zero when the hunk's first line is a pure addition at the top of
	// the before document).
	StartBefore int
	// StartAfter is the 1-based after-document line of the hunk's first line
	// (zero when the hunk's first line is a pure deletion at the top of the
	// after document).
	StartAfter int
	// Added is the number of add lines in the hunk.
	Added int
	// Deleted is the number of del lines in the hunk.
	Deleted int
}

// Text renders the hunk as its lines joined by newlines, each in
// unified-diff syntax.
func (h Hunk) Text() string {
	var sb strings.Builder
	for i, l := range h.Lines {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(l.Text())
	}
	return sb.String()
}

// DiffDoc is a complete, self-contained description of the textual change
// from before to after.
type DiffDoc struct {
	// Before is the full before text.
	Before string
	// After is the full after text.
	After string
	// Hunks are the change hunks, in document order.
	Hunks []Hunk
	// AddedLines is the total number of added lines.
	AddedLines int
	// DeletedLines is the total number of deleted lines.
	DeletedLines int
}

// Identical reports whether the two texts are byte-identical (no hunks).
func (d DiffDoc) Identical() bool {
	return len(d.Hunks) == 0
}

// String renders the diff as a unified diff body (no file headers): every
// hunk, joined by blank lines, deterministic by construction.
func (d DiffDoc) String() string {
	parts := make([]string, len(d.Hunks))
	for i, h := range d.Hunks {
		parts[i] = h.Text()
	}
	return strings.Join(parts, "\n\n")
}

// splitLines splits text into lines without their trailing newlines. A
// trailing newline does not add a phantom empty line; a non-empty final line
// without a newline is kept. Empty input yields no lines.
func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// Diff computes the deterministic line diff between before and after.
//
// The result is a pure function of its two arguments: identical inputs
// always yield identical hunks, line numbers, and ordering. Empty against
// empty is an empty diff; an empty before produces one hunk of pure
// additions and vice versa.
func Diff(before, after string) DiffDoc {
	b := splitLines(before)
	a := splitLines(after)

	lines := lcsDiff(b, a)
	hunks := groupHunks(lines)

	return DiffDoc{
		Before:       before,
		After:        after,
		Hunks:        hunks,
		AddedLines:   countLines(lines, ChangeAdd),
		DeletedLines: countLines(lines, ChangeDel),
	}
}

// lcsDiff walks the LCS dynamic-programming table to produce the full
// unified-diff line sequence of the two line slices.
//
// The table t[i][j] is the length of the longest common subsequence of
// b[i:] and a[j:] (filled bottom-up). On a mismatch the walk emits the
// before line when t[i+1][j] >= t[i][j+1] — the tie goes to deletion, which
// is what places removals before additions inside a changed block.
func lcsDiff(b, a []string) []Line {
	nb, na := len(b), len(a)
	t := make([][]int, nb+1)
	for i := range t {
		t[i] = make([]int, na+1)
	}
	for i := nb - 1; i >= 0; i-- {
		for j := na - 1; j >= 0; j-- {
			if b[i] == a[j] {
				t[i][j] = t[i+1][j+1] + 1
			} else if t[i+1][j] >= t[i][j+1] {
				t[i][j] = t[i+1][j]
			} else {
				t[i][j] = t[i][j+1]
			}
		}
	}

	lines := make([]Line, 0, nb+na)
	i, j := 0, 0
	for i < nb && j < na {
		if b[i] == a[j] {
			lines = append(lines, Line{Change: ChangeContext, Content: b[i], BeforeLine: i + 1, AfterLine: j + 1})
			i++
			j++
		} else if t[i+1][j] >= t[i][j+1] {
			lines = append(lines, Line{Change: ChangeDel, Content: b[i], BeforeLine: i + 1})
			i++
		} else {
			lines = append(lines, Line{Change: ChangeAdd, Content: a[j], AfterLine: j + 1})
			j++
		}
	}
	for i < nb {
		lines = append(lines, Line{Change: ChangeDel, Content: b[i], BeforeLine: i + 1})
		i++
	}
	for j < na {
		lines = append(lines, Line{Change: ChangeAdd, Content: a[j], AfterLine: j + 1})
		j++
	}
	return lines
}

// contextPad is the number of context lines kept on each side of a change
// core (the unified-diff default). The tests in this package use a single
// context line per side.
const contextPad = 1

// groupHunks groups the diff line sequence into change-core spans, then
// pads each span with up to contextPad context lines per side (buildHunk)
// and computes the hunk metadata.
//
// Two change cores separated by at most 2×contextPad context lines merge
// into one span (the unified rule: the hunk padding would overlap, so the
// "separate" hunks would be one); a longer gap keeps them apart.
// groupHunks groups the diff line sequence into hunks.
//
// A hunk is a change core (a maximal run of add/del lines) padded with up to
// contextPad context lines on each side (buildHunk does the padding; this
// function only finds the cores). Two change cores separated by at most
// 2×contextPad context lines merge into one hunk (the unified rule); a
// longer gap closes the hunk.
// span is a half-open range over the diff line sequence; a change
// core, possibly extended to absorb neighbouring change cores with a
// short context gap.
type span struct{ lo, hi int }

func groupHunks(lines []Line) []Hunk {
	var spans []span
	for i := 0; i < len(lines); {
		if lines[i].Change == ChangeContext {
			i++
			continue
		}
		sp := span{lo: i, hi: i}
		for i < len(lines) && lines[i].Change != ChangeContext {
			sp.hi = i + 1
			i++
		}
		if len(spans) > 0 && sp.lo-spans[len(spans)-1].hi <= 2*contextPad {
			spans[len(spans)-1].hi = sp.hi // short gap: one hunk
			continue
		}
		spans = append(spans, sp)
	}
	if len(spans) == 0 {
		return nil
	}

	hunks := make([]Hunk, 0, len(spans))
	for _, sp := range spans {
		hunks = append(hunks, buildHunk(lines, &sp))
	}
	return hunks
}

// countLines counts diff lines of the given change kind.
func countLines(lines []Line, c LineChange) int {
	n := 0
	for _, l := range lines {
		if l.Change == c {
			n++
		}
	}
	return n
}
