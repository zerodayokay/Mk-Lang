// Why: Win32 message translation, ordering, monotonic timestamps, surrogate-pair
// WM_CHAR, wheel scaling, CloseRequested latch, held-key bitmap, 75% ring
// reserve and modal-loop doorbell/timer flushing must work end-to-end.
// What: synthesised Win32 message tests and SC_MOVE modal-loop test.
// How: posts Win32 messages via PostMessageW to hidden (and one small modal)
// windows, verifies event order, timestamps, Held() bitmap, and Resized
// delivery while AppWndProc is inside WM_ENTERSIZEMOVE..WM_EXITSIZEMOVE.

import c.windows
import std.atomic
import std.core
import std.raw
import std.time

PostWinMsg(hwnd: uint, msg: i32, wp: u64, lp: i64) {
    if Target.os != core.Os.Windows || hwnd == 0 {
        return
    }
    unsafe "hwnd is a live Win32 window handle" {
        _ = windows.PostMessageW(raw.FromAddr[windows.HwndStruct](hwnd), Trunc[u32](msg), wp, lp)
    }
}

GetHwnd(surf: &Surface): uint {
    match surf.Native() {
        Native.Win32(hwnd, hinstance: _) { return hwnd }
        _ { return 0 }
    }
}

WaitNextMsg(io: Io, w: &Window, ms: i64): Error!Event {
    scope s := io.Scope() {
        s.CancelAfter(time.Millis(ms))
        return try w.Next(io)
    }
}

test "Synthesised Win32 messages deliver in order with monotonic stamps, surrogates, wheel and WM_CLOSE latch" (sys: &System) {
    if Target.os != core.Os.Windows {
        return
    }
    w := try Open(sys.heap, sys.io, WindowOptions(
        title: "mk-w2-msgs",
        width: 300,
        height: 200,
        resizable: true,
        fullscreen: Fullscreen.Windowed,
        visible: false,
        events: 128,
        sealed: 0,
    ))
    defer WaitWindowsFreed(sys.io)
    defer w.Close()
    surf := try w.Surface()
    defer surf.Close()
    hwnd := GetHwnd(&surf)
    Assert(hwnd != 0)

    // KeyDown(F1, sc=0x3B, does not generate WM_CHAR in TranslateMessage),
    // WM_CHAR('a'), WM_CHAR control '\r' (ignored),
    // WM_CHAR surrogate pair U+1F600 (0xD83D, 0xDE00), KeyUp(F1),
    // LButtonDown(10, 20), LButtonUp(12, 22), Wheel(+120), HWheel(+240), Focus(true).
    lp_f1_down := i64(0x3B) << 16
    lp_f1_up := (i64(0x3B) << 16) | (i64(1) << 30) | (i64(1) << 31)
    lp_pos1 := i64(10) | (i64(20) << 16)
    lp_pos2 := i64(12) | (i64(22) << 16)
    wp_wheel := u64(120) << 16
    wp_hwheel := u64(240) << 16

    PostWinMsg(hwnd, windows.WmKeydown, 0x70, lp_f1_down)
    PostWinMsg(hwnd, windows.WmChar, 0x61, 0)
    PostWinMsg(hwnd, windows.WmChar, 0x0D, 0)
    PostWinMsg(hwnd, windows.WmChar, 0xD83D, 0)
    PostWinMsg(hwnd, windows.WmChar, 0xDE00, 0)
    PostWinMsg(hwnd, windows.WmKeyup, 0x70, lp_f1_up)
    PostWinMsg(hwnd, windows.WmLbuttondown, 1, lp_pos1)
    PostWinMsg(hwnd, windows.WmLbuttonup, 0, lp_pos2)
    PostWinMsg(hwnd, windows.WmMousewheel, wp_wheel, 0)
    PostWinMsg(hwnd, windows.WmMousehwheel, wp_hwheel, 0)
    PostWinMsg(hwnd, windows.WmSetfocus, 0, 0)

    // Expect in order: Key(F1, down), Text('a'), Text(U+1F600), Key(F1, up),
    // Button(down), Button(up), Wheel(s) summing to (2.0, 1.0), Focus(true).
    var seen := 0
    var prev_at := InstantFromNanos(0)
    var saw_emoji := false
    var sum_dx: f32 = 0.0
    var sum_dy: f32 = 0.0
    var saw_focus := false

    for !saw_focus {
        ev := try WaitNextMsg(sys.io, &w, 2000)
        if seen > 0 {
            Assert(ev.at.Since(prev_at).Nanos() >= 0)
        }
        prev_at = ev.at
        if seen == 0 {
            match ev.kind {
                EventKind.Key(key, layout: _, scancode, down, repeat, mods: _) {
                    Assert(KeyCode(key) == KeyCode(Key.F1) && scancode == 0x3B && down && !repeat)
                }
                _ { Assert(false) }
            }
        } else if seen == 1 {
            match ev.kind {
                EventKind.Text(ch) { Assert(ch == 'a') }
                _ { Assert(false) }
            }
        } else if seen == 2 {
            match ev.kind {
                EventKind.Text(ch) {
                    Assert(Cast[u32](ch) == 0x1F600)
                    saw_emoji = true
                }
                _ { Assert(false) }
            }
        } else if seen == 3 {
            match ev.kind {
                EventKind.Key(key, layout: _, scancode: _, down, repeat: _, mods: _) {
                    Assert(KeyCode(key) == KeyCode(Key.F1) && !down)
                }
                _ { Assert(false) }
            }
        } else if seen == 4 {
            match ev.kind {
                EventKind.Button(button, down, x, y, mods: _) {
                    Assert(ButtonCode(button) == ButtonCode(Button.Left) && down && x == 10.0 && y == 20.0)
                }
                _ { Assert(false) }
            }
        } else if seen == 5 {
            match ev.kind {
                EventKind.Button(button, down, x, y, mods: _) {
                    Assert(ButtonCode(button) == ButtonCode(Button.Left) && !down && x == 12.0 && y == 22.0)
                }
                _ { Assert(false) }
            }
        } else {
            match ev.kind {
                EventKind.Wheel(dx, dy) {
                    sum_dx += dx
                    sum_dy += dy
                }
                EventKind.Focus(on) {
                    Assert(on)
                    saw_focus = true
                }
                _ { Assert(false) }
            }
        }
        seen += 1
    }
    Assert(saw_emoji && sum_dx == 2.0 && sum_dy == 1.0 && saw_focus)

    // WM_CLOSE sets CloseRequested() latch, and popping CloseRequested clears it.
    PostWinMsg(hwnd, windows.WmClose, 0, 0)
    try WaitBound(sys.io, &w, 2000)
    Assert(w.CloseRequested())
    Assert(IsNativeWindowAlive(hwnd))
    close_ev := w.Poll() else {
        Assert(false)
        return
    }
    match close_ev.kind {
        EventKind.CloseRequested {}
        _ { Assert(false) }
    }
    Assert(!w.CloseRequested())
}

WaitBound(io: Io, w: &Window, ms: i64): Error!void {
    scope s := io.Scope() {
        s.CancelAfter(time.Millis(ms))
        try w.Wait(io)
    }
}

test "Full ring never drops key-up in 25 percent reserve and Held bitmap stays accurate" (sys: &System) {
    if Target.os != core.Os.Windows {
        return
    }
    w := try Open(sys.heap, sys.io, WindowOptions(
        title: "mk-w2-ring-full",
        width: 240,
        height: 180,
        resizable: true,
        fullscreen: Fullscreen.Windowed,
        visible: false,
        events: 64,
        sealed: 0,
    ))
    defer WaitWindowsFreed(sys.io)
    defer w.Close()
    surf := try w.Surface()
    defer surf.Close()
    hwnd := GetHwnd(&surf)
    Assert(hwnd != 0)

    // Press KeyA and verify Held() sees it.
    PostWinMsg(hwnd, windows.WmKeydown, 0x41, i64(0x1E) << 16)
    ev0 := try WaitNextMsg(sys.io, &w, 2000)
    match ev0.kind {
        EventKind.Key(key, layout: _, scancode: _, down, repeat: _, mods: _) {
            Assert(KeyCode(key) == KeyCode(Key.KeyA) && down)
        }
        _ { Assert(false) }
    }
    Assert(w.Held().IsDown(Key.KeyA))

    // Fill 48/64 (75%) of the ring with alternating Wheel and Move events so they
    // flush into distinct slots, leaving only the 25% reserve (16 slots).
    for i in 0..24 {
        PostWinMsg(hwnd, windows.WmMousewheel, u64(120) << 16, 0)
        PostWinMsg(hwnd, windows.WmMove, 0, i64(10 + i))
    }
    // Wait briefly for the window thread to drain those 48 events into the ring.
    sys.io.Sleep(time.Millis(40)) else {}

    // Post another non-high-priority Move (must NOT enter the 25% reserve if >= 48 used)
    // and KeyUp(KeyA) + KeyDown(KeyB) which MUST enter the 25% reserve.
    PostWinMsg(hwnd, windows.WmMove, 0, 999)
    PostWinMsg(hwnd, windows.WmKeyup, 0x41, (i64(0x1E) << 16) | (i64(1) << 31))
    PostWinMsg(hwnd, windows.WmKeydown, 0x42, i64(0x30) << 16)
    sys.io.Sleep(time.Millis(40)) else {}

    // Held() bitmap must immediately reflect KeyA up and KeyB down regardless of ring fullness.
    held := w.Held()
    Assert(!held.IsDown(Key.KeyA))
    Assert(held.IsDown(Key.KeyB))

    // Drain ring and verify KeyUp(KeyA) and KeyDown(KeyB) were both delivered.
    var saw_a_up := false
    var saw_b_down := false
    for {
        if ev := w.Poll() {
            match ev.kind {
                EventKind.Key(key, layout: _, scancode: _, down, repeat: _, mods: _) {
                    if KeyCode(key) == KeyCode(Key.KeyA) && !down {
                        saw_a_up = true
                    }
                    if KeyCode(key) == KeyCode(Key.KeyB) && down {
                        saw_b_down = true
                    }
                }
                _ {}
            }
        } else {
            break
        }
    }
    Assert(saw_a_up && saw_b_down)

    // Now test 100% overflow with 64 WM_CHARs, then KeyUp(KeyB) -> Held() clears KeyB
    // and Dropped arrives after draining.
    for _ in 0..70 {
        PostWinMsg(hwnd, windows.WmChar, 0x7A, 0)
    }
    PostWinMsg(hwnd, windows.WmKeyup, 0x42, (i64(0x30) << 16) | (i64(1) << 31))
    sys.io.Sleep(time.Millis(50)) else {}
    Assert(!w.Held().IsDown(Key.KeyB))

    var drained := 0
    for {
        if ev := w.Poll() {
            _ = ev
            drained += 1
        } else {
            break
        }
    }
    Assert(drained == 64)

    // Next event flushed must carry Dropped, followed by Text('y').
    PostWinMsg(hwnd, windows.WmChar, 0x79, 0)
    ev_after := try WaitNextMsg(sys.io, &w, 2000)
    var saw_dropped := false
    match ev_after.kind {
        EventKind.Dropped(count) {
            Assert(count > 0)
            saw_dropped = true
        }
        _ {}
    }
    Assert(saw_dropped)
    _ = try WaitNextMsg(sys.io, &w, 2000)

    // Focus(false) clears Held() bitmap.
    PostWinMsg(hwnd, windows.WmKeydown, 0x71, i64(0x3C) << 16)
    _ = try WaitNextMsg(sys.io, &w, 2000)
    Assert(w.Held().IsDown(Key.F2))
    PostWinMsg(hwnd, windows.WmKillfocus, 0, 0)
    _ = try WaitNextMsg(sys.io, &w, 2000)
    Assert(!w.Held().IsDown(Key.F2))
}

test "Modal SC_MOVE loop delivers Resized via doorbell during the loop without stalling" (sys: &System) {
    if Target.os != core.Os.Windows {
        return
    }
    w := try Open(sys.heap, sys.io, WindowOptions(
        title: "mk-w2-modal",
        width: 160,
        height: 120,
        resizable: true,
        fullscreen: Fullscreen.Windowed,
        visible: true,
        events: 128,
        sealed: 0,
    ))
    defer WaitWindowsFreed(sys.io)
    defer w.Close()
    surf := try w.Surface()
    defer surf.Close()
    hwnd := GetHwnd(&surf)
    Assert(hwnd != 0)

    // Drain any initial focus/move events.
    sys.io.Sleep(time.Millis(20)) else {}
    for {
        if ev := w.Poll() {
            _ = ev
        } else {
            break
        }
    }

    // Enter Win32 modal move loop via WM_SYSCOMMAND(SC_MOVE).
    PostWinMsg(hwnd, windows.WmSyscommand, Trunc[u64](windows.ScMove), 0)

    var entered := false
    unsafe "surf.rec is a live WindowRec" {
        r := raw.FromAddr[WindowRec](surf.rec)
        for _ in 0..100 {
            if (*r).in_sizemove_flag.Load(atomic.Acquire) != 0 {
                entered = true
                break
            }
            sys.io.Sleep(time.Millis(10)) else { break }
        }
    }
    if !entered {
        // Non-interactive window stations (e.g. headless services) skip DefWindowProc modal loops.
        try sys.Print("skip: SC_MOVE modal loop did not start in this session\n", ())
        return
    }

    // Request a resize via the HWND_MESSAGE doorbell WHILE the modal loop runs.
    try w.SetSize(240, 180)

    var got_resized_in_loop := false
    for _ in 0..30 {
        ev := WaitNextMsg(sys.io, &w, 2000) else { break }
        match ev.kind {
            EventKind.Resized(width, height) {
                if width == 240 && height == 180 {
                    unsafe "surf.rec is a live WindowRec" {
                        r := raw.FromAddr[WindowRec](surf.rec)
                        Assert((*r).in_sizemove_flag.Load(atomic.Acquire) != 0)
                    }
                    got_resized_in_loop = true
                    break
                }
            }
            _ {}
        }
    }

    // Exit the modal loop cleanly with Escape.
    PostWinMsg(hwnd, windows.WmKeydown, 0x1B, i64(0x01) << 16)
    PostWinMsg(hwnd, windows.WmKeyup, 0x1B, (i64(0x01) << 16) | (i64(1) << 31))

    var exited := false
    unsafe "surf.rec is a live WindowRec" {
        r := raw.FromAddr[WindowRec](surf.rec)
        for _ in 0..100 {
            if (*r).in_sizemove_flag.Load(atomic.Acquire) == 0 {
                exited = true
                break
            }
            sys.io.Sleep(time.Millis(10)) else { break }
        }
    }
    Assert(got_resized_in_loop)
    Assert(exited)
}
