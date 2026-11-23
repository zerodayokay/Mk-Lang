// Why: sorting must be fast on every input shape; layer 0 gives an
// unstable sort, an in-place stable sort and binary search with no
// allocator in sight, usable at compile time and in freestanding builds.
//
// What: Slice[T].Sort (pdqsort), Slice[T].SortStableInPlace (a block merge
// sort), Slice[T].Search and StableSort, with their partition, network,
// insertion, heap and merge helpers. Slice[T].SortStable, which takes an
// allocator for scratch memory, is in coll_sort.mk.
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

/// Sorts the elements by less, keeping equal elements in their order,
/// strictly in place with no allocator or heap allocation. O(n log n)
/// calls of less and O(n log n) moves on every input, with a fixed
/// stack cache of at most 4,096 bytes for elements aligned to at most 8.
/// Works at compile time and in freestanding programs. The algorithm is
/// a block merge sort. For faster sorting through scratch memory from
/// an allocator, use SortStable.
export Slice[T].SortStableInPlace(mut self, const less: fn(&T, &T): bool) {
    s := self.ItemsMut()
    if s.Len() < 2 * BlkRun {
        PdqInsertion(s, 0, s.Len(), less)
        return
    }
    StableSort(s, less)
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

// StableSort sorts s by less keeping equal elements in their order, in place
// with no allocation: WikiSort, the block merge sort of Pok-Son
// Kim and Arne Kutzner ("Ratio Based Stable In-Place Merging", TAMC 2008) as
// Mike McFadden's WikiSort (github.com/BonzaiThePenguin/WikiSort, public
// domain) builds it and Zig's std.sort.block ports it, with WikiSort's cache
// as 4,096 bytes on the stack (BlkCacheCap). Runs of BlkRun to 2 * BlkRun
// elements are insertion sorted; a level whose runs fit the cache merges
// them through it (BlkCacheLevel2, and two levels at once when four runs fit,
// BlkCacheLevel4); a longer level merges pairs of runs by tagging A's blocks
// with an internal buffer of about sqrt(run) distinct values pulled out of
// the level's runs and rolling them through B, each dropped A block merged
// through the cache when it fits (BlkMergeExternal), else through a second
// internal buffer by swaps (BlkMergeInternal), else by rotation (BlkLevel).
// O(n log n) calls of less and O(n log n) moves on every input; no
// allocation; the call stack is a constant depth, and the cache adds 4,096
// bytes to StableSort's frame. A stable result is unique, so only the counts
// depend on the algorithm: std-coll_blockmerge_model_test.go holds its Go
// model, whose counts the Mk body's equal.
//
// A less that is no strict weak order still ends with a permutation of s:
// every move is a swap or a move into a hole (below), every index is derived
// from lengths and from the searches' results, which stay inside their
// ranges whatever less answers, and three places clamp a search's result
// (the two pulls and BlkMergeInPlace's step), a no-op under an order (the Go
// model counts them). SymSwapRange, BlkMergeInternal and the cache's moves
// check their whole range once and then move raw. The Go model panics where
// any of these checks would fail, so its tests, the no-order relations
// included, show the checks never fail on their inputs, and
// TestStdColl_BlockMergeModelEdges shows tests_sort.mk's inputs drive them
// to the first and last slots and to runs of one.
//
// The cache holds no value of its own: a value moves into it by a raw copy
// after which its slot in s is a hole, and moves back into a hole of s the
// same way, so at every call of less each element has exactly one live home,
// in s or in the cache, and less reads only live homes. Each run of moves
// into the cache is moved back out before the merge that took it ends, so
// the cache is empty when StableSort returns; Mk runs no destructor and
// nothing unwinds on a panic, so a value is never dropped or doubled there.
// The storage trick needs the compiler to emit no type-based alias metadata
// (TestSortCacheNoTBAA pins it) and the evaluator to treat memory as bytes,
// as it does (tests_sort.mk sorts at compile time against run time).
StableSort[T](s: mut []T, const less: fn(&T, &T): bool) {
    n := s.Len()
    if n < 2 * BlkRun {
        PdqInsertion(s, 0, n, less)
        return
    }
    var it := BlkIterNew(n)
    for !it.Done() {
        a := it.dec
        b := it.Next()
        PdqInsertion(s, a, b, less)
    }
    cap := BlkCacheCap[T]()
    if cap == 0 {
        BlkLevels(s, mut it, raw.Null[T](), 0, less)
        return
    }
    var words: [512]u64 = [0; 512]
    unsafe "words is 4,096 bytes aligned to 8, and BlkCacheCap is above 0 only for a T of 1 to 4,096 bytes aligned to at most 8: cap = 4,096 / SizeOf[T]() slots of T, each aligned, lie in words, and hold no value until a move puts one there" {
        BlkLevels(s, mut it, raw.PtrCast[[512]u64, T](raw.OfMut(mut words)), cap, less)
    }
}

// BlkLevels runs the merge levels, from the base runs up, with the cache of
// cap slots at cache (cap 0: none).
BlkLevels[T](s: mut []T, it: mut BlkIter, cache: *T, cap: int, const less: fn(&T, &T): bool) {
    for {
        BlkLevel(s, it, cache, cap, less)
        if !it.NextLevel() {
            break
        }
    }
}

// BlkCacheBytes is the stack cache's size: WikiSort's and Zig's cache holds
// 512 elements, 4,096 bytes of 8-byte ones.
const BlkCacheBytes = 4096

// BlkCacheCap is how many elements of T the cache holds: 4,096 / SizeOf[T](),
// or 0 when T cannot live in a [512]u64 (alignment above 8, or a size of 0 or
// above 4,096 bytes), and StableSort then merges without one.
BlkCacheCap[T](): int {
    if AlignOf[T]() > 8 || SizeOf[T]() == 0 || SizeOf[T]() > BlkCacheBytes {
        return 0
    }
    return BlkCacheBytes / SizeOf[T]()
}

// BlkCacheIn moves s[from..from + n] into the cache slots at..at + n; the
// slots it leaves in s are holes until a move fills them. The caller vouches
// cache holds cap slots, as StableSort makes them.
BlkCacheIn[T](s: mut []T, from: int, n: int, cache: *T, cap: int, at: int) {
    if n <= 0 {
        return
    }
    if from < 0 || from > s.Len() - n || at < 0 || at > cap - n {
        raw.Fail(2, "sort: cache range out of bounds")
    }
    unsafe "n > 0, from..from + n in s and at..at + n in the cache's cap slots (checked above; the caller vouches the cache), two different arrays, so the copy does not overlap; it moves the values: their slots in s are holes from here" {
        raw.Copy(raw.Offset(cache, at), raw.Offset(raw.DataMut(s), from), n)
    }
}

// BlkCacheOut moves the cache slots at..at + n into s[to..to + n], which
// the caller vouches are holes, as it vouches the cache.
BlkCacheOut[T](cache: *T, cap: int, at: int, n: int, s: mut []T, to: int) {
    if n <= 0 {
        return
    }
    if at < 0 || at > cap - n || to < 0 || to > s.Len() - n {
        raw.Fail(2, "sort: cache range out of bounds")
    }
    unsafe "n > 0, at..at + n in the cache's cap slots and to..to + n in s (checked above), two different arrays; the slots of s are holes (the caller vouches), so the copy moves the values without overwriting one" {
        raw.Copy(raw.Offset(raw.DataMut(s), to), raw.Offset(cache, at), n)
    }
}

// BlkMergeRaw merges the runs src[a..m] and src[m..b] into dst[0..b - a]
// (WikiSort's MergeInto), equal values the first run's first, moving every
// value: src[a..b] is all holes after it. The caller vouches a < m < b, that
// src[a..b] holds values and that dst holds b - a free slots of another
// array.
BlkMergeRaw[T](src: *T, a: int, m: int, b: int, dst: *T, const less: fn(&T, &T): bool) {
    unsafe "the caller vouches a < m < b, values in src[a..b] and b - a free slots at dst in another array: while both runs have values ai is in a..m and bi in m..b, both unmoved, and the slot written, d = (ai - a) + (bi - m), is below b - a; when a run ends the other's rest, m - ai or b - bi values, moves to d..b - a" {
        var ai := a
        var bi := m
        var d := 0
        for {
            pa := raw.Offset(src, ai)
            pb := raw.Offset(src, bi)
            if !less(&*pb, &*pa) {
                raw.Write(raw.Offset(dst, d), raw.Read(pa))
                ai += 1
                d += 1
                if ai == m {
                    raw.Copy(raw.Offset(dst, d), raw.Offset(src, bi), b - bi)
                    break
                }
            } else {
                raw.Write(raw.Offset(dst, d), raw.Read(pb))
                bi += 1
                d += 1
                if bi == b {
                    raw.Copy(raw.Offset(dst, d), raw.Offset(src, ai), m - ai)
                    break
                }
            }
        }
    }
}

// BlkMergeToCache merges s[a..m] and s[m..b] into the cache slots at..at +
// b - a; s[a..b] is holes after it. The caller vouches the cache.
BlkMergeToCache[T](s: mut []T, a: int, m: int, b: int, cache: *T, cap: int, at: int, const less: fn(&T, &T): bool) {
    if a < 0 || a >= m || m >= b || b > s.Len() || at < 0 || at > cap - (b - a) {
        raw.Fail(2, "sort: cache range out of bounds")
    }
    unsafe "0 <= a < m < b <= s.Len() and at + (b - a) <= cap (checked above): BlkMergeRaw's runs are values of s and its b - a destination slots are the cache's, another array" {
        BlkMergeRaw(raw.DataMut(s), a, m, b, raw.Offset(cache, at), less)
    }
}

// BlkMergeFromCache merges the cache's runs [0..m] and [m..b] into
// s[to..to + b], which the caller vouches are holes, as it vouches the cache.
BlkMergeFromCache[T](cache: *T, cap: int, m: int, b: int, s: mut []T, to: int, const less: fn(&T, &T): bool) {
    if m <= 0 || m >= b || b > cap || to < 0 || to > s.Len() - b {
        raw.Fail(2, "sort: cache range out of bounds")
    }
    unsafe "0 < m < b <= cap and to + b <= s.Len() (checked above): BlkMergeRaw's runs are the cache's values and its b destination slots are holes of s, another array" {
        BlkMergeRaw(cache, 0, m, b, raw.Offset(raw.DataMut(s), to), less)
    }
}

// BlkMergeExternal merges A, whose ae - as values wait in the cache's first
// slots (moved there from s[as..ae], now holes), with s[bs..be], bs = ae,
// into s[as..be], equal values A's first (WikiSort's MergeExternal). The
// holes are always s[as + ac + bc..bs + bc], ahead of B's next value: each
// value moves into the first hole, A's slot or one a moved B value left.
BlkMergeExternal[T](s: mut []T, as: int, ae: int, bs: int, be: int, cache: *T, cap: int, const less: fn(&T, &T): bool) {
    al := ae - as
    bl := be - bs
    if al <= 0 {
        return
    }
    if as < 0 || ae != bs || bl < 0 || be > s.Len() || al > cap {
        raw.Fail(2, "sort: merge range out of bounds")
    }
    var ac := 0
    var bc := 0
    unsafe "0 <= as, ae = bs <= be <= s.Len() and 0 < al <= cap (checked above; the caller vouches the cache holds A's values in its first al slots and s[as..ae] are holes). Each pass starts with ac < al and bc < bl, so the cache slot read is below al, the B slot bs + bc below be, and the hole written, as + ac + bc, is below bs + bc, never a slot still to be read; the last copy moves the al - ac values left in the cache into the al - ac holes left, s[as + ac + bc..bs + bc]" {
        p := raw.DataMut(s)
        if bl > 0 {
            for {
                pa := raw.Offset(cache, ac)
                pb := raw.Offset(p, bs + bc)
                d := raw.Offset(p, as + ac + bc)
                if !less(&*pb, &*pa) {
                    raw.Write(d, raw.Read(pa))
                    ac += 1
                    if ac >= al {
                        break
                    }
                } else {
                    raw.Write(d, raw.Read(pb))
                    bc += 1
                    if bc >= bl {
                        break
                    }
                }
            }
        }
        raw.Copy(raw.Offset(p, as + ac + bc), raw.Offset(cache, ac), al - ac)
    }
}

// BlkCacheLevel2 merges each pair of runs of a level whose runs fit the
// cache (WikiSort's first cache branch): B wholly before A is a rotation, a
// pair in order is left, any other has A moved to the cache and merged back
// with B (BlkMergeExternal).
BlkCacheLevel2[T](s: mut []T, it: mut BlkIter, cache: *T, cap: int, const less: fn(&T, &T): bool) {
    it.Begin()
    for !it.Done() {
        as := it.dec
        ae := it.Next()
        be := it.Next()
        if less(&s[be - 1], &s[as]) {
            BlkRotate(s, as, ae, be)
        } else if less(&s[ae], &s[ae - 1]) {
            BlkCacheIn(s, as, ae - as, cache, cap, 0)
            BlkMergeExternal(s, as, ae, ae, be, cache, cap, less)
        }
    }
}

// BlkCacheLevel4 merges two levels at once where four runs fit the cache
// (WikiSort's other cache branch): A1 and B1 merged into the cache's front,
// A2 and B2 behind them (each pair copied in reverse when B is wholly
// before A, or as it is when in order), then the two halves merged back
// into s; four runs already in order are left. Every value goes into the
// cache and back, so s[a1..b2] is holes between the two halves.
BlkCacheLevel4[T](s: mut []T, it: mut BlkIter, cache: *T, cap: int, const less: fn(&T, &T): bool) {
    it.Begin()
    for !it.Done() {
        a1 := it.dec
        a1e := it.Next()
        b1e := it.Next()
        a2e := it.Next()
        b2e := it.Next()
        if less(&s[b1e - 1], &s[a1]) {
            BlkCacheIn(s, a1, a1e - a1, cache, cap, b1e - a1e)
            BlkCacheIn(s, a1e, b1e - a1e, cache, cap, 0)
        } else if less(&s[a1e], &s[a1e - 1]) {
            BlkMergeToCache(s, a1, a1e, b1e, cache, cap, 0, less)
        } else {
            if !less(&s[a2e], &s[a2e - 1]) && !less(&s[b1e], &s[b1e - 1]) {
                continue
            }
            BlkCacheIn(s, a1, b1e - a1, cache, cap, 0)
        }
        l1 := b1e - a1
        if less(&s[b2e - 1], &s[b1e]) {
            BlkCacheIn(s, b1e, a2e - b1e, cache, cap, l1 + (b2e - a2e))
            BlkCacheIn(s, a2e, b2e - a2e, cache, cap, l1)
        } else if less(&s[a2e], &s[a2e - 1]) {
            BlkMergeToCache(s, b1e, a2e, b2e, cache, cap, l1, less)
        } else {
            BlkCacheIn(s, b1e, b2e - b1e, cache, cap, l1)
        }
        l := b2e - a1
        if BlkCacheLess(cache, cap, l - 1, 0, less) {
            BlkCacheOut(cache, cap, l1, l - l1, s, a1)
            BlkCacheOut(cache, cap, 0, l1, s, a1 + (l - l1))
        } else if BlkCacheLess(cache, cap, l1, l1 - 1, less) {
            BlkMergeFromCache(cache, cap, l1, l, s, a1, less)
        } else {
            BlkCacheOut(cache, cap, 0, l, s, a1)
        }
    }
}

// BlkCacheLess is less of the cache slots i and j, both below cap and
// holding values (the caller vouches the cache and the values).
BlkCacheLess[T](cache: *T, cap: int, i: int, j: int, const less: fn(&T, &T): bool): bool {
    if i < 0 || i >= cap || j < 0 || j >= cap {
        raw.Fail(2, "sort: cache range out of bounds")
    }
    var x := false
    unsafe "0 <= i, j < cap (checked above), slots of the cache that hold values (the caller vouches)" {
        x = less(&*raw.Offset(cache, i), &*raw.Offset(cache, j))
    }
    return x
}

// BlkRun is the shortest base run; a slice below 2 * BlkRun is one insertion
// sort.
const BlkRun = 16

// BlkIter is WikiSort's iterator over one level's runs: n split into den
// runs of n / den elements, the remainder n % den spread by a numerator, so
// run lengths differ by at most one; each level doubles the run length.
struct BlkIter {
    size: int
    den: int
    dec: int
    num: int
    dstep: int
    nstep: int
}

// BlkIterNew starts at runs of BlkRun to 2 * BlkRun elements; n >= 2 *
// BlkRun, so den >= 2.
BlkIterNew(n: int): BlkIter {
    p := Trunc[int](Trunc[u64](1) << (BitLen(n) - 1))
    den := p / BlkRun
    return BlkIter(size: n, den: den, dec: 0, num: 0, dstep: n / den, nstep: n % den)
}

BlkIter.Begin(mut self) {
    self.dec = 0
    self.num = 0
}

BlkIter.Done(&self): bool {
    return self.dec >= self.size
}

// Next moves past the next run, which starts at the old dec, and returns its
// end.
BlkIter.Next(mut self): int {
    self.dec += self.dstep
    self.num += self.nstep
    if self.num >= self.den {
        self.num -= self.den
        self.dec += 1
    }
    return self.dec
}

BlkIter.NextLevel(mut self): bool {
    self.dstep += self.dstep
    self.nstep += self.nstep
    if self.nstep >= self.den {
        self.nstep -= self.den
        self.dstep += 1
    }
    return self.dstep < self.size
}

// BlkSqrt is floor(sqrt(n)) for n >= 0, digit by digit.
BlkSqrt(n: int): int {
    var x := n
    var r := 0
    var b := 4611686018427387904
    for b > x {
        b /= 4
    }
    for b != 0 {
        if x >= r + b {
            x -= r + b
            r = r / 2 + b
        } else {
            r /= 2
        }
        b /= 4
    }
    return r
}

// BlkRotate exchanges s[a..m] and s[m..b] (SymRotate) when both are
// non-empty.
BlkRotate[T](s: mut []T, a: int, m: int, b: int) {
    if a < m && m < b {
        SymRotate(s, a, m, b)
    }
}

// The searches: v is the index of the value searched for, a..b the range,
// and the result lies in a..b whatever less answers. BlkBinaryFirst is the
// first index whose element is not less than s[v], BlkBinaryLast the first
// whose element s[v] is less than (WikiSort's BinaryFirst and BinaryLast).
BlkBinaryFirst[T](s: mut []T, v: int, a: int, b: int, const less: fn(&T, &T): bool): int {
    if a >= b {
        return b
    }
    var cur := a
    var size := b - a
    for size > 0 {
        off := size % 2
        size /= 2
        if less(&s[cur + size], &s[v]) {
            cur += size + off
        }
    }
    return cur
}

BlkBinaryLast[T](s: mut []T, v: int, a: int, b: int, const less: fn(&T, &T): bool): int {
    if a >= b {
        return b
    }
    var cur := a
    var size := b - a
    for size > 0 {
        off := size % 2
        size /= 2
        if !less(&s[v], &s[cur + size]) {
            cur += size + off
        }
    }
    return cur
}

// BlkSkip is the step of the four galloping searches: the range over the
// number of distinct values expected in it, at least 1.
BlkSkip(n: int, unique: int): int {
    x := n / unique
    return if x > 1 { x } else { 1 }
}

// BlkFirstForward, BlkFirstBackward, BlkLastForward and BlkLastBackward are
// WikiSort's FindFirstForward and its kin: a linear search in steps of
// BlkSkip, then a binary search in the last step.
BlkFirstForward[T](s: mut []T, v: int, a: int, b: int, unique: int, const less: fn(&T, &T): bool): int {
    if b - a == 0 {
        return a
    }
    skip := BlkSkip(b - a, unique)
    var index := a + skip
    for less(&s[index - 1], &s[v]) {
        if index >= b - skip {
            return BlkBinaryFirst(s, v, index, b, less)
        }
        index += skip
    }
    return BlkBinaryFirst(s, v, index - skip, index, less)
}

BlkFirstBackward[T](s: mut []T, v: int, a: int, b: int, unique: int, const less: fn(&T, &T): bool): int {
    if b - a == 0 {
        return a
    }
    skip := BlkSkip(b - a, unique)
    var index := b - skip
    for index > a && !less(&s[index - 1], &s[v]) {
        if index < a + skip {
            return BlkBinaryFirst(s, v, a, index, less)
        }
        index -= skip
    }
    return BlkBinaryFirst(s, v, index, index + skip, less)
}

BlkLastForward[T](s: mut []T, v: int, a: int, b: int, unique: int, const less: fn(&T, &T): bool): int {
    if b - a == 0 {
        return a
    }
    skip := BlkSkip(b - a, unique)
    var index := a + skip
    for !less(&s[v], &s[index - 1]) {
        if index >= b - skip {
            return BlkBinaryLast(s, v, index, b, less)
        }
        index += skip
    }
    return BlkBinaryLast(s, v, index - skip, index, less)
}

BlkLastBackward[T](s: mut []T, v: int, a: int, b: int, unique: int, const less: fn(&T, &T): bool): int {
    if b - a == 0 {
        return a
    }
    skip := BlkSkip(b - a, unique)
    var index := b - skip
    for index > a && less(&s[v], &s[index - 1]) {
        if index < a + skip {
            return BlkBinaryLast(s, v, a, index, less)
        }
        index -= skip
    }
    return BlkBinaryLast(s, v, index, index + skip, less)
}

// BlkMergeInternal merges s[as..ae] and s[bs..be] into s[as..be], where A's
// values wait in the internal buffer at buf and A's slots hold the buffer's:
// every move is a swap, so the buffer ends holding its own values in another
// order. Equal values take A's first. The loop makes SwapAt's swaps and
// less's calls, with one range check for the merge.
BlkMergeInternal[T](s: mut []T, as: int, ae: int, bs: int, be: int, buf: int, const less: fn(&T, &T): bool) {
    var ac := 0
    var bc := 0
    var ins := 0
    al := ae - as
    bl := be - bs
    if bl > 0 && al > 0 {
        if as < 0 || ae > bs || be > s.Len() || buf < 0 || buf > s.Len() - al {
            raw.Fail(2, "sort: merge range out of bounds")
        }
        unsafe "al > 0, bl > 0, 0 <= as, ae <= bs, be <= s.Len(), 0 <= buf and buf + al <= s.Len() (checked above). Each pass starts with ac < al and bc < bl (the loop ends when either reaches its length), so buf + ac is in buf..buf + al, bs + bc in bs..be, and as <= as + ins = as + ac + bc < ae + bl <= be: every slot is an initialized slot of s. Each step reads both slots before it writes either, so it exchanges them, and a slot with itself ends as it was, as SwapAt leaves it" {
            p := raw.DataMut(s)
            for {
                x := raw.Offset(p, as + ins)
                pa := raw.Offset(p, buf + ac)
                pb := raw.Offset(p, bs + bc)
                if !less(&*pb, &*pa) {
                    t := raw.Read(x)
                    raw.Write(x, raw.Read(pa))
                    raw.Write(pa, t)
                    ac += 1
                    ins += 1
                    if ac >= al {
                        break
                    }
                } else {
                    t := raw.Read(x)
                    raw.Write(x, raw.Read(pb))
                    raw.Write(pb, t)
                    bc += 1
                    ins += 1
                    if bc >= bl {
                        break
                    }
                }
            }
        }
    }
    SymSwapRange(s, buf + ac, as + ins, al - ac)
}

// BlkMergeInPlace merges s[as..ae] and s[ae..be] by binary search and
// rotation, for a level that found no second buffer, where few distinct
// values bound the rotations (WikiSort's MergeInPlace). The step after each
// rotation moves A's start by at least one, which a strict weak order does
// anyway, so a less that is no order cannot stall it.
BlkMergeInPlace[T](s: mut []T, as0: int, ae0: int, bs0: int, be: int, const less: fn(&T, &T): bool) {
    if ae0 - as0 == 0 || be - bs0 == 0 {
        return
    }
    var as := as0
    var ae := ae0
    var bs := bs0
    for {
        mid := BlkBinaryFirst(s, as, bs, be, less)
        amount := mid - ae
        BlkRotate(s, as, ae, mid)
        if be == mid {
            break
        }
        bs = mid
        as += amount
        ae = bs
        var na := BlkBinaryLast(s, as, as, ae, less)
        if na <= as {
            na = as + 1
        }
        as = na
        if ae - as == 0 {
            break
        }
    }
}

// BlkClamp is i held to lo..hi (lo <= hi).
BlkClamp(i: int, lo: int, hi: int): int {
    if i < lo {
        return lo
    }
    if i > hi {
        return hi
    }
    return i
}

// BlkPull is one internal buffer: count distinct values moved from index
// from to to, to < from to the start of an A run, to > from to the end of a
// B run, inside the merge of a..b.
struct BlkPull {
    from: int
    to: int
    count: int
    a: int
    b: int
}

// BlkLevel merges each pair of runs of one level (WikiSort's main loop),
// with the cache of cap slots at cache (cap 0: none). A level whose runs fit
// the cache merges through it (BlkCacheLevel4 or BlkCacheLevel2). Otherwise
// it finds two internal buffers of about sqrt(run) distinct values (one when
// an A block fits the cache, or a smaller one when the level holds fewer),
// pulls them out to the edges of their runs, merges each pair by tagging A's
// blocks with the first buffer and rolling them through B, merging each
// dropped A block through the cache when it fits (BlkMergeExternal), else
// the second buffer (BlkMergeInternal), else by rotation (BlkMergeInPlace),
// then sorts the second buffer and puts both back.
BlkLevel[T](s: mut []T, it: mut BlkIter, cache: *T, cap: int, const less: fn(&T, &T): bool) {
    size := it.dstep
    if size < cap {
        // Runs of size or size + 1 elements fit the cache.
        if (size + 1) * 4 <= cap && size * 4 <= s.Len() {
            BlkCacheLevel4(s, it, cache, cap, less)
            _ = it.NextLevel()
        } else {
            BlkCacheLevel2(s, it, cache, cap, less)
        }
        return
    }
    var block := BlkSqrt(size)
    var bufsize := size / block + 1
    var pull: [2]BlkPull = [BlkPull(from: 0, to: 0, count: 0, a: 0, b: 0); 2]
    var pi := 0
    var b1s := 0
    var b1e := 0
    var b2s := 0
    var b2e := 0
    var find := bufsize + bufsize
    var separately := false
    // With a cache that holds an A block, one buffer, to tag the blocks,
    // is all a level needs. find > size is never true with BlkRun = 16
    // (2 * (size / r + 1) <= r * r for r = BlkSqrt(size) >= 4), so the two
    // separately arms below never run; they are kept as WikiSort has them.
    if block <= cap {
        find = bufsize
    } else if find > size {
        find = bufsize
        separately = true
    }
    // block before the adjustment below, as WikiSort tests it.
    one_buffer := block <= cap
    it.Begin()
    for !it.Done() {
        as := it.dec
        ae := it.Next()
        bs := ae
        be := it.Next()
        // Distinct values of A, to be pulled to its start.
        var last := as
        var count := 1
        var index := 0
        for count < find {
            index = BlkLastForward(s, last, last + 1, ae, find - count, less)
            if index == ae {
                break
            }
            last = index
            count += 1
        }
        index = last
        if count >= bufsize {
            pull[pi] = BlkPull(from: index, to: as, count: count, a: as, b: be)
            pi = 1
            if count == bufsize + bufsize {
                b1s = as
                b1e = as + bufsize
                b2s = as + bufsize
                b2e = as + count
                break
            } else if find == bufsize + bufsize {
                b1s = as
                b1e = as + count
                find = bufsize
            } else if one_buffer {
                b1s = as
                b1e = as + count
                break
            } else if separately {
                // Unreachable with BlkRun = 16 (above); kept for fidelity.
                b1s = as
                b1e = as + count
                separately = false
            } else {
                b2s = as
                b2e = as + count
                break
            }
        } else if pi == 0 && count > b1e - b1s {
            b1s = as
            b1e = as + count
            pull[pi] = BlkPull(from: index, to: as, count: count, a: as, b: be)
        }
        // Distinct values of B, to be pulled to its end.
        last = be - 1
        count = 1
        for count < find {
            index = BlkFirstBackward(s, last, bs, last, find - count, less)
            if index == bs {
                break
            }
            last = index - 1
            count += 1
        }
        index = last
        if count >= bufsize {
            pull[pi] = BlkPull(from: index, to: be, count: count, a: as, b: be)
            pi = 1
            if count == bufsize + bufsize {
                b1s = be - count
                b1e = be - bufsize
                b2s = be - bufsize
                b2e = be
                break
            } else if find == bufsize + bufsize {
                b1s = be - count
                b1e = be
                find = bufsize
            } else if one_buffer {
                b1s = be - count
                b1e = be
                break
            } else if separately {
                // Unreachable with BlkRun = 16 (above); kept for fidelity.
                b1s = be - count
                b1e = be
                separately = false
            } else {
                // The first buffer came from this A: its values go back
                // before the second's.
                if pull[0].a == as {
                    pull[0].b -= pull[1].count
                }
                b2s = be - count
                b2e = be
                break
            }
        } else if pi == 0 && count > b1e - b1s {
            b1s = be - count
            b1e = be
            pull[pi] = BlkPull(from: index, to: be, count: count, a: as, b: be)
        }
    }
    // Pull the buffers out. The clamps keep room for the values still to
    // come and for those already gathered; under an order they change
    // nothing.
    for p in 0..2 {
        length := pull[p].count
        if pull[p].to < pull[p].from {
            var index := pull[p].from
            for count in 1..length {
                var i := BlkFirstBackward(s, index - 1, pull[p].to, pull[p].from - (count - 1), length - count, less)
                i = BlkClamp(i, pull[p].to + length - 1 - count, pull[p].from - count)
                BlkRotate(s, i + 1, pull[p].from + 1 - count, pull[p].from + 1)
                pull[p].from = i + count
                index = i
            }
        } else if pull[p].to > pull[p].from {
            var index := pull[p].from + 1
            for count in 1..length {
                var i := BlkLastForward(s, index, index, pull[p].to, length - count, less)
                i = BlkClamp(i, index + 1, pull[p].to - (length - 1 - count))
                BlkRotate(s, pull[p].from, pull[p].from + count, i - 1)
                pull[p].from = i - 1 - count
                index = i
            }
        }
    }
    // The first buffer tags every A block: at most size + 1 elements in
    // blocks of size / bufsize + 1.
    bufsize = b1e - b1s
    block = size / bufsize + 1
    it.Begin()
    for !it.Done() {
        var as := it.dec
        ae := it.Next()
        bs := ae
        var be := it.Next()
        // Leave out the parts the buffers hold. A buffer never takes a whole
        // run with BlkRun = 16 (2 * bufsize < run), so skip stays false; the
        // test is kept as WikiSort has it.
        start := as
        var skip := false
        for p in 0..2 {
            if !skip && start == pull[p].a {
                if pull[p].from > pull[p].to {
                    as += pull[p].count
                    skip = ae - as == 0
                } else if pull[p].from < pull[p].to {
                    be -= pull[p].count
                    skip = be - bs == 0
                }
            }
        }
        if skip {
            continue
        }
        if less(&s[be - 1], &s[as]) {
            // B wholly before A.
            BlkRotate(s, as, ae, be)
        } else if less(&s[ae], &s[ae - 1]) {
            var bas := as
            var bae := ae
            fae := as + (bae - bas) % block
            // Tag the first value of each whole A block with the first
            // buffer's.
            var ia := b1s
            var idx := fae
            for idx < bae {
                SwapAt(s, ia, idx)
                ia += 1
                idx += block
            }
            var las := as
            var lae := fae
            var lbs := 0
            var lbe := 0
            var bbs := bs
            first_b := if block < be - bs { block } else { be - bs }
            var bbe := bs + first_b
            bas += fae - as
            ia = b1s
            // The first, uneven A block waits in the cache when it fits,
            // else in the second buffer, for its merge.
            if lae - las <= cap {
                BlkCacheIn(s, las, lae - las, cache, cap, 0)
            } else if b2e - b2s > 0 {
                SymSwapRange(s, las, b2s, lae - las)
            }
            if bae - bas > 0 {
                for {
                    if (lbe - lbs > 0 && !less(&s[lbe - 1], &s[ia])) || bbe - bbs == 0 {
                        // Drop the least A block behind the previous B
                        // block's part that is less than it, and merge the
                        // previous A block with what follows it.
                        split := BlkBinaryFirst(s, ia, lbs, lbe, less)
                        rem := lbe - split
                        var mina := bas
                        var fa := mina + block
                        for fa < bae {
                            if less(&s[fa], &s[mina]) {
                                mina = fa
                            }
                            fa += block
                        }
                        SymSwapRange(s, bas, mina, block)
                        SwapAt(s, bas, ia)
                        ia += 1
                        if lae - las <= cap {
                            BlkMergeExternal(s, las, lae, lae, split, cache, cap, less)
                        } else if b2e - b2s > 0 {
                            BlkMergeInternal(s, las, lae, lae, split, b2s, less)
                        } else {
                            BlkMergeInPlace(s, las, lae, lae, split, less)
                        }
                        if b2e - b2s > 0 || block <= cap {
                            // The A block moves to where its merge reads
                            // it, the cache or the second buffer, so its
                            // slots hold nothing that must keep its order:
                            // the rest of B swaps straight into place, a
                            // rotation's work.
                            if block <= cap {
                                BlkCacheIn(s, bas, block, cache, cap, 0)
                            } else {
                                SymSwapRange(s, bas, b2s, block)
                            }
                            SymSwapRange(s, split, bas + block - rem, rem)
                        } else {
                            BlkRotate(s, split, bas, bas + block)
                        }
                        las = bas - rem
                        lae = bas - rem + block
                        lbs = lae
                        lbe = lae + rem
                        bas += block
                        if bae - bas == 0 {
                            break
                        }
                    } else if bbe - bbs < block {
                        // The last, short B block goes before the A blocks.
                        BlkRotate(s, bas, bbs, bbe)
                        lbs = bas
                        lbe = bas + (bbe - bbs)
                        bas += bbe - bbs
                        bae += bbe - bbs
                        bbe = bbs
                    } else {
                        // Roll the first A block past the next B block.
                        SymSwapRange(s, bas, bbs, block)
                        lbs = bas
                        lbe = bas + block
                        bas += block
                        bae += block
                        bbs += block
                        if bbe > be - block {
                            bbe = be
                        } else {
                            bbe += block
                        }
                    }
                }
            }
            if lae - las <= cap {
                BlkMergeExternal(s, las, lae, lae, be, cache, cap, less)
            } else if b2e - b2s > 0 {
                BlkMergeInternal(s, las, lae, lae, be, b2s, less)
            } else {
                BlkMergeInPlace(s, las, lae, lae, be, less)
            }
        }
    }
    // Sort the second buffer, then put both buffers' values back: a value
    // pulled left goes before its equals, one pulled right after them.
    PdqInsertion(s, b2s, b2e, less)
    for p in 0..2 {
        var unique := pull[p].count * 2
        if pull[p].from > pull[p].to {
            var bs := pull[p].a
            var be := pull[p].a + pull[p].count
            for be - bs > 0 {
                index := BlkFirstForward(s, bs, be, pull[p].b, unique, less)
                amount := index - be
                BlkRotate(s, bs, be, index)
                bs += amount + 1
                be += amount
                unique -= 2
            }
        } else if pull[p].from < pull[p].to {
            var bs := pull[p].b - pull[p].count
            var be := pull[p].b
            for be - bs > 0 {
                index := BlkLastBackward(s, be - 1, pull[p].a, bs, unique, less)
                amount := bs - index
                BlkRotate(s, index, bs, be)
                bs -= amount
                be -= amount + 1
                unique -= 2
            }
        }
    }
}

// SymRotate exchanges the blocks s[a..m] and s[m..b] by block swaps (Go's
// rotate_func).
SymRotate[T](s: mut []T, a: int, m: int, b: int) {
    var i := m - a
    var j := b - m
    for i != j {
        if i > j {
            SymSwapRange(s, m - i, m, j)
            i -= j
        } else {
            SymSwapRange(s, m - i, m + j - i, i)
            j -= i
        }
    }
    SymSwapRange(s, m - i, m, i)
}

// SymSwapRange exchanges s[a + i] and s[b + i] for i from 0 to n - 1, in
// that order: SwapAt's swaps, with one range check for the run.
SymSwapRange[T](s: mut []T, a: int, b: int, n: int) {
    if n <= 0 {
        return
    }
    if a < 0 || b < 0 || a > s.Len() - n || b > s.Len() - n {
        raw.Fail(2, "swap: index out of range")
    }
    unsafe "n > 0, 0 <= a, 0 <= b, a + n <= s.Len() and b + n <= s.Len() (checked above), so slots a + i and b + i are initialized slots of s for every i < n; each step reads both before it writes either, so it exchanges them, and a slot with itself (a == b) ends as it was, as SwapAt leaves it" {
        p := raw.DataMut(s)
        for i in 0..n {
            x := raw.Offset(p, a + i)
            y := raw.Offset(p, b + i)
            t := raw.Read(x)
            raw.Write(x, raw.Read(y))
            raw.Write(y, t)
        }
    }
}
