// Why: decrypted QUIC packet payloads are sequences of variable-length
// frames that must be decoded without allocation and validated against every
// wire encoding limit before any state changes.
//
// What: frame type constants, QFrame and FrameIter with Next.
//
// How: RFC 9000 sections 12.4 and 19. FrameIter scans directly over the
// decrypted payload slice, enforcing minimal frame-type varints, ACK range
// bounds (even past the caller's ranges buffer cap), 2^62 - 1 stream/crypto
// offset limits and 2^60 stream-count limits without allocating.

// RFC 9000 19 frame types.
const FramePadding: u64 = 0x00
const FramePing: u64 = 0x01
const FrameAck: u64 = 0x02
const FrameAckEcn: u64 = 0x03
const FrameResetStream: u64 = 0x04
const FrameStopSending: u64 = 0x05
const FrameCrypto: u64 = 0x06
const FrameNewToken: u64 = 0x07
const FrameStreamBase: u64 = 0x08
const FrameStreamMax: u64 = 0x0f
const FrameMaxData: u64 = 0x10
const FrameMaxStreamData: u64 = 0x11
const FrameMaxStreamsBidi: u64 = 0x12
const FrameMaxStreamsUni: u64 = 0x13
const FrameDataBlocked: u64 = 0x14
const FrameStreamDataBlocked: u64 = 0x15
const FrameStreamsBlockedBidi: u64 = 0x16
const FrameStreamsBlockedUni: u64 = 0x17
const FrameNewConnectionId: u64 = 0x18
const FrameRetireConnectionId: u64 = 0x19
const FramePathChallenge: u64 = 0x1a
const FramePathResponse: u64 = 0x1b
const FrameConnectionClose: u64 = 0x1c
const FrameConnectionCloseApp: u64 = 0x1d
const FrameHandshakeDone: u64 = 0x1e

// QFrame is one parsed frame of a decrypted QUIC packet payload (RFC 9000 19).
// Slice payloads (Crypto.data, NewToken.token, Stream.data, ConnectionClose.reason)
// borrow from the packet buffer held by FrameIter.
enum QFrame {
    Padding(n: int)
    Ping
    Ack(largest: i64, delay: u64, ranges: int)
    ResetStream(id: i64, code: u64, final_size: u64)
    StopSending(id: i64, code: u64)
    Crypto(offset: u64, data: []u8)
    NewToken(token: []u8)
    Stream(id: i64, offset: u64, data: []u8, fin: bool)
    MaxData(max: u64)
    MaxStreamData(id: i64, max: u64)
    MaxStreams(bidi: bool, max: u64)
    DataBlocked(max: u64)
    StreamDataBlocked(id: i64, max: u64)
    StreamsBlocked(bidi: bool, max: u64)
    NewConnectionId(seq: u64, retire_prior: u64, cid: Cid, token: [16]u8)
    RetireConnectionId(seq: u64)
    PathChallenge(data: [8]u8)
    PathResponse(data: [8]u8)
    ConnectionClose(code: u64, frame_type: u64, reason: []u8, app: bool)
    HandshakeDone
}

// FrameIter iterates over the frames of a decrypted QUIC packet payload
// (RFC 9000 12.4, 19). An empty payload fails on the first Next call with
// ErrProtocolViolation.
struct FrameIter {
    b: []u8
    at: int
    client: bool
    started: bool
}

// FrameIter.New returns an iterator over decrypted payload b for the role
// indicated by client. O(1); allocates nothing.
FrameIter.New(b: []u8, client: bool): FrameIter from b {
    return FrameIter(b: b, at: 0, client: client, started: false)
}

// FrameIter.Left is the number of unread payload bytes. O(1).
FrameIter.Left(&self): int {
    return self.b.Len() - self.at
}

// FrameIter.ReadVarint reads a varint field for frame ty, failing with
// ErrFrameEncoding if the payload ends early.
FrameIter.ReadVarint(mut self, ty: u64): Error!u64 {
    r := ReadVarint(self.b, self.at) else {
        fail TransportError(code: ErrFrameEncoding, frame: ty)
    }
    self.at += r.n
    return r.v
}

// FrameIter.Take reads n bytes for frame ty, failing with ErrFrameEncoding if
// fewer than n bytes remain.
FrameIter.Take(mut self, ty: u64, n: int): Error![]u8 from self.b {
    if n < 0 || self.Left() < n {
        fail TransportError(code: ErrFrameEncoding, frame: ty)
    }
    s := self.b[self.at..self.at + n]
    self.at += n
    return s
}

// FrameIter.TakeVarBytes reads a varint length and that many payload bytes for
// frame ty, failing with ErrFrameEncoding on truncation or overflow.
FrameIter.TakeVarBytes(mut self, ty: u64): Error![]u8 from self.b {
    n64 := try self.ReadVarint(ty)
    if n64 > Trunc[u64](self.Left()) {
        fail TransportError(code: ErrFrameEncoding, frame: ty)
    }
    return try self.Take(ty, Trunc[int](n64))
}

// FrameIter.Next decodes the next frame in the payload, writing an ACK
// frame's ranges into ranges (up to ranges.Len(), while parsing and bounds-
// checking every range in the frame). Returns none at the end of a non-empty
// payload. O(frame bytes + ack ranges); allocates nothing.
FrameIter.Next(mut self, ranges: mut []AckRange): Error!?QFrame from self {
    if self.at >= self.b.Len() {
        if !self.started {
            self.started = true
            fail TransportError(code: ErrProtocolViolation, frame: 0)
        }
        return none
    }
    self.started = true
    ft := ReadVarint(self.b, self.at) else {
        fail TransportError(code: ErrFrameEncoding, frame: 0)
    }
    self.at += ft.n
    ty := ft.v
    if !IsMinimalVarint(ty, ft.n) {
        fail TransportError(code: ErrProtocolViolation, frame: ty)
    }
    if ty == FramePadding {
        var count := 1
        for self.at < self.b.Len() && self.b[self.at] == 0x00 {
            self.at += 1
            count += 1
        }
        return some(QFrame.Padding(n: count))
    }
    if ty == FramePing {
        return some(QFrame.Ping)
    }
    if ty == FrameAck || ty == FrameAckEcn {
        return some(try self.ParseAck(ty, ranges))
    }
    if ty == FrameResetStream {
        id := Trunc[i64](try self.ReadVarint(ty))
        code := try self.ReadVarint(ty)
        final_size := try self.ReadVarint(ty)
        return some(QFrame.ResetStream(id: id, code: code, final_size: final_size))
    }
    if ty == FrameStopSending {
        id := Trunc[i64](try self.ReadVarint(ty))
        code := try self.ReadVarint(ty)
        return some(QFrame.StopSending(id: id, code: code))
    }
    if ty == FrameCrypto {
        offset := try self.ReadVarint(ty)
        len64 := try self.ReadVarint(ty)
        if len64 > MaxVarint - offset || len64 > Trunc[u64](self.Left()) {
            fail TransportError(code: ErrFrameEncoding, frame: ty)
        }
        data := try self.Take(ty, Trunc[int](len64))
        return some(QFrame.Crypto(offset: offset, data: data))
    }
    if ty == FrameNewToken {
        token := try self.TakeVarBytes(ty)
        if self.client && token.Len() == 0 {
            fail TransportError(code: ErrFrameEncoding, frame: ty)
        }
        return some(QFrame.NewToken(token: token))
    }
    if ty >= FrameStreamBase && ty <= FrameStreamMax {
        id := Trunc[i64](try self.ReadVarint(ty))
        var offset: u64 = 0
        if (ty & 0x04) != 0 {
            offset = try self.ReadVarint(ty)
        }
        var data := self.b[self.at..self.b.Len()]
        if (ty & 0x02) != 0 {
            data = try self.TakeVarBytes(ty)
        } else {
            self.at = self.b.Len()
        }
        if Trunc[u64](data.Len()) > MaxVarint - offset {
            fail TransportError(code: ErrFrameEncoding, frame: ty)
        }
        fin := (ty & 0x01) != 0
        return some(QFrame.Stream(id: id, offset: offset, data: data, fin: fin))
    }
    if ty == FrameMaxData {
        max := try self.ReadVarint(ty)
        return some(QFrame.MaxData(max: max))
    }
    if ty == FrameMaxStreamData {
        id := Trunc[i64](try self.ReadVarint(ty))
        max := try self.ReadVarint(ty)
        return some(QFrame.MaxStreamData(id: id, max: max))
    }
    if ty == FrameMaxStreamsBidi || ty == FrameMaxStreamsUni {
        max := try self.ReadVarint(ty)
        if max > MaxStreamsLimit {
            fail TransportError(code: ErrFrameEncoding, frame: ty)
        }
        return some(QFrame.MaxStreams(bidi: ty == FrameMaxStreamsBidi, max: max))
    }
    if ty == FrameDataBlocked {
        max := try self.ReadVarint(ty)
        return some(QFrame.DataBlocked(max: max))
    }
    if ty == FrameStreamDataBlocked {
        id := Trunc[i64](try self.ReadVarint(ty))
        max := try self.ReadVarint(ty)
        return some(QFrame.StreamDataBlocked(id: id, max: max))
    }
    if ty == FrameStreamsBlockedBidi || ty == FrameStreamsBlockedUni {
        max := try self.ReadVarint(ty)
        if max > MaxStreamsLimit {
            fail TransportError(code: ErrFrameEncoding, frame: ty)
        }
        return some(QFrame.StreamsBlocked(bidi: ty == FrameStreamsBlockedBidi, max: max))
    }
    if ty == FrameNewConnectionId {
        seq := try self.ReadVarint(ty)
        retire_prior := try self.ReadVarint(ty)
        if retire_prior > seq || self.Left() < 1 {
            fail TransportError(code: ErrFrameEncoding, frame: ty)
        }
        cid_len := int(self.b[self.at])
        self.at += 1
        if cid_len < 1 || cid_len > MaxCidLen {
            fail TransportError(code: ErrFrameEncoding, frame: ty)
        }
        cid_bytes := try self.Take(ty, cid_len)
        tok_bytes := try self.Take(ty, 16)
        var token: [16]u8 = [0; 16]
        (mut token[..]).CopyFrom(tok_bytes)
        return some(QFrame.NewConnectionId(seq: seq, retire_prior: retire_prior, cid: CidFromSlice(cid_bytes), token: token))
    }
    if ty == FrameRetireConnectionId {
        seq := try self.ReadVarint(ty)
        return some(QFrame.RetireConnectionId(seq: seq))
    }
    if ty == FramePathChallenge {
        raw := try self.Take(ty, 8)
        var data: [8]u8 = [0; 8]
        (mut data[..]).CopyFrom(raw)
        return some(QFrame.PathChallenge(data: data))
    }
    if ty == FramePathResponse {
        raw := try self.Take(ty, 8)
        var data: [8]u8 = [0; 8]
        (mut data[..]).CopyFrom(raw)
        return some(QFrame.PathResponse(data: data))
    }
    if ty == FrameConnectionClose || ty == FrameConnectionCloseApp {
        app := ty == FrameConnectionCloseApp
        code := try self.ReadVarint(ty)
        var frame_type: u64 = 0
        if !app {
            frame_type = try self.ReadVarint(ty)
        }
        reason := try self.TakeVarBytes(ty)
        return some(QFrame.ConnectionClose(code: code, frame_type: frame_type, reason: reason, app: app))
    }
    if ty == FrameHandshakeDone {
        return some(QFrame.HandshakeDone)
    }
    fail TransportError(code: ErrFrameEncoding, frame: ty)
}

// FrameIter.ParseAck parses an ACK (0x02) or ACK_ECN (0x03) frame, keeping
// up to ranges.Len() ranges while checking every range for negative packet
// numbers and ignoring ECN counts (RFC 9000 19.3).
FrameIter.ParseAck(mut self, ty: u64, ranges: mut []AckRange): Error!QFrame {
    largest := Trunc[i64](try self.ReadVarint(ty))
    delay := try self.ReadVarint(ty)
    count := try self.ReadVarint(ty)
    first_range := try self.ReadVarint(ty)
    if first_range > Trunc[u64](largest) {
        fail TransportError(code: ErrFrameEncoding, frame: ty)
    }
    // Each additional ACK Range requires at least 2 bytes (Gap + Length).
    if count > Trunc[u64](self.Left() / 2) {
        fail TransportError(code: ErrFrameEncoding, frame: ty)
    }
    var prev_smallest := largest - Trunc[i64](first_range)
    var kept := 0
    if ranges.Len() > 0 {
        ranges[0] = AckRange(lo: prev_smallest, hi: largest)
        kept = 1
    }
    var i: u64 = 0
    for i < count {
        gap := try self.ReadVarint(ty)
        ack_len := try self.ReadVarint(ty)
        if prev_smallest < 2 || gap > Trunc[u64](prev_smallest - 2) {
            fail TransportError(code: ErrFrameEncoding, frame: ty)
        }
        hi := prev_smallest - Trunc[i64](gap) - 2
        if ack_len > Trunc[u64](hi) {
            fail TransportError(code: ErrFrameEncoding, frame: ty)
        }
        lo := hi - Trunc[i64](ack_len)
        prev_smallest = lo
        if kept < ranges.Len() {
            ranges[kept] = AckRange(lo: lo, hi: hi)
            kept += 1
        }
        i += 1
    }
    if ty == FrameAckEcn {
        _ = try self.ReadVarint(ty)
        _ = try self.ReadVarint(ty)
        _ = try self.ReadVarint(ty)
    }
    return QFrame.Ack(largest: largest, delay: delay, ranges: kept)
}
