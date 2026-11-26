// Why: GLFW callbacks report native input and window changes on the window owner.
// What: Unix callbacks and GLFW-to-Event translation.
// How: the GLFW user pointer identifies the live record; timestamps and
// coalescing go through the same ring and held state as on Windows.

import std.atomic
import std.raw
import std.sys

BindUnixCallbacks(w: uint) {
    unsafe "the live GLFW window will run these C-ABI callbacks on its owner" {
        g := Glfw()
        _ = (*g).set_window_close_callback(w, UnixOnClose)
        _ = (*g).set_framebuffer_size_callback(w, UnixOnFramebuffer)
        _ = (*g).set_window_pos_callback(w, UnixOnPosition)
        _ = (*g).set_window_content_scale_callback(w, UnixOnScale)
        _ = (*g).set_window_focus_callback(w, UnixOnFocus)
        _ = (*g).set_window_iconify_callback(w, UnixOnIconify)
        _ = (*g).set_key_callback(w, UnixOnKey)
        _ = (*g).set_char_callback(w, UnixOnChar)
        _ = (*g).set_mouse_button_callback(w, UnixOnButton)
        _ = (*g).set_cursor_pos_callback(w, UnixOnPointer)
        _ = (*g).set_scroll_callback(w, UnixOnScroll)
        _ = (*g).set_cursor_enter_callback(w, UnixOnHover)
    }
}

UnixCallbackRec(w: uint): *WindowRec {
    unsafe "every registered callback is installed after assigning a live WindowRec user pointer" {
        return raw.FromAddr[WindowRec]((*Glfw()).get_window_user_pointer(w))
    }
}

UnixOnClose(w: uint) {
    r := UnixCallbackRec(w)
    unsafe "this callback owns the live GLFW window and its record" {
        (*Glfw()).set_window_should_close(w, 0)
        (*r).close_requested.Store(1, atomic.Release)
    }
    EmitEvent(r, sys.Now(), EventKind.CloseRequested, true)
}

UnixOnFramebuffer(w: uint, width: i32, height: i32) {
    r := UnixCallbackRec(w)
    unsafe "the callback owns the live window and its record" {
        (x: px, y: py) := UnixPixelRatio(w)
        (*r).unix_pixel_scale = [px, py]
        if (*r).minimized {
            return
        }
        (*r).width.Store(int(width), atomic.Release)
        (*r).height.Store(int(height), atomic.Release)
    }
    EmitEvent(r, sys.Now(), EventKind.Resized(width: int(width), height: int(height)), true)
}

UnixOnPosition(w: uint, x: i32, y: i32) {
    EmitEvent(UnixCallbackRec(w), sys.Now(), EventKind.Moved(x: int(x), y: int(y)), false)
}

UnixOnScale(w: uint, x: f32, y: f32) {
    _ = y
    if x <= 0.0 {
        return
    }
    r := UnixCallbackRec(w)
    unsafe "the callback owns the live window and its record" {
        (x: px, y: py) := UnixPixelRatio(w)
        (*r).unix_pixel_scale = [px, py]
        (*r).scale_bits.Store(x.Bits(), atomic.Release)
    }
    EmitEvent(r, sys.Now(), EventKind.ScaleChanged(scale: x), true)
}

UnixOnFocus(w: uint, focused: i32) {
    r := UnixCallbackRec(w)
    if focused == 0 {
        ClearHeld(r)
    }
    EmitEvent(r, sys.Now(), EventKind.Focus(on: focused != 0), true)
}

UnixOnIconify(w: uint, iconified: i32) {
    r := UnixCallbackRec(w)
    at := sys.Now()
    unsafe "the callback owns this live window record" {
        (*r).minimized = iconified != 0
        if iconified != 0 {
            (*r).width.Store(0, atomic.Release)
            (*r).height.Store(0, atomic.Release)
            EmitEvent(r, at, EventKind.Minimized, true)
            return
        }
    }
    EmitEvent(r, at, EventKind.Restored, true)
    (width, height) := UnixFramebuffer(w)
    UnixOnFramebuffer(w, width, height)
}

UnixMods(m: i32): Mods {
    return Mods(shift: (m & 1) != 0, ctrl: (m & 2) != 0, alt: (m & 4) != 0, meta: (m & 8) != 0)
}

UnixOnKey(w: uint, code: i32, scancode: i32, action: i32, mods: i32) {
    r := UnixCallbackRec(w)
    key := UnixKeyCode(code)
    layout := UnixKeyLayout(key, code, scancode)
    down := action != 0
    SetHeldKey(r, key, down)
    EmitEvent(r, sys.Now(), EventKind.Key(
        key: key,
        layout: layout,
        scancode: if scancode >= 0 { Trunc[u32](scancode) } else { 0 },
        down: down,
        repeat: action == 2,
        mods: UnixMods(mods),
    ), true)
}

UnixOnChar(w: uint, code: u32) {
    if code < 32 || (code >= 127 && code <= 159) || (code >= 0xD800 && code <= 0xDFFF) || code > 0x10FFFF {
        return
    }
    EmitEvent(UnixCallbackRec(w), sys.Now(), EventKind.Text(ch: Cast[char](code)), true)
}

UnixOnButton(w: uint, button: i32, action: i32, mods: i32) {
    if button < 0 || button >= 5 {
        return
    }
    r := UnixCallbackRec(w)
    b := ButtonFromCode(int(button))
    down := action != 0
    var x: f64 = 0.0
    var y: f64 = 0.0
    unsafe "the callback queries the same live window it owns for the two cursor coordinates" {
        (*Glfw()).get_cursor_pos(w, raw.OfMut(mut x), raw.OfMut(mut y))
        x *= (*r).unix_pixel_scale[0]
        y *= (*r).unix_pixel_scale[1]
    }
    SetHeldButton(r, b, down)
    EmitEvent(r, sys.Now(), EventKind.Button(button: b, down: down, x: Cast[f32](x), y: Cast[f32](y), mods: UnixMods(mods)), true)
}

UnixOnPointer(w: uint, x: f64, y: f64) {
    r := UnixCallbackRec(w)
    unsafe "this callback owns the cached framebuffer-to-window ratio" {
        EmitEvent(r, sys.Now(), EventKind.Pointer(
            x: Cast[f32](x * (*r).unix_pixel_scale[0]),
            y: Cast[f32](y * (*r).unix_pixel_scale[1]),
        ), false)
    }
}

UnixOnScroll(w: uint, x: f64, y: f64) {
    EmitEvent(UnixCallbackRec(w), sys.Now(), EventKind.Wheel(dx: Cast[f32](x), dy: Cast[f32](y)), false)
}

UnixOnHover(w: uint, inside: i32) {
    EmitEvent(UnixCallbackRec(w), sys.Now(), EventKind.Hover(inside: inside != 0), false)
}
