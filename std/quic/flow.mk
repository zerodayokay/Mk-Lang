// Why: QUIC stream and connection flow control enforce memory bounds on both
// sides, return connection credit as data is buffered, return stream credit at
// half-window consumption or body admission, and account for RESET_STREAM final
// sizes without exceeding 2^62 - 1.
//
// What: transport error codes (RFC 9000 20.1), TransportError, GapLimit, Error,
// StreamLimits, Credit, ConnFlow, and local Q3 frame writers Q23Write*.
//
// How: Credit tracks consumed bytes, advertised limit, admission floor, and
// sequence numbers so lost MAX_DATA/MAX_STREAM_DATA frames retransmit the latest
// limit rather than a stale copy (RFC 9000 13.3). ConnFlow tracks both receive
// and send connection windows.

import std.net

// RFC 9000 20.1 transport error codes (lines 6759-6830).
const ErrNoError: u64 = 0x00
const ErrInternalError: u64 = 0x01
const ErrConnectionRefused: u64 = 0x02
const ErrFlowControlError: u64 = 0x03
const ErrStreamLimitError: u64 = 0x04
const ErrStreamStateError: u64 = 0x05
const ErrFinalSizeError: u64 = 0x06
const ErrFrameEncodingError: u64 = 0x07
const ErrTransportParameterError: u64 = 0x08
const ErrConnectionIdLimitError: u64 = 0x09
const ErrProtocolViolation: u64 = 0x0A
const ErrInvalidToken: u64 = 0x0B
const ErrApplicationError: u64 = 0x0C
const ErrCryptoBufferExceeded: u64 = 0x0D
const ErrKeyUpdateError: u64 = 0x0E
const ErrAeadLimitReached: u64 = 0x0F
const ErrNoViablePath: u64 = 0x10

// A QUIC transport protocol error (RFC 9000 20.1).
error TransportError(code: u64)

// Receiving a STREAM frame would create a 33rd reassembly gap; the caller must
// drop the entire packet unacknowledged (RFC 9000 13.1).
error GapLimit

// Module-private error set for recovery, streams, flow control, and CRYPTO streams.
type Error = TransportError | GapLimit | net.StreamReset | Closed | OutOfMemory

// Flow-control and stream limits (Section 3.4.2, RFC 9000 4, 18.2).
const ConnWindow: u64 = 16_777_216
const ClientStreamWindow: u64 = 1_048_576
const ServerInitialStreamWindow: u64 = 16_384
const UniStreamWindow: u64 = 65_536
const DefaultMaxStreamsBidi: u64 = 100
const DefaultMaxStreamsUni: u64 = 8
const MaxOffset: u64 = 0x3FFF_FFFF_FFFF_FFFF
const MaxStreamCount: u64 = 0x1000_0000_0000_0000
const MaxRecvGaps: int = 32
const CryptoBufferLimit: int = 65_536

// Adds a and b, saturating at MaxOffset.
SatAddCap(a: u64, b: u64): u64 {
    if a >= MaxOffset || b > MaxOffset - a {
        return MaxOffset
    }
    return a + b
}

// Initial local and peer stream/data limits passed to Streams.New.
struct StreamLimits {
    max_data: u64
    max_stream_data_bidi_local: u64
    max_stream_data_bidi_remote: u64
    max_stream_data_uni: u64
    max_streams_bidi: u64
    max_streams_uni: u64
    peer_max_data: u64
    peer_max_stream_data_bidi_local: u64
    peer_max_stream_data_bidi_remote: u64
    peer_max_stream_data_uni: u64
    peer_max_streams_bidi: u64
    peer_max_streams_uni: u64
}

// Returns the design defaults for client or server (Section 3.4.2), with peer
// limits initialized to zero until peer transport parameters are applied.
StreamLimits.Default(client: bool): StreamLimits {
    bidi_remote := if client { ClientStreamWindow } else { ServerInitialStreamWindow }
    return StreamLimits(
        max_data: ConnWindow,
        max_stream_data_bidi_local: ClientStreamWindow,
        max_stream_data_bidi_remote: bidi_remote,
        max_stream_data_uni: UniStreamWindow,
        max_streams_bidi: DefaultMaxStreamsBidi,
        max_streams_uni: DefaultMaxStreamsUni,
        peer_max_data: 0,
        peer_max_stream_data_bidi_local: 0,
        peer_max_stream_data_bidi_remote: 0,
        peer_max_stream_data_uni: 0,
        peer_max_streams_bidi: 0,
        peer_max_streams_uni: 0,
    )
}

// Receive-side flow-control credit tracker for a stream or the connection.
// Sends updates at half the window consumed, upon body admission (Admit), or
// when the latest sent update is declared lost (RFC 9000 4.1, 4.2, 13.3).
struct Credit {
    window: u64
    max_sent: u64
    min_limit: u64
    consumed: u64
    force_send: bool
    pending_resend: bool
    tx_seq: u64
}

// Creates a Credit starting with initial_window already advertised to the peer. O(1).
Credit.New(initial_window: u64): Credit {
    capped := if initial_window > MaxOffset { MaxOffset } else { initial_window }
    return Credit(
        window: capped,
        max_sent: capped,
        min_limit: capped,
        consumed: 0,
        force_send: false,
        pending_resend: false,
        tx_seq: 0,
    )
}

// Advances connection-level accounted bytes upon buffering or final-size reset
// (RFC 9000 4.1, 4.5). O(1).
Credit.Buffered(mut self, n: u64) {
    self.consumed = SatAddCap(self.consumed, n)
}

// Advances stream-level consumed bytes upon application Read. O(1).
Credit.Consumed(mut self, n: u64) {
    self.consumed = SatAddCap(self.consumed, n)
}

// Raises the advertised stream receive limit to at least total from stream
// start (used for server body admission). O(1).
Credit.Admit(mut self, total: u64) {
    capped := if total > MaxOffset { MaxOffset } else { total }
    if capped > self.min_limit {
        self.min_limit = capped
    }
    if capped > self.window {
        self.window = capped
    }
    if self.min_limit > self.max_sent {
        self.force_send = true
    }
}

// Computes the target limit if a MAX_DATA / MAX_STREAM_DATA update is owed,
// or none when neither half-window consumption, admission, nor loss requires
// sending one (RFC 9000 4.2, 13.3). Never returns 0 or below max_sent. O(1).
Credit.Owed(&self): ?u64 {
    win_target := SatAddCap(self.consumed, self.window)
    target := if self.min_limit > win_target { self.min_limit } else { win_target }
    if target == 0 {
        return none
    }
    if self.pending_resend {
        return some(if target > self.max_sent { target } else { self.max_sent })
    }
    if target <= self.max_sent {
        return none
    }
    if self.force_send || target - self.max_sent >= self.window / 2 {
        return some(target)
    }
    return none
}

// Claims any owed update, advancing max_sent and tx_seq. O(1).
Credit.TakeOwed(mut self): ?u64 {
    v := self.Owed() else {
        return none
    }
    if v > self.max_sent {
        self.max_sent = v
    }
    self.force_send = false
    self.pending_resend = false
    self.tx_seq += 1
    return some(self.max_sent)
}

// Marks the most recent MAX_* frame with seq as lost so the current limit is
// resent; ignores loss of superseded frames (RFC 9000 13.3). O(1).
Credit.OnLost(mut self, seq: u64) {
    if seq == self.tx_seq && self.max_sent > 0 {
        self.pending_resend = true
    }
}

// Connection-level receive and send flow controller (RFC 9000 4.1, 4.5).
struct ConnFlow {
    rx_credit: Credit
    rx_total: u64
    tx_max_data: u64
    tx_sent: u64
    blocked_sent_at: ?u64
    blocked_pending: bool
}

// Initializes ConnFlow with local and peer initial_max_data limits. O(1).
ConnFlow.New(local_max_data: u64, peer_max_data: u64): ConnFlow {
    return ConnFlow(
        rx_credit: Credit.New(local_max_data),
        rx_total: 0,
        tx_max_data: peer_max_data,
        tx_sent: 0,
        blocked_sent_at: none,
        blocked_pending: false,
    )
}

// Validates and accounts for delta new bytes received across all streams
// (including RESET_STREAM final sizes, RFC 9000 4.1, 4.5). Fails with
// TransportError(ErrFlowControlError) if rx_total + delta > rx_credit.max_sent.
ConnFlow.AccountRecv(mut self, delta: u64): Error!void {
    if delta == 0 {
        return
    }
    if delta > self.rx_credit.max_sent - self.rx_total {
        fail TransportError(code: ErrFlowControlError)
    }
    self.rx_total += delta
    self.rx_credit.Buffered(delta)
}

// Updates peer's MAX_DATA; non-increasing values are ignored (RFC 9000 4.1).
ConnFlow.OnMaxData(mut self, v: u64) {
    if v > self.tx_max_data {
        self.tx_max_data = v
        self.blocked_pending = false
    }
}

// Handles peer's DATA_BLOCKED(limit); drops stale frames for already-raised
// limits without error (RFC 9000 19.12).
ConnFlow.OnDataBlocked(mut self, limit: u64) {
    _ = limit
}

// Returns available connection-level send credit in bytes. O(1).
ConnFlow.TxAvail(&self): u64 {
    if self.tx_max_data > self.tx_sent {
        return self.tx_max_data - self.tx_sent
    }
    return 0
}

// Marks connection flow control as blocked when a stream has unsent data and
// tx_avail == 0, queuing DATA_BLOCKED once per blocking limit (RFC 9000 4.1).
ConnFlow.NoteBlocked(mut self) {
    if self.TxAvail() > 0 {
        return
    }
    need := match self.blocked_sent_at {
        none { true }
        some(prev) { prev != self.tx_max_data }
    }
    if need {
        self.blocked_sent_at = some(self.tx_max_data)
        self.blocked_pending = true
    }
}

// Re-queues DATA_BLOCKED on loss only if still blocked at that exact limit
// (RFC 9000 13.3).
ConnFlow.OnBlockedLost(mut self, limit: u64) {
    if self.TxAvail() == 0 && limit == self.tx_max_data {
        self.blocked_pending = true
    }
}

// Local Q3 frame encoders (RFC 9000 19). Each returns bytes written, or 0 if
// out is too short.
Q23WriteStream(out: mut []u8, id: i64, offset: u64, data: []u8, fin: bool): int {
    var ftype: u64 = 0x0A // STREAM + LEN
    if offset > 0 {
        ftype |= 0x04 // OFF
    }
    if fin {
        ftype |= 0x01 // FIN
    }
    need := 1 + Q23VarintLen(Cast[u64](id)) + (if offset > 0 { Q23VarintLen(offset) } else { 0 }) + Q23VarintLen(Cast[u64](data.Len())) + data.Len()
    if out.Len() < need {
        return 0
    }
    var pos := 0
    pos += Q23WriteVarint(mut out[pos..], ftype)
    pos += Q23WriteVarint(mut out[pos..], Cast[u64](id))
    if offset > 0 {
        pos += Q23WriteVarint(mut out[pos..], offset)
    }
    pos += Q23WriteVarint(mut out[pos..], Cast[u64](data.Len()))
    if data.Len() > 0 {
        (mut out[pos..pos + data.Len()]).CopyFrom(data)
        pos += data.Len()
    }
    return pos
}

Q23StreamHeaderLen(id: i64, offset: u64, data_len: int): int {
    return 1 + Q23VarintLen(Cast[u64](id)) + (if offset > 0 { Q23VarintLen(offset) } else { 0 }) + Q23VarintLen(Cast[u64](data_len))
}

Q23WriteResetStream(out: mut []u8, id: i64, code: u64, final_size: u64): int {
    need := 1 + Q23VarintLen(Cast[u64](id)) + Q23VarintLen(code) + Q23VarintLen(final_size)
    if out.Len() < need {
        return 0
    }
    var pos := 0
    pos += Q23WriteVarint(mut out[pos..], 0x04)
    pos += Q23WriteVarint(mut out[pos..], Cast[u64](id))
    pos += Q23WriteVarint(mut out[pos..], code)
    pos += Q23WriteVarint(mut out[pos..], final_size)
    return pos
}

Q23WriteStopSending(out: mut []u8, id: i64, code: u64): int {
    need := 1 + Q23VarintLen(Cast[u64](id)) + Q23VarintLen(code)
    if out.Len() < need {
        return 0
    }
    var pos := 0
    pos += Q23WriteVarint(mut out[pos..], 0x05)
    pos += Q23WriteVarint(mut out[pos..], Cast[u64](id))
    pos += Q23WriteVarint(mut out[pos..], code)
    return pos
}

Q23WriteMaxData(out: mut []u8, max: u64): int {
    need := 1 + Q23VarintLen(max)
    if out.Len() < need {
        return 0
    }
    var pos := 0
    pos += Q23WriteVarint(mut out[pos..], 0x10)
    pos += Q23WriteVarint(mut out[pos..], max)
    return pos
}

Q23WriteMaxStreamData(out: mut []u8, id: i64, max: u64): int {
    need := 1 + Q23VarintLen(Cast[u64](id)) + Q23VarintLen(max)
    if out.Len() < need {
        return 0
    }
    var pos := 0
    pos += Q23WriteVarint(mut out[pos..], 0x11)
    pos += Q23WriteVarint(mut out[pos..], Cast[u64](id))
    pos += Q23WriteVarint(mut out[pos..], max)
    return pos
}

Q23WriteMaxStreams(out: mut []u8, bidi: bool, max: u64): int {
    need := 1 + Q23VarintLen(max)
    if out.Len() < need {
        return 0
    }
    var pos := 0
    pos += Q23WriteVarint(mut out[pos..], if bidi { 0x12 } else { 0x13 })
    pos += Q23WriteVarint(mut out[pos..], max)
    return pos
}

Q23WriteDataBlocked(out: mut []u8, limit: u64): int {
    need := 1 + Q23VarintLen(limit)
    if out.Len() < need {
        return 0
    }
    var pos := 0
    pos += Q23WriteVarint(mut out[pos..], 0x14)
    pos += Q23WriteVarint(mut out[pos..], limit)
    return pos
}

Q23WriteStreamDataBlocked(out: mut []u8, id: i64, limit: u64): int {
    need := 1 + Q23VarintLen(Cast[u64](id)) + Q23VarintLen(limit)
    if out.Len() < need {
        return 0
    }
    var pos := 0
    pos += Q23WriteVarint(mut out[pos..], 0x15)
    pos += Q23WriteVarint(mut out[pos..], Cast[u64](id))
    pos += Q23WriteVarint(mut out[pos..], limit)
    return pos
}

Q23WriteStreamsBlocked(out: mut []u8, bidi: bool, limit: u64): int {
    need := 1 + Q23VarintLen(limit)
    if out.Len() < need {
        return 0
    }
    var pos := 0
    pos += Q23WriteVarint(mut out[pos..], if bidi { 0x16 } else { 0x17 })
    pos += Q23WriteVarint(mut out[pos..], limit)
    return pos
}
