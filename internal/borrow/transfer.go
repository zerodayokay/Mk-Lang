// Why: every MIR statement changes the move and loan state of the roots it
// touches; this is the transfer function the fixpoint and the replay run.
//
// What: held and behind (H and B), setHeld and the holders indexes, kill
// and purge, statement, asm, valueH, operand and the operand table, e0409,
// lenRead, rvalueFV and rvalueOne (the rvalue table), and slice.
//
// How: each statement reads its operands (checking init and conflicts),
// computes the set its rvalue holds, and writes it to the destination.
// setHeld is the one primitive that writes a held set and keeps the
// body-wide holders index a superset, so a kill or purge subtracts from
// exactly the roots that may hold the loan.

package borrow

import (
	"slices"

	"mk/internal/diag"
	"mk/internal/mir"
	"mk/internal/types"
)

// held is H(r). An abstract node holds {p.inner} and, like a local, what
// the body has stored into it on the way to this point: after `h.s = x`, a
// deref of `h.s.a` reaches x's own node, so the body's write of x's loan
// through it is judged as a store of x into x (the self-reach through a
// forwarding parameter). While the self-reach rule's inferred stores run
// (csrConst), the node reads as {p.inner} alone.
func (c *checker) held(st *state, r rootID) setID {
	ri := &c.roots[r]
	if ri.kind == rkAbstract {
		in := c.params[ri.param].innerSet
		if h := st.get(r).held; h != emptySet && !c.csrConst {
			return c.sets.unite(in, h)
		}
		return in
	}
	return st.get(r).held
}

// behind is B(S): the union of H(t) over the targets t of the loans in S.
// A loan of an allocator, one whose target is an allocator root (a local
// Arena, a parameter's arena node) or whose place is an allocator (`&*a` of
// `a: mut Arena`), is kept as well: what is read back out of an allocator's
// memory borrows the allocator, so it cannot be freed or reset under the
// value. The allocator self filter (allocSelf) drops a value's loan of the
// allocator when the value is stored into the allocator's own memory, and
// memSelf drops it in a callee's body, where the caller cannot see it; the
// read side keeps it for every value, whether or not that value views the
// allocator.
//
// A static loan of a carrier static node (a named global whose type holds a
// reference, or the global node) is kept too: a static node holds nothing
// (setHeld), so without it a reference read straight through a reference into
// global memory would hold no loan, and a store through it would land nowhere
// (`m := *p` with p from `GO.Get(io, f)` of a global
// `sync.Once[&Mutex[?&i32]]`). It is static, so it never conflicts; a store of
// a mortal loan through the value is then E0605, as through the global itself.
func (c *checker) behind(st *state, s setID) setID {
	return c.behindKeep(st, s, true)
}

// behindKeep is behind, keeping the allocator loans only when alloc is set.
// The call rule's step 4 reads `*q into p` without them when q refers to an
// allocator itself (`a: &Arena`, `mut cur` of an Arena), since `*q` is then
// the allocator's handle, not its memory: a value the callee reads out of the
// memory borrows the arena by the keep inside the callee, so storing it into
// p needs `q into p`, E0604 without (std's Swap of two arenas, a
// `*a into xs` callee).
func (c *checker) behindKeep(st *state, s setID, alloc bool) setID {
	out := emptySet
	var keep []uint32
	for _, l := range c.sets.get(s) {
		lo := &c.loans[l]
		if t := lo.target; t != noRoot {
			h := c.held(st, t)
			if ti := &c.roots[t]; alloc && ti.allocator && ti.kind != rkAbstract {
				// What is read out of an allocator's memory is what was
				// stored into it, never the loans the allocator holds by its
				// making (a sub-arena's parent, an arena's heap): the value
				// borrows the allocator itself (the keep below).
				h = c.sets.minus(h, c.allocMaking(st, t))
			}
			// Stepping behind a tagged noAlloc loan (such as `sys.args: []str`
			// beside `sys.arena`) stays noAlloc on the parameter's own inner
			// loop: nothing reached through `[]str` can be an allocator.
			if lo.noAlloc && lo.param != types.NoParam && int(lo.param) < len(c.params) {
				if pi := uint32(c.params[lo.param].inner); c.sets.has(h, pi) {
					h = c.sets.unite(c.sets.minus(h, c.sets.of(pi)), c.sets.of(l))
				}
			}
			out = c.sets.unite(out, h)
			if ti := &c.roots[t]; alloc && ti.allocator || lo.static && ti.kind == rkStatic && ti.carrier {
				keep = append(keep, l)
				continue
			}
		}
		if alloc && lo.place != noPath && c.w.factsOf(c.paths.get(lo.place).typ()).Allocator {
			keep = append(keep, l)
		}
	}
	if len(keep) > 0 {
		out = c.sets.unite(out, c.sets.fromUnsorted(keep))
	}
	return out
}

// setHeld is the one primitive that writes a held set: it
// adds r to holders(root(l)) for each added loan l that has a place, so the
// holders index stays a superset. Static nodes are constants; an abstract
// node's entry is what the body stored into it (read through held); a
// non-carrier holds nothing.
func (c *checker) setHeld(st *state, r rootID, s setID) {
	ri := &c.roots[r]
	if ri.kind == rkStatic {
		return
	}
	if !ri.carrier {
		s = emptySet
	}
	e := st.get(r)
	if e.held == s && e.sh == emptySet && e.stored == emptySet {
		return
	}
	added := c.sets.minus(s, e.held)
	e.held = s
	e.sh = emptySet // a write of the whole set is deep (marks.go)
	e.stored = emptySet
	c.put(st, r, e)
	if c.report {
		c.noteStores(r, added)
	}
	c.indexHolders(st, r, added)
}

// indexHolders adds r to holders(root(l)) for every loan l of added with a
// place. The index is one per body, not per state: a superset over every
// state of the check of the roots that held a loan rooted at x, so a kill
// or purge that subtracts from each of them gives exactly the held sets of
// the full scrub, and adding to it costs O(1) amortized where a per-state
// set costs its own size.
func (c *checker) indexHolders(st *state, r rootID, added setID) {
	for _, l := range c.sets.get(added) {
		lo := &c.loans[l]
		if lo.place == noPath {
			if lo.abstract && lo.target != noRoot {
				c.indexAbsHolder(lo.target, r)
			}
			continue
		}
		pr := c.paths.get(lo.place).root
		key := uint64(pr)<<32 | uint64(r)
		pos, ok := c.holderPos[key]
		if !ok {
			for int(pr) >= len(c.holders) {
				c.holders = append(c.holders, nil)
			}
			pos = int32(len(c.holders[pr]))
			c.holderPos[key] = pos
			c.holders[pr] = append(c.holders[pr], r)
		}
		c.indexLoanHolder(loanID(l), pr, r, pos)
	}
}

// indexLoanHolder adds r, at position pos of holders[pr], to
// loanHolders[l], keeping the list in holders[pr]'s order.
func (c *checker) indexLoanHolder(l loanID, pr, r rootID, pos int32) {
	key := uint64(l)<<32 | uint64(r)
	if _, ok := c.loanHolderSeen[key]; ok {
		return
	}
	c.loanHolderSeen[key] = struct{}{}
	for int(l) >= len(c.loanHolders) {
		c.loanHolders = append(c.loanHolders, nil)
	}
	hs := c.loanHolders[l]
	at := len(hs)
	for at > 0 && c.holderPos[uint64(pr)<<32|uint64(hs[at-1])] > pos {
		at--
	}
	c.loanHolders[l] = slices.Insert(hs, at, r)
}

// loanHoldersOf is the holders index of loan l: a superset of the roots
// holding l in any state.
func (c *checker) loanHoldersOf(l loanID) []rootID {
	if int(l) < len(c.loanHolders) {
		return c.loanHolders[l]
	}
	return nil
}

// indexAbsHolder records that root r held an abstract loan whose target is
// the abstract node t: a superset, per body, of the roots through which t's
// held set is reached (absHoldersOf). An abstract node holds what the body
// stored into it, a loan with a place among them, so the liveness search
// reaches such a loan through the node.
func (c *checker) indexAbsHolder(t, r rootID) {
	if c.absHolders == nil {
		c.absHolders = map[rootID][]rootID{}
	}
	key := uint64(t)<<32 | uint64(r)
	if _, ok := c.absHolderSeen[key]; ok {
		return
	}
	if c.absHolderSeen == nil {
		c.absHolderSeen = map[uint64]struct{}{}
	}
	c.absHolderSeen[key] = struct{}{}
	c.absHolders[t] = append(c.absHolders[t], r)
}

// absHoldersOf is the roots that held an abstract loan targeting t.
func (c *checker) absHoldersOf(t rootID) []rootID { return c.absHolders[t] }

// absTargeting is the abstract loans whose target is the abstract node t
// (p.outer, p.inner, p.outerA and the tagged inner loans), cached until a
// loan is added (a tagged inner loan is made when first read).
func (c *checker) absTargeting(t rootID) setID {
	if len(c.loans) != c.absTargetsAt {
		c.absTargets, c.absTargetsAt = nil, len(c.loans)
	}
	if s, ok := c.absTargets[t]; ok {
		return s
	}
	var xs []uint32
	for i := range c.loans {
		if lo := &c.loans[i]; lo.abstract && lo.place == noPath && lo.target == t {
			xs = append(xs, uint32(i))
		}
	}
	s := c.sets.fromUnsorted(xs)
	if c.absTargets == nil {
		c.absTargets = map[rootID]setID{}
	}
	c.absTargets[t] = s
	return s
}

// holdersOf is the holders index of root x.
func (c *checker) holdersOf(x rootID) []rootID {
	if int(x) < len(c.holders) {
		return c.holders[x]
	}
	return nil
}

// addHeld is H(r) ∪= s.
func (c *checker) addHeld(st *state, r rootID, s setID) {
	if s == emptySet {
		return
	}
	if e := st.get(r); e.fv != 0 {
		// A store into a split root lands in no known field, and may add
		// a loan held already by another field: the root becomes whole
		// (fields.go).
		e.fv = 0
		c.put(st, r, e)
	}
	old, oldSh, oldStored := c.held(st, r), st.get(r).sh, st.get(r).stored
	c.setHeld(st, r, c.sets.unite(old, s))
	c.markStore(st, r, old, oldSh, s)
	if keep := c.sets.minus(oldStored, s); keep != emptySet {
		e := st.get(r)
		e.stored = c.sets.meet(keep, e.held)
		c.put(st, r, e)
	}
}

// loansOn lists, per root, the loans whose place is rooted there, in loan
// order; built lazily once all loans exist.
func (c *checker) loansOn(r rootID) []loanID {
	if c.byRoot == nil {
		c.byRoot = make(map[rootID][]loanID)
		for i := range c.loans {
			if c.loans[i].place != noPath {
				pr := c.paths.get(c.loans[i].place).root
				c.byRoot[pr] = append(c.byRoot[pr], loanID(i))
			}
		}
	}
	return c.byRoot[r]
}

// killSet is the loans whose place is p's steps followed by a deref, cached
// per path.
func (c *checker) killSet(p pathID) setID {
	if s, ok := c.kills[p]; ok {
		return s
	}
	pi := c.paths.get(p)
	var xs []uint32
	for _, l := range c.loansOn(pi.root) {
		li := c.paths.get(c.loans[l].place)
		if len(li.steps) <= len(pi.steps) || li.steps[len(pi.steps)].kind != stDeref {
			continue
		}
		ok := true
		for i := range pi.steps {
			if !li.steps[i].equalAt(pi.steps[i]) {
				ok = false
				break
			}
		}
		if ok {
			xs = append(xs, uint32(l))
		}
	}
	s := c.sets.fromUnsorted(xs)
	c.kills[p] = s
	return s
}

// kill removes killSet(p) from the held set of every root in holders(root).
func (c *checker) kill(st *state, p pathID) {
	ks := c.killSet(p)
	if ks == emptySet {
		return
	}
	root := c.paths.get(p).root
	for _, h := range c.holdersOf(root) {
		e := st.get(h)
		if c.sets.intersects(e.held, ks) {
			e.held = c.sets.minus(e.held, ks)
			c.put(st, h, e)
		}
	}
}

// rootedSet is the loans whose place root is x (for purge), cached.
func (c *checker) rootedSet(x rootID) setID {
	if s, ok := c.rooted[x]; ok {
		return s
	}
	var xs []uint32
	for _, l := range c.loansOn(x) {
		xs = append(xs, uint32(l))
	}
	s := c.sets.fromUnsorted(xs)
	c.rooted[x] = s
	return s
}

// purge ends x: subtract rootedSet(x) from every holder's
// set, then drop x's entry, which then reads as unassigned.
func (c *checker) purge(st *state, x rootID) {
	rs := c.rootedSet(x)
	if rs != emptySet {
		for _, h := range c.holdersOf(x) {
			if h == x {
				continue
			}
			e := st.get(h)
			if c.sets.intersects(e.held, rs) {
				e.held = c.sets.minus(e.held, rs)
				c.put(st, h, e)
			}
		}
	}
	c.put(st, x, entry{})
}

// statement is the transfer of one statement: the rvalue's effects on
// its operands, then the destination's.
func (c *checker) statement(st *state, ctx *stmtCtx, s *mir.Statement) {
	switch s.Kind {
	case mir.StAssign:
		ctx.bad409 = false
		// An ends clause through an argument slot: the slot's move of
		// x is the ends argument's formation, checked with x's exemption.
		if r, ok := c.endsForm[siteKey{ctx.b, uint32(ctx.i)}]; ok && c.report && s.Rvalue.Kind == mir.RvUse {
			ctx.exempt = c.exemption(st, ctx, r)
		}
		stored := c.rvalueStored(st, &s.Rvalue)
		S, fv := c.rvalueFV(st, ctx, s)
		ctx.exempt = nil
		c.destinationFV(st, ctx, s.Place, S, fv)
		if stored != emptySet && s.Place.Proj.Len == 0 {
			r := rootID(s.Place.Local)
			e := st.get(r)
			e.stored = c.sets.meet(stored, e.held)
			c.put(st, r, e)
		}
		if ctx.bad409 && s.Place.Proj.Len == 0 {
			// Error hygiene: a linear local that received
			// the value of an E0409 read is marked moved with no move site,
			// so its scope end adds no E0402 and its uses no E0401
			// (`for r in rs` over []Res is E0409 alone).
			if r := rootID(s.Place.Local); c.roots[r].linear && c.roots[r].isLocal() && r != 0 {
				e := st.get(r)
				e.init = initM
				c.put(st, r, e)
				c.setHeld(st, r, emptySet)
			}
		}
	case mir.StAsm:
		c.asm(st, ctx, &c.body.Asms[s.Asm])
	case mir.StScopeEnd:
		c.scopeEnd(st, ctx, s.Local)
	case mir.StJoin, mir.StNop:
		// StJoin is a use of s (liveness); its transfer changes nothing:
		// the scope's loans end at s's scope end.
	}
}

// asm is StAsm's transfer, as a call of a bodiless extern: every In operand is
// read, left to right (a use, ending no loan: E0401, E0403 and the read checks
// of any operand), then every Out place is assigned as by =, with H(e) := {}
// (E0502 under a live loan, E0404 for a non-var local already assigned, the
// first assignment for one not yet assigned). The statement creates no loan
// and ends none; memory reached through raw-pointer operands is not tracked,
// as for an extern call.
func (c *checker) asm(st *state, ctx *stmtCtx, a *mir.Asm) {
	for _, op := range a.Operands {
		switch op.Kind {
		case mir.AsmIn, mir.AsmInOut, mir.AsmInLateOut:
			c.operand(st, ctx, op.In)
		}
	}
	for _, op := range a.Operands {
		switch op.Kind {
		case mir.AsmOut, mir.AsmLateOut, mir.AsmInOut, mir.AsmInLateOut:
			if op.Out.Local != mir.NoLocal {
				c.destination(st, ctx, op.Out, emptySet)
			}
		}
	}
}

// valueH is the set a read of place p yields: H(root) with no deref,
// else tag(B(H(root)), p); empty for a non-carrier type.
func (c *checker) valueH(st *state, p pathID) setID {
	pi := c.paths.get(p)
	if !c.w.carrier(pi.typ()) {
		return emptySet
	}
	if s, _, ok := c.splitRead(st, p); ok {
		if pi.derefs == 0 {
			return c.outerAlloc(s, p) // a by-value parameter's allocator field
		}
		return s // a field of a split root
	}
	h := c.held(st, pi.root)
	if pi.derefs == 0 {
		return c.outerAlloc(h, p)
	}
	return c.tag(c.behind(st, h), p)
}

// outerAlloc replaces p.outer in s by p.outerA when path p reads, with no
// deref, a field of parameter p's own value whose references all refer to
// allocators (allocRefs): c.a of a by-value c: Ctx { a: &Arena, name: str }.
// The loan read stays with the first-level allocator node p#1A, as a tagged
// inner loan read from an allocator field stays with p#A.
func (c *checker) outerAlloc(s setID, p pathID) setID {
	pi := c.paths.get(p)
	if len(pi.steps) == 0 || pi.root >= rootID(len(c.body.Locals)) {
		return s
	}
	ri := &c.roots[pi.root]
	if ri.kind != rkParam || ri.param == types.NoParam || int(ri.param) >= len(c.params) {
		return s
	}
	pp := &c.params[ri.param]
	if !pp.carrier || pp.outerA == noLoan {
		return s
	}
	last := types.NoType
	for j, st := range pi.steps {
		if st.kind == stVariant {
			break
		}
		if st.kind == stField {
			last = pi.types[j]
		}
	}
	if last == types.NoType || !c.w.allocRefs(last) {
		return s
	}
	var out []uint32
	changed := false
	for _, l := range c.sets.get(s) {
		if loanID(l) == pp.outer {
			out = append(out, uint32(pp.outerA))
			changed = true
			continue
		}
		out = append(out, l)
	}
	if !changed {
		return s
	}
	return c.sets.fromUnsorted(out)
}

// tag replaces each untagged p.inner in s by the tagged loan for the field
// path between p's first deref and the next one, index steps skipped
// When the last field's references all refer to allocators, the tagged
// loan targets p#A.
func (c *checker) tag(s setID, p pathID) setID {
	pi := c.paths.get(p)
	var fields []uint32
	last := types.NoType
	first := pi.firstDeref()
	for j := first + 1; j < len(pi.steps); j++ {
		st := pi.steps[j]
		if st.kind == stDeref || st.kind == stVariant {
			break
		}
		if st.kind == stField {
			fields = append(fields, st.a)
			last = pi.types[j]
		}
	}
	if len(fields) == 0 {
		return s
	}
	alloc := last != types.NoType && c.w.allocRefs(last)
	shared := false
	if ri := &c.roots[pi.root]; ri.kind == rkParam && c.hiddenParam(ri.param) && last != types.NoType && c.tab.Kind(last) == types.KRef {
		// A read-only capture in a mut R environment holds &T (CapShared);
		// its inner loan is non-exclusive and, when T itself only references
		// allocators (such as captured a: &Arena stored as &(&Arena)), stays
		// with p#A.
		shared = true
		alloc = alloc || c.w.allocRefs(c.tab.Get(last).Elem)
	}
	noAlloc := last != types.NoType && !alloc && !c.w.reachesAlloc(last)
	var out []uint32
	changed := false
	for _, l := range c.sets.get(s) {
		lo := &c.loans[l]
		if lo.abstract && lo.inner && lo.tag == nil {
			out = append(out, uint32(c.tagged(lo.param, fields, alloc, shared, noAlloc)))
			changed = true
			continue
		}
		out = append(out, l)
	}
	if !changed {
		return s
	}
	return c.sets.fromUnsorted(out)
}

// operandFV is operand with the value's per-field sets (fields.go): a whole
// copy or move of a split root, or a read or take through payload pairs only,
// keeps its vector; 0 otherwise. The vector is read before the operand's
// effects (a move empties the root).
func (c *checker) operandFV(st *state, ctx *stmtCtx, o mir.Operand) (setID, fvID) {
	fv := c.operandVector(st, o)
	s := c.operand(st, ctx, o)
	if s == emptySet {
		fv = 0 // nothing held (E0409's recovery, a non-carrier): no field holds anything
	}
	return s, fv
}

// rvalueStored is the content-stored set an rvalue carries from its operands,
// read before the rvalue's effects move them.
func (c *checker) rvalueStored(st *state, rv *mir.Rvalue) setID {
	switch rv.Kind {
	case mir.RvUse:
		_, s := c.operandHeldStored(st, rv.A)
		return s
	case mir.RvCast:
		if rv.Cast == mir.CastNarrow {
			_, s := c.operandHeldStored(st, rv.A)
			return s
		}
	case mir.RvAggregate:
		switch rv.Agg {
		case mir.AggSome, mir.AggOk:
			if rv.Ops.Len == 1 {
				_, s := c.operandHeldStored(st, c.body.Operands[rv.Ops.Start])
				return s
			}
		case mir.AggStruct, mir.AggRecord:
			var stored, prov setID
			for _, o := range c.body.Operands[rv.Ops.Start:rv.Ops.End()] {
				h, s := c.operandHeldStored(st, o)
				stored = c.sets.unite(stored, s)
				prov = c.sets.unite(prov, c.sets.minus(h, s))
			}
			return c.sets.minus(stored, prov)
		}
	}
	return emptySet
}

// operandHeldStored is the held and content-stored sets a copy or move of o
// carries before its effects: the root's own stored set for the whole root or
// a path of payload pairs only; emptySet for anything else.
func (c *checker) operandHeldStored(st *state, o mir.Operand) (setID, setID) {
	if o.Kind != mir.OpCopy && o.Kind != mir.OpMove {
		return emptySet, emptySet
	}
	p := c.paths.ofPlace(c.body, c.tab, o.Place)
	pi := c.paths.get(p)
	e := st.get(pi.root)
	if len(pi.steps) == 0 {
		return e.held, e.stored
	}
	if k, _, _ := fieldKey(pi); k == keyPayload {
		return e.held, e.stored
	}
	return c.valueH(st, p), emptySet
}

// operand is the operand table: the set the value holds, with its checks.
// A read or take of a split root's field holds that field's set (valueH,
// splitRead).
func (c *checker) operand(st *state, ctx *stmtCtx, o mir.Operand) setID {
	switch o.Kind {
	case mir.OpConst, mir.OpImm:
		return emptySet
	}
	p := c.paths.ofPlace(c.body, c.tab, o.Place)
	pi := c.paths.get(p)
	root := pi.root
	plain := c.w.plain(pi.typ())
	whole := len(pi.steps) == 0
	if o.Kind == mir.OpCopy {
		switch {
		case whole && !plain:
			c.ice("Copy of the whole non-plain local _%d", o.Place.Local)
		case !whole && !plain:
			c.e0409(st, ctx, p)
			return emptySet
		}
		c.useCheck(st, ctx, root, !whole && c.roots[root].kind == rkTemp && pi.onlyFieldSteps())
		c.access(st, ctx, p, colRead, nil)
		return c.valueH(st, p)
	}
	// Move.
	if whole {
		c.useCheck(st, ctx, root, false)
		c.access(st, ctx, p, colMove, ctx.exempt)
		h := c.held(st, root)
		c.moveRoot(st, ctx, root, true)
		return h
	}
	if c.roots[root].kind == rkTemp && pi.onlyFieldSteps() {
		// A take: the value holds H(t), or of a split t the field the
		// take names; t becomes {T}.
		c.useCheck(st, ctx, root, true)
		h := c.held(st, root)
		if s, _, ok := c.splitRead(st, p); ok {
			h = s
		}
		e := st.get(root)
		e.init = initT
		e.moved = c.sets.add(e.moved, c.site(ctx))
		c.put(st, root, e)
		return h
	}
	c.e0409(st, ctx, p)
	return emptySet
}

// moveRoot marks a whole root moved at this statement: {M}, H := ∅, the
// site joins moved(root) and, for an E0411 parameter, changed(root).
func (c *checker) moveRoot(st *state, ctx *stmtCtx, r rootID, e0411 bool) {
	if !c.roots[r].isLocal() {
		return
	}
	c.setHeld(st, r, emptySet)
	e := st.get(r)
	e.init = initM
	e.moved = c.sets.add(e.moved, c.site(ctx))
	if e0411 && c.e0411Param(r) {
		e.changed = c.sets.add(e.changed, c.site(ctx))
	}
	c.put(st, r, e)
}

// e0409 reports a read of a non-plain place with steps; the recovery marks
// a linear root with no deref on the path moved, with no move site.
func (c *checker) e0409(st *state, ctx *stmtCtx, p pathID) {
	ctx.bad409 = true
	pi := c.paths.get(p)
	if c.captureBackstop(ctx, p, "move") {
		// captureBackstop reported it; the recovery below is
		// for a root with no deref, which a capture's path never is.
	} else if c.report {
		c.errAt(diag.E0409, ctx.span, "%s is not plain and cannot be moved or copied out of a place", c.placeString(p)).
			Text("place", c.placeString(p)).Text("kind", c.w.factsOf(pi.typ()).Kind.String())
	}
	if pi.derefs == 0 && c.roots[pi.root].linear && c.roots[pi.root].isLocal() {
		e := st.get(pi.root)
		e.init = initM
		c.put(st, pi.root, e)
		c.setHeld(st, pi.root, emptySet)
	}
}

// e0411Param reports whether root r is a parameter E0411 protects: a
// by-value, non-plain parameter of a fallible function.
func (c *checker) e0411Param(r rootID) bool {
	ri := &c.roots[r]
	return ri.kind == rkParam && ri.byValueParam && !ri.plain && c.env.Sig != nil && c.env.Sig.Errors != types.EmptyErrors
}

// changeSite records an E0411 site on a parameter root.
func (c *checker) changeSite(st *state, ctx *stmtCtx, r rootID) {
	if !c.e0411Param(r) {
		return
	}
	e := st.get(r)
	e.changed = c.sets.add(e.changed, c.site(ctx))
	c.put(st, r, e)
}

// readPlace is a read of a place by RvLen, RvDiscriminant and the like:
// init check (allowT for a taken temporary's variant or field place), then
// col: Read, deep, or colLen, RvLen's shallow read.
func (c *checker) readPlace(st *state, ctx *stmtCtx, pl mir.Place, allowTaken bool, col column) pathID {
	p := c.paths.ofPlace(c.body, c.tab, pl)
	pi := c.paths.get(p)
	c.useCheck(st, ctx, pi.root, allowTaken && c.roots[pi.root].kind == rkTemp && pi.onlyFieldSteps())
	c.access(st, ctx, p, col, nil)
	return p
}

// lenRead is RvLen's effect: the init check of the root, then the colLen
// access on the length's place, P.* for a slice or str (the length is the
// referent's, as rustc's Len(*xs)), P itself for an array.
//
// A bounds check's length read (boundsLen) makes no access of its own: a
// borrow's whole place, bounds checks included, is evaluated when it is
// formed, and the guarded access of P[i] or P[a..b] that follows conflicts
// with every loan the length read would (each is an exclusive loan on P.*
// or a prefix, which every column of P.*[i] meets), so the read would only
// repeat the access's diagnostic on the same line under E0504.
func (c *checker) lenRead(st *state, ctx *stmtCtx, pl mir.Place) {
	p := c.paths.ofPlace(c.body, c.tab, pl)
	pi := c.paths.get(p)
	c.useCheck(st, ctx, pi.root, false)
	if !ctx.term && c.boundsLen(ctx.b, ctx.i) {
		return
	}
	switch ty := pi.typ(); c.tab.Kind(ty) {
	case types.KSlice, types.KStr:
		p = c.paths.extend(p, step{kind: stDeref, mode: dmShared}, ty)
	case types.KMutSlice:
		p = c.paths.extend(p, step{kind: stDeref, mode: dmExcl}, ty)
	}
	c.access(st, ctx, p, colLen, nil)
}

// boundsLen reports whether statement i of block b is the length read of a
// bounds check, in lower's shape: `n = Len(P)` into a whole temporary, then
// in the same block only casts of it (`n' = Trunc(copy n)`), then the
// comparison `c = Lt(i, copy n')` or `Le(lo, copy n')` with the length as
// its right operand, and the block's switch on `copy c` with an empty
// `panic bounds` successor. A user `ys.Len()` in an index expression is the
// comparison's left side, never its right, so it keeps its access.
func (c *checker) boundsLen(b mir.BlockID, i int) bool {
	blk := &c.body.Blocks[b]
	s := &blk.Stmts[i]
	if s.Place.Proj.Len != 0 || c.body.Locals[s.Place.Local].Kind != mir.LocalTemp || blk.Term.Kind != mir.TermSwitch {
		return false
	}
	on := blk.Term.On
	if on.Kind != mir.OpCopy || on.Place.Proj.Len != 0 {
		return false
	}
	panics := false
	for _, sb := range c.body.Successors(b, nil) {
		t := &c.body.Blocks[sb]
		if len(t.Stmts) == 0 && t.Term.Kind == mir.TermPanic && t.Term.Panic == types.PanicBounds {
			panics = true
		}
	}
	if !panics {
		return false
	}
	cur := s.Place.Local
	isCur := func(o mir.Operand) bool {
		return o.Kind == mir.OpCopy && o.Place.Proj.Len == 0 && o.Place.Local == cur
	}
	for j := i + 1; j < len(blk.Stmts); j++ {
		t := &blk.Stmts[j]
		if t.Kind != mir.StAssign || t.Place.Proj.Len != 0 {
			continue
		}
		rv := &t.Rvalue
		switch {
		case rv.Kind == mir.RvCast && isCur(rv.A):
			cur = t.Place.Local
		case rv.Kind == mir.RvBinary && (rv.Bin == mir.Lt || rv.Bin == mir.Le) && isCur(rv.B):
			return t.Place.Local == on.Place.Local
		}
	}
	return false
}

// rvalueFV is the rvalue table with the value's per-field sets (fields.go):
// a use of a split value, a struct or record literal, and some or ok around
// a split value; 0 for every other rvalue.
func (c *checker) rvalueFV(st *state, ctx *stmtCtx, s *mir.Statement) (setID, fvID) {
	rv := &s.Rvalue
	switch rv.Kind {
	case mir.RvUse:
		return c.operandFV(st, ctx, rv.A)
	case mir.RvAggregate:
		ops := c.body.Operands[rv.Ops.Start:rv.Ops.End()]
		sets := make([]setID, len(ops))
		fvs := make([]fvID, len(ops))
		out := emptySet
		for i, o := range ops {
			sets[i], fvs[i] = c.operandFV(st, ctx, o)
			out = c.sets.unite(out, sets[i])
		}
		dt := c.body.PlaceType(s.Place)
		if !c.w.carrier(dt) {
			return emptySet, 0
		}
		return out, c.aggregateFV(rv, sets, fvs, dt)
	}
	return c.rvalueOne(st, ctx, s), 0
}

// rvalueOne is the rvalue table for every rvalue that keeps no field
// sets.
func (c *checker) rvalueOne(st *state, ctx *stmtCtx, s *mir.Statement) setID {
	rv := &s.Rvalue
	switch rv.Kind {
	case mir.RvUse:
		return c.operand(st, ctx, rv.A)
	case mir.RvRef:
		c.collapseFor(st, rv.Place, rv.Borrow == mir.Exclusive)
		p := c.paths.ofPlace(c.body, c.tab, rv.Place)
		return c.borrow(st, ctx, s, p, rv.Borrow == mir.Exclusive)
	case mir.RvSlice:
		c.collapseFor(st, rv.Place, rv.Borrow == mir.Exclusive)
		return c.slice(st, ctx, s)
	case mir.RvUnary, mir.RvRepeat:
		h := c.operand(st, ctx, rv.A)
		if rv.Kind == mir.RvRepeat {
			return h
		}
		return emptySet
	case mir.RvBinary:
		c.operand(st, ctx, rv.A)
		c.operand(st, ctx, rv.B)
		return emptySet
	case mir.RvCast:
		if rv.Cast == mir.CastNarrow && rv.A.Kind == mir.OpMove && rv.A.Place.Proj.Len == 0 {
			// A narrowing cast moves x with no E0405, E0406 or E0411 site;
			// x' gets H(x).
			r := rootID(rv.A.Place.Local)
			c.useCheck(st, ctx, r, false)
			h := c.held(st, r)
			c.moveRoot(st, ctx, r, false)
			return h
		}
		h := c.operand(st, ctx, rv.A)
		if rv.Cast == mir.CastNarrow {
			return h
		}
		return emptySet
	case mir.RvLen:
		c.lenRead(st, ctx, rv.Place)
		return emptySet
	case mir.RvDiscriminant:
		c.readPlace(st, ctx, rv.Place, true, colRead)
		if s.Place.Proj.Len == 0 {
			c.lastDiscr[s.Place.Local] = rv.Place
		}
		return emptySet
	case mir.RvGlobalRef:
		_, l := c.staticOf(rv.Item, c.body.PlaceType(s.Place), true)
		return c.sets.of(uint32(l))
	case mir.RvConstRef:
		if !c.w.carrier(c.body.PlaceType(s.Place)) {
			return emptySet
		}
		_, l := c.staticOf(rv.Item, c.body.PlaceType(s.Place), false)
		return c.sets.of(uint32(l))
	}
	// RvFnRef, RvSizeOf, RvAlignOf, RvFieldCount, RvTypeIs, RvFieldName,
	// RvConstArg: hold nothing.
	return emptySet
}

// slice is RvSlice by the type of its place: a slice or str
// value yields its own set, a mut []T acts as a borrow of p.*, an array as a
// borrow of p. Bounds operands are reads.
func (c *checker) slice(st *state, ctx *stmtCtx, s *mir.Statement) setID {
	rv := &s.Rvalue
	if rv.HasLo {
		c.operand(st, ctx, rv.A)
	}
	if rv.HasHi {
		c.operand(st, ctx, rv.B)
	}
	p := c.paths.ofPlace(c.body, c.tab, rv.Place)
	ty := c.paths.get(p).typ()
	switch c.tab.Kind(ty) {
	case types.KSlice, types.KStr:
		if rv.Borrow == mir.Exclusive {
			// E0507: `mut xs[a..b]` of a []T or str value is `mut` of its
			// elements, which are reached only through the value's shared
			// step. This is also the exclusive receiver lower forms for a mut
			// self Slice[T] method on a []T. No loan (borrowSiteLoan makes
			// none for this arm), the slice holds nothing, as borrow's
			// shared-step row.
			c.useCheck(st, ctx, c.paths.get(p).root, false)
			c.e0507Mut(ctx, c.paths.extend(p, step{kind: stDeref, mode: dmShared}, ty))
			return emptySet
		}
		c.readPlace(st, ctx, rv.Place, false, colRead)
		return c.valueH(st, p)
	case types.KMutSlice:
		dp := c.paths.extend(p, step{kind: stDeref, mode: dmExcl}, ty)
		return c.borrow(st, ctx, s, dp, rv.Borrow == mir.Exclusive)
	}
	return c.borrow(st, ctx, s, p, rv.Borrow == mir.Exclusive)
}
