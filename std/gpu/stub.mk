// Why: pure tests must verify every validation rule, command stream encoding,
// handle lifecycle, deferred release, and leak accounting without requiring
// native GPU hardware.
//
// What: StubDevice, StubConfig, OpenStub, OpenStubWith, and Native dispatch
// helpers for resource, pipeline, and swapchain creation/destruction.
//
// How: tracks live native handles, submitted opcodes, backing buffer bytes,
// and dispatches Device.Native* / Native.Destroy* calls to Stub, Dx, or Vk.

import std.gpu.dx
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
        standard_sample_locations: true,
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
        vk_bcmds: HeapList[vk.BarrierCmd].New(a),
    )
    box := Box[DeviceState].New(a, st) else e {
        _ = st.FreeAll(a)
        fail e
    }
    return Device(heap: heap, serial: NextDeviceSerial(), state: box, cell: Cell[u8].New(0))
}

Native.CloseAll(self, a: &Alloc): int {
    match self {
        .Dx(d) {
            d.Close()
            return 0
        }
        .Vk(v) { return v.Close() }
        .Stub(s) { return s.CloseAll(a) }
    }
}

Device.NativeCreateBuffer(mut self, size: int, u: &BufferUsage, label: str): Error!u64 {
    a := self.heap.Alloc()
    st := self.state.Mut()
    match mut st.native {
        .Stub(s) { return try s.CreateBuffer(a, size) }
        .Dx(d) {
            spec := dx.BufferSpec(
                size: size,
                uniform_dynamic: u.uniform_dynamic,
                uniform: u.uniform,
                storage: u.storage,
                map_read: u.map_read,
                map_write: u.map_write,
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
        .Dx(d) { d.DestroyBuffer(id) }
        .Vk(v) { v.DestroyBuffer(id) }
    }
}

Device.NativeCreateShader(mut self, code: ShaderCode, stage: Stage, entry: str, label: str): Error!u64 {
    _ = stage
    _ = entry
    st := self.state.Mut()
    match mut st.native {
        .Stub(s) { return s.AllocHandle() }
        .Dx(d) { return d.CreateShader(code.dxil) else e { fail MapDxError(e) } }
        .Vk(v) { return v.CreateShader(code.spirv, label) else e { fail self.RecordNativeVkError(e) } }
    }
}

Native.DestroyShader(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d) { d.DestroyShader(id) }
        .Vk(v) { v.DestroyShader(id) }
    }
}

Device.NativeCreateTexture(mut self, td: &TextureDesc): Error!u64 {
    a := self.heap.Alloc()
    st := self.state.Mut()
    row := RowOf(td.format)
    match mut st.native {
        .Stub(s) { return s.AllocHandle() }
        .Dx(d) {
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
        .Vk(v) {
            var vfs: [4]i32 = [0; 4]
            vcnt := if td.view_formats.Len() > 4 { 4 } else { td.view_formats.Len() }
            for vi in 0..vcnt {
                vfs[vi] = RowOf(td.view_formats[vi]).vk_format
            }
            vtd := vk.TextureDesc(
                format_idx: FormatIndex(td.format),
                vk_format: row.vk_format,
                width: td.width,
                height: td.height,
                layers: td.layers,
                mips: td.mips,
                samples: td.samples,
                copy_src: td.usage.copy_src,
                copy_dst: td.usage.copy_dst,
                sampled: td.usage.sampled,
                render_target: td.usage.render_target,
                is_depth: IsDepthStencilFormat(td.format),
                has_stencil: HasStencilAspect(td.format),
                is_compressed: row.is_compressed,
                block_w: int(row.block_w),
                block_h: int(row.block_h),
                block_bytes: int(row.block_bytes),
                view_formats: vfs[0..vcnt],
                label: td.label,
            )
            return v.CreateTexture(vtd) else e { fail self.RecordNativeVkError(e) }
        }
    }
}

Native.DestroyTexture(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d) { d.DestroyTexture(id) }
        .Vk(v) { v.DestroyTexture(id) }
    }
}

Device.NativeCreateView(mut self, ve: &ViewEntry): Error!u64 {
    a := self.heap.Alloc()
    st := self.state.Mut()
    tex_nh := if ve.tex_slot != 0 { st.textures.Get(ve.tex_slot).native_handle } else { u64(0) }
    row := RowOf(ve.format)
    match mut st.native {
        .Stub(s) { return s.AllocHandle() }
        .Dx(d) {
            spec := dx.ViewSpec(
                tex_nh: tex_nh,
                dxgi_rtv_dsv: row.dxgi_rtv_dsv,
                dxgi_srv: row.dxgi_srv,
                dxgi_stencil_srv: row.dxgi_stencil_srv,
                dim: Cast[u8](VkViewDimCode(ve.dim)),
                aspect: Cast[u8](VkAspectCode(ve.aspect)),
                first_mip: ve.first_mip,
                mip_count: ve.mip_count,
                first_layer: ve.first_layer,
                layer_count: ve.layer_count,
                samples: ve.samples,
                render_target: ve.usage.render_target,
                sampled: ve.usage.sampled,
                is_depth: IsDepthStencilFormat(ve.format),
                has_stencil: HasStencilAspect(ve.format),
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
        .Vk(v) {
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
        .Dx(d) { d.DestroyView(id) }
        .Vk(v) { v.DestroyView(id) }
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
        .Vk(v) {
            cmp_code := match sd.compare {
                some(c) { VkCompareCode(c) }
                none { 7 }
            }
            vsd := vk.SamplerDesc(
                min_linear: sd.min == Filter.Linear,
                mag_linear: sd.mag == Filter.Linear,
                mip_linear: sd.mip == Filter.Linear,
                addr_u: VkAddressCode(sd.address_u),
                addr_v: VkAddressCode(sd.address_v),
                addr_w: VkAddressCode(sd.address_w),
                lod_min: sd.lod_min,
                lod_max: sd.lod_max,
                anisotropy: sd.anisotropy,
                has_compare: sd.compare.IsSome(),
                compare: cmp_code,
            )
            return v.CreateSampler(vsd) else e { fail self.RecordNativeVkError(e) }
        }
    }
}

Native.DestroySampler(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d) { d.DestroySampler(id) }
        .Vk(v) { v.DestroySampler(id) }
    }
}

Device.NativeCreateBindGroupLayout(mut self, entries: []LayoutEntry, label: str): Error!u64 {
    match &self.state.Ref().native {
        .Stub(s: _) {}
        .Dx(d: _) { return 0 }
        .Vk(v: _) { return try self.VkCreateBindGroupLayout(entries, label) }
    }
    match mut self.state.Mut().native {
        .Stub(s) { return s.AllocHandle() }
        _ { fail Unsupported }
    }
}

Native.DestroyBindGroupLayout(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d: _) {}
        .Vk(v) { v.DestroyBindGroupLayout(id) }
    }
}

Device.NativeCreatePipelineLayout(mut self, groups: []&BindGroupLayout, immediates: int, label: str): Error!u64 {
    match &self.state.Ref().native {
        .Stub(s: _) {}
        .Dx(d: _) { return try self.DxCreatePipelineLayout(groups, immediates, label) }
        .Vk(v: _) { return try self.VkCreatePipelineLayout(groups, immediates, label) }
    }
    match mut self.state.Mut().native {
        .Stub(s) { return s.AllocHandle() }
        _ { fail Unsupported }
    }
}

Native.DestroyPipelineLayout(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d) { d.DestroyRootSignature(id) }
        .Vk(v) { v.DestroyPipelineLayout(id) }
    }
}

Device.NativeCreateBindGroup(mut self, layout_slot: u32, entries: []BindEntry, label: str): Error!u64 {
    match &self.state.Ref().native {
        .Stub(s: _) {}
        .Dx(d: _) { return try self.DxCreateBindGroup(layout_slot, entries) }
        .Vk(v: _) { return try self.VkCreateBindGroup(layout_slot, entries, label) }
    }
    match mut self.state.Mut().native {
        .Stub(s) { return s.AllocHandle() }
        _ { fail Unsupported }
    }
}

Native.DestroyBindGroup(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d) { d.DestroyBindGroup(id) }
        .Vk(v) { v.DestroyBindGroup(id) }
    }
}

Device.NativeCreateRenderPipeline(
    mut self,
    io: Io,
    layout_slot: u32,
    vs_slot: u32,
    fs_slot: u32,
    d: &RenderPipelineDesc,
): Error!u64 {
    match &self.state.Ref().native {
        .Stub(s: _) {}
        .Dx(d: _) { return try self.DxCreateRenderPipeline(io, layout_slot, vs_slot, fs_slot, d) }
        .Vk(v: _) { return try self.VkCreateRenderPipeline(io, layout_slot, vs_slot, fs_slot, d) }
    }
    match mut self.state.Mut().native {
        .Stub(s) { return s.AllocHandle() }
        _ { fail Unsupported }
    }
}

Device.NativeCreateComputePipeline(mut self, io: Io, layout_slot: u32, cs_slot: u32, label: str): Error!u64 {
    match &self.state.Ref().native {
        .Stub(s: _) {}
        .Dx(d: _) { return try self.DxCreateComputePipeline(io, layout_slot, cs_slot, label) }
        .Vk(v: _) { return try self.VkCreateComputePipeline(io, layout_slot, cs_slot, label) }
    }
    match mut self.state.Mut().native {
        .Stub(s) { return s.AllocHandle() }
        _ { fail Unsupported }
    }
}

Native.DestroyPipeline(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d) { d.DestroyPipeline(id) }
        .Vk(v) { v.DestroyPipeline(id) }
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
        .Dx(d) { d.DestroySwapchain(id) }
        .Vk(v) { v.DestroySwapchain(id) }
    }
}
