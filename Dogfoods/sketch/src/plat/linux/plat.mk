// Why: std.window has no Linux backend yet, so a scripted window run is
// Windows only; the Linux build still needs the module's names.
//
// What: the plat API of src/plat/windows, each call reporting false.
//
// How: Supported is false, and main runs scripts headless only.

import std.window

/// Whether this target can post window messages.
export const Supported: bool = false

/// Reports false: no window messages on this target. O(1).
export PostMove(s: &window.Surface, x: int, y: int): bool {
    _ = (s, x, y)
    return false
}

/// Reports false: no window messages on this target. O(1).
export PostButton(s: &window.Surface, down: bool, x: int, y: int): bool {
    _ = (s, down, x, y)
    return false
}

/// The virtual-key code a script key would have; 0 here. O(1).
export KeyCode(name: str, palette: int): u32 {
    _ = (name, palette)
    return 0
}

/// Reports false: no window messages on this target. O(1).
export PostKey(s: &window.Surface, vk: u32, down: bool): bool {
    _ = (s, vk, down)
    return false
}

/// Reports false: no window messages on this target. O(1).
export PostChar(s: &window.Surface, ch: char): bool {
    _ = (s, ch)
    return false
}

/// Reports false: no window messages on this target. O(1).
export PostSize(s: &window.Surface, minimized: bool, width: int, height: int): bool {
    _ = (s, minimized, width, height)
    return false
}

/// Reports false: no window messages on this target. O(1).
export PostFocus(s: &window.Surface, on: bool): bool {
    _ = (s, on)
    return false
}

/// Reports false: no window messages on this target. O(1).
export PostMarker(s: &window.Surface, x: int, y: int): bool {
    _ = (s, x, y)
    return false
}

/// Reports false: no window messages on this target. O(1).
export PostClose(s: &window.Surface): bool {
    _ = s
    return false
}

/// Reports false: no windows on this target. O(1).
export ResizeClient(s: &window.Surface, width: int, height: int): bool {
    _ = (s, width, height)
    return false
}
