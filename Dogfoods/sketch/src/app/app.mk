// Why: the program's behaviour is one function of window events, so a
// scripted run and a person's session do the same thing, and a headless run
// can feed it the events a window would have produced.
//
// What: App, Dirty, Palette, NewApp, App.Apply, App.Sync, App.MarkFull,
// App.ToCanvas, App.ToClient and App.Status.
//
// How: a left drag draws a stroke in canvas pixels, mapped from the client
// area, which the canvas fills whatever the window's size; keys undo, redo,
// clear, pick colours and brush sizes; Enter starts typing the caption and
// Enter or Escape ends it, and Text events count only while typing, as a
// key press also produces text.

import std.window
import src.doc

/// Eight brush colours, keys 1 to 8.
export const Palette: [8](r: u8, g: u8, b: u8) = [
    (r: 0, g: 0, b: 0),
    (r: 220, g: 40, b: 40),
    (r: 40, g: 160, b: 60),
    (r: 40, g: 80, b: 220),
    (r: 240, g: 200, b: 30),
    (r: 160, g: 60, b: 200),
    (r: 30, g: 190, b: 200),
    (r: 255, g: 255, b: 255),
]

/// How much of the picture needs painting again.
export enum Dirty {
    Clean
    /// Only the open stroke's newest piece: doc.PaintTail.
    Tail
    /// All of it: doc.Paint.
    Full
}

/// The program's state: the document, the client area's size in pixels,
/// what is held, and the modes events switch.
export struct App {
    export doc: doc.Doc
    export client_w: int
    export client_h: int
    export held_left: bool
    export typing: bool
    export focused: bool
    export minimized: bool
    export closing: bool
    /// How much of the picture the events since the last MarkClean changed.
    export dirty: Dirty
    /// Resized events seen.
    export resizes: int
}

/// An App for a width by height canvas shown in a width by height client
/// area. O(1); allocates nothing until drawn on.
export NewApp(a: &Arena, width: int, height: int): App from a {
    return App(
        doc: doc.NewDoc(a, width, height),
        client_w: width,
        client_h: height,
        held_left: false,
        typing: false,
        focused: true,
        minimized: false,
        closing: false,
        resizes: 0,
        dirty: Dirty.Full,
    )
}

/// The canvas pixel under client position (x, y). O(1).
export App.ToCanvas(&self, x: f32, y: f32): doc.Point {
    cw := if self.client_w > 0 { self.client_w } else { 1 }
    ch := if self.client_h > 0 { self.client_h } else { 1 }
    px := int(Cast[i64](x)) * self.doc.width / cw
    py := int(Cast[i64](y)) * self.doc.height / ch
    return doc.Point(x: px, y: py)
}

/// The client pixel at the centre of canvas pixel p, which ToCanvas maps
/// back to p for any client size at least the canvas size. O(1).
export App.ToClient(&self, p: doc.Point): (x: int, y: int) {
    return (
        x: (2 * p.x + 1) * self.client_w / (2 * self.doc.width),
        y: (2 * p.y + 1) * self.client_h / (2 * self.doc.height),
    )
}

/// Applies one window event and records in dirty what it changed of the
/// picture. Errors: OutOfMemory. O(1) amortised: painting happens elsewhere.
export App.Apply(mut self, a: &Arena into self, k: window.EventKind): OutOfMemory!void {
    match k {
        .CloseRequested { self.closing = true }
        .Resized(width, height) {
            self.client_w = width
            self.client_h = height
            self.resizes += 1
        }
        .Minimized { self.minimized = true }
        .Restored { self.minimized = false }
        .Focus(on) {
            self.focused = on
            if !on {
                // Focus lost mid-drag: the release will never arrive.
                self.held_left = false
                self.doc.Cancel()
                self.dirty = Dirty.Full
            }
        }
        .Button(button, down, x, y, mods: _) {
            if button == window.Button.Left {
                self.held_left = down
                if down {
                    try self.doc.Begin(a, self.ToCanvas(x, y))
                    self.MarkTail()
                } else {
                    // The stroke ends at the last pointer position: the
                    // release adds no point, so no paint is pending.
                    self.doc.End()
                }
            }
        }
        .Pointer(x, y) {
            if self.held_left {
                try self.doc.Extend(self.ToCanvas(x, y))
                self.MarkTail()
            }
        }
        .Key(key, layout: _, scancode: _, down, repeat, mods: _) {
            if down && !repeat {
                try self.Key(a, key)
            }
        }
        .Text(ch) {
            if self.typing {
                try self.doc.Type(ch)
            }
        }
        _ {}
    }
}

App.Key(mut self, a: &Arena into self, key: window.Key): OutOfMemory!void {
    if self.typing {
        match key {
            .Enter | .Escape { self.typing = false }
            .Backspace { self.doc.Backspace() }
            _ {}
        }
        return
    }
    match key {
        .Enter { self.typing = true }
        .KeyU {
            if try self.doc.Undo() { self.dirty = Dirty.Full }
        }
        .KeyR {
            if try self.doc.Redo() { self.dirty = Dirty.Full }
        }
        .KeyC {
            try self.doc.Clear(a)
            self.dirty = Dirty.Full
        }
        .KeyB { self.doc.SetRadius(self.doc.radius + 1) }
        .KeyV { self.doc.SetRadius(self.doc.radius - 1) }
        .Digit1 { self.Pick(0) }
        .Digit2 { self.Pick(1) }
        .Digit3 { self.Pick(2) }
        .Digit4 { self.Pick(3) }
        .Digit5 { self.Pick(4) }
        .Digit6 { self.Pick(5) }
        .Digit7 { self.Pick(6) }
        .Digit8 { self.Pick(7) }
        _ {}
    }
}

/// Brings px up to date with the document as dirty says, and reports
/// whether it changed. Call it after each Apply: a release commits the open
/// stroke, after which its newest piece can no longer be painted alone.
/// O(segment * radius²) for a tail, O(picture) for a full paint; allocates
/// nothing.
export App.Sync(mut self, px: mut []u8): bool {
    d := Replace(mut self.dirty, Dirty.Clean)
    match d {
        .Clean { return false }
        .Tail { doc.PaintTail(&self.doc, px) }
        .Full { doc.Paint(&self.doc, px) }
    }
    return true
}

/// Records that the whole picture must be painted, as after a load. O(1).
export App.MarkFull(mut self) {
    self.dirty = Dirty.Full
}

// MarkTail records that the open stroke grew, unless all is to be painted.
App.MarkTail(mut self) {
    if self.dirty == Dirty.Clean {
        self.dirty = Dirty.Tail
    }
}

App.Pick(mut self, i: int) {
    c := Palette[i]
    self.doc.SetColor(c.r, c.g, c.b)
}

/// The line a script's print step writes: counts, the picture's hash and
/// the caption. Errors: OutOfMemory. O(1); allocates the text in a.
export App.Status(&self, a: &Arena, hash: str): OutOfMemory!str from a {
    n := self.doc.Counts()
    return try a.Fmt(
        "strokes={s} undone={u} hash={h} caption=\"{c}\"",
        (s: n.strokes, u: n.undone, h: hash, c: self.doc.CaptionText()),
    )
}
