// Why: a program needs to list adapters, open one device on one of them,
// query limits and capabilities, and close it with an account of what leaked.
//
// What: AdapterInfo, DeviceLimits, Adapters, Open, Device, Message,
// Severity, Leaks, and internal device state, handle lifecycle, and native
// dispatch.
//
// How: Device holds a Box[DeviceState] on heap and a Cell[u8] so it is Send
// and not Share without unsafe; Native dispatches inline over Dx, Vk, and
// Test-profile Stub.

import std.gpu.dx
import std.gpu.vk
import std.mem
import std.raw
import std.rt

/// Limits the portable layer enforces: the smaller of the adapter's and
/// the portable maxima. Plain.
export struct DeviceLimits {
    export max_texture_size: int
    export max_texture_layers: int
    export max_bind_groups: int
    export max_bindings_per_group: int
    export max_dynamic_uniforms: int
    export max_immediates: int
    export max_vertex_buffers: int
    export max_vertex_attributes: int
    export max_vertex_stride: int
    export max_color_targets: int
    export uniform_offset_align: int
    export storage_offset_align: int
    export max_uniform_binding: int
    export max_storage_binding: int
    export max_buffer_size: int
    export max_uniforms_per_stage: int
    export max_storage_per_stage: int
    export max_textures_per_stage: int
    export max_samplers_per_stage: int
    export max_sampler_descriptors: int
    export max_workgroup_size_x: int
    export max_workgroup_size_y: int
    export max_workgroup_size_z: int
    export max_workgroup_invocations: int
    export max_workgroups_per_dimension: int
    export max_workgroup_storage: int
    sealed: u8
}

/// One adapter Adapters found.
export struct AdapterInfo {
    export id: AdapterId
    export name: str
    export vendor: u32
    export device: u32
    export kind: AdapterKind
    export limits: DeviceLimits
    export features: Features
}

MapDxCall(c: u8): Call {
    return match c {
        0 { Call.CreateFactory }
        1 { Call.EnumAdapters }
        2 { Call.CreateDevice }
        3 { Call.CreateHeap }
        4 { Call.CreateResource }
        5 { Call.CreateRootSignature }
        6 { Call.CreatePipeline }
        7 { Call.CreateSwapchain }
        8 { Call.ResizeBuffers }
        9 { Call.Present }
        10 { Call.ExecuteCommandLists }
        11 { Call.Signal }
        _ { Call.Map }
    }
}

MapDxErrKindToLoss(k: dx.ErrKind): LossReason {
    return match k {
        .DeviceLostHung { LossReason.Hung }
        .DeviceLostReset { LossReason.Reset }
        .DeviceLostRemoved { LossReason.Removed }
        .DeviceLostInternal { LossReason.DriverInternal }
        .DeviceLostOom { LossReason.OutOfMemory }
        _ { LossReason.Unknown }
    }
}

MapDxError(e: dx.Error): Error {
    return match e {
        dx.Unsupported { Unsupported }
        OutOfMemory { OutOfMemory }
        Canceled { Canceled }
        dx.DxFail(kind, code, call, at) {
            if kind == dx.ErrKind.Unsupported {
                Unsupported
            } else if kind == dx.ErrKind.NoAdapter {
                NoAdapter
            } else if kind == dx.ErrKind.MissingBc {
                Missing(feature: Feature.TextureCompressionBc)
            } else if kind == dx.ErrKind.MissingDepthClip {
                Missing(feature: Feature.DepthClipControl)
            } else if kind == dx.ErrKind.MissingF32Filter {
                Missing(feature: Feature.Float32Filterable)
            } else if kind == dx.ErrKind.MissingAniso {
                Missing(feature: Feature.SamplerAnisotropy)
            } else if kind == dx.ErrKind.MissingDebugLayer {
                Missing(feature: Feature.DebugLayer)
            } else if kind == dx.ErrKind.DeviceLostHung || kind == dx.ErrKind.DeviceLostReset || kind == dx.ErrKind.DeviceLostRemoved || kind == dx.ErrKind.DeviceLostInternal || kind == dx.ErrKind.DeviceLostOom || kind == dx.ErrKind.DeviceLostUnknown {
                DeviceLost(reason: MapDxErrKindToLoss(kind))
            } else if kind == dx.ErrKind.OutOfDeviceMemory {
                OutOfDeviceMemory
            } else if kind == dx.ErrKind.LimitExceeded {
                Invalid(misuse: Misuse.LimitExceeded, at: at)
            } else if kind == dx.ErrKind.SurfaceLost {
                SurfaceLost
            } else if kind == dx.ErrKind.DebugLayer {
                Invalid(misuse: Misuse.DebugLayer, at: at)
            } else {
                Driver(code: code, call: MapDxCall(call))
            }
        }
    }
}

RecordDxFailure(st: mut DeviceState, err: Error) {
    match err {
        DeviceLost(reason) {
            st.lost = true
            match st.sticky_err {
                none { st.sticky_err = some(DeviceLost(reason: reason)) }
                some(_) {}
            }
        }
        Invalid(misuse, at) {
            if misuse == Misuse.DebugLayer {
                match st.sticky_err {
                    none { st.sticky_err = some(Invalid(misuse: Misuse.DebugLayer, at: at)) }
                    some(_) {}
                }
            }
        }
        _ {}
    }
}

SyncDxMessages(a: &Alloc, st: mut DeviceState) {
    match mut st.native {
        .Dx(d) {
            (errors: errs, warnings: warns) := d.MessageCounts()
            st.err_count = errs
            st.warn_count = warns
            if d.HasMessages() {
                var ar := Arena.NewIn(a) else { return }
                defer ar.Free()
                ms := d.TakeMessages(&ar) else { return }
                for i in 0..ms.Len() {
                    if st.messages.Len() >= 256 {
                        break
                    }
                    m := &ms[i]
                    sev := if m.severity == 2 {
                        Severity.Error
                    } else if m.severity == 1 {
                        Severity.Warning
                    } else {
                        Severity.Info
                    }
                    off := st.msg_bytes.Len()
                    st.msg_bytes.Extend(a, m.text.AsBytes()) else { break }
                    tlen := st.msg_bytes.Len() - off
                    st.messages.Push(a, StoredMessage(severity: sev, id: m.id, text_off: off, text_len: tlen)) else { break }
                }
            }
            if errs > 0 || (st.debug_mode == 2 && warns > 0) {
                RecordDxFailure(st, Invalid(misuse: Misuse.DebugLayer, at: -1))
            }
        }
        .Stub(s: _) | .Vk(v: _) {}
    }
}

BuildFormatTables(triples: mut [132]u32, guar: mut [44]dx.FormatRowCaps) {
    for i in 0..TextureFormatCount {
        row := RowOf(FormatByIndex(i))
        triples[i * 3] = row.dxgi_res
        triples[i * 3 + 1] = row.dxgi_rtv_dsv
        triples[i * 3 + 2] = row.dxgi_srv
        g := row.guaranteed
        guar[i] = dx.FormatRowCaps(
            sampled: g.sampled,
            filterable: g.filterable,
            render_target: g.render_target,
            blendable: g.blendable,
            multisample: g.multisample,
            resolve: g.resolve,
            depth_stencil: g.depth_stencil,
        )
    }
}

/// Lists the adapters of backend (Auto: every backend present), best first.
/// An adapter missing a requirement or a guaranteed format capability is
/// not listed. Errors: Unsupported off Windows; Canceled while queued.
/// O(adapters × formats) driver queries on the blocking pool; allocates
/// the list and the names in a.
export Adapters(a: &Arena, io: Io, backend: Backend): Error!List[AdapterInfo] from a {
    var out := List[AdapterInfo].New(a)
    if backend == Backend.D3d12 || backend == Backend.Auto {
        var triples: [132]u32 = [0; 132]
        var guar: [44]dx.FormatRowCaps = [mem.Zeroed[dx.FormatRowCaps](); 44]
        BuildFormatTables(mut triples, mut guar)
        dxs := dx.Adapters(a, io, triples[..], guar[..]) else e {
            if backend == Backend.D3d12 {
                fail MapDxError(e)
            }
            List[dx.AdapterInfo].New(a)
        }
        for i in 0..dxs.Len() {
            ad := &dxs[i]
            var lim := DefaultPortableLimits
            lim.max_texture_size = ad.max_texture_size
            try out.Push(AdapterInfo(
                id: AdapterId(backend: Backend.D3d12, luid: ad.luid),
                name: ad.name,
                vendor: ad.vendor,
                device: ad.device,
                kind: if ad.is_software { AdapterKind.Software } else { AdapterKind.Discrete },
                limits: lim,
                features: Features(texture_compression_bc: true, depth_clip_control: true, float32_filterable: true, sampler_anisotropy: true, sealed: 0),
            ))
        }
    }
    if backend == Backend.Vulkan || backend == Backend.Auto {
        if vk.IsAvailable(io) {
            vk_list := vk.EnumAdapters(a, io) else e {
                if backend == Backend.Vulkan {
                    fail MapVkErr(e)
                }
                List[vk.AdapterDesc].New(a)
            }
            for i in 0..vk_list.Len() {
                try out.Push(AdapterInfoFromVk(&vk_list[i], Backend.Vulkan))
            }
        } else if backend == Backend.Vulkan {
            fail Unsupported
        }
    }
    if out.Len() == 0 {
        fail Unsupported
    }
    return out
}

OpenVkDevice(heap: &Heap, io: Io, o: &DeviceOptions): Error!Device from heap {
    vopts := VkOpenOptsFromGpu(o)
    vkres := vk.Open(heap, io, vopts) else e {
        fail MapVkErr(e)
    }
    vk.VkOpenResult(dev: vdev, adapter: vad) := vkres
    a := heap.Alloc()
    info := AdapterInfoFromVk(&vad, Backend.Vulkan)
    caps := CapsFromVk(&vad.caps)
    limits := LimitsFromVk(&vad.limits)
    st := DeviceState(
        info: info,
        caps: caps,
        limits: limits,
        features: o.features,
        backend: Backend.Vulkan,
        debug_mode: 0,
        native: Native.Vk(v: vdev),
        adapter_name_bytes: HeapList[u8].New(a),
        sticky_err: none,
        lost: false,
        depth_bias_clamp_ok: vad.depth_bias_clamp_ok,
        draw_params_ok: vad.draw_params_ok,
        standard_sample_locations: vad.standard_sample_locations,
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
        vk.FreeOpenAdapterName(heap, info.name)
        _ = st.FreeAll(a)
        fail e
    }
    return Device(heap: heap, serial: NextDeviceSerial(), state: box, cell: Cell[u8].New(0))
}

enum Native {
    Dx(d: dx.Device)
    Vk(v: vk.Device)
    Stub(s: StubDevice)
}

struct StoredMessage {
    severity: Severity
    id: i64
    text_off: int
    text_len: int
}

struct DeviceState {
    info: AdapterInfo
    caps: [44]FormatCaps
    limits: DeviceLimits
    features: Features
    backend: Backend
    debug_mode: u8
    native: Native
    adapter_name_bytes: HeapList[u8]
    sticky_err: ?Error
    lost: bool
    depth_bias_clamp_ok: bool
    draw_params_ok: bool
    standard_sample_locations: bool
    serials: SerialTracker
    deferred: DeferredQueue
    chunk_pool: ChunkPool
    stream: CommandStream
    pre_submit: CommandStream
    encoder_active: bool
    pass_shadow: PassShadow
    messages: HeapList[StoredMessage]
    msg_bytes: HeapList[u8]
    err_count: int
    warn_count: int
    planted_bug: PlantedBug
    buffers: SlotTable[BufferEntry]
    textures: SlotTable[TextureEntry]
    views: SlotTable[ViewEntry]
    samplers: SlotTable[SamplerEntry]
    bg_layouts: SlotTable[BindGroupLayoutEntry]
    pipe_layouts: SlotTable[PipelineLayoutEntry]
    bind_groups: SlotTable[BindGroupEntry]
    shaders: SlotTable[ShaderEntry]
    render_pipes: SlotTable[RenderPipelineEntry]
    compute_pipes: SlotTable[ComputePipelineEntry]
    swapchains: SlotTable[SwapchainEntry]
    dx_chunks: HeapList[dx.ChunkSpan]
    dx_barriers: HeapList[dx.BarrierSpec]
    dx_passes: HeapList[dx.PassReplaySpec]
    vk_bcmds: HeapList[vk.BarrierCmd]
}

/// A device and its one queue. Linear: end it with Close. Send, not Share.
export linear view struct Device {
    heap: &Heap
    serial: u32
    state: Box[DeviceState]
    cell: Cell[u8]
}

var DeviceSerial: raw.Atomic[u64] = raw.Atomic[u64].New(1)

NextDeviceSerial(): u32 {
    return Cast[u32](DeviceSerial.Add(1))
}

const DefaultPortableLimits: DeviceLimits = DeviceLimits(
    max_texture_size: 16384,
    max_texture_layers: 256,
    max_bind_groups: 4,
    max_bindings_per_group: 16,
    max_dynamic_uniforms: 4,
    max_immediates: 128,
    max_vertex_buffers: 16,
    max_vertex_attributes: 16,
    max_vertex_stride: 2048,
    max_color_targets: 8,
    uniform_offset_align: 256,
    storage_offset_align: 256,
    max_uniform_binding: 65536,
    max_storage_binding: 134217728,
    max_buffer_size: 268435456,
    max_uniforms_per_stage: 12,
    max_storage_per_stage: 8,
    max_textures_per_stage: 16,
    max_samplers_per_stage: 16,
    max_sampler_descriptors: 2048,
    max_workgroup_size_x: 1024,
    max_workgroup_size_y: 1024,
    max_workgroup_size_z: 64,
    max_workgroup_invocations: 1024,
    max_workgroups_per_dimension: 65535,
    max_workgroup_storage: 32768,
    sealed: 0,
)

ValidateDeviceOptions(o: &DeviceOptions): Error!void {
    if o.frames_in_flight < 1 || o.frames_in_flight > 3 {
        fail Invalid(misuse: Misuse.LimitExceeded, at: -1)
    }
    if o.descriptors <= 0 {
        fail Invalid(misuse: Misuse.LimitExceeded, at: -1)
    }
}

OpenD3d12(heap: &Heap, io: Io, o: &DeviceOptions): Error!Device from heap {
    a := heap.Alloc()

    var triples: [132]u32 = [0; 132]
    var guar: [44]dx.FormatRowCaps = [mem.Zeroed[dx.FormatRowCaps](); 44]
    BuildFormatTables(mut triples, mut guar)

    (has: has_luid, luid: target_luid) := match o.adapter {
        some(aid) {
            if aid.backend != Backend.D3d12 && aid.backend != Backend.Auto {
                fail NoAdapter
            }
            (has: true, luid: aid.luid)
        }
        none { (has: false, luid: u64(0)) }
    }
    dbg_u := match o.debug {
        .Off { u8(0) }
        .Validate { u8(1) }
        .Strict { u8(2) }
    }
    pwr_u := match o.power {
        .Default { u8(0) }
        .HighPerformance { u8(1) }
        .LowPower { u8(2) }
    }
    dx_opts := dx.OpenOptions(
        power: pwr_u,
        software: o.software,
        has_luid: has_luid,
        target_luid: target_luid,
        debug: dbg_u,
        frames_in_flight: o.frames_in_flight,
        descriptors: o.descriptors,
        req_bc: o.features.texture_compression_bc,
        req_depth_clip: o.features.depth_clip_control,
        req_f32_filter: o.features.float32_filterable,
        req_aniso: o.features.sampler_anisotropy,
    )
    (dev: dx_dev0, info: dx_info, caps: dx_caps) := dx.Open(heap, io, dx_opts, triples[..], guar[..]) else e {
        fail MapDxError(e)
    }
    var dx_dev := dx_dev0
    onerr dx_dev.Close()

    var caps: [44]FormatCaps = [NoCaps; 44]
    for i in 0..44 {
        c := &dx_caps[i]
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
    var lim := DefaultPortableLimits
    lim.max_texture_size = dx_info.max_texture_size
    info := AdapterInfo(
        id: AdapterId(backend: Backend.D3d12, luid: dx_info.luid),
        name: dx_info.name,
        vendor: dx_info.vendor,
        device: dx_info.device,
        kind: if dx_info.is_software { AdapterKind.Software } else { AdapterKind.Discrete },
        limits: lim,
        features: o.features,
    )
    var st := DeviceState(
        info: info,
        caps: caps,
        limits: lim,
        features: o.features,
        backend: Backend.D3d12,
        debug_mode: dbg_u,
        native: Native.Dx(d: dx_dev),
        adapter_name_bytes: HeapList[u8].New(a),
        sticky_err: none,
        lost: false,
        depth_bias_clamp_ok: true,
        draw_params_ok: true,
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
    // Serial 1 was used during Open to zero the device zero-buffer; align SerialTracker.
    st.serials.next = 2
    st.serials.completed = 1
    SyncDxMessages(a, mut st)
    box := Box[DeviceState].New(a, st) else e {
        _ = st.FreeAll(a)
        fail e
    }
    return Device(heap: heap, serial: NextDeviceSerial(), state: box, cell: Cell[u8].New(0))
}

/// Makes a device by o. Errors: Unsupported (target), NoAdapter,
/// Missing(feature), DeviceLost, OutOfDeviceMemory, OutOfMemory, Canceled
/// (only while queued). Runs creation and the format queries through
/// runtime.Blocking. Allocates the device's tables from heap.
export Open(heap: &Heap, io: Io, o: DeviceOptions): Error!Device from heap {
    try ValidateDeviceOptions(&o)
    match o.backend {
        .D3d12 {
            return try OpenD3d12(heap, io, &o)
        }
        .Vulkan {
            return try OpenVkDevice(heap, io, &o)
        }
        .Auto {
            return OpenD3d12(heap, io, &o) else e {
                match e {
                    Unsupported | NoAdapter {
                        return try OpenVkDevice(heap, io, &o)
                    }
                    _ { fail e }
                }
            }
        }
    }
    fail Unsupported
}

/// The adapter this device runs on. O(1).
export Device.Info(&self): &AdapterInfo from self {
    return &self.state.Ref().info
}

/// The backend this device runs on. O(1).
export Device.Backend(&self): Backend {
    return self.state.Ref().backend
}

/// The limits this device enforces. O(1).
export Device.Limits(&self): DeviceLimits {
    return self.state.Ref().limits
}

/// The optional features enabled at Open. O(1).
export Device.Features(&self): Features {
    return self.state.Ref().features
}

/// What f supports on this device, as queried at Open. O(1).
export Device.FormatCaps(&self, f: TextureFormat): FormatCaps {
    return self.state.Ref().caps[FormatIndex(f)]
}

/// Waits until the GPU has finished every submission. Errors: DeviceLost,
/// Canceled (repeatable). Suspends; holds no thread.
export Device.WaitIdle(mut self, io: Io): Error!void {
    try self.CheckAlive()
    try self.FlushPreSubmit()
    st := self.state.Mut()
    var last := st.serials.LastSubmitted()
    match mut st.native {
        .Stub(s) {
            try s.WaitSerial(last)
        }
        .Dx(d) {
            d.WaitQueueIdle(io, mut st.serials.next) else e {
                ge := MapDxError(e)
                RecordDxFailure(st, ge)
                fail ge
            }
            last = st.serials.LastSubmitted()
        }
        .Vk(v) {
            last = v.WaitQueuedSerial(io, last) else e {
                match e {
                    vk.DeviceLost(reason: r) {
                        st.lost = true
                        st.sticky_err = some(DeviceLost(reason: MapVkLossReason(r)))
                    }
                    _ {}
                }
                fail MapVkErr(e)
            }
        }
    }
    st.serials.Complete(last)
    self.DrainCompleted()
}

/// One debug-layer or messenger message.
export struct Message {
    export severity: Severity
    export id: i64
    export text: str
}

/// How bad a Message is.
export open enum Severity {
    Info
    Warning
    Error
}

/// Takes the messages recorded so far (at most 256 kept; Leaks counts the
/// rest). O(messages); allocates their text in a.
export Device.TakeMessages(mut self, a: &Arena): OutOfMemory!List[Message] from a {
    var out := List[Message].New(a)
    st := self.state.Mut()
    n := st.messages.Len()
    for i in 0..n {
        m := &st.messages[i]
        bs := st.msg_bytes.Items()[m.text_off..(m.text_off + m.text_len)]
        var dup := try a.Slice[u8](bs.Len())
        (mut dup).CopyFrom(bs)
        txt := rt.StrFrom(dup[..]) else { "" }
        try out.Push(Message(severity: m.severity, id: m.id, text: txt))
    }
    st.messages.Clear()
    st.msg_bytes.Clear()
    match mut st.native {
        .Vk(v) {
            var vmsgs := List[vk.DebugMessage].New(a)
            var errs := 0
            var warns := 0
            try v.DrainDebugMessages(a, mut vmsgs, mut errs, mut warns)
            for vi in 0..vmsgs.Len() {
                vm := vmsgs[vi]
                sev := match vm.severity {
                    2 { Severity.Error }
                    1 { Severity.Warning }
                    _ { Severity.Info }
                }
                try out.Push(Message(severity: sev, id: vm.id, text: vm.text))
            }
        }
        _ {}
    }
    return out
}

/// What a device still held at Close, and the debug messages it saw.
export struct Leaks {
    /// Native objects the backend still counted after releasing.
    export native: int
    export errors: int
    export warnings: int
}

DeviceState.FreeAll(self, a: &Alloc): int {
    DeviceState(
        info: _,
        caps: _,
        limits: _,
        features: _,
        backend: _,
        debug_mode: _,
        native,
        adapter_name_bytes,
        sticky_err: _,
        lost: _,
        depth_bias_clamp_ok: _,
        draw_params_ok: _,
        standard_sample_locations: _,
        serials: _,
        deferred,
        chunk_pool,
        stream,
        pre_submit,
        encoder_active: _,
        pass_shadow,
        messages,
        msg_bytes,
        err_count: _,
        warn_count: _,
        planted_bug: _,
        buffers: buffers0,
        textures: textures0,
        views: views0,
        samplers: samplers0,
        bg_layouts: bg_layouts0,
        pipe_layouts: pipe_layouts0,
        bind_groups: bind_groups0,
        shaders: shaders0,
        render_pipes: render_pipes0,
        compute_pipes: compute_pipes0,
        swapchains: swapchains0,
        dx_chunks,
        dx_barriers,
        dx_passes,
        vk_bcmds,
    ) := self

    stream.FreeAll(a)
    pre_submit.FreeAll(a)
    chunk_pool.FreeAll(a)
    deferred.Free(a)
    pass_shadow.FreeAll(a)
    messages.Free(a)
    msg_bytes.Free(a)
    adapter_name_bytes.Free(a)
    dx_chunks.Free(a)
    dx_barriers.Free(a)
    dx_passes.Free(a)
    vk_bcmds.Free(a)

    var nat := native
    var swapchains := swapchains0
    for {
        match swapchains.DrainPop(a) {
            some(e) {
                SwapchainEntry(info: _, view_slot: _, view_gen: _, native_handle, surface, handed_out: _) := e
                nat.DestroySwapchain(a, native_handle)
                match surface {
                    some(surf) { surf.Close() }
                    none {}
                }
            }
            none { break }
        }
    }
    swapchains.FreeEmpty(a)
    var render_pipes := render_pipes0
    for {
        match render_pipes.DrainPop(a) {
            some(e) {
                nh := e.native_handle
                e.Free(a)
                nat.DestroyPipeline(nh)
            }
            none { break }
        }
    }
    render_pipes.FreeEmpty(a)
    var compute_pipes := compute_pipes0
    for {
        match compute_pipes.DrainPop(a) {
            some(e) {
                nh := e.native_handle
                e.Free(a)
                nat.DestroyPipeline(nh)
            }
            none { break }
        }
    }
    compute_pipes.FreeEmpty(a)
    var shaders := shaders0
    for {
        match shaders.DrainPop(a) {
            some(e) {
                nh := e.native_handle
                e.Free(a)
                nat.DestroyShader(nh)
            }
            none { break }
        }
    }
    shaders.FreeEmpty(a)
    var bind_groups := bind_groups0
    for {
        match bind_groups.DrainPop(a) {
            some(e) {
                nh := e.native_handle
                e.Free(a)
                nat.DestroyBindGroup(nh)
            }
            none { break }
        }
    }
    bind_groups.FreeEmpty(a)
    var pipe_layouts := pipe_layouts0
    for {
        match pipe_layouts.DrainPop(a) {
            some(e) {
                nh := e.native_handle
                e.Free(a)
                nat.DestroyPipelineLayout(nh)
            }
            none { break }
        }
    }
    pipe_layouts.FreeEmpty(a)
    var bg_layouts := bg_layouts0
    for {
        match bg_layouts.DrainPop(a) {
            some(e) {
                nh := e.native_handle
                e.Free(a)
                nat.DestroyBindGroupLayout(nh)
            }
            none { break }
        }
    }
    bg_layouts.FreeEmpty(a)
    var views := views0
    for {
        match views.DrainPop(a) {
            some(e) { nat.DestroyViewEntry(&e) }
            none { break }
        }
    }
    views.FreeEmpty(a)
    var samplers := samplers0
    for {
        match samplers.DrainPop(a) {
            some(e) { nat.DestroySampler(e.native_handle) }
            none { break }
        }
    }
    samplers.FreeEmpty(a)
    var textures := textures0
    for {
        match textures.DrainPop(a) {
            some(e) {
                nh := e.native_handle
                e.Free(a)
                nat.DestroyTexture(nh)
            }
            none { break }
        }
    }
    textures.FreeEmpty(a)
    var buffers := buffers0
    for {
        match buffers.DrainPop(a) {
            some(e) { nat.DestroyBuffer(a, e.native_handle) }
            none { break }
        }
    }
    buffers.FreeEmpty(a)

    return nat.CloseAll(a)
}

WaitBeforeClose(dev: mut Device, io: Io) {
    st := dev.state.Mut()
    match mut st.native {
        .Vk(v) { _ = v.WaitQueueIdle(io) else {} }
        _ {}
    }
    if !st.lost {
        last := st.serials.LastSubmitted()
        match mut st.native {
            .Stub(s) { _ = s.WaitSerial(last) else {} }
            .Dx(d) { _ = d.WaitQueueIdle(io, mut st.serials.next) else {} }
            .Vk(v: _) {}
        }
        st.serials.Complete(st.serials.LastSubmitted())
        SyncDxMessages(dev.heap.Alloc(), st)
    }
}

/// Waits for the GPU (not cancellable; after device loss the wait ends
/// at once), releases every object the device still owns, and the
/// device. O(objects); never fails.
export Device.Close(self, io: Io): Leaks {
    var dev := self
    _ = dev.FlushPreSubmit() else {}
    WaitBeforeClose(mut dev, io.NoCancel())
    Device(heap, serial: _, state, cell: _) := dev
    a := heap.Alloc()
    st := state.Take(a)
    var errs := st.err_count
    var warns := st.warn_count
    var aname := ""
    var is_vk := false
    match &st.native {
        .Vk(v) {
            is_vk = true
            aname = st.info.name
            cnts := v.DrainDebugCounts()
            errs += cnts.errs
            warns += cnts.warns
        }
        _ {}
    }
    native_leaks := st.FreeAll(a)
    if is_vk {
        vk.FreeOpenAdapterName(heap, aname)
    }
    return Leaks(native: native_leaks, errors: errs, warnings: warns)
}

/// As Close, without the report: the error-path ending. Never fails.
export Device.Abandon(self, io: Io) {
    _ = self.Close(io)
}

Device.CheckAlive(&self): Error!void {
    st := self.state.Ref()
    match st.sticky_err {
        some(e) { fail e }
        none {}
    }
    if st.lost {
        fail DeviceLost(reason: LossReason.Unknown)
    }
}

Device.SetLost(mut self, reason: LossReason) {
    st := self.state.Mut()
    st.lost = true
    match st.sticky_err {
        none { st.sticky_err = some(DeviceLost(reason: reason)) }
        some(_) {}
    }
}

Device.FlushPreSubmit(mut self): Error!void {
    st := self.state.Mut()
    has_native_pre := match &st.native {
        .Dx(d) { d.HasPendingPreSubmit() }
        .Vk(v) { v.HasPendingPreSubmit() }
        .Stub(s: _) { false }
    }
    if st.pre_submit.IsEmpty() && !has_native_pre {
        return
    }
    serial := st.serials.Advance()
    a := self.heap.Alloc()
    match mut st.native {
        .Stub(s) {
            try s.SubmitStream(&st.pre_submit, serial)
        }
        .Dx(d) {
            d.FlushPreSubmit(serial) else e {
                SyncDxMessages(a, st)
                ge := MapDxError(e)
                RecordDxFailure(st, ge)
                st.pre_submit.RecycleToPool(a, mut st.chunk_pool, serial, false)
                fail ge
            }
            SyncDxMessages(a, st)
        }
        .Vk(v) {
            v.FlushPreSubmit(serial) else e {
                match e {
                    vk.DeviceLost(reason: r) {
                        st.lost = true
                        st.sticky_err = some(DeviceLost(reason: MapVkLossReason(r)))
                    }
                    _ {}
                }
                fail MapVkErr(e)
            }
        }
    }
    st.pre_submit.RecycleToPool(a, mut st.chunk_pool, serial, false)
}

Device.DrainCompleted(mut self) {
    a := self.heap.Alloc()
    for {
        st := self.state.Mut()
        comp := st.serials.completed
        st.chunk_pool.Recycle(a, comp)
        item_opt := st.deferred.PopReady(comp)
        match item_opt {
            none { return }
            some(item) {
                self.ReleaseDeferredItem(item)
            }
        }
    }
}

Device.ReleaseDeferredItem(mut self, item: DeferredItem) {
    match item.kind {
        DefBuffer { self.DestroyBufferSlot(item.slot) }
        DefTexture { self.DestroyTextureSlot(item.slot) }
        DefView { self.DestroyViewSlot(item.slot) }
        DefSampler { self.DestroySamplerSlot(item.slot) }
        DefBindGroupLayout { self.DestroyBgLayoutSlot(item.slot) }
        DefPipelineLayout { self.DestroyPipeLayoutSlot(item.slot) }
        DefBindGroup { self.DestroyBindGroupSlot(item.slot) }
        DefShader { self.DestroyShaderSlot(item.slot) }
        DefRenderPipeline { self.DestroyRenderPipeSlot(item.slot) }
        DefComputePipeline { self.DestroyComputePipeSlot(item.slot) }
        _ {}
    }
}

DecRefReady(st: mut DeviceState, kind: u8, slot: u32): (ready: bool, lu: u64) {
    match kind {
        DefBuffer {
            if st.buffers.DecRef(slot) {
                return (ready: true, lu: st.buffers.LastUse(slot))
            }
        }
        DefTexture {
            if st.textures.DecRef(slot) {
                return (ready: true, lu: st.textures.LastUse(slot))
            }
        }
        DefView {
            if st.views.DecRef(slot) {
                return (ready: true, lu: st.views.LastUse(slot))
            }
        }
        DefSampler {
            if st.samplers.DecRef(slot) {
                return (ready: true, lu: st.samplers.LastUse(slot))
            }
        }
        DefBindGroupLayout {
            if st.bg_layouts.DecRef(slot) {
                return (ready: true, lu: st.bg_layouts.LastUse(slot))
            }
        }
        DefPipelineLayout {
            if st.pipe_layouts.DecRef(slot) {
                return (ready: true, lu: st.pipe_layouts.LastUse(slot))
            }
        }
        DefBindGroup {
            if st.bind_groups.DecRef(slot) {
                return (ready: true, lu: st.bind_groups.LastUse(slot))
            }
        }
        DefShader {
            if st.shaders.DecRef(slot) {
                return (ready: true, lu: st.shaders.LastUse(slot))
            }
        }
        _ {}
    }
    return (ready: false, lu: 0)
}

Device.DecRefAndQueue(mut self, kind: u8, slot: u32) {
    if slot == 0 {
        return
    }
    (ready, lu) := DecRefReady(self.state.Mut(), kind, slot)
    if ready {
        self.QueueOrRelease(kind, slot, lu)
    }
}

Device.QueueOrRelease(mut self, kind: u8, slot: u32, last_use: u64) {
    st := self.state.Mut()
    if last_use <= st.serials.completed {
        self.ReleaseDeferredItem(DeferredItem(serial: last_use, kind: kind, slot: slot))
        return
    }
    a := self.heap.Alloc()
    // If bookkeeping cannot grow, the device still owns the slot until Close.
    st.deferred.Push(a, DeferredItem(serial: last_use, kind: kind, slot: slot)) else {}
}

Device.SetPlantedBug(mut self, bug: PlantedBug) {
    st := self.state.Mut()
    st.planted_bug = bug
    vbug := match bug {
        .None { vk.BugNone }
        .SkipPassBarrier { vk.BugSkipPassBarrier }
        .SkipComputeUavBarrier { vk.BugSkipGlobalBarrier }
        .SkipDecay | .SkipDirtyGroupOnLayout { vk.BugNone }
    }
    match mut st.native {
        .Vk(v) { v.SetPlantedBug(vbug) }
        _ {}
    }
}

Device.TriggerLossForTest(mut self) {
    match mut self.state.Mut().native {
        .Dx(d) { d.SimulateDeviceLoss() }
        .Vk(v) { v.SetFaultNextSubmit() }
        _ {}
    }
}
