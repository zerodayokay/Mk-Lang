// Why: InputState must track keys across all four u64 words and all buttons,
// and clear everything on Focus(false).
// What: unit tests for InputState.New, Apply, IsDown and IsButtonDown.
// How: applies synthetic Key, Button and Focus events across the key range.

import std.time

test "InputState tracks keys, buttons and clears on Focus(false)" (sys: &System) {
    now := time.Now(sys.io)
    var st := InputState.New()
    Assert(!st.IsDown(Key.Unknown))
    Assert(!st.IsDown(Key.KeyA))
    Assert(!st.IsButtonDown(Button.Left))

    mods := Mods(shift: false, ctrl: false, alt: false, meta: false)
    // Keys spanning all four u64 words (codes 1, 65, 105).
    for k in [Key.KeyA, Key.F12, Key.MetaRight] {
        ev := Event(at: now, kind: EventKind.Key(key: k, layout: k, scancode: 0, down: true, repeat: false, mods: mods))
        st.Apply(&ev)
        Assert(st.IsDown(k))
    }
    // Unknown is ignored.
    unk := Event(at: now, kind: EventKind.Key(key: Key.Unknown, layout: Key.Unknown, scancode: 0, down: true, repeat: false, mods: mods))
    st.Apply(&unk)
    Assert(!st.IsDown(Key.Unknown))

    // Release one key; others stay down.
    rel := Event(at: now, kind: EventKind.Key(key: Key.F12, layout: Key.F12, scancode: 0, down: false, repeat: false, mods: mods))
    st.Apply(&rel)
    Assert(st.IsDown(Key.KeyA))
    Assert(!st.IsDown(Key.F12))
    Assert(st.IsDown(Key.MetaRight))

    // Buttons.
    b1 := Event(at: now, kind: EventKind.Button(button: Button.Left, down: true, x: 10.0, y: 20.0, mods: mods))
    b2 := Event(at: now, kind: EventKind.Button(button: Button.Forward, down: true, x: 10.0, y: 20.0, mods: mods))
    st.Apply(&b1)
    st.Apply(&b2)
    Assert(st.IsButtonDown(Button.Left) && st.IsButtonDown(Button.Forward))
    Assert(!st.IsButtonDown(Button.Right))

    // Focus(true) keeps held state; Focus(false) clears all.
    f_on := Event(at: now, kind: EventKind.Focus(on: true))
    st.Apply(&f_on)
    Assert(st.IsDown(Key.KeyA) && st.IsButtonDown(Button.Left))

    f_off := Event(at: now, kind: EventKind.Focus(on: false))
    st.Apply(&f_off)
    Assert(!st.IsDown(Key.KeyA))
    Assert(!st.IsDown(Key.MetaRight))
    Assert(!st.IsButtonDown(Button.Left))
    Assert(!st.IsButtonDown(Button.Forward))
}
