// Why: a simulated run needs a network that never opens a real socket,
// reorders and splits traffic as a seed says, and answers alike on every
// target, so network code can be tested for its schedules.
//
// What: the World's ports, connection ends and datagrams (Port, End, Mark,
// Dgram) and the entries behind them: NetListen, NetAccept, NetDial,
// NetRecv, NetSend, NetShutdown, NetUdp, NetSendTo, NetRecvFrom,
// NetResolve and NetClose, plus ConnCut, DataCut and ArrivedBy, which weigh
// a deadline-cut wait against the instant of the cut.
//
// How: one loopback host. Each send is split at a seeded point and each
// segment gets a seeded arrival instant, so streams stay ordered but come
// back short, and datagrams reorder. The model in detail follows.
//
// Model. One host, loopback only: a port is bound by a listener or a UDP
// socket. dial connects at once to a bound listener (Refused otherwise)
// and queues the server end for accept. A connection is two ends, 2k and
// 2k + 1, each with an inbound byte FIFO and arrival marks: a send is cut
// at a seeded point into one or two segments, each arriving at
// max(the previous arrival, now + a seeded 20 to 500 us), so the stream
// keeps its order and a receive comes back short as TCP's does. A datagram
// gets its own seeded latency, so datagrams reorder (UDP keeps no order).
// A receive takes what has arrived, else moves the clock to the first
// arrival; with nothing in flight it is a deadlock in a one-task run and
// answers Canceled.

import std.core
import std.raw

// An arrival mark: the inbound bytes before end (an offset into data)
// arrive at at.
struct Mark {
    end: int
    at: i64
}

// A connection end: data[head..] is inbound and not yet received; marks
// are in order of end and at. open: this end is not closed; reset: the
// connection was reset; wshut: this end shut its sending side
// (rt.SockShutdown 1 or 2), so after the bytes in flight the peer's stream
// ends and this end's sends are Reset, as natively; rshut: this end shut
// its receiving side (0 or 2), so its receives answer 0; port: its local
// port; pipe: the end of a simulated pipe or Pty (proc.mk), whose sends
// are PipeSend's.
struct End {
    used: bool
    pipe: bool
    open: bool
    reset: bool
    wshut: bool
    rshut: bool
    data: Vec[u8]
    head: int
    marks: Vec[Mark]
    last: i64
    port: int
    // The clock at which a dial queued this server end on its
    // listener's backlog (0 for any other end), which a weighed accept's
    // take compares with its cut (ConnCut).
    born: i64
}

// A bound port: a listener (udp false) with its backlog of server ends,
// or a UDP socket.
struct Port {
    used: bool
    num: int
    udp: bool
    backlog: Vec[int]
    busy_until: i64
    connected: bool
    peer: core.IoAddr
}

struct Dgram {
    used: bool
    to: int
    from: core.IoAddr
    data: Vec[u8]
    at: i64
    seq: u64
}

const LatLo: i64 = 20_000
const LatHi: i64 = 500_000

// ---------------------------------------------------------------------------
// Addresses.

// ParsePort reads the port after the last ':' of addr: 0 to 65535, or -1.
ParsePort(addr: str): int {
    b := addr.AsBytes()
    var i := b.Len() - 1
    for i >= 0 && b[i] != b':' {
        i -= 1
    }
    if i < 0 || i == b.Len() - 1 {
        return -1
    }
    var v := 0
    for k in i + 1..b.Len() {
        c := b[k]
        if c < b'0' || c > b'9' {
            return -1
        }
        v = v * 10 + int(c - b'0')
        if v > 65535 {
            return -1
        }
    }
    return v
}

// HostOf is addr's host: the bytes before the last ':', brackets dropped.
HostOf(addr: str): []u8 {
    b := addr.AsBytes()
    var i := b.Len() - 1
    for i >= 0 && b[i] != b':' {
        i -= 1
    }
    if i < 0 {
        return b
    }
    var h := b[0..i]
    if h.Len() >= 2 && h[0] == b'[' && h[h.Len() - 1] == b']' {
        h = h[1..h.Len() - 1]
    }
    return h
}

// ParseV4 reads a dotted IPv4 address into out[0..4]; false when it is not one.
ParseV4(h: []u8, out: mut [16]u8): bool {
    var part := 0
    var v := 0
    var digits := 0
    for k in 0..h.Len() {
        c := h[k]
        if c == b'.' {
            if digits == 0 || part == 3 {
                return false
            }
            out[part] = Trunc[u8](v)
            part += 1
            v = 0
            digits = 0
        } else if c >= b'0' && c <= b'9' {
            v = v * 10 + int(c - b'0')
            digits += 1
            if v > 255 || digits > 3 {
                return false
            }
        } else {
            return false
        }
    }
    if digits == 0 || part != 3 {
        return false
    }
    out[3] = Trunc[u8](v)
    return true
}

StrEq(a: []u8, b: str): bool {
    return BytesEq(a, b.AsBytes())
}

// Resolve answers the hosts a one-host network knows: numeric IPv4,
// localhost and the empty host as 127.0.0.1, ::1 and :: as ::1; false
// for any other name.
ResolveHost(h: []u8, out: mut core.IoAddr): bool {
    var ip: [16]u8 = [0; 16]
    if h.Len() == 0 || StrEq(h, "localhost") {
        ip[0] = 127
        ip[3] = 1
        *out = core.IoAddr(ip: ip, port: 0, v6: false, zone: 0)
        return true
    }
    if StrEq(h, "::1") || StrEq(h, "::") {
        ip[15] = 1
        *out = core.IoAddr(ip: ip, port: 0, v6: true, zone: 0)
        return true
    }
    if ParseV4(h, mut ip) {
        *out = core.IoAddr(ip: ip, port: 0, v6: false, zone: 0)
        return true
    }
    return false
}

// ---------------------------------------------------------------------------
// Ports.

// FindPort is the bound port num of kind udp, or -1.
FindPort(w: &World, num: int, udp: bool): int {
    for i in 0..w.ports.Len() {
        p := w.ports.Get(i)
        if p.used && p.num == num && p.udp == udp {
            return i
        }
    }
    return -1
}

// Bind takes port num (0: the next free one from 49152); the port's
// index, or -1 with the error in e.
Bind(w: mut World, num: int, udp: bool, e: mut core.SysErr): int {
    var n := num
    if n == 0 {
        for k in 0..16384 {
            cand := 49152 + (w.next_port - 49152 + k) % 16384
            if FindPort(&*w, cand, udp) < 0 {
                n = cand
                w.next_port = 49152 + (cand - 49152 + 1) % 16384
                break
            }
        }
        if n == 0 {
            *e = core.SysErr.AddrInUse
            return -1
        }
    } else if FindPort(&*w, n, udp) >= 0 {
        *e = core.SysErr.AddrInUse
        return -1
    }
    port := Port(
        used: true,
        num: n,
        udp: udp,
        backlog: Vec[int].Empty(),
        busy_until: 0,
        connected: false,
        peer: core.IoAddr(ip: [0; 16], port: 0, v6: false, zone: 0),
    )
    for i in 0..w.ports.Len() {
        if !w.ports.Get(i).used {
            w.ports.Set(i, port)
            return i
        }
    }
    if !w.ports.Push(port) {
        *e = core.SysErr.NoSpace
        return -1
    }
    return w.ports.Len() - 1
}

FreePort(w: mut World, i: int) {
    p := w.ports.Mut(i)
    p.backlog.Free()
    p.used = false
}

// ---------------------------------------------------------------------------
// Connections.

FreshEnd(port: int): End {
    return End(used: true, pipe: false, open: true, reset: false, wshut: false, rshut: false, data: Vec[u8].Empty(), head: 0,
        marks: Vec[Mark].Empty(), last: 0, port: port, born: 0)
}

FreeEnd(e: mut End) {
    e.data.Free()
    e.marks.Free()
    e.used = false
    e.open = false
}

// NewPair adds two ends; the first's index (even), or -1 when out of memory.
NewPair(w: mut World, cport: int, sport: int): int {
    var k := 0
    for k + 1 < w.ends.Len() {
        if !w.ends.Get(k).used && !w.ends.Get(k + 1).used {
            w.ends.Set(k, FreshEnd(cport))
            w.ends.Set(k + 1, FreshEnd(sport))
            return k
        }
        k += 2
    }
    if !w.ends.Reserve(w.ends.Len() + 2) {
        return -1
    }
    _ = w.ends.Push(FreshEnd(cport))
    _ = w.ends.Push(FreshEnd(sport))
    return w.ends.Len() - 2
}

Peer(i: int): int {
    return i ^ 1
}

// Arrived is how many inbound bytes of end e have arrived by now.
Arrived(e: &End, now: i64): int {
    var upto := e.head
    for k in 0..e.marks.Len() {
        m := e.marks.Get(k)
        if m.at <= now {
            upto = m.end
        } else {
            break
        }
    }
    return upto - e.head
}

// Compact drops received bytes and their marks once they are more than half
// the buffer, so a long stream costs O(bytes in flight) memory.
Compact(e: mut End) {
    var drop := 0
    for drop < e.marks.Len() && e.marks.Get(drop).end <= e.head {
        drop += 1
    }
    e.marks.DropFront(drop)
    if e.head == 0 || e.head * 2 < e.data.Len() {
        return
    }
    rest := e.data.Len() - e.head
    unsafe "head <= len: the bytes head..len move to the start" {
        if rest > 0 {
            raw.Move(e.data.p, raw.Offset(e.data.p, e.head), rest)
        }
    }
    for k in 0..e.marks.Len() {
        e.marks.Mut(k).end -= e.head
    }
    e.data.Truncate(rest)
    e.head = 0
}

LastArrival(w: &World): i64 {
    var last: i64 = 0
    for i in 0..w.ends.Len() {
        e := w.ends.Get(i)
        if e.used && e.marks.Len() > 0 {
            at := e.marks.Get(e.marks.Len() - 1).at
            if at > last {
                last = at
            }
        }
    }
    for i in 0..w.dgrams.Len() {
        d := w.dgrams.Get(i)
        if d.used && d.at > last {
            last = d.at
        }
    }
    return last
}

// ---------------------------------------------------------------------------
// The entries' bodies.

NetListen(w: mut World, addr: str): core.IoResult {
    num := ParsePort(addr)
    var a := core.IoAddr(ip: [0; 16], port: 0, v6: false, zone: 0)
    if num < 0 || !ResolveHost(HostOf(addr), mut a) {
        return Fail(core.SysErr.InvalidAddress)
    }
    var e := core.SysErr.None
    pi := Bind(w, num, false, mut e)
    if pi < 0 {
        return Fail(e)
    }
    r := NewHand(w, HListen, pi, false)
    if r.err != core.SysErr.None {
        FreePort(w, pi)
        return r
    }
    return core.IoResult(value: r.value, err: core.SysErr.None, os: i64(w.ports.Get(pi).num))
}

NetAccept(w: mut World, h: uint): core.IoResult {
    i := FindHand(&*w, h, HListen)
    if i < 0 {
        return Fail(core.SysErr.Closed)
    }
    pi := w.handles.Get(i).obj
    if w.ports.Get(pi).backlog.Len() == 0 {
        return Fail(core.SysErr.Canceled)
    }
    se := w.ports.Mut(pi).backlog.RemoveAt(0)
    r := NewHand(w, HConn, se, true)
    if r.err != core.SysErr.None {
        _ = w.ports.Mut(pi).backlog.Push(se)
    }
    return r
}

// NetLatency draws a delivery delay from w.net_faults.latency_lo..latency_hi.
NetLatency(w: mut World): i64 {
    lo0 := w.net_faults.latency_lo.Nanos()
    hi0 := w.net_faults.latency_hi.Nanos()
    if lo0 == LatLo && hi0 == LatHi {
        return w.Latency(LatLo, LatHi)
    }
    lo := if lo0 < 0 { 0 } else { lo0 }
    hi := if hi0 < lo { lo } else { hi0 }
    if hi == lo {
        return lo
    }
    return w.Latency(lo, hi)
}

SetBlackhole(w: mut World, port: int, on: bool) {
    if on {
        for i in 0..w.blackholes.Len() {
            if w.blackholes.Get(i) == port {
                return
            }
        }
        _ = w.blackholes.Push(port)
        return
    }
    for i in 0..w.blackholes.Len() {
        if w.blackholes.Get(i) == port {
            _ = w.blackholes.RemoveAt(i)
            return
        }
    }
}

IsBlackholed(w: &World, port: int): bool {
    for i in 0..w.blackholes.Len() {
        if w.blackholes.Get(i) == port {
            return true
        }
    }
    return false
}

NetRebind(w: mut World, port: int) {
    pi := FindPort(&*w, port, true)
    if pi < 0 {
        return
    }
    var n := 0
    for k in 0..16384 {
        cand := 49152 + (w.next_port - 49152 + k) % 16384
        if cand != port && FindPort(&*w, cand, true) < 0 {
            n = cand
            w.next_port = 49152 + (cand - 49152 + 1) % 16384
            break
        }
    }
    if n > 0 {
        w.ports.Mut(pi).num = n
    }
}

DgramMatchesPeer(from: core.IoAddr, peer: core.IoAddr): bool {
    if from.port != peer.port || from.v6 != peer.v6 {
        return false
    }
    for i in 0..16 {
        if from.ip[i] != peer.ip[i] {
            return false
        }
    }
    return true
}

// rtt is set to the dial's round trip, which the caller charges.
NetDial(w: mut World, addr: str, rtt: mut i64): core.IoResult {
    num := ParsePort(addr)
    if num <= 0 {
        return Fail(core.SysErr.InvalidAddress)
    }
    var a := core.IoAddr(ip: [0; 16], port: 0, v6: false, zone: 0)
    if !ResolveHost(HostOf(addr), mut a) {
        return Fail(core.SysErr.NotFound)
    }
    pi := FindPort(&*w, num, false)
    *rtt = NetLatency(w) * 2
    if pi < 0 {
        return Fail(core.SysErr.Refused)
    }
    var e := core.SysErr.None
    cp := Bind(w, 0, false, mut e)
    if cp < 0 {
        return Fail(e)
    }
    cnum := w.ports.Get(cp).num
    // The client's port is only a number: it is not a listener.
    FreePort(w, cp)
    k := NewPair(w, cnum, num)
    if k < 0 {
        return Fail(core.SysErr.NoSpace)
    }
    w.ends.Mut(k + 1).born = w.clock
    if !w.ports.Mut(pi).backlog.Push(k + 1) {
        FreeEnd(w.ends.Mut(k))
        FreeEnd(w.ends.Mut(k + 1))
        return Fail(core.SysErr.NoSpace)
    }
    r := NewHand(w, HConn, k, true)
    if r.err != core.SysErr.None {
        _ = w.ports.Mut(pi).backlog.RemoveAt(w.ports.Get(pi).backlog.Len() - 1)
        FreeEnd(w.ends.Mut(k))
        FreeEnd(w.ends.Mut(k + 1))
    }
    return r
}

// ConnCut: an accept on listener h whose wait a deadline fire at cut
// cancelled (NetBlock) would take a connection queued after cut, or none
// is queued, so natively the deadline came first and the accept answers
// Canceled with nothing taken; a connection queued at or before cut is
// taken, as natively it was before the deadline. False for a
// handle that is no listener, which the retry answers. O(1).
ConnCut(w: &World, h: uint, cut: i64): bool {
    i := FindHand(&*w, h, HListen)
    if i < 0 {
        return false
    }
    p := w.ports.Get(w.handles.Get(i).obj)
    return p.backlog.Len() == 0 || w.ends.Get(p.backlog.Get(0)).born > cut
}

// DataCut is ConnCut for a receive on connection h: its first byte not
// yet received arrived after cut, or no byte is there and the stream has
// not ended. A reset, a shut receiving side and the stream's end carry no
// instant, so they are taken as before. O(marks).
DataCut(w: &World, h: uint, cut: i64): bool {
    hi := FindHand(&*w, h, HConn)
    if hi < 0 {
        return false
    }
    k := w.handles.Get(hi).obj
    e := w.ends.Get(k)
    if e.reset || e.rshut {
        return false
    }
    for m in 0..e.marks.Len() {
        if e.marks.Get(m).end > e.head {
            return e.marks.Get(m).at > cut
        }
    }
    return w.ends.Get(Peer(k)).open && !w.ends.Get(Peer(k)).wshut
}

// ArrivedBy is Arrived for connection h at instant at: how many of its
// unread bytes had arrived by then; 0 for a handle that is no connection.
// A weighed receive reads no more than these. O(marks).
ArrivedBy(w: &World, h: uint, at: i64): int {
    hi := FindHand(&*w, h, HConn)
    if hi < 0 {
        return 0
    }
    return Arrived(&w.ends.Get(w.handles.Get(hi).obj), at)
}

// ResetConn breaks both ends of end i's connection.
ResetConn(w: mut World, i: int) {
    w.ends.Mut(i).reset = true
    w.ends.Mut(Peer(i)).reset = true
}

// InFlight is when the connection end of h next receives bytes, when none
// has arrived but some are in flight: the first one's arrival; else 0
// (bytes arrived, nothing in flight, or h no connection of this run).
InFlight(w: &World, h: uint): i64 {
    hi := FindHand(&*w, h, HConn)
    if hi < 0 {
        return 0
    }
    e := w.ends.Get(w.handles.Get(hi).obj)
    if e.reset || e.rshut || e.marks.Len() == 0 || Arrived(&e, w.clock) > 0 {
        return 0
    }
    return e.marks.Get(0).at
}

// DgramInFlight is when the datagram socket h next receives, when no
// datagram to it has arrived but some are in flight: the first arrival;
// else 0 (one arrived, none in flight, or h no datagram socket of this
// run). O(datagrams in flight).
DgramInFlight(w: &World, h: uint): i64 {
    hi := FindHand(&*w, h, HUdp)
    if hi < 0 {
        return 0
    }
    pi := w.handles.Get(hi).obj
    p := w.ports.Get(pi)
    var first: i64 = 0
    for k in 0..w.dgrams.Len() {
        d := w.dgrams.Get(k)
        if d.used && d.to == pi && (!p.connected || DgramMatchesPeer(d.from, p.peer)) {
            if d.at <= w.clock {
                return 0
            }
            if first == 0 || d.at < first {
                first = d.at
            }
        }
    }
    return first
}

// hold: the caller holds the baton, so bytes still in flight answer
// NotYet with their arrival time instead of moving the clock.
NetRecv(w: mut World, h: uint, buf: mut []u8, hold: bool): core.IoResult {
    hi := FindHand(&*w, h, HConn)
    if hi < 0 {
        return Fail(core.SysErr.Closed)
    }
    i := w.handles.Get(hi).obj
    if w.ends.Get(i).reset {
        return Fail(core.SysErr.Reset)
    }
    if buf.Len() == 0 || w.ends.Get(i).rshut {
        return Ok(0)
    }
    var got := Arrived(&w.ends.Get(i), w.clock)
    if got == 0 {
        e := w.ends.Get(i)
        if e.marks.Len() > 0 && hold {
            return core.IoResult(value: e.marks.Get(0).at, err: core.SysErr.Canceled, os: NotYet)
        } else if e.marks.Len() > 0 {
            w.clock = e.marks.Get(0).at
            got = Arrived(&w.ends.Get(i), w.clock)
        } else if !w.ends.Get(Peer(i)).open || w.ends.Get(Peer(i)).wshut {
            return Ok(0)
        } else {
            return Fail(core.SysErr.Canceled)
        }
    }
    n := if got > buf.Len() { buf.Len() } else { got }
    e := w.ends.Mut(i)
    unsafe "data[head..head + n] has arrived and is in bounds" {
        raw.Copy(raw.DataMut(mut buf[0..n]), raw.Offset(e.data.p, e.head), n)
    }
    e.head += n
    Compact(e)
    return Ok(i64(n))
}

NetSend(w: mut World, h: uint, data: []u8): core.IoResult {
    hi := FindHand(&*w, h, HConn)
    if hi < 0 {
        uh := FindHand(&*w, h, HUdp)
        if uh >= 0 {
            pi := w.handles.Get(uh).obj
            if w.ports.Get(pi).connected {
                return NetSendTo(w, h, data, w.ports.Get(pi).peer)
            }
            return Fail(core.SysErr.InvalidAddress)
        }
        return Fail(core.SysErr.Closed)
    }
    i := w.handles.Get(hi).obj
    if w.ends.Get(i).pipe {
        return PipeSend(w, i, data)
    }
    if w.ends.Get(i).reset || w.ends.Get(i).wshut {
        // A send after this end's own shutdown is EPIPE or WSAESHUTDOWN
        // natively, which std.sys answers as Reset on both targets.
        return Fail(core.SysErr.Reset)
    }
    p := Peer(i)
    if !w.ends.Get(p).open {
        // The peer is gone: a write there is reset (EPIPE, WSAECONNRESET).
        ResetConn(w, i)
        return Fail(core.SysErr.Reset)
    }
    if data.Len() == 0 {
        return Ok(0)
    }
    cap := w.net_faults.stream_buffer
    if cap > 0 {
        room := cap - (w.ends.Get(p).data.Len() - w.ends.Get(p).head)
        if room <= 0 {
            return core.IoResult(value: 0, err: core.SysErr.Canceled, os: Full)
        }
        n := if data.Len() > room { room } else { data.Len() }
        return Deliver(w, p, data[0..n])
    }
    return Deliver(w, p, data)
}

// Deliver appends data to end p's inbound bytes, cut at a seeded point
// into one or two segments, each arriving at a seeded latency after the
// previous one (the model above). NoSpace when memory runs out.
Deliver(w: mut World, p: int, data: []u8): core.IoResult {
    cut := if data.Len() > 1 && (w.Next() & 1) == 1 { 1 + Cast[int](w.Below(Cast[u64](data.Len() - 1))) } else { data.Len() }
    lat1 := NetLatency(w)
    lat2 := NetLatency(w)
    clock := w.clock
    e := w.ends.Mut(p)
    base := e.data.Len()
    if !e.marks.Reserve(e.marks.Len() + 2) || !AppendBytes(mut e.data, data) {
        return Fail(core.SysErr.NoSpace)
    }
    at := if clock + lat1 < e.last { e.last } else { clock + lat1 }
    _ = e.marks.Push(Mark(end: base + cut, at: at))
    e.last = at
    if cut < data.Len() {
        at2 := if clock + lat2 < e.last { e.last } else { clock + lat2 }
        _ = e.marks.Push(Mark(end: base + data.Len(), at: at2))
        e.last = at2
    }
    return Ok(i64(data.Len()))
}

// CloseEnd closes end i: the peer reads the end of the stream after the
// bytes in flight; when both ends are closed the connection is freed.
CloseEnd(w: mut World, i: int) {
    w.ends.Mut(i).open = false
    KidsInputClosed(w, i)
    p := Peer(i)
    if !w.ends.Get(p).open {
        FreeEnd(w.ends.Mut(i))
        FreeEnd(w.ends.Mut(p))
    }
}

// NetShutdown shuts down the connection end h: how 0 its receiving side,
// 1 its sending side, 2 both (rt.SockShutdown's codes; any other is
// Other). A second shutdown succeeds; a reset connection is Reset, and h
// not a connection is Closed (a listener's shutdown is ENOTCONN natively,
// which no std caller makes). O(1); allocates nothing.
NetShutdown(w: mut World, h: uint, how: i64): core.IoResult {
    if how < 0 || how > 2 {
        return Fail(core.SysErr.Other)
    }
    hi := FindHand(&*w, h, HConn)
    if hi < 0 {
        return Fail(core.SysErr.Closed)
    }
    i := w.handles.Get(hi).obj
    e := w.ends.Mut(i)
    if e.reset {
        return Fail(core.SysErr.Reset)
    }
    if how != 1 {
        e.rshut = true
    }
    if how != 0 {
        e.wshut = true
    }
    return Ok(0)
}

NetUdp(w: mut World, addr: core.IoAddr): core.IoResult {
    var e := core.SysErr.None
    pi := Bind(w, int(addr.port), true, mut e)
    if pi < 0 {
        return Fail(e)
    }
    r := NewHand(w, HUdp, pi, true)
    if r.err != core.SysErr.None {
        FreePort(w, pi)
        return r
    }
    return core.IoResult(value: r.value, err: core.SysErr.None, os: i64(w.ports.Get(pi).num))
}

NetUdpConnect(w: mut World, h: uint, to: core.IoAddr): core.IoResult {
    hi := FindHand(&*w, h, HUdp)
    if hi < 0 {
        return Fail(core.SysErr.Closed)
    }
    pi := w.handles.Get(hi).obj
    w.ports.Mut(pi).connected = true
    w.ports.Mut(pi).peer = to
    for k in 0..w.dgrams.Len() {
        d := w.dgrams.Mut(k)
        if d.used && d.to == pi && !DgramMatchesPeer(d.from, to) {
            d.data.Free()
            d.used = false
        }
    }
    return Ok(0)
}

PushDgram(w: mut World, dst: int, from_addr: core.IoAddr, data: []u8, at: i64, seq: u64): bool {
    var body := Vec[u8].Empty()
    if !AppendBytes(mut body, data) {
        return false
    }
    d := Dgram(used: true, to: dst, from: from_addr, data: body, at: at, seq: seq)
    for k in 0..w.dgrams.Len() {
        if !w.dgrams.Get(k).used {
            w.dgrams.Set(k, d)
            return true
        }
    }
    if !w.dgrams.Push(d) {
        body.Free()
        return false
    }
    return true
}

DgramArrival(w: mut World, dst: int, lat: i64, nbytes: int): i64 {
    var at := w.clock + lat
    rate := w.net_faults.rate
    if rate > 0 {
        tx := if i64(nbytes) > 0x7FFF_FFFF_FFFF_FFFF / 1_000_000_000 { 0x7FFF_FFFF_FFFF_FFFF / rate } else { i64(nbytes) * 1_000_000_000 / rate }
        busy := w.ports.Get(dst).busy_until
        start := if at > busy { at } else { busy }
        at = start + tx
        w.ports.Mut(dst).busy_until = at
    }
    return at
}

NetSendTo(w: mut World, h: uint, data: []u8, to: core.IoAddr): core.IoResult {
    hi := FindHand(&*w, h, HUdp)
    if hi < 0 {
        return Fail(core.SysErr.Closed)
    }
    from := w.ports.Get(w.handles.Get(hi).obj).num
    lat := NetLatency(w)
    seq := w.NextSeq()
    dst := FindPort(&*w, int(to.port), true)
    if dst < 0 || IsBlackholed(&*w, int(to.port)) {
        return Ok(i64(data.Len()))
    }
    if w.net_faults.drop > 0 && w.Below(1_000_000) < u64(w.net_faults.drop) {
        return Ok(i64(data.Len()))
    }
    var ip: [16]u8 = [0; 16]
    if to.v6 {
        ip[15] = 1
    } else {
        ip[0] = 127
        ip[3] = 1
    }
    from_addr := core.IoAddr(ip: ip, port: Trunc[u16](from), v6: to.v6, zone: 0)
    if w.ports.Get(dst).connected && !DgramMatchesPeer(from_addr, w.ports.Get(dst).peer) {
        return Ok(i64(data.Len()))
    }
    at := DgramArrival(w, dst, lat, data.Len())
    if !PushDgram(w, dst, from_addr, data, at, seq) {
        return Fail(core.SysErr.NoSpace)
    }
    if w.net_faults.duplicate > 0 && w.Below(1_000_000) < u64(w.net_faults.duplicate) {
        lat2 := NetLatency(w)
        seq2 := w.NextSeq()
        at2 := DgramArrival(w, dst, lat2, data.Len())
        if !PushDgram(w, dst, from_addr, data, at2, seq2) {
            return Fail(core.SysErr.NoSpace)
        }
    }
    return Ok(i64(data.Len()))
}

NetRecvFrom(w: mut World, h: uint, buf: mut []u8, from: mut core.IoAddr, hold: bool): core.IoResult {
    hi := FindHand(&*w, h, HUdp)
    if hi < 0 {
        return Fail(core.SysErr.Closed)
    }
    pi := w.handles.Get(hi).obj
    p := w.ports.Get(pi)
    var best := -1
    for k in 0..w.dgrams.Len() {
        d := w.dgrams.Get(k)
        if d.used && d.to == pi && (!p.connected || DgramMatchesPeer(d.from, p.peer)) {
            if best < 0 {
                best = k
            } else {
                b := w.dgrams.Get(best)
                if d.at < b.at || d.at == b.at && d.seq < b.seq {
                    best = k
                }
            }
        }
    }
    if best < 0 {
        return Fail(core.SysErr.Canceled)
    }
    if hold && w.dgrams.Get(best).at > w.clock {
        return core.IoResult(value: w.dgrams.Get(best).at, err: core.SysErr.Canceled, os: NotYet)
    }
    d := w.dgrams.Mut(best)
    if d.at > w.clock {
        w.clock = d.at
    }
    var n := d.data.Len()
    var cut: i64 = 0
    if n > buf.Len() {
        n = buf.Len()
        cut = 1
    }
    for k in 0..n {
        buf[k] = d.data.Get(k)
    }
    *from = d.from
    d.data.Free()
    d.used = false
    return core.IoResult(value: i64(n), err: core.SysErr.None, os: cut)
}

NetTryRecvFrom(w: mut World, h: uint, buf: mut []u8, from: mut core.IoAddr): core.IoResult {
    hi := FindHand(&*w, h, HUdp)
    if hi < 0 {
        return Fail(core.SysErr.Closed)
    }
    pi := w.handles.Get(hi).obj
    p := w.ports.Get(pi)
    var best := -1
    for k in 0..w.dgrams.Len() {
        d := w.dgrams.Get(k)
        if d.used && d.to == pi && d.at <= w.clock && (!p.connected || DgramMatchesPeer(d.from, p.peer)) {
            if best < 0 {
                best = k
            } else {
                b := w.dgrams.Get(best)
                if d.at < b.at || d.at == b.at && d.seq < b.seq {
                    best = k
                }
            }
        }
    }
    if best < 0 {
        return Ok(-1)
    }
    d := w.dgrams.Mut(best)
    var n := d.data.Len()
    var cut: i64 = 0
    if n > buf.Len() {
        n = buf.Len()
        cut = 1
    }
    for k in 0..n {
        buf[k] = d.data.Get(k)
    }
    *from = d.from
    d.data.Free()
    d.used = false
    return core.IoResult(value: i64(n), err: core.SysErr.None, os: cut)
}

NetResolve(w: mut World, host: str, out: mut []core.IoAddr): core.IoResult {
    var a := core.IoAddr(ip: [0; 16], port: 0, v6: false, zone: 0)
    if !ResolveHost(host.AsBytes(), mut a) {
        return Fail(core.SysErr.NotFound)
    }
    if out.Len() == 0 {
        return Ok(0)
    }
    out[0] = a
    return Ok(1)
}

// NetClose closes a listener, a connection end or a UDP socket; false when
// h is none of them. A listener's unaccepted server ends close with it.
NetClose(w: mut World, h: uint): bool {
    var i := FindHand(&*w, h, HConn)
    if i >= 0 {
        e := w.handles.Get(i).obj
        DropHand(w, i)
        CloseEnd(w, e)
        return true
    }
    i = FindHand(&*w, h, HListen)
    if i >= 0 {
        pi := w.handles.Get(i).obj
        DropHand(w, i)
        for w.ports.Get(pi).backlog.Len() > 0 {
            se := w.ports.Mut(pi).backlog.RemoveAt(0)
            ResetConn(w, se)
            CloseEnd(w, se)
        }
        FreePort(w, pi)
        return true
    }
    i = FindHand(&*w, h, HUdp)
    if i >= 0 {
        pi := w.handles.Get(i).obj
        DropHand(w, i)
        for k in 0..w.dgrams.Len() {
            d := w.dgrams.Mut(k)
            if d.used && d.to == pi {
                d.data.Free()
                d.used = false
            }
        }
        FreePort(w, pi)
        return true
    }
    return false
}

// DropRun is Restart's half for the network and the handle table: every
// connection, port, datagram and handle of the run is gone. The runtime
// records stay with whoever owns them; their epoch no longer matches.
DropRun(w: mut World) {
    FreeNet(w)
    w.handles.Truncate(0)
    w.kids.Truncate(0)
    w.ptys.Truncate(0)
    w.blackholes.Truncate(0)
    // The keys typed for the ended run are lost with it; the terminal's
    // mode is as opened again.
    FreeEnd(mut w.term)
    w.term = FreshEnd(0)
    w.tmode = 0
}

FreeNet(w: mut World) {
    for i in 0..w.ends.Len() {
        e := w.ends.Mut(i)
        if e.used {
            FreeEnd(e)
        }
    }
    w.ends.Truncate(0)
    for i in 0..w.ports.Len() {
        if w.ports.Get(i).used {
            FreePort(w, i)
        }
    }
    w.ports.Truncate(0)
    for i in 0..w.dgrams.Len() {
        d := w.dgrams.Mut(i)
        if d.used {
            d.data.Free()
            d.used = false
        }
    }
    w.dgrams.Truncate(0)
}

FreeWorld(w: mut World) {
    FreeNet(w)
    w.ends.Free()
    w.ports.Free()
    w.dgrams.Free()
    w.handles.Free()
    w.kids.Free()
    w.ptys.Free()
    FreeEnd(mut w.term)
    w.tasks.Free()
    w.pend.Free()
    w.mains.Free()
    w.joins.Free()
    w.dls.Free()
    w.blackholes.Free()
    FreeDisk(w)
}
