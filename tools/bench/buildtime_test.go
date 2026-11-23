// Why: the build-time table must time the run-time column's own command from
// empty caches, and must not read as a compile-speed comparison.
// What: tests of buildtime.go, from TestBuildTimeFreshCommands to
// TestBuildTimeGoCell.
// How: compares fresh and run-time commands, renders a fixed result set, and
// builds one Go cell for real (skipped in -short).

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// TestBuildTimeFreshCommands: a build-time build is the run-time
// column's command with every cache under its own directory: the
// arguments are command's with the work directory moved (so no run-time
// stamp changes), Go gets a fresh GOCACHE, Zig's two caches and the Mk
// cache are under the directory, and the run-time command is unchanged.
func TestBuildTimeFreshCommands(t *testing.T) {
	dir := t.TempDir()
	rdir := filepath.Join(dir, "row")
	writeFile(t, filepath.Join(rdir, "mk", "pkg.mk"), "")
	r := &row{Name: "x", Table: "same", Dir: rdir, Sources: map[string]string{"mk": "mk", "c": "x.c", "rust": "x.rs", "zig": "x.zig", "go": "x.go", "cs": "x.cs"}}
	tc := &toolchain{Mk: "mk", Clang: "clang", Rustc: "rustc", Zig: "zig", Go: "go", Dotnet: "dotnet", Work: filepath.Join(dir, "work"), versions: map[string]string{}}
	fresh := filepath.Join(dir, "fresh")
	for _, c := range rowColumns(r) {
		before, err := tc.command(r, c, filepath.Join(dir, "exe"))
		if err != nil {
			t.Fatal(err)
		}
		b, err := freshBuild(tc, r, c, fresh)
		if err != nil {
			t.Fatal(err)
		}
		after, _ := tc.command(r, c, filepath.Join(dir, "exe"))
		if before.String() != after.String() || tc.Work != filepath.Join(dir, "work") {
			t.Errorf("%s: freshBuild changed the run-time command or the toolchain", c.ID)
		}
		text := b.Cmd.String()
		if strings.Contains(text, tc.Work) {
			t.Errorf("%s: the fresh build still uses the work directory's caches: %s", c.ID, text)
		}
		switch c.Lang {
		case "go":
			if !slices.Contains(b.Cmd.Env, "GOCACHE="+filepath.Join(fresh, "gocache")) {
				t.Errorf("go: no fresh GOCACHE: %v", b.Cmd.Env)
			}
		case "zig":
			for _, w := range []string{filepath.Join(fresh, "zig-cache"), filepath.Join(fresh, "zig-global")} {
				if !slices.Contains(b.Cmd.Args, w) {
					t.Errorf("zig: no cache %s: %s", w, text)
				}
			}
		case "mk":
			if !slices.Contains(b.Cmd.Env, "MK_CACHE="+filepath.Join(fresh, "mkcache")) || !slices.Contains(b.Cmd.Args, "--no-cache") {
				t.Errorf("mk: not a fresh, uncached build: %s", text)
			}
		}
		if len(b.Cmd.Args) != len(before.Args) || b.Cmd.Args[1] != before.Args[1] {
			t.Errorf("%s: the flags differ from the run-time column's:\n%s\n%s", c.ID, text, before.String())
		}
		if b.Exe != filepath.Join(fresh, "out", c.ID+".exe") {
			t.Errorf("%s: executable %s", c.ID, b.Exe)
		}
	}
	for _, c := range columns {
		if !c.Budget && buildTimeStd[c.ID] == "" {
			t.Errorf("column %s has no std line in the legend", c.ID)
		}
	}
}

// TestRenderBuildTime: the section states what it measures, gives the
// column legend, and per table each row's medians and an excluded cell's
// reason, with no ratio between cells.
func TestRenderBuildTime(t *testing.T) {
	cell := func(tb, rowID, col string, times ...float64) buildTimeCell {
		return buildTimeCell{Table: tb, Row: rowID, Col: col, Times: times, Sum: summarize(times)}
	}
	res := &buildTimeResults{Runs: 3, Cells: []buildTimeCell{
		cell("same", "nbody", "mk", 4, 4, 4), cell("same", "nbody", "c", 0.5, 0.5, 0.5), cell("same", "nbody", "go", 1, 1, 1),
		cell("app", "app/grep", "mk", 6, 6, 6), {Table: "app", Row: "app/grep", Col: "go", Excluded: "the program built is wrong"},
	}}
	md := renderBuildTime(res, "2026-11-16")
	for _, w := range []string{"## Table \"buildtime\"", "`tests/bench/results/2026-11-16/buildtime/results.json`", "not compiler speed",
		"| Column | LTO | Linker | What the build compiles of std |", "| go | n/a (gc whole program) | go | the std packages the program imports rebuilt (a fresh GOCACHE) |",
		"**same rows.**", "| Row | mk | c | go |", "| nbody | 4.000 s | 500.0 ms | 1.000 s |",
		"**app rows.**", "| app/grep | 6.000 s |  | excluded: the program built is wrong |", "no ratio between cells"} {
		if !strings.Contains(md, w) {
			t.Errorf("render lacks %q:\n%s", w, md)
		}
	}
	for _, bad := range []string{"(8.00)", "(1.00)", "(2.00)", "The ratio is to the fastest cell"} {
		if strings.Contains(md, bad) {
			t.Errorf("render has a cross-column ratio %q:\n%s", bad, md)
		}
	}
	if strings.Contains(md, "**idiomatic rows.**") {
		t.Error("a table with no cell is rendered")
	}
}

// TestRenderBuildTimeKeyedByTable: a row id
// that two tables share renders each table's own cell, since the render
// looks a cell up by table, row and column.
func TestRenderBuildTimeKeyedByTable(t *testing.T) {
	cell := func(tb, col string, times ...float64) buildTimeCell {
		return buildTimeCell{Table: tb, Row: "lane/x", Col: col, Times: times, Sum: summarize(times)}
	}
	res := &buildTimeResults{Runs: 3, Cells: []buildTimeCell{
		cell("same", "mk", 4, 4, 4), cell("same", "c", 1, 1, 1),
		cell("idiomatic", "mk", 2, 2, 2), cell("idiomatic", "c", 3, 3, 3),
	}}
	md := renderBuildTime(res, "2026-11-16")
	_, same, ok1 := strings.Cut(md, "**same rows.**")
	same, idiom, ok2 := strings.Cut(same, "**idiomatic rows.**")
	if !ok1 || !ok2 {
		t.Fatalf("no same and idiomatic sections:\n%s", md)
	}
	if !strings.Contains(same, "| lane/x | 4.000 s | 1.000 s |") {
		t.Errorf("the same section does not show its own cells:\n%s", same)
	}
	if !strings.Contains(idiom, "| lane/x | 2.000 s | 3.000 s |") {
		t.Errorf("the idiomatic section does not show its own cells:\n%s", idiom)
	}
}

// TestBuildTimeMkFailureFailsRun: a cell
// of the mk measured that fails fails the run, as in compile, and a
// publish writes buildtime/results.json, the failed cell excluded with
// its reason, before it returns that error. The mk measured is a path
// that does not exist, so nothing is built.
func TestBuildTimeMkFailureFailsRun(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the build-time table runs on Windows only (buildTimeTable)")
	}
	n := runtime.NumCPU()
	if n >= 64 {
		t.Skip("the publish sets the process affinity to every CPU, which one mask word cannot name here")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module mk\n\ngo 1.27\n")
	writeFile(t, filepath.Join(root, "tests", "bench", "same", "q", "mk", "pkg.mk"), "")
	writeFile(t, filepath.Join(root, "tests", "bench", "same", "q", "bench.json"),
		`{"table": "same", "overflow": "never", "args": ["1"], "quick": ["1"], "quick_checksum": "42", "sources": {"mk": "mk"}}`)
	mk := filepath.Join(root, "no-such-mk.exe")
	var lines []string
	w := func(s string) { lines = append(lines, s) }
	o := buildTimeOptions{Root: root, Work: filepath.Join(root, "work"), Mk: mk, Rows: []string{"q"}, Runs: 1, Log: func(s string) { t.Log(s) }}
	err := buildTimeTable(o, w)
	if err == nil || !strings.Contains(err.Error(), "buildtime: 1 cells of the mk measured failed") {
		t.Fatalf("a failed mk cell: %v (%q)", err, lines)
	}
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "FAIL buildtime q mk: ") {
		t.Errorf("lines %q", lines)
	}
	o.Publish, o.LockPath, o.Mask, o.Date = true, filepath.Join(t.TempDir(), "bench.lock"), uint64(1)<<n-1, "2026-11-16"
	if err := buildTimeTable(o, w); err == nil || !strings.Contains(err.Error(), "cells of the mk measured failed") {
		t.Fatalf("a published run with a failed mk cell: %v", err)
	}
	res, err := loadBuildTimeIfAny(root, o.Date)
	if err != nil || res == nil {
		t.Fatalf("no buildtime/results.json before the error: %v", err)
	}
	if len(res.Cells) != 1 || res.Cells[0].Col != "mk" || res.Cells[0].Excluded == "" || res.Cells[0].Sum.N != 0 {
		t.Errorf("the published cells: %+v", res.Cells)
	}
}

// TestBuildTimeGoCell measures one Go cell of a small row for real: each
// build in a fresh directory, the program checked at the quick size, and
// a wrong program excluded with the reason. The times are not looked at.
func TestBuildTimeGoCell(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a Go program twice from an empty GOCACHE")
	}
	if runtime.GOOS != "windows" {
		t.Skip("the go column's command line builds for windows/amd64, so its program runs only on Windows")
	}
	dir := t.TempDir()
	tc, _ := findToolchain(dir, "")
	if tc.Go == "" {
		t.Skip("no go")
	}
	rdir := filepath.Join(dir, "row")
	writeFile(t, filepath.Join(rdir, "testdata", "x.go"), "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(42) }\n")
	r := &row{Name: "x", Table: "same", Dir: rdir, Sources: map[string]string{"go": "testdata/x.go"}, QuickSum: "42"}
	c, _ := columnByID("go")
	cell := measureBuildTime(tc, r, c, buildTimeOptions{Work: filepath.Join(dir, "work"), Runs: 2})
	if cell.Excluded != "" || cell.Sum.N != 2 {
		t.Fatalf("excluded %q, %d times", cell.Excluded, cell.Sum.N)
	}
	if !slices.Contains(cell.Build.Env, "GOCACHE="+filepath.Join(dir, "work", "buildtime", "x", "go", "1", "gocache")) {
		t.Errorf("the first build had no GOCACHE of its own: %v", cell.Build.Env)
	}
	for _, k := range []string{"1", "2"} {
		if _, err := os.Stat(filepath.Join(dir, "work", "buildtime", "x", "go", k)); err == nil {
			t.Errorf("build %s's caches were kept", k)
		}
	}
	r.QuickSum = ""
	if cell = measureBuildTime(tc, r, c, buildTimeOptions{Work: filepath.Join(dir, "work"), Runs: 1}); !strings.Contains(cell.Excluded, "no quick checksum") {
		t.Errorf("a row with no quick checksum was timed: %+v", cell)
	}
	r.QuickSum = "43"
	cell = measureBuildTime(tc, r, c, buildTimeOptions{Work: filepath.Join(dir, "work"), Runs: 1})
	if !strings.Contains(cell.Excluded, "the program built is wrong") || cell.Sum.N != 0 {
		t.Errorf("a wrong program was timed: %+v", cell)
	}
}
