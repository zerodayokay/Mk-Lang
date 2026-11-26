// Why: swapchains and external renderers need an OS window handle token
// that outlives Window.Close without borrowing Window.
// What: Surface and Window.Surface, Surface.Native, Surface.Size,
// Surface.Close.
// How: portable API declarations with Unsupported stubs until native
// window record management lands.

/// A claim on a window's native window: it is not destroyed while a
/// Surface lives, even after Window.Close (it is hidden instead). What a
/// renderer, std.gpu's swapchain or a program's own, holds. Plain words, so
/// it borrows nothing and is Send. Linear: end it with Close.
export linear struct Surface {
    rec: uint
}

/// Claims the native window. Errors: Closed. O(1): one atomic increment.
export Window.Surface(&self): Error!Surface {
    _ = self.rec.Load()
    fail Unsupported
}

/// The OS's handles, valid while s lives. O(1).
export Surface.Native(&self): Native {
    _ = self.rec
    return Native.Win32(hwnd: 0, hinstance: 0)
}

/// The client size the window thread last saw. O(1).
export Surface.Size(&self): (width: int, height: int) {
    _ = self.rec
    return (width: 0, height: 0)
}

/// Ends the claim; the last one after Window.Close destroys the native
/// window. Never fails. O(1).
export Surface.Close(self) {
    Surface(rec: _) := self
}
