// Why: the C import's driver side (object keys, shims, bitfields, function
// pointers, assembly and vendored views) had defects that must stay fixed.
// What: regression tests of header-edit rebuilds, signed bitfields, GNU
// inline shims, a redeclared extern variable, function pointers outside unsafe, padded macros, C source
// requirements, assembly targets, sysroots, vendored views and the warm
// edit budget.
// How: cBin and cMain write small packages over a C header; buildRun and
// buildAt build them for the host and the tests compare output and codes.

package driver

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"mk/internal/diag"
	"mk/internal/types"
)

// buildRun builds the package at dir for the host with cache and runs it,
// failing the test on any diagnostic.
func buildRun(t *testing.T, dir, cache string) string {
	t.Helper()
	r, paths := buildAt(t, dir, cache)
	if len(r.Report.Diags) > 0 || len(paths) != 1 {
		t.Fatalf("build: %d paths\n%s", len(paths), r.Report.Text(r.Sources))
	}
	out, errOut, code := runExe(t, HostTarget(), paths[0], "")
	if code != 0 {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	return out
}

// buildAt builds the package at dir for the host with cache.
func buildAt(t *testing.T, dir, cache string) (Result, []string) {
	t.Helper()
	s, err := NewSession(Config{Root: dir, Tools: needTools(t), CacheDir: cache})
	if err != nil {
		t.Fatal(err)
	}
	r, paths, err := s.Build(filepath.Join(t.TempDir(), "p"+exeSuffix(HostTarget())))
	if err != nil {
		t.Fatal(err)
	}
	return r, paths
}

// cMain is a bin's main module that prints the values of exprs (Mk
// expressions over the C import k) inside one unsafe block.
func cMain(imports string, stmts string, format string, record string) string {
	return "import c.k\n" + imports + "\nexport Main(sys: &System): WriteError!void {\n    var out := sys.Stdout()\n" +
		"    unsafe \"k's functions are plain C\" {\n" + stmts + "        try Print(mut out, \"" + format + "\\n\", (" + record + "))\n    }\n}\n"
}

// cBin is a package with the bin app, C import k over lib/k.h, and the
// C source lib/k.c when c is not "".
func cBin(name, header, c, main string) map[string]string {
	over := map[string]string{"name": `"` + name + `"`, "bins": `[pkg.Bin(name: "` + name + `", root: "app", link_script: "", cpu: "", subsystem: "console")]`,
		"c_imports":      `[pkg.CImport(name: "k", headers: ["lib/k.h"], defines: [], macros: [], vendor: false)]`,
		"unsafe_modules": `["app"]`}
	files := map[string]string{"lib/k.h": header, "app/main.mk": main}
	if c != "" {
		over["c_sources"] = `[pkg.CSource(path: "lib/k.c", flags: [])]`
		files["lib/k.c"] = c
	}
	files["pkg.mk"] = manifest(over)
	return files
}

// TestCImport_HeaderEditRebuilds: an edit of a header changes the
// objects compiled from it, though neither the shim's text (k_inl's
// signature is the same) nor the C source's bytes change: each object's
// key covers the content of every file its compile read (-MD).
func TestCImport_HeaderEditRebuilds(t *testing.T) {
	needTools(t)
	header := func(v, m string) string {
		return "#define KVAL " + v + "\nstatic inline int k_inl(int x) { return x * " + m + "; }\nint k_fn(void);\n"
	}
	dir := writeTree(t, cBin("he", header("10", "2"), "#include \"k.h\"\nint k_fn(void) { return KVAL; }\n",
		cMain("", "        a := k.k_inl(5)\n        b := k.k_fn()\n", "{a} {b}", "a: a, b: b")))
	cache := t.TempDir()
	if out := buildRun(t, dir, cache); out != "10 10\n" {
		t.Fatalf("first build printed %q, want 10 10", out)
	}
	if err := os.WriteFile(filepath.Join(dir, "lib", "k.h"), []byte(header("20", "3")), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := buildRun(t, dir, cache); out != "15 20\n" {
		t.Errorf("after the header edit the build printed %q, want 15 20 (a stale object was linked)", out)
	}
	if out := buildRun(t, dir, cache); out != "15 20\n" {
		t.Errorf("warm rebuild printed %q, want 15 20", out)
	}
}

// TestCImport_SignedBitfieldRuns: a signed bitfield C stores -1 in
// reads -1 in Mk, and a negative value Mk sets reads back in C.
func TestCImport_SignedBitfieldRuns(t *testing.T) {
	needTools(t)
	dir := writeTree(t, cBin("sb", "typedef struct S { int sb : 3; unsigned ub : 5; } S;\nS k_make(void);\nint k_sb(S *s);\n",
		"#include \"k.h\"\nS k_make(void) { S s; s.sb = -1; s.ub = 7; return s; }\nint k_sb(S *s) { return s->sb; }\n",
		cMain("import std.raw\n", "        var s := k.k_make()\n        x := s.Sb()\n        u := s.Ub()\n        s.SetSb(-2)\n        y := s.Sb()\n        c := k.k_sb(raw.OfMut(mut s))\n",
			"{x} {u} {y} {c}", "x: x, u: u, y: y, c: c")))
	if out := buildRun(t, dir, t.TempDir()); out != "-1 7 -2 -2\n" {
		t.Errorf("printed %q, want -1 7 -2 -2", out)
	}
}

// TestCImport_GNUInlineShim: a static inline function that uses a
// GNU form (typeof), which the binding's gnu11 reading accepts, builds:
// the shim is compiled as the headers were read.
func TestCImport_GNUInlineShim(t *testing.T) {
	needTools(t)
	dir := writeTree(t, cBin("gs", "static inline int k_t(int x) { typeof(x) y = x; return y + 1; }\n", "",
		cMain("", "        a := k.k_t(41)\n", "{a}", "a: a")))
	if out := buildRun(t, dir, t.TempDir()); out != "42\n" {
		t.Errorf("printed %q, want 42", out)
	}
}

// TestCImport_RedeclaredVarRuns: a header that declares an extern
// variable twice (as the Windows SDK does its IID_ names) gives a view that
// checks, and a program writes and reads the variable C defines.
func TestCImport_RedeclaredVarRuns(t *testing.T) {
	needTools(t)
	dir := writeTree(t, cBin("rv", "extern int k_count;\nextern int k_count;\nint k_get(void);\n",
		"#include \"k.h\"\nint k_count = 7;\nint k_get(void) { return k_count; }\n",
		cMain("", "        *(&k.k_count) = k.k_count + 1\n        a := k.k_get()\n", "{a}", "a: a")))
	if out := buildRun(t, dir, t.TempDir()); out != "8\n" {
		t.Errorf("printed %q, want 8", out)
	}
}

// TestCImport_FuncPtrNotCallable: a function pointer read from C (a struct
// field, of type ?fn) cannot be called from safe code: reading it out of the
// C import's struct is E1101 outside unsafe, whether it is called at once or
// unwrapped first; an extern's function-pointer parameter still takes a Mk
// function.
func TestCImport_FuncPtrNotCallable(t *testing.T) {
	tools := needTools(t)
	pkg := func(body string) map[string]string {
		return map[string]string{
			"pkg.mk": manifest(map[string]string{"name": `"fp"`, "unsafe_modules": `["src"]`,
				"c_imports": `[pkg.CImport(name: "k", headers: ["k.h"], defines: [], macros: [], vendor: false)]`}),
			"k.h":      "typedef int (*cb_t)(int);\ntypedef struct CB { int (*f)(int); } CB;\ntypedef struct CB2 { cb_t g; cb_t h[2]; } CB2;\nCB k_cb(void);\nCB2 k_cb2(void);\nint k_apply(int (*f)(int), int x);\n",
			"src/a.mk": "import c.k\n\nTwice(x: i32): i32 {\n    return x * 2\n}\n\n" + body,
		}
	}
	std := writeTree(t, fakeStdFiles())
	r := check(t, Config{Root: writeTree(t, pkg("export Call(c: k.Cb): i32 {\n    return c.f(2)\n}\n")), Tools: tools, StdDir: std})
	if len(r.Report.Diags) == 0 {
		t.Fatal("calling a C function pointer field outside unsafe was accepted")
	}
	for _, d := range r.Report.Diags {
		if d.Diag.Code == diag.E0000 || d.Diag.Code == diag.E0001 {
			t.Errorf("unexpected %s:\n%s", d.Diag.Code, r.Report.Text(r.Sources))
		}
	}
	r = check(t, Config{Root: writeTree(t, pkg("export Apply(): i32 {\n    unsafe \"k_apply is plain C\" {\n        return k.k_apply(Twice, 3)\n    }\n}\n")), Tools: tools, StdDir: std})
	if len(r.Report.Diags) > 0 {
		t.Errorf("passing a Mk function to C:\n%s", r.Report.Text(r.Sources))
	}
	// The field is ?fn, so a safe module would unwrap it and call the local;
	// the read of a C function pointer out of a C import's struct is E1101
	// outside unsafe, as naming a bodiless extern as a value is.
	r = check(t, Config{Root: writeTree(t, pkg("export Call(c: k.Cb): i32 {\n    f := c.f else {\n        return -1\n    }\n    return f(41)\n}\n")), Tools: tools, StdDir: std})
	if !hasCode(r, diag.E1101) {
		t.Errorf("calling an unwrapped C function pointer outside unsafe was accepted:\n%s", r.Report.Text(r.Sources))
	}
	// A field of a typedef'd function-pointer type, and an array of them.
	for _, read := range []string{"f := c.g else {\n        return -1\n    }\n    return f(41)", "f := c.h[1] else {\n        return -1\n    }\n    return f(41)"} {
		r = check(t, Config{Root: writeTree(t, pkg("export Call(c: k.Cb2): i32 {\n    "+read+"\n}\n")), Tools: tools, StdDir: std})
		if !hasCode(r, diag.E1101) {
			t.Errorf("%q outside unsafe was accepted:\n%s", read, r.Report.Text(r.Sources))
		}
	}
	// Inside unsafe the read is accepted; a write of a Mk function into
	// the field needs no unsafe.
	r = check(t, Config{Root: writeTree(t, pkg("export Call(c: k.Cb): i32 {\n    unsafe \"c.f is a C function or none\" {\n        f := c.f else {\n            return -1\n        }\n        return f(41)\n    }\n}\n\nexport Set(c: mut k.Cb) {\n    c.f = some(Twice)\n}\n")), Tools: tools, StdDir: std})
	if len(r.Report.Diags) > 0 {
		t.Errorf("a read inside unsafe and a write:\n%s", r.Report.Text(r.Sources))
	}
}

// hasCode reports whether r holds a diagnostic of code c.
func hasCode(r Result, c diag.Code) bool {
	for _, d := range r.Report.Diags {
		if d.Diag.Code == c {
			return true
		}
	}
	return false
}

// TestCImport_PaddedMacro: a macros entry with blanks around it is
// the trimmed prototype, never an internal error.
func TestCImport_PaddedMacro(t *testing.T) {
	tools := needTools(t)
	r := check(t, Config{Root: writeTree(t, map[string]string{
		"pkg.mk": manifest(map[string]string{"name": `"pm"`, "unsafe_modules": `["src"]`,
			"c_imports": `[pkg.CImport(name: "k", headers: ["k.h"], defines: [], macros: ["  int ONE()", " int MAX(int a, int b) "], vendor: false)]`}),
		"k.h":      "#define ONE() 1\n#define MAX(a, b) ((a) > (b) ? (a) : (b))\n",
		"src/a.mk": "import c.k\n\nexport F(): i32 {\n    unsafe \"the shim is plain C\" {\n        return k.ONE() + k.MAX(1, 2)\n    }\n}\n",
	}), Tools: tools, StdDir: writeTree(t, fakeStdFiles())})
	if len(r.Report.Diags) > 0 {
		t.Errorf("padded macros:\n%s", r.Report.Text(r.Sources))
	}
}

// TestCImport_NeedsCSource: a std module's c.<name> resolves
// only when the package declares that C import and a C source; with the
// import alone its importer's import line is E0202 needs-cimport.
func TestCImport_NeedsCSource(t *testing.T) {
	tools := needTools(t)
	std := fakeStdFiles()
	std["pkg.mk"] = strings.Replace(std["pkg.mk"], `unsafe_modules: ["core"`, `unsafe_modules: ["db", "core"`, 1)
	std["db/db.mk"] = "import c.k\n\nexport F(): i32 {\n    unsafe \"k_one is plain C\" {\n        return k.k_one()\n    }\n}\n"
	r := check(t, Config{Root: writeTree(t, map[string]string{
		"pkg.mk":   manifest(map[string]string{"c_imports": `[pkg.CImport(name: "k", headers: ["k.h"], defines: [], macros: [], vendor: false)]`}),
		"k.h":      "int k_one(void);\n",
		"src/a.mk": "import std.db\n\nexport A(): i32 {\n    return db.F()\n}\n",
	}), StdDir: writeTree(t, std), Tools: tools})
	var found bool
	for _, d := range r.Report.Diags {
		if d.Diag.Code == diag.E0202 && fact(d.Diag, "reason") == "needs-cimport" {
			found = true
		}
	}
	if !found {
		t.Errorf("no needs-cimport:\n%s", r.Report.Text(r.Sources))
	}
}

// TestCImport_AsmSourcesFor: a build assembles the files with no
// target and those naming exactly its own, never another architecture's of
// the same OS.
func TestCImport_AsmSourcesFor(t *testing.T) {
	man := &Manifest{AsmSources: []AsmSource{{Path: "a.s", Target: "x86_64-linux"}, {Path: "b.s", Target: "aarch64-linux"},
		{Path: "c.s", Target: "x86_64-freestanding"}, {Path: "d.s", Target: ""}}}
	for _, tc := range []struct {
		target string
		want   []int
	}{{"x86_64-linux", []int{0, 3}}, {"x86_64-windows", []int{3}}, {"aarch64-linux", []int{1, 3}}} {
		if got := asmSourcesFor(man, tc.target); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.target, got, tc.want)
		}
	}
}

// TestCImport_FreestandingAsmTarget: the language reference's example
// asm_sources entry, target x86_64-freestanding, is a valid manifest field
// on a hosted build too (its file is not assembled there).
func TestCImport_FreestandingAsmTarget(t *testing.T) {
	r := check(t, Config{Root: writeTree(t, map[string]string{
		"pkg.mk":     manifest(map[string]string{"asm_sources": `[pkg.AsmSource(path: "src/boot.s", target: "x86_64-freestanding")]`}),
		"src/boot.s": "\t.text\n",
		"src/a.mk":   "export F(): int {\n    return 1\n}\n",
	}), StdDir: fakeStd(t)})
	if len(r.Report.Diags) > 0 {
		t.Errorf("freestanding asm target:\n%s", r.Report.Text(r.Sources))
	}
}

// TestCImport_SourcesSysroot: a C source sees the target's system
// headers with no C import in the package, as it does with one.
func TestCImport_SourcesSysroot(t *testing.T) {
	needTools(t)
	sys := t.TempDir()
	if err := os.WriteFile(filepath.Join(sys, "sysk.h"), []byte("#define SYS_K 9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MK_SYSROOT_"+HostTarget().Name, sys)
	dir := writeTree(t, map[string]string{
		"pkg.mk": manifest(map[string]string{"name": `"ss"`, "bins": `[pkg.Bin(name: "ss", root: "app", link_script: "", cpu: "", subsystem: "console")]`,
			"c_sources": `[pkg.CSource(path: "lib/s.c", flags: [])]`, "unsafe_modules": `["app"]`}),
		"lib/s.c":     "#include <sysk.h>\nint s_val(void) { return SYS_K; }\n",
		"app/main.mk": "extern s_val(): i32\n\nexport Main(sys: &System): WriteError!void {\n    var out := sys.Stdout()\n    unsafe \"s_val is plain C\" {\n        v := s_val()\n        try Print(mut out, \"{v}\\n\", (v: v))\n    }\n}\n",
	})
	if out := buildRun(t, dir, t.TempDir()); out != "9\n" {
		t.Errorf("printed %q, want 9", out)
	}
}

// TestCImport_VendorNoShim: a vendored view read because the
// headers are gone checks, but a build that uses a static inline the shim
// defines is E0001 naming it, not an undefined symbol at link.
func TestCImport_VendorNoShim(t *testing.T) {
	needTools(t)
	files := cBin("vs", "static inline int k_inl(int x) { return x + 1; }\n#define K_VAL 41\n", "",
		cMain("", "        a := k.k_inl(1)\n", "{a} {b}", "a: a, b: k.KVal"))
	files["pkg.mk"] = strings.Replace(files["pkg.mk"], "vendor: false", "vendor: true", 1)
	dir := writeTree(t, files)
	cache := t.TempDir()
	if out := buildRun(t, dir, cache); out != "2 41\n" {
		t.Fatalf("printed %q, want 2 41", out)
	}
	if err := os.Remove(filepath.Join(dir, "lib", "k.h")); err != nil {
		t.Fatal(err)
	}
	r, _ := buildAt(t, dir, cache)
	if len(r.Report.Diags) != 1 || r.Report.Diags[0].Diag.Code != diag.E0001 || !strings.Contains(r.Report.Text(r.Sources), "c.k.k_inl") {
		t.Errorf("want one E0001 naming c.k.k_inl, got\n%s", r.Report.Text(r.Sources))
	}
}

// TestCImport_VendorHeaderError: with vendor: true, a C error in a
// header that is found is still E0001; only a header not found falls back
// on the view.
func TestCImport_VendorHeaderError(t *testing.T) {
	tools := needTools(t)
	dir := writeTree(t, map[string]string{
		"pkg.mk": manifest(map[string]string{"name": `"vh"`,
			"c_imports": `[pkg.CImport(name: "v", headers: ["v.h"], defines: [], macros: [], vendor: true)]`}),
		"v.h":      "#define V_VAL 41\n",
		"src/a.mk": "import c.v\n\nexport F(): i32 {\n    return v.VVal + 1\n}\n",
	})
	cfg := Config{Root: dir, CacheDir: t.TempDir(), Tools: tools, StdDir: writeTree(t, fakeStdFiles())}
	if r := frontCheck(t, cfg); len(r.Report.Diags) > 0 {
		t.Fatalf("first check:\n%s", r.Report.Text(r.Sources))
	}
	if err := os.WriteFile(filepath.Join(dir, "v.h"), []byte("#define V_VAL 41\nint broken(void) oops;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := frontCheck(t, cfg)
	if len(r.Report.Diags) != 1 || r.Report.Diags[0].Diag.Code != diag.E0001 {
		t.Errorf("a C error with vendor: true: want one E0001, got\n%s", r.Report.Text(r.Sources))
	}
}

// TestCImport_OneFunctionEditBudget: the warm check after a one-function
// edit, in a package with a C import, against the budget for an edit to
// diagnostic after a body edit (100 ms; the package is small, so this is the
// C import's own warm cost: the cimport cache hit stats its headers and
// reads none). The time is logged always and asserted only under
// MK_TIMING=1. With MK_CIMPORT_WINDOWS_SYSROOT set, a package importing
// windows.h is measured too (logged only).
func TestCImport_OneFunctionEditBudget(t *testing.T) {
	tools := needTools(t)
	std := writeTree(t, fakeStdFiles())
	measure := func(name string, files map[string]string, edit func(string) string) time.Duration {
		dir := writeTree(t, files)
		cfg := Config{Root: dir, CacheDir: t.TempDir(), Target: types.X86_64Windows, Tools: tools, StdDir: std}
		if r := frontCheck(t, cfg); len(r.Report.Diags) > 0 {
			t.Fatalf("%s cold check:\n%s", name, r.Report.Text(r.Sources))
		}
		src := filepath.Join(dir, "src", "a.mk")
		best := time.Duration(1 << 62)
		for i := 0; i < 3; i++ {
			if err := os.WriteFile(src, []byte(edit("return x + "+string(rune('2'+i)))), 0o644); err != nil {
				t.Fatal(err)
			}
			s, err := NewSession(cfg)
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			r := s.Check()
			took := time.Since(start)
			if len(r.Report.Diags) > 0 || !s.w.cimports.hit {
				t.Fatalf("%s warm check (hit %v):\n%s", name, s.w.cimports.hit, r.Report.Text(r.Sources))
			}
			best = min(best, took)
		}
		t.Logf("%s: warm mk check after a one-function edit, best of 3: %v", name, best)
		return best
	}
	small := measure("small header", cEditPkg("return x + 1"), func(g string) string { return cEditPkg(g)["src/a.mk"] })
	if timingRun() && small > 100*time.Millisecond {
		t.Errorf("warm one-function edit with a C import took %v, budget 100 ms", small)
	} else if !timingRun() {
		t.Log(timingOff)
	}
	if sys := os.Getenv("MK_CIMPORT_WINDOWS_SYSROOT"); sys != "" {
		t.Setenv("MK_SYSROOT_x86_64-windows", sys)
		win := func(g string) string {
			return "import c.win\n\nexport Max(): i32 {\n    return win.MaxPath\n}\n\nexport G(x: i32): i32 {\n    " + g + "\n}\n"
		}
		measure("windows.h", map[string]string{
			"pkg.mk": manifest(map[string]string{"name": `"wh"`,
				"c_imports": `[pkg.CImport(name: "win", headers: ["windows.h"], defines: [], macros: [], vendor: false)]`}),
			"src/a.mk": win("return x + 1"),
		}, win)
	}
}
