// Why: UDP gives datagrams with no connection; programs need it for DNS,
// discovery and games, with link-local addressing that cannot go wrong.
// What: Datagram with Bind, Send, Recv, Port and Close, and the helpers
// ParseBindAddr, SendDst and PeerZoneOk.
// How: every operation is one IoTable call (udp, sendto, recvfrom); nothing
// allocates. A socket keeps its family and the numeric zone Bind was given,
// so a link-local peer is always addressed on the right link.

import std.core
import std.rt
import std.task

/// A bound UDP socket. Linear: Close releases it. Send but not Share, as
/// Stream: one task uses it at a time.
export linear struct Datagram {
    h: uint
    bound: int
    // the socket's family, which Send needs to address an IPv4 peer from a
    // dual-stack socket
    v6: bool
    // the numeric zone Bind was given (0 for none): the one link-local zone
    // this socket can name
    zone: u32
    // the connected peer when Datagram.Connect succeeded, so SharedDatagram
    // can send with to none; Cell keeps Datagram !Share.
    peer: Cell[?Addr]
}

/// Binds a UDP socket to addr: "a.b.c.d:port", "[v6]:port" (a numeric
/// "%zone" allowed, which the socket keeps), "localhost:port" (127.0.0.1)
/// or ":port" (every IPv4 interface; "[::]:port" is every interface of
/// both families). These are Listen's forms, the port one to five decimal
/// digits. Port 0 lets the OS choose; Port tells which. Errors:
/// InvalidAddress for any other form, AddrInUse when the port is taken,
/// Canceled. One runtime call; allocates nothing.
export Bind(io: Io, addr: str): Error!Datagram {
    var a := core.IoAddr(ip: [0; 16], port: 0, v6: false, zone: 0)
    if !ParseBindAddr(addr.AsBytes(), mut a) {
        try CancelPoint(io)
        fail InvalidAddress
    }
    r := core.IoUdp(io, a)
    h := try Check(r)
    return Datagram(h: Trunc[uint](h), bound: Trunc[int](r.os), v6: a.v6, zone: a.zone, peer: Cell[?Addr].New(none))
}

// ParseBindAddr reads Bind's forms into a, exactly Listen's (std.task's
// ParseAddr with listen true): a dotted IPv4 or a bracketed IPv6 host as
// ParseAddr's, an empty host (the IPv4 wildcard), "localhost" in any ASCII
// case, and a numeric zone on a bracketed IPv6 host (0 to 2^32 - 1); the
// port one to five decimal digits, at most 65535, where ParseAddr takes
// any count of leading zeros (netip's rule). A test block checks that Bind
// and Listen agree on every form.
ParseBindAddr(b: []u8, a: mut core.IoAddr): bool {
    var i := b.Len() - 1
    for i >= 0 && b[i] != 58 {
        i -= 1
    }
    if i < 0 || b.Len() - i - 1 > 5 || !ParsePortDigits(b[i + 1..], mut a.port) {
        return false
    }
    host := b[0..i]
    if host.Len() == 0 {
        return true
    }
    if IsLocalhost(host) {
        a.ip[0] = 127
        a.ip[3] = 1
        return true
    }
    if host[0] == 91 {
        if host.Len() < 2 || host[host.Len() - 1] != 93 {
            return false
        }
        var inner := host[1..host.Len() - 1]
        var pct := -1
        for k in 0..inner.Len() {
            if inner[k] == 37 {
                pct = k
                break
            }
        }
        if pct >= 0 {
            z := inner[pct + 1..]
            if z.Len() == 0 || z.Len() > 10 {
                return false
            }
            var zv := 0
            for c in z {
                d := int(c) - 48
                if d < 0 || d > 9 {
                    return false
                }
                zv = zv * 10 + d
            }
            if zv > 0xFFFF_FFFF {
                return false
            }
            a.zone = Trunc[u32](zv)
            inner = inner[0..pct]
        }
        a.v6 = true
        return ParseIp6(inner, mut a.ip)
    }
    return ParseIp4(host, mut a.ip, 0)
}

// IsLocalhost: b is "localhost", ASCII letters in any case.
IsLocalhost(b: []u8): bool {
    w := "localhost".AsBytes()
    if b.Len() != w.Len() {
        return false
    }
    for k in 0..w.Len() {
        if b[k] | 0x20 != w[k] {
            return false
        }
    }
    return true
}

/// Sends data as one datagram to the address to: all of it, or an error.
/// A datagram is never sent in part; one longer than the OS takes is Os
/// with the OS's code (10040 on Windows, 90 on Linux). Errors: Canceled;
/// InvalidAddress as below; the OS's. One runtime call; allocates nothing.
///
/// From an IPv6 socket an IPv4 to goes as ::ffff:a.b.c.d; from an IPv4
/// socket an IPv4-mapped to goes as IPv4, and any other IPv6 to is
/// InvalidAddress. A link-local to (fe80::/10) goes in the socket's zone,
/// the one Bind was given (0 when none, which the OS may refuse). Empty
/// data sends an empty datagram.
export Datagram.Send(&self, io: Io, data: []u8, to: Addr): Error!void {
    try UdpSend(self.h, self.v6, self.zone, io, data, to)
}

// UdpSend sends data as one datagram on socket h to to.
UdpSend(h: uint, v6: bool, zone: u32, io: Io, data: []u8, to: Addr): Error!void {
    var dst := core.IoAddr(ip: to.ip, port: to.port, v6: to.v6, zone: 0)
    if !SendDst(v6, zone, &to, mut dst) {
        try CancelPoint(io)
        fail InvalidAddress
    }
    n := try Check(core.IoSendTo(io, h, data, dst))
    if n != i64(data.Len()) {
        fail Os(code: 0)
    }
}

// UdpSendConnected sends data on a connected UDP socket h (no destination address).
UdpSendConnected(h: uint, io: Io, data: []u8): Error!void {
    n := try CheckCount(core.IoSend(io, h, data), data.Len())
    if n != data.Len() {
        fail Os(code: 0)
    }
}

// SendDst writes to dst the address Send hands the runtime for to, from a
// socket of family v6 bound in zone: an IPv4 to as ::ffff:a.b.c.d from an
// IPv6 socket, an IPv4-mapped to as IPv4 from an IPv4 socket, a link-local
// to in the socket's zone, anything else as it is. false: an IPv4 socket
// cannot reach to (an IPv6 address that is not mapped).
SendDst(v6: bool, zone: u32, to: &Addr, dst: mut core.IoAddr): bool {
    if v6 && !to.v6 {
        var ip: [16]u8 = [0; 16]
        ip[10] = 0xFF
        ip[11] = 0xFF
        ip[12] = to.ip[0]
        ip[13] = to.ip[1]
        ip[14] = to.ip[2]
        ip[15] = to.ip[3]
        *dst = core.IoAddr(ip: ip, port: to.port, v6: true, zone: 0)
        return true
    }
    if !v6 && to.v6 {
        if !IsMapped(&to.ip) {
            return false
        }
        u := Unmapped(*to)
        *dst = core.IoAddr(ip: u.ip, port: u.port, v6: false, zone: 0)
        return true
    }
    z: u32 = if v6 && IsLinkLocal(&to.ip) { zone } else { 0 }
    *dst = core.IoAddr(ip: to.ip, port: to.port, v6: to.v6, zone: z)
    return true
}

/// Waits for one datagram: its bytes in buf[0..n], and the peer that sent
/// it. Errors: Canceled when the scope is cancelled, at entry or while it
/// waits, with nothing consumed; InvalidAddress for a peer an Addr cannot
/// name (below); any other failure is the OS's. One runtime call;
/// allocates nothing.
///
/// A datagram longer than buf is cut to buf.Len() bytes and the rest is
/// lost (POSIX recvfrom), so size buf to the largest datagram expected
/// (65,507 bytes holds any IPv4 datagram). An empty buf discards one
/// datagram. An IPv4 peer is IPv4 (v6 false) whatever the socket's family.
/// A link-local peer in a zone other than the socket's (a sender on another
/// link than the zone Bind was given, or any link-local sender seen by a
/// socket bound without a zone) cannot be named by an Addr, so its
/// datagram is consumed and the call is InvalidAddress: a reply in zone 0
/// would go to the wrong link.
export Datagram.Recv(&self, io: Io, buf: mut []u8): Error!(n: int, peer: Addr) {
    return try UdpRecv(self.h, self.zone, io, buf)
}

// UdpRecv waits for one datagram on socket h into buf.
UdpRecv(h: uint, zone: u32, io: Io, buf: mut []u8): Error!(n: int, peer: Addr) {
    var from := core.IoAddr(ip: [0; 16], port: 0, v6: false, zone: 0)
    max := buf.Len()
    n := try CheckCount(core.IoRecvFrom(io, h, buf, mut from), max)
    if !PeerZoneOk(&from, zone) {
        fail InvalidAddress
    }
    return (n: n, peer: Unmapped(Addr(ip: from.ip, port: from.port, v6: from.v6)))
}

// UdpLocalAddr returns the bound address of UDP socket h, or ZeroAddr.
UdpLocalAddr(h: uint): Addr {
    if !rt.IsNativeSocket(h) {
        return ZeroAddr
    }
    var ip: [16]u8 = [0; 16]
    var w := core.IoWords(a: 0, b: 0, c: 0, d: 0)
    r := rt.SockAddress(rt.NativeIo(), h, false, mut ip[..], mut w)
    if r.err != core.SysErr.None || w.a < 0 || w.a > 65535 {
        return ZeroAddr
    }
    return Unmapped(Addr(ip: ip, port: Trunc[u16](w.a), v6: w.b == 1))
}

/// Connects the socket to `to`: only `to`'s datagrams are received. A
/// refused peer is reported as Refused on Linux and macOS; on Windows the
/// socket ignores ICMP, so it is not. Errors: InvalidAddress, Canceled, or
/// the OS's. O(1); allocates nothing.
export Datagram.Connect(&self, io: Io, to: Addr): Error!void {
    var dst := core.IoAddr(ip: to.ip, port: to.port, v6: to.v6, zone: 0)
    if !SendDst(self.v6, self.zone, &to, mut dst) {
        try CancelPoint(io)
        fail InvalidAddress
    }
    _ = try Check(task.NativeUdpConnect(io, self.h, dst))
    self.peer.Store(some(to))
}

/// The bound address of this socket (zero Addr for another Io's socket).
/// O(1): one system call; allocates nothing.
export Datagram.LocalAddr(&self): Addr {
    return UdpLocalAddr(self.h)
}

/// Asks the OS for receive and send buffer sizes of `recv` and `send`
/// bytes (0 keeps the current size). Errors: Unsupported for another Io's
/// socket; Os for the OS's refusal. O(1): at most two system calls;
/// allocates nothing.
export Datagram.SetBuffers(&self, recv: int, send: int): Error!void {
    if !rt.IsNativeSocket(self.h) {
        fail Unsupported
    }
    if recv != 0 {
        _ = try Check(rt.SockOption(rt.NativeIo(), self.h, core.IoSockRecvBuffer, i64(recv), 0))
    }
    if send != 0 {
        _ = try Check(rt.SockOption(rt.NativeIo(), self.h, core.IoSockSendBuffer, i64(send), 0))
    }
}

/// Sets IP Don't Fragment (`IP_DONTFRAGMENT` / `IP_MTU_DISCOVER` on IPv4;
/// `IPV6_DONTFRAG` / `IPV6_MTU_DISCOVER` on IPv6) where the OS allows.
/// Errors: Unsupported on a non-native socket (Sim, Tape), or Os.
export Datagram.SetDontFragment(&self): Error!void {
    if !rt.IsNativeSocket(self.h) {
        fail Unsupported
    }
    opt := if self.v6 { core.IoSockDontFragV6 } else { core.IoSockDontFrag }
    _ = try Check(rt.SockOption(rt.NativeIo(), self.h, opt, 1, 0))
}

/// Reports whether IP Don't Fragment is currently set on this native UDP socket.
export Datagram.DontFragment(&self): Error!bool {
    if !rt.IsNativeSocket(self.h) {
        fail Unsupported
    }
    opt := if self.v6 { core.IoSockDontFragV6 } else { core.IoSockDontFrag }
    r := try Check(rt.SockOptionGet(rt.NativeIo(), self.h, opt))
    return r != 0
}

/// Converts the socket into a SharedDatagram that tasks share by reference.
/// O(1); allocates nothing.
export Datagram.Share(self): SharedDatagram {
    Datagram(h, bound, v6, zone, peer) := self
    return SharedDatagram(
        h: h,
        bound: bound,
        v6: v6,
        zone: zone,
        peer: peer.Load(),
        reads: Mutex[u8].New(0),
        writes: Mutex[u8].New(0),
    )
}

// PeerZoneOk: an Addr on a socket of zone mine names the peer p: p is not
// link-local, so its zone does not choose the link, or its zone is the
// socket's, which Send puts back.
PeerZoneOk(p: &core.IoAddr, mine: u32): bool {
    return !p.v6 || !IsLinkLocal(&p.ip) || p.zone == mine
}

// IsLinkLocal: ip is in fe80::/10 (RFC 4291 2.5.6).
IsLinkLocal(ip: &[16]u8): bool {
    return ip[0] == 0xFE && ip[1] & 0xC0 == 0x80
}

/// The port the socket is bound to. O(1); allocates nothing.
export Datagram.Port(&self): int {
    return self.bound
}

/// Closes the socket. Infallible and not cancellable: the OS's answer to
/// the close is ignored. O(1); allocates nothing.
export Datagram.Close(self) {
    rt.Abandon(self.h)
    Datagram(h: _, bound: _, v6: _, zone: _, peer: _) := self
}
