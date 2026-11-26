// Why: a child must get exactly the handles its Command names, typed, and a
// forged MK_HANDLES must take nothing.
// What: test blocks of Command.handles and proc.Inherited, with the child
// bodies SpHandlesChild, SpForeignChild and SpHandWriteChild.
// How: a child is this test executable run again with SpChild's modes
// (tests_spawn.mk: handles, handstream, foreign, handwrite).

import std.core
import std.fs
import std.net

// SpFileOf and SpListenerOf take p as the kind each names; any other kind
// is closed and ends the child with code 92.
SpFileOf(p: net.Passed): fs.File {
    match p {
        .File(f) { return f }
        .Stream(s) { s.Close() }
        .Listener(l) { l.Close() }
        _ {}
    }
    Exit(92)
}

SpListenerOf(p: net.Passed): net.Listener {
    match p {
        .Listener(l) { return l }
        .File(f) { f.Abandon() }
        .Stream(s) { s.Close() }
        _ {}
    }
    Exit(92)
}

SpStreamOf(p: net.Passed): net.Stream {
    match p {
        .Stream(s) { return s }
        .File(f) { f.Abandon() }
        .Listener(l) { l.Close() }
        _ {}
    }
    Exit(92)
}

// SpHandlesChild is the child of the Command.handles blocks (modes handles and
// handstream, SpChild's comment); it never returns.
SpHandlesChild(sys: &System, mode: str): never {
    io := sys.io
    a := sys.heap.Alloc()
    var out := sys.Stdout()
    var got := HeapList[net.Passed].New(a)
    n := Inherited(a, io, mut got) else { Exit(90) }
    if PtSame(mode, "handstream") {
        if n != 1 {
            Exit(91)
        }
        s := SpStreamOf(got.Remove(0))
        got.FreeEmpty(a)
        s.WriteAll(io, "from child".AsBytes()) else { Exit(93) }
        s.Close()
        Exit(0)
    }
    if n != 3 {
        Exit(91)
    }
    f := SpFileOf(got.Remove(0))
    var w := SpFileOf(got.Remove(0))
    l := SpListenerOf(got.Remove(0))
    got.FreeEmpty(a)
    var b: [32]u8 = [0; 32]
    k := f.ReadAt(io, 0, mut b[..]) else { Exit(93) }
    w.WriteAll(io, b[0..k]) else { Exit(94) }
    w.Abandon()
    f.Abandon()
    c := l.Accept(io) else { Exit(95) }
    c.WriteAll(io, "acc".AsBytes()) else { Exit(95) }
    c.Close()
    l.Close()
    // Only the first Inherited takes the handles.
    var again := HeapList[net.Passed].New(a)
    m := Inherited(a, io, mut again) else { Exit(96) }
    again.FreeEmpty(a)
    // A grandchild given no handles sees no MK_HANDLES.
    var g: [4]str = [sys.args[0], sys.args[1], "envget", "MK_HANDLES"]
    var gc := DefaultCommand
    gc.args = g[..]
    gc.stdout = Stdio.Pipe
    var gb: [64]u8 = [0; 64]
    gn := SpOut(sys, &gc, mut gb[..])
    var d: [24]u8 = [0; 24]
    out.Str(PtDigits(mut d, n)) else { Exit(97) }
    out.Str(" ") else { Exit(97) }
    out.Str(PtDigits(mut d, m)) else { Exit(97) }
    out.Str(" ") else { Exit(97) }
    out.Bytes(gb[0..gn]) else { Exit(97) }
    out.Flush() else { Exit(97) }
    Exit(0)
}

// SpNum is the decimal at the start of b (its digits up to the first
// other byte).
SpNum(b: []u8): int {
    var v := 0
    for x in b {
        if x < 48 || x > 57 {
            break
        }
        v = v * 10 + int(x - 48)
    }
    return v
}

// SpAddr is "127.0.0.1:<port>" in b.
SpAddr(b: mut [32]u8, port: int): str from b {
    pre := "127.0.0.1:".AsBytes()
    (mut b[0..pre.Len()]).CopyFrom(pre)
    var d: [24]u8 = [0; 24]
    t := PtDigits(mut d, port).AsBytes()
    (mut b[pre.Len()..pre.Len() + t.Len()]).CopyFrom(t)
    return str.From(b[0..pre.Len() + t.Len()]).Must()
}

test "Command.handles: the child gets the file, the pipe end and the listener named, in order and typed; a second Inherited finds none, and its own child sees no MK_HANDLES" (sys: &System) {
    if sys.args.Len() > 2 {
        SpChild(sys)
    }
    io := sys.io
    (file: f, path: fp) := fs.CreateTemp(sys.arena, io, "", "mk-proc-hd-").Must()
    _ = f.WriteAt(io, 0, "named file".AsBytes()).Must()
    (r, w) := Pipe(io).Must()
    tl := net.Listen(io, "127.0.0.1:0").Must()
    var hs: [3]Pass = [Pass.File(f: &f), Pass.Writer(w: &w), Pass.Listener(l: &tl)]
    var args: [3]str = [sys.args[0], sys.args[1], "handles"]
    var c := DefaultCommand
    c.args = args[..]
    c.stdout = Stdio.Pipe
    c.handles = hs[..]
    (child, stdin, stdout, stderr) := Spawn(sys.arena, io, &c).Must()
    SpCloseEnds(stdin, none, stderr)
    // The parent's write end: the pipe ends once the child's copy closes.
    w.Close()
    var ab: [32]u8 = [0; 32]
    conn := net.Dial(io, SpAddr(mut ab, tl.Port())).Must()
    var cb: [8]u8 = [0; 8]
    var cn := 0
    for cn < 3 {
        k := conn.Read(io, mut cb[cn..]).Must()
        Assert(k > 0)
        cn += k
    }
    Assert(SpText(cb[0..3], "acc"))
    conn.Close()
    var pb: [32]u8 = [0; 32]
    var pn := 0
    SpDrain(io, &r, mut pb[..], mut pn)
    Assert(SpText(pb[0..pn], "named file"))
    r.Close()
    o := stdout.Must()
    var ob: [64]u8 = [0; 64]
    var on := 0
    SpDrain(io, &o, mut ob[..], mut on)
    o.Close()
    Assert(SpCode(child.Wait(io).Must()) == 0)
    Assert(SpText(ob[0..on], "3 0 <none>\n"))
    tl.Close()
    f.Close(io).Must()
    fs.Remove(io, fp).Must()
}

test "Command.handles: Spawn refuses an MK_HANDLES entry, 65 handles and a pipe end another Spawn returned with Os(0), and a Stream or a Unix-domain listener on Windows as Unsupported" (sys: &System) {
    if sys.args.Len() > 2 {
        SpChild(sys)
    }
    io := sys.io
    var args: [3]str = [sys.args[0], sys.args[1], "cat"]
    var c := DefaultCommand
    c.args = args[..]
    var vars: [1]str = ["MK_HANDLES=3"]
    c.env = Env.Extend(vars: vars[..])
    Assert(SpIsOs(SpErr(sys, &c), 0))
    c.env = Env.Inherit
    (r, w) := Pipe(io).Must()
    var many: [65]Pass = [Pass.Reader(r: &r); 65]
    c.handles = many[..]
    Assert(SpIsOs(SpErr(sys, &c), 0))
    // A pipe end Spawn returned rides this process's poller: not lent.
    var d := DefaultCommand
    d.args = args[..]
    d.stdout = Stdio.Pipe
    (child, stdin, stdout, stderr) := Spawn(sys.arena, io, &d).Must()
    SpCloseEnds(stdin, none, stderr)
    o := stdout.Must()
    var own: [1]Pass = [Pass.Reader(r: &o)]
    var e := DefaultCommand
    e.args = args[..]
    e.handles = own[..]
    Assert(SpIsOs(SpErr(sys, &e), 0))
    o.Close()
    Assert(SpCode(child.Wait(io).Must()) == 0)
    // A connected stream: Unsupported on Windows, given on Linux (the
    // stream block below).
    tl := net.Listen(io, "127.0.0.1:0").Must()
    var ab: [32]u8 = [0; 32]
    s1 := net.Dial(io, SpAddr(mut ab, tl.Port())).Must()
    var st: [1]Pass = [Pass.Stream(s: &s1)]
    c.handles = st[..]
    if SpWin() {
        Assert(match SpErr(sys, &c) {
            Unsupported { true }
            _ { false }
        })
    }
    s1.Close()
    tl.Close()
    // A Unix-domain listener: Unsupported on Windows (the child could not
    // tell its family), given on Linux. Use a relative name in the child's
    // scratch directory so the socket address fits even with a long TEMP.
    if SpWin() {
        (file: tf, path: up) := fs.CreateTemp(sys.arena, io, ".", "mk-proc-ux-").Must()
        tf.Close(io).Must()
        fs.Remove(io, up).Must()
        // Windows 10 1803 and later (the hosts this runs on) have them.
        ul := net.ListenUnix(io, up).Must()
        var ux: [1]Pass = [Pass.Listener(l: &ul)]
        var u := DefaultCommand
        u.args = args[..]
        u.handles = ux[..]
        Assert(match SpErr(sys, &u) {
            Unsupported { true }
            _ { false }
        })
        ul.Close()
        fs.Remove(io, up).Must()
    }
    r.Close()
    w.Close()
}

test "Command.handles: a Linux child given a connected stream writes through it after the parent closed its own copy, and the peer sees the end when the child closes (no shutdown on a shared socket)" (sys: &System) {
    if sys.args.Len() > 2 {
        SpChild(sys)
    }
    if SpWin() {
        return
    }
    io := sys.io
    tl := net.Listen(io, "127.0.0.1:0").Must()
    var ab: [32]u8 = [0; 32]
    peer := net.Dial(io, SpAddr(mut ab, tl.Port())).Must()
    mine := tl.Accept(io).Must()
    var st: [1]Pass = [Pass.Stream(s: &mine)]
    var args: [3]str = [sys.args[0], sys.args[1], "handstream"]
    var c := DefaultCommand
    c.args = args[..]
    c.handles = st[..]
    (child, stdin, stdout, stderr) := Spawn(sys.arena, io, &c).Must()
    SpCloseEnds(stdin, stdout, stderr)
    mine.Close()
    var b: [32]u8 = [0; 32]
    var n := 0
    for {
        k := peer.Read(io, mut b[n..]).Must()
        if k == 0 {
            break
        }
        n += k
    }
    Assert(SpText(b[0..n], "from child"))
    Assert(SpCode(child.Wait(io).Must()) == 0)
    peer.Close()
    tl.Close()
}

test "Command.handles: a Linux child holds its three streams and exactly the handles named, as descriptors 3 and on" (sys: &System) {
    if sys.args.Len() > 2 {
        SpChild(sys)
    }
    // Linux only, as its title says: the child counts its descriptors in
    // /proc/self/fd, which macOS does not have.
    if SpWin() || Target.os == core.Os.Macos {
        return
    }
    io := sys.io
    var a: [3]str = [sys.args[0], sys.args[1], "fds"]
    var c := DefaultCommand
    c.args = a[..]
    c.stdout = Stdio.Pipe
    var b0: [64]u8 = [0; 64]
    n0 := SpOut(sys, &c, mut b0[..])
    (r, w) := Pipe(io).Must()
    var hs: [2]Pass = [Pass.Reader(r: &r), Pass.Writer(w: &w)]
    c.handles = hs[..]
    var b: [64]u8 = [0; 64]
    n := SpOut(sys, &c, mut b[..])
    Assert(n0 > 1 && SpNum(b0[0..n0]) > 0 && SpNum(b[0..n]) == SpNum(b0[0..n0]) + 2)
    r.Close()
    w.Close()
}

// SpInheritedCode is Inherited's count, -22 for Os(22), -1 for any other
// error.
SpInheritedCode(sys: &System, a: &Alloc, got: mut HeapList[net.Passed]): int {
    n := Inherited(a, sys.io, got) else e {
        return if SpIsOs(e, 22) { -22 } else { -1 }
    }
    return n
}

// SpForeignChild is the child of the foreign MK_HANDLES block (mode
// foreign, SpChild's comment); it never returns.
SpForeignChild(sys: &System): never {
    io := sys.io
    a := sys.heap.Alloc()
    var out := sys.Stdout()
    var got := HeapList[net.Passed].New(a)
    k := SpInheritedCode(sys, a, mut got)
    for got.Len() > 0 {
        got.Remove(0).Close()
    }
    got.FreeEmpty(a)
    var d: [24]u8 = [0; 24]
    if k == -22 {
        out.Str("os22") else { Exit(97) }
    } else if k < 0 {
        out.Str("error") else { Exit(97) }
    } else {
        out.Str(PtDigits(mut d, k)) else { Exit(97) }
    }
    if !SpWin() {
        out.Str(if SpFd3(io) { " fd3" } else { " nofd3" }) else { Exit(97) }
    }
    // The runtime's own handles are whole: the poller, the pool, a file.
    io.Sleep(core.DurationOf(10_000_000)) else { Exit(98) }
    (file: t, path: tp) := fs.CreateTemp(sys.arena, io, "", "mk-proc-fc-") else { Exit(98) }
    _ = t.WriteAt(io, 0, "alive".AsBytes()) else { Exit(98) }
    var b: [8]u8 = [0; 8]
    n := t.ReadAt(io, 0, mut b[..]) else { Exit(98) }
    t.Close(io) else { Exit(98) }
    fs.Remove(io, tp) else { Exit(98) }
    if !SpText(b[0..n], "alive") {
        Exit(98)
    }
    out.Str(" alive\n") else { Exit(97) }
    out.Flush() else { Exit(97) }
    Exit(0)
}

// SpFd3: descriptor 3 is open in this process (Linux's /proc/self/fd,
// Darwin's /dev/fd, where a stat of an entry is fstat of the descriptor).
SpFd3(io: Io): bool {
    _ = fs.Stat(io, if Target.os == core.Os.Macos { "/dev/fd/3" } else { "/proc/self/fd/3" }) else {
        return false
    }
    return true
}

// SpHandWriteChild is the child of the read-only file block (mode
// handwrite, SpChild's comment); it never returns.
SpHandWriteChild(sys: &System): never {
    io := sys.io
    a := sys.heap.Alloc()
    var out := sys.Stdout()
    var got := HeapList[net.Passed].New(a)
    n := Inherited(a, io, mut got) else { Exit(90) }
    if n != 2 {
        Exit(91)
    }
    ro := SpFileOf(got.Remove(0))
    rw := SpFileOf(got.Remove(0))
    got.FreeEmpty(a)
    var first := "ro wrote"
    _ = ro.WriteAt(io, 0, "AA".AsBytes()) else {
        first = "ro refused"
        0
    }
    var second := "rw wrote"
    _ = rw.WriteAt(io, 5, "BB".AsBytes()) else {
        second = "rw refused"
        0
    }
    ro.Abandon()
    rw.Abandon()
    out.Str(first) else { Exit(97) }
    out.Str(", ") else { Exit(97) }
    out.Str(second) else { Exit(97) }
    out.Str("\n") else { Exit(97) }
    out.Flush() else { Exit(97) }
    Exit(0)
}

// SpForeign runs args (a shell that is no Mk Spawn, which runs this test
// in mode foreign) with the handles hs and answers the child's line.
SpForeign(sys: &System, args: []str, hs: []Pass, buf: mut []u8): int {
    var c := DefaultCommand
    c.args = args
    c.stdout = Stdio.Pipe
    c.handles = hs
    return SpOut(sys, &c, buf)
}

test "proc.Inherited: an MK_HANDLES a program that is no Mk Spawn passed on, naming a value twice (also tagged in its low two bits on Windows), a closed or standard one, or no handle, is Os(22): nothing is taken and every handle is left alone" (sys: &System) {
    if sys.args.Len() > 2 {
        SpChild(sys)
    }
    io := sys.io
    (file: f, path: fp) := fs.CreateTemp(sys.arena, io, "", "mk-proc-fg-").Must()
    _ = f.WriteAt(io, 0, "foreign".AsBytes()).Must()
    var one: [1]Pass = [Pass.File(f: &f)]
    var b: [128]u8 = [0; 128]
    if SpWin() {
        // cmd.exe passes on its environment and its inheritable handles
        // (the named file's copy among them) to the program it starts.
        var same: [8]str = ["cmd.exe", "/d", "/c", "set", "MK_HANDLES=%MK_HANDLES%&&", sys.args[0], sys.args[1], "foreign"]
        var n := SpForeign(sys, same[..], one[..], mut b[..])
        Assert(SpText(b[0..n], "1 alive\n"))
        var twice: [8]str = ["cmd.exe", "/d", "/c", "set", "MK_HANDLES=%MK_HANDLES%,%MK_HANDLES%&&", sys.args[0], sys.args[1],
            "foreign"]
        n = SpForeign(sys, twice[..], one[..], mut b[..])
        Assert(SpText(b[0..n], "os22 alive\n"))
        // h and h + 1 name one handle (the kernel ignores the low two
        // bits), so a tagged duplicate is a value twice too.
        var tagged: [12]str = ["cmd.exe", "/d", "/v:on", "/c", "set", "/a", "T=MK_HANDLES+1>nul&&", "set",
            "MK_HANDLES=!MK_HANDLES!,!T!&&", sys.args[0], sys.args[1], "foreign"]
        n = SpForeign(sys, tagged[..], one[..], mut b[..])
        Assert(SpText(b[0..n], "os22 alive\n"))
        var gone: [8]str = ["cmd.exe", "/d", "/c", "set", "MK_HANDLES=16777212&&", sys.args[0], sys.args[1], "foreign"]
        n = SpForeign(sys, gone[..], one[0..0], mut b[..])
        Assert(SpText(b[0..n], "os22 alive\n"))
    } else {
        // sh's exec keeps the descriptors that are not close-on-exec: the
        // named file is descriptor 3 there.
        var same: [5]str = ["/bin/sh", "-c", "exec \"$0\" \"$1\" foreign", sys.args[0], sys.args[1]]
        var n := SpForeign(sys, same[..], one[..], mut b[..])
        // The child closed what it took; the runtime may reuse 3 after.
        Assert(SpHas(b[0..n], "1 ") && SpHas(b[0..n], " alive\n"))
        var twice: [5]str = ["/bin/sh", "-c", "export MK_HANDLES=3,3; exec \"$0\" \"$1\" foreign", sys.args[0], sys.args[1]]
        n = SpForeign(sys, twice[..], one[..], mut b[..])
        Assert(SpText(b[0..n], "os22 fd3 alive\n"))
        // Descriptor 3 closed: whatever the runtime opens there later is
        // its own, and stays so.
        var closed: [5]str = ["/bin/sh", "-c", "exec \"$0\" \"$1\" foreign 3<&-", sys.args[0], sys.args[1]]
        n = SpForeign(sys, closed[..], one[..], mut b[..])
        Assert(SpHas(b[0..n], "os22 ") && SpHas(b[0..n], " alive\n"))
        var low: [5]str = ["/bin/sh", "-c", "export MK_HANDLES=1; exec \"$0\" \"$1\" foreign", sys.args[0], sys.args[1]]
        n = SpForeign(sys, low[..], one[0..0], mut b[..])
        Assert(SpHas(b[0..n], "os22 ") && SpHas(b[0..n], " alive\n"))
    }
    f.Close(io).Must()
    fs.Remove(io, fp).Must()
}

test "Command.handles: a file opened read-only reaches the child read-only, and one opened for reading and writing writable" (sys: &System) {
    if sys.args.Len() > 2 {
        SpChild(sys)
    }
    io := sys.io
    (file: f, path: fp) := fs.CreateTemp(sys.arena, io, "", "mk-proc-ro-").Must()
    _ = f.WriteAt(io, 0, "0123456789".AsBytes()).Must()
    ro := fs.Open(io, fp).Must()
    var hs: [2]Pass = [Pass.File(f: &ro), Pass.File(f: &f)]
    var args: [3]str = [sys.args[0], sys.args[1], "handwrite"]
    var c := DefaultCommand
    c.args = args[..]
    c.stdout = Stdio.Pipe
    c.handles = hs[..]
    var b: [64]u8 = [0; 64]
    n := SpOut(sys, &c, mut b[..])
    Assert(SpText(b[0..n], "ro refused, rw wrote\n"))
    var got: [16]u8 = [0; 16]
    k := f.ReadAt(io, 0, mut got[..]).Must()
    Assert(SpText(got[0..k], "01234BB789"))
    ro.Close(io).Must()
    f.Close(io).Must()
    fs.Remove(io, fp).Must()
}
