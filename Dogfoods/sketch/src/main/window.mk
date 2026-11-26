// Why: the windowed runs: a script posts real messages to a real window and
// draws every step through a swapchain, and draw is the interactive session.
//
// What: CmdScript, CmdDraw, Want, the event wait (Drain, Settle), the frame
// step, and the GPU checks (snapshot readback, Hidden while minimised).
//
// How: each script step posts its messages through src/plat, then takes
// events, feeding every one to the App, until the one the step caused
// arrives, within a deadline; so motion is never coalesced and the run is
// the same on any machine. A step's frame resizes the swapchain on a
// Resized event or an Outdated frame.

import std.fs
import std.gpu
import std.time
import std.window
import src.app
import src.doc
import plat
import src.script
import src.screen

// The longest wait for the event a posted message causes.
const WaitMillis: i64 = 5000

// Want is the event a posted step waits for.
enum Want {
    Nothing
    Pointer(x: int, y: int)
    Button(down: bool)
    KeyUp(key: window.Key)
    Text(ch: char)
    Resized
    Minimized
    Restored
    Focus(on: bool)
    Moved(x: int, y: int)
    Close
}

Matches(w: &Want, k: &window.EventKind): bool {
    return match *w {
        .Nothing { true }
        .Pointer(x, y) {
            match *k {
                .Pointer(x: ex, y: ey) { int(Cast[i64](ex)) == x && int(Cast[i64](ey)) == y }
                _ { false }
            }
        }
        .Button(down) {
            match *k {
                .Button(button, down: ed, x: _, y: _, mods: _) { button == window.Button.Left && ed == down }
                _ { false }
            }
        }
        .KeyUp(key) {
            match *k {
                .Key(key: ek, layout: _, scancode: _, down, repeat: _, mods: _) { ek == key && !down }
                _ { false }
            }
        }
        .Text(ch) {
            match *k {
                .Text(ch: ec) { ec == ch }
                _ { false }
            }
        }
        .Resized {
            match *k {
                .Resized(width: _, height: _) { true }
                _ { false }
            }
        }
        .Minimized {
            match *k {
                .Minimized { true }
                _ { false }
            }
        }
        .Restored {
            match *k {
                .Restored { true }
                _ { false }
            }
        }
        .Focus(on) {
            match *k {
                .Focus(on: eo) { eo == on }
                _ { false }
            }
        }
        .Moved(x, y) {
            match *k {
                .Moved(x: ex, y: ey) { ex == x && ey == y }
                _ { false }
            }
        }
        .Close {
            match *k {
                .CloseRequested { true }
                _ { false }
            }
        }
    }
}

// The position a marker message names; no real move reports it.
const MarkerX: int = 7717
const MarkerY: int = 7727

// Settle waits until every message and request queued so far is handled,
// so a size change that follows a step has reached the App.
Settle(sys: &System into ap, win: &window.Window, s: &window.Surface, ap: mut app.App, px: mut []u8, line: int): Fail!void {
    try Posted(plat.PostMarker(s, MarkerX, MarkerY), line)
    try Drain(sys, win, ap, px, Want.Moved(x: MarkerX, y: MarkerY), line)
}

// Drain feeds every queued event to the App until want arrives, or, for
// Nothing, until the queue is empty.
Drain(sys: &System into ap, win: &window.Window, ap: mut app.App, px: mut []u8, want: Want, line: int): Fail!void {
    start := time.Now(sys.io)
    for {
        if ev := win.Poll() {
            try Feed(sys, ap, px, ev.kind)
            match want {
                .Nothing {}
                _ {
                    if Matches(&want, &ev.kind) {
                        return
                    }
                }
            }
            continue
        }
        match want {
            .Nothing { return }
            _ {}
        }
        if time.Now(sys.io).Since(start).Nanos() > WaitMillis * 1000000 {
            fail StepFailed(line: line, msg: "the window did not report the event in time")
        }
        sys.io.Sleep(time.Millis(1)) else { fail StepFailed(line: line, msg: "canceled") }
    }
}

// Posted fails the step when the OS refused a message.
Posted(ok: bool, line: int): Fail!void {
    if !ok {
        fail StepFailed(line: line, msg: "the window refused the message")
    }
}

// Act posts the messages of st and returns the event to wait for.
Act(win: &window.Window, s: &window.Surface, ap: &app.App, st: &script.Step, last: mut (x: int, y: int), name: str): Fail!Want {
    line := st.line
    match st.kind {
        .Move(x, y) {
            c := ap.ToClient(doc.Point(x: x, y: y))
            *last = c
            try Posted(plat.PostMove(s, c.x, c.y), line)
            return Want.Pointer(x: c.x, y: c.y)
        }
        .Down {
            try Posted(plat.PostButton(s, true, last.x, last.y), line)
            return Want.Button(down: true)
        }
        .Up {
            try Posted(plat.PostButton(s, false, last.x, last.y), line)
            return Want.Button(down: false)
        }
        .Key(name: k) {
            pal := match k {
                .Palette(index) { index }
                _ { 0 }
            }
            vk := plat.KeyCode(name, pal)
            try Posted(plat.PostKey(s, vk, true), line)
            try Posted(plat.PostKey(s, vk, false), line)
            return Want.KeyUp(key: KeyOf(k))
        }
        .Resize(width, height) {
            (width: cw, height: ch) := win.Size()
            if cw == width && ch == height {
                return Want.Nothing
            }
            try Posted(plat.ResizeClient(s, width, height), line)
            return Want.Resized
        }
        .Minimize {
            try Posted(plat.PostSize(s, true, 0, 0), line)
            return Want.Minimized
        }
        .Restore {
            try Posted(plat.PostSize(s, false, ap.client_w, ap.client_h), line)
            return Want.Restored
        }
        .Focus(on) {
            try Posted(plat.PostFocus(s, on), line)
            return Want.Focus(on: on)
        }
        .Close {
            try Posted(plat.PostClose(s), line)
            return Want.Close
        }
        .Cursor(name: c) {
            match c {
                .Hidden { win.SetCursorMode(window.CursorMode.Hidden) else { fail StepFailed(line: line, msg: "SetCursorMode failed") } }
                _ {
                    win.SetCursorMode(window.CursorMode.Normal) else { fail StepFailed(line: line, msg: "SetCursorMode failed") }
                    shape := match c {
                        .Crosshair { window.Cursor.Crosshair }
                        .Hand { window.Cursor.Hand }
                        .Text { window.Cursor.Text }
                        _ { window.Cursor.Arrow }
                    }
                    win.SetCursor(shape) else { fail StepFailed(line: line, msg: "SetCursor failed") }
                }
            }
            return Want.Nothing
        }
        .Fullscreen(on) {
            f := if on { window.Fullscreen.Borderless } else { window.Fullscreen.Windowed }
            win.SetFullscreen(f) else { fail StepFailed(line: line, msg: "SetFullscreen failed") }
            // The window thread applies it later: the runner then settles, so its
            // Resized events reach the App before the next point is mapped.
            return Want.Nothing
        }
        .ExpectHeld(left) {
            held := win.Held().IsButtonDown(window.Button.Left)
            if held != left || ap.held_left != left {
                fail StepFailed(line: line, msg: "held state differs")
            }
            return Want.Nothing
        }
        _ { return Want.Nothing }
    }
}

// KeyName is the plat name of a script key.
KeyName(k: &script.StepKind): str {
    match *k {
        .Key(name) {
            return match name {
                .Undo { "undo" }
                .Redo { "redo" }
                .Clear { "clear" }
                .Bigger { "bigger" }
                .Smaller { "smaller" }
                .Type { "type" }
                .Escape { "escape" }
                .Backspace { "backspace" }
                .Palette(index: _) { "palette" }
            }
        }
        _ { return "" }
    }
}

// Frame draws one frame, resizing the swapchain when the window changed.
Frame(sys: &System, win: &window.Window, v: mut screen.Viewer, ap: mut app.App, resizes: mut int, line: int): Fail!screen.Shown {
    if ap.resizes != *resizes && !ap.minimized {
        *resizes = ap.resizes
        (width: w, height: h) := win.Size()
        if w > 0 && h > 0 {
            v.Resize(sys.io, w, h) else e { fail GpuFailed(sys, line, e) }
        }
    }
    var shown := v.Draw(sys.io) else e { fail GpuFailed(sys, line, e) }
    if shown == screen.Shown.Outdated {
        (width: w, height: h) := win.Size()
        if w > 0 && h > 0 {
            v.Resize(sys.io, w, h) else e { fail GpuFailed(sys, line, e) }
            shown = v.Draw(sys.io) else e { fail GpuFailed(sys, line, e) }
        }
    }
    return shown
}

// GpuFailed turns a GPU error into a step failure naming it; a misuse's
// detail goes to standard error first.
GpuFailed(sys: &System, line: int, e: gpu.Error): StepFailed | OutOfMemory {
    m := match e {
        gpu.Invalid(misuse, at) {
            _ = sys.PrintErr("sketch: GPU misuse: {m} (at {a})\n", (m: misuse, a: at)) else {}
            "GPU misuse"
        }
        gpu.NoAdapter { "no matching GPU adapter" }
        gpu.Unsupported { "unsupported on this target" }
        gpu.DeviceLost(reason: _) { "GPU device lost" }
        gpu.SurfaceLost { "window surface lost" }
        gpu.Missing(feature: _) { "missing required GPU feature" }
        OutOfMemory { return OutOfMemory }
        _ { "GPU error" }
    }
    return StepFailed(line: line, msg: m)
}

// Session is what a windowed run opens; Close ends it in order.
linear struct Session {
    win: window.Window
    surf: window.Surface
    v: screen.Viewer
}

OpenSession(sys: &System, width: int, height: int, o: WinOpts): ?Session {
    var wo := window.DefaultWindowOptions
    wo.title = "sketch"
    wo.width = width
    wo.height = height
    wo.visible = o.visible
    win := window.Open(sys.heap, sys.io, wo) else {
        _ = sys.PrintErr("sketch: cannot open a window\n") else {}
        return none
    }
    surf := win.Surface() else {
        win.Close()
        _ = sys.PrintErr("sketch: cannot open a window\n") else {}
        return none
    }
    vo := screen.ViewOptions(backend: o.backend, software: o.software, debug: o.debug, mode: o.mode)
    v := screen.NewViewer(sys.heap, sys.io, &win, width, height, vo) else e {
        msg := match GpuFailed(sys, 0, e) {
            StepFailed(line: _, msg) { msg }
            _ { "out of memory" }
        }
        _ = sys.PrintErr("sketch: {m}\n", (m: msg)) else {}
        surf.Close()
        win.Close()
        return none
    }
    return some(Session(win: win, surf: surf, v: v))
}

// CloseSession ends the session and reports leaked device objects.
CloseSession(sys: &System, s: Session): bool {
    var Session(win, surf, v) := s
    leaks := v.Close(sys.io)
    surf.Close()
    win.Close()
    if leaks.native != 0 || leaks.errors != 0 {
        _ = sys.PrintErr("sketch: leaks native={n} errors={e}\n", (n: leaks.native, e: leaks.errors)) else {}
        return false
    }
    return true
}

// Show uploads the picture when it changed and draws a frame.
Show(sys: &System, ses: mut Session, ap: mut app.App, px: []u8, changed: bool, resizes: mut int, line: int): Fail!screen.Shown {
    if changed {
        ses.v.Upload(px) else e { fail GpuFailed(sys, line, e) }
    }
    return try Frame(sys, &ses.win, mut ses.v, ap, resizes, line)
}

CmdScript(sys: &System, path: str, o: WinOpts): int {
    if !plat.Supported {
        _ = sys.PrintErr("sketch: script: unsupported on this target\n") else {}
        return ExitFail
    }
    sc := LoadScript(sys, path) else { return ExitFail }
    var ses := OpenSession(sys, sc.width, sc.height, o) else { return ExitFail }
    status := RunWindowed(sys, mut ses, &sc) else e { Failed(sys, path, e) }
    if !CloseSession(sys, ses) {
        return ExitFail
    }
    return status
}

RunWindowed(sys: &System, ses: mut Session, sc: &script.Script): Fail!int {
    var ap := app.NewApp(sys.arena, sc.width, sc.height)
    (width: cw, height: ch) := ses.win.Size()
    ap.client_w = cw
    ap.client_h = ch
    px := try sys.arena.Slice[u8](sc.width * sc.height * 4)
    var resizes := 0
    try Drain(sys, &ses.win, mut ap, px, Want.Nothing, 0)
    _ = ap.Sync(px)
    _ = try Show(sys, ses, mut ap, px[..], true, mut resizes, 0)
    var last := (x: 0, y: 0)
    var caption_len := 0
    for i in 0..sc.steps.Len() {
        st := &sc.steps[i]
        var changed := false
        done := try Common(sys, mut ap, px, st)
        if done {
            changed = ap.Sync(px)
        } else {
            want := try Act(&ses.win, &ses.surf, &ap, st, mut last, KeyName(&st.kind))
            try Drain(sys, &ses.win, mut ap, px, want, st.line)
            changed = true
            match st.kind {
                .Resize(width: _, height: _) | .Fullscreen(on: _) { try Settle(sys, &ses.win, &ses.surf, mut ap, px, st.line) }
                _ {}
            }
            match st.kind {
                .Text(s) {
                    // Each character is a message of its own.
                    for c in s.Scalars() {
                        try Posted(plat.PostChar(&ses.surf, c), st.line)
                        try Drain(sys, &ses.win, mut ap, px, Want.Text(ch: c), st.line)
                    }
                }
                _ {}
            }
        }
        if ap.closing {
            break
        }
        if ap.doc.caption.Len() != caption_len {
            caption_len = ap.doc.caption.Len()
            title := try sys.arena.Fmt("sketch - {c}", (c: ap.doc.CaptionText()))
            ses.win.SetTitle(title) else { fail StepFailed(line: st.line, msg: "SetTitle failed") }
        }
        shown := try Show(sys, ses, mut ap, px[..], changed, mut resizes, st.line)
        match st.kind {
            .Minimize {
                if shown != screen.Shown.Hidden {
                    fail StepFailed(line: st.line, msg: "a minimised window's frame was not Hidden")
                }
            }
            .Snapshot(path: _) {
                got := ses.v.Readback(sys.arena, sys.io) else e { fail GpuFailed(sys, st.line, e) }
                if got.Len() != px.Len() {
                    fail StepFailed(line: st.line, msg: "the GPU readback has the wrong size")
                }
                for k in 0..px.Len() {
                    if got[k] != px[k] {
                        _ = sys.PrintErr("sketch: the readback differs at byte {k}\n", (k)) else {}
                        fail StepFailed(line: st.line, msg: "the GPU readback differs from the canvas")
                    }
                }
            }
            _ {}
        }
    }
    return ExitOk
}

CmdDraw(sys: &System, file: str, o: WinOpts): int {
    var d := doc.NewDoc(sys.arena, 640, 480)
    if file.Len() > 0 {
        if text := ReadIfPresent(sys, file) {
            d = doc.Load(sys.arena, text) else {
                _ = sys.PrintErr("sketch: {p}: not a sketch file\n", (p: file)) else {}
                return ExitFail
            }
        }
    }
    var wo := o
    wo.visible = true
    var ses := OpenSession(sys, d.width, d.height, wo) else { return ExitFail }
    var ap := app.NewApp(sys.arena, d.width, d.height)
    ap.doc = d
    status := Interact(sys, mut ses, mut ap, file) else e { Failed(sys, file, e) }
    if !CloseSession(sys, ses) {
        return ExitFail
    }
    return status
}

ReadIfPresent(sys: &System, file: str): ?str from sys.arena {
    text := fs.ReadText(sys.arena, sys.io, file) else { return none }
    return some(text)
}

Interact(sys: &System into ap, ses: mut Session, ap: mut app.App, file: str): Fail!int {
    px := try sys.arena.Slice[u8](ap.doc.width * ap.doc.height * 4)
    (width: cw, height: ch) := ses.win.Size()
    ap.client_w = cw
    ap.client_h = ch
    var resizes := 0
    _ = ap.Sync(px)
    _ = try Show(sys, ses, ap, px[..], true, mut resizes, 0)
    ses.win.SetCursor(window.Cursor.Crosshair) else {}
    for !ap.closing {
        ev := ses.win.Next(sys.io) else { break }
        try Feed(sys, ap, px, ev.kind)
        // Draw once the queue is empty, so a burst of motion is one frame.
        try Drain(sys, &ses.win, ap, px, Want.Nothing, 0)
        shown := try Show(sys, ses, ap, px[..], true, mut resizes, 0)
        if shown == screen.Shown.Hidden {
            ses.win.Wait(sys.io) else { break }
        }
    }
    if file.Len() > 0 {
        text := try doc.Save(sys.arena, &ap.doc)
        fs.WriteAll(sys.io, file, text) else { fail StepFailed(line: 0, msg: "cannot save the file") }
    }
    return ExitOk
}
