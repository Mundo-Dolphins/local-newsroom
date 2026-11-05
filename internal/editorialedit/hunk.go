package editorialedit

// buildHunk pads the change-core span with up to contextPad context lines on
// each side (the unified-diff convention) and computes the hunk metadata.
//
// The span is exactly the maximal run of add/del lines (half-open bounds
// into the full diff line sequence); the padding reaches into the
// surrounding context and is trimmed to contextPad lines per side. Leading
// context is trimmed from the end of the leading run (keeping the lines
// closest to the core), trailing context from its start.
//
// The pad loops use explicit counters (not expressions over the moving
// bound): a bound-relative expression would start equal on both sides of
// its comparison and degenerate into an infinite loop or an over-pad.
func buildHunk(lines []Line, sp *span) Hunk {
	lo := sp.lo
	lead := 0
	for lo > 0 && lines[lo-1].Change == ChangeContext {
		lo--
		lead++
		if lead >= contextPad {
			break
		}
	}

	hi := sp.hi
	trail := 0
	for hi < len(lines) && lines[hi].Change == ChangeContext {
		hi++
		trail++
		if trail >= contextPad {
			break
		}
	}

	sub := lines[lo:hi]

	h := Hunk{Lines: sub}
	for _, l := range sub {
		switch l.Change {
		case ChangeAdd:
			h.Added++
		case ChangeDel:
			h.Deleted++
		}
		if h.StartBefore == 0 && l.BeforeLine > 0 {
			h.StartBefore = l.BeforeLine
		}
		if h.StartAfter == 0 && l.AfterLine > 0 {
			h.StartAfter = l.AfterLine
		}
	}
	return h
}
