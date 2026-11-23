// Why: Mk's std is meant to keep up with other languages' standard
// libraries, so the idiomatic tables judge every Mk cell against two bars
// and say where std falls short.
//
// What: renderStdBars (the std bars section of RESULTS.md), the thresholds
// stdBar and stdDefect, the closed exception table stdExceptions, stdName,
// checkedBuild, timed and stdTally.
//
// How: each Mk cell's ratio to the best checked peer (the fastest C, Rust,
// Zig or Go cell whose build checks bounds and traps overflow) is judged
// against 1.25, and its ratio to the best of C, Rust, Zig and Go in any
// class against 2.0, each by its bootstrap interval. The http row has its
// own section and measure (req/s, httpbench.go) and is not judged here.

package main

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
)

// The two std thresholds. stdBar: "a ratio worse than 1.25 of the best
// needs a written reason and a plan"; stdDefect: "a std function more than
// 2 times slower than the best of C, Rust, Zig and Go" is a defect, the
// entries of stdExceptions excepted.
const (
	stdBar    = 1.25
	stdDefect = 2.0
)

// stdName checks bench.json's std names (rows.go). A std name is std, its
// module, any lower-case names (a builtin type's, as in
// std.core.str.ParseInt) and one or more exported names, with no empty
// element.
var stdName = regexp.MustCompile(`^std(\.[a-z][a-z0-9_]*)+(\.[A-Z][A-Za-z0-9_]*)+$`)

// stdExceptions is the closed table of the 2x rule's stated exceptions:
// each exception's key and the one std function it excepts. bench.json's
// exception must be one of them, on a row whose std is exactly that
// function (rows.go), so no other key, and no other row, turns an
// over verdict into a stated exception.
var stdExceptions = map[string]string{
	"crypto-random": "std.crypto.Random",
	"sort-stable":   "std.core.Slice.SortStable",
}

// colFacts is the column id as the results file recorded it, or, for a file
// older than the column, as tools/bench defines it now.
func colFacts(res *results, id string) (column, bool) {
	for _, c := range res.Columns {
		if c.ID == id {
			return c, true
		}
	}
	return columnByID(id)
}

// isMkCol reports whether id is one of Mk's columns (mk, mk-wrap, mk-keyed,
// mk-ctx): the cells the std bars judge, never a reference.
func isMkCol(res *results, id string) bool {
	c, ok := colFacts(res, id)
	if !ok {
		return strings.HasPrefix(id, "mk")
	}
	return isMkLang(c.Lang)
}

// budgetCol reports whether id is a check-budget column (mk-nobounds,
// mk-noovf): a build with checks removed, neither judged by the std bars
// nor a reference.
func budgetCol(res *results, id string) bool {
	c, ok := colFacts(res, id)
	return ok && c.Budget
}

// checkedBuild reports whether column c's own build checks bounds and traps
// overflow (rust-ovf, zig-safe). It reads the column's facts, not
// the cell's class: in a row whose overflow class is "wraps" every
// column is classed "bounds", though rust-ovf and zig-safe still build the
// row's program with overflow traps (Zig compiles its std with the
// program; Rust's std is prebuilt).
func checkedBuild(c column) bool {
	return c.Bounds == "checked" && strings.HasPrefix(c.Overflow, "trap")
}

// rowSource is where a row's bench.json lives, from its id: a lane row's id
// is "<lane>/<row>" (row.id).
func rowSource(id string) string {
	if lane, _, ok := strings.Cut(id, "/"); ok {
		return "tests/bench-lanes/" + lane
	}
	return "tests/bench/idiomatic"
}

// stdLine is the summary line of a row that names its std functions: each
// name in code quotes, in bench.json's order, or "" for a row that names
// none. Rows with the same list share a line.
func stdLine(std []string) string {
	q := make([]string, len(std))
	for i, n := range std {
		q[i] = "`" + n + "`"
	}
	return strings.Join(q, ", ")
}

// timed reports whether c has a time to divide by: every time above 0, so
// the median is positive and every resampled ratio of its interval is
// finite and positive. A kernel cell whose baseline is as slow as one
// of its runs has none.
func timed(c *cell) bool {
	if c == nil || c.Excluded != "" || len(c.Times) == 0 {
		return false
	}
	for _, t := range c.Times {
		if !(t > 0) || math.IsInf(t, 1) {
			return false
		}
	}
	return true
}

// stdRatio is mk's ratio to ref, or no ratio when either has no time.
func stdRatio(mk, ref *cell) ratioEntry {
	if !timed(mk) || !timed(ref) {
		return ratioEntry{Ratio: math.NaN(), CI: interval{Lo: math.NaN(), Hi: math.NaN()}}
	}
	return ratioTo(mk, ref)
}

// barClass is classify against t, or "-" for a ratio that does not exist.
func barClass(e ratioEntry, t float64) string {
	if math.IsNaN(e.Ratio) || math.IsNaN(e.CI.Lo) || math.IsNaN(e.CI.Hi) || e.Ratio <= 0 {
		return "-"
	}
	return classify(e.CI, t)
}

// stdTally is one summary line of the std bars. Its arithmetic: cells =
// within + inconclusive + over + noPeer + peerNoKernel, the 1.25 bar's
// verdicts and the timed Mk cells it gives none; noKernel counts the Mk
// cells with no time, which are not in cells; bestNoKernel counts the
// timed Mk cells with no 2x verdict because the fastest cell of any
// class has no time.
type stdTally struct {
	cells, within, inconclusive, over, noPeer, peerNoKernel, noKernel int
	defect, excepted, bestNoKernel, peerDefect                        int
	runs                                                              []int
}

// renderStdBars writes the std bars of table tb: for every Mk cell of every
// row, its ratio to the best checked peer (the 1.25 bar) and to
// the best cell of C, Rust, Zig and Go in any class (the 2x rule, by its
// letter), each classified against 1.25 and 2.0 by its interval,
// then a summary per std function (bench.json "std") or, for a row that
// names none, per bench.json directory.
func renderStdBars(w *strings.Builder, res *results, tb string) {
	p := func(format string, a ...any) { fmt.Fprintf(w, format, a...) }
	p("### The std bars\n\n")
	p("Each Mk cell against the other languages' standard libraries, by the ratio of medians. ")
	p("The %.2f bar is against the best checked peer: the fastest cell among C, Rust, Zig, Go and C# whose own build checks bounds and traps overflow (`rust-ovf`, `zig-safe`), whatever the row's overflow class, because Mk keeps its checks. Over %.2f there needs a written reason and a plan. ", stdBar, stdBar)
	p("The %.1f rule is against the best of C, Rust, Zig, Go and C# in any class (C and `zig-fast` are unchecked): over %.1f there is a std defect, unless the row is a stated exception (crypto-random for `crypto.Random`, sort-stable for `SortStable`), which its line names. ", stdDefect, stdDefect)
	p("Each ratio is also classified against the other threshold, which is reported and carries no bar. ")
	p("A verdict cites the 95%% interval: within when the whole interval is below the threshold, over when it is above, otherwise inconclusive. It counts only for a run whose facts above show a quiet machine. A cell with no time to divide by (a kernel time at or below 0 in any run) gives no verdict: an Mk cell is not judged, and a reference with none is still the fastest cell and is named \"no kernel time\", so no slower cell stands in for it.\n\n")
	p("| Row | Mk column | Best checked peer | Mk / peer [95%%] | %.2f (bar) | %.1f | Best of C, Rust, Zig, Go and C# | Mk / best [95%%] | %.2f | %.1f |\n|---|---|---|---|---|---|---|---|---|---|\n", stdBar, stdDefect, stdBar, stdDefect)
	var order []string
	tally := map[string]*stdTally{}
	for _, r := range res.Rows {
		if r.Table != tb {
			continue
		}
		src := stdLine(r.Std)
		if src == "" {
			src = rowSource(r.ID)
		}
		t := tally[src]
		if t == nil {
			t = &stdTally{}
			tally[src] = t
			order = append(order, src)
		}
		cs, _ := kernelCells(rowCells(res, r.ID))
		// Each reference is the eligible cell with the smallest median,
		// timed or not: one with no kernel time gives no verdict rather
		// than handing the bar to a slower cell.
		ref := func(keep func(f column) bool) *cell {
			return bestOf(cs, func(c cell) bool {
				f, ok := colFacts(res, c.Col)
				return ok && !isMkCol(res, c.Col) && !f.Budget && keep(f)
			})
		}
		peer := ref(checkedBuild)
		best := ref(func(column) bool { return true })
		name := func(c *cell, none string) string {
			switch {
			case c == nil:
				return none
			case !timed(c):
				return c.Col + ": no kernel time"
			case c.Noisy:
				return c.Col + " (noisy)"
			}
			return c.Col
		}
		var mks []*cell
		for i := range cs {
			if isMkCol(res, cs[i].Col) && !budgetCol(res, cs[i].Col) {
				mks = append(mks, &cs[i])
			}
		}
		if len(mks) == 0 {
			p("| %s | mk: %s | %s | - | - | - | %s | - | - | - |\n", r.ID, skipText(r, "mk"), name(peer, "no checked cell"), name(best, "no cell"))
			continue
		}
		for _, mk := range mks {
			why := ""
			switch {
			case mk.Excluded != "":
				why = "excluded (" + cellText(mk.Excluded) + ")"
			case len(mk.Times) == 0:
				why = "no times"
			case !timed(mk):
				why = "no kernel time (a time at or below 0 after the baseline)"
			}
			if why != "" {
				if mk.Excluded == "" && len(mk.Times) > 0 {
					t.noKernel++
				}
				p("| %s | %s: %s | %s | - | - | - | %s | - | - | - |\n", r.ID, mk.Col, why, name(peer, "no checked cell"), name(best, "no cell"))
				continue
			}
			t.cells++
			if !slices.Contains(t.runs, r.Run) {
				t.runs = append(t.runs, r.Run)
			}
			ep, eb := stdRatio(mk, peer), stdRatio(mk, best)
			switch barClass(ep, stdBar) {
			case "within":
				t.within++
			case "inconclusive":
				t.inconclusive++
			case "over":
				t.over++
			default:
				if peer != nil {
					t.peerNoKernel++
				} else {
					t.noPeer++
				}
			}
			if best != nil && !timed(best) {
				t.bestNoKernel++
			}
			if barClass(ep, stdDefect) == "over" {
				t.peerDefect++
			}
			defect := barClass(eb, stdDefect)
			if defect == "over" {
				if r.Exception != "" {
					t.excepted++
					defect = "over (" + r.Exception + ", a stated exception)"
				} else {
					t.defect++
				}
			}
			p("| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", r.ID, name(mk, ""), name(peer, "no checked cell"), fmtRatio(ep), barClass(ep, stdBar), barClass(ep, stdDefect),
				name(best, "no cell"), fmtRatio(eb), barClass(eb, stdBar), defect)
		}
	}
	p("\nBy std function (bench.json `std`), or by source for a row that names none:\n\n")
	p("Mk cells timed = within + inconclusive + over %.2f + no checked peer + peer with no kernel time. An Mk cell with no kernel time is not timed.\n\n", stdBar)
	p("| Rows | Mk cells timed | Within %.2f | Inconclusive | Over %.2f | No checked peer | Peer with no kernel time | No kernel time | Over %.1f against the best | Of those, stated exceptions | Best with no kernel time | Over %.1f against the checked peer (reported) |\n|---|---|---|---|---|---|---|---|---|---|---|---|\n", stdBar, stdBar, stdDefect, stdDefect)
	for _, src := range order {
		t := tally[src]
		p("| %s | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d |\n", src, t.cells, t.within, t.inconclusive, t.over, t.noPeer, t.peerNoKernel, t.noKernel, t.defect+t.excepted, t.excepted, t.bestNoKernel, t.peerDefect)
	}
	// A line that counts cells of runs with different mk binaries,
	// std or toolchains counts more than one compiler.
	for _, src := range order {
		rs := tally[src].runs
		for _, i := range rs[min(1, len(rs)):] {
			if i < len(res.Runs) && rs[0] < len(res.Runs) {
				if d := toolchainDiff(res.Runs[rs[0]], res.Runs[i]); d != "" {
					p("\nThe line %s counts cells measured by runs with different mk binaries, std or toolchains (%s; see each run's facts).\n", src, d)
					break
				}
			}
		}
	}
	p("\n")
}
