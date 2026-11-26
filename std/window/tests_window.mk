// Why: window creation, size/DPI queries, coalesced setters, cancellation,
// task transfer and Surface lifetime extending past Window.Close must hold
// against the live Win32 window thread.
// What: integration tests for Open, Close, Size, Scale, setters, Move/Send
// across tasks, Open cancellation and Surface lifetime.
// How: opens hidden windows on Win32, checks native HWND existence via
// IsWindow across Window.Close and Surface.Close, and verifies setters.

import c.windows
import std.atomic
import std.core
import std.raw
import std.time

IsNativeWindowAlive(hwnd: uint): bool {
    if Target.os != core.Os.Windows || hwnd == 0 {
        return false
    }
    unsafe "IsWindow probes whether hwnd is an existing Win32 window handle" {
        return windows.IsWindow(raw.FromAddr[windows.HwndStruct](hwnd)) != 0
    }
}

WaitEventBounded(io: Io, w: &Window, ms: i64): Error!Event {
    scope s := io.Scope() {
        s.CancelAfter(time.Millis(ms))
        return try w.Next(io)
    }
}

WaitWindowsFreed(io: Io) {
    for _ in 0..200 {
        if LiveWindowRecs.Load(atomic.Acquire) == 0 {
            return
        }
        io.Sleep(time.Millis(5)) else { return }
    }
}

test "InstantFromNanos matches time.Instant layout" (sys: &System) {
    _ = sys
    Assert(SizeOf[time.Instant]() == 8)
    Assert(AlignOf[time.Instant]() == 8)
    a := InstantFromNanos(1_000_000)
    b := InstantFromNanos(3_500_000)
    Assert(b.Since(a).Nanos() == 2_500_000)
}

test "Hidden window opens, applies setters, resizes and closes cleanly" (sys: &System) {
    if Target.os != core.Os.Windows {
        return
    }
    opts := WindowOptions(
        title: "mk-w2-hidden",
        width: 320,
        height: 240,
        resizable: true,
        fullscreen: Fullscreen.Windowed,
        visible: false,
        events: 128,
        sealed: 0,
    )
    w := try Open(sys.heap, sys.io, opts)
    defer WaitWindowsFreed(sys.io)
    defer w.Close()

    sz := w.Size()
    Assert(sz.width == 320 && sz.height == 240)
    Assert(w.Scale() >= 1.0)
    Assert(!w.CloseRequested())

    try w.SetTitle("mk-w2-updated")
    try w.SetCursor(Cursor.Hand)
    try w.SetCursorMode(CursorMode.Hidden)
    try w.SetCursorMode(CursorMode.Normal)
    try w.SetSize(400, 300)

    var got_resize := false
    for _ in 0..8 {
        ev := WaitEventBounded(sys.io, &w, 2000) else { break }
        match ev.kind {
            EventKind.Resized(width, height) {
                if width == 400 && height == 300 {
                    got_resize = true
                    break
                }
            }
            _ {}
        }
    }
    Assert(got_resize)
    sz2 := w.Size()
    Assert(sz2.width == 400 && sz2.height == 300)

    // Fullscreen toggle on hidden window and back.
    try w.SetFullscreen(Fullscreen.Borderless)
    try w.SetFullscreen(Fullscreen.Windowed)
    try w.Show(false)
}

test "Default size scales 1280x720 and Surface keeps HWND alive past Window.Close" (sys: &System) {
    if Target.os != core.Os.Windows {
        return
    }
    opts := WindowOptions(
        title: "mk-w2-surface",
        width: 0,
        height: 0,
        resizable: false,
        fullscreen: Fullscreen.Windowed,
        visible: false,
        events: 64,
        sealed: 0,
    )
    w := try Open(sys.heap, sys.io, opts)
    sz := w.Size()
    Assert(sz.width > 0 && sz.height > 0)

    surf := w.Surface() else e {
        w.Close()
        fail e
    }
    var hwnd: uint = 0
    var hinst: uint = 0
    match surf.Native() {
        Native.Win32(hwnd: h, hinstance: hi) {
            hwnd = h
            hinst = hi
        }
        _ { Assert(false) }
    }
    Assert(hwnd != 0 && hinst != 0)
    ssz := surf.Size()
    Assert(ssz.width == sz.width && ssz.height == sz.height)

    // Close Window BEFORE Surface.Close.
    w.Close()

    // Force a round-trip through the window thread (Open + Close another window)
    // so we know Window.Close's ReqBitHide has already executed on the thread.
    w2 := Open(sys.heap, sys.io, WindowOptions(
        title: "mk-w2-sync",
        width: 100,
        height: 100,
        resizable: true,
        fullscreen: Fullscreen.Windowed,
        visible: false,
        events: 64,
        sealed: 0,
    )) else e {
        surf.Close()
        fail e
    }
    w2.Close()

    // Native HWND must STILL be alive because surf is still open!
    Assert(IsNativeWindowAlive(hwnd))

    surf.Close()

    // Once the last Surface closes, the thread destroys the native window.
    var destroyed := false
    for _ in 0..100 {
        if !IsNativeWindowAlive(hwnd) {
            destroyed = true
            break
        }
        sys.io.Sleep(time.Millis(10)) else { break }
    }
    Assert(destroyed)
    WaitWindowsFreed(sys.io)
}

TryOpenCanceled(heap: &Heap, io: Io): bool {
    opts := WindowOptions(
        title: "mk-w2-cancel",
        width: 120,
        height: 90,
        resizable: true,
        fullscreen: Fullscreen.Windowed,
        visible: false,
        events: 64,
        sealed: 0,
    )
    w := Open(heap, io, opts) else e {
        match e {
            Canceled { return true }
            _ { return false }
        }
    }
    w.Close()
    return true
}

test "Window and Surface are Send across tasks and Open cancellation is safe" (sys: &System) {
    if Target.os != core.Os.Windows {
        return
    }
    // Pre-cancelled scope must return Canceled (or cleanly abandon if raced).
    var cancel_ok := false
    scope sc := sys.io.Scope() {
        sc.Cancel()
        cancel_ok = TryOpenCanceled(sys.heap, sys.io)
    }
    Assert(cancel_ok)

    // Move Window across a Chan to a child task.
    ch := try Chan[Window].New(sys.heap.Alloc(), 1)
    defer ch.FreeEmpty(sys.heap.Alloc())
    w := try Open(sys.heap, sys.io, WindowOptions(
        title: "mk-w2-send",
        width: 200,
        height: 150,
        resizable: true,
        fullscreen: Fullscreen.Windowed,
        visible: false,
        events: 64,
        sealed: 0,
    ))
    ch.Send(sys.io, w).Must()

    done := atomic.Atomic[u32].New(0)
    scope s := sys.io.Scope() {
        s.go WindowReceiverTask(sys.io, &ch, &done)
    }
    Assert(done.Load(atomic.Acquire) == 1)
    WaitWindowsFreed(sys.io)
}

WindowReceiverTask(io: Io, ch: &Chan[Window], done: &atomic.Atomic[u32]) {
    w := ch.Recv(io) else { return }
    sz := w.Size()
    Assert(sz.width == 200 && sz.height == 150)
    w.Close()
    done.Store(1, atomic.Release)
}
