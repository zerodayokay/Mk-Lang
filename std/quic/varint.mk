// Why: QUIC encodes frame fields, lengths, packet headers and transport
// parameters as variable-length integers and reports failures with the
// transport error codes of RFC 9000.
//
// What: VarintLen, ReadVarint, WriteVarint, IsMinimalVarint, MaxVarint,
// MaxStreamsLimit, the RFC 9000 20.1 transport error code constants, and
// std.quic's internal Error type.
//
// How: RFC 9000 section 16 big-endian 1-, 2-, 4- and 8-byte encodings
// tagged by the top two bits of the first byte. Every read checks bounds
// before indexing and every write emits the shortest encoding without
// allocating.

import std.crypto
import std.net

// MaxVarint is the largest 62-bit unsigned integer QUIC varints encode
// (RFC 9000 16: 2^62 - 1).
const MaxVarint: u64 = 4_611_686_018_427_387_903

// MaxStreamsLimit is the largest cumulative stream count allowed by
// MAX_STREAMS, STREAMS_BLOCKED and the initial_max_streams_* transport
// parameters (RFC 9000 4.6, 19.11, 19.14: 2^60).
const MaxStreamsLimit: u64 = 1_152_921_504_606_846_976

// RFC 9000 20.1 transport error codes.
const ErrNoError: u64 = 0x00
const ErrInternal: u64 = 0x01
const ErrConnectionRefused: u64 = 0x02
const ErrFlowControl: u64 = 0x03
const ErrStreamLimit: u64 = 0x04
const ErrStreamState: u64 = 0x05
const ErrFinalSize: u64 = 0x06
const ErrFrameEncoding: u64 = 0x07
const ErrTransportParameter: u64 = 0x08
const ErrConnectionIdLimit: u64 = 0x09
const ErrProtocolViolation: u64 = 0x0a
const ErrInvalidToken: u64 = 0x0b
const ErrApplication: u64 = 0x0c
const ErrCryptoBufferExceeded: u64 = 0x0d
const ErrKeyUpdate: u64 = 0x0e
const ErrAeadLimitReached: u64 = 0x0f
const ErrNoViablePath: u64 = 0x10

// CRYPTO_ERROR base code (RFC 9000 20.1, RFC 9001 4.8): 0x0100 + TLS alert.
const ErrCryptoBase: u64 = 0x0100

// TransportError is a QUIC connection error with its RFC 9000 20.1 code and
// the frame type that triggered it (0 when unknown or not frame-specific).
error TransportError(code: u64, frame: u64)

// Receiving a STREAM frame would create a 33rd reassembly gap; the caller must
// drop the entire packet unacknowledged (RFC 9000 13.1).
error GapLimit

// Error is the internal error set shared across std.quic's wire, protection,
// recovery, stream, and connection layers.
type Error = TransportError | crypto.Error | GapLimit | net.StreamReset | Closed | OutOfMemory

// VarintLen returns the shortest encoded length in bytes (1, 2, 4 or 8) for
// v, or 0 if v exceeds MaxVarint (RFC 9000 16). O(1); allocates nothing.
VarintLen(v: u64): int {
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

// ReadVarint decodes a QUIC variable-length integer starting at b[at],
// returning the 62-bit value and the byte length consumed (1, 2, 4 or 8),
// or none when b ends before the encoding completes (RFC 9000 16). Accepts
// non-minimal encodings. O(1); allocates nothing.
ReadVarint(b: []u8, at: int): ?(v: u64, n: int) {
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

// IsMinimalVarint reports whether an n-byte varint encoding of v used the
// shortest possible representation (RFC 9000 12.4, 16). O(1).
IsMinimalVarint(v: u64, n: int): bool {
    return n > 0 && VarintLen(v) == n
}

// WriteVarint writes v into out using the shortest QUIC varint encoding and
// returns the number of bytes written (1, 2, 4 or 8), or 0 if v exceeds
// MaxVarint or out is too short (RFC 9000 16). O(1); allocates nothing.
WriteVarint(out: mut []u8, v: u64): int {
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

// ReadBe16 reads two big-endian bytes at b[at..at + 2]. Caller ensures
// 0 <= at and b.Len() - at >= 2.
ReadBe16(b: []u8, at: int): u16 {
    return (u16(b[at]) << 8) | u16(b[at + 1])
}

// ReadBe32 reads four big-endian bytes at b[at..at + 4]. Caller ensures
// 0 <= at and b.Len() - at >= 4.
ReadBe32(b: []u8, at: int): u32 {
    return (u32(b[at]) << 24) | (u32(b[at + 1]) << 16) | (u32(b[at + 2]) << 8) | u32(b[at + 3])
}

// ReadLe32 reads four little-endian bytes at b[at..at + 4]. Caller ensures
// 0 <= at and b.Len() - at >= 4.
ReadLe32(b: []u8, at: int): u32 {
    return u32(b[at]) | (u32(b[at + 1]) << 8) | (u32(b[at + 2]) << 16) | (u32(b[at + 3]) << 24)
}

// PutBe16 writes x big-endian into out[at..at + 2]. Caller ensures bounds.
PutBe16(out: mut []u8, at: int, x: u16) {
    out[at] = Trunc[u8](x >> 8)
    out[at + 1] = Trunc[u8](x)
}

// PutBe32 writes x big-endian into out[at..at + 4]. Caller ensures bounds.
PutBe32(out: mut []u8, at: int, x: u32) {
    out[at] = Trunc[u8](x >> 24)
    out[at + 1] = Trunc[u8](x >> 16)
    out[at + 2] = Trunc[u8](x >> 8)
    out[at + 3] = Trunc[u8](x)
}
