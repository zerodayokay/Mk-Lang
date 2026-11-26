// Why: D3D12 buffers must be placed in 64 MiB DEFAULT heaps (with +64 KiB pad
// for dynamic uniforms) and zeroed on creation, while textures, views, samplers,
// and bind groups need exact DXGI format, DSV read-only, and descriptor writes.
//
// What: Device.CreateBuffer, DestroyBuffer, CreateTexture, DestroyTexture,
// CreateView, DestroyView, CreateSampler, DestroySampler, CreateBindGroup,
// DestroyBindGroup, and helper allocators.
//
// How: wraps every ID3D12Device/Resource call in MkSystemRun, suballocates
// placed buffers via SpanAllocator, queues zeroing work in pending_zeros,
// and writes CBV/SRV/UAV/RTV/DSV descriptors directly into descriptor heaps.

import c.windows
import c.dxgi
import c.d3d12
import std.raw
import std.mem

DefaultHeapProps(typ: d3d12.D3d12HeapType): d3d12.D3d12HeapProperties {
    var hp := mem.Zeroed[d3d12.D3d12HeapProperties]()
    hp.type_ = typ
    return hp
}

BufferResourceDesc(size: u64, flags: d3d12.D3d12ResourceFlags): d3d12.D3d12ResourceDesc {
    var rd := mem.Zeroed[d3d12.D3d12ResourceDesc]()
    rd.dimension = d3d12.D3d12ResourceDimensionBuffer
    rd.alignment = 0
    rd.width = size
    rd.height = 1
    rd.depth_or_array_size = 1
    rd.mip_levels = 1
    rd.format = dxgi.DxgiFormatUnknown
    rd.sample_desc.count = 1
    rd.sample_desc.quality = 0
    rd.layout = d3d12.D3d12TextureLayoutRowMajor
    rd.flags = flags
    return rd
}

TextureResourceDesc(spec: &TextureSpec): d3d12.D3d12ResourceDesc {
    var rd := mem.Zeroed[d3d12.D3d12ResourceDesc]()
    rd.dimension = d3d12.D3d12ResourceDimensionTexture2d
    rd.alignment = 0
    rd.width = Cast[u64](spec.width)
    rd.height = Cast[windows.Uint](spec.height)
    rd.depth_or_array_size = Cast[windows.Uint16](spec.layers)
    rd.mip_levels = Cast[windows.Uint16](spec.mips)
    rd.format = Cast[dxgi.DxgiFormat](spec.dxgi_res)
    rd.sample_desc.count = Cast[windows.Uint](spec.samples)
    rd.sample_desc.quality = 0
    rd.layout = d3d12.D3d12TextureLayoutUnknown
    var flags := d3d12.D3d12ResourceFlagNone
    if spec.render_target {
        if spec.is_depth {
            flags |= d3d12.D3d12ResourceFlagAllowDepthStencil
            if !spec.sampled {
                flags |= d3d12.D3d12ResourceFlagDenyShaderResource
            }
        } else {
            flags |= d3d12.D3d12ResourceFlagAllowRenderTarget
        }
    }
    rd.flags = flags
    return rd
}

struct CreateHeapArgs {
    dev: *d3d12.ID3D12Device
    size: u64
    flags: d3d12.D3d12HeapFlags
    out_heap: *d3d12.ID3D12Heap
    hr: windows.Hresult
}

CreateHeapSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to live CreateHeapArgs" {
        p := raw.PtrCast[u8, CreateHeapArgs](arg)
        var desc := mem.Zeroed[d3d12.D3d12HeapDesc]()
        desc.size_in_bytes = (*p).size
        desc.properties = DefaultHeapProps(d3d12.D3d12HeapTypeDefault)
        desc.alignment = 65536
        desc.flags = (*p).flags
        var hp_ptr: *void = raw.Null[void]()
        hr := (*(*p).dev).CreateHeap(raw.Of(&desc), raw.Of(&d3d12.IidID3D12Heap), raw.OfMut(mut hp_ptr))
        (*p).hr = hr
        if hr >= 0 && !raw.IsNull(hp_ptr) {
            (*p).out_heap = raw.PtrCast[void, d3d12.ID3D12Heap](hp_ptr)
        }
    }
}

ReleaseHeapSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to *ID3D12Heap" {
        pp := raw.PtrCast[u8, *d3d12.ID3D12Heap](arg)
        hp := raw.Read(pp)
        if !raw.IsNull(hp) {
            _ = (*hp).Release()
        }
    }
}

ReleaseHeapPtr(hp: *d3d12.ID3D12Heap) {
    if Target.os != Os.Windows || raw.IsNull(hp) {
        return
    }
    var p := hp
    SysRun(mut p, ReleaseHeapSys)
}

struct CreateResArgs {
    dev: *d3d12.ID3D12Device
    placed: bool
    heap: *d3d12.ID3D12Heap
    heap_off: u64
    heap_type: d3d12.D3d12HeapType
    desc: d3d12.D3d12ResourceDesc
    init_state: d3d12.D3d12ResourceStates
    has_clear: bool
    clear_val: d3d12.D3d12ClearValue
    map_read_or_write: bool
    is_readback: bool
    out_res: *d3d12.ID3D12Resource
    gpu_va: u64
    mapped: *u8
    hr: windows.Hresult
}

CreateResSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to live CreateResArgs" {
        p := raw.PtrCast[u8, CreateResArgs](arg)
        var res_ptr: *void = raw.Null[void]()
        var cv_ptr := if (*p).has_clear {
            raw.Of(&(*p).clear_val)
        } else {
            raw.Null[d3d12.D3d12ClearValue]()
        }
        var hr: windows.Hresult = 0
        if (*p).placed {
            hr = (*(*p).dev).CreatePlacedResource(
                (*p).heap,
                (*p).heap_off,
                raw.Of(&(*p).desc),
                (*p).init_state,
                cv_ptr,
                raw.Of(&d3d12.IidID3D12Resource),
                raw.OfMut(mut res_ptr),
            )
        } else {
            hp := DefaultHeapProps((*p).heap_type)
            hr = (*(*p).dev).CreateCommittedResource(
                raw.Of(&hp),
                d3d12.D3d12HeapFlagNone,
                raw.Of(&(*p).desc),
                (*p).init_state,
                cv_ptr,
                raw.Of(&d3d12.IidID3D12Resource),
                raw.OfMut(mut res_ptr),
            )
        }
        (*p).hr = hr
        if hr >= 0 && !raw.IsNull(res_ptr) {
            res := raw.PtrCast[void, d3d12.ID3D12Resource](res_ptr)
            (*p).out_res = res
            if (*p).desc.dimension == d3d12.D3d12ResourceDimensionBuffer {
                (*p).gpu_va = (*res).GetGpuVirtualAddress()
            }
            if (*p).map_read_or_write {
                var empty_r := mem.Zeroed[d3d12.D3d12Range]()
                var r_ptr := if !(*p).is_readback {
                    raw.Of(&empty_r)
                } else {
                    raw.Null[d3d12.D3d12Range]()
                }
                var m_ptr: *void = raw.Null[void]()
                mhr := (*res).Map(0, r_ptr, raw.OfMut(mut m_ptr))
                if mhr >= 0 && !raw.IsNull(m_ptr) {
                    (*p).mapped = raw.PtrCast[void, u8](m_ptr)
                } else {
                    _ = (*res).Release()
                    (*p).out_res = raw.Null[d3d12.ID3D12Resource]()
                    (*p).hr = mhr
                }
            }
        }
    }
}

struct DestroyResArgs {
    res: *d3d12.ID3D12Resource
    unmap: bool
}

DestroyResSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to live DestroyResArgs" {
        p := raw.PtrCast[u8, DestroyResArgs](arg)
        if !raw.IsNull((*p).res) {
            if (*p).unmap {
                var empty_r := mem.Zeroed[d3d12.D3d12Range]()
                (*(*p).res).Unmap(0, raw.Of(&empty_r))
            }
            _ = (*(*p).res).Release()
        }
    }
}

ReleaseResPtr(res: *d3d12.ID3D12Resource, unmap: bool) {
    if Target.os != Os.Windows || raw.IsNull(res) {
        return
    }
    var args := DestroyResArgs(res: res, unmap: unmap)
    SysRun(mut args, DestroyResSys)
}

Device.AllocPlacedBufferSlot(mut self, alloc_size: u64): Error!(heap_idx: i32, offset: u64) {
    a := self.heap.Alloc()
    aligned_sz := (alloc_size + 65535) & ~u64(65535)
    for i in 0..self.buf_heaps.Len() {
        anh := self.buf_heaps[i].alloc_nh
        unsafe "anh is a live SpanAllocator pointer" {
            sa := raw.FromAddr[SpanAllocator](Trunc[uint](anh))
            off_opt := (*sa).Alloc(a, aligned_sz, 65536)
            match off_opt {
                some(off) { return (heap_idx: Cast[i32](i), offset: off) }
                none {}
            }
        }
    }
    var hargs := CreateHeapArgs(
        dev: self.dev,
        size: BufferHeapBlockBytes,
        flags: d3d12.D3d12HeapFlagAllowOnlyBuffers,
        out_heap: raw.Null[d3d12.ID3D12Heap](),
        hr: 0,
    )
    SysRun(mut hargs, CreateHeapSys)
    _ = self.DrainInfoQueue()
    if hargs.hr < 0 || raw.IsNull(hargs.out_heap) {
        fail MapHrError(self, hargs.hr, CallCreateHeap)
    }
    slot := AllocRecSlot[SpanAllocator](a) else e {
        ReleaseHeapPtr(hargs.out_heap)
        fail e
    }
    var salloc := SpanAllocator.New(a, BufferHeapBlockBytes) else e {
        unsafe "slot was reserved before SpanAllocator.New" {
            a.FreeBytes(raw.PtrCast[SpanAllocator, u8](slot), SizeOf[SpanAllocator](), AlignOf[SpanAllocator]())
        }
        ReleaseHeapPtr(hargs.out_heap)
        fail e
    }
    off := salloc.Alloc(a, aligned_sz, 65536).Must()
    anh := WriteRecSlot(slot, salloc)
    self.buf_heaps.Push(a, PlacedHeap(heap: hargs.out_heap, size: BufferHeapBlockBytes, alloc_nh: anh)) else e {
        s := FreeRec[SpanAllocator](a, anh)
        s.FreeAll(a)
        ReleaseHeapPtr(hargs.out_heap)
        fail e
    }
    return (heap_idx: Cast[i32](self.buf_heaps.Len() - 1), offset: off)
}

/// Creates a D3D12 buffer, zeroing it at creation (CPU fill for map_write,
/// GPU zero-buffer copy queued in pending_zeros for DEFAULT/map_read).
export Device.CreateBuffer(mut self, spec: &BufferSpec): Error!u64 {
    if Target.os != Os.Windows {
        fail Unsupported
    }
    if self.lost {
        fail DxFail(kind: self.loss_reason, code: 0, call: CallCreateResource, at: -1)
    }
    var sz := Cast[u64](spec.size)
    if spec.uniform_dynamic {
        sz += 65536
    }
    if spec.uniform || spec.uniform_dynamic {
        sz = (sz + 255) & ~u64(255)
    } else if spec.storage {
        sz = (sz + 3) & ~u64(3)
    }
    if sz == 0 {
        sz = 4
    }

    flags := if spec.storage {
        d3d12.D3d12ResourceFlagAllowUnorderedAccess
    } else {
        d3d12.D3d12ResourceFlagNone
    }
    desc := BufferResourceDesc(sz, flags)
    is_map := spec.map_write || spec.map_read
    var placed := !is_map && sz <= BufferHeapBlockBytes
    var heap_idx: i32 = -1
    var heap_off: u64 = 0
    var heap_ptr := raw.Null[d3d12.ID3D12Heap]()
    if placed {
        slot_r := try self.AllocPlacedBufferSlot(sz)
        heap_idx = slot_r.heap_idx
        heap_off = slot_r.offset
        heap_ptr = self.buf_heaps[int(heap_idx)].heap
    }
    heap_type := if spec.map_write {
        d3d12.D3d12HeapTypeUpload
    } else if spec.map_read {
        d3d12.D3d12HeapTypeReadback
    } else {
        d3d12.D3d12HeapTypeDefault
    }
    init_state := if spec.map_write {
        d3d12.D3d12ResourceStateGenericRead
    } else if spec.map_read {
        d3d12.D3d12ResourceStateCopyDest
    } else {
        d3d12.D3d12ResourceStateCommon
    }

    var args := CreateResArgs(
        dev: self.dev,
        placed: placed,
        heap: heap_ptr,
        heap_off: heap_off,
        heap_type: heap_type,
        desc: desc,
        init_state: init_state,
        has_clear: false,
        clear_val: mem.Zeroed[d3d12.D3d12ClearValue](),
        map_read_or_write: is_map,
        is_readback: spec.map_read,
        out_res: raw.Null[d3d12.ID3D12Resource](),
        gpu_va: 0,
        mapped: raw.Null[u8](),
        hr: 0,
    )
    SysRun(mut args, CreateResSys)
    _ = self.DrainInfoQueue()
    if args.hr < 0 || raw.IsNull(args.out_res) {
        if heap_idx >= 0 {
            aligned_sz := (sz + 65535) & ~u64(65535)
            anh := self.buf_heaps[int(heap_idx)].alloc_nh
            unsafe "anh is live SpanAllocator" {
                (*raw.FromAddr[SpanAllocator](Trunc[uint](anh))).Free(self.heap.Alloc(), heap_off, aligned_sz)
            }
        }
        fail MapHrError(self, args.hr, CallCreateResource)
    }
    SetResourceName(args.out_res, spec.label)

    a := self.heap.Alloc()
    if spec.map_write {
        unsafe "mapped points to sz bytes of persistently mapped UPLOAD buffer" {
            raw.Fill(args.mapped, 0, Cast[int](sz))
        }
    } else {
        self.pending_zeros.Push(a, PendingZero(
            kind: 0,
            res: args.out_res,
            size: sz,
            mips: 1,
            layers: 1,
            planes: 1,
            width: 0,
            height: 0,
            dxgi_fmt: 0,
            dxgi_rtv_dsv: 0,
            block_bytes: 0,
            block_w: 1,
            block_h: 1,
            samples: 1,
        )) else e {
            ReleaseResPtr(args.out_res, is_map)
            if heap_idx >= 0 {
                aligned_sz := (sz + 65535) & ~u64(65535)
                anh := self.buf_heaps[int(heap_idx)].alloc_nh
                unsafe "anh is live SpanAllocator" {
                    (*raw.FromAddr[SpanAllocator](Trunc[uint](anh))).Free(a, heap_off, aligned_sz)
                }
            }
            fail e
        }
    }

    rec := DxBuffer(
        res: args.out_res,
        gpu_va: args.gpu_va,
        mapped: args.mapped,
        alloc_size: sz,
        user_size: Cast[u64](spec.size),
        heap_idx: heap_idx,
        heap_off: heap_off,
        is_upload: spec.map_write,
        is_readback: spec.map_read,
    )
    return AllocRec(a, rec) else e {
        ReleaseResPtr(args.out_res, is_map)
        if heap_idx >= 0 {
            aligned_sz := (sz + 65535) & ~u64(65535)
            anh := self.buf_heaps[int(heap_idx)].alloc_nh
            unsafe "anh is live SpanAllocator" {
                (*raw.FromAddr[SpanAllocator](Trunc[uint](anh))).Free(a, heap_off, aligned_sz)
            }
        }
        fail e
    }
}

/// Releases a D3D12 buffer and returns any placed heap span.
export Device.DestroyBuffer(mut self, nh: u64) {
    if Target.os != Os.Windows || nh == 0 {
        return
    }
    a := self.heap.Alloc()
    rec := FreeRec[DxBuffer](a, nh)
    ReleaseResPtr(rec.res, rec.is_upload || rec.is_readback)
    if rec.heap_idx >= 0 && int(rec.heap_idx) < self.buf_heaps.Len() {
        aligned_sz := (rec.alloc_size + 65535) & ~u64(65535)
        anh := self.buf_heaps[int(rec.heap_idx)].alloc_nh
        unsafe "anh is live SpanAllocator" {
            (*raw.FromAddr[SpanAllocator](Trunc[uint](anh))).Free(a, rec.heap_off, aligned_sz)
        }
    }
}

/// Returns the persistently mapped byte slice of a map_read or map_write buffer.
export Device.BufferMappedSlice(&self, nh: u64, at: int, n: int): []u8 from self {
    _ = self
    if Target.os != Os.Windows || nh == 0 {
        return "".AsBytes()
    }
    unsafe "nh is a live mapped DxBuffer and [at..at+n] is within user_size" {
        bp := raw.FromAddr[DxBuffer](Trunc[uint](nh))
        return raw.Slice(raw.Offset((*bp).mapped, at), n)
    }
}

/// Returns the mutable persistently mapped byte slice of a map_write buffer.
export Device.BufferMappedSliceMut(mut self, nh: u64, at: int, n: int): mut []u8 from self {
    _ = self
    unsafe "nh is a live mapped DxBuffer and [at..at+n] is within user_size" {
        bp := raw.FromAddr[DxBuffer](Trunc[uint](nh))
        return raw.SliceMut(raw.Offset((*bp).mapped, at), n)
    }
}

/// Creates a D3D12 2D texture and queues GPU zeroing at creation.
export Device.CreateTexture(mut self, spec: &TextureSpec): Error!u64 {
    if Target.os != Os.Windows {
        fail Unsupported
    }
    if self.lost {
        fail DxFail(kind: self.loss_reason, code: 0, call: CallCreateResource, at: -1)
    }
    desc := TextureResourceDesc(spec)
    var cv := mem.Zeroed[d3d12.D3d12ClearValue]()
    if spec.render_target {
        cv.format = Cast[dxgi.DxgiFormat](spec.dxgi_rtv_dsv)
    }
    var args := CreateResArgs(
        dev: self.dev,
        placed: false,
        heap: raw.Null[d3d12.ID3D12Heap](),
        heap_off: 0,
        heap_type: d3d12.D3d12HeapTypeDefault,
        desc: desc,
        init_state: d3d12.D3d12ResourceStateCommon,
        has_clear: spec.render_target,
        clear_val: cv,
        map_read_or_write: false,
        is_readback: false,
        out_res: raw.Null[d3d12.ID3D12Resource](),
        gpu_va: 0,
        mapped: raw.Null[u8](),
        hr: 0,
    )
    SysRun(mut args, CreateResSys)
    _ = self.DrainInfoQueue()
    if args.hr < 0 || raw.IsNull(args.out_res) {
        fail MapHrError(self, args.hr, CallCreateResource)
    }
    SetResourceName(args.out_res, spec.label)

    a := self.heap.Alloc()
    zkind := if spec.render_target && spec.is_depth {
        u8(3)
    } else if spec.render_target {
        u8(2)
    } else {
        u8(1)
    }
    self.pending_zeros.Push(a, PendingZero(
        kind: zkind,
        res: args.out_res,
        size: 0,
        mips: Cast[u8](spec.mips),
        layers: Cast[u16](spec.layers),
        planes: Cast[u8](spec.planes),
        width: spec.width,
        height: spec.height,
        dxgi_fmt: spec.dxgi_res,
        dxgi_rtv_dsv: spec.dxgi_rtv_dsv,
        block_bytes: spec.block_bytes,
        block_w: spec.block_w,
        block_h: spec.block_h,
        samples: Cast[u8](spec.samples),
    )) else e {
        ReleaseResPtr(args.out_res, false)
        fail e
    }

    rec := DxTexture(
        res: args.out_res,
        dxgi_res: spec.dxgi_res,
        dxgi_rtv_dsv: spec.dxgi_rtv_dsv,
        width: spec.width,
        height: spec.height,
        layers: spec.layers,
        mips: spec.mips,
        samples: spec.samples,
        block_bytes: spec.block_bytes,
        block_w: spec.block_w,
        block_h: spec.block_h,
        planes: spec.planes,
        is_depth: spec.is_depth,
    )
    return AllocRec(a, rec) else e {
        ReleaseResPtr(args.out_res, false)
        fail e
    }
}

/// Releases a D3D12 texture handle.
export Device.DestroyTexture(mut self, nh: u64) {
    if Target.os != Os.Windows || nh == 0 {
        return
    }
    a := self.heap.Alloc()
    rec := FreeRec[DxTexture](a, nh)
    ReleaseResPtr(rec.res, false)
}

struct CreateRtDsViewArgs {
    dev: *d3d12.ID3D12Device
    res: *d3d12.ID3D12Resource
    is_dsv: bool
    rtv_desc: d3d12.D3d12RenderTargetViewDesc
    rtv_cpu: d3d12.D3d12CpuDescriptorHandle
    dsv_rw_desc: d3d12.D3d12DepthStencilViewDesc
    dsv_rw_cpu: d3d12.D3d12CpuDescriptorHandle
    dsv_ro_desc: d3d12.D3d12DepthStencilViewDesc
    dsv_ro_cpu: d3d12.D3d12CpuDescriptorHandle
}

CreateRtDsViewSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to live CreateRtDsViewArgs" {
        p := raw.PtrCast[u8, CreateRtDsViewArgs](arg)
        if !(*p).is_dsv {
            (*(*p).dev).CreateRenderTargetView((*p).res, raw.Of(&(*p).rtv_desc), (*p).rtv_cpu)
        } else {
            (*(*p).dev).CreateDepthStencilView((*p).res, raw.Of(&(*p).dsv_rw_desc), (*p).dsv_rw_cpu)
            (*(*p).dev).CreateDepthStencilView((*p).res, raw.Of(&(*p).dsv_ro_desc), (*p).dsv_ro_cpu)
        }
    }
}

BuildRtvDesc(spec: &ViewSpec): d3d12.D3d12RenderTargetViewDesc {
    var d := mem.Zeroed[d3d12.D3d12RenderTargetViewDesc]()
    d.format = Cast[dxgi.DxgiFormat](spec.dxgi_rtv_dsv)
    if spec.samples > 1 {
        if spec.dim == 1 {
            d.view_dimension = d3d12.D3d12RtvDimensionTexture2dmsarray
            d.anon0.SetTexture2DMSArray(d3d12.D3d12Tex2dmsArrayRtv(
                first_array_slice: Cast[windows.Uint](spec.first_layer),
                array_size: Cast[windows.Uint](spec.layer_count),
            ))
        } else {
            d.view_dimension = d3d12.D3d12RtvDimensionTexture2dms
            d.anon0.SetTexture2DMS(d3d12.D3d12Tex2dmsRtv(unused_field_nothing_to_define: 0))
        }
    } else if spec.dim == 0 && spec.first_layer == 0 && spec.layer_count == 1 {
        d.view_dimension = d3d12.D3d12RtvDimensionTexture2d
        d.anon0.SetTexture2D(d3d12.D3d12Tex2dRtv(
            mip_slice: Cast[windows.Uint](spec.first_mip),
            plane_slice: 0,
        ))
    } else {
        d.view_dimension = d3d12.D3d12RtvDimensionTexture2darray
        d.anon0.SetTexture2DArray(d3d12.D3d12Tex2dArrayRtv(
            mip_slice: Cast[windows.Uint](spec.first_mip),
            first_array_slice: Cast[windows.Uint](spec.first_layer),
            array_size: Cast[windows.Uint](spec.layer_count),
            plane_slice: 0,
        ))
    }
    return d
}

BuildDsvDesc(spec: &ViewSpec, read_only: bool): d3d12.D3d12DepthStencilViewDesc {
    var d := mem.Zeroed[d3d12.D3d12DepthStencilViewDesc]()
    d.format = Cast[dxgi.DxgiFormat](spec.dxgi_rtv_dsv)
    if read_only {
        d.flags = d3d12.D3d12DsvFlagReadOnlyDepth
        if spec.has_stencil {
            d.flags |= d3d12.D3d12DsvFlagReadOnlyStencil
        }
    } else {
        d.flags = d3d12.D3d12DsvFlagNone
    }
    if spec.samples > 1 {
        if spec.dim == 1 {
            d.view_dimension = d3d12.D3d12DsvDimensionTexture2dmsarray
            d.anon0.SetTexture2DMSArray(d3d12.D3d12Tex2dmsArrayDsv(
                first_array_slice: Cast[windows.Uint](spec.first_layer),
                array_size: Cast[windows.Uint](spec.layer_count),
            ))
        } else {
            d.view_dimension = d3d12.D3d12DsvDimensionTexture2dms
            d.anon0.SetTexture2DMS(d3d12.D3d12Tex2dmsDsv(unused_field_nothing_to_define: 0))
        }
    } else if spec.dim == 0 && spec.first_layer == 0 && spec.layer_count == 1 {
        d.view_dimension = d3d12.D3d12DsvDimensionTexture2d
        d.anon0.SetTexture2D(d3d12.D3d12Tex2dDsv(mip_slice: Cast[windows.Uint](spec.first_mip)))
    } else {
        d.view_dimension = d3d12.D3d12DsvDimensionTexture2darray
        d.anon0.SetTexture2DArray(d3d12.D3d12Tex2dArrayDsv(
            mip_slice: Cast[windows.Uint](spec.first_mip),
            first_array_slice: Cast[windows.Uint](spec.first_layer),
            array_size: Cast[windows.Uint](spec.layer_count),
        ))
    }
    return d
}

/// Creates a D3D12 texture view, allocating RTV or RW+RO DSV descriptors if render_target.
export Device.CreateView(mut self, spec: &ViewSpec): Error!u64 {
    if Target.os != Os.Windows {
        fail Unsupported
    }
    if self.lost {
        fail DxFail(kind: self.loss_reason, code: 0, call: CallCreateResource, at: -1)
    }
    var tex_res := raw.Null[d3d12.ID3D12Resource]()
    unsafe "spec.tex_nh is a live DxTexture" {
        tp := raw.FromAddr[DxTexture](Trunc[uint](spec.tex_nh))
        tex_res = (*tp).res
    }
    a := self.heap.Alloc()
    var has_rtv := false
    var rtv_slot: u64 = 0
    var rtv_ptr: u64 = 0
    var has_dsv := false
    var dsv_rw_slot: u64 = 0
    var dsv_rw_ptr: u64 = 0
    var dsv_ro_slot: u64 = 0
    var dsv_ro_ptr: u64 = 0

    if spec.render_target && !spec.is_depth {
        s := self.rtv_heap.AllocRange(a, 1) else {
            fail DxFail(kind: ErrKind.LimitExceeded, code: 0, call: CallCreateHeap, at: -1)
        }
        has_rtv = true
        rtv_slot = s
        cpu := self.rtv_heap.CpuAt(s)
        rtv_ptr = cpu.ptr
        var args := CreateRtDsViewArgs(
            dev: self.dev,
            res: tex_res,
            is_dsv: false,
            rtv_desc: BuildRtvDesc(spec),
            rtv_cpu: cpu,
            dsv_rw_desc: mem.Zeroed[d3d12.D3d12DepthStencilViewDesc](),
            dsv_rw_cpu: mem.Zeroed[d3d12.D3d12CpuDescriptorHandle](),
            dsv_ro_desc: mem.Zeroed[d3d12.D3d12DepthStencilViewDesc](),
            dsv_ro_cpu: mem.Zeroed[d3d12.D3d12CpuDescriptorHandle](),
        )
        SysRun(mut args, CreateRtDsViewSys)
    } else if spec.render_target && spec.is_depth {
        s0 := self.dsv_heap.AllocRange(a, 2) else {
            fail DxFail(kind: ErrKind.LimitExceeded, code: 0, call: CallCreateHeap, at: -1)
        }
        has_dsv = true
        dsv_rw_slot = s0
        dsv_ro_slot = s0 + 1
        cpu_rw := self.dsv_heap.CpuAt(s0)
        cpu_ro := self.dsv_heap.CpuAt(s0 + 1)
        dsv_rw_ptr = cpu_rw.ptr
        dsv_ro_ptr = cpu_ro.ptr
        var args := CreateRtDsViewArgs(
            dev: self.dev,
            res: tex_res,
            is_dsv: true,
            rtv_desc: mem.Zeroed[d3d12.D3d12RenderTargetViewDesc](),
            rtv_cpu: mem.Zeroed[d3d12.D3d12CpuDescriptorHandle](),
            dsv_rw_desc: BuildDsvDesc(spec, false),
            dsv_rw_cpu: cpu_rw,
            dsv_ro_desc: BuildDsvDesc(spec, true),
            dsv_ro_cpu: cpu_ro,
        )
        SysRun(mut args, CreateRtDsViewSys)
    }

    rec := DxView(
        tex_res: tex_res,
        dxgi_srv: spec.dxgi_srv,
        dxgi_stencil_srv: spec.dxgi_stencil_srv,
        dim: spec.dim,
        aspect: spec.aspect,
        first_mip: spec.first_mip,
        mip_count: spec.mip_count,
        first_layer: spec.first_layer,
        layer_count: spec.layer_count,
        samples: spec.samples,
        is_depth: spec.is_depth,
        has_rtv: has_rtv,
        rtv_slot: rtv_slot,
        rtv_ptr: rtv_ptr,
        has_dsv: has_dsv,
        dsv_rw_slot: dsv_rw_slot,
        dsv_rw_ptr: dsv_rw_ptr,
        dsv_ro_slot: dsv_ro_slot,
        dsv_ro_ptr: dsv_ro_ptr,
        is_swapchain: false,
        swap_res: raw.Null[d3d12.ID3D12Resource](),
    )
    return AllocRec(a, rec) else e {
        if has_rtv {
            self.rtv_heap.FreeRange(a, rtv_slot, 1)
        }
        if has_dsv {
            self.dsv_heap.FreeRange(a, dsv_rw_slot, 2)
        }
        fail e
    }
}

/// Releases a D3D12 texture view handle and frees its RTV/DSV descriptor slots.
export Device.DestroyView(mut self, nh: u64) {
    if Target.os != Os.Windows || nh == 0 {
        return
    }
    a := self.heap.Alloc()
    rec := FreeRec[DxView](a, nh)
    if rec.has_rtv && !rec.is_swapchain {
        self.rtv_heap.FreeRange(a, rec.rtv_slot, 1)
    }
    if rec.has_dsv {
        self.dsv_heap.FreeRange(a, rec.dsv_rw_slot, 2)
    }
}

MapAddressMode(a: u8): d3d12.D3d12TextureAddressMode {
    if a == 0 { return d3d12.D3d12TextureAddressModeWrap }
    if a == 1 { return d3d12.D3d12TextureAddressModeMirror }
    return d3d12.D3d12TextureAddressModeClamp
}

MapCompareFunc(c: u8): d3d12.D3d12ComparisonFunc {
    if c == 0 { return d3d12.D3d12ComparisonFuncNever }
    if c == 1 { return d3d12.D3d12ComparisonFuncLess }
    if c == 2 { return d3d12.D3d12ComparisonFuncEqual }
    if c == 3 { return d3d12.D3d12ComparisonFuncLessEqual }
    if c == 4 { return d3d12.D3d12ComparisonFuncGreater }
    if c == 5 { return d3d12.D3d12ComparisonFuncNotEqual }
    if c == 6 { return d3d12.D3d12ComparisonFuncGreaterEqual }
    return d3d12.D3d12ComparisonFuncAlways
}

MapSamplerFilter(spec: &SamplerSpec): d3d12.D3d12Filter {
    if spec.anisotropy > 1 {
        return if spec.has_compare {
            d3d12.D3d12FilterComparisonAnisotropic
        } else {
            d3d12.D3d12FilterAnisotropic
        }
    }
    var bits: i32 = 0
    if spec.min_linear { bits |= 16 }
    if spec.mag_linear { bits |= 4 }
    if spec.mip_linear { bits |= 1 }
    if spec.has_compare { bits |= 128 }
    return bits
}

/// Creates a D3D12 sampler descriptor template.
export Device.CreateSampler(mut self, spec: &SamplerSpec): Error!u64 {
    if Target.os != Os.Windows {
        fail Unsupported
    }
    var desc := mem.Zeroed[d3d12.D3d12SamplerDesc]()
    desc.filter = MapSamplerFilter(spec)
    desc.address_u = MapAddressMode(spec.addr_u)
    desc.address_v = MapAddressMode(spec.addr_v)
    desc.address_w = MapAddressMode(spec.addr_w)
    desc.mip_lod_bias = 0.0
    desc.max_anisotropy = Cast[windows.Uint](spec.anisotropy)
    desc.comparison_func = if spec.has_compare {
        MapCompareFunc(spec.compare)
    } else {
        d3d12.D3d12ComparisonFuncNone
    }
    desc.min_lod = spec.lod_min
    desc.max_lod = spec.lod_max
    return try AllocRec(self.heap.Alloc(), DxSampler(desc: desc))
}

/// Frees a D3D12 sampler handle.
export Device.DestroySampler(mut self, nh: u64) {
    if Target.os != Os.Windows || nh == 0 {
        return
    }
    _ = FreeRec[DxSampler](self.heap.Alloc(), nh)
}

struct WriteBindGroupArgs {
    dev: *d3d12.ID3D12Device
    entries_ptr: *BindEntrySpec
    entry_count: int
    cpu_start: u64
    inc_size: u64
}

WriteTexSrv(dev: *d3d12.ID3D12Device, vp: *DxView, cpu: d3d12.D3d12CpuDescriptorHandle) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "vp is a live DxView and dev is a live ID3D12Device" {
        var d := mem.Zeroed[d3d12.D3d12ShaderResourceViewDesc]()
        is_stencil := (*vp).aspect == 2 && (*vp).dxgi_stencil_srv != 0
        d.format = Cast[dxgi.DxgiFormat](if is_stencil { (*vp).dxgi_stencil_srv } else { (*vp).dxgi_srv })
        d.shader4component_mapping = Cast[u32](d3d12.D3d12DefaultShader4ComponentMapping)
        pslice: windows.Uint = if (*vp).aspect == 2 { 1 } else { 0 }
        dim := (*vp).dim
        ms := (*vp).samples > 1
        if dim == 0 {
            if ms {
                d.view_dimension = d3d12.D3d12SrvDimensionTexture2dms
                d.anon0.SetTexture2DMS(d3d12.D3d12Tex2dmsSrv(unused_field_nothing_to_define: 0))
            } else {
                d.view_dimension = d3d12.D3d12SrvDimensionTexture2d
                d.anon0.SetTexture2D(d3d12.D3d12Tex2dSrv(
                    most_detailed_mip: Cast[windows.Uint]((*vp).first_mip),
                    mip_levels: Cast[windows.Uint]((*vp).mip_count),
                    plane_slice: pslice,
                    resource_min_lod_clamp: 0.0,
                ))
            }
        } else if dim == 1 {
            if ms {
                d.view_dimension = d3d12.D3d12SrvDimensionTexture2dmsarray
                d.anon0.SetTexture2DMSArray(d3d12.D3d12Tex2dmsArraySrv(
                    first_array_slice: Cast[windows.Uint]((*vp).first_layer),
                    array_size: Cast[windows.Uint]((*vp).layer_count),
                ))
            } else {
                d.view_dimension = d3d12.D3d12SrvDimensionTexture2darray
                d.anon0.SetTexture2DArray(d3d12.D3d12Tex2dArraySrv(
                    most_detailed_mip: Cast[windows.Uint]((*vp).first_mip),
                    mip_levels: Cast[windows.Uint]((*vp).mip_count),
                    first_array_slice: Cast[windows.Uint]((*vp).first_layer),
                    array_size: Cast[windows.Uint]((*vp).layer_count),
                    plane_slice: pslice,
                    resource_min_lod_clamp: 0.0,
                ))
            }
        } else if dim == 2 {
            d.view_dimension = d3d12.D3d12SrvDimensionTexturecube
            d.anon0.SetTextureCube(d3d12.D3d12TexcubeSrv(
                most_detailed_mip: Cast[windows.Uint]((*vp).first_mip),
                mip_levels: Cast[windows.Uint]((*vp).mip_count),
                resource_min_lod_clamp: 0.0,
            ))
        } else {
            d.view_dimension = d3d12.D3d12SrvDimensionTexturecubearray
            d.anon0.SetTextureCubeArray(d3d12.D3d12TexcubeArraySrv(
                most_detailed_mip: Cast[windows.Uint]((*vp).first_mip),
                mip_levels: Cast[windows.Uint]((*vp).mip_count),
                first2d_array_face: Cast[windows.Uint]((*vp).first_layer),
                num_cubes: Cast[windows.Uint]((*vp).layer_count / 6),
                resource_min_lod_clamp: 0.0,
            ))
        }
        (*dev).CreateShaderResourceView((*vp).tex_res, raw.Of(&d), cpu)
    }
}

WriteBindGroupSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to live WriteBindGroupArgs and entries_ptr slice" {
        p := raw.PtrCast[u8, WriteBindGroupArgs](arg)
        ents := raw.Slice((*p).entries_ptr, (*p).entry_count)
        var slot_idx: u64 = 0
        for i in 0..(*p).entry_count {
            e := &ents[i]
            if e.kind == 4 || e.kind == 5 {
                continue
            }
            cpu := d3d12.D3d12CpuDescriptorHandle(ptr: (*p).cpu_start + slot_idx * (*p).inc_size)
            slot_idx += 1
            if e.kind == 0 {
                bp := raw.FromAddr[DxBuffer](Trunc[uint](e.res_nh))
                aligned := (e.size + 255) & ~255
                cbv := d3d12.D3d12ConstantBufferViewDesc(
                    buffer_location: (*bp).gpu_va + Cast[u64](e.offset),
                    size_in_bytes: Cast[windows.Uint](aligned),
                )
                (*(*p).dev).CreateConstantBufferView(raw.Of(&cbv), cpu)
            } else if e.kind == 1 {
                bp := raw.FromAddr[DxBuffer](Trunc[uint](e.res_nh))
                var srv := mem.Zeroed[d3d12.D3d12ShaderResourceViewDesc]()
                srv.view_dimension = d3d12.D3d12SrvDimensionBuffer
                srv.shader4component_mapping = Cast[u32](d3d12.D3d12DefaultShader4ComponentMapping)
                if e.stride == 0 {
                    srv.format = 39 // DXGI_FORMAT_R32_TYPELESS
                    srv.anon0.SetBuffer(d3d12.D3d12BufferSrv(
                        first_element: Cast[u64](e.offset / 4),
                        num_elements: Cast[windows.Uint](e.size / 4),
                        structure_byte_stride: 0,
                        flags: d3d12.D3d12BufferSrvFlagRaw,
                    ))
                } else {
                    srv.format = dxgi.DxgiFormatUnknown
                    srv.anon0.SetBuffer(d3d12.D3d12BufferSrv(
                        first_element: Cast[u64](e.offset / e.stride),
                        num_elements: Cast[windows.Uint](e.size / e.stride),
                        structure_byte_stride: Cast[windows.Uint](e.stride),
                        flags: d3d12.D3d12BufferSrvFlagNone,
                    ))
                }
                (*(*p).dev).CreateShaderResourceView((*bp).res, raw.Of(&srv), cpu)
            } else if e.kind == 2 {
                bp := raw.FromAddr[DxBuffer](Trunc[uint](e.res_nh))
                var uav := mem.Zeroed[d3d12.D3d12UnorderedAccessViewDesc]()
                uav.view_dimension = d3d12.D3d12UavDimensionBuffer
                if e.stride == 0 {
                    uav.format = 39 // DXGI_FORMAT_R32_TYPELESS
                    uav.anon0.SetBuffer(d3d12.D3d12BufferUav(
                        first_element: Cast[u64](e.offset / 4),
                        num_elements: Cast[windows.Uint](e.size / 4),
                        structure_byte_stride: 0,
                        counter_offset_in_bytes: 0,
                        flags: d3d12.D3d12BufferUavFlagRaw,
                    ))
                } else {
                    uav.format = dxgi.DxgiFormatUnknown
                    uav.anon0.SetBuffer(d3d12.D3d12BufferUav(
                        first_element: Cast[u64](e.offset / e.stride),
                        num_elements: Cast[windows.Uint](e.size / e.stride),
                        structure_byte_stride: Cast[windows.Uint](e.stride),
                        counter_offset_in_bytes: 0,
                        flags: d3d12.D3d12BufferUavFlagNone,
                    ))
                }
                (*(*p).dev).CreateUnorderedAccessView((*bp).res, raw.Null[d3d12.ID3D12Resource](), raw.Of(&uav), cpu)
            } else if e.kind == 3 {
                vp := raw.FromAddr[DxView](Trunc[uint](e.res_nh))
                WriteTexSrv((*p).dev, vp, cpu)
            }
        }
    }
}

/// Allocates shader-visible CBV/SRV/UAV and sampler descriptors for a bind group.
export Device.CreateBindGroup(mut self, entries: []BindEntrySpec): Error!u64 {
    if Target.os != Os.Windows {
        fail Unsupported
    }
    if self.lost {
        fail DxFail(kind: self.loss_reason, code: 0, call: CallCreateHeap, at: -1)
    }
    var view_cnt: u64 = 0
    var smp_nhs: [16]u64 = [0; 16]
    var smp_cnt: int = 0
    var dyn_vas: [4]u64 = [0; 4]
    var dyn_cnt: int = 0

    for i in 0..entries.Len() {
        e := &entries[i]
        if e.kind == 4 {
            smp_nhs[smp_cnt] = e.res_nh
            smp_cnt += 1
        } else if e.kind == 5 {
            unsafe "e.res_nh is a live DxBuffer" {
                bp := raw.FromAddr[DxBuffer](Trunc[uint](e.res_nh))
                dyn_vas[dyn_cnt] = (*bp).gpu_va + Cast[u64](e.offset)
            }
            dyn_cnt += 1
        } else {
            view_cnt += 1
        }
    }

    a := self.heap.Alloc()
    var has_views := false
    var view_off: u64 = 0
    var view_gpu: u64 = 0
    if view_cnt > 0 {
        off := self.cbv_srv_uav_heap.AllocRange(a, view_cnt) else {
            fail DxFail(kind: ErrKind.LimitExceeded, code: 0, call: CallCreateHeap, at: -1)
        }
        has_views = true
        view_off = off
        view_gpu = self.cbv_srv_uav_heap.GpuAt(off).ptr
        var wargs := WriteBindGroupArgs(
            dev: self.dev,
            entries_ptr: raw.Data(entries),
            entry_count: entries.Len(),
            cpu_start: self.cbv_srv_uav_heap.CpuAt(off).ptr,
            inc_size: self.cbv_srv_uav_heap.inc_size,
        )
        SysRun(mut wargs, WriteBindGroupSys)
    }

    var has_smp := false
    var smp_idx: int = -1
    var smp_gpu: u64 = 0
    if smp_cnt > 0 {
        (cache_idx, gpu_ptr) := self.AcquireSamplerRange(smp_nhs[0..smp_cnt]) else e {
            if has_views {
                self.cbv_srv_uav_heap.FreeRange(a, view_off, view_cnt)
            }
            fail e
        }
        has_smp = true
        smp_idx = cache_idx
        smp_gpu = gpu_ptr
    }

    rec := DxBindGroup(
        has_cbv_srv_uav: has_views,
        cbv_srv_uav_off: view_off,
        cbv_srv_uav_cnt: view_cnt,
        cbv_srv_uav_gpu: view_gpu,
        has_samplers: has_smp,
        sampler_cache_idx: smp_idx,
        sampler_gpu: smp_gpu,
        dyn_count: dyn_cnt,
        dyn_vas: dyn_vas,
    )
    return AllocRec(a, rec) else e {
        if has_views {
            self.cbv_srv_uav_heap.FreeRange(a, view_off, view_cnt)
        }
        if has_smp {
            self.ReleaseSamplerRange(smp_idx)
        }
        fail e
    }
}

/// Frees a bind group's shader-visible descriptor allocations.
export Device.DestroyBindGroup(mut self, nh: u64) {
    if Target.os != Os.Windows || nh == 0 {
        return
    }
    a := self.heap.Alloc()
    rec := FreeRec[DxBindGroup](a, nh)
    if rec.has_cbv_srv_uav {
        self.cbv_srv_uav_heap.FreeRange(a, rec.cbv_srv_uav_off, rec.cbv_srv_uav_cnt)
    }
    if rec.has_samplers {
        self.ReleaseSamplerRange(rec.sampler_cache_idx)
    }
}
