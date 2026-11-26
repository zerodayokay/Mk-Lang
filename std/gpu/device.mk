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

/// Lists the adapters of backend (Auto: every backend present), best first.
/// An adapter missing a requirement or a guaranteed format capability is
/// not listed. Errors: Unsupported off Windows; Canceled while queued.
/// O(adapters × formats) driver queries on the blocking pool; allocates
/// the list and the names in a.
export Adapters(a: &Arena, io: Io, backend: Backend): Error!List[AdapterInfo] from a {
    var out := List[AdapterInfo].New(a)
    if backend == Backend.D3d12 {
        fail Unsupported
    }
    if backend == Backend.Vulkan && !vk.IsAvailable(io) {
        fail Unsupported
    }
    if backend == Backend.Auto || backend == Backend.Vulkan {
        if vk.IsAvailable(io) {
            vk_list := vk.EnumAdapters(a, io) else e {
                fail MapVkErr(e)
            }
            for i in 0..vk_list.Len() {
                try out.Push(AdapterInfoFromVk(&vk_list[i], Backend.Vulkan))
            }
        }
    }
    if out.Len() == 0 && !vk.IsAvailable(io) {
        fail Unsupported
    }
    return out
}

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
        native: Native.Vk(v: vdev),
        sticky_err: none,
        lost: false,
        depth_bias_clamp_ok: vad.depth_bias_clamp_ok,
        draw_params_ok: vad.draw_params_ok,
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
        vk.FreeOpenAdapterName(heap, info.name)
        _ = st.FreeAll(a)
        fail e
    }
    return Device(heap: heap, serial: 1, state: box, cell: Cell[u8].New(0))
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
    native: Native
    sticky_err: ?Error
    lost: bool
    depth_bias_clamp_ok: bool
    draw_params_ok: bool
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
}

/// A device and its one queue. Linear: end it with Close. Send, not Share.
export linear view struct Device {
    heap: &Heap
    serial: u32
    state: Box[DeviceState]
    cell: Cell[u8]
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

/// Makes a device by o. Errors: Unsupported (target), NoAdapter,
/// Missing(feature), DeviceLost, OutOfDeviceMemory, OutOfMemory, Canceled
/// (only while queued). Runs creation and the format queries through
/// runtime.Blocking. Allocates the device's tables from heap.
export Open(heap: &Heap, io: Io, o: DeviceOptions): Error!Device from heap {
    try ValidateDeviceOptions(&o)
    match o.backend {
        .D3d12 {
            _ = dx.Open() else { fail Unsupported }
        }
        .Vulkan {
            return try OpenVkDevice(heap, io, &o)
        }
        .Auto {
            _ = dx.Open() else {
                return try OpenVkDevice(heap, io, &o)
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
    last := st.serials.LastSubmitted()
    match mut st.native {
        .Stub(s) {
            try s.WaitSerial(last)
        }
        .Dx(d: _) {
            fail Unsupported
        }
        .Vk(v) {
            v.WaitSerial(io, last) else e {
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
        native,
        sticky_err: _,
        lost: _,
        depth_bias_clamp_ok: _,
        draw_params_ok: _,
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
    ) := self

    stream.FreeAll(a)
    pre_submit.FreeAll(a)
    chunk_pool.FreeAll(a)
    deferred.Free(a)
    pass_shadow.FreeAll(a)
    messages.Free(a)
    msg_bytes.Free(a)

    var nat := native
    var swapchains := swapchains0
    for {
        match swapchains.DrainPop(a) {
            some(e) {
                SwapchainEntry(info: _, view_slot: _, view_gen: _, native_handle, surf) := e
                match surf {
                    some(sf) { sf.Close() }
                    none {}
                }
                nat.DestroySwapchain(a, native_handle)
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
            some(e) { nat.DestroyView(e.native_handle) }
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

/// Waits for the GPU (not cancellable; after device loss the wait ends
/// at once), releases every object the device still owns, and the
/// device. O(objects); never fails.
export Device.Close(self, io: Io): Leaks {
    var dev := self
    _ = dev.WaitIdle(io) else {}
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
    var has_native_pre := false
    match &st.native {
        .Vk(v) { has_native_pre = v.HasPendingPreSubmit() }
        _ {}
    }
    if st.pre_submit.IsEmpty() && !has_native_pre {
        return
    }
    serial := st.serials.Advance()
    match mut st.native {
        .Stub(s) {
            try s.SubmitStream(&st.pre_submit, serial)
        }
        .Dx(d: _) {
            fail Unsupported
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
    a := self.heap.Alloc()
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
    st.deferred.Push(a, DeferredItem(serial: last_use, kind: kind, slot: slot)) else {
        self.ReleaseDeferredItem(DeferredItem(serial: last_use, kind: kind, slot: slot))
    }
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
