// Why: pure tests must verify every validation rule, command stream encoding,
// handle lifecycle, deferred release, and leak accounting without requiring
// native GPU hardware.
//
// What: StubDevice, StubConfig, OpenStub, OpenStubWith, and Native dispatch
// helpers for package-internal testing.
//
// How: tracks live native handles, submitted opcodes, backing buffer bytes,
// and optional fault/outdated injection on the device's heap.

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
            v.Close()
            return 0
        }
        .Stub(s) {
            return s.CloseAll(a)
        }
    }
}

Device.NativeCreateBuffer(mut self, size: int): Error!u64 {
    a := self.heap.Alloc()
    match mut self.state.Mut().native {
        .Stub(s) { return try s.CreateBuffer(a, size) }
        .Dx(d: _) | .Vk(v: _) { fail Unsupported }
    }
}

Native.DestroyBuffer(mut self, a: &Alloc, id: u64) {
    match mut self {
        .Stub(s) { s.DestroyBuffer(a, id) }
        .Dx(d: _) | .Vk(v: _) {}
    }
}

Device.NativeCreateShader(mut self, code: ShaderCode, stage: Stage, entry: str): Error!u64 {
    _ = code
    _ = stage
    _ = entry
    match mut self.state.Mut().native {
        .Stub(s) { return s.AllocHandle() }
        .Dx(d: _) | .Vk(v: _) { fail Unsupported }
    }
}

Native.DestroyShader(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d: _) | .Vk(v: _) {}
    }
}

Device.NativeCreateTexture(mut self): Error!u64 {
    match mut self.state.Mut().native {
        .Stub(s) { return s.AllocHandle() }
        .Dx(d: _) | .Vk(v: _) { fail Unsupported }
    }
}

Native.DestroyTexture(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d: _) | .Vk(v: _) {}
    }
}

Device.NativeCreateView(mut self): Error!u64 {
    match mut self.state.Mut().native {
        .Stub(s) { return s.AllocHandle() }
        .Dx(d: _) | .Vk(v: _) { fail Unsupported }
    }
}

Native.DestroyView(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d: _) | .Vk(v: _) {}
    }
}

Device.NativeCreateSampler(mut self): Error!u64 {
    match mut self.state.Mut().native {
        .Stub(s) { return s.AllocHandle() }
        .Dx(d: _) | .Vk(v: _) { fail Unsupported }
    }
}

Native.DestroySampler(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d: _) | .Vk(v: _) {}
    }
}

Device.NativeCreateBindGroupLayout(mut self): Error!u64 {
    match mut self.state.Mut().native {
        .Stub(s) { return s.AllocHandle() }
        .Dx(d: _) | .Vk(v: _) { fail Unsupported }
    }
}

Native.DestroyBindGroupLayout(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d: _) | .Vk(v: _) {}
    }
}

Device.NativeCreatePipelineLayout(mut self): Error!u64 {
    match mut self.state.Mut().native {
        .Stub(s) { return s.AllocHandle() }
        .Dx(d: _) | .Vk(v: _) { fail Unsupported }
    }
}

Native.DestroyPipelineLayout(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d: _) | .Vk(v: _) {}
    }
}

Device.NativeCreateBindGroup(mut self): Error!u64 {
    match mut self.state.Mut().native {
        .Stub(s) { return s.AllocHandle() }
        .Dx(d: _) | .Vk(v: _) { fail Unsupported }
    }
}

Native.DestroyBindGroup(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d: _) | .Vk(v: _) {}
    }
}

Device.NativeCreatePipeline(mut self): Error!u64 {
    match mut self.state.Mut().native {
        .Stub(s) { return s.AllocHandle() }
        .Dx(d: _) | .Vk(v: _) { fail Unsupported }
    }
}

Native.DestroyPipeline(mut self, id: u64) {
    match mut self {
        .Stub(s) { s.FreeHandle(id) }
        .Dx(d: _) | .Vk(v: _) {}
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
        .Dx(d: _) | .Vk(v: _) {}
    }
}
