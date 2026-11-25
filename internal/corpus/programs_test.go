// Why: the end-to-end programs are the proof that built executables behave
// the same on every target and profile.
// What: TestPrograms, TestPanics, the executable-format tests
// (TestExeImportsOnlyKernel32, TestELFStatic) and the WSL guards and probes.
// How: each program is built once per target and profile, and each case of
// its .cases file is run; stdout, stderr and the exit status are compared
// byte for byte.
//
// Strictness: under MK_GATE=1 every program is judged strictly, since the
// gate runs every .cases file. In a plain run the programs strictProgram
// names fail on a mismatch, and the others are logged until they pass. A
// strict leaf whose only difference is an open item elsewhere is held by
// knownProgramMismatches (logged as BLOCKED; fails under MK_GATE=1).
//
// Skips: a build that stops only on a std module std does not provide yet,
// a missing LLVM, a WSL that does not answer, and a target with no runner
// skip in a plain run and fail under MK_GATE=1. On a Windows host without
// MK_LINUX=1 an x86_64-linux case skips before its build unless MK_SLOW=1
// is set (the nightly gate sets it).

package corpus

import (
	"bytes"
	"context"
	"debug/elf"
	"debug/pe"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"mk/internal/diag"
	"mk/internal/driver"
	"mk/internal/toolchain"
)

// coreGateProgram reports whether program is one of the first, core gate
// programs: fizz, hello and the panic programs.
func coreGateProgram(program string) bool {
	switch program {
	case ProgramsDir + "/fizz.mk", ProgramsDir + "/hello.mk":
		return true
	}
	return strings.HasPrefix(program, ProgramsDir+"/panics/")
}

// blockedBuild returns std's reason when every diagnostic of a BuildError
// is an E0202 on a std module the embedded std does not provide yet (reason
// not-implemented), else "". The judge's rule 7 holds such a unit
// PENDING std:. Any other diagnostic, an E0000 included, is a failure.
func blockedBuild(err error) string {
	var be *BuildError
	if !errors.As(err, &be) || len(be.Result.Report.Diags) == 0 {
		return ""
	}
	var owners []string
	for _, e := range be.Result.Report.Diags {
		if e.Diag.Code != diag.E0202 || !strings.Contains(e.Diag.Message, "does not provide it yet") {
			return ""
		}
		if found := "std: " + e.Diag.Message; !slices.Contains(owners, found) {
			owners = append(owners, found)
		}
	}
	return strings.Join(owners, "; ")
}

func needLLVM(t testing.TB) *toolchain.Tools {
	t.Helper()
	tools, err := toolchain.Discover(toolchain.Options{})
	if err != nil {
		if gateRun() {
			t.Fatalf("needs LLVM: %v", err)
		}
		t.Skipf("needs LLVM: %v", err)
	}
	return tools
}

// wslAnswer is the one liveness probe of WSL's default distro (the one
// runArgv's wsl.exe runs use) for this test binary: "" when it answered,
// else why not. Without it a hung Ubuntu made every x86_64-linux case wait
// out its 10 s timeout, about 140 of them, 922.7 s for this package (one
// run of this package on a Windows development machine); with the probe a
// hung distro costs one wait and one message per leaf.
var wslAnswer = sync.OnceValue(func() string {
	wslGuard()
	return probeWSL(90*time.Second, func(ctx context.Context) error {
		return exec.CommandContext(ctx, "wsl.exe", "-e", "true").Run()
	})
})

// probeWSL runs probe with timeout and says why WSL did not answer, or "".
func probeWSL(timeout time.Duration, probe func(ctx context.Context) error) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := probe(ctx)
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return fmt.Sprintf("wsl.exe -e true did not answer within %v", timeout)
	case err != nil:
		return "wsl.exe -e true: " + err.Error()
	}
	return ""
}

// linuxOff is the skip reason of a leaf that would start wsl.exe while
// MK_LINUX=1 is not set. It is a plain skip, also under MK_GATE=1: the gate
// runs the Linux leaves in their own pass, under the WSL lock.
const linuxOff = "MK_LINUX=1 not set"

// linuxRun reports whether this run may start wsl.exe, MK_LINUX=1.
func linuxRun() bool { return os.Getenv("MK_LINUX") == "1" }

// wslGuard panics when a wsl.exe is about to start without MK_LINUX=1: a
// leaf that reaches it missed its linuxRun check.
//
// With MK_LINUX=1 it also panics unless the run holds the WSL lock
// (tools/wsl-lock.ps1 sets MK_WSL_LOCKED=1), so a Linux run outside the
// lock cannot happen by accident; MK_WSL_UNLOCKED_OK=1 allows one on
// purpose.
func wslGuard() {
	if !linuxRun() {
		panic("wsl.exe started without MK_LINUX=1; the leaf must skip with " + linuxOff)
	}
	if os.Getenv("MK_WSL_LOCKED") != "1" && os.Getenv("MK_WSL_UNLOCKED_OK") != "1" {
		panic("wsl.exe started with MK_LINUX=1 outside the WSL lock (MK_WSL_LOCKED is not 1): run under pwsh -NoProfile -File tools/wsl-lock.ps1, or set MK_WSL_UNLOCKED_OK=1")
	}
}

// needWSLRun skips a leaf that runs an x86_64-linux executable through
// wsl.exe when WSL does not answer; under MK_GATE=1 a strict leaf (one whose
// run the gate judges) fails instead, so the gate still fails. Callers build
// first, so a build verdict never waits on WSL. Any other target, or a Linux
// host, needs nothing.
func needWSLRun(t *testing.T, target string, strict bool) {
	t.Helper()
	if target != "x86_64-linux" || runtime.GOOS != "windows" {
		return
	}
	if !linuxRun() {
		t.Skip(linuxOff)
	}
	if why := wslAnswer(); why != "" {
		if strict && gateRun() {
			t.Fatalf("needs WSL: %s", why)
		}
		t.Skipf("needs WSL: %s", why)
	}
}

// testWSLSlots is the one bound on wsl.exe runs in flight for every runner
// of this test binary: in one run of this package on a Windows development
// machine, one wsl.exe run alone took 0.1 s, but with 45 started at once
// most of them, and TestPanics' Linux cases beside them, passed the 10 s
// case timeout. Its capacity is 1: one wsl.exe client at a time, the rule
// the WSL lock keeps between processes (parallel WSL clients can hang WSL),
// kept inside this one too. It is a semaphore, never reassigned.
var testWSLSlots = make(chan struct{}, 1)

// testRunner is a runner for root with tools, its own cache and executable
// directories, and testWSLSlots.
func testRunner(t *testing.T, root string, tools *toolchain.Tools) *Runner {
	r := NewRunner(root, driver.Config{Tools: tools, CacheDir: t.TempDir()}, t.TempDir())
	r.WSLSlots = testWSLSlots
	return r
}

// programsRunner is the one runner of TestPrograms, TestPanics and the
// executable-format tests, so std is compiled once per target and profile
// for all of them, and the format tests read the executables the program
// tests built (a Runner builds each program, target and profile once). Its
// cache and executables are under programsDir, which TestMain removes.
var (
	programsOnce   sync.Once
	programsShared *Runner
	programsDir    string
	programsErr    error
)

// sharedRunner returns programsRunner, made on the first call with root
// and tools.
func sharedRunner(t *testing.T, root string, tools *toolchain.Tools) *Runner {
	t.Helper()
	programsOnce.Do(func() {
		programsDir, programsErr = os.MkdirTemp("", "mk-corpus-programs-")
		if programsErr != nil {
			return
		}
		cache, exes := filepath.Join(programsDir, "cache"), filepath.Join(programsDir, "exe")
		for _, d := range []string{cache, exes} {
			if programsErr = os.MkdirAll(d, 0o755); programsErr != nil {
				return
			}
		}
		programsShared = NewRunner(root, driver.Config{Tools: tools, CacheDir: cache}, exes)
		programsShared.WSLSlots = testWSLSlots
	})
	if programsErr != nil {
		t.Fatal(programsErr)
	}
	return programsShared
}

// programsBuilds bounds the builds of sharedRunner's leaves in flight: a
// release build links with ThinLTO on every core, so 48 of them at once
// only contend for the threads and the memory. It is a semaphore, never
// reassigned.
var programsBuilds = make(chan struct{}, 4)

// buildBounded is r.Build under programsBuilds.
func buildBounded(r *Runner, program, target, profile string) (string, error) {
	programsBuilds <- struct{}{}
	defer func() { <-programsBuilds }()
	return r.Build(program, target, profile)
}

func TestMain(m *testing.M) {
	code := m.Run()
	if programsDir != "" {
		os.RemoveAll(programsDir)
	}
	os.Exit(code)
}

// slowLinuxBuilds reports whether MK_SLOW=1 is set: then an x86_64-linux
// case is built on a Windows host even when it cannot run there
// (MK_LINUX=1 not set). The nightly gate sets it; a plain run leaves those
// builds to the Linux pass, which builds and runs them.
func slowLinuxBuilds() bool { return os.Getenv("MK_SLOW") == "1" }

// TestPrograms is every program outside panics/ (judgedStrictly says which
// mismatches fail).
func TestPrograms(t *testing.T) {
	runPrograms(t, func(p string) bool { return !strings.HasPrefix(p, ProgramsDir+"/panics/") })
}

// TestPanics: every panics/ program, built in dev and release and run on
// both targets, exits 101 with exactly its report on stderr (strict).
func TestPanics(t *testing.T) {
	runPrograms(t, func(p string) bool { return strings.HasPrefix(p, ProgramsDir+"/panics/") })
}

// runPrograms runs every case of the programs keep accepts.
func runPrograms(t *testing.T, keep func(program string) bool) {
	t.Helper()
	root := repoRoot(t)
	cases, err := LoadCases(root)
	if err != nil {
		t.Fatal(err)
	}
	tools := needLLVM(t)
	run := sharedRunner(t, root, tools)
	byProgram := map[string][]ProgramCase{}
	var programs []string
	for _, c := range cases {
		if !keep(c.Program) {
			continue
		}
		if byProgram[c.Program] == nil {
			programs = append(programs, c.Program)
		}
		byProgram[c.Program] = append(byProgram[c.Program], c)
	}
	for _, p := range programs {
		t.Run(strings.TrimPrefix(p, ProgramsDir+"/"), func(t *testing.T) {
			t.Parallel()
			for _, c := range byProgram[p] {
				for _, target := range c.Targets {
					for _, prof := range c.Profiles {
						t.Run(fmt.Sprintf("%s/%s/%s", c.Name, target, prof), func(t *testing.T) {
							checkProgramCase(t, run, c, target, prof)
						})
					}
				}
			}
		})
	}
}

// strictProgram reports whether a mismatch of program fails TestPrograms
// in a plain run: the core gate programs (coreGateProgram), and the programs
// that have passed on both targets and both profiles (tasks/, pgo/, wc,
// report, copy, echo and the ones listed below). A program added later is
// logged in a plain run until it is named here, and strict under
// MK_GATE=1 from the start (judgedStrictly).
func strictProgram(program string) bool {
	switch program {
	case ProgramsDir + "/wc.mk", ProgramsDir + "/report.mk", ProgramsDir + "/copy.mk", ProgramsDir + "/echo.mk":
		return true
	// Each passed its Windows dev and release leaves and, where it has
	// one, its Linux leaf under the WSL lock before it was named here.
	case ProgramsDir + "/files", ProgramsDir + "/sockets.mk", ProgramsDir + "/children.mk",
		ProgramsDir + "/timezones.mk", ProgramsDir + "/terminal.mk", ProgramsDir + "/httploop":
		return true
	// cseh (a Windows-only case), aliasdiff and aliaschain: each passed
	// every leaf it has under MK_GATE=1, the Linux ones under the WSL
	// lock, before it was named here.
	case ProgramsDir + "/cseh", ProgramsDir + "/aliasdiff", ProgramsDir + "/aliaschain":
		return true
	// Windows only, so they have no Linux leaf; both Windows leaves of
	// each passed before it was named here, and failed without its fix.
	case ProgramsDir + "/alignframe.mk", ProgramsDir + "/staticalign":
		return true
	// Windows only (MKH-493): both Windows leaves of each passed before it
	// was named here, and its filter cases failed without the fix.
	case ProgramsDir + "/hostfault", ProgramsDir + "/hostfaultmain":
		return true
	// Each but linuxsyscall passed its Windows dev and release leaves
	// under MK_GATE=1, its output byte-identical at MK_CARRIERS=1 and 16,
	// before it was named here. Their Linux leaves (linuxsyscall's are its
	// only ones, and its .out is written by hand) run in the separate
	// Linux pass.
	case ProgramsDir + "/trysend.mk", ProgramsDir + "/floatprefix.mk", ProgramsDir + "/wakeword.mk",
		ProgramsDir + "/coderegion", ProgramsDir + "/stackquery.mk", ProgramsDir + "/blockingcancel.mk",
		ProgramsDir + "/threads.mk", ProgramsDir + "/tailchain.mk", ProgramsDir + "/linuxsyscall":
		return true
	// Its dev leaf checks Main's error-return trace in a program with no
	// task.
	case ProgramsDir + "/tracenotask.mk":
		return true
	}
	return coreGateProgram(program) || strings.HasPrefix(program, ProgramsDir+"/tasks/") ||
		strings.HasPrefix(program, ProgramsDir+"/pgo/")
}

// judgedStrictly reports whether a mismatch of program fails its leaf:
// under MK_GATE=1 (gate) every program's does (the gate runs every .cases
// file), otherwise those strictProgram names.
func judgedStrictly(program string, gate bool) bool {
	return gate || strictProgram(program)
}

// skipOrFatal is how a leaf that cannot be judged ends: under MK_GATE=1 it
// fails, since the gate run fails if any such test skipped; in a plain run
// it skips. Every program is in scope, so a std module "not provided yet"
// is a gate failure too.
func skipOrFatal(t *testing.T, gate bool, format string, args ...any) {
	t.Helper()
	if gate {
		t.Fatalf(format, args...)
	}
	t.Skipf(format, args...)
}

// checkProgramCase runs one case and compares it; the mismatch of a
// program judgedStrictly does not name is logged.
func checkProgramCase(t *testing.T, r *Runner, c ProgramCase, target, prof string) {
	t.Helper()
	gate := gateRun()
	strict := judgedStrictly(c.Program, gate)
	report := t.Errorf
	if !strict {
		report = t.Logf
	}
	// No build for a target this host cannot run (x86_64-windows on Linux):
	// it checks nothing here and costs memory.
	if _, err := runArgv(target, r.dir, "x", nil, 0); ErrNoRunner(err) {
		skipOrFatal(t, gate, "%v", err)
	}
	// An x86_64-linux case on a Windows host without MK_LINUX=1 is not
	// run, so it is built only under MK_SLOW=1; the Linux pass, which runs
	// it, builds it (the executable-format test TestELFStatic still builds
	// the core gate programs for x86_64-linux in every run).
	if target == "x86_64-linux" && runtime.GOOS == "windows" && !linuxRun() && !slowLinuxBuilds() {
		t.Skip(linuxOff)
	}
	if _, err := buildBounded(r, c.Program, target, prof); blockedBuild(err) != "" {
		skipOrFatal(t, gate, "BLOCKED: %s", blockedBuild(err))
	}
	needWSLRun(t, target, strict)
	got, err := r.Run(c, target, prof)
	switch {
	case err == nil:
	case blockedBuild(err) != "":
		skipOrFatal(t, gate, "BLOCKED: %s", blockedBuild(err))
	case ErrNoRunner(err):
		if gateRun() {
			t.Fatal(err)
		}
		t.Skip(err)
	default:
		report("%s (%s): %v", c.At, path.Base(c.Program), err)
		return
	}
	wantOut, wantErr, err := r.Expected(c)
	if err != nil {
		t.Fatal(err)
	}
	if got.TimedOut {
		report("%s: timed out after %v", c.At, c.Timeout)
		return
	}
	if got.Exit != c.Exit {
		report("%s: exit %d, want %d (stderr %q)", c.At, got.Exit, c.Exit, got.Stderr)
	}
	if !bytes.Equal(got.Stdout, wantOut) {
		report("%s: stdout %q, want %q", c.At, got.Stdout, wantOut)
	}
	known := knownMismatchOf(c)
	isKnown := known != nil && string(got.Stderr) == known.got && string(wantErr) == known.want
	switch {
	case isKnown && !gateRun():
		t.Logf("BLOCKED on %s: %s: stderr %q, want %q", known.owner, c.At, got.Stderr, wantErr)
	case isKnown:
		report("%s: stderr %q, want %q (known: %s)", c.At, got.Stderr, wantErr, known.owner)
	case !bytes.Equal(got.Stderr, wantErr):
		report("%s: stderr %q, want %q", c.At, got.Stderr, wantErr)
	case known != nil:
		report("%s: prints its stderr file now: remove it from knownProgramMismatches", c.At)
	}
}

// knownMismatch is a strict leaf whose stderr differs from its file only by
// an open item elsewhere: the exact bytes printed and wanted, and the
// package that owns the fix. A plain run logs such a leaf as
// BLOCKED; MK_GATE=1 fails it, as it fails any strict mismatch; any other
// stderr still fails; and a leaf that prints its file fails with "remove
// it", so the list only shrinks (the knownFuzzCrashers idiom).
type knownMismatch struct {
	program, name string // ProgramCase.Program and .Name
	got, want     string
	owner         string
}

// knownProgramMismatches is empty: spawn_huge's 29:18 entry was removed
// once lower gave the spawn terminator the GoExpr's span and every
// default-slot leaf printed 29:13 on both targets and profiles.
var knownProgramMismatches = []knownMismatch{}

// knownMismatchOf is c's knownProgramMismatches entry, or nil.
func knownMismatchOf(c ProgramCase) *knownMismatch {
	for i := range knownProgramMismatches {
		if k := &knownProgramMismatches[i]; k.program == c.Program && k.name == c.Name {
			return k
		}
	}
	return nil
}

// Every knownProgramMismatches entry names a case of a strict program whose
// stderr file holds the entry's want, so an entry cannot outlive its case
// or excuse a logged program.
func TestKnownProgramMismatchesAreCurrent(t *testing.T) {
	root := repoRoot(t)
	cases, err := LoadCases(root)
	if err != nil {
		t.Fatal(err)
	}
	r := NewRunner(root, driver.Config{}, t.TempDir())
	for _, k := range knownProgramMismatches {
		i := slices.IndexFunc(cases, func(c ProgramCase) bool { return c.Program == k.program && c.Name == k.name })
		if i < 0 {
			t.Errorf("%s case %s: no such case: remove it from knownProgramMismatches", k.program, k.name)
			continue
		}
		if !strictProgram(k.program) {
			t.Errorf("%s: not a strict program; a logged program needs no entry", k.program)
		}
		if _, wantErr, err := r.Expected(cases[i]); err != nil || string(wantErr) != k.want {
			t.Errorf("%s: stderr file %q (%v), entry wants %q", cases[i].At, wantErr, err, k.want)
		}
		if k.got == k.want || k.owner == "" {
			t.Errorf("%s case %s: an entry needs a differing got and an owner", k.program, k.name)
		}
	}
}

// runArgv's host table, without running anything.
func TestRunArgv(t *testing.T) {
	if _, err := runArgv("aarch64-macos", "/r", "/r/x", nil, time.Second); !ErrNoRunner(err) {
		t.Errorf("aarch64-macos: %v, want no runner", err)
	}
	host := driver.HostTarget().Name
	argv, err := runArgv(host, "/r", "/r/x", []string{"a b", "c"}, 10*time.Second)
	if err != nil || strings.Join(argv, "|") != "/r/x|a b|c" {
		t.Errorf("host %s: %q %v", host, argv, err)
	}
	if runtime.GOOS != "windows" {
		return
	}
	if _, err := exec.LookPath("wsl.exe"); err != nil {
		t.Logf("x86_64-linux rows not checked: no wsl.exe on PATH (%v)", err)
		return
	}
	// The Linux run is bounded inside the VM, the limit
	// rounded up to whole seconds; no limit, no timeout command.
	for _, c := range []struct {
		limit time.Duration
		want  string
	}{
		{10 * time.Second, `wsl.exe|--cd|C:\r|--|timeout|-s|KILL|10|./x|a b`},
		{1500 * time.Millisecond, `wsl.exe|--cd|C:\r|--|timeout|-s|KILL|2|./x|a b`},
		{0, `wsl.exe|--cd|C:\r|--|./x|a b`},
	} {
		argv, err := runArgv("x86_64-linux", `C:\r`, `C:\r\x`, []string{"a b"}, c.limit)
		if err != nil || strings.Join(argv, "|") != c.want {
			t.Errorf("x86_64-linux, limit %v: %q %v, want %s", c.limit, argv, err, c.want)
		}
	}
}

// wslKilled takes timeout's statuses for a timeout only once the run
// lasted its limit.
func TestWSLKilled(t *testing.T) {
	for _, c := range []struct {
		exit     int
		ran, lim time.Duration
		timedOut bool
	}{
		{137, 2 * time.Second, 2 * time.Second, true},
		{124, 3 * time.Second, 2 * time.Second, true},
		{137, time.Second, 2 * time.Second, false},
		{1, 5 * time.Second, 2 * time.Second, false},
		{101, 5 * time.Second, 2 * time.Second, false},
	} {
		if got := wslKilled(c.exit, c.ran, c.lim); got != c.timedOut {
			t.Errorf("wslKilled(%d, %v, %v) = %v, want %v", c.exit, c.ran, c.lim, got, c.timedOut)
		}
	}
}

// builtCoreGatePrograms builds every core gate program (coreGateProgram)
// for target in dev and release and calls check on each executable, the
// leaves in parallel. The runner is sharedRunner, so an executable
// TestPrograms or TestPanics built is read, not built again. A build that
// stops on a std module std does not provide yet skips that program, and
// fails under MK_GATE=1 (skipOrFatal).
func builtCoreGatePrograms(t *testing.T, target string, check func(t *testing.T, exe string)) {
	t.Helper()
	root := repoRoot(t)
	cases, err := LoadCases(root)
	if err != nil {
		t.Fatal(err)
	}
	tools := needLLVM(t)
	run := sharedRunner(t, root, tools)
	seen := map[string]bool{}
	for _, c := range cases {
		if !coreGateProgram(c.Program) || seen[c.Program] {
			continue
		}
		seen[c.Program] = true
		for _, prof := range []string{"dev", "release"} {
			t.Run(strings.TrimPrefix(c.Program, ProgramsDir+"/")+"/"+prof, func(t *testing.T) {
				t.Parallel() // each leaf builds its own program and profile, and only reads it
				exe, err := buildBounded(run, c.Program, target, prof)
				switch {
				case err == nil:
					check(t, exe)
				case blockedBuild(err) != "":
					skipOrFatal(t, gateRun(), "BLOCKED: %s", blockedBuild(err))
				default:
					t.Error(err)
				}
			})
		}
	}
}

// A Windows executable imports kernel32.dll only (ws2_32.dll only for a
// program that imports std.net, and no core gate program does).
func TestExeImportsOnlyKernel32(t *testing.T) {
	builtCoreGatePrograms(t, "x86_64-windows", func(t *testing.T, exe string) {
		f, err := pe.Open(exe)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		libs, err := f.ImportedLibraries()
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range libs {
			if !strings.EqualFold(l, "kernel32.dll") {
				t.Errorf("%s imports %s; want kernel32.dll only (%q)", exe, l, libs)
			}
		}
	})
}

// A Linux executable is a static ELF: ET_EXEC, no
// PT_INTERP, no PT_DYNAMIC.
func TestELFStatic(t *testing.T) {
	builtCoreGatePrograms(t, "x86_64-linux", func(t *testing.T, exe string) {
		f, err := elf.Open(exe)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if f.Type != elf.ET_EXEC {
			t.Errorf("%s: type %v, want ET_EXEC", exe, f.Type)
		}
		for _, p := range f.Progs {
			if p.Type == elf.PT_INTERP || p.Type == elf.PT_DYNAMIC {
				t.Errorf("%s: has %v", exe, p.Type)
			}
		}
	})
}

// probeWSL's two answers, without WSL: a probe that blocks past the
// timeout returns the "did not answer" reason once the timeout passes (the
// hung-distro case, which needWSLRun turns into one skip, or
// one fatal under MK_GATE=1, per leaf instead of a 10 s wait per case), and
// a probe that answers returns "".
func TestProbeWSL(t *testing.T) {
	start := time.Now()
	why := probeWSL(50*time.Millisecond, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if !strings.Contains(why, "did not answer within 50ms") {
		t.Errorf("blocking probe: %q, want a did-not-answer reason", why)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("blocking probe returned after %v, want about its 50ms timeout", d)
	}
	if why := probeWSL(time.Minute, func(context.Context) error { return nil }); why != "" {
		t.Errorf("answering probe: %q, want \"\"", why)
	}
	if why := probeWSL(time.Minute, func(context.Context) error { return errors.New("exit status 1") }); why != "wsl.exe -e true: exit status 1" {
		t.Errorf("failing probe: %q", why)
	}
}
