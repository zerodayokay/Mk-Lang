// Why: every HPACK and QPACK representation starts with a prefix integer,
// and every name or value is a string literal behind one; both are read
// from peer bytes, so they must be bounded and must never overflow.
//
// What: IntLen, PutInt and GetInt for prefix integers (RFC 7541 5.1, RFC
// 9204 4.1.1), and StrHead, StrCost and PutStr for string literals (RFC
// 7541 5.2, RFC 9204 4.1.2), with the prefix width as a parameter.
//
// How: a prefix of n bits (1 to 8) holds values up to 2^n - 2 itself; a
// larger value fills the prefix and continues in 7-bit groups, least
// significant first, each byte but the last with its high bit set. GetInt
// reads at most MaxIntBytes bytes and sums in u64, so no peer value can
// overflow; a string literal's Huffman flag is the bit just above its
// length's prefix.

// The most bytes a prefix integer may take, its first byte included: nine
// 7-bit groups after the prefix hold every int.
const MaxIntBytes = 10

// The largest value GetInt returns: the int range.
const MaxValue: u64 = 0x7FFF_FFFF_FFFF_FFFF

// IntLen is the length of v (at least 0) as an integer with an n-bit
// prefix, n from 1 to 8. O(log v).
IntLen(v: int, n: int): int {
    max := (1 << n) - 1
    if v < max {
        return 1
    }
    var rest := v - max
    var len := 2
    for rest >= 128 {
        rest = rest >> 7
        len += 1
    }
    return len
}

// PutInt writes v (at least 0) as an integer with an n-bit prefix into out
// from out[0], with flags (the bits above the prefix) in its first byte,
// and returns its length. out holds at least IntLen(v, n) bytes.
PutInt(out: mut []u8, v: int, n: int, flags: u8): int {
    max := (1 << n) - 1
    if v < max {
        out[0] = flags | Trunc[u8](v)
        return 1
    }
    out[0] = flags | Trunc[u8](max)
    var rest := v - max
    var i := 1
    for rest >= 128 {
        out[i] = Trunc[u8](rest & 0x7F) | 0x80
        rest = rest >> 7
        i += 1
    }
    out[i] = Trunc[u8](rest)
    return i + 1
}

// GetInt reads an integer with an n-bit prefix from src at at: its value
// and the offset after it. Fails with Truncated when src ends first and
// BadInteger past MaxIntBytes bytes or past the int range. O(1): at most
// MaxIntBytes bytes are read.
GetInt(src: []u8, at: int, n: int): Truncated|BadInteger!(value: int, next: int) {
    if at < 0 || at >= src.Len() {
        fail Truncated(offset: at)
    }
    max := (1 << n) - 1
    first := int(src[at]) & max
    if first < max {
        return (value: first, next: at + 1)
    }
    var v: u64 = u64(Trunc[u8](max))
    var shift := 0
    var i := at + 1
    for {
        if i - at >= MaxIntBytes {
            fail BadInteger(offset: at)
        }
        if i >= src.Len() {
            fail Truncated(offset: at)
        }
        c := src[i]
        i += 1
        // shift is at most 56 here and each group below 2^7, so the sum
        // stays below 2^64.
        v += u64(c & 0x7F) << shift
        if c & 0x80 == 0 {
            break
        }
        shift += 7
    }
    if v > MaxValue {
        fail BadInteger(offset: at)
    }
    return (value: Cast[int](v), next: i)
}

// StrHead reads the head of a string literal at at whose length has an
// n-bit prefix and whose Huffman flag is the bit above it: whether it is
// Huffman coded, and where its bytes start and end in src. Fails with
// Truncated when src ends before the string does and BadInteger for a bad
// length. O(1).
StrHead(src: []u8, at: int, n: int): Truncated|BadInteger!(huffman: bool, start: int, end: int) {
    if at < 0 || at >= src.Len() {
        fail Truncated(offset: at)
    }
    huffman := (int(src[at]) >> n) & 1 == 1
    (value, next) := try GetInt(src, at, n)
    if value > src.Len() - next {
        fail Truncated(offset: at)
    }
    return (huffman, start: next, end: next + value)
}

// How a string literal is written: its whole encoded length, whether it is
// Huffman coded, and the length of its data (the code or the raw bytes).
type StrCode = (len: int, huff: bool, data: int)

// StrCost is how s is written as a string literal with an n-bit length
// prefix: Huffman when huffman is on and its code is not longer than s (RFC
// 7541 5.2). O(s).
StrCost(s: []u8, n: int, huffman: bool): StrCode {
    if huffman {
        h := HuffLen(s)
        if h <= s.Len() {
            return (len: IntLen(h, n) + h, huff: true, data: h)
        }
    }
    return (len: IntLen(s.Len(), n) + s.Len(), huff: false, data: s.Len())
}

// PutStr writes s as a string literal with an n-bit length prefix into out
// from out[0], as c (StrCost of s with the same n) says, with flags (the
// bits above the Huffman flag) in its first byte, and returns c.len. out
// holds at least c.len bytes. O(s).
PutStr(out: mut []u8, s: []u8, n: int, flags: u8, c: StrCode): int {
    if !c.huff {
        k := PutInt(out, s.Len(), n, flags)
        (mut out[k..k + s.Len()]).CopyFrom(s)
        return k + s.Len()
    }
    k := PutInt(out, c.data, n, flags | Trunc[u8](1 << n))
    return k + HuffPut(mut out[k..k + c.data], s)
}
