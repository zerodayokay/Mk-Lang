// Why: swapchains and external renderers need an OS window handle token
// that outlives Window.Close without borrowing Window.
// What: Surface and Window.Surface, Surface.Native, Surface.Size,
// Surface.Close.
// How: holds the WindowRec address as a plain word and increments
// alive_claims and refs on creation so the native window stays alive until
// both Window.Close and the last Surface.Close have run.

import std.atomic
import std.raw

/// A claim on a window's native window: it is not destroyed while a
/// Surface lives, even after Window.Close (it is hidden instead). What a
/// renderer, std.gpu's swapchain or a program's own, holds. Plain words, so
/// it borrows nothing and is Send. Linear: end it with Close.
export linear struct Surface {
    rec: uint
}

/// Claims the native window. Errors: Closed. O(1): one atomic increment.
export Window.Surface(&self): Error!Surface {
    if !WindowSupported() {
        fail Unsupported
    }
    addr := self.rec.Load()
    if addr == 0 {
        fail Closed
    }
    unsafe "addr points to a live WindowRec pinned by Window's ref" {
        r := raw.FromAddr[WindowRec](addr)
        _ = (*r).alive_claims.FetchAdd(1, atomic.AcqRel)
        _ = (*r).refs.FetchAdd(1, atomic.AcqRel)
    }
    return Surface(rec: addr)
}

/// The OS's handles, valid while s lives. O(1).
export Surface.Native(&self): Native {
    addr := self.rec
    if addr == 0 || !WindowSupported() {
        return Native.Win32(hwnd: 0, hinstance: 0)
    }
    unsafe "addr points to a live WindowRec pinned by Surface's ref" {
        r := raw.FromAddr[WindowRec](addr)
        if UnixWindowTarget() {
            return Native.Unix(handle: (*r).native_handle.Load(atomic.Acquire))
        }
        return Native.Win32(
            hwnd: (*r).native_handle.Load(atomic.Acquire),
            hinstance: (*r).hinst_val.Load(atomic.Acquire),
        )
    }
}

/// The client size the window thread last saw. O(1).
export Surface.Size(&self): (width: int, height: int) {
    addr := self.rec
    if addr == 0 || !WindowSupported() {
        return (width: 0, height: 0)
    }
    unsafe "addr points to a live WindowRec pinned by Surface's ref" {
        r := raw.FromAddr[WindowRec](addr)
        return (width: (*r).width.Load(atomic.Acquire), height: (*r).height.Load(atomic.Acquire))
    }
}

/// Ends the claim; the last one after Window.Close destroys the native
/// window. Never fails. O(1).
export Surface.Close(self) {
    Surface(rec: addr) := self
    if !WindowSupported() {
        return
    }
    if addr == 0 {
        return
    }
    unsafe "addr points to a live WindowRec pinned by Surface's ref" {
        r := raw.FromAddr[WindowRec](addr)
        prev := (*r).alive_claims.FetchSub(1, atomic.AcqRel)
        if prev == 1 {
            EnqueueReq(addr, ReqBitDestroy)
        }
    }
    ReleaseWindowRecRef(addr)
}
