// Why: the tagged mode runs a program under a Stacked Borrows model to find
// aliasing violations the borrow checker should have rejected; this file is
// that model's access rule and the events that change the borrow stacks.
//
// What: tacc (the access rule), ownerAccess, taggedAccess, retag and
// retagRef, tgEnter and tgPopFrame (function entry and exit), tgScopeEnd,
// tgMoved, the fallible-call error edge (tgArgUndo, tgErrorEdge,
// tgErrorExit), and tgStorePtr.
//
// How: an access checks liveness, then interior bytes (liveness, init and
// races only), then owner access or tagged access against the segment's
// borrow stack, then init. Tags are made only at borrows, slices and
// function entry, and copies keep them; raw pointers keep their source's
// tag. Conflicts are found lazily, at the access, except for a call's own
// loans, which protectors check eagerly.

package eval

import (
	"fmt"

	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// Access modes of tacc.
const (
	// accRead: a read of a value's bytes; VUninit on a frame when no byte of
	// it was written (a never-written or moved-out local).
	accRead uint8 = iota
	// accScalar: a read of a scalar or pointer word; VUninit on a frame when
	// any of its bytes was not written.
	accScalar
	// accWrite: a write; sets init bits, and an owner write owns a linear
	// local's value again.
	accWrite
	// accRetagR, accRetagW: a shared or exclusive retag's access: the stack
	// rule of a read or a write, with no effect on init bits.
	accRetagR
	accRetagW
)

// takeVal returns the tag of the pointer in flight and clears the register;
// 0 in plain mode or when none is in flight.
func (m *Machine) takeVal() uint32 {
	if m.tg == nil {
		return 0
	}
	v := m.tg.val
	m.tg.val = noVal
	if v == noVal {
		return 0
	}
	return v
}

// setVal puts tag t in flight for the next pointer store (a pointer the
// interpreter made itself: a fresh borrow, owner memory, a function value).
func (m *Machine) setVal(t uint32) {
	if m.tg != nil {
		m.tg.val = t
	}
}

// curSpan is the span of the running statement, the request's site before
// any frame.
func (m *Machine) curSpan() source.Span {
	if n := len(m.frames); n > 0 {
		return m.frames[n-1].curSpan()
	}
	return m.req.Site
}

// tgAlloc is allocation a's tagged state, made on first use for memory the
// evaluation owns and for const copies; nil for a function value, a var
// global (plain mode refuses its accesses) or no allocation.
func (m *Machine) tgAlloc(a uint32) *tagAlloc {
	ta := m.tg.rec(a)
	if ta.made {
		return ta
	}
	switch m.mem.kindOf(a) {
	case allocOwn, allocFrame:
		return m.tg.newOwn(a, uint32(len(m.mem.get(a).bytes)), false)
	case allocConst:
		return m.tg.newOwn(a, uint32(len(m.mem.get(a).bytes)), true)
	}
	return nil
}

// tacc is the access rule for n bytes at off of allocation a through
// tag (0: the owner). Plain mode's own checks run after it and refuse what
// it lets through (bounds, a global, a write to a const).
func (m *Machine) tacc(a, off uint32, n uint64, tag uint32, mode uint8) {
	if n == 0 || a == 0 {
		return
	}
	tg := m.tg
	if !m.mem.isLive(a) {
		if ta := tg.known(a); ta != nil && ta.dead {
			m.violation(Violation{Kind: VDead, Tag: tag, Made: tg.made(tag), Removed: ta.died,
				Where: m.where(a, off), Off: off, Len: uint32(n)})
		}
		return
	}
	ta := m.tgAlloc(a)
	if ta == nil || uint64(off)+n > uint64(ta.size) {
		return
	}
	lo, hi := off, off+uint32(n)
	write := mode == accWrite || mode == accRetagW
	if ta.frame && tag == 0 && mode == accWrite {
		m.restartEnded(ta, lo, hi)
	}
	i, j := ta.split(lo, hi)
	if !ta.frame && tag == 0 && mode == accWrite && len(ta.resets) > 0 {
		ta.tgRestartOwn(i, j) // the arena writes reset bytes again (tagarena.go)
	}
	var ti *taginfo
	if tag != 0 && int(tag) < len(tg.tags) {
		ti = &tg.tags[tag]
	}
	for k := i; k < j; k++ {
		s := &ta.segs[k]
		if s.ended || (ti != nil && ti.gen != s.gen) {
			v := Violation{Kind: VDeadScope, Tag: tag, Where: m.where(a, s.lo), Off: off, Len: uint32(n)}
			if ti != nil {
				v.Made, v.Removed = ti.made, ti.removed
			}
			if !ta.frame {
				if at, ok := ta.endedAt(s.lo, ti, s.gen); ok {
					v.Removed = at // the arena's Reset that ended the bytes
				}
			}
			m.violation(v)
			return
		}
		if s.interior {
			if tg.race.on && mode <= accWrite { // a retag is no access for the race check
				m.raceAccess(a, s.lo, uint64(s.hi-s.lo), mode == accWrite)
			}
			continue // checked for liveness, init and races only
		}
		if tag == 0 {
			m.ownerAccess(a, s, write)
		} else {
			m.taggedAccess(a, s, tag, write)
		}
	}
	if ta.frame {
		switch mode {
		case accRead, accScalar:
			if !ta.initRange(lo, hi, mode == accScalar) {
				m.violation(Violation{Kind: VUninit, Tag: tag, Made: tg.made(tag), Where: m.where(a, lo), Off: off, Len: uint32(n)})
			}
		case accWrite:
			ta.setInit(lo, hi)
			if tag == 0 {
				for k := i; k < j; k++ {
					if l := ta.segs[k].local; l >= 0 {
						ta.locals[l].owned = true
					}
				}
			}
		}
	}
	ta.merge()
}

func (tg *tagState) made(tag uint32) source.Span {
	if tag == 0 || int(tag) >= len(tg.tags) {
		return source.Span{}
	}
	return tg.tags[tag].made
}

// restartEnded starts the next activation of every frame local whose ended
// segments [lo, hi) meets: an owner write to an ended local is its next
// activation, live with the owner alone and no byte written.
func (m *Machine) restartEnded(ta *tagAlloc, lo, hi uint32) {
	for k := ta.find(lo); k < len(ta.segs) && ta.segs[k].lo < hi; k++ {
		s := &ta.segs[k]
		if !s.ended || s.local < 0 {
			continue
		}
		l := &ta.locals[s.local]
		for q := ta.find(l.lo); q < len(ta.segs) && ta.segs[q].lo < l.hi; q++ {
			ta.segs[q].ended = false
			ta.segs[q].stack = ownerOnly(false)
		}
		clearBits(ta.init, l.lo, l.hi)
	}
}

// remove takes item it off a stack: a protected item is VProtected (the
// access conflicts with a live call's loan); the tag's first removal
// site is kept for VRemovedTag.
func (m *Machine) remove(a uint32, s *tseg, it item) {
	tg := m.tg
	if it.prot != 0 {
		v := Violation{Kind: VProtected, Tag: it.tag, Made: tg.made(it.tag), Where: m.where(a, s.lo), Off: s.lo, Len: s.hi - s.lo}
		if int(it.prot) < len(tg.callItems) {
			v.Holder = tg.callItems[it.prot]
		}
		m.violation(v)
	}
	if t := &tg.tags[it.tag]; it.tag != 0 && !t.removed.IsValid() {
		t.removed = m.curSpan()
	}
}

// ownerAccess is an access through the owner's tag: a write pops every item
// above the owner; a read removes every Unique item above it and keeps the
// Shared ones.
func (m *Machine) ownerAccess(a uint32, s *tseg, write bool) {
	m.popAbove(a, s, 0, write)
}

// taggedAccess is an access through a tag: the topmost item with it must be
// on the stack; a write through a Shared item is VSharedWrite; then items
// above it are popped (a write) or the Unique ones removed (a read).
func (m *Machine) taggedAccess(a uint32, s *tseg, tag uint32, write bool) {
	k := -1
	for q := len(s.stack) - 1; q >= 0; q-- {
		if s.stack[q].tag == tag {
			k = q
			break
		}
	}
	tg := m.tg
	if k < 0 {
		v := Violation{Kind: VRemovedTag, Tag: tag, Where: m.where(a, s.lo), Off: s.lo, Len: s.hi - s.lo}
		if int(tag) < len(tg.tags) {
			v.Made, v.Removed = tg.tags[tag].made, tg.tags[tag].removed
		}
		m.violation(v)
		return
	}
	if write && !s.stack[k].unique {
		m.violation(Violation{Kind: VSharedWrite, Tag: tag, Made: tg.made(tag), Where: m.where(a, s.lo), Off: s.lo, Len: s.hi - s.lo})
		return
	}
	m.popAbove(a, s, k, write)
}

// popAbove removes the items above index k: all of them for a write, the
// Unique ones for a read.
func (m *Machine) popAbove(a uint32, s *tseg, k int, write bool) {
	st := s.stack
	if len(st) <= k+1 {
		return
	}
	out := st[:k+1]
	for _, it := range st[k+1:] {
		if write || it.unique {
			m.remove(a, s, it)
			continue
		}
		out = append(out, it)
	}
	clear(st[len(out):])
	s.stack = out
}

// retag is a borrow's access then push: the access over n bytes at l
// through l's tag (a read for a shared borrow, a write for an exclusive
// one), a fresh tag, the interior bytes of count values of elem marked, and
// a fresh item pushed on every other segment of the range, protected by
// call prot (0 for none). It returns the fresh tag.
func (m *Machine) retag(l loc, n uint64, elem *tinfo, count uint64, unique bool, prot uint32) uint32 {
	tg := m.tg
	mode := accRetagR
	if unique {
		mode = accRetagW
	}
	m.tacc(l.a, l.off, n, l.tag, mode)
	var gen uint32
	ta := (*tagAlloc)(nil)
	if m.mem.isLive(l.a) {
		ta = m.tgAlloc(l.a)
	}
	if ta != nil && n > 0 && uint64(l.off)+n <= uint64(ta.size) {
		gen = ta.segs[ta.find(l.off)].gen
	}
	t := tg.newTag(l.a, gen, unique, m.curSpan())
	if ta == nil || n == 0 || uint64(l.off)+n > uint64(ta.size) {
		return t
	}
	if elem != nil {
		m.markInterior(ta, l.off, elem, count)
	}
	i, j := ta.split(l.off, l.off+uint32(n))
	for k := i; k < j; k++ {
		if s := &ta.segs[k]; !s.interior {
			s.stack = append(s.stack, item{tag: t, unique: unique, prot: prot})
		}
	}
	if prot != 0 {
		f := &tg.frames[len(tg.frames)-1]
		f.prot = append(f.prot, protItem{alloc: l.a, lo: l.off, hi: l.off + uint32(n), tag: t})
	}
	ta.merge()
	return t
}

// retagRef is RvRef's retag of the place l: the whole value.
func (m *Machine) retagRef(l loc, b mir.BorrowKind) uint32 {
	return m.retag(l, l.ti.size, l.ti, 1, b == mir.Exclusive, 0)
}

// tgPushFrame gives a new frame its tagged state: one segment per local of
// nonzero size, padding between them, no byte written, and a call id.
func (m *Machine) tgPushFrame(f *frame) {
	tg := m.tg
	cd := f.code
	ta := tg.rec(f.base)
	*ta = tagAlloc{made: true, frame: true, size: uint32(cd.frameSize), fnItem: f.item, body: cd.body}
	ta.init = make([]uint64, (cd.frameSize+63)/64)
	ta.locals = make([]tlocal, len(cd.localOff))
	pos := uint32(0)
	for i, off := range cd.localOff {
		ti := cd.localTI[i]
		lo, hi := off, off+uint32(ti.size)
		ta.locals[i] = tlocal{lo: lo, hi: hi, linear: linearOwner(ti), user: cd.body.Locals[i].Kind == mir.LocalUser}
		if hi == lo {
			continue
		}
		if lo > pos {
			ta.segs = append(ta.segs, tseg{lo: pos, hi: lo, stack: ownerOnly(false), local: -1})
		}
		ta.segs = append(ta.segs, tseg{lo: lo, hi: hi, stack: ownerOnly(false), local: int32(i)})
		pos = hi
	}
	if pos < ta.size {
		ta.segs = append(ta.segs, tseg{lo: pos, hi: ta.size, stack: ownerOnly(false), local: -1})
	}
	tg.calls++
	tg.frames = append(tg.frames, tframe{call: tg.calls})
	for int(tg.calls) >= len(tg.callItems) {
		tg.callItems = append(tg.callItems, types.NoItem)
	}
	tg.callItems[tg.calls] = f.item
}

// linearOwner reports whether a local of ti owns a linear value while its
// bytes are written: a linear named type (an optional or an error union of
// one may hold none or an error, so its bytes say nothing; the linear rule
// there is borrow's, through the payload's own local).
func linearOwner(ti *tinfo) bool {
	if ti.layout.Shape.Kind != types.Linear {
		return false
	}
	switch ti.ty.Kind {
	case types.KOptional, types.KErrorUnion, types.KErrorSet:
		return false
	}
	return true
}

// tgEnter is the entry retag of frame f: each parameter of type &T,
// mut T, []T, mut []T or str is retagged from its argument's tag over its
// referent, protected by the call until the frame pops; the parameter's
// word then carries the new tag. A reborrowed argument (ArgReborrow) was
// copied by callBody: this retag is the reborrow, so none is made at the
// call site as well.
func (m *Machine) tgEnter(f *frame) {
	tg := m.tg
	call := tg.frames[len(tg.frames)-1].call
	for i := 1; i <= int(f.code.body.ParamCount); i++ {
		pl := f.local(mir.LocalID(i))
		ty := pl.ti.ty
		al := m.mem.at(pl.a)
		if al.ptr == nil || !bitAt(al.ptr, pl.off) {
			continue
		}
		w := types.LoadUint(al.bytes[pl.off:], 8)
		tag := al.ptag[pl.off]
		var nt uint32
		switch ty.Kind {
		case types.KRef, types.KMut:
			el := m.ti(ty.Elem)
			nt = m.retag(loc{a: ptrAlloc(w), off: ptrOff(w), ti: el, tag: tag}, el.size, el, 1, ty.Kind == types.KMut, call)
		case types.KSlice, types.KMutSlice, types.KStr:
			n := types.LoadUint(al.bytes[pl.off+8:], 8)
			el := m.ti(types.U8)
			if ty.Kind != types.KStr {
				el = m.ti(ty.Elem)
			}
			bytes, ok := mulBytes(n, el.size)
			if !ok {
				continue // plain mode refuses any access through it
			}
			nt = m.retag(loc{a: ptrAlloc(w), off: ptrOff(w), ti: el, tag: tag}, bytes, el, n, ty.Kind == types.KMutSlice, call)
		default:
			continue
		}
		m.mem.at(pl.a).setPtag(pl.off, nt)
	}
}

// mulBytes is n·size when it fits an allocation.
func mulBytes(n, size uint64) (uint64, bool) {
	if size != 0 && n > maxAlloc/size {
		return 0, false
	}
	return n * size, true
}

// tgPopFrame checks that no linear local of the frame still owns its value
// (the return place's value goes to the caller), ends the call's protectors
// and records where the frame died.
func (m *Machine) tgPopFrame(f *frame) {
	tg := m.tg
	ta := tg.rec(f.base)
	for i := 1; i < len(ta.locals); i++ {
		if l := &ta.locals[i]; l.linear && l.owned {
			m.violation(Violation{Kind: VLinear, Where: m.localName(f.code.body, f.item, i), Off: l.lo, Len: l.hi - l.lo})
			l.owned = false
		}
	}
	tf := tg.frames[len(tg.frames)-1]
	for _, p := range tf.prot {
		pa := tg.rec(p.alloc)
		if !pa.made || len(pa.segs) == 0 || p.hi > pa.size {
			continue
		}
		for k := pa.find(p.lo); k < len(pa.segs) && pa.segs[k].lo < p.hi; k++ {
			st := pa.segs[k].stack
			for q := range st {
				if st[q].tag == p.tag && st[q].prot == tf.call {
					st[q].prot = 0
				}
			}
		}
	}
	tg.frames = tg.frames[:len(tg.frames)-1]
	ta.dead, ta.died = true, f.curSpan()
}

// tgScopeEnd is StScopeEnd x: the linear rule first (a linear local still
// owning its value is VLinear), then the local's bytes end and its
// activation count moves on, so every tag made in this activation is dead.
func (m *Machine) tgScopeEnd(f *frame, x mir.LocalID) {
	ta := m.tg.rec(f.base)
	if int(x) >= len(ta.locals) {
		return
	}
	l := &ta.locals[x]
	if l.linear && l.owned {
		m.violation(Violation{Kind: VLinear, Where: m.localName(f.code.body, f.item, int(x)), Off: l.lo, Len: l.hi - l.lo})
	}
	l.owned = false
	if l.hi == l.lo {
		return
	}
	i, j := ta.split(l.lo, l.hi)
	for k := i; k < j; k++ {
		s := &ta.segs[k]
		s.ended = true
		s.gen++
		s.stack = ownerOnly(false)
	}
	ta.merge()
}

// tgMoved runs after an operand moved a non-plain value out of place p
// (located at src): with no dereference in p, the moved bytes are no longer
// written and p's local no longer owns a linear value. A move
// through a dereference does not occur in safe MIR (E0409).
//
// A move of a plain field out of a local is a take that consumes the whole
// value (destructuring consumes): lower ends a destructure whose bindings all
// copy with one such take (lower/stmt.go destructure), and borrow reads it as
// the consumption, so the local no longer owns a linear value; the plain bytes
// stay written.
func (m *Machine) tgMoved(f *frame, p mir.Place, src loc) {
	plain := src.ti.layout.Shape.Kind == types.Plain || src.ti.size == 0
	if plain && p.Proj.Len == 0 {
		return
	}
	b := f.code.body
	for i := p.Proj.Start; i < p.Proj.End(); i++ {
		if b.Projections[i].Kind == mir.ProjDeref {
			return
		}
	}
	ta := m.tg.rec(f.base)
	if !ta.frame || src.a != f.base {
		return
	}
	if !plain {
		clearBits(ta.init, src.off, src.off+uint32(src.ti.size))
	}
	if int(p.Local) < len(ta.locals) {
		ta.locals[p.Local].owned = false
	}
}

// tgConsumed is StConsume p: like a take (tgMoved), it consumes the whole
// value, so p's local no longer owns a linear value. p is a struct with no
// fields: no bytes stop being written.
func (m *Machine) tgConsumed(f *frame, p mir.Place) {
	b := f.code.body
	for i := p.Proj.Start; i < p.Proj.End(); i++ {
		if b.Projections[i].Kind == mir.ProjDeref {
			return
		}
	}
	ta := m.tg.rec(f.base)
	if ta.frame && int(p.Local) < len(ta.locals) {
		ta.locals[p.Local].owned = false
	}
}

// tgArgUndo records, before a fallible call's argument a moves out of a
// place of the caller's frame, that place's init bits and its local's
// ownership in the callee's tframe (tgErrorEdge).
func (m *Machine) tgArgUndo(caller *frame, a *mir.Arg) {
	op := &a.Operand
	if op.Kind != mir.OpMove && !(a.Mode == mir.ArgMove && op.Kind == mir.OpCopy) {
		return
	}
	b := caller.code.body
	for i := op.Place.Proj.Start; i < op.Place.Proj.End(); i++ {
		if b.Projections[i].Kind == mir.ProjDeref {
			return
		}
	}
	src := m.place(caller, op.Place)
	ta := m.tg.rec(caller.base)
	if !ta.frame || src.a != caller.base || src.ti.size == 0 || uint64(src.off)+src.ti.size > uint64(ta.size) {
		return
	}
	// An argument slot (t = move x, then the call takes move t) restores x
	// with what t holds at the call, and t stays moved, as borrow's slot
	// restore does (internal/borrow/slots.go restoreSlot).
	dst := src
	local := op.Place.Local
	if op.Place.Proj.Len == 0 && int(local) < len(ta.slot) && ta.slot[local] != 0 {
		x := ta.slot[local] - 1
		if xl := caller.local(x); xl.a == caller.base && xl.ti.size == src.ti.size {
			dst, local = xl, x
		}
	}
	u := argUndo{local: local, lo: dst.off, hi: dst.off + uint32(src.ti.size)}
	if int(op.Place.Local) < len(ta.locals) {
		u.owned = ta.locals[op.Place.Local].owned
	}
	u.init = make([]bool, u.hi-u.lo)
	for o := uint32(0); o < u.hi-u.lo; o++ {
		u.init[o] = bitAt(ta.init, src.off+o)
	}
	tf := &m.tg.frames[len(m.tg.frames)-1]
	tf.undo = append(tf.undo, u)
}

// tgSlot records whether whole local t of frame f last took its value by a
// whole move out of another local (tagAlloc.slot).
func (m *Machine) tgSlot(f *frame, t mir.LocalID, op *mir.Operand) {
	ta := m.tg.rec(f.base)
	if !ta.frame || int(t) >= len(ta.locals) {
		return
	}
	src := mir.LocalID(0)
	if op.Kind == mir.OpMove && op.Place.Proj.Len == 0 && op.Place.Local != t && f.code.body.Locals[t].Kind == mir.LocalTemp {
		src = op.Place.Local + 1
	}
	if src == 0 && ta.slot == nil {
		return
	}
	if ta.slot == nil {
		ta.slot = make([]mir.LocalID, len(ta.locals))
	}
	ta.slot[t] = src
}

// tgErrorEdge is the error edge of a fallible call (a fallible call
// consumes its arguments only on success): the callee's by-value
// parameters, unchanged at every error exit (E0411), go back to the caller,
// so they no longer own their values; and the caller's places its arguments
// moved out of get back the init bits and ownership they had before the call
// (the caller's bytes were never touched: the callee got a copy).
func (m *Machine) tgErrorEdge(f, caller *frame) {
	m.tgErrorExit(f)
	tf := &m.tg.frames[len(m.tg.frames)-1]
	if len(tf.undo) == 0 {
		return
	}
	ca := m.tg.rec(caller.base)
	for _, u := range tf.undo {
		for o := u.lo; o < u.hi; o++ {
			if u.init[o-u.lo] {
				setBit(ca.init, o)
			} else {
				clearBit(ca.init, o)
			}
		}
		if int(u.local) < len(ca.locals) {
			ca.locals[u.local].owned = u.owned
		}
	}
	tf.undo = nil
}

// tgErrorExit is an exit of frame f that produces an error: its by-value
// parameters no longer own their values (they are the caller's).
func (m *Machine) tgErrorExit(f *frame) {
	ta := m.tg.rec(f.base)
	for i := 1; i <= int(f.code.body.ParamCount) && i < len(ta.locals); i++ {
		ta.locals[i].owned = false
	}
}

// tgMovedArgs empties the places an intrinsic's moved arguments came from:
// an intrinsic reads its arguments itself, or not at all (raw.Forget), so
// writeOp's move rule never sees them.
func (m *Machine) tgMovedArgs(f *frame, t *mir.Terminator) {
	for _, a := range f.code.body.Args[t.Args.Start:t.Args.End()] {
		if a.Operand.Kind == mir.OpMove || a.Mode == mir.ArgMove {
			if a.Operand.Kind != mir.OpCopy && a.Operand.Kind != mir.OpMove {
				continue
			}
			src := m.place(f, a.Operand.Place)
			m.tgMoved(f, a.Operand.Place, src)
		}
	}
}

// tgCopyInit gives the n bytes written at dst the init bits of the source
// at src when both are frames: a partly written value stays partly written.
func (m *Machine) tgCopyInit(dst, src loc, n uint64) {
	if n == 0 || !m.mem.isLive(dst.a) || !m.mem.isLive(src.a) {
		return
	}
	da, sa := m.tg.rec(dst.a), m.tg.rec(src.a)
	if !da.frame || !sa.frame || uint64(dst.off)+n > uint64(da.size) || uint64(src.off)+n > uint64(sa.size) {
		return
	}
	for o := uint32(0); o < uint32(n); o++ {
		if bitAt(sa.init, src.off+o) {
			setBit(da.init, dst.off+o)
		} else {
			clearBit(da.init, dst.off+o)
		}
	}
}

// tgCopy is the tagged half of a copy of n bytes from src to dst: a read of
// the source through its tag, a write of the destination through its tag,
// then (after the bytes moved) the init bits.
func (m *Machine) tgCopyCheck(dst, src loc, n uint64) {
	m.tacc(src.a, src.off, n, src.tag, accRead)
	m.tacc(dst.a, dst.off, n, dst.tag, accWrite)
}

// tgDied records where allocation a ended (a page freed, a frame popped), so
// a later access through a pointer into it is VDead with that span.
func (m *Machine) tgDied(a uint32) {
	ta := m.tg.rec(a)
	ta.made, ta.dead, ta.died = true, true, m.curSpan()
}

// tgStorePtr gives the pointer word storePtr wrote at (a, o) the tag in
// flight. A non-null pointer with none is an evaluator bug (a producer of a
// pointer value that did not say its tag), and so is a pointer into a frame
// with the owner's tag: only a borrow or a slice makes such a pointer, and
// each makes a fresh tag.
func (m *Machine) tgStorePtr(a, o uint32, p uint64) {
	if p == 0 {
		m.takeVal()
		return
	}
	t := m.tg.val
	m.tg.val = noVal
	if t == noVal {
		source.ICEf(m.curSpan(), "eval: tagged mode: a pointer stored with no tag in flight")
	}
	if t == 0 && m.mem.kindOf(ptrAlloc(p)) == allocFrame {
		source.ICEf(m.curSpan(), "eval: tagged mode: a pointer into a frame with the owner's tag")
	}
	m.mem.at(a).setPtag(o, t)
}

// where spells a location for a record.
func (m *Machine) where(a, off uint32) string {
	if ta := m.tg.known(a); ta != nil {
		if ta.frame {
			if l := ta.localAtAny(off); l >= 0 {
				return m.localName(ta.body, ta.fnItem, l)
			}
			return "frame of " + m.itemName(ta.fnItem)
		}
	}
	switch m.mem.kindOf(a) {
	case allocConst:
		return "const"
	case allocOwn:
		return "memory"
	}
	return fmt.Sprintf("allocation %d", a)
}

func (m *Machine) itemName(it types.ItemID) string {
	if it == types.NoItem {
		return "?"
	}
	return m.cfg.Interner.Str(m.items().Get(it).Name)
}

// localName is "local x of F" (a temporary: "temporary _3 of F").
func (m *Machine) localName(b *mir.Body, fn types.ItemID, i int) string {
	if b != nil && i < len(b.Locals) {
		l := &b.Locals[i]
		if l.Name != source.NoSymbol {
			return "local " + m.cfg.Interner.Str(l.Name) + " of " + m.itemName(fn)
		}
		if mir.LocalID(i) == mir.ReturnLocal {
			return "result of " + m.itemName(fn)
		}
	}
	return fmt.Sprintf("temporary _%d of %s", i, m.itemName(fn))
}

// localAtAny is the frame local whose bytes hold o, searched over the locals
// themselves (the segments may have merged across locals' padding).
func (ta *tagAlloc) localAtAny(o uint32) int {
	for i, l := range ta.locals {
		if l.lo <= o && o < l.hi {
			return i
		}
	}
	return -1
}
