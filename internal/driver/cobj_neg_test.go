// Why: a C object's cache entry must notice a header added to an earlier
// directory of the search path, which shadows the one the compile read,
// as the translation's entry does.
// What: tests that a C object's candidate keeps its negative lookups and
// misses once one exists, that a path that existed when the candidate was
// saved is no negative, that a shadowing header added between two builds
// rebuilds the shim and the C source's objects, not only the binding,
// also through nested include directories, and that a header found beside
// its includer leaves the translation's entry a hit.
// How: cobjKey and cobjCandidate over a store with a fake dependency
// file, and packages with include_dirs built or checked twice against one
// cache.

package driver

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mk/internal/cache"
	"mk/internal/cimport"
	"mk/internal/source"
)

// TestCObj_NegativeLookups: a dependency found under the second search
// directory makes the same path under the first a negative lookup of the
// candidate; the candidate hits while it is absent and misses once a
// file is there.
func TestCObj_NegativeLookups(t *testing.T) {
	st, err := cache.Open(t.TempDir(), cache.Options{})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	for _, d := range []string{a, b} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	src, dep := filepath.Join(root, "x.c"), filepath.Join(b, "dep.h")
	for _, p := range []string{src, dep} {
		if err := os.WriteFile(p, []byte("int x;\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	depFile := filepath.Join(root, "x.d")
	if err := os.WriteFile(depFile, []byte("obj: "+src+" "+dep+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dh := cimport.NewDepHasher(st)
	base := source.HashBytes("test", []byte("base"))
	key, cand, ok := cobjKey(dh, base, depFile, src, []string{root, a, b})
	if !ok {
		t.Fatal("cobjKey failed")
	}
	shadow := filepath.Join(a, "dep.h")
	// b/b/dep.h: "b/dep.h" (its name under root) beside b/dep.h itself.
	if want := []string{filepath.Join(b, "b", "dep.h"), filepath.Join(root, "dep.h"), shadow}; !slices.Equal(cand.Neg, want) {
		t.Fatalf("negatives %q, want %q", cand.Neg, want)
	}
	cimport.PutCand(st, cobjListKey(base), cand)
	if got, ok := cobjCandidate(dh, cimport.Cands(st, cobjListKey(base)), base); !ok || got != key {
		t.Fatalf("no shadowing header: hit %v, key %v, want %v", ok, got, key)
	}
	if err := os.WriteFile(shadow, []byte("int y;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The next build's DepHasher: one remembers its stats for a build.
	if _, ok := cobjCandidate(cimport.NewDepHasher(st), cimport.Cands(st, cobjListKey(base)), base); ok {
		t.Error("a header shadowing a dependency still hit the candidate")
	}
}

// TestCObj_ExistingIsNoNegative: a path under an earlier directory that
// exists when the candidate is saved was not picked by the compile (a
// quoted include found beside its includer), so it is no negative and the
// candidate hits; the absent path beside the source is one.
func TestCObj_ExistingIsNoNegative(t *testing.T) {
	st, err := cache.Open(t.TempDir(), cache.Options{})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	src, dep, other := filepath.Join(root, "x.c"), filepath.Join(b, "bar.h"), filepath.Join(a, "bar.h")
	for _, p := range []string{src, dep, other} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("int x;\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	depFile := filepath.Join(root, "x.d")
	if err := os.WriteFile(depFile, []byte("obj: "+src+" "+dep+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dh := cimport.NewDepHasher(st)
	base := source.HashBytes("test", []byte("base"))
	key, cand, ok := cobjKey(dh, base, depFile, src, []string{a, b})
	if !ok {
		t.Fatal("cobjKey failed")
	}
	// root/bar.h, beside the source, is searched first by a quoted include.
	if want := []string{filepath.Join(root, "bar.h"), filepath.Join(b, "b", "bar.h")}; !slices.Equal(cand.Neg, want) {
		t.Fatalf("negatives %q, want %q", cand.Neg, want)
	}
	cimport.PutCand(st, cobjListKey(base), cand)
	if got, ok := cobjCandidate(dh, cimport.Cands(st, cobjListKey(base)), base); !ok || got != key {
		t.Errorf("hit %v, key %v, want %v", ok, got, key)
	}
}

// TestCImport_IncludeDirsShadow: a dep.h added to include_dirs' first
// directory, shadowing the second's, rebuilds the shim (k_inl) and the C
// source (k_fn) as well as the binding (DepMagic); so does one added to
// the package root, which comes before both.
func TestCImport_IncludeDirsShadow(t *testing.T) {
	needTools(t)
	files := cBin("shadow", "#include \"dep.h\"\nint k_fn(void);\nstatic inline int k_inl(void) { return DEP_MAGIC; }\n",
		"#include \"lib/k.h\"\nint k_fn(void) { return DEP_MAGIC; }\n",
		cMain("", "        a := k.k_inl()\n        b := k.k_fn()\n", "{a} {b} {c}", "a: a, b: b, c: k.DepMagic"))
	files["pkg.mk"] = strings.Replace(files["pkg.mk"], "vendor: false)", `vendor: false, include_dirs: ["a/inc", "b/inc"])`, 1)
	files["a/inc/README"] = "empty\n"
	files["b/inc/dep.h"] = "#define DEP_MAGIC 2\n"
	dir := writeTree(t, files)
	cache := t.TempDir()
	if out := buildRun(t, dir, cache); out != "2 2 2\n" {
		t.Fatalf("first build printed %q, want 2 2 2", out)
	}
	for _, c := range []struct{ path, magic string }{{"a/inc/dep.h", "1"}, {"dep.h", "3"}} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(c.path)), []byte("#define DEP_MAGIC "+c.magic+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		want := c.magic + " " + c.magic + " " + c.magic + "\n"
		if out := buildRun(t, dir, cache); out != want {
			t.Errorf("after adding %s printed %q, want %q", c.path, out, want)
		}
	}
}

// TestCImport_NestedIncludeDirsShadow: with include_dirs ["inc",
// "inc/sub"], lib/k.h's "sub/y.h" is found through inc; a sub/y.h added to
// the package root shadows it, and the binding, the shim and the C source
// all read the new one.
func TestCImport_NestedIncludeDirsShadow(t *testing.T) {
	needTools(t)
	files := cBin("nested", "#include \"sub/y.h\"\nint k_fn(void);\nstatic inline int k_inl(void) { return DEP_MAGIC; }\n",
		"#include \"lib/k.h\"\nint k_fn(void) { return DEP_MAGIC; }\n",
		cMain("", "        a := k.k_inl()\n        b := k.k_fn()\n", "{a} {b} {c}", "a: a, b: b, c: k.DepMagic"))
	files["pkg.mk"] = strings.Replace(files["pkg.mk"], "vendor: false)", `vendor: false, include_dirs: ["inc", "inc/sub"])`, 1)
	files["inc/sub/y.h"] = "#define DEP_MAGIC 2\n"
	dir := writeTree(t, files)
	cache := t.TempDir()
	if out := buildRun(t, dir, cache); out != "2 2 2\n" {
		t.Fatalf("first build printed %q, want 2 2 2", out)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "y.h"), []byte("#define DEP_MAGIC 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := buildRun(t, dir, cache); out != "1 1 1\n" {
		t.Errorf("after adding sub/y.h printed %q, want 1 1 1", out)
	}
}

// TestCImport_SiblingIncludeHits: b/inc/x.h includes "y.h", found beside
// it though a/inc, earlier on the path, has a y.h too; that a/inc/y.h is no
// negative lookup, so a second check hits the translation.
func TestCImport_SiblingIncludeHits(t *testing.T) {
	tools := needTools(t)
	files := cBin("sib", "#include \"x.h\"\nint k_fn(void);\n", "", cMain("", "        a := k.k_fn()\n", "{a} {c}", "a: a, c: k.DepMagic"))
	files["pkg.mk"] = strings.Replace(files["pkg.mk"], "vendor: false)", `vendor: false, include_dirs: ["a/inc", "b/inc"])`, 1)
	files["a/inc/y.h"] = "#define DEP_MAGIC 7\n"
	files["b/inc/x.h"] = "#include \"y.h\"\n"
	files["b/inc/y.h"] = "#define DEP_MAGIC 2\n"
	dir := writeTree(t, files)
	cache := t.TempDir()
	for i, want := range []bool{false, true} {
		s, err := NewSession(Config{Root: dir, Tools: tools, CacheDir: cache})
		if err != nil {
			t.Fatal(err)
		}
		r := s.Check()
		if len(r.Report.Diags) > 0 {
			t.Fatalf("check %d:\n%s", i, r.Report.Text(r.Sources))
		}
		view, _ := os.ReadFile(filepath.Join(dir, "gen", HostTarget().Name, "c", "k.mk"))
		if s.w.cimports.hit != want || !strings.Contains(string(view), "export const DepMagic: i32 = 2") {
			t.Errorf("check %d: hit %v, want %v; view:\n%s", i, s.w.cimports.hit, want, view)
		}
	}
}
