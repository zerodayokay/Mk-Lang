// Why: net.Secure.Negotiate's chosen index agrees with Conn.Alpn only if
// the client takes from the server nothing but one protocol it offered,
// and the server picks by its own order; the loopback blocks cannot make
// a peer break those rules.
// What: test blocks for the client's checks of the server's ALPN answer,
// in TLS 1.3's EncryptedExtensions and TLS 1.2's ServerHello (RFC 7301
// 3.1, RFC 8446 4.2), each with the alert it reports, and for the
// server's choice through WithOffer (RFC 7301 3.2).
// How: run by mk test --std=std.tls. The messages are written byte by
// byte, or by WriteHello, and given to the checks directly.

import std.crypto
import std.x509

// AlRefusal is a check's failure as the alert it chose and whether it was
// Protocol, or (-1, false) when it passed.
struct AlRefusal {
    alert: int
    protocol: bool
}

// AlEe runs CheckEncryptedExtensions over exts for a client that offered
// offer.
AlEe(exts: []u8, offer: []str): AlRefusal {
    suites: [1]u16 = [0x1301]
    m := Hello(suites: suites[..], random: [0; 32], sid: [0; 32], sni: "", alpn: offer, group: GroupX25519, cookie: b"")
    var alert: u8 = 0
    _ = CheckEncryptedExtensions(mut alert, exts, &m) else {
        return AlRefusal(alert: int(alert), protocol: true)
    }
    return AlRefusal(alert: -1, protocol: false)
}

test "EncryptedExtensions: the client takes one protocol it offered, and refuses another, two, or one it did not ask for" (sys: &System) {
    offer: [2]str = ["h2", "http/1.1"]
    nothing: [0]str = []
    // application_layer_protocol_negotiation (16), one name "h2".
    h2: [9]u8 = [0, 16, 0, 5, 0, 3, 2, 0x68, 0x32]
    h3: [9]u8 = [0, 16, 0, 5, 0, 3, 2, 0x68, 0x33]
    two: [18]u8 = [0, 16, 0, 14, 0, 12, 2, 0x68, 0x32, 8, 0x68, 0x74, 0x74, 0x70, 0x2f, 0x31, 0x2e, 0x31]
    suites: [1]u16 = [0x1301]
    m := Hello(suites: suites[..], random: [0; 32], sid: [0; 32], sni: "", alpn: offer[..], group: GroupX25519, cookie: b"")
    var alert: u8 = 0
    p := CheckEncryptedExtensions(mut alert, h2[..], &m) else {
        Panic("an offered protocol was refused")
    }
    Assert(p.Len() == 2 && p[0] == 0x68 && p[1] == 0x32 && alert == 0)
    // RFC 7301 3.1: the answer names one protocol, one the client offered.
    r := AlEe(h3[..], offer[..])
    Assert(r.protocol && int(AlertIllegalParameter) == r.alert)
    r1 := AlEe(two[..], offer[..])
    Assert(r1.protocol && int(AlertIllegalParameter) == r1.alert)
    // RFC 8446 4.2: an answer to an extension the client did not send.
    r2 := AlEe(h2[..], nothing[..])
    Assert(r2.protocol && int(AlertUnsupportedExtension) == r2.alert)
    // An empty list is malformed: decode_error, chosen where it is sent.
    empty: [6]u8 = [0, 16, 0, 2, 0, 0]
    r3 := AlEe(empty[..], offer[..])
    Assert(r3.protocol && r3.alert == 0)
}

// AlSh12 runs CheckServerHello12 over a TLS 1.2 ServerHello carrying the
// extensions exts, for a client that offered offer.
AlSh12(exts: []u8, offer: []str): (r: AlRefusal, n: int) {
    suites: [1]u16 = [0xc02b]
    m := Hello(suites: suites[..], random: [0; 32], sid: [0; 32], sni: "", alpn: offer, group: GroupX25519, cookie: b"")
    random: [32]u8 = [0x11; 32]
    sh := ServerHello(version: WireTls12, random: random[..], legacy_id: random[0..0], suite: 0xc02b, compression: 0, has_exts: true,
        exts: exts)
    var chosen := Chosen(alpn: [0; 255], n: 0)
    var alert: u8 = 0
    _ = CheckServerHello12(mut alert, &sh, &m, mut chosen) else {
        return (r: AlRefusal(alert: int(alert), protocol: true), n: -1)
    }
    return (r: AlRefusal(alert: -1, protocol: false), n: chosen.n)
}

test "TLS 1.2 ServerHello: the client takes one protocol it offered, and refuses another, two, or one it did not ask for" (sys: &System) {
    offer: [2]str = ["h2", "http/1.1"]
    nothing: [0]str = []
    // extended_master_secret (23), empty, then the ALPN answer.
    h2: [13]u8 = [0, 23, 0, 0, 0, 16, 0, 5, 0, 3, 2, 0x68, 0x32]
    h3: [13]u8 = [0, 23, 0, 0, 0, 16, 0, 5, 0, 3, 2, 0x68, 0x33]
    two: [22]u8 = [0, 23, 0, 0, 0, 16, 0, 14, 0, 12, 2, 0x68, 0x32, 8, 0x68, 0x74, 0x74, 0x70, 0x2f, 0x31, 0x2e, 0x31]
    ok := AlSh12(h2[..], offer[..])
    Assert(!ok.r.protocol && ok.n == 2)
    r := AlSh12(h3[..], offer[..])
    Assert(r.r.protocol && int(AlertIllegalParameter) == r.r.alert)
    r1 := AlSh12(two[..], offer[..])
    Assert(r1.r.protocol && int(AlertIllegalParameter) == r1.r.alert)
    r2 := AlSh12(h2[..], nothing[..])
    Assert(r2.r.protocol && int(AlertUnsupportedExtension) == r2.r.alert)
}

// AlOffer writes a ClientHello offering protocols, parses it, and runs
// ReadOffer with the server Config cfg under accept (WithOffer): the
// chosen protocol's bytes in out and their count, or the alert as a
// negative count.
AlOffer(a: &Arena, protocols: []str, cfg: &Config, accept: []str, out: mut []u8): OutOfMemory!int {
    suites: [1]u16 = [0x1301]
    m := Hello(suites: suites[..], random: [0; 32], sid: [0; 32], sni: "", alpn: protocols, group: GroupX25519, cookie: b"")
    var shares := Shares(x25519: [0; 32], x25519_pub: [0; 32], p256: none, p256_pub: [0; 65])
    defer shares.Wipe()
    var buf := try a.Slice[u8](HelloSize(&m))
    var w := Out.New(mut buf[..])
    WriteHello(mut w, &m, &shares)
    Assert(!w.full)
    r := ParseHandshake(w.b[0..w.at], 0xFFFFFF) else {
        Panic("WriteHello's message does not parse")
    }
    got := r else {
        Panic("WriteHello's message is short")
    }
    ch := ParseClientHello(got.m.body) else {
        Panic("WriteHello's ClientHello does not parse")
    }
    n := WithOffer(cfg, accept)
    var alert: u8 = 0
    o := ReadOffer(mut alert, &ch, &n, 0) else {
        return -int(alert)
    }
    (mut out[0..o.alpn_len]).CopyFrom(o.alpn[0..o.alpn_len])
    return o.alpn_len
}

test "WithOffer: a server picks the first of its offer the client sent, skips an empty name, and refuses no overlap with no_application_protocol" (sys: &System) {
    seed: [32]u8 = [9; 32]
    key := crypto.PrivateKey.Ed25519(seed: seed)
    nochain: [0]x509.Certificate = []
    ids: [1]Identity = [Identity(chain: nochain[..], key: &key)]
    var cfg := DefaultConfig
    cfg.identities = ids[..]
    client: [2]str = ["http/1.1", "h2"]
    var out: [255]u8 = [0; 255]
    mine: [2]str = ["h2", "http/1.1"]
    // RFC 7301 3.2: the server's preference, not the client's.
    k := try AlOffer(sys.arena, client[..], &cfg, mine[..], mut out[..])
    Assert(k == 2 && out[0] == 0x68 && out[1] == 0x32)
    skip: [2]str = ["", "http/1.1"]
    k1 := try AlOffer(sys.arena, client[..], &cfg, skip[..], mut out[..])
    Assert(k1 == 8 && out[0] == 0x68 && out[4] == 0x2f)
    other: [1]str = ["x-other"]
    k2 := try AlOffer(sys.arena, client[..], &cfg, other[..], mut out[..])
    Assert(k2 == -int(AlertNoApplicationProtocol))
    // An empty offer is the Config's own list, empty here: no choice and
    // no refusal.
    nothing: [0]str = []
    k3 := try AlOffer(sys.arena, client[..], &cfg, nothing[..], mut out[..])
    Assert(k3 == 0)
    // The offer replaces the Config's own list, which is not consulted.
    cfg.alpn = other[..]
    k4 := try AlOffer(sys.arena, client[..], &cfg, mine[..], mut out[..])
    Assert(k4 == 2 && out[0] == 0x68)
    // A client that sent no ALPN gets none, whatever the server accepts.
    k5 := try AlOffer(sys.arena, nothing[..], &cfg, mine[..], mut out[..])
    Assert(k5 == 0)
}
