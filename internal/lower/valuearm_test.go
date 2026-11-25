// Why: an if, match or else in value position (a value arm) must lower to
// the same MIR as the statement form a programmer would write by hand.
// What: TestMIREqualsStatementForm, TestTempPositions,
// TestElseValueLowers, TestEvalAgrees, TestFoldedArm, and
// regression tests for the else default, implementer receivers and type
// tests.
// How: compares the MIR text of each value form with its statement form,
// and runs both under eval.

package lower

import (
	"strings"
	"testing"
)

// bodyText is a printed body without its first line (the body's name).
func bodyText(l *lowered, key string) string {
	s := l.print(key)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// TestMIREqualsStatementForm: a value construct bound to a local lowers
// to exactly the MIR of the hand-written statement form.
func TestMIREqualsStatementForm(t *testing.T) {
	pairs := []struct{ name, value, stmt string }{
		{"if", "V(c: bool): int {\n\tx := if c { 1 } else { 2 }\n\treturn x\n}\n",
			"S(c: bool): int {\n\tx: int\n\tif c {\n\t\tx = 1\n\t} else {\n\t\tx = 2\n\t}\n\treturn x\n}\n"},
		{"var", "V(c: bool): int {\n\tvar x := if c { 1 } else { 2 }\n\tx += 1\n\treturn x\n}\n",
			"S(c: bool): int {\n\tvar x: int\n\tif c {\n\t\tx = 1\n\t} else {\n\t\tx = 2\n\t}\n\tx += 1\n\treturn x\n}\n"},
		{"typed", "V(c: bool): i8 {\n\tx: i8 = if c { 1 } else { 2 }\n\treturn x\n}\n",
			"S(c: bool): i8 {\n\tx: i8\n\tif c {\n\t\tx = 1\n\t} else {\n\t\tx = 2\n\t}\n\treturn x\n}\n"},
		{"else if", "V(n: int): int {\n\tx := if n < 0 { -1 } else if n == 0 { 0 } else { 1 }\n\treturn x\n}\n",
			"S(n: int): int {\n\tx: int\n\tif n < 0 {\n\t\tx = -1\n\t} else if n == 0 {\n\t\tx = 0\n\t} else {\n\t\tx = 1\n\t}\n\treturn x\n}\n"},
		{"if bind", "V(o: ?int): int {\n\tx := if y := o { y + 1 } else { 0 }\n\treturn x\n}\n",
			"S(o: ?int): int {\n\tx: int\n\tif y := o {\n\t\tx = y + 1\n\t} else {\n\t\tx = 0\n\t}\n\treturn x\n}\n"},
		{"statements before", "V(c: bool): int {\n\tx := if c {\n\t\ty := 3\n\t\ty * 2\n\t} else {\n\t\t2\n\t}\n\treturn x\n}\n",
			"S(c: bool): int {\n\tx: int\n\tif c {\n\t\ty := 3\n\t\tx = y * 2\n\t} else {\n\t\tx = 2\n\t}\n\treturn x\n}\n"},
		{"arm returns", "V(c: bool): int {\n\tx := if c { 1 } else { return 0 }\n\treturn x\n}\n",
			"S(c: bool): int {\n\tx: int\n\tif c {\n\t\tx = 1\n\t} else {\n\t\treturn 0\n\t}\n\treturn x\n}\n"},
		{"match with bindings", "enum E {\n\tA(n: int)\n\tB\n}\nV(e: E): int {\n\tx := match e {\n\t\t.A(n) { n }\n\t\t.B { 0 }\n\t}\n\treturn x\n}\n",
			"enum E {\n\tA(n: int)\n\tB\n}\nS(e: E): int {\n\tx: int\n\tmatch e {\n\t\t.A(n) {\n\t\t\tx = n\n\t\t}\n\t\t.B {\n\t\t\tx = 0\n\t\t}\n\t}\n\treturn x\n}\n"},
		{"nested", "V(a: bool, b: bool): int {\n\tx := if a {\n\t\tif b { 1 } else { 2 }\n\t} else {\n\t\t3\n\t}\n\treturn x\n}\n",
			"S(a: bool, b: bool): int {\n\tx: int\n\tif a {\n\t\tif b {\n\t\t\tx = 1\n\t\t} else {\n\t\t\tx = 2\n\t\t}\n\t} else {\n\t\tx = 3\n\t}\n\treturn x\n}\n"},
		{"break and continue", "V(xs: []int): int {\n\tvar n := 0\n\tfor x in xs {\n\t\td := if x < 0 { continue } else if x > 9 { break } else { x }\n\t\tn += d\n\t}\n\treturn n\n}\n",
			"S(xs: []int): int {\n\tvar n := 0\n\tfor x in xs {\n\t\td: int\n\t\tif x < 0 {\n\t\t\tcontinue\n\t\t} else if x > 9 {\n\t\t\tbreak\n\t\t} else {\n\t\t\td = x\n\t\t}\n\t\tn += d\n\t}\n\treturn n\n}\n"},
	}
	for _, p := range pairs {
		t.Run(p.name, func(t *testing.T) {
			t.Parallel()
			lv := lowerSrc(t, p.value)
			ls := lowerSrc(t, p.stmt)
			if v, s := bodyText(lv, "V"), bodyText(ls, "S"); v != s {
				t.Errorf("value form:\n%s\nstatement form:\n%s", v, s)
			}
		})
	}
}

// TestTempPositions: a value construct outside a binding goes through
// one temporary, assigned in each arm and moved or copied once.
func TestTempPositions(t *testing.T) {
	for _, c := range []struct{ name, src string }{
		{"return", "F(c: bool): int {\n\treturn if c { 1 } else { 2 }\n}\n"},
		{"argument", "G(x: int): int {\n\treturn x\n}\nF(c: bool): int {\n\treturn G(if c { 1 } else { 2 })\n}\n"},
		{"field", "F(c: bool): int {\n\tr := (a: if c { 1 } else { 2 }, b: 3)\n\treturn r.a\n}\n"},
		{"element", "F(c: bool): int {\n\txs := [0, if c { 1 } else { 2 }]\n\treturn xs[1]\n}\n"},
		{"assignment", "F(c: bool): int {\n\tvar x := 0\n\tx = if c { 1 } else { 2 }\n\treturn x\n}\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			l := lowerSrc(t, c.src)
			text := l.print("F")
			wantIn(t, text, "= int 1", "goto", "= int 2", "goto", "copy _")
		})
	}
}

// TestElseValueLowers: an else handler that yields joins the success
// edge with one temporary.
func TestElseValueLowers(t *testing.T) {
	l := lowerSrc(t, "F(o: ?int): int {\n\tn := o else { 8 }\n\treturn n\n}\nerror Bad\nG(): Bad!int {\n\treturn 1\n}\nH(): int {\n\tn := G() else e {\n\t\t_ = e\n\t\t9\n\t}\n\treturn n\n}\n")
	wantIn(t, l.print("F"), "= int 8", "goto")
	wantIn(t, l.print("H"), "= int 9", "goto")
}

// TestEvalAgrees: each value form and its statement form evaluate to
// the same value at compile time.
func TestEvalAgrees(t *testing.T) {
	for _, c := range []struct {
		name, src string
		want      int64
	}{
		{"value", "Pick(n: int): int {\n\treturn if n < 0 { -1 } else if n == 0 { 0 } else { n * 2 }\n}\nH(): int {\n\tx := Pick(-3) + Pick(0) * 10\n\ty := match Pick(4) {\n\t\t8 { 100 }\n\t\t_ { 0 }\n\t}\n\treturn x + y\n}\n", 99},
		{"statement", "Pick(n: int): int {\n\tr: int\n\tif n < 0 {\n\t\tr = -1\n\t} else if n == 0 {\n\t\tr = 0\n\t} else {\n\t\tr = n * 2\n\t}\n\treturn r\n}\nH(): int {\n\tx := Pick(-3) + Pick(0) * 10\n\ty: int\n\tmatch Pick(4) {\n\t\t8 {\n\t\t\ty = 100\n\t\t}\n\t\t_ {\n\t\t\ty = 0\n\t\t}\n\t}\n\treturn x + y\n}\n", 99},
		{"else nested", "Get(o: ?int, c: bool): int {\n\treturn o else {\n\t\tif c { 10 } else {\n\t\t\tmatch c {\n\t\t\t\ttrue { 0 }\n\t\t\t\tfalse { 20 }\n\t\t\t}\n\t\t}\n\t}\n}\nH(): int {\n\treturn Get(none, true) + Get(none, false) + Get(some(3), true)\n}\n", 33},
		{"else value", "Get(o: ?int): int {\n\treturn o else { 7 }\n}\nH(): int {\n\treturn Get(none) + Get(some(2))\n}\n", 9},
		{"operands", "Pick(c: bool): int {\n\treturn 1 + if c { 10 } else { 20 } * 2\n}\nH(): int {\n\treturn Pick(true) + Pick(false) * 100 + (match Pick(true) {\n\t\t21 { 5 }\n\t\t_ { 0 }\n\t})\n}\n", 4126},
		{"left operand read first", "H(): int {\n\tvar x := 10\n\treturn x * 100 + if x > 0 {\n\t\tx = 7\n\t\tx\n\t} else {\n\t\t0\n\t}\n}\n", 1007},
		{"const patterns", "const Two = 2\nconst Neg: i8 = -1\nP(n: int): int {\n\treturn match n {\n\t\tTwo { 10 }\n\t\t_ { 1 }\n\t}\n}\nQ(s: i8): int {\n\treturn match s {\n\t\tNeg { 100 }\n\t\t..-1 { 200 }\n\t\t_ { 300 }\n\t}\n}\nH(): int {\n\treturn P(2) + P(3) + Q(-1) + Q(-2) + Q(0)\n}\n", 611},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := evalH(t, c.src)
			if err != nil || got != c.want {
				t.Errorf("got %d %v, want %d", got, err, c.want)
			}
		})
	}
}

// TestFoldedArm: a constant condition lowers only the taken arm.
func TestFoldedArm(t *testing.T) {
	l := lowerSrc(t, "const On = true\nF(): int {\n\tx := if On { 1 } else { 2 }\n\treturn x\n}\n")
	text := l.print("F")
	if strings.Contains(text, "int 2") || strings.Contains(text, "switch") {
		t.Errorf("untaken arm lowered:\n%s", text)
	}
}

// TestElseDefaultLowers: the default `x := o else { v }` lowers.
func TestElseDefaultLowers(t *testing.T) {
	l := lowerSrc(t, "F(o: ?int): int {\n\tx := o else { 3 }\n\treturn x\n}\n")
	_ = l.print("F")
}

// TestImplementerReceiver: a mut self call on an implementer's xs[i]
// borrows the element through ItemsMut, bounds-checked once.
func TestImplementerReceiver(t *testing.T) {
	l := lowerSrc(t, `struct Counter {
	n: int
}
Counter.Bump(mut self) {
	self.n += 1
}
BumpAt(xs: mut HeapList[Counter], i: int) {
	xs[i].Bump()
}
BumpSlice(cs: mut []Counter, i: int) {
	cs[i].Bump()
}
`)
	f := l.print("BumpAt")
	wantIn(t, f, ":ItemsMut[Counter](", "panic bounds", "call m.Counter:Bump(")
	if n := strings.Count(f, "panic bounds"); n != 1 {
		t.Errorf("%d bounds checks:\n%s", n, f)
	}
	if strings.Contains(f, ":Items[Counter](") {
		t.Errorf("shared Items used:\n%s", f)
	}
	_ = l.print("BumpSlice")
}

// destLocal is the printed name (`_N`) of the local the body's `let` line
// names name.
func destLocal(t *testing.T, text, name string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "let _") && strings.HasSuffix(line, "// "+name) {
			return strings.TrimSuffix(strings.Fields(line)[1], ":")
		}
	}
	t.Fatalf("no local %s in\n%s", name, text)
	return ""
}

// TestTypeTestBindingDest: a binding whose value is an `if T is X`
// construct is its own local, declared before the construct, and
// typeTest does not narrow it: no arm reads it before assigning it (the
// E0403 it once raised). The narrowed arm's value is retyped to the local
// by CastNarrow; Validate runs on every body. The same holds for a
// statement-form `if T is X` before an arm's last line.
func TestTypeTestBindingDest(t *testing.T) {
	for _, c := range []struct{ name, src, key string }{
		{"optional", "Opt[T: Plain](v: T): ?T {\n\tx: ?T = if T is int { none } else { some(v) }\n\treturn x\n}\n", "Opt"},
		{"no expected type", "Pick[T: Plain](v: T, w: T): T {\n\tx := if T is int { w } else { v }\n\treturn x\n}\n", "Pick"},
		{"statement form inside an arm", "Pick[T: Plain](v: T, w: T, c: bool): T {\n\tx := if c {\n\t\tif T is int {\n\t\t\t_ = w\n\t\t}\n\t\tv\n\t} else {\n\t\tv\n\t}\n\treturn x\n}\n", "Pick"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			text := lowerSrc(t, c.src).print(c.key)
			x := destLocal(t, text, "x")
			for _, line := range strings.Split(text, "\n") {
				if strings.Contains(line, "Narrow[") && (strings.Contains(line, "copy "+x+")") || strings.Contains(line, "move "+x+")")) {
					t.Errorf("the destination %s is narrowed before an arm assigns it:\n%s", x, text)
				}
			}
		})
	}
	text := lowerSrc(t, "Pick[T: Plain](v: T, w: T): T {\n\treturn if T is int { w + 1 } else { v }\n}\n").print("Pick")
	wantIn(t, text, "typetest $0 is int", "Add(", "Narrow[$0](")
}

// TestNonPlainNarrowedArm: with an unbounded T (it may be linear), the
// narrowed arm moves the value: narrowed to a plain X it is moved into the
// narrowed local and retyped back by CastNarrow, narrowed to a linear X it
// is not narrowed at all. Both forms validate (lowerSrc runs Validate).
func TestNonPlainNarrowedArm(t *testing.T) {
	for _, c := range []struct{ name, src, want string }{
		{"plain X, return", "Id[T](v: T): T {\n\treturn if T is int { v } else { v }\n}\n", "Narrow[int](move _1)"},
		{"plain X, binding", "Id[T](v: T): T {\n\tx := if T is int { v } else { v }\n\treturn x\n}\n", "Narrow[$0](copy"},
		{"linear X, return", "linear struct L {\n\tn: int\n}\nId[T](v: T): T {\n\treturn if T is L { v } else { v }\n}\n", "typetest $0 is L"},
		{"linear X, binding", "linear struct L {\n\tn: int\n}\nId[T](v: T): T {\n\tx := if T is L { v } else { v }\n\treturn x\n}\n", "typetest $0 is L"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			wantIn(t, lowerSrc(t, c.src).print("Id"), c.want)
		})
	}
}
