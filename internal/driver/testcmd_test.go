// Why: mk test runs each test block in its own child process, so a panic,
// a hang or a leak in one test cannot take down the others or the report.
// What: tests of the runner (child processes, --json, --timeout, package
// modules, no tests, leaks, Linux children, a fresh arena per test) and of
// mk test --std over std's own blocks.
// How: builds small packages with test blocks, runs mk test, and compares
// the per-test results, which come in test order.

package driver

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mk/internal/types"
	"mk/std"
)

// testPkg writes files into a directory and returns it.
func testPkg(t *testing.T, files map[string]string) string {
	t.Helper()
	return writeTree(t, files)
}

// mkTest runs Session.Test over root and fails on compile diagnostics.
func mkTest(t *testing.T, cfg Config, opt TestOptions) *TestReport {
	t.Helper()
	cfg.Tools = needTools(t)
	if cfg.CacheDir == "" {
		cfg.CacheDir = t.TempDir()
	}
	if cfg.TestTimeout == 0 {
		cfg.TestTimeout = time.Minute
	}
	s, err := NewSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r, rep, err := s.Test(opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Report.Diags) > 0 || rep == nil {
		t.Fatalf("mk test did not build:\n%s", r.Report.Text(r.Sources))
	}
	return rep
}

func results(rep *TestReport) string {
	var b []string
	for _, tc := range rep.Tests {
		b = append(b, tc.Module+" "+tc.Name+" "+tc.Result)
	}
	return strings.Join(b, "\n")
}

const testsSource = `error Bad

Fails(): Bad!void {
    fail Bad
}

test "adds" {
    Assert(1 + 2 == 3)
}

test "panics" {
    Assert(1 + 2 == 4)
}

test "returns an error" {
    try Fails()
}

test "frees what it allocates" (sys: &System) {
    var xs := HeapList[int].New(sys.heap.Alloc())
    defer xs.Free(sys.heap.Alloc())
    try xs.Push(sys.heap.Alloc(), 1)
    try xs.Push(sys.heap.Alloc(), 2)
    Assert(xs.Len() == 2)
}

test "prints" {
    Assert(true)
}
`

// TestMkTestChildProcesses: a panicking test fails alone and the others
// pass; an error return is reported error (exit 1, its name printed); a
// test that allocates and frees through sys.heap passes (no false leak).
// TestMkTestErrorReturnFails and TestMkTestOrderIsDeclarationOrder are
// subtests of the same build.
func TestMkTestChildProcesses(t *testing.T) {
	dir := testPkg(t, map[string]string{"t.mk": testsSource})
	rep := mkTest(t, Config{Root: filepath.Join(dir, "t.mk")}, TestOptions{})
	want := "main adds pass\nmain panics panic\nmain returns an error error\nmain frees what it allocates pass\nmain prints pass"
	if got := results(rep); got != want {
		t.Fatalf("results:\n%s\nwant:\n%s\noutputs: %+v", got, want, rep.Tests)
	}
	if rep.Passed != 3 || rep.Failed != 2 || rep.ExitStatus() != 1 {
		t.Errorf("passed %d failed %d exit %d", rep.Passed, rep.Failed, rep.ExitStatus())
	}
	if p := rep.Tests[1]; p.Status != 101 || !strings.Contains(p.Output, "panic") {
		t.Errorf("panic test: status %d output %q", p.Status, p.Output)
	}
	if e := rep.Tests[2]; e.Status != 1 || !strings.Contains(e.Output, "Bad") {
		t.Errorf("error test: status %d output %q", e.Status, e.Output)
	}
	var text bytes.Buffer
	if err := rep.WriteText(&text, false); err != nil {
		t.Fatal(err)
	}
	tx := text.String()
	for _, l := range []string{`PASS main "adds"`, `FAIL main "panics": panic (exit 101)`, `FAIL main "returns an error": error return (exit 1)`, "3 passed, 2 failed\n"} {
		if !strings.Contains(tx, l) {
			t.Errorf("text report lacks %q:\n%s", l, tx)
		}
	}
	t.Run("RunSelects", func(t *testing.T) {
		rep := mkTest(t, Config{Root: filepath.Join(dir, "t.mk")}, TestOptions{Run: "an"})
		if got := results(rep); got != "main panics panic\nmain returns an error error" {
			t.Errorf("-run an:\n%s", got)
		}
	})
}

// TestMkTestJSON: the report is one JSON object: tests, and per test its
// module, name, result, status and output, plus the Passed and Failed counts.
func TestMkTestJSON(t *testing.T) {
	dir := testPkg(t, map[string]string{"t.mk": "test \"one\" {\n    Assert(true)\n}\n\ntest \"two\" {\n    Assert(false)\n}\n"})
	rep := mkTest(t, Config{Root: filepath.Join(dir, "t.mk")}, TestOptions{})
	var b bytes.Buffer
	if err := rep.WriteJSON(&b); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Tests          []map[string]any `json:"tests"`
		Passed, Failed int
	}
	if err := json.Unmarshal(b.Bytes(), &doc); err != nil {
		t.Fatalf("%v: %s", err, b.String())
	}
	if doc.Passed != 1 || doc.Failed != 1 || len(doc.Tests) != 2 || doc.Tests[0]["result"] != "pass" || doc.Tests[1]["result"] != "panic" ||
		doc.Tests[1]["status"] != float64(101) || doc.Tests[0]["module"] != "main" || doc.Tests[1]["name"] != "two" {
		t.Errorf("json: %s", b.String())
	}
	if !strings.HasPrefix(b.String(), `{"tests":[{"module":"main","name":"one","result":"pass","status":0,"output":""}`) {
		t.Errorf("key order: %s", b.String())
	}
}

// TestMkTestChildTimeout: a test that never returns is killed
// after --timeout and reported timeout; the other tests still run.
func TestMkTestChildTimeout(t *testing.T) {
	src := "test \"spins\" {\n    var n := 0\n    for {\n        n = n +% 1\n    }\n}\n\ntest \"returns\" {\n    Assert(true)\n}\n"
	dir := testPkg(t, map[string]string{"t.mk": src})
	start := time.Now()
	rep := mkTest(t, Config{Root: filepath.Join(dir, "t.mk"), TestTimeout: 2 * time.Second}, TestOptions{})
	if got := results(rep); got != "main spins timeout\nmain returns pass" {
		t.Fatalf("results:\n%s", got)
	}
	if took := time.Since(start); took > 50*time.Second {
		t.Errorf("mk test took %v with a 2 s timeout", took)
	}
	var text bytes.Buffer
	_ = rep.WriteText(&text, false)
	if !strings.Contains(text.String(), `FAIL main "spins": timeout after 2s`) {
		t.Errorf("text:\n%s", text.String())
	}
}

// TestMkTestPackageModules: tests of several modules run in module path
// order, then declaration order, and a package needs no Main for mk test.
func TestMkTestPackageModules(t *testing.T) {
	dir := testPkg(t, map[string]string{
		"pkg.mk":       manifest(nil),
		"main/main.mk": "import lib\n\nexport Main(sys: &System) {\n}\n\ntest \"main first\" {\n    Assert(lib.Two() == 2)\n}\n",
		"lib/lib.mk":   "export Two(): int {\n    return 2\n}\n\ntest \"lib b\" {\n    Assert(Two() == 2)\n}\n\ntest \"lib a\" {\n    Assert(Two() != 2)\n}\n",
	})
	rep := mkTest(t, Config{Root: dir}, TestOptions{})
	if got := results(rep); got != "lib lib b pass\nlib lib a panic\nmain main first pass" {
		t.Errorf("results:\n%s", got)
	}
}

// TestMkTestNoTests: a file without test blocks reports 0 passed, 0 failed
// and exits 0, and builds nothing.
func TestMkTestNoTests(t *testing.T) {
	dir := testPkg(t, map[string]string{"t.mk": "F(): int {\n    return 1\n}\n"})
	rep := mkTest(t, Config{Root: filepath.Join(dir, "t.mk")}, TestOptions{})
	var text bytes.Buffer
	_ = rep.WriteText(&text, false)
	if text.String() != "0 passed, 0 failed\n" || rep.ExitStatus() != 0 {
		t.Errorf("text %q exit %d", text.String(), rep.ExitStatus())
	}
}

var _ = os.Getenv
var _ = types.TestProfile

// stdWithLeak writes the embedded std to a directory and adds to std.fs an
// exported function that allocates 16 bytes through an Alloc and never
// frees them: safe Mk cannot leak (linear containers), so the leak test
// needs std's own allocator call.
func stdWithLeak(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	err := fs.WalkDir(std.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(std.FS, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	leak := "import std.core\n\nexport ZzLeak(a: &Alloc) {\n    _ = core.AllocBytes(a, 16, 8)\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "fs", "zz_leak.mk"), []byte(leak), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestMkTestLeakFails: a test that leaves an allocation of sys.heap live
// exits 102 with the runtime's leak line and fails as leak; one that frees
// what it allocates passes.
func TestMkTestLeakFails(t *testing.T) {
	src := "import std.fs\n\ntest \"leaks\" (sys: &System) {\n    fs.ZzLeak(sys.heap.Alloc())\n}\n\n" +
		"test \"frees\" (sys: &System) {\n    var xs := HeapList[int].New(sys.heap.Alloc())\n    defer xs.Free(sys.heap.Alloc())\n    try xs.Push(sys.heap.Alloc(), 7)\n}\n"
	dir := testPkg(t, map[string]string{"t.mk": src})
	rep := mkTest(t, Config{Root: filepath.Join(dir, "t.mk"), StdDir: stdWithLeak(t)}, TestOptions{})
	if got := results(rep); got != "main leaks leak\nmain frees pass" {
		t.Fatalf("results:\n%s\n%+v", got, rep.Tests)
	}
	if l := rep.Tests[0]; l.Status != 102 || !strings.Contains(l.Output, "leak: 1 allocations, 16 bytes still live") {
		t.Errorf("leak test: status %d output %q", l.Status, l.Output)
	}
}

// TestMkTestLinux: the same runner on x86_64-linux, each child started
// through the target's runner (WSL on Windows): pass, panic 101, error 1,
// and a test that frees what it allocates passes. Starts wsl.exe: run it
// under tools/wsl-lock.ps1 with go test -p 1.
func TestMkTestLinux(t *testing.T) {
	runner(t, types.X86_64Linux)
	dir := testPkg(t, map[string]string{"t.mk": testsSource})
	rep := mkTest(t, Config{Root: filepath.Join(dir, "t.mk"), Target: types.X86_64Linux}, TestOptions{})
	want := "main adds pass\nmain panics panic\nmain returns an error error\nmain frees what it allocates pass\nmain prints pass"
	if got := results(rep); got != want {
		t.Fatalf("results:\n%s\nwant:\n%s\n%+v", got, want, rep.Tests)
	}
}

// TestTestArenaIsFresh: each test gets its own System whose arena the
// runner makes after TrackLeaks and frees before Finish, so allocating from
// sys.arena is no leak, also when the test then returns an error (Finish
// checks leaks before the error line), at dev and release. A block left live
// on sys.heap is still a leak (TestMkTestLeakFails: safe Mk cannot write
// one). Without a fresh arena the 3 MB test exits 102 with "leak: 1
// allocations, 3000016 bytes still live".
func TestTestArenaIsFresh(t *testing.T) {
	const src = `error Bad

test "big arena slice" (sys: &System) {
    xs := try sys.arena.Slice[u8](3_000_000)
    Assert(xs.Len() == 3_000_000)
}

test "arena then error" (sys: &System) {
    _ = try sys.arena.Slice[u8](100_000)
    fail Bad
}

test "no system" {
    Assert(true)
}
`
	dir := testPkg(t, map[string]string{"t.mk": src})
	for _, prof := range []types.Profile{types.TestProfile, types.Release} {
		t.Run(prof.String(), func(t *testing.T) {
			rep := mkTest(t, Config{Root: filepath.Join(dir, "t.mk"), Profile: prof}, TestOptions{})
			want := "main big arena slice pass\nmain arena then error error\nmain no system pass"
			if got := results(rep); got != want {
				t.Fatalf("results:\n%s\nwant:\n%s\noutputs: %+v", got, want, rep.Tests)
			}
			if e := rep.Tests[1]; e.Status != 1 || !strings.Contains(e.Output, "error: main.Bad") {
				t.Errorf("error test: status %d output %q", e.Status, e.Output)
			}
		})
	}
}

// TestMkTestStdModules (Config.StdTests, mk test --std): mk test runs std's
// own test blocks, with no package, numbered by module path then declaration
// order. The driver asserts the mechanism; whether every std block passes is
// for std's own tests (std/rt, std/task: their tests_*.mk).
func TestMkTestStdModules(t *testing.T) {
	t.Run("OneBlock", func(t *testing.T) {
		rep := mkTest(t, Config{StdTests: "std.rt"}, TestOptions{Run: "RT-F16-HeapZero"})
		if len(rep.Tests) != 1 || rep.Tests[0].Module != "std.rt" || rep.Tests[0].Result != "pass" {
			t.Fatalf("want one passing std.rt test, got %+v", rep.Tests)
		}
	})
	t.Run("Selection", func(t *testing.T) {
		for _, c := range []struct {
			std  string
			want func(string) bool
			many bool
		}{
			{"std", func(p string) bool { return p == "std" || strings.HasPrefix(p, "std.") }, true},
			{"std.rt", func(p string) bool { return p == "std.rt" }, false},
			{"std.r", nil, false}, // a path prefix, not a text prefix: nothing
		} {
			cfg := Config{StdTests: c.std, Tools: needTools(t), CacheDir: t.TempDir()}
			s, err := NewSession(cfg)
			if err != nil {
				t.Fatal(err)
			}
			r, rep, err := s.Test(TestOptions{Run: "\x00 no test has this name"})
			if err != nil || rep == nil || len(r.Report.Diags) > 0 {
				t.Fatalf("--std=%s: err %v, diags %s", c.std, err, r.Report.Text(r.Sources))
			}
			for _, m := range s.testModules() {
				if c.want == nil || !c.want(m.path) {
					t.Errorf("--std=%s selects %s", c.std, m.path)
				}
				if m.path == testRunnerModule {
					t.Errorf("--std=%s selects the runner", c.std)
				}
			}
			tests := s.w.packageTests(s.testModules())
			byMod := map[string]bool{}
			for _, tc := range tests {
				byMod[tc.module] = true
			}
			if c.many && len(byMod) < 2 {
				t.Errorf("--std=%s: tests from %d modules, want several", c.std, len(byMod))
			}
			if c.want == nil && len(tests) != 0 {
				t.Errorf("--std=%s: %d tests, want none", c.std, len(tests))
			}
			if c.std == "std.rt" && len(tests) == 0 {
				t.Errorf("--std=std.rt: no tests")
			}
		}
	})
	t.Run("Usage", func(t *testing.T) {
		for _, cfg := range []Config{{StdTests: "std.rt", Root: "."}, {StdTests: "core"}} {
			if _, err := NewSession(cfg); err == nil {
				t.Errorf("%+v: no usage error", cfg)
			} else if _, ok := err.(*UsageError); !ok {
				t.Errorf("%+v: %T, want *UsageError", cfg, err)
			}
		}
	})
}

// TestMkTestStdUnixScratch: on Windows a std test declared in one of
// stdUnixScratchFiles runs in a working directory of its own, gone after
// the test, and any other std test runs in the caller's. Each block below
// writes a marker in its working directory and prints that directory.
func TestMkTestStdUnixScratch(t *testing.T) {
	for _, p := range stdUnixScratchFiles {
		if _, err := fs.Stat(std.FS, strings.TrimPrefix(p, "std/")); err != nil {
			t.Errorf("stdUnixScratchFiles names %s: %v", p, err)
		}
	}
	if HostTarget().OS != types.OSWindows {
		t.Skip("the Unix socket fixtures bind relative names on Windows only")
	}
	block := func(name, marker string) string {
		return "\ntest \"" + name + "\" (sys: &System) {\n" +
			"    fs.WriteAll(sys.io, \"" + marker + "\", \"x\".AsBytes()).Must()\n" +
			"    cwd := fs.Cwd(sys.arena, sys.io).Must()\n" +
			"    sys.Print(\"cwd {cwd}\\n\", (cwd)).Must()\n}\n"
	}
	const unixName, otherName = "cwd marker in a Unix fixture file", "cwd marker in another file"
	dir := copyStd(t, map[string]string{"net/tests_zcwd.mk": "import std.fs\n" + block(otherName, "mk-cwd-other")},
		func(p string, b []byte) []byte {
			if p == "net/tests_unix.mk" {
				return append(b, block(unixName, "mk-cwd-unix")...)
			}
			return b
		})
	caller := t.TempDir()
	t.Chdir(caller)
	rep := mkTest(t, Config{StdTests: "std.net", StdDir: dir}, TestOptions{Run: "cwd marker"})
	cwds := map[string]string{}
	for _, tc := range rep.Tests {
		_, after, ok := strings.Cut(tc.Output, "cwd ")
		if tc.Result != "pass" || !ok {
			t.Fatalf("%s %q: %s\n%s", tc.Module, tc.Name, tc.Result, tc.Output)
		}
		cwds[tc.Name] = strings.TrimSpace(after)
	}
	if len(cwds) != 2 || cwds[unixName] == "" || cwds[otherName] == "" {
		t.Fatalf("want the two marker tests, got %q", cwds)
	}
	callerInfo, err := os.Stat(caller)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(cwds[otherName]); err != nil || !os.SameFile(info, callerInfo) {
		t.Errorf("the other file's test ran in %q, want the caller's %q (%v)", cwds[otherName], caller, err)
	}
	if _, err := os.Stat(cwds[unixName]); err == nil {
		t.Errorf("the Unix fixture file's test ran in %q, which is still there", cwds[unixName])
	}
	if _, err := os.Stat(filepath.Join(caller, "mk-cwd-unix")); err == nil {
		t.Errorf("the Unix fixture file's test wrote its marker in the caller's directory")
	}
	if _, err := os.Stat(filepath.Join(caller, "mk-cwd-other")); err != nil {
		t.Errorf("the other file's marker: %v", err)
	}
	// std/task/tests_defects.mk binds the fixed name mk-sd8-unbind.sock.
	// A directory of that name in the caller's directory, which neither
	// sys.UnixUnbind nor a bind can replace, fails the block unless it runs
	// in a directory of its own.
	t.Run("std.task's fixed socket name", func(t *testing.T) {
		caller := t.TempDir()
		if err := os.MkdirAll(filepath.Join(caller, "mk-sd8-unbind.sock", "in"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Chdir(caller)
		rep := mkTest(t, Config{StdTests: "std.task"}, TestOptions{Run: "sys.UnixUnbind removes"})
		if len(rep.Tests) != 1 || rep.Tests[0].Result != "pass" {
			t.Fatalf("want the one sys.UnixUnbind block, passing; got %+v", rep.Tests)
		}
	})
}
