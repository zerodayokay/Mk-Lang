// Why: input arrives as timestamped events in order, with motion coalesced
// and key and button events never dropped.
// What: Event, EventKind, Mods, Button and compact button code helpers.
// How: plain value types matching the public API, with small integer mapping
// helpers for the 32-byte ring representation.

import std.time

/// One event, stamped when the window thread received it.
export struct Event {
    /// The monotonic clock time.Now reads under the native Io.
    export at: time.Instant
    export kind: EventKind
}

/// What happened.
export enum EventKind {
    CloseRequested
    Resized(width: int, height: int)
    Moved(x: int, y: int)
    ScaleChanged(scale: f32)
    Minimized
    Restored
    Focus(on: bool)
    /// key: by position (the US-layout key at the scancode); layout: the key
    /// the current layout puts there.
    Key(key: Key, layout: Key, scancode: u32, down: bool, repeat: bool, mods: Mods)
    /// One character of text input; never a control character.
    Text(ch: char)
    Pointer(x: f32, y: f32)
    Button(button: Button, down: bool, x: f32, y: f32, mods: Mods)
    /// Wheel notches; fractional from precise touchpads.
    Wheel(dx: f32, dy: f32)
    Hover(inside: bool)
    /// count events were lost because the ring was full: motion, wheel and
    /// hover first; key and button events only once the reserve is full.
    /// Window.Held then gives the held state.
    Dropped(count: int)
}

/// Modifier keys held.
export struct Mods {
    export shift: bool
    export ctrl: bool
    export alt: bool
    export meta: bool
}

/// A mouse button.
export open enum Button {
    Left
    Right
    Middle
    Back
    Forward
}

// ButtonCode maps b to 0..4, or 255 for an unknown variant.
ButtonCode(b: Button): int {
    match b {
        Button.Left { return 0 }
        Button.Right { return 1 }
        Button.Middle { return 2 }
        Button.Back { return 3 }
        Button.Forward { return 4 }
        _ { return 255 }
    }
}

// ButtonFromCode maps 0..4 back to Button (defaults to Left).
ButtonFromCode(c: int): Button {
    if c == 1 { return Button.Right }
    if c == 2 { return Button.Middle }
    if c == 3 { return Button.Back }
    if c == 4 { return Button.Forward }
    return Button.Left
}
