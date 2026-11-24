// Why: fuzzing and benchmarking should need no new syntax: a test block
// calls a function, and the driver decides what happens.
//
// What: Fuzz, Bench, RunSeeds, the mutation loop (Loop, Mutate, Guided)
// and the small decimal helpers it reads and writes with.
//
// How: the driver (internal/driver/fuzz.go and bench.go) tells the test
// child what to do through environment variables; with none set, Fuzz
// calls nothing and Bench calls f(1) once, so a plain test run passes over
// both. The protocol (environment variables, read once):
//
//   - MK_TESTING_FUZZ: "run" calls f once per seed of the input file, in
//     its order, writing "fuzz input <name>" to standard error before each
//     call, so a failing input is named in the test's output; "loop" runs
//     the mutation loop below.
//   - MK_TESTING_INPUT: the input file, records of a kind byte ('s' a seed,
//     'd' a dictionary token), a u32 little-endian name length, the name,
//     a u32 little-endian data length and the data. The driver writes it
//     from testdata/fuzz/<test name>/ and the target's constants.
//   - MK_TESTING_STATE (loop): a file the driver made, which the loop
//     overwrites before every call with a u64 call count, a u64 input
//     length and the input, all little-endian, from offset 0. The driver
//     reads it after a panic or a hang to save the input.
//   - MK_TESTING_SEED (loop): the generator's seed, a decimal; 0 or absent
//     is 1.
//   - MK_TESTING_ITERS (loop): stop after this many calls, a decimal; 0 or
//     absent runs until the driver stops the child.
//   - MK_TESTING_NANOS (loop): stop once this many nanoseconds have passed,
//     checked every 256 calls, so the child ends at the fuzz time by
//     itself, also through a runner (wsl.exe) whose kill may not reach it;
//     0 or absent: no limit.
//   - MK_TESTING_BENCH: "<n> <reps>", a decimal n (0: grow n from 1 until
//     one call of f takes at least 20 ms) and a decimal repetition count;
//     each repetition writes "mkbench <n> <nanoseconds>" to standard error.
//
// The loop mutates by bit flips, byte sets, inserts, deletes, splices of
// another input and dictionary tokens, and is guided by the package's live
// block counters (Guided): an input that reaches a new block joins the
// loop's pool, and later inputs are mutated from it.

import std.fs
import std.rt
import std.sys

// MaxLen is the longest input the loop makes, in bytes.
const MaxLen: int = 4096

// PoolSlots is how many inputs Guided may add to the loop's pool.
const PoolSlots: int = 64

// CmpCap is how many live comparison pairs from std.rt.CoverCmp Loop retains.
const CmpCap: int = 128

// MaxBenchN is the largest n the calibration grows to (2^30): a body
// that ignores n never takes 20 ms, and n must stay a decimal the driver's
// spec and Decimal carry.
const MaxBenchN: int = 1 << 30

// BenchTarget is the time a calibrated call of a benchmark's f takes at
// least, in nanoseconds (20 ms).
const BenchTarget: i64 = 20_000_000

/// Fuzz makes f the fuzz target of the test block that calls it, and runs
/// it. Plain `mk test` calls f once per file of testdata/fuzz/<test name>/
/// except the .report files, in name order, with the file's bytes, and
/// names each file in the test's output before the call.
/// `mk test --fuzz <test name>` instead calls f on inputs it mutates,
/// guided by the blocks they reach, until the fuzz time ends or 20 runs
/// have failed. It saves an input that panics, leaks, hangs or exits
/// non-zero into that directory, one input per distinct failure. f fails
/// by panicking. Under plain `mk test` with no corpus files, and in any run
/// outside `mk test`, f is not called.
///
/// It allocates its inputs from the process heap and frees them before it
/// returns; O(the corpus) in plain mode.
export Fuzz(f: fn([]u8)) {
    mode := rt.Env("MK_TESTING_FUZZ") else {
        return
    }
    path := rt.Env("MK_TESTING_INPUT") else {
        return
    }
    var a := Arena.New(rt.ProcessHeap()).Must()
    io := rt.NativeIo()
    blob := fs.ReadBytes(&a, io, path).Must()
    if mode == "run" {
        RunSeeds(blob, f)
    } else if mode == "loop" {
        Loop(&a, io, blob, f)
    } else {
        Panic("std.testing: MK_TESTING_FUZZ is neither run nor loop")
    }
    a.Free()
}

/// Bench makes f the benchmark of the test block that calls it. f(n) does
/// its work n times. Under `mk bench` it doubles n from 1 until one call
/// takes at least 20 ms (n stops at 2^30), then times f(n) once per
/// repetition on the monotonic clock and reports each time on standard
/// error, from which `mk bench` computes its statistics. Anywhere else,
/// plain `mk test` included, it calls f(1) once, so a test run checks that
/// the benchmark works. `mk bench` finds a benchmark by the text
/// `testing.Bench(` in its test block, so import std.testing under its own
/// name.
///
/// Bench itself allocates nothing; O(the repetitions' calls of f).
export Bench(f: fn(n: int)) {
    spec := rt.Env("MK_TESTING_BENCH") else {
        f(1)
        return
    }
    b := spec.AsBytes()
    first := Decimal(b, 0)
    var n := first.n
    reps := Decimal(b, first.end + 1).n
    if n <= 0 {
        n = 1
        for {
            t := Time(f, n)
            if t >= BenchTarget || n >= MaxBenchN {
                break
            }
            n *= 2
        }
    }
    for _ in 0..reps {
        t := Time(f, n)
        var line: [64]u8 = [0; 64]
        var w := Put(mut line[..], 0, "mkbench ")
        w = PutInt(mut line[..], w, Trunc[i64](n))
        w = Put(mut line[..], w, " ")
        w = PutInt(mut line[..], w, t)
        w = Put(mut line[..], w, "\n")
        rt.WriteErr(line[0..w])
    }
}

// Time is the nanoseconds one call f(n) takes on the monotonic clock.
Time(f: fn(n: int), n: int): i64 {
    t0 := sys.Now()
    f(n)
    return sys.Now() - t0
}

// Decimal reads the unsigned decimal at b[at..]: its value (0 when there
// is none) and the offset of the first byte after it.
Decimal(b: []u8, at: int): (n: int, end: int) {
    var n := 0
    var i := at
    for i < b.Len() && b[i] >= 48 && b[i] <= 57 && n < 100_000_000_000 {
        n = n * 10 + int(b[i]) - 48
        i += 1
    }
    return (n: n, end: i)
}

// Put copies s into line at w and returns the end.
Put(line: mut []u8, w: int, s: str): int {
    var k := w
    for c in s.AsBytes() {
        line[k] = c
        k += 1
    }
    return k
}

// PutInt writes v, which is at least 0, in decimal into line at w and
// returns the end.
PutInt(line: mut []u8, w: int, v: i64): int {
    var digits: [20]u8 = [0; 20]
    var n := 0
    var x := if v > 0 { v } else { 0 }
    for {
        digits[n] = 48 + Trunc[u8](x % 10)
        n += 1
        x /= 10
        if x == 0 {
            break
        }
    }
    var k := w
    for j in 0..n {
        line[k] = digits[n - 1 - j]
        k += 1
    }
    return k
}

// U32 is the little-endian u32 at b[i..i+4] as an int.
U32(b: []u8, i: int): int {
    return int(b[i]) | int(b[i + 1]) << 8 | int(b[i + 2]) << 16 | int(b[i + 3]) << 24
}

// RecordEnd is the offset after the record at i of the input file, whose
// kind is b[i]; its name is b[i+5..d-4] and its data b[d..end], d being
// DataAt(b, i).
RecordEnd(b: []u8, i: int): int {
    d := DataAt(b, i)
    return d + U32(b, d - 4)
}

// DataAt is the offset of the data of the record at i.
DataAt(b: []u8, i: int): int {
    return i + 5 + U32(b, i + 1) + 4
}

// RunSeeds calls f once per seed record of the input file b, in its order.
RunSeeds(b: []u8, f: fn([]u8)) {
    var i := 0
    for i < b.Len() {
        d := DataAt(b, i)
        end := RecordEnd(b, i)
        if b[i] == b's' {
            rt.WriteErr("fuzz input ".AsBytes())
            rt.WriteErr(b[i + 5..d - 4])
            rt.WriteErr("\n".AsBytes())
            f(b[d..end])
        }
        i = end
    }
}

// Step is the xorshift64 generator's next state (Marsaglia, 2003).
Step(x: u64): u64 {
    var s := x ^ (x << 13)
    s = s ^ (s >> 7)
    return s ^ (s << 17)
}

// Below is a number in [0, n) from the state r; n is at least 1.
Below(r: u64, n: int): int {
    return Trunc[int](r >> 33) % n
}

// Bucket maps a non-zero per-run block hit delta to an AFL-style 8-bit
// hit-count class bit (1, 2, 3, 4-7, 8-15, 16-31, 32-127, 128+).
Bucket(d: u64): u8 {
    if d == 1 {
        return 1
    }
    if d == 2 {
        return 2
    }
    if d == 3 {
        return 4
    }
    if d <= 7 {
        return 8
    }
    if d <= 15 {
        return 16
    }
    if d <= 31 {
        return 32
    }
    if d <= 127 {
        return 64
    }
    return 128
}

// Guided is the guidance hook: whether the input just run reached a block or
// AFL hit-count bucket no earlier input reached, computed from per-run deltas
// against live process counters (std.rt.CoverLive/CoverLoad), so the loop
// keeps it in its pool. seen holds an 8-bit bucket bitmask per counter, and
// prev holds the cumulative counter snapshots across calls. O(n counters);
// allocates nothing.
Guided(base: uint, seen: mut []u8, prev: mut []u64): bool {
    var fresh := false
    for i in 0..seen.Len() {
        cur := rt.CoverLoad(base, i)
        delta := cur -% prev[i]
        prev[i] = cur
        if delta != 0 {
            bit := Bucket(delta)
            if (seen[i] & bit) == 0 {
                seen[i] = seen[i] | bit
                fresh = true
            }
        }
    }
    return fresh
}

// Loop is the mutation loop of `mk test --fuzz`: from the seeds of b (or
// one empty input) and the inputs Guided kept, it mutates a copy, records
// it in the state file, and calls f, until MK_TESTING_ITERS calls (or for
// ever, the driver stopping the child).
Loop(a: &Arena, io: Io, b: []u8, f: fn([]u8)) {
    state_path := rt.Env("MK_TESTING_STATE") else {
        Panic("std.testing: MK_TESTING_FUZZ=loop without MK_TESTING_STATE")
    }
    var seed := Trunc[u64](EnvDecimal("MK_TESTING_SEED"))
    if seed == 0 {
        seed = 1
    }
    iters := EnvDecimal("MK_TESTING_ITERS")
    budget := Trunc[i64](EnvDecimal("MK_TESTING_NANOS"))
    start := sys.Now()
    var seeds := 0
    var dicts := 0
    var i := 0
    for i < b.Len() {
        if b[i] == b's' {
            seeds += 1
        } else {
            dicts += 1
        }
        i = RecordEnd(b, i)
    }
    // The record frame is 16 header bytes and the input.
    frame := a.Slice[u8](16 + MaxLen).Must()
    pool := a.Slice[u8](PoolSlots * MaxLen).Must()
    pool_len := a.Slice[int](PoolSlots).Must()
    live := rt.CoverLive()
    seen := a.Slice[u8](live.n).Must()
    prev := a.Slice[u64](live.n).Must()
    for k in 0..live.n {
        seen[k] = 0
        prev[k] = 0
    }
    cmp_a := a.Slice[u64](CmpCap).Must()
    cmp_b := a.Slice[u64](CmpCap).Must()
    cmp_w := a.Slice[u8](CmpCap).Must()
    var cmp_n := 0
    var cmp_next := 0
    rt.CoverCmpEnable(true)
    // The counters and hit-count buckets setup already reached are baseline.
    _ = Guided(live.base, seen, prev)
    var pooled := 0
    var state := fs.OpenWith(io, state_path, fs.ReadWrite).Must()
    var r := Step(seed *% 0x9E3779B97F4A7C15 | 1)
    var count: i64 = 0
    _ = state.WriteAt(io, 0, frame[0..16]).Must()
    for iters == 0 || count < Trunc[i64](iters) {
        if budget > 0 && count % 256 == 0 && sys.Now() - start >= budget {
            break
        }
        // The base input: a seed, a pooled input or empty.
        var n := 0
        r = Step(r)
        pick := Below(r, seeds + pooled + 1)
        if pick < seeds {
            n = CopySeed(b, pick, mut frame[16..])
        } else if pick < seeds + pooled {
            k := pick - seeds
            n = pool_len[k]
            for j in 0..n {
                frame[16 + j] = pool[k * MaxLen + j]
            }
        }
        r = Step(r)
        rounds := 1 + Below(r, 4)
        for _ in 0..rounds {
            r = Step(r)
            n = MutateGuided(r, mut frame[16..], n, b, seeds, dicts, pool[..], pool_len[..], pooled, cmp_a[..], cmp_b[..], cmp_w[..], cmp_n)
        }
        count += 1
        PutU64(mut frame[..], 0, Trunc[u64](count))
        PutU64(mut frame[..], 8, Trunc[u64](n))
        _ = state.WriteAt(io, 0, frame[0..16 + n]).Must()
        rt.CoverCmpReset()
        f(frame[16..16 + n])
        cnt := rt.CoverCmpCount()
        for ci in 0..cnt {
            w := rt.CoverCmpWidthAt(ci)
            if w != 2 && w != 4 && w != 8 {
                continue
            }
            pair := rt.CoverCmpAt(ci)
            var dup := false
            for j in 0..cmp_n {
                if cmp_w[j] == w && cmp_a[j] == pair.a && cmp_b[j] == pair.b {
                    dup = true
                    break
                }
            }
            if !dup {
                slot := if cmp_n < CmpCap { cmp_n } else { cmp_next }
                cmp_a[slot] = pair.a
                cmp_b[slot] = pair.b
                cmp_w[slot] = w
                if cmp_n < CmpCap {
                    cmp_n += 1
                } else {
                    cmp_next = (cmp_next + 1) & (CmpCap - 1)
                }
            }
        }
        if Guided(live.base, seen, prev) && n > 0 {
            k := if pooled < PoolSlots { pooled } else { Below(r, PoolSlots) }
            for j in 0..n {
                pool[k * MaxLen + j] = frame[16 + j]
            }
            pool_len[k] = n
            if pooled < PoolSlots {
                pooled += 1
            }
        }
    }
    rt.CoverCmpEnable(false)
    state.Close(io).Must()
}

// EnvDecimal is the decimal value of the variable name, 0 when unset.
EnvDecimal(name: str): int {
    v := rt.Env(name) else {
        return 0
    }
    return Decimal(v.AsBytes(), 0).n
}

// PutU64 writes v little-endian at b[i..i+8].
PutU64(b: mut []u8, i: int, v: u64) {
    for k in 0..8 {
        b[i + k] = Trunc[u8](v >> Trunc[u64](8 * k))
    }
}

// Nth is the offset of the k-th record of kind c in b; -1 when there is
// none.
Nth(b: []u8, c: u8, k: int): int {
    var i := 0
    var seen := 0
    for i < b.Len() {
        if b[i] == c {
            if seen == k {
                return i
            }
            seen += 1
        }
        i = RecordEnd(b, i)
    }
    return -1
}

// CopySeed copies seed k of b, cut to MaxLen, into dst and returns its
// length.
CopySeed(b: []u8, k: int, dst: mut []u8): int {
    at := Nth(b, b's', k)
    d := DataAt(b, at)
    var n := RecordEnd(b, at) - d
    if n > MaxLen {
        n = MaxLen
    }
    for j in 0..n {
        dst[j] = b[d + j]
    }
    return n
}

// ByteOf returns byte k of w-byte integer v in big-endian or little-endian order.
ByteOf(v: u64, w: int, k: int, big: bool): u8 {
    shift := if big { w - 1 - k } else { k }
    return Trunc[u8](v >> Trunc[u64](8 * shift))
}

// MatchesVal reports whether buf[pos..pos+w] encodes w-byte integer v.
MatchesVal(buf: []u8, pos: int, v: u64, w: int, big: bool): bool {
    for k in 0..w {
        if buf[pos + k] != ByteOf(v, w, k, big) {
            return false
        }
    }
    return true
}

// WriteVal overwrites buf[pos..pos+w] with w-byte integer v.
WriteVal(buf: mut []u8, pos: int, v: u64, w: int, big: bool) {
    for k in 0..w {
        buf[pos + k] = ByteOf(v, w, k, big)
    }
}

// Mutate applies one unguided mutation chosen by r to buf[0..n].
Mutate(r: u64, buf: mut []u8, n: int, b: []u8, seeds: int, dicts: int): int {
    var empty_u8: [0]u8 = []
    var empty_int: [0]int = []
    var empty_u64: [0]u64 = []
    return MutateGuided(r, buf, n, b, seeds, dicts, empty_u8[..], empty_int[..], 0, empty_u64[..], empty_u64[..], empty_u8[..], 0)
}

// MutateGuided applies one mutation chosen by r to the input buf[0..n] (buf
// holds MaxLen bytes), including pool splicing, Redqueen comparison operand
// replacement, and live CoverCmp constant splicing, returning the new length.
MutateGuided(r: u64, buf: mut []u8, n: int, b: []u8, seeds: int, dicts: int, pool: []u8, pool_len: []int, pooled: int, cmp_a: []u64, cmp_b: []u64, cmp_w: []u8, cmp_n: int): int {
    op := Below(r, 8)
    at := Below(Step(r), n + 1)
    v := Trunc[u8](Step(Step(r)) >> 13)
    if op == 0 && n > 0 && at < n {
        buf[at] = buf[at] ^ (1 << Trunc[u8](r & 7))
        return n
    }
    if op == 1 && n > 0 && at < n {
        buf[at] = v
        return n
    }
    if op == 2 && n < MaxLen {
        var j := n
        for j > at {
            buf[j] = buf[j - 1]
            j -= 1
        }
        buf[at] = v
        return n + 1
    }
    if op == 3 && n > 0 && at < n {
        for j in at..n - 1 {
            buf[j] = buf[j + 1]
        }
        return n - 1
    }
    if op == 4 && (seeds > 0 || pooled > 0) {
        // Splice: overwrite from at with a piece of a seed or pooled input.
        use_pool := pooled > 0 && (seeds == 0 || (Step(Step(r)) & 1) == 0)
        if use_pool {
            pk := Below(Step(r), pooled)
            sn := pool_len[pk]
            if sn == 0 {
                return n
            }
            from := Below(Step(Step(r)), sn)
            var k := 0
            for from + k < sn && at + k < MaxLen && k < 64 {
                buf[at + k] = pool[pk * MaxLen + from + k]
                k += 1
            }
            return if at + k > n { at + k } else { n }
        }
        s := Nth(b, b's', Below(Step(r), seeds))
        d := DataAt(b, s)
        sn := RecordEnd(b, s) - d
        if sn == 0 {
            return n
        }
        from := Below(Step(Step(r)), sn)
        var k := 0
        for from + k < sn && at + k < MaxLen && k < 64 {
            buf[at + k] = b[d + from + k]
            k += 1
        }
        return if at + k > n { at + k } else { n }
    }
    if op == 5 && dicts > 0 {
        // A dictionary token, inserted at at.
        s := Nth(b, b'd', Below(Step(r), dicts))
        d := DataAt(b, s)
        tn := RecordEnd(b, s) - d
        if tn == 0 || n + tn > MaxLen {
            return n
        }
        var j := n + tn - 1
        for j >= at + tn {
            buf[j] = buf[j - tn]
            j -= 1
        }
        for k in 0..tn {
            buf[at + k] = b[d + k]
        }
        return n + tn
    }
    if (op == 6 || op == 7) && cmp_n > 0 {
        ci := Below(Step(r), cmp_n)
        ca := cmp_a[ci]
        cb := cmp_b[ci]
        cw := int(cmp_w[ci])
        if cw == 2 || cw == 4 || cw == 8 {
            if op == 6 && n >= cw {
                for pos in 0..n - cw + 1 {
                    if MatchesVal(buf[..], pos, ca, cw, false) {
                        WriteVal(buf, pos, cb, cw, false)
                        return n
                    }
                    if MatchesVal(buf[..], pos, cb, cw, false) {
                        WriteVal(buf, pos, ca, cw, false)
                        return n
                    }
                    if MatchesVal(buf[..], pos, ca, cw, true) {
                        WriteVal(buf, pos, cb, cw, true)
                        return n
                    }
                    if MatchesVal(buf[..], pos, cb, cw, true) {
                        WriteVal(buf, pos, ca, cw, true)
                        return n
                    }
                }
            }
            r2 := Step(r)
            r3 := Step(r2)
            val := if (r2 & 1) == 0 { cb } else { ca }
            big := (r2 & 6) == 6
            if n >= cw && (r3 & 1) == 0 {
                pos := Below(Step(r3), n - cw + 1)
                WriteVal(buf, pos, val, cw, big)
                return n
            }
            if at + cw <= MaxLen {
                WriteVal(buf, at, val, cw, big)
                return if at + cw > n { at + cw } else { n }
            }
        }
    }
    // The chosen mutation does not apply: set a byte, or grow by one.
    if n > 0 && at < n {
        buf[at] = v
        return n
    }
    if n < MaxLen {
        buf[n] = v
        return n + 1
    }
    return n
}
