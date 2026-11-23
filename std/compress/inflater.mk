// Why: a compressed stream read from a file or a socket arrives in pieces
// and may be far larger than memory, so it must be decoded as it is read.
//
// What: compress.Inflater (New, NewGzip, NewZlib, Read, Reader, Cause) for
// a raw DEFLATE stream (RFC 1951), gzip members (RFC 1952) back to back, or
// a zlib stream (RFC 1950); GzHead, GzTrail, ZlibHead and ZlibTrail parse
// the framing.
//
// How: inflate.mk's decoder runs over an input buffer and into a window: it
// stops when the input buffer ends inside an element, and Read refills the
// buffer from r; it stops when the window has no room, and Read hands the
// window's new bytes to the caller, then slides the last 32 KiB (the
// farthest a distance reaches) to the window's start. This is Go's
// compress/flate shape (dict_decoder.go) with libdeflate's decoder inside.
// A gzip header is read a byte, a skipped run or a scanned string at a
// time, so a header longer than the input buffer (FEXTRA holds up to 65,535
// bytes, FNAME and FCOMMENT any number) streams too; each member's CRC-32,
// and a zlib stream's Adler-32, runs over the output as Read hands it out,
// since the window slides. The state lives in a core.Filtered, so Reader
// can make a Reader over it with no unsafe code here.

import std.core
import std.hash

// WinHist is the history a distance may reach; WinCap the window: the
// history, 64 KiB of new output per slide, and the room of one longest
// match plus the 8 bytes a word copy may write past it.
const WinHist = 32768
const WinCap = 98570
const InCap = 16384

// IPos is a stream's place outside the decoder: inb[0..ilen] is buffered
// input whose first byte is byte base of the stream, win[rpos..d.c.op] is
// output not yet handed out, and bad is a sticky end (1 corrupt at boff,
// 2 truncated, 3 the stream ended: a gzip stream after a member, a zlib
// stream after its trailer).
struct IPos {
    ilen: int
    base: int
    rpos: int
    bad: int
    boff: int
}

// The framings an Inflater reads.
const MRaw = 0
const MGzip = 1
const MZlib = 2

// Framing phases: inside the deflate stream (the only phase of a raw
// Inflater), inside a gzip member's or the zlib header, inside a trailer.
const GBody = 0
const GHead = 1
const GTrail = 2

// The header and trailer readers' results.
const GDone = 0
const GNeed = 1
const GBad = 2

// Gzip header states past the 10 fixed bytes (states 0 to 9): FEXTRA's XLEN
// low and high bytes, its data, FNAME, FCOMMENT, FHCRC's low and high bytes.
// A zlib header has states 0 (CMF) and 1 (FLG).
const HXlenLo = 10
const HXlenHi = 11
const HExtra = 12
const HName = 13
const HComment = 14
const HCrcLo = 15
const HCrcHi = 16

// GzPos is the framing's place: the mode, the phase, the header state hs
// with the gzip flags (the zlib CMF), the bytes of FEXTRA left to skip,
// FHCRC's low byte and its stream offset, the gzip members finished, the
// CRC-32 of the header so far and of the member's output handed out so
// far with that output's length, and the Adler-32 of a zlib stream's
// output handed out so far.
struct GzPos {
    mode: int
    ph: int
    hs: int
    flg: u8
    skip: int
    lo: u32
    hoff: int
    members: int
    hcrc: hash.Crc32State
    ocrc: hash.Crc32State
    size: int
    adl: u32
}

// GzInit is the place at the start of a stream of the given mode: a header
// first unless the stream is raw.
GzInit(mode: int): GzPos {
    ph := if mode == MRaw { GBody } else { GHead }
    return GzPos(mode: mode, ph: ph, hs: 0, flg: 0, skip: 0, lo: 0, hoff: 0, members: 0,
        hcrc: hash.Crc32State.New(), ocrc: hash.Crc32State.New(), size: 0, adl: 1)
}

// IState is everything an Inflater holds. oom says the last Read could not
// take its buffers from the arena; ended says a Reader over the state
// failed, so it fails again on every later read (the Failed class of
// cancel safety).
view struct IState {
    a: &Arena
    win: ?mut []u8
    inb: ?mut []u8
    d: Dec
    p: IPos
    g: GzPos
    oom: bool
    ended: bool
}

// NewState is the state at the start of a stream of the given mode, with
// its window and input buffer taken from a when a can give both (else
// neither, and oom set, so the first Read tries again).
NewState(a: &Arena, mode: int): IState from a {
    var s := IState(a: a, win: none, inb: none, d: NewDec(0, 0), p: IPos(ilen: 0, base: 0, rpos: 0, bad: 0, boff: 0),
        g: GzInit(mode), oom: false, ended: false)
    TakeBuffers(mut s)
    return s
}

// TakeBuffers takes the window and the input buffer from s's arena, both
// or neither: a failed second allocation leaves win none and oom set, so
// a later Read tries again instead of reading as the end of the stream.
// The first buffer stays in the arena, which frees it with the rest.
TakeBuffers(s: mut IState) {
    s.oom = true
    a := s.a
    w := a.Slice[u8](WinCap) else {
        return
    }
    ib := a.Slice[u8](InCap) else {
        return
    }
    s.oom = false
    s.win = some(w)
    s.inb = some(ib)
}

/// A streaming DEFLATE, gzip or zlib decoder that reads its input from a
/// Reader. It holds about 11.5 KiB of decoder state inline, and its window
/// (98,570 bytes) and input buffer (16 KiB) in its arena. New, NewGzip and
/// NewZlib cannot fail: when the arena cannot give the buffers, Read fails
/// with OutOfMemory and tries again at its next call.
export view struct Inflater {
    f: core.Filtered[IState]
}

/// An Inflater that reads one raw DEFLATE stream (RFC 1951). It cannot
/// fail: when a cannot give the buffers, Read fails with OutOfMemory
/// instead. O(1) plus the zero-filling of the buffers; allocates the window
/// (98,570 bytes) and the input buffer (16 KiB) in a.
export Inflater.New(a: &Arena): Inflater {
    return Inflater(f: core.Filtered[IState].New(NewState(a, MRaw)))
}

/// An Inflater that reads gzip members (RFC 1952) back to back, as Gunzip
/// does. Each header is checked as Gunzip checks it, and each trailer's
/// CRC-32 and length after the member's output is handed out. A byte after
/// a member that does not start another one is Corrupt. The stream ends
/// where r ends, after a member. It cannot fail, and costs and allocates
/// as New.
export Inflater.NewGzip(a: &Arena): Inflater {
    return Inflater(f: core.Filtered[IState].New(NewState(a, MGzip)))
}

/// An Inflater that reads one zlib stream (RFC 1950), as Unzlib does. The
/// header is checked as Unzlib checks it, and the Adler-32 after the output
/// is handed out. The stream ends at its trailer: bytes after it are not
/// data, though Read may have buffered some. It cannot fail, and costs and
/// allocates as New.
export Inflater.NewZlib(a: &Arena): Inflater {
    return Inflater(f: core.Filtered[IState].New(NewState(a, MZlib)))
}

/// Decodes the next bytes into buf and returns their count: at least 1,
/// unless buf is empty or the stream has ended (0). It writes at most
/// buf.Len() bytes. It takes no Io: r holds one. A raw stream ends at its
/// final block, a zlib stream at its trailer: bytes after it are not data,
/// though Read may have buffered some.
///
/// Bytes decoded before an error are returned first, and the error then
/// repeats on every call. It fails with Truncated when r ends inside the
/// stream, with Corrupt at the stream offset of the first bad element, and
/// with OutOfMemory while the arena has not given the window and the input
/// buffer; each Read tries again. A ReadError from r is returned as it is,
/// and Read may be called again. O(input read + buf.Len()) per call; it
/// allocates nothing, except the buffers when the constructor could not
/// take them.
export Inflater.Read(mut self, r: mut Reader, buf: mut []u8): Error|ReadError!int {
    s := self.f.Mut()
    if s.win.IsNone() && buf.Len() > 0 {
        TakeBuffers(s)
    }
    return try StateRead(s, r, buf)
}

/// A Reader of the decoded stream over r, with buf as its inline buffer.
/// An empty buf makes Line and Until take one read per byte. Its Io is
/// r's.
///
/// It fails with ReadError where Read fails, and from then on on every
/// read, a ReadError from r included, so a stream never resumes past a
/// failure. Cause says which compress.Error ended it, once the caller
/// stops using the Reader. O(1) per call plus the decoding; allocates
/// nothing.
export Inflater.Reader(mut self, r: mut Reader, buf: mut []u8): Reader from self, r, buf {
    return (&InflaterPort).Reader(mut self.f, r, buf)
}

/// The compress.Error that ended the stream: Corrupt or Truncated once Read
/// or the Reader has reached it, OutOfMemory while the arena has not given
/// the buffers. none while the stream is good, after a clean end, and when
/// only the source failed. O(1); allocates nothing.
export Inflater.Cause(&self): ?Error {
    return StateCause(self.f.Ref())
}

// StateCause is Cause over the state s.
StateCause(s: &IState): ?Error {
    if s.p.bad == 1 {
        e: Error = Corrupt(offset: s.p.boff)
        return some(e)
    }
    if s.p.bad == 2 {
        e: Error = Truncated
        return some(e)
    }
    if s.oom {
        e: Error = OutOfMemory
        return some(e)
    }
    return none
}

// InflaterPort is the table of every Reader that Inflater.Reader makes.
const InflaterPort = core.FilterPort[IState].New(InflaterFill)

// InflaterFill is the Reader's read: StateRead, with every failure made
// sticky by ended.
InflaterFill(s: mut IState, src: mut Reader, dst: mut []u8): ReadError!int {
    if s.ended {
        fail ReadError
    }
    n := StateRead(s, src, dst) else {
        s.ended = true
        fail ReadError
    }
    return n
}

// StateRead is Read over the state s once its buffers are taken.
StateRead(s: mut IState, r: mut Reader, buf: mut []u8): Error|ReadError!int {
    if buf.Len() == 0 {
        return 0
    }
    if w := mut s.win {
        if ib := mut s.inb {
            return try Pump(mut s.d, mut s.p, mut s.g, mut w[..], mut ib[..], r, buf)
        }
    }
    // win and inb are set together, so neither is: the arena could not give
    // them. Never 0, which is the end.
    fail OutOfMemory
}

// Pump is Read over the window w and the input buffer ib. In the header
// and trailer phases the decoder's bit buffer is empty and d.c.ip is the
// next input byte, so one refill serves every phase.
Pump(d: mut Dec, p: mut IPos, g: mut GzPos, w: mut []u8, ib: mut []u8, r: mut Reader, buf: mut []u8): Error|ReadError!int {
    for {
        op := d.c.op
        if p.rpos < op {
            k := if op - p.rpos > buf.Len() { buf.Len() } else { op - p.rpos }
            rp := p.rpos
            (mut buf[0..k]).CopyFrom(w[rp..rp + k])
            p.rpos = rp + k
            if g.mode == MGzip {
                g.ocrc.Bytes(buf[0..k])
                g.size += k
            } else if g.mode == MZlib {
                g.adl = AdlerUpdate(g.adl, buf[0..k])
            }
            return k
        }
        if p.bad == 1 {
            fail Corrupt(offset: p.boff)
        }
        if p.bad == 2 {
            fail Truncated
        }
        if p.bad == 3 {
            return 0
        }
        if g.ph == GHead {
            hr := if g.mode == MZlib { ZlibHead(g, mut d.c, ib[0..p.ilen], p.base) } else { GzHead(g, mut d.c, ib[0..p.ilen], p.base) }
            if hr == GDone {
                // The member's deflate stream starts here; its window starts
                // at op, so no distance reaches the previous member.
                ip := d.c.ip
                d.c = Cur(bb: 0, nb: 0, ip: ip, op: op, wbase: op, eoff: 0, need: 0)
                d.phase = PHead
                d.final = false
                d.fixed = false
                d.stored = 0
                g.ph = GBody
                continue
            }
            if hr == GBad {
                p.bad = 1
                p.boff = d.c.eoff
                continue
            }
        } else if g.ph == GTrail {
            tr := if g.mode == MZlib { ZlibTrail(g, mut d.c, ib[0..p.ilen], p.base) } else { GzTrail(g, mut d.c, ib[0..p.ilen], p.base) }
            if tr == GDone && g.mode == MZlib {
                // A zlib stream is one stream: it ends at its trailer.
                p.bad = 3
                continue
            }
            if tr == GDone {
                g.members += 1
                g.ph = GHead
                g.hs = 0
                g.hcrc = hash.Crc32State.New()
                g.ocrc = hash.Crc32State.New()
                g.size = 0
                continue
            }
            if tr == GBad {
                p.bad = 1
                p.boff = d.c.eoff
                continue
            }
        } else {
            if d.phase == PDone {
                if g.mode == MRaw {
                    return 0
                }
                // Everything is handed out, so the checksum is the
                // member's or the stream's.
                d.c.ip = StreamEnd(&d.c)
                d.c.bb = 0
                d.c.nb = 0
                g.ph = GTrail
                continue
            }
            st := Run(d, ib[0..p.ilen], w)
            if st == SDone {
                continue
            }
            if st == SBad {
                p.bad = 1
                p.boff = p.base + d.c.eoff
                continue
            }
            if p.rpos < d.c.op {
                // Hand out what this run decoded before sliding the window or
                // waiting on r for more input.
                continue
            }
            if st == SRoom {
                // Everything is handed out (rpos == op). The decoder stops
                // only within 266 bytes of the end, so op > WinHist, and the
                // kept bytes and their new place do not overlap.
                shift := d.c.op - WinHist
                MoveDown(w, shift, WinHist)
                d.c.op = WinHist
                d.c.wbase = if d.c.wbase > shift { d.c.wbase - shift } else { 0 }
                p.rpos = WinHist
                continue
            }
            if st != SInput {
                p.bad = 1
                p.boff = p.base + d.c.eoff
                continue
            }
        }
        // The phase needs more input than ib holds from d.c.ip on.
        ip := d.c.ip
        k := p.ilen - ip
        MoveDown(ib, ip, k)
        p.base += ip
        d.c.ip = 0
        p.ilen = k
        if k == InCap {
            // No element of a stream is this long; unreachable.
            p.bad = 1
            p.boff = p.base
            continue
        }
        got := try r.Read(mut ib[k..InCap])
        if got == 0 {
            // r ended right after a member (3): the stream's end.
            p.bad = if g.ph == GHead && g.hs == 0 && g.members > 0 { 3 } else { 2 }
            continue
        }
        p.ilen = k + got
    }
}

// GzHead advances a member header (RFC 1952 section 2.3) over src from
// c.ip, whose byte i is stream byte base + i, with GzHeader's checks in
// GzHeader's order: GDone with c.ip after the header, GNeed with every
// byte of src consumed, GBad with c.eoff the stream offset of the bad
// byte (for FHCRC, of its first byte).
GzHead(g: mut GzPos, c: mut Cur, src: []u8, base: int): int {
    n := src.Len()
    var i := c.ip
    for {
        hs := g.hs
        if hs == HExtra {
            k := if n - i > g.skip { g.skip } else { n - i }
            g.hcrc.Bytes(src[i..i + k])
            i += k
            g.skip -= k
            if g.skip > 0 {
                c.ip = i
                return GNeed
            }
            g.hs = HName
            continue
        }
        if hs == HName || hs == HComment {
            bit: u8 = if hs == HComment { 16 } else { 8 }
            if g.flg & bit == 0 {
                g.hs = hs + 1
                continue
            }
            var j := i
            for j < n && src[j] != 0 {
                j += 1
            }
            if j == n {
                g.hcrc.Bytes(src[i..n])
                c.ip = n
                return GNeed
            }
            g.hcrc.Bytes(src[i..j + 1])
            i = j + 1
            g.hs = hs + 1
            continue
        }
        if hs == HXlenLo && g.flg & 4 == 0 {
            g.hs = HName
            continue
        }
        if hs == HCrcLo && g.flg & 2 == 0 {
            c.ip = i
            return GDone
        }
        // One byte: a fixed byte, an XLEN byte or an FHCRC byte.
        if i >= n {
            c.ip = i
            return GNeed
        }
        b := src[i]
        if hs == HCrcLo {
            g.lo = u32(b)
            g.hoff = base + i
            i += 1
            g.hs = HCrcHi
            continue
        }
        if hs == HCrcHi {
            if g.hcrc.Sum() & 0xFFFF != g.lo | (u32(b) << 8) {
                c.eoff = g.hoff
                return GBad
            }
            c.ip = i + 1
            return GDone
        }
        var bad := false
        if hs == 0 {
            bad = b != 0x1F
        } else if hs == 1 {
            bad = b != 0x8B
        } else if hs == 2 {
            bad = b != 8
        } else if hs == 3 {
            bad = b & 0xE0 != 0
            g.flg = b
        } else if hs == HXlenLo {
            g.skip = int(b)
        } else if hs == HXlenHi {
            g.skip = g.skip | (int(b) << 8)
        }
        if bad {
            c.eoff = base + i
            return GBad
        }
        g.hcrc.Bytes(src[i..i + 1])
        i += 1
        g.hs = hs + 1
    }
}

// GzTrail checks a member trailer at src[c.ip..]: the CRC-32 and the
// length mod 2^32 of the member's output. GNeed while fewer than 8 bytes
// are buffered; GBad with c.eoff the stream offset of the bad word.
GzTrail(g: mut GzPos, c: mut Cur, src: []u8, base: int): int {
    i := c.ip
    if src.Len() - i < 8 {
        return GNeed
    }
    if Le32(src, i) != g.ocrc.Sum() {
        c.eoff = base + i
        return GBad
    }
    if Le32(src, i + 4) != Trunc[u32](g.size) {
        c.eoff = base + i + 4
        return GBad
    }
    c.ip = i + 8
    return GDone
}

// ZlibHead advances the zlib header (RFC 1950 section 2.2) over src from
// c.ip, whose byte i is stream byte base + i, with Unzlib's checks:
// GDone with c.ip after the header, GNeed with src consumed, GBad with
// c.eoff the stream offset of the bad byte.
ZlibHead(g: mut GzPos, c: mut Cur, src: []u8, base: int): int {
    var i := c.ip
    for g.hs < 2 {
        if i >= src.Len() {
            c.ip = i
            return GNeed
        }
        b := src[i]
        bad := if g.hs == 0 { ZlibCmfBad(b) } else { ZlibFlgBad(g.flg, b) }
        if bad {
            c.eoff = base + i
            return GBad
        }
        g.flg = b
        i += 1
        g.hs += 1
    }
    c.ip = i
    return GDone
}

// ZlibTrail checks the zlib trailer at src[c.ip..]: the Adler-32 of the
// output, big-endian. GNeed while fewer than 4 bytes are buffered; GBad
// with c.eoff the stream offset of the trailer.
ZlibTrail(g: mut GzPos, c: mut Cur, src: []u8, base: int): int {
    i := c.ip
    if src.Len() - i < 4 {
        return GNeed
    }
    if Be32(src, i) != g.adl {
        c.eoff = base + i
        return GBad
    }
    c.ip = i + 4
    return GDone
}

// MoveDown copies b[from..from + n] to b[0..n], front to back, which is
// right when from >= 0 even if the ranges overlap; 8 bytes a step.
MoveDown(b: mut []u8, from: int, n: int) {
    if from == 0 {
        return
    }
    var i := 0
    if from >= 8 {
        for i + 8 <= n {
            x := Le64(b[..], from + i)
            PutLe64(b, i, x)
            i += 8
        }
    }
    for i < n {
        b[i] = b[from + i]
        i += 1
    }
}
