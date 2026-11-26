// Why: two halves of an in-memory ConnPair or MuxPair share a linear
// Mutex-guarded state (ring buffers, stream list) that the last half to
// Abandon must unwrap and free; sync.Shared refuses linear T.
//
// What: PipeBox[T], an internal reference-counted box whose Release returns
// ?T (some on the last handle) instead of dropping T. This mem_ file holds
// std.net.pipe's unsafe blocks.
//
// How: mirrors std/sync/mem_shared.mk (atomic count + value on &Heap),
// except Release moves T out via raw.Read and returns some(val) when the
// count hits zero, freeing the backing box.

import std.raw
import std.core

// PipeBox[T] is a handle on a reference-counted heap box holding a linear
// Send + Share value T. Linear: end each handle with Release.
linear view struct PipeBox[T] {
    ptr: *PipeSlot[T]
    marker: [0]&T
}

struct PipeSlot[T] {
    count: raw.Atomic[i64]
    value: T
}

unsafe impl[T: Send + Share] Send for PipeBox[T] {}
unsafe impl[T: Send + Share] Share for PipeBox[T] {}

const PipeMaxCount: i64 = 9223372036854775807

// PipeBox[T].New allocates one box holding x on h. Consumes x only on success.
PipeBox[T].New(h: &Heap, x: T): OutOfMemory!PipeBox[T] from h, x {
    size := core.SizeOfT[PipeSlot[T]]()
    q := core.AllocBytes(h.Alloc(), size, core.AlignOfT[PipeSlot[T]]())
    if raw.IsNull(q) {
        fail OutOfMemory
    }
    p := PipeAt[T](q)
    unsafe "p is a fresh allocation sized and aligned for one PipeSlot, holding no value" {
        raw.Write(p, PipeSlot[T](count: raw.Atomic[i64].New(1), value: x))
    }
    return PipeBox[T](ptr: p, marker: [])
}

// PipeBox[T].Clone returns another handle on the same box.
PipeBox[T].Clone(&self): PipeBox[T] from self.ptr {
    c := PipeCount(self.ptr)
    if c.Add(1) == PipeMaxCount {
        _ = c.Add(-1)
        raw.Fail(1, "net.pipe.PipeBox.Clone: reference count overflow")
    }
    return PipeBox[T](ptr: self.ptr, marker: [])
}

// PipeBox[T].Ref returns a shared reference to the boxed value.
PipeBox[T].Ref(&self): &T {
    return PipeVal(self.ptr)
}

// PipeBox[T].Release drops this handle. On the last handle it moves the
// value out, frees the box into h, and returns some(val); otherwise none.
PipeBox[T].Release(self, h: &Heap): ?T {
    PipeBox(ptr, marker) := self
    unsafe "a zero-length marker holds no value" {
        raw.Forget(marker)
    }
    if PipeCount(ptr).Add(-1) != 1 {
        return none
    }
    unsafe "last handle: count is zero (release-acquire ordered via Add(-1)), no other reader exists, and value moves into val before freeing the slot" {
        val := raw.Read(raw.Of(PipeVal(ptr)))
        core.FreeBytes(h.Alloc(), PipeBytes(ptr), core.SizeOfT[PipeSlot[T]](), core.AlignOfT[PipeSlot[T]]())
        return some(val)
    }
}

PipeAt[T](q: *u8): *PipeSlot[T] {
    unsafe "a pointer cast of a fresh allocation sized and aligned for PipeSlot[T]" {
        return raw.PtrCast[u8, PipeSlot[T]](q)
    }
}

PipeBytes[T](p: *PipeSlot[T]): *u8 {
    unsafe "a pointer cast; nothing is dereferenced" {
        return raw.PtrCast[PipeSlot[T], u8](p)
    }
}

PipeCount[T](p: *PipeSlot[T]): &raw.Atomic[i64] {
    unsafe "p is the live box kept alive by a handle" {
        return &(*p).count
    }
}

PipeVal[T](p: *PipeSlot[T]): &T {
    unsafe "p is the live box kept alive by a handle" {
        return &(*p).value
    }
}
