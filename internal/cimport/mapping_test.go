// Why: the mapping from C declarations to Mk is what users read and call, so
// each C form must map the same way every time and on every target.
// What: TestCImport_Mapping, _Linux, _Names, _TwoImportsOneIdentity,
// _Deterministic, _HeaderError, _Negatives, _NegativesNested,
// _NegativesExisting, _NegativesDirectory and TestCTypeParse.
// How: translates sampleHeader through clang and checks the Mk lines, in
// order; the negative lookups over paths in a temporary directory.

package cimport

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mk/internal/cache"
	"mk/internal/source"
)

// TestCImport_Mapping: the sample header's declarations, mapped as cimport
// maps them: a struct with a bitfield run becomes a struct with its unit and
// accessors, a union an opaque struct with accessors, #pragma pack a packed
// struct, enums aliases plus consts, a typedef of a function pointer ?fn (an
// extern's parameter is a plain fn type), an extern int an extern var, a
// variadic function an extern with ..., a static inline function and a
// declared function-like macro externs the shim defines, object-like macros
// consts.
func TestCImport_Mapping(t *testing.T) {
	res := translate(t, "x86_64-windows", map[string]string{"a.h": sampleHeader},
		Import{Name: "a", Headers: []string{"a.h"}, Macros: []string{"int FM(int x)"}})
	text := res.Modules[0]
	dumpIf(t, "a.mk", text)
	dumpIf(t, "shim.c", res.Shim)
	wantLines(t, text,
		"import std.raw",
		"export type SizeT = uint",
		"export struct SU {\n    export bytes: [1]u32\n}",
		"export SU.X(&self): i32 {",
		"export struct S {\n    export a: i32\n    export bits4: u32\n    export d: u8\n    export u: SU\n    export next: *S\n}",
		"export S.B(&self): u32 {\n    return (self.bits4 >> 0) & 0x7\n}",
		"export S.SetC(mut self, v: u32) {\n    self.bits4 = (self.bits4 & 0xffffff07) | ((v & 0x1f) << 3)\n}",
		"export packed struct P {\n    export a: u8\n    export b: i32\n}",
		"export type E = i32", "export const E1: E = 5", "export const E2: E = 6",
		"export type Color = i32", "export const Green: Color = 1",
		"export extern add(a: i32, b: i32): i32",
		"export extern vf(fmt: *u8, ...): i32",
		"export type Cb = ?fn(*void, i32): i32",
		"export extern use_cb(f: fn(*void, i32): i32, ctx: *void): i32",
		"export extern var gv: i32",
		"export struct Flex {\n    export n: i32\n}",
		"export Flex.Data(&self): *u8 {",
		"export extern my_strlen(s: *u8): uint",
		"export struct Sqlite3 {\n}",
		"export extern open_db(name: *u8, db: **Sqlite3): i32",
		"export struct Tm2 {\n    export tm_sec: i32\n    export dw_size: i32\n    export big: Uint64T",
		"export const Int64Min: i64 = -9223372036854775807 - 1",
		"export const K: i32 = 42",
		"export const Ks: str = \"hi\\n\"",
		"export const Kf: f64 = 1.5",
		"export const Kn: i64 = -7",
		"export const Ku: u32 = 4294967295",
		"export extern FM(x: i32): i32")
	for _, bad := range []string{"Kp", "Kt", "gv"} {
		if strings.Contains(text, "export const "+bad+":") {
			t.Errorf("%s should not be bound", bad)
		}
	}
	wantLines(t, res.Shim, "#define add __mk_inline_add", "#include <a.h>", "#undef add",
		"int add(int a0, int a1) { return __mk_inline_add(a0, a1); }",
		"static inline int __mk_macro_FM(int a0) { return FM(a0); }", "#undef FM", "int FM(int a0) { return __mk_macro_FM(a0); }")
	st := res.Stats[0]
	if st.Skipped["extern var"] != 0 || st.Skipped["macro is no constant"] != 1 || st.Bound() == 0 {
		t.Errorf("stats %+v", st)
	}
}

// TestCImport_Linux: long is 64 bits on the musl triple, and SysV puts a
// bitfield after a char in the next byte of an int-aligned record, which
// Mk's struct cannot say: opaque, with the bitfield read as the u8 at
// byte 1.
func TestCImport_Linux(t *testing.T) {
	res := translate(t, "x86_64-linux", map[string]string{"l.h": "long lf(unsigned long x);\nstruct B { char c; unsigned a:3; };\n"},
		Import{Name: "l", Headers: []string{"l.h"}})
	wantLines(t, res.Modules[0], "export extern lf(x: u64): i64", "export struct B {\n    export bytes: [1]u32\n}",
		"export B.A(&self): u8 {", ">> 0) & 0x7", "export B.SetA(mut self, v: u8) {")
}

// TestCImport_Names: recasing, keywords and fold collisions, so E0206,
// E0207 and E0102 never reach a binding.
func TestCImport_Names(t *testing.T) {
	for _, tc := range []struct{ in, pascal, snake string }{
		{"SQLITE_OK", "SqliteOk", "sqlite_ok"}, {"sqlite3_stmt", "Sqlite3Stmt", "sqlite3_stmt"}, {"FILE", "File", "file"},
		{"dwSize", "DwSize", "dw_size"}, {"HTTPServer", "HttpServer", "http_server"}, {"_x", "X", "x"}, {"3d", "C3d", "f3d"},
	} {
		if got := pascal(tc.in); got != tc.pascal {
			t.Errorf("pascal(%q) = %q, want %q", tc.in, got, tc.pascal)
		}
		if got := snake(tc.in); got != tc.snake {
			t.Errorf("snake(%q) = %q, want %q", tc.in, got, tc.snake)
		}
	}
	res := translate(t, "x86_64-windows", map[string]string{"n.h": `struct stat { int type; int fn; };
int stat(const char *path, struct stat *buf);
#define STAT_MAX 3
int stat_max(int match);
typedef int Target;
`}, Import{Name: "n", Headers: []string{"n.h"}})
	wantLines(t, res.Modules[0],
		"export struct StatStruct {\n    export type_: i32\n    export fn_: i32\n}",
		"export extern stat(path: *u8, buf: *StatStruct): i32",
		"export extern stat_max(match_: i32): i32",
		"export type TargetType = i32",
		"export const StatMaxConst: i32 = 3")
}

// TestCImport_TwoImportsOneIdentity: a header two imports share is bound
// once, in the first; the second's binding imports the first's module.
func TestCImport_TwoImportsOneIdentity(t *testing.T) {
	files := map[string]string{
		"common.h": "#ifndef COMMON_H\n#define COMMON_H\ntypedef struct Shared { int v; } Shared;\n#endif\n",
		"one.h":    "#include <common.h>\nint one(Shared *s);\n",
		"two.h":    "#include <common.h>\nint two(Shared s);\n",
	}
	res := translate(t, "x86_64-windows", files, Import{Name: "one", Headers: []string{"one.h"}}, Import{Name: "two", Headers: []string{"two.h"}})
	wantLines(t, res.Modules[0], "export struct Shared {", "export extern one(s: *Shared): i32")
	wantLines(t, res.Modules[1], "import c.one", "export extern two(s: one.Shared): i32")
	if strings.Contains(res.Modules[1], "export struct Shared") {
		t.Error("Shared is declared twice")
	}
}

// TestCImport_Deterministic: two translations of one request give the
// same bytes.
func TestCImport_Deterministic(t *testing.T) {
	files := map[string]string{"a.h": sampleHeader}
	im := Import{Name: "a", Headers: []string{"a.h"}, Macros: []string{"int FM(int x)"}}
	a := translate(t, "x86_64-windows", files, im)
	b := translate(t, "x86_64-windows", files, im)
	if a.Modules[0] != b.Modules[0] || a.Shim != b.Shim {
		t.Error("two translations differ")
	}
}

// TestCImport_HeaderError: a missing header is an *Error with Clang's
// command and stderr (the driver's E0001).
func TestCImport_HeaderError(t *testing.T) {
	tl, rd := clangFor(t)
	dir := t.TempDir()
	_, err := Translate(context.Background(), tl, rd, Request{Target: "x86_64-windows", Triple: ParseTriple("x86_64-windows"),
		Imports: []Import{{Name: "m", Headers: []string{"missing.h"}}}, Include: []string{dir}, WorkDir: filepath.Join(dir, "w")})
	ce, ok := err.(*Error)
	if !ok || !strings.Contains(ce.Stderr, "missing.h") || ce.Command == "" {
		t.Fatalf("err %v", err)
	}
}

// TestCTypeParse: Clang's type spellings.
func TestCTypeParse(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"int", "int"}, {"unsigned long long", "unsigned long long"}, {"long unsigned int", "unsigned long"},
		{"const char *", "char *"}, {"char *const *", "char * *"}, {"struct S *", "struct S *"},
		{"int (*)(void *, int)", "int (void *, int) *"}, {"char[16]", "char[16]"}, {"int (*[4])(void)", "int () *[4]"},
		{"union (unnamed union at a.h:2:54)", "union (unnamed at a.h:2:54)"}, {"void (*)(int, ...)", "void (int, ...) *"},
		{"int [2][3]", "int[3][2]"},
		{"BOOLEAN (*)(PVOID, SIZE_T) __attribute__((stdcall))", "BOOLEAN (PVOID, SIZE_T) *"},
		{"ATOM (LPCSTR) __attribute__((cdecl))", "ATOM (LPCSTR)"},
		{"int (int) __attribute__((vectorcall))", "<bad int (int) __attribute__((vectorcall))>"},
	} {
		if got := parseType(tc.in).String(); got != tc.want {
			t.Errorf("parseType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestCImport_Negatives: a dependency found under search directory k
// makes the same path under each earlier directory a negative lookup,
// except a path the unit read itself (#include_next: Clang's stdint.h,
// then the sysroot's).
func TestCImport_Negatives(t *testing.T) {
	root, res, sys := filepath.FromSlash("/p"), filepath.FromSlash("/r/include"), filepath.FromSlash("/s")
	deps := []string{filepath.Join(root, "a.h"), filepath.Join(res, "stdint.h"), filepath.Join(sys, "stdint.h"), filepath.Join(sys, "w.h")}
	got := Negatives(deps, []string{root, res, sys})
	want := []string{filepath.Join(root, "stdint.h"), filepath.Join(root, "w.h"), filepath.Join(res, "w.h")}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("negatives %q, want %q", got, want)
	}
}

// TestCImport_NegativesNested: where search directories nest, a file
// counts as found through each directory holding it, by its name under
// that one: inc/sub/y.h is "sub/y.h" through inc, which root/sub/y.h
// would shadow, as well as "y.h" through inc/sub.
func TestCImport_NegativesNested(t *testing.T) {
	root := filepath.FromSlash("/p")
	inc, sub := filepath.Join(root, "inc"), filepath.Join(root, "inc", "sub")
	got := Negatives([]string{filepath.Join(sub, "y.h")}, []string{root, inc, sub})
	want := []string{filepath.Join(root, "sub", "y.h"), filepath.Join(root, "y.h"), filepath.Join(inc, "y.h")}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("negatives %q, want %q", got, want)
	}
}

// TestCImport_NegativesExisting: a path that exists when the negatives are
// taken was not picked by the unit (b/x.h's quoted "y.h" found beside it
// before a/y.h), so it is no negative; one that is absent is.
func TestCImport_NegativesExisting(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	for _, p := range []string{filepath.Join(a, "y.h"), filepath.Join(b, "x.h"), filepath.Join(b, "y.h")} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := Negatives([]string{filepath.Join(b, "x.h"), filepath.Join(b, "y.h")}, []string{a, b})
	if want := []string{filepath.Join(a, "x.h")}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("negatives %q, want %q", got, want)
	}
}

// TestCImport_NegativesDirectory: a directory named like a dependency
// under an earlier search directory is a negative (Clang's search passed
// over it, and a header put in its place shadows the dependency), and a
// translation's entry hits while it is a directory and misses once a file
// takes its place.
func TestCImport_NegativesDirectory(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	shadow, dep := filepath.Join(a, "y.h"), filepath.Join(b, "y.h")
	if err := os.MkdirAll(shadow, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dep, []byte("#define Y 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Negatives([]string{dep}, []string{a, b}); strings.Join(got, "|") != shadow {
		t.Fatalf("negatives %q, want [%q]", got, shadow)
	}
	st, err := cache.Open(t.TempDir(), cache.Options{})
	if err != nil {
		t.Fatal(err)
	}
	key := source.HashBytes("test", []byte("key"))
	Save(st, key, &Result{Modules: []string{"m"}, Deps: []string{dep}, SearchDirs: []string{a, b}})
	if _, ok := Load(st, key); !ok {
		t.Fatal("a directory named like the header made the entry miss")
	}
	if err := os.Remove(shadow); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shadow, []byte("#define Y 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := Load(st, key); ok {
		t.Error("a header put in the directory's place still hit the entry")
	}
}
