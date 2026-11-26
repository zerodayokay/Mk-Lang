// Why: window events must transfer from the OS window thread to the
// consumer task without locks, coalescing high-rate motion/wheel/resize
// events and reserving 25% of the ring for key, button, text, focus and
// close events.
// What: RawEvent (32-byte packed event), RingSlot, Ring (SPSC atomic ring)
// and Accumulator (coalescing + 75% reserve + Dropped accounting).
// How: each slot holds four atomic u64 words; the producer writes slot words
// unordered/relaxed then releases tail; the consumer acquires tail, reads
// slot words, then releases head.

import std.atomic

// Event tag constants for the 32-byte RawEvent wire layout.
const TagCloseRequested: u8 = 0
const TagResized: u8 = 1
const TagMoved: u8 = 2
const TagScaleChanged: u8 = 3
const TagMinimized: u8 = 4
const TagRestored: u8 = 5
const TagFocus: u8 = 6
const TagKey: u8 = 7
const TagText: u8 = 8
const TagPointer: u8 = 9
const TagButton: u8 = 10
const TagWheel: u8 = 11
const TagHover: u8 = 12
const TagDropped: u8 = 13

// RawEvent is one 32-byte ring entry (8 + 8 + 8 + 8 bytes).
struct RawEvent {
    at_nanos: i64
    tag: u8
    b0: u8
    b1: u8
    b2: u8
    u32_0: u32
    p0: u64
    p1: u64
}

// RingSlot is a 32-byte shared atomic slot in the SPSC event ring.
struct RingSlot {
    w0: atomic.Atomic[u64]
    w1: atomic.Atomic[u64]
    w2: atomic.Atomic[u64]
    w3: atomic.Atomic[u64]
}

RingSlotNew(): RingSlot {
    return RingSlot(
        w0: atomic.Atomic[u64].New(0),
        w1: atomic.Atomic[u64].New(0),
        w2: atomic.Atomic[u64].New(0),
        w3: atomic.Atomic[u64].New(0),
    )
}

PackWord1(tag: u8, b0: u8, b1: u8, b2: u8, u32_0: u32): u64 {
    return u64(tag) | (u64(b0) << 8) | (u64(b1) << 16) | (u64(b2) << 24) | (u64(u32_0) << 32)
}

UnpackRawEvent(w0: u64, w1: u64, w2: u64, w3: u64): RawEvent {
    return RawEvent(
        at_nanos: Trunc[i64](w0),
        tag: Trunc[u8](w1 & 0xFF),
        b0: Trunc[u8]((w1 >> 8) & 0xFF),
        b1: Trunc[u8]((w1 >> 16) & 0xFF),
        b2: Trunc[u8]((w1 >> 24) & 0xFF),
        u32_0: Trunc[u32](w1 >> 32),
        p0: w2,
        p1: w3,
    )
}

PackMods(m: Mods, down: bool, repeat: bool): u8 {
    var v: u8 = 0
    if down { v = v | 1 }
    if repeat { v = v | 2 }
    if m.shift { v = v | 4 }
    if m.ctrl { v = v | 8 }
    if m.alt { v = v | 16 }
    if m.meta { v = v | 32 }
    return v
}

UnpackMods(flags: u8): Mods {
    return Mods(
        shift: (flags & 4) != 0,
        ctrl: (flags & 8) != 0,
        alt: (flags & 16) != 0,
        meta: (flags & 32) != 0,
    )
}

// PackEventKind packs at_nanos and k into a 32-byte RawEvent.
PackEventKind(at_nanos: i64, k: EventKind): RawEvent {
    match k {
        EventKind.CloseRequested {
            return RawEvent(at_nanos: at_nanos, tag: TagCloseRequested, b0: 0, b1: 0, b2: 0, u32_0: 0, p0: 0, p1: 0)
        }
        EventKind.Resized(width, height) {
            return RawEvent(at_nanos: at_nanos, tag: TagResized, b0: 0, b1: 0, b2: 0, u32_0: 0, p0: Trunc[u64](Cast[i64](width)), p1: Trunc[u64](Cast[i64](height)))
        }
        EventKind.Moved(x, y) {
            return RawEvent(at_nanos: at_nanos, tag: TagMoved, b0: 0, b1: 0, b2: 0, u32_0: 0, p0: Trunc[u64](Cast[i64](x)), p1: Trunc[u64](Cast[i64](y)))
        }
        EventKind.ScaleChanged(scale) {
            return RawEvent(at_nanos: at_nanos, tag: TagScaleChanged, b0: 0, b1: 0, b2: 0, u32_0: scale.Bits(), p0: 0, p1: 0)
        }
        EventKind.Minimized {
            return RawEvent(at_nanos: at_nanos, tag: TagMinimized, b0: 0, b1: 0, b2: 0, u32_0: 0, p0: 0, p1: 0)
        }
        EventKind.Restored {
            return RawEvent(at_nanos: at_nanos, tag: TagRestored, b0: 0, b1: 0, b2: 0, u32_0: 0, p0: 0, p1: 0)
        }
        EventKind.Focus(on) {
            return RawEvent(at_nanos: at_nanos, tag: TagFocus, b0: if on { 1 } else { 0 }, b1: 0, b2: 0, u32_0: 0, p0: 0, p1: 0)
        }
        EventKind.Key(key, layout, scancode, down, repeat, mods) {
            return RawEvent(at_nanos: at_nanos, tag: TagKey, b0: Trunc[u8](KeyCode(key)), b1: Trunc[u8](KeyCode(layout)), b2: PackMods(mods, down, repeat), u32_0: scancode, p0: 0, p1: 0)
        }
        EventKind.Text(ch) {
            return RawEvent(at_nanos: at_nanos, tag: TagText, b0: 0, b1: 0, b2: 0, u32_0: Cast[u32](ch), p0: 0, p1: 0)
        }
        EventKind.Pointer(x, y) {
            return RawEvent(at_nanos: at_nanos, tag: TagPointer, b0: 0, b1: 0, b2: 0, u32_0: x.Bits(), p0: u64(y.Bits()), p1: 0)
        }
        EventKind.Button(button, down, x, y, mods) {
            return RawEvent(at_nanos: at_nanos, tag: TagButton, b0: Trunc[u8](ButtonCode(button)), b1: PackMods(mods, down, false), b2: 0, u32_0: x.Bits(), p0: u64(y.Bits()), p1: 0)
        }
        EventKind.Wheel(dx, dy) {
            return RawEvent(at_nanos: at_nanos, tag: TagWheel, b0: 0, b1: 0, b2: 0, u32_0: dx.Bits(), p0: u64(dy.Bits()), p1: 0)
        }
        EventKind.Hover(inside) {
            return RawEvent(at_nanos: at_nanos, tag: TagHover, b0: if inside { 1 } else { 0 }, b1: 0, b2: 0, u32_0: 0, p0: 0, p1: 0)
        }
        EventKind.Dropped(count) {
            return RawEvent(at_nanos: at_nanos, tag: TagDropped, b0: 0, b1: 0, b2: 0, u32_0: 0, p0: Trunc[u64](Cast[i64](count)), p1: 0)
        }
    }
}

// UnpackEventKind decodes r back into an EventKind.
UnpackEventKind(r: RawEvent): EventKind {
    if r.tag == TagCloseRequested { return EventKind.CloseRequested }
    if r.tag == TagResized { return EventKind.Resized(width: int(Trunc[i64](r.p0)), height: int(Trunc[i64](r.p1))) }
    if r.tag == TagMoved { return EventKind.Moved(x: int(Trunc[i64](r.p0)), y: int(Trunc[i64](r.p1))) }
    if r.tag == TagScaleChanged { return EventKind.ScaleChanged(scale: f32.FromBits(r.u32_0)) }
    if r.tag == TagMinimized { return EventKind.Minimized }
    if r.tag == TagRestored { return EventKind.Restored }
    if r.tag == TagFocus { return EventKind.Focus(on: r.b0 != 0) }
    if r.tag == TagKey {
        return EventKind.Key(
            key: KeyFromCode(int(r.b0)),
            layout: KeyFromCode(int(r.b1)),
            scancode: r.u32_0,
            down: (r.b2 & 1) != 0,
            repeat: (r.b2 & 2) != 0,
            mods: UnpackMods(r.b2),
        )
    }
    if r.tag == TagText {
        cp := if r.u32_0 <= 0x10FFFF && (r.u32_0 < 0xD800 || r.u32_0 > 0xDFFF) { r.u32_0 } else { 0xFFFD }
        return EventKind.Text(ch: Cast[char](cp))
    }
    if r.tag == TagPointer {
        return EventKind.Pointer(x: f32.FromBits(r.u32_0), y: f32.FromBits(Trunc[u32](r.p0)))
    }
    if r.tag == TagButton {
        return EventKind.Button(
            button: ButtonFromCode(int(r.b0)),
            down: (r.b1 & 1) != 0,
            x: f32.FromBits(r.u32_0),
            y: f32.FromBits(Trunc[u32](r.p0)),
            mods: UnpackMods(r.b1),
        )
    }
    if r.tag == TagWheel {
        return EventKind.Wheel(dx: f32.FromBits(r.u32_0), dy: f32.FromBits(Trunc[u32](r.p0)))
    }
    if r.tag == TagHover { return EventKind.Hover(inside: r.b0 != 0) }
    return EventKind.Dropped(count: int(Trunc[i64](r.p0)))
}

// IsHighPriority reports whether tag may enter the upper 25% reserve.
IsHighPriority(tag: u8): bool {
    return tag == TagKey || tag == TagButton || tag == TagText || tag == TagFocus || tag == TagCloseRequested || tag == TagDropped
}

// IsCoalescable reports whether tag coalesces in the accumulator.
IsCoalescable(tag: u8): bool {
    return tag == TagPointer || tag == TagWheel || tag == TagResized || tag == TagMoved || tag == TagScaleChanged || tag == TagHover
}

// ValidRingCapacity rounds or checks cap is a power of two in 64..65536.
ValidRingCapacity(cap: int): int {
    if cap <= 0 { return 4096 }
    if cap < 64 { return 64 }
    if cap > 65536 { return 65536 }
    var p := 64
    for p < cap {
        p *= 2
    }
    return p
}

// Ring is a lock-free SPSC queue of 32-byte RawEvent slots.
linear struct Ring {
    slots: HeapList[RingSlot]
    cap: int
    mask: u64
    reserve_at: int
    head: atomic.Atomic[u64]
    tail: atomic.Atomic[u64]
}

// RingNew allocates an SPSC Ring of cap slots (power of two in 64..65536) in h.
RingNew(h: &Heap, cap: int): OutOfMemory!Ring {
    c := ValidRingCapacity(cap)
    a := h.Alloc()
    var slots := HeapList[RingSlot].New(a)
    onerr slots.Free(a)
    for _ in 0..c {
        try slots.Push(a, RingSlotNew())
    }
    return Ring(
        slots: slots,
        cap: c,
        mask: Cast[u64](c - 1),
        reserve_at: (c * 3) / 4,
        head: atomic.Atomic[u64].New(0),
        tail: atomic.Atomic[u64].New(0),
    )
}

// RingClose releases the ring's backing buffer into h.
Ring.RingClose(self, h: &Heap) {
    Ring(slots, cap: _, mask: _, reserve_at: _, head: _, tail: _) := self
    slots.Free(h.Alloc())
}

// RingPush tries to push ev into the ring, enforcing the 75% reserve for
// non-high-priority events. Called only by the single producer.
Ring.RingPush(&self, ev: RawEvent): bool {
    h := self.head.Load(atomic.Acquire)
    t := self.tail.Load(atomic.Relaxed)
    used := Trunc[int](t -% h)
    if used >= self.cap {
        return false
    }
    if !IsHighPriority(ev.tag) && used >= self.reserve_at {
        return false
    }
    idx := Trunc[int](t & self.mask)
    s := &self.slots.Items()[idx]
    s.w0.Store(Trunc[u64](ev.at_nanos), atomic.Relaxed)
    s.w1.Store(PackWord1(ev.tag, ev.b0, ev.b1, ev.b2, ev.u32_0), atomic.Relaxed)
    s.w2.Store(ev.p0, atomic.Relaxed)
    s.w3.Store(ev.p1, atomic.Relaxed)
    self.tail.Store(t +% 1, atomic.Release)
    return true
}

// RingPop pops the next event, or none if empty. Called only by the consumer.
Ring.RingPop(&self): ?RawEvent {
    t := self.tail.Load(atomic.Acquire)
    h := self.head.Load(atomic.Relaxed)
    if h == t {
        return none
    }
    idx := Trunc[int](h & self.mask)
    s := &self.slots.Items()[idx]
    w0 := s.w0.Load(atomic.Relaxed)
    w1 := s.w1.Load(atomic.Relaxed)
    w2 := s.w2.Load(atomic.Relaxed)
    w3 := s.w3.Load(atomic.Relaxed)
    self.head.Store(h +% 1, atomic.Release)
    return some(UnpackRawEvent(w0, w1, w2, w3))
}

// RingLen returns the approximate number of queued events.
Ring.RingLen(&self): int {
    t := self.tail.Load(atomic.Acquire)
    h := self.head.Load(atomic.Acquire)
    return Trunc[int](t -% h)
}

// Accumulator coalesces motion, wheel, hover, resize, move and scale events
// on the producer side and tracks dropped events when the ring fills.
struct Accumulator {
    has_pending: bool
    pending: RawEvent
    dropped: int
}

AccumulatorNew(): Accumulator {
    return Accumulator(
        has_pending: false,
        pending: RawEvent(at_nanos: 0, tag: 0, b0: 0, b1: 0, b2: 0, u32_0: 0, p0: 0, p1: 0),
        dropped: 0,
    )
}

// FlushDropped attempts to write a Dropped(count) marker before any new event.
Accumulator.FlushDropped(mut self, r: &Ring): bool {
    if self.dropped == 0 {
        return true
    }
    d := RawEvent(at_nanos: self.pending.at_nanos, tag: TagDropped, b0: 0, b1: 0, b2: 0, u32_0: 0, p0: Trunc[u64](Cast[i64](self.dropped)), p1: 0)
    if r.RingPush(d) {
        self.dropped = 0
        return true
    }
    return false
}

// Flush writes any pending coalesced event (and Dropped count) to r,
// returning whether anything was written.
Accumulator.Flush(mut self, r: &Ring): bool {
    var wrote := false
    if self.dropped > 0 {
        if self.FlushDropped(r) {
            wrote = true
        } else {
            return false
        }
    }
    if self.has_pending {
        if r.RingPush(self.pending) {
            self.has_pending = false
            wrote = true
        }
    }
    return wrote
}

// Record records ev, coalescing consecutive compatible events and flushing
// before non-coalescing or different-kind events to preserve order.
Accumulator.Record(mut self, r: &Ring, ev: RawEvent): bool {
    if IsCoalescable(ev.tag) {
        if self.has_pending && self.pending.tag == ev.tag {
            if ev.tag == TagWheel {
                dx := f32.FromBits(self.pending.u32_0) + f32.FromBits(ev.u32_0)
                dy := f32.FromBits(Trunc[u32](self.pending.p0)) + f32.FromBits(Trunc[u32](ev.p0))
                self.pending.u32_0 = dx.Bits()
                self.pending.p0 = u64(dy.Bits())
                self.pending.at_nanos = ev.at_nanos
            } else {
                self.pending = ev
            }
            return false
        }
        var wrote := false
        if self.has_pending {
            if self.FlushDropped(r) && r.RingPush(self.pending) {
                wrote = true
            } else {
                self.dropped += 1
            }
        }
        self.pending = ev
        self.has_pending = true
        return wrote
    }
    // Non-coalescable (or immediate) event: flush pending first so order holds.
    var wrote := false
    if self.has_pending {
        if self.FlushDropped(r) && r.RingPush(self.pending) {
            wrote = true
        } else {
            self.dropped += 1
        }
        self.has_pending = false
    }
    if self.dropped > 0 {
        if self.FlushDropped(r) {
            wrote = true
        } else {
            self.dropped += 1
            return wrote
        }
    }
    if r.RingPush(ev) {
        return true
    }
    self.dropped += 1
    return wrote
}
