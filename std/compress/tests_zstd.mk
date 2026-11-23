// Why: a NewLazy decoder's windows must stay under three times the largest
// window over any run of frames, and never pass the limit's window.
// What: test blocks of growing, shrinking and capped frame windows, with
// frame builders (ZtFrame, ZtBlocks, ZtWin) and ZtHeld, which measures the
// heap.
// How: ZtFrame and ZtBlocks build frames in memory, ZtHeld decodes them with
// NewLazy and compares the arena bytes held with those held after NewLazy.

import std.core

// ZtBytes is a source over data, read from pos on.
view struct ZtBytes {
    data: []u8
    pos: int
}

ZtBytesFill(b: mut ZtBytes, io: Io, dst: mut []u8): ReadError!int {
    var n := b.data.Len() - b.pos
    if n > dst.Len() {
        n = dst.Len()
    }
    (mut dst[0..n]).CopyFrom(b.data[b.pos..b.pos + n])
    b.pos += n
    return n
}

const ZtBytesPort = core.ReadPortMut[ZtBytes].New(ZtBytesFill)

// ZtFrame writes at s[at..at + 13] a single-segment frame of n bytes of x
// (a 4-byte content size, one last RLE block), whose window is n plus two
// blocks of n (n below 128 KiB): 3 n.
ZtFrame(s: mut []u8, at: int, n: int, x: u8) {
    s[at] = 0x28
    s[at + 1] = 0xB5
    s[at + 2] = 0x2F
    s[at + 3] = 0xFD
    s[at + 4] = 0xA0
    s[at + 5] = Trunc[u8](n & 0xFF)
    s[at + 6] = Trunc[u8]((n >> 8) & 0xFF)
    s[at + 7] = Trunc[u8]((n >> 16) & 0xFF)
    s[at + 8] = Trunc[u8]((n >> 24) & 0xFF)
    h := 1 | (1 << 1) | (n << 3)
    s[at + 9] = Trunc[u8](h & 0xFF)
    s[at + 10] = Trunc[u8]((h >> 8) & 0xFF)
    s[at + 11] = Trunc[u8]((h >> 16) & 0xFF)
    s[at + 12] = x
}

// ZtDecode reads the stream data to its end through z's Read, or through
// its ReaderLazy when lazy, and returns the bytes decoded, or -1 on a
// failure.
ZtDecode(io: Io, z: mut ZstdDecoder, data: []u8, out: mut []u8, reader: bool): int {
    var b := ZtBytes(data: data, pos: 0)
    var sb: [0]u8 = []
    var src := (&ZtBytesPort).Reader(io, mut b, mut sb[..])
    var total := 0
    if reader {
        var rb: [64]u8 = [0; 64]
        var r := z.ReaderLazy(mut src, mut rb[..])
        for {
            got := r.Read(out) else {
                return -1
            }
            if got == 0 {
                return total
            }
            total += got
        }
    }
    for {
        got := z.Read(mut src, out) else {
            return -1
        }
        if got == 0 {
            return total
        }
        total += got
    }
}

// ZtHeld decodes, with a NewLazy decoder of the default limit, frames of
// the sizes ns, and returns the arena bytes held after the decode less
// those held after NewLazy; -1 when the arena fails, -2 when the decoded
// length is wrong.
ZtHeld(h: &Heap, io: Io, ns: []int, reader: bool): int {
    var a := Arena.New(h) else {
        return -1
    }
    s := a.Slice[u8](13 * ns.Len()) else {
        a.Free()
        return -1
    }
    for i in 0..ns.Len() {
        ZtFrame(s, 13 * i, ns[i], Trunc[u8](i))
    }
    out := a.Slice[u8](65536) else {
        a.Free()
        return -1
    }
    var z := ZstdDecoder.NewLazy(&a, 0)
    base := core.ArenaHeld(&a)
    t := ZtDecode(io, mut z, s[..], out, reader)
    held := core.ArenaHeld(&a) - base
    var want := 0
    for n in ns {
        want += n
    }
    a.Free()
    if t != want {
        return -2
    }
    return held
}

// ZtSlack is what the arena's chunks may hold past the windows: rounding
// and the chunk headers, at most a chunk of 128 KiB. core.ArenaHeld counts
// whole chunks, so a window that fits the chunk in use adds nothing: the
// blocks bound the held bytes from above only, and assert the decoded
// length (ZtHeld is -2 when it is wrong, -1 when the arena fails).
const ZtSlack = 131072

// 300 frames of 100,000 to 100,299 bytes, windows of 300,000 bytes and up,
// each a little larger than the last: doubling takes a few
// windows, under three times the largest, 3 x 300,897 bytes; growth to
// each frame's exact size would take all 300, about 90 MB.
test "NewLazy holds under three of its largest windows over 300 growing frames" (sys: &System) {
    var grow: [300]int = [0; 300]
    for i in 0..300 {
        grow[i] = 100000 + i
    }
    for reader in [false, true] {
        held := ZtHeld(sys.heap, sys.io, grow[..], reader)
        Assert(held >= 0)
        Assert(held < 3 * (3 * 100299) + ZtSlack)
    }
}

// Shrink then grow (100,000, 1,000, 120,000, 1,000, 130,000), and one
// frame of 130,000 bytes alone, which takes its own window only.
test "NewLazy windows: shrink then grow, and one frame" (sys: &System) {
    sg: [5]int = [100000, 1000, 120000, 1000, 130000]
    one: [1]int = [130000]
    for reader in [false, true] {
        k := ZtHeld(sys.heap, sys.io, sg[..], reader)
        Assert(k >= 0)
        Assert(k < 3 * (3 * 130000) + ZtSlack)
        o := ZtHeld(sys.heap, sys.io, one[..], reader)
        Assert(o >= 0)
        Assert(o < 3 * 130000 + ZtSlack)
    }
}

// ZtBlocks writes at s[at0..] RLE blocks of x totalling n bytes, each at
// most bmax, the last one marked last (one empty raw block when n is 0),
// and returns the end.
ZtBlocks(s: mut []u8, at0: int, n: int, bmax: int, x: u8): int {
    var at := at0
    if n == 0 {
        s[at] = 1
        s[at + 1] = 0
        s[at + 2] = 0
        return at + 3
    }
    var m := n
    for {
        k := if m > bmax { bmax } else { m }
        last := if k == m { 1 } else { 0 }
        h := last | (1 << 1) | (k << 3)
        s[at] = Trunc[u8](h & 0xFF)
        s[at + 1] = Trunc[u8]((h >> 8) & 0xFF)
        s[at + 2] = Trunc[u8]((h >> 16) & 0xFF)
        s[at + 3] = x
        at += 4
        m -= k
        if m == 0 {
            return at
        }
    }
}

// ZtMagic writes the frame magic at s[at..at + 4].
ZtMagic(s: mut []u8, at: int) {
    s[at] = 0x28
    s[at + 1] = 0xB5
    s[at + 2] = 0x2F
    s[at + 3] = 0xFD
}

// ZtSs writes at s[at..] a single-segment frame of n bytes of x (an 8-byte
// content size, blocks of at most 128 KiB), whose window is n plus two
// blocks of min(n, 128 KiB), and returns the end.
ZtSs(s: mut []u8, at: int, n: int, x: u8): int {
    ZtMagic(s, at)
    s[at + 4] = 0xE0
    for i in 0..8 {
        s[at + 5 + i] = Trunc[u8]((n >> (8 * i)) & 0xFF)
    }
    bm := if n < 131072 { n } else { 131072 }
    return ZtBlocks(s, at + 13, n, bm, x)
}

// ZtWin writes at s[at..] a frame with window descriptor wd (a window of
// wsize bytes) and no content size, holding n bytes of x, and returns the
// end.
ZtWin(s: mut []u8, at: int, wd: int, wsize: int, n: int, x: u8): int {
    ZtMagic(s, at)
    s[at + 4] = 0x00
    s[at + 5] = Trunc[u8](wd)
    bm := if wsize < 131072 { wsize } else { 131072 }
    return ZtBlocks(s, at + 6, n, bm, x)
}

// ZtHeldStream decodes data with a NewLazy decoder of limit wl and returns
// the arena bytes held after the decode less those held after NewLazy; -1
// when the arena fails, -2 when the decoded length is not want.
ZtHeldStream(h: &Heap, io: Io, data: []u8, wl: int, want: int, reader: bool): int {
    var a := Arena.New(h) else {
        return -1
    }
    out := a.Slice[u8](65536) else {
        a.Free()
        return -1
    }
    var z := ZstdDecoder.NewLazy(&a, wl)
    base := core.ArenaHeld(&a)
    t := ZtDecode(io, mut z, data, out, reader)
    held := core.ArenaHeld(&a) - base
    a.Free()
    if t != want {
        return -2
    }
    return held
}

// The cap at the limit's window. Under a 1 MiB limit the
// largest window is top = 1 MiB + 256 KiB = 1,310,720 bytes. A frame of
// 700,000 bytes takes 962,144 (over top / 2); then a frame with a 1 MiB
// window descriptor needs top, and doubling would give 1,924,288, so the
// cap takes top: the windows add up to 962,144 + 1,310,720. Without the
// cap the windows alone are 2,886,432, over the
// bound. Frames growing by three up to the limit stay under three of top,
// 3,932,160, the doubling bound with no slack: their windows are 3,189,152
// (the decoder held 3,369,616). Without the cap their windows are 3,899,008,
// under that bound, and held 4,079,472 only through the arena's chunk
// overhead, so the first case is the one that catches a missing cap.
test "NewLazy caps a doubled window at the limit's" (sys: &System) {
    var a := Arena.New(sys.heap) else {
        Assert(false)
        return
    }
    defer a.Free()
    s := a.Slice[u8](512) else {
        Assert(false)
        return
    }
    mib := 1048576
    top := mib + 2 * 131072
    first := 700000 + 2 * 131072
    e1 := ZtSs(s, 0, 700000, 1)
    e2 := ZtWin(s, e1, 0x50, mib, 10, 2)
    var at := e2
    for n in [1000, 3000, 9000, 27000, 81000, 243000, 729000, 1048576] {
        at = ZtSs(s, at, n, 3)
    }
    for reader in [false, true] {
        held := ZtHeldStream(sys.heap, sys.io, s[0..e2], mib, 700010, reader)
        Assert(held >= 0)
        Assert(held < first + top + ZtSlack)
        g := ZtHeldStream(sys.heap, sys.io, s[e2..at], mib, 2141576, reader)
        Assert(g >= 0)
        Assert(g < 3 * top)
    }
}
