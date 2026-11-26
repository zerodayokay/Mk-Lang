// Why: C headers are full of edge cases (odd prototypes, wide enumerators,
// unusual alignment, bitfields, SSE unions, missing headers) that must fail
// cleanly or map correctly.
// What: TestParseProto, TestCImport_Enum, _PointerOnly, _FuncPtrNullable,
// _Bitfield, _OpaqueSSE, _HeaderMissing, TestDepHasher and _Exists.
// How: small headers through translate, and the cache's dependency hasher.

package cimport

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mk/internal/cache"
)

// TestParseProto: a prototype with surrounding blanks parses as the
// trimmed one, and a malformed one is refused, never a panic.
func TestParseProto(t *testing.T) {
	for _, tc := range []struct {
		in     string
		name   string
		params int
		ok     bool
	}{
		{"  int ONE()", "ONE", 0, true},
		{" int MAX(int a, int b) ", "MAX", 2, true},
		{"\tint MAX(int a, int b)\n", "MAX", 2, true},
		{"int F(void)", "F", 0, true},
		{"  ()", "", 0, false},
		{" ) ", "", 0, false},
		{"(", "", 0, false},
		{"", "", 0, false},
	} {
		name, _, ps, ok := parseProto(tc.in)
		if ok != tc.ok || name != tc.name || len(ps) != tc.params {
			t.Errorf("parseProto(%q) = %q, %d params, %v; want %q, %d, %v", tc.in, name, len(ps), ok, tc.name, tc.params, tc.ok)
		}
	}
}

// TestCImport_Enum: an enumerator whose initializer is not of type
// int (Clang wraps its ConstantExpr in an ImplicitCastExpr) keeps its
// value, and the implicit ones after it count from it.
func TestCImport_Enum(t *testing.T) {
	res := translate(t, "x86_64-windows", map[string]string{"e.h": "enum E { A = 1u << 4, B, C = sizeof(int), D = 'x', F = (unsigned char)3, G };\n"},
		Import{Name: "e", Headers: []string{"e.h"}})
	wantLines(t, res.Modules[0], "export const A: E = 16", "export const B: E = 17", "export const C: E = 4",
		"export const D: E = 120", "export const F: E = 3", "export const G: E = 4")
}

// TestCImport_PointerOnly: a record whose alignment no Mk integer
// has is named only behind a pointer: a struct holding it by value is not
// transparent (here it is itself pointer-only), and a function taking or
// returning either by value is not bound, while one taking pointers is.
func TestCImport_PointerOnly(t *testing.T) {
	res := translate(t, "x86_64-windows", map[string]string{"k.h": `typedef struct A { _Alignas(16) int v; } A;
typedef struct B { A a; int y; } B;
typedef struct C { int x; struct { _Alignas(16) int w; } in; } C;
B k_mkb(void);
int k_taka(A a);
int k_ptra(A *a, B *b);
`}, Import{Name: "k", Headers: []string{"k.h"}})
	text := res.Modules[0]
	wantLines(t, text, "export struct A {\n}", "export struct B {\n}", "export extern k_ptra(a: *A, b: *B): i32")
	for _, bad := range []string{"export a: A", "k_mkb", "k_taka", "export in_: "} {
		if strings.Contains(text, bad) {
			t.Errorf("binding holds %q\n%s", bad, text)
		}
	}
}

// TestCImport_FuncPtrNullable: a C function pointer Mk reads from C (a struct
// field, a result, a typedef, a pointer to one) is an optional fn type, ?fn,
// whose none is NULL; an extern's own parameter, which Mk passes, stays a
// plain fn type, and that fn's own function-pointer parameters are ?fn.
func TestCImport_FuncPtrNullable(t *testing.T) {
	res := translate(t, "x86_64-windows", map[string]string{"f.h": `typedef int (*cb_t)(int);
typedef struct CB { int (*f)(int); cb_t g; } CB;
CB k_cb(void);
cb_t k_get(void);
int k_reg(cb_t cb, int (*h)(int (*inner)(int)));
void k_out(cb_t *out);
`}, Import{Name: "f", Headers: []string{"f.h"}})
	wantLines(t, res.Modules[0],
		"export type CbT = ?fn(i32): i32",
		"export struct Cb {\n    export f: ?fn(i32): i32\n    export g: ?fn(i32): i32\n}",
		"export extern k_cb(): Cb",
		"export extern k_get(): CbT",
		"export extern k_reg(cb: fn(i32): i32, h: fn(?fn(i32): i32): i32): i32",
		"export extern k_out(out: *CbT)")
	if strings.Contains(res.Modules[0], "f: fn(") {
		t.Errorf("a field is a fn type\n%s", res.Modules[0])
	}
}

// TestCImport_Bitfield: a signed bitfield reads sign-extended as
// its declared type and is set from it; an unsigned one is unchanged.
func TestCImport_Bitfield(t *testing.T) {
	res := translate(t, "x86_64-windows", map[string]string{"b.h": "typedef struct S { int sb : 3; unsigned ub : 5; } S;\nstruct T { short x; signed char sc : 2; };\nunion U { int x; int ub : 4; };\n"},
		Import{Name: "b", Headers: []string{"b.h"}})
	wantLines(t, res.Modules[0],
		"export S.Sb(&self): i32 {\n    return Trunc[i32](self.bits0 << 29) >> 29\n}",
		"export S.SetSb(mut self, v: i32) {\n    self.bits0 = (self.bits0 & 0xfffffff8) | ((Trunc[u32](i64(v)) & 0x7) << 0)\n}",
		"export S.Ub(&self): u32 {\n    return (self.bits0 >> 3) & 0x1f\n}",
		"export T.Sc(&self): i8 {\n    return Trunc[i8](self.bits2 << 6) >> 6\n}",
		"export U.Ub(&self): i32 {",
		"return i32(Trunc[i8](raw.ReadUnaligned(")
}

// TestCImport_OpaqueSSE: on SysV, a union of floats is class SSE
// in C but its opaque form is integer bytes, so a function taking or
// returning it by value (or a struct holding it) is not bound there; by
// pointer it is. Windows x64 passes it by size, so there it is bound.
func TestCImport_OpaqueSSE(t *testing.T) {
	h := map[string]string{"u.h": `union U { double d; float f; };
union I { long long a; int b; };
struct W { union U u; };
double u_get(union U u);
union U u_make(double d);
double w_get(struct W w);
long long i_get(union I i);
double u_getp(union U *u);
`}
	lin := translate(t, "x86_64-linux", h, Import{Name: "u", Headers: []string{"u.h"}}).Modules[0]
	for _, bad := range []string{"u_get(", "u_make(", "w_get("} {
		if strings.Contains(lin, "export extern "+bad) {
			t.Errorf("linux binds %s\n%s", bad, lin)
		}
	}
	wantLines(t, lin, "export extern i_get(i: I): i64", "export extern u_getp(u: *U): f64")
	win := translate(t, "x86_64-windows", h, Import{Name: "u", Headers: []string{"u.h"}}).Modules[0]
	wantLines(t, win, "export extern u_get(u: U): f64", "export extern u_make(d: f64): U", "export extern w_get(w: W): f64")
}

// TestCImport_HeaderMissing: a header that is not found is an
// *Error with Missing (a vendored binding may stand in); a C error in a
// header that is found is not.
func TestCImport_HeaderMissing(t *testing.T) {
	tl, rd := clangFor(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.h"), []byte("int f(void) oops;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		h       string
		missing bool
	}{{"missing.h", true}, {"bad.h", false}} {
		_, err := Translate(context.Background(), tl, rd, Request{Target: "x86_64-windows", Triple: ParseTriple("x86_64-windows"),
			Imports: []Import{{Name: "m", Headers: []string{tc.h}}}, Include: []string{dir}, WorkDir: filepath.Join(dir, "w-"+tc.h)})
		ce, ok := err.(*Error)
		if !ok || ce.Missing != tc.missing {
			t.Errorf("%s: err %v, want an *Error with Missing %v", tc.h, err, tc.missing)
		}
	}
}

// TestDepHasher: a dependency whose stat is unchanged and not racy
// is not read again; an edit is read and gives the new hash.
func TestDepHasher(t *testing.T) {
	st, err := cache.Open(t.TempDir(), cache.Options{})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "h.h")
	old := time.Now().Add(-time.Hour)
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	write("int a;\n")
	d := NewDepHasher(st)
	h1, ok := d.Hash(p)
	if !ok || d.reads != 1 {
		t.Fatalf("first hash: ok %v, reads %d", ok, d.reads)
	}
	d.Save()
	d2 := NewDepHasher(st)
	if h, ok := d2.Hash(p); !ok || h != h1 || d2.reads != 0 {
		t.Errorf("memo hash: ok %v, same %v, reads %d; want a hit with no read", ok, h == h1, d2.reads)
	}
	write("int ab;\n")
	if h, ok := d2.Hash(p); !ok || h == h1 || d2.reads != 1 {
		t.Errorf("after an edit: ok %v, changed %v, reads %d", ok, h != h1, d2.reads)
	}
}

// TestDepHasher_Exists: a DepHasher answers a negative lookup once, so it
// keeps its answer for the build; the next build's DepHasher sees a file
// added since.
func TestDepHasher_Exists(t *testing.T) {
	p := filepath.Join(t.TempDir(), "h.h")
	d := NewDepHasher(nil)
	if d.Exists(p) {
		t.Fatal("a missing file exists")
	}
	if err := os.WriteFile(p, []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d.Exists(p) {
		t.Error("the same DepHasher stat'ed the path again")
	}
	if !NewDepHasher(nil).Exists(p) {
		t.Error("a new DepHasher does not see the added file")
	}
}
