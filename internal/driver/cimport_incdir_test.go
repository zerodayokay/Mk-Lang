// Why: a C library's headers often include headers of another directory
// (a vendored dependency's include/), which the package root alone does not
// reach; pkg.CImport's include_dirs puts those directories on the path.
// What: tests that include_dirs reach the translation, the shim and the C
// sources, that they are part of the C import's cache key, and that a
// directory that is not a relative directory inside the package is E0202.
// How: cBin's package with include_dirs set, built and run, checked
// twice with a different directory, and checked with bad entries.

package driver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mk/internal/diag"
)

// incDirPkg is cBin's package whose lib/k.h includes dep.h, found only
// through include_dirs dirs: dep.h has a constant, a static inline
// function (so the shim's compile needs the directory) and a typedef the
// C source uses (so its compile needs it too).
func incDirPkg(t *testing.T, dirs string, deps map[string]string) map[string]string {
	t.Helper()
	files := cBin("inc", "#include \"dep.h\"\ndep_t k_twice(dep_t x);\n",
		"#include \"lib/k.h\"\ndep_t k_twice(dep_t x) { return dep_add(x, x); }\n",
		cMain("", "        a := k.k_twice(k.DepMagic)\n        b := k.dep_add(1, 2)\n", "{a} {b}", "a: a, b: b"))
	files["pkg.mk"] = strings.Replace(files["pkg.mk"], "vendor: false)", "vendor: false, include_dirs: "+dirs+")", 1)
	for p, text := range deps {
		files[p] = text
	}
	return files
}

// depHeader is a dep.h whose DEP_MAGIC is magic.
func depHeader(magic string) string {
	return "typedef int dep_t;\n#define DEP_MAGIC " + magic + "\nstatic inline dep_t dep_add(dep_t a, dep_t b) { return a + b; }\n"
}

// TestCImport_IncludeDirs: a header that includes another from an
// include_dirs directory binds, its static inline function's shim and the
// C source that includes it compile, and the program runs.
func TestCImport_IncludeDirs(t *testing.T) {
	needTools(t)
	dir := writeTree(t, incDirPkg(t, `["third/inc"]`, map[string]string{"third/inc/dep.h": depHeader("7")}))
	if out := buildRun(t, dir, t.TempDir()); out != "14 3\n" {
		t.Errorf("printed %q, want 14 3", out)
	}
}

// TestCImport_IncludeDirsKey: include_dirs are part of the translation's
// cache key: naming another directory with a dep.h of its own is a miss
// and reads that header, not the first one's binding.
func TestCImport_IncludeDirsKey(t *testing.T) {
	tools := needTools(t)
	deps := map[string]string{"a/inc/dep.h": depHeader("1"), "b/inc/dep.h": depHeader("2")}
	cache := t.TempDir()
	// One package directory, whose pkg.mk each check rewrites: the -I
	// directories are absolute, so another root would be another key.
	dir := writeTree(t, incDirPkg(t, `["a/inc"]`, deps))
	check := func(dirs string) (*cImports, string) {
		if err := os.WriteFile(filepath.Join(dir, "pkg.mk"), []byte(incDirPkg(t, dirs, deps)["pkg.mk"]), 0o644); err != nil {
			t.Fatal(err)
		}
		s, err := NewSession(Config{Root: dir, Tools: tools, CacheDir: cache})
		if err != nil {
			t.Fatal(err)
		}
		r := s.Check()
		if len(r.Report.Diags) > 0 {
			t.Fatalf("check with include_dirs %s:\n%s", dirs, r.Report.Text(r.Sources))
		}
		view, _ := os.ReadFile(filepath.Join(dir, "gen", HostTarget().Name, "c", "k.mk"))
		return s.w.cimports, string(view)
	}
	first, v1 := check(`["a/inc"]`)
	if first.hit || !strings.Contains(v1, "export const DepMagic: i32 = 1") {
		t.Fatalf("first check: hit %v, view:\n%s", first.hit, v1)
	}
	again, _ := check(`["a/inc"]`)
	if !again.hit {
		t.Error("the same include_dirs did not hit the cache")
	}
	other, v2 := check(`["b/inc"]`)
	if other.hit || !strings.Contains(v2, "export const DepMagic: i32 = 2") {
		t.Errorf("another include directory: hit %v, view:\n%s", other.hit, v2)
	}
	both, v3 := check(`["b/inc", "a/inc"]`)
	if both.hit || !strings.Contains(v3, "export const DepMagic: i32 = 2") {
		t.Errorf("b/inc before a/inc: hit %v, view:\n%s", both.hit, v3)
	}
	back, v4 := check(`["a/inc"]`)
	if !back.hit || !strings.Contains(v4, "export const DepMagic: i32 = 1") {
		t.Errorf("back to a/inc: hit %v, view:\n%s", back.hit, v4)
	}
}

// TestCImport_IncludeDirsInvalid: an include_dirs entry that is absolute,
// leaves the package, is the root itself, uses \, or names no directory is
// E0202 reason invalid at that entry.
func TestCImport_IncludeDirsInvalid(t *testing.T) {
	tools := needTools(t)
	for _, c := range []struct{ entry, want string }{
		{`"/usr/include"`, "relative directory"},
		{`"C:/inc"`, "relative directory"},
		{`"third\\inc"`, "relative directory"},
		{`"../inc"`, "inside the package"},
		{`"third/../.."`, "inside the package"},
		{`"."`, "package root"},
		{`""`, "relative directory"},
		{`"missing"`, "does not exist"},
		{`"lib/k.h"`, "does not exist"},
	} {
		dir := writeTree(t, incDirPkg(t, "["+c.entry+"]", map[string]string{"third/inc/dep.h": depHeader("7")}))
		s, err := NewSession(Config{Root: dir, Tools: tools, CacheDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		r := s.Check()
		var found bool
		for _, d := range r.Report.Diags {
			if d.Diag.Code == diag.E0202 && strings.Contains(d.Diag.Message, c.want) && fact(d.Diag, "manifest") == "c_imports[0].include_dirs[0]" {
				found = true
			}
		}
		if !found {
			t.Errorf("include_dirs [%s]: want E0202 %q at c_imports[0].include_dirs[0], got:\n%s", c.entry, c.want, r.Report.Text(r.Sources))
		}
	}
}
