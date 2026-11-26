// verdict: ok
// rule: D-924
// note: std.testing registers a fuzz target and a benchmark (BenchSys: one that allocates from the test's System) from test blocks with plain calls, no new syntax
import std.testing

Parse(b: []u8) {
    Assert(b.Len() >= 0)
}

Loop(n: int) {
    var s := 0
    for i in 0..n {
        s += i
    }
    Assert(s >= 0)
}

Fill(sys: &System, n: int) {
    var a := Arena.New(sys.heap).Must()
    defer a.Free()
    var xs := List[int].New(&a)
    for i in 0..n {
        xs.Push(i).Must()
    }
    Assert(xs.Len() == n)
}

test "parse" {
    testing.Fuzz(Parse)
}

test "loop" {
    testing.Bench(Loop)
}

test "fill" (sys: &System) {
    testing.BenchSys(sys, Fill)
}
