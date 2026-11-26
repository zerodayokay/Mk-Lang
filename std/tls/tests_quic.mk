// Why: QUIC's TLS 1.3 handshake enforces strict RFC 9001 rules (no
// TLS 1.2, no CCM_8, empty legacy_session_id, no CCS, mandatory ALPN,
// mandatory 0x39, fatal alerts, no KeyUpdate) and must match RFC 9001
// A.1's key schedule.
//
// What: unit and loopback-pipe test blocks for QuicClient, QuicServer,
// QuicPostMessage, RFC 9001 A.1 via Schedule/ExpandLabel, and every B2
// rule of RFC 9001 ("mk test --std std.tls -run QUIC").
//
// How: MemQuic (an in-memory QuicTransport over Chan + Mutex) connects
// QuicClient and QuicServer concurrently; ScriptQuic feeds scripted peer
// messages per level and records sent bytes, secrets, params and alerts.

import std.crypto
import std.encoding
import std.net
import std.time
import std.x509

// QkBuf holds queued bytes at one encryption level of a MemQueue.
struct QkBuf {
    data: [16384]u8
    start: int
    have: int
    closed: bool
}

// MemQueue is one unidirectional CRYPTO stream for one encryption level.
struct MemQueue {
    mu: Mutex[QkBuf]
    wake: Chan[u8]
}

MemQueue.New(a: &Alloc): OutOfMemory!MemQueue {
    return MemQueue(
        mu: Mutex[QkBuf].New(QkBuf(data: [0; 16384], start: 0, have: 0, closed: false)),
        wake: try Chan[u8].New(a, 64),
    )
}

MemQueue.Free(self, a: &Alloc) {
    MemQueue(mu, wake) := self
    _ = mu.Into()
    wake.Free(a)
}

MemQueue.Push(&self, io: Io, bytes: []u8): Error!void {
    var g := try self.mu.Lock(io)
    var b := g.Mut()
    if b.have + bytes.Len() > b.data.Len() {
        g.Unlock()
        fail Protocol
    }
    (mut b.data[b.have..b.have + bytes.Len()]).CopyFrom(bytes)
    b.have += bytes.Len()
    g.Unlock()
    _ = self.wake.TrySend(1) else {}
}

MemQueue.Pop(&self, io: Io, out: mut []u8): Error!int {
    for {
        var g := try self.mu.Lock(io)
        avail := g.Ref().have - g.Ref().start
        if avail > 0 {
            k := if out.Len() < avail { out.Len() } else { avail }
            at := g.Ref().start
            (mut out[0..k]).CopyFrom(g.Ref().data[at..at + k])
            g.Mut().start += k
            if g.Ref().start == g.Ref().have {
                g.Mut().start = 0
                g.Mut().have = 0
            }
            g.Unlock()
            return k
        }
        if g.Ref().closed {
            g.Unlock()
            fail net.Reset
        }
        g.Unlock()
        _ = self.wake.Recv(io) else e {
            match e {
                Canceled { fail Canceled }
                Closed { fail net.Reset }
            }
        }
    }
}

MemQueue.Close(&self, io: Io) {
    var g := self.mu.Lock(io.NoCancel()) else {
        self.wake.Close()
        return
    }
    g.Mut().closed = true
    g.Unlock()
    self.wake.Close()
}

// QkSecret records one installed traffic secret.
struct QkSecret {
    set: bool
    suite: QuicSuite
    len: int
    bytes: [48]u8
}

QkLevelIdx(l: QuicLevel): int {
    return match l {
        .Initial { 0 }
        .Handshake { 1 }
        .Application { 2 }
    }
}

// MemEnd is one side's QuicTransport over a pair of MemQueue arrays.
struct MemEnd {
    rd: [3]&MemQueue
    wr: [3]&MemQueue
    rsec: [3]QkSecret
    wsec: [3]QkSecret
    peer_params: [256]u8
    peer_params_len: int
    got_params: bool
    alert_sent: bool
    alert_code: u8
    sent_hs: [16384]u8
    sent_hs_len: int
    sent_init: [4096]u8
    sent_init_len: int
}

MemEnd.New(rd: [3]&MemQueue, wr: [3]&MemQueue): MemEnd from rd, wr {
    empty := QkSecret(set: false, suite: QuicSuite.Aes128Gcm, len: 0, bytes: [0; 48])
    return MemEnd(
        rd: rd,
        wr: wr,
        rsec: [empty, empty, empty],
        wsec: [empty, empty, empty],
        peer_params: [0; 256],
        peer_params_len: 0,
        got_params: false,
        alert_sent: false,
        alert_code: 0,
        sent_hs: [0; 16384],
        sent_hs_len: 0,
        sent_init: [0; 4096],
        sent_init_len: 0,
    )
}

impl QuicTransport for MemEnd {
    Recv(mut self, io: Io, level: QuicLevel, buf: mut []u8): Error!int {
        return try self.rd[QkLevelIdx(level)].Pop(io, buf)
    }

    Send(mut self, io: Io, level: QuicLevel, data: []u8): Error!void {
        idx := QkLevelIdx(level)
        if idx == 0 && self.sent_init_len + data.Len() <= self.sent_init.Len() {
            (mut self.sent_init[self.sent_init_len..self.sent_init_len + data.Len()]).CopyFrom(data)
            self.sent_init_len += data.Len()
        }
        if idx == 1 && self.sent_hs_len + data.Len() <= self.sent_hs.Len() {
            (mut self.sent_hs[self.sent_hs_len..self.sent_hs_len + data.Len()]).CopyFrom(data)
            self.sent_hs_len += data.Len()
        }
        try self.wr[idx].Push(io, data)
    }

    Secret(mut self, level: QuicLevel, write: bool, suite: QuicSuite, secret: []u8): Error!void {
        idx := QkLevelIdx(level)
        var rec := QkSecret(set: true, suite: suite, len: secret.Len(), bytes: [0; 48])
        (mut rec.bytes[0..secret.Len()]).CopyFrom(secret)
        if write {
            self.wsec[idx] = rec
        } else {
            self.rsec[idx] = rec
        }
    }

    PeerParams(mut self, params: []u8): Error!void {
        self.got_params = true
        self.peer_params_len = params.Len()
        if params.Len() <= self.peer_params.Len() {
            (mut self.peer_params[0..params.Len()]).CopyFrom(params)
        }
    }

    Alert(mut self, io: Io, code: u8) {
        self.alert_sent = true
        self.alert_code = code
    }
}

// ScriptQuic is a single-task QuicTransport fed with canned CRYPTO bytes per
// level, capturing outgoing bytes, secrets, peer parameters and alerts.
struct ScriptQuic {
    in_buf: [3][4096]u8
    in_at: [3]int
    in_len: [3]int
    out_buf: [3][4096]u8
    out_len: [3]int
    rsec: [3]QkSecret
    wsec: [3]QkSecret
    alert_sent: bool
    alert_code: u8
    peer_params_called: bool
    fail_on_secret: bool
    sleep_recv: bool
}

ScriptQuic.New(): ScriptQuic {
    empty := QkSecret(set: false, suite: QuicSuite.Aes128Gcm, len: 0, bytes: [0; 48])
    return ScriptQuic(
        in_buf: [[0; 4096], [0; 4096], [0; 4096]],
        in_at: [0, 0, 0],
        in_len: [0, 0, 0],
        out_buf: [[0; 4096], [0; 4096], [0; 4096]],
        out_len: [0, 0, 0],
        rsec: [empty, empty, empty],
        wsec: [empty, empty, empty],
        alert_sent: false,
        alert_code: 0,
        peer_params_called: false,
        fail_on_secret: false,
        sleep_recv: false,
    )
}

ScriptQuic.Feed(mut self, level: QuicLevel, data: []u8) {
    idx := QkLevelIdx(level)
    n := self.in_len[idx]
    (mut self.in_buf[idx][n..n + data.Len()]).CopyFrom(data)
    self.in_len[idx] += data.Len()
}

impl QuicTransport for ScriptQuic {
    Recv(mut self, io: Io, level: QuicLevel, buf: mut []u8): Error!int {
        if self.sleep_recv {
            try io.Sleep(time.Seconds(10))
        }
        idx := QkLevelIdx(level)
        avail := self.in_len[idx] - self.in_at[idx]
        if avail <= 0 {
            fail net.Reset
        }
        k := if buf.Len() < avail { buf.Len() } else { avail }
        at := self.in_at[idx]
        (mut buf[0..k]).CopyFrom(self.in_buf[idx][at..at + k])
        self.in_at[idx] += k
        return k
    }

    Send(mut self, io: Io, level: QuicLevel, data: []u8): Error!void {
        idx := QkLevelIdx(level)
        n := self.out_len[idx]
        if n + data.Len() <= self.out_buf[idx].Len() {
            (mut self.out_buf[idx][n..n + data.Len()]).CopyFrom(data)
            self.out_len[idx] += data.Len()
        }
    }

    Secret(mut self, level: QuicLevel, write: bool, suite: QuicSuite, secret: []u8): Error!void {
        if self.fail_on_secret {
            fail Protocol
        }
        idx := QkLevelIdx(level)
        var rec := QkSecret(set: true, suite: suite, len: secret.Len(), bytes: [0; 48])
        (mut rec.bytes[0..secret.Len()]).CopyFrom(secret)
        if write {
            self.wsec[idx] = rec
        } else {
            self.rsec[idx] = rec
        }
    }

    PeerParams(mut self, params: []u8): Error!void {
        self.peer_params_called = true
    }

    Alert(mut self, io: Io, code: u8) {
        self.alert_sent = true
        self.alert_code = code
    }
}

// QkBuildHello builds a raw ClientHello handshake message for negative
// server tests.
QkBuildHello(a: &Arena, sid: []u8, suites: []u16, alpn: []str, include_tls13: bool, include_0x39: bool, include_early: bool, quic_params: []u8): OutOfMemory![]u8 from a {
    buf := try a.Slice[u8](2048)
    var w := Out.New(buf)
    at := OpenMessage(mut w, HsClientHello)
    w.U16(WireTls12)
    rand: [32]u8 = [0x11; 32]
    w.Bytes(rand[..])
    w.Vec8(sid)
    sw := w.Open(2)
    for s in suites {
        w.U16(s)
    }
    w.Close(sw, 2)
    w.U8(1)
    w.U8(0)
    exts := w.Open(2)
    w.U16(ExtSupportedGroups)
    eg := w.Open(2)
    lg := w.Open(2)
    w.U16(GroupX25519)
    w.Close(lg, 2)
    w.Close(eg, 2)
    w.U16(ExtSignatureAlgorithms)
    es := w.Open(2)
    ls := w.Open(2)
    w.U16(SchemeEcdsaP256)
    w.U16(SchemeEd25519)
    w.Close(ls, 2)
    w.Close(es, 2)
    w.U16(ExtSupportedVersions)
    ev := w.Open(2)
    lv := w.Open(1)
    if include_tls13 {
        w.U16(WireTls13)
    } else {
        w.U16(WireTls12)
    }
    w.Close(lv, 1)
    w.Close(ev, 2)
    w.U16(ExtKeyShare)
    ek := w.Open(2)
    lk := w.Open(2)
    var sk: [32]u8 = [7; 32]
    pub := crypto.X25519(&sk, &X25519Base)
    WriteKeyShare(mut w, GroupX25519, pub[..])
    w.Close(lk, 2)
    w.Close(ek, 2)
    if alpn.Len() > 0 {
        w.U16(ExtAlpn)
        ea := w.Open(2)
        WriteAlpn(mut w, alpn)
        w.Close(ea, 2)
    }
    if include_0x39 {
        WriteExt(mut w, ExtQuicParams, quic_params)
    }
    if include_early {
        WriteExt(mut w, ExtEarlyData, b"")
    }
    w.Close(exts, 2)
    w.Close(at, 3)
    Assert(!w.full)
    return w.b[0..w.at]
}

// QkSameSecret checks two QkSecret entries hold identical bytes and suite.
QkSameSecret(a: &QkSecret, b: &QkSecret): bool {
    if !a.set || !b.set || a.len != b.len || a.suite != b.suite {
        return false
    }
    return crypto.Equal(a.bytes[0..a.len], b.bytes[0..b.len])
}

// QkServerTask runs QuicServer in a scoped task.
QkServerTask(h: &Heap, io: Io, t: mut MemEnd, cfg: &Config, alpn: []str, params: []u8, out: mut ?QuicDone, failed: mut bool) {
    d := QuicServer(h, io, t, cfg, alpn, params) else {
        *failed = true
        return
    }
    *out = some(d)
}

test "QUIC: full Mk QuicClient against Mk QuicServer handshake installs matching secrets, ALPN and transport parameters" (sys: &System) {
    a := sys.arena
    h := sys.heap
    io := sys.io
    // Note: QkConfig borrows from a and Kp allocated on a, so build Config
    // directly with stack storage for Identity.
    certs := (try x509.ParsePem(a, NgLeafPem)).Done()
    blocks := try encoding.ParsePem(a, NgKeyPem)
    key := try crypto.ParsePrivateKey(blocks[0].der)
    roots_certs := (try x509.ParsePem(a, NgRootPem)).Done()
    var roots := x509.Pool.New(roots_certs[..])
    var ccfg := DefaultConfig
    ccfg.roots = some(&roots)
    // QS-7: even with min_version Tls12, QUIC stays TLS 1.3 only.
    ccfg.min_version = Version.Tls12
    ids: [1]Identity = [Identity(chain: certs[..], key: &key)]
    var scfg := DefaultConfig
    scfg.identities = ids[..]

    var c2s0 := try MemQueue.New(h.Alloc())
    defer c2s0.Free(h.Alloc())
    var c2s1 := try MemQueue.New(h.Alloc())
    defer c2s1.Free(h.Alloc())
    var c2s2 := try MemQueue.New(h.Alloc())
    defer c2s2.Free(h.Alloc())
    var s2c0 := try MemQueue.New(h.Alloc())
    defer s2c0.Free(h.Alloc())
    var s2c1 := try MemQueue.New(h.Alloc())
    defer s2c1.Free(h.Alloc())
    var s2c2 := try MemQueue.New(h.Alloc())
    defer s2c2.Free(h.Alloc())
    var client_t := MemEnd.New([&s2c0, &s2c1, &s2c2], [&c2s0, &c2s1, &c2s2])
    var server_t := MemEnd.New([&c2s0, &c2s1, &c2s2], [&s2c0, &s2c1, &s2c2])

    c_alpn: [2]str = ["http/1.1", "h3"]
    s_alpn: [1]str = ["h3"]
    c_params: [6]u8 = [0x01, 0x04, 0x80, 0x00, 0x75, 0x30]
    s_params: [4]u8 = [0x04, 0x02, 0x40, 0x64]
    var s_done: ?QuicDone = none
    var s_fail := false
    var c_done: ?QuicDone = none
    scope sc := io.Scope() {
        sc.go QkServerTask(h, io, mut server_t, &scfg, s_alpn[..], s_params[..], mut s_done, mut s_fail)
        c_done = some(QuicClient(h, io, mut client_t, &ccfg, "localhost", c_alpn[..], c_params[..]) else e {
            c2s0.Close(io)
            c2s1.Close(io)
            c2s2.Close(io)
            s2c0.Close(io)
            s2c1.Close(io)
            s2c2.Close(io)
            fail e
        })
    }
    Assert(!s_fail)
    cd := c_done else { Panic("missing client done") }
    sd := s_done else { Panic("missing server done") }
    Assert(cd.Alpn() == "h3")
    Assert(sd.Alpn() == "h3")
    Assert(client_t.got_params && client_t.peer_params_len == s_params.Len())
    Assert(crypto.Equal(client_t.peer_params[0..client_t.peer_params_len], s_params[..]))
    Assert(server_t.got_params && server_t.peer_params_len == c_params.Len())
    Assert(crypto.Equal(server_t.peer_params[0..server_t.peer_params_len], c_params[..]))
    Assert(QkSameSecret(&client_t.rsec[1], &server_t.wsec[1]))
    Assert(QkSameSecret(&client_t.wsec[1], &server_t.rsec[1]))
    Assert(QkSameSecret(&client_t.rsec[2], &server_t.wsec[2]))
    Assert(QkSameSecret(&client_t.wsec[2], &server_t.rsec[2]))
    Assert(!client_t.alert_sent && !server_t.alert_sent)
    // Inspect the ClientHello sent at Initial: empty legacy_id (QS-66),
    // TLS 1.3 only (QS-7), no early_data (QS-16), has ALPN (QS-59) and
    // 0x39 (QS-62).
    ch_msg := (ParseHandshake(client_t.sent_init[0..client_t.sent_init_len], 0xFFFFFF) else { Panic("bad CH") }) else {
        Panic("short CH")
    }
    Assert(ch_msg.m.ty == HsClientHello)
    ch := ParseClientHello(ch_msg.m.body) else { Panic("parse CH") }
    Assert(ch.legacy_id.Len() == 0)
    sv := ParseVersionsClient(FindExt(ch.exts, ExtSupportedVersions).Must().Must()).Must()
    Assert(sv.Len() == 2 && U16At(sv, 0) == WireTls13)
    Assert(FindExt(ch.exts, ExtEarlyData).Must().IsNone())
    Assert(FindExt(ch.exts, ExtAlpn).Must().IsSome())
    Assert(FindExt(ch.exts, ExtQuicParams).Must().IsSome())
    // Inspect Handshake-level messages sent by both sides: no
    // CertificateRequest (QS-11), no EndOfEarlyData (QS-16), no KeyUpdate
    // (QS-43).
    var off := 0
    for off < server_t.sent_hs_len {
        m := (ParseHandshake(server_t.sent_hs[off..server_t.sent_hs_len], 0xFFFFFF) else { Panic("bad server HS") }) else {
            Panic("short server HS")
        }
        Assert(m.m.ty != HsCertificateRequest && m.m.ty != HsKeyUpdate)
        off += m.n
    }
    var coff := 0
    for coff < client_t.sent_hs_len {
        m := (ParseHandshake(client_t.sent_hs[coff..client_t.sent_hs_len], 0xFFFFFF) else { Panic("bad client HS") }) else {
            Panic("short client HS")
        }
        Assert(m.m.ty == HsFinished)
        coff += m.n
    }
}

test "QUIC: RFC 9001 A.1 Initial secrets and key, iv, hp derive identically through std.tls Schedule/ExpandLabel" (sys: &System) {
    a := sys.arena
    salt := encoding.HexDecode(a, "38762cf7f55934b34d179ae6a4c80cadccbb7f0a").Must()
    dcid := encoding.HexDecode(a, "8394c8f03e515708").Must()
    init := Extract(crypto.HashKind.Sha256, salt, dcid)
    Assert(encoding.HexEncode(a, init.Bytes()).Must() == "7db5df06e7a69e432496adedb00851923595221596ae2ae9fb8115c1e9ed0a44")
    c_in := DeriveSecret(&init, "client in", b"")
    s_in := DeriveSecret(&init, "server in", b"")
    Assert(encoding.HexEncode(a, c_in.Bytes()).Must() == "c00cf151ca5be075ed0ebfb5c80323c42d6b7db67881289af4008f1f6c357aea")
    Assert(encoding.HexEncode(a, s_in.Bytes()).Must() == "3c199828fd139efd216c155ad844cc81fb82fa8d7446fa7d78be803acdda951b")
    var c_key: [16]u8 = [0; 16]
    var c_iv: [12]u8 = [0; 12]
    var c_hp: [16]u8 = [0; 16]
    ExpandLabel(&c_in, "quic key", b"", mut c_key[..])
    ExpandLabel(&c_in, "quic iv", b"", mut c_iv[..])
    ExpandLabel(&c_in, "quic hp", b"", mut c_hp[..])
    Assert(encoding.HexEncode(a, c_key[..]).Must() == "1f369613dd76d5467730efcbe3b1a22d")
    Assert(encoding.HexEncode(a, c_iv[..]).Must() == "fa044b2f42a3fd3b46fb255c")
    Assert(encoding.HexEncode(a, c_hp[..]).Must() == "9f50449e04a0e810283a1e9933adedd2")
    var s_key: [16]u8 = [0; 16]
    var s_iv: [12]u8 = [0; 12]
    var s_hp: [16]u8 = [0; 16]
    ExpandLabel(&s_in, "quic key", b"", mut s_key[..])
    ExpandLabel(&s_in, "quic iv", b"", mut s_iv[..])
    ExpandLabel(&s_in, "quic hp", b"", mut s_hp[..])
    Assert(encoding.HexEncode(a, s_key[..]).Must() == "cf3a5331653c364c88f0f379b6067e37")
    Assert(encoding.HexEncode(a, s_iv[..]).Must() == "0ac1493ca1905853b0bba03e")
    Assert(encoding.HexEncode(a, s_hp[..]).Must() == "c206b8d9b9f0f37644430b490eeaa314")
}

test "QUIC: RFC 9001 4.1.3: unconsumed handshake bytes at a level when the next level's keys arrive are a PROTOCOL_VIOLATION" (sys: &System) {
    a := sys.arena
    h := sys.heap
    io := sys.io
    certs := (try x509.ParsePem(a, NgLeafPem)).Done()
    blocks := try encoding.ParsePem(a, NgKeyPem)
    key := try crypto.ParsePrivateKey(blocks[0].der)
    roots_certs := (try x509.ParsePem(a, NgRootPem)).Done()
    var roots := x509.Pool.New(roots_certs[..])
    var ccfg := DefaultConfig
    ccfg.roots = some(&roots)
    ids: [1]Identity = [Identity(chain: certs[..], key: &key)]
    var scfg := DefaultConfig
    scfg.identities = ids[..]

    // Case 1: peer appends extra byte in same Initial CRYPTO stream after ServerHello.
    var st := ScriptQuic.New()
    var sh_buf: [128]u8 = [0; 128]
    var swo := Out.New(mut sh_buf[..])
    var ex: [64]u8 = [0; 64]
    var ewo := Out.New(mut ex[..])
    ewo.U16(ExtSupportedVersions)
    ewo.U16(2)
    ewo.U16(WireTls13)
    var sk: [32]u8 = [3; 32]
    pub := crypto.X25519(&sk, &X25519Base)
    ewo.U16(ExtKeyShare)
    el := ewo.Open(2)
    WriteKeyShare(mut ewo, GroupX25519, pub[..])
    ewo.Close(el, 2)
    rand9: [32]u8 = [9; 32]
    sh := ServerHello(version: WireTls12, random: rand9[..], legacy_id: b"", suite: 0x1301, compression: 0, has_exts: true,
        exts: ewo.b[0..ewo.at])
    WriteServerHello(mut swo, &sh)
    // Trailing extra byte in Initial stream.
    swo.U8(0x2a)
    st.Feed(QuicLevel.Initial, swo.b[0..swo.at])
    alpn: [1]str = ["h3"]
    var got_proto := false
    _ = QuicClient(h, io, mut st, &ccfg, "localhost", alpn[..], b"") else e {
        match e {
            Protocol { got_proto = true }
            _ {}
        }
    }
    Assert(got_proto && !st.alert_sent)

    // Case 2: transport Secret itself rejects unread bytes at old level with Protocol.
    var st2 := ScriptQuic.New()
    st2.fail_on_secret = true
    suites: [1]u16 = [0x1301]
    ch := try QkBuildHello(a, b"", suites[..], alpn[..], true, true, false, b"")
    st2.Feed(QuicLevel.Initial, ch)
    var got_proto2 := false
    _ = QuicServer(h, io, mut st2, &scfg, alpn[..], b"") else e {
        match e {
            Protocol { got_proto2 = true }
            _ {}
        }
    }
    Assert(got_proto2 && !st2.alert_sent)
}

test "QUIC: RFC 9001 4.2: a QUIC ClientHello offers only TLS 1.3" (sys: &System) {
    a := sys.arena
    h := sys.heap
    io := sys.io
    roots_certs := (try x509.ParsePem(a, NgRootPem)).Done()
    var roots := x509.Pool.New(roots_certs[..])
    var ccfg := DefaultConfig
    ccfg.roots = some(&roots)
    ccfg.min_version = Version.Tls12
    alpn: [1]str = ["h3"]
    var st := ScriptQuic.New()
    // Server replies with a TLS 1.2 ServerHello (no supported_versions):
    // QuicClient must reject with Unsupported + protocol_version (0x46),
    // never entering client12.mk.
    var sh_buf: [128]u8 = [0; 128]
    var swo := Out.New(mut sh_buf[..])
    rand2: [32]u8 = [2; 32]
    sh := ServerHello(version: WireTls12, random: rand2[..], legacy_id: b"", suite: 0xc02f, compression: 0, has_exts: false,
        exts: b"")
    WriteServerHello(mut swo, &sh)
    st.Feed(QuicLevel.Initial, swo.b[0..swo.at])
    var got_unsup := false
    _ = QuicClient(h, io, mut st, &ccfg, "localhost", alpn[..], b"") else e {
        match e {
            Unsupported { got_unsup = true }
            _ {}
        }
    }
    Assert(got_unsup && st.alert_sent && st.alert_code == AlertProtocolVersion)
    m := (ParseHandshake(st.out_buf[0][0..st.out_len[0]], 0xFFFFFF) else { Panic("bad CH") }) else {
        Panic("short CH")
    }
    ch := ParseClientHello(m.m.body) else { Panic("parse CH") }
    Assert(!Offers12(&Hello(suites: ClientSuites[..], random: [0; 32], sid: [0; 32], sni: "", alpn: alpn[..], group: GroupX25519,
        cookie: b"")))
    sv := ParseVersionsClient(FindExt(ch.exts, ExtSupportedVersions).Must().Must()).Must()
    Assert(sv.Len() == 2 && U16At(sv, 0) == WireTls13)
}

test "QUIC: RFC 9001 4.2: a QUIC ClientHello without TLS 1.3 fails with protocol_version" (sys: &System) {
    a := sys.arena
    h := sys.heap
    io := sys.io
    certs := (try x509.ParsePem(a, NgLeafPem)).Done()
    blocks := try encoding.ParsePem(a, NgKeyPem)
    key := try crypto.ParsePrivateKey(blocks[0].der)
    ids: [1]Identity = [Identity(chain: certs[..], key: &key)]
    var scfg := DefaultConfig
    scfg.identities = ids[..]
    alpn: [1]str = ["h3"]
    suites: [1]u16 = [0x1301]
    ch := try QkBuildHello(a, b"", suites[..], alpn[..], false, true, false, b"")
    var st := ScriptQuic.New()
    st.Feed(QuicLevel.Initial, ch)
    var got_unsup := false
    _ = QuicServer(h, io, mut st, &scfg, alpn[..], b"") else e {
        match e {
            Unsupported { got_unsup = true }
            _ {}
        }
    }
    Assert(got_unsup && st.alert_sent && st.alert_code == AlertProtocolVersion)
}

test "QUIC: RFC 9001 4.4, 4.6.1, 6: post-handshake CertificateRequest, NewSessionTicket early_data and KeyUpdate rules" (sys: &System) {
    // QS-12: post-handshake CertificateRequest is PROTOCOL_VIOLATION (Protocol, not Alert).
    var cr_buf: [32]u8 = [0; 32]
    var cw := Out.New(mut cr_buf[..])
    cr_exts: [8]u8 = [0, 13, 0, 4, 0, 2, 0x04, 0x03]
    cr := CertificateRequest(context: b"", exts: cr_exts[..])
    WriteCertificateRequest(mut cw, &cr)
    var cr_proto := false
    _ = QuicPostMessage(false, cw.b[0..cw.at]) else e {
        match e {
            Protocol { cr_proto = true }
            _ {}
        }
    }
    Assert(cr_proto)

    // QS-13: NewSessionTicket without early_data is dropped; with
    // early_data = 0xffffffff is dropped; with other value is Protocol.
    var nt_buf: [64]u8 = [0; 64]
    var nw := Out.New(mut nt_buf[..])
    nonce1: [1]u8 = [1]
    tkt3: [3]u8 = [2, 3, 4]
    nt_ok := NewTicket(lifetime: 3600, age_add: 1, nonce: nonce1[..], ticket: tkt3[..], exts: b"")
    WriteNewTicket(mut nw, &nt_ok)
    Assert(QuicPostMessage(false, nw.b[0..nw.at]).Must() == nw.at)

    var nw2 := Out.New(mut nt_buf[..])
    ed_max: [8]u8 = [0, 42, 0, 4, 0xff, 0xff, 0xff, 0xff]
    nt_ed_ok := NewTicket(lifetime: 3600, age_add: 1, nonce: nonce1[..], ticket: tkt3[..], exts: ed_max[..])
    WriteNewTicket(mut nw2, &nt_ed_ok)
    Assert(QuicPostMessage(false, nw2.b[0..nw2.at]).Must() == nw2.at)

    var nw3 := Out.New(mut nt_buf[..])
    ed_bad: [8]u8 = [0, 42, 0, 4, 0x00, 0x00, 0x10, 0x00]
    nt_ed_bad := NewTicket(lifetime: 3600, age_add: 1, nonce: nonce1[..], ticket: tkt3[..], exts: ed_bad[..])
    WriteNewTicket(mut nw3, &nt_ed_bad)
    var nt_proto := false
    _ = QuicPostMessage(false, nw3.b[0..nw3.at]) else e {
        match e {
            Protocol { nt_proto = true }
            _ {}
        }
    }
    Assert(nt_proto)

    // QS-44: KeyUpdate is CRYPTO_ERROR 0x010a (Alert unexpected_message).
    var ku_buf: [8]u8 = [0; 8]
    var kw := Out.New(mut ku_buf[..])
    WriteKeyUpdate(mut kw, false)
    var ku_alert: u8 = 0
    _ = QuicPostMessage(false, kw.b[0..kw.at]) else e {
        match e {
            Alert(code) { ku_alert = code }
            _ {}
        }
    }
    Assert(ku_alert == AlertUnexpectedMessage)

    // QS-44 during handshake: QuicServer receiving KeyUpdate instead of ClientHello -> 0x010a.
    certs := (try x509.ParsePem(sys.arena, NgLeafPem)).Done()
    blocks := try encoding.ParsePem(sys.arena, NgKeyPem)
    key := try crypto.ParsePrivateKey(blocks[0].der)
    ids: [1]Identity = [Identity(chain: certs[..], key: &key)]
    var scfg := DefaultConfig
    scfg.identities = ids[..]
    alpn: [1]str = ["h3"]
    var st_ku := ScriptQuic.New()
    st_ku.Feed(QuicLevel.Initial, kw.b[0..kw.at])
    var ku_s_proto := false
    _ = QuicServer(sys.heap, sys.io, mut st_ku, &scfg, alpn[..], b"") else e {
        match e {
            Protocol { ku_s_proto = true }
            _ {}
        }
    }
    Assert(ku_s_proto && st_ku.alert_sent && st_ku.alert_code == AlertUnexpectedMessage)
}

test "QUIC: RFC 9001 5.3: TLS_AES_128_CCM_8_SHA256 is never selected, and a ClientHello offering CCM_8 alongside 0x1301 is accepted" (sys: &System) {
    a := sys.arena
    h := sys.heap
    io := sys.io
    certs := (try x509.ParsePem(a, NgLeafPem)).Done()
    blocks := try encoding.ParsePem(a, NgKeyPem)
    key := try crypto.ParsePrivateKey(blocks[0].der)
    ids: [1]Identity = [Identity(chain: certs[..], key: &key)]
    var scfg := DefaultConfig
    scfg.identities = ids[..]
    alpn: [1]str = ["h3"]

    // QS-28: CCM_8 only (0x1305) is refused with handshake_failure.
    only_ccm8: [1]u16 = [0x1305]
    ch_bad := try QkBuildHello(a, b"", only_ccm8[..], alpn[..], true, true, false, b"")
    var st := ScriptQuic.New()
    st.Feed(QuicLevel.Initial, ch_bad)
    var got_unsup := false
    _ = QuicServer(h, io, mut st, &scfg, alpn[..], b"") else e {
        match e {
            Unsupported { got_unsup = true }
            _ {}
        }
    }
    Assert(got_unsup && st.alert_sent && st.alert_code == AlertHandshakeFailure)

    // QS-29: CCM_8 + unknown + 0x1301 is accepted, choosing 0x1301.
    mixed: [3]u16 = [0x1305, 0x9999, 0x1301]
    ch_ok := try QkBuildHello(a, b"", mixed[..], alpn[..], true, true, false, b"")
    var st2 := ScriptQuic.New()
    st2.Feed(QuicLevel.Initial, ch_ok)
    _ = QuicServer(h, io, mut st2, &scfg, alpn[..], b"") else {}
    Assert(st2.wsec[1].set)
    is_128 := match st2.wsec[1].suite {
        .Aes128Gcm { true }
        _ { false }
    }
    Assert(is_128)

    // QS-28 (client): ServerHello selecting CCM_8 (0x1305) is refused.
    roots_certs := (try x509.ParsePem(a, NgRootPem)).Done()
    var roots := x509.Pool.New(roots_certs[..])
    var ccfg := DefaultConfig
    ccfg.roots = some(&roots)
    var ex: [64]u8 = [0; 64]
    var ewo := Out.New(mut ex[..])
    ewo.U16(ExtSupportedVersions)
    ewo.U16(2)
    ewo.U16(WireTls13)
    var sk: [32]u8 = [4; 32]
    pub := crypto.X25519(&sk, &X25519Base)
    ewo.U16(ExtKeyShare)
    el := ewo.Open(2)
    WriteKeyShare(mut ewo, GroupX25519, pub[..])
    ewo.Close(el, 2)
    rand5: [32]u8 = [5; 32]
    sh_ccm := ServerHello(version: WireTls12, random: rand5[..], legacy_id: b"", suite: 0x1305, compression: 0, has_exts: true,
        exts: ewo.b[0..ewo.at])
    var sh_buf: [128]u8 = [0; 128]
    var swo := Out.New(mut sh_buf[..])
    WriteServerHello(mut swo, &sh_ccm)
    var st_ccm := ScriptQuic.New()
    st_ccm.Feed(QuicLevel.Initial, swo.b[0..swo.at])
    var ccm_proto := false
    _ = QuicClient(h, io, mut st_ccm, &ccfg, "localhost", alpn[..], b"") else e {
        match e {
            Protocol { ccm_proto = true }
            _ {}
        }
    }
    Assert(ccm_proto && st_ccm.alert_sent && st_ccm.alert_code == AlertIllegalParameter)

    // QS-28/29: AES-256-GCM (0x1302, 48-byte SHA-384 secret) and
    // ChaCha20-Poly1305 (0x1303, 32-byte secret) are also supported.
    s256: [1]u16 = [0x1302]
    ch_256 := try QkBuildHello(a, b"", s256[..], alpn[..], true, true, false, b"")
    var st_256 := ScriptQuic.New()
    st_256.Feed(QuicLevel.Initial, ch_256)
    _ = QuicServer(h, io, mut st_256, &scfg, alpn[..], b"") else {}
    Assert(st_256.wsec[1].set && st_256.wsec[1].len == 48)
    scha: [1]u16 = [0x1303]
    ch_cha := try QkBuildHello(a, b"", scha[..], alpn[..], true, true, false, b"")
    var st_cha := ScriptQuic.New()
    st_cha.Feed(QuicLevel.Initial, ch_cha)
    _ = QuicServer(h, io, mut st_cha, &scfg, alpn[..], b"") else {}
    Assert(st_cha.wsec[1].set && st_cha.wsec[1].len == 32)
}

test "QUIC: RFC 9001 4.6.2, 8.1, 8.2, 8.4: mandatory ALPN (0x0178), mandatory 0x39 (0x016d), non-empty legacy_session_id (PROTOCOL_VIOLATION), and no early_data in EE" (sys: &System) {
    a := sys.arena
    h := sys.heap
    io := sys.io
    certs := (try x509.ParsePem(a, NgLeafPem)).Done()
    blocks := try encoding.ParsePem(a, NgKeyPem)
    key := try crypto.ParsePrivateKey(blocks[0].der)
    ids: [1]Identity = [Identity(chain: certs[..], key: &key)]
    var scfg := DefaultConfig
    scfg.identities = ids[..]
    alpn: [1]str = ["h3"]
    suites: [1]u16 = [0x1301]

    // QS-67: non-empty legacy_session_id -> Protocol without alert.
    sid: [4]u8 = [1, 2, 3, 4]
    ch_sid := try QkBuildHello(a, sid[..], suites[..], alpn[..], true, true, false, b"")
    var st_sid := ScriptQuic.New()
    st_sid.Feed(QuicLevel.Initial, ch_sid)
    var sid_proto := false
    _ = QuicServer(h, io, mut st_sid, &scfg, alpn[..], b"") else e {
        match e {
            Protocol { sid_proto = true }
            _ {}
        }
    }
    Assert(sid_proto && !st_sid.alert_sent)

    // QS-63 (server): missing 0x39 -> AlertMissingExtension (0x6d -> 0x016d).
    ch_no39 := try QkBuildHello(a, b"", suites[..], alpn[..], true, false, false, b"")
    var st_no39 := ScriptQuic.New()
    st_no39.Feed(QuicLevel.Initial, ch_no39)
    var no39_proto := false
    _ = QuicServer(h, io, mut st_no39, &scfg, alpn[..], b"") else e {
        match e {
            Protocol { no39_proto = true }
            _ {}
        }
    }
    Assert(no39_proto && st_no39.alert_sent && st_no39.alert_code == AlertMissingExtension)

    // QS-60 (server): missing ALPN -> AlertNoApplicationProtocol (0x78 -> 0x0178).
    no_alpn: [0]str = []
    ch_noalpn := try QkBuildHello(a, b"", suites[..], no_alpn[..], true, true, false, b"")
    var st_noalpn := ScriptQuic.New()
    st_noalpn.Feed(QuicLevel.Initial, ch_noalpn)
    var noalpn_unsup := false
    _ = QuicServer(h, io, mut st_noalpn, &scfg, alpn[..], b"") else e {
        match e {
            Unsupported { noalpn_unsup = true }
            _ {}
        }
    }
    Assert(noalpn_unsup && st_noalpn.alert_sent && st_noalpn.alert_code == AlertNoApplicationProtocol)

    // QS-59: QuicClient with empty alpn refuses before any I/O.
    roots_certs := (try x509.ParsePem(a, NgRootPem)).Done()
    var roots := x509.Pool.New(roots_certs[..])
    var ccfg := DefaultConfig
    ccfg.roots = some(&roots)
    var st_c0 := ScriptQuic.New()
    var c0_unsup := false
    _ = QuicClient(h, io, mut st_c0, &ccfg, "localhost", no_alpn[..], b"") else e {
        match e {
            Unsupported { c0_unsup = true }
            _ {}
        }
    }
    Assert(c0_unsup && st_c0.out_len[0] == 0)

    // QS-60, QS-61, QS-63 (client EncryptedExtensions checks):
    m := Hello(suites: suites[..], random: [0; 32], sid: [0; 32], sni: "", alpn: alpn[..], group: GroupX25519, cookie: b"")
    var alert: u8 = 0
    // Missing 0x39 in EE -> AlertMissingExtension (0x016d).
    ee_no39: [9]u8 = [0, 16, 0, 5, 0, 3, 2, 0x68, 0x33]
    var e1 := false
    _ = CheckEncryptedExtensionsWith(mut alert, ee_no39[..], &m, true) else { e1 = true }
    Assert(e1 && alert == AlertMissingExtension)
    // Missing ALPN in EE -> AlertNoApplicationProtocol (0x0178).
    alert = 0
    ee_noalpn: [4]u8 = [0, 57, 0, 0]
    var e2 := false
    _ = CheckEncryptedExtensionsWith(mut alert, ee_noalpn[..], &m, true) else { e2 = true }
    Assert(e2 && alert == AlertNoApplicationProtocol)
    // QS-61: server selects "h2" when client offered "h3" -> 0x0178.
    alert = 0
    ee_h2: [13]u8 = [0, 16, 0, 5, 0, 3, 2, 0x68, 0x32, 0, 57, 0, 0]
    var e3 := false
    _ = CheckEncryptedExtensionsWith(mut alert, ee_h2[..], &m, true) else { e3 = true }
    Assert(e3 && alert == AlertNoApplicationProtocol)

    // QS-14: server receiving ClientHello with early_data still omits
    // early_data from EncryptedExtensions.
    ch_ed := try QkBuildHello(a, b"", suites[..], alpn[..], true, true, true, b"")
    var st_ed := ScriptQuic.New()
    st_ed.Feed(QuicLevel.Initial, ch_ed)
    _ = QuicServer(h, io, mut st_ed, &scfg, alpn[..], b"") else {}
    ee_msg := (ParseHandshake(st_ed.out_buf[1][0..st_ed.out_len[1]], 0xFFFFFF) else { Panic("bad EE") }) else {
        Panic("short EE")
    }
    Assert(ee_msg.m.ty == HsEncryptedExtensions)
    ee_exts := ParseEncryptedExtensions(ee_msg.m.body) else { Panic("parse EE") }
    Assert(FindExt(ee_exts, ExtEarlyData).Must().IsNone())
    Assert(FindExt(ee_exts, ExtQuicParams).Must().IsSome())
}

test "QUIC: RFC 9001 8.2: quic_transport_parameters is refused over TCP with unsupported_extension" (sys: &System) {
    a := sys.arena
    certs := (try x509.ParsePem(a, NgLeafPem)).Done()
    blocks := try encoding.ParsePem(a, NgKeyPem)
    key := try crypto.ParsePrivateKey(blocks[0].der)
    ids: [1]Identity = [Identity(chain: certs[..], key: &key)]
    var scfg := DefaultConfig
    scfg.identities = ids[..]
    alpn: [1]str = ["h2"]
    suites: [1]u16 = [0x1301]
    // Server over TCP receiving 0x39 -> AlertUnsupportedExtension.
    ch_raw := try QkBuildHello(a, b"", suites[..], alpn[..], true, true, false, b"")
    ch := ParseClientHello(ch_raw[4..]) else { Panic("parse CH") }
    var alert: u8 = 0
    var tcp_s_err := false
    _ = ReadOffer(mut alert, &ch, &scfg, 0) else e {
        match e {
            Protocol { tcp_s_err = true }
            _ {}
        }
    }
    Assert(tcp_s_err && alert == AlertUnsupportedExtension)

    // Client over TCP receiving 0x39 in EncryptedExtensions -> AlertUnsupportedExtension.
    m := Hello(suites: suites[..], random: [0; 32], sid: [0; 32], sni: "", alpn: alpn[..], group: GroupX25519, cookie: b"")
    alert = 0
    ee_39: [4]u8 = [0, 57, 0, 0]
    var tcp_c_err := false
    _ = CheckEncryptedExtensions(mut alert, ee_39[..], &m) else e {
        match e {
            Protocol { tcp_c_err = true }
            _ {}
        }
    }
    Assert(tcp_c_err && alert == AlertUnsupportedExtension)
}

test "QUIC: Config.handshake timeout returns time.TimedOut" (sys: &System) {
    a := sys.arena
    h := sys.heap
    io := sys.io
    roots_certs := (try x509.ParsePem(a, NgRootPem)).Done()
    var roots := x509.Pool.New(roots_certs[..])
    var ccfg := DefaultConfig
    ccfg.roots = some(&roots)
    ccfg.handshake = time.Millis(20)
    alpn: [1]str = ["h3"]
    var st := ScriptQuic.New()
    st.sleep_recv = true
    var timed_out := false
    _ = QuicClient(h, io, mut st, &ccfg, "localhost", alpn[..], b"") else e {
        match e {
            time.TimedOut { timed_out = true }
            _ {}
        }
    }
    Assert(timed_out)
}
