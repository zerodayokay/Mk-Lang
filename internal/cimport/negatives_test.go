// Why: a C cache's candidate must notice every file whose appearance would
// change the compile, including one an existence lookup asked for and one
// an include spelled with "..", and must refuse to describe what it cannot.
// What: TestCImport_Probes (the scan of __has_include and its kin),
// _NegativesHasInclude, _NegativesDotDot, _NegativesNotModelled and
// TestCImport_SaveUnmodelled.
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
		{"mentions", []string{"#ifdef __has_include\n#if defined(__has_include) || defined __has_include_next\n#undef __has_embed\n"}, nil, true},
		{"fallback", []string{"#ifndef __has_include\n#  define __has_include(x) 0\n#endif\n"}, nil, true},
		{"comments and literals", []string{"// __has_include(FOO)\n/* __has_include(BAR) */\nconst char *s = \"__has_include(X)\";\n#error don't __has_include(Y) wait\n/* __has_include(Z)\n */\n"}, nil, true},
		{"comment only", []string{"// __has_include(FOO)\n/* __has_include(BAR)\n */ const char *s = \"__has_include(X)\";\n"}, nil, true},
		{"wrapper", []string{"#define HAS(h) __has_include(h)\n", "#if HAS(<w.h>) && HAS(\"v.h\")\n#endif\n"},
			[]probe{{name: "w.h"}, {name: "v.h", quoted: true}}, true},
		{"wrapper next", []string{"#define HN(a, h) __has_include_next(h)\n#if HN(1, <n.h>)\n"}, []probe{{name: "n.h", next: true}}, true},
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
	writeFiles(t, x, filepath.Join(a, "README"))
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
