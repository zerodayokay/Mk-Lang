// Why: caller tasks send open, close and setter requests to the window thread
// without suspending or blocking on a Mutex inside the OS thread loop.
// What: Doorbell (0->1 atomic notification flag), SetterState (coalesced
// window property setters) and SpinQueue[T] (spin-locked request queue).
// How: Doorbell uses a SeqCst Swap(true) so only the 0->1 transition rings
// the OS doorbell; SetterState and SpinQueue use atomic.Spin.

import std.atomic

// Doorbell is a coalesced 0->1 wake flag shared between callers and the
// window thread.
struct Doorbell {
    pending: atomic.Atomic[u8]
}

DoorbellNew(): Doorbell {
    return Doorbell(pending: atomic.Atomic[u8].New(0))
}

// Ring marks the doorbell pending and returns true only on the 0->1
// transition so at most one wake message is posted per drain cycle.
Doorbell.Ring(&self): bool {
    return self.pending.Swap(1, atomic.SeqCst) == 0
}

// Clear resets the doorbell before the thread drains the request queue.
Doorbell.Clear(&self) {
    self.pending.Store(0, atomic.SeqCst)
}

// IsPending reports whether the doorbell flag is currently set.
Doorbell.IsPending(&self): bool {
    return self.pending.Load(atomic.SeqCst) != 0
}

// Dirty bit masks for coalesced SetterState updates.
const DirtyTitle: u32 = 1
const DirtySize: u32 = 2
const DirtyFullscreen: u32 = 4
const DirtyCursor: u32 = 8
const DirtyCursorMode: u32 = 16
const DirtyVisible: u32 = 32

// SetterData holds the newest coalesced window property updates.
struct SetterData {
    dirty: u32
    title_buf: [255]u8
    title_len: u8
    width: int
    height: int
    fullscreen: Fullscreen
    cursor: Cursor
    cursor_mode: CursorMode
    visible: bool
}

SetterDataNew(): SetterData {
    return SetterData(
        dirty: 0,
        title_buf: [0; 255],
        title_len: 0,
        width: 0,
        height: 0,
        fullscreen: Fullscreen.Windowed,
        cursor: Cursor.Arrow,
        cursor_mode: CursorMode.Normal,
        visible: true,
    )
}

// SetterState protects SetterData under a spin lock for the window thread.
view struct SetterState {
    lock: atomic.Spin[SetterData]
}

SetterStateNew(): SetterState {
    return SetterState(lock: atomic.Spin[SetterData].New(SetterDataNew()))
}

// SetTitle stores up to 255 UTF-8 bytes of title and sets DirtyTitle.
SetterState.SetTitle(&self, title: str) {
    src := title.AsBytes()
    var n := src.Len()
    if n > 255 {
        n = 255
        // Trim back inside a UTF-8 continuation sequence (10xxxxxx) so the
        // truncated slice remains valid UTF-8.
        for n > 0 && (src[n] & 0xC0) == 0x80 {
            n -= 1
        }
    }
    var g := self.lock.Lock()
    d := g.Mut()
    for i in 0..n {
        d.title_buf[i] = src[i]
    }
    d.title_len = Trunc[u8](n)
    d.dirty = d.dirty | DirtyTitle
    g.Unlock()
}

// SetSize stores the newest physical pixel dimensions and sets DirtySize.
SetterState.SetSize(&self, width: int, height: int) {
    var g := self.lock.Lock()
    d := g.Mut()
    d.width = width
    d.height = height
    d.dirty = d.dirty | DirtySize
    g.Unlock()
}

// SetFullscreen stores the newest fullscreen mode and sets DirtyFullscreen.
SetterState.SetFullscreen(&self, f: Fullscreen) {
    var g := self.lock.Lock()
    d := g.Mut()
    d.fullscreen = f
    d.dirty = d.dirty | DirtyFullscreen
    g.Unlock()
}

// SetCursor stores the newest cursor shape and sets DirtyCursor.
SetterState.SetCursor(&self, c: Cursor) {
    var g := self.lock.Lock()
    d := g.Mut()
    d.cursor = c
    d.dirty = d.dirty | DirtyCursor
    g.Unlock()
}

// SetCursorMode stores the newest cursor mode and sets DirtyCursorMode.
SetterState.SetCursorMode(&self, m: CursorMode) {
    var g := self.lock.Lock()
    d := g.Mut()
    d.cursor_mode = m
    d.dirty = d.dirty | DirtyCursorMode
    g.Unlock()
}

// SetVisible stores the newest visibility flag and sets DirtyVisible.
SetterState.SetVisible(&self, visible: bool) {
    var g := self.lock.Lock()
    d := g.Mut()
    d.visible = visible
    d.dirty = d.dirty | DirtyVisible
    g.Unlock()
}

// Take snapshots all dirty fields and clears the dirty mask atomically.
SetterState.Take(&self): SetterData {
    var g := self.lock.Lock()
    d := g.Mut()
    snap := *d
    d.dirty = 0
    g.Unlock()
    return snap
}

// SpinQueue is a small spin-locked FIFO ring for u64 request tokens,
// paired with a Doorbell so senders only notify on empty->non-empty wake.
view struct SpinQueue {
    lock: atomic.Spin[SpinQueueBuf]
    bell: Doorbell
}

struct SpinQueueBuf {
    buf: mut []u64
    head: int
    len: int
}

SpinQueueNew(a: &Arena, cap: int): OutOfMemory!SpinQueue from a {
    c := if cap < 8 { 8 } else { cap }
    buf := try a.Slice[u64](c)
    return SpinQueue(
        lock: atomic.Spin[SpinQueueBuf].New(SpinQueueBuf(buf: buf, head: 0, len: 0)),
        bell: DoorbellNew(),
    )
}

// Push appends item to the queue. Returns (ok, should_ring_doorbell).
SpinQueue.Push(&self, item: u64): (ok: bool, ring: bool) {
    var g := self.lock.Lock()
    q := g.Mut()
    cap := q.buf.Len()
    if q.len >= cap {
        g.Unlock()
        return (ok: false, ring: false)
    }
    idx := (q.head + q.len) % cap
    q.buf[idx] = item
    q.len += 1
    g.Unlock()
    return (ok: true, ring: self.bell.Ring())
}

// Pop removes and returns the oldest item, or none if empty.
SpinQueue.Pop(&self): ?u64 {
    var g := self.lock.Lock()
    q := g.Mut()
    if q.len == 0 {
        g.Unlock()
        return none
    }
    item := q.buf[q.head]
    q.head = (q.head + 1) % q.buf.Len()
    q.len -= 1
    g.Unlock()
    return some(item)
}
