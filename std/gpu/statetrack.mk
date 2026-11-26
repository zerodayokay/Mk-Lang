// Why: D3D12 and Vulkan require exact per-subresource image transitions and
// buffer memory barriers, plus D3D12 COMMON-decay tracking across command lists
// and rollback when an encoder is abandoned.
//
// What: ResState bitmasks, TexState, BufState, BarrierItem, UndoOp, PlantedBug
// (Test profile hook), and StatePlanner transition functions.
//
// How: pure O(subresources) planner with O(1) uniform-texture fast path and an
// undo log replayed backwards on Encoder.Abandon.

import std.mem

const ResCommon: u16 = 0
const ResVertex: u16 = 1
const ResIndex: u16 = 2
const ResUniform: u16 = 4
const ResShaderRead: u16 = 8
const ResShaderWrite: u16 = 16
const ResRenderTarget: u16 = 32
const ResDepthWrite: u16 = 64
const ResDepthRead: u16 = 128
const ResCopySrc: u16 = 256
const ResCopyDst: u16 = 512
const ResPresent: u16 = 1024
const ResUndefined: u16 = 2048

// Test-profile hook to disable individual synchronization rules so device
// and layout-tracker tests can verify each rule is caught when broken.
enum PlantedBug {
    None
    SkipDecay
    SkipPassBarrier
    SkipComputeUavBarrier
    SkipDirtyGroupOnLayout
}

IsWriteState(s: u16): bool {
    return (s & (ResShaderWrite | ResRenderTarget | ResDepthWrite | ResCopyDst)) != 0
}

IsReadOnlyState(s: u16): bool {
    return s != ResCommon && s != ResUndefined && s != ResPresent && !IsWriteState(s)
}

MergeTexReadStates(a: u16, b: u16): ?u16 {
    if a == b {
        return some(a)
    }
    if (a == ResDepthRead && b == ResShaderRead) || (a == ResShaderRead && b == ResDepthRead) || (a == (ResDepthRead | ResShaderRead) && (b == ResDepthRead || b == ResShaderRead)) || (b == (ResDepthRead | ResShaderRead) && (a == ResDepthRead || a == ResShaderRead)) {
        return some(ResDepthRead | ResShaderRead)
    }
    return none
}

struct TexState {
    mips: u8
    layers: u16
    planes: u8
    uniform: bool
    uni_state: u16
    sub_states: HeapList[u16]
}

TexState.New(a: &Alloc, mips: int, layers: int, planes: int, init_state: u16): TexState {
    return TexState(
        mips: Cast[u8](mips),
        layers: Cast[u16](layers),
        planes: Cast[u8](planes),
        uniform: true,
        uni_state: init_state,
        sub_states: HeapList[u16].New(a),
    )
}

TexState.Free(self, a: &Alloc) {
    TexState(mips: _, layers: _, planes: _, uniform: _, uni_state: _, sub_states) := self
    sub_states.Free(a)
}

TexState.SubCount(&self): int {
    return int(self.planes) * int(self.mips) * int(self.layers)
}

TexState.SubIndex(&self, plane: int, mip: int, layer: int): int {
    return (plane * int(self.mips) + mip) * int(self.layers) + layer
}

TexState.EnsureSplit(mut self, a: &Alloc): OutOfMemory!void {
    if !self.uniform {
        return
    }
    n := self.SubCount()
    if self.sub_states.Len() < n {
        need := n - self.sub_states.Len()
        for _ in 0..need {
            try self.sub_states.Push(a, self.uni_state)
        }
    }
    for i in 0..n {
        self.sub_states[i] = self.uni_state
    }
    self.uniform = false
}

TexState.StateAt(&self, plane: int, mip: int, layer: int): u16 {
    if self.uniform {
        return self.uni_state
    }
    return self.sub_states[self.SubIndex(plane, mip, layer)]
}

TexState.TryCollapse(mut self) {
    if self.uniform {
        return
    }
    n := self.SubCount()
    if n <= 0 {
        return
    }
    first := self.sub_states[0]
    for i in 1..n {
        if self.sub_states[i] != first {
            return
        }
    }
    self.uniform = true
    self.uni_state = first
}

struct BufState {
    fixed: bool
    fixed_state: u16
    list_serial: u64
    promoted: u16
    written: bool
    vk_state: u16
}

BufState.New(fixed: bool, fixed_state: u16): BufState {
    return BufState(
        fixed: fixed,
        fixed_state: fixed_state,
        list_serial: 0,
        promoted: if fixed { fixed_state } else { ResCommon },
        written: false,
        vk_state: if fixed { fixed_state } else { ResCommon },
    )
}

struct TexBarrier {
    slot: u32
    all_sub: bool
    first_mip: u8
    mip_count: u8
    first_layer: u16
    layer_count: u16
    plane_mask: u8
    old_state: u16
    new_state: u16
}

struct BufBarrier {
    slot: u32
    old_state: u16
    new_state: u16
    is_uav: bool
    for_vk_only: bool
}

struct BarrierItem {
    is_tex: bool
    tex: TexBarrier
    buf: BufBarrier
}

struct UndoOp {
    is_tex: bool
    slot: u32
    was_uniform: bool
    sub_idx: i32
    old_state: u16
    old_list_serial: u64
    old_promoted: u16
    old_written: bool
    old_vk_state: u16
}

PushTexUndo(a: &Alloc, undo: mut HeapList[UndoOp], slot: u32, was_uniform: bool, sub_idx: i32, old_state: u16): OutOfMemory!void {
    try undo.Push(a, UndoOp(
        is_tex: true,
        slot: slot,
        was_uniform: was_uniform,
        sub_idx: sub_idx,
        old_state: old_state,
        old_list_serial: 0,
        old_promoted: 0,
        old_written: false,
        old_vk_state: 0,
    ))
}

PushBufUndo(a: &Alloc, undo: mut HeapList[UndoOp], slot: u32, st: &BufState): OutOfMemory!void {
    try undo.Push(a, UndoOp(
        is_tex: false,
        slot: slot,
        was_uniform: false,
        sub_idx: -1,
        old_state: 0,
        old_list_serial: st.list_serial,
        old_promoted: st.promoted,
        old_written: st.written,
        old_vk_state: st.vk_state,
    ))
}

TransitionTexture(
    a: &Alloc,
    slot: u32,
    tex: mut TexState,
    first_mip: int,
    mip_count: int,
    first_layer: int,
    layer_count: int,
    plane_mask: u8,
    need_state: u16,
    barriers: mut HeapList[BarrierItem],
    undo: mut HeapList[UndoOp],
    bug: PlantedBug,
): OutOfMemory!void {
    skip_emit := bug == PlantedBug.SkipPassBarrier
    all_planes := if int(tex.planes) == 1 { true } else { plane_mask == 3 }
    covers_all := first_mip == 0 && mip_count == int(tex.mips) && first_layer == 0 && layer_count == int(tex.layers) && all_planes
    if tex.uniform && covers_all {
        if tex.uni_state != need_state {
            try PushTexUndo(a, undo, slot, true, -1, tex.uni_state)
            if !skip_emit {
                try barriers.Push(a, BarrierItem(
                    is_tex: true,
                    tex: TexBarrier(
                        slot: slot,
                        all_sub: true,
                        first_mip: 0,
                        mip_count: tex.mips,
                        first_layer: 0,
                        layer_count: tex.layers,
                        plane_mask: plane_mask,
                        old_state: tex.uni_state,
                        new_state: need_state,
                    ),
                    buf: mem.Zeroed[BufBarrier](),
                ))
            }
            tex.uni_state = need_state
        }
        return
    }
    was_uni := tex.uniform
    uni_old := tex.uni_state
    try tex.EnsureSplit(a)
    if was_uni {
        try PushTexUndo(a, undo, slot, true, -1, uni_old)
    }
    for p in 0..int(tex.planes) {
        if (plane_mask & Cast[u8](1 << p)) == 0 {
            continue
        }
        for m in first_mip..(first_mip + mip_count) {
            var l := first_layer
            end_l := first_layer + layer_count
            for l < end_l {
                idx := tex.SubIndex(p, m, l)
                cur := tex.sub_states[idx]
                if cur == need_state {
                    l += 1
                    continue
                }
                var run_end := l + 1
                for run_end < end_l {
                    nidx := tex.SubIndex(p, m, run_end)
                    if tex.sub_states[nidx] != cur {
                        break
                    }
                    run_end += 1
                }
                for k in l..run_end {
                    kidx := tex.SubIndex(p, m, k)
                    if !was_uni {
                        try PushTexUndo(a, undo, slot, false, Cast[i32](kidx), cur)
                    }
                    tex.sub_states[kidx] = need_state
                }
                if !skip_emit {
                    try barriers.Push(a, BarrierItem(
                        is_tex: true,
                        tex: TexBarrier(
                            slot: slot,
                            all_sub: false,
                            first_mip: Cast[u8](m),
                            mip_count: 1,
                            first_layer: Cast[u16](l),
                            layer_count: Cast[u16](run_end - l),
                            plane_mask: Cast[u8](1 << p),
                            old_state: cur,
                            new_state: need_state,
                        ),
                        buf: mem.Zeroed[BufBarrier](),
                    ))
                }
                l = run_end
            }
        }
    }
    tex.TryCollapse()
}

TransitionBuffer(
    a: &Alloc,
    slot: u32,
    buf: mut BufState,
    cur_serial: u64,
    need_state: u16,
    barriers: mut HeapList[BarrierItem],
    undo: mut HeapList[UndoOp],
    bug: PlantedBug,
): OutOfMemory!void {
    if buf.fixed {
        return
    }
    try PushBufUndo(a, undo, slot, &*buf)
    var decayed := buf.list_serial != cur_serial
    if bug == PlantedBug.SkipDecay {
        decayed = false
    }
    if decayed {
        buf.list_serial = cur_serial
        buf.promoted = ResCommon
        buf.written = false
    }
    // Vulkan buffer RAW/WAW/WAR within or across commands in a submission.
    vk_needs_barrier := (buf.vk_state != ResCommon) && (IsWriteState(buf.vk_state) || IsWriteState(need_state))
    old_vk := buf.vk_state
    buf.vk_state = need_state

    if !buf.written {
        if IsWriteState(need_state) {
            if buf.promoted != ResCommon {
                try barriers.Push(a, BarrierItem(
                    is_tex: false,
                    tex: mem.Zeroed[TexBarrier](),
                    buf: BufBarrier(slot: slot, old_state: buf.promoted, new_state: need_state, is_uav: false, for_vk_only: false),
                ))
            } else if vk_needs_barrier {
                try barriers.Push(a, BarrierItem(
                    is_tex: false,
                    tex: mem.Zeroed[TexBarrier](),
                    buf: BufBarrier(slot: slot, old_state: old_vk, new_state: need_state, is_uav: false, for_vk_only: true),
                ))
            }
            buf.written = true
            buf.promoted = need_state
        } else {
            buf.promoted |= need_state
            if vk_needs_barrier {
                try barriers.Push(a, BarrierItem(
                    is_tex: false,
                    tex: mem.Zeroed[TexBarrier](),
                    buf: BufBarrier(slot: slot, old_state: old_vk, new_state: need_state, is_uav: false, for_vk_only: true),
                ))
            }
        }
        return
    }
    if buf.promoted == ResShaderWrite && need_state == ResShaderWrite {
        try barriers.Push(a, BarrierItem(
            is_tex: false,
            tex: mem.Zeroed[TexBarrier](),
            buf: BufBarrier(slot: slot, old_state: ResShaderWrite, new_state: ResShaderWrite, is_uav: true, for_vk_only: false),
        ))
        return
    }
    if buf.promoted != need_state {
        try barriers.Push(a, BarrierItem(
            is_tex: false,
            tex: mem.Zeroed[TexBarrier](),
            buf: BufBarrier(slot: slot, old_state: buf.promoted, new_state: need_state, is_uav: false, for_vk_only: false),
        ))
        buf.promoted = need_state
        buf.written = IsWriteState(need_state)
    } else if vk_needs_barrier {
        try barriers.Push(a, BarrierItem(
            is_tex: false,
            tex: mem.Zeroed[TexBarrier](),
            buf: BufBarrier(slot: slot, old_state: old_vk, new_state: need_state, is_uav: false, for_vk_only: true),
        ))
    }
}

ApplyTexUndo(tex: mut TexState, op: &UndoOp) {
    if op.was_uniform {
        tex.uniform = true
        tex.uni_state = op.old_state
    } else if op.sub_idx >= 0 && int(op.sub_idx) < tex.sub_states.Len() {
        tex.uniform = false
        tex.sub_states[int(op.sub_idx)] = op.old_state
    }
}

ApplyBufUndo(buf: mut BufState, op: &UndoOp) {
    buf.list_serial = op.old_list_serial
    buf.promoted = op.old_promoted
    buf.written = op.old_written
    buf.vk_state = op.old_vk_state
}
