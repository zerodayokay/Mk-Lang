// Why: lower passes a by-value whole-local argument directly only when every
// later operand is inert; otherwise it moves it first into an argument slot
// `t = Use(Move(x))`, and borrow must treat that slot as the argument.
//
// What: argSlot, findArgSlots, markSlotEnds and restoreSlot.
//
// How: findArgSlots pairs each slot with the call that reads it. On a
// fallible call's error edge the slot's source x gets back what the slot
// took, since the call consumes the argument only on success; an `ends`
// clause on the slot's parameter checks the slot's move with the ends
// exemption.

package borrow

import "mk/internal/mir"

// argSlot is an argument slot: a whole LocalTemp t assigned exactly once in
// the body, by `t = Use(Move(x))` of a whole local or parameter x != t, and
// used exactly once, as the ArgMove operand Move(t) of a call. block and
// stmt locate the slot's assignment (its move site in x's moved set).
type argSlot struct {
	src   rootID
	block mir.BlockID
	stmt  int
	// call is the block whose call reads the slot, arg its argument index.
	call mir.BlockID
	arg  int
}

// findArgSlots fills c.argSlots. Liveness must be computed (the site
// numbers).
func (c *checker) findArgSlots() {
	b := c.body
	n := len(b.Locals)
	defs := make([]uint8, n)
	uses := make([]uint8, n)
	cand := make([]argSlot, n)
	bump := func(x []uint8, l uint32) {
		if int(l) < n && x[l] < 2 {
			x[l]++
		}
	}
	var buf []uint32
	argUse := make([]bool, n)
	type use struct {
		b mir.BlockID
		i int
	}
	useAt := make([]use, n)
	for bi := range b.Blocks {
		blk := &b.Blocks[bi]
		for si := range blk.Stmts {
			s := &blk.Stmts[si]
			var def rootID
			buf, def = c.stmtUses(s, buf[:0])
			for _, u := range buf {
				bump(uses, u)
			}
			if def == noRoot {
				continue
			}
			bump(defs, uint32(def))
			rv := &s.Rvalue
			if rv.Kind == mir.RvUse && rv.A.Kind == mir.OpMove && rv.A.Place.Proj.Len == 0 && rv.A.Place.Local != s.Place.Local {
				cand[def] = argSlot{src: rootID(rv.A.Place.Local), block: mir.BlockID(bi), stmt: si}
			} else {
				cand[def] = argSlot{src: noRoot}
			}
		}
		t := &blk.Term
		var def rootID
		buf, def = c.termUses(t, buf[:0])
		for _, u := range buf {
			bump(uses, u)
		}
		if def != noRoot {
			bump(defs, uint32(def))
			cand[def] = argSlot{src: noRoot}
		}
		if isCall(t) {
			for ai, a := range b.Args[t.Args.Start:t.Args.End()] {
				if a.Mode == mir.ArgMove && a.Operand.Kind == mir.OpMove && a.Operand.Place.Proj.Len == 0 {
					argUse[a.Operand.Place.Local] = true
					useAt[a.Operand.Place.Local] = use{mir.BlockID(bi), ai}
				}
			}
		}
	}
	for l := range n {
		if b.Locals[l].Kind != mir.LocalTemp || defs[l] != 1 || uses[l] != 1 || !argUse[l] || cand[l].src == noRoot {
			continue
		}
		if c.argSlots == nil {
			c.argSlots = make(map[mir.LocalID]argSlot)
		}
		sl := cand[l]
		sl.call, sl.arg = useAt[l].b, useAt[l].i
		c.argSlots[mir.LocalID(l)] = sl
		c.markSlotEnds(sl)
	}
}

// markSlotEnds is the ends rule at a call for an argument passed through a
// slot: when the call that reads slot sl ends the slot's parameter,
// the slot's own statement `t = Use(Move(x))` is where x moves, so that move
// is checked with the exemption for x, as the whole-local ArgMove of x would
// be at the call. A spawned call ends nothing and exempts nothing.
// What the call clears is unchanged: the move already emptied H(x) and H(t)
// is the value passed. x is never an RvRef temporary in lowered MIR: lower
// slots only an operand whose read is late (lower's readsLate is false for
// a whole temporary), and a `mut self` receiver is a pass-2 borrow form, so
// its RvRef formation is the endsForm site markEndsForms records.
func (c *checker) markSlotEnds(sl argSlot) {
	t := &c.body.Blocks[sl.call].Term
	if !isCall(t) || t.Spawn != mir.NoLocal {
		return
	}
	fv := c.peekFlows(t)
	if fv == nil {
		return
	}
	for _, p := range fv.ends {
		if int(p)-1 == sl.arg {
			c.endsForm[siteKey{sl.block, uint32(sl.stmt)}] = sl.src
			return
		}
	}
}

// restoreSlot is the error-edge restore through slot sl (a fallible call
// consumes a by-value argument only on success): the source x gets back what
// the slot took (t's init and held set as they stand at the call, before its
// operands), and the slot's move site leaves x's moved and changed sets; t
// stays moved.
func (c *checker) restoreSlot(es *state, sl argSlot, t entry) {
	site := c.live.base[sl.block] + uint32(sl.stmt)
	one := c.sets.of(site)
	x := es.get(sl.src)
	x.init = initOf(t)
	x.cover = t.cover
	x.held = t.held
	x.fv = t.fv // the restore carries the field sets and the mode
	x.sh = t.sh // and the shallow marks, which belong to that held set
	x.stored = t.stored
	x.moved = c.sets.minus(x.moved, one)
	x.changed = c.sets.minus(x.changed, one)
	c.put(es, sl.src, x)
}
