// Why: GLFW key codes identify physical positions and carry separate layout names.
// What: conversion of GLFW key codes and printable names to Key.
// How: contiguous GLFW ranges map into the shared compact key table; the
// current layout maps named ASCII keys into the same public set.

import std.raw

UnixKeyCode(code: i32): Key {
    if code >= 65 && code <= 90 {
        return KeyFromCode(int(code - 65) + 1)
    }
    if code >= 48 && code <= 57 {
        return KeyFromCode(int(code - 48) + 27)
    }
    if code >= 290 && code <= 301 {
        return KeyFromCode(int(code - 290) + 54)
    }
    if code >= 320 && code <= 329 {
        return KeyFromCode(int(code - 320) + 86)
    }
    match code {
        32 { return Key.Space }
        39 { return Key.Quote }
        44 { return Key.Comma }
        45 { return Key.Minus }
        46 { return Key.Period }
        47 { return Key.Slash }
        59 { return Key.Semicolon }
        61 { return Key.Equal }
        91 { return Key.BracketLeft }
        92 { return Key.Backslash }
        93 { return Key.BracketRight }
        96 { return Key.Backquote }
        161 { return Key.IntlBackslash }
        162 { return Key.IntlBackslash }
        256 { return Key.Escape }
        257 { return Key.Enter }
        258 { return Key.Tab }
        259 { return Key.Backspace }
        260 { return Key.Insert }
        261 { return Key.Delete }
        262 { return Key.ArrowRight }
        263 { return Key.ArrowLeft }
        264 { return Key.ArrowDown }
        265 { return Key.ArrowUp }
        266 { return Key.PageUp }
        267 { return Key.PageDown }
        268 { return Key.Home }
        269 { return Key.End }
        280 { return Key.CapsLock }
        281 { return Key.ScrollLock }
        282 { return Key.NumLock }
        283 { return Key.PrintScreen }
        284 { return Key.Pause }
        330 { return Key.NumpadDecimal }
        331 { return Key.NumpadDivide }
        332 { return Key.NumpadMultiply }
        333 { return Key.NumpadSubtract }
        334 { return Key.NumpadAdd }
        335 { return Key.NumpadEnter }
        340 { return Key.ShiftLeft }
        341 { return Key.ControlLeft }
        342 { return Key.AltLeft }
        343 { return Key.MetaLeft }
        344 { return Key.ShiftRight }
        345 { return Key.ControlRight }
        346 { return Key.AltRight }
        347 { return Key.MetaRight }
        348 { return Key.ContextMenu }
        _ { return Key.Unknown }
    }
}

UnixKeyLayout(key: Key, code: i32, scancode: i32): Key {
    if code >= 256 || (code == -1 && scancode < 0) {
        return key
    }
    unsafe "glfwGetKeyName returns a NUL-terminated name valid on the owner until the next event poll" {
        p := (*Glfw()).get_key_name(code, scancode)
        if raw.IsNull(p) {
            return key
        }
        c := raw.Read(p)
        if c >= 0x80 || raw.Read(raw.Offset(p, 1)) != 0 {
            return Key.Unknown
        }
        upper := if c >= b'a' && c <= b'z' { c - 32 } else { c }
        return UnixKeyCode(i32(upper))
    }
}
