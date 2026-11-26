// Why: the HTTP/2 writer formats frames directly into a fixed connection write
// buffer without per-frame heap allocation, keeping unused flags and reserved
// bits cleared to 0 as RFC 9113 requires.
//
// What: WriteData, WriteHeadersHead, WriteContinuationHead, WriteRstStream,
// WriteSettings, WriteSettingsAck, WritePing, WriteGoaway and WriteWindowUpdate.
//
// How: each writer checks out.Len() against the exact frame length before
// writing anything, failing with fields.TooSmall(need) without mutating out
// when out is too short.

import std.http.fields

// PutFrameHead writes the 9-octet RFC 9113 4.1 frame header into out[0..9],
// masking the reserved bit of stream to 0.
PutFrameHead(out: mut []u8, length: int, kind: u8, flags: u8, stream: u32) {
    out[0] = Trunc[u8](length >> 16)
    out[1] = Trunc[u8](length >> 8)
    out[2] = Trunc[u8](length)
    out[3] = kind
    out[4] = flags
    s := stream & StreamMask
    out[5] = Trunc[u8](s >> 24)
    out[6] = Trunc[u8](s >> 16)
    out[7] = Trunc[u8](s >> 8)
    out[8] = Trunc[u8](s)
}

// PutBeU32 writes v big-endian into out[0..4].
PutBeU32(out: mut []u8, v: u32) {
    out[0] = Trunc[u8](v >> 24)
    out[1] = Trunc[u8](v >> 16)
    out[2] = Trunc[u8](v >> 8)
    out[3] = Trunc[u8](v)
}

// PutSetting writes one 6-byte (id, val) SETTINGS entry into out[0..6].
PutSetting(out: mut []u8, id: u16, val: u32) {
    out[0] = Trunc[u8](id >> 8)
    out[1] = Trunc[u8](id)
    PutBeU32(mut out[2..6], val)
}

/// Writes a DATA frame (no padding, RFC 9113 6.1) into out from out[0] and
/// returns the bytes written. Errors: fields.TooSmall(need) when out is
/// shorter than 9 + data.Len(), leaving out unchanged. O(data); allocates
/// nothing.
export WriteData(out: mut []u8, stream: u32, data: []u8, end: bool): fields.TooSmall!int {
    need := 9 + data.Len()
    if out.Len() < need {
        fail fields.TooSmall(need: need)
    }
    flags := if end { FlagEndStream } else { u8(0) }
    PutFrameHead(out, data.Len(), TypeData, flags, stream)
    (mut out[9..need]).CopyFrom(data)
    return need
}

/// Writes a 9-octet HEADERS frame header (no padding or priority, RFC 9113
/// 6.2) for a payload of length bytes into out from out[0] and returns 9.
/// Errors: fields.TooSmall(9) when out is shorter than 9 bytes. O(1); allocates
/// nothing.
export WriteHeadersHead(out: mut []u8, stream: u32, length: int, end_headers: bool, end_stream: bool): fields.TooSmall!int {
    if out.Len() < 9 {
        fail fields.TooSmall(need: 9)
    }
    var flags: u8 = 0
    if end_headers {
        flags = flags | FlagEndHeaders
    }
    if end_stream {
        flags = flags | FlagEndStream
    }
    PutFrameHead(out, length, TypeHeaders, flags, stream)
    return 9
}

/// Writes a 9-octet CONTINUATION frame header (RFC 9113 6.10) for a payload of
/// length bytes into out from out[0] and returns 9. Errors: fields.TooSmall(9)
/// when out is shorter than 9 bytes. O(1); allocates nothing.
export WriteContinuationHead(out: mut []u8, stream: u32, length: int, end_headers: bool): fields.TooSmall!int {
    if out.Len() < 9 {
        fail fields.TooSmall(need: 9)
    }
    flags := if end_headers { FlagEndHeaders } else { u8(0) }
    PutFrameHead(out, length, TypeContinuation, flags, stream)
    return 9
}

/// Writes a 13-octet RST_STREAM frame (RFC 9113 6.4) into out from out[0] and
/// returns 13. Errors: fields.TooSmall(13) when out is shorter than 13 bytes.
/// O(1); allocates nothing.
export WriteRstStream(out: mut []u8, stream: u32, code: u32): fields.TooSmall!int {
    if out.Len() < 13 {
        fail fields.TooSmall(need: 13)
    }
    PutFrameHead(out, 4, TypeRstStream, 0, stream)
    PutBeU32(mut out[9..13], code)
    return 13
}

/// Writes a SETTINGS frame (RFC 9113 6.5) encoding each setting of s that
/// differs from InitialSettings (plus any present optional limits and
/// ENABLE_PUSH=0 when disabled) into out from out[0] and returns the bytes
/// written. Errors: fields.TooSmall(need) when out is shorter than need,
/// leaving out unchanged. O(1); allocates nothing.
export WriteSettings(out: mut []u8, s: &Settings): fields.TooSmall!int {
    var count := 0
    if s.table_bytes != InitialSettings.table_bytes {
        count += 1
    }
    if !s.enable_push {
        count += 1
    }
    if s.max_streams.IsSome() {
        count += 1
    }
    if s.initial_window != InitialSettings.initial_window {
        count += 1
    }
    if s.max_frame != InitialSettings.max_frame {
        count += 1
    }
    if s.max_header_list.IsSome() {
        count += 1
    }
    plen := count * 6
    need := 9 + plen
    if out.Len() < need {
        fail fields.TooSmall(need: need)
    }
    PutFrameHead(out, plen, TypeSettings, 0, 0)
    var o := 9
    if s.table_bytes != InitialSettings.table_bytes {
        PutSetting(mut out[o..o + 6], SettingHeaderTableSize, Trunc[u32](s.table_bytes))
        o += 6
    }
    if !s.enable_push {
        PutSetting(mut out[o..o + 6], SettingEnablePush, 0)
        o += 6
    }
    if v := s.max_streams {
        PutSetting(mut out[o..o + 6], SettingMaxConcurrentStreams, Trunc[u32](v))
        o += 6
    }
    if s.initial_window != InitialSettings.initial_window {
        PutSetting(mut out[o..o + 6], SettingInitialWindowSize, Trunc[u32](s.initial_window))
        o += 6
    }
    if s.max_frame != InitialSettings.max_frame {
        PutSetting(mut out[o..o + 6], SettingMaxFrameSize, Trunc[u32](s.max_frame))
        o += 6
    }
    if v := s.max_header_list {
        PutSetting(mut out[o..o + 6], SettingMaxHeaderListSize, Trunc[u32](v))
        o += 6
    }
    return need
}

/// Writes a 9-octet SETTINGS ACK frame (RFC 9113 6.5) into out from out[0] and
/// returns 9. Errors: fields.TooSmall(9) when out is shorter than 9 bytes. O(1);
/// allocates nothing.
export WriteSettingsAck(out: mut []u8): fields.TooSmall!int {
    if out.Len() < 9 {
        fail fields.TooSmall(need: 9)
    }
    PutFrameHead(out, 0, TypeSettings, FlagAck, 0)
    return 9
}

/// Writes a 17-octet PING frame (RFC 9113 6.7) into out from out[0] and
/// returns 17. Errors: fields.TooSmall(17) when out is shorter than 17 bytes.
/// O(1); allocates nothing.
export WritePing(out: mut []u8, data: &[8]u8, ack: bool): fields.TooSmall!int {
    if out.Len() < 17 {
        fail fields.TooSmall(need: 17)
    }
    flags := if ack { FlagAck } else { u8(0) }
    PutFrameHead(out, 8, TypePing, flags, 0)
    (mut out[9..17]).CopyFrom(data[..])
    return 17
}

/// Writes a 17-octet GOAWAY frame (RFC 9113 6.8) with no debug data into out
/// from out[0] and returns 17. Errors: fields.TooSmall(17) when out is
/// shorter than 17 bytes. O(1); allocates nothing.
export WriteGoaway(out: mut []u8, last: u32, code: u32): fields.TooSmall!int {
    if out.Len() < 17 {
        fail fields.TooSmall(need: 17)
    }
    PutFrameHead(out, 8, TypeGoaway, 0, 0)
    PutBeU32(mut out[9..13], last & StreamMask)
    PutBeU32(mut out[13..17], code)
    return 17
}

/// Writes a 13-octet WINDOW_UPDATE frame (RFC 9113 6.9) into out from out[0]
/// and returns 13. Errors: fields.TooSmall(13) when out is shorter than 13
/// bytes. O(1); allocates nothing.
export WriteWindowUpdate(out: mut []u8, stream: u32, increment: u32): fields.TooSmall!int {
    if out.Len() < 13 {
        fail fields.TooSmall(need: 13)
    }
    PutFrameHead(out, 4, TypeWindowUpdate, 0, stream)
    PutBeU32(mut out[9..13], increment & StreamMask)
    return 13
}
