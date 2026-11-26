// Why: the C import's shim must define only the static inline functions
// and declared macros a program uses: an unused one may call a function
// nothing links, and the link fails on a symbol the program never names.
// What: TestCImport_ShimUnusedInline, _ShimSecondInline,
// _ShimInlineCallsInline and _ShimUnusedMacro.
// How: small packages over one header, built and run for the host; the
// session's pruned shim text is inspected, and a second build after an
// edit must hit the translation's cache and still link the new function.

package driver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shimBuild builds the package at dir for the host with cache, runs it,
// and returns its output and the package's C imports after the build.
func shimBuild(t *testing.T, dir, cache string) (string, *cImports) {
	t.Helper()
	s, err := NewSession(Config{Root: dir, Tools: needTools(t), CacheDir: cache})
	if err != nil {
		t.Fatal(err)
	}
	r, paths, err := s.Build(filepath.Join(t.TempDir(), "p"+exeSuffix(HostTarget())))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Report.Diags) > 0 || len(paths) != 1 {
		t.Fatalf("build: %d paths\n%s", len(paths), r.Report.Text(r.Sources))
	}
	out, errOut, code := runExe(t, HostTarget(), paths[0], "")
	if code != 0 {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	return out, s.w.cimports
}

// TestCImport_ShimUnusedInline: a static inline function no program
// names, which calls a function nothing defines, no longer breaks the
// link ("undefined symbol k_missing" from its shim wrapper); the shim
// defines only the used one.
func TestCImport_ShimUnusedInline(t *testing.T) {
	needTools(t)
	dir := writeTree(t, cBin("su", "int k_missing(void);\nint k_add(int a, int b);\nstatic inline int k_unused(void) { return k_missing(); }\nstatic inline int k_inc(int x) { return x + 1; }\n",
		"#include \"k.h\"\nint k_add(int a, int b) { return a + b; }\n",
		cMain("", "        a := k.k_add(2, 3)\n        b := k.k_inc(a)\n", "{a} {b}", "a: a, b: b")))
	out, ci := shimBuild(t, dir, t.TempDir())
	if out != "5 6\n" {
		t.Errorf("printed %q, want 5 6", out)
	}
	if strings.Contains(ci.shim, "k_unused") || !strings.Contains(ci.shim, "int k_inc(int a0)") {
		t.Errorf("shim:\n%s", ci.shim)
	}
}

// TestCImport_ShimSecondInline: a program that starts using a second
// static inline function links it: the translation hits its cache (the
// headers did not change) while the shim, whose text follows the functions
// used, gains the new definition and its object is compiled again.
func TestCImport_ShimSecondInline(t *testing.T) {
	needTools(t)
	h := "static inline int k_a(int x) { return x + 1; }\nstatic inline int k_b(int x) { return x * 3; }\n"
	dir := writeTree(t, cBin("s2", h, "", cMain("", "        a := k.k_a(1)\n", "{a}", "a: a")))
	cache := t.TempDir()
	out, ci := shimBuild(t, dir, cache)
	if out != "2\n" || strings.Contains(ci.shim, "k_b") {
		t.Fatalf("first build printed %q; shim:\n%s", out, ci.shim)
	}
	main := cMain("", "        a := k.k_a(1)\n        b := k.k_b(2)\n", "{a} {b}", "a: a, b: b")
	if err := os.WriteFile(filepath.Join(dir, "app", "main.mk"), []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	out, ci = shimBuild(t, dir, cache)
	if !ci.hit {
		t.Error("the second build missed the translation's cache")
	}
	if out != "2 6\n" || !strings.Contains(ci.shim, "int k_b(int a0)") {
		t.Errorf("second build printed %q, want 2 6; shim:\n%s", out, ci.shim)
	}
	// Back to the first program: the first shim, from the object cache.
	if err := os.WriteFile(filepath.Join(dir, "app", "main.mk"), []byte(cMain("", "        a := k.k_a(1)\n", "{a}", "a: a")), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, ci = shimBuild(t, dir, cache); out != "2\n" || strings.Contains(ci.shim, "k_b") {
		t.Errorf("third build printed %q; shim:\n%s", out, ci.shim)
	}
}

// TestCImport_ShimInlineCallsInline: a used static inline function that
// calls an unused one links and runs; the callee keeps its own name in the
// shim (no wrapper of its own), so the call compiles to a local copy.
func TestCImport_ShimInlineCallsInline(t *testing.T) {
	needTools(t)
	dir := writeTree(t, cBin("sc", "static inline int k_in(int x) { return x + 1; }\nstatic inline int k_out(int x) { return k_in(x) * 2; }\n", "",
		cMain("", "        a := k.k_out(20)\n", "{a}", "a: a")))
	out, ci := shimBuild(t, dir, t.TempDir())
	if out != "42\n" {
		t.Errorf("printed %q, want 42", out)
	}
	if strings.Contains(ci.shim, "__mk_inline_k_in") || !strings.Contains(ci.shim, "int k_out(int a0)") {
		t.Errorf("shim:\n%s", ci.shim)
	}
}

// TestCImport_ShimUnusedMacro: a function-like macro declared in pkg.mk
// follows the same rule: one no program names, which expands to a call of
// a function nothing defines, does not break the link.
func TestCImport_ShimUnusedMacro(t *testing.T) {
	needTools(t)
	files := cBin("sm", "int k_missing(void);\n#define GONE() k_missing()\n#define TWICE(x) ((x) * 2)\n", "",
		cMain("", "        a := k.TWICE(21)\n", "{a}", "a: a"))
	files["pkg.mk"] = strings.Replace(files["pkg.mk"], "macros: []", `macros: ["int TWICE(int x)", "int GONE(void)"]`, 1)
	dir := writeTree(t, files)
	out, ci := shimBuild(t, dir, t.TempDir())
	if out != "42\n" {
		t.Errorf("printed %q, want 42", out)
	}
	if strings.Contains(ci.shim, "GONE") || !strings.Contains(ci.shim, "int TWICE(int a0)") {
		t.Errorf("shim:\n%s", ci.shim)
	}
}
