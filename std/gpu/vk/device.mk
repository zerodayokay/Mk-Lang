// Why: opening a Vulkan 1.3 device selects a matching physical device,
// validates requested features, creates the VkDevice + queue + zero buffer +
// waiter thread, and closes every native object cleanly on shutdown.
//
// What: Open, Device.Close, Device.WaitIdle, Device.DrainDebugMessages,
// Device.PollCompleted, Device.SetFaultNextSubmit, Device.SetPlantedBug, and
// SetVkObjectName helper.
//
// How: runs physical device selection and vkCreateDevice through
// runtime.Blocking, stores state on the caller's Heap, and tears down in
// strict reverse dependency order.

import c.vulkan
import std.core
import std.mem
import std.raw
import std.runtime

const VkZeroBufferBytes: u64 = 262144 // 256 KiB host-zeroed copy source

SelectAdapter(inst_d: &InstanceDispatch, inst: vulkan.VkInstance, opts: &OpenOptions): Error!PdProbe {
    unsafe "inst and inst_d are live" {
        enum_pd := inst_d.vk_enumerate_physical_devices.Must()
        var cnt: u32 = 0
        r := enum_pd(inst, raw.OfMut(mut cnt), raw.Null[vulkan.VkPhysicalDevice]())
        if r != vulkan.VkSuccess || cnt == 0 {
            fail NoAdapter
        }
        n := if int(cnt) > 32 { 32 } else { int(cnt) }
        var pds: [32]vulkan.VkPhysicalDevice = [raw.Null[vulkan.VkPhysicalDeviceT](); 32]
        var got: u32 = Trunc[u32](n)
        _ = enum_pd(inst, raw.OfMut(mut got), raw.DataMut(mut pds[..]))

        var best: ?PdProbe = none
        var best_score := -1
        var matched_luid := false
        for i in 0..int(got) {
            match ProbePhysicalDevice(inst_d, pds[i], i) {
                none {}
                some(pr) {
                    if opts.has_luid {
                        if pr.desc.luid != opts.luid {
                            continue
                        }
                        matched_luid = true
                    }
                    if opts.software && pr.desc.kind != KindSoftware {
                        continue
                    }
                    sc := AdapterKindScore(pr.desc.kind, opts.power)
                    if sc > best_score {
                        best = some(pr)
                        best_score = sc
                    }
                }
            }
        }
        if opts.has_luid && !matched_luid {
            fail NoAdapter
        }
        match best {
            some(pr) { return pr }
            none { fail NoAdapter }
        }
    }
}

CheckRequestedFeatures(pr: &PdProbe, req: &VkFeatures): Error!void {
    if req.texture_compression_bc && !pr.desc.features.texture_compression_bc {
        fail Missing(feature: FeatTextureCompressionBc)
    }
    if req.depth_clip_control && !pr.desc.features.depth_clip_control {
        fail Missing(feature: FeatDepthClipControl)
    }
    if req.float32_filterable && !pr.desc.features.float32_filterable {
        fail Missing(feature: FeatFloat32Filterable)
    }
    if req.sampler_anisotropy && !pr.desc.features.sampler_anisotropy {
        fail Missing(feature: FeatSamplerAnisotropy)
    }
}

export struct VkOpenResult {
    export dev: Device
    export adapter: AdapterDesc
}

struct VkOpenJobIn {
    heap: &Heap
    gipa: vulkan.PfnVkGetInstanceProcAddr
    opts: OpenOptions
    out_ic: ?VkInstCreated
    out_pr: ?PdProbe
    out_vkdev: u64
    out_dev_d: ?DeviceDispatch
    out_queue: u64
    out_mprops: vulkan.VkPhysicalDeviceMemoryProperties
    out_err: ?Error
}

struct VkRawCreated {
    vkdev: u64
    dev_d: DeviceDispatch
    queue: u64
    mprops: vulkan.VkPhysicalDeviceMemoryProperties
}

CreateVkDeviceRaw(ic: &VkInstCreated, pr: &PdProbe, opts: &OpenOptions): Error!VkRawCreated {
    unsafe "ic.inst and pr.pd are live Vulkan handles" {
        inst := HandleFrom[vulkan.VkInstanceT](ic.inst)
        pd := HandleFrom[vulkan.VkPhysicalDeviceT](pr.pd)

        var qprio: f32 = 1.0
        var qci := mem.Zeroed[vulkan.VkDeviceQueueCreateInfo]()
        qci.s_type = vulkan.VkStructureTypeDeviceQueueCreateInfo
        qci.queue_family_index = pr.queue_family
        qci.queue_count = 1
        qci.p_queue_priorities = raw.Of(&qprio)

        var ext_ptrs: [2]*u8 = [raw.Null[u8](); 2]
        var ext_cnt: u32 = 0
        if pr.has_swapchain {
            ext_ptrs[int(ext_cnt)] = raw.StrData("VK_KHR_swapchain\x00")
            ext_cnt += 1
        }
        if opts.features.depth_clip_control {
            ext_ptrs[int(ext_cnt)] = raw.StrData("VK_EXT_depth_clip_control\x00")
            ext_cnt += 1
        }

        var dc_feat := mem.Zeroed[vulkan.VkPhysicalDeviceDepthClipControlFeaturesExt]()
        dc_feat.s_type = vulkan.VkStructureTypePhysicalDeviceDepthClipControlFeaturesExt
        dc_feat.depth_clip_control = if opts.features.depth_clip_control { vulkan.VkTrue } else { vulkan.VkFalse }

        var f11 := mem.Zeroed[vulkan.VkPhysicalDeviceVulkan11Features]()
        f11.s_type = vulkan.VkStructureTypePhysicalDeviceVulkan11Features
        f11.shader_draw_parameters = if pr.desc.draw_params_ok { vulkan.VkTrue } else { vulkan.VkFalse }
        if opts.features.depth_clip_control {
            f11.p_next = raw.PtrCast[vulkan.VkPhysicalDeviceDepthClipControlFeaturesExt, void](raw.Of(&dc_feat))
        }

        var f12 := mem.Zeroed[vulkan.VkPhysicalDeviceVulkan12Features]()
        f12.s_type = vulkan.VkStructureTypePhysicalDeviceVulkan12Features
        f12.timeline_semaphore = vulkan.VkTrue
        f12.p_next = raw.PtrCast[vulkan.VkPhysicalDeviceVulkan11Features, void](raw.Of(&f11))

        var f13 := mem.Zeroed[vulkan.VkPhysicalDeviceVulkan13Features]()
        f13.s_type = vulkan.VkStructureTypePhysicalDeviceVulkan13Features
        f13.dynamic_rendering = vulkan.VkTrue
        f13.synchronization2 = vulkan.VkTrue
        f13.p_next = raw.PtrCast[vulkan.VkPhysicalDeviceVulkan12Features, void](raw.Of(&f12))

        var feats2 := mem.Zeroed[vulkan.VkPhysicalDeviceFeatures2]()
        feats2.s_type = vulkan.VkStructureTypePhysicalDeviceFeatures2
        feats2.p_next = raw.PtrCast[vulkan.VkPhysicalDeviceVulkan13Features, void](raw.Of(&f13))
        feats2.features.independent_blend = vulkan.VkTrue
        feats2.features.image_cube_array = vulkan.VkTrue
        feats2.features.depth_bias_clamp = if pr.desc.depth_bias_clamp_ok { vulkan.VkTrue } else { vulkan.VkFalse }
        feats2.features.texture_compression_bc = if opts.features.texture_compression_bc { vulkan.VkTrue } else { vulkan.VkFalse }
        feats2.features.sampler_anisotropy = if opts.features.sampler_anisotropy { vulkan.VkTrue } else { vulkan.VkFalse }

        var dci := mem.Zeroed[vulkan.VkDeviceCreateInfo]()
        dci.s_type = vulkan.VkStructureTypeDeviceCreateInfo
        dci.p_next = raw.PtrCast[vulkan.VkPhysicalDeviceFeatures2, void](raw.Of(&feats2))
        dci.queue_create_info_count = 1
        dci.p_queue_create_infos = raw.Of(&qci)
        dci.enabled_extension_count = ext_cnt
        dci.pp_enabled_extension_names = if ext_cnt > 0 { raw.Data(ext_ptrs[..]) } else { raw.Null[*u8]() }

        var vkdev: vulkan.VkDevice = raw.Null[vulkan.VkDeviceT]()
        r := ic.inst_d.vk_create_device.Must()(pd, raw.Of(&dci), raw.Null[vulkan.VkAllocationCallbacks](), raw.OfMut(mut vkdev))
        if r != vulkan.VkSuccess {
            fail MapVkError(r, CallCreateDevice)
        }
        dev_d := LoadDeviceDispatch(ic.gipa, inst, vkdev, pr.has_swapchain, ic.has_debug) else {
            p_dest := raw.FnFromAddr[vulkan.PfnVkDestroyDevice](LoadInstanceProcAddr(ic.gipa, inst, "vkDestroyDevice\x00"))
            match p_dest {
                some(df) { df(vkdev, raw.Null[vulkan.VkAllocationCallbacks]()) }
                none {}
            }
            fail Unsupported
        }
        var queue: vulkan.VkQueue = raw.Null[vulkan.VkQueueT]()
        dev_d.vk_get_device_queue.Must()(vkdev, pr.queue_family, 0, raw.OfMut(mut queue))

        var mprops := mem.Zeroed[vulkan.VkPhysicalDeviceMemoryProperties]()
        ic.inst_d.vk_get_physical_device_memory_properties.Must()(pd, raw.OfMut(mut mprops))

        return VkRawCreated(vkdev: HandleAddr(vkdev), dev_d: dev_d, queue: HandleAddr(queue), mprops: mprops)
    }
}

CreateDeviceStateFromRaw(heap: &Heap, ic: VkInstCreated, pr: PdProbe, rc: VkRawCreated, opts: &OpenOptions): Error!VkOpenResult from heap {
    a := heap.Alloc()
    unsafe "handles in VkRawCreated are live" {
        vkdev := HandleFrom[vulkan.VkDeviceT](rc.vkdev)
        dev_d := rc.dev_d
        var vst := VkDeviceState(
            instance: ic.inst,
            physical_device: pr.pd,
            device: rc.vkdev,
            queue: rc.queue,
            queue_family: pr.queue_family,
            gipa: ic.gipa,
            inst_d: ic.inst_d,
            dev_d: dev_d,
            has_surface: ic.has_surface,
            has_swapchain: pr.has_swapchain,
            has_debug: ic.has_debug,
            debug_messenger: ic.messenger,
            debug_ring_ptr: ic.ring_ptr,
            d24s8_vk_format: pr.d24s8_fmt,
            frames_in_flight: opts.frames_in_flight,
            mem_props: rc.mprops,
            mem_blocks: HeapList[VkMemBlock].New(a),
            tlsf_allocs: HeapList[VkTlsf].New(a),
            timeline_sem: 0,
            queued_serial: 0,
            waiter_ptr: 0,
            cmd_slots: HeapList[VkCmdSlot].New(a),
            buffers: HeapList[VkBufRec].New(a),
            textures: HeapList[VkTexRec].New(a),
            views: HeapList[VkViewRec].New(a),
            samplers: HeapList[VkSamplerRec].New(a),
            bg_layouts: HeapList[VkBgLayoutRec].New(a),
            pipe_layouts: HeapList[VkPipeLayoutRec].New(a),
            bind_groups: HeapList[VkBgRec].New(a),
            shaders: HeapList[VkShaderRec].New(a),
            pipelines: HeapList[VkPipeRec].New(a),
            swapchains: HeapList[VkSwapchainRec].New(a),
            pending_zero_bufs: HeapList[u64].New(a),
            pending_zero_texs: HeapList[u64].New(a),
            pending_copies: HeapList[VkPreCopyOp].New(a),
            upload_chunks: HeapList[VkUploadBuf].New(a),
            staging_bufs: HeapList[VkStagingBuf].New(a),
            zero_buf: 0,
            zero_buf_alloc: mem.Zeroed[VkMemAlloc](),
            zero_buf_size: VkZeroBufferBytes,
            tracked_layouts: HeapList[i32].New(a),
            live_native: 0,
            lost: false,
            loss_reason: LossUnknown,
            fault_next_submit: raw.Atomic[u32].New(0),
            planted_bug: BugNone,
        )

        var box := Box[VkDeviceState].New(a, vst) else e {
            dev_d.vk_destroy_device.Must()(vkdev, raw.Null[vulkan.VkAllocationCallbacks]())
            FreeEmptyDeviceLists(a, vst)
            fail e
        }

        StartWaiterThread(a, box.Mut()) else e {
            var vst2 := box.Take(a)
            dev_d.vk_destroy_device.Must()(vkdev, raw.Null[vulkan.VkAllocationCallbacks]())
            FreeEmptyDeviceLists(a, vst2)
            fail e
        }

        InitZeroBuffer(a, box.Mut()) else e {
            var vst3 := box.Take(a)
            StopWaiterThread(a, mut vst3)
            FreeAllDeviceMemBlocks(a, mut vst3)
            dev_d.vk_destroy_device.Must()(vkdev, raw.Null[vulkan.VkAllocationCallbacks]())
            FreeEmptyDeviceLists(a, vst3)
            fail e
        }

        // Baseline internal allocations (zero_buf + its mem block) are internal, not user leaks.
        box.Mut().live_native = 0

        var nm: str = ""
        if pr.name_len > 0 {
            p := core.AllocBytes(a, pr.name_len, 1)
            if !raw.IsNull(p) {
                for ni in 0..pr.name_len {
                    raw.Write(raw.Offset(p, ni), pr.name_bytes[ni])
                }
                nm = raw.Str(p, pr.name_len)
            }
        }
        ad := AdapterDesc(
            luid: pr.desc.luid,
            name: nm,
            vendor: pr.desc.vendor,
            device_id: pr.desc.device_id,
            kind: pr.desc.kind,
            limits: pr.desc.limits,
            features: pr.desc.features,
            caps: pr.desc.caps,
            depth_bias_clamp_ok: pr.desc.depth_bias_clamp_ok,
            draw_params_ok: pr.desc.draw_params_ok,
            standard_sample_locations: pr.desc.standard_sample_locations,
        )
        return VkOpenResult(dev: Device(heap: heap, state: box), adapter: ad)
    }
}

InitZeroBuffer(a: &Alloc into st, st: mut VkDeviceState): Error!void {
    unsafe "st.device is live" {
        dev := HandleFrom[vulkan.VkDeviceT](st.device)
        var bci := mem.Zeroed[vulkan.VkBufferCreateInfo]()
        bci.s_type = vulkan.VkStructureTypeBufferCreateInfo
        bci.size = VkZeroBufferBytes
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
        br := st.dev_d.vk_bind_buffer_memory.Must()(dev, buf, HandleFrom[vulkan.VkDeviceMemoryT](if al.dedicated_mem != 0 { al.dedicated_mem } else { st.mem_blocks[al.block_idx].memory }), al.offset)
        if br != vulkan.VkSuccess {
            FreeDeviceMem(a, st, al)
            st.dev_d.vk_destroy_buffer.Must()(dev, buf, raw.Null[vulkan.VkAllocationCallbacks]())
            fail MapVkError(br, CallCreateBuffer)
        }
        if al.mapped_ptr != 0 {
            raw.Fill(raw.FromAddr[u8](al.mapped_ptr), 0, int(VkZeroBufferBytes))
            FlushOrInvalidateHostMem(&*st, &al, false)
        }
        st.zero_buf = HandleAddr(buf)
        st.zero_buf_alloc = al
    }
}

FreeEmptyDeviceLists(a: &Alloc, st: VkDeviceState) {
    VkDeviceState(
        instance: _, physical_device: _, device: _, queue: _, queue_family: _, gipa: _,
        inst_d: _, dev_d: _, has_surface: _, has_swapchain: _, has_debug: _,
        debug_messenger: _, debug_ring_ptr: _, d24s8_vk_format: _, frames_in_flight: _,
        mem_props: _, mem_blocks, tlsf_allocs, timeline_sem: _, queued_serial: _,
        waiter_ptr: _, cmd_slots, buffers, textures, views, samplers,
        bg_layouts, pipe_layouts, bind_groups, shaders, pipelines, swapchains,
        pending_zero_bufs, pending_zero_texs, pending_copies, upload_chunks, staging_bufs,
        zero_buf: _, zero_buf_alloc: _, zero_buf_size: _, tracked_layouts,
        live_native: _, lost: _, loss_reason: _, fault_next_submit: _, planted_bug: _,
    ) := st
    var tlsf := tlsf_allocs
    for {
        match tlsf.Pop() {
            some(trec) { trec.Free(a) }
            none { break }
        }
    }
    tlsf.FreeEmpty(a)
    mem_blocks.Free(a)
    cmd_slots.Free(a)
    buffers.Free(a)
    textures.Free(a)
    views.Free(a)
    samplers.Free(a)
    var bgls := bg_layouts
    for {
        match bgls.Pop() {
            some(bgl) { bgl.Free(a) }
            none { break }
        }
    }
    bgls.FreeEmpty(a)
    pipe_layouts.Free(a)
    var bgs := bind_groups
    for {
        match bgs.Pop() {
            some(bg) { bg.Free(a) }
            none { break }
        }
    }
    bgs.FreeEmpty(a)
    shaders.Free(a)
    pipelines.Free(a)
    var scs := swapchains
    for {
        match scs.Pop() {
            some(sc) { sc.Free(a) }
            none { break }
        }
    }
    scs.FreeEmpty(a)
    pending_zero_bufs.Free(a)
    pending_zero_texs.Free(a)
    pending_copies.Free(a)
    upload_chunks.Free(a)
    staging_bufs.Free(a)
    tracked_layouts.Free(a)
}

OpenVkBlocking(job0: VkOpenJobIn): VkOpenJobIn {
    a := job0.heap.Alloc()
    ic := CreateVkInstanceRaw(job0.gipa, some(a), job0.opts.debug_mode) else e {
        return VkOpenJobIn(heap: job0.heap, gipa: job0.gipa, opts: job0.opts, out_ic: none, out_pr: none, out_vkdev: 0, out_dev_d: none, out_queue: 0, out_mprops: job0.out_mprops, out_err: some(e))
    }
    pr := SelectAdapter(&ic.inst_d, HandleFrom[vulkan.VkInstanceT](ic.inst), &job0.opts) else e {
        DestroyVkInstance(a, ic.inst, &ic.inst_d, ic.messenger, ic.ring_ptr)
        return VkOpenJobIn(heap: job0.heap, gipa: job0.gipa, opts: job0.opts, out_ic: none, out_pr: none, out_vkdev: 0, out_dev_d: none, out_queue: 0, out_mprops: job0.out_mprops, out_err: some(e))
    }
    CheckRequestedFeatures(&pr, &job0.opts.features) else e {
        DestroyVkInstance(a, ic.inst, &ic.inst_d, ic.messenger, ic.ring_ptr)
        return VkOpenJobIn(heap: job0.heap, gipa: job0.gipa, opts: job0.opts, out_ic: none, out_pr: none, out_vkdev: 0, out_dev_d: none, out_queue: 0, out_mprops: job0.out_mprops, out_err: some(e))
    }
    rc := CreateVkDeviceRaw(&ic, &pr, &job0.opts) else e {
        DestroyVkInstance(a, ic.inst, &ic.inst_d, ic.messenger, ic.ring_ptr)
        return VkOpenJobIn(heap: job0.heap, gipa: job0.gipa, opts: job0.opts, out_ic: none, out_pr: none, out_vkdev: 0, out_dev_d: none, out_queue: 0, out_mprops: job0.out_mprops, out_err: some(e))
    }
    return VkOpenJobIn(
        heap: job0.heap,
        gipa: job0.gipa,
        opts: job0.opts,
        out_ic: some(ic),
        out_pr: some(pr),
        out_vkdev: rc.vkdev,
        out_dev_d: some(rc.dev_d),
        out_queue: rc.queue,
        out_mprops: rc.mprops,
        out_err: none,
    )
}

/// Opens a Vulkan 1.3 device according to opts on the blocking pool.
export Open(heap: &Heap, io: Io, opts: OpenOptions): Error!VkOpenResult from heap {
    gipa := LoadVkGetInstanceProcAddr(io)
    if gipa.IsNone() {
        fail Unsupported
    }
    job0 := VkOpenJobIn(
        heap: heap,
        gipa: gipa,
        opts: opts,
        out_ic: none,
        out_pr: none,
        out_vkdev: 0,
        out_dev_d: none,
        out_queue: 0,
        out_mprops: mem.Zeroed[vulkan.VkPhysicalDeviceMemoryProperties](),
        out_err: none,
    )
    job := try runtime.Blocking(io, job0, OpenVkBlocking)
    match job.out_err {
        some(e) { fail e }
        none {}
    }
    ic := job.out_ic.Must()
    pr := job.out_pr.Must()
    rc := VkRawCreated(vkdev: job.out_vkdev, dev_d: job.out_dev_d.Must(), queue: job.out_queue, mprops: job.out_mprops)
    res := CreateDeviceStateFromRaw(heap, ic, pr, rc, &opts) else e {
        a := heap.Alloc()
        unsafe "clean up raw device on CreateDeviceStateFromRaw failure" {
            rc.dev_d.vk_destroy_device.Must()(HandleFrom[vulkan.VkDeviceT](rc.vkdev), raw.Null[vulkan.VkAllocationCallbacks]())
        }
        DestroyVkInstance(a, ic.inst, &ic.inst_d, ic.messenger, ic.ring_ptr)
        fail e
    }
    return res
}

/// Frees a heap-copied adapter name returned by Open.
export FreeOpenAdapterName(heap: &Heap, name: str) {
    nb := name.AsBytes()
    if nb.Len() > 0 {
        unsafe "nb was allocated on heap.Alloc() in OpenVkBlocking" {
            core.FreeBytes(heap.Alloc(), raw.Data(nb), nb.Len(), 1)
        }
    }
}

SetVkObjectName(st: &VkDeviceState, obj_type: vulkan.VkObjectType, handle: u64, label: str) {
    if !st.has_debug || handle == 0 || label.Len() == 0 {
        return
    }
    match st.dev_d.vk_set_debug_utils_object_name_ext {
        none { return }
        some(set_fn) {
            bs := label.AsBytes()
            n := if bs.Len() > 255 { 255 } else { bs.Len() }
            var buf: [256]u8 = [0; 256]
            for i in 0..n {
                buf[i] = bs[i]
            }
            unsafe "st.device is live and buf is NUL-terminated" {
                var ni := mem.Zeroed[vulkan.VkDebugUtilsObjectNameInfoExt]()
                ni.s_type = vulkan.VkStructureTypeDebugUtilsObjectNameInfoExt
                ni.object_type = obj_type
                ni.object_handle = handle
                ni.p_object_name = raw.Data(buf[..])
                _ = set_fn(HandleFrom[vulkan.VkDeviceT](st.device), raw.Of(&ni))
            }
        }
    }
}

/// Waits on the device waiter thread until serial completes.
export Device.WaitSerial(mut self, io: Io, serial: u64): Error!void {
    try WaitForSerial(self.state.Mut(), io, serial)
}

/// Waits for at most the most recent queued signal and returns the serial waited on.
/// Errors as WaitSerial. Suspends on the waiter thread; allocates nothing.
export Device.WaitQueuedSerial(mut self, io: Io, requested: u64): Error!u64 {
    st := self.state.Mut()
    target := if requested < st.queued_serial { requested } else { st.queued_serial }
    try WaitForSerial(st, io, target)
    return target
}

/// Polls the hardware timeline semaphore counter.
export Device.PollCompleted(mut self): u64 {
    return PollCompletedSerial(self.state.Mut())
}

/// Drains captured debug messenger messages into out.
export Device.DrainDebugMessages(mut self, a: &Arena, out: mut List[DebugMessage], err_out: mut int, warn_out: mut int): OutOfMemory!void {
    st := self.state.Ref()
    try DrainDebugRing(a, st.debug_ring_ptr, out, err_out, warn_out)
}

/// Returns total error and warning counts recorded by the Vulkan debug messenger.
export Device.DrainDebugCounts(&self): (errs: int, warns: int) {
    st := self.state.Ref()
    if st.debug_ring_ptr == 0 {
        return (errs: 0, warns: 0)
    }
    unsafe "debug_ring_ptr is live for Device lifetime" {
        ring := raw.FromAddr[VkDebugRing](st.debug_ring_ptr)
        return (errs: int((*ring).err_count.Load()), warns: int((*ring).warn_count.Load()))
    }
}

/// Injects VK_ERROR_DEVICE_LOST on the next Submit (Test profile hook).
export Device.SetFaultNextSubmit(mut self) {
    self.state.Ref().fault_next_submit.Store(1)
}

/// Sets the planted-bug hook for Test-profile validation.
export Device.SetPlantedBug(mut self, bug: int) {
    self.state.Mut().planted_bug = bug
}

/// Reports whether pending pre-submit zeroing or writes await submission.
export Device.HasPendingPreSubmit(&self): bool {
    st := self.state.Ref()
    return st.pending_zero_bufs.Len() > 0 || st.pending_zero_texs.Len() > 0 || st.pending_copies.Len() > 0
}

/// Releases all native Vulkan objects, joins the waiter thread, and returns
/// the count of user-created native objects that had not been closed earlier.
export Device.Close(self): int {
    Device(heap, state) := self
    a := heap.Alloc()
    var st := state.Take(a)
    unsafe "st.device and st.instance are live until destroyed below" {
        dev := HandleFrom[vulkan.VkDeviceT](st.device)
        _ = st.dev_d.vk_device_wait_idle.Must()(dev)
        StopWaiterThread(a, mut st)

        // Destroy any unclosed user objects first and track native leaks before internal teardown.
        for i in 1..st.swapchains.Len() {
            if st.swapchains[i].alive {
                DestroySwapchainRec(a, mut st, Cast[u64](i))
            }
        }
        for i in 1..st.pipelines.Len() {
            if st.pipelines[i].alive {
                DestroyPipelineRec(mut st, Cast[u64](i))
            }
        }
        for i in 1..st.shaders.Len() {
            if st.shaders[i].alive {
                DestroyShaderRec(mut st, Cast[u64](i))
            }
        }
        for i in 1..st.bind_groups.Len() {
            if st.bind_groups[i].alive {
                DestroyBindGroupRec(a, mut st, Cast[u64](i))
            }
        }
        for i in 1..st.pipe_layouts.Len() {
            if st.pipe_layouts[i].alive {
                DestroyPipeLayoutRec(mut st, Cast[u64](i))
            }
        }
        for i in 1..st.bg_layouts.Len() {
            if st.bg_layouts[i].alive {
                DestroyBgLayoutRec(a, mut st, Cast[u64](i))
            }
        }
        for i in 1..st.views.Len() {
            if st.views[i].alive {
                DestroyViewRec(mut st, Cast[u64](i))
            }
        }
        for i in 1..st.samplers.Len() {
            if st.samplers[i].alive {
                DestroySamplerRec(mut st, Cast[u64](i))
            }
        }
        for i in 1..st.textures.Len() {
            if st.textures[i].alive {
                DestroyTextureRec(a, mut st, Cast[u64](i))
            }
        }
        for i in 1..st.buffers.Len() {
            if st.buffers[i].alive {
                DestroyBufferRec(a, mut st, Cast[u64](i))
            }
        }

        // Destroy internal upload chunks, staging buffers, zero buffer, and command pools.
        for i in 0..st.upload_chunks.Len() {
            uc := st.upload_chunks[i]
            if uc.vk_buffer != 0 {
                st.dev_d.vk_destroy_buffer.Must()(dev, HandleFrom[vulkan.VkBufferT](uc.vk_buffer), raw.Null[vulkan.VkAllocationCallbacks]())
                FreeDeviceMem(a, mut st, uc.alloc)
            }
        }
        for i in 0..st.staging_bufs.Len() {
            sb := st.staging_bufs[i]
            if sb.vk_buffer != 0 {
                st.dev_d.vk_destroy_buffer.Must()(dev, HandleFrom[vulkan.VkBufferT](sb.vk_buffer), raw.Null[vulkan.VkAllocationCallbacks]())
                FreeDeviceMem(a, mut st, sb.alloc)
            }
        }
        if st.zero_buf != 0 {
            st.dev_d.vk_destroy_buffer.Must()(dev, HandleFrom[vulkan.VkBufferT](st.zero_buf), raw.Null[vulkan.VkAllocationCallbacks]())
            FreeDeviceMem(a, mut st, st.zero_buf_alloc)
            st.zero_buf = 0
        }
        for i in 0..st.cmd_slots.Len() {
            cs := st.cmd_slots[i]
            if cs.pool != 0 {
                st.dev_d.vk_destroy_command_pool.Must()(dev, HandleFrom[vulkan.VkCommandPoolT](cs.pool), raw.Null[vulkan.VkAllocationCallbacks]())
            }
        }
        // Descriptor pools owned by bg_layouts were destroyed in DestroyBgLayoutRec.
        FreeAllDeviceMemBlocks(a, mut st)

        leaks := st.live_native
        st.dev_d.vk_destroy_device.Must()(dev, raw.Null[vulkan.VkAllocationCallbacks]())
        DestroyVkInstance(a, st.instance, &st.inst_d, st.debug_messenger, st.debug_ring_ptr)
        FreeEmptyDeviceLists(a, st)
        return leaks
    }
}
