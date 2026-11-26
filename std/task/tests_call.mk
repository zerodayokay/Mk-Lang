// Why: the OS calls behind the generic call entry (seek, metadata, locks,
// copies, mappings, paths, sockets, terminals) each need checking on both
// routes and both targets.
// What: the RT8-Call-* blocks for std/rt/call.mk, std/task/io_call.mk and
// each target's sys/call.mk, with helpers CaBytes, CaWrite, CaFifoCopy and
// the gated copy (CaGateIo, CaGateCall, CaGateJob, CaGatedCopy).
// How: run by the scheduler harness, and on both targets at 1 and 16
// carriers by std/rt_deadline_test.go (its RT8- blocks). Files are made in
// the temporary directory (FlName: TEMP or TMP on Windows, TMPDIR or /tmp
// on Linux, so a Linux run's files are on the distribution's own file
// system).

import std.core
import std.raw
import std.rt
import std.sys

// CaBytes is s's bytes, the form the call functions take a path in.
CaBytes(s: str): []u8 from s {
    return s.AsBytes()
}

// CaWrite makes name holding n bytes, byte i being i % 251.
CaWrite(io: Io, name: str, n: int) {
    o := NativeOpen(io, name, 1)
    Assert(o.err == core.SysErr.None)
    h := Trunc[uint](o.value)
    var b: [4096]u8 = [0; 4096]
    for i in 0..n {
        b[i] = Trunc[u8](i % 251)
    }
    Assert(NativeWrite(io, h, b[0..n], -1).value == i64(n))
    Assert(NativeClose(io, h).err == core.SysErr.None)
}

test "RT8-Call-Seek Seek from the start, the position and the end, on the poller's route and the pool's" (sys: &System) {
    io := sys.io
    var nb: [256]u8 = [0; 256]
    name := FlName(mut nb, "seek", 0)
    CaWrite(io, name, 100)
    for pool in 0..2 {
        FlRoute(pool == 1)
        o := NativeOpen(io, name, 2)
        Assert(o.err == core.SysErr.None)
        h := Trunc[uint](o.value)
        Assert(rt.FileSeek(io, h, 0, 2).value == 100)
        Assert(rt.FileSeek(io, h, 10, 0).value == 10)
        var b: [4]u8 = [0; 4]
        Assert(NativeRead(io, h, mut b[..], -1).value == 4)
        Assert(b[0] == 10 && b[3] == 13)
        Assert(rt.FileSeek(io, h, -4, 1).value == 10)
        Assert(rt.FileSeek(io, h, -1, 2).value == 99)
        Assert(NativeRead(io, h, mut b[..], -1).value == 1 && b[0] == 99 % 251)
        // Before the start, and an unknown whence, are Other; the position
        // stays.
        Assert(rt.FileSeek(io, h, -200, 1).err == core.SysErr.Other)
        Assert(rt.FileSeek(io, h, 0, 3).err == core.SysErr.Other)
        Assert(rt.FileSeek(io, h, 0, 1).value == 100)
        Assert(NativeClose(io, h).err == core.SysErr.None)
    }
    FlRoute(false)
    Assert(NativeRemove(io, name).err == core.SysErr.None)
}

test "RT8-Call-Meta Truncate, StatEx, SetModified and SetMode, and their errors" (sys: &System) {
    io := sys.io
    var nb: [256]u8 = [0; 256]
    name := FlName(mut nb, "meta", 0)
    CaWrite(io, name, 300)
    o := NativeOpen(io, name, 2)
    h := Trunc[uint](o.value)
    Assert(rt.FileTruncate(io, h, 120).err == core.SysErr.None)
    Assert(rt.FileTruncate(io, h, -1).err == core.SysErr.Other)
    Assert(NativeClose(io, h).err == core.SysErr.None)
    var w := core.IoWords(a: 0, b: 0, c: 0, d: 0)
    Assert(rt.StatEx(io, CaBytes(name), false, mut w).err == core.SysErr.None)
    Assert(w.a == i64(core.IoKindFile) && w.b == 120)
    // A time Windows keeps exactly (100 ns ticks): 2020-09-13T12:26:40.1234567Z.
    t: i64 = 1_600_000_000_123_456_700
    Assert(rt.SetModified(io, CaBytes(name), t).err == core.SysErr.None)
    Assert(rt.StatEx(io, CaBytes(name), true, mut w).err == core.SysErr.None)
    Assert(w.c == t && w.a == i64(core.IoKindFile))
    Assert(rt.SetMode(io, CaBytes(name), 0o444).err == core.SysErr.None)
    Assert(rt.StatEx(io, CaBytes(name), false, mut w).err == core.SysErr.None)
    Assert(w.d == 0o444)
    Assert(rt.SetMode(io, CaBytes(name), 0o640).err == core.SysErr.None)
    Assert(rt.StatEx(io, CaBytes(name), false, mut w).err == core.SysErr.None)
    want: i64 = if Target.os == Os.Windows { 0o666 } else { 0o640 }
    Assert(w.d == want)
    // A directory: kind Dir, size 0.
    var db: [256]u8 = [0; 256]
    dir := FlName(mut db, "metad", 0)
    Assert(NativeMkdir(io, dir).err == core.SysErr.None)
    Assert(rt.StatEx(io, CaBytes(dir), false, mut w).err == core.SysErr.None)
    Assert(w.a == i64(core.IoKindDir) && w.b == 0)
    Assert(NativeRemove(io, dir).err == core.SysErr.None)
    Assert(NativeRemove(io, name).err == core.SysErr.None)
    Assert(rt.StatEx(io, CaBytes(name), false, mut w).err == core.SysErr.NotFound)
    Assert(rt.SetModified(io, CaBytes(name), t).err == core.SysErr.NotFound)
    Assert(rt.SetMode(io, CaBytes(name), 0o600).err == core.SysErr.NotFound)
    // A NUL in a path is InvalidPath, with no call.
    Assert(rt.StatEx(io, "a\u{0}b".AsBytes(), false, mut w).err == core.SysErr.InvalidPath)
}

test "RT8-Call-Lock TryLock between two handles, Unlock, and a waiting Lock that gets the lock when it is freed" (sys: &System) {
    io := sys.io
    var nb: [256]u8 = [0; 256]
    name := FlName(mut nb, "lock", 0)
    CaWrite(io, name, 10)
    a := Trunc[uint](NativeOpen(io, name, 2).value)
    b := Trunc[uint](NativeOpen(io, name, 2).value)
    Assert(rt.FileLock(io, a, true, true).value == 1)
    Assert(rt.FileLock(io, b, true, true).value == 0)
    Assert(rt.FileLock(io, b, false, true).value == 0)
    Assert(rt.FileUnlock(io, a).err == core.SysErr.None)
    Assert(rt.FileLock(io, b, false, true).value == 1)
    // Shared with shared.
    Assert(rt.FileLock(io, a, false, true).value == 1)
    Assert(rt.FileUnlock(io, a).err == core.SysErr.None)
    Assert(rt.FileUnlock(io, b).err == core.SysErr.None)
    // A wait: b waits for a's exclusive lock until a's task frees it.
    Assert(rt.FileLock(io, a, true, true).value == 1)
    got := raw.Atomic[u32].New(0)
    scope s := io.Scope() {
        s.go CaLockWait(io, b, &got)
        io.Sleep(core.DurationOf(30 * DlMs)) else {}
        Assert(got.Load() == 0)
        Assert(rt.FileUnlock(io, a).err == core.SysErr.None)
    }
    Assert(got.Load() == 1)
    Assert(rt.FileUnlock(io, b).err == core.SysErr.None)
    Assert(NativeClose(io, a).err == core.SysErr.None)
    Assert(NativeClose(io, b).err == core.SysErr.None)
    Assert(NativeRemove(io, name).err == core.SysErr.None)
}

CaLockWait(io: Io, h: uint, got: &raw.Atomic[u32]) {
    if rt.FileLock(io, h, true, false).value == 1 {
        got.Store(1)
    }
}

test "RT8-Call-LockCancel a deadline ends a waiting Lock with Canceled, and the lock is not taken" (sys: &System) {
    io := sys.io
    var nb: [256]u8 = [0; 256]
    name := FlName(mut nb, "lockc", 0)
    CaWrite(io, name, 10)
    a := Trunc[uint](NativeOpen(io, name, 2).value)
    b := Trunc[uint](NativeOpen(io, name, 2).value)
    Assert(rt.FileLock(io, a, true, true).value == 1)
    var r := core.IoResult(value: 0, err: core.SysErr.None, os: 0)
    scope s := io.Scope() {
        s.CancelAfter(core.DurationOf(20 * DlMs))
        r = rt.FileLock(io, b, true, false)
    }
    Assert(r.err == core.SysErr.Canceled)
    // Repeatable: the same call again takes the lock once it is free.
    Assert(rt.FileUnlock(io, a).err == core.SysErr.None)
    Assert(rt.FileLock(io, b, true, false).value == 1)
    Assert(rt.FileUnlock(io, b).err == core.SysErr.None)
    Assert(NativeClose(io, a).err == core.SysErr.None)
    Assert(NativeClose(io, b).err == core.SysErr.None)
    Assert(NativeRemove(io, name).err == core.SysErr.None)
}

test "RT8-Call-LockCancelFree a waiting Lock in a cancelled scope returns Canceled and takes a free lock nowhere; TryLock is no cancellation point" (sys: &System) {
    io := sys.io
    var nb: [256]u8 = [0; 256]
    name := FlName(mut nb, "lockf", 0)
    CaWrite(io, name, 10)
    a := Trunc[uint](NativeOpen(io, name, 2).value)
    b := Trunc[uint](NativeOpen(io, name, 2).value)
    for k in 0..2 {
        var r := core.IoResult(value: 0, err: core.SysErr.None, os: 0)
        scope s := io.Scope() {
            if k == 0 {
                s.Cancel()
            } else {
                // A deadline that has passed: cancelled as by Cancel.
                s.CancelAfter(core.DurationOf(0))
            }
            r = rt.FileLock(io, a, true, false)
        }
        Assert(r.err == core.SysErr.Canceled && r.value == 0)
        // Nothing was taken: the other handle gets the lock at once.
        Assert(rt.FileLock(io, b, true, true).value == 1)
        Assert(rt.FileUnlock(io, b).err == core.SysErr.None)
    }
    // TryLock never waits and checks no scope: it takes a free lock.
    var t := core.IoResult(value: 0, err: core.SysErr.None, os: 0)
    scope s := io.Scope() {
        s.Cancel()
        t = rt.FileLock(io, a, true, true)
    }
    Assert(t.err == core.SysErr.None && t.value == 1)
    Assert(rt.FileUnlock(io, a).err == core.SysErr.None)
    Assert(NativeClose(io, a).err == core.SysErr.None)
    Assert(NativeClose(io, b).err == core.SysErr.None)
    Assert(NativeRemove(io, name).err == core.SysErr.None)
}

// CaBig is the size of RT8-Call-CopyCancel's source: written by the
// kernel in about a hundred milliseconds, so a copy cut at 5 ms is
// cut mid-way when the deadline is delivered on time.
const CaBig: i64 = 512 * 1024 * 1024

// CaGate is what the gated copy's job saw: 1 in entered once a pool
// thread runs it in its call, 1 in saw when its cancel word was set
// before it ran the copy, and the nanoseconds it waited in waited.
struct CaGate {
    entered: raw.Atomic[u32]
    saw: raw.Atomic[u32]
    waited: raw.Atomic[i64]
}

var CaGated: CaGate = CaGate(entered: raw.Atomic[u32].New(0), saw: raw.Atomic[u32].New(0),
    waited: raw.Atomic[i64].New(0))

// CaHold carries the call's own job from CaGateCall to CaGateJob.
struct CaHold {
    job: fn(*core.IoCallArgs)
}

// CaGateTable is the native table whose call entry gates a copy
// (CaGateCall).
const CaGateTable = core.IoTable(open: NativeOpen, read: NativeRead, write: NativeWrite, size: NativeSize,
    sync: NativeSync, close: NativeClose, rename: NativeRename, remove: NativeRemove, listen: NativeListen,
    accept: NativeAccept, dial: NativeDial, recv: NativeRecv, send: NativeSend, now: NativeNow,
    sleep: NativeSleep, random: NativeRandom, yield: NativeYield, signal: NativeSignal, wait: NativeWait,
    wake: NativeWake, mkdir: NativeMkdir, stat: NativeStat, opendir: NativeOpenDir, readdir: NativeReadDir,
    udp: NativeUdp, sendto: NativeSendTo, recvfrom: NativeRecvFrom, resolve: NativeResolveAll,
    spawn: NativeSpawn, procwait: NativeProcWait, prockill: NativeProcKill, inherited: NativeInherited,
    enter: core.InertHook, leave: core.InertHook, deadline: NativeDeadline, stdio: NativeStdio,
    call: CaGateCall)

CaGateIo(): Io {
    unsafe "CaGateTable is a static IoTable, live for the whole program" {
        return core.MakeIo(raw.Of(&CaGateTable), 0)
    }
}

// CaGateCall is the native call entry, except that a copy's job is
// CaGateJob, which runs the copy's own job (kept in w.a, a word a copy
// does not use) only once its cancel word is set.
CaGateCall(io: Io, a: *core.IoCallArgs): core.IoResult {
    unsafe "a is the caller's record, live until this returns; hold lives in this frame until then" {
        if (*a).op != core.IoOpCopy {
            return NativeCall(io, a)
        }
        var hold := CaHold(job: (*a).job)
        (*a).w.a = Trunc[i64](raw.Addr(raw.Of(&hold)))
        (*a).job = CaGateJob
        return NativeCall(io, a)
    }
}

// CaGateJob is a gated copy's pool job: on its pool thread (Current null),
// it notes its entry in CaGated, waits until its cancel word is set
// (DlBound at most, through the pool's interrupt), notes that in CaGated,
// and then runs the copy's own job with that word. A copy so gated cannot
// end before its deadline is delivered: when the delivery never comes,
// the copy runs whole and is not Canceled.
CaGateJob(a: *core.IoCallArgs) {
    unsafe "w.a is CaGateCall's hold; stop is this pool job's cancel word, both live until the call returns" {
        // Off the pool (an inline fallback on a carrier) nothing is gated,
        // so entered and saw stay 0 without stalling the carrier.
        if raw.IsNull(Current()) {
            t0 := BenchNow()
            CaGated.entered.Store(1)
            if !raw.IsNull((*a).stop) {
                word := raw.PtrCast[u32, raw.Atomic[u32]]((*a).stop)
                for (*word).Load() == 0 && BenchNow() - t0 < DlBound {
                    sys.SleepNanos(DlMs)
                }
                if (*word).Load() != 0 {
                    CaGated.waited.Store(BenchNow() - t0)
                    CaGated.saw.Store(1)
                }
            }
        }
        hold := raw.FromAddr[CaHold](Trunc[uint]((*a).w.a))
        (*a).w.a = 0
        job := (*hold).job
        job(a)
    }
}

// CaGatedCopy runs RT8-Call-CopyCancel's gated copy in its own task so
// the scope body can wait for CaGateJob to enter on a pool thread before
// arming the 5 ms deadline.
CaGatedCopy(io: Io, src: str, dst: str, err_out: &raw.Atomic[u32], val_out: &raw.Atomic[i64]) {
    r := rt.CopyFile(io, CaBytes(src), CaBytes(dst))
    val_out.Store(r.value)
    err_out.Store(if r.err == core.SysErr.Canceled { 1 } else if r.err == core.SysErr.None { 2 } else { 3 })
}

test "RT8-Call-CopyCancel a deadline ends a CopyFile in its call with Canceled, dst never made; a copy that races its deadline ends Canceled with a prefix in dst or copies all; a scope cancelled first copies nothing (prefix)" (sys: &System) {
    io := sys.io
    var nb: [256]u8 = [0; 256]
    var cb: [256]u8 = [0; 256]
    src := FlName(mut nb, "copyc", 0)
    dst := FlName(mut cb, "copyc", 1)
    // A file of CaBig bytes, its tail a hole (zeros) the copy still writes.
    CaWrite(io, src, 1000)
    h := Trunc[uint](NativeOpen(io, src, 2).value)
    Assert(rt.FileTruncate(io, h, CaBig).err == core.SysErr.None)
    Assert(NativeClose(io, h).err == core.SysErr.None)
    _ = NativeRemove(io, dst)
    // Gated: the scope body waits until the copy has entered CaGateJob on
    // a pool thread (JobInCall) before arming the 5 ms deadline, so a slow
    // pool pickup never cancels the job while queued; then the copy cannot
    // end before the deadline reaches its cancel word, however late a busy
    // machine delivers it, and ends Canceled with no dst made (each target's
    // CopyFile reads the word before it opens anything).
    gio := CaGateIo()
    CaGated.entered.Store(0)
    CaGated.saw.Store(0)
    CaGated.waited.Store(0)
    gerr := raw.Atomic[u32].New(0)
    gval := raw.Atomic[i64].New(-1)
    t0 := BenchNow()
    var armed_at: i64 = 0
    scope s := gio.Scope() {
        s.go CaGatedCopy(gio, src, dst, &gerr, &gval)
        for CaGated.entered.Load() == 0 && BenchNow() - t0 < DlBound {
            YieldNow()
        }
        if CaGated.entered.Load() == 1 {
            armed_at = BenchNow()
            s.CancelAfter(core.DurationOf(5 * DlMs))
        } else {
            s.Cancel()
        }
    }
    took := if armed_at != 0 { BenchNow() - armed_at } else { BenchNow() - t0 }
    Assert(CaGated.entered.Load() == 1)
    Assert(CaGated.saw.Load() == 1)
    Assert(gerr.Load() == 1 && gval.Load() == 0)
    Assert(took < DlBound)
    var w := core.IoWords(a: 0, b: 0, c: 0, d: 0)
    Assert(rt.StatEx(io, CaBytes(dst), false, mut w).err == core.SysErr.NotFound)
    sys.PrintErr("RT8-Call-CopyCancel gated: the deadline reached the copy after {wu} us, Canceled after {tu} us\n",
        (wu: CaGated.waited.Load() / 1000, tu: took / 1000)) else {}
    // Ungated, the whole native path: a deadline delivered within the
    // copy's time ends it Canceled with a prefix in dst (none on Windows,
    // which deletes it); one that a busy machine delivers after the copy
    // ended finds it whole. Nothing in between. A Canceled copy's prefix
    // may be all of src: Linux reads the word again after its last chunk.
    // Under MK_TIMING (a quiet machine) the deadline must win.
    timing := sys.Env("MK_TIMING") else { "" }
    var r := core.IoResult(value: 0, err: core.SysErr.None, os: 0)
    t1 := BenchNow()
    scope s1 := io.Scope() {
        s1.CancelAfter(core.DurationOf(5 * DlMs))
        r = rt.CopyFile(io, CaBytes(src), CaBytes(dst))
    }
    Assert(BenchNow() - t1 < DlBound)
    st := rt.StatEx(io, CaBytes(dst), false, mut w)
    if timing == "1" {
        Assert(r.err == core.SysErr.Canceled)
    }
    if r.err == core.SysErr.Canceled {
        Assert(st.err == core.SysErr.NotFound || (st.err == core.SysErr.None && w.b <= CaBig))
    } else {
        Assert(r.err == core.SysErr.None && r.value == CaBig)
        Assert(st.err == core.SysErr.None && w.b == CaBig)
    }
    // A scope cancelled before the call: Canceled, and the copy never ran.
    _ = NativeRemove(io, dst)
    var c := core.IoResult(value: 0, err: core.SysErr.None, os: 0)
    scope s2 := io.Scope() {
        s2.Cancel()
        c = rt.CopyFile(io, CaBytes(src), CaBytes(dst))
    }
    Assert(c.err == core.SysErr.Canceled)
    Assert(rt.StatEx(io, CaBytes(dst), false, mut w).err == core.SysErr.NotFound)
    Assert(NativeRemove(io, src).err == core.SysErr.None)
}

// CaFifoCopy is RT8-Call-CopyFifo's copying task: it copies the FIFO src
// to dst, stores 1 in out when the copy answered Other with os 22 (Linux
// CopyFile's kind check on the open descriptor) and 2 for anything else,
// and stores the clock when the copy returned in done.
CaFifoCopy(io: Io, src: str, dst: str, out: &raw.Atomic[u32], done: &raw.Atomic[u64]) {
    r := rt.CopyFile(io, CaBytes(src), CaBytes(dst))
    done.Store(Trunc[u64](BenchNow()))
    out.Store(if r.err == core.SysErr.Other && r.os == 22 { 1 } else { 2 })
}

test "RT8-Call-CopyFifo Linux: a CopyFile from a FIFO with no writer waits in its open until a writer comes, then refuses the FIFO; a deadline ends the wait with Canceled; neither makes dst" (sys: &System) {
    // A FIFO in the file system is Linux's; Windows has none, and this
    // block checks nothing there.
    if Target.os != Os.Linux {
        return
    }
    io := sys.io
    var nb: [256]u8 = [0; 256]
    var cb: [256]u8 = [0; 256]
    fifo := FlName(mut nb, "copyf", 0)
    dst := FlName(mut cb, "copyf", 1)
    _ = NativeRemove(io, fifo)
    _ = NativeRemove(io, dst)
    // std.sys makes no FIFO, so mkfifo(1) makes it, found in PATH.
    var noenv: [1]str = [""]
    var mk: [4]str = ["mkfifo", "-m", "600", fifo]
    var cs := CSpec(core.IoStdNull, core.IoStdNull, core.IoStdNull)
    m := rt.Spawn(io, mk[..], noenv[0..0], "", mut cs)
    Assert(m.err == core.SysErr.None)
    Assert(rt.ChildWait(io, Trunc[uint](m.value), cs.job).value == 0)
    PClose(io, Trunc[uint](m.value))
    var w := core.IoWords(a: 0, b: 0, c: 0, d: 0)
    // The open waits for a writer: the copy returns no earlier than the
    // clock read before this task's writer open, 30 ms after the copying
    // task started. Opened for reading and writing, as a writer, the FIFO
    // never blocks this task. The copy then sees a FIFO on its open
    // descriptor and refuses it (Other, os 22) before dst is opened.
    out := raw.Atomic[u32].New(0)
    done := raw.Atomic[u64].New(0)
    var tw: i64 = 0
    var wh: uint = 0
    scope s := io.Scope() {
        s.go CaFifoCopy(io, fifo, dst, &out, &done)
        io.Sleep(core.DurationOf(30 * DlMs)) else {
            Assert(false)
        }
        tw = BenchNow()
        o := NativeOpen(io, fifo, 2)
        Assert(o.err == core.SysErr.None)
        wh = Trunc[uint](o.value)
    }
    Assert(out.Load() == 1)
    Assert(Trunc[i64](done.Load()) >= tw)
    Assert(NativeClose(io, wh).err == core.SysErr.None)
    Assert(rt.StatEx(io, CaBytes(dst), false, mut w).err == core.SysErr.NotFound)
    // No writer comes: a deadline ends the open's wait (the pool's SIGURG
    // makes it return EINTR, and the stop word turns that into Canceled),
    // and dst is never made. 100 ms leaves the pool thread time to reach
    // the open before the deadline.
    var r := core.IoResult(value: 0, err: core.SysErr.None, os: 0)
    t0 := BenchNow()
    scope s2 := io.Scope() {
        s2.CancelAfter(core.DurationOf(100 * DlMs))
        r = rt.CopyFile(io, CaBytes(fifo), CaBytes(dst))
    }
    Assert(r.err == core.SysErr.Canceled)
    Assert(BenchNow() - t0 < DlBound)
    Assert(rt.StatEx(io, CaBytes(dst), false, mut w).err == core.SysErr.NotFound)
    Assert(core.CancelQuiet())
    Assert(NativeRemove(io, fifo).err == core.SysErr.None)
}

test "RT8-Call-Map a read-only mapping at an unaligned offset reads the file's bytes" (sys: &System) {
    io := sys.io
    var nb: [256]u8 = [0; 256]
    name := FlName(mut nb, "map", 0)
    CaWrite(io, name, 4000)
    h := Trunc[uint](NativeOpen(io, name, 0).value)
    var w := core.IoWords(a: 0, b: 0, c: 0, d: 0)
    m := rt.FileMap(io, h, 1234, 2000, mut w)
    Assert(m.err == core.SysErr.None && w.c != 0 && w.d >= 2000 && m.value - w.c >= 0)
    unsafe "the mapping is live until FileUnmap; 2000 bytes from m.value" {
        p := raw.FromAddr[u8](Trunc[uint](m.value))
        for i in 0..2000 {
            Assert(raw.Read(raw.Offset(p, i)) == Trunc[u8]((1234 + i) % 251))
        }
    }
    Assert(rt.FileUnmap(io, w.c, w.d).err == core.SysErr.None)
    Assert(rt.FileMap(io, h, 0, 0, mut w).err == core.SysErr.Other)
    Assert(rt.FileMap(io, h, -1, 10, mut w).err == core.SysErr.Other)
    Assert(NativeClose(io, h).err == core.SysErr.None)
    Assert(NativeRemove(io, name).err == core.SysErr.None)
}

test "RT8-Call-Paths CopyFile, Cwd, Absolute, Symlink and ReadLink" (sys: &System) {
    io := sys.io
    var nb: [256]u8 = [0; 256]
    var cb: [256]u8 = [0; 256]
    var lb: [256]u8 = [0; 256]
    name := FlName(mut nb, "path", 0)
    copy := FlName(mut cb, "pathc", 0)
    link := FlName(mut lb, "pathl", 0)
    CaWrite(io, name, 3000)
    Assert(rt.CopyFile(io, CaBytes(name), CaBytes(copy)).value == 3000)
    var w := core.IoWords(a: 0, b: 0, c: 0, d: 0)
    Assert(rt.StatEx(io, CaBytes(copy), false, mut w).err == core.SysErr.None && w.b == 3000)
    h := Trunc[uint](NativeOpen(io, copy, 0).value)
    var b: [3000]u8 = [0; 3000]
    Assert(NativeRead(io, h, mut b[..], 0).value == 3000)
    for i in 0..3000 {
        Assert(b[i] == Trunc[u8](i % 251))
    }
    Assert(NativeClose(io, h).err == core.SysErr.None)
    Assert(rt.CopyFile(io, CaBytes(link), CaBytes(copy)).err == core.SysErr.NotFound)
    // The working directory: a full answer, and a cut one in a short buffer.
    var out: [4096]u8 = [0; 4096]
    c := rt.Cwd(io, mut out[..])
    Assert(c.err == core.SysErr.None && c.value > 0 && c.value < 4096)
    var tiny: [2]u8 = [0; 2]
    Assert(rt.Cwd(io, mut tiny[..]).value == 2)
    // Absolute of a relative name ends with it; of an absolute one, is it.
    var ab: [4096]u8 = [0; 4096]
    r := rt.Absolute(io, "rel".AsBytes(), mut ab[..])
    n := Trunc[int](r.value)
    Assert(r.err == core.SysErr.None && n > 4 && ab[n - 1] == 108 && ab[n - 3] == 114)
    a2 := rt.Absolute(io, CaBytes(name), mut ab[..])
    Assert(a2.value == i64(name.Len()))
    // A link: on Windows without the privilege, Unsupported.
    s := rt.Symlink(io, CaBytes(name), CaBytes(link))
    if s.err == core.SysErr.None {
        Assert(rt.StatEx(io, CaBytes(link), true, mut w).err == core.SysErr.None && w.a == i64(core.IoKindLink))
        Assert(rt.StatEx(io, CaBytes(link), false, mut w).err == core.SysErr.None && w.a == i64(core.IoKindFile))
        var tb: [256]u8 = [0; 256]
        t := rt.ReadLink(io, CaBytes(link), mut tb[..])
        Assert(t.err == core.SysErr.None && t.value == i64(name.Len()))
        nm := name.AsBytes()
        for i in 0..nm.Len() {
            Assert(tb[i] == nm[i])
        }
        Assert(rt.Symlink(io, CaBytes(name), CaBytes(link)).err == core.SysErr.Exists)
        Assert(NativeRemove(io, link).err == core.SysErr.None)
    } else {
        Assert(Target.os == Os.Windows && s.err == core.SysErr.Unsupported)
    }
    // Not a link.
    var tb2: [64]u8 = [0; 64]
    Assert(rt.ReadLink(io, CaBytes(name), mut tb2[..]).err == core.SysErr.Other)
    Assert(NativeRemove(io, copy).err == core.SysErr.None)
    Assert(NativeRemove(io, name).err == core.SysErr.None)
}

test "RT8-Call-Sock socket options, addresses and a write shutdown on loopback" (sys: &System) {
    io := sys.io
    l := NListen(io)
    lh := Trunc[uint](l.value)
    var ab: [32]u8 = [0; 32]
    d := NativeDial(io, NAddr(mut ab, l.os))
    Assert(d.err == core.SysErr.None)
    a := NativeAccept(io, lh)
    Assert(a.err == core.SysErr.None)
    c := Trunc[uint](d.value)
    p := Trunc[uint](a.value)
    Assert(rt.SockOption(io, c, core.IoSockNoDelay, 1, 0).err == core.SysErr.None)
    Assert(rt.SockOption(io, c, core.IoSockKeepAlive, 1, 30).err == core.SysErr.None)
    Assert(rt.SockOption(io, c, core.IoSockRecvBuffer, 65536, 0).err == core.SysErr.None)
    Assert(rt.SockOption(io, c, core.IoSockSendBuffer, 65536, 0).err == core.SysErr.None)
    Assert(rt.SockOption(io, lh, core.IoSockReuseAddr, 1, 0).err == core.SysErr.None)
    Assert(rt.SockOption(io, c, 99, 1, 0).err == core.SysErr.Other)
    Assert(rt.SockOption(io, c, core.IoSockNoDelay, -1, 0).err == core.SysErr.Other)
    var out: [16]u8 = [0; 16]
    var w := core.IoWords(a: 0, b: 0, c: 0, d: 0)
    Assert(rt.SockAddress(io, lh, false, mut out[..], mut w).err == core.SysErr.None)
    Assert(w.a == l.os && w.b == 0)
    Assert(rt.SockAddress(io, c, true, mut out[..], mut w).err == core.SysErr.None)
    Assert(w.a == l.os && out[0] == 127 && out[3] == 1)
    Assert(rt.SockAddress(io, p, true, mut out[..], mut w).err == core.SysErr.None)
    cport := w.a
    Assert(rt.SockAddress(io, c, false, mut out[..], mut w).err == core.SysErr.None && w.a == cport)
    var small: [4]u8 = [0; 4]
    Assert(rt.SockAddress(io, c, false, mut small[..], mut w).err == core.SysErr.Other)
    // The client's write side closed: the peer reads the end of the stream.
    Assert(rt.SockShutdown(io, c, 1).err == core.SysErr.None)
    var b: [8]u8 = [0; 8]
    Assert(NativeRecv(io, p, mut b[..]).value == 0)
    Assert(rt.SockShutdown(io, c, 3).err == core.SysErr.Other)
    // Linger 0: the close resets the connection (no TIME_WAIT).
    Assert(rt.SockOption(io, p, core.IoSockLinger, 1, 0).err == core.SysErr.None)
    rt.Abandon(p)
    rt.Abandon(c)
    rt.Abandon(lh)
}

test "RT8-Call-Term IsTerminal, TermSize and TimeZone answer, and refuse a bad stream" (sys: &System) {
    io := sys.io
    Assert(rt.IsTerminal(io, 3).err == core.SysErr.Other)
    for s in 0..3 {
        r := rt.IsTerminal(io, Trunc[u32](s))
        Assert(r.err == core.SysErr.None && (r.value == 0 || r.value == 1))
        var w := core.IoWords(a: 0, b: 0, c: 0, d: 0)
        z := rt.TermSize(io, Trunc[u32](s), mut w)
        Assert(r.value == 0 || s == 0 || (z.err == core.SysErr.None && w.a > 0 && w.b > 0))
    }
    var tz: [432]u8 = [0; 432]
    z := rt.TimeZone(io, 0, mut tz[..])
    if Target.os == Os.Windows {
        Assert(z.err == core.SysErr.None && z.value == 432)
        y := rt.TimeZone(io, 2026, mut tz[..])
        Assert(y.err == core.SysErr.None && y.value == 172)
        Assert(rt.TimeZone(io, 1000, mut tz[..]).err == core.SysErr.Other)
        Assert(rt.TimeZone(io, 0, mut tz[0..100]).err == core.SysErr.Other)
    } else {
        Assert(z.err == core.SysErr.Unsupported)
    }
}

// CaMode is the open entry's options form with the permission bits perm.
CaMode(flags: u32, perm: u32): u32 {
    return core.IoOpenOptions | flags | (perm << core.IoOpenPermShift)
}

test "RT8-Call-Open the options form: exclusive create, append, truncate and a created file's permissions, on both routes" (sys: &System) {
    io := sys.io
    for pool in 0..2 {
        FlRoute(pool == 1)
        var nb: [256]u8 = [0; 256]
        name := FlName(mut nb, "open", pool)
        excl := CaMode(core.IoOpenRead | core.IoOpenWrite | core.IoOpenCreate | core.IoOpenExclusive, 0o644)
        o := NativeOpen(io, name, excl)
        Assert(o.err == core.SysErr.None)
        h := Trunc[uint](o.value)
        Assert(NativeWrite(io, h, "hello".AsBytes(), -1).value == 5)
        Assert(NativeClose(io, h).err == core.SysErr.None)
        Assert(NativeOpen(io, name, excl).err == core.SysErr.Exists)
        // Append: every write at the end, whatever was read before.
        a := Trunc[uint](NativeOpen(io, name, CaMode(core.IoOpenRead | core.IoOpenAppend, 0)).value)
        var b: [16]u8 = [0; 16]
        Assert(NativeRead(io, a, mut b[0..2], -1).value == 2)
        Assert(NativeWrite(io, a, " world".AsBytes(), -1).value == 6)
        Assert(NativeClose(io, a).err == core.SysErr.None)
        r := Trunc[uint](NativeOpen(io, name, CaMode(core.IoOpenRead, 0)).value)
        Assert(NativeRead(io, r, mut b[..], 0).value == 11 && b[0] == 104 && b[5] == 32 && b[10] == 100)
        // Read-only: a write fails.
        Assert(NativeWrite(io, r, "x".AsBytes(), 0).err != core.SysErr.None)
        Assert(NativeClose(io, r).err == core.SysErr.None)
        // Truncate without create: the file is empty; a missing one is
        // NotFound.
        t := Trunc[uint](NativeOpen(io, name, CaMode(core.IoOpenWrite | core.IoOpenTruncate, 0)).value)
        Assert(NativeSize(io, t).value == 0)
        Assert(NativeClose(io, t).err == core.SysErr.None)
        Assert(NativeRemove(io, name).err == core.SysErr.None)
        Assert(NativeOpen(io, name, CaMode(core.IoOpenWrite | core.IoOpenTruncate, 0)).err == core.SysErr.NotFound)
        // A created file's permissions: 0o600 on Linux; on Windows the
        // owner's write bit clear makes it read-only.
        perm: u32 = if Target.os == Os.Windows { 0o444 } else { 0o600 }
        c := Trunc[uint](NativeOpen(io, name, CaMode(core.IoOpenWrite | core.IoOpenCreate, perm)).value)
        Assert(NativeClose(io, c).err == core.SysErr.None)
        var w := core.IoWords(a: 0, b: 0, c: 0, d: 0)
        Assert(rt.StatEx(io, CaBytes(name), false, mut w).err == core.SysErr.None && w.d == i64(perm))
        Assert(rt.SetMode(io, CaBytes(name), 0o644).err == core.SysErr.None)
        Assert(NativeRemove(io, name).err == core.SysErr.None)
    }
    FlRoute(false)
}

test "RT8-File-Ninep a file counted as on a 9p mount is not attached: its reads and writes take the pool, and agree" (sys: &System) {
    io := sys.io
    var nb: [256]u8 = [0; 256]
    name := FlName(mut nb, "ninep", 0)
    NinepTest.Store(1)
    o := NativeOpen(io, name, 1)
    Assert(o.err == core.SysErr.None)
    h := Trunc[uint](o.value)
    // The switch acts on io_uring's open only: Linux never attaches it;
    // Windows' port takes a disk file as before.
    Assert(FlAsync(h) == (Target.os == Os.Windows && FlBackend() != 3))
    Assert(NativeWrite(io, h, "nine p".AsBytes(), -1).value == 6)
    var b: [8]u8 = [0; 8]
    Assert(NativeRead(io, h, mut b[..], 0).value == 6 && b[0] == 110 && b[5] == 112)
    Assert(NativeSync(io, h).err == core.SysErr.None)
    Assert(NativeClose(io, h).err == core.SysErr.None)
    NinepTest.Store(0)
    // Without the switch a /tmp file on io_uring is attached, as before.
    h2 := Trunc[uint](NativeOpen(io, name, 0).value)
    Assert(FlAsync(h2) == (FlBackend() != 3))
    Assert(NativeClose(io, h2).err == core.SysErr.None)
    Assert(NativeRemove(io, name).err == core.SysErr.None)
}

test "RT8-Call-DataSync a data-only sync of a written file succeeds on both routes, and a cancelled scope's is Canceled" (sys: &System) {
    io := sys.io
    var nb: [256]u8 = [0; 256]
    name := FlName(mut nb, "dsync", 0)
    CaWrite(io, name, 4000)
    for mode in 0..2 {
        // Mode 2 opens the file as it is, mode 1 truncates it; both write.
        o := NativeOpen(io, name, if mode == 0 { 2 } else { 1 })
        Assert(o.err == core.SysErr.None)
        h := Trunc[uint](o.value)
        Assert(NativeWrite(io, h, "sync".AsBytes(), -1).value == 4)
        Assert(rt.FileDataSync(io, h).err == core.SysErr.None)
        var r := core.IoResult(value: 0, err: core.SysErr.None, os: 0)
        scope s := io.Scope() {
            s.Cancel()
            r = rt.FileDataSync(io, h)
        }
        Assert(r.err == core.SysErr.Canceled)
        Assert(rt.FileDataSync(io, h).err == core.SysErr.None)
        Assert(NativeClose(io, h).err == core.SysErr.None)
    }
    Assert(NativeRemove(io, name).err == core.SysErr.None)
}

test "RT8-Call-OpenBytes rt.OpenBytes: a NUL is InvalidPath, UTF-8 opens on any Io, other bytes only natively (WTF-8 on Windows, any bytes on Linux), Unsupported elsewhere" (sys: &System) {
    io := sys.io
    var nb: [256]u8 = [0; 256]
    name := FlName(mut nb, "obytes", 0)
    CaWrite(io, name, 100)
    // UTF-8 bytes: the open entry, as NativeOpen.
    o := rt.OpenBytes(io, CaBytes(name), 0)
    Assert(o.err == core.SysErr.None)
    var b: [100]u8 = [0; 100]
    Assert(NativeRead(io, Trunc[uint](o.value), mut b[..], 0).value == 100 && b[99] == 99)
    Assert(NativeClose(io, Trunc[uint](o.value)).err == core.SysErr.None)
    // A NUL anywhere: InvalidPath before any call.
    var pb: [300]u8 = [0; 300]
    nm := name.AsBytes()
    for i in 0..nm.Len() {
        pb[i] = nm[i]
    }
    n := nm.Len()
    pb[n] = 0
    Assert(rt.OpenBytes(io, pb[0..n + 1], 0).err == core.SysErr.InvalidPath)
    // A name ending in 0xFF: not UTF-8, and not WTF-8 either.
    pb[n] = 0xFF
    bad := pb[0..n + 1]
    if PWin() {
        Assert(rt.OpenBytes(io, bad, 1).err == core.SysErr.InvalidPath)
    } else {
        // Linux takes any bytes: create, reopen and remove the name. On
        // macOS the file system judges them: APFS keeps names as UTF-8 and
        // refuses the name (EILSEQ, InvalidPath), and nothing was made.
        c := rt.OpenBytes(io, bad, 1)
        if Target.os == Os.Macos && c.err == core.SysErr.InvalidPath {
            Assert(rt.OpenBytes(io, bad, 0).err != core.SysErr.None)
        } else {
            Assert(c.err == core.SysErr.None)
            Assert(NativeClose(io, Trunc[uint](c.value)).err == core.SysErr.None)
            r := rt.OpenBytes(io, bad, 0)
            Assert(r.err == core.SysErr.None)
            Assert(NativeClose(io, Trunc[uint](r.value)).err == core.SysErr.None)
            Assert(rt.RemoveAt(io, 0, bad, false).err == core.SysErr.None)
            Assert(rt.OpenBytes(io, bad, 0).err == core.SysErr.NotFound)
        }
    }
    // An Io that is not native: UTF-8 reaches its open entry, other bytes
    // are Unsupported, a NUL is still InvalidPath.
    var inert := io
    unsafe "InertIo is a constant table, alive for the whole process" {
        inert = core.MakeIo(raw.Of(&core.InertIo), 0)
    }
    Assert(rt.OpenBytes(inert, CaBytes(name), 0).err == core.SysErr.Other)
    Assert(rt.OpenBytes(inert, bad, 0).err == core.SysErr.Unsupported)
    pb[n] = 0
    Assert(rt.OpenBytes(inert, pb[0..n + 1], 0).err == core.SysErr.InvalidPath)
    Assert(NativeRemove(io, name).err == core.SysErr.None)
}

test "RT8-Call-CopySelf CopyFile of a file onto itself, by its path or a link to it, is Denied and the file keeps its bytes; onto a longer file it truncates after the check" (sys: &System) {
    io := sys.io
    var nb: [256]u8 = [0; 256]
    var lb: [256]u8 = [0; 256]
    var bb: [256]u8 = [0; 256]
    name := FlName(mut nb, "cself", 0)
    link := FlName(mut lb, "cselfl", 0)
    big := FlName(mut bb, "cselfb", 0)
    CaWrite(io, name, 3000)
    Assert(rt.CopyFile(io, CaBytes(name), CaBytes(name)).err == core.SysErr.Denied)
    var w := core.IoWords(a: 0, b: 0, c: 0, d: 0)
    Assert(rt.StatEx(io, CaBytes(name), false, mut w).err == core.SysErr.None && w.b == 3000)
    s := rt.Symlink(io, CaBytes(name), CaBytes(link))
    if s.err == core.SysErr.None {
        Assert(rt.CopyFile(io, CaBytes(name), CaBytes(link)).err == core.SysErr.Denied)
        Assert(rt.CopyFile(io, CaBytes(link), CaBytes(name)).err == core.SysErr.Denied)
        Assert(rt.StatEx(io, CaBytes(name), false, mut w).err == core.SysErr.None && w.b == 3000)
        Assert(NativeRemove(io, link).err == core.SysErr.None)
    } else {
        Assert(Target.os == Os.Windows && s.err == core.SysErr.Unsupported)
    }
    // Onto a longer file: truncated to the source's length, after the check.
    CaWrite(io, big, 4000)
    Assert(rt.CopyFile(io, CaBytes(name), CaBytes(big)).value == 3000)
    Assert(rt.StatEx(io, CaBytes(big), false, mut w).err == core.SysErr.None && w.b == 3000)
    h := Trunc[uint](NativeOpen(io, big, 0).value)
    var b: [3000]u8 = [0; 3000]
    Assert(NativeRead(io, h, mut b[..], 0).value == 3000)
    for i in 0..3000 {
        Assert(b[i] == Trunc[u8](i % 251))
    }
    Assert(NativeClose(io, h).err == core.SysErr.None)
    Assert(NativeRemove(io, big).err == core.SysErr.None)
    Assert(NativeRemove(io, name).err == core.SysErr.None)
}

test "RT8-Call-IsNativeSocket a listener's and a stream's runtime handles are native sockets, a file's and 0 are not" (sys: &System) {
    io := sys.io
    l := NativeListen(io, "127.0.0.1:0")
    Assert(l.err == core.SysErr.None)
    lh := Trunc[uint](l.value)
    Assert(rt.IsNativeSocket(lh))
    var nb: [256]u8 = [0; 256]
    name := FlName(mut nb, "isock", 0)
    CaWrite(io, name, 10)
    h := Trunc[uint](NativeOpen(io, name, 0).value)
    Assert(!rt.IsNativeSocket(h))
    Assert(!rt.IsNativeSocket(0))
    Assert(NativeClose(io, h).err == core.SysErr.None)
    Assert(NativeClose(io, lh).err == core.SysErr.None)
    Assert(NativeRemove(io, name).err == core.SysErr.None)
}
