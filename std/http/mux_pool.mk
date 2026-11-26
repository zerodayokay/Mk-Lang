// Why: an HTTP/2 connection carries many requests at once, so the client
// shares one per origin between tasks. Claiming, coalescing the first
// requests behind one dial, retiring a connection after GOAWAY or idleness,
// and freeing it must stay race-free against Drive and Client.Close, with no
// lock on the claim path.
//
// What: MuxSlot, the helpers of its state word, the table's construction and
// release, and Client.ClaimMuxOrDial.
//
// How: one 64-bit word per slot packs the state (2 bits: Free, Dialing,
// Live, Dead), the driven bit, 21 bits of references and a 40-bit
// generation, all changed by compare-and-swap. A claim adds a reference only
// to a Live slot; a slot is retired by Live to Dead, keeping its references;
// only the release that moves Dead with one reference to Free takes the
// connection out, bumps the generation and closes it. So a release that sees
// Live never frees, and a claim never adds a reference to a Dead slot.

import std.core
import std.http.h2
import std.raw
import std.rt
import std.sync
import std.time

const MaxMuxSlots: int = 16

const MuxFree: u64 = 0
const MuxDialing: u64 = 1
const MuxLive: u64 = 2
const MuxDead: u64 = 3

const MuxStateMask: u64 = 3
const MuxDrivenBit: u64 = 4
const MuxRefsShift: u64 = 3
const MuxRefsMask: u64 = 0x1f_ffff
const MuxMaxRefs: u64 = 0x1f_ffff
const MuxGenShift: u64 = 24
const MuxGenMask: u64 = 0xff_ffff_ffff

MuxPack(state: u64, driven: bool, refs: u64, gen_: u64): u64 {
    dbit := if driven { MuxDrivenBit } else { Trunc[u64](0) }
    return (state & MuxStateMask) | dbit | ((refs & MuxRefsMask) << MuxRefsShift) | ((gen_ & MuxGenMask) << MuxGenShift)
}

MuxStateOf(w: u64): u64 {
    return w & MuxStateMask
}

MuxDrivenOf(w: u64): bool {
    return (w & MuxDrivenBit) != 0
}

MuxRefsOf(w: u64): u64 {
    return (w >> MuxRefsShift) & MuxRefsMask
}

MuxGenOf(w: u64): u64 {
    return (w >> MuxGenShift) & MuxGenMask
}

struct MuxSlotMeta {
    key: Origin
    last_active: ?time.Instant
    draining: bool
}

struct MuxSlot {
    word: raw.Atomic[u64]
    conn: raw.Atomic[*Box[h2.Conn]]
    dial_wake: sync.Notify
    meta: Mutex[MuxSlotMeta]
}

NewMuxSlot(): MuxSlot {
    return MuxSlot(
        word: raw.Atomic[u64].New(0),
        conn: raw.Atomic[*Box[h2.Conn]].New(raw.Null[Box[h2.Conn]]()),
        dial_wake: sync.Notify.New(),
        meta: Mutex[MuxSlotMeta].New(MuxSlotMeta(key: Origin(b: [0; 280], n: -1), last_active: none, draining: false)),
    )
}

NewMuxSlots2(): Pair[MuxSlot] {
    return Pair[MuxSlot](a: NewMuxSlot(), b: NewMuxSlot())
}

NewMuxSlots4(): Pair[Pair[MuxSlot]] {
    return Pair[Pair[MuxSlot]](a: NewMuxSlots2(), b: NewMuxSlots2())
}

NewMuxSlots8(): Pair[Pair[Pair[MuxSlot]]] {
    return Pair[Pair[Pair[MuxSlot]]](a: NewMuxSlots4(), b: NewMuxSlots4())
}

NewMuxSlots(): Pair[Pair[Pair[Pair[MuxSlot]]]] {
    return Pair[Pair[Pair[Pair[MuxSlot]]]](a: NewMuxSlots8(), b: NewMuxSlots8())
}

MuxSlotAt(p: &Pair[Pair[Pair[Pair[MuxSlot]]]], i: int): &MuxSlot from p {
    q := if i & 8 == 0 { &p.a } else { &p.b }
    r := if i & 4 == 0 { &q.a } else { &q.b }
    s := if i & 2 == 0 { &r.a } else { &r.b }
    return if i & 1 == 0 { &s.a } else { &s.b }
}

FreeMuxSlot(a: &Alloc, s: MuxSlot) {
    MuxSlot(word: _, conn, dial_wake: _, meta) := s
    _ = meta.Into()
    p := conn.Swap(raw.Null[Box[h2.Conn]]())
    if !raw.IsNull(p) {
        box := TakeMuxBox(a, p)
        box.Take(a).Abandon()
    }
}

FreeMuxSlots2(a: &Alloc, p: Pair[MuxSlot]) {
    Pair(a: sa, b: sb) := p
    FreeMuxSlot(a, sa)
    FreeMuxSlot(a, sb)
}

FreeMuxSlots4(a: &Alloc, p: Pair[Pair[MuxSlot]]) {
    Pair(a: sa, b: sb) := p
    FreeMuxSlots2(a, sa)
    FreeMuxSlots2(a, sb)
}

FreeMuxSlots8(a: &Alloc, p: Pair[Pair[Pair[MuxSlot]]]) {
    Pair(a: sa, b: sb) := p
    FreeMuxSlots4(a, sa)
    FreeMuxSlots4(a, sb)
}

FreeMuxSlots(a: &Alloc, p: Pair[Pair[Pair[Pair[MuxSlot]]]]) {
    Pair(a: sa, b: sb) := p
    FreeMuxSlots8(a, sa)
    FreeMuxSlots8(a, sb)
}

// MuxSlot.ClaimRef increments refs if the slot is Live and below MuxMaxRefs.
MuxSlot.ClaimRef(&self): ?u64 {
    for {
        w := self.word.Load()
        if MuxStateOf(w) != MuxLive {
            return none
        }
        refs := MuxRefsOf(w)
        if refs >= MuxMaxRefs {
            return none
        }
        gen_ := MuxGenOf(w)
        nw := MuxPack(MuxLive, MuxDrivenOf(w), refs + 1, gen_)
        if self.word.Cas(w, nw) {
            return some(gen_)
        }
    }
}

// MuxSlot.ClaimDrive sets driven and increments refs if Live and not yet driven.
MuxSlot.ClaimDrive(&self): ?u64 {
    for {
        w := self.word.Load()
        if MuxStateOf(w) != MuxLive || MuxDrivenOf(w) {
            return none
        }
        refs := MuxRefsOf(w)
        if refs >= MuxMaxRefs {
            return none
        }
        gen_ := MuxGenOf(w)
        nw := MuxPack(MuxLive, true, refs + 1, gen_)
        if self.word.Cas(w, nw) {
            return some(gen_)
        }
    }
}

// MuxSlot.MarkDead transitions Live -> Dead while preserving driven and refs.
MuxSlot.MarkDead(&self, expect_gen: u64): bool {
    for {
        w := self.word.Load()
        if MuxGenOf(w) != expect_gen || MuxStateOf(w) != MuxLive {
            return false
        }
        nw := MuxPack(MuxDead, MuxDrivenOf(w), MuxRefsOf(w), expect_gen)
        if self.word.Cas(w, nw) {
            return true
        }
    }
}

// MuxSlot.ReleaseRef decrements refs (optionally clearing driven). If the
// slot is Dead and refs transitions 1 -> 0, it exclusively takes the Box, bumps
// generation, resets the slot to Free, and returns the Box for teardown.
MuxSlot.ReleaseRef(&self, a: &Alloc, clear_driven: bool): ?Box[h2.Conn] from a {
    nc := core.IoMasked(rt.NativeIo())
    for {
        w := self.word.Load()
        st := MuxStateOf(w)
        dr := if clear_driven { false } else { MuxDrivenOf(w) }
        refs := MuxRefsOf(w)
        gen_ := MuxGenOf(w)
        if refs == 0 {
            return none
        }
        if st == MuxDead && refs == 1 {
            inter := MuxPack(MuxDead, false, 0, gen_)
            if !self.word.Cas(w, inter) {
                continue
            }
            p := self.conn.Swap(raw.Null[Box[h2.Conn]]())
            var mg := self.meta.Lock(nc).Must()
            mg.Mut().key = Origin(b: [0; 280], n: -1)
            mg.Mut().draining = false
            mg.Unlock()
            self.word.Store(MuxPack(MuxFree, false, 0, (gen_ + 1) & MuxGenMask))
            if !raw.IsNull(p) {
                return some(TakeMuxBox(a, p))
            }
            return none
        }
        nw := MuxPack(st, dr, refs - 1, gen_)
        if self.word.Cas(w, nw) {
            return none
        }
    }
}

MuxSlot.Touch(&self, expect_gen: u64, now: time.Instant) {
    nc := core.IoMasked(rt.NativeIo())
    var mg := self.meta.Lock(nc).Must()
    if MuxGenOf(self.word.Load()) == expect_gen {
        mg.Mut().last_active = some(now)
    }
    mg.Unlock()
}

// MuxSlot.TryStartDial attempts Free -> Dialing under meta lock and records key.
MuxSlot.TryStartDial(&self, key: Origin, now: time.Instant): ?u64 {
    nc := core.IoMasked(rt.NativeIo())
    var mg := self.meta.Lock(nc).Must()
    defer mg.Unlock()
    for {
        w := self.word.Load()
        if MuxStateOf(w) != MuxFree {
            return none
        }
        gen_ := MuxGenOf(w)
        nw := MuxPack(MuxDialing, false, 0, gen_)
        if self.word.Cas(w, nw) {
            m := mut *mg.Mut()
            m.key = key
            m.last_active = some(now)
            m.draining = false
            return some(gen_)
        }
    }
}

// MuxSlot.FinishDial installs box, sets Live with refs=1, and wakes waiters.
MuxSlot.FinishDial(&self, a: &Alloc, gen_: u64, box: Box[h2.Conn], now: time.Instant): OutOfMemory!void {
    nc := core.IoMasked(rt.NativeIo())
    p := try AllocMuxBox(a, box)
    var mg := self.meta.Lock(nc).Must()
    mg.Mut().last_active = some(now)
    mg.Mut().draining = false
    mg.Unlock()
    _ = self.conn.Swap(p)
    self.word.Store(MuxPack(MuxLive, false, 1, gen_))
    self.dial_wake.NotifyAll()
    self.dial_wake.NotifyOne()
}

// MuxSlot.AbortDial returns a Dialing slot to Free with bumped generation and wakes waiters.
MuxSlot.AbortDial(&self, gen_: u64) {
    nc := core.IoMasked(rt.NativeIo())
    var mg := self.meta.Lock(nc).Must()
    mg.Mut().key = Origin(b: [0; 280], n: -1)
    mg.Mut().draining = false
    mg.Unlock()
    self.word.Store(MuxPack(MuxFree, false, 0, (gen_ + 1) & MuxGenMask))
    self.dial_wake.NotifyAll()
    self.dial_wake.NotifyOne()
}

CloseMuxBox(a: &Alloc, box: Box[h2.Conn], io: Io, limit: time.Duration) {
    c := box.Take(a)
    scope s := io.Scope() {
        s.CancelAfter(limit)
        c.Close(io) else {
            c.Abandon()
        }
    }
}

// MuxLiveFor reports whether a Live slot of key that the claim has not
// tried, and that is not draining, is in the table. O(MaxMuxSlots) loads and
// uncontended locks; allocates nothing.
MuxLiveFor(t: &Pair[Pair[Pair[Pair[MuxSlot]]]], nc: Io, key: &Origin, tried_mask: u32): bool {
    for i in 0..MaxMuxSlots {
        bit := Trunc[u32](1) << Trunc[u32](i)
        sl := MuxSlotAt(t, i)
        if (tried_mask & bit) != 0 || MuxStateOf(sl.word.Load()) != MuxLive {
            continue
        }
        mg := sl.meta.Lock(nc).Must()
        mine := OriginEq(&mg.Ref().key, key) && !mg.Ref().draining
        mg.Unlock()
        if mine {
            return true
        }
    }
    return false
}

enum MuxClaimKind {
    MuxGot(idx: int, gen_: u64)
    MuxDial(idx: int, gen_: u64)
    MuxFull
}

// ClaimMuxOrDial looks for an existing Live slot for key (excluding tried_mask),
// coalesces behind an in-flight Dialing slot for key, or reserves a Free slot
// to dial (evicting an idle unreferenced Live slot if all 16 are occupied).
Client.ClaimMuxOrDial(&self, io: Io, key: Origin, start: time.Instant, tried_mask: u32): Error!MuxClaimKind {
    nc := io.NoCancel()
    a := self.heap.Alloc()
    if key.n < 0 {
        return MuxClaimKind.MuxFull
    }
    for {
        try CancelCheck(io)
        now := time.Now(nc)
        // 1) Look for an active, unexpired Live slot matching key.
        for i in 0..MaxMuxSlots {
            bit := Trunc[u32](1) << Trunc[u32](i)
            if (tried_mask & bit) != 0 {
                continue
            }
            sl := MuxSlotAt(&self.mux, i)
            if gen_ := sl.ClaimRef() {
                var mg := sl.meta.Lock(nc).Must()
                m := mut *mg.Mut()
                matches := OriginEq(&m.key, &key)
                draining := m.draining
                // Refs beyond this claim and Drive's are streams in use: the
                // connection is not idle, whenever it last went quiet.
                wc := sl.word.Load()
                drive_ref := if MuxDrivenOf(wc) { Trunc[u64](1) } else { Trunc[u64](0) }
                in_use := MuxRefsOf(wc) > 1 + drive_ref
                idle_ok := if self.limits.idle.Nanos() <= 0 || in_use {
                    true
                } else if la := m.last_active {
                    now.Since(la).Nanos() < self.limits.idle.Nanos()
                } else {
                    true
                }
                if matches && !draining && idle_ok {
                    mg.Unlock()
                    return MuxClaimKind.MuxGot(idx: i, gen_: gen_)
                }
                if matches && !idle_ok && !draining {
                    m.draining = true
                    mg.Unlock()
                    _ = sl.MarkDead(gen_)
                    if box := sl.ReleaseRef(a, false) {
                        CloseMuxBox(a, box, io, self.limits.close)
                    }
                    continue
                }
                mg.Unlock()
                if box := sl.ReleaseRef(a, false) {
                    CloseMuxBox(a, box, io, self.limits.close)
                }
            }
        }
        // 2) and 3) run under mux_dial, so two claims of one origin never
        // both find nothing and dial two connections: the second finds the
        // first's Dialing slot, or the Live slot it became.
        var dg := self.mux_dial.Lock(nc).Must()
        if MuxLiveFor(&self.mux, nc, &key, tried_mask) {
            dg.Unlock()
            continue
        }
        // 2) Coalesce behind an existing Dialing slot for key within connect deadline.
        var wait_idx := -1
        var dial_gen: u64 = 0
        for i in 0..MaxMuxSlots {
            sl := MuxSlotAt(&self.mux, i)
            w := sl.word.Load()
            if MuxStateOf(w) == MuxDialing {
                var mg := sl.meta.Lock(nc).Must()
                w2 := sl.word.Load()
                same := MuxStateOf(w2) == MuxDialing && OriginEq(&mg.Ref().key, &key)
                mg.Unlock()
                if same {
                    wait_idx = i
                    dial_gen = MuxGenOf(w2)
                    break
                }
            }
        }
        if wait_idx >= 0 {
            dg.Unlock()
            sl := MuxSlotAt(&self.mux, wait_idx)
            for {
                w := sl.word.Load()
                if MuxStateOf(w) != MuxDialing || MuxGenOf(w) != dial_gen {
                    sl.dial_wake.NotifyOne()
                    break
                }
                rem := self.limits.connect.Nanos() - time.Now(nc).Since(start).Nanos()
                if rem <= 0 {
                    fail time.TimedOut
                }
                var expired := false
                scope ws := io.Scope() {
                    ws.CancelAfter(core.DurationOf(rem))
                    sl.dial_wake.Wait(io) else {
                        expired = ws.Expired()
                    }
                }
                if expired {
                    fail time.TimedOut
                }
                try CancelCheck(io)
            }
            continue
        }
        // 3) Claim a Free slot to dial.
        for i in 0..MaxMuxSlots {
            sl := MuxSlotAt(&self.mux, i)
            if gen_ := sl.TryStartDial(key, now) {
                dg.Unlock()
                return MuxClaimKind.MuxDial(idx: i, gen_: gen_)
            }
        }
        dg.Unlock()
        // 4) Try evicting one idle Live slot (refs==0, or refs==1 held only by Drive).
        var evicted := false
        for i in 0..MaxMuxSlots {
            sl := MuxSlotAt(&self.mux, i)
            w := sl.word.Load()
            if MuxStateOf(w) == MuxLive && (MuxRefsOf(w) == 0 && !MuxDrivenOf(w) || MuxRefsOf(w) == 1 && MuxDrivenOf(w)) {
                if gen_ := sl.ClaimRef() {
                    w2 := sl.word.Load()
                    driven := MuxDrivenOf(w2)
                    idle_only := MuxRefsOf(w2) == 1 && !driven || MuxRefsOf(w2) == 2 && driven
                    if idle_only && sl.MarkDead(gen_) {
                        if driven {
                            p := sl.conn.Load()
                            if !raw.IsNull(p) {
                                MuxConnOf(p).Evict(io)
                            }
                        }
                        evicted = true
                    }
                    if box := sl.ReleaseRef(a, false) {
                        CloseMuxBox(a, box, io, self.limits.close)
                    }
                    if evicted {
                        break
                    }
                }
            }
        }
        if evicted {
            continue
        }
        return MuxClaimKind.MuxFull
    }
}
