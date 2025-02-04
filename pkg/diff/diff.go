// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package diffp implements a basic diff algorithm equivalent to patience diff.
// It is a copy of internal/diff from the main Go repo, renamed to diffp to avoid
// conflict with the existing golang.org/x/tools/internal/diff.
//
// It is a fork from <https://cs.opensource.google/go/x/tools/+/master:internal/diffp/>.
package diff

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
)

// Unified is returned by [Diff] as the representation of the unified diff.
type Unified struct {
	OldName string
	NewName string
	Hunks   []Hunk
}

// Hunk is a single hunk of the [Unified] diff.
type Hunk struct {
	LineOld  int
	CountOld int
	LineNew  int
	CountNew int
	Lines    []HunkLine
}

// SplitViewPaddings is used by the eventual template to determine the padding
// lines to write on the left and right hand side to align the diffs correctly.
func (h Hunk) SplitViewPaddings() struct{ Red, Green map[int]int } {
	red, green := map[int]int{}, map[int]int{}
	for i := 0; i < len(h.Lines); i++ {
		l := h.Lines[i]
		if l.Type() == TypeEqual {
			continue
		}
		ins, del := countNextInsertDelete(h.Lines[i:])
		if ins > del {
			red[i+del] = ins - del
		} else if del > ins {
			green[i+ins] = del - ins
		}
		i += ins + del - 1
	}
	// We have to return them like this due to text/template.
	return struct {
		Red   map[int]int
		Green map[int]int
	}{red, green}
}

func countNextInsertDelete(ll []HunkLine) (ins, del int) {
	for _, l := range ll {
		switch l.Type() {
		case TypeInsert:
			ins++
		case TypeDelete:
			del++
		default:
			return
		}
	}
	return
}

// HunkLine is an individual line in a [Hunk].
type HunkLine struct {
	NumberX int
	NumberY int
	Value   string
}

// Possible results of [HunkLine.Type].
const (
	TypeInsert  = "insert"
	TypeDelete  = "delete"
	TypeEqual   = "equal"
	TypeInvalid = "invalid"
)

func (l HunkLine) Type() string {
	switch l.Value[0] {
	case '+':
		return TypeInsert
	case '-':
		return TypeDelete
	case ' ':
		return TypeEqual
	}
	return TypeInvalid
}

func (l HunkLine) Symbol() byte {
	return l.Value[0]
}

func (l HunkLine) Content() string { return string(l.Value[1:]) }

func (d Unified) String() string {
	if len(d.Hunks) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "diff %s %s\n", d.OldName, d.NewName)
	fmt.Fprintf(&b, "--- %s\n", d.OldName)
	fmt.Fprintf(&b, "+++ %s\n", d.NewName)

	for _, hunk := range d.Hunks {
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", hunk.LineOld, hunk.CountOld, hunk.LineNew, hunk.CountNew)
		for _, s := range hunk.Lines {
			b.WriteString(string(s.Value))
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// A pair is a pair of values tracked for both the x and y side of a diff.
// It is typically a pair of line indexes.
type pair struct{ x, y int }

// Diff returns an anchored diff of the two texts old and new
// in the “unified diff” format. If old and new are identical,
// Diff returns a nil slice (no output).
//
// Unix diff implementations typically look for a diff with
// the smallest number of lines inserted and removed,
// which can in the worst case take time quadratic in the
// number of lines in the texts. As a result, many implementations
// either can be made to run for a long time or cut off the search
// after a predetermined amount of work.
//
// In contrast, this implementation looks for a diff with the
// smallest number of “unique” lines inserted and removed,
// where unique means a line that appears just once in both old and new.
// We call this an “anchored diff” because the unique lines anchor
// the chosen matching regions. An anchored diff is usually clearer
// than a standard diff, because the algorithm does not try to
// reuse unrelated blank lines or closing braces.
// The algorithm also guarantees to run in O(n log n) time
// instead of the standard O(n²) time.
//
// Some systems call this approach a “patience diff,” named for
// the “patience sorting” algorithm, itself named for a solitaire card game.
// We avoid that name for two reasons. First, the name has been used
// for a few different variants of the algorithm, so it is imprecise.
// Second, the name is frequently interpreted as meaning that you have
// to wait longer (to be patient) for the diff, meaning that it is a slower algorithm,
// when in fact the algorithm is faster than the standard one.
func Diff(oldName string, old []byte, newName string, new []byte) Unified {
	return DiffWithOptions(oldName, old, newName, new, Options{
		Context: 3,
	})
}

// Options are the options that can be passed to [DiffWithOptions].
type Options struct {
	// Normal is a function that "normalizes" the strings, to correct comparison.
	Normal func(s string) string
	// Context are the lines of context to add to the hunks.
	// [Diff] uses a default value of 3.
	Context int
}

// DiffWithOptions performs the diff on the given files, using the given [Options].
func DiffWithOptions(oldName string, old []byte, newName string, new []byte, opts Options) Unified {
	// TODO: Context lines should likely "intelligently" choose between the old
	// and new depending on whether the previous line was from the new or old text.
	// (This is useful when doing diff ignoring whitespace).

	u := Unified{OldName: oldName, NewName: newName}
	if bytes.Equal(old, new) {
		return u
	}
	xDisp, x := lines(old, opts.Normal)
	yDisp, y := lines(new, opts.Normal)

	// Loop over matches to consider,
	// expanding each match to include surrounding lines,
	// and then printing diff chunks.
	// To avoid setup/teardown cases outside the loop,
	// tgs returns a leading {0,0} and trailing {len(x), len(y)} pair
	// in the sequence of matches.
	var (
		done  pair       // printed up to x[:done.x] and y[:done.y]
		chunk pair       // start lines of current chunk
		count pair       // number of lines from each side in current chunk
		ctext []HunkLine // lines for current chunk
	)
	for _, m := range tgs(x, y) {
		if m.x < done.x {
			// Already handled scanning forward from earlier match.
			continue
		}

		// Expand matching lines as far possible,
		// establishing that x[start.x:end.x] == y[start.y:end.y].
		// Note that on the first (or last) iteration we may (or definitey do)
		// have an empty match: start.x==end.x and start.y==end.y.
		start := m
		for start.x > done.x && start.y > done.y && x[start.x-1] == y[start.y-1] {
			start.x--
			start.y--
		}
		end := m
		for end.x < len(x) && end.y < len(y) && x[end.x] == y[end.y] {
			end.x++
			end.y++
		}

		// Emit the mismatched lines before start into this chunk.
		// (No effect on first sentinel iteration, when start = {0,0}.)
		for _, s := range xDisp[done.x:start.x] {
			count.x++
			ctext = append(ctext, HunkLine{NumberX: chunk.x + count.x, NumberY: -1, Value: "-" + s})
		}
		for _, s := range yDisp[done.y:start.y] {
			count.y++
			ctext = append(ctext, HunkLine{NumberX: -1, NumberY: chunk.y + count.y, Value: "+" + s})
		}

		// If we're not at EOF and have too few common lines,
		// the chunk includes all the common lines and continues.
		if (end.x < len(x) || end.y < len(y)) &&
			(end.x-start.x < opts.Context || (len(ctext) > 0 && end.x-start.x < 2*opts.Context)) {
			for _, s := range xDisp[start.x:end.x] {
				count.x++
				count.y++
				ctext = append(ctext, HunkLine{NumberX: chunk.x + count.x, NumberY: chunk.y + count.y, Value: " " + s})
			}
			done = end
			continue
		}

		// End chunk with common lines for context.
		if len(ctext) > 0 {
			n := end.x - start.x
			if n > opts.Context {
				n = opts.Context
			}
			for _, s := range xDisp[start.x : start.x+n] {
				count.x++
				count.y++
				ctext = append(ctext, HunkLine{NumberX: chunk.x + count.x, NumberY: chunk.y + count.y, Value: " " + s})
			}
			done = pair{start.x + n, start.y + n}

			// Format and emit chunk.
			// Convert line numbers to 1-indexed.
			// Special case: empty file shows up as 0,0 not 1,0.
			if count.x > 0 {
				chunk.x++
			}
			if count.y > 0 {
				chunk.y++
			}
			u.Hunks = append(u.Hunks, Hunk{
				LineOld:  chunk.x,
				CountOld: count.x,
				LineNew:  chunk.y,
				CountNew: count.y,
				// Copy slice, as we re-use ctext.
				Lines: append(make([]HunkLine, 0, len(ctext)), ctext...),
			})
			count.x = 0
			count.y = 0
			ctext = ctext[:0]
		}

		// If we reached EOF, we're done.
		if end.x >= len(x) && end.y >= len(y) {
			break
		}

		// Otherwise start a new chunk.
		chunk = pair{end.x - opts.Context, end.y - opts.Context}
		for _, s := range xDisp[chunk.x:end.x] {
			count.x++
			count.y++
			ctext = append(ctext, HunkLine{NumberX: chunk.x + count.x, NumberY: chunk.y + count.y, Value: " " + s})
		}
		done = end
	}

	return u
}

// lines returns the lines in the file x, including newlines.
// If the file does not end in a newline, one is supplied
// along with a warning about the missing newline.
func lines(x []byte, normal func(s string) string) ([]string, []string) {
	// disp is how the lines are displayed and how they originate from the
	// source, while cmp is how they are compared.
	disp := strings.Split(string(x), "\n")
	if disp[len(disp)-1] == "" {
		disp = disp[:len(disp)-1]
	} else {
		// Treat last line as having a message about the missing newline attached,
		// using the same text as BSD/GNU diff (including the leading backslash).
		disp[len(disp)-1] += "\n\\ No newline at end of file"
	}
	if normal == nil {
		return disp, disp
	}

	cmp := make([]string, len(disp))
	for i, s := range disp {
		cmp[i] = normal(s)
	}
	return disp, cmp
}

// tgs returns the pairs of indexes of the longest common subsequence
// of unique lines in x and y, where a unique line is one that appears
// once in x and once in y.
//
// The longest common subsequence algorithm is as described in
// Thomas G. Szymanski, “A Special Case of the Maximal Common
// Subsequence Problem,” Princeton TR #170 (January 1975),
// available at https://research.swtch.com/tgs170.pdf.
func tgs(x, y []string) []pair {
	// Count the number of times each string appears in a and b.
	// We only care about 0, 1, many, counted as 0, -1, -2
	// for the x side and 0, -4, -8 for the y side.
	// Using negative numbers now lets us distinguish positive line numbers later.
	m := make(map[string]int)
	for _, s := range x {
		if c := m[s]; c > -2 {
			m[s] = c - 1
		}
	}
	for _, s := range y {
		if c := m[s]; c > -8 {
			m[s] = c - 4
		}
	}

	// Now unique strings can be identified by m[s] = -1+-4.
	//
	// Gather the indexes of those strings in x and y, building:
	//	xi[i] = increasing indexes of unique strings in x.
	//	yi[i] = increasing indexes of unique strings in y.
	//	inv[i] = index j such that x[xi[i]] = y[yi[j]].
	var xi, yi, inv []int
	for i, s := range y {
		if m[s] == -1+-4 {
			m[s] = len(yi)
			yi = append(yi, i)
		}
	}
	for i, s := range x {
		if j, ok := m[s]; ok && j >= 0 {
			xi = append(xi, i)
			inv = append(inv, j)
		}
	}

	// Apply Algorithm A from Szymanski's paper.
	// In those terms, A = J = inv and B = [0, n).
	// We add sentinel pairs {0,0}, and {len(x),len(y)}
	// to the returned sequence, to help the processing loop.
	J := inv
	n := len(xi)
	T := make([]int, n)
	L := make([]int, n)
	for i := range T {
		T[i] = n + 1
	}
	for i := 0; i < n; i++ {
		k := sort.Search(n, func(k int) bool {
			return T[k] >= J[i]
		})
		T[k] = J[i]
		L[i] = k + 1
	}
	k := 0
	for _, v := range L {
		if k < v {
			k = v
		}
	}
	seq := make([]pair, 2+k)
	seq[1+k] = pair{len(x), len(y)} // sentinel at end
	lastj := n
	for i := n - 1; i >= 0; i-- {
		if L[i] == k && J[i] < lastj {
			seq[k] = pair{xi[i], yi[J[i]]}
			k--
		}
	}
	seq[0] = pair{0, 0} // sentinel at start
	return seq
}
