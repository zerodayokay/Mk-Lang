// Why: the native file entries (io_file.mk) take different routes per
// backend and handle kind; each route must give the same answers.
// What: the RT-File-*, RT-Bench-File and RT-MN-FileTasks blocks, with helpers such as FlName, FlCreate, FlModel,
// FlErrors and FlBenchReads.
// How: they check which route each backend takes, a property test
// of every read and write form against an in-memory model on both routes,
// the error set, handle leaks, cancellation, many tasks at once, and the
// two routes timed (kind 1, run by the scheduler harness and by mk test). Every file is named after the process
// id, because the harness runs a block at several carrier counts at once
// in one directory, and is removed on the way out.

import std.core
import std.raw
import std.rt
import std.sys

// FlPut copies s into b at at; the new end.
FlPut(b: mut [256]u8, at: int, s: str): int {
    sb := s.AsBytes()
    for i in 0..sb.Len() {
        b[at + i] = sb[i]
    }
    return at + sb.Len()
}

// FlNum writes v (>= 0) in decimal into b at at; the new end.
FlNum(b: mut [256]u8, at: int, v: i64): int {
    var d: [20]u8 = [0; 20]
    var n := 0
    var x := v
    for {
        d[n] = Trunc[u8](48 + x % 10)
        n += 1
        x = x / 10
        if x == 0 {
            break
        }
    }
    for i in 0..n {
        b[at + i] = d[n - 1 - i]
    }
    return at + n
}

// FlTmp writes the temporary directory and a separator at the start of b
// and returns the end: TEMP, else TMP (Windows); TMPDIR, else /tmp
// (Linux). mk test's
// children inherit the caller's working directory, and a std package
// test's is std/, whose embed.go embeds every file in it, so a file made
// there breaks a concurrent go build of mk/std. A Windows directory that is
// unset or longer than 160 bytes gives 0: the working directory.
FlTmp(b: mut [256]u8): int {
    var n := -1
    unsafe "the RtState page Init mapped, which lives until exit; init_sp is set once at start" {
        sp := (*rt.R()).init_sp
        if Target.os == Os.Windows {
            n = sys.GetEnvRaw(sp, "TEMP", mut b[0..160])
            if n <= 0 || n > 160 {
                n = sys.GetEnvRaw(sp, "TMP", mut b[0..160])
            }
        } else {
            n = sys.GetEnvRaw(sp, "TMPDIR", mut b[0..160])
        }
    }
    if n <= 0 || n > 160 {
        if Target.os == Os.Windows {
            return 0
        }
        n = FlPut(b, 0, "/tmp")
    }
    if b[n - 1] != 47 && b[n - 1] != 92 {
        b[n] = 47
        if Target.os == Os.Windows {
            b[n] = 92
        }
        n += 1
    }
    return n
}

// FlName is "<tmp>rtsf-<pid>-<tag>-<k>" in b (FlTmp).
FlName(b: mut [256]u8, tag: str, k: int): str from b {
    n := FlTmp(b)
    return FlNameAt(b, n, tag, k)
}

// FlNameAt writes the per-process basename after the first at bytes of b.
FlNameAt(b: mut [256]u8, at: int, tag: str, k: int): str from b {
    var n := FlPut(b, at, "rtsf-")
    n = FlNum(b, n, Trunc[i64](sys.Pid()))
    n = FlPut(b, n, "-")
    n = FlPut(b, n, tag)
    n = FlPut(b, n, "-")
    n = FlNum(b, n, i64(k))
    return str.From(b[0..n]).Must()
}

// FlRng is xorshift64.
struct FlRng {
    s: u64
}

FlRng.Next(mut self): u64 {
    var x := self.s
    x ^= x << 13
    x ^= x >> 7
    x ^= x << 17
    self.s = x
    return x
}

// FlByte is the byte at offset k of a pattern file: not periodic below
// 64,256 bytes, so a read at a wrong offset shows.
FlByte(k: int): u8 {
    return Trunc[u8]((k % 251) ^ ((k / 251) % 256))
}

// FlHandles is the process's open handle count (sys is shadowed in blocks),
// less one per pool thread on Windows, where the pool keeps each thread's
// HANDLE for ThreadInterrupt (TaskState.pthr). The pool starts a thread
// whenever a job is queued while every thread is still busy, so a run of
// sequential jobs can start several, one at a time; counting their handles
// made the leak asserts fail under mk test --std (the driver's
// TestStdRuntimeTests on Windows: RT-File-Device at its last assert). On
// Linux a thread holds no descriptor.
FlHandles(): int {
    n := sys.HandleCount()
    if Target.os != Os.Windows {
        return n
    }
    st := TS()
    var p := 0
    unsafe "the TaskState page; pthreads is read under plock" {
        rt.Lock(&(*st).plock)
        p = int((*st).pthreads)
        rt.Unlock(&(*st).plock)
    }
    return n - p
}

// FlBackend is the poller backend (1 iocp, 2 io_uring, 3 epoll).
FlBackend(): u32 {
    return PollBackend()
}

// FlRoute sets the test switch: true sends every file entry to the pool.
FlRoute(pool: bool) {
    if pool {
        FilesOnPool.Store(1)
    } else {
        FilesOnPool.Store(0)
    }
}

// FlCreate makes name with size pattern bytes (FlByte) and closes it.
FlCreate(io: Io, name: str, size: int) {
    o := NativeOpen(io, name, 1)
    Assert(o.err == core.SysErr.None)
    h := Trunc[uint](o.value)
    var buf: [4096]u8 = [0; 4096]
    var off := 0
    for off < size {
        var n := size - off
        if n > 4096 {
            n = 4096
        }
        for i in 0..n {
            buf[i] = FlByte(off + i)
        }
        w := NativeWrite(io, h, buf[0..n], i64(off))
        Assert(w.err == core.SysErr.None && w.value > 0)
        off += Trunc[int](w.value)
    }
    Assert(NativeClose(io, h).err == core.SysErr.None)
}

// FlStdAttached: whether any standard stream's handle is attached.
FlStdAttached(): bool {
    unsafe "the Process record lives until exit; its streams were set before Main" {
        p := rt.Proc()
        return FlAsync((*p).stdout.h) || FlAsync((*p).stderr.h) || FlAsync((*p).stdin.h)
    }
}

// FlAsync reports whether h is attached to the poller.
FlAsync(h: uint): bool {
    unsafe "h is a live runtime handle" {
        return (*rt.Rec(h)).async
    }
}

test "RT-File-Route iocp and io_uring attach a disk file, epoll and the pool switch never do; the standard streams never move" (sys: &System) {
    var nb: [256]u8 = [0; 256]
    name := FlName(mut nb, "route", 0)
    for pool in 0..2 {
        FlRoute(pool == 1)
        o := NativeOpen(sys.io, name, 1)
        Assert(o.err == core.SysErr.None)
        h := Trunc[uint](o.value)
        // iocp and io_uring attach a disk file; epoll and the pool switch
        // never do.
        want := pool == 0 && FlBackend() != 3
        Assert(FlAsync(h) == want)
        Assert(NativeClose(sys.io, h).err == core.SysErr.None)
    }
    FlRoute(false)
    Assert(NativeRemove(sys.io, name).err == core.SysErr.None)
    Assert(!FlStdAttached())
}

// FlModel applies steps seeded random operations to a fresh file and to an
// in-memory model of it, and checks every result: kind 0 reads and writes
// at the stream position only (pos -1), 1 positioned ones only, 2 both, in
// which only the stream ones move the stream position (an attached handle's
// rule, and Linux pread's; the Windows pool path's ReadFile with an offset
// also moves the file pointer, so kind 2 is not run there). Every 40 steps
// the file is removed and made again, empty. Lengths 0 to 699, offsets 0 to
// 7,999, so writes leave holes that read as zeros.
//
// Removed and made again, not reopened truncated: on Windows hosts measured
// here, a file truncated by CREATE_ALWAYS soon after its previous handle
// closed, then written past its old extent, can read the truncated bytes
// back in its holes. A plain Win32 program with no Mk code shows it with
// the same calls and sequences: every wrong byte is the previous contents'
// at its offset (7 of 12 runs of 80 files on one volume), and none showed in
// 24 runs with a delete and a fresh create instead.
FlModel(io: Io, tag: str, k: int, seed: u64, kind: int, steps: int) {
    var nb: [256]u8 = [0; 256]
    name := FlName(mut nb, tag, k)
    var model: [9216]u8 = [0; 9216]
    var buf: [1024]u8 = [0; 1024]
    var size := 0
    var spos := 0
    var x := FlRng(s: seed)
    var o := NativeOpen(io, name, 1)
    Assert(o.err == core.SysErr.None)
    var h := Trunc[uint](o.value)
    for step in 0..steps {
        if step > 0 && step % 40 == 0 {
            Assert(NativeClose(io, h).err == core.SysErr.None)
            Assert(NativeRemove(io, name).err == core.SysErr.None)
            o = NativeOpen(io, name, 1)
            Assert(o.err == core.SysErr.None)
            h = Trunc[uint](o.value)
            for i in 0..9216 {
                model[i] = 0
            }
            size = 0
            spos = 0
        }
        r := x.Next()
        var stream := kind == 0
        if kind == 2 {
            stream = (r & 1) == 0
        }
        write := ((r >> 1) & 1) == 0
        var n := Trunc[int]((r >> 8) % 700)
        var pos := Trunc[int]((r >> 24) % 8000)
        var p := i64(pos)
        if stream {
            pos = spos
            p = -1
            if pos + n > 9216 {
                n = 9216 - pos
            }
        }
        if write {
            for i in 0..n {
                buf[i] = Trunc[u8](((r >> 40) + Trunc[u64](i)) & 255)
            }
            w := NativeWrite(io, h, buf[0..n], p)
            Assert(w.err == core.SysErr.None && w.value == i64(n))
            for i in 0..n {
                model[pos + i] = buf[i]
            }
            if n > 0 && pos + n > size {
                size = pos + n
            }
            if stream {
                spos += n
            }
        } else {
            rd := NativeRead(io, h, mut buf[0..n], p)
            var want := 0
            if pos < size {
                want = size - pos
                if want > n {
                    want = n
                }
            }
            Assert(rd.err == core.SysErr.None && rd.value == i64(want))
            for i in 0..want {
                Assert(buf[i] == model[pos + i])
            }
            if stream {
                spos += want
            }
        }
        if step % 8 == 0 {
            sz := NativeSize(io, h)
            Assert(sz.err == core.SysErr.None && sz.value == i64(size))
        }
    }
    // The whole file, positioned, against the model.
    var off := 0
    for off < size {
        rd := NativeRead(io, h, mut buf[..], i64(off))
        Assert(rd.err == core.SysErr.None && rd.value > 0)
        for i in 0..Trunc[int](rd.value) {
            Assert(buf[i] == model[off + i])
        }
        off += Trunc[int](rd.value)
    }
    Assert(NativeRead(io, h, mut buf[..], i64(size)).value == 0)
    Assert(NativeSync(io, h).err == core.SysErr.None)
    Assert(NativeClose(io, h).err == core.SysErr.None)
    Assert(NativeRemove(io, name).err == core.SysErr.None)
}

test "RT-File-RouteDiff 48 seeded op sequences per route agree with the model on the poller and on the pool" (sys: &System) {
    // The poller route (this backend's) and the pool route (FilesOnPool)
    // each run the same sequences, so on io_uring and iocp the two are a
    // differential test against each other and against the model; on epoll
    // both are the pool.
    for pool in 0..2 {
        FlRoute(pool == 1)
        for seed in 0..16 {
            s := 0x9E37_79B9_7F4A_7C15 ^ (Trunc[u64](seed + 1) << 20)
            FlModel(sys.io, "model", seed, s, 0, 120)
            FlModel(sys.io, "model", seed, s ^ 0xABCD, 1, 120)
            if pool == 0 || Target.os == Os.Linux {
                FlModel(sys.io, "model", seed, s ^ 0x1234_5678, 2, 160)
            }
        }
    }
    FlRoute(false)
}

// FlLong is tail with "./" repeated reps times before its last component,
// in b (a path longer than io_file.mk's scratch buffer, so it takes the
// pool even on io_uring). tail is FlName's: a directory may come first.
FlLong(b: mut [1600]u8, reps: int, tail: str): str from b {
    tb := tail.AsBytes()
    var cut := 0
    for i in 0..tb.Len() {
        if tb[i] == 47 || tb[i] == 92 {
            cut = i + 1
        }
    }
    for i in 0..cut {
        b[i] = tb[i]
    }
    var n := cut
    for _ in 0..reps {
        b[n] = 46
        b[n + 1] = 47
        n += 2
    }
    for i in cut..tb.Len() {
        b[n + i - cut] = tb[i]
    }
    return str.From(b[0..n + tb.Len() - cut]).Must()
}

// FlErrors checks the error set of every file entry on the current route.
FlErrors(io: Io) {
    var nb: [256]u8 = [0; 256]
    name := FlName(mut nb, "err", 0)
    var nb2: [256]u8 = [0; 256]
    name2 := FlName(mut nb2, "err", 1)
    var missing: [256]u8 = [0; 256]
    gone := FlName(mut missing, "missing", 0)
    Assert(NativeOpen(io, gone, 0).err == core.SysErr.NotFound)
    Assert(NativeRemove(io, gone).err == core.SysErr.NotFound)
    Assert(NativeRename(io, gone, name2).err == core.SysErr.NotFound)
    Assert(NativeOpen(io, "a\x00b", 0).err == core.SysErr.InvalidPath)
    // A directory opens for reading and its reads are IsDirectory; creating
    // over it is IsDirectory (std.sys's contract, Go's behaviour).
    d := NativeOpen(io, ".", 0)
    Assert(d.err == core.SysErr.None)
    var buf: [16]u8 = [0; 16]
    Assert(NativeRead(io, Trunc[uint](d.value), mut buf[..], 0).err == core.SysErr.IsDirectory)
    Assert(NativeRead(io, Trunc[uint](d.value), mut buf[..], -1).err == core.SysErr.IsDirectory)
    Assert(NativeClose(io, Trunc[uint](d.value)).err == core.SysErr.None)
    Assert(NativeOpen(io, ".", 1).err == core.SysErr.IsDirectory)
    // A read-only handle refuses a write; the file is unchanged.
    FlCreate(io, name, 10)
    r := NativeOpen(io, name, 0)
    Assert(r.err == core.SysErr.None)
    var one: [1]u8 = [7]
    Assert(NativeWrite(io, Trunc[uint](r.value), one[..], 0).err != core.SysErr.None)
    Assert(NativeWrite(io, Trunc[uint](r.value), one[..], -1).err != core.SysErr.None)
    Assert(NativeSize(io, Trunc[uint](r.value)).value == 10)
    Assert(NativeRead(io, Trunc[uint](r.value), mut buf[..], -1).value == 10)
    Assert(buf[9] == FlByte(9))
    Assert(NativeClose(io, Trunc[uint](r.value)).err == core.SysErr.None)
    // Rename replaces; the old name is gone.
    FlCreate(io, name2, 3)
    Assert(NativeRename(io, name, name2).err == core.SysErr.None)
    Assert(NativeOpen(io, name, 0).err == core.SysErr.NotFound)
    s := NativeOpen(io, name2, 0)
    Assert(s.err == core.SysErr.None && NativeSize(io, Trunc[uint](s.value)).value == 10)
    Assert(NativeClose(io, Trunc[uint](s.value)).err == core.SysErr.None)
    // A path longer than the scratch buffer works the same.
    var lb: [1600]u8 = [0; 1600]
    long := FlLong(mut lb, 600, name2)
    Assert(long.Len() > 1024)
    if Target.os == Os.Macos {
        // macOS takes no path of 1,024 bytes or more (PATH_MAX, the
        // kernel's limit on every path argument): InvalidPath, with no
        // call, on both routes.
        Assert(NativeOpen(io, long, 0).err == core.SysErr.InvalidPath)
        Assert(NativeRemove(io, long).err == core.SysErr.InvalidPath)
        Assert(NativeRemove(io, name2).err == core.SysErr.None)
        Assert(NativeOpen(io, name2, 0).err == core.SysErr.NotFound)
        return
    }
    l := NativeOpen(io, long, 0)
    Assert(l.err == core.SysErr.None && NativeSize(io, Trunc[uint](l.value)).value == 10)
    Assert(NativeClose(io, Trunc[uint](l.value)).err == core.SysErr.None)
    var lb2: [1600]u8 = [0; 1600]
    longgone := FlLong(mut lb2, 600, gone)
    Assert(NativeOpen(io, longgone, 0).err == core.SysErr.NotFound)
    Assert(NativeRemove(io, longgone).err == core.SysErr.NotFound)
    Assert(NativeRemove(io, long).err == core.SysErr.None)
    Assert(NativeOpen(io, name2, 0).err == core.SysErr.NotFound)
}

test "RT-File-Errors every file entry's errors are the same on the poller and on the pool" (sys: &System) {
    FlRoute(false)
    FlErrors(sys.io)
    FlRoute(true)
    FlErrors(sys.io)
    FlRoute(false)
}

test "RT-File-Leaks 200 rounds of failing, cancelled and normal file entries leave no OS handle behind" (sys: &System) {
    var nb: [256]u8 = [0; 256]
    name := FlName(mut nb, "leak", 0)
    var mb: [256]u8 = [0; 256]
    gone := FlName(mut mb, "missing", 1)
    var buf: [64]u8 = [0; 64]
    var before := -1
    for round in 0..201 {
        FlRoute(round % 2 == 1)
        Assert(NativeOpen(sys.io, gone, 0).err == core.SysErr.NotFound)
        d := NativeOpen(sys.io, ".", 0)
        Assert(d.err == core.SysErr.None)
        Assert(NativeRead(sys.io, Trunc[uint](d.value), mut buf[..], 0).err == core.SysErr.IsDirectory)
        rt.Abandon(Trunc[uint](d.value))
        Assert(NativeOpen(sys.io, ".", 1).err == core.SysErr.IsDirectory)
        FlCreate(sys.io, name, 100)
        // Cancelled before the entry: nothing opened.
        scope s := sys.io.Scope() {
            s.Cancel()
            Assert(NativeOpen(sys.io, name, 0).err == core.SysErr.Canceled)
        }
        Assert(NativeRemove(sys.io, name).err == core.SysErr.None)
        if round == 0 {
            // After one warm-up round: the pool thread and the poller's
            // own handles exist.
            before = FlHandles()
        }
    }
    FlRoute(false)
    after := FlHandles()
    // A per-round leak shows as 200 or more; the slack is the pool's and
    // the poller's own handles, made once.
    Assert(before < 0 || after <= before + 8)
}

// FlReader reads h at the stream position in chunks of 1 to 4,096 bytes
// until the end, a Canceled or an error, checking each chunk against the
// pattern; got is the bytes read, bad 1 on any result other than data,
// end of file or Canceled.
FlReader(io: Io, h: uint, seed: u64, got: &raw.Atomic[u32], bad: &raw.Atomic[u32]) {
    var buf: [4096]u8 = [0; 4096]
    var x := FlRng(s: seed)
    var off := 0
    for {
        n := 1 + Trunc[int](x.Next() % 4096)
        r := NativeRead(io, h, mut buf[0..n], -1)
        if r.err == core.SysErr.Canceled {
            break
        }
        if r.err != core.SysErr.None {
            bad.Store(1)
            break
        }
        if r.value == 0 {
            break
        }
        for i in 0..Trunc[int](r.value) {
            if buf[i] != FlByte(off + i) {
                bad.Store(1)
            }
        }
        off += Trunc[int](r.value)
        got.Store(Trunc[u32](off))
    }
}

test "RT-File-CancelRead a read cancelled at any moment returns data or Canceled and never moves the position past what it returned" (sys: &System) {
    var nb: [256]u8 = [0; 256]
    name := FlName(mut nb, "cancel", 0)
    size := 262_144
    FlCreate(sys.io, name, size)
    var x := FlRng(s: 0x5DEE_CE66_D1CE_4E5B)
    var buf: [4096]u8 = [0; 4096]
    for round in 0..100 {
        FlRoute(round % 2 == 1)
        o := NativeOpen(sys.io, name, 0)
        Assert(o.err == core.SysErr.None)
        h := Trunc[uint](o.value)
        got := raw.Atomic[u32].New(0)
        bad := raw.Atomic[u32].New(0)
        spin := x.Next() % 20_000
        scope s := sys.io.Scope() {
            s.go FlReader(sys.io, h, x.Next(), &got, &bad)
            var k: u64 = 0
            for k < spin {
                k += 1
            }
            if round % 3 == 0 {
                YieldNow()
            }
            s.Cancel()
        }
        Assert(bad.Load() == 0)
        // The rest, at the stream position, starts exactly where the reader
        // stopped: a Canceled read moved nothing and lost nothing.
        var off := Trunc[int](got.Load())
        for {
            r := NativeRead(sys.io.NoCancel(), h, mut buf[..], -1)
            Assert(r.err == core.SysErr.None)
            if r.value == 0 {
                break
            }
            for i in 0..Trunc[int](r.value) {
                Assert(buf[i] == FlByte(off + i))
            }
            off += Trunc[int](r.value)
        }
        Assert(off == size)
        Assert(NativeClose(sys.io, h).err == core.SysErr.None)
        // Every entry of a cancelled scope is Canceled before it starts.
        scope s2 := sys.io.Scope() {
            s2.Cancel()
            Assert(NativeOpen(sys.io, name, 0).err == core.SysErr.Canceled)
            Assert(NativeRemove(sys.io, name).err == core.SysErr.Canceled)
            Assert(NativeRename(sys.io, name, name).err == core.SysErr.Canceled)
        }
    }
    FlRoute(false)
    Assert(NativeRemove(sys.io, name).err == core.SysErr.None)
}

// FlWorker writes its own file in random chunks at the stream position,
// reads it back at the stream position and at random offsets, and adds 1
// to ok when every byte matched.
FlWorker(io: Io, k: int, ok: &raw.Atomic[u32]) {
    var nb: [256]u8 = [0; 256]
    name := FlName(mut nb, "mn", k)
    size := 65_536
    var x := FlRng(s: 0xD129_0C4B_F1F4_8E35 ^ (Trunc[u64](k + 1) << 20))
    o := NativeOpen(io, name, 1)
    Assert(o.err == core.SysErr.None)
    h := Trunc[uint](o.value)
    var buf: [2048]u8 = [0; 2048]
    var off := 0
    for off < size {
        var n := 1 + Trunc[int](x.Next() % 2048)
        if off + n > size {
            n = size - off
        }
        for i in 0..n {
            buf[i] = FlByte(off + i + k)
        }
        w := NativeWrite(io, h, buf[0..n], -1)
        Assert(w.err == core.SysErr.None && w.value == i64(n))
        off += n
    }
    var good := true
    for _ in 0..200 {
        pos := Trunc[int](x.Next() % Trunc[u64](size))
        n := 1 + Trunc[int](x.Next() % 2048)
        r := NativeRead(io, h, mut buf[0..n], i64(pos))
        var want := size - pos
        if want > n {
            want = n
        }
        if r.err != core.SysErr.None || r.value != i64(want) {
            good = false
        } else {
            for i in 0..want {
                if buf[i] != FlByte(pos + i + k) {
                    good = false
                }
            }
        }
    }
    Assert(NativeClose(io, h).err == core.SysErr.None)
    r2 := NativeOpen(io, name, 0)
    Assert(r2.err == core.SysErr.None)
    h2 := Trunc[uint](r2.value)
    off = 0
    for {
        n := 1 + Trunc[int](x.Next() % 2048)
        r := NativeRead(io, h2, mut buf[0..n], -1)
        if r.err != core.SysErr.None {
            good = false
            break
        }
        if r.value == 0 {
            break
        }
        for i in 0..Trunc[int](r.value) {
            if buf[i] != FlByte(off + i + k) {
                good = false
            }
        }
        off += Trunc[int](r.value)
    }
    Assert(NativeClose(io, h2).err == core.SysErr.None)
    Assert(NativeRemove(io, name).err == core.SysErr.None)
    if good && off == size {
        _ = ok.Add(1)
    }
}

test "RT-MN-FileTasks 32 tasks each write, read back and remove their own file at once" (sys: &System) {
    for pool in 0..2 {
        FlRoute(pool == 1)
        ok := raw.Atomic[u32].New(0)
        scope s := sys.io.Scope() {
            for k in 0..32 {
                s.go FlWorker(sys.io, k, &ok)
            }
        }
        Assert(ok.Load() == 32)
    }
    FlRoute(false)
}

// FlBenchReads times n reads of 4 KiB at positions across a 1 MiB file
// that is in the page cache, on the current route.
FlBenchReads(io: Io, name: str, n: int): i64 {
    o := NativeOpen(io, name, 0)
    Assert(o.err == core.SysErr.None)
    h := Trunc[uint](o.value)
    var buf: [4096]u8 = [0; 4096]
    for i in 0..256 {
        Assert(NativeRead(io, h, mut buf[..], i64(i) * 4096).value == 4096)
    }
    t0 := BenchNow()
    for i in 0..n {
        r := NativeRead(io, h, mut buf[..], i64(i % 256) * 4096)
        Assert(r.value == 4096)
    }
    ns := BenchNow() - t0
    Assert(NativeClose(io, h).err == core.SysErr.None)
    return ns
}

test "RT-Bench-File 20,000 cached 4 KiB reads on this backend's file route and on the pool" (sys: &System) {
    var nb: [256]u8 = [0; 256]
    name := FlName(mut nb, "bench", 0)
    FlCreate(sys.io, name, 1_048_576)
    FlRoute(false)
    poller := FlBenchReads(sys.io, name, 20_000)
    FlRoute(true)
    pool := FlBenchReads(sys.io, name, 20_000)
    FlRoute(false)
    Assert(NativeRemove(sys.io, name).err == core.SysErr.None)
    var out := sys.Stdout()
    BenchLine(mut out, "file-read-route", 20_000, poller)
    BenchLine(mut out, "file-read-pool", 20_000, pool)
    out.Flush() else {
        return
    }
}

// FlDevice is the null device's name on this target (iocp is Windows).
FlDevice(): str {
    return if FlBackend() == 1 { "NUL" } else { "/dev/null" }
}

test "RT-File-Device the null device opened by name: the port refuses it on iocp and the reopen serves it from the pool; every route reads, writes and closes it" (sys: &System) {
    base := FlHandles()
    for pool in 0..2 {
        FlRoute(pool == 1)
        for mode in 0..2 {
            for _ in 0..50 {
                o := NativeOpen(sys.io, FlDevice(), Trunc[u32](mode))
                Assert(o.err == core.SysErr.None)
                h := Trunc[uint](o.value)
                // io_uring attaches what it opened; iocp's HandleKind is 2
                // (a character device), so NativeOpen closes and reopens it
                // synchronous; epoll and the switch use the pool.
                Assert(FlAsync(h) == (pool == 0 && FlBackend() == 2))
                var buf: [16]u8 = [7; 16]
                r := NativeRead(sys.io, h, mut buf[..], -1)
                Assert(r.err == core.SysErr.None && r.value == 0)
                Assert(buf[0] == 7)
                if mode == 1 {
                    w := NativeWrite(sys.io, h, "hello".AsBytes(), -1)
                    Assert(w.err == core.SysErr.None && w.value == 5)
                }
                Assert(NativeClose(sys.io, h).err == core.SysErr.None)
            }
        }
    }
    FlRoute(false)
    // 200 opens and closes: no OS handle left behind by the refused attach.
    Assert(FlHandles() <= base + 8)
}

// FlCarrier is the index of the carrier running the calling task.
FlCarrier(): u32 {
    unsafe "the running task's carrier record" {
        return (*Here()).index
    }
}

test "RT-File-SameCarrier a task's file reads on the port wake no other carrier: a completion is resumed where it was dequeued, with no PollWake" (sys: &System) {
    var nb: [256]u8 = [0; 256]
    name := FlName(mut nb, "same", 0)
    FlCreate(sys.io, name, 65_536)
    FlRoute(false)
    o := NativeOpen(sys.io, name, 0)
    Assert(o.err == core.SysErr.None)
    h := Trunc[uint](o.value)
    var buf: [4096]u8 = [0; 4096]
    var moved := 0
    w0 := BenchWakes()
    for i in 0..2_000 {
        c0 := FlCarrier()
        r := NativeRead(sys.io, h, mut buf[..], i64(i % 16) * 4096)
        Assert(r.err == core.SysErr.None && r.value == 4096)
        Assert(buf[17] == FlByte((i % 16) * 4096 + 17))
        if FlCarrier() != c0 {
            moved += 1
        }
    }
    wakes := Trunc[i64](BenchWakes() - w0)
    // Both counts on stderr (shown with -v): the reads that resumed on
    // another carrier, and WakeOne's PollWakes during the reads.
    FNote("same-carrier reads moved", i64(moved))
    FNote("same-carrier wakes posted", wakes)
    Assert(NativeClose(sys.io, h).err == core.SysErr.None)
    Assert(NativeRemove(sys.io, name).err == core.SysErr.None)
    // iocp only: every cached overlapped read goes pending (measured), and the port hands a completion to its most recent
    // waiter. The property is no spurious wake: the carrier that
    // dequeues a completion readies the task into its own runnext and wakes
    // nobody, so the reads post no PollWake. The old hand-off (mutant W,
    // Found waking a carrier unconditionally) posts one for most reads, and
    // that woken carrier, the most recent waiter, takes the next completion.
    // The wake count is asserted, not the moved count (flake of 2026-11-09):
    // the moved count also depends on whether the parking carrier reaches
    // PollWait before its read completes. When it does not, another idle
    // carrier dequeues it, and the two carriers can stay in that alternation
    // for the whole run. Measured (2026-11-09, Windows,
    // a development desktop in use, TestRtSched_TestBlocks, 10 runs at 1, 2, 4
    // and 8 carriers, dev and release): with this code 5 of 80 runs moved
    // 1,962 to 1,973 of 2,000 reads, all in dev, the other 75 moved 0, and
    // all 80 posted 0 wakes; with Linger turned off (a mutant), 11 of 80
    // moved, again with 0 wakes; mutant W posted 1,021 to 1,898 wakes at 2,
    // 4 and 8 carriers (two runs each, dev and release) and fails here.
    if FlBackend() == 1 {
        Assert(wakes <= 100)
    }
}
