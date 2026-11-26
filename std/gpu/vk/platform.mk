// Why: Vulkan window surfaces use the platform's native window backend.
//
// What: CreateWindowSurface, GetWindowClientSize, and
// CreateTestWindow / DestroyTestWindow for hardware swapchain tests.
//
// How: call vkCreateWin32SurfaceKHR on Windows and GLFW's Vulkan bridge on
// Linux and macOS.

import c.vulkan
import c.windows
import std.core
import std.dl
import std.mem
import std.raw
import std.rt

CreateWindowSurface(gipa: vulkan.PfnVkGetInstanceProcAddr, inst: vulkan.VkInstance, window_handle: uint, window_instance: uint): Error!u64 {
    if Target.os == core.Os.Windows {
        return try CreateWin32Surface(gipa, inst, window_instance, window_handle)
    }
    if Target.os == core.Os.Linux || Target.os == core.Os.Macos {
        return try CreateGlfwSurface(inst, window_handle)
    }
    fail Unsupported
}

// GLFW's Vulkan bridge handles Xlib, XCB, Wayland and Cocoa window surfaces.
// The window module keeps its GLFWwindow alive while the swapchain uses it.
CreateGlfwSurface(inst: vulkan.VkInstance, window_handle: uint): Error!u64 {
    if window_handle == 0 {
        fail SurfaceLost
    }
    io := rt.NativeIo()
    lib := OpenGlfwSurfaceLibrary(io) else { fail Unsupported }
    defer lib.Close(io)
    entry := lib.Symbol("glfwCreateWindowSurface") else { fail Unsupported }
    unsafe "GLFW owns window_handle for the lifetime of the Surface and the VkInstance is live" {
        create := raw.FnFromAddr[fn(vulkan.VkInstance, *void, *vulkan.VkAllocationCallbacks, *vulkan.VkSurfaceKhr): i32](raw.Addr(entry))
        var surface: vulkan.VkSurfaceKhr = raw.Null[vulkan.VkSurfaceKhrT]()
        code := create(inst, raw.FromAddr[void](window_handle), raw.Null[vulkan.VkAllocationCallbacks](), raw.OfMut(mut surface))
        if code != vulkan.VkSuccess {
            fail MapVkError(code, CallCreateSwapchain)
        }
        return HandleAddr(surface)
    }
}

OpenGlfwSurfaceLibrary(io: Io): dl.Error!dl.Library {
    if Target.os == core.Os.Linux {
        return try dl.Open(io, "libglfw.so.3", dl.Search.System)
    }
    return dl.Open(io, "libglfw.3.dylib", dl.Search.Application) else {
        dl.Open(io, "libglfw.3.dylib", dl.Search.System) else {
            dl.Open(io, "/opt/homebrew/lib/libglfw.3.dylib", dl.Search.Path) else {
                try dl.Open(io, "/usr/local/lib/libglfw.3.dylib", dl.Search.Path)
            }
        }
    }
}

CreateWin32Surface(gipa: vulkan.PfnVkGetInstanceProcAddr, inst: vulkan.VkInstance, hinstance: uint, hwnd: uint): Error!u64 {
    if Target.os != core.Os.Windows {
        fail Unsupported
    }
    if hwnd == 0 {
        fail SurfaceLost
    }
    unsafe "Target.os is Windows; vkCreateWin32SurfaceKHR is loaded from a live VkInstance" {
        pfn := raw.FnFromAddr[vulkan.PfnVkCreateWin32SurfaceKhr](LoadInstanceProcAddr(gipa, inst, "vkCreateWin32SurfaceKHR\x00"))
        match pfn {
            none { fail Unsupported }
            some(create_fn) {
                var hinst := hinstance
                if hinst == 0 {
                    hinst = raw.Addr(windows.GetModuleHandleW(raw.Null[u16]()))
                }
                var ci := mem.Zeroed[vulkan.VkWin32SurfaceCreateInfoKhr]()
                ci.s_type = vulkan.VkStructureTypeWin32SurfaceCreateInfoKhr
                ci.hinstance = raw.FromAddr[windows.HinstanceStruct](hinst)
                ci.hwnd = raw.FromAddr[windows.HwndStruct](hwnd)
                var surf: vulkan.VkSurfaceKhr = raw.Null[vulkan.VkSurfaceKhrT]()
                r := create_fn(inst, raw.Of(&ci), raw.Null[vulkan.VkAllocationCallbacks](), raw.OfMut(mut surf))
                if r != vulkan.VkSuccess {
                    fail MapVkError(r, CallCreateSwapchain)
                }
                return HandleAddr(surf)
            }
        }
    }
}

GetWindowClientSize(hwnd: uint): (width: int, height: int) {
    if Target.os != core.Os.Windows || hwnd == 0 {
        return (width: 0, height: 0)
    }
    unsafe "Target.os is Windows and hwnd is a live window handle" {
        var rc := mem.Zeroed[windows.Rect]()
        if windows.GetClientRect(raw.FromAddr[windows.HwndStruct](hwnd), raw.OfMut(mut rc)) == 0 {
            return (width: 0, height: 0)
        }
        w := int(rc.right) - int(rc.left)
        h := int(rc.bottom) - int(rc.top)
        return (width: if w > 0 { w } else { 0 }, height: if h > 0 { h } else { 0 })
    }
}

CreateTestWindow(width: int, height: int): Error!(hwnd: uint, hinstance: uint) {
    if Target.os != core.Os.Windows {
        fail Unsupported
    }
    unsafe "Target.os is Windows; uses built-in STATIC class for an offscreen test HWND" {
        hinst := windows.GetModuleHandleW(raw.Null[u16]())
        cls: [7]u16 = [u16(b'S'), u16(b'T'), u16(b'A'), u16(b'T'), u16(b'I'), u16(b'C'), 0]
        title: [3]u16 = [u16(b'v'), u16(b'k'), 0]
        style: u32 = 0x00CF0000 // WS_OVERLAPPEDWINDOW (unshown)
        hwnd := windows.CreateWindowExW(
            0,
            raw.Data(cls[..]),
            raw.Data(title[..]),
            style,
            0,
            0,
            Trunc[i32](width),
            Trunc[i32](height),
            raw.Null[windows.HwndStruct](),
            raw.Null[windows.HmenuStruct](),
            hinst,
            raw.Null[void](),
        )
        if raw.IsNull(hwnd) {
            fail SurfaceLost
        }
        return (hwnd: raw.Addr(hwnd), hinstance: raw.Addr(hinst))
    }
}

DestroyTestWindow(hwnd: uint) {
    if Target.os != core.Os.Windows || hwnd == 0 {
        return
    }
    unsafe "Target.os is Windows and hwnd was created by CreateTestWindow" {
        _ = windows.DestroyWindow(raw.FromAddr[windows.HwndStruct](hwnd))
    }
}
