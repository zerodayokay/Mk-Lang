// Why: Vulkan command buffers must execute on the system stack in one switch,
// flush creation-time zeroing and pre-submit writes first, and recycle command
// pools and upload chunks once their timeline serial completes.
//
// What: SubmitStream, SubmitPreSubmit, SubmitReadbackTexture, and command pool
// slot management.
//
// How: acquires a recycled VkCmdSlot, records pre-submit zeroing/copies +
// global memory barrier + stream chunks inside task.MkSystemRun, waits on
// pending swapchain acquire semaphores, and signals timeline_sem with serial.

import c.vulkan
import std.mem
import std.raw
import std.task

StampUploadChunks(st: mut VkDeviceState, serial: u64) {
    for i in 0..st.upload_chunks.Len() {
        ch := mut st.upload_chunks[i]
        if ch.used > 0 && ch.serial == 0 {
            ch.serial = serial
        }
    }
}

AcquireCmdSlot(a: &Alloc, st: mut VkDeviceState): Error!int {
    completed := PollCompletedSerial(st)
    for i in 0..st.cmd_slots.Len() {
        if !st.cmd_slots[i].in_use || completed >= st.cmd_slots[i].serial {
            st.cmd_slots[i].in_use = true
            unsafe "pool is live" {
                r := st.dev_d.vk_reset_command_pool.Must()(HandleFrom[vulkan.VkDeviceT](st.device), HandleFrom[vulkan.VkCommandPoolT](st.cmd_slots[i].pool), 0)
                if r != vulkan.VkSuccess {
                    if IsDeviceLostResult(r) { st.lost = true }
                    fail MapVkError(r, CallQueueSubmit)
                }
            }
            return i
        }
    }
    unsafe "st.device is live" {
        dev := HandleFrom[vulkan.VkDeviceT](st.device)
        var cpci := mem.Zeroed[vulkan.VkCommandPoolCreateInfo]()
        cpci.s_type = vulkan.VkStructureTypeCommandPoolCreateInfo
        cpci.flags = Cast[u32](vulkan.VkCommandPoolCreateResetCommandBufferBit) | Cast[u32](vulkan.VkCommandPoolCreateTransientBit)
        cpci.queue_family_index = st.queue_family
        var pool: vulkan.VkCommandPool = raw.Null[vulkan.VkCommandPoolT]()
        r1 := st.dev_d.vk_create_command_pool.Must()(dev, raw.Of(&cpci), raw.Null[vulkan.VkAllocationCallbacks](), raw.OfMut(mut pool))
        if r1 != vulkan.VkSuccess {
            if IsDeviceLostResult(r1) { st.lost = true }
            fail MapVkError(r1, CallQueueSubmit)
        }
        var cbai := mem.Zeroed[vulkan.VkCommandBufferAllocateInfo]()
        cbai.s_type = vulkan.VkStructureTypeCommandBufferAllocateInfo
        cbai.command_pool = pool
        cbai.level = vulkan.VkCommandBufferLevelPrimary
        cbai.command_buffer_count = 1
        var cbuf: vulkan.VkCommandBuffer = raw.Null[vulkan.VkCommandBufferT]()
        r2 := st.dev_d.vk_allocate_command_buffers.Must()(dev, raw.Of(&cbai), raw.OfMut(mut cbuf))
        if r2 != vulkan.VkSuccess {
            st.dev_d.vk_destroy_command_pool.Must()(dev, pool, raw.Null[vulkan.VkAllocationCallbacks]())
            if IsDeviceLostResult(r2) { st.lost = true }
            fail MapVkError(r2, CallQueueSubmit)
        }
        idx := st.cmd_slots.Len()
        st.cmd_slots.Push(a, VkCmdSlot(pool: HandleAddr(pool), buf: HandleAddr(cbuf), serial: 0, in_use: true)) else e {
            st.dev_d.vk_destroy_command_pool.Must()(dev, pool, raw.Null[vulkan.VkAllocationCallbacks]())
            fail e
        }
        return idx
    }
}

ZeroCompressedTexture(st: &VkDeviceState, cmd: vulkan.VkCommandBuffer, tex: &VkTexRec) {
    unsafe "cmd, tex, and st.zero_buf are live" {
        zbuf := HandleFrom[vulkan.VkBufferT](st.zero_buf)
        img := HandleFrom[vulkan.VkImageT](tex.vk_image)
        for m in 0..tex.mips {
            mw := if (tex.width >> m) > 0 { tex.width >> m } else { 1 }
            mh := if (tex.height >> m) > 0 { tex.height >> m } else { 1 }
            bx := (mw + tex.block_w - 1) / tex.block_w
            by := (mh + tex.block_h - 1) / tex.block_h
            row_bytes := bx * tex.block_bytes
            var rows_per_chunk := Cast[int](st.zero_buf_size) / (if row_bytes > 0 { row_bytes } else { 1 })
            if rows_per_chunk < 1 {
                rows_per_chunk = 1
            }
            for l in 0..tex.layers {
                var by_off := 0
                for by_off < by {
                    take_by := if by - by_off < rows_per_chunk { by - by_off } else { rows_per_chunk }
                    py := by_off * tex.block_h
                    ph := if py + take_by * tex.block_h > mh { mh - py } else { take_by * tex.block_h }
                    var reg := vulkan.VkBufferImageCopy(
                        buffer_offset: 0,
                        buffer_row_length: Cast[u32](bx * tex.block_w),
                        buffer_image_height: Cast[u32](take_by),
                        image_subresource: vulkan.VkImageSubresourceLayers(
                            aspect_mask: Cast[u32](vulkan.VkImageAspectColorBit),
                            mip_level: Cast[u32](m),
                            base_array_layer: Cast[u32](l),
                            layer_count: 1,
                        ),
                        image_offset: vulkan.VkOffset3D(x: 0, y: Cast[i32](py), z: 0),
                        image_extent: vulkan.VkExtent3D(width: Cast[u32](mw), height: Cast[u32](ph), depth: 1),
                    )
                    st.dev_d.vk_cmd_copy_buffer_to_image.Must()(cmd, zbuf, img, vulkan.VkImageLayoutTransferDstOptimal, 1, raw.Of(&reg))
                    by_off += take_by
                }
            }
        }
    }
}

FlushPendingZeroAndCopies(st: mut VkDeviceState, cmd: vulkan.VkCommandBuffer) {
    had_work := st.pending_zero_bufs.Len() > 0 || st.pending_zero_texs.Len() > 0 || st.pending_copies.Len() > 0
    if !had_work {
        return
    }
    unsafe "cmd and st are live on system stack" {
        for i in 0..st.pending_zero_bufs.Len() {
            bid := st.pending_zero_bufs[i]
            bidx := Cast[int](bid)
            if bidx > 0 && bidx < st.buffers.Len() && st.buffers[bidx].alive && st.buffers[bidx].needs_zero {
                st.dev_d.vk_cmd_fill_buffer.Must()(cmd, HandleFrom[vulkan.VkBufferT](st.buffers[bidx].vk_buffer), 0, vulkan.VkWholeSize, 0)
                st.buffers[bidx].needs_zero = false
            }
        }
        st.pending_zero_bufs.Clear()

        for i in 0..st.pending_zero_texs.Len() {
            tid := st.pending_zero_texs[i]
            tidx := Cast[int](tid)
            if tidx <= 0 || tidx >= st.textures.Len() || !st.textures[tidx].alive || !st.textures[tidx].needs_zero {
                continue
            }
            tex := st.textures[tidx]
            img := HandleFrom[vulkan.VkImageT](tex.vk_image)
            asp := if !tex.is_depth {
                Cast[u32](vulkan.VkImageAspectColorBit)
            } else if tex.has_stencil {
                Cast[u32](vulkan.VkImageAspectDepthBit) | Cast[u32](vulkan.VkImageAspectStencilBit)
            } else {
                Cast[u32](vulkan.VkImageAspectDepthBit)
            }
            EmitSingleImageBarrier(
                &*st, cmd, img, asp, 0, Cast[u32](tex.mips), 0, Cast[u32](tex.layers),
                vulkan.VkImageLayoutUndefined, vulkan.VkImageLayoutTransferDstOptimal,
                vulkan.VkPipelineStage2None, 0,
                vulkan.VkPipelineStage2TransferBit, vulkan.VkAccess2TransferWriteBit,
            )
            var rng := vulkan.VkImageSubresourceRange(aspect_mask: asp, base_mip_level: 0, level_count: Cast[u32](tex.mips), base_array_layer: 0, layer_count: Cast[u32](tex.layers))
            if tex.is_compressed {
                ZeroCompressedTexture(&*st, cmd, &tex)
            } else if tex.is_depth {
                var ds := vulkan.VkClearDepthStencilValue(depth: 0.0, stencil: 0)
                st.dev_d.vk_cmd_clear_depth_stencil_image.Must()(cmd, img, vulkan.VkImageLayoutTransferDstOptimal, raw.Of(&ds), 1, raw.Of(&rng))
            } else {
                var col := mem.Zeroed[vulkan.VkClearColorValue]()
                st.dev_d.vk_cmd_clear_color_image.Must()(cmd, img, vulkan.VkImageLayoutTransferDstOptimal, raw.Of(&col), 1, raw.Of(&rng))
            }
            EmitSingleImageBarrier(
                &*st, cmd, img, asp, 0, Cast[u32](tex.mips), 0, Cast[u32](tex.layers),
                vulkan.VkImageLayoutTransferDstOptimal, vulkan.VkImageLayoutGeneral,
                vulkan.VkPipelineStage2TransferBit, vulkan.VkAccess2TransferWriteBit,
                vulkan.VkPipelineStage2AllCommandsBit, vulkan.VkAccess2MemoryReadBit | vulkan.VkAccess2MemoryWriteBit,
            )
            st.textures[tidx].needs_zero = false
            pm: u8 = if tex.has_stencil { 3 } else { 1 }
            TrackSetTexLayoutDirect(st, tid, 0, tex.mips, 0, tex.layers, pm, vulkan.VkImageLayoutGeneral)
        }
        st.pending_zero_texs.Clear()

        if st.pending_copies.Len() > 0 {
            EmitGlobalSubmissionBarrier(&*st, cmd)
        }

        for i in 0..st.pending_copies.Len() {
            // Consecutive writes may overlap in the same destination range.
            if i > 0 { EmitGlobalSubmissionBarrier(&*st, cmd) }
            op := st.pending_copies[i]
            if op.kind == 0 {
                bidx := Cast[int](op.dst_handle)
                if bidx > 0 && bidx < st.buffers.Len() && st.buffers[bidx].alive {
                    var reg := vulkan.VkBufferCopy(src_offset: op.src_off, dst_offset: op.dst_off, size: op.size)
                    st.dev_d.vk_cmd_copy_buffer.Must()(cmd, HandleFrom[vulkan.VkBufferT](op.src_buf), HandleFrom[vulkan.VkBufferT](st.buffers[bidx].vk_buffer), 1, raw.Of(&reg))
                }
            } else {
                tidx := Cast[int](op.dst_handle)
                if tidx > 0 && tidx < st.textures.Len() && st.textures[tidx].alive {
                    tex := st.textures[tidx]
                    img := HandleFrom[vulkan.VkImageT](tex.vk_image)
                    asp := AspectForBarrier(tex.is_depth, tex.has_stencil, op.pmask)
                    // Read tracked layout of first subresource in region; restore it after copy.
                    first_p := if op.pmask == 2 { 1 } else { 0 }
                    sidx := tex.tracked_start + (first_p * tex.mips + int(op.mip)) * tex.layers + int(op.layer)
                    cur_lay := st.tracked_layouts[sidx]
                    if cur_lay != vulkan.VkImageLayoutTransferDstOptimal {
                        EmitSingleImageBarrier(
                            &*st, cmd, img, asp, op.mip, 1, op.layer, op.layers,
                            cur_lay, vulkan.VkImageLayoutTransferDstOptimal,
                            vulkan.VkPipelineStage2AllCommandsBit, vulkan.VkAccess2MemoryReadBit | vulkan.VkAccess2MemoryWriteBit,
                            vulkan.VkPipelineStage2TransferBit, vulkan.VkAccess2TransferWriteBit,
                        )
                    }
                    var reg := vulkan.VkBufferImageCopy(
                        buffer_offset: op.src_off,
                        buffer_row_length: 0,
                        buffer_image_height: 0,
                        image_subresource: vulkan.VkImageSubresourceLayers(aspect_mask: asp, mip_level: op.mip, base_array_layer: op.layer, layer_count: op.layers),
                        image_offset: vulkan.VkOffset3D(x: op.x, y: op.y, z: 0),
                        image_extent: vulkan.VkExtent3D(width: op.w, height: op.h, depth: 1),
                    )
                    st.dev_d.vk_cmd_copy_buffer_to_image.Must()(cmd, HandleFrom[vulkan.VkBufferT](op.src_buf), img, vulkan.VkImageLayoutTransferDstOptimal, 1, raw.Of(&reg))
                    if cur_lay != vulkan.VkImageLayoutTransferDstOptimal {
                        restore_lay := if cur_lay == vulkan.VkImageLayoutUndefined { vulkan.VkImageLayoutGeneral } else { cur_lay }
                        EmitSingleImageBarrier(
                            &*st, cmd, img, asp, op.mip, 1, op.layer, op.layers,
                            vulkan.VkImageLayoutTransferDstOptimal, restore_lay,
                            vulkan.VkPipelineStage2TransferBit, vulkan.VkAccess2TransferWriteBit,
                            vulkan.VkPipelineStage2AllCommandsBit, vulkan.VkAccess2MemoryReadBit | vulkan.VkAccess2MemoryWriteBit,
                        )
                        TrackSetTexLayoutDirect(st, op.dst_handle, int(op.mip), 1, int(op.layer), int(op.layers), op.pmask, restore_lay)
                    }
                }
            }
        }
        st.pending_copies.Clear()
        EmitGlobalSubmissionBarrier(&*st, cmd)
    }
}

struct VkSubmitCallArg {
    st_ptr: uint
    cmd: u64
    serial: u64
    chunk_ptrs_ptr: uint
    chunk_ptrs_len: int
    chunk_words_ptr: uint
    barriers_ptr: uint
    barriers_len: int
    labels_ptr: uint
    labels_len: int
    out_err: ?Error
}

RunVkSubmitSys(arg: *u8) {
    unsafe "arg is a stack-pinned VkSubmitCallArg valid across MkSystemRun" {
        a := raw.PtrCast[u8, VkSubmitCallArg](arg)
        st := raw.FromAddr[VkDeviceState]((*a).st_ptr)
        cmd := HandleFrom[vulkan.VkCommandBufferT]((*a).cmd)

        var bi := mem.Zeroed[vulkan.VkCommandBufferBeginInfo]()
        bi.s_type = vulkan.VkStructureTypeCommandBufferBeginInfo
        bi.flags = Cast[u32](vulkan.VkCommandBufferUsageOneTimeSubmitBit)
        br := (*st).dev_d.vk_begin_command_buffer.Must()(cmd, raw.Of(&bi))
        if br != vulkan.VkSuccess {
            if IsDeviceLostResult(br) { (*st).lost = true }
            (*a).out_err = some(MapVkError(br, CallQueueSubmit))
            return
        }

        EmitGlobalSubmissionBarrier(&*st, cmd)
        FlushPendingZeroAndCopies(mut *st, cmd)

        if (*a).chunk_ptrs_len > 0 {
            cptrs := raw.Slice(raw.FromAddr[uint]((*a).chunk_ptrs_ptr), (*a).chunk_ptrs_len)
            cwords := raw.Slice(raw.FromAddr[int]((*a).chunk_words_ptr), (*a).chunk_ptrs_len)
            bars := if (*a).barriers_len > 0 { raw.Slice(raw.FromAddr[BarrierCmd]((*a).barriers_ptr), (*a).barriers_len) } else { raw.Slice(raw.Null[BarrierCmd](), 0) }
            lbls := if (*a).labels_len > 0 { raw.Slice(raw.FromAddr[u8]((*a).labels_ptr), (*a).labels_len) } else { raw.Slice(raw.Null[u8](), 0) }
            ReplayStreamChunks(mut *st, cmd, cptrs, cwords, bars, lbls)
        }

        er := (*st).dev_d.vk_end_command_buffer.Must()(cmd)
        if er != vulkan.VkSuccess {
            if IsDeviceLostResult(er) { (*st).lost = true }
            (*a).out_err = some(MapVkError(er, CallQueueSubmit))
            return
        }

        var waits: [16]vulkan.VkSemaphoreSubmitInfo = [mem.Zeroed[vulkan.VkSemaphoreSubmitInfo](); 16]
        var wait_sc_idx: [16]int = [0; 16]
        var wcnt := 0
        for si in 1..(*st).swapchains.Len() {
            sc := &(*st).swapchains[si]
            if sc.alive && sc.pending_submit_wait && wcnt < 16 {
                waits[wcnt] = vulkan.VkSemaphoreSubmitInfo(
                    s_type: vulkan.VkStructureTypeSemaphoreSubmitInfo,
                    p_next: raw.Null[void](),
                    semaphore: HandleFrom[vulkan.VkSemaphoreT](sc.pending_wait_sem),
                    value: 0,
                    stage_mask: if (*st).planted_bug == BugWrongAcqStage { vulkan.VkPipelineStage2TopOfPipeBit } else { vulkan.VkPipelineStage2ColorAttachmentOutputBit | vulkan.VkPipelineStage2CopyBit },
                    device_index: 0,
                )
                wait_sc_idx[wcnt] = si
                wcnt += 1
            }
        }

        var cb_info := vulkan.VkCommandBufferSubmitInfo(
            s_type: vulkan.VkStructureTypeCommandBufferSubmitInfo,
            p_next: raw.Null[void](),
            command_buffer: cmd,
            device_mask: 0,
        )
        var sig_info := vulkan.VkSemaphoreSubmitInfo(
            s_type: vulkan.VkStructureTypeSemaphoreSubmitInfo,
            p_next: raw.Null[void](),
            semaphore: HandleFrom[vulkan.VkSemaphoreT]((*st).timeline_sem),
            value: (*a).serial,
            stage_mask: vulkan.VkPipelineStage2AllCommandsBit,
            device_index: 0,
        )
        var si := mem.Zeroed[vulkan.VkSubmitInfo2]()
        si.s_type = vulkan.VkStructureTypeSubmitInfo2
        si.wait_semaphore_info_count = Cast[u32](wcnt)
        si.p_wait_semaphore_infos = if wcnt > 0 { raw.Data(waits[..]) } else { raw.Null[vulkan.VkSemaphoreSubmitInfo]() }
        si.command_buffer_info_count = 1
        si.p_command_buffer_infos = raw.Of(&cb_info)
        si.signal_semaphore_info_count = 1
        si.p_signal_semaphore_infos = raw.Of(&sig_info)

        sr := (*st).dev_d.vk_queue_submit2.Must()(HandleFrom[vulkan.VkQueueT]((*st).queue), 1, raw.Of(&si), raw.Null[vulkan.VkFenceT]())
        if sr != vulkan.VkSuccess {
            if IsDeviceLostResult(sr) { (*st).lost = true }
            (*a).out_err = some(MapVkError(sr, CallQueueSubmit))
            return
        }
        for wi in 0..wcnt {
            sci := wait_sc_idx[wi]
            sc := mut (*st).swapchains[sci]
            acqi := sc.pending_wait_acq_idx
            sc.acq[acqi].waited_by = (*a).serial
            sc.acq[acqi].signaled_unused = false
            sc.pending_submit_wait = false
            sc.pending_wait_sem = 0
        }
    }
}

/// Replays a portable stream into a Vulkan command buffer and submits it on the system stack.
export Device.SubmitStream(
    mut self,
    serial: u64,
    chunks: []StreamChunk,
    barriers: []BarrierCmd,
    labels: []u8,
): Error!void {
    a := self.heap.Alloc()
    st := self.state.Mut()
    if st.fault_next_submit.Swap(0) != 0 {
        st.lost = true
        st.loss_reason = LossRemoved
        fail DeviceLost(reason: LossRemoved)
    }
    if st.lost {
        fail DeviceLost(reason: st.loss_reason)
    }
    if serial > st.next_serial {
        st.next_serial = serial + 1
    } else {
        st.next_serial += 1
    }
    cc := chunks.Len()
    var stk_ptrs: [16]uint = [0; 16]
    var stk_words: [16]int = [0; 16]
    var heap_ptrs := HeapList[uint].New(a)
    var heap_words := HeapList[int].New(a)
    defer {
        heap_ptrs.Free(a)
        heap_words.Free(a)
    }
    var ptr_slice := stk_ptrs[0..0]
    var word_slice := stk_words[0..0]
    if cc <= 16 {
        unsafe "chunk word slices outlive MkSystemRun below" {
            for ci in 0..cc {
                wlen := chunks[ci].words.Len()
                stk_ptrs[ci] = if wlen > 0 { raw.Addr(raw.Data(chunks[ci].words)) } else { 0 }
                stk_words[ci] = wlen
            }
        }
        ptr_slice = stk_ptrs[0..cc]
        word_slice = stk_words[0..cc]
    } else {
        unsafe "chunk word slices outlive MkSystemRun below" {
            for ci in 0..cc {
                wlen := chunks[ci].words.Len()
                p := if wlen > 0 { raw.Addr(raw.Data(chunks[ci].words)) } else { 0 }
                try heap_ptrs.Push(a, p)
                try heap_words.Push(a, wlen)
            }
        }
        ptr_slice = heap_ptrs.Items()
        word_slice = heap_words.Items()
    }
    slot_idx := try AcquireCmdSlot(a, st)
    cmd := st.cmd_slots[slot_idx].buf
    st.cmd_slots[slot_idx].serial = serial

    var arg := VkSubmitCallArg(
        st_ptr: raw.Addr(raw.Of(&*st)),
        cmd: cmd,
        serial: serial,
        chunk_ptrs_ptr: raw.Addr(raw.Data(ptr_slice)),
        chunk_ptrs_len: ptr_slice.Len(),
        chunk_words_ptr: raw.Addr(raw.Data(word_slice)),
        barriers_ptr: raw.Addr(raw.Data(barriers)),
        barriers_len: barriers.Len(),
        labels_ptr: raw.Addr(raw.Data(labels)),
        labels_len: labels.Len(),
        out_err: none,
    )
    unsafe "arg and st remain pinned on this frame across MkSystemRun" {
        task.MkSystemRun(RunVkSubmitSys, raw.PtrCast[VkSubmitCallArg, u8](raw.OfMut(mut arg)))
    }
    match arg.out_err {
        some(e) { fail e }
        none {}
    }
    StampUploadChunks(st, serial)
}

/// Flushes pending creation-time zeroing and pre-submit writes on the given serial.
export Device.FlushPreSubmit(mut self, serial: u64): Error!void {
    var empty_chunks: [0]StreamChunk = []
    var empty_bars: [0]BarrierCmd = []
    var empty_lbls: [0]u8 = []
    try self.SubmitStream(serial, empty_chunks[..], empty_bars[..], empty_lbls[..])
}

SubmitReadbackTexture(
    a: &Alloc,
    st: mut VkDeviceState,
    serial: u64,
    tex_id: u64,
    stg_buf: u64,
    mip: int,
    layer: int,
    x: int,
    y: int,
    w: int,
    h: int,
    layers: int,
    pmask: u8,
): Error!u64 {
    if serial > st.next_serial {
        st.next_serial = serial + 1
    } else {
        st.next_serial += 1
    }
    slot_idx := try AcquireCmdSlot(a, st)
    cmd_u := st.cmd_slots[slot_idx].buf
    st.cmd_slots[slot_idx].serial = serial

    unsafe "st, tex, cmd_u, stg_buf are live" {
        cmd := HandleFrom[vulkan.VkCommandBufferT](cmd_u)
        var bi := mem.Zeroed[vulkan.VkCommandBufferBeginInfo]()
        bi.s_type = vulkan.VkStructureTypeCommandBufferBeginInfo
        bi.flags = Cast[u32](vulkan.VkCommandBufferUsageOneTimeSubmitBit)
        r1 := st.dev_d.vk_begin_command_buffer.Must()(cmd, raw.Of(&bi))
        if r1 != vulkan.VkSuccess { fail MapVkError(r1, CallQueueSubmit) }

        EmitGlobalSubmissionBarrier(&*st, cmd)
        FlushPendingZeroAndCopies(st, cmd)

        tex := st.textures[Cast[int](tex_id)]
        img := HandleFrom[vulkan.VkImageT](tex.vk_image)
        asp := AspectForBarrier(tex.is_depth, tex.has_stencil, pmask)
        first_p := if pmask == 2 { 1 } else { 0 }
        sidx := tex.tracked_start + (first_p * tex.mips + mip) * tex.layers + layer
        cur_lay := st.tracked_layouts[sidx]
        if cur_lay != vulkan.VkImageLayoutTransferSrcOptimal {
            EmitSingleImageBarrier(
                &*st, cmd, img, asp, Cast[u32](mip), 1, Cast[u32](layer), Cast[u32](layers),
                cur_lay, vulkan.VkImageLayoutTransferSrcOptimal,
                vulkan.VkPipelineStage2AllCommandsBit, vulkan.VkAccess2MemoryReadBit | vulkan.VkAccess2MemoryWriteBit,
                vulkan.VkPipelineStage2TransferBit, vulkan.VkAccess2TransferReadBit,
            )
        }
        var reg := vulkan.VkBufferImageCopy(
            buffer_offset: 0,
            buffer_row_length: 0,
            buffer_image_height: 0,
            image_subresource: vulkan.VkImageSubresourceLayers(aspect_mask: asp, mip_level: Cast[u32](mip), base_array_layer: Cast[u32](layer), layer_count: Cast[u32](layers)),
            image_offset: vulkan.VkOffset3D(x: Cast[i32](x), y: Cast[i32](y), z: 0),
            image_extent: vulkan.VkExtent3D(width: Cast[u32](w), height: Cast[u32](h), depth: 1),
        )
        st.dev_d.vk_cmd_copy_image_to_buffer.Must()(cmd, img, vulkan.VkImageLayoutTransferSrcOptimal, HandleFrom[vulkan.VkBufferT](stg_buf), 1, raw.Of(&reg))
        if cur_lay != vulkan.VkImageLayoutTransferSrcOptimal {
            restore_lay := if cur_lay == vulkan.VkImageLayoutUndefined { vulkan.VkImageLayoutGeneral } else { cur_lay }
            EmitSingleImageBarrier(
                &*st, cmd, img, asp, Cast[u32](mip), 1, Cast[u32](layer), Cast[u32](layers),
                vulkan.VkImageLayoutTransferSrcOptimal, restore_lay,
                vulkan.VkPipelineStage2TransferBit, vulkan.VkAccess2TransferReadBit,
                vulkan.VkPipelineStage2AllCommandsBit, vulkan.VkAccess2MemoryReadBit | vulkan.VkAccess2MemoryWriteBit,
            )
        }
        r2 := st.dev_d.vk_end_command_buffer.Must()(cmd)
        if r2 != vulkan.VkSuccess { fail MapVkError(r2, CallQueueSubmit) }

        var cb_info := vulkan.VkCommandBufferSubmitInfo(s_type: vulkan.VkStructureTypeCommandBufferSubmitInfo, p_next: raw.Null[void](), command_buffer: cmd, device_mask: 0)
        var sig_info := vulkan.VkSemaphoreSubmitInfo(s_type: vulkan.VkStructureTypeSemaphoreSubmitInfo, p_next: raw.Null[void](), semaphore: HandleFrom[vulkan.VkSemaphoreT](st.timeline_sem), value: serial, stage_mask: vulkan.VkPipelineStage2AllCommandsBit, device_index: 0)
        var si := mem.Zeroed[vulkan.VkSubmitInfo2]()
        si.s_type = vulkan.VkStructureTypeSubmitInfo2
        si.command_buffer_info_count = 1
        si.p_command_buffer_infos = raw.Of(&cb_info)
        si.signal_semaphore_info_count = 1
        si.p_signal_semaphore_infos = raw.Of(&sig_info)
        r3 := st.dev_d.vk_queue_submit2.Must()(HandleFrom[vulkan.VkQueueT](st.queue), 1, raw.Of(&si), raw.Null[vulkan.VkFenceT]())
        if r3 != vulkan.VkSuccess {
            if IsDeviceLostResult(r3) { st.lost = true }
            fail MapVkError(r3, CallQueueSubmit)
        }
        StampUploadChunks(st, serial)
        return serial
    }
}
