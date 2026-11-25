// Why: layout-aware code needs a field's byte offset, hot loops want to
// prefetch memory they will touch soon, and a plain record such as a C
// struct of many fields is filled from all-zero bytes the way C's `= {0}`
// does; none of these can be written in safe Mk.
//
// What: OffsetOf, a field's offset as a compile-time value, Prefetch, a
// read or write prefetch hint, and Zeroed, a value of a zero-valid type
// whose bytes are all zero.
//
// How: each is bodiless, so check marks it an intrinsic: codegen lowers it
// by its stable key and the evaluator runs it
// (internal/eval/intrinsic_machine.go). Each is safe and allocates nothing;
// Zeroed's T must be zero-valid and not an error union, implicit bounds
// check judges (E0701), so no all-zero value of it can break a rule of the
// language.

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
/// size, or an array or struct of such parts. O(size of T); allocates
/// nothing.
export Zeroed[T](): T
