// Why: literals, operators and conversions have exact typing rules
// (ranges, lossless conversions, Cast and Trunc) that check alone enforces.
// What: TestLiteralAdapts, TestE0303_*, TestFloatLiteralConsts,
// TestConstConversion, TestCastTrunc, TestE0302_NoConversion,
// TestE0308_NoOperator, TestE0301_TypeMismatch,
// TestNegativeLiteralConstOnUnary, TestUntypedConstType.
// How: checks small bodies and compares codes and Body.Consts values.

package check

import (
	"testing"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

// bodyDiags runs Check on every unit of the module, each under a guard, and
// returns their diagnostics; an ICE fails the test.
func (b *built) bodyDiags(t *testing.T) []*diag.Diagnostic {
	t.Helper()
	var out []*diag.Diagnostic
	for _, id := range b.m.Units() {
		it := b.w.items.Get(id)
		var body *Body
		if ie := source.Guard(it.Key.String(), it.Span, func() { body = b.w.c.Check(b.m, id) }); ie != nil {
			t.Fatalf("ICE in %s: %v", it.Key, ie)
		}
		out = append(out, body.Diags...)
	}
	return out
}

// checkBody adds a module, checks its units and compares every diagnostic
// (resolve's, stage 5's and stage 6's) with want.
func checkBody(t *testing.T, w *world, want string, src string) *built {
	t.Helper()
	b := w.add("m", opt{}, src)
	var ds []*diag.Diagnostic
	for _, d := range append(b.diags(), b.bodyDiags(t)...) {
		if d.Code != diag.E0208 { // unused locals in short test bodies
			ds = append(ds, d)
		}
	}
	if got := codes(w, ds); got != want {
		t.Fatalf("diagnostics %q, want %q\n%s", got, want, dump(w, ds))
	}
	return b
}

type bodyCase struct{ name, src, want string }

func runBodyCases(t *testing.T, cases []bodyCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			checkBody(t, newWorld(t, true), tc.want, tc.src)
		})
	}
}

func TestLiteralAdapts(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"adapts", "F() {\n\tx: i8 = -128\n\tvar s: i64 = 0\n\ty: u64 = 18446744073709551615\n\tz: f32 = 1.5\n\tw := 1.5 * 2.0\n\tb: u8 = b'a'\n\ts = 1 + s\n}\n", ""},
		{"int does not adapt to a float", "F() {\n\tx: f64 = 1\n}\n", "E0301@2:11"},
		{"none needs an expected optional", "F() {\n\tx := none\n\ty: ?int = none\n}\n", "E0711@2:7"},
		{"literal of a type parameter", "G[T](x: T): T {\n\ty: T = 1\n\treturn x\n}\n", "E0703@2:9"},
	})
}

func TestE0303_LiteralRange(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"u8", "F() {\n\tx: u8 = 300\n}\n", "E0303@2:10"},
		{"i8 low", "F() {\n\tx: i8 = -129\n}\n", "E0303@2:11"},
		{"int", "F() {\n\tx := 9223372036854775808\n}\n", "E0303@2:7"},
		{"negative unsigned", "F() {\n\tx: u32 = -1\n}\n", "E0303@2:12"},
		// Only check knows a float literal is an f32.
		{"f32", "F() {\n\tx: f32 = 1e39\n\t_ = x\n}\n", "E0303@2:11"},
		{"f32 negative", "F(): f32 {\n\treturn -1e39\n}\n", "E0303@2:10"},
		{"f32 fits", "F(): f32 {\n\tx: f32 = 3.4e38\n\treturn -x + 1e-50\n}\n", ""},
		{"f64 holds 1e39", "F(): f64 {\n\treturn 1e39\n}\n", ""},
	})
}

// TestFloatLiteralConsts: every accepted float literal has its
// value in Body.Consts, f32 and f64 alike.
func TestFloatLiteralConsts(t *testing.T) {
	w := newWorld(t, true)
	b := checkBody(t, w, "", "F(): f32 {\n\tx: f32 = 3.4e38\n\ty: f64 = 1e300\n\t_ = y\n\treturn x + 1e-50\n}\n")
	body := w.c.Check(b.m, b.item("F"))
	n := 0
	ast.Inspect(body.Node, func(x ast.Node) bool {
		if l, ok := x.(*ast.BasicLit); ok && l.Kind == ast.TokFloat {
			n++
			if body.Consts == nil || body.Consts[body.slot(l.ID())] == types.NoConst {
				t.Errorf("literal %s has no value", l.Raw)
			}
		}
		return x != nil
	})
	if n != 3 {
		t.Fatalf("%d float literals", n)
	}
}

func TestE0303_Lossy(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"widen", "Widen(x: i32): i64 {\n\treturn i64(x)\n}\n\nW2(x: u32): i64 {\n\treturn i64(x)\n}\n\nW3(x: i16): f32 {\n\treturn f32(x)\n}\n\nW4(c: char): u32 {\n\treturn u32(c)\n}\n\nW5(b: u8): char {\n\treturn char(b)\n}\n", ""},
		{"narrow", "Narrow(x: i64): i32 {\n\treturn i32(x)\n}\n", "E0303@2:9"},
		{"signed to unsigned", "F(x: i8): u64 {\n\treturn u64(x)\n}\n", "E0303@2:9"},
		{"wide int to float", "F(x: i64): f64 {\n\treturn f64(x)\n}\n", "E0303@2:9"},
		{"float to int", "F(x: f64): int {\n\treturn int(x)\n}\n", "E0303@2:9"},
		{"int to char", "F(x: u32): char {\n\treturn char(x)\n}\n", "E0303@2:9"},
		{"constant fits", "F(): u8 {\n\treturn u8(200)\n}\n\nG(): f64 {\n\treturn f64(1)\n}\n", ""},
		{"constant does not fit", "F(): u8 {\n\treturn u8(300)\n}\n", "E0303@2:9"},
		{"float literal exact in f32", "F(): f32 {\n\treturn f32(0.5)\n}\n\nG(): f32 {\n\treturn f32(-(1.5e3))\n}\n\nH(): f32 {\n\treturn f32(16_777_216.0)\n}\n", ""},
		{"float literal rounds in f32", "F(): f32 {\n\treturn f32(0.1)\n}\n", "E0303@2:9"},
		{"float literal overflows f32", "F(): f32 {\n\treturn f32(1e39)\n}\n", "E0303@2:9"},
		{"float literal past f32's 24 bits", "F(): f32 {\n\treturn f32(16777217.0)\n}\n", "E0303@2:9"},
		{"float variable", "F(x: f64): f32 {\n\treturn f32(x)\n}\n", "E0303@2:9"},
	})
}

// TestConstConversion: a constant operand is
// judged by its value, a run-time one by its type on the 64-bit targets.
func TestConstConversion(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"named const", "const PageCount: int = 256\n\nF(): u64 {\n\treturn u64(PageCount)\n}\n", ""},
		{"untyped const", "const Small = 200\n\nF(): u8 {\n\treturn u8(Small)\n}\n", ""},
		{"untyped const too big", "const Big = 300\n\nF(): u8 {\n\treturn u8(Big)\n}\n", "E0303@4:9"},
		{"int from u32", "F(x: u32): int {\n\treturn int(x)\n}\n", ""},
		{"i64 from int", "F(x: int): i64 {\n\treturn i64(x)\n}\n", ""},
		{"identity", "F(x: u16): u16 {\n\treturn u16(x)\n}\n", ""},
		{"literal too big", "F(): u8 {\n\treturn u8(300)\n}\n", "E0303@2:9"},
		{"bool from int", "F(): bool {\n\treturn bool(1)\n}\n", "E0302@2:9"},
		{"float const exact in f32", "const Half: f64 = 0.5\n\nconst Quarter = -0.25\n\nF(): f32 {\n\treturn f32(Half) + f32(Quarter)\n}\n", ""},
		{"float operator over literals", "F(): f32 {\n\treturn f32(0.25 + 0.25)\n}\n", "E0303@2:9"},
		{"u64 const past int64", "const Top: u64 = 9223372036854775808\n\nconst Max: u64 = 9223372036854775807\n\nF(): i64 {\n\treturn i64(Top) + i64(Max)\n}\n", "E0303@6:9"},
		{"enum const by type", "enum W: i32 {\n\tLo = -2\n\tHi = 300\n}\n\nconst K: W = W.Hi\n\nF(): i8 {\n\treturn i8(K)\n}\n", "E0303@9:9"},
		{"float operator over named consts", "const Half: f64 = 0.5\n\nF(): f32 {\n\treturn f32(Half + Half)\n}\n\nG(): u8 {\n\treturn u8(-(Half * Half))\n}\n", "E0303@4:9 E0303@8:9"},
	})
}

func TestE0302_NoConversion(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"bool from int", "F(): bool {\n\treturn bool(1)\n}\n", "E0302@2:9"},
		{"int from bool", "F(b: bool): int {\n\treturn int(b)\n}\n", "E0302@2:9"},
		{"int from str", "F(s: str): int {\n\treturn int(s)\n}\n", "E0302@2:9"},
		{"reference", "F(r: &int): int {\n\treturn int(r)\n}\n", "E0302@2:9"},
	})
}

func TestCastTrunc(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"accepts", "F(n: i64): f64 {\n\treturn Cast[f64](n)\n}\n\nG(): u64 {\n\treturn Trunc[u64](-1)\n}\n\nH(x: f64): u8 {\n\treturn Cast[u8](x)\n}\n", ""},
		{"Trunc on a float", "F(x: f64): f32 {\n\treturn Trunc[f32](x)\n}\n", "E0302@2:9"},
		{"Cast of a str", "F(s: str): int {\n\treturn Cast[int](s)\n}\n", "E0302@2:9"},
	})
}

func TestE0308_NoOperator(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"accepts", "F(a: str, b: str, x: i32, c: char): bool {\n\ty := x + 1\n\tz := 1 + x\n\tq := x << 3\n\tw := ~x\n\treturn a < b && c == 'a' && y == z && q != w\n}\n", ""},
		{"str plus", "F(s: str, t: str): bool {\n\treturn s + t == s\n}\n", "E0308@2:9"},
		{"negate unsigned", "F(u: u32): u32 {\n\treturn -u\n}\n", "E0308@2:9"},
		{"not on int", "F(n: int): bool {\n\treturn !n\n}\n", "E0308@2:9"},
		{"bool order", "F(a: bool, b: bool): bool {\n\treturn a < b\n}\n", "E0308@2:9"},
		{"raw pointers", "F(p: *int, q: *int): bool {\n\treturn p == q\n}\n", "E0308@2:9"},
		{"references", "F(r: int, s: int): bool {\n\treturn &r == &s\n}\n", "E0308@2:9"},
		{"none", "F(x: ?int): bool {\n\treturn x == none\n}\n", "E0308@2:9"},
		{"bit and on bool", "F(a: bool, b: bool): bool {\n\treturn a & b\n}\n", "E0308@2:9"},
		{"operator on a parameter", "Biggest[T](a: &T, b: &T): bool {\n\treturn *a > *b\n}\n", "E0703@2:9"},
	})
}

func TestE0301_TypeMismatch(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"Sign", "Sign(x: i32): i32 {\n\ts: i32 = 1.0\n\treturn s\n}\n", "E0301@2:11"},
		{"return", "F(): int {\n\treturn true\n}\n", "E0301@2:9"},
		{"operands", "F(a: i32, b: i64): i64 {\n\treturn a + b\n}\n", "E0301@2:13"},
		{"references", "F(x: int): &int {\n\ty := &x\n\tz: mut int = &x\n\treturn y\n}\n", "E0301@3:15"},
		{"deref", "F(x: &int, p: *void): int {\n\ty := *p\n\treturn *x\n}\n", "E0301@2:7"},
	})
}

func TestNegativeLiteralConstOnUnary(t *testing.T) {
	w := newWorld(t, true)
	b := checkBody(t, w, "E0301@7:11", "F() {\n\tx: i8 = -128\n}\n\ntest \"t\" (sys: &System) {\n\ty: int = 1\n\tz: int = sys\n}\n")
	body := w.c.Check(b.m, b.item("F"))
	fd := body.Node.(*ast.FuncDecl)
	un := fd.Body.Stmts[0].(*ast.BindStmt).Value.(*ast.UnaryExpr)
	k := body.Consts[body.slot(un.ID())]
	if v, ok := w.c.constInt(k); !ok || v != -128 || body.Consts[body.slot(un.X.ID())] != types.NoConst {
		t.Errorf("const on the unary %v %d, on the literal %v", ok, v, body.Consts[body.slot(un.X.ID())])
	}
}

func TestUntypedConstType(t *testing.T) {
	w := newWorld(t, true)
	b := checkBody(t, w, "E0301@6:11", "const N = 3\nconst H = 1.5\nconst M = N\n\nF() {\n\tx: i64 = N\n\ty: f64 = H\n}\n")
	for _, k := range []string{"N", "M"} {
		if d := b.decl(k).(*types.ConstDecl); d.Type != types.Int {
			t.Errorf("%s: type %v", k, b.ts(d.Type, nil))
		}
	}
	if d := b.decl("H").(*types.ConstDecl); d.Type != types.F64 {
		t.Errorf("H: type %v", b.ts(d.Type, nil))
	}
}
