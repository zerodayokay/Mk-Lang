// Why: Vulkan limits total VkDeviceMemory allocations (maxMemoryAllocationCount,
// often 4096) and requires separating buffer and optimal-image allocations to
// avoid bufferImageGranularity conflicts.
//
// What: VkTlsf suballocator, 64 MiB block pooling per (memoryType, isImage),
// dedicated allocations >= 32 MiB or when preferred/required, and persistent
// host mapping for host-visible blocks.
//
// How: TLSF bitmaps and physical/free node lists manage 64 MiB VkDeviceMemory
// blocks; FreeDeviceMem releases spans back to blocks and frees blocks/dedicated
// memory at Device.Close.

import c.vulkan
import std.mem
import std.raw

const VkBlockSize: u64 = 64 * 1024 * 1024
const VkDedicatedThreshold: u64 = 32 * 1024 * 1024

const TlsfSlCount: int = 16
const TlsfFlMax: int = 32
const TlsfHeadsCount: int = 512

struct VkTlsfNode {
    offset: u64
    size: u64
    prev_phys: i32
    next_phys: i32
    prev_free: i32
    next_free: i32
    used: bool
    alive: bool
}

struct VkTlsf {
    total: u64
    free_bytes: u64
    fl_bitmap: u32
    sl_bitmap: [32]u16
    heads: [512]i32
    nodes: HeapList[VkTlsfNode]
    free_nodes: HeapList[i32]
}

FloorLog2U64(x: u64): int {
    return 63 - raw.Clz64(x)
}

TrailingZeros32(x: u32): int {
    return raw.Ctz64(u64(x))
}

TlsfMap(size: u64): (fl: int, sl: int) {
    if size < u64(TlsfSlCount) {
        return (fl: 0, sl: Cast[int](size))
    }
    msb := FloorLog2U64(size)
    fl := msb - 4 + 1
    if fl >= TlsfFlMax {
        return (fl: TlsfFlMax - 1, sl: TlsfSlCount - 1)
    }
    sl := Cast[int]((size >> Cast[u64](msb - 4)) & 15)
    return (fl: fl, sl: sl)
}

TlsfMapSearch(size: u64): (fl: int, sl: int) {
    if size < u64(TlsfSlCount) {
        return (fl: 0, sl: Cast[int](size))
    }
    msb := FloorLog2U64(size)
    round := (u64(1) << Cast[u64](msb - 4)) - 1
    if size <= ~round {
        return TlsfMap(size + round)
    }
    return TlsfMap(size)
}

AlignUpMem(v: u64, align: u64): u64 {
    if align <= 1 {
        return v
    }
    m := align - 1
    return (v + m) & ~m
}

VkTlsf.New(a: &Alloc, total: u64): OutOfMemory!VkTlsf {
    var t := VkTlsf(
        total: total,
        free_bytes: total,
        fl_bitmap: 0,
        sl_bitmap: [0; 32],
        heads: [-1; 512],
        nodes: HeapList[VkTlsfNode].New(a),
        free_nodes: HeapList[i32].New(a),
    )
    if total > 0 {
        node := VkTlsfNode(offset: 0, size: total, prev_phys: -1, next_phys: -1, prev_free: -1, next_free: -1, used: false, alive: true)
        t.nodes.Push(a, node) else e {
            VkTlsf(total: _, free_bytes: _, fl_bitmap: _, sl_bitmap: _, heads: _, nodes, free_nodes) := t
            nodes.Free(a)
            free_nodes.Free(a)
            fail e
        }
        t.InsertFree(0)
    }
    return t
}

VkTlsf.Free(self, a: &Alloc) {
    VkTlsf(total: _, free_bytes: _, fl_bitmap: _, sl_bitmap: _, heads: _, nodes, free_nodes) := self
    nodes.Free(a)
    free_nodes.Free(a)
}

VkTlsf.AllocNode(mut self, a: &Alloc, n: VkTlsfNode): OutOfMemory!i32 {
    if self.free_nodes.Len() > 0 {
        id := self.free_nodes.Pop().Must()
        self.nodes[int(id)] = n
        return id
    }
    id := Cast[i32](self.nodes.Len())
    try self.nodes.Push(a, n)
    return id
}

VkTlsf.ReleaseNode(mut self, a: &Alloc, id: i32) {
    self.nodes[int(id)].alive = false
    self.free_nodes.Push(a, id) else {}
}

VkTlsf.InsertFree(mut self, id: i32) {
    sz := self.nodes[int(id)].size
    (fl, sl) := TlsfMap(sz)
    idx := fl * TlsfSlCount + sl
    cur := self.heads[idx]
    self.nodes[int(id)].prev_free = -1
    self.nodes[int(id)].next_free = cur
    if cur >= 0 {
        self.nodes[int(cur)].prev_free = id
    }
    self.heads[idx] = id
    self.fl_bitmap |= u32(1) << Cast[u32](fl)
    self.sl_bitmap[fl] |= u16(1) << Cast[u16](sl)
}

VkTlsf.RemoveFree(mut self, id: i32) {
    sz := self.nodes[int(id)].size
    (fl, sl) := TlsfMap(sz)
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
            self.fl_bitmap &= ~(u32(1) << Cast[u32](fl))
        }
    }
}

VkTlsf.SearchSuitable(&self, size: u64): i32 {
    (fl: fl0, sl: sl0) := TlsfMapSearch(size)
    if fl0 >= TlsfFlMax {
        return -1
    }
    var fl := fl0
    var sl_map := self.sl_bitmap[fl] & (~u16(0) << Cast[u16](sl0))
    if sl_map == 0 {
        fl_map := self.fl_bitmap & (~u32(0) << Cast[u32](fl + 1))
        if fl_map == 0 {
            return -1
        }
        fl = TrailingZeros32(fl_map)
        sl_map = self.sl_bitmap[fl]
    }
    sl := TrailingZeros32(u32(sl_map))
    return self.heads[fl * TlsfSlCount + sl]
}

VkTlsf.FindAligned(&self, size: u64, align: u64): i32 {
    need := if align <= 1 { size } else { size + (align - 1) }
    cand := self.SearchSuitable(need)
    if cand >= 0 {
        return cand
    }
    (fl: fl0, sl: sl0) := TlsfMapSearch(size)
    var fl := fl0
    var sl_start := sl0
    for fl < TlsfFlMax {
        var m := self.sl_bitmap[fl] & (~u16(0) << Cast[u16](sl_start))
        for m != 0 {
            sl := TrailingZeros32(u32(m))
            var cur := self.heads[fl * TlsfSlCount + sl]
            var steps := 0
            for cur >= 0 && steps < 16 {
                off := self.nodes[int(cur)].offset
                pad := AlignUpMem(off, align) - off
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

VkTlsf.Alloc(mut self, a: &Alloc, size: u64, align: u64): OutOfMemory!?(offset: u64, id: i32) {
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
    aligned := AlignUpMem(off, eff_align)
    front_pad := aligned - off
    if front_pad > 0 {
        prev := self.nodes[int(blk)].prev_phys
        pad_id := self.AllocNode(a, VkTlsfNode(
            offset: off, size: front_pad, prev_phys: prev, next_phys: blk,
            prev_free: -1, next_free: -1, used: false, alive: true,
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
    rem := self.nodes[int(blk)].size - size
    if rem > 0 {
        next := self.nodes[int(blk)].next_phys
        tail_id := self.AllocNode(a, VkTlsfNode(
            offset: aligned + size, size: rem, prev_phys: blk, next_phys: next,
            prev_free: -1, next_free: -1, used: false, alive: true,
        )) else e {
            self.InsertFree(blk)
            fail e
        }
        if next >= 0 {
            self.nodes[int(next)].prev_phys = tail_id
        }
        self.nodes[int(blk)].next_phys = tail_id
        self.nodes[int(blk)].size = size
        self.InsertFree(tail_id)
    }
    self.nodes[int(blk)].used = true
    self.free_bytes -= size
    return some((offset: aligned, id: blk))
}

VkTlsf.FreeSpan(mut self, a: &Alloc, id: i32) {
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

FindMemoryType(props: &vulkan.VkPhysicalDeviceMemoryProperties, type_bits: u32, req_flags: u32, pref_flags: u32): ?u32 {
    cnt := int(props.memory_type_count)
    for i in 0..cnt {
        if (type_bits & (u32(1) << Cast[u32](i))) != 0 {
            f := props.memory_types[i].property_flags
            if (f & req_flags) == req_flags && (f & pref_flags) == pref_flags {
                return some(Cast[u32](i))
            }
        }
    }
    for i in 0..cnt {
        if (type_bits & (u32(1) << Cast[u32](i))) != 0 {
            f := props.memory_types[i].property_flags
            if (f & req_flags) == req_flags {
                return some(Cast[u32](i))
            }
        }
    }
    return none
}

AllocRawVkMemory(st: mut VkDeviceState, size: u64, mem_type: u32, ded_buf: u64, ded_img: u64): Error!u64 {
    unsafe "st.device and dev_d.vk_allocate_memory are live" {
        alloc_fn := st.dev_d.vk_allocate_memory.Must()
        var ai := mem.Zeroed[vulkan.VkMemoryAllocateInfo]()
        ai.s_type = vulkan.VkStructureTypeMemoryAllocateInfo
        ai.allocation_size = size
        ai.memory_type_index = mem_type
        var dai := mem.Zeroed[vulkan.VkMemoryDedicatedAllocateInfo]()
        if ded_buf != 0 || ded_img != 0 {
            dai.s_type = vulkan.VkStructureTypeMemoryDedicatedAllocateInfo
            if ded_buf != 0 {
                dai.buffer = raw.FromAddr[vulkan.VkBufferT](Trunc[uint](ded_buf))
            }
            if ded_img != 0 {
                dai.image = raw.FromAddr[vulkan.VkImageT](Trunc[uint](ded_img))
            }
            ai.p_next = raw.PtrCast[vulkan.VkMemoryDedicatedAllocateInfo, void](raw.Of(&dai))
        }
        var vkmem: vulkan.VkDeviceMemory = raw.Null[vulkan.VkDeviceMemoryT]()
        r := alloc_fn(HandleFrom[vulkan.VkDeviceT](st.device), raw.Of(&ai), raw.Null[vulkan.VkAllocationCallbacks](), raw.OfMut(mut vkmem))
        if r != vulkan.VkSuccess {
            if IsDeviceLostResult(r) {
                st.lost = true
            }
            fail MapVkError(r, CallAllocateMemory)
        }
        st.live_native += 1
        return HandleAddr(vkmem)
    }
}

FreeRawVkMemory(st: mut VkDeviceState, vkmem: u64) {
    if vkmem == 0 {
        return
    }
    unsafe "st.device and dev_d.vk_free_memory are live" {
        free_fn := st.dev_d.vk_free_memory.Must()
        free_fn(HandleFrom[vulkan.VkDeviceT](st.device), HandleFrom[vulkan.VkDeviceMemoryT](vkmem), raw.Null[vulkan.VkAllocationCallbacks]())
        if st.live_native > 0 {
            st.live_native -= 1
        }
    }
}

MapRawVkMemory(st: mut VkDeviceState, vkmem: u64, size: u64): Error!uint {
    unsafe "st.device and dev_d.vk_map_memory are live" {
        map_fn := st.dev_d.vk_map_memory.Must()
        var ptr: *void = raw.Null[void]()
        r := map_fn(HandleFrom[vulkan.VkDeviceT](st.device), HandleFrom[vulkan.VkDeviceMemoryT](vkmem), 0, size, 0, raw.OfMut(mut ptr))
        if r != vulkan.VkSuccess {
            if IsDeviceLostResult(r) {
                st.lost = true
            }
            fail MapVkError(r, CallMap)
        }
        return raw.Addr(ptr)
    }
}

UnmapRawVkMemory(st: &VkDeviceState, vkmem: u64) {
    if vkmem == 0 {
        return
    }
    unsafe "st.device and dev_d.vk_unmap_memory are live" {
        st.dev_d.vk_unmap_memory.Must()(HandleFrom[vulkan.VkDeviceT](st.device), HandleFrom[vulkan.VkDeviceMemoryT](vkmem))
    }
}

AllocDeviceMem(
    a: &Alloc into st,
    st: mut VkDeviceState,
    size: u64,
    align: u64,
    mem_type: u32,
    is_image: bool,
    host_visible: bool,
    force_dedicated: bool,
    ded_buf: u64,
    ded_img: u64,
): Error!VkMemAlloc {
    if force_dedicated || size >= VkDedicatedThreshold {
        vkmem := try AllocRawVkMemory(st, size, mem_type, ded_buf, ded_img)
        var mptr: uint = 0
        if host_visible {
            mptr = MapRawVkMemory(st, vkmem, size) else e {
                FreeRawVkMemory(st, vkmem)
                fail e
            }
        }
        return VkMemAlloc(mem_type: mem_type, block_idx: -1, span_id: -1, dedicated_mem: vkmem, offset: 0, size: size, mapped_ptr: mptr)
    }

    for bi in 0..st.mem_blocks.Len() {
        blk := &st.mem_blocks[bi]
        if !blk.alive || blk.mem_type != mem_type || blk.is_image_block != is_image {
            continue
        }
        tlsf := mut st.tlsf_allocs.ItemsMut()[blk.tlsf_idx]
        match try tlsf.Alloc(a, size, align) {
            some(sp) {
                mptr := if blk.mapped_ptr != 0 { blk.mapped_ptr + Trunc[uint](sp.offset) } else { uint(0) }
                return VkMemAlloc(mem_type: mem_type, block_idx: bi, span_id: sp.id, dedicated_mem: 0, offset: sp.offset, size: size, mapped_ptr: mptr)
            }
            none {}
        }
    }

    blk_size := if size > VkBlockSize { size } else { VkBlockSize }
    vkmem := try AllocRawVkMemory(st, blk_size, mem_type, 0, 0)
    var mptr: uint = 0
    if host_visible {
        mptr = MapRawVkMemory(st, vkmem, blk_size) else e {
            FreeRawVkMemory(st, vkmem)
            fail e
        }
    }
    var tlsf := VkTlsf.New(a, blk_size) else e {
        if mptr != 0 { UnmapRawVkMemory(&*st, vkmem) }
        FreeRawVkMemory(st, vkmem)
        fail e
    }
    sp_opt := tlsf.Alloc(a, size, align) else e {
        tlsf.Free(a)
        if mptr != 0 { UnmapRawVkMemory(&*st, vkmem) }
        FreeRawVkMemory(st, vkmem)
        fail e
    }
    sp := sp_opt.Must()
    tidx := st.tlsf_allocs.Len()
    st.tlsf_allocs.Push(a, tlsf) else e {
        tlsf.Free(a)
        if mptr != 0 { UnmapRawVkMemory(&*st, vkmem) }
        FreeRawVkMemory(st, vkmem)
        fail e
    }
    bidx := st.mem_blocks.Len()
    st.mem_blocks.Push(a, VkMemBlock(
        alive: true, mem_type: mem_type, is_image_block: is_image,
        memory: vkmem, size: blk_size, mapped_ptr: mptr, tlsf_idx: tidx,
    )) else e {
        _ = st.tlsf_allocs.Pop().Must().Free(a)
        if mptr != 0 { UnmapRawVkMemory(&*st, vkmem) }
        FreeRawVkMemory(st, vkmem)
        fail e
    }
    alloc_ptr := if mptr != 0 { mptr + Trunc[uint](sp.offset) } else { uint(0) }
    return VkMemAlloc(mem_type: mem_type, block_idx: bidx, span_id: sp.id, dedicated_mem: 0, offset: sp.offset, size: size, mapped_ptr: alloc_ptr)
}

FreeDeviceMem(a: &Alloc, st: mut VkDeviceState, al: VkMemAlloc) {
    if al.dedicated_mem != 0 {
        if al.mapped_ptr != 0 {
            UnmapRawVkMemory(&*st, al.dedicated_mem)
        }
        FreeRawVkMemory(st, al.dedicated_mem)
        return
    }
    if al.block_idx >= 0 && al.block_idx < st.mem_blocks.Len() {
        blk := &st.mem_blocks[al.block_idx]
        if blk.alive {
            st.tlsf_allocs.ItemsMut()[blk.tlsf_idx].FreeSpan(a, al.span_id)
        }
    }
}

FreeAllDeviceMemBlocks(a: &Alloc, st: mut VkDeviceState) {
    for i in 0..st.mem_blocks.Len() {
        blk := &st.mem_blocks[i]
        if blk.alive {
            if blk.mapped_ptr != 0 {
                UnmapRawVkMemory(&*st, blk.memory)
            }
            FreeRawVkMemory(st, blk.memory)
            st.mem_blocks[i].alive = false
        }
    }
    for st.tlsf_allocs.Len() > 0 {
        st.tlsf_allocs.Pop().Must().Free(a)
    }
}

FlushOrInvalidateHostMem(st: &VkDeviceState, al: &VkMemAlloc, invalidate: bool) {
    if (st.mem_props.memory_types[int(al.mem_type)].property_flags & Cast[u32](vulkan.VkMemoryPropertyHostCoherentBit)) != 0 {
        return
    }
    vkmem := if al.dedicated_mem != 0 { al.dedicated_mem } else { st.mem_blocks[al.block_idx].memory }
    atom := st.mem_props.memory_types[int(al.mem_type)].property_flags
    _ = atom
    var rng := mem.Zeroed[vulkan.VkMappedMemoryRange]()
    rng.s_type = vulkan.VkStructureTypeMappedMemoryRange
    unsafe "vkmem is a live mapped VkDeviceMemory" {
        rng.memory = HandleFrom[vulkan.VkDeviceMemoryT](vkmem)
        rng.offset = al.offset
        rng.size = vulkan.VkWholeSize
        if invalidate {
            _ = st.dev_d.vk_invalidate_mapped_memory_ranges.Must()(HandleFrom[vulkan.VkDeviceT](st.device), 1, raw.Of(&rng))
        } else {
            _ = st.dev_d.vk_flush_mapped_memory_ranges.Must()(HandleFrom[vulkan.VkDeviceT](st.device), 1, raw.Of(&rng))
        }
    }
}
