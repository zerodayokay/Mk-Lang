// Why: keyboard events must identify physical key positions independently
// of layout, and map OS scancodes and virtual keys consistently.
// What: Key (W3C UI Events physical key codes), KeyCode, KeyFromCode,
// KeyFromScancode (IBM set 1 + E0 extended) and KeyFromVk (Win32 VK).
// How: pure lookup functions over constant code indices and set 1/VK tables.

/// Keys by position, named as W3C UI Events code values are.
export open enum Key {
    Unknown
    KeyA
    KeyB
    KeyC
    KeyD
    KeyE
    KeyF
    KeyG
    KeyH
    KeyI
    KeyJ
    KeyK
    KeyL
    KeyM
    KeyN
    KeyO
    KeyP
    KeyQ
    KeyR
    KeyS
    KeyT
    KeyU
    KeyV
    KeyW
    KeyX
    KeyY
    KeyZ
    Digit0
    Digit1
    Digit2
    Digit3
    Digit4
    Digit5
    Digit6
    Digit7
    Digit8
    Digit9
    Escape
    Enter
    Tab
    Backspace
    Space
    Minus
    Equal
    BracketLeft
    BracketRight
    Backslash
    Semicolon
    Quote
    Backquote
    Comma
    Period
    Slash
    CapsLock
    F1
    F2
    F3
    F4
    F5
    F6
    F7
    F8
    F9
    F10
    F11
    F12
    PrintScreen
    ScrollLock
    Pause
    Insert
    Home
    PageUp
    Delete
    End
    PageDown
    ArrowRight
    ArrowLeft
    ArrowDown
    ArrowUp
    NumLock
    NumpadDivide
    NumpadMultiply
    NumpadSubtract
    NumpadAdd
    NumpadEnter
    NumpadDecimal
    Numpad0
    Numpad1
    Numpad2
    Numpad3
    Numpad4
    Numpad5
    Numpad6
    Numpad7
    Numpad8
    Numpad9
    IntlBackslash
    ContextMenu
    ShiftLeft
    ShiftRight
    ControlLeft
    ControlRight
    AltLeft
    AltRight
    MetaLeft
    MetaRight
}

// KeyCount is the number of named Key variants (0..106).
const KeyCount = 107

// KeyCode maps k to its compact index (0..106), or 0 for an unknown variant.
KeyCode(k: Key): int {
    match k {
        Key.Unknown { return 0 }
        Key.KeyA { return 1 }
        Key.KeyB { return 2 }
        Key.KeyC { return 3 }
        Key.KeyD { return 4 }
        Key.KeyE { return 5 }
        Key.KeyF { return 6 }
        Key.KeyG { return 7 }
        Key.KeyH { return 8 }
        Key.KeyI { return 9 }
        Key.KeyJ { return 10 }
        Key.KeyK { return 11 }
        Key.KeyL { return 12 }
        Key.KeyM { return 13 }
        Key.KeyN { return 14 }
        Key.KeyO { return 15 }
        Key.KeyP { return 16 }
        Key.KeyQ { return 17 }
        Key.KeyR { return 18 }
        Key.KeyS { return 19 }
        Key.KeyT { return 20 }
        Key.KeyU { return 21 }
        Key.KeyV { return 22 }
        Key.KeyW { return 23 }
        Key.KeyX { return 24 }
        Key.KeyY { return 25 }
        Key.KeyZ { return 26 }
        Key.Digit0 { return 27 }
        Key.Digit1 { return 28 }
        Key.Digit2 { return 29 }
        Key.Digit3 { return 30 }
        Key.Digit4 { return 31 }
        Key.Digit5 { return 32 }
        Key.Digit6 { return 33 }
        Key.Digit7 { return 34 }
        Key.Digit8 { return 35 }
        Key.Digit9 { return 36 }
        Key.Escape { return 37 }
        Key.Enter { return 38 }
        Key.Tab { return 39 }
        Key.Backspace { return 40 }
        Key.Space { return 41 }
        Key.Minus { return 42 }
        Key.Equal { return 43 }
        Key.BracketLeft { return 44 }
        Key.BracketRight { return 45 }
        Key.Backslash { return 46 }
        Key.Semicolon { return 47 }
        Key.Quote { return 48 }
        Key.Backquote { return 49 }
        Key.Comma { return 50 }
        Key.Period { return 51 }
        Key.Slash { return 52 }
        Key.CapsLock { return 53 }
        Key.F1 { return 54 }
        Key.F2 { return 55 }
        Key.F3 { return 56 }
        Key.F4 { return 57 }
        Key.F5 { return 58 }
        Key.F6 { return 59 }
        Key.F7 { return 60 }
        Key.F8 { return 61 }
        Key.F9 { return 62 }
        Key.F10 { return 63 }
        Key.F11 { return 64 }
        Key.F12 { return 65 }
        Key.PrintScreen { return 66 }
        Key.ScrollLock { return 67 }
        Key.Pause { return 68 }
        Key.Insert { return 69 }
        Key.Home { return 70 }
        Key.PageUp { return 71 }
        Key.Delete { return 72 }
        Key.End { return 73 }
        Key.PageDown { return 74 }
        Key.ArrowRight { return 75 }
        Key.ArrowLeft { return 76 }
        Key.ArrowDown { return 77 }
        Key.ArrowUp { return 78 }
        Key.NumLock { return 79 }
        Key.NumpadDivide { return 80 }
        Key.NumpadMultiply { return 81 }
        Key.NumpadSubtract { return 82 }
        Key.NumpadAdd { return 83 }
        Key.NumpadEnter { return 84 }
        Key.NumpadDecimal { return 85 }
        Key.Numpad0 { return 86 }
        Key.Numpad1 { return 87 }
        Key.Numpad2 { return 88 }
        Key.Numpad3 { return 89 }
        Key.Numpad4 { return 90 }
        Key.Numpad5 { return 91 }
        Key.Numpad6 { return 92 }
        Key.Numpad7 { return 93 }
        Key.Numpad8 { return 94 }
        Key.Numpad9 { return 95 }
        Key.IntlBackslash { return 96 }
        Key.ContextMenu { return 97 }
        Key.ShiftLeft { return 98 }
        Key.ShiftRight { return 99 }
        Key.ControlLeft { return 100 }
        Key.ControlRight { return 101 }
        Key.AltLeft { return 102 }
        Key.AltRight { return 103 }
        Key.MetaLeft { return 104 }
        Key.MetaRight { return 105 }
        _ { return 0 }
    }
}

const KeyList: [106]Key = [
    Key.Unknown,
    Key.KeyA, Key.KeyB, Key.KeyC, Key.KeyD, Key.KeyE, Key.KeyF, Key.KeyG,
    Key.KeyH, Key.KeyI, Key.KeyJ, Key.KeyK, Key.KeyL, Key.KeyM, Key.KeyN,
    Key.KeyO, Key.KeyP, Key.KeyQ, Key.KeyR, Key.KeyS, Key.KeyT, Key.KeyU,
    Key.KeyV, Key.KeyW, Key.KeyX, Key.KeyY, Key.KeyZ,
    Key.Digit0, Key.Digit1, Key.Digit2, Key.Digit3, Key.Digit4,
    Key.Digit5, Key.Digit6, Key.Digit7, Key.Digit8, Key.Digit9,
    Key.Escape, Key.Enter, Key.Tab, Key.Backspace, Key.Space,
    Key.Minus, Key.Equal, Key.BracketLeft, Key.BracketRight, Key.Backslash,
    Key.Semicolon, Key.Quote, Key.Backquote, Key.Comma, Key.Period, Key.Slash,
    Key.CapsLock,
    Key.F1, Key.F2, Key.F3, Key.F4, Key.F5, Key.F6,
    Key.F7, Key.F8, Key.F9, Key.F10, Key.F11, Key.F12,
    Key.PrintScreen, Key.ScrollLock, Key.Pause,
    Key.Insert, Key.Home, Key.PageUp, Key.Delete, Key.End, Key.PageDown,
    Key.ArrowRight, Key.ArrowLeft, Key.ArrowDown, Key.ArrowUp,
    Key.NumLock, Key.NumpadDivide, Key.NumpadMultiply, Key.NumpadSubtract,
    Key.NumpadAdd, Key.NumpadEnter, Key.NumpadDecimal,
    Key.Numpad0, Key.Numpad1, Key.Numpad2, Key.Numpad3, Key.Numpad4,
    Key.Numpad5, Key.Numpad6, Key.Numpad7, Key.Numpad8, Key.Numpad9,
    Key.IntlBackslash, Key.ContextMenu,
    Key.ShiftLeft, Key.ShiftRight, Key.ControlLeft, Key.ControlRight,
    Key.AltLeft, Key.AltRight, Key.MetaLeft, Key.MetaRight,
]

// KeyFromCode returns the Key for compact index code, or Key.Unknown.
KeyFromCode(code: int): Key {
    if code <= 0 || code >= 106 {
        return Key.Unknown
    }
    return KeyList[code]
}

// KeyFromScancode maps an IBM PC set 1 scancode (0..255) and extended flag
// (bit 24 of Win32 lParam, 0xE0 prefix) to a physical Key.
KeyFromScancode(scancode: u32, extended: bool): Key {
    sc := int(scancode & 0xFF)
    if extended {
        if sc == 0x1C { return Key.NumpadEnter }
        if sc == 0x1D { return Key.ControlRight }
        if sc == 0x35 { return Key.NumpadDivide }
        if sc == 0x37 { return Key.PrintScreen }
        if sc == 0x38 { return Key.AltRight }
        if sc == 0x45 { return Key.NumLock }
        if sc == 0x46 { return Key.Pause }
        if sc == 0x47 { return Key.Home }
        if sc == 0x48 { return Key.ArrowUp }
        if sc == 0x49 { return Key.PageUp }
        if sc == 0x4B { return Key.ArrowLeft }
        if sc == 0x4D { return Key.ArrowRight }
        if sc == 0x4F { return Key.End }
        if sc == 0x50 { return Key.ArrowDown }
        if sc == 0x51 { return Key.PageDown }
        if sc == 0x52 { return Key.Insert }
        if sc == 0x53 { return Key.Delete }
        if sc == 0x5B { return Key.MetaLeft }
        if sc == 0x5C { return Key.MetaRight }
        if sc == 0x5D { return Key.ContextMenu }
        return Key.Unknown
    }
    if sc == 0x01 { return Key.Escape }
    if sc == 0x02 { return Key.Digit1 }
    if sc == 0x03 { return Key.Digit2 }
    if sc == 0x04 { return Key.Digit3 }
    if sc == 0x05 { return Key.Digit4 }
    if sc == 0x06 { return Key.Digit5 }
    if sc == 0x07 { return Key.Digit6 }
    if sc == 0x08 { return Key.Digit7 }
    if sc == 0x09 { return Key.Digit8 }
    if sc == 0x0A { return Key.Digit9 }
    if sc == 0x0B { return Key.Digit0 }
    if sc == 0x0C { return Key.Minus }
    if sc == 0x0D { return Key.Equal }
    if sc == 0x0E { return Key.Backspace }
    if sc == 0x0F { return Key.Tab }
    if sc == 0x10 { return Key.KeyQ }
    if sc == 0x11 { return Key.KeyW }
    if sc == 0x12 { return Key.KeyE }
    if sc == 0x13 { return Key.KeyR }
    if sc == 0x14 { return Key.KeyT }
    if sc == 0x15 { return Key.KeyY }
    if sc == 0x16 { return Key.KeyU }
    if sc == 0x17 { return Key.KeyI }
    if sc == 0x18 { return Key.KeyO }
    if sc == 0x19 { return Key.KeyP }
    if sc == 0x1A { return Key.BracketLeft }
    if sc == 0x1B { return Key.BracketRight }
    if sc == 0x1C { return Key.Enter }
    if sc == 0x1D { return Key.ControlLeft }
    if sc == 0x1E { return Key.KeyA }
    if sc == 0x1F { return Key.KeyS }
    if sc == 0x20 { return Key.KeyD }
    if sc == 0x21 { return Key.KeyF }
    if sc == 0x22 { return Key.KeyG }
    if sc == 0x23 { return Key.KeyH }
    if sc == 0x24 { return Key.KeyJ }
    if sc == 0x25 { return Key.KeyK }
    if sc == 0x26 { return Key.KeyL }
    if sc == 0x27 { return Key.Semicolon }
    if sc == 0x28 { return Key.Quote }
    if sc == 0x29 { return Key.Backquote }
    if sc == 0x2A { return Key.ShiftLeft }
    if sc == 0x2B { return Key.Backslash }
    if sc == 0x2C { return Key.KeyZ }
    if sc == 0x2D { return Key.KeyX }
    if sc == 0x2E { return Key.KeyC }
    if sc == 0x2F { return Key.KeyV }
    if sc == 0x30 { return Key.KeyB }
    if sc == 0x31 { return Key.KeyN }
    if sc == 0x32 { return Key.KeyM }
    if sc == 0x33 { return Key.Comma }
    if sc == 0x34 { return Key.Period }
    if sc == 0x35 { return Key.Slash }
    if sc == 0x36 { return Key.ShiftRight }
    if sc == 0x37 { return Key.NumpadMultiply }
    if sc == 0x38 { return Key.AltLeft }
    if sc == 0x39 { return Key.Space }
    if sc == 0x3A { return Key.CapsLock }
    if sc == 0x3B { return Key.F1 }
    if sc == 0x3C { return Key.F2 }
    if sc == 0x3D { return Key.F3 }
    if sc == 0x3E { return Key.F4 }
    if sc == 0x3F { return Key.F5 }
    if sc == 0x40 { return Key.F6 }
    if sc == 0x41 { return Key.F7 }
    if sc == 0x42 { return Key.F8 }
    if sc == 0x43 { return Key.F9 }
    if sc == 0x44 { return Key.F10 }
    if sc == 0x45 { return Key.Pause }
    if sc == 0x46 { return Key.ScrollLock }
    if sc == 0x47 { return Key.Numpad7 }
    if sc == 0x48 { return Key.Numpad8 }
    if sc == 0x49 { return Key.Numpad9 }
    if sc == 0x4A { return Key.NumpadSubtract }
    if sc == 0x4B { return Key.Numpad4 }
    if sc == 0x4C { return Key.Numpad5 }
    if sc == 0x4D { return Key.Numpad6 }
    if sc == 0x4E { return Key.NumpadAdd }
    if sc == 0x4F { return Key.Numpad1 }
    if sc == 0x50 { return Key.Numpad2 }
    if sc == 0x51 { return Key.Numpad3 }
    if sc == 0x52 { return Key.Numpad0 }
    if sc == 0x53 { return Key.NumpadDecimal }
    if sc == 0x56 { return Key.IntlBackslash }
    if sc == 0x57 { return Key.F11 }
    if sc == 0x58 { return Key.F12 }
    return Key.Unknown
}

// KeyFromVk maps a Win32 virtual-key code (VK_*) to Key for layout-mapped keys.
KeyFromVk(vk: u32): Key {
    if vk >= 0x41 && vk <= 0x5A {
        return KeyFromCode(1 + int(vk - 0x41))
    }
    if vk >= 0x30 && vk <= 0x39 {
        return KeyFromCode(27 + int(vk - 0x30))
    }
    if vk >= 0x60 && vk <= 0x69 {
        return KeyFromCode(86 + int(vk - 0x60))
    }
    if vk >= 0x70 && vk <= 0x7B {
        return KeyFromCode(54 + int(vk - 0x70))
    }
    if vk == 0x08 { return Key.Backspace }
    if vk == 0x09 { return Key.Tab }
    if vk == 0x0D { return Key.Enter }
    if vk == 0x13 { return Key.Pause }
    if vk == 0x14 { return Key.CapsLock }
    if vk == 0x1B { return Key.Escape }
    if vk == 0x20 { return Key.Space }
    if vk == 0x21 { return Key.PageUp }
    if vk == 0x22 { return Key.PageDown }
    if vk == 0x23 { return Key.End }
    if vk == 0x24 { return Key.Home }
    if vk == 0x25 { return Key.ArrowLeft }
    if vk == 0x26 { return Key.ArrowUp }
    if vk == 0x27 { return Key.ArrowRight }
    if vk == 0x28 { return Key.ArrowDown }
    if vk == 0x2C { return Key.PrintScreen }
    if vk == 0x2D { return Key.Insert }
    if vk == 0x2E { return Key.Delete }
    if vk == 0x5B { return Key.MetaLeft }
    if vk == 0x5C { return Key.MetaRight }
    if vk == 0x5D { return Key.ContextMenu }
    if vk == 0x6A { return Key.NumpadMultiply }
    if vk == 0x6B { return Key.NumpadAdd }
    if vk == 0x6D { return Key.NumpadSubtract }
    if vk == 0x6E { return Key.NumpadDecimal }
    if vk == 0x6F { return Key.NumpadDivide }
    if vk == 0x90 { return Key.NumLock }
    if vk == 0x91 { return Key.ScrollLock }
    if vk == 0xA0 { return Key.ShiftLeft }
    if vk == 0xA1 { return Key.ShiftRight }
    if vk == 0xA2 { return Key.ControlLeft }
    if vk == 0xA3 { return Key.ControlRight }
    if vk == 0xA4 { return Key.AltLeft }
    if vk == 0xA5 { return Key.AltRight }
    if vk == 0xBA { return Key.Semicolon }
    if vk == 0xBB { return Key.Equal }
    if vk == 0xBC { return Key.Comma }
    if vk == 0xBD { return Key.Minus }
    if vk == 0xBE { return Key.Period }
    if vk == 0xBF { return Key.Slash }
    if vk == 0xC0 { return Key.Backquote }
    if vk == 0xDB { return Key.BracketLeft }
    if vk == 0xDC { return Key.Backslash }
    if vk == 0xDD { return Key.BracketRight }
    if vk == 0xDE { return Key.Quote }
    if vk == 0xE2 { return Key.IntlBackslash }
    return Key.Unknown
}
