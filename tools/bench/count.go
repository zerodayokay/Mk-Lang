// Why: a matching checksum shows that the outputs agree, not that the
// algorithms do, so a row with data-dependent control flow (a sort, a tree,
// a scanner, an escape loop) has a counting build in every language.
//
// What: countCopy, countFlags, buildCounting, checkCounts, majority,
// parseCounts and countExclusions. The counting build prints, after the
// checksum line, one line
//
//	counts <name>=<value> ...
//
// with the row's counters in bench.json order, and `verify` and `publish`
// require every language to print the same line at the quick size before a
// row is timed.
//
// How: each language is switched on (tests/bench/README.md, "Counting
// builds"): C by -DBENCH_COUNT=1, Rust by --cfg bench_count, and Mk, Zig and
// Go by a copy of the source in the work directory with its one switch line
// flipped from false to true. Zig and Go cannot take a flag here: the rows
// are single files built by `zig build-exe` (no build.zig, so no -D option)
// and `go build file.go` (a build tag cannot select a constant inside the
// one file). The timed builds compile the sources as they are, with the
// switch false, so the counting code is dead and folded away.

package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// countSwitch is the one line a copied source must hold, per language; the
// counting copy replaces "false" by "true" in it.
var countSwitch = map[string]string{
	"mk":  "const BenchCount: bool = false",
	"zig": "const bench_count = false;",
	"go":  "const benchCount = false",
	"cs":  "const bool BenchCount = false;",
}

// countColumns is the column each language's counting build is made from:
// one per language (the counts cannot depend on the flags of a language's
// other columns). Zig counts in ReleaseSafe, so a counter overflow traps.
var countColumns = []string{"mk", "c", "rust", "zig-safe", "go", "cs"}

// countCopy copies the source of language lang of row r into dir, keeping
// its path relative to the row, with the switch line flipped, and returns a
// row that reads it. A source with no switch line, or more than one, is an
// error.
func countCopy(r *row, lang, dir string) (*row, error) {
	sw := countSwitch[lang]
	src := filepath.Join(r.Dir, r.Sources[lang])
	flipped := 0
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(r.Dir, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		s := string(b)
		if n := strings.Count(s, sw); n > 0 {
			flipped += n
			s = strings.ReplaceAll(s, sw, strings.Replace(sw, "false", "true", 1))
		}
		out := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		return writeIfChanged(out, s)
	})
	if err != nil {
		return nil, err
	}
	if flipped != 1 {
		return nil, fmt.Errorf("the %s source of %s holds the counting switch %q %d times, not once", lang, r.id(), sw, flipped)
	}
	c := *r
	c.Dir = dir
	return &c, nil
}

// writeIfChanged keeps an unchanged copy's modification time, so its stamp
// stays valid.
func writeIfChanged(path, s string) error {
	if old, err := os.ReadFile(path); err == nil && string(old) == s {
		return nil
	}
	return os.WriteFile(path, []byte(s), 0o644)
}

// countFlags are the flags a counting build adds to the column's command.
func countFlags(lang string) []string {
	switch lang {
	case "c":
		return []string{"-DBENCH_COUNT=1"}
	case "rust":
		return []string{"--cfg", "bench_count"}
	}
	// Mk needs no flag beyond the copy: every Mk build of tools/bench passes
	// --no-cache (columns.go), because mk serves a stale release build from
	// its cache after a const's initializer changes.
	return nil
}

// buildCounting builds the counting variant of every language of rows with
// counters, into the work directory under bin/count.
func buildCounting(t *toolchain, rows []*row, log func(string)) []*built {
	var out []*built
	for _, r := range rows {
		if len(r.Counts) == 0 {
			continue
		}
		for _, id := range countColumns {
			c, _ := columnByID(id)
			if _, ok := r.Sources[c.Lang]; !ok {
				continue
			}
			b := &built{Row: r, Col: c, Exe: exeName(t.Work, "count", r, c)}
			src := r
			if _, ok := countSwitch[c.Lang]; ok {
				dir := filepath.Join(t.Work, "count-src", strings.ReplaceAll(r.id(), "/", "_"), c.Lang)
				src, b.Err = countCopy(r, c.Lang, dir)
			}
			if b.Err == nil {
				b.Cmd, b.Err = t.command(src, c, b.Exe)
			}
			if b.Err == nil {
				if f := countFlags(c.Lang); f != nil {
					// The flags go before -o, after the column's own.
					args := b.Cmd.Args
					i := slices.Index(args, "-o")
					b.Cmd.Args = append(append(append([]string(nil), args[:i]...), f...), args[i:]...)
				}
				// The copy is what the stamp hashes.
				cb := *b
				cb.Row = src
				b.Output, b.Err = buildOne(&cb, log)
			}
			if b.Err != nil {
				log(fmt.Sprintf("counting build %s %s: %v", r.id(), c.ID, b.Err))
			}
			out = append(out, b)
		}
	}
	return out
}

// countResult is one language's counting run.
type countResult struct {
	B      *built
	Counts string // the counts line, or "" on an error
	Err    error
	Wrong  bool
}

// checkCounts runs every counting build of row r at the quick size and
// checks that each prints the quick checksum, then a counts line with the
// row's counters in order, and that every language's line is the same. It
// returns the results, the agreed line and whether the row passed.
func checkCounts(r *row, bs []*built) ([]countResult, string, bool) {
	var res []countResult
	votes := map[string]int{}
	for _, b := range bs {
		if b.Row != r {
			continue
		}
		cr := countResult{B: b, Err: b.Err}
		if cr.Err == nil {
			var out string
			out, _, cr.Err = runOnceEnv(b.Exe, r.runQuick, cellEnv(r, b.Col))
			if cr.Err == nil {
				cr.Counts, cr.Err = parseCounts(out, r)
			}
			if cr.Err == nil {
				votes[cr.Counts]++
			}
		}
		res = append(res, cr)
	}
	agreed, _ := majority(votes, len(res))
	ok := len(res) > 0
	for i := range res {
		if res[i].Err != nil {
			ok = false
		} else if res[i].Counts != agreed {
			res[i].Wrong, ok = true, false
		}
	}
	return res, agreed, ok
}

// majority is the line that more than half of n results printed (votes
// counts each line). With no such line (five languages that all differ, a
// 2-2-1 split, or too many failed builds) nothing is agreed: no group is
// right by plurality or by sort order, so the row is not timed.
func majority(votes map[string]int, n int) (string, bool) {
	for s, k := range votes {
		if 2*k > n {
			return s, true
		}
	}
	return "", false
}

// parseCounts checks a counting build's output: the quick checksum line,
// then exactly one counts line naming r.Counts in order, each with an
// unsigned decimal value.
func parseCounts(out string, r *row) (string, error) {
	lines := strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n")
	if len(lines) != 2 {
		return "", fmt.Errorf("counting build printed %d lines, want the checksum and one counts line", len(lines))
	}
	if r.QuickSum != "" && lines[0] != r.QuickSum {
		return "", fmt.Errorf("counting build printed checksum %s, want %s", lines[0], r.QuickSum)
	}
	f := strings.Fields(lines[1])
	if len(f) != len(r.Counts)+1 || f[0] != "counts" {
		return "", fmt.Errorf("counts line %q does not name %v", lines[1], r.Counts)
	}
	for i, name := range r.Counts {
		k, v, ok := strings.Cut(f[i+1], "=")
		if !ok || k != name || v == "" || strings.Trim(v, "0123456789") != "" {
			return "", fmt.Errorf("counts line %q: field %d is not %s=<n>", lines[1], i+1, name)
		}
	}
	return strings.Join(f, " "), nil
}

// countExclusions maps each language whose counts disagree (or whose
// counting build failed) to the reason, for publish to exclude that
// language's cells.
//
// With no agreed line (see majority) every language is excluded, and so is
// every other column of the row, under the key "*".
func countExclusions(res []countResult, agreed string) map[string]string {
	out := map[string]string{}
	if agreed == "" && len(res) > 0 {
		var lines []string
		for _, cr := range res {
			l := cr.Counts
			if cr.Err != nil {
				l = "error"
			}
			lines = append(lines, cr.B.Col.ID+": "+l)
		}
		out["*"] = "no counts line is printed by a majority of the languages: " + strings.Join(lines, "; ")
	}
	for _, cr := range res {
		switch {
		case cr.Err != nil:
			out[cr.B.Col.Lang] = "counting build: " + firstLines(cr.Err.Error(), 2)
		case cr.Wrong:
			out[cr.B.Col.Lang] = fmt.Sprintf("operation counts %q differ from %q", cr.Counts, agreed)
		}
	}
	return out
}
