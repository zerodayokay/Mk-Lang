// Why: an https server in Mk needs a TLS 1.3 server handshake with fixed
// bounds on memory.
//
// What: tls.Server and tls.Acceptor, over ServerHandshake and
// ServerFlights, with Offer and ReadOffer (what the server takes of a
// ClientHello), ServerShare and Sign.
//
// How: the TLS 1.3 server handshake of RFC 8446 section 2, with the
// HelloRetryRequest of 4.1.4 when the client sent no share of a group the
// server takes; key shares X25519 (preferred) and P-256; the three AEAD
// suites in the client's order; ALPN in the server's order (RFC 7301 3.2);
// SNI read and acknowledged; a server key of P-256 (ecdsa_secp256r1_sha256)
// or Ed25519, chosen as the first identity whose scheme the client lists.
//
// No NewSessionTicket is sent (no resumption), no certificate is requested,
// and early data is never accepted: a client resuming a ticket of another
// server under the same name may still send some, which the server skips
// (RFC 8446 4.2.10, up to MaxEarlySkip bytes; Go's server refuses such a
// ClientHello with unsupported_extension instead). A ClientHello whose
// pre_shared_key is not its last extension is illegal_parameter (4.2.11),
// a ChangeCipherSpec before the first ClientHello unexpected_message (5),
// and a client's NewSessionTicket after the handshake unexpected_message
// (4.6.1).
//
// Bounds: the ClientHello is at most 64 KiB, read through the client's Hs;
// the server's flight is built on one arena of the caller's heap, freed
// before Server returns; nothing here recurses.

import std.core
import std.crypto
import std.net
import std.task
import std.time

// Offer is what a ClientHello offers that the server takes: the suite,
// the group and the client's share for it (none: a HelloRetryRequest
// asks for the group), the identity and scheme the server signs with,
// the protocol chosen, whether server_name was sent, the legacy session
// id to echo, and whether early data was offered.
struct Offer {
    suite: Suite
    group: u16
    key: [65]u8
    key_len: int
    retry: bool
    ident: int
    scheme: u16
    alpn: [255]u8
    alpn_len: int
    sni: bool
    sid: [32]u8
    sid_len: int
    early: bool
}

// SchemeOf is the scheme the server signs with for key, 0 when std.tls
// signs with no scheme for it: P-256 ECDSA or Ed25519.
SchemeOf(key: &crypto.PrivateKey): u16 {
    return match key {
        .Ec(secret: _) { SchemeEcdsaP256 }
        .Ed25519(seed: _) { SchemeEd25519 }
    }
}

// ReadOffer checks a ClientHello (RFC 8446 4.1.2) and picks what the
// server answers with: TLS 1.3 in supported_versions (else
// protocol_version), the null compression alone, the first suite of the
// client's std.tls builds, the first identity whose scheme the client's
// signature_algorithms lists, the client's X25519 share, else its P-256
// share, else a group of supported_groups to ask for (4.1.4), and the
// first of c.alpn the client offers (none in common is
// no_application_protocol). pre_shared_key must be the last extension
// (illegal_parameter, RFC 8446 4.2.11, though PSKs are not taken), and
// early_data is noted for the server to skip (4.2.10). retry is the group
// a HelloRetryRequest asked for, 0 for the first ClientHello; the second
// must carry a share of it and no early_data (4.1.2).
ReadOffer(alert: mut u8, ch: &ClientHello, c: &Config, retry: u16): Error!Offer {
    var o := Offer(suite: Suite.Aes128Gcm, group: 0, key: [0; 65], key_len: 0, retry: false, ident: -1, scheme: 0, alpn: [0; 255],
        alpn_len: 0, sni: false, sid: [0; 32], sid_len: 0, early: false)
    if ch.compression.Len() != 1 || ch.compression[0] != 0 {
        try Bad(alert, AlertIllegalParameter)
    }
    o.sid_len = ch.legacy_id.Len()
    (mut o.sid[0..o.sid_len]).CopyFrom(ch.legacy_id)
    var versions: []u8 = ch.exts[0..0]
    var groups: []u8 = ch.exts[0..0]
    var schemes: []u8 = ch.exts[0..0]
    var shares: ?[]u8 = none
    var alpn: ?[]u8 = none
    var psk := false
    var it := ExtIter.New(ch.exts)
    for x := try it.Next() {
        if psk {
            try Bad(alert, AlertIllegalParameter)
        }
        if x.ty == ExtPreSharedKey {
            psk = true
        } else if x.ty == ExtEarlyData {
            if retry != 0 {
                try Bad(alert, AlertIllegalParameter)
            }
            o.early = true
        } else if x.ty == ExtSupportedVersions {
            versions = try ParseVersionsClient(x.data)
        } else if x.ty == ExtSupportedGroups {
            groups = try ParseGroups(x.data)
        } else if x.ty == ExtSignatureAlgorithms {
            schemes = try ParseSchemes(x.data)
        } else if x.ty == ExtKeyShare {
            shares = some(try ParseKeySharesClient(x.data))
        } else if x.ty == ExtAlpn {
            alpn = some(try ParseAlpn(x.data))
        } else if x.ty == ExtServerName {
            _ = try ParseServerName(x.data)
            o.sni = true
        }
    }
    if !HasU16(versions, WireTls13) {
        *alert = AlertProtocolVersion
        fail Unsupported
    }
    var found := false
    for i in 0..ch.suites.Len() / 2 {
        if s := SuiteOf(U16At(ch.suites, i)) {
            if !found {
                o.suite = s
                found = true
            }
        }
    }
    if !found {
        *alert = AlertHandshakeFailure
        fail Unsupported
    }
    for i in 0..c.identities.Len() {
        s := SchemeOf(c.identities[i].key)
        if o.ident < 0 && HasU16(schemes, s) {
            o.ident = i
            o.scheme = s
        }
    }
    if o.ident < 0 {
        *alert = AlertHandshakeFailure
        fail Unsupported
    }
    list := shares else {
        // A ClientHello with supported_groups has key_share (4.2.8).
        *alert = AlertMissingExtension
        fail Protocol
    }
    var x25519: ?[]u8 = none
    var p256: ?[]u8 = none
    var ks := KeyShares(c: Cursor.New(list))
    for e := try ks.Next() {
        if e.group == GroupX25519 && HasU16(groups, GroupX25519) {
            x25519 = some(e.key)
        } else if e.group == GroupP256 && HasU16(groups, GroupP256) {
            p256 = some(e.key)
        }
    }
    if k := x25519 {
        o.group = GroupX25519
        if k.Len() != 32 {
            try Bad(alert, AlertIllegalParameter)
        }
        o.key_len = 32
        (mut o.key[0..32]).CopyFrom(k)
    } else if k := p256 {
        o.group = GroupP256
        if k.Len() != 65 {
            try Bad(alert, AlertIllegalParameter)
        }
        o.key_len = 65
        (mut o.key[..]).CopyFrom(k)
    } else if retry != 0 {
        try Bad(alert, AlertIllegalParameter)
    } else if HasU16(groups, GroupX25519) {
        o.group = GroupX25519
        o.retry = true
    } else if HasU16(groups, GroupP256) {
        o.group = GroupP256
        o.retry = true
    } else {
        *alert = AlertHandshakeFailure
        fail Unsupported
    }
    if retry != 0 && o.group != retry {
        try Bad(alert, AlertIllegalParameter)
    }
    if offered := alpn {
        if c.alpn.Len() > 0 {
            for p in c.alpn {
                for q in AlpnNames(c: Cursor.New(offered)) {
                    if o.alpn_len == 0 && p.Len() > 0 && crypto.Equal(p.AsBytes(), q) {
                        o.alpn_len = p.Len()
                        (mut o.alpn[0..p.Len()]).CopyFrom(q)
                    }
                }
            }
            if o.alpn_len == 0 {
                *alert = AlertNoApplicationProtocol
                fail Unsupported
            }
        }
    }
    return o
}

// WriteServerHelloOf writes the ServerHello, or with retry the
// HelloRetryRequest (RFC 8446 4.1.3, 4.1.4), for o: supported_versions
// and key_share (the server's share, or the group asked for).
WriteServerHelloOf(w: mut Out, o: &Offer, random: []u8, share: []u8, retry: bool) {
    at := OpenMessage(w, HsServerHello)
    w.U16(WireTls12)
    w.Bytes(random)
    w.Vec8(o.sid[0..o.sid_len])
    w.U16(SuiteCode(o.suite))
    w.U8(0)
    exts := w.Open(2)
    w.U16(ExtSupportedVersions)
    v := w.Open(2)
    w.U16(WireTls13)
    w.Close(v, 2)
    w.U16(ExtKeyShare)
    k := w.Open(2)
    if retry {
        w.U16(o.group)
    } else {
        WriteKeyShare(w, o.group, share)
    }
    w.Close(k, 2)
    w.Close(exts, 2)
    w.Close(at, 3)
}

// Sign writes the CertificateVerify signature of content with key under
// scheme (RFC 8446 4.4.3): ECDSA P-256 over SHA-256, DER-encoded, or
// Ed25519 over the content; returns its length. The secret's copies are
// wiped.
Sign(alert: mut u8, key: &crypto.PrivateKey, content: []u8, out: mut []u8): Protocol!int {
    match key {
        .Ec(secret) {
            var d: [64]u8 = [0; 64]
            n := Digest(crypto.HashKind.Sha256, content, mut d[..])
            return crypto.EcdsaSign(secret, d[0..n], crypto.SigFormat.Der, out) else {
                try Bad(alert, AlertInternalError)
                0
            }
        }
        .Ed25519(seed) {
            var sk := crypto.Ed25519.FromSeed(seed)
            sig := crypto.Ed25519.Sign(&sk, content)
            crypto.Wipe(mut sk[..])
            (mut out[0..64]).CopyFrom(sig[..])
            return 64
        }
    }
}

// ServerShare makes the server's share for o.group into s and pub and
// returns the public value's length.
ServerShare(io: Io, alert: mut u8, o: &Offer, s: mut Shares, pub: mut []u8): Error!int {
    if o.group == GroupX25519 {
        try crypto.Random(io, mut s.x25519[..])
        p := crypto.X25519(&s.x25519, &X25519Base)
        (mut pub[0..32]).CopyFrom(p[..])
        return 32
    }
    sec := crypto.EcSecret.Generate(io, crypto.Curve.P256) else e {
        match e {
            Canceled {
                fail Canceled
            }
            _ {
                try Bad(alert, AlertInternalError)
                fail Protocol
            }
        }
    }
    // A P-256 secret's public point fits 65 bytes: Public cannot fail.
    _ = sec.Public(mut pub[0..65]).Must()
    s.p256 = some(sec)
    return 65
}

// ServerFlights is the server's side of RFC 8446 2's handshake on the
// locked halves rd and wr: ClientHello, a HelloRetryRequest and the
// second ClientHello when needed, ServerHello, then under the server's
// handshake keys EncryptedExtensions, Certificate, CertificateVerify and
// Finished, then the client's Finished, and both directions on the
// application keys. alert gets the alert a failure reports.
ServerFlights(a: &Arena, io: Io, conn: &net.Conn, c: &Config, rd: mut ReadHalf, wr: mut WriteHalf, alert: mut u8): Error!Chosen {
    var hs := try Hs.New(a, true)
    onerr { *alert = hs.alert }
    rd.server = true
    var shares := Shares(x25519: [0; 32], x25519_pub: [0; 32], p256: none, p256_pub: [0; 65])
    defer shares.Wipe()
    n0 := try hs.Expect(rd, conn, io, HsClientHello)
    hs.hello_due = false
    ch := try ParseClientHello(hs.buf[4..n0])
    var o := try ReadOffer(mut hs.alert, &ch, c, 0)
    if o.early {
        hs.early = MaxEarlySkip
    }
    kind := SuiteHash(o.suite)
    hs.transcript.Add(hs.buf[0..n0])
    hs.transcript.Choose(kind)
    hs.Drop(n0)
    var head: [256]u8 = [0; 256]
    // The compatibility mode's one ChangeCipherSpec (RFC 8446 D.4), after
    // the first ServerHello or HelloRetryRequest, when the client sent a
    // session id.
    var ccs_sent := false
    if o.retry {
        // HelloRetryRequest (4.1.4): the transcript restarts from the
        // first ClientHello's hash, and the compatibility mode's
        // ChangeCipherSpec follows it when the client sent a session id.
        if hs.have != 0 {
            try Bad(mut hs.alert, AlertUnexpectedMessage)
        }
        hs.transcript.Restart(kind)
        var hw := Out.New(mut head[..])
        WriteServerHelloOf(mut hw, &o, HrrRandom[..], b"", true)
        hs.transcript.Add(hw.b[0..hw.at])
        try wr.Send(conn, io, TypeHandshake, hw.b[0..hw.at])
        if o.sid_len > 0 {
            try wr.Send(conn, io, TypeChangeCipherSpec, Ccs[..])
            ccs_sent = true
        }
        n1 := try hs.Expect(rd, conn, io, HsClientHello)
        ch2 := try ParseClientHello(hs.buf[4..n1])
        o2 := try ReadOffer(mut hs.alert, &ch2, c, o.group)
        if o2.suite != o.suite {
            try Bad(mut hs.alert, AlertIllegalParameter)
        }
        o = o2
        hs.transcript.Add(hs.buf[0..n1])
        hs.Drop(n1)
        // The early data came before the second ClientHello.
        hs.early = 0
    }
    if hs.have != 0 {
        try Bad(mut hs.alert, AlertUnexpectedMessage)
    }
    hl := HashLen(kind)
    var pub: [65]u8 = [0; 65]
    pub_len := try ServerShare(io, mut hs.alert, &o, mut shares, mut pub[..])
    var shared: [32]u8 = [0; 32]
    try SharedSecret(mut hs.alert, &shares, o.group, o.key[0..o.key_len], mut shared[..])
    var random: [32]u8 = [0; 32]
    try crypto.Random(io, mut random[..])
    var sw := Out.New(mut head[..])
    WriteServerHelloOf(mut sw, &o, random[..], pub[0..pub_len], false)
    hs.transcript.Add(sw.b[0..sw.at])
    try wr.Send(conn, io, TypeHandshake, sw.b[0..sw.at])
    if !ccs_sent && o.sid_len > 0 {
        try wr.Send(conn, io, TypeChangeCipherSpec, Ccs[..])
    }
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
    InstallWrite(wr, o.suite, &s_hs)
    InstallRead(rd, o.suite, &c_hs)
    // The server's flight (4.3.1, 4.4.2, 4.4.3, 4.4.4), each message added
    // to the transcript as it is written.
    ident := &c.identities[o.ident]
    var size := 1024 + o.alpn_len
    for x in ident.chain {
        size += 5 + x.Der().Len()
    }
    flight := try a.Slice[u8](size)
    var w := Out.New(flight)
    var start := 0
    at := OpenMessage(mut w, HsEncryptedExtensions)
    ee := w.Open(2)
    if o.sni {
        WriteExt(mut w, ExtServerName, b"")
    }
    if o.alpn_len > 0 {
        w.U16(ExtAlpn)
        e := w.Open(2)
        l := w.Open(2)
        w.Vec8(o.alpn[0..o.alpn_len])
        w.Close(l, 2)
        w.Close(e, 2)
    }
    w.Close(ee, 2)
    w.Close(at, 3)
    ca := WriteCertificateHead(mut w, b"")
    for x in ident.chain {
        WriteCertEntry(mut w, x.Der(), b"")
    }
    CloseCertificate(mut w, ca)
    if w.full {
        try Bad(mut hs.alert, AlertInternalError)
    }
    hs.transcript.Add(w.b[start..w.at])
    start = w.at
    hs.transcript.Sum(kind, mut th[..])
    var content: [160]u8 = [0; 160]
    cl := SignedContent("TLS 1.3, server CertificateVerify", th[0..hl], mut content[..])
    var sig: [80]u8 = [0; 80]
    sl := try Sign(mut hs.alert, ident.key, content[0..cl], mut sig[..])
    cv := CertificateVerify(scheme: o.scheme, signature: sig[0..sl])
    WriteCertificateVerify(mut w, &cv)
    hs.transcript.Add(w.b[start..w.at])
    start = w.at
    hs.transcript.Sum(kind, mut th[..])
    var mine: [48]u8 = [0; 48]
    FinishedValue(&s_hs, th[0..hl], mut mine[..])
    WriteFinished(mut w, mine[0..hl])
    hs.transcript.Add(w.b[start..w.at])
    if w.full {
        try Bad(mut hs.alert, AlertInternalError)
    }
    try wr.Send(conn, io, TypeHandshake, w.b[0..w.at])
    // The application secrets over the transcript through the server's
    // Finished (7.1); the write side switches now, the read side after
    // the client's Finished.
    sched.DeriveMaster()
    hs.transcript.Sum(kind, mut th[..])
    var c_ap := DeriveSecret(&sched.master, "c ap traffic", th[0..hl])
    defer c_ap.Wipe()
    var s_ap := DeriveSecret(&sched.master, "s ap traffic", th[0..hl])
    defer s_ap.Wipe()
    InstallWrite(wr, o.suite, &s_ap)
    wr.secret = s_ap
    s_ap.Wipe()
    n2 := try hs.Expect(rd, conn, io, HsFinished)
    fin := try ParseFinished(hs.buf[4..n2], hl)
    var want: [48]u8 = [0; 48]
    FinishedValue(&c_hs, th[0..hl], mut want[..])
    if !crypto.Equal(fin, want[0..hl]) {
        try Bad(mut hs.alert, AlertDecryptError)
    }
    hs.Drop(n2)
    if hs.have != 0 {
        try Bad(mut hs.alert, AlertUnexpectedMessage)
    }
    InstallRead(rd, o.suite, &c_ap)
    rd.secret = c_ap
    c_ap.Wipe()
    var chosen := Chosen(alpn: [0; 255], n: o.alpn_len)
    (mut chosen.alpn[0..o.alpn_len]).CopyFrom(o.alpn[0..o.alpn_len])
    return chosen
}

/// Runs the server side of a TLS 1.3 handshake (RFC 8446) over conn, and
/// returns the TLS connection. On success the Conn holds conn; on failure
/// conn stays the caller's, mid-handshake, to Close or Abandon. One
/// signature and two scalar multiplications; allocates the connection's
/// state on h (about 66 KiB, freed by Close or Abandon) and the
/// handshake's buffers on an arena of h, freed before it returns.
///
/// It presents the first of c's identities whose key the client's
/// signature schemes allow (P-256 or Ed25519), and uses c's protocols and
/// limits. It asks for no client certificate.
///
/// Errors: the peer's Alert; Protocol and Unsupported, after the matching
/// alert was sent (Unsupported: no common version, suite, group, scheme or
/// protocol); the connection's net.Error, and time.TimedOut past
/// c.handshake; OutOfMemory. A cancel leaves conn with the caller
/// mid-handshake, with an unknown part of the handshake sent: close it.
export Server(h: &Heap, io: Io, conn: net.Conn, c: &Config): Error!Conn from h, conn {
    var b := try Box[Halves].New(h.Alloc(), Halves.New(c.close))
    var chosen := Chosen(alpn: [0; 255], n: 0)
    scope s := io.Scope() {
        s.CancelAfter(c.handshake)
        chosen = ServerHandshake(h, io, &conn, c, b.Ref()) else e {
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

// ServerHandshake runs ServerFlights on hv's halves, both locked for its
// whole length, on an arena of h freed when it returns; a failure sends
// the alert it chose, best effort.
ServerHandshake(h: &Heap, io: Io, conn: &net.Conn, c: &Config, hv: &Halves): Error!Chosen {
    var a := try Arena.New(h)
    defer a.Free()
    var gr := try hv.rd.Lock(io)
    var gw := hv.wr.Lock(io) else e {
        gr.Unlock()
        fail e
    }
    var alert: u8 = 0
    chosen := ServerFlights(&a, io, conn, c, gr.Mut(), gw.Mut(), mut alert) else e {
        code := AlertFor(&e, alert)
        if code != 0 {
            SendAlert(gw.Mut(), conn, io, code) else {}
        }
        gw.Unlock()
        gr.Unlock()
        fail e
    }
    gw.Unlock()
    gr.Unlock()
    return chosen
}

// AcceptAlpn is the server connector's function: tls.Server over conn,
// accepting offer's protocols in its order when offer is not empty, in a
// box reserved before the handshake and filled after it; host is unused.
// It needs no `h into conn` of its own: ConnSlot.Fill takes t with no
// clause, so filling the box stores nothing into conn. The
// connector type it is given as keeps the clause.
AcceptAlpn(c: &Config, h: &Heap, io: Io, conn: net.Conn, host: str, offer: []str): net.Error!(conn: net.Conn, chosen: int) from h, conn {
    slot := try net.ReserveConn[Secured](h)
    n := WithOffer(c, offer)
    t := Server(h, io, conn, &n) else e {
        slot.Free(h.Alloc())
        fail HandshakeError(&e)
    }
    chosen := OfferIndex(&t, offer)
    return (conn: slot.Fill(Secured(c: t)), chosen: chosen)
}

// Accept is AcceptAlpn with c's own protocols.
Accept(c: &Config, h: &Heap, io: Io, conn: net.Conn, host: str): net.Error!net.Conn from h, conn {
    own: [0]str = []
    (conn: t, chosen: _) := try AcceptAlpn(c, h, io, conn, host, own[..])
    return t
}

/// A server connector over c, to put in http.ServerOptions.secure: each
/// connection the server accepts goes through tls.Server with c. A failed
/// handshake is net.SecureFailed(why: Handshake), and one past c.handshake
/// is time.TimedOut. Its Negotiate accepts the caller's protocols, in
/// their order, in place of c.alpn. It is a plain value that borrows c;
/// copy it freely. O(1); allocates nothing until it accepts.
export Acceptor(c: &Config): net.Secure from c {
    return net.Secure.OfAlpn[Config](core.TypedCall[ConnectFn].Of[Config](c, Accept), core.TypedCall[NegotiateFn].Of[Config](c, AcceptAlpn))
}
