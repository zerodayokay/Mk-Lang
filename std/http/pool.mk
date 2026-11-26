// Why: a client that reconnects for every request pays a handshake each
// time; kept-alive connections (RFC 9112 9.3) must be shared between tasks
// without one task's long read blocking the others.
//
// What: Slot and SlotState, the table of slots inside the Client (NewSlots,
// SlotAt, FreeSlots), Client.Claim, which takes a slot for an origin, and
// Release.
//
// How: a connection lives in one Slot of the client for its whole life, and
// an Exchange's Line holds a raw pointer to its slot (mem_slot.mk). A slot
// is claimed by a compare-and-swap on its busy word, and only the task
// that claimed it locks its state. More below.

// So a connection never moves between the client and an Exchange's arena,
// and Close marks the slot idle or empties it. The table is MaxSlots slots
// built from nested pairs, so Close can release each by destructuring. A
// request that finds every slot busy gets a connection of its own, which
// its Exchange closes.

import std.net
import std.raw
import std.time

// MaxSlots is the number of connections a Client keeps: idle ones for
// reuse and busy ones serving a request.
const MaxSlots = 16

// SlotState is a slot's connection, its origin, and when it went idle.
struct SlotState {
    conn: ?net.Conn
    key: Origin
    since: ?time.Instant
}

// Slot is one pooled connection. busy is 1 while a task owns the slot.
// The compare-and-swap that sets it is the acquire edge, and the Store(0) that
// clears it the release edge: every write the owner made to st, the
// connection and its idle time included, happens before the next owner's
// claim reads them (raw.Atomic's operations are seq_cst, which is both).
struct Slot {
    busy: raw.Atomic[u32]
    st: Mutex[SlotState]
}

// Pair is two of a kind: the slot table is Pair nested four deep.
struct Pair[T] {
    a: T
    b: T
}

NewSlot(): Slot {
    return Slot(busy: raw.Atomic[u32].New(0), st: Mutex[SlotState].New(SlotState(conn: none, key: Origin(b: [0; 280], n: -1), since: none)))
}

NewSlots2(): Pair[Slot] {
    return Pair[Slot](a: NewSlot(), b: NewSlot())
}

NewSlots4(): Pair[Pair[Slot]] {
    return Pair[Pair[Slot]](a: NewSlots2(), b: NewSlots2())
}

NewSlots8(): Pair[Pair[Pair[Slot]]] {
    return Pair[Pair[Pair[Slot]]](a: NewSlots4(), b: NewSlots4())
}

// NewSlots is an empty table of MaxSlots slots.
NewSlots(): Pair[Pair[Pair[Pair[Slot]]]] {
    return Pair[Pair[Pair[Pair[Slot]]]](a: NewSlots8(), b: NewSlots8())
}

// SlotAt is slot i of the table, i in 0..MaxSlots.
SlotAt(p: &Pair[Pair[Pair[Pair[Slot]]]], i: int): &Slot from p {
    q := if i & 8 == 0 { &p.a } else { &p.b }
    r := if i & 4 == 0 { &q.a } else { &q.b }
    s := if i & 2 == 0 { &r.a } else { &r.b }
    return if i & 1 == 0 { &s.a } else { &s.b }
}

// FreeSlot releases a slot's connection without speaking to the peer.
FreeSlot(s: Slot) {
    Slot(busy: _, st) := s
    SlotState(conn, key: _, since: _) := st.Into()
    DropConn(conn)
}

FreeSlots2(p: Pair[Slot]) {
    Pair(a, b) := p
    FreeSlot(a)
    FreeSlot(b)
}

FreeSlots4(p: Pair[Pair[Slot]]) {
    Pair(a, b) := p
    FreeSlots2(a)
    FreeSlots2(b)
}

FreeSlots8(p: Pair[Pair[Pair[Slot]]]) {
    Pair(a, b) := p
    FreeSlots4(a)
    FreeSlots4(b)
}

// FreeSlots releases every slot of the table.
FreeSlots(p: Pair[Pair[Pair[Pair[Slot]]]]) {
    Pair(a, b) := p
    FreeSlots8(a)
    FreeSlots8(b)
}

// Claim is a slot a request took: its index (-1 when every slot is busy)
// and whether it holds an idle connection of the request's origin.
struct Claim {
    slot: int
    reused: bool
}

// Claim takes a slot for key: an idle connection of key's origin when one
// is there, not idle past the limit, and reuse is asked, else an empty
// slot, else the first idle slot of another origin (or of key's own when
// reuse is not asked), emptied; -1 when every slot is busy. Idle connections past the limit are closed on the way. At most
// idle_connections slots are used. O(MaxSlots) claims and locks, each
// uncontended; allocates nothing.
Client.Claim(&self, io: Io, key: Origin, reuse: bool): Claim {
    n := if self.limits.idle_connections < MaxSlots { self.limits.idle_connections } else { MaxSlots }
    if key.n < 0 || n <= 0 {
        return Claim(slot: -1, reused: false)
    }
    now := time.Now(io)
    var empty := -1
    var other := -1
    for i in 0..n {
        s := SlotAt(&self.slots, i)
        if !s.busy.Cas(0, 1) {
            continue
        }
        var g := s.st.Lock(io.NoCancel()) else {
            Panic("http.Client: a NoCancel lock was cancelled")
        }
        st := g.Mut()
        if st.conn.IsSome() {
            var stale := true
            if t := st.since {
                stale = now.Since(t).Nanos() >= self.limits.idle.Nanos()
            }
            if stale {
                DropConn(Replace(mut st.conn, none))
            }
        }
        has := st.conn.IsSome()
        mine := reuse && has && OriginEq(&st.key, &key)
        g.Unlock()
        if mine {
            // Give back the candidates claimed on the way.
            if empty >= 0 {
                SlotAt(&self.slots, empty).busy.Store(0)
            }
            if other >= 0 {
                SlotAt(&self.slots, other).busy.Store(0)
            }
            return Claim(slot: i, reused: true)
        }
        if !has && empty < 0 {
            empty = i
            continue
        }
        if has && other < 0 {
            other = i
            continue
        }
        s.busy.Store(0)
    }
    // Keep the first empty slot, else the first idle slot of another origin
    // (emptied), and give back the other one claimed.
    if empty >= 0 {
        if other >= 0 {
            SlotAt(&self.slots, other).busy.Store(0)
        }
        return Claim(slot: empty, reused: false)
    }
    if other >= 0 {
        s := SlotAt(&self.slots, other)
        var g := s.st.Lock(io.NoCancel()) else {
            Panic("http.Client: a NoCancel lock was cancelled")
        }
        DropConn(Replace(mut g.Mut().conn, none))
        g.Unlock()
        return Claim(slot: other, reused: false)
    }
    return Claim(slot: -1, reused: false)
}

// ClaimIdle takes the slot of an idle connection of key's origin, not idle
// past the limit, and returns its index, or -1 with no slot taken. With
// HTTP/2 on, such a connection is one whose server chose http/1.1 by ALPN,
// so it is used before a new connection is dialled. O(MaxSlots) claims,
// each uncontended; allocates nothing.
Client.ClaimIdle(&self, io: Io, key: Origin): int {
    n := if self.limits.idle_connections < MaxSlots { self.limits.idle_connections } else { MaxSlots }
    if key.n < 0 {
        return -1
    }
    now := time.Now(io)
    for i in 0..n {
        s := SlotAt(&self.slots, i)
        if !s.busy.Cas(0, 1) {
            continue
        }
        g := s.st.Lock(io.NoCancel()) else {
            Panic("http.Client: a NoCancel lock was cancelled")
        }
        st := g.Ref()
        var fresh := false
        if t := st.since {
            fresh = now.Since(t).Nanos() < self.limits.idle.Nanos()
        }
        mine := fresh && st.conn.IsSome() && OriginEq(&st.key, &key)
        g.Unlock()
        if mine {
            return i
        }
        s.busy.Store(0)
    }
    return -1
}

// Release ends a request's hold on slot s. When keep, the connection stays
// for reuse, idle from now; otherwise it is taken out for the caller to
// close (none when the slot holds none).
Release(s: &Slot, io: Io, keep: bool): ?net.Conn {
    var g := s.st.Lock(io.NoCancel()) else {
        Panic("http.Client: a NoCancel lock was cancelled")
    }
    st := g.Mut()
    kept := keep && st.conn.IsSome()
    if kept {
        st.since = some(time.Now(io))
    }
    out: ?net.Conn = if kept { none } else { Replace(mut st.conn, none) }
    g.Unlock()
    s.busy.Store(0)
    return out
}
