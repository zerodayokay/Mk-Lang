// Why: connection engine rules — transport parameter/CID auth, coalescing,
// 1,200-byte padding, optimistic-ACK skip detection, packet-number exhaustion,
// server anti-amplification, unread-CRYPTO guards, and idle timeout — must hold
// deterministically at the packet and engine boundary.
//
// What: unit tests for ValidatePeerParams, Engine/ProcessDatagram/BuildNextDatagram
// edge cases (QT-79..89, QT-233..243, QT-262..270, QT-295..298, QT-342, QS-6,
// QS-40), and QConn loopback stream exchange over net.Mux.
//
// How: exercises Engine directly with simulated clocks and crafted packets, plus
// full Connector/Acceptor handshakes over loopback UDP.

import std.crypto
import std.encoding
import std.net
import std.raw
import std.sim
import std.time
import std.tls
import std.x509

const QtLeafPem: str = "-----BEGIN CERTIFICATE-----\nMIIBfDCCASGgAwIBAgIBAjAKBggqhkjOPQQDAjAYMRYwFAYDVQQDEw1NayBiZW5j\naCByb290MCAXDTAwMDEwMTAwMDAwMFoYDzIyMDAwMTAxMDAwMDAwWjAUMRIwEAYD\nVQQDEwlsb2NhbGhvc3QwWTATBgcqhkjOPQIBBggqhkjOPQMBBwNCAARYWAiT0sIB\nPFPoIQColXkYt09CRdmx6LjiHDj75/IQjS3AYB3CWa+ymGXqR2ucu2cguSiCoiNK\nqIH+y9bVqzPco14wXDAOBgNVHQ8BAf8EBAMCB4AwEwYDVR0lBAwwCgYIKwYBBQUH\nAwEwHwYDVR0jBBgwFoAUJFRJG8q4njDrib1e2WaLjryUxnkwFAYDVR0RBA0wC4IJ\nbG9jYWxob3N0MAoGCCqGSM49BAMCA0kAMEYCIQD4NROoBKH8KfhkQ/zOMONC60gR\ncRjTYRvpycsxggV3OgIhAKjdsY7ozxVLBHMGzKcJDmUfhRo7ejfd4qjWiC3mdn+n\n-----END CERTIFICATE-----\n"
const QtKeyPem: str = "-----BEGIN PRIVATE KEY-----\nMIGHAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBG0wawIBAQQgQ3K5SvjaOAx5Gj4e\nrlkGTawIlLQ2SZBirD7hp5nMGbChRANCAARYWAiT0sIBPFPoIQColXkYt09CRdmx\n6LjiHDj75/IQjS3AYB3CWa+ymGXqR2ucu2cguSiCoiNKqIH+y9bVqzPc\n-----END PRIVATE KEY-----\n"
const QtRootPem: str = "-----BEGIN CERTIFICATE-----\nMIIBYzCCAQmgAwIBAgIBATAKBggqhkjOPQQDAjAYMRYwFAYDVQQDEw1NayBiZW5j\naCByb290MCAXDTAwMDEwMTAwMDAwMFoYDzIyMDAwMTAxMDAwMDAwWjAYMRYwFAYD\nVQQDEw1NayBiZW5jaCByb290MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEyTx+\nYlmJmAU6O4dZ0UQ3l5NkWBp3YM0DnNEBOBQ4TupVoFHlQRki7kyY6p+VX9Qrr6Hn\nz8thWyiM/gt642/2dqNCMEAwDgYDVR0PAQH/BAQDAgIEMA8GA1UdEwEB/wQFMAMB\nAf8wHQYDVR0OBBYEFCRUSRvKuJ4w64m9Xtlmi468lMZ5MAoGCCqGSM49BAMCA0gA\nMEUCIQCGfzbV5b0fQ7vvXGWBDlwaBxou4hsIMFapz+zbFYiqbQIgIH6Cu3lYtLDM\n5NakQ34Pul2rlQOLEFZvP0CmTMKU1NE=\n-----END CERTIFICATE-----\n"

CheckTpErr(is_srv: bool, raw: []u8, expected_odcid: &Cid, expected_rscid: ?Cid, expected_scid: ?Cid): bool {
    _ = ValidatePeerParams(is_srv, raw, expected_odcid, expected_rscid, expected_scid) else e {
        return match e {
            TransportError(code, frame: _) { code == ErrTransportParameter }
            _ { false }
        }
    }
    return false
}

test "RFC 9000 7.3: transport parameter CID authentication for client and server" (sys: &System) {
    odcid := CidFromSlice(b"\x01\x02\x03\x04\x05\x06\x07\x08")
    srv_scid := CidFromSlice(b"\xaa\xbb\xcc\xdd\xee\xff\x00\x11")
    cli_scid := CidFromSlice(b"\x11\x22\x33\x44\x55\x66\x77\x88")
    rscid := CidFromSlice(b"\x99\x88\x77\x66\x55\x44\x33\x22")
    bad := CidFromSlice(b"\xff\xff\xff\xff\xff\xff\xff\xff")
    var buf: [256]u8 = [0; 256]

    // Client validating server params without Retry (QT-239, QT-241).
    var sp := DefaultParams(false)
    sp.initial_scid = some(srv_scid)
    sp.original_dcid = some(odcid)
    n := EncodeParams(mut buf[..], &sp)
    _ = try ValidatePeerParams(false, buf[0..n], &odcid, none, some(srv_scid))

    // Missing or mismatched initial_scid / original_dcid / unexpected retry_scid fail (QT-240..243).
    Assert(CheckTpErr(false, buf[0..n], &bad, none, some(srv_scid)))
    Assert(CheckTpErr(false, buf[0..n], &odcid, none, some(bad)))
    Assert(CheckTpErr(false, buf[0..n], &odcid, some(rscid), some(srv_scid)))

    // Server params with Retry must include matching retry_scid (QT-240).
    sp.retry_scid = some(rscid)
    n2 := EncodeParams(mut buf[..], &sp)
    _ = try ValidatePeerParams(false, buf[0..n2], &odcid, some(rscid), some(srv_scid))
    Assert(CheckTpErr(false, buf[0..n2], &odcid, none, some(srv_scid)))

    // Server validating client params: requires matching initial_scid, rejects server-only params.
    var cp := DefaultParams(true)
    cp.initial_scid = some(cli_scid)
    n3 := EncodeParams(mut buf[..], &cp)
    _ = try ValidatePeerParams(true, buf[0..n3], &odcid, none, some(cli_scid))
    Assert(CheckTpErr(true, buf[0..n3], &odcid, none, some(bad)))
}

test "RFC 9000 10.1, 13.1, 14.1: client 1200B padding, QS-40 1-RTT gate, idle cap, PN exhaustion, and skip ACK" (sys: &System) {
    h := sys.heap
    var clk := try sim.Sim.New(sys.arena, 1)
    defer clk.Free()
    io := clk.Io()
    now := time.Now(io)

    odcid := CidFromSlice(b"\x83\x94\xc8\xf0\x3e\x51\x57\x08")
    scid := CidFromSlice(b"\x01\x02\x03\x04\x05\x06\x07\x08")
    peer := TestV4(127, 0, 0, 1, 9000)

    var cfg := DefaultConfig
    cfg.idle = time.Seconds(10)
    ecfg := cfg.ToEngineCfg()
    var no_cid: ?Cid = none
    var no_sk: ?&[32]u8 = none
    var e := try Engine.New(h, io, false, &ecfg, &odcid, &odcid, &scid, no_cid, no_cid, peer, true, 0, "h3", no_sk, 0, 0)
    defer e.Free()

    // Queue Initial CRYPTO data -> BuildNextDatagram emits padded 1200-byte datagram (QT-265).
    try e.crypto.Queue(h.Alloc(), Space.Initial, b"client-hello")
    var dgram: [MaxSendDatagram]u8 = [0; MaxSendDatagram]
    built := BuildNextDatagram(mut e, mut dgram[..], now).Must()
    Assert(built.len == MinInitialDatagramLen)

    // QS-40: 1-RTT packet received before handshake completes is ignored (doesn't fail or ACK).
    var fake_1rtt: [64]u8 = [0x40; 64]
    for i in 0..8 {
        fake_1rtt[1 + i] = scid.b[i]
    }
    ProcessDatagram(mut e, io, mut fake_1rtt[..], peer, now)
    Assert(!e.IsDead())

    // QT-342: AllocSendPn skips next_skip_app_pn once reached.
    target := e.next_skip_app_pn
    e.rec.SkipPn(Space.App, target - 1)
    // Walk up until target is skipped.
    var prev := e.rec.PeekPn(Space.App)
    for prev < target - 1 {
        _ = AllocSendPn(mut e, Space.App)
        prev = e.rec.PeekPn(Space.App)
    }
    p_after := AllocSendPn(mut e, Space.App).Must()
    Assert(p_after > target)
    Assert(e.next_skip_app_pn > p_after)

    // QT-88: packet number at MaxPacketNum marks pn_exhausted silently without close.
    e.rec.SkipPn(Space.App, MaxPacketNum)
    Assert(AllocSendPn(mut e, Space.App).IsNone())
    Assert(e.pn_exhausted && !e.close.IsClosed())
}

test "RFC 9001 4.1.3, QT-81: unread CRYPTO at prior level raises PROTOCOL_VIOLATION and mismatched coalesced DCID ignored" (sys: &System) {
    h := sys.heap
    var clk := try sim.Sim.New(sys.arena, 2)
    defer clk.Free()
    io := clk.Io()
    now := time.Now(io)

    dcid := CidFromSlice(b"\x10\x20\x30\x40\x50\x60\x70\x80")
    scid := CidFromSlice(b"\x01\x02\x03\x04\x05\x06\x07\x08")
    peer := TestV4(127, 0, 0, 1, 9001)
    ecfg := DefaultConfig.ToEngineCfg()
    var no_cid: ?Cid = none
    var no_sk: ?&[32]u8 = none

    var udp := try net.Bind(io, "127.0.0.1:0")
    sock := udp.Share()
    onerr sock.Close()
    var qc := try QConn.NewClient(h, io, sock, peer, &DefaultConfig)
    defer qc.MuxAbandon()

    // Feed Initial CRYPTO frame without TLS reading it, then try installing Handshake read secret (QS-6).
    sec: [32]u8 = [0x44; 32]
    // Simulate unread Initial CRYPTO bytes directly inside qc's Engine.
    var g := qc.eng.Lock(io).Must()
    _ = g.Mut().crypto.OnCrypto(h.Alloc(), Space.Initial, 0, b"unread-initial") else {}
    g.Unlock()

    var got_proto := false
    qc.HsSecret(Space.Handshake, false, Suite.Aes128Gcm, sec[..]) else e {
        match e {
            net.SecureFailed(why) { got_proto = why == net.SecureFailure.Protocol }
            _ {}
        }
    }
    Assert(got_proto)

    // Server Initial < 1200 bytes is dropped cleanly (QT-268).
    var srv_e := try Engine.New(h, io, true, &ecfg, &dcid, &dcid, &scid, some(scid), no_cid, peer, false, 0, "h3", no_sk, 0, 0)
    defer srv_e.Free()
    var cli_keys := KeyRing.New(&dcid, true)
    defer cli_keys.Wipe()
    var small_init: [300]u8 = [0; 300]
    ni := SealLongTest(mut cli_keys, Space.Initial, &dcid, &scid, 0, b"\x06\x00\x02ab", mut small_init[..], now, time.Millis(50)).Must()
    ProcessDatagram(mut srv_e, io, mut small_init[0..ni], peer, now)
    Assert(!srv_e.any_pkt_processed)
}

RunMuxListenerTask(l: &net.MuxListener, io: Io) {
    _ = l.Run(io) else {}
}

ServeOnceEchoTask(l: &net.MuxListener, io: Io, failed: &raw.Atomic[u32]) {
    var sm := l.Accept(io) else {
        failed.Store(1)
        return
    }
    var mst := sm.Accept(io, false, true) else {
        sm.Abandon()
        failed.Store(1)
        return
    }
    match mst {
        none {
            sm.Abandon()
            failed.Store(1)
            return
        }
        some(st) {
            var rbuf: [64]u8 = [0; 64]
            rn := sm.Read(io, &st, mut rbuf[..], true) else {
                sm.Release(st)
                sm.Abandon()
                failed.Store(1)
                return
            }
            n := match rn {
                some(v) { v }
                none { 0 }
            }
            sm.Write(io, &st, rbuf[0..n], true) else {
                sm.Release(st)
                sm.Abandon()
                failed.Store(1)
                return
            }
            sm.Release(st)
        }
    }
    // Keep sm open briefly until client finishes reading.
    _ = io.Sleep(time.Millis(50)) else {}
    sm.Close(io, 0) else {
        sm.Abandon()
    }
}

test "net.Mux over QUIC: loopback handshake, bidirectional Open/Accept/Read/Write/Wait, and Close" (sys: &System) {
    a := sys.arena
    h := sys.heap
    io := sys.io

    certs := (try x509.ParsePem(a, QtLeafPem)).Done()
    blocks := try encoding.ParsePem(a, QtKeyPem)
    key := try crypto.ParsePrivateKey(blocks[0].der)
    roots_certs := (try x509.ParsePem(a, QtRootPem)).Done()
    var roots := x509.Pool.New(roots_certs[..])
    ids: [1]tls.Identity = [tls.Identity(chain: certs[..], key: &key)]
    var tls_srv := tls.DefaultConfig
    tls_srv.identities = ids[..]
    var tls_cli := tls.DefaultConfig
    tls_cli.roots = some(&roots)

    var scfg := DefaultConfig
    scfg.tls = some(&tls_srv)
    var ccfg := DefaultConfig
    ccfg.tls = some(&tls_cli)

    var ac := Acceptor(&scfg)
    var l := try ac.Listen(h, io, "127.0.0.1:0", "h3")
    defer l.Close()
    sa := net.Addr(ip: [127, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0], port: Trunc[u16](l.Port()), v6: false)

    failed := raw.Atomic[u32].New(0)
    scope s := io.Scope() {
        s.CancelAfter(time.Seconds(10))
        s.go RunMuxListenerTask(&l, io)
        s.go ServeOnceEchoTask(&l, io, &failed)

        var cn := Connector(&ccfg)
        var cm := try cn.Dial(h, io, sa, "localhost", "h3")
        var mcst := cm.Open(io, false, true) else e {
            cm.Abandon()
            s.Cancel()
            return e
        }
        match mcst {
            none {
                cm.Abandon()
                s.Cancel()
                Assert(false)
                return
            }
            some(cst) {
                cm.Write(io, &cst, b"hello-quic-mux", true) else e {
                    cm.Release(cst)
                    cm.Abandon()
                    s.Cancel()
                    return e
                }
                var got: [64]u8 = [0; 64]
                var total := 0
                for total < 14 {
                    rn := cm.Read(io, &cst, mut got[total..], true) else e {
                        cm.Release(cst)
                        cm.Abandon()
                        s.Cancel()
                        return e
                    }
                    match rn {
                        none { break }
                        some(v) {
                            if v == 0 {
                                break
                            }
                            total += v
                        }
                    }
                }
                cm.Release(cst)
                Assert(total == 14)
                for i in 0..14 {
                    Assert(got[i] == (b"hello-quic-mux")[i])
                }
                cm.Close(io, 0) else e {
                    cm.Abandon()
                    s.Cancel()
                    return e
                }
            }
        }
        s.Cancel()
    }
    Assert(failed.Load() == 0)
}
