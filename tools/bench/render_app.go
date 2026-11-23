// Why: app rows compare whole programs built from std, ecosystem libraries
// or hand-written code, so their table must say which, and must flag where
// Mk's std falls far behind (README "Applications").
//
// What: the app table's sections of RESULTS.md: appClass, the grid helpers,
// peakText and linuxPeakText, and the per-row section with its defect line.
//
// How: each cell's kind beside its class, the ratio to the best cell of its
// kind, the program's peak memory as a fact, the libraries and hand-written
// parts named, and the defect line: an Mk cell over 2 times the best std
// cell of C, Rust, Zig or Go is a std defect.

package main

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
)

// appClass is an app cell's class. An eco cell runs mostly its libraries'
// code, which its column's flags do not make checked (ring is C and
// assembly inside, regex uses unsafe Rust), so it has no class and never
// enters a class ratio; a std or hand cell has its column's class.
func appClass(r *row, c column) string {
	if r.Kinds[c.Lang] == "eco" {
		return "eco (not classed)"
	}
	return c.class(r)
}

// gridHead, gridSep and gridLead are the first columns of a table's summary
// grid (renderTables): the row and its overflow class, or on the app table
// the row alone, since an app cell's class depends on its kind.
func gridHead(tb string) string {
	if tb == "app" {
		return "| Row |"
	}
	return "| Row | Overflow class |"
}

func gridSep(tb string) string {
	if tb == "app" {
		return "|---|"
	}
	return "|---|---|"
}

func gridLead(tb string, r rowInfo) string {
	if tb == "app" {
		return "| " + r.ID + " |"
	}
	return "| " + r.ID + " | " + orDash(r.Overflow) + " |"
}

// gridKind is what an app cell of the summary grid adds after its ratio to
// the row's best: its kind, so no eco cell is read as ranked against a std
// cell without it ("", off the app table).
func gridKind(tb string, r rowInfo, col string) string {
	if tb != "app" {
		return ""
	}
	return ", " + orDash(r.Kinds[langOf(col)])
}

// fmtMiB is a byte count in MiB.
func fmtMiB(n uint64) string {
	return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
}

// peakText is a cell's peak memory: the peak working set (Windows) or the
// maximum resident set (Linux), and the peak commit where there is one. A
// Linux reading at or under its spawner's floor bounds the program's peak
// and prints as "at most" the reading, rounded up; "-" is a cell
// with no reading.
func peakText(c *cell) string {
	if c.Peak == nil {
		return "-"
	}
	if c.Peak.RSS == 0 {
		if c.Peak.Maxrss > 0 {
			return fmt.Sprintf("at most %.1f MiB", math.Ceil(float64(c.Peak.Maxrss)/(1<<20)*10)/10)
		}
		return "-"
	}
	s := fmtMiB(c.Peak.RSS)
	if c.Peak.Commit > 0 {
		s += " (commit " + fmtMiB(c.Peak.Commit) + ")"
	}
	return s
}

// linuxPeakText is the row's sentence on how its Linux peaks were read,
// or "" when no cell has a spawner's reading.
func linuxPeakText(cs []cell) string {
	var names []string
	for _, c := range cs {
		if c.Peak != nil && c.Peak.Floor > 0 && !slices.Contains(names, c.Peak.Spawner) {
			names = append(names, c.Peak.Spawner)
		}
	}
	if len(names) == 0 {
		return ""
	}
	slices.Sort(names)
	for i, n := range names {
		switch n {
		case minSpawnerName:
			names[i] = "`peakspawn`, a minimal Go program"
		case "tools/bench":
			names[i] = "tools/bench itself, the fallback when no `peakspawn` is beside it"
		default:
			names[i] = "`" + n + "`"
		}
	}
	return "Peak memory on Linux is the program's `ru_maxrss`, the kernel's figure for its process and the children it waited for, read by a spawner that starts it in place of tools/bench (" + strings.Join(names, "; ") + "); the spawner's own peak, its floor, folds into the reading and is recorded beside it in results.json, so a reading at or under the floor is printed as \"at most\" the reading."
}

// outputCheckText says how a row's output file is checked.
func outputCheckText(o *outputSpec) string {
	switch o.Check {
	case "digest":
		return "its SHA-256 joins the checksum"
	case "zlib", "gzip":
		return fmt.Sprintf("Go's compress/%s decodes it, the result must be the input byte for byte, and its SHA-256 joins the checksum (the output bytes differ by library, so only the round trip is compared; their size is printed)", o.Check)
	case "then":
		return "its bytes differ by library and are never compared; the program's then run, named next, reads it back, and its output is the check, which verify also requires of every other column's then run on this column's file (its size is printed)"
	}
	return o.Check
}

// renderAppRow is one app row's section.
func renderAppRow(w *strings.Builder, res *results, r rowInfo) {
	p := func(format string, a ...any) { fmt.Fprintf(w, format, a...) }
	cs := rowCells(res, r.ID)
	best := bestOf(cs, func(cell) bool { return true })
	kind := func(col string) string { return r.Kinds[langOf(col)] }
	p("### %s\n\n%s Arguments `%s`, checksum `%s`.", r.ID, r.Algorithm, strings.Join(r.Args, " "), r.Checksum)
	if pk := r.Package; pk != nil {
		bin := ""
		if pk.Bin != "" {
			bin = ", bin `" + pk.Bin + "`"
		}
		state := ""
		if pk.Dirty {
			state = ", with uncommitted changes in it"
		}
		if pk.Unknown != "" {
			p(" The Mk side is the package `%s`%s; its commit and state are unknown (%s).", pk.Path, bin, pk.Unknown)
		} else {
			p(" The Mk side is the package `%s`%s, used unchanged, last changed by commit %s%s.", pk.Path, bin, orDash(short(pk.Commit)), state)
		}
	}
	if len(r.Env) > 0 {
		var kv []string
		for _, k := range slices.Sorted(maps.Keys(r.Env)) {
			kv = append(kv, "`"+k+"="+r.Env[k]+"`")
		}
		p(" Every program of the row runs with %s.", strings.Join(kv, ", "))
	}
	if r.Digest {
		if r.Package != nil {
			p(" The checksum is the SHA-256 of standard output, CR LF read as LF and the data and output paths and the package directory written as `$DATA`, `$OUT` and `$PKG`.")
		} else {
			p(" The checksum is the SHA-256 of standard output, CR LF read as LF and the data and output paths written as `$DATA` and `$OUT`.")
		}
	}
	if o := r.Output; o != nil {
		p(" Each program writes `%s`, deleted before every run; %s.", o.File, outputCheckText(o))
		if len(o.Then) > 0 {
			p(" After each checked run, `%s` runs untimed, and its output is the checksum's.", strings.Join(o.Then, " "))
		}
	}
	if best != nil && best.Summary.Median < 1 {
		p(" The fastest cell ran under 1 s, below the contract's floor; the row's size is to be raised.")
	}
	if cc := r.Concurrency; cc != nil {
		p(" Concurrency row: every program runs on the CPU set `%s` with %d threads (GOMAXPROCS and MK_CARRIERS); each cell names its model.", cc.Mask, cc.Threads)
	}
	if s := linuxPeakText(cs); s != "" {
		p(" %s", s)
	}
	extra, extraSep := "", ""
	if r.Output != nil {
		extra, extraSep = " Output bytes |", "---|"
	}
	if r.Concurrency != nil {
		extra, extraSep = extra+" Model |", extraSep+"---|"
	}
	if len(r.Labels) > 0 {
		extra, extraSep = extra+" Label |", extraSep+"---|"
	}
	p("\n\n| Column | Kind | Class | Median | Min | MAD | RSD | Runs | Ratio to best [95%%] | Ratio to best of its kind [95%%] | Peak memory |%s\n|---|---|---|---|---|---|---|---|---|---|---|%s\n", extra, extraSep)
	for i := range cs {
		c := &cs[i]
		more := ""
		if r.Output != nil {
			ob := "-"
			if c.OutBytes > 0 {
				ob = fmt.Sprint(c.OutBytes)
			}
			more += " " + ob + " |"
		}
		if r.Concurrency != nil {
			more += " " + orDash(cellText(r.Concurrency.Models[langOf(c.Col)])) + " |"
		}
		if len(r.Labels) > 0 {
			more += " " + orDash(cellText(r.Labels[langOf(c.Col)])) + " |"
		}
		if c.Excluded != "" {
			p("| %s | %s | %s | excluded: %s | | | | | | | |%s\n", c.Col, orDash(kind(c.Col)), c.Class, cellText(c.Excluded), more)
			continue
		}
		k := kind(c.Col)
		kb := bestOf(cs, func(x cell) bool { return kind(x.Col) == k })
		p("| %s | %s | %s | %s | %s | %s | %.1f%% | %d | %s | %s | %s |%s\n", c.Col, orDash(k), c.Class, fmtSec(c.Summary.Median), fmtSec(c.Summary.Min),
			fmtSec(c.Summary.MAD), c.Summary.RSD*100, c.Summary.N, fmtRatio(ratioTo(c, best)), fmtRatio(ratioTo(c, kb)), peakText(c), more)
	}
	for _, lang := range slices.Sorted(maps.Keys(r.Libs)) {
		var ls []string
		for _, l := range r.Libs[lang] {
			ls = append(ls, l.String())
		}
		p("\n- `%s` is `eco`: %s.", lang, strings.Join(ls, ", "))
	}
	for _, lang := range slices.Sorted(maps.Keys(r.Hand)) {
		p("\n- `%s` is `hand`: %s.", lang, r.Hand[lang])
	}
	// The defect line, against the best std cell of the other
	// languages.
	ref := bestOf(cs, func(x cell) bool { return kind(x.Col) == "std" && !isMkLang(langOf(x.Col)) })
	for i := range cs {
		c := &cs[i]
		if !isMkLang(langOf(c.Col)) || c.Excluded != "" || len(c.Times) == 0 {
			continue
		}
		if ref == nil {
			p("\n- Defect line: `%s` has no std cell of C, Rust, Zig, Go or C# to be judged against.", c.Col)
			continue
		}
		e := ratioTo(c, ref)
		verdict := classify(e.CI, 2.0)
		text := map[string]string{
			"over":         "**over 2.0: a std defect for the std function the row's profile names**",
			"within":       "within 2.0",
			"inconclusive": "inconclusive at 2.0",
		}[verdict]
		p("\n- Defect line: `%s` against the best std cell, `%s`: %s, %s.", c.Col, ref.Col, fmtRatio(e), text)
	}
	for _, n := range r.Notes {
		p("\n- %s", n)
	}
	p("\n\n")
}
