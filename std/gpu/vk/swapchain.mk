// Why: Vulkan swapchains require WSI surface negotiation, an F+1 acquire
// semaphore ring, first-Submit acquire wait synchronization, a signal-only
// vkQueueSubmit2 before vkQueuePresentKHR, and vkQueueWaitIdle before resize.
//
// What: CreateSwapchain, DestroySwapchainRec, DestroySwapchain,
// GetSwapchainInfo, SwapchainAcquire, SwapchainPresent, and SwapchainResize.
//
// How: implements the full WSI frame protocol with system-stack calls for
// acquire/present/submit, runtime.Blocking for queue wait idle and 100 ms
// acquire retries, and Test-profile planted-bug assertions.

import c.vulkan
import std.core
import std.mem
import std.raw
import std.runtime
import std.task

CreateBinarySem(st: mut VkDeviceState): Error!u64 {
    unsafe "st.device is live" {
        var sci := mem.Zeroed[vulkan.VkSemaphoreCreateInfo]()
        sci.s_type = vulkan.VkStructureTypeSemaphoreCreateInfo
        var sem: vulkan.VkSemaphore = raw.Null[vulkan.VkSemaphoreT]()
        r := st.dev_d.vk_create_semaphore.Must()(HandleFrom[vulkan.VkDeviceT](st.device), raw.Of(&sci), raw.Null[vulkan.VkAllocationCallbacks](), raw.OfMut(mut sem))
        if r != vulkan.VkSuccess {
            if IsDeviceLostResult(r) { st.lost = true }
            fail MapVkError(r, CallCreateSemaphore)
        }
        st.live_native += 1
        return HandleAddr(sem)
    }
}

DestroyBinarySem(st: mut VkDeviceState, sem: u64) {
    if sem == 0 {
        return
    }
    unsafe "st.device and sem are live" {
        st.dev_d.vk_destroy_semaphore.Must()(HandleFrom[vulkan.VkDeviceT](st.device), HandleFrom[vulkan.VkSemaphoreT](sem), raw.Null[vulkan.VkAllocationCallbacks]())
        if st.live_native > 0 {
            st.live_native -= 1
        }
    }
}

ChooseSurfaceFormat(st: &VkDeviceState, surf: vulkan.VkSurfaceKhr, want_vk_fmt: i32): Error!vulkan.VkSurfaceFormatKhr {
    unsafe "pd and surf are live" {
        pd := HandleFrom[vulkan.VkPhysicalDeviceT](st.physical_device)
        var cnt: u32 = 0
        r := st.inst_d.vk_get_physical_device_surface_formats_khr.Must()(pd, surf, raw.OfMut(mut cnt), raw.Null[vulkan.VkSurfaceFormatKhr]())
        if r != vulkan.VkSuccess || cnt == 0 {
            fail Unsupported
        }
        n := if int(cnt) > 64 { 64 } else { int(cnt) }
        var fmts: [64]vulkan.VkSurfaceFormatKhr = [mem.Zeroed[vulkan.VkSurfaceFormatKhr](); 64]
        var got: u32 = Trunc[u32](n)
        _ = st.inst_d.vk_get_physical_device_surface_formats_khr.Must()(pd, surf, raw.OfMut(mut got), raw.DataMut(mut fmts[..]))
        for i in 0..int(got) {
            if fmts[i].format == want_vk_fmt && fmts[i].color_space == vulkan.VkColorSpaceSrgbNonlinearKhr {
                return fmts[i]
            }
        }
        for i in 0..int(got) {
            if fmts[i].format == want_vk_fmt {
                return fmts[i]
            }
        }
        fail Unsupported
    }
}

ChoosePresentMode(st: &VkDeviceState, surf: vulkan.VkSurfaceKhr, req_mode: int): (vk_mode: vulkan.VkPresentModeKhr, actual_mode: int) {
    var has_mailbox := false
    var has_immediate := false
    unsafe "pd and surf are live" {
        pd := HandleFrom[vulkan.VkPhysicalDeviceT](st.physical_device)
        var cnt: u32 = 0
        if st.inst_d.vk_get_physical_device_surface_present_modes_khr.Must()(pd, surf, raw.OfMut(mut cnt), raw.Null[vulkan.VkPresentModeKhr]()) == vulkan.VkSuccess && cnt > 0 {
            n := if int(cnt) > 16 { 16 } else { int(cnt) }
            var modes: [16]vulkan.VkPresentModeKhr = [0; 16]
            var got: u32 = Trunc[u32](n)
            _ = st.inst_d.vk_get_physical_device_surface_present_modes_khr.Must()(pd, surf, raw.OfMut(mut got), raw.DataMut(mut modes[..]))
            for i in 0..int(got) {
                if modes[i] == vulkan.VkPresentModeMailboxKhr { has_mailbox = true }
                if modes[i] == vulkan.VkPresentModeImmediateKhr { has_immediate = true }
            }
        }
    }
    if req_mode == ModeImmediate {
        if has_immediate { return (vk_mode: vulkan.VkPresentModeImmediateKhr, actual_mode: ModeImmediate) }
        if has_mailbox { return (vk_mode: vulkan.VkPresentModeMailboxKhr, actual_mode: ModeMailbox) }
        return (vk_mode: vulkan.VkPresentModeFifoKhr, actual_mode: ModeFifo)
    }
    if req_mode == ModeMailbox {
        if has_mailbox { return (vk_mode: vulkan.VkPresentModeMailboxKhr, actual_mode: ModeMailbox) }
        return (vk_mode: vulkan.VkPresentModeFifoKhr, actual_mode: ModeFifo)
    }
    return (vk_mode: vulkan.VkPresentModeFifoKhr, actual_mode: ModeFifo)
}

DestroySwapImagesAndViews(st: mut VkDeviceState, imgs: mut HeapList[VkSwapImage]) {
    unsafe "st.device is live" {
        for i in 0..imgs.Len() {
            im := imgs[i]
            if im.view_id != 0 {
                vidx := Cast[int](im.view_id)
                if vidx > 0 && vidx < st.views.Len() {
                    st.views[vidx].alive = false
                    st.views[vidx].vk_view = 0
                }
            }
            if im.vk_view != 0 {
                st.dev_d.vk_destroy_image_view.Must()(HandleFrom[vulkan.VkDeviceT](st.device), HandleFrom[vulkan.VkImageViewT](im.vk_view), raw.Null[vulkan.VkAllocationCallbacks]())
                if st.live_native > 0 { st.live_native -= 1 }
            }
            DestroyBinarySem(st, im.render_done_sem)
        }
    }
    imgs.Clear()
}

BuildVkSwapchainImages(
    a: &Alloc,
    st: mut VkDeviceState,
    sc_id: u64,
    surf_h: u64,
    old_sc_h: u64,
    vk_fmt: i32,
    req_mode: int,
    req_buffers: int,
    copy_src: bool,
    req_w: int,
    req_h: int,
    imgs_out: mut HeapList[VkSwapImage],
): Error!(sc_h: u64, w: int, h: int, actual_mode: int) {
    unsafe "st.device, st.physical_device, and surf_h are live" {
        pd := HandleFrom[vulkan.VkPhysicalDeviceT](st.physical_device)
        dev := HandleFrom[vulkan.VkDeviceT](st.device)
        surf := HandleFrom[vulkan.VkSurfaceKhrT](surf_h)

        var caps := mem.Zeroed[vulkan.VkSurfaceCapabilitiesKhr]()
        cr := st.inst_d.vk_get_physical_device_surface_capabilities_khr.Must()(pd, surf, raw.OfMut(mut caps))
        if cr != vulkan.VkSuccess {
            fail MapVkError(cr, CallCreateSwapchain)
        }

        var ew := caps.current_extent.width
        var eh := caps.current_extent.height
        if ew == 0xFFFF_FFFF {
            ew = Cast[u32](req_w)
            eh = Cast[u32](req_h)
            if ew < caps.min_image_extent.width { ew = caps.min_image_extent.width }
            if ew > caps.max_image_extent.width { ew = caps.max_image_extent.width }
            if eh < caps.min_image_extent.height { eh = caps.min_image_extent.height }
            if eh > caps.max_image_extent.height { eh = caps.max_image_extent.height }
        }
        if ew == 0 || eh == 0 {
            return (sc_h: 0, w: 0, h: 0, actual_mode: req_mode)
        }

        sfmt := try ChooseSurfaceFormat(&*st, surf, vk_fmt)
        (vk_mode, actual_mode) := ChoosePresentMode(&*st, surf, req_mode)

        var img_cnt: u32 = Cast[u32](req_buffers)
        if img_cnt < caps.min_image_count {
            img_cnt = caps.min_image_count
        }
        if caps.max_image_count > 0 && img_cnt > caps.max_image_count {
            img_cnt = caps.max_image_count
        }
        var usage: u32 = Cast[u32](vulkan.VkImageUsageColorAttachmentBit)
        if copy_src {
            if (caps.supported_usage_flags & Cast[u32](vulkan.VkImageUsageTransferSrcBit)) == 0 {
                fail Unsupported
            }
            usage |= Cast[u32](vulkan.VkImageUsageTransferSrcBit)
        }
        pre_tf := if (caps.supported_transforms & Cast[u32](vulkan.VkSurfaceTransformIdentityBitKhr)) != 0 {
            vulkan.VkSurfaceTransformIdentityBitKhr
        } else {
            caps.current_transform
        }
        var comp_alpha := vulkan.VkCompositeAlphaOpaqueBitKhr
        if (caps.supported_composite_alpha & Cast[u32](vulkan.VkCompositeAlphaOpaqueBitKhr)) == 0 {
            comp_alpha = vulkan.VkCompositeAlphaInheritBitKhr
        }

        var sci := mem.Zeroed[vulkan.VkSwapchainCreateInfoKhr]()
        sci.s_type = vulkan.VkStructureTypeSwapchainCreateInfoKhr
        sci.surface = surf
        sci.min_image_count = img_cnt
        sci.image_format = sfmt.format
        sci.image_color_space = sfmt.color_space
        sci.image_extent = vulkan.VkExtent2D(width: ew, height: eh)
        sci.image_array_layers = 1
        sci.image_usage = usage
        sci.image_sharing_mode = vulkan.VkSharingModeExclusive
        sci.pre_transform = pre_tf
        sci.composite_alpha = comp_alpha
        sci.present_mode = vk_mode
        sci.clipped = vulkan.VkTrue
        sci.old_swapchain = HandleFrom[vulkan.VkSwapchainKhrT](old_sc_h)

        var new_sc: vulkan.VkSwapchainKhr = raw.Null[vulkan.VkSwapchainKhrT]()
        sr := st.dev_d.vk_create_swapchain_khr.Must()(dev, raw.Of(&sci), raw.Null[vulkan.VkAllocationCallbacks](), raw.OfMut(mut new_sc))
        if sr != vulkan.VkSuccess {
            if IsDeviceLostResult(sr) { st.lost = true }
            fail MapVkError(sr, CallCreateSwapchain)
        }
        st.live_native += 1

        var real_cnt: u32 = 0
        _ = st.dev_d.vk_get_swapchain_images_khr.Must()(dev, new_sc, raw.OfMut(mut real_cnt), raw.Null[vulkan.VkImage]())
        n := if int(real_cnt) > 16 { 16 } else { int(real_cnt) }
        var vk_imgs: [16]vulkan.VkImage = [raw.Null[vulkan.VkImageT](); 16]
        var got: u32 = Trunc[u32](n)
        _ = st.dev_d.vk_get_swapchain_images_khr.Must()(dev, new_sc, raw.OfMut(mut got), raw.DataMut(mut vk_imgs[..]))

        for i in 0..int(got) {
            var ivci := mem.Zeroed[vulkan.VkImageViewCreateInfo]()
            ivci.s_type = vulkan.VkStructureTypeImageViewCreateInfo
            ivci.image = vk_imgs[i]
            ivci.view_type = vulkan.VkImageViewType2d
            ivci.format = sfmt.format
            ivci.subresource_range = vulkan.VkImageSubresourceRange(
                aspect_mask: Cast[u32](vulkan.VkImageAspectColorBit),
                base_mip_level: 0,
                level_count: 1,
                base_array_layer: 0,
                layer_count: 1,
            )
            var iv: vulkan.VkImageView = raw.Null[vulkan.VkImageViewT]()
            vr := st.dev_d.vk_create_image_view.Must()(dev, raw.Of(&ivci), raw.Null[vulkan.VkAllocationCallbacks](), raw.OfMut(mut iv))
            if vr != vulkan.VkSuccess {
                DestroySwapImagesAndViews(st, imgs_out)
                st.dev_d.vk_destroy_swapchain_khr.Must()(dev, new_sc, raw.Null[vulkan.VkAllocationCallbacks]())
                if st.live_native > 0 { st.live_native -= 1 }
                fail MapVkError(vr, CallCreateSwapchain)
            }
            st.live_native += 1

            rsem := CreateBinarySem(st) else e {
                st.dev_d.vk_destroy_image_view.Must()(dev, iv, raw.Null[vulkan.VkAllocationCallbacks]())
                if st.live_native > 0 { st.live_native -= 1 }
                DestroySwapImagesAndViews(st, imgs_out)
                st.dev_d.vk_destroy_swapchain_khr.Must()(dev, new_sc, raw.Null[vulkan.VkAllocationCallbacks]())
                if st.live_native > 0 { st.live_native -= 1 }
                fail e
            }

            vrec := VkViewRec(
                alive: true,
                vk_view: HandleAddr(iv),
                tex_id: 0,
                vk_image: HandleAddr(vk_imgs[i]),
                is_swapchain: true,
                sc_id: sc_id,
                sc_img_idx: Cast[u32](i),
                first_mip: 0,
                mip_count: 1,
                first_layer: 0,
                layer_count: 1,
                plane_mask: 1,
                is_depth: false,
                has_stencil: false,
            )
            vid := AllocViewSlot(st, a, vrec) else e {
                DestroyBinarySem(st, rsem)
                st.dev_d.vk_destroy_image_view.Must()(dev, iv, raw.Null[vulkan.VkAllocationCallbacks]())
                if st.live_native > 0 { st.live_native -= 1 }
                DestroySwapImagesAndViews(st, imgs_out)
                st.dev_d.vk_destroy_swapchain_khr.Must()(dev, new_sc, raw.Null[vulkan.VkAllocationCallbacks]())
                if st.live_native > 0 { st.live_native -= 1 }
                fail e
            }

            imgs_out.Push(a, VkSwapImage(
                vk_image: HandleAddr(vk_imgs[i]),
                vk_view: HandleAddr(iv),
                view_id: vid,
                render_done_sem: rsem,
                ever_rendered: false,
                tracked_layout: vulkan.VkImageLayoutUndefined,
            )) else e {
                DestroyBinarySem(st, rsem)
                DestroyViewRec(st, vid)
                DestroySwapImagesAndViews(st, imgs_out)
                st.dev_d.vk_destroy_swapchain_khr.Must()(dev, new_sc, raw.Null[vulkan.VkAllocationCallbacks]())
                if st.live_native > 0 { st.live_native -= 1 }
                fail e
            }
        }
        return (sc_h: HandleAddr(new_sc), w: int(ew), h: int(eh), actual_mode: actual_mode)
    }
}

/// Creates a Win32 Vulkan surface and swapchain, returning its backend ID.
export Device.CreateSwapchain(mut self, opts: SwapchainOptions): Error!u64 {
    a := self.heap.Alloc()
    st := self.state.Mut()
    if st.lost { fail DeviceLost(reason: st.loss_reason) }
    if !st.has_surface || !st.has_swapchain {
        fail Unsupported
    }
    surf_h := try CreateWin32Surface(st.gipa, HandleFrom[vulkan.VkInstanceT](st.instance), opts.hinstance, opts.hwnd)
    st.live_native += 1

    unsafe "st.physical_device and surf_h are live" {
        var sup: vulkan.VkBool32 = 0
        sr := st.inst_d.vk_get_physical_device_surface_support_khr.Must()(HandleFrom[vulkan.VkPhysicalDeviceT](st.physical_device), st.queue_family, HandleFrom[vulkan.VkSurfaceKhrT](surf_h), raw.OfMut(mut sup))
        if sr != vulkan.VkSuccess || sup == 0 {
            st.inst_d.vk_destroy_surface_khr.Must()(HandleFrom[vulkan.VkInstanceT](st.instance), HandleFrom[vulkan.VkSurfaceKhrT](surf_h), raw.Null[vulkan.VkAllocationCallbacks]())
            if st.live_native > 0 { st.live_native -= 1 }
            fail Unsupported
        }
    }

    var acq: [4]VkAcqSem = [mem.Zeroed[VkAcqSem](); 4]
    f := st.frames_in_flight
    for ai in 0..(f + 1) {
        s := CreateBinarySem(st) else e {
            for k in 0..ai {
                DestroyBinarySem(st, acq[k].sem)
            }
            unsafe "cleanup surface" {
                st.inst_d.vk_destroy_surface_khr.Must()(HandleFrom[vulkan.VkInstanceT](st.instance), HandleFrom[vulkan.VkSurfaceKhrT](surf_h), raw.Null[vulkan.VkAllocationCallbacks]())
                if st.live_native > 0 { st.live_native -= 1 }
            }
            fail e
        }
        acq[ai] = VkAcqSem(sem: s, waited_by: 0, signaled_unused: false)
    }

    if st.swapchains.Len() == 0 {
        s0 := VkSwapchainRec(
            alive: false, hwnd: 0, hinstance: 0, surface: 0, swapchain: 0,
            vk_format: 0, format_idx: 0, mode: 0, req_buffers: 0, copy_src: false,
            width: 0, height: 0, frames_in_flight: 2,
            images: HeapList[VkSwapImage].New(a), acq: [mem.Zeroed[VkAcqSem](); 4],
            acq_cursor: 0, present_serials: [0; 3], present_cursor: 0,
            handed_out: false, handed_image: 0, pending_submit_wait: false,
            pending_wait_sem: 0, pending_wait_acq_idx: 0, needs_recreate: false,
        )
        st.swapchains.Push(a, s0) else e {
            s0.Free(a)
            for k in 0..(f + 1) { DestroyBinarySem(st, acq[k].sem) }
            unsafe "cleanup surface" {
                st.inst_d.vk_destroy_surface_khr.Must()(HandleFrom[vulkan.VkInstanceT](st.instance), HandleFrom[vulkan.VkSurfaceKhrT](surf_h), raw.Null[vulkan.VkAllocationCallbacks]())
                if st.live_native > 0 { st.live_native -= 1 }
            }
            fail e
        }
    }

    var found := 0
    for i in 1..st.swapchains.Len() {
        if !st.swapchains[i].alive {
            found = i
            break
        }
    }
    target_sc_id := if found > 0 { Cast[u64](found) } else { Cast[u64](st.swapchains.Len()) }

    var imgs := HeapList[VkSwapImage].New(a)
    res := BuildVkSwapchainImages(a, st, target_sc_id, surf_h, 0, opts.vk_format, opts.mode, opts.buffers, opts.copy_src, opts.width, opts.height, mut imgs) else e {
        DestroySwapImagesAndViews(st, mut imgs)
        imgs.FreeEmpty(a)
        for k in 0..(f + 1) { DestroyBinarySem(st, acq[k].sem) }
        unsafe "cleanup surface" {
            st.inst_d.vk_destroy_surface_khr.Must()(HandleFrom[vulkan.VkInstanceT](st.instance), HandleFrom[vulkan.VkSurfaceKhrT](surf_h), raw.Null[vulkan.VkAllocationCallbacks]())
            if st.live_native > 0 { st.live_native -= 1 }
        }
        fail e
    }

    var sc_id: u64 = 0
    if found > 0 {
        empty0 := core.Replace(mut st.swapchains[found].images, imgs)
        empty0.FreeEmpty(a)
        sc := mut st.swapchains[found]
        sc.alive = true
        sc.hwnd = opts.hwnd
        sc.hinstance = opts.hinstance
        sc.surface = surf_h
        sc.swapchain = res.sc_h
        sc.vk_format = opts.vk_format
        sc.format_idx = opts.format_idx
        sc.mode = res.actual_mode
        sc.req_buffers = opts.buffers
        sc.copy_src = opts.copy_src
        sc.width = res.w
        sc.height = res.h
        sc.frames_in_flight = f
        sc.acq = acq
        sc.acq_cursor = 0
        sc.present_serials = [0; 3]
        sc.present_cursor = 0
        sc.handed_out = false
        sc.handed_image = 0
        sc.pending_submit_wait = false
        sc.pending_wait_sem = 0
        sc.pending_wait_acq_idx = 0
        sc.needs_recreate = false
        sc_id = Cast[u64](found)
    } else {
        sc_id = Cast[u64](st.swapchains.Len())
        rec := VkSwapchainRec(
            alive: true,
            hwnd: opts.hwnd,
            hinstance: opts.hinstance,
            surface: surf_h,
            swapchain: res.sc_h,
            vk_format: opts.vk_format,
            format_idx: opts.format_idx,
            mode: res.actual_mode,
            req_buffers: opts.buffers,
            copy_src: opts.copy_src,
            width: res.w,
            height: res.h,
            frames_in_flight: f,
            images: imgs,
            acq: acq,
            acq_cursor: 0,
            present_serials: [0; 3],
            present_cursor: 0,
            handed_out: false,
            handed_image: 0,
            pending_submit_wait: false,
            pending_wait_sem: 0,
            pending_wait_acq_idx: 0,
            needs_recreate: false,
        )
        st.swapchains.Push(a, rec) else e {
            var rec2 := rec
            DestroySwapImagesAndViews(st, mut rec2.images)
            rec2.Free(a)
            unsafe "cleanup swapchain" {
                st.dev_d.vk_destroy_swapchain_khr.Must()(HandleFrom[vulkan.VkDeviceT](st.device), HandleFrom[vulkan.VkSwapchainKhrT](res.sc_h), raw.Null[vulkan.VkAllocationCallbacks]())
                if st.live_native > 0 { st.live_native -= 1 }
            }
            for k in 0..(f + 1) { DestroyBinarySem(st, acq[k].sem) }
            unsafe "cleanup surface" {
                st.inst_d.vk_destroy_surface_khr.Must()(HandleFrom[vulkan.VkInstanceT](st.instance), HandleFrom[vulkan.VkSurfaceKhrT](surf_h), raw.Null[vulkan.VkAllocationCallbacks]())
                if st.live_native > 0 { st.live_native -= 1 }
            }
            fail e
        }
    }
    return sc_id
}

RetireSwapchainImages(a: &Alloc into st, st: mut VkDeviceState, idx: int) {
    empty := HeapList[VkSwapImage].New(a)
    var imgs := core.Replace(mut st.swapchains[idx].images, empty)
    DestroySwapImagesAndViews(st, mut imgs)
    imgs.FreeEmpty(a)
    if st.swapchains[idx].swapchain != 0 {
        unsafe "st.device and swapchain are live and idle" {
            dev := HandleFrom[vulkan.VkDeviceT](st.device)
            st.dev_d.vk_destroy_swapchain_khr.Must()(dev, HandleFrom[vulkan.VkSwapchainKhrT](st.swapchains[idx].swapchain), raw.Null[vulkan.VkAllocationCallbacks]())
        }
        st.swapchains[idx].swapchain = 0
        if st.live_native > 0 { st.live_native -= 1 }
    }
    st.swapchains[idx].pending_submit_wait = false
    st.swapchains[idx].pending_wait_sem = 0
}

DestroySwapchainRec(a: &Alloc into st, st: mut VkDeviceState, sc_id: u64) {
    idx := Cast[int](sc_id)
    if idx <= 0 || idx >= st.swapchains.Len() || !st.swapchains[idx].alive {
        return
    }
    st.swapchains[idx].alive = false
    unsafe "st.device and st.instance are live" {
        _ = st.dev_d.vk_queue_wait_idle.Must()(HandleFrom[vulkan.VkQueueT](st.queue))
        RetireSwapchainImages(a, st, idx)
        for ai in 0..4 {
            sem := st.swapchains[idx].acq[ai].sem
            if sem != 0 {
                DestroyBinarySem(st, sem)
                st.swapchains[idx].acq[ai].sem = 0
            }
        }
        if st.swapchains[idx].surface != 0 {
            st.inst_d.vk_destroy_surface_khr.Must()(HandleFrom[vulkan.VkInstanceT](st.instance), HandleFrom[vulkan.VkSurfaceKhrT](st.swapchains[idx].surface), raw.Null[vulkan.VkAllocationCallbacks]())
            st.swapchains[idx].surface = 0
            if st.live_native > 0 { st.live_native -= 1 }
        }
    }
}

/// Destroys a Vulkan swapchain, its surface, and its semaphores.
export Device.DestroySwapchain(mut self, sc_id: u64) {
    a := self.heap.Alloc()
    DestroySwapchainRec(a, self.state.Mut(), sc_id)
}

/// Returns the negotiated swapchain properties.
export Device.GetSwapchainInfo(&self, sc_id: u64): SwapchainInfo {
    st := self.state.Ref()
    idx := Cast[int](sc_id)
    if idx <= 0 || idx >= st.swapchains.Len() || !st.swapchains[idx].alive {
        return SwapchainInfo(format_idx: 0, mode: 0, width: 0, height: 0, images: 0)
    }
    sc := &st.swapchains[idx]
    return SwapchainInfo(format_idx: sc.format_idx, mode: sc.mode, width: sc.width, height: sc.height, images: sc.images.Len())
}

struct VkAcquireCallArg {
    acq_fn_addr: uint
    dev: u64
    swapchain: u64
    timeout: u64
    sem: u64
    out_img: u32
    out_res: i32
}

RunAcquireSys(arg: *u8) {
    unsafe "arg is a stack-pinned VkAcquireCallArg" {
        a := raw.PtrCast[u8, VkAcquireCallArg](arg)
        f := raw.FnFromAddr[vulkan.PfnVkAcquireNextImageKhr]((*a).acq_fn_addr).Must()
        var idx: u32 = 0
        r := f(
            HandleFrom[vulkan.VkDeviceT]((*a).dev),
            HandleFrom[vulkan.VkSwapchainKhrT]((*a).swapchain),
            (*a).timeout,
            HandleFrom[vulkan.VkSemaphoreT]((*a).sem),
            raw.Null[vulkan.VkFenceT](),
            raw.OfMut(mut idx),
        )
        (*a).out_img = idx
        (*a).out_res = r
    }
}

RunAcquireBlocking(a0: VkAcquireCallArg): VkAcquireCallArg {
    var a := a0
    unsafe "a is local stack value passed by pointer into RunAcquireSys" {
        RunAcquireSys(raw.PtrCast[VkAcquireCallArg, u8](raw.OfMut(mut a)))
    }
    return a
}

/// Acquires the next swapchain image following §B.1 pacing and retry rules.
export Device.SwapchainAcquire(mut self, io: Io, sc_id: u64, win_w: int, win_h: int): Error!AcquireResult {
    st := self.state.Mut()
    if st.lost { fail DeviceLost(reason: st.loss_reason) }
    sc_idx := Cast[int](sc_id)
    if sc_idx <= 0 || sc_idx >= st.swapchains.Len() || !st.swapchains[sc_idx].alive {
        fail SurfaceLost
    }
    if st.swapchains[sc_idx].needs_recreate || st.swapchains[sc_idx].handed_out {
        return AcquireResult(status: 1, view_id: 0, image_idx: 0)
    }
    if win_w == 0 || win_h == 0 || st.swapchains[sc_idx].swapchain == 0 {
        return AcquireResult(status: 2, view_id: 0, image_idx: 0)
    }

    // Step 3: frame-in-flight pacing against present_serials[k mod F].
    p_ser := st.swapchains[sc_idx].present_serials[st.swapchains[sc_idx].present_cursor]
    if p_ser > 0 {
        try WaitForSerial(st, io, p_ser)
    }

    // Step 4: take acq[i] from F+1 ring; assert waited_by has completed.
    f := st.swapchains[sc_idx].frames_in_flight
    ring_len := if st.planted_bug == BugAcqRingF { f } else { f + 1 }
    ai := st.swapchains[sc_idx].acq_cursor % ring_len
    waited_by := st.swapchains[sc_idx].acq[ai].waited_by
    if waited_by > 0 && PollCompletedSerial(st) < waited_by {
        Panic("vk swapchain: acquire semaphore reused before prior submit completed")
    }
    acq_sem := st.swapchains[sc_idx].acq[ai].sem

    // Step 5: non-blocking acquire on system stack, retrying in Blocking with 100ms timeout.
    var acq_addr: uint = 0
    unsafe "extracts vkAcquireNextImageKHR fn address" {
        var afn := st.dev_d.vk_acquire_next_image_khr.Must()
        acq_addr = raw.Read(raw.PtrCast[fn(*vulkan.VkDeviceT, *vulkan.VkSwapchainKhrT, u64, *vulkan.VkSemaphoreT, *vulkan.VkFenceT, *u32): vulkan.VkResult, uint](raw.Of(&afn)))
    }
    var arg := VkAcquireCallArg(
        acq_fn_addr: acq_addr,
        dev: st.device,
        swapchain: st.swapchains[sc_idx].swapchain,
        timeout: 0,
        sem: acq_sem,
        out_img: 0,
        out_res: 0,
    )
    unsafe "arg is stack-pinned across MkSystemRun" {
        task.MkSystemRun(RunAcquireSys, raw.PtrCast[VkAcquireCallArg, u8](raw.OfMut(mut arg)))
    }
    for arg.out_res == vulkan.VkNotReady || arg.out_res == vulkan.VkTimeout {
        arg.timeout = 100_000_000 // 100 ms finite timeout
        arg = runtime.Blocking(io, arg, RunAcquireBlocking) else e {
            fail e
        }
        if arg.out_res == vulkan.VkSuccess || arg.out_res == vulkan.VkSuboptimalKhr {
            io.Sleep(core.DurationOf(0)) else e {
                // Cancel won after this 100ms call signalled acq_sem; mark unused for recreate.
                st.swapchains[sc_idx].acq[ai].signaled_unused = true
                st.swapchains[sc_idx].needs_recreate = true
                fail e
            }
            break
        }
    }

    r := arg.out_res
    if r == vulkan.VkErrorOutOfDateKhr {
        st.swapchains[sc_idx].needs_recreate = true
        return AcquireResult(status: 1, view_id: 0, image_idx: 0)
    }
    if r == vulkan.VkErrorSurfaceLostKhr {
        fail SurfaceLost
    }
    if r == vulkan.VkErrorDeviceLost {
        st.lost = true
        fail DeviceLost(reason: LossUnknown)
    }
    if r != vulkan.VkSuccess && r != vulkan.VkSuboptimalKhr {
        fail MapVkError(r, CallAcquireNextImage)
    }
    if r == vulkan.VkSuboptimalKhr {
        st.swapchains[sc_idx].needs_recreate = true
    }

    img_idx := arg.out_img
    st.swapchains[sc_idx].acq_cursor = (ai + 1) % ring_len
    st.swapchains[sc_idx].handed_out = true
    st.swapchains[sc_idx].handed_image = img_idx
    st.swapchains[sc_idx].pending_submit_wait = true
    st.swapchains[sc_idx].pending_wait_sem = acq_sem
    st.swapchains[sc_idx].pending_wait_acq_idx = ai
    st.swapchains[sc_idx].acq[ai].signaled_unused = true

    vid := st.swapchains[sc_idx].images[int(img_idx)].view_id
    return AcquireResult(status: 0, view_id: vid, image_idx: img_idx)
}

struct VkPresentCallArg {
    st_ptr: uint
    sc_idx: int
    img_idx: u32
    no_sub_serial: u64
    present_serial: u64
    out_suboptimal: bool
    out_err: ?Error
}

RunPresentSys(arg: *u8) {
    unsafe "arg is stack-pinned across MkSystemRun" {
        a := raw.PtrCast[u8, VkPresentCallArg](arg)
        st := raw.FromAddr[VkDeviceState]((*a).st_ptr)
        sc := mut (*st).swapchains[(*a).sc_idx]
        dev := HandleFrom[vulkan.VkDeviceT]((*st).device)
        q := HandleFrom[vulkan.VkQueueT]((*st).queue)
        img := mut sc.images[int((*a).img_idx)]

        // If no Submit ran since Acquire, submit a minimal UNDEFINED -> PRESENT_SRC_KHR transition waiting on acq_sem.
        if sc.pending_submit_wait {
            alloc := (*st).mem_blocks.Len()
            _ = alloc
            var cpci := mem.Zeroed[vulkan.VkCommandPoolCreateInfo]()
            cpci.s_type = vulkan.VkStructureTypeCommandPoolCreateInfo
            cpci.flags = Cast[u32](vulkan.VkCommandPoolCreateTransientBit)
            cpci.queue_family_index = (*st).queue_family
            var pool: vulkan.VkCommandPool = raw.Null[vulkan.VkCommandPoolT]()
            if (*st).dev_d.vk_create_command_pool.Must()(dev, raw.Of(&cpci), raw.Null[vulkan.VkAllocationCallbacks](), raw.OfMut(mut pool)) == vulkan.VkSuccess {
                var cbai := mem.Zeroed[vulkan.VkCommandBufferAllocateInfo]()
                cbai.s_type = vulkan.VkStructureTypeCommandBufferAllocateInfo
                cbai.command_pool = pool
                cbai.level = vulkan.VkCommandBufferLevelPrimary
                cbai.command_buffer_count = 1
                var cmd: vulkan.VkCommandBuffer = raw.Null[vulkan.VkCommandBufferT]()
                if (*st).dev_d.vk_allocate_command_buffers.Must()(dev, raw.Of(&cbai), raw.OfMut(mut cmd)) == vulkan.VkSuccess {
                    var bi := mem.Zeroed[vulkan.VkCommandBufferBeginInfo]()
                    bi.s_type = vulkan.VkStructureTypeCommandBufferBeginInfo
                    bi.flags = Cast[u32](vulkan.VkCommandBufferUsageOneTimeSubmitBit)
                    _ = (*st).dev_d.vk_begin_command_buffer.Must()(cmd, raw.Of(&bi))
                    EmitSingleImageBarrier(
                        &*st, cmd, HandleFrom[vulkan.VkImageT](img.vk_image), Cast[u32](vulkan.VkImageAspectColorBit),
                        0, 1, 0, 1,
                        vulkan.VkImageLayoutUndefined, vulkan.VkImageLayoutPresentSrcKhr,
                        vulkan.VkPipelineStage2ColorAttachmentOutputBit, 0,
                        vulkan.VkPipelineStage2None, 0,
                    )
                    _ = (*st).dev_d.vk_end_command_buffer.Must()(cmd)
                    var wait_i := vulkan.VkSemaphoreSubmitInfo(
                        s_type: vulkan.VkStructureTypeSemaphoreSubmitInfo, p_next: raw.Null[void](),
                        semaphore: HandleFrom[vulkan.VkSemaphoreT](sc.pending_wait_sem), value: 0,
                        stage_mask: vulkan.VkPipelineStage2ColorAttachmentOutputBit | vulkan.VkPipelineStage2CopyBit, device_index: 0,
                    )
                    var cb_i := vulkan.VkCommandBufferSubmitInfo(s_type: vulkan.VkStructureTypeCommandBufferSubmitInfo, p_next: raw.Null[void](), command_buffer: cmd, device_mask: 0)
                    var sig_i := vulkan.VkSemaphoreSubmitInfo(
                        s_type: vulkan.VkStructureTypeSemaphoreSubmitInfo, p_next: raw.Null[void](),
                        semaphore: HandleFrom[vulkan.VkSemaphoreT]((*st).timeline_sem), value: (*a).no_sub_serial,
                        stage_mask: vulkan.VkPipelineStage2AllCommandsBit, device_index: 0,
                    )
                    var si := mem.Zeroed[vulkan.VkSubmitInfo2]()
                    si.s_type = vulkan.VkStructureTypeSubmitInfo2
                    si.wait_semaphore_info_count = 1
                    si.p_wait_semaphore_infos = raw.Of(&wait_i)
                    si.command_buffer_info_count = 1
                    si.p_command_buffer_infos = raw.Of(&cb_i)
                    si.signal_semaphore_info_count = 1
                    si.p_signal_semaphore_infos = raw.Of(&sig_i)
                    _ = (*st).dev_d.vk_queue_submit2.Must()(q, 1, raw.Of(&si), raw.Null[vulkan.VkFenceT]())
                    _ = (*st).dev_d.vk_queue_wait_idle.Must()(q)
                }
                (*st).dev_d.vk_destroy_command_pool.Must()(dev, pool, raw.Null[vulkan.VkAllocationCallbacks]())
            }
            acqi := sc.pending_wait_acq_idx
            sc.acq[acqi].waited_by = (*a).no_sub_serial
            sc.acq[acqi].signaled_unused = false
            sc.pending_submit_wait = false
            sc.pending_wait_sem = 0
            img.tracked_layout = vulkan.VkImageLayoutPresentSrcKhr
        }

        // Step 1: signal-only vkQueueSubmit2 (no command buffers) signalling render_done[j] + timeline_sem.
        if (*st).planted_bug != BugSkipSignalPresent {
            var sigs: [2]vulkan.VkSemaphoreSubmitInfo = [
                vulkan.VkSemaphoreSubmitInfo(
                    s_type: vulkan.VkStructureTypeSemaphoreSubmitInfo, p_next: raw.Null[void](),
                    semaphore: HandleFrom[vulkan.VkSemaphoreT](img.render_done_sem), value: 0,
                    stage_mask: vulkan.VkPipelineStage2AllCommandsBit, device_index: 0,
                ),
                vulkan.VkSemaphoreSubmitInfo(
                    s_type: vulkan.VkStructureTypeSemaphoreSubmitInfo, p_next: raw.Null[void](),
                    semaphore: HandleFrom[vulkan.VkSemaphoreT]((*st).timeline_sem), value: (*a).present_serial,
                    stage_mask: vulkan.VkPipelineStage2AllCommandsBit, device_index: 0,
                ),
            ]
            var si2 := mem.Zeroed[vulkan.VkSubmitInfo2]()
            si2.s_type = vulkan.VkStructureTypeSubmitInfo2
            si2.signal_semaphore_info_count = 2
            si2.p_signal_semaphore_infos = raw.Data(sigs[..])
            sr := (*st).dev_d.vk_queue_submit2.Must()(q, 1, raw.Of(&si2), raw.Null[vulkan.VkFenceT]())
            if sr != vulkan.VkSuccess {
                if IsDeviceLostResult(sr) { (*st).lost = true }
                (*a).out_err = some(MapVkError(sr, CallQueueSubmit))
                return
            }
        }

        // Step 2: vkQueuePresentKHR waiting on render_done[j].
        var wsem := HandleFrom[vulkan.VkSemaphoreT](img.render_done_sem)
        var vsc := HandleFrom[vulkan.VkSwapchainKhrT](sc.swapchain)
        var iidx := (*a).img_idx
        var pi := mem.Zeroed[vulkan.VkPresentInfoKhr]()
        pi.s_type = vulkan.VkStructureTypePresentInfoKhr
        pi.wait_semaphore_count = 1
        pi.p_wait_semaphores = raw.Of(&wsem)
        pi.swapchain_count = 1
        pi.p_swapchains = raw.Of(&vsc)
        pi.p_image_indices = raw.Of(&iidx)
        pr := (*st).dev_d.vk_queue_present_khr.Must()(q, raw.Of(&pi))

        if pr == vulkan.VkErrorDeviceLost {
            (*st).lost = true
            (*a).out_err = some(DeviceLost(reason: LossUnknown))
            return
        }
        if pr == vulkan.VkErrorSurfaceLostKhr {
            (*a).out_err = some(SurfaceLost)
            return
        }
        if pr == vulkan.VkErrorOutOfDateKhr {
            sc.needs_recreate = true
            (*a).out_suboptimal = true
        } else if pr == vulkan.VkSuboptimalKhr {
            sc.needs_recreate = true
            (*a).out_suboptimal = true
        } else if pr != vulkan.VkSuccess {
            (*a).out_err = some(MapVkError(pr, CallQueuePresent))
            return
        }
        sc.present_serials[sc.present_cursor] = (*a).present_serial
        sc.present_cursor = (sc.present_cursor + 1) % sc.frames_in_flight
        sc.handed_out = false
    }
}

/// Presents an acquired swapchain image following §B.1.
export Device.SwapchainPresent(mut self, sc_id: u64, img_idx: u32, no_sub_serial: u64, present_serial: u64): Error!bool {
    st := self.state.Mut()
    if st.lost { fail DeviceLost(reason: st.loss_reason) }
    sc_idx := Cast[int](sc_id)
    if sc_idx <= 0 || sc_idx >= st.swapchains.Len() || !st.swapchains[sc_idx].alive {
        fail SurfaceLost
    }
    if present_serial > st.next_serial {
        st.next_serial = present_serial + 1
    } else {
        st.next_serial += 1
    }
    var arg := VkPresentCallArg(
        st_ptr: raw.Addr(raw.Of(&*st)),
        sc_idx: sc_idx,
        img_idx: img_idx,
        no_sub_serial: no_sub_serial,
        present_serial: present_serial,
        out_suboptimal: false,
        out_err: none,
    )
    unsafe "arg is stack-pinned across MkSystemRun" {
        task.MkSystemRun(RunPresentSys, raw.PtrCast[VkPresentCallArg, u8](raw.OfMut(mut arg)))
    }
    match arg.out_err {
        some(e) { fail e }
        none {}
    }
    return arg.out_suboptimal
}

struct QueueWaitJob {
    wait_fn_addr: uint
    queue: u64
    out_res: i32
}

RunQueueWaitIdleJob(j0: QueueWaitJob): QueueWaitJob {
    var j := j0
    unsafe "queue handle and vkQueueWaitIdle remain valid across Blocking" {
        f := raw.FnFromAddr[vulkan.PfnVkQueueWaitIdle](j.wait_fn_addr).Must()
        j.out_res = f(HandleFrom[vulkan.VkQueueT](j.queue))
        return j
    }
}

/// Waits for the graphics queue on a blocking thread. Reports device loss,
/// driver errors, or cancellation before the wait starts. Allocates nothing.
export Device.WaitQueueIdle(mut self, io: Io): Error!void {
    try WaitQueueIdle(self.state.Mut(), io)
}

WaitQueueIdle(st: mut VkDeviceState, io: Io): Error!void {
    var qwi_addr: uint = 0
    unsafe "extracts vkQueueWaitIdle fn address" {
        var qfn := st.dev_d.vk_queue_wait_idle.Must()
        qwi_addr = raw.Read(raw.PtrCast[fn(*vulkan.VkQueueT): vulkan.VkResult, uint](raw.Of(&qfn)))
    }
    var job := QueueWaitJob(wait_fn_addr: qwi_addr, queue: st.queue, out_res: 0)
    job = try runtime.Blocking(io, job, RunQueueWaitIdleJob)
    if job.out_res != vulkan.VkSuccess {
        if IsDeviceLostResult(job.out_res) { st.lost = true }
        fail MapVkError(job.out_res, CallQueueSubmit)
    }
}

/// Recreates swapchain images at width x height after vkQueueWaitIdle.
export Device.SwapchainResize(mut self, io: Io, sc_id: u64, width: int, height: int): Error!void {
    a := self.heap.Alloc()
    st := self.state.Mut()
    sc_idx := Cast[int](sc_id)
    if st.lost { fail DeviceLost(reason: st.loss_reason) }
    if sc_idx <= 0 || sc_idx >= st.swapchains.Len() || !st.swapchains[sc_idx].alive {
        fail SurfaceLost
    }
    try WaitQueueIdle(st, io)

    old_sc := st.swapchains[sc_idx].swapchain
    surf_h := st.swapchains[sc_idx].surface
    vk_fmt := st.swapchains[sc_idx].vk_format
    req_mode := st.swapchains[sc_idx].mode
    req_bufs := st.swapchains[sc_idx].req_buffers
    copy_src := st.swapchains[sc_idx].copy_src
    f := st.swapchains[sc_idx].frames_in_flight

    var new_acq: [4]u64 = [0; 4]
    onerr {
        for ai in 0..(f + 1) { DestroyBinarySem(st, new_acq[ai]) }
    }
    for ai in 0..(f + 1) {
        if st.swapchains[sc_idx].acq[ai].signaled_unused {
            new_acq[ai] = try CreateBinarySem(st)
        }
    }

    var new_imgs := HeapList[VkSwapImage].New(a)
    res := BuildVkSwapchainImages(a, st, sc_id, surf_h, old_sc, vk_fmt, req_mode, req_bufs, copy_src, width, height, mut new_imgs) else e {
        DestroySwapImagesAndViews(st, mut new_imgs)
        new_imgs.FreeEmpty(a)
        // Creating a replacement can retire oldSwapchain even on failure.
        RetireSwapchainImages(a, st, sc_idx)
        st.swapchains[sc_idx].needs_recreate = true
        fail e
    }

    RetireSwapchainImages(a, st, sc_idx)
    empty := core.Replace(mut st.swapchains[sc_idx].images, new_imgs)
    empty.FreeEmpty(a)

    // Replace any acquire semaphore that was signalled in an abandoned frame and never waited.
    for ai in 0..(f + 1) {
        if new_acq[ai] != 0 {
            old_sem := st.swapchains[sc_idx].acq[ai].sem
            DestroyBinarySem(st, old_sem)
            st.swapchains[sc_idx].acq[ai] = VkAcqSem(sem: new_acq[ai], waited_by: 0, signaled_unused: false)
        }
    }

    sc := mut st.swapchains[sc_idx]
    sc.swapchain = res.sc_h
    sc.width = res.w
    sc.height = res.h
    sc.mode = res.actual_mode
    sc.handed_out = false
    sc.handed_image = 0
    sc.needs_recreate = false
    sc.present_serials = [0; 3]
    sc.present_cursor = 0
}
