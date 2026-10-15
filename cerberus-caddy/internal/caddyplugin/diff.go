package caddyplugin

import (
	"fmt"
	"strings"
)

const diffContext = 3

type diffLine struct {
	kind byte // ' ', '-' or '+'
	text string
}

// unifiedDiff returns a unified diff of two texts, or "" when they are equal.
// The files here are small, so a plain LCS table is enough and keeps the
// plugin free of a diff dependency.
func unifiedDiff(oldName, newName, oldText, newText string) string {
	if oldText == newText {
		return ""
	}
	a := splitLines(oldText)
	b := splitLines(newText)
	ops := diffLines(a, b)

	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", oldName, newName)

	// Walk the edit script, emitting a hunk around each run of changes.
	n := len(ops)
	i := 0
	for i < n {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		// Start the hunk diffContext lines before the first change.
		start := i - diffContext
		if start < 0 {
			start = 0
		}
		// Extend while another change falls within 2*diffContext lines.
		last := i
		for j := i + 1; j < n && j-last <= 2*diffContext; j++ {
			if ops[j].kind != ' ' {
				last = j
			}
		}
		end := last + diffContext + 1
		if end > n {
			end = n
		}
		writeHunk(&out, ops, start, end)
		i = end
	}
	return out.String()
}

func writeHunk(out *strings.Builder, ops []diffLine, start, end int) {
	oldStart, newStart := 1, 1
	for _, op := range ops[:start] {
		if op.kind != '+' {
			oldStart++
		}
		if op.kind != '-' {
			newStart++
		}
	}
	oldCount, newCount := 0, 0
	for _, op := range ops[start:end] {
		if op.kind != '+' {
			oldCount++
		}
		if op.kind != '-' {
			newCount++
		}
	}
	if oldCount == 0 {
		oldStart--
	}
	if newCount == 0 {
		newStart--
	}
	fmt.Fprintf(out, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
	for _, op := range ops[start:end] {
		out.WriteByte(op.kind)
		out.WriteString(op.text)
		out.WriteByte('\n')
	}
}

func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// diffLines is the full edit script between a and b.
func diffLines(a, b []string) []diffLine {
	n, m := len(a), len(b)
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
	var ops []diffLine
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffLine{' ', a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, diffLine{'-', a[i]})
			i++
		default:
			ops = append(ops, diffLine{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffLine{'-', a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffLine{'+', b[j]})
	}
	return ops
}
