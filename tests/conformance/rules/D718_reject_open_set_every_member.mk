// verdict: reject E0306
// rule: D-718
// rule: LIB150
// note: `net.Error` is open, so outside std a match with one arm per member of this release does not cover what a later release adds; `mk fix` adds the `_` arm (reason: open)
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
    }
}
