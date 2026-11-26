// verdict: ok
// rule: LIB150
// rule: D-239
// note: `net.Error` is `Refused | Reset | AddrInUse | Unreachable | InvalidAddress | Os | Canceled | Closed | time.TimedOut | Unsupported | SecureFailed | StreamReset | MuxClosed | OutOfMemory`, payload-free except `Os(code: i64)`, `SecureFailed(why)`, `StreamReset(code)` and `MuxClosed(code, app, peer)`; a match with one arm per member covers this release, and since `net.Error` is open, outside std it also has the `_` arm `mk fix` adds
import std.net
import std.time

Kind(e: net.Error): int {
    match e {
        net.Refused {
            return 1
        }
        net.Reset {
            return 2
        }
        net.AddrInUse {
            return 3
        }
        net.Unreachable {
            return 4
        }
        net.InvalidAddress {
            return 5
        }
        net.Os(code: c) {
            return Trunc[int](c)
        }
        Canceled {
            return 6
        }
        Closed {
            return 7
        }
        time.TimedOut {
            return 8
        }
        net.Unsupported {
            return 9
        }
        net.SecureFailed(why: _) {
            return 10
        }
        OutOfMemory {
            return 11
        }
        net.StreamReset(code: _) {
            return 12
        }
        net.MuxClosed(code: _, app: _, peer: _) {
            return 13
        }
        _ {
            Panic("net.Error gained a member this match does not handle")
        }
    }
}
