// Why: Unix key codes and framebuffer dimensions enter through GLFW's ABI.
// What: covers representative key groups and pixel-to-logical size limits.
// How: checks the platform-independent conversion helpers on any test host.

test "GLFW key codes and framebuffer dimensions map to window values" (sys: &System) {
    _ = sys
    Assert(UnixKeyCode(65) == Key.KeyA)
    Assert(UnixKeyCode(90) == Key.KeyZ)
    Assert(UnixKeyCode(48) == Key.Digit0)
    Assert(UnixKeyCode(57) == Key.Digit9)
    Assert(UnixKeyCode(290) == Key.F1)
    Assert(UnixKeyCode(301) == Key.F12)
    Assert(UnixKeyCode(320) == Key.Numpad0)
    Assert(UnixKeyCode(329) == Key.Numpad9)
    Assert(UnixKeyCode(347) == Key.MetaRight)
    Assert(UnixKeyCode(348) == Key.ContextMenu)
    Assert(UnixKeyCode(-1) == Key.Unknown)
    Assert(UnixLogicalSize(800.0, 2.0) == 400)
    Assert(UnixLogicalSize(805.0, 2.0) == 403)
    Assert(UnixLogicalSize(1.0, 3.0) == 1)
    Assert(UnixLogicalSize(10_000_000_000.0, 1.0) == 2147483647)
}
