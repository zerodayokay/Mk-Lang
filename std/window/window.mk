// Why: programs open windows and read their input from any task, and keep
// drawing while the user drags or resizes them.
// What: Window and its methods (Open, Close, Poll, Next, Wait, Size, Scale,
// CloseRequested, Held, SetTitle, SetSize, SetFullscreen, SetCursor,
// SetCursorMode, Show).
// How: Cell[uint] holds the WindowRec address so Window stays Send and not
// Share; native operations delegate to the dedicated Win32 window thread on
// Windows and fail Unsupported on other targets.

import std.atomic
import std.core
import std.raw

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
    if Target.os != core.Os.Windows {
        fail Unsupported
    }
    return try OpenWindow(heap, io, o)
}

/// Asks the window thread to destroy the window; the ring and record go
/// once the thread and the last swapchain on it are done. Never waits,
/// never fails. O(1).
export Window.Close(self) {
    Window(heap: _, rec: cell) := self
    if Target.os != core.Os.Windows {
        return
    }
    addr := cell.Load()
    if addr == 0 {
        return
    }
    unsafe "addr points to a live WindowRec pinned by Window's ref" {
        r := raw.FromAddr[WindowRec](addr)
        prev := (*r).alive_claims.FetchSub(1, atomic.AcqRel)
        if prev == 1 {
            EnqueueReq(addr, ReqBitDestroy)
        } else {
            EnqueueReq(addr, ReqBitHide)
        }
    }
    ReleaseWindowRecRef(addr)
}

/// The next queued event, or none. O(1); allocates nothing.
export Window.Poll(&self): ?Event {
    addr := self.rec.Load()
    if addr == 0 || Target.os != core.Os.Windows {
        return none
    }
    unsafe "addr points to a live WindowRec pinned by Window's ref" {
        r := raw.FromAddr[WindowRec](addr)
        if ev := (*r).ring.RingPop() {
            if ev.tag == TagCloseRequested {
                (*r).close_requested.Store(0, atomic.Release)
            }
            return some(Event(at: InstantFromNanos(ev.at_nanos), kind: UnpackEventKind(ev)))
        }
    }
    return none
}

/// Waits for the next event. Errors: Closed, Canceled (repeatable: nothing
/// taken). Suspends on a Waker.
export Window.Next(&self, io: Io): Error!Event {
    if Target.os != core.Os.Windows {
        fail Unsupported
    }
    for {
        addr := self.rec.Load()
        if addr == 0 {
            fail Closed
        }
        if ev := self.Poll() {
            return ev
        }
        unsafe "addr points to a live WindowRec pinned by Window's ref" {
            r := raw.FromAddr[WindowRec](addr)
            (*r).event_waker.Wait(io) else { fail Canceled }
        }
    }
}

/// Waits until an event is queued, taking none. Errors: Closed, Canceled.
export Window.Wait(&self, io: Io): Error!void {
    if Target.os != core.Os.Windows {
        fail Unsupported
    }
    for {
        addr := self.rec.Load()
        if addr == 0 {
            fail Closed
        }
        unsafe "addr points to a live WindowRec pinned by Window's ref" {
            r := raw.FromAddr[WindowRec](addr)
            if (*r).ring.RingLen() > 0 {
                return
            }
            (*r).event_waker.Wait(io) else { fail Canceled }
        }
    }
}

/// The client size the thread last saw, in physical pixels. O(1).
export Window.Size(&self): (width: int, height: int) {
    addr := self.rec.Load()
    if addr == 0 || Target.os != core.Os.Windows {
        return (width: 0, height: 0)
    }
    unsafe "addr points to a live WindowRec pinned by Window's ref" {
        r := raw.FromAddr[WindowRec](addr)
        return (width: (*r).width.Load(atomic.Acquire), height: (*r).height.Load(atomic.Acquire))
    }
}

/// Physical pixels per logical pixel: 1.0 at 96 dpi. O(1).
export Window.Scale(&self): f32 {
    addr := self.rec.Load()
    if addr == 0 || Target.os != core.Os.Windows {
        return 1.0
    }
    unsafe "addr points to a live WindowRec pinned by Window's ref" {
        r := raw.FromAddr[WindowRec](addr)
        return f32.FromBits((*r).scale_bits.Load(atomic.Acquire))
    }
}

/// Whether a close was requested and not yet seen as an event. O(1).
export Window.CloseRequested(&self): bool {
    addr := self.rec.Load()
    if addr == 0 || Target.os != core.Os.Windows {
        return false
    }
    unsafe "addr points to a live WindowRec pinned by Window's ref" {
        r := raw.FromAddr[WindowRec](addr)
        return (*r).close_requested.Load(atomic.Acquire) != 0
    }
}

/// The keys and buttons held now, from the bitmap the window thread keeps:
/// the state to resynchronise from after Dropped. O(1): nine atomic loads.
export Window.Held(&self): InputState {
    addr := self.rec.Load()
    if addr == 0 || Target.os != core.Os.Windows {
        return InputState.New()
    }
    unsafe "addr points to a live WindowRec pinned by Window's ref" {
        r := raw.FromAddr[WindowRec](addr)
        k0 := (*r).held_keys[0].Load(atomic.Acquire)
        k1 := (*r).held_keys[1].Load(atomic.Acquire)
        k2 := (*r).held_keys[2].Load(atomic.Acquire)
        k3 := (*r).held_keys[3].Load(atomic.Acquire)
        btns := (*r).held_buttons.Load(atomic.Acquire)
        return InputState(keys: [k0, k1, k2, k3], buttons: btns)
    }
}

/// Sets the title; the thread applies the newest value. Errors: Closed.
/// Never waits; copies title into the shared record (at most 255 bytes).
export Window.SetTitle(&self, title: str): Error!void {
    if Target.os != core.Os.Windows {
        fail Unsupported
    }
    addr := self.rec.Load()
    if addr == 0 {
        fail Closed
    }
    unsafe "addr points to a live WindowRec pinned by Window's ref" {
        (*raw.FromAddr[WindowRec](addr)).setters.SetTitle(title)
    }
    EnqueueReq(addr, ReqBitSetters)
}

/// Sets the client size in physical pixels. Errors: Closed. Never waits.
export Window.SetSize(&self, width: int, height: int): Error!void {
    if Target.os != core.Os.Windows {
        fail Unsupported
    }
    addr := self.rec.Load()
    if addr == 0 {
        fail Closed
    }
    unsafe "addr points to a live WindowRec pinned by Window's ref" {
        (*raw.FromAddr[WindowRec](addr)).setters.SetSize(width, height)
    }
    EnqueueReq(addr, ReqBitSetters)
}

/// Enters or leaves borderless fullscreen. Errors: Closed. Never waits.
export Window.SetFullscreen(&self, f: Fullscreen): Error!void {
    if Target.os != core.Os.Windows {
        fail Unsupported
    }
    addr := self.rec.Load()
    if addr == 0 {
        fail Closed
    }
    unsafe "addr points to a live WindowRec pinned by Window's ref" {
        (*raw.FromAddr[WindowRec](addr)).setters.SetFullscreen(f)
    }
    EnqueueReq(addr, ReqBitSetters)
}

/// Sets the cursor shape. Errors: Closed. Never waits.
export Window.SetCursor(&self, c: Cursor): Error!void {
    if Target.os != core.Os.Windows {
        fail Unsupported
    }
    addr := self.rec.Load()
    if addr == 0 {
        fail Closed
    }
    unsafe "addr points to a live WindowRec pinned by Window's ref" {
        (*raw.FromAddr[WindowRec](addr)).setters.SetCursor(c)
    }
    EnqueueReq(addr, ReqBitSetters)
}

/// Shows or hides the cursor over the client area. Errors: Closed.
export Window.SetCursorMode(&self, m: CursorMode): Error!void {
    if Target.os != core.Os.Windows {
        fail Unsupported
    }
    addr := self.rec.Load()
    if addr == 0 {
        fail Closed
    }
    unsafe "addr points to a live WindowRec pinned by Window's ref" {
        (*raw.FromAddr[WindowRec](addr)).setters.SetCursorMode(m)
    }
    EnqueueReq(addr, ReqBitSetters)
}

/// Shows or hides the window. Errors: Closed. Never waits.
export Window.Show(&self, visible: bool): Error!void {
    if Target.os != core.Os.Windows {
        fail Unsupported
    }
    addr := self.rec.Load()
    if addr == 0 {
        fail Closed
    }
    unsafe "addr points to a live WindowRec pinned by Window's ref" {
        (*raw.FromAddr[WindowRec](addr)).setters.SetVisible(visible)
    }
    EnqueueReq(addr, ReqBitSetters)
}
