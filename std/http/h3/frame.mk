// Why: std.http speaks HTTP/3 (RFC 9114) over any multiplexed transport a
// net.Mux gives, so its framing and control streams sit in a std-internal
// module that knows nothing of QUIC; frame headers and payloads come from
// untrusted peer bytes and must be bounded, total and zero-allocation.
//
// What: RFC 9114 6.2 unidirectional stream types, RFC 9114 7.2 frame types,
// IsReservedH2Frame, MaxControlFrameBytes, FrameHeader, ParseFrameHeader,
// WriteFrameHeader, ParseSingleIdPayload, WriteSingleIdFrame, and
// ParsePushPromisePrefix.
//
// How: ParseFrameHeader decodes the Type and Length QUIC varints without
// advancing past truncated headers; payload parsers verify that fields fill
// the declared Length exactly (RFC 9114 7.1, 10.8) and return Violation on
// mismatch.

/// RFC 9114 6.2.1: control unidirectional stream type.
export const StreamControl: u64 = 0x00

/// RFC 9114 6.2.2: server push unidirectional stream type.
export const StreamPush: u64 = 0x01

/// RFC 9204 4.2: QPACK encoder unidirectional stream type.
export const StreamQpackEncoder: u64 = 0x02

/// RFC 9204 4.2: QPACK decoder unidirectional stream type.
export const StreamQpackDecoder: u64 = 0x03

/// RFC 9114 7.2.1: DATA frame type.
export const FrameData: u64 = 0x00

/// RFC 9114 7.2.2: HEADERS frame type.
export const FrameHeaders: u64 = 0x01

/// RFC 9114 7.2.3: CANCEL_PUSH frame type.
export const FrameCancelPush: u64 = 0x03

/// RFC 9114 7.2.4: SETTINGS frame type.
export const FrameSettings: u64 = 0x04

/// RFC 9114 7.2.5: PUSH_PROMISE frame type.
export const FramePushPromise: u64 = 0x05

/// RFC 9114 7.2.6: GOAWAY frame type.
export const FrameGoaway: u64 = 0x07

/// RFC 9114 7.2.7: MAX_PUSH_ID frame type.
export const FrameMaxPushId: u64 = 0x0d

/// Maximum payload size accepted for a single control-stream frame (RFC 9114
/// 10.5 excessive-load guard).
export const MaxControlFrameBytes: int = 16384

/// Reports whether kind is one of the HTTP/2-only frame types reserved in
/// HTTP/3 (RFC 9114 7.2.8, Table 2: 0x02, 0x06, 0x08, 0x09). O(1); allocates
/// nothing.
export IsReservedH2Frame(kind: u64): bool {
    return kind == 0x02 || kind == 0x06 || kind == 0x08 || kind == 0x09
}

/// Decoded HTTP/3 frame header: Type, Length and total header byte size.
export struct FrameHeader {
    /// Frame type varint.
    export kind: u64
    /// Declared payload length in bytes.
    export length: u64
    /// Byte length of the Type + Length header (2..16).
    export header_len: int
}

/// Decodes one HTTP/3 frame header (Type + Length varints) from b[0..];
/// returns none when b ends before both varints complete (RFC 9114 7.1). O(1);
/// allocates nothing.
export ParseFrameHeader(b: []u8): ?FrameHeader {
    t := ReadVarint(b, 0) else {
        return none
    }
    l := ReadVarint(b, t.n) else {
        return none
    }
    return some(FrameHeader(kind: t.v, length: l.v, header_len: t.n + l.n))
}

/// Writes an HTTP/3 frame header (Type + Length varints) into out and returns
/// bytes written, or 0 if either integer exceeds MaxVarint or out is too
/// short. O(1); allocates nothing.
export WriteFrameHeader(out: mut []u8, kind: u64, length: u64): int {
    n1 := WriteVarint(out, kind)
    if n1 == 0 {
        return 0
    }
    n2 := WriteVarint(mut out[n1..], length)
    if n2 == 0 {
        return 0
    }
    return n1 + n2
}

/// Parses a single-varint frame payload (CANCEL_PUSH, GOAWAY or MAX_PUSH_ID),
/// failing with Violation(H3FrameError, -1) if p is empty, truncated or has
/// trailing bytes (RFC 9114 7.1, 10.8). O(1); allocates nothing.
export ParseSingleIdPayload(p: []u8): Violation!u64 {
    r := ReadVarint(p, 0) else {
        fail Violation(code: H3FrameError, stream: -1)
    }
    if r.n != p.Len() {
        fail Violation(code: H3FrameError, stream: -1)
    }
    return r.v
}

/// Writes a single-varint frame (CANCEL_PUSH, GOAWAY or MAX_PUSH_ID) into out
/// and returns bytes written, or 0 if id exceeds MaxVarint or out is too
/// short. O(1); allocates nothing.
export WriteSingleIdFrame(out: mut []u8, kind: u64, id: u64): int {
    plen := VarintLen(id)
    if plen == 0 {
        return 0
    }
    hlen := WriteFrameHeader(out, kind, Cast[u64](plen))
    if hlen == 0 || out.Len() - hlen < plen {
        return 0
    }
    _ = WriteVarint(mut out[hlen..], id)
    return hlen + plen
}

/// Parses the Push ID varint prefix of a PUSH_PROMISE payload (RFC 9114
/// 7.2.5), returning the push ID and offset of the encoded field section.
/// Errors: Violation(H3FrameError, -1) when p ends before Push ID completes.
/// O(1); allocates nothing.
export ParsePushPromisePrefix(p: []u8): Violation!(push_id: u64, header_off: int) {
    r := ReadVarint(p, 0) else {
        fail Violation(code: H3FrameError, stream: -1)
    }
    return (push_id: r.v, header_off: r.n)
}
