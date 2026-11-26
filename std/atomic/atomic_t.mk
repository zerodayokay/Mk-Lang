// Why: lock-free code and kernels need atomics of any integer width or a
// raw pointer, with an explicit memory order, at no run-time cost.
//
// What: Atomic[T] with New, Load, Store, Swap, CompareExchange,
// CompareExchangeWeak and FetchAdd to FetchXor, each taking the C++20
// memory orders (atomic.Order) as const parameters.
//
// How: no Io and no allocation, so a kernel can use it, and a `var` global
// may hold one. Unique by identity, Send and Share, never Freeze (the
// raw.Atomic field). Each operation is one LLVM atomic instruction with the
// order written at the call, so the order is part of the instance and costs
// nothing at run time. Which edge an order makes (C++20 [atomics.order]):
// - Relaxed: atomicity and the word's single modification order only; no
//   edge with any other memory.
// - Release on a store or read-modify-write W, Acquire on a load or
//   read-modify-write R that reads W's value (or a later value of W's
//   release sequence): every write before W happens before every read
//   after R. That is the edge publication needs: write the data, then
//   Store(.., Release); Load(Acquire), then read the data.
// - AcqRel on a read-modify-write: both edges at once.
// - SeqCst: Acquire and Release as applicable, and every SeqCst operation
//   of the program falls in one total order consistent with each word's
//   modification order (the store-load edge Dekker's algorithm needs).
// An order an operation cannot have is E0706 at the call: a load cannot
// release, a store cannot acquire, a failed compare-exchange is a load
// (Order.CanLoad, CanStore and CanFence are the clauses; a caller that
// passes its own const order on states the same one). Unordered (order 5)
// is a load or store order only, so every read-modify-write states
// CanModify, both orders of a compare-exchange included.
//
// FetchAdd to FetchXor need an integer T (E0701 at the call for a pointer)
// and wrap, as every hardware read-modify-write does (C++, Rust and Go do
// the same; AtomicInt.Add is the checked one).

import std.raw

/// One atomic word of T, with the memory order of each operation given at
/// the call. T is an integer of 1, 2, 4 or 8 bytes, int, uint or a raw
/// pointer (E0701 otherwise). Each operation is one atomic instruction;
/// none allocates or takes an Io.
///
/// An Atomic is unique, Send and Share whatever its T: reading through a
/// pointer it holds is for unsafe code to justify. A `var` global may hold
/// one, and an array of them starts zeroed with `mem.Zeroed`, as `[e; n]`
/// copies e and an Atomic is not Plain.
export struct Atomic[T: Plain] {
    v: raw.Atomic[T]
}

/// Returns an Atomic holding x. O(1); it allocates nothing, so a `var`
/// global may be initialized with it.
export Atomic[T].New(x: T): Atomic[T] {
    return Atomic(v: raw.Atomic[T].New(x))
}

/// Returns the value of the word. o is Relaxed, Acquire, SeqCst or
/// Unordered (E0706 otherwise). O(1); it allocates nothing.
export Atomic[T].Load(&self, const o: Order): T
    requires o.CanLoad() {
    // Edge: the caller's order o (an Acquire or SeqCst load pairs with a
    // Release or SeqCst write of the same word).
    return self.v.LoadOrd(o.v)
}

/// Sets the word to x. o is Relaxed, Release, SeqCst or Unordered (E0706
/// otherwise). O(1); it allocates nothing.
export Atomic[T].Store(&self, x: T, const o: Order)
    requires o.CanStore() {
    // Edge: the caller's order o (a Release or SeqCst store pairs with an
    // Acquire or SeqCst read of the same word).
    self.v.StoreOrd(x, o.v)
}

/// Sets the word to x and returns what it held, in one read-modify-write.
/// Any order but Unordered. O(1); it allocates nothing.
export Atomic[T].Swap(&self, x: T, const o: Order): T
    requires o.CanModify() {
    // Edge: the caller's order o, on both the read and the write.
    return self.v.SwapOrd(x, o.v)
}

/// Sets the word to new if it holds old. value is what the word held either
/// way, so a retry loop needs no second load. ok is true when the word held
/// old: then the read-modify-write has order success. Otherwise nothing is
/// written, and the read is a load of order failure, which may be stronger
/// than success (as C++20 allows) but cannot release. Strong: ok is false
/// only when value != old. O(1); it allocates nothing.
export Atomic[T].CompareExchange(&self, old: T, new: T, const success: Order, const failure: Order): (ok: bool, value: T)
    requires success.CanModify()
    requires failure.CanLoad() && failure.CanModify() {
    // Edge: the caller's orders, success on the read-modify-write and
    // failure on the read alone.
    return self.v.CasOrd(old, new, success.v, failure.v, false)
}

/// CompareExchange that may fail spuriously, with ok false and value ==
/// old, for a loop that retries anyway. On CPUs whose compare-exchange is
/// a load-linked and store-conditional pair it saves an inner loop; on
/// x86-64 it is the same instruction as CompareExchange. O(1); it
/// allocates nothing.
export Atomic[T].CompareExchangeWeak(&self, old: T, new: T, const success: Order, const failure: Order): (ok: bool, value: T)
    requires success.CanModify()
    requires failure.CanLoad() && failure.CanModify() {
    // Edge: as CompareExchange's.
    return self.v.CasOrd(old, new, success.v, failure.v, true)
}

/// Adds x, wrapping, and returns the old value. T is an integer (E0701 for
/// a pointer). Wait-free. Any order but Unordered. O(1); it allocates
/// nothing.
export Atomic[T].FetchAdd(&self, x: T, const o: Order): T
    requires o.CanModify() {
    // Edge: the caller's order o, on both the read and the write.
    return self.v.AddOrd(x, o.v)
}

/// Subtracts x, wrapping, and returns the old value. T is an integer.
/// Wait-free. Any order but Unordered. O(1); it allocates nothing.
export Atomic[T].FetchSub(&self, x: T, const o: Order): T
    requires o.CanModify() {
    return self.v.SubOrd(x, o.v)
}

/// Sets the word to its bitwise and with x and returns the old value. T is
/// an integer. Any order but Unordered. O(1); it allocates nothing. On
/// x86-64, when the result is used, it is a compare-exchange loop and not
/// wait-free.
export Atomic[T].FetchAnd(&self, x: T, const o: Order): T
    requires o.CanModify() {
    return self.v.AndOrd(x, o.v)
}

/// Sets the word to its bitwise or with x and returns the old value. T is
/// an integer. Any order but Unordered. O(1); it allocates nothing. On
/// x86-64, when the result is used, it is a compare-exchange loop and not
/// wait-free.
export Atomic[T].FetchOr(&self, x: T, const o: Order): T
    requires o.CanModify() {
    return self.v.OrOrd(x, o.v)
}

/// Sets the word to its bitwise xor with x and returns the old value. T is
/// an integer. Any order but Unordered. O(1); it allocates nothing. On
/// x86-64, when the result is used, it is a compare-exchange loop and not
/// wait-free.
export Atomic[T].FetchXor(&self, x: T, const o: Order): T
    requires o.CanModify() {
    return self.v.XorOrd(x, o.v)
}
