// Why: pattern typing and exhaustiveness decide whether a match is
// accepted, and the search must stay bounded.
// What: TestE0306_* (non-exhaustive, past the cap, wildcards, or-pattern
// expansion), TestBindingModes, TestE0408_PatternLeftovers, TestPatternTyping,
// TestConstPatterns.
// How: checks matches and compares codes and missing-case witnesses.

package check

import (
	"fmt"
	"strings"
	"testing"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/types"
)

const shapeEnum = "enum Shape {\n\tCircle(r: f64)\n\tRect(w: f64, h: f64)\n\tEmpty\n}\n\n"

func TestE0306_NonExhaustive(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"Area", shapeEnum + "Area(s: Shape): f64 {\n\tmatch s {\n\t\t.Circle(r) {\n\t\t\treturn 3.14 * r * r\n\t\t}\n\t\t.Rect(w, h) {\n\t\t\treturn w * h\n\t\t}\n\t\t.Empty {\n\t\t\treturn 0.0\n\t\t}\n\t}\n}\n", ""},
		{"missing variant", shapeEnum + "Area(s: Shape): f64 {\n\tmatch s {\n\t\t.Circle(r) {\n\t\t\treturn r\n\t\t}\n\t\t.Empty {\n\t\t\treturn 0.0\n\t\t}\n\t}\n\treturn 1.0\n}\n", "E0306@8:2"},
		{"optional without none", "F(x: ?int): int {\n\tmatch x {\n\t\tsome(v) {\n\t\t\treturn v\n\t\t}\n\t}\n\treturn 0\n}\n", "E0306@2:2"},
		{"error union missing a member", "error A\nerror B\n\nG(): A | B!int {\n\treturn 1\n}\n\nF(): int {\n\tmatch G() {\n\t\tok(v) {\n\t\t\treturn v\n\t\t}\n\t\terr(A) {\n\t\t\treturn 0\n\t\t}\n\t}\n\treturn 1\n}\n", "E0306@9:2"},
		{"int without _", "F(x: int): int {\n\tmatch x {\n\t\t0 {\n\t\t\treturn 1\n\t\t}\n\t\t-1 {\n\t\t\treturn 2\n\t\t}\n\t}\n\treturn 0\n}\n", "E0306@2:2"},
		{"int with _", "F(x: int): int {\n\tmatch x {\n\t\t0 {\n\t\t\treturn 1\n\t\t}\n\t\t_ {\n\t\t\treturn 2\n\t\t}\n\t}\n}\n", ""},
		{"bool", "F(b: bool): int {\n\tmatch b {\n\t\ttrue {\n\t\t\treturn 1\n\t\t}\n\t\tfalse {\n\t\t\treturn 0\n\t\t}\n\t}\n}\n", ""},
		{"alternatives", shapeEnum + "F(s: Shape): int {\n\tmatch s {\n\t\t.Circle | .Rect {\n\t\t\treturn 1\n\t\t}\n\t\t.Empty {\n\t\t\treturn 0\n\t\t}\n\t}\n}\n", ""},
		{"nested", "F(x: ??bool): int {\n\tmatch x {\n\t\tsome(some(true)) {\n\t\t\treturn 1\n\t\t}\n\t\tsome(none) | none {\n\t\t\treturn 0\n\t\t}\n\t}\n\treturn 2\n}\n", "E0306@2:2"},
	})
	t.Run("witnesses", func(t *testing.T) {
		w := newWorld(t, true)
		b := checkBody(t, w, "E0306@2:2", "F(x: ??bool): int {\n\tmatch x {\n\t\tsome(some(true)) {\n\t\t\treturn 1\n\t\t}\n\t\tnone {\n\t\t\treturn 0\n\t\t}\n\t}\n\treturn 2\n}\n")
		f, _ := b.bodyDiags(t)[0].FactValue("missing")
		var got []string
		for _, x := range f.List {
			got = append(got, x.Str)
		}
		if strings.Join(got, " ") != "some(some(false)) some(none)" {
			t.Errorf("missing %v", got)
		}
	})
}

// grid writes three (or more) 16-variant enums, a struct of them, and a
// match with one arm per combination for which keep returns true.
func grid(fields int, keep func(ix []int) bool, extra string) string {
	var sb strings.Builder
	for f := 0; f < fields; f++ {
		fmt.Fprintf(&sb, "enum E%d {\n", f)
		for v := 0; v < 16; v++ {
			fmt.Fprintf(&sb, "\tV%d\n", v)
		}
		sb.WriteString("}\n\n")
	}
	sb.WriteString("struct S {\n")
	for f := 0; f < fields; f++ {
		fmt.Fprintf(&sb, "\tf%d: E%d\n", f, f)
	}
	sb.WriteString("}\n\nF(s: S): int {\n\tmatch s {\n")
	ix := make([]int, fields)
	var rec func(k int)
	rec = func(k int) {
		if k == fields {
			if keep(ix) {
				var parts []string
				for f, v := range ix {
					parts = append(parts, fmt.Sprintf("f%d: .V%d", f, v))
				}
				fmt.Fprintf(&sb, "\t\tS(%s) {\n\t\t\treturn 1\n\t\t}\n", strings.Join(parts, ", "))
			}
			return
		}
		for v := 0; v < 16; v++ {
			ix[k] = v
			rec(k + 1)
		}
	}
	rec(0)
	sb.WriteString(extra)
	sb.WriteString("\t}\n\treturn 0\n}\n")
	return sb.String()
}

func TestE0306_PastCap(t *testing.T) {
	last := func(ix []int) bool { return !(ix[0] == 15 && ix[1] == 15 && ix[2] == 15) }
	w := newWorld(t, true)
	b := w.add("m", opt{}, grid(3, last, ""))
	ds := b.bodyDiags(t)
	if len(ds) != 1 || ds[0].Code != diag.E0306 {
		t.Fatalf("got %s", dump(w, ds))
	}
	if f, _ := ds[0].FactValue("missing"); len(f.List) != 1 || f.List[0].Str != "S(f0: .V15, f1: .V15, f2: .V15)" {
		t.Errorf("missing %+v", f.List)
	}
	all := func([]int) bool { return true }
	w2 := newWorld(t, true)
	if ds := w2.add("m", opt{}, grid(3, all, "")).bodyDiags(t); len(ds) != 0 {
		t.Errorf("complete grid: %s", dump(w2, ds))
	}
	// Every column has every constructor at every depth, so the search
	// branches 16 ways five times and passes the cap with no witness.
	var arms strings.Builder
	for f := 0; f < 5; f++ {
		for v := 0; v < 16; v++ {
			fmt.Fprintf(&arms, "\t\tS(f%d: .V%d) {\n\t\t\treturn 1\n\t\t}\n", f, v)
		}
	}
	none := func([]int) bool { return false }
	src := grid(5, none, arms.String())
	var first string
	for i := 0; i < 2; i++ {
		w3 := newWorld(t, true)
		ds := w3.add("m", opt{}, src).bodyDiags(t)
		if len(ds) != 1 || ds[0].Code != diag.E0306 || fact(ds[0], "reason") != "too-complex" {
			t.Fatalf("too complex: %s", dump(w3, ds))
		}
		if i == 0 {
			first = dump(w3, ds)
		} else if dump(w3, ds) != first {
			t.Errorf("not the same on every run")
		}
	}
}

func TestE0306_WildcardShortCircuit(t *testing.T) {
	var arms strings.Builder
	for f := 0; f < 5; f++ {
		for v := 0; v < 16; v++ {
			fmt.Fprintf(&arms, "\t\tS(f%d: .V%d) {\n\t\t\treturn 1\n\t\t}\n", f, v)
		}
	}
	arms.WriteString("\t\t_ {\n\t\t\treturn 2\n\t\t}\n")
	w := newWorld(t, true)
	if ds := w.add("m", opt{}, grid(5, func([]int) bool { return false }, arms.String())).bodyDiags(t); len(ds) != 0 {
		t.Errorf("got %s", dump(w, ds))
	}
}

func TestBindingModes(t *testing.T) {
	w := newWorld(t, true)
	b := checkBody(t, w, "", shapeEnum+"F(s: Shape, p: &Shape) {\n\tmatch s {\n\t\t.Circle(r) {\n\t\t}\n\t\t_ {\n\t\t}\n\t}\n\tmatch &s {\n\t\t.Circle(r) {\n\t\t}\n\t\t_ {\n\t\t}\n\t}\n\tmatch p {\n\t\t.Rect(w, h: hh) {\n\t\t}\n\t\t_ {\n\t\t}\n\t}\n}\n")
	body := w.c.Check(b.m, b.item("F"))
	want := map[string]types.TypeID{}
	for _, l := range body.Locals[1:] {
		if n := w.in.Str(l.Name); n == "r" || n == "w" || n == "hh" {
			if _, dup := want[n]; dup {
				n += "2"
			}
			want[n] = l.Type
		}
	}
	ref := w.tab.Ref(types.F64)
	if want["r"] != types.F64 || want["r2"] != ref || want["w"] != ref || want["hh"] != ref {
		t.Errorf("binding types r %v r2 %v w %v hh %v", b.ts(want["r"], nil), b.ts(want["r2"], nil), b.ts(want["w"], nil), b.ts(want["hh"], nil))
	}
	fd := body.Node.(*ast.FuncDecl)
	arm := fd.Body.Stmts[0].(*ast.MatchStmt).Arms[0]
	if u := body.Uses[body.slot(arm.Pat.ID())]; u.Kind != UseVariant || u.Index != 0 {
		t.Errorf("variant use %+v", u)
	}
}

func TestE0408_PatternLeftovers(t *testing.T) {
	const holder = "enum H {\n\tFull(c: Cell[int], n: int)\n\tEmpty\n}\n\n"
	runBodyCases(t, []bodyCase{
		{"left out", holder + "F(h: H) {\n\tmatch h {\n\t\t.Full(n) {\n\t\t}\n\t\t.Empty {\n\t\t}\n\t}\n}\n", "E0408@8:3"},
		{"wildcard", holder + "F(h: H) {\n\tmatch h {\n\t\t.Full(c: _, n) {\n\t\t}\n\t\t.Empty {\n\t\t}\n\t}\n}\n", "E0408@8:12"},
		{"by reference", holder + "F(h: H) {\n\tmatch &h {\n\t\t.Full(n) {\n\t\t}\n\t\t.Empty {\n\t\t}\n\t}\n}\n", ""},
		{"bound", holder + "F(h: H) {\n\tmatch h {\n\t\t.Full(c, n) {\n\t\t}\n\t\t.Empty {\n\t\t}\n\t}\n}\n", ""},
	})
}

func TestPatternTyping(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"unknown variant", shapeEnum + "F(s: Shape) {\n\tmatch s {\n\t\t.Square {\n\t\t}\n\t\t_ {\n\t\t}\n\t}\n}\n", "E0203@9:4"},
		{"recased variant", shapeEnum + "F(s: Shape) {\n\tmatch s {\n\t\t.empty {\n\t\t}\n\t\t_ {\n\t\t}\n\t}\n}\n", "E0207@9:4"},
		{"unknown field", shapeEnum + "F(s: Shape) {\n\tmatch s {\n\t\t.Circle(q) {\n\t\t}\n\t\t_ {\n\t\t}\n\t}\n}\n", "E0309@9:11"},
		{"float pattern", "F(x: f64) {\n\tmatch x {\n\t\t1.5 {\n\t\t}\n\t\t_ {\n\t\t}\n\t}\n}\n", "E0308@3:3"},
		{"literal range", "F(x: u8) {\n\tmatch x {\n\t\t300 {\n\t\t}\n\t\t_ {\n\t\t}\n\t}\n}\n", "E0303@3:3"},
		{"str literal", "F(s: str): int {\n\tmatch s {\n\t\t\"a\" {\n\t\t\treturn 1\n\t\t}\n\t\t_ {\n\t\t\treturn 0\n\t\t}\n\t}\n}\n", ""},
		{"error not in set", "error A\nerror B\nerror C\n\nG(): A | B!int {\n\treturn 1\n}\n\nF() {\n\tmatch G() {\n\t\tok(v) {\n\t\t}\n\t\terr(C) {\n\t\t}\n\t\t_ {\n\t\t}\n\t}\n}\n", "E0301@13:7"},
		{"some on a plain value", "F(x: int) {\n\tmatch x {\n\t\tsome(v) {\n\t\t}\n\t\t_ {\n\t\t}\n\t}\n}\n", "E0301@3:3"},
		{"qualified variant", shapeEnum + "F(s: Shape): f64 {\n\tmatch s {\n\t\tShape.Circle(r) {\n\t\t\treturn r\n\t\t}\n\t\t_ {\n\t\t\treturn 0.0\n\t\t}\n\t}\n}\n", ""},
		{"error payload", "error NotFound(code: int)\n\nG(): NotFound!int {\n\treturn 1\n}\n\nF(): int {\n\tmatch G() {\n\t\tok(v) {\n\t\t\treturn v\n\t\t}\n\t\terr(NotFound(code)) {\n\t\t\treturn code\n\t\t}\n\t}\n}\n", ""},
	})
}

// boolMatch is a struct of n bool fields matched by one arm whose field k is
// pats(k), followed by more arms.
func boolMatch(n int, pats func(k int) string, more string) string {
	var sb strings.Builder
	sb.WriteString("struct S {\n")
	for f := 0; f < n; f++ {
		fmt.Fprintf(&sb, "\tf%d: bool\n", f)
	}
	sb.WriteString("}\n\nF(s: S): int {\n\tmatch s {\n\t\tS(")
	for f := 0; f < n; f++ {
		if f > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "f%d: %s", f, pats(f))
	}
	sb.WriteString(") {\n\t\t\treturn 1\n\t\t}\n" + more + "\t}\n\treturn 0\n}\n")
	return sb.String()
}

// TestE0306_AltExpansionCap: or-patterns inside a
// constructor are expanded lazily at the head of a row, never multiplied out
// and cut: an arm of 13 `true | false` fields (8,192 combinations) matches
// every value, so the match is accepted; a missing case is a true witness;
// past the search cap the verdict is too-complex with no missing fact.
func TestE0306_AltExpansionCap(t *testing.T) {
	both := func(int) string { return "true | false" }
	w := newWorld(t, true)
	if ds := w.add("m", opt{}, boolMatch(13, both, "")).bodyDiags(t); len(ds) != 0 {
		t.Fatalf("13 x (true | false): %s", dump(w, ds))
	}
	w2 := newWorld(t, true)
	firstTrue := func(k int) string {
		if k == 0 {
			return "true"
		}
		return "true | false"
	}
	ds := w2.add("m", opt{}, boolMatch(13, firstTrue, "")).bodyDiags(t)
	if len(ds) != 1 || ds[0].Code != diag.E0306 {
		t.Fatalf("f0: true only: %s", dump(w2, ds))
	}
	if f, _ := ds[0].FactValue("missing"); len(f.List) != 1 || f.List[0].Str != "S(f0: false)" {
		t.Errorf("missing %+v", f.List)
	}
	w3 := newWorld(t, true)
	ds = w3.add("m", opt{}, boolMatch(13, firstTrue, "\t\tS(f0: false) {\n\t\t\treturn 2\n\t\t}\n")).bodyDiags(t)
	if len(ds) != 0 {
		t.Fatalf("completed by a second arm: %s", dump(w3, ds))
	}
	w4 := newWorld(t, true)
	ds = w4.add("m", opt{}, boolMatch(18, both, "")).bodyDiags(t)
	if len(ds) != 1 || ds[0].Code != diag.E0306 || fact(ds[0], "reason") != "too-complex" {
		t.Fatalf("past the cap: %s", dump(w4, ds))
	}
	if _, ok := ds[0].FactValue("missing"); ok {
		t.Errorf("a capped search has no witness: %s", dump(w4, ds))
	}
}

// TestConstPatterns: a named const is a pattern matched by its value, as a
// literal of that value: it covers that value for exhaustiveness, takes
// only the types a literal takes, and must have the scrutinee's type.
func TestConstPatterns(t *testing.T) {
	const consts = "const Limit = 8\nconst Low: u8 = 0\nconst High: u8 = 1\nconst On = true\nconst Hi = \"hi\"\nconst Half = 0.5\n\n"
	runBodyCases(t, []bodyCase{
		{"int", consts + "F(n: int): int {\n\tmatch n {\n\t\tLimit {\n\t\t\treturn 1\n\t\t}\n\t\t_ {\n\t\t\treturn 0\n\t\t}\n\t}\n}\n", ""},
		{"int without _", consts + "F(n: int): int {\n\tmatch n {\n\t\tLimit {\n\t\t\treturn 1\n\t\t}\n\t}\n\treturn 0\n}\n", "E0306@9:2"},
		{"u8 covered by consts and a range", consts + "F(n: u8): int {\n\tmatch n {\n\t\tLow | High {\n\t\t\treturn 1\n\t\t}\n\t\t2.. {\n\t\t\treturn 0\n\t\t}\n\t}\n}\n", ""},
		{"u8 gap", consts + "F(n: u8): int {\n\tmatch n {\n\t\tLow {\n\t\t\treturn 1\n\t\t}\n\t\t2.. {\n\t\t\treturn 0\n\t\t}\n\t}\n\treturn 2\n}\n", "E0306@9:2"},
		{"bool covered", consts + "F(b: bool): int {\n\tmatch b {\n\t\tOn {\n\t\t\treturn 1\n\t\t}\n\t\tfalse {\n\t\t\treturn 0\n\t\t}\n\t}\n}\n", ""},
		{"str", consts + "F(s: str): int {\n\tmatch s {\n\t\tHi {\n\t\t\treturn 1\n\t\t}\n\t\t_ {\n\t\t\treturn 0\n\t\t}\n\t}\n}\n", ""},
		{"inside some", consts + "F(x: ?u8): int {\n\tmatch x {\n\t\tsome(Low) {\n\t\t\treturn 1\n\t\t}\n\t\t_ {\n\t\t\treturn 0\n\t\t}\n\t}\n}\n", ""},
		{"type mismatch", consts + "F(n: u8): int {\n\tmatch n {\n\t\tLimit {\n\t\t\treturn 1\n\t\t}\n\t\t_ {\n\t\t\treturn 0\n\t\t}\n\t}\n}\n", "E0301@10:3"},
		{"float const", consts + "F(x: f64): int {\n\tmatch x {\n\t\tHalf {\n\t\t\treturn 1\n\t\t}\n\t\t_ {\n\t\t\treturn 0\n\t\t}\n\t}\n}\n", "E0308@10:3"},
		{"enum const", "enum C {\n\tRed\n\tGreen\n}\n\nconst Def = C.Red\n\nF(c: C): int {\n\tmatch c {\n\t\tDef {\n\t\t\treturn 1\n\t\t}\n\t\t_ {\n\t\t\treturn 0\n\t\t}\n\t}\n}\n", "E0301@10:3"},
		{"with fields", consts + "F(n: int): int {\n\tmatch n {\n\t\tLimit(x) {\n\t\t\treturn x\n\t\t}\n\t\t_ {\n\t\t\treturn 0\n\t\t}\n\t}\n}\n", "E0301@10:3"},
	})
}
