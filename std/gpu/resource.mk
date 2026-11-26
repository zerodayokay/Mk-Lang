// Why: buffers, textures, views and samplers are the data a GPU program
// reads and writes; they outlive frames and move between tasks.
//
// What: BufferUsage, Buffer, TextureUsage, TextureDesc, Texture,
// ViewDimension, Aspect, ViewDesc, TextureView, Filter, Address, Compare,
// SamplerDesc, Sampler, and their Device creation and Close methods.
//
// How: handles are generation-checked (slot, generation, device serial)
// triples; usage, format capability, twin view format, and anisotropy rules
// are validated at creation before allocating native objects.

/// What a buffer may be used for. Start from NoBufferUsage and set fields.
export struct BufferUsage {
    export vertex: bool
    export index: bool
    /// Bound to a non-dynamic Uniform entry.
    export uniform: bool
    /// Bound to a dynamic Uniform entry. The D3D12 resource is 64 KiB larger
    /// than asked (zeroed, never addressable through the API).
    export uniform_dynamic: bool
    export storage: bool
    export copy_src: bool
    export copy_dst: bool
    /// Readable by MapRead; only with copy_dst.
    export map_read: bool
    /// Writable by MapWrite; only with copy_src.
    export map_write: bool
    sealed: u8
}

/// No usage at all.
export const NoBufferUsage: BufferUsage = BufferUsage(vertex: false, index: false, uniform: false, uniform_dynamic: false, storage: false, copy_src: false, copy_dst: false, map_read: false, map_write: false, sealed: 0)

/// A GPU buffer its device owns. Linear: end it with Close.
export linear struct Buffer {
    slot: u32
    generation: u32
    device: u32
}

struct BufferEntry {
    size: int
    usage: BufferUsage
    state: BufState
    native_handle: u64
}

ValidateBufferUsage(u: &BufferUsage): Invalid!void {
    any := u.vertex || u.index || u.uniform || u.uniform_dynamic || u.storage || u.copy_src || u.copy_dst || u.map_read || u.map_write
    if !any {
        fail Invalid(misuse: Misuse.BadUsage, at: -1)
    }
    if u.map_read {
        if !u.copy_dst || u.vertex || u.index || u.uniform || u.uniform_dynamic || u.storage || u.copy_src || u.map_write {
            fail Invalid(misuse: Misuse.BadUsage, at: -1)
        }
    }
    if u.map_write {
        if !u.copy_src || u.vertex || u.index || u.uniform || u.uniform_dynamic || u.storage || u.copy_dst || u.map_read {
            fail Invalid(misuse: Misuse.BadUsage, at: -1)
        }
    }
}

/// Makes a zeroed buffer of size bytes. Errors: Invalid(BadUsage) for a
/// usage set the rules forbid, Invalid(LimitExceeded), OutOfDeviceMemory,
/// DeviceLost. O(1) driver calls; may allocate a memory block; allocates a
/// slot entry from the device's heap when its table grows.
export Device.NewBuffer(mut self, size: int, usage: BufferUsage, label: str): Error!Buffer {
    try self.CheckAlive()
    try ValidateBufferUsage(&usage)
    if size <= 0 || size > self.Limits().max_buffer_size {
        fail Invalid(misuse: Misuse.LimitExceeded, at: -1)
    }
    nh := try self.NativeCreateBuffer(size, &usage, label)
    a := self.heap.Alloc()
    onerr self.state.Mut().native.DestroyBuffer(a, nh)
    fixed := usage.map_read || usage.map_write
    fixed_st := if usage.map_read { ResCopyDst } else if usage.map_write { ResCopySrc } else { ResCommon }
    entry := BufferEntry(size: size, usage: usage, state: BufState.New(fixed, fixed_st), native_handle: nh)
    match self.state.Mut().buffers.Alloc(a, entry) {
        .Ok(slot, gen_) {
            st := self.state.Mut()
            match &st.native {
                .Dx(d) {
                    if d.HasPendingPreSubmit() {
                        st.buffers.StampUse(slot, st.serials.next)
                    }
                }
                _ {}
            }
            return Buffer(slot: slot, generation: gen_, device: self.serial)
        }
        .Oom(val: _) {
            self.state.Mut().native.DestroyBuffer(a, nh)
            fail OutOfMemory
        }
    }
}

/// The size NewBuffer was given. O(1).
export Device.BufferSize(&self, b: &Buffer): int {
    if b.device != self.serial {
        return 0
    }
    st := self.state.Ref()
    if !st.buffers.IsLive(b.slot, b.generation) {
        return 0
    }
    return st.buffers.Get(b.slot).size
}

/// Gives the buffer back; the native buffer goes when the GPU and every
/// bind group are done with it. Panics (code 12) given another device. O(1).
export Buffer.Close(self, dev: mut Device) {
    Buffer(slot, generation, device) := self
    if device != dev.serial {
        Panic("gpu: a handle closed with another device")
    }
    dev.CloseBufferSlot(slot, generation)
}

Device.CloseBufferSlot(mut self, slot: u32, gen_: u32) {
    st := self.state.Mut()
    if !st.buffers.MarkUserClosed(slot, gen_) {
        return
    }
    lu := st.buffers.LastUse(slot)
    self.QueueOrRelease(DefBuffer, slot, lu)
}

Device.DestroyBufferSlot(mut self, slot: u32) {
    a := self.heap.Alloc()
    st := self.state.Mut()
    match st.buffers.Release(a, slot) {
        some(e) { st.native.DestroyBuffer(a, e.native_handle) }
        none {}
    }
}

/// What a texture may be used for. Start from NoTextureUsage.
export struct TextureUsage {
    export copy_src: bool
    export copy_dst: bool
    export sampled: bool
    export render_target: bool
    sealed: u8
}

/// No usage at all.
export const NoTextureUsage: TextureUsage = TextureUsage(copy_src: false, copy_dst: false, sampled: false, render_target: false, sealed: 0)

/// A two-dimensional texture, an array of them, or a cube. Start from
/// DefaultTextureDesc.
export struct TextureDesc {
    export format: TextureFormat
    export width: int
    export height: int
    /// Layers: 1, or 6 and more for a cube (width == height).
    export layers: int
    export mips: int
    /// 1 or 4.
    export samples: int
    export usage: TextureUsage
    /// Other formats views of it may take (sRGB or linear twins only).
    export view_formats: []TextureFormat
    export label: str
    sealed: u8
}

/// A 1 by 1 Rgba8Unorm texture, one layer, one mip, one sample, sampled
/// and copy_dst.
export const DefaultTextureDesc: TextureDesc = TextureDesc(format: TextureFormat.Rgba8Unorm, width: 1, height: 1, layers: 1, mips: 1, samples: 1, usage: TextureUsage(copy_src: false, copy_dst: true, sampled: true, render_target: false, sealed: 0), view_formats: [], label: "", sealed: 0)

/// A texture its device owns. Linear: end it with Close.
export linear struct Texture {
    slot: u32
    generation: u32
    device: u32
}

struct TextureEntry {
    format: TextureFormat
    width: int
    height: int
    layers: int
    mips: int
    samples: int
    usage: TextureUsage
    view_formats: HeapList[TextureFormat]
    state: TexState
    native_handle: u64
}

TextureEntry.Free(self, a: &Alloc) {
    TextureEntry(format: _, width: _, height: _, layers: _, mips: _, samples: _, usage: _, view_formats, state, native_handle: _) := self
    view_formats.Free(a)
    state.Free(a)
}

MaxMipsFor(w: int, h: int): int {
    var m := if w > h { w } else { h }
    var count := 1
    for m > 1 {
        m >>= 1
        count += 1
    }
    return count
}

/// Makes a zeroed texture by d. Errors: Invalid(FormatCapability) for a
/// usage the format lacks on this device, Invalid(LimitExceeded),
/// OutOfDeviceMemory, DeviceLost. O(1) driver calls.
export Device.NewTexture(mut self, d: TextureDesc): Error!Texture {
    try self.CheckAlive()
    lim := self.Limits()
    if d.width <= 0 || d.height <= 0 || d.layers <= 0 || d.mips <= 0 {
        fail Invalid(misuse: Misuse.LimitExceeded, at: -1)
    }
    if d.width > lim.max_texture_size || d.height > lim.max_texture_size || d.layers > lim.max_texture_layers || d.mips > 15 || d.mips > MaxMipsFor(d.width, d.height) {
        fail Invalid(misuse: Misuse.LimitExceeded, at: -1)
    }
    if d.samples != 1 && d.samples != 4 {
        fail Invalid(misuse: Misuse.SamplesMismatch, at: -1)
    }
    if d.samples == 4 && (d.mips != 1 || d.layers != 1) {
        fail Invalid(misuse: Misuse.SamplesMismatch, at: -1)
    }
    u := &d.usage
    if !u.copy_src && !u.copy_dst && !u.sampled && !u.render_target {
        fail Invalid(misuse: Misuse.BadUsage, at: -1)
    }
    row := RowOf(d.format)
    if row.needs_bc && !self.Features().texture_compression_bc {
        fail Invalid(misuse: Misuse.FormatCapability, at: -1)
    }
    if row.is_compressed {
        if d.samples != 1 {
            fail Invalid(misuse: Misuse.FormatCapability, at: -1)
        }
        if d.width % int(row.block_w) != 0 || d.height % int(row.block_h) != 0 {
            fail Invalid(misuse: Misuse.ExtentMismatch, at: -1)
        }
    }
    caps := self.FormatCaps(d.format)
    if u.sampled && !caps.sampled {
        fail Invalid(misuse: Misuse.FormatCapability, at: -1)
    }
    if u.render_target {
        if IsDepthStencilFormat(d.format) {
            if !caps.depth_stencil {
                fail Invalid(misuse: Misuse.FormatCapability, at: -1)
            }
        } else if !caps.render_target {
            fail Invalid(misuse: Misuse.FormatCapability, at: -1)
        }
    }
    if d.samples == 4 && !caps.multisample {
        fail Invalid(misuse: Misuse.FormatCapability, at: -1)
    }
    for i in 0..d.view_formats.Len() {
        if !IsTwinFormat(d.format, d.view_formats[i]) {
            fail Invalid(misuse: Misuse.FormatMismatch, at: i)
        }
    }

    a := self.heap.Alloc()
    var vfs := HeapList[TextureFormat].New(a)
    onerr vfs.Free(a)
    for i in 0..d.view_formats.Len() {
        try vfs.Push(a, d.view_formats[i])
    }
    nh := try self.NativeCreateTexture(&d)
    onerr self.state.Mut().native.DestroyTexture(nh)
    planes := PlaneCount(d.format)
    entry := TextureEntry(
        format: d.format,
        width: d.width,
        height: d.height,
        layers: d.layers,
        mips: d.mips,
        samples: d.samples,
        usage: d.usage,
        view_formats: vfs,
        state: TexState.New(a, d.mips, d.layers, planes, ResCommon),
        native_handle: nh,
    )
    match self.state.Mut().textures.Alloc(a, entry) {
        .Ok(slot, gen_) {
            st := self.state.Mut()
            match &st.native {
                .Dx(d: dx_dev) {
                    if dx_dev.HasPendingPreSubmit() {
                        st.textures.StampUse(slot, st.serials.next)
                    }
                }
                _ {}
            }
            return Texture(slot: slot, generation: gen_, device: self.serial)
        }
        .Oom(val) {
            val.Free(a)
            self.state.Mut().native.DestroyTexture(nh)
            fail OutOfMemory
        }
    }
}

/// Gives the texture back, as Buffer.Close. O(1).
export Texture.Close(self, dev: mut Device) {
    Texture(slot, generation, device) := self
    if device != dev.serial {
        Panic("gpu: a handle closed with another device")
    }
    dev.CloseTextureSlot(slot, generation)
}

Device.CloseTextureSlot(mut self, slot: u32, gen_: u32) {
    st := self.state.Mut()
    if !st.textures.MarkUserClosed(slot, gen_) {
        return
    }
    lu := st.textures.LastUse(slot)
    self.QueueOrRelease(DefTexture, slot, lu)
}

Device.DestroyTextureSlot(mut self, slot: u32) {
    a := self.heap.Alloc()
    st := self.state.Mut()
    match st.textures.Release(a, slot) {
        some(e) {
            nh := e.native_handle
            e.Free(a)
            st.native.DestroyTexture(nh)
        }
        none {}
    }
}

/// How a view sees its texture.
export open enum ViewDimension {
    D2
    D2Array
    Cube
    CubeArray
}

/// The part of a depth-stencil format a view or copy covers.
export open enum Aspect {
    All
    Depth
    Stencil
}

/// A view's format, dimension and subresource range. Start from
/// DefaultViewDesc.
export struct ViewDesc {
    /// none: the texture's format; else one of its view_formats.
    export format: ?TextureFormat
    /// none: D2 for one layer, D2Array for several.
    export dimension: ?ViewDimension
    export aspect: Aspect
    export first_mip: int
    /// none: every mip from first_mip.
    export mips: ?int
    export first_layer: int
    /// none: every layer from first_layer.
    export layers: ?int
    sealed: u8
}

/// The whole texture in its own format.
export const DefaultViewDesc: ViewDesc = ViewDesc(format: none, dimension: none, aspect: Aspect.All, first_mip: 0, mips: none, first_layer: 0, layers: none, sealed: 0)

/// A view of a texture. The texture lives while the view does.
export linear struct TextureView {
    slot: u32
    generation: u32
    device: u32
}

struct ViewEntry {
    tex_slot: u32
    format: TextureFormat
    dim: ViewDimension
    aspect: Aspect
    first_mip: int
    mip_count: int
    first_layer: int
    layer_count: int
    width: int
    height: int
    samples: int
    usage: TextureUsage
    plane_mask: u8
    is_swapchain: bool
    native_handle: u64
}

AspectMaskFor(fmt: TextureFormat, aspect: Aspect): Invalid!u8 {
    row := RowOf(fmt)
    if !IsDepthStencilFormat(fmt) {
        match aspect {
            .All { return 1 }
            _ { fail Invalid(misuse: Misuse.BadView, at: -1) }
        }
    }
    if (row.aspects & AspectBitStencil) == 0 {
        match aspect {
            .All | .Depth { return 1 }
            .Stencil { fail Invalid(misuse: Misuse.BadView, at: -1) }
        }
    }
    return match aspect {
        .All { 3 }
        .Depth { 1 }
        .Stencil { 2 }
    }
}

ValidateViewDesc(st: &DeviceState, t: &Texture, d: &ViewDesc): Invalid!ViewEntry {
    if !st.textures.IsLive(t.slot, t.generation) {
        fail Invalid(misuse: Misuse.StaleHandle, at: -1)
    }
    tex := st.textures.Get(t.slot)
    view_fmt := match d.format {
        none { tex.format }
        some(f) {
            var listed := FormatIndex(f) == FormatIndex(tex.format)
            for i in 0..tex.view_formats.Len() {
                if FormatIndex(tex.view_formats[i]) == FormatIndex(f) {
                    listed = true
                }
            }
            if !listed {
                fail Invalid(misuse: Misuse.ViewFormatNotListed, at: -1)
            }
            f
        }
    }
    if d.first_mip < 0 || d.first_mip >= tex.mips {
        fail Invalid(misuse: Misuse.BadView, at: -1)
    }
    mip_count := match d.mips {
        some(m) { m }
        none { tex.mips - d.first_mip }
    }
    if mip_count <= 0 || d.first_mip + mip_count > tex.mips {
        fail Invalid(misuse: Misuse.BadView, at: -1)
    }
    if d.first_layer < 0 || d.first_layer >= tex.layers {
        fail Invalid(misuse: Misuse.BadView, at: -1)
    }
    layer_count := match d.layers {
        some(l) { l }
        none { tex.layers - d.first_layer }
    }
    if layer_count <= 0 || d.first_layer + layer_count > tex.layers {
        fail Invalid(misuse: Misuse.BadView, at: -1)
    }
    dim := match d.dimension {
        some(dm) { dm }
        none { if tex.layers == 1 { ViewDimension.D2 } else { ViewDimension.D2Array } }
    }
    match dim {
        .D2 {
            if layer_count != 1 {
                fail Invalid(misuse: Misuse.BadView, at: -1)
            }
        }
        .D2Array {
            if tex.samples != 1 {
                fail Invalid(misuse: Misuse.BadView, at: -1)
            }
        }
        .Cube {
            if tex.width != tex.height || layer_count != 6 || tex.samples != 1 {
                fail Invalid(misuse: Misuse.BadView, at: -1)
            }
        }
        .CubeArray {
            if tex.width != tex.height || layer_count < 6 || layer_count % 6 != 0 || tex.samples != 1 {
                fail Invalid(misuse: Misuse.BadView, at: -1)
            }
        }
    }
    plane_mask := try AspectMaskFor(tex.format, d.aspect)
    return ViewEntry(
        tex_slot: t.slot,
        format: view_fmt,
        dim: dim,
        aspect: d.aspect,
        first_mip: d.first_mip,
        mip_count: mip_count,
        first_layer: d.first_layer,
        layer_count: layer_count,
        width: MipDim(tex.width, d.first_mip),
        height: MipDim(tex.height, d.first_mip),
        samples: tex.samples,
        usage: tex.usage,
        plane_mask: plane_mask,
        is_swapchain: false,
        native_handle: 0,
    )
}

/// Makes a view of t. Errors: Invalid(BadView), Invalid(ViewFormatNotListed),
/// StaleHandle. O(1); writes descriptors.
export Device.NewView(mut self, t: &Texture, d: ViewDesc): Error!TextureView {
    try self.CheckAlive()
    if t.device != self.serial {
        fail Invalid(misuse: Misuse.StaleHandle, at: -1)
    }
    var entry := try ValidateViewDesc(self.state.Ref(), t, &d)
    nh := try self.NativeCreateView(&entry)
    entry.native_handle = nh
    a := self.heap.Alloc()
    match self.state.Mut().views.Alloc(a, entry) {
        .Ok(slot, gen_) {
            self.state.Mut().textures.IncRef(t.slot)
            return TextureView(slot: slot, generation: gen_, device: self.serial)
        }
        .Oom(val: _) {
            self.state.Mut().native.DestroyView(nh)
            fail OutOfMemory
        }
    }
}

/// Gives the view back. O(1).
export TextureView.Close(self, dev: mut Device) {
    TextureView(slot, generation, device) := self
    if device != dev.serial {
        Panic("gpu: a handle closed with another device")
    }
    dev.CloseViewSlot(slot, generation)
}

Device.CloseViewSlot(mut self, slot: u32, gen_: u32) {
    st := self.state.Mut()
    if !st.views.MarkUserClosed(slot, gen_) {
        return
    }
    lu := st.views.LastUse(slot)
    self.QueueOrRelease(DefView, slot, lu)
}

Device.ReleaseViewRef(mut self, slot: u32): u32 {
    a := self.heap.Alloc()
    st := self.state.Mut()
    match st.views.Release(a, slot) {
        some(e) {
            st.native.DestroyView(e.native_handle)
            return e.tex_slot
        }
        none { return 0 }
    }
}

Device.DestroyViewSlot(mut self, slot: u32) {
    tex_slot := self.ReleaseViewRef(slot)
    if tex_slot != 0 {
        st := self.state.Mut()
        if st.textures.DecRef(tex_slot) {
            lu := st.textures.LastUse(tex_slot)
            self.QueueOrRelease(DefTexture, tex_slot, lu)
        }
    }
}

/// Texel filtering.
export open enum Filter {
    Nearest
    Linear
}

/// What a sampler reads outside 0..1.
export open enum Address {
    Repeat
    MirrorRepeat
    ClampToEdge
}

/// A comparison function for depth, stencil and comparison samplers.
export open enum Compare {
    Never
    Less
    Equal
    LessEqual
    Greater
    NotEqual
    GreaterEqual
    Always
}

/// How a sampler filters and addresses. Start from DefaultSamplerDesc.
export struct SamplerDesc {
    export min: Filter
    export mag: Filter
    export mip: Filter
    export address_u: Address
    export address_v: Address
    export address_w: Address
    export lod_min: f32
    export lod_max: f32
    /// 1 to 16; above 1 needs every filter Linear and sampler_anisotropy.
    export anisotropy: int
    /// some: a comparison sampler for shadow maps.
    export compare: ?Compare
    sealed: u8
}

/// Linear filtering, clamp to edge, every mip, no anisotropy, no compare.
export const DefaultSamplerDesc: SamplerDesc = SamplerDesc(min: Filter.Linear, mag: Filter.Linear, mip: Filter.Linear, address_u: Address.ClampToEdge, address_v: Address.ClampToEdge, address_w: Address.ClampToEdge, lod_min: 0.0, lod_max: 32.0, anisotropy: 1, compare: none, sealed: 0)

/// A sampler its device owns.
export linear struct Sampler {
    slot: u32
    generation: u32
    device: u32
}

struct SamplerEntry {
    comparison: bool
    native_handle: u64
}

/// Makes a sampler. Errors: Invalid(BadUsage) (anisotropy rules),
/// Invalid(LimitExceeded) (the 2,048 sampler descriptors). O(1).
export Device.NewSampler(mut self, d: SamplerDesc): Error!Sampler {
    try self.CheckAlive()
    if d.anisotropy < 1 || d.anisotropy > 16 {
        fail Invalid(misuse: Misuse.BadUsage, at: -1)
    }
    if d.anisotropy > 1 {
        if d.min != Filter.Linear || d.mag != Filter.Linear || d.mip != Filter.Linear || !self.Features().sampler_anisotropy {
            fail Invalid(misuse: Misuse.BadUsage, at: -1)
        }
    }
    if d.lod_min < 0.0 || d.lod_max < d.lod_min {
        fail Invalid(misuse: Misuse.BadUsage, at: -1)
    }
    if self.state.Ref().samplers.LiveCount() >= self.Limits().max_sampler_descriptors {
        fail Invalid(misuse: Misuse.LimitExceeded, at: -1)
    }
    nh := try self.NativeCreateSampler(&d)
    a := self.heap.Alloc()
    match self.state.Mut().samplers.Alloc(a, SamplerEntry(comparison: d.compare.IsSome(), native_handle: nh)) {
        .Ok(slot, gen_) { return Sampler(slot: slot, generation: gen_, device: self.serial) }
        .Oom(val: _) {
            self.state.Mut().native.DestroySampler(nh)
            fail OutOfMemory
        }
    }
}

/// Gives the sampler back. O(1).
export Sampler.Close(self, dev: mut Device) {
    Sampler(slot, generation, device) := self
    if device != dev.serial {
        Panic("gpu: a handle closed with another device")
    }
    dev.CloseSamplerSlot(slot, generation)
}

Device.CloseSamplerSlot(mut self, slot: u32, gen_: u32) {
    st := self.state.Mut()
    if !st.samplers.MarkUserClosed(slot, gen_) {
        return
    }
    lu := st.samplers.LastUse(slot)
    self.QueueOrRelease(DefSampler, slot, lu)
}

Device.DestroySamplerSlot(mut self, slot: u32) {
    a := self.heap.Alloc()
    st := self.state.Mut()
    match st.samplers.Release(a, slot) {
        some(e) { st.native.DestroySampler(e.native_handle) }
        none {}
    }
}
