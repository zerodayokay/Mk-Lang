# std.window

`std.window` opens native windows and delivers their input as timestamped events: size, position, scale, focus, keys by position and by layout, text, pointer, buttons and wheel. One window thread, started by the first `Open`, owns every window and answers the OS, so a program keeps drawing while the user drags or resizes, and any task may own a window. A `Window` is what [`std.gpu`](gpu.md) presents into. The module is Experimental: its API may change before it is stabilised.

Read the [library guide](README.md) first.

```mk
// verdict: parse
import std.window
```

## Contents

- [The main pieces](#the-main-pieces)
- [Requirements](#requirements)
- [Events](#events)
- [Sizes and scale](#sizes-and-scale)
- [Changing a window](#changing-a-window)
- [Surfaces](#surfaces)
- [Errors](#errors)
- [Examples](#examples): [an event loop](#an-event-loop), [held keys after a drop](#held-keys-after-a-drop)
- [API](#api)

## The main pieces

| Task | Use |
|---|---|
| Open and close a window | `Open` with `WindowOptions` (`DefaultWindowOptions`), `Window.Close` |
| Read input | `Window.Poll` (never waits), `Window.Next` (waits for one), `Window.Wait` (waits, takes none) |
| Know what is held | `Window.Held` returning `InputState` (`IsDown`, `IsButtonDown`) |
| Read the window's state | `Window.Size`, `Window.Scale`, `Window.CloseRequested` |
| Change the window | `SetTitle`, `SetSize`, `SetFullscreen`, `SetCursor`, `SetCursorMode`, `Show` |
| Hand the native window to a renderer | `Window.Surface`, `Surface.Native`, `Surface.Close` |

## Requirements

- **Targets.** `x86_64-windows`, on Win32. On every other target the module compiles and `Open` fails with `Unsupported`.
- **DPI.** Windows are per-monitor DPI aware: sizes are physical pixels, and `Window.Scale` (1.0 at 96 dpi) says how many physical pixels make one logical pixel. A move to a monitor of another scale reports `ScaleChanged` and resizes the window to keep its logical size.
- **Threads.** The window thread runs the Win32 message loop; it is started by the first `Open` and is not a task. A `Window` is `Send` but not `Share`: one task reads its events at a time.

## Events

Each `Event` holds `at`, the monotonic time the window thread received it, and its `kind`, an `EventKind`:

| Kind | Reports |
|---|---|
| `CloseRequested` | The user asked to close (the title bar's button, Alt+F4); the window stays open until `Close` |
| `Resized(width, height)`, `Moved(x, y)`, `ScaleChanged(scale)` | The client area's new size in physical pixels, its position, the monitor's scale |
| `Minimized`, `Restored` | The window was minimised, or restored from it |
| `Focus(on)` | Keyboard focus gained or lost; on a loss, every held key and button is released |
| `Key(key, layout, scancode, down, repeat, mods)` | `key` names the key by position (the US-layout key at that place), `layout` by what the current layout puts there; `repeat` marks auto-repeat |
| `Text(ch)` | One character of text input, after the layout and dead keys; never a control character |
| `Pointer(x, y)`, `Button(button, down, x, y, mods)`, `Wheel(dx, dy)`, `Hover(inside)` | Pointer position in client pixels, button changes (the window captures the pointer while a button is held), wheel notches (fractional on precise touchpads), entering and leaving the client area |
| `Dropped(count)` | `count` events were lost because the ring was full |

Events queue in a ring of `WindowOptions.events` entries (4,096 by default). On the window thread, a run of `Pointer`, `Wheel`, `Hover`, `Resized`, `Moved` or `ScaleChanged` events of one kind is merged into one before it reaches the ring, so motion never floods it; keys, buttons and text are never merged. When the ring fills, motion, wheel and hover events are dropped first; key and button events only once a reserve kept for them is full too. A `Dropped` event then says how many were lost, and `Window.Held` gives the true state of every key and button to resynchronise from.

A key press usually produces both a `Key` event and a `Text` event; a text field uses `Text`, a game uses `Key`.

## Sizes and scale

`Window.Size()` returns the client area's size as the window thread last saw it; it is 0 by 0 while the window is minimised. A swapchain sizes itself from it: after a `Resized` event, or when `Device.Acquire` reports `Outdated`, call `Device.Resize` with the new size. While the user drags the window's edge the thread keeps reporting `Resized`, so a program that draws on each one redraws during the drag.

`WindowOptions.width` and `height` are physical pixels; 0 opens at 1280 by 720 times the scale of the monitor the window opens on.

## Changing a window

The setters (`SetTitle`, `SetSize`, `SetFullscreen`, `SetCursor`, `SetCursorMode`, `Show`) never wait: each records the newest value and wakes the window thread, which applies it soon after. Their effects arrive as events like any other change: a `SetSize` or a switch to `Fullscreen.Borderless` (a borderless window covering its monitor) reports `Resized` once applied. `SetCursorMode(CursorMode.Hidden)` hides the cursor over the client area only.

## Surfaces

`Window.Surface()` returns a `Surface`, a claim on the native window that a renderer keeps: the OS window is not destroyed while a surface lives, even after `Window.Close` (it is hidden instead). `Surface.Native()` gives the OS handles, `Native.Win32(hwnd, hinstance)`, and `Surface.Size()` the client size. A `std.gpu` swapchain holds one, so a program may close the window and its swapchain in either order. A surface is plain words and `Send`; end it with `Close`.

## Errors

| Error | When |
|---|---|
| `Unsupported` | No window system on this target |
| `Closed` | The window was closed |
| `Os(code)` | The OS refused to make the window; `code` is its error number |
| `Canceled` | The task was canceled while waiting |
| `OutOfMemory` | The event ring or the window's record could not be allocated |

## Examples

### An event loop

Opens a window and prints the scan code of each key pressed, and the text typed, until it is closed:

```mk
// verdict: ok
import std.window

Run(sys: &System): window.Error!void {
    var o := window.DefaultWindowOptions
    o.title = "keys"
    o.width = 640
    o.height = 480
    win := try window.Open(sys.heap, sys.io, o)
    defer win.Close()
    for {
        ev := try win.Next(sys.io)
        match ev.kind {
            .CloseRequested { return }
            .Key(key: _, layout: _, scancode, down, repeat, mods: _) {
                if down && !repeat {
                    _ = sys.Print("key {s}\n", (s: scancode)) else {}
                }
            }
            .Text(ch) { _ = sys.Print("text {c}\n", (c: ch)) else {} }
            _ {}
        }
    }
}
```

### Held keys after a drop

A game reads the held state each frame rather than tracking every press, and so never misses a release, even after `Dropped`:

```mk
// verdict: ok
import std.window

// Step returns the player's horizontal move this frame: -1, 0 or 1.
Step(win: &window.Window): int {
    for ev := win.Poll() {
        _ = ev
    }
    held := win.Held()
    var dx := 0
    if held.IsDown(window.Key.ArrowLeft) || held.IsDown(window.Key.KeyA) {
        dx -= 1
    }
    if held.IsDown(window.Key.ArrowRight) || held.IsDown(window.Key.KeyD) {
        dx += 1
    }
    return dx
}
```

The dogfood [sketch](../../Dogfoods/sketch) drives a window by script: it posts real messages to the window and waits for each event, and checks the program does the same as when the events are made up.

## API

<!-- api:begin -->
Stability: Experimental (its API may change before stabilisation).

### `Button`

```
export open enum Button
```

A mouse button.

Variants:

- `Left`
- `Right`
- `Middle`
- `Back`
- `Forward`

### `Cursor`

```
export open enum Cursor
```

A cursor shape.

Variants:

- `Arrow`
- `Text`
- `Hand`
- `Crosshair`
- `ResizeHorizontal`
- `ResizeVertical`
- `NotAllowed`

### `CursorMode`

```
export open enum CursorMode
```

Whether the cursor shows over the client area.

Variants:

- `Normal`
- `Hidden`

### `DefaultWindowOptions`

```
export const DefaultWindowOptions: WindowOptions
```

No title, 1280 by 720 scaled, resizable, windowed, visible, 4,096 events.

### `Error`

```
export open type Error = Unsupported | Closed | Os | OutOfMemory | Canceled
```

Every error std.window returns.

### `Event`

```
export struct Event
```

One event, stamped when the window thread received it.

Fields:

- `at: time.Instant`: The monotonic clock time.Now reads under the native Io.
- `kind: EventKind`

### `EventKind`

```
export enum EventKind
```

What happened.

Variants:

- `CloseRequested`
- `Resized(width: int, height: int)`
- `Moved(x: int, y: int)`
- `ScaleChanged(scale: f32)`
- `Minimized`
- `Restored`
- `Focus(on: bool)`
- `Key(key: Key, layout: Key, scancode: u32, down: bool, repeat: bool, mods: Mods)`: key: by position (the US-layout key at the scancode); layout: the key
  the current layout puts there.
- `Text(ch: char)`: One character of text input; never a control character.
- `Pointer(x: f32, y: f32)`
- `Button(button: Button, down: bool, x: f32, y: f32, mods: Mods)`
- `Wheel(dx: f32, dy: f32)`: Wheel notches; fractional from precise touchpads.
- `Hover(inside: bool)`
- `Dropped(count: int)`: count events were lost because the ring was full: motion, wheel and
  hover first; key and button events only once the reserve is full.
  Window.Held then gives the held state.

### `Fullscreen`

```
export open enum Fullscreen
```

Windowed or covering its monitor.

Variants:

- `Windowed`
- `Borderless`: A borderless window covering the monitor the window is on.

### `InputState`

```
export struct InputState
```

Keys and buttons held: built by applying events, or read from
Window.Held. Plain.

### `InputState.Apply`

```
export InputState.Apply(mut self, e: &Event)
```

Applies one event; Focus(false) clears everything held. O(1).

### `InputState.IsButtonDown`

```
export InputState.IsButtonDown(&self, b: Button): bool
```

Whether b is held. O(1).

### `InputState.IsDown`

```
export InputState.IsDown(&self, k: Key): bool
```

Whether k is held. O(1).

### `InputState.New`

```
export InputState.New(): InputState
```

Nothing held. O(1).

### `Key`

```
export open enum Key
```

Keys by position, named as W3C UI Events code values are.

Variants:

- `Unknown`
- `KeyA`
- `KeyB`
- `KeyC`
- `KeyD`
- `KeyE`
- `KeyF`
- `KeyG`
- `KeyH`
- `KeyI`
- `KeyJ`
- `KeyK`
- `KeyL`
- `KeyM`
- `KeyN`
- `KeyO`
- `KeyP`
- `KeyQ`
- `KeyR`
- `KeyS`
- `KeyT`
- `KeyU`
- `KeyV`
- `KeyW`
- `KeyX`
- `KeyY`
- `KeyZ`
- `Digit0`
- `Digit1`
- `Digit2`
- `Digit3`
- `Digit4`
- `Digit5`
- `Digit6`
- `Digit7`
- `Digit8`
- `Digit9`
- `Escape`
- `Enter`
- `Tab`
- `Backspace`
- `Space`
- `Minus`
- `Equal`
- `BracketLeft`
- `BracketRight`
- `Backslash`
- `Semicolon`
- `Quote`
- `Backquote`
- `Comma`
- `Period`
- `Slash`
- `CapsLock`
- `F1`
- `F2`
- `F3`
- `F4`
- `F5`
- `F6`
- `F7`
- `F8`
- `F9`
- `F10`
- `F11`
- `F12`
- `PrintScreen`
- `ScrollLock`
- `Pause`
- `Insert`
- `Home`
- `PageUp`
- `Delete`
- `End`
- `PageDown`
- `ArrowRight`
- `ArrowLeft`
- `ArrowDown`
- `ArrowUp`
- `NumLock`
- `NumpadDivide`
- `NumpadMultiply`
- `NumpadSubtract`
- `NumpadAdd`
- `NumpadEnter`
- `NumpadDecimal`
- `Numpad0`
- `Numpad1`
- `Numpad2`
- `Numpad3`
- `Numpad4`
- `Numpad5`
- `Numpad6`
- `Numpad7`
- `Numpad8`
- `Numpad9`
- `IntlBackslash`
- `ContextMenu`
- `ShiftLeft`
- `ShiftRight`
- `ControlLeft`
- `ControlRight`
- `AltLeft`
- `AltRight`
- `MetaLeft`
- `MetaRight`

### `Mods`

```
export struct Mods
```

Modifier keys held.

Fields:

- `shift: bool`
- `ctrl: bool`
- `alt: bool`
- `meta: bool`

### `Native`

```
export open enum Native
```

The OS's handles for a window.

Variants:

- `Win32(hwnd: uint, hinstance: uint)`

### `Open`

```
export Open(heap: &Heap, io: Io, o: WindowOptions): Error!Window from heap
```

- Flows: The result borrows from heap.
- Errors: Error (Canceled, Closed, OutOfMemory, Os(code: i64), Unsupported)

Opens a window on std's window thread (started by the first call).
Errors: Unsupported off Windows, Os(code), OutOfMemory, Canceled
(repeatable: a window made meanwhile is destroyed by the thread).
Suspends until the thread answers. Allocates the event ring
(events × 32 bytes) and the shared record from heap.

### `Os`

```
export error Os(code: i64)
```

The OS refused: its error code.

### `Surface`

```
export linear struct Surface
```

A claim on a window's native window: it is not destroyed while a
Surface lives, even after Window.Close (it is hidden instead). What a
renderer, std.gpu's swapchain or a program's own, holds. Plain words, so
it borrows nothing and is Send. Linear: end it with Close.

### `Surface.Close`

```
export Surface.Close(self)
```

Ends the claim; the last one after Window.Close destroys the native
window. Never fails. O(1).

### `Surface.Native`

```
export Surface.Native(&self): Native
```

The OS's handles, valid while s lives. O(1).

### `Surface.Size`

```
export Surface.Size(&self): (width: int, height: int)
```

The client size the window thread last saw. O(1).

### `Unsupported`

```
export error Unsupported
```

This target has no window system std supports.

### `Window`

```
export linear view struct Window
```

A window. Linear: end it with Close. Send, not Share: one task reads its
events.

### `Window.Close`

```
export Window.Close(self)
```

Asks the window thread to destroy the window; the ring and record go
once the thread and the last swapchain on it are done. Never waits,
never fails. O(1).

### `Window.CloseRequested`

```
export Window.CloseRequested(&self): bool
```

Whether a close was requested and not yet seen as an event. O(1).

### `Window.Held`

```
export Window.Held(&self): InputState
```

The keys and buttons held now, from the bitmap the window thread keeps:
the state to resynchronise from after Dropped. O(1): nine atomic loads.

### `Window.Next`

```
export Window.Next(&self, io: Io): Error!Event
```

- Errors: Error (Canceled, Closed, OutOfMemory, Os(code: i64), Unsupported)

Waits for the next event. Errors: Closed, Canceled (repeatable: nothing
taken). Suspends on a Waker.

### `Window.Poll`

```
export Window.Poll(&self): ?Event
```

The next queued event, or none. O(1); allocates nothing.

### `Window.Scale`

```
export Window.Scale(&self): f32
```

Physical pixels per logical pixel: 1.0 at 96 dpi. O(1).

### `Window.SetCursor`

```
export Window.SetCursor(&self, c: Cursor): Error!void
```

- Errors: Error (Canceled, Closed, OutOfMemory, Os(code: i64), Unsupported)

Sets the cursor shape. Errors: Closed. Never waits.

### `Window.SetCursorMode`

```
export Window.SetCursorMode(&self, m: CursorMode): Error!void
```

- Errors: Error (Canceled, Closed, OutOfMemory, Os(code: i64), Unsupported)

Shows or hides the cursor over the client area. Errors: Closed.

### `Window.SetFullscreen`

```
export Window.SetFullscreen(&self, f: Fullscreen): Error!void
```

- Errors: Error (Canceled, Closed, OutOfMemory, Os(code: i64), Unsupported)

Enters or leaves borderless fullscreen. Errors: Closed. Never waits.

### `Window.SetSize`

```
export Window.SetSize(&self, width: int, height: int): Error!void
```

- Errors: Error (Canceled, Closed, OutOfMemory, Os(code: i64), Unsupported)

Sets the client size in physical pixels. Errors: Closed. Never waits.

### `Window.SetTitle`

```
export Window.SetTitle(&self, title: str): Error!void
```

- Errors: Error (Canceled, Closed, OutOfMemory, Os(code: i64), Unsupported)

Sets the title; the thread applies the newest value. Errors: Closed.
Never waits; copies title into the shared record (at most 255 bytes).

### `Window.Show`

```
export Window.Show(&self, visible: bool): Error!void
```

- Errors: Error (Canceled, Closed, OutOfMemory, Os(code: i64), Unsupported)

Shows or hides the window. Errors: Closed. Never waits.

### `Window.Size`

```
export Window.Size(&self): (width: int, height: int)
```

The client size the thread last saw, in physical pixels. O(1).

### `Window.Surface`

```
export Window.Surface(&self): Error!Surface
```

- Errors: Error (Canceled, Closed, OutOfMemory, Os(code: i64), Unsupported)

Claims the native window. Errors: Closed. O(1): one atomic increment.

### `Window.Wait`

```
export Window.Wait(&self, io: Io): Error!void
```

- Errors: Error (Canceled, Closed, OutOfMemory, Os(code: i64), Unsupported)

Waits until an event is queued, taking none. Errors: Closed, Canceled.

### `WindowOptions`

```
export struct WindowOptions
```

How Open makes a window. Start from DefaultWindowOptions.

Fields:

- `title: str`
- `width: int`: Client area in physical pixels; 0 is 1280 by 720 times the scale of
  the monitor the window opens on.
- `height: int`
- `resizable: bool`
- `fullscreen: Fullscreen`
- `visible: bool`
- `events: int`: Events the ring holds: a power of two, 64 to 65,536.

<!-- api:end -->
