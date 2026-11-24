// Why: a type's shape (size, alignment, ABI class, kind and marks) and its
// layout are public facts other modules compile against; the cache stores
// and hashes them apart from the type's declaration.
//
// What: writeShape, writeTerm and writeLayout with their readers, and the
// hashes Hasher.Shape and Hasher.ShapeTerm (mk.shape) and Hasher.Layout
// (mk.layout).
//
// How: a non-generic exported type has one shape record; a generic type a
// shape term over its parameters, encoded as the producer gave it. Every
// field is written in declaration order; decoding bounds term depth.

package iface

import (
	"mk/internal/source"
	"mk/internal/types"
)

// writeShape writes a shape record: size, align, ABI class (kind, count,
// byval, the four parts), kind, carrier, view, opaque, send, share, freeze.
func (w *walker) writeShape(s *types.Shape) {
	e := w.e
	e.Uvarint(s.Size)
	e.Uvarint(uint64(s.Align))
	e.Uvarint(uint64(s.ABI.Kind))
	e.Uvarint(uint64(s.ABI.Count))
	e.Bool(s.ABI.Byval)
	for _, p := range s.ABI.Parts {
		e.Uvarint(uint64(p.Class))
		e.Uvarint(uint64(p.Bytes))
	}
	e.Uvarint(uint64(s.Kind))
	e.Bool(s.Carrier)
	e.Bool(s.View)
	e.Bool(s.MutView)
	e.Bool(s.PlainView)
	e.Bool(s.LoanInterior)
	e.Bool(s.Opaque)
	e.Bool(s.Send)
	e.Bool(s.Share)
	e.Bool(s.Freeze)
}

// writeTerm writes a shape term: its kind, then every field in declaration
// order, recursively.
func (w *walker) writeTerm(t *types.ShapeTerm) {
	e := w.e
	e.Uvarint(uint64(t.Kind))
	w.writeShape(&t.Leaf)
	e.Uvarint(uint64(t.Param))
	e.Uvarint(uint64(len(t.Fields)))
	for i := range t.Fields {
		w.writeTerm(&t.Fields[i])
	}
	e.Uvarint(uint64(t.Packed))
	e.Uvarint(uint64(t.Align))
	e.Bool(t.View)
	e.Bool(t.MutView)
	e.Bool(t.PlainView)
	e.Bool(t.Linear)
	e.Uvarint(uint64(t.Marks))
	e.Uvarint(uint64(len(t.Variants)))
	for _, v := range t.Variants {
		e.Uvarint(uint64(len(v)))
		for i := range v {
			w.writeTerm(&v[i])
		}
	}
	e.Uvarint(uint64(len(t.Discrs)))
	for _, d := range t.Discrs {
		e.Uvarint(d)
	}
	w.typ(t.Tag)
	w.constArg(t.Len)
	w.writeTermOpt(t.Elem)
	w.errset(t.Errors)
}

// writeLayout writes a layout: shape, fields (offset, type), has tag, tag
// (offset, size, niche), variants (discr, fields).
func (w *walker) writeLayout(l *types.Layout) {
	e := w.e
	w.writeShape(&l.Shape)
	w.writeFieldLayouts(l.Fields)
	e.Bool(l.HasTag)
	e.Uvarint(l.Tag.Offset)
	e.Uvarint(uint64(l.Tag.Size))
	e.Bool(l.Tag.Niche)
	e.Uvarint(uint64(len(l.Variants)))
	for i := range l.Variants {
		e.Uvarint(l.Variants[i].Discr)
		w.writeFieldLayouts(l.Variants[i].Fields)
	}
}

func (w *walker) writeFieldLayouts(fs []types.FieldLayout) {
	w.e.Uvarint(uint64(len(fs)))
	for _, f := range fs {
		w.e.Uvarint(f.Offset)
		w.typ(f.Type)
	}
}

// Shape is the mk.shape hash of one shape record.
func (h *Hasher) Shape(s types.Shape) source.Hash {
	var e Encoder
	w := walker{e: &e, h: h}
	e.Uvarint(1)
	w.writeShape(&s)
	return e.sum(DomShape)
}

// ShapeTerm is the mk.shape hash of a shape term (term form).
func (h *Hasher) ShapeTerm(t *types.ShapeTerm) source.Hash {
	var e Encoder
	w := walker{e: &e, h: h}
	e.Uvarint(2)
	w.writeTerm(t)
	return e.sum(DomShape)
}

// Layout is the mk.layout hash of a layout.
func (h *Hasher) Layout(l *types.Layout) source.Hash {
	var e Encoder
	w := walker{e: &e, h: h}
	w.writeLayout(l)
	return e.sum(DomLayout)
}

func (r *reader) readShape(d *Decoder) types.Shape {
	s := types.Shape{Size: d.Uvarint(), Align: d.U32()}
	s.ABI.Kind = types.AbiKind(d.U8())
	s.ABI.Count = d.U8()
	s.ABI.Byval = d.Bool()
	for i := range s.ABI.Parts {
		s.ABI.Parts[i] = types.AbiPart{Class: types.RegClass(d.U8()), Bytes: d.U8()}
	}
	s.Kind = types.Kind(d.U8())
	s.Carrier = d.Bool()
	s.View = d.Bool()
	s.MutView = d.Bool()
	s.PlainView = d.Bool()
	s.LoanInterior = d.Bool()
	s.Opaque = d.Bool()
	s.Send = d.Bool()
	s.Share = d.Bool()
	s.Freeze = d.Bool()
	return s
}

// maxTermDepth bounds term nesting on decode, so forged input cannot
// exhaust the Go stack.
const maxTermDepth = 512

func (r *reader) readTerm(d *Decoder, depth int) types.ShapeTerm {
	if depth > maxTermDepth {
		d.Failf("shape term nested too deep")
		return types.ShapeTerm{}
	}
	t := types.ShapeTerm{Kind: types.ShapeTermKind(d.U8()), Leaf: r.readShape(d), Param: d.U32()}
	n := d.Count(20)
	for i := 0; i < n && d.Err == nil; i++ {
		t.Fields = append(t.Fields, r.readTerm(d, depth+1))
	}
	t.Packed = d.U32()
	t.Align = d.U32()
	t.View = d.Bool()
	t.MutView = d.Bool()
	t.PlainView = d.Bool()
	t.Linear = d.Bool()
	t.Marks = types.TermMarks(d.U8())
	n = d.Count(1)
	for i := 0; i < n && d.Err == nil; i++ {
		m := d.Count(20)
		var v []types.ShapeTerm
		for j := 0; j < m && d.Err == nil; j++ {
			v = append(v, r.readTerm(d, depth+1))
		}
		t.Variants = append(t.Variants, v)
	}
	n = d.Count(1)
	for i := 0; i < n; i++ {
		t.Discrs = append(t.Discrs, d.Uvarint())
	}
	t.Tag = r.typ(d, 0)
	t.Len = r.readConstArg(d, 0)
	if d.Bool() && d.Err == nil {
		e := r.readTerm(d, depth+1)
		t.Elem = &e
	}
	t.Errors = r.errset(d, 0)
	return t
}

func (r *reader) readLayout(d *Decoder) types.Layout {
	l := types.Layout{Shape: r.readShape(d), Fields: r.readFieldLayouts(d), HasTag: d.Bool()}
	l.Tag = types.TagLayout{Offset: d.Uvarint(), Size: d.U8(), Niche: d.Bool()}
	n := d.Count(2)
	for i := 0; i < n; i++ {
		l.Variants = append(l.Variants, types.VariantLayout{Discr: d.Uvarint(), Fields: r.readFieldLayouts(d)})
	}
	return l
}

func (r *reader) readFieldLayouts(d *Decoder) []types.FieldLayout {
	n := d.Count(2)
	var out []types.FieldLayout
	for i := 0; i < n; i++ {
		out = append(out, types.FieldLayout{Offset: d.Uvarint(), Type: r.typ(d, 0)})
	}
	return out
}
