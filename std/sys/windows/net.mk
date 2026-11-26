// Why: std.net's TCP and UDP sockets must work on Windows through the
// completion port, with the same answers as on Linux.
//
// What: SocketOpen, SocketOpenUdp, SocketBind, SocketListen,
// SocketListenWith, SocketName, the shutdown and close calls, ResolveAll
// and ResolveHost, over the Winsock entries of ws2_32.mk.
//
// How: every socket is overlapped and non-inheritable; an async socket is
// attached to the completion port here, with skip-on-success when every
// provider allows it (ws2_32.mk). Every failure path closes what it made
// (no handle leaks).

import std.core
import std.raw

const SoMaxConn: i32 = 0x7FFF_FFFF
const WsaeInval: u32 = 10022
const IpProtoIpv6: i32 = 41
const Ipv6V6Only: i32 = 27

// AttachSocket associates s with the port; skip-on-success only when safe
// for sockets, so a synchronous success posts nothing exactly when
// PollSubmit returns false for it.
AttachSocket(s: uint): bool {
    unsafe "a kernel32 call that passes no memory, only values and handles" {
        if CreateIoCompletionPort(s, Port(), 0, 0) == 0 {
            return false
        }
        if SkipSockets() {
            return SetFileCompletionNotificationModes(s, SkipModes) != 0
        }
    }
    return true
}

/// SocketOpen makes a TCP socket of family (Inet4, Inet6); async attaches it
/// to the port. value is the SOCKET.
/// O(1) system calls; allocates nothing beyond the OS socket.
export SocketOpen(family: u16, async: bool): OsResult {
    af := OsFamily(family)
    if af == 0 {
        return FailWith(core.SysErr.InvalidAddress)
    }
    if !WinsockReady() {
        return FailWith(core.SysErr.Other)
    }
    s := NewSocket(af)
    if s == InvalidSocket {
        return WsaFail()
    }
    // Dual-stack, as on Linux: Windows defaults IPV6_V6ONLY to 1, Linux to
    // 0; both set 0, so an [::] listener accepts IPv4 peers.
    if family == Inet6 {
        var zero: u32 = 0
        unsafe "zero is a DWORD option value" {
            if SetSockOpt(s, IpProtoIpv6, Ipv6V6Only, raw.PtrCast[u32, u8](raw.OfMut(mut zero)), 4) != 0 {
                r := WsaFail()
                _ = CloseSocketRaw(s)
                return r
            }
        }
    }
    if async && !AttachSocket(s) {
        r := LastError()
        _ = CloseSocketRaw(s)
        return r
    }
    return Ok(Trunc[i64](s))
}

/// SocketOpenUdp makes a UDP socket of family for the poller:
/// overlapped, non-inheritable, attached to the port, dual-stack for Inet6.
/// SIO_UDP_CONNRESET is turned off, so an ICMP port-unreachable for an
/// earlier send never fails a later receive (Go and libuv do the same; Linux
/// reports it only on a connected socket). value is the SOCKET.
/// O(1) system calls; allocates nothing beyond the OS socket.
export SocketOpenUdp(family: u16): OsResult {
    af := OsFamily(family)
    if af == 0 {
        return FailWith(core.SysErr.InvalidAddress)
    }
    if !WinsockReady() {
        return FailWith(core.SysErr.Other)
    }
    s := NewDgram(af)
    if s == InvalidSocket {
        return WsaFail()
    }
    var zero: u32 = 0
    var got: u32 = 0
    unsafe "zero is a DWORD option value; WSAIoctl with no overlapped is synchronous" {
        if family == Inet6 {
            if SetSockOpt(s, IpProtoIpv6, Ipv6V6Only, raw.PtrCast[u32, u8](raw.OfMut(mut zero)), 4) != 0 {
                r := WsaFail()
                _ = CloseSocketRaw(s)
                return r
            }
        }
        f := raw.Read(raw.PtrCast[u64, fn(uint, u32, *u8, u32, *u8, u32, *u32, *u8, *u8): i32](WsFn(WsIoctl)))
        if f(s, SioUdpConnReset, raw.PtrCast[u32, u8](raw.OfMut(mut zero)), 4, raw.Null[u8](), 0,
            raw.OfMut(mut got), raw.Null[u8](), raw.Null[u8]()) != 0 {
            r := WsaFail()
            _ = CloseSocketRaw(s)
            return r
        }
        var one: u32 = 1
        if f(s, FionBio, raw.PtrCast[u32, u8](raw.OfMut(mut one)), 4, raw.Null[u8](), 0,
            raw.OfMut(mut got), raw.Null[u8](), raw.Null[u8]()) != 0 {
            r := WsaFail()
            _ = CloseSocketRaw(s)
            return r
        }
    }
    if !AttachSocket(s) {
        r := LastError()
        _ = CloseSocketRaw(s)
        return r
    }
    return Ok(Trunc[i64](s))
}

// SioUdpConnReset is SIO_UDP_CONNRESET (_WSAIOW(IOC_VENDOR, 12)).
const SioUdpConnReset: u32 = 0x9800_000C
// FionBio is FIONBIO (_IOW('f', 126, u_long)) for non-blocking TryRecvFrom.
const FionBio: u32 = 0x8004_667E
const WsaeWouldBlock: u32 = 10035
const WsaeMsgSizeNow: u32 = 10040

/// SocketConnectUdp connects the UDP socket h to a (no wait).
/// O(1) system calls; allocates nothing.
export SocketConnectUdp(h: uint, a: &SockAddr): OsResult {
    if !WinsockReady() {
        return FailWith(core.SysErr.Other)
    }
    var sa: [4]u64 = [0; 4]
    var n := 0
    unsafe "sa holds 32 bytes" {
        n = EncodeAddr(a, raw.PtrCast[u64, u8](raw.DataMut(mut sa[..])))
    }
    if n == 0 {
        return FailWith(core.SysErr.InvalidAddress)
    }
    unsafe "sa is the encoded address" {
        if ConnectRaw(h, raw.PtrCast[u64, u8](raw.Data(sa[..])), Trunc[i32](n)) != 0 {
            return WsaFail()
        }
    }
    return Ok(0)
}

/// SocketRecvFromNow performs a non-blocking non-overlapped WSARecvFrom on h:
/// returns the count (os 1 when truncated), or -1 with err None when nothing
/// is queued. O(1) system calls; allocates nothing.
export SocketRecvFromNow(h: uint, buf: *u8, len: int, from: mut SockAddr): OsResult {
    if !WinsockReady() {
        return FailWith(core.SysErr.Other)
    }
    var n := len
    if n > 0x7FFF_FFFF {
        n = 0x7FFF_FFFF
    }
    if n < 0 {
        n = 0
    }
    var sa: [4]u64 = [0; 4]
    var sa_len: i32 = 32
    var count: u32 = 0
    var flags: u32 = 0
    unsafe "buf holds n bytes; sa holds 32 bytes; non-overlapped WSARecvFrom on FIONBIO socket" {
        var wb: [2]u64 = [Trunc[u64](n), u64(raw.Addr(buf))]
        bufs := raw.PtrCast[u64, u8](raw.Data(wb[..]))
        f := raw.Read(raw.PtrCast[u64, fn(uint, *u8, u32, *u32, *u32, *u8, *i32, *u8, *u8): i32](WsFn(WsRecvFrom)))
        r := f(h, bufs, 1, raw.OfMut(mut count), raw.OfMut(mut flags),
            raw.PtrCast[u64, u8](raw.DataMut(mut sa[..])), raw.OfMut(mut sa_len),
            raw.Null[u8](), raw.Null[u8]())
        if r == 0 {
            if !DecodeAddr(raw.PtrCast[u64, u8](raw.Data(sa[..])), from) {
                *from = SockAddr(family: 0, port: 0, addr: [0; 16], scope_id: 0)
            }
            return Ok(i64(count))
        }
        e := WsaError()
        if e == WsaeWouldBlock {
            return Ok(-1)
        }
        if e == WsaeMsgSizeNow {
            if !DecodeAddr(raw.PtrCast[u64, u8](raw.Data(sa[..])), from) {
                *from = SockAddr(family: 0, port: 0, addr: [0; 16], scope_id: 0)
            }
            return OsResult(value: i64(count), err: core.SysErr.None, os: 1)
        }
        return Fail(e)
    }
}

/// SocketBind binds h to a (no SO_REUSEADDR: on Windows it lets another
/// socket take the port).
/// O(1) system calls; allocates nothing.
export SocketBind(h: uint, a: &SockAddr): OsResult {
    if !WinsockReady() {
        return FailWith(core.SysErr.Other)
    }
    var sa: [4]u64 = [0; 4]
    var n := 0
    unsafe "sa holds 32 bytes" {
        n = EncodeAddr(a, raw.PtrCast[u64, u8](raw.DataMut(mut sa[..])))
    }
    if n == 0 {
        return FailWith(core.SysErr.InvalidAddress)
    }
    unsafe "sa is the encoded address" {
        if BindRaw(h, raw.PtrCast[u64, u8](raw.Data(sa[..])), Trunc[i32](n)) != 0 {
            return WsaFail()
        }
    }
    return Ok(0)
}

/// SocketListen binds h to a and listens; backlog <= 0 asks for the OS
/// maximum. No SO_REUSEADDR: on Windows it lets another socket take the port.
/// O(1) system calls; allocates nothing.
export SocketListen(h: uint, a: &SockAddr, backlog: int): OsResult {
    return SocketListenWith(h, a, backlog, true)
}

/// SocketListenWith is SocketListen with the reuse choice: Windows binds a
/// listener over TIME_WAIT either way, so reuse adds nothing, and without
/// it SO_EXCLUSIVEADDRUSE is set before the bind, which keeps any other
/// socket off the port while this one holds it (rt.ListenWith).
/// O(1) system calls; allocates nothing.
export SocketListenWith(h: uint, a: &SockAddr, backlog: int, reuse: bool): OsResult {
    if !WinsockReady() {
        return FailWith(core.SysErr.Other)
    }
    var sa: [4]u64 = [0; 4]
    var n := 0
    unsafe "sa holds 32 bytes" {
        n = EncodeAddr(a, raw.PtrCast[u64, u8](raw.DataMut(mut sa[..])))
    }
    if n == 0 {
        return FailWith(core.SysErr.InvalidAddress)
    }
    b := if backlog > 0 && backlog < 0x7FFF_FFFF { Trunc[i32](backlog) } else { SoMaxConn }
    if !reuse {
        var one: [1]u32 = [1]
        unsafe "Winsock; one holds 4 bytes" {
            if SetSockOpt(h, SolSocket, SoExclusiveAddrUse, raw.PtrCast[u32, u8](raw.DataMut(mut one[..])), 4) != 0 {
                return WsaFail()
            }
        }
    }
    unsafe "sa is the encoded address" {
        if BindRaw(h, raw.PtrCast[u64, u8](raw.Data(sa[..])), Trunc[i32](n)) != 0 {
            return WsaFail()
        }
    }
    return if ListenRaw(h, b) != 0 { WsaFail() } else { Ok(0) }
}

/// SocketName is getsockname: the address h is bound to.
/// O(1): one system call; allocates nothing.
export SocketName(h: uint, a: mut SockAddr): OsResult {
    if !WinsockReady() {
        return FailWith(core.SysErr.Other)
    }
    var sa: [4]u64 = [0; 4]
    var n: i32 = 32
    unsafe "sa holds 32 bytes" {
        if GetSockNameRaw(h, raw.PtrCast[u64, u8](raw.DataMut(mut sa[..])), raw.OfMut(mut n)) != 0 {
            return WsaFail()
        }
        if !DecodeAddr(raw.PtrCast[u64, u8](raw.Data(sa[..])), a) {
            return FailWith(core.SysErr.InvalidAddress)
        }
    }
    return Ok(0)
}

/// SocketShutdown shuts down one or both directions of the socket h: how 0
/// read, 1 write, 2 both (SD_RECEIVE, SD_SEND, SD_BOTH). Other for a how
/// above 2, else the OS's error.
/// O(1): one system call; allocates nothing.
export SocketShutdown(h: uint, how: u32): OsResult {
    if how > 2 {
        return FailWith(core.SysErr.Other)
    }
    if !WinsockReady() {
        return FailWith(core.SysErr.Other)
    }
    return if ShutdownRaw(h, Trunc[i32](i64(how))) != 0 { WsaFail() } else { Ok(0) }
}

/// SocketShutdownClose shuts both directions and closes h. The shutdown's
/// own error (a peer that reset, a socket that never connected) is ignored:
/// it cannot keep the socket open, and reporting it would make the caller
/// close again a handle that is already gone. A failed closesocket is
/// reported; the handle is then the caller's.
/// O(1) system calls; allocates nothing.
export SocketShutdownClose(h: uint): OsResult {
    if !WinsockReady() {
        return FailWith(core.SysErr.Other)
    }
    _ = ShutdownRaw(h, 2)
    return if CloseSocketRaw(h) != 0 { WsaFail() } else { Ok(0) }
}

/// SocketAbortClose closes h with no shutdown first, for a socket whose
/// SO_LINGER is on with 0 seconds: closesocket then resets the connection
/// and leaves no TIME_WAIT entry (after a shutdown both ends keep one,
/// measured). A failed closesocket is reported; the handle is then the
/// caller's.
/// O(1) system calls; allocates nothing.
export SocketAbortClose(h: uint): OsResult {
    if !WinsockReady() {
        return FailWith(core.SysErr.Other)
    }
    return if CloseSocketRaw(h) != 0 { WsaFail() } else { Ok(0) }
}

/// ResolveAll is GetAddrInfoW for name (the resolve entry): every
/// answer in the resolver's order, one per address (SOCK_STREAM hints), as
/// many as out holds; value the count written. A name with no answer
/// (WSAHOST_NOT_FOUND 11001, WSANO_DATA 11004) is NotFound. Blocking: the
/// pool runs it.
/// O(answers) besides the lookup; allocates nothing that outlives the call.
export ResolveAll(name: str, out: mut []SockAddr): OsResult {
    if !WinsockReady() {
        return FailWith(core.SysErr.Other)
    }
    var wname: [256]u16 = [0; 256]
    if name.Len() == 0 || Wide(name, mut wname[..]) < 0 {
        return FailWith(core.SysErr.InvalidAddress)
    }
    var hints: [6]u64 = [0; 6]
    var res: u64 = 0
    var n := 0
    unsafe "the ready table's GetAddrInfoW and FreeAddrInfoW; the list is the resolver's until it is freed" {
        hp := raw.PtrCast[u64, u8](raw.DataMut(mut hints[..]))
        Put32(hp, 8, Trunc[u32](i64(SockStream)))
        Put32(hp, 12, Trunc[u32](i64(IpProtoTcp)))
        get := raw.Read(raw.PtrCast[u64, fn(*u16, *u16, *u8, *u64): i32](WsFn(WsGetAddrInfo)))
        e := get(raw.Data(wname[..]), raw.Null[u16](), hp, raw.OfMut(mut res))
        if e != 0 {
            code := Trunc[u32](i64(e))
            if code == 11001 || code == 11004 {
                return OsResult(value: 0, err: core.SysErr.NotFound, os: i64(code))
            }
            return Fail(code)
        }
        var cur := res
        for cur != 0 && n < out.Len() {
            ai := raw.FromAddr[u8](Trunc[uint](cur))
            sa := Get64(ai, 32)
            if sa != 0 && DecodeAddr(raw.FromAddr[u8](Trunc[uint](sa)), mut out[n]) {
                n += 1
            }
            cur = Get64(ai, 40)
        }
        free := raw.Read(raw.PtrCast[u64, fn(u64)](WsFn(WsFreeAddrInfo)))
        free(res)
    }
    return Ok(i64(n))
}

/// ResolveHost resolves a host name to one TCP address with port:
/// GetAddrInfoW (a blocking call: std.task runs it on the pool),
/// the first IPv4 answer, else the first IPv6 one. Errors keep the
/// resolver's own code: 11001 (WSAHOST_NOT_FOUND) is InvalidAddress; a name
/// with a NUL or longer than a DNS name can be is InvalidAddress too. The
/// answer list is freed before return; nothing is kept.
/// O(answers) besides the lookup; allocates nothing that outlives the call.
export ResolveHost(name: str, port: u16, out: mut SockAddr): OsResult {
    if !WinsockReady() {
        return FailWith(core.SysErr.Other)
    }
    var wname: [256]u16 = [0; 256]
    if name.Len() == 0 || Wide(name, mut wname[..]) < 0 {
        return FailWith(core.SysErr.InvalidAddress)
    }
    // ADDRINFOW (48 bytes on x64): ai_flags 0, ai_family 4, ai_socktype 8,
    // ai_protocol 12, ai_addrlen 16, ai_canonname 24, ai_addr 32, ai_next 40.
    var hints: [6]u64 = [0; 6]
    var res: u64 = 0
    unsafe "the ready table's GetAddrInfoW and FreeAddrInfoW; the list is the resolver's until it is freed" {
        hp := raw.PtrCast[u64, u8](raw.DataMut(mut hints[..]))
        Put32(hp, 8, Trunc[u32](i64(SockStream)))
        Put32(hp, 12, Trunc[u32](i64(IpProtoTcp)))
        get := raw.Read(raw.PtrCast[u64, fn(*u16, *u16, *u8, *u64): i32](WsFn(WsGetAddrInfo)))
        e := get(raw.Data(wname[..]), raw.Null[u16](), hp, raw.OfMut(mut res))
        if e != 0 {
            return Fail(Trunc[u32](i64(e)))
        }
        var pick: u64 = 0
        var cur := res
        for cur != 0 && pick == 0 {
            ai := raw.FromAddr[u8](Trunc[uint](cur))
            if Get32(ai, 4) == u32(AfInet) && Get64(ai, 32) != 0 {
                pick = Get64(ai, 32)
            }
            cur = Get64(ai, 40)
        }
        cur = res
        for cur != 0 && pick == 0 {
            ai := raw.FromAddr[u8](Trunc[uint](cur))
            if Get32(ai, 4) == u32(AfInet6) && Get64(ai, 32) != 0 {
                pick = Get64(ai, 32)
            }
            cur = Get64(ai, 40)
        }
        ok := if pick != 0 { DecodeAddr(raw.FromAddr[u8](Trunc[uint](pick)), out) } else { false }
        free := raw.Read(raw.PtrCast[u64, fn(u64)](WsFn(WsFreeAddrInfo)))
        free(res)
        if !ok {
            return FailWith(core.SysErr.InvalidAddress)
        }
        out.port = port
    }
    return Ok(0)
}
