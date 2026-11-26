// Why: shaders read resources through bind groups, which map one to one
// onto a D3D12 descriptor table range and a VkDescriptorSet.
//
// What: Visibility, SampleType, BindingKind, LayoutEntry, BindGroupLayout,
// PipelineLayout, Bound, BindEntry, BindGroup, and their Device creation
// and Close methods.
//
// How: validates limits, uniqueness, stage visibility, writable-storage-in-
// render refusal, dynamic uniform alignment/size, and buffer/view/sampler
// compatibility at creation, keeping ref-counts to dependencies.

/// The stages a binding is visible to.
export struct Visibility {
    export vertex: bool
    export fragment: bool
    export compute: bool
}

/// What a sampled texture binding returns.
export open enum SampleType {
    Float
    UnfilterableFloat
    Depth
    Sint
    Uint
}

/// What one binding of a group holds.
export open enum BindingKind {
    /// A constant buffer. dynamic: its offset is given at SetBindGroup, and
    /// the buffer needs uniform_dynamic. min_size bounds the shader's block.
    Uniform(dynamic: bool, min_size: int)
    /// stride: 0 for ByteAddressBuffer, sizeof(T) for StructuredBuffer<T>.
    /// A writable one is visible to compute only.
    Storage(read_only: bool, min_size: int, stride: int)
    Texture(sample: SampleType, dimension: ViewDimension, multisampled: bool)
    Sampler(comparison: bool)
}

/// One binding of a layout: its number (the HLSL register number), its
/// stages and its kind.
export struct LayoutEntry {
    export binding: int
    export visibility: Visibility
    export kind: BindingKind
}

/// The shape of a bind group.
export linear struct BindGroupLayout {
    slot: u32
    generation: u32
    device: u32
}

struct BindGroupLayoutEntry {
    entries: HeapList[LayoutEntry]
    dynamic_count: int
    fingerprint: u64
    native_handle: u64
}

BindGroupLayoutEntry.Free(self, a: &Alloc) {
    BindGroupLayoutEntry(entries, dynamic_count: _, fingerprint: _, native_handle: _) := self
    entries.Free(a)
}

HashU64Step(h: u64, v: u64): u64 {
    return (h ^ v) *% 0x100000001b3
}

SampleTypeCode(s: SampleType): u64 {
    return match s {
        .Float { 0 }
        .UnfilterableFloat { 1 }
        .Depth { 2 }
        .Sint { 3 }
        .Uint { 4 }
    }
}

ViewDimCode(d: ViewDimension): u64 {
    return match d {
        .D2 { 0 }
        .D2Array { 1 }
        .Cube { 2 }
        .CubeArray { 3 }
    }
}

ComputeBglFingerprint(entries: []LayoutEntry): u64 {
    var h: u64 = 0xcbf29ce484222325
    for i in 0..entries.Len() {
        e := &entries[i]
        h = HashU64Step(h, Cast[u64](e.binding))
        vis := (if e.visibility.vertex { u64(1) } else { u64(0) }) |
            (if e.visibility.fragment { u64(2) } else { u64(0) }) |
            (if e.visibility.compute { u64(4) } else { u64(0) })
        h = HashU64Step(h, vis)
        match e.kind {
            .Uniform(dynamic, min_size) {
                h = HashU64Step(h, 1)
                h = HashU64Step(h, if dynamic { 1 } else { 0 })
                h = HashU64Step(h, Cast[u64](min_size))
            }
            .Storage(read_only, min_size, stride) {
                h = HashU64Step(h, 2)
                h = HashU64Step(h, if read_only { 1 } else { 0 })
                h = HashU64Step(h, Cast[u64](min_size))
                h = HashU64Step(h, Cast[u64](stride))
            }
            .Texture(sample, dimension, multisampled) {
                h = HashU64Step(h, 3)
                sc := SampleTypeCode(sample)
                dc := ViewDimCode(dimension)
                h = HashU64Step(h, sc | (dc << 8) | (if multisampled { u64(1) << 16 } else { u64(0) }))
            }
            .Sampler(comparison) {
                h = HashU64Step(h, 4)
                h = HashU64Step(h, if comparison { 1 } else { 0 })
            }
        }
    }
    return h
}

/// Makes a layout. Binding numbers are unique in the group across kinds and
/// below 16. Errors: Invalid(BadLayout), Invalid(LimitExceeded),
/// Invalid(StorageWriteInRender). O(entries log entries); allocates the
/// sorted entries from the device's heap.
export Device.NewBindGroupLayout(mut self, entries: []LayoutEntry, label: str): Error!BindGroupLayout {
    _ = label
    try self.CheckAlive()
    lim := self.Limits()
    if entries.Len() > lim.max_bindings_per_group {
        fail Invalid(misuse: Misuse.LimitExceeded, at: -1)
    }
    var dyn_count := 0
    for i in 0..entries.Len() {
        e := &entries[i]
        if e.binding < 0 || e.binding >= lim.max_bindings_per_group {
            fail Invalid(misuse: Misuse.LimitExceeded, at: i)
        }
        if !e.visibility.vertex && !e.visibility.fragment && !e.visibility.compute {
            fail Invalid(misuse: Misuse.BadLayout, at: i)
        }
        match e.kind {
            .Uniform(dynamic, min_size) {
                if min_size <= 0 {
                    fail Invalid(misuse: Misuse.BadLayout, at: i)
                }
                if min_size > lim.max_uniform_binding {
                    fail Invalid(misuse: Misuse.LimitExceeded, at: i)
                }
                if dynamic {
                    if min_size % 16 != 0 {
                        fail Invalid(misuse: Misuse.BadLayout, at: i)
                    }
                    dyn_count += 1
                }
            }
            .Storage(read_only, min_size, stride) {
                if !read_only && (e.visibility.vertex || e.visibility.fragment) {
                    fail Invalid(misuse: Misuse.StorageWriteInRender, at: i)
                }
                if min_size < 0 || min_size > lim.max_storage_binding {
                    fail Invalid(misuse: Misuse.LimitExceeded, at: i)
                }
                if stride < 0 || (stride > 0 && stride % 4 != 0) {
                    fail Invalid(misuse: Misuse.BadLayout, at: i)
                }
            }
            .Texture(sample: _, dimension, multisampled) {
                if multisampled && dimension != ViewDimension.D2 {
                    fail Invalid(misuse: Misuse.BadLayout, at: i)
                }
            }
            .Sampler(comparison: _) {}
        }
        for j in 0..i {
            if entries[j].binding == e.binding {
                fail Invalid(misuse: Misuse.BadLayout, at: i)
            }
        }
    }
    if dyn_count > lim.max_dynamic_uniforms {
        fail Invalid(misuse: Misuse.LimitExceeded, at: -1)
    }
    a := self.heap.Alloc()
    var sorted := HeapList[LayoutEntry].New(a)
    onerr sorted.Free(a)
    for i in 0..entries.Len() {
        try sorted.Push(a, entries[i])
    }
    for i in 1..sorted.Len() {
        key := sorted[i]
        var j := i - 1
        for j >= 0 && sorted[j].binding > key.binding {
            sorted[j + 1] = sorted[j]
            j -= 1
        }
        sorted[j + 1] = key
    }
    fp := ComputeBglFingerprint(sorted.Items())
    nh := try self.NativeCreateBindGroupLayout()
    bgl := BindGroupLayoutEntry(entries: sorted, dynamic_count: dyn_count, fingerprint: fp, native_handle: nh)
    match self.state.Mut().bg_layouts.Alloc(a, bgl) {
        .Ok(slot, gen_) { return BindGroupLayout(slot: slot, generation: gen_, device: self.serial) }
        .Oom(val) {
            val.Free(a)
            self.state.Mut().native.DestroyBindGroupLayout(nh)
            fail OutOfMemory
        }
    }
}

/// Gives the layout back. O(1).
export BindGroupLayout.Close(self, dev: mut Device) {
    BindGroupLayout(slot, generation, device) := self
    if device != dev.serial {
        Panic("gpu: a handle closed with another device")
    }
    dev.CloseBgLayoutSlot(slot, generation)
}

Device.CloseBgLayoutSlot(mut self, slot: u32, gen_: u32) {
    st := self.state.Mut()
    if !st.bg_layouts.MarkUserClosed(slot, gen_) {
        return
    }
    lu := st.bg_layouts.LastUse(slot)
    self.QueueOrRelease(DefBindGroupLayout, slot, lu)
}

Device.DestroyBgLayoutSlot(mut self, slot: u32) {
    a := self.heap.Alloc()
    st := self.state.Mut()
    match st.bg_layouts.Release(a, slot) {
        some(e) {
            nh := e.native_handle
            e.Free(a)
            st.native.DestroyBindGroupLayout(nh)
        }
        none {}
    }
}

/// The layout of up to four groups (group i is HLSL space i) and of
/// immediates bytes (a multiple of 4, at most 128) at b0, space8.
export linear struct PipelineLayout {
    slot: u32
    generation: u32
    device: u32
}

struct PipelineLayoutEntry {
    group_slots: [4]u32
    group_fps: [4]u64
    group_count: int
    immediates: int
    native_handle: u64
}

PipelineLayoutEntry.Free(self, a: &Alloc) {
    _ = self
    _ = a
}

struct StageCounts {
    u: int
    s: int
    t: int
    smp: int
}

AddStageCounts(c: mut StageCounts, kind: BindingKind, lim: &DeviceLimits): Invalid!void {
    match kind {
        .Uniform(dynamic: _, min_size: _) {
            c.u += 1
            if c.u > lim.max_uniforms_per_stage { fail Invalid(misuse: Misuse.LimitExceeded, at: -1) }
        }
        .Storage(read_only: _, min_size: _, stride: _) {
            c.s += 1
            if c.s > lim.max_storage_per_stage { fail Invalid(misuse: Misuse.LimitExceeded, at: -1) }
        }
        .Texture(sample: _, dimension: _, multisampled: _) {
            c.t += 1
            if c.t > lim.max_textures_per_stage { fail Invalid(misuse: Misuse.LimitExceeded, at: -1) }
        }
        .Sampler(comparison: _) {
            c.smp += 1
            if c.smp > lim.max_samplers_per_stage { fail Invalid(misuse: Misuse.LimitExceeded, at: -1) }
        }
    }
}

ValidatePipelineLayoutGroups(st: &DeviceState, serial: u32, groups: []&BindGroupLayout, immediates: int): Invalid!PipelineLayoutEntry {
    lim := st.limits
    if groups.Len() > lim.max_bind_groups {
        fail Invalid(misuse: Misuse.LimitExceeded, at: -1)
    }
    if immediates < 0 || immediates > lim.max_immediates {
        fail Invalid(misuse: Misuse.LimitExceeded, at: -1)
    }
    if immediates % 4 != 0 {
        fail Invalid(misuse: Misuse.BadLayout, at: -1)
    }
    var gslots: [4]u32 = [0; 4]
    var gfps: [4]u64 = [0; 4]
    var total_dyn := 0
    var vs_c := StageCounts(u: 0, s: 0, t: 0, smp: 0)
    var fs_c := StageCounts(u: 0, s: 0, t: 0, smp: 0)
    var cs_c := StageCounts(u: 0, s: 0, t: 0, smp: 0)

    for gi in 0..groups.Len() {
        g := groups[gi]
        if g.device != serial || !st.bg_layouts.IsLive(g.slot, g.generation) {
            fail Invalid(misuse: Misuse.StaleHandle, at: gi)
        }
        bgl := st.bg_layouts.Get(g.slot)
        gslots[gi] = g.slot
        gfps[gi] = bgl.fingerprint
        total_dyn += bgl.dynamic_count
        for ei in 0..bgl.entries.Len() {
            e := &bgl.entries[ei]
            if e.visibility.vertex { try AddStageCounts(mut vs_c, e.kind, &lim) }
            if e.visibility.fragment { try AddStageCounts(mut fs_c, e.kind, &lim) }
            if e.visibility.compute { try AddStageCounts(mut cs_c, e.kind, &lim) }
        }
    }
    if total_dyn > lim.max_dynamic_uniforms {
        fail Invalid(misuse: Misuse.LimitExceeded, at: -1)
    }
    return PipelineLayoutEntry(
        group_slots: gslots,
        group_fps: gfps,
        group_count: groups.Len(),
        immediates: immediates,
        native_handle: 0,
    )
}

/// Makes a pipeline layout; its group layouts live while it does. Errors:
/// Invalid(LimitExceeded) (groups, per-stage counts, immediates). O(entries);
/// one root signature or VkPipelineLayout.
export Device.NewPipelineLayout(mut self, groups: []&BindGroupLayout, immediates: int, label: str): Error!PipelineLayout {
    try self.CheckAlive()
    var entry := try ValidatePipelineLayoutGroups(self.state.Ref(), self.serial, groups, immediates)
    nh := try self.NativeCreatePipelineLayout(groups, immediates, label)
    entry.native_handle = nh
    a := self.heap.Alloc()
    match self.state.Mut().pipe_layouts.Alloc(a, entry) {
        .Ok(slot, gen_) {
            for gi in 0..groups.Len() {
                self.state.Mut().bg_layouts.IncRef(groups[gi].slot)
            }
            return PipelineLayout(slot: slot, generation: gen_, device: self.serial)
        }
        .Oom(val: _) {
            self.state.Mut().native.DestroyPipelineLayout(nh)
            fail OutOfMemory
        }
    }
}

/// Gives the pipeline layout back. O(1).
export PipelineLayout.Close(self, dev: mut Device) {
    PipelineLayout(slot, generation, device) := self
    if device != dev.serial {
        Panic("gpu: a handle closed with another device")
    }
    dev.ClosePipeLayoutSlot(slot, generation)
}

Device.ClosePipeLayoutSlot(mut self, slot: u32, gen_: u32) {
    st := self.state.Mut()
    if !st.pipe_layouts.MarkUserClosed(slot, gen_) {
        return
    }
    lu := st.pipe_layouts.LastUse(slot)
    self.QueueOrRelease(DefPipelineLayout, slot, lu)
}

Device.ReleasePipeLayoutRef(mut self, slot: u32): (count: int, slots: [4]u32) {
    a := self.heap.Alloc()
    st := self.state.Mut()
    match st.pipe_layouts.Release(a, slot) {
        some(e) {
            st.native.DestroyPipelineLayout(e.native_handle)
            return (count: e.group_count, slots: e.group_slots)
        }
        none { return (count: 0, slots: [0; 4]) }
    }
}

Device.DestroyPipeLayoutSlot(mut self, slot: u32) {
    (count: gc, slots: gslots) := self.ReleasePipeLayoutRef(slot)
    for gi in 0..gc {
        s := gslots[gi]
        st := self.state.Mut()
        if st.bg_layouts.DecRef(s) {
            lu := st.bg_layouts.LastUse(s)
            self.QueueOrRelease(DefBindGroupLayout, s, lu)
        }
    }
}

/// The resource a bind entry points at.
export enum Bound {
    Buffer(b: &Buffer, offset: int, size: int)
    View(v: &TextureView)
    Sampler(s: &Sampler)
}

/// One entry of a bind group.
export struct BindEntry {
    export binding: int
    export bound: Bound
}

/// Resources bound together; each lives while the group does.
export linear struct BindGroup {
    slot: u32
    generation: u32
    device: u32
}

struct BoundBufRec {
    buf_slot: u32
    buf_size: int
    offset: int
    size: int
    is_dynamic: bool
    is_storage: bool
    read_only: bool
}

struct BoundTexRec {
    view_slot: u32
    tex_slot: u32
    first_mip: int
    mip_count: int
    first_layer: int
    layer_count: int
    plane_mask: u8
    is_depth: bool
}

struct BindGroupEntry {
    layout_slot: u32
    layout_fp: u64
    dynamic_count: int
    dyn_buf_slots: [4]u32
    dyn_offsets: [4]int
    dyn_sizes: [4]int
    bufs: HeapList[BoundBufRec]
    texs: HeapList[BoundTexRec]
    samplers: HeapList[u32]
    native_handle: u64
}

BindGroupEntry.Free(self, a: &Alloc) {
    BindGroupEntry(layout_slot: _, layout_fp: _, dynamic_count: _, dyn_buf_slots: _, dyn_offsets: _, dyn_sizes: _, bufs, texs, samplers, native_handle: _) := self
    bufs.Free(a)
    texs.Free(a)
    samplers.Free(a)
}

ViewCompatibleSampleType(fmt: TextureFormat, aspect: Aspect, caps: FormatCaps, want: SampleType): bool {
    row := RowOf(fmt)
    if IsDepthStencilFormat(fmt) {
        match aspect {
            .Stencil {
                return want == SampleType.Uint
            }
            .All | .Depth {
                return want == SampleType.Depth || want == SampleType.UnfilterableFloat
            }
        }
    }
    return match want {
        .Float { row.num_class == NumClassFloat && caps.filterable }
        .UnfilterableFloat { row.num_class == NumClassFloat }
        .Depth { false }
        .Sint { row.num_class == NumClassSint }
        .Uint { row.num_class == NumClassUint }
    }
}

BuildBindGroupEntry(
    a: &Alloc,
    st: &DeviceState,
    serial: u32,
    layout: &BindGroupLayout,
    entries: []BindEntry,
): Error!BindGroupEntry from a {
    if !st.bg_layouts.IsLive(layout.slot, layout.generation) {
        fail Invalid(misuse: Misuse.StaleHandle, at: -1)
    }
    bgl := st.bg_layouts.Get(layout.slot)
    if entries.Len() != bgl.entries.Len() {
        fail Invalid(misuse: Misuse.BindMismatch, at: -1)
    }
    lim := st.limits
    var bufs := HeapList[BoundBufRec].New(a)
    onerr bufs.Free(a)
    var texs := HeapList[BoundTexRec].New(a)
    onerr texs.Free(a)
    var samplers := HeapList[u32].New(a)
    onerr samplers.Free(a)
    var dyn_count := 0
    var dyn_slots: [4]u32 = [0; 4]
    var dyn_offs: [4]int = [0; 4]
    var dyn_szs: [4]int = [0; 4]

    for li in 0..bgl.entries.Len() {
        le := &bgl.entries[li]
        var found_idx := -1
        for ei in 0..entries.Len() {
            if entries[ei].binding == le.binding {
                if found_idx >= 0 {
                    fail Invalid(misuse: Misuse.BindMismatch, at: ei)
                }
                found_idx = ei
            }
        }
        if found_idx < 0 {
            fail Invalid(misuse: Misuse.BindMismatch, at: li)
        }
        be := &entries[found_idx]
        match le.kind {
            .Uniform(dynamic, min_size) {
                match be.bound {
                    .Buffer(b, offset, size) {
                        if b.device != serial || !st.buffers.IsLive(b.slot, b.generation) {
                            fail Invalid(misuse: Misuse.StaleHandle, at: found_idx)
                        }
                        be_buf := st.buffers.Get(b.slot)
                        ok_usage := if dynamic { be_buf.usage.uniform_dynamic } else { be_buf.usage.uniform }
                        if !ok_usage {
                            fail Invalid(misuse: Misuse.BindMismatch, at: found_idx)
                        }
                        if offset < 0 || offset % lim.uniform_offset_align != 0 || size < min_size || i64(offset) + i64(size) > i64(be_buf.size) {
                            fail Invalid(misuse: Misuse.BindMismatch, at: found_idx)
                        }
                        try bufs.Push(a, BoundBufRec(buf_slot: b.slot, buf_size: be_buf.size, offset: offset, size: size, is_dynamic: dynamic, is_storage: false, read_only: true))
                        if dynamic {
                            dyn_slots[dyn_count] = b.slot
                            dyn_offs[dyn_count] = offset
                            dyn_szs[dyn_count] = size
                            dyn_count += 1
                        }
                    }
                    .View(v: _) | .Sampler(s: _) { fail Invalid(misuse: Misuse.BindMismatch, at: found_idx) }
                }
            }
            .Storage(read_only, min_size, stride) {
                match be.bound {
                    .Buffer(b, offset, size) {
                        if b.device != serial || !st.buffers.IsLive(b.slot, b.generation) {
                            fail Invalid(misuse: Misuse.StaleHandle, at: found_idx)
                        }
                        be_buf := st.buffers.Get(b.slot)
                        if !be_buf.usage.storage {
                            fail Invalid(misuse: Misuse.BindMismatch, at: found_idx)
                        }
                        if offset < 0 || offset % lim.storage_offset_align != 0 || size < min_size || i64(offset) + i64(size) > i64(be_buf.size) {
                            fail Invalid(misuse: Misuse.BindMismatch, at: found_idx)
                        }
                        if stride > 0 && (offset % stride != 0 || size % stride != 0) {
                            fail Invalid(misuse: Misuse.BindMismatch, at: found_idx)
                        }
                        try bufs.Push(a, BoundBufRec(buf_slot: b.slot, buf_size: be_buf.size, offset: offset, size: size, is_dynamic: false, is_storage: true, read_only: read_only))
                    }
                    .View(v: _) | .Sampler(s: _) { fail Invalid(misuse: Misuse.BindMismatch, at: found_idx) }
                }
            }
            .Texture(sample, dimension, multisampled) {
                match be.bound {
                    .View(v) {
                        if v.device != serial || !st.views.IsLive(v.slot, v.generation) {
                            fail Invalid(misuse: Misuse.StaleHandle, at: found_idx)
                        }
                        ve := st.views.Get(v.slot)
                        if !ve.usage.sampled || ve.dim != dimension || (ve.samples > 1) != multisampled {
                            fail Invalid(misuse: Misuse.BindMismatch, at: found_idx)
                        }
                        caps := st.caps[FormatIndex(ve.format)]
                        if !ViewCompatibleSampleType(ve.format, ve.aspect, caps, sample) {
                            fail Invalid(misuse: Misuse.BindMismatch, at: found_idx)
                        }
                        try texs.Push(a, BoundTexRec(
                            view_slot: v.slot,
                            tex_slot: ve.tex_slot,
                            first_mip: ve.first_mip,
                            mip_count: ve.mip_count,
                            first_layer: ve.first_layer,
                            layer_count: ve.layer_count,
                            plane_mask: ve.plane_mask,
                            is_depth: IsDepthStencilFormat(ve.format),
                        ))
                    }
                    .Buffer(b: _, offset: _, size: _) | .Sampler(s: _) { fail Invalid(misuse: Misuse.BindMismatch, at: found_idx) }
                }
            }
            .Sampler(comparison) {
                match be.bound {
                    .Sampler(s) {
                        if s.device != serial || !st.samplers.IsLive(s.slot, s.generation) {
                            fail Invalid(misuse: Misuse.StaleHandle, at: found_idx)
                        }
                        se := st.samplers.Get(s.slot)
                        if se.comparison != comparison {
                            fail Invalid(misuse: Misuse.BindMismatch, at: found_idx)
                        }
                        try samplers.Push(a, s.slot)
                    }
                    .Buffer(b: _, offset: _, size: _) | .View(v: _) { fail Invalid(misuse: Misuse.BindMismatch, at: found_idx) }
                }
            }
        }
    }

    return BindGroupEntry(
        layout_slot: layout.slot,
        layout_fp: bgl.fingerprint,
        dynamic_count: dyn_count,
        dyn_buf_slots: dyn_slots,
        dyn_offsets: dyn_offs,
        dyn_sizes: dyn_szs,
        bufs: bufs,
        texs: texs,
        samplers: samplers,
        native_handle: 0,
    )
}

/// Makes a group of entries, one per layout entry. Errors:
/// Invalid(BindMismatch, at: entry) for a missing, extra or wrong-kind
/// entry, a missing usage, an unaligned offset or a range past the buffer.
/// O(entries) descriptor writes; allocates descriptor space.
export Device.NewBindGroup(mut self, layout: &BindGroupLayout, entries: []BindEntry, label: str): Error!BindGroup {
    _ = label
    try self.CheckAlive()
    if layout.device != self.serial {
        fail Invalid(misuse: Misuse.StaleHandle, at: -1)
    }
    a := self.heap.Alloc()
    var entry := try BuildBindGroupEntry(a, self.state.Ref(), self.serial, layout, entries)
    nh := self.NativeCreateBindGroup(layout.slot, entries) else e {
        entry.Free(a)
        fail e
    }
    entry.native_handle = nh
    match self.state.Mut().bind_groups.Alloc(a, entry) {
        .Ok(slot, gen_) {
            st := self.state.Mut()
            st.bg_layouts.IncRef(layout.slot)
            bg := st.bind_groups.Get(slot)
            for i in 0..bg.bufs.Len() {
                st.buffers.IncRef(bg.bufs[i].buf_slot)
            }
            for i in 0..bg.texs.Len() {
                st.views.IncRef(bg.texs[i].view_slot)
            }
            for i in 0..bg.samplers.Len() {
                st.samplers.IncRef(bg.samplers[i])
            }
            return BindGroup(slot: slot, generation: gen_, device: self.serial)
        }
        .Oom(val) {
            val.Free(a)
            self.state.Mut().native.DestroyBindGroup(nh)
            fail OutOfMemory
        }
    }
}

/// Gives the group back. O(1).
export BindGroup.Close(self, dev: mut Device) {
    BindGroup(slot, generation, device) := self
    if device != dev.serial {
        Panic("gpu: a handle closed with another device")
    }
    dev.CloseBindGroupSlot(slot, generation)
}

Device.CloseBindGroupSlot(mut self, slot: u32, gen_: u32) {
    st := self.state.Mut()
    if !st.bind_groups.MarkUserClosed(slot, gen_) {
        return
    }
    lu := st.bind_groups.LastUse(slot)
    self.QueueOrRelease(DefBindGroup, slot, lu)
}

Device.ReleaseBindGroupRefs(mut self, slot: u32, bslots: mut HeapList[u32], vslots: mut HeapList[u32], sslots: mut HeapList[u32]): u32 {
    a := self.heap.Alloc()
    st := self.state.Mut()
    match st.bind_groups.Release(a, slot) {
        some(e) {
            lslot := e.layout_slot
            for i in 0..e.bufs.Len() { bslots.Push(a, e.bufs[i].buf_slot) else {} }
            for i in 0..e.texs.Len() { vslots.Push(a, e.texs[i].view_slot) else {} }
            for i in 0..e.samplers.Len() { sslots.Push(a, e.samplers[i]) else {} }
            nh := e.native_handle
            e.Free(a)
            st.native.DestroyBindGroup(nh)
            return lslot
        }
        none { return 0 }
    }
}

Device.DestroyBindGroupSlot(mut self, slot: u32) {
    a := self.heap.Alloc()
    var bslots := HeapList[u32].New(a)
    var vslots := HeapList[u32].New(a)
    var sslots := HeapList[u32].New(a)
    defer {
        bslots.Free(a)
        vslots.Free(a)
        sslots.Free(a)
    }
    lslot := self.ReleaseBindGroupRefs(slot, mut bslots, mut vslots, mut sslots)
    self.DecRefAndQueue(DefBindGroupLayout, lslot)
    for i in 0..bslots.Len() {
        self.DecRefAndQueue(DefBuffer, bslots[i])
    }
    for i in 0..vslots.Len() {
        self.DecRefAndQueue(DefView, vslots[i])
    }
    for i in 0..sslots.Len() {
        self.DecRefAndQueue(DefSampler, sslots[i])
    }
}
