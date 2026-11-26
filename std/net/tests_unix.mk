// Why: Unix-domain sockets and handle passing differ by OS and file system;
// the promised errors and orderings need checks on each.
// What: test blocks for ListenUnix and DialUnix (an echo, the half-close, the
// addresses and options a Unix stream lacks, a path in use, refused paths)
// and handle passing with SendHandles and RecvHandles (SCM_RIGHTS on Linux,
// Unsupported on Windows), and a Write cut by its deadline.
// How: on Windows a random relative name in the child's scratch working
// directory fits sun_path even when TEMP does not. Else use MK_LINUX_TMP
// (WSL's Windows drive refuses a bind), TEMP, TMPDIR or /tmp. Remove the
// socket afterwards. Run by mk test --std=std.net and
// std/std-net_connblocks_test.go.

import std.core
import std.fs
import std.time

// UxDir is the directory for socket files.
UxDir(sys: &System): str from sys {
    if Target.os == core.Os.Windows {
        return "."
    }
    for name in ["MK_LINUX_TMP", "TEMP", "TMPDIR"] {
        if d := sys.Env(name) {
            if d.Len() > 0 {
                return d
            }
        }
    }
    return "/tmp"
}

// UxPath is a fresh socket path in UxDir: "<dir>/mk-net-<16 hex>".
UxPath(sys: &System, b: mut [256]u8): str from b {
    dir := UxDir(sys)
    var r: [8]u8 = [0; 8]
    sys.io.NoCancel().Random(mut r[..]) else {
    }
    var n := 0
    (mut b[0..dir.Len()]).CopyFrom(dir.AsBytes())
    n = dir.Len()
    pre := "/mk-net-".AsBytes()
    (mut b[n..n + pre.Len()]).CopyFrom(pre)
    n += pre.Len()
    for i in 0..8 {
        b[n] = HexLow(r[i] >> 4)
        b[n + 1] = HexLow(r[i] & 15)
        n += 2
    }
    return str.From(b[0..n]).Must()
}

// RemoveSock removes the socket file at path, if any.
RemoveSock(io: Io, path: str) {
    _ = core.IoRemove(io.NoCancel(), path)
}

// HexLow is the lowercase hex digit of d (0 to 15).
HexLow(d: u8): u8 {
    return if d < 10 { 48 + d } else { 87 + d }
}

// UxEcho accepts one connection on l and echoes it to its end.
UxEcho(l: &Listener, io: Io, done: mut int) {
    c := l.Accept(io) else {
        *done = -1
        return
    }
    var buf: [512]u8 = [0; 512]
    for {
        n := c.Read(io, mut buf[..]) else {
            *done = -2
            c.Close()
            return
        }
        if n == 0 {
            break
        }
        c.WriteAll(io, buf[0..n]) else {
            *done = -3
            c.Close()
            return
        }
        *done += n
    }
    c.Close()
}

test "A Unix stream echoes through a Conn, half-closes, and has no address or TCP option" (sys: &System) {
    var pb: [256]u8 = [0; 256]
    path := UxPath(sys, mut pb)
    l := ListenUnix(sys.io, path) else e {
        // Windows before 10 1803 has no Unix sockets; Linux always has.
        Assert(ErrorName(e) == "Unsupported" && Target.os == core.Os.Windows)
        return
    }
    defer RemoveSock(sys.io, path)
    defer l.Close()
    Assert(l.Port() == 0)
    var echoed := 0
    var buf: [64]u8 = [0; 64]
    scope s := sys.io.Scope() {
        s.go UxEcho(&l, sys.io, mut echoed)
        st := try DialUnix(sys.io, path)
        Assert(SameAddr(st.PeerAddr(), ZeroAddr) && SameAddr(st.LocalAddr(), ZeroAddr))
        var why := ""
        st.SetNoDelay(true) else e {
            why = ErrorName(e)
        }
        Assert(why == "Unsupported")
        why = ""
        st.SetKeepAlive(none) else e {
            why = ErrorName(e)
        }
        Assert(why == "Unsupported")
        conn := st.Conn()
        onerr conn.Abandon()
        Assert(conn.PeerAddr().IsNone())
        try conn.WriteAll(sys.io, "hello, unix".AsBytes())
        try conn.Shutdown(sys.io, Shutdown.Write)
        var n := 0
        // The echo's end of stream: bounded, since Windows' AF_UNIX can
        // lose a peer's close under an overlapped receive (an OS defect).
        scope r := sys.io.Scope() {
            r.CancelAfter(time.Seconds(10))
            for {
                k := conn.Read(sys.io, mut buf[n..]) else {
                    break
                }
                if k == 0 {
                    break
                }
                n += k
            }
        }
        Assert(n == 11 && buf[0] == 0x68 && buf[10] == 0x78)
        try conn.Close(sys.io)
    }
    Assert(echoed == 11)
}

test "ListenUnix refuses a path in use, an empty path and one too long; DialUnix to nothing is Refused" (sys: &System) {
    var pb: [256]u8 = [0; 256]
    path := UxPath(sys, mut pb)
    l := ListenUnix(sys.io, path) else e {
        Assert(ErrorName(e) == "Unsupported" && Target.os == core.Os.Windows)
        return
    }
    defer RemoveSock(sys.io, path)
    var why := ""
    l2 := ListenUnix(sys.io, path) else e {
        why = ErrorName(e)
        l.Close()
        Assert(why == "AddrInUse")
        // Closing leaves the socket file, so the path stays in use.
        l3 := ListenUnix(sys.io, path) else e3 {
            Assert(ErrorName(e3) == "AddrInUse")
            // Gone once removed: a dial finds nothing there.
            RemoveSock(sys.io, path)
            d := DialUnix(sys.io, path) else e4 {
                Assert(ErrorName(e4) == "Refused")
                e5 := ListenUnix(sys.io, "") else e5 {
                    Assert(ErrorName(e5) == "InvalidAddress")
                    var long: [120]u8 = [0x61; 120]
                    e6 := ListenUnix(sys.io, str.From(long[..]).Must()) else e6 {
                        Assert(ErrorName(e6) == "InvalidAddress")
                        return
                    }
                    e6.Close()
                    Panic("ListenUnix took a 120-byte path")
                }
                e5.Close()
                Panic("ListenUnix took an empty path")
            }
            d.Close()
            Panic("DialUnix reached a removed path")
        }
        l3.Close()
        Panic("ListenUnix took a path whose socket file stayed")
    }
    l2.Close()
    l.Close()
    Panic("ListenUnix took a path in use")
}

// UxPair is a connected pair of Unix streams over l, listening at path:
// the dialler's end and the accepted end.
UxPair(io: Io, l: &Listener, path: str): (tx: Stream, rx: Stream) {
    tx := DialUnix(io, path).Must()
    rx := l.Accept(io) else {
        tx.Close()
        Panic("Accept failed on a Unix listener")
    }
    return (tx: tx, rx: rx)
}

// UxOs is e's OS code when e is Os, else -1.
UxOs(e: Error): i64 {
    return match e {
        Os(code: c) { c }
        _ { -1 }
    }
}

// UxStream, UxListener and UxFile take p as the kind each names; any other
// kind is closed and fails the block.
UxStream(p: Passed): Stream {
    match p {
        .Stream(s) { return s }
        .File(f) { f.Abandon() }
        .Listener(l) { l.Close() }
        _ {}
    }
    Panic("a passed handle is not a Stream")
}

UxListener(p: Passed): Listener {
    match p {
        .Listener(l) { return l }
        .File(f) { f.Abandon() }
        .Stream(s) { s.Close() }
        _ {}
    }
    Panic("a passed handle is not a Listener")
}

UxFile(p: Passed): fs.File {
    match p {
        .File(f) { return f }
        .Stream(s) { s.Close() }
        .Listener(l) { l.Close() }
        _ {}
    }
    Panic("a passed handle is not a File")
}

// UxAcceptOne accepts one connection on l and closes it; *n counts it.
UxAcceptOne(l: &Listener, io: Io, n: mut int) {
    c := l.Accept(io) else {
        return
    }
    *n += 1
    c.Close()
}

// UxSendLater sleeps 50 ms, then sends "p" with f attached on tx; *sent
// is 1 once it went. It closes tx and f.
UxSendLater(io: Io, tx: Stream, f: fs.File, sent: mut int) {
    io.Sleep(time.Millis(50)) else {}
    var one: [1]Pass = [Pass.File(f: &f)]
    tx.SendHandles(io, "p".AsBytes(), one[..]) else {
        tx.Close()
        f.Abandon()
        return
    }
    *sent = 1
    tx.Close()
    f.Abandon()
}

// UxDrainUntil receives on rx with RecvHandles until a byte Y arrives,
// counting the handles in *n (each closed) and saying in *z whether a byte
// Z arrived on the way. It closes rx.
UxDrainUntil(io: Io, rx: Stream, h: &Heap, n: mut int, z: mut bool, y: mut bool) {
    a := h.Alloc()
    var got := HeapList[Passed].New(a)
    var b: [4096]u8 = [0; 4096]
    for !*y {
        k := rx.RecvHandles(a, io, mut b[..], mut got) else {
            break
        }
        if k == 0 {
            break
        }
        for x in b[0..k] {
            if x == 0x5A {
                *z = true
            }
            if x == 0x59 {
                *y = true
            }
        }
    }
    *n = got.Len()
    for got.Len() > 0 {
        got.Pop().Must().Close()
    }
    got.FreeEmpty(a)
    rx.Close()
}

// UxFds is how many descriptors the process's descriptor directory lists,
// the listing's own included: /proc/self/fd on Linux, /dev/fd on macOS
// (which has no /proc).
UxFds(sys: &System): int {
    var d := fs.Dir.Open(sys.io, if Target.os == core.Os.Macos { "/dev/fd" } else { "/proc/self/fd" }).Must()
    var n := 0
    for {
        e := d.Next(sys.arena, sys.io).Must()
        if e.IsNone() {
            break
        }
        n += 1
    }
    d.Close()
    return n
}

// UxTemp is a fresh file in UxDir holding text; the caller removes path.
UxTemp(sys: &System, text: str): (file: fs.File, path: str) from sys {
    (file: f, path: p) := fs.CreateTemp(sys.arena, sys.io, UxDir(sys), "mk-pass-").Must()
    _ = f.WriteAt(sys.io, 0, text.AsBytes()).Must()
    return (file: f, path: p)
}

test "SendHandles: a file, a listener and a stream sent beside bytes arrive in order and typed, each works, and a stream outlives the sender's copy; Windows answers Unsupported" (sys: &System) {
    io := sys.io
    a := sys.heap.Alloc()
    var pb: [256]u8 = [0; 256]
    path := UxPath(sys, mut pb)
    l := ListenUnix(io, path) else e {
        Assert(ErrorName(e) == "Unsupported" && Target.os == core.Os.Windows)
        return
    }
    defer RemoveSock(io, path)
    defer l.Close()
    (tx, rx) := UxPair(io, &l, path)
    defer tx.Close()
    defer rx.Close()
    (tx: tx2, rx: rx2) := UxPair(io, &l, path)
    (file: f, path: fpath) := UxTemp(sys, "passed file")
    defer RemoveSock(io, fpath)
    defer f.Abandon()
    tcp := Listen(io, "127.0.0.1:0").Must()
    defer tcp.Close()
    var ps: [3]Pass = [Pass.File(f: &f), Pass.Listener(l: &tcp), Pass.Stream(s: &tx2)]
    tx.SendHandles(io, "hdr!".AsBytes(), ps[..]) else e {
        // An AF_UNIX socket on Windows carries no handles.
        Assert(ErrorName(e) == "Unsupported" && Target.os == core.Os.Windows)
        tx2.Close()
        rx2.Close()
        return
    }
    Assert(Target.os != core.Os.Windows)
    var got := HeapList[Passed].New(a)
    var buf: [16]u8 = [0; 16]
    var n := rx.RecvHandles(a, io, mut buf[..], mut got).Must()
    for n < 4 {
        k := rx.Read(io, mut buf[n..]).Must()
        Assert(k > 0)
        n += k
    }
    Assert(n == 4 && str.From(buf[0..4]).Must() == "hdr!" && got.Len() == 3)
    // The stream: the sender's copy closes first, and the passed one still
    // carries bytes to the far end, which sees the end only after it too
    // closes (no shutdown on a shared socket).
    s := UxStream(got.Pop().Must())
    tx2.Close()
    s.WriteAll(io, "via copy".AsBytes()).Must()
    var b2: [16]u8 = [0; 16]
    var m := 0
    for m < 8 {
        k := rx2.Read(io, mut b2[m..]).Must()
        Assert(k > 0)
        m += k
    }
    Assert(str.From(b2[0..8]).Must() == "via copy")
    s.Close()
    Assert(rx2.Read(io, mut b2[..]).Must() == 0)
    rx2.Close()
    // The listener: its port, and it accepts beside the original.
    pl := UxListener(got.Pop().Must())
    Assert(pl.Port() == tcp.Port())
    var accepted := 0
    var ab: [32]u8 = [0; 32]
    scope sc := io.Scope() {
        sc.go UxAcceptOne(&pl, io, mut accepted)
        c := TnDial(io, TnAddr(mut ab, pl.Port())).Must()
        c.Close()
    }
    Assert(accepted == 1)
    pl.Close()
    // The file: its bytes from position 0.
    pf := UxFile(got.Pop().Must())
    var fb: [16]u8 = [0; 16]
    fk := pf.ReadAt(io, 0, mut fb[..]).Must()
    Assert(str.From(fb[0..fk]).Must() == "passed file")
    pf.Abandon()
    got.FreeEmpty(a)
}

test "RecvHandles: a deadline with nothing queued is Canceled; bytes before the handles come back as Read gives them; a Read of the handles' byte closes them; a receive parked on the poller wakes for a later send (Linux)" (sys: &System) {
    io := sys.io
    a := sys.heap.Alloc()
    var pb: [256]u8 = [0; 256]
    path := UxPath(sys, mut pb)
    l := ListenUnix(io, path) else e {
        Assert(ErrorName(e) == "Unsupported" && Target.os == core.Os.Windows)
        return
    }
    defer RemoveSock(io, path)
    defer l.Close()
    (tx, rx) := UxPair(io, &l, path)
    defer rx.Close()
    (file: f, path: fpath) := UxTemp(sys, "x")
    defer RemoveSock(io, fpath)
    var one: [1]Pass = [Pass.File(f: &f)]
    var got := HeapList[Passed].New(a)
    var buf: [16]u8 = [0; 16]
    var why := ""
    scope sc := io.Scope() {
        sc.CancelAfter(time.Millis(50))
        _ = rx.RecvHandles(a, io, mut buf[..], mut got) else e {
            why = ErrorName(e)
            0
        }
    }
    if Target.os == core.Os.Windows {
        Assert(why == "Unsupported")
        got.FreeEmpty(a)
        tx.Close()
        f.Abandon()
        return
    }
    Assert(why == "Canceled" && got.Len() == 0)
    // Plain bytes, then a byte with a handle: three bytes and one handle
    // in all, however the calls split them.
    tx.WriteAll(io, "ab".AsBytes()).Must()
    tx.SendHandles(io, "c".AsBytes(), one[..]).Must()
    var n := 0
    for n < 3 {
        k := rx.RecvHandles(a, io, mut buf[n..], mut got).Must()
        Assert(k > 0)
        n += k
    }
    Assert(str.From(buf[0..3]).Must() == "abc" && got.Len() == 1)
    UxFile(got.Pop().Must()).Abandon()
    // A Read of a byte that carries a handle: the handle is closed in this
    // process, never installed.
    before := UxFds(sys)
    tx.SendHandles(io, "d".AsBytes(), one[..]).Must()
    k := rx.Read(io, mut buf[..]).Must()
    Assert(k == 1 && buf[0] == 0x64 && UxFds(sys) == before)
    // Parked on the poller: nothing is queued when the receive starts, and
    // a task sends 50 ms later (it takes tx and f, and closes both).
    var sent := 0
    var pn := 0
    scope sp := io.Scope() {
        sp.go UxSendLater(io, tx, f, mut sent)
        pn = rx.RecvHandles(a, io, mut buf[..], mut got).Must()
    }
    Assert(sent == 1 && pn == 1 && buf[0] == 0x70 && got.Len() == 1)
    UxFile(got.Pop().Must()).Abandon()
    got.FreeEmpty(a)
}

test "SendHandles refuses an empty message, no handles and 65 handles with Os(0), and a TCP stream as Unsupported; RecvHandles an empty buffer with Os(0) and a TCP stream as Unsupported" (sys: &System) {
    io := sys.io
    a := sys.heap.Alloc()
    tcp := Listen(io, "127.0.0.1:0").Must()
    defer tcp.Close()
    var ab: [32]u8 = [0; 32]
    c := TnDial(io, TnAddr(mut ab, tcp.Port())).Must()
    defer c.Close()
    s := tcp.Accept(io).Must()
    defer s.Close()
    (file: f, path: fpath) := UxTemp(sys, "x")
    defer RemoveSock(io, fpath)
    defer f.Abandon()
    var one: [1]Pass = [Pass.File(f: &f)]
    var many: [65]Pass = [Pass.File(f: &f); 65]
    var code: i64 = 1
    c.SendHandles(io, "".AsBytes(), one[..]) else e {
        code = UxOs(e)
    }
    Assert(code == 0)
    code = 1
    c.SendHandles(io, "x".AsBytes(), one[0..0]) else e {
        code = UxOs(e)
    }
    Assert(code == 0)
    code = 1
    c.SendHandles(io, "x".AsBytes(), many[..]) else e {
        code = UxOs(e)
    }
    Assert(code == 0)
    var why := ""
    c.SendHandles(io, "x".AsBytes(), one[..]) else e {
        why = ErrorName(e)
    }
    Assert(why == "Unsupported")
    var got := HeapList[Passed].New(a)
    var buf: [4]u8 = [0; 4]
    code = 1
    _ = s.RecvHandles(a, io, mut buf[0..0], mut got) else e {
        code = UxOs(e)
        0
    }
    Assert(code == 0)
    why = ""
    _ = s.RecvHandles(a, io, mut buf[..], mut got) else e {
        why = ErrorName(e)
        0
    }
    Assert(why == "Unsupported" && got.Len() == 0)
    got.FreeEmpty(a)
}

test "SendHandles on a full socket buffer: a deadline ends it with Canceled and nothing sent, and the same call then sends once the peer reads (repeatable; Linux)" (sys: &System) {
    if Target.os == core.Os.Windows {
        // Unsupported there, before any wait: the first SendHandles block shows it.
        return
    }
    io := sys.io
    var pb: [256]u8 = [0; 256]
    path := UxPath(sys, mut pb)
    l := ListenUnix(io, path).Must()
    defer RemoveSock(io, path)
    defer l.Close()
    (tx, rx) := UxPair(io, &l, path)
    defer tx.Close()
    (file: f, path: fpath) := UxTemp(sys, "x")
    defer RemoveSock(io, fpath)
    defer f.Abandon()
    var one: [1]Pass = [Pass.File(f: &f)]
    // Fill the socket's buffer: WriteAll until a deadline cuts it.
    var filler: [4096]u8 = [0x61; 4096]
    var why := ""
    scope sc := io.Scope() {
        sc.CancelAfter(time.Millis(300))
        for {
            tx.WriteAll(io, filler[..]) else e {
                why = ErrorName(e)
                break
            }
        }
    }
    Assert(why == "Canceled")
    why = ""
    scope sd := io.Scope() {
        sd.CancelAfter(time.Millis(100))
        tx.SendHandles(io, "Z".AsBytes(), one[..]) else e {
            why = ErrorName(e)
        }
    }
    Assert(why == "Canceled")
    // A reader drains the buffer; the next send goes once there is room,
    // and only its byte and its one handle arrive: the cut one sent nothing.
    var handles := 0
    var sawz := false
    var sawy := false
    scope rd := io.Scope() {
        rd.go UxDrainUntil(io, rx, sys.heap, mut handles, mut sawz, mut sawy)
        tx.SendHandles(io, "Y".AsBytes(), one[..]).Must()
    }
    Assert(sawy && !sawz && handles == 1)
}

test "a Unix Stream.Write cut by its deadline is prefix: the peer reads the counted bytes and some or all of the cut chunk, or a Reset ends it" (sys: &System) {
    // The case seen on Windows AF_UNIX, where a cancelled
    // send was still delivered whole (WriteCut, tests_stream.mk).
    var pb: [256]u8 = [0; 256]
    path := UxPath(sys, mut pb)
    l := ListenUnix(sys.io, path) else e {
        // Windows before 10 1803 has no Unix sockets; Linux always has.
        Assert(ErrorName(e) == "Unsupported" && Target.os == core.Os.Windows)
        return
    }
    defer RemoveSock(sys.io, path)
    defer l.Close()
    c := try DialUnix(sys.io, path)
    s := l.Accept(sys.io) else e {
        c.Close()
        fail e
    }
    defer s.Close()
    WriteCut(sys, c, &s)
}
