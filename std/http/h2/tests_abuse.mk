// Why: rapid resets, control floods, unsolicited WINDOW_UPDATEs and slow
// data dribble must be caught deterministically by the abuse-defence state,
// verified with a simulated fake clock so no test sleeps in real time.
//
// What: tests for Bucket, Progress, and UpdateGate using std.sim's fake clock.
//
// How: drives time.Now(io) and io.Sleep under sim.Sim to test continuous
// bucket refill without drift, progress-floor starvation over request_header,
// and unsolicited WINDOW_UPDATE classification.

import std.sim
import std.time

test "RFC 9113 10.5: WINDOW_UPDATEs that follow DATA are not charged" {
    var g := UpdateGate.New()
    // Unsolicited before any DATA is charged.
    Assert(g.Charge(4096) == true)
    // After DATA, >= 1024 is not charged.
    g.SentData()
    Assert(g.Charge(1024) == false)
    // A second WINDOW_UPDATE without new DATA is charged.
    Assert(g.Charge(4096) == true)
    // After DATA, < 1024 is charged and clears sent.
    g.SentData()
    Assert(g.Charge(1023) == true)
    Assert(g.Charge(4096) == true)
    // Multiple legitimate updates within cumulative DATA bytes are not charged.
    g.SentBytes(65536)
    Assert(g.Charge(32768) == false)
    Assert(g.Charge(32768) == false)
    Assert(g.Charge(1024) == true)
}

test "RFC 9113 10.5: one-byte window updates neither make one-byte DATA nor hold a stream forever" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 1)
    defer s.Free()
    io := s.Io()
    var p := Progress.New()
    t0 := time.Now(io)
    win := time.Seconds(30)
    p.Pending(t0)
    // Trickle 1 byte every 5 seconds for 25 seconds: total 5 bytes, not yet past 30 s window.
    for _ in 0..5 {
        try io.Sleep(time.Seconds(5))
        p.Moved(1)
        // Re-calling Pending while already active must NOT reset the start time.
        p.Pending(time.Now(io))
        Assert(!p.Starved(time.Now(io), win, ProgressFloor))
    }
    // Cross 30 s with only 6 bytes moved (< 30 * 1024 = 30720): starved!
    try io.Sleep(time.Seconds(5))
    p.Moved(1)
    Assert(p.Starved(time.Now(io), win, ProgressFloor))
    // Moving enough bytes to meet the floor clears starvation; Idle resets.
    // Note: Sim.Sleep may add sub-millisecond timer jitter, so compute exact floor for elapsed.
    elapsed := time.Now(io).Since(t0).Nanos()
    need := int((elapsed / 1_000_000_000) * i64(ProgressFloor) + ((elapsed % 1_000_000_000) * i64(ProgressFloor) + 999_999_999) / 1_000_000_000)
    p.Moved(need - 6)
    Assert(!p.Starved(time.Now(io), win, ProgressFloor))
    p.Idle()
    Assert(!p.Starved(time.Now(io), win, ProgressFloor))
    // Exactly at floor is NOT starved.
    t1 := time.Now(io)
    p.Pending(t1)
    try io.Sleep(time.Seconds(30))
    e2 := time.Now(io).Since(t1).Nanos()
    need2 := int((e2 / 1_000_000_000) * i64(ProgressFloor) + ((e2 % 1_000_000_000) * i64(ProgressFloor) + 999_999_999) / 1_000_000_000)
    p.Moved(need2)
    Assert(!p.Starved(time.Now(io), win, ProgressFloor))
}

test "Bucket refills continuously at rate per second without sub-second drift" (sys: &System) {
    var s := try sim.Sim.New(sys.arena, 2)
    defer s.Free()
    io := s.Io()
    var b := Bucket.New(ResetBudget, ResetRefill, time.Now(io))
    // Drain all 200 tokens at t0.
    for _ in 0..ResetBudget {
        Assert(b.Take(time.Now(io)))
    }
    Assert(!b.Take(time.Now(io)))
    // Advance 5 ms repeatedly (100/s = 1 token per 10 ms): sub-second calls must accumulate.
    try io.Sleep(time.Millis(5))
    Assert(!b.Take(time.Now(io)))
    try io.Sleep(time.Millis(5))
    Assert(b.Take(time.Now(io)))
    Assert(!b.Take(time.Now(io)))
    // Advance 2 seconds: capped at ResetBudget (200), not 200+extra.
    try io.Sleep(time.Seconds(5))
    Assert(b.Take(time.Now(io)))
    Assert(b.Tokens() == ResetBudget - 1)
}
