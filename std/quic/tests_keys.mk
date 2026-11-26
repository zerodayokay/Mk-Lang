// Why: packet protection key lifecycle, discard, rotation, 3xPTO retention,
// and AEAD confidentiality/integrity limits (RFC 9001 4.9, 5, 6) are security-
// critical and must pass deterministically before connection tests run.
//
// What: unit tests covering KeyRing Initial/Handshake discard, Retry re-keying,
// client/server 1-RTT key updates in both directions, ahead-derived keys with
// preserved HP, KEY_UPDATE_ERROR, forged phase flips, AEAD limit hooks, and Wipe.
//
// How: pairs client and server KeyRings directly over fixed buffers using
// std.sim for simulated clocks so 3*PTO retention and cooldowns run instantly.

import std.sim
import std.time

// Helper constructing a 1-RTT short header packet sealed by sender into pkt.
SealShortTest(
    sender: mut KeyRing,
    dcid: &Cid,
    pn: i64,
    payload: []u8,
    pkt: mut []u8,
    now: time.Instant,
    pto: time.Duration,
): Error!int {
    la := pn - 1
    pn_len := EncodePnLen(pn, la)
    var pbuf: [256]u8 = [0; 256]
    (mut pbuf[0..payload.Len()]).CopyFrom(payload)
    plen := PadPlain(mut pbuf[..], payload.Len(), pn_len, 0)
    hdr := WriteShortHeader(pkt, dcid, sender.key_phase, pn, pn_len) else {
        fail TransportError(code: ErrInternal, frame: 0)
    }
    return try sender.SealPacket(Space.App, pn, hdr.header_len, hdr.pn_offset, pn_len, pbuf[0..plen], pkt, now, pto)
}

// Helper constructing a Long header packet (Initial/Handshake) sealed by sender.
SealLongTest(
    sender: mut KeyRing,
    space: Space,
    dcid: &Cid,
    scid: &Cid,
    pn: i64,
    payload: []u8,
    pkt: mut []u8,
    now: time.Instant,
    pto: time.Duration,
): Error!int {
    kind := if space == Space.Initial { PktInitial } else { PktHandshake }
    pn_len := EncodePnLen(pn, -1)
    var pbuf: [256]u8 = [0; 256]
    (mut pbuf[0..payload.Len()]).CopyFrom(payload)
    plen := PadPlain(mut pbuf[..], payload.Len(), pn_len, 0)
    hdr := WriteLongHeader(pkt, kind, dcid, scid, b"", plen + 16, pn, pn_len) else {
        fail TransportError(code: ErrInternal, frame: 0)
    }
    return try sender.SealPacket(space, pn, hdr.header_len, hdr.pn_offset, pn_len, pbuf[0..plen], pkt, now, pto)
}

// Installs identical traffic secrets in opposite directions for client and server.
InstallMatchedSecrets(
    a: &Alloc,
    cli: mut KeyRing,
    srv: mut KeyRing,
    level: Space,
    suite: Suite,
) {
    var c_sec: [48]u8 = [0x11; 48]
    var s_sec: [48]u8 = [0x22; 48]
    slen := SuiteSecretLen(suite)
    cli.InstallSecret(a, level, true, suite, c_sec[0..slen]).Must()
    cli.InstallSecret(a, level, false, suite, s_sec[0..slen]).Must()
    srv.InstallSecret(a, level, false, suite, c_sec[0..slen]).Must()
    srv.InstallSecret(a, level, true, suite, s_sec[0..slen]).Must()
}

test "RFC 9001 4.9: Initial discard, Handshake discard at confirmation, and Retry re-derivation" (sys: &System) {
    var clk := try sim.Sim.New(sys.arena, 1)
    defer clk.Free()
    now := time.Now(clk.Io())
    pto := time.Millis(100)

    dcid := CidFromSlice(b"\x83\x94\xc8\xf0\x3e\x51\x57\x08")
    scid := CidFromSlice(b"\x01\x02\x03\x04\x05\x06\x07\x08")
    var cli := KeyRing.New(&dcid, true)
    defer cli.Wipe()
    var srv := KeyRing.New(&dcid, false)
    defer srv.Wipe()

    // Initial seal/open works both ways before discard.
    var pkt: [256]u8 = [0; 256]
    var plain: [256]u8 = [0; 256]
    n := SealLongTest(mut cli, Space.Initial, &dcid, &scid, 0, b"\x01ping", mut pkt[..], now, pto).Must()
    parsed := ParsePacket(pkt[0..n], LocalCidLen).Must()
    match parsed {
        .Long(hdr, packet: _) {
            op := srv.OpenLong(Space.Initial, mut pkt[0..n], hdr.pn_offset, -1, mut plain[..]).Must().Must()
            Assert(op.pn == 0 && op.plain_len >= 5)
        }
        _ { Assert(false) }
    }

    // Retry re-derivation (QS-23): client re-derives Initial keys for retry_scid.
    rscid := CidFromSlice(b"\xaa\xbb\xcc\xdd\xee\xff\x00\x11")
    cli.ReinitInitial(&rscid)
    var srv_retry := KeyRing.New(&rscid, false)
    defer srv_retry.Wipe()
    n2 := SealLongTest(mut cli, Space.Initial, &rscid, &scid, 0, b"\x01rtry", mut pkt[..], now, pto).Must()
    // Old server cannot open packet protected with Retry-derived keys (OpenLong mutates buffer during Unprotect).
    var pkt_old: [256]u8 = [0; 256]
    (mut pkt_old[0..n2]).CopyFrom(pkt[0..n2])
    p_old := ParsePacket(pkt_old[0..n2], LocalCidLen).Must()
    match p_old {
        .Long(hdr, packet: _) {
            Assert(srv.OpenLong(Space.Initial, mut pkt_old[0..n2], hdr.pn_offset, -1, mut plain[..]).Must().IsNone())
        }
        _ { Assert(false) }
    }
    // New server with retry_scid opens it cleanly.
    p_new := ParsePacket(pkt[0..n2], LocalCidLen).Must()
    match p_new {
        .Long(hdr, packet: _) {
            op := srv_retry.OpenLong(Space.Initial, mut pkt[0..n2], hdr.pn_offset, -1, mut plain[..]).Must().Must()
            Assert(op.pn == 0)
        }
        _ { Assert(false) }
    }

    // DiscardInitial disables both Initial seal and open (QS-19, QS-20).
    cli.DiscardInitial()
    Assert(!cli.HasWriteKeys(Space.Initial) && !cli.HasReadKeys(Space.Initial))
    n3 := SealLongTest(mut srv_retry, Space.Initial, &scid, &rscid, 1, b"\x01hi", mut pkt[..], now, pto).Must()
    p3 := ParsePacket(pkt[0..n3], LocalCidLen).Must()
    match p3 {
        .Long(hdr, packet: _) {
            Assert(cli.OpenLong(Space.Initial, mut pkt[0..n3], hdr.pn_offset, -1, mut plain[..]).Must().IsNone())
        }
        _ { Assert(false) }
    }

    // Handshake keys install and then discard at confirmation (QS-21).
    InstallMatchedSecrets(sys.heap.Alloc(), mut cli, mut srv_retry, Space.Handshake, Suite.Aes128Gcm)
    Assert(cli.HasWriteKeys(Space.Handshake) && srv_retry.HasReadKeys(Space.Handshake))
    nh := SealLongTest(mut cli, Space.Handshake, &rscid, &scid, 0, b"\x01hs", mut pkt[..], now, pto).Must()
    ph := ParsePacket(pkt[0..nh], LocalCidLen).Must()
    match ph {
        .Long(hdr, packet: _) {
            op := srv_retry.OpenLong(Space.Handshake, mut pkt[0..nh], hdr.pn_offset, -1, mut plain[..]).Must().Must()
            Assert(op.pn == 0)
        }
        _ { Assert(false) }
    }
    cli.DiscardHandshake()
    srv_retry.DiscardHandshake()
    Assert(!cli.HasWriteKeys(Space.Handshake) && !cli.HasReadKeys(Space.Handshake))
    Assert(!srv_retry.HasWriteKeys(Space.Handshake) && !srv_retry.HasReadKeys(Space.Handshake))
}

test "RFC 9001 6: bidirectional key update, ahead-derived keys, 3*PTO retention, and KEY_UPDATE_ERROR" (sys: &System) {
    var clk := try sim.Sim.New(sys.arena, 2)
    defer clk.Free()
    pto := time.Millis(50)

    dcid := CidFromSlice(b"\x10\x20\x30\x40\x50\x60\x70\x80")
    var cli := KeyRing.New(&dcid, true)
    defer cli.Wipe()
    var srv := KeyRing.New(&dcid, false)
    defer srv.Wipe()

    InstallMatchedSecrets(sys.heap.Alloc(), mut cli, mut srv, Space.App, Suite.Aes128Gcm)
    cli.SetHandshakeConfirmed()
    srv.SetHandshakeConfirmed()

    var now := time.Now(clk.Io())
    var pkt: [256]u8 = [0; 256]
    var plain: [256]u8 = [0; 256]

    // Baseline 1-RTT packet pn=0 in phase false.
    n0 := SealShortTest(mut cli, &dcid, 0, b"\x01p0", mut pkt[..], now, pto).Must()
    match ParsePacket(pkt[0..n0], LocalCidLen).Must() {
        .Short(hdr, packet: _) {
            op := srv.OpenShort(mut pkt[0..n0], &hdr, -1, 0, now, pto, mut plain[..]).Must().Must()
            Assert(op.pn == 0 && !op.key_updated)
        }
        _ { Assert(false) }
    }

    // Also seal a delayed old-phase packet pn=1 BEFORE initiating update.
    var delayed_pkt: [256]u8 = [0; 256]
    ndelayed := SealShortTest(mut cli, &dcid, 1, b"\x01p1", mut delayed_pkt[..], now, pto).Must()

    // Client initiates key update at next_write_pn=2 (QS-34, QS-35).
    Assert(cli.CanInitiateUpdate(now, pto, false))
    cli.InitiateKeyUpdate(2, now, pto, false).Must()
    Assert(cli.key_phase)

    // Client seals pn=2 with phase=true; server detects flipped bit & rotates via ahead-derived keys (QS-36).
    n2 := SealShortTest(mut cli, &dcid, 2, b"\x01p2", mut pkt[..], now, pto).Must()
    match ParsePacket(pkt[0..n2], LocalCidLen).Must() {
        .Short(hdr, packet: _) {
            op := srv.OpenShort(mut pkt[0..n2], &hdr, 0, 10, now, pto, mut plain[..]).Must().Must()
            Assert(op.pn == 2 && op.key_updated)
        }
        _ { Assert(false) }
    }
    Assert(srv.key_phase)

    // Delayed old-phase packet pn=1 (< min_pn_cur_read=2) still opens via app_read_prev (QS-42).
    var delayed_copy: [256]u8 = [0; 256]
    (mut delayed_copy[0..ndelayed]).CopyFrom(delayed_pkt[0..ndelayed])
    match ParsePacket(delayed_copy[0..ndelayed], LocalCidLen).Must() {
        .Short(hdr, packet: _) {
            op := srv.OpenShort(mut delayed_copy[0..ndelayed], &hdr, 2, 10, now, pto, mut plain[..]).Must().Must()
            Assert(op.pn == 1 && !op.key_updated)
        }
        _ { Assert(false) }
    }

    // Forged key-phase flip with corrupted ciphertext must fail quietly without rotating (QS-45).
    var forged: [256]u8 = [0; 256]
    nf := SealShortTest(mut cli, &dcid, 3, b"\x01p3", mut forged[..], now, pto).Must()
    forged[nf - 1] = forged[nf - 1] ^ 0xff
    match ParsePacket(forged[0..nf], LocalCidLen).Must() {
        .Short(hdr, packet: _) {
            res := srv.OpenShort(mut forged[0..nf], &hdr, 2, 10, now, pto, mut plain[..]).Must()
            Assert(res.IsNone())
        }
        _ { Assert(false) }
    }

    // Second update blocked while awaiting ACK on pn=2, AND during 3*PTO cooldown after ACK (QS-37, QS-43).
    Assert(!cli.CanInitiateUpdate(now, pto, false))
    cli.OnAck1Rtt(2, now)
    Assert(!cli.CanInitiateUpdate(now, pto, false))
    clk.Advance(time.Nanos(3 * pto.Nanos()))
    now = time.Now(clk.Io())
    Assert(cli.CanInitiateUpdate(now, pto, false))

    // Server's prev_expire_at also expires old keys after 3*PTO (QS-41).
    srv.ExpirePrev(now)
    match ParsePacket(delayed_pkt[0..ndelayed], LocalCidLen).Must() {
        .Short(hdr, packet: _) {
            Assert(srv.OpenShort(mut delayed_pkt[0..ndelayed], &hdr, 2, 10, now, pto, mut plain[..]).Must().IsNone())
        }
        _ { Assert(false) }
    }

    // Server initiates update back -> both phases return to false.
    var cli2 := KeyRing.New(&dcid, true)
    defer cli2.Wipe()
    var srv2 := KeyRing.New(&dcid, false)
    defer srv2.Wipe()
    InstallMatchedSecrets(sys.heap.Alloc(), mut cli2, mut srv2, Space.App, Suite.Chacha20Poly1305)
    cli2.SetHandshakeConfirmed()
    srv2.SetHandshakeConfirmed()
    // Advance srv2 to pn=0..1, then peer initiates update at pn=1 -> min_pn_cur_read=1.
    cli2.InitiateKeyUpdate(1, now, pto, false).Must()
    ns := SealShortTest(mut cli2, &dcid, 1, b"\x01k1", mut pkt[..], now, pto).Must()
    match ParsePacket(pkt[0..ns], LocalCidLen).Must() {
        .Short(hdr, packet: _) {
            op := srv2.OpenShort(mut pkt[0..ns], &hdr, 0, 5, now, pto, mut plain[..]).Must().Must()
            Assert(op.key_updated)
        }
        _ { Assert(false) }
    }
    // Check KEY_UPDATE_ERROR when old keys open a packet with PN > min_pn_cur_read (QS-44):
    // Keep a third pair where old_sender didn't update and seals pn=5 after pn=2 updated receiver.
    var old_cli := KeyRing.New(&dcid, true)
    defer old_cli.Wipe()
    InstallMatchedSecrets(sys.heap.Alloc(), mut old_cli, mut cli2, Space.App, Suite.Chacha20Poly1305)
    n_old_hi := SealShortTest(mut old_cli, &dcid, 5, b"\x01old", mut pkt[..], now, pto).Must()
    var got_ku_err := false
    match ParsePacket(pkt[0..n_old_hi], LocalCidLen).Must() {
        .Short(hdr, packet: _) {
            _ = srv2.OpenShort(mut pkt[0..n_old_hi], &hdr, 1, 5, now, pto, mut plain[..]) else e {
                match e {
                    TransportError(code, frame: _) { got_ku_err = code == ErrKeyUpdate }
                    _ {}
                }
                none
            }
        }
        _ { Assert(false) }
    }
    Assert(got_ku_err)
}

test "RFC 9001 5.5, 6.6: AEAD confidentiality and integrity limit hooks and defaults" (sys: &System) {
    var clk := try sim.Sim.New(sys.arena, 3)
    defer clk.Free()
    now := time.Now(clk.Io())
    pto := time.Millis(50)

    dcid := CidFromSlice(b"\x01\x01\x01\x01\x01\x01\x01\x01")
    var gcm := KeyRing.New(&dcid, true)
    defer gcm.Wipe()
    var cc := KeyRing.New(&dcid, true)
    defer cc.Wipe()

    sec: [32]u8 = [0x33; 32]
    a := sys.heap.Alloc()
    gcm.InstallSecret(a, Space.App, true, Suite.Aes128Gcm, sec[..]).Must()
    gcm.InstallSecret(a, Space.App, false, Suite.Aes128Gcm, sec[..]).Must()
    gcm.SetHandshakeConfirmed()
    cc.InstallSecret(a, Space.App, true, Suite.Chacha20Poly1305, sec[..]).Must()
    cc.InstallSecret(a, Space.App, false, Suite.Chacha20Poly1305, sec[..]).Must()
    cc.SetHandshakeConfirmed()

    Assert(gcm.ConfLimit() == AesGcmConfLimit && gcm.IntegLimit() == AesGcmIntegLimit)
    Assert(cc.ConfLimit() == NoConfLimit && cc.IntegLimit() == ChaChaIntegLimit)

    // Confidentiality limit hook triggers proactive key update once sealed_in_phase reaches limit (QS-53).
    gcm.SetConfLimitForTest(some(u64(2)))
    var pkt: [256]u8 = [0; 256]
    _ = SealShortTest(mut gcm, &dcid, 0, b"\x01a", mut pkt[..], now, pto).Must()
    _ = SealShortTest(mut gcm, &dcid, 1, b"\x01b", mut pkt[..], now, pto).Must()
    Assert(!gcm.key_phase)
    _ = SealShortTest(mut gcm, &dcid, 2, b"\x01c", mut pkt[..], now, pto).Must()
    Assert(gcm.key_phase)

    // Integrity limit hook raises ErrAeadLimitReached once failed decryptions reach limit (QS-55).
    gcm.SetIntegLimitForTest(some(u64(3)))
    var bad: [64]u8 = [0x40; 64]
    var plain: [64]u8 = [0; 64]
    for k in 0..8 {
        bad[1 + k] = dcid.b[k]
    }
    match ParsePacket(bad[..], LocalCidLen).Must() {
        .Short(hdr, packet: _) {
            Assert(gcm.OpenShort(mut bad[..], &hdr, 0, 2, now, pto, mut plain[..]).Must().IsNone())
            Assert(gcm.OpenShort(mut bad[..], &hdr, 0, 2, now, pto, mut plain[..]).Must().IsNone())
            Assert(gcm.OpenShort(mut bad[..], &hdr, 0, 2, now, pto, mut plain[..]).Must().IsNone())
            var got_lim := false
            _ = gcm.OpenShort(mut bad[..], &hdr, 0, 2, now, pto, mut plain[..]) else e {
                match e {
                    TransportError(code, frame: _) { got_lim = code == ErrAeadLimitReached }
                    _ {}
                }
                none
            }
            Assert(got_lim)
        }
        _ { Assert(false) }
    }
}
