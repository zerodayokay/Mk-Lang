// Why: abi takes layouts from its caller, so its tests need their own source
// of layouts for the suite's types.
// What: a small layout engine over the suite's declarations: sa, layout,
// structOf, tagged and compute on the test world.
// How: natural alignment, an optional pack cap, and tagged unions as
// { tag, union of variants }, an E!T's variants each at its own
// alignment. It is test code; abi itself lays out nothing
// but scalars and slices.

package abi

import (
	"mk/internal/source"
	"mk/internal/types"
)

func alignUp(n uint64, a uint32) uint64 {
	if a <= 1 {
		return n
	}
	return (n + uint64(a) - 1) / uint64(a) * uint64(a)
}

// sa is the size and alignment of any concrete type.
func (w *world) sa(t types.TypeID) (uint64, uint32) {
	ty := w.tt.Get(t)
	switch ty.Kind {
	case types.KVoid, types.KNever:
		return 0, 1
	case types.KBool:
		return 1, 1
	case types.KChar:
		return 4, 4
	case types.KInt, types.KFloat:
		return uint64(ty.Bits) / 8, uint32(ty.Bits) / 8
	case types.KIntPtr, types.KRef, types.KMut, types.KRawPtr, types.KFn:
		return 8, 8
	case types.KStr, types.KSlice, types.KMutSlice:
		return 16, 8
	}
	l := w.layout(t)
	return l.Shape.Size, l.Shape.Align
}

// layout is the LayoutOf the tests pass to abi.
func (w *world) layout(t types.TypeID) *types.Layout {
	w.mu.Lock()
	l, ok := w.layouts[t]
	w.mu.Unlock()
	if ok {
		return l
	}
	l = w.compute(t)
	w.mu.Lock()
	w.layouts[t] = l
	w.mu.Unlock()
	return l
}

// structOf lays out fields in order with natural alignment, capped at pack
// when pack is not 0.
func (w *world) structOf(ts []types.TypeID, pack uint32) *types.Layout {
	l := &types.Layout{}
	off, align := uint64(0), uint32(1)
	for _, ft := range ts {
		s, a := w.sa(ft)
		if pack != 0 {
			a = min(a, pack)
		}
		off = alignUp(off, a)
		l.Fields = append(l.Fields, types.FieldLayout{Offset: off, Type: ft})
		off += s
		align = max(align, a)
	}
	l.Shape.Size, l.Shape.Align = alignUp(off, align), align
	return l
}

// tagged lays out { tag, union of variants }: each variant a struct of its
// fields, the union at the tag's end rounded up to the union's alignment,
// or with own (an E!T or an error set, as check lays them out) each variant
// at the tag's end rounded up to its own alignment.
func (w *world) tagged(tagSize uint8, tagAlign uint32, own bool, discrs []uint64, variants [][]types.TypeID) *types.Layout {
	var vs []*types.Layout
	usize, ualign := uint64(0), uint32(1)
	for _, fs := range variants {
		v := w.structOf(fs, 0)
		vs = append(vs, v)
		usize, ualign = max(usize, v.Shape.Size), max(ualign, v.Shape.Align)
	}
	p := alignUp(uint64(tagSize), ualign)
	align := max(tagAlign, ualign)
	l := &types.Layout{HasTag: true, Tag: types.TagLayout{Size: tagSize}}
	l.Shape.Size, l.Shape.Align = alignUp(p+usize, align), align
	if own {
		end := uint64(tagSize)
		for _, v := range vs {
			end = max(end, alignUp(uint64(tagSize), v.Shape.Align)+v.Shape.Size)
		}
		l.Shape.Size = alignUp(end, align)
	}
	for i, v := range vs {
		vl := types.VariantLayout{Discr: discrs[i]}
		at := p
		if own {
			at = alignUp(uint64(tagSize), v.Shape.Align)
		}
		for _, f := range v.Fields {
			vl.Fields = append(vl.Fields, types.FieldLayout{Offset: at + f.Offset, Type: f.Type})
		}
		l.Variants = append(l.Variants, vl)
	}
	return l
}

func (w *world) compute(t types.TypeID) *types.Layout {
	ty := w.tt.Get(t)
	switch ty.Kind {
	case types.KArray:
		s, a := w.sa(ty.Elem)
		return &types.Layout{Shape: types.Shape{Size: s * w.arrayN[t], Align: a}}
	case types.KRecord:
		var ts []types.TypeID
		for _, f := range ty.Fields {
			ts = append(ts, f.Type)
		}
		return w.structOf(ts, 0)
	case types.KOptional:
		if types.NicheOptional(w.tt.Kind(ty.Elem)) {
			return &types.Layout{Shape: types.Shape{Size: 8, Align: 8}, HasTag: true,
				Tag:      types.TagLayout{Size: 8, Niche: true},
				Variants: []types.VariantLayout{{Discr: 0}, {Discr: 1, Fields: []types.FieldLayout{{Offset: 0, Type: ty.Elem}}}}}
		}
		return w.tagged(1, 1, false, []uint64{0, 1}, [][]types.TypeID{nil, {ty.Elem}})
	case types.KErrorUnion, types.KErrorSet:
		var discrs []uint64
		var vs [][]types.TypeID
		if ty.Kind == types.KErrorUnion {
			discrs = append(discrs, 0)
			if ty.Elem == types.Void {
				vs = append(vs, nil)
			} else {
				vs = append(vs, []types.TypeID{ty.Elem})
			}
		}
		for _, m := range w.tt.Members(ty.Errors) {
			it := w.items.Get(m.Item)
			d := it.Decl.(*types.ErrorDecl)
			discrs = append(discrs, uint64(d.Code))
			var fs []types.TypeID
			for _, f := range d.Fields {
				fs = append(fs, f.Type)
			}
			vs = append(vs, fs)
		}
		return w.tagged(4, 4, true, discrs, vs)
	case types.KNamed:
		switch d := w.items.Get(ty.Item).Decl.(type) {
		case *types.StructDecl:
			var ts []types.TypeID
			for _, f := range d.Fields {
				ts = append(ts, f.Type)
			}
			return w.structOf(ts, d.Packed)
		case *types.EnumDecl:
			var discrs []uint64
			var vs [][]types.TypeID
			for _, v := range d.Variants {
				discrs = append(discrs, v.Discr)
				var fs []types.TypeID
				for _, f := range v.Fields {
					fs = append(fs, f.Type)
				}
				vs = append(vs, fs)
			}
			ts := types.EnumTagBytes(discrs)
			if d.Tag != types.NoType {
				s, _ := w.sa(d.Tag)
				ts = uint8(s)
			}
			return w.tagged(ts, uint32(ts), false, discrs, vs)
		}
	}
	source.ICEf(source.Span{}, "test layout: no layout for kind %s", ty.Kind)
	return nil
}
