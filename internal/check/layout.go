// Why: every later stage (borrow, mono, codegen, the cache) needs the size,
// alignment and field offsets of a closed type, computed once and the same
// way everywhere.
//
// What: Checker.Shape and Checker.Layout, sizeOnCycle, arrayLen, enumTagBytes
// and layoutWalk with its cases (scalar, structOf, optional, tagged4,
// taggedUnion, enum).
//
// How: a recursive walk memoized per TypeID (memo.go). Structs lay fields
// out in order with alignment padding (or packed); an optional of a niche
// element is one pointer, else {u8 some, T}; error unions and enums are
// tagged unions. A type on a by-value cycle (E0907) gets size zero.

package check

import (
	"mk/internal/source"
	"mk/internal/types"
)

// Shape returns the public shape of a closed type: size, alignment, kind,
// carrier, view, Send, Share, Freeze; Opaque is always false and ABI
// is left zero for abi. ICE on a type that mentions a type parameter
// or Infer.
func (c *Checker) Shape(t types.TypeID) types.Shape { return c.Layout(t).Shape }

// Layout returns the full layout of a closed type:
// field offsets, tag, variants. A type on a by-value cycle (E0907, reported
// by the declaration check) has the zero size.
func (c *Checker) Layout(t types.TypeID) types.Layout {
	if e := c.memo.layoutOf(t); e != nil {
		return e.layout
	}
	if !c.closed(t) {
		source.ICEf(source.Span{}, "check.Layout: %s mentions a type parameter", c.cfg.Types.TypeString(t, nil))
	}
	w := layoutWalk{c: c}
	l, _ := w.layout(t)
	return l
}

// sizeOnCycle reports whether closed t is on a by-value layout cycle
// (E0907 at its declaration), whose size Layout gives as zero: a fold of
// `SizeOf[t]()` must not read that zero (it would add a cascade E0706 on
// a program already rejected).
func (c *Checker) sizeOnCycle(t types.TypeID) bool {
	if c.memo.layoutOf(t) != nil {
		return false
	}
	w := layoutWalk{c: c}
	_, cyc := w.layout(t)
	return cyc
}

// layoutWalk lays out closed types; a type met again on its own by-value
// path is a cycle and lays out as zero, never memoized.
type layoutWalk struct {
	c     *Checker
	stack []types.TypeID
}

func alignUp(n uint64, a uint32) uint64 {
	if a <= 1 {
		return n
	}
	return (n + uint64(a) - 1) / uint64(a) * uint64(a)
}

func (w *layoutWalk) layout(t types.TypeID) (types.Layout, bool) {
	if e := w.c.memo.layoutOf(t); e != nil {
		return e.layout, false
	}
	for _, s := range w.stack {
		if s == t {
			return types.Layout{Shape: types.Shape{Align: 1}}, true
		}
	}
	w.stack = append(w.stack, t)
	l, cyc := w.compute(t)
	w.stack = w.stack[:len(w.stack)-1]
	if cyc {
		return l, true
	}
	f := w.c.Facts(t)
	l.Shape.Kind, l.Shape.Carrier, l.Shape.View = f.Kind, f.Carrier, f.View
	l.Shape.MutView, l.Shape.LoanInterior = f.MutView, f.LoanInterior
	l.Shape.Send, l.Shape.Share, l.Shape.Freeze = f.Send, f.Share, f.Freeze
	return w.c.memo.putLayout(t, layoutEntry{shape: l.Shape, layout: l}).layout, false
}

// sizeAlign is a scalar's size and alignment.
func (w *layoutWalk) scalar(size uint64, align uint32) (types.Layout, bool) {
	return types.Layout{Shape: types.Shape{Size: size, Align: align}}, false
}

func (w *layoutWalk) compute(t types.TypeID) (types.Layout, bool) {
	c := w.c
	tab := c.cfg.Types
	ty := tab.Get(t)
	ptr := uint64(c.cfg.Target.PointerBytes)
	if ptr == 0 {
		ptr = 8
	}
	switch ty.Kind {
	case types.KInt, types.KFloat:
		return w.scalar(uint64(ty.Bits/8), uint32(ty.Bits/8))
	case types.KIntPtr, types.KRef, types.KMut, types.KRawPtr, types.KFn:
		return w.scalar(ptr, uint32(ptr))
	case types.KBool:
		return w.scalar(1, 1)
	case types.KChar:
		return w.scalar(4, 4)
	case types.KVoid, types.KNever, types.KPoison:
		return w.scalar(0, 1)
	case types.KStr, types.KSlice, types.KMutSlice:
		return w.scalar(2*ptr, uint32(ptr))
	case types.KArray:
		e, cyc := w.layout(ty.Elem)
		n := c.arrayLen(ty.Len)
		return types.Layout{Shape: types.Shape{Size: n * e.Shape.Size, Align: e.Shape.Align}}, cyc
	case types.KOptional:
		return w.optional(ty.Elem)
	case types.KErrorUnion:
		return w.tagged4(ty.Errors, ty.Elem, true)
	case types.KErrorSet:
		return w.tagged4(ty.Errors, types.NoType, false)
	case types.KRecord:
		fts := make([]types.TypeID, len(ty.Fields))
		for i, f := range ty.Fields {
			fts[i] = f.Type
		}
		return w.structOf(fts, 0, 0)
	case types.KNamed:
		it := c.cfg.Items.Get(ty.Item)
		switch d := it.Decl.(type) {
		case *types.StructDecl:
			if ty.Item == c.known.Scope && len(ty.Args) == 1 && len(d.Generics) == 0 && c.cfg.Types.Kind(ty.Args[0].Type) == types.KErrorSet {
				// Scope[E] is { hdr: Scope, err: E!void }.
				es := c.cfg.Types.Get(ty.Args[0].Type).Errors
				return w.structOf([]types.TypeID{c.cfg.Types.Named(ty.Item, nil), c.cfg.Types.ErrorUnion(es, types.Void)}, 0, 0)
			}
			fts := make([]types.TypeID, len(d.Fields))
			for i, f := range d.Fields {
				fts[i] = c.subst(f.Type, ty.Args, types.NoType)
			}
			l, cyc := w.structOf(fts, d.Packed, 0)
			if d.Align > l.Shape.Align { // align(n) raises it, size rounded up
				l.Shape.Align = d.Align
				l.Shape.Size = alignUp(l.Shape.Size, d.Align)
			}
			return l, cyc
		case *types.EnumDecl:
			return w.enum(d, ty.Args)
		}
		source.ICEf(it.Span, "check.Layout: %s has no struct or enum declaration", it.Key)
	}
	source.ICEf(source.Span{}, "check.Layout: a %s type", ty.Kind)
	return types.Layout{}, false
}

// arrayLen is the value of a closed array length. A length below 0 is
// E0303, already reported where it was written; such a type can
// still reach a layout through an instance whose check waited for its
// declaration (negativeInstance's queue: `struct Buf[const N: int] { x:
// Buf[-1], y: [N]u8 }`, then `struct W { b: Buf[2] }`), so it lays out as
// zero elements, like Poison, never an ICE (FuzzCheck ffede09c4eb7f4e1).
func (c *Checker) arrayLen(a types.ConstArg) uint64 {
	switch a.Kind {
	case types.CAValue:
		n, ok := c.constInt(a.Value)
		if ok && n >= 0 {
			return uint64(n)
		}
		if ok {
			return 0 // reported as E0303
		}
	case types.CASizeOf:
		return c.Shape(a.Type).Size
	}
	source.ICEf(source.Span{}, "check: array length %+v is not a closed non-negative int", a)
	return 0
}

// lenWalk looks for an array length below 0 in a type, through its
// by-value and reference structure and the substituted fields of the named
// instances it reaches (negativeInstance). incomplete reports a
// declaration that is not done yet; seen bounds the walk.
type lenWalk struct {
	c          *Checker
	rec        recorder
	seen       map[types.TypeID]bool
	incomplete bool
}

func (w *lenWalk) negative(t types.TypeID) bool {
	c := w.c
	if t == types.NoType || w.seen[t] || len(w.seen) > 4096 {
		return false
	}
	w.seen[t] = true
	ty := c.cfg.Types.Get(t)
	switch ty.Kind {
	case types.KArray:
		if ty.Len.Kind == types.CAValue {
			if v, known := c.constInt(ty.Len.Value); known && v < 0 {
				return true
			}
		}
		return w.negative(ty.Elem)
	case types.KOptional, types.KErrorUnion, types.KRef, types.KMut, types.KSlice, types.KMutSlice, types.KRawPtr:
		return w.negative(ty.Elem)
	case types.KRecord:
		for _, f := range ty.Fields {
			if w.negative(f.Type) {
				return true
			}
		}
	case types.KNamed:
		for _, a := range ty.Args {
			if !a.IsConst && w.negative(a.Type) {
				return true
			}
		}
		it := c.cfg.Items.Get(ty.Item)
		if m := c.module(it.Module); m != nil && m.d != nil && !m.final && m.d.state[it.ID] == declBusy {
			// Decl is set before its fields are filled (declStruct).
			w.incomplete = true
			return false
		}
		var fs []types.FieldDecl
		switch d := c.declOfItem(it).(type) {
		case *types.StructDecl:
			fs = d.Fields
		case *types.EnumDecl:
			for _, v := range d.Variants {
				fs = append(fs, v.Fields...)
			}
		case nil:
			w.incomplete = true
			return false
		}
		w.rec.shape(it.Key)
		for _, f := range fs {
			if w.negative(c.subst(f.Type, ty.Args, types.NoType)) {
				return true
			}
		}
	}
	return false
}

// structOf lays out fields in order at the next multiple of their alignment
// (packed caps every alignment), starting at offset base; the struct's
// alignment is the maximum, its size rounded up.
func (w *layoutWalk) structOf(fts []types.TypeID, packed uint32, base uint64) (types.Layout, bool) {
	l := types.Layout{Shape: types.Shape{Align: 1}}
	off := base
	cyc := false
	for _, ft := range fts {
		fl, c := w.layout(ft)
		cyc = cyc || c
		a := fl.Shape.Align
		if packed != 0 && a > packed {
			a = packed
		}
		off = alignUp(off, a)
		l.Fields = append(l.Fields, types.FieldLayout{Offset: off, Type: ft})
		off += fl.Shape.Size
		l.Shape.Align = max(l.Shape.Align, a)
	}
	l.Shape.Size = alignUp(off, l.Shape.Align)
	return l, cyc
}

// optional is ?T: one pointer for a niche element, else {u8 some, T}.
func (w *layoutWalk) optional(elem types.TypeID) (types.Layout, bool) {
	e, cyc := w.layout(elem)
	ek := w.c.cfg.Types.Kind(elem)
	if types.NicheOptional(ek) {
		return types.Layout{Shape: types.Shape{Size: e.Shape.Size, Align: e.Shape.Align}, HasTag: true,
			Tag:      types.TagLayout{Offset: 0, Size: uint8(e.Shape.Size), Niche: true},
			Variants: []types.VariantLayout{{Discr: 0}, {Discr: 1, Fields: []types.FieldLayout{{Offset: 0, Type: elem}}}}}, cyc
	}
	a := max(e.Shape.Align, 1)
	off := alignUp(1, a)
	return types.Layout{Shape: types.Shape{Size: alignUp(off+e.Shape.Size, a), Align: a}, HasTag: true,
		Tag:      types.TagLayout{Offset: 0, Size: 1},
		Variants: []types.VariantLayout{{Discr: 0}, {Discr: 1, Fields: []types.FieldLayout{{Offset: off, Type: elem}}}}}, cyc
}

// tagged4 is E!T (hasOk) or an error-set value: {u32 code, union}, the
// union sized and aligned to the largest of T and every error's payload.
// Variants: ok (code 0) first, then each error by its
// ErrorCode in the set's member order.
func (w *layoutWalk) tagged4(es types.ErrorSetID, ok types.TypeID, hasOk bool) (types.Layout, bool) {
	c := w.c
	type part struct {
		discr uint64
		fts   []types.TypeID
	}
	var parts []part
	if hasOk {
		parts = append(parts, part{0, []types.TypeID{ok}})
	}
	for _, m := range c.cfg.Types.Members(es) {
		if m.IsParam {
			source.ICEf(source.Span{}, "check.Layout: error set with a parameter")
		}
		it := c.cfg.Items.Get(m.Item)
		d, isErr := it.Decl.(*types.ErrorDecl)
		if !isErr {
			source.ICEf(it.Span, "check.Layout: error %s has no declaration", it.Key)
		}
		var fts []types.TypeID
		for _, f := range d.Fields {
			fts = append(fts, f.Type)
		}
		parts = append(parts, part{uint64(d.Code), fts})
	}
	return w.taggedUnion(4, func(yield func(uint64, []types.TypeID)) {
		for _, p := range parts {
			yield(p.discr, p.fts)
		}
	})
}

// taggedUnion lays out {tag of tagSize bytes, union of variant structs}:
// the union aligned to its largest member and placed after the tag; each
// variant's fields are offsets from the start of the whole value.
func (w *layoutWalk) taggedUnion(tagSize uint8, each func(yield func(uint64, []types.TypeID))) (types.Layout, bool) {
	type v struct {
		discr uint64
		l     types.Layout
	}
	var vs []v
	ua := uint32(1)
	cyc := false
	each(func(d uint64, fts []types.TypeID) {
		l, c := w.structOf(fts, 0, 0)
		cyc = cyc || c
		vs = append(vs, v{d, l})
		ua = max(ua, l.Shape.Align)
	})
	var usize uint64
	for _, x := range vs {
		usize = max(usize, x.l.Shape.Size)
	}
	ta := uint32(tagSize)
	uoff := alignUp(uint64(tagSize), ua)
	align := max(ta, ua)
	if usize == 0 {
		uoff = uint64(tagSize)
	}
	out := types.Layout{Shape: types.Shape{Size: alignUp(uoff+usize, align), Align: align}, HasTag: true,
		Tag: types.TagLayout{Offset: 0, Size: tagSize}}
	for _, x := range vs {
		vl := types.VariantLayout{Discr: x.discr}
		for _, f := range x.l.Fields {
			vl.Fields = append(vl.Fields, types.FieldLayout{Offset: uoff + f.Offset, Type: f.Type})
		}
		out.Variants = append(out.Variants, vl)
	}
	return out, cyc
}

// enum is a payload-free enum (the tag alone) or {tag, union}; the tag is
// the declared type, else the smallest of u8, u16, u32 that holds every
// discriminant.
func (w *layoutWalk) enum(d *types.EnumDecl, args []types.GenericArg) (types.Layout, bool) {
	c := w.c
	tagSize := c.enumTagBytes(d)
	return w.taggedUnion(tagSize, func(yield func(uint64, []types.TypeID)) {
		for _, v := range d.Variants {
			fts := make([]types.TypeID, len(v.Fields))
			for i, f := range v.Fields {
				fts[i] = c.subst(f.Type, args, types.NoType)
			}
			yield(v.Discr, fts)
		}
	})
}

// enumTagBytes is an enum's tag width; 0 never reaches a layout (the
// declaration check reports a discriminant past u32 as E0303).
func (c *Checker) enumTagBytes(d *types.EnumDecl) uint8 {
	if d.Tag != types.NoType {
		return uint8(c.Shape(d.Tag).Size)
	}
	ds := make([]uint64, len(d.Variants))
	for i, v := range d.Variants {
		ds[i] = v.Discr
	}
	n := types.EnumTagBytes(ds)
	if n == 0 {
		n = 4
	}
	return n
}
