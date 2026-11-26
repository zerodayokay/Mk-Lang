// Why: D3D12 backend invariants—especially DESCRIPTORS_STATIC_KEEPING_BUFFER_BOUNDS_CHECKS
// out-of-bounds zeroing, swapchain WaitableObject credit accounting, resize/abandon
// recreation, Strict debug-layer cleanliness, and RemoveDevice device loss—must be
// verified against both WARP and hardware adapters.
//
// What: tests_dx.mk running backend-level D1 + D2b verification on MK_GPU_ADAPTER.
//
// How: decodes precompiled DXIL/DXBC test shaders, creates hidden Win32 HWNDs via
// c.windows when testing swapchains, and skips cleanly on non-Windows targets.

import c.windows
import std.compress
import std.encoding
import std.mem
import std.raw
import std.rt
import std.task

const TestPostInvertCsDxilZ: str = "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"

// Precompiled cs_5_0 DXBC shader reading cbuffer v[32] (512 bytes) and writing
// asuint(v[0].x), v[15].x, v[16].x, v[31].x into RWStructuredBuffer<uint> dst[0..3].
const TestCbvOobCsDxbcZ: str = "vZK5SwNBFIe/l0OCBLwaSeMiKWIjBiwsVNRNPCAeuIKxVHdFJbqyBm0sUgh2amEnWAuLnX+GpYVFkCgWFta2kXEnGsLa+mD2x5v59s07JlecMlcvPlOpl+JT/13u7LVv902AgyjEgWHgQaAq8CHwGIHlXH76UiACnBLoKEDcMhHYFsVks2qvFzCADJAGkooDToA2vdR9Ma2NVSHcGuf2YRlzA7+i/Xt0Ik3x1X5SgtjKokC1AUQC/F279Xq93qxHbJXc9fIwvu8LUYTYdx1h9qz6pC7Tdai4OQmPm86XnD1nv4x97Ho2PvQg35mG25jA/M6m5x66W2UjszxgzBasgmFtr9uOZ5ju3sFOyfGM7NBgljlrZiGh/1O62OJbs/liDVgizgiwm0DWIDZ9HlSmvjeqhFrXz0yuIdY8l1to79bnMqFnorW98tuhVk5CuI4QTvWulesM4dQsW7k+YBzEWplcuVLP8c+uBvn8t6l3+AU="

const TestTriVsDxilZ: str = "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"
const TestTriPsDxilZ: str = "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"

DecodeTestBlob(a: &Arena, z_b64: str): []u8 from a {
    raw_b := encoding.Base64Decode(a, z_b64, encoding.Base64).Must()
    return compress.Inflate(a, raw_b, 4 * 1024 * 1024).Must()
}

TestPackHdr(op: u8, words: u8, flags: u16, arg: u32): u64 {
    lo := u32(op) | (u32(words) << 8) | (u32(flags) << 16)
    return u64(lo) | (u64(arg) << 32)
}

TestUseSoftware(): bool {
    match rt.Env("MK_GPU_ADAPTER") {
        some(v) {
            if v.Eq(&"warp") { return true }
            if v.Eq(&"d3d12") { return false }
        }
        none {}
    }
    return false
}

MakeRgba8GuarTable(triples: mut [132]u32, guar: mut [44]FormatRowCaps) {
    for i in 0..44 {
        triples[i * 3] = 28 // DXGI_FORMAT_R8G8B8A8_UNORM
        triples[i * 3 + 1] = 28
        triples[i * 3 + 2] = 28
        guar[i] = mem.Zeroed[FormatRowCaps]()
    }
    // Index 4 is Bgra8UnormSrgb (guaranteed sampled/rt/blendable in portable table).
    triples[4 * 3] = 87
    triples[4 * 3 + 1] = 91
    triples[4 * 3 + 2] = 91
    guar[4] = FormatRowCaps(
        sampled: true,
        filterable: true,
        render_target: true,
        blendable: true,
        multisample: false,
        resolve: false,
        depth_stencil: false,
    )
}

OpenTestDxDevice(heap: &Heap, io: Io): (dev: Device, is_sw: bool) {
    var triples: [132]u32 = [0; 132]
    var guar: [44]FormatRowCaps = [mem.Zeroed[FormatRowCaps](); 44]
    MakeRgba8GuarTable(mut triples, mut guar)
    sw := TestUseSoftware()
    opts := OpenOptions(
        power: 1,
        software: sw,
        has_luid: false,
        target_luid: 0,
        debug: 2, // DebugModeStrict
        frames_in_flight: 2,
        descriptors: 256,
        req_bc: true,
        req_depth_clip: true,
        req_f32_filter: true,
        req_aniso: true,
    )
    (dev, info, caps: _) := Open(heap, io, opts, triples[..], guar[..]).Must()
    if sw {
        Assert(info.is_software)
    } else {
        Assert(!info.is_software)
    }
    return (dev: dev, is_sw: sw)
}

CreateHiddenHwndSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to live *uint out_hwnd" {
        out_p := raw.PtrCast[u8, uint](arg)
        cls := "STATIC\0"
        var wc16: [16]windows.Wchar = [0; 16]
        for i in 0..cls.AsBytes().Len() {
            wc16[i] = u16(cls.AsBytes()[i])
        }
        hinst := windows.GetModuleHandleW(raw.Null[windows.Wchar]())
        // WS_OVERLAPPEDWINDOW without WS_VISIBLE: 0x00CF0000
        hwnd := windows.CreateWindowExW(
            0,
            raw.Of(&wc16[0]),
            raw.Of(&wc16[0]),
            0x00CF0000,
            0,
            0,
            64,
            64,
            raw.Null[windows.HwndStruct](),
            raw.Null[windows.HmenuStruct](),
            hinst,
            raw.Null[void](),
        )
        raw.Write(out_p, raw.Addr(hwnd))
    }
}

/// Creates a hidden Win32 window for native swapchain tests. Returns zero on failure.
export CreateHiddenTestHwnd(): uint {
    if Target.os != Os.Windows {
        return 0
    }
    var out_hwnd: uint = 0
    unsafe "out_hwnd stays live across MkSystemRun" {
        task.MkSystemRun(CreateHiddenHwndSys, raw.PtrCast[uint, u8](raw.OfMut(mut out_hwnd)))
    }
    return out_hwnd
}

DestroyHiddenHwndSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to live *uint hwnd" {
        h := raw.Read(raw.PtrCast[u8, uint](arg))
        if h != 0 {
            _ = windows.DestroyWindow(raw.FromAddr[windows.HwndStruct](h))
        }
    }
}

/// Releases a window created by CreateHiddenTestHwnd. Does nothing for zero.
export DestroyHiddenTestHwnd(h: uint) {
    if Target.os != Os.Windows || h == 0 {
        return
    }
    var hh := h
    unsafe "hh stays live across MkSystemRun" {
        task.MkSystemRun(DestroyHiddenHwndSys, raw.PtrCast[uint, u8](raw.OfMut(mut hh)))
    }
}

ReadU32Le(bs: []u8, idx: int): u32 {
    o := idx * 4
    return u32(bs[o]) | (u32(bs[o + 1]) << 8) | (u32(bs[o + 2]) << 16) | (u32(bs[o + 3]) << 24)
}

WriteU32Le(dst: mut []u8, idx: int, v: u32) {
    o := idx * 4
    dst[o] = Trunc[u8](v & 0xFF)
    dst[o + 1] = Trunc[u8]((v >> 8) & 0xFF)
    dst[o + 2] = Trunc[u8]((v >> 16) & 0xFF)
    dst[o + 3] = Trunc[u8]((v >> 24) & 0xFF)
}

DxSkipReason(a: &Arena, io: Io): ?str {
    if Target.os != Os.Windows {
        return some("requires Windows")
    }
    match rt.Env("MK_GPU_ADAPTER") {
        some(v) {
            if v.Eq(&"vulkan") {
                return some("MK_GPU_ADAPTER=vulkan")
            }
            if v.Eq(&"warp") {
                return none
            }
        }
        none {}
    }
    var triples: [132]u32 = [0; 132]
    var guar: [44]FormatRowCaps = [mem.Zeroed[FormatRowCaps](); 44]
    MakeRgba8GuarTable(mut triples, mut guar)
    ads := Adapters(a, io, triples[..], guar[..]) else { return some("no d3d12 adapter") }
    for i in 0..ads.Len() {
        if !ads[i].is_software {
            return none
        }
    }
    return some("no hardware d3d12 adapter")
}

test "dx bounds check: DESCRIPTORS_STATIC_KEEPING_BUFFER_BOUNDS_CHECKS on StructuredBuffer and CBV" (sys: &System) {
    if r := DxSkipReason(sys.arena, sys.io) {
        _ = sys.Print("skip: {s}\n", (s: r)) else {}
        return
    }
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    (dev: dev0, is_sw: _) := OpenTestDxDevice(sys.heap, sys.io)
    var dev := dev0
    onerr dev.Close()

    // 1. StructuredBuffer OOB read proof via post_invert (64 threads, src size=16 -> 4 uints).
    sb_dxil := DecodeTestBlob(&ar, TestPostInvertCsDxilZ)
    sb_cs := dev.CreateShader(sb_dxil).Must()
    sb_entries := [
        LayoutEntrySpec(binding: 0, vis_vertex: false, vis_fragment: false, vis_compute: true, kind: 1, dynamic: false, read_only: true),
        LayoutEntrySpec(binding: 1, vis_vertex: false, vis_fragment: false, vis_compute: true, kind: 1, dynamic: false, read_only: false),
    ]
    sb_grps := [sb_entries[..]]
    sb_rs := dev.CreateRootSignature(&PipeLayoutSpec(groups: sb_grps[..], immediates: 0, label: "sb_oob_rs")).Must()
    sb_pso := dev.CreateComputePipeline(sys.io, sb_rs, sb_cs, "sb_oob_pso").Must()

    buf_src := dev.CreateBuffer(&BufferSpec(size: 256, uniform: false, uniform_dynamic: false, storage: true, map_read: false, map_write: false, label: "src")).Must()
    buf_dst := dev.CreateBuffer(&BufferSpec(size: 256, uniform: false, uniform_dynamic: false, storage: true, map_read: false, map_write: false, label: "dst")).Must()
    buf_rb := dev.CreateBuffer(&BufferSpec(size: 256, uniform: false, uniform_dynamic: false, storage: false, map_read: true, map_write: false, label: "rb")).Must()

    var init_src := try ar.Slice[u8](256)
    var init_dst := try ar.Slice[u8](256)
    for k in 0..64 {
        WriteU32Le(mut init_src[..], k, 0x11223344)
        WriteU32Le(mut init_dst[..], k, 0x12345678)
    }
    dev.QueueWriteBuffer(buf_src, 0, init_src[..]).Must()
    dev.QueueWriteBuffer(buf_dst, 0, init_dst[..]).Must()

    // Bind ONLY first 16 bytes (4 uints) of the 256-byte src buffer!
    sb_bg := dev.CreateBindGroup([
        BindEntrySpec(binding: 0, kind: 1, res_nh: buf_src, offset: 0, size: 16, stride: 4),
        BindEntrySpec(binding: 1, kind: 2, res_nh: buf_dst, offset: 0, size: 256, stride: 4),
    ][..]).Must()

    dev.FlushPreSubmit(2).Must()
    sb_bars := [
        BarrierSpec(is_tex: false, nh: buf_src, all_sub: true, first_mip: 0, mip_count: 1, first_layer: 0, layer_count: 1, plane_mask: 1, old_state: 0, new_state: 8, is_uav: false),
        BarrierSpec(is_tex: false, nh: buf_dst, all_sub: true, first_mip: 0, mip_count: 1, first_layer: 0, layer_count: 1, plane_mask: 1, old_state: 0, new_state: 16, is_uav: false),
        BarrierSpec(is_tex: false, nh: buf_dst, all_sub: true, first_mip: 0, mip_count: 1, first_layer: 0, layer_count: 1, plane_mask: 1, old_state: 16, new_state: 256, is_uav: false),
    ]
    sb_words := [
        TestPackHdr(1, 2, 0, 0), u64(2),
        TestPackHdr(4, 2, 1, 0), sb_pso,
        TestPackHdr(5, 2, 0, 0), sb_bg,
        TestPackHdr(15, 2, 0, 1), u64(1) | (u64(1) << 32),
        TestPackHdr(1, 2, 0, 2), u64(1),
        TestPackHdr(16, 5, 0, 256), buf_dst, buf_rb, u64(0), u64(0),
    ]
    sb_spans := [MakeChunkSpan(sb_words[..])]
    empty_passes := try ar.Slice[PassReplaySpec](0)
    dev.ReplayAndSubmit(sb_spans[..], sb_bars[..], "".AsBytes(), empty_passes[..], false, 3).Must()
    dev.WaitSerial(sys.io, 3).Must()
    sb_out := dev.BufferMappedSlice(buf_rb, 0, 256)
    for k in 0..4 {
        Assert(ReadU32Le(sb_out, k) == 0x11DDCCBB)
    }
    for k in 4..64 {
        Assert(ReadU32Le(sb_out, k) == 0x00FFFFFF)
    }

    // 2. CBV OOB read proof via cs_5_0 DXBC shader (512B backing, CBV SizeInBytes=256).
    cb_dxbc := DecodeTestBlob(&ar, TestCbvOobCsDxbcZ)
    cb_cs := dev.CreateShader(cb_dxbc).Must()
    cb_entries := [
        LayoutEntrySpec(binding: 0, vis_vertex: false, vis_fragment: false, vis_compute: true, kind: 0, dynamic: false, read_only: true),
        LayoutEntrySpec(binding: 1, vis_vertex: false, vis_fragment: false, vis_compute: true, kind: 1, dynamic: false, read_only: false),
    ]
    cb_grps := [cb_entries[..]]
    cb_rs := dev.CreateRootSignature(&PipeLayoutSpec(groups: cb_grps[..], immediates: 0, label: "cb_oob_rs")).Must()
    cb_pso := dev.CreateComputePipeline(sys.io, cb_rs, cb_cs, "cb_oob_pso").Must()

    cb_buf := dev.CreateBuffer(&BufferSpec(size: 512, uniform: true, uniform_dynamic: false, storage: false, map_read: false, map_write: false, label: "cb")).Must()
    cb_dst := dev.CreateBuffer(&BufferSpec(size: 256, uniform: false, uniform_dynamic: false, storage: true, map_read: false, map_write: false, label: "cb_dst")).Must()
    cb_rb := dev.CreateBuffer(&BufferSpec(size: 256, uniform: false, uniform_dynamic: false, storage: false, map_read: true, map_write: false, label: "cb_rb")).Must()

    var cb_init := try ar.Slice[u8](512)
    for k in 0..64 {
        WriteU32Le(mut cb_init[..], k, 0x11223344)
    }
    for k in 64..128 {
        WriteU32Le(mut cb_init[..], k, 0xAABBCCDD)
    }
    dev.QueueWriteBuffer(cb_buf, 0, cb_init[..]).Must()
    dev.QueueWriteBuffer(cb_dst, 0, init_dst[..]).Must()

    // Bind ONLY first 256 bytes of 512-byte constant buffer.
    cb_bg := dev.CreateBindGroup([
        BindEntrySpec(binding: 0, kind: 0, res_nh: cb_buf, offset: 0, size: 256, stride: 0),
        BindEntrySpec(binding: 1, kind: 2, res_nh: cb_dst, offset: 0, size: 256, stride: 4),
    ][..]).Must()

    dev.FlushPreSubmit(4).Must()
    cb_bars := [
        BarrierSpec(is_tex: false, nh: cb_dst, all_sub: true, first_mip: 0, mip_count: 1, first_layer: 0, layer_count: 1, plane_mask: 1, old_state: 0, new_state: 16, is_uav: false),
        BarrierSpec(is_tex: false, nh: cb_dst, all_sub: true, first_mip: 0, mip_count: 1, first_layer: 0, layer_count: 1, plane_mask: 1, old_state: 16, new_state: 256, is_uav: false),
    ]
    cb_words := [
        TestPackHdr(1, 2, 0, 0), u64(1),
        TestPackHdr(4, 2, 1, 0), cb_pso,
        TestPackHdr(5, 2, 0, 0), cb_bg,
        TestPackHdr(15, 2, 0, 1), u64(1) | (u64(1) << 32),
        TestPackHdr(1, 2, 0, 1), u64(1),
        TestPackHdr(16, 5, 0, 256), cb_dst, cb_rb, u64(0), u64(0),
    ]
    cb_spans := [MakeChunkSpan(cb_words[..])]
    dev.ReplayAndSubmit(cb_spans[..], cb_bars[..], "".AsBytes(), empty_passes[..], false, 5).Must()
    dev.WaitSerial(sys.io, 5).Must()
    cb_out := dev.BufferMappedSlice(cb_rb, 0, 16)
    Assert(ReadU32Le(cb_out, 0) == 0x11223344)
    Assert(ReadU32Le(cb_out, 1) == 0x11223344)
    Assert(ReadU32Le(cb_out, 2) == 0)
    Assert(ReadU32Le(cb_out, 3) == 0)

    dev.DestroyBindGroup(cb_bg)
    dev.DestroyBuffer(cb_rb)
    dev.DestroyBuffer(cb_dst)
    dev.DestroyBuffer(cb_buf)
    dev.DestroyPipeline(cb_pso)
    dev.DestroyRootSignature(cb_rs)
    dev.DestroyShader(cb_cs)

    dev.DestroyBindGroup(sb_bg)
    dev.DestroyBuffer(buf_rb)
    dev.DestroyBuffer(buf_dst)
    dev.DestroyBuffer(buf_src)
    dev.DestroyPipeline(sb_pso)
    dev.DestroyRootSignature(sb_rs)
    dev.DestroyShader(sb_cs)

    (errors: errs, warnings: warns) := dev.MessageCounts()
    Assert(errs == 0 && warns == 0)
    dev.Close()
}

test "dx swapchain: acquire, render, present, resize, hidden, abandon, waitable credit, and device loss" (sys: &System) {
    if r := DxSkipReason(sys.arena, sys.io) {
        _ = sys.Print("skip: {s}\n", (s: r)) else {}
        return
    }
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    hwnd := CreateHiddenTestHwnd()
    Assert(hwnd != 0)
    defer DestroyHiddenTestHwnd(hwnd)

    (dev: dev0, is_sw: _) := OpenTestDxDevice(sys.heap, sys.io)
    var dev := dev0
    onerr dev.Close()

    vs := dev.CreateShader(DecodeTestBlob(&ar, TestTriVsDxilZ)).Must()
    ps := dev.CreateShader(DecodeTestBlob(&ar, TestTriPsDxilZ)).Must()
    var empty_grps: [0][]LayoutEntrySpec = []
    rs := dev.CreateRootSignature(&PipeLayoutSpec(groups: empty_grps[..], immediates: 0, label: "sc_rs")).Must()
    targets := [ColorTargetSpec(
        dxgi_format: 91,
        blend_enable: false,
        color: BlendCompSpec(src: 1, dst: 0, op: 0),
        alpha: BlendCompSpec(src: 1, dst: 0, op: 0),
        write_mask: 15,
    )]
    var empty_attrs: [0]VertexAttrSpec = []
    pso := dev.CreateRenderPipeline(sys.io, &RenderPipeSpec(
        root_sig_nh: rs,
        vs_nh: vs,
        has_fs: true,
        fs_nh: ps,
        attrs: empty_attrs[..],
        topology: 3,
        has_strip_cut: false,
        strip_cut_u32: false,
        cull: 0,
        front_ccw: false,
        depth_clip_disable: false,
        targets: targets[..],
        has_depth: false,
        depth_dxgi: 0,
        depth_write: false,
        depth_compare: 7,
        stencil_enable: false,
        stencil_read_mask: 0,
        stencil_write_mask: 0,
        stencil_front: StencilFaceSpec(compare: 7, on_fail: 0, on_depth_fail: 0, on_pass: 0),
        stencil_back: StencilFaceSpec(compare: 7, on_fail: 0, on_depth_fail: 0, on_pass: 0),
        bias_constant: 0,
        bias_slope: 0.0,
        bias_clamp: 0.0,
        samples: 1,
        alpha_to_coverage: false,
        label: "sc_pso",
    )).Must()

    sc_spec := SwapchainSpec(
        hwnd: hwnd,
        width: 64,
        height: 64,
        dxgi_res: 87,
        dxgi_rtv: 91,
        mode: 0, // Fifo
        buffers: 3,
        copy_src: false,
    )
    sc_r := dev.CreateSwapchain(&sc_spec).Must()
    sc := sc_r.sc_nh
    sc_view := sc_r.view_nh
    Assert(sc_r.images == 3)
    wstate0 := SwapchainWaitableState(sc)
    Assert(wstate0.waitable != 0 && !wstate0.credit_held)

    var empty_bars: [0]BarrierSpec = []
    var empty_passes: [0]PassReplaySpec = []
    var ser: u64 = 2
    // Render + Present 4 frames across 3 back buffers and frames_in_flight=2 pacing.
    for _ in 0..4 {
        aq := dev.AcquireSwapchain(sys.io, sc, sc_view, 64, 64).Must()
        Assert(aq.status == 0)
        var cols: [8]PassColorSpec = [mem.Zeroed[PassColorSpec](); 8]
        cols[0] = PassColorSpec(
            is_swapchain: true,
            res_ptr: aq.res_ptr,
            rtv_ptr: aq.rtv_ptr,
            has_resolve: false,
            resolve_res_ptr: 0,
            resolve_sub: 0,
            src_sub: 0,
            dxgi_format: 91,
            load_op: 0,
            clear_r: 0.1,
            clear_g: 0.2,
            clear_b: 0.3,
            clear_a: 1.0,
        )
        passes := [PassReplaySpec(
            width: 64,
            height: 64,
            color_count: 1,
            colors: cols,
            depth: mem.Zeroed[PassDepthSpec](),
        )]
        words := [
            TestPackHdr(2, 1, 0, 0),
            TestPackHdr(4, 2, 0, 0), pso,
            TestPackHdr(13, 3, 0, 3), u64(1), u64(0),
            TestPackHdr(3, 1, 0, 0),
        ]
        spans := [MakeChunkSpan(words[..])]
        dev.ReplayAndSubmit(spans[..], empty_bars[..], "".AsBytes(), passes[..], false, ser).Must()
        _ = dev.PresentSwapchain(sc, ser).Must()
        ser += 1
    }

    // Waitable pre-canceled scope test: returns Canceled, does not wedge next Acquire.
    scope cscope := sys.io.Scope() {
        cscope.Cancel()
        _ = dev.AcquireSwapchain(sys.io, sc, sc_view, 64, 64) else ce {
            match ce {
                Canceled {}
                _ { Assert(false) }
            }
        }
    }

    // Resize to 128x96 and present one frame.
    dev.ResizeSwapchain(sys.io, sc, sc_view, 128, 96, mut ser).Must()
    aq_r := dev.AcquireSwapchain(sys.io, sc, sc_view, 128, 96).Must()
    Assert(aq_r.status == 0)
    var rcols: [8]PassColorSpec = [mem.Zeroed[PassColorSpec](); 8]
    rcols[0] = PassColorSpec(
        is_swapchain: true,
        res_ptr: aq_r.res_ptr,
        rtv_ptr: aq_r.rtv_ptr,
        has_resolve: false,
        resolve_res_ptr: 0,
        resolve_sub: 0,
        src_sub: 0,
        dxgi_format: 91,
        load_op: 0,
        clear_r: 0.5,
        clear_g: 0.0,
        clear_b: 0.0,
        clear_a: 1.0,
    )
    rpasses := [PassReplaySpec(width: 128, height: 96, color_count: 1, colors: rcols, depth: mem.Zeroed[PassDepthSpec]())]
    rwords := [TestPackHdr(2, 1, 0, 0), TestPackHdr(3, 1, 0, 0)]
    rspans := [MakeChunkSpan(rwords[..])]
    dev.ReplayAndSubmit(rspans[..], empty_bars[..], "".AsBytes(), rpasses[..], false, ser).Must()
    _ = dev.PresentSwapchain(sc, ser).Must()
    ser += 1

    // Minimized 0x0 -> Hidden (status == 2), then restore 64x64 -> Ready.
    dev.ResizeSwapchain(sys.io, sc, sc_view, 0, 0, mut ser).Must()
    aq_hid := dev.AcquireSwapchain(sys.io, sc, sc_view, 0, 0).Must()
    Assert(aq_hid.status == 2)
    dev.ResizeSwapchain(sys.io, sc, sc_view, 64, 64, mut ser).Must()

    // Abandon -> Acquire returns Outdated (status == 1) -> Resize recreates -> Ready + Present.
    AbandonSwapchain(sc)
    aq_out := dev.AcquireSwapchain(sys.io, sc, sc_view, 64, 64).Must()
    Assert(aq_out.status == 1)
    dev.ResizeSwapchain(sys.io, sc, sc_view, 64, 64, mut ser).Must()
    aq_ok := dev.AcquireSwapchain(sys.io, sc, sc_view, 64, 64).Must()
    Assert(aq_ok.status == 0)
    var ok_cols: [8]PassColorSpec = [mem.Zeroed[PassColorSpec](); 8]
    ok_cols[0] = PassColorSpec(
        is_swapchain: true,
        res_ptr: aq_ok.res_ptr,
        rtv_ptr: aq_ok.rtv_ptr,
        has_resolve: false,
        resolve_res_ptr: 0,
        resolve_sub: 0,
        src_sub: 0,
        dxgi_format: 91,
        load_op: 0,
        clear_r: 0.0,
        clear_g: 0.5,
        clear_b: 0.0,
        clear_a: 1.0,
    )
    ok_passes := [PassReplaySpec(width: 64, height: 64, color_count: 1, colors: ok_cols, depth: mem.Zeroed[PassDepthSpec]())]
    dev.ReplayAndSubmit(rspans[..], empty_bars[..], "".AsBytes(), ok_passes[..], false, ser).Must()
    _ = dev.PresentSwapchain(sc, ser).Must()
    ser += 1
    dev.WaitSerial(sys.io, ser - 1).Must()

    (errors: errs, warnings: warns) := dev.MessageCounts()
    Assert(errs == 0 && warns == 0)

    // RemoveDevice loss test: SimulateDeviceLoss triggers DeviceLostRemoved cleanly.
    dev.SimulateDeviceLoss()
    _ = dev.ReplayAndSubmit(rspans[..], empty_bars[..], "".AsBytes(), empty_passes[..], false, ser) else {}
    var saw_loss := false
    _ = dev.WaitSerial(sys.io, ser) else e {
        match e {
            DxFail(kind, code: _, call: _, at: _) {
                if kind == ErrKind.DeviceLostRemoved || kind == ErrKind.DeviceLostHung || kind == ErrKind.DeviceLostUnknown {
                    saw_loss = true
                }
            }
            _ {}
        }
    }
    Assert(saw_loss)

    dev.DestroyView(sc_view)
    dev.DestroySwapchain(sc)
    dev.DestroyPipeline(pso)
    dev.DestroyRootSignature(rs)
    dev.DestroyShader(ps)
    dev.DestroyShader(vs)
    dev.Close()
}

test "dx planted bug: SkipPassBarrier fails Strict debug layer on swapchain Present" (sys: &System) {
    if r := DxSkipReason(sys.arena, sys.io) {
        _ = sys.Print("skip: {s}\n", (s: r)) else {}
        return
    }
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    hwnd := CreateHiddenTestHwnd()
    Assert(hwnd != 0)
    defer DestroyHiddenTestHwnd(hwnd)

    (dev: dev0, is_sw: _) := OpenTestDxDevice(sys.heap, sys.io)
    var dev := dev0
    onerr dev.Close()

    sc_spec := SwapchainSpec(
        hwnd: hwnd,
        width: 64,
        height: 64,
        dxgi_res: 87,
        dxgi_rtv: 91,
        mode: 0,
        buffers: 2,
        copy_src: false,
    )
    sc_r := dev.CreateSwapchain(&sc_spec).Must()
    sc := sc_r.sc_nh
    sc_view := sc_r.view_nh
    aq := dev.AcquireSwapchain(sys.io, sc, sc_view, 64, 64).Must()
    Assert(aq.status == 0)
    var cols: [8]PassColorSpec = [mem.Zeroed[PassColorSpec](); 8]
    cols[0] = PassColorSpec(
        is_swapchain: true,
        res_ptr: aq.res_ptr,
        rtv_ptr: aq.rtv_ptr,
        has_resolve: false,
        resolve_res_ptr: 0,
        resolve_sub: 0,
        src_sub: 0,
        dxgi_format: 91,
        load_op: 0,
        clear_r: 0.0,
        clear_g: 0.0,
        clear_b: 0.0,
        clear_a: 1.0,
    )
    passes := [PassReplaySpec(width: 64, height: 64, color_count: 1, colors: cols, depth: mem.Zeroed[PassDepthSpec]())]
    words := [TestPackHdr(2, 1, 0, 0), TestPackHdr(3, 1, 0, 0)]
    spans := [MakeChunkSpan(words[..])]
    var empty_bars: [0]BarrierSpec = []
    dev.ReplayAndSubmit(spans[..], empty_bars[..], "".AsBytes(), passes[..], true, 2).Must()
    // Present expects PRESENT/COMMON state; skipping EndPass barrier leaves RENDER_TARGET -> Strict DebugLayer!
    _ = dev.PresentSwapchain(sc, 2) else {}
    (errors: errs, warnings: warns) := dev.MessageCounts()
    Assert(errs > 0 || warns > 0)
    var next_serial: u64 = 3
    dev.WaitQueueIdle(sys.io, mut next_serial).Must()
    dev.DestroyView(sc_view)
    dev.DestroySwapchain(sc)
    dev.Close()
}
