// Why: schedulers, Dijkstra and merge steps need the least element fast
// while elements keep arriving.
//
// What: PriorityQueue, a min-heap by Ord.Less in an arena, with New, Push,
// Pop, Peek and Len. Equal elements come out in no stated order.
//
// How: an implicit four-ary heap in one array (Johnson 1975, d-heaps;
// LaMarca and Ladner 1996: four children per node fit a cache line and
// halve the depth). Push sifts up with a hole, writing the new value once
// (Rust's BinaryHeap Hole): at most log4 n compares and moves. Pop is
// bottom-up (Wegener 1993; Rust's sift_down_to_bottom): the hole runs from
// the root to a leaf along the least child, 3 compares per level as a
// branch-free tournament, then the last value sifts up from there, which
// usually stops at once since the last value of a heap is large; at most
// 3 log4 n + 3 compares on the way down plus the sift-up. Growth as List:
// max(4, 2 cap, need), in place when the arena can, else a new buffer.

import std.raw

/// A priority queue in an arena: Pop and Peek give the least element by
/// T's Ord. Push and Pop are O(log n), Peek O(1). Equal elements come out
/// in no stated order.
export view struct PriorityQueue[T: Ord] {
    ptr: *T
    n: int
    cap: int
    alloc: &Alloc
}

/// An empty queue in a. O(1); allocates nothing until the first Push.
export PriorityQueue[T].New(a: &Arena): PriorityQueue[T] {
    return PriorityQueue[T](ptr: Dangling[T](), n: 0, cap: 0, alloc: a.Alloc())
}

/// Adds x. Fails with OutOfMemory when the queue is full and the arena
/// cannot grow it; the queue grows before it takes x, so x is consumed
/// only on success. O(log n) compares, amortized; a growth allocates from
/// the arena and leaves the old array to it.
export PriorityQueue[T].Push(mut self, x: T into self): OutOfMemory!void {
    if self.n == self.cap {
        c := NextCap(self.cap, self.n + 1)
        q := GrowSlots(self.alloc, self.ptr, self.n, self.cap, c, false)
        if raw.IsNull(q) {
            fail OutOfMemory
        }
        self.ptr = q
        self.cap = c
    }
    p := self.ptr
    var i := self.n
    for i > 0 {
        up := (i - 1) >> 2
        if !x.Less(SlotRef(p, up)) {
            break
        }
        SlotWrite(p, i, SlotRead(p, up))
        i = up
    }
    SlotWrite(p, i, x)
    self.n += 1
}

/// The least element, removed; none when empty. O(log n) compares and
/// moves; allocates nothing.
export PriorityQueue[T].Pop(mut self): ?T {
    if self.n == 0 {
        return none
    }
    self.n -= 1
    n := self.n
    p := self.ptr
    top := SlotRead(p, 0)
    if n == 0 {
        return some(top)
    }
    last := SlotRead(p, n)
    // The hole at i has children 4i+1 to 4i+4; the first is in the heap
    // exactly when 4i + 1 < n, that is i < (n + 2) / 4 (no overflow).
    var i := 0
    leaves := (n + 2) >> 2
    for i < leaves {
        // i < leaves gives 4i + 1 < n: the wrapping ops cannot wrap.
        c := 4 *% i +% 1
        var m := c
        if c < n - 3 {
            // All four children: a tournament of three compares, each
            // choosing an index, so release code selects with cmov and
            // random values take no mispredicted branch.
            var b := c + 2
            if SlotRef(p, c + 1).Less(SlotRef(p, c)) {
                m = c + 1
            }
            if SlotRef(p, c + 3).Less(SlotRef(p, c + 2)) {
                b = c + 3
            }
            if SlotRef(p, b).Less(SlotRef(p, m)) {
                m = b
            }
        } else {
            var j := c + 1
            for j < n {
                if SlotRef(p, j).Less(SlotRef(p, m)) {
                    m = j
                }
                j += 1
            }
        }
        SlotWrite(p, i, SlotRead(p, m))
        i = m
    }
    for i > 0 {
        up := (i - 1) >> 2
        if !last.Less(SlotRef(p, up)) {
            break
        }
        SlotWrite(p, i, SlotRead(p, up))
        i = up
    }
    SlotWrite(p, i, last)
    return some(top)
}

/// The least element, left in place; none when empty. O(1).
export PriorityQueue[T].Peek(&self): ?&T {
    if self.n == 0 {
        return none
    }
    return some(SlotRef(self.ptr, 0))
}

/// The number of elements. O(1).
export PriorityQueue[T].Len(&self): int {
    return self.n
}
