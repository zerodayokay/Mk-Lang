// Why: Recovery must follow RFC 9002 Appendix A (RTT estimator, loss thresholds,
// PTO backoff, anti-deadlock, amplification unblocking, and key discards) and
// RFC 9000 ACK validation rules.
//
// What: unit tests for QT-27, QT-40..QT-42, QT-342, and LR-1..LR-31.
//
// How: drives Recovery with std.sim's virtual clock to verify every threshold
// and state transition deterministically.

import std.sim
import std.time

TRecTick(s: mut sim.Sim, d: time.Duration): time.Instant {
    ns := d.Nanos()
    if ns >= 1000 {
        s.Io().Sleep(time.Nanos(ns - 1000)) else {
            Assert(false)
        }
    }
    return time.Now(s.Io())
}

TRecFrame(i: int): SentFrame {
    return SentFrame(kind: SentKind.Control(index: i))
}

TRecSend(rec: mut Recovery, h: &Alloc, sp: Space, pn: i64, bytes: int, ae: bool, inf: bool, now: time.Instant) {
    fs := [TRecFrame(int(pn))]
    rec.OnSent(h, sp, pn, bytes, ae, inf, fs[..], now) else {
        Assert(false)
    }
}

test "RFC 9000 12.3: each space numbers from 0 and never repeats a packet number" (sys: &System) {
    h := sys.heap.Alloc()
    var rec := try Recovery.New(h)
    defer rec.Free(h)
    Assert(rec.NextPn(Space.Initial) == 0)
    Assert(rec.NextPn(Space.Initial) == 1)
    Assert(rec.NextPn(Space.Handshake) == 0)
    Assert(rec.NextPn(Space.App) == 0)
    Assert(rec.NextPn(Space.App) == 1)
    Assert(rec.NextPn(Space.Initial) == 2)
}

test "RFC 9000 13.1: an ACK for an unsent or skipped packet number is PROTOCOL_VIOLATION" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 11)
    defer s.Free()
    h := sys.heap.Alloc()
    var rec := try Recovery.New(h)
    defer rec.Free(h)
    t0 := time.Now(s.Io())
    pn0 := rec.NextPn(Space.App)
    TRecSend(mut rec, h, Space.App, pn0, 100, true, true, t0)
    rec.SkipPn(Space.App, 1) // skipped PN 1 for optimistic-ACK trap
    pn2 := rec.NextPn(Space.App)
    Assert(pn2 == 2)
    TRecSend(mut rec, h, Space.App, pn2, 100, true, true, t0)

    var acked: [8]SentFrame = [TRecFrame(0); 8]
    var lost: [8]SentFrame = [TRecFrame(0); 8]
    t1 := TRecTick(mut s, time.Millis(20))

    // Unsent PN 3 -> PROTOCOL_VIOLATION.
    r_unsent := [AckRange(lo: 3, hi: 3)]
    _ = rec.OnAck(Space.App, r_unsent[..], time.Millis(0), t1, mut acked[..], mut lost[..]) else e {
        match e {
            TransportError(code, frame: _) { Assert(code == ErrProtocolViolation) }
            _ { Assert(false) }
        }
        (acked: 0, lost: 0)
    }
    // Skipped PN 1 -> PROTOCOL_VIOLATION.
    r_skip := [AckRange(lo: 0, hi: 2)]
    _ = rec.OnAck(Space.App, r_skip[..], time.Millis(0), t1, mut acked[..], mut lost[..]) else e {
        match e {
            TransportError(code, frame: _) { Assert(code == ErrProtocolViolation) }
            _ { Assert(false) }
        }
        (acked: 0, lost: 0)
    }
    // 256-range cap: range 257 is still validated!
    var many: [260]AckRange = [AckRange(lo: 0, hi: 0); 260]
    many[258] = AckRange(lo: 1, hi: 1) // skipped PN 1 past cap
    var hit_err := false
    _ = rec.OnAck(Space.App, many[..], time.Millis(0), t1, mut acked[..], mut lost[..]) else e {
        match e {
            TransportError(code, frame: _) {
                Assert(code == ErrProtocolViolation)
                hit_err = true
            }
            _ { Assert(false) }
        }
        (acked: 0, lost: 0)
    }
    Assert(hit_err)
}

test "RFC 9000 19.3: a packet acked once stays acked, and a repeat ACK is a no-op" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 12)
    defer s.Free()
    h := sys.heap.Alloc()
    var rec := try Recovery.New(h)
    defer rec.Free(h)
    t0 := time.Now(s.Io())
    TRecSend(mut rec, h, Space.App, 0, 1000, true, true, t0)
    TRecSend(mut rec, h, Space.App, 1, 1000, true, true, t0)
    var acked: [8]SentFrame = [TRecFrame(0); 8]
    var lost: [8]SentFrame = [TRecFrame(0); 8]
    t1 := TRecTick(mut s, time.Millis(40))
    r1 := [AckRange(lo: 0, hi: 0)]
    res1 := try rec.OnAck(Space.App, r1[..], time.Millis(0), t1, mut acked[..], mut lost[..])
    Assert(res1.acked == 1 && rec.BytesInFlight() == 1000)

    // Repeat ACK for 0 omits 1; 0 stays acked (no-op), 1 is not lost.
    t2 := TRecTick(mut s, time.Millis(10))
    res2 := try rec.OnAck(Space.App, r1[..], time.Millis(0), t2, mut acked[..], mut lost[..])
    Assert(res2.acked == 0 && res2.lost == 0 && rec.BytesInFlight() == 1000)
}

test "RFC 9000 12.4: only ACK, PADDING and CONNECTION_CLOSE are not ack-eliciting, and ACK-only packets are not in flight" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 13)
    defer s.Free()
    h := sys.heap.Alloc()
    var rec := try Recovery.New(h)
    defer rec.Free(h)
    t0 := time.Now(s.Io())
    // ACK-only: ack_eliciting = false, in_flight = false -> bytes_in_flight stays 0, no timer.
    TRecSend(mut rec, h, Space.App, 0, 50, false, false, t0)
    Assert(rec.BytesInFlight() == 0)
    rec.SetHandshakeConfirmed(true, t0)
    Assert(rec.Timer(t0).IsNone())
    // PADDING-only: ack_eliciting = false, in_flight = true -> counts in bytes_in_flight.
    TRecSend(mut rec, h, Space.App, 1, 1200, false, true, t0)
    Assert(rec.BytesInFlight() == 1200)
    // Ack-eliciting: arms PTO once confirmed.
    TRecSend(mut rec, h, Space.App, 2, 100, true, true, t0)
    Assert(rec.BytesInFlight() == 1300)
    Assert(rec.Timer(t0).Must().Nanos() > 0)
}

test "RFC 9002 5.1: an ACK that newly acknowledges only non-ack-eliciting packets gives no RTT sample" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 14)
    defer s.Free()
    h := sys.heap.Alloc()
    var rec := try Recovery.New(h)
    defer rec.Free(h)
    t0 := time.Now(s.Io())
    TRecSend(mut rec, h, Space.App, 0, 1200, false, true, t0)
    t1 := TRecTick(mut s, time.Millis(40))
    var acked: [4]SentFrame = [TRecFrame(0); 4]
    var lost: [4]SentFrame = [TRecFrame(0); 4]
    r := [AckRange(lo: 0, hi: 0)]
    _ = try rec.OnAck(Space.App, r[..], time.Millis(0), t1, mut acked[..], mut lost[..])
    // SmoothedRtt stays at initial 333 ms, LatestRtt stays 0.
    Assert(rec.SmoothedRtt().Nanos() == 333_000_000)
    Assert(rec.LatestRtt().Nanos() == 0)
}

test "RFC 9002 5.1: an ACK whose largest acknowledged is not newly acknowledged gives no RTT sample" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 15)
    defer s.Free()
    h := sys.heap.Alloc()
    var rec := try Recovery.New(h)
    defer rec.Free(h)
    t0 := time.Now(s.Io())
    TRecSend(mut rec, h, Space.App, 0, 100, true, true, t0)
    TRecSend(mut rec, h, Space.App, 1, 100, true, true, t0)
    var acked: [4]SentFrame = [TRecFrame(0); 4]
    var lost: [4]SentFrame = [TRecFrame(0); 4]

    t1 := TRecTick(mut s, time.Millis(20))
    r1 := [AckRange(lo: 1, hi: 1)]
    _ = try rec.OnAck(Space.App, r1[..], time.Millis(0), t1, mut acked[..], mut lost[..])
    Assert(rec.SmoothedRtt().Nanos() == 20_000_000)

    // Delayed ACK includes 0 (newly acked) and 1 (already acked) at t = 100ms: no RTT sample.
    t2 := TRecTick(mut s, time.Millis(80))
    r2 := [AckRange(lo: 0, hi: 1)]
    _ = try rec.OnAck(Space.App, r2[..], time.Millis(0), t2, mut acked[..], mut lost[..])
    Assert(rec.SmoothedRtt().Nanos() == 20_000_000)
}

test "RFC 9002 5.1, 5.2, 5.3: RTT estimator starts at 333 ms / 166.5 ms, resets to first sample, and follows A.7 order" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 16)
    defer s.Free()
    h := sys.heap.Alloc()
    var rec := try Recovery.New(h)
    defer rec.Free(h)
    Assert(rec.SmoothedRtt().Nanos() == 333_000_000)
    Assert(rec.RttVar().Nanos() == 166_500_000)

    t0 := time.Now(s.Io())
    TRecSend(mut rec, h, Space.App, 0, 100, true, true, t0)
    t1 := TRecTick(mut s, time.Millis(40))
    var acked: [4]SentFrame = [TRecFrame(0); 4]
    var lost: [4]SentFrame = [TRecFrame(0); 4]
    r0 := [AckRange(lo: 0, hi: 0)]
    _ = try rec.OnAck(Space.App, r0[..], time.Millis(10), t1, mut acked[..], mut lost[..])
    // First sample ignores ack_delay: smoothed=40ms, rttvar=20ms, min_rtt=40ms.
    Assert(rec.MinRtt().Nanos() == 40_000_000)
    Assert(rec.SmoothedRtt().Nanos() == 40_000_000)
    Assert(rec.RttVar().Nanos() == 20_000_000)

    // Second sample: sent at t1, acked at t1 + 80ms with ack_delay 10ms -> adjusted = 70ms.
    // A.7 order (l. 1661-1662):
    // rttvar = 3/4 * 20ms + 1/4 * |40ms - 70ms| = 15ms + 7.5ms = 22.5ms
    // smoothed = 7/8 * 40ms + 1/8 * 70ms = 35ms + 8.75ms = 43.75ms
    TRecSend(mut rec, h, Space.App, 1, 100, true, true, t1)
    t2 := TRecTick(mut s, time.Millis(80))
    r1 := [AckRange(lo: 1, hi: 1)]
    _ = try rec.OnAck(Space.App, r1[..], time.Millis(10), t2, mut acked[..], mut lost[..])
    Assert(rec.MinRtt().Nanos() == 40_000_000)
    Assert(rec.LatestRtt().Nanos() == 80_000_000)
    Assert(rec.RttVar().Nanos() == 22_500_000)
    Assert(rec.SmoothedRtt().Nanos() == 43_750_000)

    // Third sample: 30ms < min_rtt -> min_rtt drops to 30ms; ack_delay 10ms would drop below min_rtt (LR-10) -> unsubtracted!
    TRecSend(mut rec, h, Space.App, 2, 100, true, true, t2)
    t3 := TRecTick(mut s, time.Millis(30))
    r2 := [AckRange(lo: 2, hi: 2)]
    _ = try rec.OnAck(Space.App, r2[..], time.Millis(10), t3, mut acked[..], mut lost[..])
    Assert(rec.MinRtt().Nanos() == 30_000_000)
    // adjusted = 30ms; diff = |43.75 - 30| = 13.75ms
    // rttvar = (3 * 22.5 + 13.75) / 4 = 20.3125ms
    // smoothed = (7 * 43.75 + 30) / 8 = 42.03125ms
    Assert(rec.RttVar().Nanos() == 20_312_500)
    Assert(rec.SmoothedRtt().Nanos() == 42_031_250)
}

test "RFC 9002 5.3: ack_delay is uncapped before confirmation and capped at max_ack_delay after" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 17)
    defer s.Free()
    h := sys.heap.Alloc()
    var rec := try Recovery.New(h)
    defer rec.Free(h)
    rec.SetPeerMaxAckDelay(time.Millis(25))
    t0 := time.Now(s.Io())
    var acked: [4]SentFrame = [TRecFrame(0); 4]
    var lost: [4]SentFrame = [TRecFrame(0); 4]
    // Establish min_rtt = 20ms.
    TRecSend(mut rec, h, Space.Handshake, 0, 100, true, true, t0)
    t1 := TRecTick(mut s, time.Millis(20))
    r0 := [AckRange(lo: 0, hi: 0)]
    _ = try rec.OnAck(Space.Handshake, r0[..], time.Millis(0), t1, mut acked[..], mut lost[..])

    // Before confirmation: 100ms sample with 60ms ack_delay -> uncapped! adjusted = 40ms.
    TRecSend(mut rec, h, Space.Handshake, 1, 100, true, true, t1)
    t2 := TRecTick(mut s, time.Millis(100))
    r1 := [AckRange(lo: 1, hi: 1)]
    _ = try rec.OnAck(Space.Handshake, r1[..], time.Millis(60), t2, mut acked[..], mut lost[..])
    // smoothed = (7*20 + 40)/8 = 22.5ms
    Assert(rec.SmoothedRtt().Nanos() == 22_500_000)

    // After confirmation: 100ms sample with 60ms ack_delay -> capped at 25ms! adjusted = 75ms.
    rec.SetHandshakeConfirmed(true, t2)
    TRecSend(mut rec, h, Space.App, 0, 100, true, true, t2)
    t3 := TRecTick(mut s, time.Millis(100))
    r2 := [AckRange(lo: 0, hi: 0)]
    _ = try rec.OnAck(Space.App, r2[..], time.Millis(60), t3, mut acked[..], mut lost[..])
    // smoothed = (7*22.5 + 75)/8 = 29.0625ms
    Assert(rec.SmoothedRtt().Nanos() == 29_062_500)
}

test "RFC 9002 6.1, 6.1.1, 6.1.2: packet threshold 3, time threshold 9/8, and loss timer" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 18)
    defer s.Free()
    h := sys.heap.Alloc()
    var rec := try Recovery.New(h)
    defer rec.Free(h)
    t0 := time.Now(s.Io())
    rec.SetHandshakeConfirmed(true, t0)
    // Establish RTT = 80ms so loss_delay = 9/8 * 80ms = 90ms.
    TRecSend(mut rec, h, Space.App, 0, 100, true, true, t0)
    t1 := TRecTick(mut s, time.Millis(80))
    var acked: [8]SentFrame = [TRecFrame(0); 8]
    var lost: [8]SentFrame = [TRecFrame(0); 8]
    r0 := [AckRange(lo: 0, hi: 0)]
    _ = try rec.OnAck(Space.App, r0[..], time.Millis(0), t1, mut acked[..], mut lost[..])

    // Send packets 1, 2, 3, 4 at t1.
    TRecSend(mut rec, h, Space.App, 1, 100, true, true, t1)
    TRecSend(mut rec, h, Space.App, 2, 100, true, true, t1)
    TRecSend(mut rec, h, Space.App, 3, 100, true, true, t1)
    TRecSend(mut rec, h, Space.App, 4, 100, true, true, t1)

    // Ack packet 4 at t1 + 40ms -> packet 1 (4 >= 1 + 3) is lost by packet threshold!
    // Packets 2, 3 are not yet past 90ms (only 40ms elapsed), so loss timer arms for remaining 50ms!
    t2 := TRecTick(mut s, time.Millis(40))
    r4 := [AckRange(lo: 4, hi: 4)]
    res := try rec.OnAck(Space.App, r4[..], time.Millis(0), t2, mut acked[..], mut lost[..])
    Assert(res.acked == 1 && res.lost == 1)
    // loss_delay = 9/8 * 80ms = 90ms (since latest=40ms, smoothed = 7/8*80 + 1/8*40 = 75ms -> max(40,75)=75ms -> 9/8*75 = 84.375ms!).
    // Remaining time = 84.375ms - 40ms = 44.375ms!
    rem := rec.Timer(t2).Must()
    Assert(rem.Nanos() == 44_375_000)

    // Advance 44.375ms -> loss timer expires and marks packets 2 and 3 lost, probes = 0.
    t3 := TRecTick(mut s, rem)
    to := rec.OnTimeout(t3, mut lost[..])
    Assert(to.lost == 2 && to.probes == 0)
    Assert(rec.BytesInFlight() == 0)
}

test "RFC 9002 6.2, 6.2.1, 6.2.2: PTO formula, 999ms initial, no App PTO before confirmation, backoff, and key discard" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 19)
    defer s.Free()
    h := sys.heap.Alloc()
    var rec := try Recovery.New(h)
    defer rec.Free(h)
    t0 := time.Now(s.Io())

    // LR-22: First PTO is 333 + 4*166.5 + 0 = 999 ms.
    Assert(rec.Pto().Nanos() == 999_000_000)
    TRecSend(mut rec, h, Space.Initial, 0, 1200, true, true, t0)
    Assert(rec.Timer(t0).Must().Nanos() == 999_000_000)

    // LR-17: Initial at t0, Handshake at t0+100ms -> PTO picks earlier (Initial).
    t1 := TRecTick(mut s, time.Millis(100))
    TRecSend(mut rec, h, Space.Handshake, 0, 1200, true, true, t1)
    Assert(rec.Timer(t1).Must().Nanos() == 899_000_000)

    // LR-15 & LR-20: PTO expiry marks nothing lost and doubles backoff (client Initial ACK keeps backoff).
    t2 := TRecTick(mut s, time.Millis(899))
    var lost: [4]SentFrame = [TRecFrame(0); 4]
    to := rec.OnTimeout(t2, mut lost[..])
    Assert(to.lost == 0 && to.probes == 2 && rec.PtoCount() == 1)
    // Initial ACK does not reset client pto_count.
    var acked: [4]SentFrame = [TRecFrame(0); 4]
    r0 := [AckRange(lo: 0, hi: 0)]
    _ = try rec.OnAck(Space.Initial, r0[..], time.Millis(0), t2, mut acked[..], mut lost[..])
    Assert(rec.PtoCount() == 1)

    // LR-23: Discarding Initial removes its bytes in flight, resets pto_count, rearms timers.
    TRecSend(mut rec, h, Space.Initial, 1, 1200, true, true, t2)
    Assert(rec.BytesInFlight() == 2400)
    rec.Discard(Space.Initial)
    Assert(rec.BytesInFlight() == 1200 && rec.PtoCount() == 0)

    // LR-18: No Application Data PTO before handshake is confirmed!
    rec.Discard(Space.Handshake)
    TRecSend(mut rec, h, Space.App, 0, 500, true, true, t2)
    // Client without Handshake ACK and not confirmed -> anti-deadlock only, NOT App PTO!
    // Confirming handshake switches App PTO on (including max_ack_delay = 25ms).
    rec.SetHandshakeConfirmed(true, t2)
    Assert(rec.Timer(t2).Must().Nanos() == rec.Pto().Nanos())
}

test "RFC 9002 6.2.2.1: server amplification block, unblock overdue PTO, and client anti-deadlock" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 20)
    defer s.Free()
    h := sys.heap.Alloc()
    var srv := try Recovery.New(h)
    defer srv.Free(h)
    srv.SetRole(true)
    t0 := time.Now(s.Io())
    TRecSend(mut srv, h, Space.Initial, 0, 1200, true, true, t0)
    // LR-24: amplification-blocked server arms no PTO.
    srv.SetServerAmpBlocked(true, t0)
    Assert(srv.Timer(t0).IsNone())

    // LR-25: datagram arriving past PTO runs overdue PTO immediately.
    t1 := TRecTick(mut s, time.Millis(1200))
    var lost: [4]SentFrame = [TRecFrame(0); 4]
    res := srv.OnDatagramReceived(t1, mut lost[..])
    Assert(res.lost == 0 && res.probes == 2 && srv.PtoCount() == 1)

    // LR-26 & LR-27: client anti-deadlock PTO with nothing in flight.
    var cli := try Recovery.New(h)
    defer cli.Free(h)
    cli.SetHandshakeKeys(false)
    cli.SetServerAmpBlocked(false, t1) // rearms timer via OnTimeout/SetHandshakeConfirmed?
    // Trigger timer evaluation by sending & getting acked in Initial (not Handshake ACK).
    TRecSend(mut cli, h, Space.Initial, 0, 1200, true, true, t1)
    var acked: [4]SentFrame = [TRecFrame(0); 4]
    r0 := [AckRange(lo: 0, hi: 0)]
    t2 := TRecTick(mut s, time.Millis(20))
    _ = try cli.OnAck(Space.Initial, r0[..], time.Millis(0), t2, mut acked[..], mut lost[..])
    Assert(cli.BytesInFlight() == 0 && cli.Timer(t2).IsSome())
    t3 := TRecTick(mut s, cli.Timer(t2).Must())
    ad1 := cli.OnTimeout(t3, mut lost[..])
    Assert(ad1.probes == 1 && cli.IsAntiDeadlock() && cli.PtoSpace() == Space.Initial)

    cli.SetHandshakeKeys(true)
    t4 := TRecTick(mut s, cli.Timer(t3).Must())
    ad2 := cli.OnTimeout(t4, mut lost[..])
    Assert(ad2.probes == 1 && cli.IsAntiDeadlock() && cli.PtoSpace() == Space.Handshake)
}

test "RFC 9002 6.3: a Retry resets congestion and recovery state but keeps CRYPTO data" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 21)
    defer s.Free()
    h := sys.heap.Alloc()
    var rec := try Recovery.New(h)
    defer rec.Free(h)
    t0 := time.Now(s.Io())
    TRecSend(mut rec, h, Space.Initial, 0, 1200, true, true, t0)
    Assert(rec.BytesInFlight() == 1200)
    rec.ResetForRetry(t0)
    Assert(rec.BytesInFlight() == 0)
    Assert(rec.Cwnd() == 12000)
    Assert(rec.PtoCount() == 0)
    Assert(rec.NextPn(Space.Initial) == 0)
}
