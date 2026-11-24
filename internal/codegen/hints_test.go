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

	// f:
	//   c := Unlikely(cond)
	//   if c { PreHelper(x); Cold(); return SlowHelper(x) }
	//   if Likely(flag) { return HotHelper(x) }
	//   match tag { 1 => HotHelper(x); _ => { Cold(); SlowHelper(x) } }
	f := e.body(e.fn("Hinted", []types.TypeID{types.Bool, types.Bool, types.Int}, types.Int, 0))
	spF := f.at(10, 1)
	uc := f.local(types.Bool)
	bbColdPre := f.newBlock()
	bbCold := f.newBlock()
	bbColdPost := f.newBlock()
	bbHotCheck := f.newBlock()
	bbHotFast := f.newBlock()
	bbMatch := f.newBlock()
	bbArm1 := f.newBlock()
	bbArmDef := f.newBlock()
	bbArmDefPost := f.newBlock()

	f.callTo(direct(unlikelyFn), place(uc), mir.NoLocal, spF, mir.Copy(place(1)))
	f.term(mir.Terminator{Kind: mir.TermSwitch, On: mir.Copy(place(uc)),
		Cases: f.b.AddCases(mir.SwitchCase{Value: 1, Target: bbColdPre}), Default: bbHotCheck, Span: spF})

	// Cold path: PreHelper (backward) -> Cold() (seed) -> SlowHelper (forward).
	f.cur = bbColdPre
	pv := f.local(types.Int)
	f.callTo(direct(preHelper.item), place(pv), mir.NoLocal, spF, mir.Copy(place(3)))
	f.cur = f.b.Blocks[bbColdPre].Term.Target
	f.cur = bbColdPre
	// Fix target manually via block chaining below.
	_ = pv

	// Rebuild F clean linear blocks with explicit transitions.
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

	_ = bbCold
	_ = bbColdPost
	_ = bbHotCheck
	_ = bbHotFast
	_ = bbMatch
	_ = bbArm1
	_ = bbArmDef
	_ = bbArmDefPost

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
					if !strings.Contains(ll, " cold") {
						t.Errorf("%s: missing cold attribute on cold path call: %s", target.Name, line)
					}
				}
			}
			clangC(t, target, t.TempDir(), "hints", ll, "-O3")
		})
	}
}
