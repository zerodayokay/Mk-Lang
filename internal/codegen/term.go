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
	}
	var lc []llvm.Case
	for j, c := range cases {
		lc = append(lc, llvm.Case{Val: m.ConstInt(on.v.Ty, c.Value), Target: fg.edge(j, fg.blocks[c.Target])})
	}
	var w []llvm.Attach
	if counts != nil {
		w = fg.weights(append([]uint64{dc}, counts[:len(cases)]...)) // switch: default, then the cases
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
