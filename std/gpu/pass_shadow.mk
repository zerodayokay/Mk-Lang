// Why: PassShadow tracks active render pass attachments, pipeline/group/vertex/
// dynamic state, and dirty bits so redundant state is elided before flush.
//
// What: PassAttachRec, PassTexUse, PassBufUse, PassShadow, and dirty flag bits.
//
// How: allocated inside DeviceState and reset at each BeginRenderPass; merges
// compatible read-only texture uses inside the pass.

import std.mem

const DirtyPipeline: u32 = 1 << 0
const DirtyGroup0: u32 = 1 << 1
const DirtyGroup1: u32 = 1 << 2
const DirtyGroup2: u32 = 1 << 3
const DirtyGroup3: u32 = 1 << 4
const DirtyImmediates: u32 = 1 << 5
const DirtyVertexBase: u32 = 1 << 6
const DirtyIndex: u32 = 1 << 22
const DirtyViewport: u32 = 1 << 23
const DirtyScissor: u32 = 1 << 24
const DirtyBlend: u32 = 1 << 25
const DirtyStencil: u32 = 1 << 26

struct PassAttachRec {
    tex_slot: u32
    first_mip: int
    mip_count: int
    first_layer: int
    layer_count: int
    plane_mask: u8
    state: u16
    is_read_only_depth: bool
}

struct PassTexUse {
    tex_slot: u32
    first_mip: int
    mip_count: int
    first_layer: int
    layer_count: int
    plane_mask: u8
    state: u16
}

struct PassBufUse {
    buf_slot: u32
    state: u16
}

struct PassShadow {
    barrier_loc: StreamLoc
    pass_idx: u32
    extent_w: int
    extent_h: int
    color_count: int
    color_formats: [8]TextureFormat
    depth_format: ?TextureFormat
    samples: int
    attaches: [9]PassAttachRec
    attach_count: int
    tex_uses: HeapList[PassTexUse]
    buf_uses: HeapList[PassBufUse]
    pipeline_slot: u32
    layout_slot: u32
    layout_group_count: int
    layout_group_fps: [4]u64
    layout_immediates: int
    dirty: u32
    groups: [4]u32
    dyn_counts: [4]int
    dyn_offsets: [16]u32
    immediates: [128]u8
    vb_slots: [16]u32
    vb_offsets: [16]int
    vb_sizes: [16]int
    pipe_vb_count: int
    pipe_vb_strides: [16]int
    ib_slot: u32
    ib_offset: int
    ib_size: int
    ib_fmt: IndexFormat
    vp_x: f32
    vp_y: f32
    vp_w: f32
    vp_h: f32
    vp_min: f32
    vp_max: f32
    sc_x: int
    sc_y: int
    sc_w: int
    sc_h: int
    blend_r: f32
    blend_g: f32
    blend_b: f32
    blend_a: f32
    stencil_ref: u32
    prev_compute_write: bool
}

PassShadow.New(a: &Alloc): PassShadow {
    return PassShadow(
        barrier_loc: StreamLoc(chunk_idx: -1, word_idx: -1),
        pass_idx: 0,
        extent_w: 0,
        extent_h: 0,
        color_count: 0,
        color_formats: [TextureFormat.Rgba8Unorm; 8],
        depth_format: none,
        samples: 1,
        attaches: [mem.Zeroed[PassAttachRec](); 9],
        attach_count: 0,
        tex_uses: HeapList[PassTexUse].New(a),
        buf_uses: HeapList[PassBufUse].New(a),
        pipeline_slot: 0,
        layout_slot: 0,
        layout_group_count: 0,
        layout_group_fps: [0; 4],
        layout_immediates: 0,
        dirty: 0,
        groups: [0; 4],
        dyn_counts: [0; 4],
        dyn_offsets: [0; 16],
        immediates: [0; 128],
        vb_slots: [0; 16],
        vb_offsets: [0; 16],
        vb_sizes: [0; 16],
        pipe_vb_count: 0,
        pipe_vb_strides: [0; 16],
        ib_slot: 0,
        ib_offset: 0,
        ib_size: 0,
        ib_fmt: IndexFormat.Uint16,
        vp_x: 0.0,
        vp_y: 0.0,
        vp_w: 0.0,
        vp_h: 0.0,
        vp_min: 0.0,
        vp_max: 1.0,
        sc_x: 0,
        sc_y: 0,
        sc_w: 0,
        sc_h: 0,
        blend_r: 0.0,
        blend_g: 0.0,
        blend_b: 0.0,
        blend_a: 0.0,
        stencil_ref: 0,
        prev_compute_write: false,
    )
}

PassShadow.Reset(mut self, w: int, h: int) {
    self.extent_w = w
    self.extent_h = h
    self.color_count = 0
    self.depth_format = none
    self.samples = 1
    self.attach_count = 0
    self.pass_idx = 0
    self.tex_uses.Clear()
    self.buf_uses.Clear()
    self.pipeline_slot = 0
    self.layout_slot = 0
    self.layout_group_count = 0
    self.layout_group_fps = [0; 4]
    self.layout_immediates = 0
    self.dirty = 0
    self.groups = [0; 4]
    self.dyn_counts = [0; 4]
    self.dyn_offsets = [0; 16]
    self.immediates = [0; 128]
    self.vb_slots = [0; 16]
    self.vb_offsets = [0; 16]
    self.vb_sizes = [0; 16]
    self.pipe_vb_count = 0
    self.pipe_vb_strides = [0; 16]
    self.ib_slot = 0
    self.ib_offset = 0
    self.ib_size = 0
    self.vp_x = 0.0
    self.vp_y = 0.0
    self.vp_w = Cast[f32](w)
    self.vp_h = Cast[f32](h)
    self.vp_min = 0.0
    self.vp_max = 1.0
    self.sc_x = 0
    self.sc_y = 0
    self.sc_w = w
    self.sc_h = h
    self.blend_r = 0.0
    self.blend_g = 0.0
    self.blend_b = 0.0
    self.blend_a = 0.0
    self.stencil_ref = 0
    self.prev_compute_write = false
}

PassShadow.FreeAll(self, a: &Alloc) {
    PassShadow(
        barrier_loc: _, pass_idx: _, extent_w: _, extent_h: _, color_count: _, color_formats: _, depth_format: _, samples: _, attaches: _, attach_count: _,
        tex_uses,
        buf_uses,
        pipeline_slot: _, layout_slot: _, layout_group_count: _, layout_group_fps: _, layout_immediates: _, dirty: _, groups: _, dyn_counts: _, dyn_offsets: _, immediates: _,
        vb_slots: _, vb_offsets: _, vb_sizes: _, pipe_vb_count: _, pipe_vb_strides: _, ib_slot: _, ib_offset: _, ib_size: _, ib_fmt: _,
        vp_x: _, vp_y: _, vp_w: _, vp_h: _, vp_min: _, vp_max: _, sc_x: _, sc_y: _, sc_w: _, sc_h: _, blend_r: _, blend_g: _, blend_b: _, blend_a: _, stencil_ref: _, prev_compute_write: _,
    ) := self
    tex_uses.Free(a)
    buf_uses.Free(a)
}

PassShadow.RecordBufUse(mut self, a: &Alloc, slot: u32, state: u16): Invalid!void {
    if slot == 0 {
        return
    }
    for i in 0..self.buf_uses.Len() {
        if self.buf_uses[i].buf_slot == slot {
            self.buf_uses[i].state |= state
            return
        }
    }
    self.buf_uses.Push(a, PassBufUse(buf_slot: slot, state: state)) else {
        fail Invalid(misuse: Misuse.LimitExceeded, at: -1)
    }
}

RangesOverlap(a0: int, ac: int, b0: int, bc: int): bool {
    return a0 < b0 + bc && b0 < a0 + ac
}

PassTexUseOverlaps(a: &PassAttachRec, b: &BoundTexRec): bool {
    if a.tex_slot != b.tex_slot {
        return false
    }
    if (a.plane_mask & b.plane_mask) == 0 {
        return false
    }
    return RangesOverlap(a.first_mip, a.mip_count, b.first_mip, b.mip_count) && RangesOverlap(a.first_layer, a.layer_count, b.first_layer, b.layer_count)
}

PassShadow.RecordTexUse(mut self, a: &Alloc, u: PassTexUse): Invalid!void {
    for i in 0..self.tex_uses.Len() {
        ex := mut self.tex_uses[i]
        if ex.tex_slot == u.tex_slot && ex.first_mip == u.first_mip && ex.mip_count == u.mip_count && ex.first_layer == u.first_layer && ex.layer_count == u.layer_count && ex.plane_mask == u.plane_mask {
            match MergeTexReadStates(ex.state, u.state) {
                some(m) {
                    ex.state = m
                    return
                }
                none {
                    fail Invalid(misuse: Misuse.AttachmentAlsoBound, at: -1)
                }
            }
        }
    }
    self.tex_uses.Push(a, u) else {
        fail Invalid(misuse: Misuse.LimitExceeded, at: -1)
    }
}
