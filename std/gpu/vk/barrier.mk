// Why: Vulkan synchronization2 requires explicit per-subresource image layout
// transitions, buffer memory barriers, and a submission-start global memory
// barrier, and Test/Checked builds need replay-time layout verification.
//
// What: MapTexStateToVk, MapBufStateToVk, EmitGlobalSubmissionBarrier,
// EmitVkBarriers, EmitSingleImageBarrier, and Test/Checked-profile
// replay-time layout tracker assertions.
//
// How: converts BarrierCmd items to VkImageMemoryBarrier2 /
// VkBufferMemoryBarrier2 batches for vkCmdPipelineBarrier2, while tracking
// expected layouts in st.tracked_layouts when build.Profile is Test/Checked.

import c.vulkan
import std.build
import std.mem
import std.raw

TrackerEnabled(): bool {
    return build.Profile == build.ProfileKind.Test || build.Profile == build.ProfileKind.Checked
}

FormatTrackerPanic(kind: str, op_idx: int, want: i32, got: i32): never {
    // Format integer fields without heap allocation on a fixed stack buffer.
    var buf: [192]u8 = [0; 192]
    var pos := 0
    pos = CopyAscii(mut buf[..], pos, "vk layout tracker mismatch (")
    pos = CopyAscii(mut buf[..], pos, kind)
    pos = CopyAscii(mut buf[..], pos, ") at opcode ")
    pos = AppendInt(mut buf[..], pos, op_idx)
    pos = CopyAscii(mut buf[..], pos, ": want=")
    pos = AppendInt(mut buf[..], pos, int(want))
    pos = CopyAscii(mut buf[..], pos, " got=")
    pos = AppendInt(mut buf[..], pos, int(got))
    unsafe "buf lives across Panic" {
        Panic(raw.Str(raw.Data(buf[..]), pos))
    }
}

CopyAscii(buf: mut []u8, pos: int, s: str): int {
    bs := s.AsBytes()
    var p := pos
    for i in 0..bs.Len() {
        if p >= buf.Len() {
            break
        }
        buf[p] = bs[i]
        p += 1
    }
    return p
}

AppendInt(buf: mut []u8, pos: int, v0: int): int {
    var p := pos
    var v := v0
    if v < 0 {
        if p < buf.Len() {
            buf[p] = b'-'
            p += 1
        }
        v = -v
    }
    if v == 0 {
        if p < buf.Len() {
            buf[p] = b'0'
            p += 1
        }
        return p
    }
    var tmp: [24]u8 = [0; 24]
    var n := 0
    for v > 0 && n < 24 {
        tmp[n] = Trunc[u8](b'0' + Trunc[u8](v % 10))
        v /= 10
        n += 1
    }
    var i := n - 1
    for i >= 0 && p < buf.Len() {
        buf[p] = tmp[i]
        p += 1
        i -= 1
    }
    return p
}

MapTexStateToVk(state: u16, is_depth: bool): (layout: i32, stage: u64, access: u64) {
    if state == ResUndefined {
        return (layout: vulkan.VkImageLayoutUndefined, stage: vulkan.VkPipelineStage2None, access: vulkan.VkAccess2None)
    }
    if state == ResCommon {
        return (layout: vulkan.VkImageLayoutGeneral, stage: vulkan.VkPipelineStage2AllCommandsBit, access: vulkan.VkAccess2MemoryReadBit | vulkan.VkAccess2MemoryWriteBit)
    }
    if state == ResRenderTarget {
        return (layout: vulkan.VkImageLayoutColorAttachmentOptimal, stage: vulkan.VkPipelineStage2ColorAttachmentOutputBit, access: vulkan.VkAccess2ColorAttachmentReadBit | vulkan.VkAccess2ColorAttachmentWriteBit)
    }
    if state == ResDepthWrite {
        return (
            layout: vulkan.VkImageLayoutDepthStencilAttachmentOptimal,
            stage: vulkan.VkPipelineStage2EarlyFragmentTestsBit | vulkan.VkPipelineStage2LateFragmentTestsBit,
            access: vulkan.VkAccess2DepthStencilAttachmentReadBit | vulkan.VkAccess2DepthStencilAttachmentWriteBit,
        )
    }
    if state == ResDepthRead {
        return (
            layout: vulkan.VkImageLayoutDepthStencilReadOnlyOptimal,
            stage: vulkan.VkPipelineStage2EarlyFragmentTestsBit | vulkan.VkPipelineStage2LateFragmentTestsBit,
            access: vulkan.VkAccess2DepthStencilAttachmentReadBit,
        )
    }
    if state == (ResDepthRead | ResShaderRead) {
        return (
            layout: vulkan.VkImageLayoutDepthStencilReadOnlyOptimal,
            stage: vulkan.VkPipelineStage2EarlyFragmentTestsBit | vulkan.VkPipelineStage2LateFragmentTestsBit | vulkan.VkPipelineStage2VertexShaderBit | vulkan.VkPipelineStage2FragmentShaderBit | vulkan.VkPipelineStage2ComputeShaderBit,
            access: vulkan.VkAccess2DepthStencilAttachmentReadBit | vulkan.VkAccess2ShaderReadBit,
        )
    }
    if state == ResShaderRead {
        lay := if is_depth { vulkan.VkImageLayoutDepthStencilReadOnlyOptimal } else { vulkan.VkImageLayoutShaderReadOnlyOptimal }
        return (
            layout: lay,
            stage: vulkan.VkPipelineStage2VertexShaderBit | vulkan.VkPipelineStage2FragmentShaderBit | vulkan.VkPipelineStage2ComputeShaderBit,
            access: vulkan.VkAccess2ShaderReadBit,
        )
    }
    if state == ResCopySrc {
        return (layout: vulkan.VkImageLayoutTransferSrcOptimal, stage: vulkan.VkPipelineStage2TransferBit, access: vulkan.VkAccess2TransferReadBit)
    }
    if state == ResCopyDst {
        return (layout: vulkan.VkImageLayoutTransferDstOptimal, stage: vulkan.VkPipelineStage2TransferBit, access: vulkan.VkAccess2TransferWriteBit)
    }
    if state == ResPresent {
        return (layout: vulkan.VkImageLayoutPresentSrcKhr, stage: vulkan.VkPipelineStage2None, access: vulkan.VkAccess2None)
    }
    return (layout: vulkan.VkImageLayoutGeneral, stage: vulkan.VkPipelineStage2AllCommandsBit, access: vulkan.VkAccess2MemoryReadBit | vulkan.VkAccess2MemoryWriteBit)
}

MapBufStateToVk(state: u16): (stage: u64, access: u64) {
    if state == ResCommon {
        return (stage: vulkan.VkPipelineStage2AllCommandsBit, access: vulkan.VkAccess2MemoryReadBit | vulkan.VkAccess2MemoryWriteBit)
    }
    var stage: u64 = 0
    var access: u64 = 0
    if (state & ResVertex) != 0 {
        stage |= vulkan.VkPipelineStage2VertexAttributeInputBit
        access |= vulkan.VkAccess2VertexAttributeReadBit
    }
    if (state & ResIndex) != 0 {
        stage |= vulkan.VkPipelineStage2IndexInputBit
        access |= vulkan.VkAccess2IndexReadBit
    }
    if (state & ResUniform) != 0 {
        stage |= vulkan.VkPipelineStage2VertexShaderBit | vulkan.VkPipelineStage2FragmentShaderBit | vulkan.VkPipelineStage2ComputeShaderBit
        access |= vulkan.VkAccess2UniformReadBit
    }
    if (state & ResShaderRead) != 0 {
        stage |= vulkan.VkPipelineStage2VertexShaderBit | vulkan.VkPipelineStage2FragmentShaderBit | vulkan.VkPipelineStage2ComputeShaderBit
        access |= vulkan.VkAccess2ShaderStorageReadBit
    }
    if (state & ResShaderWrite) != 0 {
        stage |= vulkan.VkPipelineStage2ComputeShaderBit
        access |= vulkan.VkAccess2ShaderStorageReadBit | vulkan.VkAccess2ShaderStorageWriteBit
    }
    if (state & ResCopySrc) != 0 {
        stage |= vulkan.VkPipelineStage2TransferBit
        access |= vulkan.VkAccess2TransferReadBit
    }
    if (state & ResCopyDst) != 0 {
        stage |= vulkan.VkPipelineStage2TransferBit
        access |= vulkan.VkAccess2TransferWriteBit
    }
    if stage == 0 {
        stage = vulkan.VkPipelineStage2AllCommandsBit
    }
    return (stage: stage, access: access)
}

AspectForBarrier(is_depth: bool, has_stencil: bool, pmask: u8): u32 {
    if !is_depth {
        return Cast[u32](vulkan.VkImageAspectColorBit)
    }
    if !has_stencil {
        return Cast[u32](vulkan.VkImageAspectDepthBit)
    }
    if pmask == 1 { return Cast[u32](vulkan.VkImageAspectDepthBit) }
    if pmask == 2 { return Cast[u32](vulkan.VkImageAspectStencilBit) }
    return Cast[u32](vulkan.VkImageAspectDepthBit) | Cast[u32](vulkan.VkImageAspectStencilBit)
}

TrackAssertAndSetTex(st: mut VkDeviceState, tex_id: u64, first_mip: int, mip_count: int, first_layer: int, layer_count: int, pmask: u8, old_lay: i32, new_lay: i32, op_idx: int) {
    if !TrackerEnabled() {
        return
    }
    tidx := Cast[int](tex_id)
    if tidx <= 0 || tidx >= st.textures.Len() || !st.textures[tidx].alive {
        return
    }
    tex := st.textures[tidx]
    planes := if tex.has_stencil { 2 } else { 1 }
    for p in 0..planes {
        if (pmask & Cast[u8](1 << p)) == 0 {
            continue
        }
        for m in first_mip..(first_mip + mip_count) {
            for l in first_layer..(first_layer + layer_count) {
                sidx := tex.tracked_start + (p * tex.mips + m) * tex.layers + l
                cur := st.tracked_layouts[sidx]
                if old_lay != vulkan.VkImageLayoutUndefined && cur != old_lay {
                    FormatTrackerPanic("barrier", op_idx, old_lay, cur)
                }
                st.tracked_layouts[sidx] = new_lay
            }
        }
    }
}

TrackCheckTexLayout(st: &VkDeviceState, tex_id: u64, first_mip: int, mip_count: int, first_layer: int, layer_count: int, pmask: u8, want_lay: i32, op_idx: int, kind: str) {
    if !TrackerEnabled() {
        return
    }
    tidx := Cast[int](tex_id)
    if tidx <= 0 || tidx >= st.textures.Len() || !st.textures[tidx].alive {
        return
    }
    tex := &st.textures[tidx]
    planes := if tex.has_stencil { 2 } else { 1 }
    for p in 0..planes {
        if (pmask & Cast[u8](1 << p)) == 0 {
            continue
        }
        for m in first_mip..(first_mip + mip_count) {
            for l in first_layer..(first_layer + layer_count) {
                sidx := tex.tracked_start + (p * tex.mips + m) * tex.layers + l
                cur := st.tracked_layouts[sidx]
                if cur != want_lay {
                    FormatTrackerPanic(kind, op_idx, want_lay, cur)
                }
            }
        }
    }
}

TrackSetTexLayoutDirect(st: mut VkDeviceState, tex_id: u64, first_mip: int, mip_count: int, first_layer: int, layer_count: int, pmask: u8, new_lay: i32) {
    if !TrackerEnabled() {
        return
    }
    tidx := Cast[int](tex_id)
    if tidx <= 0 || tidx >= st.textures.Len() || !st.textures[tidx].alive {
        return
    }
    tex := st.textures[tidx]
    planes := if tex.has_stencil { 2 } else { 1 }
    for p in 0..planes {
        if (pmask & Cast[u8](1 << p)) == 0 {
            continue
        }
        for m in first_mip..(first_mip + mip_count) {
            for l in first_layer..(first_layer + layer_count) {
                sidx := tex.tracked_start + (p * tex.mips + m) * tex.layers + l
                st.tracked_layouts[sidx] = new_lay
            }
        }
    }
}

EmitGlobalSubmissionBarrier(st: &VkDeviceState, cmd: vulkan.VkCommandBuffer) {
    if st.planted_bug == BugSkipGlobalBarrier {
        return
    }
    unsafe "cmd and st.dev_d.vk_cmd_pipeline_barrier2 are live" {
        var mb := mem.Zeroed[vulkan.VkMemoryBarrier2]()
        mb.s_type = vulkan.VkStructureTypeMemoryBarrier2
        mb.src_stage_mask = vulkan.VkPipelineStage2AllCommandsBit
        mb.src_access_mask = vulkan.VkAccess2MemoryWriteBit
        mb.dst_stage_mask = vulkan.VkPipelineStage2AllCommandsBit
        mb.dst_access_mask = vulkan.VkAccess2MemoryReadBit | vulkan.VkAccess2MemoryWriteBit
        var di := mem.Zeroed[vulkan.VkDependencyInfo]()
        di.s_type = vulkan.VkStructureTypeDependencyInfo
        di.memory_barrier_count = 1
        di.p_memory_barriers = raw.Of(&mb)
        st.dev_d.vk_cmd_pipeline_barrier2.Must()(cmd, raw.Of(&di))
    }
}

EmitSingleImageBarrier(
    st: &VkDeviceState,
    cmd: vulkan.VkCommandBuffer,
    vk_img: vulkan.VkImage,
    aspect: u32,
    base_mip: u32,
    mip_cnt: u32,
    base_layer: u32,
    layer_cnt: u32,
    old_lay: i32,
    new_lay: i32,
    src_stage: u64,
    src_access: u64,
    dst_stage: u64,
    dst_access: u64,
) {
    unsafe "cmd and vk_img are live" {
        var imb := mem.Zeroed[vulkan.VkImageMemoryBarrier2]()
        imb.s_type = vulkan.VkStructureTypeImageMemoryBarrier2
        imb.src_stage_mask = src_stage
        imb.src_access_mask = src_access
        imb.dst_stage_mask = dst_stage
        imb.dst_access_mask = dst_access
        imb.old_layout = old_lay
        imb.new_layout = new_lay
        imb.src_queue_family_index = vulkan.VkQueueFamilyIgnored
        imb.dst_queue_family_index = vulkan.VkQueueFamilyIgnored
        imb.image = vk_img
        imb.subresource_range = vulkan.VkImageSubresourceRange(
            aspect_mask: aspect,
            base_mip_level: base_mip,
            level_count: mip_cnt,
            base_array_layer: base_layer,
            layer_count: layer_cnt,
        )
        var di := mem.Zeroed[vulkan.VkDependencyInfo]()
        di.s_type = vulkan.VkStructureTypeDependencyInfo
        di.image_memory_barrier_count = 1
        di.p_image_memory_barriers = raw.Of(&imb)
        st.dev_d.vk_cmd_pipeline_barrier2.Must()(cmd, raw.Of(&di))
    }
}

FlushBarrierBatch(st: &VkDeviceState, cmd: vulkan.VkCommandBuffer, imgs: []vulkan.VkImageMemoryBarrier2, ic: int, bufs: []vulkan.VkBufferMemoryBarrier2, bc: int) {
    if ic == 0 && bc == 0 {
        return
    }
    unsafe "cmd and barrier slices live across vkCmdPipelineBarrier2" {
        var di := mem.Zeroed[vulkan.VkDependencyInfo]()
        di.s_type = vulkan.VkStructureTypeDependencyInfo
        di.buffer_memory_barrier_count = Cast[u32](bc)
        di.p_buffer_memory_barriers = if bc > 0 { raw.Data(bufs) } else { raw.Null[vulkan.VkBufferMemoryBarrier2]() }
        di.image_memory_barrier_count = Cast[u32](ic)
        di.p_image_memory_barriers = if ic > 0 { raw.Data(imgs) } else { raw.Null[vulkan.VkImageMemoryBarrier2]() }
        st.dev_d.vk_cmd_pipeline_barrier2.Must()(cmd, raw.Of(&di))
    }
}

EmitVkBarrierBatch(st: mut VkDeviceState, cmd: vulkan.VkCommandBuffer, items: []BarrierCmd, start_idx: int, count: int, op_idx: int) {
    if count <= 0 || st.planted_bug == BugSkipPassBarrier {
        return
    }
    var imgs: [32]vulkan.VkImageMemoryBarrier2 = [mem.Zeroed[vulkan.VkImageMemoryBarrier2](); 32]
    var bufs: [32]vulkan.VkBufferMemoryBarrier2 = [mem.Zeroed[vulkan.VkBufferMemoryBarrier2](); 32]
    var icnt := 0
    var bcnt := 0

    for k in 0..count {
        it := &items[start_idx + k]
        if it.is_tex {
            tidx := Cast[int](it.handle)
            if tidx <= 0 || tidx >= st.textures.Len() || !st.textures[tidx].alive {
                continue
            }
            tex := st.textures[tidx]
            old_m := MapTexStateToVk(it.old_state, tex.is_depth)
            new_m := MapTexStateToVk(it.new_state, tex.is_depth)
            fm := int(it.first_mip)
            mc := int(it.mip_count)
            fl := int(it.first_layer)
            lc := int(it.layer_count)
            pm := if it.all_sub && tex.has_stencil { u8(3) } else { it.plane_mask }
            TrackAssertAndSetTex(st, it.handle, fm, mc, fl, lc, pm, old_m.layout, new_m.layout, op_idx)
            if old_m.layout == new_m.layout && old_m.stage == new_m.stage && (old_m.access & vulkan.VkAccess2MemoryWriteBit) == 0 {
                continue
            }
            imgs[icnt] = vulkan.VkImageMemoryBarrier2(
                s_type: vulkan.VkStructureTypeImageMemoryBarrier2,
                p_next: raw.Null[void](),
                src_stage_mask: old_m.stage,
                src_access_mask: old_m.access,
                dst_stage_mask: new_m.stage,
                dst_access_mask: new_m.access,
                old_layout: old_m.layout,
                new_layout: new_m.layout,
                src_queue_family_index: vulkan.VkQueueFamilyIgnored,
                dst_queue_family_index: vulkan.VkQueueFamilyIgnored,
                image: HandleFrom[vulkan.VkImageT](tex.vk_image),
                subresource_range: vulkan.VkImageSubresourceRange(
                    aspect_mask: AspectForBarrier(tex.is_depth, tex.has_stencil, pm),
                    base_mip_level: Cast[u32](fm),
                    level_count: Cast[u32](mc),
                    base_array_layer: Cast[u32](fl),
                    layer_count: Cast[u32](lc),
                ),
            )
            icnt += 1
            if icnt == 32 {
                FlushBarrierBatch(&*st, cmd, imgs[..], icnt, bufs[..], bcnt)
                icnt = 0
                bcnt = 0
            }
        } else {
            bidx := Cast[int](it.handle)
            if bidx <= 0 || bidx >= st.buffers.Len() || !st.buffers[bidx].alive {
                continue
            }
            buf := st.buffers[bidx]
            src_m := MapBufStateToVk(it.old_state)
            dst_m := MapBufStateToVk(it.new_state)
            bufs[bcnt] = vulkan.VkBufferMemoryBarrier2(
                s_type: vulkan.VkStructureTypeBufferMemoryBarrier2,
                p_next: raw.Null[void](),
                src_stage_mask: src_m.stage,
                src_access_mask: src_m.access,
                dst_stage_mask: dst_m.stage,
                dst_access_mask: dst_m.access,
                src_queue_family_index: vulkan.VkQueueFamilyIgnored,
                dst_queue_family_index: vulkan.VkQueueFamilyIgnored,
                buffer: HandleFrom[vulkan.VkBufferT](buf.vk_buffer),
                offset: 0,
                size: vulkan.VkWholeSize,
            )
            bcnt += 1
            if bcnt == 32 {
                FlushBarrierBatch(&*st, cmd, imgs[..], icnt, bufs[..], bcnt)
                icnt = 0
                bcnt = 0
            }
        }
    }
    FlushBarrierBatch(&*st, cmd, imgs[..], icnt, bufs[..], bcnt)
}
