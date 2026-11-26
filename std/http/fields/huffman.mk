// Why: most field strings an HTTP/2 or HTTP/3 peer sends are Huffman coded
// (RFC 7541 5.2, Appendix B; RFC 9204 4.1.2 uses the same code), so
// decoding sits on every received header, and its input is the peer's.
//
// What: HuffLen, the coded length of a string; HuffPut, the encoder;
// HuffMaxOut, the bound of a decoded length; HuffDecode, the decoder.
//
// How: the encoder shifts each symbol's code into a 64-bit accumulator and
// stores 32 bits at a time, then pads the last byte with ones (the start
// of EOS). The decoder walks huffman_table.mk's state machine four bits at
// a time: two table loads per input byte, whatever the code lengths. A
// step that completes EOS fails, and the string must end in a state whose
// path since the last symbol is at most 7 ones, so longer padding and
// padding with a zero fail too.

// HuffLen is the length in bytes of s Huffman coded, padding included.
// O(s); allocates nothing.
HuffLen(s: []u8): int {
    var bits := 0
    for c in s {
        bits += int(HuffBits[c])
    }
    return (bits + 7) / 8
}

// HuffPut writes s Huffman coded into out from out[0] and returns its
// length, HuffLen(s); out holds at least that. O(s); allocates nothing.
HuffPut(out: mut []u8, s: []u8): int {
    // acc holds the pending bits in its low n bits; n stays below 32
    // between symbols, and a code is at most 30 bits, so acc never needs
    // more than 62.
    var acc: u64 = 0
    var n := 0
    var o := 0
    for c in s {
        len := int(HuffBits[c])
        acc = (acc << len) | u64(HuffCode[c])
        n += len
        if n >= 32 {
            n -= 32
            w := acc >> n
            out[o] = Trunc[u8](w >> 24)
            out[o + 1] = Trunc[u8](w >> 16)
            out[o + 2] = Trunc[u8](w >> 8)
            out[o + 3] = Trunc[u8](w)
            o += 4
            acc = acc & ((1 << n) - 1)
        }
    }
    for n >= 8 {
        n -= 8
        out[o] = Trunc[u8](acc >> n)
        o += 1
    }
    if n > 0 {
        // The padding: the high bits of EOS, which are ones.
        pad := 8 - n
        out[o] = Trunc[u8]((acc << pad) | ((1 << pad) - 1))
        o += 1
    }
    return o
}

// HuffMaxOut is the most bytes n Huffman-coded bytes decode to: the
// shortest code is 5 bits. O(1).
HuffMaxOut(n: int): int {
    return n / 5 * 8 + (n % 5) * 8 / 5
}

// HuffDecode decodes the Huffman-coded src into out from out[0] and
// returns the length decoded, or none when the string is malformed (it
// holds EOS, or its padding is longer than 7 bits or not all ones) or when
// out is too short for it; Huffman tells the two apart. O(src); allocates
// nothing.
HuffDecode(out: mut []u8, src: []u8): ?int {
    var state: u32 = 0
    var ok := true
    var o := 0
    cap := out.Len()
    for c in src {
        hi := HuffDec[int((state << 4) | u32(c >> 4))]
        if hi & HuffFail != 0 {
            return none
        }
        if hi & HuffEmit != 0 {
            if o == cap {
                return none
            }
            out[o] = Trunc[u8](hi >> 8)
            o += 1
        }
        lo := HuffDec[int(((hi & 0xFF) << 4) | u32(c & 0x0F))]
        if lo & HuffFail != 0 {
            return none
        }
        if lo & HuffEmit != 0 {
            if o == cap {
                return none
            }
            out[o] = Trunc[u8](lo >> 8)
            o += 1
        }
        state = lo & 0xFF
        ok = lo & HuffAccept != 0
    }
    if !ok {
        return none
    }
    return some(o)
}

// HuffValid is whether src is a well-formed Huffman string, and its
// decoded length when it is: HuffDecode without the output. O(src);
// allocates nothing.
HuffValid(src: []u8): ?int {
    var state: u32 = 0
    var ok := true
    var o := 0
    for c in src {
        hi := HuffDec[int((state << 4) | u32(c >> 4))]
        lo := HuffDec[int(((hi & 0xFF) << 4) | u32(c & 0x0F))]
        if (hi | lo) & HuffFail != 0 {
            return none
        }
        o += int((hi >> 16) & 1) + int((lo >> 16) & 1)
        state = lo & 0xFF
        ok = lo & HuffAccept != 0
    }
    if !ok {
        return none
    }
    return some(o)
}
