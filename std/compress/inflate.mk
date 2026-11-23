// Why: Inflate, Gunzip and Inflater share one RFC 1951 decoder, which must
// be fast on whole inputs and still resume when a stream arrives in pieces.
//
// What: Dec and Cur (the decoder and its bit cursor), Run (decode until the
// input ends, the output is full, or the stream ends), Header, Dynamic,
// Stored, HuffLoop and SlowMatch.
//
// How: the shape is libdeflate's (deflate_decompress.c,
// decompress_template.h): a 64-bit bit buffer with the branch-free word
// refill, one table load per symbol, a fast loop with no per-symbol checks
// while 16 input bytes and 266 output bytes remain, 8-byte match copies into
// the output's slack, and an exact slow loop for the edges. Unlike libdeflate
// it is resumable at symbol boundaries: when the input ends inside a symbol
// (or inside a dynamic block header) it rewinds to that symbol's first bit
// and returns SInput, and when a symbol does not fit the output it rewinds
// the same way and returns SRoom with the room it needs. A lookup is trusted
// only when its code length is at most the bits held, so the decoder never
// asks for a byte the stream does not need.
//
// The bit buffer holds bits LSB first: bits 0 to nb - 1 are the next
// unread bits, and any bit above them is either 0 or the true value of
// the stream at that position (the word refill loads whole words and
// counts only whole bytes), so OR-ing a byte in again at nb is exact.

import std.simd

// Decoder status (Run's result).
const SOk = 0
const SDone = 1
const SInput = 2
const SRoom = 3
const SBad = 4
const SEnd = 5

// Decoder phase: before a block header, inside a stored block, inside a
// Huffman block, after the final block.
const PHead = 0
const PStored = 1
const PHuff = 2
const PDone = 3

// Cur is the position: the bit buffer bb with nb valid bits, the next
// input byte ip, the next output byte op, and wbase, the output index
// where this stream's window starts (a distance may reach back to it, no
// further). eoff is the input byte of a corrupt element after SBad, need
// the output room a symbol wants after SRoom.
struct Cur {
    bb: u64
    nb: u64
    ip: int
    op: int
    wbase: int
    eoff: int
    need: int
}

// Dec is a decoder: the dynamic tables of the current block, the position,
// and the block state. eob is the length of the dynamic block's
// end-of-block code. About 11.5 KiB, all inline: no allocation.
struct Dec {
    lit: [2342]u32
    dist: [402]u32
    c: Cur
    phase: int
    final: bool
    fixed: bool
    stored: int
    eob: u64
}

// NewDec is a decoder at the start of a stream whose input starts at ip
// and whose output (and window) starts at op.
NewDec(ip: int, op: int): Dec {
    return Dec(lit: [0; 2342], dist: [0; 402], c: Cur(bb: 0, nb: 0, ip: ip, op: op, wbase: op, eoff: 0, need: 0),
        phase: PHead, final: false, fixed: false, stored: 0, eob: 0)
}

// Le64 is the 8 bytes of b at i as a little-endian word: one sub-slice of
// constant length, which LLVM folds into one unaligned load.
Le64(b: []u8, i: int): u64 {
    w := b[i..b.Len()][0..8]
    return u64(w[0]) | (u64(w[1]) << 8) | (u64(w[2]) << 16) | (u64(w[3]) << 24) |
        (u64(w[4]) << 32) | (u64(w[5]) << 40) | (u64(w[6]) << 48) | (u64(w[7]) << 56)
}

// PutLe64 stores x little-endian at out[i..i + 8].
PutLe64(out: mut []u8, i: int, x: u64) {
    t := mut out[i..out.Len()]
    w := mut t[0..8]
    w[0] = Trunc[u8](x)
    w[1] = Trunc[u8](x >> 8)
    w[2] = Trunc[u8](x >> 16)
    w[3] = Trunc[u8](x >> 24)
    w[4] = Trunc[u8](x >> 32)
    w[5] = Trunc[u8](x >> 40)
    w[6] = Trunc[u8](x >> 48)
    w[7] = Trunc[u8](x >> 56)
}

// BitPos is the stream position of the next unread bit, in bits from src[0].
BitPos(c: &Cur): int {
    return c.ip * 8 - Trunc[int](c.nb)
}

// Need loads bytes until at least k bits are held; false when the input
// ends first (the bytes loaded stay loaded, which changes nothing read).
Need(c: mut Cur, src: []u8, k: u64): bool {
    for c.nb < k {
        if c.ip >= src.Len() {
            return false
        }
        c.bb = c.bb | (u64(src[c.ip]) << c.nb)
        c.ip += 1
        c.nb += 8
    }
    return true
}

// Take consumes and returns the next k bits (k < 57, and held).
Take(c: mut Cur, k: u64): u64 {
    v := c.bb & ((u64(1) << k) - 1)
    c.bb = c.bb >> k
    c.nb -= k
    return v
}

// Run decodes from in into out until the stream ends (SDone), the input
// ends inside an element (SInput), the output has no room for the next
// symbol (SRoom, d.c.need set), or the stream is corrupt (SBad, d.c.eoff
// set). It resumes where it stopped when called again with more input
// (the bytes from d.c.ip on unchanged) or more room.
Run(d: mut Dec, src: []u8, out: mut []u8): int {
    for {
        if d.phase == PHuff {
            r := if d.fixed {
                HuffLoop(&FixedLit, &FixedDist, 7, mut d.c, src, out)
            } else {
                HuffLoop(&d.lit, &d.dist, d.eob, mut d.c, src, out)
            }
            if r != SEnd {
                return r
            }
            d.phase = if d.final { PDone } else { PHead }
            continue
        }
        if d.phase == PHead {
            r := Header(d, src)
            if r != SOk {
                return r
            }
            continue
        }
        if d.phase == PStored {
            r := Stored(d, src, out)
            if r != SOk {
                return r
            }
            continue
        }
        return SDone
    }
}

// Header reads a block header (section 3.2.3), and for a dynamic block its
// code lengths and tables, as one step: SInput rewinds to the header.
Header(d: mut Dec, src: []u8): int {
    save := d.c
    pos := BitPos(&d.c)
    if !Need(mut d.c, src, 3) {
        d.c = save
        return SInput
    }
    d.final = Take(mut d.c, 1) == 1
    t := Take(mut d.c, 2)
    if t == 0 {
        _ = Take(mut d.c, d.c.nb & 7)
        if !Need(mut d.c, src, 32) {
            d.c = save
            return SInput
        }
        lpos := BitPos(&d.c)
        n := Take(mut d.c, 16)
        nn := Take(mut d.c, 16)
        if n ^ nn != 0xFFFF {
            d.c.eoff = lpos >> 3
            return SBad
        }
        d.stored = Trunc[int](n)
        d.phase = PStored
        return SOk
    }
    if t == 1 {
        d.fixed = true
        d.phase = PHuff
        return SOk
    }
    if t == 3 {
        d.c.eoff = pos >> 3
        return SBad
    }
    r := Dynamic(d, src)
    if r == SInput {
        d.c = save
    }
    return r
}

// Fill loads bytes while fewer than 57 bits are held and input remains.
Fill(c: mut Cur, src: []u8) {
    n := src.Len()
    for c.nb <= 56 && c.ip < n {
        c.bb = c.bb | (u64(src[c.ip]) << c.nb)
        c.ip += 1
        c.nb += 8
    }
}

// Dynamic reads HLIT, HDIST, HCLEN, the code-length code and the code
// lengths of a dynamic block (section 3.2.7), and builds its tables.
// Corrupt (as Go's compress/flate): HLIT above 286, HDIST above 30, a bad
// code-length code, a repeat with no previous length or past the end, and
// a litlen or distance code that Build refuses.
Dynamic(d: mut Dec, src: []u8): int {
    if !Need(mut d.c, src, 14) {
        return SInput
    }
    p0 := BitPos(&d.c)
    nlit := Trunc[int](Take(mut d.c, 5)) + 257
    ndist := Trunc[int](Take(mut d.c, 5)) + 1
    ncl := Trunc[int](Take(mut d.c, 4)) + 4
    if nlit > 286 || ndist > 30 {
        d.c.eoff = p0 >> 3
        return SBad
    }
    var pl: [19]u8 = [0; 19]
    for i in 0..ncl {
        if !Need(mut d.c, src, 3) {
            return SInput
        }
        pl[PreOrder[i]] = Trunc[u8](Take(mut d.c, 3))
    }
    var pt: [128]u32 = [0; 128]
    if !Build(mut pt[..], pl[..], PreBits, WPre) {
        d.c.eoff = (p0 + 14) >> 3
        return SBad
    }
    total := nlit + ndist
    var lens: [320]u8 = [0; 320]
    var i := 0
    for i < total {
        Fill(mut d.c, src)
        spos := BitPos(&d.c)
        e := pt[Trunc[int](d.c.bb & 127)]
        l := u64(e & 15)
        if l > d.c.nb {
            return SInput
        }
        if (e >> 8) & 7 == KBad {
            d.c.eoff = spos >> 3
            return SBad
        }
        _ = Take(mut d.c, l)
        sym := Trunc[int](e >> 16)
        if sym < 16 {
            lens[i] = Trunc[u8](sym)
            i += 1
            continue
        }
        var rep := 0
        var val: u8 = 0
        if sym == 16 {
            if i == 0 {
                d.c.eoff = spos >> 3
                return SBad
            }
            if !Need(mut d.c, src, 2) {
                return SInput
            }
            rep = 3 + Trunc[int](Take(mut d.c, 2))
            val = lens[i - 1]
        } else if sym == 17 {
            if !Need(mut d.c, src, 3) {
                return SInput
            }
            rep = 3 + Trunc[int](Take(mut d.c, 3))
        } else {
            if !Need(mut d.c, src, 7) {
                return SInput
            }
            rep = 11 + Trunc[int](Take(mut d.c, 7))
        }
        if i + rep > total {
            d.c.eoff = spos >> 3
            return SBad
        }
        for _ in 0..rep {
            lens[i] = val
            i += 1
        }
    }
    if !Build(mut d.lit[..], lens[0..nlit], LitBits, WLit) {
        d.c.eoff = p0 >> 3
        return SBad
    }
    if !Build(mut d.dist[..], lens[nlit..total], DistBits, WDist) {
        d.c.eoff = p0 >> 3
        return SBad
    }
    d.fixed = false
    d.eob = u64(lens[256])
    d.phase = PHuff
    return SOk
}

// Stored copies a stored block's bytes (section 3.2.4): first the whole
// bytes still in the bit buffer, then straight from in.
Stored(d: mut Dec, src: []u8, out: mut []u8): int {
    m := out.Len()
    for d.stored > 0 && d.c.nb >= 8 {
        if d.c.op >= m {
            d.c.need = 1
            return SRoom
        }
        out[d.c.op] = Trunc[u8](d.c.bb)
        d.c.bb = d.c.bb >> 8
        d.c.nb -= 8
        d.c.op += 1
        d.stored -= 1
    }
    if d.stored > 0 {
        // nb is 0 here (a stored block starts on a byte), so the bits above
        // it are the bytes about to be copied: clear them.
        d.c.bb = 0
        var k := d.stored
        if src.Len() - d.c.ip < k {
            k = src.Len() - d.c.ip
        }
        if m - d.c.op < k {
            k = m - d.c.op
        }
        ip := d.c.ip
        op := d.c.op
        (mut out[op..op + k]).CopyFrom(src[ip..ip + k])
        d.c.ip = ip + k
        d.c.op = op + k
        d.stored -= k
        if d.stored > 0 {
            if d.c.op >= m {
                d.c.need = 1
                return SRoom
            }
            return SInput
        }
    }
    d.phase = if d.final { PDone } else { PHead }
    return SOk
}

// HuffFast runs the unchecked fast loop while at least 16 input bytes and
// 280 output bytes remain. Keeping it separate from SlowMatch leaves bb, nb
// and ip as pure registers with no address-taken spills. Returns SOk when
// either buffer nears its end, SEnd at end-of-block, or SBad on bad data.
HuffFast(lit: &[2342]u32, dist: &[402]u32, c: mut Cur, src: []u8, out: mut []u8): int {
    n := src.Len()
    m := out.Len()
    var ip := c.ip
    var op := c.op
    wbase := c.wbase
    var bb := c.bb
    var nb := c.nb
    if ip < 0 || op < 0 || wbase < 0 || wbase > op || nb >= 64 {
        return SOk
    }
    nlim := n - 24
    mlim := m - 280
    // One word refill gives at least 56 bits: enough for up to 4 primary
    // literals (4 * 11 = 44 <= 56) or a full litlen+extra+dist+extra match
    // (15 + 5 + 15 + 13 = 48). 280 bytes of room cover up to 3 literals
    // plus the longest match (258) and 16-byte word-copy slack; 24 input
    // bytes cover two 16-byte vector refills in one iteration.
    for ip <= nlim && op <= mlim {
        bb = bb | (simd.Vec[u8, 16].FromSlice(src, ip).Reinterpret[u64, 2]().Lane(0) << (nb & 63))
        ip = ip + Trunc[int]((63 -% (nb & 63)) >> 3)
        nb = nb | 56
        var e := lit[Trunc[int](bb & 0x7FF)]
        if (e & 0x700) == 0 {
            l1 := u64(e & 15)
            bb = bb >> l1
            nb = nb -% l1
            out[op] = Trunc[u8](e >> 16)
            op = op + 1
            e = lit[Trunc[int](bb & 0x7FF)]
            if (e & 0x700) == 0 {
                l2 := u64(e & 15)
                bb = bb >> l2
                nb = nb -% l2
                out[op] = Trunc[u8](e >> 16)
                op = op + 1
                e = lit[Trunc[int](bb & 0x7FF)]
                if (e & 0x700) == 0 {
                    l3 := u64(e & 15)
                    bb = bb >> l3
                    nb = nb -% l3
                    out[op] = Trunc[u8](e >> 16)
                    op = op + 1
                    e = lit[Trunc[int](bb & 0x7FF)]
                    if (e & 0x700) == 0 {
                        l4 := u64(e & 15)
                        bb = bb >> l4
                        nb = nb -% l4
                        out[op] = Trunc[u8](e >> 16)
                        op = op + 1
                        continue
                    }
                }
            }
            if nb < 48 {
                bb = bb | (simd.Vec[u8, 16].FromSlice(src, ip).Reinterpret[u64, 2]().Lane(0) << (nb & 63))
                ip = ip + Trunc[int]((63 -% (nb & 63)) >> 3)
                nb = nb | 56
            }
        }
        if (e >> 8) & 7 == KSub {
            e = lit[Trunc[int](e >> 16) +% Trunc[int]((bb >> 11) & ((u64(1) << u64((e >> 4) & 15)) -% 1))]
        }
        kind := (e >> 8) & 7
        l := u64(e & 15)
        if kind == KLit {
            bb = bb >> l
            nb = nb -% l
            out[op] = Trunc[u8](e >> 16)
            op = op + 1
            continue
        }
        if kind != KLen {
            if kind == KEnd {
                bb = bb >> l
                nb = nb -% l
                c.bb = bb
                c.nb = nb
                c.ip = ip
                c.op = op
                return SEnd
            }
            c.bb = bb
            c.nb = nb
            c.ip = ip
            c.op = op
            c.eoff = (ip * 8 - Trunc[int](nb)) >> 3
            return SBad
        }
        x := u64((e >> 4) & 15)
        length := Trunc[int](e >> 16) +% Trunc[int]((bb >> l) & ((u64(1) << x) -% 1))
        bb = bb >> (l +% x)
        nb = nb -% (l +% x)
        var de := dist[Trunc[int](bb & 0xFF)]
        if (de >> 8) & 7 == KSub {
            de = dist[Trunc[int](de >> 16) +% Trunc[int]((bb >> 8) & ((u64(1) << u64((de >> 4) & 15)) -% 1))]
        }
        if (de >> 8) & 7 != KLen {
            c.bb = bb
            c.nb = nb
            c.ip = ip
            c.op = op
            c.eoff = (ip * 8 - Trunc[int](nb)) >> 3
            return SBad
        }
        ld := u64(de & 15)
        xd := u64((de >> 4) & 15)
        dst := Trunc[int](de >> 16) +% Trunc[int]((bb >> ld) & ((u64(1) << xd) -% 1))
        if dst <= 0 || dst > op - wbase {
            c.bb = bb
            c.nb = nb
            c.ip = ip
            c.op = op
            c.eoff = (ip * 8 - Trunc[int](nb)) >> 3
            return SBad
        }
        bb = bb >> (ld +% xd)
        nb = nb -% (ld +% xd)
        end := op + length
        if end > m - 16 {
            c.bb = bb
            c.nb = nb
            c.ip = ip
            c.op = op
            c.eoff = (ip * 8 - Trunc[int](nb)) >> 3
            return SBad
        }
        var s := op - dst
        var t := op
        if dst >= 8 {
            simd.Vec[u8, 8].FromSlice(out[..], s).CopyTo(out, t)
            t = t + 8
            if t < end {
                s = s + 8
                if dst >= 16 {
                    for t < end {
                        simd.Vec[u8, 16].FromSlice(out[..], s).CopyTo(out, t)
                        s = s + 16
                        t = t + 16
                    }
                } else {
                    for t < end {
                        simd.Vec[u8, 8].FromSlice(out[..], s).CopyTo(out, t)
                        s = s + 8
                        t = t + 8
                    }
                }
            }
        } else if dst == 1 {
            v := simd.Vec[u8, 16].Splat(out[s])
            for t < end {
                v.CopyTo(out, t)
                t = t + 16
            }
        } else if dst == 2 {
            b0 := out[s]
            b1 := out[s + 1]
            v := simd.Vec[u8, 16].FromArray([b0, b1, b0, b1, b0, b1, b0, b1, b0, b1, b0, b1, b0, b1, b0, b1])
            for t < end {
                v.CopyTo(out, t)
                t = t + 16
            }
        } else if dst == 4 {
            b0 := out[s]
            b1 := out[s + 1]
            b2 := out[s + 2]
            b3 := out[s + 3]
            v := simd.Vec[u8, 16].FromArray([b0, b1, b2, b3, b0, b1, b2, b3, b0, b1, b2, b3, b0, b1, b2, b3])
            for t < end {
                v.CopyTo(out, t)
                t = t + 16
            }
        } else {
            for t < end {
                out[t] = out[s]
                s = s + 1
                t = t + 1
            }
        }
        op = end
    }
    c.bb = bb
    c.nb = nb
    c.ip = ip
    c.op = op
    return SOk
}

// HuffLoop decodes the symbols of a Huffman block with the tables lit and
// dist until its end-of-block code (SEnd), or SInput, SRoom or SBad. eob
// is the length of the block's end-of-block code: a block cannot end in
// fewer bits, so with fewer held at the end of the input the stream is
// truncated whatever the next symbol is (Go's compress/flate raises its
// litlen read to that length in the same way).
HuffLoop(lit: &[2342]u32, dist: &[402]u32, eob: u64, c: mut Cur, src: []u8, out: mut []u8): int {
    n := src.Len()
    m := out.Len()
    wbase := c.wbase
    for {
        if n - c.ip >= 24 && m - c.op >= 280 {
            fst := HuffFast(lit, dist, c, src, out)
            if fst != SOk {
                return fst
            }
        }
        // The slow path: one symbol, every need checked, rewound to its
        // first bit when the input or the room runs out.
        var bb := c.bb
        var nb := c.nb
        var ip := c.ip
        var op := c.op
        sbb := bb
        snb := nb
        sip := ip
        for nb <= 56 && ip < n {
            bb = bb | (u64(src[ip]) << nb)
            ip += 1
            nb += 8
        }
        spos := ip * 8 - Trunc[int](nb)
        var e := lit[Trunc[int](bb & 0x7FF)]
        if (e >> 8) & 7 == KSub {
            e = lit[Trunc[int](e >> 16) + Trunc[int]((bb >> 11) & ((u64(1) << u64((e >> 4) & 15)) - 1))]
        }
        kind := (e >> 8) & 7
        l := u64(e & 15)
        var st := SOk
        if l > nb || eob > nb {
            st = SInput
        } else if kind == KBad {
            c.eoff = spos >> 3
            st = SBad
        } else if kind == KEnd {
            bb = bb >> l
            nb -= l
            st = SEnd
        } else if kind == KLit {
            if op >= m {
                c.need = 1
                st = SRoom
            } else {
                bb = bb >> l
                nb -= l
                out[op] = Trunc[u8](e >> 16)
                op += 1
            }
        } else {
            st = SlowMatch(dist, mut bb, mut nb, mut ip, op, wbase, e, c, src, out)
            if st == SOk {
                op += c.need
            }
        }
        if st == SInput || st == SRoom {
            bb = sbb
            nb = snb
            ip = sip
        }
        c.bb = bb
        c.nb = nb
        c.ip = ip
        c.op = op
        if st != SOk {
            return st
        }
    }
}

// SlowMatch finishes a length symbol e of the slow path: its extra bits,
// the distance code and its extra bits, every one checked, then the copy
// into out at op, byte by byte. On SOk it sets c.need to the length
// copied; on SRoom to the length wanted.
SlowMatch(dist: &[402]u32, bb: mut u64, nb: mut u64, ip: mut int, op: int, wbase: int, e: u32, c: mut Cur, src: []u8, out: mut []u8): int {
    n := src.Len()
    l := u64(e & 15)
    x := u64((e >> 4) & 15)
    if l + x > *nb {
        return SInput
    }
    length := Trunc[int](e >> 16) + Trunc[int]((*bb >> l) & ((u64(1) << x) - 1))
    *bb = *bb >> (l + x)
    *nb = *nb - (l + x)
    for *nb <= 56 && *ip < n {
        *bb = *bb | (u64(src[*ip]) << *nb)
        *ip = *ip + 1
        *nb = *nb + 8
    }
    dpos := *ip * 8 - Trunc[int](*nb)
    var de := dist[Trunc[int](*bb & 0xFF)]
    if (de >> 8) & 7 == KSub {
        de = dist[Trunc[int](de >> 16) + Trunc[int]((*bb >> 8) & ((u64(1) << u64((de >> 4) & 15)) - 1))]
    }
    ld := u64(de & 15)
    if ld > *nb {
        return SInput
    }
    if (de >> 8) & 7 != KLen {
        c.eoff = dpos >> 3
        return SBad
    }
    xd := u64((de >> 4) & 15)
    if ld + xd > *nb {
        return SInput
    }
    dst := Trunc[int](de >> 16) + Trunc[int]((*bb >> ld) & ((u64(1) << xd) - 1))
    if dst > op - wbase {
        c.eoff = dpos >> 3
        return SBad
    }
    if out.Len() - op < length {
        c.need = length
        return SRoom
    }
    *bb = *bb >> (ld + xd)
    *nb = *nb - (ld + xd)
    for k in 0..length {
        out[op + k] = out[op + k - dst]
    }
    c.need = length
    return SOk
}
