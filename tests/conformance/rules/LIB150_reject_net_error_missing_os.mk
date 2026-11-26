// verdict: reject E0306
// rule: LIB150
// rule: D-239
// note: the twin of LIB150_accept_net_error_members without the `Os` arm: E0306 at the `match`, missing `Os`
import std.net
import std.time

Kind(e: net.Error): int {
    match e { // <- E0306
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
    }
}
