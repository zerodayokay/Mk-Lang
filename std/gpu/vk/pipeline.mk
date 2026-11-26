// Why: Vulkan shader modules and render/compute pipelines compile SPIR-V
// bytecode through runtime.Blocking with dynamic rendering and dynamic
// viewport/scissor/blend/stencil states.
//
// What: CreateShader, DestroyShader, CreateRenderPipeline,
// CreateComputePipeline, and DestroyPipeline.
//
// How: builds VkGraphicsPipelineCreateInfo (chained with
// VkPipelineRenderingCreateInfo) or VkComputePipelineCreateInfo and runs the
// driver compiler inside runtime.Blocking.

import c.vulkan
import std.mem
import std.raw
import std.runtime

AllocShaderSlot(st: mut VkDeviceState, a: &Alloc, rec: VkShaderRec): OutOfMemory!u64 {
    if st.shaders.Len() == 0 {
        try st.shaders.Push(a, mem.Zeroed[VkShaderRec]())
    }
    for i in 1..st.shaders.Len() {
        if !st.shaders[i].alive {
            st.shaders[i] = rec
            return Cast[u64](i)
        }
    }
    id := Cast[u64](st.shaders.Len())
    try st.shaders.Push(a, rec)
    return id
}

AllocPipeSlot(st: mut VkDeviceState, a: &Alloc, rec: VkPipeRec): OutOfMemory!u64 {
    if st.pipelines.Len() == 0 {
        try st.pipelines.Push(a, mem.Zeroed[VkPipeRec]())
    }
    for i in 1..st.pipelines.Len() {
        if !st.pipelines[i].alive {
            st.pipelines[i] = rec
            return Cast[u64](i)
        }
    }
    id := Cast[u64](st.pipelines.Len())
    try st.pipelines.Push(a, rec)
    return id
}

/// Creates a VkShaderModule from validated SPIR-V bytes.
export Device.CreateShader(mut self, spirv: []u8, label: str): Error!u64 {
    a := self.heap.Alloc()
    st := self.state.Mut()
    if st.lost { fail DeviceLost(reason: st.loss_reason) }
    unsafe "st.device and spirv bytes are valid" {
        dev := HandleFrom[vulkan.VkDeviceT](st.device)
        var ci := mem.Zeroed[vulkan.VkShaderModuleCreateInfo]()
        ci.s_type = vulkan.VkStructureTypeShaderModuleCreateInfo
        ci.code_size = Trunc[uint](spirv.Len())
        ci.p_code = raw.PtrCast[u8, u32](raw.Data(spirv))
        var sm: vulkan.VkShaderModule = raw.Null[vulkan.VkShaderModuleT]()
        r := st.dev_d.vk_create_shader_module.Must()(dev, raw.Of(&ci), raw.Null[vulkan.VkAllocationCallbacks](), raw.OfMut(mut sm))
        if r != vulkan.VkSuccess {
            if IsDeviceLostResult(r) { st.lost = true }
            fail MapVkError(r, CallCreatePipeline)
        }
        st.live_native += 1
        id := AllocShaderSlot(st, a, VkShaderRec(alive: true, vk_module: HandleAddr(sm))) else e {
            st.dev_d.vk_destroy_shader_module.Must()(dev, sm, raw.Null[vulkan.VkAllocationCallbacks]())
            if st.live_native > 0 { st.live_native -= 1 }
            fail e
        }
        SetVkObjectName(&*st, vulkan.VkObjectTypeShaderModule, HandleAddr(sm), label)
        return id
    }
}

DestroyShaderRec(st: mut VkDeviceState, id: u64) {
    idx := Cast[int](id)
    if idx <= 0 || idx >= st.shaders.Len() || !st.shaders[idx].alive {
        return
    }
    rec := st.shaders[idx]
    st.shaders[idx].alive = false
    unsafe "st.device and rec.vk_module are live" {
        st.dev_d.vk_destroy_shader_module.Must()(HandleFrom[vulkan.VkDeviceT](st.device), HandleFrom[vulkan.VkShaderModuleT](rec.vk_module), raw.Null[vulkan.VkAllocationCallbacks]())
        if st.live_native > 0 {
            st.live_native -= 1
        }
    }
}

/// Destroys a VkShaderModule.
export Device.DestroyShader(mut self, id: u64) {
    DestroyShaderRec(self.state.Mut(), id)
}

MapVkTopology(t: int): vulkan.VkPrimitiveTopology {
    return match t {
        0 { vulkan.VkPrimitiveTopologyPointList }
        1 { vulkan.VkPrimitiveTopologyLineList }
        2 { vulkan.VkPrimitiveTopologyLineStrip }
        4 { vulkan.VkPrimitiveTopologyTriangleStrip }
        _ { vulkan.VkPrimitiveTopologyTriangleList }
    }
}

MapVkCull(c: int): u32 {
    return match c {
        1 { Cast[u32](vulkan.VkCullModeFrontBit) }
        2 { Cast[u32](vulkan.VkCullModeBackBit) }
        _ { Cast[u32](vulkan.VkCullModeNone) }
    }
}

MapVkBlendFactor(f: int): vulkan.VkBlendFactor {
    return match f {
        0 { vulkan.VkBlendFactorZero }
        1 { vulkan.VkBlendFactorOne }
        2 { vulkan.VkBlendFactorSrcColor }
        3 { vulkan.VkBlendFactorOneMinusSrcColor }
        4 { vulkan.VkBlendFactorSrcAlpha }
        5 { vulkan.VkBlendFactorOneMinusSrcAlpha }
        6 { vulkan.VkBlendFactorDstColor }
        7 { vulkan.VkBlendFactorOneMinusDstColor }
        8 { vulkan.VkBlendFactorDstAlpha }
        9 { vulkan.VkBlendFactorOneMinusDstAlpha }
        10 { vulkan.VkBlendFactorConstantColor }
        _ { vulkan.VkBlendFactorOneMinusConstantColor }
    }
}

MapVkBlendOp(op: int): vulkan.VkBlendOp {
    return match op {
        1 { vulkan.VkBlendOpSubtract }
        2 { vulkan.VkBlendOpReverseSubtract }
        3 { vulkan.VkBlendOpMin }
        4 { vulkan.VkBlendOpMax }
        _ { vulkan.VkBlendOpAdd }
    }
}

MapVkStencilOp(op: int): vulkan.VkStencilOp {
    return match op {
        1 { vulkan.VkStencilOpZero }
        2 { vulkan.VkStencilOpReplace }
        3 { vulkan.VkStencilOpIncrementAndClamp }
        4 { vulkan.VkStencilOpDecrementAndClamp }
        5 { vulkan.VkStencilOpInvert }
        6 { vulkan.VkStencilOpIncrementAndWrap }
        7 { vulkan.VkStencilOpDecrementAndWrap }
        _ { vulkan.VkStencilOpKeep }
    }
}

struct CompileGfxJob {
    vk_dev: u64
    create_fn_addr: uint
    pl: u64
    vs_mod: u64
    fs_mod: u64
    vs_entry: str
    fs_entry: str
    d24s8_fmt: i32
    // Flattened plain arrays (Send!) for Blocking.
    vb_cnt: int
    vb_strides: [16]u32
    vb_inst: [16]bool
    va_cnt: int
    va_locs: [16]u32
    va_binds: [16]u32
    va_fmts: [16]i32
    va_offs: [16]u32
    topology: int
    has_strip_idx: bool
    cull: int
    front_cw: bool
    tgt_cnt: int
    tgt_fmts: [8]i32
    tgt_blend: [8]bool
    tgt_sc: [8]int
    tgt_dc: [8]int
    tgt_oc: [8]int
    tgt_sa: [8]int
    tgt_da: [8]int
    tgt_oa: [8]int
    tgt_mask: [8]u8
    has_depth: bool
    depth_vk_fmt: i32
    depth_write: bool
    depth_compare: int
    has_stencil: bool
    stencil_front: StencilFaceDesc
    stencil_back: StencilFaceDesc
    stencil_read_mask: u8
    stencil_write_mask: u8
    bias_const: i32
    bias_slope: f32
    bias_clamp: f32
    samples: int
    alpha_to_coverage: bool
    out_pipe: u64
    out_res: i32
}

RunCompileGfxJob(j0: CompileGfxJob): CompileGfxJob {
    var j := j0
    unsafe "j carries plain handles valid while Blocking waits" {
        dev := HandleFrom[vulkan.VkDeviceT](j.vk_dev)
        create_fn := raw.FnFromAddr[vulkan.PfnVkCreateGraphicsPipelines](j.create_fn_addr).Must()

        var stages: [2]vulkan.VkPipelineShaderStageCreateInfo = [mem.Zeroed[vulkan.VkPipelineShaderStageCreateInfo](); 2]
        var scnt: u32 = 1
        stages[0].s_type = vulkan.VkStructureTypePipelineShaderStageCreateInfo
        stages[0].stage = vulkan.VkShaderStageVertexBit
        stages[0].module = HandleFrom[vulkan.VkShaderModuleT](j.vs_mod)
        stages[0].p_name = raw.StrData(j.vs_entry)
        if j.fs_mod != 0 {
            stages[1].s_type = vulkan.VkStructureTypePipelineShaderStageCreateInfo
            stages[1].stage = vulkan.VkShaderStageFragmentBit
            stages[1].module = HandleFrom[vulkan.VkShaderModuleT](j.fs_mod)
            stages[1].p_name = raw.StrData(j.fs_entry)
            scnt = 2
        }

        var vbs: [16]vulkan.VkVertexInputBindingDescription = [mem.Zeroed[vulkan.VkVertexInputBindingDescription](); 16]
        for vi in 0..j.vb_cnt {
            vbs[vi] = vulkan.VkVertexInputBindingDescription(
                binding: Cast[u32](vi),
                stride: j.vb_strides[vi],
                input_rate: if j.vb_inst[vi] { vulkan.VkVertexInputRateInstance } else { vulkan.VkVertexInputRateVertex },
            )
        }
        var vas: [16]vulkan.VkVertexInputAttributeDescription = [mem.Zeroed[vulkan.VkVertexInputAttributeDescription](); 16]
        for ai in 0..j.va_cnt {
            vas[ai] = vulkan.VkVertexInputAttributeDescription(
                location: j.va_locs[ai],
                binding: j.va_binds[ai],
                format: j.va_fmts[ai],
                offset: j.va_offs[ai],
            )
        }
        var vis := mem.Zeroed[vulkan.VkPipelineVertexInputStateCreateInfo]()
        vis.s_type = vulkan.VkStructureTypePipelineVertexInputStateCreateInfo
        vis.vertex_binding_description_count = Cast[u32](j.vb_cnt)
        vis.p_vertex_binding_descriptions = if j.vb_cnt > 0 { raw.Data(vbs[..]) } else { raw.Null[vulkan.VkVertexInputBindingDescription]() }
        vis.vertex_attribute_description_count = Cast[u32](j.va_cnt)
        vis.p_vertex_attribute_descriptions = if j.va_cnt > 0 { raw.Data(vas[..]) } else { raw.Null[vulkan.VkVertexInputAttributeDescription]() }

        var ias := mem.Zeroed[vulkan.VkPipelineInputAssemblyStateCreateInfo]()
        ias.s_type = vulkan.VkStructureTypePipelineInputAssemblyStateCreateInfo
        ias.topology = MapVkTopology(j.topology)
        ias.primitive_restart_enable = if j.has_strip_idx { vulkan.VkTrue } else { vulkan.VkFalse }

        var vps := mem.Zeroed[vulkan.VkPipelineViewportStateCreateInfo]()
        vps.s_type = vulkan.VkStructureTypePipelineViewportStateCreateInfo
        vps.viewport_count = 1
        vps.scissor_count = 1

        var ras := mem.Zeroed[vulkan.VkPipelineRasterizationStateCreateInfo]()
        ras.s_type = vulkan.VkStructureTypePipelineRasterizationStateCreateInfo
        ras.polygon_mode = vulkan.VkPolygonModeFill
        ras.cull_mode = MapVkCull(j.cull)
        ras.front_face = if j.front_cw { vulkan.VkFrontFaceClockwise } else { vulkan.VkFrontFaceCounterClockwise }
        ras.depth_bias_enable = if j.has_depth && (j.bias_const != 0 || j.bias_slope != 0.0 || j.bias_clamp != 0.0) { vulkan.VkTrue } else { vulkan.VkFalse }
        ras.depth_bias_constant_factor = Cast[f32](j.bias_const)
        ras.depth_bias_clamp = j.bias_clamp
        ras.depth_bias_slope_factor = j.bias_slope
        ras.line_width = 1.0

        var mss := mem.Zeroed[vulkan.VkPipelineMultisampleStateCreateInfo]()
        mss.s_type = vulkan.VkStructureTypePipelineMultisampleStateCreateInfo
        mss.rasterization_samples = if j.samples == 4 { vulkan.VkSampleCount4Bit } else { vulkan.VkSampleCount1Bit }
        mss.alpha_to_coverage_enable = if j.alpha_to_coverage { vulkan.VkTrue } else { vulkan.VkFalse }

        var dss := mem.Zeroed[vulkan.VkPipelineDepthStencilStateCreateInfo]()
        dss.s_type = vulkan.VkStructureTypePipelineDepthStencilStateCreateInfo
        if j.has_depth {
            dss.depth_test_enable = vulkan.VkTrue
            dss.depth_write_enable = if j.depth_write { vulkan.VkTrue } else { vulkan.VkFalse }
            dss.depth_compare_op = MapVkCompareOp(j.depth_compare)
            if j.has_stencil {
                dss.stencil_test_enable = vulkan.VkTrue
                dss.front = vulkan.VkStencilOpState(
                    fail_op: MapVkStencilOp(j.stencil_front.on_fail),
                    pass_op: MapVkStencilOp(j.stencil_front.on_pass),
                    depth_fail_op: MapVkStencilOp(j.stencil_front.on_depth_fail),
                    compare_op: MapVkCompareOp(j.stencil_front.compare),
                    compare_mask: Cast[u32](j.stencil_read_mask),
                    write_mask: Cast[u32](j.stencil_write_mask),
                    reference: 0,
                )
                dss.back = vulkan.VkStencilOpState(
                    fail_op: MapVkStencilOp(j.stencil_back.on_fail),
                    pass_op: MapVkStencilOp(j.stencil_back.on_pass),
                    depth_fail_op: MapVkStencilOp(j.stencil_back.on_depth_fail),
                    compare_op: MapVkCompareOp(j.stencil_back.compare),
                    compare_mask: Cast[u32](j.stencil_read_mask),
                    write_mask: Cast[u32](j.stencil_write_mask),
                    reference: 0,
                )
            }
        }

        var cbas: [8]vulkan.VkPipelineColorBlendAttachmentState = [mem.Zeroed[vulkan.VkPipelineColorBlendAttachmentState](); 8]
        for ti in 0..j.tgt_cnt {
            cbas[ti] = vulkan.VkPipelineColorBlendAttachmentState(
                blend_enable: if j.tgt_blend[ti] { vulkan.VkTrue } else { vulkan.VkFalse },
                src_color_blend_factor: MapVkBlendFactor(j.tgt_sc[ti]),
                dst_color_blend_factor: MapVkBlendFactor(j.tgt_dc[ti]),
                color_blend_op: MapVkBlendOp(j.tgt_oc[ti]),
                src_alpha_blend_factor: MapVkBlendFactor(j.tgt_sa[ti]),
                dst_alpha_blend_factor: MapVkBlendFactor(j.tgt_da[ti]),
                alpha_blend_op: MapVkBlendOp(j.tgt_oa[ti]),
                color_write_mask: Cast[u32](j.tgt_mask[ti]),
            )
        }
        var cbs := mem.Zeroed[vulkan.VkPipelineColorBlendStateCreateInfo]()
        cbs.s_type = vulkan.VkStructureTypePipelineColorBlendStateCreateInfo
        cbs.attachment_count = Cast[u32](j.tgt_cnt)
        cbs.p_attachments = if j.tgt_cnt > 0 { raw.Data(cbas[..]) } else { raw.Null[vulkan.VkPipelineColorBlendAttachmentState]() }

        var dyn_states: [4]vulkan.VkDynamicState = [
            vulkan.VkDynamicStateViewport,
            vulkan.VkDynamicStateScissor,
            vulkan.VkDynamicStateBlendConstants,
            vulkan.VkDynamicStateStencilReference,
        ]
        var dyns := mem.Zeroed[vulkan.VkPipelineDynamicStateCreateInfo]()
        dyns.s_type = vulkan.VkStructureTypePipelineDynamicStateCreateInfo
        dyns.dynamic_state_count = 4
        dyns.p_dynamic_states = raw.Data(dyn_states[..])

        var ren := mem.Zeroed[vulkan.VkPipelineRenderingCreateInfo]()
        ren.s_type = vulkan.VkStructureTypePipelineRenderingCreateInfo
        ren.color_attachment_count = Cast[u32](j.tgt_cnt)
        ren.p_color_attachment_formats = if j.tgt_cnt > 0 { raw.Data(j.tgt_fmts[..]) } else { raw.Null[i32]() }
        if j.has_depth {
            ren.depth_attachment_format = j.depth_vk_fmt
            if j.has_stencil {
                ren.stencil_attachment_format = j.depth_vk_fmt
            }
        }

        var gpci := mem.Zeroed[vulkan.VkGraphicsPipelineCreateInfo]()
        gpci.s_type = vulkan.VkStructureTypeGraphicsPipelineCreateInfo
        gpci.p_next = raw.PtrCast[vulkan.VkPipelineRenderingCreateInfo, void](raw.Of(&ren))
        gpci.stage_count = scnt
        gpci.p_stages = raw.Data(stages[..])
        gpci.p_vertex_input_state = raw.Of(&vis)
        gpci.p_input_assembly_state = raw.Of(&ias)
        gpci.p_viewport_state = raw.Of(&vps)
        gpci.p_rasterization_state = raw.Of(&ras)
        gpci.p_multisample_state = raw.Of(&mss)
        gpci.p_depth_stencil_state = raw.Of(&dss)
        gpci.p_color_blend_state = raw.Of(&cbs)
        gpci.p_dynamic_state = raw.Of(&dyns)
        gpci.layout = HandleFrom[vulkan.VkPipelineLayoutT](j.pl)

        var pipe: vulkan.VkPipeline = raw.Null[vulkan.VkPipelineT]()
        r := create_fn(dev, raw.Null[vulkan.VkPipelineCacheT](), 1, raw.Of(&gpci), raw.Null[vulkan.VkAllocationCallbacks](), raw.OfMut(mut pipe))
        j.out_res = r
        j.out_pipe = HandleAddr(pipe)
        return j
    }
}

/// Compiles a Vulkan graphics pipeline via runtime.Blocking.
export Device.CreateRenderPipeline(mut self, io: Io, d: RenderPipelineDesc, vs_entry: str, fs_entry: str, label: str): Error!u64 {
    a := self.heap.Alloc()
    st := self.state.Mut()
    if st.lost { fail DeviceLost(reason: st.loss_reason) }

    pl := st.pipe_layouts[Cast[int](d.layout_id)].vk_layout
    vs_mod := st.shaders[Cast[int](d.vs_id)].vk_module
    fs_mod := if d.has_fs { st.shaders[Cast[int](d.fs_id)].vk_module } else { u64(0) }

    var job := CompileGfxJob(
        vk_dev: st.device,
        create_fn_addr: 0,
        pl: pl,
        vs_mod: vs_mod,
        fs_mod: fs_mod,
        vs_entry: vs_entry,
        fs_entry: fs_entry,
        d24s8_fmt: st.d24s8_vk_format,
        vb_cnt: d.vertex_layouts.Len(),
        vb_strides: [0; 16],
        vb_inst: [false; 16],
        va_cnt: 0,
        va_locs: [0; 16],
        va_binds: [0; 16],
        va_fmts: [0; 16],
        va_offs: [0; 16],
        topology: d.topology,
        has_strip_idx: d.has_strip_idx,
        cull: d.cull,
        front_cw: d.front_cw,
        tgt_cnt: d.targets.Len(),
        tgt_fmts: [0; 8],
        tgt_blend: [false; 8],
        tgt_sc: [0; 8],
        tgt_dc: [0; 8],
        tgt_oc: [0; 8],
        tgt_sa: [0; 8],
        tgt_da: [0; 8],
        tgt_oa: [0; 8],
        tgt_mask: [0; 8],
        has_depth: d.has_depth,
        depth_vk_fmt: if d.depth_format_idx == TexFmtDepth24PlusStencil8Idx { st.d24s8_vk_format } else { d.depth_vk_format },
        depth_write: d.depth_write,
        depth_compare: d.depth_compare,
        has_stencil: d.has_stencil,
        stencil_front: d.stencil_front,
        stencil_back: d.stencil_back,
        stencil_read_mask: d.stencil_read_mask,
        stencil_write_mask: d.stencil_write_mask,
        bias_const: d.bias_const,
        bias_slope: d.bias_slope,
        bias_clamp: d.bias_clamp,
        samples: d.samples,
        alpha_to_coverage: d.alpha_to_coverage,
        out_pipe: 0,
        out_res: 0,
    )
    unsafe "extracts device fn address for Blocking" {
        var cfn := st.dev_d.vk_create_graphics_pipelines.Must()
        job.create_fn_addr = raw.Read(raw.PtrCast[fn(*vulkan.VkDeviceT, *vulkan.VkPipelineCacheT, u32, *vulkan.VkGraphicsPipelineCreateInfo, *vulkan.VkAllocationCallbacks, *vulkan.VkPipeline): vulkan.VkResult, uint](raw.Of(&cfn)))
    }
    for vi in 0..d.vertex_layouts.Len() {
        vl := &d.vertex_layouts[vi]
        job.vb_strides[vi] = Cast[u32](vl.stride)
        job.vb_inst[vi] = vl.step_instance
        for ai in 0..vl.attrs.Len() {
            at := &vl.attrs[ai]
            idx := job.va_cnt
            job.va_locs[idx] = Cast[u32](at.location)
            job.va_binds[idx] = Cast[u32](vi)
            job.va_fmts[idx] = at.vk_format
            job.va_offs[idx] = Cast[u32](at.offset)
            job.va_cnt += 1
        }
    }
    for ti in 0..d.targets.Len() {
        ct := &d.targets[ti]
        job.tgt_fmts[ti] = ct.vk_format
        job.tgt_blend[ti] = ct.blend_enable
        job.tgt_sc[ti] = ct.src_color
        job.tgt_dc[ti] = ct.dst_color
        job.tgt_oc[ti] = ct.op_color
        job.tgt_sa[ti] = ct.src_alpha
        job.tgt_da[ti] = ct.dst_alpha
        job.tgt_oa[ti] = ct.op_alpha
        job.tgt_mask[ti] = ct.write_mask
    }

    job = try runtime.Blocking(io, job, RunCompileGfxJob)
    if job.out_res != vulkan.VkSuccess {
        if IsDeviceLostResult(job.out_res) { st.lost = true }
        fail MapVkError(job.out_res, CallCreatePipeline)
    }
    st.live_native += 1
    id := AllocPipeSlot(st, a, VkPipeRec(alive: true, is_compute: false, vk_pipe: job.out_pipe, layout_id: d.layout_id)) else e {
        unsafe "cleanup on OOM" {
            st.dev_d.vk_destroy_pipeline.Must()(HandleFrom[vulkan.VkDeviceT](st.device), HandleFrom[vulkan.VkPipelineT](job.out_pipe), raw.Null[vulkan.VkAllocationCallbacks]())
        }
        if st.live_native > 0 { st.live_native -= 1 }
        fail e
    }
    SetVkObjectName(&*st, vulkan.VkObjectTypePipeline, job.out_pipe, label)
    return id
}

struct CompileCompJob {
    vk_dev: u64
    create_fn_addr: uint
    pl: u64
    cs_mod: u64
    cs_entry: str
    out_pipe: u64
    out_res: i32
}

RunCompileCompJob(j0: CompileCompJob): CompileCompJob {
    var j := j0
    unsafe "j carries plain handles valid while Blocking waits" {
        dev := HandleFrom[vulkan.VkDeviceT](j.vk_dev)
        create_fn := raw.FnFromAddr[vulkan.PfnVkCreateComputePipelines](j.create_fn_addr).Must()
        var stage := mem.Zeroed[vulkan.VkPipelineShaderStageCreateInfo]()
        stage.s_type = vulkan.VkStructureTypePipelineShaderStageCreateInfo
        stage.stage = vulkan.VkShaderStageComputeBit
        stage.module = HandleFrom[vulkan.VkShaderModuleT](j.cs_mod)
        stage.p_name = raw.StrData(j.cs_entry)

        var cpci := mem.Zeroed[vulkan.VkComputePipelineCreateInfo]()
        cpci.s_type = vulkan.VkStructureTypeComputePipelineCreateInfo
        cpci.stage = stage
        cpci.layout = HandleFrom[vulkan.VkPipelineLayoutT](j.pl)

        var pipe: vulkan.VkPipeline = raw.Null[vulkan.VkPipelineT]()
        r := create_fn(dev, raw.Null[vulkan.VkPipelineCacheT](), 1, raw.Of(&cpci), raw.Null[vulkan.VkAllocationCallbacks](), raw.OfMut(mut pipe))
        j.out_res = r
        j.out_pipe = HandleAddr(pipe)
        return j
    }
}

/// Compiles a Vulkan compute pipeline via runtime.Blocking.
export Device.CreateComputePipeline(mut self, io: Io, layout_id: u64, cs_id: u64, cs_entry: str, label: str): Error!u64 {
    a := self.heap.Alloc()
    st := self.state.Mut()
    if st.lost { fail DeviceLost(reason: st.loss_reason) }
    pl := st.pipe_layouts[Cast[int](layout_id)].vk_layout
    cs_mod := st.shaders[Cast[int](cs_id)].vk_module

    var job := CompileCompJob(vk_dev: st.device, create_fn_addr: 0, pl: pl, cs_mod: cs_mod, cs_entry: cs_entry, out_pipe: 0, out_res: 0)
    unsafe "extracts device fn address for Blocking" {
        var cfn := st.dev_d.vk_create_compute_pipelines.Must()
        job.create_fn_addr = raw.Read(raw.PtrCast[fn(*vulkan.VkDeviceT, *vulkan.VkPipelineCacheT, u32, *vulkan.VkComputePipelineCreateInfo, *vulkan.VkAllocationCallbacks, *vulkan.VkPipeline): vulkan.VkResult, uint](raw.Of(&cfn)))
    }
    job = try runtime.Blocking(io, job, RunCompileCompJob)
    if job.out_res != vulkan.VkSuccess {
        if IsDeviceLostResult(job.out_res) { st.lost = true }
        fail MapVkError(job.out_res, CallCreatePipeline)
    }
    st.live_native += 1
    id := AllocPipeSlot(st, a, VkPipeRec(alive: true, is_compute: true, vk_pipe: job.out_pipe, layout_id: layout_id)) else e {
        unsafe "cleanup on OOM" {
            st.dev_d.vk_destroy_pipeline.Must()(HandleFrom[vulkan.VkDeviceT](st.device), HandleFrom[vulkan.VkPipelineT](job.out_pipe), raw.Null[vulkan.VkAllocationCallbacks]())
        }
        if st.live_native > 0 { st.live_native -= 1 }
        fail e
    }
    SetVkObjectName(&*st, vulkan.VkObjectTypePipeline, job.out_pipe, label)
    return id
}

DestroyPipelineRec(st: mut VkDeviceState, id: u64) {
    idx := Cast[int](id)
    if idx <= 0 || idx >= st.pipelines.Len() || !st.pipelines[idx].alive {
        return
    }
    rec := st.pipelines[idx]
    st.pipelines[idx].alive = false
    unsafe "st.device and rec.vk_pipe are live" {
        st.dev_d.vk_destroy_pipeline.Must()(HandleFrom[vulkan.VkDeviceT](st.device), HandleFrom[vulkan.VkPipelineT](rec.vk_pipe), raw.Null[vulkan.VkAllocationCallbacks]())
        if st.live_native > 0 {
            st.live_native -= 1
        }
    }
}

/// Destroys a Vulkan graphics or compute pipeline.
export Device.DestroyPipeline(mut self, id: u64) {
    DestroyPipelineRec(self.state.Mut(), id)
}
