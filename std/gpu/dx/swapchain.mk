// Why: D3D12 flip-model swapchains require MkSystemRun for creation,
// ResizeBuffers, and Present, bounded frame-latency ObjectWait with credit
// accounting, linear back buffers with sRGB RTVs, and full recreation after
// abandoned frames.
//
// What: Device.CreateSwapchain, DestroySwapchain, AcquireSwapchain,
// AbandonSwapchain, PresentSwapchain, ResizeSwapchain, and
// SwapchainWaitableHandle.
//
// How: creates FLIP_DISCARD swapchains with FRAME_LATENCY_WAITABLE_OBJECT and
// optional ALLOW_TEARING, paces on per-frame present serials + ObjectWait(100ms),
// and drops all back-buffer references before ResizeBuffers or recreation.

import c.windows
import c.dxgi
import c.d3d12
import std.core
import std.raw
import std.mem
import std.rt

const LatencyTimeoutNs: i64 = 100000000 // 100 ms

struct CreateScArgs {
    factory: *dxgi.IdxgiFactory6
    queue: *d3d12.ID3D12CommandQueue
    dev: *d3d12.ID3D12Device
    hwnd: windows.Hwnd
    w: windows.Uint
    h: windows.Uint
    dxgi_swap: dxgi.DxgiFormat
    dxgi_rtv: dxgi.DxgiFormat
    buffers: windows.Uint
    flags: windows.Uint
    out_sc: *dxgi.IdxgiSwapChain3
    waitable: uint
    back_res: [3]*d3d12.ID3D12Resource
    rtv_ptrs: [3]u64
    hr: windows.Hresult
}

CreateScSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to live CreateScArgs" {
        p := raw.PtrCast[u8, CreateScArgs](arg)
        desc := dxgi.DxgiSwapChainDesc1(
            width: (*p).w,
            height: (*p).h,
            format: (*p).dxgi_swap,
            stereo: 0,
            sample_desc: dxgi.DxgiSampleDesc(count: 1, quality: 0),
            buffer_usage: dxgi.DxgiUsageRenderTargetOutput,
            buffer_count: (*p).buffers,
            scaling: dxgi.DxgiScalingStretch,
            swap_effect: dxgi.DxgiSwapEffectFlipDiscard,
            alpha_mode: dxgi.DxgiAlphaModeIgnore,
            flags: (*p).flags,
        )
        var sc1 := raw.Null[dxgi.IdxgiSwapChain1]()
        hr := (*(*p).factory).CreateSwapChainForHwnd(
            raw.PtrCast[d3d12.ID3D12CommandQueue, windows.IUnknown]((*p).queue),
            (*p).hwnd,
            raw.Of(&desc),
            raw.Null[dxgi.DxgiSwapChainFullscreenDesc](),
            raw.Null[dxgi.IdxgiOutput](),
            raw.OfMut(mut sc1),
        )
        (*p).hr = hr
        if hr < 0 || raw.IsNull(sc1) {
            return
        }
        _ = (*(*p).factory).MakeWindowAssociation((*p).hwnd, DxgiMwaNoAltEnter)
        var sc3_ptr: *void = raw.Null[void]()
        qhr := (*sc1).QueryInterface(raw.Of(&dxgi.IidIdxgiSwapChain3), raw.OfMut(mut sc3_ptr))
        _ = (*sc1).Release()
        if qhr < 0 || raw.IsNull(sc3_ptr) {
            (*p).hr = qhr
            return
        }
        sc3 := raw.PtrCast[void, dxgi.IdxgiSwapChain3](sc3_ptr)
        (*p).out_sc = sc3
        w := (*sc3).GetFrameLatencyWaitableObject()
        (*p).waitable = if raw.IsNull(w) { 0 } else { raw.Addr(w) }

        var i: windows.Uint = 0
        for i < (*p).buffers {
            var buf_ptr: *void = raw.Null[void]()
            ghr := (*sc3).GetBuffer(i, raw.Of(&d3d12.IidID3D12Resource), raw.OfMut(mut buf_ptr))
            if ghr < 0 || raw.IsNull(buf_ptr) {
                (*p).hr = ghr
                return
            }
            res := raw.PtrCast[void, d3d12.ID3D12Resource](buf_ptr)
            (*p).back_res[int(i)] = res
            var rtv := mem.Zeroed[d3d12.D3d12RenderTargetViewDesc]()
            rtv.format = (*p).dxgi_rtv
            rtv.view_dimension = d3d12.D3d12RtvDimensionTexture2d
            rtv.anon0.SetTexture2D(d3d12.D3d12Tex2dRtv(mip_slice: 0, plane_slice: 0))
            (*(*p).dev).CreateRenderTargetView(res, raw.Of(&rtv), d3d12.D3d12CpuDescriptorHandle(ptr: (*p).rtv_ptrs[int(i)]))
            i += 1
        }
    }
}

struct SetMaxLatArgs {
    sc: *dxgi.IdxgiSwapChain3
    latency: windows.Uint
}

SetMaxLatSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to live SetMaxLatArgs" {
        p := raw.PtrCast[u8, SetMaxLatArgs](arg)
        _ = (*(*p).sc).SetMaximumFrameLatency((*p).latency)
    }
}

struct ReleaseScArgs {
    sc: *dxgi.IdxgiSwapChain3
    waitable: uint
    buffers: int
    back_res: [3]*d3d12.ID3D12Resource
}

ReleaseScSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to live ReleaseScArgs" {
        p := raw.PtrCast[u8, ReleaseScArgs](arg)
        for i in 0..(*p).buffers {
            if !raw.IsNull((*p).back_res[i]) {
                _ = (*(*p).back_res[i]).Release()
                (*p).back_res[i] = raw.Null[d3d12.ID3D12Resource]()
            }
        }
        if (*p).waitable != 0 {
            _ = windows.CloseHandle(raw.FromAddr[void]((*p).waitable))
        }
        if !raw.IsNull((*p).sc) {
            _ = (*(*p).sc).Release()
        }
    }
}

/// Creates a D3D12 flip-discard swapchain and a DxView representing its current back buffer.
export Device.CreateSwapchain(
    mut self,
    spec: &SwapchainSpec,
): Error!(sc_nh: u64, view_nh: u64, width: int, height: int, images: int, mode: u8) {
    if Target.os != Os.Windows {
        fail Unsupported
    }
    if self.lost {
        fail DxFail(kind: self.loss_reason, code: 0, call: CallCreateSwapchain, at: -1)
    }
    a := self.heap.Alloc()
    var actual_mode := spec.mode
    if actual_mode == 2 && !self.tearing_ok {
        actual_mode = 1
    }
    var flags := Cast[windows.Uint](dxgi.DxgiSwapChainFlagFrameLatencyWaitableObject)
    if self.tearing_ok {
        flags |= Cast[windows.Uint](dxgi.DxgiSwapChainFlagAllowTearing)
    }

    var rtv_slots: [3]u64 = [0; 3]
    var rtv_ptrs: [3]u64 = [0; 3]
    for i in 0..spec.buffers {
        s := self.rtv_heap.AllocRange(a, 1) else {
            for j in 0..i {
                self.rtv_heap.FreeRange(a, rtv_slots[j], 1)
            }
            fail DxFail(kind: ErrKind.LimitExceeded, code: 0, call: CallCreateHeap, at: -1)
        }
        rtv_slots[i] = s
        rtv_ptrs[i] = self.rtv_heap.CpuAt(s).ptr
    }

    cw := if spec.width > 0 { spec.width } else { 1 }
    ch := if spec.height > 0 { spec.height } else { 1 }
    var hwnd_ptr := raw.Null[windows.HwndStruct]()
    unsafe "spec.hwnd is a valid Win32 HWND" { hwnd_ptr = raw.FromAddr[windows.HwndStruct](spec.hwnd) }
    var cargs := CreateScArgs(
        factory: self.factory,
        queue: self.queue,
        dev: self.dev,
        hwnd: hwnd_ptr,
        w: Cast[windows.Uint](cw),
        h: Cast[windows.Uint](ch),
        dxgi_swap: Cast[dxgi.DxgiFormat](spec.dxgi_res),
        dxgi_rtv: Cast[dxgi.DxgiFormat](spec.dxgi_rtv),
        buffers: Cast[windows.Uint](spec.buffers),
        flags: flags,
        out_sc: raw.Null[dxgi.IdxgiSwapChain3](),
        waitable: 0,
        back_res: [raw.Null[d3d12.ID3D12Resource](); 3],
        rtv_ptrs: rtv_ptrs,
        hr: 0,
    )
    SysRun(mut cargs, CreateScSys)
    _ = self.DrainInfoQueue()
    if cargs.hr < 0 || raw.IsNull(cargs.out_sc) {
        var rargs := ReleaseScArgs(
            sc: cargs.out_sc,
            waitable: cargs.waitable,
            buffers: spec.buffers,
            back_res: cargs.back_res,
        )
        SysRun(mut rargs, ReleaseScSys)
        for i in 0..spec.buffers {
            self.rtv_heap.FreeRange(a, rtv_slots[i], 1)
        }
        if cargs.hr == -2005270494 { // DXGI_ERROR_INVALID_CALL
            fail DxFail(kind: ErrKind.SurfaceLost, code: i64(cargs.hr), call: CallCreateSwapchain, at: -1)
        }
        fail MapHrError(self, cargs.hr, CallCreateSwapchain)
    }

    var lat_args := SetMaxLatArgs(sc: cargs.out_sc, latency: Cast[windows.Uint](self.frames_in_flight))
    SysRun(mut lat_args, SetMaxLatSys)

    vrec := DxView(
        tex_res: raw.Null[d3d12.ID3D12Resource](),
        dxgi_srv: 0,
        dxgi_stencil_srv: 0,
        dim: 0,
        aspect: 0,
        first_mip: 0,
        mip_count: 1,
        first_layer: 0,
        layer_count: 1,
        samples: 1,
        is_depth: false,
        has_rtv: true,
        rtv_slot: 0,
        rtv_ptr: rtv_ptrs[0],
        has_dsv: false,
        dsv_rw_slot: 0,
        dsv_rw_ptr: 0,
        dsv_ro_slot: 0,
        dsv_ro_ptr: 0,
        is_swapchain: true,
        swap_res: cargs.back_res[0],
    )
    view_nh := AllocRec(a, vrec) else e {
        var rargs := ReleaseScArgs(sc: cargs.out_sc, waitable: cargs.waitable, buffers: spec.buffers, back_res: cargs.back_res)
        SysRun(mut rargs, ReleaseScSys)
        for i in 0..spec.buffers {
            self.rtv_heap.FreeRange(a, rtv_slots[i], 1)
        }
        fail e
    }

    srec := DxSwapchain(
        sc: cargs.out_sc,
        hwnd: hwnd_ptr,
        waitable: cargs.waitable,
        dxgi_res: spec.dxgi_res,
        dxgi_rtv: spec.dxgi_rtv,
        mode: actual_mode,
        buffers: Cast[u32](spec.buffers),
        copy_src: spec.copy_src,
        width: spec.width,
        height: spec.height,
        flags: flags,
        back_res: cargs.back_res,
        rtv_slots: rtv_slots,
        rtv_ptrs: rtv_ptrs,
        present_serials: [0; 3],
        frame_idx: 0,
        needs_recreate: spec.width == 0 || spec.height == 0,
        abandoned: false,
        credit_held: false,
    )
    sc_nh := AllocRec(a, srec) else e {
        _ = FreeRec[DxView](a, view_nh)
        var rargs := ReleaseScArgs(sc: cargs.out_sc, waitable: cargs.waitable, buffers: spec.buffers, back_res: cargs.back_res)
        SysRun(mut rargs, ReleaseScSys)
        for i in 0..spec.buffers {
            self.rtv_heap.FreeRange(a, rtv_slots[i], 1)
        }
        fail e
    }

    return (sc_nh: sc_nh, view_nh: view_nh, width: spec.width, height: spec.height, images: spec.buffers, mode: actual_mode)
}

/// Releases a D3D12 swapchain and its back-buffer references and RTV slots.
export Device.DestroySwapchain(mut self, nh: u64) {
    if Target.os != Os.Windows || nh == 0 {
        return
    }
    a := self.heap.Alloc()
    rec := FreeRec[DxSwapchain](a, nh)
    var rargs := ReleaseScArgs(
        sc: rec.sc,
        waitable: rec.waitable,
        buffers: Cast[int](rec.buffers),
        back_res: rec.back_res,
    )
    SysRun(mut rargs, ReleaseScSys)
    for i in 0..Cast[int](rec.buffers) {
        self.rtv_heap.FreeRange(a, rec.rtv_slots[i], 1)
    }
}

struct CurIndexArgs {
    sc: *dxgi.IdxgiSwapChain3
    idx: u32
}

CurIndexSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to live CurIndexArgs" {
        p := raw.PtrCast[u8, CurIndexArgs](arg)
        (*p).idx = (*(*p).sc).GetCurrentBackBufferIndex()
    }
}

/// Paces frames in flight and waits on the DXGI frame latency waitable object.
export Device.AcquireSwapchain(
    mut self,
    io: Io,
    sc_nh: u64,
    view_nh: u64,
    cur_w: int,
    cur_h: int,
): Error!AcquireResult {
    if Target.os != Os.Windows {
        fail Unsupported
    }
    if self.lost {
        fail DxFail(kind: self.loss_reason, code: 0, call: CallPresent, at: -1)
    }
    var scp := raw.Null[DxSwapchain]()
    unsafe "sc_nh is a live DxSwapchain" {
        scp = raw.FromAddr[DxSwapchain](Trunc[uint](sc_nh))
        if (*scp).abandoned {
            return AcquireResult(status: 1, image_idx: 0, res_ptr: 0, rtv_ptr: 0)
        }
        if cur_w == 0 || cur_h == 0 {
            return AcquireResult(status: 2, image_idx: 0, res_ptr: 0, rtv_ptr: 0)
        }
        if (*scp).needs_recreate {
            return AcquireResult(status: 1, image_idx: 0, res_ptr: 0, rtv_ptr: 0)
        }
    }

    var pace_serial: u64 = 0
    var waitable: uint = 0
    var credit_held := false
    unsafe "scp is live" {
        slot := Cast[int]((*scp).frame_idx % Cast[u64](self.frames_in_flight))
        pace_serial = (*scp).present_serials[slot]
        waitable = (*scp).waitable
        credit_held = (*scp).credit_held
    }
    if pace_serial > 0 {
        try self.WaitSerial(io, pace_serial)
    }
    if credit_held {
        unsafe "scp is live" { (*scp).credit_held = false }
    } else if waitable != 0 {
        r := rt.ObjectWait(io, waitable, LatencyTimeoutNs)
        if r.err == core.SysErr.Canceled {
            if r.fired_after_cancel {
                unsafe "scp is live" { (*scp).credit_held = true }
            }
            fail Canceled
        }
    }

    var sc_ptr := raw.Null[dxgi.IdxgiSwapChain3]()
    unsafe "scp is live" { sc_ptr = (*scp).sc }
    var idx_args := CurIndexArgs(sc: sc_ptr, idx: 0)
    SysRun(mut idx_args, CurIndexSys)
    idx := idx_args.idx
    var res_ptr: u64 = 0
    var rtv_ptr: u64 = 0
    unsafe "scp and view_nh are live" {
        res := (*scp).back_res[int(idx)]
        rtv := (*scp).rtv_ptrs[int(idx)]
        vp := raw.FromAddr[DxView](Trunc[uint](view_nh))
        (*vp).swap_res = res
        (*vp).rtv_ptr = rtv
        res_ptr = Trunc[u64](raw.Addr(res))
        rtv_ptr = rtv
    }
    return AcquireResult(status: 0, image_idx: idx, res_ptr: res_ptr, rtv_ptr: rtv_ptr)
}

/// Marks a swapchain as abandoned so the next Acquire returns Outdated and Resize recreates it.
export AbandonSwapchain(sc_nh: u64) {
    if Target.os != Os.Windows || sc_nh == 0 {
        return
    }
    unsafe "sc_nh is a live DxSwapchain" {
        scp := raw.FromAddr[DxSwapchain](Trunc[uint](sc_nh))
        (*scp).abandoned = true
    }
}

struct PresentArgs {
    sc: *dxgi.IdxgiSwapChain3
    sync: windows.Uint
    flags: windows.Uint
    hr: windows.Hresult
}

PresentSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to live PresentArgs" {
        p := raw.PtrCast[u8, PresentArgs](arg)
        (*p).hr = (*(*p).sc).Present((*p).sync, (*p).flags)
    }
}

/// Presents the current back buffer on the system stack and records its frame serial.
export Device.PresentSwapchain(mut self, sc_nh: u64, last_submitted: u64): Error!bool {
    if Target.os != Os.Windows {
        fail Unsupported
    }
    if self.lost {
        fail DxFail(kind: self.loss_reason, code: 0, call: CallPresent, at: -1)
    }
    var pargs := PresentArgs(sc: raw.Null[dxgi.IdxgiSwapChain3](), sync: 1, flags: 0, hr: 0)
    unsafe "sc_nh is a live DxSwapchain" {
        scp := raw.FromAddr[DxSwapchain](Trunc[uint](sc_nh))
        pargs.sc = (*scp).sc
        if (*scp).mode == 0 {
            pargs.sync = 1
            pargs.flags = 0
        } else if (*scp).mode == 2 && self.tearing_ok {
            pargs.sync = 0
            pargs.flags = DxgiPresentAllowTearingFlag
        } else {
            pargs.sync = 0
            pargs.flags = 0
        }
    }
    SysRun(mut pargs, PresentSys)
    has_dbg_err := self.DrainInfoQueue()
    if pargs.hr < 0 && pargs.hr != HresultDxgiStatusOccluded {
        fail MapHrError(self, pargs.hr, CallPresent)
    }
    unsafe "sc_nh is live" {
        scp := raw.FromAddr[DxSwapchain](Trunc[uint](sc_nh))
        slot := Cast[int]((*scp).frame_idx % Cast[u64](self.frames_in_flight))
        (*scp).present_serials[slot] = last_submitted
        (*scp).frame_idx += 1
    }
    self.present_pending = true
    if has_dbg_err {
        fail DxFail(kind: ErrKind.DebugLayer, code: 0, call: CallPresent, at: -1)
    }
    return false
}

struct ResizeArgs {
    sc: *dxgi.IdxgiSwapChain3
    dev: *d3d12.ID3D12Device
    w: windows.Uint
    h: windows.Uint
    dxgi_rtv: dxgi.DxgiFormat
    buffers: windows.Uint
    flags: windows.Uint
    back_res: [3]*d3d12.ID3D12Resource
    rtv_ptrs: [3]u64
    hr: windows.Hresult
}

ResizeSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to live ResizeArgs with released back_res" {
        p := raw.PtrCast[u8, ResizeArgs](arg)
        for i in 0..Cast[int]((*p).buffers) {
            if !raw.IsNull((*p).back_res[i]) {
                _ = (*(*p).back_res[i]).Release()
                (*p).back_res[i] = raw.Null[d3d12.ID3D12Resource]()
            }
        }
        hr := (*(*p).sc).ResizeBuffers(0, (*p).w, (*p).h, dxgi.DxgiFormatUnknown, (*p).flags)
        (*p).hr = hr
        if hr < 0 {
            return
        }
        var i: windows.Uint = 0
        for i < (*p).buffers {
            var buf_ptr: *void = raw.Null[void]()
            ghr := (*(*p).sc).GetBuffer(i, raw.Of(&d3d12.IidID3D12Resource), raw.OfMut(mut buf_ptr))
            if ghr < 0 || raw.IsNull(buf_ptr) {
                (*p).hr = ghr
                return
            }
            res := raw.PtrCast[void, d3d12.ID3D12Resource](buf_ptr)
            (*p).back_res[int(i)] = res
            var rtv := mem.Zeroed[d3d12.D3d12RenderTargetViewDesc]()
            rtv.format = (*p).dxgi_rtv
            rtv.view_dimension = d3d12.D3d12RtvDimensionTexture2d
            rtv.anon0.SetTexture2D(d3d12.D3d12Tex2dRtv(mip_slice: 0, plane_slice: 0))
            (*(*p).dev).CreateRenderTargetView(res, raw.Of(&rtv), d3d12.D3d12CpuDescriptorHandle(ptr: (*p).rtv_ptrs[int(i)]))
            i += 1
        }
    }
}

/// Resizes the swapchain at width x height after waiting for last_serial, or
/// recreates it cleanly after an abandoned frame.
export Device.ResizeSwapchain(
    mut self,
    io: Io,
    sc_nh: u64,
    view_nh: u64,
    w: int,
    h: int,
    last_serial: u64,
): Error!void {
    if Target.os != Os.Windows {
        fail Unsupported
    }
    if self.lost {
        fail DxFail(kind: self.loss_reason, code: 0, call: CallResizeBuffers, at: -1)
    }
    _ = last_serial
    try self.WaitQueueIdle(io)
    if w == 0 || h == 0 {
        unsafe "sc_nh is live" {
            scp := raw.FromAddr[DxSwapchain](Trunc[uint](sc_nh))
            (*scp).width = 0
            (*scp).height = 0
            (*scp).needs_recreate = true
        }
        return
    }

    var was_abandoned := false
    var buffers: u32 = 2
    var hwnd := raw.Null[windows.HwndStruct]()
    var dxgi_res: u32 = 0
    var dxgi_rtv: u32 = 0
    var flags: windows.Uint = 0
    var old_sc := raw.Null[dxgi.IdxgiSwapChain3]()
    var old_waitable: uint = 0
    var back_res: [3]*d3d12.ID3D12Resource = [raw.Null[d3d12.ID3D12Resource](); 3]
    var rtv_ptrs: [3]u64 = [0; 3]

    unsafe "sc_nh is live" {
        scp := raw.FromAddr[DxSwapchain](Trunc[uint](sc_nh))
        was_abandoned = (*scp).abandoned
        buffers = (*scp).buffers
        hwnd = (*scp).hwnd
        dxgi_res = (*scp).dxgi_res
        dxgi_rtv = (*scp).dxgi_rtv
        flags = (*scp).flags
        old_sc = (*scp).sc
        old_waitable = (*scp).waitable
        back_res = (*scp).back_res
        rtv_ptrs = (*scp).rtv_ptrs
    }

    if was_abandoned {
        var rel := ReleaseScArgs(sc: old_sc, waitable: old_waitable, buffers: Cast[int](buffers), back_res: back_res)
        SysRun(mut rel, ReleaseScSys)
        var cargs := CreateScArgs(
            factory: self.factory,
            queue: self.queue,
            dev: self.dev,
            hwnd: hwnd,
            w: Cast[windows.Uint](w),
            h: Cast[windows.Uint](h),
            dxgi_swap: Cast[dxgi.DxgiFormat](dxgi_res),
            dxgi_rtv: Cast[dxgi.DxgiFormat](dxgi_rtv),
            buffers: buffers,
            flags: flags,
            out_sc: raw.Null[dxgi.IdxgiSwapChain3](),
            waitable: 0,
            back_res: [raw.Null[d3d12.ID3D12Resource](); 3],
            rtv_ptrs: rtv_ptrs,
            hr: 0,
        )
        SysRun(mut cargs, CreateScSys)
        _ = self.DrainInfoQueue()
        if cargs.hr < 0 || raw.IsNull(cargs.out_sc) {
            var rel2 := ReleaseScArgs(sc: cargs.out_sc, waitable: cargs.waitable, buffers: Cast[int](buffers), back_res: cargs.back_res)
            SysRun(mut rel2, ReleaseScSys)
            unsafe "clear dead pointers on scp" {
                scp := raw.FromAddr[DxSwapchain](Trunc[uint](sc_nh))
                (*scp).sc = raw.Null[dxgi.IdxgiSwapChain3]()
                (*scp).waitable = 0
                (*scp).back_res = [raw.Null[d3d12.ID3D12Resource](); 3]
                (*scp).needs_recreate = true
            }
            fail MapHrError(self, cargs.hr, CallCreateSwapchain)
        }
        var lat := SetMaxLatArgs(sc: cargs.out_sc, latency: Cast[windows.Uint](self.frames_in_flight))
        SysRun(mut lat, SetMaxLatSys)
        unsafe "update scp and view_nh" {
            scp := raw.FromAddr[DxSwapchain](Trunc[uint](sc_nh))
            (*scp).sc = cargs.out_sc
            (*scp).waitable = cargs.waitable
            (*scp).back_res = cargs.back_res
            (*scp).width = w
            (*scp).height = h
            (*scp).abandoned = false
            (*scp).needs_recreate = false
            (*scp).credit_held = false
            (*scp).present_serials = [0; 3]
            (*scp).frame_idx = 0
            vp := raw.FromAddr[DxView](Trunc[uint](view_nh))
            (*vp).swap_res = cargs.back_res[0]
            (*vp).rtv_ptr = rtv_ptrs[0]
        }
        return
    }

    var rargs := ResizeArgs(
        sc: old_sc,
        dev: self.dev,
        w: Cast[windows.Uint](w),
        h: Cast[windows.Uint](h),
        dxgi_rtv: Cast[dxgi.DxgiFormat](dxgi_rtv),
        buffers: buffers,
        flags: flags,
        back_res: back_res,
        rtv_ptrs: rtv_ptrs,
        hr: 0,
    )
    SysRun(mut rargs, ResizeSys)
    _ = self.DrainInfoQueue()
    unsafe "update back_res on scp" {
        scp := raw.FromAddr[DxSwapchain](Trunc[uint](sc_nh))
        (*scp).back_res = rargs.back_res
    }
    if rargs.hr < 0 {
        fail MapHrError(self, rargs.hr, CallResizeBuffers)
    }
    unsafe "update scp and view_nh after resize" {
        scp := raw.FromAddr[DxSwapchain](Trunc[uint](sc_nh))
        (*scp).width = w
        (*scp).height = h
        (*scp).needs_recreate = false
        vp := raw.FromAddr[DxView](Trunc[uint](view_nh))
        (*vp).swap_res = rargs.back_res[0]
        (*vp).rtv_ptr = rtv_ptrs[0]
    }
}

/// Returns the native waitable handle and credit_held state for tests.
export SwapchainWaitableState(sc_nh: u64): (waitable: uint, credit_held: bool) {
    if Target.os != Os.Windows || sc_nh == 0 {
        return (waitable: 0, credit_held: false)
    }
    unsafe "sc_nh is a live DxSwapchain" {
        scp := raw.FromAddr[DxSwapchain](Trunc[uint](sc_nh))
        return (waitable: (*scp).waitable, credit_held: (*scp).credit_held)
    }
}
