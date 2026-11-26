// Why: std.tls is TLS 1.3 (RFC 8446) over a net.Conn, client and server,
// plus an opt-in TLS 1.2 client (RFC 5246, client12.mk), written in Mk;
// this file is the module's front door.
//
// What: the errors, Config, DefaultConfig, Version, Identity and
// SystemPoolOf. The key schedule, records, messages, connection and roles
// are in files of their own. Not built: resumption and PSK, 0-RTT, client
// certificates, renegotiation, compression. Experimental until it has had
// an outside security review.
//
// How: every secret, key and IV lives in a key object of std.crypto (Prk,
// AesGcm) or in a fixed array of this module that is wiped with
// crypto.Wipe when it is replaced or the connection ends. No branch, index
// or loop bound depends on a secret: the module's own secret-dependent
// work is the XOR of a record nonce, the Finished comparison
// (crypto.Equal) and the copies between key objects; the rest is
// std.crypto's, whose constant-time rules apply.

import std.crypto
import std.net
import std.time
import std.x509

/// The peer sent an alert; code is the alert's description, such as 40
/// handshake_failure or 70 protocol_version. A close_notify is not an
/// error: Read reports it as the end of the stream.
export error Alert(code: u8)

/// The peer broke the protocol: a malformed or unexpected message or
/// record, a bad Finished or signature, or a value RFC 8446 forbids. The
/// connection sent the matching alert before it failed.
export error Protocol

/// The peer chose what std.tls does not support or the configuration
/// refuses: a version below the configured minimum, or a cipher suite,
/// group or signature scheme std.tls does not offer.
export error Unsupported

/// Every failure of std.tls: its own three, the certificate check's
/// (x509.Error), and the connection's (net.Error), which include Canceled
/// and time.TimedOut for a handshake or a Close past its limit. The set is
/// open, so a match on it outside std keeps a `_` arm.
export open type Error = Alert | Protocol | Unsupported | x509.Error | net.Error

/// A protocol version std.tls speaks: TLS 1.2 (client only) or TLS 1.3.
export enum Version {
    Tls12
    Tls13
}

/// What a server presents: a certificate chain, leaf first, and the leaf's
/// private key, which is P-256 or Ed25519.
export struct Identity {
    /// The chain, leaf first, as the server sends it.
    export chain: []x509.Certificate
    /// The leaf's private key.
    export key: &crypto.PrivateKey
}

/// The settings of a TLS connection: what it trusts, presents and allows.
/// A plain record of references and slices into the caller's memory, Send
/// and Share, so one Config serves every task. Start from DefaultConfig and
/// assign the fields to change; a literal of it does not compile outside
/// std. A Conn copies what it needs, so a Config may go once the handshake
/// is done.
export struct Config {
    /// The certificates a client trusts. none means the system's
    /// (x509.SystemPool), loaded by the first handshake that needs it and
    /// kept for the process; on Linux the store is read once and kept when
    /// it holds a certificate. Under a simulated or taped Io (a Sim's,
    /// Recorder's or Replayer's), which SystemPool refuses, each handshake
    /// loads SystemRoots' pool through that Io instead.
    export roots: ?&x509.Pool
    /// A server's chains and keys. The server presents the first whose key
    /// the client's signature schemes allow.
    export identities: []Identity
    /// The application protocols offered (a client) or accepted (a
    /// server), in preference order (RFC 7301); empty offers none. Each
    /// is 1 to 255 bytes, at most 65,535 bytes in all: a client refuses
    /// another list before any I/O, and a server never matches such a name.
    export alpn: []str
    /// The lowest version a client accepts. Tls12 turns on the TLS 1.2
    /// client; the default, Tls13, offers TLS 1.3 only.
    export min_version: Version
    /// The longest a handshake may take before it fails with
    /// time.TimedOut; zero or less fails at once.
    export handshake: time.Duration
    /// The longest a Close may wait for the peer before it fails with
    /// time.TimedOut, also under a NoCancel Io.
    export close: time.Duration
    sealed: u8
}

/// The default settings: trust the system's roots (roots is none), offer
/// no application protocol, TLS 1.3 only, a 10-second handshake limit and
/// a 5-second close limit, and no server identities.
export const DefaultConfig: Config = Config(roots: none, identities: [], alpn: [], min_version: Version.Tls13,
    handshake: time.Seconds(10), close: time.Seconds(5), sealed: 0)

// SystemPoolOf is the pool a client with roots none verifies against:
// x509.SystemPool's, kept for the process in one block of h, or, where it
// fails with Unsupported (a simulated or taped Io, a target with no store), the
// pool x509.SystemRoots loads into a, which fails the same way on a
// target with no store.
SystemPoolOf(h: &Heap, a: &Arena, io: Io): x509.Error!x509.Pool from a {
    p := x509.SystemPool(h, io) else e {
        match e {
            x509.Unsupported {
                return try x509.SystemRoots(a, io)
            }
            _ {
                fail e
            }
        }
    }
    return *p
}
