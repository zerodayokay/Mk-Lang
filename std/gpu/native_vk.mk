// Why: keeps Vulkan-specific native creation and enum mapping helpers out of
// stub.mk so each file remains focused and within the standard library size rule.
//
// What: RecordNativeVkError, Vk*Code enum mappers, BuildVkBgEntries,
// VkCreateBindGroupLayout, VkCreatePipelineLayout, VkCreateBindGroup,
// VkCreateRenderPipeline, and VkCreateComputePipeline.
//
// How: resolves validated slots to Vulkan native handles and records device loss
// sticky errors uniformly on vk.Error returns.

import std.rt
import std.gpu.vk

MapVkFeature(f: int): Feature {
    return match f {
        0 { Feature.TextureCompressionBc }
        1 { Feature.DepthClipControl }
        2 { Feature.Float32Filterable }
        3 { Feature.SamplerAnisotropy }
        _ { Feature.DebugLayer }
    }
}

MapVkLossReason(r: int): LossReason {
    return match r {
        0 { LossReason.Hung }
        1 { LossReason.Reset }
        2 { LossReason.Removed }
        3 { LossReason.DriverInternal }
        4 { LossReason.OutOfMemory }
        _ { LossReason.Unknown }
    }
}

MapVkCall(c: int): Call {
    if c == vk.CallCreateInstance { return Call.CreateInstance }
    if c == vk.CallEnumPhysicalDevices { return Call.EnumeratePhysicalDevices }
    if c == vk.CallCreateDevice { return Call.CreateDevice }
    if c == vk.CallAllocateMemory { return Call.AllocateMemory }
    if c == vk.CallCreateImage { return Call.CreateImage }
    if c == vk.CallCreateBuffer { return Call.CreateBuffer }
    if c == vk.CallCreateDescriptorPool { return Call.CreateDescriptorPool }
    if c == vk.CallCreateSemaphore { return Call.CreateSemaphore }
    if c == vk.CallCreatePipeline { return Call.CreatePipeline }
    if c == vk.CallCreateSwapchain { return Call.CreateSwapchain }
    if c == vk.CallAcquireNextImage { return Call.AcquireNextImage }
    if c == vk.CallQueueSubmit { return Call.QueueSubmit }
    if c == vk.CallQueuePresent { return Call.QueuePresent }
    if c == vk.CallWaitSemaphores { return Call.WaitSemaphores }
    if c == vk.CallMap { return Call.Map }
    return Call.ExecuteCommandLists
}

MapVkErr(e: vk.Error): Error {
    return match e {
        vk.Unsupported { Unsupported }
        vk.NoAdapter { NoAdapter }
        vk.Missing(feature: f) { Missing(feature: MapVkFeature(f)) }
        vk.DeviceLost(reason: r) { DeviceLost(reason: MapVkLossReason(r)) }
        vk.OutOfDeviceMemory { OutOfDeviceMemory }
        OutOfMemory { OutOfMemory }
        vk.SurfaceLost { SurfaceLost }
        vk.Driver(code, call) { Driver(code: code, call: MapVkCall(call)) }
        Canceled { Canceled }
    }
}

LimitsFromVk(l: &vk.VkLimits): DeviceLimits {
    return DeviceLimits(
        max_texture_size: l.max_texture_size,
        max_texture_layers: l.max_texture_layers,
        max_bind_groups: l.max_bind_groups,
        max_bindings_per_group: l.max_bindings_per_group,
        max_dynamic_uniforms: l.max_dynamic_uniforms,
        max_immediates: l.max_immediates,
        max_vertex_buffers: l.max_vertex_buffers,
        max_vertex_attributes: l.max_vertex_attributes,
        max_vertex_stride: l.max_vertex_stride,
        max_color_targets: l.max_color_targets,
        uniform_offset_align: l.uniform_offset_align,
        storage_offset_align: l.storage_offset_align,
        max_uniform_binding: l.max_uniform_binding,
        max_storage_binding: l.max_storage_binding,
        max_buffer_size: l.max_buffer_size,
        max_uniforms_per_stage: l.max_uniforms_per_stage,
        max_storage_per_stage: l.max_storage_per_stage,
        max_textures_per_stage: l.max_textures_per_stage,
        max_samplers_per_stage: l.max_samplers_per_stage,
        max_sampler_descriptors: l.max_sampler_descriptors,
        max_workgroup_size_x: l.max_workgroup_size_x,
        max_workgroup_size_y: l.max_workgroup_size_y,
        max_workgroup_size_z: l.max_workgroup_size_z,
        max_workgroup_invocations: l.max_workgroup_invocations,
        max_workgroups_per_dimension: l.max_workgroups_per_dimension,
        max_workgroup_storage: l.max_workgroup_storage,
        sealed: 0,
    )
}

FeaturesFromVk(f: &vk.VkFeatures): Features {
    return Features(
        texture_compression_bc: f.texture_compression_bc,
        depth_clip_control: f.depth_clip_control,
        float32_filterable: f.float32_filterable,
        sampler_anisotropy: f.sampler_anisotropy,
        sealed: 0,
    )
}

CapsFromVk(vc: &[44]vk.VkFormatCaps): [44]FormatCaps {
    var caps: [44]FormatCaps = [NoCaps; 44]
    for i in 0..44 {
        c := vc[i]
        caps[i] = FormatCaps(
            sampled: c.sampled,
            filterable: c.filterable,
            render_target: c.render_target,
            blendable: c.blendable,
            multisample: c.multisample,
            resolve: c.resolve,
            depth_stencil: c.depth_stencil,
            sealed: 0,
        )
    }
    return caps
}

KindFromVk(k: int): AdapterKind {
    if k == vk.KindDiscrete { return AdapterKind.Discrete }
    if k == vk.KindIntegrated { return AdapterKind.Integrated }
    if k == vk.KindSoftware { return AdapterKind.Software }
    return AdapterKind.Other
}

AdapterInfoFromVk(d: &vk.AdapterDesc, backend: Backend): AdapterInfo {
    return AdapterInfo(
        id: AdapterId(backend: backend, luid: d.luid),
        name: d.name,
        vendor: d.vendor,
        device: d.device_id,
        kind: KindFromVk(d.kind),
        limits: LimitsFromVk(&d.limits),
        features: FeaturesFromVk(&d.features),
    )
}

VkOpenOptsFromGpu(o: &DeviceOptions): vk.OpenOptions {
    pwr := match o.power {
        .Default { vk.PowerDefault }
        .HighPerformance { vk.PowerHighPerformance }
        .LowPower { vk.PowerLowPower }
    }
    dbg := match o.debug {
        .Off { vk.DebugOff }
        .Validate { vk.DebugValidate }
        .Strict { vk.DebugStrict }
    }
    var has_luid := false
    var luid: u64 = 0
    match o.adapter {
        some(aid) {
            has_luid = true
            luid = aid.luid
        }
        none {}
    }
    return vk.OpenOptions(
        power: pwr,
        software: o.software,
        has_luid: has_luid,
        luid: luid,
        debug_mode: dbg,
        frames_in_flight: o.frames_in_flight,
        features: vk.VkFeatures(
            texture_compression_bc: o.features.texture_compression_bc,
            depth_clip_control: o.features.depth_clip_control,
            float32_filterable: o.features.float32_filterable,
            sampler_anisotropy: o.features.sampler_anisotropy,
            debug_layer: o.debug == DebugMode.Strict,
        ),
        descriptors: o.descriptors,
    )
}

Device.RecordNativeVkError(mut self, e: vk.Error): Error {
    match e {
        vk.DeviceLost(reason: r) {
            st := self.state.Mut()
            st.lost = true
            st.sticky_err = some(DeviceLost(reason: MapVkLossReason(r)))
        }
        _ {}
    }
    return MapVkErr(e)
}

VkViewDimCode(dim: ViewDimension): int {
    return match dim {
        .D2 { 0 }
        .D2Array { 1 }
        .Cube { 2 }
        .CubeArray { 3 }
    }
}

VkAspectCode(a: Aspect): int {
    return match a {
        .All { 0 }
        .Depth { 1 }
        .Stencil { 2 }
    }
}

VkAddressCode(a: Address): int {
    return match a {
        .Repeat { 0 }
        .MirrorRepeat { 1 }
        .ClampToEdge { 2 }
    }
}

VkCompareCode(c: Compare): int {
    return match c {
        .Never { 0 }
        .Less { 1 }
        .Equal { 2 }
        .LessEqual { 3 }
        .Greater { 4 }
        .NotEqual { 5 }
        .GreaterEqual { 6 }
        .Always { 7 }
    }
}

VkTopologyCode(t: Topology): int {
    return match t {
        .PointList { 0 }
        .LineList { 1 }
        .LineStrip { 2 }
        .TriangleList { 3 }
        .TriangleStrip { 4 }
    }
}

VkCullCode(c: Cull): int {
    return match c {
        .None { 0 }
        .Front { 1 }
        .Back { 2 }
    }
}

VkBlendFactorCode(f: BlendFactor): int {
    return match f {
        .Zero { 0 }
        .One { 1 }
        .Src { 2 }
        .OneMinusSrc { 3 }
        .SrcAlpha { 4 }
        .OneMinusSrcAlpha { 5 }
        .Dst { 6 }
        .OneMinusDst { 7 }
        .DstAlpha { 8 }
        .OneMinusDstAlpha { 9 }
        .Constant { 10 }
        .OneMinusConstant { 11 }
    }
}

VkBlendOpCode(op: BlendOp): int {
    return match op {
        .Add { 0 }
        .Subtract { 1 }
        .ReverseSubtract { 2 }
        .Min { 3 }
        .Max { 4 }
    }
}

VkStencilOpCode(op: StencilOp): int {
    return match op {
        .Keep { 0 }
        .Zero { 1 }
        .Replace { 2 }
        .IncrementClamp { 3 }
        .DecrementClamp { 4 }
        .Invert { 5 }
        .IncrementWrap { 6 }
        .DecrementWrap { 7 }
    }
}

VkStencilFaceFrom(f: StencilFace): vk.StencilFaceDesc {
    return vk.StencilFaceDesc(
        compare: VkCompareCode(f.compare),
        on_fail: VkStencilOpCode(f.on_fail),
        on_depth_fail: VkStencilOpCode(f.on_depth_fail),
        on_pass: VkStencilOpCode(f.on_pass),
    )
}

VkPresentModeCode(m: PresentMode): int {
    return match m {
        .Fifo { vk.ModeFifo }
        .Mailbox { vk.ModeMailbox }
        .Immediate { vk.ModeImmediate }
    }
}

BuildVkBgEntries(st: &DeviceState, entries: []BindEntry, cnt: int): [16]vk.BgBindEntry {
    var vk_es: [16]vk.BgBindEntry = [vk.BgBindEntry(binding: 0, kind: 0, buf_id: 0, offset: 0, size: 0, view_id: 0, sampler_id: 0); 16]
    for i in 0..cnt {
        be := &entries[i]
        match be.bound {
            .Buffer(b, offset, size) {
                is_sto := !st.buffers.Get(b.slot).usage.uniform && !st.buffers.Get(b.slot).usage.uniform_dynamic
                vk_es[i] = vk.BgBindEntry(
                    binding: be.binding,
                    kind: if is_sto { 1 } else { 0 },
                    buf_id: st.buffers.Get(b.slot).native_handle,
                    offset: offset,
                    size: size,
                    view_id: 0,
                    sampler_id: 0,
                )
            }
            .View(v: vw) {
                vk_es[i] = vk.BgBindEntry(
                    binding: be.binding,
                    kind: 2,
                    buf_id: 0,
                    offset: 0,
                    size: 0,
                    view_id: st.views.Get(vw.slot).native_handle,
                    sampler_id: 0,
                )
            }
            .Sampler(s: smp) {
                vk_es[i] = vk.BgBindEntry(
                    binding: be.binding,
                    kind: 3,
                    buf_id: 0,
                    offset: 0,
                    size: 0,
                    view_id: 0,
                    sampler_id: st.samplers.Get(smp.slot).native_handle,
                )
            }
        }
    }
    return vk_es
}

Device.VkCreateBindGroupLayout(mut self, entries: []LayoutEntry, label: str): Error!u64 {
    var vk_es: [16]vk.BgLayoutEntry = [vk.BgLayoutEntry(binding: 0, vis_vs: false, vis_fs: false, vis_cs: false, kind: 0, dynamic: false, read_only: false); 16]
    cnt := if entries.Len() > 16 { 16 } else { entries.Len() }
    for i in 0..cnt {
        e := &entries[i]
        var k := 0
        var dyn := false
        var ro := false
        match e.kind {
            .Uniform(dynamic, min_size: _) {
                k = 0
                dyn = dynamic
            }
            .Storage(read_only, min_size: _, stride: _) {
                k = 1
                ro = read_only
            }
            .Texture(sample: _, dimension: _, multisampled: _) { k = 2 }
            .Sampler(comparison: _) { k = 3 }
        }
        vk_es[i] = vk.BgLayoutEntry(
            binding: e.binding,
            vis_vs: e.visibility.vertex,
            vis_fs: e.visibility.fragment,
            vis_cs: e.visibility.compute,
            kind: k,
            dynamic: dyn,
            read_only: ro,
        )
    }
    match mut self.state.Mut().native {
        .Vk(v) { return v.CreateBindGroupLayout(vk_es[0..cnt], label) else e { fail self.RecordNativeVkError(e) } }
        _ { fail Unsupported }
    }
}

Device.VkCreatePipelineLayout(mut self, groups: []&BindGroupLayout, immediates: int, label: str): Error!u64 {
    var bg_nhs: [16]u64 = [0; 16]
    cnt := if groups.Len() > 16 { 16 } else { groups.Len() }
    st_ref := self.state.Ref()
    for i in 0..cnt {
        bg_nhs[i] = st_ref.bg_layouts.Get(groups[i].slot).native_handle
    }
    match mut self.state.Mut().native {
        .Vk(v) { return v.CreatePipelineLayout(bg_nhs[0..cnt], immediates, label) else e { fail self.RecordNativeVkError(e) } }
        _ { fail Unsupported }
    }
}

Device.VkCreateBindGroup(mut self, layout_slot: u32, entries: []BindEntry, label: str): Error!u64 {
    cnt := if entries.Len() > 16 { 16 } else { entries.Len() }
    st_ref := self.state.Ref()
    layout_nh := st_ref.bg_layouts.Get(layout_slot).native_handle
    vk_es := BuildVkBgEntries(st_ref, entries, cnt)
    match mut self.state.Mut().native {
        .Vk(v) { return v.CreateBindGroup(layout_nh, vk_es[0..cnt], label) else e { fail self.RecordNativeVkError(e) } }
        _ { fail Unsupported }
    }
}

Device.VkCreateRenderPipeline(
    mut self,
    io: Io,
    layout_slot: u32,
    vs_slot: u32,
    fs_slot: u32,
    d: &RenderPipelineDesc,
): Error!u64 {
    var hs := ResolveRenderPipeHandles(self.state.Ref(), layout_slot, vs_slot, fs_slot)
    vs_entry := rt.StrFrom(hs.vs_ebuf[0..hs.vs_elen]) else { "main" }
    fs_entry := rt.StrFrom(hs.fs_ebuf[0..hs.fs_elen]) else { "" }

    var attrs_flat: [32]vk.VertexAttrDesc = [vk.VertexAttrDesc(location: 0, vk_format: 0, offset: 0); 32]
    var ac := 0
    var vl_starts: [16]int = [0; 16]
    var vl_ends: [16]int = [0; 16]
    var vl_strides: [16]int = [0; 16]
    var vl_inst: [16]bool = [false; 16]
    vbc := if d.vertex_layouts.Len() > 16 { 16 } else { d.vertex_layouts.Len() }
    for bi in 0..vbc {
        vl := &d.vertex_layouts[bi]
        start := ac
        for ai in 0..vl.attributes.Len() {
            if ac < 32 {
                at := &vl.attributes[ai]
                attrs_flat[ac] = vk.VertexAttrDesc(location: at.location, vk_format: VertexInfoOf(at.format).vk_format, offset: at.offset)
                ac += 1
            }
        }
        vl_starts[bi] = start
        vl_ends[bi] = ac
        vl_strides[bi] = vl.stride
        vl_inst[bi] = vl.step == StepMode.Instance
    }
    var vls: [16]vk.VertexLayoutDesc = [vk.VertexLayoutDesc(stride: 0, step_instance: false, attrs: attrs_flat[0..0]); 16]
    for bi in 0..vbc {
        vls[bi] = vk.VertexLayoutDesc(stride: vl_strides[bi], step_instance: vl_inst[bi], attrs: attrs_flat[vl_starts[bi]..vl_ends[bi]])
    }
    var tgts: [8]vk.ColorTargetDesc = [vk.ColorTargetDesc(vk_format: 0, format_idx: 0, blend_enable: false, src_color: 1, dst_color: 0, op_color: 0, src_alpha: 1, dst_alpha: 0, op_alpha: 0, write_mask: 0xF); 8]
    tcnt := if d.targets.Len() > 8 { 8 } else { d.targets.Len() }
    for ti in 0..tcnt {
        ct := &d.targets[ti]
        var wm: u8 = 0
        if ct.write.r { wm |= 1 }
        if ct.write.g { wm |= 2 }
        if ct.write.b { wm |= 4 }
        if ct.write.a { wm |= 8 }
        var ben := false
        var sc := 1
        var dc := 0
        var oc := 0
        var sa := 1
        var da := 0
        var oa := 0
        match ct.blend {
            some(bl) {
                ben = true
                sc = VkBlendFactorCode(bl.color.src)
                dc = VkBlendFactorCode(bl.color.dst)
                oc = VkBlendOpCode(bl.color.op)
                sa = VkBlendFactorCode(bl.alpha.src)
                da = VkBlendFactorCode(bl.alpha.dst)
                oa = VkBlendOpCode(bl.alpha.op)
            }
            none {}
        }
        tgts[ti] = vk.ColorTargetDesc(
            vk_format: RowOf(ct.format).vk_format,
            format_idx: FormatIndex(ct.format),
            blend_enable: ben,
            src_color: sc,
            dst_color: dc,
            op_color: oc,
            src_alpha: sa,
            dst_alpha: da,
            op_alpha: oa,
            write_mask: wm,
        )
    }
    var has_d := false
    var dfmt_vk: i32 = 0
    var dfmt_idx := 0
    var dwrite := false
    var dcmp := 7
    var has_st := false
    def_face := vk.StencilFaceDesc(compare: 7, on_fail: 0, on_depth_fail: 0, on_pass: 0)
    var sfront := def_face
    var sback := def_face
    var srmask: u8 = 0
    var swmask: u8 = 0
    var bconst: i32 = 0
    var bslope: f32 = 0.0
    var bclamp: f32 = 0.0
    match d.depth {
        some(ds) {
            has_d = true
            dfmt_vk = RowOf(ds.format).vk_format
            dfmt_idx = FormatIndex(ds.format)
            dwrite = ds.write
            dcmp = VkCompareCode(ds.compare)
            bconst = ds.bias.constant
            bslope = ds.bias.slope
            bclamp = ds.bias.clamp
            match ds.stencil {
                some(st) {
                    has_st = true
                    sfront = VkStencilFaceFrom(st.front)
                    sback = VkStencilFaceFrom(st.back)
                    srmask = st.read_mask
                    swmask = st.write_mask
                }
                none {}
            }
        }
        none {}
    }
    vk_d := vk.RenderPipelineDesc(
        layout_id: hs.pl_nh,
        vs_id: hs.vs_nh,
        has_fs: hs.fs_nh != 0,
        fs_id: hs.fs_nh,
        vertex_layouts: vls[0..vbc],
        topology: VkTopologyCode(d.topology),
        has_strip_idx: d.strip_index_format.IsSome(),
        cull: VkCullCode(d.cull),
        front_cw: d.front == FrontFace.Cw,
        targets: tgts[0..tcnt],
        has_depth: has_d,
        depth_vk_format: dfmt_vk,
        depth_format_idx: dfmt_idx,
        depth_write: dwrite,
        depth_compare: dcmp,
        has_stencil: has_st,
        stencil_front: sfront,
        stencil_back: sback,
        stencil_read_mask: srmask,
        stencil_write_mask: swmask,
        bias_const: bconst,
        bias_slope: bslope,
        bias_clamp: bclamp,
        samples: d.samples,
        alpha_to_coverage: d.alpha_to_coverage,
    )
    match mut self.state.Mut().native {
        .Vk(v) { return v.CreateRenderPipeline(io, vk_d, vs_entry, fs_entry, d.label) else e { fail self.RecordNativeVkError(e) } }
        _ { fail Unsupported }
    }
}

Device.VkCreateComputePipeline(mut self, io: Io, layout_slot: u32, cs_slot: u32, label: str): Error!u64 {
    var hs := ResolveComputePipeHandles(self.state.Ref(), layout_slot, cs_slot)
    cs_entry := rt.StrFrom(hs.cs_ebuf[0..hs.cs_elen]) else { "main" }
    match mut self.state.Mut().native {
        .Vk(v) { return v.CreateComputePipeline(io, hs.pl_nh, hs.cs_nh, cs_entry, label) else e { fail self.RecordNativeVkError(e) } }
        _ { fail Unsupported }
    }
}
