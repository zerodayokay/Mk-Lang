// Why: the std bars decide where Mk's std is called slow or defective, so
// their references, verdicts and counts must be exactly right.
// What: tests of renderStdBars, the row fields std and exception, the std
// names against std/, and checkedBuild.
// How: renders hand-made results with chosen medians and kernel times and
// checks the lines and counts printed.

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestRenderStdBars: every Mk cell of an
// idiomatic row is judged against the best checked peer, found from the
// column's own build facts even in a "wraps" row, where the class of every
// column is "bounds"; the best of C, Rust, Zig and Go in any class is
// reported beside it; Mk's own columns are never a reference; a row with
// no checked peer says so; and the summary counts each source.
func TestRenderStdBars(t *testing.T) {
	times := func(m float64) []float64 { return []float64{m * 0.99, m, m * 1.01} }
	// class is what publish records for the cell (column.class of the row).
	cellOf := func(row, col, class string, m float64) cell {
		c := cell{Row: row, Table: "idiomatic", Col: col, Class: class, Times: times(m)}
		c.Summary = summarize(c.Times)
		return c
	}
	res := &results{Columns: columns, Sweeps: 3,
		Rows: []rowInfo{
			{ID: "maprow", Table: "idiomatic", Overflow: "wraps", Skip: map[string]string{"c": "no std hash map in C"}},
			{ID: "fast", Table: "idiomatic", Overflow: "never"},
			{ID: "edge", Table: "idiomatic", Overflow: "never"},
			{ID: "std-x/tasks", Table: "idiomatic", Overflow: "wraps"},
			{ID: "std-x/gone", Table: "idiomatic", Overflow: "never", Skip: map[string]string{"mk": "pending: std.gone has not landed"}},
			{ID: "kern", Table: "same", Overflow: "never"},
		},
		Cells: []cell{
			// A wraps row: rust-ovf and zig-safe are classed "bounds", as
			// publish classes them, and are still the checked peers.
			cellOf("maprow", "mk", "bounds", 1.10),
			cellOf("maprow", "mk-keyed", "bounds", 2.10),
			cellOf("maprow", "rust", "bounds", 0.90),
			cellOf("maprow", "rust-ovf", "bounds", 1.05),
			cellOf("maprow", "zig-fast", "unchecked", 0.50),
			cellOf("maprow", "zig-safe", "bounds", 1.00),
			cellOf("maprow", "go", "bounds", 0.80),
			// Mk fastest: its own cell is no reference, so the ratio is
			// below 1.
			cellOf("fast", "mk", "checked", 0.80),
			cellOf("fast", "rust-ovf", "checked", 1.00),
			cellOf("fast", "c", "unchecked", 0.90),
			// At the threshold: the interval straddles 1.25.
			cellOf("edge", "mk", "checked", 1.25),
			cellOf("edge", "zig-safe", "checked", 1.00),
			{Row: "edge", Table: "idiomatic", Col: "mk-ctx", Class: "checked", Excluded: "failed: checksum 1 != 2"},
			// Mk and Go only: no checked peer, the row best reported.
			cellOf("std-x/tasks", "mk", "bounds", 3.00),
			cellOf("std-x/tasks", "go", "bounds", 1.00),
			cellOf("std-x/gone", "go", "bounds", 1.00),
			cellOf("std-x/gone", "rust-ovf", "checked", 1.00),
			// Not an idiomatic row: no std bar.
			cellOf("kern", "mk", "checked", 9.00),
			cellOf("kern", "zig-safe", "checked", 1.00),
		}}
	var w strings.Builder
	renderTables(&w, res, facts{})
	md := w.String()
	_, bars, ok := strings.Cut(md, "### The std bars")
	if !ok {
		t.Fatalf("no std bars section:\n%s", md)
	}
	bars, _, _ = strings.Cut(bars, "## Table")
	for _, want := range []string{
		"| Row | Mk column | Best checked peer | Mk / peer [95%] | 1.25 (bar) | 2.0 | Best of C, Rust, Zig, Go and C# | Mk / best [95%] | 1.25 | 2.0 |",
		"| maprow | mk | zig-safe | 1.10 [",
		"] | within | within | zig-fast | 2.20 [",
		"] | over | over |",
		"| maprow | mk-keyed | zig-safe | 2.10 [",
		"| fast | mk | rust-ovf | 0.80 [",
		"| edge | mk | zig-safe | 1.25 [",
		"] | inconclusive | within | zig-safe | 1.25 [",
		"| edge | mk-ctx: excluded (failed: checksum 1 != 2) | zig-safe | - | - | - | zig-safe | - | - | - |",
		"| std-x/tasks | mk | no checked cell | - | - | - | go | 3.00 [",
		"| std-x/gone | mk: pending: std.gone has not landed | rust-ovf | - | - | - | go | - | - | - |",
		// The 2x rule is tallied against the best of any class:
		// maprow's mk (2.20 to zig-fast) and mk-keyed;
		// the checked peer's 2x is reported (mk-keyed only).
		"| tests/bench/idiomatic | 4 | 2 | 1 | 1 | 0 | 0 | 0 | 2 | 0 | 0 | 1 |",
		"| tests/bench-lanes/std-x | 1 | 0 | 0 | 0 | 1 | 0 | 0 | 1 | 0 | 0 | 0 |",
	} {
		if !strings.Contains(bars, want) {
			t.Errorf("std bars lack %q:\n%s", want, bars)
		}
	}
	// The mk-keyed line: over both thresholds against the checked peer.
	for _, l := range strings.Split(bars, "\n") {
		if strings.HasPrefix(l, "| maprow | mk-keyed |") && !strings.Contains(l, "] | over | over | zig-fast |") {
			t.Errorf("mk-keyed line: %s", l)
		}
		if strings.HasPrefix(l, "| fast | mk |") && !strings.Contains(l, "] | within | within | c | 0.89 [") {
			t.Errorf("fast line: %s", l)
		}
		// Mk's own columns are never the peer or the best.
		if strings.HasPrefix(l, "| maprow | mk |") && strings.Contains(l, "mk-keyed") {
			t.Errorf("an Mk column as a reference: %s", l)
		}
	}
	if strings.Contains(bars, "| kern |") {
		t.Errorf("a same-algorithm row in the std bars:\n%s", bars)
	}
	if n := strings.Count(md, "### The std bars"); n != 1 {
		t.Errorf("%d std bars sections", n)
	}
}

// TestStdBarsReviewFixes: the
// summary groups by bench.json's std; a stated exception's over verdict
// names its exception and is counted apart; a check-budget column is
// neither judged nor a reference; a kernel cell with no time to divide by
// gets no verdict and is no reference; and a summary line over runs with
// different mk binaries says so.
func TestStdBarsReviewFixes(t *testing.T) {
	times := func(m float64) []float64 { return []float64{m * 0.99, m, m * 1.01} }
	cellOf := func(row, col string, m float64) cell {
		c := cell{Row: row, Table: "idiomatic", Col: col, Times: times(m)}
		c.Summary = summarize(c.Times)
		return c
	}
	// kern: a baseline row whose mk kernel median is below 0, and whose
	// rust-ovf, the fastest cell, has a negative kernel too: it stays the
	// reference, named with no kernel time, and zig-safe does not stand in
	// for it.
	kern := func(col string, whole, base float64) cell {
		c := cellOf("kern", col, whole)
		c.Base = times(base)
		s := summarize(c.Base)
		c.BaseSummary = &s
		return c
	}
	res := &results{Columns: columns, Sweeps: 3,
		Runs: []facts{{MkSHA256: "aaaa"}, {MkSHA256: "bbbb"}},
		Rows: []rowInfo{
			{ID: "sortstable", Table: "idiomatic", Std: []string{"std.core.Slice.SortStable"}, Exception: "sort-stable", Run: 0},
			{ID: "sort", Table: "idiomatic", Std: []string{"std.core.Slice.SortStable"}, Run: 1},
			{ID: "budget", Table: "idiomatic"},
			{ID: "kern", Table: "idiomatic"},
			{ID: "fmt", Table: "idiomatic", Std: []string{"std.core.Print", "std.core.str.ParseInt"}, Run: 0},
		},
		Cells: []cell{
			cellOf("fmt", "mk", 1.1),
			cellOf("fmt", "zig-safe", 1.0),
			cellOf("sortstable", "mk", 2.6),
			cellOf("sortstable", "rust-ovf", 1.0),
			cellOf("sort", "mk", 1.1),
			cellOf("sort", "zig-safe", 1.0),
			cellOf("budget", "mk", 1.1),
			cellOf("budget", "mk-noovf", 1.1),
			cellOf("budget", "zig-safe", 1.0),
			kern("mk", 1.0, 1.2),
			kern("rust-ovf", 1.0, 1.5),
			kern("zig-safe", 1.0, 0.5),
		}}
	var w strings.Builder
	renderStdBars(&w, res, "idiomatic")
	bars := w.String()
	for _, want := range []string{
		"| sortstable | mk | rust-ovf | 2.60 [",
		"| over | over (sort-stable, a stated exception) |",
		"| `std.core.Slice.SortStable` | 2 | 1 | 0 | 1 | 0 | 0 | 0 | 1 | 1 | 0 | 1 |",
		"| kern | mk: no kernel time (a time at or below 0 after the baseline) | rust-ovf: no kernel time | - | - | - | rust-ovf: no kernel time | - | - | - |",
		"| tests/bench/idiomatic | 1 | 1 | 0 | 0 | 0 | 0 | 1 | 0 | 0 | 0 | 0 |",
		"| `std.core.Print`, `std.core.str.ParseInt` | 1 | 1 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |",
		"The line `std.core.Slice.SortStable` counts cells measured by runs with different mk binaries, std or toolchains (mk sha256 aaaa",
	} {
		if !strings.Contains(bars, want) {
			t.Errorf("std bars lack %q:\n%s", want, bars)
		}
	}
	if strings.Contains(bars, "| budget | mk-noovf") {
		t.Errorf("a check-budget column judged:\n%s", bars)
	}
	if strings.Contains(bars, "[-") {
		t.Errorf("a negative ratio printed:\n%s", bars)
	}
	if strings.Contains(bars, "The line tests/bench/idiomatic counts") {
		t.Errorf("a one-run line flagged as mixed:\n%s", bars)
	}
}

// TestStdBarsReferenceKernel: a reference is the fastest eligible cell even with no
// kernel time, which then gives no verdict and is counted, so a slower
// peer never stands in for it; the checked peer and the best of any class
// are judged apart; and a cell with any kernel time at or below 0 has no
// time, so no interval reaches 0 or infinity.
func TestStdBarsReferenceKernel(t *testing.T) {
	// kc is a baseline cell whose kernel times are times (a baseline of 0.1
	// subtracted from each run).
	kc := func(row, col string, times ...float64) cell {
		c := cell{Row: row, Table: "idiomatic", Col: col}
		for _, k := range times {
			c.Times = append(c.Times, k+0.1)
		}
		c.Summary = summarize(c.Times)
		c.Base = []float64{0.1, 0.1, 0.1}
		s := summarize(c.Base)
		c.BaseSummary = &s
		return c
	}
	res := &results{Columns: columns, Sweeps: 3,
		Rows: []rowInfo{
			{ID: "kf", Table: "idiomatic", Std: []string{"std.core.A"}},
			{ID: "k", Table: "idiomatic", Std: []string{"std.core.B"}},
			{ID: "kb", Table: "idiomatic", Std: []string{"std.core.C"}},
			{ID: "kp", Table: "idiomatic", Std: []string{"std.core.D"}},
			{ID: "kx", Table: "idiomatic", Std: []string{"std.core.E"}},
		},
		Cells: []cell{
			// zig-safe is the fastest cell and has no kernel time;
			// rust-ovf, slower, must not judge mk in its place.
			kc("kf", "mk", 0.49, 0.50, 0.51),
			kc("kf", "zig-safe", -0.011, -0.01, -0.009),
			kc("kf", "rust-ovf", 0.44, 0.45, 0.46),
			// The only checked cell has no kernel time: a peer
			// with none, not "no checked cell".
			kc("k", "mk", 0.49, 0.50, 0.51),
			kc("k", "zig-safe", -0.03, -0.02, -0.01),
			kc("k", "go", 0.39, 0.40, 0.41),
			// The best of any class has no kernel time, the checked
			// peer has: a 1.25 verdict, no 2x verdict.
			kc("kb", "mk", 0.49, 0.50, 0.51),
			kc("kb", "zig-fast", -0.02, -0.01, 0.01),
			kc("kb", "rust-ovf", 0.44, 0.45, 0.46),
			// On the peer: a positive median with one run
			// at or below 0 is no time; zig-fast, timed, is the best.
			kc("kp", "mk", 0.49, 0.50, 0.51),
			kc("kp", "zig-safe", -0.20, 0.40, 0.60),
			kc("kp", "zig-fast", 0.10, 0.11, 0.12),
			// On the Mk cell: no time, so no interval [-x, ...].
			kc("kx", "mk", -0.20, 0.40, 0.60),
			kc("kx", "rust-ovf", 0.39, 0.40, 0.41),
		}}
	var w strings.Builder
	renderStdBars(&w, res, "idiomatic")
	bars := w.String()
	for _, want := range []string{
		"| kf | mk | zig-safe: no kernel time | - | - | - | zig-safe: no kernel time | - | - | - |",
		"| `std.core.A` | 1 | 0 | 0 | 0 | 0 | 1 | 0 | 0 | 0 | 1 | 0 |",
		"| k | mk | zig-safe: no kernel time | - | - | - | zig-safe: no kernel time | - | - | - |",
		"| `std.core.B` | 1 | 0 | 0 | 0 | 0 | 1 | 0 | 0 | 0 | 1 | 0 |",
		"| kb | mk | rust-ovf | 1.11 [",
		"| zig-fast: no kernel time | - | - | - |",
		"| `std.core.C` | 1 | 1 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 1 | 0 |",
		"| kp | mk | zig-safe: no kernel time | - | - | - | zig-fast | 4.55 [",
		"| `std.core.D` | 1 | 0 | 0 | 0 | 0 | 1 | 0 | 1 | 0 | 0 | 0 |",
		"| kx | mk: no kernel time (a time at or below 0 after the baseline) | rust-ovf |",
		"| `std.core.E` | 0 | 0 | 0 | 0 | 0 | 0 | 1 | 0 | 0 | 0 | 0 |",
	} {
		if !strings.Contains(bars, want) {
			t.Errorf("std bars lack %q:\n%s", want, bars)
		}
	}
	for _, bad := range []string{"[-", "Inf", "NaN", "| kf | mk | rust-ovf", "no checked cell"} {
		if strings.Contains(bars, bad) {
			t.Errorf("std bars have %q:\n%s", bad, bars)
		}
	}
}

// TestStdBarsRowFields: std (a list) and exception are checked, and only
// the idiomatic table has them. An exception is one of the closed table's
// two, on a row whose std is exactly the function it excepts.
func TestStdBarsRowFields(t *testing.T) {
	root := fakeTree(t)
	dir := filepath.Join(root, "tests", "bench", "idiomatic", "x")
	writeFile(t, filepath.Join(dir, "x.c"), "// x\n")
	p := filepath.Join(dir, "bench.json")
	for body, ok := range map[string]bool{
		`{"table": "idiomatic", "sources": {"c": "x.c"}, "std": ["std.core.Slice.SortStable"], "exception": "sort-stable"}`:                 true,
		`{"table": "idiomatic", "sources": {"c": "x.c"}, "std": ["std.core.Print", "std.core.str.ParseInt"]}`:                               true,
		`{"table": "idiomatic", "sources": {"c": "x.c"}, "std": "std.core.Slice.Sort"}`:                                                     false,
		`{"table": "idiomatic", "sources": {"c": "x.c"}, "std": []}`:                                                                        false,
		`{"table": "idiomatic", "sources": {"c": "x.c"}, "std": ["sort"]}`:                                                                  false,
		`{"table": "idiomatic", "sources": {"c": "x.c"}, "std": ["std.core.Map."]}`:                                                         false,
		`{"table": "idiomatic", "sources": {"c": "x.c"}, "std": ["std.core.Map", "std.core.Map"]}`:                                          false,
		`{"table": "idiomatic", "sources": {"c": "x.c"}, "exception": "639"}`:                                                               false,
		`{"table": "idiomatic", "sources": {"c": "x.c"}, "std": ["std.crypto.Random"], "exception": "crypto-random"}`:                       true,
		`{"table": "idiomatic", "sources": {"c": "x.c"}, "std": ["std.core.Map"], "exception": "D-1"}`:                                      false,
		`{"table": "idiomatic", "sources": {"c": "x.c"}, "std": ["std.core.Map"], "exception": "D-0"}`:                                      false,
		`{"table": "idiomatic", "sources": {"c": "x.c"}, "std": ["std.core.Map"], "exception": "no-such-key"}`:                              false,
		`{"table": "idiomatic", "sources": {"c": "x.c"}, "std": ["std.core.Map"], "exception": "sort-stable"}`:                              false,
		`{"table": "idiomatic", "sources": {"c": "x.c"}, "std": ["std.core.Slice.SortStable"], "exception": "crypto-random"}`:               false,
		`{"table": "idiomatic", "sources": {"c": "x.c"}, "exception": "sort-stable"}`:                                                       false,
		`{"table": "idiomatic", "sources": {"c": "x.c"}, "std": ["std.core.Slice.SortStable", "std.core.Map"], "exception": "sort-stable"}`: false,
		`{"table": "idiomatic", "sources": {"c": "x.c"}, "std": ["std.core.Map.."]}`:                                                        false,
		`{"table": "idiomatic", "sources": {"c": "x.c"}, "std": ["std.Map"]}`:                                                               false,
		`{"table": "idiomatic", "sources": {"c": "x.c"}, "std": ["std.core.map"]}`:                                                          false,
		`{"table": "idiomatic", "sources": {"c": "x.c"}, "std": ["std.Core.Map"]}`:                                                          false,
		`{"table": "same", "sources": {"c": "x.c"}, "std": ["std.core.Slice.Sort"]}`:                                                        false,
	} {
		writeFile(t, p, body)
		if _, err := loadRow(p, ""); (err == nil) != ok {
			t.Errorf("%s: %v", body, err)
		}
	}
}

// TestStdNamesInStd: every
// tree idiomatic row names the std functions it prices, sortstable names
// its stated exception, and every std name of tests/bench's and tests/bench-lanes'
// rows is in std: its module is a directory under std/, and its last name
// is declared there by a non-test file's export or impl line. The pattern
// alone is not enough: std.sort.Sort matches it, though std has no module
// sort.
func TestStdNamesInStd(t *testing.T) {
	root := repoRootForTest(t)
	rows, err := discoverTree(t, root)
	if err != nil {
		t.Fatal(err)
	}
	decl := map[string]string{} // module -> its non-test files' export and impl lines
	declOf := func(mod string) (string, bool) {
		if d, ok := decl[mod]; ok {
			return d, true
		}
		files, _ := filepath.Glob(filepath.Join(root, "std", mod, "*.mk"))
		if len(files) == 0 {
			return "", false
		}
		var b strings.Builder
		for _, f := range files {
			if strings.HasPrefix(filepath.Base(f), "tests_") {
				continue
			}
			text, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, l := range strings.Split(string(text), "\n") {
				if strings.HasPrefix(l, "export ") || strings.HasPrefix(l, "impl ") {
					b.WriteString(l + "\n")
				}
			}
		}
		decl[mod] = b.String()
		return decl[mod], true
	}
	named := 0
	for _, r := range rows {
		if r.Lane == "" && r.Table == "idiomatic" {
			if len(r.Std) == 0 {
				t.Errorf("%s: a tree idiomatic row names no std function", r.id())
			}
			if want := map[string]string{"sortstable": "sort-stable"}[r.Name]; r.Exception != want {
				t.Errorf("%s: exception %q, want %q", r.id(), r.Exception, want)
			}
		}
		for _, n := range r.Std {
			named++
			parts := strings.Split(n, ".")
			d, ok := declOf(parts[1])
			if !ok {
				t.Errorf("%s: std %q: std has no module %s", r.id(), n, parts[1])
				continue
			}
			last := parts[len(parts)-1]
			if !regexp.MustCompile(`\b` + last + `\b`).MatchString(d) {
				t.Errorf("%s: std %q: std/%s declares no %s on an export or impl line", r.id(), n, parts[1], last)
			}
		}
	}
	if named == 0 {
		t.Fatal("no row names a std function")
	}
	// The closed table of stated exceptions names functions std has too.
	for d, n := range stdExceptions {
		parts := strings.Split(n, ".")
		decls, ok := declOf(parts[1])
		if !stdName.MatchString(n) || !ok || !regexp.MustCompile(`\b`+parts[len(parts)-1]+`\b`).MatchString(decls) {
			t.Errorf("stdExceptions[%s] = %q is not in std", d, n)
		}
	}
}

// TestCheckedBuild: the checked peers are the columns whose own build checks
// bounds and traps overflow, by the column table.
func TestCheckedBuild(t *testing.T) {
	var got []string
	for _, c := range columns {
		if checkedBuild(c) && !strings.HasPrefix(c.Lang, "mk") {
			got = append(got, c.ID)
		}
	}
	if strings.Join(got, ",") != "rust-ovf,zig-safe" {
		t.Fatalf("checked peers %v, want rust-ovf and zig-safe", got)
	}
	if rowSource("std-hash/maphash") != "tests/bench-lanes/std-hash" || rowSource("hashmap") != "tests/bench/idiomatic" {
		t.Fatal("rowSource")
	}
}
