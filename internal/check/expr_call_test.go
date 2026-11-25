// Why: calls, inference and construction are the bulk of body checking.
// What: tests of argument counts and names, inference, bounds at a call,
// members and privacy, receiver adjustment, construction, literals and
// late literal arguments.
// How: checks small bodies and compares codes and the recorded call data.

package check

import (
	"runtime"
	"strings"
	"testing"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/types"
)

const pointSrc = "struct Point {\n\tx: int\n\ty: int\n}\n\n"

func TestE0304_ArgumentCount(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"accepts", "Add(a: int, b: int): int {\n\treturn a + b\n}\n\nF(): int {\n\treturn Add(1, Add(2, 3))\n}\n", ""},
		{"too few", "Add(a: int, b: int): int {\n\treturn a + b\n}\n\nF(): int {\n\treturn Add(1)\n}\n", "E0304@6:9"},
		{"too many", "Add(a: int, b: int): int {\n\treturn a + b\n}\n\nF(): int {\n\treturn Add(1, 2, 3)\n}\n", "E0304@6:9"},
		{"method", pointSrc + "Point.Sum(&self, k: int): int {\n\treturn self.x + self.y + k\n}\n\nF(p: Point): int {\n\treturn p.Sum()\n}\n", "E0304@11:9"},
		{"fn value", "F(g: fn(int): int): int {\n\treturn g(1, 2)\n}\n", "E0304@2:9"},
		{"argument type", "Add(a: int, b: int): int {\n\treturn a + b\n}\n\nF(): int {\n\treturn Add(1, true)\n}\n", "E0301@6:16"},
	})
}

func TestE0305_FieldNames(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"accepts", pointSrc + "F(): Point {\n\treturn Point(y: 2, x: 1)\n}\n", ""},
		{"Short", pointSrc + "Short(): Point {\n\treturn Point(x: 1)\n}\n", "E0305@7:9"},
		{"positional", pointSrc + "F(x: int, y: int): Point {\n\treturn Point(x, y)\n}\n", "E0305@7:9"},
		{"unknown", pointSrc + "F(): Point {\n\treturn Point(x: 1, y: 2, z: 3)\n}\n", "E0305@7:9"},
		{"repeated", pointSrc + "F(): Point {\n\treturn Point(x: 1, x: 2, y: 3)\n}\n", "E0305@7:9"},
		{"payload-free error", "error Denied\n\nF(): Denied {\n\treturn Denied()\n}\n", "E0305@4:9"},
		{"bare error", "error Denied\n\nF(): Denied {\n\treturn Denied\n}\n", ""},
		{"variant", "enum Shape {\n\tCircle(r: f64)\n\tEmpty\n}\n\nF(): Shape {\n\treturn Shape.Circle(r: 1.0)\n}\n\nG(): Shape {\n\treturn Shape.Empty\n}\n", ""},
		{"variant positional", "enum Shape {\n\tCircle(r: f64)\n\tEmpty\n}\n\nF(): Shape {\n\treturn Shape.Circle(1.0)\n}\n", "E0305@7:9"},
		{"field type", pointSrc + "F(): Point {\n\treturn Point(x: 1, y: true)\n}\n", "E0301@7:24"},
	})
	t.Run("proven fix for positional", func(t *testing.T) {
		w := newWorld(t, true)
		b := checkBody(t, w, "E0305@7:9", pointSrc+"F(x: int, y: int): Point {\n\treturn Point(x, y)\n}\n")
		d := b.bodyDiags(t)[0]
		if len(d.Fixes) != 1 || d.Fixes[0].Class != diag.Proven || len(d.Fixes[0].Edits) != 2 {
			t.Errorf("fixes %+v", d.Fixes)
		}
	})
}

func TestNamedArguments(t *testing.T) {
	w := newWorld(t, true)
	b := checkBody(t, w, "E0305@6:11", "F(x: int, y: int): int {\n\treturn x + y\n}\n\nG(): int {\n\treturn F(x: 1, y: 2)\n}\n")
	d := b.bodyDiags(t)[0]
	if fact(d, "reason") != "named-argument" || len(d.Fixes) != 1 || d.Fixes[0].Class != diag.Proven {
		t.Errorf("%s", dump(w, []*diag.Diagnostic{d}))
	}
	w2 := newWorld(t, true)
	b2 := checkBody(t, w2, "E0305@6:11", "F(x: int, y: int): int {\n\treturn x + y\n}\n\nG(): int {\n\treturn F(y: 1, x: 2)\n}\n")
	if d := b2.bodyDiags(t)[0]; len(d.Fixes) != 0 {
		t.Errorf("out-of-order names got a fix")
	}
}

func TestE0711_CannotInfer(t *testing.T) {
	const parse = "Parse[T](s: str): T {\n\treturn Parse(s)\n}\n\n"
	runBodyCases(t, []bodyCase{
		{"unbound", parse + "F() {\n\tx := Parse(\"1\")\n}\n", "E0711@6:7"},
		{"from expected", parse + "F() {\n\tx: i32 = Parse(\"1\")\n}\n", ""},
		{"written", parse + "F(): i32 {\n\treturn Parse[i32](\"1\")\n}\n", ""},
		{"empty array", "F() {\n\tx := []\n\ty: [0]int = []\n}\n", "E0711@2:7"},
		{"from arguments", "Max[T: Plain](a: T, b: T): T {\n\treturn a\n}\n\nF(x: i32): i32 {\n\treturn Max(x, x)\n}\n", ""},
		{"disagreeing arguments", "Max[T: Plain](a: T, b: T): T {\n\treturn a\n}\n\nF(x: i32, y: i64): i32 {\n\treturn Max(x, y)\n}\n", "E0301@6:16"},
		// The value names its arguments; the bare form is E0711.
		{"Sort(xs, Less[int])", "Less[T: Plain](a: &T, b: &T): bool {\n\treturn true\n}\n\nSort[T](xs: mut []T, less: fn(&T, &T): bool) {\n}\n\nF(xs: mut []int) {\n\tSort(xs, Less[int])\n}\n", ""},
		{"Sort(xs, Less)", "Less[T: Plain](a: &T, b: &T): bool {\n\treturn true\n}\n\nSort[T](xs: mut []T, less: fn(&T, &T): bool) {\n}\n\nF(xs: mut []int) {\n\tSort(xs, Less)\n}\n", "E0711@9:11"},
		{"generic fn value without context", "Less[T: Plain](a: &T, b: &T): bool {\n\treturn true\n}\n\nF() {\n\tf := Less\n}\n", "E0711@6:7"},
		{"caller's parameter", "Id[T](x: &T): &T {\n\treturn x\n}\n\nG[T](y: &T): &T {\n\treturn Id(y)\n}\n", ""},
	})
}

func TestE0701_CallSite(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"Bad", "struct Circle {\n\tr: f64\n}\n\ninterface Shape {\n\tArea(&self): f64\n}\n\nMeasure[T: Shape](x: &T): f64 {\n\treturn x.Area()\n}\n\nBad(c: &Circle): f64 {\n\treturn Measure(c)\n}\n", "E0701@14:9"},
		{"Plain", "Dup[T: Plain](x: T): T {\n\treturn x\n}\n\nF(c: Cell[int]) {\n\td := Dup(c)\n}\n", "E0701@6:7"},
	})
}

const shapeIface = "interface Shape {\n\tArea(&self): f64\n}\n\nstruct Square {\n\tside: f64\n}\n\nstruct Circle {\n\tr: f64\n}\n\nimpl Shape for Square {\n\tArea(&self): f64 {\n\t\treturn self.side * self.side\n\t}\n}\n\n"

func TestE0702_NoImpl(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"Larger", shapeIface + "Larger[T: Shape](a: &T, b: &T): bool {\n\treturn a.Area() > b.Area()\n}\n\nF(s: Square): f64 {\n\treturn s.Area()\n}\n", ""},
		{"no impl", shapeIface + "F(c: Circle): f64 {\n\treturn c.Area()\n}\n", "E0702@20:11"},
	})
}

func TestE0703_NeedsBound(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"Biggest", "Biggest[T](a: &T, b: &T): bool {\n\treturn *a > *b\n}\n", "E0703@2:9"},
		{"method of an unbounded parameter", shapeIface + "M[T](a: &T): f64 {\n\treturn a.Area()\n}\n", "E0703@20:11"},
		{"field of a parameter", "M[T](a: &T): int {\n\treturn a.n\n}\n", "E0703@2:11"},
		{"index of a parameter", "M[T](a: &T): int {\n\treturn a[0]\n}\n", "E0703@2:9"},
		// A conversion of a parameter is E0703, not E0302.
		{"conversion of a parameter", "F[T](x: T): i64 {\n\treturn i64(x)\n}\n", "E0703@2:9"},
		{"Cast of a parameter", "F[T](x: T): i64 {\n\treturn Cast[i64](x)\n}\n", "E0703@2:9"},
		{"Trunc of a parameter", "F[T](x: T): u8 {\n\treturn Trunc[u8](x)\n}\n", "E0703@2:9"},
		{"Cast to a parameter", "F[T](x: i64): T {\n\treturn Cast[T](x)\n}\n", "E0703@2:9"},
	})
	t.Run("conversion facts", func(t *testing.T) {
		w := newWorld(t, true)
		b := checkBody(t, w, "E0703@2:9", "F[T](x: T): i64 {\n\treturn Cast[i64](x)\n}\n")
		d := b.bodyDiags(t)[0]
		if fact(d, "param") != "T" || fact(d, "operation") != "the conversion `Cast[i64](x)`" {
			t.Fatalf("facts %s", dump(w, b.bodyDiags(t)))
		}
	})
}

func TestE0203_E0207_Members(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"unknown", pointSrc + "F(p: Point): int {\n\treturn p.z\n}\n", "E0203@7:11"},
		{"recase", pointSrc + "F(p: Point): int {\n\treturn p.X\n}\n", "E0207@7:11"},
		{"len", "F(xs: []int): int {\n\treturn xs.len()\n}\n", "E0207@2:12"},
		{"Len", "F(xs: &[]int, s: str, a: [3]u8): int {\n\treturn xs.Len() + s.Len() + a.Len()\n}\n", ""},
		{"Or on E!T", "error E\n\nG(): E!int {\n\treturn 1\n}\n\nF(): int {\n\treturn G().Or(0)\n}\n", "E0207@8:13"},
		{"optional", "F(x: ?int): int {\n\tif x.IsSome() {\n\t\treturn x.Must()\n\t}\n\treturn x else { 0 }\n}\n", ""},
	})
	t.Run("members fact", func(t *testing.T) {
		w := newWorld(t, true)
		b := checkBody(t, w, "E0203@11:11", pointSrc+"Point.Sum(&self): int {\n\treturn self.x\n}\n\nF(p: Point): int {\n\treturn p.z\n}\n")
		f, _ := b.bodyDiags(t)[0].FactValue("members")
		if len(f.List) != 3 || f.List[0].Str != "Sum" || f.List[1].Str != "x" || f.List[2].Str != "y" {
			t.Errorf("members %+v", f.List)
		}
	})
	// The list names what the calling module can use: another
	// module's private fields and Type.Name functions are left out, and an
	// invariant (an unnamed item of the struct) is no member in any module.
	t.Run("members fact visible from the caller", func(t *testing.T) {
		w := newWorld(t, true)
		w.add("geo", opt{}, "export struct P {\n\texport x: int\n\ty: int\n\tinvariant y >= 0\n}\n\nexport P.New(): P {\n\treturn P(x: 0, y: 0)\n}\n\nP.Fix(&self): int {\n\treturn self.y\n}\n")
		b := w.add("m", opt{}, "import geo\n\nF(p: geo.P): int {\n\treturn p.z\n}\n")
		ds := b.bodyDiags(t)
		if got := codes(w, ds); got != "E0203@4:11" {
			t.Fatalf("got %q\n%s", got, dump(w, ds))
		}
		f, _ := ds[0].FactValue("members")
		var got []string
		for _, x := range f.List {
			got = append(got, x.Str)
		}
		if strings.Join(got, ",") != "New,x" {
			t.Errorf("members from another module %q, want New,x", got)
		}
		own := checkBody(t, newWorld(t, true), "E0203@7:11", "struct P {\n\tx: int\n\tinvariant x >= 0\n}\n\nF(p: P): int {\n\treturn p.z\n}\n")
		f, _ = own.bodyDiags(t)[0].FactValue("members")
		if len(f.List) != 1 || f.List[0].Str != "x" {
			t.Errorf("members in the declaring module %+v, want [x]", f.List)
		}
	})
}

func TestE0204_PrivateField(t *testing.T) {
	w := newWorld(t, true)
	w.add("geo", opt{}, "export struct P {\n\texport x: int\n\ty: int\n}\n\nexport struct Q {\n\texport a: int\n}\n")
	b := w.add("m", opt{}, "import geo\n\nF(p: &geo.P): int {\n\treturn p.x + p.y\n}\n\nG(): geo.P {\n\treturn geo.P(x: 1, y: 2)\n}\n\nH(): geo.Q {\n\treturn geo.Q(a: 1)\n}\n")
	ds := b.bodyDiags(t)
	if got := codes(w, ds); got != "E0204@4:17 E0204@8:9" {
		t.Fatalf("got %q\n%s", got, dump(w, ds))
	}
	for _, d := range ds {
		if d.Phase != diag.PhaseTypes {
			t.Errorf("E0204 phase %v, want types (CHK-05)", d.Phase)
		}
	}
}

func TestReceiverAdjust(t *testing.T) {
	w := newWorld(t, true)
	b := checkBody(t, w, "", pointSrc+`Point.Get(&self): int {
	return self.x
}

Point.Set(mut self, v: int) {
	self.x = v
}

Point.Own(self): int {
	return self.y
}

F(var p: Point, r: &Point, q: mut Point): int {
	p.Set(1)
	q.Set(2)
	return p.Get() + r.Get() + r.Own() + r.x
}
`)
	body := w.c.Check(b.m, b.item("F"))
	fd := body.Node.(*ast.FuncDecl)
	recv := func(e ast.Expr) Adjust {
		fe := e.(*ast.CallExpr).Fun.(*ast.FieldExpr)
		return body.Adjust[body.slot(fe.X.ID())]
	}
	if a := recv(fd.Body.Stmts[0].(*ast.ExprStmt).X); a != (Adjust{Kind: AdjRefMut}) {
		t.Errorf("p.Set: %+v", a)
	}
	if a := recv(fd.Body.Stmts[1].(*ast.ExprStmt).X); a != (Adjust{Kind: AdjRefMut, Deref: 1}) {
		t.Errorf("q.Set: %+v", a)
	}
	sum := fd.Body.Stmts[2].(*ast.ReturnStmt).Value.(*ast.BinaryExpr)
	l1 := sum.X.(*ast.BinaryExpr)
	l2 := l1.X.(*ast.BinaryExpr)
	if a := recv(l2.X); a != (Adjust{Kind: AdjRefShared}) {
		t.Errorf("p.Get: %+v", a)
	}
	if a := recv(l2.Y); a != (Adjust{Kind: AdjRefShared, Deref: 1}) {
		t.Errorf("r.Get: %+v", a)
	}
	if a := recv(l1.Y); a != (Adjust{Kind: AdjCopyThrough, Deref: 1}) {
		t.Errorf("r.Own: %+v", a)
	}
	fx := sum.Y.(*ast.FieldExpr)
	if a := body.Adjust[body.slot(fx.X.ID())]; a != (Adjust{Kind: AdjDeref, Deref: 1}) {
		t.Errorf("r.x: %+v", a)
	}
	call := l2.X.(*ast.CallExpr)
	if u := body.Uses[body.slot(call.ID())]; u.Kind != UseMethod || u.Item != b.item("Point:Get") {
		t.Errorf("p.Get use %+v", u)
	}
	if u := body.Uses[body.slot(fx.ID())]; u.Kind != UseField || u.Index != 0 {
		t.Errorf("r.x use %+v", u)
	}
}

func TestConstructionAndLiterals(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"generic construction", "struct Stack2[T] {\n\titems: []T\n}\n\nF(xs: []u8): Stack2[u8] {\n\treturn Stack2(items: xs)\n}\n\nG(xs: []u8): Stack2[u8] {\n\treturn Stack2[u8](items: xs)\n}\n", ""},
		{"generic method", "struct Stack2[T] {\n\titems: []T\n}\n\nStack2[T].First(&self): &T {\n\treturn &self.items[0]\n}\n\nF(s: &Stack2[u8]): u8 {\n\treturn *s.First()\n}\n", ""},
		{"records", "F(): (a: int, b: bool) {\n\tb := true\n\treturn (b, a: 1)\n}\n", ""},
		{"record mismatch", "F(): (a: int, b: bool) {\n\treturn (a: 1, c: true)\n}\n", "E0301@2:9"},
		{"arrays", "F(): [3]u8 {\n\tx := [1, 2]\n\treturn [1, 2, 3]\n}\n\nG(): [4]int {\n\treturn [0; 4]\n}\n", ""},
		{"repeat of a non-plain value", "F(c: Cell[int]): [2]Cell[int] {\n\treturn [c; 2]\n}\n", "E0408@2:10"},
		{"fn literal", "F(): fn(int): int {\n\treturn fn(x: int): int {\n\t\treturn x + 1\n\t}\n}\n", ""},
		{"fn value", "Inc(x: int): int {\n\treturn x + 1\n}\n\nF(): int {\n\tf := Inc\n\treturn f(1)\n}\n", ""},
		{"namespace form", pointSrc + "Point.Sum(&self): int {\n\treturn self.x\n}\n\nPoint.Origin(): Point {\n\treturn Point(x: 0, y: 0)\n}\n\nF(p: Point): int {\n\treturn Point.Sum(&p) + Point.Origin().x\n}\n", ""},
		{"indexing", "F(xs: []int, a: &[4]u8, s: str): int {\n\tt := s[1..2]\n\tys := xs[0..1]\n\treturn xs[0] + int(a[1]) + ys[0]\n}\n", ""},
		{"index a str", "F(s: str): u8 {\n\treturn s[0]\n}\n", "E0308@2:9"},
		{"index by bool", "F(xs: []int): int {\n\treturn xs[true]\n}\n", "E0301@2:12"},
		{"call a non-function", "F(x: int): int {\n\treturn x(1)\n}\n", "E0301@2:9"},
		{"Slice implementer", "struct Buf {\n\tb: []u8\n}\n\nimpl Slice[u8] for Buf {\n}\n\nF(b: &Buf): u8 {\n\tvar s: u8 = 0\n\tfor x in b {\n\t\ts += x\n\t}\n\treturn s + b[0]\n}\n", ""},
		{"Assert and Panic", "F(x: int) {\n\tAssert(x > 0)\n\tPanic(\"no\")\n}\n", ""},
	})
}

func TestUseSiteAmbiguous(t *testing.T) {
	run := func(procs int, call bool) string {
		prev := runtime.GOMAXPROCS(procs)
		defer runtime.GOMAXPROCS(prev)
		w := newWorld(t, true)
		w.add("m1", opt{}, "export struct C {\n\texport n: int\n}\n")
		w.add("m2", opt{}, "import m1\nexport interface I {\n\tName(&self): str\n}\nimpl I for m1.C {\n\tName(&self): str {\n\t\treturn \"i\"\n\t}\n}\n")
		w.add("m3", opt{}, "import m1\nexport interface J {\n\tName(&self): str\n}\nimpl J for m1.C {\n\tName(&self): str {\n\t\treturn \"j\"\n\t}\n}\n")
		src := "import m1\nimport m2\nimport m3\nF(c: &m1.C): int {\n\treturn c.n\n}\n"
		if call {
			src = "import m1\nimport m2\nimport m3\nF(c: &m1.C): str {\n\treturn c.Name()\n}\n"
		}
		b := w.add("m4", opt{}, src)
		var ds []*diag.Diagnostic
		for _, d := range append(b.diags(), b.bodyDiags(t)...) {
			if d.Code != diag.E0208 { // m2 and m3 are imported for their impls only
				ds = append(ds, d)
			}
		}
		return codes(w, ds) + "\n" + dump(w, ds)
	}
	if got := run(4, false); got != "\n" {
		t.Errorf("without the call: %q", got)
	}
	one := run(1, true)
	if one[:len("E0206@5:11\n")] != "E0206@5:11\n" {
		t.Fatalf("got %q", one)
	}
	if eight := run(8, true); eight != one {
		t.Errorf("GOMAXPROCS 8 differs:\n%s\nvs\n%s", eight, one)
	}
}

func TestCallRecordsGeneric(t *testing.T) {
	w := newWorld(t, true)
	b := checkBody(t, w, "", "Id[T](x: T): T {\n\treturn x\n}\n\nF(): i32 {\n\tx: i32 = 3\n\treturn Id(x)\n}\n")
	body := w.c.Check(b.m, b.item("F"))
	call := body.Node.(*ast.FuncDecl).Body.Stmts[1].(*ast.ReturnStmt).Value.(*ast.CallExpr)
	u := body.Uses[body.slot(call.ID())]
	if u.Kind != UseItem || u.Inst == 0 || len(body.Generic[u.Inst]) != 1 || body.Generic[u.Inst][0].Type != types.I32 {
		t.Errorf("use %+v generic %v", u, body.Generic)
	}
	if body.Types[body.slot(call.ID())] != types.I32 {
		t.Errorf("call type %v", body.Types[body.slot(call.ID())])
	}
}

// TestLateLiteralArgs: an untyped literal for a parameter
// whose type is still open is checked after the call's expected type, so it
// adapts like a literal in any other expected position
// (`var Counter: Cell[i32] = Cell.New(0)`).
func TestLateLiteralArgs(t *testing.T) {
	const src = "Max[T](a: T, b: T): T {\n\treturn a\n}\n\nstruct Pair[T] {\n\ta: T\n\tb: T\n}\n\nWrap[T](x: ?T): ?T {\n\treturn x\n}\n\n"
	runBodyCases(t, []bodyCase{
		{"Max literals adapt", src + "F() {\n\tx: u8 = Max(1, 2)\n}\n", ""},
		{"a typed argument binds first", src + "F(y: i32) {\n\tx := Max(1, y)\n}\n", ""},
		{"construction", src + "F() {\n\tp: Pair[u8] = Pair(a: 1, b: 2)\n}\n", ""},
		{"Cell.New", "F() {\n\tc: Cell[i32] = Cell.New(0)\n}\n", ""},
		{"none with no context", src + "F() {\n\tx := Wrap(none)\n}\n", "E0711@15:7"},
		{"none from the expected type", src + "F() {\n\tx: ?int = Wrap(none)\n}\n", ""},
		{"no expected type", src + "F() {\n\tx := Max(1, 2)\n\ty: int = x\n}\n", ""},
	})
}

// TestE0204_PrivateFieldInPattern: a struct pattern in a match
// names a field another module does not export: E0204 (phase types) at the
// field's name, by value and by reference, as a destructure would be.
// Exported fields only are clean.
func TestE0204_PrivateFieldInPattern(t *testing.T) {
	const lib = "export struct P {\n\tsecret: int\n\texport Name: int\n}\n"
	cases := []struct{ name, src, want string }{
		{"by value", "F(p: lib.P): int {\n\tmatch p {\n\t\tlib.P(secret) {\n\t\t\treturn secret\n\t\t}\n\t}\n}\n", "E0204@5:9"},
		{"by reference", "F(p: lib.P): int {\n\tmatch &p {\n\t\tlib.P(secret: s) {\n\t\t\treturn *s\n\t\t}\n\t}\n}\n", "E0204@5:9"},
		{"exported only", "F(p: lib.P): int {\n\tmatch p {\n\t\tlib.P(Name) {\n\t\t\treturn Name\n\t\t}\n\t}\n}\n", ""},
		{"no fields", "F(p: lib.P): int {\n\tmatch p {\n\t\tlib.P() {\n\t\t\treturn 1\n\t\t}\n\t}\n}\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t, true)
			w.add("lib", opt{}, lib)
			b := w.add("m", opt{}, "import lib\n\n"+tc.src)
			ds := b.bodyDiags(t)
			if got := codes(w, ds); got != tc.want {
				t.Fatalf("got %q, want %q\n%s", got, tc.want, dump(w, ds))
			}
			for _, d := range ds {
				if d.Phase != diag.PhaseTypes {
					t.Errorf("E0204 phase %v, want types", d.Phase)
				}
			}
		})
	}
}
