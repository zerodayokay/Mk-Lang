// Why: a published table must be reproducible and honest about its
// machine, so one code path builds, checks, times and writes every table.
//
// What: `tools/bench publish`: publish, the results and cell types of
// results.json, mergeResults, the shared-machine checks (noisyShare,
// sharedMachine) and the RESULTS.md writer that carries older sections.
//
// How: under the machine lock, build every column, verify every checksum,
// pin to one CPU, time every cell with hyperfine in three sweeps of rotated
// column order, rerun noisy cells once, and write
// tests/bench/results/<date>/ and tests/bench/RESULTS.md.

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// cell is one (row, column) of a published table.
type cell struct {
	Row      string    `json:"row"`
	Table    string    `json:"table"`
	Col      string    `json:"column"`
	Class    string    `json:"class"`
	Checksum string    `json:"checksum,omitempty"`
	Times    []float64 `json:"times,omitempty"`
	Summary  summary   `json:"summary"`
	Noisy    bool      `json:"noisy,omitempty"`
	Excluded string    `json:"excluded,omitempty"` // why the cell has no number
	// Resumed names the raw exports taken from a dead run (publish -resume)
	// instead of timed by this one.
	Resumed []string `json:"resumed,omitempty"`
	// Base holds the times of the row's baseline (row.Baseline): the
	// same program with the kernel's repetitions set to 0, timed in every
	// sweep beside the cell.
	Base        []float64 `json:"base,omitempty"`
	BaseSummary *summary  `json:"base_summary,omitempty"`
	// An app cell's facts, from its checked run (app.go): the program's
	// peak memory, never a ratio, and the size of the file it wrote.
	Peak     *memPeak `json:"peak,omitempty"`
	OutBytes int64    `json:"out_bytes,omitempty"`
}

// rowInfo is what a results file keeps about a row.
type rowInfo struct {
	ID        string            `json:"id"`
	Table     string            `json:"table"`
	Algorithm string            `json:"algorithm"`
	Overflow  string            `json:"overflow"`
	Args      []string          `json:"args"`
	Checksum  string            `json:"checksum"`
	Notes     []string          `json:"notes,omitempty"`
	Counts    string            `json:"counts,omitempty"` // the agreed counts line at the quick size
	Skip      map[string]string `json:"skip,omitempty"`
	Run       int               `json:"run"` // index into results.Runs
	// The facts of the SIMD and concurrency rows.
	Cpu         string    `json:"cpu,omitempty"`
	Bytes       int64     `json:"bytes,omitempty"`
	Baseline    []string  `json:"baseline,omitempty"` // row.Baseline
	Concurrency *concSpec `json:"concurrency,omitempty"`
	// The app table's facts (app.go): each language's kind, libraries and
	// hand-written part, the row's environment, its Mk package and the
	// commit that last touched it, and its check.
	Kinds   map[string]string `json:"kinds,omitempty"`
	Libs    map[string][]lib  `json:"libs,omitempty"`
	Hand    map[string]string `json:"hand,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"` // printed beside each of the language's cells
	Env     map[string]string `json:"env,omitempty"`
	Package *pkgFact          `json:"package,omitempty"`
	Digest  bool              `json:"digest,omitempty"`
	Output  *outputSpec       `json:"output,omitempty"`
	// The std bars' facts of an idiomatic row (stdbars.go): the std
	// functions it prices and the decision of a stated exception.
	Std       []string `json:"std,omitempty"`
	Exception string   `json:"exception,omitempty"`
}

// results is results/<date>/results.json.
type results struct {
	Runs    []facts             `json:"runs"` // one per publish merged into this file, oldest first
	Columns []column            `json:"columns"`
	Builds  map[string]buildCmd `json:"builds"` // "<row> <column>" -> the command
	Rows    []rowInfo           `json:"rows"`
	Cells   []cell              `json:"cells"`
	Sweeps  int                 `json:"sweeps"`
	Options hfOptions           `json:"hyperfine"`
	// Platform is "" for the Windows tables and "linux" for the WSL table.
	Platform string `json:"platform,omitempty"`
	// Kind is "budgets" for the check-budget table, else "".
	Kind string `json:"kind,omitempty"`
	// codegenPublished is set by writeResultsMD when the date has a codegen
	// table, which then replaces the "not published" note.
	codegenPublished bool
}

type publishOptions struct {
	Root     string
	Work     string
	Mk       string
	Rows     []string
	Table    string
	LockPath string
	Mask     uint64
	Sweeps   int
	HF       hfOptions
	Date     string
	Merge    bool // keep the rows of an earlier publish of the same date that this run does not time
	// Resume reuses the raw exports a dead run of the same date left
	// (resumeRaw), so a publish killed in its reruns loses no sweep.
	Resume bool
	Log    func(string)
	// The Linux table (README "Linux", linux.go), set for the run inside WSL.
	Platform  string   // "linux": results go to results/<date>/linux
	NoLock    bool     // the machine lock is held by the Windows side
	Prebuilt  string   // directory of Mk programs cross-built on Windows
	Only      []string // only these columns (none: every column)
	MkLabel   string   // the mk that built Prebuilt, its SHA-256 and its identity (mkIdentity), for the facts
	MkSHA     string
	MkVersion string
	// Budgets runs the check-budget table (budgets.go), PGO the labelled PGO
	// table (pgo.go), and Tree restricts selection to tree rows (no lanes).
	Budgets bool
	PGO     bool
	Tree    bool
}

func publish(o publishOptions) error {
	// The Linux run inside WSL has no mk, only the identity publish-linux
	// read (it refuses first too), so -prebuilt requires that identity.
	if o.Prebuilt != "" && o.MkVersion == "" {
		return errors.New("refusing to publish: -prebuilt needs -mk-version, the identity of the mk that built it (publish-linux passes it), to check it for a tuning experiment")
	}
	if err := refuseExperiment(o.Mk, o.MkVersion); err != nil {
		return err
	}
	all, err := discover(o.Root)
	if err != nil {
		return err
	}
	logBroken(all, o.Log)
	table := o.Table
	if o.Budgets {
		if o.Platform != "" || o.Prebuilt != "" {
			return errors.New("-budgets is a Windows table of its own: no -platform or -prebuilt")
		}
		if table == "" {
			table = "same"
		}
		if table != "same" {
			return fmt.Errorf("-budgets measures the same-algorithm table, not %q", table)
		}
		if len(o.Only) == 0 {
			o.Only = budgetColumnIDs
		}
	}
	if o.PGO {
		if o.Budgets || o.Platform != "" || o.Prebuilt != "" || len(o.Only) > 0 {
			return errors.New("-pgo is a Windows table of its own: no -budgets, -platform, -prebuilt or -cols")
		}
		if table == "" {
			table = "same"
		}
		if table != "same" {
			return fmt.Errorf("-pgo measures the same-algorithm rows, not %q", table)
		}
	}
	if o.Tree {
		all = slices.DeleteFunc(all, func(r *row) bool { return r.Lane != "" })
	}
	rows, err := selectRows(all, table, o.Rows)
	if err == nil && o.Budgets {
		rows = slices.DeleteFunc(rows, func(r *row) bool { return r.Sources["mk"] == "" })
	}
	if err == nil && o.PGO {
		rows = pgoRows(rows)
	}
	if err == nil {
		err = prepareRows(o.Root, rows)
	}
	if err != nil {
		return err
	}
	if s := strayProfiles(o.Root); len(s) > 0 {
		return fmt.Errorf("refusing to run: a PGO profile would switch PGO on for one column: %s", strings.Join(s, ", "))
	}
	if !o.NoLock {
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
	f := gatherFacts(o.Root, t)
	f.Date = o.Date
	f.Started = time.Now().Format(time.RFC3339)
	f.MkBinary, f.MkSHA256 = o.Mk, fileSHA(o.Mk)
	f.MkStd = t.MkStd
	var only func(column) bool
	if len(o.Only) > 0 {
		only = func(c column) bool { return slices.Contains(o.Only, c.ID) }
	}
	var cbs []*built
	if o.Prebuilt != "" {
		t.MkPrebuilt = o.Prebuilt
		f.MkBinary, f.MkSHA256 = o.MkLabel, o.MkSHA
		// The commit the cross-building mk was built from, read on
		// the Windows side, not this tree's HEAD (required above).
		f.Versions["mk"] = o.MkVersion + ", cross-built on Windows (--target x86_64-linux) by " + o.MkLabel
	} else if !o.Budgets && !o.PGO {
		// Operation counts are a property of the sources; the Linux run and
		// the check-budget and PGO tables rely on the Windows table's check.
		cbs = buildCounting(t, rows, o.Log)
	}
	var bs []*built
	if o.PGO {
		bs = buildPGO(t, "publish", rows, o.Log)
	} else {
		bs = buildAll(t, "publish", rows, only, o.Log)
	}
	if o.Mask != 0 {
		if err := setAffinity(o.Mask); err != nil {
			return err
		}
		curMask = o.Mask
		f.Affinity = fmt.Sprintf("%#x (one logical CPU, set on tools/bench and inherited)", o.Mask)
	} else {
		f.Affinity = "taskset -c 2, set on tools/bench from outside and inherited"
	}

	res := results{Runs: []facts{f}, Columns: columns, Builds: map[string]buildCmd{}, Sweeps: o.Sweeps, Options: o.HF, Platform: o.Platform}
	if o.Platform == "linux" {
		res.Columns = linuxColumnFacts(columns)
	}
	outDir := filepath.Join(o.Root, "tests", "bench", "results", o.Date)
	if o.Platform != "" {
		outDir = filepath.Join(outDir, o.Platform)
	}
	if o.Budgets {
		res.Kind = "budgets"
		outDir = filepath.Join(outDir, "budgets")
	}
	if o.PGO {
		res.Kind = "pgo"
		res.Columns = append(slices.Clone(columns), pgoColumn)
		outDir = filepath.Join(outDir, "pgo")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	if o.Merge {
		var ids []string
		for _, r := range rows {
			ids = append(ids, r.id())
		}
		old, err := os.ReadFile(filepath.Join(outDir, "results.json"))
		if err == nil {
			var prev results
			if err := json.Unmarshal(old, &prev); err != nil {
				return fmt.Errorf("merge: %w", err)
			}
			if res, err = mergeResults(prev, res, ids); err != nil {
				return fmt.Errorf("merge into %s: %w", outDir, err)
			}
		}
	}
	run := len(res.Runs) - 1
	rawDir := filepath.Join(outDir, "raw")
	if err := os.MkdirAll(rawDir, 0o755); err != nil {
		return err
	}
	type live struct {
		b *built
		c *cell
	}
	var cells [][]live // per row
	for _, r := range rows {
		var rb []*built
		for _, b := range bs {
			if b.Row == r {
				rb = append(rb, b)
				res.Builds[r.id()+" "+b.Col.ID] = b.Cmd
			}
		}
		res.Rows = append(res.Rows, rowInfo{ID: r.id(), Table: r.Table, Algorithm: r.Algorithm, Overflow: r.Overflow,
			Args: r.Args, Checksum: r.Checksum, Notes: r.Notes, Skip: r.Skip, Run: run, Cpu: r.Cpu, Bytes: r.Bytes, Baseline: r.Baseline, Concurrency: r.Concurrency,
			Kinds: r.Kinds, Libs: r.Libs, Hand: r.Hand, Labels: r.Labels, Env: r.Env, Package: packageFact(o.Root, r.Package), Digest: r.Digest, Output: r.Output, Std: r.Std, Exception: r.Exception})
		checks, agreed, _ := verify(rb, r.runArgs, r.Checksum)
		if s := crossLine(checks); s != "" {
			o.Log(r.id() + " " + s)
		}
		// A language whose operation counts differ is not timed.
		var countEx map[string]string
		if len(r.Counts) > 0 && cbs != nil {
			cres, cagreed, _ := checkCounts(r, cbs)
			countEx = countExclusions(cres, cagreed)
			res.Rows[len(res.Rows)-1].Counts = cagreed
		}
		var lv []live
		for _, cr := range checks {
			c := &cell{Row: r.id(), Table: r.Table, Col: cr.B.Col.ID, Class: cr.B.Col.class(r), Checksum: cr.Sum}
			appCellFacts(c, r, cr) // app.go: class by kind, peak, output bytes
			switch {
			case cr.Err != nil:
				c.Excluded = "failed: " + firstLines(cr.Err.Error(), 3)
			case cr.Wrong:
				c.Excluded = fmt.Sprintf("checksum %s, expected %s", cr.Sum, agreed)
			case countEx[cr.B.Col.Lang] != "":
				c.Excluded = countEx[cr.B.Col.Lang]
			case countEx["*"] != "":
				c.Excluded = countEx["*"]
			}
			if c.Excluded != "" {
				o.Log(fmt.Sprintf("%s %s excluded: %s", r.id(), c.Col, c.Excluded))
			}
			lv = append(lv, live{cr.B, c})
		}
		cells = append(cells, lv)
	}
	// timeRaw times one program into the raw export p, or, with -resume,
	// reuses a dead run's export of the same program and arguments.
	timeRaw := func(c *cell, p string, b *built, args []string, opt hfOptions) ([]float64, bool, error) {
		// The cell's arguments, prepare and environment (app.go timedRun).
		run, err := timedRun(b, args)
		if err != nil {
			return nil, false, err
		}
		opt.Prepare = run.Prepare
		if o.Resume {
			if ts, ok := resumeRaw(p, b.Exe, run.Args, opt.Runs); ok {
				c.Resumed = append(c.Resumed, filepath.Base(p))
				return ts, true, nil
			}
		}
		ts, err := hyperfineCellEnv(t.Hyperfine, b.Exe, run.Args, opt, p, run.Env)
		return ts, false, err
	}
	// Each sweep runs every row with its columns in a rotated order, so
	// drift over the session does not land on one language.
	for s := range o.Sweeps {
		if s > 0 && o.Platform == "" {
			// The start of the run is not the whole run; another job that
			// starts a compiler or a test binary later is recorded too.
			res.Runs[run].BusyLater = mergeBusy(res.Runs[run].BusyLater, busyNow())
		}
		for _, lv := range cells {
			n := len(lv)
			for k := range n {
				x := lv[(k+s)%n]
				if x.c.Excluded != "" {
					continue
				}
				r := x.b.Row
				p := filepath.Join(rawDir, strings.ReplaceAll(r.id(), "/", "_"), fmt.Sprintf("%s.%d.json", x.c.Col, s))
				os.MkdirAll(filepath.Dir(p), 0o755)
				if err := rowAffinity(r, o.Mask); err != nil {
					return err
				}
				ts, resumed, err := timeRaw(x.c, p, x.b, r.runArgs, o.HF)
				if err != nil {
					x.c.Excluded = "hyperfine: " + firstLines(err.Error(), 3)
					o.Log(fmt.Sprintf("%s %s: %v", r.id(), x.c.Col, err))
					continue
				}
				x.c.Times = append(x.c.Times, ts...)
				from := ""
				if resumed {
					from = " (resumed from " + p + ")"
				}
				o.Log(fmt.Sprintf("sweep %d %s %s median %.4fs%s", s, r.id(), x.c.Col, median(ts), from))
				if len(r.runBaseline) > 0 {
					// The baseline right after its cell, in the same
					// sweep, so both see the same machine.
					bp := filepath.Join(rawDir, strings.ReplaceAll(r.id(), "/", "_"), fmt.Sprintf("%s.base.%d.json", x.c.Col, s))
					bs, _, err := timeRaw(x.c, bp, x.b, r.runBaseline, o.HF)
					if err != nil {
						x.c.Excluded = "baseline: " + firstLines(err.Error(), 3)
						o.Log(fmt.Sprintf("%s %s baseline: %v", r.id(), x.c.Col, err))
						continue
					}
					x.c.Base = append(x.c.Base, bs...)
					o.Log(fmt.Sprintf("sweep %d %s %s baseline median %.4fs", s, r.id(), x.c.Col, median(bs)))
				}
			}
		}
	}
	// A cell above the noise threshold is rerun once with the pooled
	// run count, and is marked noisy if it is still above it.
	for _, lv := range cells {
		for _, x := range lv {
			if x.c.Excluded != "" {
				continue
			}
			if summarize(x.c.Times).RSD > noisyRSD {
				r := x.b.Row
				p := filepath.Join(rawDir, strings.ReplaceAll(r.id(), "/", "_"), x.c.Col+".rerun.json")
				opt := o.HF
				opt.Runs = len(x.c.Times)
				if err := rowAffinity(r, o.Mask); err != nil {
					return err
				}
				run, err := timedRun(x.b, r.runArgs)
				if err != nil {
					return err
				}
				opt.Prepare = run.Prepare
				ts, ok := []float64(nil), false
				if o.Resume {
					if ts, ok = resumeRaw(p, x.b.Exe, run.Args, opt.Runs); ok {
						x.c.Resumed = append(x.c.Resumed, filepath.Base(p))
						o.Log(fmt.Sprintf("rerun %s %s resumed from %s", r.id(), x.c.Col, p))
					}
				}
				if !ok {
					ts, err = hyperfineCellEnv(t.Hyperfine, x.b.Exe, run.Args, opt, p, run.Env)
				}
				if err == nil {
					x.c.Times = ts
				}
				x.c.Noisy = summarize(x.c.Times).RSD > noisyRSD
				o.Log(fmt.Sprintf("rerun %s %s: RSD %.1f%%, noisy %v", r.id(), x.c.Col, summarize(x.c.Times).RSD*100, x.c.Noisy))
			}
			x.c.Summary = summarize(x.c.Times)
			if len(x.c.Base) > 0 {
				bs := summarize(x.c.Base)
				x.c.BaseSummary = &bs
			}
		}
	}
	for _, lv := range cells {
		for _, x := range lv {
			res.Cells = append(res.Cells, *x.c)
		}
	}
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "results.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	return writeResultsMD(o.Root, o.Date)
}

// curMask is the affinity tools/bench last set (0: never set by rowAffinity).
var curMask uint64

// rowAffinity sets tools/bench's own affinity, which hyperfine and the
// timed program inherit, to the row's: the one-CPU mask of the run, or
// a concurrency row's CPU set. It does nothing when the run sets no
// mask (the Linux run is pinned from outside).
func rowAffinity(r *row, runMask uint64) error {
	m, change, err := nextMask(r, runMask, curMask)
	if err != nil || !change {
		return err
	}
	if err := setAffinity(m); err != nil {
		return err
	}
	curMask = m
	return nil
}

// nextMask is rowAffinity's decision, apart from the system call: the mask
// row r's programs need when the run's mask is runMask and cur is set now,
// and whether it must be set. A run with no mask (runMask 0, the Linux run)
// never sets one.
func nextMask(r *row, runMask, cur uint64) (uint64, bool, error) {
	if runMask == 0 {
		return 0, false, nil
	}
	m := runMask
	if r.Concurrency != nil {
		v, err := parseMask(r.Concurrency.Mask)
		if err != nil {
			return 0, false, err
		}
		m = v
	}
	return m, m != cur, nil
}

// logBroken reports the tests/bench-lanes rows that did not load and are
// left out.
func logBroken(rows []*row, log func(string)) {
	for _, r := range brokenRows(rows) {
		log(fmt.Sprintf("left out, does not load: %s: %s", r.id(), r.Broken))
	}
}

// writeResultsMD renders RESULTS.md from results/<date>/results.json (the
// Windows tables) and, when it exists, results/<date>/linux/results.json.
func writeResultsMD(root, date string) error {
	dir := filepath.Join(root, "tests", "bench", "results", date)
	load := func(p string) (*results, error) {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var res results
		if err := json.Unmarshal(b, &res); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		return &res, nil
	}
	win, err := load(filepath.Join(dir, "results.json"))
	if err != nil {
		return err
	}
	// A section this date has not measured is carried from the newest
	// earlier date that has it, under that date's name and with a line
	// saying so: it is that run's table, with that run's mk and facts, and
	// never merged into this date's rows.
	carried := func(sub string) string {
		d := latestWith(root, date, filepath.Join(sub, "results.json"))
		if d != "" && d != date {
			return d
		}
		return date
	}
	carryNote := func(from, what string) string {
		if from == date {
			return ""
		}
		return fmt.Sprintf("*Carried from the publish of %s: the %s was not rerun with %s's mk, so its numbers are that run's, with the facts printed here.*\n\n", from, what, date)
	}
	cgDate := carried("codegen")
	cg, err := loadCodegenIfAny(root, cgDate)
	if err != nil {
		return err
	}
	win.codegenPublished = cg != nil
	md := renderResults(win, date)
	if cg != nil {
		md += withNote(renderCodegen(cg, cgDate), carryNote(cgDate, "codegen table"))
	}
	linDate := carried("linux")
	if lin, err := load(filepath.Join(root, "tests", "bench", "results", linDate, "linux", "results.json")); err == nil {
		md += withNote(renderLinux(lin, linDate), carryNote(linDate, "Linux table"))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	csDate := carried("compile")
	if cs, err := loadCompileIfAny(root, csDate); err != nil {
		return err
	} else if cs != nil {
		md += withNote(renderCompile(cs, csDate), carryNote(csDate, "compile-speed table"))
	}
	// The other compile tables, each in its own directory of a date and
	// carried the same way: the Linux run, the real programs, and the
	// build time of the benchmark programs.
	clDate := carried("compile-linux")
	if b, err := os.ReadFile(filepath.Join(root, "tests", "bench", "results", clDate, "compile-linux", "results.json")); err == nil {
		var cl compileResults
		if err := json.Unmarshal(b, &cl); err != nil {
			return fmt.Errorf("compile-linux results of %s: %w", clDate, err)
		}
		md += withNote(renderCompile(&cl, clDate), carryNote(clDate, "Linux compile-speed table"))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	crDate := carried("compile-real")
	if cr, err := loadRealIfAny(root, crDate); err != nil {
		return err
	} else if cr != nil {
		md += withNote(renderReal(cr, crDate), carryNote(crDate, "real-program compile table"))
	}
	btDate := carried("buildtime")
	if bt, err := loadBuildTimeIfAny(root, btDate); err != nil {
		return err
	} else if bt != nil {
		md += withNote(renderBuildTime(bt, btDate), carryNote(btDate, "build-time table"))
	}
	htDate := carried("http")
	if ht, err := loadHTTPIfAny(root, htDate); err != nil {
		return err
	} else if ht != nil {
		md += withNote(renderHTTP(ht, htDate), carryNote(htDate, "http row"))
	}
	// The app table's server rows (serverbench.go).
	for _, name := range serverRows {
		sd := carried(name)
		if sr, err := loadServerIfAny(root, sd, name); err != nil {
			return err
		} else if sr != nil {
			md += withNote(renderServer(sr, sd), carryNote(sd, name+" row"))
		}
	}
	if bud, err := load(filepath.Join(dir, "budgets", "results.json")); err == nil {
		md += renderBudgets(bud, date)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if pg, err := load(filepath.Join(dir, "pgo", "results.json")); err == nil {
		md += renderPGO(pg, date)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.WriteFile(filepath.Join(root, "tests", "bench", "RESULTS.md"), []byte(md), 0o644)
}

// latestWith is the newest results date not after date whose directory
// holds rel, or "" when none does. Dates are YYYY-MM-DD names, so byte
// order is time order.
func latestWith(root, date, rel string) string {
	ents, err := os.ReadDir(filepath.Join(root, "tests", "bench", "results"))
	if err != nil {
		return ""
	}
	best := ""
	for _, e := range ents {
		n := e.Name()
		if !e.IsDir() || n > date || n <= best {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, "tests", "bench", "results", n, rel)); err == nil {
			best = n
		}
	}
	return best
}

// withNote puts note under a section's first line (its heading).
func withNote(section, note string) string {
	if note == "" {
		return section
	}
	head, rest, _ := strings.Cut(section, "\n\n")
	return head + "\n\n" + note + rest
}

// renderLinux is the Linux section of RESULTS.md: its facts and its tables,
// under one heading.
func renderLinux(res *results, date string) string {
	var w strings.Builder
	fmt.Fprintf(&w, "# Linux (x86_64-linux in WSL 2)\n\nFrom `tests/bench/results/%s/linux/results.json`. Mk against C only: the Mk programs are cross-built on Windows by the same mk (`--target x86_64-linux`), the C programs are built inside WSL by clang 21 against glibc and linked statically by lld, and everything is timed inside WSL. WSL 2 is a virtual machine: `taskset -c 2` pins a virtual CPU, not a physical one.\n\n", date)
	for i := range res.Runs {
		renderFacts(&w, res, i)
	}
	renderTables(&w, res, res.Runs[len(res.Runs)-1])
	return w.String()
}

// mergeResults keeps prev's rows other than ids, with their cells, builds
// and the runs they came from, and appends next's run (next holds one run
// and no rows yet). It refuses when a kept run measured with another mk
// binary, std or toolchain than next's: one table holds one set of
// compilers, so its column legend and its mk / c median are true of every
// row.
func mergeResults(prev, next results, ids []string) (results, error) {
	out := next
	out.Runs = nil
	out.Rows, out.Cells = nil, nil
	keep := map[int]int{} // prev run index -> out run index
	for _, r := range prev.Rows {
		if slices.Contains(ids, r.ID) {
			continue
		}
		if _, ok := keep[r.Run]; !ok && r.Run < len(prev.Runs) {
			keep[r.Run] = len(out.Runs)
			out.Runs = append(out.Runs, prev.Runs[r.Run])
		}
		r.Run = keep[r.Run]
		out.Rows = append(out.Rows, r)
		for _, c := range prev.Cells {
			if c.Row == r.ID {
				out.Cells = append(out.Cells, c)
			}
		}
		for k, b := range prev.Builds {
			if strings.HasPrefix(k, r.ID+" ") {
				out.Builds[k] = b
			}
		}
	}
	for i, f := range out.Runs {
		for _, n := range next.Runs {
			if d := toolchainDiff(f, n); d != "" {
				var kept []string
				for _, r := range out.Rows {
					if r.Run == i {
						kept = append(kept, r.ID)
					}
				}
				return results{}, fmt.Errorf("the kept rows %s were measured with %s; publish them again with this mk and toolchain, or use -merge=false or another -date (one table, one toolchain)",
					strings.Join(kept, ", "), d)
			}
		}
	}
	out.Runs = append(out.Runs, next.Runs...)
	return out, nil
}

// toolchainDiff names the first difference between two runs' mk binary,
// std and toolchain versions, or is "" when they match.
func toolchainDiff(a, b facts) string {
	if a.MkSHA256 != b.MkSHA256 {
		return fmt.Sprintf("mk sha256 %s, this run with %s", orDash(short(a.MkSHA256)), orDash(short(b.MkSHA256)))
	}
	if a.MkStd != b.MkStd {
		return fmt.Sprintf("std (MK_STD) %q, this run with %q", a.MkStd, b.MkStd)
	}
	var keys []string
	for k := range a.Versions {
		keys = append(keys, k)
	}
	for k := range b.Versions {
		if _, ok := a.Versions[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, k := range keys {
		if a.Versions[k] != b.Versions[k] {
			return fmt.Sprintf("%s %q, this run with %q", k, a.Versions[k], b.Versions[k])
		}
	}
	return ""
}

// rowRuns is the runs of res that measured at least one row of table tb
// ("" for every table), in order.
func rowRuns(res *results, tb string) []int {
	var out []int
	for _, r := range res.Rows {
		if (tb == "" || r.Table == tb) && r.Run < len(res.Runs) && !slices.Contains(out, r.Run) {
			out = append(out, r.Run)
		}
	}
	slices.Sort(out)
	return out
}

// legendVersion is the toolchain of language key lang for the column
// legend: the last run's, or, when the runs of the file disagree, every
// run's with its number (for a file merged before mergeResults
// refused it).
func legendVersion(res *results, last facts, lang string) string {
	runs := rowRuns(res, "")
	var vs []string
	for _, i := range runs {
		if v := res.Runs[i].Versions[lang]; !slices.Contains(vs, v) {
			vs = append(vs, v)
		}
	}
	if len(vs) <= 1 {
		return orDash(last.Versions[lang])
	}
	var parts []string
	for _, i := range runs {
		parts = append(parts, fmt.Sprintf("run %d: %s", i+1, orDash(res.Runs[i].Versions[lang])))
	}
	return strings.Join(parts, "; ")
}

// ratioEntry is one column's ratio to a reference column of its row.
type ratioEntry struct {
	Ratio float64
	CI    interval
}

// bestOf is the cell with the smallest median among cs that keep(c).
func bestOf(cs []cell, keep func(cell) bool) *cell {
	var best *cell
	for i := range cs {
		c := &cs[i]
		if c.Excluded != "" || len(c.Times) == 0 || !keep(*c) {
			continue
		}
		if best == nil || c.Summary.Median < best.Summary.Median {
			best = c
		}
	}
	return best
}

func ratioTo(c, ref *cell) ratioEntry {
	if ref == nil || c.Excluded != "" || len(c.Times) == 0 {
		return ratioEntry{Ratio: math.NaN()}
	}
	return ratioEntry{Ratio: c.Summary.Median / ref.Summary.Median, CI: ratioCI(c.Times, ref.Times)}
}

func fmtSec(s float64) string {
	switch {
	case s >= 1:
		return fmt.Sprintf("%.3f s", s)
	case s >= 1e-3:
		return fmt.Sprintf("%.1f ms", s*1e3)
	default:
		return fmt.Sprintf("%.0f us", s*1e6)
	}
}

func fmtRatio(e ratioEntry) string {
	if math.IsNaN(e.Ratio) {
		return "-"
	}
	return fmt.Sprintf("%.2f [%.2f, %.2f]", e.Ratio, e.CI.Lo, e.CI.Hi)
}

// cellText makes s safe inside one Markdown table cell: whitespace runs
// (newlines included) become one space, a pipe is escaped, and text beyond
// 200 bytes is cut with an ellipsis, so a compiler's multi-line diagnostic
// cannot break the table.
func cellText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

// renderResults writes RESULTS.md from one run. Every number in it comes
// from res, so the file says only what the run measured.
func renderResults(res *results, date string) string {
	var w strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&w, format, a...) }
	p("# Benchmark results\n\n")
	p("Written by `go run ./tools/bench publish` on %s. Do not edit by hand: every number here comes from `tests/bench/results/%s/results.json`, whose raw hyperfine exports are beside it. The contract these numbers are held to is `tests/bench/README.md`.\n\n", date, date)
	for i := range res.Runs {
		var ids []string
		for _, r := range res.Rows {
			if r.Run == i {
				ids = append(ids, r.ID)
			}
		}
		if len(res.Runs) > 1 {
			p("**Run %d** (rows %s)\n\n", i+1, strings.Join(ids, ", "))
		}
		renderFacts(&w, res, i)
	}
	renderTables(&w, res, res.Runs[len(res.Runs)-1])
	return w.String()
}

// renderFacts prints the facts of run i of res.
func renderFacts(w *strings.Builder, res *results, i int) {
	f := res.Runs[i]
	p := func(format string, a ...any) { fmt.Fprintf(w, format, a...) }
	p("| Fact | Value |\n|---|---|\n")
	dirty := ""
	if f.Dirty {
		dirty = " (working tree had uncommitted changes to tracked files)"
	}
	p("| Commit | `%s`%s |\n", f.Commit, dirty)
	p("| mk | `%s`, sha256 `%s` |\n", binName(f.MkBinary), short(f.MkSHA256))
	if f.MkStd != "" {
		p("| std (MK_STD) | `%s` |\n", f.MkStd)
	}
	if f.Started != "" {
		p("| Started | %s |\n", f.Started)
	}
	p("| Machine | %s, %s |\n", f.CPU, f.OS)
	p("| Power scheme | %s |\n", f.Power)
	p("| Defender real-time protection | %s |\n", orDash(f.Defender))
	p("| Affinity | %s |\n", f.Affinity)
	p("| GOGC | %s |\n", f.GOGC)
	if f.RunEnv != "" {
		p("| Program environment | %s |\n", f.RunEnv)
	}
	p("| Timing | hyperfine `-N --warmup %d --runs %d`, %d sweeps in rotated column order, pooled; RSD above %.0f%% reruns once, then the cell is marked noisy |\n", res.Options.Warmup, res.Options.Runs, res.Sweeps, noisyRSD*100)
	busy := "none"
	if len(f.Busy) > 0 {
		busy = strings.Join(f.Busy, ", ")
	}
	p("| Other compilers and test binaries running at the start | %s |\n", busy)
	if len(f.BusyLater) > 0 {
		p("| The same, at the start of a later sweep | %s |\n", strings.Join(f.BusyLater, ", "))
	}
	p("\n")
	noisy, timed := runNoise(res, i)
	if why := sharedMachine(f, noisy, timed); why != "" {
		p("The machine was shared during this run (%s), so these numbers are a first table, not the final one: their medians cannot carry the 1.25 bar. The final table is measured on a quiet machine.\n\n", why)
	}
}

// noisyShare is the loaded-machine limit: a run in which more than this
// share of the timed cells stayed noisy after their rerun ran on a loaded machine,
// whatever the process list said when it started.
const noisyShare = 0.25

// runNoise counts the timed cells of run i (rows whose Run is i, cells not
// excluded) and how many of them are marked noisy.
func runNoise(res *results, i int) (noisy, timed int) {
	rows := map[string]bool{}
	for _, r := range res.Rows {
		if r.Run == i {
			rows[r.ID] = true
		}
	}
	for _, c := range res.Cells {
		if !rows[c.Row] || c.Excluded != "" || len(c.Times) == 0 {
			continue
		}
		timed++
		if c.Noisy {
			noisy++
		}
	}
	return noisy, timed
}

// sharedMachine says why a run's numbers are provisional (a "first table": a
// shared machine's run), or "" when nothing shows the machine was shared:
// compilers or test binaries at its start or at a later sweep's, or more than
// noisyShare of its cells noisy after their rerun.
func sharedMachine(f facts, noisy, timed int) string {
	var why []string
	if len(f.Busy) > 0 {
		why = append(why, "compilers or test binaries were running at its start")
	}
	if len(f.BusyLater) > 0 {
		why = append(why, "some were running at the start of a later sweep")
	}
	if timed > 0 && float64(noisy) > noisyShare*float64(timed) {
		why = append(why, fmt.Sprintf("%d of its %d timed cells stayed noisy after their rerun", noisy, timed))
	}
	return strings.Join(why, "; ")
}

// renderTables prints the column legend and every table.
func renderTables(w *strings.Builder, res *results, f facts) {
	p := func(format string, a ...any) { fmt.Fprintf(w, format, a...) }
	p("## Columns\n\n| Column | Toolchain | Bounds | Overflow | Panic | LTO | Linker | CRT | std at x86-64-v2 |\n|---|---|---|---|---|---|---|---|---|\n")
	used := map[string]bool{}
	for _, c := range res.Cells {
		used[c.Col] = true
	}
	for _, c := range res.Columns {
		if !used[c.ID] {
			continue
		}
		p("| %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", c.ID, strings.ReplaceAll(legendVersion(res, f, langKey(c.Lang)), "|", "\\|"), c.Bounds, c.Overflow, c.Panic, c.LTO, c.Linker, c.CRT, c.StdV2)
	}
	p("\nThe build command of every cell is in `results.json` (`builds`). Only Mk and C share an LLVM (clang 21); against Rust (its own LLVM), Zig (its bundled LLVM), Go (gc) and C# (.NET 11 RyuJIT / NativeAOT), a row compares whole toolchains and their std, not code generators alone. `cs` includes JIT start-up in the process time and `cs-aot` does not. On Windows, Mk's release path is distributed ThinLTO (internal/toolchain `thinLink`, the default since b166ee6): lld-link writes the index and clang runs each backend at -O3, so the SLP vectorizer runs as in clang -O3 and on Linux's ld.lld; an mk older than b166ee6 ran lld-link's in-process backend, which runs none.\n\n")

	tables := []string{}
	for _, r := range res.Rows {
		if !slices.Contains(tables, r.Table) {
			tables = append(tables, r.Table)
		}
	}
	for _, tb := range tables {
		p("## Table %q\n\n", tb)
		if intro := tableIntro[tb]; intro != "" {
			p("%s\n\n", intro)
		}
		var cols []string
		for _, c := range res.Columns {
			for _, x := range res.Cells {
				if x.Table == tb && x.Col == c.ID && !slices.Contains(cols, c.ID) {
					cols = append(cols, c.ID)
				}
			}
			// Outside the same-algorithm table a language with no std
			// facility keeps its column, so its reason is read in the
			// table ("no std JSON in C"), not lost.
			if tb != "same" && !c.Budget && slices.Contains([]string{"mk", "c", "rust", "zig", "go", "cs"}, c.Lang) && !slices.Contains(cols, c.ID) {
				for _, r := range res.Rows {
					if r.Table == tb && r.Skip[c.Lang] != "" {
						cols = append(cols, c.ID)
						break
					}
				}
			}
		}
		p("%s", gridHead(tb)) // render_app.go: the app grid has kinds, no class
		for _, c := range cols {
			p(" %s |", c)
		}
		p("\n%s%s\n", gridSep(tb), strings.Repeat("---|", len(cols)))
		for _, r := range res.Rows {
			if r.Table != tb {
				continue
			}
			cs, _ := kernelCells(rowCells(res, r.ID))
			best := bestOf(cs, func(cell) bool { return true })
			p("%s", gridLead(tb, r))
			for _, col := range cols {
				c := findCell(cs, col)
				switch {
				case c == nil:
					p(" %s |", skipText(r, col))
				case c.Excluded != "":
					p(" excluded (%s) |", cellText(c.Excluded))
				default:
					e := ratioTo(c, best)
					noisy := ""
					if c.Noisy {
						noisy = ", noisy"
					}
					p(" %s, x%.2f%s%s |", fmtSec(c.Summary.Median), e.Ratio, noisy, gridKind(tb, r, col))
				}
			}
			p("\n")
		}
		p("\n")
		for _, r := range res.Rows {
			if r.Table != tb {
				continue
			}
			if tb == "app" {
				renderAppRow(w, res, r) // render_app.go
				continue
			}
			whole := rowCells(res, r.ID)
			cs, kernel := kernelCells(whole)
			best := bestOf(cs, func(cell) bool { return true })
			p("### %s\n\n%s Arguments `%s`, checksum `%s`.", r.ID, r.Algorithm, strings.Join(r.Args, " "), r.Checksum)
			if kernel {
				p(" Baseline `%s` (the same program with no kernel repetition: start, input fill and first-touch page faults), timed in every sweep beside each cell; Median, Min, MAD, RSD, the ratios and GB/s are the kernel's, each run's time minus its cell's baseline median, and the ratio intervals resample the kernel runs with that median fixed.", strings.Join(r.Baseline, " "))
			} else if r.Bytes > 0 {
				p(" Timed before baselines: every number is the whole program's, the input fill included, and GB/s divides the kernel's bytes by that time, so it understates every kernel and can rank fill loops.")
			}
			if best != nil && best.Summary.Median < 1 && r.Table != "startup" {
				p(" The fastest cell ran under 1 s, below the contract's floor; the row's size is to be raised.")
			}
			if cpu := r.Cpu; cpu != "" && cpu != "x86-64-v2" {
				p(" CPU target `%s` in every column.", cpu)
			}
			if cc := r.Concurrency; cc != nil {
				p(" Concurrency row: every program runs on the CPU set `%s` with %d threads (GOMAXPROCS and MK_CARRIERS); each cell names its model.", cc.Mask, cc.Threads)
			}
			extra, extraSep := "", ""
			if r.Concurrency != nil {
				extra, extraSep = " Model |", "---|"
			}
			switch {
			case kernel:
				extra, extraSep = extra+" Whole run (median) | Baseline (median, share of the run) | GB/s (kernel) |", extraSep+"---|---|---|"
			case r.Bytes > 0:
				extra, extraSep = extra+" GB/s (median, fill included) |", extraSep+"---|"
			}
			p("\n\n| Column | Class | Median | Min | MAD | RSD | Runs | Ratio to best [95%%] | Ratio to best of its class [95%%] |%s\n|---|---|---|---|---|---|---|---|---|%s\n", extra, extraSep)
			for i := range cs {
				c := &cs[i]
				more := ""
				if r.Concurrency != nil {
					more += " " + orDash(cellText(r.Concurrency.Models[langOf(c.Col)])) + " |"
				}
				if c.Excluded != "" {
					switch {
					case kernel:
						more += " | | |"
					case r.Bytes > 0:
						more += " |"
					}
					p("| %s | %s | excluded: %s | | | | | | |%s\n", c.Col, c.Class, cellText(c.Excluded), more)
					continue
				}
				switch {
				case kernel:
					w := findCell(whole, c.Col)
					gbs := "-"
					if c.Summary.Median > 0 {
						gbs = fmt.Sprintf("%.2f", float64(r.Bytes)/c.Summary.Median/1e9)
					}
					more += fmt.Sprintf(" %s | %s (%.0f%%) | %s |", fmtSec(w.Summary.Median), fmtSec(w.BaseSummary.Median), 100*w.BaseSummary.Median/w.Summary.Median, gbs)
				case r.Bytes > 0:
					more += fmt.Sprintf(" %.2f |", float64(r.Bytes)/c.Summary.Median/1e9)
				}
				cb := bestOf(cs, func(x cell) bool { return x.Class == c.Class })
				p("| %s | %s | %s | %s | %s | %.1f%% | %d | %s | %s |%s\n", c.Col, c.Class, fmtSec(c.Summary.Median), fmtSec(c.Summary.Min),
					fmtSec(c.Summary.MAD), c.Summary.RSD*100, c.Summary.N, fmtRatio(ratioTo(c, best)), fmtRatio(ratioTo(c, cb)), more)
			}
			for _, n := range r.Notes {
				p("\n- %s", n)
			}
			p("\n\n")
		}
		if tb == "idiomatic" {
			renderStdBars(w, res, tb)
		}
		if tb == "same" {
			p("### Mk against C (the codegen metric, same LLVM)\n\n| Row | mk / c [95%%] | Against 1.10 | mk-wrap / c [95%%] |\n|---|---|---|---|\n")
			var ratios []float64
			for _, r := range res.Rows {
				if r.Table != tb {
					continue
				}
				cs := rowCells(res, r.ID)
				mk, c, mw := findCell(cs, "mk"), findCell(cs, "c"), findCell(cs, "mk-wrap")
				if mk == nil || c == nil || mk.Excluded != "" || c.Excluded != "" {
					continue
				}
				e := ratioTo(mk, c)
				ratios = append(ratios, e.Ratio)
				ew := "-"
				if mw != nil && mw.Excluded == "" {
					ew = fmtRatio(ratioTo(mw, c))
				}
				p("| %s | %s | %s | %s |\n", r.ID, fmtRatio(e), classify(e.CI, 1.10), ew)
			}
			mixed := false
			if rs := rowRuns(res, tb); len(rs) > 1 {
				for _, i := range rs[1:] {
					mixed = mixed || toolchainDiff(res.Runs[rs[0]], res.Runs[i]) != ""
				}
			}
			switch {
			case len(ratios) > 0 && mixed:
				// A median over two compilers is not one compiler's.
				p("\nNo median of mk / c: these rows were measured by runs with different mk binaries, std or toolchains (see each run's facts).\n\n")
			case len(ratios) > 0:
				p("\nMedian of mk / c over these rows: %.2f (%d rows).\n\n", median(ratios), len(ratios))
			}
		}
	}
	if !slices.Contains(tables, "codegen") && res.Platform == "" && !res.codegenPublished {
		p("## Table \"codegen\"\n\nNot published yet: the in-process Mk-against-C harness needs a way to link a C object into a Mk build from tools/bench.\n")
	}
}

// tableIntro is the paragraph that opens each table of RESULTS.md.
var tableIntro = map[string]string{
	"same":      "Same algorithm: each row is one reference algorithm written in every language with the same data structures, loop structure and arithmetic types (its `README.md`). Whole programs, timed from outside. Cells: median wall time, and the ratio of medians to the fastest cell of the row with its 95% bootstrap interval.",
	"startup":   "Startup: process start and one line of output. Reported, never subtracted from another row. Cells: median wall time, and the ratio of medians to the fastest cell.",
	"idiomatic": "Idiomatic standard library: each row states a task, and each language does it with its own standard library in its idiomatic form (the row's `README.md`), so a row compares libraries and their algorithms, not code generators. A language with no standard facility for the task has no cell and says so; no third-party library stands in. Cells: median wall time, and the ratio of medians to the fastest cell of the row.",
	"app":       "Applications: each row is a real program that uses its language's standard library, and states its task, its input and its output (the row's `README.md`), not its code. Each cell has a kind: `std` (the standard library only), `eco` (named libraries pinned to a version) or `hand` (the program writes a facility itself). Cells: median wall time, and the ratio of medians to the fastest cell of the row. These ratios never feed the median mk / c or the 1.10 bar; the table has no verdict beyond the defect line of each row.",
	"simd":      "SIMD: each row is one vector kernel written with each language's own SIMD layer (C `<immintrin.h>`, Rust `std::arch`, Zig `@Vector`, Go scalar or its assembly-backed std, C# `System.Numerics.Vector<T>`, Mk `std.simd`) at one fixed CPU target with no run-time dispatch, at an L1, an L3 and a DRAM size, every buffer starting on a 64-byte boundary. Cells: median time and the ratio of medians to the fastest cell of the row; in a row with a baseline the time is the kernel's, the whole run minus the baseline's median, and otherwise the whole program's, its input fill included. GB/s in the row's own table.",
}

// skipText is the cell of a column row r does not have: in the
// same-algorithm table a baseline "not written" with its reason; in the
// other tables the reason itself ("no std JSON in C"), or a pending Mk side.
func skipText(r rowInfo, col string) string {
	reason := r.Skip[langOf(col)]
	switch {
	case reason == "":
		return "-"
	case r.Table == "same":
		return "not written: " + reason
	}
	return cellText(reason)
}

func langOf(col string) string {
	if c, ok := columnByID(col); ok {
		return c.Lang
	}
	return col
}

func langKey(lang string) string {
	if lang == "mk-wrap" || lang == "mk-keyed" || lang == "mk-ctx" {
		return "mk"
	}
	return lang
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// binName is the file name of an mk binary's path, either separator, so a
// published table names the build by its name and hash, not by where it
// was on the measuring machine.
func binName(p string) string {
	return p[strings.LastIndexAny(p, `/\`)+1:]
}

func short(s string) string {
	if len(s) > 16 {
		return s[:16]
	}
	return s
}

// acquireLock takes the machine lock, logging while it waits.
func acquireLock(path string, log func(string)) (interface {
	Release() error
	Path() string
}, error) {
	l, err := machinelockAcquire(path, func(w time.Duration) {
		log(fmt.Sprintf("waiting for the machine lock %s (%s)", path, w.Round(time.Second)))
	})
	if err != nil {
		return nil, err
	}
	return l, nil
}
