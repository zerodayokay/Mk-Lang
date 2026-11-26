// Why: std.http learns the application protocol a handshake chose only
// through net.Secure.Negotiate, on both the client's and the server's
// side, so the index it returns and the RFC 7301 refusal must hold end to
// end.
// What: test blocks for Connector and Acceptor through Negotiate: a
// client offering h2 and http/1.1 against servers accepting each order,
// one, and neither; empty offers on either side; an offer of about 20
// KiB; and offers the client cannot send, which it refuses before any
// I/O.
// How: run by mk test --std=std.tls. tls.Connector and tls.Acceptor run
// as two tasks of one test over loopback, with a P-256 leaf and root
// valid from 2000 to 2200 given as constants; each case reports both
// sides' chosen index and whether four bytes echoed.

import std.crypto
import std.encoding
import std.net
import std.x509

const NgLeafPem: str = "-----BEGIN CERTIFICATE-----\nMIIBfDCCASGgAwIBAgIBAjAKBggqhkjOPQQDAjAYMRYwFAYDVQQDEw1NayBiZW5j\naCByb290MCAXDTAwMDEwMTAwMDAwMFoYDzIyMDAwMTAxMDAwMDAwWjAUMRIwEAYD\nVQQDEwlsb2NhbGhvc3QwWTATBgcqhkjOPQIBBggqhkjOPQMBBwNCAARYWAiT0sIB\nPFPoIQColXkYt09CRdmx6LjiHDj75/IQjS3AYB3CWa+ymGXqR2ucu2cguSiCoiNK\nqIH+y9bVqzPco14wXDAOBgNVHQ8BAf8EBAMCB4AwEwYDVR0lBAwwCgYIKwYBBQUH\nAwEwHwYDVR0jBBgwFoAUJFRJG8q4njDrib1e2WaLjryUxnkwFAYDVR0RBA0wC4IJ\nbG9jYWxob3N0MAoGCCqGSM49BAMCA0kAMEYCIQD4NROoBKH8KfhkQ/zOMONC60gR\ncRjTYRvpycsxggV3OgIhAKjdsY7ozxVLBHMGzKcJDmUfhRo7ejfd4qjWiC3mdn+n\n-----END CERTIFICATE-----\n"
const NgKeyPem: str = "-----BEGIN PRIVATE KEY-----\nMIGHAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBG0wawIBAQQgQ3K5SvjaOAx5Gj4e\nrlkGTawIlLQ2SZBirD7hp5nMGbChRANCAARYWAiT0sIBPFPoIQColXkYt09CRdmx\n6LjiHDj75/IQjS3AYB3CWa+ymGXqR2ucu2cguSiCoiNKqIH+y9bVqzPc\n-----END PRIVATE KEY-----\n"
const NgRootPem: str = "-----BEGIN CERTIFICATE-----\nMIIBYzCCAQmgAwIBAgIBATAKBggqhkjOPQQDAjAYMRYwFAYDVQQDEw1NayBiZW5j\naCByb290MCAXDTAwMDEwMTAwMDAwMFoYDzIyMDAwMTAxMDAwMDAwWjAYMRYwFAYD\nVQQDEw1NayBiZW5jaCByb290MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEyTx+\nYlmJmAU6O4dZ0UQ3l5NkWBp3YM0DnNEBOBQ4TupVoFHlQRki7kyY6p+VX9Qrr6Hn\nz8thWyiM/gt642/2dqNCMEAwDgYDVR0PAQH/BAQDAgIEMA8GA1UdEwEB/wQFMAMB\nAf8wHQYDVR0OBBYEFCRUSRvKuJ4w64m9Xtlmi468lMZ5MAoGCCqGSM49BAMCA0gA\nMEUCIQCGfzbV5b0fQ7vvXGWBDlwaBxou4hsIMFapz+zbFYiqbQIgIH6Cu3lYtLDM\n5NakQ34Pul2rlQOLEFZvP0CmTMKU1NE=\n-----END CERTIFICATE-----\n"

// NgRefused is a side's report for a handshake that failed with
// SecureFailed(why: Handshake); NgOther for any other failure.
const NgRefused: int = -2
const NgOther: int = -3

// NgCode is a failed Negotiate as a side's report.
NgCode(e: &net.Error): int {
    return match *e {
        net.SecureFailed(why) {
            if why == net.SecureFailure.Handshake { NgRefused } else { NgOther }
        }
        _ { NgOther }
    }
}

// NgSame reports whether b holds exactly s.
NgSame(b: []u8, s: str): bool {
    t := str.From(b) else {
        return false
    }
    return t == s
}

// NgServe accepts one connection, negotiates accept through sec, then
// echoes four bytes: report is the chosen index or NgCode's, and echoed
// whether the four bytes went back.
NgServe(h: &Heap, io: Io, l: &net.Listener, sec: &net.Secure, accept: []str, report: mut int, echoed: mut bool) {
    st := l.Accept(io) else {
        *report = NgOther
        return
    }
    conn := st.Conn()
    (conn: c, chosen) := sec.Negotiate(h, io, conn, "", accept) else e {
        conn.Abandon()
        *report = NgCode(&e)
        return
    }
    *report = chosen
    var buf: [4]u8 = [0; 4]
    var got := 0
    for got < 4 {
        k := c.Read(io, mut buf[got..]) else {
            break
        }
        if k == 0 {
            break
        }
        got += k
    }
    if got == 4 {
        c.WriteAll(io, buf[..]) else {
            got = 0
        }
    }
    *echoed = got == 4
    c.Close(io) else {
        c.Abandon()
    }
}

// NgResult is one case: each side's chosen index or failure code, and
// whether "ping" went to the server and came back.
struct NgResult {
    client: int
    server: int
    echoed: bool
}

// NgCase runs a client negotiating offer against a server negotiating
// accept, over loopback; own is the client Config's own alpn list.
NgCase(sys: &System, offer: []str, accept: []str, own: []str): x509.Error|encoding.Error|crypto.Error|Error!NgResult {
    io := sys.io
    a := sys.arena
    certs := try x509.ParsePem(a, NgLeafPem)
    blocks := try encoding.ParsePem(a, NgKeyPem)
    key := try crypto.ParsePrivateKey(blocks[0].der)
    roots := try x509.ParsePem(a, NgRootPem)
    chain := certs.Done()
    ids: [1]Identity = [Identity(chain: chain[..], key: &key)]
    var scfg := DefaultConfig
    scfg.identities = ids[..]
    rs := roots.Done()
    pool := x509.Pool.New(rs[..])
    var ccfg := DefaultConfig
    ccfg.roots = some(&pool)
    ccfg.alpn = own
    server := Acceptor(&scfg)
    client := Connector(&ccfg)
    l := try net.Listen(io, "127.0.0.1:0")
    defer l.Close()
    addr := try a.Fmt("127.0.0.1:{p}", (p: l.Port()))
    var r := NgResult(client: NgOther, server: NgOther, echoed: false)
    var served := false
    var echoed := false
    scope s := io.Scope() {
        s.go NgServe(sys.heap, io, &l, &server, accept, mut r.server, mut served)
        if !NgClient(sys.heap, io, &client, addr, offer, mut r.client, mut echoed) {
            // No connection reached the server: end its Accept, so the
            // case fails instead of waiting.
            s.Cancel()
        }
    }
    r.echoed = echoed && served
    return r
}

// NgClient dials addr, negotiates offer through sec, sends "ping" and
// reads it back: report is the chosen index or NgCode's, and echoed
// whether "ping" came back. It returns false when the dial failed.
NgClient(h: &Heap, io: Io, sec: &net.Secure, addr: str, offer: []str, report: mut int, echoed: mut bool): bool {
    stream := net.Dial(io, addr) else {
        return false
    }
    conn := stream.Conn()
    (conn: c, chosen) := sec.Negotiate(h, io, conn, "localhost", offer) else e {
        conn.Abandon()
        *report = NgCode(&e)
        return true
    }
    *report = chosen
    c.WriteAll(io, "ping".AsBytes()) else {
        c.Abandon()
        return true
    }
    var back: [4]u8 = [0; 4]
    var got := 0
    for got < 4 {
        k := c.Read(io, mut back[got..]) else {
            break
        }
        if k == 0 {
            break
        }
        got += k
    }
    *echoed = got == 4 && NgSame(back[..], "ping")
    c.Close(io) else {
        c.Abandon()
    }
    return true
}

test "Negotiate offering h2 then http/1.1: the server's order decides, and both sides report the index into their own offer" (sys: &System) {
    offer: [2]str = ["h2", "http/1.1"]
    h2: [1]str = ["h2"]
    h1: [1]str = ["http/1.1"]
    rev: [2]str = ["http/1.1", "h2"]
    own: [0]str = []
    r := try NgCase(sys, offer[..], h2[..], own[..])
    Assert(r.client == 0 && r.server == 0 && r.echoed)
    r1 := try NgCase(sys, offer[..], h1[..], own[..])
    Assert(r1.client == 1 && r1.server == 0 && r1.echoed)
    // RFC 7301 3.2: the server picks by its own preference.
    r2 := try NgCase(sys, offer[..], rev[..], own[..])
    Assert(r2.client == 1 && r2.server == 0 && r2.echoed)
}

test "Negotiate with no protocol in common fails on both sides as a failed handshake" (sys: &System) {
    offer: [2]str = ["h2", "http/1.1"]
    other: [1]str = ["x-other"]
    own: [0]str = []
    r := try NgCase(sys, offer[..], other[..], own[..])
    // The server sends no_application_protocol and fails with Unsupported,
    // the client reads Alert(code: 120); through net.Secure both are
    // SecureFailed(why: Handshake). TestStdTls_GoClientNegotiate checks the
    // alert as Go's client reports it.
    Assert(r.client == NgRefused && r.server == NgRefused && !r.echoed)
}

test "Negotiate with an empty offer is Connect: the connector's own list is used and chosen is -1" (sys: &System) {
    offer: [2]str = ["h2", "http/1.1"]
    h2: [1]str = ["h2"]
    h1: [1]str = ["http/1.1"]
    own: [0]str = []
    // No ALPN from the client: the server, which accepts h2, chooses none.
    r := try NgCase(sys, own[..], h2[..], own[..])
    Assert(r.client == -1 && r.server == -1 && r.echoed)
    // An empty server offer is the server Config's own list, empty here:
    // the server ignores the client's ALPN and chooses none.
    r1 := try NgCase(sys, offer[..], own[..], own[..])
    Assert(r1.client == -1 && r1.server == -1 && r1.echoed)
    // The client Config's own list is offered: the server chooses it,
    // but the client's empty offer has no index for it.
    r2 := try NgCase(sys, own[..], offer[..], h1[..])
    Assert(r2.client == -1 && r2.server == 1 && r2.echoed)
}

test "Negotiate refuses an offer name that is empty or over 255 bytes, as it does one of Config.alpn" (sys: &System) {
    accept: [1]str = ["h2"]
    own: [0]str = []
    empty: [2]str = ["h2", ""]
    r := try NgCase(sys, empty[..], accept[..], own[..])
    Assert(r.client == NgRefused && r.server <= NgRefused && !r.echoed)
    var long: [256]u8 = [0x61; 256]
    // 256 bytes of ASCII are valid UTF-8, so From cannot fail.
    name := str.From(long[..]).Must()
    big: [1]str = [name]
    r1 := try NgCase(sys, big[..], accept[..], own[..])
    Assert(r1.client == NgRefused && r1.server <= NgRefused && !r1.echoed)
}

// NgCount accepts one connection and reads it to its end, counting the
// bytes in got.
NgCount(io: Io, l: &net.Listener, got: mut int) {
    st := l.Accept(io) else {
        *got = -1
        return
    }
    var buf: [512]u8 = [0; 512]
    for {
        k := st.Read(io, mut buf[..]) else {
            break
        }
        if k == 0 {
            break
        }
        *got += k
    }
    st.Close()
}

// NgUnsent dials addr and runs tls.Client with c: whether it failed with
// Unsupported. The connection is abandoned either way.
NgUnsent(h: &Heap, io: Io, addr: str, c: &Config): bool {
    stream := net.Dial(io, addr) else {
        return false
    }
    conn := stream.Conn()
    t := Client(h, io, conn, c, "localhost") else e {
        conn.Abandon()
        return match e {
            Unsupported { true }
            _ { false }
        }
    }
    t.Abandon()
    return false
}

// NgSilent runs tls.Client offering bad against a listener that counts
// what reaches it: whether the client failed with Unsupported, and the
// bytes the server read.
NgSilent(sys: &System, bad: []str): Error!(refused: bool, got: int) {
    io := sys.io
    l := try net.Listen(io, "127.0.0.1:0")
    defer l.Close()
    addr := try sys.arena.Fmt("127.0.0.1:{p}", (p: l.Port()))
    var ccfg := DefaultConfig
    var refused := false
    var got := 0
    n := WithOffer(&ccfg, bad)
    scope s := io.Scope() {
        s.go NgCount(io, &l, mut got)
        refused = NgUnsent(sys.heap, io, addr, &n)
    }
    return (refused: refused, got: got)
}

test "An offer the client cannot send fails tls.Client with Unsupported before any I/O: the server reads nothing" (sys: &System) {
    empty: [2]str = ["h2", ""]
    r := try NgSilent(sys, empty[..])
    Assert(r.refused && r.got == 0)
    var long: [256]u8 = [0x61; 256]
    // 256 bytes of ASCII are valid UTF-8, so From cannot fail.
    name := str.From(long[..]).Must()
    big: [1]str = [name]
    r1 := try NgSilent(sys, big[..])
    Assert(r1.refused && r1.got == 0)
    // 257 names of 255 bytes are 65,792 bytes of list, past its 2-byte
    // length (RFC 7301 3.1).
    many: [257]str = [name[0..255]; 257]
    r2 := try NgSilent(sys, many[..])
    Assert(r2.refused && r2.got == 0)
}

test "AlpnFits takes names of 1 to 255 bytes up to a list of 65,535 bytes" (sys: &System) {
    var long: [255]u8 = [0x61; 255]
    name := str.From(long[..]).Must()
    // 255 names of 256 bytes on the wire and one of 255: 65,535 bytes.
    var full: [256]str = [name; 256]
    full[255] = name[0..254]
    Assert(AlpnFits(full[..]))
    full[255] = name
    Assert(!AlpnFits(full[..]))
    nothing: [0]str = []
    Assert(AlpnFits(nothing[..]))
    one: [1]str = [""]
    Assert(!AlpnFits(one[..]))
}

// NgMany is count names of 250 bytes, each made distinct by its first
// three bytes, then last: an offer of about 250 * count bytes.
NgMany(a: &Arena, count: int, last: str): OutOfMemory![]str from a {
    var out := List[str].New(a)
    for i in 0..count {
        var b: [250]u8 = [0x62; 250]
        b[0] = Trunc[u8](0x41 + i / 676 % 26)
        b[1] = Trunc[u8](0x41 + i / 26 % 26)
        b[2] = Trunc[u8](0x41 + i % 26)
        try out.Push(try a.Dup(str.From(b[..]).Must()))
    }
    try out.Push(try a.Dup(last))
    return out.Done()[..]
}

test "Negotiate with an offer of about 20 KiB, a ClientHello over two records, picks its last name" (sys: &System) {
    offer := try NgMany(sys.arena, 80, "h2")
    accept: [1]str = ["h2"]
    own: [0]str = []
    r := try NgCase(sys, offer, accept[..], own[..])
    Assert(r.client == 80 && r.server == 0 && r.echoed)
}
