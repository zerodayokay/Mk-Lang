// Why: std's own test blocks are the main check that std works, on every
// target and build mode.
// What: TestStdMkTests and BenchmarkStdCheck.
// How: runs every std module's test blocks under mk test, natively on Windows
// and through WSL on Linux, at dev and release, with no leak; the benchmark
// times checking std, which every cold build pays.

package stdtest

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"mk/internal/driver"
	"mk/internal/types"
)

// stdExpect is a block's expected end, from a name ending in "[exit <code>:
// <text>]" (the runtime tests' convention, std/rt-sched_harness_test.go and
// internal/driver stdtests_test.go): that exit status and output containing
// the text. A block without it must pass.
var stdExpect = regexp.MustCompile(`\[exit ([0-9]+): ([^\]]*)\]$`)

// stdEnvTag is a block's environment, from "[env K=V ...]" in its name: the
// block runs again alone with it.
var stdEnvTag = regexp.MustCompile(`\[env ([^\]]*)\]`)

// runtimeModules are tested by internal/driver's TestStdRuntimeTests, which
// knows their tags and their environment, so this test leaves them out rather
// than run them twice in the gate.
var runtimeModules = []string{"std.rt", "std.task"}

// stdRoot is the live std directory of this repository, or MK_STDTEST_STD
// when set (a planted copy, to see that a test sees a planted bug).
func stdRoot(t testing.TB) string {
	t.Helper()
	dir := os.Getenv("MK_STDTEST_STD")
	if dir == "" {
		dir = filepath.Join("..", "..", "std")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "core")); err != nil {
		t.Fatalf("no std at %s: %v", dir, err)
	}
	return dir
}

// targetModuleDirs are std/pkg.mk's target-selected module directories that
// hold test blocks, by directory, with the module they form; a module that
// exists for one target only is in targetOnlyModules.
var targetModuleDirs = map[string]string{"linux/linux": "std.linux"}

// targetOnlyModules are the std modules whose blocks run on one target only.
var targetOnlyModules = map[string]string{"std.linux": "x86_64-linux"}

// stdTestModules are the std modules that hold test blocks (a tests_*.mk
// file), by import path, sorted; the runtime's modules and the
// per-target std.sys directories are left out.
func stdTestModules(t *testing.T, root string) []string {
	t.Helper()
	seen := map[string]bool{}
	err := fs.WalkDir(os.DirFS(root), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		name := path.Base(p)
		if !strings.HasPrefix(name, "tests_") || !strings.HasSuffix(name, ".mk") {
			return nil
		}
		dir := path.Dir(p)
		if dir == "." || strings.HasPrefix(dir, "sys/") {
			return nil
		}
		mod := "std." + strings.ReplaceAll(dir, "/", ".")
		if sel, ok := targetModuleDirs[dir]; ok {
			mod = sel
		}
		if !slices.Contains(runtimeModules, mod) {
			seen[mod] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var mods []string
	for m := range seen {
		mods = append(mods, m)
	}
	slices.Sort(mods)
	return mods
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

// wslAnswers stops t unless `wsl.exe -e true` answers within 30 s (the WSL
// rule: a WSL that does not answer is reported, not retried).
func wslAnswers(t *testing.T) {
	t.Helper()
	if !linuxRun() {
		t.Skip(linuxOff)
	}
	wslGuard()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "wsl.exe", "-e", "true").Run(); err != nil {
		t.Fatalf("not verified: wsl.exe -e true did not answer within 30 s (%v)", err)
	}
}

// stdMkTest runs mk test over std module mod's own blocks and returns the
// report; a std that does not build fails t with its diagnostics.
func stdMkTest(t *testing.T, cfg driver.Config, opt driver.TestOptions) *driver.TestReport {
	t.Helper()
	s, err := driver.NewSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r, rep, err := s.Test(opt)
	if err != nil {
		if strings.Contains(err.Error(), "LLVM") {
			t.Skipf("needs the LLVM toolchain (the gate fails on this skip): %v", err)
		}
		t.Fatal(err)
	}
	if len(r.Report.Diags) > 0 || rep == nil {
		t.Fatalf("mk test --std=%s did not build:\n%s", cfg.StdTests, r.Report.Text(r.Sources))
	}
	return rep
}

// judgeStdTest fails t unless tc ended as its name asks.
func judgeStdTest(t *testing.T, tc driver.TestCase) {
	t.Helper()
	if reason, ok := stdSkipped(tc); ok {
		t.Logf("%s %q: skipped: %s", tc.Module, tc.Name, reason)
		return
	}
	code, text := 0, ""
	if m := stdExpect.FindStringSubmatch(tc.Name); m != nil {
		code, _ = strconv.Atoi(m[1])
		text = m[2]
	}
	if tc.Status != code || !strings.Contains(tc.Output, text) {
		t.Errorf("%s %q: %s, status %d, want %d with %q in the output\n%s", tc.Module, tc.Name, tc.Result, tc.Status, code, text, tc.Output)
	}
}

// stdSkipped reports a block that ended with status 0 after printing
// "skip: <reason>" as its first line: one whose feature the target does not
// have by design, so its name's verdict cannot be reached. The reason is
// logged; the block is neither a pass nor a failure.
func stdSkipped(tc driver.TestCase) (string, bool) {
	if tc.Status != 0 {
		return "", false
	}
	first, _, _ := strings.Cut(strings.TrimLeft(tc.Output, "\r\n"), "\n")
	reason, ok := strings.CutPrefix(strings.TrimRight(first, "\r"), "skip: ")
	return reason, ok
}

// TestStdMkTests: every test block of every std module outside
// the runtime ends as its name asks, under mk test --std=<module>, for
// x86_64-windows natively and x86_64-linux through WSL, at dev and release.
// The subtest names the module, so a failure points at its tests_<topic>.mk
// file.
func TestStdMkTests(t *testing.T) {
	root := stdRoot(t)
	mods := stdTestModules(t, root)
	if !slices.Contains(mods, "std.core") {
		t.Fatalf("no test blocks in std.core: modules %v", mods)
	}
	for _, target := range stdTestTargets() {
		t.Run(target.Name, func(t *testing.T) {
			linux := target.Name == types.X86_64Linux.Name
			host := runtime.GOOS + "/" + runtime.GOARCH
			switch {
			case target.Name == driver.HostTarget().Name:
				// Native: the host's own target, such as aarch64-macos on
				// a Mac or aarch64-linux on an ARM Linux machine.
			case linux && runtime.GOOS == "windows":
				// A Windows host runs the Linux leg through wsl.exe.
				wslAnswers(t)
			default:
				t.Skipf("no runner for %s on %s", target.Name, host)
			}
			for _, prof := range []types.Profile{types.TestProfile, types.Release} {
				name := "dev"
				if prof == types.Release {
					name = "release"
				}
				t.Run(name, func(t *testing.T) {
					cache := stdTestCacheDir(t)
					for _, mod := range mods {
						if only, ok := targetOnlyModules[mod]; ok && only != target.Name {
							continue
						}
						t.Run(mod, func(t *testing.T) {
							cfg := driver.Config{StdTests: mod, StdDir: root, Target: target, Profile: prof, CacheDir: cache, TestTimeout: 2 * time.Minute}
							rep := stdMkTest(t, cfg, driver.TestOptions{})
							if len(rep.Tests) == 0 {
								t.Errorf("%s: no test blocks ran", mod)
							}
							for _, tc := range rep.Tests {
								if tc.Module != mod {
									// --std=std.encoding also runs the blocks of
									// std.encoding.cjk, which its own subtest judges.
									if strings.HasPrefix(tc.Module, mod+".") {
										continue
									}
									t.Errorf("--std=%s ran %s %q", mod, tc.Module, tc.Name)
								}
								m := stdEnvTag.FindStringSubmatch(tc.Name)
								if m == nil {
									judgeStdTest(t, tc)
									continue
								}
								// Run it again, alone, with its environment.
								var keys []string
								for _, kv := range strings.Fields(m[1]) {
									k, v, _ := strings.Cut(kv, "=")
									t.Setenv(k, v)
									keys = append(keys, k)
								}
								if linux && runtime.GOOS == "windows" {
									wslenv := strings.Join(keys, ":")
									if old := os.Getenv("WSLENV"); old != "" {
										wslenv = old + ":" + wslenv
									}
									t.Setenv("WSLENV", wslenv)
								}
								one := stdMkTest(t, cfg, driver.TestOptions{Run: tc.Name})
								if len(one.Tests) != 1 {
									t.Errorf("-run %q selected %d tests", tc.Name, len(one.Tests))
									continue
								}
								judgeStdTest(t, one.Tests[0])
							}
						})
					}
				})
			}
		})
	}
}

// stdTestTargets are the targets std's blocks run for: x86_64-windows and
// x86_64-linux, which the gate runs, and the host's own target when it is
// another (aarch64-linux, aarch64-macos, x86_64-macos), which runs natively.
func stdTestTargets() []types.Target {
	ts := []types.Target{types.X86_64Windows, types.X86_64Linux}
	if h := driver.HostTarget(); h.Name != ts[0].Name && h.Name != ts[1].Name {
		ts = append(ts, h)
	}
	return ts
}

const (
	// stdTestCacheMaxRootLen keeps "<root>\mkst-<10 digits>" strictly under 85 bytes.
	stdTestCacheMaxRootLen = 68
	stdTestStaleCacheAge   = 24 * time.Hour
)

var (
	stdTestSweepMu   sync.Mutex
	stdTestSweptRoot = map[string]bool{}
)

// sweepStaleStdTestCaches removes mkst-* directories under root older than
// stdTestStaleCacheAge (left behind by a killed process) without racing live tests.
func sweepStaleStdTestCaches(root string, cutoff time.Time) {
	stdTestSweepMu.Lock()
	if stdTestSweptRoot[root] {
		stdTestSweepMu.Unlock()
		return
	}
	stdTestSweptRoot[root] = true
	stdTestSweepMu.Unlock()
	ents, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range ents {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "mkst-") {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}

// stdTestCacheDir returns a temporary cache directory whose path does not grow
// with TEMP on Windows. Starting 64 copies of an executable at once fails
// there with ERROR_NOT_ENOUGH_MEMORY once the image path reaches ~190 bytes,
// and placing <cache>/v1/bin/<hash>/mk-test.exe under a long TEMP exceeds it.
func stdTestCacheDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		return t.TempDir()
	}
	var roots []string
	if uc, err := os.UserCacheDir(); err == nil && uc != "" {
		roots = append(roots, uc)
	}
	if sd := strings.TrimRight(os.Getenv("SystemDrive"), `\/`); sd != "" {
		roots = append(roots, sd+string(filepath.Separator))
	}
	if vol := filepath.VolumeName(os.TempDir()); vol != "" {
		roots = append(roots, vol+string(filepath.Separator))
	}
	return stdTestCacheDirFrom(t, roots)
}

func stdTestCacheDirFrom(t *testing.T, rawRoots []string) string {
	t.Helper()
	seen := map[string]bool{}
	var roots []string
	for _, r := range rawRoots {
		if r == "" {
			continue
		}
		key := strings.ToLower(filepath.Clean(r))
		if seen[key] {
			continue
		}
		seen[key] = true
		roots = append(roots, r)
	}
	cutoff := time.Now().Add(-stdTestStaleCacheAge)
	for _, maxLen := range []int{stdTestCacheMaxRootLen, 1 << 20} {
		for _, r := range roots {
			if len(r) > maxLen {
				continue
			}
			sweepStaleStdTestCaches(r, cutoff)
			dir, err := os.MkdirTemp(r, "mkst-")
			if err == nil {
				t.Cleanup(func() { _ = os.RemoveAll(dir) })
				return dir
			}
		}
	}
	return t.TempDir()
}

// TestStdTestCacheDir checks stdTestCacheDir's path bound, fallback order,
// uniqueness, and t.Cleanup removal.
func TestStdTestCacheDir(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows cache-root selection is Windows-only")
	}
	base := t.TempDir()
	longRoot := filepath.Join(base, strings.Repeat("x", max(1, 72-len(base)-1)))
	if err := os.Mkdir(longRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	shortBase := base
	if len(filepath.Join(shortBase, "s")) > stdTestCacheMaxRootLen {
		shortBase = stdTestCacheDir(t)
	}
	shortRoot := filepath.Join(shortBase, "s")
	if err := os.Mkdir(shortRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if len(shortRoot) > stdTestCacheMaxRootLen {
		t.Skipf("no <= %d-byte root available (%q is %d bytes)", stdTestCacheMaxRootLen, shortRoot, len(shortRoot))
	}
	missingRoot := filepath.Join(base, "missing")

	var created []string
	t.Run("prefers_short_and_cleans_up", func(t *testing.T) {
		d1 := stdTestCacheDirFrom(t, []string{missingRoot, longRoot, shortRoot, shortRoot})
		d2 := stdTestCacheDirFrom(t, []string{missingRoot, longRoot, shortRoot})
		if d1 == d2 {
			t.Fatalf("duplicate cache dir %q", d1)
		}
		for _, d := range []string{d1, d2} {
			if filepath.Dir(d) != shortRoot {
				t.Fatalf("picked %q, want under %q (skipping %d-byte %q)", d, shortRoot, len(longRoot), longRoot)
			}
			if len(d) >= 85 {
				t.Fatalf("cache dir %q is %d bytes, want < 85", d, len(d))
			}
			created = append(created, d)
		}
	})
	for _, d := range created {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Fatalf("cache dir %q survived t.Cleanup: %v", d, err)
		}
	}

	t.Run("all_roots_unwritable_falls_back_to_temp", func(t *testing.T) {
		d := stdTestCacheDirFrom(t, []string{missingRoot})
		if info, err := os.Stat(d); err != nil || !info.IsDir() {
			t.Fatalf("fallback %q is not an existing directory: %v", d, err)
		}
	})

	t.Run("respects_short_LocalAppData_under_long_TEMP", func(t *testing.T) {
		long := filepath.Join(base, strings.Repeat("t", max(1, 160-len(base)-1)))
		if err := os.Mkdir(long, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GOTMPDIR", "")
		t.Setenv("TEMP", long)
		t.Setenv("TMP", long)
		t.Setenv("LocalAppData", shortRoot)
		d := stdTestCacheDir(t)
		if filepath.Dir(d) != shortRoot || len(d) >= 85 {
			t.Fatalf("stdTestCacheDir = %q (len %d), want under %q and < 85 bytes", d, len(d), shortRoot)
		}
	})

	t.Run("sweeps_only_stale_mkst_dirs", func(t *testing.T) {
		sweepRoot := filepath.Join(base, "sweep")
		if err := os.Mkdir(sweepRoot, 0o755); err != nil {
			t.Fatal(err)
		}
		stale := filepath.Join(sweepRoot, "mkst-old")
		live := filepath.Join(sweepRoot, "mkst-live")
		other := filepath.Join(sweepRoot, "keep-me")
		for _, p := range []string{stale, live, other} {
			if err := os.Mkdir(p, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		oldTime := time.Now().Add(-48 * time.Hour)
		if err := os.Chtimes(stale, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}
		_ = stdTestCacheDirFrom(t, []string{sweepRoot})
		if _, err := os.Stat(stale); !os.IsNotExist(err) {
			t.Fatalf("stale dir %q was not swept: %v", stale, err)
		}
		for _, p := range []string{live, other} {
			if _, err := os.Stat(p); err != nil {
				t.Fatalf("non-stale dir %q disappeared: %v", p, err)
			}
		}
	})
}

// TestStdMkProcLongTemp runs std.proc's 64-worker Supervise block under a
// TEMP past 150 bytes: stdTestCacheDir keeps mk-test.exe at a short path that
// does not grow with TEMP.
func TestStdMkProcLongTemp(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows 64-start long-path limit is Windows-only")
	}
	root := stdRoot(t)
	long := t.TempDir()
	if n := 160 - len(long) - 1; n > 0 {
		long = filepath.Join(long, strings.Repeat("p", n))
		if err := os.Mkdir(long, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GOTMPDIR", "")
	t.Setenv("TEMP", long)
	t.Setenv("TMP", long)
	for _, prof := range []types.Profile{types.TestProfile, types.Release} {
		name := "dev"
		if prof == types.Release {
			name = "release"
		}
		t.Run(name, func(t *testing.T) {
			cache := stdTestCacheDir(t)
			if strings.HasPrefix(strings.ToLower(cache), strings.ToLower(long)) || len(cache) >= 85 {
				t.Fatalf("stdTestCacheDir(%q) grew with TEMP or is too long (%d bytes)", cache, len(cache))
			}
			cfg := driver.Config{
				StdTests:    "std.proc",
				StdDir:      root,
				Target:      types.X86_64Windows,
				Profile:     prof,
				CacheDir:    cache,
				TestTimeout: 2 * time.Minute,
			}
			rep := stdMkTest(t, cfg, driver.TestOptions{Run: "Supervise: 64 workers"})
			if len(rep.Tests) != 1 {
				t.Fatalf("selected %d tests, want 1", len(rep.Tests))
			}
			judgeStdTest(t, rep.Tests[0])
		})
	}
}

// BenchmarkStdCheck: mk check of an empty program over the live
// std, with no cache, so every iteration checks every std module through
// every stage check runs. The time per op is the cost std adds to a cold
// build.
func BenchmarkStdCheck(b *testing.B) {
	root := stdRoot(b)
	pkg := b.TempDir()
	if err := os.WriteFile(filepath.Join(pkg, "main.mk"), []byte("export Main(sys: &System) {\n}\n"), 0o644); err != nil {
		b.Fatal(err)
	}
	cfg := driver.Config{Root: filepath.Join(pkg, "main.mk"), StdDir: root, NoCache: true}
	for b.Loop() {
		s, err := driver.NewSession(cfg)
		if err != nil {
			b.Fatal(err)
		}
		r := s.Check()
		if len(r.Report.Diags) > 0 {
			b.Fatalf("std does not check clean:\n%s", r.Report.Text(r.Sources))
		}
	}
}
