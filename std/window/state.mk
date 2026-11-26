// Why: callers need a compact, allocation-free held-keys and held-buttons
// snapshot that stays consistent across events and resynchronises after Dropped.
// What: InputState (New, Apply, IsDown, IsButtonDown).
// How: 256-bit key bitmap in [4]u64 plus an 8-bit button mask, updated on
// Key/Button events and cleared on Focus(false).

/// Keys and buttons held: built by applying events, or read from
/// Window.Held. Plain.
export struct InputState {
    keys: [4]u64
    buttons: u8
}

/// Nothing held. O(1).
export InputState.New(): InputState {
    return InputState(keys: [0; 4], buttons: 0)
}

// SetKeyBit updates the bit for key index c in keys[4].
SetKeyBit(keys: mut [4]u64, c: int, down: bool) {
    if c <= 0 || c >= 256 {
        return
    }
    w := c >> 6
    bit: u64 = u64(1) << Trunc[u64](c & 63)
    if down {
        keys[w] = keys[w] | bit
    } else {
        keys[w] = keys[w] & (bit ^ 0xFFFFFFFFFFFFFFFF)
    }
}

// SetButtonBit updates the bit for button index b in buttons.
SetButtonBit(buttons: u8, b: int, down: bool): u8 {
    if b < 0 || b >= 8 {
        return buttons
    }
    bit: u8 = u8(1) << Trunc[u8](b)
    if down {
        return buttons | bit
    }
    return buttons & (bit ^ 0xFF)
}

/// Applies one event; Focus(false) clears everything held. O(1).
export InputState.Apply(mut self, e: &Event) {
    match e.kind {
        EventKind.Key(key, layout: _, scancode: _, down, repeat: _, mods: _) {
            SetKeyBit(mut self.keys, KeyCode(key), down)
        }
        EventKind.Button(button, down, x: _, y: _, mods: _) {
            self.buttons = SetButtonBit(self.buttons, ButtonCode(button), down)
        }
        EventKind.Focus(on) {
            if !on {
                self.keys = [0; 4]
                self.buttons = 0
            }
        }
        _ {}
    }
}

/// Whether k is held. O(1).
export InputState.IsDown(&self, k: Key): bool {
    c := KeyCode(k)
    if c <= 0 || c >= 256 {
        return false
    }
    w := c >> 6
    bit: u64 = u64(1) << Trunc[u64](c & 63)
    return (self.keys[w] & bit) != 0
}

/// Whether b is held. O(1).
export InputState.IsButtonDown(&self, b: Button): bool {
    c := ButtonCode(b)
    if c < 0 || c >= 8 {
        return false
    }
    bit: u8 = u8(1) << Trunc[u8](c)
    return (self.buttons & bit) != 0
}
