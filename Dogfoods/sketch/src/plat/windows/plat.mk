// Why: a scripted run must exercise the real window: the messages go through
// the OS queue to std.window's window procedure, exactly as input does, so
// the program sees what a person's mouse and keyboard would produce.
//
// What: Supported, PostMove, PostButton, KeyCode, PostKey, PostChar,
// PostSize, PostFocus, PostMarker, PostClose and ResizeClient.
//
// How: PostMessageW to the window's HWND from window.Surface.Native, and
// SetWindowPos for a real resize; every call checks the handle is live and
// reports false when the OS refuses.

import c.windows
import std.raw
import std.window

/// Whether this target can post window messages.
export const Supported: bool = true

const VkUndo: u32 = 0x55
const VkRedo: u32 = 0x52
const VkClear: u32 = 0x43
const VkBigger: u32 = 0x42
const VkSmaller: u32 = 0x56
const VkEnter: u32 = 0x0D
const VkEscape: u32 = 0x1B
const VkBack: u32 = 0x08
const VkDigit1: u32 = 0x31

// Hwnd is the window's handle, 0 once it is gone.
Hwnd(s: &window.Surface): uint {
    match s.Native() {
        .Win32(hwnd, hinstance: _) { return hwnd }
        _ { return 0 }
    }
}

// Post queues one message; lp packs two 16-bit words as MAKELPARAM does.
Post(s: &window.Surface, msg: i32, wp: u64, lo: int, hi: int): bool {
    h := Hwnd(s)
    if h == 0 {
        return false
    }
    lp := i64((hi & 0xFFFF) << 16 | (lo & 0xFFFF))
    unsafe "h is the live HWND a Surface pins; PostMessageW copies its arguments" {
        return windows.PostMessageW(raw.FromAddr[windows.HwndStruct](h), Trunc[u32](msg), wp, lp) != 0
    }
}

/// Posts a pointer move to client pixel (x, y). O(1).
export PostMove(s: &window.Surface, x: int, y: int): bool {
    return Post(s, windows.WmMousemove, 0, x, y)
}

/// Posts a left-button press or release at client pixel (x, y). O(1).
export PostButton(s: &window.Surface, down: bool, x: int, y: int): bool {
    if down {
        return Post(s, windows.WmLbuttondown, Cast[u64](windows.MkLbutton), x, y)
    }
    return Post(s, windows.WmLbuttonup, 0, x, y)
}

/// The virtual-key code for a script key: undo U, redo R, clear C, bigger B,
/// smaller V, type Enter, Escape, Backspace, palette 1 to 8.
export KeyCode(name: str, palette: int): u32 {
    if name.Eq(&"undo") { return VkUndo }
    if name.Eq(&"redo") { return VkRedo }
    if name.Eq(&"clear") { return VkClear }
    if name.Eq(&"bigger") { return VkBigger }
    if name.Eq(&"smaller") { return VkSmaller }
    if name.Eq(&"type") { return VkEnter }
    if name.Eq(&"escape") { return VkEscape }
    if name.Eq(&"backspace") { return VkBack }
    return VkDigit1 + Trunc[u32](palette)
}

/// Posts a key press or release of virtual key vk, with no scan code, so the
/// key is named by vk alone. O(1).
export PostKey(s: &window.Surface, vk: u32, down: bool): bool {
    // Bits 30 and 31 of a release: the key was down and is being released.
    hi := if down { 0 } else { 0xC000 }
    msg := if down { windows.WmKeydown } else { windows.WmKeyup }
    return Post(s, msg, u64(vk), 1, hi)
}

/// Posts one character of text input; a character outside the Basic
/// Multilingual Plane is posted as its two UTF-16 halves. O(1).
export PostChar(s: &window.Surface, ch: char): bool {
    v := u32(ch)
    if v < 0x10000 {
        return Post(s, windows.WmChar, u64(v), 1, 0)
    }
    w := v - 0x10000
    return Post(s, windows.WmChar, u64(0xD800 + (w >> 10)), 1, 0) && Post(s, windows.WmChar, u64(0xDC00 + (w & 0x3FF)), 1, 0)
}

/// Posts the size change a minimise (width and height 0) or a restore to
/// width by height reports. O(1).
export PostSize(s: &window.Surface, minimized: bool, width: int, height: int): bool {
    if minimized {
        return Post(s, windows.WmSize, Cast[u64](windows.SizeMinimized), 0, 0)
    }
    return Post(s, windows.WmSize, Cast[u64](windows.SizeRestored), width, height)
}

/// Posts a focus gain or loss. O(1).
export PostFocus(s: &window.Surface, on: bool): bool {
    if on {
        return Post(s, windows.WmSetfocus, 0, 0, 0)
    }
    return Post(s, windows.WmKillfocus, 0, 0, 0)
}

/// Posts a move notification for (x, y) without moving the window: the
/// window thread handles messages in order, so once its Moved event
/// arrives, every message and request queued before it has been handled.
/// O(1).
export PostMarker(s: &window.Surface, x: int, y: int): bool {
    return Post(s, windows.WmMove, 0, x, y)
}

/// Posts a close request, as the title bar's close button does. O(1).
export PostClose(s: &window.Surface): bool {
    return Post(s, windows.WmClose, 0, 0, 0)
}

/// Resizes the window so that its client area is width by height pixels.
/// O(1): three OS calls.
export ResizeClient(s: &window.Surface, width: int, height: int): bool {
    h := Hwnd(s)
    if h == 0 {
        return false
    }
    unsafe "h is the live HWND a Surface pins; the rects are locals" {
        hwnd := raw.FromAddr[windows.HwndStruct](h)
        var wr := windows.Rect(left: 0, top: 0, right: 0, bottom: 0)
        var cr := windows.Rect(left: 0, top: 0, right: 0, bottom: 0)
        if windows.GetWindowRect(hwnd, raw.OfMut(mut wr)) == 0 || windows.GetClientRect(hwnd, raw.OfMut(mut cr)) == 0 {
            return false
        }
        // The frame is what the window rect adds to the client rect.
        fw := (wr.right - wr.left) - (cr.right - cr.left)
        fh := (wr.bottom - wr.top) - (cr.bottom - cr.top)
        flags := Trunc[u32](windows.SwpNomove | windows.SwpNozorder | windows.SwpNoactivate)
        return windows.SetWindowPos(hwnd, raw.Null[windows.HwndStruct](), 0, 0, Trunc[i32](width) + fw, Trunc[i32](height) + fh, flags) != 0
    }
}
