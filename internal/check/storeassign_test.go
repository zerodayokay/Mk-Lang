// Why: an E!T is kept only by a binding that writes its type, so an
// assignment must store one only where a binding is, as a construction's
// field and an array literal already refuse it.
// What: TestE0802_AssignedFallible.
// How: body cases over every assignment target (a field, an element, a
// record member, a dereference, a field of an implicit self) with a call's
// and a stored E!T as the value, refused, and a typed local as the target,
// accepted (a global is never assigned: borrow's E0507).

package check

import "testing"

func TestE0802_AssignedFallible(t *testing.T) {
	const pre = "error E\n\nF(): E!int {\n\treturn 1\n}\n\nstruct S {\n\tr: E!int\n}\n\n" // lines 1-10
	runBodyCases(t, []bodyCase{
		{"field from a call", pre + "G(s: mut S) {\n\ts.r = F()\n}\n", "E0802@12:8"},
		{"field from a typed local", pre + "G(s: mut S) {\n\tvar v: E!int = F()\n\ts.r = v\n}\n", "E0802@13:8"},
		{"field from a field", pre + "G(s: mut S, o: &S) {\n\ts.r = o.r\n}\n", "E0802@12:8"},
		{"parenthesized field", pre + "G(s: mut S) {\n\t(s.r) = F()\n}\n", "E0802@12:10"},
		{"element", pre + "G(xs: mut [2](E!int)) {\n\txs[1] = F()\n}\n", "E0802@12:10"},
		{"record member", pre + "G(t: mut (r: E!int, n: int)) {\n\tt.r = F()\n}\n", "E0802@12:8"},
		{"dereference", pre + "G(p: mut (E!int)) {\n\t*p = F()\n}\n", "E0802@12:7"},
		{"method's field", pre + "S.Set(mut self) {\n\tself.r = F()\n}\n", "E0802@12:11"},
		{"typed local from a call", pre + "G(): int {\n\tvar v: E!int = F()\n\tv = F()\n\treturn v else { 0 }\n}\n", ""},
		{"typed local from a local", pre + "G(): int {\n\tvar v: E!int = F()\n\tw: E!int = F()\n\tv = w\n\treturn v else { 0 }\n}\n", ""},
		{"whole struct holding one", pre + "G(s: mut S, o: S) {\n\t*s = o\n}\n", ""},
		{"a fn literal's capture", pre + "Do(const f: fn()) {\n\tf()\n}\n\nG(): int {\n\tvar v: E!int = F()\n\ta := v else { 0 }\n\tDo(fn() { v = F() })\n\treturn a + (v else { 0 })\n}\n", "E0802@18:16"},
		{"handled value into a field", pre + "G(s: mut S, n: mut int) {\n\t*n = F() else { 0 }\n\ts.r = s.r\n}\n", "E0802@13:8"},
	})
}
