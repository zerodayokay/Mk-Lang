// Why: std.fs, std.net, std.time, std.proc, std.rt, std.task and std.sim must
// call the OS through an Io without seeing its private fields; these
// std-internal functions are that door.
//
// What: MakeIo and IoFlags; the task depth and scope-chain cancellation state
// (DepthHook, ChainWords, ChainInit, ChainState); masking (IoMasked,
// IoMaskAs); one wrapper per IoTable entry (IoOpen, IoRead, IoWrite, IoWait,
// IoSleep, IoEnter, IoLeave and the rest); IoCall with the IoOp constants;
// and the word wait and wake used by std.atomic (IoWaitWord, IoWakeWord,
// WakeListen).
//
// How: each Io* function is one call of the IoTable entry through IoTab, the
// one place the table pointer is dereferenced. Operations that only some
// programs use go through the single call entry with an IoOp code, so a
// program links an operation's code only when it names it.

import std.raw

/// Makes an Io over the table t with the flags word flags. t must point at a
/// global table, because an Io holds no loan. O(1); it allocates nothing.
export MakeIo(t: *IoTable, flags: uint): Io {
    return Io(table: t, flags: flags)
}

/// Returns io's flags word; bit 0 is set on a NoCancel Io. O(1); it allocates
/// nothing.
export IoFlags(io: Io): uint {
    return io.flags
}

/// The position of the mask depth in an Io's flags word. The flags hold: bit
/// 0 the NoCancel mask; bits 1 to 31 a Sim's or Tape's token; bits 32 to 47
/// the scope depth at which the mask was taken (saturating at MaskDepthMax);
/// bits 48 to 63 a Sim's or Tape's slot.
export const MaskDepthShift: uint = 32
/// The largest mask depth the flags hold; a deeper NoCancel records this
/// value.
export const MaskDepthMax: uint = 0xFFFF

/// The functions std.task registers so std.core can read the running task's
/// scope depth and mute its scheduler hooks without importing std.task.
/// Reading them never touches an Io's table, so no table entry is linked. In
/// a process with no scheduler, every depth is 0.
export struct DepthHook {
    export depth: fn(): u32
    /// Mutes the running task's scheduler hooks; returns true when it did,
    /// and that value is passed to unmute.
    export mute: fn(): bool
    export unmute: fn(bool)
}

// DepthAt is the address of std.task's DepthHook, 0 before it starts.
var DepthAt: raw.Atomic[u64] = raw.Atomic[u64].New(0)

/// Registers h, a global. std.task calls it once, before its first task runs.
/// O(1); it allocates nothing.
export DepthInit(h: *DepthHook) {
    // Edge: stored before any task exists (as NativeInit's), so every load
    // on a task (TaskDepth) reads it.
    DepthAt.Store(u64(raw.Addr(h)))
}

/// Mutes the running task's scheduler hooks while it holds a process stream's
/// lock. A task of a Sim that waits for the lock, or writes the stream out on
/// the blocking pool, then keeps the Sim's turn, so no other task of the Sim
/// can wait for the turn while this one holds the lock. False off a hooked
/// task or before std.task starts. O(1): one indirect call; it allocates
/// nothing.
HookMuteHere(): bool {
    // Edge: as DepthInit's store.
    p := DepthAt.Load()
    if p == 0 {
        return false
    }
    unsafe "p is the address of std.task's global DepthHook (DepthInit)" {
        f := (*raw.FromAddr[DepthHook](uint(p))).mute
        return f()
    }
}

/// Ends HookMuteHere's mute when on is true. O(1).
HookUnmuteHere(on: bool) {
    if !on {
        return
    }
    unsafe "std.task's DepthHook was registered: HookMuteHere returned true" {
        f := (*raw.FromAddr[DepthHook](uint(DepthAt.Load()))).unmute
        f(on)
    }
}

/// Returns the running task's scope depth, or 0 off a task or before std.task
/// starts. O(1); it allocates nothing.
export TaskDepth(): u32 {
    // Edge: as DepthInit's store.
    p := DepthAt.Load()
    if p == 0 {
        return 0
    }
    unsafe "p is the address of std.task's global DepthHook (DepthInit)" {
        f := (*raw.FromAddr[DepthHook](uint(p))).depth
        return f()
    }
}

/// The words that let std.core tell whether the running task's scope chain is
/// cancelled without calling into std.task. std.task registers them once with
/// ChainInit, before any task runs: the base, size and slot shift of the two
/// task stack regions, the distance from a slot's top down to its task
/// record, and the offsets of the record's inner scope and of a scope
/// record's cancel flag.
///
/// A slot holds one task's stack, so the address of a frame names the running
/// task. The words are written once at start and sit on cache lines of their
/// own, so no cancel or count store shares them.
align(64) struct ChainWords {
    base0: raw.Atomic[u64]
    size0: raw.Atomic[u64]
    shift0: raw.Atomic[u64]
    base1: raw.Atomic[u64]
    size1: raw.Atomic[u64]
    shift1: raw.Atomic[u64]
    task: raw.Atomic[u64]
    inner: raw.Atomic[u64]
    flag: raw.Atomic[u64]
}

var Chain: ChainWords = ChainWords(base0: raw.Atomic[u64].New(0), size0: raw.Atomic[u64].New(0),
    shift0: raw.Atomic[u64].New(0), base1: raw.Atomic[u64].New(0), size1: raw.Atomic[u64].New(0),
    shift1: raw.Atomic[u64].New(0), task: raw.Atomic[u64].New(0), inner: raw.Atomic[u64].New(0),
    flag: raw.Atomic[u64].New(0))

/// A region of one task stack, as ChainInit takes it; base 0 means none.
export struct ChainRegion {
    export base: u64
    export size: u64
    export shift: u64
}

/// Registers the words ChainWords describes: r0 and r1, the task stack
/// regions; task, the bytes from a slot's top down to its task record; inner,
/// the offset of the record's inner scope pointer; flag, the offset of a
/// scope record's cancel flag (a u32). std.task calls it once, before its
/// first task runs. O(1); it allocates nothing.
export ChainInit(r0: ChainRegion, r1: ChainRegion, task: u64, inner: u64, flag: u64) {
    // Edge: every word is stored before base0 and base1, and ChainState reads
    // a base before the words it guards, so a nonzero base comes with its
    // region's size and shift and with the offsets (as rt's RegionReserve
    // stores shift and size before base). All of it precedes the first task
    // (TaskInit), so any task's load sees it.
    Chain.task.Store(task)
    Chain.inner.Store(inner)
    Chain.flag.Store(flag)
    Chain.size0.Store(r0.size)
    Chain.shift0.Store(r0.shift)
    Chain.size1.Store(r1.size)
    Chain.shift1.Store(r1.shift)
    Chain.base1.Store(r1.base)
    Chain.base0.Store(r0.base)
}

/// ChainState's answer when the frame is on no task stack, or before
/// ChainInit.
export const ChainUnknown: u32 = 2

/// Reports whether the running task's scope chain is cancelled: 0 when its
/// inner scope is absent or the scope's cancel flag is clear, 1 when the flag
/// is set, and ChainUnknown when this frame is on no task stack (a thread's
/// system stack, a foreign thread) or nothing is registered. On ChainUnknown
/// the caller makes the full check. Cancelling a scope sets the flag of every
/// scope under it, so the inner scope's flag speaks for the whole chain. The
/// caller tests the NoCancel mask first. O(1): two range compares and two
/// dependent loads; it allocates nothing.
export ChainState(): u32 {
    var here: u64 = 0
    a := u64(raw.Addr(raw.Of(&here)))
    // Edge: as ChainInit's: the base load comes first.
    var top: u64 = 0
    b0 := Chain.base0.Load()
    o0 := a -% b0
    if b0 != 0 && o0 < Chain.size0.Load() {
        top = b0 + (((o0 >> Chain.shift0.Load()) + 1) << Chain.shift0.Load())
    } else {
        b1 := Chain.base1.Load()
        o1 := a -% b1
        if b1 == 0 || o1 >= Chain.size1.Load() {
            return ChainUnknown
        }
        top = b1 + (((o1 >> Chain.shift1.Load()) + 1) << Chain.shift1.Load())
    }
    unsafe "top is the top of the slot this frame is on, whose task record lies task bytes below it (ChainInit); the record's inner scope pointer is the running task's own, and a scope record outlives every task inside it" {
        inner := raw.Read(raw.FromAddr[u64](Trunc[uint](top - Chain.task.Load() + Chain.inner.Load())))
        if inner == 0 {
            return 0
        }
        // Edge: the cancel flag as std.task's Cancelled reads it: a stale 0
        // is closed by the recheck under the scope's lock that every wait
        // makes, and a cancellation point that misses a store just made
        // returns Canceled at the next one (level-triggered).
        f := (*raw.FromAddr[raw.Atomic[u32]](Trunc[uint](inner + Chain.flag.Load()))).Load()
        return if f != 0 { 1 } else { 0 }
    }
}

/// Returns io with every cancellation masked: no scope's Cancel or deadline
/// reaches an operation on it. It is for std's own cleanup, such as a final
/// flush or a wake, which opens no scope from it. Unlike Io.NoCancel it reads
/// no task's depth, so it links nothing. O(1); it allocates nothing.
export IoMasked(io: Io): Io {
    return Io(table: io.table, flags: io.flags | 1 | (MaskDepthMax << MaskDepthShift))
}

/// Returns inner with from's NoCancel mask: bit 0 and from's mask depth (the
/// deeper one when inner is masked too), with inner's other bits kept, or
/// inner unchanged when from is unmasked. A wrapping Io, such as std.sim's
/// Recorder, forwards an operation to its inner Io this way, so a scope
/// opened from the wrapping Io is past the inner Io's mask exactly when it is
/// past the caller's. Io.NoCancel would instead record the depth at the
/// forward. O(1); it allocates nothing.
export IoMaskAs(inner: Io, from: Io): Io {
    if (from.flags & 1) == 0 {
        return inner
    }
    var d := (from.flags >> MaskDepthShift) & MaskDepthMax
    if (inner.flags & 1) != 0 {
        old := (inner.flags >> MaskDepthShift) & MaskDepthMax
        if old > d {
            d = old
        }
    }
    keep := inner.flags & ~(MaskDepthMax << MaskDepthShift)
    return Io(table: inner.table, flags: keep | 1 | (d << MaskDepthShift))
}

/// Returns io's mask depth: the scope depth at which NoCancel was taken.
/// Meaningful only when bit 0 of the flags is set. O(1); it allocates
/// nothing.
export IoMaskDepth(io: Io): u32 {
    return Trunc[u32]((io.flags >> MaskDepthShift) & MaskDepthMax)
}

/// Arms (op 0) or disarms (op 1) the deadline of the scope record rec, nanos
/// from now on io's clock, through io's deadline entry. One indirect call
/// here; it allocates nothing.
export IoDeadline(io: Io, rec: *u8, nanos: i64, op: u32): IoResult {
    f := IoTab(io).deadline
    return f(io, rec, nanos, op)
}

/// Writes data whole to process stream i (0 stdout, 1 stderr) through io's
/// stdio entry, after the stream's buffered bytes and under its lock, so it
/// never interleaves with another write of the stream. It is not a
/// cancellation point. Other on an Io with no process. One indirect call
/// here; it allocates nothing.
export IoStdio(io: Io, i: u32, data: []u8): IoResult {
    f := IoTab(io).stdio
    return f(io, i, data)
}

/// The operations of an Io's call entry, passed in IoCallArgs.op. Each line
/// gives the handle h, the inputs p and q, the bytes out it fills, the words
/// it reads (a, b, c, d of w) and those it sets (after <-); value is the
/// result. A table without an op answers Unsupported. std.rt wraps each op in
/// a function.
///
/// IoOpSeek: h; a offset, b whence (0 start, 1 current, 2 end) <- value the
/// new position.
export const IoOpSeek: u32 = 1
/// IoOpTruncate: h; a the new size.
export const IoOpTruncate: u32 = 2
/// IoOpLock: h, whole-file and advisory; a 1 shared or 2 exclusive, b 1 to
/// try <- value 1 held, 0 another holder has it (try only). A wait is
/// cancellable.
export const IoOpLock: u32 = 3
/// IoOpUnlock: h.
export const IoOpUnlock: u32 = 4
/// IoOpMap: h; a offset, b length (> 0) <- value the address of offset,
/// c the mapping's base, d its length (IoOpUnmap's). Read-only, private;
/// a fault inside it is a panic report.
export const IoOpMap: u32 = 5
/// IoOpUnmap: a base, b length, as IoOpMap gave them.
export const IoOpUnmap: u32 = 6
/// IoOpStat: p a path; a 1 not to follow a final link <- a kind (IoKind*),
/// b size, c modified (nanoseconds since the epoch), d the mode bits (the
/// Unix mode & 0o7777; Windows 0o444 or 0o666 by the read-only attribute,
/// with 0o111 on a directory).
export const IoOpStat: u32 = 7
/// IoOpSetModified: p; a nanoseconds since the epoch (access time kept).
export const IoOpSetModified: u32 = 8
/// IoOpSetMode: p; a the mode bits (Windows: read-only when the user
/// write bit 0o200 is clear).
export const IoOpSetMode: u32 = 9
/// IoOpSymlink: p the target, q the link's path.
export const IoOpSymlink: u32 = 10
/// IoOpReadLink: p; out <- value the bytes written; value == out.Len()
/// means it may be cut: call again with more.
export const IoOpReadLink: u32 = 11
/// IoOpCwd: out <- value as IoOpReadLink.
export const IoOpCwd: u32 = 12
/// IoOpAbsolute: p; out <- value as IoOpReadLink (Windows
/// GetFullPathNameW; Linux joins the working directory and p without
/// resolving links or dot segments).
export const IoOpAbsolute: u32 = 13
/// IoOpCopy: p the source, q the destination (created or truncated) <-
/// value the bytes copied.
export const IoOpCopy: u32 = 14
/// IoOpSockOpt: h a socket; a the option (IoSock*), b its value, c a
/// second value (IoSockKeepAlive's idle seconds, IoSockLinger's seconds).
export const IoOpSockOpt: u32 = 15
/// IoOpShutdown: h a socket; a 0 read, 1 write, 2 both.
export const IoOpShutdown: u32 = 16
/// IoOpLocalAddr: h a socket; out 16 bytes, the address in network order
/// (IoAddr.ip's layout) <- a port, b 1 for IPv6, c the zone.
export const IoOpLocalAddr: u32 = 17
/// IoOpPeerAddr: as IoOpLocalAddr, for the peer.
export const IoOpPeerAddr: u32 = 18
/// IoOpIsTerminal: a the standard stream (0 in, 1 out, 2 err) <- value 1
/// when it is a terminal (a Windows console), else 0. b 1 (rt.StdVt):
/// value 1 only when it also reads escape sequences, a Windows
/// console's output having its virtual-terminal bit turned on if need be.
export const IoOpIsTerminal: u32 = 19
/// IoOpTermSize: a the standard stream <- a columns, b rows.
export const IoOpTermSize: u32 = 20
/// IoOpTimeZone: a the year (0 for the zone in force) <- out the OS's
/// record (Windows: DYNAMIC_TIME_ZONE_INFORMATION, 432 bytes, for year 0;
/// TIME_ZONE_INFORMATION, 172 bytes, for a year), value its length.
/// Unsupported on Linux, whose zone is TZ or /etc/localtime.
export const IoOpTimeZone: u32 = 21
/// IoOpWall: <- value the wall clock, nanoseconds since the Unix epoch: the
/// OS's, or a Sim's simulated one under std.sim. It is a call op, not a table
/// entry, so a program links the OS clock call, and on Windows its import,
/// only when it reads the wall clock.
export const IoOpWall: u32 = 22
/// IoOpPipe: <- value the read end's OS handle, os the write end's (both
/// blocking and not inherited); std.rt's PipeOpen wraps them as pool handles.
export const IoOpPipe: u32 = 23
/// IoOpSpawn: p the arguments and q the environment (each a *str and its
/// count), out the directory's bytes, w.a the address of the caller's
/// IoSpawnReq <- value the child's OS handle (the pidfd, or the process
/// handle), os its pid, and IoSpawnReq's pipes the parent's OS pipe ends;
/// std.rt's Spawn wraps them.
export const IoOpSpawn: u32 = 24
/// IoOpSignal: h a child; a the signal (Linux's numbers: 9 kills), b 1 to
/// reach its group, c its pid, d the group's OS handle (Windows' job, 0
/// none).
export const IoOpSignal: u32 = 25
/// IoOpDetach: h a child, a the group's OS handle (0 none): the child and
/// its group run on, and the runtime reaps the child.
export const IoOpDetach: u32 = 26
/// IoOpGroupEnd: a the group's OS handle: what remains of a group whose
/// leader has ended is killed, and the handle closed (Windows; Linux's
/// poller does it in the wait).
export const IoOpGroupEnd: u32 = 27
/// IoOpPtyOpen: a columns, b rows <- value the term (IoPty.term), a and b
/// the OS handles of the read and write ends: std.rt's PtyOpen wraps them.
export const IoOpPtyOpen: u32 = 28
/// IoOpPtyResize: a columns, b rows, c the term.
export const IoOpPtyResize: u32 = 29
/// IoOpPtyClose: a the term (Windows closes its pseudo-console; nothing on
/// Linux).
export const IoOpPtyClose: u32 = 30
/// IoOpTermOpen: <- value the controlling terminal's input OS handle, os its
/// output's (the same descriptor on Linux); std.rt's TermOpen wraps them.
export const IoOpTermOpen: u32 = 31
/// IoOpTermMode: h the input handle, a the output's runtime handle, b the
/// mode to set (IoTermSaved, IoTermRaw, IoTermNoEcho; IoTermGet sets none); p
/// the saved mode to set back for IoTermSaved; out <- the mode in force
/// before, value its length (IoTermModeBytes at most).
export const IoOpTermMode: u32 = 32
/// IoOpReady: h <- value 0 once h has input to read; nothing is consumed.
/// A cancel ends the wait with Canceled.
export const IoOpReady: u32 = 33
/// IoOpTermRead: h the terminal's input; out <- value the bytes of input
/// it holds now, without waiting (Linux the bytes, Windows whole
/// INPUT_RECORDs of 20 bytes), 0 when none.
export const IoOpTermRead: u32 = 34
/// IoOpUnixListen: p the path, w.a the backlog (0 or less: the largest) <-
/// value the listening OS socket, ready for the poller; std.rt wraps it.
export const IoOpUnixListen: u32 = 35
/// IoOpUnixDial: p the path; value <- the connected OS socket, or -1 when
/// the listener's backlog is full (nothing held; std.rt asks again).
export const IoOpUnixDial: u32 = 36
/// IoOpDataSync: h <- its written data and the metadata a read needs
/// flushed (Linux fdatasync; Windows FlushFileBuffers, the whole flush).
export const IoOpDataSync: u32 = 37
/// IoOpCertVerify: p the chain (sys.CertRecords' form), q the host, w.a
/// now in Unix nanoseconds; value <- the platform's verdict (0 trusted),
/// os <- the chain's status bits. Windows only.
export const IoOpCertVerify: u32 = 38
/// IoOpDirOpenAt: h the held directory (0: p is a whole path), p the
/// entry's name; value <- the directory's OS handle (std.rt wraps it as a
/// directory stream); a link is never followed (NotDirectory).
export const IoOpDirOpenAt: u32 = 39
/// IoOpRemoveAt: h the held directory (0: p is a whole path), p the
/// entry's name, w.a 1 for a directory; the entry (a link itself, never
/// its target) is removed.
export const IoOpRemoveAt: u32 = 40
/// IoOpListen: p the address ("host:port", as the table's listen entry), w.a
/// the backlog (0 or less: the largest), w.b 1 to let the bind take a port
/// still in TIME_WAIT, 0 to refuse one <- value the listener's runtime
/// handle, os the bound port. A Sim answers it as its listen entry, which has
/// no queue or TIME_WAIT to choose.
export const IoOpListen: u32 = 41
/// IoOpUdpConnect: h a UDP socket; p the peer (an IoAddr).
export const IoOpUdpConnect: u32 = 42
/// IoOpTryRecvFrom: h; out the buffer; q the source <- the count, or -1
/// with err None when nothing is queued.
export const IoOpTryRecvFrom: u32 = 43
/// The number of ops; an op at or past it is Unsupported everywhere.
export const IoOpCount: u32 = 44

/// IoOpTermMode's b: set the saved mode p back.
export const IoTermSaved: i64 = 0
/// IoOpTermMode's b: raw mode (no echo, no line editing, no signals from
/// keys, virtual-terminal input on Windows).
export const IoTermRaw: i64 = 1
/// IoOpTermMode's b: echo off, line editing kept (a secret's line).
export const IoTermNoEcho: i64 = 2
/// IoOpTermMode's b: set nothing, only read the mode in force into out.
/// std.term uses it to save the mode before it sets one.
export const IoTermGet: i64 = 3
/// The bytes of a saved terminal mode: a Linux termios (36), or Windows'
/// two console modes (8).
export const IoTermModeBytes: int = 64

/// IoOpSockOpt's option TCP_NODELAY: b 1 on, 0 off.
export const IoSockNoDelay: i64 = 1
/// IoOpSockOpt's option SO_KEEPALIVE: b 1 on; c the idle seconds before the
/// first probe, 0 the OS's.
export const IoSockKeepAlive: i64 = 2
/// IoOpSockOpt's option SO_REUSEADDR (Linux; Windows succeeds and changes
/// nothing).
export const IoSockReuseAddr: i64 = 3
/// IoOpSockOpt's option SO_RCVBUF: b bytes.
export const IoSockRecvBuffer: i64 = 4
/// IoOpSockOpt's option SO_SNDBUF: b bytes.
export const IoSockSendBuffer: i64 = 5
/// IoOpSockOpt's option SO_LINGER: b 1 on, c seconds; on with 0 closes by
/// reset, leaving no TIME_WAIT.
export const IoSockLinger: i64 = 6
/// IoOpSockOpt's option IP_DONTFRAGMENT / IP_MTU_DISCOVER (IPv4): b 1 on, 0 off.
export const IoSockDontFrag: i64 = 7
/// IoOpSockOpt's option IPV6_DONTFRAG / IPV6_MTU_DISCOVER (IPv6): b 1 on, 0 off.
export const IoSockDontFragV6: i64 = 8

/// Makes one call through io's call entry; a.op names the operation (IoOp*).
/// It is the only native code the call links. One indirect call here; it
/// allocates nothing. The op's errors and cost are its own.
export IoCall(io: Io, a: *IoCallArgs): IoResult {
    f := IoTab(io).call
    return f(io, a)
}

// IoTab is io's table, as a reference: Io holds a raw pointer, not an
// &IoTable, and the table is always a global (the static pseudo-loan), so
// this is the one place it is dereferenced.
// Each Io* call below reads one entry through it: one load of the table
// pointer and one of the entry, with no copy of the 20-entry table even in
// a dev build.
IoTab(io: Io): &IoTable {
    unsafe "an Io's table always points at a global table" {
        return &*io.table
    }
}

/// Opens path with mode, through io's open entry. err is SysErr.None on
/// success, and value then holds the entry's result. std/sys documents each
/// entry's errors and cost. One indirect call here; it allocates nothing.
export IoOpen(io: Io, path: str, mode: u32): IoResult {
    f := IoTab(io).open
    return f(io, path, mode)
}

/// Reads into buf from handle h at offset pos (-1: the stream position),
/// through io's read entry. err is SysErr.None on success, and value then
/// holds the entry's result. std/sys documents each entry's errors and cost.
/// One indirect call here; it allocates nothing.
export IoRead(io: Io, h: uint, buf: mut []u8, pos: i64): IoResult {
    f := IoTab(io).read
    return f(io, h, buf, pos)
}

/// Writes data to handle h at offset pos (-1: the stream position), through
/// io's write entry. err is SysErr.None on success, and value then holds the
/// entry's result. std/sys documents each entry's errors and cost. One
/// indirect call here; it allocates nothing.
export IoWrite(io: Io, h: uint, data: []u8, pos: i64): IoResult {
    f := IoTab(io).write
    return f(io, h, data, pos)
}

/// Returns the size in bytes of the file behind handle h, through io's size
/// entry. err is SysErr.None on success, and value then holds the entry's
/// result. std/sys documents each entry's errors and cost. One indirect call
/// here; it allocates nothing.
export IoSize(io: Io, h: uint): IoResult {
    f := IoTab(io).size
    return f(io, h)
}

/// Flushes the file behind handle h to its device, through io's sync entry.
/// err is SysErr.None on success, and value then holds the entry's result.
/// std/sys documents each entry's errors and cost. One indirect call here; it
/// allocates nothing.
export IoSync(io: Io, h: uint): IoResult {
    f := IoTab(io).sync
    return f(io, h)
}

/// Closes handle h, through io's close entry. err is SysErr.None on success,
/// and value then holds the entry's result. std/sys documents each entry's
/// errors and cost. One indirect call here; it allocates nothing.
export IoClose(io: Io, h: uint): IoResult {
    f := IoTab(io).close
    return f(io, h)
}

/// Renames src to dst, through io's rename entry. err is SysErr.None on
/// success, and value then holds the entry's result. std/sys documents each
/// entry's errors and cost. One indirect call here; it allocates nothing.
export IoRename(io: Io, src: str, dst: str): IoResult {
    f := IoTab(io).rename
    return f(io, src, dst)
}

/// Removes the file or empty directory at path, through io's remove entry.
/// err is SysErr.None on success, and value then holds the entry's result.
/// std/sys documents each entry's errors and cost. One indirect call here; it
/// allocates nothing.
export IoRemove(io: Io, path: str): IoResult {
    f := IoTab(io).remove
    return f(io, path)
}

/// Opens a listening stream socket at addr, through io's listen entry. err is
/// SysErr.None on success, and value then holds the entry's result. std/sys
/// documents each entry's errors and cost. One indirect call here; it
/// allocates nothing.
export IoListen(io: Io, addr: str): IoResult {
    f := IoTab(io).listen
    return f(io, addr)
}

/// Waits for and accepts a connection on listener h, through io's accept
/// entry. err is SysErr.None on success, and value then holds the entry's
/// result. std/sys documents each entry's errors and cost. One indirect call
/// here; it allocates nothing.
export IoAccept(io: Io, h: uint): IoResult {
    f := IoTab(io).accept
    return f(io, h)
}

/// Connects a stream socket to addr, through io's dial entry. err is
/// SysErr.None on success, and value then holds the entry's result. std/sys
/// documents each entry's errors and cost. One indirect call here; it
/// allocates nothing.
export IoDial(io: Io, addr: str): IoResult {
    f := IoTab(io).dial
    return f(io, addr)
}

/// Receives into buf from socket h, through io's recv entry. err is
/// SysErr.None on success, and value then holds the entry's result. std/sys
/// documents each entry's errors and cost. One indirect call here; it
/// allocates nothing.
export IoRecv(io: Io, h: uint, buf: mut []u8): IoResult {
    f := IoTab(io).recv
    return f(io, h, buf)
}

/// Sends data on socket h, through io's send entry. err is SysErr.None on
/// success, and value then holds the entry's result. std/sys documents each
/// entry's errors and cost. One indirect call here; it allocates nothing.
export IoSend(io: Io, h: uint, data: []u8): IoResult {
    f := IoTab(io).send
    return f(io, h, data)
}

/// Reads the runtime clock in nanoseconds, through io's now entry. err is
/// SysErr.None on success, and value then holds the reading. One indirect
/// call here; it allocates nothing.
export IoNow(io: Io): IoResult {
    f := IoTab(io).now
    return f(io)
}

/// Waits until the process signal s is delivered, through io's signal entry;
/// the wait is cancellable unless io is NoCancel. err is SysErr.None on
/// success, and value then holds the entry's result; std/sys documents the
/// entry's errors and cost.
///
/// An s of IoSigFrom (1 << 30) or more is Other, before any entry is called:
/// those bits mark the native entry's other requests, which only IoSignalMark
/// and IoSignalFrom make. One compare and one indirect call here; it
/// allocates nothing.
export IoSignal(io: Io, s: u32): IoResult {
    if s >= IoSigFrom {
        return IoResult(value: 0, err: SysErr.Other, os: 0)
    }
    f := IoTab(io).signal
    return f(io, s)
}

/// The bit that asks the native signal entry for a mark instead of a wait: s
/// with bit 31 set asks for the delivery count of the signal in bits 0 to 3.
/// An s below 1 << 30 is the plain wait. Only IoSignalMark passes it, and
/// only to the native table; IoSignal refuses it (Other).
export const IoSigMark: u32 = 0x8000_0000
/// The bit that asks the native signal entry to wait from a mark: s with bit
/// 30 set carries the signal in bits 0 to 3 and the mark's low 26 bits
/// (IoSigMarkLow) in bits 4 to 29. Only IoSignalFrom passes it.
export const IoSigFrom: u32 = 0x4000_0000
/// The bits of a mark that IoSignalFrom passes: its low 26. The native entry
/// rebuilds the whole mark from them and the current count, which is exact
/// while fewer than 1 << 26 deliveries of the signal came between the mark
/// and the wait.
export const IoSigMarkLow: u64 = 0x3FF_FFFF

/// Returns signal s's delivery count now, as value, after the OS watch of s
/// is installed, so every later delivery moves the count. Pass it to
/// IoSignalFrom as the mark. It neither waits nor is a cancellation point, so
/// it may be called on a masked Io or off a task stack. O(1); it allocates
/// nothing.
///
/// Only the native table has it: on every other Io (a Sim, a Recorder's Tape,
/// a Replayer, a test's table) it is Unsupported and reaches no table.
/// Otherwise it fails as the plain wait does: Other when s is not a signal or
/// the OS refused the watch, Unsupported for Resize and Continue (3 and 4) on
/// Windows.
export IoSignalMark(io: Io, s: u32): IoResult {
    if !IsNative(io) {
        return IoResult(value: 0, err: SysErr.Unsupported, os: 0)
    }
    if s > 15 {
        return IoResult(value: 0, err: SysErr.Other, os: 0)
    }
    f := IoTab(io).signal
    return f(io, s | IoSigMark)
}

/// Waits until signal s's delivery count is no longer mark, a value
/// IoSignalMark returned for s; the wait is cancellable unless io is
/// NoCancel. A delivery after the mark ends the wait at once, even one that
/// came before this call, which a plain IoSignal wait (counting from its own
/// call) misses. O(1) besides the wait; it allocates nothing.
///
/// The result is the plain wait's: None on a delivery, Canceled when the
/// cancel came first, Other when s is not a signal. Unsupported on any Io but
/// the native table's, as IoSignalMark. The wait is exact while fewer than 1
/// << 26 deliveries of s come between the mark and the wait.
export IoSignalFrom(io: Io, s: u32, mark: u64): IoResult {
    if !IsNative(io) {
        return IoResult(value: 0, err: SysErr.Unsupported, os: 0)
    }
    if s > 15 {
        return IoResult(value: 0, err: SysErr.Other, os: 0)
    }
    low := Trunc[u32](mark & IoSigMarkLow)
    f := IoTab(io).signal
    return f(io, s | IoSigFrom | (low << 4))
}

// The entries Io's own methods and the std.core locks use; only std.core
// calls them, except IoSleep.

/// Sleeps nanos through io's sleep entry and returns the entry's result as it
/// is, so a Recorder logs what its inner Io answered; Io.Sleep reads every
/// failure as Canceled instead. One indirect call at most; it allocates
/// nothing.
///
/// A zero sleep is the cancellation check. On the native table with no mask,
/// and on a task stack, it answers from ChainState with no indirect call; on
/// a masked Io or another table, the entry answers.
export IoSleep(io: Io, nanos: i64): IoResult {
    if nanos == 0 && (io.flags & 1) == 0 && IsNative(io) {
        k := ChainState()
        if k == 0 {
            return IoResult(value: 0, err: SysErr.None, os: 0)
        }
        if k == 1 {
            return IoResult(value: 0, err: SysErr.Canceled, os: 0)
        }
    }
    f := IoTab(io).sleep
    return f(io, nanos)
}

IoRandom(io: Io, buf: mut []u8): IoResult {
    f := IoTab(io).random
    return f(io, buf)
}

IoYield(io: Io): IoResult {
    f := IoTab(io).yield
    return f(io)
}

// IoWait returns when *addr != expected, on a wake, spuriously, or with
// Canceled; IoWake wakes one waiter, or every waiter when all.
IoWait(io: Io, addr: *u32, expected: u32): IoResult {
    f := IoTab(io).wait
    return f(io, addr, expected)
}

IoWake(io: Io, addr: *u32, all: bool) {
    f := IoTab(io).wake
    f(io, addr, all)
}

/// Parks the task through io's wait entry while the 4-byte atomic word at
/// address word holds the bits of the 4-byte value at address expected (read
/// once, on entry); the body of std.atomic's Atomic[T].Wait. Returns true
/// when the wait ended with Canceled. It may return early, as the entry may.
/// O(1) besides the wait; it allocates nothing.
///
/// Both values are 4 bytes because the OS waits (futex, WaitOnAddress)
/// compare 4-byte words. On the native table off a task it panics (code 16).
export IoWaitWord(io: Io, word: uint, expected: uint): bool {
    unsafe "word is the address of a live 4-byte atomic word (the Wait receiver's) and expected that of a 4-byte local of the caller's frame, both from raw.Addr of a reference" {
        bits := raw.Read(raw.FromAddr[u32](expected))
        return IoWait(io, raw.FromAddr[u32](word), bits).err == SysErr.Canceled
    }
}

/// Wakes one task waiting on the 4-byte word at address word, or all of them
/// when all is true; the body of std.atomic's Atomic[T].Wake and of
/// std.runtime's Waker.Wake. It needs no Io and may be called from any
/// thread, a foreign one included. O(1) besides the wakes (one per
/// listener); it allocates nothing.
///
/// The wake goes through the native table's wake entry, then through every
/// listener registered with WakeListen, so a task parked on a Sim's Io wakes
/// too; with no listener that costs one load. Before std.task registers its
/// table, at compile time and on x86_64-freestanding, the native wake is
/// skipped.
export IoWakeWord(word: uint, all: bool) {
    // Edge: seq_cst; NativeInit stores the table's address once, before any
    // task runs, so a nonzero load reads a registered table.
    t := Native.tab.Load()
    if t != 0 {
        unsafe "word is the address of a live 4-byte atomic word (the Wake receiver's); t is the address of the native table NativeInit registered, a global that lives until exit" {
            IoWake(Io(table: raw.FromAddr[IoTable](uint(t)), flags: 0), raw.FromAddr[u32](word), all)
        }
    }
    // Edge: a seq_cst fence, so the caller's store of the word (which may
    // be a release store) is ordered before the listeners' loads of their
    // live counts below, against a waiter whose table was counted (a
    // seq_cst RMW) before it compared the word: the waiter sees the store
    // or the listener sees the table. The native wake above may make
    // no system call, so its barrier is not relied on.
    raw.Fence()
    // Edge: acquire, pairing with WakeListen's release store of ready, so
    // every listener below ready is written before it is called.
    k := Listeners.ready.LoadOrd(1)
    for i in 0..int(k) {
        f := Listeners.f[i]
        unsafe "word is the address of a live 4-byte atomic word" {
            f(raw.FromAddr[u32](word), all)
        }
    }
}

/// The most word-wake listeners a process can register.
export const WakeListenMax: int = 8

// WakeListenerSet is the process-global list of word-wake listeners:
// claimed counts slots handed out, ready the prefix that is
// written and may be called. Listeners are never removed.
struct WakeListenerSet {
    claimed: raw.Atomic[u32]
    ready: raw.Atomic[u32]
    f: [8]fn(*u32, bool)
}

NoWakeListener(addr: *u32, all: bool) {
}

var Listeners: WakeListenerSet = WakeListenerSet(claimed: raw.Atomic[u32].New(0), ready: raw.Atomic[u32].New(0), f: [NoWakeListener; 8])

/// Registers f, which every IoWakeWord then calls with the word's address and
/// all, after the native wake. A table that parks tasks on words its own way
/// (std.sim's) registers once, at its first use, so a wake with no Io reaches
/// its tasks. False when WakeListenMax listeners are registered. O(1) besides
/// waiting for an earlier registration to publish; it allocates nothing.
export WakeListen(f: fn(*u32, bool)): bool {
    // Edge: none needed for the claim; the slot is this caller's alone.
    i := Listeners.claimed.Add(1) // the value before the add
    if i >= Trunc[u32](WakeListenMax) {
        return false
    }
    unsafe "slot i is this caller's alone (the claim), and no reader calls it before ready passes it" {
        raw.Write(raw.Offset(raw.PtrCast[[8]fn(*u32, bool), fn(*u32, bool)](raw.Of(&Listeners.f)), int(i)), f)
    }
    // Edge: release, after the slot's write; an earlier claimant publishes
    // first, so ready only grows over written slots.
    for Listeners.ready.LoadOrd(1) != i {
        SpinPauseHint()
    }
    Listeners.ready.StoreOrd(i + 1, 2)
    return true
}

/// Calls io's enter hook for task. The task scheduler calls the enter and
/// leave hooks around each run slice; std.task and std.sim call them. One
/// indirect call; it allocates nothing.
export IoEnter(io: Io, task: u64) {
    f := IoTab(io).enter
    f(io, task)
}

/// Calls io's leave hook for task, as IoEnter calls enter. One indirect
/// call; allocates nothing.
export IoLeave(io: Io, task: u64) {
    f := IoTab(io).leave
    f(io, task)
}

/// Creates the directory at path, through io's mkdir entry. err is
/// SysErr.None on success, and value then holds the entry's result. std/sys
/// documents each entry's errors and cost. One indirect call here; it
/// allocates nothing.
export IoMkdir(io: Io, path: str): IoResult {
    f := IoTab(io).mkdir
    return f(io, path)
}

/// Fills st with what path is, its size and its modification time, through
/// io's stat entry. err is SysErr.None on success, and value then holds the
/// entry's result. std/sys documents each entry's errors and cost. One
/// indirect call here; it allocates nothing.
export IoStatPath(io: Io, path: str, st: mut IoStat): IoResult {
    f := IoTab(io).stat
    return f(io, path, st)
}

/// Opens the directory at path for reading, through io's opendir entry. err
/// is SysErr.None on success, and value then holds the entry's result.
/// std/sys documents each entry's errors and cost. One indirect call here; it
/// allocates nothing.
export IoOpenDir(io: Io, path: str): IoResult {
    f := IoTab(io).opendir
    return f(io, path)
}

/// Reads the next entry of directory h, its name into name and its kind and
/// size into st, through io's readdir entry. err is SysErr.None on success,
/// and value then holds the entry's result. std/sys documents each entry's
/// errors and cost. One indirect call here; it allocates nothing.
export IoReadDir(io: Io, h: uint, name: mut []u8, st: mut IoStat): IoResult {
    f := IoTab(io).readdir
    return f(io, h, name, st)
}

/// Opens a datagram socket bound to addr, through io's udp entry. err is
/// SysErr.None on success, and value then holds the entry's result. std/sys
/// documents each entry's errors and cost. One indirect call here; it
/// allocates nothing.
export IoUdp(io: Io, addr: IoAddr): IoResult {
    f := IoTab(io).udp
    return f(io, addr)
}

/// Sends one datagram of data on socket h to the address to, through io's
/// sendto entry. err is SysErr.None on success, and value then holds the
/// entry's result. std/sys documents each entry's errors and cost. One
/// indirect call here; it allocates nothing.
export IoSendTo(io: Io, h: uint, data: []u8, to: IoAddr): IoResult {
    f := IoTab(io).sendto
    return f(io, h, data, to)
}

/// Receives one datagram into buf from socket h, and its sender into from,
/// through io's recvfrom entry. err is SysErr.None on success, and value then
/// holds the entry's result. std/sys documents each entry's errors and cost.
/// One indirect call here; it allocates nothing.
export IoRecvFrom(io: Io, h: uint, buf: mut []u8, from: mut IoAddr): IoResult {
    f := IoTab(io).recvfrom
    return f(io, h, buf, from)
}

/// Resolves host into the addresses of out, through io's resolve entry. err
/// is SysErr.None on success, and value then holds the entry's result.
/// std/sys documents each entry's errors and cost. One indirect call here; it
/// allocates nothing.
export IoResolve(io: Io, host: str, out: mut []IoAddr): IoResult {
    f := IoTab(io).resolve
    return f(io, host, out)
}

/// Starts the process args with the standard streams child.stdio names, and
/// fills child's out fields, through io's spawn entry. err is SysErr.None on
/// success, and value then holds the entry's result. std/sys documents each
/// entry's errors and cost. One indirect call here; it allocates nothing.
export IoSpawn(io: Io, args: []str, child: mut IoChild): IoResult {
    f := IoTab(io).spawn
    return f(io, args, child)
}

/// Waits for the child process h to exit, through io's procwait entry. err is
/// SysErr.None on success, and value then holds the entry's result. std/sys
/// documents each entry's errors and cost. One indirect call here; it
/// allocates nothing.
export IoProcWait(io: Io, h: uint): IoResult {
    f := IoTab(io).procwait
    return f(io, h)
}

/// Kills the child process h, through io's prockill entry. err is SysErr.None
/// on success, and value then holds the entry's result. std/sys documents
/// each entry's errors and cost. One indirect call here; it allocates
/// nothing.
export IoProcKill(io: Io, h: uint): IoResult {
    f := IoTab(io).prockill
    return f(io, h)
}

/// Returns the socket handle this process inherited from its parent, if any,
/// through io's inherited entry. err is SysErr.None on success, and value
/// then holds the entry's result. std/sys documents each entry's errors and
/// cost. One indirect call here; it allocates nothing.
export IoInherited(io: Io): IoResult {
    f := IoTab(io).inherited
    return f(io)
}
