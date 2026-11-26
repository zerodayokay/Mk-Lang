// Why: QUIC (RFC 9001) carries TLS 1.3 handshake messages in CRYPTO frames
// across Initial, Handshake and Application encryption levels without a TLS
// record layer or compatibility ChangeCipherSpec.
//
// What: QuicLevel, QuicSuite, QuicTransport, QuicDone, QuicClient,
// QuicServer, the private QuicWire[T] adapter and QuicPostMessage.
//
// How: QuicClient and QuicServer drive ClientFlights[QuicWire[T]] and
// ServerFlights[QuicWire[T]] under Config.handshake, exchanging CRYPTO
// bytes through QuicTransport.Recv/Send, delivering traffic secrets
// through Secret, and enforcing RFC 9001's rules (TLS 1.3 only, empty
// legacy_session_id, mandatory ALPN, mandatory quic_transport_parameters
// 0x39, no ChangeCipherSpec, fatal alerts).

import std.task
import std.time

/// The levels QUIC carries TLS messages at (RFC 9001 4.1.4); 0-RTT is not built.
export enum QuicLevel {
    Initial
    Handshake
    Application
}

/// The cipher suite a level's secrets are for (RFC 9001 5.3).
export enum QuicSuite {
    Aes128Gcm
    Aes256Gcm
    Chacha20Poly1305
}

// QuicSuiteOf maps internal Suite to the exported QuicSuite.
QuicSuiteOf(s: Suite): QuicSuite {
    return match s {
        .Aes128Gcm { QuicSuite.Aes128Gcm }
        .Aes256Gcm { QuicSuite.Aes256Gcm }
        .Chacha20Poly1305 { QuicSuite.Chacha20Poly1305 }
    }
}

/// What std.tls asks of the QUIC connection that runs its handshake. An
/// error from any method fails the handshake with it.
export interface QuicTransport {
    /// Waits for the peer's handshake bytes at level; copies at most
    /// buf.Len() into buf, never 0.
    Recv(mut self, io: Io, level: QuicLevel, buf: mut []u8): Error!int
    /// Queues data at level for CRYPTO frames.
    Send(mut self, io: Io, level: QuicLevel, data: []u8): Error!void
    /// The traffic secret of level, one direction (write: this side's),
    /// HashLen(suite) bytes. The callee derives its keys and wipes its copy;
    /// it fails with Protocol when CRYPTO bytes of the old level are left
    /// unread (RFC 9001 4.1.3).
    Secret(mut self, level: QuicLevel, write: bool, suite: QuicSuite, secret: []u8): Error!void
    /// The peer's quic_transport_parameters as sent (RFC 9001 8.2).
    PeerParams(mut self, params: []u8): Error!void
    /// The handshake fails with alert code: close with CRYPTO_ERROR
    /// 0x100 + code (RFC 9001 4.8).
    Alert(mut self, io: Io, code: u8)
}

/// What a finished QUIC handshake chose.
export struct QuicDone {
    alpn: [255]u8
    n: int
}

/// The protocol both sides agreed on (never empty in QUIC). O(1);
/// allocates nothing.
export QuicDone.Alpn(&self): str from self {
    return str.From(self.alpn[0..self.n]).Must()
}

// QuicWire adapts a QuicTransport to the internal handshake Wire.
struct QuicWire[T: QuicTransport] {
    t: mut T
    rlevel: QuicLevel
    wlevel: QuicLevel
    no_alert: bool
}

impl[T: QuicTransport] Wire for QuicWire[T] {
    Feed(mut self, hs: mut Hs, io: Io): Error!void {
        var buf: [4096]u8 = [0; 4096]
        n := self.t.Recv(io, self.rlevel, mut buf[..]) else e {
            self.no_alert = true
            fail e
        }
        if n <= 0 || n > 4096 {
            self.no_alert = true
            fail Protocol
        }
        try hs.Append(buf[0..n])
    }

    Send(mut self, io: Io, data: []u8): Error!void {
        self.t.Send(io, self.wlevel, data) else e {
            self.no_alert = true
            fail e
        }
    }

    Compat(mut self, io: Io): Error!void {
    }

    Read(mut self, suite: Suite, secret: &Secret, app: bool): Error!void {
        self.rlevel = if app { QuicLevel.Application } else { QuicLevel.Handshake }
        self.t.Secret(self.rlevel, false, QuicSuiteOf(suite), secret.Bytes()) else e {
            self.no_alert = true
            fail e
        }
    }

    Write(mut self, suite: Suite, secret: &Secret, app: bool): Error!void {
        self.wlevel = if app { QuicLevel.Application } else { QuicLevel.Handshake }
        self.t.Secret(self.wlevel, true, QuicSuiteOf(suite), secret.Bytes()) else e {
            self.no_alert = true
            fail e
        }
    }

    Tls12(mut self, h: &Heap, a: &Arena, io: Io, c: &Config, hs: mut Hs, m: &Hello, n: int, name: str): Error!Chosen {
        fail Unsupported
    }

    Alert(mut self, io: Io, code: u8) {
        self.t.Alert(io, code)
    }

    Peer(mut self, params: []u8): Error!void {
        self.t.PeerParams(params) else e {
            self.no_alert = true
            fail e
        }
    }

    Quic(&self): bool {
        return true
    }
}

/// Runs the client's TLS 1.3 handshake over t for host, offering alpn
/// (mandatory) and sending params as quic_transport_parameters, under
/// c.handshake. Always TLS 1.3 with an empty legacy_session_id and no
/// ChangeCipherSpec (RFC 9001 4, 8). Errors: the peer's Alert or t's
/// errors, Protocol and Unsupported (after t.Alert for TLS alerts;
/// without t.Alert for connection-level violations such as unconsumed
/// bytes at a level change), x509.Error, Canceled, time.TimedOut past
/// c.handshake, OutOfMemory. O(handshake); allocates the handshake arena
/// on h, freed before returning.
export QuicClient[T: QuicTransport](h: &Heap, io: Io, t: mut T, c: &Config, host: str, alpn: []str, params: []u8): Error!QuicDone {
    if HostOf(host).Len() == 0 || alpn.Len() == 0 || !AlpnFits(alpn) || params.Len() > 65535 {
        fail Unsupported
    }
    var cfg := *c
    cfg.alpn = alpn
    cfg.min_version = Version.Tls13
    var a := try Arena.New(h)
    defer a.Free()
    var w := QuicWire[T](t: mut *t, rlevel: QuicLevel.Initial, wlevel: QuicLevel.Initial, no_alert: false)
    var alert: u8 = 0
    var no_alert := false
    var chosen := Chosen(alpn: [0; 255], n: 0)
    scope s := io.Scope() {
        s.CancelAfter(c.handshake)
        chosen = ClientFlights(h, &a, io, mut w, &cfg, host, ClientSuites[..], params, mut alert, mut no_alert) else e {
            canceled := match e {
                Canceled { true }
                _ { false }
            }
            if task.LimitHit(&s, canceled) {
                fail time.TimedOut
            }
            if !w.no_alert && !no_alert {
                code := AlertFor(&e, alert)
                if code != 0 {
                    w.Alert(io, code)
                }
            }
            fail e
        }
    }
    return QuicDone(alpn: chosen.alpn, n: chosen.n)
}

/// Runs the server's TLS 1.3 handshake over t, requiring a protocol from
/// alpn (mandatory) and sending params as quic_transport_parameters,
/// under c.handshake. Errors: as QuicClient, plus Protocol without
/// t.Alert when the client sent a non-empty legacy_session_id (RFC 9001
/// 8.4). O(handshake); allocates the handshake arena on h, freed before
/// returning.
export QuicServer[T: QuicTransport](h: &Heap, io: Io, t: mut T, c: &Config, alpn: []str, params: []u8): Error!QuicDone {
    if alpn.Len() == 0 || !AlpnFits(alpn) || params.Len() > 65535 {
        fail Unsupported
    }
    var cfg := *c
    cfg.alpn = alpn
    var a := try Arena.New(h)
    defer a.Free()
    var w := QuicWire[T](t: mut *t, rlevel: QuicLevel.Initial, wlevel: QuicLevel.Initial, no_alert: false)
    var alert: u8 = 0
    var no_alert := false
    var chosen := Chosen(alpn: [0; 255], n: 0)
    scope s := io.Scope() {
        s.CancelAfter(c.handshake)
        chosen = ServerFlights(&a, io, mut w, &cfg, params, mut alert, mut no_alert) else e {
            canceled := match e {
                Canceled { true }
                _ { false }
            }
            if task.LimitHit(&s, canceled) {
                fail time.TimedOut
            }
            if !w.no_alert && !no_alert {
                code := AlertFor(&e, alert)
                if code != 0 {
                    w.Alert(io, code)
                }
            }
            fail e
        }
    }
    return QuicDone(alpn: chosen.alpn, n: chosen.n)
}

// QuicPostMessage checks one 1-RTT post-handshake CRYPTO message in buf
// (RFC 9001 4.4, 4.6.1, 6): returns 0 when buf does not yet hold a whole
// message, or the bytes consumed for a dropped NewSessionTicket on a
// client. Fails with Protocol (connection PROTOCOL_VIOLATION) on a
// CertificateRequest (4.4) or a NewSessionTicket whose early_data
// extension is not 0xffffffff (4.6.1), with Alert(AlertDecodeError) on a
// malformed NewSessionTicket, and with Alert(AlertUnexpectedMessage)
// (CRYPTO_ERROR 0x010a) on KeyUpdate (6) or any other message.
QuicPostMessage(server: bool, buf: []u8): Alert|Protocol!int {
    if buf.Len() < 4 {
        return 0
    }
    ty := buf[0]
    len := (int(buf[1]) << 16) | (int(buf[2]) << 8) | int(buf[3])
    if ty == HsNewTicket && !server {
        if len < 13 || len > 131337 {
            fail Alert(code: AlertDecodeError)
        }
        if buf.Len() < 4 + len {
            return 0
        }
        nt := ParseNewTicket(buf[4..4 + len]) else {
            fail Alert(code: AlertDecodeError)
        }
        ed := FindExt(nt.exts, ExtEarlyData) else {
            fail Alert(code: AlertDecodeError)
        }
        if x := ed {
            if x.Len() != 4 || x[0] != 0xff || x[1] != 0xff || x[2] != 0xff || x[3] != 0xff {
                fail Protocol
            }
        }
        return 4 + len
    }
    if ty == HsCertificateRequest {
        fail Protocol
    }
    fail Alert(code: AlertUnexpectedMessage)
}
