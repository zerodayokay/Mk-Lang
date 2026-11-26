// Why: a build compiles only the shim functions its pruned bindings keep,
// so an unused static inline function that calls what nothing links never
// breaks the link; the rule must hold for inline functions and declared
// macros alike, per import.
// What: TestCImport_KeptShim and TestCImport_KeptShimTranslated.
// How: KeptShim over hand-written functions and bindings, then over a
// real translation pruned to one inline function and one macro.

package cimport

import (
	"strings"
	"testing"
)

// TestCImport_KeptShim: KeptShim keeps a function exactly when its own
// import's binding still declares its extern (an extern var, a comment or
// another import's binding of the name does not count), renames only the
// kept inline functions, and is "" when nothing is kept.
func TestCImport_KeptShim(t *testing.T) {
	imports := []Import{{Name: "a", Headers: []string{"a.h"}, Defines: []string{"A_X"}}, {Name: "b", Headers: []string{"b.h"}}}
	fs := []ShimFunc{
		{Import: 0, Name: "a_in", Inline: true, Def: "#undef a_in\nint a_in(void) { return __mk_inline_a_in(); }\n"},
		{Import: 0, Name: "a_gone", Inline: true, Def: "#undef a_gone\nint a_gone(void) { return __mk_inline_a_gone(); }\n"},
		{Import: 0, Name: "A_MAC", Def: "static inline int __mk_macro_A_MAC(void) { return A_MAC(); }\n#undef A_MAC\nint A_MAC(void) { return __mk_macro_A_MAC(); }\n"},
		{Import: 1, Name: "b_in", Inline: true, Def: "#undef b_in\nint b_in(void) { return __mk_inline_b_in(); }\n"},
	}
	texts := []string{
		"// head\n\n// C: int a_in(void)\n// static inline: defined by the shim of this package\nexport extern a_in(): i32\n\n\n\n" +
			"// C: #define A_MAC(...), declared in pkg.mk as int A_MAC(); defined by the shim\nexport extern A_MAC(): i32\n" +
			"\nexport extern var b_in: i32\n// export extern a_gone(): i32\n",
		"// head\n\n\n",
	}
	got := KeptShim(imports, fs, texts)
	for _, want := range []string{"#define a_in __mk_inline_a_in\n", "#define A_X 1\n#include <a.h>\n#include <b.h>\n",
		"int a_in(void) { return __mk_inline_a_in(); }", "int A_MAC(void) { return __mk_macro_A_MAC(); }"} {
		if !strings.Contains(got, want) {
			t.Errorf("kept shim lacks %q:\n%s", want, got)
		}
	}
	for _, bad := range []string{"a_gone", "b_in", "#define A_MAC"} {
		if strings.Contains(got, bad) {
			t.Errorf("kept shim has %q:\n%s", bad, got)
		}
	}
	if s := KeptShim(imports, fs, []string{"// head\n", "// head\n"}); s != "" {
		t.Errorf("nothing used, shim %q", s)
	}
	if s := KeptShim(imports, fs, texts[:1]); !strings.Contains(s, "a_in") || strings.Contains(s, "b_in") {
		t.Errorf("one text: %q", s)
	}
	if ShimText(imports, fs) != ShimText(imports, fs) || !strings.Contains(ShimText(imports, fs), "a_gone") {
		t.Error("ShimText of every function")
	}
}

// TestCImport_KeptShimTranslated: a translated header with three static
// inline functions (one calling another) and two declared macros, its
// binding pruned to one inline and one macro: the whole shim still defines
// every function (Result.Shim), the kept one only the used two, and the
// inline the used one calls keeps its own name, so the call compiles to a
// local copy.
func TestCImport_KeptShimTranslated(t *testing.T) {
	h := "int k_missing(void);\nstatic inline int k_in(int x) { return x + 1; }\nstatic inline int k_out(int x) { return k_in(x) * 2; }\n" +
		"static inline int k_unused(void) { return k_missing(); }\n#define TWICE(x) ((x) * 2)\n#define GONE() k_missing()\n"
	im := Import{Name: "k", Headers: []string{"k.h"}, Macros: []string{"int TWICE(int x)", "int GONE(void)"}}
	res := translate(t, "x86_64-windows", map[string]string{"k.h": h}, im)
	if len(res.ShimFuncs) != 5 {
		t.Fatalf("%d shim functions, want 5: %+v", len(res.ShimFuncs), res.ShimFuncs)
	}
	for _, n := range []string{"k_in(", "k_out(", "k_unused(", "TWICE(", "GONE("} {
		if !strings.Contains(res.Shim, "int "+n) {
			t.Errorf("whole shim lacks %s:\n%s", n, res.Shim)
		}
	}
	want := Want{}
	want.Add("k", "k_out")
	want.Add("k", "TWICE")
	pruned := PruneAll([]string{"k"}, res.Modules, want)
	got := KeptShim([]Import{im}, res.ShimFuncs, pruned)
	wantLines(t, got, "#define k_out __mk_inline_k_out", "#include <k.h>", "int k_out(int a0) { return __mk_inline_k_out(a0); }",
		"int TWICE(int a0) { return __mk_macro_TWICE(a0); }")
	for _, bad := range []string{"__mk_inline_k_in", "k_unused", "GONE"} {
		if strings.Contains(got, bad) {
			t.Errorf("kept shim has %q:\n%s", bad, got)
		}
	}
}
