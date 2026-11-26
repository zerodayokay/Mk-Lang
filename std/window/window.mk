// Why: programs open windows and read their input from any task, and keep
// drawing while the user drags or resizes them.
// What: Window and its methods (Open, Close, Poll, Next, Wait, Size, Scale,
// CloseRequested, Held, SetTitle, SetSize, SetFullscreen, SetCursor,
// SetCursorMode, Show).
// How: portable API declarations with Unsupported stubs for native window
// thread operations; Cell[uint] keeps Window Send and not Share without
// unsafe.

/// A window. Linear: end it with Close. Send, not Share: one task reads its
/// events.
export linear view struct Window {
    heap: &Heap
    rec: Cell[uint]
}

/// Opens a window on std's window thread (started by the first call).
/// Errors: Unsupported off Windows, Os(code), OutOfMemory, Canceled
/// (repeatable: a window made meanwhile is destroyed by the thread).
/// Suspends until the thread answers. Allocates the event ring
/// (events × 32 bytes) and the shared record from heap.
export Open(heap: &Heap, io: Io, o: WindowOptions): Error!Window from heap {
    _ = heap
    _ = io
    _ = o
    fail Unsupported
}

/// Asks the window thread to destroy the window; the ring and record go
/// once the thread and the last swapchain on it are done. Never waits,
/// never fails. O(1).
export Window.Close(self) {
    Window(heap: _, rec: _) := self
}

/// The next queued event, or none. O(1); allocates nothing.
export Window.Poll(&self): ?Event {
    _ = self.rec.Load()
    return none
}

/// Waits for the next event. Errors: Closed, Canceled (repeatable: nothing
/// taken). Suspends on a Waker.
export Window.Next(&self, io: Io): Error!Event {
    _ = io
    _ = self.rec.Load()
    fail Unsupported
}

/// Waits until an event is queued, taking none. Errors: Closed, Canceled.
export Window.Wait(&self, io: Io): Error!void {
    _ = io
    _ = self.rec.Load()
    fail Unsupported
}

/// The client size the thread last saw, in physical pixels. O(1).
export Window.Size(&self): (width: int, height: int) {
    _ = self.rec.Load()
    return (width: 0, height: 0)
}

/// Physical pixels per logical pixel: 1.0 at 96 dpi. O(1).
export Window.Scale(&self): f32 {
    _ = self.rec.Load()
    return 1.0
}

/// Whether a close was requested and not yet seen as an event. O(1).
export Window.CloseRequested(&self): bool {
    _ = self.rec.Load()
    return false
}

/// The keys and buttons held now, from the bitmap the window thread keeps:
/// the state to resynchronise from after Dropped. O(1): nine atomic loads.
export Window.Held(&self): InputState {
    _ = self.rec.Load()
    return InputState.New()
}

/// Sets the title; the thread applies the newest value. Errors: Closed.
/// Never waits; copies title into the shared record (at most 255 bytes).
export Window.SetTitle(&self, title: str): Error!void {
    _ = title
    _ = self.rec.Load()
    fail Unsupported
}

/// Sets the client size in physical pixels. Errors: Closed. Never waits.
export Window.SetSize(&self, width: int, height: int): Error!void {
    _ = width
    _ = height
    _ = self.rec.Load()
    fail Unsupported
}

/// Enters or leaves borderless fullscreen. Errors: Closed. Never waits.
export Window.SetFullscreen(&self, f: Fullscreen): Error!void {
    _ = f
    _ = self.rec.Load()
    fail Unsupported
}

/// Sets the cursor shape. Errors: Closed. Never waits.
export Window.SetCursor(&self, c: Cursor): Error!void {
    _ = c
    _ = self.rec.Load()
    fail Unsupported
}

/// Shows or hides the cursor over the client area. Errors: Closed.
export Window.SetCursorMode(&self, m: CursorMode): Error!void {
    _ = m
    _ = self.rec.Load()
    fail Unsupported
}

/// Shows or hides the window. Errors: Closed. Never waits.
export Window.Show(&self, visible: bool): Error!void {
    _ = visible
    _ = self.rec.Load()
    fail Unsupported
}
