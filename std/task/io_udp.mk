// Why: std.net's net.Bind and Datagram need UDP sockets that wait on the
// poller like every other native I/O, without holding a thread.
//
// What: the native table's datagram entries NativeUdp, NativeSendTo and
// NativeRecvFrom, and the address conversions ToSock and FromSock.
//
// How: a UDP socket is a runtime handle on the poller; SendTo and RecvFrom
// are one Await each, and every error path closes the socket.

import std.core
import std.raw
import std.rt
import std.sys

// ToSock and FromSock convert between core.IoAddr and sys.SockAddr (the
// same bytes: IPv4 in ip[0..4], the port in host order).
ToSock(a: core.IoAddr): sys.SockAddr {
    f := if a.v6 { sys.Inet6 } else { sys.Inet4 }
    return sys.SockAddr(family: f, port: a.port, addr: a.ip, scope_id: a.zone)
}

FromSock(a: &sys.SockAddr): core.IoAddr {
    return core.IoAddr(ip: a.addr, port: a.port, v6: a.family == sys.Inet6, zone: a.scope_id)
}

// NativeUdp makes a UDP socket bound to addr: value the runtime handle, os
// the bound port (as NativeListen), so port 0 tells the caller the port the
// OS chose. Every error path closes the socket.
NativeUdp(io: Io, addr: core.IoAddr): core.IoResult {
    if Stop(io) {
        return ErrOf(core.SysErr.Canceled)
    }
    a := ToSock(addr)
    s := sys.SocketOpenUdp(a.family)
    if s.err != core.SysErr.None {
        return rt.IoOf(s)
    }
    sh := Trunc[uint](s.value)
    b := sys.SocketBind(sh, &a)
    if b.err != core.SysErr.None {
        _ = sys.SocketShutdownClose(sh)
        return rt.IoOf(b)
    }
    var bound := a
    n := sys.SocketName(sh, mut bound)
    if n.err != core.SysErr.None {
        _ = sys.SocketShutdownClose(sh)
        return rt.IoOf(n)
    }
    h := rt.NewSocketHandle(sh, a.family)
    if h == 0 {
        _ = sys.SocketShutdownClose(sh)
        return ErrOf(core.SysErr.Other)
    }
    return core.IoResult(value: Trunc[i64](h), err: core.SysErr.None, os: i64(bound.port))
}

// NativeSendTo sends data as one datagram to to: value the bytes sent (all
// of data, or an error: a datagram is never sent in part). An empty data is
// an empty datagram.
NativeSendTo(io: Io, h: uint, data: []u8, to: core.IoAddr): core.IoResult {
    if Stop(io) {
        return ErrOf(core.SysErr.Canceled)
    }
    r := rt.Rec(h)
    unsafe "h is a live socket record; the poller only reads a send buffer" {
        var op := sys.NewOp(sys.OpSendTo, (*r).os)
        op.buf = raw.Data(data)
        op.len = data.Len()
        op.addr = ToSock(to)
        e := Await(raw.OfMut(mut op), Cancellable(io))
        return if e != core.SysErr.None { ErrOf(e) } else { rt.IoOf(op.res) }
    }
}

// NativeRecvFrom waits for one datagram: value its bytes in buf, from its
// source; a datagram longer than buf is cut to it and os is 1. from is
// written only on success.
NativeRecvFrom(io: Io, h: uint, buf: mut []u8, from: mut core.IoAddr): core.IoResult {
    if Stop(io) {
        return ErrOf(core.SysErr.Canceled)
    }
    r := rt.Rec(h)
    unsafe "h is a live socket record; buf is the caller's until Await returns" {
        var op := sys.NewOp(sys.OpRecvFrom, (*r).os)
        op.buf = raw.DataMut(buf)
        op.len = buf.Len()
        e := Await(raw.OfMut(mut op), Cancellable(io))
        if e != core.SysErr.None {
            return ErrOf(e)
        }
        if op.res.err == core.SysErr.None {
            *from = FromSock(&op.addr)
        }
        return rt.IoOf(op.res)
    }
}

UdpConnectJob(a: *core.IoCallArgs) {
    unsafe "a is the call's live record; p holds core.IoAddr" {
        if (*a).pn < SizeOf[core.IoAddr]() {
            (*a).res = ErrOf(core.SysErr.InvalidAddress)
            return
        }
        addr := raw.Read(raw.PtrCast[u8, core.IoAddr]((*a).p))
        sa := ToSock(addr)
        (*a).res = rt.IoOf(sys.SocketConnectUdp((*a).os, &sa))
    }
}

/// Connects the UDP socket `h` to `to` without waiting. Runs through
/// IoOpUdpConnect so simulated and recorded Ios answer it. O(1); allocates
/// nothing.
export NativeUdpConnect(io: Io, h: uint, to: core.IoAddr): core.IoResult {
    var a := core.IoCallArgs(
        op: core.IoOpUdpConnect,
        route: core.IoRouteChecked,
        h: h,
        os: 0,
        p: raw.Null[u8](),
        pn: 0,
        q: raw.Null[u8](),
        qn: 0,
        out: raw.Null[u8](),
        outn: 0,
        w: core.IoWords(a: 0, b: 0, c: 0, d: 0),
        res: core.IoResult(value: 0, err: core.SysErr.None, os: 0),
        job: UdpConnectJob,
        stop: raw.Null[u32](),
    )
    unsafe "to and a live until IoCall returns" {
        a.p = raw.PtrCast[core.IoAddr, u8](raw.Of(&to))
        a.pn = SizeOf[core.IoAddr]()
        return core.IoCall(io, raw.OfMut(mut a))
    }
}

TryRecvFromJob(a: *core.IoCallArgs) {
    unsafe "a is the call's live record; out and q live until return" {
        var s := sys.SockAddr(family: 0, port: 0, addr: [0; 16], scope_id: 0)
        r := sys.SocketRecvFromNow((*a).os, (*a).out, (*a).outn, mut s)
        if r.err == core.SysErr.None && r.value >= 0 && (*a).qn >= SizeOf[core.IoAddr]() {
            raw.Write(raw.PtrCast[u8, core.IoAddr]((*a).q), FromSock(&s))
        }
        (*a).res = rt.IoOf(r)
    }
}

/// Receives one queued datagram from UDP socket `h` without waiting, or
/// returns value -1 with err None when nothing is queued. Runs through
/// IoOpTryRecvFrom so simulated and recorded Ios answer it. O(1); allocates
/// nothing.
export NativeTryRecvFrom(io: Io, h: uint, buf: mut []u8, from: mut core.IoAddr): core.IoResult {
    var a := core.IoCallArgs(
        op: core.IoOpTryRecvFrom,
        route: core.IoRouteChecked,
        h: h,
        os: 0,
        p: raw.Null[u8](),
        pn: 0,
        q: raw.Null[u8](),
        qn: 0,
        out: raw.Null[u8](),
        outn: 0,
        w: core.IoWords(a: 0, b: 0, c: 0, d: 0),
        res: core.IoResult(value: 0, err: core.SysErr.None, os: 0),
        job: TryRecvFromJob,
        stop: raw.Null[u32](),
    )
    unsafe "buf, from and a live until IoCall returns" {
        a.out = raw.DataMut(buf)
        a.outn = buf.Len()
        a.q = raw.PtrCast[core.IoAddr, u8](raw.OfMut(from))
        a.qn = SizeOf[core.IoAddr]()
        return core.IoCall(io, raw.OfMut(mut a))
    }
}
