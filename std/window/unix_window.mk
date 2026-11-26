// Why: GLFW reports different logical and framebuffer sizes on high-DPI displays.
// What: Unix window creation, property changes and native lifetime.
// How: the owner consumes the shared request queue; successful creation keeps
// the thread reference until the last native-window claim is gone.

import std.atomic
import std.raw

const GlfwClientApi: i32 = 0x00022001
const GlfwResizable: i32 = 0x00020003
const GlfwVisible: i32 = 0x00020004
const GlfwCursorMode: i32 = 0x00033001
const GlfwCursorNormal: i32 = 0x00034001
const GlfwCursorHidden: i32 = 0x00034002
const GlfwPlatformError: i32 = 0x00010008
const GlfwDontCare: i32 = -1

ProcessUnixRequest(addr: uint, bits: u32) {
    if (bits & ReqBitOpen) != 0 {
        UnixOpen(addr)
    }
    if (bits & ReqBitDestroy) != 0 {
        UnixDestroy(addr)
        return
    }
    unsafe "the request holds a reference to rec and we own the GLFW window" {
        r := raw.FromAddr[WindowRec](addr)
        w := (*r).native_handle.Load(atomic.Relaxed)
        if w == 0 {
            return
        }
        if (bits & ReqBitSetters) != 0 {
            UnixSetters(r, w)
        }
        if (bits & ReqBitHide) != 0 {
            UnixFullscreen(r, w, false)
            (*Glfw()).hide_window(w)
        }
    }
}

UnixOpen(addr: uint) {
    unsafe "the open request owns a reference to rec and runs on the GLFW owner" {
        r := raw.FromAddr[WindowRec](addr)
        if (*r).open_state.Load(atomic.Acquire) == OpenAbandoned {
            ReleaseWindowRecRef(addr)
            return
        }
        g := Glfw()
        var title: [256]u8 = [0; 256]
        for i in 0..int((*r).init_title_len) {
            title[i] = (*r).init_title[i]
        }
        (*g).window_hint(GlfwClientApi, 0)
        (*g).window_hint(GlfwVisible, 0)
        (*g).window_hint(GlfwResizable, if (*r).init_resizable { 1 } else { 0 })
        w := (*g).create_window(1280, 720, raw.Data(title[..]), 0, 0)
        if w == 0 {
            code := (*g).get_error(raw.Null[*u8]())
            (*r).open_err.Store(i64(if code != 0 { code } else { GlfwPlatformError }), atomic.Release)
            (*r).open_state.Store(OpenFailed, atomic.Release)
            (*r).open_waker.Wake()
            ReleaseWindowRecRef(addr)
            return
        }
        (*r).native_handle.Store(w, atomic.Release)
        scale := UnixContentScale(w)
        (x: px, y: py) := UnixPixelRatio(w)
        width := if (*r).init_w > 0 { Cast[f64]((*r).init_w) } else { 1280.0 * f64(scale) }
        height := if (*r).init_h > 0 { Cast[f64]((*r).init_h) } else { 720.0 * f64(scale) }
        (*g).set_window_size(w, UnixLogicalSize(width, px), UnixLogicalSize(height, py))
        (*r).unix_visible = (*r).init_visible
        (*r).unix_fullscreen = (*r).init_fullscreen == Fullscreen.Borderless
        UnixFullscreen(r, w, (*r).unix_visible && (*r).unix_fullscreen)
        (width: fw, height: fh) := UnixFramebuffer(w)
        (*r).width.Store(int(fw), atomic.Release)
        (*r).height.Store(int(fh), atomic.Release)
        (*r).scale_bits.Store(UnixContentScale(w).Bits(), atomic.Release)
        (x: fx, y: fy) := UnixPixelRatio(w)
        (*r).unix_pixel_scale = [fx, fy]
        (*g).set_window_user_pointer(w, addr)
        BindUnixCallbacks(w)
        if !(*r).open_state.CompareExchange(OpenPending, OpenOk, atomic.AcqRel, atomic.Acquire).ok {
            (*g).destroy_window(w)
            (*r).native_handle.Store(0, atomic.Release)
            ReleaseWindowRecRef(addr)
            return
        }
        ThreadLinkWindow(addr)
        if (*r).init_visible {
            (*g).show_window(w)
        }
        (*r).open_waker.Wake()
    }
}

UnixDestroy(addr: uint) {
    ThreadUnlinkWindow(addr)
    unsafe "the live window and rec belong to this owner until its reference is released" {
        r := raw.FromAddr[WindowRec](addr)
        w := (*r).native_handle.Swap(0, atomic.AcqRel)
        if w != 0 {
            g := Glfw()
            (*g).destroy_window(w)
            if (*r).unix_cursor != 0 {
                (*g).destroy_cursor((*r).unix_cursor)
            }
        }
    }
    ReleaseWindowRecRef(addr)
}

UnixFramebuffer(w: uint): (width: i32, height: i32) {
    var width: i32 = 0
    var height: i32 = 0
    unsafe "the owner supplies live GLFW window and integer out parameters" {
        (*Glfw()).get_framebuffer_size(w, raw.OfMut(mut width), raw.OfMut(mut height))
    }
    return (width: width, height: height)
}

UnixContentScale(w: uint): f32 {
    var x: f32 = 1.0
    var y: f32 = 1.0
    unsafe "the owner supplies a GLFW window and two float out parameters" {
        (*Glfw()).get_window_content_scale(w, raw.OfMut(mut x), raw.OfMut(mut y))
    }
    return if x > 0.0 { x } else { 1.0 }
}

UnixPixelRatio(w: uint): (x: f64, y: f64) {
    var width: i32 = 0
    var height: i32 = 0
    unsafe "the owner supplies a GLFW window and integer out parameters" {
        (*Glfw()).get_window_size(w, raw.OfMut(mut width), raw.OfMut(mut height))
    }
    (width: fw, height: fh) := UnixFramebuffer(w)
    return (
        x: if fw > 0 && width > 0 { Cast[f64](fw) / Cast[f64](width) } else { 1.0 },
        y: if fh > 0 && height > 0 { Cast[f64](fh) / Cast[f64](height) } else { 1.0 },
    )
}

UnixLogicalSize(physical: f64, ratio: f64): i32 {
    logical := physical / ratio
    if logical < 1.0 {
        return 1
    }
    if logical >= 2147483647.0 {
        return 2147483647
    }
    return Cast[i32](logical + 0.5)
}

UnixSetters(r: *WindowRec, w: uint) {
    unsafe "the owner has a live GLFW window and its retained record" {
        g := Glfw()
        snap := (*r).setters.Take()
        if (snap.dirty & DirtyTitle) != 0 {
            var title: [256]u8 = [0; 256]
            for i in 0..int(snap.title_len) {
                title[i] = snap.title_buf[i]
            }
            (*g).set_window_title(w, raw.Data(title[..]))
        }
        if (snap.dirty & DirtyFullscreen) != 0 {
            (*r).unix_fullscreen = snap.fullscreen == Fullscreen.Borderless
        }
        if (snap.dirty & DirtyVisible) != 0 {
            (*r).unix_visible = snap.visible
        }
        UnixFullscreen(r, w, (*r).unix_visible && (*r).unix_fullscreen)
        if (snap.dirty & DirtySize) != 0 && !(*r).is_fullscreen && snap.width > 0 && snap.height > 0 {
            ratio := (*r).unix_pixel_scale
            (*g).set_window_size(w, UnixLogicalSize(Cast[f64](snap.width), ratio[0]), UnixLogicalSize(Cast[f64](snap.height), ratio[1]))
        }
        if (snap.dirty & DirtyCursor) != 0 {
            cur := (*g).create_standard_cursor(UnixCursor(snap.cursor))
            (*g).set_cursor(w, cur)
            if (*r).unix_cursor != 0 {
                (*g).destroy_cursor((*r).unix_cursor)
            }
            (*r).unix_cursor = cur
        }
        if (snap.dirty & DirtyCursorMode) != 0 {
            (*g).set_input_mode(w, GlfwCursorMode, if snap.cursor_mode == CursorMode.Hidden { GlfwCursorHidden } else { GlfwCursorNormal })
        }
        if (snap.dirty & DirtyVisible) != 0 {
            if snap.visible {
                (*g).show_window(w)
            } else {
                (*g).hide_window(w)
            }
        }
    }
}

UnixCursor(c: Cursor): i32 {
    match c {
        Cursor.Text { return 0x00036002 }
        Cursor.Crosshair { return 0x00036003 }
        Cursor.Hand { return 0x00036004 }
        Cursor.ResizeHorizontal { return 0x00036005 }
        Cursor.ResizeVertical { return 0x00036006 }
        Cursor.NotAllowed { return 0x0003600A }
        _ { return 0x00036001 }
    }
}

UnixFullscreen(r: *WindowRec, w: uint, enable: bool) {
    unsafe "the owner changes mode on its live GLFW window" {
        if enable == (*r).is_fullscreen {
            return
        }
        g := Glfw()
        if !enable {
            saved := (*r).unix_saved
            (*g).set_window_monitor(w, 0, saved[0], saved[1], saved[2], saved[3], GlfwDontCare)
            (*r).is_fullscreen = false
            return
        }
        var x: i32 = 0
        var y: i32 = 0
        var width: i32 = 0
        var height: i32 = 0
        (*g).get_window_pos(w, raw.OfMut(mut x), raw.OfMut(mut y))
        (*g).get_window_size(w, raw.OfMut(mut width), raw.OfMut(mut height))
        (*r).unix_saved = [x, y, width, height]
        monitor := UnixMonitor(x, y, width, height)
        if monitor == 0 {
            return
        }
        mode := (*g).get_video_mode(monitor)
        if !raw.IsNull(mode) {
            (*g).set_window_monitor(w, monitor, 0, 0, (*mode).width, (*mode).height, (*mode).refresh_rate)
            (*r).is_fullscreen = true
        }
    }
}

UnixMonitor(x: i32, y: i32, width: i32, height: i32): uint {
    unsafe "GLFW's monitor array and video modes stay live until the next event pump" {
        g := Glfw()
        var best := (*g).get_primary_monitor()
        var best_area: i64 = 0
        var n: i32 = 0
        monitors := (*g).get_monitors(raw.OfMut(mut n))
        for i in 0..int(n) {
            mon := raw.Read(raw.Offset(monitors, i))
            mode := (*g).get_video_mode(mon)
            if raw.IsNull(mode) {
                continue
            }
            var mx: i32 = 0
            var my: i32 = 0
            (*g).get_monitor_pos(mon, raw.OfMut(mut mx), raw.OfMut(mut my))
            area := UnixOverlap(x, width, mx, (*mode).width) * UnixOverlap(y, height, my, (*mode).height)
            if area > best_area {
                best = mon
                best_area = area
            }
        }
        return best
    }
}

UnixOverlap(a: i32, na: i32, b: i32, nb: i32): i64 {
    hi_a := i64(a) + i64(na)
    hi_b := i64(b) + i64(nb)
    hi := if hi_a < hi_b { hi_a } else { hi_b }
    lo := i64(if a > b { a } else { b })
    return if hi > lo { hi - lo } else { 0 }
}
