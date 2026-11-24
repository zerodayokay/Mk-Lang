// Why: coverage counts are only meaningful with the process's load bias,
// which only the running process knows, so the reader lives in std, not in
// the driver. The fuzz loop also reads the counts live.
//
// What: CoverStart, CoverLive, CoverLoad and CoverFlush, with the writer
// thread CoverLoop and its file helpers (CoverPath, CoverExists, CoverMark).
//
// How: codegen puts the counters in their own section (.mkcc on COFF,
// mk_cnts on ELF; internal/codegen/cover.go); this file finds them, writes
// them to a file for the driver, and serves them to std.testing's Guided.
//
// The driver finds the counter section in the linked executable and
// passes it in MK_COVER as four fields: the section's address and size at
// the image's link-time base, that base (the image's ImageBase on COFF,
// its lowest PT_LOAD address on ELF), and the file the counts go to ("-"
// for none: the fuzz loop reads them live). The running image's own low
// bound (Rt.image_lo, sys.ImageBounds at Init) less that base is the load
// bias, so the reader needs no section sentinel and no __start_ symbol,
// which a build without --cover would leave undefined.
//
// The counts file is the counters as they are, n little-endian u64s,
// written whole each time (WriteSync at offset 0): when the test runner's
// Main ends (CoverFlush, both of its exits), every 100 ms from a thread of
// its own, and at once when the driver asks before it stops a child on its
// timeout (it creates <file>.req; the thread writes the counts, then
// <file>.ack). A panic ends the process with the last periodic write: the
// counts of its last 100 ms may be missing, and a test that panics within
// its first 100 ms reports nothing at all. The file is opened without
// truncation (always the counts' size), so a kill mid-write leaves the
// last whole counts. A runner a test re-executes as its own child finds
// the file's .owner mark and counts nothing.

import std.core
import std.raw
import std.sys

// CoverBase and CoverCount are the counters' address in this process
// and their number, set once by CoverStart; CoverOn is 1 after them.
var CoverBase: raw.Atomic[u64] = raw.Atomic[u64].New(0)
var CoverCount: raw.Atomic[u64] = raw.Atomic[u64].New(0)
var CoverOn: raw.Atomic[u32] = raw.Atomic[u32].New(0)

// CoverField reads the unsigned number at b[at..] (hexadecimal after 0x,
// else decimal) and returns it with the offset after it and the spaces
// after that.
CoverField(b: []u8, at: int): (v: uint, end: int) {
    var i := at
    var v: uint = 0
    if i + 1 < b.Len() && b[i] == 48 && (b[i + 1] == 120 || b[i + 1] == 88) {
        i += 2
        for i < b.Len() && b[i] != 32 {
            c := b[i]
            var d: uint = 16
            if c >= 48 && c <= 57 {
                d = Trunc[uint](c - 48)
            } else if c >= 97 && c <= 102 {
                d = Trunc[uint](c - 87)
            } else if c >= 65 && c <= 70 {
                d = Trunc[uint](c - 55)
            }
            if d == 16 {
                break
            }
            v = (v << 4) | d
            i += 1
        }
    } else {
        for i < b.Len() && b[i] >= 48 && b[i] <= 57 {
            v = v *% 10 +% Trunc[uint](b[i] - 48)
            i += 1
        }
    }
    for i < b.Len() && b[i] == 32 {
        i += 1
    }
    return (v: v, end: i)
}

/// CoverStart reads MK_COVER and, when it names a counter section, records
/// where the counters are in this process and, unless the file is "-",
/// starts the thread that writes them. The test runner's Main calls it
/// first; without MK_COVER (no --cover, no fuzz guidance) it does nothing.
/// O(1) besides one thread start; allocates nothing on the heap.
export CoverStart() {
    spec := Env("MK_COVER") else {
        return
    }
    b := spec.AsBytes()
    a := CoverField(b, 0)
    size := CoverField(b, a.end)
    link_base := CoverField(b, size.end)
    // Edge: Init's store of the image bounds, before Main (and so before
    // this call) on the main thread.
    lo := Trunc[uint](Rt.image_lo.Load())
    if a.v == 0 || size.v == 0 || lo == 0 {
        return
    }
    // A runner a test re-executes inherits MK_COVER: the counts file's
    // owner mark tells it the file is its parent's, and it counts nothing.
    if CoverFileSet() {
        if CoverExists(".owner") {
            return
        }
        CoverMark(".owner")
    }
    // Edge: none yet; CoverOn's Store of 1 below publishes this word.
    // The load bias wraps: an image mapped below its link-time base gives
    // the same sum modulo 2^64.
    CoverBase.Store(u64(a.v +% (lo -% link_base.v)))
    // Edge: as CoverBase's, published by CoverOn's Store of 1 below.
    CoverCount.Store(u64(size.v / 8))
    // Edge: the two stores above come before this one; CoverLive and
    // CoverFlush read them after loading 1.
    CoverOn.Store(1)
    if !CoverFileSet() {
        return
    }
    unsafe "CoverLoop takes no argument" {
        _ = sys.ThreadStart(CoverLoop, raw.Null[u8](), 65536)
    }
}

/// CoverLive is the counters of this process, n of them at base, or (0, 0)
/// without MK_COVER. Read each with CoverLoad. O(1); allocates nothing.
export CoverLive(): (base: uint, n: int) {
    // Edge: CoverStart's Store of 1 after the fields.
    if CoverOn.Load() != 1 {
        return (base: 0, n: 0)
    }
    return (base: Trunc[uint](CoverBase.Load()), n: Trunc[int](CoverCount.Load()))
}

/// CoverLoad is counter i of base, read as the increments write it
/// (relaxed). i is below CoverLive's n. O(1); allocates nothing.
export CoverLoad(base: uint, i: int): u64 {
    unsafe "base is CoverLive's, the counter section of this image; i is below its n" {
        p := raw.FromAddr[raw.Atomic[u64]](base + Trunc[uint](i * 8))
        // Edge: none; a relaxed read of a counter the increments write
        // relaxed (internal/codegen/cover.go), a count that may lag.
        return (*p).LoadOrd(0)
    }
}

struct CoverCmpWords {
    on: raw.Atomic[u32]
    idx: raw.Atomic[u32]
    a: [64]raw.Atomic[u64]
    b: [64]raw.Atomic[u64]
    w: [64]raw.Atomic[u32]
}

var CoverCmps: CoverCmpWords = CoverCmpWords(
    on: raw.Atomic[u32].New(0),
    idx: raw.Atomic[u32].New(0),
    a: [raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0),
        raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0),
        raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0),
        raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0),
        raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0),
        raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0),
        raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0),
        raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0)],
    b: [raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0),
        raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0),
        raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0),
        raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0),
        raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0),
        raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0),
        raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0),
        raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0)],
    w: [raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0),
        raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0),
        raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0),
        raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0),
        raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0),
        raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0),
        raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0),
        raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0), raw.Atomic[u32].New(0)],
)

/// CoverCmpEnable turns live integer-comparison recording on or off for fuzz guidance.
/// O(1); allocates nothing.
export CoverCmpEnable(on: bool) {
    CoverCmps.on.StoreOrd(if on { 1 } else { 0 }, 0)
}

/// CoverCmp records a failed 2, 4 or 8-byte integer comparison operand pair
/// into the live 64-entry ring buffer while fuzz guidance is active.
/// O(1); allocates nothing.
export CoverCmp(a: u64, b: u64, width: u8) {
    if a == b || CoverCmps.on.LoadOrd(0) == 0 {
        return
    }
    slot := Trunc[int](CoverCmps.idx.AddOrd(1, 0) & 63)
    CoverCmps.a[slot].StoreOrd(a, 0)
    CoverCmps.b[slot].StoreOrd(b, 0)
    CoverCmps.w[slot].StoreOrd(Trunc[u32](width), 0)
}

/// CoverCmpReset clears the live comparison ring buffer before each fuzz call.
/// O(1); allocates nothing.
export CoverCmpReset() {
    prev := CoverCmps.idx.LoadOrd(0)
    if prev != 0 {
        limit := if prev > 64 { 64 } else { Trunc[int](prev) }
        for i in 0..limit {
            CoverCmps.w[i].StoreOrd(0, 0)
        }
        CoverCmps.idx.StoreOrd(0, 0)
    }
}

/// CoverCmpCount returns the number of entries recorded in the live comparison ring.
/// O(1); allocates nothing.
export CoverCmpCount(): int {
    n := CoverCmps.idx.LoadOrd(0)
    if n > 64 {
        return 64
    }
    return Trunc[int](n)
}

/// CoverCmpAt returns the operand pair at slot i of the live comparison ring.
/// O(1); allocates nothing.
export CoverCmpAt(i: int): (a: u64, b: u64) {
    slot := i & 63
    return (a: CoverCmps.a[slot].LoadOrd(0), b: CoverCmps.b[slot].LoadOrd(0))
}

/// CoverCmpWidthAt returns the byte width (2, 4, or 8, or 0 if unset) at slot i.
/// O(1); allocates nothing.
export CoverCmpWidthAt(i: int): u8 {
    return Trunc[u8](CoverCmps.w[i & 63].LoadOrd(0))
}

// CoverFile is MK_COVER's last field, the counts file's path; "" when
// unset or "-".
CoverFile(): str {
    spec := Env("MK_COVER") else {
        return ""
    }
    b := spec.AsBytes()
    a := CoverField(b, 0)
    size := CoverField(b, a.end)
    base := CoverField(b, size.end)
    f := spec[base.end..]
    return if f == "-" { "" } else { f }
}

// CoverFileSet reports whether the counts go to a file.
CoverFileSet(): bool {
    return CoverFile().Len() > 0
}

// CoverPath is the counts file's path with suffix appended, in out; its
// length, 0 when it does not fit.
CoverPath(out: mut []u8, suffix: str): int {
    var k := 0
    for c in CoverFile().AsBytes() {
        if k >= out.Len() {
            return 0
        }
        out[k] = c
        k += 1
    }
    for c in suffix.AsBytes() {
        if k >= out.Len() {
            return 0
        }
        out[k] = c
        k += 1
    }
    return k
}

/// CoverFlush writes the counters to the counts file, whole, at offset 0.
/// The test runner's Main calls it before each of its returns; it does
/// nothing without a counts file. O(n) bytes written; allocates nothing.
export CoverFlush() {
    // Edge: CoverStart's Store of 1 after the base and the count.
    if CoverOn.Load() != 1 {
        return
    }
    var path: [4096]u8 = [0; 4096]
    np := CoverPath(mut path[..], "")
    if np == 0 {
        return
    }
    unsafe "path holds np bytes of the driver's UTF-8 path; the counters are n u64s at base, in the image" {
        // Mode 2 opens or creates without truncating: the file is always
        // the counts' size, so a kill during a write leaves the earlier
        // counts in place, never an empty file.
        f := sys.OpenFile(raw.Str(raw.Data(path[..]), np), 2, false)
        if f.err != core.SysErr.None {
            return
        }
        h := Trunc[uint](f.value)
        // Edge: CoverOn's Load of 1 above, after CoverStart's stores.
        _ = sys.WriteSync(h, raw.Slice(raw.FromAddr[u8](Trunc[uint](CoverBase.Load())), Trunc[int](CoverCount.Load()) * 8), 0)
        _ = sys.Close(h)
    }
}

// CoverExists reports whether the file at the counts path plus suffix
// exists (it opens for reading).
CoverExists(suffix: str): bool {
    var path: [4096]u8 = [0; 4096]
    np := CoverPath(mut path[..], suffix)
    if np == 0 {
        return false
    }
    unsafe "path holds np bytes of UTF-8" {
        f := sys.OpenFile(raw.Str(raw.Data(path[..]), np), 0, false)
        if f.err != core.SysErr.None {
            return false
        }
        _ = sys.Close(Trunc[uint](f.value))
        return true
    }
}

// CoverMark creates the empty file at the counts path plus suffix.
CoverMark(suffix: str) {
    var path: [4096]u8 = [0; 4096]
    np := CoverPath(mut path[..], suffix)
    if np == 0 {
        return
    }
    unsafe "path holds np bytes of UTF-8" {
        f := sys.OpenFile(raw.Str(raw.Data(path[..]), np), 1, false)
        if f.err == core.SysErr.None {
            _ = sys.Close(Trunc[uint](f.value))
        }
    }
}

// CoverLoop is the writer thread: every 20 ms it looks for the driver's
// request, and every 100 ms it writes the counts anyway. After answering
// a request it writes nothing more and ends: the driver kills the process
// next, and a write it could cut is one write fewer.
CoverLoop(arg: *u8) {
    _ = arg
    var tick := 0
    for {
        sys.SleepNanos(20_000_000)
        tick += 1
        if CoverExists(".req") {
            CoverFlush()
            CoverMark(".ack")
            return
        }
        if tick % 5 == 0 {
            CoverFlush()
        }
    }
}
