// Why: TCP is the network most programs need; std.net gives it as linear
// handles over the runtime's sockets, with no allocation and no hidden
// buffers (layer 2).
// What: Listener (Listen, Accept, Port, Close), Stream (Dial,
// Read, Write, WriteAll, Reader, Writer, Close), the error mapping Check and
// CheckCount, CancelPoint, and the inherited-listener hand-off a
// proc.Supervise worker uses (Inherit, Stash, TakeStash).
// How: every operation is one IoTable call through std.core, except WriteAll
// (a loop of send) and the Reader and Writer, which buffer in the caller's
// memory. Addresses are host:port, [v6]:port or :port; name resolution is
// the runtime's.

import std.core
import std.raw
import std.rt

/// A bound, listening socket. Linear: Close releases it. Send and Share:
/// several tasks may wait in Accept on one listener at once.
export linear struct Listener {
    h: uint
    // the bound port, which listen reports in IoResult.os on success;
    // 0 for a Unix-domain listener
    bound: int
    // Cached bind address for a simulated or replayed listener.
    local: Addr
    // StreamTcp or StreamUnix: the kind of the streams Accept makes
    kind: u8
    // ListenOptions.no_delay: Accept sets TCP_NODELAY on each stream
    no_delay: bool
}

/// A connected TCP or Unix-domain stream. Linear: Close releases it. Send
/// but not Share, so one task reads and writes it at a time; to read in
/// one task while another writes, make it a Conn (Stream.Conn).
export linear struct Stream {
    h: uint
    // the peer's address, read once when the stream was made, since a
    // reset peer has none to read later (as Go's netFD keeps raddr); none
    // for a Unix stream, another Io's stream, or a peer the OS could not
    // name (so Conn.PeerAddr is none, never the zero Addr)
    peer: ?Addr
    // StreamTcp, StreamUnix or StreamForeign
    kind: u8
    nosh: [0]Cell[u8]
}

// The kinds of stream. A StreamTcp or StreamUnix handle is an OS socket
// the native Io reaches, so the calls that take no Io (the options,
// LocalAddr) go through rt.NativeIo(); a StreamForeign handle is another
// Io's (std.sim, a Recorder's Tape: no OS socket, or a call entry that
// answered Unsupported when the stream was made), and those calls are
// Unsupported.
const StreamTcp: u8 = 0
const StreamUnix: u8 = 1
const StreamForeign: u8 = 2

// ZeroAddr is the address a stream reports when it has none to report.
const ZeroAddr = Addr(ip: [0; 16], port: 0, v6: false)

// NewStream wraps the TCP handle h, which io made, reading its peer's
// address through io's call entry. Natively that is inline: for a socket
// the native accept made, the address the accept op returned, kept in its
// record (no system call); for any other socket (a dialled one, a
// passed one) one getpeername. The stream is StreamForeign when h is no
// OS socket (rt.IsNativeSocket: a Sim's or a Replayer's record) or when
// io's call entry answers Unsupported (a Recorder over the native Io,
// whose handles are OS sockets: so a recording and its replay agree); any
// other failure (getpeername's ENOTCONN: a dialled peer already gone, or
// an accepted socket whose address the accept could not decode) leaves
// the peer none.
NewStream(io: Io, h: uint): Stream {
    if !rt.IsNativeSocket(h) {
        return Stream(h: h, peer: none, kind: StreamForeign, nosh: [])
    }
    var ip: [16]u8 = [0; 16]
    var w := core.IoWords(a: 0, b: 0, c: 0, d: 0)
    r := rt.SockAddress(io, h, true, mut ip[..], mut w)
    if r.err == core.SysErr.Unsupported {
        return Stream(h: h, peer: none, kind: StreamForeign, nosh: [])
    }
    if r.err != core.SysErr.None || w.a < 0 || w.a > 65535 {
        return Stream(h: h, peer: none, kind: StreamTcp, nosh: [])
    }
    peer := Unmapped(Addr(ip: ip, port: Trunc[u16](w.a), v6: w.b == 1))
    return Stream(h: h, peer: some(peer), kind: StreamTcp, nosh: [])
}

// Check is r's value, or the net.Error its SysErr maps to:
// Refused Reset AddrInUse Unreachable InvalidAddress Canceled Closed and
// Unsupported to the member of the same name, anything else to Os with
// the OS's own code. So a Bind, Datagram.Send or Recv, Listen, Dial,
// Accept or Resolve that meets SysErr.Unsupported fails with Unsupported,
// not Os.
Check(r: core.IoResult): Error!i64 {
    match r.err {
        .None { return r.value }
        .Refused { fail Refused }
        .Reset { fail Reset }
        .AddrInUse { fail AddrInUse }
        .Unreachable { fail Unreachable }
        .InvalidAddress { fail InvalidAddress }
        .Canceled { fail Canceled }
        .Closed { fail Closed }
        .Unsupported { fail Unsupported }
        _ { fail Os(code: r.os) }
    }
}

// CheckCount is Check for a recv or a send of at most max bytes: a count
// below 0 or above max breaks the runtime's contract and is
// Os(code: 0), as Go's io.Copy reports errInvalidWrite.
CheckCount(r: core.IoResult, max: int): Error!int {
    n := try Check(r)
    if n < 0 || n > i64(max) {
        fail Os(code: 0)
    }
    return int(n)
}

/// Binds addr and listens, with the OS's largest backlog. addr is
/// "a.b.c.d:port", "[v6]:port" (a numeric "%zone" allowed),
/// "localhost:port" (127.0.0.1) or ":port" (every IPv4 interface;
/// "[::]:port" is every interface of both families). Port 0 lets the OS
/// choose; Port tells which. Errors: AddrInUse when the port is taken,
/// InvalidAddress for any other form, Canceled in a cancelled scope,
/// checked first. O(1) runtime calls; allocates nothing.
///
/// In a worker that proc.Supervise started, a Listen of the parent's
/// address returns the listener the parent passed down, with its bound
/// port. A Listen whose port is that listener's port, or 0 (as the
/// parent's own address may be), takes it, once. A Listen of any other
/// port binds as above, so a worker may open an admin or metrics port
/// before or after it, from another task too. Two Listens of port 0
/// cannot be told apart: the first takes the inherited listener. A
/// cancelled Listen leaves the inherited listener for the next.
export Listen(io: Io, addr: str): Error!Listener {
    try CancelPoint(io)
    if l := Inherit(io, PortOf(addr)) {
        return l
    }
    r := core.IoListen(io, addr)
    h := try Check(r)
    return NewListener(Trunc[uint](h), Trunc[int](r.os), addr, false)
}

// NewListener records the bind IP and the port reported by IoListen, including
// the allocated port when the caller requested 0.
NewListener(h: uint, port: int, addr: str, no_delay: bool): Listener {
    var a := core.IoAddr(ip: [0; 16], port: 0, v6: false, zone: 0)
    var local := ZeroAddr
    if port >= 0 && port <= 65535 && ParseBindAddr(addr.AsBytes(), mut a) {
        local = Unmapped(Addr(ip: a.ip, port: Trunc[u16](port), v6: a.v6))
    }
    return Listener(h: h, bound: port, local: local, kind: StreamTcp, no_delay: no_delay)
}

// Inherit is Listen's and ListenWith's step after their cancellation
// check: the listener the parent passed this worker when want is its port
// or 0, taken once, or one stashed by an earlier call for want, or
// none. One IoInherited per call, whatever came before in the process, so
// a Recorder's log of a Listen replays the same calls; the runtime answers
// once per process, so a listener for another port is stashed for the
// Listen of its port.
Inherit(io: Io, want: int): ?Listener {
    InheritBegin()
    return InheritSettle(io, want, core.IoInherited(io))
}

// InheritBegin counts this call among those that may hold the inherited
// listener and not yet have stashed it, before it asks
// the runtime: the runtime's Cas that hands the listener to one caller
// comes after this Add.
InheritBegin() {
    _ = Inherited.asking.Add(1)
}

// InheritSettle is Inherit after the runtime answered i. The winner claims
// the listener or stashes it, then leaves the count. A loser leaves the
// count first, then takes the stash; while another call is still counted
// (it may hold the listener unstashed) it yields and looks
// again, so a Listen of the parent's port never binds anew while a
// concurrent Listen of an admin port is between its IoInherited and its
// Stash. That window holds no await natively (NativeInherited and Stash
// are system calls and stores), so the loop yields only when two carriers
// race; under std.sim the entry answers NotFound inline and nothing waits.
// A cancel ends the wait with none, and the Listen's own call then fails
// with Canceled. Limit: io.Yield is a recorded call, so a Recorder over
// the native Io that saw two Listens race here logs Yields its replay need
// not repeat, and that replay diverges (Canceled), as a replay of any
// racing native recording may.
InheritSettle(io: Io, want: int, i: core.IoResult): ?Listener {
    if i.err == core.SysErr.None {
        if want == 0 || Trunc[int](i.os) == want {
            InheritDone()
            return some(Listener(h: Trunc[uint](i.value), bound: Trunc[int](i.os), local: ZeroAddr, kind: StreamTcp, no_delay: false))
        }
        Stash(Cast[u64](i.value), Cast[u64](i.os))
        InheritDone()
        return none
    }
    InheritDone()
    var h: uint = 0
    var bound := 0
    for {
        // Edge: a count of 0 read here means every winner counted before
        // it has run its Stash (Store StashHeld before its Sub; raw.Atomic
        // is seq_cst), so the TakeStash below sees what it stashed. A
        // winner whose runtime Cas made this call's IoInherited NotFound
        // did its Add before that Cas, so it is counted until it stashed.
        busy := Inherited.asking.Load() != 0
        if TakeStash(want, mut h, mut bound) {
            return some(Listener(h: h, bound: bound, local: ZeroAddr, kind: StreamTcp, no_delay: false))
        }
        if !busy {
            return none
        }
        io.Yield() else {
            return none
        }
    }
}

// InheritDone leaves the count InheritBegin joined (an atomic add wraps,
// so adding u32's max subtracts 1).
InheritDone() {
    _ = Inherited.asking.Add(0xFFFF_FFFF)
}

// PortOf is addr's port, the decimal digits after its last colon; -1 when
// there are none, so no inherited listener matches (IoListen then refuses
// the address).
PortOf(addr: str): int {
    b := addr.AsBytes()
    var i := b.Len() - 1
    for i >= 0 && b[i] != 58 {
        i -= 1
    }
    var p: u16 = 0
    if i < 0 || !ParsePortDigits(b[i + 1..], mut p) {
        return -1
    }
    return int(p)
}

// The inherited listener a Listen of another port was handed, kept for the
// Listen of its port: state StashEmpty, StashFilling (one Stash writes h
// and port), StashHeld, then StashTaken once a Listen claimed it.
const StashEmpty: u32 = 0
const StashFilling: u32 = 1
const StashHeld: u32 = 2
const StashTaken: u32 = 3

struct InheritedStash {
    state: raw.Atomic[u32]
    h: raw.Atomic[u64]
    port: raw.Atomic[u64]
    // the Inherit calls between InheritBegin and InheritDone
    asking: raw.Atomic[u32]
}

var Inherited: InheritedStash = InheritedStash(state: raw.Atomic[u32].New(0), h: raw.Atomic[u64].New(0), port: raw.Atomic[u64].New(0),
    asking: raw.Atomic[u32].New(0))

// Stash keeps an inherited listener for a later Listen of its port. The
// runtime hands it out once, so at most one Stash fills the word; a second
// (a replayed log's) finds it in use and is dropped, as an Io's handle may
// be.
Stash(h: u64, port: u64) {
    // The Cas makes this the only writer of h and port.
    if !Inherited.state.Cas(StashEmpty, StashFilling) {
        return
    }
    Inherited.h.Store(h)
    Inherited.port.Store(port)
    // Edge: publishes h and port to the Load of StashHeld in TakeStash
    // (raw.Atomic is seq_cst).
    Inherited.state.Store(StashHeld)
}

// TakeStash gives the kept listener's handle and port to a Listen of port
// want (0 matches any port), once; false when none is kept, it was taken,
// or its port is another.
TakeStash(want: int, h: mut uint, port: mut int): bool {
    if want < 0 || Inherited.state.Load() != StashHeld {
        return false
    }
    // Edge: the Load above read StashHeld, so h and port are Stash's.
    p := Trunc[int](Inherited.port.Load())
    if want != 0 && p != want {
        return false
    }
    // The Cas gives the handle to one Listen; a loser sees StashTaken.
    if !Inherited.state.Cas(StashHeld, StashTaken) {
        return false
    }
    *h = Trunc[uint](Inherited.h.Load())
    *port = p
    return true
}

/// Connects to addr: "a.b.c.d:port", "[v6]:port" (a numeric %zone
/// allowed), "localhost:port" or ":port" (both 127.0.0.1), or
/// "name:port", resolved first: on Windows the system's first IPv4
/// answer is dialled, else its first IPv6 one; on Linux each answer of the
/// lookup Resolve makes (/etc/hosts, then the nameservers), in order and as
/// the lookup gives it (a zoned or IPv4-mapped one too), until one connects.
/// Errors: Refused when nothing listens there, Unreachable when no route
/// leads there, InvalidAddress for an addr that does not parse or a name
/// that does not resolve (as Resolve reports it), Canceled; for a name
/// dialled at several addresses, the first one's error. One runtime call
/// (with a name, a lookup and a connect per address tried); allocates
/// nothing.
///
/// The peer's address is read once here and kept for Stream.PeerAddr:
/// one system call more, which does not wait.
export Dial(io: Io, addr: str): Error!Stream {
    var r := core.IoDial(io, addr)
    if r.err == core.SysErr.NotFound {
        // A name with no answer: InvalidAddress, as Resolve reports it.
        r.err = core.SysErr.InvalidAddress
    }
    h := try Check(r)
    return NewStream(io, Trunc[uint](h))
}

// std's handle seam (core.IoHandle): std.proc's Supervise passes the
// listener's runtime handle to spawn's inherit. Not an export.
impl core.IoHandle for Listener {
    IoHandle(&self): uint {
        return self.h
    }
}

/// Waits for the next connection and returns its stream. Errors: Canceled
/// when the scope is cancelled, at entry or while it waits (nothing is
/// lost: call again); Closed when the listener's handle is gone; any other
/// failure is the OS's, as Reset, Os and the other members. The peer's
/// address comes from the accept itself, so a client that reset before
/// Accept returned is still named. One runtime call, plus reading the
/// peer's address from the accept with no system call (native Io), plus
/// one system call that sets TCP_NODELAY when ListenWith asked for
/// no_delay (a failure of that one is ignored: the stream works without
/// it); allocates nothing.
export Listener.Accept(&self, io: Io): Error!Stream {
    h := Trunc[uint](try Check(core.IoAccept(io, self.h)))
    if self.kind == StreamUnix {
        return Stream(h: h, peer: none, kind: StreamUnix, nosh: [])
    }
    if self.no_delay {
        _ = rt.SockOption(io, h, core.IoSockNoDelay, 1, 0)
    }
    return NewStream(io, h)
}

/// The port the listener is bound to; 0 for a Unix-domain
/// listener. O(1); allocates nothing.
export Listener.Port(&self): int {
    return self.bound
}

/// The TCP listener's bound IP and port. Unix-domain listeners report the
/// zero Addr. O(1); at most one runtime call; allocates nothing.
export Listener.Addr(&self): Addr {
    if self.kind != StreamTcp {
        return ZeroAddr
    }
    if !rt.IsNativeSocket(self.h) {
        return self.local
    }
    var ip: [16]u8 = [0; 16]
    var w := core.IoWords(a: 0, b: 0, c: 0, d: 0)
    r := rt.SockAddress(rt.NativeIo(), self.h, false, mut ip[..], mut w)
    if r.err != core.SysErr.None || w.a < 0 || w.a > 65535 {
        return self.local
    }
    return Unmapped(Addr(ip: ip, port: Trunc[u16](w.a), v6: w.b == 1))
}

/// Closes the listener. Infallible and not cancellable: the OS's answer
/// to the close is ignored. O(1); allocates nothing.
///
/// A Unix-domain listener leaves its socket file behind: remove it with
/// fs.Remove before listening at that path again.
export Listener.Close(self) {
    rt.Abandon(self.h)
    Listener(h: _, bound: _, local: _, kind: _, no_delay: _) := self
}

/// Reads once: at most buf.Len() bytes, the count read, 0 at the end of
/// the stream (the peer shut its side down). Errors: Reset when the peer
/// dropped the connection; Canceled when the scope is cancelled, before
/// or while it waits, with nothing consumed (call again). One runtime
/// call; allocates nothing.
///
/// An empty buf returns 0 without reading, after the cancellation check.
/// A count outside 0..buf.Len() from the OS layer is Os(code: 0).
export Stream.Read(&self, io: Io, buf: mut []u8): Error!int {
    if buf.Len() == 0 {
        try CancelPoint(io)
        return 0
    }
    return try CheckCount(core.IoRecv(io, self.h, buf), buf.Len())
}

/// Sends once and returns the count sent, which may be less than
/// data.Len(). Errors: Reset when the peer dropped the connection;
/// Canceled. Empty data sends nothing, after the cancellation check. A
/// Write that returns Canceled may have sent some or all of data, so
/// calling it again may send those bytes twice: close the stream. One
/// runtime call; allocates nothing.
export Stream.Write(&self, io: Io, data: []u8): Error!int {
    if data.Len() == 0 {
        try CancelPoint(io)
        return 0
    }
    return try CheckCount(core.IoSend(io, self.h, data), data.Len())
}

/// Sends every byte of data, looping over partial sends, and fails with
/// the first failed send's error. On Canceled an unknown prefix of data
/// was sent: close the stream. O(n); allocates nothing.
///
/// A send that makes no progress on non-empty data, which the OS layer
/// never answers, is reported as Os(code: 0) instead of looping forever.
export Stream.WriteAll(&self, io: Io, data: []u8): Error!void {
    try SendAll(io, self.h, data)
}

// SendAll is WriteAll on the socket h, shared by Stream and a Conn over a
// stream's handle.
SendAll(io: Io, h: uint, data: []u8): Error!void {
    if data.Len() == 0 {
        try CancelPoint(io)
        return
    }
    var off := 0
    for off < data.Len() {
        n := try CheckCount(core.IoSend(io, h, data[off..]), data.Len() - off)
        if n == 0 {
            fail Os(code: 0)
        }
        off += n
    }
}

// CancelPoint is the cancellation check for a call that makes no runtime
// call, and the one std.core and std.sync make (core.SyncCancel): Canceled
// in a cancelled scope that io's mask does not cover (a scope opened from a
// NoCancel Io is cancelled by its own Cancel), with the native fast
// path. A check that fails with anything other than Canceled (an Io with no
// OS behind it) is not a cancellation: the call goes on and reports what
// its own runtime call answers, or its result when it makes none. Not
// Io.Sleep(0), which reads every failure as Canceled.
CancelPoint(io: Io): Error!void {
    core.SyncCancel(io) else {
        fail Canceled
    }
}

/// A buffered Reader over this stream, using buf as its buffer. Each fill
/// is one Read; any net.Error becomes the Reader's ReadError. Bytes it
/// buffered and the caller did not read are lost when it is dropped.
/// O(1); allocates nothing.
export Stream.Reader(&self, io: Io, buf: mut []u8): Reader from self, buf {
    return core.NewReader(io, raw.Of(&StreamReadTable), self.h, buf)
}

/// A buffered Writer over this stream, using buf as its buffer. Each
/// flush sends with WriteAll; any failure becomes the Writer's WriteError.
/// Dropping it with buffered bytes loses them: Flush first. O(1);
/// allocates nothing.
export Stream.Writer(&self, io: Io, buf: mut []u8): Writer from self, buf {
    return core.NewWriter(io, raw.Of(&StreamWriteTable), self.h, buf)
}

/// Closes the stream. Infallible and not cancellable. O(1); allocates
/// nothing.
export Stream.Close(self) {
    rt.Abandon(self.h)
    Stream(h: _, peer: _, kind: _, nosh: _) := self
}

const StreamReadTable = core.ReaderTable(read: SockRead, line: core.ReadLineBytewise)
const StreamWriteTable = core.WriterTable(write: SockWrite, flush: SockFlush)

// A stream reader's read entry: one recv into buf; any net.Error is the
// Reader's ReadError.
SockRead(r: mut Reader, buf: mut []u8): ReadError!int {
    if buf.Len() == 0 {
        return 0
    }
    res := core.IoRecv(core.ReaderIo(&*r), core.ReaderCtx(&*r), buf)
    if res.err != core.SysErr.None || res.value < 0 || res.value > i64(buf.Len()) {
        fail ReadError
    }
    return int(res.value)
}

// A stream writer's write entry: every byte of b, looping over partial
// sends; any failure or a send with no progress is WriteError.
SockWrite(w: mut Writer, b: []u8): WriteError!void {
    io := core.WriterIo(&*w)
    h := core.WriterCtx(&*w)
    var off := 0
    for off < b.Len() {
        r := core.IoSend(io, h, b[off..])
        if r.err != core.SysErr.None || r.value <= 0 || r.value > i64(b.Len() - off) {
            fail WriteError
        }
        off += int(r.value)
    }
}

// Sockets have no flush: every byte the writer handed over is sent.
SockFlush(w: mut Writer): WriteError!void {
}
