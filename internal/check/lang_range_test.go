// Why: `become` (a guaranteed tail call) has refusals check must give, and
// range patterns need typing and exhaustiveness over intervals.
// What: TestLang* for E0312, E0313, E0314, E0406 and E0806, accepted
// forms, range pattern typing (E0315) and exhaustiveness.
// How: checks small programs and compares codes; exhaustiveness is checked at
// every integer width and sign and for char.

package check

import (
	"fmt"
	"strings"
	"testing"
)

// TestLangBecomeAccepted: a direct, generic, method, fn-table and
// fn-parameter callee of the caller's signature, a parameter passed on at
// its position whatever its size, a fallible callee, flows forwarded.
func TestLangBecomeAccepted(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"direct", "Count(n: int, acc: int): int {\n\tif n == 0 {\n\t\treturn acc\n\t}\n\tbecome Count(n - 1, acc + 1)\n}\n", ""},
		{"generic callee", "G[T](x: T, n: int): T {\n\tif n == 0 {\n\t\treturn x\n\t}\n\tbecome G[T](x, n - 1)\n}\nH[U](x: U, n: int): U {\n\tbecome G(x, n)\n}\n", ""},
		{"fn table", "struct Vm {\n\tk: int\n}\nStep(vm: &Vm, n: int): int {\n\tif n == 0 {\n\t\treturn vm.k\n\t}\n\tbecome Table[n % 2](vm, n - 1)\n}\nconst Table: [2]fn(&Vm, int): int = [Step, Step]\n", ""},
		{"method", "struct Box {\n\tn: int\n}\nBox.Down(&self, k: int): int {\n\tif k == 0 {\n\t\treturn self.n\n\t}\n\tbecome self.Down(k - 1)\n}\n", ""},
		{"big parameter passed on", "struct Big {\n\ta: int\n\tb: int\n\tc: int\n}\nSum(b: Big, n: int): int {\n\tif n == 0 {\n\t\treturn b.a\n\t}\n\tbecome Sum(b, n - 1)\n}\n", ""},
		{"fallible", "error Bad\nDown(n: int): Bad!int {\n\tif n < 0 {\n\t\tfail Bad\n\t}\n\tif n == 0 {\n\t\treturn 0\n\t}\n\tbecome Down(n - 1)\n}\n", ""},
		{"flows", "Pick(a: &int, b: &int): &int from a {\n\tbecome Pick(a, b)\n}\n", ""},
		{"plain structs of 1, 2, 4 and 8 bytes", "struct P8 {\n\ta: i32\n\tb: i32\n}\nstruct P2 {\n\ta: u8\n\tb: u8\n}\nF(x: P8, y: P2, n: int): int {\n\tif n == 0 {\n\t\treturn 0\n\t}\n\tbecome F(P8(a: 1, b: 2), P2(a: 1, b: 2), n - 1)\n}\n", ""},
		{"register kinds", "F(a: i8, b: u64, c: f32, d: bool, e: char, f: &int, g: *u8, h: fn(int): int, n: int): int {\n\tbecome F(a, b, c, d, e, f, g, h, n)\n}\n", ""},
	})
}

// TestLangE0312: the callee's signature differs from the caller's.
func TestLangE0312(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"parameters", "Add(a: int, b: int): int {\n\treturn a + b\n}\nF(n: int): int {\n\tbecome Add(n, 1)\n}\n", "E0312@5:9"},
		{"result", "G(n: int): i32 {\n\treturn 0\n}\nF(n: int): int {\n\tbecome G(n)\n}\n", "E0312@5:9"},
		{"error set", "error Bad\nG(n: int): Bad!int {\n\treturn n\n}\nF(n: int): int {\n\tbecome G(n)\n}\n", "E0312@6:9"},
		{"flows", "G(a: &int, b: &int): &int from b {\n\treturn b\n}\nF(a: &int, b: &int): &int from a {\n\tbecome G(a, b)\n}\n", "E0312@5:9"},
		{"fn value", "F(f: fn(int, int): int, n: int): int {\n\tbecome f(n, 1)\n}\n", "E0312@2:9"},
		{"fn parameter", "Call(f: fn(int): int, x: int): int {\n\tbecome f(x)\n}\n", "E0312@2:9"},
		{"generic instance", "G[T](x: T): T {\n\treturn x\n}\nF[T](x: T): T {\n\tbecome G[int](1)\n}\n", "E0312@5:9"},
		{"ensures", "G(n: int): int {\n\treturn n\n}\nF(n: int): int\n\tensures result >= 0\n{\n\tbecome G(n)\n}\n", "E0312@7:9"},
		{"const parameter", "G(const k: int, n: int): int {\n\treturn n\n}\nF(const k: int, n: int): int {\n\tbecome G(k, n)\n}\n", "E0312@5:9"},
	})
}

// TestLangE0313: an argument some target passes in memory.
func TestLangE0313(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"a new big struct", "struct Big {\n\ta: int\n\tb: int\n\tc: int\n}\nF(b: Big, n: int): int {\n\tbecome F(Big(a: 1, b: 2, c: 3), n - 1)\n}\n", "E0313@7:11"},
		{"a parameter at another position", "struct Big {\n\ta: int\n\tb: int\n\tc: int\n}\nF(x: Big, y: Big): int {\n\tbecome F(y, x)\n}\n", "E0313@7:11 E0313@7:14"},
		{"a var parameter", "struct Big {\n\ta: int\n\tb: int\n\tc: int\n}\nF(var b: Big, n: int): int {\n\tb.a = 1\n\tbecome F(b, n)\n}\n", "E0313@8:11"},
		{"a slice", "F(xs: []int, n: int): int {\n\tbecome F(xs[1..], n)\n}\n", "E0313@2:11"},
		{"a str passed on is fine", "F(s: str, n: int): int {\n\tbecome F(s, n)\n}\n", ""},
		{"a 3-byte struct", "struct P3 {\n\ta: u8\n\tb: u8\n\tc: u8\n}\nF(p: P3, n: int): int {\n\tbecome F(P3(a: 1, b: 2, c: 3), n)\n}\n", "E0313@7:11"},
		{"a type parameter", "F[T](x: T, y: T): T {\n\tbecome F(y, x)\n}\n", "E0313@2:11 E0313@2:14"},
	})
}

// TestLangE0314: targets a tail call cannot reach.
func TestLangE0314(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"bound", "interface Shape {\n\tArea(&self): f64\n}\nM[T: Shape](s: &T): f64 {\n\tbecome s.Area()\n}\n", "E0314@5:9"},
		{"cpu", "Fast(x: int): int\n\tcpu \"avx2\"\n{\n\treturn x\n}\nSlow(x: int): int {\n\tunsafe \"t\" {\n\t\tbecome Fast(x)\n\t}\n}\n", "E0314@8:10"},
		{"construction", "struct P {\n\tx: int\n}\nF(x: int): P {\n\tbecome P(x: x)\n}\n", "E0314@5:9"},
		{"builtin", "F(x: int): never {\n\tbecome Panic(\"x\")\n}\n", "E0314@2:9"},
		{"test block", "G(): void {\n}\ntest \"t\" {\n\tbecome G()\n}\n", "E0314@4:2"},
	})
}

// TestLangE0406AndE0806: something left to run after the callee.
func TestLangE0406AndE0806(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"defer", "Nop() {\n}\nF(n: int): int {\n\tdefer Nop()\n\tbecome F(n)\n}\n", "E0406@5:2"},
		{"defer in an outer block", "Nop() {\n}\nF(n: int): int {\n\tdefer Nop()\n\tif n > 0 {\n\t\tbecome F(n - 1)\n\t}\n\treturn 0\n}\n", "E0406@6:3"},
		{"defer after is fine", "Nop() {\n}\nF(n: int): int {\n\tif n > 0 {\n\t\tbecome F(n - 1)\n\t}\n\tdefer Nop()\n\treturn 0\n}\n", ""},
		{"defer in a sibling block is fine", "Nop() {\n}\nF(n: int): int {\n\tif n > 5 {\n\t\tdefer Nop()\n\t}\n\tif n > 0 {\n\t\tbecome F(n - 1)\n\t}\n\treturn 0\n}\n", ""},
		{"inside a defer", "G(): void {\n}\nF(): void {\n\tdefer {\n\t\tbecome G()\n\t}\n}\n", "E0806@5:3"},
	})
}

// TestLangRangePatternTyping: ends of the scrutinee's type, E0315 for an
// empty range, E0303 for an end outside the type, E0301 for a range on a
// type with no order or an end of the other kind.
func TestLangRangePatternTyping(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"ints", "F(x: i8): int {\n\treturn match x {\n\t\t-128..0 { 1 }\n\t\t0.. { 2 }\n\t}\n}\n", ""},
		{"chars", "F(c: char): int {\n\treturn match c {\n\t\t..'a' { 1 }\n\t\t'a'..'{' { 2 }\n\t\t'{'.. { 3 }\n\t}\n}\n", ""},
		{"empty", "F(x: int): int {\n\treturn match x {\n\t\t5..5 { 1 }\n\t\t_ { 0 }\n\t}\n}\n", "E0315@3:3"},
		{"reversed", "F(x: int): int {\n\treturn match x {\n\t\t5..-5 { 1 }\n\t\t_ { 0 }\n\t}\n}\n", "E0315@3:3"},
		{"empty open low", "F(x: u8): int {\n\treturn match x {\n\t\t..0 { 1 }\n\t\t_ { 0 }\n\t}\n}\n", "E0315@3:3"},
		{"end outside the type", "F(x: u8): int {\n\treturn match x {\n\t\t0..256 { 1 }\n\t\t_ { 0 }\n\t}\n}\n", "E0303@3:6"},
		{"char end on an int", "F(x: int): int {\n\treturn match x {\n\t\t'a'..'z' { 1 }\n\t\t_ { 0 }\n\t}\n}\n", "E0301@3:3 E0301@3:8"},
		{"on a bool", "F(x: bool): int {\n\treturn match x {\n\t\t0..1 { 1 }\n\t\t_ { 0 }\n\t}\n}\n", "E0301@3:3"},
		{"in a destructure", "struct P {\n\tx: int\n}\nF(p: P): int {\n\tP(x: 1..5) := p\n\treturn 0\n}\n", "E0306@5:7"},
	})
}

// TestLangRangeExhaustiveness: a match covering every value by ranges
// and literals is exhaustive at every integer width and sign, and for char
// (whose surrogates no range needs to cover); one value short is E0306
// naming it.
func TestLangRangeExhaustiveness(t *testing.T) {
	type width struct {
		ty       string
		min, max string
	}
	ws := []width{
		{"i8", "-128", "127"}, {"i16", "-32768", "32767"}, {"i32", "-2147483648", "2147483647"},
		{"i64", "-9223372036854775808", "9223372036854775807"}, {"int", "-9223372036854775808", "9223372036854775807"},
		{"u8", "0", "255"}, {"u16", "0", "65535"}, {"u32", "0", "4294967295"},
		{"u64", "0", "18446744073709551615"}, {"uint", "0", "18446744073709551615"},
	}
	var cases []bodyCase
	for _, w := range ws {
		cases = append(cases,
			bodyCase{w.ty + " open ends", fmt.Sprintf("F(x: %s): int {\n\treturn match x {\n\t\t..10 { 1 }\n\t\t10.. { 2 }\n\t}\n}\n", w.ty), ""},
			bodyCase{w.ty + " limits", fmt.Sprintf("F(x: %s): int {\n\treturn match x {\n\t\t%s { 0 }\n\t\t%s..%s { 1 }\n\t\t%s { 2 }\n\t}\n}\n", w.ty, w.min, w.min+"+1", w.max, w.max), ""},
			bodyCase{w.ty + " missing the maximum", fmt.Sprintf("F(x: %s): int {\n\treturn match x {\n\t\t..%s { 1 }\n\t}\n}\n", w.ty, w.max), "E0306@2:9"},
			bodyCase{w.ty + " missing one value", fmt.Sprintf("F(x: %s): int {\n\treturn match x {\n\t\t..7 { 1 }\n\t\t8.. { 2 }\n\t}\n}\n", w.ty), "E0306@2:9"},
		)
	}
	// "+1" is no literal: build the limits case with the next value spelled.
	for i := range cases {
		cases[i].src = strings.NewReplacer(
			"-128+1", "-127", "-32768+1", "-32767", "-2147483648+1", "-2147483647",
			"-9223372036854775808+1", "-9223372036854775807", "0+1", "1").Replace(cases[i].src)
	}
	cases = append(cases,
		bodyCase{"char whole", "F(c: char): int {\n\treturn match c {\n\t\t..'\\u{D7FF}' { 1 }\n\t\t'\\u{D7FF}' { 2 }\n\t\t'\\u{E000}'.. { 3 }\n\t}\n}\n", ""},
		bodyCase{"char missing the last", "F(c: char): int {\n\treturn match c {\n\t\t..'\\u{10FFFF}' { 1 }\n\t}\n}\n", "E0306@2:9"},
		bodyCase{"or-patterns", "F(x: u8): int {\n\treturn match x {\n\t\t0..10 | 20..30 { 1 }\n\t\t10..20 | 30.. { 2 }\n\t}\n}\n", ""},
		bodyCase{"inside a struct field", "struct P {\n\tx: u8\n\tok: bool\n}\nF(p: P): int {\n\treturn match p {\n\t\tP(x: ..128, ok: true) { 1 }\n\t\tP(x: 128.., ok: true) { 2 }\n\t\tP(ok: false) { 3 }\n\t}\n}\n", ""},
		bodyCase{"inside a struct field, one missing", "struct P {\n\tx: u8\n\tok: bool\n}\nF(p: P): int {\n\treturn match p {\n\t\tP(x: ..128, ok: true) { 1 }\n\t\tP(x: 129.., ok: true) { 2 }\n\t\tP(ok: false) { 3 }\n\t}\n}\n", "E0306@6:9"},
		bodyCase{"a covered arm is no error (G-56)", "F(x: u8): int {\n\treturn match x {\n\t\t..200 { 1 }\n\t\t100..150 { 2 }\n\t\t200.. { 3 }\n\t}\n}\n", ""},
	)
	runBodyCases(t, cases)
}

// TestLangU8LiteralMatchExhaustive: a match over all 256 values of u8 by literals is
// exhaustive (it was E0306); 255 of them are not, and E0306 names
// the one left out.
func TestLangU8LiteralMatchExhaustive(t *testing.T) {
	arms := func(skip int) string {
		var b strings.Builder
		for v := 0; v < 256; v++ {
			if v != skip {
				fmt.Fprintf(&b, "\t\t%d { %d }\n", v, v%7)
			}
		}
		return "F(x: u8): int {\n\treturn match x {\n" + b.String() + "\t}\n}\n"
	}
	runBodyCases(t, []bodyCase{
		{"all 256", arms(-1), ""},
		{"by bytes", "F(x: u8): int {\n\treturn match x {\n\t\tb'a' { 1 }\n\t\t..97 | 98.. { 0 }\n\t}\n}\n", ""},
	})
	w := newWorld(t, true)
	b := w.add("m", opt{}, arms(77))
	ds := b.bodyDiags(t)
	if len(ds) != 1 || !strings.Contains(ds[0].Message, "77") {
		t.Errorf("255 arms: %s", dump(w, ds))
	}
}

// TestLangE0314Forms: an extern, a std intrinsic and a naked caller, and
// E0312 for a `mut self` method of a struct with invariants (lower checks
// them before every exit).
func TestLangE0314Forms(t *testing.T) {
	cases := []struct {
		name string
		o    opt
		src  string
		want string
	}{
		{"extern", opt{unsafe: true}, "extern Puts(s: *u8): i32\n\nF(s: *u8): i32 {\n\tunsafe \"t\" {\n\t\tbecome Puts(s)\n\t}\n}\n", "E0314@5:10"},
		{"intrinsic", opt{std: true}, "export Ctz(x: u64): u64\n\nF(x: u64): u64 {\n\tbecome Ctz(x)\n}\n", "E0314@4:9"},
		{"naked", opt{}, "G() {\n}\n\nnaked Halt() {\n\tbecome G()\n}\n", "E0314@5:9 E1105@5:2"},
		{"invariants", opt{}, "struct Span {\n\tlo: int\n\thi: int\n\tinvariant lo <= hi\n}\n\nSpan.Grow(mut self, n: int) {\n\tif n == 0 {\n\t\treturn\n\t}\n\tself.hi += 1\n\tbecome self.Grow(n - 1)\n}\n", "E0312@12:9"},
		{"invariants only on mut self", opt{}, "struct Span {\n\tlo: int\n\thi: int\n\tinvariant lo <= hi\n}\n\nSpan.Len(&self, n: int): int {\n\tif n == 0 {\n\t\treturn self.hi - self.lo\n\t}\n\tbecome self.Len(n - 1)\n}\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			checkBodyIn(t, newWorld(t, true), c.o, c.want, c.src)
		})
	}
}

// TestLangBecomeNarrowedAndInLoops: a become inside `if T is X`
// passing the narrowed parameter, and one inside a for loop over a range
// and over a slice, check (lowered and run by the driver's tests). G[T]
// written in the branch is G[int], whose signature is not the generic's
// (E0312, as G[int] written so).
func TestLangBecomeNarrowedAndInLoops(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"in a narrowing branch", "H[T](n: int): int {\n\tif T is int {\n\t\tif n > 0 {\n\t\t\tbecome H[T](n - 1)\n\t\t}\n\t}\n\treturn n\n}\n", ""},
		{"T written in the branch is int", "G[T](x: T, n: int): T {\n\tif T is int {\n\t\tbecome G[T](x, n - 1)\n\t}\n\treturn x\n}\n", "E0312@3:10"},
		{"narrowed to int", "G[T](x: T, n: int): T {\n\tif T is int {\n\t\tbecome G[int](x, n - 1)\n\t}\n\treturn x\n}\n", "E0312@3:10"},
		{"in a range loop", "F(n: int): int {\n\tfor i in 0..n {\n\t\tif i == 3 {\n\t\t\tbecome F(n - 1)\n\t\t}\n\t}\n\treturn n\n}\n", ""},
		{"in a slice loop", "F(xs: &[]int, n: int): int {\n\tfor x in xs {\n\t\tif x == n {\n\t\t\tbecome F(xs, n + 1)\n\t\t}\n\t}\n\treturn n\n}\n", ""},
	})
}
