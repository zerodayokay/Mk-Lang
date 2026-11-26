// Why: HTTP/3 encodes frame types, frame lengths, unidirectional stream types,
// setting identifiers and values, and GOAWAY/PUSH identifiers as QUIC
// variable-length integers without importing std.quic.
//
// What: MaxVarint, VarintLen, ReadVarint, WriteVarint, IsGrease, and GreaseId.
//
// How: RFC 9000 section 16 big-endian 1-, 2-, 4- and 8-byte encodings tagged
// by the top two bits of the first byte, with explicit bounds checks on every
// read and shortest-encoding emission on every write.

/// Maximum 62-bit unsigned integer a QUIC varint encodes (RFC 9000 16: 2^62 - 1).
export const MaxVarint: u64 = 4_611_686_018_427_387_903

/// Shortest encoded length in bytes (1, 2, 4 or 8) for v, or 0 if v exceeds
/// MaxVarint (RFC 9000 16). O(1); allocates nothing.
export VarintLen(v: u64): int {
    if v <= 0x3f {
        return 1
    }
    if v <= 0x3fff {
        return 2
    }
    if v <= 0x3fff_ffff {
        return 4
    }
    if v <= MaxVarint {
        return 8
    }
    return 0
}

/// Decodes a QUIC variable-length integer starting at b[at], returning the
/// 62-bit value and byte count consumed (1, 2, 4 or 8), or none when b is
/// incomplete (RFC 9000 16). Accepts non-minimal encodings. O(1); allocates
/// nothing.
export ReadVarint(b: []u8, at: int): ?(v: u64, n: int) {
    if at < 0 || at >= b.Len() {
        return none
    }
    first := b[at]
    n := 1 << int(first >> 6)
    if b.Len() - at < n {
        return none
    }
    var v: u64 = u64(first & 0x3f)
    for i in 1..n {
        v = (v << 8) | u64(b[at + i])
    }
    return some((v: v, n: n))
}

/// Writes v into out using the shortest QUIC varint encoding and returns the
/// byte count written (1, 2, 4 or 8), or 0 if v exceeds MaxVarint or out is
/// too short (RFC 9000 16). O(1); allocates nothing.
export WriteVarint(out: mut []u8, v: u64): int {
    n := VarintLen(v)
    if n == 0 || out.Len() < n {
        return 0
    }
    match n {
        1 {
            out[0] = Trunc[u8](v)
        }
        2 {
            out[0] = Trunc[u8]((v >> 8) | 0x40)
            out[1] = Trunc[u8](v)
        }
        4 {
            out[0] = Trunc[u8]((v >> 24) | 0x80)
            out[1] = Trunc[u8](v >> 16)
            out[2] = Trunc[u8](v >> 8)
            out[3] = Trunc[u8](v)
        }
        _ {
            out[0] = Trunc[u8]((v >> 56) | 0xc0)
            out[1] = Trunc[u8](v >> 48)
            out[2] = Trunc[u8](v >> 40)
            out[3] = Trunc[u8](v >> 32)
            out[4] = Trunc[u8](v >> 24)
            out[5] = Trunc[u8](v >> 16)
            out[6] = Trunc[u8](v >> 8)
            out[7] = Trunc[u8](v)
        }
    }
    return n
}

/// Reports whether v is a reserved GREASE identifier of the form 0x1f * N + 0x21
/// (RFC 9114 6.2.3, 7.2.4.1, 7.2.8, 8.1). O(1); allocates nothing.
export IsGrease(v: u64): bool {
    return v >= 0x21 && (v - 0x21) % 0x1f == 0
}

/// Returns the GREASE identifier 0x1f * n + 0x21 for n, or 0x21 if it would
/// exceed MaxVarint. O(1); allocates nothing.
export GreaseId(n: u64): u64 {
    if n > (MaxVarint - 0x21) / 0x1f {
        return 0x21
    }
    return 0x1f * n + 0x21
}
