// Why: std.testing must behave in a plain test run and parse its inputs.
// What: test blocks of Fuzz, FuzzSys, Bench, BenchSys, ExpectPanic, the
// bench spec, the input file's records and Mutate, run by
// `mk test --std=std.testing`.
// How: a plain mk test child with no fuzz corpus gets only
// MK_TESTING_EXPECT (the run's token) of the MK_TESTING_ variables, so
// Fuzz and FuzzSys call nothing, Bench calls f(1), BenchSys f(sys, 1)
// and ExpectPanic writes its line; internal/driver/fuzz_test.go and
// bench_test.go run the other modes.

import std.rt

NeverCalled(b: []u8) {
    _ = b
    Panic("a fuzz target ran with no corpus")
}

NeverCalledSys(sys: &System, b: []u8) {
    _ = sys
    _ = b
    Panic("a FuzzSys target ran with no corpus")
}

// TakeFromArena takes 64 KiB from its sys.arena, more than an arena's
// first chunks hold, so the bytes are a heap block the leak counters see.
TakeFromArena(sys: &System, b: []u8) {
    Assert(b.Len() == 1 && b[0] == b'x')
    Assert(sys.arena.Slice[u8](1 << 16).Must().Len() == 1 << 16)
}

OnlyOne(n: int) {
    Assert(n == 1)
}

// OnlyOneSys allocates from the heap of the System it is given.
OnlyOneSys(sys: &System, n: int) {
    Assert(n == 1)
    var a := Arena.New(sys.heap).Must()
    defer a.Free()
    Assert(a.Slice[u8](64).Must().Len() == 64)
}

test "Testing Fuzz with no corpus calls nothing, Bench calls f(1)" {
    Fuzz(NeverCalled)
    Bench(OnlyOne)
}

test "Testing BenchSys calls f(sys, 1)" (sys: &System) {
    BenchSys(sys, OnlyOneSys)
}

// mk test passes this block on its panic (internal/driver/expectpanic_test.go
// covers the failing outcomes); the harness judges its exit and report.
test "Testing ExpectPanic passes on the panic it names [exit 101: panic: assert]" {
    ExpectPanic("assert")
    Assert(Decimal("7".AsBytes(), 0).n == 8)
}

test "Testing FuzzSys with no corpus calls nothing" (sys: &System) {
    FuzzSys(sys, NeverCalledSys)
}

test "Testing a FuzzSys target's arena is freed when it returns" (sys: &System) {
    live := rt.LiveAllocations()
    Call(some(sys), none, some(TakeFromArena), "x".AsBytes())
    after := rt.LiveAllocations()
    Assert(after.count == live.count && after.bytes == live.bytes)
}

test "Testing Decimal and PutInt" {
    d := Decimal("12 345".AsBytes(), 0)
    Assert(d.n == 12 && d.end == 2)
    Assert(Decimal("12 345".AsBytes(), 3).n == 345)
    Assert(Decimal("x".AsBytes(), 0).n == 0)
    Assert(SpecN("0 20") == 0 && SpecReps("0 20") == 20 && SpecN("64 3") == 64 && SpecReps("64 3") == 3)
    var line: [32]u8 = [0; 32]
    w := PutInt(mut line[..], 0, 9_007_199_254_740_993)
    Assert(str.From(line[0..w]).Must() == "9007199254740993")
    Assert(PutInt(mut line[..], 0, 0) == 1 && line[0] == 48)
}

test "Testing the input file's records" {
    // 's' "ab" [1, 2, 3], then 'd' "" [9].
    b := [b's', 2, 0, 0, 0, b'a', b'b', 3, 0, 0, 0, 1, 2, 3, b'd', 0, 0, 0, 0, 1, 0, 0, 0, 9]
    Assert(DataAt(b[..], 0) == 11 && RecordEnd(b[..], 0) == 14)
    Assert(Nth(b[..], b'd', 0) == 14 && Nth(b[..], b's', 1) == -1)
    var buf: [8]u8 = [0; 8]
    Assert(CopySeed(b[..], 0, mut buf[..]) == 3 && buf[2] == 3)
}

test "Testing Mutate keeps the input within MaxLen and its bounds" {
    b := [b's', 0, 0, 0, 0, 2, 0, 0, 0, 7, 8, b'd', 0, 0, 0, 0, 3, 0, 0, 0, 1, 2, 3]
    var a := Arena.New(rt.ProcessHeap()).Must()
    buf := a.Slice[u8](MaxLen).Must()
    var n := 0
    var r: u64 = 1
    for _ in 0..20_000 {
        r = Step(r)
        Assert(r != 0)
        n = Mutate(r, buf, n, b[..], 1, 1)
        Assert(n >= 0 && n <= MaxLen)
    }
    a.Free()
}

test "Testing Bucket classes and rt CoverCmp ring buffer" {
    Assert(Bucket(1) == 1 && Bucket(2) == 2 && Bucket(3) == 4)
    Assert(Bucket(4) == 8 && Bucket(7) == 8 && Bucket(8) == 16 && Bucket(15) == 16)
    Assert(Bucket(16) == 32 && Bucket(31) == 32 && Bucket(32) == 64 && Bucket(127) == 64 && Bucket(128) == 128)

    rt.CoverCmpReset()
    rt.CoverCmpEnable(true)
    rt.CoverCmp(0x1111, 0x1111, 2) // equal operands ignored
    Assert(rt.CoverCmpCount() == 0)
    rt.CoverCmp(0x11223344, 0xCAFEBABE, 4)
    Assert(rt.CoverCmpCount() == 1)
    pair := rt.CoverCmpAt(0)
    Assert(pair.a == 0x11223344 && pair.b == 0xCAFEBABE && rt.CoverCmpWidthAt(0) == 4)
    rt.CoverCmpReset()
    Assert(rt.CoverCmpCount() == 0)
    rt.CoverCmpEnable(false)
}
