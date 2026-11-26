// Why: stateless Retry tokens and RFC 9001 5.8 integrity tags prevent DoS
// amplification and off-path Retry spoofing without server per-Retry memory.
//
// What: unit tests for MakeRetryToken, VerifyRetryToken (valid, wrong IP, wrong
// port, expired, tampered, non-Retry prefix), and WriteRetryPacket round-trip.
//
// How: constructs Retry tokens over simulated clocks and verifies ParsePacket +
// VerifyRetryTag in both success and failure cases.

import std.sim
import std.time

IsInvalidRetryRes(r: RetryTokenResult): bool {
    return match r {
        .InvalidRetry { true }
        _ { false }
    }
}

IsNotRetryRes(r: RetryTokenResult): bool {
    return match r {
        .NotRetry { true }
        _ { false }
    }
}

test "RFC 9000 8.1: Retry token seal, verify, address binding, expiry, tampering, and unknown prefix" (sys: &System) {
    var clk := try sim.Sim.New(sys.arena, 1)
    defer clk.Free()
    io := clk.Io()

    var key := try RetryTokenKey.New(io)
    defer key.Wipe()

    cli := TestV4(192, 0, 2, 10, 12345)
    odcid := CidFromSlice(b"\x83\x94\xc8\xf0\x3e\x51\x57\x08")
    now_ns := InstantNanos(time.Now(io))

    var tok: [MaxRetryTokenLen]u8 = [0; MaxRetryTokenLen]
    tlen := MakeRetryToken(io, &key, &cli, &odcid, now_ns, mut tok[..])
    Assert(tlen > 0 && tlen <= MaxRetryTokenLen)
    Assert(tok[0] == RetryTokenMarker)

    // Valid token recovers exact odcid (QT-273, QT-274).
    match VerifyRetryToken(&key, tok[0..tlen], &cli, now_ns + 1_000_000_000, RetryTokenMaxAgeNs) {
        .Valid(odcid: got) { Assert(CidEqual(&got, &odcid)) }
        _ { Assert(false) }
    }

    // Wrong IP or wrong port -> InvalidRetry (QT-276).
    bad_ip := TestV4(192, 0, 2, 11, 12345)
    bad_port := TestV4(192, 0, 2, 10, 12346)
    Assert(IsInvalidRetryRes(VerifyRetryToken(&key, tok[0..tlen], &bad_ip, now_ns, RetryTokenMaxAgeNs)))
    Assert(IsInvalidRetryRes(VerifyRetryToken(&key, tok[0..tlen], &bad_port, now_ns, RetryTokenMaxAgeNs)))

    // Expired token (> 10s) -> InvalidRetry (QT-275).
    Assert(IsInvalidRetryRes(VerifyRetryToken(&key, tok[0..tlen], &cli, now_ns + RetryTokenMaxAgeNs + 1, RetryTokenMaxAgeNs)))

    // Tampered ciphertext -> InvalidRetry (QT-277).
    var bad_tok: [MaxRetryTokenLen]u8 = [0; MaxRetryTokenLen]
    (mut bad_tok[0..tlen]).CopyFrom(tok[0..tlen])
    bad_tok[tlen - 1] = bad_tok[tlen - 1] ^ 0x01
    Assert(IsInvalidRetryRes(VerifyRetryToken(&key, bad_tok[0..tlen], &cli, now_ns, RetryTokenMaxAgeNs)))

    // Non-Retry marker prefix -> NotRetry (QT-278).
    var unk: [16]u8 = [0x01; 16]
    Assert(IsNotRetryRes(VerifyRetryToken(&key, unk[..], &cli, now_ns, RetryTokenMaxAgeNs)))

    // WriteRetryPacket produces valid RFC 9001 5.8 tagged Retry packet (QT-279).
    var pkt: [256]u8 = [0; 256]
    cli_scid := CidFromSlice(b"\x01\x02\x03\x04")
    srv_scid := CidFromSlice(b"\xaa\xbb\xcc\xdd\xee\xff\x11\x22")
    plen := WriteRetryPacket(mut pkt[..], &cli_scid, &srv_scid, tok[0..tlen], &odcid)
    Assert(plen > 0)
    match ParsePacket(pkt[0..plen], LocalCidLen).Must() {
        .Retry(version, dcid, scid, token: ptok, tag, packet: raw_pkt) {
            Assert(version == Version1)
            Assert(CidEqual(&dcid, &cli_scid) && CidEqual(&scid, &srv_scid))
            Assert(ptok.Len() == tlen)
            Assert(VerifyRetryTag(&odcid, raw_pkt, &tag))
            Assert(!VerifyRetryTag(&cli_scid, raw_pkt, &tag))
        }
        _ { Assert(false) }
    }
}
