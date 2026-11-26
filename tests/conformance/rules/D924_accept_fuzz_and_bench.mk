// verdict: ok
// rule: D-924
// note: std.testing registers a fuzz target and a benchmark (FuzzSys and BenchSys: ones that take the test's System) from test blocks with plain calls, no new syntax
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

// Oracle reaches the test's System, as a target that runs an external
// oracle through sys.io does.
Oracle(sys: &System, b: []u8) {
    var a := Arena.New(sys.heap).Must()
    defer a.Free()
    Assert(a.Slice[u8](b.Len()).Must().Len() == b.Len())
}

test "parse" {
    testing.Fuzz(Parse)
}

test "oracle" (sys: &System) {
    testing.FuzzSys(sys, Oracle)
}

test "loop" {
    testing.Bench(Loop)
}

test "fill" (sys: &System) {
    testing.BenchSys(sys, Fill)
}
