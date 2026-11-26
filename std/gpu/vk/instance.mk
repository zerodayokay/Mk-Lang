// Why: Vulkan initialization must probe vulkan-1.dll cleanly without crashing
// when absent, enable Vulkan 1.3 + surface + optional debug_utils, capture
// debug messages in a lock-free ring, and select qualifying physical devices.
//
// What: IsAvailable, CreateVkInstance, DestroyVkInstance, VkDebugRing,
// DrainDebugMessages, EnumAdapters, and QueryFormatCaps.
//
// How: uses dl.Present(io, vulkan.vkGetInstanceProcAddr) for detection, runs
// adapter enumeration through runtime.Blocking, checks Vulkan 1.3 + timeline
// semaphore + dynamic rendering + sync2 + draw parameters, and maps
// VkFormatProperties (including D24S8 -> D32S8 fallback) for all 44 formats.

import c.vulkan
import std.core
import std.dl
import std.mem
import std.raw
import std.runtime

var GVkGipaAddr: raw.Atomic[uint] = raw.Atomic[uint].New(0)

LoadVkGetInstanceProcAddr(io: Io): vulkan.PfnVkGetInstanceProcAddr {
    cached := GVkGipaAddr.Load()
    if cached != 0 {
        unsafe "cached is vkGetInstanceProcAddr address from detached vulkan loader library" {
            return raw.FnFromAddr[vulkan.PfnVkGetInstanceProcAddr](cached)
        }
    }
    lib_name := if Target.os == core.Os.Windows { "vulkan-1.dll" } else { "libvulkan.so.1" }
    lib := dl.Open(io, lib_name, dl.Search.System) else { return none }
    sym := lib.Symbol("vkGetInstanceProcAddr")
    match sym {
        none {
            lib.Close(io)
            return none
        }
        some(p) {
            addr := raw.Addr(p)
            if addr == 0 {
                lib.Close(io)
                return none
            }
            _ = GVkGipaAddr.Cas(0, addr)
            lib.Detach()
            unsafe "addr is vkGetInstanceProcAddr from detached vulkan loader library" {
                return raw.FnFromAddr[vulkan.PfnVkGetInstanceProcAddr](GVkGipaAddr.Load())
            }
        }
    }
}

/// Reports whether Vulkan (vulkan-1.dll / libvulkan.so.1) is present and loadable on this host.
export IsAvailable(io: Io): bool {
    return LoadVkGetInstanceProcAddr(io).IsSome()
}

DebugMessengerCallback(
    severity: vulkan.VkDebugUtilsMessageSeverityFlagBitsExt,
    msg_types: vulkan.VkDebugUtilsMessageTypeFlagsExt,
    data: *vulkan.VkDebugUtilsMessengerCallbackDataExt,
    user_data: *void,
): vulkan.VkBool32 {
    _ = msg_types
    if raw.IsNull(user_data) || raw.IsNull(data) {
        return vulkan.VkFalse
    }
    unsafe "user_data is a live Heap-allocated VkDebugRing and data is valid during callback" {
        ring := raw.PtrCast[void, VkDebugRing](user_data)
        sev_u := Cast[u32](severity)
        if (sev_u & Cast[u32](vulkan.VkDebugUtilsMessageSeverityErrorBitExt)) != 0 {
            _ = (*ring).err_count.Add(1)
        } else if (sev_u & Cast[u32](vulkan.VkDebugUtilsMessageSeverityWarningBitExt)) != 0 {
            _ = (*ring).warn_count.Add(1)
        }
        idx := Cast[int]((*ring).write_idx.Add(1) & Cast[u32](DebugRingSlots - 1))
        slot := raw.OfMut(mut (*ring).slots[idx])
        if !(*slot).state.Cas(0, 1) {
            _ = (*ring).dropped.Add(1)
            return vulkan.VkFalse
        }
        (*slot).severity = sev_u
        (*slot).id = i64((*data).message_id_number)
        var n := 0
        if !raw.IsNull((*data).p_message) {
            p := raw.PtrCast[u8, u8]((*data).p_message)
            for n < DebugRingTextBytes {
                c := raw.Read(raw.Offset(p, n))
                if c == 0 {
                    break
                }
                (*slot).text[n] = c
                n += 1
            }
        }
        (*slot).len = Cast[u32](n)
        (*slot).state.Store(2)
    }
    return vulkan.VkFalse
}

CStrEq(p: *u8, lit: str): bool {
    bs := lit.AsBytes()
    unsafe "p is a NUL-terminated C string from a Vulkan property struct" {
        for i in 0..bs.Len() {
            if raw.Read(raw.Offset(p, i)) != bs[i] {
                return false
            }
        }
        return raw.Read(raw.Offset(p, bs.Len())) == 0
    }
}

CheckInstanceSupport(gd: &GlobalDispatch): (has_surface: bool, has_win32: bool, has_debug: bool, has_val: bool) {
    var has_surface := false
    var has_win32 := false
    var has_debug := false
    var has_val := false
    match gd.vk_enumerate_instance_extension_properties {
        some(enum_ext) {
            unsafe "enumerates instance extensions into temporary stack/heap buffer" {
                var cnt: u32 = 0
                if enum_ext(raw.Null[u8](), raw.OfMut(mut cnt), raw.Null[vulkan.VkExtensionProperties]()) == vulkan.VkSuccess && cnt > 0 {
                    n := if int(cnt) > 256 { 256 } else { int(cnt) }
                    var exts: [256]vulkan.VkExtensionProperties = [mem.Zeroed[vulkan.VkExtensionProperties](); 256]
                    var got: u32 = Trunc[u32](n)
                    if enum_ext(raw.Null[u8](), raw.OfMut(mut got), raw.DataMut(mut exts[..])) == vulkan.VkSuccess || true {
                        for i in 0..int(got) {
                            ep := raw.Data(exts[i].extension_name[..])
                            if CStrEq(ep, "VK_KHR_surface") { has_surface = true }
                            if CStrEq(ep, "VK_KHR_win32_surface") { has_win32 = true }
                            if CStrEq(ep, "VK_EXT_debug_utils") { has_debug = true }
                        }
                    }
                }
            }
        }
        none {}
    }
    match gd.vk_enumerate_instance_layer_properties {
        some(enum_lay) {
            unsafe "enumerates instance layers into fixed stack array" {
                var cnt: u32 = 0
                if enum_lay(raw.OfMut(mut cnt), raw.Null[vulkan.VkLayerProperties]()) == vulkan.VkSuccess && cnt > 0 {
                    n := if int(cnt) > 128 { 128 } else { int(cnt) }
                    var lays: [128]vulkan.VkLayerProperties = [mem.Zeroed[vulkan.VkLayerProperties](); 128]
                    var got: u32 = Trunc[u32](n)
                    _ = enum_lay(raw.OfMut(mut got), raw.DataMut(mut lays[..]))
                    for i in 0..int(got) {
                        lp := raw.Data(lays[i].layer_name[..])
                        if CStrEq(lp, "VK_LAYER_KHRONOS_validation") { has_val = true }
                    }
                }
            }
        }
        none {}
    }
    return (has_surface: has_surface, has_win32: has_win32, has_debug: has_debug, has_val: has_val)
}

struct VkInstCreated {
    inst: u64
    gipa: vulkan.PfnVkGetInstanceProcAddr
    inst_d: InstanceDispatch
    has_surface: bool
    has_debug: bool
    messenger: u64
    ring_ptr: uint
}

CreateVkInstance(a: &Alloc, io: Io, debug_mode: int): Error!VkInstCreated {
    gipa := LoadVkGetInstanceProcAddr(io)
    if gipa.IsNone() {
        fail Unsupported
    }
    return try CreateVkInstanceRaw(gipa, some(a), debug_mode)
}

CreateVkInstanceRaw(gipa: vulkan.PfnVkGetInstanceProcAddr, ring_alloc: ?&Alloc, debug_mode: int): Error!VkInstCreated {
    gd := match LoadGlobalDispatch(gipa) {
        some(g) { g }
        none { fail Unsupported }
    }
    match gd.vk_enumerate_instance_version {
        some(ev) {
            unsafe "ver is a stack u32" {
                var ver: u32 = 0
                if ev(raw.OfMut(mut ver)) != vulkan.VkSuccess || ver < vulkan.VkApiVersion13 {
                    fail Unsupported
                }
            }
        }
        none { fail Unsupported }
    }

    sup := CheckInstanceSupport(&gd)
    if debug_mode == DebugStrict && !sup.has_val {
        fail Missing(feature: FeatDebugLayer)
    }

    ring_ptr: uint = match ring_alloc {
        some(ra) { if sup.has_debug { AllocZeroedDebugRing(ra) } else { 0 } }
        none { 0 }
    }
    var ext_ptrs: [4]*u8 = [raw.Null[u8](); 4]
    var ext_cnt: u32 = 0
    if sup.has_surface {
        ext_ptrs[int(ext_cnt)] = raw.StrData("VK_KHR_surface\x00")
        ext_cnt += 1
        if Win32SurfaceSupported() && sup.has_win32 {
            ext_ptrs[int(ext_cnt)] = raw.StrData("VK_KHR_win32_surface\x00")
            ext_cnt += 1
        }
    }
    if sup.has_debug {
        ext_ptrs[int(ext_cnt)] = raw.StrData("VK_EXT_debug_utils\x00")
        ext_cnt += 1
    }

    var lay_ptrs: [1]*u8 = [raw.StrData("VK_LAYER_KHRONOS_validation\x00")]
    lay_cnt: u32 = if debug_mode >= DebugValidate && sup.has_val { 1 } else { 0 }

    var app := mem.Zeroed[vulkan.VkApplicationInfo]()
    app.s_type = vulkan.VkStructureTypeApplicationInfo
    app.p_application_name = raw.StrData("mk\x00")
    app.p_engine_name = raw.StrData("mk.gpu\x00")
    app.api_version = vulkan.VkApiVersion13

    var ici := mem.Zeroed[vulkan.VkInstanceCreateInfo]()
    ici.s_type = vulkan.VkStructureTypeInstanceCreateInfo
    ici.p_application_info = raw.Of(&app)
    ici.enabled_layer_count = lay_cnt
    ici.pp_enabled_layer_names = if lay_cnt > 0 { raw.Data(lay_ptrs[..]) } else { raw.Null[*u8]() }
    ici.enabled_extension_count = ext_cnt
    ici.pp_enabled_extension_names = if ext_cnt > 0 { raw.Data(ext_ptrs[..]) } else { raw.Null[*u8]() }

    var inst: vulkan.VkInstance = raw.Null[vulkan.VkInstanceT]()
    unsafe "app, ici, ext_ptrs and lay_ptrs live across vkCreateInstance" {
        r := gd.vk_create_instance.Must()(raw.Of(&ici), raw.Null[vulkan.VkAllocationCallbacks](), raw.OfMut(mut inst))
        if r != vulkan.VkSuccess {
            match ring_alloc {
                some(ra) { FreeDebugRing(ra, ring_ptr) }
                none {}
            }
            fail MapVkError(r, CallCreateInstance)
        }
    }

    inst_d := LoadInstanceDispatch(gipa, inst, sup.has_surface, sup.has_debug) else {
        unsafe "inst was just created; vkDestroyInstance cleans up" {
            p_dest := raw.FnFromAddr[vulkan.PfnVkDestroyInstance](LoadInstanceProcAddr(gipa, inst, "vkDestroyInstance\x00"))
            match p_dest {
                some(df) { df(inst, raw.Null[vulkan.VkAllocationCallbacks]()) }
                none {}
            }
        }
        match ring_alloc {
            some(ra) { FreeDebugRing(ra, ring_ptr) }
            none {}
        }
        fail Unsupported
    }

    var messenger: u64 = 0
    if sup.has_debug && ring_ptr != 0 {
        match inst_d.vk_create_debug_utils_messenger_ext {
            some(cm) {
                unsafe "inst is live and DebugMessengerCallback + ring_ptr remain valid until vkDestroyDebugUtilsMessengerEXT" {
                    var mci := mem.Zeroed[vulkan.VkDebugUtilsMessengerCreateInfoExt]()
                    mci.s_type = vulkan.VkStructureTypeDebugUtilsMessengerCreateInfoExt
                    mci.message_severity = Cast[u32](vulkan.VkDebugUtilsMessageSeverityWarningBitExt) | Cast[u32](vulkan.VkDebugUtilsMessageSeverityErrorBitExt)
                    mci.message_type = Cast[u32](vulkan.VkDebugUtilsMessageTypeGeneralBitExt) | Cast[u32](vulkan.VkDebugUtilsMessageTypeValidationBitExt) | Cast[u32](vulkan.VkDebugUtilsMessageTypePerformanceBitExt)
                    mci.pfn_user_callback = some(DebugMessengerCallback)
                    mci.p_user_data = raw.FromAddr[void](ring_ptr)
                    var msgr: vulkan.VkDebugUtilsMessengerExt = raw.Null[vulkan.VkDebugUtilsMessengerExtT]()
                    if cm(inst, raw.Of(&mci), raw.Null[vulkan.VkAllocationCallbacks](), raw.OfMut(mut msgr)) == vulkan.VkSuccess {
                        messenger = HandleAddr(msgr)
                    }
                }
            }
            none {}
        }
    }

    return VkInstCreated(
        inst: HandleAddr(inst),
        gipa: gipa,
        inst_d: inst_d,
        has_surface: sup.has_surface,
        has_debug: sup.has_debug,
        messenger: messenger,
        ring_ptr: ring_ptr,
    )
}

AllocZeroedDebugRing(a: &Alloc): uint {
    unsafe "allocates zero-initialized VkDebugRing on a until FreeDebugRing" {
        p := core.AllocBytes(a, SizeOf[VkDebugRing](), AlignOf[VkDebugRing]())
        if raw.IsNull(p) {
            return 0
        }
        raw.Fill(p, 0, SizeOf[VkDebugRing]())
        return raw.Addr(p)
    }
}

FreeDebugRing(a: &Alloc, ring_ptr: uint) {
    if ring_ptr == 0 {
        return
    }
    unsafe "ring_ptr was created by AllocZeroedDebugRing on a" {
        core.FreeBytes(a, raw.FromAddr[u8](ring_ptr), SizeOf[VkDebugRing](), AlignOf[VkDebugRing]())
    }
}

DrainDebugRing(a: &Arena, ring_ptr: uint, out: mut List[DebugMessage], err_out: mut int, warn_out: mut int): OutOfMemory!void {
    if ring_ptr == 0 {
        return
    }
    unsafe "ring_ptr is a live VkDebugRing" {
        ring := raw.FromAddr[VkDebugRing](ring_ptr)
        *err_out = int((*ring).err_count.Load())
        *warn_out = int((*ring).warn_count.Load())
        for i in 0..DebugRingSlots {
            slot := raw.OfMut(mut (*ring).slots[i])
            if (*slot).state.Load() == 2 {
                n := int((*slot).len)
                var buf := try a.Slice[u8](n)
                for k in 0..n {
                    buf[k] = (*slot).text[k]
                }
                txt := raw.Str(raw.Data(buf[..]), n)
                sev := if ((*slot).severity & Cast[u32](vulkan.VkDebugUtilsMessageSeverityErrorBitExt)) != 0 {
                    2
                } else if ((*slot).severity & Cast[u32](vulkan.VkDebugUtilsMessageSeverityWarningBitExt)) != 0 {
                    1
                } else {
                    0
                }
                try out.Push(DebugMessage(severity: sev, id: (*slot).id, text: txt))
                (*slot).state.Store(0)
            }
        }
    }
}

DestroyVkInstance(a: &Alloc, inst: u64, inst_d: &InstanceDispatch, messenger: u64, ring_ptr: uint) {
    if messenger != 0 {
        match inst_d.vk_destroy_debug_utils_messenger_ext {
            some(dm) {
                unsafe "inst and messenger are live" {
                    dm(HandleFrom[vulkan.VkInstanceT](inst), HandleFrom[vulkan.VkDebugUtilsMessengerExtT](messenger), raw.Null[vulkan.VkAllocationCallbacks]())
                }
            }
            none {}
        }
    }
    if inst != 0 {
        unsafe "inst is live" {
            inst_d.vk_destroy_instance.Must()(HandleFrom[vulkan.VkInstanceT](inst), raw.Null[vulkan.VkAllocationCallbacks]())
        }
    }
    FreeDebugRing(a, ring_ptr)
}

VkFormatCodeForIndex(i: int, d24s8_fmt: i32): i32 {
    if i == TexFmtDepth24PlusStencil8Idx {
        return d24s8_fmt
    }
    return match i {
        0 { 9 }
        1 { 10 }
        2 { 13 }
        3 { 14 }
        4 { 16 }
        5 { 17 }
        6 { 20 }
        7 { 21 }
        8 { 37 }
        9 { 43 }
        10 { 38 }
        11 { 41 }
        12 { 42 }
        13 { 44 }
        14 { 50 }
        15 { 64 }
        16 { 122 }
        17 { 76 }
        18 { 83 }
        19 { 97 }
        20 { 74 }
        21 { 75 }
        22 { 95 }
        23 { 96 }
        24 { 100 }
        25 { 103 }
        26 { 109 }
        27 { 98 }
        28 { 99 }
        29 { 101 }
        30 { 107 }
        31 { 124 }
        32 { 126 }
        34 { 130 }
        35 { 133 }
        36 { 134 }
        37 { 137 }
        38 { 138 }
        39 { 139 }
        40 { 141 }
        41 { 143 }
        42 { 145 }
        _ { 146 }
    }
}

IsIntFormatIdx(i: int): bool {
    return i == 2 || i == 3 || i == 6 || i == 7 || i == 11 || i == 12 || i == 20 || i == 21 || i == 22 || i == 23 || i == 27 || i == 28 || i == 29 || i == 30
}

IsDepthFormatIdx(i: int): bool {
    return i >= 31 && i <= 34
}

IsCompressedFormatIdx(i: int): bool {
    return i >= 35
}

QueryAllFormatCaps(inst_d: &InstanceDispatch, pd: vulkan.VkPhysicalDevice, limits: &vulkan.VkPhysicalDeviceLimits): (caps: [44]VkFormatCaps, d24s8_fmt: i32) {
    var caps: [44]VkFormatCaps = [VkFormatCaps(sampled: false, filterable: false, render_target: false, blendable: false, multisample: false, resolve: false, depth_stencil: false); 44]
    var d24s8_fmt: i32 = 129 // VK_FORMAT_D24_UNORM_S8_UINT
    unsafe "pd and inst_d are live" {
        get_fp := inst_d.vk_get_physical_device_format_properties.Must()
        var fp24 := mem.Zeroed[vulkan.VkFormatProperties]()
        get_fp(pd, 129, raw.OfMut(mut fp24))
        need_ds := Cast[u32](vulkan.VkFormatFeatureDepthStencilAttachmentBit) | Cast[u32](vulkan.VkFormatFeatureSampledImageBit)
        if (fp24.optimal_tiling_features & need_ds) != need_ds {
            d24s8_fmt = 130 // Fallback to VK_FORMAT_D32_SFLOAT_S8_UINT
        }
        ms_color := (limits.framebuffer_color_sample_counts & Cast[u32](vulkan.VkSampleCount4Bit)) != 0
        ms_depth := (limits.framebuffer_depth_sample_counts & Cast[u32](vulkan.VkSampleCount4Bit)) != 0
        ms_samp := (limits.sampled_image_color_sample_counts & Cast[u32](vulkan.VkSampleCount4Bit)) != 0
        for i in 0..TexFmtCount {
            vkf := VkFormatCodeForIndex(i, d24s8_fmt)
            var fp := mem.Zeroed[vulkan.VkFormatProperties]()
            get_fp(pd, vkf, raw.OfMut(mut fp))
            opt := fp.optimal_tiling_features
            samp := (opt & Cast[u32](vulkan.VkFormatFeatureSampledImageBit)) != 0
            filt := (opt & Cast[u32](vulkan.VkFormatFeatureSampledImageFilterLinearBit)) != 0
            rt := (opt & Cast[u32](vulkan.VkFormatFeatureColorAttachmentBit)) != 0
            bl := (opt & Cast[u32](vulkan.VkFormatFeatureColorAttachmentBlendBit)) != 0
            ds := (opt & Cast[u32](vulkan.VkFormatFeatureDepthStencilAttachmentBit)) != 0
            is_d := IsDepthFormatIdx(i)
            is_c := IsCompressedFormatIdx(i)
            is_i := IsIntFormatIdx(i)
            ms := !is_c && ((is_d && ds && ms_depth) || (!is_d && ((rt && ms_color) || (!rt && samp && ms_samp))))
            res := rt && !is_d && !is_i
            caps[i] = VkFormatCaps(sampled: samp, filterable: filt, render_target: rt, blendable: bl, multisample: ms, resolve: res, depth_stencil: ds)
        }
    }
    return (caps: caps, d24s8_fmt: d24s8_fmt)
}

CheckDeviceExtensions(inst_d: &InstanceDispatch, pd: vulkan.VkPhysicalDevice): (has_swapchain: bool, has_depth_clip: bool) {
    var has_swapchain := false
    var has_depth_clip := false
    unsafe "pd and inst_d are live" {
        enum_ext := inst_d.vk_enumerate_device_extension_properties.Must()
        var cnt: u32 = 0
        if enum_ext(pd, raw.Null[u8](), raw.OfMut(mut cnt), raw.Null[vulkan.VkExtensionProperties]()) == vulkan.VkSuccess && cnt > 0 {
            n := if int(cnt) > 512 { 512 } else { int(cnt) }
            var exts: [512]vulkan.VkExtensionProperties = [mem.Zeroed[vulkan.VkExtensionProperties](); 512]
            var got: u32 = Trunc[u32](n)
            _ = enum_ext(pd, raw.Null[u8](), raw.OfMut(mut got), raw.DataMut(mut exts[..]))
            for i in 0..int(got) {
                ep := raw.Data(exts[i].extension_name[..])
                if CStrEq(ep, "VK_KHR_swapchain") { has_swapchain = true }
                if CStrEq(ep, "VK_EXT_depth_clip_control") { has_depth_clip = true }
            }
        }
    }
    return (has_swapchain: has_swapchain, has_depth_clip: has_depth_clip)
}

struct PdProbe {
    pd: u64
    queue_family: u32
    has_swapchain: bool
    has_depth_clip: bool
    d24s8_fmt: i32
    name_bytes: [256]u8
    name_len: int
    desc: AdapterDesc
}

MinInt(a: int, b: int): int {
    return if a < b { a } else { b }
}

ProbePhysicalDevice(inst_d: &InstanceDispatch, pd: vulkan.VkPhysicalDevice, idx: int): ?PdProbe {
    unsafe "pd and inst_d are live" {
        var idp := mem.Zeroed[vulkan.VkPhysicalDeviceIdProperties]()
        idp.s_type = vulkan.VkStructureTypePhysicalDeviceIdProperties
        var props2 := mem.Zeroed[vulkan.VkPhysicalDeviceProperties2]()
        props2.s_type = vulkan.VkStructureTypePhysicalDeviceProperties2
        props2.p_next = raw.PtrCast[vulkan.VkPhysicalDeviceIdProperties, void](raw.Of(&idp))
        inst_d.vk_get_physical_device_properties2.Must()(pd, raw.OfMut(mut props2))

        p := &props2.properties
        if p.api_version < vulkan.VkApiVersion13 {
            return none
        }

        var f11 := mem.Zeroed[vulkan.VkPhysicalDeviceVulkan11Features]()
        f11.s_type = vulkan.VkStructureTypePhysicalDeviceVulkan11Features
        var f12 := mem.Zeroed[vulkan.VkPhysicalDeviceVulkan12Features]()
        f12.s_type = vulkan.VkStructureTypePhysicalDeviceVulkan12Features
        f12.p_next = raw.PtrCast[vulkan.VkPhysicalDeviceVulkan11Features, void](raw.Of(&f11))
        var f13 := mem.Zeroed[vulkan.VkPhysicalDeviceVulkan13Features]()
        f13.s_type = vulkan.VkStructureTypePhysicalDeviceVulkan13Features
        f13.p_next = raw.PtrCast[vulkan.VkPhysicalDeviceVulkan12Features, void](raw.Of(&f12))
        var feats2 := mem.Zeroed[vulkan.VkPhysicalDeviceFeatures2]()
        feats2.s_type = vulkan.VkStructureTypePhysicalDeviceFeatures2
        feats2.p_next = raw.PtrCast[vulkan.VkPhysicalDeviceVulkan13Features, void](raw.Of(&f13))
        inst_d.vk_get_physical_device_features2.Must()(pd, raw.OfMut(mut feats2))

        if f12.timeline_semaphore == 0 || f13.dynamic_rendering == 0 || f13.synchronization2 == 0 {
            return none
        }
        if feats2.features.independent_blend == 0 || feats2.features.image_cube_array == 0 {
            return none
        }

        var qf_cnt: u32 = 0
        inst_d.vk_get_physical_device_queue_family_properties.Must()(pd, raw.OfMut(mut qf_cnt), raw.Null[vulkan.VkQueueFamilyProperties]())
        if qf_cnt == 0 {
            return none
        }
        qn := if int(qf_cnt) > 32 { 32 } else { int(qf_cnt) }
        var qfs: [32]vulkan.VkQueueFamilyProperties = [mem.Zeroed[vulkan.VkQueueFamilyProperties](); 32]
        var qgot: u32 = Trunc[u32](qn)
        inst_d.vk_get_physical_device_queue_family_properties.Must()(pd, raw.OfMut(mut qgot), raw.DataMut(mut qfs[..]))
        var qfam: i32 = -1
        req_q := Cast[u32](vulkan.VkQueueGraphicsBit) | Cast[u32](vulkan.VkQueueComputeBit)
        for qi in 0..int(qgot) {
            if (qfs[qi].queue_flags & req_q) == req_q {
                qfam = Trunc[i32](qi)
                break
            }
        }
        if qfam < 0 {
            return none
        }

        exts := CheckDeviceExtensions(inst_d, pd)
        qfc := QueryAllFormatCaps(inst_d, pd, &p.limits)
        caps := qfc.caps
        d24s8_fmt := qfc.d24s8_fmt

        var luid: u64 = 0
        if idp.device_luid_valid != 0 {
            for b in 0..8 {
                luid |= u64(idp.device_luid[b]) << Cast[u64](b * 8)
            }
        } else {
            luid = (u64(p.vendor_id) << 32) | u64(p.device_id) | (Cast[u64](idx + 1) << 48)
        }

        var name_len := 0
        var name_bytes: [256]u8 = [0; 256]
        for name_len < 256 && p.device_name[name_len] != 0 {
            name_bytes[name_len] = p.device_name[name_len]
            name_len += 1
        }
        name := ""

        kind := match p.device_type {
            vulkan.VkPhysicalDeviceTypeDiscreteGpu { KindDiscrete }
            vulkan.VkPhysicalDeviceTypeIntegratedGpu { KindIntegrated }
            vulkan.VkPhysicalDeviceTypeCpu { KindSoftware }
            _ { KindOther }
        }

        f32_filt := caps[24].filterable && caps[25].filterable && caps[26].filterable
        feats := VkFeatures(
            texture_compression_bc: feats2.features.texture_compression_bc != 0,
            depth_clip_control: exts.has_depth_clip,
            float32_filterable: f32_filt,
            sampler_anisotropy: feats2.features.sampler_anisotropy != 0,
            debug_layer: false,
        )

        lim := &p.limits
        vlim := VkLimits(
            max_texture_size: MinInt(16384, int(lim.max_image_dimension2d)),
            max_texture_layers: MinInt(256, int(lim.max_image_array_layers)),
            max_bind_groups: MinInt(4, int(lim.max_bound_descriptor_sets)),
            max_bindings_per_group: 16,
            max_dynamic_uniforms: MinInt(4, int(lim.max_descriptor_set_uniform_buffers_dynamic)),
            max_immediates: MinInt(128, int(lim.max_push_constants_size)),
            max_vertex_buffers: MinInt(16, int(lim.max_vertex_input_bindings)),
            max_vertex_attributes: MinInt(16, int(lim.max_vertex_input_attributes)),
            max_vertex_stride: MinInt(2048, int(lim.max_vertex_input_binding_stride)),
            max_color_targets: MinInt(8, int(lim.max_color_attachments)),
            uniform_offset_align: 256,
            storage_offset_align: 256,
            max_uniform_binding: MinInt(65536, int(lim.max_uniform_buffer_range)),
            max_storage_binding: MinInt(134217728, int(lim.max_storage_buffer_range)),
            max_buffer_size: 268435456,
            max_uniforms_per_stage: MinInt(12, int(lim.max_per_stage_descriptor_uniform_buffers)),
            max_storage_per_stage: MinInt(8, int(lim.max_per_stage_descriptor_storage_buffers)),
            max_textures_per_stage: MinInt(16, int(lim.max_per_stage_descriptor_sampled_images)),
            max_samplers_per_stage: MinInt(16, int(lim.max_per_stage_descriptor_samplers)),
            max_sampler_descriptors: MinInt(2048, int(lim.max_descriptor_set_samplers)),
            max_workgroup_size_x: MinInt(1024, int(lim.max_compute_work_group_size[0])),
            max_workgroup_size_y: MinInt(1024, int(lim.max_compute_work_group_size[1])),
            max_workgroup_size_z: MinInt(64, int(lim.max_compute_work_group_size[2])),
            max_workgroup_invocations: MinInt(1024, int(lim.max_compute_work_group_invocations)),
            max_workgroups_per_dimension: MinInt(65535, int(lim.max_compute_work_group_count[0])),
            max_workgroup_storage: MinInt(32768, int(lim.max_compute_shared_memory_size)),
        )

        return some(PdProbe(
            pd: HandleAddr(pd),
            queue_family: Cast[u32](qfam),
            has_swapchain: exts.has_swapchain,
            has_depth_clip: exts.has_depth_clip,
            d24s8_fmt: d24s8_fmt,
            name_bytes: name_bytes,
            name_len: name_len,
            desc: AdapterDesc(
                luid: luid,
                name: name,
                vendor: p.vendor_id,
                device_id: p.device_id,
                kind: kind,
                limits: vlim,
                features: feats,
                caps: caps,
                depth_bias_clamp_ok: feats2.features.depth_bias_clamp != 0,
                draw_params_ok: f11.shader_draw_parameters != 0,
                standard_sample_locations: lim.standard_sample_locations != 0,
            ),
        ))
    }
}

AdapterKindScore(kind: int, power: int): int {
    if power == PowerLowPower {
        return match kind {
            KindIntegrated { 4 }
            KindDiscrete { 3 }
            KindOther { 2 }
            _ { 1 }
        }
    }
    return match kind {
        KindDiscrete { 4 }
        KindIntegrated { 3 }
        KindOther { 2 }
        _ { 1 }
    }
}

struct EnumJob {
    gipa: vulkan.PfnVkGetInstanceProcAddr
    items: [16]?PdProbe
    count: int
    err: ?Error
}

EnumJobFn(j0: EnumJob): EnumJob {
    var j := j0
    ic := CreateVkInstanceRaw(j.gipa, none, DebugOff) else e {
        j.err = some(e)
        return j
    }
    defer {
        if ic.messenger != 0 {
            match ic.inst_d.vk_destroy_debug_utils_messenger_ext {
                some(dm) {
                    unsafe "ic.inst and ic.messenger are live" {
                        dm(HandleFrom[vulkan.VkInstanceT](ic.inst), HandleFrom[vulkan.VkDebugUtilsMessengerExtT](ic.messenger), raw.Null[vulkan.VkAllocationCallbacks]())
                    }
                }
                none {}
            }
        }
        match ic.inst_d.vk_destroy_instance {
            some(di) {
                unsafe "ic.inst is live" {
                    di(HandleFrom[vulkan.VkInstanceT](ic.inst), raw.Null[vulkan.VkAllocationCallbacks]())
                }
            }
            none {}
        }
    }
    unsafe "ic.inst and ic.inst_d are live" {
        inst := HandleFrom[vulkan.VkInstanceT](ic.inst)
        enum_pd := ic.inst_d.vk_enumerate_physical_devices.Must()
        var cnt: u32 = 0
        r := enum_pd(inst, raw.OfMut(mut cnt), raw.Null[vulkan.VkPhysicalDevice]())
        if r != vulkan.VkSuccess || cnt == 0 {
            return j
        }
        n := if int(cnt) > 16 { 16 } else { int(cnt) }
        var pds: [16]vulkan.VkPhysicalDevice = [raw.Null[vulkan.VkPhysicalDeviceT](); 16]
        var got: u32 = Trunc[u32](n)
        _ = enum_pd(inst, raw.OfMut(mut got), raw.DataMut(mut pds[..]))
        for i in 0..int(got) {
            if j.count >= 16 {
                break
            }
            match ProbePhysicalDevice(&ic.inst_d, pds[i], i) {
                some(pr) {
                    j.items[j.count] = some(pr)
                    j.count += 1
                }
                none {}
            }
        }
    }
    return j
}

/// Enumerates qualifying Vulkan 1.3 physical devices on this host.
export EnumAdapters(a: &Arena, io: Io): Error!List[AdapterDesc] from a {
    var out := List[AdapterDesc].New(a)
    gipa := LoadVkGetInstanceProcAddr(io)
    if gipa.IsNone() {
        return out
    }
    var job := EnumJob(gipa: gipa, items: [none; 16], count: 0, err: none)
    job = try runtime.Blocking(io, job, EnumJobFn)
    match job.err {
        some(e) { fail e }
        none {}
    }
    for i in 0..job.count {
        pr := job.items[i].Must()
        var d := pr.desc
        nlen := pr.name_len
        dup := try a.Slice[u8](nlen)
        for k in 0..nlen {
            dup[k] = pr.name_bytes[k]
        }
        unsafe "dup lives on Arena a" {
            d.name = raw.Str(raw.Data(dup[..]), nlen)
        }
        try out.Push(d)
    }
    for i in 1..out.Len() {
        cur := out[i]
        sc := AdapterKindScore(cur.kind, PowerDefault)
        var j := i - 1
        for j >= 0 && AdapterKindScore(out[j].kind, PowerDefault) < sc {
            out[j + 1] = out[j]
            j -= 1
        }
        out[j + 1] = cur
    }
    return out
}
