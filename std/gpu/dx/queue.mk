// Why: D3D12 queue execution, fence synchronization, upload ring staging,
// creation zeroing, and synchronous texture readback must run driver calls
// on the system stack and re-read fence values after every ObjectWait wake.
//
// What: CompletedFenceValue, WaitSerial, AcquireCmdAllocator, StageUploadData,
// QueueWriteBuffer, QueueWriteTexture, FlushPreSubmit, ExecuteAndSignal, and
// ReadTextureSync.
//
// How: wraps Wait/Signal/ExecuteCommandLists/CreateCommandAllocator in
// MkSystemRun, waits via SetEventOnCompletion + rt.ObjectWait, and repacks
// texture rows to D3D12's 256-byte pitch alignment.

import c.windows
import c.dxgi
import c.d3d12
import std.core
import std.raw
import std.mem
import std.rt

const D3d12RowPitchAlign: int = 256
const D3d12PlacementAlign: u64 = 512

struct FenceReadArgs {
    fence: *d3d12.ID3D12Fence
    val: u64
}

FenceReadSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to live FenceReadArgs with live ID3D12Fence" {
        p := raw.PtrCast[u8, FenceReadArgs](arg)
        (*p).val = (*(*p).fence).GetCompletedValue()
    }
}

struct FenceEventArgs {
    fence: *d3d12.ID3D12Fence
    target: u64
    ev: windows.Handle
    hr: windows.Hresult
}

FenceSetEventSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to live FenceEventArgs" {
        p := raw.PtrCast[u8, FenceEventArgs](arg)
        _ = windows.ResetEvent((*p).ev)
        (*p).hr = (*(*p).fence).SetEventOnCompletion((*p).target, (*p).ev)
    }
}

/// Reads the GPU fence completed value on the system stack and detects device loss.
export Device.CompletedFenceValue(mut self): u64 {
    if Target.os != Os.Windows || raw.IsNull(self.fence) {
        return 0
    }
    var args := FenceReadArgs(fence: self.fence, val: 0)
    SysRun(mut args, FenceReadSys)
    if args.val == 0xFFFFFFFFFFFFFFFF {
        if !self.lost && !raw.IsNull(self.dev) {
            unsafe "self.dev is a live ID3D12Device" {
                rem := (*self.dev).GetDeviceRemovedReason()
                self.lost = true
                self.loss_reason = LossKindFromHr(rem)
            }
        }
        return self.completed_serial
    }
    if args.val > self.completed_serial {
        self.completed_serial = args.val
    }
    return self.completed_serial
}

/// Suspends via rt.ObjectWait until the D3D12 fence reaches target, re-reading
/// GetCompletedValue after every wake. Repeatable on Canceled; returns DeviceLost
/// immediately if the device was removed.
export Device.WaitSerial(mut self, io: Io, target: u64): Error!void {
    if Target.os != Os.Windows {
        fail Unsupported
    }
    if self.lost {
        fail DxFail(kind: self.loss_reason, code: 0, call: CallSignal, at: -1)
    }
    if target == 0 {
        return
    }
    for {
        comp := self.CompletedFenceValue()
        if self.lost {
            fail DxFail(kind: self.loss_reason, code: 0, call: CallSignal, at: -1)
        }
        if comp >= target {
            return
        }
        var ev_h: windows.Handle = raw.Null[void]()
        unsafe "self.fence_event is a valid Win32 event handle" { ev_h = raw.FromAddr[void](self.fence_event) }
        var ev_args := FenceEventArgs(
            fence: self.fence,
            target: target,
            ev: ev_h,
            hr: 0,
        )
        SysRun(mut ev_args, FenceSetEventSys)
        if ev_args.hr < 0 {
            fail MapHrError(self, ev_args.hr, CallSignal)
        }
        // Re-check before parking in case the GPU completed concurrently.
        comp2 := self.CompletedFenceValue()
        if self.lost {
            fail DxFail(kind: self.loss_reason, code: 0, call: CallSignal, at: -1)
        }
        if comp2 >= target {
            return
        }
        r := rt.ObjectWait(io, self.fence_event, -1)
        if r.err == core.SysErr.Canceled {
            fail Canceled
        }
        if r.err != core.SysErr.None {
            fail DxFail(kind: ErrKind.Driver, code: i64(0), call: CallSignal, at: -1)
        }
    }
}

struct QueueSignalArgs {
    queue: *d3d12.ID3D12CommandQueue
    fence: *d3d12.ID3D12Fence
    serial: u64
    hr: windows.Hresult
}

QueueSignalSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to live QueueSignalArgs" {
        p := raw.PtrCast[u8, QueueSignalArgs](arg)
        (*p).hr = (*(*p).queue).Signal((*p).fence, (*p).serial)
    }
}

/// Signals a fresh fence serial on the direct queue and waits until it retires
/// so post-command-list queue operations like DXGI Present have completed.
/// The signal reserves its serial from the caller's submission sequence.
export Device.WaitQueueIdle(mut self, io: Io, next_serial: mut u64): Error!void {
    if Target.os != Os.Windows {
        fail Unsupported
    }
    if self.lost {
        fail DxFail(kind: self.loss_reason, code: 0, call: CallSignal, at: -1)
    }
    if self.present_pending {
        ser := *next_serial
        Assert(ser > self.last_submitted)
        var sargs := QueueSignalArgs(queue: self.queue, fence: self.fence, serial: ser, hr: 0)
        SysRun(mut sargs, QueueSignalSys)
        if sargs.hr < 0 {
            fail MapHrError(self, sargs.hr, CallSignal)
        }
        self.last_submitted = ser
        *next_serial = ser + 1
        self.present_pending = false
    }
    try self.WaitSerial(io, self.last_submitted)
}

struct CreateCmdAllocArgs {
    dev: *d3d12.ID3D12Device
    out_alloc: *d3d12.ID3D12CommandAllocator
    hr: windows.Hresult
}

CreateCmdAllocSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to live CreateCmdAllocArgs" {
        p := raw.PtrCast[u8, CreateCmdAllocArgs](arg)
        var ap: *void = raw.Null[void]()
        hr := (*(*p).dev).CreateCommandAllocator(
            d3d12.D3d12CommandListTypeDirect,
            raw.Of(&d3d12.IidID3D12CommandAllocator),
            raw.OfMut(mut ap),
        )
        (*p).hr = hr
        if hr >= 0 && !raw.IsNull(ap) {
            (*p).out_alloc = raw.PtrCast[void, d3d12.ID3D12CommandAllocator](ap)
        }
    }
}

AcquireCmdAllocator(dev: mut Device): Error!int {
    if Target.os != Os.Windows {
        fail Unsupported
    }
    comp := dev.CompletedFenceValue()
    for i in 0..dev.cmd_allocs.Len() {
        if dev.cmd_allocs[i].serial <= comp {
            return i
        }
    }
    var args := CreateCmdAllocArgs(dev: dev.dev, out_alloc: raw.Null[d3d12.ID3D12CommandAllocator](), hr: 0)
    SysRun(mut args, CreateCmdAllocSys)
    _ = dev.DrainInfoQueue()
    if args.hr < 0 || raw.IsNull(args.out_alloc) {
        fail MapHrError(dev, args.hr, CallExecuteCommandLists)
    }
    a := dev.heap.Alloc()
    dev.cmd_allocs.Push(a, CmdAllocRec(alloc: args.out_alloc, serial: 0)) else e {
        unsafe "release unstored allocator" {
            _ = (*args.out_alloc).Release()
        }
        fail e
    }
    return dev.cmd_allocs.Len() - 1
}

Device.CreateUploadResource(mut self, bytes: u64): Error!(res: *d3d12.ID3D12Resource, cpu: *u8, gpu_va: u64) {
    desc := BufferResourceDesc(bytes, d3d12.D3d12ResourceFlagNone)
    var args := CreateResArgs(
        dev: self.dev,
        placed: false,
        heap: raw.Null[d3d12.ID3D12Heap](),
        heap_off: 0,
        heap_type: d3d12.D3d12HeapTypeUpload,
        desc: desc,
        init_state: d3d12.D3d12ResourceStateGenericRead,
        has_clear: false,
        clear_val: mem.Zeroed[d3d12.D3d12ClearValue](),
        map_read_or_write: true,
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
    return (res: args.out_res, cpu: args.mapped, gpu_va: args.gpu_va)
}

Device.AllocUploadSpan(mut self, bytes: u64, align: u64): Error!(res: *d3d12.ID3D12Resource, cpu: *u8, off: u64) {
    if Target.os != Os.Windows {
        fail Unsupported
    }
    al := if align > 1 { align } else { u64(1) }
    mask := al - 1
    comp := self.CompletedFenceValue()
    a := self.heap.Alloc()

    if bytes <= UploadChunkBytes {
        if self.cur_upload_idx >= 0 && self.cur_upload_idx < self.upload_chunks.Len() {
            ch := mut self.upload_chunks[self.cur_upload_idx]
            if ch.serial <= comp {
                if ch.serial > 0 {
                    ch.used = 0
                    ch.serial = 0
                }
                aligned_off := (ch.used + mask) & ~mask
                if aligned_off + bytes <= ch.size {
                    ch.used = aligned_off + bytes
                    unsafe "ch.cpu_ptr is persistently mapped for ch.size bytes" {
                        return (res: ch.res, cpu: raw.Offset(ch.cpu_ptr, Cast[int](aligned_off)), off: aligned_off)
                    }
                }
            }
        }
        // Look for any retired upload chunk.
        for i in 0..self.upload_chunks.Len() {
            ch := mut self.upload_chunks[i]
            if ch.serial > 0 && ch.serial <= comp {
                ch.used = 0
                ch.serial = 0
                if bytes <= ch.size {
                    ch.used = bytes
                    self.cur_upload_idx = i
                    return (res: ch.res, cpu: ch.cpu_ptr, off: 0)
                }
            } else if ch.serial == 0 {
                aligned_off := (ch.used + mask) & ~mask
                if aligned_off + bytes <= ch.size {
                    ch.used = aligned_off + bytes
                    self.cur_upload_idx = i
                    unsafe "ch.cpu_ptr is persistently mapped" {
                        return (res: ch.res, cpu: raw.Offset(ch.cpu_ptr, Cast[int](aligned_off)), off: aligned_off)
                    }
                }
            }
        }
        // Allocate a fresh 4 MiB chunk.
        (res, cpu, gpu_va) := try self.CreateUploadResource(UploadChunkBytes)
        ch := UploadChunk(res: res, cpu_ptr: cpu, gpu_va: gpu_va, size: UploadChunkBytes, used: bytes, serial: 0)
        self.upload_chunks.Push(a, ch) else e {
            ReleaseResPtr(res, true)
            fail e
        }
        self.cur_upload_idx = self.upload_chunks.Len() - 1
        return (res: res, cpu: cpu, off: 0)
    }

    // Large staging buffer pooled by power-of-two class.
    var p2: u64 = 1
    var cls := 0
    for p2 < bytes {
        p2 <<= 1
        cls += 1
    }
    for i in 0..self.staging_bufs.Len() {
        sb := mut self.staging_bufs[i]
        if !sb.in_use && sb.serial <= comp && sb.class_idx == cls {
            sb.in_use = true
            sb.serial = 0
            return (res: sb.res, cpu: sb.cpu_ptr, off: 0)
        }
    }
    (res, cpu, gpu_va: _) := try self.CreateUploadResource(p2)
    sb := StagingBuf(res: res, cpu_ptr: cpu, size: p2, class_idx: cls, serial: 0, in_use: true)
    self.staging_bufs.Push(a, sb) else e {
        ReleaseResPtr(res, true)
        fail e
    }
    return (res: res, cpu: cpu, off: 0)
}

Device.StampUploadInflight(mut self, serial: u64) {
    for i in 0..self.upload_chunks.Len() {
        if self.upload_chunks[i].used > 0 && self.upload_chunks[i].serial == 0 {
            self.upload_chunks[i].serial = serial
        }
    }
    for i in 0..self.staging_bufs.Len() {
        if self.staging_bufs[i].in_use {
            self.staging_bufs[i].in_use = false
            self.staging_bufs[i].serial = serial
        }
    }
}

/// Copies data into the upload ring now and returns (ID3D12Resource ptr, offset)
/// for an in-stream Encoder.WriteBuffer record.
export Device.StageUploadData(mut self, data: []u8): Error!(res_ptr: u64, off: u64) {
    if Target.os != Os.Windows {
        fail Unsupported
    }
    if self.lost {
        fail DxFail(kind: self.loss_reason, code: 0, call: CallMap, at: -1)
    }
    n := data.Len()
    if n == 0 {
        return (res_ptr: 0, off: 0)
    }
    (res, cpu, off) := try self.AllocUploadSpan(Cast[u64](n), 4)
    unsafe "cpu points to n writable upload bytes and data has n bytes" {
        raw.Copy(cpu, raw.Data(data), n)
    }
    return (res_ptr: Trunc[u64](raw.Addr(res)), off: off)
}

/// Stages a Device.WriteBuffer copy in the pre-submit queue.
export Device.QueueWriteBuffer(mut self, dst_nh: u64, dst_off: int, data: []u8): Error!void {
    if Target.os != Os.Windows {
        fail Unsupported
    }
    if self.lost {
        fail DxFail(kind: self.loss_reason, code: 0, call: CallMap, at: -1)
    }
    if data.Len() == 0 {
        return
    }
    (res: src_res, cpu, off: src_off) := try self.AllocUploadSpan(Cast[u64](data.Len()), 4)
    unsafe "cpu points to data.Len() writable upload bytes" {
        raw.Copy(cpu, raw.Data(data), data.Len())
    }
    var dst_res := raw.Null[d3d12.ID3D12Resource]()
    unsafe "dst_nh is a live DxBuffer" {
        dst_res = (*raw.FromAddr[DxBuffer](Trunc[uint](dst_nh))).res
    }
    a := self.heap.Alloc()
    try self.pending_uploads.Push(a, PendingUploadCopy(
        kind: 0,
        src_res: src_res,
        src_off: src_off,
        dst_res: dst_res,
        dst_off: Cast[u64](dst_off),
        bytes: Cast[u64](data.Len()),
        subresource: 0,
        dst_x: 0,
        dst_y: 0,
        width: 0,
        height: 0,
        row_pitch: 0,
        dxgi_fmt: 0,
        cur_state: ResStateCommon,
    ))
}

/// Stages a Device.WriteTexture copy in the pre-submit queue, repacking tight rows to 256-byte pitch.
export Device.QueueWriteTexture(
    mut self,
    dst_nh: u64,
    mip: int,
    layer: int,
    x: int,
    y: int,
    width: int,
    height: int,
    layers: int,
    aspect: u8,
    cur_state: u16,
    data: []u8,
): Error!void {
    if Target.os != Os.Windows {
        fail Unsupported
    }
    if self.lost {
        fail DxFail(kind: self.loss_reason, code: 0, call: CallMap, at: -1)
    }
    var dst_res := raw.Null[d3d12.ID3D12Resource]()
    var dxgi_fmt: u32 = 0
    var bw := 1
    var bh := 1
    var bbytes := 4
    var total_mips := 1
    var total_layers := 1
    unsafe "dst_nh is a live DxTexture" {
        tp := raw.FromAddr[DxTexture](Trunc[uint](dst_nh))
        dst_res = (*tp).res
        dxgi_fmt = (*tp).dxgi_res
        bw = (*tp).block_w
        bh = (*tp).block_h
        bbytes = (*tp).block_bytes
        total_mips = (*tp).mips
        total_layers = (*tp).layers
    }
    if aspect == 2 {
        bbytes = 1
    } else if aspect == 1 && dxgi_fmt == 19 { // DXGI_FORMAT_R32G8X24_TYPELESS
        bbytes = 4
    }
    bx := (width + bw - 1) / bw
    by := (height + bh - 1) / bh
    row_bytes := bx * bbytes
    row_pitch := (row_bytes + D3d12RowPitchAlign - 1) & ~(D3d12RowPitchAlign - 1)
    slice_bytes := row_pitch * by
    total_bytes := slice_bytes * layers

    (res: src_res, cpu, off: src_off) := try self.AllocUploadSpan(Cast[u64](total_bytes), D3d12PlacementAlign)
    var src_idx := 0
    for li in 0..layers {
        slice_base := li * slice_bytes
        for ry in 0..by {
            dst_row_off := slice_base + ry * row_pitch
            unsafe "cpu + dst_row_off fits row_bytes within total_bytes" {
                raw.Copy(raw.Offset(cpu, dst_row_off), raw.Offset(raw.Data(data), src_idx), row_bytes)
            }
            src_idx += row_bytes
        }
    }

    plane := if aspect == 2 { 1 } else { 0 }
    a := self.heap.Alloc()
    for li in 0..layers {
        sub := Cast[windows.Uint](mip + (layer + li) * total_mips + plane * total_mips * total_layers)
        try self.pending_uploads.Push(a, PendingUploadCopy(
            kind: 1,
            src_res: src_res,
            src_off: src_off + Cast[u64](li * slice_bytes),
            dst_res: dst_res,
            dst_off: 0,
            bytes: Cast[u64](slice_bytes),
            subresource: sub,
            dst_x: Cast[windows.Uint](x),
            dst_y: Cast[windows.Uint](y),
            width: Cast[windows.Uint](bx * bw),
            height: Cast[windows.Uint](by * bh),
            row_pitch: Cast[windows.Uint](row_pitch),
            dxgi_fmt: dxgi_fmt,
            cur_state: cur_state,
        ))
    }
}

RecordPendingZerosAndUploads(dev: *Device, cmd: *d3d12.ID3D12GraphicsCommandList) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "dev points to live dx.Device" {
        // 1. Creation zeroing.
        for i in 0..(*dev).pending_zeros.Len() {
            z := &(*dev).pending_zeros[i]
            if z.kind == 0 {
                var done: u64 = 0
                for done < z.size {
                    rem := z.size - done
                    chunk := if rem > (*dev).zero_buf_size { (*dev).zero_buf_size } else { rem }
                    (*cmd).CopyBufferRegion(z.res, done, (*dev).zero_buf, 0, chunk)
                    done += chunk
                }
            } else if z.kind == 2 {
                EmitTransition(cmd, z.res, D3d12AllSubresources, d3d12.D3d12ResourceStateCommon, d3d12.D3d12ResourceStateRenderTarget)
                var zero_col: [4]windows.Float = [0.0, 0.0, 0.0, 0.0]
                rtv_cpu := (*dev).rtv_heap.CpuAt(0)
                if z.samples > 1 {
                    var rtv := mem.Zeroed[d3d12.D3d12RenderTargetViewDesc]()
                    rtv.format = Cast[dxgi.DxgiFormat](z.dxgi_rtv_dsv)
                    if z.layers > 1 {
                        rtv.view_dimension = d3d12.D3d12RtvDimensionTexture2dmsarray
                        rtv.anon0.SetTexture2DMSArray(d3d12.D3d12Tex2dmsArrayRtv(
                            first_array_slice: 0,
                            array_size: Cast[windows.Uint](z.layers),
                        ))
                    } else {
                        rtv.view_dimension = d3d12.D3d12RtvDimensionTexture2dms
                        rtv.anon0.SetTexture2DMS(d3d12.D3d12Tex2dmsRtv(unused_field_nothing_to_define: 0))
                    }
                    (*(*dev).dev).CreateRenderTargetView(z.res, raw.Of(&rtv), rtv_cpu)
                    (*cmd).ClearRenderTargetView(rtv_cpu, raw.Of(&zero_col[0]), 0, raw.Null[d3d12.D3d12Rect]())
                } else {
                    for m in 0..int(z.mips) {
                        for l in 0..int(z.layers) {
                            var rtv := mem.Zeroed[d3d12.D3d12RenderTargetViewDesc]()
                            rtv.format = Cast[dxgi.DxgiFormat](z.dxgi_rtv_dsv)
                            if z.layers > 1 {
                                rtv.view_dimension = d3d12.D3d12RtvDimensionTexture2darray
                                rtv.anon0.SetTexture2DArray(d3d12.D3d12Tex2dArrayRtv(
                                    mip_slice: Cast[windows.Uint](m),
                                    first_array_slice: Cast[windows.Uint](l),
                                    array_size: 1,
                                    plane_slice: 0,
                                ))
                            } else {
                                rtv.view_dimension = d3d12.D3d12RtvDimensionTexture2d
                                rtv.anon0.SetTexture2D(d3d12.D3d12Tex2dRtv(mip_slice: Cast[windows.Uint](m), plane_slice: 0))
                            }
                            (*(*dev).dev).CreateRenderTargetView(z.res, raw.Of(&rtv), rtv_cpu)
                            (*cmd).ClearRenderTargetView(rtv_cpu, raw.Of(&zero_col[0]), 0, raw.Null[d3d12.D3d12Rect]())
                        }
                    }
                }
                EmitTransition(cmd, z.res, D3d12AllSubresources, d3d12.D3d12ResourceStateRenderTarget, d3d12.D3d12ResourceStateCommon)
            } else if z.kind == 3 {
                EmitTransition(cmd, z.res, D3d12AllSubresources, d3d12.D3d12ResourceStateCommon, d3d12.D3d12ResourceStateDepthWrite)
                dsv_cpu := (*dev).dsv_heap.CpuAt(0)
                cflags := if z.planes > 1 {
                    d3d12.D3d12ClearFlagDepth | d3d12.D3d12ClearFlagStencil
                } else {
                    d3d12.D3d12ClearFlagDepth
                }
                if z.samples > 1 {
                    var dsv := mem.Zeroed[d3d12.D3d12DepthStencilViewDesc]()
                    dsv.format = Cast[dxgi.DxgiFormat](z.dxgi_rtv_dsv)
                    if z.layers > 1 {
                        dsv.view_dimension = d3d12.D3d12DsvDimensionTexture2dmsarray
                        dsv.anon0.SetTexture2DMSArray(d3d12.D3d12Tex2dmsArrayDsv(
                            first_array_slice: 0,
                            array_size: Cast[windows.Uint](z.layers),
                        ))
                    } else {
                        dsv.view_dimension = d3d12.D3d12DsvDimensionTexture2dms
                        dsv.anon0.SetTexture2DMS(d3d12.D3d12Tex2dmsDsv(unused_field_nothing_to_define: 0))
                    }
                    (*(*dev).dev).CreateDepthStencilView(z.res, raw.Of(&dsv), dsv_cpu)
                    (*cmd).ClearDepthStencilView(dsv_cpu, cflags, 0.0, 0, 0, raw.Null[d3d12.D3d12Rect]())
                } else {
                    for m in 0..int(z.mips) {
                        for l in 0..int(z.layers) {
                            var dsv := mem.Zeroed[d3d12.D3d12DepthStencilViewDesc]()
                            dsv.format = Cast[dxgi.DxgiFormat](z.dxgi_rtv_dsv)
                            if z.layers > 1 {
                                dsv.view_dimension = d3d12.D3d12DsvDimensionTexture2darray
                                dsv.anon0.SetTexture2DArray(d3d12.D3d12Tex2dArrayDsv(
                                    mip_slice: Cast[windows.Uint](m),
                                    first_array_slice: Cast[windows.Uint](l),
                                    array_size: 1,
                                ))
                            } else {
                                dsv.view_dimension = d3d12.D3d12DsvDimensionTexture2d
                                dsv.anon0.SetTexture2D(d3d12.D3d12Tex2dDsv(mip_slice: Cast[windows.Uint](m)))
                            }
                            (*(*dev).dev).CreateDepthStencilView(z.res, raw.Of(&dsv), dsv_cpu)
                            (*cmd).ClearDepthStencilView(dsv_cpu, cflags, 0.0, 0, 0, raw.Null[d3d12.D3d12Rect]())
                        }
                    }
                }
                EmitTransition(cmd, z.res, D3d12AllSubresources, d3d12.D3d12ResourceStateDepthWrite, d3d12.D3d12ResourceStateCommon)
            } else {
                // CopyTextureRegion from zero_buf for non-RT/DS textures.
                EmitTransition(cmd, z.res, D3d12AllSubresources, d3d12.D3d12ResourceStateCommon, d3d12.D3d12ResourceStateCopyDest)
                for p in 0..int(z.planes) {
                    bb := if p == 1 { 1 } else if z.dxgi_fmt == 19 { 4 } else { z.block_bytes }
                    for m in 0..int(z.mips) {
                        mw := z.width >> m
                        mh := z.height >> m
                        w := if mw > 0 { mw } else { 1 }
                        h := if mh > 0 { mh } else { 1 }
                        aw := ((w + z.block_w - 1) / z.block_w) * z.block_w
                        ah := ((h + z.block_h - 1) / z.block_h) * z.block_h
                        bx := aw / z.block_w
                        by := ah / z.block_h
                        pitch := ((bx * bb) + D3d12RowPitchAlign - 1) & ~(D3d12RowPitchAlign - 1)
                        max_block_rows := Cast[int]((*dev).zero_buf_size) / pitch
                        strip_bh := if max_block_rows < by { max_block_rows } else { by }
                        strip_h := strip_bh * z.block_h
                        for l in 0..int(z.layers) {
                            sub := Cast[windows.Uint](m + l * int(z.mips) + p * int(z.mips) * int(z.layers))
                            var y0 := 0
                            for y0 < ah {
                                rem_h := ah - y0
                                cur_h := if rem_h > strip_h { strip_h } else { rem_h }
                                var dst_loc := mem.Zeroed[d3d12.D3d12TextureCopyLocation]()
                                dst_loc.p_resource = z.res
                                dst_loc.type_ = d3d12.D3d12TextureCopyTypeSubresourceIndex
                                dst_loc.anon0.SetSubresourceIndex(sub)

                                var src_loc := mem.Zeroed[d3d12.D3d12TextureCopyLocation]()
                                src_loc.p_resource = (*dev).zero_buf
                                src_loc.type_ = d3d12.D3d12TextureCopyTypePlacedFootprint
                                src_loc.anon0.SetPlacedFootprint(d3d12.D3d12PlacedSubresourceFootprint(
                                    offset: 0,
                                    footprint: d3d12.D3d12SubresourceFootprint(
                                        format: Cast[dxgi.DxgiFormat](z.dxgi_fmt),
                                        width: Cast[windows.Uint](aw),
                                        height: Cast[windows.Uint](cur_h),
                                        depth: 1,
                                        row_pitch: Cast[windows.Uint](pitch),
                                    ),
                                ))
                                var bx_src := d3d12.D3d12Box(left: 0, top: 0, front: 0, right: Cast[windows.Uint](aw), bottom: Cast[windows.Uint](cur_h), back: 1)
                                (*cmd).CopyTextureRegion(raw.Of(&dst_loc), 0, Cast[windows.Uint](y0), 0, raw.Of(&src_loc), raw.Of(&bx_src))
                                y0 += cur_h
                            }
                        }
                    }
                }
                EmitTransition(cmd, z.res, D3d12AllSubresources, d3d12.D3d12ResourceStateCopyDest, d3d12.D3d12ResourceStateCommon)
            }
        }

        // 2. Device.WriteBuffer / Device.WriteTexture uploads.
        for i in 0..(*dev).pending_uploads.Len() {
            u := &(*dev).pending_uploads[i]
            if u.kind == 0 {
                (*cmd).CopyBufferRegion(u.dst_res, u.dst_off, u.src_res, u.src_off, u.bytes)
            } else {
                before := MapResState(u.cur_state)
                EmitTransition(cmd, u.dst_res, u.subresource, before, d3d12.D3d12ResourceStateCopyDest)
                var dst_loc := mem.Zeroed[d3d12.D3d12TextureCopyLocation]()
                dst_loc.p_resource = u.dst_res
                dst_loc.type_ = d3d12.D3d12TextureCopyTypeSubresourceIndex
                dst_loc.anon0.SetSubresourceIndex(u.subresource)

                var src_loc := mem.Zeroed[d3d12.D3d12TextureCopyLocation]()
                src_loc.p_resource = u.src_res
                src_loc.type_ = d3d12.D3d12TextureCopyTypePlacedFootprint
                src_loc.anon0.SetPlacedFootprint(d3d12.D3d12PlacedSubresourceFootprint(
                    offset: u.src_off,
                    footprint: d3d12.D3d12SubresourceFootprint(
                        format: Cast[dxgi.DxgiFormat](u.dxgi_fmt),
                        width: u.width,
                        height: u.height,
                        depth: 1,
                        row_pitch: u.row_pitch,
                    ),
                ))
                var bx_src := d3d12.D3d12Box(left: 0, top: 0, front: 0, right: u.width, bottom: u.height, back: 1)
                (*cmd).CopyTextureRegion(raw.Of(&dst_loc), u.dst_x, u.dst_y, 0, raw.Of(&src_loc), raw.Of(&bx_src))
                EmitTransition(cmd, u.dst_res, u.subresource, d3d12.D3d12ResourceStateCopyDest, before)
            }
        }
    }
}

struct ExecListArgs {
    queue: *d3d12.ID3D12CommandQueue
    cmd: *d3d12.ID3D12GraphicsCommandList
    fence: *d3d12.ID3D12Fence
    serial: u64
    close_hr: windows.Hresult
    sig_hr: windows.Hresult
}

ExecListSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to live ExecListArgs" {
        p := raw.PtrCast[u8, ExecListArgs](arg)
        hr := (*(*p).cmd).Close()
        (*p).close_hr = hr
        if hr < 0 {
            return
        }
        var lists: [1]*d3d12.ID3D12CommandList = [raw.PtrCast[d3d12.ID3D12GraphicsCommandList, d3d12.ID3D12CommandList]((*p).cmd)]
        (*(*p).queue).ExecuteCommandLists(1, raw.Of(&lists[0]))
        (*p).sig_hr = (*(*p).queue).Signal((*p).fence, (*p).serial)
    }
}

struct ResetListArgs {
    alloc: *d3d12.ID3D12CommandAllocator
    cmd: *d3d12.ID3D12GraphicsCommandList
    cbv_heap: *d3d12.ID3D12DescriptorHeap
    smp_heap: *d3d12.ID3D12DescriptorHeap
    hr: windows.Hresult
}

ResetListSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to live ResetListArgs" {
        p := raw.PtrCast[u8, ResetListArgs](arg)
        hr := (*(*p).alloc).Reset()
        if hr < 0 {
            (*p).hr = hr
            return
        }
        hr2 := (*(*p).cmd).Reset((*p).alloc, raw.Null[d3d12.ID3D12PipelineState]())
        (*p).hr = hr2
        if hr2 >= 0 {
            var heaps: [2]*d3d12.ID3D12DescriptorHeap = [(*p).cbv_heap, (*p).smp_heap]
            (*(*p).cmd).SetDescriptorHeaps(2, raw.Of(&heaps[0]))
        }
    }
}

struct FlushPreArgs {
    dev: *Device
    alloc: *d3d12.ID3D12CommandAllocator
    serial: u64
    hr: windows.Hresult
    step: u8
}

FlushPreSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to live FlushPreArgs" {
        p := raw.PtrCast[u8, FlushPreArgs](arg)
        d := (*p).dev
        hr := (*(*p).alloc).Reset()
        if hr < 0 {
            (*p).hr = hr
            (*p).step = 0
            return
        }
        hr2 := (*(*d).cmd_list).Reset((*p).alloc, raw.Null[d3d12.ID3D12PipelineState]())
        if hr2 < 0 {
            (*p).hr = hr2
            (*p).step = 0
            return
        }
        RecordPendingZerosAndUploads(d, (*d).cmd_list)
        hr3 := (*(*d).cmd_list).Close()
        if hr3 < 0 {
            (*p).hr = hr3
            (*p).step = 1
            return
        }
        var lists: [1]*d3d12.ID3D12CommandList = [raw.PtrCast[d3d12.ID3D12GraphicsCommandList, d3d12.ID3D12CommandList]((*d).cmd_list)]
        (*(*d).queue).ExecuteCommandLists(1, raw.Of(&lists[0]))
        hr4 := (*(*d).queue).Signal((*d).fence, (*p).serial)
        (*p).hr = hr4
        (*p).step = 2
    }
}

/// Returns true when creation zeroing or Device.Write* copies are queued.
export Device.HasPendingPreSubmit(&self): bool {
    return self.pending_zeros.Len() > 0 || self.pending_uploads.Len() > 0
}

/// Submits queued creation zeroing and Device.Write* uploads under serial.
export Device.FlushPreSubmit(mut self, serial: u64): Error!void {
    if Target.os != Os.Windows {
        fail Unsupported
    }
    if !self.HasPendingPreSubmit() {
        return
    }
    if self.lost {
        self.pending_zeros.Clear()
        self.pending_uploads.Clear()
        fail DxFail(kind: self.loss_reason, code: 0, call: CallExecuteCommandLists, at: -1)
    }
    alloc_idx := try AcquireCmdAllocator(self)
    var args := FlushPreArgs(
        dev: raw.OfMut(self),
        alloc: self.cmd_allocs[alloc_idx].alloc,
        serial: serial,
        hr: 0,
        step: 0,
    )
    SysRun(mut args, FlushPreSys)
    self.pending_zeros.Clear()
    self.pending_uploads.Clear()
    self.cmd_allocs[alloc_idx].serial = serial
    self.StampUploadInflight(serial)
    self.last_submitted = serial
    self.present_pending = false
    has_dbg_err := self.DrainInfoQueue()
    if args.hr < 0 {
        fail MapHrError(self, args.hr, CallExecuteCommandLists)
    }
    if has_dbg_err {
        fail DxFail(kind: ErrKind.DebugLayer, code: 0, call: CallExecuteCommandLists, at: -1)
    }
}

struct ReadTexRecordArgs {
    dev: *Device
    alloc: *d3d12.ID3D12CommandAllocator
    src_res: *d3d12.ID3D12Resource
    rb_res: *d3d12.ID3D12Resource
    dxgi_fmt: u32
    cur_state: u16
    mip: int
    layer: int
    x: int
    y: int
    width: int
    height: int
    layers: int
    bw: int
    bh: int
    total_mips: int
    total_layers: int
    plane: int
    row_pitch: int
    slice_bytes: int
    serial: u64
    hr: windows.Hresult
}

ReadTexRecordSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to live ReadTexRecordArgs" {
        p := raw.PtrCast[u8, ReadTexRecordArgs](arg)
        d := (*p).dev
        cmd := (*d).cmd_list
        hr := (*(*p).alloc).Reset()
        if hr < 0 {
            (*p).hr = hr
            return
        }
        hr2 := (*cmd).Reset((*p).alloc, raw.Null[d3d12.ID3D12PipelineState]())
        if hr2 < 0 {
            (*p).hr = hr2
            return
        }
        RecordPendingZerosAndUploads(d, cmd)
        before := MapResState((*p).cur_state)
        bx := ((*p).width + (*p).bw - 1) / (*p).bw
        by := ((*p).height + (*p).bh - 1) / (*p).bh
        cw := bx * (*p).bw
        ch := by * (*p).bh
        for li in 0..(*p).layers {
            sub := Cast[windows.Uint]((*p).mip + ((*p).layer + li) * (*p).total_mips + (*p).plane * (*p).total_mips * (*p).total_layers)
            EmitTransition(cmd, (*p).src_res, sub, before, d3d12.D3d12ResourceStateCopySource)
            var dst_loc := mem.Zeroed[d3d12.D3d12TextureCopyLocation]()
            dst_loc.p_resource = (*p).rb_res
            dst_loc.type_ = d3d12.D3d12TextureCopyTypePlacedFootprint
            dst_loc.anon0.SetPlacedFootprint(d3d12.D3d12PlacedSubresourceFootprint(
                offset: Cast[u64](li * (*p).slice_bytes),
                footprint: d3d12.D3d12SubresourceFootprint(
                    format: Cast[dxgi.DxgiFormat]((*p).dxgi_fmt),
                    width: Cast[windows.Uint](cw),
                    height: Cast[windows.Uint](ch),
                    depth: 1,
                    row_pitch: Cast[windows.Uint]((*p).row_pitch),
                ),
            ))
            var src_loc := mem.Zeroed[d3d12.D3d12TextureCopyLocation]()
            src_loc.p_resource = (*p).src_res
            src_loc.type_ = d3d12.D3d12TextureCopyTypeSubresourceIndex
            src_loc.anon0.SetSubresourceIndex(sub)
            var bx_src := d3d12.D3d12Box(
                left: Cast[windows.Uint]((*p).x),
                top: Cast[windows.Uint]((*p).y),
                front: 0,
                right: Cast[windows.Uint]((*p).x + cw),
                bottom: Cast[windows.Uint]((*p).y + ch),
                back: 1,
            )
            (*cmd).CopyTextureRegion(raw.Of(&dst_loc), 0, 0, 0, raw.Of(&src_loc), raw.Of(&bx_src))
            EmitTransition(cmd, (*p).src_res, sub, d3d12.D3d12ResourceStateCopySource, before)
        }
        hr3 := (*cmd).Close()
        if hr3 < 0 {
            (*p).hr = hr3
            return
        }
        var lists: [1]*d3d12.ID3D12CommandList = [raw.PtrCast[d3d12.ID3D12GraphicsCommandList, d3d12.ID3D12CommandList](cmd)]
        (*(*d).queue).ExecuteCommandLists(1, raw.Of(&lists[0]))
        (*p).hr = (*(*d).queue).Signal((*d).fence, (*p).serial)
    }
}

struct ReadbackMapArgs {
    res: *d3d12.ID3D12Resource
    end_bytes: u64
    out_ptr: *u8
    hr: windows.Hresult
}

ReadbackMapSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to live ReadbackMapArgs" {
        p := raw.PtrCast[u8, ReadbackMapArgs](arg)
        var mp: *void = raw.Null[void]()
        rng := d3d12.D3d12Range(begin: 0, end: (*p).end_bytes)
        hr := (*(*p).res).Map(0, raw.Of(&rng), raw.OfMut(mut mp))
        (*p).hr = hr
        if hr >= 0 && !raw.IsNull(mp) {
            (*p).out_ptr = raw.PtrCast[void, u8](mp)
        }
    }
}

ReadbackUnmapSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to *ID3D12Resource" {
        pp := raw.PtrCast[u8, *d3d12.ID3D12Resource](arg)
        res := raw.Read(pp)
        if !raw.IsNull(res) {
            empty_r := mem.Zeroed[d3d12.D3d12Range]()
            (*res).Unmap(0, raw.Of(&empty_r))
        }
    }
}

/// Copies a texture region into a temporary READBACK buffer, waits, and returns tight rows in a.
export Device.ReadTextureSync(
    mut self,
    a: &Arena,
    io: Io,
    src_nh: u64,
    mip: int,
    layer: int,
    x: int,
    y: int,
    width: int,
    height: int,
    layers: int,
    aspect: u8,
    cur_state: u16,
    need_bytes: int,
    serial: u64,
): Error![]u8 from a {
    if Target.os != Os.Windows {
        fail Unsupported
    }
    if self.lost {
        fail DxFail(kind: self.loss_reason, code: 0, call: CallMap, at: -1)
    }
    var src_res := raw.Null[d3d12.ID3D12Resource]()
    var dxgi_fmt: u32 = 0
    var bw := 1
    var bh := 1
    var bbytes := 4
    var total_mips := 1
    var total_layers := 1
    unsafe "src_nh is a live DxTexture" {
        tp := raw.FromAddr[DxTexture](Trunc[uint](src_nh))
        src_res = (*tp).res
        dxgi_fmt = (*tp).dxgi_res
        bw = (*tp).block_w
        bh = (*tp).block_h
        bbytes = (*tp).block_bytes
        total_mips = (*tp).mips
        total_layers = (*tp).layers
    }
    var copy_fmt := dxgi_fmt
    if aspect == 2 {
        bbytes = 1
        if dxgi_fmt == 44 || dxgi_fmt == 19 {
            copy_fmt = 62 // DXGI_FORMAT_R8_UINT
        }
    } else if aspect == 1 && dxgi_fmt == 19 {
        bbytes = 4
        copy_fmt = 41 // DXGI_FORMAT_R32_FLOAT
    }
    bx := (width + bw - 1) / bw
    by := (height + bh - 1) / bh
    row_bytes := bx * bbytes
    row_pitch := (row_bytes + D3d12RowPitchAlign - 1) & ~(D3d12RowPitchAlign - 1)
    slice_bytes := row_pitch * by
    total_bytes := slice_bytes * layers

    desc := BufferResourceDesc(Cast[u64](total_bytes), d3d12.D3d12ResourceFlagNone)
    var rargs := CreateResArgs(
        dev: self.dev,
        placed: false,
        heap: raw.Null[d3d12.ID3D12Heap](),
        heap_off: 0,
        heap_type: d3d12.D3d12HeapTypeReadback,
        desc: desc,
        init_state: d3d12.D3d12ResourceStateCopyDest,
        has_clear: false,
        clear_val: mem.Zeroed[d3d12.D3d12ClearValue](),
        map_read_or_write: false,
        is_readback: true,
        out_res: raw.Null[d3d12.ID3D12Resource](),
        gpu_va: 0,
        mapped: raw.Null[u8](),
        hr: 0,
    )
    SysRun(mut rargs, CreateResSys)
    if rargs.hr < 0 || raw.IsNull(rargs.out_res) {
        fail MapHrError(self, rargs.hr, CallCreateResource)
    }
    defer ReleaseResPtr(rargs.out_res, false)

    alloc_idx := try AcquireCmdAllocator(self)
    plane := if aspect == 2 { 1 } else { 0 }
    var rec_args := ReadTexRecordArgs(
        dev: raw.OfMut(self),
        alloc: self.cmd_allocs[alloc_idx].alloc,
        src_res: src_res,
        rb_res: rargs.out_res,
        dxgi_fmt: copy_fmt,
        cur_state: cur_state,
        mip: mip,
        layer: layer,
        x: x,
        y: y,
        width: width,
        height: height,
        layers: layers,
        bw: bw,
        bh: bh,
        total_mips: total_mips,
        total_layers: total_layers,
        plane: plane,
        row_pitch: row_pitch,
        slice_bytes: slice_bytes,
        serial: serial,
        hr: 0,
    )
    SysRun(mut rec_args, ReadTexRecordSys)
    self.pending_zeros.Clear()
    self.pending_uploads.Clear()
    self.cmd_allocs[alloc_idx].serial = serial
    self.StampUploadInflight(serial)
    self.last_submitted = serial
    self.present_pending = false
    has_dbg_err := self.DrainInfoQueue()
    if rec_args.hr < 0 {
        fail MapHrError(self, rec_args.hr, CallExecuteCommandLists)
    }
    self.WaitSerial(io, serial) else e {
        match e {
            Canceled { _ = self.WaitSerial(io.NoCancel(), serial) else {} }
            _ {}
        }
        fail e
    }
    if has_dbg_err {
        fail DxFail(kind: ErrKind.DebugLayer, code: 0, call: CallExecuteCommandLists, at: -1)
    }

    var out := try a.Slice[u8](need_bytes)
    var map_args := ReadbackMapArgs(
        res: rargs.out_res,
        end_bytes: Cast[u64](total_bytes),
        out_ptr: raw.Null[u8](),
        hr: 0,
    )
    SysRun(mut map_args, ReadbackMapSys)
    if map_args.hr < 0 || raw.IsNull(map_args.out_ptr) {
        fail MapHrError(self, map_args.hr, CallMap)
    }
    src_u8 := map_args.out_ptr
    var dst_idx := 0
    for li in 0..layers {
        slice_base := li * slice_bytes
        for ry in 0..by {
            src_row_off := slice_base + ry * row_pitch
            unsafe "src_u8 + src_row_off has row_bytes and out has need_bytes" {
                raw.Copy(raw.Offset(raw.DataMut(out), dst_idx), raw.Offset(src_u8, src_row_off), row_bytes)
            }
            dst_idx += row_bytes
        }
    }
    var rb_res_unmap := rargs.out_res
    SysRun(mut rb_res_unmap, ReadbackUnmapSys)
    return out[..]
}
