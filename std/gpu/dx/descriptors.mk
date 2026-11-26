// Why: D3D12 bind groups, RTVs, DSVs, and placed buffer heaps need fast
// aligned suballocation, coalescing free lists, and sampler-table caching
// within the 2,048 shader-visible sampler heap limit.
//
// What: SpanAllocator, DescHeap, CreateDescHeap, and Device sampler-range
// acquisition and release.
//
// How: maintains sorted non-overlapping free spans with immediate neighbour
// coalescing, wraps ID3D12DescriptorHeap creation/destruction in MkSystemRun,
// and deduplicates shader-visible sampler tables by sampler handle tuple.

import c.windows
import c.d3d12
import std.raw
import std.mem

SpanAllocator.New(a: &Alloc, total: u64): OutOfMemory!SpanAllocator {
    var fl := HeapList[Span].New(a)
    if total > 0 {
        fl.Push(a, Span(offset: 0, size: total)) else e {
            fl.Free(a)
            fail e
        }
    }
    return SpanAllocator(total: total, free_list: fl)
}

SpanAllocator.FreeAll(self, a: &Alloc) {
    SpanAllocator(total: _, free_list) := self
    free_list.Free(a)
}

SpanAllocator.Alloc(mut self, a: &Alloc, size: u64, align: u64): ?u64 {
    if size == 0 {
        return some(0)
    }
    al := if align > 1 { align } else { u64(1) }
    n := self.free_list.Len()
    for i in 0..n {
        sp := self.free_list[i]
        mask := al - 1
        aligned := (sp.offset + mask) & ~mask
        if aligned >= sp.offset {
            pad := aligned - sp.offset
            if sp.size >= pad + size {
                end_sp := sp.offset + sp.size
                rem := end_sp - (aligned + size)
                if pad > 0 && rem > 0 {
                    self.free_list[i] = Span(offset: sp.offset, size: pad)
                    self.free_list.Insert(a, i + 1, Span(offset: aligned + size, size: rem)) else {
                        self.free_list[i] = sp
                        return none
                    }
                } else if pad > 0 {
                    self.free_list[i] = Span(offset: sp.offset, size: pad)
                } else if rem > 0 {
                    self.free_list[i] = Span(offset: aligned + size, size: rem)
                } else {
                    _ = self.free_list.Remove(i)
                }
                return some(aligned)
            }
        }
    }
    return none
}

SpanAllocator.Free(mut self, a: &Alloc, offset: u64, size: u64) {
    if size == 0 {
        return
    }
    var ins := 0
    n := self.free_list.Len()
    for ins < n && self.free_list[ins].offset < offset {
        ins += 1
    }
    var new_off := offset
    var new_sz := size
    if ins > 0 {
        prev := self.free_list[ins - 1]
        if prev.offset + prev.size == new_off {
            new_off = prev.offset
            new_sz += prev.size
            _ = self.free_list.Remove(ins - 1)
            ins -= 1
        }
    }
    if ins < self.free_list.Len() {
        nxt := self.free_list[ins]
        if new_off + new_sz == nxt.offset {
            new_sz += nxt.size
            self.free_list[ins] = Span(offset: new_off, size: new_sz)
            return
        }
    }
    self.free_list.Insert(a, ins, Span(offset: new_off, size: new_sz)) else {}
}

struct CreateDescHeapArgs {
    dev: *d3d12.ID3D12Device
    typ: d3d12.D3d12DescriptorHeapType
    count: u64
    shader_visible: bool
    out_heap: *d3d12.ID3D12DescriptorHeap
    cpu_start: u64
    gpu_start: u64
    inc_size: u64
    hr: windows.Hresult
}

CreateDescHeapSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to live CreateDescHeapArgs on caller frame" {
        p := raw.PtrCast[u8, CreateDescHeapArgs](arg)
        var desc := mem.Zeroed[d3d12.D3d12DescriptorHeapDesc]()
        desc.type_ = (*p).typ
        desc.num_descriptors = Cast[windows.Uint]((*p).count)
        desc.flags = if (*p).shader_visible {
            d3d12.D3d12DescriptorHeapFlagShaderVisible
        } else {
            d3d12.D3d12DescriptorHeapFlagNone
        }
        var hp_ptr: *void = raw.Null[void]()
        hr := (*(*p).dev).CreateDescriptorHeap(raw.Of(&desc), raw.Of(&d3d12.IidID3D12DescriptorHeap), raw.OfMut(mut hp_ptr))
        (*p).hr = hr
        if hr >= 0 && !raw.IsNull(hp_ptr) {
            hp := raw.PtrCast[void, d3d12.ID3D12DescriptorHeap](hp_ptr)
            (*p).out_heap = hp
            (*p).inc_size = u64((*(*p).dev).GetDescriptorHandleIncrementSize((*p).typ))
            var cpu := mem.Zeroed[d3d12.D3d12CpuDescriptorHandle]()
            _ = (*hp).GetCpuDescriptorHandleForHeapStart(raw.OfMut(mut cpu))
            (*p).cpu_start = cpu.ptr
            if (*p).shader_visible {
                var gpu := mem.Zeroed[d3d12.D3d12GpuDescriptorHandle]()
                _ = (*hp).GetGpuDescriptorHandleForHeapStart(raw.OfMut(mut gpu))
                (*p).gpu_start = gpu.ptr
            }
        }
    }
}

CreateDescHeap(a: &Alloc, dev: *d3d12.ID3D12Device, typ: d3d12.D3d12DescriptorHeapType, count: u64, shader_visible: bool): Error!DescHeap {
    if Target.os != Os.Windows {
        fail Unsupported
    }
    var args := CreateDescHeapArgs(
        dev: dev,
        typ: typ,
        count: count,
        shader_visible: shader_visible,
        out_heap: raw.Null[d3d12.ID3D12DescriptorHeap](),
        cpu_start: 0,
        gpu_start: 0,
        inc_size: 0,
        hr: 0,
    )
    SysRun(mut args, CreateDescHeapSys)
    if args.hr < 0 || raw.IsNull(args.out_heap) {
        if args.hr == HresultOutOfMemory {
            fail DxFail(kind: ErrKind.OutOfDeviceMemory, code: i64(args.hr), call: CallCreateHeap, at: -1)
        }
        fail DxFail(kind: ErrKind.Driver, code: i64(args.hr), call: CallCreateHeap, at: -1)
    }
    alloc := SpanAllocator.New(a, count) else e {
        ReleaseDescHeapPtr(args.out_heap)
        fail e
    }
    return DescHeap(
        heap: args.out_heap,
        cpu_start: args.cpu_start,
        gpu_start: args.gpu_start,
        inc_size: args.inc_size,
        capacity: count,
        alloc: alloc,
    )
}

ReleaseDescHeapSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to *ID3D12DescriptorHeap" {
        pp := raw.PtrCast[u8, *d3d12.ID3D12DescriptorHeap](arg)
        hp := raw.Read(pp)
        if !raw.IsNull(hp) {
            _ = (*hp).Release()
        }
    }
}

ReleaseDescHeapPtr(hp: *d3d12.ID3D12DescriptorHeap) {
    if Target.os != Os.Windows || raw.IsNull(hp) {
        return
    }
    var p := hp
    SysRun(mut p, ReleaseDescHeapSys)
}

DescHeap.Close(self, a: &Alloc) {
    DescHeap(heap, cpu_start: _, gpu_start: _, inc_size: _, capacity: _, alloc) := self
    ReleaseDescHeapPtr(heap)
    alloc.FreeAll(a)
}

DescHeap.CpuAt(&self, idx: u64): d3d12.D3d12CpuDescriptorHandle {
    return d3d12.D3d12CpuDescriptorHandle(ptr: self.cpu_start + idx * self.inc_size)
}

DescHeap.GpuAt(&self, idx: u64): d3d12.D3d12GpuDescriptorHandle {
    return d3d12.D3d12GpuDescriptorHandle(ptr: self.gpu_start + idx * self.inc_size)
}

DescHeap.AllocRange(mut self, a: &Alloc, count: u64): ?u64 {
    return self.alloc.Alloc(a, count, 1)
}

DescHeap.FreeRange(mut self, a: &Alloc, idx: u64, count: u64) {
    self.alloc.Free(a, idx, count)
}

struct WriteSamplersArgs {
    dev: *d3d12.ID3D12Device
    nhs: *u64
    count: int
    cpu_start: u64
    inc_size: u64
}

WriteSamplersSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to live WriteSamplersArgs and nhs points to count live DxSampler pointers" {
        p := raw.PtrCast[u8, WriteSamplersArgs](arg)
        nhs := raw.Slice((*p).nhs, (*p).count)
        for i in 0..(*p).count {
            sp := raw.FromAddr[DxSampler](Trunc[uint](nhs[i]))
            var desc := (*sp).desc
            h := d3d12.D3d12CpuDescriptorHandle(ptr: (*p).cpu_start + Cast[u64](i) * (*p).inc_size)
            (*(*p).dev).CreateSampler(raw.Of(&desc), h)
        }
    }
}

Device.AcquireSamplerRange(mut self, sampler_nhs: []u64): Error!(cache_idx: int, gpu_ptr: u64) {
    if Target.os != Os.Windows {
        fail Unsupported
    }
    n := sampler_nhs.Len()
    if n == 0 {
        return (cache_idx: -1, gpu_ptr: 0)
    }
    var found_idx := -1
    var found_off: u64 = 0
    for i in 0..self.sampler_cache.Len() {
        e := &self.sampler_cache[i]
        if e.ref_count > 0 && e.count == n {
            var same := true
            for k in 0..n {
                if e.sampler_nhs[k] != sampler_nhs[k] {
                    same = false
                    break
                }
            }
            if same {
                found_idx = i
                found_off = u64(e.offset)
                break
            }
        }
    }
    if found_idx >= 0 {
        self.sampler_cache[found_idx].ref_count += 1
        gpu := self.sampler_heap.GpuAt(found_off).ptr
        return (cache_idx: found_idx, gpu_ptr: gpu)
    }
    a := self.heap.Alloc()
    off := self.sampler_heap.AllocRange(a, Cast[u64](n)) else {
        fail DxFail(kind: ErrKind.LimitExceeded, code: 0, call: CallCreateHeap, at: -1)
    }
    var arr: [16]u64 = [0; 16]
    for k in 0..n {
        arr[k] = sampler_nhs[k]
    }
    var wargs := WriteSamplersArgs(
        dev: self.dev,
        nhs: raw.Data(sampler_nhs),
        count: n,
        cpu_start: self.sampler_heap.CpuAt(off).ptr,
        inc_size: self.sampler_heap.inc_size,
    )
    SysRun(mut wargs, WriteSamplersSys)

    var reuse := -1
    for i in 0..self.sampler_cache.Len() {
        if self.sampler_cache[i].ref_count == 0 {
            reuse = i
            break
        }
    }
    entry := SamplerCacheEntry(count: n, sampler_nhs: arr, offset: Cast[u32](off), ref_count: 1)
    if reuse >= 0 {
        self.sampler_cache[reuse] = entry
    } else {
        self.sampler_cache.Push(a, entry) else e {
            self.sampler_heap.FreeRange(a, off, Cast[u64](n))
            fail e
        }
        reuse = self.sampler_cache.Len() - 1
    }
    return (cache_idx: reuse, gpu_ptr: self.sampler_heap.GpuAt(off).ptr)
}

Device.ReleaseSamplerRange(mut self, cache_idx: int) {
    if cache_idx < 0 || cache_idx >= self.sampler_cache.Len() {
        return
    }
    if self.sampler_cache[cache_idx].ref_count > 0 {
        self.sampler_cache[cache_idx].ref_count -= 1
        if self.sampler_cache[cache_idx].ref_count == 0 {
            a := self.heap.Alloc()
            off := u64(self.sampler_cache[cache_idx].offset)
            cnt := Cast[u64](self.sampler_cache[cache_idx].count)
            self.sampler_heap.FreeRange(a, off, cnt)
        }
    }
}
