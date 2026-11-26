// Why: layout-aware code needs a field's byte offset, hot loops want to
// prefetch memory they will touch soon, and a plain record such as a C
// struct of many fields is filled from all-zero bytes the way C's `= {0}`
// does; none of these can be written in safe Mk.
//
// What: OffsetOf, a field's offset as a compile-time value, Prefetch, a
// read or write prefetch hint, Zeroed, a value of a zero-valid type
// whose bytes are all zero, and AsBytes / SliceAsBytes, read-only byte views
// of unpadded plain values and slices.
//
// How: each is bodiless, so check marks it an intrinsic: codegen lowers it
// by its stable key and the evaluator runs it
// (internal/eval/intrinsic_machine.go). Each is safe and allocates nothing;
// Zeroed's T must be zero-valid and not an error union, implicit bounds
// check judges (E0701), so no all-zero value of it can break a rule of the
// language; AsBytes and SliceAsBytes require an unpadded plain T with no
// references, raw pointers or interior mutability (E0701).

/// The byte offset of the field named field in T's layout, as a
/// compile-time value; a field T lacks is E0901. It allocates nothing.
export OffsetOf[T](field: str): int
/// A hint that p's memory will soon be read, or written when write is
/// true; it changes nothing a program can observe. O(1); allocates
/// nothing.
export Prefetch[T](p: &T, write: bool)
/// A T whose bytes are all zero: every integer and float 0, every bool
/// false, every raw pointer null, every optional none. T must be zero-valid
/// and not an error union (E0701 otherwise): a C import's struct of any
/// size, an atomic word or Cell of a zero-valid T, or an array or struct
/// of such parts, so it starts a global table of atomics. O(size of T);
/// allocates nothing.
export Zeroed[T](): T
/// A read-only byte view of p. T must be bool, char, a number, void, an
/// array of such types, or an unpadded plain struct (or record) with no
/// references, raw pointers or interior mutability (E0701 otherwise).
/// O(1); allocates nothing.
export AsBytes[T](p: &T): []u8 from p
/// A read-only byte view of xs's elements in order. T must satisfy the same
/// layout and plainness rules as AsBytes (E0701 otherwise). O(1); allocates
/// nothing.
export SliceAsBytes[T](xs: []T): []u8 from xs
