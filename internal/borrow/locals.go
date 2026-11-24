// Why: user locals and by-value parameters keep one loan set per field, as
// temporaries do, so that borrowing one field leaves the others free; this
// file holds the rules that keep such a root split and the ones that make
// it whole.
//
// What: splitInfo and the per-field liveness elements (elem, fieldUses,
// defElems), fieldOfLoan, writeSplit, storeLocal and noteStored, fieldWrite,
// collapse and collapseFor, holderLive, splitEntry, and the split-union
// self-check (SetSplitUnionCheck, SplitUnionChecks).
//
// How: an allowlist of uses keeps a root split; every other use that may
// write the whole value makes it whole (collapse). A by-value parameter
// starts split. A store that reaches a split root through a loan whose
// place names field f lands in f; through a loan on the whole root, or one
// with no place, in every field.

package borrow

import (
	"slices"
	"sync/atomic"

	"mk/internal/mir"
	"mk/internal/source"
)

// elemBase numbers liveness's field elements: field f of a split
// root r is element fieldBase[r]+f, above every root ID, so a live set
// holds roots and (root, field) pairs alike.
const elemBase = 1 << 30

// splitInfo fills, for every root that may be split, whether each field
// of its shape is loan-holding (fcar) and its first field element.
func (c *checker) splitInfo() {
	next := uint32(elemBase)
	for r := range c.roots {
		ri := &c.roots[r]
		if ri.nfields == 0 {
			continue
		}
		fts, _ := c.shapeFields(ri.typ)
		ri.fcar = make([]bool, len(fts))
		for i, ft := range fts {
			ri.fcar[i] = c.w.carrier(ft)
		}
		ri.felem = next
		next += uint32(ri.nfields)
		c.anySplit = true
	}
}

// elem is liveness's element of field f of root r.
func (c *checker) elem(r rootID, f int) uint32 { return c.roots[r].felem + uint32(f) }

// fieldUses appends liveness's field elements a use of place pl uses
// on a root that may be split, a place that names field f
// after its payload pairs uses f; any other use of the root (a whole move
// or copy, a whole borrow, an argument, `return`, a payload) uses every
// field.
func (c *checker) fieldUses(pl mir.Place, buf []uint32) []uint32 {
	r := rootID(pl.Local)
	n := c.roots[r].nfields
	if n == 0 {
		return buf
	}
	if k, f, _ := c.keyOf(c.paths.get(c.paths.ofPlace(c.body, c.tab, pl))); k == keyField && int(f) < n {
		return append(buf, c.elem(r, int(f)))
	}
	for f := range n {
		buf = append(buf, c.elem(r, f))
	}
	return buf
}

// defElems is what a def of the whole root d defines: d and, on a root
// that may be split, every field element.
func (c *checker) defElems(d rootID) []uint32 {
	n := c.roots[d].nfields
	out := make([]uint32, 0, n+1)
	out = append(out, uint32(d))
	for f := range n {
		out = append(out, c.elem(d, f))
	}
	return out
}

// fieldOfLoan is the field of root t that loan l's place names (a borrow
// of t.f…, its place inside t's field f), or -1: the whole root, a
// payload, or a loan with no place.
func (c *checker) fieldOfLoan(l loanID, t rootID) int {
	lo := &c.loans[l]
	if lo.place == noPath || c.roots[t].nfields == 0 {
		return -1
	}
	pi := c.paths.get(lo.place)
	if pi.root != t {
		return -1
	}
	if k, f, _ := c.keyOf(pi); k == keyField && int(f) < c.roots[t].nfields {
		return int(f)
	}
	return -1
}

// writeSplit makes r split with sets fs, held their union (held is exactly
// the union of the fields of a split root). deep is the loans the write
// adds deep: they lose their shallow mark (marks.go, deep wins).
func (c *checker) writeSplit(st *state, r rootID, fs []setID, deep setID) {
	S := emptySet
	for _, f := range fs {
		S = c.sets.unite(S, f)
	}
	e := st.get(r)
	old := e.held
	e.held, e.fv = S, c.sets.internFV(fs)
	e.sh = c.sets.minus(e.sh, deep)
	e.stored = c.sets.minus(e.stored, deep)
	c.put(st, r, e)
	if added := c.sets.minus(S, old); added != emptySet {
		if c.report {
			c.noteStores(r, added)
		}
		c.indexHolders(st, r, added)
	}
}

// storeLocal is H(r) ∪= S for a store into frame root r: on a split root into
// field f (f >= 0), or into every loan-holding field (f < 0: a whole loan, or
// a store that knows no field); a whole root takes it whole.
func (c *checker) storeLocal(st *state, r rootID, S setID, f int) {
	if S == emptySet {
		return
	}
	old, oldSh, oldStored := st.get(r).held, st.get(r).sh, st.get(r).stored
	fs := c.sets.fieldSets(st.get(r).fv)
	if fs == nil || len(fs) != c.roots[r].nfields || !c.roots[r].carrier {
		c.addHeld(st, r, S)
		c.noteStored(st, r, old, oldStored, S)
		return
	}
	out := slices.Clone(fs)
	for i := range out {
		if f < 0 && c.roots[r].fcar[i] || i == f {
			out[i] = c.sets.unite(out[i], S)
		}
	}
	c.writeSplit(st, r, out, S)
	c.markStore(st, r, old, oldSh, S)
	c.noteStored(st, r, old, oldStored, S)
}

// noteStored sets root r's content-stored set after a store of s into its
// memory (storeLocal), given its held and stored sets before the store:
// the loans of s that r did not hold before join stored, and those r held
// in stored already stay there; a loan r held as its own value stays
// outside stored (provenance wins).
func (c *checker) noteStored(st *state, r rootID, old, oldStored, s setID) {
	e := st.get(r)
	stored := c.sets.meet(c.sets.unite(oldStored, c.sets.minus(s, old)), e.held)
	if stored == e.stored {
		return
	}
	e.stored = stored
	c.put(st, r, e)
}

// fieldWrite is a destination `x.f… = e` with no deref on a split root
// strong when the place is field f itself (after payload
// pairs), weak (added) when it is deeper inside f. ok is false when the
// root is whole or the place names no field; the caller then writes whole.
func (c *checker) fieldWrite(st *state, p pathID, S setID) bool {
	pi := c.paths.get(p)
	r := pi.root
	fs := c.sets.fieldSets(st.get(r).fv)
	if fs == nil || len(fs) != c.roots[r].nfields {
		return false
	}
	k, f, rest := c.keyOf(pi)
	if k != keyField || int(f) >= len(fs) {
		return false
	}
	out := slices.Clone(fs)
	if !c.roots[r].fcar[f] {
		S = emptySet
	}
	if rest == len(pi.steps) {
		out[f] = S
	} else {
		out[f] = c.sets.unite(out[f], S)
	}
	c.writeSplit(st, r, out, S)
	return true
}

// collapse makes split root r whole (every use outside the allowlist), held
// unchanged: it is already the union.
func (c *checker) collapse(st *state, r rootID) {
	if e := st.get(r); e.fv != 0 {
		e.fv = 0
		c.put(st, r, e)
	}
}

// collapseFor is the collapse at an RvRef or RvSlice of place pl: a borrow
// of x.f… keeps x split; a borrow of the whole of x keeps it only as a
// shared borrow of a type not writable by type that mentions no type
// parameter; on a concrete non-view struct/record, a whole exclusive borrow (or
// shared borrow of a writable struct) merges only the fields that are
// store-compatible at depth 1 (splitMergeMatrix) instead of collapsing whole;
// every other borrow makes x whole.
func (c *checker) collapseFor(st *state, pl mir.Place, excl bool) {
	p := c.paths.ofPlace(c.body, c.tab, pl)
	pi := c.paths.get(p)
	r := pi.root
	if int(r) >= len(c.roots) || c.roots[r].nfields == 0 || st.get(r).fv == 0 {
		return
	}
	k, _, _ := c.keyOf(pi)
	if k == keyField {
		return // a borrow of one field keeps x split
	}
	ri := &c.roots[r]
	if c.mentionsParam(ri.typ) {
		// Writable by type is judged on the instance: Two2[T] holds a Cell
		// inline at T = Slot, so in a generic body a whole borrow of a type
		// that mentions a parameter or Self makes it whole.
		c.collapse(st, r)
		return
	}
	if len(pi.steps) == 0 && !excl && !ri.writable {
		return // a shared whole borrow keeps x split
	}
	if (len(pi.steps) == 0 || k == keyPayload) && !ri.rest {
		if reach := c.w.splitMergeMatrix(ri.typ, ri.nfields, ri.fcar); reach != nil {
			fs := c.sets.fieldSets(st.get(r).fv)
			if len(fs) == ri.nfields {
				merged := make([]setID, ri.nfields)
				added := emptySet
				for i := range merged {
					if !ri.fcar[i] {
						continue
					}
					s := fs[i]
					for j, f := range fs {
						if reach[i][j] {
							s = c.sets.unite(s, f)
						}
					}
					merged[i] = s
					added = c.sets.unite(added, c.sets.minus(s, fs[i]))
				}
				c.writeSplit(st, r, merged, added)
				return
			}
		}
	}
	c.collapse(st, r)
}

// holderLive is rootLive for a holder of some loan of x (live loans per
// root and field): a split holder is live for x when a field
// holding a loan of x is live before the access; a whole one when any of
// its elements is.
func (c *checker) holderLive(st *state, ctx *stmtCtx, h rootID, x setID, noReturn bool) bool {
	if c.sets.has(ctx.slots, uint32(h)) {
		return true
	}
	ri := &c.roots[h]
	if !ri.isLocal() || noReturn && h == 0 {
		return false
	}
	if ri.nfields == 0 {
		return c.liveBefore(h, ctx.b, ctx.i)
	}
	fs := c.sets.fieldSets(st.get(h).fv)
	if fs == nil || len(fs) != ri.nfields {
		return c.liveBefore(h, ctx.b, ctx.i)
	}
	// held is exactly the union of the fields (writeSplit, put), so every
	// loan of x is in some field.
	for f, s := range fs {
		if c.sets.intersects(s, x) && c.liveBefore(rootID(c.elem(h, f)), ctx.b, ctx.i) {
			return true
		}
	}
	return false
}

// splitUnion makes put and joinInto check, on every write and every join of a
// split root, that held is exactly the union of the fields, which holderLive
// relies on; a miss is an internal error (E0000). borrow's tests turn it on,
// and so do the soundness runs, through SetSplitUnionCheck. splitUnionN counts
// the checks made.
var (
	splitUnion  atomic.Bool
	splitUnionN atomic.Uint64
)

// SetSplitUnionCheck turns the split-union check on or off for every check
// from now on, and returns the previous setting.
func SetSplitUnionCheck(on bool) bool { return splitUnion.Swap(on) }

// SplitUnionChecks is the number of split-union checks made so far in this
// process.
func SplitUnionChecks() uint64 { return splitUnionN.Load() }

// checkSplitUnion is that check at a write (put).
func (c *checker) checkSplitUnion(r rootID, e entry) {
	splitUnionAt(c.sets, r, e, c.body.Spans[0])
}

// joinSpan is a join's: joinInto knows no body.
var joinSpan source.Span

// splitUnionAt is the check, an ICE at span on a miss.
func splitUnionAt(ss *setStore, r rootID, e entry, span source.Span) {
	splitUnionN.Add(1)
	u := emptySet
	for _, f := range ss.fieldSets(e.fv) {
		u = ss.unite(u, f)
	}
	if u != e.held {
		source.ICEf(span, "borrow: root %d split with held %v, the union of its fields %v", r, ss.get(e.held), ss.get(u))
	}
}

// splitEntry is a by-value parameter's entry on a split shape:
// every loan-holding field holds what the parameter holds, {p.outer}; a
// field whose references all refer to allocators reads p.outerA (valueH's
// outerAlloc), so no field gets an abstract loan of its own.
func (c *checker) splitEntry(r rootID, held setID) fvID {
	ri := &c.roots[r]
	if ri.nfields == 0 || held == emptySet {
		return 0
	}
	fs := make([]setID, ri.nfields)
	for i := range fs {
		if ri.fcar[i] {
			fs[i] = held
		}
	}
	return c.sets.internFV(fs)
}
