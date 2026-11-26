// Why: the server demux loop receives UDP datagrams into a shared fixed-size
// pool so routing a datagram to a connection costs zero per-datagram heap
// allocations and caps endpoint buffering at 5.75 MiB.
//
// What: PoolSlot, DgramPool (New, Free, AllocSlot, FreeSlot, SlotMut, SlotRef),
// and pool capacity constants (1,472-byte slots, 256-slot chunks, 4,096 max).
//
// How: allocates 256-slot Box chunks lazily on &Alloc as load grows, linking
// free slot indices into an intrusive u16 free list; SlotNone (0xffff) marks the
// end of the list.

import std.net

// Maximum UDP payload size advertised in transport parameters (RFC 9000 18.2).
const PoolSlotBytes: int = 1472

// Slots allocated per chunk as load grows (Section 3.7.1).
const PoolChunkSlots: int = 256

// Maximum chunks (16 * 256 = 4,096 slots, ~5.75 MiB, Section 3.7.1).
const PoolMaxChunks: int = 16
const PoolMaxSlots: int = PoolChunkSlots * PoolMaxChunks

// Per-connection inbox quota (Section 3.7.1).
const ConnInboxQuota: int = 64

// Sentinel slot index indicating empty free list.
const PoolSlotNone: u16 = 0xffff

// One 1,472-byte datagram slot in the endpoint pool.
struct PoolSlot {
    data: [PoolSlotBytes]u8
    len: int
    from: net.Addr
    next_free: u16
}

// Empty PoolSlot initializer.
EmptyPoolSlot(): PoolSlot {
    return PoolSlot(data: [0; PoolSlotBytes], len: 0, from: QuicZeroAddr, next_free: PoolSlotNone)
}

// Chunk of 256 PoolSlots allocated together in one Box.
struct PoolChunk {
    slots: [PoolChunkSlots]PoolSlot
}

// Endpoint-wide datagram pool of up to 4,096 1,472-byte slots.
// Linear: owns Heap-allocated PoolChunkBox entries.
linear struct DgramPool {
    chunks: HeapList[PoolChunkBox]
    free_head: u16
    in_use: int
}

// Creates an empty DgramPool (no chunks allocated until first AllocSlot). O(1).
DgramPool.New(a: &Alloc): DgramPool {
    return DgramPool(
        chunks: HeapList[PoolChunkBox].New(a),
        free_head: PoolSlotNone,
        in_use: 0,
    )
}

// Frees all allocated chunks back to a. O(chunks).
DgramPool.Free(self, a: &Alloc) {
    DgramPool(chunks: c_list, free_head: _, in_use: _) := self
    var chunks := c_list
    for {
        c := chunks.Pop() else { break }
        c.Free(a)
    }
    chunks.FreeEmpty(a)
}

// Initializes free-list links across one 256-slot PoolChunk starting at base.
InitPoolChunk(c: mut PoolChunk, base: int) {
    for j in 0..PoolChunkSlots {
        c.slots[j].next_free = if j + 1 < PoolChunkSlots { Trunc[u16](base + j + 1) } else { PoolSlotNone }
    }
}

// Claims slot off inside chunk, returning its previous next_free link.
ClaimChunkSlot(c: mut PoolChunk, off: int): u16 {
    next := c.slots[off].next_free
    c.slots[off].next_free = PoolSlotNone
    c.slots[off].len = 0
    return next
}

// Returns slot off inside chunk onto free_head.
ReleaseChunkSlot(c: mut PoolChunk, off: int, free_head: u16) {
    c.slots[off].len = 0
    c.slots[off].next_free = free_head
}

// Allocates a free slot index, growing a new 256-slot chunk on a if empty and
// below PoolMaxChunks. Returns none when all 4,096 slots are in use or OOM. O(1).
DgramPool.AllocSlot(mut self, a: &Alloc into self): ?u16 {
    if self.free_head == PoolSlotNone {
        if self.chunks.Len() >= PoolMaxChunks {
            return none
        }
        ci := self.chunks.Len()
        var chunk := PoolChunkBox.NewZero(a) else {
            return none
        }
        base := ci * PoolChunkSlots
        InitPoolChunk(chunk.Mut(), base)
        self.chunks.Push(a, chunk) else {
            chunk.Free(a)
            return none
        }
        self.free_head = Trunc[u16](base)
    }
    idx := self.free_head
    ci := int(idx) / PoolChunkSlots
    off := int(idx) % PoolChunkSlots
    self.free_head = ClaimChunkSlot(self.chunks.ItemsMut()[ci].Mut(), off)
    self.in_use += 1
    return some(idx)
}

// Returns slot idx to the free list. O(1).
DgramPool.FreeSlot(mut self, idx: u16) {
    ci := int(idx) / PoolChunkSlots
    off := int(idx) % PoolChunkSlots
    if ci < 0 || ci >= self.chunks.Len() {
        return
    }
    ReleaseChunkSlot(self.chunks.ItemsMut()[ci].Mut(), off, self.free_head)
    self.free_head = idx
    if self.in_use > 0 {
        self.in_use -= 1
    }
}

// Returns a mutable reference to slot idx. Caller ensures idx is allocated. O(1).
DgramPool.SlotMut(mut self, idx: u16): mut PoolSlot from self {
    ci := int(idx) / PoolChunkSlots
    off := int(idx) % PoolChunkSlots
    return mut self.chunks.ItemsMut()[ci].Mut().slots[off]
}

// Returns a read-only reference to slot idx. Caller ensures idx is allocated. O(1).
DgramPool.SlotRef(&self, idx: u16): &PoolSlot from self {
    ci := int(idx) / PoolChunkSlots
    off := int(idx) % PoolChunkSlots
    return &self.chunks[ci].Ref().slots[off]
}
