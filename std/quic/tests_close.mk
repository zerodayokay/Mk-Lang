// Why: QUIC closing/draining periods and stateless-reset limits (RFC 9000 10.2,
// 10.3) prevent reflection floods, stale connection hangs, and reset storms.
//
// What: unit tests for CloseState (Closing rate limit, unvalidated address
// drop, keyless 3x amplification cap, Draining silence, 3*PTO expiry),
// ResetLimiter (64/sec bucket + refill), and WriteStatelessReset (length & tail).
//
// How: steps simulated clocks with std.sim and verifies every state transition.

import std.net
import std.sim
import std.time

TestV4(a: u8, b: u8, c: u8, d: u8, port: u16): net.Addr {
    return net.Addr(ip: [a, b, c, d, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0], port: port, v6: false)
}

test "RFC 9000 10.2: CloseState Closing rate limit, keyless 3x cap, unvalidated drop, Draining, expiry" (sys: &System) {
    var clk := try sim.Sim.New(sys.arena, 1)
    defer clk.Free()
    io := clk.Io()
    var now := time.Now(io)
    pto := time.Millis(100)

    a_val := TestV4(10, 0, 0, 1, 4444)
    a_unval := TestV4(10, 0, 0, 2, 5555)

    var cs := CloseState.New()
    Assert(!cs.IsClosed() && cs.Due().IsNone())
    cs.Close(42, true, 0, now, pto)
    Assert(cs.IsClosed() && cs.Due().IsSome())
    cs.SetValidatedAddr(a_val)
    cs.SetKeyless(true)

    // Unvalidated source address never gets close reply (QT-328).
    Assert(!cs.Answer(&a_unval, 100, 60))

    // Validated address rate-limited to odd packets (1st true, 2nd false, 3rd true) (QT-326).
    Assert(cs.Answer(&a_val, 30, 60))
    Assert(!cs.Answer(&a_val, 30, 60))
    // 3rd packet triggers keyless 3x amplification check: total rx=90 -> max tx=270.
    // Already tx=60; reply 250 would be 310 > 270 -> blocked (QT-327).
    Assert(!cs.Answer(&a_val, 30, 250))

    // Before 3*PTO (300 ms): not expired; at 3*PTO: expired (QT-329).
    clk.Advance(time.Millis(299))
    Assert(!cs.Expired(time.Now(io)))
    clk.Advance(time.Millis(1))
    Assert(cs.Expired(time.Now(io)))

    // Draining state never answers any packet (QT-329).
    var dr := CloseState.New()
    dr.OnPeerClose(0, false, time.Now(io), pto)
    Assert(dr.IsClosed())
    Assert(!dr.Answer(&a_val, 1200, 60))
}

test "RFC 9000 10.3: ResetLimiter 64/sec cap and WriteStatelessReset framing" (sys: &System) {
    var clk := try sim.Sim.New(sys.arena, 2)
    defer clk.Free()
    io := clk.Io()
    var rl := ResetLimiter.New()
    now := time.Now(io)

    // Burst of 64 allowed; 65th blocked (QT-335).
    for _ in 0..64 {
        Assert(rl.Allow(now))
    }
    Assert(!rl.Allow(now))

    // 500 ms refills 32 tokens.
    clk.Advance(time.Millis(500))
    now2 := time.Now(io)
    for _ in 0..32 {
        Assert(rl.Allow(now2))
    }
    Assert(!rl.Allow(now2))

    // WriteStatelessReset produces strictly shorter packet with token in last 16 bytes (QT-334).
    tok: [16]u8 = [0xfe; 16]
    var out: [64]u8 = [0; 64]
    n := WriteStatelessReset(io, mut out[..], &tok, 45)
    Assert(n >= MinShortPacketLen && n < 45)
    Assert((out[0] & 0xc0) == 0x40)
    for i in 0..16 {
        Assert(out[n - 16 + i] == 0xfe)
    }
    // Too-short trigger packet (< 22) produces no reset.
    Assert(WriteStatelessReset(io, mut out[..], &tok, 21) == 0)
}
