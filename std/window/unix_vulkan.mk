// Why: GLFW's Cocoa Vulkan path attaches a Metal layer to an AppKit view.
// What: Vulkan surface creation on the Unix window owner.
// How: one waiting task publishes a stack record and the owner signals a
// process-wide completion word after its last access to that record.

import std.atomic
import std.core
import std.raw

struct UnixVulkanRequest {
    handle: uint
    instance: uint
    loader: uint
    supported: bool
    code: i32
    surface: uint
}

var UnixVulkanLock: Mutex[bool] = Mutex[bool].New(false)
var UnixVulkanJob: atomic.Atomic[uint] = atomic.Atomic[uint].New(0)
var UnixVulkanDone: atomic.Atomic[u32] = atomic.Atomic[u32].New(0)

/// Creates a Vulkan surface on the Unix window owner. Call in `unsafe`.
/// Keep the `Surface` that owns the GLFW handle and the `VkInstance` live
/// until the call returns. Supply the `vkGetInstanceProcAddr` address from
/// the Vulkan 1.3 loader that created the instance. On success, the caller
/// owns the resulting `VkSurfaceKHR`. Returns `Unsupported` outside native
/// Unix or if the loader differs, and otherwise returns GLFW's `VkResult`
/// and handle.
/// O(1) besides the owner wait and native call. Allocates nothing.
export CreateUnixVulkanSurface(io: Io, handle: uint, instance: uint, loader: uint): Unsupported!(code: i32, surface: uint) {
    if !UnixWindowTarget() || !core.IsNative(io) || handle == 0 || instance == 0 || loader == 0 || UnixThread.init_state.Load(atomic.Acquire) != ThreadReady {
        fail Unsupported
    }
    nc := io.NoCancel()
    guard := UnixVulkanLock.Lock(nc) else { fail Unsupported }
    defer guard.Unlock()
    var job := UnixVulkanRequest(handle: handle, instance: instance, loader: loader, supported: false, code: 0, surface: 0)
    UnixVulkanDone.Store(0, atomic.Relaxed)
    unsafe "job stays on this stack until the owner publishes completion" {
        UnixVulkanJob.Store(raw.Addr(raw.OfMut(mut job)), atomic.Release)
        UnixWake()
        for UnixVulkanDone.Load(atomic.Acquire) == 0 {
            UnixVulkanDone.Wait(nc, 0) else {}
        }
    }
    if !job.supported {
        fail Unsupported
    }
    return (code: job.code, surface: job.surface)
}

RunUnixVulkanRequest() {
    addr := UnixVulkanJob.Swap(0, atomic.AcqRel)
    if addr == 0 {
        return
    }
    unsafe "the waiting task keeps the job, instance and window live; the GLFW owner uses the C ABI on its system stack" {
        job := raw.FromAddr[UnixVulkanRequest](addr)
        g := Glfw()
        if (*g).get_instance_proc_address != 0 && (*g).create_window_surface != 0 {
            get := raw.FnFromAddr[fn(*void, *u8): uint]((*g).get_instance_proc_address)
            // Do not pass an instance to a second loader.
            if get(raw.Null[void](), raw.StrData("vkGetInstanceProcAddr\x00")) == (*job).loader {
                create := raw.FnFromAddr[fn(*void, *void, *void, **void): i32]((*g).create_window_surface)
                var surface := raw.Null[void]()
                (*job).code = create(raw.FromAddr[void]((*job).instance), raw.FromAddr[void]((*job).handle), raw.Null[void](), raw.OfMut(mut surface))
                (*job).surface = raw.Addr(surface)
                (*job).supported = true
            }
        }
    }
    UnixVulkanDone.Store(1, atomic.Release)
    UnixVulkanDone.Wake(true)
}
