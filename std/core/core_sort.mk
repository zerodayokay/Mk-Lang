// Why: sorting must be fast on every input shape; layer 0 gives an
// unstable sort, an in-place stable sort and binary search with no
// allocator in sight, usable at compile time and in freestanding builds.
//
// What: Slice[T].Sort (pdqsort) and Slice[T].Search, with their
// partition, network, insertion and heap helpers. Slice[T].SortStableInPlace
// and StableSort are in core_sort_stable.mk; Slice[T].SortStable, which
// takes an allocator for scratch memory, is in coll_sort.mk.
//
// How: Sort is Go's pdqsort_func (Go 1.27, zsortfunc.go): ninther pivots,
// pattern breaking with a xorshift seeded by the length, heapsort after too
// many bad pivots; with four parts of Rust's ipnsort (core::slice::sort):
// - an element of 1 to 8 bytes sorts a range of up to PdqSmallMax by the
//   sort13 or sort9 network and an insertion sort of the rest (PdqSmall),
//   whose compare-and-swaps feed no branch, and a whole slice that short
//   skips a network its sorted prefix already spans; any other element
//   takes Go's insertion sort below 13;
// - for an element of 1 to 8 bytes, a range of 64 or more whose ninther
//   sample read neither increasing nor decreasing takes median3_rec's
//   pseudo-median of spread samples as its pivot (PdqMed3Rec), which
//   periodic inputs do not fool;
// - an element of at most PdqLomutoBytes is partitioned by the branch-free
//   cyclic Lomuto partition (partition_lomuto_branchless_cyclic), whose
//   comparisons feed no branch, so random keys cost no mispredictions,
//   unless its pivot sample reads increasing; such a range, and a larger
//   element, take Go's Hoare partition, which moves fewer bytes and leaves
//   an ordered range in order;
// - the elements equal to the pivot are split off by the same Lomuto
//   partition, for an element of at most PdqLomutoBytes.
// The insertion sorts hold one element out and shift (Go's swap per step
// makes the same calls of less and the same order). Unstable, in place,
// deterministic: std's tests hold it to a Go model
// (std-defects_sortmodel_test.go), comparison for comparison. less is a
// const parameter, so each comparator instantiates and inlines, a capturing
// literal included; a comparator that needs a context captures it, so there
// are no With forms. StableSort is WikiSort's in-place block merge sort;
// Search gives Go's sort.Search's index by a loop whose trip count depends
// only on the length, its probes chosen without a branch.
//
// The unsafe blocks move elements by raw reads and writes inside one range
// that a check before the block holds to s (a raw.Fail, unreachable from
// the exported functions, which pass ranges inside the slice), so no access
// leaves s whatever less answers; each value read out of a slot is written
// once, into a slot whose own value has already been read out, so a less
// that is no order still leaves a permutation, with no value copied or
// lost; tests_sort.mk drives every such range to its edges. Each element
// has exactly one live home at every call of less, the one less is given,
// but the slice itself is a permutation of its input between two calls only
// while no value is held out of it: during an insertion sort's shift
// (PdqInsertFrom: Sort's leaves, StableSort's base runs and second buffer,
// DriftSort's small sorts), the Lomuto partition (an element of at most 32
// bytes), a merge through StableSort's cache, and DriftSort's partitions
// and merges through its scratch (mem_drift.mk), a slot keeps a stale copy
// until the move that fills it, so a less that reads the slice through a
// capture may see one value twice and another not at all.
//
// Search's unsafe block only reads, and rests on its loop's invariant
// rather than on a check: i + n <= Len() whatever less answers, so each
// probe, i + ceil(n/2) - 1, is an element of s; tests_search.mk runs every
// length from 0 to 300 inside a guard band, under answers of less that
// follow no order, and fails on any probe outside s.

import std.raw

const PdqMaxInsertion = 12
// PdqSmallMax is the longest range PdqSmall sorts. ipnsort's network sort
// takes up to 32 by merging two sorted halves through scratch, which Mk
// cannot give a generic T (no uninitialized storage). Without the merge, the
// model counts 1.29 times Go's comparisons for 8-byte sawtooth keys at n =
// 1,000 with ranges of 20, over TestSortCountsWithinGo's bound (a network's
// calls are fixed, where insertion's are few on runs of equal keys); 16
// holds it.
const PdqSmallMax = 16
const PdqShortestNinther = 50
const PdqMaxSwaps = 12
const PdqMaxSteps = 5
const PdqShortestShifting = 50
// PdqMedRecLen is the shortest range PdqMed3Rec picks the pivot of
// (ipnsort's PSEUDO_MEDIAN_REC_THRESHOLD).
const PdqMedRecLen = 64
// PdqLomutoBytes is the largest element the Lomuto partition moves: two
// moves per element where Hoare's makes one per misplaced pair, so a large
// element keeps Hoare's. Rust's ipnsort takes up to 96 bytes; 32 is a
// guess, measured only at 8 bytes (u64).
const PdqLomutoBytes = 32

/// Sorts the elements by less, in place; equal elements may change order.
/// O(n log n) in the worst case, and the same result on every run; it
/// allocates nothing. A less that needs context is a function literal that
/// reads it, so no sort or search takes a separate context argument. The
/// algorithm is a
/// pattern-defeating quicksort.
export Slice[T].Sort(mut self, const less: fn(&T, &T): bool) {
    s := self.ItemsMut()
    n := s.Len()
    if PdqSmallFast[T]() && n <= PdqSmallMax {
        // A whole slice this short goes to PdqSmall with the length of its
        // sorted prefix, so a sorted or all-equal one costs n - 1 calls of
        // less, insertion's, and no network (whose 25 or 45 are fixed).
        var k := 1
        for k < n && !less(&s[k], &s[k - 1]) {
            k += 1
        }
        PdqSmall(s, 0, n, k, less)
        return
    }
    Pdq(s, 0, n, BitLen(n), less)
}

// BitLen is Go's bits.Len(uint(n)) for n >= 0.
BitLen(n: int): int {
    return 64 - raw.Clz64(Trunc[u64](n))
}

PdqReverse[T](s: mut []T, a: int, b: int) {
    var i := a
    var j := b - 1
    for i < j {
        SwapAt(s, i, j)
        i += 1
        j -= 1
    }
}

// PdqBreakPatterns is Go's breakPatterns_func: it swaps idx, idx + 1 and
// idx + 2, which are mid - 1, mid and mid + 1 for mid = a + length/4*2
// (idx - 1 + i would give a different permutation from Go's).
PdqBreakPatterns[T](s: mut []T, a: int, b: int) {
    length := b - a
    if length < 8 {
        return
    }
    var r := Trunc[u64](length)
    modulus := Trunc[u64](1) << BitLen(length)
    idx := a + (length / 4) * 2 - 1
    for i in 0..3 {
        r = r ^ (r << 13)
        r = r ^ (r >> 7)
        r = r ^ (r << 17)
        var other := Trunc[int](r & (modulus - 1))
        if other >= length {
            other -= length
        }
        SwapAt(s, idx + i, a + other)
    }
}

// PdqInsertion insertion sorts s[a..b] (PdqInsertFrom); Go's
// insertionSort_func, a swap per step, makes the same calls of less and
// leaves the same order.
PdqInsertion[T](s: mut []T, a: int, b: int, const less: fn(&T, &T): bool) {
    if b - a < 2 {
        return
    }
    if a < 0 || b > s.Len() {
        raw.Fail(2, "sort: insertion range out of bounds")
    }
    unsafe "0 <= a < a + 2 <= b <= s.Len() (checked above): the b - a elements at p + a are s[a..b], initialized, as PdqInsertFrom needs" {
        PdqInsertFrom(raw.Offset(raw.DataMut(s), a), 1, b - a, less)
    }
}

// PdqInsertFrom insertion sorts the n elements at p, whose first k are
// sorted (k >= 1): each later element that is less than the one before it
// is held out, the greater ones before it shift up one slot, and it fills
// the hole, so each step is one move where a swap is three. The caller
// vouches the n slots at p are initialized and k >= 1.
PdqInsertFrom[T](p: *T, k: int, n: int, const less: fn(&T, &T): bool) {
    unsafe "the caller vouches p[0..n] is initialized and 1 <= k: i runs in k..n and j from i down to 1, so every read is of p[i] or p[j - 1] and every write of p[j], all in 0..n; held owns the value of the one hole, at j, which each shift moves down and the last write fills" {
        for i in k..n {
            if less(&*raw.Offset(p, i), &*raw.Offset(p, i - 1)) {
                held := raw.Read(raw.Offset(p, i))
                var j := i
                for {
                    raw.Write(raw.Offset(p, j), raw.Read(raw.Offset(p, j - 1)))
                    j -= 1
                    if j == 0 || !less(&held, &*raw.Offset(p, j - 1)) {
                        break
                    }
                }
                raw.Write(raw.Offset(p, j), held)
            }
        }
    }
}

// PdqSmallFast is whether T takes PdqSmall for its short ranges: 1 to 8
// bytes, where a compare-and-swap is two loads and two stores chosen by the
// comparison (ipnsort takes its networks for the same sizes,
// has_efficient_in_place_swap). A zero-size T keeps insertion, whose calls
// of less are the fewest.
PdqSmallFast[T](): bool {
    return SizeOf[T]() > 0 && SizeOf[T]() <= 8
}

// PdqSmall sorts s[a..b], at most PdqSmallMax elements of a PdqSmallFast
// T, whose first k0 are sorted (k0 >= 1): 13 or more by the sort13 network,
// 9 or more by sort9, unless the sorted prefix already spans the network or
// leaves two or fewer elements after it, and the rest insertion sorted into
// the sorted prefix (ipnsort's small_sort_network sorts each half of a range
// of up to 32 so).
PdqSmall[T](s: mut []T, a: int, b: int, k0: int, const less: fn(&T, &T): bool) {
    n := b - a
    if n < 2 {
        return
    }
    if a < 0 || b > s.Len() || k0 < 1 {
        raw.Fail(2, "sort: small range out of bounds")
    }
    unsafe "0 <= a < a + 2 <= b <= s.Len() and k0 >= 1 (checked above): the n = b - a elements at p are s[a..b], initialized; sort13 runs only when n >= 13 and sort9 when n >= 9, and PdqInsertFrom gets k >= 1" {
        p := raw.Offset(raw.DataMut(s), a)
        var k := k0
        if n >= 13 {
            if k < 13 && n - k > 2 {
                PdqSort13(p, less)
                k = 13
            }
        } else if n >= 9 {
            if k < 9 && n - k > 2 {
                PdqSort9(p, less)
                k = 9
            }
        }
        PdqInsertFrom(p, k, n, less)
    }
}

// PdqSwapIfLess orders the elements at p + i and p + j: when the one at j is
// less than the one at i they exchange (ipnsort's swap_if_less). The
// comparison picks the source of each move, so no branch depends on it. The
// caller vouches both slots are initialized and i != j.
PdqSwapIfLess[T](p: *T, i: int, j: int, const less: fn(&T, &T): bool) {
    unsafe "the caller vouches p + i and p + j are distinct initialized slots: t takes the value of the slot written last, so each slot ends with one of the two values and each value lands once, swapped when x and in place when not" {
        pa := raw.Offset(p, i)
        pb := raw.Offset(p, j)
        x := less(&*pb, &*pa)
        sa := if x { pb } else { pa }
        sb := if x { pa } else { pb }
        t := raw.Read(sb)
        raw.Write(pa, raw.Read(sa))
        raw.Write(pb, t)
    }
}

// PdqSort9 and PdqSort13 sort the first 9 and 13 elements at p by ipnsort's
// sort9_optimal and sort13_optimal networks (25 and 45 compare-and-swaps,
// 7 and 10 deep), which sort every input of their length: the model's
// TestSortNetworksZeroOne checks every input of 0s and 1s, and
// TestSortNetworksMatchMk these bodies against the model's pairs. The
// caller vouches p holds 9 or 13 initialized elements.
PdqSort9[T](p: *T, const less: fn(&T, &T): bool) {
    PdqSwapIfLess(p, 0, 3, less)
    PdqSwapIfLess(p, 1, 7, less)
    PdqSwapIfLess(p, 2, 5, less)
    PdqSwapIfLess(p, 4, 8, less)
    PdqSwapIfLess(p, 0, 7, less)
    PdqSwapIfLess(p, 2, 4, less)
    PdqSwapIfLess(p, 3, 8, less)
    PdqSwapIfLess(p, 5, 6, less)
    PdqSwapIfLess(p, 0, 2, less)
    PdqSwapIfLess(p, 1, 3, less)
    PdqSwapIfLess(p, 4, 5, less)
    PdqSwapIfLess(p, 7, 8, less)
    PdqSwapIfLess(p, 1, 4, less)
    PdqSwapIfLess(p, 3, 6, less)
    PdqSwapIfLess(p, 5, 7, less)
    PdqSwapIfLess(p, 0, 1, less)
    PdqSwapIfLess(p, 2, 4, less)
    PdqSwapIfLess(p, 3, 5, less)
    PdqSwapIfLess(p, 6, 8, less)
    PdqSwapIfLess(p, 2, 3, less)
    PdqSwapIfLess(p, 4, 5, less)
    PdqSwapIfLess(p, 6, 7, less)
    PdqSwapIfLess(p, 1, 2, less)
    PdqSwapIfLess(p, 3, 4, less)
    PdqSwapIfLess(p, 5, 6, less)
}

PdqSort13[T](p: *T, const less: fn(&T, &T): bool) {
    PdqSwapIfLess(p, 0, 12, less)
    PdqSwapIfLess(p, 1, 10, less)
    PdqSwapIfLess(p, 2, 9, less)
    PdqSwapIfLess(p, 3, 7, less)
    PdqSwapIfLess(p, 5, 11, less)
    PdqSwapIfLess(p, 6, 8, less)
    PdqSwapIfLess(p, 1, 6, less)
    PdqSwapIfLess(p, 2, 3, less)
    PdqSwapIfLess(p, 4, 11, less)
    PdqSwapIfLess(p, 7, 9, less)
    PdqSwapIfLess(p, 8, 10, less)
    PdqSwapIfLess(p, 0, 4, less)
    PdqSwapIfLess(p, 1, 2, less)
    PdqSwapIfLess(p, 3, 6, less)
    PdqSwapIfLess(p, 7, 8, less)
    PdqSwapIfLess(p, 9, 10, less)
    PdqSwapIfLess(p, 11, 12, less)
    PdqSwapIfLess(p, 4, 6, less)
    PdqSwapIfLess(p, 5, 9, less)
    PdqSwapIfLess(p, 8, 11, less)
    PdqSwapIfLess(p, 10, 12, less)
    PdqSwapIfLess(p, 0, 5, less)
    PdqSwapIfLess(p, 3, 8, less)
    PdqSwapIfLess(p, 4, 7, less)
    PdqSwapIfLess(p, 6, 11, less)
    PdqSwapIfLess(p, 9, 10, less)
    PdqSwapIfLess(p, 0, 1, less)
    PdqSwapIfLess(p, 2, 5, less)
    PdqSwapIfLess(p, 6, 9, less)
    PdqSwapIfLess(p, 7, 8, less)
    PdqSwapIfLess(p, 10, 11, less)
    PdqSwapIfLess(p, 1, 3, less)
    PdqSwapIfLess(p, 2, 4, less)
    PdqSwapIfLess(p, 5, 6, less)
    PdqSwapIfLess(p, 9, 10, less)
    PdqSwapIfLess(p, 1, 2, less)
    PdqSwapIfLess(p, 3, 4, less)
    PdqSwapIfLess(p, 5, 7, less)
    PdqSwapIfLess(p, 6, 8, less)
    PdqSwapIfLess(p, 2, 3, less)
    PdqSwapIfLess(p, 4, 5, less)
    PdqSwapIfLess(p, 6, 7, less)
    PdqSwapIfLess(p, 8, 9, less)
    PdqSwapIfLess(p, 3, 4, less)
    PdqSwapIfLess(p, 5, 6, less)
}

// PdqMed3 is ipnsort's median3: the index of the median of s[a], s[b] and
// s[c] by less, in two calls when s[a] is the median and three otherwise.
PdqMed3[T](s: mut []T, a: int, b: int, c: int, const less: fn(&T, &T): bool): int {
    x := less(&s[a], &s[b])
    y := less(&s[a], &s[c])
    if x == y {
        z := less(&s[b], &s[c])
        return if z != x { c } else { b }
    }
    return a
}

// PdqMed3Rec is the pseudo-median of 3^k samples of the blocks of n
// elements at a, b and c: ipnsort's median3_rec, which recurses into three
// blocks of n / 8 of each block while n >= 8, except that the sub-blocks are
// centred at 1/8, 1/2 and 7/8 of their block where ipnsort starts them at 0,
// 1/2 and 7/8, so a range and its mirror image are sampled alike (with
// ipnsort's offsets the model counts 1.28 times Go's comparisons for 8-byte
// organ-pipe keys at n = 20,000, over TestSortCountsWithinGo's bound: both
// outer blocks fall on the smallest keys). Each sub-block ends by
// 6 * (n / 8) + (n / 8) / 2 + n / 8 <= n within its block, so every index is
// in the range the first call's blocks lie in, and s[a] and its kin are
// checked reads.
PdqMed3Rec[T](s: mut []T, a: int, b: int, c: int, n: int, const less: fn(&T, &T): bool): int {
    var x := a
    var y := b
    var z := c
    if n >= 8 {
        n8 := n / 8
        h := n8 / 2
        x = PdqMed3Rec(s, a + h, a + n8 * 3 + h, a + n8 * 6 + h, n8, less)
        y = PdqMed3Rec(s, b + h, b + n8 * 3 + h, b + n8 * 6 + h, n8, less)
        z = PdqMed3Rec(s, c + h, c + n8 * 3 + h, c + n8 * 6 + h, n8, less)
    }
    return PdqMed3(s, x, y, z, less)
}

// PdqMedSpread is PdqMed3Rec's pivot for s[a..a + length], length >= 8:
// the three blocks of length / 8 centred at 1/8, 1/2 and 7/8 of the range.
PdqMedSpread[T](s: mut []T, a: int, length: int, const less: fn(&T, &T): bool): int {
    n8 := length / 8
    h := n8 / 2
    return PdqMed3Rec(s, a + h, a + n8 * 3 + h, a + n8 * 6 + h, n8, less)
}

PdqSiftDown[T](s: mut []T, lo: int, hi: int, first: int, const less: fn(&T, &T): bool) {
    var root := lo
    for {
        var child := 2 * root + 1
        if child >= hi {
            return
        }
        if child + 1 < hi && less(&s[first + child], &s[first + child + 1]) {
            child += 1
        }
        if !less(&s[first + root], &s[first + child]) {
            return
        }
        SwapAt(s, first + root, first + child)
        root = child
    }
}

PdqHeapSort[T](s: mut []T, a: int, b: int, const less: fn(&T, &T): bool) {
    first := a
    hi := b - a
    var i := (hi - 1) / 2
    for i >= 0 {
        PdqSiftDown(s, i, hi, first, less)
        i -= 1
    }
    i = hi - 1
    for i >= 0 {
        SwapAt(s, first, first + i)
        PdqSiftDown(s, 0, i, first, less)
        i -= 1
    }
}

// PdqMedian orders a, b, c by less as Go's median_func does, counting the
// swaps, and returns the middle index.
PdqMedian[T](s: mut []T, a: int, b: int, c: int, swaps: mut int, const less: fn(&T, &T): bool): int {
    var x := a
    var y := b
    var z := c
    if less(&s[y], &s[x]) {
        *swaps += 1
        t := x
        x = y
        y = t
    }
    if less(&s[z], &s[y]) {
        *swaps += 1
        t := y
        y = z
        z = t
    }
    if less(&s[y], &s[x]) {
        *swaps += 1
        t := x
        x = y
        y = t
    }
    return y
}

// PdqPivot is Go's choosePivot_func: the pivot index, and the hint 0
// unknown, 1 increasing, 2 decreasing.
PdqPivot[T](s: mut []T, a: int, b: int, hint: mut int, const less: fn(&T, &T): bool): int {
    l := b - a
    var swaps := 0
    var i := a + l / 4 * 1
    var j := a + l / 4 * 2
    var k := a + l / 4 * 3
    if l >= 8 {
        if l >= PdqShortestNinther {
            i = PdqMedian(s, i - 1, i, i + 1, mut swaps, less)
            j = PdqMedian(s, j - 1, j, j + 1, mut swaps, less)
            k = PdqMedian(s, k - 1, k, k + 1, mut swaps, less)
        }
        j = PdqMedian(s, i, j, k, mut swaps, less)
    }
    if swaps == 0 {
        *hint = 1
    } else if swaps == PdqMaxSwaps {
        *hint = 2
    } else {
        *hint = 0
    }
    return j
}

PdqPartialInsertion[T](s: mut []T, a: int, b: int, const less: fn(&T, &T): bool): bool {
    var i := a + 1
    for _ in 0..PdqMaxSteps {
        for i < b && !less(&s[i], &s[i - 1]) {
            i += 1
        }
        if i == b {
            return true
        }
        if b - a < PdqShortestShifting {
            return false
        }
        SwapAt(s, i, i - 1)
        if i - a >= 2 {
            var j := i - 1
            for j >= 1 {
                if !less(&s[j], &s[j - 1]) {
                    break
                }
                SwapAt(s, j, j - 1)
                j -= 1
            }
        }
        if b - i >= 2 {
            var j := i + 1
            for j < b {
                if !less(&s[j], &s[j - 1]) {
                    break
                }
                SwapAt(s, j, j - 1)
                j += 1
            }
        }
    }
    return false
}

// PdqPartition is Go's partition_func; alreadyPartitioned is set when no
// element moved. An element of at most PdqLomutoBytes goes to
// PdqPartitionLomuto, which never reports already partitioned, unless
// increasing (the pivot sample's hint 1): the cyclic Lomuto rotates the
// part not less than the pivot by one, so on a nearly sorted range the
// next PdqPartialInsertion would shift that element across the range and
// fail, about 3 times Go's comparisons at 100 swaps in 10^6; Go's
// Hoare partition leaves an ordered range in order and says so.
PdqPartition[T](s: mut []T, a: int, b: int, pivot: int, increasing: bool, already: mut bool, const less: fn(&T, &T): bool): int {
    if SizeOf[T]() <= PdqLomutoBytes && !increasing {
        *already = false
        return PdqPartitionLomuto(s, a, b, pivot, false, less)
    }
    SwapAt(s, a, pivot)
    var i := a + 1
    var j := b - 1
    for i <= j && less(&s[i], &s[a]) {
        i += 1
    }
    for i <= j && !less(&s[j], &s[a]) {
        j -= 1
    }
    if i > j {
        SwapAt(s, j, a)
        *already = true
        return j
    }
    SwapAt(s, i, j)
    i += 1
    j -= 1
    for {
        for i <= j && less(&s[i], &s[a]) {
            i += 1
        }
        for i <= j && !less(&s[j], &s[a]) {
            j -= 1
        }
        if i > j {
            break
        }
        SwapAt(s, i, j)
        i += 1
        j -= 1
    }
    SwapAt(s, j, a)
    *already = false
    return j
}

// PdqPartitionLomuto puts s[pivot] at a, then partitions s[a + 1..b] in
// one pass with a gap (ipnsort's cyclic Lomuto): the element at a + 1 is
// held out, and for each later element r the first element not less than
// the pivot moves into the gap, r moves to its place, and the count of
// lesser elements grows by the comparison's result, so the comparison
// feeds no branch. The held element goes back last. The pivot ends at the
// returned index, the elements before it less than it, those after it not.
// With neg an element goes before the pivot when the pivot is not less than
// it (ipnsort's partition by !is_less(pivot, x)), so the elements before it
// are those not greater than it: the equal partition, when no element of
// the range is less than the pivot. Needs b - a >= 2. O(b - a) comparisons
// and moves; allocates nothing.
PdqPartitionLomuto[T](s: mut []T, a: int, b: int, pivot: int, const neg: bool, const less: fn(&T, &T): bool): int {
    SwapAt(s, a, pivot)
    if a < 0 || b > s.Len() || b - a < 2 {
        raw.Fail(2, "sort: partition range out of bounds")
    }
    var lt := a + 1
    unsafe "0 <= a < a + 1 < b <= s.Len() (checked above): r runs in a + 2..b, lt and gap stay in a + 1..r (lt grows by at most one a step, from a + 1), and the pivot at a never moves in the loop, so every slot read or written is in a..b; every slot from a + 1 to b is initialized but the gap, which held's move vacated and a move fills before the next one empties it" {
        p := raw.DataMut(s)
        pv := raw.Offset(p, a)
        held := raw.Read(raw.Offset(p, a + 1))
        var gap := a + 1
        for r in (a + 2)..b {
            rp := raw.Offset(p, r)
            x := if neg { !less(&*pv, &*rp) } else { less(&*rp, &*pv) }
            raw.Write(raw.Offset(p, gap), raw.Read(raw.Offset(p, lt)))
            raw.Write(raw.Offset(p, lt), raw.Read(rp))
            gap = r
            lt += if x { 1 } else { 0 }
        }
        raw.Write(raw.Offset(p, gap), raw.Read(raw.Offset(p, lt)))
        x := if neg { !less(&*pv, &held) } else { less(&held, &*pv) }
        raw.Write(raw.Offset(p, lt), held)
        lt += if x { 1 } else { 0 }
    }
    SwapAt(s, a, lt - 1)
    return lt - 1
}

PdqPartitionEqual[T](s: mut []T, a: int, b: int, pivot: int, const less: fn(&T, &T): bool): int {
    SwapAt(s, a, pivot)
    var i := a + 1
    var j := b - 1
    for {
        for i <= j && !less(&s[a], &s[i]) {
            i += 1
        }
        for i <= j && less(&s[a], &s[j]) {
            j -= 1
        }
        if i > j {
            break
        }
        SwapAt(s, i, j)
        i += 1
        j -= 1
    }
    return i
}

Pdq[T](s: mut []T, a0: int, b0: int, limit0: int, const less: fn(&T, &T): bool) {
    var a := a0
    var b := b0
    var limit := limit0
    var balanced := true
    var partitioned := true
    for {
        length := b - a
        if PdqSmallFast[T]() {
            if length <= PdqSmallMax {
                PdqSmall(s, a, b, 1, less)
                return
            }
        } else if length <= PdqMaxInsertion {
            PdqInsertion(s, a, b, less)
            return
        }
        if limit == 0 {
            PdqHeapSort(s, a, b, less)
            return
        }
        if !balanced {
            PdqBreakPatterns(s, a, b)
            limit -= 1
        }
        var hint := 0
        var pivot := PdqPivot(s, a, b, mut hint, less)
        if hint == 2 {
            PdqReverse(s, a, b)
            pivot = (b - 1) - (pivot - a)
            hint = 1
        } else if hint == 0 && PdqSmallFast[T]() && length >= PdqMedRecLen {
            // The ninther samples adjacent triples, which a periodic input
            // (a sawtooth) can make all alike. Only for an element of 1 to 8
            // bytes, the class with the cheap compares and the timed gain:
            // on k reversed blocks or k descending runs (k from 2 to 10) the
            // spread pivot cost a 40-byte element 1.26 to 1.56 times Go's
            // calls of less, where the ninther's matches Go's exactly (the
            // review's probe), and 16- and 40-byte shapes timed no slower
            // without it (tp/sweep6, writer's probe). A sample that read increasing
            // keeps the ninther's pivot, which the partial insertion sort
            // and Hoare's partition on nearly sorted input rely on (taking
            // the spread pivot there too, the model counts 1.38 times Go's
            // comparisons for sorted input with 10 swaps at n = 1,000,000,
            // over TestNearlySortedWithinGo's bound).
            pivot = PdqMedSpread(s, a, length, less)
        }
        if balanced && partitioned && hint == 1 {
            if PdqPartialInsertion(s, a, b, less) {
                return
            }
        }
        if a > 0 && !less(&s[a - 1], &s[pivot]) {
            // No element of s[a..b] is less than s[a - 1], which is not
            // less than the pivot: the elements not greater than the pivot
            // are its equals, and the next range starts after them.
            if SizeOf[T]() <= PdqLomutoBytes {
                a = PdqPartitionLomuto(s, a, b, pivot, true, less) + 1
            } else {
                a = PdqPartitionEqual(s, a, b, pivot, less)
            }
            continue
        }
        var already := false
        mid := PdqPartition(s, a, b, pivot, hint == 1, mut already, less)
        partitioned = already
        left := mid - a
        right := b - mid
        threshold := length / 8
        if left < right {
            balanced = left >= threshold
            Pdq(s, a, mid, limit, less)
            a = mid + 1
        } else {
            balanced = right >= threshold
            Pdq(s, mid + 1, b, limit, less)
            b = mid
        }
    }
}

/// Binary search: on a slice where less(element, key) holds for a prefix
/// and not after it, the prefix's length. That is the first index whose
/// element is not less than key, or Len() when there is none: the place
/// where key goes to keep the slice sorted. On any other slice it returns
/// some index from 0 to Len(). O(log n), at most ceil(log2(n + 1)) calls of
/// less; allocates nothing and never panics.
export Slice[T].Search[K](&self, key: &K, const less: fn(&T, &K): bool): int {
    // The answer lies from i to i + n, both included. Each step asks less
    // of element i + h - 1, h = ceil(n/2): when it holds the answer lies
    // from i + h to i + n, else from i to i + h - 1; either way n becomes
    // floor(n/2), which covers both (h - 1 <= floor(n/2) = n - h). So the
    // run of n, and the count of calls, bit length of Len(), depend only on
    // the length: the loop's branch goes the same way on every search, and
    // the answer of less only picks i, by raw.Select, a conditional move,
    // so a random key costs no misprediction (Rust's binary_search and
    // Zig's partitionPoint loop so too).
    s := self.Items()
    var i := 0
    var n := s.Len()
    unsafe "i + n <= s.Len() holds on entry and after each step whatever less answers (n shrinks by h every step, and i grows by h or by 0), and h >= 1 while n > 0, so i + h - 1 is in 0..s.Len(): the wrapping ops cannot wrap and each read is of an element of s" {
        p := raw.Data(s)
        for n > 0 {
            h := n -% (n >> 1)
            j := i +% h
            i = raw.Select(less(&*raw.Offset(p, j -% 1), key), j, i)
            n = n >> 1
        }
    }
    return i
}
