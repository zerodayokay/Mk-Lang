// Why: shutdown must wake a task parked in Read or WriteAll, and concurrent
// Listens must hand the inherited listener to exactly one caller.
// What: regression blocks for shutdown waking a parked Read or WriteAll,
// reads after Shutdown(Read), and the inherited listener's stash.
// How: run by mk test --std=std.net and by std/std-net_connblocks_test.go at 1
// and 16 carriers. A block whose fix needs a Windows runtime change not yet
// made asserts on Windows only when MK_NET3_BLOCKED is set (AsyncAsserts);
// on Linux it always asserts.

import std.core
import std.raw
import std.time

// AsyncAsserts: the block asserts here. Windows waits for the runtime's
// shutdown wake of a parked Read or WriteAll, not yet made, unless
// MK_NET3_BLOCKED is set and not empty.
AsyncAsserts(sys: &System): bool {
    if Target.os != core.Os.Windows {
        return true
    }
    v := sys.Env("MK_NET3_BLOCKED") else {
        return false
    }
    return v.Len() > 0
}

// AsyncName is e's member name, or "eof" and "data" for a count.
AsyncName(e: Error): str {
    return match e {
        Canceled { "Canceled" }
        Reset { "Reset" }
        Closed { "Closed" }
        Os(code: _) { "Os" }
        _ { "other" }
    }
}

// ParkRead reads c once under a 3 s deadline of its own and says what
// ended it: "eof", "data", or the error's name ("Canceled" at the deadline).
ParkRead(io: Io, c: &Conn, what: mut str) {
    var buf: [64]u8 = [0; 64]
    scope r := io.Scope() {
        r.CancelAfter(time.Millis(3000))
        n := c.Read(io, mut buf[..]) else e {
            *what = AsyncName(e)
            0
        }
        if *what == "" {
            *what = if n == 0 { "eof" } else { "data" }
        }
    }
}

// ShutWakesRead: a loopback pair, a task parked in a Read of the dialled
// end's Conn, and 100 ms later a Shutdown(how) of that Conn from this
// task. What ended the parked Read.
ShutWakesRead(io: Io, how: Shutdown): Error!str {
    l := TnListen(io)
    defer l.Close()
    (c, s) := try TnPair(io, &l)
    defer s.Close()
    conn := c.Conn()
    var what := ""
    var shut := "ok"
    scope sc := io.Scope() {
        sc.go ParkRead(io, &conn, mut what)
        io.Sleep(time.Millis(100)) else {}
        conn.Shutdown(io, how) else e {
            shut = AsyncName(e)
        }
    }
    conn.Abandon()
    Assert(shut == "ok")
    return what
}

test "a local Conn.Shutdown(Read) or Shutdown(Both) ends a Conn.Read parked in another task with the end of the stream" (sys: &System) {
    if !AsyncAsserts(sys) {
        return
    }
    r := try ShutWakesRead(sys.io, Shutdown.Read)
    b := try ShutWakesRead(sys.io, Shutdown.Both)
    var out := sys.Stdout()
    _ = Print(mut out, "NET3-ASYNC-1 read: Shutdown(Read) {r}, Shutdown(Both) {b}\n", (r, b)) else {}
    _ = out.Flush() else {}
    Assert(r == "eof")
    Assert(b == "eof")
}

// Flood writes buf to c until a WriteAll fails or cap bytes went, under a
// 4 s deadline of its own: what ended it ("Canceled" at the deadline, "cap"
// when it never blocked).
Flood(io: Io, c: &Conn, buf: []u8, cap: u64, sent: &raw.Atomic[u64], what: mut str) {
    scope r := io.Scope() {
        r.CancelAfter(time.Millis(4000))
        for sent.Load() < cap {
            c.WriteAll(io, buf) else e {
                *what = AsyncName(e)
                break
            }
            _ = sent.Add(Cast[u64](buf.Len()))
        }
    }
    if *what == "" {
        *what = "cap"
    }
}

test "a local Conn.Shutdown(Both) ends a Conn.WriteAll parked in another task with a failure, not at its own deadline" (sys: &System) {
    if !AsyncAsserts(sys) {
        return
    }
    io := sys.io
    buf := try sys.arena.Slice[u8](1 << 20)
    l := TnListen(io)
    defer l.Close()
    (c, s) := try TnPair(io, &l)
    // s never reads, so the writer parks once both buffers are full.
    defer s.Close()
    conn := c.Conn()
    sent := raw.Atomic[u64].New(0)
    var what := ""
    var shut := "ok"
    var waited: i64 = 0
    scope sc := io.Scope() {
        sc.go Flood(io, &conn, buf[..], 256 << 20, &sent, mut what)
        // Shut once the count stopped moving for 300 ms (at most 3 s).
        t0 := time.Now(io)
        var last: u64 = 0
        var still := 0
        for still < 3 && time.Now(io).Since(t0).Nanos() < 3_000_000_000 {
            io.Sleep(time.Millis(100)) else {}
            n := sent.Load()
            if n == last {
                still += 1
            } else {
                still = 0
                last = n
            }
        }
        waited = time.Now(io).Since(t0).Nanos() / 1_000_000
        conn.Shutdown(io, Shutdown.Both) else e {
            shut = AsyncName(e)
        }
        // The scope's join waits for the writer.
    }
    conn.Abandon()
    var out := sys.Stdout()
    _ = Print(mut out, "NET3-ASYNC-1 write: parked after {waited} ms, shutdown {shut}, the WriteAll ended with {what}\n", (waited, shut, what)) else {}
    _ = out.Flush() else {}
    Assert(shut == "ok")
    // The writer parked (it did not reach the cap) and was ended by the
    // shutdown, not by its own deadline.
    Assert(what != "cap")
    Assert(what != "Canceled")
}

test "after a local Shutdown(Read), a Read of the Conn, of the Stream and of its Reader returns the end of the stream" (sys: &System) {
    if !AsyncAsserts(sys) {
        return
    }
    io := sys.io
    l := TnListen(io)
    defer l.Close()
    (c, s) := try TnPair(io, &l)
    defer s.Close()
    c.Shutdown(io, Shutdown.Read) else e {
        c.Close()
        fail e
    }
    var one: [8]u8 = [0; 8]
    var st := ""
    var rd := ""
    scope r := io.Scope() {
        r.CancelAfter(time.Millis(2000))
        n := c.Read(io, mut one[..]) else e {
            st = AsyncName(e)
            0
        }
        if st == "" {
            st = if n == 0 { "eof" } else { "data" }
        }
        var rb: [16]u8 = [0; 16]
        var rr := c.Reader(io, mut rb[..])
        k := rr.Read(mut one[..]) else {
            rd = "ReadError"
            0
        }
        if rd == "" {
            rd = if k == 0 { "eof" } else { "data" }
        }
    }
    conn := c.Conn()
    var cv := ""
    scope r2 := io.Scope() {
        r2.CancelAfter(time.Millis(2000))
        n := conn.Read(io, mut one[..]) else e {
            cv = AsyncName(e)
            0
        }
        if cv == "" {
            cv = if n == 0 { "eof" } else { "data" }
        }
    }
    conn.Abandon()
    var out := sys.Stdout()
    _ = Print(mut out, "NET3-ASYNC-5: Stream.Read {st}, its Reader {rd}, Conn.Read {cv}\n", (st, rd, cv)) else {}
    _ = out.Flush() else {}
    Assert(st == "eof")
    Assert(rd == "eof")
    Assert(cv == "eof")
}

// StashClear empties the inherited-listener stash of this test process
// (each block runs in a process of its own).
StashClear() {
    Inherited.state.Store(StashEmpty)
    Inherited.asking.Store(0)
}

// StashDrop closes the listener the stash still holds, if any.
StashDrop() {
    var h: uint = 0
    var p := 0
    if TakeStash(0, mut h, mut p) {
        Listener(h: h, bound: p, local: ZeroAddr, kind: StreamTcp, no_delay: false).Close()
    }
}

// LoseInherit is the Listen that lost the inherited entry: counted as every
// Inherit is, then IoInherited answered it NotFound. got is the port of the listener it settled on, 0
// for none.
LoseInherit(io: Io, want: int, got: mut int) {
    InheritBegin()
    if l := InheritSettle(io, want, core.IoResult(value: 0, err: core.SysErr.NotFound, os: 0)) {
        *got = l.Port()
        l.Close()
        return
    }
    *got = 0
}

test "a Listen that loses the inherited listener to a concurrent Listen of another port takes it from the stash" (sys: &System) {
    io := sys.io
    StashClear()
    // A real listener stands in for the one the parent passed down.
    parent := TnListen(io)
    port := parent.Port()
    Listener(h, ..) := parent
    // B, a Listen of an admin port, has won IoInherited and not yet
    // stashed what it got.
    InheritBegin()
    var got := -1
    scope sc := io.Scope() {
        // A, a Listen of the parent's port, loses the entry meanwhile.
        sc.go LoseInherit(io, port, mut got)
        io.Sleep(time.Millis(50)) else {}
        // B settles: the listener is for another port, so it stashes it.
        b := InheritSettle(io, port + 1, core.IoResult(value: Cast[i64](h), err: core.SysErr.None, os: i64(port)))
        if l := b {
            l.Close()
            Panic("B kept a listener for another port")
        }
    }
    var out := sys.Stdout()
    _ = Print(mut out, "NET3-ASYNC-2: the losing Listen settled on port {got} (the parent's is {port})\n", (got, port)) else {}
    _ = out.Flush() else {}
    StashDrop()
    Assert(got == port)
}

// ListenOutcome is what a Listen (or ListenWith) of addr gave: an error's
// name, or "listener" (closed here).
ListenOutcome(io: Io, addr: str, with: bool): str {
    if with {
        l := ListenWith(io, addr, DefaultListenOptions) else e {
            return ErrorName(e)
        }
        l.Close()
        return "listener"
    }
    l := Listen(io, addr) else e {
        return ErrorName(e)
    }
    l.Close()
    return "listener"
}

test "Listen and ListenWith in a cancelled scope are Canceled and leave a stashed inherited listener for the next Listen" (sys: &System) {
    io := sys.io
    StashClear()
    parent := TnListen(io)
    port := parent.Port()
    Listener(h, ..) := parent
    // An earlier Listen of another port stashed the parent's listener.
    Stash(Cast[u64](h), Cast[u64](port))
    var b: [32]u8 = [0; 32]
    addr := TnAddr(mut b, port)
    var plain := ""
    var with := ""
    scope s := io.Scope() {
        s.Cancel()
        plain = ListenOutcome(io, addr, false)
        with = ListenOutcome(io, addr, true)
    }
    var kept: uint = 0
    var p := 0
    held := TakeStash(port, mut kept, mut p)
    if held {
        Listener(h: kept, bound: p, local: ZeroAddr, kind: StreamTcp, no_delay: false).Close()
    }
    var out := sys.Stdout()
    _ = Print(mut out, "NET3-ASYNC-4: Listen {plain}, ListenWith {with}, stash still held {held}\n", (plain, with, held)) else {}
    _ = out.Flush() else {}
    Assert(plain == "Canceled")
    Assert(with == "Canceled")
    Assert(held && kept == h)
}
