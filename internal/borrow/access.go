// Why: every read, write, borrow, move and scope end must be checked against
// the loans live at that point, and this is the hottest path of the checker.
//
// What: the access columns and the conflict table (conflict, deep), the
// cached candidate loans per (path, column), access and liveLoan, the
// per-root "on" filter that put maintains, and the conflict reports with
// their facts and fixes (conflictAt, fieldClauseFix, loanFact).
//
// How: a loan is live when some live root or formed argument slot reaches
// it; liveHolder searches backward from the loan over the holders index, so
// the cost is the loan's backward region, not the number of live roots. The
// walk stops at the first live conflict in loan order, the one the report
// keeps.

package borrow

import (
	"slices"

	"mk/internal/diag"
	"mk/internal/mir"
	"mk/internal/types"
)

// column is the access's column of the conflict table.
type column uint8

const (
	colRead column = iota
	colWrite
	colRef
	colMut
	colMove
	colEnd
	// colLen is RvLen's read, the Read column on the length of the place's
	// referent (an array's own length; a slice's or str's, read as the place
	// P.*): it conflicts with a loan on that place or a prefix of it, and
	// never with a loan on an element or below. So `xs[1]`'s bounds check
	// while `mut xs[0]` is live is no E0504, while `xs.Len()` inside
	// `for x in mut xs` is (the loop holds a loan on xs.*).
	// This is rustc's Shallow(ArrayLength) access.
	colLen
)

// deep reports the access depth: Write, ScopeEnd and the length read are
// shallow.
func (k column) deep() bool { return k != colWrite && k != colEnd && k != colLen }

// conflict is the conflict table; 0 is ok.
func conflict(excl bool, k column) diagCode {
	switch k {
	case colRead, colRef, colLen:
		if excl {
			return diag.E0504
		}
		return 0
	case colWrite:
		return diag.E0502
	case colMut:
		return diag.E0501
	case colMove:
		return diag.E0503
	}
	return diag.E0505
}

type diagCode = diag.Code

type candKey struct {
	p   pathID
	col column
}

// candidates is the loans whose place has p's root, overlaps p at the
// column's depth, and whose table entry is an error: a property of the body,
// cached.
func (c *checker) candidates(p pathID, col column) []loanID {
	k := candKey{p, col}
	if cs, ok := c.cands[k]; ok {
		return cs
	}
	var out []loanID
	for _, l := range c.loansOn(c.paths.get(p).root) {
		lo := &c.loans[l]
		if conflict(lo.excl, col) == 0 {
			continue
		}
		if col == colLen {
			if !c.paths.overlapLen(lo.place, p) {
				continue
			}
		} else if !c.paths.overlap(lo.place, p, col.deep()) {
			continue
		}
		out = append(out, l)
	}
	c.cands[k] = out
	return out
}

// eachLiveCandidate calls f on candidates(p, col) restricted to the loans
// in on of p's root in st, in loan order, until f returns false. A
// candidate outside on is held by no root of st, so liveHolder finds no seed
// for it: the filter changes no result, so an access does not cost every
// loan ever made on its root. Both lists are sorted by loan, so the walk is
// lazy: an access that stops at its first live conflict costs the
// candidates before it, not all of them.
func (c *checker) eachLiveCandidate(st *state, p pathID, col column, f func(loanID) bool) {
	cands := c.candidates(p, col)
	if len(cands) <= c.onMin || c.noOnFilter {
		for _, l := range cands {
			if !f(l) {
				return
			}
		}
		return
	}
	on := c.sets.get(st.get(c.paths.get(p).root).on)
	if len(on) < len(cands) {
		for _, l := range on {
			if _, ok := slices.BinarySearch(cands, loanID(l)); ok && !f(loanID(l)) {
				return
			}
		}
		return
	}
	for _, l := range cands {
		if _, ok := slices.BinarySearch(on, uint32(l)); ok && !f(l) {
			return
		}
	}
}

// onFilterMin is the candidate count up to which eachLiveCandidate walks the
// cached list unfiltered (the filter costs more than it saves).
const onFilterMin = 8

// onPruneMax bounds pruneOn's scan of a loan's holders index: a loan with
// more holders stays in on (a superset is enough), so a loan copied into
// thousands of roots (a long chain of copies) costs O(1) per removal.
const onPruneMax = 16

// put writes root r's entry; every write of a state entry outside state.go
// goes through it. It keeps r's own on and maintains the invariant of on: a
// loan added to a held set joins on of its place root; a loan removed from
// one leaves it when no root of st holds it any more (decided over the
// loan's holders index, which is a superset of its holders in every state).
func (c *checker) put(st *state, r rootID, e entry) {
	old := st.get(r)
	e.on = old.on
	// Field sets (fields.go): a write that changes held and keeps
	// the vector is no split write. A removal (a kill, a purge, a move,
	// ends) takes its loans from every field; anything else makes the root
	// whole.
	if e.fv != 0 && e.fv == old.fv && e.held != old.held {
		if c.sets.minus(e.held, old.held) == emptySet {
			e.fv = c.sets.meetFV(e.fv, e.held)
		} else {
			e.fv = 0
		}
	}
	if e.sh != emptySet {
		e.sh = c.sets.meet(e.sh, e.held) // the shallow marks stay within held
	}
	if e.stored != emptySet {
		e.stored = c.sets.meet(e.stored, e.held)
	}
	if e == old {
		return
	}
	if e.fv != 0 && splitUnion.Load() {
		c.checkSplitUnion(r, e)
	}
	st.set(r, e)
	if old.held == e.held || c.noOnFilter {
		return
	}
	// One merge walk of the two sorted held sets: added, then removed.
	xs, ys := c.sets.get(e.held), c.sets.get(old.held)
	i, j := 0, 0
	for i < len(xs) || j < len(ys) {
		switch {
		case j == len(ys) || i < len(xs) && xs[i] < ys[j]:
			c.onAdd(st, loanID(xs[i]))
			i++
		case i == len(xs) || ys[j] < xs[i]:
			c.onRemove(st, loanID(ys[j]))
			j++
		default:
			i++
			j++
		}
	}
}

// onTracked reports whether on of l's place root is kept: only a root with
// more than onMin loans can have more than onMin candidates, so the filter
// is never asked of any other root and its sets stay empty.
func (c *checker) onTracked(l loanID) rootID {
	pr := c.placeRoot(l)
	if pr == noRoot || len(c.loansOn(pr)) <= c.onMin {
		return noRoot
	}
	return pr
}

// onAdd records that some root of st holds l.
func (c *checker) onAdd(st *state, l loanID) {
	pr := c.onTracked(l)
	if pr == noRoot {
		return
	}
	x := st.get(pr)
	if !c.sets.has(x.on, uint32(l)) {
		x.on = c.sets.add(x.on, uint32(l))
		st.set(pr, x)
	}
}

// onRemove drops l from on of its place root when no root of st holds it.
func (c *checker) onRemove(st *state, l loanID) {
	pr := c.onTracked(l)
	if pr == noRoot {
		return
	}
	x := st.get(pr)
	if !c.sets.has(x.on, uint32(l)) {
		return
	}
	hs := c.loanHoldersOf(l)
	if len(hs) > c.pruneMax {
		return
	}
	for _, h := range hs {
		if c.sets.has(c.held(st, h), uint32(l)) {
			return
		}
	}
	x.on = c.sets.minus(x.on, c.sets.of(uint32(l)))
	st.set(pr, x)
}

// placeRoot is the root of loan l's place, or noRoot for a loan with none.
func (c *checker) placeRoot(l loanID) rootID {
	if p := c.loans[l].place; p != noPath {
		return c.paths.get(p).root
	}
	return noRoot
}

// access checks an access of place p in column col against every live loan
// in the report pass only. skip, when set, exempts loans (a loop's
// element borrow, the formation of an ends clause).
//
// Outside ScopeEnd every conflict of one access has the same code and primary
// span (the code depends on the column alone), and the report keeps one
// diagnostic per (code, primary span). The one kept is the conflict with the
// first live candidate in loan order (the body's order of borrow sites), so
// the walk stops there: an access with k live conflicting loans costs one
// live-loan search, not k (choosing the least message instead made deeply
// nested scopes quadratic). A ScopeEnd reports every live loan: each is a
// separate diagnostic at its own borrow site.
func (c *checker) access(st *state, ctx *stmtCtx, p pathID, col column, skip func(loanID) bool) {
	if !c.report {
		return
	}
	if c.fullScan && col != colEnd {
		c.accessFullScan(st, ctx, p, col, skip)
		return
	}
	all := col == colEnd
	c.eachLiveCandidate(st, p, col, func(l loanID) bool {
		if skip != nil && skip(l) {
			return true
		}
		h := c.liveHolder(st, ctx, l, col == colEnd, noRoot)
		if h == noRoot {
			return true
		}
		c.conflictAt(ctx, p, col, l, h)
		return all
	})
}

// accessFullScan is access's reference (test only, fullScan): every
// candidate, unfiltered and to the end, and the live one with the least
// loan ID.
func (c *checker) accessFullScan(st *state, ctx *stmtCtx, p pathID, col column, skip func(loanID) bool) {
	best, bestH := noLoan, noRoot
	for _, l := range c.candidates(p, col) {
		if skip != nil && skip(l) {
			continue
		}
		h := c.liveHolder(st, ctx, l, false, noRoot)
		if h != noRoot && (best == noLoan || l < best) {
			best, bestH = l, h
		}
	}
	if best != noLoan {
		c.conflictAt(ctx, p, col, best, bestH)
	}
}

// liveLoan reports whether loan l is live at the access: whether it is in
// R(live roots ∪ formed slots) of state st, with local 0 left out for a
// ScopeEnd and root except left out (the exemption of an ends clause's
// formation). It searches backward from l, which decides the same relation
// as the forward reach: a root reaches l when it holds l, or holds a loan
// targeting a root that reaches l. The holders index lists the roots that
// may hold a loan with a place; a loan that targets h has place root h, so
// holdersOf(h) lists every predecessor of h. A static node never holds a
// loan with a place. An abstract node holds one when the body stored it
// there; its predecessors hold abstract loans, which have no place, and
// absPredecessors finds them. This is the fallback for wide live sets,
// taken from the start: the cost is the backward region of l, not the
// number of live roots.
func (c *checker) liveLoan(st *state, ctx *stmtCtx, l loanID, noReturn bool, except rootID) bool {
	return c.liveHolder(st, ctx, l, noReturn, except) != noRoot
}

// liveHolder is liveLoan returning the live root found to reach l (the
// `holder` and `kept_live_by` facts of the diagnostic), or noRoot.
func (c *checker) liveHolder(st *state, ctx *stmtCtx, l loanID, noReturn bool, except rootID) rootID {
	c.newEpoch()
	work := c.scratchRoots[:0]
	// A split holder is live for the loans it was reached through only
	// when a field holding one of them is (liveness is per root and field):
	// via accumulates them per split root, which is revisited when a new
	// path adds loans of another field.
	clear(c.liveVia)
	reach := func(g rootID, x setID) {
		if c.roots[g].nfields == 0 || st.get(g).fv == 0 {
			if c.rootMark[g] != c.epoch {
				c.rootMark[g] = c.epoch
				work = append(work, g)
			}
			return
		}
		x = c.sets.meet(x, c.held(st, g))
		old, seen := c.liveVia[g]
		if seen && c.sets.minus(x, old) == emptySet {
			return
		}
		c.liveVia[g] = c.sets.unite(old, x)
		c.rootMark[g] = c.epoch
		work = append(work, g)
	}
	one := c.sets.of(uint32(l))
	for _, h := range c.loanHoldersOf(l) {
		if c.sets.has(c.held(st, h), uint32(l)) {
			reach(h, one)
		}
	}
	found := noRoot
	for len(work) > 0 {
		h := work[len(work)-1]
		work = work[:len(work)-1]
		if h != except {
			x, split := c.liveVia[h]
			if !split && c.rootLive(ctx, h, noReturn) || split && c.holderLive(st, ctx, h, x, noReturn) {
				found = h
				break
			}
		}
		if c.roots[h].kind == rkAbstract {
			c.absPredecessors(st, h, reach)
			continue
		}
		tg := c.targeting(h)
		if tg == emptySet {
			continue
		}
		for _, g := range c.holdersOf(h) {
			if c.sets.intersects(c.held(st, g), tg) {
				reach(g, tg)
			}
		}
	}
	c.scratchRoots = work[:0]
	return found
}

// absPredecessors hands reach the roots holding an abstract loan whose
// target is the abstract node h: the roots the body gave such a loan
// (absHoldersOf), the parameter's own root, which holds p.outer, and the
// parameter's nodes, which hold p.inner. A loan the body stored into h is
// then live while anything that reaches h is: for example,
// `e := First(&*h)` keeps `h.r = &*xs` live, so xs.Push is E0501. reach is
// liveHolder's, so a split holder (a by-value parameter starts split) is
// live only through a field that holds one of those loans, and is
// revisited when this path adds loans of another field.
func (c *checker) absPredecessors(st *state, h rootID, reach func(rootID, setID)) {
	tg := c.absTargeting(h)
	p := c.roots[h].param
	if c.isParamN2Field(p, h) {
		pi := &c.params[p]
		tg2 := c.absTargeting(pi.n2)
		pushTg := func(g rootID, set setID) {
			if g != noRoot && set != emptySet && c.sets.intersects(c.held(st, g), set) {
				reach(g, set)
			}
		}
		if tg != emptySet {
			for _, g := range c.absHoldersOf(h) {
				pushTg(g, tg)
			}
		}
		// Any caller holding untagged p.inner (or p itself / p#1 / p#A) can
		// reach this field node without routing into sibling n2F[j] nodes.
		tgAll := c.sets.unite(tg, tg2)
		if tg2 != emptySet {
			for _, g := range c.absHoldersOf(pi.n2) {
				pushTg(g, tg2)
			}
		}
		pushTg(rootID(p), tgAll)
		pushTg(pi.n1, tgAll)
		pushTg(pi.n1A, tgAll)
		pushTg(pi.nA, tgAll)
		return
	}
	if tg == emptySet {
		return
	}
	push := func(g rootID) {
		if g != noRoot && c.sets.intersects(c.held(st, g), tg) {
			reach(g, tg)
		}
	}
	for _, g := range c.absHoldersOf(h) {
		push(g)
	}
	if pi := &c.params[p]; pi.carrier {
		push(rootID(p))
		push(pi.n1)
		push(pi.n1A)
		push(pi.n2)
		push(pi.nA)
		for _, nf := range pi.n2F {
			push(nf)
		}
		if h == pi.n2 {
			// held(nf) includes st.get(pi.n2).held, so any live holder of
			// n2F[i] keeps loans stored directly into p#2 alive too.
			for _, nf := range pi.n2F {
				if nf != noRoot {
					nfg := c.absTargeting(nf)
					if nfg == emptySet {
						continue
					}
					for _, g := range c.absHoldersOf(nf) {
						if g != noRoot && c.sets.intersects(c.held(st, g), nfg) {
							reach(g, nfg)
						}
					}
				}
			}
		}
	}
}

// rootLive reports whether root r counts as live at the access: a formed
// argument slot of this call, or a local live before the statement.
func (c *checker) rootLive(ctx *stmtCtx, r rootID, noReturn bool) bool {
	if c.sets.has(ctx.slots, uint32(r)) {
		return true
	}
	if !c.roots[r].isLocal() || noReturn && r == 0 {
		return false
	}
	return c.liveBefore(r, ctx.b, ctx.i)
}

// targeting is the loans whose target is root h, cached: borrows of h's own
// storage, every one with place root h.
func (c *checker) targeting(h rootID) setID {
	if s, ok := c.targets[h]; ok {
		return s
	}
	var xs []uint32
	for _, l := range c.loansOn(h) {
		if c.loans[l].target == h {
			xs = append(xs, uint32(l))
		}
	}
	s := c.sets.fromUnsorted(xs)
	c.targets[h] = s
	return s
}

func (c *checker) newEpoch() {
	if len(c.rootMark) < len(c.roots) {
		c.rootMark = make([]uint32, len(c.roots)+16)
	}
	c.epoch++
}

// conflictAt reports one conflict: at the access for Read, Write, &, mut and
// Move; at the loan's creation site for ScopeEnd (E0505, or E0506 for a
// temporary or loop holder).
func (c *checker) conflictAt(ctx *stmtCtx, p pathID, col column, l loanID, holder rootID) {
	lo := &c.loans[l]
	code := conflict(lo.excl, col)
	lf := c.loanFact(l)
	hn := c.rootName(holder)
	kept := func(d *diag.Diagnostic) {
		if u := c.nextUse(holder, ctx.b, ctx.i); u != mir.NoSpan {
			d.Loc("kept_live_by", c.body.Span(u))
		}
	}
	if col == colEnd {
		r := c.paths.get(p).root
		if k := c.roots[r].kind; k == rkTemp || k == rkHolder {
			d := c.errAt(diag.E0506, lo.site, "this temporary is borrowed by %s, which outlives its statement", hn).
				Fact("loan", lf).Text("holder", hn)
			kept(d)
			d.Loc("ends_at", c.body.Span(ctx.span))
			c.bindLocalFix(d, lo.site, r)
			return
		}
		at, moved := c.storeSite(l, holder)
		d := c.errAt(diag.E0505, at, "%s does not live long enough: it is still borrowed by %s when its scope ends", c.rootName(r), hn).
			Fact("loan", lf).Text("holder", hn)
		if moved {
			d.Message += "; this statement stores the loan"
			d.Loc("created_at", c.body.Span(lo.site))
		}
		kept(d)
		d.Loc("ends_at", c.body.Span(ctx.span))
		return
	}
	d := c.errAt(code, ctx.span, "%s conflicts with a live %s loan held by %s", c.placeString(p), kindWord(lo.excl), hn).
		Fact("loan", lf).Text("holder", hn)
	kept(d)
	c.sigFact(d, ctx)
	if code == diag.E0501 {
		c.fieldClauseFix(d, l)
	}
}

// fieldClauseFix is E0501's `from self.f` fix: the live loan was made at a
// direct call, the innermost call whose span holds the loan's site, and the
// callee's body shows that its result comes from one reference field of the
// borrowed parameter (Env.FieldClause). Writing that clause on the callee
// makes the result hold only what the field holds, so the call no longer keeps
// the argument borrowed. Proven for a private callee; compiles for an exported
// one, since the clause can move errors to its other callers.
func (c *checker) fieldClauseFix(d *diag.Diagnostic, l loanID) {
	if c.env.FieldClause == nil {
		return
	}
	site := c.body.Span(c.loans[l].site)
	var callee types.ItemID
	best := -1
	for bi := range c.body.Blocks {
		t := &c.body.Blocks[bi].Term
		if !isCall(t) || t.Callee.Kind != mir.CallDirect {
			continue
		}
		sp := c.body.Span(t.Span)
		if sp.File != site.File || sp.Start > site.Start || sp.End < site.End {
			continue
		}
		if n := int(sp.End - sp.Start); best < 0 || n < best {
			best, callee = n, t.Callee.Fn.Item
		}
	}
	if best < 0 {
		return
	}
	fc, ok := c.env.FieldClause(callee)
	if !ok {
		return
	}
	class := diag.Proven
	if fc.Exported {
		class = diag.Compiles
	}
	d.Fix(diag.Fix{Class: class, Edits: []diag.Edit{{Span: fc.At, Text: " " + fc.Clause}},
		Why: "write `" + fc.Clause + "` on the callee: its body returns that field, so its result no longer keeps the argument borrowed"})
}

func kindWord(excl bool) string {
	if excl {
		return "exclusive"
	}
	return "shared"
}

// loanFact is the diagnostic's loan fact: the place in Mk spelling, the kind, the
// creation site.
func (c *checker) loanFact(l loanID) diag.Fact {
	lo := &c.loans[l]
	kind := diag.LoanShared
	if lo.excl {
		kind = diag.LoanExclusive
	}
	place := c.loanPlace(l)
	at := c.body.Spans[0]
	if lo.site != mir.NoSpan && int(lo.site) < len(c.body.Spans) && lo.place != noPath {
		at = c.body.Span(lo.site)
	}
	return diag.LoanFact(place, kind, at)
}

// loanPlace names what loan l borrows: its place, "global", the parameter
// for an outer abstract loan, and `*p` for an inner one, what p's
// references reach (`prog` reads as a loan of the shared parameter
// itself).
func (c *checker) loanPlace(l loanID) string {
	lo := &c.loans[l]
	switch {
	case lo.place != noPath:
		return c.placeString(lo.place)
	case lo.static:
		return "global"
	case lo.abstract && (lo.inner || lo.tag != nil):
		return "*" + c.paramName(lo.param)
	case lo.abstract:
		return c.paramName(lo.param)
	}
	return ""
}
