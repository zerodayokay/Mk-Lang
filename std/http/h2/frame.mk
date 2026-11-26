// Why: std.http speaks HTTP/2 (RFC 9113) through an engine of its own, in a
// std-internal module, so the engine's frames, streams and windows are never
// part of a program's API; the frame parser reads peer bytes and must be
// bounded, total and zero-allocation.
//
// What: RFC 9113 6 frame type constants, the 24-byte Preface, Header,
// ParseHeader, Frame, and Parse.
//
// How: ParseHeader decodes the 9-byte header and masks the reserved bit; Parse
// checks every per-frame RFC 9113 rule that needs no connection state (size,
// stream-0 legality, padding bounds, unknown error codes) and returns views of
// the caller's read buffer.

// RFC 9113 6 frame types.
/// RFC 9113 6.1: DATA frame type.
export const TypeData: u8 = 0x0
/// RFC 9113 6.2: HEADERS frame type.
export const TypeHeaders: u8 = 0x1
/// RFC 9113 6.3: PRIORITY frame type.
export const TypePriority: u8 = 0x2
/// RFC 9113 6.4: RST_STREAM frame type.
export const TypeRstStream: u8 = 0x3
/// RFC 9113 6.5: SETTINGS frame type.
export const TypeSettings: u8 = 0x4
/// RFC 9113 6.6: PUSH_PROMISE frame type.
export const TypePushPromise: u8 = 0x5
/// RFC 9113 6.7: PING frame type.
export const TypePing: u8 = 0x6
/// RFC 9113 6.8: GOAWAY frame type.
export const TypeGoaway: u8 = 0x7
/// RFC 9113 6.9: WINDOW_UPDATE frame type.
export const TypeWindowUpdate: u8 = 0x8
/// RFC 9113 6.10: CONTINUATION frame type.
export const TypeContinuation: u8 = 0x9

// RFC 9113 6 frame flags.
const FlagEndStream: u8 = 0x01
const FlagAck: u8 = 0x01
const FlagEndHeaders: u8 = 0x04
const FlagPadded: u8 = 0x08
const FlagPriority: u8 = 0x20

// Mask for 31-bit stream identifiers (RFC 9113 4.1: ignore reserved bit).
const StreamMask: u32 = 0x7FFF_FFFF

/// The 24-byte HTTP/2 client connection preface: "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"
/// (RFC 9113 3.4).
export const Preface: [24]u8 = [
    0x50, 0x52, 0x49, 0x20, 0x2a, 0x20, 0x48, 0x54,
    0x54, 0x50, 0x2f, 0x32, 0x2e, 0x30, 0x0d, 0x0a,
    0x0d, 0x0a, 0x53, 0x4d, 0x0d, 0x0a, 0x0d, 0x0a,
]

/// A parsed 9-octet HTTP/2 frame header (RFC 9113 4.1).
export struct Header {
    /// Payload length in octets (24-bit unsigned).
    export length: int
    /// Frame type byte.
    export kind: u8
    /// Frame flags byte (unused bits preserved by ParseHeader).
    export flags: u8
    /// 31-bit stream identifier (reserved bit masked to 0).
    export stream: u32
}

/// Decodes the 9-octet frame header at b[0..9], masking the reserved bit to 0
/// (RFC 9113 4.1); returns none when b has fewer than 9 bytes. O(1); allocates
/// nothing.
export ParseHeader(b: []u8): ?Header {
    if b.Len() < 9 {
        return none
    }
    length := (int(b[0]) << 16) | (int(b[1]) << 8) | int(b[2])
    stream := ((u32(b[5]) & 0x7F) << 24) | (u32(b[6]) << 16) | (u32(b[7]) << 8) | u32(b[8])
    return some(Header(length: length, kind: b[3], flags: b[4], stream: stream))
}

/// One parsed HTTP/2 frame whose slice payloads are views of the caller's read
/// buffer. Padding is stripped; padded reports total padding bytes removed
/// (Pad Length byte plus padding octets) for flow-control accounting.
export enum Frame {
    Data(stream: u32, data: []u8, padded: int, end: bool)
    Headers(stream: u32, block: []u8, padded: int, end_headers: bool, end_stream: bool)
    Priority(stream: u32)
    RstStream(stream: u32, code: u32)
    Settings(ack: bool, entries: []u8)
    PushPromise(stream: u32, promised: u32, block: []u8, end_headers: bool)
    Ping(ack: bool, data: [8]u8)
    Goaway(last: u32, code: u32, debug: []u8)
    WindowUpdate(stream: u32, increment: u32)
    Continuation(stream: u32, block: []u8, end_headers: bool)
    Unknown(kind: u8, stream: u32)
}

// BeU32 decodes a big-endian u32 from b[0..4].
BeU32(b: []u8): u32 {
    return (u32(b[0]) << 24) | (u32(b[1]) << 16) | (u32(b[2]) << 8) | u32(b[3])
}

/// Parses one frame from b (9-byte header plus h.length payload bytes) and
/// checks all per-frame rules of RFC 9113 4 and 6 that require no connection
/// state: max_frame bound, stream-0 legality, mandatory field sizes, padding
/// bounds, reserved bit masking, unused flag ignoring, and mapping unknown
/// error codes to InternalError. Errors: Violation(FrameSizeError, 0) when
/// h.length exceeds max_frame, b length differs from 9 + h.length, or a
/// connection-scoped mandatory field size fails; Violation(FrameSizeError, s)
/// for PRIORITY length != 5; Violation(ProtocolError, 0) for stream-0 misuse
/// or invalid padding. O(1) (copies 8 bytes for PING); allocates nothing.
export Parse(h: &Header, b: []u8, max_frame: int): Violation!Frame from b {
    if h.length < 0 || h.length > max_frame || b.Len() != 9 + h.length {
        fail Violation(code: FrameSizeError, stream: 0)
    }
    p := b[9..]
    s := h.stream
    f := h.flags
    if h.kind == TypeData {
        if s == 0 {
            fail Violation(code: ProtocolError, stream: 0)
        }
        end := (f & FlagEndStream) != 0
        if (f & FlagPadded) != 0 {
            if p.Len() < 1 {
                fail Violation(code: FrameSizeError, stream: 0)
            }
            pad := int(p[0])
            if pad >= p.Len() {
                fail Violation(code: ProtocolError, stream: 0)
            }
            return Frame.Data(stream: s, data: p[1..p.Len() - pad], padded: 1 + pad, end: end)
        }
        return Frame.Data(stream: s, data: p, padded: 0, end: end)
    }
    if h.kind == TypeHeaders {
        if s == 0 {
            fail Violation(code: ProtocolError, stream: 0)
        }
        end_stream := (f & FlagEndStream) != 0
        end_headers := (f & FlagEndHeaders) != 0
        padded_flag := (f & FlagPadded) != 0
        prio_flag := (f & FlagPriority) != 0
        var off := 0
        var pad := 0
        var padded := 0
        if padded_flag {
            if p.Len() < 1 {
                fail Violation(code: FrameSizeError, stream: 0)
            }
            pad = int(p[0])
            off = 1
            padded = 1 + pad
        }
        if prio_flag {
            if p.Len() - off < 5 {
                fail Violation(code: FrameSizeError, stream: 0)
            }
            off += 5
        }
        if padded_flag && pad > p.Len() - off {
            fail Violation(code: ProtocolError, stream: 0)
        }
        return Frame.Headers(stream: s, block: p[off..p.Len() - pad], padded: padded, end_headers: end_headers, end_stream: end_stream)
    }
    if h.kind == TypePriority {
        if s == 0 {
            fail Violation(code: ProtocolError, stream: 0)
        }
        if p.Len() != 5 {
            fail Violation(code: FrameSizeError, stream: s)
        }
        return Frame.Priority(stream: s)
    }
    if h.kind == TypeRstStream {
        if s == 0 {
            fail Violation(code: ProtocolError, stream: 0)
        }
        if p.Len() != 4 {
            fail Violation(code: FrameSizeError, stream: 0)
        }
        return Frame.RstStream(stream: s, code: NormError(BeU32(p)))
    }
    if h.kind == TypeSettings {
        if s != 0 {
            fail Violation(code: ProtocolError, stream: 0)
        }
        ack := (f & FlagAck) != 0
        if ack && p.Len() != 0 {
            fail Violation(code: FrameSizeError, stream: 0)
        }
        if !ack && p.Len() % 6 != 0 {
            fail Violation(code: FrameSizeError, stream: 0)
        }
        return Frame.Settings(ack: ack, entries: p)
    }
    if h.kind == TypePushPromise {
        if s == 0 {
            fail Violation(code: ProtocolError, stream: 0)
        }
        end_headers := (f & FlagEndHeaders) != 0
        padded_flag := (f & FlagPadded) != 0
        var off := 0
        var pad := 0
        if padded_flag {
            if p.Len() < 1 {
                fail Violation(code: FrameSizeError, stream: 0)
            }
            pad = int(p[0])
            off = 1
        }
        if p.Len() - off < 4 {
            fail Violation(code: FrameSizeError, stream: 0)
        }
        promised := BeU32(p[off..off + 4]) & StreamMask
        off += 4
        if padded_flag && pad > p.Len() - off {
            fail Violation(code: ProtocolError, stream: 0)
        }
        return Frame.PushPromise(stream: s, promised: promised, block: p[off..p.Len() - pad], end_headers: end_headers)
    }
    if h.kind == TypePing {
        if s != 0 {
            fail Violation(code: ProtocolError, stream: 0)
        }
        if p.Len() != 8 {
            fail Violation(code: FrameSizeError, stream: 0)
        }
        var data: [8]u8 = [0; 8]
        (mut data[..]).CopyFrom(p)
        return Frame.Ping(ack: (f & FlagAck) != 0, data: data)
    }
    if h.kind == TypeGoaway {
        if s != 0 {
            fail Violation(code: ProtocolError, stream: 0)
        }
        if p.Len() < 8 {
            fail Violation(code: FrameSizeError, stream: 0)
        }
        last := BeU32(p[0..4]) & StreamMask
        code := NormError(BeU32(p[4..8]))
        return Frame.Goaway(last: last, code: code, debug: p[8..])
    }
    if h.kind == TypeWindowUpdate {
        if p.Len() != 4 {
            fail Violation(code: FrameSizeError, stream: 0)
        }
        inc := BeU32(p) & StreamMask
        return Frame.WindowUpdate(stream: s, increment: inc)
    }
    if h.kind == TypeContinuation {
        if s == 0 {
            fail Violation(code: ProtocolError, stream: 0)
        }
        return Frame.Continuation(stream: s, block: p, end_headers: (f & FlagEndHeaders) != 0)
    }
    return Frame.Unknown(kind: h.kind, stream: s)
}
