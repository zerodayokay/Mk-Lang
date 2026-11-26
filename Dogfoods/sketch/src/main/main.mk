// Why: sketch's command line: a scripted run checks the program without a
// person, headless on every target or through a real window on Windows,
// and an interactive session paints with the mouse.
//
// What: Main, HelpText, CmdCheckScript (the headless step loop), Common (the
// steps that need no window), Synth (the events a headless run feeds), the
// error texts, and the export and check-shaders commands.
//
// How: a step either produces window events (posted for real by
// src/main/window.mk, made up here when headless) or acts on the program's
// state directly; both runners call the same App.Apply and App.Sync, so a
// script prints the same lines either way.

import std.fs
import std.gpu
import std.image
import std.window
import src.app
import src.doc
import src.script
import src.shaders

const ExitOk: int = 0
const ExitFail: int = 1
const ExitUsage: int = 2

const HelpText: str = """
    usage: sketch <command> [arguments] [options]

    commands:
      check-script SCRIPT         run SCRIPT against the document alone (any target)
      script SCRIPT [options]     run SCRIPT through a real window (Windows)
      draw [FILE] [options]       paint with the mouse; FILE is loaded and saved on close
      export FILE OUT.png         paint a saved FILE and write it as a PNG
      check-shaders               check the embedded shaders against their recorded hashes
      help                        print this help text

    window options:
      --backend auto|d3d12|vulkan select GPU backend (default auto)
      --warp                      use the software adapter (WARP)
      --validate                  enable the debug layer when present
      --mode fifo|mailbox|immediate  swapchain present mode (default fifo)
      --visible                   show the window during a script

    keys: drag to draw, 1-8 colours, B and V brush larger and smaller,
    U undo, R redo, C clear, Enter types a caption (Enter or Esc ends it).
    """

/// A script step whose check failed: line is the script's, msg says why.
error StepFailed(line: int, msg: str)

type Fail = StepFailed | OutOfMemory

UsageErr(sys: &System, msg: str): int {
    _ = sys.PrintErr("sketch: {m}\n", (m: msg)) else {}
    return ExitUsage
}

// Failed prints a step failure or an out-of-memory and returns ExitFail.
Failed(sys: &System, path: str, e: Fail): int {
    match e {
        StepFailed(line, msg) {
            _ = sys.PrintErr("sketch: {p}:{l}: {m}\n", (p: path, l: line, m: msg)) else {}
        }
        OutOfMemory {
            _ = sys.PrintErr("sketch: out of memory\n") else {}
        }
    }
    return ExitFail
}

// Hash paints a fresh copy of the document and returns its SHA-256.
Hash(a: &Arena, d: &doc.Doc): OutOfMemory!str from a {
    px := try a.Slice[u8](d.width * d.height * 4)
    doc.Paint(d, px)
    return doc.HashHex(a, px[..]) else { fail OutOfMemory }
}

/// Performs a step that needs no window: brush settings, files, checks and
/// print. Returns false for a step the runner must handle. Errors: Fail.
/// O(picture) for a check or file step; allocates in sys.arena.
Common(sys: &System into ap, ap: mut app.App, px: mut []u8, st: &script.Step): Fail!bool {
    line := st.line
    match st.kind {
        .Color(r, g, b) { ap.doc.SetColor(r, g, b) }
        .Brush(radius) { ap.doc.SetRadius(radius) }
        .Save(path) {
            text := try doc.Save(sys.arena, &ap.doc)
            fs.WriteAll(sys.io, path, text) else { fail StepFailed(line: line, msg: "cannot write the file") }
        }
        .Load(path) {
            text := fs.ReadText(sys.arena, sys.io, path) else { fail StepFailed(line: line, msg: "cannot read the file") }
            loaded := doc.Load(sys.arena, text) else e {
                match e {
                    doc.BadFile(line: fl, msg) {
                        _ = sys.PrintErr("sketch: {p}:{l}: {m}\n", (p: path, l: fl, m: msg)) else {}
                        fail StepFailed(line: line, msg: "the file is not a sketch file")
                    }
                    _ { fail OutOfMemory }
                }
            }
            if loaded.width != ap.doc.width || loaded.height != ap.doc.height {
                fail StepFailed(line: line, msg: "the file's canvas differs from the script's")
            }
            ap.doc = loaded
            ap.MarkFull()
        }
        .Snapshot(path) {
            png := image.EncodePng(sys.arena, doc.ToImage(&ap.doc, px[..])) else { fail StepFailed(line: line, msg: "PNG encoding failed") }
            fs.WriteAll(sys.io, path, png) else { fail StepFailed(line: line, msg: "cannot write the file") }
        }
        .ExpectCounts(strokes, undone) {
            n := ap.doc.Counts()
            if n.strokes != strokes || n.undone != undone {
                _ = sys.PrintErr("sketch: strokes {s} undone {u}\n", (s: n.strokes, u: n.undone)) else {}
                fail StepFailed(line: line, msg: "the counts differ")
            }
        }
        .ExpectHash(hex) {
            got := doc.HashHex(sys.arena, px[..]) else { fail OutOfMemory }
            if !got.Eq(&hex) {
                _ = sys.PrintErr("sketch: hash {g}\n", (g: got)) else {}
                fail StepFailed(line: line, msg: "the hash differs")
            }
        }
        .ExpectCaption(text) {
            if !ap.doc.CaptionText().Eq(&text) {
                _ = sys.PrintErr("sketch: caption \"{c}\"\n", (c: ap.doc.CaptionText())) else {}
                fail StepFailed(line: line, msg: "the caption differs")
            }
        }
        .ExpectSaved(path) {
            // What Save writes now must equal the file byte for byte.
            want := fs.ReadBytes(sys.arena, sys.io, path) else { fail StepFailed(line: line, msg: "cannot read the file") }
            got := try doc.Save(sys.arena, &ap.doc)
            if !SameBytes(got, want) {
                fail StepFailed(line: line, msg: "the document saves differently from the file")
            }
        }
        .Print {
            // The picture as painted step by step must equal a fresh paint.
            fresh := try Hash(sys.arena, &ap.doc)
            got := doc.HashHex(sys.arena, px[..]) else { fail OutOfMemory }
            if !got.Eq(&fresh) {
                fail StepFailed(line: line, msg: "the painted picture differs from a repaint")
            }
            s := try ap.Status(sys.arena, got)
            _ = sys.Print("{s}\n", (s: s)) else {}
        }
        _ { return false }
    }
    return true
}

// SameBytes reports whether a and b hold the same bytes.
SameBytes(a: []u8, b: []u8): bool {
    if a.Len() != b.Len() {
        return false
    }
    for i in 0..a.Len() {
        if a[i] != b[i] {
            return false
        }
    }
    return true
}

// KeyOf is the window key a script key presses.
KeyOf(k: script.KeyName): window.Key {
    return match k {
        .Undo { window.Key.KeyU }
        .Redo { window.Key.KeyR }
        .Clear { window.Key.KeyC }
        .Bigger { window.Key.KeyB }
        .Smaller { window.Key.KeyV }
        .Type { window.Key.Enter }
        .Escape { window.Key.Escape }
        .Backspace { window.Key.Backspace }
        .Palette(index) { PaletteKey(index) }
    }
}

PaletteKey(i: int): window.Key {
    return match i {
        0 { window.Key.Digit1 }
        1 { window.Key.Digit2 }
        2 { window.Key.Digit3 }
        3 { window.Key.Digit4 }
        4 { window.Key.Digit5 }
        5 { window.Key.Digit6 }
        6 { window.Key.Digit7 }
        _ { window.Key.Digit8 }
    }
}

const NoMods: window.Mods = window.Mods(shift: false, ctrl: false, alt: false, meta: false)

// Feed applies one event and paints what it changed.
Feed(sys: &System into ap, ap: mut app.App, px: mut []u8, k: window.EventKind): OutOfMemory!void {
    try ap.Apply(sys.arena, k)
    _ = ap.Sync(px)
}

/// Runs st headless: the events a window would report for it, made up.
/// Errors: Fail. O(1) per event.
Synth(sys: &System into ap, ap: mut app.App, px: mut []u8, st: &script.Step, last: mut (x: int, y: int)): Fail!void {
    match st.kind {
        .Move(x, y) {
            c := ap.ToClient(doc.Point(x: x, y: y))
            *last = c
            try Feed(sys, ap, px, window.EventKind.Pointer(x: Cast[f32](c.x), y: Cast[f32](c.y)))
        }
        .Down | .Up {
            down := match st.kind {
                .Down { true }
                _ { false }
            }
            try Feed(sys, ap, px, window.EventKind.Button(button: window.Button.Left, down: down, x: Cast[f32](last.x), y: Cast[f32](last.y), mods: NoMods))
        }
        .Key(name) {
            key := KeyOf(name)
            try Feed(sys, ap, px, window.EventKind.Key(key: key, layout: key, scancode: 0, down: true, repeat: false, mods: NoMods))
            try Feed(sys, ap, px, window.EventKind.Key(key: key, layout: key, scancode: 0, down: false, repeat: false, mods: NoMods))
        }
        .Text(s) {
            for ch in s.Scalars() {
                try Feed(sys, ap, px, window.EventKind.Text(ch: ch))
            }
        }
        .Resize(width, height) { try Feed(sys, ap, px, window.EventKind.Resized(width: width, height: height)) }
        .Minimize { try Feed(sys, ap, px, window.EventKind.Minimized) }
        .Restore { try Feed(sys, ap, px, window.EventKind.Restored) }
        .Focus(on) { try Feed(sys, ap, px, window.EventKind.Focus(on: on)) }
        .Close { try Feed(sys, ap, px, window.EventKind.CloseRequested) }
        // Held is the program's own record when no window keeps one.
        .ExpectHeld(left) {
            if ap.held_left != left {
                fail StepFailed(line: st.line, msg: "held state differs")
            }
        }
        // A cursor or fullscreen change has nothing to show headless.
        _ {}
    }
}

CmdCheckScript(sys: &System, args: []str): int {
    if args.Len() != 1 {
        return UsageErr(sys, "check-script requires SCRIPT")
    }
    path := args[0]
    sc := LoadScript(sys, path) else { return ExitFail }
    var ap := app.NewApp(sys.arena, sc.width, sc.height)
    px := sys.arena.Slice[u8](sc.width * sc.height * 4) else { return Failed(sys, path, OutOfMemory) }
    _ = ap.Sync(px)
    var last := (x: 0, y: 0)
    for i in 0..sc.steps.Len() {
        st := &sc.steps[i]
        done := Common(sys, mut ap, px, st) else e { return Failed(sys, path, e) }
        if done {
            // A load replaces the picture: repaint before the next step.
            _ = ap.Sync(px)
        } else {
            Synth(sys, mut ap, px, st, mut last) else e { return Failed(sys, path, e) }
        }
        if ap.closing {
            break
        }
    }
    return ExitOk
}

// LoadScript reads and parses path, printing the failure.
LoadScript(sys: &System, path: str): ?script.Script from sys.arena {
    text := fs.ReadText(sys.arena, sys.io, path) else {
        _ = sys.PrintErr("sketch: {p}: cannot read the file\n", (p: path)) else {}
        return none
    }
    sc := script.Parse(sys.arena, text) else e {
        match e {
            script.BadScript(line, msg) {
                _ = sys.PrintErr("sketch: {p}:{l}: {m}\n", (p: path, l: line, m: msg)) else {}
            }
            _ {
                _ = sys.PrintErr("sketch: out of memory\n") else {}
            }
        }
        return none
    }
    return some(sc)
}

CmdExport(sys: &System, args: []str): int {
    if args.Len() != 2 {
        return UsageErr(sys, "export requires FILE OUT.png")
    }
    text := fs.ReadText(sys.arena, sys.io, args[0]) else {
        _ = sys.PrintErr("sketch: {p}: cannot read the file\n", (p: args[0])) else {}
        return ExitFail
    }
    d := doc.Load(sys.arena, text) else e {
        match e {
            doc.BadFile(line, msg) {
                _ = sys.PrintErr("sketch: {p}:{l}: {m}\n", (p: args[0], l: line, m: msg)) else {}
            }
            _ { _ = sys.PrintErr("sketch: out of memory\n") else {} }
        }
        return ExitFail
    }
    px := sys.arena.Slice[u8](d.width * d.height * 4) else { return Failed(sys, args[0], OutOfMemory) }
    doc.Paint(&d, px)
    png := image.EncodePng(sys.arena, doc.ToImage(&d, px[..])) else {
        _ = sys.PrintErr("sketch: PNG encoding failed\n") else {}
        return ExitFail
    }
    fs.WriteAll(sys.io, args[1], png) else {
        _ = sys.PrintErr("sketch: {p}: cannot write the file\n", (p: args[1])) else {}
        return ExitFail
    }
    n := d.Counts()
    _ = sys.Print("exported {w}x{h} strokes={s}\n", (w: d.width, h: d.height, s: n.strokes)) else {}
    return ExitOk
}

/// The window options every window command takes.
struct WinOpts {
    backend: gpu.Backend
    software: bool
    debug: gpu.DebugMode
    mode: gpu.PresentMode
    visible: bool
}

// ParseWinOpts reads the options after the positional arguments; none on
// a usage error, which it prints.
ParseWinOpts(sys: &System, args: []str): ?WinOpts {
    var o := WinOpts(backend: gpu.Backend.Auto, software: false, debug: gpu.DebugMode.Off, mode: gpu.PresentMode.Fifo, visible: false)
    var i := 0
    for i < args.Len() {
        a := args[i]
        if a.Eq(&"--backend") && i + 1 < args.Len() {
            b := args[i + 1]
            if b.Eq(&"auto") {
                o.backend = gpu.Backend.Auto
            } else if b.Eq(&"d3d12") {
                o.backend = gpu.Backend.D3d12
            } else if b.Eq(&"vulkan") {
                o.backend = gpu.Backend.Vulkan
            } else {
                _ = UsageErr(sys, "unknown backend")
                return none
            }
            i += 2
        } else if a.Eq(&"--mode") && i + 1 < args.Len() {
            m := args[i + 1]
            if m.Eq(&"fifo") {
                o.mode = gpu.PresentMode.Fifo
            } else if m.Eq(&"mailbox") {
                o.mode = gpu.PresentMode.Mailbox
            } else if m.Eq(&"immediate") {
                o.mode = gpu.PresentMode.Immediate
            } else {
                _ = UsageErr(sys, "unknown present mode")
                return none
            }
            i += 2
        } else if a.Eq(&"--warp") {
            o.software = true
            i += 1
        } else if a.Eq(&"--validate") {
            o.debug = gpu.DebugMode.Validate
            i += 1
        } else if a.Eq(&"--visible") {
            o.visible = true
            i += 1
        } else {
            _ = UsageErr(sys, "unknown option")
            return none
        }
    }
    return some(o)
}

export Main(sys: &System): int {
    args := sys.args
    if args.Len() < 2 {
        _ = sys.Print("{h}", (h: HelpText)) else {}
        return ExitOk
    }
    cmd := args[1]
    rest := args[2..args.Len()]
    if cmd.Eq(&"help") || cmd.Eq(&"--help") || cmd.Eq(&"-h") {
        _ = sys.Print("{h}", (h: HelpText)) else {}
        return ExitOk
    }
    if cmd.Eq(&"check-script") {
        return CmdCheckScript(sys, rest)
    }
    if cmd.Eq(&"export") {
        return CmdExport(sys, rest)
    }
    if cmd.Eq(&"check-shaders") {
        n := shaders.VerifyAll(sys.arena) else {
            _ = sys.PrintErr("sketch: the embedded shaders do not match their hashes, or fail CheckShader\n") else {}
            return ExitFail
        }
        _ = sys.Print("shaders: {n} ok\n", (n)) else {}
        return ExitOk
    }
    if cmd.Eq(&"script") {
        if rest.Len() < 1 {
            return UsageErr(sys, "script requires SCRIPT")
        }
        o := ParseWinOpts(sys, rest[1..rest.Len()]) else { return ExitUsage }
        return CmdScript(sys, rest[0], o)
    }
    if cmd.Eq(&"draw") {
        var file := ""
        var from := 0
        if rest.Len() > 0 && !rest[0].StartsWith("--") {
            file = rest[0]
            from = 1
        }
        o := ParseWinOpts(sys, rest[from..rest.Len()]) else { return ExitUsage }
        return CmdDraw(sys, file, o)
    }
    return UsageErr(sys, "unknown command")
}
