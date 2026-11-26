// Why: the prelude's Reader and Writer methods promise a cancel-safety class: a
// repeatable call, interrupted and called again, delivers what an uninterrupted
// run does; a prefix call, interrupted, loses only what it took.
// What: core8Cancel's blocks, TestCore_CancelClasses and TestCore_CancelIsLoadBearing.
// How: a source that fails a chosen fill (Flaky) or a sink that fails a chosen write
// (Rec) reaches each await point in turn, run by `mk test` in std.coreprobe.
//
// Under std.sim a Reader is a task of the run and a second task cancels its
// scope while it is parked on a refill, beside a control run of the same
// seed with no cancel. A std.sim write never waits and does not read the
// scope's cancel flag, so a Writer's interruption under std.sim is a fault
// (Sim.FailNext) and its cancel is tested natively, where every
// cancellable entry checks the flag first (std/task/iotable.mk, Stop).

package std

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// core8Cancel is the cancel-safety test blocks.
const core8Cancel = `import std.core
import std.net
import std.sim

const Story = "alpha one\nbeta two three\ngamma\n\ndelta four five six\nend"

// StoryLines is Story as Line gives it back, a newline after each line.
const StoryLines = "alpha one\nbeta two three\ngamma\n\ndelta four five six\nend\n"

// OneGap reports whether got is want with one contiguous run removed.
OneGap(got: []u8, want: str): bool {
    lost := want.Len() - got.Len()
    if lost < 0 {
        return false
    }
    w := want.AsBytes()
    for g in 0..got.Len() + 1 {
        var ok := true
        for i in 0..got.Len() {
            var j := i
            if i >= g {
                j = i + lost
            }
            if got[i] != w[j] {
                ok = false
                break
            }
        }
        if ok {
            return true
        }
    }
    return false
}

// Expected is how many fills or writes failed when call fail_at of calls
// was the one to fail: 1 when that call was made, else 0.
Expected(fail_at: int, calls: int): int {
    return if fail_at >= 1 && fail_at <= calls { 1 } else { 0 }
}

test "Reader.Read on a buffered reader is repeatable at every fill, the direct read included" (sys: &System) {
    for k in 0..40 {
        for chunk in 1..6 {
            for want in [3, 7, 9] {
                var f := Flaky(data: Story, pos: 0, chunk: chunk, fail_at: k, calls: 0)
                var b: [7]u8 = [0; 7]
                var r := f.Reader(sys.io, mut b[..])
                var out: [128]u8 = [0; 128]
                var got := 0
                var fails := 0
                for {
                    // want >= 7 reads straight into d (Go bufio's rule).
                    var d: [9]u8 = [0; 9]
                    n := r.Read(mut d[0..want]) else {
                        fails += 1
                        continue
                    }
                    if n == 0 {
                        break
                    }
                    (mut out[got..got + n]).CopyFrom(d[0..n])
                    got += n
                }
                Assert(Same(out[0..got], Story))
                Assert(fails == Expected(k, f.calls))
            }
        }
    }
}

test "Reader.Line on a buffered reader is repeatable at every fill" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    for k in 0..60 {
        for chunk in 1..5 {
            var f := Flaky(data: Story, pos: 0, chunk: chunk, fail_at: k, calls: 0)
            var b: [24]u8 = [0; 24]
            var r := f.Reader(sys.io, mut b[..])
            var t := Text.New(&ar)
            var fails := 0
            for {
                l := r.Line(&ar) else {
                    fails += 1
                    continue
                }
                s := l else {
                    break
                }
                try t.Push(s)
                try t.Push("\n")
            }
            Assert(t.Str() == StoryLines)
            Assert(fails == Expected(k, f.calls))
            ar.Reset()
        }
    }
}

// Fit has two lines exactly as long as an 8-byte buffer: Line reads the
// one probe byte after each (text_text.mk, room == 0).
const Fit = "abcdefgh\nij\nklmnopqr\n"

// Probe is Flaky that also records the size of the buffer the failed fill
// was given.
struct Probe {
    data: str
    pos: int
    chunk: int
    fail_at: int
    calls: int
    failed_len: int
}

ProbeFill(f: mut Probe, io: Io, dst: mut []u8): ReadError!int {
    f.calls += 1
    if f.calls == f.fail_at {
        f.failed_len = dst.Len()
        fail ReadError
    }
    var n := f.data.Len() - f.pos
    if n > f.chunk {
        n = f.chunk
    }
    if n > dst.Len() {
        n = dst.Len()
    }
    (mut dst[0..n]).CopyFrom(f.data.AsBytes()[f.pos..f.pos + n])
    f.pos += n
    return n
}

const ProbePort = core.ReadPortMut[Probe].New(ProbeFill)

test "Reader.Line is repeatable at the probe read of a full buffer" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    for k in 0..30 {
        for chunk in 1..10 {
            var f := Probe(data: Fit, pos: 0, chunk: chunk, fail_at: k, calls: 0, failed_len: 0)
            var b: [8]u8 = [0; 8]
            var r := (&ProbePort).Reader(sys.io, mut f, mut b[..])
            var t := Text.New(&ar)
            var fails := 0
            for {
                l := r.Line(&ar) else {
                    fails += 1
                    continue
                }
                s := l else {
                    break
                }
                try t.Push(s)
                try t.Push("\n")
            }
            Assert(t.Str() == Fit)
            Assert(fails == Expected(k, f.calls))
            ar.Reset()
        }
    }
    // With chunk 8 the first fill takes the whole first line, so fill 2 is
    // the probe: a 1-byte read into a full buffer with no newline.
    var f := Probe(data: Fit, pos: 0, chunk: 8, fail_at: 2, calls: 0, failed_len: 0)
    var b: [8]u8 = [0; 8]
    var r := (&ProbePort).Reader(sys.io, mut f, mut b[..])
    var probed := false
    _ = r.Line(&ar) else {
        probed = true
    }
    Assert(probed)
    Assert((try r.Line(&ar)).Must() == "abcdefgh")
    Assert((try r.Line(&ar)).Must() == "ij")
    Assert(f.failed_len == 1 && f.pos >= 8)
    // A line one byte longer than the buffer is ReadError.
    var g := Probe(data: "abcdefghi\n", pos: 0, chunk: 4, fail_at: 0, calls: 0, failed_len: 0)
    var gb: [8]u8 = [0; 8]
    var gr := (&ProbePort).Reader(sys.io, mut g, mut gb[..])
    var long := false
    _ = gr.Line(&ar) else {
        long = true
    }
    Assert(long)
}

test "on an unbuffered reader Read is repeatable, and Line and Until are prefix: an interrupted call loses only what it took" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    for k in 0..70 {
        var f := Flaky(data: Story, pos: 0, chunk: 4, fail_at: k, calls: 0)
        var nobuf: [0]u8 = []
        var r := f.Reader(sys.io, mut nobuf[..])
        var t := Text.New(&ar)
        var fails := 0
        for {
            l := r.Line(&ar) else {
                fails += 1
                continue
            }
            s := l else {
                break
            }
            try t.Push(s)
            try t.Push("\n")
        }
        // ReadLineBytewise has copied the line's first bytes out of the
        // source when a later byte's fill fails: those bytes are lost, and
        // the next Line gives the rest of the line.
        Assert(OneGap(t.Str().AsBytes(), StoryLines))
        Assert(StoryLines.Len() - t.Str().Len() <= 19)
        Assert(fails == Expected(k, f.calls))
        ar.Reset()
    }
    // Read stays repeatable unbuffered: each call is one fill straight into
    // the caller's bytes, and a failed fill took nothing.
    for k in 0..70 {
        var f := Flaky(data: Story, pos: 0, chunk: 4, fail_at: k, calls: 0)
        var nobuf: [0]u8 = []
        var r := f.Reader(sys.io, mut nobuf[..])
        var out: [128]u8 = [0; 128]
        var got := 0
        var fails := 0
        for {
            var d: [3]u8 = [0; 3]
            n := r.Read(mut d[..]) else {
                fails += 1
                continue
            }
            if n == 0 {
                break
            }
            (mut out[got..got + n]).CopyFrom(d[0..n])
            got += n
        }
        Assert(Same(out[0..got], Story))
        Assert(fails == Expected(k, f.calls))
    }
    for delim in [0x20, 0x0A] {
        for k in 0..70 {
            var f := Flaky(data: Story, pos: 0, chunk: 4, fail_at: k, calls: 0)
            var nobuf: [0]u8 = []
            var r := f.Reader(sys.io, mut nobuf[..])
            var out: [128]u8 = [0; 128]
            var got := 0
            var fails := 0
            for {
                var d: [32]u8 = [0; 32]
                n := r.Until(Trunc[u8](delim), mut d[..]) else {
                    fails += 1
                    continue
                }
                if n == 0 {
                    break
                }
                (mut out[got..got + n]).CopyFrom(d[0..n])
                got += n
            }
            Assert(OneGap(out[0..got], Story))
            Assert(fails == Expected(k, f.calls))
        }
    }
}

test "Reader.Until and ReadAll are prefix: an interrupted call loses only what it took" (sys: &System) {
    for k in 0..40 {
        var f := Flaky(data: Story, pos: 0, chunk: 3, fail_at: k, calls: 0)
        var b: [8]u8 = [0; 8]
        var r := f.Reader(sys.io, mut b[..])
        var out: [128]u8 = [0; 128]
        var got := 0
        var fails := 0
        for {
            var d: [32]u8 = [0; 32]
            n := r.Until(0x20, mut d[..]) else {
                fails += 1
                continue
            }
            if n == 0 {
                break
            }
            (mut out[got..got + n]).CopyFrom(d[0..n])
            got += n
        }
        Assert(OneGap(out[0..got], Story))
        Assert(fails == Expected(k, f.calls))
    }
    for k in 0..40 {
        var f := Flaky(data: Story, pos: 0, chunk: 3, fail_at: k, calls: 0)
        var b: [8]u8 = [0; 8]
        var r := f.Reader(sys.io, mut b[..])
        var out: [128]u8 = [0; 128]
        var got := 0
        var fails := 0
        for {
            var d: [10]u8 = [0; 10]
            n := r.ReadAll(mut d[..]) else {
                fails += 1
                continue
            }
            if n == 0 {
                break
            }
            (mut out[got..got + n]).CopyFrom(d[0..n])
            got += n
        }
        Assert(OneGap(out[0..got], Story))
        Assert(fails == Expected(k, f.calls))
    }
}

// WritePiece writes part through w by Str, Bytes or Print, chosen by the
// piece's number, then flushes, and returns how many of the two calls
// failed; it goes on after a failure, as a caller that closes the stream
// afterwards does (prefix).
WritePiece(w: mut Writer, piece: int, part: str): int {
    var fails := 0
    if piece % 3 == 0 {
        w.Str(part) else {
            fails += 1
        }
    } else if piece % 3 == 1 {
        w.Bytes(part.AsBytes()) else {
            fails += 1
        }
    } else {
        Print(w, "{s}", (s: part)) else {
            fails += 1
        }
    }
    w.Flush() else {
        fails += 1
    }
    return fails
}

// Pieces is how many 5-byte pieces Story is written in.
const Pieces = 11

// WritePieces writes Story through w in 5-byte pieces and returns how many
// calls failed.
WritePieces(w: mut Writer): int {
    var fails := 0
    for piece in 0..Pieces {
        i := piece * 5
        var n := 5
        if i + n > Story.Len() {
            n = Story.Len() - i
        }
        fails += WritePiece(w, piece, Story[i..i + n])
    }
    return fails
}

test "every Writer method over a stream is prefix: an interrupted write loses one run" (sys: &System) {
    Assert((Pieces - 1) * 5 < Story.Len() && Story.Len() <= Pieces * 5)
    for k in 0..40 {
        for fk in 0..3 {
            var rec := NewRec(k, fk * 4)
            var b: [7]u8 = [0; 7]
            var w := (&RecPort).Writer(sys.io, mut rec, mut b[..])
            fails := WritePieces(mut w)
            // A failed write drops the run it was given and what the buffer
            // held, nothing else; a failed flush drops nothing.
            Assert(OneGap(rec.data[0..rec.len], Story))
            Assert(Story.Len() - rec.len <= 12)
            Assert(fails == Expected(k, rec.calls) + Expected(fk * 4, rec.flushes))
        }
    }
}

// WriteSizes writes Story through w in pieces whose sizes cycle through
// sizes, by Str and Bytes in turn, with no Flush between them, then
// flushes; it returns how many calls failed.
WriteSizes(w: mut Writer, sizes: []int): int {
    var i := 0
    var piece := 0
    var fails := 0
    for i < Story.Len() {
        var n := sizes[piece % sizes.Len()]
        if i + n > Story.Len() {
            n = Story.Len() - i
        }
        part := Story[i..i + n]
        if piece % 2 == 0 {
            w.Str(part) else {
                fails += 1
            }
        } else {
            w.Bytes(part.AsBytes()) else {
                fails += 1
            }
        }
        piece += 1
        i += n
    }
    w.Flush() else {
        fails += 1
    }
    return fails
}

// WriteSizesCase runs WriteSizes over a 7-byte buffer into a Rec that
// fails write k, and checks the prefix state.
WriteSizesCase(io: Io, k: int, sizes: []int) {
    var rec := NewRec(k, 0)
    var b: [7]u8 = [0; 7]
    var w := (&RecPort).Writer(io, mut rec, mut b[..])
    fails := WriteSizes(mut w, sizes)
    // A failed write loses what the buffer held and the run being written,
    // at most the buffer and the longest piece: one gap.
    Assert(OneGap(rec.data[0..rec.len], Story))
    Assert(Story.Len() - rec.len <= 7 + 13)
    Assert(fails == Expected(k, rec.calls))
}

test "a Writer's direct and top-up writes are prefix: an interrupted one loses one run" (sys: &System) {
    // 9 into an empty buffer: the direct write. 3 then 13: BytesSlow's
    // top-up drain, then the rest (9 > 7) written directly. 3 then 9: the
    // top-up drain, then the rest (5) buffered.
    for k in 0..30 {
        WriteSizesCase(sys.io, k, [9, 3, 13, 3, 9][..])
        WriteSizesCase(sys.io, k, [13, 3][..])
        WriteSizesCase(sys.io, k, [3, 3, 9][..])
    }
}

// SimStory is what a peer sends over a loopback stream under std.sim.
const SimStory = "first line\nsecond line is longer\nthird\n"

// SimFit has lines of at most 10 bytes, the first exactly 10: over a
// 10-byte buffer Line reads a probe byte after it.
const SimFit = "first line\nsecondone\nthird\n"

// SimPeer sends rest on srv after nap ns of simulated time, then closes
// srv: the reader's refill waits for it.
SimPeer(io: Io, srv: net.Stream, rest: str, nap: i64) {
    io.Sleep(core.DurationOf(nap)) else {
        srv.Close()
        return
    }
    srv.WriteAll(io, rest.AsBytes()) else {
    }
    srv.Close()
}

// SimCancel cancels s after nap ns of simulated time.
SimCancel(s: &Scope, io: Io, nap: i64) {
    io.Sleep(core.DurationOf(nap)) else {
        return
    }
    s.Cancel()
}

// SimOut is what a reader task delivered: its bytes, how many calls were
// interrupted under the cancelled scope (cuts) and after it (late), and
// whether a call misbehaved (bad).
struct SimOut {
    data: [64]u8
    n: int
    cuts: int
    late: int
    bad: bool
}

// SimStep makes one call of mode's operation (0 Line, 1 Until on a space,
// 2 ReadAll, 3 Read) asking for want bytes, and copies what it gave into
// dst (a line with its newline); 0 at the end of input.
SimStep(r: mut Reader, a: &Arena, mode: int, want: int, dst: mut []u8): ReadError|OutOfMemory!int {
    if mode == 0 {
        line := try r.Line(a)
        s := line else {
            return 0
        }
        (mut dst[0..s.Len()]).CopyFrom(s.AsBytes())
        dst[s.Len()] = 0x0A
        return s.Len() + 1
    }
    var d: [24]u8 = [0; 24]
    var k := 0
    if mode == 1 {
        k = try r.Until(0x20, mut d[0..want])
    } else if mode == 2 {
        k = try r.ReadAll(mut d[0..want])
    } else {
        k = try r.Read(mut d[0..want])
    }
    (mut dst[0..k]).CopyFrom(d[0..k])
    return k
}

// SimReader is the reader task: it reads c to its end through a Reader
// with a cap-byte buffer by mode's operation, under a scope that another
// task cancels at cancel_at ns (none when 0), then after that scope, and
// stores what it delivered in out.
SimReader(io: Io, h: &Heap, c: net.Stream, mode: int, cap: int, want: int, cancel_at: i64, out: &Mutex[SimOut]) {
    var ar := Arena.New(h) else {
        c.Close()
        return
    }
    var b: [24]u8 = [0; 24]
    var r := c.Reader(io, mut b[0..cap])
    var got: [64]u8 = [0; 64]
    var n := 0
    var cuts := 0
    var late := 0
    var done := false
    var bad := false
    scope inner := io.Scope() {
        if cancel_at > 0 {
            inner.go SimCancel(&inner, io, cancel_at)
        }
        for !done && !bad {
            k := SimStep(mut r, &ar, mode, want, mut got[n..]) else {
                cuts += 1
                break
            }
            if k == 0 {
                done = true
            }
            n += k
        }
    }
    for !done && !bad {
        k := SimStep(mut r, &ar, mode, want, mut got[n..]) else {
            late += 1
            bad = true
            break
        }
        if k == 0 {
            done = true
        }
        n += k
    }
    ar.Free()
    c.Close()
    var g := out.Lock(io) else {
        return
    }
    o := g.Mut()
    (mut o.data[0..n]).CopyFrom(got[0..n])
    o.n = n
    o.cuts = cuts
    o.late = late
    o.bad = bad
    g.Unlock()
}

// SimRun sends story over a loopback stream of a Sim seeded seed: its
// first p bytes at once, the rest from a peer task 2 ms in; a reader task
// reads it as SimReader does. It returns what the reader delivered.
SimRun(a: &Arena, h: &Heap, seed: u64, story: str, p: int, mode: int, cap: int, want: int, cancel_at: i64): OutOfMemory|net.Error!SimOut {
    var sm := try sim.Sim.New(a, seed)
    defer sm.Free()
    io := sm.Io()
    l := try net.Listen(io, "127.0.0.1:7531")
    defer l.Close()
    c := try net.Dial(io, "127.0.0.1:7531")
    srv := l.Accept(io) else e {
        c.Close()
        fail e
    }
    srv.WriteAll(io, story[0..p].AsBytes()) else e {
        srv.Close()
        c.Close()
        fail e
    }
    out := Mutex[SimOut].New(SimOut(data: [0; 64], n: 0, cuts: -1, late: 0, bad: true))
    scope outer := io.Scope() {
        outer.go SimPeer(io, srv, story[p..], 2_000_000)
        outer.go SimReader(io, h, c, mode, cap, want, cancel_at, &out)
    }
    return out.Into()
}

// SimCase checks one configuration at every split point p of story, with
// no cancel (the control: nothing is interrupted, every byte arrives),
// with the cancel while the reader waits for the peer (1 ms: interrupted
// exactly once) and with cancels about when the peer's bytes arrive (at
// most once). A repeatable mode delivers story every time, a prefix one
// story with one run removed.
SimCase(a: &Arena, h: &Heap, story: str, mode: int, cap: int, want: int, repeatable: bool): OutOfMemory|net.Error!void {
    for p in 0..story.Len() + 1 {
        for cancel_at in [0, 1_000_000, 2_000_000, 2_100_000, 2_300_000, 2_600_000] {
            o := try SimRun(a, h, Cast[u64](700 + p), story, p, mode, cap, want, i64(cancel_at))
            Assert(!o.bad && o.late == 0)
            if cancel_at == 0 {
                Assert(o.cuts == 0)
            } else if cancel_at == 1_000_000 {
                Assert(o.cuts == 1)
            } else {
                Assert(o.cuts <= 1)
            }
            if repeatable || o.cuts == 0 {
                Assert(Same(o.data[0..o.n], story))
            } else {
                Assert(OneGap(o.data[0..o.n], story))
            }
        }
    }
}

test "under std.sim: Reader.Line is repeatable across a cancel while it waits, the probe read included" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    try SimCase(&ar, sys.heap, SimStory, 0, 24, 0, true)
    try SimCase(&ar, sys.heap, SimFit, 0, 10, 0, true)
}

test "under std.sim: Reader.Read is repeatable across a cancel while it waits, the direct read included" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    try SimCase(&ar, sys.heap, SimStory, 3, 8, 3, true)
    try SimCase(&ar, sys.heap, SimStory, 3, 8, 8, true)
}

test "under std.sim: Until and ReadAll are prefix across a cancel while they wait" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    try SimCase(&ar, sys.heap, SimStory, 1, 8, 6, false)
    try SimCase(&ar, sys.heap, SimStory, 2, 8, 6, false)
}

// ReadRest reads s to its end into t.
ReadRest(s: &net.Stream, io: Io, t: mut Text): net.Error|OutOfMemory!void {
    var d: [16]u8 = [0; 16]
    for {
        n := try s.Read(io, mut d[..])
        if n == 0 {
            return
        }
        try t.Push(str.From(d[0..n]).Must())
    }
}

test "under std.sim: every Writer method over a stream is prefix at an interrupted write" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    for k in 0..Pieces + 1 {
        var sm := try sim.Sim.New(sys.arena, Cast[u64](900 + k))
        defer sm.Free()
        io := sm.Io()
        l := try net.Listen(io, "127.0.0.1:7534")
        defer l.Close()
        c := try net.Dial(io, "127.0.0.1:7534")
        srv := l.Accept(io) else e {
            c.Close()
            fail e
        }
        defer srv.Close()
        var b: [7]u8 = [0; 7]
        var w := c.Writer(io, mut b[..])
        var fails := 0
        for piece in 0..Pieces {
            // The write of piece k fails, as an interruption there does.
            if piece == k {
                sm.FailNext(sim.Op.Write, sim.Fault.NoSpace)
            }
            i := piece * 5
            var n := 5
            if i + n > Story.Len() {
                n = Story.Len() - i
            }
            fails += WritePiece(mut w, piece, Story[i..i + n])
        }
        c.Close()
        var t := Text.New(&ar)
        try ReadRest(&srv, io, mut t)
        got := t.Str()
        if k < Pieces {
            Assert(fails == 1)
            Assert(OneGap(got.AsBytes(), Story))
            Assert(Story.Len() - got.Len() >= 1 && Story.Len() - got.Len() <= 5)
        } else {
            Assert(fails == 0)
            Assert(got == Story)
        }
        ar.Reset()
    }
}

// Addr formats the loopback address of port into buf.
Addr(buf: mut []u8, port: int): str from buf {
    var c := core.Capped.New(buf)
    var w := c.Writer()
    Print(mut w, "127.0.0.1:{port}", (port)) else {
    }
    n := c.Bytes().Len()
    return str.From(buf[0..n]) else {
        return ""
    }
}

test "every Writer method over a stream fails under a cancelled scope and loses only that piece" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    for k in 0..Pieces + 1 {
        l := try net.Listen(sys.io, "127.0.0.1:0")
        defer l.Close()
        var ab: [32]u8 = [0; 32]
        c := try net.Dial(sys.io, Addr(mut ab[..], l.Port()))
        srv := l.Accept(sys.io) else e {
            c.Close()
            fail e
        }
        defer srv.Close()
        var b: [7]u8 = [0; 7]
        var w := c.Writer(sys.io, mut b[..])
        var fails := 0
        for piece in 0..Pieces {
            i := piece * 5
            var n := 5
            if i + n > Story.Len() {
                n = Story.Len() - i
            }
            if piece < k {
                scope s := sys.io.Scope() {
                    s.Cancel()
                    fails += WritePiece(mut w, piece, Story[i..i + n])
                }
            } else {
                fails += WritePiece(mut w, piece, Story[i..i + n])
            }
        }
        c.Close()
        var t := Text.New(&ar)
        try ReadRest(&srv, sys.io, mut t)
        // Each cancelled piece is buffered, then its Flush's write fails
        // before it starts: that piece, and only it, is lost.
        Assert(fails == k)
        Assert(t.Str() == Story[k * 5..])
        ar.Reset()
    }
}
`

// core8CancelNames are the blocks of core8Cancel whose verdict depends on
// the cancel: with every s.Cancel() removed each must fail, and every
// other block must still pass.
var core8CancelNames = []string{
	"under std.sim: Reader.Line is repeatable across a cancel while it waits, the probe read included",
	"under std.sim: Reader.Read is repeatable across a cancel while it waits, the direct read included",
	"under std.sim: Until and ReadAll are prefix across a cancel while they wait",
	"every Writer method over a stream fails under a cancelled scope and loses only that piece",
}

// core8TestNames are the names of the test blocks in an Mk file.
func core8TestNames(file string) []string {
	var names []string
	for _, m := range regexp.MustCompile(`(?m)^test "([^"]+)"`).FindAllStringSubmatch(file, -1) {
		names = append(names, m[1])
	}
	return names
}

// TestCore_CancelClasses: the cancel-safety classes of the prelude's Reader and
// Writer lines (core8Cancel's comment), at 1 and 16 carriers.
func TestCore_CancelClasses(t *testing.T) {
	for _, carriers := range []int{1, 16} {
		t.Run(fmt.Sprintf("carriers=%d", carriers), func(t *testing.T) {
			out, status := core8MkTest(t, core8Fixtures(map[string]string{"tests_cancel.mk": core8Cancel}), carriers)
			core8WantPass(t, out, status, core8Cancel)
		})
	}
}

// TestCore_CancelIsLoadBearing: the cancel
// blocks fail when the cancel is removed, so they test the cancel and not
// a deadlock's Canceled or nothing at all; the other blocks still pass.
// It is a cancel-safety test; tools/gate/safety.go lists it.
func TestCore_CancelIsLoadBearing(t *testing.T) {
	mutant := strings.ReplaceAll(core8Cancel, "s.Cancel()\n", "_ = s\n")
	if mutant == core8Cancel {
		t.Fatal("no s.Cancel() in core8Cancel")
	}
	out, _ := core8MkTest(t, core8Fixtures(map[string]string{"tests_cancel.mk": mutant}), 1)
	for _, name := range core8CancelNames {
		if !strings.Contains(out, fmt.Sprintf("FAIL std.coreprobe %q", name)) {
			t.Errorf("%q did not fail without its cancel", name)
		}
	}
	for _, m := range core8TestNames(core8Cancel) {
		if !slices.Contains(core8CancelNames, m) && !strings.Contains(out, fmt.Sprintf("PASS std.coreprobe %q", m)) {
			t.Errorf("%q did not pass without the cancel, and does not depend on it", m)
		}
	}
	if t.Failed() {
		t.Logf("output:\n%s", out)
	}
}
