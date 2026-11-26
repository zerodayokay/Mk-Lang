# std.math

`std.math` has the elementary functions (roots, powers, exponentials, logarithms, trigonometric and hyperbolic functions). It also has exact float operations such as rounding and splitting, integer bit operations, checked and wrapping arithmetic, a float's decimal digits, and a seeded random generator. Every function returns the same bits on every target and at compile time. For integers of any size use [`std.big`](big.md); for random numbers that must stay secret use [`crypto.Random`](crypto.md).

Read the [standard library guide](README.md) first.

```mk
// verdict: parse
import std.math
```

## Contents

- [The main pieces](#the-main-pieces)
- [The same bits everywhere](#the-same-bits-everywhere)
- [NaN](#nan)
- [Accuracy](#accuracy)
- [Integers](#integers)
- [Decimal digits](#decimal-digits)
- [Random numbers](#random-numbers)
- [Memory and cost](#memory-and-cost)
- [Examples](#examples): [float functions](#float-functions), [checked and wrapping arithmetic](#checked-and-wrapping-arithmetic), [bit operations](#bit-operations), [random numbers with a seed](#random-numbers-with-a-seed), [decimal digits of a float](#decimal-digits-of-a-float)
- [API](#api)

## The main pieces

| Task | Use |
|---|---|
| Constants | `Pi`, `E`, `Ln2`, `Sqrt2`, `Inf`, `Nan` (all `f64`) |
| Smaller, larger, magnitude | `Min`, `Max` (any ordered type), `Abs`, `CopySign` |
| Roots and powers | `Sqrt`, `Cbrt`, `Hypot`, `Pow`, `Fma` |
| Exponentials and logarithms | `Exp`, `Exp2`, `Expm1`, `Log`, `Log2`, `Log10`, `Log1p` |
| Trigonometry | `Sin`, `Cos`, `Tan`, `Asin`, `Acos`, `Atan`, `Atan2` |
| Hyperbolic functions | `Sinh`, `Cosh`, `Tanh`, `Asinh`, `Acosh`, `Atanh` |
| Round to an integer | `Floor`, `Ceil`, `RoundToZero`, `Round` (half away from zero), `RoundEven` (half to even) |
| Take a float apart | `Fract`, `FrExp`, `LdExp`, `NextAfter`, `Mod` |
| Classify a float | `IsNan`, `IsInf`, `IsFinite` |
| Count, rotate and reverse bits | `LeadingZeros64`, `TrailingZeros64`, `OnesCount64`, `RotateLeft64`, `RotateRight64`, `ReverseBits64`, `ReverseBytes64`, and the same at 32, 16 and 8 bits (`ReverseBytes` at 32 and 16) |
| Exact integer arithmetic | `Gcd`, `CheckedPow`, `CheckedPowU64`, `AddOverflow`, `SubOverflow`, `MulOverflow` |
| Arithmetic on 128 bits and on many words | `MulWide`, `DivWide`, `AddCarry`, `SubBorrow` |
| Float to integer | `CastSat`, `CastSatF32` (saturating), `WrapI32`, `WrapU32` (wrapping) |
| A float's decimal digits | `ShortestDigits`, `ExactDigits`, which return a `Digits`; `ShortestDigitsInto`, into a buffer of 17 bytes |
| Random numbers | `Rng.New` (from a seed), `Rng.FromIo` (from the OS), then `U64`, `Below`, `Float`, `Fill` |

The float functions come in `f64`, and with an `F32` suffix in `f32`: `Sin` and `SinF32`, `Floor` and `FloorF32`. The constants, `Abs`, `WrapI32`, `WrapU32` and the digit functions are `f64` only.

## The same bits everywhere

Every `std.math` function is written in Mk over IEEE 754 operations, and its result never depends on the CPU. So a function returns the same bits on Linux and Windows, in a dev and a release build, and at compile time, where a `const` initializer calls it. The compiler never fuses `a * b + c` into one rounding; call `Fma` for that.

`Floor`, `Ceil`, `RoundToZero`, `Round` and `RoundEven` (and their `F32` forms) compile to the CPU's rounding instruction wherever the calling function's features include `sse4.1`, which every hosted x86_64 target's do. Either way the bits are those of the software form. `Fma` uses the fused multiply-add instruction wherever `simd.Cpu()` reports `fma` on the running CPU, and a software routine elsewhere and at compile time. IEEE 754 defines the instruction's result bit for bit, so both give the same bits; the software routine is the reference the instruction is tested against.

```mk
// verdict: ok
import std.math

// computed by the compiler, stored in the program as constants
const AtCompileTime: [3]u64 = [math.Exp(1.0).Bits(), math.Sin(1e22).Bits(), math.Log(-1.0).Bits()]

export Main(sys: &System): WriteError!void {
    xs := [1.0, 1e22, -1.0]
    at_run_time := [math.Exp(xs[0]).Bits(), math.Sin(xs[1]).Bits(), math.Log(xs[2]).Bits()]
    for i in 0..3 {
        same := AtCompileTime[i] == at_run_time[i]
        try sys.Print("{c:x} {same}\n", (c: AtCompileTime[i], same))
    }
}
```

```text
$ mk run same.mk
4005bf0a8b145769 true
bfeb453ab76bf397 true
7ff8000000000000 true
```

## NaN

A NaN that a `std.math` function computes is always `Nan`: the bits `0x7FF8000000000000`, or `0x7FC00000` in `f32`, whatever NaN its inputs held or the hardware made. `Abs`, `CopySign`, `Min` and `Max` only set a sign bit or return an argument, so they keep their argument's bits, a NaN's payload included.

`Min` and `Max` return `a` when neither argument is less, so `Min(Nan, 1.0)` is NaN but `Min(1.0, Nan)` is 1; test with `IsNan` first when a NaN may reach them. They take any ordered plain type, integers included.

An arithmetic NaN's sign and payload are unspecified ([memory](../memory.md#r19-unsafe)); they survive only where your code spells them in bits (`f64.FromBits`, `Bits()`). The optimizer may merge a guard such as `if x != x || x < 0.0 { return math.Nan }` into the operation it guards, and then the NaN is the hardware's. To return `Nan`'s bits from a guard of your own, write its condition on the encoding, as `Sqrt` does with its raw root `r`: `if r.Bits() & 0x7FFFFFFFFFFFFFFF > 0x7FF0000000000000 { return math.Nan }`.

## Accuracy

| Functions | Result |
|---|---|
| The rounding functions, `Abs`, `Fract`, `CopySign`, `FrExp`, `NextAfter`, `Mod`, the classifiers | Exact |
| `LdExp`, `LdExpF32` | Correctly rounded (exact unless the result is subnormal or out of range) |
| `Sqrt`, `Fma`, and every `F32` form of the exponentials, logarithms, trigonometric, inverse trigonometric and hyperbolic functions and `Cbrt`, plus `HypotF32` | Correctly rounded for every input |
| `Log`, `Sinh`, `Cosh`, `Tanh`, `Asinh`, `Acosh`, `Atanh`, `Atan2F32`, `PowF32` | Correctly rounded on every input tested, not proved for all |
| `Exp`, `Exp2`, `Expm1`, `Log2`, `Log10`, `Log1p`, `Sin`, `Cos`, `Tan`, `Asin`, `Acos`, `Atan`, `Atan2`, `Pow`, `Hypot`, `Cbrt` | Faithfully rounded: within 1 ulp |

Each function's doc text in the [API](#api) says "correctly rounded" or states its largest error. A correctly rounded function never changes its results. Any other function's results may change in a minor release.

`Mod` is the exact remainder of truncated division, with the sign of `x`, the same as the `%` operator on floats. `Sin`, `Cos` and `Tan` reduce a huge argument by pi/2 without losing bits, so `Sin(1e22)` keeps the same accuracy as `Sin(1.0)`.

## Integers

- The bit operations take the unsigned type of their width and have the width in their name: `OnesCount32(x: u32)`. Pass a signed value through `Cast` when it is not negative, or `Trunc` to keep the bits of a negative one ([conversions](../reference.md#conversions)).
- `CheckedPow` and `CheckedPowU64` are `none` on overflow.
- `AddOverflow`, `SubOverflow` and `MulOverflow` work on any integer type and return the wrapped value and whether the exact result did not fit.
- `MulWide` is the full 128-bit product of two `u64`. `DivWide` divides a 128-bit value by a `u64` and panics with a division by zero (code 3) when `d` is 0, and otherwise with an overflow (code 1) when `hi >= d`, so the quotient always fits a `u64`. `AddCarry` and `SubBorrow` take and return a carry or borrow of 0 or 1, so a chain of calls adds or subtracts numbers of many words.
- `WrapI32` and `WrapU32` convert as JavaScript's `ToInt32` and `ToUint32` (ECMA-262): NaN and the infinities give 0, any other value is truncated toward zero, modulo 2^32.
- `CastSat` converts a float to an integer, truncating toward zero and saturating at the type's range; NaN gives 0. A plain `Cast` panics instead.
- `math.Integer` is the interface of the integer types, `i8` to `i64`, `u8` to `u64`, `int` and `uint`. The compiler supplies it: an `impl` of it is E0704.

## Decimal digits

`ShortestDigits` and `ExactDigits` give a float's decimal digits for a printer that lays them out itself, such as one for another language's number format. Each returns a plain `Digits`:

- `digits[0..len]` is ASCII, with no leading or trailing zero. The value is `d.ddd... * 10^exp`, so `0.1` is the digit `1` with `exp` -1.
- `neg` is the sign bit, so `-0.0` is negative.
- `len` is 0 for a zero, a NaN, an infinity and a value that rounds away to zero; tell those apart from the value.

`ShortestDigits` gives the digits `{x}` prints: the fewest that read back to the same bits. `ExactDigits` rounds the float's exact binary value to `n` digits. Its `DigitMode` says what `n` counts and how a tie rounds:

| Mode | `n` counts | A tie rounds |
|---|---|---|
| `Significant` | Significant digits, at least 1 | To even |
| `Fraction` | Digits after the point, at least 0 | To even |
| `SignificantHalfUp` | Significant digits, at least 1 | Away from zero, as JavaScript's `toPrecision` and `toExponential` |
| `FractionHalfUp` | Digits after the point, at least 0 | Away from zero, as JavaScript's `toFixed` |

Any other `n` panics with code 8.

`ShortestDigitsInto(out, x)` writes `ShortestDigits`'s digits into `out[0..len]` and returns `(len, exp)`, for a printer that formats many numbers: a `Digits` is 792 bytes, and `ShortestDigits` zeroes its 768-byte array on every call. An `f64` has at most 17 shortest digits, so `out` needs 17 bytes, and a shorter `out` panics with code 8 whatever `x` is. It writes nothing past `out[0..17]` and zeroes nothing; past `len` the buffer may hold `'0'`s. The sign is `x`'s.

## Random numbers

`Rng` is a pseudo-random generator (xoshiro256\*\*, seeded through SplitMix64) for tests, simulations, sampling and shuffles. It is a plain 32-byte value with no `Io`:

- `Rng.New(seed)` gives one sequence for each seed, the same on every target, at compile time and in every release. Every seed is valid, 0 included.
- `Rng.FromIo(io)` seeds from the OS, for a sequence that differs on every run. It is the one call in `std.math` that takes an `Io`, and it fails only with `Canceled`.
- `U64` gives 64 random bits. `Below(n)` is uniform on 0 to n - 1, with no bias, and panics when `n` is 0. `Float` is uniform on [0, 1), with 53 random bits, and never returns 1. `Fill` fills a byte slice.

`Rng` is not for secrets: whoever sees four outputs can compute every later one. Use `crypto.Random` for keys, tokens and nonces.

## Memory and cost

Nothing in `std.math` allocates. Every function is O(1) except `Gcd` (O(log a + log b)), `CheckedPow` and `CheckedPowU64` (O(log n)), `Mod` and `ModF32` (O(the exponent difference)), `ExactDigits` with more than 18 digits (O(767)) and `Rng.Fill` (O(n)). No function panics except `DivWide`, `ExactDigits`, `ShortestDigitsInto` and `Rng.Below` on the arguments their docs name. The module needs no `Io` except `Rng.FromIo`, so it runs at compile time. In a freestanding program its integer functions work; its float functions need the bin's `cpu` set to `"baseline"`, else they are E1108.

## Examples

Each program below runs as written with `mk run`.

### Float functions

```mk
// verdict: ok
import std.math

export Main(sys: &System): WriteError!void {
    side := math.Hypot(3.0, 4.0)               // sqrt(3^2 + 4^2) without overflow
    angle := math.Atan2(1.0, 1.0) * 180.0 / math.Pi
    growth := math.Pow(1.05, 10.0)             // ten years at 5%
    try sys.Print("hypot {side}\n", (side))
    try sys.Print("angle {angle}\n", (angle))
    try sys.Print("growth {growth:.4}\n", (growth))
    try sys.Print("round {r} {e}\n", (r: math.Round(2.5), e: math.RoundEven(2.5)))
    try sys.Print("cbrt {c} log2 {l}\n", (c: math.Cbrt(-27.0), l: math.Log2(1024.0)))
    bad := math.Sqrt(-1.0)                     // Nan, the same bits everywhere
    try sys.Print("sqrt(-1) {bad} bits {b:x}\n", (bad, b: bad.Bits()))
    try sys.Print("nan? {n}\n", (n: math.IsNan(bad)))
}
```

```text
$ mk run float.mk
hypot 5
angle 45
growth 1.6289
round 3 2
cbrt -3 log2 10
sqrt(-1) nan bits 7ff8000000000000
nan? true
```

- `Round` takes a half away from zero, `RoundEven` to the even neighbour.
- `{x}` prints the shortest digits that read back to `x`; `{x:.4}` rounds to four digits after the point.
- A function given an argument outside its domain returns `Nan`; none of them panics.

### Checked and wrapping arithmetic

```mk
// verdict: ok
import std.math

export Main(sys: &System): WriteError!void {
    // CheckedPow is none when the result leaves i64's range
    try sys.Print("3^20 {a}\n", (a: math.CheckedPow(3, 20)))
    try sys.Print("3^40 {a}\n", (a: math.CheckedPow(3, 40)))

    // the wrapped value and whether it overflowed
    s := math.AddOverflow[u8](200, 100)
    try sys.Print("200 + 100 in u8: {v} overflow {o}\n", (v: s.value, o: s.overflow))
    m := math.MulOverflow[i32](65536, 65536)
    try sys.Print("65536 * 65536 in i32: {v} overflow {o}\n", (v: m.value, o: m.overflow))

    // the full 128-bit product, then back through DivWide
    w := math.MulWide(0xFFFFFFFFFFFFFFFF, 3)
    try sys.Print("wide {hi} {lo:x}\n", (hi: w.hi, lo: w.lo))
    d := math.DivWide(w.hi, w.lo, 3)
    try sys.Print("back {q:x} rem {r}\n", (q: d.q, r: d.r))

    // float to integer: saturating, and JavaScript's ToInt32
    try sys.Print("sat {a} {b} {c}\n", (a: math.CastSat[u8](300.0), b: math.CastSat[i8](-1000.5), c: math.CastSat[int](math.Nan)))
    try sys.Print("wrap {a} {b}\n", (a: math.WrapI32(4294967297.0), b: math.WrapI32(-1.5)))
}
```

```text
$ mk run checked.mk
3^20 some(3486784401)
3^40 none
200 + 100 in u8: 44 overflow true
65536 * 65536 in i32: 0 overflow true
wide 2 fffffffffffffffd
back ffffffffffffffff rem 0
sat 255 -128 0
wrap 1 -1
```

- Plain `+`, `-` and `*` panic on overflow (code 1). These functions are for code that wants the wrapped value, or wants to handle the overflow itself.
- `DivWide` panics unless `hi < d`; here `hi` is 2 and `d` is 3.

### Bit operations

```mk
// verdict: ok
import std.math

export Main(sys: &System): WriteError!void {
    x: u32 = 0x00F0_0000
    try sys.Print("leading {l} trailing {t} ones {o}\n", (l: math.LeadingZeros32(x), t: math.TrailingZeros32(x), o: math.OnesCount32(x)))
    try sys.Print("rotl {r:x}\n", (r: math.RotateLeft32(0x80000001, 4)))
    try sys.Print("bswap {b:x}\n", (b: math.ReverseBytes32(0x11223344)))
    try sys.Print("rbit {b:08b}\n", (b: math.ReverseBits8(0b0000_0110)))

    // a signed value goes to the unsigned type of its width: Cast when it
    // is not negative, Trunc to keep the bits of a negative one
    n: i64 = -8
    try sys.Print("ones in -8 {o}\n", (o: math.OnesCount64(Trunc[u64](n))))
    try sys.Print("gcd {g}\n", (g: math.Gcd(84, 36)))
}
```

```text
$ mk run bits.mk
leading 8 trailing 20 ones 4
rotl 18
bswap 44332211
rbit 01100000
ones in -8 61
gcd 12
```

- The counts give the width for 0: `LeadingZeros32(0)` is 32.
- A rotation takes `n` modulo the width, and a negative `n` rotates the other way.
- In a release build on hosted x86_64, `OnesCount` and `TrailingZeros` are one instruction (`popcnt`, `tzcnt`); `LeadingZeros` takes a few, since `lzcnt` is not in the baseline.

### Random numbers with a seed

```mk
// verdict: ok
import std.math

// Shuffle puts xs in a random order (Fisher-Yates).
Shuffle(r: mut math.Rng, xs: mut []int) {
    var i := xs.Len() - 1
    for i > 0 {
        j := Cast[int](r.Below(Cast[u64](i + 1))) // uniform on 0 to i, i included
        t := xs[i]
        xs[i] = xs[j]
        xs[j] = t
        i -= 1
    }
}

export Main(sys: &System): WriteError|Canceled!void {
    var r := math.Rng.New(42) // one seed, one sequence, on every target
    var deck := [1, 2, 3, 4, 5, 6, 7, 8]
    Shuffle(mut r, mut deck[..])
    try sys.Print("deck {d}\n", (d: deck[..]))
    try sys.Print("die {a} {b} {c}\n", (a: r.Below(6) + 1, b: r.Below(6) + 1, c: r.Below(6) + 1))
    f := r.Float() // in [0, 1)
    try sys.Print("float {f:.6}\n", (f))

    var fresh := try math.Rng.FromIo(sys.io) // seeded by the OS: differs on every run
    _ = fresh.U64()
}
```

```text
$ mk run shuffle.mk
deck [8, 2, 7, 4, 6, 5, 3, 1]
die 6 5 4
float 0.682453
```

- Every run prints the same lines, on every target: the seed fixes the sequence. Log the seed of a failing test to replay it.
- `Rng` is a plain value. Pass it as `mut` to a function that draws from it, so the caller's generator moves on.

### Decimal digits of a float

```mk
// verdict: ok
import std.math

// Show prints d as digits, then the exponent: 0.1 is "1 e-1".
Show(sys: &System, label: str, d: &math.Digits): WriteError!void {
    sign := if d.neg { "-" } else { "" }
    digits := str.From(d.digits[0..d.len]) else { "?" } // ASCII, so never "?"
    try sys.Print("{label}: {sign}{digits} e{exp}\n", (label, sign, digits, exp: d.exp))
}

export Main(sys: &System): WriteError!void {
    try Show(sys, "shortest 0.1", &math.ShortestDigits(0.1))
    try Show(sys, "shortest 1e21", &math.ShortestDigits(1e21))
    try Show(sys, "shortest -2.5", &math.ShortestDigits(-2.5))
    // 0.1's exact binary value, to 20 significant digits
    try Show(sys, "exact 0.1", &math.ExactDigits(0.1, 20, math.DigitMode.Significant))
    // no digits after the point: ties to even, or away from zero
    try Show(sys, "2.5 even", &math.ExactDigits(2.5, 0, math.DigitMode.Fraction))
    try Show(sys, "2.5 up", &math.ExactDigits(2.5, 0, math.DigitMode.FractionHalfUp))
    try Show(sys, "zero", &math.ShortestDigits(0.0))
}
```

```text
$ mk run digits.mk
shortest 0.1: 1 e-1
shortest 1e21: 1 e21
shortest -2.5: -25 e0
exact 0.1: 10000000000000000555 e-1
2.5 even: 2 e0
2.5 up: 3 e0
zero:  e0
```

- The value is `d.ddd... * 10^exp`: `-25 e0` is -2.5.
- `ExactDigits` shows that the `f64` nearest 0.1 is a little above it.
- A zero has no digits (`len` 0); the caller writes it as it likes.

## API

<!-- api:begin -->
Stability: Stable.

### `Abs`

```
export Abs(x: f64): f64
```

|x|: the sign bit cleared, so Abs(-0.0) is 0 and a NaN stays a NaN.
O(1); allocates nothing.

### `Acos`

```
export Acos(x: f64): f64
```

Acos is the arccosine of x, in [0, pi]: Nan outside [-1, 1] or for a NaN.
Faithfully rounded (within 1 ulp) but not correctly rounded, so its results
may change in a minor release. TestSTDMATH_TrueError: largest error 0.829
ulp over 20,151 inputs, 794 of them not correctly rounded. O(1); allocates
nothing.

### `AcosF32`

```
export AcosF32(x: f32): f32
```

AcosF32 is Acos for f32, correctly rounded for every one of its 2^32 inputs.
O(1); allocates nothing.

### `Acosh`

```
export Acosh(x: f64): f64
```

Acosh is the inverse hyperbolic cosine of x, for x >= 1: 0 at 1, inf for
inf, Nan below 1 or for a NaN. Correctly rounded on every input tested, but
not proved so for every input, so its results may change in a minor release.
TestSTDMATH_TrueError: largest error 0.500 ulp over 20,161 inputs, every
one correctly rounded. Over the 3,119 adversarial and random inputs of
TestSTDMATH_Edges, largest error 0.500 ulp. O(1); allocates nothing.

### `AcoshF32`

```
export AcoshF32(x: f32): f32
```

AcoshF32 is Acosh for f32, correctly rounded for every one of its
2^32 inputs. O(1); allocates nothing.

### `AddCarry`

```
export AddCarry(x: u64, y: u64, c: u64): (sum: u64, carry: u64)
```

x + y + c with a carry out: sum is the low 64 bits, and carry is 1 when the
total is 2^64 or more, else 0. Pass the carry of the limb below as c (0 or
1), so a chain of calls adds numbers of many limbs. O(1); allocates nothing.

### `AddOverflow`

```
export AddOverflow[T: Integer](a: T, b: T): (value: T, overflow: bool)
```

- Flows: The result borrows from a and b.

a + b: value is the sum wrapped to T, and overflow is whether the exact
sum does not fit T. O(1); allocates nothing.

### `Asin`

```
export Asin(x: f64): f64
```

Asin is the arcsine of x, in [-pi/2, pi/2]: Nan outside [-1, 1] or for a
NaN. Faithfully rounded (within 1 ulp) but not correctly rounded, so its
results may change in a minor release. TestSTDMATH_TrueError: largest error
0.762 ulp over 20,151 inputs, 624 of them not correctly rounded. O(1);
allocates nothing.

### `AsinF32`

```
export AsinF32(x: f32): f32
```

AsinF32 is Asin for f32, correctly rounded for every one of its 2^32 inputs.
O(1); allocates nothing.

### `Asinh`

```
export Asinh(x: f64): f64
```

Asinh is the inverse hyperbolic sine of x: an infinity keeps its sign, Nan
for a NaN. Correctly rounded on every input tested, but not proved so for
every input, so its results may change in a minor release.
TestSTDMATH_TrueError: largest error 0.500 ulp over 20,173 inputs, every
one correctly rounded. Over the 3,040 adversarial and random inputs of
TestSTDMATH_Edges, largest error 0.500 ulp. O(1); allocates nothing.

### `AsinhF32`

```
export AsinhF32(x: f32): f32
```

AsinhF32 is Asinh for f32, correctly rounded for every one of its
2^32 inputs. O(1); allocates nothing.

### `Atan`

```
export Atan(x: f64): f64
```

Atan is the arctangent of x, in [-pi/2, pi/2]: +-pi/2 for an infinity, Nan
for a NaN. Faithfully rounded (within 1 ulp) but not correctly rounded, so
its results may change in a minor release. TestSTDMATH_TrueError: largest
error 0.695 ulp over 20,173 inputs, 231 of them not correctly rounded. O(1);
allocates nothing.

### `Atan2`

```
export Atan2(y: f64, x: f64): f64
```

Atan2 is the angle of the point (x, y) from the positive x axis, in [-pi,
pi], with C99's special cases: a zero y keeps its sign, a zero y with a
negative x gives +-pi, and a NaN in either gives Nan. Faithfully rounded
(within 1 ulp) but not correctly rounded, so its results may change in a
minor release. TestSTDMATH_TrueError: largest error 0.557 ulp over 21,681
inputs, 7 of them not correctly rounded. Over the 3,146 adversarial and
random inputs of TestSTDMATH_Edges, largest error 0.500 ulp. O(1);
allocates nothing.

### `Atan2F32`

```
export Atan2F32(y: f32, x: f32): f32
```

Atan2F32 is Atan2 for f32. Largest error 0.500 ulp over the 21,369 inputs of
TestSTDMATH_Oracle and the 80 of TestSTDMATH_Midpoint, every one correctly
rounded; not proved correctly rounded for every input, so its results may
change in a minor release. None of the 238 near-midpoint inputs of
TestSTDMATH_Atan2Search is misrounded. O(1); allocates nothing.

### `AtanF32`

```
export AtanF32(x: f32): f32
```

AtanF32 is Atan for f32, correctly rounded for every one of its 2^32 inputs.
O(1); allocates nothing.

### `Atanh`

```
export Atanh(x: f64): f64
```

Atanh is the inverse hyperbolic tangent of x: +-inf at +-1, Nan beyond them
or for a NaN. Correctly rounded on every input tested, but not proved so for
every input, so its results may change in a minor release.
TestSTDMATH_TrueError: largest error 0.500 ulp over 20,153 inputs, every
one correctly rounded. Over the 3,130 adversarial and random inputs of
TestSTDMATH_Edges, largest error 0.500 ulp. O(1); allocates nothing.

### `AtanhF32`

```
export AtanhF32(x: f32): f32
```

AtanhF32 is Atanh for f32, correctly rounded for every one of its
2^32 inputs. O(1); allocates nothing.

### `CastSat`

```
export CastSat[T: Integer](x: f64): T
```

x truncated toward zero and clamped to T's range: below it gives T's least
value, above it T's greatest, and NaN gives 0. O(1); allocates nothing.

### `CastSatF32`

```
export CastSatF32[T: Integer](x: f32): T
```

CastSat of an f32. O(1); allocates nothing.

### `Cbrt`

```
export Cbrt(x: f64): f64
```

Cbrt is the real cube root of x, with x's sign: a zero or an infinity is
itself, and a NaN gives Nan. Faithfully rounded (within 1 ulp) but not
correctly rounded, so its results may change in a minor release.
TestSTDMATH_TrueError: largest error 0.664 ulp over 20,187 inputs, 1,641 of
them not correctly rounded. O(1); allocates nothing.

### `CbrtF32`

```
export CbrtF32(x: f32): f32
```

CbrtF32 is Cbrt for f32, correctly rounded for every one of its 2^32 inputs.
O(1); allocates nothing.

### `Ceil`

```
export Ceil(x: f64): f64
```

Ceil is the least integer not below x, keeping a zero's sign (-0.5
gives -0); an infinity is itself and a NaN is Nan. Exact. O(1);
allocates nothing.

### `CeilF32`

```
export CeilF32(x: f32): f32
```

CeilF32 is the least integer not below x, keeping a zero's sign. Exact.
O(1); allocates nothing.

### `CheckedPow`

```
export CheckedPow(x: i64, n: u32): ?i64
```

CheckedPow is x to the power n, or none when it leaves the i64 range;
x^0 is 1, 0^0 included. O(log n) multiplications; allocates nothing.

### `CheckedPowU64`

```
export CheckedPowU64(x: u64, n: u32): ?u64
```

CheckedPowU64 is x to the power n, or none when it exceeds the u64
range; x^0 is 1, 0^0 included. O(log n) multiplications; allocates
nothing.

### `CopySign`

```
export CopySign(x: f64, sign: f64): f64
```

CopySign is x's magnitude with sign's sign bit, x's other bits kept, a
NaN's payload included. Exact. O(1); allocates nothing.

### `CopySignF32`

```
export CopySignF32(x: f32, sign: f32): f32
```

CopySignF32 is x's magnitude with sign's sign bit, x's other bits kept.
Exact. O(1); allocates nothing.

### `Cos`

```
export Cos(x: f64): f64
```

Cos is the cosine of x in radians: Nan for an infinity or a NaN. Faithfully
rounded (within 1 ulp) but not correctly rounded, so its results may change
in a minor release. TestSTDMATH_TrueError: largest error 0.725 ulp over
20,271 inputs, 485 of them not correctly rounded. Over the 2,010 inputs
nearest a multiple of pi/2 of TestSTDMATH_Reduction, largest error 0.500
ulp. O(1); allocates nothing.

### `CosF32`

```
export CosF32(x: f32): f32
```

CosF32 is Cos for f32, correctly rounded for every one of its 2^32 inputs.
O(1); allocates nothing.

### `Cosh`

```
export Cosh(x: f64): f64
```

Cosh is the hyperbolic cosine of x: inf for an infinity, Nan for a NaN.
Correctly rounded on every input tested, but not proved so for every input,
so its results may change in a minor release. TestSTDMATH_TrueError:
largest error 0.500 ulp over 20,185 inputs, every one correctly rounded.
Over the 3,054 adversarial and random inputs of TestSTDMATH_Edges, largest
error 0.500 ulp. O(1); allocates nothing.

### `CoshF32`

```
export CoshF32(x: f32): f32
```

CoshF32 is Cosh for f32, correctly rounded for every one of its 2^32 inputs.
O(1); allocates nothing.

### `DigitMode`

```
export enum DigitMode
```

How ExactDigits counts its n digits and rounds the exact binary value.
`Significant` keeps n significant digits and `Fraction` n digits after the
point, both with ties to even. `SignificantHalfUp` and `FractionHalfUp`
break ties away from zero, as ECMA-262's toPrecision, toExponential and
toFixed do.

Variants:

- `Significant`: n significant digits, ties to even.
- `Fraction`: n digits after the point, ties to even.
- `SignificantHalfUp`: n significant digits, ties away from zero.
- `FractionHalfUp`: n digits after the point, ties away from zero.

### `Digits`

```
export struct Digits
```

A float's decimal digits. The magnitude is d.ddd... * 10^exp, with the
digits in `digits[0..len]` as ASCII, the first and the last not '0';
trailing zeros of the value are in exp. `neg` is the sign bit, so -0.0 is
neg. The 768 bytes hold an f64's longest exact expansion, 767 digits, so no
result is cut. A zero, a value rounded away to zero, NaN and the infinities
have len 0 and exp 0; tell them apart by the value itself.

Fields:

- `digits: [768]u8`: The digits as ASCII, in digits[0..len].
- `len: int`: The number of digits; 0 for a zero, NaN and the infinities.
- `exp: int`: The decimal exponent: the magnitude is d.ddd... * 10^exp.
- `neg: bool`: The sign bit, so -0.0 is neg.

### `DivWide`

```
export DivWide(hi: u64, lo: u64, d: u64): (q: u64, r: u64)
```

The 128-bit value hi * 2^64 + lo divided by d: quotient q and remainder r.
Panics with a division by zero when d is 0, and otherwise with an overflow
when hi >= d, so the quotient always fits a u64. O(1); allocates nothing.

### `E`

```
export const E: f64 = f64.FromBits(0x4005BF0A8B145769)
```

E, the f64 nearest to Euler's number e.

### `ExactDigits`

```
export ExactDigits(x: f64, n: int, mode: DigitMode): Digits
```

x's exact value rounded by mode to n digits: n significant digits (n at
least 1) or n digits after the point (n at least 0). Any other n panics with
code 8. An n past the exact expansion rounds nothing. O(1) when the result
has at most 18 digits, else O(767); allocates nothing.

### `Exp`

```
export Exp(x: f64): f64
```

Exp is e^x: inf past 709.78, 0 below -745.13, Nan for a NaN. Faithfully
rounded (within 1 ulp) but not correctly rounded, so its results may change
in a minor release. TestSTDMATH_TrueError: largest error 0.502 ulp over
20,243 inputs, 5 of them not correctly rounded. Over the 3,254 adversarial
and random inputs of TestSTDMATH_Edges, largest error 0.506 ulp. O(1);
allocates nothing.

### `Exp2`

```
export Exp2(x: f64): f64
```

Exp2 is 2^x: exact at an integer x, inf from 1024 up, 0 at -1075 and below,
Nan for a NaN. Faithfully rounded (within 1 ulp) but not correctly rounded,
so its results may change in a minor release. TestSTDMATH_TrueError:
largest error 0.803 ulp over 20,283 inputs, 933 of them not correctly
rounded. O(1); allocates nothing.

### `Exp2F32`

```
export Exp2F32(x: f32): f32
```

Exp2F32 is Exp2 for f32, correctly rounded for every one of its 2^32 inputs.
O(1); allocates nothing.

### `ExpF32`

```
export ExpF32(x: f32): f32
```

ExpF32 is Exp for f32, correctly rounded for every one of its 2^32 inputs.
O(1); allocates nothing.

### `Expm1`

```
export Expm1(x: f64): f64
```

Expm1 is e^x - 1, accurate near 0, where Exp(x) - 1 cancels: -1 below -56
ln2, inf past 709.78, Nan for a NaN. Faithfully rounded (within 1 ulp) but
not correctly rounded, so its results may change in a minor release.
TestSTDMATH_TrueError: largest error 0.743 ulp over 20,221 inputs, 922 of
them not correctly rounded. O(1); allocates nothing.

### `Expm1F32`

```
export Expm1F32(x: f32): f32
```

Expm1F32 is Expm1 for f32, correctly rounded for every one of its
2^32 inputs. O(1); allocates nothing.

### `Floor`

```
export Floor(x: f64): f64
```

Floor is the greatest integer not above x, keeping a zero's sign; an
infinity is itself and a NaN is Nan. Exact. O(1); allocates nothing.

### `FloorF32`

```
export FloorF32(x: f32): f32
```

FloorF32 is the greatest integer not above x, keeping a zero's sign.
Exact. O(1); allocates nothing.

### `Fma`

```
export Fma(x: f64, y: f64, z: f64): f64
```

Fma is x * y + z rounded once, to nearest with ties to even: correctly
rounded, with Nan for a NaN result. On a CPU with the fused multiply-add
instruction (simd.Cpu()) it is the instruction; elsewhere, and in the
evaluator, a software routine. IEEE 754 defines the instruction's result
bit for bit, so the result is the same either way, on every target and
at compile time. O(1); allocates nothing.

### `FmaF32`

```
export FmaF32(x: f32, y: f32, z: f32): f32
```

FmaF32 is x * y + z rounded once to f32, to nearest with ties to even:
correctly rounded, with Nan's f32 bits for a NaN result. O(1); allocates
nothing.

### `FrExp`

```
export FrExp(x: f64): (frac: f64, exp: int)
```

FrExp splits x into frac in [0.5, 1) and exp with x = frac * 2^exp,
frac keeping x's sign; a zero or an infinity is (x, 0) and a NaN is
(Nan, 0). Exact. O(1); allocates nothing.

### `FrExpF32`

```
export FrExpF32(x: f32): (frac: f32, exp: int)
```

FrExpF32 splits x into frac in [0.5, 1) and exp with x = frac * 2^exp;
a zero or an infinity is (x, 0) and a NaN is (Nan, 0). Exact. O(1);
allocates nothing.

### `Fract`

```
export Fract(x: f64): f64
```

Fract is x - RoundToZero(x), the fractional part, with x's sign (Fract(-3.0)
is -0); Nan for an infinity or a NaN. Exact. O(1); allocates nothing.

### `FractF32`

```
export FractF32(x: f32): f32
```

FractF32 is x - RoundToZeroF32(x) with x's sign; Nan's f32 bits for an
infinity or a NaN. Exact. O(1); allocates nothing.

### `Gcd`

```
export Gcd(a: u64, b: u64): u64
```

Gcd is the greatest common divisor of a and b; Gcd(a, 0) is a, so
Gcd(0, 0) is 0. Stein's binary algorithm: O(log a + log b) steps of a
subtraction and a shift, no division; allocates nothing.

### `Hypot`

```
export Hypot(x: f64, y: f64): f64
```

Hypot is sqrt(x^2 + y^2), without overflow or underflow on the way: inf when
either is infinite (a NaN in the other included), else Nan when either is a
NaN. Faithfully rounded (within 1 ulp) but not correctly rounded, so its
results may change in a minor release. TestSTDMATH_TrueError: largest error
0.856 ulp over 21,681 inputs, 890 of them not correctly rounded. O(1);
allocates nothing.

### `HypotF32`

```
export HypotF32(x: f32, y: f32): f32
```

HypotF32 is Hypot for f32: inf when either is infinite (a NaN in the other
included), else Nan's f32 bits when either is a NaN. Correctly rounded for
every input. O(1); allocates nothing.

### `Inf`

```
export const Inf: f64 = f64.FromBits(0x7FF0000000000000)
```

Inf, positive infinity.

### `Integer`

```
export interface Integer {
}
```

The integer types: i8 to i64, u8 to u64, int and uint. The compiler supplies
the set; an impl of Integer is E0704.

### `IsFinite`

```
export IsFinite(x: f64): bool
```

IsFinite reports whether x is neither an infinity nor a NaN. O(1);
allocates nothing.

### `IsFiniteF32`

```
export IsFiniteF32(x: f32): bool
```

IsFiniteF32 reports whether x is neither an infinity nor a NaN. O(1);
allocates nothing.

### `IsInf`

```
export IsInf(x: f64): bool
```

IsInf reports whether x is +inf or -inf. O(1); allocates nothing.

### `IsInfF32`

```
export IsInfF32(x: f32): bool
```

IsInfF32 reports whether x is +inf or -inf. O(1); allocates nothing.

### `IsNan`

```
export IsNan(x: f64): bool
```

IsNan reports whether x is a NaN. O(1); allocates nothing.

### `IsNanF32`

```
export IsNanF32(x: f32): bool
```

IsNanF32 reports whether x is a NaN. O(1); allocates nothing.

### `LdExp`

```
export LdExp(frac: f64, exp: int): f64
```

LdExp is frac * 2^exp rounded once, to nearest with ties to even, so it is
exact unless the result is subnormal or out of range: an infinity past the
largest f64, a zero of frac's sign below the least. A zero or an infinity is
itself, and a NaN gives Nan. O(1); allocates nothing.

### `LdExpF32`

```
export LdExpF32(frac: f32, exp: int): f32
```

LdExpF32 is frac * 2^exp rounded once, to nearest with ties to even:
exact unless the result is subnormal or out of range. O(1); allocates
nothing.

### `LeadingZeros16`

```
export LeadingZeros16(x: u16): int
```

LeadingZeros16 counts the zero bits above x's highest one; 16 for 0.
O(1); allocates nothing.

### `LeadingZeros32`

```
export LeadingZeros32(x: u32): int
```

LeadingZeros32 counts the zero bits above x's highest one; 32 for 0.
O(1); allocates nothing.

### `LeadingZeros64`

```
export LeadingZeros64(x: u64): int
```

LeadingZeros64 counts the zero bits above x's highest one; 64 for 0.
O(1); allocates nothing.

### `LeadingZeros8`

```
export LeadingZeros8(x: u8): int
```

LeadingZeros8 counts the zero bits above x's highest one; 8 for 0.
O(1); allocates nothing.

### `Ln2`

```
export const Ln2: f64 = f64.FromBits(0x3FE62E42FEFA39EF)
```

Ln2, the f64 nearest to the natural logarithm of 2.

### `Log`

```
export Log(x: f64): f64
```

Log is the natural logarithm: -inf at a zero, Nan below zero or for a NaN,
inf for inf. Correctly rounded on every input tested, but not proved so for
every input, so its results may change in a minor release.
TestSTDMATH_TrueError: largest error 0.500 ulp over 20,181 inputs, every
one correctly rounded. Over the 3,412 adversarial and random inputs of
TestSTDMATH_Edges, largest error 0.500 ulp. O(1); allocates nothing.

### `Log10`

```
export Log10(x: f64): f64
```

Log10 is the base-10 logarithm: -inf at a zero, Nan below zero or for a NaN.
Faithfully rounded (within 1 ulp) but not correctly rounded, so its results
may change in a minor release. TestSTDMATH_TrueError: largest error 0.576
ulp over 20,203 inputs, 14 of them not correctly rounded. O(1); allocates
nothing.

### `Log10F32`

```
export Log10F32(x: f32): f32
```

Log10F32 is Log10 for f32, correctly rounded for every one of its
2^32 inputs. O(1); allocates nothing.

### `Log1p`

```
export Log1p(x: f64): f64
```

Log1p is log(1 + x), accurate near 0, where Log(1 + x) loses x's low bits:
-inf at -1, Nan below -1 or for a NaN. Faithfully rounded (within 1 ulp) but
not correctly rounded, so its results may change in a minor release.
TestSTDMATH_TrueError: largest error 0.727 ulp over 20,181 inputs, 630 of
them not correctly rounded. O(1); allocates nothing.

### `Log1pF32`

```
export Log1pF32(x: f32): f32
```

Log1pF32 is Log1p for f32, correctly rounded for every one of its
2^32 inputs. O(1); allocates nothing.

### `Log2`

```
export Log2(x: f64): f64
```

Log2 is the base-2 logarithm: exact at a power of two, -inf at a zero, Nan
below zero or for a NaN. Faithfully rounded (within 1 ulp) but not correctly
rounded, so its results may change in a minor release.
TestSTDMATH_TrueError: largest error 0.518 ulp over 20,181 inputs, 7 of
them not correctly rounded. O(1); allocates nothing.

### `Log2F32`

```
export Log2F32(x: f32): f32
```

Log2F32 is Log2 for f32, correctly rounded for every one of its 2^32 inputs.
O(1); allocates nothing.

### `LogF32`

```
export LogF32(x: f32): f32
```

LogF32 is Log for f32, correctly rounded for every one of its 2^32 inputs.
O(1); allocates nothing.

### `Max`

```
export Max[T: Ord + Plain](a: T, b: T): T
```

- Flows: The result borrows from a and b.

The greater of a and b; a when neither is greater (a.Less(&b) is
false). O(1); allocates nothing.

### `Min`

```
export Min[T: Ord + Plain](a: T, b: T): T
```

- Flows: The result borrows from a and b.

The lesser of a and b; a when neither is less (b.Less(&a) is false).
O(1); allocates nothing.

### `Mod`

```
export Mod(x: f64, y: f64): f64
```

Mod is the exact remainder of x / y truncated toward zero: x - n*y for the
integer n of that quotient, with x's sign. Nan when x is infinite, y is
zero, or either is a NaN; x when y is infinite and x finite. O(ex - ey) in
the exponent difference, one integer division per 11 bits; allocates
nothing.

### `ModF32`

```
export ModF32(x: f32, y: f32): f32
```

ModF32 is the exact remainder of x / y truncated toward zero, with x's sign.
Nan's f32 bits when x is infinite, y is zero, or either is a NaN; x when y
is infinite and x finite. O(ex - ey) in the exponent difference, one integer
division per 40 bits; allocates nothing.

### `MulOverflow`

```
export MulOverflow[T: Integer](a: T, b: T): (value: T, overflow: bool)
```

- Flows: The result borrows from a and b.

a * b: value is the product wrapped to T, and overflow is whether the
exact product does not fit T. O(1); allocates nothing.

### `MulWide`

```
export MulWide(a: u64, b: u64): (hi: u64, lo: u64)
```

The full 128-bit product of a and b, as its high and low halves. O(1);
allocates nothing.

### `Nan`

```
export const Nan: f64 = f64.FromBits(0x7FF8000000000000)
```

Nan, the quiet NaN 0x7FF8000000000000 that every std.math function
returns for a NaN result.

### `NextAfter`

```
export NextAfter(x: f64, toward: f64): f64
```

NextAfter is the f64 next to x in the direction of toward: x when they
are equal, Nan when either is a NaN, the least subnormal of toward's
sign from a zero. Exact. O(1); allocates nothing.

### `NextAfterF32`

```
export NextAfterF32(x: f32, toward: f32): f32
```

NextAfterF32 is the f32 next to x in the direction of toward: x when
they are equal, Nan's f32 bits when either is a NaN. Exact. O(1);
allocates nothing.

### `OnesCount16`

```
export OnesCount16(x: u16): int
```

OnesCount16 counts x's one bits. O(1); allocates nothing.

### `OnesCount32`

```
export OnesCount32(x: u32): int
```

OnesCount32 counts x's one bits. O(1); allocates nothing.

### `OnesCount64`

```
export OnesCount64(x: u64): int
```

OnesCount64 counts x's one bits. O(1); allocates nothing.

### `OnesCount8`

```
export OnesCount8(x: u8): int
```

OnesCount8 counts x's one bits. O(1); allocates nothing.

### `Pi`

```
export const Pi: f64 = f64.FromBits(0x400921FB54442D18)
```

Pi, the f64 nearest to π.

### `Pow`

```
export Pow(x: f64, y: f64): f64
```

Pow is x to the power y, with C99's special cases: Pow(x, 0) and Pow(1, y)
are 1 for every x and y, a NaN included; a negative x to a power that is not
an integer is Nan; a zero to a negative power is an infinity; an overflow is
an infinity and an underflow a zero, of the result's sign. Faithfully
rounded (within 1 ulp) but not correctly rounded, so its results may change
in a minor release. TestSTDMATH_TrueError: largest error 0.503 ulp over
21,681 inputs, 11 of them not correctly rounded. Over the 3,069 adversarial
and random inputs of TestSTDMATH_Edges, largest error 0.504 ulp. O(1);
allocates nothing.

### `PowF32`

```
export PowF32(x: f32, y: f32): f32
```

PowF32 is Pow for f32, with Pow's special cases. Largest error 0.500 ulp
over the 21,369 inputs of TestSTDMATH_Oracle and the 94 of
TestSTDMATH_Midpoint, every one correctly rounded; not proved correctly
rounded for every input, so its results may change in a minor release. None
of the 45,816 near-midpoint inputs of TestSTDMATH_PowSearch is misrounded,
an exact power included. O(1); allocates nothing.

### `ReverseBits16`

```
export ReverseBits16(x: u16): u16
```

ReverseBits16 reverses the order of x's bits. O(1); allocates nothing.

### `ReverseBits32`

```
export ReverseBits32(x: u32): u32
```

ReverseBits32 reverses the order of x's bits. O(1); allocates nothing.

### `ReverseBits64`

```
export ReverseBits64(x: u64): u64
```

ReverseBits64 reverses the order of x's bits. O(1); allocates nothing.

### `ReverseBits8`

```
export ReverseBits8(x: u8): u8
```

ReverseBits8 reverses the order of x's bits. O(1); allocates nothing.

### `ReverseBytes16`

```
export ReverseBytes16(x: u16): u16
```

ReverseBytes16 swaps x's two bytes. O(1); allocates nothing.

### `ReverseBytes32`

```
export ReverseBytes32(x: u32): u32
```

ReverseBytes32 reverses the order of x's bytes. O(1); allocates nothing.

### `ReverseBytes64`

```
export ReverseBytes64(x: u64): u64
```

ReverseBytes64 reverses the order of x's bytes. O(1); allocates nothing.

### `Rng`

```
export struct Rng
```

A pseudo-random generator, xoshiro256**: one seed gives the same sequence on
every target. It is not for secrets; use crypto.Random for those. Plain, 32
bytes: four state words, never all zero.

### `Rng.Below`

```
export Rng.Below(mut self, n: u64): u64
```

A uniform integer from 0 to n - 1, with no bias, by Lemire's method: one
multiplication per draw, and a division only in the rare case that the draw
lands near the biased band. Fewer than 2 draws are expected for every n, and
1 + n / 2^64 on average. n == 0 panics with code 8. It allocates nothing.

### `Rng.Fill`

```
export Rng.Fill(mut self, buf: mut []u8)
```

Fills buf with random bytes: U64 outputs in little-endian order, 8 bytes
each, and for a tail of 1 to 7 bytes the low bytes of one more output. The
bytes are the same on every target. O(n); allocates nothing.

### `Rng.Float`

```
export Rng.Float(mut self): f64
```

A uniform f64 in [0, 1): the top 53 bits of U64 times 2^-53, so every result
is a multiple of 2^-53 and 1 is never returned. O(1); allocates nothing.

### `Rng.FromIo`

```
export Rng.FromIo(io: Io): Canceled!Rng
```

- Errors: Canceled

A generator seeded with 32 random bytes from io, for a sequence that differs
on every run. If the bytes are all zero, the one state the generator cannot
leave, it starts from New(0)'s state instead. One Io call (Io.Random), so it
fails with Canceled when the task's scope is cancelled. It allocates
nothing.

### `Rng.New`

```
export Rng.New(seed: u64): Rng
```

A generator whose state is four successive SplitMix64 outputs from seed.
Every seed is valid, 0 included: the four outputs are never all zero. O(1);
allocates nothing.

### `Rng.U64`

```
export Rng.U64(mut self): u64
```

The next 64 random bits. The sequence repeats after 2^256 - 1 draws. O(1);
allocates nothing.

### `RotateLeft16`

```
export RotateLeft16(x: u16, n: int): u16
```

RotateLeft16 rotates x left by n mod 16 bits; a negative n rotates
right. O(1); allocates nothing.

### `RotateLeft32`

```
export RotateLeft32(x: u32, n: int): u32
```

RotateLeft32 rotates x left by n mod 32 bits; a negative n rotates
right. O(1); allocates nothing.

### `RotateLeft64`

```
export RotateLeft64(x: u64, n: int): u64
```

RotateLeft64 rotates x left by n mod 64 bits; a negative n rotates
right. O(1); allocates nothing.

### `RotateLeft8`

```
export RotateLeft8(x: u8, n: int): u8
```

RotateLeft8 rotates x left by n mod 8 bits; a negative n rotates right.
O(1); allocates nothing.

### `RotateRight16`

```
export RotateRight16(x: u16, n: int): u16
```

RotateRight16 rotates x right by n mod 16 bits; a negative n rotates
left. O(1); allocates nothing.

### `RotateRight32`

```
export RotateRight32(x: u32, n: int): u32
```

RotateRight32 rotates x right by n mod 32 bits; a negative n rotates
left. O(1); allocates nothing.

### `RotateRight64`

```
export RotateRight64(x: u64, n: int): u64
```

RotateRight64 rotates x right by n mod 64 bits; a negative n rotates
left. O(1); allocates nothing.

### `RotateRight8`

```
export RotateRight8(x: u8, n: int): u8
```

RotateRight8 rotates x right by n mod 8 bits; a negative n rotates
left. O(1); allocates nothing.

### `Round`

```
export Round(x: f64): f64
```

Round rounds x to the nearest integer, a half away from zero, keeping
its sign; an infinity is itself and a NaN is Nan. Exact. O(1);
allocates nothing.

### `RoundEven`

```
export RoundEven(x: f64): f64
```

RoundEven rounds x to the nearest integer, a half to the even one,
keeping its sign; an infinity is itself and a NaN is Nan. Exact. O(1);
allocates nothing.

### `RoundEvenF32`

```
export RoundEvenF32(x: f32): f32
```

RoundEvenF32 rounds x to the nearest integer, a half to the even one,
keeping its sign. Exact. O(1); allocates nothing.

### `RoundF32`

```
export RoundF32(x: f32): f32
```

RoundF32 rounds x to the nearest integer, a half away from zero,
keeping its sign. Exact. O(1); allocates nothing.

### `RoundToZero`

```
export RoundToZero(x: f64): f64
```

RoundToZero rounds x toward zero to an integer, keeping its sign (-0.5 gives
-0); an infinity is itself and a NaN is Nan. Exact. O(1); allocates
nothing.

### `RoundToZeroF32`

```
export RoundToZeroF32(x: f32): f32
```

RoundToZeroF32 rounds x toward zero to an integer, keeping its sign; an
infinity is itself and a NaN is Nan's f32 bits. Exact. O(1); allocates
nothing.

### `ShortestDigits`

```
export ShortestDigits(x: f64): Digits
```

The shortest digits that read back to x's exact bits (round to nearest
even); when two digit strings of that length do, the correctly rounded one.
These are the digits `{x}` prints. ShortestDigitsInto with a Digits around
it. O(1); allocates nothing.

### `ShortestDigitsInto`

```
export ShortestDigitsInto(out: mut []u8, x: f64): (len: int, exp: int)
```

ShortestDigits's digits written into out[0..len] as ASCII, the first and
the last not '0', with the decimal exponent exp of d.ddd... * 10^exp, as
Digits holds them. An f64 has at most 17 shortest digits, so out must hold
at least 17 bytes: a shorter out panics with code 8, whatever x is. Only
out[0..17] is written, and nothing is zeroed: past len it may hold
trimmed '0's. A zero, NaN and the infinities have len 0 and exp 0; the
sign is x's. O(1); allocates nothing.

### `Sin`

```
export Sin(x: f64): f64
```

Sin is the sine of x in radians: Nan for an infinity or a NaN. Faithfully
rounded (within 1 ulp) but not correctly rounded, so its results may change
in a minor release. TestSTDMATH_TrueError: largest error 0.715 ulp over
20,271 inputs, 501 of them not correctly rounded. Over the 2,010 inputs
nearest a multiple of pi/2 of TestSTDMATH_Reduction, largest error 0.500
ulp. O(1), a large x included; allocates nothing.

### `SinF32`

```
export SinF32(x: f32): f32
```

SinF32 is Sin for f32, correctly rounded for every one of its 2^32 inputs.
O(1); allocates nothing.

### `Sinh`

```
export Sinh(x: f64): f64
```

Sinh is the hyperbolic sine of x: an infinity keeps its sign, Nan for a NaN.
Correctly rounded on every input tested, but not proved so for every input,
so its results may change in a minor release. TestSTDMATH_TrueError:
largest error 0.500 ulp over 20,185 inputs, every one correctly rounded.
Over the 3,054 adversarial and random inputs of TestSTDMATH_Edges, largest
error 0.543 ulp. O(1); allocates nothing.

### `SinhF32`

```
export SinhF32(x: f32): f32
```

SinhF32 is Sinh for f32, correctly rounded for every one of its 2^32 inputs.
O(1); allocates nothing.

### `Sqrt`

```
export Sqrt(x: f64): f64
```

The correctly rounded square root (IEEE 754 squareRoot): Nan below
zero and for a NaN, -0 for -0, inf for inf. O(1);
allocates nothing.

### `Sqrt2`

```
export const Sqrt2: f64 = f64.FromBits(0x3FF6A09E667F3BCD)
```

Sqrt2, the f64 nearest to the square root of 2.

### `SqrtF32`

```
export SqrtF32(x: f32): f32
```

SqrtF32 is the correctly rounded square root: Nan's f32 bits below zero
or for a NaN, -0 for -0, inf for inf. O(1); allocates nothing.

### `SubBorrow`

```
export SubBorrow(x: u64, y: u64, b: u64): (diff: u64, borrow: u64)
```

x - y - b with a borrow out: diff is the result modulo 2^64, and borrow is 1
when x is less than y + b, else 0. Pass the borrow of the limb below as b (0
or 1). O(1); allocates nothing.

### `SubOverflow`

```
export SubOverflow[T: Integer](a: T, b: T): (value: T, overflow: bool)
```

- Flows: The result borrows from a and b.

a - b: value is the difference wrapped to T, and overflow is whether
the exact difference does not fit T. O(1); allocates nothing.

### `Tan`

```
export Tan(x: f64): f64
```

Tan is the tangent of x in radians: Nan for an infinity or a NaN. Faithfully
rounded (within 1 ulp) but not correctly rounded, so its results may change
in a minor release. TestSTDMATH_TrueError: largest error 0.778 ulp over
20,257 inputs, 580 of them not correctly rounded. Over the 2,010 inputs
nearest a multiple of pi/2 of TestSTDMATH_Reduction, largest error 0.500
ulp. O(1); allocates nothing.

### `TanF32`

```
export TanF32(x: f32): f32
```

TanF32 is Tan for f32, correctly rounded for every one of its 2^32 inputs.
O(1); allocates nothing.

### `Tanh`

```
export Tanh(x: f64): f64
```

Tanh is the hyperbolic tangent of x: +-1 for an infinity, Nan for a NaN.
Correctly rounded on every input tested, but not proved so for every input,
so its results may change in a minor release. TestSTDMATH_TrueError:
largest error 0.500 ulp over 20,193 inputs, every one correctly rounded.
Over the 3,036 adversarial and random inputs of TestSTDMATH_Edges, largest
error 0.500 ulp. O(1); allocates nothing.

### `TanhF32`

```
export TanhF32(x: f32): f32
```

TanhF32 is Tanh for f32, correctly rounded for every one of its 2^32 inputs.
O(1); allocates nothing.

### `TrailingZeros16`

```
export TrailingZeros16(x: u16): int
```

TrailingZeros16 counts the zero bits below x's lowest one; 16 for 0.
O(1); allocates nothing.

### `TrailingZeros32`

```
export TrailingZeros32(x: u32): int
```

TrailingZeros32 counts the zero bits below x's lowest one; 32 for 0.
O(1); allocates nothing.

### `TrailingZeros64`

```
export TrailingZeros64(x: u64): int
```

TrailingZeros64 counts the zero bits below x's lowest one; 64 for 0.
O(1); allocates nothing.

### `TrailingZeros8`

```
export TrailingZeros8(x: u8): int
```

TrailingZeros8 counts the zero bits below x's lowest one; 8 for 0.
O(1); allocates nothing.

### `WrapI32`

```
export WrapI32(x: f64): i32
```

x converted to i32 with wrapping: NaN and the infinities give 0, any other x
is truncated toward zero and reduced modulo 2^32 into i32's range. This is
ECMA-262's ToInt32. O(1); allocates nothing.

### `WrapU32`

```
export WrapU32(x: f64): u32
```

x converted to u32 with wrapping: NaN and the infinities give 0, any other x
is truncated toward zero and reduced modulo 2^32 into u32's range. This is
ECMA-262's ToUint32. O(1); allocates nothing.

<!-- api:end -->
