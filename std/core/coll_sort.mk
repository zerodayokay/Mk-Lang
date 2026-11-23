// Why: a fast stable sort merges through scratch memory, and every
// allocation in Mk comes from an allocator the caller passes.
//
// What: Slice[T].SortStable, here in layer 1 since it allocates.
//
// How: mem_drift.mk's DriftSort through scratch on the stack (when it fits
// in 4,096 bytes) or from the given allocator a, freed before it returns.
// Short inputs and a zero-size T skip the allocation.

/// Sorts the elements by less, keeping equal elements in their order.
/// O(n log n) calls of less and moves on every input, and O(n) on input
/// that is already sorted or strictly descending. Past 20 elements it
/// merges through scratch memory of Len() / 2 elements or more (the whole
/// slice while that is at most 8,000,000 bytes, at least 48 elements): on
/// the stack when that fits in 4,096 bytes and T is aligned to at most 8,
/// and otherwise from a, falling back to Len() / 2 elements from a before
/// failing, and freed before it returns. Below 21 elements, for a
/// zero-size T, or when the scratch fits on the stack, it allocates
/// nothing from a. Fails with OutOfMemory when a cannot give even
/// Len() / 2 elements of scratch, before any element moves, so the slice
/// is unchanged. The algorithm is Rust's driftsort. For a stable sort
/// that takes no allocator, use SortStableInPlace.
export Slice[T].SortStable(mut self, a: &Alloc, const less: fn(&T, &T): bool): OutOfMemory!void {
    s := self.ItemsMut()
    // A short slice is one insertion sort (DriftStable's first step), here,
    // so it enters no frame that holds a stack scratch.
    if s.Len() <= DriftInsertMax {
        PdqInsertion(s, 0, s.Len(), less)
        return
    }
    if !DriftStable(s, a, less) {
        fail OutOfMemory
    }
}
