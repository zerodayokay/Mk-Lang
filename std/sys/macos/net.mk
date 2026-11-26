// Why: std.net's TCP and UDP sockets need Darwin's socket calls, closing
// every descriptor on every failure path.
//
// What: SocketOpen, SocketOpenUdp, SocketBind, SocketListen(With),
// SocketName, the shutdown and close calls, ResolveHost and ResolveAll
// (which refuse: names are resolved above this layer), the resolver test
// probes, and the sockaddr helpers EncodeAddr, DecodeAddr and ConnectFail.
//
// How: libSystem's socket calls. Darwin has no SOCK_CLOEXEC, SOCK_NONBLOCK
// or MSG_NOSIGNAL, so every socket is made close-on-exec by fcntl right
// after socket(2) and gets SO_NOSIGPIPE (SockSetup), and an async socket is
// made non-blocking and registered on kqueue here. A Darwin sockaddr starts
// with its length byte (sa_len), then the family byte.

import std.core
import std.raw
import std.sys.lc

const SockStream: i32 = 1
const SockDgram: i32 = 2
const SoReuseAddr: i32 = 0x4
const SoMaxConn: i32 = 128
const IpProtoIpv6: i32 = 41
const Ipv6V6Only: i32 = 27

/// SocketOpen makes a TCP socket of family (Inet4, Inet6), close-on-exec
/// and SO_NOSIGPIPE; async makes it non-blocking and registers it on
/// kqueue. value is the fd.
/// O(1) system calls; allocates nothing beyond the OS socket.
export SocketOpen(family: u16, async: bool): OsResult {
    return SocketMake(family, SockStream, async)
}

/// TestSkipOff is Windows' test switch (no skip-on-success on its
/// sockets); macOS has no such mode: true, nothing changed.
/// O(1); allocates nothing.
export TestSkipOff(): bool {
    return true
}

/// SocketOpenUdp makes a UDP socket of family for the poller:
/// close-on-exec, SO_NOSIGPIPE, non-blocking and registered on kqueue,
/// dual-stack for Inet6, as SocketOpen's TCP sockets. value is the fd.
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
    unsafe "sa is the encoded address" {
        if connect(Trunc[i32](h), raw.PtrCast[u64, u8](raw.Data(sa[..])), Trunc[u32](n)) == -1 {
            return ConnectFail(Errno())
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
    unsafe "buf holds n bytes; sa holds 32 bytes" {
        for {
            sa_len[0] = 32
            r := recvfrom(Trunc[i32](h), buf, Trunc[uint](n), 0x80,
                raw.PtrCast[u64, u8](raw.DataMut(mut sa[..])), raw.DataMut(mut sa_len[..]))
            if r >= 0 {
                if !DecodeAddr(raw.PtrCast[u64, u8](raw.Data(sa[..])), from) {
                    *from = SockAddr(family: 0, port: 0, addr: [0; 16], scope_id: 0)
                }
                return Ok(i64(r))
            }
            e := Errno()
            if e == 4 {
                continue
            }
            if e == 35 {
                return Ok(-1)
            }
            return SockFail(e)
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
    unsafe "sa is the encoded address" {
        if bind(Trunc[i32](h), raw.PtrCast[u64, u8](raw.Data(sa[..])), Trunc[u32](n)) == -1 {
            return SockFail(Errno())
        }
    }
    return Ok(0)
}

// SocketMake makes a socket of family and type kind (SOCK_STREAM or
// SOCK_DGRAM); every failure closes what it made. A descriptor made here
// is not close-on-exec until SockSetup's fcntl: a spawn on another thread
// in that window passes it on unless the spawn closes every descriptor it
// was not told to pass (POSIX_SPAWN_CLOEXEC_DEFAULT).
SocketMake(family: u16, kind: i32, async: bool): OsResult {
    af: i32 = if family == Inet4 {
        i32(AfInet)
    } else if family == Inet6 {
        i32(AfInet6)
    } else {
        return FailWith(core.SysErr.InvalidAddress)
    }
    unsafe "libSystem calls; zero is a 4-byte option value in this frame" {
        s := socket(af, kind, 0)
        if s == -1 {
            return SockFail(Errno())
        }
        e := SockSetup(s, async)
        if e != 0 {
            _ = lc.CloseFd(s)
            return SockFail(e)
        }
        // IPv6 sockets are dual-stack (IPV6_V6ONLY 0, Go's choice for
        // "tcp"): an [::] listener accepts IPv4 peers. Set, never left to
        // the default.
        if family == Inet6 {
            var zero: [1]u32 = [0]
            if setsockopt(s, IpProtoIpv6, Ipv6V6Only, raw.PtrCast[u32, u8](raw.Data(zero[..])), 4) == -1 {
                e6 := Errno()
                _ = lc.CloseFd(s)
                return SockFail(e6)
            }
        }
        if async && !KqAdd(Trunc[uint](s)) {
            _ = lc.CloseFd(s)
            return Fail(Emfile)
        }
        return Ok(i64(s))
    }
}

/// SocketListen sets SO_REUSEADDR (a restarted server rebinds a port in
/// TIME_WAIT), binds h to a and listens; backlog <= 0 asks for the maximum
/// (SOMAXCONN, which Darwin also caps a larger backlog at).
/// O(1) system calls; allocates nothing.
export SocketListen(h: uint, a: &SockAddr, backlog: int): OsResult {
    return SocketListenWith(h, a, backlog, true)
}

/// SocketListenWith is SocketListen with the reuse choice: reuse sets
/// SO_REUSEADDR before the bind, and without it a port still in TIME_WAIT
/// is AddrInUse (rt.ListenWith).
/// O(1) system calls; allocates nothing.
export SocketListenWith(h: uint, a: &SockAddr, backlog: int, reuse: bool): OsResult {
    var sa: [4]u64 = [0; 4]
    var n := 0
    unsafe "sa holds 32 bytes" {
        n = EncodeAddr(a, raw.PtrCast[u64, u8](raw.DataMut(mut sa[..])))
    }
    if n == 0 {
        return FailWith(core.SysErr.InvalidAddress)
    }
    b := if backlog > 0 && backlog < 0x7FFF_FFFF { Trunc[i32](backlog) } else { SoMaxConn }
    s := Trunc[i32](h)
    var one: [1]u32 = [1]
    unsafe "sa is the encoded address; one is a 4-byte option value" {
        if reuse && setsockopt(s, SolSocket, SoReuseAddr, raw.PtrCast[u32, u8](raw.Data(one[..])), 4) == -1 {
            return SockFail(Errno())
        }
        if bind(s, raw.PtrCast[u64, u8](raw.Data(sa[..])), Trunc[u32](n)) == -1 {
            return SockFail(Errno())
        }
        if listen(s, b) == -1 {
            return SockFail(Errno())
        }
    }
    return Ok(0)
}

/// SocketName is getsockname: the address h is bound to.
/// O(1): one system call; allocates nothing.
export SocketName(h: uint, a: mut SockAddr): OsResult {
    var sa: [4]u64 = [0; 4]
    var n: [1]u32 = [32]
    unsafe "sa holds 32 bytes" {
        if getsockname(Trunc[i32](h), raw.PtrCast[u64, u8](raw.DataMut(mut sa[..])), raw.DataMut(mut n[..])) == -1 {
            return SockFail(Errno())
        }
        if !DecodeAddr(raw.PtrCast[u64, u8](raw.Data(sa[..])), a) {
            return FailWith(core.SysErr.InvalidAddress)
        }
    }
    return Ok(0)
}

/// SocketShutdown shuts down one or both directions of the socket h: how 0
/// read, 1 write, 2 both (SHUT_RD, SHUT_WR, SHUT_RDWR). Other for a how
/// above 2, else the OS's error. Darwin answers ENOTCONN for a direction
/// already shut, the read side included once the peer's FIN arrived, and
/// then skips the write side of a SHUT_RDWR; Linux shuts what is left and
/// answers ENOTCONN only for a socket with no connection. So on ENOTCONN
/// the write side of a 2 is shut on its own, which succeeds when it was
/// still open; otherwise the call succeeds while getpeername still finds a
/// peer.
/// O(1): one to three system calls; allocates nothing.
export SocketShutdown(h: uint, how: u32): OsResult {
    if how > 2 {
        return FailWith(core.SysErr.Other)
    }
    s := Trunc[i32](h)
    unsafe "shutdown passes no memory; getpeername writes into sa and n, this frame's, of the size n gives" {
        if shutdown(s, Trunc[i32](how)) == 0 {
            return Ok(0)
        }
        e := Errno()
        if e != Enotconn {
            return SockFail(e)
        }
        // The write side shut here is the call's success even when the
        // peer's ACK of that FIN, on loopback at once, has already ended
        // the connection, so getpeername would find none.
        if how == 2 && shutdown(s, 1) == 0 {
            return Ok(0)
        }
        var sa: [16]u64 = [0; 16]
        var n: [1]u32 = [128]
        if getpeername(s, raw.PtrCast[u64, u8](raw.DataMut(mut sa[..])), raw.DataMut(mut n[..])) == -1 {
            return SockFail(e)
        }
    }
    return Ok(0)
}

/// SocketShutdownClose leaves the poller, shuts both directions and closes
/// h. The shutdown's own error (a reset peer, a socket that never connected)
/// is ignored: it cannot keep the socket open. Darwin releases the
/// descriptor even when close reports EINTR, so it is never retried (a
/// retry could close a descriptor another thread was just given); only its
/// other errors are reported.
/// O(1) system calls; allocates nothing.
export SocketShutdownClose(h: uint): OsResult {
    KqDel(h)
    unsafe "shutdown passes no memory" {
        _ = shutdown(Trunc[i32](h), 2)
    }
    if lc.CloseFd(Trunc[i32](h)) == -1 {
        e := Errno()
        if e != Eintr {
            return Fail(e)
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
    KqDel(h)
    if lc.CloseFd(Trunc[i32](h)) == -1 {
        e := Errno()
        if e != Eintr {
            return Fail(e)
        }
    }
    return Ok(0)
}

/// ResolveAll answers NotFound for every name, so the variants keep one
/// API: std.task's resolve entry reads /etc/hosts itself and queries the
/// servers of resolv.conf over its UDP and TCP entries
/// (std/task/io_dns.mk); libSystem's resolver is not used.
/// O(1); allocates nothing.
export ResolveAll(name: str, out: mut []SockAddr): OsResult {
    return FailWith(core.SysErr.NotFound)
}

/// ResolveHost resolves a host name: every name is InvalidAddress here;
/// names are resolved by std.task's resolve entry above this layer. The
/// Windows variant asks GetAddrInfoW.
/// O(1); allocates nothing.
export ResolveHost(name: str, port: u16, out: mut SockAddr): OsResult {
    return FailWith(core.SysErr.InvalidAddress)
}

/// ResolveProbe is Windows' regression probe of its lookup routine
/// (std/sys/windows/resolve.mk); macOS has no completion routine, so it is
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

// ConnectFail is SockFail for a connect: a reset that answers the SYN
// (ECONNRESET) is the peer's refusal, as ECONNREFUSED.
ConnectFail(errno: i64): OsResult {
    if errno == 54 {
        return OsResult(value: 0, err: core.SysErr.Refused, os: errno)
    }
    return SockFail(errno)
}

// Swap16 converts a port between host and network order.
Swap16(v: u16): u16 {
    return (v >> 8) | (v << 8)
}

// OsAddrLen is the size of the Darwin sockaddr for family (sockaddr_in 16,
// sockaddr_in6 28), or 0.
OsAddrLen(family: u16): int {
    if family == Inet4 {
        return 16
    }
    return if family == Inet6 { 28 } else { 0 }
}

// EncodeAddr writes a as the Darwin sockaddr at p (at least 28 bytes) and
// returns its length, or 0 for an unknown family: sa_len at 0, sa_family
// at 1, the port (network order) at 2; sockaddr_in's address at 4;
// sockaddr_in6's flow info at 4 (0), address at 8 and scope id at 24.
EncodeAddr(a: &SockAddr, p: *u8): int {
    n := OsAddrLen(a.family)
    if n == 0 {
        return 0
    }
    unsafe "p holds 28 bytes" {
        raw.Fill(p, 0, 28)
        raw.Write(p, Trunc[u8](n))
        if a.family == Inet4 {
            raw.Write(raw.Offset(p, 1), Trunc[u8](AfInet))
            for i in 0..4 {
                raw.Write(raw.Offset(p, 4 + i), a.addr[i])
            }
        } else {
            raw.Write(raw.Offset(p, 1), Trunc[u8](AfInet6))
            for i in 0..16 {
                raw.Write(raw.Offset(p, 8 + i), a.addr[i])
            }
            Put32(p, 24, a.scope_id)
        }
    }
    Put16(p, 2, Swap16(a.port))
    return n
}

// DecodeAddr reads the Darwin sockaddr at p into a (the family byte at 1);
// false for a family other than AF_INET and AF_INET6 (a is then the zero
// address).
DecodeAddr(p: *u8, a: mut SockAddr): bool {
    a.family = 0
    a.port = 0
    a.addr = [0; 16]
    a.scope_id = 0
    unsafe "p holds the sockaddr the OS wrote" {
        f := u16(raw.Read(raw.Offset(p, 1)))
        if f == AfInet {
            a.family = Inet4
            for i in 0..4 {
                a.addr[i] = raw.Read(raw.Offset(p, 4 + i))
            }
        } else if f == AfInet6 {
            a.family = Inet6
            for i in 0..16 {
                a.addr[i] = raw.Read(raw.Offset(p, 8 + i))
            }
            a.scope_id = Get32(p, 24)
        } else {
            return false
        }
    }
    a.port = Swap16(Get16(p, 2))
    return true
}
