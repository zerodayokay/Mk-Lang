// Why: the wire code, loss recovery, the stream table and the connection
// all name packet number spaces, connection ids, acknowledged ranges and
// the records of what a sent packet carried, so these types are declared
// once, apart from any one of them.
//
// What: the module std.quic's shared types: Space, Cid, AckRange,
// SentKind and SentFrame, all module-private, and the exported Version1.
//
// How: plain values with no methods; each part of the module that reads or
// writes them states its own rules over them.

/// QUIC version 1, the version this module speaks (RFC 9000 15).
export const Version1: u32 = 0x00000001

// The longest connection id QUIC version 1 allows (RFC 9000 17.2).
const MaxCidLen = 20

// A packet number space (RFC 9000 12.3): Initial packets, Handshake
// packets, and the application data space of 1-RTT packets. Each has its
// own packet numbers, acknowledgements and keys.
enum Space {
    Initial
    Handshake
    App
}

// A connection id (RFC 9000 5.1): its first n bytes of b, 0 <= n <=
// MaxCidLen.
struct Cid {
    b: [MaxCidLen]u8
    n: int
}

// The packet numbers lo through hi of one space, both included: one range
// of an ACK frame (RFC 9000 19.3.1), lo <= hi.
struct AckRange {
    lo: i64
    hi: i64
}

// What one frame of a sent packet carried, kept until the packet is
// acknowledged or declared lost, so that loss resends the data and not the
// packet. Stream: len bytes of stream id from offset, and its end when fin.
// Crypto: len bytes of the CRYPTO stream of level from offset. Control: a
// control frame, by its index in the connection's record of the control
// frames it may have to send again.
enum SentKind {
    Stream(id: i64, offset: u64, len: int, fin: bool)
    Crypto(level: Space, offset: u64, len: int)
    Control(index: int)
}

// One frame's record in a sent packet's list.
struct SentFrame {
    kind: SentKind
}
