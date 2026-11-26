// Why: queues, work lists and sliding windows need cheap pushes and pops at
// both ends, which List gives only at the back.
//
// What: Deque (in an arena) and HeapDeque (linear, on an allocator), each
// with Push, Pop, PushFront, PopFront, Len and Get; HeapDeque adds Free,
// FreeEmpty and FreeEach.
//
// How: a ring buffer of power-of-two capacity (Rust's VecDeque; Knuth TAOCP
// 2.2.2); value i sits at slot (head + i) & (cap - 1). Every operation is
// O(1); a push that finds the ring full doubles it first, moving the shorter
// wrapped run when the buffer grows in place. HeapDeque records its allocator,
// as HeapList. Push and Pop work at the back, as on List; PushFront and
// PopFront at the front.

import std.raw

// Ring is the buffer both deques share: cap is 0 or a power of two, and
// the n values sit at slots (head + i) & (cap - 1) for i in 0..n.
struct Ring[T] {
    ptr: *T
    head: int
    n: int
    cap: int
}

/// A double-ended queue in an arena: push and pop at either end, and
/// index from the front, each O(1) (a push amortized). Its storage lives
/// in the arena and is freed with it, its elements unconsumed, so it holds
/// no linear value: Push and PushFront of one are E0701. A HeapDeque holds
/// one.
export view struct Deque[T] {
    r: Ring[T]
    alloc: &Alloc
}

/// A double-ended queue on an allocator, with the operations of Deque
/// except GetMut.
/// It is linear: give it back with Free, FreeEmpty or FreeEach. It is
/// Send and Share when T is.
export linear view struct HeapDeque[T] {
    r: Ring[T]
    owner: *Alloc
    marker: [0]&T
}

// Send and Share if the elements are, as HeapList.
unsafe impl[T: Send] Send for HeapDeque[T] {}
unsafe impl[T: Share] Share for HeapDeque[T] {}

EmptyRing[T](): Ring[T] {
    return Ring[T](ptr: Dangling[T](), head: 0, n: 0, cap: 0)
}

// RingSlot is the slot of the i-th value; 0 <= i < cap.
RingSlot[T](r: &Ring[T], i: int): int {
    return (r.head + i) & (r.cap - 1)
}

// ZeroSizeCap is the capacity a ring of a zero-size T takes at its first
// growth: it needs no memory, and no program pushes 2^62 values.
const ZeroSizeCap: int = 0x4000000000000000

// RingGrow doubles the ring (4 slots at first). In place when a can resize
// the buffer: then a wrapped ring moves the shorter of its two runs, the
// head run to the top of the new buffer or the tail run to just above the
// old top (Rust's VecDeque::handle_capacity_increase), so the runs never
// overlap. Else a new buffer gets the two runs in order at its start, and
// the old one is freed when free is set or left to the arena. False when
// a is out of memory or the capacity cannot double; nothing changed then.
RingGrow[T](r: mut Ring[T], a: &Alloc, free: bool): bool {
    old := r.cap
    if SizeOf[T]() == 0 {
        if old != 0 {
            return false
        }
        r.cap = ZeroSizeCap
        return true
    }
    if old > MaxBytes / 2 {
        return false
    }
    c := if old > 0 { old * 2 } else { 4 }
    if old > 0 && ResizeSlots(a, r.ptr, old, c) {
        tail := r.head + r.n - old
        if tail > 0 {
            top := old - r.head
            if tail <= top {
                SlotCopy(r.ptr, old, r.ptr, 0, tail)
            } else {
                SlotCopy(r.ptr, c - top, r.ptr, r.head, top)
                r.head = c - top
            }
        }
        r.cap = c
        return true
    }
    p := AllocSlots[T](a, c)
    if raw.IsNull(p) {
        return false
    }
    first := if old - r.head < r.n { old - r.head } else { r.n }
    SlotCopy(p, 0, r.ptr, r.head, first)
    SlotCopy(p, first, r.ptr, 0, r.n - first)
    if free {
        FreeSlots(a, r.ptr, old)
    }
    r.ptr = p
    r.head = 0
    r.cap = c
    return true
}

// RingPush and RingPushFront store x; the ring has room (n < cap).
RingPush[T](r: mut Ring[T], x: T) {
    SlotWrite(r.ptr, (r.head + r.n) & (r.cap - 1), x)
    r.n += 1
}

RingPushFront[T](r: mut Ring[T], x: T) {
    r.head = (r.head + r.cap - 1) & (r.cap - 1)
    SlotWrite(r.ptr, r.head, x)
    r.n += 1
}

RingPopFront[T](r: mut Ring[T]): ?T {
    if r.n == 0 {
        return none
    }
    x := SlotRead(r.ptr, r.head)
    r.head = (r.head + 1) & (r.cap - 1)
    r.n -= 1
    return some(x)
}

RingPop[T](r: mut Ring[T]): ?T {
    if r.n == 0 {
        return none
    }
    r.n -= 1
    return some(SlotRead(r.ptr, (r.head + r.n) & (r.cap - 1)))
}

RingGet[T](r: &Ring[T], i: int): ?&T {
    if i < 0 || i >= r.n {
        return none
    }
    return some(SlotRef(r.ptr, RingSlot(r, i)))
}

/// An empty Deque in a. O(1); allocates nothing until the first push.
export Deque[T].New(a: &Arena): Deque[T] {
    return Deque[T](r: EmptyRing[T](), alloc: a.Alloc())
}

/// Appends x at the back. Fails with OutOfMemory when the deque is full
/// and the arena cannot double its capacity; the deque grows before it
/// takes x, so x is consumed only on success. Amortized O(1); a growth
/// allocates twice the capacity from the arena and leaves the old buffer
/// to it. T may not be linear (E0701 at the call, which names HeapDeque).
export Deque[T].Push(mut self, x: T into self): OutOfMemory!void {
    if self.r.n == self.r.cap {
        if !RingGrow(mut self.r, self.alloc, false) {
            fail OutOfMemory
        }
    }
    RingPush(mut self.r, x)
}

/// Prepends x at the front, as Push; T may not be linear (E0701 at the
/// call, which names HeapDeque). Amortized O(1).
export Deque[T].PushFront(mut self, x: T into self): OutOfMemory!void {
    if self.r.n == self.r.cap {
        if !RingGrow(mut self.r, self.alloc, false) {
            fail OutOfMemory
        }
    }
    RingPushFront(mut self.r, x)
}

/// The front value, removed; none when empty. O(1).
export Deque[T].PopFront(mut self): ?T {
    return RingPopFront(mut self.r)
}

/// The back value, removed; none when empty. O(1).
export Deque[T].Pop(mut self): ?T {
    return RingPop(mut self.r)
}

/// The number of values. O(1).
export Deque[T].Len(&self): int {
    return self.r.n
}

/// The i-th value from the front; none unless 0 <= i < Len(). O(1).
export Deque[T].Get(&self, i: int): ?&T {
    return RingGet(&self.r, i)
}

/// The i-th value from the front, writable; none unless 0 <= i < Len().
/// It never panics, as Slice.GetMut. O(1).
export Deque[T].GetMut(mut self, i: int): ?mut T {
    if i < 0 || i >= self.r.n {
        return none
    }
    return some(SlotMut(self.r.ptr, RingSlot(&self.r, i)))
}

/// An empty HeapDeque on a. O(1); allocates nothing until the first push.
export HeapDeque[T].New(a: &Alloc): HeapDeque[T] {
    return HeapDeque[T](r: EmptyRing[T](), owner: raw.Of(a), marker: [])
}

/// Appends x at the back, as Deque.Push, growing with a. A growth frees
/// the old buffer, and panics (code 12) when a is not the allocator given
/// to New; a push with room does not look at a. Amortized O(1).
export HeapDeque[T].Push(mut self, a: &Alloc, x: T into self): OutOfMemory!void {
    if self.r.n == self.r.cap {
        OwnerCheck(a, self.owner, "HeapDeque.Push")
        if !RingGrow(mut self.r, a, true) {
            fail OutOfMemory
        }
    }
    RingPush(mut self.r, x)
}

/// Prepends x at the front, as Push. Amortized O(1).
export HeapDeque[T].PushFront(mut self, a: &Alloc, x: T into self): OutOfMemory!void {
    if self.r.n == self.r.cap {
        OwnerCheck(a, self.owner, "HeapDeque.PushFront")
        if !RingGrow(mut self.r, a, true) {
            fail OutOfMemory
        }
    }
    RingPushFront(mut self.r, x)
}

/// The front value, removed; none when empty. O(1).
export HeapDeque[T].PopFront(mut self): ?T {
    return RingPopFront(mut self.r)
}

/// The back value, removed; none when empty. O(1).
export HeapDeque[T].Pop(mut self): ?T {
    return RingPop(mut self.r)
}

/// The number of values. O(1).
export HeapDeque[T].Len(&self): int {
    return self.r.n
}

/// The i-th value from the front; none unless 0 <= i < Len(). O(1).
export HeapDeque[T].Get(&self, i: int): ?&T {
    return RingGet(&self.r, i)
}

/// Frees every element with its Free, front to back, then gives the
/// buffer back to a. Panics (code 12) when a is not the allocator given to
/// New. O(1) when T is neither linear nor Free, since there is nothing to
/// free per element; O(n) frees otherwise. A call where T is linear and
/// not Free is E0701: use FreeEach.
impl[T] Free for HeapDeque[T] {
    Free(self, a: &Alloc) {
        HeapDeque(r, owner, marker) := self
        OwnerCheck(a, owner, "HeapDeque.Free")
        Drop(marker)
        if CollNeedsRelease[T]() {
            for i in 0..r.n {
                CollRelease(SlotRead(r.ptr, RingSlot(&r, i)), a)
            }
        }
        FreeSlots(a, r.ptr, r.cap)
    }
}

/// Frees the buffer of an empty deque. Panics (code 11) when it holds an
/// element, and (code 12) when a is not the allocator given to New. O(1).
export HeapDeque[T].FreeEmpty(self, a: &Alloc) {
    if self.r.n > 0 {
        raw.Fail(11, "HeapDeque.FreeEmpty of a non-empty deque")
    }
    HeapDeque(r, owner, marker) := self
    OwnerCheck(a, owner, "HeapDeque.FreeEmpty")
    Drop(marker)
    FreeSlots(a, r.ptr, r.cap)
}

/// Hands every element to release, front to back, then frees the buffer.
/// Use it for elements that are linear and not Free. Panics (code 12) when
/// a is not the allocator given to New. release may capture locals and
/// must consume its argument. O(n) calls of release.
export HeapDeque[T].FreeEach(self, a: &Alloc, const release: fn(T)) {
    HeapDeque(r, owner, marker) := self
    OwnerCheck(a, owner, "HeapDeque.FreeEach")
    Drop(marker)
    for i in 0..r.n {
        release(SlotRead(r.ptr, RingSlot(&r, i)))
    }
    FreeSlots(a, r.ptr, r.cap)
}
