// Why: types appear in every signature, declaration and body the cache
// stores, so their encoding must not depend on session IDs or on the order
// of Go constants.
//
// What: typeTag (the frozen tag table) and tagKind, writeType, writeFnType,
// writeFieldSources, writeErrSet with their readers, and the hashes
// Hasher.Type (mk.ty), ErrorSet and GenericArgs (mk.args).
//
// How: each TypeKind maps to a fixed tag in an explicit table, so reordering
// internal/types' constants changes no encoding. Error sets are written in
// canonical member order. TypeIDs index only the memo, never the output.

package iface

import (
	"mk/internal/source"
	"mk/internal/types"
)

// typeTag is the frozen mk.ty tag of each types.TypeKind.
// It is an explicit table, pinned by TestTypeTagsFrozen, so reordering
// internal/types' constants cannot change an encoding. 0 means "never
// encoded": KNone, and KInfer and KPoison, which never reach a hash or an
// entry.
var typeTag = map[types.TypeKind]uint64{
	types.KInt:        1,
	types.KIntPtr:     2,
	types.KFloat:      3,
	types.KBool:       4,
	types.KChar:       5,
	types.KVoid:       6,
	types.KNever:      7,
	types.KRef:        8,
	types.KMut:        9,
	types.KSlice:      10,
	types.KMutSlice:   11,
	types.KStr:        12,
	types.KArray:      13,
	types.KOptional:   14,
	types.KErrorUnion: 15,
	types.KErrorSet:   16,
	types.KRecord:     17,
	types.KFn:         18,
	types.KRawPtr:     19,
	types.KNamed:      20,
	types.KParam:      21,
	types.KSelf:       22,
}

// tagKind inverts typeTag for decoding.
var tagKind = func() map[uint64]types.TypeKind {
	m := make(map[uint64]types.TypeKind, len(typeTag))
	for k, t := range typeTag {
		m[t] = k
	}
	return m
}()

// writeType writes one type node: its tag, then its fields, children through
// the walker (hashes or pool indices).
func (w *walker) writeType(ty *types.Type) {
	tag, ok := typeTag[ty.Kind]
	if !ok {
		source.ICEf(source.Span{}, "iface: type kind %v reached an encoding", ty.Kind)
	}
	e := w.e
	e.Uvarint(tag)
	switch ty.Kind {
	case types.KInt:
		e.Uvarint(uint64(ty.Bits))
		e.Bool(ty.Signed)
	case types.KIntPtr:
		e.Bool(ty.Signed)
	case types.KFloat:
		e.Uvarint(uint64(ty.Bits))
	case types.KBool, types.KChar, types.KVoid, types.KNever, types.KStr, types.KSelf:
	case types.KRef, types.KMut, types.KSlice, types.KMutSlice, types.KOptional, types.KRawPtr:
		w.typ(ty.Elem)
	case types.KArray:
		w.constArg(ty.Len)
		w.typ(ty.Elem)
	case types.KErrorUnion:
		w.errset(ty.Errors)
		w.typ(ty.Elem)
	case types.KErrorSet:
		w.errset(ty.Errors)
	case types.KRecord:
		e.Uvarint(uint64(len(ty.Fields)))
		for _, f := range ty.Fields {
			w.sym(f.Name)
			w.typ(f.Type)
		}
	case types.KFn:
		w.writeFnType(ty.Fn)
	case types.KNamed:
		w.item(ty.Item)
		w.genericArgs(ty.Args)
	case types.KParam:
		e.Uvarint(uint64(ty.Index))
	}
}

// writeFnType writes a fn payload: params (type, into), result, error set,
// sources, per-field sources, stores, ends (positional flows).
func (w *walker) writeFnType(fn *types.FnType) {
	e := w.e
	e.Uvarint(uint64(len(fn.Params)))
	for _, p := range fn.Params {
		w.typ(p.Type)
		e.Uvarint(uint64(p.Into))
	}
	w.typ(fn.Result)
	w.errset(fn.Errors)
	w.writeSources(fn.Sources)
	w.writeFieldSources(fn.Fields)
	w.writeStores(fn.Stores)
	w.writeEnds(fn.Ends)
}

// writeFieldSources writes a per-field clause after the sources: a
// count, then per entry the field index and its sources; none is a zero
// count.
func (w *walker) writeFieldSources(fs []types.FieldSources) {
	w.e.Uvarint(uint64(len(fs)))
	for _, f := range fs {
		w.e.Uvarint(uint64(f.Field))
		w.writeSources(f.Sources)
	}
}

func (w *walker) writeSources(ss []types.Source) {
	e := w.e
	e.Uvarint(uint64(len(ss)))
	for _, s := range ss {
		e.Uvarint(uint64(s.Param))
		e.Bool(s.Behind)
		e.Uvarint(uint64(len(s.Path)))
		for _, st := range s.Path {
			e.Uvarint(uint64(st.Field))
			w.typ(st.Type)
		}
	}
}

func (w *walker) writeStores(ss []types.Store) {
	w.e.Uvarint(uint64(len(ss)))
	for _, s := range ss {
		w.e.Uvarint(uint64(s.From))
		w.e.Uvarint(uint64(s.Into))
	}
}

func (w *walker) writeEnds(es []types.ParamIndex) {
	w.e.Uvarint(uint64(len(es)))
	for _, p := range es {
		w.e.Uvarint(uint64(p))
	}
}

// writeErrSet writes an error set's members in internal/types' canonical
// order (params by index, then errors by key): count, then 1 + param index or
// 2 + error item.
func (w *walker) writeErrSet(ms []types.ErrorMember) {
	w.e.Uvarint(uint64(len(ms)))
	for _, m := range ms {
		if m.IsParam {
			w.e.Uvarint(1)
			w.e.Uvarint(uint64(m.Param))
		} else {
			w.e.Uvarint(2)
			w.item(m.Item)
		}
	}
}

// emptyArgsHash is the constant mk.args hash of an empty generic argument list.
var emptyArgsHash = source.HashBytes(DomArgs, []byte{0})

// Type is the mk.ty hash of t. Infer and Poison raise an ICE:
// they never reach a hash. NoType is an ICE too; walks write the zero
// hash for an absent type themselves.
func (h *Hasher) Type(t types.TypeID) source.Hash {
	if v, ok := h.tyMemo.get(uint32(t)); ok {
		return v
	}
	if t == types.NoType {
		source.ICEf(source.Span{}, "iface: Hasher.Type(NoType)")
	}
	var e Encoder
	w := walker{e: &e, h: h}
	w.writeType(h.tab.Get(t))
	v := e.sum(DomType)
	h.tyMemo.set(uint32(t), v)
	return v
}

// ErrorSet hashes the members in set order (params by index, then errors by
// key, internal/types' canonical order). The empty set has a hash too.
func (h *Hasher) ErrorSet(es types.ErrorSetID) source.Hash {
	if v, ok := h.esMemo.get(uint32(es)); ok {
		return v
	}
	var e Encoder
	w := walker{e: &e, h: h}
	w.writeErrSet(h.tab.Members(es))
	v := e.sum(DomErrSet)
	h.esMemo.set(uint32(es), v)
	return v
}

// GenericArgs is the mk.args hash of a generic argument list: count, then
// each argument (1 + type hash, or 2 + const arg).
func (h *Hasher) GenericArgs(a []types.GenericArg) source.Hash {
	if len(a) == 0 {
		return emptyArgsHash
	}
	var e Encoder
	w := walker{e: &e, h: h}
	w.genericArgs(a)
	return e.sum(DomArgs)
}
