// Why: std.fs, std.net, std.term and std.time reach the OS through the
// IoTable's call entry (std.core's IoOp constants); this file is the macOS
// side of those calls, through libSystem.
//
// What: lseek, flock, mmap and munmap (MapFile, UnmapFile), stat and lstat
// (StatEx), utimensat, chmod, symlink, readlink, getcwd, CopyFile (a read
// and write loop), setsockopt, getsockopt, getpeername, isatty and
// TIOCGWINSZ, TimeZone and OnNinep, with the helpers FsCBytes,
// FsFailCall, FsStopped, FsCopyFds and FsPeerAddr.
//
// How: each call is one libSystem call or a bounded loop of them, and every
// path that opens a descriptor closes it on every error path. errno is read
// through __error() right after the call that failed. EINTR from the pool's
// interrupt is Canceled, which the pool repeats when its own job was not
// cancelled (MapError); CopyFile's loops retry it themselves. Errors map
// through MapError, plus FsFailCall's rows: ENOTSUP (45), EOPNOTSUPP (102)
// and ENOSYS (78) are Unsupported. Paths are copied NUL-terminated into a
// PATH_MAX (1,024) stack buffer.

import std.core
import std.raw
import std.sys.lc

// flock's operations (Darwin's <sys/file.h>).
const FsLockSh: i32 = 1
const FsLockEx: i32 = 2
const FsLockNb: i32 = 4
const FsLockUn: i32 = 8
const FsProtRead: i32 = 1
const FsTiocgwinsz: uint = 0x4008_7468
// The socket options (Darwin's <sys/socket.h> and <netinet/tcp.h>).
const FsIpProtoTcp: i32 = 6
const FsTcpNoDelay: i32 = 0x1
// FsTcpKeepAlive is TCP_KEEPALIVE: the idle seconds before the first probe
// (Linux's TCP_KEEPIDLE).
const FsTcpKeepAlive: i32 = 0x10
const FsSolSocket: i32 = 0xFFFF
const FsSoReuseAddr: i32 = 0x4
const FsSoKeepAlive: i32 = 0x8
const FsSoSndBuf: i32 = 0x1001
const FsSoRcvBuf: i32 = 0x1002
const FsIpProtoIp: i32 = 0
const FsIpDontFrag: i32 = 28
const FsIpProtoIpv6: i32 = 41
const FsIpv6DontFrag: i32 = 62
// FsSoLingerSec is SO_LINGER_SEC: struct linger with l_linger in seconds
// (plain SO_LINGER counts it in clock ticks).
const FsSoLingerSec: i32 = 0x1080
// CopyFile's stack buffer.
const FsCopyBuffer: int = 65536

// FsFailCall is Fail for the calls of this file: ENOTSUP, EOPNOTSUPP and
// ENOSYS are Unsupported (a file system without the call), the rest
// MapError's.
FsFailCall(errno: i64): OsResult {
    if errno == 45 || errno == 102 || errno == 78 {
        return OsResult(value: 0, err: core.SysErr.Unsupported, os: errno)
    }
    return Fail(errno)
}

// FsCBytes copies the path bytes p NUL-terminated into buf: false when it
// does not fit or holds a NUL (InvalidPath). The bytes need not be UTF-8:
// the kernel takes any bytes but NUL, and the file system judges the rest.
FsCBytes(p: []u8, buf: mut []u8): bool {
    if p.Len() + 1 > buf.Len() {
        return false
    }
    for i, x in p {
        if x == 0 {
            return false
        }
        buf[i] = x
    }
    buf[p.Len()] = 0
    return true
}

/// Seek moves h's offset (lseek): whence 0 from the start, 1 from the
/// current offset, 2 from the end; value is the new offset. A whence above
/// 2 is Other with no call; a result before 0 is the OS's EINVAL.
/// O(1): one system call; allocates nothing.
export Seek(h: uint, off: i64, whence: u32): OsResult {
    if whence > 2 {
        return FailWith(core.SysErr.Other)
    }
    unsafe "lseek passes no memory" {
        r := lseek(Trunc[i32](h), off, Trunc[i32](whence))
        return if r < 0 { FsFailCall(Errno()) } else { Ok(r) }
    }
}

/// FileLock takes, or tries once to take, the whole-file advisory lock of
/// h's open file (flock: shared or exclusive; LOCK_NB with once): value 1
/// when held, 0 when once is set and another open file holds a conflicting
/// lock (EWOULDBLOCK). A flock lock and an fcntl lock (LockRange) do not
/// see each other; std uses only flock for File.Lock. Without once the
/// call blocks the thread until the lock is free: std.task calls it with
/// once and waits on its own clock instead, so a cancel ends the wait.
/// O(1): one system call besides the wait; allocates nothing.
export FileLock(h: uint, exclusive: bool, once: bool): OsResult {
    var how := if exclusive { FsLockEx } else { FsLockSh }
    if once {
        how = how | FsLockNb
    }
    unsafe "flock passes no memory" {
        if flock(Trunc[i32](h), how) == 0 {
            return Ok(1)
        }
    }
    e := Errno()
    return if e == 35 { Ok(0) } else { FsFailCall(e) }
}

/// FileUnlock drops h's flock lock (LOCK_UN); unlocking a file that holds
/// none succeeds.
/// O(1): one system call; allocates nothing.
export FileUnlock(h: uint): OsResult {
    unsafe "flock passes no memory" {
        if flock(Trunc[i32](h), FsLockUn) != 0 {
            return FsFailCall(Errno())
        }
    }
    return Ok(0)
}

/// MapFile maps len bytes of h from off, read-only and private:
/// off is rounded down to the page (16 KiB on arm64, 4 KiB on x86_64), so
/// w.c is the mapping's base and w.d its length (UnmapFile's arguments),
/// and value is the address of byte off. len must be above 0 (std maps 0
/// bytes with no call); a negative off is Other with no call. A read past
/// the file's end inside the mapping faults (SIGBUS), which the runtime
/// reports as a panic (std.rt's mapping table).
/// O(1): one system call; allocates nothing but the mapping.
export MapFile(h: uint, off: i64, len: int, w: mut core.IoWords): OsResult {
    if off < 0 || len <= 0 {
        return FailWith(core.SysErr.Other)
    }
    page := i64(PageSize())
    base := off - (off % page)
    delta := off - base
    total := i64(len) + delta
    if total < i64(len) {
        return FailWith(core.SysErr.Other)
    }
    unsafe "mmap makes new pages; nothing of the caller's is passed" {
        p := mmap(raw.Null[u8](), Trunc[uint](total), FsProtRead, MapPrivate, Trunc[i32](h), base)
        if raw.Addr(p) == MapFailed {
            return FsFailCall(Errno())
        }
        w.c = Trunc[i64](raw.Addr(p))
        w.d = total
        return Ok(Trunc[i64](raw.Addr(p)) + delta)
    }
}

/// UnmapFile releases a mapping MapFile made: base and len as it gave them in
/// w.c and w.d.
/// O(1): one system call; allocates nothing.
export UnmapFile(base: uint, len: int): OsResult {
    unsafe "the caller's mapping, not used after this" {
        if munmap(raw.FromAddr[u8](base), Trunc[uint](len)) != 0 {
            return FsFailCall(Errno())
        }
    }
    return Ok(0)
}

/// StatEx fills w for path p: a the kind (core.IoKind*), b the size (0 for
/// a directory or a link), c the modification time in nanoseconds since
/// the epoch, d the mode bits (st_mode & 0o7777). nofollow reports a final
/// symbolic link itself (lstat, fs.StatLink).
/// O(1): one system call; allocates nothing (the path is copied into a
/// stack buffer).
export StatEx(p: []u8, nofollow: bool, w: mut core.IoWords): OsResult {
    var c: [1024]u8 = [0; 1024]
    if !FsCBytes(p, mut c[..]) {
        return FailWith(core.SysErr.InvalidPath)
    }
    var sb: [18]u64 = [0; 18]
    if PathStat(c[..], nofollow, mut sb) != 0 {
        return FsFailCall(Errno())
    }
    var st := core.IoStat(kind: 0, size: 0, modified: 0)
    FsStatInto(&sb, mut st)
    w.a = i64(st.kind)
    w.b = st.size
    w.c = st.modified
    w.d = i64(StatMode(&sb) & 0o7777)
    return Ok(0)
}

/// SetModified sets p's modification time to nanos since the epoch,
/// following a link, and keeps its access time: stat reads the access
/// time, and utimensat writes it back with the new modification time (an
/// access between the two calls is lost).
/// O(1): two system calls; allocates nothing (the path is copied into a
/// stack buffer).
export SetModified(p: []u8, nanos: i64): OsResult {
    var c: [1024]u8 = [0; 1024]
    if !FsCBytes(p, mut c[..]) {
        return FailWith(core.SysErr.InvalidPath)
    }
    var sb: [18]u64 = [0; 18]
    if PathStat(c[..], false, mut sb) != 0 {
        return FsFailCall(Errno())
    }
    var sec := nanos / 1_000_000_000
    var ns := nanos % 1_000_000_000
    if ns < 0 {
        sec -= 1
        ns += 1_000_000_000
    }
    // st_atimespec is at byte 32 of struct stat.
    var ts: [4]i64 = [Trunc[i64](sb[4]), Trunc[i64](sb[5]), sec, ns]
    unsafe "ts is two struct timespec, c is NUL-terminated" {
        if utimensat(FsAtFdcwd, raw.Data(c[..]), raw.DataMut(mut ts[..]), 0) != 0 {
            return FsFailCall(Errno())
        }
    }
    return Ok(0)
}

/// SetMode sets p's mode bits (chmod; mode & 0o7777), following a link.
/// O(1): one system call; allocates nothing (the path is copied into a
/// stack buffer).
export SetMode(p: []u8, mode: u32): OsResult {
    var c: [1024]u8 = [0; 1024]
    if !FsCBytes(p, mut c[..]) {
        return FailWith(core.SysErr.InvalidPath)
    }
    unsafe "c is NUL-terminated" {
        if chmod(raw.Data(c[..]), Trunc[u16](mode & 0o7777)) != 0 {
            return FsFailCall(Errno())
        }
    }
    return Ok(0)
}

/// Symlink makes link a symbolic link to target (symlink); target is
/// stored as given, not checked. An existing link path is Exists.
/// O(1): one system call; allocates nothing (both paths are copied into
/// stack buffers).
export Symlink(target: []u8, link: []u8): OsResult {
    var t: [1024]u8 = [0; 1024]
    var l: [1024]u8 = [0; 1024]
    if !FsCBytes(target, mut t[..]) || !FsCBytes(link, mut l[..]) {
        return FailWith(core.SysErr.InvalidPath)
    }
    unsafe "t and l are NUL-terminated" {
        if lc.SymlinkPath(raw.Data(t[..]), raw.Data(l[..])) != 0 {
            return FsFailCall(Errno())
        }
    }
    return Ok(0)
}

/// ReadLink writes the target of the symbolic link p into out: value is
/// the bytes written, and value == out.Len() means the target may be cut
/// (the caller calls again with more room). Not a link is the OS's EINVAL,
/// Other.
/// O(1): one system call; allocates nothing (the path is copied into a
/// stack buffer).
export ReadLink(p: []u8, out: mut []u8): OsResult {
    var c: [1024]u8 = [0; 1024]
    if !FsCBytes(p, mut c[..]) {
        return FailWith(core.SysErr.InvalidPath)
    }
    unsafe "c is NUL-terminated, out holds out.Len() bytes" {
        r := lc.ReadLinkAt(raw.Data(c[..]), raw.DataMut(out), Trunc[uint](out.Len()))
        return if r < 0 { FsFailCall(Errno()) } else { Ok(i64(r)) }
    }
}

/// Cwd writes the working directory into out, without its NUL: value is
/// the bytes written, out.Len() when it did not fit (ERANGE), so the caller
/// calls again with more room.
/// O(len) of the path: one call and a scan for its NUL; allocates nothing.
export Cwd(out: mut []u8): OsResult {
    if out.Len() == 0 {
        return Ok(0)
    }
    unsafe "out holds out.Len() bytes" {
        p := getcwd(raw.DataMut(out), Trunc[uint](out.Len()))
        if raw.IsNull(p) {
            e := Errno()
            return if e == 34 { Ok(i64(out.Len())) } else { FsFailCall(e) }
        }
    }
    var n := 0
    for n < out.Len() && out[n] != 0 {
        n += 1
    }
    return Ok(i64(n))
}

/// Absolute writes p as an absolute path into out: p itself when it starts
/// with '/', else the working directory, '/' and p, with no dot segment or
/// link resolved (Go's filepath.Abs cleans; std.fs does). value as Cwd's.
/// O(len) of the path: one call at most; allocates nothing.
export Absolute(p: []u8, out: mut []u8): OsResult {
    var n := 0
    if p.Len() == 0 || p[0] != 47 {
        r := Cwd(out)
        if r.err != core.SysErr.None || r.value >= i64(out.Len()) {
            return r
        }
        n = Trunc[int](r.value)
        if n == 0 || out[n - 1] != 47 {
            out[n] = 47
            n += 1
        }
    }
    for x in p {
        if n == out.Len() {
            return Ok(i64(out.Len()))
        }
        out[n] = x
        n += 1
    }
    return if n == out.Len() { Ok(i64(out.Len())) } else { Ok(i64(n)) }
}

// FsOpenStop is open(2) of the NUL-terminated c, repeated on EINTR while
// stop is not set: the descriptor, or the failure (Canceled once stop is
// set).
FsOpenStop(c: []u8, flags: i32, perm: i32, stop: *u32): OsResult {
    for {
        if FsStopped(stop) {
            return FailWith(core.SysErr.Canceled)
        }
        var fd: i32 = -1
        unsafe "c is a NUL-terminated path in the caller's frame" {
            fd = open(raw.Data(c), flags, perm)
        }
        if fd >= 0 {
            return Ok(i64(fd))
        }
        e := Errno()
        if e != 4 {
            return FsFailCall(e)
        }
    }
}

/// CopyFile copies the bytes of src into dst, which it creates or
/// truncates (mode 0o666 less the umask): a read and write loop through a
/// 64 KiB stack buffer, as Go's io.Copy over two os.Files does on macOS
/// (copyfile(3) cannot see the cancel word). value is the bytes copied. A
/// copy of a file onto itself (one device and inode, by any path or link)
/// is Denied before anything is truncated, as Windows' CopyFileExW answers
/// (a sharing violation): dst is opened without O_TRUNC, both are fstat'ed,
/// and only then is dst truncated. A source that is not a regular file is
/// refused on its open descriptor before dst is opened: IsDirectory (os
/// 21) for a directory, Other with os 22 (EINVAL) for anything else. Both
/// descriptors are closed on every path. Short counts are retried. stop,
/// when not null, is the caller's cancel word (std.task's pool job): read
/// before each open and each chunk, and at an EINTR (the pool's interrupt
/// ends a wait in an open, a FIFO's with no writer, or a read); once it is
/// 1 the copy ends with Canceled, dst holding an unknown prefix of src. An
/// EINTR with stop 0 is retried.
/// O(size / 64 KiB) system calls; allocates nothing.
export CopyFile(src: []u8, dst: []u8, stop: *u32): OsResult {
    var a: [1024]u8 = [0; 1024]
    var b: [1024]u8 = [0; 1024]
    if !FsCBytes(src, mut a[..]) || !FsCBytes(dst, mut b[..]) {
        return FailWith(core.SysErr.InvalidPath)
    }
    r := FsOpenStop(a[..], ORdOnly | OCloExec, 0, stop)
    if r.err != core.SysErr.None {
        return r
    }
    from := Trunc[uint](r.value)
    // Only a regular source is copied, checked on the open descriptor
    // before dst is opened, so a source swapped for a directory or a FIFO
    // after std.fs's stat never creates or truncates dst.
    var ss: [18]u64 = [0; 18]
    if StatFd(from, mut ss) != 0 {
        e := Errno()
        _ = Close(from)
        return FsFailCall(e)
    }
    kind := StatMode(&ss) & 0o170000
    if kind != 0o100000 {
        _ = Close(from)
        return if kind == 0o040000 { OsResult(value: 0, err: core.SysErr.IsDirectory, os: 21) } else { OsResult(value: 0, err: core.SysErr.Other, os: 22) }
    }
    r2 := FsOpenStop(b[..], OWrOnly | OCreat | OCloExec, 0o666, stop)
    if r2.err != core.SysErr.None {
        _ = Close(from)
        return r2
    }
    to := Trunc[uint](r2.value)
    // The same file is refused before the truncate empties it.
    var sb: [18]u64 = [0; 18]
    var bad := Ok(0)
    if StatFd(to, mut sb) != 0 {
        bad = FsFailCall(Errno())
    } else if (ss[0] & 0xFFFF_FFFF) == (sb[0] & 0xFFFF_FFFF) && ss[StIno / 8] == sb[StIno / 8] {
        bad = FailWith(core.SysErr.Denied)
    } else if (StatMode(&sb) & 0o170000) == 0o100000 {
        // Only a regular file is truncated: a device or a FIFO (/dev/null)
        // is written as it is.
        for {
            var t: i32 = 0
            unsafe "ftruncate passes no memory" {
                t = ftruncate(Trunc[i32](to), 0)
            }
            if t == 0 {
                break
            }
            e := Errno()
            if e != 4 {
                bad = FsFailCall(e)
                break
            }
        }
    }
    if bad.err != core.SysErr.None {
        _ = Close(from)
        _ = Close(to)
        return bad
    }
    res := FsCopyFds(from, to, stop)
    _ = Close(from)
    c := Close(to)
    if res.err == core.SysErr.None && c.err != core.SysErr.None {
        return c
    }
    return res
}

// FsStopped reports whether the cancel word stop (null: none) is set.
// Edge: reads std.task's CancelJob store; a load that misses it is
// followed by the next step's load, or by the interrupt's EINTR.
FsStopped(stop: *u32): bool {
    if raw.IsNull(stop) {
        return false
    }
    unsafe "stop is the live pool job's cancel word" {
        return (*raw.PtrCast[u32, raw.Atomic[u32]](stop)).Load() != 0
    }
}

// FsCopyFds copies from's bytes from its offset to its end into to,
// ending with Canceled once stop is set (CopyFile).
FsCopyFds(from: uint, to: uint, stop: *u32): OsResult {
    var total: i64 = 0
    var buf: [65536]u8 = [0; 65536]
    for {
        if FsStopped(stop) {
            return FailWith(core.SysErr.Canceled)
        }
        var n := 0
        unsafe "buf holds FsCopyBuffer bytes" {
            n = read(Trunc[i32](from), raw.DataMut(mut buf[..]), Trunc[uint](FsCopyBuffer))
        }
        if n < 0 {
            e := Errno()
            if e == 4 {
                continue
            }
            return FsFailCall(e)
        }
        if n == 0 {
            return Ok(total)
        }
        var done := 0
        for done < n {
            var m := 0
            unsafe "buf[done..n] is filled" {
                m = write(Trunc[i32](to), raw.Offset(raw.Data(buf[..]), done), Trunc[uint](n - done))
            }
            if m < 0 {
                e := Errno()
                if e == 4 {
                    if FsStopped(stop) {
                        return FailWith(core.SysErr.Canceled)
                    }
                    continue
                }
                return FsFailCall(e)
            }
            if m == 0 {
                return FailWith(core.SysErr.Other)
            }
            done += m
        }
        total += i64(n)
    }
}

// FsSockOpt is the level and name of the option opt (core.IoSock*):
// level 0 for an option this layer does not know.
FsSockOpt(opt: i64): (level: i32, name: i32) {
    if opt == core.IoSockNoDelay {
        return (level: FsIpProtoTcp, name: FsTcpNoDelay)
    }
    if opt == core.IoSockKeepAlive {
        return (level: FsSolSocket, name: FsSoKeepAlive)
    }
    if opt == core.IoSockReuseAddr {
        return (level: FsSolSocket, name: FsSoReuseAddr)
    }
    if opt == core.IoSockRecvBuffer {
        return (level: FsSolSocket, name: FsSoRcvBuf)
    }
    if opt == core.IoSockSendBuffer {
        return (level: FsSolSocket, name: FsSoSndBuf)
    }
    if opt == core.IoSockLinger {
        return (level: FsSolSocket, name: FsSoLingerSec)
    }
    if opt == core.IoSockDontFrag {
        return (level: FsIpProtoIp, name: FsIpDontFrag)
    }
    if opt == core.IoSockDontFragV6 {
        return (level: FsIpProtoIpv6, name: FsIpv6DontFrag)
    }
    return (level: 0, name: 0)
}

/// SocketOption sets one option of the socket h (core.IoSock*): NoDelay
/// (b 1 on), KeepAlive (b 1 on; c > 0 the idle seconds before the first
/// probe, TCP_KEEPALIVE), ReuseAddr, RecvBuffer and SendBuffer (b bytes,
/// kept as given), Linger (b 1 on, c seconds, SO_LINGER_SEC: on with 0
/// makes close send a reset and leave no TIME_WAIT). An unknown option, or
/// a value outside an i32, is Other with no call.
/// O(1): one or two system calls; allocates nothing.
export SocketOption(h: uint, opt: i64, b: i64, c: i64): OsResult {
    if b < 0 || b > 0x7FFF_FFFF || c < 0 || c > 0x7FFF_FFFF {
        return FailWith(core.SysErr.Other)
    }
    o := FsSockOpt(opt)
    if o.level == 0 {
        return FailWith(core.SysErr.Other)
    }
    var v: [2]i32 = [Trunc[i32](b), 0]
    var size: u32 = 4
    if opt == core.IoSockLinger {
        v[1] = Trunc[i32](c)
        size = 8
    }
    unsafe "v holds size bytes" {
        if setsockopt(Trunc[i32](h), o.level, o.name, raw.PtrCast[i32, u8](raw.DataMut(mut v[..])), size) != 0 {
            return FsFailCall(Errno())
        }
        if opt == core.IoSockKeepAlive && b == 1 && c > 0 {
            var idle: [1]i32 = [Trunc[i32](c)]
            if setsockopt(Trunc[i32](h), FsIpProtoTcp, FsTcpKeepAlive, raw.PtrCast[i32, u8](raw.DataMut(mut idle[..])),
                4) != 0 {
                return FsFailCall(Errno())
            }
        }
    }
    return Ok(0)
}

/// SocketOptionGet reads one option of the socket h (core.IoSock*) with
/// getsockopt: value NoDelay's or KeepAlive's 0 or 1 (and for KeepAlive,
/// os the TCP_KEEPALIVE seconds), ReuseAddr's 0 or 1, RecvBuffer's or
/// SendBuffer's bytes, Linger's 1 or 0 (os its seconds). An unknown option
/// is Other with no call.
/// O(1): one or two system calls; allocates nothing.
export SocketOptionGet(h: uint, opt: i64): OsResult {
    o := FsSockOpt(opt)
    if o.level == 0 {
        return FailWith(core.SysErr.Other)
    }
    var v: [2]i32 = [0, 0]
    var n: u32 = 8
    unsafe "v holds 8 bytes and n their count" {
        if getsockopt(Trunc[i32](h), o.level, o.name, raw.PtrCast[i32, u8](raw.DataMut(mut v[..])), raw.OfMut(mut n)) != 0 {
            return FsFailCall(Errno())
        }
        if opt == core.IoSockLinger {
            return OsResult(value: if v[0] != 0 { 1 } else { 0 }, err: core.SysErr.None, os: i64(v[1]))
        }
        value: i64 = if opt == core.IoSockRecvBuffer || opt == core.IoSockSendBuffer { i64(v[0]) } else if v[0] != 0 { 1 } else { 0 }
        var idle: i64 = 0
        if opt == core.IoSockKeepAlive {
            var w: i32 = 0
            var m: u32 = 4
            if getsockopt(Trunc[i32](h), FsIpProtoTcp, FsTcpKeepAlive, raw.PtrCast[i32, u8](raw.OfMut(mut w)),
                raw.OfMut(mut m)) == 0 {
                idle = i64(w)
            }
        }
        return OsResult(value: value, err: core.SysErr.None, os: idle)
    }
}

// FsPeerAddr decodes the Darwin sockaddr at p (sa_len, then sa_family, a
// byte each; sockaddr_in: port at 2 big-endian, address at 4;
// sockaddr_in6: port at 2, sin6_flowinfo at 4, address at 8, scope id at
// 24) into a; false for any other family.
FsPeerAddr(p: *u8, a: mut SockAddr): bool {
    unsafe "p holds a sockaddr of at least 28 bytes" {
        fam := u16(raw.Read(raw.Offset(p, 1)))
        port := (u16(raw.Read(raw.Offset(p, 2))) << 8) | u16(raw.Read(raw.Offset(p, 3)))
        if fam == AfInet {
            a.family = Inet4
            a.port = port
            a.addr = [0; 16]
            for i in 0..4 {
                a.addr[i] = raw.Read(raw.Offset(p, 4 + i))
            }
            a.scope_id = 0
            return true
        }
        if fam == AfInet6 {
            a.family = Inet6
            a.port = port
            for i in 0..16 {
                a.addr[i] = raw.Read(raw.Offset(p, 8 + i))
            }
            a.scope_id = raw.ReadUnaligned(raw.PtrCast[u8, u32](raw.Offset(p, 24)))
            return true
        }
        return false
    }
}

/// SocketPeer is getpeername: the address h is connected to. A socket
/// that is not connected is the OS's ENOTCONN, Other; a peer of a family
/// other than IPv4 and IPv6 is InvalidAddress.
/// O(1): one system call; allocates nothing.
export SocketPeer(h: uint, a: mut SockAddr): OsResult {
    var sa: [16]u64 = [0; 16]
    var n: u32 = 128
    unsafe "sa holds 128 bytes, a struct sockaddr_storage" {
        p := raw.PtrCast[u64, u8](raw.DataMut(mut sa[..]))
        if getpeername(Trunc[i32](h), p, raw.OfMut(mut n)) != 0 {
            return SockFail(Errno())
        }
        if !FsPeerAddr(p, a) {
            return FailWith(core.SysErr.InvalidAddress)
        }
    }
    return Ok(0)
}

/// IsTerminal reports whether h is a terminal (isatty).
/// O(1): one system call; allocates nothing.
export IsTerminal(h: uint): bool {
    unsafe "isatty passes no memory" {
        return isatty(Trunc[i32](h)) == 1
    }
}

/// TermSize reads the terminal h's size (TIOCGWINSZ): w.a columns, w.b
/// rows. Not a terminal is the OS's ENOTTY, Other.
/// O(1): one system call; allocates nothing.
export TermSize(h: uint, w: mut core.IoWords): OsResult {
    // struct winsize: ws_row, ws_col, ws_xpixel, ws_ypixel, a u16 each.
    var ws: [4]u16 = [0; 4]
    unsafe "ws holds a struct winsize (8 bytes), passed as ioctl's pointer argument" {
        if ioctl(Trunc[i32](h), FsTiocgwinsz, raw.PtrCast[u16, u8](raw.DataMut(mut ws[..]))) != 0 {
            return FsFailCall(Errno())
        }
    }
    w.a = i64(ws[1])
    w.b = i64(ws[0])
    return Ok(0)
}

/// TimeZone is Windows' time-zone record: Unsupported on macOS, whose zone
/// std.time reads from TZ and /etc/localtime (a link into the system's
/// zoneinfo).
/// O(1); allocates nothing.
export TimeZone(year: i64, out: mut []u8): OsResult {
    return FailWith(core.SysErr.Unsupported)
}

/// OnNinep reports whether h is a file on a 9p mount, which macOS has
/// none of: false.
/// O(1); allocates nothing.
export OnNinep(h: uint): bool {
    return false
}
