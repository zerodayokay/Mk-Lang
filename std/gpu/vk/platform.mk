// Why: Vulkan WSI surfaces are OS-specific, so Win32 HWND/HINSTANCE types
// and vkCreateWin32SurfaceKHR must be isolated behind Target.os == Os.Windows.
//
// What: Win32SurfaceSupported, CreateWin32Surface, GetWindowClientSize, and
// CreateTestWindow / DestroyTestWindow for hardware swapchain tests.
//
// How: imports c.windows and c.vulkan only here, gating every Win32 call
// under Target.os == core.Os.Windows and loading vkCreateWin32SurfaceKHR
// through LoadInstanceProc.

import c.vulkan
import c.windows
import std.core
import std.mem
import std.raw

Win32SurfaceSupported(): bool {
    return Target.os == core.Os.Windows
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
