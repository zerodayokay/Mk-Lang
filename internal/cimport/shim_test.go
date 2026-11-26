// Why: a build compiles only the shim functions the program names, so an
// unused static inline function that calls what nothing links never
// breaks the link; the rule must hold for inline functions and declared
// macros alike, per import, and must not follow what the pruned bindings
// keep for other reasons.
// What: TestCImport_KeptShim, _KeptShimTranslated and _ShimCache.
// How: KeptShim over hand-written functions and names, then over a real
// translation whose pruned binding keeps more functions than the program
// names; Save and Load of entries with and without the functions.

package cimport

import (
	"reflect"
	"strings"
	"testing"

	"mk/internal/cache"
	"mk/internal/source"
)

// TestCImport_KeptShim: KeptShim keeps a function exactly when the program
// names it from its own import (the name from another import, or spelled
// in another case, does not count), renames only the kept inline
// functions, and is "" when nothing is named.
func TestCImport_KeptShim(t *testing.T) {
	imports := []Import{{Name: "a", Headers: []string{"a.h"}, Defines: []string{"A_X"}}, {Name: "b", Headers: []string{"b.h"}}}
	fs := []ShimFunc{
		{Import: 0, Name: "a_in", Inline: true, Def: "#undef a_in\nint a_in(void) { return __mk_inline_a_in(); }\n"},
		{Import: 0, Name: "a_gone", Inline: true, Def: "#undef a_gone\nint a_gone(void) { return __mk_inline_a_gone(); }\n"},
		{Import: 0, Name: "A_MAC", Def: "static inline int __mk_macro_A_MAC(void) { return A_MAC(); }\n#undef A_MAC\nint A_MAC(void) { return __mk_macro_A_MAC(); }\n"},
		{Import: 1, Name: "b_in", Inline: true, Def: "#undef b_in\nint b_in(void) { return __mk_inline_b_in(); }\n"},
	}
	want := Want{}
	for _, n := range []string{"a_in", "A_MAC", "b_in", "AGone", "A_GONE"} {
		want.Add("a", n)
	}
	got := KeptShim(imports, fs, want)
	for _, w := range []string{"#define a_in __mk_inline_a_in\n", "#define A_X 1\n#include <a.h>\n#include <b.h>\n",
		"int a_in(void) { return __mk_inline_a_in(); }", "int A_MAC(void) { return __mk_macro_A_MAC(); }"} {
		if !strings.Contains(got, w) {
			t.Errorf("kept shim lacks %q:\n%s", w, got)
		}
	}
	for _, bad := range []string{"a_gone", "b_in", "#define A_MAC"} {
		if strings.Contains(got, bad) {
			t.Errorf("kept shim has %q:\n%s", bad, got)
		}
	}
	if s := KeptShim(imports, fs, Want{"a": {}, "b": {}}); !strings.Contains(s, "#define A_X 1\n#include <a.h>\n#include <b.h>\n") || strings.Contains(s, "__mk_inline_") || strings.Contains(s, "__mk_macro_") {
		t.Errorf("nothing used, expected defines+includes with no wrappers, got %q", s)
	}
	if s := KeptShim(imports, []ShimFunc{}, Want{"a": {}}); s != "" {
		t.Errorf("no shim functions, expected empty shim, got %q", s)
	}
	if s := KeptShim(imports, fs, Want{"b": {"b_in": true}}); !strings.Contains(s, "int b_in(void)") || strings.Contains(s, "a_in") {
		t.Errorf("b_in named from b: %q", s)
	}
	if ShimText(imports, fs) != ShimText(imports, fs) || !strings.Contains(ShimText(imports, fs), "a_gone") {
		t.Error("ShimText of every function")
	}
}

// TestCImport_KeptShimTranslated: a translated header with static inline
// functions (one calling another) and two declared macros, the program
// naming one inline, one macro and one struct: the whole shim still
// defines every function (Result.Shim), the kept one only the two named,
// and the inline the named one calls keeps its own name, so the call
// compiles to a local copy. The pruned binding also keeps the inline
// functions named like the struct's field and the named function's
// parameter, which the kept shim must not define.
func TestCImport_KeptShimTranslated(t *testing.T) {
	h := "int k_missing(void);\nstruct k_s { int k_field; };\nstatic inline int k_in(int x) { return x + 1; }\n" +
		"static inline int k_out(int k_param) { return k_in(k_param) * 2; }\n" +
		"static inline int k_unused(void) { return k_missing(); }\nstatic inline int k_field(void) { return k_missing(); }\n" +
		"static inline int k_param(void) { return k_missing(); }\n#define TWICE(x) ((x) * 2)\n#define GONE() k_missing()\n"
	im := Import{Name: "k", Headers: []string{"k.h"}, Macros: []string{"int TWICE(int x)", "int GONE(void)"}}
	res := translate(t, "x86_64-windows", map[string]string{"k.h": h}, im)
	if len(res.ShimFuncs) != 7 {
		t.Fatalf("%d shim functions, want 7: %+v", len(res.ShimFuncs), res.ShimFuncs)
	}
	for _, n := range []string{"k_in(", "k_out(", "k_unused(", "k_field(", "k_param(", "TWICE(", "GONE("} {
		if !strings.Contains(res.Shim, "int "+n) {
			t.Errorf("whole shim lacks %s:\n%s", n, res.Shim)
		}
	}
	want := Want{}
	for _, n := range []string{"k_out", "TWICE", "KS"} {
		want.Add("k", n)
	}
	pruned := PruneAll([]string{"k"}, res.Modules, cloneWantForTest(want))
	for _, n := range []string{"k_field", "k_param"} {
		if !strings.Contains(pruned[0], "\nexport extern "+n+"(") {
			t.Errorf("the pruned binding no longer keeps %s, so this test no longer shows the shim ignores it:\n%s", n, pruned[0])
		}
	}
	got := KeptShim([]Import{im}, res.ShimFuncs, want)
	wantLines(t, got, "#define k_out __mk_inline_k_out", "#include <k.h>", "int k_out(int a0) { return __mk_inline_k_out(a0); }",
		"int TWICE(int a0) { return __mk_macro_TWICE(a0); }")
	for _, bad := range []string{"__mk_inline_k_in", "k_unused", "k_field", "k_param", "GONE"} {
		if strings.Contains(got, bad) {
			t.Errorf("kept shim has %q:\n%s", bad, got)
		}
	}
}

// cloneWantForTest copies a Want, which PruneAll extends.
func cloneWantForTest(w Want) Want {
	out := Want{}
	for imp, names := range w {
		for n := range names {
			out.Add(imp, n)
		}
	}
	return out
}

// TestCImport_ShimCache: a translation's cache entry keeps the shim's
// functions one by one, so a build that hits it still defines only those
// it names; an entry with a shim but no functions (as one saved without
// them would be) is a miss, never a shim of nothing; an entry with no
// shim is a hit.
func TestCImport_ShimCache(t *testing.T) {
	st, err := cache.Open(t.TempDir(), cache.Options{})
	if err != nil {
		t.Fatal(err)
	}
	fs := []ShimFunc{{Import: 0, Name: "k_in", Inline: true, Def: "#undef k_in\nint k_in(void) { return __mk_inline_k_in(); }\n"}}
	key := source.HashBytes("test", []byte("with functions"))
	Save(st, key, &Result{Modules: []string{"m"}, Shim: "shim", ShimFuncs: fs})
	if res, ok := Load(st, key); !ok || !reflect.DeepEqual(res.ShimFuncs, fs) || res.Shim != "shim" {
		t.Errorf("entry with functions: hit %v, %+v", ok, res)
	}
	old := source.HashBytes("test", []byte("without functions"))
	Save(st, old, &Result{Modules: []string{"m"}, Shim: "shim"})
	if _, ok := Load(st, old); ok {
		t.Error("an entry with a shim and no functions hit")
	}
	plain := source.HashBytes("test", []byte("no shim"))
	Save(st, plain, &Result{Modules: []string{"m"}})
	if _, ok := Load(st, plain); !ok {
		t.Error("an entry with no shim missed")
	}
}
