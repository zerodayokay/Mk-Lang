// Why: window creation, fullscreen transitions, cursor configuration and
// swapchain creation share small value option and handle enums.
// What: WindowOptions, DefaultWindowOptions, Fullscreen, Cursor,
// CursorMode and Native.
// How: plain exported structs and open enums with DefaultWindowOptions.

/// How Open makes a window. Start from DefaultWindowOptions.
export struct WindowOptions {
    export title: str
    /// Client area in physical pixels; 0 is 1280 by 720 times the scale of
    /// the monitor the window opens on.
    export width: int
    export height: int
    export resizable: bool
    export fullscreen: Fullscreen
    export visible: bool
    /// Events the ring holds: a power of two, 64 to 65,536.
    export events: int
    sealed: u8
}

/// No title, 1280 by 720 scaled, resizable, windowed, visible, 4,096 events.
export const DefaultWindowOptions: WindowOptions = WindowOptions(title: "", width: 0, height: 0, resizable: true, fullscreen: Fullscreen.Windowed, visible: true, events: 4096, sealed: 0)

/// Windowed or covering its monitor.
export open enum Fullscreen {
    Windowed
    /// A borderless window covering the monitor the window is on.
    Borderless
}

/// A cursor shape.
export open enum Cursor {
    Arrow
    Text
    Hand
    Crosshair
    ResizeHorizontal
    ResizeVertical
    NotAllowed
}

/// Whether the cursor shows over the client area.
export open enum CursorMode {
    Normal
    Hidden
}

/// The OS's handles for a window.
export open enum Native {
    Win32(hwnd: uint, hinstance: uint)
}
