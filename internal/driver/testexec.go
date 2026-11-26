// Why: one test must not take down another, so mk test runs each test in a
// child process of its own, under a timeout, and still reports in a fixed
// order.
//
// What: TestOptions, TestCase, TestReport (ExitStatus, WriteJSON,
// WriteText), Session.Test, runChild and the child environment (testEnv,
// childEnv), with the --sim seed and --prop plumbing.
//
// How: one runner executable is built over the selected tests
// (testrunner.go); each test runs as that executable with its index, at most
// Jobs at once, and the report is written in test order. Children of another
// target run one at a time through its runner (wsl.exe on Windows).

package driver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/toolchain"
	"mk/internal/types"
)

// ProfileTest is types.TestProfile, for cmd/mk: mk test's profile
// (contracts on, the counting heap).
const ProfileTest = types.TestProfile

// TestOptions are mk test's own options. Config.TestTimeout bounds each
// child (0: no bound).
type TestOptions struct {
	// Run selects the tests whose names contain it ("" selects every one).
	Run string
	// Sim is mk test --sim SEED, meaningful when SimSet: every child gets
	// SimSeedEnv=<Sim in decimal> in its environment (WSLENV carries it to
	// a Linux child), and the report carries the seed. The library half
	// (sim.Sim reading the seed) is std's; a child that does
	// not read it runs as without --sim.
	Sim    uint64
	SimSet bool
	// Prop is mk test --prop: the property tester (prop.go) runs instead of
	// the test children, one report per contract. With SimSet, Sim seeds its
	// generator; else PropDefaultSeed does.
	Prop bool
	// PropCases is how many valid cases each contract instance gets; 0
	// means PropDefaultCases. cmd/mk does not set it.
	PropCases int
	// Fuzz, FuzzTime and FuzzHang are mk test --fuzz <test name> and
	// --fuzztime (fuzz.go); Bench and BenchLimit are mk bench (bench.go).
	Fuzz       string
	FuzzTime   time.Duration
	FuzzHang   time.Duration
	FuzzIters  int
	Bench      bool
	BenchLimit time.Duration
	// fuzzDirs is every test's corpus directory name (fuzzcorpus.go),
	// made once per run by Test.
	fuzzDirs map[fuzzKey]string
	// Cover is mk test --cover (cover.go): the build counts blocks, each
	// child writes its counts, and the report carries line coverage.
	// --fuzz builds with the counters too, for the loop's guidance.
	Cover bool
	// cover is the runner image's counters (nil: none) and coverDir the
	// directory of the children's counts files.
	cover    *coverImage
	coverDir string
}

// SimSeedEnv is the environment variable a test child reads its --sim seed
// from (System.Env), never a command-line argument: the runner's only
// argument is the test index.
const SimSeedEnv = "MK_SIM_SEED"

// TestCase is one test's outcome.
type TestCase struct {
	Module string `json:"module"`
	Name   string `json:"name"`
	// Result is pass, fail, panic, leak, error or timeout; under --prop
	// pass, fail or skip.
	Result string `json:"result"`
	// Detail is a --prop contract's reason: the finding's kind, or why it
	// was skipped.
	Detail string `json:"detail,omitempty"`
	// Status is the child's exit status; -1 when it was killed or did not
	// start.
	Status int `json:"status"`
	// Output is the child's standard output and standard error, in the
	// order written.
	Output   string        `json:"output"`
	Duration time.Duration `json:"-"`
	// coverAcked is whether the child answered the driver's request for
	// its counts before a timeout kill (cover.go's coverAsk).
	coverAcked bool
	// Bench is a benchmark's statistics under mk bench (bench.go).
	Bench *BenchResult `json:"bench,omitempty"`
	// Note is a line the text report prints under the test whatever its
	// result: a corpus directory not read (fuzzcorpus.go).
	Note string `json:"note,omitempty"`
}

// TestReport is mk test's report, tests in test order.
type TestReport struct {
	Tests  []TestCase `json:"tests"`
	Passed int        `json:"passed"`
	Failed int        `json:"failed"`
	// SimSeed is the --sim seed every child ran under; absent without --sim
	// and under --prop.
	SimSeed *uint64 `json:"sim_seed,omitempty"`
	// Cover is mk test --cover's line coverage (cover.go).
	Cover *CoverReport `json:"cover,omitempty"`
	// Skipped counts --prop contracts the tester could not run.
	Skipped int `json:"skipped,omitempty"`
	// PropSeed is the --prop generator's seed; absent without --prop.
	PropSeed *uint64 `json:"prop_seed,omitempty"`
	// timeout is the bound the children ran under, for the timeout reason.
	timeout time.Duration
	// ShowOutput is mk test --show-output: the text report prints a passing
	// test's output under its PASS line too. The JSON report carries every
	// test's output whatever it is.
	ShowOutput bool `json:"-"`
}

// ExitStatus is 0 when every selected test passed, 1 otherwise.
func (r *TestReport) ExitStatus() int {
	if r.Failed > 0 {
		return 1
	}
	return 0
}

// WriteJSON writes the report as one JSON object and a newline:
// {"tests": [{"module", "name", "result", "status", "output"}],
// "passed", "failed"}.
func (r *TestReport) WriteJSON(w io.Writer) error {
	doc := *r
	if doc.Tests == nil {
		doc.Tests = []TestCase{}
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// WriteText writes PASS and FAIL lines in test order, a failed test's
// output indented under it (every test's under --prop or ShowOutput), then
// "N passed, M failed".
// Durations only with timings.
func (r *TestReport) WriteText(w io.Writer, timings bool) error {
	var b strings.Builder
	for _, t := range r.Tests {
		switch t.Result {
		case "pass":
			fmt.Fprintf(&b, "PASS %s %s", t.Module, strconv.Quote(t.Name))
		case "skip":
			fmt.Fprintf(&b, "SKIP %s %s: %s", t.Module, strconv.Quote(t.Name), t.Detail)
		default:
			fmt.Fprintf(&b, "FAIL %s %s: %s", t.Module, strconv.Quote(t.Name), r.reason(t))
		}
		if timings {
			fmt.Fprintf(&b, " (%s)", t.Duration.Round(time.Millisecond))
		}
		b.WriteByte('\n')
		if t.Note != "" {
			b.WriteString("    note: " + t.Note + "\n")
		}
		if (t.Result != "pass" || r.PropSeed != nil || r.ShowOutput) && t.Output != "" {
			for _, l := range strings.Split(strings.TrimRight(strings.ReplaceAll(t.Output, "\r\n", "\n"), "\n"), "\n") {
				b.WriteString("    " + l + "\n")
			}
		}
	}
	fmt.Fprintf(&b, "%d passed, %d failed", r.Passed, r.Failed)
	if r.Skipped > 0 {
		fmt.Fprintf(&b, ", %d skipped", r.Skipped)
	}
	if r.SimSeed != nil {
		fmt.Fprintf(&b, " (sim seed %d)", *r.SimSeed)
	}
	if r.PropSeed != nil {
		fmt.Fprintf(&b, " (prop seed %d)", *r.PropSeed)
	}
	b.WriteByte('\n')
	_, err := io.WriteString(w, b.String())
	return err
}

// reason is a failed test's reason text.
func (r *TestReport) reason(t TestCase) string {
	if t.Detail != "" {
		return t.Detail
	}
	switch t.Result {
	case "timeout":
		return "timeout after " + timeoutText(r.timeout)
	case "error":
		return "error return (exit 1)"
	case "panic":
		return "panic (exit 101)"
	case "leak":
		return "leak (exit 102)"
	}
	if t.Status < 0 {
		return "did not run"
	}
	return "exit " + strconv.Itoa(t.Status)
}

// timeoutText prints a test timeout as the text report writes it: whole
// seconds as "60s" and "90s", never Go's "1m0s". A timeout that is not a
// whole number of seconds keeps time.Duration's form ("1.5s", "250ms"),
// which loses nothing.
func timeoutText(d time.Duration) string {
	if d > 0 && d%time.Second == 0 {
		return strconv.FormatInt(int64(d/time.Second), 10) + "s"
	}
	return d.String()
}

// resultOf maps a child's exit status to a result (0 pass, 1 an error
// return, 101 a panic, 102 a leak).
func resultOf(status int) string {
	switch status {
	case 0:
		return "pass"
	case 1:
		return "error"
	case 101:
		return "panic"
	case 102:
		return "leak"
	}
	return "fail"
}

// Test is mk test: check the package with the runner module added (at the
// test profile unless Config.Profile is Release), then, when the report is
// clean, build the runner over the tests opt.Run selects and run each in
// its own child. A compile failure returns a nil report. A target this
// host cannot run is a *UsageError.
func (s *Session) Test(opt TestOptions) (Result, *TestReport, error) {
	if opt.Cover {
		// cover.go: what coverage cannot serve is refused.
		switch {
		case opt.Fuzz != "":
			return Result{}, nil, usagef("--cover with --fuzz: the loop's children are killed at the fuzz time and the counts guide the loop, so none is a coverage report; run --cover without --fuzz")
		case opt.Prop:
			return Result{}, nil, usagef("--cover with --prop: the property tester runs no test children to count")
		case s.cfg.StdTests != "":
			return Result{}, nil, usagef("--cover with --std: std's own code is never counted, so there is nothing to report")
		}
	}
	if opt.Prop {
		return s.testProp(opt)
	}
	if opt.Cover || opt.Fuzz != "" {
		s.cfg.Cover = true // cover.go: counters for the report or the loop's guidance
	}
	if s.cfg.Profile != types.Release {
		s.cfg.Profile = types.TestProfile
	}
	s.testing = true
	s.startTools()
	defer s.joinTools()
	r := s.runCheck()
	defer s.w.maybeGC() // once, after every child exited
	if len(r.Report.Diags) > 0 {
		return s.verifiedReport(r), nil, nil
	}
	if d := notBuilt(s.cfg.Target); d != nil {
		// A checked-only target is refused before the runner lookup.
		s.p.diags = append(s.p.diags, d)
		return s.p.result(s.w, s.cfg.MaxDiags), nil, nil
	}
	prefix, ok := s.cfg.runner()
	if !ok {
		return r, nil, usagef("cannot run %s executables on this machine", s.cfg.Target.Name)
	}
	var sel []testCase
	all := s.w.packageTests(s.testModules())
	opt.fuzzDirs = fuzzDirMap(all) // once per run: fuzzcorpus.go
	for _, tc := range all {
		if s.selectTest(tc, opt) { // testing.go: --run, --fuzz, mk bench
			sel = append(sel, tc)
		}
	}
	rep := &TestReport{timeout: s.cfg.TestTimeout}
	if opt.SimSet {
		seed := opt.Sim
		rep.SimSeed = &seed
	}
	env := testEnv(os.Environ(), len(prefix) > 0, opt)
	if len(sel) == 0 {
		r.Reached = StageNative
		return r, rep, nil
	}
	if ie := source.Guard("the test runner", source.Span{}, func() { s.w.testMain(s.runner, sel) }); ie != nil {
		s.p.diags = append(s.p.diags, diag.FromInternal(ie))
		return s.p.result(s.w, s.cfg.MaxDiags), nil, nil
	}
	place, keyed := s.runPlacement("mk-test")
	exe, ds := s.native(binTarget{name: "mk-test", mod: s.runner}, place, keyed)
	if len(ds) > 0 {
		s.p.diags = append(s.p.diags, ds...)
		return s.p.result(s.w, s.cfg.MaxDiags), nil, nil
	}
	if opt.Cover || opt.Fuzz != "" { // cover.go
		img, err := readCoverImage(exe)
		if err != nil {
			return r, nil, err
		}
		opt.cover = img
		if opt.Cover && img != nil {
			if opt.coverDir, err = os.MkdirTemp("", "mk-cover-"); err != nil {
				return r, nil, err
			}
			defer os.RemoveAll(opt.coverDir)
		}
	}
	if s.cfg.NoCache && exe != "" {
		defer os.RemoveAll(filepath.Dir(exe))
	}
	r = s.p.result(s.w, s.cfg.MaxDiags)
	r.Reached = StageNative
	rep.Tests = make([]TestCase, len(sel))
	argv := append(append([]string(nil), prefix...), toolchain.HostPathFor(prefix, exe))
	// Children of another target run through its runner (wsl.exe on
	// Windows), one at a time: parallel WSL clients fail to connect or
	// hang WSL (TestMkTestLinux saw "a connection attempt failed").
	jobs := s.cfg.Jobs
	if len(prefix) > 0 || opt.Bench { // benchmarks are timed one at a time
		jobs = 1
	}
	sem := make(chan struct{}, jobs)
	var wg sync.WaitGroup
	for i, tc := range sel {
		rep.Tests[i] = TestCase{Module: tc.module, Name: tc.name}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if ie := source.Guard("test child "+tc.name, source.Span{}, func() {
				s.runTestChild(&rep.Tests[i], tc, argv, env, i, opt) // testing.go
			}); ie != nil {
				rep.Tests[i].Result, rep.Tests[i].Status, rep.Tests[i].Output = "fail", -1, "internal error: "+ie.What
			}
		}()
	}
	wg.Wait()
	if opt.coverDir != "" { // cover.go: every child's counts, summed
		sum := make([]uint64, opt.cover.size/8)
		for i := range sel {
			if err := addCounts(sum, filepath.Join(opt.coverDir, strconv.Itoa(i))); err != nil {
				n := &rep.Tests[i].Note
				if *n != "" {
					*n += "; "
				}
				*n += "coverage: " + err.Error()
			}
		}
		var mods []string
		for _, m := range s.testModules() {
			if m.path != testRunnerModule { // the runner and the test blocks' clones
				mods = append(mods, m.path)
			}
		}
		rep.Cover = opt.cover.coverReport(sum, mods)
	}
	for _, t := range rep.Tests {
		if t.Result == "pass" {
			rep.Passed++
		} else {
			rep.Failed++
		}
	}
	return r, rep, nil
}

// testModules are the modules whose test blocks mk test runs: the
// package's, or with Config.StdTests the std modules it selects (the
// module itself and the modules under it).
func (s *Session) testModules() []*modNode {
	if s.cfg.StdTests == "" {
		return s.pkgB.mods
	}
	var out []*modNode
	for _, m := range s.stdB.mods {
		if m.path == s.cfg.StdTests || s.cfg.StdTests == "std" || strings.HasPrefix(m.path, s.cfg.StdTests+".") {
			out = append(out, m)
		}
	}
	return out
}

// runChild runs test index i of the runner and fills tc. A child still
// running after timeout (when nonzero) is killed with its process tree
// (killTree) and reported timeout.
func runChild(tc *TestCase, argv, env []string, i int, timeout time.Duration, beforeKill ...func()) {
	cmd := exec.Command(argv[0], append(argv[1:], strconv.Itoa(i))...)
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out // one writer: one goroutine writes
	cmd.WaitDelay = 5 * time.Second     // a grandchild holding the pipe cannot hang Wait
	prepareChild(cmd)
	start := time.Now()
	if err := cmd.Start(); err != nil {
		tc.Result, tc.Status, tc.Output = "fail", -1, err.Error()
		return
	}
	done := make(chan error, 1)
	var waitErr error
	go func() {
		defer func() { done <- waitErr }()
		if ie := source.Guard("wait for a test child", source.Span{}, func() { waitErr = cmd.Wait() }); ie != nil {
			waitErr = errors.New(ie.What)
		}
	}()
	var timer <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timer = t.C
	}
	var err error
	timedOut := false
	select {
	case err = <-done:
	case <-timer:
		timedOut = true
		for _, f := range beforeKill { // cover.go: the child's counts first
			f()
		}
		killTree(cmd)
		err = <-done
	}
	tc.Duration = time.Since(start)
	tc.Output = out.String()
	var ee *exec.ExitError
	switch {
	case timedOut:
		tc.Result, tc.Status = "timeout", -1
	case err == nil:
		tc.Result, tc.Status = "pass", 0
	case errors.As(err, &ee) && childStatus(ee) >= 0:
		tc.Status = childStatus(ee)
		tc.Result = resultOf(tc.Status)
	default:
		tc.Result, tc.Status = "fail", -1
		tc.Output += err.Error()
	}
}

// testEnv is a test child's environment: mk's own (base), with
// SimSeedEnv set to the --sim seed when one was given and removed when not,
// so a seed set outside mk never makes a plain mk test run seeded
// while its report says no seed.
func testEnv(base []string, viaRunner bool, opt TestOptions) []string {
	if opt.SimSet {
		return childEnv(base, viaRunner, SimSeedEnv+"="+strconv.FormatUint(opt.Sim, 10))
	}
	out := make([]string, 0, len(base))
	for _, kv := range base {
		n, v, _ := strings.Cut(kv, "=")
		switch {
		case strings.EqualFold(n, SimSeedEnv):
			continue
		case viaRunner && strings.EqualFold(n, "WSLENV"):
			kv = "WSLENV=" + dropWSLName(v, SimSeedEnv)
		}
		out = append(out, kv)
	}
	return out
}

// dropWSLName removes name (with any /flags) from a WSLENV list.
func dropWSLName(list, name string) string {
	var keep []string
	for _, e := range strings.Split(list, ":") {
		if n, _, _ := strings.Cut(e, "/"); e != "" && !strings.EqualFold(n, name) {
			keep = append(keep, e)
		}
	}
	return strings.Join(keep, ":")
}

// childEnv is base with each NAME=value of set replacing any entry of the
// same name. viaRunner (a Linux child through wsl.exe) also lists the names
// in WSLENV, which is how WSL passes a Windows variable into the Linux
// process.
func childEnv(base []string, viaRunner bool, set ...string) []string {
	names := make([]string, len(set))
	for i, kv := range set {
		names[i], _, _ = strings.Cut(kv, "=")
	}
	out := make([]string, 0, len(base)+len(set)+1)
	wslenv := ""
	for _, kv := range base {
		n, v, _ := strings.Cut(kv, "=")
		switch {
		case viaRunner && strings.EqualFold(n, "WSLENV"):
			wslenv = v
		case !slicesContainsFold(names, n):
			out = append(out, kv)
		}
	}
	out = append(out, set...)
	if viaRunner {
		for _, n := range names {
			if wslenv != "" {
				wslenv += ":"
			}
			wslenv += n + "/u"
		}
		out = append(out, "WSLENV="+wslenv)
	}
	return out
}

// slicesContainsFold reports whether names holds n, ignoring case (Windows
// environment names are case-insensitive).
func slicesContainsFold(names []string, n string) bool {
	for _, m := range names {
		if strings.EqualFold(m, n) {
			return true
		}
	}
	return false
}
