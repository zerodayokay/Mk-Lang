// Why: D3D12 shaders store validated DXIL bytecode on the device heap, and
// graphics/compute PSOs must compile off the task stack on runtime.Blocking.
//
// What: Device.CreateShader, DestroyShader, Device.CreateRenderPipeline,
// Device.CreateComputePipeline, and Device.DestroyPipeline.
//
// How: copies DXIL into a heap-backed DxShader record, translates portable
// vertex/blend/rasterizer/depth-stencil descriptors into D3D12 PSO structs,
// and calls CreateGraphicsPipelineState / CreateComputePipelineState via Blocking.

import c.windows
import c.dxgi
import c.d3d12
import std.raw
import std.mem
import std.runtime

/// Copies DXIL bytecode into a device-owned shader handle.
export Device.CreateShader(mut self, dxil: []u8): Error!u64 {
    if Target.os != Os.Windows {
        fail Unsupported
    }
    if dxil.Len() == 0 {
        fail Unsupported
    }
    a := self.heap.Alloc()
    slot := try AllocRecSlot[DxShader](a)
    var list := HeapList[u8].New(a)
    list.Extend(a, dxil) else e {
        list.Free(a)
        unsafe "slot was reserved before failure and never written" {
            a.FreeBytes(raw.PtrCast[DxShader, u8](slot), SizeOf[DxShader](), AlignOf[DxShader]())
        }
        fail e
    }
    return WriteRecSlot(slot, DxShader(dxil: list))
}

/// Frees a D3D12 shader handle and its DXIL bytecode copy.
export Device.DestroyShader(mut self, nh: u64) {
    if Target.os != Os.Windows || nh == 0 {
        return
    }
    a := self.heap.Alloc()
    rec := FreeRec[DxShader](a, nh)
    DxShader(dxil) := rec
    dxil.Free(a)
}

MapBlendFactor(f: u8, alpha: bool): d3d12.D3d12Blend {
    return match f {
        0 { d3d12.D3d12BlendZero }
        1 { d3d12.D3d12BlendOne }
        2 { if alpha { d3d12.D3d12BlendSrcAlpha } else { d3d12.D3d12BlendSrcColor } }
        3 { if alpha { d3d12.D3d12BlendInvSrcAlpha } else { d3d12.D3d12BlendInvSrcColor } }
        4 { d3d12.D3d12BlendSrcAlpha }
        5 { d3d12.D3d12BlendInvSrcAlpha }
        6 { if alpha { d3d12.D3d12BlendDestAlpha } else { d3d12.D3d12BlendDestColor } }
        7 { if alpha { d3d12.D3d12BlendInvDestAlpha } else { d3d12.D3d12BlendInvDestColor } }
        8 { d3d12.D3d12BlendDestAlpha }
        9 { d3d12.D3d12BlendInvDestAlpha }
        10 { d3d12.D3d12BlendBlendFactor }
        _ { d3d12.D3d12BlendInvBlendFactor }
    }
}

MapBlendOp(op: u8): d3d12.D3d12BlendOp {
    return match op {
        0 { d3d12.D3d12BlendOpAdd }
        1 { d3d12.D3d12BlendOpSubtract }
        2 { d3d12.D3d12BlendOpRevSubtract }
        3 { d3d12.D3d12BlendOpMin }
        _ { d3d12.D3d12BlendOpMax }
    }
}

MapStencilOp(op: u8): d3d12.D3d12StencilOp {
    return match op {
        0 { d3d12.D3d12StencilOpKeep }
        1 { d3d12.D3d12StencilOpZero }
        2 { d3d12.D3d12StencilOpReplace }
        3 { d3d12.D3d12StencilOpIncrSat }
        4 { d3d12.D3d12StencilOpDecrSat }
        5 { d3d12.D3d12StencilOpInvert }
        6 { d3d12.D3d12StencilOpIncr }
        _ { d3d12.D3d12StencilOpDecr }
    }
}

MapStencilFace(f: StencilFaceSpec): d3d12.D3d12DepthStencilopDesc {
    return d3d12.D3d12DepthStencilopDesc(
        stencil_fail_op: MapStencilOp(f.on_fail),
        stencil_depth_fail_op: MapStencilOp(f.on_depth_fail),
        stencil_pass_op: MapStencilOp(f.on_pass),
        stencil_func: MapCompareFunc(f.compare),
    )
}

MapTopologyType(t: u8): d3d12.D3d12PrimitiveTopologyType {
    return match t {
        0 { d3d12.D3d12PrimitiveTopologyTypePoint }
        1 | 2 { d3d12.D3d12PrimitiveTopologyTypeLine }
        _ { d3d12.D3d12PrimitiveTopologyTypeTriangle }
    }
}

MapPrimitiveTopology(t: u8): d3d12.D3d12PrimitiveTopology {
    return match t {
        0 { d3d12.D3dPrimitiveTopologyPointlist }
        1 { d3d12.D3dPrimitiveTopologyLinelist }
        2 { d3d12.D3dPrimitiveTopologyLinestrip }
        3 { d3d12.D3dPrimitiveTopologyTrianglelist }
        _ { d3d12.D3dPrimitiveTopologyTrianglestrip }
    }
}

struct CreateRenderPsoArgs {
    dev: *d3d12.ID3D12Device
    desc: d3d12.D3d12GraphicsPipelineStateDesc
    out_pso: *d3d12.ID3D12PipelineState
    hr: windows.Hresult
}

CreateRenderPsoJob(addr: uint): uint {
    if Target.os != Os.Windows {
        return addr
    }
    unsafe "addr points to live CreateRenderPsoArgs on caller frame parked in Blocking" {
        p := raw.FromAddr[CreateRenderPsoArgs](addr)
        var pso_ptr: *void = raw.Null[void]()
        hr := (*(*p).dev).CreateGraphicsPipelineState(
            raw.Of(&(*p).desc),
            raw.Of(&d3d12.IidID3D12PipelineState),
            raw.OfMut(mut pso_ptr),
        )
        (*p).hr = hr
        if hr >= 0 && !raw.IsNull(pso_ptr) {
            (*p).out_pso = raw.PtrCast[void, d3d12.ID3D12PipelineState](pso_ptr)
        }
    }
    return addr
}

struct CreateComputePsoArgs {
    dev: *d3d12.ID3D12Device
    desc: d3d12.D3d12ComputePipelineStateDesc
    out_pso: *d3d12.ID3D12PipelineState
    hr: windows.Hresult
}

CreateComputePsoJob(addr: uint): uint {
    if Target.os != Os.Windows {
        return addr
    }
    unsafe "addr points to live CreateComputePsoArgs on caller frame parked in Blocking" {
        p := raw.FromAddr[CreateComputePsoArgs](addr)
        var pso_ptr: *void = raw.Null[void]()
        hr := (*(*p).dev).CreateComputePipelineState(
            raw.Of(&(*p).desc),
            raw.Of(&d3d12.IidID3D12PipelineState),
            raw.OfMut(mut pso_ptr),
        )
        (*p).hr = hr
        if hr >= 0 && !raw.IsNull(pso_ptr) {
            (*p).out_pso = raw.PtrCast[void, d3d12.ID3D12PipelineState](pso_ptr)
        }
    }
    return addr
}

ReleasePsoSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to *ID3D12PipelineState" {
        pp := raw.PtrCast[u8, *d3d12.ID3D12PipelineState](arg)
        pso := raw.Read(pp)
        if !raw.IsNull(pso) {
            _ = (*pso).Release()
        }
    }
}

ReleasePsoPtr(pso: *d3d12.ID3D12PipelineState) {
    if Target.os != Os.Windows || raw.IsNull(pso) {
        return
    }
    var p := pso
    SysRun(mut p, ReleasePsoSys)
}

/// Compiles a D3D12 graphics PSO on runtime.Blocking and returns its handle.
export Device.CreateRenderPipeline(mut self, io: Io, spec: &RenderPipeSpec): Error!u64 {
    if Target.os != Os.Windows {
        fail Unsupported
    }
    if self.lost {
        fail DxFail(kind: self.loss_reason, code: 0, call: CallCreatePipeline, at: -1)
    }

    var rs_ptr := raw.Null[DxRootSig]()
    var vs_ptr := raw.Null[DxShader]()
    var fs_ptr := raw.Null[DxShader]()
    unsafe "root_sig_nh, vs_nh, and optional fs_nh are live DxRootSig/DxShader records" {
        rs_ptr = raw.FromAddr[DxRootSig](Trunc[uint](spec.root_sig_nh))
        vs_ptr = raw.FromAddr[DxShader](Trunc[uint](spec.vs_nh))
        if spec.has_fs {
            fs_ptr = raw.FromAddr[DxShader](Trunc[uint](spec.fs_nh))
        }
    }

    var desc := mem.Zeroed[d3d12.D3d12GraphicsPipelineStateDesc]()
    unsafe "rs_ptr and vs_ptr point to live records with valid DXIL slices" {
        desc.p_root_signature = (*rs_ptr).sig
        desc.vs = d3d12.D3d12ShaderBytecode(
            p_shader_bytecode: raw.PtrCast[u8, void](raw.Data((*vs_ptr).dxil.Items())),
            bytecode_length: Cast[u64]((*vs_ptr).dxil.Len()),
        )
        if spec.has_fs && !raw.IsNull(fs_ptr) {
            desc.ps = d3d12.D3d12ShaderBytecode(
                p_shader_bytecode: raw.PtrCast[u8, void](raw.Data((*fs_ptr).dxil.Items())),
                bytecode_length: Cast[u64]((*fs_ptr).dxil.Len()),
            )
        }
    }

    // Input layout: max 16 attributes.
    var elems: [16]d3d12.D3d12InputElementDesc = [mem.Zeroed[d3d12.D3d12InputElementDesc](); 16]
    acnt := spec.attrs.Len()
    sem := "LOC\0"
    for i in 0..acnt {
        a := &spec.attrs[i]
        elems[i] = d3d12.D3d12InputElementDesc(
            semantic_name: raw.StrData(sem),
            semantic_index: Cast[windows.Uint](a.location),
            format: Cast[dxgi.DxgiFormat](a.dxgi_format),
            input_slot: Cast[windows.Uint](a.slot),
            aligned_byte_offset: Cast[windows.Uint](a.offset),
            input_slot_class: if a.per_instance {
                d3d12.D3d12InputClassificationPerInstanceData
            } else {
                d3d12.D3d12InputClassificationPerVertexData
            },
            instance_data_step_rate: if a.per_instance { 1 } else { 0 },
        )
    }
    if acnt > 0 {
        desc.input_layout.p_input_element_descs = raw.Of(&elems[0])
        desc.input_layout.num_elements = Cast[windows.Uint](acnt)
    }

    // Blend state.
    desc.sample_mask = 0xFFFFFFFF
    desc.blend_state.alpha_to_coverage_enable = if spec.alpha_to_coverage { 1 } else { 0 }
    desc.blend_state.independent_blend_enable = 1
    tcnt := spec.targets.Len()
    desc.num_render_targets = Cast[windows.Uint](tcnt)
    for i in 0..8 {
        if i < tcnt {
            t := &spec.targets[i]
            desc.rtv_formats[i] = Cast[dxgi.DxgiFormat](t.dxgi_format)
            var rt := mem.Zeroed[d3d12.D3d12RenderTargetBlendDesc]()
            rt.blend_enable = if t.blend_enable { 1 } else { 0 }
            if t.blend_enable {
                rt.src_blend = MapBlendFactor(t.color.src, false)
                rt.dest_blend = MapBlendFactor(t.color.dst, false)
                rt.blend_op = MapBlendOp(t.color.op)
                rt.src_blend_alpha = MapBlendFactor(t.alpha.src, true)
                rt.dest_blend_alpha = MapBlendFactor(t.alpha.dst, true)
                rt.blend_op_alpha = MapBlendOp(t.alpha.op)
            } else {
                rt.src_blend = d3d12.D3d12BlendOne
                rt.dest_blend = d3d12.D3d12BlendZero
                rt.blend_op = d3d12.D3d12BlendOpAdd
                rt.src_blend_alpha = d3d12.D3d12BlendOne
                rt.dest_blend_alpha = d3d12.D3d12BlendZero
                rt.blend_op_alpha = d3d12.D3d12BlendOpAdd
            }
            rt.render_target_write_mask = t.write_mask
            desc.blend_state.render_target[i] = rt
        } else {
            desc.rtv_formats[i] = dxgi.DxgiFormatUnknown
            var rt := mem.Zeroed[d3d12.D3d12RenderTargetBlendDesc]()
            rt.src_blend = d3d12.D3d12BlendOne
            rt.dest_blend = d3d12.D3d12BlendZero
            rt.blend_op = d3d12.D3d12BlendOpAdd
            rt.src_blend_alpha = d3d12.D3d12BlendOne
            rt.dest_blend_alpha = d3d12.D3d12BlendZero
            rt.blend_op_alpha = d3d12.D3d12BlendOpAdd
            rt.render_target_write_mask = 15
            desc.blend_state.render_target[i] = rt
        }
    }

    // Rasterizer state.
    desc.rasterizer_state.fill_mode = d3d12.D3d12FillModeSolid
    desc.rasterizer_state.cull_mode = if spec.cull == 1 {
        d3d12.D3d12CullModeFront
    } else if spec.cull == 2 {
        d3d12.D3d12CullModeBack
    } else {
        d3d12.D3d12CullModeNone
    }
    desc.rasterizer_state.front_counter_clockwise = if spec.front_ccw { 1 } else { 0 }
    desc.rasterizer_state.depth_bias = spec.bias_constant
    desc.rasterizer_state.depth_bias_clamp = spec.bias_clamp
    desc.rasterizer_state.slope_scaled_depth_bias = spec.bias_slope
    desc.rasterizer_state.depth_clip_enable = if spec.depth_clip_disable { 0 } else { 1 }
    desc.rasterizer_state.multisample_enable = if spec.samples > 1 { 1 } else { 0 }

    // Depth-stencil state.
    if spec.has_depth {
        desc.dsv_format = Cast[dxgi.DxgiFormat](spec.depth_dxgi)
        desc.depth_stencil_state.depth_enable = 1
        desc.depth_stencil_state.depth_write_mask = if spec.depth_write {
            d3d12.D3d12DepthWriteMaskAll
        } else {
            d3d12.D3d12DepthWriteMaskZero
        }
        desc.depth_stencil_state.depth_func = MapCompareFunc(spec.depth_compare)
        if spec.stencil_enable {
            desc.depth_stencil_state.stencil_enable = 1
            desc.depth_stencil_state.stencil_read_mask = spec.stencil_read_mask
            desc.depth_stencil_state.stencil_write_mask = spec.stencil_write_mask
            desc.depth_stencil_state.front_face = MapStencilFace(spec.stencil_front)
            desc.depth_stencil_state.back_face = MapStencilFace(spec.stencil_back)
        }
    } else {
        desc.dsv_format = dxgi.DxgiFormatUnknown
    }

    desc.primitive_topology_type = MapTopologyType(spec.topology)
    desc.ib_strip_cut_value = if !spec.has_strip_cut {
        d3d12.D3d12IndexBufferStripCutValueDisabled
    } else if spec.strip_cut_u32 {
        d3d12.D3d12IndexBufferStripCutValue0xFfffffff
    } else {
        d3d12.D3d12IndexBufferStripCutValue0xFfff
    }
    desc.sample_desc.count = Cast[windows.Uint](spec.samples)
    desc.sample_desc.quality = 0

    var args := CreateRenderPsoArgs(
        dev: self.dev,
        desc: desc,
        out_pso: raw.Null[d3d12.ID3D12PipelineState](),
        hr: 0,
    )
    _ = try runtime.Blocking(io, raw.Addr(raw.OfMut(mut args)), CreateRenderPsoJob)
    _ = self.DrainInfoQueue()
    if args.hr < 0 || raw.IsNull(args.out_pso) {
        fail MapHrError(self, args.hr, CallCreatePipeline)
    }
    SetPipelineName(args.out_pso, spec.label)
    rec := DxPipeline(
        pso: args.out_pso,
        root_sig: rs_ptr,
        is_compute: false,
        topology: MapPrimitiveTopology(spec.topology),
    )
    return AllocRec(self.heap.Alloc(), rec) else e {
        ReleasePsoPtr(args.out_pso)
        fail e
    }
}

/// Compiles a D3D12 compute PSO on runtime.Blocking and returns its handle.
export Device.CreateComputePipeline(mut self, io: Io, root_sig_nh: u64, cs_nh: u64, label: str): Error!u64 {
    if Target.os != Os.Windows {
        fail Unsupported
    }
    if self.lost {
        fail DxFail(kind: self.loss_reason, code: 0, call: CallCreatePipeline, at: -1)
    }

    var rs_ptr := raw.Null[DxRootSig]()
    var cs_ptr := raw.Null[DxShader]()
    var desc := mem.Zeroed[d3d12.D3d12ComputePipelineStateDesc]()
    unsafe "root_sig_nh and cs_nh are live DxRootSig and DxShader records" {
        rs_ptr = raw.FromAddr[DxRootSig](Trunc[uint](root_sig_nh))
        cs_ptr = raw.FromAddr[DxShader](Trunc[uint](cs_nh))
        desc.p_root_signature = (*rs_ptr).sig
        desc.cs = d3d12.D3d12ShaderBytecode(
            p_shader_bytecode: raw.PtrCast[u8, void](raw.Data((*cs_ptr).dxil.Items())),
            bytecode_length: Cast[u64]((*cs_ptr).dxil.Len()),
        )
    }

    var args := CreateComputePsoArgs(
        dev: self.dev,
        desc: desc,
        out_pso: raw.Null[d3d12.ID3D12PipelineState](),
        hr: 0,
    )
    _ = try runtime.Blocking(io, raw.Addr(raw.OfMut(mut args)), CreateComputePsoJob)
    _ = self.DrainInfoQueue()
    if args.hr < 0 || raw.IsNull(args.out_pso) {
        fail MapHrError(self, args.hr, CallCreatePipeline)
    }
    SetPipelineName(args.out_pso, label)
    rec := DxPipeline(
        pso: args.out_pso,
        root_sig: rs_ptr,
        is_compute: true,
        topology: d3d12.D3dPrimitiveTopologyUndefined,
    )
    return AllocRec(self.heap.Alloc(), rec) else e {
        ReleasePsoPtr(args.out_pso)
        fail e
    }
}

/// Releases a D3D12 graphics or compute PSO handle.
export Device.DestroyPipeline(mut self, nh: u64) {
    if Target.os != Os.Windows || nh == 0 {
        return
    }
    a := self.heap.Alloc()
    rec := FreeRec[DxPipeline](a, nh)
    ReleasePsoPtr(rec.pso)
}
