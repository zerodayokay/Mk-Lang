// Why: mutual and self-recursive `become` loops without a const fn dispatch
// table must also promote to `preserve_nonecc` so all 6+ hot loop parameters
// remain in GPRs on Windows x64 and Linux x64 with zero stack argument stores.
//
// What: TestPnoneDirectCyclesIR, TestPnoneDirectCyclesAsm, and
// TestPnoneDirectPackFacts.
//
// How: builds direct `become` units (`Ping <-> Pong`, self-loop `Countdown`,
// and non-cyclic `Start -> Leaf`) and checks emitted LLVM IR across 4 targets,
// `clang -O3 -S` disassembly on Windows/Linux x64, and cross-pack PackFacts.

package codegen

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mk/internal/mir"
	"mk/internal/types"
)

// directCycleUnit builds:
//   - Ping(n, a1..a5: u64): u64 -> n == 0 ? sum : become Pong(n-1, ...)
//   - Pong(n, a1..a5: u64): u64 -> n == 0 ? sum : become Ping(n-1, ...)
//   - Countdown(n, a1..a5: u64): u64 -> n == 0 ? sum : become Countdown(n-1, ...)
//   - Start(n, a1..a5: u64): u64 -> become Leaf(...) (non-cyclic: stays C ABI)
//   - Leaf(n, a1..a5: u64): u64 -> sum
func directCycleUnit(e *testEnv) *Unit {
	p6 := make([]types.TypeID, 6)
	for i := range p6 {
		p6[i] = types.U64
	}
	pingItem := e.fn("Ping", p6, types.U64, 0)
	pongItem := e.fn("Pong", p6, types.U64, 0)
	cntItem := e.fn("Countdown", p6, types.U64, 0)
	startItem := e.fn("Start", p6, types.U64, 0)
	leafItem := e.fn("Leaf", p6, types.U64, 0)

	emitHop := func(selfItem, nextItem types.ItemID, add1 uint64, line uint32) *fnb {
		f := e.body(selfItem)
		sp := f.at(line, 3)
		cond := f.local(types.Bool)
		f.assign(place(cond), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.Eq, A: mir.Copy(place(1)), B: mir.Imm(types.U64, 0), Type: types.Bool}, sp)
		doneB, loopB := f.newBlock(), f.newBlock()
		f.term(mir.Terminator{Kind: mir.TermSwitch, On: mir.Copy(place(cond)),
			Cases: f.b.AddCases(mir.SwitchCase{Value: 1, Target: doneB}), Default: loopB, Span: sp})
		f.cur = doneB
		s := f.local(types.U64)
		f.assign(place(s), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.AddWrap, A: mir.Copy(place(2)), B: mir.Copy(place(3)), Type: types.U64}, sp)
		s2 := f.local(types.U64)
		f.assign(place(s2), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.AddWrap, A: mir.Copy(place(4)), B: mir.Copy(place(5)), Type: types.U64}, sp)
		s3 := f.local(types.U64)
		f.assign(place(s3), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.AddWrap, A: mir.Copy(place(s)), B: mir.Copy(place(6)), Type: types.U64}, sp)
		f.assign(place(0), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.AddWrap, A: mir.Copy(place(s2)), B: mir.Copy(place(s3)), Type: types.U64}, sp)
		f.ret(sp)

		f.cur = loopB
		n1 := f.local(types.U64)
		f.assign(place(n1), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.SubWrap, A: mir.Copy(place(1)), B: mir.Imm(types.U64, 1), Type: types.U64}, sp)
		a1 := f.local(types.U64)
		f.assign(place(a1), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.AddWrap, A: mir.Copy(place(2)), B: mir.Imm(types.U64, add1), Type: types.U64}, sp)
		f.term(mir.Terminator{Kind: mir.TermTailCall, Callee: direct(nextItem),
			Args: f.b.AddArgs(
				mir.Arg{Operand: mir.Copy(place(n1))},
				mir.Arg{Operand: mir.Copy(place(3))},
				mir.Arg{Operand: mir.Copy(place(4))},
				mir.Arg{Operand: mir.Copy(place(5))},
				mir.Arg{Operand: mir.Copy(place(6))},
				mir.Arg{Operand: mir.Copy(place(a1))},
			), Dest: place(0), Spawn: mir.NoLocal, Span: sp})
		return f
	}

	ping := emitHop(pingItem, pongItem, 3, 10)
	pong := emitHop(pongItem, pingItem, 5, 20)
	cnt := emitHop(cntItem, cntItem, 7, 30)

	start := e.body(startItem)
	spSt := start.at(40, 3)
	start.term(mir.Terminator{Kind: mir.TermTailCall, Callee: direct(leafItem),
		Args: start.b.AddArgs(
			mir.Arg{Operand: mir.Copy(place(1))}, mir.Arg{Operand: mir.Copy(place(2))}, mir.Arg{Operand: mir.Copy(place(3))},
			mir.Arg{Operand: mir.Copy(place(4))}, mir.Arg{Operand: mir.Copy(place(5))}, mir.Arg{Operand: mir.Copy(place(6))},
		), Dest: place(0), Spawn: mir.NoLocal, Span: spSt})

	leaf := e.body(leafItem)
	spL := leaf.at(50, 3)
	leaf.assign(place(0), use(mir.Copy(place(6))), spL)
	leaf.ret(spL)

	return unitOf(e, []*mir.Body{ping.b, pong.b, cnt.b, start.b, leaf.b})
}

func TestPnoneDirectCyclesIR(t *testing.T) {
	targets := []types.Target{types.X86_64Windows, types.X86_64Linux, types.Aarch64Linux, types.Aarch64Macos}
	for _, target := range targets {
		t.Run(target.Name, func(t *testing.T) {
			e := newTestEnv()
			u := directCycleUnit(e)
			ll := emit(t, e, u, target, types.Release)
			for _, want := range []string{
				`preserve_nonecc noundef i64 @mk.main.Ping$pnone(`,
				`preserve_nonecc noundef i64 @mk.main.Pong$pnone(`,
				`preserve_nonecc noundef i64 @mk.main.Countdown$pnone(`,
				`musttail call preserve_nonecc noundef i64 @mk.main.Pong$pnone(`,
				`musttail call preserve_nonecc noundef i64 @mk.main.Ping$pnone(`,
				`musttail call preserve_nonecc noundef i64 @mk.main.Countdown$pnone(`,
			} {
				if !strings.Contains(ll, want) {
					t.Errorf("%s: missing %q:\n%s", target.Name, want, ll)
				}
			}
			for _, no := range []string{"Start$pnone", "Leaf$pnone"} {
				if strings.Contains(ll, no) {
					t.Errorf("%s: non-cyclic function got %s:\n%s", target.Name, no, ll)
				}
			}
			clangC(t, target, t.TempDir(), "directpnone", ll, "-O3")
		})
	}
}

func TestPnoneDirectCyclesAsm(t *testing.T) {
	dir := needLLVM(t)
	e := newTestEnv()
	u := directCycleUnit(e)
	targets := []struct {
		target   types.Target
		triple   string
		wantJump string
		spillPat string
	}{
		{types.X86_64Windows, "x86_64-pc-windows-msvc", "\tjmp\t", "(%rsp)"},
		{types.X86_64Linux, "x86_64-unknown-linux-gnu", "\tjmp\t", "(%rsp)"},
	}
	for _, tc := range targets {
		t.Run(tc.target.Name, func(t *testing.T) {
			ll := emit(t, e, u, tc.target, types.Release)
			tmp := t.TempDir()
			llPath := filepath.Join(tmp, "pnone_direct.ll")
			sPath := filepath.Join(tmp, "pnone_direct.s")
			if err := os.WriteFile(llPath, []byte(ll), 0o644); err != nil {
				t.Fatal(err)
			}
			runTool(t, tool(dir, "clang"), "--target="+tc.triple, "-Wno-override-module",
				"-fverify-intermediate-code", "-Werror", "-O3", "-S", llPath, "-o", sPath)
			asmBytes, err := os.ReadFile(sPath)
			if err != nil {
				t.Fatal(err)
			}
			asm := string(asmBytes)
			for _, label := range []string{"mk.main.Ping$pnone:", "mk.main.Pong$pnone:"} {
				start := strings.Index(asm, label)
				if start < 0 {
					t.Fatalf("%s: assembly lacks %s\n%s", tc.target.Name, label, asm)
				}
				rest := asm[start:]
				end := strings.Index(rest, "-- End function")
				if end < 0 {
					end = len(rest)
				}
				body := rest[:end]
				if !strings.Contains(body, tc.wantJump) {
					t.Errorf("%s: %s lacks direct tail jump %q:\n%s", tc.target.Name, label, tc.wantJump, body)
				}
				if strings.Contains(body, tc.spillPat) {
					t.Errorf("%s: %s spilled to stack (%q):\n%s", tc.target.Name, label, tc.spillPat, body)
				}
			}
		})
	}
}

func TestPnoneDirectPackFacts(t *testing.T) {
	build := func(breakCycle bool) (*testEnv, *Unit) {
		e := newTestEnv()
		u := directCycleUnit(e)
		// Pack 0: Ping; Pack 1: Pong, Countdown, Start, Leaf.
		u.Packs = [][]int{{0}, {1, 2, 3, 4}}
		u.Funcs[0].Pack = 0
		for _, i := range []int{1, 2, 3, 4} {
			u.Funcs[i].Pack = 1
		}
		if breakCycle {
			// Disqualify Pong via section attribute so the Ping<->Pong cycle breaks across packs.
			e.items.Get(u.Funcs[1].Item).Decl.(*types.FuncDecl).Section = ".text.cold"
		}
		return e, u
	}
	cfg := Config{Target: types.X86_64Windows, Profile: types.Release}
	e0, u0 := build(false)
	e1, u1 := build(true)
	h0, ok0 := pfHash(u0, 0, e0, cfg)
	h1, ok1 := pfHash(u1, 0, e1, cfg)
	if !ok0 || !ok1 {
		t.Fatal("PackFacts refused release config")
	}
	if h0 == h1 {
		t.Error("breaking cross-pack Pong->Ping cycle left pack 0 PackFacts hash unchanged")
	}
	var ll0, ll1 bytes.Buffer
	if _, err := EmitPack(u0, 0, e0, cfg, &ll0); err != nil {
		t.Fatal(err)
	}
	if _, err := EmitPack(u0, 1, e0, cfg, &ll1); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ll0.String(), `declare dso_local preserve_nonecc noundef i64 @mk.main.Pong$pnone(`) {
		t.Errorf("pack 0 lacks preserve_nonecc declare of Pong$pnone:\n%s", ll0.String())
	}
	if !strings.Contains(ll1.String(), `declare dso_local preserve_nonecc noundef i64 @mk.main.Ping$pnone(`) {
		t.Errorf("pack 1 lacks preserve_nonecc declare of Ping$pnone:\n%s", ll1.String())
	}
	tmp := t.TempDir()
	clangC(t, types.X86_64Windows, tmp, "dp0", ll0.String(), "-O2")
	clangC(t, types.X86_64Windows, tmp, "dp1", ll1.String(), "-O2")
}

func TestPnoneDirectCycleSysVGPCap(t *testing.T) {
	run := func(nParams int) string {
		e := newTestEnv()
		params := make([]types.TypeID, nParams)
		for i := range params {
			params[i] = types.U64
		}
		fnItem := e.fn("Loop", params, types.U64, 0)
		f := e.body(fnItem)
		sp := f.at(10, 3)
		cond := f.local(types.Bool)
		f.assign(place(cond), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.Eq, A: mir.Copy(place(1)), B: mir.Imm(types.U64, 0), Type: types.Bool}, sp)
		doneB, loopB := f.newBlock(), f.newBlock()
		f.term(mir.Terminator{Kind: mir.TermSwitch, On: mir.Copy(place(cond)),
			Cases: f.b.AddCases(mir.SwitchCase{Value: 1, Target: doneB}), Default: loopB, Span: sp})
		f.cur = doneB
		f.assign(place(0), use(mir.Copy(place(2))), sp)
		f.ret(sp)

		f.cur = loopB
		n1 := f.local(types.U64)
		f.assign(place(n1), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.SubWrap, A: mir.Copy(place(1)), B: mir.Imm(types.U64, 1), Type: types.U64}, sp)
		args := make([]mir.Arg, nParams)
		args[0] = mir.Arg{Operand: mir.Copy(place(n1))}
		for i := 1; i < nParams; i++ {
			args[i] = mir.Arg{Operand: mir.Copy(place(mir.LocalID(i + 1)))}
		}
		f.term(mir.Terminator{Kind: mir.TermTailCall, Callee: direct(fnItem),
			Args: f.b.AddArgs(args...), Dest: place(0), Spawn: mir.NoLocal, Span: sp})
		u := unitOf(e, []*mir.Body{f.b})
		return emit(t, e, u, types.X86_64Linux, types.Release)
	}
	if ll10 := run(10); !strings.Contains(ll10, `preserve_nonecc noundef i64 @mk.main.Loop$pnone(`) {
		t.Errorf("10-GP-arg SysV direct cycle did not promote to preserve_nonecc:\n%s", ll10)
	}
	if ll11 := run(11); strings.Contains(ll11, `$pnone`) {
		t.Errorf("11-GP-arg SysV direct cycle exceeded 10-GP-arg cap and promoted to preserve_nonecc:\n%s", ll11)
	}
}
