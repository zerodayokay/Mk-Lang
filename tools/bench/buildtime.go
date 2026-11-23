// Why: a reader of the run-time tables also asks how long each language
// takes to build what was measured (tests/bench/README.md, "Build time of
// the benchmark programs"). That is not the compilers' speed in their
// default profiles, which is the compile table's question, so it is a table
// of its own: no class, no bar, and nothing in it feeds another table.
//
// What: `tools/bench buildtime`: buildTimeTable, freshBuild,
// measureBuildTime and renderBuildTime. Each column of a run-time row builds
// the executable that row times, with that column's own flags, from empty
// caches.
//
// How: the compile table's empty caches, applied to the run-time command
// lines without changing them (so no run-time build's stamp moves): every
// build gets its own fresh work directory, which holds the Mk cache (and the
// builds are `--no-cache` anyway), Zig's local and global caches and a
// crate's cargo target directory; Go gets a fresh GOCACHE; rustc on one file
// and C keep no cache. Each build is forced (no stamp), and the first
// build's program is checked at the quick size before its times count.
//
//	tools/bench buildtime [-table T] [-runs N] [rows]       provisional times
//	tools/bench buildtime -publish [-date D] [rows]         results/<date>/buildtime

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// buildTimeTables are the run-time tables whose rows the table covers.
var buildTimeTables = []string{"same", "idiomatic", "app"}

// buildTimeStd is what each column's build compiles of its language's
// standard library and runtime, printed in the table's legend.
var buildTimeStd = map[string]string{
	"mk":       "std checked and compiled with the program (`--no-cache`, a fresh MK_CACHE)",
	"mk-wrap":  "as mk",
	"mk-keyed": "as mk",
	"mk-ctx":   "as mk",
	"c":        "none: the static UCRT is prebuilt",
	"rust":     "std precompiled with the toolchain, linked into fat LTO; a crate's dependencies built from source in a fresh target directory",
	"rust-ovf": "as rust",
	"zig-fast": "std compiled with the program, and compiler_rt rebuilt (fresh local and global caches)",
	"zig-safe": "as zig-fast",
	"go":       "the std packages the program imports rebuilt (a fresh GOCACHE)",
	"cs":       "none: .NET 11 BCL is prebuilt (framework-dependent apphost + managed IL)",
	"cs-aot":   "whole-program NativeAOT compile and trim of the program and referenced BCL from offline NuGet packages",
}

// buildTimeCell is one row and column.
type buildTimeCell struct {
	Table    string    `json:"table"`
	Row      string    `json:"row"`
	Col      string    `json:"col"`
	Times    []float64 `json:"times"`
	Sum      summary   `json:"summary"`
	Build    buildCmd  `json:"build"` // the first build's command
	Excluded string    `json:"excluded,omitempty"`
}

type buildTimeResults struct {
	Run   facts           `json:"run"`
	Runs  int             `json:"runs"` // builds per cell
	Cells []buildTimeCell `json:"cells"`
}

type buildTimeOptions struct {
	Root, Work, Mk string
	Table          string
	Rows           []string
	Runs           int
	Publish        bool
	LockPath       string
	Mask           uint64
	Date           string
	Log            func(string)
}

// freshBuild is column c of row r's command with every cache in dir, a
// directory of its own: t's work directory replaced by dir, and Go's
// GOCACHE added. The arguments and flags are command's.
func freshBuild(t *toolchain, r *row, c column, dir string) (*built, error) {
	ft := *t
	ft.Work = dir
	b := &built{Row: r, Col: c, Exe: filepath.Join(dir, "out", c.ID+".exe")}
	cmd, err := ft.command(r, c, b.Exe)
	if err != nil {
		return nil, err
	}
	if c.Lang == "go" {
		cmd.Env = append(slices.Clone(cmd.Env), "GOCACHE="+filepath.Join(dir, "gocache"))
	}
	b.Cmd = cmd
	return b, nil
}

// timeBuild runs b's command once, with no stamp, and returns its wall
// time: from the compiler's start to its exit, plus the copy of a cargo
// product.
func timeBuild(b *built) (time.Duration, error) {
	if err := os.MkdirAll(filepath.Dir(b.Exe), 0o755); err != nil {
		return 0, err
	}
	if b.Cmd.Prep != nil {
		if err := b.Cmd.Prep(); err != nil {
			return 0, err
		}
	}
	cmd := exec.Command(b.Cmd.Args[0], b.Cmd.Args[1:]...)
	cmd.Dir = b.Cmd.Dir
	cmd.Env = append(buildEnv(os.Environ(), b.Cmd.Unset), b.Cmd.Env...)
	start := time.Now()
	out, err := cmd.CombinedOutput()
	if err == nil && b.Cmd.Product != "" {
		err = copyVendorFile(b.Cmd.Product, b.Exe)
		if err == nil && len(b.Cmd.Sidecars) > 0 {
			err = copySidecars(b.Cmd.Product, b.Exe, b.Cmd.Sidecars)
		}
	}
	d := time.Since(start)
	if err != nil {
		return 0, fmt.Errorf("%v: %s", err, firstLines(string(out), 20))
	}
	return d, nil
}

// measureBuildTime is one cell: Runs builds, each in a fresh directory,
// the first one's program checked at the quick size.
func measureBuildTime(t *toolchain, r *row, c column, o buildTimeOptions) buildTimeCell {
	cell := buildTimeCell{Table: r.Table, Row: r.id(), Col: c.ID}
	if r.QuickSum == "" {
		// verify would accept any output of a single column.
		cell.Excluded = "the row has no quick checksum to check the program against"
		return cell
	}
	base := filepath.Join(o.Work, "buildtime", strings.ReplaceAll(r.id(), "/", "_"), c.ID)
	for k := 1; k <= o.Runs; k++ {
		dir := filepath.Join(base, fmt.Sprint(k))
		if err := os.RemoveAll(dir); err != nil {
			cell.Excluded = err.Error()
			return cell
		}
		// Every cache of a build is its own (a GOCACHE, Zig's caches,
		// a cargo target), so each is removed once its time is taken
		// and its program checked: kept, they would fill the disk.
		defer os.RemoveAll(dir)
		b, err := freshBuild(t, r, c, dir)
		if err == nil {
			var d time.Duration
			d, err = timeBuild(b)
			cell.Times = append(cell.Times, d.Seconds())
		}
		if err != nil {
			cell.Times, cell.Excluded = nil, firstLines(err.Error(), 8)
			return cell
		}
		if k == 1 {
			cell.Build = b.Cmd
			res, agreed, ok := verify([]*built{b}, r.runQuick, r.QuickSum)
			if !ok {
				why := "the checksum differs from the row's quick checksum " + r.QuickSum
				if res[0].Err != nil {
					why = res[0].Err.Error()
				}
				cell.Times, cell.Excluded = nil, firstLines("the program built is wrong: "+why+" (printed "+agreed+")", 8)
				return cell
			}
		}
		if err := os.RemoveAll(dir); err != nil {
			cell.Times, cell.Excluded = nil, "removing the build's caches: "+err.Error()
			return cell
		}
	}
	cell.Sum = summarize(cell.Times)
	return cell
}

// buildTimeTable is `tools/bench buildtime`.
func buildTimeTable(o buildTimeOptions, w func(string)) error {
	if runtime.GOOS != "windows" {
		// Every command line builds for x86_64-windows.
		return errors.New("buildtime: the build-time table runs on Windows")
	}
	if o.Runs < 1 {
		return errors.New("buildtime: -runs must be at least 1")
	}
	all, err := discover(o.Root)
	if err != nil {
		return err
	}
	var rows []*row
	matched := map[string]bool{}
	for _, tb := range buildTimeTables {
		if o.Table != "" && o.Table != tb {
			continue
		}
		var names []string
		for _, n := range o.Rows {
			// The names this table has; selectRows refuses a broken one.
			if slices.ContainsFunc(all, func(r *row) bool { return r.Table == tb && (r.id() == n || r.Name == n) }) {
				names = append(names, n)
				matched[n] = true
			}
		}
		if len(o.Rows) > 0 && len(names) == 0 {
			continue
		}
		sel, err := selectRows(all, tb, names)
		if err != nil {
			return err
		}
		for _, r := range sel {
			// With no rows named, the tree's rows only: the lane
			// rows under tests/bench-lanes are many more cells
			// (tools/bench list), built three times each; name them
			// to include them.
			if len(o.Rows) > 0 || r.Lane == "" {
				rows = append(rows, r)
			}
		}
	}
	for _, n := range o.Rows {
		if !matched[n] {
			return fmt.Errorf("buildtime: no row %q in the tables %s", n, strings.Join(buildTimeTables, ", "))
		}
	}
	if o.Table != "" && !slices.Contains(buildTimeTables, o.Table) {
		return fmt.Errorf("buildtime: the table %q is not a run-time table of whole programs (%s)", o.Table, strings.Join(buildTimeTables, ", "))
	}
	if len(rows) == 0 {
		return errors.New("buildtime: no row selected")
	}
	if err := prepareRows(o.Root, rows); err != nil {
		return err
	}
	if o.Publish {
		lock, err := acquireLock(o.LockPath, o.Log)
		if err != nil {
			return err
		}
		defer lock.Release()
		o.Log("holding the machine lock " + lock.Path())
	}
	t, err := findToolchain(o.Work, o.Mk)
	if err != nil {
		return err
	}
	res := buildTimeResults{Runs: o.Runs}
	if o.Publish {
		res.Run = gatherFacts(o.Root, t)
		res.Run.Date = time.Now().Format("2006-01-02 15:04")
		res.Run.MkBinary, res.Run.MkSHA256, res.Run.MkStd = o.Mk, fileSHA(o.Mk), t.MkStd
		if t.Cargo != "" {
			res.Run.Versions["cargo"] = firstLine(output(t.Cargo, "--version"))
		}
		if err := setAffinity(o.Mask); err != nil {
			return err
		}
		res.Run.Affinity = fmt.Sprintf("%#x (one logical CPU on each of 8 cores, set on tools/bench and inherited by every compiler, each at its default parallelism)", o.Mask)
	}
	tag := "PROVISIONAL (no machine lock)"
	if o.Publish {
		tag = "published"
	}
	mkFailed := 0
	for _, r := range rows {
		for _, c := range rowColumns(r) {
			cell := measureBuildTime(t, r, c, o)
			if cell.Excluded != "" {
				if isMkLang(c.Lang) && r.QuickSum != "" {
					// The mk measured failing is a failed run, as in
					// compile and compile -real.
					mkFailed++
				}
				w(fmt.Sprintf("FAIL buildtime %s %s: %s", r.id(), c.ID, cell.Excluded))
			} else {
				w(fmt.Sprintf("ok buildtime %s %s: median %s (min %s), %s", r.id(), c.ID, fmtSec(cell.Sum.Median), fmtSec(cell.Sum.Min), tag))
			}
			res.Cells = append(res.Cells, cell)
		}
	}
	if o.Publish {
		dir := filepath.Join(o.Root, "tests", "bench", "results", o.Date, "buildtime")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		b, err := json.MarshalIndent(res, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "results.json"), append(b, '\n'), 0o644); err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(o.Root, "tests", "bench", "results", o.Date, "results.json")); err == nil {
			if err := writeResultsMD(o.Root, o.Date); err != nil {
				return err
			}
		} else {
			o.Log("results/" + o.Date + "/results.json is not there yet, so RESULTS.md is rendered by the main publish of that date")
		}
	}
	if mkFailed > 0 {
		return fmt.Errorf("buildtime: %d cells of the mk measured failed (see FAIL above)", mkFailed)
	}
	return nil
}

func loadBuildTimeIfAny(root, date string) (*buildTimeResults, error) {
	b, err := os.ReadFile(filepath.Join(root, "tests", "bench", "results", date, "buildtime", "results.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r buildTimeResults
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("buildtime results of %s: %w", date, err)
	}
	return &r, nil
}

// renderBuildTime is the build-time section of RESULTS.md: a legend of the
// columns, then one table per run-time table, a row per benchmark row and
// a column per language column, each cell's median. There is no ratio
// between cells: the columns' flags differ in kind, so a ratio across them
// would read as a compile-speed ratio, which this table is not.
func renderBuildTime(res *buildTimeResults, date string) string {
	var w strings.Builder
	fmt.Fprintf(&w, "## Table \"buildtime\"\n\nFrom `tests/bench/results/%s/buildtime/results.json`. The build time of each benchmark program: every column of the tree's `same`, `idiomatic` and `app` rows that have a quick checksum (lane rows only when named), built with the run-time table's own flags (README \"Columns\"), the executable those tables time, from empty caches: each build in a fresh directory for its caches, and Go with a fresh GOCACHE. %d builds per cell, forced (no stamp); the first build's program is checked at the row's quick size before its times count. Wall time from the compiler's start to its exit, one build at a time. The flags are not each language's default profile and differ in kind across columns (Rust: fat LTO and one codegen unit; C: one translation unit, no LTO; Mk: ThinLTO with std compiled with the program), so this measures what building the measured program costs, not compiler speed; the compile tables measure that. No class, no bar, no ratio between cells, and nothing here feeds another table.\n\n",
		date, res.Runs)
	busy := "none"
	if len(res.Run.Busy) > 0 {
		busy = strings.Join(res.Run.Busy, ", ")
	}
	fmt.Fprintf(&w, "Machine: %s; %s; power scheme %s; affinity %s; measured %s. mk `%s` (%s, sha256 `%s`). Other compilers and test binaries running at the start: %s.\n\n",
		orDash(res.Run.CPU), orDash(res.Run.OS), orDash(res.Run.Power), orDash(res.Run.Affinity), orDash(res.Run.Date),
		orDash(binName(res.Run.MkBinary)), orDash(res.Run.Versions["mk"]), short(res.Run.MkSHA256), busy)
	var cols []column
	for _, c := range columns {
		if !c.Budget && slices.ContainsFunc(res.Cells, func(x buildTimeCell) bool { return x.Col == c.ID }) {
			cols = append(cols, c)
		}
	}
	w.WriteString("| Column | LTO | Linker | What the build compiles of std |\n|---|---|---|---|\n")
	for _, c := range cols {
		fmt.Fprintf(&w, "| %s | %s | %s | %s |\n", c.ID, c.LTO, c.Linker, buildTimeStd[c.ID])
	}
	w.WriteString("\n")
	for _, tb := range buildTimeTables {
		var rowIDs []string
		for _, c := range res.Cells {
			if c.Table == tb && !slices.Contains(rowIDs, c.Row) {
				rowIDs = append(rowIDs, c.Row)
			}
		}
		if len(rowIDs) == 0 {
			continue
		}
		fmt.Fprintf(&w, "**%s rows.**\n\n| Row |", tb)
		for _, c := range cols {
			fmt.Fprintf(&w, " %s |", c.ID)
		}
		w.WriteString("\n|---|" + strings.Repeat("---|", len(cols)) + "\n")
		for _, id := range rowIDs {
			fmt.Fprintf(&w, "| %s |", id)
			for _, col := range cols {
				text := ""
				for _, c := range res.Cells {
					if c.Table != tb || c.Row != id || c.Col != col.ID {
						continue
					}
					switch {
					case c.Excluded != "":
						text = "excluded: " + cellText(c.Excluded)
					case c.Sum.N > 0:
						text = fmtSec(c.Sum.Median)
					}
				}
				fmt.Fprintf(&w, " %s |", text)
			}
			w.WriteString("\n")
		}
		w.WriteString("\n")
	}
	return w.String()
}
