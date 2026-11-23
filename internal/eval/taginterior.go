// Why: a write inside a Cell, Mutex or other interior type under shared
// loans is defined, so the tagged mode must know which bytes are interior;
// and codegen's `noalias readonly` rests on a type's Freeze fact, which a
// wrong shape would break.
//
// What: isInteriorItem, interior and walkInterior (the interior byte ranges
// of a type), mergeRanges, and markInterior.
//
// How: the tagged mode's own walk over a type's layout, through fields,
// array elements, enum payloads and optionals, never through a reference or
// a raw pointer, cached per TypeID. A type with interior bytes whose shape
// says Freeze is reported as VFacts, once per type.

package eval

import (
	"mk/internal/types"
)

// irange is one interior byte range [lo, hi) of a value, from its start.
type irange struct{ lo, hi uint32 }

// isInteriorItem reports whether a named item is one of the interior
// types: Cell, Mutex, raw.Atomic, Chan, SpinCore, Arena, Scope or any
// `mut view struct`.
func (e *Evaluator) isInteriorItem(it types.ItemID) bool {
	if it == types.NoItem {
		return false
	}
	k := &e.cfg.Known
	switch it {
	case k.Cell, k.Mutex, k.Atomic, k.Chan, k.SpinCore, k.Arena, k.Scope:
		return true
	}
	if e.cfg.Items != nil {
		if sd, ok := e.cfg.Items.Get(it).Decl.(*types.StructDecl); ok && sd.MutView {
			return true
		}
	}
	return false
}

// interior returns the interior ranges of ti's values, sorted and merged;
// nil when there are none. The walk is cached per type.
func (e *Evaluator) interior(ti *tinfo) []irange {
	e.tiMu.RLock()
	r, ok := e.inter[ti.id]
	e.tiMu.RUnlock()
	if ok {
		return r
	}
	var out []irange
	e.walkInterior(ti, 0, &out, 0)
	out = mergeRanges(out)
	e.tiMu.Lock()
	if e.inter == nil {
		e.inter = map[types.TypeID][]irange{}
	}
	e.inter[ti.id] = out
	e.tiMu.Unlock()
	return out
}

// walkInterior appends the interior ranges of a value of ti at base. depth
// bounds the walk against a malformed layout (a type is never its own field
// by value).
func (e *Evaluator) walkInterior(ti *tinfo, base uint32, out *[]irange, depth int) {
	if ti.size == 0 || depth > 64 {
		return
	}
	ty := ti.ty
	switch ty.Kind {
	case types.KRef, types.KMut, types.KRawPtr, types.KFn, types.KSlice, types.KMutSlice, types.KStr:
		return // never through a reference or a raw pointer
	case types.KNamed:
		if e.isInteriorItem(ty.Item) {
			*out = append(*out, irange{base, base + uint32(ti.size)})
			return
		}
	case types.KArray:
		el := e.tinfo(ty.Elem)
		if el.size == 0 {
			return
		}
		var one []irange
		e.walkInterior(el, 0, &one, depth+1)
		if len(one) == 0 {
			return
		}
		n := ti.size / el.size
		for i := uint64(0); i < n; i++ {
			off := base + uint32(i*el.size)
			for _, r := range one {
				*out = append(*out, irange{off + r.lo, off + r.hi})
			}
		}
		return
	}
	l := ti.layout
	for _, f := range l.Fields {
		e.walkInterior(e.tinfo(f.Type), base+uint32(f.Offset), out, depth+1)
	}
	for _, v := range l.Variants {
		for _, f := range v.Fields {
			e.walkInterior(e.tinfo(f.Type), base+uint32(f.Offset), out, depth+1)
		}
	}
}

// mergeRanges sorts ranges by start and joins overlapping or adjacent ones.
func mergeRanges(rs []irange) []irange {
	if len(rs) < 2 {
		return rs
	}
	for i := 1; i < len(rs); i++ { // insertion sort: walks emit nearly sorted ranges
		for j := i; j > 0 && rs[j].lo < rs[j-1].lo; j-- {
			rs[j], rs[j-1] = rs[j-1], rs[j]
		}
	}
	out := rs[:1]
	for _, r := range rs[1:] {
		p := &out[len(out)-1]
		if r.lo <= p.hi {
			p.hi = max(p.hi, r.hi)
			continue
		}
		out = append(out, r)
	}
	return out
}

// markInterior marks the interior bytes of count values of ti from off of
// allocation ta, and reports VFacts once per type when ti's shape says
// Freeze.
func (m *Machine) markInterior(ta *tagAlloc, off uint32, ti *tinfo, count uint64) {
	rs := m.ev.interior(ti)
	if len(rs) == 0 {
		return
	}
	if ti.layout.Shape.Freeze && !m.tg.facts[ti.id] {
		m.tg.facts[ti.id] = true
		m.violation(Violation{Kind: VFacts, Where: "type " + m.tab().TypeString(ti.id, nil), Off: off, Len: uint32(ti.size)})
	}
	for i := uint64(0); i < count; i++ {
		base := off + uint32(i*ti.size)
		for _, r := range rs {
			lo, hi := base+r.lo, base+r.hi
			if hi > ta.size || lo >= hi {
				continue
			}
			a, b := ta.split(lo, hi)
			for k := a; k < b; k++ {
				ta.segs[k].interior = true
			}
		}
	}
}
