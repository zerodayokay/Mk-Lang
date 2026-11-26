// Why: a pipeline fixes the shaders and fixed-function state a draw or
// dispatch runs with, and verifies shader interface compatibility against the
// pipeline layout before driver compilation.
//
// What: StepMode, VertexAttribute, VertexLayout, Topology, Cull, FrontFace,
// BlendFactor, BlendOp, BlendComponent, Blend, BlendOver, ColorWrites,
// WriteAll, ColorTarget, ColorOf, StencilOp, StencilFace, Stencil, DepthBias,
// DepthStencil, DepthOf, RenderPipelineDesc, DefaultRenderPipeline,
// RenderPipeline, ComputePipeline, and their Device calls.
//
// How: checks vertex layouts, target/depth capabilities, workgroup limits,
// and SPIR-V shader bindings/blocks/inputs against the pipeline layout.

/// How a vertex buffer steps.
export open enum StepMode {
    Vertex
    Instance
}

/// One vertex attribute: HLSL semantic LOC<location>, [[vk::location]].
export struct VertexAttribute {
    export location: int
    export format: VertexFormat
    export offset: int
}

/// The layout of one vertex buffer slot.
export struct VertexLayout {
    export stride: int
    export step: StepMode
    export attributes: []VertexAttribute
}

/// Primitive topology.
export open enum Topology {
    PointList
    LineList
    LineStrip
    TriangleList
    TriangleStrip
}

/// Which faces are culled.
export open enum Cull {
    None
    Front
    Back
}

/// Which winding is the front, in framebuffer space with Y down.
export open enum FrontFace {
    Ccw
    Cw
}

/// A blend factor.
export open enum BlendFactor {
    Zero
    One
    Src
    OneMinusSrc
    SrcAlpha
    OneMinusSrcAlpha
    Dst
    OneMinusDst
    DstAlpha
    OneMinusDstAlpha
    Constant
    OneMinusConstant
}

/// A blend operation.
export open enum BlendOp {
    Add
    Subtract
    ReverseSubtract
    Min
    Max
}

/// One blend equation.
export struct BlendComponent {
    export src: BlendFactor
    export dst: BlendFactor
    export op: BlendOp
}

/// Colour and alpha blend equations.
export struct Blend {
    export color: BlendComponent
    export alpha: BlendComponent
}

/// Premultiplied-alpha "over".
export const BlendOver: Blend = Blend(color: BlendComponent(src: BlendFactor.One, dst: BlendFactor.OneMinusSrcAlpha, op: BlendOp.Add), alpha: BlendComponent(src: BlendFactor.One, dst: BlendFactor.OneMinusSrcAlpha, op: BlendOp.Add))

/// Channels a target writes.
export struct ColorWrites {
    export r: bool
    export g: bool
    export b: bool
    export a: bool
}

/// Every channel.
export const WriteAll: ColorWrites = ColorWrites(r: true, g: true, b: true, a: true)

/// One colour target of a pipeline.
export struct ColorTarget {
    export format: TextureFormat
    export blend: ?Blend
    export write: ColorWrites
    sealed: u8
}

/// A target of format with no blending, writing every channel. O(1).
export ColorOf(format: TextureFormat): ColorTarget {
    return ColorTarget(format: format, blend: none, write: WriteAll, sealed: 0)
}

/// A stencil operation.
export open enum StencilOp {
    Keep
    Zero
    Replace
    IncrementClamp
    DecrementClamp
    Invert
    IncrementWrap
    DecrementWrap
}

/// Stencil state of one face.
export struct StencilFace {
    export compare: Compare
    export on_fail: StencilOp
    export on_depth_fail: StencilOp
    export on_pass: StencilOp
}

/// Stencil state: two faces, one read mask and one write mask for both
/// (D3D12 has one pair).
export struct Stencil {
    export front: StencilFace
    export back: StencilFace
    export read_mask: u8
    export write_mask: u8
}

/// Depth bias. clamp other than 0 needs Vulkan's depthBiasClamp (refused
/// as Unsupported where absent).
export struct DepthBias {
    export constant: i32
    export slope: f32
    export clamp: f32
}

/// Depth and stencil state of a pipeline.
export struct DepthStencil {
    export format: TextureFormat
    export write: bool
    export compare: Compare
    export stencil: ?Stencil
    export bias: DepthBias
    sealed: u8
}

/// Depth testing with compare, writing depth, no stencil, no bias. O(1).
export DepthOf(format: TextureFormat, compare: Compare): DepthStencil {
    return DepthStencil(format: format, write: true, compare: compare, stencil: none, bias: DepthBias(constant: 0, slope: 0.0, clamp: 0.0), sealed: 0)
}

/// Everything a render pipeline fixes besides its shaders and layout.
/// Start from DefaultRenderPipeline and assign the fields you change.
export struct RenderPipelineDesc {
    export vertex_layouts: []VertexLayout
    export topology: Topology
    /// For strip topologies with indexed draws: the index format whose
    /// all-ones value restarts the strip.
    export strip_index_format: ?IndexFormat
    export cull: Cull
    export front: FrontFace
    export targets: []ColorTarget
    export depth: ?DepthStencil
    export samples: int
    export alpha_to_coverage: bool
    export label: str
    sealed: u8
}

/// Triangle lists, no culling, counter-clockwise front, no targets, no
/// depth, one sample.
export const DefaultRenderPipeline: RenderPipelineDesc = RenderPipelineDesc(vertex_layouts: [], topology: Topology.TriangleList, strip_index_format: none, cull: Cull.None, front: FrontFace.Ccw, targets: [], depth: none, samples: 1, alpha_to_coverage: false, label: "", sealed: 0)

/// A compiled render pipeline.
export linear struct RenderPipeline {
    slot: u32
    generation: u32
    device: u32
}

struct RenderPipelineEntry {
    layout_slot: u32
    group_count: int
    group_fps: [4]u64
    immediates: int
    vs_slot: u32
    fs_slot: u32
    vertex_slot_count: int
    vertex_strides: [16]int
    target_count: int
    target_formats: [8]TextureFormat
    depth_format: ?TextureFormat
    samples: int
    native_handle: u64
}

RenderPipelineEntry.Free(self, a: &Alloc) {
    _ = self
    _ = a
}

CheckShaderVsLayout(st: &DeviceState, refl: &SpirvReflection, pl: &PipelineLayoutEntry, stage: Stage): Invalid!void {
    if refl.push_size > pl.immediates {
        fail Invalid(misuse: Misuse.ImmediatesRange, at: -1)
    }
    for bi in 0..refl.bindings.Len() {
        sb := &refl.bindings[bi]
        if int(sb.set) >= pl.group_count {
            fail Invalid(misuse: Misuse.ShaderMismatch, at: int(sb.binding))
        }
        bgl := st.bg_layouts.Get(pl.group_slots[int(sb.set)])
        var found := false
        for ei in 0..bgl.entries.Len() {
            le := &bgl.entries[ei]
            if le.binding != int(sb.binding) {
                continue
            }
            found = true
            vis_ok := match stage {
                .Vertex { le.visibility.vertex }
                .Fragment { le.visibility.fragment }
                .Compute { le.visibility.compute }
            }
            if !vis_ok {
                fail Invalid(misuse: Misuse.ShaderMismatch, at: le.binding)
            }
            match le.kind {
                .Uniform(dynamic, min_size) {
                    if sb.kind != SpvBindUniform {
                        fail Invalid(misuse: Misuse.ShaderMismatch, at: le.binding)
                    }
                    if sb.block_size > min_size {
                        fail Invalid(misuse: Misuse.BlockTooLarge, at: le.binding)
                    }
                    if dynamic && sb.is_block_array && sb.non_const_index {
                        fail Invalid(misuse: Misuse.ShaderMismatch, at: le.binding)
                    }
                }
                .Storage(read_only: _, min_size, stride: _) {
                    if sb.kind != SpvBindStorage {
                        fail Invalid(misuse: Misuse.ShaderMismatch, at: le.binding)
                    }
                    if sb.block_size > min_size {
                        fail Invalid(misuse: Misuse.BlockTooLarge, at: le.binding)
                    }
                }
                .Texture(sample, dimension, multisampled) {
                    if sb.kind != SpvBindTexture || sb.dim != dimension || sb.multisampled != multisampled {
                        fail Invalid(misuse: Misuse.ShaderMismatch, at: le.binding)
                    }
                    want_cls := match sample {
                        .Sint { NumClassSint }
                        .Uint { NumClassUint }
                        _ { NumClassFloat }
                    }
                    if sb.sample_class != want_cls {
                        fail Invalid(misuse: Misuse.ShaderMismatch, at: le.binding)
                    }
                }
                .Sampler(comparison: _) {
                    if sb.kind != SpvBindSampler {
                        fail Invalid(misuse: Misuse.ShaderMismatch, at: le.binding)
                    }
                }
            }
            break
        }
        if !found {
            fail Invalid(misuse: Misuse.ShaderMismatch, at: int(sb.binding))
        }
    }
}

ValidateRenderPipelineDesc(
    st: &DeviceState,
    serial: u32,
    layout: &PipelineLayout,
    vs: &Shader,
    fs: ?&Shader,
    fs_slot: u32,
    d: &RenderPipelineDesc,
): Error!RenderPipelineEntry {
    lim := st.limits
    if !st.pipe_layouts.IsLive(layout.slot, layout.generation) {
        fail Invalid(misuse: Misuse.StaleHandle, at: -1)
    }
    if !st.shaders.IsLive(vs.slot, vs.generation) {
        fail Invalid(misuse: Misuse.StaleHandle, at: -1)
    }
    vse := st.shaders.Get(vs.slot)
    if vse.stage != Stage.Vertex {
        fail Invalid(misuse: Misuse.StageMismatch, at: -1)
    }
    match fs {
        some(f) {
            if f.device != serial || !st.shaders.IsLive(f.slot, f.generation) {
                fail Invalid(misuse: Misuse.StaleHandle, at: -1)
            }
            if st.shaders.Get(f.slot).stage != Stage.Fragment {
                fail Invalid(misuse: Misuse.StageMismatch, at: -1)
            }
        }
        none {}
    }

    if d.vertex_layouts.Len() > lim.max_vertex_buffers {
        fail Invalid(misuse: Misuse.LimitExceeded, at: -1)
    }
    var strides: [16]int = [0; 16]
    var total_attrs := 0
    var seen_locs: [16]int = [-1; 16]
    var seen_attrs: [16]VertexFormat = [VertexFormat.Float32; 16]
    for vi in 0..d.vertex_layouts.Len() {
        vl := &d.vertex_layouts[vi]
        if vl.stride < 0 || vl.stride > lim.max_vertex_stride {
            fail Invalid(misuse: Misuse.LimitExceeded, at: vi)
        }
        if vl.stride % 4 != 0 {
            fail Invalid(misuse: Misuse.BadLayout, at: vi)
        }
        strides[vi] = vl.stride
        for ai in 0..vl.attributes.Len() {
            if total_attrs >= lim.max_vertex_attributes {
                fail Invalid(misuse: Misuse.LimitExceeded, at: ai)
            }
            at := &vl.attributes[ai]
            vinfo := VertexInfoOf(at.format)
            if at.offset < 0 || at.offset % 4 != 0 {
                fail Invalid(misuse: Misuse.BadLayout, at: ai)
            }
            if vl.stride > 0 && at.offset + vinfo.bytes > vl.stride {
                fail Invalid(misuse: Misuse.BadLayout, at: ai)
            }
            for k in 0..total_attrs {
                if seen_locs[k] == at.location {
                    fail Invalid(misuse: Misuse.BadLayout, at: ai)
                }
            }
            seen_locs[total_attrs] = at.location
            seen_attrs[total_attrs] = at.format
            total_attrs += 1
        }
    }

    for ii in 0..vse.refl.inputs.Len() {
        inp := &vse.refl.inputs[ii]
        var matched := false
        for k in 0..total_attrs {
            if seen_locs[k] == inp.location {
                if VertexInfoOf(seen_attrs[k]).num_class != inp.num_class {
                    fail Invalid(misuse: Misuse.ShaderMismatch, at: inp.location)
                }
                matched = true
                break
            }
        }
        if !matched {
            fail Invalid(misuse: Misuse.ShaderMismatch, at: inp.location)
        }
    }

    is_strip := d.topology == Topology.LineStrip || d.topology == Topology.TriangleStrip
    if !is_strip && d.strip_index_format.IsSome() {
        fail Invalid(misuse: Misuse.BadLayout, at: -1)
    }
    if d.targets.Len() > lim.max_color_targets {
        fail Invalid(misuse: Misuse.LimitExceeded, at: -1)
    }
    if d.targets.Len() == 0 && d.depth.IsNone() {
        fail Invalid(misuse: Misuse.BadLayout, at: -1)
    }
    if d.samples != 1 && d.samples != 4 {
        fail Invalid(misuse: Misuse.SamplesMismatch, at: -1)
    }
    if d.alpha_to_coverage && d.samples != 4 {
        fail Invalid(misuse: Misuse.BadLayout, at: -1)
    }
    var target_fmts: [8]TextureFormat = [TextureFormat.Rgba8Unorm; 8]
    for ti in 0..d.targets.Len() {
        ct := &d.targets[ti]
        if IsDepthStencilFormat(ct.format) {
            fail Invalid(misuse: Misuse.FormatMismatch, at: ti)
        }
        caps := st.caps[FormatIndex(ct.format)]
        if !caps.render_target || (ct.blend.IsSome() && !caps.blendable) || (d.samples == 4 && !caps.multisample) {
            fail Invalid(misuse: Misuse.FormatCapability, at: ti)
        }
        target_fmts[ti] = ct.format
    }
    var depth_fmt: ?TextureFormat = none
    match d.depth {
        some(ds) {
            if !IsDepthStencilFormat(ds.format) {
                fail Invalid(misuse: Misuse.FormatMismatch, at: -1)
            }
            caps := st.caps[FormatIndex(ds.format)]
            if !caps.depth_stencil || (d.samples == 4 && !caps.multisample) {
                fail Invalid(misuse: Misuse.FormatCapability, at: -1)
            }
            if ds.bias.clamp != 0.0 && !st.depth_bias_clamp_ok {
                fail Unsupported
            }
            depth_fmt = some(ds.format)
        }
        none {}
    }

    pl := st.pipe_layouts.Get(layout.slot)
    try CheckShaderVsLayout(st, &vse.refl, pl, Stage.Vertex)
    match fs {
        some(f) {
            fse := st.shaders.Get(f.slot)
            try CheckShaderVsLayout(st, &fse.refl, pl, Stage.Fragment)
        }
        none {}
    }

    return RenderPipelineEntry(
        layout_slot: layout.slot,
        group_count: pl.group_count,
        group_fps: pl.group_fps,
        immediates: pl.immediates,
        vs_slot: vs.slot,
        fs_slot: fs_slot,
        vertex_slot_count: d.vertex_layouts.Len(),
        vertex_strides: strides,
        target_count: d.targets.Len(),
        target_formats: target_fmts,
        depth_format: depth_fmt,
        samples: d.samples,
        native_handle: 0,
    )
}

/// Makes a render pipeline; layout lives while it does. fs none: depth
/// only. Checks the shaders' interfaces (from SPIR-V) against layout and
/// d. Errors: Invalid (ShaderMismatch, BlockTooLarge, StageMismatch,
/// FormatCapability, ...), OutOfDeviceMemory, DeviceLost, Canceled (only
/// while queued). The driver compiles through runtime.Blocking.
export Device.NewRenderPipeline(mut self, io: Io, layout: &PipelineLayout, vs: &Shader, fs: ?&Shader, d: RenderPipelineDesc): Error!RenderPipeline {
    _ = io
    try self.CheckAlive()
    if layout.device != self.serial || vs.device != self.serial {
        fail Invalid(misuse: Misuse.StaleHandle, at: -1)
    }
    fs_slot := match fs {
        some(f) { f.slot }
        none { u32(0) }
    }
    var entry := try ValidateRenderPipelineDesc(self.state.Ref(), self.serial, layout, vs, fs, fs_slot, &d)
    nh := try self.NativeCreatePipeline()
    entry.native_handle = nh
    a := self.heap.Alloc()
    match self.state.Mut().render_pipes.Alloc(a, entry) {
        .Ok(slot, gen_) {
            st := self.state.Mut()
            st.pipe_layouts.IncRef(layout.slot)
            st.shaders.IncRef(vs.slot)
            if fs_slot != 0 {
                st.shaders.IncRef(fs_slot)
            }
            return RenderPipeline(slot: slot, generation: gen_, device: self.serial)
        }
        .Oom(val: _) {
            self.state.Mut().native.DestroyPipeline(nh)
            fail OutOfMemory
        }
    }
}

/// Gives the pipeline back. O(1).
export RenderPipeline.Close(self, dev: mut Device) {
    RenderPipeline(slot, generation, device) := self
    if device != dev.serial {
        Panic("gpu: a handle closed with another device")
    }
    dev.CloseRenderPipeSlot(slot, generation)
}

Device.CloseRenderPipeSlot(mut self, slot: u32, gen_: u32) {
    st := self.state.Mut()
    if !st.render_pipes.MarkUserClosed(slot, gen_) {
        return
    }
    lu := st.render_pipes.LastUse(slot)
    self.QueueOrRelease(DefRenderPipeline, slot, lu)
}

Device.ReleaseRenderPipeRefs(mut self, slot: u32): (lslot: u32, vs: u32, fs: u32) {
    a := self.heap.Alloc()
    st := self.state.Mut()
    match st.render_pipes.Release(a, slot) {
        some(e) {
            st.native.DestroyPipeline(e.native_handle)
            return (lslot: e.layout_slot, vs: e.vs_slot, fs: e.fs_slot)
        }
        none { return (lslot: 0, vs: 0, fs: 0) }
    }
}

Device.DestroyRenderPipeSlot(mut self, slot: u32) {
    (lslot, vs: vs_slot, fs: fs_slot) := self.ReleaseRenderPipeRefs(slot)
    self.DecRefAndQueue(DefPipelineLayout, lslot)
    self.DecRefAndQueue(DefShader, vs_slot)
    self.DecRefAndQueue(DefShader, fs_slot)
}

/// A compiled compute pipeline.
export linear struct ComputePipeline {
    slot: u32
    generation: u32
    device: u32
}

struct ComputePipelineEntry {
    layout_slot: u32
    group_count: int
    group_fps: [4]u64
    immediates: int
    cs_slot: u32
    native_handle: u64
}

ComputePipelineEntry.Free(self, a: &Alloc) {
    _ = self
    _ = a
}

ValidateComputePipelineDesc(st: &DeviceState, layout: &PipelineLayout, cs: &Shader): Error!ComputePipelineEntry {
    lim := st.limits
    if !st.pipe_layouts.IsLive(layout.slot, layout.generation) || !st.shaders.IsLive(cs.slot, cs.generation) {
        fail Invalid(misuse: Misuse.StaleHandle, at: -1)
    }
    cse := st.shaders.Get(cs.slot)
    if cse.stage != Stage.Compute {
        fail Invalid(misuse: Misuse.StageMismatch, at: -1)
    }
    r := &cse.refl
    if r.local_x <= 0 || r.local_y <= 0 || r.local_z <= 0 {
        fail Invalid(misuse: Misuse.LimitExceeded, at: -1)
    }
    if r.local_x > lim.max_workgroup_size_x || r.local_y > lim.max_workgroup_size_y || r.local_z > lim.max_workgroup_size_z {
        fail Invalid(misuse: Misuse.LimitExceeded, at: -1)
    }
    if i64(r.local_x) * i64(r.local_y) * i64(r.local_z) > i64(lim.max_workgroup_invocations) {
        fail Invalid(misuse: Misuse.LimitExceeded, at: -1)
    }
    pl := st.pipe_layouts.Get(layout.slot)
    try CheckShaderVsLayout(st, r, pl, Stage.Compute)
    return ComputePipelineEntry(
        layout_slot: layout.slot,
        group_count: pl.group_count,
        group_fps: pl.group_fps,
        immediates: pl.immediates,
        cs_slot: cs.slot,
        native_handle: 0,
    )
}

/// Makes a compute pipeline, as NewRenderPipeline. Also checks the
/// workgroup size against the limits.
export Device.NewComputePipeline(mut self, io: Io, layout: &PipelineLayout, cs: &Shader, label: str): Error!ComputePipeline {
    _ = io
    _ = label
    try self.CheckAlive()
    if layout.device != self.serial || cs.device != self.serial {
        fail Invalid(misuse: Misuse.StaleHandle, at: -1)
    }
    var entry := try ValidateComputePipelineDesc(self.state.Ref(), layout, cs)
    nh := try self.NativeCreatePipeline()
    entry.native_handle = nh
    a := self.heap.Alloc()
    match self.state.Mut().compute_pipes.Alloc(a, entry) {
        .Ok(slot, gen_) {
            st := self.state.Mut()
            st.pipe_layouts.IncRef(layout.slot)
            st.shaders.IncRef(cs.slot)
            return ComputePipeline(slot: slot, generation: gen_, device: self.serial)
        }
        .Oom(val: _) {
            self.state.Mut().native.DestroyPipeline(nh)
            fail OutOfMemory
        }
    }
}

/// Gives the pipeline back. O(1).
export ComputePipeline.Close(self, dev: mut Device) {
    ComputePipeline(slot, generation, device) := self
    if device != dev.serial {
        Panic("gpu: a handle closed with another device")
    }
    dev.CloseComputePipeSlot(slot, generation)
}

Device.CloseComputePipeSlot(mut self, slot: u32, gen_: u32) {
    st := self.state.Mut()
    if !st.compute_pipes.MarkUserClosed(slot, gen_) {
        return
    }
    lu := st.compute_pipes.LastUse(slot)
    self.QueueOrRelease(DefComputePipeline, slot, lu)
}

Device.ReleaseComputePipeRefs(mut self, slot: u32): (lslot: u32, cs: u32) {
    a := self.heap.Alloc()
    st := self.state.Mut()
    match st.compute_pipes.Release(a, slot) {
        some(e) {
            st.native.DestroyPipeline(e.native_handle)
            return (lslot: e.layout_slot, cs: e.cs_slot)
        }
        none { return (lslot: 0, cs: 0) }
    }
}

Device.DestroyComputePipeSlot(mut self, slot: u32) {
    (lslot, cs: cs_slot) := self.ReleaseComputePipeRefs(slot)
    self.DecRefAndQueue(DefPipelineLayout, lslot)
    self.DecRefAndQueue(DefShader, cs_slot)
}
