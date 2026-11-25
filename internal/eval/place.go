// Why: every read and write of the interpreter goes through a MIR place, so
// this is where field offsets, dereferences, bounds and variant checks are
// done once, for both the plain and the tagged mode.
//
// What: loc (an allocation, offset, tag and type), place, index, elemOff,
// downcast, sliceWords, load, store, writeOp, copyValue and convertErr.
//
// How: a place is resolved step by step: a field adds its offset, a
// dereference loads the pointer word, an index checks its bound without
// wrapping, a downcast checks the variant's tag. A downcast yields its base
// type; SelErr yields an error-set value view; a payload is the field step
// after the downcast. Every access is checked by memory.go.

package eval

import (
	"math"
	"math/bits"

	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// loc is a typed location in evaluation memory.
type loc struct {
	a, off uint32
	// tag is the tagged mode's tag of the reference the place went through
	// (0: the owner, a local named directly).
	tag uint32
	ti  *tinfo
	// view is set on an error-set value seen through SelErr inside an E!T:
	// the union whose layout holds the payloads.
	view *tinfo
}

// local is the location of a whole local of frame f.
func (f *frame) local(l mir.LocalID) loc {
	return loc{a: f.base, off: f.code.localOff[l], ti: f.code.localTI[l]}
}

// place resolves a place of frame f.
func (m *Machine) place(f *frame, p mir.Place) loc {
	cur := f.local(p.Local)
	if p.Proj.Len == 0 {
		return cur
	}
	b := f.code.body
	// Resolving a place leaves the tagged mode's pointer in flight as it
	// found it: the words its dereferences read are taken here.
	saved := noVal
	if m.tg != nil {
		saved = m.tg.val
	}
	var down *mir.Projection
	for i := p.Proj.Start; i < p.Proj.End(); i++ {
		pr := &b.Projections[i]
		switch pr.Kind {
		case mir.ProjField:
			if pr.FieldLoop {
				source.ICEf(b.Spans[0], "eval: fields-loop step in an instance body")
			}
			if down == nil && cur.ti.ty.Kind == types.KArray {
				// A constant element step: element A of the array.
				ti := f.code.projTI[i]
				cur = loc{a: cur.a, off: elemOff(cur.off, uint64(pr.A), ti.size), tag: cur.tag, ti: ti}
				break
			}
			fl := m.fieldLayout(cur, down, pr.A)
			cur = loc{a: cur.a, off: cur.off + uint32(fl.Offset), tag: cur.tag, ti: f.code.projTI[i]}
		case mir.ProjDeref:
			// Reading the reference is a use of the loan that holds it; the
			// place continues with the word's own tag.
			w := m.load(loc{a: cur.a, off: cur.off, tag: cur.tag, ti: cur.ti})
			if w == 0 {
				refuse(types.RefuseRawAccess, "null")
			}
			cur = loc{a: ptrAlloc(w), off: ptrOff(w), tag: m.takeVal(), ti: f.code.projTI[i]}
		case mir.ProjIndex:
			cur = m.index(f, cur, mir.LocalID(pr.A), f.code.projTI[i])
		case mir.ProjDowncast:
			cur = m.downcast(cur, pr)
		default:
			source.ICEf(b.Spans[0], "eval: projection kind %d", pr.Kind)
		}
		down = nil
		if pr.Kind == mir.ProjDowncast {
			down = pr
		}
	}
	if m.tg != nil {
		m.tg.val = saved
	}
	return cur
}

// fieldLayout is field i of the value at cur, after the downcast down (or
// none).
func (m *Machine) fieldLayout(cur loc, down *mir.Projection, i uint32) types.FieldLayout {
	l := cur.ti.layout
	if cur.view != nil {
		l = cur.view.layout
	}
	pick := func(fs []types.FieldLayout) types.FieldLayout {
		if int(i) >= len(fs) {
			source.ICEf(source.Span{}, "eval: field %d of %s, which has %d", i, m.tab().TypeString(cur.ti.id, nil), len(fs))
		}
		return fs[i]
	}
	if down != nil {
		switch down.Sel {
		case mir.SelSome:
			if cur.ti.class == cPtr {
				return types.FieldLayout{Offset: 0, Type: cur.ti.ty.Elem}
			}
			return pick(l.Variants[1].Fields)
		case mir.SelOk:
			return pick(l.Variants[0].Fields)
		case mir.SelEnum:
			return pick(l.Variants[down.A].Fields)
		case mir.SelCase:
			return pick(m.variantByDiscr(l, uint64(m.errorCode(types.ItemID(down.A)))).Fields)
		}
		source.ICEf(source.Span{}, "eval: field after downcast %d", down.Sel)
	}
	switch cur.ti.ty.Kind {
	case types.KErrorSet:
		ms := m.tab().Members(cur.ti.ty.Errors)
		if len(ms) != 1 || ms[0].IsParam {
			source.ICEf(source.Span{}, "eval: field of an error set of %d", len(ms))
		}
		return pick(m.variantByDiscr(l, uint64(m.errorCode(ms[0].Item))).Fields)
	}
	return pick(l.Fields)
}

func (m *Machine) variantByDiscr(l *types.Layout, d uint64) *types.VariantLayout {
	for i := range l.Variants {
		if l.Variants[i].Discr == d {
			return &l.Variants[i]
		}
	}
	panicWith(types.PanicUnreachable, "")
	return nil
}

// errorCode is an error item's run-time identity.
func (m *Machine) errorCode(it types.ItemID) uint32 {
	if d, ok := m.items().Get(it).Decl.(*types.ErrorDecl); ok && d.Code != 0 {
		return d.Code
	}
	return types.ErrorCode(m.items().Get(it).Key)
}

// index resolves element idx of the array, slice or str at cur (PanicBounds
// when out of range).
func (m *Machine) index(f *frame, cur loc, idx mir.LocalID, elem *tinfo) loc {
	il := f.local(idx)
	i := m.load(il)
	if il.ti.signed && int64(signExtend(i, il.ti.width)) < 0 {
		panicWith(types.PanicBounds, "")
	}
	switch cur.ti.ty.Kind {
	case types.KArray:
		n := m.arrayLen(cur.ti.ty)
		if i >= n {
			panicWith(types.PanicBounds, "")
		}
		return loc{a: cur.a, off: elemOff(cur.off, i, elem.size), tag: cur.tag, ti: elem}
	case types.KSlice, types.KMutSlice, types.KStr:
		data, n := m.sliceWords(cur)
		t := m.takeVal()
		if i >= n {
			panicWith(types.PanicBounds, "")
		}
		return loc{a: ptrAlloc(data), off: elemOff(ptrOff(data), i, elem.size), tag: t, ti: elem}
	}
	source.ICEf(source.Span{}, "eval: index of %s", m.tab().TypeString(cur.ti.id, nil))
	return loc{}
}

// elemOff is the offset of element i of size bytes from off, computed without
// wrapping: an offset beyond 32 bits lies outside every allocation (maxAlloc),
// so it is RefuseRawAccess at once, never a wrapped offset inside the
// allocation. A length word built by raw.Slice can hold any length, so i < len
// bounds nothing.
func elemOff(off uint32, i, size uint64) uint32 {
	hi, lo := bits.Mul64(i, size)
	sum, carry := bits.Add64(lo, uint64(off), 0)
	if hi != 0 || carry != 0 || sum > math.MaxUint32 {
		refuse(types.RefuseRawAccess, "")
	}
	return uint32(sum)
}

// sliceWords reads the data pointer and length of a slice or str value.
//
// In the tagged mode it is a read through l's tag, and it puts the data
// word's tag in flight.
func (m *Machine) sliceWords(l loc) (uint64, uint64) {
	if m.tg != nil {
		m.tacc(l.a, l.off, 16, l.tag, accScalar)
	}
	al := m.mem.access(l.a, l.off, 16, false)
	p, n := al.loadPtr(l.off), al.loadScalar(l.off+8, 8)
	if m.tg != nil {
		m.tg.val = al.ptag[l.off]
	}
	return p, n
}

// arrayLen is the length of an array type.
func (m *Machine) arrayLen(ty *types.Type) uint64 {
	return m.constArg(ty.Len)
}

// constArg is the value of a ConstArg as an unsigned word.
func (m *Machine) constArg(ca types.ConstArg) uint64 {
	switch ca.Kind {
	case types.CAValue:
		v := m.cfg.Consts.Get(ca.Value)
		return types.LoadUint(v.Bytes, len(v.Bytes))
	case types.CASizeOf:
		return m.ti(ca.Type).size
	}
	source.ICEf(source.Span{}, "eval: const argument of kind %d in an instance body", ca.Kind)
	return 0
}

// downcast checks the variant of the value at cur and returns the view the
// steps after it use: the base type, or for SelErr the error-set
// value view of the union.
func (m *Machine) downcast(cur loc, pr *mir.Projection) loc {
	l := cur.ti.layout
	switch pr.Sel {
	case mir.SelSome:
		if cur.ti.class == cPtr {
			if m.load(cur) == 0 {
				panicWith(types.PanicUnreachable, "")
			}
			return cur
		}
		if m.tagAt(cur, l) != 1 {
			panicWith(types.PanicUnreachable, "")
		}
		return cur
	case mir.SelOk:
		if m.tagAt(cur, l) != 0 {
			panicWith(types.PanicUnreachable, "")
		}
		return cur
	case mir.SelErr:
		if m.tagAt(cur, l) == 0 {
			panicWith(types.PanicUnreachable, "")
		}
		return loc{a: cur.a, off: cur.off, tag: cur.tag, ti: m.ti(pr.Type), view: cur.ti}
	case mir.SelEnum:
		if m.tagAt(cur, l) != l.Variants[pr.A].Discr {
			panicWith(types.PanicUnreachable, "")
		}
		return cur
	case mir.SelCase:
		vl := l
		if cur.view != nil {
			vl = cur.view.layout
		}
		if m.tagAt(cur, vl) != uint64(m.errorCode(types.ItemID(pr.A))) {
			panicWith(types.PanicUnreachable, "")
		}
		return cur
	}
	source.ICEf(source.Span{}, "eval: downcast %d", pr.Sel)
	return loc{}
}

// tagAt reads the tag of a tagged value at cur with layout l.
func (m *Machine) tagAt(cur loc, l *types.Layout) uint64 {
	if !l.HasTag {
		source.ICEf(source.Span{}, "eval: tag of %s, which has none", m.tab().TypeString(cur.ti.id, nil))
	}
	if m.tg != nil {
		m.tacc(cur.a, cur.off+uint32(l.Tag.Offset), uint64(l.Tag.Size), cur.tag, accScalar)
	}
	al := m.mem.access(cur.a, cur.off+uint32(l.Tag.Offset), uint64(l.Tag.Size), false)
	if l.Tag.Niche {
		if al.loadPtr(cur.off+uint32(l.Tag.Offset)) != 0 {
			return 1
		}
		return 0
	}
	return al.loadScalar(cur.off+uint32(l.Tag.Offset), int(l.Tag.Size))
}

// load reads a scalar or pointer value at l, with the validity checks
// of bool, char and enum tags.
func (m *Machine) load(l loc) uint64 {
	ti := l.ti
	if m.tg != nil {
		m.tacc(l.a, l.off, uint64(ti.width), l.tag, accScalar)
	}
	al := m.mem.access(l.a, l.off, uint64(ti.width), false)
	if ti.class == cPtr {
		p := al.loadPtr(l.off)
		if m.tg != nil {
			m.tg.val = al.ptag[l.off] // the word's tag travels with it
		}
		return p
	}
	x := al.loadScalar(l.off, ti.width)
	switch ti.class {
	case cBool:
		if x > 1 {
			refuse(types.RefuseRawAccess, "bool")
		}
	case cChar:
		if x > 0x10ffff || (x >= 0xd800 && x <= 0xdfff) {
			refuse(types.RefuseRawAccess, "char")
		}
	case cInt:
		if ti.enum {
			m.checkTag(ti, x)
		}
	}
	return x
}

func (m *Machine) checkTag(ti *tinfo, x uint64) {
	for _, v := range ti.layout.Variants {
		if v.Discr == x {
			return
		}
	}
	panicWith(types.PanicEnumTag, "")
}

// store writes a scalar or pointer value at l.
func (m *Machine) store(l loc, x uint64) {
	ti := l.ti
	if m.tg != nil {
		m.tacc(l.a, l.off, uint64(ti.width), l.tag, accWrite)
	}
	al := m.mem.access(l.a, l.off, uint64(ti.width), true)
	if ti.class == cPtr {
		al.storePtr(l.off, x)
		if m.tg != nil {
			m.tgStorePtr(l.a, l.off, x)
		}
		return
	}
	al.storeScalar(l.off, ti.width, x)
}

// operandTI is the type info of an operand: a place's from the compiled
// body, no lookup; a const's or immediate's through the evaluation's table.
func (m *Machine) operandTI(f *frame, op *mir.Operand) *tinfo {
	if op.Kind == mir.OpCopy || op.Kind == mir.OpMove {
		if op.Place.Proj.Len == 0 {
			return f.code.localTI[op.Place.Local]
		}
		return f.code.projTI[op.Place.Proj.End()-1]
	}
	return m.ti(op.Type)
}

// scalar reads a scalar or pointer operand.
func (m *Machine) scalar(f *frame, op *mir.Operand) uint64 {
	switch op.Kind {
	case mir.OpImm:
		return op.Bits
	case mir.OpConst:
		ti := m.ti(op.Type)
		v := m.cfg.Consts.Get(op.Const)
		if len(v.Relocs) == 0 && ti.class != cPtr {
			return types.LoadUint(v.Bytes, ti.width)
		}
		return m.load(loc{a: m.constAlloc(op.Const), ti: ti})
	}
	return m.load(m.place(f, op.Place))
}

// operandLoc is where a memory-class operand's value is.
func (m *Machine) operandLoc(f *frame, op *mir.Operand) loc {
	switch op.Kind {
	case mir.OpConst:
		return loc{a: m.constAlloc(op.Const), ti: m.ti(op.Type)}
	case mir.OpImm:
		source.ICEf(source.Span{}, "eval: immediate of memory type")
	}
	return m.place(f, op.Place)
}

// writeOp writes an operand's value to dst.
func (m *Machine) writeOp(f *frame, op *mir.Operand, dst loc) {
	if dst.ti.class == cVoid {
		return
	}
	if m.tg != nil && op.Kind == mir.OpMove {
		// A move empties its place: the source's bytes are read,
		// then no longer written.
		src := m.operandLoc(f, op)
		if dst.ti.isScalar() {
			m.store(dst, m.load(src))
		} else {
			m.copyValue(dst, src)
		}
		// `x = x` moves the value into its own place: the place is written
		// again, so it stays written and owned (no false VUninit after
		// `h = h`).
		if dst.a != src.a || dst.off != src.off {
			m.tgMoved(f, op.Place, src)
		}
		return
	}
	if dst.ti.isScalar() {
		m.store(dst, m.scalar(f, op))
		return
	}
	m.copyValue(dst, m.operandLoc(f, op))
}

// copyValue copies the value at src into dst, converting an error-set value
// between layouts when they differ (a SelErr view, a widened set).
func (m *Machine) copyValue(dst, src loc) {
	if src.view != nil || dst.view != nil || (dst.ti != src.ti && isErrorKind(dst.ti) && isErrorKind(src.ti)) {
		m.convertErr(dst, src)
		return
	}
	if m.tg != nil {
		m.tgCopyCheck(dst, src, dst.ti.size)
		m.mem.copyBytes(dst.a, dst.off, src.a, src.off, dst.ti.size)
		m.tgCopyInit(dst, src, dst.ti.size)
		return
	}
	m.mem.copyBytes(dst.a, dst.off, src.a, src.off, dst.ti.size)
}

func isErrorKind(ti *tinfo) bool {
	return ti.ty.Kind == types.KErrorSet || ti.ty.Kind == types.KErrorUnion
}

// convertErr copies a tagged error value (E!T or an error set) between two
// layouts by its code: the code, then the fields of the variant with that
// discriminant, from the source's offsets to the destination's.
func (m *Machine) convertErr(dst, src loc) {
	sl, dl := src.ti.layout, dst.ti.layout
	if src.view != nil {
		sl = src.view.layout
	}
	if dst.view != nil {
		dl = dst.view.layout
	}
	code := m.tagAt(src, sl)
	sv, dv := m.variantByDiscr(sl, code), m.variantByDiscr(dl, code)
	if len(sv.Fields) != len(dv.Fields) {
		source.ICEf(source.Span{}, "eval: error variant %d has %d and %d fields", code, len(sv.Fields), len(dv.Fields))
	}
	size := dst.ti.size
	if dst.view != nil {
		size = dst.view.size
	}
	if m.tg != nil {
		ssize := src.ti.size
		if src.view != nil {
			ssize = src.view.size
		}
		m.tacc(src.a, src.off, ssize, src.tag, accRead)
		m.tacc(dst.a, dst.off, size, dst.tag, accWrite)
	}
	m.mem.access(dst.a, dst.off, size, true).fill(dst.off, uint32(size), 0)
	dal := m.mem.access(dst.a, dst.off+uint32(dl.Tag.Offset), uint64(dl.Tag.Size), true)
	dal.storeScalar(dst.off+uint32(dl.Tag.Offset), int(dl.Tag.Size), code)
	for i := range sv.Fields {
		ft := m.ti(dv.Fields[i].Type)
		m.mem.copyBytes(dst.a, dst.off+uint32(dv.Fields[i].Offset), src.a, src.off+uint32(sv.Fields[i].Offset), ft.size)
	}
}

func signExtend(x uint64, width int) uint64 {
	s := 64 - 8*uint(width)
	return uint64(int64(x<<s) >> s)
}
