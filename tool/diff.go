package tool

import (
	"fmt"
	"strings"
)

// diffContext is the number of unchanged lines shown around each hunk,
// the same 3 every diff reader expects.
const diffContext = 3

// diffLCSLimit guards the quadratic LCS table. Beyond this many changed
// lines on a side, the middle becomes one replace hunk, which is still a
// valid and readable diff, just coarser.
const diffLCSLimit = 2000

// UnifiedDiff renders a line-based unified diff between two versions of
// one file. The permission renderer (doc 05) and the edit display both
// lean on it, so it lives here rather than in a UI package.
func UnifiedDiff(path, oldText, newText string) string {
	if oldText == newText {
		return ""
	}
	oldLines := diffSplit(oldText)
	newLines := diffSplit(newText)

	// Trim the common prefix and suffix so the LCS only sees the churn.
	pre := 0
	for pre < len(oldLines) && pre < len(newLines) && oldLines[pre] == newLines[pre] {
		pre++
	}
	post := 0
	for post < len(oldLines)-pre && post < len(newLines)-pre &&
		oldLines[len(oldLines)-1-post] == newLines[len(newLines)-1-post] {
		post++
	}
	oldMid := oldLines[pre : len(oldLines)-post]
	newMid := newLines[pre : len(newLines)-post]

	ops := diffOps(oldMid, newMid)

	var b strings.Builder
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", strings.TrimPrefix(path, "/"), strings.TrimPrefix(path, "/"))
	writeHunks(&b, oldLines, pre, ops)
	return b.String()
}

// diffOp is one edit step over the trimmed middle: ' ' keep, '-' delete
// from old, '+' insert from new.
type diffOp struct {
	kind byte
	text string
}

// diffOps computes the edit script for the middle. Small inputs get a
// real LCS; oversized ones get delete-all-insert-all, guarded by
// diffLCSLimit so a generated file cannot stall the loop.
func diffOps(oldMid, newMid []string) []diffOp {
	if len(oldMid) > diffLCSLimit || len(newMid) > diffLCSLimit {
		ops := make([]diffOp, 0, len(oldMid)+len(newMid))
		for _, l := range oldMid {
			ops = append(ops, diffOp{'-', l})
		}
		for _, l := range newMid {
			ops = append(ops, diffOp{'+', l})
		}
		return ops
	}

	// Classic LCS table, then a backtrack into ops.
	m, n := len(oldMid), len(newMid)
	lcs := make([][]int, m+1)
	for i := range lcs {
		lcs[i] = make([]int, n+1)
	}
	for i := m - 1; i >= 0; i-- {
		for j := n - 1; j >= 0; j-- {
			if oldMid[i] == newMid[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < m && j < n {
		switch {
		case oldMid[i] == newMid[j]:
			ops = append(ops, diffOp{' ', oldMid[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, diffOp{'-', oldMid[i]})
			i++
		default:
			ops = append(ops, diffOp{'+', newMid[j]})
			j++
		}
	}
	for ; i < m; i++ {
		ops = append(ops, diffOp{'-', oldMid[i]})
	}
	for ; j < n; j++ {
		ops = append(ops, diffOp{'+', newMid[j]})
	}
	return ops
}

// writeHunks groups ops into @@ hunks with diffContext lines of context,
// pulling extra context from the trimmed prefix and suffix when the
// churn sits near either end of the middle.
func writeHunks(b *strings.Builder, oldLines []string, pre int, ops []diffOp) {
	// Rebuild a full op list with the trimmed prefix and suffix as keeps,
	// so hunk grouping and line numbering work over one sequence.
	full := make([]diffOp, 0, pre+len(ops)+len(oldLines))
	for _, l := range oldLines[:pre] {
		full = append(full, diffOp{' ', l})
	}
	full = append(full, ops...)
	post := len(oldLines) - pre - countKind(ops, ' ') - countKind(ops, '-')
	for _, l := range oldLines[len(oldLines)-post:] {
		full = append(full, diffOp{' ', l})
	}

	// Walk ops tracking both line cursors; open a hunk at the first
	// change, close it when a gap of keeps exceeds twice the context.
	type hunk struct {
		oldStart, newStart int // 1-based
		ops                []diffOp
	}
	var hunks []hunk
	oldLn, newLn := 1, 1
	i := 0
	for i < len(full) {
		if full[i].kind == ' ' {
			oldLn++
			newLn++
			i++
			continue
		}
		// Found a change; back up for leading context.
		start := i
		lead := min(diffContext, countLeadingKeeps(full, i))
		h := hunk{oldStart: oldLn - lead, newStart: newLn - lead}
		for k := start - lead; k < start; k++ {
			h.ops = append(h.ops, full[k])
		}
		// Consume changes and keeps until the keep run reaches the point
		// where two hunks would no longer merge.
		for i < len(full) {
			if full[i].kind != ' ' {
				h.ops = append(h.ops, full[i])
				if full[i].kind == '-' {
					oldLn++
				} else {
					newLn++
				}
				i++
				continue
			}
			run := keepRun(full, i)
			if i+run >= len(full) || run > 2*diffContext {
				trail := min(diffContext, run)
				for k := range trail {
					h.ops = append(h.ops, full[i+k])
				}
				oldLn += run
				newLn += run
				i += run
				break
			}
			for k := range run {
				h.ops = append(h.ops, full[i+k])
			}
			oldLn += run
			newLn += run
			i += run
		}
		hunks = append(hunks, h)
	}

	for _, h := range hunks {
		oldN := countKind(h.ops, ' ') + countKind(h.ops, '-')
		newN := countKind(h.ops, ' ') + countKind(h.ops, '+')
		fmt.Fprintf(b, "@@ -%d,%d +%d,%d @@\n", h.oldStart, oldN, h.newStart, newN)
		for _, op := range h.ops {
			b.WriteByte(op.kind)
			b.WriteString(op.text)
			b.WriteByte('\n')
		}
	}
}

func countKind(ops []diffOp, kind byte) int {
	n := 0
	for _, op := range ops {
		if op.kind == kind {
			n++
		}
	}
	return n
}

// countLeadingKeeps counts consecutive keeps immediately before ops[i].
func countLeadingKeeps(ops []diffOp, i int) int {
	n := 0
	for k := i - 1; k >= 0 && ops[k].kind == ' '; k-- {
		n++
	}
	return n
}

// keepRun counts consecutive keeps starting at ops[i].
func keepRun(ops []diffOp, i int) int {
	n := 0
	for k := i; k < len(ops) && ops[k].kind == ' '; k++ {
		n++
	}
	return n
}

// diffSplit splits into lines for diffing, trimming one trailing newline
// the same way splitLines does, and treating empty text as zero lines.
func diffSplit(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}
