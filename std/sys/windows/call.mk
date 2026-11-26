// Why: std.fs, std.net, std.term and std.time reach the OS through the
// IoTable's call entry (std.core's IoOp constants); this file is the Windows
// side of those calls.
//
// What: Seek, FileLock, FileUnlock, MapFile, UnmapFile, StatEx, SetModified,
// SetMode, Symlink, ReadLink, Cwd, Absolute, CopyFile, SocketOption,
// SocketOptionGet, SocketPeer, IsTerminal, TermSize, TimeZone and OnNinep.
//
// How: SetFilePointerEx, LockFileEx, CreateFileMappingW and MapViewOfFile,
// the file information classes, SetFileAttributesW, CreateSymbolicLinkW and
// FSCTL_GET_REPARSE_POINT, GetFullPathNameW, CopyFileExW, Winsock, the
// console buffer and GetDynamicTimeZoneInformation (no registry read). Every
// import is kernel32's, or ws2_32's through the Winsock table. Each path that
// opens a handle closes it on every error path. Errors map through MapError,
// plus FailCall's rows: ERROR_NOT_SUPPORTED (50) and
// ERROR_CALL_NOT_IMPLEMENTED (120) are Unsupported.

import std.core
import std.raw

const FileWriteAttributes: u32 = 0x100
const FileAttributeReadOnly: u32 = 1
const FileBasicInfoClass: u32 = 0
const FileStandardInfoClass: u32 = 1
const FileAttributeTagInfoClass: u32 = 9
const PageReadOnly: u32 = 2
const FileMapRead: u32 = 4
const MapGranularity: i64 = 65536
const LockNotHeld: u32 = 158
const SymlinkDirectory: u32 = 1
const SymlinkUnprivileged: u32 = 2
const FsctlGetReparsePoint: u32 = 0x0009_00A8
const ReparseBytes: int = 16384
const NoReparsePoint: u32 = 4390
const MaxPathUnits: int = 32768
const TcpNoDelayWin: i32 = 1
const TcpKeepIdleWin: i32 = 3
const SoKeepAliveWin: i32 = 8
const SoLingerWin: i32 = 0x80
const SoSndBufWin: i32 = 0x1001
const SoRcvBufWin: i32 = 0x1002
const IpProtoIpWin: i32 = 0
const IpDontFragmentWin: i32 = 14
const IpProtoIpv6Win: i32 = 41
const Ipv6DontFragWin: i32 = 14
const DynamicZoneBytes: int = 432
const ZoneBytes: int = 172

// FailCall is Fail for the calls of this file: ERROR_NOT_SUPPORTED and
// ERROR_CALL_NOT_IMPLEMENTED are Unsupported, the rest MapError's.
FailCall(e: u32): OsResult {
    if e == 50 || e == 120 {
        return OsResult(value: 0, err: core.SysErr.Unsupported, os: i64(e))
    }
    return Fail(e)
}

// LastCall is LastError with FailCall's rows.
LastCall(): OsResult {
    unsafe "kernel32" {
        return FailCall(GetLastError())
    }
}

// PathOf is the call's path bytes as the str WidePath converts: WTF-8,
// which Wide checks byte by byte, so bytes that are not WTF-8 are
// InvalidPath and never read past their end.
PathOf(p: []u8): str {
    unsafe "the bytes live as long as the call; Wide validates them as WTF-8 and refuses a NUL" {
        return raw.Str(raw.Data(p), p.Len())
    }
}

// Narrowed writes the units UTF-16 units at p as UTF-8 into out; the bytes
// written, or out.Len() when they do not fit (the caller's "call again").
// An unpaired surrogate is its WTF-8 (NameScalar).
Narrowed(p: *u16, units: int, out: mut []u8): int {
    var i := 0
    var n := 0
    for i < units {
        (c, w) := NameScalar(p, units, i)
        need := if c < 0x80 { 1 } else if c < 0x800 { 2 } else if c < 0x10000 { 3 } else { 4 }
        if n + need > out.Len() {
            return out.Len()
        }
        n = PutScalar(out, n, c)
        i += w
    }
    return n
}

/// Seek moves h's file pointer (SetFilePointerEx): whence 0 from the
/// start, 1 from the current pointer, 2 from the end; value is the new
/// position. A whence above 2 is Other with no call. A handle on the
/// completion port has no pointer the runtime uses (std.task keeps its
/// position), so std.task calls this only for a synchronous handle.
/// O(1): one system call; allocates nothing.
export Seek(h: uint, off: i64, whence: u32): OsResult {
    if whence > 2 {
        return FailWith(core.SysErr.Other)
    }
    var pos: i64 = 0
    unsafe "kernel32; pos is in this frame" {
        if SetFilePointerEx(h, off, raw.OfMut(mut pos), whence) == 0 {
            return LastCall()
        }
    }
    return Ok(pos)
}

// LockCall runs LockFileEx or UnlockFileEx over the whole file with an
// OVERLAPPED whose event has its low bit set, so an overlapped handle on
// the completion port posts no packet (as PosIo's calls); a pending call
// is waited for. The answer: 0 done, else the error code.
LockCall(h: uint, flags: u32, lock: bool): u32 {
    l := LockLen(0)
    unsafe "kernel32; ov is an OVERLAPPED in this frame until the call is done" {
        t := EvTake()
        if t.ev == 0 {
            return GetLastError()
        }
        var ov := Overlapped(0)
        ov[3] = u64(t.ev) | 1
        ovp := raw.PtrCast[u64, u8](raw.DataMut(mut ov[..]))
        ok := if lock { LockFileEx(h, flags, 0, l.low, l.high, ovp) } else { UnlockFileEx(h, 0, l.low, l.high, ovp) }
        var e: u32 = 0
        if ok == 0 {
            e = GetLastError()
            if e == WsaIoPending {
                var done: u32 = 0
                e = if GetOverlappedResult(h, ovp, raw.OfMut(mut done), 1) == 0 { GetLastError() } else { 0 }
            }
        }
        EvGive(t.slot, t.ev)
        return e
    }
}

/// FileLock takes, or tries once to take, the whole-file lock of h
/// (LockFileEx over every byte, shared or exclusive; fail-immediately with
/// once): value 1 when held, 0 when once is set and another handle holds a
/// conflicting lock (ERROR_LOCK_VIOLATION). Windows locks are mandatory
/// for other handles' reads and writes of the file, and a handle that
/// locks twice conflicts with itself. Without once the thread waits
/// for the lock: std.task calls it with once and waits on its own clock.
/// O(1) system calls besides the wait; allocates nothing.
export FileLock(h: uint, exclusive: bool, once: bool): OsResult {
    var flags: u32 = if exclusive { LockExclusive } else { 0 }
    if once {
        flags = flags | LockFailImmediately
    }
    e := LockCall(h, flags, true)
    if e == 33 {
        return Ok(0)
    }
    return if e != 0 { FailCall(e) } else { Ok(1) }
}

/// FileUnlock drops h's whole-file lock (UnlockFileEx); a handle that holds
/// none succeeds, as on Linux (ERROR_NOT_LOCKED).
/// O(1): one system call; allocates nothing.
export FileUnlock(h: uint): OsResult {
    e := LockCall(h, 0, false)
    return if e != 0 && e != LockNotHeld { FailCall(e) } else { Ok(0) }
}

/// MapFile maps len bytes of h from off, read-only and private:
/// a read-only section (CreateFileMappingW, PAGE_READONLY) and a view of it
/// (MapViewOfFile, FILE_MAP_READ) from off rounded down to the 64 KiB
/// allocation granularity; the section handle is closed at once (the view
/// keeps it). w.c is the view's base and w.d its length (UnmapFile's
/// arguments); value is the address of byte off. len must be above 0; a
/// negative off is Other with no call. Windows refuses to shorten a file
/// with a view open, so the view cannot fault that way.
/// O(1) system calls; allocates nothing but the view.
export MapFile(h: uint, off: i64, len: int, w: mut core.IoWords): OsResult {
    if off < 0 || len <= 0 {
        return FailWith(core.SysErr.Other)
    }
    base := off - (off % MapGranularity)
    delta := off - base
    total := i64(len) + delta
    if total < i64(len) {
        return FailWith(core.SysErr.Other)
    }
    unsafe "kernel32; the section handle is closed on every path" {
        m := CreateFileMappingW(h, raw.Null[u8](), PageReadOnly, 0, 0, raw.Null[u16]())
        if m == 0 {
            return LastCall()
        }
        ub := Trunc[u64](base)
        v := MapViewOfFile(m, FileMapRead, Trunc[u32](ub >> 32), Trunc[u32](ub), Trunc[uint](total))
        e := GetLastError()
        _ = CloseHandle(m)
        if raw.IsNull(v) {
            return FailCall(e)
        }
        w.c = Trunc[i64](raw.Addr(v))
        w.d = total
        return Ok(Trunc[i64](raw.Addr(v)) + delta)
    }
}

/// UnmapFile releases a view MapFile made (UnmapViewOfFile); len is not
/// needed on Windows.
/// O(1): one system call; allocates nothing.
export UnmapFile(base: uint, len: int): OsResult {
    unsafe "the caller's view, not used after this" {
        if UnmapViewOfFile(raw.FromAddr[u8](base)) == 0 {
            return LastCall()
        }
    }
    return Ok(0)
}

// ModeOf is the mode bits Windows has: 0o444 for a read-only file, else
// 0o666, and 0o111 more on a directory (Go's os.Stat).
ModeOf(attr: u32): i64 {
    var m: i64 = if (attr & FileAttributeReadOnly) != 0 { 0o444 } else { 0o666 }
    if (attr & AttributeDirectory) != 0 {
        m = m | 0o111
    }
    return m
}

/// StatEx fills w for path p: a the kind (core.IoKind*), b the size (0 for
/// a directory or a link), c the modification time in nanoseconds since
/// the epoch, d the mode bits (ModeOf). nofollow opens a final reparse
/// point itself (FILE_FLAG_OPEN_REPARSE_POINT), and a symbolic link or
/// junction is then IoKindLink (fs.StatLink). First, for a path that
/// AttributesStep allows, one GetFileAttributesExW, which opens no handle,
/// as StatPath (dir.mk) and Go's os.Stat do (StatExAttributes): for an
/// entry that is no reparse point its answer is the handle path's (nofollow
/// changes nothing for it), and for a missing file or path too; an entry
/// the caller may list but not open (System Volume Information or an NTFS
/// metafile, for a standard user) has its facts there, where the handle
/// path is Denied, as with Go's os.Stat. Anything else takes the handle
/// path (StatExOpen).
/// O(1) system calls: one for an ordinary file or directory or a missing
/// one, else one more than the handle path's (as many for a path
/// AttributesStep refuses); allocates nothing (the path converts in a
/// stack buffer, or in pages for a long path).
export StatEx(p: []u8, nofollow: bool, w: mut core.IoWords): OsResult {
    var small: [1024]u16 = [0; 1024]
    wp := WidePath(PathOf(p), mut small[..])
    if wp.err != core.SysErr.None {
        return FailWith(wp.err)
    }
    var r := Ok(0)
    if !AttributesStep(p) {
        r = StatExOpen(wp.p, nofollow, w)
    } else if a := StatExAttributes(wp.p, w) {
        r = a
    } else {
        r = StatExOpen(wp.p, nofollow, w)
    }
    FreePath(wp)
    return r
}

// AttributesStep: p may take StatExAttributes's GetFileAttributesExW, read
// from the caller's bytes (WidePath gives a long path a \\?\ prefix of its
// own). The step opens a named pipe or a mailslot as a client, taking a
// pipe's listening instance (with one left, the handle path's own open then
// finds it busy, 231), and answers a mailslot as a file, so it is refused
// for the ways those are named: a path that starts with two separators,
// each / or \ (UNC, \\server\pipe\x included; \\.\ device paths; \\?\
// paths), and one that starts with the NT prefix \??\ (any separators). Of
// those, \\?\X:\ (backslashes, a drive letter and a colon) is allowed: it
// is a lettered disk path in its long form, and must answer as X:\ does.
// A known limit: a pipe reached through a directory link to \\.\pipe\
// still takes the step; its zero-time rule gives the handle path's
// answer, IoKindOther, but the step has taken an instance, so a pipe with
// one instance left is pipe busy (231) by that path, where the handle
// path alone answers Other. No check of the
// path's text can see the link.
AttributesStep(p: []u8): bool {
    n := p.Len()
    if n >= 4 && PathSep(p[0]) && p[1] == 0x3F && p[2] == 0x3F && PathSep(p[3]) {
        return false
    }
    if n >= 2 && PathSep(p[0]) && PathSep(p[1]) {
        return n >= 7 && p[0] == 0x5C && p[1] == 0x5C && p[2] == 0x3F && p[3] == 0x5C && DriveLetter(p[4]) && p[5] == 0x3A && p[6] == 0x5C
    }
    return true
}

// PathSep: c is / or \.
PathSep(c: u8): bool {
    return c == 0x2F || c == 0x5C
}

// DriveLetter: c is an ASCII letter.
DriveLetter(c: u8): bool {
    return (c >= 0x41 && c <= 0x5A) || (c >= 0x61 && c <= 0x7A)
}

// StatExAttributes is the first step of StatEx and StatPath, for a path
// AttributesStep allows: one GetFileAttributesExW on the NUL-terminated
// path p. When it succeeds on an entry that is no reparse point, its
// WIN32_FILE_ATTRIBUTE_DATA (attributes at 0, ftLastWriteTime at 20,
// nFileSizeHigh at 28, nFileSizeLow at 32) holds what StatExHandle reads
// from a handle: the same attributes, so the same kind and mode, the
// last-write time and the end of file. It needs only the right to list the
// entry's directory, so it also answers for an entry the caller may not
// open, which the handle path finds Denied. A missing file or path (2, 3)
// is that error, the one CreateFileW gives. none leaves p to the handle
// path: a symbolic link or junction, which must be followed or opened
// itself; a device, which only a handle's GetFileType tells from a file
// (NUL, CON and a volume fail here, and a named pipe answers with every
// time 0, so a zero last-write time is left to the handle path); a file the
// system holds (pagefile.sys, 32); and every other error. The root of a FAT
// volume keeps no times of its own: by X:\ or \\?\X:\ this step answers
// 1980-01-01 local time, as Go's os.Stat does, and by \\?\Volume{...}\ or
// \??\Volume{...}\, which AttributesStep refuses, the handle path answers
// the earliest time (both seen on a FAT32 volume named by \??\Volume{...}\;
// a lettered one was not tried). Besides
// those, each answer is the handle path's word for word on every path
// TestFs_StatRoutesAgree probes. w is written only when the step
// answers.
StatExAttributes(p: *u16, w: mut core.IoWords): ?OsResult {
    var fa: [5]u64 = [0; 5]
    unsafe "kernel32; p is NUL-terminated; fa holds 40 bytes, the 36 of WIN32_FILE_ATTRIBUTE_DATA" {
        d := raw.PtrCast[u64, u8](raw.DataMut(mut fa[..]))
        if GetFileAttributesExW(p, 0, d) == 0 {
            e := GetLastError()
            if e == 2 || e == 3 {
                return some(FailCall(e))
            }
            return none
        }
        attr := Get32(d, 0)
        ft := Get64(d, 20)
        // A named pipe answers with no reparse bit and every time 0, and
        // only its handle says FILE_TYPE_PIPE (IoKindOther), so a zero
        // last-write time takes the handle path too; a file whose time is
        // 1601-01-01 exactly pays the handle path's calls and gets the
        // same answer.
        if (attr & AttributeReparse) != 0 || ft == 0 {
            return none
        }
        if (attr & AttributeDirectory) != 0 {
            w.a = i64(core.IoKindDir)
            w.b = 0
        } else {
            w.a = i64(core.IoKindFile)
            w.b = Trunc[i64]((u64(Get32(d, 28)) << 32) | u64(Get32(d, 32)))
        }
        w.c = UnixNanos(Trunc[i64](ft))
        w.d = ModeOf(attr)
    }
    return some(Ok(0))
}

// StatExOpen is StatEx's handle path: CreateFileW with FILE_READ_ATTRIBUTES
// on p (FILE_FLAG_OPEN_REPARSE_POINT for nofollow), then StatExHandle.
StatExOpen(p: *u16, nofollow: bool, w: mut core.IoWords): OsResult {
    var flags := FlagBackupSemantics
    if nofollow {
        flags = flags | FlagOpenReparsePoint
    }
    var r := Ok(0)
    unsafe "kernel32; p is NUL-terminated, the handle is closed below" {
        h := CreateFileW(p, FileReadAttributes, ShareAll, raw.Null[u8](), OpenExisting, flags, 0)
        if h == InvalidHandle {
            r = LastCall()
        } else {
            r = StatExHandle(h, w)
            _ = CloseHandle(h)
        }
    }
    return r
}

// StatExHandle fills w from FILE_BASIC_INFO (LastWriteTime at 16,
// FileAttributes at 32), FILE_STANDARD_INFO (EndOfFile at 8) and
// FILE_ATTRIBUTE_TAG_INFO (ReparseTag at 4).
StatExHandle(h: uint, w: mut core.IoWords): OsResult {
    var basic: [5]u64 = [0; 5]
    var stdi: [3]u64 = [0; 3]
    var tag: [1]u64 = [0]
    unsafe "kernel32; basic holds 40 bytes, stdi 24, tag 8" {
        b := raw.PtrCast[u64, u8](raw.DataMut(mut basic[..]))
        s := raw.PtrCast[u64, u8](raw.DataMut(mut stdi[..]))
        t := raw.PtrCast[u64, u8](raw.DataMut(mut tag[..]))
        if GetFileInformationByHandleEx(h, FileBasicInfoClass, b, 40) == 0 {
            return LastCall()
        }
        if GetFileInformationByHandleEx(h, FileStandardInfoClass, s, 24) == 0 {
            return LastCall()
        }
        attr := Get32(b, 32)
        var kind := core.IoKindFile
        if ((attr & AttributeReparse) != 0 && GetFileInformationByHandleEx(h, FileAttributeTagInfoClass, t, 8) != 0
            && LinkTag(attr, Get32(t, 4))) {
            kind = core.IoKindLink
        } else if (attr & AttributeDirectory) != 0 {
            kind = core.IoKindDir
        } else if GetFileType(h) != 1 {
            kind = core.IoKindOther
        }
        w.a = i64(kind)
        w.b = if kind == core.IoKindFile || kind == core.IoKindOther { Trunc[i64](Get64(s, 8)) } else { 0 }
        w.c = UnixNanos(Trunc[i64](Get64(b, 16)))
        w.d = ModeOf(attr)
    }
    return Ok(0)
}

/// SetModified sets p's last-write time to nanos since the epoch, the
/// other times kept (FILE_BASIC_INFO with only LastWriteTime set; 0 keeps
/// a field), following a link. A time at or before 1601-01-01 is Other.
/// O(1) system calls; allocates nothing (the path converts in a stack
/// buffer, or in pages for a long path).
export SetModified(p: []u8, nanos: i64): OsResult {
    var ticks := nanos / 100
    if nanos % 100 < 0 {
        ticks -= 1
    }
    ft := ticks +% UnixEpoch
    if ft <= 0 {
        return FailWith(core.SysErr.Other)
    }
    var small: [1024]u16 = [0; 1024]
    wp := WidePath(PathOf(p), mut small[..])
    if wp.err != core.SysErr.None {
        return FailWith(wp.err)
    }
    var r := Ok(0)
    var basic: [5]u64 = [0, 0, Trunc[u64](ft), 0, 0]
    unsafe "kernel32; wp.p is NUL-terminated, basic is a FILE_BASIC_INFO, the handle is closed below" {
        h := CreateFileW(wp.p, FileWriteAttributes, ShareAll, raw.Null[u8](), OpenExisting, FlagBackupSemantics, 0)
        if h == InvalidHandle {
            r = LastCall()
        } else {
            b := raw.PtrCast[u64, u8](raw.DataMut(mut basic[..]))
            if SetFileInformationByHandle(h, FileBasicInfoClass, b, 40) == 0 {
                r = LastCall()
            }
            _ = CloseHandle(h)
        }
    }
    FreePath(wp)
    return r
}

/// SetMode sets p's read-only attribute from the owner's write bit
/// (0o200 clear: read-only), keeping its other attributes, as Go's
/// os.Chmod on Windows; the other bits have no Windows meaning.
/// O(1) system calls; allocates nothing (the path converts in a stack
/// buffer, or in pages for a long path).
export SetMode(p: []u8, mode: u32): OsResult {
    var small: [1024]u16 = [0; 1024]
    wp := WidePath(PathOf(p), mut small[..])
    if wp.err != core.SysErr.None {
        return FailWith(wp.err)
    }
    var r := Ok(0)
    unsafe "kernel32; wp.p is NUL-terminated" {
        a := GetFileAttributesW(wp.p)
        if a == InvalidAttributes {
            r = LastCall()
        } else {
            var b := if (mode & 0o200) == 0 { a | FileAttributeReadOnly } else { a & ~FileAttributeReadOnly }
            if b == 0 {
                b = AttributeNormal
            }
            if b != a && SetFileAttributesW(wp.p, b) == 0 {
                r = LastCall()
            }
        }
    }
    FreePath(wp)
    return r
}

/// Symlink makes link a symbolic link to target (CreateSymbolicLinkW with
/// SYMBOLIC_LINK_FLAG_ALLOW_UNPRIVILEGED_CREATE, which Developer Mode
/// honours); a directory target (relative to link's directory when
/// relative, as Go's os.Symlink judges it) makes a directory link, and '/'
/// in target becomes '\'. Without the privilege (ERROR_PRIVILEGE_NOT_HELD,
/// 1314) it is Unsupported.
/// Paths of fewer than MaxUnits bytes, as WidePath takes them, are taken,
/// as Go's os.Symlink takes them: link goes through WidePath (the
/// \\?\ form from LongPath units on); an absolute target (a drive and a
/// separator, or two separators) does too, and is stored as that full
/// path; so does a drive-relative one (`C:x`), which the system resolves
/// against that drive's working directory and stores as the full path
/// either way; any other target is stored as written, never resolved
/// against the working directory, so a relative link stays relative at
/// any length. The link's reparse buffer (16 KiB) holds the target twice,
/// so a relative target of 4,092 units or more, or a full path of 4,088
/// or more (a UNC one of 4,086), fails with Os(code: 4392),
/// ERROR_INVALID_REPARSE_DATA (measured
/// by the Mk authors on Windows 11). CreateSymbolicLinkW returns a BOOLEAN,
/// so only the low byte answers (kernel32.mk declares it i32, and on that
/// failure the rest of the register is not zero).
/// O(1) system calls; allocates nothing for short paths (stack buffers);
/// a long path or a directory test's join past 1 KiB converts in pages
/// mapped for the call and given back.
export Symlink(target: []u8, link: []u8): OsResult {
    var ts: [1024]u16 = [0; 1024]
    var ls: [1024]u16 = [0; 1024]
    t := if FullForm(target) || DriveRelative(target) { WidePath(PathOf(target), mut ts[..]) } else { WideGrown(PathOf(target), mut ts[..]) }
    if t.err != core.SysErr.None {
        return FailWith(t.err)
    }
    l := WidePath(PathOf(link), mut ls[..])
    if l.err != core.SysErr.None {
        FreePath(t)
        return FailWith(l.err)
    }
    unsafe "t.p is NUL-terminated UTF-16 in ts or in t's pages, which are writable" {
        var i := 0
        for raw.Read(raw.Offset(t.p, i)) != 0 {
            if raw.Read(raw.Offset(t.p, i)) == 47 {
                raw.Write(raw.Offset(t.p, i), 92)
            }
            i += 1
        }
    }
    var r := Ok(0)
    var flags := SymlinkUnprivileged
    if LinkIsDir(target, link) {
        flags = flags | SymlinkDirectory
    }
    unsafe "kernel32; l.p and t.p are NUL-terminated UTF-16" {
        if (CreateSymbolicLinkW(l.p, t.p, flags) & 0xFF) == 0 {
            e := GetLastError()
            r = if e == 1314 { OsResult(value: 0, err: core.SysErr.Unsupported, os: i64(e)) } else { FailCall(e) }
        }
    }
    FreePath(l)
    FreePath(t)
    return r
}

// FullForm: the bytes p start with a drive, a colon and a separator, or
// with two separators (UNC, \\?\ or \\.\), either separator '/' or '\':
// Go's filepath.IsAbs on Windows, the test os.Symlink makes before it
// gives a target the \\?\ form (ShortFull's absolute test, on bytes).
// O(1); allocates nothing.
FullForm(p: []u8): bool {
    if p.Len() < 2 {
        return false
    }
    s0 := p[0] == 47 || p[0] == 92
    s1 := p[1] == 47 || p[1] == 92
    if s0 && s1 {
        return true
    }
    return p.Len() >= 3 && !s0 && p[1] == 58 && (p[2] == 47 || p[2] == 92)
}

// DriveRelative: the bytes p start with a drive and a colon not followed
// by a separator (`C:x`, `C:`), a path the system resolves against that
// drive's working directory, as ShortFull's drive test reads one. Such a
// target goes through WidePath, so a short one is passed as written and
// a long one as the \\?\ full path it resolves to, which the system
// stores in either case (as written, a long one failed with Os(code: 87)
// once it resolved to 260 units). O(1);
// allocates nothing.
DriveRelative(p: []u8): bool {
    return p.Len() >= 2 && p[0] != 47 && p[0] != 92 && p[0] != 0 && p[1] == 58 && (p.Len() == 2 || (p[2] != 47 && p[2] != 92))
}

// LinkIsDir: target names a directory as link's directory sees it, much
// as Go's os.Symlink judges it: a target that starts with a separator or
// has a drive is itself, any other is joined to link's directory (link up
// to its last separator). This only chooses the directory flag, and Go
// differs in two cases: a root-relative target (\x) of a link on another
// drive is judged here on the working directory's drive (Go puts the
// link's volume first), and a drive-relative link (C:l) joins nothing
// here (Go joins to C:.). IsDir does not follow a target that is itself
// a link. The join goes through WidePath, so a join of any
// length is judged; one that cannot be made or converted is not a
// directory, as Go's Stat error is. O(len) plus at most two system calls;
// maps pages only for a join past 1 KiB, or a long one.
LinkIsDir(target: []u8, link: []u8): bool {
    tl := target.Len()
    var cut := -1
    if !(tl > 0 && (target[0] == 47 || target[0] == 92 || (tl > 1 && target[1] == 58))) {
        for i in 0..link.Len() {
            if link[i] == 47 || link[i] == 92 {
                cut = i
            }
        }
    }
    var ws: [1024]u16 = [0; 1024]
    if cut < 0 {
        return WideIsDir(PathOf(target), mut ws[..])
    }
    // The join, link[0..cut + 1] then target, in js or in pages.
    n := cut + 1 + tl
    var js: [1024]u8 = [0; 1024]
    var jmap := raw.Null[u8]()
    var jbytes := 0
    var j := raw.DataMut(mut js[..])
    unsafe "the pages mapped here hold the join's n bytes" {
        if n > js.Len() {
            jbytes = PageRound(n)
            jmap = Reserve(jbytes, 4096)
            if raw.IsNull(jmap) || !Commit(jmap, jbytes) {
                FreeMap(jmap, jbytes)
                return false
            }
            j = jmap
        }
        for i in 0..cut + 1 {
            raw.Write(raw.Offset(j, i), link[i])
        }
        for i in 0..tl {
            raw.Write(raw.Offset(j, cut + 1 + i), target[i])
        }
        // The join is WTF-8 when its two parts are: they meet at an ASCII
        // separator, and Wide checks every byte again.
        dir := WideIsDir(raw.Str(j, n), mut ws[..])
        FreeMap(jmap, jbytes)
        return dir
    }
}

// WideIsDir: path, through WidePath, names a directory (false when it
// cannot be converted). One or two system calls; maps pages only for a
// long path, given back.
WideIsDir(path: str, small: mut []u16): bool {
    w := WidePath(path, small)
    if w.err != core.SysErr.None {
        return false
    }
    dir := IsDir(w.p)
    FreePath(w)
    return dir
}

// WideGrown converts path to NUL-terminated UTF-16 for a call that must see
// the text as written (GetFullPathNameW's input, a link's relative
// target): in small when it fits, else in pages mapped for it, given back
// by FreePath. Unlike WidePath it never makes the \\?\ form. A NUL byte,
// bytes that are not WTF-8 (Wide) or MaxUnits bytes or more is
// InvalidPath; a mapping that fails is Other. O(len), no system call
// unless small is too short; the pages are written, so the caller may
// rewrite units in place (Symlink's '/').
WideGrown(path: str, small: mut []u16): WPath {
    var r := WPath(p: raw.Null[u16](), map: raw.Null[u8](), bytes: 0, err: core.SysErr.InvalidPath)
    b := path.AsBytes()
    for x in b {
        if x == 0 {
            return r
        }
    }
    if b.Len() >= MaxUnits {
        return r
    }
    n := Wide(path, small)
    if n == WideBad {
        return r
    }
    if n > 0 {
        r.p = raw.DataMut(small)
        r.err = core.SysErr.None
        return r
    }
    unsafe "the pages mapped here hold a unit per byte and the NUL" {
        bytes := PageRound((b.Len() + 1) * 2)
        m := Reserve(bytes, 4096)
        if raw.IsNull(m) || !Commit(m, bytes) {
            FreeMap(m, bytes)
            r.err = core.SysErr.Other
            return r
        }
        p := raw.PtrCast[u8, u16](m)
        // Checked: the bytes are WTF-8 (above) with no NUL, and the pages
        // hold a unit per byte and the NUL.
        if Wide(path, raw.SliceMut(p, bytes / 2)) <= 0 {
            FreeMap(m, bytes)
            return r
        }
        r.p = p
        r.map = m
        r.bytes = bytes
        r.err = core.SysErr.None
        return r
    }
}

/// ReadLink writes the target of the symbolic link or junction p into out
/// (FSCTL_GET_REPARSE_POINT on p opened as a reparse point): its
/// substitute name, normalized as Go's os.Readlink does (ReparseTarget),
/// as UTF-8.
/// value is the bytes written, and value == out.Len() means it may be cut.
/// Not a link is Other (ERROR_NOT_A_REPARSE_POINT; Linux's EINVAL).
/// O(1) system calls; allocates nothing (a 16 KiB stack buffer holds the
/// reparse data).
export ReadLink(p: []u8, out: mut []u8): OsResult {
    var small: [1024]u16 = [0; 1024]
    wp := WidePath(PathOf(p), mut small[..])
    if wp.err != core.SysErr.None {
        return FailWith(wp.err)
    }
    var data: [2048]u64 = [0; 2048]
    var r := Ok(0)
    unsafe "kernel32; wp.p is NUL-terminated, data holds ReparseBytes bytes, the handle is closed below" {
        h := CreateFileW(wp.p, FileReadAttributes, ShareAll, raw.Null[u8](), OpenExisting,
            FlagBackupSemantics | FlagOpenReparsePoint, 0)
        if h == InvalidHandle {
            r = LastCall()
        } else {
            var got: u32 = 0
            d := raw.PtrCast[u64, u8](raw.DataMut(mut data[..]))
            if DeviceIoControl(h, FsctlGetReparsePoint, raw.Null[u8](), 0, d, Trunc[u32](ReparseBytes),
                raw.OfMut(mut got), raw.Null[u8]()) == 0 {
                r = LastCall()
            } else {
                r = ReparseTarget(d, Trunc[int](got), out)
            }
            _ = CloseHandle(h)
        }
    }
    FreePath(wp)
    return r
}

// ReparseTarget reads a REPARSE_DATA_BUFFER of n bytes at d: a symbolic
// link's names start at 20 (its flags at 16), a junction's at 16; each
// name is (offset, length) in bytes into the path buffer. The answer is
// the substitute name, the path the I/O manager follows, never the print
// name, which the link's maker chooses freely (a junction can show any
// decoy); the print name is read only when the substitute is empty. The
// substitute is normalized as Go's os.Readlink does (normalizeLinkPath):
// a relative symbolic link as it is; \??\C:\x as C:\x; \??\UNC\s\x as
// \\s\x; any other \??\ form (a volume GUID path) as \\?\ and the rest.
ReparseTarget(d: *u8, n: int, out: mut []u8): OsResult {
    tag := Get32(d, 0)
    var start := 0
    var relative := false
    if tag == TagSymlink {
        start = 20
        relative = n >= 20 && (Get32(d, 16) & SymlinkRelative) != 0
    } else if tag == TagMountPoint {
        start = 16
    } else {
        return OsResult(value: 0, err: core.SysErr.Other, os: i64(NoReparsePoint))
    }
    if n < start {
        return FailWith(core.SysErr.Other)
    }
    var off := int(Get16(d, 8))
    var len := int(Get16(d, 10))
    if len == 0 {
        off = int(Get16(d, 12))
        len = int(Get16(d, 14))
    }
    if start + off + len > n || len % 2 != 0 {
        return FailWith(core.SysErr.Other)
    }
    unsafe "the name lies inside the n bytes the call filled" {
        var q := raw.PtrCast[u8, u16](raw.Offset(d, start + off))
        var units := len / 2
        var lead := 0
        if !relative && UnitsAre(q, units, "\\??\\") {
            q = raw.Offset(q, 4)
            units -= 4
            if units >= 2 && raw.Read(raw.Offset(q, 1)) == 58 {
                // \??\C:\x: a drive path, as it is.
                lead = 0
            } else if UnitsAre(q, units, "UNC\\") {
                // \??\UNC\server\share: the UNC path \\server\share; the
                // name keeps the backslash after UNC, so one more is led.
                q = raw.Offset(q, 3)
                units -= 3
                lead = 1
            } else {
                // \??\Volume{...}\: the Win32 form \\?\Volume{...}\.
                lead = 4
            }
        }
        if lead >= out.Len() {
            return Ok(i64(out.Len()))
        }
        for k in 0..lead {
            out[k] = if k == 2 { 63 } else { 92 }
        }
        return Ok(i64(lead + Narrowed(q, units, mut out[lead..])))
    }
}

// UnitsAre reports whether the first units of q are the ASCII text t.
UnitsAre(q: *u16, units: int, t: str): bool {
    b := t.AsBytes()
    if b.Len() > units {
        return false
    }
    unsafe "q holds units units, and b.Len() <= units" {
        for k in 0..b.Len() {
            if raw.Read(raw.Offset(q, k)) != u16(b[k]) {
                return false
            }
        }
    }
    return true
}

/// Cwd writes the working directory into out as UTF-8: value is the bytes
/// written, out.Len() when it did not fit, so the caller calls again with
/// more room.
/// O(1): one system call; allocates nothing (a 64 KiB stack buffer holds
/// the longest path Windows has).
export Cwd(out: mut []u8): OsResult {
    var buf: [32768]u16 = [0; 32768]
    unsafe "kernel32; buf holds MaxPathUnits units" {
        n := GetCurrentDirectoryW(Trunc[u32](MaxPathUnits), raw.DataMut(mut buf[..]))
        if n == 0 {
            return LastCall()
        }
        if int(n) >= MaxPathUnits {
            return FailWith(core.SysErr.Other)
        }
        return Ok(i64(Narrowed(raw.Data(buf[..]), int(n), out)))
    }
}

/// Absolute writes p as an absolute path into out (GetFullPathNameW: the
/// working directory joined, '.' and '..' resolved, no link followed).
/// value as Cwd's. p of fewer than MaxUnits bytes is taken as written
/// (WideGrown: never the \\?\ form, which GetFullPathNameW would keep in
/// its answer).
/// O(1): one system call; allocates nothing for p under 1,024 units (stack
/// buffers), else converts p in pages mapped for the call and given back.
export Absolute(p: []u8, out: mut []u8): OsResult {
    var small: [1024]u16 = [0; 1024]
    w := WideGrown(PathOf(p), mut small[..])
    if w.err != core.SysErr.None {
        return FailWith(w.err)
    }
    var r := Ok(0)
    var buf: [32768]u16 = [0; 32768]
    unsafe "kernel32; w.p is NUL-terminated, buf holds MaxPathUnits units" {
        n := GetFullPathNameW(w.p, Trunc[u32](MaxPathUnits), raw.DataMut(mut buf[..]), raw.Null[u8]())
        r = if n == 0 { LastCall() } else if int(n) >= MaxPathUnits { FailWith(core.SysErr.Other) } else { Ok(i64(Narrowed(raw.Data(buf[..]), int(n), out))) }
    }
    FreePath(w)
    return r
}

/// CopyFile copies src into dst, replacing it (CopyFileExW, which also
/// copies the attributes and times, as the OS's own copy does); value is
/// the bytes copied, read back from dst's size. A copy onto itself fails
/// with a sharing violation (Denied) before dst is touched: CopyFileExW
/// opens the source denying writers (RT8-Call-CopySelf). stop,
/// when not null, is the caller's cancel word (std.task's pool job),
/// passed as CopyFileExW's pbCancel, which the copy reads between its
/// chunks: once it is 1 the copy ends with Canceled (ERROR_REQUEST_ABORTED,
/// and CopyFileExW deletes the partial dst: an empty prefix of src;
/// as for any failure, fs.Copy's doc says what is left when another
/// handle holds dst);
/// the pool's interrupt (CancelSynchronousIo) ends a wait in an open.
/// O(size) in the kernel, O(1) system calls; allocates nothing (the paths
/// convert in stack buffers, or in pages for long paths).
export CopyFile(src: []u8, dst: []u8, stop: *u32): OsResult {
    var sa: [1024]u16 = [0; 1024]
    var sb: [1024]u16 = [0; 1024]
    a := WidePath(PathOf(src), mut sa[..])
    if a.err != core.SysErr.None {
        return FailWith(a.err)
    }
    b := WidePath(PathOf(dst), mut sb[..])
    if b.err != core.SysErr.None {
        FreePath(a)
        return FailWith(b.err)
    }
    var r := Ok(0)
    var fa: [5]u64 = [0; 5]
    unsafe "kernel32; a.p and b.p are NUL-terminated, fa holds 40 bytes" {
        if CopyFileExW(a.p, b.p, raw.Null[u8](), raw.Null[u8](), raw.PtrCast[u32, i32](stop), 0) == 0 {
            e := GetLastError()
            // 1235 ERROR_REQUEST_ABORTED (pbCancel), 995
            // ERROR_OPERATION_ABORTED (CancelSynchronousIo): Canceled once
            // the word is set; a stray abort is the OS's error.
            r = if (e == 1235 || e == 995) && CopyStopped(stop) { FailWith(core.SysErr.Canceled) } else { FailCall(e) }
        } else {
            d := raw.PtrCast[u64, u8](raw.DataMut(mut fa[..]))
            if GetFileAttributesExW(b.p, 0, d) == 0 {
                r = LastCall()
            } else {
                r = Ok(Trunc[i64]((u64(Get32(d, 28)) << 32) | u64(Get32(d, 32))))
            }
        }
    }
    FreePath(b)
    FreePath(a)
    return r
}

// CopyStopped reports whether the cancel word stop (null: none) is set.
// Edge: reads std.task's CancelJob store.
CopyStopped(stop: *u32): bool {
    if raw.IsNull(stop) {
        return false
    }
    unsafe "stop is the live pool job's cancel word" {
        return (*raw.PtrCast[u32, raw.Atomic[u32]](stop)).Load() != 0
    }
}

/// SocketOption sets one option of the socket h (core.IoSock*) through
/// Winsock's setsockopt: NoDelay, KeepAlive (c > 0: TCP_KEEPIDLE seconds,
/// Windows 10 1709 and later), RecvBuffer, SendBuffer, Linger (struct
/// linger's two u16s: on with 0 closes by reset). ReuseAddr succeeds and
/// changes nothing: Windows binds a listener over TIME_WAIT already, and
/// its SO_REUSEADDR would let another socket take a bound port. An unknown
/// option, or a value outside its field, is Other with no call.
/// O(1): one or two calls; allocates nothing.
export SocketOption(h: uint, opt: i64, b: i64, c: i64): OsResult {
    if b < 0 || b > 0x7FFF_FFFF || c < 0 || c > 0x7FFF_FFFF {
        return FailWith(core.SysErr.Other)
    }
    if !WinsockReady() {
        return FailWith(core.SysErr.Other)
    }
    if opt == core.IoSockReuseAddr {
        return Ok(0)
    }
    var v: [1]u32 = [Trunc[u32](b)]
    var level := SolSocket
    var name: i32 = 0
    if opt == core.IoSockNoDelay {
        level = IpProtoTcp
        name = TcpNoDelayWin
    } else if opt == core.IoSockKeepAlive {
        name = SoKeepAliveWin
    } else if opt == core.IoSockRecvBuffer {
        name = SoRcvBufWin
    } else if opt == core.IoSockSendBuffer {
        name = SoSndBufWin
    } else if opt == core.IoSockLinger {
        if c > 0xFFFF {
            return FailWith(core.SysErr.Other)
        }
        name = SoLingerWin
        v[0] = (Trunc[u32](c) << 16) | (Trunc[u32](b) & 0xFFFF)
    } else if opt == core.IoSockDontFrag {
        level = IpProtoIpWin
        name = IpDontFragmentWin
    } else if opt == core.IoSockDontFragV6 {
        level = IpProtoIpv6Win
        name = Ipv6DontFragWin
    } else {
        return FailWith(core.SysErr.Other)
    }
    unsafe "Winsock; v holds 4 bytes" {
        if SetSockOpt(h, level, name, raw.PtrCast[u32, u8](raw.DataMut(mut v[..])), 4) != 0 {
            return WsaFail()
        }
        if opt == core.IoSockKeepAlive && b == 1 && c > 0 {
            var idle: [1]u32 = [Trunc[u32](c)]
            if SetSockOpt(h, IpProtoTcp, TcpKeepIdleWin, raw.PtrCast[u32, u8](raw.DataMut(mut idle[..])), 4) != 0 {
                return WsaFail()
            }
        }
    }
    return Ok(0)
}

/// SocketOptionGet reads one option of the socket h (core.IoSock*)
/// through Winsock's getsockopt: value NoDelay's or KeepAlive's 0 or 1
/// (and for KeepAlive, os the TCP_KEEPIDLE seconds when Windows answers
/// it, else 0), RecvBuffer's or SendBuffer's bytes, Linger's 1 or 0 (os
/// its seconds). ReuseAddr, or an unknown option, is Other with no call.
/// O(1): one or two calls; allocates nothing.
export SocketOptionGet(h: uint, opt: i64): OsResult {
    if !WinsockReady() {
        return FailWith(core.SysErr.Other)
    }
    var level := SolSocket
    var name: i32 = 0
    if opt == core.IoSockNoDelay {
        level = IpProtoTcp
        name = TcpNoDelayWin
    } else if opt == core.IoSockKeepAlive {
        name = SoKeepAliveWin
    } else if opt == core.IoSockRecvBuffer {
        name = SoRcvBufWin
    } else if opt == core.IoSockSendBuffer {
        name = SoSndBufWin
    } else if opt == core.IoSockLinger {
        name = SoLingerWin
    } else if opt == core.IoSockDontFrag {
        level = IpProtoIpWin
        name = IpDontFragmentWin
    } else if opt == core.IoSockDontFragV6 {
        level = IpProtoIpv6Win
        name = Ipv6DontFragWin
    } else {
        return FailWith(core.SysErr.Other)
    }
    var v: [1]u32 = [0]
    var n: i32 = 4
    unsafe "Winsock; v holds 4 bytes" {
        if GetSockOpt(h, level, name, raw.PtrCast[u32, u8](raw.DataMut(mut v[..])), raw.OfMut(mut n)) != 0 {
            return WsaFail()
        }
        if opt == core.IoSockLinger {
            return OsResult(value: if (v[0] & 0xFFFF) != 0 { 1 } else { 0 }, err: core.SysErr.None, os: i64(v[0] >> 16))
        }
        // A BOOL option may answer one byte (Windows writes a BOOLEAN for
        // TCP_NODELAY on some versions), so only the low byte counts.
        value: i64 = if opt == core.IoSockRecvBuffer || opt == core.IoSockSendBuffer { i64(v[0]) } else if (v[0] & 0xFF) != 0 { 1 } else { 0 }
        var idle: i64 = 0
        if opt == core.IoSockKeepAlive {
            var w: [1]u32 = [0]
            var m: i32 = 4
            if GetSockOpt(h, IpProtoTcp, TcpKeepIdleWin, raw.PtrCast[u32, u8](raw.DataMut(mut w[..])), raw.OfMut(mut m)) == 0 {
                idle = i64(w[0])
            }
        }
        return OsResult(value: value, err: core.SysErr.None, os: idle)
    }
}

/// SocketPeer is getpeername: the address h is connected to. A socket
/// that is not connected is WSAENOTCONN, Other.
/// O(1): one call; allocates nothing.
export SocketPeer(h: uint, a: mut SockAddr): OsResult {
    if !WinsockReady() {
        return FailWith(core.SysErr.Other)
    }
    var sa: [4]u64 = [0; 4]
    var n: i32 = 32
    unsafe "sa holds 32 bytes" {
        if GetPeerNameRaw(h, raw.PtrCast[u64, u8](raw.DataMut(mut sa[..])), raw.OfMut(mut n)) != 0 {
            return WsaFail()
        }
        if !DecodeAddr(raw.PtrCast[u64, u8](raw.Data(sa[..])), a) {
            return FailWith(core.SysErr.InvalidAddress)
        }
    }
    return Ok(0)
}

/// IsTerminal reports whether h is a console (GetConsoleMode succeeds on
/// it, as Go's x/term IsTerminal judges it).
/// O(1): one system call; allocates nothing.
export IsTerminal(h: uint): bool {
    var mode: u32 = 0
    unsafe "kernel32; mode is in this frame" {
        return GetConsoleMode(h, raw.OfMut(mut mode)) != 0
    }
}

/// TermSize reads the console h's window size
/// (GetConsoleScreenBufferInfo's srWindow, at 10): w.a columns, w.b rows.
/// Not a console is the OS's error.
/// O(1): one system call; allocates nothing.
export TermSize(h: uint, w: mut core.IoWords): OsResult {
    var info: [3]u64 = [0; 3]
    unsafe "kernel32; info holds a CONSOLE_SCREEN_BUFFER_INFO (22 bytes)" {
        q := raw.PtrCast[u64, u8](raw.DataMut(mut info[..]))
        if GetConsoleScreenBufferInfo(h, q) == 0 {
            return LastCall()
        }
        left := i64(Trunc[i16](Get16(q, 10)))
        top := i64(Trunc[i16](Get16(q, 12)))
        right := i64(Trunc[i16](Get16(q, 14)))
        bottom := i64(Trunc[i16](Get16(q, 16)))
        w.a = right - left + 1
        w.b = bottom - top + 1
    }
    return Ok(0)
}

/// TimeZone writes the OS's time-zone record into out, with no registry
/// read: for year 0 the zone in force (GetDynamicTimeZoneInformation,
/// DYNAMIC_TIME_ZONE_INFORMATION, 432 bytes, its key name at 172), else
/// that zone's rules for the year (GetTimeZoneInformationForYear,
/// TIME_ZONE_INFORMATION, 172 bytes). value is the record's length. An out
/// shorter than the record, or a year outside 1601 to 30827, is Other with
/// no call.
/// O(1): one system call; allocates nothing.
export TimeZone(year: i64, out: mut []u8): OsResult {
    if year == 0 {
        if out.Len() < DynamicZoneBytes {
            return FailWith(core.SysErr.Other)
        }
        unsafe "kernel32; out holds the record" {
            if GetDynamicTimeZoneInformation(raw.DataMut(out)) == 0xFFFF_FFFF {
                return LastCall()
            }
        }
        return Ok(i64(DynamicZoneBytes))
    }
    if year < 1601 || year > 30827 || out.Len() < ZoneBytes {
        return FailWith(core.SysErr.Other)
    }
    unsafe "kernel32; out holds the record" {
        if GetTimeZoneInformationForYear(Trunc[u16](year), raw.Null[u8](), raw.DataMut(out)) == 0 {
            return LastCall()
        }
    }
    return Ok(i64(ZoneBytes))
}

/// OnNinep is Linux's 9p test: no Windows file is on io_uring, so
/// false. O(1); allocates nothing.
export OnNinep(h: uint): bool {
    return false
}
