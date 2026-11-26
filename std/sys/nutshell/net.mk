// Why: std.sys for os "nutshell" is a placeholder that must export exactly
// what std/sys/linux exports, so std checks and builds for the target until
// the Nutshell backend replaces this directory.
//
// What: the socket calls (SocketOpen to SocketAbortClose), ResolveAll,
// ResolveHost and the resolver probes.
//
// How: every call fails with Unsupported.

/// SocketOpen has std/sys/linux's contract; this placeholder fails with Other.
/// O(1); allocates nothing.
export SocketOpen(family: u16, async: bool): OsResult {
    return Unsupported()
}

/// TestSkipOff is the Windows variant's test switch; nothing to turn off.
/// O(1); allocates nothing.
export TestSkipOff(): bool {
    return true
}

/// SocketOpenUdp has std/sys/linux's contract; this placeholder fails with Other.
/// O(1); allocates nothing.
export SocketOpenUdp(family: u16): OsResult {
    return Unsupported()
}

/// SocketConnectUdp has std/sys/linux's contract; this placeholder fails with Unsupported.
/// O(1); allocates nothing.
export SocketConnectUdp(h: uint, a: &SockAddr): OsResult {
    return Unsupported()
}

/// SocketRecvFromNow has std/sys/linux's contract; this placeholder fails with Unsupported.
/// O(1); allocates nothing.
export SocketRecvFromNow(h: uint, buf: *u8, len: int, from: mut SockAddr): OsResult {
    return Unsupported()
}

/// SocketBind has std/sys/linux's contract; this placeholder fails with Other.
/// O(1); allocates nothing.
export SocketBind(h: uint, a: &SockAddr): OsResult {
    return Unsupported()
}

/// SocketListen has std/sys/linux's contract; this placeholder fails with Other.
/// O(1); allocates nothing.
export SocketListen(h: uint, a: &SockAddr, backlog: int): OsResult {
    return Unsupported()
}

/// SocketListenWith has std/sys/linux's contract; this placeholder answers
/// Unsupported. O(1); allocates nothing.
export SocketListenWith(h: uint, a: &SockAddr, backlog: int, reuse: bool): OsResult {
    return Unsupported()
}

/// SocketName has std/sys/linux's contract; this placeholder fails with Other.
/// O(1); allocates nothing.
export SocketName(h: uint, a: mut SockAddr): OsResult {
    return Unsupported()
}

/// SocketShutdown has std/sys/linux's contract; this placeholder fails with Other.
/// O(1); allocates nothing.
export SocketShutdown(h: uint, how: u32): OsResult {
    return Unsupported()
}

/// SocketShutdownClose has std/sys/linux's contract; this placeholder fails with Other.
/// O(1); allocates nothing.
export SocketShutdownClose(h: uint): OsResult {
    return Unsupported()
}

/// SocketAbortClose has std/sys/linux's contract; this placeholder
/// answers Unsupported. O(1); allocates nothing.
export SocketAbortClose(h: uint): OsResult {
    return NotHere()
}

/// ResolveAll has std/sys/linux's contract; this placeholder fails with Other.
/// O(1); allocates nothing.
export ResolveAll(name: str, out: mut []SockAddr): OsResult {
    return Unsupported()
}

/// ResolveHost has std/sys/linux's contract; this placeholder fails with Other.
/// O(1); allocates nothing.
export ResolveHost(name: str, port: u16, out: mut SockAddr): OsResult {
    return Unsupported()
}

/// ResolveProbe has std/sys/linux's contract; this placeholder returns `0`.
/// O(1); allocates nothing.
export ResolveProbe(): u32 {
    return 0
}

/// ResolveCancelProbe has std/sys/linux's contract; this placeholder returns `0`.
/// O(1); allocates nothing.
export ResolveCancelProbe(): u32 {
    return 0
}

/// ResolveRaceProbe has std/sys/linux's contract; this placeholder returns `0`.
/// O(1); allocates nothing.
export ResolveRaceProbe(rounds: int): u32 {
    return 0
}
