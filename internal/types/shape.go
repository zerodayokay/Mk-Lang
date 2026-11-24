// Why: the loan pass, codegen, abi and importers all need a type's
// ownership kind, its memory facts and its layout, and an importer must get
// them without the type's private fields.
//
// What: Kind, Shape (the public record), Facts, the layouts (Layout,
// FieldLayout, TagLayout, VariantLayout), AbiClass, ShapeTerm with
// TermMarks, NicheOptional and EnumTagBytes.
//
// How: the shape pass computes one Shape and Facts per concrete type at
// stage 5. A generic type's ShapeTerm is a closed tree over its parameters,
// so an importer computes an instance's shape from one record, and equal
// terms make a private-field edit invisible to importers.

package types

import "strconv"

// Kind is a type's ownership kind, ordered Plain < Unique <
// Linear so a struct's kind is the maximum over its fields.
type Kind uint8

const (
	Plain Kind = iota
	Unique
	Linear
)

// String is plain, unique or linear.
func (k Kind) String() string {
	switch k {
	case Plain:
		return "plain"
	case Unique:
		return "unique"
	case Linear:
		return "linear"
	}
	return "kind(" + strconv.Itoa(int(k)) + ")"
}

// RegClass is the register class of one part of a value under the C ABI.
type RegClass uint8

const (
	RegInt RegClass = iota
	RegFloat
	RegVector
)

// AbiPart is one register-sized part of a directly passed value.
type AbiPart struct {
	Class RegClass
	Bytes uint8
}

// AbiKind tags an AbiClass.
type AbiKind uint8

const (
	// AbiIgnore: zero-sized, passed as nothing.
	AbiIgnore AbiKind = iota
	// AbiDirect: in registers, as Parts[:Count] (coerced to those LLVM types).
	AbiDirect
	// AbiIndirect: by pointer to a copy; for a result, through sret. Byval
	// says whether the copy is made by the callee's frame (byval) or the caller.
	AbiIndirect
)

// AbiClass is how a value travels under the target's C ABI (every Mk
// function uses it). Produced by abi, read by codegen. Comparable.
type AbiClass struct {
	Kind  AbiKind
	Count uint8
	Byval bool
	Parts [4]AbiPart
}

// Shape is the public record of one concrete type for one target, produced by
// the shape pass at stage 5 and stored in the interface. Two types with equal
// shapes are interchangeable to every importer that reads only the shape.
// Comparable.
type Shape struct {
	Size  uint64
	Align uint32
	ABI   AbiClass
	Kind  Kind
	// Carrier: a reference type, a type with a carrier field, a view type, or
	// a type parameter.
	Carrier bool
	View    bool
	// MutView is true on a `mut view struct` itself (not on wrappers or
	// arrays/optionals of it). MutView implies View and LoanInterior.
	MutView bool
	// PlainView is true on a `plain view struct` itself (not on wrappers or
	// arrays/optionals of it). PlainView implies View, Kind == Plain and Freeze.
	PlainView bool
	// LoanInterior is true when a shared reference `&T` can store a loan into
	// interior storage: a `mut view struct`, `Arena`, `Scope`, a `Cell[U]` /
	// `Mutex[U]` / `Chan[U]` whose payload `U` is a carrier, or a composite
	// holding one inline (not behind a reference).
	LoanInterior bool
	// Opaque marks a C layout Mk cannot express (C import; always false in
	// the one-shot).
	Opaque bool
	Send   bool
	Share  bool
	// Freeze: no byte can change through a shared reference. False when the
	// type holds, without passing through a reference, a Cell, Mutex, atomic,
	// Chan, raw pointer, or a view built on raw pointers.
	// Never visible to Mk code.
	Freeze bool
}

// Facts is what the loan pass and codegen read about a type, so neither touches
// a Layout. One per concrete TypeID, written only by the shape pass.
type Facts struct {
	Kind    Kind
	Carrier bool
	View    bool
	// MutView is true on a `mut view struct` itself; cleared by wrappers like
	// `[N]T`, `?T` and `E!T`.
	MutView bool
	// PlainView is true on a `plain view struct` itself; cleared by wrappers
	// like `[N]T`, `?T` and `E!T`.
	PlainView bool
	// Allocator is true for Arena, Alloc and Heap.
	Allocator bool
	// Interior is true for a type that is, or holds inline (not behind a
	// reference), a Cell, Mutex, Chan, Arena, raw.Atomic, Scope or `mut view
	// struct`. Codegen reads this (with Freeze) for LLVM immutability
	// attributes; loan writability uses LoanInterior.
	Interior bool
	// LoanInterior is true for a type that can accept a loan store through a
	// shared reference: a `mut view struct`, Arena, Scope, a Cell[T] /
	// Mutex[T] / Chan[T] whose payload T is a carrier, or a composite holding
	// one inline. Non-carrier Cell/Mutex/Chan and raw.Atomic have
	// LoanInterior == false while keeping Interior == true and Freeze == false.
	LoanInterior bool
	// SliceImpl is true for a type with an impl Slice[T].
	SliceImpl bool
	Send      bool
	Share     bool
	Freeze    bool
	IsFn      bool
}

// FieldLayout is one field's offset and type.
type FieldLayout struct {
	Offset uint64
	Type   TypeID
}

// TagLayout is where a tagged type keeps its tag. A niche tag is the pointer
// word of ?&T, ?mut T or ?fn, where none is zero; every other ?T, ?str and ?[]T
// included, is {u8 some, T value}. NicheOptional is the one predicate for it.
type TagLayout struct {
	Offset uint64
	Size   uint8
	Niche  bool
}

// NicheOptional reports whether ?T with an element of kind elem is one pointer
// word whose zero is none: exactly &T, mut T and fn. Raw pointers may be null,
// and str and slices are two words, so their optionals carry a some byte.
// check, eval, codegen and abi all read this.
func NicheOptional(elem TypeKind) bool { return elem == KRef || elem == KMut || elem == KFn }

// EnumTagBytes is the tag width of an enum with no declared tag type and
// these logical discriminants: the smallest of 1, 2 and 4 bytes that holds
// every one; 0 when one exceeds u32 (the checker reports it). An enum with
// no variants has a 1-byte tag.
func EnumTagBytes(discrs []uint64) uint8 {
	var hi uint64
	for _, d := range discrs {
		hi = max(hi, d)
	}
	switch {
	case hi <= 0xff:
		return 1
	case hi <= 0xffff:
		return 2
	case hi <= 0xffffffff:
		return 4
	}
	return 0
}

// VariantLayout is one variant of an enum, optional or error union: its
// logical discriminant and its fields. For ?T: none 0, some 1. For E!T: ok 0,
// then each error by its ErrorCode.
type VariantLayout struct {
	Discr  uint64
	Fields []FieldLayout
}

// Layout is the full layout codegen and the evaluator read, so const data and
// native code agree byte for byte. It is never written to an interface.
type Layout struct {
	Shape    Shape
	Fields   []FieldLayout
	HasTag   bool
	Tag      TagLayout
	Variants []VariantLayout
}

// ShapeTermKind tags a ShapeTerm.
type ShapeTermKind uint8

const (
	// TermLeaf is a concrete shape.
	TermLeaf ShapeTermKind = iota
	// TermParam is generic parameter Param, filled by the importer.
	TermParam
	// TermStruct is a struct of Fields with Packed, View and Linear.
	TermStruct
	// TermRecord is a record of Fields in canonical order.
	TermRecord
	// TermEnum is an enum of Variants, their logical discriminants Discrs
	// (one per variant, explicit = n or implicit), and an optional declared
	// Tag type.
	TermEnum
	// TermArray is [Len]Elem.
	TermArray
	// TermOptional is ?Elem.
	TermOptional
	// TermErrorUnion is Errors!Elem.
	TermErrorUnion
)

// TermMarks are the facts a struct term carries that its fields do not
// show. The producer inlines a compiler-known generic item such as
// Cell[T] or Mutex[T] into a TermStruct of its fields, which would lose what
// makes it special; the producer sets the item's marks on that node, and an
// instance's Freeze, Share, Send and Interior are the fields' values with
// every mark on the path applied.
type TermMarks uint8

const (
	// MarkInterior: a Cell. Interior is true; Freeze and Share are false.
	MarkInterior TermMarks = 1 << iota
	// MarkNoFreeze: a Mutex, an atomic, a Chan, or a view over raw pointers:
	// bytes change through a shared reference.
	MarkNoFreeze
	// MarkNoShare: not Share whatever the fields are.
	MarkNoShare
	// MarkNoSend: not Send whatever the fields are.
	MarkNoSend
	// MarkInteriorOnly: Interior is true and nothing else changes; set with
	// MarkNoFreeze on an inlined Mutex or raw.Atomic, which stay Share
	// (Facts.Interior).
	MarkInteriorOnly
)

// ShapeTerm is a closed tree over TermParam leaves (nested generics are
// inlined by the producer), so an importer computes Wrap[Foo]'s shape from
// one record, and equal terms make a private-field edit invisible to
// importers. It holds everything an instance's Layout and Shape need: field
// order and packing, enum discriminants (Discrs, which fix the tag width and
// VariantLayout.Discr), the declared tag, and Marks.
type ShapeTerm struct {
	Kind      ShapeTermKind
	Leaf      Shape
	Param     uint32
	Fields    []ShapeTerm
	Packed    uint32
	View      bool
	MutView   bool
	PlainView bool
	Linear    bool
	Marks     TermMarks
	Variants  [][]ShapeTerm
	Discrs    []uint64
	Tag       TypeID
	Len       ConstArg
	Elem      *ShapeTerm
	Errors    ErrorSetID
	// Align is a TermStruct's align(n), 0 when absent, read with Packed
	// when an instance's layout is computed.
	Align uint32
}
