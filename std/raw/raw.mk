// Why: std is written in Mk, but some of its work (pointers, atomics, bit
// counts, float bits) needs operations only the compiler can provide.
//
// What: the compiler intrinsics std is written with: pointer operations
// (Of, OfMut, Addr, FromAddr, Offset, Read, Copy, Move, Fill, Slice),
// atomics and fences, Clz64, Ctz64, Popcount64, Mul128, the float bit
// casts, SqrtF64, Select (a branch-free choice) and others.
//
// How: every function here is bodiless, so check marks it an intrinsic;
// codegen and eval implement each by its stable key. One not marked safe
// needs an unsafe context (E1101). std.raw is internal to std and holds
// declarations only, so it needs no unsafe_modules entry.

/// Of is r's address as a raw pointer. Safe.
export Of[T](r: &T): *T
/// OfMut is r's address as a raw pointer to write through. Safe.
export OfMut[T](r: mut T): *T
/// Null is the null pointer. Null[void]() is a null *void. Safe.
export Null[T](): *T
/// IsNull reports whether p is null. Safe.
export IsNull[T](p: *T): bool
/// Same reports whether a and b hold the same address. Safe.
export Same[T](a: *T, b: *T): bool
/// Offset is p moved by n elements of T, n times T's size in bytes; n
/// may be negative. Needs unsafe.
export Offset[T](p: *T, n: int): *T
// The natural name is Cast, but Cast is a universe name (E0102), so
// the item is PtrCast, and eval also answers to the key std.raw.Cast.
/// PtrCast is p as a pointer to U, at the same address. T or U may be
/// void: PtrCast[T, void](p) is p as C's untyped `void *`. Needs unsafe.
export PtrCast[T, U](p: *T): *U

/// Read loads a T from p, which must be aligned to AlignOf[T](). Needs
/// unsafe.
export Read[T](p: *T): T
/// ReadUnaligned loads an integer T from p, at any alignment; any other
/// T is E0701. Needs unsafe.
export ReadUnaligned[T: Plain](p: *T): T
/// Write stores x at p, which must be aligned to AlignOf[T](). The value
/// that was there is overwritten, not released. Needs unsafe.
export Write[T](p: *T, x: T)
/// WriteUnaligned stores an integer x at p, at any alignment; any other
/// T is E0701. The value that was there is overwritten. Needs unsafe.
export WriteUnaligned[T: Plain](p: *T, x: T)

/// Copy copies n elements from src to dst; the two ranges must not
/// overlap. O(n). Needs unsafe.
export Copy[T](dst: *T, src: *T, n: int)
/// Move copies n elements from src to dst; the ranges may overlap. O(n).
/// Needs unsafe.
export Move[T](dst: *T, src: *T, n: int)
/// Fill sets n bytes at dst to b. O(n). Needs unsafe.
export Fill(dst: *u8, b: u8, n: int)
/// Compare orders the n bytes at a and b as unsigned bytes: below 0 when
/// a's come first, 0 when they are equal, above 0 otherwise. O(n). Needs
/// unsafe.
export Compare(a: *u8, b: *u8, n: int): int

/// Slice is a slice of the n elements at p. Needs unsafe: the caller
/// answers for the memory staying valid while the slice is in use.
export Slice[T](p: *T, n: int): []T
/// SliceMut is a writable slice of the n elements at p. Needs unsafe.
export SliceMut[T](p: *T, n: int): mut []T
/// Data is the address of s's first element. Safe.
export Data[T](s: []T): *T
/// DataMut is the address of s's first element, to write through. Safe.
export DataMut[T](s: mut []T): *T
/// StrData is the address of s's first byte. Safe.
export StrData(s: str): *u8
/// Str is the n bytes at p as a str. Needs unsafe: the caller answers
/// for the bytes being UTF-8 and staying valid.
export Str(p: *u8, n: int): str

/// Addr is p's address as an integer. Safe.
export Addr[T](p: *T): uint
/// FromAddr is the address a as a pointer to T, which may be void. Needs
/// unsafe.
export FromAddr[T](a: uint): *T

/// Forget ends x, a linear value too, without releasing what it owns.
/// Needs unsafe.
export Forget[T](x: T)
/// IsLinear reports whether T is linear, as a constant for each T. Safe.
export IsLinear[T](): bool
/// Release ends x: it calls T's Free with a (an &Alloc) when T
/// implements Free, and otherwise forgets x. The choice is made at
/// compile time for each T. Needs unsafe.
export Release[T, A](x: T, a: &A)
/// HasFree reports whether T implements Free, as a constant for each T.
/// Needs unsafe.
export HasFree[T](): bool
/// Fail panics with the message msg and the panic code code. Safe.
export Fail(code: u32, msg: str): never

// The compiler-known atomic (KnownItems.Atomic): T is an integer type
// of 1, 2, 4 or 8 bytes or a raw pointer. The operations without
// an order are sequentially consistent; Add needs an integer T.
/// Atomic is a word that tasks on any thread read and write whole. T is
/// an integer type of 1, 2, 4 or 8 bytes or a raw pointer; any other T is
/// E0701. The methods without an order are sequentially consistent. The
/// methods whose names end in Ord take a constant order o, std.atomic's
/// Order value: 0 relaxed, 1 acquire, 2 release, 3 acquire-release, 4
/// sequentially consistent, 5 unordered (loads and stores only). An order
/// a method does not allow is E0706 at the call. Every method is safe.
export struct Atomic[T: Plain] {
    v: T
}

/// New is an Atomic that holds x.
export Atomic[T].New(x: T): Atomic[T]
/// Load reads the word.
export Atomic[T].Load(&self): T
/// Store writes x.
export Atomic[T].Store(&self, x: T)
/// Add adds x, wrapping, and gives the old value. T must be an integer
/// (E0701).
export Atomic[T].Add(&self, x: T): T
/// Swap writes x and gives the old value.
export Atomic[T].Swap(&self, x: T): T
/// Cas writes new when the word holds old, and reports whether it did.
export Atomic[T].Cas(&self, old: T, new: T): bool
/// Fence is a sequentially consistent fence. Safe.
export Fence()

// The ordered forms (behind std.atomic's Atomic[T] and Fence). o is
// std.atomic's Order value: 0 relaxed, 1 acquire, 2 release, 3 acq_rel,
// 4 seq_cst (C++20 [atomics.order]), 5 unordered (LLVM's, a load or store
// order only, as in the memory model). Each form's requires clause bounds
// its const order, so a bad order is E0706 at the call, never an internal
// error; std.atomic's clauses (CanLoad, CanStore,
// CanFence, CanModify) imply these over its six orders, and std.atomic,
// passing its own order on, states those instead (check's
// stdForwardsRawOrder). AddOrd to XorOrd need an integer T and wrap.
/// LoadOrd reads the word with order o: relaxed, acquire, sequentially
/// consistent or unordered.
export Atomic[T].LoadOrd(&self, const o: u8): T
    requires o == 0 || o == 1 || o == 4 || o == 5
/// StoreOrd writes x with order o: relaxed, release, sequentially
/// consistent or unordered.
export Atomic[T].StoreOrd(&self, x: T, const o: u8)
    requires o == 0 || o == 2 || o == 4 || o == 5
/// SwapOrd writes x with order o (0 to 4) and gives the old value.
export Atomic[T].SwapOrd(&self, x: T, const o: u8): T
    requires o <= 4
/// AddOrd adds x, wrapping, with order o (0 to 4) and gives the old
/// value. T must be an integer.
export Atomic[T].AddOrd(&self, x: T, const o: u8): T
    requires o <= 4
/// SubOrd subtracts x, wrapping, with order o (0 to 4) and gives the old
/// value. T must be an integer.
export Atomic[T].SubOrd(&self, x: T, const o: u8): T
    requires o <= 4
/// AndOrd ands x into the word with order o (0 to 4) and gives the old
/// value. T must be an integer.
export Atomic[T].AndOrd(&self, x: T, const o: u8): T
    requires o <= 4
/// OrOrd ors x into the word with order o (0 to 4) and gives the old
/// value. T must be an integer.
export Atomic[T].OrOrd(&self, x: T, const o: u8): T
    requires o <= 4
/// XorOrd xors x into the word with order o (0 to 4) and gives the old
/// value. T must be an integer.
export Atomic[T].XorOrd(&self, x: T, const o: u8): T
    requires o <= 4
/// CasOrd writes new when the word holds old, with order s (0 to 4) on
/// success and f (relaxed, acquire or sequentially consistent) on
/// failure. ok reports whether it wrote, and value is what the word held.
/// A weak CasOrd may fail even when the word holds old, so call it in a
/// loop.
export Atomic[T].CasOrd(&self, old: T, new: T, const s: u8, const f: u8, const weak: bool): (ok: bool, value: T)
    requires s <= 4
    requires f == 0 || f == 1 || f == 4
/// FenceOrd is a fence with order o: acquire, release, acquire-release or
/// sequentially consistent. Safe.
export FenceOrd(const o: u8)
    requires o >= 1 && o <= 4

/// Ctz64 counts x's trailing zero bits; 64 for 0. Safe.
export Ctz64(x: u64): int
/// Clz64 counts x's leading zero bits; 64 for 0. Safe.
export Clz64(x: u64): int
/// Popcount64 counts x's one bits. Safe.
export Popcount64(x: u64): int
/// RotL64 rotates x left by n bits, n taken modulo 64. Safe.
export RotL64(x: u64, n: int): u64
/// Mul128 is the full 128-bit product of a and b: hi holds its high 64
/// bits and lo its low 64. Safe.
export Mul128(a: u64, b: u64): (hi: u64, lo: u64)
/// F64Bits is x's IEEE 754 encoding, NaN payloads included. Safe.
export F64Bits(x: f64): u64
/// F64From is the f64 whose IEEE 754 encoding is x. Safe.
export F64From(x: u64): f64
/// F32Bits is x's IEEE 754 encoding, NaN payloads included. Safe.
export F32Bits(x: f32): u32
/// F32From is the f32 whose IEEE 754 encoding is x. Safe.
export F32From(x: u32): f32
/// SqrtF64 is the correctly rounded square root of x. Safe.
export SqrtF64(x: f64): f64
/// Select is a when c holds, else b. The compiler is told c is
/// unpredictable, so for an integer, bool, pointer or record T the choice
/// has no branch on c (a cmov on x86_64, a csel on aarch64), even in a
/// loop whose next load depends on it, a binary search's, where the
/// backend would otherwise branch; a float T may still branch on x86_64.
/// Both a and b are evaluated. Safe.
export Select[T: Plain](c: bool, a: T, b: T): T
