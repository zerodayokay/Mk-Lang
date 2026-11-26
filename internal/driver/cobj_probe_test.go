// Why: an existence lookup (__has_include) of an absent header and an
// include spelled with ".." leave the C caches no trace of the file that
// would change the compile once it appears.
// What: TestCImport_HasIncludeAdded, TestCImport_DotDotShadow (a header
// that appears rebuilds the binding, the shim and the C source, while an
// unchanged second check hits), and TestCObj_ProbeCandidate (a C object's
// candidate keeps such negatives; a lookup through a macro leaves it
// uncached).
// How: packages built twice against one cache; cobjKey over a fake
// dependency file.

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

// probeBin is a package whose header lib/k.h starts with head and gives
// OPT_V (0 unless head defines it) to the binding (k.OptV), the shim
// (k_inl) and the C source (k_fn), with include_dirs a/inc and b/inc.
func probeBin(name, head string) map[string]string {
	files := cBin(name, head+"\n#ifndef OPT_V\n#define OPT_V 0\n#endif\nint k_fn(void);\nstatic inline int k_inl(void) { return OPT_V; }\n",
		"#include \"lib/k.h\"\nint k_fn(void) { return OPT_V; }\n",
		cMain("", "        a := k.k_inl()\n        b := k.k_fn()\n", "{a} {b} {c}", "a: a, b: b, c: k.OptV"))
	files["pkg.mk"] = strings.Replace(files["pkg.mk"], "vendor: false)", `vendor: false, include_dirs: ["a/inc", "b/inc"])`, 1)
	files["a/inc/README"] = "empty\n"
	files["b/inc/README"] = "empty\n"
	return files
}

// checkHits checks the package at dir twice against cache and reports
// whether the second check hit the translation.
func checkHits(t *testing.T, dir, cache string) bool {
	t.Helper()
	var hit bool
	for i := 0; i < 2; i++ {
		s, err := NewSession(Config{Root: dir, Tools: needTools(t), CacheDir: cache})
		if err != nil {
			t.Fatal(err)
		}
		if r := s.Check(); len(r.Report.Diags) > 0 {
			t.Fatalf("check %d:\n%s", i, r.Report.Text(r.Sources))
		}
		hit = s.w.cimports.hit
	}
	return hit
}

// TestCImport_HasIncludeAdded: lib/k.h includes opt.h only when
// __has_include finds it; it is absent, then added to a/inc.
func TestCImport_HasIncludeAdded(t *testing.T) {
	needTools(t)
	files := probeBin("hasinc", "#if __has_include(\"opt.h\")\n#include \"opt.h\"\n#endif")
	dir := writeTree(t, files)
	cache := t.TempDir()
	if out := buildRun(t, dir, cache); out != "0 0 0\n" {
		t.Fatalf("first build printed %q, want 0 0 0", out)
	}
	if !checkHits(t, dir, cache) {
		t.Error("an unchanged check missed the translation")
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "inc", "opt.h"), []byte("#define OPT_V 7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := buildRun(t, dir, cache); out != "7 7 7\n" {
		t.Errorf("after adding a/inc/opt.h printed %q, want 7 7 7", out)
	}
}

// TestCImport_DotDotShadow: lib/k.h's "../x.h" is found through b/inc as
// b/inc/../x.h; an a/x.h added (a/inc/../x.h, earlier on the path)
// shadows it.
func TestCImport_DotDotShadow(t *testing.T) {
	needTools(t)
	files := probeBin("dotdot", "#include \"../x.h\"")
	files["b/x.h"] = "#define OPT_V 1\n"
	dir := writeTree(t, files)
	cache := t.TempDir()
	if out := buildRun(t, dir, cache); out != "1 1 1\n" {
		t.Fatalf("first build printed %q, want 1 1 1", out)
	}
	if !checkHits(t, dir, cache) {
		t.Error("an unchanged check missed the translation")
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "x.h"), []byte("#define OPT_V 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := buildRun(t, dir, cache); out != "2 2 2\n" {
		t.Errorf("after adding a/x.h printed %q, want 2 2 2", out)
	}
}

// TestCObj_ProbeCandidate: a C source that asks __has_include("opt.h") and
// reads b/inc/../x.h (as -MD spells it) has a candidate whose negatives
// hold a/inc/opt.h and a/x.h; one whose lookup goes through a macro has
// none (false: the object is not cached).
func TestCObj_ProbeCandidate(t *testing.T) {
	st, err := cache.Open(t.TempDir(), cache.Options{})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	a, b := filepath.Join(root, "a", "inc"), filepath.Join(root, "b", "inc")
	src, x := filepath.Join(root, "s.c"), filepath.Join(root, "b", "x.h")
	for p, text := range map[string]string{src: "#if __has_include(\"opt.h\")\n#endif\n#include \"../x.h\"\n", x: "\n", filepath.Join(a, "README"): "\n", filepath.Join(b, "README"): "\n"} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sep := string(filepath.Separator)
	depFile := filepath.Join(root, "s.d")
	if err := os.WriteFile(depFile, []byte("obj: "+src+" "+b+sep+".."+sep+"x.h\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dh := cimport.NewDepHasher(st)
	base := source.HashBytes("test", []byte("base"))
	_, cand, ok := cobjKey(dh, base, depFile, src, []string{a, b}, []string{src})
	if !ok {
		t.Fatal("cobjKey failed")
	}
	for _, p := range []string{filepath.Join(a, "opt.h"), filepath.Join(b, "opt.h"), filepath.Join(root, "opt.h"), filepath.Join(root, "a", "x.h")} {
		if !slices.Contains(cand.Neg, p) {
			t.Errorf("no negative %s in %q", p, cand.Neg)
		}
	}
	if len(cand.Deps) != 1 || cand.Deps[0].Path != x {
		t.Errorf("deps %v, want the clean %s", cand.Deps, x)
	}
	if err := os.WriteFile(src, []byte("#define O \"opt.h\"\n#if __has_include(O)\n#endif\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := cobjKey(dh, base, depFile, src, []string{a, b}, []string{src}); ok {
		t.Error("a lookup through a macro left the object cached")
	}
}
