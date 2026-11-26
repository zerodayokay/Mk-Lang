// Why: a loan matters only while some root that holds it is still used, so
// the checker needs root liveness at every access, cheaply.
//
// What: liveness and computeLiveness, liveBefore, the use and def tables of
// statements, rvalues and terminators, and nextUse.
//
// How: liveness per root, backward over blocks to a fixed point; live sets
// are hash-consed root sets, one per block boundary, never per statement.
// Inside a block, whether a root is live before a statement is answered by
// its next occurrence in the block (a use: live; a def: dead; none: live out
// of the block), so no per-statement set is built: a body with k roots live
// across m statements would otherwise intern k·m elements.

package borrow

import (
	"mk/internal/mir"
)

// liveness holds, per block, the uses and def of each statement and
// terminator (index len(Stmts)), the block's gen and kill sets, its live-in
// and live-out sets, and, built on first query, each root's occurrences.
type liveness struct {
	// base numbers every statement and terminator densely: statement i of
	// block b is site base[b]+i, its terminator base[b]+len(Stmts).
	base []uint32
	uses [][]setID
	defs [][]rootID
	gen  []setID
	kill []setID
	in   []setID
	out  []setID
	occ  []map[rootID][]occurrence
}

// occurrence is a root's appearance at statement pos of a block: a use, or
// a def with no use there.
type occurrence struct {
	pos int32
	use bool
}

func (c *checker) computeLiveness() *liveness {
	b := c.body
	n := len(b.Blocks)
	lv := &liveness{base: make([]uint32, n), uses: make([][]setID, n), defs: make([][]rootID, n),
		gen: make([]setID, n), kill: make([]setID, n), in: make([]setID, n), out: make([]setID, n),
		occ: make([]map[rootID][]occurrence, n)}
	var next uint32
	var buf, gen, kill []uint32
	seen := make(map[rootID]bool)
	for bi := range b.Blocks {
		blk := &b.Blocks[bi]
		lv.base[bi] = next
		next += uint32(len(blk.Stmts)) + 1
		us := make([]setID, len(blk.Stmts)+1)
		ds := make([]rootID, len(blk.Stmts)+1)
		for i := range blk.Stmts {
			buf = buf[:0]
			buf, ds[i] = c.stmtUses(&blk.Stmts[i], buf)
			us[i] = c.sets.fromUnsorted(buf)
		}
		buf = buf[:0]
		buf, ds[len(blk.Stmts)] = c.termUses(&blk.Term, buf)
		us[len(blk.Stmts)] = c.sets.fromUnsorted(buf)
		lv.uses[bi], lv.defs[bi] = us, ds
		// gen: used before any def in the block; kill: defined in it.
		clear(seen)
		gen, kill = gen[:0], kill[:0]
		for j := range us {
			for _, u := range c.sets.get(us[j]) {
				if !seen[rootID(u)] {
					seen[rootID(u)] = true
					gen = append(gen, u)
				}
			}
			if d := ds[j]; d != noRoot {
				// A def of the whole root defines every field.
				for _, x := range c.defElems(d) {
					if !seen[rootID(x)] {
						seen[rootID(x)] = true
						kill = append(kill, x)
					}
				}
			}
		}
		lv.gen[bi] = c.sets.fromUnsorted(gen)
		lv.kill[bi] = c.sets.fromUnsorted(kill)
	}
	// Backward fixed point over the reachable blocks, in postorder:
	// in = gen ∪ (out \ kill).
	var succ []mir.BlockID
	for changed := true; changed; {
		changed = false
		for i := len(c.rpo) - 1; i >= 0; i-- {
			bb := c.rpo[i]
			o := emptySet
			succ = b.Successors(bb, succ[:0])
			for _, s := range succ {
				o = c.sets.unite(o, lv.in[s])
			}
			lv.out[bb] = o
			l := c.sets.unite(lv.gen[bb], c.sets.minus(o, lv.kill[bb]))
			if l != lv.in[bb] {
				lv.in[bb] = l
				changed = true
			}
		}
	}
	return lv
}

// liveBefore reports whether root r is live before statement i of block b
// (index len(Stmts) is the terminator): its next occurrence at or after i is
// a use, or it has none and r is live out of b. Live-before of an access
// includes the access's own uses.
func (c *checker) liveBefore(r rootID, b mir.BlockID, i int) bool {
	lv := c.live
	occ := lv.occ[b]
	if occ == nil {
		occ = make(map[rootID][]occurrence)
		for j := range lv.uses[b] {
			for _, u := range c.sets.get(lv.uses[b][j]) {
				occ[rootID(u)] = append(occ[rootID(u)], occurrence{pos: int32(j), use: true})
			}
			if d := lv.defs[b][j]; d != noRoot {
				for _, x := range c.defElems(d) {
					os := occ[rootID(x)]
					if k := len(os); k == 0 || os[k-1].pos != int32(j) {
						occ[rootID(x)] = append(os, occurrence{pos: int32(j)})
					}
				}
			}
		}
		lv.occ[b] = occ
	}
	os := occ[r]
	lo, hi := 0, len(os)
	for lo < hi {
		mid := (lo + hi) / 2
		if os[mid].pos < int32(i) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(os) {
		return os[lo].use
	}
	return c.sets.has(lv.out[b], uint32(r))
}

// placeUses appends the roots a read of place pl uses: its root and every
// index local.
func (c *checker) placeUses(pl mir.Place, buf []uint32, withRoot bool) []uint32 {
	if withRoot {
		buf = append(buf, uint32(pl.Local))
		buf = c.fieldUses(pl, buf)
	}
	for _, pr := range c.body.Proj(pl) {
		if pr.Kind == mir.ProjIndex {
			buf = append(buf, pr.A)
		}
	}
	return buf
}

func (c *checker) operandUses(o mir.Operand, buf []uint32) []uint32 {
	if o.Kind == mir.OpCopy || o.Kind == mir.OpMove {
		return c.placeUses(o.Place, buf, true)
	}
	return buf
}

// hasDeref reports whether a place has a deref step (or an index through a
// slice, which is one).
func (c *checker) hasDeref(pl mir.Place) bool {
	return c.paths.get(c.paths.ofPlace(c.body, c.tab, pl)).derefs > 0
}

// stmtUses is the liveness table for a statement: its uses, and its def (a whole
// local assigned), or noRoot.
func (c *checker) stmtUses(st *mir.Statement, buf []uint32) ([]uint32, rootID) {
	switch st.Kind {
	case mir.StAssign:
		buf = c.rvalueUses(&st.Rvalue, buf)
		if st.Place.Proj.Len == 0 {
			return buf, rootID(st.Place.Local)
		}
		buf = c.placeUses(st.Place, buf, c.hasDeref(st.Place))
		return buf, noRoot
	case mir.StJoin:
		return c.fieldUses(mir.LocalPlace(st.Local), append(buf, uint32(st.Local))), noRoot
	case mir.StConsume:
		return c.operandUses(mir.Move(st.Place), buf), noRoot
	case mir.StAsm:
		// Inline asm: each In is a use; each Out place is written. An asm may
		// write several whole locals and the table has one def, so the
		// writes count as uses: liveness only grows, and an asm operand is
		// plain, so it holds no loan whose life this extends.
		for _, op := range c.body.Asms[st.Asm].Operands {
			switch op.Kind {
			case mir.AsmIn, mir.AsmInOut, mir.AsmInLateOut:
				buf = c.operandUses(op.In, buf)
			}
			switch op.Kind {
			case mir.AsmOut, mir.AsmLateOut, mir.AsmInOut, mir.AsmInLateOut:
				if op.Out.Local != mir.NoLocal {
					buf = c.placeUses(op.Out, buf, true)
				}
			}
		}
		return buf, noRoot
	}
	return buf, noRoot
}

func (c *checker) rvalueUses(rv *mir.Rvalue, buf []uint32) []uint32 {
	switch rv.Kind {
	case mir.RvUse, mir.RvUnary, mir.RvCast, mir.RvRepeat:
		buf = c.operandUses(rv.A, buf)
	case mir.RvBinary:
		buf = c.operandUses(rv.A, buf)
		buf = c.operandUses(rv.B, buf)
	case mir.RvRef, mir.RvLen, mir.RvDiscriminant:
		buf = c.placeUses(rv.Place, buf, true)
	case mir.RvSlice:
		buf = c.placeUses(rv.Place, buf, true)
		if rv.HasLo {
			buf = c.operandUses(rv.A, buf)
		}
		if rv.HasHi {
			buf = c.operandUses(rv.B, buf)
		}
	case mir.RvAggregate:
		for _, o := range c.body.Operands[rv.Ops.Start:rv.Ops.End()] {
			buf = c.operandUses(o, buf)
		}
	}
	return buf
}

// termUses is the liveness table for a terminator.
func (c *checker) termUses(t *mir.Terminator, buf []uint32) ([]uint32, rootID) {
	switch t.Kind {
	case mir.TermCall, mir.TermTailCall:
		// A tail call's Dest is the zero Place, _0: the callee's result is
		// the body's (tailcall.go).
		for _, a := range c.body.Args[t.Args.Start:t.Args.End()] {
			buf = c.operandUses(a.Operand, buf)
		}
		if t.Callee.Kind == mir.CallValue {
			buf = c.operandUses(t.Callee.Value, buf)
		}
		if t.Spawn != mir.NoLocal {
			buf = append(buf, uint32(t.Spawn))
		}
		if t.Dest.Proj.Len == 0 {
			return buf, rootID(t.Dest.Local)
		}
		return c.placeUses(t.Dest, buf, c.hasDeref(t.Dest)), noRoot
	case mir.TermSwitch:
		return c.operandUses(t.On, buf), noRoot
	case mir.TermReturn:
		return c.fieldUses(mir.LocalPlace(0), append(buf, 0)), noRoot
	case mir.TermPanic:
		if t.HasMsg {
			return c.operandUses(t.Msg, buf), noRoot
		}
	}
	return buf, noRoot
}

// nextUse is the span of root r's next use at or after statement i of block
// b, in this block, or NoSpan (the diagnostic's kept_live_by).
func (c *checker) nextUse(r rootID, b mir.BlockID, i int) mir.SpanID {
	if !c.liveBefore(r, b, i) {
		return mir.NoSpan
	}
	for _, o := range c.live.occ[b][r] {
		if int(o.pos) >= i && o.use {
			blk := &c.body.Blocks[b]
			if int(o.pos) >= len(blk.Stmts) {
				return blk.Term.Span
			}
			return blk.Stmts[o.pos].Span
		}
	}
	return mir.NoSpan
}
