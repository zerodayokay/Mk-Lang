// Why: an https or wss client needs a TLS 1.3 handshake written in Mk, with
// fixed bounds on memory, so std.http and std.websocket need no C library.
//
// What: tls.Client, the TLS 1.3 client handshake (ClientFlights), with Hs
// (the handshake message reader), Shares, WriteHello, CheckServerHello,
// CheckEncryptedExtensions and VerifySignature.
//
// How: RFC 8446's full handshake (section 2) with HelloRetryRequest (4.1.4),
// the server's certificate checked by std.x509 and its CertificateVerify
// (4.4.3) and Finished (4.4.4) by std.crypto, and the compatibility mode of D.4
// (a 32-byte legacy_session_id, a dummy ChangeCipherSpec before the second
// flight, a peer's one tolerated). Key shares are X25519 first and P-256 after
// a HelloRetryRequest that asks for it (4.2.7, 4.2.8). A CertificateRequest is
// answered with an empty Certificate: client certificates are not built.
//
// Bounds: a handshake message is at most 64 KiB, a Certificate 256 KiB, a
// chain 16 certificates; the handshake's memory is one arena on the
// caller's heap, freed before Client returns, and no function here
// recurses.

import std.crypto
import std.hash
import std.net
import std.task
import std.time
import std.x509

// MaxMessage is the longest handshake message but a Certificate.
const MaxMessage: int = 65536

// MaxCertificate is the longest Certificate message (Go's
// maxHandshakeCertificateMsg).
const MaxCertificate: int = 262144

// MaxChain is the most certificates a server's chain may hold.
const MaxChain: int = 16

// MaxEarlySkip is how many bytes of records a server skips as early data
// it did not accept (RFC 8446 4.2.10): BoringSSL's kMaxEarlyDataSkipped,
// 16,384. std.tls issues no tickets, so it has no max_early_data_size of
// its own; past the bound a record is read as if no early data had been
// offered, and fails.
const MaxEarlySkip: int = 16384

// Hs is a handshake in progress: the message bytes received and not yet
// consumed (buf[0..have], grown on the arena up to the bounds), the
// transcript, the alert a failure reports, whether the peer's one
// ChangeCipherSpec was seen, whether a Certificate may take MaxCertificate
// (a client after ServerHello; never a server, which takes no Certificate:
// Go's readHandshake bounds it the same way), whether a server still waits
// for the first ClientHello (a ChangeCipherSpec before it is unexpected,
// RFC 8446 5), and how many bytes of early data records a server may
// still skip (4.2.10; 0 when none was offered).
struct Hs {
    a: &Arena
    buf: mut []u8
    have: int
    transcript: Transcript
    alert: u8
    no_alert: bool
    ccs: bool
    big: bool
    hello_due: bool
    early: int
}

// Hs.New is a handshake with an empty 16 KiB buffer on a; server says
// whether it is a server's, which waits for the first ClientHello.
Hs.New(a: &Arena, server: bool): OutOfMemory!Hs from a {
    return Hs(a: a, buf: try a.Slice[u8](16384), have: 0, transcript: Transcript.New(), alert: 0, no_alert: false, ccs: false,
        big: false, hello_due: server, early: 0)
}

// Bad sets the alert of the failure about to be reported and fails with
// Protocol.
Bad(alert: mut u8, code: u8): Protocol!void {
    *alert = code
    fail Protocol
}

// BadNoAlert marks a QUIC protocol violation reported without a TLS alert.
BadNoAlert(no_alert: mut bool): Protocol!void {
    *no_alert = true
    fail Protocol
}

// Hs.Room makes room for n more bytes, growing the buffer on the arena;
// the bounds of Next keep n within one message and one record.
Hs.Room(mut self, n: int): OutOfMemory!void {
    if self.have + n <= self.buf.Len() {
        return
    }
    var size := self.buf.Len() * 2
    if size < self.have + n {
        size = self.have + n
    }
    nb := try self.a.Slice[u8](size)
    (mut nb[0..self.have]).CopyFrom(self.buf[0..self.have])
    self.buf = nb
}

// Hs.Append adds record content to the message bytes.
Hs.Append(mut self, b: []u8): OutOfMemory!void {
    try self.Room(b.Len())
    (mut self.buf[self.have..self.have + b.Len()]).CopyFrom(b)
    self.have += b.Len()
}

// Hs.Drop consumes the first n bytes, the message just handled.
Hs.Drop(mut self, n: int) {
    rest := self.have - n
    for i in 0..rest {
        self.buf[i] = self.buf[n + i]
    }
    self.have = rest
}

// Hs.Next reads from w until buf starts with one whole handshake message
// and returns its type and length with its header; the message is
// buf[0..n].
Hs.Next[W: Wire](mut self, w: mut W, io: Io): Error!(ty: u8, n: int) {
    return try self.NextOf(w, io, -1)
}

// Hs.NextOf is Next for a message of type want (any when want is -1): a
// header of another type is refused as soon as its 4 bytes are in, so
// no byte of a message the handshake cannot take is buffered past them.
Hs.NextOf[W: Wire](mut self, w: mut W, io: Io, want: int): Error!(ty: u8, n: int) {
    for {
        if self.have >= 4 {
            len := (int(self.buf[1]) << 16) | (int(self.buf[2]) << 8) | int(self.buf[3])
            max := if self.buf[0] == HsCertificate && self.big { MaxCertificate } else { MaxMessage }
            if len > max {
                try Bad(mut self.alert, AlertDecodeError)
            }
            if want >= 0 && int(self.buf[0]) != want {
                try Bad(mut self.alert, AlertUnexpectedMessage)
            }
            if self.have >= 4 + len {
                return (ty: self.buf[0], n: 4 + len)
            }
        }
        try w.Feed(mut *self, io)
    }
}

// Hs.Expect reads the next message, which must be of type ty (refused at
// its header otherwise); returns its length.
Hs.Expect[W: Wire](mut self, w: mut W, io: Io, ty: u8): Error!int {
    m := try self.NextOf(w, io, int(ty))
    return m.n
}

// Shares are the client's ephemeral key shares: an X25519 secret and
// its public value, and a P-256 secret made when a HelloRetryRequest asks
// for P-256.
struct Shares {
    x25519: [32]u8
    x25519_pub: [32]u8
    p256: ?crypto.EcSecret
    p256_pub: [65]u8
}

// Shares.Wipe clears both secrets.
Shares.Wipe(mut self) {
    crypto.Wipe(mut self.x25519[..])
    if s := mut self.p256 {
        s.Wipe()
    }
}

// X25519Base is the X25519 base point u = 9 (RFC 7748 4.1).
const X25519Base: [32]u8 = [9, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0]

// SharedSecret is the (EC)DHE shared secret of the client's share for
// group with the server's key (RFC 8446 7.4): X25519's 32 bytes, refused
// when all zero (7.4.2), or P-256's x coordinate, the key a valid
// uncompressed point (4.2.8.2). out gets 32 bytes. A bad key is
// illegal_parameter.
SharedSecret(alert: mut u8, s: &Shares, group: u16, key: []u8, out: mut []u8): Protocol!void {
    if group == GroupX25519 {
        if key.Len() != 32 {
            try Bad(alert, AlertIllegalParameter)
        }
        var peer: [32]u8 = [0; 32]
        (mut peer[..]).CopyFrom(key)
        var r := crypto.X25519(&s.x25519, &peer)
        zero: [32]u8 = [0; 32]
        if crypto.Equal(r[..], zero[..]) {
            crypto.Wipe(mut r[..])
            try Bad(alert, AlertIllegalParameter)
        }
        (mut out[0..32]).CopyFrom(r[..])
        crypto.Wipe(mut r[..])
        return
    }
    secret := s.p256 else {
        try Bad(alert, AlertIllegalParameter)
        return
    }
    crypto.EcdhShared(crypto.Curve.P256, &secret, key, mut out[0..32]) else {
        try Bad(alert, AlertIllegalParameter)
    }
}

// IsIpLiteral reports whether host is an IPv4 or IPv6 literal, which
// server_name does not carry (RFC 6066 3).
IsIpLiteral(host: str): bool {
    b := host.AsBytes()
    var dots := 0
    for x in b {
        if x == b':' {
            return true
        }
        if x == b'.' {
            dots += 1
        } else if x < b'0' || x > b'9' {
            return false
        }
    }
    return dots == 3
}

// HostOf is host without IPv6 brackets or a final dot, as it is checked
// against the certificate and sent in server_name.
HostOf(host: str): str from host {
    var h := host
    if h.Len() >= 2 && h.StartsWith("[") && h.EndsWith("]") {
        h = h[1..h.Len() - 1]
    }
    if h.Len() > 1 && h.EndsWith(".") {
        h = h[0..h.Len() - 1]
    }
    return h
}

// The schemes the client offers in signature_algorithms (RFC 8446
// 4.2.3), in preference order: the CertificateVerify schemes std.crypto
// verifies, then PKCS#1 v1.5 for certificate signatures only.
const ClientSchemes: [9]u16 = [SchemeEcdsaP256, SchemeEd25519, SchemePssSha256, SchemeEcdsaP384, SchemePssSha384,
    SchemePssSha512, SchemePkcs1Sha256, SchemePkcs1Sha384, SchemePkcs1Sha512]

// ClientSuites are the cipher suites the client offers.
const ClientSuites: [3]u16 = [0x1301, 0x1303, 0x1302]

// Hello is what a ClientHello says: the random, the session id, the host
// for server_name (empty for none), the protocols, which share to send
// and the cookie to echo.
struct Hello {
    suites: []u16
    random: [32]u8
    sid: [32]u8
    sni: str
    alpn: []str
    group: u16
    cookie: []u8
}

// WriteHello writes a ClientHello (RFC 8446 4.1.2) with the client's
// extensions (4.2): server_name, supported_groups, signature_algorithms,
// supported_versions, key_share with one share, ALPN when protocols are
// offered, and the cookie after a HelloRetryRequest.
WriteHello(w: mut Out, m: &Hello, s: &Shares) {
    WriteHelloWith(w, m, s, false, b"")
}

// WriteHelloWith is WriteHello, in QUIC mode when quic is true (RFC 9001
// 8.2, 8.4): empty legacy_session_id and quic_transport_parameters (0x39).
WriteHelloWith(w: mut Out, m: &Hello, s: &Shares, quic: bool, params: []u8) {
    at := OpenMessage(w, HsClientHello)
    w.U16(WireTls12)
    w.Bytes(m.random[..])
    if quic {
        w.Vec8(b"")
    } else {
        w.Vec8(m.sid[..])
    }
    suites := w.Open(2)
    for x in m.suites {
        w.U16(x)
    }
    w.Close(suites, 2)
    w.U8(1)
    w.U8(0)
    exts := w.Open(2)
    if m.sni.Len() > 0 {
        w.U16(ExtServerName)
        e := w.Open(2)
        WriteServerName(w, m.sni.AsBytes())
        w.Close(e, 2)
    }
    w.U16(ExtSupportedGroups)
    e1 := w.Open(2)
    l1 := w.Open(2)
    w.U16(GroupX25519)
    w.U16(GroupP256)
    w.Close(l1, 2)
    w.Close(e1, 2)
    w.U16(ExtSignatureAlgorithms)
    e2 := w.Open(2)
    l2 := w.Open(2)
    for x in ClientSchemes {
        w.U16(x)
    }
    w.Close(l2, 2)
    w.Close(e2, 2)
    w.U16(ExtSupportedVersions)
    e3 := w.Open(2)
    l3 := w.Open(1)
    w.U16(WireTls13)
    if !quic && Offers12(m) {
        w.U16(WireTls12)
    }
    w.Close(l3, 1)
    w.Close(e3, 2)
    w.U16(ExtKeyShare)
    e4 := w.Open(2)
    l4 := w.Open(2)
    if m.group == GroupX25519 {
        WriteKeyShare(w, GroupX25519, s.x25519_pub[..])
    } else {
        WriteKeyShare(w, GroupP256, s.p256_pub[..])
    }
    w.Close(l4, 2)
    w.Close(e4, 2)
    if m.alpn.Len() > 0 {
        w.U16(ExtAlpn)
        e5 := w.Open(2)
        WriteAlpn(w, m.alpn)
        w.Close(e5, 2)
    }
    if m.cookie.Len() > 0 {
        w.U16(ExtCookie)
        e6 := w.Open(2)
        w.Vec16(m.cookie)
        w.Close(e6, 2)
    }
    if quic {
        WriteExt(w, ExtQuicParams, params)
    } else if Offers12(m) {
        WriteHello12Exts(w)
    }
    w.Close(exts, 2)
    w.Close(at, 3)
}

// HelloSize bounds a ClientHello's length for its buffer.
HelloSize(m: &Hello): int {
    var n := 512 + 2 * m.suites.Len() + m.sni.Len() + m.cookie.Len()
    for p in m.alpn {
        n += 1 + p.Len()
    }
    return n
}

// ServerSays is what the ServerHello's checks keep: the suite and the
// server's share.
struct ServerSays {
    suite: Suite
    group: u16
    key: []u8
}

// CheckServerHello checks a ServerHello or HelloRetryRequest against the
// ClientHello it answers (RFC 8446 4.1.3, 4.1.4, 4.2): TLS 1.3 chosen
// through supported_versions, the session id echoed, no compression, an
// offered suite, and only the extensions the message may carry; returns
// the suite and the key_share data (or the HRR's selected group, key
// empty), and the cookie when an HRR has one.
CheckServerHello(alert: mut u8, sh: &ServerHello, m: &Hello, retry: bool): Error!(says: ServerSays, cookie: []u8) from sh {
    return try CheckServerHelloWith(alert, sh, m, retry, false)
}

// CheckServerHelloWith is CheckServerHello, expecting an empty
// legacy_session_id_echo when quic is true (RFC 9001 8.4).
CheckServerHelloWith(alert: mut u8, sh: &ServerHello, m: &Hello, retry: bool, quic: bool): Error!(says: ServerSays, cookie: []u8) from sh {
    var it := ExtIter.New(sh.exts)
    var version: u16 = 0
    var share: ?[]u8 = none
    var cookie: []u8 = sh.exts[0..0]
    for x := try it.Next() {
        if x.ty == ExtSupportedVersions {
            version = try ParseVersionServer(x.data)
        } else if x.ty == ExtKeyShare {
            share = some(x.data)
        } else if x.ty == ExtCookie && retry {
            cookie = try ParseCookie(x.data)
        } else {
            try Bad(alert, AlertUnsupportedExtension)
        }
    }
    if version == 0 {
        // No supported_versions: the server chose TLS 1.2 or older.
        *alert = AlertProtocolVersion
        fail Unsupported
    }
    if version != WireTls13 || sh.version != WireTls12 || sh.compression != 0 {
        try Bad(alert, AlertIllegalParameter)
    }
    if quic {
        if sh.legacy_id.Len() != 0 {
            try Bad(alert, AlertIllegalParameter)
        }
    } else if !crypto.Equal(sh.legacy_id, m.sid[..]) {
        try Bad(alert, AlertIllegalParameter)
    }
    var offered := false
    for x in m.suites {
        if x == sh.suite {
            offered = true
        }
    }
    suite := SuiteOf(sh.suite) else {
        try Bad(alert, AlertIllegalParameter)
        fail Protocol
    }
    if !offered {
        try Bad(alert, AlertIllegalParameter)
    }
    data := share else {
        if retry && cookie.Len() > 0 {
            return (says: ServerSays(suite: suite, group: m.group, key: cookie[0..0]), cookie: cookie)
        }
        try Bad(alert, AlertMissingExtension)
        fail Protocol
    }
    if retry {
        g := try ParseKeyShareRetry(data)
        if (g != GroupX25519 && g != GroupP256) || g == m.group {
            try Bad(alert, AlertIllegalParameter)
        }
        return (says: ServerSays(suite: suite, group: g, key: data[0..0]), cookie: cookie)
    }
    e := try ParseKeyShareServer(data)
    if e.group != m.group {
        try Bad(alert, AlertIllegalParameter)
    }
    return (says: ServerSays(suite: suite, group: e.group, key: e.key), cookie: cookie)
}

// CheckEncryptedExtensions reads EncryptedExtensions (RFC 8446 4.3.1):
// server_name's empty acknowledgement when one was sent, supported_groups
// (informative), and ALPN, one protocol the client offered (RFC 7301
// 3.1); any other extension was not asked for (unsupported_extension, 4.2).
// Returns the chosen protocol, empty for none.
CheckEncryptedExtensions(alert: mut u8, exts: []u8, m: &Hello): Protocol![]u8 from exts {
    r := try CheckEncryptedExtensionsWith(alert, exts, m, false)
    return r.alpn
}

// CheckEncryptedExtensionsWith is CheckEncryptedExtensions, in QUIC mode
// when quic is true: requires quic_transport_parameters (0x39, RFC 9001
// 8.2) and ALPN (0x0178, RFC 9001 8.1), refusing 0x39 when !quic (8.2).
CheckEncryptedExtensionsWith(alert: mut u8, exts: []u8, m: &Hello, quic: bool): Protocol!(alpn: []u8, params: ?[]u8) from exts {
    var it := ExtIter.New(exts)
    var chosen: []u8 = exts[0..0]
    var params: ?[]u8 = none
    for x := try it.Next() {
        if x.ty == ExtServerName && m.sni.Len() > 0 && x.data.Len() == 0 {
            continue
        }
        if x.ty == ExtSupportedGroups {
            _ = try ParseGroups(x.data)
            continue
        }
        if x.ty == ExtQuicParams && quic {
            params = some(x.data)
            continue
        }
        if x.ty == ExtAlpn && m.alpn.Len() > 0 {
            list := try ParseAlpn(x.data)
            var names := AlpnNames(c: Cursor.New(list))
            p := names.Next() else {
                try Bad(alert, AlertDecodeError)
                fail Protocol
            }
            if names.Next().IsSome() {
                try Bad(alert, AlertIllegalParameter)
            }
            var offered := false
            for o in m.alpn {
                if crypto.Equal(o.AsBytes(), p) {
                    offered = true
                }
            }
            if !offered {
                try Bad(alert, if quic { AlertNoApplicationProtocol } else { AlertIllegalParameter })
            }
            chosen = p
            continue
        }
        try Bad(alert, AlertUnsupportedExtension)
    }
    if quic {
        if params.IsNone() {
            try Bad(alert, AlertMissingExtension)
        }
        if chosen.Len() == 0 {
            try Bad(alert, AlertNoApplicationProtocol)
        }
    }
    return (alpn: chosen, params: params)
}

// SignedContent writes what a CertificateVerify signs (RFC 8446 4.4.3):
// 64 spaces, the context string, a zero byte and the transcript hash;
// returns its length.
SignedContent(context: str, hash: []u8, out: mut []u8): int {
    for i in 0..64 {
        out[i] = 0x20
    }
    (mut out[64..64 + context.Len()]).CopyFrom(context.AsBytes())
    at := 64 + context.Len()
    out[at] = 0
    (mut out[at + 1..at + 1 + hash.Len()]).CopyFrom(hash)
    return at + 1 + hash.Len()
}

// Digest writes the hash of msg under the scheme's hash h to out and
// returns its length.
Digest(h: crypto.HashKind, msg: []u8, out: mut []u8): int {
    return match h {
        .Sha256 {
            d := hash.Sha256(msg)
            (mut out[0..32]).CopyFrom(d[..])
            32
        }
        .Sha384 {
            d := hash.Sha384(msg)
            (mut out[0..48]).CopyFrom(d[..])
            48
        }
        .Sha512 {
            d := hash.Sha512(msg)
            (mut out[0..64]).CopyFrom(d[..])
            64
        }
    }
}

// VerifySignature checks a CertificateVerify's signature over content
// with the leaf's key (RFC 8446 4.4.3): the scheme must be one the client
// offered and fit the key, its curve and its hash (ECDSA P-256 with
// SHA-256 and P-384 with SHA-384, RSA-PSS with an rsaEncryption key,
// Ed25519); a mismatch is illegal_parameter, a signature that does not
// verify decrypt_error.
VerifySignature(alert: mut u8, key: &crypto.PublicKey, scheme: u16, sig: []u8, content: []u8): Protocol!void {
    var d: [64]u8 = [0; 64]
    ok := match key {
        .Ec(curve, point) {
            if scheme == SchemeEcdsaP256 && *curve == crypto.Curve.P256 {
                n := Digest(crypto.HashKind.Sha256, content, mut d[..])
                crypto.EcdsaVerify(*point, *curve, d[0..n], crypto.SigFormat.Der, sig)
            } else if scheme == SchemeEcdsaP384 && *curve == crypto.Curve.P384 {
                n := Digest(crypto.HashKind.Sha384, content, mut d[..])
                crypto.EcdsaVerify(*point, *curve, d[0..n], crypto.SigFormat.Der, sig)
            } else {
                try Bad(alert, AlertIllegalParameter)
                false
            }
        }
        .Rsa(key: rsa) {
            if scheme != SchemePssSha256 && scheme != SchemePssSha384 && scheme != SchemePssSha512 {
                try Bad(alert, AlertIllegalParameter)
            }
            h := if scheme == SchemePssSha256 {
                crypto.HashKind.Sha256
            } else if scheme == SchemePssSha384 {
                crypto.HashKind.Sha384
            } else {
                crypto.HashKind.Sha512
            }
            n := Digest(h, content, mut d[..])
            crypto.RsaVerifyPss(rsa, h, d[0..n], sig)
        }
        .Ed25519(key: pk) {
            if scheme != SchemeEd25519 {
                try Bad(alert, AlertIllegalParameter)
            }
            if sig.Len() != 64 {
                try Bad(alert, AlertDecryptError)
            }
            var s: [64]u8 = [0; 64]
            (mut s[..]).CopyFrom(sig)
            crypto.Ed25519.Verify(pk, content, &s)
        }
    }
    if !ok {
        try Bad(alert, AlertDecryptError)
    }
}

// Ccs is the dummy ChangeCipherSpec record's content (RFC 8446 D.4).
const Ccs: [1]u8 = [1]

// Chosen is the protocol the handshake chose, copied out of its buffers.
struct Chosen {
    alpn: [255]u8
    n: int
}

// ClientFlights is the client's side of RFC 8446 2's handshake over the
// Wire w: ClientHello, a HelloRetryRequest answered once, ServerHello,
// then under the handshake keys EncryptedExtensions, an optional
// CertificateRequest, Certificate, CertificateVerify and Finished, then
// the client's Finished, and both directions on the application keys.
// alert gets the alert a failure reports.
ClientFlights[W: Wire](h: &Heap, a: &Arena, io: Io, w: mut W, c: &Config, host: str, suites: []u16, params: []u8, alert: mut u8, no_alert: mut bool): Error!Chosen {
    var hs := try Hs.New(a, false)
    onerr {
        *alert = hs.alert
        *no_alert = hs.no_alert
    }
    quic := w.Quic()
    var shares := Shares(x25519: [0; 32], x25519_pub: [0; 32], p256: none, p256_pub: [0; 65])
    defer shares.Wipe()
    try crypto.Random(io, mut shares.x25519[..])
    shares.x25519_pub = crypto.X25519(&shares.x25519, &X25519Base)
    var m := Hello(suites: suites, random: [0; 32], sid: [0; 32], sni: "", alpn: c.alpn, group: GroupX25519, cookie: b"")
    if !quic && c.min_version == Version.Tls12 {
        // The TLS 1.2 client is on (client12.mk).
        m.suites = try WithSuites12(a, suites)
    }
    try crypto.Random(io, mut m.random[..])
    if !quic {
        try crypto.Random(io, mut m.sid[..])
    }
    name := HostOf(host)
    if !IsIpLiteral(name) {
        m.sni = name
    }
    extra := if quic { 4 + params.Len() } else { 0 }
    var out := try a.Slice[u8](HelloSize(&m) + extra)
    var wo := Out.New(mut out[..])
    WriteHelloWith(mut wo, &m, &shares, quic, params)
    if wo.full {
        // The extensions past their 2-byte length: an ALPN list that fits
        // its own (ClientWith checked it) but not beside the other
        // extensions, or a host of tens of KiB.
        fail Unsupported
    }
    hs.transcript.Add(wo.b[0..wo.at])
    try w.Send(io, wo.b[0..wo.at])
    // ServerHello, after at most one HelloRetryRequest (RFC 8446 4.1.4).
    var retried := false
    var retry_suite := Suite.Aes128Gcm
    var suite := Suite.Aes128Gcm
    var group: u16 = 0
    var key: [65]u8 = [0; 65]
    var key_len := 0
    for {
        n := try hs.Expect(w, io, HsServerHello)
        sh := try ParseServerHello(hs.buf[4..n])
        retry := sh.IsRetry()
        if !quic && !retry && !retried && Offers12(&m) && try Is12(&sh) {
            // The server chose TLS 1.2, which the client offered.
            return try w.Tls12(h, a, io, c, mut hs, &m, n, name)
        }
        if retry && retried {
            try Bad(mut hs.alert, AlertUnexpectedMessage)
        }
        r := try CheckServerHelloWith(mut hs.alert, &sh, &m, retry, quic)
        if retried && r.says.suite != retry_suite {
            try Bad(mut hs.alert, AlertIllegalParameter)
        }
        kind := SuiteHash(r.says.suite)
        hs.transcript.Choose(kind)
        if !retry {
            suite = r.says.suite
            group = r.says.group
            key_len = r.says.key.Len()
            if key_len > 65 {
                try Bad(mut hs.alert, AlertIllegalParameter)
            }
            (mut key[0..key_len]).CopyFrom(r.says.key)
            hs.transcript.Add(hs.buf[0..n])
            hs.Drop(n)
            break
        }
        hs.transcript.Restart(kind)
        hs.transcript.Add(hs.buf[0..n])
        if r.cookie.Len() > 0 {
            cookie := try a.Slice[u8](r.cookie.Len())
            (mut cookie[..]).CopyFrom(r.cookie)
            m.cookie = cookie[..]
        }
        retry_suite = r.says.suite
        m.group = r.says.group
        retried = true
        hs.Drop(n)
        if hs.have != 0 {
            try Bad(mut hs.alert, AlertUnexpectedMessage)
        }
        if m.group == GroupP256 {
            sec := crypto.EcSecret.Generate(io, crypto.Curve.P256) else e {
                match e {
                    Canceled {
                        fail Canceled
                    }
                    _ {
                        try Bad(mut hs.alert, AlertInternalError)
                        fail Protocol
                    }
                }
            }
            // A P-256 secret's public point fits 65 bytes: Public cannot
            // fail.
            _ = sec.Public(mut shares.p256_pub[..]).Must()
            shares.p256 = some(sec)
        }
        // Compatibility mode (RFC 8446 D.4): the dummy ChangeCipherSpec
        // goes before the second ClientHello.
        try w.Compat(io)
        out2 := try a.Slice[u8](HelloSize(&m) + extra)
        var w2 := Out.New(out2)
        WriteHelloWith(mut w2, &m, &shares, quic, params)
        if w2.full {
            fail Unsupported
        }
        hs.transcript.Add(w2.b[0..w2.at])
        try w.Send(io, w2.b[0..w2.at])
    }
    if hs.have != 0 {
        // A message after ServerHello in its plaintext record or Initial
        // CRYPTO stream would span the key change (RFC 8446 5.1, RFC
        // 9001 4.1.3).
        if quic {
            try BadNoAlert(mut hs.no_alert)
        } else {
            try Bad(mut hs.alert, AlertUnexpectedMessage)
        }
    }
    // The version is TLS 1.3: the server's Certificate may take its bound.
    hs.big = true
    kind := SuiteHash(suite)
    hl := HashLen(kind)
    var shared: [32]u8 = [0; 32]
    try SharedSecret(mut hs.alert, &shares, group, key[0..key_len], mut shared[..])
    var sched := Schedule.New(kind)
    defer sched.Wipe()
    sched.DeriveHandshake(shared[..])
    crypto.Wipe(mut shared[..])
    var th: [48]u8 = [0; 48]
    hs.transcript.Sum(kind, mut th[..])
    var c_hs := DeriveSecret(&sched.handshake, "c hs traffic", th[0..hl])
    defer c_hs.Wipe()
    var s_hs := DeriveSecret(&sched.handshake, "s hs traffic", th[0..hl])
    defer s_hs.Wipe()
    try w.Read(suite, &s_hs, false)
    // From here the server reads the client's records, alerts included,
    // under the client's handshake keys (RFC 8446 2); the compatibility
    // mode's dummy ChangeCipherSpec (D.4) goes first, in plaintext, unless
    // one went before a second ClientHello.
    if !retried {
        try w.Compat(io)
    }
    try w.Write(suite, &c_hs, false)
    // EncryptedExtensions (RFC 8446 4.3.1).
    var chosen := Chosen(alpn: [0; 255], n: 0)
    n1 := try hs.Expect(w, io, HsEncryptedExtensions)
    exts := try ParseEncryptedExtensions(hs.buf[4..n1])
    ee := try CheckEncryptedExtensionsWith(mut hs.alert, exts, &m, quic)
    if pp := ee.params {
        try w.Peer(pp)
    }
    (mut chosen.alpn[0..ee.alpn.Len()]).CopyFrom(ee.alpn)
    chosen.n = ee.alpn.Len()
    hs.transcript.Add(hs.buf[0..n1])
    hs.Drop(n1)
    // An optional CertificateRequest (4.3.2), then Certificate (4.4.2).
    var requested := false
    var context: [255]u8 = [0; 255]
    var context_len := 0
    var mc := try hs.Next(w, io)
    if mc.ty == HsCertificateRequest {
        cr := try ParseCertificateRequest(hs.buf[4..mc.n])
        if (try FindExt(cr.exts, ExtSignatureAlgorithms)).IsNone() {
            try Bad(mut hs.alert, AlertMissingExtension)
        }
        context_len = cr.context.Len()
        (mut context[0..context_len]).CopyFrom(cr.context)
        requested = true
        hs.transcript.Add(hs.buf[0..mc.n])
        hs.Drop(mc.n)
        mc = try hs.Next(w, io)
    }
    if mc.ty != HsCertificate {
        try Bad(mut hs.alert, AlertUnexpectedMessage)
    }
    cert := try ParseCertificate(hs.buf[4..mc.n])
    if cert.context.Len() != 0 {
        try Bad(mut hs.alert, AlertIllegalParameter)
    }
    var chain := List[x509.Certificate].New(a)
    var entries := CertEntries.New(cert.list)
    for e := try entries.Next() {
        if e.exts.Len() != 0 {
            // No status_request or signed_certificate_timestamp was sent.
            try Bad(mut hs.alert, AlertUnsupportedExtension)
        }
        if chain.Len() == MaxChain {
            try Bad(mut hs.alert, AlertBadCertificate)
        }
        der := try a.Slice[u8](e.cert.Len())
        (mut der[..]).CopyFrom(e.cert)
        try chain.Push(try x509.Parse(a, der[..]))
    }
    if chain.Len() == 0 {
        try Bad(mut hs.alert, AlertDecodeError)
    }
    hs.transcript.Add(hs.buf[0..mc.n])
    hs.Drop(mc.n)
    certs := chain.Done()
    var pool := x509.Pool.New(certs[0..0])
    if r := c.roots {
        pool = *r
    } else {
        pool = try SystemPoolOf(h, a, io)
    }
    try x509.Verify(a, io, certs[..], &pool, time.Wall(io), name)
    // CertificateVerify (4.4.3) over the transcript through Certificate.
    hs.transcript.Sum(kind, mut th[..])
    n2 := try hs.Expect(w, io, HsCertificateVerify)
    cv := try ParseCertificateVerify(hs.buf[4..n2])
    leaf_key := certs[0].PublicKey() else {
        try Bad(mut hs.alert, AlertUnsupportedCertificate)
        fail Protocol
    }
    var content: [160]u8 = [0; 160]
    cl := SignedContent("TLS 1.3, server CertificateVerify", th[0..hl], mut content[..])
    try VerifySignature(mut hs.alert, &leaf_key, cv.scheme, cv.signature, content[0..cl])
    hs.transcript.Add(hs.buf[0..n2])
    hs.Drop(n2)
    // The server's Finished (4.4.4).
    hs.transcript.Sum(kind, mut th[..])
    n3 := try hs.Expect(w, io, HsFinished)
    fin := try ParseFinished(hs.buf[4..n3], hl)
    var want: [48]u8 = [0; 48]
    FinishedValue(&s_hs, th[0..hl], mut want[..])
    if !crypto.Equal(fin, want[0..hl]) {
        try Bad(mut hs.alert, AlertDecryptError)
    }
    hs.transcript.Add(hs.buf[0..n3])
    hs.Drop(n3)
    if hs.have != 0 {
        if quic {
            try BadNoAlert(mut hs.no_alert)
        } else {
            try Bad(mut hs.alert, AlertUnexpectedMessage)
        }
    }
    // The application secrets over the transcript through the server's
    // Finished (7.1); the read side switches now.
    sched.DeriveMaster()
    hs.transcript.Sum(kind, mut th[..])
    var c_ap := DeriveSecret(&sched.master, "c ap traffic", th[0..hl])
    defer c_ap.Wipe()
    var s_ap := DeriveSecret(&sched.master, "s ap traffic", th[0..hl])
    defer s_ap.Wipe()
    try w.Read(suite, &s_ap, true)
    s_ap.Wipe()
    // The client's second flight under its handshake keys: an empty
    // Certificate when one was requested, and Finished.
    var flight: [400]u8 = [0; 400]
    var fw := Out.New(mut flight[..])
    if requested {
        at := WriteCertificateHead(mut fw, context[0..context_len])
        CloseCertificate(mut fw, at)
        hs.transcript.Add(fw.b[0..fw.at])
    }
    hs.transcript.Sum(kind, mut th[..])
    var mine: [48]u8 = [0; 48]
    FinishedValue(&c_hs, th[0..hl], mut mine[..])
    WriteFinished(mut fw, mine[0..hl])
    try w.Send(io, fw.b[0..fw.at])
    try w.Write(suite, &c_ap, true)
    c_ap.Wipe()
    return chosen
}

/// Runs the client side of a TLS handshake over conn with the server
/// named host, and returns the TLS connection. On success the Conn holds
/// conn; on failure conn stays the caller's, mid-handshake, to Close or
/// Abandon. Two scalar multiplications and the chain's checks; allocates
/// the connection's state on h (about 66 KiB, freed by Close or Abandon)
/// and the handshake's buffers on an arena of h, freed before it returns.
///
/// It speaks TLS 1.3 (RFC 8446), or TLS 1.2 (RFC 5246, ECDHE and AEAD
/// suites) when c.min_version is Tls12 and the server chooses it; a TLS
/// 1.2 server without the extended master secret is refused with
/// Unsupported. A TLS 1.2 connection's Read fails with Protocol on a
/// HelloRequest, since there is no renegotiation. The handshake uses c's
/// roots, protocols and limits, and x509.Verify checks the server's chain
/// for host at the wall clock. A server's request for a client certificate
/// is answered with none.
///
/// With roots none, the chain is checked against the system's pool,
/// x509.SystemPool: on Linux the whole store is read and parsed by the
/// process's first such handshake that finds a certificate, and kept on h
/// until exit. Under a simulated or taped Io, SystemRoots' pool is loaded
/// through that Io at each handshake.
///
/// Errors: the peer's Alert; Protocol and Unsupported, after the matching
/// alert was sent; Unsupported also, before any I/O, for an empty host,
/// which names nothing to check, and for a c.alpn that cannot be sent (a
/// name that is empty or over 255 bytes, or names of more than 65,535
/// bytes in all, RFC 7301 3.1); x509.Error's members for the chain; the
/// connection's net.Error, and time.TimedOut past c.handshake;
/// OutOfMemory. A cancel leaves conn with the caller mid-handshake, with
/// an unknown part of the handshake sent: close it.
export Client(h: &Heap, io: Io, conn: net.Conn, c: &Config, host: str): Error!Conn from h, conn {
    return try ClientWith(h, io, conn, c, host, ClientSuites[..])
}

// ClientWith is Client offering the cipher suites suites, in order.
ClientWith(h: &Heap, io: Io, conn: net.Conn, c: &Config, host: str, suites: []u16): Error!Conn from h, conn {
    if HostOf(host).Len() == 0 {
        // x509.Verify checks no name for an empty host: without one any
        // certificate under the roots would do.
        fail Unsupported
    }
    if !AlpnFits(c.alpn) {
        // The configuration's protocols cannot be sent: refused before
        // any I/O, so the peer sees nothing (Go's makeClientHello too).
        fail Unsupported
    }
    var b := try Box[Halves].New(h.Alloc(), Halves.New(c.close))
    var chosen := Chosen(alpn: [0; 255], n: 0)
    scope s := io.Scope() {
        s.CancelAfter(c.handshake)
        chosen = ClientHandshake(h, io, &conn, c, host, suites, b.Ref()) else e {
            FreeHalves(b, h)
            canceled := match e {
                Canceled { true }
                _ { false }
            }
            if task.LimitHit(&s, canceled) {
                fail time.TimedOut
            }
            fail e
        }
    }
    b.Mut().alpn = chosen.alpn
    b.Mut().alpn_len = chosen.n
    return Conn(conn: conn, heap: h, box: b)
}

// ClientHandshake runs ClientFlights on hv's halves, both locked for its
// whole length, on an arena of h freed when it returns; a failure sends
// the alert it chose, best effort.
ClientHandshake(h: &Heap, io: Io, conn: &net.Conn, c: &Config, host: str, suites: []u16, hv: &Halves): Error!Chosen {
    var a := try Arena.New(h)
    defer a.Free()
    var gr := try hv.rd.Lock(io)
    var gw := hv.wr.Lock(io) else e {
        gr.Unlock()
        fail e
    }
    var w := RecordWire(conn: conn, rd: gr.Mut(), wr: gw.Mut())
    var alert: u8 = 0
    var no_alert := false
    chosen := ClientFlights(h, &a, io, mut w, c, host, suites, b"", mut alert, mut no_alert) else e {
        code := AlertFor(&e, alert)
        if code != 0 {
            w.Alert(io, code)
        }
        gw.Unlock()
        gr.Unlock()
        fail e
    }
    gw.Unlock()
    gr.Unlock()
    return chosen
}
