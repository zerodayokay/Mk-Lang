// Why: packet assembly must encode every QUIC frame cleanly into a caller's
// fixed payload buffer without allocation and leave the buffer untouched when
// space runs out.
//
// What: one writer per QUIC frame type (WritePadding, WritePing, WriteAck,
// WriteResetStream, WriteStopSending, WriteCrypto, WriteNewToken,
// WriteStream, WriteMaxData, WriteMaxStreamData, WriteMaxStreams,
// WriteDataBlocked, WriteStreamDataBlocked, WriteStreamsBlocked,
// WriteNewConnectionId, WriteRetireConnectionId, WritePathChallenge,
// WritePathResponse, WriteConnectionClose, WriteHandshakeDone) plus
// WriteFrame.
//
// How: RFC 9000 section 19. Every writer computes its exact encoded length
// upfront, emits the shortest one-byte frame type (12.4), and returns none
// without writing when out is too short or an argument exceeds varint bounds.

// WritePadding writes n PADDING (0x00) frames into out, returning n or none
// when n < 0 or out is too short (RFC 9000 19.1). O(n); allocates nothing.
WritePadding(out: mut []u8, n: int): ?int {
    if n < 0 || out.Len() < n {
        return none
    }
    for i in 0..n {
        out[i] = 0x00
    }
    return some(n)
}

// WritePing writes a PING (0x01) frame into out (RFC 9000 19.2). O(1).
WritePing(out: mut []u8): ?int {
    if out.Len() < 1 {
        return none
    }
    out[0] = Trunc[u8](FramePing)
    return some(1)
}

// WriteAck writes an ACK (0x02) frame for descending ranges and scaled delay
// into out (RFC 9000 19.3). Returns none if ranges is empty, malformed or out
// is too short. O(ranges.Len()); allocates nothing.
WriteAck(out: mut []u8, ranges: []AckRange, delay: u64): ?int {
    if ranges.Len() == 0 {
        return none
    }
    r0 := ranges[0]
    if r0.lo < 0 || r0.lo > r0.hi || Trunc[u64](r0.hi) > MaxVarint {
        return none
    }
    largest := Trunc[u64](r0.hi)
    first_range := Trunc[u64](r0.hi - r0.lo)
    count := Trunc[u64](ranges.Len() - 1)
    dl := VarintLen(delay)
    if dl == 0 {
        return none
    }
    var need := 1 + VarintLen(largest) + dl + VarintLen(count) + VarintLen(first_range)
    for i in 1..ranges.Len() {
        prev := ranges[i - 1]
        cur := ranges[i]
        if cur.lo < 0 || cur.lo > cur.hi || prev.lo < cur.hi + 2 {
            return none
        }
        gap := Trunc[u64](prev.lo - cur.hi - 2)
        ack_len := Trunc[u64](cur.hi - cur.lo)
        need += VarintLen(gap) + VarintLen(ack_len)
    }
    if out.Len() < need {
        return none
    }
    out[0] = Trunc[u8](FrameAck)
    var pos := 1
    pos += WriteVarint(mut out[pos..], largest)
    pos += WriteVarint(mut out[pos..], delay)
    pos += WriteVarint(mut out[pos..], count)
    pos += WriteVarint(mut out[pos..], first_range)
    for i in 1..ranges.Len() {
        prev := ranges[i - 1]
        cur := ranges[i]
        pos += WriteVarint(mut out[pos..], Trunc[u64](prev.lo - cur.hi - 2))
        pos += WriteVarint(mut out[pos..], Trunc[u64](cur.hi - cur.lo))
    }
    return some(pos)
}

// WriteResetStream writes a RESET_STREAM (0x04) frame into out (RFC 9000 19.4).
// O(1); allocates nothing.
WriteResetStream(out: mut []u8, id: i64, code: u64, final_size: u64): ?int {
    if id < 0 {
        return none
    }
    il := VarintLen(Trunc[u64](id))
    cl := VarintLen(code)
    fl := VarintLen(final_size)
    if il == 0 || cl == 0 || fl == 0 {
        return none
    }
    need := 1 + il + cl + fl
    if out.Len() < need {
        return none
    }
    out[0] = Trunc[u8](FrameResetStream)
    var pos := 1
    pos += WriteVarint(mut out[pos..], Trunc[u64](id))
    pos += WriteVarint(mut out[pos..], code)
    pos += WriteVarint(mut out[pos..], final_size)
    return some(pos)
}

// WriteStopSending writes a STOP_SENDING (0x05) frame into out (RFC 9000 19.5).
// O(1); allocates nothing.
WriteStopSending(out: mut []u8, id: i64, code: u64): ?int {
    if id < 0 {
        return none
    }
    il := VarintLen(Trunc[u64](id))
    cl := VarintLen(code)
    if il == 0 || cl == 0 {
        return none
    }
    need := 1 + il + cl
    if out.Len() < need {
        return none
    }
    out[0] = Trunc[u8](FrameStopSending)
    var pos := 1
    pos += WriteVarint(mut out[pos..], Trunc[u64](id))
    pos += WriteVarint(mut out[pos..], code)
    return some(pos)
}

// WriteCrypto writes a CRYPTO (0x06) frame into out (RFC 9000 19.6).
// O(data.Len()); allocates nothing.
WriteCrypto(out: mut []u8, offset: u64, data: []u8): ?int {
    ol := VarintLen(offset)
    ll := VarintLen(Trunc[u64](data.Len()))
    if ol == 0 || ll == 0 || Trunc[u64](data.Len()) > MaxVarint - offset {
        return none
    }
    need := 1 + ol + ll + data.Len()
    if out.Len() < need {
        return none
    }
    out[0] = Trunc[u8](FrameCrypto)
    var pos := 1
    pos += WriteVarint(mut out[pos..], offset)
    pos += WriteVarint(mut out[pos..], Trunc[u64](data.Len()))
    (mut out[pos..pos + data.Len()]).CopyFrom(data)
    return some(need)
}

// WriteNewToken writes a NEW_TOKEN (0x07) frame into out (RFC 9000 19.7).
// Empty tokens are refused. O(token.Len()); allocates nothing.
WriteNewToken(out: mut []u8, token: []u8): ?int {
    if token.Len() == 0 {
        return none
    }
    tl := VarintLen(Trunc[u64](token.Len()))
    if tl == 0 {
        return none
    }
    need := 1 + tl + token.Len()
    if out.Len() < need {
        return none
    }
    out[0] = Trunc[u8](FrameNewToken)
    var pos := 1
    pos += WriteVarint(mut out[pos..], Trunc[u64](token.Len()))
    (mut out[pos..pos + token.Len()]).CopyFrom(token)
    return some(need)
}

// WriteStream writes a STREAM (0x08..0x0f) frame into out (RFC 9000 19.8).
// Omits Offset when offset == 0 and Length when with_len is false (valid only
// for the last frame in a packet). O(data.Len()); allocates nothing.
WriteStream(out: mut []u8, id: i64, offset: u64, data: []u8, fin: bool, with_len: bool): ?int {
    if id < 0 || Trunc[u64](data.Len()) > MaxVarint - offset {
        return none
    }
    il := VarintLen(Trunc[u64](id))
    if il == 0 {
        return none
    }
    var ty: u8 = 0x08
    var ol := 0
    if offset > 0 {
        ty = ty | 0x04
        ol = VarintLen(offset)
        if ol == 0 {
            return none
        }
    }
    var ll := 0
    if with_len {
        ty = ty | 0x02
        ll = VarintLen(Trunc[u64](data.Len()))
        if ll == 0 {
            return none
        }
    }
    if fin {
        ty = ty | 0x01
    }
    need := 1 + il + ol + ll + data.Len()
    if out.Len() < need {
        return none
    }
    out[0] = ty
    var pos := 1
    pos += WriteVarint(mut out[pos..], Trunc[u64](id))
    if offset > 0 {
        pos += WriteVarint(mut out[pos..], offset)
    }
    if with_len {
        pos += WriteVarint(mut out[pos..], Trunc[u64](data.Len()))
    }
    (mut out[pos..pos + data.Len()]).CopyFrom(data)
    return some(need)
}

// WriteMaxData writes a MAX_DATA (0x10) frame into out (RFC 9000 19.9). O(1).
WriteMaxData(out: mut []u8, max: u64): ?int {
    return WriteOneVarFrame(out, FrameMaxData, max)
}

// WriteMaxStreamData writes a MAX_STREAM_DATA (0x11) frame into out
// (RFC 9000 19.10). O(1).
WriteMaxStreamData(out: mut []u8, id: i64, max: u64): ?int {
    return WriteTwoVarFrame(out, FrameMaxStreamData, id, max)
}

// WriteMaxStreams writes a MAX_STREAMS (0x12 bidi / 0x13 uni) frame into out
// (RFC 9000 19.11). Returns none if max exceeds 2^60. O(1).
WriteMaxStreams(out: mut []u8, bidi: bool, max: u64): ?int {
    if max > MaxStreamsLimit {
        return none
    }
    ty := if bidi { FrameMaxStreamsBidi } else { FrameMaxStreamsUni }
    return WriteOneVarFrame(out, ty, max)
}

// WriteDataBlocked writes a DATA_BLOCKED (0x14) frame into out (RFC 9000 19.12). O(1).
WriteDataBlocked(out: mut []u8, max: u64): ?int {
    return WriteOneVarFrame(out, FrameDataBlocked, max)
}

// WriteStreamDataBlocked writes a STREAM_DATA_BLOCKED (0x15) frame into out
// (RFC 9000 19.13). O(1).
WriteStreamDataBlocked(out: mut []u8, id: i64, max: u64): ?int {
    return WriteTwoVarFrame(out, FrameStreamDataBlocked, id, max)
}

// WriteStreamsBlocked writes a STREAMS_BLOCKED (0x16 bidi / 0x17 uni) frame
// into out (RFC 9000 19.14). Returns none if max exceeds 2^60. O(1).
WriteStreamsBlocked(out: mut []u8, bidi: bool, max: u64): ?int {
    if max > MaxStreamsLimit {
        return none
    }
    ty := if bidi { FrameStreamsBlockedBidi } else { FrameStreamsBlockedUni }
    return WriteOneVarFrame(out, ty, max)
}

// WriteNewConnectionId writes a NEW_CONNECTION_ID (0x18) frame into out
// (RFC 9000 19.15). O(1); allocates nothing.
WriteNewConnectionId(out: mut []u8, seq: u64, retire_prior: u64, cid: &Cid, token: &[16]u8): ?int {
    if retire_prior > seq || cid.n < 1 || cid.n > MaxCidLen {
        return none
    }
    sl := VarintLen(seq)
    rl := VarintLen(retire_prior)
    if sl == 0 || rl == 0 {
        return none
    }
    need := 1 + sl + rl + 1 + cid.n + 16
    if out.Len() < need {
        return none
    }
    out[0] = Trunc[u8](FrameNewConnectionId)
    var pos := 1
    pos += WriteVarint(mut out[pos..], seq)
    pos += WriteVarint(mut out[pos..], retire_prior)
    out[pos] = Trunc[u8](cid.n)
    pos += 1
    (mut out[pos..pos + cid.n]).CopyFrom(cid.b[0..cid.n])
    pos += cid.n
    (mut out[pos..pos + 16]).CopyFrom(token[..])
    return some(need)
}

// WriteRetireConnectionId writes a RETIRE_CONNECTION_ID (0x19) frame into out
// (RFC 9000 19.16). O(1).
WriteRetireConnectionId(out: mut []u8, seq: u64): ?int {
    return WriteOneVarFrame(out, FrameRetireConnectionId, seq)
}

// WritePathChallenge writes a PATH_CHALLENGE (0x1a) frame into out
// (RFC 9000 19.17). O(1).
WritePathChallenge(out: mut []u8, data: &[8]u8): ?int {
    if out.Len() < 9 {
        return none
    }
    out[0] = Trunc[u8](FramePathChallenge)
    (mut out[1..9]).CopyFrom(data[..])
    return some(9)
}

// WritePathResponse writes a PATH_RESPONSE (0x1b) frame into out
// (RFC 9000 19.18). O(1).
WritePathResponse(out: mut []u8, data: &[8]u8): ?int {
    if out.Len() < 9 {
        return none
    }
    out[0] = Trunc[u8](FramePathResponse)
    (mut out[1..9]).CopyFrom(data[..])
    return some(9)
}

// WriteConnectionClose writes a CONNECTION_CLOSE (0x1c transport / 0x1d app)
// frame into out (RFC 9000 19.19). O(reason.Len()); allocates nothing.
WriteConnectionClose(out: mut []u8, code: u64, frame_type: u64, reason: []u8, app: bool): ?int {
    cl := VarintLen(code)
    rl := VarintLen(Trunc[u64](reason.Len()))
    if cl == 0 || rl == 0 {
        return none
    }
    var fl := 0
    if !app {
        fl = VarintLen(frame_type)
        if fl == 0 {
            return none
        }
    }
    need := 1 + cl + fl + rl + reason.Len()
    if out.Len() < need {
        return none
    }
    out[0] = Trunc[u8](if app { FrameConnectionCloseApp } else { FrameConnectionClose })
    var pos := 1
    pos += WriteVarint(mut out[pos..], code)
    if !app {
        pos += WriteVarint(mut out[pos..], frame_type)
    }
    pos += WriteVarint(mut out[pos..], Trunc[u64](reason.Len()))
    (mut out[pos..pos + reason.Len()]).CopyFrom(reason)
    return some(need)
}

// WriteHandshakeDone writes a HANDSHAKE_DONE (0x1e) frame into out
// (RFC 9000 19.20). O(1).
WriteHandshakeDone(out: mut []u8): ?int {
    if out.Len() < 1 {
        return none
    }
    out[0] = Trunc[u8](FrameHandshakeDone)
    return some(1)
}

// WriteFrame writes any QFrame into out (using ranges for Ack and with_len
// true for Stream). Returns none if out is too short or fields are invalid.
WriteFrame(out: mut []u8, f: &QFrame, ranges: []AckRange): ?int {
    return match f {
        .Padding(n) { WritePadding(out, *n) }
        .Ping { WritePing(out) }
        .Ack(largest, delay, ranges: count) {
            _ = largest
            WriteAck(out, ranges[0..*count], *delay)
        }
        .ResetStream(id, code, final_size) { WriteResetStream(out, *id, *code, *final_size) }
        .StopSending(id, code) { WriteStopSending(out, *id, *code) }
        .Crypto(offset, data) { WriteCrypto(out, *offset, *data) }
        .NewToken(token) { WriteNewToken(out, *token) }
        .Stream(id, offset, data, fin) { WriteStream(out, *id, *offset, *data, *fin, true) }
        .MaxData(max) { WriteMaxData(out, *max) }
        .MaxStreamData(id, max) { WriteMaxStreamData(out, *id, *max) }
        .MaxStreams(bidi, max) { WriteMaxStreams(out, *bidi, *max) }
        .DataBlocked(max) { WriteDataBlocked(out, *max) }
        .StreamDataBlocked(id, max) { WriteStreamDataBlocked(out, *id, *max) }
        .StreamsBlocked(bidi, max) { WriteStreamsBlocked(out, *bidi, *max) }
        .NewConnectionId(seq, retire_prior, cid, token) { WriteNewConnectionId(out, *seq, *retire_prior, cid, token) }
        .RetireConnectionId(seq) { WriteRetireConnectionId(out, *seq) }
        .PathChallenge(data) { WritePathChallenge(out, data) }
        .PathResponse(data) { WritePathResponse(out, data) }
        .ConnectionClose(code, frame_type, reason, app) { WriteConnectionClose(out, *code, *frame_type, *reason, *app) }
        .HandshakeDone { WriteHandshakeDone(out) }
    }
}

WriteOneVarFrame(out: mut []u8, ty: u64, v: u64): ?int {
    vl := VarintLen(v)
    if vl == 0 || out.Len() < 1 + vl {
        return none
    }
    out[0] = Trunc[u8](ty)
    _ = WriteVarint(mut out[1..], v)
    return some(1 + vl)
}

WriteTwoVarFrame(out: mut []u8, ty: u64, id: i64, v: u64): ?int {
    if id < 0 {
        return none
    }
    il := VarintLen(Trunc[u64](id))
    vl := VarintLen(v)
    if il == 0 || vl == 0 || out.Len() < 1 + il + vl {
        return none
    }
    out[0] = Trunc[u8](ty)
    var pos := 1
    pos += WriteVarint(mut out[pos..], Trunc[u64](id))
    pos += WriteVarint(mut out[pos..], v)
    return some(pos)
}
