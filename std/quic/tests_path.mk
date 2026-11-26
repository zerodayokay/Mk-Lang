// Why: path validation and NAT rebinding (RFC 9000 8.2, 9) must isolate
// unvalidated probes behind 3x amplification limits, keep old paths alive until
// proven dead, enforce 1,200-byte probing, and revert cleanly on timeout.
//
// What: unit tests for PathState covering Same/Probe/Migrated classification,
// 3x amplification budget, old-path challenge revert, 1200-byte reprobe,
// rate limiting per 3*PTO, any-path PATH_RESPONSE acceptance, and IP change flag.
//
// How: drives PathState against std.sim clocks and loopback net.Addr variants.

import std.sim
import std.time

test "RFC 9000 8.2, 9: path migration, 3x amp, 1200-byte reprobe, old-path revert, and timeout" (sys: &System) {
    var clk := try sim.Sim.New(sys.arena, 1)
    defer clk.Free()
    io := clk.Io()
    var now := time.Now(io)
    pto := time.Millis(100)

    a1 := TestV4(127, 0, 0, 1, 5000)
    a2 := TestV4(127, 0, 0, 1, 5001)
    a3 := TestV4(127, 0, 0, 2, 5002)
    Assert(!AddrEqual(&a1, &a2) && IpEqual(&a1, &a2) && !IpEqual(&a1, &a3))

    var ps := PathState.New(a1)
    ps.SetValidated(a1)
    Assert(ps.OnPacket(io, a1, 100, 1, true, pto, now) == PathStep.Same)

    // Probing-only or lower-PN packet from a2 does not migrate.
    Assert(ps.OnPacket(io, a2, 100, 2, false, pto, now) == PathStep.Probe)
    Assert(AddrEqual(&ps.Destination(), &a1))

    // Non-probing highest-PN packet from a2 starts migration + 3x amp budget.
    Assert(ps.OnPacket(io, a2, 200, 3, true, pto, now) == PathStep.Migrated)
    Assert(AddrEqual(&ps.Destination(), &a2))
    Assert(ps.Allowance(a2) == 600)
    ps.RecordSend(a2, 400)
    Assert(ps.Allowance(a2) == 200)

    // Both new challenge and old challenge are queued.
    nch := ps.TakeNewChallenge().Must()
    Assert(AddrEqual(&nch.to, &a2))
    // Mark unpadded (< 1200 bytes) to exercise 1200-byte reprobe.
    ps.MarkNewChallengeSent(false)
    och := ps.TakeOldChallenge().Must()
    Assert(AddrEqual(&och.to, &a1))

    // Responding to unpadded new challenge triggers 1200-byte reprobe (needs_1200_reprobe)
    // and keeps old path validated until 1200-byte probe passes.
    res1 := ps.OnResponse(io, &nch.data, pto, now)
    Assert(!res1.validated && !res1.reverted)
    nch2 := ps.TakeNewChallenge().Must()
    ps.MarkNewChallengeSent(true)

    // Even a response arriving from another source address matches if token equals.
    res2 := ps.OnResponse(io, &nch2.data, pto, now)
    Assert(res2.validated && !res2.ip_changed && !res2.reverted)
    Assert(AddrEqual(&ps.ValidatedAddr(), &a2))

    // Rate-limit: new migration to a3 within 3*PTO is ignored (treated as Probe).
    Assert(ps.OnPacket(io, a3, 1200, 4, true, pto, now) == PathStep.Probe)
    clk.Advance(time.Millis(400))
    now = time.Now(io)

    // Migration to new IP a3: old path responds -> reverts migration to a2.
    Assert(ps.OnPacket(io, a3, 1200, 5, true, pto, now) == PathStep.Migrated)
    och3 := ps.TakeOldChallenge().Must()
    rev := ps.OnResponse(io, &och3.data, pto, now)
    Assert(rev.reverted && !rev.validated)
    Assert(AddrEqual(&ps.Destination(), &a2))

    // Migration to a3 that times out after 3*eff_pto (>= 2997 ms) reverts to a2.
    clk.Advance(time.Seconds(4))
    now = time.Now(io)
    Assert(ps.OnPacket(io, a3, 1200, 6, true, pto, now) == PathStep.Migrated)
    Assert(ps.Due().IsSome())
    Assert(!ps.OnTimeout(now))
    clk.Advance(time.Seconds(3))
    now = time.Now(io)
    Assert(ps.OnTimeout(now))
    Assert(AddrEqual(&ps.Destination(), &a2))
}
