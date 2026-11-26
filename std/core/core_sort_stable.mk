// Why: stable sorting without an allocator must preserve equal elements' order.
// What: Slice[T].SortStableInPlace and its block merge sort.
// How: small runs are insertion sorted and merged using a fixed stack cache
// and buffers pulled from the runs themselves.

import std.raw

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
