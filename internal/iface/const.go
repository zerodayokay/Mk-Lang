// Why: compile-time constant values and function references are part of
// signatures, layouts and bodies, so the cache must store and hash them
// without depending on session IDs.
//
// What: writeConst and readConst (the store form of a const value),
// Hasher.Const (mk.const), Hasher.FnRef and Hasher.LiteralName (mk.fn).
//
// How: mk.const is deep: a relocation contributes its pointee's hash in
// hash form and a pool index in store form. Values must already be canonical
// (Canonicalize keeps them as they are). A nominal function hashes by its
// key, a fn literal by its content, so equal literals hash equal.

package iface

import (
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// writeConst writes a const value: type, align, bytes, reloc count, per reloc
// (offset, kind 1 data or 2 fn, then the pointee const or the fn ref).
func (w *walker) writeConst(v *types.ConstValue) {
	e := w.e
	w.typ(v.Type)
	e.Uvarint(uint64(v.Align))
	e.Bytes(v.Bytes)
	e.Uvarint(uint64(len(v.Relocs)))
	for _, r := range v.Relocs {
		e.Uvarint(uint64(r.Offset))
		switch r.Kind {
		case types.RelocData:
			e.Uvarint(1)
			w.cnst(r.Data)
		case types.RelocFn:
			e.Uvarint(2)
			w.fnref(r.Fn)
		default:
			source.ICEf(source.Span{}, "iface: reloc kind %d", r.Kind)
		}
	}
}

// readConst reads a const value entry, checking what Canonicalize and
// ConstTable.Intern would reject.
func (r *reader) readConst(d *Decoder, own uint32) types.ConstValue {
	v := types.ConstValue{Type: r.needTyp(d, own), Align: d.U32(), Bytes: d.Bytes()}
	n := d.Count(3)
	for j := 0; j < n; j++ {
		rl := types.Reloc{Offset: d.U32()}
		switch d.Uvarint() {
		case 1:
			rl.Kind = types.RelocData
			i := r.refVal(d, valConst, own, "const")
			if i == 0 {
				d.Failf("data reloc with no pointee")
			}
			rl.Data = r.cIDs[i]
		case 2:
			rl.Kind = types.RelocFn
			i := d.U32()
			if i == 0 || int(i) >= len(r.itemIDs) {
				d.Failf("fn reloc item %d", i)
			} else {
				rl.Fn.Item = r.itemIDs[i]
			}
			rl.Fn.Args = r.readGenericArgs(d, own)
		default:
			d.Failf("reloc kind")
		}
		v.Relocs = append(v.Relocs, rl)
	}
	if d.Err != nil {
		return v
	}
	// Stored values are canonical: Canonicalize must keep them as they are.
	pb := r.env.PointerBytes
	if pb == 0 {
		pb = 8
	}
	for j := 1; j < len(v.Relocs); j++ {
		if v.Relocs[j-1].Offset >= v.Relocs[j].Offset {
			d.Failf("relocs not sorted")
			return v
		}
	}
	c := types.ConstValue{Bytes: append([]byte(nil), v.Bytes...), Relocs: append([]types.Reloc(nil), v.Relocs...)}
	if err := c.Canonicalize(pb); err != nil {
		d.Failf("%v", err)
		return v
	}
	for j := range c.Bytes {
		if c.Bytes[j] != v.Bytes[j] {
			d.Failf("pointer word not zero under a reloc")
			return v
		}
	}
	return v
}

// Const is the deep mk.const hash of an interned value: a
// RelocData contributes its pointee's Const hash, a RelocFn its FnRef hash.
func (h *Hasher) Const(c types.ConstID) source.Hash {
	if v, ok := h.cMemo.get(uint32(c)); ok {
		return v
	}
	var e Encoder
	w := walker{e: &e, h: h}
	w.writeConst(h.consts.Get(c))
	v := e.sum(DomConst)
	h.cMemo.set(uint32(c), v)
	return v
}

// FnRef is the mk.fn hash of a function reference. A nominal function is
// tag 1, its item key and its generic arguments' hash. A fn literal (an item
// of kind KindLiteral, whose LiteralKey counts literals by source order) is
// tag 2, its signature in literal form (item key and name omitted), its
// body's mk.body hash and its generic arguments' hash, and nothing
// positional, so two identical literals at different sites hash equal.
// The literal's generic arguments are written because one
// literal instantiated twice is two functions.
func (h *Hasher) FnRef(f types.FnRef) source.Hash {
	if len(f.Args) == 0 {
		if v, ok := h.plainFnMemo.get(uint32(f.Item)); ok {
			return v
		}
	}
	it := h.items.Get(f.Item)
	var e Encoder
	if it.Kind != types.KindLiteral {
		e.Uvarint(1)
		e.String(it.Key.String())
		e.Hash(h.GenericArgs(f.Args))
		v := e.sum(DomFn)
		if len(f.Args) == 0 {
			h.plainFnMemo.set(uint32(f.Item), v)
		}
		return v
	}
	fd, ok := it.Decl.(*types.FuncDecl)
	if !ok {
		source.ICEf(it.Span, "iface: fn literal %s has no checked signature", it.Key)
	}
	e.Uvarint(2)
	e.Hash(h.sig(&fd.Sig, true))
	e.Hash(h.Body(h.needBody(f.Item)))
	e.Hash(h.GenericArgs(f.Args))
	return e.sum(DomFn)
}

// LiteralName is the content name of a fn literal item (its key is a
// types.LiteralKey): mk.fn's literal form with no generic arguments. Its
// first 16 hex digits name the literal's symbol.
func (h *Hasher) LiteralName(lit types.ItemID) source.Hash {
	if it := h.items.Get(lit); it.Kind != types.KindLiteral {
		source.ICEf(it.Span, "iface: LiteralName of %s, a %v", it.Key, it.Kind)
	}
	return h.FnRef(types.FnRef{Item: lit})
}

// needBody is the body a hash requires; its absence is an ICE (hash form is
// total).
func (h *Hasher) needBody(it types.ItemID) *mir.Body {
	var b *mir.Body
	if h.body != nil {
		b = h.body(it)
	}
	if b == nil {
		source.ICEf(h.items.Get(it).Span, "iface: no body for %s, which a hash requires", h.items.Get(it).Key)
	}
	return b
}
