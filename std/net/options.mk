// Why: servers and clients tune their sockets (backlog, address reuse,
// Nagle, keep-alive, half-close), and each option should cost nothing to a
// program that never names it.
// What: ListenOptions, DefaultListenOptions, ListenWith, Stream.SetNoDelay,
// SetKeepAlive, Stream.Shutdown with the Shutdown enum, LocalAddr and PeerAddr.
// How: each option is one setsockopt through std.rt's call entry
// (rt.SockOption), so a program links it only when it names it. The calls
// that take no Io reach the OS socket through rt.NativeIo(), and only for a
// stream the native Io made (StreamTcp or StreamUnix); another Io's stream
// (std.sim, a Recorder) answers Unsupported.

import std.core
import std.rt
import std.time

/// How ListenWith binds, and what the streams its Accept makes carry.
/// Plain. Start from DefaultListenOptions and assign the fields you
/// change: a literal of it does not compile outside std, so a field added
/// later breaks no program.
export struct ListenOptions {
    /// The queue of connections not yet accepted. A positive value is the
    /// queue asked for, which the OS may round up; 0 or less is the OS's
    /// largest, which Listen asks for.
    export backlog: int
    /// Rebind a port whose connections are still in TIME_WAIT
    /// (SO_REUSEADDR on Linux). Windows rebinds without it, and false there
    /// keeps every other socket off the port while the listener holds it.
    export reuse_addr: bool
    /// Every accepted stream sends small writes at once (TCP_NODELAY).
    export no_delay: bool
    sealed: u8
}

/// What Listen does: the OS's largest backlog, rebinding over TIME_WAIT
/// allowed, Nagle's algorithm on.
export const DefaultListenOptions: ListenOptions = ListenOptions(backlog: 0, reuse_addr: true, no_delay: false, sealed: 0)

/// Binds addr and listens as Listen does, with o's choices. A positive
/// backlog is the queue the OS is asked for (listen(2) may round it up).
///
/// With reuse_addr false, Linux refuses a port whose connections are
/// still in TIME_WAIT (AddrInUse). Linux rebinds over TIME_WAIT only when
/// the socket that left it also set SO_REUSEADDR, so the TIME_WAIT
/// entries of a listener made with reuse_addr false hold its port against
/// both choices until they expire. Windows binds over TIME_WAIT either
/// way, and with reuse_addr false keeps any other socket off the port
/// while the listener holds it (SO_EXCLUSIVEADDRUSE).
///
/// Port 0 and the address forms are Listen's, and so is a worker's
/// inherited listener, which keeps the options the parent listened with,
/// except no_delay, which applies at each Accept. Errors: Listen's
/// (Canceled is checked first, before the inherited listener is looked
/// for); Unsupported for a positive backlog or reuse_addr false under an
/// Io that cannot honour them (a Recorder's Tape). O(1) runtime calls;
/// allocates nothing.
export ListenWith(io: Io, addr: str, o: ListenOptions): Error!Listener {
    try CancelPoint(io)
    if l := Inherit(io, PortOf(addr)) {
        Listener(h, bound, local, kind, no_delay: _) := l
        return Listener(h: h, bound: bound, local: local, kind: kind, no_delay: o.no_delay)
    }
    // DefaultListenOptions stays on Listen's op, which every Io answers;
    // only a choice Listen does not make takes the call op.
    r := if o.backlog <= 0 && o.reuse_addr { core.IoListen(io, addr) } else { rt.ListenWith(io, addr, o.backlog, o.reuse_addr) }
    h := try Check(r)
    return NewListener(Trunc[uint](h), Trunc[int](r.os), addr, o.no_delay)
}

/// Sends small writes at once (on true: TCP_NODELAY, Nagle's algorithm
/// off) or lets the OS gather them (false). Errors: Unsupported on a Unix
/// stream, or on a stream an Io other than the native one made (std.sim's,
/// a Recorder's); Os for the OS's refusal. O(1): one system call;
/// allocates nothing.
export Stream.SetNoDelay(&self, on: bool): Error!void {
    if self.kind != StreamTcp {
        fail Unsupported
    }
    _ = try Check(rt.SockOption(rt.NativeIo(), self.h, core.IoSockNoDelay, if on { 1 } else { 0 }, 0))
}

/// Turns TCP keep-alive probes on, the first after idle without traffic
/// (rounded up to whole seconds), or off with none. An idle of zero or
/// less turns probes on and keeps the socket's idle time: the OS's
/// default (Linux tcp_keepalive_time, two hours on Windows), or the one
/// an earlier SetKeepAlive on this stream set.
/// Errors:
/// Unsupported on a Unix stream or another Io's stream, as SetNoDelay; Os
/// for the OS's refusal, an idle time past its range included (Linux takes
/// at most 32,767 seconds). O(1): one or two system calls; allocates
/// nothing.
export Stream.SetKeepAlive(&self, idle: ?time.Duration): Error!void {
    if self.kind != StreamTcp {
        fail Unsupported
    }
    var on: i64 = 0
    // 0 sets no TCP_KEEPIDLE, and the OS keeps the last one set.
    var secs: i64 = 0
    if d := idle {
        on = 1
        secs = KeepAliveSeconds(d.Nanos())
    }
    _ = try Check(rt.SockOption(rt.NativeIo(), self.h, core.IoSockKeepAlive, on, secs))
}

// KeepAliveSeconds is nanos rounded up to whole seconds, 0 (set no idle
// time) for zero or less; no overflow, since nanos / 1e9 + 1 is far below i64's max.
KeepAliveSeconds(nanos: i64): i64 {
    if nanos <= 0 {
        return 0
    }
    s := nanos / 1_000_000_000
    return if nanos % 1_000_000_000 == 0 { s } else { s + 1 }
}

/// Asks the OS for receive and send buffer sizes of `recv` and `send`
/// bytes (0 keeps the current size). Errors: Unsupported for another Io's
/// stream; Os for the OS's refusal. O(1): at most two system calls;
/// allocates nothing.
export Stream.SetBuffers(&self, recv: int, send: int): Error!void {
    if self.kind == StreamForeign {
        fail Unsupported
    }
    if recv != 0 {
        _ = try Check(rt.SockOption(rt.NativeIo(), self.h, core.IoSockRecvBuffer, i64(recv), 0))
    }
    if send != 0 {
        _ = try Check(rt.SockOption(rt.NativeIo(), self.h, core.IoSockSendBuffer, i64(send), 0))
    }
}

/// Which directions Stream.Shutdown and Conn.Shutdown end.
export open enum Shutdown {
    /// No more reads: later reads return 0, and a read waiting in another
    /// task returns 0.
    Read
    /// No more writes: the peer reads the end of the stream after what was
    /// sent.
    Write
    /// No more reads and no more writes.
    Both
}

// ShutdownCode is rt.SockShutdown's how for s.
ShutdownCode(s: Shutdown): u32 {
    return match s {
        .Read { 0 }
        .Write { 1 }
        .Both { 2 }
    }
}

/// Ends one or both directions of the stream (shutdown(2)); the handle
/// stays open until Close. Shutdown(Write) is the half-close a
/// request-response protocol sends after its request. Errors: Canceled in
/// a cancelled scope, checked first, and not cancellable after that;
/// Unsupported for another Io's stream;
/// Os for the OS's refusal (ENOTCONN on Linux, for a stream the peer
/// reset). O(1): one system call, which does not wait; allocates nothing.
export Stream.Shutdown(&self, io: Io, how: Shutdown): Error!void {
    try CancelPoint(io)
    _ = try Check(rt.SockShutdown(io, self.h, ShutdownCode(how)))
}

/// This end's address: the IP and port the stream is bound to. The zero
/// Addr for a Unix stream, for another Io's stream, and when the OS cannot
/// tell (the socket was reset). O(1): one system call; allocates nothing.
export Stream.LocalAddr(&self): Addr {
    if self.kind != StreamTcp {
        return ZeroAddr
    }
    var ip: [16]u8 = [0; 16]
    var w := core.IoWords(a: 0, b: 0, c: 0, d: 0)
    r := rt.SockAddress(rt.NativeIo(), self.h, false, mut ip[..], mut w)
    if r.err != core.SysErr.None || w.a < 0 || w.a > 65535 {
        return ZeroAddr
    }
    return Unmapped(Addr(ip: ip, port: Trunc[u16](w.a), v6: w.b == 1))
}

/// The peer's address, read when the stream was made (by Dial or Accept),
/// so a peer that has since gone still has one; an accepted stream's comes
/// from the accept itself. The zero Addr for a Unix stream, for another
/// Io's stream, and for a dialled peer already gone when the stream was
/// made. An IPv4 peer of an IPv6 listener is its IPv4 address. O(1);
/// allocates nothing.
export Stream.PeerAddr(&self): Addr {
    if p := self.peer {
        return p
    }
    return ZeroAddr
}
