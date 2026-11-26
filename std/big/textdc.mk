// Why: printing a long Int a chunk at a time is quadratic, one dependent
// division per chunk.
//
// What: TextLarge and TextBlock, the divide-and-conquer half of Int.Text, and
// the threshold TextLeafWords.
//
// How: as Go's nat.convertWords and GMP's mpn_get_str: x is split by the
// largest power bb^(2^i) of the word chunk bb = base^k below it, near its
// square root, into a quotient and a remainder whose digits fill independent
// fixed-width blocks, down to TextLeafWords words, which the chunk loop
// converts. Its divisions are DivMag's, recursive (divrec.mk) from 60 words
// of divisor, so the whole costs O(M(n) log^2 n); below that its products
// run at multiplication's throughput rather than one dependent 2-by-1
// division per word and chunk. The recursion halves the bit length at each
// level: at most log2(n) deep.

// TextLeafWords is the length at or below which Text divides by bb word by
// word (measured: a 1000-word Text took about the same with a leaf
// of 8 to 60 words, and 23 percent longer with 120).
const TextLeafWords: int = 30

// TextLarge writes the digits of the normalized x in base ub, right-aligned
// over buf[1..], left-padded with '0', and returns the index of the first
// significant digit; its scratch lives in a sub-arena of a, freed on
// return.
TextLarge(a: &Arena, buf: mut []u8, x: []u64, ub: u64): OutOfMemory!int {
    var s := try a.Sub()
    defer s.Free()
    (k, bb) := Chunk(ub)
    // table[i] = bb^(2^i), while it is at most half of x's length.
    var table := List[[]u64].New(&s)
    first := try s.Slice[u64](1)
    first[0] = bb
    try table.Push(first[..])
    for table[table.Len() - 1].Len() * 2 <= x.Len() {
        last := table[table.Len() - 1]
        try table.Push(try MulMag(&s, last, last))
    }
    all := table.Done()
    try TextBlock(&s, mut buf[1..], x, ub, k, bb, all[..], DepthLimit)
    var pos := 1
    for pos < buf.Len() - 1 && buf[pos] == 0x30 {
        pos += 1
    }
    return pos
}

// TextBlock writes x's digits right-aligned over all of buf, left-padded
// with '0'; x < base^buf.Len(). table holds the powers it may divide by. At
// depth 0 it converts x by the leaf's chunk loop alone.
TextBlock(s: &Arena, buf: mut []u8, x: []u64, ub: u64, k: int, bb: u64, table: [][]u64, depth: u8): OutOfMemory!void {
    var q := x
    var hi := buf.Len()
    var t := table.Len() - 1
    for q.Len() > TextLeafWords && t >= 0 && depth > 0 {
        // The largest table power below q, near its square root.
        half := q.Len() / 2
        for t > 0 && table[t - 1].Len() > half {
            t -= 1
        }
        if CmpVV(table[t], q) > 0 {
            t -= 1
            if t < 0 {
                break
            }
        }
        (q: qq, r) := try DivMag(s, q, table[t])
        nd := k << t
        try TextBlock(s, mut buf[hi - nd..hi], r, ub, k, bb, table[..t], depth - 1)
        hi -= nd
        q = qq
    }
    // The leaf: one division by bb per k digits, then '0's to the left.
    work := try s.Slice[u64](q.Len())
    Copy(mut work[..], q)
    var len := q.Len()
    var pos := hi
    for len > 0 {
        r := DivInPlace(mut work[..len], bb)
        for len > 0 && work[len - 1] == 0 {
            len -= 1
        }
        pos = PutDigits(mut buf[..], pos, r, ub, k, len > 0)
    }
    for pos > 0 {
        pos -= 1
        buf[pos] = 0x30
    }
}
