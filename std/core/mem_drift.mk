// Why: an in-place stable sort makes several times the moves and calls of
// less that a merge through scratch memory makes, so SortStable sorts
// through scratch memory from the caller's allocator.
//
// What: DriftStable (SortStable's body), DriftIn, DriftSort and its run,
// merge, quicksort and partition helpers.
//
// How: Rust's driftsort (core::slice::sort::stable, Orson Peters and Lukas
// Bergdoll): runs found in the input (non-descending, or strictly descending
// and reversed) or made of the next chunk, merged in powersort's order
// (Munro and Wild); a chunk that is no run stays unsorted until a merge
// needs it sorted or the merged chunk outgrows the scratch, then a stable
// quicksort sorts it, whose partition of the elements equal to the pivot
// makes k distinct keys cost O(n log k); merges are branch-free, the
// shorter run moved out. The scratch is n / 2 elements or more: n while
// that is at most DriftFullBytes, as Rust's; a slice whose scratch fits in
// 4,096 bytes uses the stack, as Rust's does. Larger scratch comes from the
// allocator passed to SortStable, falling back to n / 2 elements if the
// larger block is refused, and returning false (OutOfMemory) before any
// element moves if even n / 2 elements cannot be allocated.
//
// Every move is a slot helper's (mem_slots.mk), from a slot that holds a
// value into one that holds none, so each element has exactly one live home
// at every call of less, in s or in the scratch, and each function checks
// its ranges against s and the scratch once before it moves anything. Each
// merge and partition moves every value it took out back into s before it
// returns, so the scratch holds no value when the sort ends, and a less that
// is no order still leaves a permutation: every loop is bounded by lengths,
// whatever less answers. Mk runs no destructor and a panic ends the process,
// so a panicking less drops or doubles nothing.

import std.raw

// DriftInsertMax is the longest slice sorted by insertion alone, with no
// scratch (Rust's MAX_LEN_ALWAYS_INSERTION_SORT).
const DriftInsertMax = 20

// DriftSmall is the longest range the quicksort leaves to DriftSmallSort,
// and an eager run's length (Rust's SMALL_SORT_GENERAL_THRESHOLD).
const DriftSmall = 32

// DriftSqrtRun is driftsort's MIN_SQRT_RUN_LEN: up to DriftSqrtRun^2
// elements a found run must be min(n / 2, DriftSqrtRun) long to be
// used, above it sqrt(n).
const DriftSqrtRun = 64

// DriftFullBytes is the most scratch, in bytes, taken to hold a whole slice
// (Rust's MAX_FULL_ALLOC_BYTES); a longer slice takes n - n / 2 elements.
const DriftFullBytes = 8000000

// DriftStackBytes is the stack scratch: 4,096 bytes, as Rust's.
const DriftStackBytes = 4096

// DriftScratchLen is the scratch, in elements of T, for n elements, n >= 2:
// max(n - n / 2, min(n, DriftFullBytes / SizeOf[T]()), DriftSmallScratch),
// as Rust's.
DriftScratchLen[T](n: int): int {
    full := DriftFullBytes / SizeOfT[T]()
    m := if n < full { n } else { full }
    h := n - n / 2
    c := if h > m { h } else { m }
    return if c > DriftSmallScratch { c } else { DriftSmallScratch }
}

// DriftStackCap is how many elements of T the stack scratch holds, or 0
// when T cannot live in a [512]u64 (alignment above 8, a size above 4,096
// bytes).
DriftStackCap[T](): int {
    if AlignOfT[T]() > 8 || SizeOfT[T]() > DriftStackBytes {
        return 0
    }
    return DriftStackBytes / SizeOfT[T]()
}

// DriftStable sorts s stably (SortStable's body): by insertion up to
// DriftInsertMax, in place for a zero-size T, by DriftOnStack when the
// scratch fits the stack scratch, else by DriftIn through a (requesting
// DriftScratchLen slots, falling back to n / 2 slots if a refuses the
// larger block). Returns false, with s unchanged, when a cannot give even
// n / 2 slots. The stack scratch is in DriftOnStack's frame, so a short
// slice pays for none.
DriftStable[T](s: mut []T, a: &Alloc, const less: fn(&T, &T): bool): bool {
    n := s.Len()
    if n <= DriftInsertMax || SizeOfT[T]() == 0 {
        StableSort(s, less)
        return true
    }
    cap := DriftScratchLen[T](n)
    if cap <= DriftStackCap[T]() {
        DriftOnStack(s, cap, less)
        return true
    }
    if DriftIn(s, a, cap, less) {
        return true
    }
    half := n / 2
    if cap > half && DriftIn(s, a, half, less) {
        return true
    }
    return false
}

// DriftOnStack sorts s by DriftSort through cap <= DriftStackCap slots of
// a local, which Mk zeroes (it has no unset locals): 512 bytes when the
// scratch fits in them (DriftOnStack64), else 4,096, so only the zeroing
// follows the size. Both inline into DriftStable, whose frame then holds
// 4,096 bytes for every slice past DriftInsertMax (StableSort's cache
// takes as much); SortStable keeps shorter slices out of it.
DriftOnStack[T](s: mut []T, cap: int, const less: fn(&T, &T): bool) {
    if cap * 8 <= DriftStackCap[T]() {
        DriftOnStack64(s, cap, less)
        return
    }
    var words: [512]u64 = [0; 512]
    unsafe "words is 4,096 bytes aligned to 8, and DriftStackCap is at least cap only for a T of 1 to 4,096 bytes aligned to at most 8: cap slots of T, each aligned, lie in words, and hold no value until a move puts one there" {
        DriftSort(s, raw.PtrCast[[512]u64, T](raw.OfMut(mut words)), cap, s.Len() <= 2 * DriftSmall, less)
    }
}

DriftOnStack64[T](s: mut []T, cap: int, const less: fn(&T, &T): bool) {
    var small: [64]u64 = [0; 64]
    unsafe "small is 512 bytes aligned to 8, and DriftStackCap / 8 is at least cap only for a T of 1 to 512 bytes aligned to at most 8: cap slots of T, each aligned, lie in small, and hold no value until a move puts one there" {
        DriftSort(s, raw.PtrCast[[64]u64, T](raw.OfMut(mut small)), cap, s.Len() <= 2 * DriftSmall, less)
    }
}

// DriftIn sorts s, longer than DriftInsertMax, by DriftSort through cap
// slots of scratch from a (cap >= n / 2), freed before it returns; false,
// with s unchanged, when a cannot give them.
DriftIn[T](s: mut []T, a: &Alloc, cap: int, const less: fn(&T, &T): bool): bool {
    p := GrowSlots(a, Dangling[T](), 0, 0, cap, false)
    if raw.IsNull(p) {
        return false
    }
    DriftSort(s, p, cap, s.Len() <= 2 * DriftSmall, less)
    FreeSlots(a, p, cap)
    return true
}

// A run is its length times two, plus 1 when it is sorted.
DriftRunLen(r: int): int {
    return r / 2
}

DriftSorted(r: int): bool {
    return r % 2 == 1
}

DriftRun(n: int, sorted: bool): int {
    if sorted {
        return n * 2 + 1
    }
    return n * 2
}

// DriftDepth is powersort's node power of the merge of the runs [l..m] and
// [m..r] (driftsort's merge_tree_depth): the leading zeros of the xor of the
// two midpoints scaled by scale = ceil(2^62 / n), at most 64. l + m and
// m + r are at most 2n, so neither product passes 2^63 + 2n.
DriftDepth(l: int, m: int, r: int, scale: u64): int {
    x := scale * Trunc[u64](l + m)
    y := scale * Trunc[u64](m + r)
    return raw.Clz64(x ^ y)
}

// DriftSort sorts s stably through buf, cap >= s.Len() / 2 free slots
// (driftsort's main loop). That is enough: a chunk left unsorted is at most
// good <= n / 2 long, and two stay unsorted together only while they fit
// cap, so each quicksort's range fits; a merge moves out its shorter run,
// at most n / 2; and a small sort's halves are at most 16, within cap for
// any n of 32 or more; eager makes every chunk that is no run a
// sorted one of DriftSmall at once, as a short slice and the quicksort's
// fallback do. The stack of runs holds at most 66, since each depth on it
// is below the next and every depth is at most 64.
DriftSort[T](s: mut []T, buf: *T, cap: int, eager: bool, const less: fn(&T, &T): bool) {
    n := s.Len()
    if n < 2 {
        return
    }
    if cap < n / 2 {
        raw.Fail(2, "sort: scratch too short")
    }
    un := Trunc[u64](n)
    scale := ((Trunc[u64](1) << 62) + un - 1) / un
    good := if n <= DriftSqrtRun * DriftSqrtRun { if n / 2 < DriftSqrtRun { n / 2 } else { DriftSqrtRun } } else { BlkSqrt(n) }
    // Each entry is a run (DriftRun) times 128 plus its depth, at most 64.
    var stack: [66]int = [0; 66]
    var top := 0
    var scan := 0
    var prev := DriftRun(0, true)
    for {
        var next := DriftRun(0, true)
        var depth := 0
        if scan < n {
            next = DriftCreateRun(s, scan, buf, cap, good, eager, less)
            depth = DriftDepth(scan - DriftRunLen(prev), scan, scan + DriftRunLen(next), scale)
        }
        for top > 1 && stack[top - 1] % 128 >= depth {
            left := stack[top - 1] / 128
            len := DriftRunLen(left) + DriftRunLen(prev)
            prev = DriftLogicalMerge(s, scan - len, scan, buf, cap, left, prev, less)
            top -= 1
        }
        stack[top] = prev * 128 + depth
        top += 1
        if scan >= n {
            break
        }
        scan += DriftRunLen(next)
        prev = next
    }
    if !DriftSorted(prev) {
        DriftQuick(s, 0, n, buf, cap, DriftLimit(n), -1, less)
    }
}

// DriftLimit is the quicksort's budget of bad pivots for n elements before
// it falls back to an eager DriftSort: 2 * floor(log2(n | 1)).
DriftLimit(n: int): int {
    return 2 * (BitLen(n | 1) - 1)
}

// DriftCreateRun is the run at s[at..]: a found run of at least good
// elements, reversed when strictly descending; else, eager, the next
// DriftSmall (or fewer) sorted; else the next good (or fewer), unsorted.
DriftCreateRun[T](s: mut []T, at: int, buf: *T, cap: int, good: int, eager: bool, const less: fn(&T, &T): bool): int {
    len := s.Len() - at
    if len >= good && len >= 2 {
        var k := 2
        if less(&s[at + 1], &s[at]) {
            for k < len && less(&s[at + k], &s[at + k - 1]) {
                k += 1
            }
            if k >= good {
                PdqReverse(s, at, at + k)
                return DriftRun(k, true)
            }
        } else {
            for k < len && !less(&s[at + k], &s[at + k - 1]) {
                k += 1
            }
            if k >= good {
                return DriftRun(k, true)
            }
        }
    }
    if eager {
        k := if len < DriftSmall { len } else { DriftSmall }
        DriftSmallSort(s, at, at + k, buf, cap, less)
        return DriftRun(k, true)
    }
    return DriftRun(if len < good { len } else { good }, false)
}

// DriftLogicalMerge merges the runs left and right that lie in s[a..b]:
// two unsorted ones that fit the scratch together make one unsorted run;
// otherwise the unsorted one (or both) is quicksorted and the two merged.
DriftLogicalMerge[T](s: mut []T, a: int, b: int, buf: *T, cap: int, left: int, right: int, const less: fn(&T, &T): bool): int {
    len := b - a
    if len > cap || DriftSorted(left) || DriftSorted(right) {
        m := a + DriftRunLen(left)
        if !DriftSorted(left) {
            DriftQuick(s, a, m, buf, cap, DriftLimit(m - a), -1, less)
        }
        if !DriftSorted(right) {
            DriftQuick(s, m, b, buf, cap, DriftLimit(b - m), -1, less)
        }
        DriftMerge(s, a, m, b, buf, cap, less)
        return DriftRun(len, true)
    }
    return DriftRun(len, false)
}

// DriftQuick sorts s[a..b], at most cap elements, by driftsort's stable
// quicksort: a range of DriftSmall or fewer by DriftSmallSort; each other a
// partition stable on both sides around a pivot (DriftPivot), the right side
// by recursion and the left by the loop. anc is the index of the ancestor
// pivot, -1 for none: an element of the range that no element of it is less
// than, the last pivot whose right side the range is. A pivot that is not
// greater than it, or that leaves nothing less than it, is a repeat of the
// range's least value, so the range splits into its equals, which are done,
// and the rest. After limit bad pivots an eager DriftSort sorts the range.
DriftQuick[T](s: mut []T, a0: int, b0: int, buf: *T, cap: int, limit0: int, anc0: int, const less: fn(&T, &T): bool) {
    var a := a0
    var b := b0
    var limit := limit0
    var anc := anc0
    for {
        len := b - a
        if len <= DriftSmall {
            DriftSmallSort(s, a, b, buf, cap, less)
            return
        }
        if limit == 0 {
            DriftSort(mut s[a..b], buf, cap, true, less)
            return
        }
        limit -= 1
        pivot := DriftPivot(s, a, b, less)
        var eq := anc >= 0 && !less(&s[anc], &s[pivot])
        var nl := 0
        var at := anc
        var pat := 0
        if !eq {
            nl = DriftPartition(s, a, b, pivot, false, buf, cap, mut at, mut pat, less)
            eq = nl == 0
        }
        if eq {
            a += DriftPartition(s, a, b, pivot, true, buf, cap, mut at, mut pat, less)
            anc = -1
            continue
        }
        DriftQuick(s, a + nl, b, buf, cap, limit, pat, less)
        b = a + nl
        // The ancestor went left under an order; under none, the range has
        // no ancestor.
        anc = if at >= a && at < b { at } else { -1 }
    }
}

// DriftSmallSort sorts s[a..b], at most DriftSmall elements, stably through
// buf (Rust's small_sort_general): below 8 by insertion; else each half's
// first 8 (from 16 elements) or 4 sorted into buf, by DriftSort8 or
// DriftSort4, the rest of each half moved after them one by one and
// inserted, and the halves merged back into s by DriftBidi. That takes
// len + 16 slots of buf, which SortStable's DriftScratchLen always has (at
// least DriftSmallScratch) and its half-slice fallback has from 96 elements
// (cap = n / 2 >= 48). With fewer, DriftSmallNarrow sorts the range. Every value
// stays in one slot of s or buf at every call of less.
DriftSmallSort[T](s: mut []T, a: int, b: int, buf: *T, cap: int, const less: fn(&T, &T): bool) {
    len := b - a
    if len < 8 {
        PdqInsertion(s, a, b, less)
        return
    }
    if a < 0 || b > s.Len() || len > DriftSmall || cap < 8 {
        raw.Fail(2, "sort: small sort range out of bounds")
    }
    if cap < len + 16 {
        DriftSmallNarrow(s, a, b, buf, cap, less)
        return
    }
    v := SlotAt(raw.DataMut(s), a) // a..b in s (checked above)
    h := len / 2
    var k := 4
    if len >= 16 {
        k = 8
        DriftSort8(v, buf, SlotAt(buf, len), less)
        DriftSort8(SlotAt(v, h), SlotAt(buf, h), SlotAt(buf, len + 8), less)
    } else {
        DriftSort4(v, buf, less)
        DriftSort4(SlotAt(v, h), SlotAt(buf, h), less)
    }
    DriftExtend(v, buf, k, h, less)
    DriftExtend(SlotAt(v, h), SlotAt(buf, h), k, len - h, less)
    // s[a..b] are all holes; buf[0..len] holds the two sorted halves.
    if !DriftBidi(buf, len, v, less) {
        DriftMergeUp(buf, h, SlotAt(buf, h), len - h, v, less)
    }
}

// DriftSmallScratch is the least scratch DriftSmallSort's fast path needs
// for a range of DriftSmall (Rust's SMALL_SORT_GENERAL_SCRATCH_LEN).
const DriftSmallScratch = 48

// DriftExtend moves src[k..n] one by one to dst[k..n], dst[0..k] sorted,
// inserting each into the sorted prefix (Rust's insert_tail). The caller
// vouches src[k..n] holds values and dst[0..k] values and dst[k..n] none.
DriftExtend[T](src: *T, dst: *T, k: int, n: int, const less: fn(&T, &T): bool) {
    for i in k..n {
        SlotCopy(SlotAt(dst, i), SlotAt(src, i), 1)
        PdqInsertFrom(dst, i, i + 1, less)
    }
}

// DriftSmallNarrow is DriftSmallSort with fewer than len + 16 slots of
// scratch (SortStable's half-slice fallback on a slice under 96 elements):
// each half sorted in place, its first 8 or 4 through buf, then the left
// half moved out and DriftMerge's merge of the two.
DriftSmallNarrow[T](s: mut []T, a: int, b: int, buf: *T, cap: int, const less: fn(&T, &T): bool) {
    len := b - a
    h := len / 2
    v := SlotAt(raw.DataMut(s), a) // a..b in s (DriftSmallSort checked it)
    var k := 4
    if len >= 16 {
        k = 8
        DriftSort8(v, v, buf, less)
        DriftSort8(SlotAt(v, h), SlotAt(v, h), buf, less)
    } else {
        DriftSort4(v, buf, less)
        SlotCopy(v, buf, 4)
        DriftSort4(SlotAt(v, h), buf, less)
        SlotCopy(SlotAt(v, h), buf, 4)
    }
    PdqInsertFrom(v, k, h, less)
    PdqInsertFrom(SlotAt(v, h), k, len - h, less)
    DriftMerge(s, a, a + h, b, buf, cap, less)
}

// DriftSort4 moves the 4 values at v, sorted stably, to the 4 free slots at
// dst (Rust's sort4_stable): five calls of less, every one before any move,
// pick each value's slot with no branch. The caller vouches v holds 4
// values and dst 4 free slots of another array.
DriftSort4[T](v: *T, dst: *T, const less: fn(&T, &T): bool) {
    c1 := less(SlotRef(v, 1), SlotRef(v, 0))
    c2 := less(SlotRef(v, 3), SlotRef(v, 2))
    ia := if c1 { 1 } else { 0 }
    ib := if c1 { 0 } else { 1 }
    ic := if c2 { 3 } else { 2 }
    id := if c2 { 2 } else { 3 }
    c3 := less(SlotRef(v, ic), SlotRef(v, ia))
    c4 := less(SlotRef(v, id), SlotRef(v, ib))
    imin := if c3 { ic } else { ia }
    imax := if c4 { ib } else { id }
    icb := if c4 { ic } else { ib }
    ibc := if c3 { ib } else { ic }
    ul := if c3 { ia } else { icb }
    ur := if c4 { id } else { ibc }
    c5 := less(SlotRef(v, ur), SlotRef(v, ul))
    ilo := if c5 { ur } else { ul }
    ihi := if c5 { ul } else { ur }
    SlotCopy(SlotAt(dst, 0), SlotAt(v, imin), 1)
    SlotCopy(SlotAt(dst, 1), SlotAt(v, ilo), 1)
    SlotCopy(SlotAt(dst, 2), SlotAt(v, ihi), 1)
    SlotCopy(SlotAt(dst, 3), SlotAt(v, imax), 1)
}

// DriftSort8 moves the 8 values at v, sorted stably, to the 8 slots at
// dst, through the 8 free slots at tmp: each 4 by DriftSort4 into tmp,
// then merged into dst by DriftBidi (or, under a less that is no order,
// DriftMergeUp). dst may be v itself, whose slots are all holes once the
// values are in tmp; tmp is another array.
DriftSort8[T](v: *T, dst: *T, tmp: *T, const less: fn(&T, &T): bool) {
    DriftSort4(v, tmp, less)
    DriftSort4(SlotAt(v, 4), SlotAt(tmp, 4), less)
    if !DriftBidi(tmp, 8, dst, less) {
        DriftMergeUp(tmp, 4, SlotAt(tmp, 4), 4, dst, less)
    }
}

// DriftBidi merges the sorted runs src[0..n / 2] and src[n / 2..n],
// n <= DriftSmall, into the n free slots at dst, another array, equal
// values the first run's first (Rust's bidirectional_merge): n / 2 steps
// each take the lesser of the two runs' first values for the front of dst
// and the greater of their last for the back, a branch-free choice; an odd
// n takes the one value left for the middle. No value moves until every
// call of less is made, so each call reads values in their own slots; then,
// when the two ends met (always, under an order), every value moves to its
// place and it returns true. Under a less that is no order the ends may
// miss each other: it moves nothing and returns false. Every index read is
// in 0..n: each end moves at most n / 2 times from its run's edge.
DriftBidi[T](src: *T, n: int, dst: *T, const less: fn(&T, &T): bool): bool {
    if n > DriftSmall || n < 2 {
        raw.Fail(2, "sort: bidirectional merge out of bounds")
    }
    h := n / 2
    var slots: [32]int = [0; 32]
    var at := raw.Null[int]()
    unsafe "a pointer to the 32 ints of slots, a local that outlives every use; each index written is in 0..n, n <= 32" {
        at = raw.PtrCast[[32]int, int](raw.OfMut(mut slots))
    }
    var l := 0
    var r := h
    var lb := h -% 1
    var rb := n -% 1
    var df := 0
    var db := n -% 1
    for _ in 0..h {
        tl := !less(SlotRef(src, r), SlotRef(src, l))
        SlotWrite(at, df, if tl { l } else { r })
        gl := if tl { 1 } else { 0 }
        l = l +% gl
        r = r +% (1 -% gl)
        df = df +% 1
        tb := less(SlotRef(src, rb), SlotRef(src, lb))
        SlotWrite(at, db, if tb { lb } else { rb })
        gb := if tb { 1 } else { 0 }
        lb = lb -% gb
        rb = rb -% (1 -% gb)
        db = db -% 1
    }
    if n % 2 != 0 {
        left := l <= lb
        SlotWrite(at, df, if left { l } else { r })
        g := if left { 1 } else { 0 }
        l = l +% g
        r = r +% (1 -% g)
    }
    if l != lb +% 1 || r != rb +% 1 {
        return false
    }
    for d in 0..n {
        SlotCopy(SlotAt(dst, d), SlotAt(src, SlotRead(at, d)), 1)
    }
    return true
}

// DriftMergeUp merges the sorted runs x[0..nx] and y[0..ny] into the
// nx + ny free slots at dst, another array, forward, equal values x's
// first, with no branch on the comparison: DriftBidi's merge when its ends
// miss under a less that is no order.
DriftMergeUp[T](x: *T, nx: int, y: *T, ny: int, dst: *T, const less: fn(&T, &T): bool) {
    var l := 0
    var r := 0
    var d := 0
    for l < nx && r < ny {
        tl := !less(SlotRef(y, r), SlotRef(x, l))
        src := if tl { SlotAt(x, l) } else { SlotAt(y, r) }
        SlotCopy(SlotAt(dst, d), src, 1)
        gl := if tl { 1 } else { 0 }
        l = l +% gl
        r = r +% (1 -% gl)
        d = d +% 1
    }
    DriftMove(SlotAt(dst, d), SlotAt(x, l), nx - l)
    DriftMove(SlotAt(dst, d +% nx -% l), SlotAt(y, r), ny - r)
}

// DriftPivot is the quicksort's pivot in s[a..b], b - a > DriftSmall:
// median3 of samples at 0, 4/8 and 7/8 below PdqMedRecLen, PdqMed3Rec's
// pseudo-median above it.
DriftPivot[T](s: mut []T, a: int, b: int, const less: fn(&T, &T): bool): int {
    len := b - a
    if len < PdqMedRecLen {
        n8 := len / 8
        return PdqMed3(s, a, a + n8 * 4, a + n8 * 7, less)
    }
    return PdqMedSpread(s, a, len, less)
}

// DriftPartition partitions s[a..b] around s[pivot] stably through the
// scratch (driftsort's stable_partition), and returns how many went left:
// those less than the pivot, or with left those the pivot is not less than.
// Each element is compared with the pivot in its own slot, then moved to the
// next free slot of buf from the front (left) or from the back (right), so
// both sides keep their order; the pivot's own turn reserves its slot, and
// it moves there last, from the slot every comparison read; then the left
// side moves back in order and the right side reversed. Branch-free: the
// comparison picks the destination. at, an index of s, becomes the index
// its element moves to (unchanged when it is outside a..b), and pat the
// index the pivot moves to.
DriftPartition[T](s: mut []T, a: int, b: int, pivot: int, const left: bool, buf: *T, cap: int, at: mut int, pat: mut int, const less: fn(&T, &T): bool): int {
    len := b - a
    if a < 0 || b > s.Len() || len > cap || pivot < a || pivot >= b {
        raw.Fail(2, "sort: partition range out of bounds")
    }
    p := raw.DataMut(s)
    pv := SlotRef(p, pivot) // a <= pivot < b: a value until it moves last
    t := *at
    // Each step takes one slot of buf: before the step of i, nl + (len - r)
    // = i - a < len slots are taken, [0..nl] from the front and
    // [r + nl..len] from the back, so after r's decrement both destinations,
    // nl and r + nl, are free and in 0..len. The pivot's step and t's are
    // taken apart from the spans between them, so the spans' loops test
    // neither.
    var nl := 0
    var r := len
    var k1 := pivot
    var k2 := b
    if t >= a && t < b && t != pivot {
        if t < pivot {
            k1 = t
            k2 = pivot
        } else {
            k2 = t
        }
    }
    var pslot := 0
    var tslot := -1
    DriftSpan(p, a, k1, pv, left, buf, mut nl, mut r, less)
    for k in [k1, k2] {
        if k == b {
            break
        }
        if k == pivot {
            r -= 1
            pslot = if left { nl } else { r + nl }
            nl += if left { 1 } else { 0 }
        } else {
            tslot = DriftStep(p, k, pv, left, buf, mut nl, mut r, less)
        }
        e := if k == k1 { k2 } else { b }
        DriftSpan(p, k + 1, e, pv, left, buf, mut nl, mut r, less)
    }
    SlotCopy(SlotAt(buf, pslot), SlotAt(p, pivot), 1)          // the pivot to its reserved slot
    SlotCopy(SlotAt(p, a), buf, nl)                            // s[a..b] are all holes now
    DriftCopyRev(SlotAt(p, a + nl), SlotAt(buf, nl), len - nl) // the right side, reversed
    // buf[d] went to s[a + d] for d < nl, else to s[b - 1 - d + nl].
    if t == pivot {
        tslot = pslot
    }
    if tslot >= 0 {
        *at = if tslot < nl { a + tslot } else { b - 1 - tslot + nl }
    }
    *pat = if pslot < nl { a + pslot } else { b - 1 - pslot + nl }
    return nl
}

// DriftStep is DriftPartition's step for s[i], i not the pivot: r drops by
// one, s[i] moves to buf[nl] when it goes left, else to buf[r + nl], and nl
// counts it when it goes left. It returns the slot it moved to. The caller
// vouches i holds a value and both slots are free and in buf.
DriftStep[T](p: *T, i: int, pv: &T, const left: bool, buf: *T, nl: mut int, r: mut int, const less: fn(&T, &T): bool): int {
    *r -= 1
    x := if left { !less(pv, SlotRef(p, i)) } else { less(SlotRef(p, i), pv) }
    o := if x { 0 } else { *r }
    d := o + *nl
    SlotCopy(SlotAt(buf, d), SlotAt(p, i), 1)
    *nl += if x { 1 } else { 0 }
    return d
}

// DriftSpan is DriftStep for each i of i0..i1, none of them the pivot. The
// counts stay in 0..len (DriftPartition's invariant), so their arithmetic
// wraps, which it never does, with no overflow check.
DriftSpan[T](p: *T, i0: int, i1: int, pv: &T, const left: bool, buf: *T, nl0: mut int, r0: mut int, const less: fn(&T, &T): bool) {
    var nl := *nl0
    var r := *r0
    var i := i0
    // Four steps a pass, then one, as Rust's loop: the destination of each
    // is buf[nl] or buf[r + nl], the comparison choosing the base.
    for i +% 4 <= i1 {
        DriftSpanStep(p, i, pv, left, buf, mut nl, mut r, less)
        DriftSpanStep(p, i +% 1, pv, left, buf, mut nl, mut r, less)
        DriftSpanStep(p, i +% 2, pv, left, buf, mut nl, mut r, less)
        DriftSpanStep(p, i +% 3, pv, left, buf, mut nl, mut r, less)
        i = i +% 4
    }
    for i < i1 {
        DriftSpanStep(p, i, pv, left, buf, mut nl, mut r, less)
        i = i +% 1
    }
    *nl0 = nl
    *r0 = r
}

// DriftSpanStep is DriftStep with wrapping arithmetic and no result.
DriftSpanStep[T](p: *T, i: int, pv: &T, const left: bool, buf: *T, nl: mut int, r: mut int, const less: fn(&T, &T): bool) {
    *r = *r -% 1
    x := if left { !less(pv, SlotRef(p, i)) } else { less(SlotRef(p, i), pv) }
    base := if x { buf } else { SlotAt(buf, *r) }
    SlotCopy(SlotAt(base, *nl), SlotAt(p, i), 1)
    g := if x { 1 } else { 0 }
    *nl = *nl +% g
}

// DriftMove moves the n values at src to the n slots at dst, which hold
// none and do not overlap them: by one move each up to DriftMoveLoop, which
// for the short tails of the small sort's merges costs less than a call of
// memcpy, else by one copy. The index stays in 0..n, so it wraps, which it
// never does, with no overflow check.
DriftMove[T](dst: *T, src: *T, n: int) {
    if n > DriftMoveLoop {
        SlotCopy(dst, src, n)
        return
    }
    var i := 0
    for i < n {
        SlotCopy(SlotAt(dst, i), SlotAt(src, i), 1)
        i = i +% 1
    }
}

// DriftMoveLoop is the longest move DriftMove makes one element at a time.
const DriftMoveLoop = 16

// DriftCopyRev moves the n values at src, last first, to the n slots at
// dst, which hold none and do not overlap them: dst[j] takes src[n - 1 - j].
// j and n - 1 - j stay in 0..n, so the index arithmetic wraps, which it
// never does, with no overflow check.
DriftCopyRev[T](dst: *T, src: *T, n: int) {
    var j := 0
    var k := n -% 1
    for j < n {
        SlotCopy(SlotAt(dst, j), SlotAt(src, k), 1)
        j = j +% 1
        k = k -% 1
    }
}

// DriftMerge merges the sorted runs s[a..m] and s[m..b] stably through buf
// (driftsort's merge): the shorter run moves out, then a branch-free merge
// fills the holes, from the front (the left run out: merge_up) or from the
// back (merge_down), equal values the left run's first; what is left of the
// moved run moves back last. min(m - a, b - m) <= cap.
DriftMerge[T](s: mut []T, a: int, m: int, b: int, buf: *T, cap: int, const less: fn(&T, &T): bool) {
    ll := m - a
    rl := b - m
    if ll <= 0 || rl <= 0 {
        return
    }
    short := if ll < rl { ll } else { rl }
    if a < 0 || b > s.Len() || short > cap {
        raw.Fail(2, "sort: merge range out of bounds")
    }
    p := raw.DataMut(s)
    if ll <= rl {
        DriftMove(buf, SlotAt(p, a), ll) // s[a..m] are holes
        var l := 0
        var r := m
        var d := a
        // The holes are s[d..r] (d = a + l + (r - m) < r while l < ll); buf
        // holds the left run's l..ll.
        for l < ll && r < b {
            x := !less(SlotRef(p, r), SlotRef(buf, l))
            src := if x { SlotAt(buf, l) } else { SlotAt(p, r) }
            SlotCopy(SlotAt(p, d), src, 1)
            l += if x { 1 } else { 0 }
            r += if x { 0 } else { 1 }
            d += 1
        }
        DriftMove(SlotAt(p, d), SlotAt(buf, l), ll - l) // the last ll - l holes
    } else {
        DriftMove(buf, SlotAt(p, m), rl) // s[m..b] are holes
        var l := m
        var r := rl
        var d := b
        // The holes are s[l..d] (d = l + r); buf holds the right run's 0..r;
        // each pass starts with l > a and r > 0.
        for {
            d -= 1
            x := less(SlotRef(buf, r - 1), SlotRef(p, l - 1))
            src := if x { SlotAt(p, l - 1) } else { SlotAt(buf, r - 1) }
            SlotCopy(SlotAt(p, d), src, 1)
            l -= if x { 1 } else { 0 }
            r -= if x { 0 } else { 1 }
            if l == a || r == 0 {
                break
            }
        }
        DriftMove(SlotAt(p, l), buf, r) // the last r holes, s[l..l + r]
    }
}
