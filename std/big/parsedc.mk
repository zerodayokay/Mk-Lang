// Why: Horner's rule parses d digits in O(d^2) time, too slow for numbers of
// hundreds of thousands of digits.
//
// What: ParseLarge, the subquadratic half of big.Parse, with ParseInto,
// ParseHorner, ParseBits, ParseSplit, ParseTmp and the threshold
// ParseLeafDigits.
//
// How: a base that is a power of two takes bits directly, in O(d) for d
// digits. Otherwise, past ParseLeafDigits digits, the digits are split as
// GMP's mpn_dc_set_str splits them: the low k 2^t digits, t the largest with
// k 2^t below the count (k digits a word chunk bb = base^k), and the high
// rest, so value = high * bb^(2^t) + low, each half converted the same way
// down to the leaf, where Horner's rule takes a chunk at a time. The cost is
// O(M(n) log n) word operations for n words, M the product's cost
// (Karatsuba, Toom-3). The high half has at most k 2^t digits, so t falls by
// one or more at each level: the recursion is at most log2(d / k) + 1 deep,
// below 48 for any text that fits in memory. Scratch is one sub-arena of the
// caller's arena, sized before the work starts and freed on return: the
// powers (about twice the result's words, with their squarings' scratch) and
// one scratch slice (ParseTmp): at most about 14 words of scratch per result
// word (13.3 is the most the sizing functions give for 20,001 to 3,000,000
// digits in bases 3, 7, 10 and 36).

// ParseLeafDigits is the digit count at or below which Parse uses Horner's
// rule alone (measured in base 10: Horner and the split were within noise
// of each other at 19,266 digits, the split slower below and 1.6 times
// faster at 192,659).
const ParseLeafDigits: int = 20000

// WordsFor bounds the words of any number of nd digits in base: nd log2(base)
// / 64, rounded up, plus one. nd is below 2^48 (a text's length), so the
// products stay under 2^63.
WordsFor(nd: int, base: int): int {
    return nd / 64 * Log2Base[base] / 1024 + (nd % 64) * Log2Base[base] / 65536 + 2
}

// ParseHorner sets z to the value of the digits d in base ub, k digits to
// the chunk; z holds any number of d.Len() digits.
ParseHorner(z: mut []u64, d: []u8, ub: u64, k: int) {
    Clear(z)
    var len := 0
    var j := 0
    // The first chunk takes the digits left over by whole chunks.
    var take := d.Len() % k
    if take == 0 {
        take = k
    }
    for j < d.Len() {
        var c: u64 = 0
        var p: u64 = 1
        for _ in 0..take {
            c = c * ub + DigitValue(d[j])
            p = p * ub
            j += 1
        }
        carry := MulAddInPlace(mut z[..len], p, c)
        if carry != 0 {
            z[len] = carry
            len += 1
        }
        take = k
    }
}

// ParseBits sets z to the value of the digits d in base 2^lg; z holds any
// number of d.Len() digits.
ParseBits(z: mut []u64, d: []u8, lg: int) {
    Clear(z)
    var at := 0
    var j := d.Len() - 1
    for j >= 0 {
        v := DigitValue(d[j])
        w := at >> 6
        s := at & 63
        z[w] = z[w] | (v << s)
        if s + lg > 64 {
            z[w + 1] = z[w + 1] | (v >> (64 - s))
        }
        at += lg
        j -= 1
    }
}

// ParseSplit is the t of a split of nd digits, nd > k: the largest t with
// k 2^t below nd.
ParseSplit(nd: int, k: int): int {
    var t := 0
    for (k << (t + 1)) < nd {
        t += 1
    }
    return t
}

// ParseTmp is the scratch ParseInto needs for nd digits, mirroring its
// recursion: the high half's words, then the high half's own scratch or
// the product and its multiplication scratch, whichever is larger; the low
// half's scratch is used before the high half exists.
ParseTmp(nd: int, base: int, k: int, table: [][]u64, depth: u8): int {
    if nd <= ParseLeafDigits || depth == 0 {
        return 0
    }
    t := ParseSplit(nd, k)
    pl := table[t].Len()
    lo := k << t
    hn := WordsFor(nd - lo, base)
    low := ParseTmp(lo, base, k, table, depth - 1)
    high := ParseTmp(nd - lo, base, k, table, depth - 1)
    prod := hn + pl + MulScratch(hn, pl)
    deeper := if high > prod { high } else { prod }
    inner := hn + deeper
    return if low > inner { low } else { inner }
}

// ParseInto sets z to the value of the digits d in base ub; z holds any
// number of d.Len() digits, table[i] is bb^(2^i) for every split below, and
// tmp has ParseTmp(d.Len(), depth) words. At depth 0 it takes Horner's rule.
ParseInto(z: mut []u64, d: []u8, base: int, ub: u64, k: int, table: [][]u64, tmp: mut []u64, depth: u8) {
    nd := d.Len()
    if nd <= ParseLeafDigits || depth == 0 {
        ParseHorner(z, d, ub, k)
        return
    }
    t := ParseSplit(nd, k)
    pw := table[t]
    pl := pw.Len()
    hd := nd - (k << t)
    hn := WordsFor(hd, base)
    // The low half is below pw, so it fits in pl words, and pl words fit in
    // z: pw is below base^nd.
    ParseInto(mut z[..pl], d[hd..], base, ub, k, table, mut tmp[..], depth - 1)
    Clear(mut z[pl..])
    (left: hi, right: rest) := tmp.SplitAt(hn)
    ParseInto(mut hi[..], d[..hd], base, ub, k, table, mut rest[..], depth - 1)
    (left: prod, right: mt) := rest.SplitAt(hn + pl)
    MulNat(mut prod[..], hi[..], pw, mut mt[..])
    // The sum is the value of d, so it fits in z.
    _ = AddTo(z, Norm(prod[..]))
}

// ParseLarge sets z, which holds any number of d.Len() digits, to the value
// of the digits d (more than ParseLeafDigits of them) in base ub, a base
// that is not a power of two, with its scratch in a sub-arena of a.
ParseLarge(a: &Arena, z: mut []u64, d: []u8, base: int, ub: u64): OutOfMemory!void {
    var s := try a.Sub()
    defer s.Free()
    (k, bb) := Chunk(ub)
    top := ParseSplit(d.Len(), k)
    var table := List[[]u64].New(&s)
    first := try s.Slice[u64](1)
    first[0] = bb
    try table.Push(first[..])
    for table.Len() <= top {
        last := table[table.Len() - 1]
        try table.Push(try MulMag(&s, last, last))
    }
    all := table.Done()
    tmp := try s.Slice[u64](ParseTmp(d.Len(), base, k, all[..], DepthLimit))
    ParseInto(z, d, base, ub, k, all[..], mut tmp[..], DepthLimit)
}
