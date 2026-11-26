// Why: recorded portable CommandStreams must replay into a VkCommandBuffer
// inside a single system-stack call with zero heap allocation and full
// Test/Checked-profile layout validation.
//
// What: ReplayStreamChunks and per-opcode Vulkan replay handlers.
//
// How: walks each chunk's 8-byte-aligned records, dispatches on opcode into
// st.dev_d, validates image layouts at attachments, bind-group flushes, and
// copies in Test/Checked profile, and transitions active swapchain targets.

import c.vulkan
import std.mem
import std.raw

struct ReplayState {
    cur_pipe_is_compute: bool
    cur_layout_id: u64
    bound_bgs: [4]u64
    active_swap_views: [8]u64
    active_swap_cnt: int
}

UnpackOpCode(w0: u64): u8 {
    return Trunc[u8](w0 & 0xFF)
}

UnpackOpWords(w0: u64): int {
    return Cast[int]((w0 >> 8) & 0xFF)
}

UnpackOpFlags(w0: u64): u16 {
    return Trunc[u16]((w0 >> 16) & 0xFFFF)
}

UnpackOpArg(w0: u64): u32 {
    return Trunc[u32](w0 >> 32)
}

UnpackLo32(w: u64): u32 {
    return Trunc[u32](w & 0xFFFFFFFF)
}

UnpackHi32(w: u64): u32 {
    return Trunc[u32](w >> 32)
}

MapAttachmentLoad(code: u32): vulkan.VkAttachmentLoadOp {
    return match code {
        0 { vulkan.VkAttachmentLoadOpClear }
        1 { vulkan.VkAttachmentLoadOpLoad }
        _ { vulkan.VkAttachmentLoadOpDontCare }
    }
}

MapAttachmentStore(code: u32): vulkan.VkAttachmentStoreOp {
    return match code {
        0 { vulkan.VkAttachmentStoreOpStore }
        _ { vulkan.VkAttachmentStoreOpDontCare }
    }
}

CheckBoundGroupsLayouts(st: &VkDeviceState, rs: &ReplayState, op_idx: int) {
    if !TrackerEnabled() {
        return
    }
    for gi in 0..4 {
        bg_id := rs.bound_bgs[gi]
        if bg_id == 0 {
            continue
        }
        bidx := Cast[int](bg_id)
        if bidx <= 0 || bidx >= st.bind_groups.Len() || !st.bind_groups[bidx].alive {
            continue
        }
        bg := &st.bind_groups[bidx]
        for vi in 0..bg.tex_views.Len() {
            vid := bg.tex_views[vi]
            vidx := Cast[int](vid)
            if vidx <= 0 || vidx >= st.views.Len() || !st.views[vidx].alive {
                continue
            }
            v := &st.views[vidx]
            want := if v.is_depth { vulkan.VkImageLayoutDepthStencilReadOnlyOptimal } else { vulkan.VkImageLayoutShaderReadOnlyOptimal }
            TrackCheckTexLayout(st, v.tex_id, v.first_mip, v.mip_count, v.first_layer, v.layer_count, v.plane_mask, want, op_idx, "descriptor")
        }
    }
}

ReplayBeginRendering(st: mut VkDeviceState, rs: mut ReplayState, cmd: vulkan.VkCommandBuffer, words: []u64, wi: int, op_idx: int) {
    w0 := words[wi]
    w1 := words[wi + 1]
    flags := UnpackOpFlags(w0)
    w := UnpackOpArg(w0)
    h := UnpackLo32(w1)
    dflags := UnpackHi32(w1)
    color_cnt := int(flags & 0xF)
    has_depth := (flags & 0x10) != 0

    rs.active_swap_cnt = 0
    var color_atts: [8]vulkan.VkRenderingAttachmentInfo = [mem.Zeroed[vulkan.VkRenderingAttachmentInfo](); 8]
    var cur := wi + 2

    unsafe "cmd and views live during replay" {
        for ci in 0..color_cnt {
            view_id := words[cur]
            res_id := words[cur + 1]
            meta := UnpackLo32(words[cur + 2])
            cload := meta & 3
            cstore := (meta >> 2) & 1
            num_kind := (meta >> 3) & 3
            cr := f64.FromBits(words[cur + 3])
            cg := f64.FromBits(words[cur + 4])
            cb := f64.FromBits(words[cur + 5])
            ca := f64.FromBits(words[cur + 6])
            cur += 7

            vrec := st.views[Cast[int](view_id)]
            if vrec.is_swapchain {
                sc := &st.swapchains[Cast[int](vrec.sc_id)]
                img := &sc.images[int(vrec.sc_img_idx)]
                vk_img := img.vk_image
                old_lay := if !img.ever_rendered || cload != 1 { vulkan.VkImageLayoutUndefined } else { vulkan.VkImageLayoutPresentSrcKhr }
                if TrackerEnabled() && old_lay != vulkan.VkImageLayoutUndefined && img.tracked_layout != old_lay {
                    FormatTrackerPanic("swapchain_begin", op_idx, old_lay, img.tracked_layout)
                }
                src_st := if st.planted_bug == BugWrongAcqStage { vulkan.VkPipelineStage2TopOfPipeBit } else { vulkan.VkPipelineStage2ColorAttachmentOutputBit }
                EmitSingleImageBarrier(
                    &*st, cmd, HandleFrom[vulkan.VkImageT](vk_img), Cast[u32](vulkan.VkImageAspectColorBit),
                    0, 1, 0, 1,
                    old_lay, vulkan.VkImageLayoutColorAttachmentOptimal,
                    src_st, 0,
                    vulkan.VkPipelineStage2ColorAttachmentOutputBit, vulkan.VkAccess2ColorAttachmentReadBit | vulkan.VkAccess2ColorAttachmentWriteBit,
                )
                st.swapchains[Cast[int](vrec.sc_id)].images[int(vrec.sc_img_idx)].tracked_layout = vulkan.VkImageLayoutColorAttachmentOptimal
                rs.active_swap_views[rs.active_swap_cnt] = view_id
                rs.active_swap_cnt += 1
            } else {
                TrackCheckTexLayout(&*st, vrec.tex_id, vrec.first_mip, vrec.mip_count, vrec.first_layer, vrec.layer_count, vrec.plane_mask, vulkan.VkImageLayoutColorAttachmentOptimal, op_idx, "color_attachment")
            }

            var cv := mem.Zeroed[vulkan.VkClearValue]()
            var ccv := mem.Zeroed[vulkan.VkClearColorValue]()
            if num_kind == 1 {
                (mut ccv).SetUint32([Cast[u32](cr), Cast[u32](cg), Cast[u32](cb), Cast[u32](ca)])
            } else if num_kind == 2 {
                (mut ccv).SetInt32([Cast[i32](cr), Cast[i32](cg), Cast[i32](cb), Cast[i32](ca)])
            } else {
                (mut ccv).SetFloat32([Cast[f32](cr), Cast[f32](cg), Cast[f32](cb), Cast[f32](ca)])
            }
            (mut cv).SetColor(ccv)

            color_atts[ci].s_type = vulkan.VkStructureTypeRenderingAttachmentInfo
            color_atts[ci].image_view = HandleFrom[vulkan.VkImageViewT](vrec.vk_view)
            color_atts[ci].image_layout = vulkan.VkImageLayoutColorAttachmentOptimal
            color_atts[ci].load_op = MapAttachmentLoad(cload)
            color_atts[ci].store_op = MapAttachmentStore(cstore)
            color_atts[ci].clear_value = cv
            if res_id != 0 {
                rrec := st.views[Cast[int](res_id)]
                TrackCheckTexLayout(&*st, rrec.tex_id, rrec.first_mip, rrec.mip_count, rrec.first_layer, rrec.layer_count, rrec.plane_mask, vulkan.VkImageLayoutColorAttachmentOptimal, op_idx, "resolve_attachment")
                color_atts[ci].resolve_mode = vulkan.VkResolveModeAverageBit
                color_atts[ci].resolve_image_view = HandleFrom[vulkan.VkImageViewT](rrec.vk_view)
                color_atts[ci].resolve_image_layout = vulkan.VkImageLayoutColorAttachmentOptimal
            }
        }

        var depth_att := mem.Zeroed[vulkan.VkRenderingAttachmentInfo]()
        var stencil_att := mem.Zeroed[vulkan.VkRenderingAttachmentInfo]()
        var has_stencil := false
        if has_depth {
            dview_id := words[cur]
            dclear_u := UnpackLo32(words[cur + 1])
            sclear_u := UnpackHi32(words[cur + 1])
            dload := dflags & 3
            dstore := (dflags >> 2) & 1
            sload := (dflags >> 3) & 3
            sstore := (dflags >> 5) & 1
            read_only := (dflags & 64) != 0
            has_stencil = (dflags & 128) != 0

            dv := st.views[Cast[int](dview_id)]
            dlay := if read_only { vulkan.VkImageLayoutDepthStencilReadOnlyOptimal } else { vulkan.VkImageLayoutDepthStencilAttachmentOptimal }
            TrackCheckTexLayout(&*st, dv.tex_id, dv.first_mip, dv.mip_count, dv.first_layer, dv.layer_count, dv.plane_mask, dlay, op_idx, "depth_attachment")

            var dcv := mem.Zeroed[vulkan.VkClearValue]()
            (mut dcv).SetDepthStencil(vulkan.VkClearDepthStencilValue(depth: f32.FromBits(dclear_u), stencil: sclear_u))

            depth_att.s_type = vulkan.VkStructureTypeRenderingAttachmentInfo
            depth_att.image_view = HandleFrom[vulkan.VkImageViewT](dv.vk_view)
            depth_att.image_layout = dlay
            depth_att.load_op = MapAttachmentLoad(dload)
            depth_att.store_op = MapAttachmentStore(dstore)
            depth_att.clear_value = dcv
            if has_stencil {
                stencil_att.s_type = vulkan.VkStructureTypeRenderingAttachmentInfo
                stencil_att.image_view = HandleFrom[vulkan.VkImageViewT](dv.vk_view)
                stencil_att.image_layout = dlay
                stencil_att.load_op = MapAttachmentLoad(sload)
                stencil_att.store_op = MapAttachmentStore(sstore)
                stencil_att.clear_value = dcv
            }
        }

        var ri := mem.Zeroed[vulkan.VkRenderingInfo]()
        ri.s_type = vulkan.VkStructureTypeRenderingInfo
        ri.render_area = vulkan.VkRect2D(offset: vulkan.VkOffset2D(x: 0, y: 0), extent: vulkan.VkExtent2D(width: w, height: h))
        ri.layer_count = 1
        ri.color_attachment_count = Cast[u32](color_cnt)
        ri.p_color_attachments = if color_cnt > 0 { raw.Data(color_atts[..]) } else { raw.Null[vulkan.VkRenderingAttachmentInfo]() }
        if has_depth {
            ri.p_depth_attachment = raw.Of(&depth_att)
            if has_stencil {
                ri.p_stencil_attachment = raw.Of(&stencil_att)
            }
        }
        st.dev_d.vk_cmd_begin_rendering.Must()(cmd, raw.Of(&ri))
        // Default viewport (Y-flipped to match D3D12 clip space) and full-extent scissor.
        var vp := vulkan.VkViewport(x: 0.0, y: Cast[f32](h), width: Cast[f32](w), height: -Cast[f32](h), min_depth: 0.0, max_depth: 1.0)
        st.dev_d.vk_cmd_set_viewport.Must()(cmd, 0, 1, raw.Of(&vp))
        var rc := vulkan.VkRect2D(offset: vulkan.VkOffset2D(x: 0, y: 0), extent: vulkan.VkExtent2D(width: w, height: h))
        st.dev_d.vk_cmd_set_scissor.Must()(cmd, 0, 1, raw.Of(&rc))
    }
}

ReplayEndRendering(st: mut VkDeviceState, rs: mut ReplayState, cmd: vulkan.VkCommandBuffer) {
    unsafe "cmd is live" {
        st.dev_d.vk_cmd_end_rendering.Must()(cmd)
        for si in 0..rs.active_swap_cnt {
            vrec := st.views[Cast[int](rs.active_swap_views[si])]
            vk_img := st.swapchains[Cast[int](vrec.sc_id)].images[int(vrec.sc_img_idx)].vk_image
            EmitSingleImageBarrier(
                &*st, cmd, HandleFrom[vulkan.VkImageT](vk_img), Cast[u32](vulkan.VkImageAspectColorBit),
                0, 1, 0, 1,
                vulkan.VkImageLayoutColorAttachmentOptimal, vulkan.VkImageLayoutPresentSrcKhr,
                vulkan.VkPipelineStage2ColorAttachmentOutputBit, vulkan.VkAccess2ColorAttachmentWriteBit,
                vulkan.VkPipelineStage2None, 0,
            )
            st.swapchains[Cast[int](vrec.sc_id)].images[int(vrec.sc_img_idx)].ever_rendered = true
            st.swapchains[Cast[int](vrec.sc_id)].images[int(vrec.sc_img_idx)].tracked_layout = vulkan.VkImageLayoutPresentSrcKhr
        }
    }
    rs.active_swap_cnt = 0
}

ReplayStreamChunks(
    st: mut VkDeviceState,
    cmd: vulkan.VkCommandBuffer,
    chunk_ptrs: []uint,
    chunk_words: []int,
    barriers: []BarrierCmd,
    labels: []u8,
) {
    var rs := ReplayState(cur_pipe_is_compute: false, cur_layout_id: 0, bound_bgs: [0; 4], active_swap_views: [0; 8], active_swap_cnt: 0)
    var op_idx := 0
    unsafe "cmd and all chunk word slices are valid during replay on the system stack" {
        for ci in 0..chunk_ptrs.Len() {
            end_w := chunk_words[ci]
            if end_w <= 0 {
                continue
            }
            words := raw.Slice(raw.FromAddr[u64](chunk_ptrs[ci]), end_w)
            var wi := 0
            for wi < end_w {
                w0 := words[wi]
                op := UnpackOpCode(w0)
                wlen := UnpackOpWords(w0)
                if wlen <= 0 || wi + wlen > end_w {
                    break
                }
                match op {
                    OpBarrierRef {
                        start_idx := Cast[int](UnpackOpArg(w0))
                        cnt := Cast[int](UnpackLo32(words[wi + 1]))
                        EmitVkBarrierBatch(st, cmd, barriers, start_idx, cnt, op_idx)
                    }
                    OpBeginRendering {
                        ReplayBeginRendering(st, mut rs, cmd, words, wi, op_idx)
                    }
                    OpEndRendering {
                        ReplayEndRendering(st, mut rs, cmd)
                    }
                    OpSetPipeline {
                        is_comp := UnpackOpFlags(w0) != 0
                        pid := words[wi + 1]
                        prec := st.pipelines[Cast[int](pid)]
                        bp := if is_comp { vulkan.VkPipelineBindPointCompute } else { vulkan.VkPipelineBindPointGraphics }
                        rs.cur_pipe_is_compute = is_comp
                        rs.cur_layout_id = prec.layout_id
                        st.dev_d.vk_cmd_bind_pipeline.Must()(cmd, bp, HandleFrom[vulkan.VkPipelineT](prec.vk_pipe))
                    }
                    OpSetGroup {
                        gi := Cast[u32](UnpackOpFlags(w0))
                        dc := Cast[int](UnpackOpArg(w0))
                        bg_id := words[wi + 1]
                        rs.bound_bgs[int(gi)] = bg_id
                        bg := &st.bind_groups[Cast[int](bg_id)]
                        if TrackerEnabled() {
                            for vi in 0..bg.tex_views.Len() {
                                v := &st.views[Cast[int](bg.tex_views[vi])]
                                want := if v.is_depth { vulkan.VkImageLayoutDepthStencilReadOnlyOptimal } else { vulkan.VkImageLayoutShaderReadOnlyOptimal }
                                TrackCheckTexLayout(&*st, v.tex_id, v.first_mip, v.mip_count, v.first_layer, v.layer_count, v.plane_mask, want, op_idx, "descriptor")
                            }
                        }
                        var dyns: [4]u32 = [0; 4]
                        if dc > 0 {
                            dyns[0] = UnpackLo32(words[wi + 2])
                            if dc > 1 { dyns[1] = UnpackHi32(words[wi + 2]) }
                        }
                        if dc > 2 {
                            dyns[2] = UnpackLo32(words[wi + 3])
                            if dc > 3 { dyns[3] = UnpackHi32(words[wi + 3]) }
                        }
                        bp := if rs.cur_pipe_is_compute { vulkan.VkPipelineBindPointCompute } else { vulkan.VkPipelineBindPointGraphics }
                        vkl := HandleFrom[vulkan.VkPipelineLayoutT](st.pipe_layouts[Cast[int](rs.cur_layout_id)].vk_layout)
                        var dset := HandleFrom[vulkan.VkDescriptorSetT](bg.vk_set)
                        st.dev_d.vk_cmd_bind_descriptor_sets.Must()(cmd, bp, vkl, gi, 1, raw.Of(&dset), Cast[u32](dc), if dc > 0 { raw.Data(dyns[..]) } else { raw.Null[u32]() })
                    }
                    OpSetVertexBuffers {
                        slot := Cast[u32](UnpackOpFlags(w0))
                        bid := words[wi + 1]
                        off := u64(UnpackLo32(words[wi + 2]))
                        var buf := HandleFrom[vulkan.VkBufferT](st.buffers[Cast[int](bid)].vk_buffer)
                        st.dev_d.vk_cmd_bind_vertex_buffers.Must()(cmd, slot, 1, raw.Of(&buf), raw.Of(&off))
                    }
                    OpSetIndexBuffer {
                        itype := if UnpackOpFlags(w0) == 1 { vulkan.VkIndexTypeUint32 } else { vulkan.VkIndexTypeUint16 }
                        bid := words[wi + 1]
                        off := words[wi + 2]
                        st.dev_d.vk_cmd_bind_index_buffer.Must()(cmd, HandleFrom[vulkan.VkBufferT](st.buffers[Cast[int](bid)].vk_buffer), off, itype)
                    }
                    OpSetViewport {
                        x := f32.FromBits(UnpackLo32(words[wi + 1]))
                        y := f32.FromBits(UnpackHi32(words[wi + 1]))
                        w := f32.FromBits(UnpackLo32(words[wi + 2]))
                        h := f32.FromBits(UnpackHi32(words[wi + 2]))
                        zmin := f32.FromBits(UnpackLo32(words[wi + 3]))
                        zmax := f32.FromBits(UnpackHi32(words[wi + 3]))
                        // Negative height flips Y to match D3D12 clip space.
                        var vp := vulkan.VkViewport(x: x, y: y + h, width: w, height: -h, min_depth: zmin, max_depth: zmax)
                        st.dev_d.vk_cmd_set_viewport.Must()(cmd, 0, 1, raw.Of(&vp))
                    }
                    OpSetScissor {
                        sx := Cast[i32](UnpackLo32(words[wi + 1]))
                        sy := Cast[i32](UnpackHi32(words[wi + 1]))
                        sw := UnpackLo32(words[wi + 2])
                        sh := UnpackHi32(words[wi + 2])
                        var rc := vulkan.VkRect2D(offset: vulkan.VkOffset2D(x: sx, y: sy), extent: vulkan.VkExtent2D(width: sw, height: sh))
                        st.dev_d.vk_cmd_set_scissor.Must()(cmd, 0, 1, raw.Of(&rc))
                    }
                    OpSetBlendConst {
                        var bc: [4]f32 = [
                            f32.FromBits(UnpackLo32(words[wi + 1])),
                            f32.FromBits(UnpackHi32(words[wi + 1])),
                            f32.FromBits(UnpackLo32(words[wi + 2])),
                            f32.FromBits(UnpackHi32(words[wi + 2])),
                        ]
                        st.dev_d.vk_cmd_set_blend_constants.Must()(cmd, raw.Data(bc[..]))
                    }
                    OpSetStencilRef {
                        sref := UnpackOpArg(w0)
                        st.dev_d.vk_cmd_set_stencil_reference.Must()(cmd, Cast[u32](vulkan.VkStencilFaceFrontAndBack), sref)
                    }
                    OpSetImmediates {
                        sz := UnpackOpArg(w0)
                        vkl := HandleFrom[vulkan.VkPipelineLayoutT](st.pipe_layouts[Cast[int](rs.cur_layout_id)].vk_layout)
                        stages := Cast[u32](vulkan.VkShaderStageVertexBit) | Cast[u32](vulkan.VkShaderStageFragmentBit) | Cast[u32](vulkan.VkShaderStageComputeBit)
                        st.dev_d.vk_cmd_push_constants.Must()(cmd, vkl, stages, 0, sz, raw.PtrCast[u64, void](raw.Of(&words[wi + 1])))
                    }
                    OpDraw {
                        CheckBoundGroupsLayouts(&*st, &rs, op_idx)
                        verts := UnpackOpArg(w0)
                        insts := UnpackLo32(words[wi + 1])
                        fv := UnpackHi32(words[wi + 1])
                        fi := Trunc[u32](words[wi + 2])
                        st.dev_d.vk_cmd_draw.Must()(cmd, verts, insts, fv, fi)
                    }
                    OpDrawIndexed {
                        CheckBoundGroupsLayouts(&*st, &rs, op_idx)
                        inds := UnpackOpArg(w0)
                        insts := UnpackLo32(words[wi + 1])
                        find := UnpackHi32(words[wi + 1])
                        bv := Cast[i32](UnpackLo32(words[wi + 2]))
                        fi := UnpackHi32(words[wi + 2])
                        st.dev_d.vk_cmd_draw_indexed.Must()(cmd, inds, insts, find, bv, fi)
                    }
                    OpDispatch {
                        CheckBoundGroupsLayouts(&*st, &rs, op_idx)
                        gx := UnpackOpArg(w0)
                        gy := UnpackLo32(words[wi + 1])
                        gz := UnpackHi32(words[wi + 1])
                        st.dev_d.vk_cmd_dispatch.Must()(cmd, gx, gy, gz)
                    }
                    OpCopyBuffer {
                        n := u64(UnpackOpArg(w0))
                        if n > 0 {
                            src := HandleFrom[vulkan.VkBufferT](st.buffers[Cast[int](words[wi + 1])].vk_buffer)
                            dst := HandleFrom[vulkan.VkBufferT](st.buffers[Cast[int](words[wi + 2])].vk_buffer)
                            var reg := vulkan.VkBufferCopy(src_offset: words[wi + 3], dst_offset: words[wi + 4], size: n)
                            st.dev_d.vk_cmd_copy_buffer.Must()(cmd, src, dst, 1, raw.Of(&reg))
                        }
                    }
                    OpCopyBufferToTexture {
                        pmask := Trunc[u8](UnpackOpFlags(w0))
                        mip := UnpackOpArg(w0)
                        src := HandleFrom[vulkan.VkBufferT](st.buffers[Cast[int](words[wi + 1])].vk_buffer)
                        tid := words[wi + 2]
                        tex := st.textures[Cast[int](tid)]
                        boff := words[wi + 3]
                        bpr := int(UnpackLo32(words[wi + 4]))
                        rpl := int(UnpackHi32(words[wi + 4]))
                        dx := Cast[i32](UnpackLo32(words[wi + 5]))
                        dy := Cast[i32](UnpackHi32(words[wi + 5]))
                        sw := UnpackLo32(words[wi + 6])
                        sh := UnpackHi32(words[wi + 6])
                        dlay := UnpackLo32(words[wi + 7])
                        slays := UnpackHi32(words[wi + 7])
                        TrackCheckTexLayout(&*st, tid, int(mip), 1, int(dlay), int(slays), pmask, vulkan.VkImageLayoutTransferDstOptimal, op_idx, "copy_b2t")
                        row_texels := Cast[u32]((bpr / tex.block_bytes) * tex.block_w)
                        img_h := Cast[u32](rpl * tex.block_h)
                        var reg := vulkan.VkBufferImageCopy(
                            buffer_offset: boff,
                            buffer_row_length: row_texels,
                            buffer_image_height: img_h,
                            image_subresource: vulkan.VkImageSubresourceLayers(
                                aspect_mask: AspectForBarrier(tex.is_depth, tex.has_stencil, pmask),
                                mip_level: mip,
                                base_array_layer: dlay,
                                layer_count: slays,
                            ),
                            image_offset: vulkan.VkOffset3D(x: dx, y: dy, z: 0),
                            image_extent: vulkan.VkExtent3D(width: sw, height: sh, depth: 1),
                        )
                        st.dev_d.vk_cmd_copy_buffer_to_image.Must()(cmd, src, HandleFrom[vulkan.VkImageT](tex.vk_image), vulkan.VkImageLayoutTransferDstOptimal, 1, raw.Of(&reg))
                    }
                    OpCopyTextureToBuffer {
                        pmask := Trunc[u8](UnpackOpFlags(w0))
                        mip := UnpackOpArg(w0)
                        tid := words[wi + 1]
                        tex := st.textures[Cast[int](tid)]
                        dst := HandleFrom[vulkan.VkBufferT](st.buffers[Cast[int](words[wi + 2])].vk_buffer)
                        boff := words[wi + 3]
                        bpr := int(UnpackLo32(words[wi + 4]))
                        rpl := int(UnpackHi32(words[wi + 4]))
                        sx := Cast[i32](UnpackLo32(words[wi + 5]))
                        sy := Cast[i32](UnpackHi32(words[wi + 5]))
                        sw := UnpackLo32(words[wi + 6])
                        sh := UnpackHi32(words[wi + 6])
                        slay := UnpackLo32(words[wi + 7])
                        slays := UnpackHi32(words[wi + 7])
                        TrackCheckTexLayout(&*st, tid, int(mip), 1, int(slay), int(slays), pmask, vulkan.VkImageLayoutTransferSrcOptimal, op_idx, "copy_t2b")
                        row_texels := Cast[u32]((bpr / tex.block_bytes) * tex.block_w)
                        img_h := Cast[u32](rpl * tex.block_h)
                        var reg := vulkan.VkBufferImageCopy(
                            buffer_offset: boff,
                            buffer_row_length: row_texels,
                            buffer_image_height: img_h,
                            image_subresource: vulkan.VkImageSubresourceLayers(
                                aspect_mask: AspectForBarrier(tex.is_depth, tex.has_stencil, pmask),
                                mip_level: mip,
                                base_array_layer: slay,
                                layer_count: slays,
                            ),
                            image_offset: vulkan.VkOffset3D(x: sx, y: sy, z: 0),
                            image_extent: vulkan.VkExtent3D(width: sw, height: sh, depth: 1),
                        )
                        st.dev_d.vk_cmd_copy_image_to_buffer.Must()(cmd, HandleFrom[vulkan.VkImageT](tex.vk_image), vulkan.VkImageLayoutTransferSrcOptimal, dst, 1, raw.Of(&reg))
                    }
                    OpCopyTexture {
                        pmask := Trunc[u8](UnpackOpFlags(w0))
                        layers := UnpackOpArg(w0)
                        stid := words[wi + 1]
                        dtid := words[wi + 2]
                        stex := st.textures[Cast[int](stid)]
                        dtex := st.textures[Cast[int](dtid)]
                        smip := UnpackLo32(words[wi + 3])
                        dmip := UnpackHi32(words[wi + 3])
                        sx := Cast[i32](UnpackLo32(words[wi + 4]))
                        sy := Cast[i32](UnpackHi32(words[wi + 4]))
                        sw := UnpackLo32(words[wi + 5])
                        sh := UnpackHi32(words[wi + 5])
                        dx := Cast[i32](UnpackLo32(words[wi + 6]))
                        dy := Cast[i32](UnpackHi32(words[wi + 6]))
                        slay := UnpackLo32(words[wi + 7])
                        dlay := UnpackHi32(words[wi + 7])
                        TrackCheckTexLayout(&*st, stid, int(smip), 1, int(slay), int(layers), pmask, vulkan.VkImageLayoutTransferSrcOptimal, op_idx, "copy_tex_src")
                        TrackCheckTexLayout(&*st, dtid, int(dmip), 1, int(dlay), int(layers), pmask, vulkan.VkImageLayoutTransferDstOptimal, op_idx, "copy_tex_dst")
                        asp := AspectForBarrier(stex.is_depth, stex.has_stencil, pmask)
                        var reg := vulkan.VkImageCopy(
                            src_subresource: vulkan.VkImageSubresourceLayers(aspect_mask: asp, mip_level: smip, base_array_layer: slay, layer_count: layers),
                            src_offset: vulkan.VkOffset3D(x: sx, y: sy, z: 0),
                            dst_subresource: vulkan.VkImageSubresourceLayers(aspect_mask: asp, mip_level: dmip, base_array_layer: dlay, layer_count: layers),
                            dst_offset: vulkan.VkOffset3D(x: dx, y: dy, z: 0),
                            extent: vulkan.VkExtent3D(width: sw, height: sh, depth: 1),
                        )
                        st.dev_d.vk_cmd_copy_image.Must()(cmd, HandleFrom[vulkan.VkImageT](stex.vk_image), vulkan.VkImageLayoutTransferSrcOptimal, HandleFrom[vulkan.VkImageT](dtex.vk_image), vulkan.VkImageLayoutTransferDstOptimal, 1, raw.Of(&reg))
                    }
                    OpClearBuffer {
                        n := u64(UnpackOpArg(w0))
                        if n > 0 {
                            buf := HandleFrom[vulkan.VkBufferT](st.buffers[Cast[int](words[wi + 1])].vk_buffer)
                            off := words[wi + 2]
                            st.dev_d.vk_cmd_fill_buffer.Must()(cmd, buf, off, n, 0)
                        }
                    }
                    OpPushDebugGroup {
                        if st.has_debug {
                            match st.dev_d.vk_cmd_begin_debug_utils_label_ext {
                                some(bfn) {
                                    off := int(UnpackOpArg(w0))
                                    llen := int(UnpackLo32(words[wi + 1]))
                                    var lbuf: [256]u8 = [0; 256]
                                    cn := if llen > 255 { 255 } else { llen }
                                    for k in 0..cn {
                                        lbuf[k] = labels[off + k]
                                    }
                                    var li := mem.Zeroed[vulkan.VkDebugUtilsLabelExt]()
                                    li.s_type = vulkan.VkStructureTypeDebugUtilsLabelExt
                                    li.p_label_name = raw.Data(lbuf[..])
                                    bfn(cmd, raw.Of(&li))
                                }
                                none {}
                            }
                        }
                    }
                    OpPopDebugGroup {
                        if st.has_debug {
                            match st.dev_d.vk_cmd_end_debug_utils_label_ext {
                                some(efn) { efn(cmd) }
                                none {}
                            }
                        }
                    }
                    _ {}
                }
                wi += wlen
                op_idx += 1
            }
        }
    }
}
