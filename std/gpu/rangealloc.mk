// Why: D3D12 placed-resource heaps and descriptor heaps need fast, bounded-
// fragmentation suballocation with immediate coalescing.
//
// What: TLSF (Two-Level Segregated Fit) RangeAlloc over u64 spans, supporting
// arbitrary power-of-two alignments and O(1) alloc and free.
//
// How: 36 first-level classes x 16 second-level bins (576 heads, up to 1 TiB),
// bitmaps for O(1) bin lookup, and doubly-linked physical + free lists in a
// pooled HeapList of TlsfNode records.

const TlsfSlLog2: int = 4
const TlsfSlCount: int = 16
const TlsfFlMax: int = 36
const TlsfHeadsCount: int = 576

struct TlsfNode {
    offset: u64
    size: u64
    prev_phys: i32
    next_phys: i32
    prev_free: i32
    next_free: i32
    used: bool
    alive: bool
}

struct RangeSpan {
    offset: u64
    size: u64
    id: i32
}

struct RangeAlloc {
    total: u64
    free_bytes: u64
    fl_bitmap: u64
    sl_bitmap: [36]u16
    heads: [576]i32
    nodes: HeapList[TlsfNode]
    free_nodes: HeapList[i32]
}

FloorLog2(x: u64): int {
    var v := x
    var r := 0
    if v >= (1 << 32) {
        v >>= 32
        r += 32
    }
    if v >= (1 << 16) {
        v >>= 16
        r += 16
    }
    if v >= (1 << 8) {
        v >>= 8
        r += 8
    }
    if v >= (1 << 4) {
        v >>= 4
        r += 4
    }
    if v >= (1 << 2) {
        v >>= 2
        r += 2
    }
    if v >= 2 {
        r += 1
    }
    return r
}

TrailingZeros64(x: u64): int {
    if x == 0 {
        return 64
    }
    var v := x
    var r := 0
    if (v & 0xFFFFFFFF) == 0 {
        v >>= 32
        r += 32
    }
    if (v & 0xFFFF) == 0 {
        v >>= 16
        r += 16
    }
    if (v & 0xFF) == 0 {
        v >>= 8
        r += 8
    }
    if (v & 0xF) == 0 {
        v >>= 4
        r += 4
    }
    if (v & 0x3) == 0 {
        v >>= 2
        r += 2
    }
    if (v & 0x1) == 0 {
        r += 1
    }
    return r
}

TrailingZeros16(x: u16): int {
    return TrailingZeros64(u64(x))
}

TlsfMapping(size: u64): (fl: int, sl: int) {
    if size < u64(TlsfSlCount) {
        return (fl: 0, sl: Cast[int](size))
    }
    msb := FloorLog2(size)
    fl := msb - TlsfSlLog2 + 1
    if fl >= TlsfFlMax {
        return (fl: TlsfFlMax - 1, sl: TlsfSlCount - 1)
    }
    sl := Cast[int]((size >> Cast[u64](msb - TlsfSlLog2)) & Cast[u64](TlsfSlCount - 1))
    return (fl: fl, sl: sl)
}

TlsfMappingSearch(size: u64): (fl: int, sl: int) {
    if size < u64(TlsfSlCount) {
        return (fl: 0, sl: Cast[int](size))
    }
    msb := FloorLog2(size)
    round := (u64(1) << Cast[u64](msb - TlsfSlLog2)) - 1
    if size <= ~round {
        return TlsfMapping(size + round)
    }
    return TlsfMapping(size)
}

AlignUpU64(v: u64, align: u64): u64 {
    if align <= 1 {
        return v
    }
    mask := align - 1
    return (v + mask) & ~mask
}

RangeAlloc.New(a: &Alloc, total: u64): OutOfMemory!RangeAlloc {
    var ra := RangeAlloc(
        total: total,
        free_bytes: total,
        fl_bitmap: 0,
        sl_bitmap: [0; 36],
        heads: [-1; 576],
        nodes: HeapList[TlsfNode].New(a),
        free_nodes: HeapList[i32].New(a),
    )
    if total > 0 {
        node := TlsfNode(offset: 0, size: total, prev_phys: -1, next_phys: -1, prev_free: -1, next_free: -1, used: false, alive: true)
        ra.nodes.Push(a, node) else e {
            RangeAlloc(total: _, free_bytes: _, fl_bitmap: _, sl_bitmap: _, heads: _, nodes, free_nodes) := ra
            nodes.Free(a)
            free_nodes.Free(a)
            fail e
        }
        ra.InsertFree(0)
    }
    return ra
}

RangeAlloc.Free(self, a: &Alloc) {
    RangeAlloc(total: _, free_bytes: _, fl_bitmap: _, sl_bitmap: _, heads: _, nodes, free_nodes) := self
    nodes.Free(a)
    free_nodes.Free(a)
}

RangeAlloc.AllocNode(mut self, a: &Alloc, n: TlsfNode): OutOfMemory!i32 {
    if self.free_nodes.Len() > 0 {
        id := self.free_nodes.Pop().Must()
        self.nodes[int(id)] = n
        return id
    }
    id := Cast[i32](self.nodes.Len())
    try self.nodes.Push(a, n)
    return id
}

RangeAlloc.ReleaseNode(mut self, a: &Alloc, id: i32) {
    self.nodes[int(id)].alive = false
    self.free_nodes.Push(a, id) else {}
}

RangeAlloc.InsertFree(mut self, id: i32) {
    sz := self.nodes[int(id)].size
    (fl, sl) := TlsfMapping(sz)
    idx := fl * TlsfSlCount + sl
    cur := self.heads[idx]
    self.nodes[int(id)].prev_free = -1
    self.nodes[int(id)].next_free = cur
    if cur >= 0 {
        self.nodes[int(cur)].prev_free = id
    }
    self.heads[idx] = id
    self.fl_bitmap |= u64(1) << Cast[u64](fl)
    self.sl_bitmap[fl] |= u16(1) << Cast[u16](sl)
}

RangeAlloc.RemoveFree(mut self, id: i32) {
    sz := self.nodes[int(id)].size
    (fl, sl) := TlsfMapping(sz)
    idx := fl * TlsfSlCount + sl
    prev := self.nodes[int(id)].prev_free
    next := self.nodes[int(id)].next_free
    if prev >= 0 {
        self.nodes[int(prev)].next_free = next
    } else {
        self.heads[idx] = next
    }
    if next >= 0 {
        self.nodes[int(next)].prev_free = prev
    }
    self.nodes[int(id)].prev_free = -1
    self.nodes[int(id)].next_free = -1
    if self.heads[idx] < 0 {
        self.sl_bitmap[fl] &= ~(u16(1) << Cast[u16](sl))
        if self.sl_bitmap[fl] == 0 {
            self.fl_bitmap &= ~(u64(1) << Cast[u64](fl))
        }
    }
}

RangeAlloc.SearchSuitable(mut self, size: u64): i32 {
    (fl: fl0, sl: sl0) := TlsfMappingSearch(size)
    if fl0 >= TlsfFlMax {
        return -1
    }
    var fl := fl0
    var sl_map := self.sl_bitmap[fl] & (~u16(0) << Cast[u16](sl0))
    if sl_map == 0 {
        fl_map := self.fl_bitmap & (~u64(0) << Cast[u64](fl + 1))
        if fl_map == 0 {
            return -1
        }
        fl = TrailingZeros64(fl_map)
        sl_map = self.sl_bitmap[fl]
    }
    sl := TrailingZeros16(sl_map)
    return self.heads[fl * TlsfSlCount + sl]
}

// FindSuitableWithAlign finds a free block large enough after alignment.
// Usually the first suitable block in the rounded-up class works; if not,
// walks up to 16 blocks or higher classes before returning -1.
RangeAlloc.FindAligned(mut self, size: u64, align: u64): i32 {
    need := if align <= 1 { size } else { size + (align - 1) }
    cand := self.SearchSuitable(need)
    if cand >= 0 {
        return cand
    }
    // Exact-fit fallback: a block of size >= size whose offset happens to
    // satisfy align without worst-case padding.
    (fl: fl0, sl: sl0) := TlsfMappingSearch(size)
    var fl := fl0
    var sl_start := sl0
    for fl < TlsfFlMax {
        sl_map := self.sl_bitmap[fl] & (~u16(0) << Cast[u16](sl_start))
        var m := sl_map
        for m != 0 {
            sl := TrailingZeros16(m)
            var cur := self.heads[fl * TlsfSlCount + sl]
            var steps := 0
            for cur >= 0 && steps < 16 {
                off := self.nodes[int(cur)].offset
                aligned := AlignUpU64(off, align)
                pad := aligned - off
                if self.nodes[int(cur)].size >= size + pad {
                    return cur
                }
                cur = self.nodes[int(cur)].next_free
                steps += 1
            }
            m &= ~(u16(1) << Cast[u16](sl))
        }
        fl += 1
        sl_start = 0
    }
    return -1
}

RangeAlloc.Alloc(mut self, a: &Alloc, size: u64, align: u64): OutOfMemory!?RangeSpan {
    if size == 0 || size > self.free_bytes {
        return none
    }
    eff_align := if align == 0 { u64(1) } else { align }
    blk := self.FindAligned(size, eff_align)
    if blk < 0 {
        return none
    }
    self.RemoveFree(blk)
    off := self.nodes[int(blk)].offset
    blk_size := self.nodes[int(blk)].size
    aligned := AlignUpU64(off, eff_align)
    front_pad := aligned - off
    var target := blk
    if front_pad > 0 {
        // Split off leading unaligned padding as a free block.
        prev := self.nodes[int(blk)].prev_phys
        pad_id := self.AllocNode(a, TlsfNode(
            offset: off,
            size: front_pad,
            prev_phys: prev,
            next_phys: blk,
            prev_free: -1,
            next_free: -1,
            used: false,
            alive: true,
        )) else e {
            self.InsertFree(blk)
            fail e
        }
        if prev >= 0 {
            self.nodes[int(prev)].next_phys = pad_id
        }
        self.nodes[int(blk)].prev_phys = pad_id
        self.nodes[int(blk)].offset = aligned
        self.nodes[int(blk)].size = blk_size - front_pad
        self.InsertFree(pad_id)
    }
    rem := self.nodes[int(target)].size - size
    if rem > 0 {
        next := self.nodes[int(target)].next_phys
        tail_id := self.AllocNode(a, TlsfNode(
            offset: aligned + size,
            size: rem,
            prev_phys: target,
            next_phys: next,
            prev_free: -1,
            next_free: -1,
            used: false,
            alive: true,
        )) else e {
            self.InsertFree(target)
            fail e
        }
        if next >= 0 {
            self.nodes[int(next)].prev_phys = tail_id
        }
        self.nodes[int(target)].next_phys = tail_id
        self.nodes[int(target)].size = size
        self.InsertFree(tail_id)
    }
    self.nodes[int(target)].used = true
    self.free_bytes -= size
    return some(RangeSpan(offset: aligned, size: size, id: target))
}

RangeAlloc.FreeSpan(mut self, a: &Alloc, id: i32) {
    if id < 0 || int(id) >= self.nodes.Len() {
        return
    }
    if !self.nodes[int(id)].alive || !self.nodes[int(id)].used {
        return
    }
    self.nodes[int(id)].used = false
    self.free_bytes += self.nodes[int(id)].size
    var cur := id
    prev := self.nodes[int(cur)].prev_phys
    if prev >= 0 && !self.nodes[int(prev)].used {
        self.RemoveFree(prev)
        self.nodes[int(prev)].size += self.nodes[int(cur)].size
        next := self.nodes[int(cur)].next_phys
        self.nodes[int(prev)].next_phys = next
        if next >= 0 {
            self.nodes[int(next)].prev_phys = prev
        }
        self.ReleaseNode(a, cur)
        cur = prev
    }
    next := self.nodes[int(cur)].next_phys
    if next >= 0 && !self.nodes[int(next)].used {
        self.RemoveFree(next)
        self.nodes[int(cur)].size += self.nodes[int(next)].size
        after := self.nodes[int(next)].next_phys
        self.nodes[int(cur)].next_phys = after
        if after >= 0 {
            self.nodes[int(after)].prev_phys = cur
        }
        self.ReleaseNode(a, next)
    }
    self.InsertFree(cur)
}
