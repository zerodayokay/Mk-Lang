// Why: ./std's tests take hours, so the gate gives them their own go test and
// timeout; a package that ends mid-run must read as not run, never missing.
// What: tests of splitStd, testStep, linuxStep, linuxPlan, mathStep, fold and
// the parser's handling of timed-out, killed and crashed packages.
// How: the step tests run the real steps with g.runCmd recording and
// answering each command; the parse tests replay recorded go test -json streams.

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestSplitStd: ./std leaves every run with other packages and runs
// alone with the std timeout; a "..." pattern is replaced by what go list
// gave for it, so a pattern holds ./std exactly when go list says so;
// a pattern go list did not expand stays.
func TestSplitStd(t *testing.T) {
	expand := map[string][]string{
		"./...":          {"./cmd/mk", "./internal/abi", "./std", "./tools/gate"},
		"./s...":         {"./std"},
		"./std/...":      {"./std"},
		"./internal/...": {"./internal/abi", "./internal/check"},
	}
	for _, tc := range []struct {
		pkgs []string
		want []goTestRun
	}{
		{[]string{"./..."}, []goTestRun{
			{pkgs: []string{"./cmd/mk", "./internal/abi", "./tools/gate"}, timeout: time.Hour},
			{pkgs: []string{"./std"}, timeout: 4 * time.Hour}}},
		{[]string{"./std"}, []goTestRun{{pkgs: []string{"./std"}, timeout: 4 * time.Hour}}},
		{[]string{"./s..."}, []goTestRun{{pkgs: []string{"./std"}, timeout: 4 * time.Hour}}},
		{[]string{"./internal/abi"}, []goTestRun{{pkgs: []string{"./internal/abi"}, timeout: time.Hour}}},
		{[]string{"./internal/...", "./std", "./internal/abi"}, []goTestRun{
			{pkgs: []string{"./internal/abi", "./internal/check"}, timeout: time.Hour},
			{pkgs: []string{"./std"}, timeout: 4 * time.Hour}}},
		{[]string{"./std/...", "./tools/gate"}, []goTestRun{
			{pkgs: []string{"./tools/gate"}, timeout: time.Hour},
			{pkgs: []string{"./std"}, timeout: 4 * time.Hour}}},
		{[]string{"./tools/..."}, []goTestRun{{pkgs: []string{"./tools/..."}, timeout: time.Hour}}},
		{nil, nil},
	} {
		got := splitStd(tc.pkgs, expand, 4*time.Hour)
		if !sameRuns(got, tc.want) {
			t.Errorf("splitStd(%q) = %+v, want %+v", tc.pkgs, got, tc.want)
		}
	}
	if got := wildcards([]string{"./...", "./internal/check", "./s...", "./std"}); !slices.Equal(got, []string{"./...", "./s..."}) {
		t.Errorf("wildcards = %q", got)
	}
}

func sameRuns(a, b []goTestRun) bool {
	return slices.EqualFunc(a, b, func(x, y goTestRun) bool {
		return slices.Equal(x.pkgs, y.pkgs) && x.timeout == y.timeout && x.run == y.run
	})
}

// TestParseArgs: -stdtimeout reaches the gate, 4h by default, and a
// value under a second is refused.
func TestParseArgs(t *testing.T) {
	g, err := parseArgs(nil)
	if err != nil || g.stdTimeout != 4*time.Hour {
		t.Errorf("default: %v %v", g, err)
	}
	g, err = parseArgs([]string{"-stdtimeout", "3h", "-tier", "full", "-only", "./std,mk/internal/check"})
	if err != nil || g.stdTimeout != 3*time.Hour || g.tier != "full" || !slices.Equal(g.only, []string{"./std", "./internal/check"}) {
		t.Errorf("-stdtimeout 3h: %+v %v", g, err)
	}
	if _, err := parseArgs([]string{"-stdtimeout", "0s"}); err == nil {
		t.Errorf("-stdtimeout 0s accepted")
	}
}

// call is one command a step started.
type call struct {
	argv, env []string
}

func (c call) line() string { return strings.Join(c.argv, " ") }

func (c call) has(kv string) bool { return slices.Contains(c.env, kv) }

// fakeGate is a gate whose commands g.runCmd records and answer answers.
func fakeGate(t *testing.T, tier string, answer func(argv []string) (string, int)) (*gate, *[]call) {
	t.Helper()
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	g := &gate{tier: tier, goExe: "go", pwsh: "pwsh", self: "gate.exe", root: "R", work: work,
		tripDir: "TRIP", tripLog: filepath.Join(work, "tripwire.log"), stdTimeout: defaultStdTimeout,
		skip: map[string]bool{}, linuxTests: map[string]map[string]bool{}, testResults: map[string]string{},
		ended: map[string]pkgEnd{}, rep: &Report{}}
	calls := &[]call{}
	g.runCmd = func(argv, env []string, out io.Writer) (int, error) {
		*calls = append(*calls, call{slices.Clone(argv), slices.Clone(env)})
		o, code := answer(argv)
		io.WriteString(out, o)
		return code, nil
	}
	return g, calls
}

// ev is one go test -json line.
func ev(action, pkg, test, output string) string {
	s := fmt.Sprintf(`{"Action":%q,"Package":%q`, action, pkg)
	if test != "" {
		s += fmt.Sprintf(`,"Test":%q`, test)
	}
	if output != "" {
		s += fmt.Sprintf(`,"Output":%q`, output+"\n")
	}
	return s + "}\n"
}

// passes are the lines of tests that pass in pkg, then the package's pass.
func passes(pkg string, tests ...string) string {
	var b strings.Builder
	for _, t := range tests {
		b.WriteString(ev("run", pkg, t, "") + ev("pass", pkg, t, ""))
	}
	return b.String() + ev("pass", pkg, "", "")
}

// linuxSkip is a test of pkg that skips for MK_LINUX.
func linuxSkip(pkg, test string) string {
	return ev("run", pkg, test, "") + ev("output", pkg, test, "    x_test.go:1: "+linuxOff) + ev("skip", pkg, test, "")
}

// stepAnswer answers the test and Linux steps' commands: go list, the
// probe, and go test runs whose packages report a pass, std and driver
// with one Linux skip each in the test step.
func stepAnswer(argv []string) (string, int) {
	j := strings.Join(argv, " ")
	switch {
	case slices.Contains(argv, "list"):
		return "mk/internal/abi\nmk/internal/driver\nmk/std\nmk/tools/gate\n", 0
	case strings.HasSuffix(j, "-probe-wsl"):
		return "answered\n", 0
	case !slices.Contains(argv, "test"):
		return "", 3
	}
	if slices.Contains(argv, "-run") {
		run := argv[slices.Index(argv, "-run")+1]
		pkg := "mk/" + strings.TrimPrefix(argv[len(argv)-1], "./")
		return passes(pkg, strings.Split(strings.Trim(run, "^()$"), "|")...), 0
	}
	var b strings.Builder
	for _, p := range argv[slices.Index(argv, "test")+1:] {
		if !strings.HasPrefix(p, "./") {
			continue
		}
		pkg := "mk/" + strings.TrimPrefix(p, "./")
		switch p {
		case "./std":
			b.WriteString(linuxSkip(pkg, "TestCore_LinuxBlocks") + passes(pkg, "TestA"))
		case "./internal/driver":
			b.WriteString(linuxSkip(pkg, "TestD") + passes(pkg, "TestB"))
		default:
			b.WriteString(passes(pkg, "TestC"))
		}
	}
	return b.String(), 0
}

// TestTestAndLinuxSteps: testStep lists the module, runs the other
// packages, then ./std alone with -stdtimeout, both behind the tripwire;
// linuxStep then reruns the Linux skips that testStep recorded, under
// tools/wsl-lock.ps1 with -p 1 and MK_LINUX=1, ./std alone again, and the
// Linux packages testStep did not report whole.
func TestTestAndLinuxSteps(t *testing.T) {
	g, calls := fakeGate(t, "quick", stepAnswer)
	g.testStep()
	if len(*calls) != 3 {
		t.Fatalf("test step calls: %q", *calls)
	}
	if got := (*calls)[0].line(); got != "go list -e ./..." {
		t.Errorf("list: %q", got)
	}
	for i, want := range []string{
		"go test -json -timeout 3600s ./internal/abi ./internal/driver ./tools/gate",
		"go test -json -timeout 14400s ./std",
	} {
		c := (*calls)[i+1]
		if c.line() != want {
			t.Errorf("test run %d:\n got %q\nwant %q", i+1, c.line(), want)
		}
		path := ""
		for _, kv := range c.env {
			if k, v, _ := strings.Cut(kv, "="); sameKey(k, "PATH") {
				path = v
			}
		}
		if !strings.HasPrefix(path, "TRIP"+string(os.PathListSeparator)) || !c.has("MK_GATE_TRIPWIRE="+g.tripLog) || !c.has("MK_GATE=1") {
			t.Errorf("test run %d is not behind the tripwire: PATH %q, env %q", i+1, path, c.env)
		}
		if slices.ContainsFunc(c.env, func(kv string) bool { return strings.HasPrefix(kv, "MK_LINUX=") }) {
			t.Errorf("test run %d sets MK_LINUX", i+1)
		}
	}
	s := g.rep.Steps[0]
	if s.Status != "pass" || s.Tests.Packages != 4 || len(s.Logs) != 2 || strings.Count(s.Command, " ; ") != 1 {
		t.Errorf("test step: %s %q, %+v, logs %q, command %q", s.Status, s.Reason, s.Tests, s.Logs, s.Command)
	}
	if !g.testRan || !g.linuxTests["./std"]["TestCore_LinuxBlocks"] || !g.linuxTests["./internal/driver"]["TestD"] {
		t.Errorf("the Linux skips the test step recorded: %v", g.linuxTests)
	}
	if runtime.GOOS != "windows" {
		return // linuxStep runs on a Windows host only
	}

	*calls = nil
	g.linuxStep()
	lock := "pwsh -NoProfile -File " + filepath.Join("R", "tools", "wsl-lock.ps1") + " "
	var whole []string
	for _, p := range linuxPkgs {
		if _, reported := g.linuxTests[p]; !reported {
			whole = append(whole, p)
		}
	}
	want := []string{
		lock + "gate.exe -probe-wsl",
		lock + "go test -json -timeout 3600s -p 1 -run ^(TestD)$ ./internal/driver",
		lock + "go test -json -timeout 14400s -p 1 -run ^(TestCore_LinuxBlocks)$ ./std",
		lock + "go test -json -timeout 3600s -p 1 " + strings.Join(whole, " "),
	}
	var got []string
	for _, c := range *calls {
		got = append(got, c.line())
	}
	if !slices.Equal(got, want) {
		t.Fatalf("linux step:\n got %q\nwant %q", got, want)
	}
	for _, c := range (*calls)[1:] {
		if !c.has("MK_LINUX=1") || !c.has("MK_GATE=1") || slices.ContainsFunc(c.env, func(kv string) bool { return strings.HasPrefix(kv, "MK_GATE_TRIPWIRE=") }) {
			t.Errorf("%q: env %q", c.line(), c.env)
		}
	}
	if ls := g.rep.Steps[len(g.rep.Steps)-1]; ls.Name != linuxStepName || ls.Status != "pass" || g.testResults["./std TestCore_LinuxBlocks"] != "pass" {
		t.Errorf("linux step: %s %s %q; result %q", ls.Name, ls.Status, ls.Reason, g.testResults["./std TestCore_LinuxBlocks"])
	}

	// -skip test: the Linux packages run whole, ./std alone.
	g, calls = fakeGate(t, "full", stepAnswer)
	g.linuxStep()
	got = nil
	for _, c := range *calls {
		got = append(got, c.line())
	}
	var others []string
	for _, p := range linuxPkgs {
		if p != stdPkg {
			others = append(others, p)
		}
	}
	want = []string{
		lock + "gate.exe -probe-wsl",
		lock + "go test -json -timeout 3600s -count=1 -p 1 " + strings.Join(others, " "),
		lock + "go test -json -timeout 14400s -count=1 -p 1 ./std",
	}
	if !slices.Equal(got, want) {
		t.Errorf("linux step after -skip test:\n got %q\nwant %q", got, want)
	}
}

// TestTestStepOnly: -only's packages need no go list; ./std among
// them still runs alone, after the others.
func TestTestStepOnly(t *testing.T) {
	g, calls := fakeGate(t, "full", stepAnswer)
	g.only = []string{"./std", "./internal/check"}
	g.testStep()
	var got []string
	for _, c := range *calls {
		got = append(got, c.line())
	}
	want := []string{
		"go test -json -timeout 3600s -count=1 ./internal/check",
		"go test -json -timeout 14400s -count=1 ./std",
	}
	if !slices.Equal(got, want) {
		t.Errorf("-only:\n got %q\nwant %q", got, want)
	}
}

// TestListFailureStopsLinux: a go list that fails fails the test step
// with its error, runs no go test, and fails the Linux step instead of
// running every Linux package whole.
func TestListFailureStopsLinux(t *testing.T) {
	g, calls := fakeGate(t, "quick", func(argv []string) (string, int) {
		if slices.Contains(argv, "list") {
			return "pattern ./...: no go.mod", 1
		}
		return stepAnswer(argv)
	})
	g.testStep()
	g.linuxStep()
	if len(*calls) != 1 {
		t.Errorf("commands after a failed go list: %q", *calls)
	}
	ts := g.rep.Steps[0]
	if ts.Status != "fail" || !strings.Contains(ts.Reason, "go list -e ./...") || !strings.Contains(ts.Reason, "no go.mod") {
		t.Errorf("test step: %s %q", ts.Status, ts.Reason)
	}
	if runtime.GOOS == "windows" {
		ls := g.rep.Steps[len(g.rep.Steps)-1]
		if ls.Name != linuxStepName || ls.Status != "fail" || !strings.Contains(ls.Reason, "not run: the test step's go list") {
			t.Errorf("linux step: %s %s %q", ls.Name, ls.Status, ls.Reason)
		}
	}
}

// TestMathStepKeepsTheHour: the math checksums select one test each,
// so they keep -timeout 3600s, not -stdtimeout; the Linux one runs under
// the lock with -p 1.
func TestMathStepKeepsTheHour(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the Linux checksum runs on a Windows host only")
	}
	g, calls := fakeGate(t, "full", stepAnswer)
	g.stdTimeout = 3 * time.Hour
	g.mathStep()
	lock := "pwsh -NoProfile -File " + filepath.Join("R", "tools", "wsl-lock.ps1") + " "
	want := []string{
		"go test -json -timeout 3600s -count=1 -run ^TestSTDMATH_Exhaustive$ ./std",
		lock + "gate.exe -probe-wsl",
		lock + "go test -json -timeout 3600s -count=1 -p 1 -run ^TestSTDMATH_ExhaustiveLinux$ ./std",
	}
	var got []string
	for _, c := range *calls {
		got = append(got, c.line())
	}
	if !slices.Equal(got, want) {
		t.Errorf("math step:\n got %q\nwant %q", got, want)
	}
}

// TestListThisModule: go list -e ./... over this module gives the
// test step a first run of every package but ./std, as "./dir".
func TestListThisModule(t *testing.T) {
	goExe, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	g := &gate{goExe: goExe, root: root, tier: "quick", stdTimeout: defaultStdTimeout}
	expand, err := g.listPkgs(wildcards(g.testPkgs()))
	if err != nil {
		t.Fatal(err)
	}
	runs := g.testRuns(g.testPkgs(), expand)
	if len(runs) != 2 || !slices.Equal(runs[1].pkgs, []string{"./std"}) {
		t.Fatalf("runs %+v", runs)
	}
	for _, p := range []string{"./cmd/mk", "./internal/driver", "./internal/stdtest", "./tools/gate"} {
		if !slices.Contains(runs[0].pkgs, p) {
			t.Errorf("the first run lacks %s: %q", p, runs[0].pkgs)
		}
	}
	if slices.Contains(runs[0].pkgs, "./std") {
		t.Errorf("the first run holds ./std")
	}
}

// TestLinuxPlan: the Linux step's plan after a test step whose ./std
// timed out, and with -only ./std.
func TestLinuxPlan(t *testing.T) {
	argvs := func(g *gate, runs []goTestRun) []string {
		var out []string
		for _, r := range runs {
			out = append(out, strings.Join(g.testArgv(r, "-p", "1"), " "))
		}
		return out
	}
	var others []string
	for _, p := range linuxPkgs {
		if p != stdPkg {
			others = append(others, p)
		}
	}
	g := &gate{goExe: "go", tier: "quick", stdTimeout: defaultStdTimeout, testRan: true,
		linuxTests: map[string]map[string]bool{
			"./std":             {"TestB": true, "TestA": true},
			"./internal/driver": {"TestD": true},
			"./internal/abi":    {},
		},
		ended: map[string]pkgEnd{"./std": {why: "timed out after 4h0m0s in the test step"}},
	}
	var whole []string
	for _, p := range others {
		if p != "./internal/driver" && p != "./internal/abi" {
			whole = append(whole, p)
		}
	}
	runs, notes := g.linuxPlan()
	want := []string{
		"go test -json -timeout 3600s -p 1 -run ^(TestD)$ ./internal/driver",
		"go test -json -timeout 14400s -p 1 -run ^(TestA|TestB)$ ./std",
		"go test -json -timeout 3600s -p 1 " + strings.Join(whole, " "),
	}
	if got := argvs(g, runs); !slices.Equal(got, want) {
		t.Errorf("after the test step:\n got %q\nwant %q", got, want)
	}
	joined := strings.Join(notes, "\n")
	for _, n := range []string{
		"./internal/abi: no leaf skipped",
		"./std timed out after 4h0m0s in the test step: only its Linux leaves that skipped before then run here",
		"1 top-level tests of ./internal/driver",
		"2 top-level tests of ./std",
	} {
		if !strings.Contains(joined, n) {
			t.Errorf("notes lack %q:\n%s", n, joined)
		}
	}

	g = &gate{goExe: "go", tier: "quick", stdTimeout: defaultStdTimeout, testRan: true, only: []string{"./std"},
		linuxTests: map[string]map[string]bool{"./std": {"TestA": true}}}
	runs, _ = g.linuxPlan()
	if got := argvs(g, runs); !slices.Equal(got, []string{"go test -json -timeout 14400s -p 1 -run ^(TestA)$ ./std"}) {
		t.Errorf("-only ./std: %q", got)
	}
}

// parseFixture feeds a recorded go test -json stream from testdata to a
// new parse.
func parseFixture(t *testing.T, name string) *testParse {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return parseLines(strings.Split(strings.TrimRight(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n"), "\n"))
}

func parseLines(lines []string) *testParse {
	tp := newTestParse()
	for _, l := range lines {
		tp.line(strings.TrimSuffix(l, "\n"))
	}
	tp.finish()
	return tp
}

// TestParseTimedOutPackage: testdata/std-timeout.jsonl is cut from a
// recorded gate test step (./std at the 3600 s alarm: passes, a MK_LINUX
// skip, a paused parallel test, the subtest in flight, the panic with its
// running tests, the package's FAIL). The parse gives one failure naming
// ./std, its timeout and what was running, not one line per test without a
// result.
func TestParseTimedOutPackage(t *testing.T) {
	tp := parseFixture(t, "std-timeout.jsonl")
	want := "./std [timed out after 1h0m0s; running TestProc_SpawnBlocks (43s), TestProc_SpawnBlocks/release (17s); 3 started tests without a result]"
	if len(tp.failed) != 1 || tp.failed[0] != want {
		t.Fatalf("failed %q\nwant [%q]", tp.failed, want)
	}
	if tp.ended["./std"] != "timed out after 1h0m0s" {
		t.Errorf("ended %q", tp.ended)
	}
	if tp.counts.PackagesFailed != 1 || tp.counts.Pass != 3 || tp.counts.Skip != 1 || tp.counts.LinuxSkips != 1 {
		t.Errorf("counts %+v", tp.counts)
	}
	if !tp.linux["./std"]["TestCore_LinuxBlocks"] || tp.results["./std TestCore_LinuxBlocks"] != "skip" {
		t.Errorf("the Linux skip: %v %v", tp.linux, tp.results)
	}
	if len(tp.failOut) != 1 {
		t.Fatalf("fail output: %d entries", len(tp.failOut))
	}
	for _, s := range []string{"panic: test timed out after 1h0m0s", "running tests:", "TestProc_SpawnBlocks/release (17s)", "3 started tests without a result:", "  TestCoreNew_BitsMatchGo"} {
		if !strings.Contains(tp.failOut[0], s) {
			t.Errorf("fail output lacks %q:\n%s", s, tp.failOut[0])
		}
	}
	if strings.Contains(tp.failOut[0], "goroutine") {
		t.Errorf("fail output keeps the goroutine dump:\n%s", tp.failOut[0])
	}
}

// TestParseDeathKeepsItsReason: recorded streams of a
// package that dies without a timeout (fx/gpanic: a panic in a goroutine
// a test started; fx/logfatal: log.Fatal; fx/exit: os.Exit), each with a
// test paused in t.Parallel. The one entry names the test that was
// running, and its output keeps that test's last lines and the package's
// own, not the paused test's.
func TestParseDeathKeepsItsReason(t *testing.T) {
	for _, tc := range []struct {
		file, entry, reason string
	}{
		{"gpanic.jsonl", "./gpanic [ended mid-run; running TestSpawn; 2 started tests without a result]", "panic: worker lost its carrier"},
		{"logfatal.jsonl", "./logfatal [ended mid-run; running TestFatal; 2 started tests without a result]", "cannot open the fixture: denied"},
		{"exit.jsonl", "./exit [ended mid-run; running TestExit; 2 started tests without a result]", "the reason it exits: lost the child"},
	} {
		tp := parseFixture(t, tc.file)
		if len(tp.failed) != 1 || tp.failed[0] != tc.entry {
			t.Errorf("%s: failed %q\nwant [%q]", tc.file, tp.failed, tc.entry)
			continue
		}
		if tp.ended[strings.Fields(tc.entry)[0]] != "ended mid-run" {
			t.Errorf("%s: ended %q", tc.file, tp.ended)
		}
		if len(tp.failOut) != 1 {
			t.Fatalf("%s: %d fail outputs", tc.file, len(tp.failOut))
		}
		out := tp.failOut[0]
		for _, s := range []string{tc.reason, "the package's own lines:", "FAIL\tmk/"} {
			if !strings.Contains(out, s) {
				t.Errorf("%s: fail output lacks %q:\n%s", tc.file, s, out)
			}
		}
		if strings.Contains(out, "=== PAUSE") {
			t.Errorf("%s: fail output keeps the paused test's lines:\n%s", tc.file, out)
		}
	}
}

// TestParseKill: a recorded stream of a test binary go
// test killed (fx/kill, -timeout 2s, the binary blocked before its tests
// start): no test had started, and the one entry gives the -timeout, the
// kill line's time less the minute cmd/go adds.
func TestParseKill(t *testing.T) {
	tp := parseFixture(t, "kill.jsonl")
	if want := []string{"./kill [timed out after 2s]"}; !slices.Equal(tp.failed, want) {
		t.Errorf("failed %q, want %q", tp.failed, want)
	}
	if tp.ended["./kill"] != "timed out after 2s" {
		t.Errorf("ended %q", tp.ended)
	}
	if len(tp.failOut) != 1 || !strings.Contains(tp.failOut[0], "*** Test killed: ran too long (1m2s).") {
		t.Errorf("fail output %q", tp.failOut)
	}
	if got := killTimeout("1h1m0s"); got != "1h0m0s" {
		t.Errorf("killTimeout(1h1m0s) = %q", got)
	}
}

// TestParseOtherEnds: a crash with no output, go test's kill line
// with a test running, output that stops before the package's result
// (with and without the alarm), and an alarm with many running tests each
// give one entry per package.
func TestParseOtherEnds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines []string
		want  []string
		ended map[string]string
	}{
		{"crash", []string{
			ev("run", "mk/internal/x", "TestA", ""),
			ev("run", "mk/internal/x", "TestB", ""),
			ev("output", "mk/internal/x", "TestB", "panic: boom"),
			ev("fail", "mk/internal/x", "TestB", ""),
			ev("fail", "mk/internal/x", "", ""),
		}, []string{"./internal/x TestB", "./internal/x [ended mid-run; running TestA; 1 started tests without a result]"},
			map[string]string{"./internal/x": "ended mid-run"}},
		{"killed", []string{
			ev("run", "mk/std", "TestA", ""),
			ev("output", "mk/std", "", "*** Test killed with quit: ran too long (4h1m0s)."),
			ev("fail", "mk/std", "", ""),
		}, []string{"./std [timed out after 4h0m0s; 1 started tests without a result]"},
			map[string]string{"./std": "timed out after 4h0m0s"}},
		{"cut off", []string{
			ev("run", "mk/std", "TestA", ""),
			ev("run", "mk/std", "TestA/sub", ""),
			ev("run", "mk/internal/x", "TestC", ""),
		}, []string{"./internal/x [had no result when go test ended; running TestC; 1 started tests without a result]",
			"./std [had no result when go test ended; running TestA, TestA/sub; 2 started tests without a result]"},
			map[string]string{"./internal/x": "had no result when go test ended", "./std": "had no result when go test ended"}},
		{"cut off after the alarm", []string{
			ev("run", "mk/std", "TestA", ""),
			ev("output", "mk/std", "TestA", "panic: test timed out after 5m0s"),
			ev("output", "mk/std", "TestA", "\trunning tests:"),
			ev("output", "mk/std", "TestA", "\t\tTestA (5m0s)"),
		}, []string{"./std [timed out after 5m0s and had no result when go test ended; running TestA (5m0s); 1 started tests without a result]"},
			map[string]string{"./std": "timed out after 5m0s and had no result when go test ended"}},
		{"many running", []string{
			ev("run", "mk/std", "T1", ""),
			ev("output", "mk/std", "T1", "panic: test timed out after 1h0m0s"),
			ev("output", "mk/std", "T1", "\trunning tests:"),
			ev("output", "mk/std", "T1", "\t\tT1 (1s)"),
			ev("output", "mk/std", "T1", "\t\tT2 (1s)"),
			ev("output", "mk/std", "T1", "\t\tT3 (1s)"),
			ev("output", "mk/std", "T1", "\t\tT4 (1s)"),
			ev("output", "mk/std", "T1", "\t\tT5 (1s)"),
			ev("output", "mk/std", "T1", ""),
			ev("fail", "mk/std", "", ""),
		}, []string{"./std [timed out after 1h0m0s; running T1 (1s), T2 (1s), T3 (1s) and 2 more; 1 started tests without a result]"},
			map[string]string{"./std": "timed out after 1h0m0s"}},
	} {
		tp := parseLines(tc.lines)
		if !slices.Equal(tp.failed, tc.want) {
			t.Errorf("%s: failed %q\nwant %q", tc.name, tp.failed, tc.want)
		}
		if len(tp.ended) != len(tc.ended) {
			t.Errorf("%s: ended %q", tc.name, tp.ended)
		}
		for p, w := range tc.ended {
			if tp.ended[p] != w {
				t.Errorf("%s: ended[%s] %q, want %q", tc.name, p, tp.ended[p], w)
			}
		}
	}
}

// TestReasonCountsEveryRun: a step of two go test runs that both fail
// counts the failures of both, and a timeout in the second leads.
func TestReasonCountsEveryRun(t *testing.T) {
	g := &gate{testResults: map[string]string{}, ended: map[string]pkgEnd{}}
	s := &Step{Name: "test", Status: "pass"}
	g.fold(s, parseLines([]string{
		ev("run", "mk/internal/x", "TestA", ""),
		ev("fail", "mk/internal/x", "TestA", ""),
		ev("fail", "mk/internal/x", "", ""),
	}), 1)
	g.fold(s, parseLines([]string{
		ev("run", "mk/std", "TestB", ""), ev("fail", "mk/std", "TestB", ""),
		ev("run", "mk/std", "TestC", ""), ev("fail", "mk/std", "TestC", ""),
		ev("run", "mk/std", "TestD", ""), ev("fail", "mk/std", "TestD", ""),
		ev("fail", "mk/std", "", ""),
	}), 1)
	if s.Status != "fail" || s.Reason != "4 failing tests or packages" {
		t.Errorf("two failing runs: %s %q", s.Status, s.Reason)
	}

	s = &Step{Name: "test", Status: "pass"}
	g.fold(s, parseLines([]string{
		ev("run", "mk/internal/x", "TestA", ""),
		ev("fail", "mk/internal/x", "TestA", ""),
		ev("fail", "mk/internal/x", "", ""),
	}), 1)
	g.fold(s, parseFixture(t, "std-timeout.jsonl"), 1)
	if s.Reason != "./std timed out after 1h0m0s; 2 failing tests or packages" {
		t.Errorf("a failure, then a timeout: %q", s.Reason)
	}
	if g.ended["./std"].why != "timed out after 1h0m0s in the test step" || g.ended["./std"].linux {
		t.Errorf("gate ended %+v", g.ended)
	}

	// A reason set before fold (the tripwire) stays first.
	s = &Step{Name: "test", Status: "fail", Reason: "wsl.exe started while MK_LINUX=1 was not set (tripwire)"}
	g.fold(s, parseFixture(t, "std-timeout.jsonl"), 1)
	if !strings.HasPrefix(s.Reason, "wsl.exe started") || !strings.Contains(s.Reason, "; ./std timed out after 1h0m0s") {
		t.Errorf("tripwire, then a timeout: %q", s.Reason)
	}
}

// TestCancelSaysNotRun: over the recorded fragment's results, the
// cancel-safety step says its tests were not run because ./std timed out,
// never "no such test"; a listed failure stays after that reason.
func TestCancelSaysNotRun(t *testing.T) {
	g := &gate{testResults: map[string]string{}, ended: map[string]pkgEnd{}}
	g.fold(&Step{Name: "test", Status: "pass"}, parseFixture(t, "std-timeout.jsonl"), 1)
	all := func(string) bool { return true }
	c := &Step{Status: "pass"}
	judgeCancel(c, false, all, g.testResults, g.ended)
	want := "38 of 39 listed tests not run because ./std timed out after 1h0m0s in the test step"
	if c.Status != "fail" || c.Reason != want {
		t.Errorf("cancel step: %s %q\nwant %q", c.Status, c.Reason, want)
	}
	if strings.Contains(c.Reason, "no such test") || len(c.Failed) != 38 {
		t.Errorf("cancel step: %q, %d failed", c.Reason, len(c.Failed))
	}
	if len(cancelSafety) != 39 {
		t.Fatalf("cancelSafety has %d rows: update the counts above", len(cancelSafety))
	}

	res := map[string]string{"./std TestCore_CancelClasses": "fail"}
	c = &Step{Status: "pass"}
	judgeCancel(c, false, all, res, g.ended)
	if !strings.HasPrefix(c.Reason, "38 of 39 listed tests not run because ./std timed out") ||
		!strings.HasSuffix(c.Reason, "; ./std TestCore_CancelClasses failed (the test step has its output)") {
		t.Errorf("cancel step with a failure: %q", c.Reason)
	}

	c = &Step{Status: "pass"}
	judgeCancel(c, false, all, map[string]string{}, nil)
	if !strings.Contains(c.Reason, "no such test") {
		t.Errorf("no ended package: %q", c.Reason)
	}
}

// TestCancelAfterLinuxTimeout: a Linux leaf that kept the test
// step's skip because ./std timed out in the Linux step is not run because
// of that timeout, in the quick tier and the full one; a skip with no
// Linux-step end is still the quick tier's note.
func TestCancelAfterLinuxTimeout(t *testing.T) {
	g := &gate{testResults: map[string]string{}, ended: map[string]pkgEnd{}}
	res := map[string]string{}
	var leaf string
	for _, c := range cancelSafety {
		res[c.pkg+" "+c.test] = "pass"
		if c.linux && leaf == "" {
			leaf = c.pkg + " " + c.test
		}
	}
	res[leaf] = "skip"
	all := func(string) bool { return true }
	c := &Step{Status: "pass"}
	judgeCancel(c, false, all, res, g.ended)
	if c.Status != "pass" {
		t.Errorf("quick, no Linux end: %s %q", c.Status, c.Reason)
	}

	g.fold(&Step{Name: linuxStepName, Status: "pass"}, parseFixture(t, "std-timeout.jsonl"), 1)
	if !g.ended["./std"].linux {
		t.Fatalf("a Linux-step end is not marked: %+v", g.ended)
	}
	want := "1 of 39 listed tests not run because ./std timed out after 1h0m0s in the " + linuxStepName + " step"
	for _, strict := range []bool{false, true} {
		c := &Step{Status: "pass"}
		judgeCancel(c, strict, all, res, g.ended)
		if c.Status != "fail" || c.Reason != want || !slices.Equal(c.Failed, []string{leaf}) {
			t.Errorf("strict %v: %s %q %q\nwant %q", strict, c.Status, c.Reason, c.Failed, want)
		}
	}
}

// TestExecRecordsEveryCommand: a step of several commands keeps each
// command line, the first log in Log and every log in Logs, its
// environment once, and the first non-zero exit status.
func TestExecRecordsEveryCommand(t *testing.T) {
	codes := []int{2, 0}
	g, _ := fakeGate(t, "quick", func([]string) (string, int) {
		c := codes[0]
		codes = codes[1:]
		return "", c
	})
	s := &Step{Name: "test", Status: "pass"}
	g.exec(s, spec{argv: []string{"go", "no-such-command"}, set: []string{"MK_GATE=1"}}, func(string) {})
	g.exec(s, spec{argv: []string{"go", "version"}, set: []string{"MK_GATE=1"}}, func(string) {})
	if s.Command != "go no-such-command ; go version" {
		t.Errorf("command %q", s.Command)
	}
	if len(s.Logs) != 2 || s.Log != s.Logs[0] || s.Logs[0] == s.Logs[1] {
		t.Errorf("log %q logs %q", s.Log, s.Logs)
	}
	if n := strings.Count(strings.Join(s.Env, "\n"), "MK_GATE=1"); n != 1 {
		t.Errorf("env %q", s.Env)
	}
	if s.ExitCode != 2 {
		t.Errorf("exit code %d, want the first command's 2", s.ExitCode)
	}
}
