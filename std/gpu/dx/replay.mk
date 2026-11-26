// Why: D3D12 command submission must reset an allocator and command list,
// set the shader-visible descriptor heaps, translate every portable stream
// opcode into ID3D12GraphicsCommandList calls, and execute + signal in one
// MkSystemRun call.
//
// What: PassReplaySpec, ViewRtvInfo, ViewDsvInfo, TexResPtr, and
// Device.ReplayAndSubmit.
//
// How: decodes packed 8-byte stream records across chunk slices, tracks the
// active DxRootSig and compute/graphics mode across OpSetPipeline, and emits
// all barriers, passes, draws, dispatches, copies, and debug markers on the
// system stack.

import c.windows
import c.dxgi
import c.d3d12
import std.raw
import std.mem

const StreamOpNop: u8 = 0
const StreamOpBarrierRef: u8 = 1
const StreamOpBeginRendering: u8 = 2
const StreamOpEndRendering: u8 = 3
const StreamOpSetPipeline: u8 = 4
const StreamOpSetGroup: u8 = 5
const StreamOpSetVertexBuffers: u8 = 6
const StreamOpSetIndexBuffer: u8 = 7
const StreamOpSetViewport: u8 = 8
const StreamOpSetScissor: u8 = 9
const StreamOpSetBlendConst: u8 = 10
const StreamOpSetStencilRef: u8 = 11
const StreamOpSetImmediates: u8 = 12
const StreamOpDraw: u8 = 13
const StreamOpDrawIndexed: u8 = 14
const StreamOpDispatch: u8 = 15
const StreamOpCopyBuffer: u8 = 16
const StreamOpCopyBufferToTexture: u8 = 17
const StreamOpCopyTextureToBuffer: u8 = 18
const StreamOpCopyTexture: u8 = 19
const StreamOpClearBuffer: u8 = 20
const StreamOpPushDebugGroup: u8 = 21
const StreamOpPopDebugGroup: u8 = 22

/// Render pass attachment metadata passed from portable CommandStream to replay.
export struct PassReplaySpec {
    export width: int
    export height: int
    export color_count: int
    export colors: [8]PassColorSpec
    export depth: PassDepthSpec
}

/// Resolves RTV resource pointer, CPU descriptor pointer, and swapchain flag from a view handle.
export ViewRtvInfo(nh: u64): (res_ptr: u64, rtv_ptr: u64, is_swapchain: bool) {
    if Target.os != Os.Windows || nh == 0 {
        return (res_ptr: 0, rtv_ptr: 0, is_swapchain: false)
    }
    unsafe "nh is a live DxView handle" {
        vp := raw.FromAddr[DxView](Trunc[uint](nh))
        res := if (*vp).is_swapchain { (*vp).swap_res } else { (*vp).tex_res }
        return (
            res_ptr: Trunc[u64](raw.Addr(res)),
            rtv_ptr: (*vp).rtv_ptr,
            is_swapchain: (*vp).is_swapchain,
        )
    }
}

/// Resolves DSV CPU descriptor pointer (normal or read-only) from a view handle.
export ViewDsvInfo(nh: u64, read_only: bool): u64 {
    if Target.os != Os.Windows || nh == 0 {
        return 0
    }
    unsafe "nh is a live DxView handle" {
        vp := raw.FromAddr[DxView](Trunc[uint](nh))
        return if read_only { (*vp).dsv_ro_ptr } else { (*vp).dsv_rw_ptr }
    }
}

/// Resolves the underlying ID3D12Resource pointer from a DxTexture handle.
export TexResPtr(nh: u64): u64 {
    if Target.os != Os.Windows || nh == 0 {
        return 0
    }
    unsafe "nh is a live DxTexture handle" {
        tp := raw.FromAddr[DxTexture](Trunc[uint](nh))
        return Trunc[u64](raw.Addr((*tp).res))
    }
}

HdrOp(w0: u64): u8 { return Trunc[u8](w0 & 0xFF) }
HdrWords(w0: u64): int { return Cast[int]((w0 >> 8) & 0xFF) }
HdrFlags(w0: u64): u16 { return Trunc[u16]((w0 >> 16) & 0xFFFF) }
HdrArg(w0: u64): u32 { return Trunc[u32](w0 >> 32) }
Lo32(w: u64): u32 { return Trunc[u32](w & 0xFFFFFFFF) }
Hi32(w: u64): u32 { return Trunc[u32](w >> 32) }

struct ReplayArgs {
    dev: *Device
    alloc: *d3d12.ID3D12CommandAllocator
    chunks_ptr: *ChunkSpan
    chunk_count: int
    barriers_ptr: *BarrierSpec
    barrier_count: int
    labels_ptr: *u8
    label_count: int
    passes_ptr: *PassReplaySpec
    pass_count: int
    skip_pass_barrier: bool
    serial: u64
    hr: windows.Hresult
    step: u8 // 0=reset, 1=close, 2=signal
}

ReplayBeginPass(
    cmd: *d3d12.ID3D12GraphicsCommandList,
    ps: &PassReplaySpec,
) {
    if Target.os != Os.Windows {
        return
    }
    var rtvs: [8]d3d12.D3d12CpuDescriptorHandle = [mem.Zeroed[d3d12.D3d12CpuDescriptorHandle](); 8]
    for ci in 0..ps.color_count {
        c := &ps.colors[ci]
        if c.is_swapchain && c.res_ptr != 0 {
            unsafe "c.res_ptr is a live swapchain back-buffer ID3D12Resource" {
                res := raw.FromAddr[d3d12.ID3D12Resource](Trunc[uint](c.res_ptr))
                EmitTransition(cmd, res, 0, d3d12.D3d12ResourceStatePresent, d3d12.D3d12ResourceStateRenderTarget)
            }
        }
        rtvs[ci] = d3d12.D3d12CpuDescriptorHandle(ptr: c.rtv_ptr)
    }
    var dsv := d3d12.D3d12CpuDescriptorHandle(ptr: ps.depth.dsv_ptr)
    unsafe "cmd is a live recording ID3D12GraphicsCommandList" {
        rtv_p := if ps.color_count > 0 { raw.Of(&rtvs[0]) } else { raw.Null[d3d12.D3d12CpuDescriptorHandle]() }
        dsv_p := if ps.depth.has_depth { raw.Of(&dsv) } else { raw.Null[d3d12.D3d12CpuDescriptorHandle]() }
        (*cmd).OmSetRenderTargets(Cast[windows.Uint](ps.color_count), rtv_p, 0, dsv_p)
        for ci in 0..ps.color_count {
            c := &ps.colors[ci]
            if c.load_op == 0 {
                var rgba: [4]windows.Float = [c.clear_r, c.clear_g, c.clear_b, c.clear_a]
                (*cmd).ClearRenderTargetView(rtvs[ci], raw.Of(&rgba[0]), 0, raw.Null[d3d12.D3d12Rect]())
            }
        }
        if ps.depth.has_depth {
            var cflags: d3d12.D3d12ClearFlags = 0
            if ps.depth.depth_load == 0 {
                cflags |= d3d12.D3d12ClearFlagDepth
            }
            if ps.depth.has_stencil && ps.depth.stencil_load == 0 {
                cflags |= d3d12.D3d12ClearFlagStencil
            }
            if cflags != 0 {
                (*cmd).ClearDepthStencilView(dsv, cflags, ps.depth.depth_clear, ps.depth.stencil_clear, 0, raw.Null[d3d12.D3d12Rect]())
            }
        }
        var vp := d3d12.D3d12Viewport(
            top_left_x: 0.0,
            top_left_y: 0.0,
            width: Cast[f32](ps.width),
            height: Cast[f32](ps.height),
            min_depth: 0.0,
            max_depth: 1.0,
        )
        (*cmd).RsSetViewports(1, raw.Of(&vp))
        var sc := d3d12.D3d12Rect(
            left: 0,
            top: 0,
            right: Cast[windows.Long](ps.width),
            bottom: Cast[windows.Long](ps.height),
        )
        (*cmd).RsSetScissorRects(1, raw.Of(&sc))
        var bf: [4]windows.Float = [0.0, 0.0, 0.0, 0.0]
        (*cmd).OmSetBlendFactor(raw.Of(&bf[0]))
        (*cmd).OmSetStencilRef(0)
    }
}

ReplayEndPass(
    cmd: *d3d12.ID3D12GraphicsCommandList,
    ps: &PassReplaySpec,
    skip_pass_barrier: bool,
) {
    if Target.os != Os.Windows {
        return
    }
    for ci in 0..ps.color_count {
        c := &ps.colors[ci]
        if c.has_resolve && c.res_ptr != 0 && c.resolve_res_ptr != 0 {
            unsafe "src and resolve resources are live ID3D12Resources" {
                src_res := raw.FromAddr[d3d12.ID3D12Resource](Trunc[uint](c.res_ptr))
                dst_res := raw.FromAddr[d3d12.ID3D12Resource](Trunc[uint](c.resolve_res_ptr))
                EmitTransition(cmd, src_res, c.src_sub, d3d12.D3d12ResourceStateRenderTarget, d3d12.D3d12ResourceStateResolveSource)
                EmitTransition(cmd, dst_res, c.resolve_sub, d3d12.D3d12ResourceStateRenderTarget, d3d12.D3d12ResourceStateResolveDest)
                (*cmd).ResolveSubresource(dst_res, c.resolve_sub, src_res, c.src_sub, Cast[dxgi.DxgiFormat](c.dxgi_format))
                EmitTransition(cmd, src_res, c.src_sub, d3d12.D3d12ResourceStateResolveSource, d3d12.D3d12ResourceStateRenderTarget)
                EmitTransition(cmd, dst_res, c.resolve_sub, d3d12.D3d12ResourceStateResolveDest, d3d12.D3d12ResourceStateRenderTarget)
            }
        }
        if c.is_swapchain && c.res_ptr != 0 && !skip_pass_barrier {
            unsafe "c.res_ptr is a live swapchain back-buffer ID3D12Resource" {
                res := raw.FromAddr[d3d12.ID3D12Resource](Trunc[uint](c.res_ptr))
                EmitTransition(cmd, res, 0, d3d12.D3d12ResourceStateRenderTarget, d3d12.D3d12ResourceStatePresent)
            }
        }
    }
}

ReplayRecord(p: *ReplayArgs, cmd: *d3d12.ID3D12GraphicsCommandList) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "all slices inside ReplayArgs are live for the duration of MkSystemRun" {
        chunks := raw.Slice((*p).chunks_ptr, (*p).chunk_count)
        barriers := if (*p).barrier_count > 0 {
            raw.Slice((*p).barriers_ptr, (*p).barrier_count)
        } else {
            raw.Slice(raw.Null[BarrierSpec](), 0)
        }
        labels := if (*p).label_count > 0 {
            raw.Slice((*p).labels_ptr, (*p).label_count)
        } else {
            raw.Slice(raw.Null[u8](), 0)
        }
        passes := if (*p).pass_count > 0 {
            raw.Slice((*p).passes_ptr, (*p).pass_count)
        } else {
            raw.Slice(raw.Null[PassReplaySpec](), 0)
        }

        var cur_rs := raw.Null[DxRootSig]()
        var cur_is_comp := false

        for ci in 0..(*p).chunk_count {
            span := chunks[ci]
            words := raw.Slice(span.ptr, span.len)
            n := words.Len()
            var wi := 0
            for wi < n {
                w0 := words[wi]
                op := HdrOp(w0)
                wcnt := HdrWords(w0)
                if wcnt <= 0 {
                    break
                }
                if op == StreamOpBarrierRef {
                    bstart := Cast[int](HdrArg(w0))
                    bcnt := Cast[int](Lo32(words[wi + 1]))
                    if bcnt > 0 && bstart + bcnt <= barriers.Len() {
                        EmitBarrierBatch(cmd, barriers[bstart..(bstart + bcnt)])
                    }
                } else if op == StreamOpBeginRendering {
                    pidx := Cast[int](HdrArg(w0))
                    if pidx >= 0 && pidx < passes.Len() {
                        ReplayBeginPass(cmd, &passes[pidx])
                    }
                } else if op == StreamOpEndRendering {
                    pidx := Cast[int](HdrArg(w0))
                    if pidx >= 0 && pidx < passes.Len() {
                        ReplayEndPass(cmd, &passes[pidx], (*p).skip_pass_barrier)
                    }
                } else if op == StreamOpSetPipeline {
                    is_comp := HdrFlags(w0) != 0
                    pp := raw.FromAddr[DxPipeline](Trunc[uint](words[wi + 1]))
                    rs := (*pp).root_sig
                    if !raw.Same(rs, cur_rs) || is_comp != cur_is_comp {
                        if is_comp {
                            (*cmd).SetComputeRootSignature((*rs).sig)
                        } else {
                            (*cmd).SetGraphicsRootSignature((*rs).sig)
                        }
                        cur_rs = rs
                        cur_is_comp = is_comp
                    }
                    (*cmd).SetPipelineState((*pp).pso)
                    if !is_comp {
                        (*cmd).IaSetPrimitiveTopology((*pp).topology)
                    }
                } else if op == StreamOpSetGroup {
                    gi := int(HdrFlags(w0))
                    dc := int(HdrArg(w0))
                    bg := raw.FromAddr[DxBindGroup](Trunc[uint](words[wi + 1]))
                    if !raw.IsNull(cur_rs) {
                        gr := &(*cur_rs).groups[gi]
                        if (*bg).has_cbv_srv_uav && gr.has_table {
                            gh := d3d12.D3d12GpuDescriptorHandle(ptr: (*bg).cbv_srv_uav_gpu)
                            if cur_is_comp {
                                (*cmd).SetComputeRootDescriptorTable(gr.table_param, gh)
                            } else {
                                (*cmd).SetGraphicsRootDescriptorTable(gr.table_param, gh)
                            }
                        }
                        if (*bg).has_samplers && gr.has_samplers {
                            sh := d3d12.D3d12GpuDescriptorHandle(ptr: (*bg).sampler_gpu)
                            if cur_is_comp {
                                (*cmd).SetComputeRootDescriptorTable(gr.sampler_param, sh)
                            } else {
                                (*cmd).SetGraphicsRootDescriptorTable(gr.sampler_param, sh)
                            }
                        }
                        for di in 0..dc {
                            w_pair := words[wi + 2 + (di / 2)]
                            dyn_off := if (di & 1) == 0 { Lo32(w_pair) } else { Hi32(w_pair) }
                            va := (*bg).dyn_vas[di] + u64(dyn_off)
                            if cur_is_comp {
                                (*cmd).SetComputeRootConstantBufferView(gr.dyn_params[di], va)
                            } else {
                                (*cmd).SetGraphicsRootConstantBufferView(gr.dyn_params[di], va)
                            }
                        }
                    }
                } else if op == StreamOpSetVertexBuffers {
                    slot := Cast[windows.Uint](HdrFlags(w0))
                    stride := Cast[windows.Uint](HdrArg(w0))
                    bp := raw.FromAddr[DxBuffer](Trunc[uint](words[wi + 1]))
                    w2 := words[wi + 2]
                    vbv := d3d12.D3d12VertexBufferView(
                        buffer_location: (*bp).gpu_va + u64(Lo32(w2)),
                        size_in_bytes: Cast[windows.Uint](Hi32(w2)),
                        stride_in_bytes: stride,
                    )
                    (*cmd).IaSetVertexBuffers(slot, 1, raw.Of(&vbv))
                } else if op == StreamOpSetIndexBuffer {
                    fmt := if HdrFlags(w0) != 0 { DxgiFormatR32Uint } else { DxgiFormatR16Uint }
                    sz := Cast[windows.Uint](HdrArg(w0))
                    bp := raw.FromAddr[DxBuffer](Trunc[uint](words[wi + 1]))
                    off := words[wi + 2]
                    ibv := d3d12.D3d12IndexBufferView(
                        buffer_location: (*bp).gpu_va + off,
                        size_in_bytes: sz,
                        format: fmt,
                    )
                    (*cmd).IaSetIndexBuffer(raw.Of(&ibv))
                } else if op == StreamOpSetViewport {
                    w1 := words[wi + 1]
                    w2 := words[wi + 2]
                    w3 := words[wi + 3]
                    vp := d3d12.D3d12Viewport(
                        top_left_x: raw.F32From(Lo32(w1)),
                        top_left_y: raw.F32From(Hi32(w1)),
                        width: raw.F32From(Lo32(w2)),
                        height: raw.F32From(Hi32(w2)),
                        min_depth: raw.F32From(Lo32(w3)),
                        max_depth: raw.F32From(Hi32(w3)),
                    )
                    (*cmd).RsSetViewports(1, raw.Of(&vp))
                } else if op == StreamOpSetScissor {
                    w1 := words[wi + 1]
                    w2 := words[wi + 2]
                    sx := Cast[i32](Lo32(w1))
                    sy := Cast[i32](Hi32(w1))
                    sw := Cast[i32](Lo32(w2))
                    sh := Cast[i32](Hi32(w2))
                    sc := d3d12.D3d12Rect(left: sx, top: sy, right: sx + sw, bottom: sy + sh)
                    (*cmd).RsSetScissorRects(1, raw.Of(&sc))
                } else if op == StreamOpSetBlendConst {
                    w1 := words[wi + 1]
                    w2 := words[wi + 2]
                    var rgba: [4]windows.Float = [
                        raw.F32From(Lo32(w1)),
                        raw.F32From(Hi32(w1)),
                        raw.F32From(Lo32(w2)),
                        raw.F32From(Hi32(w2)),
                    ]
                    (*cmd).OmSetBlendFactor(raw.Of(&rgba[0]))
                } else if op == StreamOpSetStencilRef {
                    (*cmd).OmSetStencilRef(HdrArg(w0))
                } else if op == StreamOpSetImmediates {
                    is_comp := HdrFlags(w0) != 0
                    num_vals := Cast[windows.Uint](HdrArg(w0) / 4)
                    if !raw.IsNull(cur_rs) && (*cur_rs).has_imm && num_vals > 0 {
                        data_ptr := raw.PtrCast[u64, void](raw.Of(&words[wi + 1]))
                        if is_comp {
                            (*cmd).SetComputeRoot32BitConstants((*cur_rs).imm_param, num_vals, data_ptr, 0)
                        } else {
                            (*cmd).SetGraphicsRoot32BitConstants((*cur_rs).imm_param, num_vals, data_ptr, 0)
                        }
                    }
                } else if op == StreamOpDraw {
                    verts := HdrArg(w0)
                    w1 := words[wi + 1]
                    w2 := words[wi + 2]
                    (*cmd).DrawInstanced(verts, Lo32(w1), Hi32(w1), Lo32(w2))
                } else if op == StreamOpDrawIndexed {
                    idxs := HdrArg(w0)
                    w1 := words[wi + 1]
                    w2 := words[wi + 2]
                    bv := Cast[i32](Lo32(w2))
                    (*cmd).DrawIndexedInstanced(idxs, Lo32(w1), Hi32(w1), bv, Hi32(w2))
                } else if op == StreamOpDispatch {
                    gx := HdrArg(w0)
                    w1 := words[wi + 1]
                    (*cmd).Dispatch(gx, Lo32(w1), Hi32(w1))
                } else if op == StreamOpCopyBuffer {
                    nbytes := u64(HdrArg(w0))
                    is_staged := HdrFlags(w0) != 0
                    src_res := if is_staged {
                        raw.FromAddr[d3d12.ID3D12Resource](Trunc[uint](words[wi + 1]))
                    } else {
                        (*raw.FromAddr[DxBuffer](Trunc[uint](words[wi + 1]))).res
                    }
                    dst_bp := raw.FromAddr[DxBuffer](Trunc[uint](words[wi + 2]))
                    (*cmd).CopyBufferRegion((*dst_bp).res, words[wi + 4], src_res, words[wi + 3], nbytes)
                } else if op == StreamOpCopyBufferToTexture {
                    pmask := HdrFlags(w0)
                    mip := Cast[int](HdrArg(w0))
                    bp := raw.FromAddr[DxBuffer](Trunc[uint](words[wi + 1]))
                    tp := raw.FromAddr[DxTexture](Trunc[uint](words[wi + 2]))
                    off := words[wi + 3]
                    bpr := Lo32(words[wi + 4])
                    rpl := Hi32(words[wi + 4])
                    dx := Lo32(words[wi + 5])
                    dy := Hi32(words[wi + 5])
                    cw := Lo32(words[wi + 6])
                    ch := Hi32(words[wi + 6])
                    first_layer := Cast[int](Lo32(words[wi + 7]))
                    layer_count := Cast[int](Hi32(words[wi + 7]))
                    plane := if (pmask & 2) != 0 { 1 } else { 0 }
                    aw := ((Cast[int](cw) + (*tp).block_w - 1) / (*tp).block_w) * (*tp).block_w
                    ah := ((Cast[int](ch) + (*tp).block_h - 1) / (*tp).block_h) * (*tp).block_h
                    by := ah / (*tp).block_h
                    rows_p := if rpl == 0 { by } else { Cast[int](rpl) }
                    slice_bytes := u64(bpr) * Cast[u64](rows_p)
                    for li in 0..layer_count {
                        sub := Cast[windows.Uint](mip + (first_layer + li) * (*tp).mips + plane * (*tp).mips * (*tp).layers)
                        var dst_loc := mem.Zeroed[d3d12.D3d12TextureCopyLocation]()
                        dst_loc.p_resource = (*tp).res
                        dst_loc.type_ = d3d12.D3d12TextureCopyTypeSubresourceIndex
                        dst_loc.anon0.SetSubresourceIndex(sub)
                        var src_loc := mem.Zeroed[d3d12.D3d12TextureCopyLocation]()
                        src_loc.p_resource = (*bp).res
                        src_loc.type_ = d3d12.D3d12TextureCopyTypePlacedFootprint
                        src_loc.anon0.SetPlacedFootprint(d3d12.D3d12PlacedSubresourceFootprint(
                            offset: off + Cast[u64](li) * slice_bytes,
                            footprint: d3d12.D3d12SubresourceFootprint(
                                format: Cast[dxgi.DxgiFormat]((*tp).dxgi_res),
                                width: Cast[windows.Uint](aw),
                                height: Cast[windows.Uint](ah),
                                depth: 1,
                                row_pitch: bpr,
                            ),
                        ))
                        var bx_src := d3d12.D3d12Box(left: 0, top: 0, front: 0, right: Cast[windows.Uint](aw), bottom: Cast[windows.Uint](ah), back: 1)
                        (*cmd).CopyTextureRegion(raw.Of(&dst_loc), dx, dy, 0, raw.Of(&src_loc), raw.Of(&bx_src))
                    }
                } else if op == StreamOpCopyTextureToBuffer {
                    pmask := HdrFlags(w0)
                    mip := Cast[int](HdrArg(w0))
                    tp := raw.FromAddr[DxTexture](Trunc[uint](words[wi + 1]))
                    bp := raw.FromAddr[DxBuffer](Trunc[uint](words[wi + 2]))
                    off := words[wi + 3]
                    bpr := Lo32(words[wi + 4])
                    rpl := Hi32(words[wi + 4])
                    sx := Lo32(words[wi + 5])
                    sy := Hi32(words[wi + 5])
                    cw := Lo32(words[wi + 6])
                    ch := Hi32(words[wi + 6])
                    first_layer := Cast[int](Lo32(words[wi + 7]))
                    layer_count := Cast[int](Hi32(words[wi + 7]))
                    plane := if (pmask & 2) != 0 { 1 } else { 0 }
                    aw := ((Cast[int](cw) + (*tp).block_w - 1) / (*tp).block_w) * (*tp).block_w
                    ah := ((Cast[int](ch) + (*tp).block_h - 1) / (*tp).block_h) * (*tp).block_h
                    by := ah / (*tp).block_h
                    rows_p := if rpl == 0 { by } else { Cast[int](rpl) }
                    slice_bytes := u64(bpr) * Cast[u64](rows_p)
                    for li in 0..layer_count {
                        sub := Cast[windows.Uint](mip + (first_layer + li) * (*tp).mips + plane * (*tp).mips * (*tp).layers)
                        var dst_loc := mem.Zeroed[d3d12.D3d12TextureCopyLocation]()
                        dst_loc.p_resource = (*bp).res
                        dst_loc.type_ = d3d12.D3d12TextureCopyTypePlacedFootprint
                        dst_loc.anon0.SetPlacedFootprint(d3d12.D3d12PlacedSubresourceFootprint(
                            offset: off + Cast[u64](li) * slice_bytes,
                            footprint: d3d12.D3d12SubresourceFootprint(
                                format: Cast[dxgi.DxgiFormat]((*tp).dxgi_res),
                                width: Cast[windows.Uint](aw),
                                height: Cast[windows.Uint](ah),
                                depth: 1,
                                row_pitch: bpr,
                            ),
                        ))
                        var src_loc := mem.Zeroed[d3d12.D3d12TextureCopyLocation]()
                        src_loc.p_resource = (*tp).res
                        src_loc.type_ = d3d12.D3d12TextureCopyTypeSubresourceIndex
                        src_loc.anon0.SetSubresourceIndex(sub)
                        var bx_src := d3d12.D3d12Box(left: sx, top: sy, front: 0, right: sx + Cast[windows.Uint](aw), bottom: sy + Cast[windows.Uint](ah), back: 1)
                        (*cmd).CopyTextureRegion(raw.Of(&dst_loc), 0, 0, 0, raw.Of(&src_loc), raw.Of(&bx_src))
                    }
                } else if op == StreamOpCopyTexture {
                    pmask := HdrFlags(w0)
                    layer_count := Cast[int](HdrArg(w0))
                    stp := raw.FromAddr[DxTexture](Trunc[uint](words[wi + 1]))
                    dtp := raw.FromAddr[DxTexture](Trunc[uint](words[wi + 2]))
                    smip := Cast[int](Lo32(words[wi + 3]))
                    dmip := Cast[int](Hi32(words[wi + 3]))
                    sx := Lo32(words[wi + 4])
                    sy := Hi32(words[wi + 4])
                    cw := Lo32(words[wi + 5])
                    ch := Hi32(words[wi + 5])
                    dx := Lo32(words[wi + 6])
                    dy := Hi32(words[wi + 6])
                    slayer := Cast[int](Lo32(words[wi + 7]))
                    dlayer := Cast[int](Hi32(words[wi + 7]))
                    for plane in 0..2 {
                        if (pmask & Trunc[u16](1 << plane)) == 0 {
                            continue
                        }
                        for li in 0..layer_count {
                            ssub := Cast[windows.Uint](smip + (slayer + li) * (*stp).mips + plane * (*stp).mips * (*stp).layers)
                            dsub := Cast[windows.Uint](dmip + (dlayer + li) * (*dtp).mips + plane * (*dtp).mips * (*dtp).layers)
                            var dst_loc := mem.Zeroed[d3d12.D3d12TextureCopyLocation]()
                            dst_loc.p_resource = (*dtp).res
                            dst_loc.type_ = d3d12.D3d12TextureCopyTypeSubresourceIndex
                            dst_loc.anon0.SetSubresourceIndex(dsub)
                            var src_loc := mem.Zeroed[d3d12.D3d12TextureCopyLocation]()
                            src_loc.p_resource = (*stp).res
                            src_loc.type_ = d3d12.D3d12TextureCopyTypeSubresourceIndex
                            src_loc.anon0.SetSubresourceIndex(ssub)
                            var bx_src := d3d12.D3d12Box(left: sx, top: sy, front: 0, right: sx + cw, bottom: sy + ch, back: 1)
                            (*cmd).CopyTextureRegion(raw.Of(&dst_loc), dx, dy, 0, raw.Of(&src_loc), raw.Of(&bx_src))
                        }
                    }
                } else if op == StreamOpClearBuffer {
                    nbytes := u64(HdrArg(w0))
                    bp := raw.FromAddr[DxBuffer](Trunc[uint](words[wi + 1]))
                    at := words[wi + 2]
                    d := (*p).dev
                    var done: u64 = 0
                    for done < nbytes {
                        rem := nbytes - done
                        chunk := if rem > (*d).zero_buf_size { (*d).zero_buf_size } else { rem }
                        (*cmd).CopyBufferRegion((*bp).res, at + done, (*d).zero_buf, 0, chunk)
                        done += chunk
                    }
                } else if op == StreamOpPushDebugGroup {
                    off := int(HdrArg(w0))
                    llen := int(Lo32(words[wi + 1]))
                    if off + llen <= labels.Len() {
                        var tmp: [256]u8 = [0; 256]
                        c := if llen > 255 { 255 } else { llen }
                        for k in 0..c {
                            tmp[k] = labels[off + k]
                        }
                        tmp[c] = 0
                        (*cmd).BeginEvent(1, raw.PtrCast[u8, void](raw.Of(&tmp[0])), Cast[windows.Uint](c + 1))
                    }
                } else if op == StreamOpPopDebugGroup {
                    (*cmd).EndEvent()
                }
                wi += wcnt
            }
        }
    }
}

ReplaySys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to live ReplayArgs" {
        p := raw.PtrCast[u8, ReplayArgs](arg)
        d := (*p).dev
        cmd := (*d).cmd_list
        hr := (*(*p).alloc).Reset()
        if hr < 0 {
            (*p).hr = hr
            (*p).step = 0
            return
        }
        hr2 := (*cmd).Reset((*p).alloc, raw.Null[d3d12.ID3D12PipelineState]())
        if hr2 < 0 {
            (*p).hr = hr2
            (*p).step = 0
            return
        }
        var heaps: [2]*d3d12.ID3D12DescriptorHeap = [(*d).cbv_srv_uav_heap.heap, (*d).sampler_heap.heap]
        (*cmd).SetDescriptorHeaps(2, raw.Of(&heaps[0]))
        ReplayRecord(p, cmd)
        hr3 := (*cmd).Close()
        if hr3 < 0 {
            (*p).hr = hr3
            (*p).step = 1
            return
        }
        var lists: [1]*d3d12.ID3D12CommandList = [raw.PtrCast[d3d12.ID3D12GraphicsCommandList, d3d12.ID3D12CommandList](cmd)]
        (*(*d).queue).ExecuteCommandLists(1, raw.Of(&lists[0]))
        hr4 := (*(*d).queue).Signal((*d).fence, (*p).serial)
        (*p).hr = hr4
        (*p).step = 2
    }
}

/// Captures a chunk word slice pointer and length for replay without holding slice borrows.
export MakeChunkSpan(words: []u64): ChunkSpan {
    return ChunkSpan(ptr: raw.Data(words), len: words.Len())
}

/// Replays a portable command stream into the D3D12 command list and submits it
/// under serial inside one MkSystemRun switch.
export Device.ReplayAndSubmit(
    mut self,
    chunks: []ChunkSpan,
    barriers: []BarrierSpec,
    labels: []u8,
    passes: []PassReplaySpec,
    skip_pass_barrier: bool,
    serial: u64,
): Error!void {
    if Target.os != Os.Windows {
        fail Unsupported
    }
    if self.lost {
        fail DxFail(kind: self.loss_reason, code: 0, call: CallExecuteCommandLists, at: -1)
    }
    alloc_idx := try AcquireCmdAllocator(self)
    var args := ReplayArgs(
        dev: raw.OfMut(self),
        alloc: self.cmd_allocs[alloc_idx].alloc,
        chunks_ptr: if chunks.Len() > 0 { raw.Data(chunks) } else { raw.Null[ChunkSpan]() },
        chunk_count: chunks.Len(),
        barriers_ptr: if barriers.Len() > 0 { raw.Data(barriers) } else { raw.Null[BarrierSpec]() },
        barrier_count: barriers.Len(),
        labels_ptr: if labels.Len() > 0 { raw.Data(labels) } else { raw.Null[u8]() },
        label_count: labels.Len(),
        passes_ptr: if passes.Len() > 0 { raw.Data(passes) } else { raw.Null[PassReplaySpec]() },
        pass_count: passes.Len(),
        skip_pass_barrier: skip_pass_barrier,
        serial: serial,
        hr: 0,
        step: 0,
    )
    SysRun(mut args, ReplaySys)
    self.RecordCommandSubmission(alloc_idx, serial, args.step == 2, args.hr >= 0)
    has_dbg_err := self.DrainInfoQueue()
    if args.hr < 0 {
        fail MapHrError(self, args.hr, CallExecuteCommandLists)
    }
    if has_dbg_err {
        fail DxFail(kind: ErrKind.DebugLayer, code: 0, call: CallExecuteCommandLists, at: -1)
    }
}
