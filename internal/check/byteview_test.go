// Why: mem.AsBytes and mem.SliceAsBytes return a read-only []u8 view of a
// value or slice without copying, so check must refuse padded layouts,
// reference-holding types, interior mutability, raw pointers, enums,
// optionals, error unions and unclosed type parameters.
//
// What: TestByteViewBounds.
//
// How: checks accepted types (bool, char, integers, floats, void, arrays,
// unpadded structs, packed structs, narrowed generics) and rejected types
// (padded structs, tail padding, align(N) padding, packed(2) padding,
// nested padding, references, slices, str, raw pointers, fn, optionals,
// enums, unique/linear structs, Cell, raw.Atomic, open generics, fn values)
// against std.mem's AsBytes and SliceAsBytes.

package check

import "testing"

const memByteViewDecls = `
export AsBytes[T](p: &T): []u8 from p
export SliceAsBytes[T](xs: []T): []u8 from xs
`

func TestByteViewBounds(t *testing.T) {
	w := newWorld(t, true)
	mem := w.add("std.mem", opt{std: true}, memByteViewDecls)
	if len(mem.diags()) > 0 {
		t.Fatalf("std.mem:\n%s", dump(w, mem.diags()))
	}
	raw := w.add("std.raw", opt{std: true}, rawAtomicDecls)
	if len(raw.diags()) > 0 {
		t.Fatalf("std.raw:\n%s", dump(w, raw.diags()))
	}
	w.c.known = w.rs.Known()

	const imp = "import std.mem\nimport std.raw\n\n"
	for i, tc := range []bodyCase{
		{"bool and char", imp + "F(b: &bool, c: &char): int {\n\treturn mem.AsBytes(b).Len() + mem.AsBytes(c).Len()\n}\n", ""},
		{"numbers", imp + "F(a: &u8, b: &i64, c: &f32, d: &f64): int {\n\treturn mem.AsBytes(a).Len() + mem.AsBytes(b).Len() + mem.AsBytes(c).Len() + mem.AsBytes(d).Len()\n}\n", ""},
		{"arrays and slices", imp + "F(a: &[4]u32, xs: []f32): int {\n\treturn mem.AsBytes(a).Len() + mem.SliceAsBytes(xs).Len()\n}\n", ""},
		{"unpadded plain struct", imp + "struct Pair {\n\ta: u16\n\tb: u16\n}\nstruct Nested {\n\tp: Pair\n\tb: bool\n\tpad: u8\n}\nF(n: &Nested, xs: []Nested): int {\n\treturn mem.AsBytes(n).Len() + mem.SliceAsBytes(xs).Len()\n}\n", ""},
		{"packed struct", imp + "packed struct Wire {\n\ttag: u8\n\tlen: u32\n\tflags: u16\n}\nF(w: &Wire): []u8 from w {\n\treturn mem.AsBytes(w)\n}\n", ""},
		{"narrowed generic", imp + "F[T](p: &T): int {\n\tif T is u32 {\n\t\treturn mem.AsBytes(p).Len()\n\t}\n\treturn 0\n}\n", ""},
		{"interior padded struct", imp + "struct Padded {\n\ta: u8\n\tb: u32\n}\nF(p: &Padded): []u8 from p {\n\treturn mem.AsBytes(p)\n}\n", "E0701@9:9"},
		{"tail padded struct", imp + "struct Tail {\n\ta: u32\n\tb: u8\n}\nF(xs: []Tail): []u8 from xs {\n\treturn mem.SliceAsBytes(xs)\n}\n", "E0701@9:9"},
		{"align(16) padded struct", imp + "align(16) struct Al {\n\ta: u32\n}\nF(p: &Al): []u8 from p {\n\treturn mem.AsBytes(p)\n}\n", "E0701@8:9"},
		{"packed(2) padded struct", imp + "packed(2) struct Half {\n\ttag: u8\n\tlen: u32\n}\nF(p: &Half): []u8 from p {\n\treturn mem.AsBytes(p)\n}\n", "E0701@9:9"},
		{"nested padded struct", imp + "struct Inner {\n\ta: u8\n\tb: u32\n}\nstruct Outer {\n\tx: Inner\n}\nF(p: &Outer): []u8 from p {\n\treturn mem.AsBytes(p)\n}\n", "E0701@12:9"},
		{"padded array elem", imp + "struct P {\n\ta: u8\n\tb: u16\n}\nF(p: &[2]P): []u8 from p {\n\treturn mem.AsBytes(p)\n}\n", "E0701@9:9"},
		{"ref field", imp + "struct R {\n\tr: &u8\n}\nF(p: &R): []u8 from p {\n\treturn mem.AsBytes(p)\n}\n", "E0701@8:9"},
		{"str field", imp + "struct S {\n\ts: str\n}\nF(xs: []S): []u8 from xs {\n\treturn mem.SliceAsBytes(xs)\n}\n", "E0701@8:9"},
		{"slice field", imp + "struct S {\n\tb: []u8\n}\nF(p: &S): []u8 from p {\n\treturn mem.AsBytes(p)\n}\n", "E0701@8:9"},
		{"raw ptr", imp + "F(p: &*u8): []u8 from p {\n\treturn mem.AsBytes(p)\n}\n", "E0701@5:9"},
		{"fn value", imp + "F(f: &fn()): []u8 from f {\n\treturn mem.AsBytes(f)\n}\n", "E0701@5:9"},
		{"optional", imp + "F(o: &?u32): []u8 from o {\n\treturn mem.AsBytes(o)\n}\n", "E0701@5:9"},
		{"enum", imp + "enum E: u32 {\n\tA\n\tB\n}\nF(e: &E): []u8 from e {\n\treturn mem.AsBytes(e)\n}\n", "E0701@9:9"},
		{"linear struct", imp + "linear struct L {\n\tx: u32\n}\nF(l: &L): []u8 from l {\n\treturn mem.AsBytes(l)\n}\n", "E0701@8:9"},
		{"atomic field", imp + "struct A {\n\ta: raw.Atomic[u32]\n}\nF(p: &A): []u8 from p {\n\treturn mem.AsBytes(p)\n}\n", "E0701@8:9"},
		{"open generic", imp + "F[T](p: &T): []u8 from p {\n\treturn mem.AsBytes(p)\n}\n", "E0701@5:9"},
		{"fn value ref", imp + "struct P {\n\ta: u8\n\tb: u32\n}\nF() {\n\t_ = mem.AsBytes[P]\n}\n", "E0701@9:6"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := w.add("std.bv"+string(rune('a'+i)), opt{std: true}, tc.src)
			var got []string
			for _, d := range append(b.diags(), b.bodyDiags(t)...) {
				if s := d.Code.String(); s != "E0208" {
					got = append(got, s+"@"+w.lc(d.At))
				}
			}
			if g := sortStrings(got); g != tc.want {
				t.Fatalf("diagnostics %q, want %q\n%s", g, tc.want, dump(w, append(b.diags(), b.bodyDiags(t)...)))
			}
		})
	}
}
