// Why: a function's signature is what callers in other modules compile
// against, so its hash decides when they recheck; it must cover every
// public fact of the signature and nothing positional.
//
// What: writeGenerics, writeSig and their readers (the store form) and
// Hasher.Signature (mk.sig).
//
// How: mk.sig writes the item key, name, receiver, generics with bounds,
// params, result, error set, flows (the automatic result source included),
// contracts by body hash and text, variadic, exported, extern and the cpu
// clause's feature set; never a span. Contract bodies are always hashed:
// a missing one is an ICE, never a key fallback.

package iface

import (
	"mk/internal/source"
	"mk/internal/types"
)

// writeGenerics writes generic parameters: const, name, bounds, type, span.
func (w *walker) writeGenerics(gs []types.GenericParam) {
	w.e.Uvarint(uint64(len(gs)))
	for i := range gs {
		g := &gs[i]
		w.e.Bool(g.Const)
		w.sym(g.Name)
		w.writeBounds(g.Bounds)
		w.typ(g.Type)
		w.span(g.Span)
	}
}

func (w *walker) writeBounds(bs []types.Bound) {
	w.e.Uvarint(uint64(len(bs)))
	for i := range bs {
		w.writeBound(&bs[i])
	}
}

func (w *walker) writeBound(b *types.Bound) {
	w.e.Uvarint(uint64(b.Kind))
	w.item(b.Item)
	w.genericArgs(b.Args)
	w.e.Uvarint(uint64(b.Inner))
}

// writeSig writes a signature. literal omits the item key and name (the
// literal form of mk.fn tag 2, hash form only). In hash form a contract
// is its kind, its body's mk.body hash and its text; in store form, its
// kind, its predicate item, its span and its text.
func (w *walker) writeSig(s *types.Signature, literal bool) {
	e := w.e
	if !literal {
		w.item(s.Item)
		w.sym(s.Name)
	}
	e.Uvarint(uint64(s.Receiver))
	w.writeGenerics(s.Generics)
	e.Uvarint(uint64(s.OwnGenerics))
	e.Uvarint(uint64(len(s.Params)))
	for i := range s.Params {
		p := &s.Params[i]
		w.sym(p.Name)
		w.typ(p.Type)
		e.Bool(p.Const)
		e.Bool(p.Var)
		e.Uvarint(uint64(p.Into))
		w.span(p.Span)
	}
	w.typ(s.Result)
	w.errset(s.Errors)
	w.writeSources(s.Flows.Sources)
	w.writeFieldSources(s.Flows.Fields)
	e.Bool(s.Flows.Declared)
	w.writeStores(s.Flows.Stores)
	w.writeEnds(s.Flows.Ends)
	e.Bool(s.Flows.Ambiguous)
	e.Bool(s.Flows.ClauseError)
	e.Uvarint(uint64(len(s.Contracts)))
	for _, c := range s.Contracts {
		e.Uvarint(uint64(c.Kind))
		if w.store() {
			w.item(c.Body)
			w.span(c.Span)
			e.String(c.Text) // E0706's clause fact on a cached callee
		} else {
			e.Hash(w.h.Body(w.h.needBody(c.Body)))
			// The clause's text too: check folds a same-module clause from its
			// AST and matches a restatement by this text, and two clauses that
			// check tells apart can lower to one body. `requires SizeOf[T]() +
			// SizeOf[u8]() < 4` does not fold and `requires SizeOf[T]() + 1 <
			// 4` does, but both lower to the same body, so without the text a
			// caller would keep the old verdict from a warm cache.
			e.String(c.Text)
		}
	}
	e.Bool(s.Variadic)
	e.Bool(s.Exported)
	e.Bool(s.Extern)
	w.writeStrings(s.CPU) // the cpu clause's closure
	if w.store() {
		// The declaration's spelling of its aliases: display only, so
		// not in the hash, which a spelling-only edit then keeps.
		w.writeStrings(s.Spelled)
	}
	w.span(s.Span)
}

// sig is the mk.sig hash of s, in literal form when literal is set.
func (h *Hasher) sig(s *types.Signature, literal bool) source.Hash {
	var e Encoder
	w := walker{e: &e, h: h}
	w.writeSig(s, literal)
	return e.sum(DomSig)
}

// Signature is the mk.sig hash of s. Every contract body must be
// available through the body accessor: an absent one is an ICE.
func (h *Hasher) Signature(s *types.Signature) source.Hash { return h.sig(s, false) }

// readGenerics reads what writeGenerics wrote.
func (r *reader) readGenerics(d *Decoder) []types.GenericParam {
	n := d.Count(4)
	if n == 0 {
		return nil
	}
	out := make([]types.GenericParam, n)
	for i := range out {
		g := &out[i]
		g.Const = d.Bool()
		g.Name = r.sym(d)
		g.Bounds = r.readBounds(d)
		g.Type = r.typ(d, 0)
		g.Span = r.span(d)
	}
	return out
}

func (r *reader) readBounds(d *Decoder) []types.Bound {
	n := d.Count(3)
	if n == 0 {
		return nil
	}
	out := make([]types.Bound, n)
	for i := range out {
		out[i] = r.readBound(d)
	}
	return out
}

func (r *reader) readBound(d *Decoder) types.Bound {
	b := types.Bound{Kind: types.BoundKind(d.U8()), Item: r.item(d), Args: r.readGenericArgs(d, 0)}
	b.Inner = types.BoundKind(d.U8())
	return b
}

// readSig reads a store-form signature.
func (r *reader) readSig(d *Decoder) types.Signature {
	s := types.Signature{Item: r.item(d), Name: r.sym(d), Receiver: types.Receiver(d.U8())}
	s.Generics = r.readGenerics(d)
	s.OwnGenerics = d.U32()
	n := d.Count(6)
	if n > 0 {
		s.Params = make([]types.Param, n)
	}
	for i := range s.Params {
		p := &s.Params[i]
		p.Name = r.sym(d)
		p.Type = r.typ(d, 0)
		p.Const = d.Bool()
		p.Var = d.Bool()
		p.Into = types.ParamIndex(d.U32())
		p.Span = r.span(d)
	}
	s.Result = r.typ(d, 0)
	s.Errors = r.errset(d, 0)
	s.Flows.Sources = r.readSources(d, 0)
	s.Flows.Fields = r.readFieldSources(d, 0)
	s.Flows.Declared = d.Bool()
	s.Flows.Stores = r.readStores(d)
	s.Flows.Ends = r.readEnds(d)
	s.Flows.Ambiguous = d.Bool()
	s.Flows.ClauseError = d.Bool()
	n = d.Count(3)
	for i := 0; i < n; i++ {
		c := types.Contract{Kind: types.ContractKind(d.U8()), Body: r.item(d), Span: r.span(d)}
		c.Text = d.String()
		s.Contracts = append(s.Contracts, c)
	}
	s.Variadic = d.Bool()
	s.Exported = d.Bool()
	s.Extern = d.Bool()
	s.CPU = readStrings(d)
	s.Spelled = readStrings(d)
	s.Span = r.span(d)
	return s
}
