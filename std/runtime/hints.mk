// Why: hot interpreter loops, GC barriers, and inline caches need branch
// prediction and cold-path hints without extra language syntax.
// What: runtime.Unlikely, runtime.Likely, and runtime.Cold.
// How: bodiless declarations checked as compiler intrinsics: Unlikely and
// Likely lower inline to `llvm.expect.i1`, and Cold marks its basic block
// path cold for branch weights and outgoing call attributes.
// Safe, O(1), allocates nothing.

/// Hints that `cond` is usually false and returns `cond` unchanged. Lowers
/// inline to `llvm.expect.i1`; no-op at compile time. O(1); allocates nothing.
export Unlikely(cond: bool): bool

/// Hints that `cond` is usually true and returns `cond` unchanged. Lowers
/// inline to `llvm.expect.i1`; no-op at compile time. O(1); allocates nothing.
export Likely(cond: bool): bool

/// Hints that the current execution path is cold. Marks the enclosing basic
/// block path cold for branch prediction and outgoing calls; no-op at compile
/// time. O(1); allocates nothing.
export Cold()
