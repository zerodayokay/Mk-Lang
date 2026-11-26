// Why: std.sim's core promise is that one seed gives one run of a world
// with a disk, a clock, a random stream and a network; these blocks pin
// each part of that world and the recorder.
// What: blocks on writes, pages and syncs, Restart, the namespace,
// FailNext, the crash recipe over 1,000 seeds, the clock, the random
// stream, streams and datagrams, record and replay, and corrupt logs.
// How: each block makes its own Sim and frees it, so it leaks nothing (the
// test heap's verdict). The scheduler's blocks are in tests_tasks.mk, the
// faults' in tests_faults.mk; mk test --std=std.sim runs them all.

import std.core
import std.fs
import std.net
import std.raw
import std.time

// SfName is e's member name, for asserting which error a call failed with.
SfName(e: fs.Error | OutOfMemory): str {
    match e {
        fs.NotFound {
            return "NotFound"
        }
        fs.Exists {
            return "Exists"
        }
        fs.Denied {
            return "Denied"
        }
        fs.IsDirectory {
            return "IsDirectory"
        }
        fs.NotDirectory {
            return "NotDirectory"
        }
        fs.NoSpace {
            return "NoSpace"
        }
        fs.InvalidPath {
            return "InvalidPath"
        }
        fs.Unsupported {
            return "Unsupported"
        }
        Utf8Error {
            return "Utf8Error"
        }
        fs.Os(code) {
            if code == 39 {
                return "NotEmpty"
            }
            return "Os"
        }
        OutOfMemory {
            return "OutOfMemory"
        }
        Canceled {
            return "Canceled"
        }
    }
}

// SnName is e's member name for std.net.
SnName(e: net.Error): str {
    return match e {
        Canceled { "Canceled" }
        net.Refused { "Refused" }
        net.Reset { "Reset" }
        _ { "other" }
    }
}

// Put is the cookbook's: create, write, sync, close.
Put(io: Io, key: str, value: str): fs.Error!void {
    var f := try fs.Create(io, key)
    onerr f.Abandon()
    try f.WriteAll(io, value.AsBytes())
    try f.Sync(io)
    try f.Close(io)
}

// PutNoSync writes and closes with no sync.
PutNoSync(io: Io, key: str, value: str): fs.Error!void {
    var f := try fs.Create(io, key)
    onerr f.Abandon()
    try f.WriteAll(io, value.AsBytes())
    try f.Close(io)
}

test "SIM write, read back and size" (sys: &System) {
    var s := try Sim.New(sys.arena, 1)
    defer s.Free()
    io := s.Io()
    try fs.WriteAll(io, "a", "hello, world".AsBytes())
    text := try fs.ReadText(sys.arena, io, "a")
    Assert(text == "hello, world")
    var f := try fs.Open(io, "a")
    onerr f.Abandon()
    var buf: [5]u8 = [0; 5]
    n := f.ReadAt(io, 7, mut buf[..]) else {
        f.Abandon()
        Assert(false)
        return
    }
    Assert(n == 5)
    Assert(str.From(buf[..]).Must() == "world")
    m := f.ReadAt(io, 100, mut buf[..]) else {
        f.Abandon()
        Assert(false)
        return
    }
    Assert(m == 0)
    try f.Close(io)
}

test "SIM writes across pages and holes read as zeros" (sys: &System) {
    var s := try Sim.New(sys.arena, 2)
    defer s.Free()
    io := s.Io()
    var f := try fs.Create(io, "big")
    onerr f.Abandon()
    var data: [10000]u8 = [7; 10000]
    _ = try f.WriteAt(io, 3000, data[..])
    var buf: [13000]u8 = [1; 13000]
    n := try f.ReadAt(io, 0, mut buf[..])
    Assert(n == 13000)
    Assert(buf[0] == 0 && buf[2999] == 0 && buf[3000] == 7 && buf[12999] == 7)
    try f.Close(io)
}

test "SIM unsynced data is gone after Restart and synced data stays" (sys: &System) {
    var s := try Sim.New(sys.arena, 3)
    defer s.Free()
    io := s.Io()
    try Put(io, "kept", "synced")
    try PutNoSync(io, "lost", "not synced")
    io2 := s.Restart()
    Assert(try fs.ReadText(sys.arena, io2, "kept") == "synced")
    // The namespace is durable at once, the contents only at sync.
    Assert(try fs.ReadText(sys.arena, io2, "lost") == "")
}

test "SIM a sync shares pages and a later write copies them" (sys: &System) {
    var s := try Sim.New(sys.arena, 4)
    defer s.Free()
    io := s.Io()
    try Put(io, "f", "first version")
    var f := try fs.Open(io, "f")
    f.Abandon()
    var g := try fs.Create(io, "f")
    onerr g.Abandon()
    try g.WriteAll(io, "second".AsBytes())
    try g.Close(io)
    Assert(try fs.ReadText(sys.arena, io, "f") == "second")
    io2 := s.Restart()
    Assert(try fs.ReadText(sys.arena, io2, "f") == "first version")
}

test "SIM the old run's Io and a freed Sim's Io answer Canceled" (sys: &System) {
    var s := try Sim.New(sys.arena, 5)
    io := s.Io()
    _ = s.Restart()
    _ = fs.ReadText(sys.arena, io, "x") else e {
        Assert(SfName(e) == "Canceled")
        io3 := s.Io()
        s.Free()
        _ = fs.ReadText(sys.arena, io3, "x") else e2 {
            Assert(SfName(e2) == "Canceled")
            return
        }
        Assert(false)
        return
    }
    s.Free()
    Assert(false)
}

test "SIM namespace: mkdir, rename over, remove, stat and errors" (sys: &System) {
    var s := try Sim.New(sys.arena, 6)
    defer s.Free()
    io := s.Io()
    Assert(WriteAllErr(io, "d/x", "1") == "NotFound")
    // the mkdir entry directly, so the block sees the Sim's own answer codes
    Assert(SimMkdir(io, "d").err == core.SysErr.None)
    Assert(SimMkdir(io, "d").err == core.SysErr.Exists)
    try fs.WriteAll(io, "d/x", "1".AsBytes())
    try fs.WriteAll(io, "./d//y", "22".AsBytes())
    try fs.Rename(io, "d/y", "d/x")
    Assert(try fs.ReadText(sys.arena, io, "d/x") == "22")
    Assert(ReadErr(sys.arena, io, "d/y") == "NotFound")
    Assert(RemoveErr(io, "d") == "NotEmpty")
    try fs.Rename(io, "d", "e")
    Assert(try fs.ReadText(sys.arena, io, "e/x") == "22")
    try fs.Remove(io, "e/x")
    try fs.Remove(io, "e")
    Assert(ReadErr(sys.arena, io, "e/x") == "NotFound")
}

test "SIM FailNext fails the next operation of its kind once" (sys: &System) {
    var s := try Sim.New(sys.arena, 7)
    defer s.Free()
    io := s.Io()
    // each armed call must fail (the name is "" on success).
    s.FailNext(Op.Open, Fault.Denied)
    Assert(WriteAllErr(io, "a", "x") == "Denied")
    try fs.WriteAll(io, "a", "x".AsBytes())
    s.FailNext(Op.Write, Fault.NoSpace)
    Assert(WriteAllErr(io, "b", "y") == "NoSpace")
    s.FailNext(Op.Sync, Fault.NoSpace)
    Assert(PutErr(io, "c", "z") == "NoSpace")
    try Put(io, "c", "z")
    s.FailNext(Op.Read, Fault.Denied)
    Assert(ReadErr(sys.arena, io, "c") == "Denied")
    Assert(try fs.ReadText(sys.arena, io, "c") == "z")
}

test "SIM the cookbook crash recipe holds over 1,000 seeds, and some seeds crash" (sys: &System) {
    var crashed := 0
    var whole := 0
    for seed in 0..1000 {
        var s := try Sim.New(sys.arena, Cast[u64](seed))
        defer s.Free()
        s.CrashAfterRandomOps()
        var ok := true
        Put(s.Io(), "key", "value") else {
            ok = false
        }
        if !ok {
            crashed += 1
        }
        io := s.Restart()
        text := fs.ReadText(sys.arena, io, "key") else {
            continue
        }
        Assert(text == "value" || text == "")
        if text == "value" {
            whole += 1
        }
    }
    Assert(crashed > 0 && whole > 0)
}

test "SIM the clock: Sleep and Advance move it, I/O takes time" (sys: &System) {
    var s := try Sim.New(sys.arena, 8)
    defer s.Free()
    io := s.Io()
    t0 := time.Now(io)
    try io.Sleep(time.Seconds(5))
    t1 := time.Now(io)
    d := t1.Since(t0).Nanos()
    Assert(d >= 5_000_000_000 && d < 5_001_000_000)
    s.Advance(time.Millis(250))
    t2 := time.Now(io)
    Assert(t2.Since(t1).Nanos() >= 250_000_000)
    try fs.WriteAll(io, "a", "x".AsBytes())
    Assert(time.Now(io).Since(t2).Nanos() > 1_000)
}

test "SIM one seed gives one random stream, another seed another" (sys: &System) {
    var a: [32]u8 = [0; 32]
    var b: [32]u8 = [0; 32]
    var c: [32]u8 = [0; 32]
    var s1 := try Sim.New(sys.arena, 42)
    defer s1.Free()
    try s1.Io().Random(mut a[..])
    var s2 := try Sim.New(sys.arena, 42)
    defer s2.Free()
    try s2.Io().Random(mut b[..])
    var s3 := try Sim.New(sys.arena, 43)
    defer s3.Free()
    try s3.Io().Random(mut c[..])
    var same := true
    var differ := false
    for i in 0..32 {
        if a[i] != b[i] {
            same = false
        }
        if a[i] != c[i] {
            differ = true
        }
    }
    Assert(same && differ)
}

test "SIM a stream echoes through short reads, and a dial with no listener is Refused" (sys: &System) {
    var s := try Sim.New(sys.arena, 9)
    defer s.Free()
    io := s.Io()
    l := try net.Listen(io, "127.0.0.1:0")
    defer l.Close()
    Assert(l.Port() >= 49152)
    bound := l.Addr()
    Assert(!bound.v6 && bound.ip[0] == 127 && bound.ip[3] == 1)
    Assert(int(bound.port) == l.Port())
    addr := try sys.arena.Fmt("127.0.0.1:{p}", (p: l.Port()))
    c := try net.Dial(io, addr)
    defer c.Close()
    srv := try l.Accept(io)
    defer srv.Close()
    var msg: [1000]u8 = [0; 1000]
    for i in 0..1000 {
        msg[i] = Trunc[u8](i)
    }
    try c.WriteAll(io, msg[..])
    var got: [1000]u8 = [0; 1000]
    var n := 0
    var reads := 0
    for n < 1000 {
        k := try srv.Read(io, mut got[n..])
        Assert(k > 0)
        n += k
        reads += 1
    }
    for i in 0..1000 {
        Assert(got[i] == msg[i])
    }
    Assert(reads >= 1)
    bad := net.Dial(io, "127.0.0.1:1") else e {
        Assert(SnName(e) == "Refused")
        return
    }
    bad.Close()
    Assert(false)
}

test "SIM an injected Reset breaks the connection both ways" (sys: &System) {
    var s := try Sim.New(sys.arena, 10)
    defer s.Free()
    io := s.Io()
    l := try net.Listen(io, "localhost:7000")
    defer l.Close()
    c := try net.Dial(io, "localhost:7000")
    defer c.Close()
    srv := try l.Accept(io)
    defer srv.Close()
    s.FailNext(Op.Write, Fault.Reset)
    _ = c.Write(io, "x".AsBytes()) else e {
        Assert(SnName(e) == "Reset")
    }
    var buf: [4]u8 = [0; 4]
    _ = srv.Read(io, mut buf[..]) else e {
        Assert(SnName(e) == "other" || SnName(e) == "Reset")
        return
    }
}

test "SIM an accept no dial can satisfy is Canceled, not a hang" (sys: &System) {
    var s := try Sim.New(sys.arena, 11)
    defer s.Free()
    io := s.Io()
    l := try net.Listen(io, "127.0.0.1:0")
    defer l.Close()
    got := l.Accept(io) else e {
        Assert(SnName(e) == "Canceled")
        return
    }
    got.Close()
    Assert(false)
}

// RecScript does file, random and clock I/O through io and digests what it
// saw: the same digest means the same answers.
RecScript(a: &Arena, io: Io, p: str): fs.Error|OutOfMemory!u64 {
    try fs.WriteAll(io, p, "recorded".AsBytes())
    text := try fs.ReadText(a, io, p)
    var r: [16]u8 = [0; 16]
    try io.Random(mut r[..])
    var d := FnvBytes(FnvSeed, text.AsBytes())
    d = FnvBytes(d, r[..])
    return FnvI(d, core.IoNow(io).value)
}

// World workload: files, a crash point, a stream and the clock, for the
// determinism test; errors are part of the run, so none ends it.
SimWorkload(io: Io) {
    Put(io, "k1", "v1") else {
    }
    PutNoSync(io, "k2", "v2") else {
    }
    l := net.Listen(io, "127.0.0.1:9000") else {
        return
    }
    defer l.Close()
    c := net.Dial(io, "127.0.0.1:9000") else {
        return
    }
    defer c.Close()
    srv := l.Accept(io) else {
        return
    }
    defer srv.Close()
    c.WriteAll(io, "ping ping ping ping".AsBytes()) else {
    }
    var buf: [64]u8 = [0; 64]
    _ = srv.Read(io, mut buf[..]) else {
    }
    var r: [8]u8 = [0; 8]
    io.Random(mut r[..]) else {
    }
    _ = core.IoNow(io)
}

test "SIM record a native run and replay it with no OS behind it" (sys: &System) {
    p := "mk_tsim_rec"
    var rec := try Recorder.New(sys.io)
    defer rec.Free()
    d1 := try RecScript(sys.arena, rec.Io(), p)
    try fs.Remove(sys.io, p)
    var rp := try Replayer.New(try rec.Log(sys.arena))
    defer rp.Free()
    d2 := try RecScript(sys.arena, rp.Io(), p)
    Assert(d1 == d2)
    Assert(rp.Done() && !rp.Diverged())
    _ = fs.ReadText(sys.arena, sys.io, p) else e {
        Assert(SfName(e) == "NotFound")
        return
    }
    Assert(false)
}

test "SIM a replay that asks something else diverges" (sys: &System) {
    var rec := try Recorder.New(sys.io)
    defer rec.Free()
    _ = try RecScript(sys.arena, rec.Io(), "mk_tsim_div")
    try fs.Remove(sys.io, "mk_tsim_div")
    var rp := try Replayer.New(try rec.Log(sys.arena))
    defer rp.Free()
    _ = RecScript(sys.arena, rp.Io(), "mk_tsim_other") else e {
        Assert(SfName(e) == "Canceled")
    }
    Assert(rp.Diverged())
}

test "SIM one seed gives one run at the Io boundary, byte for byte" (sys: &System) {
    var logs: [3]u64 = [0; 3]
    var lens: [3]int = [0; 3]
    seeds: [3]u64 = [77, 77, 78]
    for i in 0..3 {
        var s := try Sim.New(sys.arena, seeds[i])
        defer s.Free()
        s.CrashAfterRandomOps()
        var rec := try Recorder.New(s.Io())
        defer rec.Free()
        SimWorkload(rec.Io())
        _ = s.Restart()
        log := try rec.Log(sys.arena)
        logs[i] = FnvBytes(FnvSeed, log)
        lens[i] = log.Len()
    }
    Assert(logs[0] == logs[1] && lens[0] == lens[1] && lens[0] > 0)
    Assert(logs[0] != logs[2])
}

// Loop is 127.0.0.1 at port p.
Loop(p: int): core.IoAddr {
    var ip: [16]u8 = [0; 16]
    ip[0] = 127
    ip[3] = 1
    return core.IoAddr(ip: ip, port: Trunc[u16](p), v6: false, zone: 0)
}

test "SIM datagrams arrive whole, may reorder, and a long one is cut" (sys: &System) {
    var s := try Sim.New(sys.arena, 12)
    defer s.Free()
    io := s.Io()
    a := SimUdp(io, Loop(0))
    b := SimUdp(io, Loop(0))
    Assert(a.err == core.SysErr.None && b.err == core.SysErr.None && a.os != b.os)
    ha := Cast[uint](a.value)
    hb := Cast[uint](b.value)
    var one: [1]u8 = [0; 1]
    for i in 0..20 {
        one[0] = Trunc[u8](i)
        Assert(SimSendTo(io, ha, one[..], Loop(Cast[int](b.os))).value == 1)
    }
    var seen: [20]bool = [false; 20]
    var inorder := true
    var from := Loop(0)
    for i in 0..20 {
        r := SimRecvFrom(io, hb, mut one[..], mut from)
        Assert(r.err == core.SysErr.None && r.value == 1 && r.os == 0)
        Assert(int(from.port) == Cast[int](a.os))
        seen[int(one[0])] = true
        if int(one[0]) != i {
            inorder = false
        }
    }
    for i in 0..20 {
        Assert(seen[i])
    }
    Assert(!inorder)
    var ten: [10]u8 = [9; 10]
    _ = SimSendTo(io, ha, ten[..], Loop(Cast[int](b.os)))
    var four: [4]u8 = [0; 4]
    r := SimRecvFrom(io, hb, mut four[..], mut from)
    Assert(r.value == 4 && r.os == 1 && four[3] == 9)
    empty := SimRecvFrom(io, hb, mut four[..], mut from)
    Assert(empty.err == core.SysErr.Canceled)
    Assert(SimClose(io, ha).err == core.SysErr.None && SimClose(io, hb).err == core.SysErr.None)
}

test "SIM Run delivers what is in flight, and resolve knows one host" (sys: &System) {
    var s := try Sim.New(sys.arena, 13)
    defer s.Free()
    io := s.Io()
    l := try net.Listen(io, "127.0.0.1:8080")
    defer l.Close()
    c := try net.Dial(io, "127.0.0.1:8080")
    defer c.Close()
    srv := try l.Accept(io)
    defer srv.Close()
    try c.WriteAll(io, "hello".AsBytes())
    before := core.IoNow(io).value
    s.Run()
    after := core.IoNow(io).value
    Assert(after > before)
    var buf: [5]u8 = [0; 5]
    var n := 0
    for n < 5 {
        n += try srv.Read(io, mut buf[n..])
    }
    // Everything had arrived, so the reads moved the clock by now's 1 us only.
    Assert(core.IoNow(io).value - after < 10_000)
    var out: [2]core.IoAddr = [Loop(0), Loop(0)]
    r := SimResolve(io, "localhost", mut out[..])
    Assert(r.value == 1 && out[0].ip[0] == 127)
    Assert(SimResolve(io, "example.com", mut out[..]).err == core.SysErr.NotFound)
}

test "SIM a directory lists its children once, in creation order" (sys: &System) {
    var s := try Sim.New(sys.arena, 14)
    defer s.Free()
    io := s.Io()
    Assert(SimMkdir(io, "d").err == core.SysErr.None)
    try fs.WriteAll(io, "d/alpha", "1".AsBytes())
    try fs.WriteAll(io, "d/b", "22".AsBytes())
    try fs.WriteAll(io, "top", "3".AsBytes())
    d := SimOpenDir(io, "d")
    Assert(d.err == core.SysErr.None)
    h := Cast[uint](d.value)
    var st := core.IoStat(kind: 0, size: 0, modified: 0)
    var small: [2]u8 = [0; 2]
    short := SimReadDir(io, h, mut small[..], mut st)
    Assert(short.err == core.SysErr.NoSpace && short.value == 5)
    var name: [16]u8 = [0; 16]
    r1 := SimReadDir(io, h, mut name[..], mut st)
    Assert(r1.value == 5 && str.From(name[0..5]).Must() == "alpha" && st.kind == core.IoKindFile && st.size == 1)
    r2 := SimReadDir(io, h, mut name[..], mut st)
    Assert(r2.value == 1 && name[0] == b'b' && st.size == 2)
    Assert(SimReadDir(io, h, mut name[..], mut st).value == 0)
    Assert(SimClose(io, h).err == core.SysErr.None)
    Assert(SimOpenDir(io, "top").err == core.SysErr.NotDirectory)
    Assert(SimOpenDir(io, "nope").err == core.SysErr.NotFound)
}

// A close with no Io (Stream.Close, Listener.Close, File.Abandon)
// reaches the simulation through rt.KindForeign's ops: the peer reads the
// end of the stream, the port is free again, and a handle of an ended run
// or a freed Sim closes safely.
test "SIM a close with no Io ends the stream and frees the port" (sys: &System) {
    var s := try Sim.New(sys.arena, 12)
    defer s.Free()
    io := s.Io()
    addr := "127.0.0.1:7000"
    l := try net.Listen(io, addr)
    onerr l.Close()
    c := try net.Dial(io, addr)
    onerr c.Close()
    srv := try l.Accept(io)
    onerr srv.Close()
    try c.WriteAll(io, "bye".AsBytes())
    c.Close()
    var buf: [16]u8 = [0; 16]
    var n := 0
    for {
        k := try srv.Read(io, mut buf[n..])
        if k == 0 {
            break
        }
        n += k
    }
    Assert(n == 3 && buf[0] == 98 && buf[2] == 101)
    srv.Close()
    l.Close()
    l2 := try net.Listen(io, addr)
    l2.Close()
    f := try fs.Create(io, "a")
    f.Abandon()
    try fs.Remove(io, "a")
    l3 := try net.Listen(io, addr)
    io2 := s.Restart()
    l3.Close()
    l4 := try net.Listen(io2, addr)
    onerr l4.Close()
    var t := try Sim.New(sys.arena, 13)
    onerr t.Free()
    l5 := try net.Listen(t.Io(), addr)
    t.Free()
    l5.Close()
    l4.Close()
}

// CorruptScript reads a file and one directory entry. A log of it whose
// counts were edited diverges; it never makes a replayed call index past
// its caller's buffer.
CorruptScript(a: &Arena, io: Io): fs.Error|OutOfMemory!void {
    _ = try fs.ReadBytes(a, io, "f")
    var d := try fs.Dir.Open(io, ".")
    defer d.Close()
    _ = try d.Next(a, io)
}

test "SIM a replay of a log with corrupt counts diverges and does not panic" (sys: &System) {
    var s := try Sim.New(sys.arena, 33)
    defer s.Free()
    try fs.WriteAll(s.Io(), "f", "hello".AsBytes())
    var rec := try Recorder.New(s.Io())
    defer rec.Free()
    try CorruptScript(sys.arena, rec.Io())
    log := try rec.Log(sys.arena)
    for which in 0..2 {
        var b := try sys.arena.Slice[u8](log.Len())
        for k in 0..log.Len() {
            b[k] = log[k]
        }
        // Walk the records; set the count of the first read (which = 0) or
        // of the first readdir (which = 1) to 100,000.
        var at := 0
        var patched := false
        for at + RecordHeader <= b.Len() {
            n := Cast[int](GetU64(b[..], at + 33) & 0xFFFF_FFFF)
            if !patched && ((which == 0 && b[at] == ORead) || (which == 1 && b[at] == OReadDir)) {
                v: u64 = 100_000
                for k in 0..8 {
                    b[at + 9 + k] = Trunc[u8](v >> Trunc[u64](k * 8))
                }
                patched = true
            }
            at += RecordHeader + n
        }
        Assert(patched)
        var rp := try Replayer.New(b[..])
        defer rp.Free()
        CorruptScript(sys.arena, rp.Io()) else e {
            Assert(SfName(e) == "Canceled")
        }
        Assert(rp.Diverged())
    }
}

// RecLen is the length of rec's log, under its lock.
RecLen(rec: &Recorder): int {
    LockSlot(rec.slot)
    var n := 0
    unsafe "the recorder owns its tape, alive until Free" {
        n = (*rec.t).log.Len()
    }
    UnlockSlot(rec.slot)
    return n
}

// A record that does not fit stops the recording on a record boundary,
// and Log reports it instead of handing out a cut log.
test "SIM a recording that ran out of memory is reported by Log, cut on a record boundary" (sys: &System) {
    var s := try Sim.New(sys.arena, 34)
    defer s.Free()
    var rec := try Recorder.New(s.Io())
    defer rec.Free()
    try fs.WriteAll(rec.Io(), "f", "hello".AsBytes())
    whole := try rec.Log(sys.arena)
    // A payload past MaxVecBytes: its reserve fails before any byte is
    // read or appended, as an exhausted heap's would.
    var huge: []u8 = whole[0..0]
    unsafe "the slice is only measured: the failed reserve reads none of it" {
        huge = raw.Slice(raw.Data(whole), MaxVecBytes)
    }
    r := core.IoResult(value: 0, err: core.SysErr.None, os: 0)
    Post2(rec.Io(), OWrite, 0, FnvSeed, r, whole[0..0], huge)
    // Nothing of the record that failed was appended.
    Assert(RecLen(&rec) == whole.Len())
    _ = rec.Log(sys.arena) else {
        return
    }
    Assert(false)
}

// A directory stream keeps its directory, so its slot is not reused by
// the next mkdir.
test "SIM a stream of a removed directory lists nothing, not another directory" (sys: &System) {
    var s := try Sim.New(sys.arena, 35)
    defer s.Free()
    io := s.Io()
    try fs.MakeDir(io, "d")
    var d := try fs.Dir.Open(io, "d")
    defer d.Close()
    try fs.Remove(io, "d")
    try fs.MakeDir(io, "e")
    try fs.WriteAll(io, "e/x", "1".AsBytes())
    Assert((try d.Next(sys.arena, io)).IsNone())
    var d2 := try fs.Dir.Open(io, "e")
    defer d2.Close()
    Assert(!(try d2.Next(sys.arena, io)).IsNone())
}

test "SIM a Listen recorded through a Recorder replays the same calls whatever Listens ran before it" (sys: &System) {
    var rec := try Recorder.New(sys.io)
    defer rec.Free()
    l := net.Listen(rec.Io(), "127.0.0.1:0") else {
        Panic("the recorded Listen failed")
    }
    l.Close()
    // A native Listen between: net.Listen keeps no state that would make
    // the replayed Listen skip a call the log holds.
    n := net.Listen(sys.io, "127.0.0.1:0") else {
        Panic("the native Listen failed")
    }
    n.Close()
    var rp := try Replayer.New(try rec.Log(sys.arena))
    defer rp.Free()
    m := net.Listen(rp.Io(), "127.0.0.1:0") else {
        Panic("the replayed Listen failed")
    }
    m.Close()
    Assert(rp.Done() && !rp.Diverged())
}

test "SIM a TCP listener saves its bound IPv6 address and port for UDP on the same address" (sys: &System) {
    var s := try Sim.New(sys.arena, 10)
    defer s.Free()
    io := s.Io()
    l := try net.ListenWith(io, "[::1]:0", net.DefaultListenOptions)
    defer l.Close()
    a := l.Addr()
    Assert(a.v6 && a.ip[15] == 1 && int(a.port) == l.Port())
    udp := try net.Bind(io, try sys.arena.Fmt("{a}", (a)))
    defer udp.Close()
    Assert(udp.Port() == l.Port())
}
