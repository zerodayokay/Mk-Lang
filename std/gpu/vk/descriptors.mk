// Why: Vulkan shaders access resources via VkDescriptorSetLayout,
// VkPipelineLayout, and VkDescriptorSet allocated from per-layout 64-set pools.
//
// What: CreateBindGroupLayout, DestroyBindGroupLayout, CreatePipelineLayout,
// DestroyPipelineLayout, CreateBindGroup, and DestroyBindGroup.
//
// How: keeps a free list of VkDescriptorSets per BindGroupLayout so closed
// bind groups recycle sets after their GPU serial finishes, and stores bound
// TextureView IDs on each BindGroup for replay-time layout verification.

import c.vulkan
import std.mem
import std.raw

MapStageFlags(vs: bool, fs: bool, cs: bool): u32 {
    var f: u32 = 0
    if vs { f |= Cast[u32](vulkan.VkShaderStageVertexBit) }
    if fs { f |= Cast[u32](vulkan.VkShaderStageFragmentBit) }
    if cs { f |= Cast[u32](vulkan.VkShaderStageComputeBit) }
    return f
}

MapDescriptorType(kind: int, dynamic: bool): vulkan.VkDescriptorType {
    return match kind {
        0 { if dynamic { vulkan.VkDescriptorTypeUniformBufferDynamic } else { vulkan.VkDescriptorTypeUniformBuffer } }
        1 { vulkan.VkDescriptorTypeStorageBuffer }
        2 { vulkan.VkDescriptorTypeSampledImage }
        _ { vulkan.VkDescriptorTypeSampler }
    }
}

AllocPipeLayoutSlot(st: mut VkDeviceState, a: &Alloc, rec: VkPipeLayoutRec): OutOfMemory!u64 {
    if st.pipe_layouts.Len() == 0 {
        try st.pipe_layouts.Push(a, mem.Zeroed[VkPipeLayoutRec]())
    }
    for i in 1..st.pipe_layouts.Len() {
        if !st.pipe_layouts[i].alive {
            st.pipe_layouts[i] = rec
            return Cast[u64](i)
        }
    }
    id := Cast[u64](st.pipe_layouts.Len())
    try st.pipe_layouts.Push(a, rec)
    return id
}

/// Creates a Vulkan descriptor set layout and returns its backend ID.
export Device.CreateBindGroupLayout(mut self, entries: []BgLayoutEntry, label: str): Error!u64 {
    a := self.heap.Alloc()
    st := self.state.Mut()
    if st.lost { fail DeviceLost(reason: st.loss_reason) }

    var bindings: [16]vulkan.VkDescriptorSetLayoutBinding = [mem.Zeroed[vulkan.VkDescriptorSetLayoutBinding](); 16]
    cnt := entries.Len()
    for i in 0..cnt {
        e := &entries[i]
        bindings[i] = vulkan.VkDescriptorSetLayoutBinding(
            binding: Cast[u32](e.binding),
            descriptor_type: MapDescriptorType(e.kind, e.dynamic),
            descriptor_count: 1,
            stage_flags: MapStageFlags(e.vis_vs, e.vis_fs, e.vis_cs),
            p_immutable_samplers: raw.Null[vulkan.VkSampler](),
        )
    }

    unsafe "st.device is live" {
        dev := HandleFrom[vulkan.VkDeviceT](st.device)
        var ci := mem.Zeroed[vulkan.VkDescriptorSetLayoutCreateInfo]()
        ci.s_type = vulkan.VkStructureTypeDescriptorSetLayoutCreateInfo
        ci.binding_count = Cast[u32](cnt)
        ci.p_bindings = if cnt > 0 { raw.Data(bindings[..]) } else { raw.Null[vulkan.VkDescriptorSetLayoutBinding]() }
        var dsl: vulkan.VkDescriptorSetLayout = raw.Null[vulkan.VkDescriptorSetLayoutT]()
        r := st.dev_d.vk_create_descriptor_set_layout.Must()(dev, raw.Of(&ci), raw.Null[vulkan.VkAllocationCallbacks](), raw.OfMut(mut dsl))
        if r != vulkan.VkSuccess {
            if IsDeviceLostResult(r) { st.lost = true }
            fail MapVkError(r, CallCreateDevice)
        }
        st.live_native += 1

        if st.bg_layouts.Len() == 0 {
            d0 := VkBgLayoutRec(alive: false, vk_layout: 0, entries: HeapList[BgLayoutEntry].New(a), pools: HeapList[u64].New(a), free_sets: HeapList[u64].New(a))
            st.bg_layouts.Push(a, d0) else e {
                d0.Free(a)
                st.dev_d.vk_destroy_descriptor_set_layout.Must()(dev, dsl, raw.Null[vulkan.VkAllocationCallbacks]())
                if st.live_native > 0 { st.live_native -= 1 }
                fail e
            }
        }
        var found := 0
        for i in 1..st.bg_layouts.Len() {
            if !st.bg_layouts[i].alive {
                found = i
                break
            }
        }
        var id: u64 = 0
        if found > 0 {
            st.bg_layouts[found].entries.Clear()
            st.bg_layouts[found].pools.Clear()
            st.bg_layouts[found].free_sets.Clear()
            for si in 0..cnt {
                st.bg_layouts[found].entries.Push(a, entries[si]) else e {
                    st.bg_layouts[found].entries.Clear()
                    st.dev_d.vk_destroy_descriptor_set_layout.Must()(dev, dsl, raw.Null[vulkan.VkAllocationCallbacks]())
                    if st.live_native > 0 { st.live_native -= 1 }
                    fail e
                }
            }
            st.bg_layouts[found].vk_layout = HandleAddr(dsl)
            st.bg_layouts[found].alive = true
            id = Cast[u64](found)
        } else {
            var new_entries := HeapList[BgLayoutEntry].New(a)
            for si in 0..cnt {
                new_entries.Push(a, entries[si]) else e {
                    new_entries.Free(a)
                    st.dev_d.vk_destroy_descriptor_set_layout.Must()(dev, dsl, raw.Null[vulkan.VkAllocationCallbacks]())
                    if st.live_native > 0 { st.live_native -= 1 }
                    fail e
                }
            }
            id = Cast[u64](st.bg_layouts.Len())
            rec := VkBgLayoutRec(
                alive: true,
                vk_layout: HandleAddr(dsl),
                entries: new_entries,
                pools: HeapList[u64].New(a),
                free_sets: HeapList[u64].New(a),
            )
            st.bg_layouts.Push(a, rec) else e {
                rec.Free(a)
                st.dev_d.vk_destroy_descriptor_set_layout.Must()(dev, dsl, raw.Null[vulkan.VkAllocationCallbacks]())
                if st.live_native > 0 { st.live_native -= 1 }
                fail e
            }
        }
        SetVkObjectName(&*st, vulkan.VkObjectTypeDescriptorSetLayout, HandleAddr(dsl), label)
        return id
    }
}

DestroyBgLayoutRec(a: &Alloc, st: mut VkDeviceState, id: u64) {
    idx := Cast[int](id)
    if idx <= 0 || idx >= st.bg_layouts.Len() || !st.bg_layouts[idx].alive {
        return
    }
    st.bg_layouts[idx].alive = false
    unsafe "st.device and pools/layout are live" {
        dev := HandleFrom[vulkan.VkDeviceT](st.device)
        pools := &st.bg_layouts[idx].pools
        for pi in 0..pools.Len() {
            if (*pools)[pi] != 0 {
                st.dev_d.vk_destroy_descriptor_pool.Must()(dev, HandleFrom[vulkan.VkDescriptorPoolT]((*pools)[pi]), raw.Null[vulkan.VkAllocationCallbacks]())
            }
        }
        st.bg_layouts[idx].pools.Clear()
        st.bg_layouts[idx].free_sets.Clear()
        st.bg_layouts[idx].entries.Clear()
        if st.bg_layouts[idx].vk_layout != 0 {
            st.dev_d.vk_destroy_descriptor_set_layout.Must()(dev, HandleFrom[vulkan.VkDescriptorSetLayoutT](st.bg_layouts[idx].vk_layout), raw.Null[vulkan.VkAllocationCallbacks]())
            st.bg_layouts[idx].vk_layout = 0
            if st.live_native > 0 {
                st.live_native -= 1
            }
        }
    }
    _ = a
}

/// Destroys a Vulkan descriptor set layout and its descriptor pools.
export Device.DestroyBindGroupLayout(mut self, id: u64) {
    a := self.heap.Alloc()
    DestroyBgLayoutRec(a, self.state.Mut(), id)
}

/// Creates a Vulkan pipeline layout and returns its backend ID.
export Device.CreatePipelineLayout(mut self, bg_layout_ids: []u64, immediates: int, label: str): Error!u64 {
    a := self.heap.Alloc()
    st := self.state.Mut()
    if st.lost { fail DeviceLost(reason: st.loss_reason) }
    var dsls: [4]vulkan.VkDescriptorSetLayout = [raw.Null[vulkan.VkDescriptorSetLayoutT](); 4]
    cnt := bg_layout_ids.Len()
    for i in 0..cnt {
        lidx := Cast[int](bg_layout_ids[i])
        if lidx <= 0 || lidx >= st.bg_layouts.Len() || !st.bg_layouts[lidx].alive {
            fail Driver(code: -1, call: CallCreatePipeline)
        }
        dsls[i] = HandleFrom[vulkan.VkDescriptorSetLayoutT](st.bg_layouts[lidx].vk_layout)
    }
    var pcr := vulkan.VkPushConstantRange(
        stage_flags: Cast[u32](vulkan.VkShaderStageVertexBit) | Cast[u32](vulkan.VkShaderStageFragmentBit) | Cast[u32](vulkan.VkShaderStageComputeBit),
        offset: 0,
        size: Cast[u32](immediates),
    )
    unsafe "st.device is live" {
        dev := HandleFrom[vulkan.VkDeviceT](st.device)
        var plci := mem.Zeroed[vulkan.VkPipelineLayoutCreateInfo]()
        plci.s_type = vulkan.VkStructureTypePipelineLayoutCreateInfo
        plci.set_layout_count = Cast[u32](cnt)
        plci.p_set_layouts = if cnt > 0 { raw.Data(dsls[..]) } else { raw.Null[vulkan.VkDescriptorSetLayout]() }
        if immediates > 0 {
            plci.push_constant_range_count = 1
            plci.p_push_constant_ranges = raw.Of(&pcr)
        }
        var pl: vulkan.VkPipelineLayout = raw.Null[vulkan.VkPipelineLayoutT]()
        r := st.dev_d.vk_create_pipeline_layout.Must()(dev, raw.Of(&plci), raw.Null[vulkan.VkAllocationCallbacks](), raw.OfMut(mut pl))
        if r != vulkan.VkSuccess {
            if IsDeviceLostResult(r) { st.lost = true }
            fail MapVkError(r, CallCreatePipeline)
        }
        st.live_native += 1
        id := AllocPipeLayoutSlot(st, a, VkPipeLayoutRec(alive: true, vk_layout: HandleAddr(pl))) else e {
            st.dev_d.vk_destroy_pipeline_layout.Must()(dev, pl, raw.Null[vulkan.VkAllocationCallbacks]())
            if st.live_native > 0 { st.live_native -= 1 }
            fail e
        }
        SetVkObjectName(&*st, vulkan.VkObjectTypePipelineLayout, HandleAddr(pl), label)
        return id
    }
}

DestroyPipeLayoutRec(st: mut VkDeviceState, id: u64) {
    idx := Cast[int](id)
    if idx <= 0 || idx >= st.pipe_layouts.Len() || !st.pipe_layouts[idx].alive {
        return
    }
    rec := st.pipe_layouts[idx]
    st.pipe_layouts[idx].alive = false
    unsafe "st.device and rec.vk_layout are live" {
        st.dev_d.vk_destroy_pipeline_layout.Must()(HandleFrom[vulkan.VkDeviceT](st.device), HandleFrom[vulkan.VkPipelineLayoutT](rec.vk_layout), raw.Null[vulkan.VkAllocationCallbacks]())
        if st.live_native > 0 {
            st.live_native -= 1
        }
    }
}

/// Destroys a Vulkan pipeline layout.
export Device.DestroyPipelineLayout(mut self, id: u64) {
    DestroyPipeLayoutRec(self.state.Mut(), id)
}

AllocDescriptorSetFromLayout(a: &Alloc, st: mut VkDeviceState, lidx: int): Error!u64 {
    bgl := mut st.bg_layouts[lidx]
    if bgl.free_sets.Len() > 0 {
        return bgl.free_sets.Pop().Must()
    }
    var cnt_ubo: u32 = 0
    var cnt_dyn: u32 = 0
    var cnt_sto: u32 = 0
    var cnt_img: u32 = 0
    var cnt_smp: u32 = 0
    for i in 0..bgl.entries.Len() {
        e := &bgl.entries[i]
        match e.kind {
            0 { if e.dynamic { cnt_dyn += 64 } else { cnt_ubo += 64 } }
            1 { cnt_sto += 64 }
            2 { cnt_img += 64 }
            _ { cnt_smp += 64 }
        }
    }
    var psizes: [5]vulkan.VkDescriptorPoolSize = [mem.Zeroed[vulkan.VkDescriptorPoolSize](); 5]
    var pcnt: u32 = 0
    if cnt_ubo > 0 {
        psizes[int(pcnt)] = vulkan.VkDescriptorPoolSize(type_: vulkan.VkDescriptorTypeUniformBuffer, descriptor_count: cnt_ubo)
        pcnt += 1
    }
    if cnt_dyn > 0 {
        psizes[int(pcnt)] = vulkan.VkDescriptorPoolSize(type_: vulkan.VkDescriptorTypeUniformBufferDynamic, descriptor_count: cnt_dyn)
        pcnt += 1
    }
    if cnt_sto > 0 {
        psizes[int(pcnt)] = vulkan.VkDescriptorPoolSize(type_: vulkan.VkDescriptorTypeStorageBuffer, descriptor_count: cnt_sto)
        pcnt += 1
    }
    if cnt_img > 0 {
        psizes[int(pcnt)] = vulkan.VkDescriptorPoolSize(type_: vulkan.VkDescriptorTypeSampledImage, descriptor_count: cnt_img)
        pcnt += 1
    }
    if cnt_smp > 0 {
        psizes[int(pcnt)] = vulkan.VkDescriptorPoolSize(type_: vulkan.VkDescriptorTypeSampler, descriptor_count: cnt_smp)
        pcnt += 1
    }
    if pcnt == 0 {
        psizes[0] = vulkan.VkDescriptorPoolSize(type_: vulkan.VkDescriptorTypeUniformBuffer, descriptor_count: 64)
        pcnt = 1
    }

    unsafe "st.device is live" {
        dev := HandleFrom[vulkan.VkDeviceT](st.device)
        var dpci := mem.Zeroed[vulkan.VkDescriptorPoolCreateInfo]()
        dpci.s_type = vulkan.VkStructureTypeDescriptorPoolCreateInfo
        dpci.max_sets = 64
        dpci.pool_size_count = pcnt
        dpci.p_pool_sizes = raw.Data(psizes[..])
        var pool: vulkan.VkDescriptorPool = raw.Null[vulkan.VkDescriptorPoolT]()
        r := st.dev_d.vk_create_descriptor_pool.Must()(dev, raw.Of(&dpci), raw.Null[vulkan.VkAllocationCallbacks](), raw.OfMut(mut pool))
        if r != vulkan.VkSuccess {
            if IsDeviceLostResult(r) { st.lost = true }
            fail MapVkError(r, CallCreateDescriptorPool)
        }
        st.bg_layouts[lidx].pools.Push(a, HandleAddr(pool)) else e {
            st.dev_d.vk_destroy_descriptor_pool.Must()(dev, pool, raw.Null[vulkan.VkAllocationCallbacks]())
            fail e
        }

        var layouts: [64]vulkan.VkDescriptorSetLayout = [HandleFrom[vulkan.VkDescriptorSetLayoutT](st.bg_layouts[lidx].vk_layout); 64]
        var sets: [64]vulkan.VkDescriptorSet = [raw.Null[vulkan.VkDescriptorSetT](); 64]
        var ai := mem.Zeroed[vulkan.VkDescriptorSetAllocateInfo]()
        ai.s_type = vulkan.VkStructureTypeDescriptorSetAllocateInfo
        ai.descriptor_pool = pool
        ai.descriptor_set_count = 64
        ai.p_set_layouts = raw.Data(layouts[..])
        ar := st.dev_d.vk_allocate_descriptor_sets.Must()(dev, raw.Of(&ai), raw.DataMut(mut sets[..]))
        if ar != vulkan.VkSuccess {
            if IsDeviceLostResult(ar) { st.lost = true }
            fail MapVkError(ar, CallCreateDescriptorPool)
        }
        // Push sets 1..63 to free_sets in reverse so set 0 is returned first.
        var si := 63
        for si >= 1 {
            try st.bg_layouts[lidx].free_sets.Push(a, HandleAddr(sets[si]))
            si -= 1
        }
        return HandleAddr(sets[0])
    }
}

/// Allocates and populates a VkDescriptorSet from layout_id's pools.
export Device.CreateBindGroup(mut self, layout_id: u64, entries: []BgBindEntry, label: str): Error!u64 {
    a := self.heap.Alloc()
    st := self.state.Mut()
    if st.lost { fail DeviceLost(reason: st.loss_reason) }
    lidx := Cast[int](layout_id)
    if lidx <= 0 || lidx >= st.bg_layouts.Len() || !st.bg_layouts[lidx].alive {
        fail Driver(code: -1, call: CallCreateDescriptorPool)
    }

    set_h := try AllocDescriptorSetFromLayout(a, st, lidx)
    var tv_buf: [16]u64 = [0; 16]
    var tv_cnt := 0

    var writes: [16]vulkan.VkWriteDescriptorSet = [mem.Zeroed[vulkan.VkWriteDescriptorSet](); 16]
    var buf_infos: [16]vulkan.VkDescriptorBufferInfo = [mem.Zeroed[vulkan.VkDescriptorBufferInfo](); 16]
    var img_infos: [16]vulkan.VkDescriptorImageInfo = [mem.Zeroed[vulkan.VkDescriptorImageInfo](); 16]
    var wcnt := 0

    bgl_entries := &st.bg_layouts[lidx].entries
    for ei in 0..entries.Len() {
        be := &entries[ei]
        var dyn := false
        for li in 0..bgl_entries.Len() {
            if (*bgl_entries)[li].binding == be.binding {
                dyn = (*bgl_entries)[li].dynamic
                break
            }
        }
        dtype := MapDescriptorType(be.kind, dyn)
        unsafe "descriptor handles and info arrays live across vkUpdateDescriptorSets" {
            writes[wcnt].s_type = vulkan.VkStructureTypeWriteDescriptorSet
            writes[wcnt].dst_set = HandleFrom[vulkan.VkDescriptorSetT](set_h)
            writes[wcnt].dst_binding = Cast[u32](be.binding)
            writes[wcnt].descriptor_count = 1
            writes[wcnt].descriptor_type = dtype
            match be.kind {
                0 | 1 {
                    brec := &st.buffers[Cast[int](be.buf_id)]
                    buf_infos[wcnt] = vulkan.VkDescriptorBufferInfo(
                        buffer: HandleFrom[vulkan.VkBufferT](brec.vk_buffer),
                        offset: Cast[u64](be.offset),
                        range: Cast[u64](be.size),
                    )
                    writes[wcnt].p_buffer_info = raw.Of(&buf_infos[wcnt])
                }
                2 {
                    vrec := &st.views[Cast[int](be.view_id)]
                    if tv_cnt < 16 {
                        tv_buf[tv_cnt] = be.view_id
                        tv_cnt += 1
                    }
                    lay := if vrec.is_depth { vulkan.VkImageLayoutDepthStencilReadOnlyOptimal } else { vulkan.VkImageLayoutShaderReadOnlyOptimal }
                    img_infos[wcnt] = vulkan.VkDescriptorImageInfo(
                        sampler: raw.Null[vulkan.VkSamplerT](),
                        image_view: HandleFrom[vulkan.VkImageViewT](vrec.vk_view),
                        image_layout: lay,
                    )
                    writes[wcnt].p_image_info = raw.Of(&img_infos[wcnt])
                }
                _ {
                    srec := &st.samplers[Cast[int](be.sampler_id)]
                    img_infos[wcnt] = vulkan.VkDescriptorImageInfo(
                        sampler: HandleFrom[vulkan.VkSamplerT](srec.vk_sampler),
                        image_view: raw.Null[vulkan.VkImageViewT](),
                        image_layout: vulkan.VkImageLayoutUndefined,
                    )
                    writes[wcnt].p_image_info = raw.Of(&img_infos[wcnt])
                }
            }
        }
        wcnt += 1
    }

    if wcnt > 0 {
        unsafe "st.device and writes/infos are live" {
            st.dev_d.vk_update_descriptor_sets.Must()(HandleFrom[vulkan.VkDeviceT](st.device), Cast[u32](wcnt), raw.Data(writes[..]), 0, raw.Null[vulkan.VkCopyDescriptorSet]())
        }
    }

    st.live_native += 1
    if st.bind_groups.Len() == 0 {
        b0 := VkBgRec(alive: false, vk_set: 0, layout_id: 0, tex_views: HeapList[u64].New(a))
        st.bind_groups.Push(a, b0) else e {
            b0.Free(a)
            st.bg_layouts[lidx].free_sets.Push(a, set_h) else {}
            if st.live_native > 0 { st.live_native -= 1 }
            fail e
        }
    }
    var found := 0
    for i in 1..st.bind_groups.Len() {
        if !st.bind_groups[i].alive {
            found = i
            break
        }
    }
    var id: u64 = 0
    if found > 0 {
        st.bind_groups[found].tex_views.Clear()
        for ti in 0..tv_cnt {
            st.bind_groups[found].tex_views.Push(a, tv_buf[ti]) else e {
                st.bind_groups[found].tex_views.Clear()
                st.bg_layouts[lidx].free_sets.Push(a, set_h) else {}
                if st.live_native > 0 { st.live_native -= 1 }
                fail e
            }
        }
        st.bind_groups[found].vk_set = set_h
        st.bind_groups[found].layout_id = layout_id
        st.bind_groups[found].alive = true
        id = Cast[u64](found)
    } else {
        var tex_views := HeapList[u64].New(a)
        for ti in 0..tv_cnt {
            tex_views.Push(a, tv_buf[ti]) else e {
                tex_views.Free(a)
                st.bg_layouts[lidx].free_sets.Push(a, set_h) else {}
                if st.live_native > 0 { st.live_native -= 1 }
                fail e
            }
        }
        id = Cast[u64](st.bind_groups.Len())
        rec := VkBgRec(alive: true, vk_set: set_h, layout_id: layout_id, tex_views: tex_views)
        st.bind_groups.Push(a, rec) else e {
            rec.Free(a)
            st.bg_layouts[lidx].free_sets.Push(a, set_h) else {}
            if st.live_native > 0 { st.live_native -= 1 }
            fail e
        }
    }
    SetVkObjectName(&*st, vulkan.VkObjectTypeDescriptorSet, set_h, label)
    return id
}

DestroyBindGroupRec(a: &Alloc, st: mut VkDeviceState, id: u64) {
    idx := Cast[int](id)
    if idx <= 0 || idx >= st.bind_groups.Len() || !st.bind_groups[idx].alive {
        return
    }
    lidx := Cast[int](st.bind_groups[idx].layout_id)
    set_h := st.bind_groups[idx].vk_set
    st.bind_groups[idx].alive = false
    st.bind_groups[idx].tex_views.Clear()
    if lidx > 0 && lidx < st.bg_layouts.Len() && st.bg_layouts[lidx].alive && set_h != 0 {
        st.bg_layouts[lidx].free_sets.Push(a, set_h) else {}
    }
    if st.live_native > 0 {
        st.live_native -= 1
    }
}

/// Returns a Vulkan descriptor set to its layout's free list.
export Device.DestroyBindGroup(mut self, id: u64) {
    a := self.heap.Alloc()
    DestroyBindGroupRec(a, self.state.Mut(), id)
}
