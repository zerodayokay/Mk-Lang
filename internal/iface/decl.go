// Why: a module's declarations are the main part of its cached interface,
// and a reader's cached check is valid only while the facts it read stay
// the same, so each kind of fact needs its own stable hash.
//
// What: writeDecl and readDecl (every declaration kind in store form) and the
// API-part hashes Hasher.Decl (mk.decl, or mk.sig for functions),
// Hasher.Fields (mk.fields) and Hasher.Impl (mk.impl).
//
// How: the store form writes every field in declaration order after a tag.
// The hashes split what readers see: mk.decl holds a non-function item's
// "signature" part, mk.fields a struct's visible fields plus a "has
// private fields" bit, so a private field edit leaves foreign readers valid.

package iface

import (
	"mk/internal/source"
	"mk/internal/types"
)

// Decl tags in store form and in mk.decl.
const (
	declNone = iota
	declStruct
	declEnum
	declError
	declInterface
	declImpl
	declAlias
	declConst
	declGlobal
	declFunc
	declTest
	declGen
)

func (w *walker) writeFields(fs []types.FieldDecl) {
	w.e.Uvarint(uint64(len(fs)))
	for i := range fs {
		f := &fs[i]
		w.sym(f.Name)
		w.typ(f.Type)
		w.e.Bool(f.Exported)
		w.span(f.Span)
	}
}

func (w *walker) writeItems(its []types.ItemID) {
	w.e.Uvarint(uint64(len(its)))
	for _, it := range its {
		w.item(it)
	}
}

// writeTermOpt writes a present bit, then the term.
func (w *walker) writeTermOpt(t *types.ShapeTerm) {
	w.e.Bool(t != nil)
	if t != nil {
		w.writeTerm(t)
	}
}

// writeDecl writes a declaration in store form: its tag, then every field
// in declaration order.
func (w *walker) writeDecl(dcl types.Decl) {
	e := w.e
	switch d := dcl.(type) {
	case nil:
		e.Uvarint(declNone)
	case *types.StructDecl:
		e.Uvarint(declStruct)
		w.writeGenerics(d.Generics)
		w.writeFields(d.Fields)
		e.Bool(d.View)
		e.Bool(d.MutView)
		e.Bool(d.Linear)
		e.Uvarint(uint64(d.Packed))
		e.Uvarint(uint64(d.Align))
		w.writeItems(d.Invariants)
		w.writeTermOpt(d.Shape)
	case *types.EnumDecl:
		e.Uvarint(declEnum)
		w.writeGenerics(d.Generics)
		e.Uvarint(uint64(len(d.Variants)))
		for i := range d.Variants {
			v := &d.Variants[i]
			w.sym(v.Name)
			w.writeFields(v.Fields)
			e.Uvarint(v.Discr)
			w.span(v.Span)
		}
		w.typ(d.Tag)
		w.writeTermOpt(d.Shape)
		e.Bool(d.Open)
	case *types.ErrorDecl:
		e.Uvarint(declError)
		w.writeFields(d.Fields)
		e.Uvarint(uint64(d.Code))
	case *types.InterfaceDecl:
		e.Uvarint(declInterface)
		w.writeGenerics(d.Generics)
		e.Uvarint(uint64(len(d.Methods)))
		for i := range d.Methods {
			w.writeSig(&d.Methods[i], false)
		}
	case *types.ImplDecl:
		e.Uvarint(declImpl)
		w.writeGenerics(d.Generics)
		w.writeBound(&d.Bound)
		w.typ(d.For)
		w.writeItems(d.Methods)
		e.Bool(d.Unsafe)
	case *types.AliasDecl:
		e.Uvarint(declAlias)
		w.writeGenerics(d.Generics)
		w.typ(d.Type)
		e.Bool(d.Open)
	case *types.ConstDecl:
		e.Uvarint(declConst)
		w.typ(d.Type)
		e.Uvarint(d.Fuel)
		w.cnst(d.Value)
		e.Bool(d.HasValue)
		e.String(d.Section)
	case *types.GlobalDecl:
		e.Uvarint(declGlobal)
		w.typ(d.Type)
		w.cnst(d.Value)
		e.Bool(d.HasValue)
		e.Bool(d.Extern)
		e.String(d.Section)
	case *types.FuncDecl:
		e.Uvarint(declFunc)
		w.writeSig(&d.Sig, false)
		e.Bool(d.Bodiless)
		e.Bool(d.Intrinsic)
		e.Bool(d.Naked)
		e.String(d.Section)
	case *types.TestDecl:
		e.Uvarint(declTest)
		e.String(d.Name)
		e.Bool(d.TakesSystem)
	case *types.GenDecl:
		e.Uvarint(declGen)
		w.item(d.Generator)
		e.Uvarint(uint64(len(d.Args)))
		for _, a := range d.Args {
			e.Uvarint(uint64(a.Kind))
			w.typ(a.Type)
			w.item(a.Item)
			w.cnst(a.Value)
		}
		e.Uvarint(d.Fuel)
	default:
		source.ICEf(source.Span{}, "iface: decl %T", dcl)
	}
}

func (r *reader) readFields(d *Decoder) []types.FieldDecl {
	n := d.Count(4)
	if n == 0 {
		return nil
	}
	out := make([]types.FieldDecl, n)
	for i := range out {
		out[i] = types.FieldDecl{Name: r.sym(d), Type: r.typ(d, 0), Exported: d.Bool(), Span: r.span(d)}
	}
	return out
}

func (r *reader) readItems(d *Decoder) []types.ItemID {
	n := d.Count(1)
	if n == 0 {
		return nil
	}
	out := make([]types.ItemID, n)
	for i := range out {
		out[i] = r.item(d)
	}
	return out
}

func (r *reader) readTermOpt(d *Decoder) *types.ShapeTerm {
	if !d.Bool() {
		return nil
	}
	t := r.readTerm(d, 0)
	return &t
}

// readDecl reads what writeDecl wrote.
func (r *reader) readDecl(d *Decoder) types.Decl {
	switch tag := d.Uvarint(); tag {
	case declNone:
		return nil
	case declStruct:
		s := &types.StructDecl{Generics: r.readGenerics(d), Fields: r.readFields(d)}
		s.View = d.Bool()
		s.MutView = d.Bool()
		s.Linear = d.Bool()
		s.Packed = d.U32()
		s.Align = d.U32()
		s.Invariants = r.readItems(d)
		s.Shape = r.readTermOpt(d)
		return s
	case declEnum:
		en := &types.EnumDecl{Generics: r.readGenerics(d)}
		n := d.Count(4)
		for i := 0; i < n; i++ {
			en.Variants = append(en.Variants, types.VariantDecl{Name: r.sym(d), Fields: r.readFields(d), Discr: d.Uvarint(), Span: r.span(d)})
		}
		en.Tag = r.typ(d, 0)
		en.Shape = r.readTermOpt(d)
		en.Open = d.Bool()
		return en
	case declError:
		return &types.ErrorDecl{Fields: r.readFields(d), Code: d.U32()}
	case declInterface:
		in := &types.InterfaceDecl{Generics: r.readGenerics(d)}
		n := d.Count(8)
		for i := 0; i < n; i++ {
			in.Methods = append(in.Methods, r.readSig(d))
		}
		return in
	case declImpl:
		im := &types.ImplDecl{Generics: r.readGenerics(d), Bound: r.readBound(d), For: r.typ(d, 0)}
		im.Methods = r.readItems(d)
		im.Unsafe = d.Bool()
		return im
	case declAlias:
		al := &types.AliasDecl{Generics: r.readGenerics(d), Type: r.typ(d, 0)}
		al.Open = d.Bool()
		return al
	case declConst:
		return &types.ConstDecl{Type: r.typ(d, 0), Fuel: d.Uvarint(), Value: r.cnst(d, 0), HasValue: d.Bool(), Section: d.String()}
	case declGlobal:
		return &types.GlobalDecl{Type: r.typ(d, 0), Value: r.cnst(d, 0), HasValue: d.Bool(), Extern: d.Bool(), Section: d.String()}
	case declFunc:
		return &types.FuncDecl{Sig: r.readSig(d), Bodiless: d.Bool(), Intrinsic: d.Bool(), Naked: d.Bool(), Section: d.String()}
	case declTest:
		return &types.TestDecl{Name: d.String(), TakesSystem: d.Bool()}
	case declGen:
		g := &types.GenDecl{Generator: r.item(d)}
		n := d.Count(4)
		for i := 0; i < n; i++ {
			g.Args = append(g.Args, types.GenArg{Kind: types.GenArgKind(d.U8()), Type: r.typ(d, 0), Item: r.item(d), Value: r.cnst(d, 0)})
		}
		g.Fuel = d.Uvarint()
		return g
	default:
		d.Failf("decl tag %d", tag)
		return nil
	}
}

// Decl is the mk.decl hash of a non-function item's "signature" part, or
// for a function (and fn literal) its mk.sig hash (mixed with its naked
// flag and section when it has either, funcDeclExtra). It includes the item's
// kind and Exported flag, so a private item becoming exported changes it
// (an E0204 reader rechecks). A struct's fields are not in it (they are
// mk.fields); an enum's variants are, with their fields. An item with no
// Decl yet is an ICE.
func (h *Hasher) Decl(it *types.Item) source.Hash {
	if fd, ok := it.Decl.(*types.FuncDecl); ok {
		return h.funcDeclExtra(fd)
	}
	var e Encoder
	w := walker{e: &e, h: h}
	e.Uvarint(uint64(it.Kind))
	e.String(it.Key.String())
	e.Bool(it.Exported)
	switch d := it.Decl.(type) {
	case *types.StructDecl:
		e.Uvarint(declStruct)
		w.writeGenerics(d.Generics)
		e.Bool(d.View)
		e.Bool(d.MutView)
		e.Bool(d.Linear)
		e.Uvarint(uint64(d.Packed))
		e.Uvarint(uint64(d.Align)) // align(n) is a public layout fact
		// The invariants decide zero-validity (a struct with one is not
		// zero-valid), which a foreign judgement reads through this part
		// (check's zeroValid records ReadSignature).
		w.writeItems(d.Invariants)
	case *types.EnumDecl:
		e.Uvarint(declEnum)
		w.writeGenerics(d.Generics)
		w.typ(d.Tag)
		e.Uvarint(uint64(len(d.Variants)))
		for i := range d.Variants {
			v := &d.Variants[i]
			w.sym(v.Name)
			e.Uvarint(v.Discr)
			w.writeFields(v.Fields)
		}
		e.Bool(d.Open) // open is a public fact that matches read
	case *types.ErrorDecl:
		e.Uvarint(declError)
		w.writeFields(d.Fields)
	case *types.InterfaceDecl:
		e.Uvarint(declInterface)
		w.writeGenerics(d.Generics)
		e.Uvarint(uint64(len(d.Methods)))
		for i := range d.Methods {
			e.Hash(h.Signature(&d.Methods[i]))
		}
	case *types.ImplDecl:
		e.Uvarint(declImpl)
		w.writeGenerics(d.Generics)
		w.writeBound(&d.Bound)
		w.typ(d.For)
		w.writeItems(d.Methods)
		e.Bool(d.Unsafe)
	case *types.AliasDecl:
		e.Uvarint(declAlias)
		w.writeGenerics(d.Generics)
		w.typ(d.Type)
		e.Bool(d.Open)
	case *types.ConstDecl:
		e.Uvarint(declConst)
		w.typ(d.Type)
		e.String(d.Section)
	case *types.GlobalDecl:
		e.Uvarint(declGlobal)
		w.typ(d.Type)
		e.Bool(d.Extern)
		e.String(d.Section)
	case *types.TestDecl:
		e.Uvarint(declTest)
		e.String(d.Name)
		e.Bool(d.TakesSystem)
	case *types.GenDecl:
		e.Uvarint(declGen)
		w.item(d.Generator)
		e.Uvarint(uint64(len(d.Args)))
		for _, a := range d.Args {
			e.Uvarint(uint64(a.Kind))
			w.typ(a.Type)
			w.item(a.Item)
			w.cnst(a.Value)
		}
		e.Uvarint(d.Fuel)
	default:
		source.ICEf(it.Span, "iface: Decl hash of %s with decl %T", it.Key, it.Decl)
	}
	return e.sum(DomDecl)
}

// Fields is the mk.fields hash of a struct's or error's visible fields in
// declaration order, (name, type, exported) each, then the "has private
// fields" bit. A foreign reader (all false) sees the exported fields only
// plus that bit, because construction names every field and a
// private field cannot be named from outside; all is the same module's view.
// ok is false for an item with no fields part.
func (h *Hasher) Fields(it *types.Item, all bool) (source.Hash, bool) {
	var fs []types.FieldDecl
	switch d := it.Decl.(type) {
	case *types.StructDecl:
		fs = d.Fields
	case *types.ErrorDecl:
		fs = d.Fields
	case *types.EnumDecl:
		// An enum's variants and their payloads are all public: its
		// "fields" (a borrow walk through it) are its declaration.
		var e Encoder
		e.Hash(h.Decl(it))
		return e.sum(DomFields), true
	default:
		return source.Hash{}, false
	}
	var e Encoder
	w := walker{e: &e, h: h}
	private := false
	var vis []types.FieldDecl
	for _, f := range fs {
		if !f.Exported {
			private = true
		}
		if all || f.Exported {
			vis = append(vis, f)
		}
	}
	w.writeFields(vis)
	e.Bool(private)
	return e.sum(DomFields), true
}

// Impl is the mk.impl hash of an impl item: its mk.decl hash, then
// each method's mk.sig hash in order.
func (h *Hasher) Impl(it *types.Item) source.Hash {
	d, ok := it.Decl.(*types.ImplDecl)
	if !ok {
		source.ICEf(it.Span, "iface: Impl hash of %s, decl %T", it.Key, it.Decl)
	}
	var e Encoder
	e.Hash(h.Decl(it))
	e.Uvarint(uint64(len(d.Methods)))
	for _, m := range d.Methods {
		mi := h.items.Get(m)
		fd, ok := mi.Decl.(*types.FuncDecl)
		if !ok {
			source.ICEf(mi.Span, "iface: impl method %s has no signature", mi.Key)
		}
		e.Hash(h.Signature(&fd.Sig))
	}
	return e.sum(DomImpl)
}
