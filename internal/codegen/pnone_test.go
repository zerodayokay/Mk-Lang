// Why: preserve_nonecc tail-dispatch table cloning rewrites calling
// conventions and emits companion tables across functions and packs, so IR
// generation, rejection of escaping/invalid cases, cross-target assembly
// lowering and PackFacts cache invalidation must all be tested directly.
//
// What: TestPnoneTailTableIR, TestPnoneTailTableSliceAndSret,
// TestPnoneTailTableRejections, TestPnoneCrossTargetAsm and
// TestPnonePackFactsCrossPack.
//
// How: hand-built MIR units exercise 8-parameter dispatch tables, direct
// become closure, re-entrant and outside C ABI calls, slices, sret, shared
// tables, every disqualification rule, clang -O3 -S across Windows x64,
// Linux x64 and AArch64, and cross-pack PackFacts hashing.

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

// makeFnTableConst interns a [N]fn(...) const item holding items.
func makeFnTableConst(e *testEnv, name string, exported bool, fnT types.TypeID, elems ...types.ItemID) (types.ItemID, types.TypeID) {
	n := uint64(len(elems))
	arrT := e.tt.Array(types.ConstArg{Kind: types.CAValue, Value: e.intConst(types.Int, n), Type: types.Int}, fnT)
	relocs := make([]types.Reloc, len(elems))
	for i, it := range elems {
		relocs[i] = types.Reloc{Offset: uint32(i * 8), Kind: types.RelocFn, Fn: types.FnRef{Item: it}}
	}
	cv := e.consts.Intern(types.ConstValue{Type: arrT, Align: 8, Bytes: make([]byte, len(elems)*8), Relocs: relocs})
	id := e.item(name, types.KindConst, &types.ConstDecl{Type: arrT, Value: cv, HasValue: true})
	e.items.Get(id).Exported = exported
	return id, arrT
}

// makeFnSliceConst interns a []fn(...) const item holding items.
func makeFnSliceConst(e *testEnv, name string, fnT types.TypeID, elems ...types.ItemID) (types.ItemID, types.TypeID) {
	n := uint64(len(elems))
	arrT := e.tt.Array(types.ConstArg{Kind: types.CAValue, Value: e.intConst(types.Int, n), Type: types.Int}, fnT)
	relocs := make([]types.Reloc, len(elems))
	for i, it := range elems {
		relocs[i] = types.Reloc{Offset: uint32(i * 8), Kind: types.RelocFn, Fn: types.FnRef{Item: it}}
	}
	dataCV := e.consts.Intern(types.ConstValue{Type: arrT, Align: 8, Bytes: make([]byte, len(elems)*8), Relocs: relocs})
	sliceT := e.tt.Slice(fnT)
	hdrBytes := make([]byte, 16)
	types.StoreUint(hdrBytes[8:], 8, n)
	hdrCV := e.consts.Intern(types.ConstValue{Type: sliceT, Align: 8, Bytes: hdrBytes,
		Relocs: []types.Reloc{{Offset: 0, Kind: types.RelocData, Data: dataCV}}})
	id := e.item(name, types.KindConst, &types.ConstDecl{Type: sliceT, Value: hdrCV, HasValue: true})
	return id, sliceT
}

// pnone8Unit builds an 8-parameter threaded interpreter unit:
//   - Ops = [Op0, Op1]
//   - Op0 checks p0 == 0 -> Slow(p1) + p7; else become Ops[p0 & 1](p0 - 1, p2, p3, p4, p5, p6, p1, p7 + p1)
//   - Op1 does direct become Helper(p0 - 1, p1, p2, p3, p4, p5, p6, p7 + p2)
//   - Helper does become Ops[p0 & 1](p0, p1, p2, p3, p4, p5, p6, p7)
//   - Slow(x: int): int returns x + 1
//   - Enter(idx, a0..a7) calls Ops[idx](a0..a7) via regular TermCall
//   - EnterDirect(a0..a7) calls Op0(a0..a7) via regular TermCall
func pnone8Unit(e *testEnv) *Unit {
	p8 := make([]types.TypeID, 8)
	fnParams := make([]types.FnParam, 8)
	for i := range p8 {
		p8[i] = types.Int
		fnParams[i] = types.FnParam{Type: types.Int}
	}
	fnT := e.tt.Fn(types.FnType{Params: fnParams, Result: types.Int})
	op0Item := e.fn("Op0", p8, types.Int, 0)
	op1Item := e.fn("Op1", p8, types.Int, 0)
	helpItem := e.fn("Helper", p8, types.Int, 0)
	slowItem := e.fn("Slow", []types.TypeID{types.Int}, types.Int, 0)
	tabItem, arrT := makeFnTableConst(e, "Ops", false, fnT, op0Item, op1Item)

	addTailDispatch := func(f *fnb, tab types.ItemID, tabT types.TypeID, idxL mir.LocalID, args []mir.Arg, sp mir.SpanID) {
		tl := f.local(tabT)
		f.assign(place(tl), mir.Rvalue{Kind: mir.RvConstRef, Item: tab, Type: tabT}, sp)
		// Emit an RvLen like lower's boundsCheck does.
		ln := f.local(types.Int)
		f.assign(place(ln), mir.Rvalue{Kind: mir.RvLen, Place: place(tl), Type: types.Int}, sp)
		fp := f.local(fnT)
		ip := f.b.Place(tl, mir.Projection{Kind: mir.ProjIndex, A: uint32(idxL), Type: fnT})
		f.assign(place(fp), use(mir.Copy(ip)), sp)
		f.term(mir.Terminator{Kind: mir.TermTailCall, Callee: mir.Callee{Kind: mir.CallValue, Value: mir.Copy(place(fp))},
			Args: f.b.AddArgs(args...), Dest: place(0), Spawn: mir.NoLocal, Span: sp})
	}

	// Op0
	op0 := e.body(op0Item)
	sp0 := op0.at(10, 3)
	cond := op0.local(types.Bool)
	op0.assign(place(cond), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.Eq, A: mir.Copy(place(1)), B: mir.Imm(types.Int, 0), Type: types.Bool}, sp0)
	doneB, nextB := op0.newBlock(), op0.newBlock()
	op0.term(mir.Terminator{Kind: mir.TermSwitch, On: mir.Copy(place(cond)),
		Cases: op0.b.AddCases(mir.SwitchCase{Value: 1, Target: doneB}), Default: nextB, Span: sp0})
	op0.cur = doneB
	sv := op0.local(types.Int)
	op0.callTo(direct(slowItem), place(sv), mir.NoLocal, sp0, mir.Copy(place(2)))
	op0.assign(place(0), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.AddWrap, A: mir.Copy(place(sv)), B: mir.Copy(place(8)), Type: types.Int}, sp0)
	op0.ret(sp0)
	op0.cur = nextB
	idx0 := op0.local(types.Int)
	op0.assign(place(idx0), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.BitAnd, A: mir.Copy(place(1)), B: mir.Imm(types.Int, 1), Type: types.Int}, sp0)
	n0 := op0.local(types.Int)
	op0.assign(place(n0), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.SubWrap, A: mir.Copy(place(1)), B: mir.Imm(types.Int, 1), Type: types.Int}, sp0)
	acc0 := op0.local(types.Int)
	op0.assign(place(acc0), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.AddWrap, A: mir.Copy(place(8)), B: mir.Copy(place(2)), Type: types.Int}, sp0)
	addTailDispatch(op0, tabItem, arrT, idx0, []mir.Arg{
		{Operand: mir.Copy(place(n0))}, {Operand: mir.Copy(place(3))}, {Operand: mir.Copy(place(4))}, {Operand: mir.Copy(place(5))},
		{Operand: mir.Copy(place(6))}, {Operand: mir.Copy(place(7))}, {Operand: mir.Copy(place(2))}, {Operand: mir.Copy(place(acc0))},
	}, sp0)

	// Op1: direct become Helper
	op1 := e.body(op1Item)
	sp1 := op1.at(20, 3)
	n1 := op1.local(types.Int)
	op1.assign(place(n1), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.SubWrap, A: mir.Copy(place(1)), B: mir.Imm(types.Int, 1), Type: types.Int}, sp1)
	acc1 := op1.local(types.Int)
	op1.assign(place(acc1), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.AddWrap, A: mir.Copy(place(8)), B: mir.Copy(place(3)), Type: types.Int}, sp1)
	op1.term(mir.Terminator{Kind: mir.TermTailCall, Callee: direct(helpItem),
		Args: op1.b.AddArgs(
			mir.Arg{Operand: mir.Copy(place(n1))}, mir.Arg{Operand: mir.Copy(place(2))}, mir.Arg{Operand: mir.Copy(place(3))}, mir.Arg{Operand: mir.Copy(place(4))},
			mir.Arg{Operand: mir.Copy(place(5))}, mir.Arg{Operand: mir.Copy(place(6))}, mir.Arg{Operand: mir.Copy(place(7))}, mir.Arg{Operand: mir.Copy(place(acc1))},
		), Dest: place(0), Spawn: mir.NoLocal, Span: sp1})

	// Helper: indirect become Ops[p0 & 1]
	help := e.body(helpItem)
	spH := help.at(30, 3)
	idxH := help.local(types.Int)
	help.assign(place(idxH), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.BitAnd, A: mir.Copy(place(1)), B: mir.Imm(types.Int, 1), Type: types.Int}, spH)
	addTailDispatch(help, tabItem, arrT, idxH, []mir.Arg{
		{Operand: mir.Copy(place(1))}, {Operand: mir.Copy(place(2))}, {Operand: mir.Copy(place(3))}, {Operand: mir.Copy(place(4))},
		{Operand: mir.Copy(place(5))}, {Operand: mir.Copy(place(6))}, {Operand: mir.Copy(place(7))}, {Operand: mir.Copy(place(8))},
	}, spH)

	// Slow
	slow := e.body(slowItem)
	spS := slow.at(40, 3)
	slow.assign(place(0), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.AddWrap, A: mir.Copy(place(1)), B: mir.Imm(types.Int, 1), Type: types.Int}, spS)
	slow.ret(spS)

	// Enter: regular call through Ops[idx]
	enterParams := append([]types.TypeID{types.Int}, p8...)
	enter := e.body(e.fn("Enter", enterParams, types.Int, 0))
	spE := enter.at(50, 3)
	tlE := enter.local(arrT)
	enter.assign(place(tlE), mir.Rvalue{Kind: mir.RvConstRef, Item: tabItem, Type: arrT}, spE)
	ipE := enter.b.Place(tlE, mir.Projection{Kind: mir.ProjIndex, A: 1, Type: fnT})
	enter.callTo(mir.Callee{Kind: mir.CallValue, Value: mir.Copy(ipE)}, place(0), mir.NoLocal, spE,
		mir.Copy(place(2)), mir.Copy(place(3)), mir.Copy(place(4)), mir.Copy(place(5)),
		mir.Copy(place(6)), mir.Copy(place(7)), mir.Copy(place(8)), mir.Copy(place(9)))
	enter.ret(spE)

	// EnterDirect: regular call directly to Op0
	enterD := e.body(e.fn("EnterDirect", p8, types.Int, 0))
	spD := enterD.at(60, 3)
	enterD.callTo(direct(op0Item), place(0), mir.NoLocal, spD,
		mir.Copy(place(1)), mir.Copy(place(2)), mir.Copy(place(3)), mir.Copy(place(4)),
		mir.Copy(place(5)), mir.Copy(place(6)), mir.Copy(place(7)), mir.Copy(place(8)))
	enterD.ret(spD)

	u := unitOf(e, []*mir.Body{op0.b, op1.b, help.b, slow.b, enter.b, enterD.b})
	u.Funcs[0].AddrTaken = true
	u.Funcs[1].AddrTaken = true
	return u
}

// fnDefText returns the define block of symbol (unlike fnText, never matching
// an earlier call site to symbol inside another function).
func fnDefText(ll, symbol string) string {
	for _, pat := range []string{"@" + symbol + "(", `@"` + symbol + `"(`} {
		off := 0
		for {
			i := strings.Index(ll[off:], pat)
			if i < 0 {
				break
			}
			pos := off + i
			lineStart := strings.LastIndexByte(ll[:pos], '\n') + 1
			if strings.HasPrefix(ll[lineStart:], "define ") {
				end := strings.Index(ll[pos:], "\n}\n")
				if end < 0 {
					return ll[lineStart:]
				}
				return ll[lineStart : pos+end+2]
			}
			off = pos + len(pat)
		}
	}
	return ""
}

func TestPnoneTailTableIR(t *testing.T) {
	e := newTestEnv()
	u := pnone8Unit(e)
	for _, target := range []types.Target{types.X86_64Windows, types.X86_64Linux, types.Aarch64Linux, types.Aarch64Macos} {
		t.Run(target.Name, func(t *testing.T) {
			ll := emit(t, e, u, target, types.Release)
			for _, want := range []string{
				`@mk.main.Ops$pnone = private unnamed_addr constant [2 x ptr] [ptr @mk.main.Op0$pnone, ptr @mk.main.Op1$pnone], align 8`,
				`preserve_nonecc noundef i64 @mk.main.Op0$pnone(`,
				`preserve_nonecc noundef i64 @mk.main.Op1$pnone(`,
				`preserve_nonecc noundef i64 @mk.main.Helper$pnone(`,
				`mk.fr.mk.main.Op0$pnone`,
				`mk.fr.mk.main.Op1$pnone`,
				`mk.fr.mk.main.Helper$pnone`,
			} {
				if !strings.Contains(ll, want) {
					t.Errorf("%s: IR lacks %q", target.Name, want)
				}
			}
			// C ABI wrappers call their $pnone body via call preserve_nonecc (not musttail).
			for _, name := range []string{"mk.main.Op0", "mk.main.Op1", "mk.main.Helper"} {
				w := fnDefText(ll, name)
				if strings.Contains(w, "define dso_local preserve_nonecc") || strings.Contains(w, "define hidden preserve_nonecc") {
					t.Errorf("%s: wrapper %s should keep C ABI:\n%s", target.Name, name, w)
				}
				if !strings.Contains(w, "call preserve_nonecc noundef i64 @"+name+"$pnone(") {
					t.Errorf("%s: wrapper %s does not call %s$pnone:\n%s", target.Name, name, name, w)
				}
				if strings.Contains(w, "musttail") {
					t.Errorf("%s: wrapper %s should not use musttail:\n%s", target.Name, name, w)
				}
			}
			// Op0$pnone does indirect musttail preserve_nonecc AND calls normal C ABI Slow.
			op0Body := fnDefText(ll, "mk.main.Op0$pnone")
			if !strings.Contains(op0Body, "musttail call preserve_nonecc noundef i64 %") {
				t.Errorf("%s: Op0$pnone lacks indirect musttail preserve_nonecc:\n%s", target.Name, op0Body)
			}
			if !strings.Contains(op0Body, "call noundef i64 @mk.main.Slow(") {
				t.Errorf("%s: Op0$pnone lacks C-ABI Slow call:\n%s", target.Name, op0Body)
			}
			// Op1$pnone does direct musttail preserve_nonecc to Helper$pnone.
			op1Body := fnDefText(ll, "mk.main.Op1$pnone")
			if !strings.Contains(op1Body, "musttail call preserve_nonecc noundef i64 @mk.main.Helper$pnone(") {
				t.Errorf("%s: Op1$pnone lacks direct musttail to Helper$pnone:\n%s", target.Name, op1Body)
			}
			// Enter uses C ABI table, not $pnone.
			enterBody := fnDefText(ll, "mk.main.Enter")
			if strings.Contains(enterBody, "$pnone") || strings.Contains(enterBody, "preserve_nonecc") {
				t.Errorf("%s: Enter should call through C ABI table:\n%s", target.Name, enterBody)
			}
			// Slow is not cloned.
			if strings.Contains(ll, "mk.main.Slow$pnone") {
				t.Errorf("%s: Slow should not be cloned to $pnone", target.Name)
			}
			clangC(t, target, t.TempDir(), "pnone8", ll, "-O2")
		})
	}
	// Disabled in Dev, Debug, and Size profiles.
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{"dev", Config{Target: types.X86_64Windows, Profile: types.Dev}},
		{"debug", Config{Target: types.X86_64Windows, Profile: types.Release, Debug: true}},
		{"size", Config{Target: types.X86_64Windows, Profile: types.Release, Size: true}},
	} {
		t.Run("off/"+tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if _, err := EmitPack(u, 0, e, tc.cfg, &buf); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(buf.String(), "preserve_nonecc") || strings.Contains(buf.String(), "$pnone") {
				t.Errorf("%s: emitted preserve_nonecc when disabled", tc.name)
			}
		})
	}
}

// TestPnoneTailTableSliceAndSret covers a const slice table ([]fn), an sret
// return type (24-byte struct), an Indirect non-byval parameter (16-byte Pair
// on Win64), and two tables sharing a handler.
func TestPnoneTailTableSliceAndSret(t *testing.T) {
	e := newTestEnv()
	big := e.structT("Big", types.Int, types.Int, types.Int)
	pair := e.structT("Pair", types.Int, types.Int)
	params := []types.TypeID{pair, types.Int, types.Int, types.Int, types.Int}
	fnT := e.tt.Fn(types.FnType{
		Params: []types.FnParam{{Type: pair}, {Type: types.Int}, {Type: types.Int}, {Type: types.Int}, {Type: types.Int}},
		Result: big,
	})
	op0Item := e.fn("Op0", params, big, 0)
	op1Item := e.fn("Op1", params, big, 0)
	op2Item := e.fn("Op2", params, big, 0)
	t1, sliceT := makeFnSliceConst(e, "Fast", fnT, op0Item, op1Item)
	t2, arrT := makeFnTableConst(e, "Slow", false, fnT, op0Item, op2Item)

	buildOp := func(item types.ItemID, tab types.ItemID, tabT types.TypeID, useSecond bool) *fnb {
		f := e.body(item)
		sp := f.at(10, 3)
		cond := f.local(types.Bool)
		f.assign(place(cond), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.Eq, A: mir.Copy(place(2)), B: mir.Imm(types.Int, 0), Type: types.Bool}, sp)
		doneB, nextB := f.newBlock(), f.newBlock()
		f.term(mir.Terminator{Kind: mir.TermSwitch, On: mir.Copy(place(cond)),
			Cases: f.b.AddCases(mir.SwitchCase{Value: 1, Target: doneB}), Default: nextB, Span: sp})
		f.cur = doneB
		f.assign(place(0), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggStruct, Item: e.tt.Get(big).Item,
			Ops: f.b.AddOperands(mir.Copy(place(3)), mir.Copy(place(4)), mir.Copy(place(5))), Type: big}, sp)
		f.ret(sp)
		f.cur = nextB
		idx := f.local(types.Int)
		f.assign(place(idx), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.BitAnd, A: mir.Copy(place(2)), B: mir.Imm(types.Int, 1), Type: types.Int}, sp)
		n := f.local(types.Int)
		f.assign(place(n), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.SubWrap, A: mir.Copy(place(2)), B: mir.Imm(types.Int, 1), Type: types.Int}, sp)
		fp := f.local(fnT)
		tl1 := f.local(tabT)
		f.assign(place(tl1), mir.Rvalue{Kind: mir.RvConstRef, Item: tab, Type: tabT}, sp)
		ip1 := f.b.Place(tl1, mir.Projection{Kind: mir.ProjIndex, A: uint32(idx), Type: fnT})
		f.assign(place(fp), use(mir.Copy(ip1)), sp)
		if useSecond {
			// Also dispatch through t2 in another branch to exercise shared handlers across two tables.
			b2, callB := f.newBlock(), f.newBlock()
			c2 := f.local(types.Bool)
			f.assign(place(c2), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.Eq, A: mir.Copy(place(3)), B: mir.Imm(types.Int, 0), Type: types.Bool}, sp)
			f.term(mir.Terminator{Kind: mir.TermSwitch, On: mir.Copy(place(c2)),
				Cases: f.b.AddCases(mir.SwitchCase{Value: 1, Target: b2}), Default: callB, Span: sp})
			f.cur = b2
			tl2 := f.local(arrT)
			f.assign(place(tl2), mir.Rvalue{Kind: mir.RvConstRef, Item: t2, Type: arrT}, sp)
			ip2 := f.b.Place(tl2, mir.Projection{Kind: mir.ProjIndex, A: uint32(idx), Type: fnT})
			f.assign(place(fp), use(mir.Copy(ip2)), sp)
			f.term(mir.Terminator{Kind: mir.TermGoto, Target: callB, Span: sp})
			f.cur = callB
		}
		f.term(mir.Terminator{Kind: mir.TermTailCall, Callee: mir.Callee{Kind: mir.CallValue, Value: mir.Copy(place(fp))},
			Args: f.b.AddArgs(
				mir.Arg{Operand: mir.Copy(place(1))}, mir.Arg{Operand: mir.Copy(place(n))},
				mir.Arg{Operand: mir.Copy(place(3))}, mir.Arg{Operand: mir.Copy(place(4))}, mir.Arg{Operand: mir.Copy(place(5))},
			), Dest: place(0), Spawn: mir.NoLocal, Span: sp})
		return f
	}
	op0 := buildOp(op0Item, t1, sliceT, true)
	op1 := buildOp(op1Item, t1, sliceT, false)
	op2 := buildOp(op2Item, t2, arrT, false)
	u := unitOf(e, []*mir.Body{op0.b, op1.b, op2.b})
	for i := range u.Funcs {
		u.Funcs[i].AddrTaken = true
	}
	for _, target := range []types.Target{types.X86_64Windows, types.X86_64Linux, types.Aarch64Linux, types.Aarch64Macos} {
		t.Run(target.Name, func(t *testing.T) {
			ll := emit(t, e, u, target, types.Release)
			for _, want := range []string{
				`@mk.main.Fast$pnone.data = private unnamed_addr constant [2 x ptr] [ptr @mk.main.Op0$pnone, ptr @mk.main.Op1$pnone]`,
				`@mk.main.Fast$pnone = private unnamed_addr constant { ptr, i64 } { ptr @mk.main.Fast$pnone.data, i64 2 }`,
				`@mk.main.Slow$pnone = private unnamed_addr constant [2 x ptr] [ptr @mk.main.Op0$pnone, ptr @mk.main.Op2$pnone]`,
				`musttail call preserve_nonecc void %`,
				`sret([24 x i8])`,
			} {
				if !strings.Contains(ll, want) {
					t.Errorf("%s: IR lacks %q", target.Name, want)
				}
			}
			clangC(t, target, t.TempDir(), "pnonesret", ll, "-O2")
		})
	}
}

// TestPnoneTailTableRejections verifies every disqualification rule emits zero
// preserve_nonecc definitions.
func TestPnoneTailTableRejections(t *testing.T) {
	cases := []struct {
		name   string
		target types.Target
		build  func(e *testEnv) *Unit
	}{
		{
			name:   "exported-table",
			target: types.X86_64Windows,
			build: func(e *testEnv) *Unit {
				u := pnone8Unit(e)
				// Mark Ops exported.
				id, _ := e.items.ByKey(types.ItemKey("main", "Ops"))
				e.items.Get(id).Exported = true
				return u
			},
		},
		{
			name:   "table-addr-escape",
			target: types.X86_64Windows,
			build: func(e *testEnv) *Unit {
				u := pnone8Unit(e)
				opsID, _ := e.items.ByKey(types.ItemKey("main", "Ops"))
				opsT := e.items.Get(opsID).Decl.(*types.ConstDecl).Type
				esc := e.body(e.fn("Escape", nil, e.tt.Ref(opsT), 0))
				esc.assign(place(0), mir.Rvalue{Kind: mir.RvGlobalRef, Item: opsID, Type: e.tt.Ref(opsT)}, esc.at(70, 3))
				esc.ret(esc.at(70, 3))
				u.Packs[0] = append(u.Packs[0], len(u.Funcs))
				u.Funcs = append(u.Funcs, Func{Symbol: "mk.main.Escape", Key: "main.Escape", Item: esc.item, Body: esc.b})
				return u
			},
		},
		{
			name:   "table-slice-escape",
			target: types.X86_64Windows,
			build: func(e *testEnv) *Unit {
				u := pnone8Unit(e)
				opsID, _ := e.items.ByKey(types.ItemKey("main", "Ops"))
				opsT := e.items.Get(opsID).Decl.(*types.ConstDecl).Type
				fnT := e.tt.Get(opsT).Elem
				esc := e.body(e.fn("EscapeSlice", nil, e.tt.Slice(fnT), 0))
				tl := esc.local(opsT)
				esc.assign(place(tl), mir.Rvalue{Kind: mir.RvConstRef, Item: opsID, Type: opsT}, esc.at(71, 3))
				esc.assign(place(0), mir.Rvalue{Kind: mir.RvSlice, Borrow: mir.Shared, Place: place(tl), Type: e.tt.Slice(fnT)}, esc.at(71, 3))
				esc.ret(esc.at(71, 3))
				u.Packs[0] = append(u.Packs[0], len(u.Funcs))
				u.Funcs = append(u.Funcs, Func{Symbol: "mk.main.EscapeSlice", Key: "main.EscapeSlice", Item: esc.item, Body: esc.b})
				return u
			},
		},
		{
			name:   "no-become-in-handlers",
			target: types.X86_64Windows,
			build: func(e *testEnv) *Unit {
				fnT := e.tt.Fn(types.FnType{Params: []types.FnParam{{Type: types.Int}}, Result: types.Int})
				hItem := e.fn("H", []types.TypeID{types.Int}, types.Int, 0)
				tab, arrT := makeFnTableConst(e, "T", false, fnT, hItem)
				h := e.body(hItem)
				h.assign(place(0), use(mir.Copy(place(1))), h.at(1, 1))
				h.ret(h.at(1, 1))
				caller := e.body(e.fn("Caller", []types.TypeID{types.Int}, types.Int, 0))
				tl := caller.local(arrT)
				caller.assign(place(tl), mir.Rvalue{Kind: mir.RvConstRef, Item: tab, Type: arrT}, caller.at(2, 1))
				ip := caller.b.Place(tl, mir.Projection{Kind: mir.ProjIndex, A: 1, Type: fnT})
				caller.callTo(mir.Callee{Kind: mir.CallValue, Value: mir.Copy(ip)}, place(0), mir.NoLocal, caller.at(2, 1), mir.Copy(place(1)))
				caller.ret(caller.at(2, 1))
				u := unitOf(e, []*mir.Body{h.b, caller.b})
				u.Funcs[0].AddrTaken = true
				return u
			},
		},
		{
			name:   "arbitrary-fn-param-become",
			target: types.X86_64Windows,
			build: func(e *testEnv) *Unit {
				fnT := e.tt.Fn(types.FnType{Params: []types.FnParam{{Type: types.Int}}, Result: types.Int})
				p := []types.TypeID{types.Int, fnT}
				fnT2 := e.tt.Fn(types.FnType{Params: []types.FnParam{{Type: types.Int}, {Type: fnT}}, Result: types.Int})
				op0Item := e.fn("Op0", p, types.Int, 0)
				op1Item := e.fn("Op1", p, types.Int, 0)
				tab, arrT := makeFnTableConst(e, "T", false, fnT2, op0Item, op1Item)
				op0 := e.body(op0Item)
				tl := op0.local(arrT)
				op0.assign(place(tl), mir.Rvalue{Kind: mir.RvConstRef, Item: tab, Type: arrT}, op0.at(1, 1))
				ip := op0.b.Place(tl, mir.Projection{Kind: mir.ProjIndex, A: 1, Type: fnT2})
				op0.term(mir.Terminator{Kind: mir.TermTailCall, Callee: mir.Callee{Kind: mir.CallValue, Value: mir.Copy(ip)},
					Args: op0.b.AddArgs(mir.Arg{Operand: mir.Copy(place(1))}, mir.Arg{Operand: mir.Copy(place(2))}), Dest: place(0), Spawn: mir.NoLocal, Span: op0.at(1, 1)})
				// Op1 tail-calls an arbitrary fn parameter -> disqualifies Op1 and T!
				op1 := e.body(op1Item)
				op1.term(mir.Terminator{Kind: mir.TermTailCall, Callee: mir.Callee{Kind: mir.CallValue, Value: mir.Copy(place(2))},
					Args: op1.b.AddArgs(mir.Arg{Operand: mir.Copy(place(1))}, mir.Arg{Operand: mir.Copy(place(2))}), Dest: place(0), Spawn: mir.NoLocal, Span: op1.at(2, 1)})
				u := unitOf(e, []*mir.Body{op0.b, op1.b})
				u.Funcs[0].AddrTaken, u.Funcs[1].AddrTaken = true, true
				return u
			},
		},
		{
			name:   "shared-local-call-and-tailcall",
			target: types.X86_64Windows,
			build: func(e *testEnv) *Unit {
				fnT := e.tt.Fn(types.FnType{Params: []types.FnParam{{Type: types.Int}}, Result: types.Int})
				opItem := e.fn("Op", []types.TypeID{types.Int}, types.Int, 0)
				tab, arrT := makeFnTableConst(e, "T", false, fnT, opItem)
				op := e.body(opItem)
				sp := op.at(1, 1)
				tl := op.local(arrT)
				op.assign(place(tl), mir.Rvalue{Kind: mir.RvConstRef, Item: tab, Type: arrT}, sp)
				fp := op.local(fnT)
				ip := op.b.Place(tl, mir.Projection{Kind: mir.ProjIndex, A: 1, Type: fnT})
				op.assign(place(fp), use(mir.Copy(ip)), sp)
				c := op.local(types.Bool)
				op.assign(place(c), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.Eq, A: mir.Copy(place(1)), B: mir.Imm(types.Int, 0), Type: types.Bool}, sp)
				bCall, bTail := op.newBlock(), op.newBlock()
				op.term(mir.Terminator{Kind: mir.TermSwitch, On: mir.Copy(place(c)),
					Cases: op.b.AddCases(mir.SwitchCase{Value: 1, Target: bCall}), Default: bTail, Span: sp})
				op.cur = bCall
				op.callTo(mir.Callee{Kind: mir.CallValue, Value: mir.Copy(place(fp))}, place(0), mir.NoLocal, sp, mir.Copy(place(1)))
				op.ret(sp)
				op.cur = bTail
				op.term(mir.Terminator{Kind: mir.TermTailCall, Callee: mir.Callee{Kind: mir.CallValue, Value: mir.Copy(place(fp))},
					Args: op.b.AddArgs(mir.Arg{Operand: mir.Copy(place(1))}), Dest: place(0), Spawn: mir.NoLocal, Span: sp})
				u := unitOf(e, []*mir.Body{op.b})
				u.Funcs[0].AddrTaken = true
				return u
			},
		},
		{
			name:   "byval-linux-stays-non-pnone",
			target: types.X86_64Linux,
			build: func(e *testEnv) *Unit {
				big := e.structT("Big", types.Int, types.Int, types.Int)
				fnT := e.tt.Fn(types.FnType{Params: []types.FnParam{{Type: big}, {Type: types.Int}}, Result: types.Int})
				opItem := e.fn("Op", []types.TypeID{big, types.Int}, types.Int, 0)
				tab, arrT := makeFnTableConst(e, "T", false, fnT, opItem)
				op := e.body(opItem)
				sp := op.at(1, 1)
				tl := op.local(arrT)
				op.assign(place(tl), mir.Rvalue{Kind: mir.RvConstRef, Item: tab, Type: arrT}, sp)
				ip := op.b.Place(tl, mir.Projection{Kind: mir.ProjIndex, A: 2, Type: fnT})
				op.term(mir.Terminator{Kind: mir.TermTailCall, Callee: mir.Callee{Kind: mir.CallValue, Value: mir.Copy(ip)},
					Args: op.b.AddArgs(mir.Arg{Operand: mir.Copy(place(1))}, mir.Arg{Operand: mir.Copy(place(2))}), Dest: place(0), Spawn: mir.NoLocal, Span: sp})
				u := unitOf(e, []*mir.Body{op.b})
				u.Funcs[0].AddrTaken = true
				return u
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv()
			u := tc.build(e)
			cfg := Config{Target: tc.target, Profile: types.Release}
			g := newPackGen(u, 0, e, &cfg, nil)
			if s := g.pnoneSetOf(); s != nil && (len(s.tables) > 0 || len(s.funcs) > 0) {
				t.Errorf("%s: got %d tables and %d funcs, want none", tc.name, len(s.tables), len(s.funcs))
			}
			// For cases whose MIR is valid to emit (not synthetic byval-through-fn-value which tailcall rejects),
			// also confirm the emitted IR contains no preserve_nonecc.
			if tc.name != "byval-linux-stays-non-pnone" && tc.name != "arbitrary-fn-param-become" {
				ll := emit(t, e, u, tc.target, types.Release)
				if strings.Contains(ll, "preserve_nonecc") || strings.Contains(ll, "$pnone") {
					t.Errorf("%s: emitted preserve_nonecc:\n%s", tc.name, ll)
				}
			}
		})
	}
}

// TestPnoneCrossTargetAsm compiles the 8-register-parameter dispatch loop to
// assembly with clang -O3 -S for Windows x86-64, Linux x86-64, and Linux
// AArch64, proving cross-target lowering and zero stack spills on the hot
// handler dispatch path.
func TestPnoneCrossTargetAsm(t *testing.T) {
	dir := needLLVM(t)
	e := newTestEnv()
	u := pnone8Unit(e)
	targets := []struct {
		target   types.Target
		triple   string
		tailJump string
		spillPat string
	}{
		{types.X86_64Windows, "x86_64-pc-windows-msvc", "jmpq\t*", "(%rsp)"},
		{types.X86_64Linux, "x86_64-unknown-linux-gnu", "jmpq\t*", "(%rsp)"},
		{types.Aarch64Linux, "aarch64-unknown-linux-gnu", "\tbr\tx", "[sp"},
	}
	for _, tc := range targets {
		t.Run(tc.target.Name, func(t *testing.T) {
			ll := emit(t, e, u, tc.target, types.Release)
			tmp := t.TempDir()
			llPath := filepath.Join(tmp, "pnone.ll")
			sPath := filepath.Join(tmp, "pnone.s")
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
			// Extract Helper$pnone (pure 8-register-arg indirect dispatch, no cold call).
			label := "mk.main.Helper$pnone:"
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
			if !strings.Contains(body, tc.tailJump) {
				t.Errorf("%s: Helper$pnone lacks indirect tail jump %q:\n%s", tc.target.Name, tc.tailJump, body)
			}
			if strings.Contains(body, tc.spillPat) {
				t.Errorf("%s: Helper$pnone spilled to stack (%q):\n%s", tc.target.Name, tc.spillPat, body)
			}
		})
	}
}

// TestPnonePackFactsCrossPack verifies that when pack 0 defines Op0 and pack 1
// defines Op1 and Ops = [Op0, Op1], escaping Ops in pack 1 moves pack 0's
// PackFacts hash, and both packs emit valid cross-pack preserve_nonecc IR.
func TestPnonePackFactsCrossPack(t *testing.T) {
	build := func(escape bool) (*testEnv, *Unit) {
		e := newTestEnv()
		u := pnone8Unit(e)
		// Split: pack 0 holds Op0 and Slow; pack 1 holds Op1, Helper, Enter, EnterDirect (and optional Escape).
		u.Packs = [][]int{{0, 3}, {1, 2, 4, 5}}
		u.Funcs[0].Pack, u.Funcs[3].Pack = 0, 0
		u.Funcs[1].Pack, u.Funcs[2].Pack, u.Funcs[4].Pack, u.Funcs[5].Pack = 1, 1, 1, 1
		if escape {
			opsID, _ := e.items.ByKey(types.ItemKey("main", "Ops"))
			opsT := e.items.Get(opsID).Decl.(*types.ConstDecl).Type
			esc := e.body(e.fn("Escape", nil, e.tt.Ref(opsT), 0))
			esc.assign(place(0), mir.Rvalue{Kind: mir.RvGlobalRef, Item: opsID, Type: e.tt.Ref(opsT)}, esc.at(70, 3))
			esc.ret(esc.at(70, 3))
			u.Packs[1] = append(u.Packs[1], len(u.Funcs))
			u.Funcs = append(u.Funcs, Func{Symbol: "mk.main.Escape", Key: "main.Escape", Item: esc.item, Body: esc.b, Pack: 1})
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
		t.Error("escaping Ops in pack 1 left pack 0's PackFacts hash unchanged")
	}
	// Emit both packs of u0 and verify cross-pack declarations & clang verification.
	var ll0, ll1 bytes.Buffer
	if _, err := EmitPack(u0, 0, e0, cfg, &ll0); err != nil {
		t.Fatal(err)
	}
	if _, err := EmitPack(u0, 1, e0, cfg, &ll1); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ll0.String(), `declare dso_local preserve_nonecc noundef i64 @mk.main.Op1$pnone(`) {
		t.Errorf("pack 0 lacks cross-pack preserve_nonecc declare of Op1$pnone:\n%s", ll0.String())
	}
	if !strings.Contains(ll1.String(), `declare dso_local preserve_nonecc noundef i64 @mk.main.Op0$pnone(`) {
		t.Errorf("pack 1 lacks cross-pack preserve_nonecc declare of Op0$pnone:\n%s", ll1.String())
	}
	tmp := t.TempDir()
	clangC(t, types.X86_64Windows, tmp, "p0", ll0.String(), "-O2")
	clangC(t, types.X86_64Windows, tmp, "p1", ll1.String(), "-O2")
}
