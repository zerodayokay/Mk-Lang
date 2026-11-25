// Why: match and if arms may yield values, and else may give a value, with
// rules on arm shapes, types and the statement form.
// What: TestArmShapes to TestStatementFormUnchanged, TestNarrowedArm and TestOrRemoved.
// How: checks value constructs and compares codes and the construct's type.

package check

import (
	"testing"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

// unitBodies checks every unit and returns their bodies by item name suffix.
func (b *built) unitBodies(t *testing.T) map[string]*Body {
	t.Helper()
	out := map[string]*Body{}
	for _, id := range b.m.Units() {
		it := b.w.items.Get(id)
		var body *Body
		if ie := source.Guard(it.Key.String(), it.Span, func() { body = b.w.c.Check(b.m, id) }); ie != nil {
			t.Fatalf("ICE in %s: %v", it.Key, ie)
		}
		out[b.w.c.cfg.Interner.Str(it.Name)] = body
	}
	return out
}

func TestArmShapes(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"bind", "F(c: bool): int {\n\tx := if c { 1 } else { 2 }\n\treturn x\n}\n", ""},
		{"statements before the value", "F(c: bool): int {\n\tx := if c {\n\t\ty := 1\n\t\ty + 1\n\t} else {\n\t\t2\n\t}\n\treturn x\n}\n", ""},
		{"return operand", "F(n: i32): i32 {\n\treturn if n < 0 { -1 } else if n == 0 { 0 } else { 1 }\n}\n", ""},
		{"arm returns", "F(c: bool): int {\n\tx := if c { 1 } else { return 0 }\n\treturn x\n}\n", ""},
		{"nested last line", "F(a: bool, b: bool): int {\n\tx := if a {\n\t\tif b { 1 } else { 2 }\n\t} else {\n\t\t3\n\t}\n\treturn x\n}\n", ""},
		{"argument", "G(x: i32) { }\nF(c: bool) {\n\tG(if c { 1 } else { 2 })\n}\n", ""},
		{"all arms return", "F(c: bool): int {\n\tx := if c { return 1 } else { return 2 }\n}\n", ""},
		{"break in a loop", "F(c: bool): int {\n\tvar n := 0\n\tfor {\n\t\tn = if c { break } else { n + 1 }\n\t}\n\treturn n\n}\n", ""},
		{"if bind", "F(o: ?int): int {\n\treturn if y := o { y } else { 0 }\n}\n", ""},
		{"binary operand adapts to the other side", "F(c: bool, n: i32): i32 {\n\treturn n + if c { 1 } else { 2 }\n}\n", ""},
		{"operand with no expected type", "F(c: bool): int {\n\tx := (if c { 1 } else { 2 }) * 3\n\treturn x\n}\n", ""},
		{"match operand", "F(n: int): int {\n\treturn 1 + match n {\n\t\t0 { 10 }\n\t\t_ { 20 }\n\t}\n}\n", ""},
		{"index", "F(c: bool, xs: []int): int {\n\treturn xs[if c { 0 } else { 1 }]\n}\n", ""},
		{"condition", "F(c: bool, n: int): int {\n\tif n == if c { 1 } else { 2 } {\n\t\treturn 1\n\t}\n\treturn 0\n}\n", ""},
		{"operand without else", "F(c: bool): int {\n\treturn 1 + if c { 1 }\n}\n", "E0311@2:13"},
		{"operand arms disagree", "F(c: bool, n: i32, m: i64): i32 {\n\treturn n + if c { n } else { m }\n}\n", "E0301@2:31"},
	})
}

func TestE0310(t *testing.T) {
	cases := []struct{ name, arm, last string }{
		{"binding", "z := 2", "binding"},
		{"assignment", "v = 2", "assignment"},
		{"discard", "_ = 2", "discard"},
		{"defer", "defer G()", "defer"},
		{"loop", "for { break }", "loop"},
		{"unsafe", "unsafe \"r\" { }", "unsafe"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t, true)
			b := checkBody(t, w, "E0310@5:3", "G() { }\nF(c: bool): int {\n\tvar v := 0\n\tx := if c { 1 } else {\n\t\t"+c.arm+"\n\t}\n\treturn x + v\n}\n")
			for _, d := range b.bodyDiags(t) {
				if d.Code == diag.E0310 && (fact(d, "last") != c.last || fact(d, "construct") != "if") {
					t.Errorf("facts: %s", dump(w, []*diag.Diagnostic{d}))
				}
			}
		})
	}
	runBodyCases(t, []bodyCase{
		{"empty arm", "F(c: bool): int {\n\tx := if c { 1 } else { }\n\treturn x\n}\n", "E0310@2:25"},
		{"match arm", "enum E {\n\tA\n\tB\n}\nF(e: E): int {\n\tx := match e {\n\t\t.A { 1 }\n\t\t.B { y := 2 }\n\t}\n\treturn x\n}\n", "E0310@8:8"},
	})
}

func TestE0311(t *testing.T) {
	// The shape of doc/reference.md's E0311 example (Pick): exactly E0311 and
	// E0310, nothing on the return.
	runBodyCases(t, []bodyCase{
		{"pick", "Pick(c: bool): i32 {\n\tx := if c { 1 }\n\ty := if c {\n\t\t1\n\t} else {\n\t\tz := 2\n\t}\n\treturn x + y\n}\n", "E0310@6:3 E0311@2:7"},
	})
}

func TestTypeTestValue(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"is", "Size[T](x: T): int {\n\treturn if T is i32 { 4 } else { 8 }\n}\n", ""},
	})
}

// TestNarrowedArm: the arm under `if T is X` is checked against the
// construct's type with T read as X, as the statement form's `return` and
// assignments are, and its narrowed type does not leak out.
func TestNarrowedArm(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"return", "Pick[T: Plain](v: T, w: T): T {\n\treturn if T is int { w + 1 } else { v }\n}\n", ""},
		{"statement form", "Pick[T: Plain](v: T, w: T): T {\n\tif T is int {\n\t\treturn w + 1\n\t} else {\n\t\treturn v\n\t}\n}\n", ""},
		{"typed binding", "Pick[T: Plain](v: T, w: T): T {\n\tx: T = if T is int { w } else { v }\n\treturn x\n}\n", ""},
		{"no expected type", "Pick[T: Plain](v: T, w: T): T {\n\tx := if T is int { w } else { v }\n\treturn x\n}\n", ""},
		{"optional", "Opt[T: Plain](v: T): ?T {\n\tx: ?T = if T is int { none } else { some(v) }\n\treturn x\n}\n", ""},
		{"literal arm", "Size[T: Plain](v: T): T {\n\tx := if T is i32 { 4 } else { v }\n\treturn x\n}\n", ""},
		{"literal arm last", "Size[T: Plain](v: T): T {\n\tx := if T is i32 { v } else if T is i64 { 8 } else { v }\n\treturn x\n}\n", ""},
		{"narrowed arm only", "Only[T: Plain](w: T): int {\n\tx := if T is int { w } else { return 0 }\n\treturn x\n}\n", ""},
		{"wrong under the narrowing", "Pick[T: Plain](v: T, s: str): T {\n\treturn if T is int { s } else { v }\n}\n", "E0301@2:23"},
	})
	w := newWorld(t, true)
	b := w.add("m", opt{}, "Pick[T: Plain](v: T, w: T): T {\n\tx := if T is int { w } else { v }\n\treturn x\n}\n")
	body := b.unitBodies(t)["Pick"]
	ast.Inspect(body.Node, func(nd ast.Node) bool {
		if s, ok := nd.(*ast.IfStmt); ok {
			if k := w.tab.Kind(body.TypeOf(s.ID())); k != types.KParam {
				t.Errorf("construct typed %s, want T", w.tab.TypeString(body.TypeOf(s.ID()), nil))
			}
		}
		return true
	})
}

func TestArmTypes(t *testing.T) {
	runBodyCases(t, []bodyCase{
		// The first arm that is not an untyped literal decides.
		{"literal first", "F(c: bool, n: i32): i32 {\n\tx := if c { 0 } else { n }\n\treturn x\n}\n", ""},
		{"literal last", "F(c: bool, n: i32): i32 {\n\tx := if c { n } else { 0 }\n\treturn x\n}\n", ""},
		{"all literals default", "F(c: bool): int {\n\tx := if c { 0 } else { 1 }\n\treturn x\n}\n", ""},
		{"want", "F(c: bool): i8 {\n\tx: i8 = if c { 0 } else { 1 }\n\treturn x\n}\n", ""},
		{"mismatch", "F(c: bool, n: i32, s: str): i32 {\n\tx := if c { n } else { s }\n\treturn x\n}\n", "E0301@2:25"},
		{"mismatch against want", "F(c: bool, s: str): i32 {\n\treturn if c { 1 } else { s }\n}\n", "E0301@2:27"},
		// L7R-7: `none` is a literal arm too; it waits for the other arm.
		{"none first", "F(c: bool): ?int {\n\tx := if c { none } else { some(1) }\n\treturn x\n}\n", ""},
		{"none last", "F(c: bool): ?int {\n\tx := if c { some(1) } else { none }\n\treturn x\n}\n", ""},
		{"empty array first", "F(c: bool, ys: [0]i8): [0]i8 {\n\tx := if c { [] } else { ys }\n\treturn x\n}\n", ""},
		{"all none", "F(c: bool) {\n\tx := if c { none } else { none }\n}\n", "E0711@2:14 E0711@2:28"},
	})
	for _, src := range []string{
		"F(c: bool): ?int {\n\tx := if c { none } else { some(1) }\n\treturn x\n}\n",
		"F(c: bool): ?int {\n\tx := if c { some(1) } else { none }\n\treturn x\n}\n",
	} {
		w := newWorld(t, true)
		b := w.add("m", opt{}, src)
		body := b.unitBodies(t)["F"]
		ast.Inspect(body.Node, func(nd ast.Node) bool {
			if s, ok := nd.(*ast.IfStmt); ok {
				if got := body.TypeOf(s.ID()); got != w.tab.Optional(types.Int) {
					t.Errorf("%q: construct typed %s, want ?int", src, w.tab.TypeString(got, nil))
				}
			}
			return true
		})
	}
	w := newWorld(t, true)
	b := w.add("m", opt{}, "F(c: bool, n: i32, s: str): i32 {\n\tx := if c { n } else { s }\n\treturn x\n}\n")
	for _, d := range b.bodyDiags(t) {
		if d.Code == diag.E0301 && fact(d, "arm") == "" {
			t.Errorf("no arm fact: %s", dump(w, []*diag.Diagnostic{d}))
		}
	}
	// The literal arm checked after the deciding arm has the decided type.
	b = w.add("n", opt{}, "F(c: bool, n: i32): i32 {\n\tx := if c { 0 } else { n }\n\treturn x\n}\n")
	body := b.unitBodies(t)["F"]
	found := false
	ast.Inspect(body.Node, func(nd ast.Node) bool {
		if lit, ok := nd.(*ast.BasicLit); ok && lit.Raw == "0" {
			found = true
			if got := body.TypeOf(lit.ID()); got != types.I32 {
				t.Errorf("literal arm typed %v, want i32", got)
			}
		}
		if s, ok := nd.(*ast.IfStmt); ok {
			if !body.Values.Has(s.ID(), body.Base) || body.TypeOf(s.ID()) != types.I32 {
				t.Errorf("if not a value of i32")
			}
		}
		return true
	})
	if !found {
		t.Fatal("no literal")
	}
}

func TestElseValue(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"value", "F(o: ?i64): i64 {\n\tn := o else { 8080 }\n\treturn n\n}\n", ""},
		{"bound error", "error Bad\nG(): Bad!i64 {\n\treturn 1\n}\nF(): i64 {\n\tn := G() else e {\n\t\t_ = e\n\t\t8080\n\t}\n\treturn n\n}\n", ""},
		{"falls through", "G() { }\nF(o: ?i64): i64 {\n\tn := o else { G() }\n\treturn n\n}\n", "E0803@3:9"},
		{"empty", "F(o: ?i64): i64 {\n\tn := o else { }\n\treturn n\n}\n", "E0803@2:9"},
		{"binding last", "F(o: ?i64): i64 {\n\tn := o else {\n\t\tz := 1\n\t}\n\treturn n\n}\n", "E0803@2:9"},
		{"diverges", "F(o: ?i64): i64 {\n\tn := o else { return 0 }\n\treturn n\n}\n", ""},
		{"nested if", "F(o: ?i64, c: bool): i64 {\n\tn := o else {\n\t\tif c { 1 } else { 2 }\n\t}\n\treturn n\n}\n", ""},
		{"nested match", "F(o: ?i64, c: bool): i64 {\n\tn := o else {\n\t\tmatch c {\n\t\t\ttrue { 1 }\n\t\t\tfalse { 2 }\n\t\t}\n\t}\n\treturn n\n}\n", ""},
		{"dropped", "error Bad\nG(): Bad!void {\n\treturn\n}\nF() {\n\tG() else { }\n}\n", ""},
	})
}

func TestStatementFormUnchanged(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"E0209 in a statement if", "F(c: bool) {\n\tif c { 1 } else { 2 }\n}\n", "E0209@2:20 E0209@2:9"},
		{"E0805 with the return fix", "F(c: bool): int {\n\tif c { 1 } else { 2 }\n}\n", "E0209@2:20 E0209@2:9 E0805@3:1"},
	})
	w := newWorld(t, true)
	b := w.add("m", opt{}, "F(c: bool): int {\n\tif c { 1 } else { 2 }\n}\n")
	for _, d := range b.bodyDiags(t) {
		if d.Code == diag.E0805 {
			if len(d.Fixes) != 1 || d.Fixes[0].Class != diag.Compiles || d.Fixes[0].Edits[0].Text != "return " {
				t.Errorf("E0805 fix: %+v", d.Fixes)
			}
		}
	}
}

// TestOrRemoved: `Or` on `?T` is E0207, since `Or` on an optional was
// removed; internal/driver TestOrIsE0207 applies its fix.
func TestOrRemoved(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"or", "F(o: ?int): int {\n\tx := o.Or(3)\n\treturn x\n}\n", "E0207@2:9"},
	})
}
