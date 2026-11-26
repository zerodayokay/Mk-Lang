// Why: a multiplexed connection without a dedicated reader task relies on
// whichever task is waiting to hold the read token, read the socket, dispatch
// frames, and hand the token off on any exit without losing a wake-up or
// stranding the connection.
//
// What: Driven and Wait, implementing the read-token protocol once for both
// std.http.h2 and std.quic.
//
// How: Wait loops over Driven.Begin(nc, p) using nc = io.NoCancel() for state
// locks (rule L1) and cancellable waits only for Driven.Lead, Notify.Wait,
// and the caller's write wait (rule L2).
//
// Protocol rules:
//   L1, locks never fail: every lock of connection state is taken with
//       io.NoCancel() so a cancelled exit can clear its registration and pass
//       the token.
//   L2, only three waits are cancellable: the write-token acquire, a party's
//       Notify.Wait, and the leader's socket read. Dispatch runs after the
//       read scope closes.
//   T1, no lost wake-up: a party enters under the state lock before waiting;
//       Notify keeps one permit for a Wait not yet started; state changes
//       under the lock notify each affected party.
//   T2, no lost token: every End while the token is free and a party waits
//       returns that party, and Wait wakes it via NotifyOne. A notified
//       waiter returns ok even if cancelled concurrently and re-runs Begin.
//   T3, a cancelled leader loses nothing: its socket read consumed nothing
//       before returning Canceled, so it passes the token and returns
//       Canceled.
//   T4, spurious wakes are harmless: every wake re-runs Begin, checking the
//       condition against the slot's generation.
//   T5, timers need no token: a party whose Wait ends on its deadline runs
//       Timers(nc) under the lock and loops without holding the token.
//   T6, a party with no stream: p may be a driver or handshake slot whose
//       condition is never true or tracks handshake progress.

import std.sync
import std.time

/// What a multiplexed connection gives the read-token protocol. `nc` is
/// `io.NoCancel()` so state-lock operations never fail with Canceled.
export interface Driven {
    /// Under the state lock: returns Step.Done when `p`'s condition or the
    /// connection fault holds, else calls Token.Enter(p) and returns
    /// Step.Lead or Step.Wait with the next timer deadline (or none).
    Begin(&self, nc: Io, p: int): Step
    /// The leader's turn: performs one socket read in a scope of `io` with
    /// `deadline`, then dispatches under the lock with `nc` and notifies
    /// affected parties, then runs due timers. Returns Canceled only for `io`'s
    /// own cancellation; deadline expiry returns ok.
    Lead(&self, io: Io, nc: Io, deadline: ?time.Instant): Canceled!void
    /// Under the state lock: runs due timers and sends what they produce.
    Timers(&self, nc: Io)
    /// Under the state lock: calls Token.Leave(p) and returns the next party
    /// to wake, or -1.
    End(&self, nc: Io, p: int): int
    /// Returns the Notify that party `p` waits on.
    Party(&self, p: int): &sync.Notify
}

// WakeNext wakes party `wake` if non-negative.
WakeNext[D: Driven](d: &D, wake: int) {
    if wake >= 0 {
        d.Party(wake).NotifyOne()
    }
}

/// Waits until party `p`'s condition holds, reading the connection for
/// everyone while the read token is `p`'s. Fails with
/// Canceled when `io` is cancelled, passing the read token on first.
/// O(1) per wake plus the Driven calls; allocates nothing.
export Wait[D: Driven](d: &D, io: Io, p: int): Canceled!void {
    nc := io.NoCancel()
    for {
        step := d.Begin(nc, p)
        match step {
            Step.Done {
                WakeNext(d, d.End(nc, p))
                return
            }
            Step.Lead(deadline: dl) {
                var cancelled := false
                d.Lead(io, nc, dl) else {
                    cancelled = true
                }
                WakeNext(d, d.End(nc, p))
                if cancelled {
                    fail Canceled
                }
            }
            Step.Wait(deadline: dl) {
                match dl {
                    some(t) {
                        var expired := false
                        var cancelled := false
                        scope s := io.Scope() {
                            s.CancelAfter(t.Since(time.Now(io)))
                            d.Party(p).Wait(io) else {
                                if s.Expired() {
                                    expired = true
                                } else {
                                    cancelled = true
                                }
                            }
                        }
                        if expired {
                            d.Timers(nc)
                            continue
                        }
                        if cancelled {
                            WakeNext(d, d.End(nc, p))
                            fail Canceled
                        }
                    }
                    none {
                        var cancelled := false
                        d.Party(p).Wait(io) else {
                            cancelled = true
                        }
                        if cancelled {
                            WakeNext(d, d.End(nc, p))
                            fail Canceled
                        }
                    }
                }
            }
        }
    }
}
