// Why: Unix-domain stream sockets (std/rt/unix.mk, each target's sys/unix.mk)
// must behave like TCP ones: echo, refusals, backlog waits and cancels.
// What: RT8-Unix-Echo, -Refused, -Backlog and -AcceptCancel (UxName, UxPhase).
// How: a cancel or a deadline must end a waiting accept or dial with nothing
// held, so it can be repeated. Windows uses a relative socket name in the
// child's scratch working directory, within the sun_path limit. Linux uses
// TMPDIR or /tmp on the distribution's file system (WSL's Windows drive
// refuses a bind). Run
// by the scheduler harness, and at 1 and 16 carriers by std/rt_deadline_test.go.

import std.core
import std.raw
import std.rt

// UxName removes a socket left by an earlier run before listening.
UxName(io: Io, b: mut [256]u8, tag: str): str from b {
    name := if Target.os == core.Os.Windows { FlNameAt(b, 0, tag, 0) } else { FlName(b, tag, 0) }
    _ = NativeRemove(io, name)
    return name
}

// UxPhase sets slot k's 4-bit phase in w (the watchdog's report: clients
// 1 dialing, 2 sending, 3 receiving, 4 closing, 5 done; echoes 1
// accepting, 2 receiving, 3 sending, 4 closing, 5 done, 6 abandoning after a failed receive, 7 abandoned).
UxPhase(w: &raw.Atomic[u64], k: int, phase: u64) {
    sh := Trunc[u64](k) * 4
    for {
        old := w.Load()
        if w.Cas(old, (old & ~(Trunc[u64](15) << sh)) | (phase << sh)) {
            return
        }
    }
}

// UxEcho is NEcho (tests_net.mk) for the Unix block, with its phase kept
// in slot k of ph.
UxEcho(io: Io, l: uint, done: &raw.Atomic[u64], ph: &raw.Atomic[u64], k: int) {
    UxPhase(ph, k, 1)
    a := NativeAccept(io, l)
    if a.err != core.SysErr.None {
        return
    }
    s := Trunc[uint](a.value)
    var buf: [256]u8 = [0; 256]
    for {
        UxPhase(ph, k, 2)
        r := NativeRecv(io, s, mut buf[..])
        if r.err != core.SysErr.None {
            UxPhase(ph, k, 6)
            rt.Abandon(s)
            UxPhase(ph, k, 7)
            return
        }
        if r.value == 0 {
            break
        }
        n := Trunc[int](r.value)
        UxPhase(ph, k, 3)
        if !NSendAll(io, s, buf[0..n]) {
            rt.Abandon(s)
            return
        }
        _ = done.Add(Trunc[u64](n))
    }
    UxPhase(ph, k, 4)
    Assert(rt.CloseRuntimeHandle(s).err == core.SysErr.None)
    _ = done.Add(1 << 40)
    UxPhase(ph, k, 5)
}

// UxHex writes w as 16 hex digits into b at at.
UxHex(b: mut [96]u8, at: int, w: u64) {
    for i in 0..16 {
        d := Trunc[u8]((w >> (60 - 4 * Trunc[u64](i))) & 15)
        b[at + i] = if d < 10 { 48 + d } else { 87 + d }
    }
}

// UxWatch waits up to 20 s for fin; then it writes the phases (clients'
// cp, echoes' ep) and the bytes echoed to stderr and panics, so a hang
// ends with its state instead of the harness's kill.
UxWatch(io: Io, fin: &raw.Atomic[u32], cp: &raw.Atomic[u64], ep: &raw.Atomic[u64], done: &raw.Atomic[u64]) {
    for _ in 0..200 {
        if fin.Load() != 0 {
            return
        }
        io.Sleep(core.DurationOf(100 * DlMs)) else {
            return
        }
    }
    var b: [96]u8 = [32; 96]
    UxHex(mut b, 0, cp.Load())
    UxHex(mut b, 17, ep.Load())
    UxHex(mut b, 34, done.Load())
    msg := str.From(b[0..50]) else { "?" }
    rt.Reporting(raw.Null[rt.Site](), "RT8-Unix-Echo stalled: clients, echoes, echoed ", msg, "")
}

// UxFail records a client's failure in bad: the stage (1 dial, 2 send, 3
// receive, 4 a byte that came back changed), the error and the OS code.
UxFail(bad: &raw.Atomic[u64], stage: u64, r: core.IoResult) {
    _ = bad.Swap((stage << 56) | (u64(r.err) << 32) | (Trunc[u64](r.os) & 0xFFFF_FFFF))
}

// UxClient dials path and makes rounds round trips of a 1 to 200 byte
// message with a pattern from seed; ok gets 1 when every byte came back,
// and bad the failure otherwise (UxFail); sent counts the bytes it sends.
UxClient(io: Io, path: []u8, rounds: int, seed: u64, ok: &raw.Atomic[u32], bad: &raw.Atomic[u64], ph: &raw.Atomic[u64], k: int, sent: &raw.Atomic[u64]) {
    UxPhase(ph, k, 1)
    d := rt.UnixDial(io, path)
    if d.err != core.SysErr.None {
        UxFail(bad, 1, d)
        return
    }
    c := Trunc[uint](d.value)
    var out: [200]u8 = [0; 200]
    var back: [200]u8 = [0; 200]
    var rng := FlRng(s: seed | 1)
    for _ in 0..rounds {
        n := 1 + Trunc[int](rng.Next() % 200)
        for i in 0..n {
            out[i] = Trunc[u8](rng.Next())
        }
        UxPhase(ph, k, 2)
        _ = sent.Add(Trunc[u64](n))
        if !NSendAll(io, c, out[0..n]) {
            UxFail(bad, 2, core.IoResult(value: 0, err: core.SysErr.Other, os: 0))
            rt.Abandon(c)
            return
        }
        UxPhase(ph, k, 3)
        var got := 0
        for got < n {
            r := NativeRecv(io, c, mut back[got..n])
            if r.err != core.SysErr.None || r.value <= 0 {
                UxFail(bad, 3, r)
                rt.Abandon(c)
                return
            }
            got += Trunc[int](r.value)
        }
        for i in 0..n {
            if back[i] != out[i] {
                UxFail(bad, 4, core.IoResult(value: 0, err: core.SysErr.Other, os: 0))
                rt.Abandon(c)
                return
            }
        }
    }
    UxPhase(ph, k, 4)
    Assert(rt.CloseRuntimeHandle(c).err == core.SysErr.None)
    _ = ok.Add(1)
    UxPhase(ph, k, 5)
}

test "RT8-Unix-Echo 8 Unix-domain connections at once, 300 round trips each, through the accept, recv and send entries" (sys: &System) {
    io := sys.io
    var nb: [256]u8 = [0; 256]
    name := UxName(io, mut nb, "ux-echo")
    // MK_RT8_NOSKIP: every socket posts a packet for a synchronous success
    // too (the measurement of the lost end of stream with skip-on-success
    // off); it must come before the first socket.
    if (sys.Env("MK_RT8_NOSKIP") else { "" }).Len() > 0 {
        Assert(USkipOff())
    }
    l := rt.UnixListen(io, name.AsBytes(), 0)
    Assert(l.err == core.SysErr.None)
    lh := Trunc[uint](l.value)
    done := raw.Atomic[u64].New(0)
    ok := raw.Atomic[u32].New(0)
    bad := raw.Atomic[u64].New(0)
    sent := raw.Atomic[u64].New(0)
    cp := raw.Atomic[u64].New(0)
    ep := raw.Atomic[u64].New(0)
    fin := raw.Atomic[u32].New(0)
    var lost: u64 = 0
    scope w := io.Scope() {
        w.go UxWatch(io, &fin, &cp, &ep, &done)
        scope s := io.Scope() {
            for k in 0..8 {
                s.go UxEcho(io, lh, &done, &ep, k)
            }
            scope c := io.Scope() {
                for k in 0..8 {
                    c.go UxClient(io, name.AsBytes(), 300, Trunc[u64](k) + 5, &ok, &bad, &cp, k, &sent)
                }
            }
            if ok.Load() != 8 {
                // A client that failed leaves an accept waiting: end it, so
                // the block fails with the cause instead of hanging.
                s.Cancel()
            } else {
                // Every client has closed: each echo's pending receive must
                // see the end of the stream. Windows' AF_UNIX provider now
                // and then never completes an overlapped receive pending at
                // the peer's close (Go's net, with the same echo, shows it
                // too; a blocking receive does not): after 2 s such echoes
                // are counted and cancelled.
                var waited := 0
                for (done.Load() >> 40) < 8 && waited < 200 {
                    io.Sleep(core.DurationOf(10 * DlMs)) else {}
                    waited += 1
                }
                lost = 8 - (done.Load() >> 40)
                if lost > 0 {
                    s.Cancel()
                }
            }
        }
        _ = fin.Store(1)
    }
    if ok.Load() != 8 {
        f := bad.Load()
        sys.PrintErr("client failed: stage {st} err {e} os {o}\n",
            (st: f >> 56, e: (f >> 32) & 0xFF, o: f & 0xFFFF_FFFF)) else {}
    }
    Assert(ok.Load() == 8)
    // Every byte every client sent was echoed.
    Assert((done.Load() & 0xFF_FFFF_FFFF) == sent.Load())
    if lost > 0 {
        sys.PrintErr("RT8-Unix-Echo: the OS lost the end of stream on {lost} of 8 connections\n", (lost: lost)) else {}
    }
    // Only Windows' provider loses the end of a stream; Linux never may.
    Assert(lost == 0 || PWin())
    Assert(rt.CloseRuntimeHandle(lh).err == core.SysErr.None)
    Assert(NativeRemove(io, name).err == core.SysErr.None)
}
test "RT8-Unix-Refused bad paths, a cancelled scope, a path in use, nothing listening, and the TCP-only calls on a Unix socket" (sys: &System) {
    io := sys.io
    var nb: [256]u8 = [0; 256]
    name := UxName(io, mut nb, "ux-ref")
    var empty: [1]u8 = [0]
    Assert(rt.UnixListen(io, empty[0..0], 0).err == core.SysErr.InvalidAddress)
    Assert(rt.UnixDial(io, empty[0..0]).err == core.SysErr.InvalidAddress)
    var nul: [4]u8 = [97, 0, 98, 0]
    Assert(rt.UnixListen(io, nul[0..3], 0).err == core.SysErr.InvalidAddress)
    Assert(rt.UnixDial(io, nul[0..3]).err == core.SysErr.InvalidAddress)
    var long: [108]u8 = [97; 108]
    Assert(rt.UnixListen(io, long[..], 0).err == core.SysErr.InvalidAddress)
    Assert(rt.UnixDial(io, long[..]).err == core.SysErr.InvalidAddress)
    // Nothing at the path: refused (Linux: the path does not exist).
    d := rt.UnixDial(io, name.AsBytes())
    Assert(d.err == core.SysErr.Refused || d.err == core.SysErr.NotFound)
    // A cancelled scope's listen and dial are Canceled before any call, as
    // net.Listen's and net.Dial's: no socket and no file is made.
    var cl := core.IoResult(value: 0, err: core.SysErr.None, os: 0)
    var cd := core.IoResult(value: 0, err: core.SysErr.None, os: 0)
    scope s := io.Scope() {
        s.Cancel()
        cl = rt.UnixListen(io, name.AsBytes(), 0)
        cd = rt.UnixDial(io, name.AsBytes())
    }
    Assert(cl.err == core.SysErr.Canceled && cd.err == core.SysErr.Canceled)
    l := rt.UnixListen(io, name.AsBytes(), 0)
    Assert(l.err == core.SysErr.None)
    lh := Trunc[uint](l.value)
    Assert(rt.UnixListen(io, name.AsBytes(), 0).err == core.SysErr.AddrInUse)
    c := rt.UnixDial(io, name.AsBytes())
    Assert(c.err == core.SysErr.None)
    ch := Trunc[uint](c.value)
    a := NativeAccept(io, lh)
    Assert(a.err == core.SysErr.None)
    ah := Trunc[uint](a.value)
    var out: [16]u8 = [0; 16]
    var w := core.IoWords(a: 0, b: 0, c: 0, d: 0)
    Assert(rt.SockAddress(io, ch, true, mut out[..], mut w).err == core.SysErr.Unsupported)
    Assert(rt.SockAddress(io, lh, false, mut out[..], mut w).err == core.SysErr.Unsupported)
    Assert(rt.SockOption(io, ch, core.IoSockNoDelay, 1, 0).err == core.SysErr.Unsupported)
    Assert(rt.SockOption(io, ch, core.IoSockKeepAlive, 1, 30).err == core.SysErr.Unsupported)
    Assert(rt.SockOption(io, ch, core.IoSockSendBuffer, 65536, 0).err == core.SysErr.None)
    // The dialer's write side closed: the peer reads the end of the stream.
    Assert(NativeSend(io, ch, "unix".AsBytes()).value == 4)
    Assert(rt.SockShutdown(io, ch, 1).err == core.SysErr.None)
    var b: [8]u8 = [0; 8]
    Assert(NativeRecv(io, ah, mut b[..]).value == 4 && b[0] == 117)
    Assert(NativeRecv(io, ah, mut b[..]).value == 0)
    Assert(rt.CloseRuntimeHandle(ah).err == core.SysErr.None)
    Assert(rt.CloseRuntimeHandle(ch).err == core.SysErr.None)
    Assert(rt.CloseRuntimeHandle(lh).err == core.SysErr.None)
    // The listener's close leaves its file; a new listen needs it removed.
    Assert(rt.UnixListen(io, name.AsBytes(), 0).err == core.SysErr.AddrInUse)
    Assert(NativeRemove(io, name).err == core.SysErr.None)
}

// UxDialInto dials path and stores the handle in got (0 on an error).
UxDialInto(io: Io, path: []u8, got: &raw.Atomic[u64]) {
    d := rt.UnixDial(io, path)
    if d.err == core.SysErr.None {
        got.Store(Trunc[u64](d.value))
    }
}

test "RT8-Unix-Backlog a dial waits while the backlog is full, a deadline ends it with nothing held, and an accept lets it in" (sys: &System) {
    io := sys.io
    // Darwin's connect to a Unix listener whose queue is full answers
    // ECONNREFUSED, as when nothing listens, where Linux answers EAGAIN;
    // so there the dial cannot wait, and a dial after an accept connects.
    mac := Target.os == Os.Macos
    stop := if mac { core.SysErr.Refused } else { core.SysErr.Canceled }
    var nb: [256]u8 = [0; 256]
    name := UxName(io, mut nb, "ux-full")
    l := rt.UnixListen(io, name.AsBytes(), 1)
    Assert(l.err == core.SysErr.None)
    lh := Trunc[uint](l.value)
    // Fill the backlog: each dial under a short deadline, until one waits
    // (on macOS, until one is refused).
    var held: [64]uint = [0; 64]
    var n := 0
    var full := false
    for n < 64 && !full {
        var r := core.IoResult(value: 0, err: core.SysErr.None, os: 0)
        scope s := io.Scope() {
            s.CancelAfter(core.DurationOf(30 * DlMs))
            r = rt.UnixDial(io, name.AsBytes())
        }
        if r.err == stop {
            full = true
        } else {
            Assert(r.err == core.SysErr.None)
            held[n] = Trunc[uint](r.value)
            n += 1
        }
    }
    Assert(full && n >= 1)
    // A dial waiting on the full backlog gets in once one is accepted (on
    // macOS a dial started after the accept).
    got := raw.Atomic[u64].New(0)
    scope s := io.Scope() {
        if !mac {
            s.go UxDialInto(io, name.AsBytes(), &got)
            io.Sleep(core.DurationOf(20 * DlMs)) else {}
        }
        a := NativeAccept(io, lh)
        Assert(a.err == core.SysErr.None)
        held[n] = Trunc[uint](a.value)
        n += 1
        if mac {
            s.go UxDialInto(io, name.AsBytes(), &got)
        }
    }
    Assert(got.Load() != 0)
    Assert(rt.CloseRuntimeHandle(Trunc[uint](got.Load())).err == core.SysErr.None)
    for i in 0..n {
        Assert(rt.CloseRuntimeHandle(held[i]).err == core.SysErr.None)
    }
    Assert(rt.CloseRuntimeHandle(lh).err == core.SysErr.None)
    Assert(NativeRemove(io, name).err == core.SysErr.None)
}

test "RT8-Unix-AcceptCancel a deadline ends a waiting accept on a Unix listener; the next accept takes the next dial" (sys: &System) {
    io := sys.io
    var nb: [256]u8 = [0; 256]
    name := UxName(io, mut nb, "ux-acc")
    l := rt.UnixListen(io, name.AsBytes(), 0)
    Assert(l.err == core.SysErr.None)
    lh := Trunc[uint](l.value)
    for _ in 0..3 {
        var r := core.IoResult(value: 0, err: core.SysErr.None, os: 0)
        scope s := io.Scope() {
            s.CancelAfter(core.DurationOf(10 * DlMs))
            r = NativeAccept(io, lh)
        }
        Assert(r.err == core.SysErr.Canceled)
    }
    // Repeatable: nothing was taken, and the listener still serves.
    c := rt.UnixDial(io, name.AsBytes())
    Assert(c.err == core.SysErr.None)
    a := NativeAccept(io, lh)
    Assert(a.err == core.SysErr.None)
    Assert(rt.CloseRuntimeHandle(Trunc[uint](a.value)).err == core.SysErr.None)
    Assert(rt.CloseRuntimeHandle(Trunc[uint](c.value)).err == core.SysErr.None)
    Assert(rt.CloseRuntimeHandle(lh).err == core.SysErr.None)
    Assert(NativeRemove(io, name).err == core.SysErr.None)
}
