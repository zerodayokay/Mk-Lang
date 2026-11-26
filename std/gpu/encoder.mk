// Why: commands are recorded on any task, validated as they are recorded,
// and handed to the driver in one call per submission on the system stack.
//
// What: Extent, DataLayout, TextureRegion, Encoder, its copy/clear/write/debug
// commands, Submit, Abandon, and Device.WriteBuffer, WriteTexture, ReadTexture.
//
// How: an Encoder borrows mut Device, writes packed records and barriers into
// the device's active CommandStream, records the first misuse for Submit to
// report, and rolls back resource states via the undo log on Abandon.

import std.mem
import std.gpu.dx
import std.gpu.vk

// The command-stream chunks a Vulkan submit describes without allocating.
const InlineVkChunks: int = 8

/// A copy's size in texels and layers.
export struct Extent {
    export width: int
    export height: int
    export layers: int
}

/// Where texel rows sit in a buffer. bytes_per_row is a multiple of 256.
export struct DataLayout {
    export offset: int
    export bytes_per_row: int
    export rows_per_layer: int
}

/// One mip of a range of layers of a texture, from an origin.
export struct TextureRegion {
    export texture: &Texture
    export mip: int
    export layer: int
    export x: int
    export y: int
    export aspect: Aspect
}

/// Records commands for one submission. Linear: Submit or Abandon.
export linear view struct Encoder {
    dev: mut Device
    stream: u32
}

/// Starts an encoder on a recycled command stream. Errors: DeviceLost. O(1);
/// allocates only while the stream pool warms up.
export Device.Encoder(mut self, label: str): Error!Encoder from self {
    _ = label
    try self.CheckAlive()
    try self.FlushPreSubmit()
    st := self.state.Mut()
    st.stream.ResetKeepChunks()
    st.encoder_active = true
    ser := Trunc[u32](st.serials.next)
    return Encoder(dev: self, stream: ser)
}

Encoder.RecordMisuse(mut self, m: Misuse) {
    st := self.dev.state.Mut()
    st.stream.RecordError(m, st.stream.cmd_count)
}

Encoder.TransitionBuf(mut self, slot: u32, need: u16) {
    a := self.dev.heap.Alloc()
    st := self.dev.state.Mut()
    serial := st.serials.next
    bug := st.planted_bug
    st.buffers.StampUse(slot, serial)
    buf := st.buffers.GetMut(slot)
    TransitionBuffer(a, slot, mut buf.state, serial, need, mut st.stream.barriers, mut st.stream.undo, bug) else {
        st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
    }
}

Encoder.TransitionTex(mut self, slot: u32, first_mip: int, mip_count: int, first_layer: int, layer_count: int, plane_mask: u8, need: u16) {
    a := self.dev.heap.Alloc()
    st := self.dev.state.Mut()
    serial := st.serials.next
    bug := st.planted_bug
    st.textures.StampUse(slot, serial)
    tex := st.textures.GetMut(slot)
    TransitionTexture(a, slot, mut tex.state, first_mip, mip_count, first_layer, layer_count, plane_mask, need, mut st.stream.barriers, mut st.stream.undo, bug) else {
        st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
    }
}

Encoder.EmitBarriers(mut self, start_idx: int) {
    st := self.dev.state.Mut()
    cnt := st.stream.barriers.Len() - start_idx
    if cnt <= 0 {
        return
    }
    a := self.dev.heap.Alloc()
    _ = st.stream.Write2(a, mut st.chunk_pool, PackHdr(OpBarrierRef, 2, 0, Cast[u32](start_idx)), PackU32Pair(Cast[u32](cnt), 0)) else {
        st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
    }
}

CheckCopyBuffer(st: &DeviceState, serial: u32, src: &Buffer, src_at: int, dst: &Buffer, dst_at: int, n: int): Invalid!(src_nh: u64, dst_nh: u64) {
    if src.device != serial || dst.device != serial || !st.buffers.IsLive(src.slot, src.generation) || !st.buffers.IsLive(dst.slot, dst.generation) {
        fail Invalid(misuse: Misuse.StaleHandle, at: -1)
    }
    sb := st.buffers.Get(src.slot)
    db := st.buffers.Get(dst.slot)
    if !sb.usage.copy_src || !db.usage.copy_dst {
        fail Invalid(misuse: Misuse.BadUsage, at: -1)
    }
    if src_at < 0 || dst_at < 0 || n < 0 || i64(src_at) + i64(n) > i64(sb.size) || i64(dst_at) + i64(n) > i64(db.size) {
        fail Invalid(misuse: Misuse.RangeOutOfBounds, at: -1)
    }
    if src.slot == dst.slot && !(i64(src_at) + i64(n) <= i64(dst_at) || i64(dst_at) + i64(n) <= i64(src_at)) {
        fail Invalid(misuse: Misuse.RangeOutOfBounds, at: -1)
    }
    return (src_nh: sb.native_handle, dst_nh: db.native_handle)
}

/// Copies n bytes. A misuse is recorded and returned by Submit. O(1).
export Encoder.CopyBuffer(mut self, src: &Buffer, src_at: int, dst: &Buffer, dst_at: int, n: int) {
    if self.dev.state.Ref().stream.HasError() {
        return
    }
    (src_nh, dst_nh) := CheckCopyBuffer(self.dev.state.Ref(), self.dev.serial, src, src_at, dst, dst_at, n) else e {
        self.RecordMisuse(e.misuse)
        return
    }
    bstart := self.dev.state.Ref().stream.barriers.Len()
    self.TransitionBuf(src.slot, ResCopySrc)
    self.TransitionBuf(dst.slot, ResCopyDst)
    self.EmitBarriers(bstart)
    a := self.dev.heap.Alloc()
    st := self.dev.state.Mut()
    words := [
        PackHdr(OpCopyBuffer, 5, 0, Trunc[u32](n)),
        src_nh,
        dst_nh,
        Cast[u64](src_at),
        Cast[u64](dst_at),
    ]
    _ = st.stream.WriteSlice(a, mut st.chunk_pool, words[..]) else {
        st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
    }
}

CheckCopyBufToTex(st: &DeviceState, serial: u32, src: &Buffer, layout: DataLayout, dst: TextureRegion, size: Extent): Invalid!(src_nh: u64, dst_nh: u64, pmask: u8) {
    if src.device != serial || dst.texture.device != serial || !st.buffers.IsLive(src.slot, src.generation) || !st.textures.IsLive(dst.texture.slot, dst.texture.generation) {
        fail Invalid(misuse: Misuse.StaleHandle, at: -1)
    }
    sb := st.buffers.Get(src.slot)
    dt := st.textures.Get(dst.texture.slot)
    if !sb.usage.copy_src || !dt.usage.copy_dst {
        fail Invalid(misuse: Misuse.BadUsage, at: -1)
    }
    try ValidateTexRegion(dt.format, dt.width, dt.height, dt.layers, dt.mips, dt.samples, dst, size)
    _ = try ComputeCopyFootprint(dt.format, dst.aspect, layout, size, sb.size)
    pmask := try AspectMaskFor(dt.format, dst.aspect)
    return (src_nh: sb.native_handle, dst_nh: dt.native_handle, pmask: pmask)
}

/// Copies buffer rows into a texture region. O(1).
export Encoder.CopyBufferToTexture(mut self, src: &Buffer, layout: DataLayout, dst: TextureRegion, size: Extent) {
    if self.dev.state.Ref().stream.HasError() {
        return
    }
    (src_nh, dst_nh, pmask) := CheckCopyBufToTex(self.dev.state.Ref(), self.dev.serial, src, layout, dst, size) else e {
        self.RecordMisuse(e.misuse)
        return
    }
    bstart := self.dev.state.Ref().stream.barriers.Len()
    self.TransitionBuf(src.slot, ResCopySrc)
    self.TransitionTex(dst.texture.slot, dst.mip, 1, dst.layer, size.layers, pmask, ResCopyDst)
    self.EmitBarriers(bstart)
    a := self.dev.heap.Alloc()
    st := self.dev.state.Mut()
    words := [
        PackHdr(OpCopyBufferToTexture, 8, u16(pmask), Cast[u32](dst.mip)),
        src_nh,
        dst_nh,
        Cast[u64](layout.offset),
        PackU32Pair(Cast[u32](layout.bytes_per_row), Cast[u32](layout.rows_per_layer)),
        PackU32Pair(Cast[u32](dst.x), Cast[u32](dst.y)),
        PackU32Pair(Cast[u32](size.width), Cast[u32](size.height)),
        PackU32Pair(Cast[u32](dst.layer), Cast[u32](size.layers)),
    ]
    _ = st.stream.WriteSlice(a, mut st.chunk_pool, words[..]) else {
        st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
    }
}

CheckCopyTexToBuf(st: &DeviceState, serial: u32, src: TextureRegion, dst: &Buffer, layout: DataLayout, size: Extent): Invalid!(src_nh: u64, dst_nh: u64, pmask: u8) {
    if src.texture.device != serial || dst.device != serial || !st.textures.IsLive(src.texture.slot, src.texture.generation) || !st.buffers.IsLive(dst.slot, dst.generation) {
        fail Invalid(misuse: Misuse.StaleHandle, at: -1)
    }
    srct := st.textures.Get(src.texture.slot)
    db := st.buffers.Get(dst.slot)
    if !srct.usage.copy_src || !db.usage.copy_dst {
        fail Invalid(misuse: Misuse.BadUsage, at: -1)
    }
    try ValidateTexRegion(srct.format, srct.width, srct.height, srct.layers, srct.mips, srct.samples, src, size)
    _ = try ComputeCopyFootprint(srct.format, src.aspect, layout, size, db.size)
    pmask := try AspectMaskFor(srct.format, src.aspect)
    return (src_nh: srct.native_handle, dst_nh: db.native_handle, pmask: pmask)
}

/// Copies a texture region into buffer rows. O(1).
export Encoder.CopyTextureToBuffer(mut self, src: TextureRegion, dst: &Buffer, layout: DataLayout, size: Extent) {
    if self.dev.state.Ref().stream.HasError() {
        return
    }
    (src_nh, dst_nh, pmask) := CheckCopyTexToBuf(self.dev.state.Ref(), self.dev.serial, src, dst, layout, size) else e {
        self.RecordMisuse(e.misuse)
        return
    }
    bstart := self.dev.state.Ref().stream.barriers.Len()
    self.TransitionTex(src.texture.slot, src.mip, 1, src.layer, size.layers, pmask, ResCopySrc)
    self.TransitionBuf(dst.slot, ResCopyDst)
    self.EmitBarriers(bstart)
    a := self.dev.heap.Alloc()
    st := self.dev.state.Mut()
    words := [
        PackHdr(OpCopyTextureToBuffer, 8, u16(pmask), Cast[u32](src.mip)),
        src_nh,
        dst_nh,
        Cast[u64](layout.offset),
        PackU32Pair(Cast[u32](layout.bytes_per_row), Cast[u32](layout.rows_per_layer)),
        PackU32Pair(Cast[u32](src.x), Cast[u32](src.y)),
        PackU32Pair(Cast[u32](size.width), Cast[u32](size.height)),
        PackU32Pair(Cast[u32](src.layer), Cast[u32](size.layers)),
    ]
    _ = st.stream.WriteSlice(a, mut st.chunk_pool, words[..]) else {
        st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
    }
}

CheckCopyTex(st: &DeviceState, serial: u32, src: TextureRegion, dst: TextureRegion, size: Extent): Invalid!(src_nh: u64, dst_nh: u64, pmask: u8) {
    if src.texture.device != serial || dst.texture.device != serial || !st.textures.IsLive(src.texture.slot, src.texture.generation) || !st.textures.IsLive(dst.texture.slot, dst.texture.generation) {
        fail Invalid(misuse: Misuse.StaleHandle, at: -1)
    }
    srct := st.textures.Get(src.texture.slot)
    dstt := st.textures.Get(dst.texture.slot)
    if !srct.usage.copy_src || !dstt.usage.copy_dst {
        fail Invalid(misuse: Misuse.BadUsage, at: -1)
    }
    if FormatIndex(srct.format) != FormatIndex(dstt.format) {
        fail Invalid(misuse: Misuse.FormatMismatch, at: -1)
    }
    if srct.samples != dstt.samples {
        fail Invalid(misuse: Misuse.SamplesMismatch, at: -1)
    }
    try ValidateTexRegion(srct.format, srct.width, srct.height, srct.layers, srct.mips, 1, src, size)
    try ValidateTexRegion(dstt.format, dstt.width, dstt.height, dstt.layers, dstt.mips, 1, dst, size)
    spmask := try AspectMaskFor(srct.format, src.aspect)
    dpmask := try AspectMaskFor(dstt.format, dst.aspect)
    if spmask != dpmask {
        fail Invalid(misuse: Misuse.BadCopyAspect, at: -1)
    }
    return (src_nh: srct.native_handle, dst_nh: dstt.native_handle, pmask: spmask)
}

/// Copies between textures of one format. O(1).
export Encoder.CopyTexture(mut self, src: TextureRegion, dst: TextureRegion, size: Extent) {
    if self.dev.state.Ref().stream.HasError() {
        return
    }
    (src_nh, dst_nh, pmask) := CheckCopyTex(self.dev.state.Ref(), self.dev.serial, src, dst, size) else e {
        self.RecordMisuse(e.misuse)
        return
    }
    bstart := self.dev.state.Ref().stream.barriers.Len()
    self.TransitionTex(src.texture.slot, src.mip, 1, src.layer, size.layers, pmask, ResCopySrc)
    self.TransitionTex(dst.texture.slot, dst.mip, 1, dst.layer, size.layers, pmask, ResCopyDst)
    self.EmitBarriers(bstart)
    a := self.dev.heap.Alloc()
    st := self.dev.state.Mut()
    words := [
        PackHdr(OpCopyTexture, 8, u16(pmask), Cast[u32](size.layers)),
        src_nh,
        dst_nh,
        PackU32Pair(Cast[u32](src.mip), Cast[u32](dst.mip)),
        PackU32Pair(Cast[u32](src.x), Cast[u32](src.y)),
        PackU32Pair(Cast[u32](size.width), Cast[u32](size.height)),
        PackU32Pair(Cast[u32](dst.x), Cast[u32](dst.y)),
        PackU32Pair(Cast[u32](src.layer), Cast[u32](dst.layer)),
    ]
    _ = st.stream.WriteSlice(a, mut st.chunk_pool, words[..]) else {
        st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
    }
}

CheckBufWrite(st: &DeviceState, serial: u32, dst: &Buffer, at: int, len: int): Invalid!u64 {
    if dst.device != serial || !st.buffers.IsLive(dst.slot, dst.generation) {
        fail Invalid(misuse: Misuse.StaleHandle, at: -1)
    }
    db := st.buffers.Get(dst.slot)
    if !db.usage.copy_dst {
        fail Invalid(misuse: Misuse.BadUsage, at: -1)
    }
    if at < 0 || i64(at) + i64(len) > i64(db.size) {
        fail Invalid(misuse: Misuse.RangeOutOfBounds, at: -1)
    }
    return db.native_handle
}

/// Copies data into the upload ring now, and records its copy at this
/// point of the stream. Errors: OutOfDeviceMemory. O(bytes).
export Encoder.WriteBuffer(mut self, dst: &Buffer, at: int, data: []u8): Error!void {
    try self.dev.CheckAlive()
    nh := try CheckBufWrite(self.dev.state.Ref(), self.dev.serial, dst, at, data.Len())
    bstart := self.dev.state.Ref().stream.barriers.Len()
    self.TransitionBuf(dst.slot, ResCopyDst)
    self.EmitBarriers(bstart)
    st := self.dev.state.Mut()
    match mut st.native {
        .Stub(s) {
            idx := Cast[int](nh - 1)
            if idx >= 0 && idx < s.buf_mems.Len() && s.buf_mems[idx].alive {
                dst_slice := s.buf_mems[idx].bytes.ItemsMut()
                (mut dst_slice[at..(at + data.Len())]).CopyFrom(data)
            }
            a := self.dev.heap.Alloc()
            try st.stream.Write3(a, mut st.chunk_pool, PackHdr(OpCopyBuffer, 3, 0, Cast[u32](data.Len())), nh, Cast[u64](at))
        }
        .Dx(d) {
            if data.Len() > 0 {
                (res_ptr, off: src_off) := d.StageUploadData(data) else e {
                    ge := MapDxError(e)
                    RecordDxFailure(st, ge)
                    fail ge
                }
                a := self.dev.heap.Alloc()
                _ = try st.stream.WriteSlice(a, mut st.chunk_pool, [
                    PackHdr(OpCopyBuffer, 5, 1, Cast[u32](data.Len())),
                    res_ptr,
                    nh,
                    src_off,
                    Cast[u64](at),
                ][..])
            }
        }
        .Vk(v) {
            if data.Len() > 0 {
                stg := v.StageEncoderWrite(data) else e { fail MapVkErr(e) }
                a := self.dev.heap.Alloc()
                words := [
                    PackHdr(OpCopyBuffer, 5, 1, Cast[u32](data.Len())),
                    stg.vk_buf,
                    nh,
                    stg.offset,
                    Cast[u64](at),
                ]
                _ = try st.stream.WriteSlice(a, mut st.chunk_pool, words[..])
            }
        }
    }
}

CheckClearBuf(st: &DeviceState, serial: u32, b: &Buffer, at: int, n: int): Invalid!u64 {
    if b.device != serial || !st.buffers.IsLive(b.slot, b.generation) {
        fail Invalid(misuse: Misuse.StaleHandle, at: -1)
    }
    be := st.buffers.Get(b.slot)
    if !be.usage.copy_dst {
        fail Invalid(misuse: Misuse.BadUsage, at: -1)
    }
    if at < 0 || n < 0 || at % 4 != 0 || n % 4 != 0 || i64(at) + i64(n) > i64(be.size) {
        fail Invalid(misuse: if at % 4 != 0 || n % 4 != 0 { Misuse.AlignmentOffset } else { Misuse.RangeOutOfBounds }, at: -1)
    }
    return be.native_handle
}

/// Fills n bytes with zeros. O(1).
export Encoder.ClearBuffer(mut self, b: &Buffer, at: int, n: int) {
    if self.dev.state.Ref().stream.HasError() {
        return
    }
    nh := CheckClearBuf(self.dev.state.Ref(), self.dev.serial, b, at, n) else e {
        self.RecordMisuse(e.misuse)
        return
    }
    bstart := self.dev.state.Ref().stream.barriers.Len()
    self.TransitionBuf(b.slot, ResCopyDst)
    self.EmitBarriers(bstart)
    a := self.dev.heap.Alloc()
    st := self.dev.state.Mut()
    _ = st.stream.Write3(a, mut st.chunk_pool, PackHdr(OpClearBuffer, 3, 0, Cast[u32](n)), nh, Cast[u64](at)) else {
        st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
    }
}

/// Opens a debug group (a PIX event, a debug-utils label). O(label).
export Encoder.PushDebugGroup(mut self, label: str) {
    if self.dev.state.Ref().stream.HasError() {
        return
    }
    a := self.dev.heap.Alloc()
    st := self.dev.state.Mut()
    (offset: off, len: lbl_len) := st.stream.PushLabel(a, label) else {
        st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
        return
    }
    st.stream.debug_depth += 1
    _ = st.stream.Write2(a, mut st.chunk_pool, PackHdr(OpPushDebugGroup, 2, 0, off), PackU32Pair(lbl_len, 0)) else {
        st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
    }
}

/// Closes the innermost debug group. O(1).
export Encoder.PopDebugGroup(mut self) {
    if self.dev.state.Ref().stream.HasError() {
        return
    }
    a := self.dev.heap.Alloc()
    st := self.dev.state.Mut()
    if st.stream.debug_depth <= 0 {
        st.stream.RecordError(Misuse.DebugGroupUnbalanced, st.stream.cmd_count)
        return
    }
    st.stream.debug_depth -= 1
    _ = st.stream.Write1(a, mut st.chunk_pool, PackHdr(OpPopDebugGroup, 1, 0, 0)) else {
        st.stream.RecordError(Misuse.LimitExceeded, st.stream.cmd_count)
    }
}

CheckStreamForSubmit(st: mut DeviceState): Error!void {
    if st.stream.debug_depth != 0 {
        st.stream.RecordError(Misuse.DebugGroupUnbalanced, st.stream.cmd_count)
    }
    match st.stream.first_error {
        some(e) { fail e }
        none {}
    }
}

/// Hands the stream to the driver: replays it into a native command list
/// on the system stack and executes it after every Device write made since
/// the last submission. Fails only before it changes anything: with the
/// first misuse recorded (Invalid(misuse, at: command index)), StaleHandle,
/// or DeviceLost; the encoder is then still the caller's. Driver errors
/// found after execution become the device's sticky error. O(commands);
/// never waits; allocates nothing after warm-up.
export Encoder.Submit(self): Error!void {
    try self.dev.CheckAlive()
    try CheckStreamForSubmit(self.dev.state.Mut())
    try self.dev.FlushPreSubmit()
    var is_dx := false
    match &self.dev.state.Ref().native {
        .Dx(d: _) { is_dx = true }
        .Vk(v: _) {}
        .Stub(s: _) {}
    }
    a := self.dev.heap.Alloc()
    st := self.dev.state.Mut()
    if is_dx {
        st.dx_chunks.Clear()
        for ci in 0..st.stream.chunk_ids.Len() {
            cid := st.stream.chunk_ids[ci]
            end_w := st.stream.ChunkEndWord(ci)
            ch := st.chunk_pool.chunks[cid].Ref()
            span := dx.MakeChunkSpan(ch.words[0..end_w])
            st.dx_chunks.Push(a, span) else e {
                submit_serial := st.serials.Advance()
                st.stream.RecycleToPool(a, mut st.chunk_pool, submit_serial, true)
                st.encoder_active = false
                fail e
            }
        }
        st.dx_barriers.Clear()
        for bi in 0..st.stream.barriers.Len() {
            b := &st.stream.barriers[bi]
            item := if b.is_tex {
                dx.BarrierSpec(
                    is_tex: true,
                    nh: st.textures.Get(b.tex.slot).native_handle,
                    all_sub: b.tex.all_sub,
                    first_mip: b.tex.first_mip,
                    mip_count: b.tex.mip_count,
                    first_layer: b.tex.first_layer,
                    layer_count: b.tex.layer_count,
                    plane_mask: b.tex.plane_mask,
                    old_state: b.tex.old_state,
                    new_state: b.tex.new_state,
                    is_uav: false,
                )
            } else if b.buf.for_vk_only {
                mem.Zeroed[dx.BarrierSpec]()
            } else {
                dx.BarrierSpec(
                    is_tex: false,
                    nh: st.buffers.Get(b.buf.slot).native_handle,
                    all_sub: true,
                    first_mip: 0,
                    mip_count: 1,
                    first_layer: 0,
                    layer_count: 1,
                    plane_mask: 1,
                    old_state: b.buf.old_state,
                    new_state: b.buf.new_state,
                    is_uav: b.buf.is_uav,
                )
            }
            st.dx_barriers.Push(a, item) else e {
                submit_serial := st.serials.Advance()
                st.stream.RecycleToPool(a, mut st.chunk_pool, submit_serial, true)
                st.encoder_active = false
                fail e
            }
        }
        st.dx_passes.Clear()
        for pi in 0..st.stream.passes.Len() {
            ps := &st.stream.passes[pi]
            var cols: [8]dx.PassColorSpec = [mem.Zeroed[dx.PassColorSpec](); 8]
            for ci in 0..ps.color_count {
                c := &ps.colors[ci]
                rtv := dx.ViewRtvInfo(c.view_nh)
                res_v := dx.ViewRtvInfo(c.resolve_view_nh)
                cols[ci] = dx.PassColorSpec(
                    is_swapchain: rtv.is_swapchain,
                    res_ptr: rtv.res_ptr,
                    rtv_ptr: rtv.rtv_ptr,
                    has_resolve: c.resolve_view_nh != 0,
                    resolve_res_ptr: res_v.res_ptr,
                    resolve_sub: c.resolve_sub,
                    src_sub: c.subresource,
                    dxgi_format: c.dxgi_format,
                    load_op: c.load_op,
                    clear_r: c.clear_r,
                    clear_g: c.clear_g,
                    clear_b: c.clear_b,
                    clear_a: c.clear_a,
                )
            }
            dsv_ptr := if ps.depth.has_depth { dx.ViewDsvInfo(ps.depth.view_nh, ps.depth.read_only) } else { 0 }
            dp := dx.PassDepthSpec(
                has_depth: ps.depth.has_depth,
                dsv_ptr: dsv_ptr,
                read_only: ps.depth.read_only,
                depth_load: ps.depth.depth_load,
                stencil_load: ps.depth.stencil_load,
                depth_clear: ps.depth.depth_clear,
                stencil_clear: ps.depth.stencil_clear,
                has_stencil: ps.depth.has_stencil,
            )
            pspec := dx.PassReplaySpec(
                width: ps.width,
                height: ps.height,
                color_count: ps.color_count,
                colors: cols,
                depth: dp,
            )
            st.dx_passes.Push(a, pspec) else e {
                submit_serial := st.serials.Advance()
                st.stream.RecycleToPool(a, mut st.chunk_pool, submit_serial, true)
                st.encoder_active = false
                fail e
            }
        }
    }
    submit_serial := st.serials.Advance()
    skip_pb := st.planted_bug == PlantedBug.SkipPassBarrier
    var vk_err: ?Error = none
    match mut st.native {
        .Stub(s) {
            s.RecordStreamOps(a, &st.stream, &st.chunk_pool)
            s.SubmitStream(&st.stream, submit_serial) else e {
                st.lost = true
                st.sticky_err = some(e)
            }
        }
        .Dx(d) {
            d.ReplayAndSubmit(
                st.dx_chunks.Items(),
                st.dx_barriers.Items(),
                st.stream.labels.Items(),
                st.dx_passes.Items(),
                skip_pb,
                submit_serial,
            ) else e {
                ge := MapDxError(e)
                RecordDxFailure(st, ge)
            }
            SyncDxMessages(a, st)
        }
        .Vk(v) {
            cc := st.stream.chunk_ids.Len()
            st.vk_bcmds.Clear()
            for bi in 0..st.stream.barriers.Len() {
                it := &st.stream.barriers[bi]
                if it.is_tex {
                    tb := &it.tex
                    try st.vk_bcmds.Push(a, vk.BarrierCmd(
                        is_tex: true,
                        handle: st.textures.Get(tb.slot).native_handle,
                        all_sub: tb.all_sub,
                        first_mip: tb.first_mip,
                        mip_count: tb.mip_count,
                        first_layer: tb.first_layer,
                        layer_count: tb.layer_count,
                        plane_mask: tb.plane_mask,
                        old_state: tb.old_state,
                        new_state: tb.new_state,
                        is_uav: false,
                    ))
                } else {
                    bb := &it.buf
                    try st.vk_bcmds.Push(a, vk.BarrierCmd(
                        is_tex: false,
                        handle: st.buffers.Get(bb.slot).native_handle,
                        all_sub: false,
                        first_mip: 0,
                        mip_count: 0,
                        first_layer: 0,
                        layer_count: 0,
                        plane_mask: 0,
                        old_state: bb.old_state,
                        new_state: bb.new_state,
                        is_uav: bb.is_uav,
                    ))
                }
            }
            // A submit usually spans few chunks: describe up to InlineVkChunks
            // of them on the stack and spill to the heap only past that.
            var no_words: [0]u64 = []
            var inline: [InlineVkChunks]vk.StreamChunk = [vk.StreamChunk(words: no_words[..]); InlineVkChunks]
            var spill := HeapList[vk.StreamChunk].New(a)
            defer spill.Free(a)
            for ci in 0..cc {
                ch := st.chunk_pool.chunks[st.stream.chunk_ids[ci]].Ref()
                piece := vk.StreamChunk(words: ch.words[0..st.stream.ChunkEndWord(ci)])
                if cc <= InlineVkChunks {
                    inline[ci] = piece
                } else {
                    try spill.Push(a, piece)
                }
            }
            chunks := if cc <= InlineVkChunks { inline[0..cc] } else { spill.Items() }
            v.SubmitStream(submit_serial, chunks, st.vk_bcmds.Items(), st.stream.labels.Items()) else e {
                gerr := MapVkErr(e)
                st.lost = true
                st.sticky_err = some(gerr)
                vk_err = some(gerr)
            }
        }
    }
    st.stream.RecycleToPool(a, mut st.chunk_pool, submit_serial, false)
    st.encoder_active = false
    match vk_err {
        some(e) {
            fail e
        }
        none {
            Encoder(dev: _, stream: _) := self
        }
    }
}

/// Drops what was recorded and recycles the stream. Never fails. O(1).
export Encoder.Abandon(self) {
    Encoder(dev, stream: _) := self
    a := dev.heap.Alloc()
    st := dev.state.Mut()
    var i := st.stream.undo.Len() - 1
    for i >= 0 {
        op := &st.stream.undo[i]
        if op.is_tex {
            ApplyTexUndo(mut st.textures.GetMut(op.slot).state, op)
        } else {
            ApplyBufUndo(mut st.buffers.GetMut(op.slot).state, op)
        }
        i -= 1
    }
    st.stream.RecycleToPool(a, mut st.chunk_pool, 0, true)
    st.encoder_active = false
}

/// Copies data into the upload ring; the copy runs before the next
/// submission's commands. Errors: Invalid (range, usage), OutOfDeviceMemory.
/// O(bytes).
export Device.WriteBuffer(mut self, b: &Buffer, at: int, data: []u8): Error!void {
    try self.CheckAlive()
    nh := try CheckBufWrite(self.state.Ref(), self.serial, b, at, data.Len())
    a := self.heap.Alloc()
    st := self.state.Mut()
    st.buffers.StampUse(b.slot, st.serials.next)
    match mut st.native {
        .Stub(s) {
            idx := Cast[int](nh - 1)
            if idx >= 0 && idx < s.buf_mems.Len() && s.buf_mems[idx].alive {
                dst_slice := s.buf_mems[idx].bytes.ItemsMut()
                (mut dst_slice[at..(at + data.Len())]).CopyFrom(data)
            }
            try st.pre_submit.Write3(a, mut st.chunk_pool, PackHdr(OpCopyBuffer, 3, 0, Cast[u32](data.Len())), nh, Cast[u64](at))
        }
        .Dx(d) {
            d.QueueWriteBuffer(nh, at, data) else e {
                ge := MapDxError(e)
                RecordDxFailure(st, ge)
                fail ge
            }
        }
        .Vk(v) {
            v.WriteBufferPre(nh, at, data) else e { fail MapVkErr(e) }
        }
    }
}

PrepDeviceWriteTex(st: &DeviceState, serial: u32, dst: TextureRegion, data_len: int, size: Extent): Invalid!(nh: u64, pmask: u8) {
    if dst.texture.device != serial || !st.textures.IsLive(dst.texture.slot, dst.texture.generation) {
        fail Invalid(misuse: Misuse.StaleHandle, at: -1)
    }
    dt := st.textures.Get(dst.texture.slot)
    if !dt.usage.copy_dst {
        fail Invalid(misuse: Misuse.BadUsage, at: -1)
    }
    try ValidateTexRegion(dt.format, dt.width, dt.height, dt.layers, dt.mips, dt.samples, dst, size)
    pmask := try AspectMaskFor(dt.format, dst.aspect)
    row := RowOf(dt.format)
    bx := (size.width + int(row.block_w) - 1) / int(row.block_w)
    by := (size.height + int(row.block_h) - 1) / int(row.block_h)
    need := i64(bx) * i64(row.block_bytes) * i64(by) * i64(size.layers)
    if i64(data_len) < need {
        fail Invalid(misuse: Misuse.RangeOutOfBounds, at: -1)
    }
    return (nh: dt.native_handle, pmask: pmask)
}

CopyLayerStates(tex: &TextureEntry, region: TextureRegion, layers: int, plane: int): [MaxTextureLayers]u16 {
    var states: [MaxTextureLayers]u16 = [0; MaxTextureLayers]
    for li in 0..layers {
        states[li] = tex.state.StateAt(plane, region.mip, region.layer + li)
    }
    return states
}

/// Copies tight rows of data into a texture region before the next
/// submission's commands. O(bytes): repacks rows to 256-byte pitch.
export Device.WriteTexture(mut self, dst: TextureRegion, data: []u8, size: Extent): Error!void {
    try self.CheckAlive()
    (nh, pmask) := try PrepDeviceWriteTex(self.state.Ref(), self.serial, dst, data.Len(), size)
    a := self.heap.Alloc()
    st := self.state.Mut()
    st.textures.StampUse(dst.texture.slot, st.serials.next)
    match mut st.native {
        .Stub(s: _) {
            try st.pre_submit.Write3(a, mut st.chunk_pool, PackHdr(OpCopyBufferToTexture, 3, u16(pmask), Cast[u32](dst.mip)), nh, PackU32Pair(Cast[u32](size.width), Cast[u32](size.height)))
        }
        .Dx(d) {
            asp := match dst.aspect {
                .Depth { u8(1) }
                .Stencil { u8(2) }
                _ { u8(0) }
            }
            plane := if asp == 2 { 1 } else { 0 }
            states := CopyLayerStates(st.textures.Get(dst.texture.slot), dst, size.layers, plane)
            d.QueueWriteTexture(nh, dst.mip, dst.layer, dst.x, dst.y, size.width, size.height, asp, states[0..size.layers], data) else e {
                ge := MapDxError(e)
                RecordDxFailure(st, ge)
                fail ge
            }
        }
        .Vk(v) {
            v.WriteTexturePre(nh, dst.mip, dst.layer, dst.x, dst.y, size.width, size.height, size.layers, pmask, data) else e { fail MapVkErr(e) }
        }
    }
}

PrepDeviceReadTex(st: &DeviceState, serial: u32, src: TextureRegion, size: Extent): Invalid!(need: int, last_use: u64) {
    if src.texture.device != serial || !st.textures.IsLive(src.texture.slot, src.texture.generation) {
        fail Invalid(misuse: Misuse.StaleHandle, at: -1)
    }
    srct := st.textures.Get(src.texture.slot)
    if !srct.usage.copy_src {
        fail Invalid(misuse: Misuse.BadUsage, at: -1)
    }
    try ValidateTexRegion(srct.format, srct.width, srct.height, srct.layers, srct.mips, srct.samples, src, size)
    _ = try AspectMaskFor(srct.format, src.aspect)
    if FormatIndex(srct.format) == FormatIndex(TextureFormat.Depth24PlusStencil8) && src.aspect != Aspect.Stencil {
        fail Invalid(misuse: Misuse.BadCopyAspect, at: -1)
    }
    row := RowOf(srct.format)
    bbytes := if FormatIndex(srct.format) == FormatIndex(TextureFormat.Depth24PlusStencil8) { 1 } else if FormatIndex(srct.format) == FormatIndex(TextureFormat.Depth32FloatStencil8) && src.aspect == Aspect.Stencil { 1 } else if FormatIndex(srct.format) == FormatIndex(TextureFormat.Depth32FloatStencil8) { 4 } else { int(row.block_bytes) }
    bx := (size.width + int(row.block_w) - 1) / int(row.block_w)
    by := (size.height + int(row.block_h) - 1) / int(row.block_h)
    return (need: bx * bbytes * by * size.layers, last_use: st.textures.LastUse(src.texture.slot))
}

/// Reads a texture region into a, tight rows: a staging buffer, one
/// submission and a wait. For tests and screenshots. Errors: Invalid,
/// DeviceLost, Canceled (repeatable). O(bytes); allocates the result in a.
export Device.ReadTexture(mut self, a: &Arena, io: Io, src: TextureRegion, size: Extent): Error![]u8 from a {
    try self.CheckAlive()
    (need, last_use) := try PrepDeviceReadTex(self.state.Ref(), self.serial, src, size)
    try self.FlushPreSubmit()
    st := self.state.Mut()
    wait_target := st.serials.ClampWait(last_use)
    match mut st.native {
        .Stub(s) {
            try s.WaitSerial(wait_target)
            st.serials.Complete(wait_target)
            out := try a.Slice[u8](need)
            return out[..]
        }
        .Dx(d) {
            asp := match src.aspect {
                .Depth { u8(1) }
                .Stencil { u8(2) }
                _ { u8(0) }
            }
            plane := if asp == 2 { 1 } else { 0 }
            src_nh := st.textures.Get(src.texture.slot).native_handle
            states := CopyLayerStates(st.textures.Get(src.texture.slot), src, size.layers, plane)
            read_serial := st.serials.Advance()
            out := d.ReadTextureSync(a, io, src_nh, src.mip, src.layer, src.x, src.y, size.width, size.height, asp, states[0..size.layers], need, read_serial) else e {
                SyncDxMessages(self.heap.Alloc(), st)
                ge := MapDxError(e)
                RecordDxFailure(st, ge)
                fail ge
            }
            st.serials.Complete(read_serial)
            SyncDxMessages(self.heap.Alloc(), st)
            return out
        }
        .Vk(v) {
            src_nh := st.textures.Get(src.texture.slot).native_handle
            pmask := AspectMaskFor(st.textures.Get(src.texture.slot).format, src.aspect) else { 1 }
            r_ser := st.serials.Advance()
            var out := try a.Slice[u8](need)
            v.ReadTexture(io, r_ser, src_nh, src.mip, src.layer, src.x, src.y, size.width, size.height, size.layers, pmask, mut out[..]) else e {
                match e {
                    vk.DeviceLost(reason: r) {
                        st.lost = true
                        st.sticky_err = some(DeviceLost(reason: MapVkLossReason(r)))
                    }
                    _ {}
                }
                fail MapVkErr(e)
            }
            st.serials.Complete(r_ser)
            return out[..]
        }
    }
}

Encoder.InspectBarrierRef(&self): (barriers: int, w0: u64, w1: u64) {
    st := self.dev.state.Ref()
    bl := st.pass_shadow.barrier_loc
    cid := st.stream.chunk_ids[bl.chunk_idx]
    ch := st.chunk_pool.chunks[cid].Ref()
    return (barriers: st.stream.barriers.Len(), w0: ch.words[bl.word_idx], w1: ch.words[bl.word_idx + 1])
}
