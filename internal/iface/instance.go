// Why: a generic instance needs two identities: a nominal name for symbols,
// E0708 counts and pack order, which must not move with a callee's body, and
// a content hash for object pack keys, which must move with everything the
// generated code bakes in.
//
// What: InstanceName (mk.instname), LayoutName, InstanceContentHash
// (mk.instance), the pack cut constants and PackCutAfter.
//
// How: the name hashes the generic's key and the canonical hash of its
// arguments; a fn literal argument counts by its content, never its site.
// The content hash covers the substituted body's hash and the sorted
// external facts it uses: callee signatures, type and error layouts, consts.

package iface

import (
	"cmp"
	"encoding/binary"
	"slices"
	"strings"

	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// InstanceName is the nominal identity of an instance (mono's "instance key"
// for symbols and E0708): mk.instname over the generic's key and the canonical
// hash of its generic arguments. Its first 16 hex digits are the symbol
// suffix. A fn literal reaches it through a const argument's RelocFn, whose
// mk.fn hash is the literal's content, never its site, so identical
// comparators at different sites give one name, one symbol and one E0708
// count. It does not depend on any body, so a callee body edit renames
// nothing.
func (h *Hasher) InstanceName(item types.Key, args []types.GenericArg) source.Hash {
	var e Encoder
	e.String(item.String())
	e.Hash(h.GenericArgs(args))
	return e.sum(DomInstName)
}

// LayoutName is the canonical spelling of a type's generic arguments, the
// Name of a ReadLayout read: "" for none, else "[" + the hex mk.args hash
// of the arguments + "]". It is injective over instances, which a printed
// spelling is not: TypeString writes a named type by its bare name (so
// Wrap[geo.Point] and Wrap[other.Point] would spell alike) and a fn literal
// const argument by its positional LiteralKey. mk.args writes named types
// by key, consts by deep hash and fn literals by content. A World
// that serves layouts keys them by (item key, LayoutName of the arguments).
func (h *Hasher) LayoutName(args []types.GenericArg) string {
	if len(args) == 0 {
		return ""
	}
	return "[" + h.GenericArgs(args).String() + "]"
}

// instFact is one external fact an instance's code bakes in.
type instFact struct {
	class uint8 // 1 callee, 2 named type layout, 3 global or const, 4 error layout
	key   string
	a, b  source.Hash
}

// declWorld is optionally implemented by a World whose declarations are frozen
// (such as driver's packWorld during emitPacks) so InstanceContentHash reuses
// memoized Decl hashes across parallel pack workers.
type declWorld interface {
	DeclHash(it *types.Item) source.Hash
}

// InstanceContentHash is what the instance's code depends on:
// mk.instance over the substituted body's mk.body hash, then the sorted
// external facts the code bakes in: per direct callee and function value
// (its mk.fn hash and its mk.sig hash, because its declare line and
// ABI lowering come from the signature); per named type mentioned at any
// depth (its mk.ty hash and its current mk.layout part from w, because
// projections become offsets); per error of an error set mentioned at any
// depth, and per error a SelCase downcast or an AggError names (its mk.decl
// hash and its mk.layout part, ReadLayout with Name ""); per global or
// const referenced (its type hash, and a const's value hash). A fn literal,
// as an argument or as a direct callee or RvFnRef, is its content, never its
// LiteralKey: its fact has no key and its signature in literal form.
// An external fact w cannot answer is an ICE: hash form is total, and the
// driver loads layouts before codegen.
func (h *Hasher) InstanceContentHash(w World, b *mir.Body) source.Hash {
	dw, _ := w.(declWorld)
	declOf := func(it *types.Item) source.Hash {
		if dw != nil {
			return dw.DeclHash(it)
		}
		return h.Decl(it)
	}
	var facts []instFact
	var seenFnBuf [8]source.Hash
	seenFnN := 0
	var seenFn map[source.Hash]bool
	addFn := func(f mir.FnRef) {
		if f.Item == types.NoItem {
			return
		}
		fh := h.FnRef(types.FnRef{Item: f.Item, Args: b.GenericArgs[f.Args.Start:f.Args.End()]})
		if seenFn != nil {
			if seenFn[fh] {
				return
			}
			seenFn[fh] = true
		} else {
			for k := 0; k < seenFnN; k++ {
				if seenFnBuf[k] == fh {
					return
				}
			}
			if seenFnN < len(seenFnBuf) {
				seenFnBuf[seenFnN] = fh
				seenFnN++
			} else {
				seenFn = make(map[source.Hash]bool, len(seenFnBuf)*2)
				for k := 0; k < seenFnN; k++ {
					seenFn[seenFnBuf[k]] = true
				}
				seenFn[fh] = true
			}
		}
		it := h.items.Get(f.Item)
		if it.Kind == types.KindLiteral {
			// A fn literal is its content, never its site. Its
			// LiteralKey counts literals by source order and mk.sig writes
			// it, so the fact has no key and its signature in literal form.
			fd, ok := it.Decl.(*types.FuncDecl)
			if !ok {
				source.ICEf(it.Span, "iface: fn literal %s has no checked signature", it.Key)
			}
			facts = append(facts, instFact{class: 1, a: fh, b: h.sig(&fd.Sig, true)})
			return
		}
		facts = append(facts, instFact{class: 1, key: it.Key.String(), a: fh, b: declOf(it)})
	}
	var seenItem map[types.ItemID]bool
	addRef := func(id types.ItemID) {
		if id == types.NoItem || seenItem[id] {
			return
		}
		if seenItem == nil {
			seenItem = map[types.ItemID]bool{}
		}
		seenItem[id] = true
		it := h.items.Get(id)
		switch d := it.Decl.(type) {
		case *types.ConstDecl:
			if !d.HasValue {
				source.ICEf(it.Span, "iface: instance reads const %s with no value", it.Key)
			}
			facts = append(facts, instFact{class: 3, key: it.Key.String(), a: h.Type(d.Type), b: h.Const(d.Value)})
		case *types.GlobalDecl:
			facts = append(facts, instFact{class: 3, key: it.Key.String(), a: h.Type(d.Type)})
		default:
			source.ICEf(it.Span, "iface: instance references %s, decl %T", it.Key, it.Decl)
		}
	}
	// An error item is never a KNamed type, but E!T's payload is a union sized
	// and aligned to the largest of T and every error of E, and a SelCase
	// downcast's field projections become the error's offsets: per error
	// member, its decl and its layout, read as ReadLayout of the error's key
	// with Name "" (errors are not generic). AggError and SelCase name errors
	// of the set their place or destination has, so the set's members cover
	// them.
	var seenErr map[types.ItemID]bool
	addErr := func(id types.ItemID) {
		if id == types.NoItem || seenErr[id] {
			return
		}
		if seenErr == nil {
			seenErr = map[types.ItemID]bool{}
		}
		seenErr[id] = true
		it := h.items.Get(id)
		if it.Kind != types.KindError {
			source.ICEf(it.Span, "iface: error member %s is a %v", it.Key, it.Kind)
		}
		lay, ok := layoutPart(w, types.NoType, id, func() types.Read { return types.Read{Kind: types.ReadLayout, Key: it.Key} })
		if !ok {
			source.ICEf(it.Span, "iface: no layout of error %s for an instance hash", it.Key)
		}
		facts = append(facts, instFact{class: 4, key: it.Key.String(), a: declOf(it), b: lay})
	}
	addErrs := func(es types.ErrorSetID) {
		for _, m := range h.tab.Members(es) {
			if !m.IsParam {
				addErr(m.Item)
			}
		}
	}
	var seenTyLow uint64
	var seenTy map[types.TypeID]bool
	var addTy func(t types.TypeID)
	addTy = func(t types.TypeID) {
		if t == types.NoType {
			return
		}
		if t < 64 {
			bit := uint64(1) << t
			if seenTyLow&bit != 0 {
				return
			}
			seenTyLow |= bit
		} else {
			if seenTy[t] {
				return
			}
			if seenTy == nil {
				seenTy = map[types.TypeID]bool{}
			}
			seenTy[t] = true
		}
		ty := h.tab.Get(t)
		switch ty.Kind {
		case types.KNamed:
			for _, a := range ty.Args {
				if !a.IsConst {
					addTy(a.Type)
				} else if a.Const.Kind == types.CASizeOf {
					addTy(a.Const.Type)
				}
			}
			it := h.items.Get(ty.Item)
			lay, ok := layoutPart(w, t, types.NoItem, func() types.Read { return types.Read{Kind: types.ReadLayout, Key: it.Key, Name: h.LayoutName(ty.Args)} })
			if !ok {
				source.ICEf(it.Span, "iface: no layout of %s for an instance hash", h.tab.TypeString(t, nil))
			}
			facts = append(facts, instFact{class: 2, key: it.Key.String(), a: h.Type(t), b: lay})
		case types.KRecord:
			for _, f := range ty.Fields {
				addTy(f.Type)
			}
		case types.KFn:
			for _, p := range ty.Fn.Params {
				addTy(p.Type)
			}
			addTy(ty.Fn.Result)
			addErrs(ty.Fn.Errors)
		case types.KArray:
			if ty.Len.Kind == types.CASizeOf {
				addTy(ty.Len.Type)
			}
			addTy(ty.Elem)
		case types.KErrorUnion:
			addErrs(ty.Errors)
			addTy(ty.Elem)
		case types.KErrorSet:
			addErrs(ty.Errors)
		default:
			addTy(ty.Elem)
		}
	}
	for _, l := range b.Locals {
		addTy(l.Type)
	}
	for _, p := range b.Projections {
		addTy(p.Type)
		if p.Kind == mir.ProjDowncast && p.Sel == mir.SelCase {
			addErr(types.ItemID(p.A))
		}
	}
	for _, a := range b.GenericArgs {
		addTy(a.Type)
	}
	op := func(o *mir.Operand) { addTy(o.Type) }
	for i := range b.Operands {
		op(&b.Operands[i])
	}
	for i := range b.Args {
		op(&b.Args[i].Operand)
	}
	for i := range b.Blocks {
		bl := &b.Blocks[i]
		for j := range bl.Stmts {
			rv := &bl.Stmts[j].Rvalue
			addTy(rv.Type)
			op(&rv.A)
			op(&rv.B)
			addFn(rv.Fn)
			if rv.Kind == mir.RvAggregate && rv.Agg == mir.AggError {
				addErr(rv.Item)
			}
			if rv.Kind == mir.RvGlobalRef || rv.Kind == mir.RvConstRef {
				addRef(rv.Item)
			}
		}
		t := &bl.Term
		addTy(t.Type)
		op(&t.On)
		op(&t.Msg)
		op(&t.Callee.Value)
		addTy(t.Callee.SelfType)
		if (t.Kind == mir.TermCall || t.Kind == mir.TermTailCall) && t.Callee.Kind == mir.CallDirect {
			addFn(t.Callee.Fn) // a tail call's callee is a fact as a call's
		}
	}
	slices.SortFunc(facts, func(x, y instFact) int {
		return cmp.Or(cmp.Compare(x.class, y.class), strings.Compare(x.key, y.key), x.a.Compare(y.a))
	})
	var e Encoder
	e.Hash(h.Body(b))
	e.Uvarint(uint64(len(facts)))
	for _, f := range facts {
		e.Uvarint(uint64(f.class))
		e.String(f.key)
		e.Hash(f.a)
		e.Hash(f.b)
	}
	return e.sum(DomInstance)
}

// Pack cut policy: modulus 32. Smaller moduli (16, 8) made clean builds
// slower without speeding a one-function edit.
const (
	PackCutModulus = 32
	PackSoloOps    = 40_000
)

// PackCutAfter is the pack cut predicate: true when the low 64 bits of name
// (its last 8 bytes, big-endian) modulo PackCutModulus are 0. It reads nothing
// but the instance's own name; an instance over PackSoloOps operations is a
// pack of its own, which the partitioner applies from that instance's count.
func PackCutAfter(name source.Hash) bool {
	return binary.BigEndian.Uint64(name[24:])%PackCutModulus == 0
}

// layoutPart is w's layout part of the named type t, or of the error item
// e when t is NoType: by identity when w is a LayoutWorld, else by the read
// r spells.
func layoutPart(w World, t types.TypeID, e types.ItemID, r func() types.Read) (source.Hash, bool) {
	if lw, ok := w.(LayoutWorld); ok {
		return lw.LayoutPart(t, e)
	}
	return w.Part(r())
}
