// Why: every net operation fails the same few ways; one error set lets
// callers match on them across TCP, UDP, Unix sockets and secure connects.
// What: the members of net.Error, SecureFailed's reason SecureFailure and
// its Format, and the Error set itself.
// How: every member is payload-free except Os, SecureFailed, StreamReset
// and MuxClosed; Canceled and Closed are the prelude's, TimedOut
// std.time's.

import std.time

/// Nothing listens at the address dialled.
export error Refused
/// The peer dropped the connection.
export error Reset
/// The address is already bound.
export error AddrInUse
/// No route leads to the address.
export error Unreachable
/// The address does not parse, a name Resolve looks up does not resolve,
/// or the address cannot be used here.
export error InvalidAddress
/// Any other OS failure, with the OS's error code. The code is 0 when the
/// OS layer answered something impossible (a count out of range, a send
/// that made no progress), and for a fault a simulated Io injects that
/// has no member of its own (Denied, for one).
export error Os(code: i64)
/// What this target, this kind of socket or this Io cannot do: a Unix
/// socket before Windows 10 1803, NoDelay on a Unix stream, a socket
/// option under std.sim.
export error Unsupported
/// A secure connector (net.Secure) could not make the connection secure;
/// why says at which step.
export error SecureFailed(why: SecureFailure)
/// The peer reset a stream of a multiplexed connection or asked it to
/// stop sending (HTTP/2's RST_STREAM, QUIC's RESET_STREAM and
/// STOP_SENDING); code is the peer's error code. The connection and its
/// other streams go on. Provisional.
export error StreamReset(code: u64)
/// A multiplexed connection ended, failing every stream still open on it.
/// code is the error code of the close: in the application's space (an
/// HTTP/2 GOAWAY's or HTTP/3's code) when app, the transport's (QUIC's)
/// otherwise; peer is true when the peer closed it, false when this side
/// did. Provisional.
export error MuxClosed(code: u64, app: bool, peer: bool)

/// Why a secure connect failed, as the connector reports it through
/// net.Secure, where no connection exists yet to ask.
export open enum SecureFailure {
    /// The handshake failed: an alert, a version or cipher mismatch.
    Handshake
    /// The peer's certificate does not parse or is not usable.
    BadCertificate
    /// The chain ends at no trusted root.
    UnknownAuthority
    /// A certificate of the chain is outside its validity period.
    Expired
    /// The certificate does not name the host dialled.
    NameMismatch
    /// The peer broke the protocol after the handshake began.
    Protocol
}

/// Writes the member's name, so SecureFailed prints as
/// SecureFailed(why: Expired). One Writer.Str call; allocates nothing.
impl Format for SecureFailure {
    Format(&self, w: mut Writer): WriteError!void {
        try w.Str(match *self {
            .Handshake { "Handshake" }
            .BadCertificate { "BadCertificate" }
            .UnknownAuthority { "UnknownAuthority" }
            .Expired { "Expired" }
            .NameMismatch { "NameMismatch" }
            .Protocol { "Protocol" }
        })
    }
}

/// Every way a net operation fails. It holds the prelude's Canceled and
/// Closed, and time.TimedOut, which reports a limit of the connection's
/// own (a Close that speaks to the peer, a handshake), never a deadline of
/// the caller's scope: that is Canceled. std.tls and std.websocket include
/// this whole set in theirs. It is open: a match on it outside std keeps a
/// `_` arm.
export open type Error = Refused | Reset | AddrInUse | Unreachable | InvalidAddress | Os | Canceled | Closed | time.TimedOut | Unsupported | SecureFailed | StreamReset | MuxClosed | OutOfMemory
