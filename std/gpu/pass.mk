// Why: draws and dispatches happen inside passes, which fix the attachments,
// hold their encoder exclusively, deduplicate redundant state, and flush
// barriers and dirty state only at draws and dispatches.
//
// What: RenderPass, PassShadow, and all render pass commands.
//
// How: shadow state lives inside DeviceState.pass_shadow so RenderPass stays
// a thin linear view; RenderPass.End resolves merged texture uses and patches
// OpBarrierRef at pass begin.

import std.mem

ApplyLayoutChangeGroups(st: mut DeviceState, new_layout: u32, group_count: int, group_fps: [4]u64, immediates: int) {
    sh := mut st.pass_shadow
    if st.planted_bug == PlantedBug.SkipDirtyGroupOnLayout {
        return
    }
    for gi in 0..4 {
        if gi >= group_count {
            sh.groups[gi] = 0
        } else if sh.groups[gi] != 0 {
            bg_fp := st.bind_groups.Get(sh.groups[gi]).layout_fp
            if bg_fp != group_fps[gi] {
                sh.groups[gi] = 0
            } else {
                sh.dirty |= (DirtyGroup0 << Cast[u32](gi))
            }
        }
    }
    if immediates > 0 {
        sh.dirty |= DirtyImmediates
    }
    _ = new_layout
}

ShadowSetBindGroup(st: mut DeviceState, serial: u32, index: int, g: &BindGroup, dynamic: []u32): bool {
    if st.stream.HasError() {
        return false
    }
    if index < 0 || index >= st.limits.max_bind_groups {
        st.stream.RecordError(Misuse.GroupIndex, st.stream.cmd_count)
        return false
    }
    if g.device != serial || !st.bind_groups.IsLive(g.slot, g.generation) {
        st.stream.RecordError(Misuse.StaleHandle, st.stream.cmd_count)
        return false
    }
    bg := st.bind_groups.Get(g.slot)
    if dynamic.Len() != bg.dynamic_count {
        st.stream.RecordError(Misuse.DynamicOffsetCount, st.stream.cmd_count)
        return false
    }
    for di in 0..bg.dynamic_count {
        off := dynamic[di]
        if off % 256 != 0 {
            st.stream.RecordError(Misuse.DynamicOffsetAlignment, st.stream.cmd_count)
            return false
        }
        bslot := bg.dyn_buf_slots[di]
        bsize := st.buffers.Get(bslot).size
        if i64(off) + i64(bg.dyn_sizes[di]) > i64(bsize) {
            st.stream.RecordError(Misuse.RangeOutOfBounds, st.stream.cmd_count)
            return false
        }
    }
    st.bind_groups.StampUse(g.slot, st.serials.next)
    sh := mut st.pass_shadow
    var same := sh.groups[index] == g.slot && sh.dyn_counts[index] == dynamic.Len()
    if same {
        base := index * 4
        for di in 0..dynamic.Len() {
            if sh.dyn_offsets[base + di] != dynamic[di] {
                same = false
                break
            }
        }
    }
    if same {
        return false
    }
    sh.groups[index] = g.slot
    sh.dyn_counts[index] = dynamic.Len()
    base := index * 4
    for di in 0..dynamic.Len() {
        sh.dyn_offsets[base + di] = dynamic[di]
    }
    sh.dirty |= (DirtyGroup0 << Cast[u32](index))
    return true
}

ShadowSetImmediates(st: mut DeviceState, offset: int, data: []u8): bool {
    if st.stream.HasError() {
        return false
    }
    if offset < 0 || offset % 4 != 0 || data.Len() % 4 != 0 || offset + data.Len() > st.limits.max_immediates {
        st.stream.RecordError(Misuse.ImmediatesRange, st.stream.cmd_count)
        return false
    }
    sh := mut st.pass_shadow
    var changed := false
    for i in 0..data.Len() {
        if sh.immediates[offset + i] != data[i] {
            sh.immediates[offset + i] = data[i]
            changed = true
        }
    }
    if changed {
        sh.dirty |= DirtyImmediates
        return true
    }
    return false
}

EmitSetGroupRecord(a: &Alloc into pool, sh: &PassShadow, stream: mut CommandStream, pool: mut ChunkPool, gi: int, nh: u64): bool {
    dc := sh.dyn_counts[gi]
    wlen := 2 + (dc + 1) / 2
    var words: [4]u64 = [0; 4]
    words[0] = PackHdr(OpSetGroup, Cast[u8](wlen), Cast[u16](gi), Cast[u32](dc))
    words[1] = nh
    base := gi * 4
    if dc > 0 {
        w2lo := sh.dyn_offsets[base]
        w2hi := if dc > 1 { sh.dyn_offsets[base + 1] } else { u32(0) }
        words[2] = PackU32Pair(w2lo, w2hi)
    }
    if dc > 2 {
        w3lo := sh.dyn_offsets[base + 2]
        w3hi := if dc > 3 { sh.dyn_offsets[base + 3] } else { u32(0) }
        words[3] = PackU32Pair(w3lo, w3hi)
    }
    _ = stream.WriteSlice(a, pool, words[0..wlen]) else {
        stream.RecordError(Misuse.LimitExceeded, stream.cmd_count)
        return false
    }
    return true
}

EmitImmediatesRecord(a: &Alloc into pool, sh: &PassShadow, stream: mut CommandStream, pool: mut ChunkPool, flags: u16): bool {
    if sh.layout_immediates <= 0 {
        return true
    }
    wcnt := (sh.layout_immediates + 7) / 8
    var words: [17]u64 = [0; 17]
    words[0] = PackHdr(OpSetImmediates, Cast[u8](1 + wcnt), flags, Cast[u32](sh.layout_immediates))
    for wi in 0..wcnt {
        bo := wi * 8
        var v: u64 = 0
        for k in 0..8 {
            if bo + k < sh.layout_immediates {
                v |= u64(sh.immediates[bo + k]) << Cast[u64](k * 8)
            }
        }
        words[1 + wi] = v
    }
    _ = stream.WriteSlice(a, pool, words[0..(1 + wcnt)]) else {
        stream.RecordError(Misuse.LimitExceeded, stream.cmd_count)
        return false
    }
    return true
}

CheckGroupsBound(st: mut DeviceState): bool {
    sh := &st.pass_shadow
    if sh.pipeline_slot == 0 {
        st.stream.RecordError(Misuse.NoPipeline, st.stream.cmd_count)
        return false
    }
    for gi in 0..sh.layout_group_count {
        gslot := sh.groups[gi]
        if gslot == 0 {
            st.stream.RecordError(Misuse.GroupMissing, st.stream.cmd_count)
            return false
        }
        bg := st.bind_groups.Get(gslot)
        if bg.layout_fp != sh.layout_group_fps[gi] {
            st.stream.RecordError(Misuse.GroupIncompatible, st.stream.cmd_count)
            return false
        }
    }
    return true
}

/// A render pass being recorded. Linear: End it.
export linear view struct RenderPass {
    enc: mut Encoder
    pipeline: u32
    dirty: u32
}

SetupRenderPassAttachments(a: &Alloc, st: mut DeviceState, serial: u32, d: &PassDesc): Invalid!(w: int, h: int, pass_idx: u32) {
    lim := st.limits
    if d.colors.Len() > lim.max_color_targets || (d.colors.Len() == 0 && d.depth.IsNone()) {
        fail Invalid(misuse: if d.colors.Len() > lim.max_color_targets { Misuse.LimitExceeded } else { Misuse.ExtentMismatch }, at: -1)
    }
    var w := -1
    var h := -1
    var samples := -1
    var prec := mem.Zeroed[PassStreamRec]()
    prec.color_count = d.colors.Len()
    for ci in 0..d.colors.Len() {
        ca := &d.colors[ci]
        if ca.target.device != serial || !st.views.IsLive(ca.target.slot, ca.target.generation) {
            fail Invalid(misuse: Misuse.StaleHandle, at: ci)
        }
        ve := st.views.Get(ca.target.slot)
        ve_fmt := ve.format
        ve_samples := ve.samples
        ve_tex := ve.tex_slot
        ve_nh := ve.native_handle
        ve_fm := ve.first_mip
        ve_mc := ve.mip_count
        ve_fl := ve.first_layer
        ve_lc := ve.layer_count
        ve_pm := ve.plane_mask
        ve_w := ve.width
        ve_h := ve.height
        if !ve.usage.render_target || ve_lc != 1 || IsDepthStencilFormat(ve_fmt) {
            fail Invalid(misuse: if IsDepthStencilFormat(ve_fmt) { Misuse.FormatMismatch } else { Misuse.BadUsage }, at: ci)
        }
        if w < 0 {
            w = ve_w
            h = ve_h
            samples = ve_samples
        } else if ve_w != w || ve_h != h {
            fail Invalid(misuse: Misuse.ExtentMismatch, at: ci)
        } else if ve_samples != samples {
            fail Invalid(misuse: Misuse.SamplesMismatch, at: ci)
        }
        st.pass_shadow.color_formats[ci] = ve_fmt
        st.pass_shadow.attaches[st.pass_shadow.attach_count] = PassAttachRec(
            tex_slot: ve_tex,
            first_mip: ve_fm,
            mip_count: ve_mc,
            first_layer: ve_fl,
            layer_count: ve_lc,
            plane_mask: ve_pm,
            state: ResRenderTarget,
            is_read_only_depth: false,
        )
        st.pass_shadow.attach_count += 1
        var c_tex_nh: u64 = 0
        var c_sub: u32 = 0
        if ve_tex != 0 {
            te := st.textures.Get(ve_tex)
            c_tex_nh = te.native_handle
            c_sub = Cast[u32](ve_fm + ve_fl * te.mips)
            try st.pass_shadow.RecordTexUse(a, PassTexUse(
                tex_slot: ve_tex,
                first_mip: ve_fm,
                mip_count: ve_mc,
                first_layer: ve_fl,
                layer_count: ve_lc,
                plane_mask: ve_pm,
                state: ResRenderTarget,
            ))
            st.textures.StampUse(ve_tex, st.serials.next)
        }
        st.views.StampUse(ca.target.slot, st.serials.next)

        var r_vnh: u64 = 0
        var r_tnh: u64 = 0
        var r_sub: u32 = 0
        match ca.resolve {
            some(rv) {
                if rv.device != serial || !st.views.IsLive(rv.slot, rv.generation) {
                    fail Invalid(misuse: Misuse.StaleHandle, at: ci)
                }
                rve := st.views.Get(rv.slot)
                if ve_samples != 4 || rve.samples != 1 {
                    fail Invalid(misuse: Misuse.SamplesMismatch, at: ci)
                }
                if rve.width != w || rve.height != h {
                    fail Invalid(misuse: Misuse.ExtentMismatch, at: ci)
                }
                if FormatIndex(rve.format) != FormatIndex(ve_fmt) {
                    fail Invalid(misuse: Misuse.FormatMismatch, at: ci)
                }
                r_vnh = rve.native_handle
                if rve.tex_slot != 0 {
                    rte := st.textures.Get(rve.tex_slot)
                    r_tnh = rte.native_handle
                    r_sub = Cast[u32](rve.first_mip + rve.first_layer * rte.mips)
                    try st.pass_shadow.RecordTexUse(a, PassTexUse(
                        tex_slot: rve.tex_slot,
                        first_mip: rve.first_mip,
                        mip_count: rve.mip_count,
                        first_layer: rve.first_layer,
                        layer_count: rve.layer_count,
                        plane_mask: rve.plane_mask,
                        state: ResRenderTarget,
                    ))
                    st.textures.StampUse(rve.tex_slot, st.serials.next)
                }
                st.views.StampUse(rv.slot, st.serials.next)
            }
            none {}
        }
        (op: lop, r: cr, g: cg, b: cb, a: ca_v) := match ca.load {
            .Clear(r, g, b, a: av) { (op: u8(0), r: Cast[f32](r), g: Cast[f32](g), b: Cast[f32](b), a: Cast[f32](av)) }
            .Keep { (op: u8(1), r: f32(0.0), g: f32(0.0), b: f32(0.0), a: f32(0.0)) }
            .DontCare { (op: u8(2), r: f32(0.0), g: f32(0.0), b: f32(0.0), a: f32(0.0)) }
        }
        sop := match ca.store {
            .Store { u8(0) }
            .Discard { u8(1) }
        }
        prec.colors[ci] = PassColorStreamRec(
            view_nh: ve_nh,
            tex_nh: c_tex_nh,
            subresource: c_sub,
            resolve_view_nh: r_vnh,
            resolve_tex_nh: r_tnh,
            resolve_sub: r_sub,
            dxgi_format: RowOf(ve_fmt).dxgi_rtv_dsv,
            load_op: lop,
            store_op: sop,
            clear_r: cr,
            clear_g: cg,
            clear_b: cb,
            clear_a: ca_v,
        )
    }
    match d.depth {
        some(da) {
            if da.target.device != serial || !st.views.IsLive(da.target.slot, da.target.generation) {
                fail Invalid(misuse: Misuse.StaleHandle, at: -1)
            }
            dve := st.views.Get(da.target.slot)
            dve_fmt := dve.format
            if !dve.usage.render_target || dve.layer_count != 1 || !IsDepthStencilFormat(dve_fmt) {
                fail Invalid(misuse: if !IsDepthStencilFormat(dve_fmt) { Misuse.FormatMismatch } else { Misuse.BadUsage }, at: -1)
            }
            if w >= 0 && (dve.width != w || dve.height != h) {
                fail Invalid(misuse: Misuse.ExtentMismatch, at: -1)
            }
            if samples >= 0 && dve.samples != samples {
                fail Invalid(misuse: Misuse.SamplesMismatch, at: -1)
            }
            if w < 0 {
                w = dve.width
                h = dve.height
                samples = dve.samples
            }
            d_state := if da.read_only { ResDepthRead } else { ResDepthWrite }
            st.pass_shadow.depth_format = some(dve_fmt)
            st.pass_shadow.attaches[st.pass_shadow.attach_count] = PassAttachRec(
                tex_slot: dve.tex_slot,
                first_mip: dve.first_mip,
                mip_count: dve.mip_count,
                first_layer: dve.first_layer,
                layer_count: dve.layer_count,
                plane_mask: dve.plane_mask,
                state: d_state,
                is_read_only_depth: da.read_only,
            )
            st.pass_shadow.attach_count += 1
            dve_nh := dve.native_handle
            dve_tex := dve.tex_slot
            dve_fm := dve.first_mip
            dve_mc := dve.mip_count
            dve_fl := dve.first_layer
            dve_lc := dve.layer_count
            dve_pm := dve.plane_mask
            st.views.StampUse(da.target.slot, st.serials.next)
            if dve_tex != 0 {
                try st.pass_shadow.RecordTexUse(a, PassTexUse(
                    tex_slot: dve_tex,
                    first_mip: dve_fm,
                    mip_count: dve_mc,
                    first_layer: dve_fl,
                    layer_count: dve_lc,
                    plane_mask: dve_pm,
                    state: d_state,
                ))
                st.textures.StampUse(dve_tex, st.serials.next)
            }
            (op: dlop, val: dclr) := match da.depth_load {
                .Clear(depth) { (op: u8(0), val: depth) }
                .Keep { (op: u8(1), val: f32(0.0)) }
                .DontCare { (op: u8(2), val: f32(0.0)) }
            }
            (op: slop, val: sclr) := match da.stencil_load {
                .Clear(value) { (op: u8(0), val: Trunc[u8](value)) }
                .Keep { (op: u8(1), val: u8(0)) }
                .DontCare { (op: u8(2), val: u8(0)) }
            }
            dsop := match da.depth_store {
                .Store { u8(0) }
                .Discard { u8(1) }
            }
            ssop := match da.stencil_store {
                .Store { u8(0) }
                .Discard { u8(1) }
            }
            prec.depth = PassDepthStreamRec(
                has_depth: true,
                view_nh: dve_nh,
                read_only: da.read_only,
                depth_load: dlop,
                depth_store: dsop,
                stencil_load: slop,
                stencil_store: ssop,
                depth_clear: dclr,
                stencil_clear: sclr,
                has_stencil: (RowOf(dve_fmt).aspects & AspectBitStencil) != 0,
            )
        }
        none {}
    }
    prec.width = w
    prec.height = h
    st.stream.passes.Push(a, prec) else {
        fail Invalid(misuse: Misuse.LimitExceeded, at: -1)
    }
    pidx := Cast[u32](st.stream.passes.Len() - 1)
    st.pass_shadow.pass_idx = pidx
    st.pass_shadow.extent_w = w
    st.pass_shadow.extent_h = h
    st.pass_shadow.color_count = d.colors.Len()
    st.pass_shadow.samples = samples
    st.pass_shadow.vp_w = Cast[f32](w)
    st.pass_shadow.vp_h = Cast[f32](h)
    st.pass_shadow.sc_w = w
    st.pass_shadow.sc_h = h
    return (w: w, h: h, pass_idx: pidx)
}

/// Begins a render pass; viewport and scissor are the attachments' extent,
/// blend constant 0, stencil reference 0. Misuse (ExtentMismatch, stale
/// views) is recorded and returned by Submit. O(attachments). Allocates
/// nothing.
export Encoder.RenderPass(mut self, d: &PassDesc): RenderPass from self {
    a := self.dev.heap.Alloc()
    st := self.dev.state.Mut()
    st.pass_shadow.Reset(0, 0)
    st.stream.pass_debug_base = st.stream.debug_depth
    if st.stream.HasError() {
        return RenderPass(enc: self, pipeline: 0, dirty: 0)
    }
    (w, h, pass_idx) := SetupRenderPassAttachments(a, st, self.dev.serial, d) else e {
        st.stream.RecordError(e.misuse, st.stream.cmd_count)
        return RenderPass(enc: self, pipeline: 0, dirty: 0)
    }
    loc := st.stream.Write2(a, mut st.chunk_pool, PackHdr(OpBarrierRef, 2, 0, 0), 0) else {
        st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
        return RenderPass(enc: self, pipeline: 0, dirty: 0)
    }
    st.pass_shadow.barrier_loc = loc
    is_dx := match &st.native {
        .Dx(d: _) { true }
        _ { false }
    }
    if is_dx {
        _ = st.stream.Write2(a, mut st.chunk_pool, PackHdr(OpBeginRendering, 2, Cast[u16](d.colors.Len()), pass_idx), PackU32Pair(Cast[u32](w), Cast[u32](h))) else {
            st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
        }
        return RenderPass(enc: self, pipeline: 0, dirty: 0)
    }
    cc := d.colors.Len()
    has_d := d.depth.IsSome()
    var words: [60]u64 = [0; 60]
    flags := Cast[u16](cc) | (if has_d { u16(0x10) } else { u16(0) })
    var dflags: u32 = 0
    var dview_nh: u64 = 0
    var dclear_u: u32 = 0
    var sclear_u: u32 = 0
    match d.depth {
        some(da) {
            dview_nh = st.views.Get(da.target.slot).native_handle
            dl: u32 = match da.depth_load {
                .Clear(depth) {
                    dclear_u = F32ToU32(depth)
                    0
                }
                .Keep { 1 }
                .DontCare { 2 }
            }
            ds: u32 = match da.depth_store {
                .Store { 0 }
                .Discard { 1 }
            }
            sl: u32 = match da.stencil_load {
                .Clear(value) {
                    sclear_u = value
                    0
                }
                .Keep { 1 }
                .DontCare { 2 }
            }
            ss: u32 = match da.stencil_store {
                .Store { 0 }
                .Discard { 1 }
            }
            dve := st.views.Get(da.target.slot)
            ro_bit: u32 = if da.read_only { 64 } else { 0 }
            has_s_bit: u32 = if HasStencilAspect(dve.format) { 128 } else { 0 }
            dflags = dl | (ds << 2) | (sl << 3) | (ss << 5) | ro_bit | has_s_bit
        }
        none {}
    }
    total_w := 2 + cc * 7 + (if has_d { 2 } else { 0 })
    words[0] = PackHdr(OpBeginRendering, Cast[u8](total_w), flags, Cast[u32](w))
    words[1] = PackU32Pair(Cast[u32](h), dflags)
    var cur := 2
    for ci in 0..cc {
        ca := &d.colors[ci]
        ve := st.views.Get(ca.target.slot)
        tnh := ve.native_handle
        rnh := match ca.resolve {
            some(rv) { st.views.Get(rv.slot).native_handle }
            none { 0 }
        }
        var cr: f64 = 0.0
        var cg: f64 = 0.0
        var cb: f64 = 0.0
        var ca_v: f64 = 0.0
        cl: u32 = match ca.load {
            .Clear(r, g, b, a: a_v) {
                cr = r
                cg = g
                cb = b
                ca_v = a_v
                0
            }
            .Keep { 1 }
            .DontCare { 2 }
        }
        cs: u32 = match ca.store {
            .Store { 0 }
            .Discard { 1 }
        }
        nk := u32(RowOf(ve.format).num_class)
        meta := cl | (cs << 2) | (nk << 3)
        words[cur] = tnh
        words[cur + 1] = rnh
        words[cur + 2] = PackU32Pair(meta, 0)
        words[cur + 3] = cr.Bits()
        words[cur + 4] = cg.Bits()
        words[cur + 5] = cb.Bits()
        words[cur + 6] = ca_v.Bits()
        cur += 7
    }
    if has_d {
        words[cur] = dview_nh
        words[cur + 1] = PackU32Pair(dclear_u, sclear_u)
        cur += 2
    }
    _ = st.stream.WriteSlice(a, mut st.chunk_pool, words[0..cur]) else {
        st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
    }
    return RenderPass(enc: self, pipeline: 0, dirty: 0)
}

/// Binds a pipeline; bindings and immediates of an incompatible layout
/// become unbound. O(1).
export RenderPass.SetPipeline(mut self, p: &RenderPipeline) {
    st := self.enc.dev.state.Mut()
    if st.stream.HasError() {
        return
    }
    if p.device != self.enc.dev.serial || !st.render_pipes.IsLive(p.slot, p.generation) {
        st.stream.RecordError(Misuse.StaleHandle, st.stream.cmd_count)
        return
    }
    pe := st.render_pipes.Get(p.slot)
    pe_tcnt := pe.target_count
    pe_samples := pe.samples
    pe_tfmts := pe.target_formats
    pe_dfmt := pe.depth_format
    new_layout := pe.layout_slot
    gcnt := pe.group_count
    gfps := pe.group_fps
    imm := pe.immediates
    vcnt := pe.vertex_slot_count
    vstrides := pe.vertex_strides

    sh := mut st.pass_shadow
    if pe_tcnt != sh.color_count || pe_samples != sh.samples {
        st.stream.RecordError(if pe_samples != sh.samples { Misuse.SamplesMismatch } else { Misuse.FormatMismatch }, st.stream.cmd_count)
        return
    }
    for ti in 0..pe_tcnt {
        if FormatIndex(pe_tfmts[ti]) != FormatIndex(sh.color_formats[ti]) {
            st.stream.RecordError(Misuse.FormatMismatch, st.stream.cmd_count)
            return
        }
    }
    match pe_dfmt {
        some(a) {
            match sh.depth_format {
                some(b) {
                    if FormatIndex(a) != FormatIndex(b) {
                        st.stream.RecordError(Misuse.FormatMismatch, st.stream.cmd_count)
                        return
                    }
                }
                none {
                    st.stream.RecordError(Misuse.FormatMismatch, st.stream.cmd_count)
                    return
                }
            }
        }
        none {
            if sh.depth_format.IsSome() {
                st.stream.RecordError(Misuse.FormatMismatch, st.stream.cmd_count)
                return
            }
        }
    }
    st.render_pipes.StampUse(p.slot, st.serials.next)
    if sh.pipeline_slot == p.slot {
        return
    }
    old_layout := sh.layout_slot

    sh.pipeline_slot = p.slot
    sh.layout_slot = new_layout
    sh.layout_group_count = gcnt
    sh.layout_group_fps = gfps
    sh.layout_immediates = imm
    sh.dirty |= DirtyPipeline
    self.pipeline = p.slot

    if old_layout != new_layout {
        ApplyLayoutChangeGroups(st, new_layout, gcnt, gfps, imm)
    }
    sh2 := mut st.pass_shadow
    for vi in 0..vcnt {
        if vstrides[vi] != sh2.pipe_vb_strides[vi] {
            sh2.pipe_vb_strides[vi] = vstrides[vi]
            if sh2.vb_slots[vi] != 0 {
                sh2.dirty |= (DirtyVertexBase << Cast[u32](vi))
            }
        }
    }
    sh2.pipe_vb_count = vcnt
    self.dirty = sh2.dirty
}

/// Binds group g at index; dynamic holds one offset per dynamic entry, in
/// binding order. O(dynamic).
export RenderPass.SetBindGroup(mut self, index: int, g: &BindGroup, dynamic: []u32) {
    st := self.enc.dev.state.Mut()
    if ShadowSetBindGroup(st, self.enc.dev.serial, index, g, dynamic) {
        self.dirty = st.pass_shadow.dirty
    }
}

/// Sets immediates bytes at offset (multiples of 4). O(bytes).
export RenderPass.SetImmediates(mut self, offset: int, data: []u8) {
    st := self.enc.dev.state.Mut()
    if ShadowSetImmediates(st, offset, data) {
        self.dirty = st.pass_shadow.dirty
    }
}

/// Binds a vertex buffer slot from offset to the buffer's end. O(1).
export RenderPass.SetVertexBuffer(mut self, slot: int, b: &Buffer, offset: int) {
    st := self.enc.dev.state.Mut()
    if st.stream.HasError() {
        return
    }
    if slot < 0 || slot >= st.limits.max_vertex_buffers {
        st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
        return
    }
    if b.device != self.enc.dev.serial || !st.buffers.IsLive(b.slot, b.generation) {
        st.stream.RecordError(Misuse.StaleHandle, st.stream.cmd_count)
        return
    }
    be := st.buffers.Get(b.slot)
    be_size := be.size
    if !be.usage.vertex {
        st.stream.RecordError(Misuse.BadUsage, st.stream.cmd_count)
        return
    }
    if offset < 0 || offset > be_size {
        st.stream.RecordError(Misuse.RangeOutOfBounds, st.stream.cmd_count)
        return
    }
    st.buffers.StampUse(b.slot, st.serials.next)
    sh := mut st.pass_shadow
    if sh.vb_slots[slot] == b.slot && sh.vb_offsets[slot] == offset && sh.vb_sizes[slot] == be_size - offset {
        return
    }
    sh.vb_slots[slot] = b.slot
    sh.vb_offsets[slot] = offset
    sh.vb_sizes[slot] = be_size - offset
    sh.dirty |= (DirtyVertexBase << Cast[u32](slot))
    self.dirty = sh.dirty
}

/// Binds the index buffer. O(1).
export RenderPass.SetIndexBuffer(mut self, b: &Buffer, format: IndexFormat, offset: int) {
    st := self.enc.dev.state.Mut()
    if st.stream.HasError() {
        return
    }
    if b.device != self.enc.dev.serial || !st.buffers.IsLive(b.slot, b.generation) {
        st.stream.RecordError(Misuse.StaleHandle, st.stream.cmd_count)
        return
    }
    be := st.buffers.Get(b.slot)
    be_size := be.size
    if !be.usage.index {
        st.stream.RecordError(Misuse.BadUsage, st.stream.cmd_count)
        return
    }
    esz := IndexByteSize(format)
    if offset < 0 || offset > be_size || offset % esz != 0 {
        st.stream.RecordError(if offset % esz != 0 { Misuse.AlignmentOffset } else { Misuse.RangeOutOfBounds }, st.stream.cmd_count)
        return
    }
    st.buffers.StampUse(b.slot, st.serials.next)
    sh := mut st.pass_shadow
    if sh.ib_slot == b.slot && sh.ib_offset == offset && sh.ib_fmt == format {
        return
    }
    sh.ib_slot = b.slot
    sh.ib_offset = offset
    sh.ib_size = be_size - offset
    sh.ib_fmt = format
    sh.dirty |= DirtyIndex
    self.dirty = sh.dirty
}

/// Sets the viewport, in framebuffer pixels with Y down. O(1).
export RenderPass.SetViewport(mut self, x: f32, y: f32, w: f32, h: f32, min_depth: f32, max_depth: f32) {
    st := self.enc.dev.state.Mut()
    if st.stream.HasError() {
        return
    }
    if w <= 0.0 || h <= 0.0 || min_depth < 0.0 || max_depth > 1.0 || min_depth > max_depth {
        st.stream.RecordError(Misuse.ExtentMismatch, st.stream.cmd_count)
        return
    }
    sh := mut st.pass_shadow
    if sh.vp_x == x && sh.vp_y == y && sh.vp_w == w && sh.vp_h == h && sh.vp_min == min_depth && sh.vp_max == max_depth {
        return
    }
    sh.vp_x = x
    sh.vp_y = y
    sh.vp_w = w
    sh.vp_h = h
    sh.vp_min = min_depth
    sh.vp_max = max_depth
    sh.dirty |= DirtyViewport
    self.dirty = sh.dirty
}

/// Sets the scissor rectangle, clamped to the attachments. O(1).
export RenderPass.SetScissor(mut self, x: int, y: int, w: int, h: int) {
    st := self.enc.dev.state.Mut()
    if st.stream.HasError() {
        return
    }
    if x < 0 || y < 0 || w < 0 || h < 0 {
        st.stream.RecordError(Misuse.ExtentMismatch, st.stream.cmd_count)
        return
    }
    sh := mut st.pass_shadow
    cw := if x + w > sh.extent_w { if sh.extent_w > x { sh.extent_w - x } else { 0 } } else { w }
    ch := if y + h > sh.extent_h { if sh.extent_h > y { sh.extent_h - y } else { 0 } } else { h }
    if sh.sc_x == x && sh.sc_y == y && sh.sc_w == cw && sh.sc_h == ch {
        return
    }
    sh.sc_x = x
    sh.sc_y = y
    sh.sc_w = cw
    sh.sc_h = ch
    sh.dirty |= DirtyScissor
    self.dirty = sh.dirty
}

/// Sets the blend constant. O(1).
export RenderPass.SetBlendConstant(mut self, r: f32, g: f32, b: f32, a: f32) {
    st := self.enc.dev.state.Mut()
    if st.stream.HasError() {
        return
    }
    sh := mut st.pass_shadow
    if sh.blend_r == r && sh.blend_g == g && sh.blend_b == b && sh.blend_a == a {
        return
    }
    sh.blend_r = r
    sh.blend_g = g
    sh.blend_b = b
    sh.blend_a = a
    sh.dirty |= DirtyBlend
    self.dirty = sh.dirty
}

/// Sets the stencil reference. O(1).
export RenderPass.SetStencilReference(mut self, value: u32) {
    st := self.enc.dev.state.Mut()
    if st.stream.HasError() {
        return
    }
    sh := mut st.pass_shadow
    if sh.stencil_ref == value {
        return
    }
    sh.stencil_ref = value
    sh.dirty |= DirtyStencil
    self.dirty = sh.dirty
}

RenderPass.FlushDraw(mut self, need_index: bool): bool {
    a := self.enc.dev.heap.Alloc()
    st := self.enc.dev.state.Mut()
    if !CheckGroupsBound(st) {
        return false
    }
    sh := mut st.pass_shadow
    for vi in 0..sh.pipe_vb_count {
        if sh.vb_slots[vi] == 0 {
            st.stream.RecordError(Misuse.VertexBufferMissing, st.stream.cmd_count)
            return false
        }
    }
    if need_index && sh.ib_slot == 0 {
        st.stream.RecordError(Misuse.IndexBufferMissing, st.stream.cmd_count)
        return false
    }
    if (sh.dirty & DirtyPipeline) != 0 {
        nh := st.render_pipes.Get(sh.pipeline_slot).native_handle
        _ = st.stream.Write2(a, mut st.chunk_pool, PackHdr(OpSetPipeline, 2, 0, 0), nh) else {
            st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
            return false
        }
    }
    for gi in 0..sh.layout_group_count {
        if (sh.dirty & (DirtyGroup0 << Cast[u32](gi))) != 0 {
            gslot := sh.groups[gi]
            bg := st.bind_groups.Get(gslot)
            for ti in 0..bg.texs.Len() {
                bt := &bg.texs[ti]
                for ai in 0..sh.attach_count {
                    att := &sh.attaches[ai]
                    if PassTexUseOverlaps(att, bt) && !att.is_read_only_depth {
                        st.stream.RecordError(Misuse.AttachmentAlsoBound, st.stream.cmd_count)
                        return false
                    }
                }
                if bt.tex_slot != 0 {
                    st.textures.StampUse(bt.tex_slot, st.serials.next)
                    use_st := if bt.is_depth { ResDepthRead | ResShaderRead } else { ResShaderRead }
                    sh.RecordTexUse(a, PassTexUse(
                        tex_slot: bt.tex_slot,
                        first_mip: bt.first_mip,
                        mip_count: bt.mip_count,
                        first_layer: bt.first_layer,
                        layer_count: bt.layer_count,
                        plane_mask: bt.plane_mask,
                        state: use_st,
                    )) else e {
                        st.stream.RecordError(e.misuse, st.stream.cmd_count)
                        return false
                    }
                }
            }
            for bi in 0..bg.bufs.Len() {
                bb := &bg.bufs[bi]
                st.buffers.StampUse(bb.buf_slot, st.serials.next)
                bst := if bb.is_storage { ResShaderRead } else { ResUniform }
                sh.RecordBufUse(a, bb.buf_slot, bst) else e {
                    st.stream.RecordError(e.misuse, st.stream.cmd_count)
                    return false
                }
            }
            if !EmitSetGroupRecord(a, &*sh, mut st.stream, mut st.chunk_pool, gi, bg.native_handle) {
                return false
            }
        }
    }
    if (sh.dirty & DirtyImmediates) != 0 {
        if !EmitImmediatesRecord(a, &*sh, mut st.stream, mut st.chunk_pool, 0) {
            return false
        }
    }
    for vi in 0..sh.pipe_vb_count {
        if (sh.dirty & (DirtyVertexBase << Cast[u32](vi))) != 0 {
            sh.RecordBufUse(a, sh.vb_slots[vi], ResVertex) else e {
                st.stream.RecordError(e.misuse, st.stream.cmd_count)
                return false
            }
            nh := st.buffers.Get(sh.vb_slots[vi]).native_handle
            _ = st.stream.Write3(a, mut st.chunk_pool, PackHdr(OpSetVertexBuffers, 3, Cast[u16](vi), Cast[u32](sh.pipe_vb_strides[vi])), nh, PackU32Pair(Cast[u32](sh.vb_offsets[vi]), Cast[u32](sh.vb_sizes[vi]))) else {
                st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
                return false
            }
        }
    }
    if (sh.dirty & DirtyIndex) != 0 && sh.ib_slot != 0 {
        sh.RecordBufUse(a, sh.ib_slot, ResIndex) else e {
            st.stream.RecordError(e.misuse, st.stream.cmd_count)
            return false
        }
        nh := st.buffers.Get(sh.ib_slot).native_handle
        fmt_u := if sh.ib_fmt == IndexFormat.Uint32 { u16(1) } else { u16(0) }
        _ = st.stream.Write3(a, mut st.chunk_pool, PackHdr(OpSetIndexBuffer, 3, fmt_u, Cast[u32](sh.ib_size)), nh, Cast[u64](sh.ib_offset)) else {
            st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
            return false
        }
    }
    if (sh.dirty & DirtyViewport) != 0 {
        _ = st.stream.Write4(a, mut st.chunk_pool, PackHdr(OpSetViewport, 4, 0, 0), PackU32Pair(F32ToU32(sh.vp_x), F32ToU32(sh.vp_y)), PackU32Pair(F32ToU32(sh.vp_w), F32ToU32(sh.vp_h)), PackU32Pair(F32ToU32(sh.vp_min), F32ToU32(sh.vp_max))) else {
            st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
            return false
        }
    }
    if (sh.dirty & DirtyScissor) != 0 {
        _ = st.stream.Write3(a, mut st.chunk_pool, PackHdr(OpSetScissor, 3, 0, 0), PackU32Pair(Cast[u32](sh.sc_x), Cast[u32](sh.sc_y)), PackU32Pair(Cast[u32](sh.sc_w), Cast[u32](sh.sc_h))) else {
            st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
            return false
        }
    }
    if (sh.dirty & DirtyBlend) != 0 {
        _ = st.stream.Write3(a, mut st.chunk_pool, PackHdr(OpSetBlendConst, 3, 0, 0), PackU32Pair(F32ToU32(sh.blend_r), F32ToU32(sh.blend_g)), PackU32Pair(F32ToU32(sh.blend_b), F32ToU32(sh.blend_a))) else {
            st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
            return false
        }
    }
    if (sh.dirty & DirtyStencil) != 0 {
        _ = st.stream.Write2(a, mut st.chunk_pool, PackHdr(OpSetStencilRef, 2, 0, sh.stencil_ref), 0) else {
            st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
            return false
        }
    }
    sh.dirty = 0
    self.dirty = 0
    return true
}

/// Draws vertices. SV_VertexID excludes first_vertex and SV_InstanceID
/// excludes first_instance on both backends, as on D3D12. O(1); see the per-draw budget.
export RenderPass.Draw(mut self, vertices: int, instances: int, first_vertex: int, first_instance: int) {
    if self.enc.dev.state.Ref().stream.HasError() {
        return
    }
    if self.dirty != 0 || self.pipeline == 0 {
        if !self.FlushDraw(false) {
            return
        }
    }
    if vertices < 0 || instances < 0 || first_vertex < 0 || first_instance < 0 {
        self.enc.RecordMisuse(Misuse.RangeOutOfBounds)
        return
    }
    a := self.enc.dev.heap.Alloc()
    st := self.enc.dev.state.Mut()
    _ = st.stream.Write3(a, mut st.chunk_pool, PackHdr(OpDraw, 3, 0, Cast[u32](vertices)), PackU32Pair(Cast[u32](instances), Cast[u32](first_vertex)), Cast[u64](first_instance)) else {
        st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
    }
}

IndexRangeFits(sh: &PassShadow, indices: int, first_index: int): bool {
    esz := i64(IndexByteSize(sh.ib_fmt))
    return (i64(first_index) + i64(indices)) * esz <= i64(sh.ib_size)
}

/// Draws indexed vertices; first_index + indices must fit the index buffer.
export RenderPass.DrawIndexed(mut self, indices: int, instances: int, first_index: int, base_vertex: int, first_instance: int) {
    if self.enc.dev.state.Ref().stream.HasError() {
        return
    }
    if self.dirty != 0 || self.pipeline == 0 || self.enc.dev.state.Ref().pass_shadow.ib_slot == 0 {
        if !self.FlushDraw(true) {
            return
        }
    }
    if indices < 0 || instances < 0 || first_index < 0 || first_instance < 0 {
        self.enc.RecordMisuse(Misuse.RangeOutOfBounds)
        return
    }
    if !IndexRangeFits(&self.enc.dev.state.Ref().pass_shadow, indices, first_index) {
        self.enc.RecordMisuse(Misuse.RangeOutOfBounds)
        return
    }
    a := self.enc.dev.heap.Alloc()
    st := self.enc.dev.state.Mut()
    _ = st.stream.Write4(a, mut st.chunk_pool, PackHdr(OpDrawIndexed, 4, 0, Cast[u32](indices)), PackU32Pair(Cast[u32](instances), Cast[u32](first_index)), PackU32Pair(Trunc[u32](base_vertex), Cast[u32](first_instance)), 0) else {
        st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
    }
}

/// Opens a debug group. O(label).
export RenderPass.PushDebugGroup(mut self, label: str) {
    self.enc.PushDebugGroup(label)
}

/// Closes the innermost debug group. O(1).
export RenderPass.PopDebugGroup(mut self) {
    st := self.enc.dev.state.Mut()
    if st.stream.HasError() {
        return
    }
    if st.stream.debug_depth <= st.stream.pass_debug_base {
        st.stream.RecordError(Misuse.DebugGroupUnbalanced, st.stream.cmd_count)
        return
    }
    self.enc.PopDebugGroup()
}

/// Ends the pass. O(1).
export RenderPass.End(self) {
    RenderPass(enc, pipeline: _, dirty: _) := self
    a := enc.dev.heap.Alloc()
    st := enc.dev.state.Mut()
    if st.stream.debug_depth != st.stream.pass_debug_base {
        st.stream.RecordError(Misuse.DebugGroupUnbalanced, st.stream.cmd_count)
    }
    if !st.stream.HasError() {
        bstart := st.stream.barriers.Len()
        bug := st.planted_bug
        serial := st.serials.next
        for i in 0..st.pass_shadow.tex_uses.Len() {
            u := &st.pass_shadow.tex_uses[i]
            tex := st.textures.GetMut(u.tex_slot)
            TransitionTexture(a, u.tex_slot, mut tex.state, u.first_mip, u.mip_count, u.first_layer, u.layer_count, u.plane_mask, u.state, mut st.stream.barriers, mut st.stream.undo, bug) else {
                st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
                break
            }
        }
        for i in 0..st.pass_shadow.buf_uses.Len() {
            bu := &st.pass_shadow.buf_uses[i]
            buf := st.buffers.GetMut(bu.buf_slot)
            TransitionBuffer(a, bu.buf_slot, mut buf.state, serial, bu.state, mut st.stream.barriers, mut st.stream.undo, bug) else {
                st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
                break
            }
        }
        bcnt := st.stream.barriers.Len() - bstart
        st.stream.PatchBarrierRef(mut st.chunk_pool, st.pass_shadow.barrier_loc, Cast[u32](bstart), Cast[u32](bcnt), 0)
        _ = st.stream.Write1(a, mut st.chunk_pool, PackHdr(OpEndRendering, 1, 0, st.pass_shadow.pass_idx)) else {
            st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
        }
    }
}
