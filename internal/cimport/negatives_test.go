// Why: a C cache's candidate must notice every file whose appearance would
// change the compile, including one an existence lookup asked for and one
// an include spelled with "..", and must refuse to describe what it cannot.
// What: TestCImport_Probes (the scan of __has_include and its kin),
// _NegativesHasInclude, _NegativesDotDot, _NegativesNotModelled,
// _NegativesAbsoluteName, TestCImport_SaveUnmodelled, _SaveUnitText and
// _ParseDepFileUnit.
// How: Negatives over files in a temporary tree; probes over texts.

package cimport

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"mk/internal/cache"
	"mk/internal/source"
)

// writeFiles writes an empty line to each path, making its directory.
func writeFiles(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		writeText(t, p, "\n")
	}
}

func writeText(t *testing.T, p, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCImport_Probes: each lookup form, a mention that looks nothing up, a
// fallback's definition, text in comments and literals, a wrapper macro
// and its calls, and the forms not modelled.
func TestCImport_Probes(t *testing.T) {
	for _, c := range []struct {
		name  string
		texts []string
		want  []probe
		ok    bool
	}{
		{"quoted", []string{`#if __has_include("opt.h")` + "\n#endif\n"}, []probe{{name: "opt.h", quoted: true}}, true},
		{"angled spaced", []string{"#if __has_include ( <sys/x.h> ) && 1\n"}, []probe{{name: "sys/x.h"}}, true},
		{"next", []string{"#if __has_include_next(<stdint.h>)\n"}, []probe{{name: "stdint.h", next: true}}, true},
		{"embed", []string{`#if __has_embed("data.bin")` + "\n"}, []probe{{name: "data.bin", quoted: true}}, true},
		{"continuation", []string{"#if __has_include \\\n (\"a.h\")\n"}, []probe{{name: "a.h", quoted: true}}, true},
		{"mentions", []string{"#ifdef __has_include\n#if defined(__has_include) || defined __has_include_next\n#undef __has_embed\n#  ifndef /* x */ __has_include\n#elifdef __has_embed\n"}, nil, true},
		{"fallback", []string{"#ifndef __has_include\n#  define __has_include(x) 0\n#endif\n"}, nil, true},
		{"comments and literals", []string{"// __has_include(FOO)\n/* __has_include(BAR) */\nconst char *s = \"__has_include(X)\";\n#error don't __has_include(Y) wait\n/* __has_include(Z)\n */\n"}, nil, true},
		{"comment only", []string{"// __has_include(FOO)\n/* __has_include(BAR)\n */ const char *s = \"__has_include(X)\";\n"}, nil, true},
		{"wrapper", []string{"#define HAS(h) __has_include(h)\n", "#if HAS(\"w.h\") && HAS(\"v.h\")\n#endif\n"},
			[]probe{{name: "w.h", quoted: true}, {name: "v.h", quoted: true}}, true},
		{"wrapper next", []string{"#define HN(a, h) __has_include_next(h)\n#if HN(1, \"n.h\")\n"}, []probe{{name: "n.h", quoted: true, next: true}}, true},
		// An angled argument is macro-expanded before the wrapper's lookup
		// reads it (linux is 1 in gnu11).
		{"wrapper angled", []string{"#define HAS(h) __has_include(h)\n#if HAS(<linux/x.h>)\n"}, nil, false},
		{"wrapper of a wrapper", []string{"#define HAS(h) __has_include(h)\n#define H2(a, b) HAS(b)\n#if H2(1, \"x.h\")\n"},
			[]probe{{name: "x.h", quoted: true}}, true},
		{"wrapper defined twice", []string{"#define W(a, b) __has_include(a)\n", "#undef W\n#define W(a, b) __has_include(b)\n#if W(\"x.h\", \"y.h\")\n"},
			[]probe{{name: "x.h", quoted: true}, {name: "y.h", quoted: true}}, true},
		// An alias (glibc's __glibc_has_include) is followed by the file's
		// own tokens, so its angled name is read as spelled.
		{"alias", []string{"#define HAS __has_include\n#if HAS(\"opt.h\") || HAS (<sys/x.h>)\n#endif\n"},
			[]probe{{name: "opt.h", quoted: true}, {name: "sys/x.h"}}, true},
		{"alias of a wrapper", []string{"#define W(h) __has_include(h)\n#define H W\n#if H(\"o.h\")\n"}, []probe{{name: "o.h", quoted: true}}, true},
		{"alias in a -D text", []string{"#define HAS __has_include\n", "#if HAS(\"o.h\")\n"}, []probe{{name: "o.h", quoted: true}}, true},
		{"alias in a longer body", []string{"#define HAS __has_include && 1\n"}, nil, false},
		{"alias of an alias's use", []string{"#define H __has_include\n#define G(f) f\n#if G(H)(\"o.h\")\n"}, nil, false},
		{"name as an argument", []string{"#define APPLY(f, x) f(x)\n#if APPLY(__has_include, \"opt.h\")\n"}, nil, false},
		{"name as an argument in a body", []string{"#define ID(x) x\n#define K ID(__has_include)(\"o.h\")\n"}, nil, false},
		{"angled in a body", []string{"#define HASX(x) __has_include(<x.h>)\n#if HASX(opt)\n"}, nil, false},
		{"angled in an object body", []string{"#define NAME opt\n#define HAVE __has_include(<NAME.h>)\n"}, nil, false},
		{"quoted in an object body", []string{"#define HAVE __has_include(\"o.h\")\n#if HAVE\n"}, []probe{{name: "o.h", quoted: true}}, true},
		{"comment before the parenthesis", []string{"#if __has_include/**/(\"opt.h\") && __has_include /* */ ( <p.h> )\n"},
			[]probe{{name: "opt.h", quoted: true}, {name: "p.h"}}, true},
		{"comment in a directive", []string{"#/**/define HAS __has_include\n#if HAS(\"opt.h\")\n"}, []probe{{name: "opt.h", quoted: true}}, true},
		{"comment in the argument", []string{"#if __has_include(/**/\"opt.h\")\n"}, nil, false},
		{"digraph directive", []string{"%:define HAS __has_include\n#if HAS(\"opt.h\")\n"}, []probe{{name: "opt.h", quoted: true}}, true},
		{"splice in the name", []string{"#if __has_\\\ninclude(\"opt.h\")\n"}, []probe{{name: "opt.h", quoted: true}}, true},
		{"splice after blanks", []string{"#define HAS \\  \n __has_include\n"}, nil, true},
		{"carriage returns", []string{"#define X 1\r#if __has_include(\"a.h\")\r#endif\r"}, []probe{{name: "a.h", quoted: true}}, true},
		{"trigraph", []string{"??=define HAS __has_include\n"}, nil, false},
		{"paste in an #if", []string{"#define CAT(a, b) a##b\n#if CAT(__has_, include)(\"opt.h\")\n"}, nil, false},
		{"paste of a body's piece", []string{"#define P __\n#define CAT(a, b) a %:%: b\n#if CAT(1, 2)\n"}, nil, false},
		{"paste no #if reaches", []string{"#define CAT(a, b) a##b\nint CAT(__has_, include);\n#define P __\n"}, nil, true},
		{"paste of no piece", []string{"#define DH(name) struct name##__; int __ x;\n#define CAT(a, b) a##b\n#if CAT(1, 2)\n"}, nil, true},
		{"piece before an empty macro", []string{"#define EMPTY\n#define P __ EMPTY EMPTYF()\n#define CAT(a, b) a##b\n#define XCAT(a, b) CAT(a, b)\n" +
			"#if XCAT(P, has_include)(\"opt.h\")\n"}, nil, false},
		{"angled inside a call", []string{"#define ID(x) x\n#define NAME opt\n#if ID(__has_include(<NAME.h>))\n"}, nil, false},
		{"angled inside parentheses", []string{"#if (__has_include(<a.h>)) && defined(X) && (1 + (__has_include(<b.h>)))\n"},
			[]probe{{name: "a.h"}, {name: "b.h"}}, true},
		{"quoted inside a call", []string{"#define ID(x) x\n#if ID(__has_include(\"o.h\"))\n"}, []probe{{name: "o.h", quoted: true}}, true},
		{"bare name in a function-like body", []string{"#define W(h) __has_include_next\n"}, nil, false},
		{"bare name in an #if", []string{"#if 1 + __has_include\n"}, nil, false},
		{"defined in a body", []string{"#define HAVE_HI defined(__has_include) && defined __has_embed\n"}, nil, true},
		{"macro argument", []string{"#define H \"opt.h\"\n#if __has_include(H)\n"}, nil, false},
		{"wrapper with a macro", []string{"#define HAS(h) __has_include(h)\n#if HAS(H)\n"}, nil, false},
		{"not a parameter", []string{"#define HAS(h) __has_include(g)\n"}, nil, false},
		{"concatenated", []string{`#if __has_include("a" "b.h")` + "\n"}, nil, false},
		{"empty", []string{`#if __has_include("")` + "\n"}, nil, false},
		{"plain", []string{"int has_include_x;\n#include <a.h>\n"}, nil, true},
	} {
		got, ok := probes(c.texts)
		if ok != c.ok || (ok && !slices.Equal(got, c.want)) {
			t.Errorf("%s: probes %v %v, want %v %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

// TestCImport_NegativesHasInclude: c/api.h asks __has_include("opt.h")
// and opt.h is nowhere: the name under every search directory and beside
// the includer is a negative; one that exists is none.
func TestCImport_NegativesHasInclude(t *testing.T) {
	root := t.TempDir()
	a, b, c := filepath.Join(root, "a"), filepath.Join(root, "b"), filepath.Join(root, "c")
	api := filepath.Join(c, "api.h")
	writeText(t, api, "#if __has_include(\"opt.h\")\n#include \"opt.h\"\n#endif\n")
	writeFiles(t, filepath.Join(b, "README"))
	got, ok := Negatives([]string{api}, []string{root, a, b}, nil)
	if !ok {
		t.Fatal("not modelled")
	}
	for _, d := range []string{root, a, b, c} {
		if p := filepath.Join(d, "opt.h"); !slices.Contains(got, p) {
			t.Errorf("no negative %s in %q", p, got)
		}
	}
	writeFiles(t, filepath.Join(b, "opt.h"))
	if got, _ := Negatives([]string{api}, []string{root, a, b}, nil); slices.Contains(got, filepath.Join(b, "opt.h")) {
		t.Errorf("an existing file is a negative: %q", got)
	}
}

// TestCImport_NegativesDotDot: "../x.h" from c/api.h, found through the
// second search directory as b/inc/../x.h (the spelling -MD keeps): the
// name under the first, a/inc/../x.h, and beside the includer, c/../x.h,
// are negatives.
func TestCImport_NegativesDotDot(t *testing.T) {
	root := t.TempDir()
	a, b, c := filepath.Join(root, "a", "inc"), filepath.Join(root, "b", "inc"), filepath.Join(root, "c")
	api, x := filepath.Join(c, "api.h"), filepath.Join(root, "b", "x.h")
	writeText(t, api, "#include \"../x.h\"\n")
	writeFiles(t, x, filepath.Join(a, "README"), filepath.Join(b, "README"))
	sep := string(filepath.Separator)
	raw := b + sep + ".." + sep + "x.h"
	got, ok := Negatives([]string{api, raw}, []string{a, b}, nil)
	if !ok {
		t.Fatal("not modelled")
	}
	for _, p := range []string{filepath.Join(root, "a", "x.h"), filepath.Join(root, "x.h")} {
		if !slices.Contains(got, p) {
			t.Errorf("no negative %s in %q", p, got)
		}
	}
	if slices.Contains(got, x) {
		t.Errorf("a dependency is a negative: %q", got)
	}
	// A search directory spelled with "..": the path begins with it as
	// spelled, so the unit is modelled and a/x.h is still a negative.
	sys := root + sep + "c" + sep + ".." + sep + "b"
	got, ok = Negatives([]string{sys + sep + "x.h"}, []string{filepath.Join(root, "a"), sys}, nil)
	if !ok || !slices.Contains(got, filepath.Join(root, "a", "x.h")) {
		t.Errorf("a directory spelled with ..: %v %q", ok, got)
	}
}

// TestCImport_NegativesNotModelled: a lookup through a macro, and a ".."
// spelling that begins in no search or includer directory, leave the unit
// not described (false).
func TestCImport_NegativesNotModelled(t *testing.T) {
	root := t.TempDir()
	h := filepath.Join(root, "inc", "h.h")
	writeText(t, h, "#define OPT \"opt.h\"\n#if __has_include(OPT)\n#endif\n")
	if _, ok := Negatives([]string{h}, []string{filepath.Join(root, "inc")}, nil); ok {
		t.Error("a lookup through a macro is modelled")
	}
	x := filepath.Join(root, "y", "x.h")
	writeFiles(t, x)
	sep := string(filepath.Separator)
	if _, ok := Negatives([]string{filepath.Join(root, "z") + sep + ".." + sep + "y" + sep + "x.h"}, []string{filepath.Join(root, "inc")}, nil); ok {
		t.Error("a .. spelling from no known directory is modelled")
	}
}

// TestCImport_SaveUnmodelled: a translation whose headers look a file up
// through a macro is not stored, so the next run translates again.
func TestCImport_SaveUnmodelled(t *testing.T) {
	st, err := cache.Open(t.TempDir(), cache.Options{})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	h := filepath.Join(root, "h.h")
	writeText(t, h, "#if __has_include(OPT)\n#endif\n")
	key := source.HashBytes("test", []byte("key"))
	Save(st, key, &Result{Modules: []string{"m"}, Deps: []string{h}, SearchDirs: []string{root}})
	if _, ok := Load(st, key); ok {
		t.Error("an unmodelled translation was stored")
	}
	writeText(t, h, "#if __has_include(\"opt.h\")\n#endif\n")
	Save(st, key, &Result{Modules: []string{"m"}, Deps: []string{h}, SearchDirs: []string{root}})
	if _, ok := Load(st, key); !ok {
		t.Error("a modelled translation was not stored")
	}
	writeFiles(t, filepath.Join(root, "opt.h"))
	if _, ok := Load(st, key); ok {
		t.Error("the entry hits after opt.h appeared")
	}
}

// TestCImport_NegativesAbsoluteName: a lookup of an absolute name has that
// path as its negative, not the name under each directory; a rooted name
// without a volume, on Windows, is not modelled.
func TestCImport_NegativesAbsoluteName(t *testing.T) {
	root := t.TempDir()
	inc, h := filepath.Join(root, "inc"), filepath.Join(root, "inc", "h.h")
	opt := filepath.ToSlash(filepath.Join(root, "x", "opt.h"))
	writeText(t, h, "#if __has_include(\""+opt+"\")\n#endif\n")
	got, ok := Negatives([]string{h}, []string{inc}, nil)
	if !ok || !slices.Contains(got, filepath.Clean(filepath.FromSlash(opt))) {
		t.Errorf("absolute name: %v %q, want %s", ok, got, opt)
	}
	if filepath.Separator == '\\' {
		writeText(t, h, "#if __has_include(\"/x/opt.h\")\n#endif\n")
		if _, ok := Negatives([]string{h}, []string{inc}, nil); ok {
			t.Error("a rooted name is modelled")
		}
	}
}

// TestCImport_SaveUnitText: the unit's own text (tu.c's defines) is read
// for lookups: HAS, defined there, looks opt.h up in h.h.
func TestCImport_SaveUnitText(t *testing.T) {
	st, err := cache.Open(t.TempDir(), cache.Options{})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	h := filepath.Join(root, "h.h")
	writeText(t, h, "#if HAS(\"opt.h\")\n#endif\n")
	key := source.HashBytes("test", []byte("key"))
	Save(st, key, &Result{Modules: []string{"m"}, Deps: []string{h}, SearchDirs: []string{root}, Unit: "#define HAS __has_include\n#include <h.h>\n"})
	if _, ok := Load(st, key); !ok {
		t.Fatal("a modelled translation was not stored")
	}
	writeFiles(t, filepath.Join(root, "opt.h"))
	if _, ok := Load(st, key); ok {
		t.Error("the entry hits after opt.h appeared")
	}
}

// TestCImport_ParseDepFileUnit: only the unit itself, named as the
// command line names it, is left out; a header named tu.c is kept.
func TestCImport_ParseDepFileUnit(t *testing.T) {
	hdr := filepath.Join(t.TempDir(), "tu.c")
	got := parseDepFile("tu: tu.c " + hdr + "\n")
	if !slices.Equal(got, []string{hdr}) {
		t.Errorf("parseDepFile = %q, want %q", got, hdr)
	}
}
