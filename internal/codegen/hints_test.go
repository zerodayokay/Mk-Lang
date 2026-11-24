// Why: std.runtime.Likely, Unlikely, and Cold must emit exact LLVM prediction
// hints (llvm.expect.i1, !prof cold branch_weights, and cold call attributes)
// without marking Cold blocks alwaysinline, and compile cleanly across all
// hosted targets.
//
// What: TestRuntimeHintsIR.
//
// How: constructs MIR bodies calling std.runtime.Unlikely, Likely, and Cold
// across CondBr (`if`), Switch (`match`), forward/backward propagation, and
// outgoing calls, verifies emitted LLVM IR, and compiles via clang -O3 across
// x86_64-windows, x86_64-linux, aarch64-linux, and aarch64-macos.

package codegen

import (
	"bytes"
	"strings"
	"testing"

	"mk/internal/mir"
	"mk/internal/types"
)

func TestRuntimeHintsIR(t *testing.T) {
	e := newTestEnv()
	unlikelyFn := e.fnIn("std.runtime", "", "Unlikely", []types.TypeID{types.Bool}, types.Bool, true)
	likelyFn := e.fnIn("std.runtime", "", "Likely", []types.TypeID{types.Bool}, types.Bool, true)
	coldFn := e.fnIn("std.runtime", "", "Cold", nil, types.Void, true)

	hotHelper := e.body(e.fn("HotHelper", []types.TypeID{types.Int}, types.Int, 0))
	spH := hotHelper.at(1, 1)
	hotHelper.assign(place(0), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.AddWrap, A: mir.Copy(place(1)), B: mir.Imm(types.Int, 1), Type: types.Int}, spH)
	hotHelper.ret(spH)

	slowHelper := e.body(e.fn("SlowHelper", []types.TypeID{types.Int}, types.Int, 0))
	spS := slowHelper.at(2, 1)
	slowHelper.assign(place(0), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.AddWrap, A: mir.Copy(place(1)), B: mir.Imm(types.Int, 9), Type: types.Int}, spS)
	slowHelper.ret(spS)

	preHelper := e.body(e.fn("PreHelper", []types.TypeID{types.Int}, types.Int, 0))
	spP := preHelper.at(3, 1)
	preHelper.assign(place(0), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.AddWrap, A: mir.Copy(place(1)), B: mir.Imm(types.Int, 5), Type: types.Int}, spP)
	preHelper.ret(spP)

	// f2:
	//   c := Unlikely(cond)
	//   if c { PreHelper(x); Cold(); return SlowHelper(x) }
	//   if Likely(flag) { return HotHelper(x) }
	//   match tag { 1 => HotHelper(x); _ => { Cold(); SlowHelper(x) } }
	f2 := e.body(e.fn("HintedClean", []types.TypeID{types.Bool, types.Bool, types.Int}, types.Int, 0))
	sp := f2.at(20, 1)
	bEntry := mir.BlockID(0)
	_ = bEntry
	cUn := f2.local(types.Bool)
	cLi := f2.local(types.Bool)
	preOut := f2.local(types.Int)
	bCond2 := f2.newBlock()
	bColdPre2 := f2.newBlock()
	bColdCall2 := f2.newBlock()
	bColdAfter2 := f2.newBlock()
	bHot2 := f2.newBlock()
	bLikelyCall := f2.newBlock()
	bSw := f2.newBlock()
	bSw1 := f2.newBlock()
	bSwDef := f2.newBlock()
	bSwDefPost := f2.newBlock()

	f2.term(mir.Terminator{Kind: mir.TermCall, Callee: direct(unlikelyFn),
		Args: f2.b.AddArgs(mir.Arg{Operand: mir.Copy(place(1))}), Dest: place(cUn), Target: bCond2, Spawn: mir.NoLocal, Span: sp})

	f2.cur = bCond2
	f2.term(mir.Terminator{Kind: mir.TermSwitch, On: mir.Copy(place(cUn)),
		Cases: f2.b.AddCases(mir.SwitchCase{Value: 1, Target: bColdPre2}), Default: bHot2, Span: sp})

	f2.cur = bColdPre2
	f2.term(mir.Terminator{Kind: mir.TermCall, Callee: direct(preHelper.item),
		Args: f2.b.AddArgs(mir.Arg{Operand: mir.Copy(place(3))}), Dest: place(preOut), Target: bColdCall2, Spawn: mir.NoLocal, Span: sp})

	f2.cur = bColdCall2
	v0 := f2.local(types.Void)
	f2.term(mir.Terminator{Kind: mir.TermCall, Callee: direct(coldFn),
		Dest: place(v0), Target: bColdAfter2, Spawn: mir.NoLocal, Span: sp})

	f2.cur = bColdAfter2
	sOut := f2.local(types.Int)
	bColdRet := f2.newBlock()
	f2.term(mir.Terminator{Kind: mir.TermCall, Callee: direct(slowHelper.item),
		Args: f2.b.AddArgs(mir.Arg{Operand: mir.Copy(place(preOut))}), Dest: place(sOut), Target: bColdRet, Spawn: mir.NoLocal, Span: sp})
	f2.cur = bColdRet
	f2.assign(place(0), use(mir.Copy(place(sOut))), sp)
	f2.ret(sp)

	f2.cur = bHot2
	f2.term(mir.Terminator{Kind: mir.TermCall, Callee: direct(likelyFn),
		Args: f2.b.AddArgs(mir.Arg{Operand: mir.Copy(place(2))}), Dest: place(cLi), Target: bLikelyCall, Spawn: mir.NoLocal, Span: sp})

	f2.cur = bLikelyCall
	bHotDone := f2.newBlock()
	f2.term(mir.Terminator{Kind: mir.TermSwitch, On: mir.Copy(place(cLi)),
		Cases: f2.b.AddCases(mir.SwitchCase{Value: 1, Target: bHotDone}), Default: bSw, Span: sp})

	f2.cur = bHotDone
	bHotRet := f2.newBlock()
	hOut := f2.local(types.Int)
	f2.term(mir.Terminator{Kind: mir.TermCall, Callee: direct(hotHelper.item),
		Args: f2.b.AddArgs(mir.Arg{Operand: mir.Copy(place(3))}), Dest: place(hOut), Target: bHotRet, Spawn: mir.NoLocal, Span: sp})
	f2.cur = bHotRet
	f2.assign(place(0), use(mir.Copy(place(hOut))), sp)
	f2.ret(sp)

	f2.cur = bSw
	f2.term(mir.Terminator{Kind: mir.TermSwitch, On: mir.Copy(place(3)),
		Cases: f2.b.AddCases(mir.SwitchCase{Value: 1, Target: bSw1}), Default: bSwDef, Span: sp})

	f2.cur = bSw1
	bSw1Ret := f2.newBlock()
	h1 := f2.local(types.Int)
	f2.term(mir.Terminator{Kind: mir.TermCall, Callee: direct(hotHelper.item),
		Args: f2.b.AddArgs(mir.Arg{Operand: mir.Copy(place(3))}), Dest: place(h1), Target: bSw1Ret, Spawn: mir.NoLocal, Span: sp})
	f2.cur = bSw1Ret
	f2.assign(place(0), use(mir.Copy(place(h1))), sp)
	f2.ret(sp)

	f2.cur = bSwDef
	v1 := f2.local(types.Void)
	f2.term(mir.Terminator{Kind: mir.TermCall, Callee: direct(coldFn),
		Dest: place(v1), Target: bSwDefPost, Spawn: mir.NoLocal, Span: sp})

	f2.cur = bSwDefPost
	bSwDefRet := f2.newBlock()
	dOut := f2.local(types.Int)
	f2.term(mir.Terminator{Kind: mir.TermCall, Callee: direct(slowHelper.item),
		Args: f2.b.AddArgs(mir.Arg{Operand: mir.Copy(place(3))}), Dest: place(dOut), Target: bSwDefRet, Spawn: mir.NoLocal, Span: sp})
	f2.cur = bSwDefRet
	f2.assign(place(0), use(mir.Copy(place(dOut))), sp)
	f2.ret(sp)

	u := unitOf(e, []*mir.Body{hotHelper.b, slowHelper.b, preHelper.b, f2.b})
	for _, target := range []types.Target{types.X86_64Windows, types.X86_64Linux, types.Aarch64Linux, types.Aarch64Macos} {
		t.Run(target.Name, func(t *testing.T) {
			ll := emit(t, e, u, target, types.Release)
			for _, want := range []string{
				`@llvm.expect.i1(i1`,
				`, i1 false)`,
				`, i1 true)`,
				`!{!"branch_weights", i32 1, i32 1048575}`,
				`call noundef i64 @mk.main.SlowHelper(`,
				`call noundef i64 @mk.main.PreHelper(`,
				`cold`,
			} {
				if !strings.Contains(ll, want) {
					t.Errorf("%s: missing %q in IR:\n%s", target.Name, want, ll)
				}
			}
			// Outgoing calls inside Cold() blocks must NOT be marked alwaysinline even if sole-called.
			body := fnDefText(ll, "mk.main.HintedClean")
			for _, line := range strings.Split(body, "\n") {
				if strings.Contains(line, "@mk.main.PreHelper(") || strings.Contains(line, "@mk.main.SlowHelper(") {
					if strings.Contains(line, "alwaysinline") {
						t.Errorf("%s: cold call must not get alwaysinline: %s", target.Name, line)
					}
					if !strings.Contains(line, " cold") {
						t.Errorf("%s: missing cold attribute on cold path call: %s", target.Name, line)
					}
				}
			}
			clangC(t, target, t.TempDir(), "hints", ll, "-O3")
		})
	}
}

func TestRuntimeHintsColdNoBlockAndCrossPack(t *testing.T) {
	e := newTestEnv()
	coldFn := e.fnIn("std.runtime", "", "Cold", nil, types.Void, true)
	e.mods["other"] = e.items.AddModule(&types.Module{Path: "other"})
	crossID := e.itemIn("other", types.ItemKey("other", "CrossSlow"), "CrossSlow", types.KindFunc,
		&types.FuncDecl{Sig: types.Signature{
			Name:   e.in.Intern("CrossSlow"),
			Params: []types.Param{{Name: e.in.Intern("x"), Type: types.Int}},
			Result: types.Int,
		}})
	crossCallee := e.body(crossID)
	spC := crossCallee.at(1, 1)
	acc := crossCallee.local(types.Int)
	crossCallee.assign(place(acc), use(mir.Copy(place(1))), spC)
	for range 40 {
		next := crossCallee.local(types.Int)
		crossCallee.assign(place(next), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.AddWrap, A: mir.Copy(place(acc)), B: mir.Imm(types.Int, 7), Type: types.Int}, spC)
		acc = next
	}
	crossCallee.assign(place(0), use(mir.Copy(place(acc))), spC)
	crossCallee.ret(spC)

	// Main calls CrossSlow (in another pack) inside a Cold() block and exercises
	// exhaustive bool and integer TermSwitch terminators with Default == mir.NoBlock.
	fn := e.body(e.fn("MainHinted", []types.TypeID{types.Bool, types.Int}, types.Int, 0))
	sp := fn.at(10, 1)
	bBoolT := fn.newBlock()
	bBoolF := fn.newBlock()
	bSw := fn.newBlock()
	bSw0 := fn.newBlock()
	bSw1 := fn.newBlock()
	bSw1Post := fn.newBlock()

	fn.term(mir.Terminator{Kind: mir.TermSwitch, On: mir.Copy(place(1)),
		Cases:   fn.b.AddCases(mir.SwitchCase{Value: 1, Target: bBoolT}, mir.SwitchCase{Value: 0, Target: bBoolF}),
		Default: mir.NoBlock, Span: sp})

	// Keep only Value=1 in the bool switch so Default == mir.NoBlock on the false arm.
	_ = bBoolF
	fn.b.Blocks[0].Term.Cases.Len = 1

	fn.cur = bBoolT
	fn.term(mir.Terminator{Kind: mir.TermGoto, Target: bSw, Span: sp})

	fn.cur = bSw
	fn.term(mir.Terminator{Kind: mir.TermSwitch, On: mir.Copy(place(2)),
		Cases:   fn.b.AddCases(mir.SwitchCase{Value: 0, Target: bSw0}, mir.SwitchCase{Value: 1, Target: bSw1}),
		Default: mir.NoBlock, Span: sp})

	fn.cur = bSw0
	fn.assign(place(0), use(mir.Imm(types.Int, 42)), sp)
	fn.ret(sp)

	fn.cur = bSw1
	voidCold := fn.local(types.Void)
	fn.callTo(direct(coldFn), place(voidCold), mir.NoLocal, sp)
	fn.term(mir.Terminator{Kind: mir.TermGoto, Target: bSw1Post, Span: sp})

	fn.cur = bSw1Post
	out := fn.local(types.Int)
	fn.callTo(direct(crossCallee.item), place(out), mir.NoLocal, sp, mir.Copy(place(2)))
	fn.cur = fn.b.Blocks[bSw1Post].Term.Target
	fn.assign(place(0), use(mir.Copy(place(out))), sp)
	fn.ret(sp)

	u := unitOf(e, []*mir.Body{fn.b, crossCallee.b})
	u.Packs = [][]int{{0}, {1}}
	u.Funcs[1].Pack = 1

	ll := emit(t, e, u, types.X86_64Linux, types.Release)
	var buf1 bytes.Buffer
	if _, err := EmitPack(u, 1, e, Config{Target: types.X86_64Linux, Profile: types.Release}, &buf1); err != nil {
		t.Fatalf("EmitPack(pack 1): %v", err)
	}
	ll1 := buf1.String()
	// Cross-pack callee whose sole call site is inside a Cold block must NOT get alwaysinline in either pack.
	if strings.Contains(fnDefText(ll, "mk.other.CrossSlow"), "alwaysinline") || strings.Contains(fnDefText(ll1, "mk.other.CrossSlow"), "alwaysinline") {
		t.Errorf("cross-pack cold-called helper must not get alwaysinline:\npack0:\n%s\npack1:\n%s", ll, ll1)
	}
	// Exhaustive int switch with Default == mir.NoBlock and cold arm gets weight 0 for default, 1048575 for hot case, 1 for cold case.
	if !strings.Contains(ll, `!{!"branch_weights", i32 0, i32 1048575, i32 1}`) {
		t.Errorf("expected weight 0 for Default == mir.NoBlock in cold switch:\n%s", ll)
	}
	clangC(t, types.X86_64Linux, t.TempDir(), "hints_noblock", ll, "-O3")
}
