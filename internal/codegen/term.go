// Why: every MIR block ends in a terminator, and none may become LLVM
// undefined behaviour: a MIR unreachable must panic, not let LLVM assume
// the point is never reached.
//
// What: term (every terminator kind), switchTerm and ret.
//
// How: goto is br; a switch gets an LLVM unreachable default only when its
// cases are total; calls go to call.go, tail calls to tailcall.go; a return
// loads the lowered result; panics and unreachable are panic hook calls.

package codegen

import (
	"mk/internal/abi"
	"mk/internal/llvm"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

func (fg *fnGen) term(t *mir.Terminator) {
	fg.setLoc(t.Span)
	switch t.Kind {
	case mir.TermGoto:
		fg.cur.Br(fg.blocks[t.Target])
	case mir.TermSwitch:
		fg.switchTerm(t)
	case mir.TermCall:
		fg.callTerm(t)
	case mir.TermTailCall:
		fg.tailCallTerm(t) // tailcall.go
	case mir.TermReturn:
		fg.ret()
	case mir.TermPanic:
		if t.Panic == types.PanicBounds && fg.g.cfg.TestNoBoundsChecks {
			fg.cur.Unreachable() // Config.TestNoBoundsChecks: test-only
			return
		}
		if t.HasMsg {
			msg := fg.operand(t.Msg)
			fg.panicCall(t.Panic, &msg)
		} else {
			fg.panicCall(t.Panic, nil)
		}
	case mir.TermUnreachable:
		fg.panicCall(types.PanicUnreachable, nil)
	default:
		source.ICEf(fg.spanOf(t.Span), "codegen: terminator kind %d in an instance body", t.Kind)
	}
}

// switchTerm is br i1 for a bool, else switch on the bit patterns; a NoBlock
// default goes to an unreachable block (the cases are total).
func (fg *fnGen) switchTerm(t *mir.Terminator) {
	m := fg.g.m
	on := fg.operand(t.On)
	if on.kind != rScalar {
		source.ICEf(fg.spanOf(t.Span), "codegen: switch on %s", fg.g.tt.TypeString(on.t, nil))
	}
	cases := fg.b.Cases[t.Cases.Start:t.Cases.End()]
	def := (*llvm.Block)(nil)
	if t.Default != mir.NoBlock {
		def = fg.blocks[t.Default]
	}
	// Successor j of the MIR terminator is case j, then the default (the
	// PGO counter order); counts holds their recorded counts when profiled.
	nsucc := len(cases)
	if def != nil {
		nsucc++
	}
	counts := fg.edgeCounts(nsucc)
	if def != nil {
		def = fg.edge(len(cases), def)
	}
	if on.v.Ty.Bits() == 1 {
		var tb, fb *llvm.Block = def, def
		tMir, fMir := t.Default, t.Default
		tj, fj := len(cases), len(cases) // the edge each side takes, for weights
		for j, c := range cases {
			if c.Value&1 == 1 {
				tb, tj, tMir = fg.edge(j, fg.blocks[c.Target]), j, c.Target
			} else {
				fb, fj, fMir = fg.edge(j, fg.blocks[c.Target]), j, c.Target
			}
		}
		if tb == nil || fb == nil {
			u := fg.newBlock()
			u.Unreachable()
			if tb == nil {
				tb, tj = u, -1
			}
			if fb == nil {
				fb, fj = u, -1
			}
		}
		var w []llvm.Attach
		if counts != nil {
			at := func(j int) uint64 {
				if j < 0 || j >= len(counts) {
					return 0
				}
				return counts[j]
			}
			w = fg.weights([]uint64{at(tj), at(fj)}) // br: true, then false
		} else if fg.coldBlock[tMir] && !fg.coldBlock[fMir] {
			w = []llvm.Attach{fg.trapWeights(true)}
		} else if fg.coldBlock[fMir] && !fg.coldBlock[tMir] {
			w = []llvm.Attach{fg.trapWeights(false)}
		} else if cold, ok := fg.coldGuard[fg.bi]; ok {
			if tMir == cold && fMir != cold {
				w = []llvm.Attach{fg.trapWeights(true)}
			} else if fMir == cold && tMir != cold {
				w = []llvm.Attach{fg.trapWeights(false)}
			}
		}
		fg.cur.CondBr(on.v, tb, fb, w...)
		return
	}
	var dc uint64
	if def == nil {
		def = fg.newBlock()
		def.Unreachable()
	} else if counts != nil {
		dc = counts[len(cases)]
	} else if !fg.hasMixedColdSwitch(cases, t.Default) && fg.tryBitmaskSwitch(on.v, cases, t.Default, def) {
		return
	}
	var lc []llvm.Case
	bits := on.v.Ty.Bits()
	if fg.covered() && (bits == 16 || bits == 32 || bits == 64) && len(cases) > 0 && len(cases) <= 32 {
		wBytes := uint8(bits / 8)
		for _, c := range cases {
			fg.coverCmp(on.v, m.ConstInt(on.v.Ty, c.Value), wBytes)
		}
	}
	for j, c := range cases {
		lc = append(lc, llvm.Case{Val: m.ConstInt(on.v.Ty, c.Value), Target: fg.edge(j, fg.blocks[c.Target])})
	}
	var w []llvm.Attach
	if counts != nil {
		w = fg.weights(append([]uint64{dc}, counts[:len(cases)]...)) // switch: default, then the cases
	} else if fg.hasMixedColdSwitch(cases, t.Default) {
		w = fg.coldSwitchWeights(cases, t.Default)
	}
	fg.cur.Switch(on.v, def, lc, w...)
}

// ret returns local 0: Direct results load their parts, sret and void
// return nothing.
func (fg *fnGen) ret() {
	sig := fg.sig
	if sig.s.Ret.Kind != abi.Direct {
		fg.cur.Ret(nil)
		return
	}
	parts := fg.loadParts(fg.locals[mir.ReturnLocal], sig.result, sig.s.Ret, sig.retArray, true)
	v := parts[0]
	if len(parts) > 1 {
		v = fg.g.m.Poison(sig.ret)
		for k, p := range parts {
			v = fg.cur.InsertValue(v, p, k)
		}
	}
	fg.cur.Ret(&v)
}

// tryBitmaskSwitch lowers a 2-target integer switch (all >= 4 cases branching
// to one target, default to another) spanning at most 128 values to a 1- or
// 2-word constant bitmask test. This eliminates indirect jump tables on
// character-class matches and keeps small predicates inlineable by LLVM.
func (fg *fnGen) tryBitmaskSwitch(on llvm.Value, cases []mir.SwitchCase, defID mir.BlockID, def *llvm.Block) bool {
	if fg.pgo != nil || fg.g.cfg.Cover || fg.g.cfg.Debug || fg.fi < 0 || fg.g.ctModule(fg.fi) {
		return false
	}
	if def == nil || len(cases) < 4 {
		return false
	}
	bits := on.Ty.Bits()
	if bits < 8 || bits > 64 {
		return false
	}
	yesID := cases[0].Target
	if yesID == defID {
		return false
	}
	for _, c := range cases[1:] {
		if c.Target != yesID {
			return false
		}
	}
	valMask := ^uint64(0)
	if bits < 64 {
		valMask = (uint64(1) << bits) - 1
	}
	minV := cases[0].Value & valMask
	maxV := minV
	for _, c := range cases[1:] {
		v := c.Value & valMask
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	span := maxV - minV
	if span >= 128 {
		return false
	}
	var loMask, hiMask uint64
	for _, c := range cases {
		d := (c.Value & valMask) - minV
		if d < 64 {
			loMask |= uint64(1) << d
		} else {
			hiMask |= uint64(1) << (d - 64)
		}
	}
	m := fg.g.m
	i64 := m.I(64)
	x := on
	if bits < 64 {
		x = fg.cur.Cast(llvm.ZExt, x, i64)
	}
	off := x
	if minV != 0 {
		off = fg.cur.Bin(llvm.Sub, x, m.ConstInt(i64, minV), 0)
	}
	inRange := fg.cur.ICmp(llvm.IULE, off, m.ConstInt(i64, span))
	var mask llvm.Value
	if span < 64 {
		mask = m.ConstInt(i64, loMask)
	} else {
		isHi := fg.cur.ICmp(llvm.IUGE, off, m.ConstInt(i64, 64))
		mask = fg.cur.Select(isHi, m.ConstInt(i64, hiMask), m.ConstInt(i64, loMask))
	}
	shft := fg.cur.Bin(llvm.And, off, m.ConstInt(i64, 63), 0)
	bit := fg.cur.Cast(llvm.Trunc, fg.cur.Bin(llvm.LShr, mask, shft, 0), m.I(1))
	hit := fg.cur.Bin(llvm.And, inRange, bit, 0)
	fg.cur.CondBr(hit, fg.blocks[yesID], def)
	return true
}

// findColdGuards identifies nested-loop versioning guard chains (from lower's
// versionNestedBounds) so unprofiled codegen marks their fallback edges cold.
// Marking a 2D fallback loop cold stops LLVM's LoopVectorizer and register
// allocator from bloating/spilling the hot fast nest for the cold fallback,
// while leaving 1D loops (whose guards often take the 0-trip exit) untouched.
func findColdGuards(b *mir.Body) map[int]mir.BlockID {
	nb := len(b.Blocks)
	isHeader := func(id mir.BlockID) bool {
		blk := &b.Blocks[id]
		if len(blk.Stmts) != 1 || blk.Term.Kind != mir.TermSwitch || blk.Term.Cases.Len != 1 || blk.Term.Default == mir.NoBlock {
			return false
		}
		s := &blk.Stmts[0]
		return s.Kind == mir.StAssign && s.Place.Proj.Len == 0 && s.Rvalue.Kind == mir.RvBinary && s.Rvalue.Bin.IsCompare()
	}
	var candEnds []int
	for i := range nb {
		blk := &b.Blocks[i]
		if len(blk.Stmts) == 0 && blk.Term.Kind == mir.TermGoto && isHeader(blk.Term.Target) {
			candEnds = append(candEnds, i)
		}
	}
	if len(candEnds) == 0 {
		return nil
	}
	succ := make([][]int, nb)
	preds := make([][]int, nb)
	for i := range nb {
		for _, s := range b.Successors(mir.BlockID(i), nil) {
			si := int(s)
			succ[i] = append(succ[i], si)
			preds[si] = append(preds[si], i)
		}
	}
	// Compute dominators once to test whether a fallback header encloses an inner loop.
	var idom, rpoPos []int32
	var isNested func(slow mir.BlockID) bool
	isNested = func(slow mir.BlockID) bool {
		if idom == nil {
			idom, rpoPos = mirDominators(nb, succ, preds)
		}
		dom := func(a, c int) bool {
			if rpoPos[a] < 0 || rpoPos[c] < 0 {
				return false
			}
			for x := int32(c); ; x = idom[x] {
				if int(x) == a {
					return true
				}
				if x == 0 {
					return false
				}
			}
		}
		h := int(slow)
		var stack []int
		for _, p := range preds[h] {
			if dom(h, p) {
				stack = append(stack, p)
			}
		}
		if len(stack) == 0 {
			return false
		}
		in := make([]bool, nb)
		in[h] = true
		for len(stack) > 0 {
			u := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if in[u] {
				continue
			}
			in[u] = true
			for _, p := range preds[u] {
				if dom(u, p) {
					return true // u != h is an inner loop header inside slow
				}
				stack = append(stack, p)
			}
		}
		return false
	}

	var out map[int]mir.BlockID
	for _, end := range candEnds {
		fast := b.Blocks[end].Term.Target
		if len(preds[end]) != 1 {
			continue
		}
		cur := preds[end][0]
		child := mir.BlockID(end)
		slow := mir.NoBlock
		var chain []int
		seen := map[int]bool{end: true}
		for !seen[cur] {
			seen[cur] = true
			t := &b.Blocks[cur].Term
			if t.Kind != mir.TermSwitch || t.Cases.Len != 1 || t.Default == mir.NoBlock {
				break
			}
			c0 := b.Cases[t.Cases.Start].Target
			c1 := t.Default
			var other mir.BlockID
			if c0 == child && c1 != child {
				other = c1
			} else if c1 == child && c0 != child {
				other = c0
			} else {
				break
			}
			if slow == mir.NoBlock {
				if other == fast || !isHeader(other) {
					break
				}
				fb, sb := &b.Blocks[fast], &b.Blocks[other]
				if fb.Term.Span != sb.Term.Span || fb.Stmts[0].Rvalue != sb.Stmts[0].Rvalue {
					break
				}
				if !isNested(other) {
					break
				}
				slow = other
			} else if other != slow {
				break
			}
			chain = append(chain, cur)
			if len(preds[cur]) != 1 {
				break
			}
			child = mir.BlockID(cur)
			cur = preds[cur][0]
		}
		if len(chain) > 0 && slow != mir.NoBlock {
			if out == nil {
				out = make(map[int]mir.BlockID, len(chain))
			}
			for _, g := range chain {
				out[g] = slow
			}
		}
	}
	return out
}

func mirDominators(nb int, succ, preds [][]int) (idom, pos []int32) {
	pos = make([]int32, nb)
	idom = make([]int32, nb)
	for i := range nb {
		pos[i], idom[i] = -1, -1
	}
	seen := make([]bool, nb)
	type frame struct{ b, i int }
	var post []int
	stack := []frame{{0, 0}}
	seen[0] = true
	for len(stack) > 0 {
		f := &stack[len(stack)-1]
		if f.i < len(succ[f.b]) {
			s := succ[f.b][f.i]
			f.i++
			if !seen[s] {
				seen[s] = true
				stack = append(stack, frame{s, 0})
			}
			continue
		}
		post = append(post, f.b)
		stack = stack[:len(stack)-1]
	}
	rpo := make([]int, 0, len(post))
	for i := len(post) - 1; i >= 0; i-- {
		pos[post[i]] = int32(len(rpo))
		rpo = append(rpo, post[i])
	}
	idom[0] = 0
	inter := func(x, y int32) int32 {
		for x != y {
			for pos[x] > pos[y] {
				x = idom[x]
			}
			for pos[y] > pos[x] {
				y = idom[y]
			}
		}
		return x
	}
	for changed := true; changed; {
		changed = false
		for _, u := range rpo[1:] {
			nd := int32(-1)
			for _, p := range preds[u] {
				if idom[p] < 0 {
					continue
				}
				if nd < 0 {
					nd = int32(p)
				} else {
					nd = inter(nd, int32(p))
				}
			}
			if nd >= 0 && idom[u] != nd {
				idom[u] = nd
				changed = true
			}
		}
	}
	return idom, pos
}

// findColdBlocks returns every MIR block that exclusively lies on or
// unconditionally leads into a call to `std.runtime.Cold()`.
func findColdBlocks(b *mir.Body, items *types.Items) map[mir.BlockID]bool {
	if b == nil || items == nil || len(b.Blocks) == 0 {
		return nil
	}
	nb := len(b.Blocks)
	isSeed := make([]bool, nb)
	anySeed := false
	for i := range nb {
		t := &b.Blocks[i].Term
		if t.Kind == mir.TermCall && t.Callee.Kind == mir.CallDirect {
			if it := items.Get(t.Callee.Fn.Item); it != nil && it.Key.String() == "std.runtime.Cold" {
				isSeed[i] = true
				anySeed = true
			}
		}
	}
	if !anySeed {
		return nil
	}
	succ := make([][]int, nb)
	for i := range nb {
		for _, s := range b.Successors(mir.BlockID(i), nil) {
			succ[i] = append(succ[i], int(s))
		}
	}
	hotReachable := make([]bool, nb)
	var q []int
	if !isSeed[0] {
		hotReachable[0] = true
		q = append(q, 0)
	}
	for len(q) > 0 {
		u := q[len(q)-1]
		q = q[:len(q)-1]
		for _, v := range succ[u] {
			if !isSeed[v] && !hotReachable[v] {
				hotReachable[v] = true
				q = append(q, v)
			}
		}
	}
	cold := make([]bool, nb)
	var cq []int
	for i := range nb {
		if isSeed[i] {
			cold[i] = true
			cq = append(cq, i)
		}
	}
	for len(cq) > 0 {
		u := cq[len(cq)-1]
		cq = cq[:len(cq)-1]
		for _, v := range succ[u] {
			if !hotReachable[v] && !cold[v] {
				cold[v] = true
				cq = append(cq, v)
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for i := range nb {
			if cold[i] || len(succ[i]) == 0 {
				continue
			}
			all := true
			for _, v := range succ[i] {
				if !cold[v] {
					all = false
					break
				}
			}
			if all {
				cold[i] = true
				changed = true
			}
		}
	}
	out := make(map[mir.BlockID]bool)
	for i, c := range cold {
		if c {
			out[mir.BlockID(i)] = true
		}
	}
	return out
}

func (fg *fnGen) hasMixedColdSwitch(cases []mir.SwitchCase, def mir.BlockID) bool {
	if len(fg.coldBlock) == 0 {
		return false
	}
	hasCold, hasHot := false, false
	mark := func(b mir.BlockID) {
		if b == mir.NoBlock {
			return
		}
		if fg.coldBlock[b] {
			hasCold = true
		} else {
			hasHot = true
		}
	}
	mark(def)
	for _, c := range cases {
		mark(c.Target)
	}
	return hasCold && hasHot
}

func (fg *fnGen) coldSwitchWeights(cases []mir.SwitchCase, def mir.BlockID) []llvm.Attach {
	ws := make([]uint64, 1+len(cases))
	wOf := func(b mir.BlockID) uint64 {
		if b != mir.NoBlock && fg.coldBlock[b] {
			return 1
		}
		return 1048575
	}
	ws[0] = wOf(def)
	for j, c := range cases {
		ws[1+j] = wOf(c.Target)
	}
	return fg.weights(ws)
}
