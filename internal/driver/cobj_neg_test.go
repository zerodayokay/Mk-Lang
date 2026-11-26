// Why: a C object's cache entry must notice a header added to an earlier
// directory of the search path, which shadows the one the compile read,
// as the translation's entry does (MKH-408).
// What: tests that a C object's candidate keeps its negative lookups and
// misses once one exists, and that a shadowing header added between two
// builds rebuilds the shim and the C source's objects, not only the
// binding.
// How: cobjKey and cobjCandidate over a store with a fake dependency
// file, and a package with include_dirs ["a/inc", "b/inc"] built twice
// against one cache.

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
	if want := []string{filepath.Join(root, "dep.h"), shadow}; !slices.Equal(cand.Neg, want) {
		t.Fatalf("negatives %q, want %q", cand.Neg, want)
	}
	saveCobjCandidate(st, base, cand)
	if got, ok := cobjCandidate(st, dh, base); !ok || got != key {
		t.Fatalf("no shadowing header: hit %v, key %v, want %v", ok, got, key)
	}
	if err := os.WriteFile(shadow, []byte("int y;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := cobjCandidate(st, dh, base); ok {
		t.Error("a header shadowing a dependency still hit the candidate")
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
