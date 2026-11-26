// Why: pure tests must verify every validation rule, command stream encoding,
// handle lifecycle, deferred release, and leak accounting without requiring
// native GPU hardware.
//
// What: StubDevice, StubConfig, OpenStub, OpenStubWith, and Native dispatch
// helpers for package-internal testing.
//
// How: tracks live native handles, submitted opcodes, backing buffer bytes,
// and optional fault/outdated injection on the device's heap.

import std.gpu.vk

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
        native: Native.Stub(s: StubDevice.New(a)),
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
            return v.Close()
        }
        .Stub(s) {
            return s.CloseAll(a)
        }
    }
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

Device.NativeCreateBuffer(mut self, size: int, u: &BufferUsage, label: str): Error!u64 {
    a := self.heap.Alloc()
    match mut self.state.Mut().native {
        .Stub(s) { return try s.CreateBuffer(a, size) }
        .Dx(d: _) { fail Unsupported }
        .Vk(v) {
            bd := vk.BufferDesc(
                size: size,
                vertex: u.vertex,
                index: u.index,
                uniform: u.uniform,
                uniform_dynamic: u.uniform_dynamic,
                storage: u.storage,
                copy_src: u.copy_src,
                copy_dst: u.copy_dst,
                map_read: u.map_read,
                map_write: u.map_write,
                label: label,
            )
            return v.CreateBuffer(bd) else e { fail self.RecordNativeVkError(e) }
        }
    }
}

Native.DestroyBuffer(mut self, a: &Alloc, id: u64) {
    match mut self {
        .Stub(s) { s.DestroyBuffer(a, id) }
        .Dx(d: _) {}
        .Vk(v) { v.DestroyBuffer(id) }
    }
}

Device.NativeCreateShader(mut self, code: ShaderCode, stage: Stage, entry: str, label: str): Error!u64 {
    _ = stage
    _ = entry
    match mut self.state.Mut().native {
        .Stub(s) { return s.AllocHandle() }
        .Dx(d: _) { fail Unsupported }
        .Vk(v) {
            return v.CreateShader(code.spirv, label) else e { fail self.RecordNativeVkError(e) }
        }
    }
}

Native.DestroyShader(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d: _) {}
        .Vk(v) { v.DestroyShader(id) }
    }
}

Device.NativeCreateTexture(mut self, d: &TextureDesc): Error!u64 {
    match mut self.state.Mut().native {
        .Stub(s) { return s.AllocHandle() }
        .Dx(d: _) { fail Unsupported }
        .Vk(v) {
            row := RowOf(d.format)
            var vfs: [4]i32 = [0; 4]
            vcnt := if d.view_formats.Len() > 4 { 4 } else { d.view_formats.Len() }
            for vi in 0..vcnt {
                vfs[vi] = RowOf(d.view_formats[vi]).vk_format
            }
            td := vk.TextureDesc(
                format_idx: FormatIndex(d.format),
                vk_format: row.vk_format,
                width: d.width,
                height: d.height,
                layers: d.layers,
                mips: d.mips,
                samples: d.samples,
                copy_src: d.usage.copy_src,
                copy_dst: d.usage.copy_dst,
                sampled: d.usage.sampled,
                render_target: d.usage.render_target,
                is_depth: IsDepthStencilFormat(d.format),
                has_stencil: HasStencilAspect(d.format),
                is_compressed: row.is_compressed,
                block_w: int(row.block_w),
                block_h: int(row.block_h),
                block_bytes: int(row.block_bytes),
                view_formats: vfs[0..vcnt],
                label: d.label,
            )
            return v.CreateTexture(td) else e { fail self.RecordNativeVkError(e) }
        }
    }
}

Native.DestroyTexture(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d: _) {}
        .Vk(v) { v.DestroyTexture(id) }
    }
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

Device.NativeCreateView(mut self, tex_nh: u64, ve: &ViewEntry): Error!u64 {
    match mut self.state.Mut().native {
        .Stub(s) { return s.AllocHandle() }
        .Dx(d: _) { fail Unsupported }
        .Vk(v) {
            row := RowOf(ve.format)
            vd := vk.ViewDesc(
                tex_id: tex_nh,
                vk_format: row.vk_format,
                format_idx: FormatIndex(ve.format),
                dim: VkViewDimCode(ve.dim),
                aspect: VkAspectCode(ve.aspect),
                first_mip: ve.first_mip,
                mip_count: ve.mip_count,
                first_layer: ve.first_layer,
                layer_count: ve.layer_count,
                is_depth: IsDepthStencilFormat(ve.format),
                has_stencil: HasStencilAspect(ve.format),
            )
            return v.CreateView(vd) else e { fail self.RecordNativeVkError(e) }
        }
    }
}

Device.NativeCreateSwapView(mut self): Error!u64 {
    match mut self.state.Mut().native {
        .Stub(s) { return s.AllocHandle() }
        .Dx(d: _) { fail Unsupported }
        .Vk(v: _) { return 0 }
    }
}

Native.DestroyView(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d: _) {}
        .Vk(v) { v.DestroyView(id) }
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

Device.NativeCreateSampler(mut self, d: &SamplerDesc): Error!u64 {
    match mut self.state.Mut().native {
        .Stub(s) { return s.AllocHandle() }
        .Dx(d: _) { fail Unsupported }
        .Vk(v) {
            cmp_code := match d.compare {
                some(c) { VkCompareCode(c) }
                none { 7 }
            }
            sd := vk.SamplerDesc(
                min_linear: d.min == Filter.Linear,
                mag_linear: d.mag == Filter.Linear,
                mip_linear: d.mip == Filter.Linear,
                addr_u: VkAddressCode(d.address_u),
                addr_v: VkAddressCode(d.address_v),
                addr_w: VkAddressCode(d.address_w),
                lod_min: d.lod_min,
                lod_max: d.lod_max,
                anisotropy: d.anisotropy,
                has_compare: d.compare.IsSome(),
                compare: cmp_code,
            )
            return v.CreateSampler(sd) else e { fail self.RecordNativeVkError(e) }
        }
    }
}

Native.DestroySampler(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d: _) {}
        .Vk(v) { v.DestroySampler(id) }
    }
}

Device.NativeCreateBindGroupLayout(mut self, entries: []LayoutEntry, label: str): Error!u64 {
    match mut self.state.Mut().native {
        .Stub(s) { return s.AllocHandle() }
        .Dx(d: _) { fail Unsupported }
        .Vk(v) {
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
            return v.CreateBindGroupLayout(vk_es[0..cnt], label) else e { fail self.RecordNativeVkError(e) }
        }
    }
}

Native.DestroyBindGroupLayout(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d: _) {}
        .Vk(v) { v.DestroyBindGroupLayout(id) }
    }
}

Device.NativeCreatePipelineLayout(mut self, bg_nhs: []u64, immediates: int, label: str): Error!u64 {
    match mut self.state.Mut().native {
        .Stub(s) { return s.AllocHandle() }
        .Dx(d: _) { fail Unsupported }
        .Vk(v) {
            return v.CreatePipelineLayout(bg_nhs, immediates, label) else e { fail self.RecordNativeVkError(e) }
        }
    }
}

Native.DestroyPipelineLayout(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d: _) {}
        .Vk(v) { v.DestroyPipelineLayout(id) }
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

Device.NativeCreateBindGroup(mut self, layout_nh: u64, entries: []BindEntry, label: str): Error!u64 {
    cnt := if entries.Len() > 16 { 16 } else { entries.Len() }
    vk_es := BuildVkBgEntries(self.state.Ref(), entries, cnt)
    match mut self.state.Mut().native {
        .Stub(s) { return s.AllocHandle() }
        .Dx(d: _) { fail Unsupported }
        .Vk(v) {
            return v.CreateBindGroup(layout_nh, vk_es[0..cnt], label) else e { fail self.RecordNativeVkError(e) }
        }
    }
}

Native.DestroyBindGroup(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d: _) {}
        .Vk(v) { v.DestroyBindGroup(id) }
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

Device.NativeCreateRenderPipeline(mut self, io: Io, layout_nh: u64, vs_nh: u64, fs_nh: u64, vs_entry: str, fs_entry: str, d: &RenderPipelineDesc): Error!u64 {
    match mut self.state.Mut().native {
        .Stub(s) { return s.AllocHandle() }
        .Dx(d: _) { fail Unsupported }
        .Vk(v) {
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
                layout_id: layout_nh,
                vs_id: vs_nh,
                has_fs: fs_nh != 0,
                fs_id: fs_nh,
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
            return v.CreateRenderPipeline(io, vk_d, vs_entry, fs_entry, d.label) else e { fail self.RecordNativeVkError(e) }
        }
    }
}

Device.NativeCreateComputePipeline(mut self, io: Io, layout_nh: u64, cs_nh: u64, cs_entry: str, label: str): Error!u64 {
    match mut self.state.Mut().native {
        .Stub(s) { return s.AllocHandle() }
        .Dx(d: _) { fail Unsupported }
        .Vk(v) {
            return v.CreateComputePipeline(io, layout_nh, cs_nh, cs_entry, label) else e { fail self.RecordNativeVkError(e) }
        }
    }
}

Native.DestroyPipeline(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d: _) {}
        .Vk(v) { v.DestroyPipeline(id) }
    }
}

VkPresentModeCode(m: PresentMode): int {
    return match m {
        .Fifo { vk.ModeFifo }
        .Mailbox { vk.ModeMailbox }
        .Immediate { vk.ModeImmediate }
    }
}

Device.NativeCreateSwapchain(mut self, o: &SwapchainOptions, w: int, h: int, hwnd: uint, hinst: uint): Error!u64 {
    match mut self.state.Mut().native {
        .Stub(s) { return s.AllocHandle() }
        .Dx(d: _) { fail Unsupported }
        .Vk(v) {
            sc_opts := vk.SwapchainOptions(
                hwnd: hwnd,
                hinstance: hinst,
                vk_format: RowOf(o.format).vk_format,
                format_idx: FormatIndex(o.format),
                mode: VkPresentModeCode(o.mode),
                buffers: o.buffers,
                copy_src: o.copy_src,
                width: w,
                height: h,
            )
            return v.CreateSwapchain(sc_opts) else e { fail self.RecordNativeVkError(e) }
        }
    }
}

Native.DestroySwapchain(mut self, a: &Alloc, id: u64) {
    _ = a
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d: _) {}
        .Vk(v) { v.DestroySwapchain(id) }
    }
}
