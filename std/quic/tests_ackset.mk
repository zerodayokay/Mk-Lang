// Why: AckSet tracks received packet numbers per space, enforces the 64-range
// cap and duplicate floor, schedules immediate or delayed ACKs, and encodes
// ACK frames largest-first.
//
// What: unit tests for through .
//
// How: uses std.sim's virtual clock for deterministic instants and decodes
// emitted ACK frames to verify wire encoding.

import std.sim
import std.time

// Reads a QUIC varint from b[at..] for test assertions on emitted ACK frames.
T23ReadVarint(b: []u8, at: int): ?(v: u64, next: int) {
    if at >= b.Len() {
        return none
    }
    first := b[at]
    tag := first >> 6
    len := 1 << Cast[u64](tag)
    if at + len > b.Len() {
        return none
    }
    var v := Cast[u64](first & 0x3F)
    for i in 1..len {
        v = (v << 8) | Cast[u64](b[at + i])
    }
    return some((v: v, next: at + len))
}

// Advances a Sim clock by d (compensating for Now's 1 us step) and returns the new Instant.
T23Tick(s: mut sim.Sim, d: time.Duration): time.Instant {
    ns := d.Nanos()
    if ns >= 1000 {
        s.Io().Sleep(time.Nanos(ns - 1000)) else {
            Assert(false)
        }
    }
    return time.Now(s.Io())
}

test "RFC 9000 13.2.6: each packet number space has its own ACK state and ACKs only its own packets" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 1)
    defer s.Free()
    t0 := time.Now(s.Io())
    var acks := AckSet.New()
    Assert(acks.Add(Space.Initial, 0, true, t0))
    Assert(acks.Add(Space.Handshake, 1, true, t0))
    Assert(acks.Add(Space.App, 5, true, t0))
    Assert(acks.Largest(Space.Initial).Must() == 0)
    Assert(acks.Largest(Space.Handshake).Must() == 1)
    Assert(acks.Largest(Space.App).Must() == 5)

    var out: [64]u8 = [0; 64]
    n := acks.Write(Space.Initial, mut out[..], t0)
    Assert(n > 0)
    // Handshake and App remain pending when Initial is written.
    Assert(!acks.HasPendingAck(Space.Initial))
    Assert(acks.HasPendingAck(Space.Handshake))
    Assert(acks.HasPendingAck(Space.App))
    // Initial decoded largest is 0, first_range 0, extra 0.
    r0 := T23ReadVarint(out[0..n], 0).Must()
    Assert(r0.v == 0x02)
    r1 := T23ReadVarint(out[0..n], r0.next).Must()
    Assert(r1.v == 0)
    r2 := T23ReadVarint(out[0..n], r1.next).Must()
    r3 := T23ReadVarint(out[0..n], r2.next).Must()
    Assert(r3.v == 0)
    r4 := T23ReadVarint(out[0..n], r3.next).Must()
    Assert(r4.v == 0 && r4.next == n)
}

test "RFC 9000 12.3: a duplicate packet, or one below the floor of dropped ranges, is discarded after decryption" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 2)
    defer s.Free()
    t0 := time.Now(s.Io())
    var acks := AckSet.New()
    // Insert 64 disjoint even packet numbers: 0, 2, ..., 126.
    for i in 0..64 {
        Assert(acks.Add(Space.App, i64(i * 2), true, t0))
    }
    Assert(acks.RangeCount(Space.App) == 64)
    Assert(acks.Floor(Space.App) == 0)
    // Duplicate inside a kept range.
    Assert(!acks.Add(Space.App, 10, true, t0))
    // Insert 128 (65th range): drops [0..0], raising floor to 1.
    Assert(acks.Add(Space.App, 128, true, t0))
    Assert(acks.RangeCount(Space.App) == 64)
    Assert(acks.Floor(Space.App) == 1)
    // Packet 0 is below floor -> treated as duplicate and discarded.
    Assert(!acks.Add(Space.App, 0, true, t0))
    // Packet 1 merges into [1..2] without adding a range -> accepted!
    Assert(acks.Add(Space.App, 1, true, t0))
    Assert(acks.RangeCount(Space.App) == 64)
    // Inserting 130 drops [1..2], raising floor to 3.
    Assert(acks.Add(Space.App, 130, true, t0))
    Assert(acks.Floor(Space.App) == 3)
    Assert(!acks.Add(Space.App, 1, true, t0))
    Assert(!acks.Add(Space.App, 2, true, t0))
}

test "RFC 9000 13.2.3: the largest processed packet number survives dropping every range" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 3)
    defer s.Free()
    t0 := time.Now(s.Io())
    var acks := AckSet.New()
    for i in 0..100 {
        Assert(acks.Add(Space.App, i64(i * 2), false, t0))
    }
    Assert(acks.RangeCount(Space.App) == 64)
    Assert(acks.Largest(Space.App).Must() == 198)
    Assert(acks.Floor(Space.App) == 71)
    // Out-of-order packet 60 is below floor 71 -> rejected as duplicate.
    Assert(!acks.Add(Space.App, 60, false, t0))
    Assert(acks.Floor(Space.App) == 71)
    Assert(acks.Largest(Space.App).Must() == 198)
    // ClearSpace drops ranges cleanly.
    acks.ClearSpace(Space.App)
    Assert(acks.Largest(Space.App).IsNone())
}

test "RFC 9000 13.2.1: Initial and Handshake are ACKed at once and 1-RTT within max_ack_delay" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 4)
    defer s.Free()
    t0 := time.Now(s.Io())
    var acks := AckSet.New()
    Assert(acks.Add(Space.Initial, 0, true, t0))
    Assert(acks.NeedsAckOnly(Space.Initial, t0))
    Assert(acks.Due(t0).Must().Nanos() == 0)

    var out: [64]u8 = [0; 64]
    _ = acks.Write(Space.Initial, mut out[..], t0)
    Assert(acks.Add(Space.Handshake, 0, true, t0))
    Assert(acks.NeedsAckOnly(Space.Handshake, t0))
    _ = acks.Write(Space.Handshake, mut out[..], t0)

    // First in-order 1-RTT ack-eliciting packet waits up to max_ack_delay (25 ms).
    Assert(acks.Add(Space.App, 0, true, t0))
    Assert(!acks.NeedsAckOnly(Space.App, t0))
    Assert(acks.Due(t0).Must().Nanos() == 25_000_000)
    t1 := T23Tick(mut s, time.Millis(25))
    Assert(acks.NeedsAckOnly(Space.App, t1))
    Assert(acks.Due(t1).Must().Nanos() == 0)
}

test "RFC 9000 13.2.1: no ACK-only packet answers a non-ack-eliciting one, and at most one answers each ack-eliciting one" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 5)
    defer s.Free()
    t0 := time.Now(s.Io())
    var acks := AckSet.New()
    // Non-ack-eliciting packets with gaps never schedule an ACK-only packet.
    Assert(acks.Add(Space.App, 0, false, t0))
    Assert(acks.Add(Space.App, 5, false, t0))
    Assert(acks.Due(t0).IsNone())
    Assert(!acks.NeedsAckOnly(Space.App, t0))

    // One ack-eliciting packet -> Write clears pending trigger so no second ACK-only fires.
    Assert(acks.Add(Space.App, 6, true, t0))
    t1 := T23Tick(mut s, time.Millis(25))
    Assert(acks.NeedsAckOnly(Space.App, t1))
    var out: [64]u8 = [0; 64]
    Assert(acks.Write(Space.App, mut out[..], t1) > 0)
    Assert(!acks.NeedsAckOnly(Space.App, t1))
    Assert(acks.Due(t1).IsNone())
}

test "RFC 9000 13.2.1: a PING is added to ACK-only packets at most once per RTT" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 6)
    defer s.Free()
    t0 := time.Now(s.Io())
    var acks := AckSet.New()
    rtt := time.Millis(50)
    Assert(acks.AllowAckPing(t0, rtt))
    t1 := T23Tick(mut s, time.Millis(49))
    Assert(!acks.AllowAckPing(t1, rtt))
    t2 := T23Tick(mut s, time.Millis(1))
    Assert(acks.AllowAckPing(t2, rtt))
}

test "RFC 9000 13.2.2: the second ack-eliciting packet triggers an ACK" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 7)
    defer s.Free()
    t0 := time.Now(s.Io())
    var acks := AckSet.New()
    Assert(acks.Add(Space.App, 0, true, t0))
    Assert(!acks.NeedsAckOnly(Space.App, t0))
    Assert(acks.Add(Space.App, 1, true, t0))
    Assert(acks.NeedsAckOnly(Space.App, t0))
}

test "RFC 9000 13.2.1: a reordered or gapped ack-eliciting packet is ACKed at once" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 8)
    defer s.Free()
    t0 := time.Now(s.Io())
    var acks := AckSet.New()
    var out: [64]u8 = [0; 64]
    Assert(acks.Add(Space.App, 0, true, t0))
    Assert(acks.Add(Space.App, 2, true, t0)) // gap: 1 missing
    Assert(acks.NeedsAckOnly(Space.App, t0))
    _ = acks.Write(Space.App, mut out[..], t0)
    // Reordered packet 1 < largest 2 -> immediate ACK.
    Assert(acks.Add(Space.App, 1, true, t0))
    Assert(acks.NeedsAckOnly(Space.App, t0))
}

test "RFC 9000 13.2.3: an ACK frame always has the largest range and drops the oldest to fit" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 9)
    defer s.Free()
    t0 := time.Now(s.Io())
    var acks := AckSet.New()
    // Create 4 ranges: [0..0], [2..2], [4..4], [6..6].
    Assert(acks.Add(Space.App, 0, true, t0))
    Assert(acks.Add(Space.App, 2, true, t0))
    Assert(acks.Add(Space.App, 4, true, t0))
    Assert(acks.Add(Space.App, 6, true, t0))
    // Full header + 1 extra range = 5 + 2 = 7 bytes; tight 7-byte buffer drops [0..0] and [2..2].
    var tight: [7]u8 = [0; 7]
    n := acks.Write(Space.App, mut tight[..], t0)
    Assert(n == 7)
    r_type := T23ReadVarint(tight[0..n], 0).Must()
    r_largest := T23ReadVarint(tight[0..n], r_type.next).Must()
    r_delay := T23ReadVarint(tight[0..n], r_largest.next).Must()
    r_cnt := T23ReadVarint(tight[0..n], r_delay.next).Must()
    r_first := T23ReadVarint(tight[0..n], r_cnt.next).Must()
    r_gap := T23ReadVarint(tight[0..n], r_first.next).Must()
    r_len := T23ReadVarint(tight[0..n], r_gap.next).Must()
    Assert(r_largest.v == 6 && r_cnt.v == 1 && r_first.v == 0)
    // Next range down from [6..6] is [4..4]: gap = 6 - 4 - 2 = 0, len = 0.
    Assert(r_gap.v == 0 && r_len.v == 0 && r_len.next == n)
}

test "RFC 9000 13.2.5: ACK Delay is the receive-to-send time scaled by ack_delay_exponent" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 10)
    defer s.Free()
    t0 := time.Now(s.Io())
    var acks := AckSet.New()
    Assert(acks.Add(Space.App, 0, true, t0))
    t1 := T23Tick(mut s, time.Millis(16))
    var out: [64]u8 = [0; 64]
    n := acks.Write(Space.App, mut out[..], t1)
    r_type := T23ReadVarint(out[0..n], 0).Must()
    r_largest := T23ReadVarint(out[0..n], r_type.next).Must()
    r_delay := T23ReadVarint(out[0..n], r_largest.next).Must()
    // 16 ms = 16000 us >> 3 = 2000.
    Assert(r_delay.v == 2000)
}
