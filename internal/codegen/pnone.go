// Why: a threaded bytecode interpreter dispatches from handler to handler
// through a module-private const table of fn values (`become handlers[op](...)`),
// carrying 8 to 12 state values (vm, ip, sp, registers) as parameters. Under
// the platform C ABI (4 integer registers on Win64, 6 on System V, 8 on
// AArch64), arguments past the register count spill through the stack on every
// dispatch and every handler preserves callee-saved registers it touches.
// LLVM's preserve_nonecc calling convention keeps 8 to 12 arguments in
// registers with no callee-saved spills across musttail hops.
//
// What: pnoneEnabled, pnoneSetOf (unit-wide fixed-point analysis of eligible
// const dispatch tables and handler functions), pnoneBody, pnoneBodyLocals,
// pnoneTableLocal, pnoneDecl, pnoneTableGlobal, emitPnoneEntry and
// pnonePackFacts.
//
// How: in optimized, non-debug, non-size builds on x86_64 and aarch64,
// pnoneSetOf scans the unit for non-exported, non-section const arrays or
// slices of concrete non-extern, non-naked, non-variadic, non-byval Mk
// functions that never escape (only indexed as call/tail-call callees or read
// for length). A greatest fixed point keeps tables with at least one
// tail-dispatch inside the handler set and functions whose every TermTailCall
// stays inside the set (either direct to another member or a pure-tail load
// from an eligible table). Each member gets a preserve_nonecc body
// <sym>$pnone while its original symbol <sym> stays a C ABI wrapper that calls
// <sym>$pnone once, so ordinary calls and entry dispatches through the C ABI
// table work unchanged while every inner `become` dispatches through a private
// companion table <table_sym>$pnone via `musttail call preserve_nonecc`.
// PackFacts hashes each pack member's pnone bit and pure-tail local bindings.

package codegen

import (
	"strings"

	"mk/internal/abi"
	"mk/internal/llvm"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// pnoneSuffix names a preserve_nonecc body and its companion dispatch table:
// '$' is in no Mk symbol.
const pnoneSuffix = "$pnone"

// pnoneEnabled reports whether preserve_nonecc tail-dispatch cloning runs for
// cfg: optimized builds (release, checked) without --debug or --profile size,
// on architectures where LLVM 21 supports preserve_nonecc (x86_64, aarch64).
func pnoneEnabled(cfg *Config) bool {
	if !cfg.Profile.Optimized() || cfg.Debug || cfg.Size {
		return false
	}
	return cfg.Target.Arch == types.ArchX64 || cfg.Target.Arch == types.ArchArm64
}

// pnoneKey keys Unit's memoized pnoneSet so tests reusing one Unit across
// targets or configurations never read a stale set.
type pnoneKey struct {
	n      int
	target string
	arch   types.Arch
	os     types.OS
	root   string
	main   types.Key
}

// pnoneTable describes one eligible const function dispatch table.
type pnoneTable struct {
	item  types.ItemID
	slice bool  // []fn(...) rather than [N]fn(...)
	elems []int // Funcs indices in table order
}

// pnoneSet is the unit's eligible preserve_nonecc tail-dispatch tables and
// handler functions, computed once per (Unit, pnoneKey) without touching g.m.
type pnoneSet struct {
	tables     map[types.ItemID]*pnoneTable
	funcs      map[int]bool
	bodyTables map[int][]types.ItemID // per fi in funcs: pure-tail table item per local (nil when none)
}

// pnoneSetOf computes or returns the unit's memoized pnoneSet (nil when
// disabled). Safe with g.m == nil (PackFacts).
func (g *packGen) pnoneSetOf() *pnoneSet {
	if !pnoneEnabled(g.cfg) {
		return nil
	}
	u := g.u
	key := pnoneKey{n: len(u.Funcs) + 1, target: g.cfg.Target.Name, arch: g.cfg.Target.Arch, os: g.cfg.Target.OS, root: g.cfg.RootModule, main: g.cfg.Main}
	u.idxMu.Lock()
	if u.pnoneKey == key && u.pnone != nil {
		s := u.pnone
		u.idxMu.Unlock()
		return s
	}
	u.idxMu.Unlock()
	s := g.computePnoneSet()
	u.idxMu.Lock()
	u.pnone, u.pnoneKey = s, key
	u.idxMu.Unlock()
	return s
}

// pnoneBody reports whether Funcs[fi] is split into a C ABI wrapper at its own
// symbol and a preserve_nonecc body at <sym>$pnone.
func (g *packGen) pnoneBody(fi int) bool {
	s := g.pnoneSetOf()
	return s != nil && s.funcs[fi]
}

// pnoneBodyLocals returns the per-local pure-tail table bindings of Funcs[fi],
// or nil when Funcs[fi] has none.
func (g *packGen) pnoneBodyLocals(fi int) []types.ItemID {
	s := g.pnoneSetOf()
	if s == nil {
		return nil
	}
	return s.bodyTables[fi]
}

// pnoneTableLocal is the eligible const table item local li is bound to inside
// a preserve_nonecc body, or NoItem.
func (fg *fnGen) pnoneTableLocal(li int) types.ItemID {
	if !fg.pnone || li < 0 || li >= len(fg.pnoneLocals) {
		return types.NoItem
	}
	return fg.pnoneLocals[li]
}

// pnoneDecl is the preserve_nonecc body of split Funcs[fi] in this pack: its
// definition when this pack holds it, else a declaration with preserve_nonecc.
func (g *packGen) pnoneDecl(fi int) *llvm.Function {
	if b, ok := g.pnoneDefs[fi]; ok {
		return b
	}
	f := &g.u.Funcs[fi]
	if f.Local && f.Body != nil {
		source.ICEf(source.Span{}, "codegen: %s is Local to pack %d but referenced from pack %d", f.Symbol, f.Pack, g.p)
	}
	ls := g.funcSigOf(fi)
	lk, vis, dso := g.tailLinkage(f)
	attrs := llvm.Attrs{}.Flag("nounwind")
	if ls.result == types.Never {
		attrs = attrs.Flag("noreturn")
	}
	sig := g.funcSig(ls, lk, vis, dso, attrs)
	sig.CC = llvm.PreserveNoneCC
	return g.m.Declare(f.Symbol+pnoneSuffix, sig)
}

// pnoneTableGlobal is this pack's private constant companion dispatch table
// holding pointers to each element's <sym>$pnone body, memoized per pack.
func (g *packGen) pnoneTableGlobal(item types.ItemID) llvm.Value {
	if v, ok := g.pnoneTables[item]; ok {
		return v
	}
	s := g.pnoneSetOf()
	tb := s.tables[item]
	if tb == nil {
		source.ICEf(source.Span{}, "codegen: preserve_nonecc table requested for ineligible const %s", g.items.Get(item).Key)
	}
	m := g.m
	it := g.items.Get(item)
	ptrs := make([]llvm.Value, len(tb.elems))
	for i, fi := range tb.elems {
		ptrs[i] = g.pnoneDecl(fi).Ref()
	}
	arrInit := m.ConstArray(m.Ptr(), ptrs...)
	name := "mk." + it.Key.String() + pnoneSuffix
	var v llvm.Value
	if tb.slice {
		dataGl := m.AddGlobal(name+".data", llvm.GlobalAttrs{
			Linkage: llvm.Private, Constant: true, UnnamedAddr: true, Align: 8, Type: arrInit.Ty, Init: arrInit,
		})
		hdrInit := m.ConstStruct(false, m.GlobalRef(dataGl), m.ConstInt(m.I(64), uint64(len(ptrs))))
		gl := m.AddGlobal(name, llvm.GlobalAttrs{
			Linkage: llvm.Private, Constant: true, UnnamedAddr: true, Align: 8, Type: hdrInit.Ty, Init: hdrInit,
		})
		v = m.GlobalRef(gl)
	} else {
		gl := m.AddGlobal(name, llvm.GlobalAttrs{
			Linkage: llvm.Private, Constant: true, UnnamedAddr: true, Align: 8, Type: arrInit.Ty, Init: arrInit,
		})
		v = m.GlobalRef(gl)
	}
	if g.pnoneTables == nil {
		g.pnoneTables = map[types.ItemID]llvm.Value{}
	}
	g.pnoneTables[item] = v
	return v
}

// emitPnoneEntry fills the C ABI wrapper of split Funcs[fi]: a regular
// (non-musttail) preserve_nonecc call to <sym>$pnone forwarding all lowered
// parameters and returning its result, so callee-saved registers are saved
// once at the outer C ABI boundary.
func (g *packGen) emitPnoneEntry(fi int) {
	w, body := g.defs[fi], g.pnoneDefs[fi]
	b := w.NewBlock("entry")
	args := make([]llvm.Value, len(w.Sig().Params))
	for i := range args {
		args[i] = w.Param(i)
	}
	sig := body.Sig()
	var argAttrs []llvm.Attrs
	if len(sig.ParamAttrs) != 0 {
		argAttrs = sig.ParamAttrs
	}
	attrs := llvm.Attrs{}.Flag("nounwind")
	if g.funcSigOf(fi).result == types.Never {
		attrs = attrs.Flag("noreturn")
	}
	r := b.CallCC(llvm.PreserveNoneCC, body.Type(), body.Ref(), args, argAttrs, sig.RetAttrs, attrs)
	switch {
	case g.funcSigOf(fi).result == types.Never:
		b.Unreachable()
	case sig.Ret == g.m.Void():
		b.Ret(nil)
	default:
		b.Ret(&r)
	}
}

// pnonePackFacts hashes pack p's preserve_nonecc tail-table decisions into h
// so incremental cache keys move whenever another pack enables, disables or
// changes a member's preserve_nonecc split or table bindings.
func pnonePackFacts(ag *packGen, p int, h *source.Hasher) {
	if !pnoneEnabled(ag.cfg) {
		return
	}
	h.String("pnone.v1")
	s := ag.pnoneSetOf()
	for _, fi := range ag.u.Packs[p] {
		on := s != nil && s.funcs[fi]
		h.Bool(on)
		if !on {
			continue
		}
		bt := s.bodyTables[fi]
		h.Uvarint(uint64(len(bt)))
		cnt := 0
		for _, ti := range bt {
			if ti != types.NoItem {
				cnt++
			}
		}
		h.Uvarint(uint64(cnt))
		for li, ti := range bt {
			if ti != types.NoItem {
				h.Uvarint(uint64(li))
				h.String(ag.items.Get(ti).Key.String())
			}
		}
	}
}

// pnoneFuncEligible reports whether Funcs[fi] may be given a preserve_nonecc
// body: a concrete, non-extern, non-naked, non-variadic, non-section,
// non-runtime-hook, non-constant-time Mk function whose lowered signature on
// this target has no byval parameter (byval functions belong to tailbody.go)
// and, on non-Windows x86_64, uses at most 10 general-purpose argument
// register slots.
func (g *packGen) pnoneFuncEligible(fi int) bool {
	if fi < 0 || fi >= len(g.u.Funcs) {
		return false
	}
	f := &g.u.Funcs[fi]
	if f.Body == nil || g.isExtern(f) || g.nakedFn(f) || g.funcSection(f.Item) != "" ||
		g.wrappedMain(f) || g.libcFn(f) || g.codegenCalled(fi) {
		return false
	}
	it := g.items.Get(f.Item)
	if it == nil {
		return false
	}
	if mod := g.items.Module(it.Module); mod != nil &&
		(mod.Path == "std.crypto" || strings.HasPrefix(mod.Path, "std.crypto.") ||
			mod.Path == "std.simd.kernel" || strings.HasPrefix(mod.Path, "std.simd.kernel.")) {
		return false
	}
	if fd, ok := it.Decl.(*types.FuncDecl); ok && fd.Sig.Variadic {
		return false
	}
	b := f.Body
	params := make([]types.TypeID, int(b.ParamCount))
	for i := range params {
		params[i] = b.Locals[i+1].Type
	}
	result := b.Locals[mir.ReturnLocal].Type
	as := abi.Lower(g.tt, g.env.Layout, g.cfg.Target, params, len(params), false, result)
	for _, sl := range as.Params {
		if sl.Kind == abi.Indirect && sl.Byval {
			return false
		}
	}
	// On non-Windows x86_64, preserve_nonecc assigns up to 12 GP argument
	// registers (r12..r15, rdi, rsi, rdx, rcx, r8, r9, r11, rax). Leaving r11
	// and rax free (at most 10 GP slots) avoids two LLVM 21 x86_64 System V
	// defects: inline-asm stack probing hardcodes r11 as its scratch limit
	// register without saving it (silently corrupting an 11th GP parameter on
	// >16 KiB frames or >4 KiB stack realignments), and occupying all 8
	// registers of GR64_TC (at >=12 GP slots) exhausts registers and crashes
	// the register allocator on musttail dispatches.
	if g.cfg.Target.Arch == types.ArchX64 && g.cfg.Target.OS != types.OSWindows && pnoneX64SysVGPRegs(as) > 10 {
		return false
	}
	return true
}

// pnoneX64SysVGPRegs counts the 64-bit general-purpose argument register slots
// occupied by lowered signature as on non-Windows x86_64 (including a hidden
// sret pointer and two slots per 128-bit integer).
func pnoneX64SysVGPRegs(as abi.Sig) int {
	n := 0
	if as.Ret.Kind == abi.Indirect {
		n++
	}
	for _, sl := range as.Params {
		switch sl.Kind {
		case abi.Indirect:
			n++
		case abi.Direct:
			for _, p := range sl.Parts {
				if p.Class == types.RegInt {
					n += int((p.Bytes + 7) / 8)
				}
			}
		}
	}
	return n
}

// candidateTable checks whether item is a module-private, non-section const
// array or slice of eligible concrete Mk functions.
func (g *packGen) candidateTable(item types.ItemID) *pnoneTable {
	it := g.items.Get(item)
	if it == nil || it.Kind != types.KindConst || it.Exported {
		return nil
	}
	cd, ok := it.Decl.(*types.ConstDecl)
	if !ok || !cd.HasValue || cd.Section != "" {
		return nil
	}
	cv := g.env.Consts().Get(cd.Value)
	if cv == nil {
		return nil
	}
	ty := g.tt.Get(cd.Type)
	var relocs []types.Reloc
	var isSlice bool
	switch ty.Kind {
	case types.KArray:
		if g.tt.Kind(ty.Elem) != types.KFn {
			return nil
		}
		n := len(cv.Relocs)
		if n == 0 || len(cv.Bytes) != n*8 {
			return nil
		}
		relocs = cv.Relocs
	case types.KSlice:
		if g.tt.Kind(ty.Elem) != types.KFn {
			return nil
		}
		if len(cv.Bytes) != 16 || len(cv.Relocs) != 1 || cv.Relocs[0].Kind != types.RelocData || cv.Relocs[0].Offset != 0 {
			return nil
		}
		n := types.LoadUint(cv.Bytes[8:], 8)
		dataCV := g.env.Consts().Get(cv.Relocs[0].Data)
		if n == 0 || dataCV == nil || uint64(len(dataCV.Relocs)) != n || uint64(len(dataCV.Bytes)) != n*8 {
			return nil
		}
		relocs = dataCV.Relocs
		isSlice = true
	default:
		return nil
	}
	elems := make([]int, len(relocs))
	for i, rl := range relocs {
		if rl.Kind != types.RelocFn || rl.Offset != uint32(i*8) || g.inlineIntrinsic(rl.Fn.Item) {
			return nil
		}
		fi, found := g.findFunc(rl.Fn.Item, rl.Fn.Args)
		if !found || !g.pnoneFuncEligible(fi) {
			return nil
		}
		elems[i] = fi
	}
	return &pnoneTable{item: item, slice: isSlice, elems: elems}
}

// pnoneBodyInfo summarizes one MIR body's tail-call edges and table-local uses.
type pnoneBodyInfo struct {
	otherTail      bool           // indirect become not from pure-tail candidate table, or invalid direct become
	directTails    []int          // resolved Funcs indices of direct TermTailCalls
	indirectTables []types.ItemID // candidate tables dispatched via pure-tail indirect TermTailCalls
	pureTabLocals  []types.ItemID // per local: candidate table item if pure-tail table local, else NoItem
}

// computePnoneSet runs the unit-wide table discovery, escape analysis, direct
// tail-call closure, and greatest-fixed-point pruning.
func (g *packGen) computePnoneSet() *pnoneSet {
	u := g.u
	cands := map[types.ItemID]*pnoneTable{}
	checked := map[types.ItemID]bool{}
	for fi := range u.Funcs {
		b := u.Funcs[fi].Body
		if b == nil {
			continue
		}
		for bi := range b.Blocks {
			for si := range b.Blocks[bi].Stmts {
				s := &b.Blocks[bi].Stmts[si]
				if s.Kind != mir.StAssign || s.Rvalue.Kind != mir.RvConstRef {
					continue
				}
				id := s.Rvalue.Item
				if checked[id] {
					continue
				}
				checked[id] = true
				if tb := g.candidateTable(id); tb != nil {
					cands[id] = tb
				}
			}
		}
	}
	if len(cands) == 0 {
		return &pnoneSet{}
	}
	escaped := map[types.ItemID]bool{}
	binfo := make([]pnoneBodyInfo, len(u.Funcs))
	for fi := range u.Funcs {
		if u.Funcs[fi].Body != nil {
			binfo[fi] = g.analyzeBodyPnone(fi, cands, escaped)
		}
	}
	for id := range escaped {
		delete(cands, id)
	}
	if len(cands) == 0 {
		return &pnoneSet{}
	}
	// Forward closure from table elements along direct TermTailCall edges.
	funcs := map[int]bool{}
	var work []int
	for _, tb := range cands {
		for _, fi := range tb.elems {
			if !funcs[fi] && g.pnoneFuncEligible(fi) {
				funcs[fi] = true
				work = append(work, fi)
			}
		}
	}
	for len(work) > 0 {
		fi := work[len(work)-1]
		work = work[:len(work)-1]
		for _, gi := range binfo[fi].directTails {
			if !funcs[gi] && g.pnoneFuncEligible(gi) {
				funcs[gi] = true
				work = append(work, gi)
			}
		}
	}
	// Greatest fixed point: prune invalid functions, invalid/unused tables,
	// and functions no longer reachable from any surviving table.
	for changed := true; changed; {
		changed = false
		for fi := range funcs {
			bi := &binfo[fi]
			ok := !bi.otherTail
			for _, gi := range bi.directTails {
				if !funcs[gi] {
					ok = false
					break
				}
			}
			for _, ti := range bi.indirectTables {
				if cands[ti] == nil {
					ok = false
					break
				}
			}
			if !ok {
				delete(funcs, fi)
				changed = true
			}
		}
		usedByFunc := map[types.ItemID]bool{}
		for fi := range funcs {
			for _, ti := range binfo[fi].indirectTables {
				usedByFunc[ti] = true
			}
		}
		for ti, tb := range cands {
			ok := usedByFunc[ti]
			for _, fi := range tb.elems {
				if !funcs[fi] {
					ok = false
					break
				}
			}
			if !ok {
				delete(cands, ti)
				changed = true
			}
		}
		reach := map[int]bool{}
		var q []int
		for _, tb := range cands {
			for _, fi := range tb.elems {
				if funcs[fi] && !reach[fi] {
					reach[fi] = true
					q = append(q, fi)
				}
			}
		}
		for len(q) > 0 {
			fi := q[len(q)-1]
			q = q[:len(q)-1]
			for _, gi := range binfo[fi].directTails {
				if funcs[gi] && !reach[gi] {
					reach[gi] = true
					q = append(q, gi)
				}
			}
		}
		for fi := range funcs {
			if !reach[fi] {
				delete(funcs, fi)
				changed = true
			}
		}
	}
	if len(cands) == 0 || len(funcs) == 0 {
		return &pnoneSet{}
	}
	bodyTables := make(map[int][]types.ItemID, len(funcs))
	for fi := range funcs {
		raw := binfo[fi].pureTabLocals
		if len(raw) == 0 {
			continue
		}
		var out []types.ItemID
		for li, ti := range raw {
			if ti != types.NoItem && cands[ti] != nil {
				if out == nil {
					out = make([]types.ItemID, len(raw))
				}
				out[li] = ti
			}
		}
		if out != nil {
			bodyTables[fi] = out
		}
	}
	return &pnoneSet{tables: cands, funcs: funcs, bodyTables: bodyTables}
}

// analyzeBodyPnone classifies every table reference, fn-local flow, and
// TermTailCall in Funcs[fi]. Any non-call escape of a candidate table is
// recorded in escaped.
func (g *packGen) analyzeBodyPnone(fi int, cands map[types.ItemID]*pnoneTable, escaped map[types.ItemID]bool) pnoneBodyInfo {
	f := &g.u.Funcs[fi]
	b := f.Body
	nloc := len(b.Locals)
	tabLoc := make([]types.ItemID, nloc)
	tabBad := make([]bool, nloc)
	fnBad := make([]bool, nloc)
	fnAssigned := make([]bool, nloc)
	for li := range b.Locals {
		if li == int(mir.ReturnLocal) || li <= int(b.ParamCount) {
			tabBad[li] = true
			fnBad[li] = true
		}
		if g.tt.Kind(b.Locals[li].Type) != types.KFn {
			fnBad[li] = true
		}
	}
	// Pass 1: find locals assigned exclusively by RvConstRef of a single
	// candidate table, and mark writes/borrows/asm uses on all locals.
	for i := range b.Asms {
		for k := range b.Asms[i].Operands {
			op := &b.Asms[i].Operands[k]
			switch op.Kind {
			case mir.AsmSym:
				if cands[op.Sym] != nil {
					escaped[op.Sym] = true
				}
			case mir.AsmOut, mir.AsmLateOut, mir.AsmInOut, mir.AsmInLateOut:
				if op.Out.Local != mir.NoLocal && int(op.Out.Local) < nloc {
					tabBad[op.Out.Local] = true
					fnBad[op.Out.Local] = true
				}
			}
			if (op.Kind == mir.AsmIn || op.Kind == mir.AsmInOut || op.Kind == mir.AsmInLateOut) &&
				(op.In.Kind == mir.OpCopy || op.In.Kind == mir.OpMove) {
				tabBad[op.In.Place.Local] = true
				fnBad[op.In.Place.Local] = true
			}
		}
	}
	for bi := range b.Blocks {
		blk := &b.Blocks[bi]
		for si := range blk.Stmts {
			s := &blk.Stmts[si]
			switch s.Kind {
			case mir.StJoin:
				tabBad[s.Local] = true
				fnBad[s.Local] = true
			case mir.StAssign:
				rv := &s.Rvalue
				if rv.Kind == mir.RvGlobalRef && cands[rv.Item] != nil {
					escaped[rv.Item] = true
				}
				if rv.Kind == mir.RvRef || rv.Kind == mir.RvSlice {
					// Borrowing a table local or fn local disqualifies it.
					tabBad[rv.Place.Local] = true
					fnBad[rv.Place.Local] = true
				}
				dst := s.Place
				if rv.Kind == mir.RvConstRef && cands[rv.Item] != nil {
					if dst.Proj.Len != 0 {
						escaped[rv.Item] = true
						tabBad[dst.Local] = true
					} else {
						switch {
						case tabLoc[dst.Local] == types.NoItem:
							tabLoc[dst.Local] = rv.Item
						case tabLoc[dst.Local] != rv.Item:
							escaped[tabLoc[dst.Local]] = true
							escaped[rv.Item] = true
							tabBad[dst.Local] = true
						}
					}
				} else {
					tabBad[dst.Local] = true
				}
			}
		}
		t := &blk.Term
		if isCall(t) {
			tabBad[t.Dest.Local] = true
			fnBad[t.Dest.Local] = true
			if t.HasErr {
				tabBad[t.Err.Error] = true
				fnBad[t.Err.Error] = true
			}
			if t.Spawn != mir.NoLocal {
				tabBad[t.Spawn] = true
				fnBad[t.Spawn] = true
			}
			for _, a := range b.Args[t.Args.Start:t.Args.End()] {
				if a.Mode == mir.ArgReborrow && (a.Operand.Kind == mir.OpCopy || a.Operand.Kind == mir.OpMove) {
					tabBad[a.Operand.Place.Local] = true
					fnBad[a.Operand.Place.Local] = true
				}
			}
		}
	}
	for li, ti := range tabLoc {
		if ti != types.NoItem && tabBad[li] {
			escaped[ti] = true
			tabLoc[li] = types.NoItem
		}
	}
	isTabIndex := func(p mir.Place) bool {
		return tabLoc[p.Local] != types.NoItem && p.Proj.Len == 1 && b.Proj(p)[0].Kind == mir.ProjIndex
	}
	// Pass 2: determine fn-local eligibility (every write is Copy/Move of a
	// table index or another eligible fn local) to a fixed point.
	type edge struct{ from, to int }
	var tabEdges []edge // tabLocal -> fnLocal
	var fnEdges []edge  // fnLocal -> fnLocal
	for bi := range b.Blocks {
		for si := range b.Blocks[bi].Stmts {
			s := &b.Blocks[bi].Stmts[si]
			if s.Kind != mir.StAssign {
				continue
			}
			dst := s.Place
			rv := &s.Rvalue
			if dst.Proj.Len != 0 {
				fnBad[dst.Local] = true
				continue
			}
			if rv.Kind == mir.RvUse && (rv.A.Kind == mir.OpCopy || rv.A.Kind == mir.OpMove) {
				src := rv.A.Place
				switch {
				case isTabIndex(src):
					fnAssigned[dst.Local] = true
					tabEdges = append(tabEdges, edge{int(src.Local), int(dst.Local)})
					continue
				case src.Proj.Len == 0 && !fnBad[src.Local]:
					fnAssigned[dst.Local] = true
					fnEdges = append(fnEdges, edge{int(src.Local), int(dst.Local)})
					continue
				}
			}
			fnBad[dst.Local] = true
		}
	}
	for li := range fnBad {
		if !fnAssigned[li] {
			fnBad[li] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, e := range fnEdges {
			if fnBad[e.from] && !fnBad[e.to] {
				fnBad[e.to] = true
				changed = true
			}
		}
	}
	// Pass 3: classify every read of table locals and eligible fn locals, and
	// collect all direct and indirect TermTailCalls.
	tabTail := make([]bool, nloc)
	tabCall := make([]bool, nloc)
	tabEsc := make([]bool, nloc)
	fnTail := make([]bool, nloc)
	fnCall := make([]bool, nloc)
	fnEsc := make([]bool, nloc)
	markEscPlace := func(p mir.Place) {
		if tabLoc[p.Local] != types.NoItem {
			tabEsc[p.Local] = true
		}
		if !fnBad[p.Local] {
			fnEsc[p.Local] = true
		}
	}
	markEscOp := func(o mir.Operand) {
		if o.Kind == mir.OpCopy || o.Kind == mir.OpMove {
			markEscPlace(o.Place)
		}
	}
	for _, e := range tabEdges {
		if fnBad[e.to] {
			tabEsc[e.from] = true
		}
	}
	for _, e := range fnEdges {
		if !fnBad[e.from] && fnBad[e.to] {
			fnEsc[e.from] = true
		}
	}
	var info pnoneBodyInfo
	type tailCallee struct {
		isTab bool
		local int
	}
	var indirectTails []tailCallee
	cg := &fnGen{g: g, f: f, fi: fi, b: b}
	for bi := range b.Blocks {
		blk := &b.Blocks[bi]
		for si := range blk.Stmts {
			s := &blk.Stmts[si]
			if s.Kind != mir.StAssign {
				continue
			}
			if s.Place.Proj.Len > 0 {
				markEscPlace(s.Place)
			}
			rv := &s.Rvalue
			switch rv.Kind {
			case mir.RvUse:
				// Already accounted for as a tabEdges/fnEdges copy if dst is an
				// eligible fn local; any other RvUse escapes its source.
				if s.Place.Proj.Len == 0 && !fnBad[s.Place.Local] && (rv.A.Kind == mir.OpCopy || rv.A.Kind == mir.OpMove) {
					src := rv.A.Place
					if isTabIndex(src) || (src.Proj.Len == 0 && !fnBad[src.Local]) {
						continue
					}
				}
				markEscOp(rv.A)
			case mir.RvLen:
				// Length of a whole table local (bounds check) is non-escaping.
				if rv.Place.Proj.Len == 0 && tabLoc[rv.Place.Local] != types.NoItem {
					continue
				}
				markEscPlace(rv.Place)
			case mir.RvRef, mir.RvSlice, mir.RvDiscriminant:
				markEscPlace(rv.Place)
				markEscOp(rv.A)
				markEscOp(rv.B)
			case mir.RvUnary, mir.RvBinary, mir.RvCast, mir.RvRepeat:
				markEscOp(rv.A)
				markEscOp(rv.B)
			case mir.RvAggregate:
				for _, op := range b.Operands[rv.Ops.Start:rv.Ops.End()] {
					markEscOp(op)
				}
			}
		}
		t := &blk.Term
		switch t.Kind {
		case mir.TermSwitch:
			markEscOp(t.On)
		case mir.TermPanic:
			if t.HasMsg {
				markEscOp(t.Msg)
			}
		case mir.TermCall:
			if t.Dest.Proj.Len > 0 {
				markEscPlace(t.Dest)
			}
			for _, a := range b.Args[t.Args.Start:t.Args.End()] {
				markEscOp(a.Operand)
			}
			if t.Callee.Kind == mir.CallValue {
				o := t.Callee.Value
				if t.Spawn == mir.NoLocal && (o.Kind == mir.OpCopy || o.Kind == mir.OpMove) {
					p := o.Place
					switch {
					case isTabIndex(p):
						tabCall[p.Local] = true
						continue
					case p.Proj.Len == 0 && !fnBad[p.Local]:
						fnCall[p.Local] = true
						continue
					}
				}
				markEscOp(o)
			}
		case mir.TermTailCall:
			for _, a := range b.Args[t.Args.Start:t.Args.End()] {
				markEscOp(a.Operand)
			}
			switch t.Callee.Kind {
			case mir.CallDirect:
				if fd, ok := g.items.Get(t.Callee.Fn.Item).Decl.(*types.FuncDecl); ok && (fd.Intrinsic || fd.Sig.Extern && fd.Bodiless) {
					info.otherTail = true
					continue
				}
				gi, ok := cg.callIndexOK(t)
				if !ok {
					info.otherTail = true
					continue
				}
				info.directTails = append(info.directTails, gi)
			case mir.CallValue:
				o := t.Callee.Value
				if o.Kind == mir.OpCopy || o.Kind == mir.OpMove {
					p := o.Place
					switch {
					case isTabIndex(p):
						tabTail[p.Local] = true
						indirectTails = append(indirectTails, tailCallee{isTab: true, local: int(p.Local)})
						continue
					case p.Proj.Len == 0 && !fnBad[p.Local]:
						fnTail[p.Local] = true
						indirectTails = append(indirectTails, tailCallee{isTab: false, local: int(p.Local)})
						continue
					}
				}
				markEscOp(o)
				info.otherTail = true
			default:
				info.otherTail = true
			}
		}
	}
	// Propagate tail/call/escape uses backward and reaching tables forward.
	for changed := true; changed; {
		changed = false
		for _, e := range fnEdges {
			if fnBad[e.from] || fnBad[e.to] {
				continue
			}
			if fnTail[e.to] && !fnTail[e.from] {
				fnTail[e.from] = true
				changed = true
			}
			if fnCall[e.to] && !fnCall[e.from] {
				fnCall[e.from] = true
				changed = true
			}
			if fnEsc[e.to] && !fnEsc[e.from] {
				fnEsc[e.from] = true
				changed = true
			}
		}
	}
	for _, e := range tabEdges {
		if fnBad[e.to] {
			continue
		}
		if fnTail[e.to] {
			tabTail[e.from] = true
		}
		if fnCall[e.to] {
			tabCall[e.from] = true
		}
		if fnEsc[e.to] {
			tabEsc[e.from] = true
		}
	}
	for li, ti := range tabLoc {
		if ti != types.NoItem && tabEsc[li] {
			escaped[ti] = true
		}
	}
	// Forward-propagate the reaching table locals into each eligible fn local
	// so every indirect tail call knows the exact set of tables it can load.
	fnTabs := make([][]int, nloc)
	addTab := func(v, tl int) bool {
		for _, x := range fnTabs[v] {
			if x == tl {
				return false
			}
		}
		fnTabs[v] = append(fnTabs[v], tl)
		return true
	}
	for _, e := range tabEdges {
		if !fnBad[e.to] {
			addTab(e.to, e.from)
		}
	}
	for changed := true; changed; {
		changed = false
		for _, e := range fnEdges {
			if fnBad[e.from] || fnBad[e.to] {
				continue
			}
			for _, tl := range fnTabs[e.from] {
				if addTab(e.to, tl) {
					changed = true
				}
			}
		}
	}
	pureTab := func(tl int) bool {
		return tabLoc[tl] != types.NoItem && tabTail[tl] && !tabCall[tl] && !tabEsc[tl]
	}
	seenTab := map[types.ItemID]bool{}
	addIndirectTable := func(ti types.ItemID) {
		if !seenTab[ti] {
			seenTab[ti] = true
			info.indirectTables = append(info.indirectTables, ti)
		}
	}
	for _, tc := range indirectTails {
		if tc.isTab {
			if !pureTab(tc.local) {
				info.otherTail = true
				continue
			}
			addIndirectTable(tabLoc[tc.local])
			continue
		}
		if fnCall[tc.local] || fnEsc[tc.local] || len(fnTabs[tc.local]) == 0 {
			info.otherTail = true
			continue
		}
		for _, tl := range fnTabs[tc.local] {
			if !pureTab(tl) {
				info.otherTail = true
				continue
			}
			addIndirectTable(tabLoc[tl])
		}
	}
	var pure []types.ItemID
	for li, ti := range tabLoc {
		if pureTab(li) {
			if pure == nil {
				pure = make([]types.ItemID, nloc)
			}
			pure[li] = ti
		}
	}
	info.pureTabLocals = pure
	return info
}

// callIndexOK is callIndex returning (index, false) rather than raising an ICE
// when a callee instance is absent (safe for unit-wide pre-analysis).
func (fg *fnGen) callIndexOK(t *mir.Terminator) (int, bool) {
	g := fg.g
	item := t.Callee.Fn.Item
	it := g.items.Get(item)
	if it == nil {
		return 0, false
	}
	fd, ok := it.Decl.(*types.FuncDecl)
	if !ok {
		return 0, false
	}
	for _, p := range fd.Sig.Params {
		if p.Const {
			return 0, false
		}
	}
	args := fg.b.GenericArgs[t.Callee.Fn.Args.Start:t.Callee.Fn.Args.End()]
	if len(args) > 0 && it.Kind != types.KindLiteral {
		if caller := g.cpuOf(fg.fi); len(caller) > 0 {
			own := g.ownCPU(item)
			extra := minusCPU(unionCPU(caller, own), own, types.CPUBaseline(g.cfg.Target))
			if len(extra) > 0 {
				i, ok := g.u.index().byKey[cpuIndexKey(item, args, extra)]
				return i, ok
			}
		}
	}
	return g.findFunc(item, args)
}
