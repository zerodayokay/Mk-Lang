// Why: the QUIC server endpoint and its accepted connections share a linear
// Mutex-guarded hub (socket, datagram pool, demux table, connection slots)
// that the last owner to Abandon must unwrap and free; sync.Shared refuses
// linear T.
//
// What: EpBox[T], an internal reference-counted box whose Release returns
// ?T (some on the last handle) instead of dropping T. This mem_ file holds
// std.quic's unsafe blocks.
//
// How: mirrors std/net/pipe/mem_pipe.mk (atomic count + value on &Heap),
// freeing the backing box and moving T out via raw.Read when the count
// reaches zero.

import std.core
import std.raw

// EpBox[T] is a handle on a reference-counted heap box holding a linear
// Send + Share value T. Linear: end each handle with Release.
linear view struct EpBox[T] {
    ptr: *EpSlot[T]
    marker: [0]&T
}

struct EpSlot[T] {
    count: raw.Atomic[i64]
    value: T
}

unsafe impl[T: Send + Share] Send for EpBox[T] {}
unsafe impl[T: Send + Share] Share for EpBox[T] {}

const EpMaxCount: i64 = 9223372036854775807

// Allocates one box holding x on h. Consumes x only on success.
// O(1) plus one allocation from h.
EpBox[T].New(h: &Heap, x: T): OutOfMemory!EpBox[T] from h, x {
    size := core.SizeOfT[EpSlot[T]]()
    q := core.AllocBytes(h.Alloc(), size, core.AlignOfT[EpSlot[T]]())
    if raw.IsNull(q) {
        fail OutOfMemory
    }
    p := EpAt[T](q)
    unsafe "p is a fresh allocation sized and aligned for one EpSlot, holding no value" {
        raw.Write(p, EpSlot[T](count: raw.Atomic[i64].New(1), value: x))
    }
    return EpBox[T](ptr: p, marker: [])
}

// Returns another handle on the same box. O(1); allocates nothing.
EpBox[T].Clone(&self, h: &Heap): EpBox[T] from h {
    _ = h
    c := EpCount(self.ptr)
    if c.Add(1) == EpMaxCount {
        _ = c.Add(-1)
        raw.Fail(1, "quic.EpBox.Clone: reference count overflow")
    }
    return EpBox[T](ptr: self.ptr, marker: [])
}

// Returns a shared reference to the boxed value. O(1).
EpBox[T].Ref(&self): &T {
    return EpVal(self.ptr)
}

// Drops this handle. On the last handle it moves the value out, frees the box
// into h, and returns some(val); otherwise returns none. O(1).
EpBox[T].Release(self, h: &Heap): ?T {
    EpBox(ptr, marker) := self
    unsafe "a zero-length marker holds no value" {
        raw.Forget(marker)
    }
    if EpCount(ptr).Add(-1) != 1 {
        return none
    }
    unsafe "last handle: count is zero (release-acquire ordered via Add(-1)), no other reader exists, and value moves into val before freeing the slot" {
        val := raw.Read(raw.Of(EpVal(ptr)))
        core.FreeBytes(h.Alloc(), EpBytes(ptr), core.SizeOfT[EpSlot[T]](), core.AlignOfT[EpSlot[T]]())
        return some(val)
    }
}

EpAt[T](q: *u8): *EpSlot[T] {
    unsafe "a pointer cast of a fresh allocation sized and aligned for EpSlot[T]" {
        return raw.PtrCast[u8, EpSlot[T]](q)
    }
}

EpBytes[T](p: *EpSlot[T]): *u8 {
    unsafe "a pointer cast; nothing is dereferenced" {
        return raw.PtrCast[EpSlot[T], u8](p)
    }
}

EpCount[T](p: *EpSlot[T]): &raw.Atomic[i64] {
    unsafe "p is the live box kept alive by a handle" {
        return &(*p).count
    }
}

EpVal[T](p: *EpSlot[T]): &T {
    unsafe "p is the live box kept alive by a handle" {
        return &(*p).value
    }
}

// Opaque heap-boxed QConn stored in EndpointState.accept_q; unpacked with the
// endpoint's Heap so EpAccept returns net.Mux from h rather than from self.
linear struct QueuedConn {
    ptr: *QConn
    owner: *Alloc
}

unsafe impl Send for QueuedConn {}
unsafe impl Share for QueuedConn {}

// Allocates a QConn slot on h and moves q in on success (caller keeps q on OOM).
QueuedConn.Pack(h: &Heap, q: QConn): OutOfMemory!QueuedConn {
    a := h.Alloc()
    size := core.SizeOfT[QConn]()
    buf := core.AllocBytes(a, size, core.AlignOfT[QConn]())
    if raw.IsNull(buf) {
        fail OutOfMemory
    }
    unsafe "buf is a fresh allocation sized and aligned for one QConn" {
        p := raw.PtrCast[u8, QConn](buf)
        raw.Write(p, q)
        return QueuedConn(ptr: p, owner: raw.Of(a))
    }
}

// Unpacks QConn and frees its slot back to h (panics code 12 on heap mismatch).
QueuedConn.Unpack(self, h: &Heap): QConn from h {
    QueuedConn(ptr, owner) := self
    a := h.Alloc()
    if !raw.Same(raw.Of(a), owner) {
        raw.Fail(12, "quic.QueuedConn.Unpack: wrong heap")
    }
    unsafe "ptr is a live QConn allocated on a" {
        val := raw.Read(ptr)
        core.FreeBytes(a, raw.PtrCast[QConn, u8](ptr), core.SizeOfT[QConn](), core.AlignOfT[QConn]())
        return val
    }
}

// Zero-initialized heap box for PoolChunk (avoids placing ~387 KiB PoolChunk on a task stack).
linear struct PoolChunkBox {
    ptr: *PoolChunk
}

unsafe impl Send for PoolChunkBox {}
unsafe impl Share for PoolChunkBox {}

PoolChunkBox.NewZero(a: &Alloc): OutOfMemory!PoolChunkBox {
    size := core.SizeOfT[PoolChunk]()
    buf := core.AllocBytes(a, size, core.AlignOfT[PoolChunk]())
    if raw.IsNull(buf) {
        fail OutOfMemory
    }
    unsafe "buf is a fresh size-byte allocation for PoolChunk" {
        raw.Fill(buf, 0, size)
        return PoolChunkBox(ptr: raw.PtrCast[u8, PoolChunk](buf))
    }
}

PoolChunkBox.Free(self, a: &Alloc) {
    PoolChunkBox(ptr) := self
    unsafe "ptr was allocated by PoolChunkBox.NewZero on a" {
        core.FreeBytes(a, raw.PtrCast[PoolChunk, u8](ptr), core.SizeOfT[PoolChunk](), core.AlignOfT[PoolChunk]())
    }
}

PoolChunkBox.Ref(&self): &PoolChunk {
    unsafe "ptr is live for &self's borrow" {
        return &(*self.ptr)
    }
}

PoolChunkBox.Mut(mut self): mut PoolChunk {
    unsafe "ptr is uniquely borrowed through mut self" {
        return mut *self.ptr
    }
}

// Generic heap box for large structures inside Engine (e.g. AppKeyState).
// Ownership is enforced by the enclosing linear struct (KeyRing.Wipe).
struct PtrBox[T] {
    ptr: *T
    owner: *Alloc
}

unsafe impl[T: Send] Send for PtrBox[T] {}
unsafe impl[T: Share] Share for PtrBox[T] {}

PtrBox[T].New(a: &Alloc, val: T): OutOfMemory!PtrBox[T] from val {
    size := core.SizeOfT[T]()
    buf := core.AllocBytes(a, size, core.AlignOfT[T]())
    if raw.IsNull(buf) {
        fail OutOfMemory
    }
    unsafe "p is a fresh size/aligned allocation for T" {
        p := raw.PtrCast[u8, T](buf)
        raw.Write(p, val)
        return PtrBox[T](ptr: p, owner: raw.Of(a))
    }
}

PtrBox[T].Ref(&self): &T {
    unsafe "ptr is live for &self's borrow" {
        return &(*self.ptr)
    }
}

PtrBox[T].Mut(mut self): mut T {
    unsafe "ptr is uniquely borrowed through mut self" {
        return mut *self.ptr
    }
}

PtrBox[T].Free(self): T {
    PtrBox(ptr, owner) := self
    unsafe "ptr was allocated by PtrBox.New on owner" {
        val := raw.Read(ptr)
        core.FreeBytes(&(*owner), raw.PtrCast[T, u8](ptr), core.SizeOfT[T](), core.AlignOfT[T]())
        return val
    }
}
