// Why: pure tests must verify every validation rule, command stream encoding,
// handle lifecycle, deferred release, and leak accounting without requiring
// native GPU hardware.
//
// What: StubDevice, StubConfig, OpenStub, OpenStubWith, and Native dispatch
// helpers for package-internal testing.
//
// How: tracks live native handles, submitted opcodes, backing buffer bytes,
// and optional fault/outdated injection on the device's heap.

import std.gpu.dx

struct StubConfig {
    limits: DeviceLimits
    features: Features
    caps_override_idx: int
    caps_override: FormatCaps
    depth_bias_clamp_ok: bool
    draw_params_ok: bool
}

DefaultStubConfig(): StubConfig {
    return StubConfig(
        limits: DefaultPortableLimits,
        features: NoFeatures,
        caps_override_idx: -1,
        caps_override: NoCaps,
        depth_bias_clamp_ok: true,
        draw_params_ok: true,
    )
}

struct StubBufMem {
    alive: bool
    bytes: HeapList[u8]
}

struct StubDevice {
    live_native: int
    next_id: u64
    calls: HeapList[u8]
    submitted_ops: HeapList[u8]
    buf_mems: HeapList[StubBufMem]
    fail_next_submit: ?Error
    acquire_outdated: bool
    acquire_hidden: bool
    swap_w: int
    swap_h: int
    swap_fmt: TextureFormat
    swap_mode: PresentMode
    completed_serial: u64
}

StubDevice.New(a: &Alloc): StubDevice {
    return StubDevice(
        live_native: 0,
        next_id: 1,
        calls: HeapList[u8].New(a),
        submitted_ops: HeapList[u8].New(a),
        buf_mems: HeapList[StubBufMem].New(a),
        fail_next_submit: none,
        acquire_outdated: false,
        acquire_hidden: false,
        swap_w: 800,
        swap_h: 600,
        swap_fmt: TextureFormat.Bgra8UnormSrgb,
        swap_mode: PresentMode.Fifo,
        completed_serial: 0,
    )
}

StubDevice.AllocHandle(mut self): u64 {
    id := self.next_id
    self.next_id += 1
    self.live_native += 1
    return id
}

StubDevice.FreeHandle(mut self, id: u64) {
    if id != 0 && self.live_native > 0 {
        self.live_native -= 1
    }
}

StubDevice.CreateBuffer(mut self, a: &Alloc into self, size: int): OutOfMemory!u64 {
    id := self.AllocHandle()
    var mem := HeapList[u8].New(a)
    for _ in 0..size {
        mem.Push(a, 0) else e {
            mem.Free(a)
            self.FreeHandle(id)
            fail e
        }
    }
    sbm := StubBufMem(alive: true, bytes: mem)
    self.buf_mems.Push(a, sbm) else e {
        StubBufMem(alive: _, bytes) := sbm
        bytes.Free(a)
        self.FreeHandle(id)
        fail e
    }
    return Cast[u64](self.buf_mems.Len())
}

StubDevice.DestroyBuffer(mut self, a: &Alloc, id: u64) {
    if id == 0 {
        return
    }
    idx := Cast[int](id - 1)
    if idx >= 0 && idx < self.buf_mems.Len() && self.buf_mems[idx].alive {
        self.buf_mems[idx].alive = false
        self.buf_mems[idx].bytes.Clear()
        self.FreeHandle(id)
    }
    _ = a
}

StubDevice.SubmitStream(mut self, st: &CommandStream, serial: u64): Error!void {
    match self.fail_next_submit {
        some(e) {
            self.fail_next_submit = none
            fail e
        }
        none {}
    }
    _ = serial
    _ = st
}

StubDevice.RecordStreamOps(mut self, a: &Alloc, st: &CommandStream, pool: &ChunkPool) {
    nc := st.chunk_ids.Len()
    for ci in 0..nc {
        ch := pool.chunks[st.chunk_ids[ci]].Ref()
        end_w := st.ChunkEndWord(ci)
        var wi := 0
        for wi < end_w {
            w0 := ch.words[wi]
            op := UnpackOp(w0)
            words := UnpackWords(w0)
            self.submitted_ops.Push(a, op) else {}
            if words <= 0 {
                break
            }
            wi += words
        }
    }
}

StubDevice.WaitSerial(mut self, serial: u64): Error!void {
    match self.fail_next_submit {
        some(e) {
            self.fail_next_submit = none
            fail e
        }
        none {}
    }
    if serial > self.completed_serial {
        self.completed_serial = serial
    }
}

StubDevice.CloseAll(self, a: &Alloc): int {
    StubDevice(live_native, next_id: _, calls, submitted_ops, buf_mems: buf_mems0, fail_next_submit: _, acquire_outdated: _, acquire_hidden: _, swap_w: _, swap_h: _, swap_fmt: _, swap_mode: _, completed_serial: _) := self
    calls.Free(a)
    submitted_ops.Free(a)
    var buf_mems := buf_mems0
    for buf_mems.Len() > 0 {
        StubBufMem(alive: _, bytes) := buf_mems.Pop().Must()
        bytes.Free(a)
    }
    buf_mems.FreeEmpty(a)
    return live_native
}

OpenStub(heap: &Heap, o: DeviceOptions): Error!Device from heap {
    return try OpenStubWith(heap, o, DefaultStubConfig())
}

OpenStubWith(heap: &Heap, o: DeviceOptions, cfg: StubConfig): Error!Device from heap {
    try ValidateDeviceOptions(&o)
    if o.features.texture_compression_bc && !cfg.features.texture_compression_bc {
        // Stub allows requested features unless overridden; test overrides can deny them.
    }
    a := heap.Alloc()
    var caps: [44]FormatCaps = [NoCaps; 44]
    for i in 0..TextureFormatCount {
        f := FormatByIndex(i)
        caps[i] = RowOf(f).guaranteed
        if o.features.float32_filterable && (i == FormatIndex(TextureFormat.R32Float) || i == FormatIndex(TextureFormat.Rg32Float) || i == FormatIndex(TextureFormat.Rgba32Float)) {
            caps[i].filterable = true
        }
    }
    if cfg.caps_override_idx >= 0 && cfg.caps_override_idx < TextureFormatCount {
        caps[cfg.caps_override_idx] = cfg.caps_override
    }
    info := AdapterInfo(
        id: AdapterId(backend: Backend.Auto, luid: 1),
        name: "Stub GPU",
        vendor: 0x1000,
        device: 0x0001,
        kind: AdapterKind.Software,
        limits: cfg.limits,
        features: o.features,
    )
    st := DeviceState(
        info: info,
        caps: caps,
        limits: cfg.limits,
        features: o.features,
        backend: Backend.Auto,
        debug_mode: 0,
        native: Native.Stub(s: StubDevice.New(a)),
        adapter_name_bytes: HeapList[u8].New(a),
        sticky_err: none,
        lost: false,
        depth_bias_clamp_ok: cfg.depth_bias_clamp_ok,
        draw_params_ok: cfg.draw_params_ok,
        serials: SerialTracker.New(),
        deferred: DeferredQueue.New(a),
        chunk_pool: ChunkPool.New(a),
        stream: CommandStream.New(a),
        pre_submit: CommandStream.New(a),
        encoder_active: false,
        pass_shadow: PassShadow.New(a),
        messages: HeapList[StoredMessage].New(a),
        msg_bytes: HeapList[u8].New(a),
        err_count: 0,
        warn_count: 0,
        planted_bug: PlantedBug.None,
        buffers: SlotTable[BufferEntry].New(a),
        textures: SlotTable[TextureEntry].New(a),
        views: SlotTable[ViewEntry].New(a),
        samplers: SlotTable[SamplerEntry].New(a),
        bg_layouts: SlotTable[BindGroupLayoutEntry].New(a),
        pipe_layouts: SlotTable[PipelineLayoutEntry].New(a),
        bind_groups: SlotTable[BindGroupEntry].New(a),
        shaders: SlotTable[ShaderEntry].New(a),
        render_pipes: SlotTable[RenderPipelineEntry].New(a),
        compute_pipes: SlotTable[ComputePipelineEntry].New(a),
        swapchains: SlotTable[SwapchainEntry].New(a),
        dx_chunks: HeapList[dx.ChunkSpan].New(a),
        dx_barriers: HeapList[dx.BarrierSpec].New(a),
        dx_passes: HeapList[dx.PassReplaySpec].New(a),
    )
    box := Box[DeviceState].New(a, st) else e {
        _ = st.FreeAll(a)
        fail e
    }
    return Device(heap: heap, serial: 1, state: box, cell: Cell[u8].New(0))
}

Native.CloseAll(self, a: &Alloc): int {
    match self {
        .Dx(d) {
            d.Close()
            return 0
        }
        .Vk(v) {
            v.Close()
            return 0
        }
        .Stub(s) {
            return s.CloseAll(a)
        }
    }
}

Device.NativeCreateBuffer(mut self, size: int, usage: BufferUsage, label: str): Error!u64 {
    a := self.heap.Alloc()
    st := self.state.Mut()
    match mut st.native {
        .Stub(s) { return try s.CreateBuffer(a, size) }
        .Dx(d) {
            spec := dx.BufferSpec(
                size: size,
                uniform_dynamic: usage.uniform_dynamic,
                uniform: usage.uniform,
                storage: usage.storage,
                map_read: usage.map_read,
                map_write: usage.map_write,
                label: label,
            )
            nh := d.CreateBuffer(&spec) else e {
                SyncDxMessages(a, st)
                ge := MapDxError(e)
                RecordDxFailure(st, ge)
                fail ge
            }
            SyncDxMessages(a, st)
            return nh
        }
        .Vk(v: _) { fail Unsupported }
    }
}

Native.DestroyBuffer(mut self, a: &Alloc, id: u64) {
    match mut self {
        .Stub(s) { s.DestroyBuffer(a, id) }
        .Dx(d) { d.DestroyBuffer(id) }
        .Vk(v: _) {}
    }
}

Device.NativeCreateShader(mut self, code: ShaderCode, stage: Stage, entry: str): Error!u64 {
    _ = stage
    _ = entry
    st := self.state.Mut()
    match mut st.native {
        .Stub(s) { return s.AllocHandle() }
        .Dx(d) {
            return d.CreateShader(code.dxil) else e { fail MapDxError(e) }
        }
        .Vk(v: _) { fail Unsupported }
    }
}

Native.DestroyShader(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d) { d.DestroyShader(id) }
        .Vk(v: _) {}
    }
}

Device.NativeCreateTexture(mut self, td: &TextureDesc): Error!u64 {
    a := self.heap.Alloc()
    st := self.state.Mut()
    match mut st.native {
        .Stub(s) { return s.AllocHandle() }
        .Dx(d) {
            row := RowOf(td.format)
            spec := dx.TextureSpec(
                dxgi_res: row.dxgi_res,
                dxgi_rtv_dsv: row.dxgi_rtv_dsv,
                width: td.width,
                height: td.height,
                layers: td.layers,
                mips: td.mips,
                samples: td.samples,
                sampled: td.usage.sampled,
                render_target: td.usage.render_target,
                is_depth: IsDepthStencilFormat(td.format),
                copy_src: td.usage.copy_src,
                copy_dst: td.usage.copy_dst,
                block_bytes: int(row.block_bytes),
                block_w: int(row.block_w),
                block_h: int(row.block_h),
                planes: PlaneCount(td.format),
                label: td.label,
            )
            nh := d.CreateTexture(&spec) else e {
                SyncDxMessages(a, st)
                ge := MapDxError(e)
                RecordDxFailure(st, ge)
                fail ge
            }
            SyncDxMessages(a, st)
            return nh
        }
        .Vk(v: _) { fail Unsupported }
    }
}

Native.DestroyTexture(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d) { d.DestroyTexture(id) }
        .Vk(v: _) {}
    }
}

Device.NativeCreateView(mut self, ve: &ViewEntry): Error!u64 {
    a := self.heap.Alloc()
    st := self.state.Mut()
    tex_nh := if ve.tex_slot != 0 { st.textures.Get(ve.tex_slot).native_handle } else { u64(0) }
    match mut st.native {
        .Stub(s) { return s.AllocHandle() }
        .Dx(d) {
            row := RowOf(ve.format)
            dim_u := match ve.dim {
                .D2 { u8(0) }
                .D2Array { u8(1) }
                .Cube { u8(2) }
                .CubeArray { u8(3) }
            }
            asp_u := match ve.aspect {
                .All { u8(0) }
                .Depth { u8(1) }
                .Stencil { u8(2) }
            }
            spec := dx.ViewSpec(
                tex_nh: tex_nh,
                dxgi_rtv_dsv: row.dxgi_rtv_dsv,
                dxgi_srv: row.dxgi_srv,
                dxgi_stencil_srv: row.dxgi_stencil_srv,
                dim: dim_u,
                aspect: asp_u,
                first_mip: ve.first_mip,
                mip_count: ve.mip_count,
                first_layer: ve.first_layer,
                layer_count: ve.layer_count,
                samples: ve.samples,
                render_target: ve.usage.render_target,
                sampled: ve.usage.sampled,
                is_depth: IsDepthStencilFormat(ve.format),
                has_stencil: (row.aspects & AspectBitStencil) != 0,
            )
            nh := d.CreateView(&spec) else e {
                SyncDxMessages(a, st)
                ge := MapDxError(e)
                RecordDxFailure(st, ge)
                fail ge
            }
            SyncDxMessages(a, st)
            return nh
        }
        .Vk(v: _) { fail Unsupported }
    }
}

Native.DestroyView(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d) { d.DestroyView(id) }
        .Vk(v: _) {}
    }
}

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

Device.NativeCreateSampler(mut self, sd: &SamplerDesc): Error!u64 {
    st := self.state.Mut()
    match mut st.native {
        .Stub(s) { return s.AllocHandle() }
        .Dx(d) {
            (has: has_cmp, cmp: cmp_u) := match sd.compare {
                some(c) { (has: true, cmp: MapGpuCmp(c)) }
                none { (has: false, cmp: u8(0)) }
            }
            spec := dx.SamplerSpec(
                min_linear: sd.min == Filter.Linear,
                mag_linear: sd.mag == Filter.Linear,
                mip_linear: sd.mip == Filter.Linear,
                addr_u: MapGpuAddr(sd.address_u),
                addr_v: MapGpuAddr(sd.address_v),
                addr_w: MapGpuAddr(sd.address_w),
                lod_min: sd.lod_min,
                lod_max: sd.lod_max,
                anisotropy: sd.anisotropy,
                has_compare: has_cmp,
                compare: cmp_u,
            )
            return d.CreateSampler(&spec) else e { fail MapDxError(e) }
        }
        .Vk(v: _) { fail Unsupported }
    }
}

Native.DestroySampler(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d) { d.DestroySampler(id) }
        .Vk(v: _) {}
    }
}

Device.NativeCreateBindGroupLayout(mut self): Error!u64 {
    match mut self.state.Mut().native {
        .Stub(s) { return s.AllocHandle() }
        .Dx(d: _) { return 0 }
        .Vk(v: _) { fail Unsupported }
    }
}

Native.DestroyBindGroupLayout(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d: _) | .Vk(v: _) {}
    }
}

Device.NativeCreatePipelineLayout(mut self, groups: []&BindGroupLayout, immediates: int, label: str): Error!u64 {
    a := self.heap.Alloc()
    var ar := try Arena.New(self.heap)
    defer ar.Free()
    st := self.state.Mut()
    match &st.native {
        .Stub(s: _) {}
        .Dx(d: _) {
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
                _ {}
            }
        }
        .Vk(v: _) { fail Unsupported }
    }
    match mut st.native {
        .Stub(s) { return s.AllocHandle() }
        _ { fail Unsupported }
    }
}

Native.DestroyPipelineLayout(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d) { d.DestroyRootSignature(id) }
        .Vk(v: _) {}
    }
}

Device.NativeCreateBindGroup(mut self, layout_slot: u32, entries: []BindEntry): Error!u64 {
    a := self.heap.Alloc()
    var ar := try Arena.New(self.heap)
    defer ar.Free()
    st := self.state.Mut()
    match &st.native {
        .Stub(s: _) {}
        .Dx(d: _) {
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
                _ {}
            }
        }
        .Vk(v: _) { fail Unsupported }
    }
    match mut st.native {
        .Stub(s) { return s.AllocHandle() }
        _ { fail Unsupported }
    }
}

Native.DestroyBindGroup(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d) { d.DestroyBindGroup(id) }
        .Vk(v: _) {}
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

Device.NativeCreateRenderPipeline(
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
    match &st.native {
        .Stub(s: _) {}
        .Dx(d: _) {
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
                _ {}
            }
        }
        .Vk(v: _) { fail Unsupported }
    }
    match mut st.native {
        .Stub(s) { return s.AllocHandle() }
        _ { fail Unsupported }
    }
}

Device.NativeCreateComputePipeline(mut self, io: Io, layout_slot: u32, cs_slot: u32, label: str): Error!u64 {
    a := self.heap.Alloc()
    st := self.state.Mut()
    match &st.native {
        .Stub(s: _) {}
        .Dx(d: _) {
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
                _ {}
            }
        }
        .Vk(v: _) { fail Unsupported }
    }
    match mut st.native {
        .Stub(s) { return s.AllocHandle() }
        _ { fail Unsupported }
    }
}

Native.DestroyPipeline(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d) { d.DestroyPipeline(id) }
        .Vk(v: _) {}
    }
}

Device.NativeCreateSwapchain(mut self): Error!u64 {
    match mut self.state.Mut().native {
        .Stub(s) { return s.AllocHandle() }
        .Dx(d: _) | .Vk(v: _) { fail Unsupported }
    }
}

Native.DestroySwapchain(mut self, a: &Alloc, id: u64) {
    _ = a
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d) { d.DestroySwapchain(id) }
        .Vk(v: _) {}
    }
}
