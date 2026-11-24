// Why: std.runtime.Likely, Unlikely, and Cold must work cleanly as no-op
// hints in compile-time CTFE evaluation without hitting an unknown-intrinsic ICE.
//
// What: TestRuntimeHintsEval.
//
// How: evaluates MIR calls to std.runtime.Likely, Unlikely, and Cold via Machine.

package eval

import (
	"testing"

	"mk/internal/types"
)

func TestRuntimeHintsEval(t *testing.T) {
	w := newWorld(t)
	likely := w.fn("std.runtime", "Likely", types.Bool)
	w.items.Get(likely).Decl.(*types.FuncDecl).Sig.Params = []types.Param{{Name: w.in.Intern("cond"), Type: types.Bool}}
	w.items.Get(likely).Decl.(*types.FuncDecl).Bodiless = true
	w.items.Get(likely).Decl.(*types.FuncDecl).Intrinsic = true

	unlikely := w.fn("std.runtime", "Unlikely", types.Bool)
	w.items.Get(unlikely).Decl.(*types.FuncDecl).Sig.Params = []types.Param{{Name: w.in.Intern("cond"), Type: types.Bool}}
	w.items.Get(unlikely).Decl.(*types.FuncDecl).Bodiless = true
	w.items.Get(unlikely).Decl.(*types.FuncDecl).Intrinsic = true

	cold := w.fn("std.runtime", "Cold", types.Void)
	w.items.Get(cold).Decl.(*types.FuncDecl).Bodiless = true
	w.items.Get(cold).Decl.(*types.FuncDecl).Intrinsic = true

	f := w.fn("lib", "UseHints", types.Int)
	x := w.body(f, types.Int)
	tl := x.local(types.Bool)
	tu := x.local(types.Bool)
	tv := x.local(types.Void)
	b1, b2, b3, bOk, bBad := x.block(), x.block(), x.block(), x.block(), x.block()
	x.call(0, likely, lp(tl), b1, imm(types.Bool, 1))
	x.call(b1, unlikely, lp(tu), b2, imm(types.Bool, 0))
	x.call(b2, cold, lp(tv), b3)
	x.branch(b3, cp(tl), bOk, bBad)
	x.assign(bOk, lp(0), use(imm(types.Int, 42)))
	x.ret(bOk)
	x.assign(bBad, lp(0), use(imm(types.Int, 99)))
	x.ret(bBad)
	x.done()

	r := w.call(w.evaluator(0), f, 0)
	if r.Err != nil {
		t.Fatalf("UseHints failed: %+v", r.Err)
	}
	if w.intOf(r.Value) != 42 {
		t.Fatalf("got %d, want 42", w.intOf(r.Value))
	}
}
