// Why: requests to the same HTTPS origin share one driven QUIC connection.
// What: the bounded HTTP/3 connection pool and its reference management.
// How: a mutex protects the slots. One caller reserves a slot and dials
// outside the lock. Retiring a slot prevents new borrowers; the final
// request or driver releases its storage.

import std.core
import std.http.h3
import std.net
import std.raw
import std.time

struct H3Conn {
    mux: *net.Mux
    session: Box[h3.Session]
}

// The Mux is immutable behind its owned pointer and may be borrowed by
// any task holding a connection reference. Reclamation follows the pool.
unsafe impl Send for H3Conn {}
unsafe impl Share for H3Conn {}

const H3Slots = 16

struct H3Slot {
    origin: Origin
    conn: *H3Conn
    dialing: bool
    retired: bool
    driving: bool
    requests: int
    last: ?time.Instant
}

// Each pointer is owned by the pool and reclaimed only under its mutex,
// after all request and driver references have been released.
unsafe impl Send for H3Slot {}

EmptyH3Slot(): H3Slot {
    return H3Slot(origin: Origin(b: [0; 280], n: -1), conn: raw.Null[H3Conn](), dialing: false, retired: false, driving: false, requests: 0, last: none)
}

NewH3Slots(): [16]H3Slot {
    return [
        EmptyH3Slot(), EmptyH3Slot(), EmptyH3Slot(), EmptyH3Slot(),
        EmptyH3Slot(), EmptyH3Slot(), EmptyH3Slot(), EmptyH3Slot(),
        EmptyH3Slot(), EmptyH3Slot(), EmptyH3Slot(), EmptyH3Slot(),
        EmptyH3Slot(), EmptyH3Slot(), EmptyH3Slot(), EmptyH3Slot(),
    ]
}

H3Store[T](a: &Alloc, v: T): OutOfMemory!*T {
    unsafe "the allocation has T's size and alignment, and this call initializes it exactly once" {
        p := a.Bytes(SizeOf[T](), AlignOf[T]())
        if raw.IsNull(p) {
            fail OutOfMemory
        }
        q := raw.PtrCast[u8, T](p)
        raw.Write(q, v)
        return q
    }
}

H3Take[T](a: &Alloc, p: *T): T {
    unsafe "the caller exclusively owns the initialized allocation returned by H3Store on a and no borrower remains" {
        v := raw.Read(p)
        a.FreeBytes(raw.PtrCast[T, u8](p), SizeOf[T](), AlignOf[T]())
        return v
    }
}

H3ConnRef(p: *H3Conn): &H3Conn {
    unsafe "the caller owns a request or driver reference, so the pool cannot reclaim the initialized connection" {
        return &*p
    }
}

H3Mux(p: *net.Mux): &net.Mux {
    unsafe "the owning H3Conn or the dial creating it keeps this shared Mux alive and stable until the Session is closed" {
        return &*p
    }
}

DropH3(a: &Alloc, p: *H3Conn) {
    if !raw.IsNull(p) {
        H3Conn(mux, session) := H3Take(a, p)
        session.Take(a).Close(a)
        H3Take(a, mux).Abandon()
    }
}

CloseH3(a: &Alloc, p: *H3Conn, io: Io, limit: time.Duration, code: u64) {
    if !raw.IsNull(p) {
        H3Conn(mux, session) := H3Take(a, p)
        session.Take(a).Close(a)
        m := H3Take(a, mux)
        scope closing := io.Scope() {
            closing.CancelAfter(limit)
            m.Close(io, code) else { m.Abandon() }
        }
    }
}

// H3PutBack returns the connection for teardown once the last borrower
// of a retired slot leaves. Indices remain stable while borrowed.
Client.H3PutBack(&self, io: Io, idx: int, driver: bool, retire: bool): *H3Conn {
    nc := io.NoCancel()
    state := self.H3State().Must()
    var g := state.pool.Lock(nc).Must()
    s := mut (*g.Mut())[idx]
    if driver {
        s.driving = false
    } else {
        s.requests -= 1
        s.last = some(time.Now(nc))
    }
    s.retired = s.retired || retire
    var p := raw.Null[H3Conn]()
    if s.retired && s.requests == 0 && !s.driving {
        p = s.conn
        *s = EmptyH3Slot()
    }
    g.Unlock()
    state.WakeClaims(io)
    return p
}

Client.H3Retire(&self, io: Io, key: &Origin) {
    nc := io.NoCancel()
    var g := self.H3State().Must().pool.Lock(nc).Must()
    for i in 0..H3Slots {
        slot := mut (*g.Mut())[i]
        if !raw.IsNull(slot.conn) && OriginEq(&slot.origin, key) {
            slot.retired = true
        }
    }
    g.Unlock()
}

Client.H3Running(&self): bool {
    return self.drive_active.Load() != 0 && self.drive_stop.Load() == 0
}

DriveH3(client: &Client, conn: &H3Conn, io: Io, idx: int) {
    var code := h3.H3NoError
    sess := conn.session.Ref()
    idle := client.limits.idle.Nanos()
    quic_idle := H3Mux(conn.mux).Limits().idle.Nanos()
    ping := quic_idle / 2
    interval := if ping > 0 && (idle <= 0 || ping < idle) { ping } else if idle > 0 { idle } else { time.Seconds(90).Nanos() }
    for {
        var retry := false
        var alive := false
        var quiet := false
        scope reading := io.Scope() {
            reading.CancelAfter(time.Nanos(interval))
            sess.Serve(io) else e {
                match e {
                    Canceled { retry = reading.Expired() }
                    h3.Violation(code: c, stream: _) { code = c }
                    _ {}
                }
            }
        }
        if retry && client.H3Running() {
            nc := io.NoCancel()
            g := client.H3State().Must().pool.Lock(nc).Must()
            s := &(*g.Ref())[idx]
            if !s.retired {
                age := if last := s.last { time.Now(nc).Since(last).Nanos() } else { i64(0) }
                alive = s.requests > 0 || idle <= 0 || age < idle
                quiet = ping > 0 && age >= ping
            }
            g.Unlock()
        }
        if !alive {
            break
        }
        if quiet {
            var failed := false
            scope keepalive := io.Scope() {
                keepalive.CancelAfter(time.Nanos(interval))
                sess.KeepAlive(io) else { failed = true }
            }
            if failed {
                break
            }
        }
    }
    closed := client.H3PutBack(io, idx, true, true)
    CloseH3(client.heap.Alloc(), closed, io.NoCancel(), client.limits.close, code)
}

DialH3Conn(h: &Heap, io: Io, lim: &ClientLimits, dialer: &net.MuxDialer, host: str, routes: &AltRoutes): Error!*H3Conn {
    arena := try Arena.New(h)
    defer arena.Free()
    addrs := try net.Resolve(&arena, io, host)
    var last: Error = net.Refused
    for i in 0..routes.n {
        for addr in addrs.Items() {
            var to := addr
            to.port = Trunc[u16](routes.ports[i].port)
            if allow := lim.allow {
                if !allow(&to) {
                    continue
                }
            }
            m := dialer.Dial(h, io, to, host, "h3") else e {
                try CancelCheck(io)
                last = e
                continue
            }
            a := h.Alloc()
            mp := H3Store(a, m) else {
                m.Abandon()
                fail OutOfMemory
            }
            onerr H3Take(a, mp).Abandon()
            session := h3.Session.Client(h, io, H3Mux(mp), lim.header_bytes) else e {
                fail MapH3Error(e)
            }
            owned := H3Conn(mux: mp, session: session)
            return H3Store(a, owned) else {
                H3Conn(mux: _, session: sb) := owned
                sb.Take(a).Close(a)
                fail OutOfMemory
            }
        }
    }
    fail last
}

struct H3ClientState {
    pool: Mutex[[16]H3Slot]
    routes: Mutex[[64]AltOrigin]
    epoch: raw.Atomic[u32]
}

NewH3ClientState(): H3ClientState {
    return H3ClientState(pool: Mutex[[16]H3Slot].New(NewH3Slots()), routes: Mutex[[64]AltOrigin].New([NoAltOrigin; 64]), epoch: raw.Atomic[u32].New(0))
}

// Keep a pool change visible to every task between checking and parking.
H3ClientState.WakeClaims(&self, io: Io) {
    _ = self.epoch.Add(1)
    core.SyncWake(io, &self.epoch, true)
}

FreeH3ClientState(a: &Alloc, p: *H3ClientState) {
    H3ClientState(pool, routes, epoch: _) := H3Take(a, p)
    _ = routes.Into()
    slots := pool.Into()
    for sl in &slots[..] {
        DropH3(a, sl.conn)
    }
}

Client.H3State(&self): ?&H3ClientState from self {
    p := self.h3_state.Load()
    if raw.IsNull(p) {
        return none
    }
    unsafe "only Client.Close removes this initialized state, and it cannot consume the client while self is borrowed" {
        return some(&*p)
    }
}

Client.EnsureH3State(&self): OutOfMemory!&H3ClientState from self {
    if state := self.H3State() {
        return state
    }
    a := self.heap.Alloc()
    state := NewH3ClientState()
    p := H3Store(a, state) else {
        H3ClientState(pool, routes, epoch: _) := state
        _ = pool.Into()
        _ = routes.Into()
        fail OutOfMemory
    }
    if !self.h3_state.Cas(raw.Null[H3ClientState](), p) {
        FreeH3ClientState(a, p)
    }
    return self.H3State().Must()
}

struct H3Claim {
    index: int
    conn: *H3Conn
}

// Returns none before sending any request if QUIC is unavailable,
// disabled, or a dial failed. Each occupied slot is scoped to one origin.
Client.ClaimH3(&self, io: Io, key: Origin, host: str, port: int): Error!?H3Claim {
    dialer := self.limits.h3 else { return none }
    if key.n < 0 {
        return none
    }
    if !self.limits.h3_direct && self.H3State().IsNone() {
        return none
    }
    state := try self.EnsureH3State()
    nc := io.NoCancel()
    for {
        epoch := state.epoch.Load()
        if !self.H3Running() {
            return none
        }
        try CancelCheck(io)
        var g := state.pool.Lock(nc).Must()
        routes := self.H3Routes(io, &key, port)
        if routes.clear {
            g.Unlock()
            return none
        }
        var empty := -1
        var wait := false
        var claim: ?H3Claim = none
        for i in 0..H3Slots {
            s := mut (*g.Mut())[i]
            if s.retired {
                continue
            }
            if s.origin.n < 0 {
                empty = i
                continue
            }
            if !OriginEq(&s.origin, &key) {
                continue
            }
            if s.dialing || s.requests >= (if self.limits.h2_streams > 0 { self.limits.h2_streams } else { 1 }) {
                wait = true
            } else {
                s.requests += 1
                claim = some(H3Claim(index: i, conn: s.conn))
            }
            break
        }
        if got := claim {
            g.Unlock()
            conn := H3ConnRef(got.conn)
            if H3Mux(conn.mux).Usable(io) && conn.session.Ref().Goaway().IsNone() {
                return some(got)
            }
            p := self.H3PutBack(io, got.index, false, true)
            CloseH3(self.heap.Alloc(), p, nc, self.limits.close, h3.H3NoError)
            continue
        }
        if wait {
            g.Unlock()
            if core.SyncWait(io, &state.epoch, epoch) {
                fail Canceled
            }
            continue
        }
        if empty < 0 || routes.n == 0 {
            g.Unlock()
            return none
        }
        slot := mut (*g.Mut())[empty]
        slot.origin = key
        slot.dialing = true
        g.Unlock()
        var p := raw.Null[H3Conn]()
        var err: ?Error = none
        scope connect := io.Scope() {
            connect.CancelAfter(self.limits.h3_connect)
            p = DialH3Conn(self.heap, io, &self.limits, &dialer, host, &routes) else e {
                err = some(e)
                raw.Null[H3Conn]()
            }
        }
        var canceled := false
        core.SyncCancel(io) else { canceled = true }
        var oom := false
        if e := err {
            match e {
                OutOfMemory { oom = true }
                _ {}
            }
        }
        if raw.IsNull(p) && !canceled && !oom {
            self.H3Broken(io, key)
        }
        var finish := state.pool.Lock(nc).Must()
        result := mut (*finish.Mut())[empty]
        published := !raw.IsNull(p) && self.H3Running() && !canceled
        if published {
            result.conn = p
            result.dialing = false
            result.requests = 1
            result.last = some(time.Now(nc))
        } else {
            *result = EmptyH3Slot()
        }
        finish.Unlock()
        state.WakeClaims(io)
        if published {
            self.drive_wake.NotifyOne()
            return some(H3Claim(index: empty, conn: p))
        }
        DropH3(self.heap.Alloc(), p)
        try CancelCheck(io)
        if oom {
            fail OutOfMemory
        }
        return none
    }
}
