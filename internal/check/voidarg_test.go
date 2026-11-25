// Why: C's `void *` is `*void`, and a program must be able to build one from
// a typed pointer, a null or an address; that takes `void` as a written type
// argument, which a generic call refused while it inferred the same argument.
// What: TestExplicitVoidTypeArgument.
// How: each case checks a body against a few generic pointer functions shaped
// as std.raw's are, and compares codes and positions.

package check

import "testing"

// TestExplicitVoidTypeArgument: a type argument written at a generic call
// may be void, as the one inference chooses for a `*void` argument may. It
// was E0301 "`void` is only a result type", so `raw.Null[void]()` and
// `raw.PtrCast[T, void](p)` could not be written. `never`, a generic type's
// argument and a declared local keep their rule.
func TestExplicitVoidTypeArgument(t *testing.T) {
	const decls = "struct Obj {\n\tx: int\n}\nstruct Box[T] {\n\tv: *T\n}\nRetype[T, U](p: *T): *U {\n\t_ = p\n\tfor {\n\t}\n}\nNull[T](): *T {\n\tfor {\n\t}\n}\nAddr[T](p: *T): uint {\n\t_ = p\n\treturn 0\n}\n"
	runBodyCases(t, []bodyCase{
		{"written where inferred", decls + "F(p: *Obj, q: *void): uint {\n\tv: *void = Retype[Obj, void](p)\n\to: *Obj = Retype[void, Obj](q)\n\tn := Null[void]()\n\treturn Addr(v) + Addr[void](n) + Addr(o)\n}\n", ""},
		{"never is still a result type", decls + "F(): uint {\n\treturn Addr(Null[never]())\n}\n", "E0301@21:19"},
		{"a generic type's argument", decls + "F(b: Box[void]): uint {\n\treturn Addr(b.v)\n}\n", "E0301@20:10"},
		{"a declared local", decls + "F(p: *Obj) {\n\tv: void = Retype[Obj, void](p)\n}\n", "E0301@21:5"},
	})
}
