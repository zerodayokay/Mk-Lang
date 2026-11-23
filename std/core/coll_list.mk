// Why: most growable arrays live and die with an arena; such a list needs no
// free and no allocator check, only fast appends.
//
// What: List[T] with New, Push, Pop, Len, Clear, Truncate, Insert, Remove and
// Done, plus its Slice and Format impls.
//
// How: growth is max(4, 2·cap, needed). When the buffer is the arena's last
// block it grows in place; otherwise a new buffer is taken and the old one is
// abandoned to the arena. Growth comes before any move, so on OutOfMemory the
// caller still owns its value. The raw slot operations are mem_slots.mk's.

import std.raw

/// A growable array in an arena. A growth makes the capacity the largest of
/// 4, twice the old capacity and what is needed. It grows in place when the
/// buffer is the arena's last block; otherwise it takes a new buffer and
/// leaves the old one to the arena.
export view struct List[T] {
    ptr: *T
    n: int
    cap: int
    alloc: &Alloc
}

impl[T] Slice[T] for List[T] {
    // ptr holds n initialized elements, alive while self is.
    Items(&self): []T {
        return SlotView(self.ptr, self.n)
    }

    ItemsMut(mut self): mut []T {
        return SlotViewMut(self.ptr, self.n)
    }
}

// Prints [a, b, c] ([] when empty) through FmtElems (core_impls.mk).
impl[T: Format] Format for List[T] {
    Format(&self, w: mut Writer): WriteError!void {
        try FmtElems(w, self.Items())
    }
}

/// An empty list in a. O(1); allocates nothing until the first Push. While
/// the capacity is 0, its slices (from Items or Done) still have a non-null
/// data pointer.
export List[T].New(a: &Arena): List[T] {
    return List[T](ptr: Dangling[T](), n: 0, cap: 0, alloc: a.Alloc())
}

/// Appends x. OutOfMemory when the arena cannot grow the buffer, and then the
/// caller still owns x. Amortized O(1); growth allocates from the arena.
export List[T].Push(mut self, x: T into self): OutOfMemory!void {
    if self.n == self.cap {
        try self.Grow(self.n + 1)
    }
    SlotWrite(self.ptr, self.n, x) // n < cap: an empty slot
    self.n += 1
}

/// Removes and returns the last element, or none when the list is empty. O(1);
/// allocates nothing.
export List[T].Pop(mut self): ?T {
    if self.n == 0 {
        return none
    }
    self.n -= 1
    return some(SlotRead(self.ptr, self.n)) // slot n held an element
}

/// The number of elements. O(1).
export List[T].Len(&self): int {
    return self.n
}

/// Empties the list and keeps its capacity. A list that still holds linear
/// elements panics (code 8) rather than drop them. O(1).
export List[T].Clear(mut self) {
    if self.n > 0 && raw.IsLinear[T]() {
        raw.Fail(8, "List.Clear would drop a linear value")
    }
    self.n = 0
}

/// Keeps the first n elements and drops the rest, keeping the capacity; n at
/// or above Len() changes nothing. Panics (code 2) when n < 0, and (code 8)
/// when it would drop a linear element, as Clear does. O(1); allocates
/// nothing.
export List[T].Truncate(mut self, n: int) {
    if n < 0 {
        raw.Fail(2, "List.Truncate: negative length")
    }
    if n >= self.n {
        return
    }
    if raw.IsLinear[T]() {
        raw.Fail(8, "List.Truncate would drop a linear value")
    }
    self.n = n
}

/// Inserts x before element i (i == Len() appends). Panics (code 2) unless
/// 0 <= i <= Len(), before any growth. OutOfMemory when the buffer cannot
/// grow, and then the caller still owns x. O(Len() - i); growth allocates from
/// the arena.
export List[T].Insert(mut self, i: int, x: T into self): OutOfMemory!void {
    if i < 0 || i > self.n {
        raw.Fail(2, "List.Insert: index out of range")
    }
    if self.n == self.cap {
        try self.Grow(self.n + 1)
    }
    SlotShift(self.ptr, i + 1, i, self.n - i) // n < cap: room for the tail
    SlotWrite(self.ptr, i, x)
    self.n += 1
}

/// Removes and returns element i, keeping the order of the rest. Panics
/// (code 2) unless 0 <= i < Len(). O(Len() - i); allocates nothing.
export List[T].Remove(mut self, i: int): T {
    if i < 0 || i >= self.n {
        raw.Fail(2, "List.Remove: index out of range")
    }
    x := SlotRead(self.ptr, i)
    SlotShift(self.ptr, i, i + 1, self.n - i - 1)
    self.n -= 1
    return x
}

/// Ends the list and returns its elements as a slice in the list's arena,
/// valid as long as the arena. O(1); allocates nothing.
export List[T].Done(self): mut []T {
    return SlotViewMut(self.ptr, self.n)
}

// Grow makes room for at least need elements: in place when the arena's
// last block is this buffer, else a new buffer, the old one abandoned.
List[T].Grow(mut self, need: int): OutOfMemory!void {
    c := NextCap(self.cap, need)
    p := GrowSlots(self.alloc, self.ptr, self.n, self.cap, c, false)
    if raw.IsNull(p) {
        fail OutOfMemory
    }
    self.ptr = p
    self.cap = c
}
