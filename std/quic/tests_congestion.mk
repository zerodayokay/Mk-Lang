// Why: QUIC NewReno congestion control, pacing, and persistent congestion
// enforce window bounds, slow start, recovery entry/exit, congestion avoidance,
// burst limits, and app-limited freezes per RFC 9002 Section 7 and Appendix B.
//
// What: tests for congestion control, recovery, and pacing.
//
// How: drives Recovery with std.sim's virtual clock over realistic packet
// sequences and checks exact cwnd, ssthresh, bytes_in_flight, and pacing delays.

import std.sim
import std.time

TCcTick(s: mut sim.Sim, d: time.Duration): time.Instant {
    ns := d.Nanos()
    if ns >= 1000 {
        s.Io().Sleep(time.Nanos(ns - 1000)) else {
            Assert(false)
        }
    }
    return time.Now(s.Io())
}

TCcFrame(i: int): SentFrame {
    return SentFrame(kind: SentKind.Control(index: i))
}

TCcSend(rec: mut Recovery, h: &Alloc, sp: Space, pn: i64, bytes: int, ae: bool, inf: bool, now: time.Instant) {
    fs := [TCcFrame(int(pn))]
    rec.OnSent(h, sp, pn, bytes, ae, inf, fs[..], now) else {
        Assert(false)
    }
}

test "RFC 9002 7: ACK-only packets do not count in bytes in flight; PADDING-only ones do" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 30)
    defer s.Free()
    h := sys.heap.Alloc()
    var rec := try Recovery.New(h)
    defer rec.Free(h)
    t0 := time.Now(s.Io())
    TCcSend(mut rec, h, Space.App, 0, 40, false, false, t0)
    Assert(rec.BytesInFlight() == 0)
    TCcSend(mut rec, h, Space.App, 1, 1200, false, true, t0)
    Assert(rec.BytesInFlight() == 1200)
}

test "RFC 9002 7.2, 7.3.1, 7.3.2, 7.3.3, 7.4: initial window, slow start, loss reduction, recovery freeze, and CA" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 31)
    defer s.Free()
    h := sys.heap.Alloc()
    var rec := try Recovery.New(h)
    defer rec.Free(h)
    Assert(rec.Cwnd() == 12000)

    t0 := time.Now(s.Io())
    rec.SetHandshakeConfirmed(true, t0)
    var acked: [16]SentFrame = [TCcFrame(0); 16]
    var lost: [16]SentFrame = [TCcFrame(0); 16]

    // Slow start: acking 1200 bytes grows cwnd from 12,000 to 13,200.
    TCcSend(mut rec, h, Space.App, 0, 1200, true, true, t0)
    t1 := TCcTick(mut s, time.Millis(20))
    r0 := [AckRange(lo: 0, hi: 0)]
    _ = try rec.OnAck(Space.App, r0[..], time.Millis(0), t1, mut acked[..], mut lost[..])
    Assert(rec.Cwnd() == 13200)

    // Send packets 1..5 at t1; ack packet 5 -> packet 1 lost (5 >= 1 + 3).
    // Note packet 1 was sent AFTER acknowledged packet 0.
    for i in 1..6 {
        TCcSend(mut rec, h, Space.App, i64(i), 1200, true, true, t1)
    }
    t2 := TCcTick(mut s, time.Millis(20))
    r5 := [AckRange(lo: 5, hi: 5)]
    res := try rec.OnAck(Space.App, r5[..], time.Millis(0), t2, mut acked[..], mut lost[..])
    // Packets 1 and 2 are both <= 5 - 3 -> 2 packets lost.
    Assert(res.lost == 2)
    // Entering recovery halves 13200 -> 6600.
    Assert(rec.Ssthresh() == 6600 && rec.Cwnd() == 6600)

    // Packets 2..4 were sent at t1 <= recovery_start (t2): losing 2 later does NOT reduce cwnd again.
    t3 := TCcTick(mut s, time.Millis(40))
    _ = rec.OnTimeout(t3, mut lost[..])
    Assert(rec.Cwnd() == 6600)

    // Send a packet 6 at t3 (> recovery_start t2) and ack it -> exits recovery and enters CA!
    TCcSend(mut rec, h, Space.App, 6, 1200, true, true, t3)
    t4 := TCcTick(mut s, time.Millis(20))
    r6 := [AckRange(lo: 6, hi: 6)]
    _ = try rec.OnAck(Space.App, r6[..], time.Millis(0), t4, mut acked[..], mut lost[..])
    // CA: adds 1200 * 1200 / 6600 = 218 bytes (at most 1200 per cwnd acked).
    Assert(rec.Cwnd() == 6600 + 218)
}

test "RFC 9002 7.2: no loss takes the window below 2,400 bytes" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 32)
    defer s.Free()
    h := sys.heap.Alloc()
    var rec := try Recovery.New(h)
    defer rec.Free(h)
    var acked: [8]SentFrame = [TCcFrame(0); 8]
    var lost: [8]SentFrame = [TCcFrame(0); 8]
    var pn: i64 = 0
    // Repeated distinct recovery periods eventually clamp at 2,400 bytes.
    for _ in 0..4 {
        t_send := TCcTick(mut s, time.Millis(10))
        p_lost := pn
        TCcSend(mut rec, h, Space.App, p_lost, 1200, true, true, t_send)
        TCcSend(mut rec, h, Space.App, p_lost + 1, 1200, true, true, t_send)
        TCcSend(mut rec, h, Space.App, p_lost + 2, 1200, true, true, t_send)
        TCcSend(mut rec, h, Space.App, p_lost + 3, 1200, true, true, t_send)
        pn += 4
        t_ack := TCcTick(mut s, time.Millis(20))
        r := [AckRange(lo: p_lost + 3, hi: p_lost + 3)]
        _ = try rec.OnAck(Space.App, r[..], time.Millis(0), t_ack, mut acked[..], mut lost[..])
        // Clear remaining bytes in flight via time-threshold loss.
        t_loss := TCcTick(mut s, time.Millis(500))
        _ = rec.OnTimeout(t_loss, mut lost[..])
    }
    Assert(rec.Cwnd() == 2400)
}

test "RFC 9002 7, 7.5: cwnd blocks normal sends past limit, allows one recovery packet and PTO probes" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 33)
    defer s.Free()
    h := sys.heap.Alloc()
    var rec := try Recovery.New(h)
    defer rec.Free(h)
    t0 := time.Now(s.Io())
    // Fill 12,000-byte initial window (10 * 1200).
    for i in 0..10 {
        Assert(rec.CanSend(1200, t0))
        TCcSend(mut rec, h, Space.App, i64(i), 1200, true, true, t0)
    }
    Assert(rec.BytesInFlight() == 12000)
    Assert(!rec.CanSend(1200, t0))
    // Probe is never blocked by cwnd and counts in bytes_in_flight.
    try rec.OnProbeSent(h, Space.App, 10, 1200, [TCcFrame(10)][..], t0)
    Assert(rec.BytesInFlight() == 13200)
}

test "RFC 9002 7.6: persistent congestion drops window to 2,400 when two ack-eliciting losses span > 3 PTOs with nothing acked between" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 34)
    defer s.Free()
    h := sys.heap.Alloc()
    var rec := try Recovery.New(h)
    defer rec.Free(h)
    rec.SetPeerMaxAckDelay(time.Millis(25))
    t0 := time.Now(s.Io())
    rec.SetHandshakeConfirmed(true, t0)

    // Packet 0 establishes first_rtt_sample = 50ms -> smoothed=50ms, rttvar=25ms, max_ack_delay=25ms.
    // pc_duration = (50 + 4*25 + 25) * 3 = 175 * 3 = 525ms.
    TCcSend(mut rec, h, Space.App, 0, 1200, true, true, t0)
    t1 := TCcTick(mut s, time.Millis(50))
    var acked: [8]SentFrame = [TCcFrame(0); 8]
    var lost: [8]SentFrame = [TCcFrame(0); 8]
    r0 := [AckRange(lo: 0, hi: 0)]
    _ = try rec.OnAck(Space.App, r0[..], time.Millis(0), t1, mut acked[..], mut lost[..])
    Assert(rec.Cwnd() == 13200)

    // Send packet 1 at t1 + 10ms, packet 2 at t1 + 600ms (span = 590ms > 525ms), nothing acked between.
    t2 := TCcTick(mut s, time.Millis(10))
    TCcSend(mut rec, h, Space.App, 1, 1200, true, true, t2)
    t3 := TCcTick(mut s, time.Millis(590))
    TCcSend(mut rec, h, Space.App, 2, 1200, true, true, t3)
    // Send packets 3..5 at t3 + 10ms and ack 5 -> packets 1 and 2 declared lost!
    t4 := TCcTick(mut s, time.Millis(10))
    TCcSend(mut rec, h, Space.App, 3, 1200, true, true, t4)
    TCcSend(mut rec, h, Space.App, 4, 1200, true, true, t4)
    TCcSend(mut rec, h, Space.App, 5, 1200, true, true, t4)
    t5 := TCcTick(mut s, time.Millis(50))
    r5 := [AckRange(lo: 5, hi: 5)]
    _ = try rec.OnAck(Space.App, r5[..], time.Millis(0), t5, mut acked[..], mut lost[..])
    Assert(rec.Cwnd() == 2400)
}

test "RFC 9002 7.7: sends are paced at 1.25 windows per smoothed_rtt with bursts of at most 10 packets, ACK-only unpaced" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 35)
    defer s.Free()
    h := sys.heap.Alloc()
    var rec := try Recovery.New(h)
    defer rec.Free(h)
    t0 := time.Now(s.Io())
    // Establish smoothed_rtt = 100ms, cwnd = 13,200; ack packet 0 so bytes_in_flight returns to 0.
    TCcSend(mut rec, h, Space.App, 0, 1200, true, true, t0)
    t1 := TCcTick(mut s, time.Millis(100))
    var acked: [16]SentFrame = [TCcFrame(0); 16]
    var lost: [16]SentFrame = [TCcFrame(0); 16]
    _ = try rec.OnAck(Space.App, [AckRange(lo: 0, hi: 0)][..], time.Millis(0), t1, mut acked[..], mut lost[..])
    // Refill pacer full after 100ms, then send a burst of 10 * 1200 = 12,000 bytes (cwnd is 13,200).
    t2 := TCcTick(mut s, time.Millis(100))
    for i in 1..11 {
        Assert(rec.CanSend(1200, t2))
        TCcSend(mut rec, h, Space.App, i64(i), 1200, true, true, t2)
    }
    // cwnd still has room (12,000 < 13,200), but 11th packet is blocked by 10-packet pacer burst!
    Assert(!rec.CanSend(1200, t2))
    delay := rec.NextPaceDelay(1200, t2).Must()
    // Rate = 1.25 * 13200 / 100ms = 165 bytes/ms -> 1200 bytes takes ceil(1200 / 165) ms = 7.272728ms.
    Assert(delay.Nanos() > 7_000_000 && delay.Nanos() < 8_000_000)
    t3 := TCcTick(mut s, delay)
    Assert(rec.CanSend(1200, t3))
    // ACK-only packet leaves immediately without touching pacer or cwnd.
    TCcSend(mut rec, h, Space.App, 11, 50, false, false, t2)
    Assert(rec.BytesInFlight() == 12000)
}

test "RFC 9002 7.8: acknowledgments while application-limited do not grow the window" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 36)
    defer s.Free()
    h := sys.heap.Alloc()
    var rec := try Recovery.New(h)
    defer rec.Free(h)
    t0 := time.Now(s.Io())
    rec.SetAppLimited(true)
    TCcSend(mut rec, h, Space.App, 0, 1200, true, true, t0)
    t1 := TCcTick(mut s, time.Millis(20))
    var acked: [4]SentFrame = [TCcFrame(0); 4]
    var lost: [4]SentFrame = [TCcFrame(0); 4]
    _ = try rec.OnAck(Space.App, [AckRange(lo: 0, hi: 0)][..], time.Millis(0), t1, mut acked[..], mut lost[..])
    Assert(rec.Cwnd() == 12000 && rec.BytesInFlight() == 0)
}
