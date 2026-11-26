// Why: std.net's TCP and UDP sockets need the Linux socket calls without
// libc, closing every descriptor on every failure path.
//
// What: SocketOpen, SocketOpenUdp, SocketBind, SocketListen(With),
// SocketName, the shutdown and close calls, ResolveHost and ResolveAll
// (which refuse: no resolver without libc), and the resolver test probes.
//
// How: raw system calls; every socket is close-on-exec, and an async socket
// on epoll is made non-blocking and registered here (io_uring needs
// neither). Names are resolved by std.task's resolve entry above this layer.

import std.core
import std.raw

const SockStream: uint = 1
const SockDgram: uint = 2
const SockCloExec: uint = 0x80000
const SockNonBlock: uint = 0x800
const SolSocket: uint = 1
const SoReuseAddr: uint = 2
const SoMaxConn: uint = 4096
const IpProtoIpv6: uint = 41
const Ipv6V6Only: uint = 26

/// SocketOpen makes a TCP socket of family (Inet4, Inet6); value is the fd.
/// O(1) system calls; allocates nothing beyond the OS socket.
export SocketOpen(family: u16, async: bool): OsResult {
    return SocketMake(family, SockStream, async)
}

/// TestSkipOff is Windows' test switch (no skip-on-success on its
/// sockets); Linux has no such mode: true, nothing changed.
/// O(1); allocates nothing.
export TestSkipOff(): bool {
    return true
}

/// SocketOpenUdp makes a UDP socket of family for the poller:
/// close-on-exec, non-blocking and registered on epoll, dual-stack for
/// Inet6, as SocketOpen's TCP sockets. value is the fd.
/// O(1) system calls; allocates nothing beyond the OS socket.
export SocketOpenUdp(family: u16): OsResult {
    return SocketMake(family, SockDgram, true)
}

/// SocketConnectUdp connects the UDP socket h to a (no wait).
/// O(1) system calls; allocates nothing.
export SocketConnectUdp(h: uint, a: &SockAddr): OsResult {
    var sa: [4]u64 = [0; 4]
    var n := 0
    unsafe "sa holds 32 bytes" {
        n = EncodeAddr(a, raw.PtrCast[u64, u8](raw.DataMut(mut sa[..])))
    }
    if n == 0 {
        return FailWith(core.SysErr.InvalidAddress)
    }
    unsafe "system call; sa is the encoded address" {
        r := Syscall(Nrs().connect, h, raw.Addr(raw.Data(sa[..])), Trunc[uint](n), 0, 0, 0)
        if IsErr(r) {
            return ConnectFail(i64(-r))
        }
    }
    return Ok(0)
}

/// SocketRecvFromNow performs a non-blocking recvfrom(MSG_DONTWAIT) on h:
/// returns the count, or -1 with err None when nothing is queued.
/// O(1) system calls; allocates nothing.
export SocketRecvFromNow(h: uint, buf: *u8, len: int, from: mut SockAddr): OsResult {
    var sa: [4]u64 = [0; 4]
    var sa_len: [1]u32 = [32]
    n := if len < 0 { 0 } else { len }
    unsafe "system call; buf holds n bytes; sa holds 32 bytes" {
        for {
            sa_len[0] = 32
            r := Syscall(Nrs().recvfrom, h, raw.Addr(buf), Trunc[uint](n), 0x40,
                raw.Addr(raw.DataMut(mut sa[..])), raw.Addr(raw.DataMut(mut sa_len[..])))
            if !IsErr(r) {
                if !DecodeAddr(raw.PtrCast[u64, u8](raw.Data(sa[..])), from) {
                    *from = SockAddr(family: 0, port: 0, addr: [0; 16], scope_id: 0)
                }
                return Ok(i64(r))
            }
            if r == -4 {
                continue
            }
            if r == -11 {
                return Ok(-1)
            }
            return SockFail(i64(-r))
        }
    }
}

/// SocketBind binds h to a, with no SO_REUSEADDR (a UDP socket with it
/// could share a port another process holds).
/// O(1) system calls; allocates nothing.
export SocketBind(h: uint, a: &SockAddr): OsResult {
    var sa: [4]u64 = [0; 4]
    var n := 0
    unsafe "sa holds 32 bytes" {
        n = EncodeAddr(a, raw.PtrCast[u64, u8](raw.DataMut(mut sa[..])))
    }
    if n == 0 {
        return FailWith(core.SysErr.InvalidAddress)
    }
    unsafe "system call; sa is the encoded address" {
        r := Syscall(Nrs().bind, h, raw.Addr(raw.Data(sa[..])), Trunc[uint](n), 0, 0, 0)
        if IsErr(r) {
            return SockFail(i64(-r))
        }
    }
    return Ok(0)
}

// SocketMake makes a socket of family and type kind (SOCK_STREAM or
// SOCK_DGRAM); every failure closes what it made.
SocketMake(family: u16, kind: uint, async: bool): OsResult {
    af: uint = if family == Inet4 {
        uint(AfInet)
    } else if family == Inet6 {
        uint(AfInet6)
    } else {
        return FailWith(core.SysErr.InvalidAddress)
    }
    epoll := async && !IsUring()
    ty := if epoll { kind | SockCloExec | SockNonBlock } else { kind | SockCloExec }
    unsafe "system calls; zero is a 4-byte option value in this frame" {
        r := Syscall(Nrs().socket, af, ty, 0, 0, 0, 0)
        if IsErr(r) {
            return SockFail(i64(-r))
        }
        s := Trunc[uint](r)
        // IPv6 sockets are dual-stack on both targets (IPV6_V6ONLY 0, Go's
        // choice for "tcp"): an [::] listener accepts IPv4 peers. The OS
        // defaults differ (Linux net.ipv6.bindv6only, 0 by default; Windows
        // 1), so it is set, never left to the default.
        if family == Inet6 {
            var zero: [1]u32 = [0]
            r6 := Syscall(Nrs().setsockopt, s, IpProtoIpv6, Ipv6V6Only, raw.Addr(raw.Data(zero[..])), 4, 0)
            if IsErr(r6) {
                _ = Syscall(Nrs().close, s, 0, 0, 0, 0, 0)
                return SockFail(i64(-r6))
            }
        }
        if epoll && !EpollAdd(s) {
            _ = Syscall(Nrs().close, s, 0, 0, 0, 0, 0)
            return Fail(24)
        }
        return Ok(i64(r))
    }
}

/// SocketListen sets SO_REUSEADDR (a restarted server rebinds a port in
/// TIME_WAIT), binds h to a and listens; backlog <= 0 asks for the maximum.
/// O(1) system calls; allocates nothing.
export SocketListen(h: uint, a: &SockAddr, backlog: int): OsResult {
    return SocketListenWith(h, a, backlog, true)
}

/// SocketListenWith is SocketListen with the reuse choice: reuse sets
/// SO_REUSEADDR before the bind, and without it a port still in TIME_WAIT
/// is AddrInUse (rt.ListenWith); Linux shares a TIME_WAIT port only when
/// the socket that left it set SO_REUSEADDR too. O(1) system calls; allocates nothing.
export SocketListenWith(h: uint, a: &SockAddr, backlog: int, reuse: bool): OsResult {
    var sa: [4]u64 = [0; 4]
    var n := 0
    unsafe "sa holds 32 bytes" {
        n = EncodeAddr(a, raw.PtrCast[u64, u8](raw.DataMut(mut sa[..])))
    }
    if n == 0 {
        return FailWith(core.SysErr.InvalidAddress)
    }
    b := if backlog > 0 && backlog < 0x7FFF_FFFF { Trunc[uint](backlog) } else { SoMaxConn }
    var one: [1]u32 = [1]
    unsafe "system call; sa is the encoded address" {
        if reuse {
            r := Syscall(Nrs().setsockopt, h, SolSocket, SoReuseAddr, raw.Addr(raw.Data(one[..])), 4, 0)
            if IsErr(r) {
                return SockFail(i64(-r))
            }
        }
        r2 := Syscall(Nrs().bind, h, raw.Addr(raw.Data(sa[..])), Trunc[uint](n), 0, 0, 0)
        if IsErr(r2) {
            return SockFail(i64(-r2))
        }
        r3 := Syscall(Nrs().listen, h, b, 0, 0, 0, 0)
        if IsErr(r3) {
            return SockFail(i64(-r3))
        }
    }
    return Ok(0)
}

/// SocketName is getsockname: the address h is bound to.
/// O(1): one system call; allocates nothing.
export SocketName(h: uint, a: mut SockAddr): OsResult {
    var sa: [4]u64 = [0; 4]
    var n: [1]u32 = [32]
    unsafe "system call; sa holds 32 bytes" {
        r := Syscall(Nrs().getsockname, h, raw.Addr(raw.DataMut(mut sa[..])), raw.Addr(raw.DataMut(mut n[..])), 0, 0, 0)
        if IsErr(r) {
            return SockFail(i64(-r))
        }
        if !DecodeAddr(raw.PtrCast[u64, u8](raw.Data(sa[..])), a) {
            return FailWith(core.SysErr.InvalidAddress)
        }
    }
    return Ok(0)
}

/// SocketShutdown shuts down one or both directions of the socket h: how 0
/// read, 1 write, 2 both (SHUT_RD, SHUT_WR, SHUT_RDWR). Other for a how
/// above 2, else the OS's error.
/// O(1): one system call; allocates nothing.
export SocketShutdown(h: uint, how: u32): OsResult {
    if how > 2 {
        return FailWith(core.SysErr.Other)
    }
    unsafe "a system call that passes no memory, only values and handles" {
        r := Syscall(Nrs().shutdown, h, uint(how), 0, 0, 0, 0)
        if IsErr(r) {
            return SockFail(i64(-r))
        }
    }
    return Ok(0)
}

/// SocketShutdownClose leaves the poller, shuts both directions and closes
/// h. The shutdown's own error (a reset peer, a socket that never connected)
/// is ignored: it cannot keep the socket open. close always releases the
/// descriptor on Linux, EINTR included, so it is never retried (a retry
/// could close a descriptor another thread was just given); only its other
/// errors are reported.
/// O(1) system calls; allocates nothing.
export SocketShutdownClose(h: uint): OsResult {
    if !IsUring() {
        EpollDel(h)
    }
    unsafe "a system call that passes no memory, only values and handles" {
        _ = Syscall(Nrs().shutdown, h, 2, 0, 0, 0, 0)
        r := Syscall(Nrs().close, h, 0, 0, 0, 0, 0)
        if IsErr(r) && r != -4 {
            return Result(r)
        }
    }
    return Ok(0)
}

/// SocketAbortClose leaves the poller and closes h with no shutdown first,
/// for a socket whose SO_LINGER is on with 0 seconds: the close resets the
/// connection and leaves no TIME_WAIT entry (a shutdown would send a FIN
/// first). Errors as SocketShutdownClose's.
/// O(1) system calls; allocates nothing.
export SocketAbortClose(h: uint): OsResult {
    if !IsUring() {
        EpollDel(h)
    }
    unsafe "a system call that passes no memory, only values and handles" {
        r := Syscall(Nrs().close, h, 0, 0, 0, 0, 0)
        if IsErr(r) && r != -4 {
            return Result(r)
        }
    }
    return Ok(0)
}

/// ResolveAll answers NotFound for every name, so the two variants keep one
/// API: Linux has no resolver without libc, and std.task's resolve entry
/// reads /etc/hosts itself and queries the servers of resolv.conf over its
/// UDP and TCP entries (std/task/io_dns.mk).
/// O(1); allocates nothing.
export ResolveAll(name: str, out: mut []SockAddr): OsResult {
    return FailWith(core.SysErr.NotFound)
}

/// ResolveHost resolves a host name: Linux has no resolver without
/// libc, so every name is InvalidAddress here; names are resolved by
/// std.task's resolve entry above this layer. The Windows variant asks
/// GetAddrInfoW.
/// O(1); allocates nothing.
export ResolveHost(name: str, port: u16, out: mut SockAddr): OsResult {
    return FailWith(core.SysErr.InvalidAddress)
}

/// ResolveProbe is Windows' regression probe of its lookup routine
/// (std/sys/windows/resolve.mk); Linux has no completion routine, so it is
/// 0, "not this target", and the block judges it only on Windows.
/// Tests only. O(1); allocates nothing.
export ResolveProbe(): u32 {
    return 0
}

/// ResolveCancelProbe and ResolveRaceProbe are Windows' probes of its lookup
/// cancel (std/sys/windows/resolve.mk): 0 here, as ResolveProbe.
/// Tests only. O(1); allocates nothing.
export ResolveCancelProbe(): u32 {
    return 0
}

/// ResolveRaceProbe: 0, as ResolveCancelProbe.
/// Tests only. O(1); allocates nothing.
export ResolveRaceProbe(rounds: int): u32 {
    return 0
}
