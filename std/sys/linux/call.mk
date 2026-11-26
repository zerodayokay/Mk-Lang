// Why: std.fs, std.net, std.term and std.time reach the OS through the
// IoTable's call entry (std.core's IoOp constants); this file is the Linux
// side of those calls, without libc.
//
// What: lseek, flock, mmap and munmap (MapFile, UnmapFile), statx with
// AT_SYMLINK_NOFOLLOW, utimensat, fchmodat, symlinkat, readlinkat, getcwd,
// CopyFile (copy_file_range), setsockopt, getpeername, the terminal ioctls,
// TimeZone and OnNinep.
//
// How: each call is one system call or a bounded loop of them, and every
// path that opens a descriptor closes it on every error path. EINTR from the
// pool's cancel returns Canceled, which the pool repeats when its own job was
// not cancelled (MapError); CopyFile's loops retry it themselves. Errors map
// through MapError, plus FailCall's rows: EOPNOTSUPP (95) and ENOSYS (38) are
// Unsupported.

import std.core
import std.raw

const AtSymlinkNoFollowCall: uint = 0x100
const AtNoAutomountCall: uint = 0x800
// TYPE | MODE | MTIME | SIZE.
const StatxModeMask: uint = 0x1 | 0x2 | 0x40 | 0x200
const LockSh: uint = 1
const LockEx: uint = 2
const LockNb: uint = 4
const LockUn: uint = 8
const ProtRead: uint = 1

// O_WRONLY | O_CREAT: CopyFile's destination, truncated only after the
// same-file check.
const OWrCreate: uint = 0o1 | 0o100
const UtimeOmit: i64 = 0x3FFF_FFFE
const Tcgets: uint = 0x5401
const Tiocgwinsz: uint = 0x5413
const IpProtoTcp: uint = 6
const TcpNoDelay: uint = 1
const TcpKeepIdle: uint = 4
const SoKeepAlive: uint = 9
const SoSndBuf: uint = 7
const SoRcvBuf: uint = 8
const SoLinger: uint = 13
const SoReuseAddrCall: uint = 2
const SolSocketCall: uint = 1
const IpProtoIp: uint = 0
const IpMtuDiscover: uint = 10
const IpProtoIpv6: uint = 41
const Ipv6MtuDiscover: uint = 23
const IpPmtudiscDo: i32 = 2
// CopyChunk is one copy_file_range's request: 16 MiB, so a cancel is seen
// within one chunk's time (Go asks 1 GiB, with no cancel to see); 64
// system calls per GiB cost nothing next to the copy.
const CopyChunk: uint = 0x100_0000
// CopyBuffer is the fallback loop's stack buffer.
const CopyBuffer: int = 65536

// FailCall is Fail for the calls of this file: EOPNOTSUPP and ENOSYS are
// Unsupported (a file system or kernel without the call), the rest
// MapError's.
FailCall(errno: i64): OsResult {
    if errno == 95 || errno == 38 {
        return OsResult(value: 0, err: core.SysErr.Unsupported, os: errno)
    }
    return Fail(errno)
}

// ResultCall is Result with FailCall's rows.
ResultCall(r: int): OsResult {
    return if IsErr(r) { FailCall(i64(-r)) } else { Ok(i64(r)) }
}

// CBytes copies the path bytes p NUL-terminated into buf: false when it
// does not fit or holds a NUL (InvalidPath). The bytes need not be UTF-8: a
// Linux name is any bytes but NUL and '/' in a component.
CBytes(p: []u8, buf: mut []u8): bool {
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
    unsafe "a system call that passes no memory, only values and handles" {
        return ResultCall(Syscall(Nrs().lseek, h, Trunc[uint](off), uint(whence), 0, 0, 0))
    }
}

/// FileLock takes, or tries once to take, the whole-file advisory lock of
/// h's open description (flock: shared or exclusive; LOCK_NB with once):
/// value 1 when held, 0 when once is set and another description holds a
/// conflicting lock (EWOULDBLOCK). A flock lock and an fcntl lock do not
/// see each other on Linux; std uses only flock for File.Lock. Without
/// once the call blocks the thread until the lock is free: std.task calls it
/// with once and waits on its own clock instead, so a cancel ends the wait.
/// O(1): one system call besides the wait; allocates nothing.
export FileLock(h: uint, exclusive: bool, once: bool): OsResult {
    var how := if exclusive { LockEx } else { LockSh }
    if once {
        how = how | LockNb
    }
    unsafe "a system call that passes no memory, only values and handles" {
        r := Syscall(Nrs().flock, h, how, 0, 0, 0, 0)
        if r == -11 {
            return Ok(0)
        }
        return if IsErr(r) { FailCall(i64(-r)) } else { Ok(1) }
    }
}

/// FileUnlock drops h's flock lock (LOCK_UN); unlocking a description that
/// holds none succeeds.
/// O(1): one system call; allocates nothing.
export FileUnlock(h: uint): OsResult {
    unsafe "a system call that passes no memory, only values and handles" {
        return ResultCall(Syscall(Nrs().flock, h, LockUn, 0, 0, 0, 0))
    }
}

/// MapFile maps len bytes of h from off, read-only and private:
/// off is rounded down to the page, so w.c is the mapping's base and w.d
/// its length (UnmapFile's arguments), and value is the address of byte off.
/// len must be above 0 (std maps 0 bytes with no call); a negative off is
/// Other with no call. A read past the file's end inside the mapping
/// faults (SIGBUS), which the runtime reports as a panic (std.rt's mapping
/// table).
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
    unsafe "a system call that maps new pages; nothing of the caller's is passed" {
        r := Syscall(Nrs().mmap, 0, Trunc[uint](total), ProtRead, MapPrivate, h, Trunc[uint](base))
        if IsErr(r) {
            return FailCall(i64(-r))
        }
        w.c = i64(r)
        w.d = total
        return Ok(i64(r) + delta)
    }
}

/// UnmapFile releases a mapping MapFile made: base and len as it gave them in
/// w.c and w.d.
/// O(1): one system call; allocates nothing.
export UnmapFile(base: uint, len: int): OsResult {
    unsafe "the caller's mapping, not used after this" {
        return ResultCall(Syscall(Nrs().munmap, base, Trunc[uint](len), 0, 0, 0, 0))
    }
}

/// StatEx fills w for path p: a the kind (core.IoKind*), b the size (0 for
/// a directory or a link), c the modification time in nanoseconds since
/// the epoch, d the mode bits (st_mode & 0o7777). nofollow reports a final
/// symbolic link itself (AT_SYMLINK_NOFOLLOW, fs.StatLink).
/// O(1): one system call; allocates nothing (the path is copied into a
/// stack buffer).
export StatEx(p: []u8, nofollow: bool, w: mut core.IoWords): OsResult {
    var c: [4096]u8 = [0; 4096]
    if !CBytes(p, mut c[..]) {
        return FailWith(core.SysErr.InvalidPath)
    }
    var b: [32]u64 = [0; 32]
    flags := if nofollow { AtSymlinkNoFollowCall | AtNoAutomountCall } else { AtNoAutomountCall }
    unsafe "system call; b holds 256 bytes, c is NUL-terminated" {
        q := raw.PtrCast[u64, u8](raw.DataMut(mut b[..]))
        r := Syscall(Nrs().statx, AtFdcwd, raw.Addr(raw.Data(c[..])), flags, StatxModeMask, raw.Addr(q), 0)
        if IsErr(r) {
            return FailCall(i64(-r))
        }
        m := u32(Get16(q, 28))
        t := m & 0o170000
        kind := if t == 0o100000 {
            core.IoKindFile
        } else if t == 0o040000 {
            core.IoKindDir
        } else if t == 0o120000 {
            core.IoKindLink
        } else {
            core.IoKindOther
        }
        w.a = i64(kind)
        w.b = if kind == core.IoKindFile || kind == core.IoKindOther { Trunc[i64](Get64(q, 40)) } else { 0 }
        w.c = StatNanos(Trunc[i64](Get64(q, 112)), i64(Get32(q, 120)))
        w.d = i64(m & 0o7777)
    }
    return Ok(0)
}

/// SetModified sets p's modification time to nanos since the epoch,
/// leaving its access time (utimensat with UTIME_OMIT), following a link.
/// O(1): one system call; allocates nothing (the path is copied into a
/// stack buffer).
export SetModified(p: []u8, nanos: i64): OsResult {
    var c: [4096]u8 = [0; 4096]
    if !CBytes(p, mut c[..]) {
        return FailWith(core.SysErr.InvalidPath)
    }
    var sec := nanos / 1_000_000_000
    var ns := nanos % 1_000_000_000
    if ns < 0 {
        sec -= 1
        ns += 1_000_000_000
    }
    var ts: [4]i64 = [0, UtimeOmit, sec, ns]
    unsafe "system call; ts is two struct timespec, c is NUL-terminated" {
        return ResultCall(Syscall(Nrs().utimensat, AtFdcwd, raw.Addr(raw.Data(c[..])),
            raw.Addr(raw.DataMut(mut ts[..])), 0, 0, 0))
    }
}

/// SetMode sets p's mode bits (fchmodat; mode & 0o7777), following a link.
/// O(1): one system call; allocates nothing (the path is copied into a
/// stack buffer).
export SetMode(p: []u8, mode: u32): OsResult {
    var c: [4096]u8 = [0; 4096]
    if !CBytes(p, mut c[..]) {
        return FailWith(core.SysErr.InvalidPath)
    }
    unsafe "system call; c is NUL-terminated" {
        return ResultCall(Syscall(Nrs().fchmodat, AtFdcwd, raw.Addr(raw.Data(c[..])), uint(mode & 0o7777), 0, 0, 0))
    }
}

/// Symlink makes link a symbolic link to target (symlinkat); target is
/// stored as given, not checked. An existing link path is Exists.
/// O(1): one system call; allocates nothing (both paths are copied into
/// stack buffers).
export Symlink(target: []u8, link: []u8): OsResult {
    var t: [4096]u8 = [0; 4096]
    var l: [4096]u8 = [0; 4096]
    if !CBytes(target, mut t[..]) || !CBytes(link, mut l[..]) {
        return FailWith(core.SysErr.InvalidPath)
    }
    unsafe "system call; t and l are NUL-terminated" {
        return ResultCall(Syscall(Nrs().symlinkat, raw.Addr(raw.Data(t[..])), AtFdcwd, raw.Addr(raw.Data(l[..])), 0, 0,
            0))
    }
}

/// ReadLink writes the target of the symbolic link p into out: value is
/// the bytes written, and value == out.Len() means the target may be cut
/// (the caller calls again with more room). Not a link is the OS's EINVAL,
/// Other.
/// O(1): one system call; allocates nothing (the path is copied into a
/// stack buffer).
export ReadLink(p: []u8, out: mut []u8): OsResult {
    var c: [4096]u8 = [0; 4096]
    if !CBytes(p, mut c[..]) {
        return FailWith(core.SysErr.InvalidPath)
    }
    unsafe "system call; c is NUL-terminated, out holds out.Len() bytes" {
        return ResultCall(Syscall(Nrs().readlinkat, AtFdcwd, raw.Addr(raw.Data(c[..])), raw.Addr(raw.DataMut(out)),
            Trunc[uint](out.Len()), 0, 0))
    }
}

/// Cwd writes the working directory into out, without its NUL: value is
/// the bytes written, out.Len() when it did not fit (ERANGE), so the caller
/// calls again with more room.
/// O(1): one system call; allocates nothing.
export Cwd(out: mut []u8): OsResult {
    if out.Len() == 0 {
        return Ok(0)
    }
    unsafe "system call; out holds out.Len() bytes" {
        r := Syscall(Nrs().getcwd, raw.Addr(raw.DataMut(out)), Trunc[uint](out.Len()), 0, 0, 0, 0)
        if r == -34 {
            return Ok(i64(out.Len()))
        }
        if IsErr(r) {
            return FailCall(i64(-r))
        }
        // The kernel's count includes the NUL.
        return Ok(i64(r) - 1)
    }
}

/// Absolute writes p as an absolute path into out: p itself when it starts
/// with '/', else the working directory, '/' and p, with no dot segment or
/// link resolved (Go's filepath.Abs cleans; std.fs does). value as Cwd's.
/// O(1): one system call at most; allocates nothing.
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

/// CopyFile copies the bytes of src into dst, which it creates or
/// truncates (mode 0o666 less the umask): copy_file_range in 1 GiB
/// requests, falling back to a read and write loop through a 64 KiB stack
/// buffer when the kernel or the file systems refuse it (EXDEV, ENOSYS,
/// EINVAL, EOPNOTSUPP), as Go's io.Copy over two os.Files does. value is
/// the bytes copied. A copy of a file onto itself (one device and inode,
/// by any path or link) is Denied before anything is truncated, as
/// Windows' CopyFileExW answers (a sharing violation): dst is opened
/// without O_TRUNC, both are fstat'ed, and only then is dst truncated.
/// A source that is not a regular file is refused on its open
/// descriptor before dst is opened: IsDirectory (os 21) for a directory,
/// Other with os 22 (EINVAL) for anything else. Both descriptors are
/// closed on every path. Short counts are
/// retried. stop, when not null, is the caller's cancel word (std.task's
/// pool job): read before each open and each chunk, and at an EINTR
/// (the pool's interrupt ends a wait in an open, a FIFO's with no writer,
/// or a read); once it is 1 the copy ends with Canceled, dst holding an
/// unknown prefix of src. An EINTR with stop 0 is retried.
/// O(size / 16 MiB) system calls (O(size / 64 KiB) on the fallback);
/// allocates nothing.
export CopyFile(src: []u8, dst: []u8, stop: *u32): OsResult {
    var a: [4096]u8 = [0; 4096]
    var b: [4096]u8 = [0; 4096]
    if !CBytes(src, mut a[..]) || !CBytes(dst, mut b[..]) {
        return FailWith(core.SysErr.InvalidPath)
    }
    var from: uint = 0
    var to: uint = 0
    unsafe "system calls; a and b are NUL-terminated" {
        var r := 0
        for {
            if Stopped(stop) {
                return FailWith(core.SysErr.Canceled)
            }
            r = Syscall(Nrs().openat, AtFdcwd, raw.Addr(raw.Data(a[..])), ORdOnly | OCloExec, 0, 0, 0)
            if r != -4 {
                break
            }
        }
        if IsErr(r) {
            return FailCall(i64(-r))
        }
        from = Trunc[uint](r)
        // Only a regular source is copied, checked on the open descriptor
        // before dst is opened, so a source swapped for a directory or a
        // FIFO after std.fs's stat never creates or truncates dst.
        var ss: [18]u64 = [0; 18]
        if !Stat(from, mut ss) {
            _ = Close(from)
            return FailWith(core.SysErr.Other)
        }
        kind := Get32(raw.PtrCast[u64, u8](raw.Data(ss[..])), StatMode()) & 0o170000
        if kind != 0o100000 {
            _ = Close(from)
            return if kind == 0o040000 { OsResult(value: 0, err: core.SysErr.IsDirectory, os: 21) } else { OsResult(value: 0, err: core.SysErr.Other, os: 22) }
        }
        var r2 := 0
        for {
            if Stopped(stop) {
                _ = Close(from)
                return FailWith(core.SysErr.Canceled)
            }
            r2 = Syscall(Nrs().openat, AtFdcwd, raw.Addr(raw.Data(b[..])), OWrCreate | OCloExec, 0o666, 0, 0)
            if r2 != -4 {
                break
            }
        }
        if IsErr(r2) {
            _ = Close(from)
            return FailCall(i64(-r2))
        }
        to = Trunc[uint](r2)
    }
    // The same file is refused before the truncate empties it.
    var ia: [2]u64 = [0; 2]
    var sb: [18]u64 = [0; 18]
    var bad := FileId(from, mut ia)
    if bad.err == core.SysErr.None && !Stat(to, mut sb) {
        bad = FailWith(core.SysErr.Other)
    }
    if bad.err == core.SysErr.None && ia[0] == sb[0] && ia[1] == sb[1] {
        bad = FailWith(core.SysErr.Denied)
    }
    // Only a regular file is truncated: O_TRUNC left a device or a FIFO
    // (/dev/null) as it was, and ftruncate refuses one (EINVAL).
    var regular := false
    unsafe "sb is the struct stat fstat filled" {
        regular = (Get32(raw.PtrCast[u64, u8](raw.Data(sb[..])), StatMode()) & 0o170000) == 0o100000
    }
    if bad.err == core.SysErr.None && regular {
        unsafe "system call on the descriptor opened above" {
            for {
                t := Syscall(Nrs().ftruncate, to, 0, 0, 0, 0, 0)
                if t == -4 {
                    continue
                }
                if IsErr(t) {
                    bad = FailCall(i64(-t))
                }
                break
            }
        }
    }
    if bad.err != core.SysErr.None {
        _ = Close(from)
        _ = Close(to)
        return bad
    }
    res := CopyFds(from, to, stop)
    _ = Close(from)
    c := Close(to)
    if res.err == core.SysErr.None && c.err != core.SysErr.None {
        return c
    }
    return res
}

// Stopped reports whether the cancel word stop (null: none) is set.
// Edge: reads std.task's CancelJob store; a load that misses it is
// followed by the next step's load, or by the interrupt's EINTR.
Stopped(stop: *u32): bool {
    if raw.IsNull(stop) {
        return false
    }
    unsafe "stop is the live pool job's cancel word" {
        return (*raw.PtrCast[u32, raw.Atomic[u32]](stop)).Load() != 0
    }
}

// CopyFds copies from's bytes from its offset to its end into to, ending
// with Canceled once stop is set (CopyFile).
CopyFds(from: uint, to: uint, stop: *u32): OsResult {
    var total: i64 = 0
    var fast := true
    for fast {
        if Stopped(stop) {
            return FailWith(core.SysErr.Canceled)
        }
        var r := 0
        unsafe "a system call with null offsets: both descriptors' own offsets move" {
            r = Syscall(Nrs().copy_file_range, from, 0, to, 0, CopyChunk, 0)
        }
        if r == -4 {
            continue
        }
        if r == -18 || r == -38 || r == -22 || r == -95 || r == -1 {
            fast = false
        } else if IsErr(r) {
            return FailCall(i64(-r))
        } else if r == 0 {
            return Ok(total)
        } else {
            total += i64(r)
        }
    }
    var buf: [65536]u8 = [0; 65536]
    for {
        if Stopped(stop) {
            return FailWith(core.SysErr.Canceled)
        }
        var n := 0
        unsafe "system call; buf holds CopyBuffer bytes" {
            n = Syscall(Nrs().read, from, raw.Addr(raw.DataMut(mut buf[..])), Trunc[uint](CopyBuffer), 0, 0, 0)
        }
        if n == -4 {
            continue
        }
        if IsErr(n) {
            return FailCall(i64(-n))
        }
        if n == 0 {
            return Ok(total)
        }
        var done := 0
        for done < n {
            var m := 0
            unsafe "system call; buf[done..n] is filled" {
                m = Syscall(Nrs().write, to, raw.Addr(raw.Offset(raw.Data(buf[..]), done)), Trunc[uint](n - done), 0, 0, 0)
            }
            if m == -4 {
                continue
            }
            if IsErr(m) {
                return FailCall(i64(-m))
            }
            done += m
        }
        total += i64(n)
    }
}

/// SocketOption sets one option of the socket h (core.IoSock*): NoDelay
/// (b 1 on), KeepAlive (b 1 on; c > 0 the idle seconds before the first
/// probe, TCP_KEEPIDLE), ReuseAddr, RecvBuffer and SendBuffer (b bytes,
/// which the kernel doubles), Linger (b 1 on, c seconds: on with 0 makes
/// close send a reset and leave no TIME_WAIT). An unknown option, or a
/// value outside an i32, is Other with no call.
/// O(1): one or two system calls; allocates nothing.
export SocketOption(h: uint, opt: i64, b: i64, c: i64): OsResult {
    if b < 0 || b > 0x7FFF_FFFF || c < 0 || c > 0x7FFF_FFFF {
        return FailWith(core.SysErr.Other)
    }
    var v: [2]i32 = [Trunc[i32](b), 0]
    var level := SolSocketCall
    var name: uint = 0
    var size: uint = 4
    if opt == core.IoSockNoDelay {
        level = IpProtoTcp
        name = TcpNoDelay
    } else if opt == core.IoSockKeepAlive {
        name = SoKeepAlive
    } else if opt == core.IoSockReuseAddr {
        name = SoReuseAddrCall
    } else if opt == core.IoSockRecvBuffer {
        name = SoRcvBuf
    } else if opt == core.IoSockSendBuffer {
        name = SoSndBuf
    } else if opt == core.IoSockLinger {
        name = SoLinger
        v[1] = Trunc[i32](c)
        size = 8
    } else if opt == core.IoSockDontFrag {
        level = IpProtoIp
        name = IpMtuDiscover
        v[0] = if b != 0 { IpPmtudiscDo } else { 0 }
    } else if opt == core.IoSockDontFragV6 {
        level = IpProtoIpv6
        name = Ipv6MtuDiscover
        v[0] = if b != 0 { IpPmtudiscDo } else { 0 }
    } else {
        return FailWith(core.SysErr.Other)
    }
    unsafe "system call; v holds size bytes" {
        r := Syscall(Nrs().setsockopt, h, level, name, raw.Addr(raw.DataMut(mut v[..])), size, 0)
        if IsErr(r) {
            return FailCall(i64(-r))
        }
        if opt == core.IoSockKeepAlive && b == 1 && c > 0 {
            var idle: [1]i32 = [Trunc[i32](c)]
            r2 := Syscall(Nrs().setsockopt, h, IpProtoTcp, TcpKeepIdle, raw.Addr(raw.DataMut(mut idle[..])), 4, 0)
            if IsErr(r2) {
                return FailCall(i64(-r2))
            }
        }
    }
    return Ok(0)
}

/// SocketOptionGet reads one option of the socket h (core.IoSock*) with
/// getsockopt: value NoDelay's or KeepAlive's 0 or 1 (and for KeepAlive,
/// os the TCP_KEEPIDLE seconds), ReuseAddr's 0 or 1, RecvBuffer's or
/// SendBuffer's bytes (the kernel's doubled value), Linger's 1 or 0 (os
/// its seconds). An unknown option is Other with no call.
/// O(1): one or two system calls; allocates nothing.
export SocketOptionGet(h: uint, opt: i64): OsResult {
    var level := SolSocketCall
    var name: uint = 0
    if opt == core.IoSockNoDelay {
        level = IpProtoTcp
        name = TcpNoDelay
    } else if opt == core.IoSockKeepAlive {
        name = SoKeepAlive
    } else if opt == core.IoSockReuseAddr {
        name = SoReuseAddrCall
    } else if opt == core.IoSockRecvBuffer {
        name = SoRcvBuf
    } else if opt == core.IoSockSendBuffer {
        name = SoSndBuf
    } else if opt == core.IoSockLinger {
        name = SoLinger
    } else if opt == core.IoSockDontFrag {
        level = IpProtoIp
        name = IpMtuDiscover
    } else if opt == core.IoSockDontFragV6 {
        level = IpProtoIpv6
        name = Ipv6MtuDiscover
    } else {
        return FailWith(core.SysErr.Other)
    }
    var v: [2]i32 = [0, 0]
    var n: [1]u32 = [8]
    unsafe "system call; v holds 8 bytes and n their count" {
        r := Syscall(Nrs().getsockopt, h, level, name, raw.Addr(raw.DataMut(mut v[..])), raw.Addr(raw.DataMut(mut n[..])),
            0)
        if IsErr(r) {
            return FailCall(i64(-r))
        }
        if opt == core.IoSockLinger {
            return OsResult(value: if v[0] != 0 { 1 } else { 0 }, err: core.SysErr.None, os: i64(v[1]))
        }
        value: i64 = if opt == core.IoSockRecvBuffer || opt == core.IoSockSendBuffer { i64(v[0]) } else if v[0] != 0 { 1 } else { 0 }
        var idle: i64 = 0
        if opt == core.IoSockKeepAlive {
            var w: [1]i32 = [0]
            var m: [1]u32 = [4]
            r2 := Syscall(Nrs().getsockopt, h, IpProtoTcp, TcpKeepIdle, raw.Addr(raw.DataMut(mut w[..])),
                raw.Addr(raw.DataMut(mut m[..])), 0)
            if !IsErr(r2) {
                idle = i64(w[0])
            }
        }
        return OsResult(value: value, err: core.SysErr.None, os: idle)
    }
}

/// SocketPeer is getpeername: the address h is connected to. A socket
/// that is not connected is the OS's ENOTCONN, Other.
/// O(1): one system call; allocates nothing.
export SocketPeer(h: uint, a: mut SockAddr): OsResult {
    var sa: [4]u64 = [0; 4]
    var n: [1]u32 = [32]
    unsafe "system call; sa holds 32 bytes" {
        r := Syscall(Nrs().getpeername, h, raw.Addr(raw.DataMut(mut sa[..])), raw.Addr(raw.DataMut(mut n[..])), 0, 0,
            0)
        if IsErr(r) {
            return SockFail(i64(-r))
        }
        if !DecodeAddr(raw.PtrCast[u64, u8](raw.Data(sa[..])), a) {
            return FailWith(core.SysErr.InvalidAddress)
        }
    }
    return Ok(0)
}

/// IsTerminal reports whether h is a terminal: TCGETS succeeds on it (as
/// isatty does).
/// O(1): one system call; allocates nothing.
export IsTerminal(h: uint): bool {
    var t: [8]u64 = [0; 8]
    unsafe "system call; t holds a struct termios (60 bytes)" {
        return !IsErr(Syscall(Nrs().ioctl, h, Tcgets, raw.Addr(raw.DataMut(mut t[..])), 0, 0, 0))
    }
}

/// TermSize reads the terminal h's size (TIOCGWINSZ): w.a columns, w.b
/// rows. Not a terminal is the OS's ENOTTY, Other.
/// O(1): one system call; allocates nothing.
export TermSize(h: uint, w: mut core.IoWords): OsResult {
    var ws: [1]u64 = [0]
    unsafe "system call; ws holds a struct winsize (8 bytes)" {
        q := raw.PtrCast[u64, u8](raw.DataMut(mut ws[..]))
        r := Syscall(Nrs().ioctl, h, Tiocgwinsz, raw.Addr(q), 0, 0, 0)
        if IsErr(r) {
            return FailCall(i64(-r))
        }
        w.a = i64(Get16(q, 2))
        w.b = i64(Get16(q, 0))
    }
    return Ok(0)
}

/// TimeZone is Windows' time-zone record: Unsupported on Linux, whose zone
/// std.time reads from TZ and /etc/localtime.
/// O(1); allocates nothing.
export TimeZone(year: i64, out: mut []u8): OsResult {
    return FailWith(core.SysErr.Unsupported)
}

// V9fsMagic is statfs's f_type of a 9p mount (V9FS_MAGIC, include/uapi/
// linux/magic.h); on WSL2's 6.6.87.2 kernel, `stat -f -c %t` on a drive
// mount such as /mnt/c prints 1021997.
const V9fsMagic: u64 = 0x0102_1997

/// OnNinep reports whether the descriptor h is a file on a 9p mount (WSL's
/// drive bridge): fstatfs's f_type, the first 8 bytes of struct statfs on
/// x86_64 and aarch64. std.task keeps such a file off io_uring:
/// a ring read of it goes to io-wq, whose deferred free can run the last
/// fput under exit_group's SIGKILL and hang 9p's flush. A
/// failed fstatfs answers false.
/// O(1): one system call; allocates nothing.
export OnNinep(h: uint): bool {
    var b: [15]u64 = [0; 15]
    unsafe "system call; b holds a struct statfs (120 bytes)" {
        r := Syscall(Nrs().fstatfs, h, raw.Addr(raw.DataMut(mut b[..])), 0, 0, 0, 0)
        return !IsErr(r) && b[0] == V9fsMagic
    }
}
