// Why: each vector form of scan.mk must give exactly its scalar form's result.
// What: tests at 16, 32 and 64 lanes against the scalar forms, on random and
// adversarial bytes, every length 0 to 300 at every offset 0 to 63, and
// 2,000 random lengths up to 5,000; and Utf16LenBytes, through its
// dispatch too, against known counts of WTF-8 pieces and of junk.
// How: the wide forms are called directly (the baseline build legalizes the
// wide vectors, so they run on any machine). Helper names start with Ts,
// because every file of std/simd is one module.

TsRand(x: u64): u64 {
    var y := x
    y ^= y << 13
    y ^= y >> 7
    y ^= y << 17
    return y
}

// TsFill fills buf by kind: 0 random bytes, 1 ASCII with one high byte at
// a position drawn from the seed, 2 UTF-8 and WTF-8 pieces (lone and
// paired surrogates, four-byte scalars, stray continuations), 3 one byte
// value repeated with a few others.
TsFill(buf: mut []u8, kind: int, seed: u64) {
    var r := seed | 1
    n := buf.Len()
    var i := 0
    for i < n {
        r = TsRand(r)
        if kind == 0 {
            buf[i] = Trunc[u8](r)
            i += 1
        } else if kind == 1 {
            buf[i] = Trunc[u8](r) & 0x7F
            i += 1
        } else if kind == 3 {
            buf[i] = if r % 7 == 0 { Trunc[u8](r >> 8) } else { 0xED }
            i += 1
        } else {
            // A piece of WTF-8 or broken UTF-8.
            pieces: [10][4]u8 = [[0x41, 0, 0, 0], [0xC3, 0xA9, 0, 0], [0xE2, 0x82, 0xAC, 0], [0xF0, 0x9F, 0x98, 0x80],
                [0xED, 0xA0, 0x80, 0], [0xED, 0xB0, 0x80, 0], [0xED, 0x9F, 0xBF, 0], [0x80, 0, 0, 0],
                [0xF4, 0x90, 0x80, 0x80], [0xE0, 0x80, 0x80, 0]]
            lens: [10]int = [1, 2, 3, 4, 3, 3, 3, 1, 4, 3]
            p := Trunc[int](r % 10)
            // Broken pieces are rare, so most texts get far before failing.
            q := if (p == 7 || p == 8 || p == 9) && (r >> 20) % 8 != 0 { 0 } else { p }
            for k in 0..lens[q] {
                if i < n {
                    buf[i] = pieces[q][k]
                    i += 1
                }
            }
        }
    }
    if kind == 1 && n > 0 && seed % 3 != 0 {
        buf[Trunc[int]((seed >> 5) % Cast[u64](n))] = 0x80 | Trunc[u8](seed)
    }
}

// TsAt is an offset or none as an int, -1 for none.
TsAt(x: ?int): int {
    if k := x {
        return k
    }
    return -1
}

TsCheck(s: []u8, b: u8) {
    a := IsAsciiScalar(s)
    Assert(IsAsciiN[16](s) == a && IsAsciiN[32](s) == a && IsAsciiN[64](s) == a)
    l := TsAt(LastIndexByteScalar(s, b))
    Assert(TsAt(LastIndexByteN[16](s, b)) == l && TsAt(LastIndexByteN[32](s, b)) == l &&
        TsAt(LastIndexByteN[64](s, b)) == l)
    w := ValidWtf8Scalar(s)
    Assert(ValidWtf8N[16](s) == w && ValidWtf8N[32](s) == w && ValidWtf8N[64](s) == w)
    // The count is defined on any bytes, and is a decoder's count
    // on WTF-8.
    u := Utf16LenScalar(s)
    Assert(Utf16LenN[16](s) == u && Utf16LenN[32](s) == u && Utf16LenN[64](s) == u)
    if w {
        Assert(u == TsUtf16Decode(s))
    }
    // ValidUtf8's lookup is ValidWtf8's first half: keep it exact too.
    v := ValidUtf8Scalar(s)
    Assert(ValidUtf8N[16](s) == v && ValidUtf8N[32](s) == v && ValidUtf8N[64](s) == v)
}

// TsUtf16Decode counts UTF-16 units scalar by scalar, a decoder's count:
// a lead below 0x80 is one byte, below 0xE0 two, below 0xF0 three (a
// surrogate's WTF-8 too), else four and two units. s is WTF-8.
TsUtf16Decode(s: []u8): int {
    var units := 0
    var i := 0
    n := s.Len()
    for i < n {
        x := s[i]
        if x < 0x80 {
            i += 1
        } else if x < 0xE0 {
            i += 2
        } else if x < 0xF0 {
            i += 3
        } else {
            i += 4
            units += 1
        }
        units += 1
    }
    return units
}

test "string kernels: every length 0 to 300 at every offset: vector forms equal the scalar forms" {
    var buf: [400]u8 = [0; 400]
    for kind in 0..4 {
        for n in 0..301 {
            TsFill(mut buf[..], kind, Cast[u64](n * 4 + kind + 1))
            for off in 0..64 {
                if off + n <= 400 {
                    TsCheck(buf[off..off + n], buf[(n * 7) % 400])
                }
            }
        }
    }
}

test "string kernels: random lengths up to 5,000: vector forms equal the scalar forms" {
    var buf: [5100]u8 = [0; 5100]
    var r: u64 = 0x9E3779B97F4A7C15
    for k in 0..2000 {
        r = TsRand(r)
        n := Trunc[int](r % 5001)
        kind := k % 4
        TsFill(mut buf[..], kind, r)
        off := Trunc[int]((r >> 32) % 64)
        TsCheck(buf[off..off + n], Trunc[u8](r >> 40))
    }
}

// TsValid fills buf with valid UTF-8 by kind: 0 all 😀, 1 all €, 2 a mix
// of the four lengths; a tail too short for the next scalar is ASCII.
TsValid(buf: mut []u8, kind: int) {
    n := buf.Len()
    var i := 0
    var k := 0
    for i < n {
        w := if kind == 0 { 4 } else if kind == 1 { 3 } else { 1 + k % 4 }
        k += 1
        if i + w > n {
            buf[i] = 0x41
            i += 1
        } else if w == 4 {
            buf[i] = 0xF0
            buf[i + 1] = 0x9F
            buf[i + 2] = 0x98
            buf[i + 3] = 0x80
            i += 4
        } else if w == 3 {
            buf[i] = 0xE2
            buf[i + 1] = 0x82
            buf[i + 2] = 0xAC
            i += 3
        } else if w == 2 {
            buf[i] = 0xC3
            buf[i + 1] = 0xA9
            i += 2
        } else {
            buf[i] = 0x61
            i += 1
        }
    }
}

test "string kernels: long valid text past every counter fold: Utf16Len at 16, 32 and 64 lanes equals the scalar form" {
    // 40,000 bytes: past 127 steps of two 64-lane vectors (16,256 bytes),
    // so each width folds its byte counters more than once.
    var buf: [40000]u8 = [0; 40000]
    for kind in 0..3 {
        TsValid(mut buf[..], kind)
        for n in [0, 1, 63, 64, 4064, 16255, 16256, 16257, 32512, 40000] {
            s := buf[0..n]
            u := Utf16LenScalar(s)
            Assert(Utf16LenN[16](s) == u && Utf16LenN[32](s) == u && Utf16LenN[64](s) == u)
            Assert(Utf16LenBytes(s) == u)
        }
        // The whole buffer is valid (a shorter prefix may cut a scalar).
        Assert(ValidWtf8Scalar(buf[..]) && ValidWtf8N[16](buf[..]) && ValidWtf8N[32](buf[..]) &&
            ValidWtf8N[64](buf[..]))
    }
}

test "string kernels: the WTF-8 rules: lone surrogates pass, a separated pair fails, UTF-8's errors fail" {
    // ED A0 80 (U+D800) alone, ED B0 80 (U+DC00) alone, and the trail then
    // the lead: WTF-8. The lead then the trail: not. Every str is WTF-8.
    goods: [5][8]u8 = [[0xED, 0xA0, 0x80, 0, 0, 0, 0, 0], [0xED, 0xB0, 0x80, 0, 0, 0, 0, 0],
        [0xED, 0xB0, 0x80, 0xED, 0xA0, 0x80, 0, 0], [0x61, 0xED, 0xA0, 0x80, 0x62, 0xED, 0xB0, 0x80],
        [0xF0, 0x9F, 0x98, 0x80, 0, 0, 0, 0]]
    good_lens: [5]int = [3, 3, 6, 8, 4]
    bads: [6][8]u8 = [[0xED, 0xA0, 0x80, 0xED, 0xB0, 0x80, 0, 0], [0xED, 0xAF, 0xBF, 0xED, 0xBF, 0xBF, 0, 0],
        [0xC0, 0x80, 0, 0, 0, 0, 0, 0], [0xED, 0xA0, 0, 0, 0, 0, 0, 0], [0xF4, 0x90, 0x80, 0x80, 0, 0, 0, 0],
        [0x80, 0, 0, 0, 0, 0, 0, 0]]
    bad_lens: [6]int = [6, 6, 2, 2, 4, 1]
    var buf: [256]u8 = [0x20; 256]
    for gi in 0..5 {
        g := goods[gi][0..good_lens[gi]]
        Assert(ValidWtf8(g) && ValidWtf8Scalar(g))
        // The same bytes at the end of a long ASCII run, through the
        // vector paths.
        for k in 0..g.Len() {
            buf[256 - g.Len() + k] = g[k]
        }
        Assert(ValidWtf8(buf[..]) && ValidWtf8N[16](buf[..]) && ValidWtf8N[64](buf[..]))
        for k in 0..g.Len() {
            buf[256 - g.Len() + k] = 0x20
        }
    }
    for bi in 0..6 {
        x := bads[bi][0..bad_lens[bi]]
        Assert(!ValidWtf8(x) && !ValidWtf8Scalar(x))
        for k in 0..x.Len() {
            buf[100 + k] = x[k]
        }
        Assert(!ValidWtf8(buf[..]) && !ValidWtf8N[16](buf[..]) && !ValidWtf8N[32](buf[..]))
        for k in 0..x.Len() {
            buf[100 + k] = 0x20
        }
    }
    Assert(Utf16Len("") == 0 && Utf16Len("a\u{E9}\u{20AC}\u{1F600}") == 5)
    Assert(IsAscii(buf[0..0]) && IsAscii(buf[..]) && !IsAscii(goods[1][0..3]))
    abc: [6]u8 = [0x61, 0x62, 0x63, 0x61, 0x62, 0x63]
    Assert(TsAt(LastIndexByte(abc[..], 0x62)) == 4 && TsAt(LastIndexByte(abc[..], 0x7A)) == -1)
}

test "string kernels: a WTF-8 pair at every offset and near the end of every length to 200, across each width's vectors, blocks and tail window" {
    // The pair (ED A0 80 ED B0 80) is found four bytes back from its trail
    // in the lookup pass (Wtf8Pair): at every offset, so the trail's second
    // byte meets every lane, the edge of each vector and of each block of
    // four, and the last window, whose four bytes before come from prev's
    // top lanes. The trail then the lead, and a lead and trail with a
    // scalar between, are WTF-8 at every offset too.
    pair: [6]u8 = [0xED, 0xA0, 0x80, 0xED, 0xB0, 0x80]
    swap: [6]u8 = [0xED, 0xB0, 0x80, 0xED, 0xA0, 0x80]
    apart: [7]u8 = [0xED, 0xAF, 0xBF, 0x61, 0xED, 0xBF, 0xBF]
    var buf: [200]u8 = [0x61; 200]
    for at in 0..195 {
        for k in 0..6 {
            buf[at + k] = pair[k]
        }
        Assert(TsWtf8All(buf[..], false))
        for k in 0..6 {
            buf[at + k] = swap[k]
        }
        Assert(TsWtf8All(buf[..], true))
        for k in 0..6 {
            buf[at + k] = 0x61
        }
    }
    for at in 0..194 {
        for k in 0..7 {
            buf[at + k] = apart[k]
        }
        Assert(TsWtf8All(buf[..], true))
        for k in 0..7 {
            buf[at + k] = 0x61
        }
    }
    // Every length 6 to 200 with the pair in its last twelve bytes: the
    // tail window then starts one to three lanes before bytes no vector
    // checked, so the pair's lead is in prev's fourth lane from the top.
    for n in 6..201 {
        lo := if n >= 12 { n - 12 } else { 0 }
        for at in lo..n - 5 {
            for k in 0..6 {
                buf[at + k] = pair[k]
            }
            Assert(TsWtf8All(buf[0..n], false))
            for k in 0..6 {
                buf[at + k] = 0x61
            }
        }
    }
}

// TsWtf8All: the scalar form, each width and the dispatched form all
// answer want for s.
TsWtf8All(s: []u8, want: bool): bool {
    return ValidWtf8Scalar(s) == want && ValidWtf8N[16](s) == want && ValidWtf8N[32](s) == want &&
        ValidWtf8N[64](s) == want && ValidWtf8(s) == want
}

test "string kernels: Utf16LenBytes counts WTF-8: a lone surrogate, a pair's four bytes and a split pair, at every vector width's edges" {
    // Each piece and its units: U+D800 and U+DC00 alone (one each), the
    // trail then the lead (two), U+1F600 (two), and a lead and trail
    // surrogate written apart, which ValidWtf8 refuses but the count still
    // takes as two three-byte sequences.
    pieces: [5][6]u8 = [[0xED, 0xA0, 0x80, 0, 0, 0], [0xED, 0xB0, 0x80, 0, 0, 0], [0xED, 0xB0, 0x80, 0xED, 0xA0, 0x80],
        [0xF0, 0x9F, 0x98, 0x80, 0, 0], [0xED, 0xA0, 0x80, 0xED, 0xB0, 0x80]]
    piece_lens: [5]int = [3, 3, 6, 4, 6]
    piece_units: [5]int = [1, 1, 2, 2, 2]
    var buf: [600]u8 = [0x61; 600]
    // Lengths about each width's single vector, its two-vector step, the
    // avx2 and avx512bw dispatch bounds, and one 64-lane counter fold.
    for n in [15, 16, 17, 31, 32, 33, 63, 64, 65, 127, 128, 129, 255, 256, 257, 511, 512, 513, 600] {
        for pi in 0..5 {
            p := pieces[pi][0..piece_lens[pi]]
            // The piece at the start, in the middle, one byte before the
            // end, and at the very end, where the tail window ends.
            for at in [0, n / 2, n - p.Len() - 1, n - p.Len()] {
                for k in 0..p.Len() {
                    buf[at + k] = p[k]
                }
                s := buf[0..n]
                want := n - p.Len() + piece_units[pi]
                Assert(Utf16LenBytes(s) == want && Utf16LenScalar(s) == want && Utf16LenN[16](s) == want &&
                    Utf16LenN[32](s) == want && Utf16LenN[64](s) == want)
                for k in 0..p.Len() {
                    buf[at + k] = 0x61
                }
            }
        }
    }
    // The bytes alone, below every vector width.
    for pi in 0..5 {
        Assert(Utf16LenBytes(pieces[pi][0..piece_lens[pi]]) == piece_units[pi])
    }
    Assert(Utf16LenBytes(buf[0..0]) == 0)
    // Bytes that are not WTF-8 count the same on every path: a
    // continuation byte nothing, another byte below 0xF0 one, and a byte
    // from 0xF0 on two.
    junk: [6]u8 = [0x80, 0xBF, 0xF8, 0xC0, 0xFF, 0xF0]
    junk_units: [6]int = [0, 0, 2, 1, 2, 2]
    Assert(Utf16LenBytes(junk[..]) == 7 && Utf16LenScalar(junk[..]) == 7)
    // Junk repeated through each width's vectors, steps and tail window,
    // and past both dispatch bounds.
    for n in [16, 17, 63, 64, 65, 255, 256, 257, 600] {
        var want := 0
        for k in 0..n {
            buf[k] = junk[k % 6]
            want += junk_units[k % 6]
        }
        s := buf[0..n]
        Assert(Utf16LenBytes(s) == want && Utf16LenScalar(s) == want && Utf16LenN[16](s) == want &&
            Utf16LenN[32](s) == want && Utf16LenN[64](s) == want)
    }
}
