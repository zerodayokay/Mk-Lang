// Why: name resolution inside signatures and bodies decides what every
// identifier refers to, and each scope rule needs a case.
// What: tests of signatures, readings, bodies, patterns, literals and the
// E0208 unused-name rule.
// How: parses small Mk modules with the helpers_test.go harness, resolves
// them and checks the readings and diagnostics.

package resolve

import (
	"testing"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/types"
)

func TestFlowNamesParameter(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	b := w.add("main", opt{}, `struct Buf {
    data: []u8
}
First(a: &Buf, b: &Buf): &u8 from a.data { return &a.data[0] }
Put(a: mut List[int], x: int into a) { }
Bad(a: &Buf): &u8 from c { return &a.data[0] }
Ends(a: &Buf, b: mut Buf) ends b { }
Store(a: mut Buf, b: &u8) *b into a { }
`)
	b.want(t, "E0201@6:24")
	if d := b.diags[0]; fact(d, "expected") != "parameter" {
		t.Errorf("%s", b.dump())
	}
	if r := b.ref("a", 1); r.Kind != RefParam {
		t.Errorf("from a: %+v", r)
	}
}

// TestReceiverFlowNamesParameter: a receiver's `into q` binds q as a
// parameter's does, so the method and the free function with the receiver
// as its first parameter name the same index.
func TestReceiverFlowNamesParameter(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	b := w.add("main", opt{}, `struct Names {
    all: List[str]
}
Names.Keys(&self into arena, arena: &Arena) { }
Keys(n: &Names into arena, arena: &Arena) { }
Names.Own(mut self into self) { }
Names.Bad(self into c) { }
`)
	b.want(t, "E0201@7:21")
	for k := 0; k < 2; k++ {
		if r := b.ref("arena", 2*k); r.Kind != RefParam || r.A != 2 {
			t.Errorf("into arena #%d: %+v", k, r)
		}
	}
	if r := b.ref("self", 0); r.Kind != RefParam || r.A != 1 {
		t.Errorf("self into self: %+v", r)
	}
}

func TestSelfWithoutReceiver(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "struct P {\n    x: int\n}\nP.Get(&self): &int from self { return &self.x }\nF(p: &P): &int from self { return &p.x }\n")
	b.want(t, "E0201@5:21")
}

func TestFnTypeFlowScope(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "F(g: fn(x: &i32): &i32 from x) { }\nG(g: fn(&i32): &i32 from y) { }\n")
	b.want(t, "E0201@2:26")
	if r := b.ref("x", 1); r.Kind != RefParam || r.A != 1 || r.B != 0 {
		t.Errorf("fn type flow: %+v", r)
	}
}

func TestEnsuresResult(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "Abs(x: int): int\n    requires x > -100\n    ensures result >= 0\n{\n    return x\n}\nG(): int { return result }\n")
	b.want(t, "E0201@7:19")
	r := b.ref("result", 0)
	if r.Kind != RefResult || types.ItemID(r.B) != w.item("main.Abs").ID {
		t.Errorf("result: %+v", r)
	}
	if r := b.ref("x", 1); r.Kind != RefParam || r.A != 1 || types.ItemID(r.B) != w.item("main.Abs").ID {
		t.Errorf("x in requires: %+v", r)
	}
}

func TestParamNamedResult(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "F(result: int): int\n    ensures result > 0\n{\n    return 1\n}\n")
	b.want(t, "E0206@1:3")
}

func TestInvariantSeesFields(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "struct Span {\n    lo: int\n    hi: int\n    invariant lo <= hi && self.lo >= 0\n}\n")
	b.want(t)
	if r := b.ref("lo", 1); r.Kind != RefField || r.A != 0 {
		t.Errorf("lo: %+v", r)
	}
	if r := b.ref("hi", 1); r.Kind != RefField || r.A != 1 {
		t.Errorf("hi: %+v", r)
	}
	if r := b.ref("self", 0); r.Kind != RefParam || types.ItemID(r.B) != w.item("main.Span#invariant0").ID {
		t.Errorf("self: %+v", r)
	}
}

func TestContractItems(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	w.add("main", opt{}, "F(x: int): int\n    requires x > 0\n    requires x < 9\n    ensures result > 0\n{\n    return x\n}\n").want(t)
	for _, k := range []string{"main.F#requires0", "main.F#requires1", "main.F#ensures0"} {
		if it := w.item(k); it.Owner != w.item("main.F").ID || it.Kind != types.KindFunc {
			t.Errorf("%s: %+v", k, it)
		}
	}
}

func TestTypeOrExprByParamKind(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "struct Buf[const N: int] {\n    x: [N]u8\n}\nstruct Stack2[T] {\n    x: T\n}\nF(n: int) {\n    _ = Cast[i32](n)\n    _ = Buf[16]()\n    _ = Stack2[int]()\n}\n")
	b.want(t)
	got := readings(b)
	want := map[string]Reading{"Cast[i32]": ReadGeneric, "Buf[16]": ReadGeneric, "Stack2[int]": ReadGeneric,
		"i32": ReadType, "16": ReadExpr, "int": ReadType}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %v, want %v (all %v)", k, got[k], v, got)
		}
	}
}

func TestImportedConstGeneric(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	w.add("m", opt{}, "export struct Buf[const N: int] {\n    x: [N]u8\n}\n")
	b := w.add("main", opt{}, "import m\n\nF(b: m.Buf[16], xs: List[int]) { }\n")
	b.want(t)
	got := readings(b)
	if got["16"] != ReadExpr || got["int"] != ReadType {
		t.Errorf("readings %v", got)
	}
}

func TestIndexIsIndex(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "F(xs: []int, i: int): int { return xs[i] }\n")
	b.want(t)
	if got := readings(b); got["xs[i]"] != ReadIndex || got["i"] != ReadExpr {
		t.Errorf("readings %v", got)
	}
}

func TestReadUndecidedOnMember(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "struct P {}\nF(p: P) { _ = p.M[int]() }\n")
	b.want(t)
	if got := readings(b); got["p.M[int]"] != ReadUndecided {
		t.Errorf("readings %v", got)
	}
}

func TestGenArgsReadings(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	w.add("std.derive", opt{std: true}, "export Eq(g: int)\n")
	b := w.add("main", opt{noSettle: true}, "import std.derive\n\nstruct User {}\ngen derive.Eq(User, 3, -1, &User)\n")
	got := readings(b)
	if got["User"] != ReadType || got["3"] != ReadExpr || got["-1"] != ReadExpr || got["&User"] != ReadType {
		t.Errorf("readings %v", got)
	}
	b.settle()
	b.want(t)
}

func TestBracketUnaryAsType(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	b := w.add("main", opt{}, "struct G[T] {\n    x: T\n}\nF() {\n    _ = G[&int]()\n    _ = G[mut int]()\n    _ = G[OutOfMemory|Canceled!int]()\n    _ = G[1 + 2]()\n}\n")
	b.want(t, "E0101@8:11")
	if b.diags[0].Phase != diag.PhaseNames {
		t.Error("E0101 not in names")
	}
}

func TestE0206_LocalShadowsLocal(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "F(x: int) {\n    y := x\n    if x > 0 {\n        y := 2\n        _ = y\n    }\n    _ = y\n    X := 1\n    _ = X\n}\n")
	b.want(t, "E0206@4:9", "E0206@8:5", "E0207@8:5")
}

func TestSiblingBlocksReuseName(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "F(c: bool) {\n    if c {\n        y := 1\n        _ = y\n    } else {\n        y := 2\n        _ = y\n    }\n}\n")
	b.want(t)
}

func TestBindReadsOuterFirst(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "F() {\n    x := x + 1\n}\n")
	b.want(t, "E0201@2:10", "E0208@2:5")
}

func TestLabels(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "F() {\n    outer: for {\n        inner: for {\n            break outer\n        }\n        Outer: for { continue inner }\n    }\n}\n")
	b.want(t, "E0201@6:31", "E0206@6:9", "E0207@6:9")
	if r := b.ref("outer", 1); r.Kind != RefLabel {
		t.Errorf("break outer: %+v", r)
	}
}

func TestUnknownLabel(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "F() {\n    for { break nowhere }\n}\n")
	b.want(t, "E0201@2:17")
	if fact(b.diags[0], "expected") != "label" {
		t.Errorf("%s", b.dump())
	}
}

func TestIsNeedsTypeParam(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "struct P {}\nF[T](x: T) {\n    if T is u8 { }\n    if P is u8 { }\n}\n")
	b.want(t, "E0201@4:8")
	if fact(b.diags[0], "expected") != "type parameter" {
		t.Errorf("%s", b.dump())
	}
}

func TestDestructureBindings(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "struct Point {\n    x: int\n    y: int\n}\nF(p: Point) {\n    Point(x, y) := p\n    var (a, b) := (a: 1, b: 2)\n    _ = x + y + a + b\n}\n")
	b.want(t)
	body := itemInfo(b, "main.F").Body
	if len(body.Locals) != 6 || body.Locals[2].Kind != LocalDestr || !body.Locals[4].Var {
		t.Errorf("locals %+v", body.Locals)
	}
}

func TestRecordLitBareFieldIsUse(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "F(x: int) {\n    r := (x)\n    _ = r\n    _ = (a: 1, A: 2)\n}\n")
	b.want(t, "E0206@4:16")
	if r := b.ref("x", 1); r.Kind != RefParam {
		t.Errorf("(x): %+v", r)
	}
}

func TestElseBinding(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	b := w.add("main", opt{}, "G(): OutOfMemory!int { return 1 }\nF(): int {\n    v := G() else e { return 0 }\n    return v\n}\n")
	b.want(t, "E0208@3:19")
	b2 := w.add("m2", opt{}, "G(): OutOfMemory!int { return 1 }\nF(): int {\n    v := G() else e { _ = e\n return 0 }\n    return v\n}\n")
	b2.want(t)
}

func TestScopeBinding(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	b := w.add("main", opt{}, "W() { }\nF(io: Io) {\n    scope s := io.Scope() {\n        s.go W()\n    }\n}\n")
	b.want(t)
}

func TestErrorTypeTest(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	b := w.add("main", opt{}, "error Full\nF(x: Full|OutOfMemory!int) {\n    match x {\n        err(Full) { }\n        err(OutOfMemory) { }\n        ok(v) { _ = v }\n    }\n}\n")
	b.want(t)
	if r := b.ref("Full", 2); r.Kind != RefItem {
		t.Errorf("err(Full): %+v", r)
	}
}

// TestConstIsAValueTest: a const in a pattern, bare, in an alternative or
// nested, names the const (a value test), never a binding.
func TestConstIsAValueTest(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "const Max = 3\nF(x: int, y: ?int) {\n    match x {\n        Max { }\n        1 | Max { }\n        _ { }\n    }\n    match y {\n        some(Max) { }\n        _ { }\n    }\n}\n")
	b.want(t)
	for n := 1; n <= 3; n++ {
		if r := b.ref("Max", n); r.Kind != RefItem {
			t.Errorf("Max #%d: %+v", n, r)
		}
	}
}

func TestBareOkIsE0101(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	b := w.add("main", opt{}, "F(x: OutOfMemory!int) {\n    match x {\n        ok { }\n        _ { }\n    }\n}\n")
	b.want(t, "E0101@3:9")
}

func TestPascalBindingDeferred(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "enum Color { Red\n Green }\nF(c: Color) {\n    match c {\n        Red { }\n        other { _ = other }\n    }\n}\n")
	b.want(t, "E0208@5:9")
	body := itemInfo(b, "main.F").Body
	if !body.Locals[2].Pascal || body.Locals[3].Pascal {
		t.Errorf("locals %+v", body.Locals)
	}
}

func TestBindingShadowsLocal(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "F(x: ?int) {\n    v := 1\n    match x {\n        some(v) { _ = v }\n        none { }\n    }\n    _ = v\n}\n")
	b.want(t, "E0206@4:14")
}

func TestAltNameMustResolve(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	b := w.add("main", opt{}, "error Full\nF(x: Full|OutOfMemory!int) {\n    match x {\n        err(Full | Nope) { }\n        _ { }\n    }\n}\n")
	b.want(t, "E0201@4:20")
}

func TestTrueFalsePatterns(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "F(c: bool) {\n    match c {\n        true { }\n        false { }\n    }\n}\n")
	b.want(t)
	if r := b.ref("true", 0); r.Kind != RefUniverse {
		t.Errorf("true: %+v", r)
	}
}

func TestLiteralParamShadows(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "F(x: int) {\n    g := fn(x: int): int { return x }\n    _ = g\n}\n")
	b.want(t, "E0206@2:13")
}

func TestLiteralKeys(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	w.add("main", opt{}, "F() {\n    a := fn() { b := fn() { }\n b() }\n    c := fn() { }\n    a()\n    c()\n}\n").want(t)
	for _, k := range []string{"main.F#fn0", "main.F#fn0#fn0", "main.F#fn1"} {
		w.item(k)
	}
}

func TestLabelsDoNotCrossLiteral(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{parseErrors: true}, "F() {\n    outer: for {\n        g := fn() { for { break outer } }\n        g()\n    }\n}\n")
	if len(b.only(diag.E0201)) != 1 {
		t.Errorf("%s", b.dump())
	}
}

func TestE0208_UnusedLocal(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "F(p: int) {\n    x := 1\n    y := p\n    z := G()\n}\nG(): int { return 1 }\n")
	b.want(t, "E0208@2:5", "E0208@3:5", "E0208@4:5")
	if len(b.diags[0].Fixes) != 1 || len(b.diags[2].Fixes) != 0 {
		t.Errorf("%s", b.dump())
	}
}

func TestE0208_UnusedLoopBinding(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "F(xs: []int) {\n    for i, x in xs { _ = x }\n}\n")
	b.want(t, "E0208@2:9")
	if len(b.diags[0].Alternatives) == 0 {
		t.Errorf("%s", b.dump())
	}
}

func TestE0208_ParametersExempt(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	w.add("main", opt{}, "Use[T](x: &T) { }\nstruct P {}\nP.Override(&self, n: int) { }\n").want(t)
}

func TestE0208_TestPreludeExempt(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{testPrelude: "Helper() {\n    unused := 1\n}\n"}, "F() { Helper() }\n")
	b.want(t)
}

func TestE0208_AssignmentCounts(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	w.add("main", opt{}, "F() {\n    var x := 1\n    x = 2\n}\n").want(t)
}

func TestE0208_PhaseIsTypes(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "F() {\n    x := 1\n}\n")
	if b.diags[0].Phase != diag.PhaseTypes {
		t.Errorf("phase %v", b.diags[0].Phase)
	}
}

// itemInfo finds an ItemInfo by key.
func itemInfo(b *built, key string) *ItemInfo {
	for _, it := range b.info.Items {
		if b.w.items.Get(it.Item).Key.String() == key {
			return it
		}
	}
	b.w.t.Fatalf("no item info %s", key)
	return nil
}

// readings maps each node with a reading to it, by its source text.
func readings(b *built) map[string]Reading {
	out := map[string]Reading{}
	for _, fi := range b.info.Files {
		text := b.w.sm.File(fi.FileID).Text
		ast.Inspect(fi.File, func(n ast.Node) bool {
			if n == nil {
				return false
			}
			if r := fi.Readings[n.ID()]; r != ReadingNone {
				s := n.Span()
				out[text[s.Start:s.End]] = r
			}
			return true
		})
	}
	return out
}

// TestPerFieldClauseNames: a per-field clause's sources name
// parameters (E0201 otherwise), on a signature and on a fn type; the field
// names are check's (E0203), so resolve binds none of them.
func TestPerFieldClauseNames(t *testing.T) {
	t.Parallel()
	w := newWorld(t, false)
	b := w.add("main", opt{}, "struct P {\n    a: &i32\n    b: &i32\n}\nF(a: &i32, x: &i32): P from (a: x, b: zz) { return P(a: x, b: x) }\nG(g: fn(x: &i32, y: &i32): P from (a: x, b: q)) { }\n")
	b.want(t, "E0201@5:39 E0201@6:45")
	if r := b.ref("x", 1); r.Kind != RefParam || r.A != 2 {
		t.Errorf("a source of the clause: %+v", r)
	}
	if r := b.ref("a", 2); r.Kind == RefParam {
		t.Errorf("the clause's field name bound to a parameter: %+v", r)
	}
}
