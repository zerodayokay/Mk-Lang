// Why: HTTP servers and package archives send Zstandard, which decodes
// faster than DEFLATE at a better ratio; the decoder must stream from a
// Reader in bounded memory.
//
// What: compress.ZstdDecoder (New, NewLazy, Read, Reader, ReaderLazy,
// Cause) and compress.ZstdContent, over Zstandard frames (RFC 8878).
//
// How: frames and skippable frames back to back, the content size and the
// content checksum checked, no dictionaries. A block is decoded whole
// (zstdblock.mk) once its bytes are buffered, into a window of window_limit
// + 256 KiB bytes that wraps, as the reference's streaming decoder
// (ZSTD_decompressStream) does: when a block might not fit after the
// current output, and that output has been handed out, writing starts again
// at the window's start, and a match that reaches back past it reads the
// end of the previous segment, which holds at least the frame's window
// then. A frame whose window (or, for a single-segment frame, whose content
// size) is over window_limit fails with TooLarge, so the decoder never
// needs more than the memory it took at construction. Memory is the window,
// a 128 KiB input buffer and a 128 KiB literal buffer in the arena, and
// about 17 KiB inline; stack is a few frames, with no recursion. This file,
// zstdblock.mk and zstdxx.mk are the whole zstd decoder, and tests_zstd.mk
// tests it.
//
// A decoder from NewLazy takes no window at construction: each frame's
// window is taken from the arena at the frame's header, after the TooLarge
// check, at the frame's window plus two of its blocks, the same wrap
// invariant at a smaller size. A later frame whose window does not fit
// takes a new one of twice the old size, or its own size when that is
// more, at most the limit's window. An arena cannot free, so the old one
// stays: growth by doubling is what bounds a stream under three times the
// largest window it takes, and at most three times window_limit + 256
// KiB. Growth to each frame's exact size would not: frames whose windows
// grow a little each would add up quadratically. Its Reader is
// ReaderLazy, over core.FilterPort, which takes each frame's window
// from the decoder's arena.

import std.core

// ZWinDefault is the window limit a window_limit of 0 or less means (RFC
// 8878 section 3.1.1.1.2 recommends decoders support 8 MiB); ZWinMax caps
// a larger one, past every window RFC 8878 can describe.
const ZWinDefault = 8388608
const ZWinMax = 4398046511104

// ZBlockMax is the largest block (RFC 8878 section 3.1.1.2.3); ZInCap the
// input buffer: a block and its 3-byte header, with room to spare.
const ZBlockMax = 131072
const ZInCap = 135168

// Stream phases: before a frame's magic number, inside a skippable frame,
// before the frame header's first byte, before a block header, before a
// block's body, after a frame's last block (its content size and
// checksum), and after a frame header whose window is not checked yet.
const ZPMagic = 0
const ZPSkip = 1
const ZPHead = 2
const ZPBlock = 3
const ZPBody = 4
const ZPEnd = 5
const ZPWin = 6

// Sticky ends (ZState.bad): corrupt at eoff, truncated, the stream ended
// after a frame, a window over the limit.
const ZBCorrupt = 1
const ZBTruncated = 2
const ZBEnd = 3
const ZBTooLarge = 4

// ZIn is the input buffer's place: inb[0..ilen] is buffered, inb[ip] is
// the next byte, and inb[0] is byte base of the stream.
struct ZIn {
    ilen: int
    ip: int
    base: int
}

// ZFrame is the current frame: its window size and largest block, its
// content size when the header gave one, whether it has a checksum, the
// bytes decoded so far, the block being read (type, size, last, the
// stream offset of its header), and the bytes of a skippable frame left.
struct ZFrame {
    wsize: int
    bmax: int
    fcs: u64
    hasfcs: bool
    csum: bool
    produced: u64
    btype: int
    bsize: int
    blast: bool
    boff: int
    skip: u64
}

// ZState is everything a ZstdDecoder holds: the buffers, the frame's
// tables and checksum, and the place (win[z.rpos..z.op] is output not yet
// handed out; z.old is the end of the window's previous segment once it
// has wrapped in this frame, 0 before). oom says the arena could not give
// the buffers (or, lazy, the frame's window); ended says a Reader over the
// state failed, so it fails again on every later read (cancel-safety
// class Failed); lazy says the state came from NewLazy, so each
// frame's window is taken at its header.
view struct ZState {
    a: &Arena
    win: ?mut []u8
    inb: ?mut []u8
    lit: ?mut []u8
    t: ZTabs
    x: ZxState
    z: ZPos
    oom: bool
    ended: bool
    lazy: bool
}

// ZPos is the stream's place: the window limit, the frame, the input,
// the phase, the window's output end op, hand-out position rpos and old
// segment end old, the frames finished, and the sticky end with its
// offset.
struct ZPos {
    wl: int
    fr: ZFrame
    p: ZIn
    ph: int
    op: int
    rpos: int
    old: int
    frames: int
    bad: int
    eoff: int
}

// ZTake takes the window, the input buffer and the literal buffer from
// s's arena, all or none: on a failure win stays none and oom is set, so
// a later Read tries again. The window is the limit and two blocks: ZBody
// wraps only when a block might not fit, so the old segment then ends past
// the frame's window plus a block, and an offset ZSequences admits (at
// most the window plus this block's output so far) never reaches a byte
// this block has overwritten.
ZTake(s: mut ZState) {
    s.oom = true
    a := s.a
    w := a.Slice[u8](s.z.wl + 2 * ZBlockMax) else {
        return
    }
    ib := a.Slice[u8](ZInCap) else {
        return
    }
    lb := a.Slice[u8](ZBlockMax) else {
        return
    }
    s.oom = false
    s.win = some(w)
    s.inb = some(ib)
    s.lit = some(lb)
}

// ZTakeBufs is ZTake for a lazy state: the input buffer and the literal
// buffer, both or none, and no window (ZWindow takes each frame's).
ZTakeBufs(s: mut ZState) {
    s.oom = true
    a := s.a
    ib := a.Slice[u8](ZInCap) else {
        return
    }
    lb := a.Slice[u8](ZBlockMax) else {
        return
    }
    s.oom = false
    s.inb = some(ib)
    s.lit = some(lb)
}

// ZNeedWin is the window the current frame needs: its window and two of
// its blocks. ZBody wraps only when a block might not fit, so the old
// segment then ends past the frame's window plus a block, and an offset
// ZSequences admits (at most the window plus this block's output so far)
// never reaches a byte this block has overwritten. A single-segment frame
// of content size 0 needs none. ZTake's window, the limit and two of the
// largest blocks, holds every frame's.
ZNeedWin(fr: &ZFrame): int {
    return fr.wsize + 2 * fr.bmax
}

// ZWindow takes the current frame's window from s's arena: the
// first one at the frame's size; a later one, taken only when the frame's
// window does not fit the one held, at twice the held size or the frame's
// size, whichever is more, and at most the limit's window. The arena
// never frees the old one, so each window taken after the first is at
// least twice the one before it, except a last one capped at the limit,
// and the windows of a stream add up to less than three times the
// largest; exact-size growth could add up quadratically over many
// slightly growing frames. False, with oom set and the stream left before
// the frame's first block, when the arena cannot give it.
ZWindow(s: mut ZState): bool {
    need := ZNeedWin(&s.z.fr)
    var size := need
    if w := &s.win {
        if w.Len() >= need {
            return true
        }
        top := s.z.wl + 2 * ZBlockMax
        twice := if w.Len() > top / 2 { top } else { 2 * w.Len() }
        if twice > size {
            size = twice
        }
    }
    a := s.a
    w := a.Slice[u8](size) else {
        s.oom = true
        return false
    }
    s.oom = false
    s.win = some(w)
    return true
}

/// A streaming Zstandard decoder (RFC 8878) that reads its input from a
/// Reader. It holds about 17 KiB inline, and in its arena a 132 KiB input
/// buffer, a 128 KiB literal buffer and its window: from New the window
/// limit plus 256 KiB, from NewLazy one sized to the frames it reads.
/// There is no zstd encoder.
export view struct ZstdDecoder {
    f: core.Filtered[ZState]
}

/// A decoder of Zstandard frames back to back, skippable frames among
/// them. Each frame's content size and content checksum are checked. A
/// frame that names a dictionary is Corrupt: dictionaries are not
/// supported. A frame whose window, or for a single-segment frame whose
/// content size, is over window_limit bytes fails with TooLarge; a
/// window_limit of 0 or less means 8 MiB.
///
/// It cannot fail: when a cannot give the buffers, Read fails with
/// OutOfMemory instead. O(1) plus the zero-filling of the buffers;
/// allocates window_limit + 256 KiB, 132 KiB and 128 KiB in a. NewLazy
/// sizes the window to each frame instead.
export ZstdDecoder.New(a: &Arena, window_limit: int): ZstdDecoder {
    var s := ZNewState(a, window_limit, false)
    ZTake(mut s)
    return ZstdDecoder(f: core.Filtered[ZState].New(s))
}

/// As New, but the window is taken lazily, so a small frame costs a small
/// window. The constructor takes only the 132 KiB input and 128 KiB
/// literal buffers. Each frame's window is taken from a at the frame's
/// header, after the TooLarge check: the frame's window plus two of its
/// blocks. A later frame whose window does not fit the one held takes
/// twice the held one or its own, whichever is more, at most window_limit
/// + 256 KiB, so a stream holds less than three times its largest window.
///
/// A window a cannot give is OutOfMemory from Read and Cause, and the next
/// Read tries again. Read it through Read or ReaderLazy; Reader panics on
/// it. It cannot fail. O(1) plus the zero-filling of the buffers;
/// allocates 132 KiB and 128 KiB in a.
export ZstdDecoder.NewLazy(a: &Arena, window_limit: int): ZstdDecoder {
    var s := ZNewState(a, window_limit, true)
    ZTakeBufs(mut s)
    return ZstdDecoder(f: core.Filtered[ZState].New(s))
}

// ZNewState is a state before any buffer is taken: the window limit
// window_limit, 8 MiB at 0 or less and ZWinMax at most.
ZNewState(a: &Arena, window_limit: int, lazy: bool): ZState from a {
    var wl := window_limit
    if wl <= 0 {
        wl = ZWinDefault
    } else if wl > ZWinMax {
        wl = ZWinMax
    }
    fr := ZFrame(wsize: 0, bmax: 0, fcs: 0, hasfcs: false, csum: false, produced: 0, btype: 0, bsize: 0, blast: false, boff: 0, skip: 0)
    z := ZPos(wl: wl, fr: fr, p: ZIn(ilen: 0, ip: 0, base: 0), ph: ZPMagic, op: 0, rpos: 0, old: 0, frames: 0, bad: 0, eoff: 0)
    return ZState(a: a, win: none, inb: none, lit: none, t: ZNewTabs(), x: ZxNew(), z: z, oom: false, ended: false, lazy: lazy)
}

/// Decodes the next bytes into buf and returns their count: at least 1,
/// unless buf is empty or the stream has ended (0), which is when r ends
/// right after a frame.
///
/// Bytes decoded before an error are returned first, and the error then
/// repeats on every call. It fails with Truncated when r ends inside a
/// frame, or before the first one. It fails with Corrupt at the stream
/// offset of the bad element: a compressed block's header for anything
/// wrong inside the block, the last block's for a content size that does
/// not match, the checksum's for a checksum that does not. A match offset
/// that reaches past the window plus the block's output so far is Corrupt
/// too. It fails with TooLarge for a window over the limit, and with
/// OutOfMemory while the arena has not given the buffers, or for a NewLazy
/// decoder the frame's window; each Read tries again.
///
/// A ReadError from r is returned as it is, nothing is consumed, and Read
/// may be called again. O(input read + buf.Len()) per call, a block's
/// decoding included; allocates nothing, except the buffers when the
/// constructor could not take them, and for a NewLazy decoder a frame's
/// window at its header when the one held does not fit it.
export ZstdDecoder.Read(mut self, r: mut Reader, buf: mut []u8): Error|ReadError!int {
    s := self.f.Mut()
    if s.lazy {
        return try ZStateReadGrow(s, r, buf)
    }
    if s.win.IsNone() && buf.Len() > 0 {
        ZTake(s)
    }
    return try ZStateRead(s, r, buf)
}

/// A Reader of the decoded stream over r, with buf as its inline buffer,
/// for a decoder from New. An empty buf makes Line and Until take one read
/// per byte. Its Io is r's. It panics on a decoder from NewLazy, whose
/// reads take windows: use ReaderLazy for that one.
///
/// It fails with ReadError where Read fails, and from then on on every
/// read, a ReadError from r included, so a stream never resumes past a
/// failure. Cause says which compress.Error ended it. O(1) per call plus
/// the decoding; allocates nothing.
export ZstdDecoder.Reader(mut self, r: mut Reader, buf: mut []u8): Reader from self, r, buf {
    if self.f.Ref().lazy {
        Panic("ZstdDecoder.Reader: a NewLazy decoder takes its windows in its reads, so its Reader is ReaderLazy")
    }
    return (&ZstdPort).Reader(mut self.f, r, buf)
}

/// A Reader of the decoded stream, as Reader, for a decoder from New or
/// NewLazy. Its reads take a NewLazy decoder's window from the decoder's
/// arena at each frame's header as Read takes it. Its Io is r's.
///
/// It fails with ReadError where Read fails, a window the arena cannot
/// give included, and from then on on every read. Cause says which
/// compress.Error ended it. O(1) per call plus the decoding; allocates
/// what Read allocates.
export ZstdDecoder.ReaderLazy(mut self, r: mut Reader, buf: mut []u8): Reader from self, r, buf {
    return (&ZstdPortGrow).Reader(mut self.f, r, buf)
}

/// The compress.Error that ended the stream: Corrupt, Truncated or
/// TooLarge once Read or the Reader has reached it, OutOfMemory while the
/// arena has not given the buffers or a NewLazy decoder's frame window.
/// none while the stream is good, after a clean end, and when only the
/// source failed. O(1); allocates nothing.
export ZstdDecoder.Cause(&self): ?Error {
    return ZCause(self.f.Ref())
}

/// The zstd content coding (RFC 8878), token zstd: frames read by a
/// ZstdDecoder.NewLazy with an 8 MiB window limit, the limit RFC 8878 asks
/// of an HTTP client, so a small body costs a small window. A window the
/// arena cannot give is the Reader's ReadError.
export const ZstdContent = Content(name: "zstd", wrap: ZstdReader)

// ZstdReader is ZstdContent's wrap: a ZstdDecoder.NewLazy in a, and its
// ReaderLazy over src. OutOfMemory when a cannot give the decoder's buffers, as
// GzipReader. A frame's window is taken at its header, so a window a cannot
// give is the Reader's ReadError.
ZstdReader(a: &Arena, src: mut Reader, buf: mut []u8): OutOfMemory!Reader from a, src, buf {
    z := ZstdDecoder.NewLazy(a, ZWinDefault)
    // Before any read, Cause is OutOfMemory or none.
    if z.Cause().IsSome() {
        fail OutOfMemory
    }
    d := try a.Put(z)
    return d.ReaderLazy(src, buf)
}

// ZCause is Cause over the state s.
ZCause(s: &ZState): ?Error {
    if s.z.bad == ZBCorrupt {
        e: Error = Corrupt(offset: s.z.eoff)
        return some(e)
    }
    if s.z.bad == ZBTruncated {
        e: Error = Truncated
        return some(e)
    }
    if s.z.bad == ZBTooLarge {
        e: Error = TooLarge
        return some(e)
    }
    if s.oom {
        e: Error = OutOfMemory
        return some(e)
    }
    return none
}

// ZstdPort is the table of every Reader that ZstdDecoder.Reader makes.
const ZstdPort = core.FilterPort[ZState].New(ZstdFill)

// ZstdFill is the Reader's read: ZStateRead, with every failure made
// sticky by ended.
ZstdFill(s: mut ZState, src: mut Reader, dst: mut []u8): ReadError!int {
    if s.ended {
        fail ReadError
    }
    n := ZStateRead(s, src, dst) else {
        s.ended = true
        fail ReadError
    }
    return n
}

// ZstdPortGrow is the table of every Reader that ZstdDecoder.ReaderLazy
// makes.
const ZstdPortGrow = core.FilterPort[ZState].New(ZstdFillGrow)

// ZstdFillGrow is ReaderLazy's read: ZStateReadGrow, with every failure
// made sticky by ended. It may take buffers and windows from s's arena
// into s.
ZstdFillGrow(s: mut ZState, src: mut Reader, dst: mut []u8): ReadError!int {
    if s.ended {
        fail ReadError
    }
    n := ZStateReadGrow(s, src, dst) else {
        s.ended = true
        fail ReadError
    }
    return n
}

// ZStateRead is Read over the state s of a New decoder once its buffers
// are taken.
ZStateRead(s: mut ZState, r: mut Reader, buf: mut []u8): Error|ReadError!int {
    if buf.Len() == 0 {
        return 0
    }
    if w := mut s.win {
        if ib := mut s.inb {
            if lb := mut s.lit {
                n := try ZPump(mut s.z, mut s.t, mut s.x, mut w[..], mut ib[..], mut lb[..], r, buf)
                if n < 0 {
                    // Never: ZTake's window holds every frame's (ZNeedWin).
                    fail OutOfMemory
                }
                return n
            }
        }
    }
    // The three buffers are set together, so none is: the arena could not
    // give them. Never 0, which is the end.
    fail OutOfMemory
}

// ZStateReadGrow is Read over the state s, for Read on a NewLazy decoder
// and for ReaderLazy on either: it takes the buffers when the constructor
// could not, and a frame's window when ZPump stops at a header whose
// window is not held (-1). A New decoder's window holds every frame's.
ZStateReadGrow(s: mut ZState, r: mut Reader, buf: mut []u8): Error|ReadError!int {
    if buf.Len() == 0 {
        return 0
    }
    if s.inb.IsNone() {
        if s.lazy {
            ZTakeBufs(s)
        } else {
            ZTake(s)
        }
    }
    for {
        n := try ZStep(s, r, buf)
        if n >= 0 {
            return n
        }
        if !ZWindow(s) {
            // Never 0, which is the end.
            fail OutOfMemory
        }
    }
}

// ZStep is one ZPump over s's buffers and window, held or not yet (an
// empty one): -1 when the frame's window must be taken first.
ZStep(s: mut ZState, r: mut Reader, buf: mut []u8): Error|ReadError!int {
    if ib := mut s.inb {
        if lb := mut s.lit {
            if w := mut s.win {
                return try ZPump(mut s.z, mut s.t, mut s.x, mut w[..], mut ib[..], mut lb[..], r, buf)
            }
            var no: [0]u8 = []
            return try ZPump(mut s.z, mut s.t, mut s.x, mut no[..], mut ib[..], mut lb[..], r, buf)
        }
    }
    // The buffers are set together, so neither is: the arena could not
    // give them. Never 0, which is the end.
    fail OutOfMemory
}

// ZNeed makes at least k bytes buffered from p.ip on (k at most ZInCap),
// moving the buffered bytes to the buffer's start when they would not fit
// after it; false when r ends first, with what it gave kept.
ZNeed(ib: mut []u8, p: mut ZIn, r: mut Reader, k: int): ReadError!bool {
    for p.ilen - p.ip < k {
        if p.ip + k > ib.Len() {
            ip := p.ip
            n := p.ilen - ip
            MoveDown(ib, ip, n)
            p.base += ip
            p.ilen = n
            p.ip = 0
        }
        got := try r.Read(mut ib[p.ilen..ib.Len()])
        if got == 0 {
            return false
        }
        p.ilen += got
    }
    return true
}

// ZPump is Read over the window w, the input buffer ib and the literal
// buffer lb: it hands out decoded bytes, else advances the stream by one
// element (a magic number, a skipped run, a frame header, a block header,
// a block, a frame's end) and loops; -1, with nothing handed out, after a
// frame header whose window does not fit w (ZStateReadGrow takes it).
ZPump(z: mut ZPos, t: mut ZTabs, x: mut ZxState, w: mut []u8, ib: mut []u8, lb: mut []u8, r: mut Reader, buf: mut []u8): Error|ReadError!int {
    for {
        if z.rpos < z.op {
            k := if z.op - z.rpos > buf.Len() { buf.Len() } else { z.op - z.rpos }
            rp := z.rpos
            (mut buf[0..k]).CopyFrom(w[rp..rp + k])
            z.rpos = rp + k
            if z.fr.csum {
                ZxUpdate(x, buf[0..k])
            }
            return k
        }
        if z.bad == ZBCorrupt {
            fail Corrupt(offset: z.eoff)
        }
        if z.bad == ZBTruncated {
            fail Truncated
        }
        if z.bad == ZBTooLarge {
            fail TooLarge
        }
        if z.bad == ZBEnd {
            return 0
        }
        ph := z.ph
        if ph == ZPWin {
            // A frame header was read: its blocks need a window of
            // ZNeedWin bytes, which the caller takes when w is smaller.
            if w.Len() < ZNeedWin(&z.fr) {
                return -1
            }
            z.ph = ZPBlock
            continue
        }
        if ph == ZPMagic {
            ok1 := try ZNeed(ib, mut z.p, r, 4)
            if !ok1 {
                z.bad = if z.p.ilen == z.p.ip && z.frames > 0 { ZBEnd } else { ZBTruncated }
                continue
            }
            ip := z.p.ip
            magic := Le32(ib[..], ip)
            if magic == 0xFD2FB528 {
                z.p.ip = ip + 4
                z.ph = ZPHead
                continue
            }
            if magic & 0xFFFFFFF0 == 0x184D2A50 {
                ok2 := try ZNeed(ib, mut z.p, r, 8)
                if !ok2 {
                    z.bad = ZBTruncated
                    continue
                }
                ip2 := z.p.ip
                z.fr.skip = u64(Le32(ib[..], ip2 + 4))
                z.p.ip = ip2 + 8
                z.ph = ZPSkip
                continue
            }
            z.bad = ZBCorrupt
            z.eoff = z.p.base + ip
            continue
        }
        if ph == ZPSkip {
            if z.fr.skip == 0 {
                z.frames += 1
                z.ph = ZPMagic
                continue
            }
            ok3 := try ZNeed(ib, mut z.p, r, 1)
            if !ok3 {
                z.bad = ZBTruncated
                continue
            }
            have := Trunc[u64](z.p.ilen - z.p.ip)
            k := if have > z.fr.skip { z.fr.skip } else { have }
            z.p.ip += Trunc[int](k)
            z.fr.skip -= k
            continue
        }
        if ph == ZPHead {
            ok4 := try ZNeed(ib, mut z.p, r, 1)
            if !ok4 {
                z.bad = ZBTruncated
                continue
            }
            ip := z.p.ip
            fhd := int(ib[ip])
            ss := fhd & 0x20 != 0
            fcsf := fhd >> 6
            var fcsz := if fcsf == 0 { 0 } else { 1 << fcsf }
            if fcsf == 0 && ss {
                fcsz = 1
            }
            if fhd & 8 != 0 {
                // The reserved bit, refused before the fields are read.
                z.bad = ZBCorrupt
                z.eoff = z.p.base + ip
                continue
            }
            didf := fhd & 3
            didz := if didf == 0 { 0 } else { 1 << (didf - 1) }
            wdz := if ss { 0 } else { 1 }
            ok5 := try ZNeed(ib, mut z.p, r, 1 + wdz + didz + fcsz)
            if !ok5 {
                z.bad = ZBTruncated
                continue
            }
            ip2 := z.p.ip
            ZHeader(z, t, x, ib[..], ip2, ss, wdz, didz, fcsz)
            continue
        }
        if ph == ZPBlock {
            ok6 := try ZNeed(ib, mut z.p, r, 3)
            if !ok6 {
                z.bad = ZBTruncated
                continue
            }
            ip := z.p.ip
            h := int(ib[ip]) | (int(ib[ip + 1]) << 8) | (int(ib[ip + 2]) << 16)
            z.fr.blast = h & 1 != 0
            z.fr.btype = (h >> 1) & 3
            z.fr.bsize = h >> 3
            z.fr.boff = z.p.base + ip
            if z.fr.btype == 3 || z.fr.bsize > z.fr.bmax {
                z.bad = ZBCorrupt
                z.eoff = z.fr.boff
                continue
            }
            z.p.ip = ip + 3
            z.ph = ZPBody
            continue
        }
        if ph == ZPBody {
            need := if z.fr.btype == 1 { 1 } else { z.fr.bsize }
            ok7 := try ZNeed(ib, mut z.p, r, need)
            if !ok7 {
                z.bad = ZBTruncated
                continue
            }
            ZBody(z, t, w, ib[0..z.p.ilen], lb, need)
            continue
        }
        // ZPEnd: the frame's output is all handed out, so the checksum
        // covers it.
        if z.fr.hasfcs && z.fr.produced != z.fr.fcs {
            z.bad = ZBCorrupt
            z.eoff = z.fr.boff
            continue
        }
        if z.fr.csum {
            ok8 := try ZNeed(ib, mut z.p, r, 4)
            if !ok8 {
                z.bad = ZBTruncated
                continue
            }
            ip := z.p.ip
            if Le32(ib[..], ip) != Trunc[u32](ZxDigest(&*x)) {
                z.bad = ZBCorrupt
                z.eoff = z.p.base + ip
                continue
            }
            z.p.ip = ip + 4
        }
        z.frames += 1
        z.ph = ZPMagic
    }
}

// ZHeader reads the frame header whose descriptor is ib[ip] and whose
// fields (a window descriptor of wdz bytes, a dictionary ID of didz, a
// content size of fcsz) follow it, all buffered (RFC 8878 section
// 3.1.1.1), and starts the frame; or sets the sticky end: Corrupt at the
// dictionary ID for one that is not 0, TooLarge for a window over the
// limit. (ZPump refuses the descriptor's reserved bit before.)
ZHeader(z: mut ZPos, t: mut ZTabs, x: mut ZxState, ib: []u8, ip: int, ss: bool, wdz: int, didz: int, fcsz: int) {
    fhd := ib[ip]
    var j := ip + 1
    var wsize: u64 = 0
    if wdz == 1 {
        wd := ib[j]
        wlog := u64(wd >> 3) + 10
        wbase := u64(1) << wlog
        wsize = wbase + (wbase >> 3) * u64(wd & 7)
        j += 1
    }
    for d in 0..didz {
        if ib[j + d] != 0 {
            z.bad = ZBCorrupt
            z.eoff = z.p.base + j
            return
        }
    }
    j += didz
    var fcs: u64 = 0
    if fcsz == 1 {
        fcs = u64(ib[j])
    } else if fcsz == 2 {
        fcs = 256 + (u64(ib[j]) | (u64(ib[j + 1]) << 8))
    } else if fcsz == 4 {
        fcs = u64(Le32(ib, j))
    } else if fcsz == 8 {
        fcs = Le64(ib, j)
    }
    j += fcsz
    if ss {
        wsize = fcs
    }
    if wsize > Trunc[u64](z.wl) {
        z.bad = ZBTooLarge
        return
    }
    ws := Trunc[int](wsize)
    z.fr = ZFrame(wsize: ws, bmax: if ws < ZBlockMax { ws } else { ZBlockMax }, fcs: fcs, hasfcs: fcsz > 0,
        csum: fhd & 4 != 0, produced: 0, btype: 0, bsize: 0, blast: false, boff: 0, skip: 0)
    ZResetTabs(t)
    *x = ZxNew()
    z.op = 0
    z.rpos = 0
    z.old = 0
    z.p.ip = j
    z.ph = ZPWin
}

// ZBody decodes the buffered block body ib[z.p.ip..z.p.ip + need] (a raw
// block's bytes, an RLE block's byte, a compressed block) into the window
// at z.op, first wrapping the window when the block might not fit (every
// byte before z.op is handed out here), and checks the frame's content
// size; or sets the sticky Corrupt at the block's header.
ZBody(z: mut ZPos, t: mut ZTabs, w: mut []u8, ib: []u8, lb: mut []u8, need: int) {
    fr := z.fr
    if z.op + fr.bmax > w.Len() {
        z.old = z.op
        z.op = 0
        z.rpos = 0
    }
    op := z.op
    ip := z.p.ip
    var n := 0
    if fr.btype == 0 {
        n = fr.bsize
        (mut w[op..op + n]).CopyFrom(ib[ip..ip + n])
    } else if fr.btype == 1 {
        n = fr.bsize
        (mut w[op..op + n]).Fill(ib[ip])
    } else {
        hist := if fr.produced > Trunc[u64](fr.wsize) { fr.wsize } else { Trunc[int](fr.produced) }
        q := ZBlock(t, ib, ip, ip + need, lb, w, op, hist, z.old, fr.bmax)
        if q < 0 {
            z.bad = ZBCorrupt
            z.eoff = fr.boff
            return
        }
        n = q - op
    }
    nn := Trunc[u64](n)
    if fr.hasfcs && nn > fr.fcs - fr.produced {
        z.bad = ZBCorrupt
        z.eoff = fr.boff
        return
    }
    z.fr.produced = fr.produced + nn
    z.op = op + n
    z.p.ip = ip + need
    z.ph = if fr.blast { ZPEnd } else { ZPBlock }
}
