package diff

import "strings"

// contextLines is how many unchanged lines to keep around each change.
const contextLines = 3

// maxEdits bounds the Myers search, and with it memory (quadratic in the
// number of edits); beyond it the values are shown as a wholesale
// replacement, which is how a human would read them anyway.
const maxEdits = 1500

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func wholeLines(s, op string) []Line {
	lines := splitLines(s)
	out := make([]Line, len(lines))
	for i, l := range lines {
		out[i] = Line{Op: op, Text: l}
	}
	return out
}

// lineDiff diffs two strings line by line and elides long unchanged runs.
func lineDiff(before, after string) []Line {
	a, b := splitLines(before), splitLines(after)
	ops, ok := myers(a, b)
	if !ok {
		return append(wholeLines(before, "-"), wholeLines(after, "+")...)
	}
	out := collapse(ops)
	if bn, an := strings.HasSuffix(before, "\n"), strings.HasSuffix(after, "\n"); bn != an {
		note := "a trailing newline is removed"
		if an {
			note = "a trailing newline is added"
		}
		out = append(out, Line{Op: "note", Text: note})
	}
	return out
}

// LineEdits is the shortest edit script from a to b, as " ", "-" and "+"
// lines. Lists too different to search are a wholesale replacement.
func LineEdits(a, b []string) []Line {
	if ops, ok := myers(a, b); ok {
		return ops
	}
	out := make([]Line, 0, len(a)+len(b))
	for _, l := range a {
		out = append(out, Line{Op: "-", Text: l})
	}
	for _, l := range b {
		out = append(out, Line{Op: "+", Text: l})
	}
	return out
}

// myers returns the shortest edit script between a and b.
func myers(a, b []string) ([]Line, bool) {
	n, m := len(a), len(b)
	limit := min(n+m, maxEdits)
	offset := limit + 1
	v := make([]int, 2*limit+3)
	// trace[d] keeps only the diagonals reachable at step d, so memory grows
	// with the square of the edit count rather than with the input length.
	var trace [][]int
	found := false
	for d := 0; d <= limit && !found; d++ {
		lo, hi := offset-d-1, offset+d+1
		snapshot := make([]int, hi-lo+1)
		copy(snapshot, v[lo:hi+1])
		trace = append(trace, snapshot)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = v[offset+k+1]
			} else {
				x = v[offset+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[offset+k] = x
			if x >= n && y >= m {
				found = true
				break
			}
		}
	}
	if !found {
		return nil, false
	}
	// Walk the trace backwards to recover the script.
	var rev []Line
	x, y := n, m
	for d := len(trace) - 1; d >= 0; d-- {
		snap := trace[d]
		base := offset - d - 1
		vd := func(k int) int { return snap[offset+k-base] }
		k := x - y
		var prevK int
		if k == -d || (k != d && vd(k-1) < vd(k+1)) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := vd(prevK)
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			x--
			y--
			rev = append(rev, Line{Op: " ", Text: a[x]})
		}
		if d > 0 {
			if x == prevX {
				y--
				rev = append(rev, Line{Op: "+", Text: b[y]})
			} else {
				x--
				rev = append(rev, Line{Op: "-", Text: a[x]})
			}
		}
	}
	out := make([]Line, len(rev))
	for i := range rev {
		out[i] = rev[len(rev)-1-i]
	}
	return out, true
}

// collapse replaces unchanged runs longer than the context window with a
// single "skip" line.
func collapse(ops []Line) []Line {
	var out []Line
	for i := 0; i < len(ops); {
		if ops[i].Op != " " {
			out = append(out, ops[i])
			i++
			continue
		}
		j := i
		for j < len(ops) && ops[j].Op == " " {
			j++
		}
		run := ops[i:j]
		head, tail := contextLines, contextLines
		if i == 0 {
			head = 0
		}
		if j == len(ops) {
			tail = 0
		}
		if len(run) <= head+tail+1 {
			out = append(out, run...)
		} else {
			out = append(out, run[:head]...)
			out = append(out, Line{Op: "skip", Skip: len(run) - head - tail})
			out = append(out, run[len(run)-tail:]...)
		}
		i = j
	}
	return out
}
