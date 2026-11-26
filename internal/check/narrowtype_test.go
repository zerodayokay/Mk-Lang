// Why: inside `if T is X` the reference says T is X, so a type written T in
// the branch must read as X wherever a value of type T does, or a call such
// as Id[T](x) expects a T where the branch gives an X.
// What: TestNarrowedTypeExpr, TestNarrowedChain and TestNarrowedTypeExprRaw.
// How: body cases over the forms a type is written in (a type argument, a
// local's declared type, a conversion, a nested generic, a method of a
// generic owner), an `else if T is` chain and the else branch, which keeps
// T opaque; a narrowing to a parameter that is narrowed in turn; then
// std.raw's integer-bound intrinsics over a stub of std.raw.

package check

import (
	"testing"

	"mk/internal/types"
)

func TestNarrowedTypeExpr(t *testing.T) {
	const id = "Id[T](x: T): T {\n\treturn x\n}\n\n"                                                  // lines 1-4
	const box = "struct Box[T: Plain] {\n\tv: T\n}\n\nBox[T].Get(&self): T {\n\treturn self.v\n}\n\n" // lines 5-12
	runBodyCases(t, []bodyCase{
		{"type argument", id + "F[T: Plain](x: T): T {\n\tif T is u32 {\n\t\treturn Id[T](x) * 2\n\t}\n\treturn x\n}\n", ""},
		{"declared local", id + "F[T: Plain](x: T): T {\n\tif T is u32 {\n\t\tvar b: T = x + 1\n\t\treturn b\n\t}\n\treturn x\n}\n", ""},
		{"conversion", "F[T: Plain](x: T, n: u8): T {\n\tif T is u32 {\n\t\treturn T(n) + x\n\t}\n\treturn x\n}\n", ""},
		{"cast target", "F[T: Plain](x: T, n: i64): T {\n\tif T is u32 {\n\t\treturn Cast[T](n) + x\n\t}\n\treturn x\n}\n", ""},
		{"nested generic and method", id + box + "F[T: Plain](x: T): T {\n\tif T is u32 {\n\t\treturn Id[Box[T]](Box[T](v: x)).Get() + 1\n\t}\n\treturn x\n}\n", ""},
		{"else if chain", id + "F[T: Plain](x: T): T {\n\tif T is u32 {\n\t\treturn Id[T](x) + 1\n\t} else if T is i64 {\n\t\tvar f: T = x - 1\n\t\treturn Id[T](f) * 10\n\t}\n\treturn Id[T](x)\n}\n", ""},
		// The else branch is not narrowed: T there is any type.
		{"else branch keeps T", id + "F[T: Plain](x: T): T {\n\tif T is u32 {\n\t\treturn x\n\t} else {\n\t\treturn Id[T](x) + 1\n\t}\n}\n", "E0703@9:21"},
		// A struct is constructed by its name: T(..) for a struct X stays
		// E0301, not a one-value conversion.
		{"struct target", "struct Pt {\n\tx: int\n}\n\nF[T: Plain](p: T): int {\n\tif T is Pt {\n\t\tq := T(x: 1)\n\t\treturn q.x\n\t}\n\treturn 0\n}\n", "E0301@7:8"},
		{"conversion outside a branch", "F[T: Plain](n: u8): T {\n\treturn T(n)\n}\n", "E0301@2:9"},
		// The narrowed argument is a closed type, judged as one written so.
		{"narrowed argument against a closed parameter", id + "F[T: Plain](x: T, y: i64): i64 {\n\tif T is u32 {\n\t\treturn Id[T](y)\n\t}\n\treturn y\n}\n", "E0301@7:10 E0301@7:16"},
	})
}

// TestNarrowedChain: inside `if T is U { if U is u32 { .. } }` T is u32,
// for its values and the types written T alike; between the two tests T is
// U, still opaque.
func TestNarrowedChain(t *testing.T) {
	const id = "Id[T](x: T): T {\n\treturn x\n}\n\n" // lines 1-4
	runBodyCases(t, []bodyCase{
		{"type argument and value", id + "F[T: Plain, U: Plain](x: T): int {\n\tif T is U {\n\t\tif U is u32 {\n\t\t\ta := Id[T](x) + 1\n\t\t\tvar b: T = x + 2\n\t\t\treturn int(a + b + Cast[T](3))\n\t\t}\n\t}\n\treturn 0\n}\n", ""},
		{"three steps", id + "F[T: Plain, U: Plain, V: Plain](x: T): T {\n\tif T is U {\n\t\tif U is V {\n\t\t\tif V is u16 {\n\t\t\t\tvar b: [2]T = [x, x + 1]\n\t\t\t\treturn Id[T](b[1])\n\t\t\t}\n\t\t}\n\t}\n\treturn x\n}\n", ""},
		{"through a constructed type", id + "F[T: Plain, U: Plain](x: T): int {\n\tif T is ?U {\n\t\tif U is u8 {\n\t\t\tvar y: T = some(Id[U](3) + 1)\n\t\t\treturn int(y else { 0 }) + int(x else { 9 })\n\t\t}\n\t}\n\treturn -1\n}\n", ""},
		{"the result", "F[T: Plain, U: Plain](x: T): T {\n\tif T is U {\n\t\tif U is u32 {\n\t\t\treturn x * 3\n\t\t}\n\t}\n\treturn x\n}\n", ""},
		// Outside the inner test T is U, which has no bound for a literal.
		{"between the tests", "F[T: Plain, U: Plain](x: T): T {\n\tif T is U {\n\t\tif U is u32 {\n\t\t\treturn x\n\t\t}\n\t\treturn x + 1\n\t}\n\treturn x\n}\n", "E0703@6:14"},
	})
}

// TestNarrowedTypeExprRaw: std.raw's ReadUnaligned[T] inside `if T is u32`
// takes a *u32 and returns a u32, and its integer bound is judged at u32;
// in `if T is f64` it is E0701, as ReadUnaligned[f64] is.
func TestNarrowedTypeExprRaw(t *testing.T) {
	w := newWorld(t, true)
	raw := w.add("std.raw", opt{std: true}, rawDecls)
	if len(raw.diags()) > 0 {
		t.Fatalf("std.raw:\n%s", dump(w, raw.diags()))
	}
	w.c.known = w.rs.Known()
	if w.c.known.Atomic == types.NoItem {
		t.Fatal("Known.Atomic not set")
	}
	const imp = "import std.raw\n\n"
	for i, tc := range []bodyCase{
		{"u32", imp + "F[T: Plain](p: *u32, x: T): T {\n\tif T is u32 {\n\t\tunsafe \"test\" {\n\t\t\treturn raw.ReadUnaligned[T](p) + x\n\t\t}\n\t}\n\treturn x\n}\n", ""},
		{"f64", imp + "F[T: Plain](p: *f64, x: T): T {\n\tif T is f64 {\n\t\tunsafe \"test\" {\n\t\t\treturn raw.ReadUnaligned[T](p) + x\n\t\t}\n\t}\n\treturn x\n}\n", "E0701@6:11"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := w.add("std.n"+string(rune('a'+i)), opt{std: true}, tc.src)
			var got []string
			for _, d := range append(b.diags(), b.bodyDiags(t)...) {
				if s := d.Code.String(); s != "E0208" && s != "E1101" {
					got = append(got, s+"@"+w.lc(d.At))
				}
			}
			if g := sortStrings(got); g != tc.want {
				t.Errorf("diagnostics %q, want %q", g, tc.want)
			}
		})
	}
}
