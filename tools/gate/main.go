// Why: a change is ready only when it builds, vets, passes its tests (and the
// Linux, timing and fuzz runs) and the corpus; one runner does all of it in
// a fixed order so the result is one report, not a memory of commands.
//
// What: the gate command, its flags, the Step and Report types it writes as
// JSON, the package lists (linuxPkgs, timingRuns) and the run loop. The steps
// live in steps.go, fuzz.go, dogfood.go, safety.go, apidiff.go and zdec.go.
//
// How: the steps run strictly one after another, each as a child process,
// with go test -json parsed (parse.go) into per-test results that later steps
// judge. A tripwire wsl.exe on PATH makes a stray WSL start fail loudly, and
// the machine lock keeps two measurements off the machine at once.

// Command gate runs the repository's verification gate in one fixed order
// and writes one JSON report, so the result can be read instead of running
// the tests again:
//
//	go run ./tools/gate [-tier quick|full|soak] [-since <rev>|last-fuzz] [-fuzztime 5m]
//	    [-skip build,test,linux,corpus,dogfood,timing,fuzz] [-only pkgs] [-json out.json]
//	    [-stdtimeout 4h] [-work W] [-lock F]
//	go run ./tools/gate lock -- <cmd> [args]
//
// The steps run strictly one after another, never two at once:
//
//	build    go build and go vet for the host and for GOOS=linux, gofmt -l
//	test     go test with MK_GATE=1 and MK_LINUX and MK_TIMING unset, at the
//	         normal parallelism; a tripwire wsl.exe is first on PATH, so a
//	         leaf that starts WSL here fails loudly
//	linux    the tests that skipped in the test step with "MK_LINUX=1 not
//	         set", again with MK_GATE=1 MK_LINUX=1, under tools/wsl-lock.ps1
//	         with go test -p 1 (parallel WSL clients hang WSL)
//	corpus   go run ./cmd/mk corpus, and corpus --all --strict; then
//	         mk fmt -l over every mk block under doc/ (designfmt.go)
//	dogfood  go run ./tools/dogfood run (quick: -changed, the dogfoods
//	         whose files, std modules or mk changed since their last pass
//	         in a whole-folder run;
//	         full and soak: all, then cover, on Windows); the Linux runs
//	         in a second process without the tripwire, the harness taking
//	         the WSL lock itself
//	timing   the tests whose wall-clock assertions run only under
//	         MK_TIMING=1 (timingRuns), -count=1 -p 1; their Linux leaves
//	         under the lock
//	fuzz     every Fuzz target (quick: only those whose package, or a
//	         package it imports in this module, changed since -since), each
//	         alone for -fuzztime; then the untrusted-input list checked
//	         against what ran (safety.go)
//
// The test and Linux steps run ./std in a go test of its own, after the
// other packages, with -timeout from -stdtimeout (default 4h); the other
// packages, and the timing and math runs, keep -timeout 3600s.
// Without -only the test step names the other packages from go list -e
// ./.... A package that ends before its started tests have a result (a
// timeout, a crash) is one failure naming the package and why, and the
// cancel-safety step reports its listed tests of that package as not run
// because of it.
//
// After linux, the cancel-safety tests are checked against the test
// and Linux results, and in the full and soak tiers the math checksums
// run on both targets (safety.go).
//
// The quick tier keeps go test's cache (no -count=1), so a package whose
// inputs did not change is not run again; the full and soak tiers pass
// -count=1 and fuzz every target (10 min and 1 h each). -only limits the
// test, linux, timing and fuzz steps to the named packages. The exit
// status is 0 only when every step that ran passed.
//
// -work defaults to the shared work root: $MK_WORK, else mk-work in the
// user's cache directory (CONTRIBUTING.md). The machine lock is -lock,
// by default machine.lock in that root whatever -work is
// (tools/machinelock). The timing and fuzz steps hold it, a full or soak
// run holds it throughout, and `gate lock -- <cmd>` holds the default one
// while cmd runs (a separate fuzz or benchmark run), so no two
// measurements share the machine.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"mk/tools/machinelock"
)

// Report is the JSON file the gate writes.
type Report struct {
	Tier        string    `json:"tier"`
	Since       string    `json:"since"`
	Root        string    `json:"root"`
	Host        string    `json:"host"`
	Started     time.Time `json:"started"`
	WallSeconds float64   `json:"wall_seconds"`
	OK          bool      `json:"ok"`
	Steps       []*Step   `json:"steps"`
	// PackageDigests (each package's files) and FuzzInputs (each fuzz
	// package's files and its module imports') are recorded by every run
	// with a fuzz step; fuzz-state.json keeps FuzzInputs of the packages
	// whose targets passed, for -since last-fuzz.
	PackageDigests map[string]string `json:"package_digests,omitempty"`
	FuzzInputs     map[string]string `json:"fuzz_inputs,omitempty"`
}

// Step is one command of the gate and what it gave; a step of several
// commands (the test and Linux steps) keeps every command, every log and
// the first non-zero exit status. Failed is "<package> <test>", or
// "<package> [why]" for a package failure: [build failed], [package
// failed], or one entry for a package that ended mid-run ("./std [timed
// out after 4h0m0s; running TestX (17s); 257 started tests without a
// result]").
type Step struct {
	Name        string        `json:"name"`
	Command     string        `json:"command,omitempty"` // the commands, joined by " ; "
	Env         []string      `json:"env,omitempty"`     // the variables the gate set or removed
	Status      string        `json:"status"`            // pass, fail or skip
	Reason      string        `json:"reason,omitempty"`
	ExitCode    int           `json:"exit_code"`
	WallSeconds float64       `json:"wall_seconds"`
	Log         string        `json:"log,omitempty"`  // the first command's log
	Logs        []string      `json:"logs,omitempty"` // every command's log, when the step ran more than one
	Failed      []string      `json:"failed,omitempty"`
	FailOutput  []string      `json:"fail_output,omitempty"`
	Tests       *TestCounts   `json:"tests,omitempty"`
	Corpus      *CorpusCounts `json:"corpus,omitempty"`
	Fuzz        *FuzzResult   `json:"fuzz,omitempty"`
	Output      []string      `json:"output,omitempty"` // the last lines, for a step that is not go test
	Notes       []string      `json:"notes,omitempty"`

	// fold's state over the step's go test runs: the reasons set before
	// it (preReasons), the reason it wrote, the packages that ended
	// mid-run, and the first non-zero exit status.
	preReasons []string
	foldReason string
	endedWhy   []string
	firstCode  int
}

// linuxStepName is the Linux step's name in the report.
const linuxStepName = "linux (MK_LINUX=1, under the WSL lock)"

// TestCounts sums one go test -json run.
type TestCounts struct {
	Packages       int `json:"packages"`
	PackagesCached int `json:"packages_cached"`
	PackagesFailed int `json:"packages_failed"`
	Pass           int `json:"pass"`
	Fail           int `json:"fail"`
	Skip           int `json:"skip"`
	LinuxSkips     int `json:"linux_skips"`  // skipped with "MK_LINUX=1 not set"
	TimingSkips    int `json:"timing_skips"` // skipped with "MK_TIMING=1 not set"
	// TimingLogged counts tests that passed with a wall-clock assertion
	// left out ("MK_TIMING=1 not set" in their log).
	TimingLogged int      `json:"timing_logged"`
	GuardHits    []string `json:"guard_hits,omitempty"`
	TripwireHits []string `json:"tripwire_hits,omitempty"`
}

// CorpusCounts is the totals line of mk corpus.
type CorpusCounts struct {
	Pass    int `json:"pass"`
	Fail    int `json:"fail"`
	Pending int `json:"pending"`
	Listed  int `json:"listed"`
}

// FuzzResult is one fuzz target's run.
type FuzzResult struct {
	Package  string `json:"package"`
	Target   string `json:"target"`
	FuzzTime string `json:"fuzztime"`
	// Parallel is go test's -parallel for this target when -fuzzjobs is
	// above 1 (0: go test's default, GOMAXPROCS workers).
	Parallel int    `json:"parallel,omitempty"`
	Execs    int64  `json:"execs"`
	Crasher  string `json:"crasher,omitempty"`
}

// linuxPkgs are the packages whose tests may start wsl.exe when MK_LINUX=1 is
// set. cmd/mk has no Linux leaf; it stays listed so that one added there runs
// in the Linux step, under the lock. tools/dogfood's leaf checks its Linux
// scratch directory (in the distribution's own /tmp) on the timeout path.
var linuxPkgs = []string{
	"./internal/abi", "./internal/toolchain", "./internal/llvm", "./internal/codegen",
	"./internal/driver", "./internal/corpus", "./std", "./cmd/mk", "./internal/stdtest",
	"./tools/dogfood",
}

// timingRun is one go test run of the timing step: the tests of pkg that
// -run selects, with MK_TIMING=1. linux runs under the WSL lock with
// MK_LINUX=1; env is extra environment.
type timingRun struct {
	pkg   string
	run   string
	linux bool
	env   []string
}

// timingBlocks is the -run (or RTS_ONLY) alternation of the runtime test
// blocks whose verdict depends on wall-clock time; std/timing_test.go's
// rtsTimingBlocks and internal/driver's stdTimingBlocks are the same list.
const timingBlocks = "RT-Bench-DeepChain|RT-Bench-SleepDepth|RT-Bench-CancelWaiters|RT-DeepChain-Cancel|RT-DeepChain-Fail|RT-DeepChain-Join|" +
	"RT-R17-CancelLevel|RT-R17-CancelNested|RT-R17-FirstErrorWins|RT-Signal-Cancel|RT-Pool-CancelQueued|RT-Io-CancelInFlight|" +
	"RT-F13-PoolCancelNoBleed|RT-F13-PoolCancelEarly|RT-MN-ReapClear|RT-Linger-Ends"

// timingRuns are the tests gated on MK_TIMING=1. Add a row with each new
// gated test.
var timingRuns = []timingRun{
	{pkg: "./internal/borrow", run: "^(TestBudgetNestedScopeSpawns|TestGateFuzzInputBudget|TestBudget64kOneSecond|TestBudgetSeqMutBorrows64k)$"},
	{pkg: "./internal/syntax", run: "^(TestToTypeLinear|TestFileHashLinear|TestMergeLinear)$"},
	{pkg: "./internal/check", run: "^TestCorpusBlocksNoICE$"},
	{pkg: "./std", run: "^(TestFindNativeAndWorstCase|TestSTDSYNC_LockCostIndependentOfDepth|TestSTDSYNC_MutexMaxLatency|" +
		"TestRt_PollerWindows|TestRtIo_WaitEdgesWindows|TestRtIo_ArgEdgesWindows|TestRtIo_PollWakeWindows)$"},
	{pkg: "./std", run: "^TestRtSched_TestBlocks$/^(dev|release)$/^(" + timingBlocks + ")_"},
	{pkg: "./std", run: "^TestRtSched_Stress$/^(" + timingBlocks + ")_"},
	{pkg: "./internal/driver", run: "^TestStdRuntimeTests$/^x86_64-windows$"},
	{pkg: "./std", run: "^(TestRt_PollerLinux|TestRtIo_WaitEdgesLinux|TestRtIo_ArgEdgesLinux|TestRtIo_PollWakeLinux)$", linux: true},
	{pkg: "./std", run: "^TestRtSched_LinuxBlocks$", linux: true, env: []string{"RTS_ONLY=^(" + timingBlocks + ") "}},
	{pkg: "./internal/driver", run: "^TestStdRuntimeTests$/^x86_64-linux$", linux: true},
}

// fuzzUnderLock names the fuzz targets ("<pkg> <target>") that start
// wsl.exe and so run under the WSL lock with MK_LINUX=1. None does: FuzzEmit
// compiles with the host's clang only.
var fuzzUnderLock = map[string]bool{}

// fuzzOneAtATime names the fuzz targets that open loopback sockets for
// their inputs; with -fuzzjobs above 1 no two of them run at once, so the
// machine stays under its 10,000 TCP entries.
var fuzzOneAtATime = map[string]bool{
	"./std FuzzStdHttp_ClientReply": true,
	"./std FuzzStdHttp_ServeWith":   true,
	"./std FuzzStdWs_Frames":        true,
	"./std FuzzStdWs_Reply":         true,
	"./std FuzzStdWs_Request":       true,
	"./std FuzzStdTls_Messages12":   true,
	"./std FuzzStdTls_Records12":    true,
}

// fuzzMinimizeTime caps go test's minimisation of each new interesting
// input (60s by default) for the fuzz targets ("<pkg> <target>") whose every
// execution is a round trip to a driver process: on a cold fuzz cache,
// minimising one input of a few KiB took the rest of a 60-second run at 0
// execs/s, and a 2s cap still spent most of it minimising; 100 runs an input
// keeps the run fuzzing.
var fuzzMinimizeTime = map[string]string{
	"./std FuzzStdHttpFields_Hpack":   "100x",
	"./std FuzzStdHttpFields_Huffman": "100x",
	"./std FuzzStdHttpFields_Qpack":   "100x",
	"./std FuzzStdHttpFields_Check":   "100x",
	"./std FuzzStdHttpH2_Frames":      "100x",
}

// skipNames are the values -skip accepts.
var skipNames = []string{"build", "test", "linux", "corpus", "dogfood", "timing", "fuzz", "apidiff"}

// gate is one run's settings and state.
type gate struct {
	tier     string
	since    string
	fuzzTime time.Duration
	skip     map[string]bool
	only     []string
	jsonOut  string
	work     string
	root     string
	goExe    string
	gofmtExe string
	pwsh     string
	self     string
	tripDir  string
	tripLog  string
	seq      int
	rep      *Report
	// linuxTests maps a package (as "./dir") to the top-level tests that
	// skipped with "MK_LINUX=1 not set" in the test step; a package is a
	// key once the test step reported it.
	linuxTests map[string]map[string]bool
	testRan    bool
	wslDown    string
	lock       *machinelock.Lock
	lockPath   string // -lock, the machine lock file
	// testResults maps "pkg test" (top-level tests) to pass, skip or fail,
	// the Linux pass's outcome replacing the test step's (read by cancelStep).
	testResults map[string]string
	// stdTimeout is -stdtimeout, go test's -timeout for the test and Linux
	// steps' run of ./std alone; ended maps a package that ended mid-run
	// in a step to why and where ("timed out after 4h0m0s in the test
	// step"), for the steps that read testResults.
	stdTimeout time.Duration
	ended      map[string]pkgEnd
	// listErr is the test step's go list failure, which stops the Linux
	// step too; runCmd, when set (the tests), starts every command instead
	// of os/exec: it gets the argv and environment and answers with the
	// output and exit status.
	listErr string
	runCmd  func(argv, env []string, out io.Writer) (int, error)
	// fuzzFound are the fuzz targets the fuzz step listed ("pkg name"),
	// nil when it did not list them; fuzzPassed whether each one it ran
	// passed (read by untrustedStep).
	fuzzFound  []string
	fuzzPassed map[string]bool
	// fuzzJobs is how many fuzz targets run at once (-fuzzjobs); mu guards
	// the report's steps, seq and fuzzPassed while they do.
	fuzzJobs int
	mu       sync.Mutex
}

func main() {
	if base := strings.ToLower(filepath.Base(os.Args[0])); base == "wsl.exe" || base == "wsl" {
		os.Exit(tripwire())
	}
	if len(os.Args) == 2 && os.Args[1] == "-probe-wsl" {
		os.Exit(probeWSL())
	}
	if len(os.Args) >= 2 && os.Args[1] == "lock" {
		os.Exit(lockCmd(os.Args[2:]))
	}
	os.Exit(run())
}

// parseArgs reads the gate's flags from args into a new gate.
func parseArgs(args []string) (*gate, error) {
	g := &gate{skip: map[string]bool{}, linuxTests: map[string]map[string]bool{}, testResults: map[string]string{}, fuzzPassed: map[string]bool{}, ended: map[string]pkgEnd{}}
	fs := flag.NewFlagSet("gate", flag.ContinueOnError)
	fs.StringVar(&g.tier, "tier", "quick", "quick (go test's cache, affected fuzz targets), full (-count=1, every fuzz target) or soak (full, 1 h per target)")
	fs.StringVar(&g.since, "since", "HEAD", "git revision, or last-fuzz; quick-tier fuzzing selects the targets affected by changes since it (uncommitted and untracked files included)")
	fs.DurationVar(&g.fuzzTime, "fuzztime", 0, "time per fuzz target (default 5m quick, 10m full)")
	fs.DurationVar(&g.stdTimeout, "stdtimeout", defaultStdTimeout, "go test -timeout for ./std, which the test and Linux steps run alone (the other packages: 1h); a changed value reruns ./std in the quick tier, since go test's cache key holds it")
	fs.IntVar(&g.fuzzJobs, "fuzzjobs", 1, "fuzz targets run at once, each with go test -parallel NumCPU/fuzzjobs; the WSL-lock targets run alone and the socket targets one at a time")
	skip := fs.String("skip", "", "comma-separated steps to skip: "+strings.Join(skipNames, ","))
	only := fs.String("only", "", "comma-separated packages (./internal/x) for the test, linux, timing and fuzz steps")
	fs.StringVar(&g.work, "work", machinelock.WorkRoot(), "directory for TMP, TEMP, GOTMPDIR, GOCACHE, logs and the tripwire: $"+machinelock.WorkEnv+", else mk-work in the user's cache directory")
	fs.StringVar(&g.lockPath, "lock", machinelock.DefaultPath(), "machine lock file, shared with tools/bench: machine.lock in the work root, whatever -work is")
	fs.StringVar(&g.jsonOut, "json", "", "report file (default <work>/gate-last.json)")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected arguments %q", fs.Args())
	}
	// The steps run in other directories and pass <work>/tmp to their
	// children, so -work and -lock are made absolute here; an empty -lock
	// is the default.
	if g.lockPath == "" {
		g.lockPath = machinelock.DefaultPath()
	}
	var err error
	if g.lockPath, err = filepath.Abs(g.lockPath); err != nil {
		return nil, err
	}
	if g.work != "" {
		if g.work, err = filepath.Abs(g.work); err != nil {
			return nil, err
		}
	}
	if g.tier != "quick" && g.tier != "full" && g.tier != "soak" {
		return nil, fmt.Errorf("-tier %q: want quick, full or soak", g.tier)
	}
	if g.fuzzJobs < 1 {
		g.fuzzJobs = 1
	}
	if g.stdTimeout < time.Second {
		return nil, fmt.Errorf("-stdtimeout %v: want at least 1s", g.stdTimeout)
	}
	if g.fuzzTime == 0 {
		g.fuzzTime = map[string]time.Duration{"quick": 5 * time.Minute, "full": 10 * time.Minute, "soak": time.Hour}[g.tier]
	}
	for _, s := range strings.Split(*skip, ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		if !slices.Contains(skipNames, s) {
			return nil, fmt.Errorf("-skip %q: want some of %s", s, strings.Join(skipNames, ","))
		}
		g.skip[s] = true
	}
	for _, p := range strings.Split(*only, ",") {
		if p = strings.TrimSpace(p); p != "" {
			g.only = append(g.only, normPkg(p))
		}
	}
	return g, nil
}

func run() int {
	if err := machinelock.CheckEnv(); err != nil {
		fmt.Fprintf(os.Stderr, "gate: %v\n", err)
		return 2
	}
	g, err := parseArgs(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "gate: %v\n", err)
		return 2
	}
	if err := g.setup(); err != nil {
		fmt.Fprintf(os.Stderr, "gate: %v\n", err)
		return 2
	}
	g.rep = &Report{Tier: g.tier, Since: g.since, Root: g.root, Host: runtime.GOOS + "/" + runtime.GOARCH, Started: time.Now()}
	fmt.Printf("gate: tier %s, since %s, fuzztime %v, root %s, work %s\n", g.tier, g.since, g.fuzzTime, g.root, g.work)

	if g.tier != "quick" {
		g.holdLock()
	}
	g.buildSteps()
	g.testStep()
	g.linuxStep()
	g.cancelStep()
	g.mathStep()
	g.corpusSteps()
	g.designFmtStep()
	g.apidiffStep()
	g.dogfoodSteps()
	if !g.skip["timing"] || !g.skip["fuzz"] {
		g.holdLock()
	}
	g.timingSteps()
	g.fuzzSteps()
	g.untrustedStep()
	g.lock.Release()

	g.rep.WallSeconds = secs(time.Since(g.rep.Started))
	g.rep.OK = true
	for _, s := range g.rep.Steps {
		if s.Status == "fail" {
			g.rep.OK = false
		}
	}
	g.summary()
	if err := g.writeReport(); err != nil {
		fmt.Fprintf(os.Stderr, "gate: %v\n", err)
		return 2
	}
	if !g.rep.OK {
		return 1
	}
	return 0
}

// holdLock takes the machine lock (-lock), once per run, waiting as long as
// another gate or bench run holds it.
func (g *gate) holdLock() {
	if g.lock != nil {
		return
	}
	path := g.lockPath
	fmt.Printf("gate: taking the machine lock %s\n", path)
	start := time.Now()
	l, err := machinelock.Acquire(path, func(w time.Duration) {
		fmt.Printf("gate: waiting for the machine lock (%v)\n", w.Round(time.Minute))
	})
	if err != nil {
		s := g.newStep("machine lock")
		fail(s, err.Error())
		return
	}
	if w := time.Since(start); w > time.Second {
		fmt.Printf("gate: machine lock taken after %v\n", w.Round(time.Second))
	}
	g.lock = l
}

// lockCmd is `gate lock -- <cmd> [args]`: it holds the machine lock while
// cmd runs, with MK_MACHINE_LOCKED=1, and exits with cmd's status. It
// takes no flags: the lock is always machine.lock in the work root
// (machinelock.DefaultPath), never a gate run's -lock.
func lockCmd(args []string) int {
	if err := machinelock.CheckEnv(); err != nil {
		fmt.Fprintf(os.Stderr, "gate lock: %v\n", err)
		return 2
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: go run ./tools/gate lock -- <cmd> [args]")
		return 2
	}
	path := machinelock.DefaultPath()
	l, err := machinelock.Acquire(path, func(w time.Duration) {
		fmt.Fprintf(os.Stderr, "gate lock: waiting for %s (%v)\n", path, w.Round(time.Minute))
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "gate lock: %v\n", err)
		return 2
	}
	defer l.Release()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), "MK_MACHINE_LOCKED=1", machinelock.WorkEnv+"="+machinelock.WorkRoot())
	err = cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		return ee.ExitCode()
	}
	fmt.Fprintf(os.Stderr, "gate lock: %v\n", err)
	return 2
}

// setup finds the module root, the tools and the work directories.
func (g *gate) setup() error {
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	for d := wd; ; d = filepath.Dir(d) {
		if b, err := os.ReadFile(filepath.Join(d, "go.mod")); err == nil && strings.HasPrefix(string(b), "module mk") {
			g.root = d
			break
		}
		if filepath.Dir(d) == d {
			return fmt.Errorf("no go.mod of module mk above %s", wd)
		}
	}
	if g.goExe, err = exec.LookPath("go"); err != nil {
		g.goExe = filepath.Join(runtime.GOROOT(), "bin", "go")
		if _, serr := os.Stat(g.goExe + exeSuffix()); serr != nil {
			return fmt.Errorf("go not found on PATH or in GOROOT: %v", err)
		}
	}
	g.gofmtExe = filepath.Join(filepath.Dir(g.goExe), "gofmt"+exeSuffix())
	if p, err := exec.LookPath("pwsh"); err == nil {
		g.pwsh = p
	}
	if g.self, err = os.Executable(); err != nil {
		return err
	}
	if g.work != "" {
		for _, d := range []string{"tmp", "gocache", "logs"} {
			if err := os.MkdirAll(filepath.Join(g.work, d), 0o755); err != nil {
				return err
			}
		}
	}
	if g.jsonOut == "" {
		g.jsonOut = filepath.Join(g.work, "gate-last.json")
	}
	if runtime.GOOS == "windows" {
		// The tripwire is this program under the name wsl.exe, first on
		// PATH in every step that runs without MK_LINUX=1.
		g.tripDir = filepath.Join(g.work, "tripwire")
		g.tripLog = filepath.Join(g.work, "tripwire.log")
		if err := os.MkdirAll(g.tripDir, 0o755); err != nil {
			return err
		}
		b, err := os.ReadFile(g.self)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(g.tripDir, "wsl.exe"), b, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// tripwire is the program run as wsl.exe: it records the call in
// MK_GATE_TRIPWIRE and fails, so no Linux run starts outside the Linux pass.
func tripwire() int {
	wd, _ := os.Getwd()
	line := fmt.Sprintf("%s wsl.exe %q in %s\n", time.Now().Format(time.RFC3339), os.Args[1:], wd)
	if p := os.Getenv("MK_GATE_TRIPWIRE"); p != "" {
		if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			f.WriteString(line)
			f.Close()
		}
	}
	fmt.Fprintf(os.Stderr, "gate tripwire: wsl.exe started while MK_LINUX=1 is not set: %s", line)
	return 97
}

// probeWSL asks `wsl.exe -e true` to answer within 30 s; it runs under the
// WSL lock, as the first command of the Linux pass.
func probeWSL() int {
	return probe(30 * time.Second)
}

// summary prints one line per step and the failures.
func (g *gate) summary() {
	fmt.Printf("\ngate summary (%s tier, %.1f s)\n", g.tier, g.rep.WallSeconds)
	for _, s := range g.rep.Steps {
		detail := s.Reason
		if t := s.Tests; t != nil {
			detail = fmt.Sprintf("packages %d (cached %d, failed %d), tests pass %d fail %d skip %d (MK_LINUX %d, MK_TIMING %d; %d passed with timing unasserted)",
				t.Packages, t.PackagesCached, t.PackagesFailed, t.Pass, t.Fail, t.Skip, t.LinuxSkips, t.TimingSkips, t.TimingLogged)
		}
		if c := s.Corpus; c != nil {
			detail = fmt.Sprintf("pass %d, fail %d, pending %d (listed %d)", c.Pass, c.Fail, c.Pending, c.Listed)
		}
		if f := s.Fuzz; f != nil {
			detail = fmt.Sprintf("%d execs in %s", f.Execs, f.FuzzTime)
			if f.Crasher != "" {
				detail += ", crasher " + f.Crasher
			}
		}
		fmt.Printf("  %-4s %-44s %9.1fs exit %-3d %s\n", s.Status, s.Name, s.WallSeconds, s.ExitCode, detail)
	}
	for _, s := range g.rep.Steps {
		if s.Status != "fail" {
			continue
		}
		fmt.Printf("\nFAIL %s: %s\n", s.Name, s.Reason)
		for i, f := range s.Failed {
			if i == 30 {
				fmt.Printf("  ... %d more in %s\n", len(s.Failed)-i, g.jsonOut)
				break
			}
			fmt.Printf("  %s\n", f)
		}
		if t := s.Tests; t != nil {
			for _, h := range append(t.GuardHits, t.TripwireHits...) {
				fmt.Printf("  WSL outside the Linux pass: %s\n", h)
			}
		}
		if len(s.Failed) == 0 {
			for _, l := range s.Output {
				fmt.Printf("  | %s\n", l)
			}
		}
		switch {
		case len(s.Logs) > 0:
			for _, l := range s.Logs {
				fmt.Printf("  log: %s\n", l)
			}
		case s.Log != "":
			fmt.Printf("  log: %s\n", s.Log)
		}
	}
	verdict := "PASS"
	if !g.rep.OK {
		verdict = "FAIL"
	}
	fmt.Printf("\ngate %s; report %s\n", verdict, g.jsonOut)
}

func (g *gate) writeReport() error {
	b, err := json.MarshalIndent(g.rep, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(g.jsonOut), 0o755); err != nil {
		return err
	}
	return os.WriteFile(g.jsonOut, append(b, '\n'), 0o644)
}

// normPkg writes a package as "./dir" with slashes.
func normPkg(p string) string {
	p = filepath.ToSlash(p)
	p = strings.TrimPrefix(p, "mk/")
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimSuffix(p, "/")
	return "./" + p
}

// selected reports whether -only lets the step run package p.
func (g *gate) selected(p string) bool {
	return len(g.only) == 0 || slices.Contains(g.only, normPkg(p))
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func secs(d time.Duration) float64 {
	return float64(d.Milliseconds()) / 1000
}
