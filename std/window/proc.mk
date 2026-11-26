// Why: Win32 delivers window lifecycle, DPI, keyboard, text, mouse and modal
// move/resize messages to a window procedure on the window thread; proc.mk
// translates them into timestamped Events, updates the atomic held bitmap,
// and flushes the coalescing accumulator on timers and non-coalescing input.
//
// What: AppWndProc and its per-message helpers (keyboard, surrogate-pair
// WM_CHAR, mouse capture/hover/wheel, DPI/size/move, modal timer, WM_CLOSE).
//
// How: GWLP_USERDATA points to the live WindowRec while the window exists on
// the window thread; sys.Now provides monotonic QPC timestamps; non-coalescing
// events and Resized flush immediately and wake the task's Waker.

import c.windows
import std.atomic
import std.core
import std.mem
import std.raw
import std.sys

// ReadMods snapshots modifier key state via GetKeyState on the window thread.
ReadMods(): Mods {
    if Target.os != core.Os.Windows {
        return Mods(shift: false, ctrl: false, alt: false, meta: false)
    }
    unsafe "GetKeyState reads thread message key state without pointers" {
        shift := windows.GetKeyState(windows.VkShift) < 0
        ctrl := windows.GetKeyState(windows.VkControl) < 0
        alt := windows.GetKeyState(windows.VkMenu) < 0
        meta := windows.GetKeyState(windows.VkLwin) < 0 || windows.GetKeyState(windows.VkRwin) < 0
        return Mods(shift: shift, ctrl: ctrl, alt: alt, meta: meta)
    }
}

// SetHeldKey updates the atomic 256-bit held-keys bitmap for k.
SetHeldKey(r: *WindowRec, k: Key, down: bool) {
    c := KeyCode(k)
    if c <= 0 || c >= 256 {
        return
    }
    w := c >> 6
    bit := u64(1) << Trunc[u64](c & 63)
    unsafe "r is a live WindowRec on the window thread" {
        if down {
            _ = (*r).held_keys[w].FetchOr(bit, atomic.Release)
        } else {
            _ = (*r).held_keys[w].FetchAnd(bit ^ 0xFFFFFFFFFFFFFFFF, atomic.Release)
        }
    }
}

// SetHeldButton updates the atomic held-buttons mask for b.
SetHeldButton(r: *WindowRec, b: Button, down: bool) {
    c := ButtonCode(b)
    if c < 0 || c >= 8 {
        return
    }
    bit := u8(1) << Trunc[u8](c)
    unsafe "r is a live WindowRec on the window thread" {
        if down {
            _ = (*r).held_buttons.FetchOr(bit, atomic.Release)
        } else {
            _ = (*r).held_buttons.FetchAnd(bit ^ 0xFF, atomic.Release)
        }
    }
}

// ClearHeld clears all held keys and buttons (called on WM_KILLFOCUS).
ClearHeld(r: *WindowRec) {
    unsafe "r is a live WindowRec on the window thread" {
        for i in 0..4 {
            (*r).held_keys[i].Store(0, atomic.Release)
        }
        (*r).held_buttons.Store(0, atomic.Release)
        (*r).buttons_down_mask = 0
    }
}

// EmitEvent records kind into rec's accumulator, optionally flushing at once.
EmitEvent(r: *WindowRec, at_nanos: i64, kind: EventKind, flush_now: bool) {
    raw_ev := PackEventKind(at_nanos, kind)
    unsafe "r is a live WindowRec on the window thread" {
        var wrote := (*r).acc.Record(&(*r).ring, raw_ev)
        if flush_now {
            if (*r).acc.Flush(&(*r).ring) {
                wrote = true
            }
        }
        if wrote {
            (*r).event_waker.Wake()
        }
    }
}

// HandleKeyMessage translates WM_KEYDOWN/UP/SYSKEY into EventKind.Key.
HandleKeyMessage(r: *WindowRec, at_nanos: i64, msg: u32, wp: windows.Wparam, lp: windows.Lparam) {
    if Target.os != core.Os.Windows {
        return
    }
    down := msg == Trunc[u32](windows.WmKeydown) || msg == Trunc[u32](windows.WmSyskeydown)
    vk := Trunc[u32](wp & 0xFFFF)
    lp_u := Trunc[u64](lp)
    sc_raw := Trunc[u32]((lp_u >> 16) & 0xFF)
    ext := (lp_u & (u64(1) << 24)) != 0
    repeat := down && ((lp_u & (u64(1) << 30)) != 0)
    sc_ex := if ext { sc_raw | 0xE000 } else { sc_raw }

    var phys := KeyFromScancode(sc_raw, ext)
    if phys == Key.Unknown {
        phys = KeyFromVk(vk)
    }

    var layout_vk := vk
    if sc_raw != 0 {
        unsafe "MapVirtualKeyW reads the thread keyboard layout without pointers" {
            mvk := windows.MapVirtualKeyW(sc_ex, Trunc[u32](windows.MapvkVscToVkEx))
            if mvk != 0 {
                layout_vk = mvk
            } else {
                mvk2 := windows.MapVirtualKeyW(sc_raw, Trunc[u32](windows.MapvkVscToVk))
                if mvk2 != 0 {
                    layout_vk = mvk2
                }
            }
        }
    }
    layout := KeyFromVk(layout_vk)
    SetHeldKey(r, phys, down)
    EmitEvent(r, at_nanos, EventKind.Key(key: phys, layout: layout, scancode: sc_ex, down: down, repeat: repeat, mods: ReadMods()), true)
}

// HandleCharMessage decodes UTF-16 WM_CHAR code units including surrogate pairs.
HandleCharMessage(r: *WindowRec, at_nanos: i64, wp: windows.Wparam) {
    code := Trunc[u32](wp & 0xFFFF)
    unsafe "r is a live WindowRec on the window thread" {
        if code >= 0xD800 && code <= 0xDBFF {
            (*r).high_surrogate = code
            return
        }
        var cp: u32 = code
        if code >= 0xDC00 && code <= 0xDFFF {
            hi := (*r).high_surrogate
            (*r).high_surrogate = 0
            if hi == 0 {
                return
            }
            cp = 0x10000 + ((hi - 0xD800) << 10) + (code - 0xDC00)
        } else {
            (*r).high_surrogate = 0
            // Filter out ASCII and C1 control characters per EventKind.Text spec.
            if cp < 0x20 || cp == 0x7F || (cp >= 0x80 && cp <= 0x9F) {
                return
            }
        }
        EmitEvent(r, at_nanos, EventKind.Text(ch: Cast[char](cp)), true)
    }
}

// HandleMouseButton updates capture, held bitmap and emits EventKind.Button.
HandleMouseButton(r: *WindowRec, hwnd: windows.Hwnd, at_nanos: i64, b: Button, down: bool, lp: windows.Lparam) {
    if Target.os != core.Os.Windows {
        return
    }
    x := Cast[f32](int(Trunc[i16](lp & 0xFFFF)))
    y := Cast[f32](int(Trunc[i16]((lp >> 16) & 0xFFFF)))
    bc := ButtonCode(b)
    bit := u8(1) << Trunc[u8](bc)
    unsafe "r is a live WindowRec on the window thread" {
        prev := (*r).buttons_down_mask
        if down {
            (*r).buttons_down_mask = prev | bit
            if prev == 0 {
                _ = windows.SetCapture(hwnd)
            }
        } else {
            (*r).buttons_down_mask = prev & (bit ^ 0xFF)
            if (*r).buttons_down_mask == 0 {
                if raw.Same(windows.GetCapture(), hwnd) {
                    _ = windows.ReleaseCapture()
                }
            }
        }
    }
    SetHeldButton(r, b, down)
    EmitEvent(r, at_nanos, EventKind.Button(button: b, down: down, x: x, y: y, mods: ReadMods()), true)
}

// AppWndProc handles all Win32 messages for windows owned by std.window.
AppWndProc(hwnd: windows.Hwnd, msg: u32, wp: windows.Wparam, lp: windows.Lparam): windows.Lresult {
    if Target.os != core.Os.Windows {
        return 0
    }
    unsafe "GWLP_USERDATA holds the live WindowRec address while the window is open" {
        ud := windows.GetWindowLongPtrW(hwnd, windows.GwlpUserdata)
        if ud == 0 {
            return windows.DefWindowProcW(hwnd, msg, wp, lp)
        }
        r := raw.FromAddr[WindowRec](Trunc[uint](ud))
        at_nanos := sys.Now()

        if msg == Trunc[u32](windows.WmClose) {
            (*r).close_requested.Store(1, atomic.Release)
            EmitEvent(r, at_nanos, EventKind.CloseRequested, true)
            return 0
        }
        if msg == Trunc[u32](windows.WmDestroy) {
            (*r).hwnd_val.Store(0, atomic.Release)
            return 0
        }
        if msg == Trunc[u32](windows.WmSize) {
            w := int(lp & 0xFFFF)
            h := int((lp >> 16) & 0xFFFF)
            kind := Trunc[int](wp)
            if kind == int(windows.SizeMinimized) {
                (*r).width.Store(0, atomic.Release)
                (*r).height.Store(0, atomic.Release)
                if (*r).open_ready && !(*r).minimized {
                    (*r).minimized = true
                    EmitEvent(r, at_nanos, EventKind.Minimized, true)
                }
            } else {
                (*r).width.Store(w, atomic.Release)
                (*r).height.Store(h, atomic.Release)
                if (*r).open_ready {
                    if (*r).minimized {
                        (*r).minimized = false
                        EmitEvent(r, at_nanos, EventKind.Restored, true)
                    }
                    if w != (*r).last_w || h != (*r).last_h {
                        (*r).last_w = w
                        (*r).last_h = h
                        EmitEvent(r, at_nanos, EventKind.Resized(width: w, height: h), true)
                    }
                }
            }
            return 0
        }
        if msg == Trunc[u32](windows.WmMove) {
            if (*r).open_ready {
                x := int(Trunc[i16](lp & 0xFFFF))
                y := int(Trunc[i16]((lp >> 16) & 0xFFFF))
                EmitEvent(r, at_nanos, EventKind.Moved(x: x, y: y), false)
            }
            return 0
        }
        if msg == Trunc[u32](windows.WmDpichanged) {
            dpi := Trunc[int](wp & 0xFFFF)
            if dpi > 0 {
                scale := Cast[f32](dpi) / 96.0
                (*r).scale_bits.Store(scale.Bits(), atomic.Release)
                if (*r).open_ready {
                    EmitEvent(r, at_nanos, EventKind.ScaleChanged(scale: scale), false)
                }
            }
            if lp != 0 {
                rc := raw.FromAddr[windows.Rect](Trunc[uint](lp))
                flags := Trunc[u32](windows.SwpNozorder | windows.SwpNoactivate)
                _ = windows.SetWindowPos(
                    hwnd,
                    raw.Null[windows.HwndStruct](),
                    (*rc).left,
                    (*rc).top,
                    (*rc).right - (*rc).left,
                    (*rc).bottom - (*rc).top,
                    flags,
                )
            }
            return 0
        }
        if msg == Trunc[u32](windows.WmSetfocus) {
            EmitEvent(r, at_nanos, EventKind.Focus(on: true), true)
            return 0
        }
        if msg == Trunc[u32](windows.WmKillfocus) {
            if raw.Same(windows.GetCapture(), hwnd) {
                _ = windows.ReleaseCapture()
            }
            ClearHeld(r)
            EmitEvent(r, at_nanos, EventKind.Focus(on: false), true)
            return 0
        }
        if msg == Trunc[u32](windows.WmKeydown) || msg == Trunc[u32](windows.WmKeyup) {
            HandleKeyMessage(r, at_nanos, msg, wp, lp)
            return 0
        }
        if msg == Trunc[u32](windows.WmSyskeydown) || msg == Trunc[u32](windows.WmSyskeyup) {
            HandleKeyMessage(r, at_nanos, msg, wp, lp)
            return windows.DefWindowProcW(hwnd, msg, wp, lp)
        }
        if msg == Trunc[u32](windows.WmChar) {
            HandleCharMessage(r, at_nanos, wp)
            return 0
        }
        if msg == Trunc[u32](windows.WmMousemove) {
            if !(*r).hovering {
                (*r).hovering = true
                var tme := mem.Zeroed[windows.TagTrackmouseevent]()
                tme.cb_size = Trunc[u32](SizeOf[windows.TagTrackmouseevent]())
                tme.dw_flags = Trunc[u32](windows.TmeLeave)
                tme.hwnd_track = hwnd
                _ = windows.TrackMouseEvent(raw.OfMut(mut tme))
                EmitEvent(r, at_nanos, EventKind.Hover(inside: true), false)
            }
            x := Cast[f32](int(Trunc[i16](lp & 0xFFFF)))
            y := Cast[f32](int(Trunc[i16]((lp >> 16) & 0xFFFF)))
            EmitEvent(r, at_nanos, EventKind.Pointer(x: x, y: y), false)
            return 0
        }
        if msg == Trunc[u32](windows.WmMouseleave) {
            (*r).hovering = false
            EmitEvent(r, at_nanos, EventKind.Hover(inside: false), false)
            return 0
        }
        if msg == Trunc[u32](windows.WmLbuttondown) {
            HandleMouseButton(r, hwnd, at_nanos, Button.Left, true, lp)
            return 0
        }
        if msg == Trunc[u32](windows.WmLbuttonup) {
            HandleMouseButton(r, hwnd, at_nanos, Button.Left, false, lp)
            return 0
        }
        if msg == Trunc[u32](windows.WmRbuttondown) {
            HandleMouseButton(r, hwnd, at_nanos, Button.Right, true, lp)
            return 0
        }
        if msg == Trunc[u32](windows.WmRbuttonup) {
            HandleMouseButton(r, hwnd, at_nanos, Button.Right, false, lp)
            return 0
        }
        if msg == Trunc[u32](windows.WmMbuttondown) {
            HandleMouseButton(r, hwnd, at_nanos, Button.Middle, true, lp)
            return 0
        }
        if msg == Trunc[u32](windows.WmMbuttonup) {
            HandleMouseButton(r, hwnd, at_nanos, Button.Middle, false, lp)
            return 0
        }
        if msg == Trunc[u32](windows.WmXbuttondown) {
            xbtn := Trunc[int]((wp >> 16) & 0xFFFF)
            b := if xbtn == int(windows.Xbutton1) { Button.Back } else { Button.Forward }
            HandleMouseButton(r, hwnd, at_nanos, b, true, lp)
            return 1
        }
        if msg == Trunc[u32](windows.WmXbuttonup) {
            xbtn := Trunc[int]((wp >> 16) & 0xFFFF)
            b := if xbtn == int(windows.Xbutton1) { Button.Back } else { Button.Forward }
            HandleMouseButton(r, hwnd, at_nanos, b, false, lp)
            return 1
        }
        if msg == Trunc[u32](windows.WmMousewheel) {
            delta := int(Trunc[i16]((wp >> 16) & 0xFFFF))
            dy := Cast[f32](delta) / Cast[f32](windows.WheelDelta)
            EmitEvent(r, at_nanos, EventKind.Wheel(dx: 0.0, dy: dy), false)
            return 0
        }
        if msg == Trunc[u32](windows.WmMousehwheel) {
            delta := int(Trunc[i16]((wp >> 16) & 0xFFFF))
            dx := Cast[f32](delta) / Cast[f32](windows.WheelDelta)
            EmitEvent(r, at_nanos, EventKind.Wheel(dx: dx, dy: 0.0), false)
            return 0
        }
        if msg == Trunc[u32](windows.WmEntersizemove) {
            (*r).in_sizemove_flag.Store(1, atomic.Release)
            if !(*r).timer_active {
                _ = windows.SetTimer(hwnd, ModalTimerId, ModalTimerMs, raw.FnFromAddr[fn(windows.Hwnd, windows.Uint, windows.UintPtr, windows.Dword)](0))
                (*r).timer_active = true
            }
            return 0
        }
        if msg == Trunc[u32](windows.WmExitsizemove) {
            if (*r).timer_active {
                _ = windows.KillTimer(hwnd, ModalTimerId)
                (*r).timer_active = false
            }
            (*r).in_sizemove_flag.Store(0, atomic.Release)
            FlushWindow(Trunc[uint](ud))
            return 0
        }
        if msg == Trunc[u32](windows.WmTimer) && wp == ModalTimerId {
            FlushWindow(Trunc[uint](ud))
            return 0
        }
        if msg == Trunc[u32](windows.WmSetcursor) {
            if int(lp & 0xFFFF) == int(windows.Htclient) {
                if (*r).cursor_mode == CursorMode.Hidden {
                    _ = windows.SetCursor(raw.Null[windows.HiconStruct]())
                } else {
                    _ = windows.SetCursor((*r).hcursor)
                }
                return 1
            }
        }
        return windows.DefWindowProcW(hwnd, msg, wp, lp)
    }
}
