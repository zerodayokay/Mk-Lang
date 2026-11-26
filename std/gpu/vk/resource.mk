// Why: Vulkan buffers, textures, image views, samplers, host-mapped memory,
// and upload/readback transfers must obey Vulkan alignment, dedicated-memory,
// D24S8 fallback, and creation-time zeroing rules without leaking handles.
//
// What: CreateBuffer, DestroyBuffer, CreateTexture, DestroyTexture,
// CreateView, DestroyView, CreateSampler, DestroySampler, WriteBufferPre,
// WriteTexturePre, AllocUploadData, MapBytes, MapBytesMut, FlushMapped,
// and ReadTexture.
//
// How: assigns 1-based dense IDs in VkDeviceState tables, suballocates memory
// via memory.mk (64 MiB TLSF blocks or dedicated), queues creation-time GPU
// zeroing into pending_zero_bufs/pending_zero_texs, and uses upload chunks /
// readback staging buffers for Device.Write*/ReadTexture.

import c.vulkan
import std.mem
import std.raw

AllocBufSlot(st: mut VkDeviceState, a: &Alloc, rec: VkBufRec): OutOfMemory!u64 {
    if st.buffers.Len() == 0 {
        try st.buffers.Push(a, mem.Zeroed[VkBufRec]())
    }
    for i in 1..st.buffers.Len() {
        if !st.buffers[i].alive {
            st.buffers[i] = rec
            return Cast[u64](i)
        }
    }
    id := Cast[u64](st.buffers.Len())
    try st.buffers.Push(a, rec)
    return id
}

AllocTexSlot(st: mut VkDeviceState, a: &Alloc, rec: VkTexRec): OutOfMemory!u64 {
    if st.textures.Len() == 0 {
        try st.textures.Push(a, VkTexRec(
            alive: false, vk_image: 0, vk_format: 0, width: 0, height: 0, layers: 0,
            mips: 0, samples: 0, is_depth: false, has_stencil: false, is_compressed: false,
            block_w: 1, block_h: 1, block_bytes: 4, alloc: mem.Zeroed[VkMemAlloc](),
            needs_zero: false, tracked_start: 0,
        ))
    }
    for i in 1..st.textures.Len() {
        if !st.textures[i].alive {
            st.textures[i] = rec
            return Cast[u64](i)
        }
    }
    id := Cast[u64](st.textures.Len())
    try st.textures.Push(a, rec)
    return id
}

AllocViewSlot(st: mut VkDeviceState, a: &Alloc, rec: VkViewRec): OutOfMemory!u64 {
    if st.views.Len() == 0 {
        try st.views.Push(a, mem.Zeroed[VkViewRec]())
    }
    for i in 1..st.views.Len() {
        if !st.views[i].alive {
            st.views[i] = rec
            return Cast[u64](i)
        }
    }
    id := Cast[u64](st.views.Len())
    try st.views.Push(a, rec)
    return id
}

AllocSamplerSlot(st: mut VkDeviceState, a: &Alloc, rec: VkSamplerRec): OutOfMemory!u64 {
    if st.samplers.Len() == 0 {
        try st.samplers.Push(a, mem.Zeroed[VkSamplerRec]())
    }
    for i in 1..st.samplers.Len() {
        if !st.samplers[i].alive {
            st.samplers[i] = rec
            return Cast[u64](i)
        }
    }
    id := Cast[u64](st.samplers.Len())
    try st.samplers.Push(a, rec)
    return id
}

/// Creates a zeroed Vulkan buffer and returns its backend ID.
export Device.CreateBuffer(mut self, d: BufferDesc): Error!u64 {
    a := self.heap.Alloc()
    st := self.state.Mut()
    if st.lost {
        fail DeviceLost(reason: st.loss_reason)
    }
    aligned_sz := AlignUpMem(Cast[u64](d.size), 4)
    var usage: u32 = Cast[u32](vulkan.VkBufferUsageTransferDstBit)
    if d.vertex { usage |= Cast[u32](vulkan.VkBufferUsageVertexBufferBit) }
    if d.index { usage |= Cast[u32](vulkan.VkBufferUsageIndexBufferBit) }
    if d.uniform || d.uniform_dynamic { usage |= Cast[u32](vulkan.VkBufferUsageUniformBufferBit) }
    if d.storage { usage |= Cast[u32](vulkan.VkBufferUsageStorageBufferBit) }
    if d.copy_src || d.map_write { usage |= Cast[u32](vulkan.VkBufferUsageTransferSrcBit) }

    unsafe "st.device is live" {
        dev := HandleFrom[vulkan.VkDeviceT](st.device)
        var bci := mem.Zeroed[vulkan.VkBufferCreateInfo]()
        bci.s_type = vulkan.VkStructureTypeBufferCreateInfo
        bci.size = aligned_sz
        bci.usage = usage
        bci.sharing_mode = vulkan.VkSharingModeExclusive
        var buf: vulkan.VkBuffer = raw.Null[vulkan.VkBufferT]()
        r := st.dev_d.vk_create_buffer.Must()(dev, raw.Of(&bci), raw.Null[vulkan.VkAllocationCallbacks](), raw.OfMut(mut buf))
        if r != vulkan.VkSuccess {
            if IsDeviceLostResult(r) { st.lost = true }
            fail MapVkError(r, CallCreateBuffer)
        }
        st.live_native += 1

        var ded_req := mem.Zeroed[vulkan.VkMemoryDedicatedRequirements]()
        ded_req.s_type = vulkan.VkStructureTypeMemoryDedicatedRequirements
        var req2 := mem.Zeroed[vulkan.VkMemoryRequirements2]()
        req2.s_type = vulkan.VkStructureTypeMemoryRequirements2
        req2.p_next = raw.PtrCast[vulkan.VkMemoryDedicatedRequirements, void](raw.Of(&ded_req))
        var info2 := mem.Zeroed[vulkan.VkBufferMemoryRequirementsInfo2]()
        info2.s_type = vulkan.VkStructureTypeBufferMemoryRequirementsInfo2
        info2.buffer = buf
        st.dev_d.vk_get_buffer_memory_requirements2.Must()(dev, raw.Of(&info2), raw.OfMut(mut req2))
        req := &req2.memory_requirements

        var req_flags: u32 = Cast[u32](vulkan.VkMemoryPropertyDeviceLocalBit)
        var pref_flags: u32 = 0
        host_vis := d.map_read || d.map_write
        if d.map_read {
            req_flags = Cast[u32](vulkan.VkMemoryPropertyHostVisibleBit) | Cast[u32](vulkan.VkMemoryPropertyHostCoherentBit)
            pref_flags = Cast[u32](vulkan.VkMemoryPropertyHostCachedBit)
        } else if d.map_write {
            req_flags = Cast[u32](vulkan.VkMemoryPropertyHostVisibleBit) | Cast[u32](vulkan.VkMemoryPropertyHostCoherentBit)
        }
        mt := match FindMemoryType(&st.mem_props, req.memory_type_bits, req_flags, pref_flags) {
            some(m) { m }
            none {
                // Fallback without HOST_COHERENT if driver lacked it.
                fb := if host_vis { Cast[u32](vulkan.VkMemoryPropertyHostVisibleBit) } else { req_flags }
                match FindMemoryType(&st.mem_props, req.memory_type_bits, fb, 0) {
                    some(m2) { m2 }
                    none {
                        st.dev_d.vk_destroy_buffer.Must()(dev, buf, raw.Null[vulkan.VkAllocationCallbacks]())
                        if st.live_native > 0 { st.live_native -= 1 }
                        fail OutOfDeviceMemory
                    }
                }
            }
        }

        force_ded := ded_req.requires_dedicated_allocation != 0 || ded_req.prefers_dedicated_allocation != 0
        al := AllocDeviceMem(a, st, req.size, req.alignment, mt, false, host_vis, force_ded, HandleAddr(buf), 0) else e {
            st.dev_d.vk_destroy_buffer.Must()(dev, buf, raw.Null[vulkan.VkAllocationCallbacks]())
            if st.live_native > 0 { st.live_native -= 1 }
            fail e
        }
        vkmem := if al.dedicated_mem != 0 { al.dedicated_mem } else { st.mem_blocks[al.block_idx].memory }
        br := st.dev_d.vk_bind_buffer_memory.Must()(dev, buf, HandleFrom[vulkan.VkDeviceMemoryT](vkmem), al.offset)
        if br != vulkan.VkSuccess {
            FreeDeviceMem(a, st, al)
            st.dev_d.vk_destroy_buffer.Must()(dev, buf, raw.Null[vulkan.VkAllocationCallbacks]())
            if st.live_native > 0 { st.live_native -= 1 }
            if IsDeviceLostResult(br) { st.lost = true }
            fail MapVkError(br, CallCreateBuffer)
        }
        if host_vis && al.mapped_ptr != 0 {
            raw.Fill(raw.FromAddr[u8](al.mapped_ptr), 0, Cast[int](aligned_sz))
            FlushOrInvalidateHostMem(&*st, &al, false)
        }

        rec := VkBufRec(
            alive: true,
            vk_buffer: HandleAddr(buf),
            size: aligned_sz,
            alloc: al,
            needs_zero: !host_vis,
            map_read: d.map_read,
            map_write: d.map_write,
        )
        id := AllocBufSlot(st, a, rec) else e {
            FreeDeviceMem(a, st, al)
            st.dev_d.vk_destroy_buffer.Must()(dev, buf, raw.Null[vulkan.VkAllocationCallbacks]())
            if st.live_native > 0 { st.live_native -= 1 }
            fail e
        }
        if !host_vis {
            st.pending_zero_bufs.Push(a, id) else {}
        }
        SetVkObjectName(&*st, vulkan.VkObjectTypeBuffer, HandleAddr(buf), d.label)
        return id
    }
}

DestroyBufferRec(a: &Alloc, st: mut VkDeviceState, id: u64) {
    idx := Cast[int](id)
    if idx <= 0 || idx >= st.buffers.Len() || !st.buffers[idx].alive {
        return
    }
    rec := st.buffers[idx]
    st.buffers[idx].alive = false
    if rec.size == 0 {
        return
    }
    unsafe "st.device and rec.vk_buffer are live" {
        st.dev_d.vk_destroy_buffer.Must()(HandleFrom[vulkan.VkDeviceT](st.device), HandleFrom[vulkan.VkBufferT](rec.vk_buffer), raw.Null[vulkan.VkAllocationCallbacks]())
        if st.live_native > 0 {
            st.live_native -= 1
        }
    }
    FreeDeviceMem(a, st, rec.alloc)
}

/// Destroys a Vulkan buffer and returns its memory span to TLSF.
export Device.DestroyBuffer(mut self, id: u64) {
    a := self.heap.Alloc()
    DestroyBufferRec(a, self.state.Mut(), id)
}

/// Creates a zeroed Vulkan image and returns its backend ID.
export Device.CreateTexture(mut self, d: TextureDesc): Error!u64 {
    a := self.heap.Alloc()
    st := self.state.Mut()
    if st.lost {
        fail DeviceLost(reason: st.loss_reason)
    }
    vk_fmt := if d.format_idx == TexFmtDepth24PlusStencil8Idx { st.d24s8_vk_format } else { d.vk_format }
    var usage: u32 = Cast[u32](vulkan.VkImageUsageTransferDstBit)
    if d.copy_src { usage |= Cast[u32](vulkan.VkImageUsageTransferSrcBit) }
    if d.sampled { usage |= Cast[u32](vulkan.VkImageUsageSampledBit) }
    if d.render_target {
        if d.is_depth {
            usage |= Cast[u32](vulkan.VkImageUsageDepthStencilAttachmentBit)
        } else {
            usage |= Cast[u32](vulkan.VkImageUsageColorAttachmentBit)
        }
    }
    var flags: u32 = 0
    if d.width == d.height && d.layers >= 6 && d.samples == 1 {
        flags |= Cast[u32](vulkan.VkImageCreateCubeCompatibleBit)
    }
    var fmt_list: [4]i32 = [vk_fmt, 0, 0, 0]
    var fmt_cnt: u32 = 1
    if d.view_formats.Len() > 0 {
        flags |= Cast[u32](vulkan.VkImageCreateMutableFormatBit)
        for i in 0..d.view_formats.Len() {
            if int(fmt_cnt) < 4 {
                fmt_list[int(fmt_cnt)] = d.view_formats[i]
                fmt_cnt += 1
            }
        }
    }

    unsafe "st.device is live" {
        dev := HandleFrom[vulkan.VkDeviceT](st.device)
        var fli := mem.Zeroed[vulkan.VkImageFormatListCreateInfo]()
        fli.s_type = vulkan.VkStructureTypeImageFormatListCreateInfo
        fli.view_format_count = fmt_cnt
        fli.p_view_formats = raw.Data(fmt_list[..])

        var ici := mem.Zeroed[vulkan.VkImageCreateInfo]()
        ici.s_type = vulkan.VkStructureTypeImageCreateInfo
        if d.view_formats.Len() > 0 {
            ici.p_next = raw.PtrCast[vulkan.VkImageFormatListCreateInfo, void](raw.Of(&fli))
        }
        ici.flags = flags
        ici.image_type = vulkan.VkImageType2d
        ici.format = vk_fmt
        ici.extent = vulkan.VkExtent3D(width: Cast[u32](d.width), height: Cast[u32](d.height), depth: 1)
        ici.mip_levels = Cast[u32](d.mips)
        ici.array_layers = Cast[u32](d.layers)
        ici.samples = if d.samples == 4 { vulkan.VkSampleCount4Bit } else { vulkan.VkSampleCount1Bit }
        ici.tiling = vulkan.VkImageTilingOptimal
        ici.usage = usage
        ici.sharing_mode = vulkan.VkSharingModeExclusive
        ici.initial_layout = vulkan.VkImageLayoutUndefined

        var img: vulkan.VkImage = raw.Null[vulkan.VkImageT]()
        r := st.dev_d.vk_create_image.Must()(dev, raw.Of(&ici), raw.Null[vulkan.VkAllocationCallbacks](), raw.OfMut(mut img))
        if r != vulkan.VkSuccess {
            if IsDeviceLostResult(r) { st.lost = true }
            fail MapVkError(r, CallCreateImage)
        }
        st.live_native += 1

        var ded_req := mem.Zeroed[vulkan.VkMemoryDedicatedRequirements]()
        ded_req.s_type = vulkan.VkStructureTypeMemoryDedicatedRequirements
        var req2 := mem.Zeroed[vulkan.VkMemoryRequirements2]()
        req2.s_type = vulkan.VkStructureTypeMemoryRequirements2
        req2.p_next = raw.PtrCast[vulkan.VkMemoryDedicatedRequirements, void](raw.Of(&ded_req))
        var info2 := mem.Zeroed[vulkan.VkImageMemoryRequirementsInfo2]()
        info2.s_type = vulkan.VkStructureTypeImageMemoryRequirementsInfo2
        info2.image = img
        st.dev_d.vk_get_image_memory_requirements2.Must()(dev, raw.Of(&info2), raw.OfMut(mut req2))
        req := &req2.memory_requirements

        mt := match FindMemoryType(&st.mem_props, req.memory_type_bits, Cast[u32](vulkan.VkMemoryPropertyDeviceLocalBit), 0) {
            some(m) { m }
            none {
                st.dev_d.vk_destroy_image.Must()(dev, img, raw.Null[vulkan.VkAllocationCallbacks]())
                if st.live_native > 0 { st.live_native -= 1 }
                fail OutOfDeviceMemory
            }
        }
        force_ded := ded_req.requires_dedicated_allocation != 0 || ded_req.prefers_dedicated_allocation != 0
        al := AllocDeviceMem(a, st, req.size, req.alignment, mt, true, false, force_ded, 0, HandleAddr(img)) else e {
            st.dev_d.vk_destroy_image.Must()(dev, img, raw.Null[vulkan.VkAllocationCallbacks]())
            if st.live_native > 0 { st.live_native -= 1 }
            fail e
        }
        vkmem := if al.dedicated_mem != 0 { al.dedicated_mem } else { st.mem_blocks[al.block_idx].memory }
        br := st.dev_d.vk_bind_image_memory.Must()(dev, img, HandleFrom[vulkan.VkDeviceMemoryT](vkmem), al.offset)
        if br != vulkan.VkSuccess {
            FreeDeviceMem(a, st, al)
            st.dev_d.vk_destroy_image.Must()(dev, img, raw.Null[vulkan.VkAllocationCallbacks]())
            if st.live_native > 0 { st.live_native -= 1 }
            if IsDeviceLostResult(br) { st.lost = true }
            fail MapVkError(br, CallCreateImage)
        }

        planes := if d.has_stencil { 2 } else { 1 }
        sub_count := planes * d.mips * d.layers
        t_start := st.tracked_layouts.Len()
        for _ in 0..sub_count {
            st.tracked_layouts.Push(a, vulkan.VkImageLayoutUndefined) else e {
                FreeDeviceMem(a, st, al)
                st.dev_d.vk_destroy_image.Must()(dev, img, raw.Null[vulkan.VkAllocationCallbacks]())
                if st.live_native > 0 { st.live_native -= 1 }
                fail e
            }
        }

        rec := VkTexRec(
            alive: true,
            vk_image: HandleAddr(img),
            vk_format: vk_fmt,
            width: d.width,
            height: d.height,
            layers: d.layers,
            mips: d.mips,
            samples: d.samples,
            is_depth: d.is_depth,
            has_stencil: d.has_stencil,
            is_compressed: d.is_compressed,
            block_w: d.block_w,
            block_h: d.block_h,
            block_bytes: d.block_bytes,
            alloc: al,
            needs_zero: true,
            tracked_start: t_start,
        )
        id := AllocTexSlot(st, a, rec) else e {
            FreeDeviceMem(a, st, al)
            st.dev_d.vk_destroy_image.Must()(dev, img, raw.Null[vulkan.VkAllocationCallbacks]())
            if st.live_native > 0 { st.live_native -= 1 }
            fail e
        }
        st.pending_zero_texs.Push(a, id) else {}
        SetVkObjectName(&*st, vulkan.VkObjectTypeImage, HandleAddr(img), d.label)
        return id
    }
}

DestroyTextureRec(a: &Alloc, st: mut VkDeviceState, id: u64) {
    idx := Cast[int](id)
    if idx <= 0 || idx >= st.textures.Len() || !st.textures[idx].alive {
        return
    }
    rec := st.textures[idx]
    st.textures[idx].alive = false
    unsafe "st.device and rec.vk_image are live" {
        st.dev_d.vk_destroy_image.Must()(HandleFrom[vulkan.VkDeviceT](st.device), HandleFrom[vulkan.VkImageT](rec.vk_image), raw.Null[vulkan.VkAllocationCallbacks]())
        if st.live_native > 0 {
            st.live_native -= 1
        }
    }
    FreeDeviceMem(a, st, rec.alloc)
}

/// Destroys a Vulkan image and returns its memory span to TLSF.
export Device.DestroyTexture(mut self, id: u64) {
    a := self.heap.Alloc()
    DestroyTextureRec(a, self.state.Mut(), id)
}

AspectFlagsForView(is_depth: bool, has_stencil: bool, aspect: int): u32 {
    if !is_depth {
        return Cast[u32](vulkan.VkImageAspectColorBit)
    }
    if !has_stencil {
        return Cast[u32](vulkan.VkImageAspectDepthBit)
    }
    return match aspect {
        1 { Cast[u32](vulkan.VkImageAspectDepthBit) }
        2 { Cast[u32](vulkan.VkImageAspectStencilBit) }
        _ { Cast[u32](vulkan.VkImageAspectDepthBit) | Cast[u32](vulkan.VkImageAspectStencilBit) }
    }
}

/// Creates a Vulkan image view and returns its backend ID.
export Device.CreateView(mut self, d: ViewDesc): Error!u64 {
    a := self.heap.Alloc()
    st := self.state.Mut()
    if st.lost {
        fail DeviceLost(reason: st.loss_reason)
    }
    tidx := Cast[int](d.tex_id)
    if tidx <= 0 || tidx >= st.textures.Len() || !st.textures[tidx].alive {
        fail Driver(code: -1, call: CallCreateImage)
    }
    tex := st.textures[tidx]
    vk_fmt := if d.format_idx == TexFmtDepth24PlusStencil8Idx { st.d24s8_vk_format } else { d.vk_format }
    vtype := match d.dim {
        1 { vulkan.VkImageViewType2dArray }
        2 { vulkan.VkImageViewTypeCube }
        3 { vulkan.VkImageViewTypeCubeArray }
        _ { vulkan.VkImageViewType2d }
    }
    amask := AspectFlagsForView(d.is_depth, d.has_stencil, d.aspect)
    pmask: u8 = if !d.is_depth || !d.has_stencil {
        1
    } else if d.aspect == 1 {
        1
    } else if d.aspect == 2 {
        2
    } else {
        3
    }

    unsafe "st.device and tex.vk_image are live" {
        dev := HandleFrom[vulkan.VkDeviceT](st.device)
        var ivci := mem.Zeroed[vulkan.VkImageViewCreateInfo]()
        ivci.s_type = vulkan.VkStructureTypeImageViewCreateInfo
        ivci.image = HandleFrom[vulkan.VkImageT](tex.vk_image)
        ivci.view_type = vtype
        ivci.format = vk_fmt
        ivci.subresource_range = vulkan.VkImageSubresourceRange(
            aspect_mask: amask,
            base_mip_level: Cast[u32](d.first_mip),
            level_count: Cast[u32](d.mip_count),
            base_array_layer: Cast[u32](d.first_layer),
            layer_count: Cast[u32](d.layer_count),
        )
        var iv: vulkan.VkImageView = raw.Null[vulkan.VkImageViewT]()
        r := st.dev_d.vk_create_image_view.Must()(dev, raw.Of(&ivci), raw.Null[vulkan.VkAllocationCallbacks](), raw.OfMut(mut iv))
        if r != vulkan.VkSuccess {
            if IsDeviceLostResult(r) { st.lost = true }
            fail MapVkError(r, CallCreateImage)
        }
        st.live_native += 1

        rec := VkViewRec(
            alive: true,
            vk_view: HandleAddr(iv),
            tex_id: d.tex_id,
            vk_image: tex.vk_image,
            is_swapchain: false,
            sc_id: 0,
            sc_img_idx: 0,
            first_mip: d.first_mip,
            mip_count: d.mip_count,
            first_layer: d.first_layer,
            layer_count: d.layer_count,
            plane_mask: pmask,
            is_depth: d.is_depth,
            has_stencil: d.has_stencil,
        )
        return AllocViewSlot(st, a, rec) else e {
            st.dev_d.vk_destroy_image_view.Must()(dev, iv, raw.Null[vulkan.VkAllocationCallbacks]())
            if st.live_native > 0 { st.live_native -= 1 }
            fail e
        }
    }
}

DestroyViewRec(st: mut VkDeviceState, id: u64) {
    idx := Cast[int](id)
    if idx <= 0 || idx >= st.views.Len() || !st.views[idx].alive {
        return
    }
    rec := st.views[idx]
    st.views[idx].alive = false
    if rec.vk_view != 0 {
        unsafe "st.device and rec.vk_view are live" {
            st.dev_d.vk_destroy_image_view.Must()(HandleFrom[vulkan.VkDeviceT](st.device), HandleFrom[vulkan.VkImageViewT](rec.vk_view), raw.Null[vulkan.VkAllocationCallbacks]())
            if st.live_native > 0 {
                st.live_native -= 1
            }
        }
    }
}

/// Destroys a Vulkan image view.
export Device.DestroyView(mut self, id: u64) {
    DestroyViewRec(self.state.Mut(), id)
}

MapVkAddressMode(a: int): vulkan.VkSamplerAddressMode {
    return match a {
        0 { vulkan.VkSamplerAddressModeRepeat }
        1 { vulkan.VkSamplerAddressModeMirroredRepeat }
        _ { vulkan.VkSamplerAddressModeClampToEdge }
    }
}

MapVkCompareOp(c: int): vulkan.VkCompareOp {
    return match c {
        0 { vulkan.VkCompareOpNever }
        1 { vulkan.VkCompareOpLess }
        2 { vulkan.VkCompareOpEqual }
        3 { vulkan.VkCompareOpLessOrEqual }
        4 { vulkan.VkCompareOpGreater }
        5 { vulkan.VkCompareOpNotEqual }
        6 { vulkan.VkCompareOpGreaterOrEqual }
        _ { vulkan.VkCompareOpAlways }
    }
}

/// Creates a Vulkan sampler and returns its backend ID.
export Device.CreateSampler(mut self, d: SamplerDesc): Error!u64 {
    a := self.heap.Alloc()
    st := self.state.Mut()
    if st.lost {
        fail DeviceLost(reason: st.loss_reason)
    }
    unsafe "st.device is live" {
        dev := HandleFrom[vulkan.VkDeviceT](st.device)
        var sci := mem.Zeroed[vulkan.VkSamplerCreateInfo]()
        sci.s_type = vulkan.VkStructureTypeSamplerCreateInfo
        sci.mag_filter = if d.mag_linear { vulkan.VkFilterLinear } else { vulkan.VkFilterNearest }
        sci.min_filter = if d.min_linear { vulkan.VkFilterLinear } else { vulkan.VkFilterNearest }
        sci.mipmap_mode = if d.mip_linear { vulkan.VkSamplerMipmapModeLinear } else { vulkan.VkSamplerMipmapModeNearest }
        sci.address_mode_u = MapVkAddressMode(d.addr_u)
        sci.address_mode_v = MapVkAddressMode(d.addr_v)
        sci.address_mode_w = MapVkAddressMode(d.addr_w)
        sci.anisotropy_enable = if d.anisotropy > 1 { vulkan.VkTrue } else { vulkan.VkFalse }
        sci.max_anisotropy = Cast[f32](d.anisotropy)
        sci.compare_enable = if d.has_compare { vulkan.VkTrue } else { vulkan.VkFalse }
        sci.compare_op = MapVkCompareOp(d.compare)
        sci.min_lod = d.lod_min
        sci.max_lod = d.lod_max
        var smp: vulkan.VkSampler = raw.Null[vulkan.VkSamplerT]()
        r := st.dev_d.vk_create_sampler.Must()(dev, raw.Of(&sci), raw.Null[vulkan.VkAllocationCallbacks](), raw.OfMut(mut smp))
        if r != vulkan.VkSuccess {
            if IsDeviceLostResult(r) { st.lost = true }
            fail MapVkError(r, CallCreateDevice)
        }
        st.live_native += 1
        return AllocSamplerSlot(st, a, VkSamplerRec(alive: true, vk_sampler: HandleAddr(smp))) else e {
            st.dev_d.vk_destroy_sampler.Must()(dev, smp, raw.Null[vulkan.VkAllocationCallbacks]())
            if st.live_native > 0 { st.live_native -= 1 }
            fail e
        }
    }
}

DestroySamplerRec(st: mut VkDeviceState, id: u64) {
    idx := Cast[int](id)
    if idx <= 0 || idx >= st.samplers.Len() || !st.samplers[idx].alive {
        return
    }
    rec := st.samplers[idx]
    st.samplers[idx].alive = false
    unsafe "st.device and rec.vk_sampler are live" {
        st.dev_d.vk_destroy_sampler.Must()(HandleFrom[vulkan.VkDeviceT](st.device), HandleFrom[vulkan.VkSamplerT](rec.vk_sampler), raw.Null[vulkan.VkAllocationCallbacks]())
        if st.live_native > 0 {
            st.live_native -= 1
        }
    }
}

/// Destroys a Vulkan sampler.
export Device.DestroySampler(mut self, id: u64) {
    DestroySamplerRec(self.state.Mut(), id)
}

AllocUploadRegion(a: &Alloc into st, st: mut VkDeviceState, bytes: []u8, align: int): Error!(vk_buf: u64, offset: u64) {
    n := bytes.Len()
    sz := if n == 0 { 4 } else { n }
    completed := PollCompletedSerial(st)
    if st.lost { fail DeviceLost(reason: st.loss_reason) }
    for ci in 0..st.upload_chunks.Len() {
        serial := st.upload_chunks[ci].serial
        if serial > completed { continue }
        if serial != 0 {
            st.upload_chunks[ci].used = 0
            st.upload_chunks[ci].serial = 0
        }
        off := AlignUpInt(st.upload_chunks[ci].used, align)
        if off + sz <= st.upload_chunks[ci].size {
            al := st.upload_chunks[ci].alloc
            vk_buf := st.upload_chunks[ci].vk_buffer
            if n > 0 && al.mapped_ptr != 0 {
                unsafe "al.mapped_ptr is live and in bounds" {
                    dst := raw.FromAddr[u8](al.mapped_ptr + Trunc[uint](off))
                    raw.Copy(dst, raw.Data(bytes), n)
                    FlushOrInvalidateHostMem(&*st, &al, false)
                }
            }
            st.upload_chunks[ci].used = off + sz
            return (vk_buf: vk_buf, offset: Cast[u64](off))
        }
    }
    chunk_sz := if sz > 4 * 1024 * 1024 { AlignUpInt(sz, 4096) } else { 4 * 1024 * 1024 }
    unsafe "st.device is live" {
        dev := HandleFrom[vulkan.VkDeviceT](st.device)
        var bci := mem.Zeroed[vulkan.VkBufferCreateInfo]()
        bci.s_type = vulkan.VkStructureTypeBufferCreateInfo
        bci.size = Cast[u64](chunk_sz)
        bci.usage = Cast[u32](vulkan.VkBufferUsageTransferSrcBit)
        bci.sharing_mode = vulkan.VkSharingModeExclusive
        var buf: vulkan.VkBuffer = raw.Null[vulkan.VkBufferT]()
        r := st.dev_d.vk_create_buffer.Must()(dev, raw.Of(&bci), raw.Null[vulkan.VkAllocationCallbacks](), raw.OfMut(mut buf))
        if r != vulkan.VkSuccess {
            fail MapVkError(r, CallCreateBuffer)
        }
        var req := mem.Zeroed[vulkan.VkMemoryRequirements]()
        st.dev_d.vk_get_buffer_memory_requirements.Must()(dev, buf, raw.OfMut(mut req))
        mt := match FindMemoryType(&st.mem_props, req.memory_type_bits, Cast[u32](vulkan.VkMemoryPropertyHostVisibleBit) | Cast[u32](vulkan.VkMemoryPropertyHostCoherentBit), 0) {
            some(m) { m }
            none {
                st.dev_d.vk_destroy_buffer.Must()(dev, buf, raw.Null[vulkan.VkAllocationCallbacks]())
                fail OutOfDeviceMemory
            }
        }
        al := AllocDeviceMem(a, st, req.size, req.alignment, mt, false, true, false, 0, 0) else e {
            st.dev_d.vk_destroy_buffer.Must()(dev, buf, raw.Null[vulkan.VkAllocationCallbacks]())
            fail e
        }
        vkmem := if al.dedicated_mem != 0 { al.dedicated_mem } else { st.mem_blocks[al.block_idx].memory }
        br := st.dev_d.vk_bind_buffer_memory.Must()(dev, buf, HandleFrom[vulkan.VkDeviceMemoryT](vkmem), al.offset)
        if br != vulkan.VkSuccess {
            FreeDeviceMem(a, st, al)
            st.dev_d.vk_destroy_buffer.Must()(dev, buf, raw.Null[vulkan.VkAllocationCallbacks]())
            fail MapVkError(br, CallCreateBuffer)
        }
        if n > 0 && al.mapped_ptr != 0 {
            raw.Copy(raw.FromAddr[u8](al.mapped_ptr), raw.Data(bytes), n)
            FlushOrInvalidateHostMem(&*st, &al, false)
        }
        uc := VkUploadBuf(vk_buffer: HandleAddr(buf), alloc: al, size: chunk_sz, used: sz, serial: 0)
        st.upload_chunks.Push(a, uc) else e {
            FreeDeviceMem(a, st, al)
            st.dev_d.vk_destroy_buffer.Must()(dev, buf, raw.Null[vulkan.VkAllocationCallbacks]())
            fail e
        }
        return (vk_buf: HandleAddr(buf), offset: 0)
    }
}

AlignUpInt(v: int, align: int): int {
    if align <= 1 {
        return v
    }
    rem := v % align
    if rem == 0 {
        return v
    }
    return v + (align - rem)
}

/// Copies data into the upload ring and queues a pre-submit buffer copy.
export Device.WriteBufferPre(mut self, buf_id: u64, at: int, data: []u8): Error!void {
    a := self.heap.Alloc()
    st := self.state.Mut()
    if st.lost { fail DeviceLost(reason: st.loss_reason) }
    bidx := Cast[int](buf_id)
    if bidx <= 0 || bidx >= st.buffers.Len() || !st.buffers[bidx].alive {
        fail Driver(code: -1, call: CallCreateBuffer)
    }
    if data.Len() == 0 {
        return
    }
    reg := try AllocUploadRegion(a, st, data, 4)
    try st.pending_copies.Push(a, VkPreCopyOp(
        kind: 0, src_buf: reg.vk_buf, dst_handle: buf_id, src_off: reg.offset, dst_off: Cast[u64](at),
        size: Cast[u64](data.Len()), mip: 0, layer: 0, layers: 0, x: 0, y: 0, w: 0, h: 0, bpr: 0, rpl: 0, pmask: 0,
    ))
}

/// Stages the write and returns the native upload buffer and byte offset.
export Device.StageEncoderWrite(mut self, data: []u8): Error!(vk_buf: u64, offset: u64) {
    a := self.heap.Alloc()
    st := self.state.Mut()
    if st.lost { fail DeviceLost(reason: st.loss_reason) }
    return try AllocUploadRegion(a, st, data, 4)
}

/// Queues a pre-submit tight-row texture write from data.
export Device.WriteTexturePre(mut self, tex_id: u64, mip: int, layer: int, x: int, y: int, w: int, h: int, layers: int, pmask: u8, data: []u8): Error!void {
    a := self.heap.Alloc()
    st := self.state.Mut()
    if st.lost { fail DeviceLost(reason: st.loss_reason) }
    tidx := Cast[int](tex_id)
    if tidx <= 0 || tidx >= st.textures.Len() || !st.textures[tidx].alive {
        fail Driver(code: -1, call: CallCreateImage)
    }
    tex := st.textures[tidx]
    bx := (w + tex.block_w - 1) / tex.block_w
    by := (h + tex.block_h - 1) / tex.block_h
    need := bx * tex.block_bytes * by * layers
    if data.Len() < need {
        fail Driver(code: -1, call: CallCreateImage)
    }
    reg := try AllocUploadRegion(a, st, data[0..need], 16)
    try st.pending_copies.Push(a, VkPreCopyOp(
        kind: 1, src_buf: reg.vk_buf, dst_handle: tex_id, src_off: reg.offset, dst_off: 0,
        size: Cast[u64](need), mip: Cast[u32](mip), layer: Cast[u32](layer), layers: Cast[u32](layers),
        x: Cast[i32](x), y: Cast[i32](y), w: Cast[u32](w), h: Cast[u32](h),
        bpr: 0, rpl: 0, pmask: pmask,
    ))
}

/// Returns a read-only slice over a persistently mapped map_read buffer.
export Device.MapBytes(&self, buf_id: u64, at: int, n: int): []u8 from self {
    st := self.state.Ref()
    bidx := Cast[int](buf_id)
    rec := &st.buffers[bidx]
    FlushOrInvalidateHostMem(st, &rec.alloc, true)
    unsafe "rec.alloc.mapped_ptr is persistently mapped for the buffer's lifetime" {
        return raw.Slice(raw.FromAddr[u8](rec.alloc.mapped_ptr + Trunc[uint](at)), n)
    }
}

/// Returns a mutable slice over a persistently mapped map_write buffer.
export Device.MapBytesMut(mut self, buf_id: u64, at: int, n: int): mut []u8 from self {
    st := self.state.Ref()
    bidx := Cast[int](buf_id)
    rec := &st.buffers[bidx]
    unsafe "rec.alloc.mapped_ptr is persistently mapped for the buffer's lifetime" {
        return raw.SliceMut(raw.FromAddr[u8](rec.alloc.mapped_ptr + Trunc[uint](at)), n)
    }
}

/// Flushes a non-coherent mapped range after CPU writes.
export Device.FlushMapped(mut self, buf_id: u64) {
    st := self.state.Ref()
    bidx := Cast[int](buf_id)
    FlushOrInvalidateHostMem(st, &st.buffers[bidx].alloc, false)
}

/// Reads a texture region synchronously via a readback staging buffer.
export Device.ReadTexture(mut self, io: Io, serial: u64, tex_id: u64, mip: int, layer: int, x: int, y: int, w: int, h: int, layers: int, pmask: u8, dst: mut []u8): Error!void {
    a := self.heap.Alloc()
    st := self.state.Mut()
    if st.lost { fail DeviceLost(reason: st.loss_reason) }
    tidx := Cast[int](tex_id)
    if tidx <= 0 || tidx >= st.textures.Len() || !st.textures[tidx].alive {
        fail Driver(code: -1, call: CallCreateImage)
    }
    need := dst.Len()
    if need == 0 {
        return
    }

    // Create temporary host-cached readback buffer.
    var stg_buf: u64 = 0
    var stg_al := mem.Zeroed[VkMemAlloc]()
    unsafe "st.device is live" {
        dev := HandleFrom[vulkan.VkDeviceT](st.device)
        var bci := mem.Zeroed[vulkan.VkBufferCreateInfo]()
        bci.s_type = vulkan.VkStructureTypeBufferCreateInfo
        bci.size = AlignUpMem(Cast[u64](need), 4)
        bci.usage = Cast[u32](vulkan.VkBufferUsageTransferDstBit)
        bci.sharing_mode = vulkan.VkSharingModeExclusive
        var buf: vulkan.VkBuffer = raw.Null[vulkan.VkBufferT]()
        r := st.dev_d.vk_create_buffer.Must()(dev, raw.Of(&bci), raw.Null[vulkan.VkAllocationCallbacks](), raw.OfMut(mut buf))
        if r != vulkan.VkSuccess {
            fail MapVkError(r, CallCreateBuffer)
        }
        var req := mem.Zeroed[vulkan.VkMemoryRequirements]()
        st.dev_d.vk_get_buffer_memory_requirements.Must()(dev, buf, raw.OfMut(mut req))
        mt := match FindMemoryType(&st.mem_props, req.memory_type_bits, Cast[u32](vulkan.VkMemoryPropertyHostVisibleBit) | Cast[u32](vulkan.VkMemoryPropertyHostCoherentBit), Cast[u32](vulkan.VkMemoryPropertyHostCachedBit)) {
            some(m) { m }
            none {
                st.dev_d.vk_destroy_buffer.Must()(dev, buf, raw.Null[vulkan.VkAllocationCallbacks]())
                fail OutOfDeviceMemory
            }
        }
        stg_al = AllocDeviceMem(a, st, req.size, req.alignment, mt, false, true, false, 0, 0) else e {
            st.dev_d.vk_destroy_buffer.Must()(dev, buf, raw.Null[vulkan.VkAllocationCallbacks]())
            fail e
        }
        vkmem := if stg_al.dedicated_mem != 0 { stg_al.dedicated_mem } else { st.mem_blocks[stg_al.block_idx].memory }
        br := st.dev_d.vk_bind_buffer_memory.Must()(dev, buf, HandleFrom[vulkan.VkDeviceMemoryT](vkmem), stg_al.offset)
        if br != vulkan.VkSuccess {
            FreeDeviceMem(a, st, stg_al)
            st.dev_d.vk_destroy_buffer.Must()(dev, buf, raw.Null[vulkan.VkAllocationCallbacks]())
            fail MapVkError(br, CallCreateBuffer)
        }
        stg_buf = HandleAddr(buf)
    }

    defer {
        unsafe "stg_buf and st.device are live" {
            st.dev_d.vk_destroy_buffer.Must()(HandleFrom[vulkan.VkDeviceT](st.device), HandleFrom[vulkan.VkBufferT](stg_buf), raw.Null[vulkan.VkAllocationCallbacks]())
            FreeDeviceMem(a, st, stg_al)
        }
    }

    ser := try SubmitReadbackTexture(a, st, serial, tex_id, stg_buf, mip, layer, x, y, w, h, layers, pmask)
    WaitForSerial(st, io, ser) else e {
        match e {
            Canceled { _ = WaitForSerial(st, io.NoCancel(), ser) else {} }
            _ {}
        }
        fail e
    }
    FlushOrInvalidateHostMem(&*st, &stg_al, true)
    unsafe "stg_al.mapped_ptr holds need bytes from GPU copy" {
        raw.Copy(raw.DataMut[u8](dst), raw.FromAddr[u8](stg_al.mapped_ptr), need)
    }
}
