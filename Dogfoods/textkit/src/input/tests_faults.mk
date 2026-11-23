// Why: input errors must be reported, not swallowed.
// What: tests of ForEach on simulated disks with faults.
// How: Gather collects what ForEach hands on.

import std.fs
import std.sim

/// Collects every line it is given, to check what `ForEach` hands on.
struct Gather {
    arena: &Arena
    lines: List[str]
}

/// Takes every line of `r` into `g`.
Take(g: mut Gather, r: mut Reader): Error!void {
    var src := try LineSource.New(g.arena, r, 4)
    for {
        line := try src.Next(g.arena) else { break }
        try g.lines.Push(line)
    }
}

/// Runs `ForEach` over `paths` on the simulated disk of `s`, with standard
/// output and error kept in `out` and `err`. Returns the exit status.
RunOn(sys: &System, s: &sim.Sim, paths: []str, out: mut Text, err: mut Text, g: mut Gather): int {
    c := Ctx(arena: sys.arena, heap: sys.heap, io: s.Io())
    var streams := Streams(out: out.Writer(), err: err.Writer())
    var stdin := sys.Stdin()
    status := ForEach(&c, mut streams, mut stdin, paths, 8, g, fn(ctx: &Ctx, state: mut Gather, out_of: mut Streams, reader: mut Reader, name_of: str): Error!void { try Take(state, reader) })
    streams.out.Flush() else {}
    streams.err.Flush() else {}
    return status
}

test "a read fault is reported with its file, and the next file is still read" (sys: &System) {
    for seed in 0..20 {
        var s := try sim.Sim.New(sys.arena, Cast[u64](seed))
        defer s.Free()
        try fs.WriteAll(s.Io(), "a.txt", "one\ntwo\n".AsBytes())
        try fs.WriteAll(s.Io(), "b.txt", "three\n".AsBytes())
        s.FailNext(sim.Op.Read, sim.Fault.Denied)
        var out := Text.New(sys.arena)
        var err := Text.New(sys.arena)
        var g := Gather(arena: sys.arena, lines: List[str].New(sys.arena))
        paths: [2]str = ["a.txt", "b.txt"]
        status := RunOn(sys, &s, paths[..], mut out, mut err, mut g)
        Assert(status == Failed)
        Assert(err.Str() == "textkit: a.txt: ReadError\n")
        Assert(g.lines.Len() == 1)
        Assert(g.lines[0] == "three")
    }
}

test "an open fault is reported as the file system's error" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 11)
    defer s.Free()
    try fs.WriteAll(s.Io(), "a.txt", "one\n".AsBytes())
    s.FailNext(sim.Op.Open, sim.Fault.Denied)
    var out := Text.New(sys.arena)
    var err := Text.New(sys.arena)
    var g := Gather(arena: sys.arena, lines: List[str].New(sys.arena))
    paths: [2]str = ["a.txt", "a.txt"]
    status := RunOn(sys, &s, paths[..], mut out, mut err, mut g)
    Assert(status == Failed)
    Assert(err.Str() == "textkit: a.txt: Denied\n")
    Assert(g.lines.Len() == 1)
}

test "a missing file and invalid bytes give their own errors, with the line" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 3)
    defer s.Free()
    bad: [9]u8 = [b'o', b'k', b'\n', b'b', 0xC3, b'(', b'\n', b'x', b'\n']
    try fs.WriteAll(s.Io(), "bad.txt", bad[..])
    var out := Text.New(sys.arena)
    var err := Text.New(sys.arena)
    var g := Gather(arena: sys.arena, lines: List[str].New(sys.arena))
    paths: [2]str = ["gone.txt", "bad.txt"]
    status := RunOn(sys, &s, paths[..], mut out, mut err, mut g)
    Assert(status == Failed)
    Assert(err.Str() == "textkit: gone.txt: NotFound\ntextkit: bad.txt: BadUtf8(line: 2)\n")
    Assert(g.lines.Len() == 1)
    Assert(g.lines[0] == "ok")
}
