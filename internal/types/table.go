// Why: types are compared everywhere, so they are hash-consed: equal types
// get one TypeID and type equality is ==. The table is shared by every
// worker, so it must be cheap to read concurrently.
//
// What: Table (NewTable, Intern, Get) and its constructors (IntType, Ref,
// Slice, Optional, Array, Record, Fn, Named, ...), the error-set table
// (ErrorSet, Members, Union, Subset) and the invariant check on interning.
//
// How: a type is encoded to bytes and looked up in one of 64 shards under a
// read lock; a new one is appended under a mutex, and reads of an existing
// ID take no lock. Records are kept in name order and error sets sorted by
// stable key, so equal types intern alike in every process.

package types

import (
	"encoding/binary"
	"slices"
	"strings"
	"sync"

	"mk/internal/source"
)

// Table interns every type and error set of a session. Structurally equal
// types get one TypeID. It is safe for concurrent use: a lookup of an
// existing type takes one shard's read lock (64 shards, as source.Interner),
// a new type that shard's write lock and then the append lock; Get and
// Members take none. It is append-only. The zero Table is not usable; call
// NewTable.
type Table struct {
	in     *source.Interner
	items  *Items
	consts *ConstTable

	typeShards [tableShards]tableShard[TypeID]
	setShards  [tableShards]tableShard[ErrorSetID]
	appendMu   sync.Mutex
	types      source.AppendList[*Type]
	sets       source.AppendList[[]ErrorMember]
}

// tableShards is the number of independently locked lookup shards.
const tableShards = 64

// tableShard is one lock-protected slice of an encoded-key index.
type tableShard[V any] struct {
	mu sync.RWMutex
	m  map[string]V
	_  [40]byte // keeps neighbouring shards' locks off one cache line
}

// shardOf picks a shard by FNV-1a over the encoded key. The choice affects
// only locking, never an ID a caller can observe in output.
func shardOf(b []byte) uint32 {
	h := uint32(2166136261)
	for _, c := range b {
		h ^= uint32(c)
		h *= 16777619
	}
	return h % tableShards
}

// lookupOrAdd returns the value for key in sh, or calls add under the
// shard's write lock (after a re-check) and records its result. add runs
// under appendMu. Lock order: shard, then appendMu.
func lookupOrAdd[V any](sh *tableShard[V], appendMu *sync.Mutex, key []byte, add func() V) V {
	sh.mu.RLock()
	v, ok := sh.m[string(key)]
	sh.mu.RUnlock()
	if ok {
		return v
	}
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if v, ok := sh.m[string(key)]; ok {
		return v
	}
	appendMu.Lock()
	v = add()
	appendMu.Unlock()
	sh.m[string(key)] = v
	return v
}

// NewTable returns a table holding exactly the fixed types (and the empty
// error set). in spells record field names for their canonical order; items
// gives error items their stable keys for error-set order; consts spells
// const arguments (array lengths, const generics) by value in TypeString. A
// nil consts is allowed for a table that never prints a CAValue argument;
// printing one is then an ICE.
func NewTable(in *source.Interner, items *Items, consts *ConstTable) *Table {
	t := &Table{in: in, items: items, consts: consts}
	for i := range t.typeShards {
		t.typeShards[i].m = make(map[string]TypeID)
		t.setShards[i].m = make(map[string]ErrorSetID)
	}
	t.types.Append(&Type{}) // NoType
	t.sets.Append(nil)      // EmptyErrors
	fixed := []Type{
		Void: {Kind: KVoid}, Never: {Kind: KNever}, Bool: {Kind: KBool}, Char: {Kind: KChar},
		I8: {Kind: KInt, Bits: 8, Signed: true}, I16: {Kind: KInt, Bits: 16, Signed: true},
		I32: {Kind: KInt, Bits: 32, Signed: true}, I64: {Kind: KInt, Bits: 64, Signed: true},
		U8: {Kind: KInt, Bits: 8}, U16: {Kind: KInt, Bits: 16}, U32: {Kind: KInt, Bits: 32}, U64: {Kind: KInt, Bits: 64},
		Int: {Kind: KIntPtr, Signed: true}, Uint: {Kind: KIntPtr},
		F32: {Kind: KFloat, Bits: 32}, F64: {Kind: KFloat, Bits: 64}, Str: {Kind: KStr}, Poison: {Kind: KPoison},
	}
	for want := Void; want < numFixed; want++ {
		if got := t.Intern(fixed[want]); got != want {
			source.ICEf(source.Span{}, "fixed type %d interned as %d", want, got)
		}
	}
	return t
}

// Consts is the const table TypeString spells const arguments from (nil when
// the table was made without one).
func (t *Table) Consts() *ConstTable { return t.consts }

// Interner is the table's symbol interner.
func (t *Table) Interner() *source.Interner { return t.in }

// Items is the item table whose keys order error sets.
func (t *Table) Items() *Items { return t.items }

// Get returns the type with the given ID. The result is shared: do not modify
// it. An ID this table never handed out is an ICE.
func (t *Table) Get(id TypeID) *Type {
	ty, ok := t.types.Get(uint32(id))
	if !ok || id == NoType {
		source.ICEf(source.Span{}, "types.Table.Get: unknown type %d", id)
	}
	return ty
}

// Kind is Get(id).Kind.
func (t *Table) Kind(id TypeID) TypeKind { return t.Get(id).Kind }

// Len is the number of table slots in use, slot 0 included.
func (t *Table) Len() int { return int(t.types.Len()) }

// Intern returns the ID of ty, adding it when new. ty must satisfy its kind's
// invariants, else it is an ICE: a child of an element kind is a
// valid ID; an error union's set is not empty; record fields are sorted by
// name string and unique (use Record to sort them); a fn type has a payload.
// The table keeps its own copy of ty's slices.
func (t *Table) Intern(ty Type) TypeID {
	t.check(&ty)
	var buf [128]byte
	key := encodeType(buf[:0], &ty)
	return lookupOrAdd(&t.typeShards[shardOf(key)], &t.appendMu, key, func() TypeID {
		c := ty
		c.Args = slices.Clone(ty.Args)
		c.Fields = slices.Clone(ty.Fields)
		if ty.Fn != nil {
			fn := *ty.Fn
			fn.Params = slices.Clone(fn.Params)
			fn.Sources = cloneSources(fn.Sources)
			fn.Fields = CloneFieldSources(fn.Fields)
			fn.Stores = slices.Clone(fn.Stores)
			fn.Ends = slices.Clone(fn.Ends)
			c.Fn = &fn
		}
		return TypeID(t.types.Append(&c))
	})
}

// CloneFieldSources is a deep copy of a per-field clause.
func CloneFieldSources(fs []FieldSources) []FieldSources {
	if fs == nil {
		return nil
	}
	out := make([]FieldSources, len(fs))
	for i, f := range fs {
		out[i] = FieldSources{Field: f.Field, Sources: cloneSources(f.Sources)}
	}
	return out
}

func cloneSources(s []Source) []Source {
	out := slices.Clone(s)
	for i := range out {
		out[i].Path = slices.Clone(out[i].Path)
	}
	return out
}

func (t *Table) valid(id TypeID) bool {
	return id != NoType && id < TypeID(t.types.Len())
}

// check enforces the per-kind invariants of Intern.
func (t *Table) check(ty *Type) {
	bad := func(format string, args ...any) {
		source.ICEf(source.Span{}, "types.Table.Intern(%v): "+format, append([]any{ty.Kind}, args...)...)
	}
	switch ty.Kind {
	case KInt:
		if ty.Bits != 8 && ty.Bits != 16 && ty.Bits != 32 && ty.Bits != 64 {
			bad("bits %d", ty.Bits)
		}
	case KFloat:
		if ty.Bits != 32 && ty.Bits != 64 {
			bad("bits %d", ty.Bits)
		}
	case KRef, KMut, KSlice, KMutSlice, KRawPtr, KOptional:
		if !t.valid(ty.Elem) {
			bad("element %d", ty.Elem)
		}
	case KArray:
		// A length of kind CANone with a Param is a const generic a call has
		// not inferred yet (check's infer), as a KInfer element is a type one.
		if !t.valid(ty.Elem) || ty.Len.Kind == CANone && ty.Len.Param == 0 {
			bad("element %d, length kind %d", ty.Elem, ty.Len.Kind)
		}
	case KErrorUnion:
		if !t.valid(ty.Elem) || ty.Errors == EmptyErrors {
			bad("ok %d, errors %d", ty.Elem, ty.Errors)
		}
	case KErrorSet:
		if ty.Errors == EmptyErrors {
			bad("empty error set")
		}
	case KRecord:
		for i, f := range ty.Fields {
			if !t.valid(f.Type) {
				bad("field type %d", f.Type)
			}
			if i > 0 && t.in.Str(ty.Fields[i-1].Name) >= t.in.Str(f.Name) {
				bad("fields not sorted and unique at %q", t.in.Str(f.Name))
			}
		}
	case KFn:
		if ty.Fn == nil || !t.valid(ty.Fn.Result) {
			bad("no payload or result")
		}
		for i := 1; i < len(ty.Fn.Fields); i++ {
			if ty.Fn.Fields[i-1].Field >= ty.Fn.Fields[i].Field {
				bad("per-field clause not sorted and unique by field")
			}
		}
	case KNamed:
		if ty.Item == NoItem {
			bad("no item")
		}
	case KVoid, KNever, KBool, KChar, KStr, KIntPtr, KParam, KSelf, KInfer, KPoison:
	default:
		bad("unknown kind")
	}
}

// encodeType is the in-memory interning key: the kind and every field, with
// children as TypeIDs (valid because children are interned first). It is
// never written out or hashed; the stable encoding is iface's.
func encodeType(b []byte, ty *Type) []byte {
	u := func(v uint64) { b = binary.AppendUvarint(b, v) }
	ca := func(c ConstArg) { u(uint64(c.Kind)); u(uint64(c.Param)); u(uint64(c.Value)); u(uint64(c.Type)) }
	bo := func(v bool) {
		if v {
			u(1)
		} else {
			u(0)
		}
	}
	u(uint64(ty.Kind))
	u(uint64(ty.Bits))
	bo(ty.Signed)
	u(uint64(ty.Elem))
	ca(ty.Len)
	u(uint64(ty.Errors))
	u(uint64(ty.Item))
	u(uint64(ty.Index))
	u(uint64(len(ty.Args)))
	for _, a := range ty.Args {
		bo(a.IsConst)
		u(uint64(a.Type))
		ca(a.Const)
	}
	u(uint64(len(ty.Fields)))
	for _, f := range ty.Fields {
		u(uint64(f.Name))
		u(uint64(f.Type))
	}
	if fn := ty.Fn; fn != nil {
		u(1)
		u(uint64(len(fn.Params)))
		for _, p := range fn.Params {
			u(uint64(p.Type))
			u(uint64(p.Into))
		}
		u(uint64(fn.Result))
		u(uint64(fn.Errors))
		srcs := func(ss []Source) {
			u(uint64(len(ss)))
			for _, s := range ss {
				u(uint64(s.Param))
				bo(s.Behind)
				u(uint64(len(s.Path)))
				for _, st := range s.Path {
					u(uint64(st.Field))
					u(uint64(st.Type))
				}
			}
		}
		srcs(fn.Sources)
		// The per-field clause after Sources; none is a zero count.
		u(uint64(len(fn.Fields)))
		for _, f := range fn.Fields {
			u(uint64(f.Field))
			srcs(f.Sources)
		}
		u(uint64(len(fn.Stores)))
		for _, s := range fn.Stores {
			u(uint64(s.From))
			u(uint64(s.Into))
		}
		u(uint64(len(fn.Ends)))
		for _, e := range fn.Ends {
			u(uint64(e))
		}
	} else {
		u(0)
	}
	return b
}

// Constructors for the common kinds.

// IntType is i<bits> or u<bits>.
func (t *Table) IntType(bits uint8, signed bool) TypeID {
	return t.Intern(Type{Kind: KInt, Bits: bits, Signed: signed})
}

// Ref is &elem.
func (t *Table) Ref(elem TypeID) TypeID { return t.Intern(Type{Kind: KRef, Elem: elem}) }

// Mut is mut elem.
func (t *Table) Mut(elem TypeID) TypeID { return t.Intern(Type{Kind: KMut, Elem: elem}) }

// Slice is []elem.
func (t *Table) Slice(elem TypeID) TypeID { return t.Intern(Type{Kind: KSlice, Elem: elem}) }

// MutSlice is mut []elem.
func (t *Table) MutSlice(elem TypeID) TypeID { return t.Intern(Type{Kind: KMutSlice, Elem: elem}) }

// RawPtr is *elem.
func (t *Table) RawPtr(elem TypeID) TypeID { return t.Intern(Type{Kind: KRawPtr, Elem: elem}) }

// Optional is ?elem.
func (t *Table) Optional(elem TypeID) TypeID { return t.Intern(Type{Kind: KOptional, Elem: elem}) }

// Array is [n]elem.
func (t *Table) Array(n ConstArg, elem TypeID) TypeID {
	return t.Intern(Type{Kind: KArray, Len: n, Elem: elem})
}

// ErrorUnion is errs!ok.
func (t *Table) ErrorUnion(errs ErrorSetID, ok TypeID) TypeID {
	return t.Intern(Type{Kind: KErrorUnion, Errors: errs, Elem: ok})
}

// ErrorSetType is the value type of an error of the set.
func (t *Table) ErrorSetType(errs ErrorSetID) TypeID {
	return t.Intern(Type{Kind: KErrorSet, Errors: errs})
}

// Record is the record of fields in any written order: they are sorted by
// name string. Two fields of one name are an ICE (E0309 and the parser
// catch them earlier).
func (t *Table) Record(fields []RecordField) TypeID {
	fs := slices.Clone(fields)
	slices.SortFunc(fs, func(a, b RecordField) int { return strings.Compare(t.in.Str(a.Name), t.in.Str(b.Name)) })
	return t.Intern(Type{Kind: KRecord, Fields: fs})
}

// Fn is the fn type with the payload.
func (t *Table) Fn(fn FnType) TypeID { return t.Intern(Type{Kind: KFn, Fn: &fn}) }

// Named is item[args].
func (t *Table) Named(item ItemID, args []GenericArg) TypeID {
	return t.Intern(Type{Kind: KNamed, Item: item, Args: args})
}

// Param is the index-th generic of the owning item.
func (t *Table) Param(index uint32) TypeID { return t.Intern(Type{Kind: KParam, Index: index}) }

// SelfType is Self inside an interface.
func (t *Table) SelfType() TypeID { return t.Intern(Type{Kind: KSelf}) }

// Infer is inference variable n (checker-internal).
func (t *Table) Infer(n uint32) TypeID { return t.Intern(Type{Kind: KInfer, Index: n}) }

// ReturnType is errs!result when errs is not empty, else result: the type
// of MIR local 0 of a function.
func (t *Table) ReturnType(result TypeID, errs ErrorSetID) TypeID {
	if errs == EmptyErrors {
		return result
	}
	return t.ErrorUnion(errs, result)
}

// ErrorSet interns the set of members in any order, with duplicates: params
// first by index, then errors by stable key string, so A|B and B|A are one ID
// and the order is the same in every process. The order is ABI-visible
// only through error codes, which come from keys (ErrorCode). The empty set
// is EmptyErrors. Every item member must already have a key.
func (t *Table) ErrorSet(members []ErrorMember) ErrorSetID {
	if len(members) == 0 {
		return EmptyErrors
	}
	ms := slices.Clone(members)
	key := func(m ErrorMember) string { return t.items.Get(m.Item).Key.String() }
	slices.SortFunc(ms, func(a, b ErrorMember) int {
		switch {
		case a.IsParam && b.IsParam:
			return int(a.Param) - int(b.Param)
		case a.IsParam:
			return -1
		case b.IsParam:
			return 1
		}
		return strings.Compare(key(a), key(b))
	})
	ms = slices.Compact(ms)
	var buf [64]byte
	b := buf[:0]
	for _, m := range ms {
		if m.IsParam {
			b = binary.AppendUvarint(append(b, 1), uint64(m.Param))
		} else {
			b = binary.AppendUvarint(append(b, 2), uint64(m.Item))
		}
	}
	return lookupOrAdd(&t.setShards[shardOf(b)], &t.appendMu, b, func() ErrorSetID {
		return ErrorSetID(t.sets.Append(ms))
	})
}

// Members returns the members of an error set in canonical order; shared, do
// not modify. EmptyErrors gives nil.
func (t *Table) Members(id ErrorSetID) []ErrorMember {
	ms, ok := t.sets.Get(uint32(id))
	if !ok {
		source.ICEf(source.Span{}, "types.Table.Members: unknown error set %d", id)
	}
	return ms
}

// Union is the set of every member of a and b.
func (t *Table) Union(a, b ErrorSetID) ErrorSetID {
	if a == b || b == EmptyErrors {
		return a
	}
	if a == EmptyErrors {
		return b
	}
	return t.ErrorSet(append(slices.Clone(t.Members(a)), t.Members(b)...))
}

// Subset reports whether every member of a is in b.
func (t *Table) Subset(a, b ErrorSetID) bool {
	bm := t.Members(b)
	for _, m := range t.Members(a) {
		if !slices.Contains(bm, m) {
			return false
		}
	}
	return true
}
