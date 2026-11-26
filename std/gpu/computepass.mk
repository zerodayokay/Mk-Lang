// Why: compute passes record dispatches, validate read/write storage
// conflicts, emit UAV/texture barriers, and track dirty shadow state
// with zero heap allocation after warm-up.
//
// What: ComputePass, Encoder.ComputePass, SetPipeline, SetBindGroup,
// SetImmediates, Dispatch, PushDebugGroup, PopDebugGroup, End.
//
// How: borrows the encoder's device state, checks write/read buffer hazards
// before dispatching, transitions bound resources, and flushes dirty shadow
// state into CommandStream.

/// A compute pass being recorded. Linear: End it.
export linear view struct ComputePass {
    enc: mut Encoder
    pipeline: u32
    dirty: u32
}

/// Begins a compute pass. O(1).
export Encoder.ComputePass(mut self, label: str): ComputePass from self {
    _ = label
    st := self.dev.state.Mut()
    st.pass_shadow.Reset(0, 0)
    st.stream.pass_debug_base = st.stream.debug_depth
    return ComputePass(enc: self, pipeline: 0, dirty: 0)
}

/// Binds a compute pipeline. O(1).
export ComputePass.SetPipeline(mut self, p: &ComputePipeline) {
    st := self.enc.dev.state.Mut()
    if st.stream.HasError() {
        return
    }
    if p.device != self.enc.dev.serial || !st.compute_pipes.IsLive(p.slot, p.generation) {
        st.stream.RecordError(Misuse.StaleHandle, st.stream.cmd_count)
        return
    }
    ce := st.compute_pipes.Get(p.slot)
    new_layout := ce.layout_slot
    gcnt := ce.group_count
    gfps := ce.group_fps
    imm := ce.immediates
    st.compute_pipes.StampUse(p.slot, st.serials.next)
    sh := mut st.pass_shadow
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
    self.dirty = st.pass_shadow.dirty
}

/// Binds group g at index, as RenderPass.SetBindGroup.
export ComputePass.SetBindGroup(mut self, index: int, g: &BindGroup, dynamic: []u32) {
    st := self.enc.dev.state.Mut()
    if ShadowSetBindGroup(st, self.enc.dev.serial, index, g, dynamic) {
        self.dirty = st.pass_shadow.dirty
    }
}

/// Sets immediates bytes at offset. O(bytes).
export ComputePass.SetImmediates(mut self, offset: int, data: []u8) {
    st := self.enc.dev.state.Mut()
    if ShadowSetImmediates(st, offset, data) {
        self.dirty = st.pass_shadow.dirty
    }
}

/// Dispatches x by y by z workgroups, each at most the limit. O(1).
export ComputePass.Dispatch(mut self, x: int, y: int, z: int) {
    a := self.enc.dev.heap.Alloc()
    st := self.enc.dev.state.Mut()
    if st.stream.HasError() {
        return
    }
    lim := st.limits
    if x < 0 || y < 0 || z < 0 || x > lim.max_workgroups_per_dimension || y > lim.max_workgroups_per_dimension || z > lim.max_workgroups_per_dimension {
        st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
        return
    }
    if !CheckGroupsBound(st) {
        return
    }
    sh := mut st.pass_shadow
    var w_slots: [64]u32 = [0; 64]
    var w_count := 0
    var r_slots: [64]u32 = [0; 64]
    var r_count := 0
    var any_writable := false
    for gi in 0..sh.layout_group_count {
        bg := st.bind_groups.Get(sh.groups[gi])
        for bi in 0..bg.bufs.Len() {
            bb := &bg.bufs[bi]
            if bb.is_storage && !bb.read_only {
                any_writable = true
                if w_count < 64 {
                    w_slots[w_count] = bb.buf_slot
                    w_count += 1
                }
            } else {
                if r_count < 64 {
                    r_slots[r_count] = bb.buf_slot
                    r_count += 1
                }
            }
        }
    }
    for wi in 0..w_count {
        for ri in 0..r_count {
            if w_slots[wi] == r_slots[ri] {
                st.stream.RecordError(Misuse.WriteReadConflict, st.stream.cmd_count)
                return
            }
        }
    }

    bstart := st.stream.barriers.Len()
    serial := st.serials.next
    bug := st.planted_bug
    skip_uav := bug == PlantedBug.SkipComputeUavBarrier
    for gi in 0..sh.layout_group_count {
        bg := st.bind_groups.Get(sh.groups[gi])
        for ti in 0..bg.texs.Len() {
            bt := &bg.texs[ti]
            if bt.tex_slot != 0 {
                st.textures.StampUse(bt.tex_slot, serial)
                tex := st.textures.GetMut(bt.tex_slot)
                TransitionTexture(a, bt.tex_slot, mut tex.state, bt.first_mip, bt.mip_count, bt.first_layer, bt.layer_count, bt.plane_mask, ResShaderRead, mut st.stream.barriers, mut st.stream.undo, bug) else {
                    st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
                    return
                }
            }
        }
        for bi in 0..bg.bufs.Len() {
            bb := &bg.bufs[bi]
            st.buffers.StampUse(bb.buf_slot, serial)
            need := if bb.is_storage && !bb.read_only { ResShaderWrite } else if bb.is_storage { ResShaderRead } else { ResUniform }
            if !(skip_uav && need == ResShaderWrite && st.buffers.Get(bb.buf_slot).state.promoted == ResShaderWrite) {
                buf := st.buffers.GetMut(bb.buf_slot)
                TransitionBuffer(a, bb.buf_slot, mut buf.state, serial, need, mut st.stream.barriers, mut st.stream.undo, bug) else {
                    st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
                    return
                }
            }
        }
    }
    bcnt := st.stream.barriers.Len() - bstart
    if bcnt > 0 {
        _ = st.stream.Write2(a, mut st.chunk_pool, PackHdr(OpBarrierRef, 2, 0, Cast[u32](bstart)), PackU32Pair(Cast[u32](bcnt), 0)) else {
            st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
            return
        }
    }

    if (sh.dirty & DirtyPipeline) != 0 {
        nh := st.compute_pipes.Get(sh.pipeline_slot).native_handle
        _ = st.stream.Write2(a, mut st.chunk_pool, PackHdr(OpSetPipeline, 2, 1, 0), nh) else {
            st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
            return
        }
    }
    for gi in 0..sh.layout_group_count {
        if (sh.dirty & (DirtyGroup0 << Cast[u32](gi))) != 0 {
            nh := st.bind_groups.Get(sh.groups[gi]).native_handle
            if !EmitSetGroupRecord(a, &*sh, mut st.stream, mut st.chunk_pool, gi, nh) {
                return
            }
        }
    }
    if (sh.dirty & DirtyImmediates) != 0 {
        if !EmitImmediatesRecord(a, &*sh, mut st.stream, mut st.chunk_pool, 1) {
            return
        }
    }
    sh.dirty = 0
    self.dirty = 0
    sh.prev_compute_write = any_writable

    _ = st.stream.Write3(a, mut st.chunk_pool, PackHdr(OpDispatch, 3, 0, Cast[u32](x)), PackU32Pair(Cast[u32](y), Cast[u32](z)), 0) else {
        st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
    }
}

/// Opens a debug group. O(label).
export ComputePass.PushDebugGroup(mut self, label: str) {
    self.enc.PushDebugGroup(label)
}

/// Closes the innermost debug group. O(1).
export ComputePass.PopDebugGroup(mut self) {
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
export ComputePass.End(self) {
    ComputePass(enc, pipeline: _, dirty: _) := self
    st := enc.dev.state.Mut()
    if st.stream.debug_depth != st.stream.pass_debug_base {
        st.stream.RecordError(Misuse.DebugGroupUnbalanced, st.stream.cmd_count)
    }
}
