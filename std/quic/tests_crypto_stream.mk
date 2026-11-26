// Why: CRYPTO streams run independently from offset 0 per encryption level,
// cap out-of-order buffering at 64 KiB, retransmit lost data, and free
// Initial/Handshake state when keys are discarded.
//
// What: unit tests for retransmission and out-of-order CRYPTO reassembly.
//
// How: exercises CryptoStreams directly across Space.Initial, Space.Handshake,
// and Space.App.

TCryDummy(): SentFrame {
    return SentFrame(kind: SentKind.Control(index: 0))
}

test "RFC 9000 7.5: CRYPTO data past 64 KiB out of order at one level is CRYPTO_BUFFER_EXCEEDED" (sys: &System) {
    h := sys.heap.Alloc()
    var cs := CryptoStreams.New(h)
    defer cs.Free(h)

    // Exactly at 65,536 bound (offset 65535, len 1 -> end 65536) succeeds.
    try cs.OnCrypto(h, Space.Initial, 65535, "a".AsBytes())
    // One byte past 65,536 fails with CRYPTO_BUFFER_EXCEEDED.
    var hit_err := false
    cs.OnCrypto(h, Space.Initial, 65536, "b".AsBytes()) else e {
        match e {
            TransportError(code, frame: _) {
                Assert(code == ErrCryptoBufferExceeded)
                hit_err = true
            }
            _ { Assert(false) }
        }
    }
    Assert(hit_err)
    // Independent levels: Handshake still has its own 64 KiB budget.
    try cs.OnCrypto(h, Space.Handshake, 65535, "c".AsBytes())
}

test "RFC 9000 19.6: each level's CRYPTO stream starts at 0, and a discarded CRYPTO frame's packet is ACKed" (sys: &System) {
    h := sys.heap.Alloc()
    var cs := CryptoStreams.New(h)
    defer cs.Free(h)

    try cs.OnCrypto(h, Space.Initial, 0, "init".AsBytes())
    try cs.OnCrypto(h, Space.Handshake, 0, "hs".AsBytes())
    try cs.OnCrypto(h, Space.App, 0, "nst".AsBytes())

    var b0: [8]u8 = [0; 8]
    var b1: [8]u8 = [0; 8]
    var b2: [8]u8 = [0; 8]
    Assert(cs.Read(Space.Initial, mut b0[..]) == 4 && T3Eq(b0[0..4], "init".AsBytes()))
    Assert(cs.Read(Space.Handshake, mut b1[..]) == 2 && T3Eq(b1[0..2], "hs".AsBytes()))
    Assert(cs.Read(Space.App, mut b2[..]) == 3 && T3Eq(b2[0..3], "nst".AsBytes()))

    // Out-of-order reassembly at Handshake.
    try cs.OnCrypto(h, Space.Handshake, 4, "lo".AsBytes())
    Assert(cs.Read(Space.Handshake, mut b1[..]) == 0)
    try cs.OnCrypto(h, Space.Handshake, 2, "ll".AsBytes())
    Assert(cs.Read(Space.Handshake, mut b1[..]) == 4 && T3Eq(b1[0..4], "lllo".AsBytes()))

    // Discarded CRYPTO level safely absorbs late frames without error so the packet can be ACKed.
    cs.Discard(Space.Initial)
    try cs.OnCrypto(h, Space.Initial, 4, "late".AsBytes())
    Assert(cs.Read(Space.Initial, mut b0[..]) == 0)
}

test "RFC 9000 13.3: CRYPTO data is resent until acked and freed with its level's keys" (sys: &System) {
    h := sys.heap.Alloc()
    var cs := CryptoStreams.New(h)
    defer cs.Free(h)

    try cs.Queue(h, Space.Initial, "ClientHello".AsBytes())
    var out1: [64]u8 = [0; 64]
    var out2: [64]u8 = [0; 64]
    var fs: [4]SentFrame = [TCryDummy(); 4]

    r1 := cs.Next(Space.Initial, mut out1[..], 64, mut fs[..])
    Assert(r1.frames == 1)
    ft := T23ReadVarint(out1[0..r1.n], 0).Must()
    Assert(ft.v == 0x06)

    // Loss queues identical retransmission.
    cs.OnLost(h, &fs[0])
    r2 := cs.Next(Space.Initial, mut out2[..], 64, mut fs[..])
    Assert(r2.frames == 1 && r2.n == r1.n && T3Eq(out1[0..r1.n], out2[0..r2.n]))

    // Ack clears send buffer; subsequent Next emits nothing.
    cs.OnAcked(h, &fs[0])
    Assert(cs.Next(Space.Initial, mut out1[..], 64, mut fs[..]).frames == 0)

    // Queued Handshake data is dropped when Handshake keys are discarded.
    try cs.Queue(h, Space.Handshake, "Finished".AsBytes())
    cs.Discard(Space.Handshake)
    Assert(cs.Next(Space.Handshake, mut out1[..], 64, mut fs[..]).frames == 0)
}
