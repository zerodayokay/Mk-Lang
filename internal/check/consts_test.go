// Why: consts fold in two stages, early in signatures and late in bodies,
// and every fold, panic and cycle must give one right diagnostic.
// What: TestStageRule_*, TestFoldPanics, TestEvalErrors_E0901_E0902_E0909,
// TestIfConst, TestLiteralConsts, TestEarly*, TestConstConversion* and
// TestPoisonReadsOfFailedConst.
// How: checks modules against fakeEval and reads Body.Consts, the decls'
// values, the early set and the codes.

package check

import (
	"strings"
	"testing"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

// TestStageRule_SameModuleConstInType: a same-module const in a type is
// folded in stage 5, its decl gets the value, and
// the layout uses it; no evaluator is needed for a foldable initializer.
func TestStageRule_SameModuleConstInType(t *testing.T) {
	w := newWorld(t, true)
	b := checkBody(t, w, "", "const Slots: int = 254\nconst N = 2 * 3 + Slots - Slots\nstruct Record {\n\ta: u8\n}\nstruct Page {\n\trecords: [Slots]Record\n\tsmall: [N]u8\n}\nF(): int {\n\tx: [N]u8 = [0; N]\n\treturn x.Len()\n}\n")
	page := w.items.Get(b.item("Page"))
	l := w.c.Layout(w.tab.Named(page.ID, nil))
	if l.Shape.Size != 260 {
		t.Fatalf("Page size %d, want 260 (%s)", l.Shape.Size, w.tab.TypeString(page.Decl.(*types.StructDecl).Fields[0].Type, nil))
	}
	for _, name := range []string{"Slots", "N"} {
		d := w.items.Get(b.item(name)).Decl.(*types.ConstDecl)
		if !d.HasValue {
			t.Fatalf("%s has no value", name)
		}
	}
	if v, _ := w.c.constInt(w.items.Get(b.item("N")).Decl.(*types.ConstDecl).Value); v != 6 {
		t.Fatalf("N = %d, want 6", v)
	}
	var early []string
	for _, eb := range b.m.Early() {
		early = append(early, b.key(eb.Item))
	}
	if got := strings.Join(early, " "); got != "N Slots" { // key order
		t.Fatalf("Early = %q, want \"N Slots\"", got)
	}
	for _, u := range b.m.Units() {
		if k := b.key(u); k == "Slots" || k == "N" {
			t.Fatalf("early const %s is also a unit", k)
		}
	}
}

// TestFoldPanics: a typed overflow, a division by zero and a shift past the
// width while folding a const argument are E0901 at the operator; a typed
// value of the wrong type is E0301; an untyped one too large E0303.
func TestFoldPanics(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"overflow", "const A: u8 = 200\nstruct S {\n\tx: [A + 100]u8\n}\n", "E0901@3:6"},
		{"divide", "struct S {\n\tx: [4 / 0]u8\n}\n", "E0901@2:6"},
		{"shift", "struct S {\n\tx: [1 << 64]u8\n}\n", "E0901@2:6"},
		{"typed", "const A: u8 = 2\nstruct S {\n\tx: [A * 2]u8\n}\n", "E0301@3:6"},
		{"too large", "struct S {\n\tx: [3000000000 * 4000000000]u8\n}\n", "E0303@2:6"},
		{"negative ok to fold", "struct S {\n\tx: [8 - 3]u8\n}\n", ""},
	})
}

// TestStageRule_E0908: a same-module const whose initializer calls a
// same-module function is legal (it is evaluated at StageBodies), but a
// body's array length that needs it at StageSignatures is E0908 at the use,
// from the evaluator's EvalStage; the const is early and Prepared.
func TestStageRule_E0908(t *testing.T) {
	w := newWorld(t, true)
	fe := w.withEval()
	var prepared []string
	w.cfg.Prepare = func(b *Body) { prepared = append(prepared, w.items.Get(b.Item).Key.String()) }
	fe.fn = func(req *types.EvalRequest) (types.EvalResult, bool) {
		k := w.items.Get(req.Item).Key.String()
		if k == "m.K" && req.Stage == types.StageSignatures {
			return types.EvalResult{Err: &types.EvalError{Kind: types.EvalStage, Items: []types.ItemID{req.Item, computeID(w)},
				Trace: []source.Span{req.Site, {}}}}, true
		}
		return types.EvalResult{}, false
	}
	b := checkBody(t, w, "E0908@6:6", "Compute(): int {\n\treturn 4\n}\nconst K = Compute()\nF() {\n\tx: [K]u8 = [0; 4]\n}\n")
	ds := b.bodyDiags(t)
	if items := fact(ds[0], "items"); items != "[m.K m.Compute]" {
		t.Fatalf("items %s", items)
	}
	if got := strings.Join(prepared, " "); got != "m.K" {
		t.Fatalf("prepared %q", got)
	}
	if rq := fe.requests(); len(rq) == 0 || !strings.HasSuffix(rq[0], " m.K") || !strings.HasPrefix(rq[0], "0/0") {
		t.Fatalf("requests %v", rq)
	}
}

// TestStageRule_LayoutConst: a const whose type holds a same-module struct
// by value cannot be lowered in stage 5: not Prepared.
func TestStageRule_LayoutConst(t *testing.T) {
	w := newWorld(t, true)
	var prepared []string
	w.cfg.Prepare = func(b *Body) { prepared = append(prepared, w.items.Get(b.Item).Key.String()) }
	b := checkBody(t, w, "", "struct Pt {\n\tx: int\n}\nconst P: ?Pt = none\nconst Q: &Pt = &Pt(x: 1)\nconst N: int = 3\nstruct S {\n\ta: [N]u8\n}\n")
	if got := w.items.Get(b.m.localLayoutItem(w.items.Get(b.item("P")))).Key.String(); got != "m.Pt" {
		t.Fatalf("P needs %s", got)
	}
	if got := b.m.localLayoutItem(w.items.Get(b.item("Q"))); got != types.NoItem {
		t.Fatalf("Q (a reference) needs %v", got)
	}
	if got := strings.Join(prepared, " "); got != "m.N" {
		t.Fatalf("prepared %q", got)
	}
}

// TestEvalErrors_E0901_E0902_E0909: a const's own evaluation renders at
// its name; a use of a failing const is poison with no diagnostic.
func TestEvalErrors_E0901_E0902_E0909(t *testing.T) {
	w := newWorld(t, true)
	fe := w.withEval()
	b := checkBody(t, w, "", "Boom(): int {\n\treturn 1\n}\nconst P = Boom()\nconst F = Boom()\nconst R = Boom()\nG() {\n\tx: [P]u8 = [0; 1]\n}\n")
	id := func(n string) types.ItemID { return b.item(n) }
	boom := id("Boom")
	at := source.Span{File: b.srcs[0].ID, Start: 10, End: 11}
	fe.answers["m.P"] = types.EvalResult{Err: &types.EvalError{Kind: types.EvalPanic, Panic: types.PanicExplicit, Message: "no", Items: []types.ItemID{id("P"), boom}, Trace: []source.Span{{}, at}}}
	fe.answers["m.F"] = types.EvalResult{Err: &types.EvalError{Kind: types.EvalFuel, Steps: 4000001, Items: []types.ItemID{id("F")}, Trace: []source.Span{at}}}
	fe.answers["m.R"] = types.EvalResult{Err: &types.EvalError{Kind: types.EvalRefused, Refusal: types.RefuseIo, Items: []types.ItemID{id("R")}, Trace: []source.Span{at}}}
	if ds := b.bodyDiags(t); len(ds) != 0 {
		t.Fatalf("a use of a failing const reports:\n%s", dump(w, ds))
	}
	cases := []struct{ name, code, key, val string }{
		{"P", "E0901@4:7", "message", "no"},
		{"F", "E0902@5:7", "fuel", "4000000"},
		{"R", "E0901@6:7", "refused", "io"},
	}
	for _, tc := range cases {
		v, ds := w.c.EvalConst(b.m, id(tc.name))
		if v != types.NoConst || codes(w, ds) != tc.code {
			t.Fatalf("%s: %v %s", tc.name, v, dump(w, ds))
		}
		if got := fact(ds[0], tc.key); got != tc.val {
			t.Fatalf("%s: %s = %q, want %q", tc.name, tc.key, got, tc.val)
		}
	}
	if got := fact(mustOne(t, w, b, "P"), "trace"); got != "[? ?]" { // two object frames
		t.Fatalf("trace %s", got)
	}
	// The own evaluations are StageBodies.
	own := 0
	for _, r := range fe.requests() {
		if strings.HasPrefix(r, "0/1 ") {
			own++
		} else if r != "0/0 m.P" {
			t.Fatalf("request %s", r)
		}
	}
	if own != 4 {
		t.Fatalf("%d own evaluations at StageBodies, want 4", own)
	}
	// A success returns the value and reports nothing.
	fe.answers["m.P"] = types.EvalResult{Value: w.c.intConst(7)}
	if v, ds := w.c.EvalConst(b.m, id("P")); v == types.NoConst || len(ds) != 0 {
		t.Fatalf("success: %v %s", v, dump(w, ds))
	}
}

// TestE0901_ConstReadInBody is tests/conformance/codes/E0901_used.mk with
// an evaluator (the corpus stand-in has none, so the unit is excepted
// there): a const whose own evaluation panics, read as a value in a body.
// The reader draws nothing (no E0301 from the poisoned `Bad + 1`, no ICE),
// and the own evaluation draws the one E0901, at the const's name.
func TestE0901_ConstReadInBody(t *testing.T) {
	w := newWorld(t, true)
	fe := w.withEval()
	fe.fn = func(req *types.EvalRequest) (types.EvalResult, bool) {
		if w.items.Get(req.Item).Key.String() != "m.Bad" {
			return types.EvalResult{}, false
		}
		return types.EvalResult{Err: &types.EvalError{Kind: types.EvalPanic, Panic: types.PanicDivideByZero,
			Items: []types.ItemID{req.Item}, Trace: []source.Span{{}}}}, true
	}
	src := "Div(a: int, b: int): int {\n    return a / b\n}\n\nconst Bad: int = Div(1, 0)\n\nRead(): int {\n    return Bad + 1\n}\n"
	b := checkBody(t, w, "", src)
	if ds := b.bodyDiags(t); len(ds) != 0 {
		t.Fatalf("the reader of a failing const reports:\n%s", dump(w, ds))
	}
	v, ds := w.c.EvalConst(b.m, b.item("Bad"))
	if v != types.NoConst || codes(w, ds) != "E0901@5:7" {
		t.Fatalf("own evaluation: %v %s", v, dump(w, ds))
	}
	if got := fact(ds[0], "panic"); got != types.PanicDivideByZero.String() {
		t.Fatalf("panic = %q", got)
	}
	asked := false
	for _, r := range fe.requests() {
		if strings.HasSuffix(r, " m.Bad") {
			asked = true
		}
	}
	if !asked {
		t.Fatalf("no evaluation of m.Bad was requested: %v", fe.requests())
	}
}

func mustOne(t *testing.T, w *world, b *built, name string) *diag.Diagnostic {
	_, ds := w.c.EvalConst(b.m, b.item(name))
	if len(ds) != 1 {
		t.Fatalf("%s: %s", name, dump(w, ds))
	}
	return ds[0]
}

// TestE0909_OneAtLeastKeyDecl: one E0909 per cycle, from the least-key
// const's own evaluation, at its declaration. An untyped
// cycle is also reported by stage 5 (typeCycle), with the
// same code, span and message, which diag.Order deduplicates.
func TestE0909_OneAtLeastKeyDecl(t *testing.T) {
	w := newWorld(t, true)
	fe := w.withEval()
	b := checkBody(t, w, "E0909@2:7", "const B = A + 1\nconst A = B + 1\nF(): int {\n\treturn A\n}\n")
	cyc := &types.EvalError{Kind: types.EvalCycle, Items: []types.ItemID{b.item("A"), b.item("B")}}
	fe.fn = func(*types.EvalRequest) (types.EvalResult, bool) { return types.EvalResult{Err: cyc}, true }
	_, da := w.c.EvalConst(b.m, b.item("A"))
	_, db := w.c.EvalConst(b.m, b.item("B"))
	if codes(w, da) != "E0909@2:7" || len(db) != 0 {
		t.Fatalf("A: %s B: %s", dump(w, da), dump(w, db))
	}
	if got := fact(da[0], "cycle"); got != "[A B]" {
		t.Fatalf("cycle %s", got)
	}
	if s5 := b.diags()[0]; s5.Code != da[0].Code || s5.At != da[0].At || s5.Message != da[0].Message {
		t.Fatalf("stage 5 %q and the own evaluation %q differ", s5.Message, da[0].Message)
	}
}

func computeID(w *world) types.ItemID {
	id, _ := w.items.ByKey(types.KeyFromString("m.Compute"))
	return id
}

// TestIfConst: an if on a constant condition records its value;
// both branches are still checked; a run-time condition records none.
func TestIfConst(t *testing.T) {
	w := newWorld(t, true)
	b := checkBody(t, w, "E0301@8:13", "const Debug = false\nconst Level: int = 3\nF(n: int): int {\n\tif Debug || Level > 2 {\n\t\treturn 1\n\t}\n\tif !Debug {\n\t\tx: bool = 1\n\t}\n\tif n > 2 {\n\t\treturn 2\n\t}\n\treturn 0\n}\n")
	body := w.c.Check(b.m, b.item("F"))
	var got []string
	ast.Inspect(body.Node, func(n ast.Node) bool {
		if s, ok := n.(*ast.IfStmt); ok {
			c := s.Cond.(ast.Expr)
			v := types.NoConst
			if body.Consts != nil {
				v = body.Consts[body.slot(c.ID())]
			}
			if v == types.NoConst {
				got = append(got, "-")
			} else {
				got = append(got, w.tab.ConstString(v))
			}
		}
		return true
	})
	if s := strings.Join(got, " "); s != "true true -" {
		t.Fatalf("if consts %q", s)
	}
}

// TestLiteralConsts: char, byte and string literals have their values in
// Body.Consts, a str in the evaluator's {ptr, len} form.
func TestLiteralConsts(t *testing.T) {
	w := newWorld(t, true)
	b := checkBody(t, w, "", "F() {\n\ta := 'x'\n\tc := b'a'\n\ts := \"h\\x41\\u{e9}\\n\"\n\te := \"\"\n\tr := r\"a\\n\"\n\t_ = (a, c, s, e, r)\n}\n")
	body := w.c.Check(b.m, b.item("F"))
	var got []string
	ast.Inspect(body.Node, func(n ast.Node) bool {
		if l, ok := n.(*ast.BasicLit); ok && body.Consts != nil {
			if v := body.Consts[body.slot(l.ID())]; v != types.NoConst {
				got = append(got, w.tab.ConstString(v))
			}
		}
		return true
	})
	if s := strings.Join(got, " "); s != `'x' 97 "hAé\n" "" "a\\n"` {
		t.Fatalf("consts %s", s)
	}
	if bs, ok := w.c.strBytes(w.c.strConst([]byte("abc"))); !ok || string(bs) != "abc" {
		t.Fatalf("strBytes %q %v", bs, ok)
	}
}

// TestEarlyOnDemand: an early initializer that needs another early const's
// value checks (and Prepares) that one first; Early stays
// in key order; a layout-bound const is Deferred.
func TestEarlyOnDemand(t *testing.T) {
	w := newWorld(t, true)
	fe := w.withEval()
	var prepared []string
	w.cfg.Prepare = func(b *Body) { prepared = append(prepared, w.items.Get(b.Item).Key.String()) }
	// No request may reach a same-module const before it is Prepared (the
	// real evaluator has no MIR for it then): typeConsts' throwaway units
	// run before the sweep and must not ask.
	fe.fn = func(req *types.EvalRequest) (types.EvalResult, bool) {
		k := w.items.Get(req.Item).Key.String()
		if req.What == types.EvalConst && !strings.Contains(strings.Join(prepared, " "), k) {
			t.Errorf("request for %s before it was prepared (prepared: %v)", k, prepared)
		}
		return types.EvalResult{}, false
	}
	// `[P.IsSome()]` is a method call check does not fold: E0705, never a
	// silent Poison; P is still early and Deferred. `[A]u8`
	// in a declaration reaches the same-module Compute through A and B, so it
	// is E0908 at the type.
	b := checkBody(t, w, "E0705@14:6 E0908@7:6", "Compute(): int {\n\treturn 4\n}\nconst B = Compute()\nconst A = [0; B].Len()\nstruct S {\n\tx: [A]u8\n}\nstruct Pt {\n\tx: int\n}\nconst P: ?Pt = none\nstruct T {\n\tp: [P.IsSome()]u8\n}\n")
	if got := strings.Join(prepared, " "); got != "m.B m.A" {
		t.Fatalf("prepared %q, want B before A", got)
	}
	var early []string
	for _, eb := range b.m.Early() {
		early = append(early, b.key(eb.Item))
	}
	if got := strings.Join(early, " "); got != "A B P" {
		t.Fatalf("Early %q", got)
	}
	if d := b.m.Deferred(); len(d) != 1 || b.key(d[0]) != "P" {
		t.Fatalf("Deferred %v", d)
	}
}

// TestEarlySet_ConversionsAndRequires: the early set also holds a const
// judged by value in a conversion and a const named by a requires clause.
func TestEarlySet_ConversionsAndRequires(t *testing.T) {
	w := newWorld(t, true)
	b := checkBody(t, w, "E0303@6:9", "const Big = 300\n\nconst Lim = 2\n\nF(): u8 {\n\treturn u8(Big)\n}\n\nG[const N: int]()\n\trequires N > Lim\n{\n}\n")
	var keys []string
	for _, eb := range b.m.Early() {
		keys = append(keys, w.items.Get(eb.Item).Key.String())
	}
	if got := strings.Join(keys, " "); got != "m.Big m.Lim" {
		t.Fatalf("early %q", got)
	}
}

// TestEarlySet_ConversionOperators: a conversion operand of integer
// operators over consts is a constant, judged by its folded value, and its
// consts are early; an operand that names a local is judged by its type.
func TestEarlySet_ConversionOperators(t *testing.T) {
	w := newWorld(t, true)
	b := checkBody(t, w, "E0303@8:6 E0303@9:6", "const N = 4\n\nconst M = 300\n\nconst Unused = 9\n\nF(x: int): u8 {\n\t_ = u8(M - 1)\n\t_ = u8(x + N)\n\t_ = u8(-(N - 10))\n\treturn u8((N + 1) * 2)\n}\n")
	var keys []string
	for _, eb := range b.m.Early() {
		keys = append(keys, w.items.Get(eb.Item).Key.String())
	}
	if got := strings.Join(keys, " "); got != "m.M m.N" {
		t.Fatalf("early %q", got)
	}
}

// TestEarlyICEReportedOnce: an early const whose stage-5 check is an ICE
// (here the evaluator panics on a use request in A's initializer) gives one
// E0000 and is neither in Early nor a stage-6 unit, so it is not checked a
// second time.
func TestEarlyICEReportedOnce(t *testing.T) {
	w := newWorld(t, true)
	fe := w.withEval()
	fe.fn = func(req *types.EvalRequest) (types.EvalResult, bool) {
		if w.items.Get(req.Item).Key.String() == "m.B" {
			panic("evaluator failed")
		}
		return types.EvalResult{}, false
	}
	b := w.add("m", opt{}, `Compute(): int {
	return 3
}

const B = Compute()

const A = [0; B].Len()

F() {
	a: [A]u8 = [0; A]
	_ = a
}
`)
	n := 0
	for _, d := range b.diags() {
		if d.Code == diag.E0000 {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d E0000, want 1: %s", n, dump(w, b.diags()))
	}
	for _, id := range b.m.Units() {
		if k := w.items.Get(id).Key.String(); k == "m.A" || k == "m.B" {
			t.Fatalf("%s is a stage-6 unit", k)
		}
	}
	var keys []string
	for _, eb := range b.m.Early() {
		keys = append(keys, w.items.Get(eb.Item).Key.String())
	}
	if got := strings.Join(keys, " "); got != "m.B" {
		t.Fatalf("early %q", got)
	}
}

// TestConstConversionEvaluated: a same-module const whose initializer
// does not fold is judged by its value in a conversion: one EvalConst
// request at StageSignatures, E0303 when the value does not fit, E0908 at
// the use when the evaluator refuses the stage, never an unchecked accept.
func TestConstConversionEvaluated(t *testing.T) {
	const src = "Compute(): int {\n\treturn 300\n}\n\nconst Big: int = Compute()\n\nF(): u8 {\n\treturn u8(Big)\n}\n\nG(): u8 {\n\treturn u8(Big + 0)\n}\n"
	t.Run("value", func(t *testing.T) {
		w := newWorld(t, true)
		fe := w.withEval()
		fe.answers["m.Big"] = types.EvalResult{Value: w.c.scalarConst(types.Int, 8, 300)}
		checkBody(t, w, "E0303@12:9 E0303@8:9", src)
		rq := fe.requests()
		if len(rq) == 0 {
			t.Fatal("no request for Big")
		}
		for _, r := range rq {
			if r != "0/0 m.Big" {
				t.Fatalf("requests %v", rq)
			}
		}
	})
	t.Run("fits", func(t *testing.T) {
		w := newWorld(t, true)
		fe := w.withEval()
		fe.answers["m.Big"] = types.EvalResult{Value: w.c.scalarConst(types.Int, 8, 200)}
		checkBody(t, w, "", src)
	})
	t.Run("stage", func(t *testing.T) {
		w := newWorld(t, true)
		fe := w.withEval()
		fe.fn = func(req *types.EvalRequest) (types.EvalResult, bool) {
			return types.EvalResult{Err: &types.EvalError{Kind: types.EvalStage, Items: []types.ItemID{req.Item, computeID(w)},
				Trace: []source.Span{req.Site, {}}}}, true
		}
		checkBody(t, w, "E0908@12:12 E0908@8:12", src)
	})
}

// TestPoisonReadsOfFailedConst: an untyped const whose initializer
// failed to type has no type, and its E0301 is its own unit's. Every body
// that reads its value lists it in PoisonReads (B reads A, F reads B), with
// no diagnostic of its own; the const itself and a body reading another
// const list nothing.
func TestPoisonReadsOfFailedConst(t *testing.T) {
	w := newWorld(t, true)
	b := checkBody(t, w, "E0301@1:15", "const A = 1 + true\nconst B = A\nconst C = 2\nF(): int {\n\treturn B\n}\nG(): int {\n\treturn C\n}\n")
	want := map[string]string{"A": "", "B": "A", "C": "", "F": "B", "G": ""}
	for _, id := range b.m.Units() {
		k := b.key(id)
		body := w.c.Check(b.m, id)
		var got []string
		for _, c := range body.PoisonReads {
			got = append(got, b.key(c))
		}
		if g := strings.Join(got, " "); g != want[k] {
			t.Errorf("%s: PoisonReads %q, want %q", k, g, want[k])
		}
		if k != "A" && len(body.Diags) > 0 {
			t.Errorf("%s: diagnostics %s, want none", k, dump(w, body.Diags))
		}
	}
}

// TestE0902_GlobalFuelHint: a const out of fuel is told to raise its own
// fuel; a var global cannot state fuel (E0101), so it is told the route
// that compiles: the work in a const with that fuel, the global built from
// the const.
func TestE0902_GlobalFuelHint(t *testing.T) {
	w := newWorld(t, true)
	fe := w.withEval()
	b := checkBody(t, w, "", "Spin(): int {\n\treturn 1\n}\nconst C: int = Spin()\nvar V: int = Spin()\n")
	at := source.Span{File: b.srcs[0].ID, Start: 10, End: 11}
	for _, n := range []string{"C", "V"} {
		fe.answers["m."+n] = types.EvalResult{Err: &types.EvalError{Kind: types.EvalFuel, Steps: 4000001, Items: []types.ItemID{b.item(n)}, Trace: []source.Span{at}}}
	}
	for _, tc := range []struct{ name, alt string }{
		{"C", "raise the limit: fuel 8000000"},
		{"V", "a global cannot state fuel: do the work in a const with fuel 8000000, and initialize V from that const"},
	} {
		_, ds := w.c.EvalConst(b.m, b.item(tc.name))
		if len(ds) != 1 || ds[0].Code != diag.E0902 || len(ds[0].Alternatives) != 1 || ds[0].Alternatives[0] != tc.alt {
			t.Fatalf("%s: %s, want E0902 with the alternative %q", tc.name, dump(w, ds), tc.alt)
		}
	}
}
