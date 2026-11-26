// Why: a carrier's system stack grows when a C call needs more of it, and
// the next C call on that carrier starts from the top again.
// What: Main, which makes a deep C call and then a shallower one that still
// reaches the pages the first committed, on the main task, on a task of a
// shared carrier and on tasks of a dedicated carrier.
// How: ext.Deep(200) uses about 800 KiB of the 8 MiB system stack, far past
// what a new thread has committed; ext.Deep(100) then runs about 400 KiB
// deep. Each prints 0 when its frame is inside the stack the thread's
// StackLimit describes. Want "0 0 0 0 0 0".

import ext
import std.runtime

Pair(a: mut u64, b: mut u64) {
    *a = ext.Deep(200)
    *b = ext.Deep(100)
}

One(frames: u64, out: mut u64) {
    *out = ext.Deep(frames)
}

export Main(sys: &System): WriteError!void {
    var m1: u64 = 9
    var m2: u64 = 9
    var s1: u64 = 9
    var s2: u64 = 9
    var d1: u64 = 9
    var d2: u64 = 9
    Pair(mut m1, mut m2) // two calls from one task
    scope s := sys.io.Scope() {
        s.go Pair(mut s1, mut s2) // the same from a spawned task
    }
    dio := runtime.Dedicated(sys.io)
    scope s := dio.Scope() {
        s.go One(200, mut d1) // one task grows the dedicated carrier's stack
    }
    scope s := dio.Scope() {
        s.go One(100, mut d2) // and a later task of that carrier calls into it
    }
    try sys.Print("{m1} {m2} {s1} {s2} {d1} {d2}\n", (m1, m2, s1, s2, d1, d2))
}
