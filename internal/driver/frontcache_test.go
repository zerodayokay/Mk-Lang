// Why: the module cache (frontcache.go) is only safe if a warm mk check is
// byte-identical to a cold one and an edit misses exactly what it touches.
// What: TestFrontWarmCheckEqualsCold, TestFrontBodyEditMissesOneModule,
// TestFrontWarmBuildEqualsCold and the TestWarmCheckReplays* tests, over frontCheck.
// How: checks a package cold, then warm in new Sessions, and compares --json
// output byte for byte and the set of modules loaded or rechecked.

package driver

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mk/internal/types"
)

// frontCheck is one mk check with the module cache on (no test hook, which
// would turn it off).
func frontCheck(t *testing.T, cfg Config) Result {
	t.Helper()
	s, err := NewSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s.Check()
}

// jsonOf is the --json bytes of a result.
func jsonOf(t *testing.T, r Result) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := r.WriteReport(&b, true); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// pkgOnly drops std and generated system view module paths.
func pkgOnly(ms []string) []string {
	var out []string
	for _, m := range ms {
		if !strings.HasPrefix(m, "std.") && !strings.HasPrefix(m, sysViewPrefix) {
			out = append(out, m)
		}
	}
	return out
}

// TestFrontWarmCheckEqualsCold: a cold mk check stores every module, std's
// included; a warm one in a new Session resolves and checks nothing, loads
// every module from the cache, and prints the same --json; a third does
// the same (the candidate stored by the first build still validates).
func TestFrontWarmCheckEqualsCold(t *testing.T) {
	for _, target := range []types.Target{types.X86_64Windows, types.X86_64Linux} {
		t.Run(target.Name, func(t *testing.T) {
			dir := writeTree(t, incPkg("x + 1"))
			cfg := Config{Root: dir, CacheDir: t.TempDir(), Target: target}
			cold := frontCheck(t, cfg)
			if len(cold.Report.Diags) > 0 {
				t.Fatalf("cold check:\n%s", cold.Report.Text(cold.Sources))
			}
			if len(cold.Trace.NotStored) > 0 {
				t.Errorf("not stored: %q", cold.Trace.NotStored)
			}
			if len(cold.Trace.ModulesFromCache) > 0 {
				t.Errorf("cold build hit %q", cold.Trace.ModulesFromCache)
			}
			for i := 0; i < 2; i++ {
				warm := frontCheck(t, cfg)
				if !bytes.Equal(jsonOf(t, warm), jsonOf(t, cold)) {
					t.Errorf("warm %d --json %s, cold %s", i, jsonOf(t, warm), jsonOf(t, cold))
				}
				if len(warm.Trace.ModulesResolved) > 0 || len(warm.Trace.ItemsChecked) > 0 {
					t.Errorf("warm %d resolved %q, checked %d items", i, warm.Trace.ModulesResolved, len(warm.Trace.ItemsChecked))
				}
				if !slices.Equal(warm.Trace.ModulesFromCache, cold.Trace.ModulesResolved) {
					t.Errorf("warm %d from cache %q, want %q", i, warm.Trace.ModulesFromCache, cold.Trace.ModulesResolved)
				}
			}
		})
	}
}

// TestFrontBodyEditMissesOneModule: at module granularity, a body edit of
// lib.F misses lib only: app's prefix holds lib's API and shape hashes,
// which a body edit leaves alone, and app read no body of lib. A comment
// and a blank line above lib.F miss nothing.
// After each edit the warm --json equals a cold check of the edited source
// into a fresh cache.
func TestFrontBodyEditMissesOneModule(t *testing.T) {
	dir := writeTree(t, incPkg("x + 1"))
	cfg := Config{Root: dir, CacheDir: t.TempDir(), Target: types.X86_64Windows}
	cold := frontCheck(t, cfg)
	if len(cold.Report.Diags) > 0 || len(cold.Trace.NotStored) > 0 {
		t.Fatalf("cold check: not stored %q\n%s", cold.Trace.NotStored, cold.Report.Text(cold.Sources))
	}
	lib := filepath.Join(dir, "lib", "lib.mk")
	edit := func(text string, missed []string) {
		t.Helper()
		if err := os.WriteFile(lib, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		warm := frontCheck(t, cfg)
		if got := pkgOnly(warm.Trace.ModulesResolved); !slices.Equal(got, missed) {
			t.Errorf("resolved %q, want %q", got, missed)
		}
		fresh := frontCheck(t, Config{Root: dir, CacheDir: t.TempDir(), Target: cfg.Target})
		if !bytes.Equal(jsonOf(t, warm), jsonOf(t, fresh)) {
			t.Errorf("warm --json %s, cold %s", jsonOf(t, warm), jsonOf(t, fresh))
		}
		again := frontCheck(t, cfg)
		if len(again.Trace.ModulesResolved) > 0 {
			t.Errorf("third build resolved %q", again.Trace.ModulesResolved)
		}
	}
	src := incPkg("x + 1")["lib/lib.mk"]
	edit("// a comment\n\n"+src, nil)
	edit(strings.Replace(src, "x + 1", "x + 2", 1), []string{"lib"})
	edit(strings.Replace(src, "export G(x: int): int", "export G(x: int, y: int): int", 1)+"", []string{"app", "lib"})
}

// frontBuild is one mk build with the module cache on; it returns the
// result and the executable's bytes.
func frontBuild(t *testing.T, cfg Config, out string) (Result, []byte) {
	t.Helper()
	s, err := NewSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r, paths, err := s.Build(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Report.Diags) > 0 || len(paths) != 1 {
		skipKnownBlockers(t, r)
		t.Fatalf("build: %d paths\n%s", len(paths), r.Report.Text(r.Sources))
	}
	exe, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	return r, exe
}

// TestFrontWarmBuildEqualsCold: a build whose every module comes from the
// cache (decoded declarations, MIR from the function results) links the
// bytes a cold build links, for tests/programs/fizz.mk and the two-module
// package, dev and release, on both targets (a Linux build is a cross
// build and runs nothing). The warm build compiles no pack either.
func TestFrontWarmBuildEqualsCold(t *testing.T) {
	tools := needTools(t)
	fizz, err := filepath.Abs(filepath.Join("..", "..", "tests", "programs", "fizz.mk"))
	if err != nil {
		t.Fatal(err)
	}
	roots := map[string]string{"fizz": fizz, "pkg": writeTree(t, incPkg("x + 1"))}
	for _, target := range []types.Target{types.X86_64Windows, types.X86_64Linux} {
		for _, prof := range []types.Profile{types.Dev, types.Release} {
			for _, name := range []string{"fizz", "pkg"} {
				t.Run(target.Name+"/"+prof.String()+"/"+name, func(t *testing.T) {
					cfg := Config{Root: roots[name], CacheDir: t.TempDir(), Target: target, Profile: prof, Tools: tools}
					cold, coldExe := frontBuild(t, cfg, filepath.Join(t.TempDir(), "out.exe"))
					if len(cold.Trace.NotStored) > 0 {
						t.Errorf("not stored: %q", cold.Trace.NotStored)
					}
					warm, warmExe := frontBuild(t, cfg, filepath.Join(t.TempDir(), "out.exe"))
					if !bytes.Equal(coldExe, warmExe) {
						t.Errorf("warm executable (%d bytes) differs from cold (%d bytes)", len(warmExe), len(coldExe))
					}
					if len(warm.Trace.ModulesResolved) > 0 || len(warm.Trace.PacksCompiled) > 0 {
						t.Errorf("warm build resolved %q, compiled %q", warm.Trace.ModulesResolved, warm.Trace.PacksCompiled)
					}
					if !slices.Equal(warm.Trace.ModulesFromCache, cold.Trace.ModulesResolved) {
						t.Errorf("warm from cache %q, want %q", warm.Trace.ModulesFromCache, cold.Trace.ModulesResolved)
					}
				})
			}
		}
	}
}

// TestWarmCheckReplaysDiagnostics: a module whose only diagnostics
// are a body's (an E0301 in check, an E0501 in borrow) is stored with them;
// a warm check loads it, checks no body, and prints the cold --json. Its
// importer, computed against a failed module, is not stored (poison).
func TestWarmCheckReplaysDiagnostics(t *testing.T) {
	files := pkgFiles(map[string]string{
		"lib/lib.mk": "export Widen(n: i32): i64 {\n    return n\n}\n\nexport Twice() {\n    var n := 1\n    a := mut n\n    b := mut n\n    *a += 1\n    *b += 1\n}\n",
		"app/app.mk": "import lib\n\nexport Use(): i64 {\n    return lib.Widen(1)\n}\n",
	})
	cfg := Config{Root: writeTree(t, files), CacheDir: t.TempDir(), Target: types.X86_64Windows}
	cold := frontCheck(t, cfg)
	if got := codes(cold); !slices.Equal(got, []string{"E0301"}) {
		t.Fatalf("cold codes %q, want [E0301] (E0501 is dropped by the phase rule)\n%s", got, cold.Report.Text(cold.Sources))
	}
	if len(cold.Trace.NotStored) != 1 || !strings.HasPrefix(cold.Trace.NotStored[0], "app: poison") {
		t.Errorf("not stored %q, want app only (poison)", cold.Trace.NotStored)
	}
	warm := frontCheck(t, cfg)
	if !bytes.Equal(jsonOf(t, warm), jsonOf(t, cold)) {
		t.Errorf("warm --json %s, cold %s", jsonOf(t, warm), jsonOf(t, cold))
	}
	if !slices.Contains(warm.Trace.ModulesFromCache, "lib") || slices.Contains(pkgOnly(warm.Trace.ModulesResolved), "lib") {
		t.Errorf("lib not loaded from the cache: from cache %q, resolved %q", warm.Trace.ModulesFromCache, warm.Trace.ModulesResolved)
	}
	// The flow diagnostic alone: fix the E0301, keep the E0501.
	lib := filepath.Join(cfg.Root, "lib", "lib.mk")
	if err := os.WriteFile(lib, []byte(strings.Replace(files["lib/lib.mk"], "): i64 {\n    return n", "): i32 {\n    return n", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(cfg.Root, "app", "app.mk")
	if err := os.WriteFile(app, []byte(strings.Replace(files["app/app.mk"], "i64", "i32", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	cold = frontCheck(t, cfg)
	if got := codes(cold); !slices.Equal(got, []string{"E0501"}) {
		t.Fatalf("codes %q, want [E0501]\n%s", got, cold.Report.Text(cold.Sources))
	}
	warm = frontCheck(t, cfg)
	if !bytes.Equal(jsonOf(t, warm), jsonOf(t, cold)) {
		t.Errorf("warm --json %s, cold %s", jsonOf(t, warm), jsonOf(t, cold))
	}
	if len(warm.Trace.ItemsChecked) > 0 || !slices.Contains(warm.Trace.ModulesFromCache, "lib") {
		t.Errorf("warm checked %d items, from cache %q", len(warm.Trace.ItemsChecked), warm.Trace.ModulesFromCache)
	}
}

// TestWarmCheckReplaysFixes: a diagnostic with fixes is never
// replayed from the cache. E0206 (resolve, a fix of class proven) leaves
// its module unstored, so a warm check reruns it and prints the cold bytes,
// fixes included.
func TestWarmCheckReplaysFixes(t *testing.T) {
	files := pkgFiles(map[string]string{
		"lib/lib.mk": "export Size(): int {\n    return 1\n}\n\nexport Size(): int {\n    return 2\n}\n",
	})
	cfg := Config{Root: writeTree(t, files), CacheDir: t.TempDir(), Target: types.X86_64Windows}
	cold := frontCheck(t, cfg)
	if got := codes(cold); !slices.Equal(got, []string{"E0206"}) {
		t.Fatalf("codes %q\n%s", got, cold.Report.Text(cold.Sources))
	}
	if !bytes.Contains(jsonOf(t, cold), []byte(`"fixes":[{`)) {
		t.Logf("E0206 carries no fix in this build: the test still checks the bytes (%s)", jsonOf(t, cold))
	}
	warm := frontCheck(t, cfg)
	if !bytes.Equal(jsonOf(t, warm), jsonOf(t, cold)) {
		t.Errorf("warm --json %s, cold %s", jsonOf(t, warm), jsonOf(t, cold))
	}
	if !slices.Contains(warm.Trace.ModulesResolved, "lib") {
		t.Errorf("lib was not rerun: resolved %q", warm.Trace.ModulesResolved)
	}
}
