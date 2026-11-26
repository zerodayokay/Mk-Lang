// Why: keeps D3D12-specific native creation and enum mapping helpers out of
// stub.mk so each file remains focused and within the standard library size rule.
//
// What: DxCreatePipelineLayout, DxCreateBindGroup, DxCreateRenderPipeline,
// DxCreateComputePipeline, and DXGI/D3D12 enum mapping helpers.
//
// How: converts validated portable descriptors into std.gpu.dx spec structs and
// syncs info-queue diagnostics on return or error.

import std.gpu.dx

MapGpuAddr(addr: Address): u8 {
    return match addr {
        .Repeat { 0 }
        .MirrorRepeat { 1 }
        .ClampToEdge { 2 }
    }
}

MapGpuCmp(c: Compare): u8 {
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

MapBlendFactorU(f: BlendFactor): u8 {
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

MapBlendOpU(op: BlendOp): u8 {
    return match op {
        .Add { 0 }
        .Subtract { 1 }
        .ReverseSubtract { 2 }
        .Min { 3 }
        .Max { 4 }
    }
}

MapStencilOpU(op: StencilOp): u8 {
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

MapStencilFaceSpec(f: &StencilFace): dx.StencilFaceSpec {
    return dx.StencilFaceSpec(
        compare: MapGpuCmp(f.compare),
        on_fail: MapStencilOpU(f.on_fail),
        on_depth_fail: MapStencilOpU(f.on_depth_fail),
        on_pass: MapStencilOpU(f.on_pass),
    )
}

Device.DxCreatePipelineLayout(mut self, groups: []&BindGroupLayout, immediates: int, label: str): Error!u64 {
    a := self.heap.Alloc()
    var ar := try Arena.New(self.heap)
    defer ar.Free()
    st := self.state.Mut()
    var grp_specs := List[[]dx.LayoutEntrySpec].New(&ar)
    for gi in 0..groups.Len() {
        bgl := st.bg_layouts.Get(groups[gi].slot)
        var es := try ar.Slice[dx.LayoutEntrySpec](bgl.entries.Len())
        for ei in 0..bgl.entries.Len() {
            le := &bgl.entries[ei]
            (kind: kind_u, dyn: dyn_b, ro: ro_b) := match le.kind {
                .Uniform(dynamic, min_size: _) { (kind: u8(0), dyn: dynamic, ro: true) }
                .Storage(read_only, min_size: _, stride: _) { (kind: u8(1), dyn: false, ro: read_only) }
                .Texture(sample: _, dimension: _, multisampled: _) { (kind: u8(2), dyn: false, ro: true) }
                .Sampler(comparison: _) { (kind: u8(3), dyn: false, ro: true) }
            }
            es[ei] = dx.LayoutEntrySpec(
                binding: le.binding,
                vis_vertex: le.visibility.vertex,
                vis_fragment: le.visibility.fragment,
                vis_compute: le.visibility.compute,
                kind: kind_u,
                dynamic: dyn_b,
                read_only: ro_b,
            )
        }
        try grp_specs.Push(es[..])
    }
    spec := dx.PipeLayoutSpec(groups: grp_specs.Items(), immediates: immediates, label: label)
    match mut st.native {
        .Dx(d) {
            nh := d.CreateRootSignature(&spec) else e {
                SyncDxMessages(a, st)
                ge := MapDxError(e)
                RecordDxFailure(st, ge)
                fail ge
            }
            SyncDxMessages(a, st)
            return nh
        }
        _ { fail Unsupported }
    }
}

Device.DxCreateBindGroup(mut self, layout_slot: u32, entries: []BindEntry): Error!u64 {
    a := self.heap.Alloc()
    var ar := try Arena.New(self.heap)
    defer ar.Free()
    st := self.state.Mut()
    bgl := st.bg_layouts.Get(layout_slot)
    var specs := try ar.Slice[dx.BindEntrySpec](bgl.entries.Len())
    for li in 0..bgl.entries.Len() {
        le := &bgl.entries[li]
        var found_idx := -1
        for ei in 0..entries.Len() {
            if entries[ei].binding == le.binding {
                found_idx = ei
                break
            }
        }
        be := &entries[found_idx]
        match le.kind {
            .Uniform(dynamic, min_size: _) {
                match be.bound {
                    .Buffer(b, offset, size) {
                        bnh := st.buffers.Get(b.slot).native_handle
                        specs[li] = dx.BindEntrySpec(
                            binding: le.binding,
                            kind: if dynamic { u8(5) } else { u8(0) },
                            res_nh: bnh,
                            offset: offset,
                            size: size,
                            stride: 0,
                        )
                    }
                    _ {}
                }
            }
            .Storage(read_only, min_size: _, stride) {
                match be.bound {
                    .Buffer(b, offset, size) {
                        bnh := st.buffers.Get(b.slot).native_handle
                        specs[li] = dx.BindEntrySpec(
                            binding: le.binding,
                            kind: if read_only { u8(1) } else { u8(2) },
                            res_nh: bnh,
                            offset: offset,
                            size: size,
                            stride: stride,
                        )
                    }
                    _ {}
                }
            }
            .Texture(sample: _, dimension: _, multisampled: _) {
                match be.bound {
                    .View(v) {
                        vnh := st.views.Get(v.slot).native_handle
                        specs[li] = dx.BindEntrySpec(
                            binding: le.binding,
                            kind: 3,
                            res_nh: vnh,
                            offset: 0,
                            size: 0,
                            stride: 0,
                        )
                    }
                    _ {}
                }
            }
            .Sampler(comparison: _) {
                match be.bound {
                    .Sampler(s) {
                        snh := st.samplers.Get(s.slot).native_handle
                        specs[li] = dx.BindEntrySpec(
                            binding: le.binding,
                            kind: 4,
                            res_nh: snh,
                            offset: 0,
                            size: 0,
                            stride: 0,
                        )
                    }
                    _ {}
                }
            }
        }
    }
    match mut st.native {
        .Dx(d) {
            nh := d.CreateBindGroup(specs[..]) else e {
                SyncDxMessages(a, st)
                ge := MapDxError(e)
                RecordDxFailure(st, ge)
                fail ge
            }
            SyncDxMessages(a, st)
            return nh
        }
        _ { fail Unsupported }
    }
}

Device.DxCreateRenderPipeline(
    mut self,
    io: Io,
    layout_slot: u32,
    vs_slot: u32,
    fs_slot: u32,
    d: &RenderPipelineDesc,
): Error!u64 {
    a := self.heap.Alloc()
    var ar := try Arena.New(self.heap)
    defer ar.Free()
    st := self.state.Mut()
    rs_nh := st.pipe_layouts.Get(layout_slot).native_handle
    vs_nh := st.shaders.Get(vs_slot).native_handle
    has_fs := fs_slot != 0
    fs_nh := if has_fs { st.shaders.Get(fs_slot).native_handle } else { u64(0) }

    var attr_cnt := 0
    for vi in 0..d.vertex_layouts.Len() {
        attr_cnt += d.vertex_layouts[vi].attributes.Len()
    }
    var attrs := try ar.Slice[dx.VertexAttrSpec](attr_cnt)
    var ai := 0
    for vi in 0..d.vertex_layouts.Len() {
        vl := &d.vertex_layouts[vi]
        per_inst := vl.step == StepMode.Instance
        for aj in 0..vl.attributes.Len() {
            at := &vl.attributes[aj]
            attrs[ai] = dx.VertexAttrSpec(
                location: at.location,
                dxgi_format: VertexInfoOf(at.format).dxgi_format,
                offset: at.offset,
                slot: vi,
                per_instance: per_inst,
            )
            ai += 1
        }
    }
    var tspecs := try ar.Slice[dx.ColorTargetSpec](d.targets.Len())
    for ti in 0..d.targets.Len() {
        ct := &d.targets[ti]
        (en: b_en, col: c_sp, alp: a_sp) := match ct.blend {
            some(b) {
                (
                    en: true,
                    col: dx.BlendCompSpec(src: MapBlendFactorU(b.color.src), dst: MapBlendFactorU(b.color.dst), op: MapBlendOpU(b.color.op)),
                    alp: dx.BlendCompSpec(src: MapBlendFactorU(b.alpha.src), dst: MapBlendFactorU(b.alpha.dst), op: MapBlendOpU(b.alpha.op)),
                )
            }
            none {
                z := dx.BlendCompSpec(src: 1, dst: 0, op: 0)
                (en: false, col: z, alp: z)
            }
        }
        var wm: u8 = 0
        if ct.write.r { wm |= 1 }
        if ct.write.g { wm |= 2 }
        if ct.write.b { wm |= 4 }
        if ct.write.a { wm |= 8 }
        tspecs[ti] = dx.ColorTargetSpec(
            dxgi_format: RowOf(ct.format).dxgi_rtv_dsv,
            blend_enable: b_en,
            color: c_sp,
            alpha: a_sp,
            write_mask: wm,
        )
    }
    topo_u := match d.topology {
        .PointList { u8(0) }
        .LineList { u8(1) }
        .LineStrip { u8(2) }
        .TriangleList { u8(3) }
        .TriangleStrip { u8(4) }
    }
    (has: has_sc, u32_: sc_u32) := match d.strip_index_format {
        some(f) { (has: true, u32_: f == IndexFormat.Uint32) }
        none { (has: false, u32_: false) }
    }
    cull_u := match d.cull {
        .None { u8(0) }
        .Front { u8(1) }
        .Back { u8(2) }
    }
    var has_d := false
    var d_dxgi: u32 = 0
    var d_write := false
    var d_cmp: u8 = 7
    var s_en := false
    var s_rm: u8 = 0
    var s_wm: u8 = 0
    var s_front := dx.StencilFaceSpec(compare: 7, on_fail: 0, on_depth_fail: 0, on_pass: 0)
    var s_back := s_front
    var b_const: i32 = 0
    var b_slope: f32 = 0.0
    var b_clamp: f32 = 0.0
    match d.depth {
        some(ds) {
            has_d = true
            d_dxgi = RowOf(ds.format).dxgi_rtv_dsv
            d_write = ds.write
            d_cmp = MapGpuCmp(ds.compare)
            b_const = ds.bias.constant
            b_slope = ds.bias.slope
            b_clamp = ds.bias.clamp
            match ds.stencil {
                some(ss) {
                    s_en = true
                    s_rm = ss.read_mask
                    s_wm = ss.write_mask
                    s_front = MapStencilFaceSpec(&ss.front)
                    s_back = MapStencilFaceSpec(&ss.back)
                }
                none {}
            }
        }
        none {}
    }
    spec := dx.RenderPipeSpec(
        root_sig_nh: rs_nh,
        vs_nh: vs_nh,
        has_fs: has_fs,
        fs_nh: fs_nh,
        attrs: attrs[..],
        topology: topo_u,
        has_strip_cut: has_sc,
        strip_cut_u32: sc_u32,
        cull: cull_u,
        front_ccw: d.front == FrontFace.Ccw,
        depth_clip_disable: st.features.depth_clip_control && b_clamp != 0.0,
        targets: tspecs[..],
        has_depth: has_d,
        depth_dxgi: d_dxgi,
        depth_write: d_write,
        depth_compare: d_cmp,
        stencil_enable: s_en,
        stencil_read_mask: s_rm,
        stencil_write_mask: s_wm,
        stencil_front: s_front,
        stencil_back: s_back,
        bias_constant: b_const,
        bias_slope: b_slope,
        bias_clamp: b_clamp,
        samples: d.samples,
        alpha_to_coverage: d.alpha_to_coverage,
        label: d.label,
    )
    match mut st.native {
        .Dx(d: dx_dev) {
            nh := dx_dev.CreateRenderPipeline(io, &spec) else e {
                SyncDxMessages(a, st)
                ge := MapDxError(e)
                RecordDxFailure(st, ge)
                fail ge
            }
            SyncDxMessages(a, st)
            return nh
        }
        _ { fail Unsupported }
    }
}

Device.DxCreateComputePipeline(mut self, io: Io, layout_slot: u32, cs_slot: u32, label: str): Error!u64 {
    a := self.heap.Alloc()
    st := self.state.Mut()
    rs_nh := st.pipe_layouts.Get(layout_slot).native_handle
    cs_nh := st.shaders.Get(cs_slot).native_handle
    match mut st.native {
        .Dx(d: dx_dev) {
            nh := dx_dev.CreateComputePipeline(io, rs_nh, cs_nh, label) else e {
                SyncDxMessages(a, st)
                ge := MapDxError(e)
                RecordDxFailure(st, ge)
                fail ge
            }
            SyncDxMessages(a, st)
            return nh
        }
        _ { fail Unsupported }
    }
}
