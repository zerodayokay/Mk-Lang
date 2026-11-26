// Why: QUIC's TLS 1.3 handshake drives encryption-level transitions,
// validates peer transport parameters and connection IDs (RFC 9000 7.3),
// authenticates parameters only on completion (RFC 9001 8.2), and turns TLS
// alerts into QUIC CRYPTO_ERROR connection closes.
//
// What: HandshakeWire (implementing tls.QuicTransport), ValidatePeerParams,
// and MapTlsError / MapTransportError.
//
// How: HandshakeWire borrows &QConn for the duration of QConn.Handshake;
// Recv waits via lead.Wait on CRYPTO bytes at the requested level, Send queues
// CRYPTO frames and flushes, Secret installs keys (rejecting unread bytes from
// the prior level per RFC 9001 4.1.3), and Alert queues CONNECTION_CLOSE 0x1c
// with 0x0100 + code.

import std.crypto
import std.net
import std.time
import std.tls
import std.x509

// Maps a tls.QuicLevel to std.quic's Space enum. O(1).
SpaceOfQuicLevel(l: tls.QuicLevel): Space {
    return match l {
        .Initial { Space.Initial }
        .Handshake { Space.Handshake }
        .Application { Space.App }
    }
}

// Validates raw peer transport parameters and enforces RFC 9000 7.3 CID
// authentication (QT-239..QT-243). Fails with TransportError(ErrTransportParameter, 0)
// on any missing or mismatched CID parameter. O(raw.Len()).
ValidatePeerParams(
    is_server: bool,
    raw: []u8,
    orig_dcid: &Cid,
    retry_scid: ?Cid,
    peer_init_scid: ?Cid,
): Error!Params {
    p := try DecodeParams(raw, is_server)
    iscid := p.initial_scid else {
        fail TransportError(code: ErrTransportParameter, frame: 0)
    }
    exp_iscid := peer_init_scid else {
        fail TransportError(code: ErrTransportParameter, frame: 0)
    }
    if !CidEqual(&iscid, &exp_iscid) {
        fail TransportError(code: ErrTransportParameter, frame: 0)
    }
    if !is_server {
        odcid := p.original_dcid else {
            fail TransportError(code: ErrTransportParameter, frame: 0)
        }
        if !CidEqual(&odcid, orig_dcid) {
            fail TransportError(code: ErrTransportParameter, frame: 0)
        }
        match retry_scid {
            some(exp_r) {
                got_r := p.retry_scid else {
                    fail TransportError(code: ErrTransportParameter, frame: 0)
                }
                if !CidEqual(&got_r, &exp_r) {
                    fail TransportError(code: ErrTransportParameter, frame: 0)
                }
            }
            none {
                if p.retry_scid.IsSome() {
                    fail TransportError(code: ErrTransportParameter, frame: 0)
                }
            }
        }
    }
    return p
}

// Maps an internal QUIC Error to net.Error for external callers. O(1).
MapQuicError(e: &Error, peer: bool): net.Error {
    return match *e {
        TransportError(code, frame: _) { net.MuxClosed(code: code, app: false, peer: peer) }
        OutOfMemory { OutOfMemory }
        Closed { Closed }
        net.StreamReset(code) { net.StreamReset(code: code) }
        _ { net.SecureFailed(why: net.SecureFailure.Protocol) }
    }
}

// Maps a std.tls Error from QuicClient / QuicServer to net.Error. O(1).
MapTlsError(e: &tls.Error): net.Error {
    return match *e {
        tls.Alert | tls.Protocol | tls.Unsupported { net.SecureFailed(why: net.SecureFailure.Handshake) }
        x509.UnknownAuthority { net.SecureFailed(why: net.SecureFailure.UnknownAuthority) }
        x509.Expired { net.SecureFailed(why: net.SecureFailure.Expired) }
        x509.NameMismatch { net.SecureFailed(why: net.SecureFailure.NameMismatch) }
        x509.Malformed | x509.BadSignature | x509.BadUsage | x509.TooComplex | x509.Unsupported {
            net.SecureFailed(why: net.SecureFailure.BadCertificate)
        }
        OutOfMemory { OutOfMemory }
        net.Refused { net.Refused }
        net.Reset { net.Reset }
        net.AddrInUse { net.AddrInUse }
        net.Unreachable { net.Unreachable }
        net.InvalidAddress { net.InvalidAddress }
        net.Os(code) { net.Os(code: code) }
        Canceled { Canceled }
        Closed { Closed }
        time.TimedOut { time.TimedOut }
        net.Unsupported { net.Unsupported }
        net.SecureFailed(why) { net.SecureFailed(why: why) }
        net.StreamReset(code) { net.StreamReset(code: code) }
        net.MuxClosed(code, app, peer) { net.MuxClosed(code: code, app: app, peer: peer) }
    }
}

// Adapter implementing tls.QuicTransport for one QConn handshake run.
struct HandshakeWire {
    conn: &QConn
    party: int
    is_server: bool
}

impl tls.QuicTransport for HandshakeWire {
    Recv(mut self, io: Io, level: tls.QuicLevel, buf: mut []u8): tls.Error!int {
        sp := SpaceOfQuicLevel(level)
        return try self.conn.HsRecv(io, self.party, sp, buf)
    }

    Send(mut self, io: Io, level: tls.QuicLevel, data: []u8): tls.Error!void {
        sp := SpaceOfQuicLevel(level)
        try self.conn.HsSend(io, sp, data)
    }

    Secret(mut self, level: tls.QuicLevel, write: bool, suite: tls.QuicSuite, secret: []u8): tls.Error!void {
        sp := SpaceOfQuicLevel(level)
        try self.conn.HsSecret(sp, write, KeySuiteOf(suite), secret)
    }

    PeerParams(mut self, params: []u8): tls.Error!void {
        try self.conn.HsPeerParams(params)
    }

    Alert(mut self, io: Io, code: u8) {
        self.conn.HsAlert(io, code)
    }
}

// Owned copy of a server tls.Config's cert DER bytes and keys on EndpointShared's Heap.
const MaxOwnedIdentities: int = 8
const MaxOwnedCerts: int = 32

struct CertSpan {
    at: int
    len: int
}

struct IdSpan {
    cert_start: int
    cert_count: int
    key: crypto.PrivateKey
}

linear struct OwnedTlsCfg {
    cert_bytes: HeapList[u8]
    cert_spans: [MaxOwnedCerts]CertSpan
    cert_count: int
    id_spans: [MaxOwnedIdentities]IdSpan
    id_count: int
    handshake: time.Duration
    close: time.Duration
}

OwnedTlsCfg.New(a: &Alloc, src: &tls.Config): OutOfMemory!OwnedTlsCfg from a {
    var bytes := HeapList[u8].New(a)
    onerr bytes.Free(a)
    var cspans: [MaxOwnedCerts]CertSpan = [CertSpan(at: 0, len: 0); MaxOwnedCerts]
    var ccount := 0
    dummy_k := crypto.PrivateKey.Ed25519(seed: [0; 32])
    var ispans: [MaxOwnedIdentities]IdSpan = [IdSpan(cert_start: 0, cert_count: 0, key: dummy_k); MaxOwnedIdentities]
    var icount := 0
    for i in 0..src.identities.Len() {
        if icount >= MaxOwnedIdentities {
            break
        }
        id := &src.identities[i]
        start := ccount
        var cnt := 0
        for j in 0..id.chain.Len() {
            if ccount >= MaxOwnedCerts {
                break
            }
            der := id.chain[j].data
            at := bytes.Len()
            try bytes.Extend(a, der)
            cspans[ccount] = CertSpan(at: at, len: der.Len())
            ccount += 1
            cnt += 1
        }
        ispans[icount] = IdSpan(cert_start: start, cert_count: cnt, key: *id.key)
        icount += 1
    }
    return OwnedTlsCfg(
        cert_bytes: bytes,
        cert_spans: cspans,
        cert_count: ccount,
        id_spans: ispans,
        id_count: icount,
        handshake: src.handshake,
        close: src.close,
    )
}

OwnedTlsCfg.Free(self, a: &Alloc) {
    OwnedTlsCfg(cert_bytes, ..) := self
    cert_bytes.Free(a)
}

// Reconstructs stack tls.Config from owned DER/keys and runs tls.QuicServer.
OwnedTlsCfg.RunQuicServer(
    &self,
    h: &Heap,
    io: Io,
    wire: mut HandshakeWire,
    alpn: []str,
    params: []u8,
): tls.Error!tls.QuicDone {
    if self.id_count == 0 || self.cert_count == 0 {
        var tc := tls.DefaultConfig
        tc.handshake = self.handshake
        tc.close = self.close
        return try tls.QuicServer(h, io, wire, &tc, alpn, params)
    }
    all_b := self.cert_bytes.Items()
    var scratch := try Arena.New(h)
    defer scratch.Free()
    sp0 := self.cert_spans[0]
    first_c := try x509.Parse(&scratch, all_b[sp0.at..sp0.at + sp0.len])
    var certs: [MaxOwnedCerts]x509.Certificate = [first_c; MaxOwnedCerts]
    for i in 1..self.cert_count {
        sp := self.cert_spans[i]
        certs[i] = try x509.Parse(&scratch, all_b[sp.at..sp.at + sp.len])
    }
    var ids: [MaxOwnedIdentities]tls.Identity = [tls.Identity(chain: certs[0..0], key: &self.id_spans[0].key); MaxOwnedIdentities]
    for i in 0..self.id_count {
        isp := &self.id_spans[i]
        ids[i] = tls.Identity(
            chain: certs[isp.cert_start..isp.cert_start + isp.cert_count],
            key: &isp.key,
        )
    }
    var tc := tls.DefaultConfig
    tc.identities = ids[0..self.id_count]
    tc.handshake = self.handshake
    tc.close = self.close
    return try tls.QuicServer(h, io, wire, &tc, alpn, params)
}
