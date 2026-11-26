// Why: set 1 scancodes, Win32 VK codes and compact key indices must map
// deterministically and stay within the 256-key InputState bitmap.
// What: tests of KeyCode, KeyFromCode, KeyFromScancode and KeyFromVk.
// How: verifies round-trip of all 106 keys and checks set 1 extended vs
// non-extended edge cases (Pause vs NumLock, PrintScreen vs NumpadMultiply).

test "KeyCode round-trips every named Key and stays below 256" (sys: &System) {
    _ = sys
    for i in 0..106 {
        k := KeyFromCode(i)
        Assert(KeyCode(k) == i)
    }
    Assert(KeyCode(Key.Unknown) == 0)
    Assert(KeyCode(KeyFromCode(-1)) == 0)
    Assert(KeyCode(KeyFromCode(106)) == 0)
}

test "KeyFromScancode distinguishes non-extended and extended set 1 codes" (sys: &System) {
    _ = sys
    Assert(KeyCode(KeyFromScancode(0x1E, false)) == KeyCode(Key.KeyA))
    Assert(KeyCode(KeyFromScancode(0x01, false)) == KeyCode(Key.Escape))
    Assert(KeyCode(KeyFromScancode(0x1C, false)) == KeyCode(Key.Enter))
    Assert(KeyCode(KeyFromScancode(0x1C, true)) == KeyCode(Key.NumpadEnter))
    Assert(KeyCode(KeyFromScancode(0x1D, false)) == KeyCode(Key.ControlLeft))
    Assert(KeyCode(KeyFromScancode(0x1D, true)) == KeyCode(Key.ControlRight))
    Assert(KeyCode(KeyFromScancode(0x38, false)) == KeyCode(Key.AltLeft))
    Assert(KeyCode(KeyFromScancode(0x38, true)) == KeyCode(Key.AltRight))
    Assert(KeyCode(KeyFromScancode(0x37, false)) == KeyCode(Key.NumpadMultiply))
    Assert(KeyCode(KeyFromScancode(0x37, true)) == KeyCode(Key.PrintScreen))
    Assert(KeyCode(KeyFromScancode(0x45, false)) == KeyCode(Key.Pause))
    Assert(KeyCode(KeyFromScancode(0x45, true)) == KeyCode(Key.NumLock))
    Assert(KeyCode(KeyFromScancode(0x48, true)) == KeyCode(Key.ArrowUp))
    Assert(KeyCode(KeyFromScancode(0x5B, true)) == KeyCode(Key.MetaLeft))
    Assert(KeyCode(KeyFromScancode(0xFF, false)) == KeyCode(Key.Unknown))
}

test "KeyFromVk maps letters, digits, function keys and OEM keys" (sys: &System) {
    _ = sys
    Assert(KeyCode(KeyFromVk(0x41)) == KeyCode(Key.KeyA))
    Assert(KeyCode(KeyFromVk(0x5A)) == KeyCode(Key.KeyZ))
    Assert(KeyCode(KeyFromVk(0x30)) == KeyCode(Key.Digit0))
    Assert(KeyCode(KeyFromVk(0x39)) == KeyCode(Key.Digit9))
    Assert(KeyCode(KeyFromVk(0x70)) == KeyCode(Key.F1))
    Assert(KeyCode(KeyFromVk(0x7B)) == KeyCode(Key.F12))
    Assert(KeyCode(KeyFromVk(0xA0)) == KeyCode(Key.ShiftLeft))
    Assert(KeyCode(KeyFromVk(0xA1)) == KeyCode(Key.ShiftRight))
}
