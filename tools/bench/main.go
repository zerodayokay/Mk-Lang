// Why: a published benchmark number is worth something only when one tool
// builds, checks and times every language the same way; this is that tool.
//
// What: the bench command's entry point: flag parsing, the subcommand
// dispatch (list, gen, verify, publish, compare, render, codegen, compile,
// http, kv, ir, editloop, framepointers and the Linux legs) and the
// toolchain set-up they share.
//
// How: each subcommand discovers the rows under tests/bench, builds the
// columns into the work directory, checks every checksum before it times
// anything, and only `publish` variants write under tests/bench/results.

// Command bench is the only way a benchmark number gets into
// tests/bench/RESULTS.md. The contract it enforces is tests/bench/README.md.
//
//	go run ./tools/bench list                         the rows and their columns
//	go run ./tools/bench gen [rows]                   write the data files of rows that read one
//	go run ./tools/bench verify [-publish-size] [rows] build every column, run it, check the checksums
//	go run ./tools/bench publish [rows]               the published table, under the machine lock
//	go run ./tools/bench publish -budgets [rows]      the check-budget table (Mk with and without its checks)
//	go run ./tools/bench publish -pgo [rows]          the labelled PGO table (Mk with and without its row's profile)
//	go run ./tools/bench verify -pgo [rows]           train each same row's profile and check both PGO columns, untimed
//	go run ./tools/bench compare --base A --head B rows   provisional A/B of two mk executables
//	go run ./tools/bench render [-date D]             rewrite RESULTS.md from results/<D>/results.json
//	go run ./tools/bench codegen [-quick] [kernels]   build the codegen kernels, check, provisional times
//	go run ./tools/bench codegen -publish [kernels]   the codegen table, under the machine lock
//	go run ./tools/bench link [-o out] <pkg> <file.c> a Mk program with one freestanding C file linked in
//	go run ./tools/bench compile [-quick] [-langs L]    the compile-speed builds, checked, provisional times
//	go run ./tools/bench compile -publish [-date D]    the compile-speed table, under the machine lock
//	go run ./tools/bench compile -real [-publish] [-vs L=mk,...]  real Mk programs, across mk binaries
//	go run ./tools/bench compile-linux -mk <linux mk>  the compile table's Mk builds inside WSL, under the WSL lock
//	go run ./tools/bench buildtime [-publish] [rows]   each benchmark program's build time from empty caches
//	go run ./tools/bench http [-publish] [-date D]     the idiomatic http row: std servers under a load generator
//	go run ./tools/bench http -row httpapi [-check|-publish]  the app table's JSON API row
//	go run ./tools/bench kv [-check|-publish] [-date D] the app table's kvserve row: key-value servers under tools/bench's kv generator
//	go run ./tools/bench ir [-func F] [-cfunc G] <row>  Mk's IR, remarks and machine code beside the C twin's (no timing)
//	go run ./tools/bench editloop -mk M [-vs L=mk,...] -ws DIR -by NAME   the edit loop on tools/synth's workspace, provisional (editloop.go)
//	go run ./tools/bench framepointers [-mk M] [-count time|instructions] [rows]  frame pointers off and on, provisional (framepointers.go)
//
// Every subcommand refuses to run when a default.mkprof or default.pgo is
// under tests/bench or tests/bench-lanes. Builds and caches go to the
// work directory (-work, else $MK_BENCH_WORK, else the bench directory of
// the work root tools/gate uses: $MK_WORK, else mk-work in the user's cache
// directory), never into the tree. The machine lock (-lock) is machine.lock
// in that work root, whatever -work is (CONTRIBUTING.md).
package main

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"mk/tools/machinelock"
)

func main() {
	// The Linux peak spawner (mem_linux.go) runs before anything else.
	if len(os.Args) > 1 && os.Args[1] == spawnArg {
		os.Exit(spawnMain(os.Args[2:]))
	}
	if tool := shimTool(os.Args[0]); tool != "" {
		os.Exit(runShim(tool, os.Args[0], os.Args[1:])) // shim.go
	}
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func machinelockAcquire(path string, wait func(time.Duration)) (*machinelock.Lock, error) {
	return machinelock.Acquire(path, wait)
}

// defaultWork is -work's default: $MK_BENCH_WORK, else bench in the shared
// work root.
func defaultWork() string {
	if w := os.Getenv("MK_BENCH_WORK"); w != "" {
		return w
	}
	return filepath.Join(machinelock.WorkRoot(), "bench")
}

func fileSHA(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// buildMk builds ./cmd/mk of the tree into the work directory, for runs
// that are not given -mk.
func buildMk(root, work string) (string, error) {
	out := filepath.Join(work, "mk-tree.exe")
	cmd := exec.Command("go", "build", "-o", out, "./cmd/mk")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOFLAGS=")
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("building ./cmd/mk failed (pass -mk <path> to measure another mk): %v\n%s", err, firstLines(string(b), 10))
	}
	return out, nil
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: bench list|gen|verify|publish|publish-linux|compare|codegen|compile|compile-linux|buildtime|http|kv|ir|render [flags] [rows]")
		return 2
	}
	if err := machinelock.CheckEnv(); err != nil {
		fmt.Fprintf(stderr, "bench: %v\n", err)
		return 2
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("bench "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	work := fs.String("work", defaultWork(), "work directory for builds, caches and raw results: $MK_BENCH_WORK, else bench in $"+machinelock.WorkEnv+" or in mk-work of the user's cache directory")
	mk := fs.String("mk", "", "mk executable to measure (default: build ./cmd/mk of the tree)")
	table := fs.String("table", "", "only rows of this table")
	fs.StringVar(&mkStd, "mk-std", "", "MK_STD for every Mk build (default: the std embedded in the mk measured); recorded with each build command")
	var (
		lockPath = machinelock.DefaultPath()
		mask     = "0x4"
		sweeps   = 3
		warmup   = 3
		runs     = 10
		date     = time.Now().Format("2006-01-02")
		base     string
		head     string
		pairs    = 15
		cols     = "mk,mk-wrap"
		quick    bool
		pubSize  bool
		merge    bool
		resume   bool
		budgets  bool
		pgoTable bool
		// codegen
		cgPublish bool
		// compile
		spec                = genSpec{Modules: 50, Funcs: 40, Structs: 8}
		cleanRuns, editRuns = 3, 10
		warmRuns, checkRuns = 5, 10
		real                bool
		programs            = "kvd,lox,orderbook,pack"
		vs                  string
		headLabel           = "head"
		btRuns              = 3
		langs               = "c,rust,zig,go,mk"
		procs               = 3
		linkOut             string
		irFuncs, irCFuncs   string
		// the app table's server rows (serverbench.go)
		srvCheck bool
		srvRow   string
		// editloop and framepointers
		elWS, elSynth, elModes, elBy, elJSON string
		elLines, elRuns, elJobs              = 1000000, 3, 0
		fpCount                              = "time"
		// publish inside WSL, and publish-linux
		platform, prebuilt, only, mkLabel, mkSHA, mkVersion string
		noLock, treeOnly                                    bool
		distroWork                                          string
		// render -doc (docbench.go)
		docOut    bool
		docBefore string
	)
	switch sub {
	case "publish":
		fs.StringVar(&lockPath, "lock", lockPath, "machine lock file (tools/machinelock)")
		fs.StringVar(&mask, "affinity", mask, "CPU affinity mask (one logical CPU, its SMT sibling idle)")
		fs.IntVar(&sweeps, "sweeps", sweeps, "sweeps in rotated column order")
		fs.IntVar(&warmup, "warmup", warmup, "hyperfine warm-up runs per cell and sweep")
		fs.IntVar(&runs, "runs", runs, "hyperfine runs per cell and sweep (at least 10)")
		fs.StringVar(&date, "date", date, "results directory name")
		fs.BoolVar(&merge, "merge", true, "keep the other rows of an earlier publish into the same date")
		fs.BoolVar(&resume, "resume", false, "reuse a dead run's raw hyperfine exports of this date: a sweep or rerun file is reused only when it names the same program and arguments, has the run count, and is newer than the program (resumeRaw)")
		// Set by publish-linux for the run inside WSL (linux.go).
		fs.StringVar(&platform, "platform", "", "linux: write results/<date>/linux (the run inside WSL)")
		fs.BoolVar(&noLock, "no-lock", false, "the machine lock is held by the caller (publish-linux)")
		fs.StringVar(&prebuilt, "prebuilt", "", "directory of Mk programs cross-built elsewhere")
		fs.StringVar(&only, "cols", "", "only these columns, comma-separated")
		fs.StringVar(&mkLabel, "mk-label", "", "the mk that built -prebuilt")
		fs.StringVar(&mkSHA, "mk-sha", "", "its SHA-256")
		fs.StringVar(&mkVersion, "mk-version", "", "the commit it was built from and its LLVM (mkIdentity)")
		fs.BoolVar(&budgets, "budgets", false, "the check-budget table: the same rows' Mk columns with and without bounds and overflow checks, into results/<date>/budgets (budgets.go)")
		fs.BoolVar(&pgoTable, "pgo", false, "the labelled PGO table: the same rows' Mk column built with --pgo=off and with a profile trained on the row's quick input, into results/<date>/pgo (pgo.go)")
		fs.BoolVar(&treeOnly, "tree", false, "tree rows under tests/bench only (leave out tests/bench-lanes)")
	case "publish-linux":
		fs.StringVar(&lockPath, "lock", lockPath, "machine lock file (tools/machinelock)")
		fs.StringVar(&date, "date", date, "results directory name")
		fs.StringVar(&distroWork, "linux-work", "/tmp/mk-bench", "work directory inside WSL (a Linux file system, not /mnt)")
	case "compare":
		fs.StringVar(&base, "base", "", "the old mk executable")
		fs.StringVar(&head, "head", "", "the new mk executable")
		fs.IntVar(&pairs, "pairs", pairs, "A/B pairs per cell")
		fs.StringVar(&cols, "cols", cols, "Mk columns to compare")
		fs.BoolVar(&quick, "quick", false, "use the rows' quick arguments")
	case "codegen":
		fs.BoolVar(&cgPublish, "publish", false, "publish: hold the machine lock, run the publish size -procs times, write results/<date>/codegen")
		fs.BoolVar(&quick, "quick", false, "without -publish: run the quick size once (default: the publish size once)")
		fs.IntVar(&procs, "procs", procs, "processes per kernel when publishing, each running the kernel's rounds")
		fs.StringVar(&date, "date", date, "results directory name")
		fs.StringVar(&lockPath, "lock", lockPath, "machine lock file (tools/machinelock)")
		fs.StringVar(&mask, "affinity", mask, "CPU affinity mask")
	case "compile":
		fs.BoolVar(&cgPublish, "publish", false, "publish: hold the machine lock, pin 8 CPUs, write results/<date>/compile")
		fs.BoolVar(&quick, "quick", false, "a small program (6 modules of 4 functions), one clean and one edit build")
		fs.IntVar(&spec.Modules, "modules", spec.Modules, "modules of the generated program")
		fs.IntVar(&spec.Funcs, "funcs", spec.Funcs, "functions per module")
		fs.IntVar(&spec.Structs, "structs", spec.Structs, "structs per module")
		fs.IntVar(&cleanRuns, "clean-runs", cleanRuns, "clean builds per cell")
		fs.IntVar(&editRuns, "edit-runs", editRuns, "timed one-function-edit builds per cell")
		fs.StringVar(&langs, "langs", langs, "languages, comma-separated")
		fs.StringVar(&date, "date", date, "results directory name")
		fs.StringVar(&lockPath, "lock", lockPath, "machine lock file (tools/machinelock)")
		fs.StringVar(&mask, "affinity", compileMask, "CPU affinity mask: 8 CPUs for 8 jobs")
		fs.IntVar(&warmRuns, "warm-runs", warmRuns, "timed no-change builds per cell (0: none)")
		fs.IntVar(&checkRuns, "check-runs", checkRuns, "timed mk check runs per check row (0: none)")
		fs.BoolVar(&real, "real", false, "the real-program table: dogfoods, Mk only, across mk binaries (compile_real.go)")
		fs.StringVar(&programs, "programs", programs, "with -real: programs, comma-separated")
		fs.StringVar(&vs, "vs", "", "with -real: older mk binaries to compare, label=path, comma-separated")
		fs.StringVar(&headLabel, "head-label", headLabel, "with -real: the label of the mk measured (-mk)")
		// Set by compile-linux for the run inside WSL (compile_wsl.go).
		fs.StringVar(&platform, "platform", "", "linux: the run inside WSL, Mk only, results/<date>/compile-linux")
		fs.BoolVar(&noLock, "no-lock", false, "the machine lock is held by the caller (compile-linux)")
		fs.StringVar(&mkLabel, "mk-label", "", "the mk's name in the facts")
	case "compile-linux":
		fs.StringVar(&lockPath, "lock", lockPath, "machine lock file (tools/machinelock)")
		fs.StringVar(&date, "date", date, "results directory name")
		fs.StringVar(&distroWork, "linux-work", "/tmp/mk-bench-compile", "work directory inside WSL (a Linux file system, not /mnt)")
		fs.IntVar(&spec.Modules, "modules", spec.Modules, "modules of the generated program")
		fs.IntVar(&spec.Funcs, "funcs", spec.Funcs, "functions per module")
		fs.IntVar(&spec.Structs, "structs", spec.Structs, "structs per module")
		fs.IntVar(&cleanRuns, "clean-runs", cleanRuns, "clean builds per cell")
		fs.IntVar(&editRuns, "edit-runs", editRuns, "timed one-function-edit builds per cell")
		fs.IntVar(&warmRuns, "warm-runs", warmRuns, "timed no-change builds per cell (0: none)")
		fs.IntVar(&checkRuns, "check-runs", checkRuns, "timed mk check runs per check row (0: none)")
	case "buildtime":
		fs.BoolVar(&cgPublish, "publish", false, "publish: hold the machine lock, pin 8 CPUs, write results/<date>/buildtime")
		fs.IntVar(&btRuns, "runs", btRuns, "builds per cell, each from empty caches")
		fs.StringVar(&date, "date", date, "results directory name")
		fs.StringVar(&lockPath, "lock", lockPath, "machine lock file (tools/machinelock)")
		fs.StringVar(&mask, "affinity", compileMask, "CPU affinity mask: 8 CPUs, as the compile table")
	case "http", "kv":
		fs.BoolVar(&cgPublish, "publish", false, "publish: hold the machine lock, pin server and generator, write results/<date>/<row>")
		fs.BoolVar(&quick, "full", false, "the idiomatic http row without -publish: the spec's connection count, unpinned, a 5 s window")
		fs.BoolVar(&srvCheck, "check", false, "an app server row: check_requests per connection, every reply checked, nothing timed")
		fs.StringVar(&srvRow, "row", "http", "http only: the row, http (idiomatic hello) or httpapi (the app table's JSON API)")
		fs.StringVar(&date, "date", date, "results directory name")
		fs.StringVar(&lockPath, "lock", lockPath, "machine lock file (tools/machinelock)")
	case "ir":
		fs.StringVar(&irFuncs, "func", "", "hot functions to disassemble, comma-separated: an exact symbol, a last name segment (Build for mk.main.Build), or a substring")
		fs.StringVar(&irCFuncs, "cfunc", "", "the C executable's functions when their names differ (default: -func)")
	case "editloop":
		fs.StringVar(&elWS, "ws", "", "the synthetic workspace (generated by tools/synth when it holds no synth.json)")
		fs.IntVar(&elLines, "lines", elLines, "lines of a workspace to generate")
		fs.StringVar(&elSynth, "synth", "", "a tools/synth executable (default: built into -work)")
		fs.StringVar(&vs, "vs", "", "other mk binaries to compare, label=path, comma-separated")
		fs.StringVar(&headLabel, "head-label", headLabel, "the label of the mk measured (-mk)")
		fs.StringVar(&elModes, "modes", "check,build", "check, build or both")
		fs.IntVar(&elRuns, "runs", elRuns, "rounds; each runs every mk and mode's sequence once, from a fresh cache")
		fs.IntVar(&elJobs, "jobs", elJobs, "mk's -j (0: mk's default)")
		fs.StringVar(&elBy, "by", "", "who measures (required)")
		fs.StringVar(&elJSON, "json", "", "also write the raw runs here")
	case "framepointers":
		fs.IntVar(&pairs, "pairs", pairs, "off/on pairs per row")
		fs.BoolVar(&quick, "quick", false, "use the rows' quick arguments")
		fs.StringVar(&fpCount, "count", fpCount, "time (wall clock) or instructions (Linux perf stat)")
		fs.StringVar(&platform, "target", "", "cross-build both programs of every row for this target (x86_64-linux) and run nothing")
		fs.StringVar(&prebuilt, "prebuilt", "", "copy the programs from this directory (a -target build) instead of building them")
		fs.StringVar(&mkLabel, "mk-label", "", "with -prebuilt: the mk that cross-built the programs (its path, commit or sha256), for the report")
		fs.StringVar(&elBy, "by", "", "who measures, for the report's label")
	case "link":
		fs.StringVar(&linkOut, "o", "", "copy the program here (default: printed, under the work directory)")
	case "render":
		fs.StringVar(&date, "date", date, "results directory to render")
		fs.BoolVar(&docOut, "doc", false, "write doc/benchmarks.md comparing -before with -date (docbench.go) instead of RESULTS.md")
		fs.StringVar(&docBefore, "before", "", "with -doc: the results directory of the before run")
	case "verify":
		fs.BoolVar(&pubSize, "publish-size", false, "run the publish arguments instead of the quick ones")
		fs.BoolVar(&budgets, "budgets", false, "build and check the check-budget columns too (mk-nobounds, mk-noovf)")
		fs.BoolVar(&pgoTable, "pgo", false, "the PGO table's columns only: mk, then a training run at the quick arguments and mk-pgo, for the same rows with an mk source (pgo.go)")
		fs.BoolVar(&treeOnly, "tree", false, "tree rows under tests/bench only (leave out tests/bench-lanes)")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	// The builds run in the row's directory and the shim in mk's own work
	// directory, so a relative -work would scatter their files. This holds
	// for every subcommand; Abs leaves the default and any absolute
	// backslashed path as it is, so published commands do not change.
	absWork, err := filepath.Abs(*work)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	*work = absWork
	// The lock is the one cross-tool file: an empty -lock means the
	// default, as in tools/gate, and a relative one is made absolute, as
	// -work is.
	if lockPath == "" {
		lockPath = machinelock.DefaultPath()
	}
	if lockPath, err = filepath.Abs(lockPath); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if mkStd != "" {
		a, err := filepath.Abs(mkStd)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		mkStd = a
	}
	log := func(s string) { fmt.Fprintf(stderr, "[%s] %s\n", time.Now().Format("15:04:05"), s) }
	wd, _ := os.Getwd()
	root, err := repoRoot(wd)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if s := strayProfiles(root); len(s) > 0 {
		fmt.Fprintf(stderr, "refusing to run: a PGO profile under the benchmark trees would switch PGO on for one column: %s\n", strings.Join(s, ", "))
		return 1
	}
	if err := os.MkdirAll(*work, 0o755); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	needMk := func() (string, error) {
		if *mk != "" {
			return filepath.Abs(*mk)
		}
		return buildMk(root, *work)
	}
	fail := func(err error) int {
		fmt.Fprintln(stderr, "bench:", err)
		return 1
	}
	switch sub {
	case "list":
		rows, err := discover(root)
		if err != nil {
			return fail(err)
		}
		for _, r := range rows {
			if r.Broken != "" {
				fmt.Fprintf(stdout, "%-8s %-28s %-6s BROKEN, left out of every run: %s\n", orDash(r.Table), r.id(), "-", r.Broken)
				continue
			}
			var ids []string
			for _, c := range rowColumns(r) {
				ids = append(ids, c.ID)
			}
			fmt.Fprintf(stdout, "%-8s %-28s %-6s %s\n", r.Table, r.id(), r.Overflow, strings.Join(ids, " "))
		}
		ks, err := loadKernels(root, nil)
		if err != nil {
			return fail(err)
		}
		for _, k := range ks {
			fmt.Fprintf(stdout, "%-8s %-28s %-6s %s\n", "codegen", k.Name, "-", "mk c (in process: tools/bench codegen)")
		}
		return 0
	case "gen":
		rows, err := discover(root)
		if err == nil {
			rows, err = selectRows(rows, *table, fs.Args())
		}
		if err != nil {
			return fail(err)
		}
		for _, r := range rows {
			if r.Data == nil {
				continue
			}
			p, err := genData(dataDir(root), *r.Data)
			if err != nil {
				return fail(err)
			}
			fmt.Fprintln(stdout, p)
		}
		return 0
	case "verify":
		m, err := needMk()
		if err != nil {
			return fail(err)
		}
		rows, err := discover(root)
		if err == nil {
			logBroken(rows, log)
			if treeOnly {
				rows = slices.DeleteFunc(rows, func(r *row) bool { return r.Lane != "" })
			}
			rows, err = selectRows(rows, *table, fs.Args())
		}
		if err != nil {
			return fail(err)
		}
		if pgoTable {
			if budgets {
				return fail(errors.New("-pgo and -budgets are two tables: name one"))
			}
			if rows = pgoRows(rows); len(rows) == 0 {
				return fail(errors.New("-pgo: no same-algorithm row with an mk source is selected"))
			}
		}
		if err := prepareRows(root, rows); err != nil {
			return fail(err)
		}
		t, _ := findToolchain(*work, m)
		bad := 0
		for _, r := range rows {
			var sel func(column) bool
			if budgets {
				sel = func(column) bool { return true }
			}
			var bs []*built
			if pgoTable {
				bs = buildPGO(t, "verify", []*row{r}, log)
			} else {
				bs = buildAll(t, "verify", []*row{r}, sel, log)
			}
			a, want := r.runQuick, r.QuickSum
			if pubSize {
				a, want = r.runArgs, r.Checksum
			}
			res, agreed, ok := verify(bs, a, want)
			status := "ok"
			if !ok {
				status = "FAIL"
				bad++
			}
			fmt.Fprintf(stdout, "%s %s args %q checksum %s\n", status, r.id(), a, agreed)
			if s := crossLine(res); s != "" {
				fmt.Fprintf(stdout, "    %s\n", s)
			}
			for _, cr := range res {
				note := ""
				switch {
				case cr.Err != nil:
					note = "error: " + firstLines(cr.Err.Error(), 6)
				case cr.Wrong:
					note = "WRONG checksum " + cr.Sum
				}
				fmt.Fprintf(stdout, "    %-11s %9s %s\n", cr.B.Col.ID, fmtSec(cr.Time.Seconds()), note)
			}
			if len(r.Counts) == 0 {
				continue
			}
			// The counting builds must agree before the row is timed.
			cres, cagreed, cok := checkCounts(r, buildCounting(t, []*row{r}, log))
			status = "ok"
			if !cok {
				status = "FAIL"
				bad++
			}
			fmt.Fprintf(stdout, "%s %s counts (quick size) %s\n", status, r.id(), cagreed)
			for _, cr := range cres {
				note := ""
				switch {
				case cr.Err != nil:
					note = "error: " + firstLines(cr.Err.Error(), 6)
				case cr.Wrong:
					note = "DIFFERENT " + cr.Counts
				}
				fmt.Fprintf(stdout, "    %-11s %s\n", cr.B.Col.ID, note)
			}
		}
		if bad > 0 {
			return 1
		}
		return 0
	case "publish-linux":
		m, err := needMk()
		if err != nil {
			return fail(err)
		}
		err = publishLinux(linuxOptions{Root: root, Work: *work, Mk: m, Rows: fs.Args(), Table: *table, LockPath: lockPath,
			Date: date, LinuxWork: distroWork, Log: log})
		if err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, "wrote tests/bench/RESULTS.md and tests/bench/results/"+date+"/linux")
		return 0
	case "publish":
		m := ""
		if prebuilt == "" {
			m, err = needMk()
			if err != nil {
				return fail(err)
			}
		}
		var mv uint64
		if platform == "" {
			mv, err = strconv.ParseUint(strings.TrimPrefix(mask, "0x"), 16, 64)
			if err != nil || mv == 0 {
				return fail(fmt.Errorf("bad -affinity %q", mask))
			}
		}
		if runs < 10 {
			return fail(errors.New("-runs below 10 breaks the contract (tests/bench/README.md)"))
		}
		var onlyCols []string
		if only != "" {
			onlyCols = strings.Split(only, ",")
		}
		if platform != "" {
			// Data files on the Linux file system: reading them from /mnt
			// (9P) would add its cost to every run.
			dataRoot = filepath.Join(*work, "data")
		}
		err = publish(publishOptions{Root: root, Work: *work, Mk: m, Rows: fs.Args(), Table: *table, LockPath: lockPath,
			Mask: mv, Sweeps: sweeps, HF: hfOptions{Warmup: warmup, Runs: runs}, Date: date, Merge: merge, Resume: resume, Log: log,
			Platform: platform, NoLock: noLock, Prebuilt: prebuilt, Only: onlyCols, MkLabel: mkLabel, MkSHA: mkSHA, MkVersion: mkVersion, Budgets: budgets, PGO: pgoTable, Tree: treeOnly})
		if err != nil {
			return fail(err)
		}
		if budgets {
			date += "/budgets"
		}
		if pgoTable {
			date += "/pgo"
		}
		fmt.Fprintln(stdout, "wrote tests/bench/RESULTS.md and tests/bench/results/"+date)
		return 0
	case "codegen":
		m, err := needMk()
		if err != nil {
			return fail(err)
		}
		mv, err := strconv.ParseUint(strings.TrimPrefix(mask, "0x"), 16, 64)
		if err != nil || mv == 0 {
			return fail(fmt.Errorf("bad -affinity %q", mask))
		}
		if procs < 1 {
			return fail(errors.New("-procs must be at least 1"))
		}
		err = codegen(codegenOptions{Root: root, Work: *work, Mk: m, Names: fs.Args(), Publish: cgPublish, Quick: quick,
			Procs: procs, LockPath: lockPath, Mask: mv, Date: date, Log: log}, func(s string) { fmt.Fprintln(stdout, s) })
		if err != nil {
			return fail(err)
		}
		if cgPublish {
			fmt.Fprintln(stdout, "wrote tests/bench/RESULTS.md and tests/bench/results/"+date+"/codegen")
		}
		return 0
	case "compile":
		m, err := needMk()
		if err != nil {
			return fail(err)
		}
		mv, err := strconv.ParseUint(strings.TrimPrefix(mask, "0x"), 16, 64)
		if err != nil || mv == 0 {
			return fail(fmt.Errorf("bad -affinity %q", mask))
		}
		if quick {
			spec, cleanRuns, editRuns, warmRuns, checkRuns = genSpec{Modules: 6, Funcs: 4, Structs: 2}, 1, 1, 1, 1
		}
		if cleanRuns < 1 || editRuns < 1 || warmRuns < 0 || checkRuns < 0 {
			return fail(errors.New("-clean-runs and -edit-runs must be at least 1, -warm-runs and -check-runs at least 0"))
		}
		if real {
			others, err := parseVs(vs)
			if err != nil {
				return fail(err)
			}
			mks := append([]realMk{{Label: headLabel, Path: m}}, others...)
			err = compileReal(realOptions{Root: root, Work: *work, Mks: mks, Programs: strings.Split(programs, ","), Publish: cgPublish,
				CleanRuns: cleanRuns, EditRuns: editRuns, WarmRuns: warmRuns, CheckRuns: checkRuns, LockPath: lockPath, Mask: mv, Date: date, Log: log},
				func(s string) { fmt.Fprintln(stdout, s) })
			if err != nil {
				return fail(err)
			}
			if cgPublish {
				fmt.Fprintln(stdout, "wrote tests/bench/results/"+date+"/compile-real")
			}
			return 0
		}
		if platform == "linux" {
			if m, err = linuxCompileMk(*work, m); err != nil {
				return fail(err)
			}
		}
		err = compileTable(compileOptions{Root: root, Work: *work, Mk: m, Spec: spec, Langs: strings.Split(langs, ","),
			Publish: cgPublish, CleanRuns: cleanRuns, EditRun: editRuns, WarmRuns: warmRuns, CheckRuns: checkRuns,
			LockPath: lockPath, Mask: mv, Date: date, Log: log, Platform: platform, NoLock: noLock, MkLabel: mkLabel},
			func(s string) { fmt.Fprintln(stdout, s) })
		if err != nil {
			return fail(err)
		}
		if cgPublish {
			fmt.Fprintln(stdout, "wrote tests/bench/results/"+date+"/"+compileDir(platform))
		}
		return 0
	case "compile-linux":
		if *mk == "" {
			return fail(errors.New("compile-linux needs -mk <a Linux mk>, built with GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/mk"))
		}
		m, err := filepath.Abs(*mk)
		if err != nil {
			return fail(err)
		}
		err = compileLinux(compileLinuxOptions{Root: root, Work: *work, Mk: m, LinuxWork: distroWork, Spec: spec,
			CleanRuns: cleanRuns, EditRuns: editRuns, WarmRuns: warmRuns, CheckRuns: checkRuns, LockPath: lockPath, Date: date, Log: log})
		if err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, "wrote tests/bench/results/"+date+"/compile-linux")
		return 0
	case "buildtime":
		m, err := needMk()
		if err != nil {
			return fail(err)
		}
		mv, err := strconv.ParseUint(strings.TrimPrefix(mask, "0x"), 16, 64)
		if err != nil || mv == 0 {
			return fail(fmt.Errorf("bad -affinity %q", mask))
		}
		err = buildTimeTable(buildTimeOptions{Root: root, Work: *work, Mk: m, Table: *table, Rows: fs.Args(), Runs: btRuns,
			Publish: cgPublish, LockPath: lockPath, Mask: mv, Date: date, Log: log}, func(s string) { fmt.Fprintln(stdout, s) })
		if err != nil {
			return fail(err)
		}
		if cgPublish {
			fmt.Fprintln(stdout, "wrote tests/bench/results/"+date+"/buildtime")
		}
		return 0
	case "kv", "http":
		m, err := needMk()
		if err != nil {
			return fail(err)
		}
		if sub == "kv" || srvRow != "http" {
			name := "kvserve"
			if sub == "http" {
				name = srvRow
				if name != "httpapi" {
					fmt.Fprintf(stderr, "bench http: -row %s: the rows are http and httpapi\n", srvRow)
					return 2
				}
			} else if srvRow != "http" {
				fmt.Fprintln(stderr, "bench kv: -row belongs to bench http")
				return 2
			}
			if quick {
				fmt.Fprintln(stderr, "bench "+sub+": -full belongs to the idiomatic http row")
				return 2
			}
			err = serverTable(serverOptions{Root: root, Work: *work, Mk: m, Name: name, Check: srvCheck, Publish: cgPublish, LockPath: lockPath, Date: date, Log: log},
				func(s string) { fmt.Fprintln(stdout, s) })
			if err != nil {
				return fail(err)
			}
			return 0
		}
		if srvCheck {
			fmt.Fprintln(stderr, "bench http: -check belongs to the app server rows (-row httpapi)")
			return 2
		}
		err = httpTable(httpOptions{Root: root, Work: *work, Mk: m, Publish: cgPublish, Full: quick, LockPath: lockPath, Date: date, Log: log},
			func(s string) { fmt.Fprintln(stdout, s) })
		if err != nil {
			return fail(err)
		}
		return 0
	case "link":
		// One Mk package with one freestanding C file linked in, as the
		// codegen kernels are built (README "The codegen table"); for
		// tests/bench/ccall and anyone who needs such a program.
		if len(fs.Args()) != 2 {
			fmt.Fprintln(stderr, "usage: bench link [-mk mk.exe] [-o out.exe] <package dir> <file.c>")
			return 2
		}
		m, err := needMk()
		if err != nil {
			return fail(err)
		}
		pkg, err1 := filepath.Abs(fs.Arg(0))
		cf, err2 := filepath.Abs(fs.Arg(1))
		if err := errors.Join(err1, err2); err != nil {
			return fail(err)
		}
		t, _ := findToolchain(*work, m)
		name := filepath.Base(pkg)
		exe, cmds, err := buildWithC(t, filepath.Join(*work, "link", name), name, pkg, cf)
		for _, c := range cmds {
			fmt.Fprintln(stderr, strings.Join(c, " "))
		}
		if err != nil {
			return fail(err)
		}
		if linkOut != "" {
			b, err := os.ReadFile(exe)
			if err == nil {
				err = os.WriteFile(linkOut, b, 0o755)
			}
			if err != nil {
				return fail(err)
			}
			exe = linkOut
		}
		fmt.Fprintln(stdout, exe)
		return 0
	case "ir":
		if len(fs.Args()) != 1 {
			fmt.Fprintln(stderr, "usage: bench ir [-mk mk.exe] [-work dir] [-func F,...] [-cfunc G,...] <row>")
			return 2
		}
		m, err := needMk()
		if err != nil {
			return fail(err)
		}
		split := func(s string) []string {
			var out []string
			for _, f := range strings.Split(s, ",") {
				if f = strings.TrimSpace(f); f != "" {
					out = append(out, f)
				}
			}
			return out
		}
		idx, err := irView(irOptions{Root: root, Work: *work, Mk: m, Row: fs.Arg(0), MkFuncs: split(irFuncs), CFuncs: split(irCFuncs), Log: log})
		if err != nil {
			return fail(err)
		}
		fmt.Fprint(stdout, idx)
		return 0
	case "render":
		if docOut {
			if docBefore == "" {
				return fail(errors.New("render -doc needs -before"))
			}
			if err := writeBenchDoc(root, docBefore, date); err != nil {
				return fail(err)
			}
			fmt.Fprintln(stdout, "wrote doc/benchmarks.md from tests/bench/results/"+docBefore+" and tests/bench/results/"+date)
			return 0
		}
		if err := writeResultsMD(root, date); err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, "wrote tests/bench/RESULTS.md from tests/bench/results/"+date)
		return 0
	case "editloop":
		if *mk == "" || elWS == "" {
			fmt.Fprintln(stderr, "usage: bench editloop -mk <mk> [-vs label=<mk>,...] -ws DIR -by NAME [-lines N] [-runs N] [-modes check,build]")
			return 2
		}
		m, err := filepath.Abs(*mk)
		if err != nil {
			return fail(err)
		}
		ws, err := filepath.Abs(elWS)
		if err != nil {
			return fail(err)
		}
		others, err := parseVs(vs)
		if err != nil {
			return fail(err)
		}
		res, err := editLoop(editOptions{Root: root, Work: *work, WS: ws, Lines: elLines, Synth: elSynth,
			Mks: append([]realMk{{Label: headLabel, Path: m}}, others...), Modes: strings.Split(elModes, ","),
			Runs: elRuns, Jobs: elJobs, By: elBy, Log: log}, stdout)
		if res != nil {
			renderEditLoop(stdout, res)
			if elJSON != "" {
				if jerr := writeEditJSON(elJSON, res); jerr != nil && err == nil {
					err = jerr
				}
			}
		}
		if err != nil {
			return fail(err)
		}
		return 0
	case "framepointers":
		m := ""
		if prebuilt != "" && mkLabel == "" {
			return fail(errors.New("framepointers -prebuilt needs -mk-label: the mk that cross-built the programs"))
		}
		if prebuilt == "" {
			var err error
			if m, err = needMk(); err != nil {
				return fail(err)
			}
		}
		cs, err := framePointers(fpOptions{Root: root, Work: *work, Mk: m, Rows: fs.Args(), Table: cmp.Or(*table, "same"), Pairs: pairs, Quick: quick,
			Count: fpCount, Target: platform, Prebuilt: prebuilt, Log: log})
		if len(cs) > 0 {
			renderFP(stdout, cs, m, mkLabel, fpCount, elBy)
		}
		if err != nil {
			return fail(err)
		}
		return 0
	case "compare":
		if base == "" || head == "" || len(fs.Args()) == 0 {
			fmt.Fprintln(stderr, "usage: bench compare --base <old mk> --head <new mk> <rows>")
			return 2
		}
		ab, err1 := filepath.Abs(base)
		ah, err2 := filepath.Abs(head)
		if err := errors.Join(err1, err2); err != nil {
			return fail(err)
		}
		cs, err := compare(compareOptions{Root: root, Work: *work, Base: ab, Head: ah, Rows: fs.Args(), Pairs: pairs, Cols: cols, Quick: quick, Log: log, Out: stdout})
		if err != nil {
			return fail(err)
		}
		renderCompare(stdout, cs, ab, ah)
		return 0
	}
	fmt.Fprintf(stderr, "bench: unknown subcommand %q\n", sub)
	return 2
}
