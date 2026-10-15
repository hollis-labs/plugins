package forgeplugin

import (
	"fmt"
	"strings"
)

// diffContext is how many unchanged lines surround each change in Unified.
const diffContext = 2

// maxDiffCells bounds the line-diff table (current lines × proposed lines).
// A deployment script is a few dozen lines; past this the preview reports a
// whole-script replacement rather than spending the call on a table.
const maxDiffCells = 4_000_000

type diffOp struct {
	kind byte // ' ', '-', '+'
	line string
}

// diffScripts compares the current deployment script with the proposed one.
func diffScripts(current, proposed string) ScriptDiff {
	if current == proposed {
		return ScriptDiff{Changed: false}
	}
	a, b := splitLines(current), splitLines(proposed)
	if len(a)*len(b) > maxDiffCells {
		return ScriptDiff{Changed: true, Added: len(b), Removed: len(a), Truncated: true}
	}
	ops := lineDiff(a, b)
	out := ScriptDiff{Changed: true}
	for _, op := range ops {
		switch op.kind {
		case '+':
			out.Added++
		case '-':
			out.Removed++
		}
	}
	out.Unified = unified(ops)
	return out
}

// splitLines splits a script into lines. A trailing newline does not make an
// empty last line, and "" is no lines at all.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// lineDiff is a longest-common-subsequence diff: every line is kept, removed
// or added, in order.
func lineDiff(a, b []string) []diffOp {
	n, m := len(a), len(b)
	// lcs[i][j] is the LCS length of a[i:] and b[j:].
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	ops := make([]diffOp, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{' ', a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, diffOp{'-', a[i]})
			i++
		default:
			ops = append(ops, diffOp{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{'-', a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{'+', b[j]})
	}
	return ops
}

// unified renders the changes as unified-diff hunks, each with diffContext
// lines of context and a "@@ -start,count +start,count @@" header.
func unified(ops []diffOp) string {
	var b strings.Builder
	for start := 0; start < len(ops); {
		// Find the next change.
		first := start
		for first < len(ops) && ops[first].kind == ' ' {
			first++
		}
		if first == len(ops) {
			break
		}
		// Extend the hunk while changes are within 2*context of each other.
		last := first
		for k := first; k < len(ops); k++ {
			if ops[k].kind != ' ' {
				last = k
				continue
			}
			if k-last > 2*diffContext {
				break
			}
		}
		lo := max(first-diffContext, start)
		hi := min(last+diffContext, len(ops)-1)

		oldStart, newStart := 1, 1
		for _, op := range ops[:lo] {
			if op.kind != '+' {
				oldStart++
			}
			if op.kind != '-' {
				newStart++
			}
		}
		oldCount, newCount := 0, 0
		for _, op := range ops[lo : hi+1] {
			if op.kind != '+' {
				oldCount++
			}
			if op.kind != '-' {
				newCount++
			}
		}
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		for _, op := range ops[lo : hi+1] {
			b.WriteByte(op.kind)
			b.WriteString(op.line)
			b.WriteByte('\n')
		}
		start = hi + 1
	}
	return b.String()
}
