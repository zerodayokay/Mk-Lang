# std.simd

`std.simd` has fixed-width vectors (`Vec[T, N]`) and masks (`Mask[T, N]`), and CPU feature detection and dispatch at run time. It also has whole-slice routines for bytes and floats that pick the widest vectors the CPU has. Reach for the slice routines (`IndexByte`, `Count`, `Equal`, `ValidUtf8`, `Dot`, `Sum`) first: they are ready-made kernels. Write your own `Vec` loop when no routine does the job. The module is [provisional](../codebase.md#7-stability-levels).

The language side of vectors (the types, their operators and `cpu` clauses) is in the reference: [vectors](../reference.md#vectors), [vector operators](../reference.md#vector-operators) and [CPU features](../reference.md#cpu-features).

```mk
// verdict: parse
import std.simd
```

## Contents

- [The main pieces](#the-main-pieces)
- [Vectors and masks](#vectors-and-masks)
- [Lane classes](#lane-classes)
- [Loads, stores and lane moves](#loads-stores-and-lane-moves)
- [Arithmetic, conversions and reductions](#arithmetic-conversions-and-reductions)
- [Generic code](#generic-code)
- [CPU features and dispatch](#cpu-features-and-dispatch)
- [Slice routines](#slice-routines)
- [Examples](#examples): [a vector loop](#a-vector-loop), [search with a mask](#search-with-a-mask), [byte routines](#byte-routines), [dot products and sums](#dot-products-and-sums), [widening at compile time](#widening-at-compile-time)
- [API](#api)

## The main pieces

| Task | Use |
|---|---|
| Find, count or compare bytes | `IndexByte`, `LastIndexByte`, `IndexAny`, `Count`, `Equal`, `Compare` |
| Check text | `ValidUtf8`, `ValidWtf8`, `IsAscii`, `Utf16Len`, `Utf16LenBytes` |
| Add floats in a fixed order | `Dot`, `Sum` |
| Make a vector | `Vec.Splat`, `Zero`, `Iota`, `FromArray`, `FromSlice`, `FromSliceOr`, `Gather` |
| Store a vector | `Vec.CopyTo`, `CopyToMasked`, `ToArray` |
| Read or change one lane | `Vec.Lane`, `With`, `Broadcast` |
| Move lanes | `Shuffle`, `Reverse`, `Rotate`, `Slide`, `InterleaveLo`, `InterleaveHi`, `DeinterleaveEven`, `DeinterleaveOdd`, `Lookup16` |
| Reduce to one value | `ReduceSum`, `ReduceSumWrap`, `ReduceSumWide`, `ReduceMin`, `ReduceMax`, `ReduceAnd`, `ReduceOr`, `ReduceXor` |
| Lane-wise math beyond the operators | `Min`, `Max`, `Abs`, `AbsWrap`, `AddSat`, `SubSat`, `MulHigh`, `MulWidenLo`, `MulWidenHi`, `DivTrunc`, `ShlEach`, `ShrEach`, `Fma`, `Sqrt`, `Floor`, `Ceil`, `RoundEven`, `RoundToZero` |
| Change the lane type | `Cast`, `CastSat`, `Trunc`, `WidenLo`, `WidenHi`, `NarrowSat`, `Reinterpret` |
| Work with a comparison's result | `Mask.Any`, `All`, `CountSet`, `FirstSet`, `Bits`, `FromBits`, `Select` |
| Ask what the CPU has | `Cpu`, `CpuFeatures.Of`, `Has`, `HasAll` |
| Pick a function by CPU | `Choice`, `Dispatch` |
| The natural lane count of a type | `Lanes` |

## Vectors and masks

A `Vec[T, N]` is `N` lanes of `T`, laid out as `[N]T`. Comparing two vectors gives a `Mask[T, N]`, one truth value per lane. Both are plain values: pass them, return them and copy them freely. Every operation requires `N` to be 2, 4, 8, 16, 32 or 64 (`Lookup16`: 16, 32 or 64).

Every `Vec` and `Mask` function is an intrinsic, compiled inline, so none is a call. Unless its doc says otherwise, each costs a few instructions, cannot fail and allocates nothing. `Lanes[T]()` is the natural lane count of `T` on the target: 16 for `u8` and 4 for `f32` on every supported target. For a concrete `T` it is a constant you can write as a lane count, `simd.Vec[f32, simd.Lanes[f32]()]`. Of a type parameter it is a run-time value, and using it as a lane count is E0703.

```mk
// verdict: ok
// add two slices four lanes at a time, then the tail
import std.simd

AddInto(dst: mut []f32, a: []f32, b: []f32) {
    var i := 0
    for i + 4 <= dst.Len() {
        v := simd.Vec[f32, 4].FromSlice(a, i) + simd.Vec[f32, 4].FromSlice(b, i)
        v.CopyTo(dst, i)
        i += 4
    }
    for i < dst.Len() {
        dst[i] = a[i] + b[i]
        i += 1
    }
}
```

## Lane classes

Some functions take only one class of lanes. A call with another class is E0701:

| Lanes | Functions |
|---|---|
| Integer | `ReduceSumWrap`, `ReduceAnd`, `ReduceOr`, `ReduceXor`, `AddSat`, `SubSat`, `DivTrunc`, `ShlEach`, `ShrEach`, `MulWidenLo`, `MulWidenHi`, `NarrowSat`, `Trunc`, and `Gather`'s index lanes `I` |
| Integer, at most 32 bits | `ReduceSumWide`, `MulHigh` |
| Signed | `AbsWrap` |
| Float | `ReduceSum`, `Abs`, `Fma`, `Sqrt`, `Floor`, `Ceil`, `RoundEven`, `RoundToZero`, `Dot`, `Sum` |

```mk
// verdict: reject E0701
// index lanes are integers, ReduceSumWide and MulHigh take integer lanes, and WidenLo keeps the class
import std.simd

Pick(xs: []i32, idx: simd.Vec[f32, 4]): simd.Vec[i32, 4] {
    return simd.Vec[i32, 4].Gather(xs, idx) // <- E0701
}

Total(v: simd.Vec[f32, 4]): i64 {
    return v.ReduceSumWide() // <- E0701
}

High(v: simd.Vec[f32, 4]): simd.Vec[f32, 4] {
    return v.MulHigh(v) // <- E0701
}

Wide(v: simd.Vec[i16, 8]): simd.Vec[f32, 4] {
    return v.WidenLo[f32, 4]() // <- E0701
}
```

## Loads, stores and lane moves

- `FromSlice` and `CopyTo` check `0 <= i <= xs.Len() - N` once for the whole span, and panic with the bounds panic otherwise.
- `FromSliceOr` takes the lanes from `i` up to `xs.Len()`, and the rest from `fill`: a load of a slice's tail.
- `CopyToMasked` stores only the set lanes, and checks only those. `Gather` checks every index.
- `Lane`, `With` and `Broadcast` take a constant lane index, from 0 to `N - 1`.
- `Shuffle`'s indices pick from `self` (0 to `N - 1`), then from `b` (`N` to `2N - 1`); an index outside that is E0706.
- `Slide(prev, k)` is the last `k` lanes of `prev` followed by the first `N - k` of `self`. `Rotate(k)` turns the lanes down, lane `j` taking lane `(j + k) mod N`.
- `Lookup16` maps each lane below 16 through `table`, and every other lane to 0.
- `Mask.Bits` has bit `j` set when lane `j` is set. `FromBits` ignores the bits from `N` up.

## Arithmetic, conversions and reductions

The operators work lane by lane, as [vector operators](../reference.md#vector-operators) describes: `+ - * /` on floats, `+% -% *%`, `& | ^ ~` and `<< >>` on integers, and the comparisons. The named functions add:

- **Widening.** `WidenLo` and `WidenHi` turn the low or high half of the lanes into lanes twice as wide. `MulWidenLo` and `MulWidenHi` give the exact products of those halves. The new lane type `U` has `T`'s class and signedness (E0701 otherwise): a signed lane is sign-extended, an unsigned one zero-extended, and an `f32` becomes the same `f64`.
- **Narrowing.** `NarrowSat` clamps each lane of `self`, then of `b`, to `U`'s range.
- **Conversion.** `Cast` converts lane by lane as the scalar [`Cast`](../reference.md#3-types-and-kinds) does. It panics when a lane does not fit, and passes NaN and the infinities through to a float lane. `Trunc` keeps the low bits. `CastSat` saturates, NaN giving 0. `Reinterpret` keeps the bytes.
- **Integer sums.** An integer sum has no trapping form: `ReduceSumWrap` wraps, and `ReduceSumWide` is exact in `i64`.
- **Float sums.** `ReduceSum` adds lanes as a pairwise tree, each half to the other, down to one lane.
- **NaN and zero.** On floats, `Min`, `Max`, `ReduceMin` and `ReduceMax` give NaN when a lane is NaN, and order -0 below +0.
- **Panics.** `DivTrunc` panics on a zero divisor lane or a `MIN / -1` pair. `ShlEach` and `ShrEach` panic on an amount lane at or above the lane width. `AbsWrap` leaves `MIN` as it is.
- **CPU features.** `Fma` rounds once, and compiles only in a function whose features hold `fma`. On x86_64, `Floor`, `Ceil`, `RoundEven` and `RoundToZero` compile only where the features hold `sse4.1`, which the hosted baseline does. Elsewhere each is E1106, since Mk links no `fma` or `floor` routine to fall back on.

## Generic code

A function generic over its lane count restates the clause of each `Vec` function it calls, as any [generic](../reference.md#10-generics-and-interfaces) restates the clauses it relies on: `requires N == 2 || N == 4 || ...`. The cookbook recipe [Write a vector kernel once and pick it by CPU](../cookbook.md#write-a-vector-kernel-once-and-pick-it-by-cpu) shows one.

A generic whose lane type is its own bare type parameter also restates the clauses of the widening forms, `NarrowSat` and `Reinterpret`. For example, `W[T: Lane, U: Lane]` calling `v.WidenLo[U, 2]()` states `requires 2 * 2 == 4 && 2 >= 2 && SizeOf[U]() == 2 * SizeOf[T]()`.

The compiler checks these clauses where the generic is called. Two rules it cannot decide there, so they are checked when the call runs, in every profile, release included: such a clause over a type built on a type parameter, and the rule that the widening forms and `Trunc` keep the lane class and signedness when `T` or `U` is a type parameter. A false one is a contract panic at the call, and E0901 when the call runs at compile time. The recipe [Write a kernel that widens its lanes](../cookbook.md#write-a-kernel-that-widens-its-lanes) shows how to relate two lane counts.

## CPU features and dispatch

- `Cpu()` reads the running CPU once and caches the answer. It reports a feature whose registers the OS must save (the AVX and AVX-512 families) only when the OS has enabled that state, so a feature it reports can run.
- At compile time, `Cpu()` answers the target's baseline, and each operation runs lane by lane with the results described here.
- `CpuFeatures.Of` and `Has` take the feature names of a [`cpu` clause](../reference.md#cpu-features) (E1106 otherwise). `Has` of another architecture's name is false.
- `Dispatch` returns the `f` of the first choice whose `needs` the running CPU has, else `base`. Order the choices best first. A `Choice` that names a `cpu` function is made inside `unsafe`, and its reason is that `needs` covers the function's clause. A module with an `unsafe` block is listed in its package's `unsafe_modules`.

## Slice routines

The slice routines take the widest vectors the CPU has. The byte and text routines take avx2 from 64 bytes, and all but `Count` and `Compare` take avx512bw from 256 bytes. `Dot` and `Sum` take avx2 from 64 elements. Their results never depend on the CPU. None allocates. Each is O(n), and `IndexAny` is O(n + set.Len()).

- `Dot` and `Sum` keep 16 partial sums, the `j`th taking elements `j`, `j + 16` and so on. Each starts at +0.0, so a sum of -0.0 elements alone is +0.0. They add the partial sums as `ReduceSum` does, then add the tail in order, on every CPU, so a dispatched run and a baseline run give the same bits. `Dot` panics when the lengths differ.
- `Compare` gives -1, 0 or 1 by the first byte that differs, a proper prefix first. `IndexAny` finds the first byte of `s` that is in `set`.
- `IsAscii`, `Utf16Len`, `Utf16LenBytes`, `LastIndexByte`, `ValidUtf8` and `ValidWtf8` agree with their one-byte-at-a-time forms on any bytes.
- `Utf16LenBytes` is `Utf16Len` for bytes that may not be a `str`, such as WTF-8 holding a lone surrogate, which it counts as one unit. On any bytes it counts one unit per byte that is not a continuation byte (`10xxxxxx`) and one more per byte from `0xF0` on, which on UTF-8 and WTF-8 is the UTF-16 length.
- `ValidWtf8` is strict: a lead surrogate's bytes directly before a trail surrogate's fail.
- On `x86_64-freestanding` the byte routines and `ValidUtf8` use general registers only, and `Dot` and `Sum` never take their avx2 form.

The prelude's string functions use these routines on longer inputs:

- `str.From` and `str.FromC` call `ValidUtf8` from 256 bytes.
- A one-byte `Find` and `Slice[u8].Index` call `IndexByte` past the first 32 bytes searched.
- `StartsWith` and `EndsWith` call `Equal` above 16 bytes.

Below those lengths, and always on `x86_64-freestanding`, they run their own word-at-a-time forms, which give the same results.

## Examples

Each program with a `Main` below runs as written with `mk run`; the last example compiles with `mk check`.

### A vector loop

```mk
// verdict: ok
import std.simd

// Scale multiplies every element of xs by k, four lanes at a time,
// then finishes the elements that do not fill a vector.
Scale(xs: mut []f32, k: f32) {
    kv := simd.Vec[f32, 4].Splat(k) // every lane is k
    var i := 0
    for i + 4 <= xs.Len() {
        v := simd.Vec[f32, 4].FromSlice(xs[..], i) // lanes xs[i] .. xs[i + 3]
        (v * kv).CopyTo(xs, i)
        i += 4
    }
    for i < xs.Len() {
        xs[i] *= k
        i += 1
    }
}

export Main(sys: &System): WriteError!void {
    var xs: [6]f32 = [1.0, 2.0, 3.0, 4.0, 5.0, 6.0]
    Scale(mut xs[..], 0.5)
    try sys.Print("{xs}\n", (xs: xs[..]))

    a := simd.Vec[i32, 4].FromArray([1, 2, 3, 4])
    b := simd.Vec[i32, 4].Splat(10)
    sum := a +% b // integer lanes take the wrapping operators
    lanes := sum.ToArray() // Vec has no Format: print its lanes
    try sys.Print("{s} total {t}\n", (s: lanes[..], t: sum.ReduceSumWrap()))
    rev := sum.Reverse().ToArray()
    try sys.Print("reversed {r}, lane 3 is {l}\n", (r: rev[..], l: sum.Lane(3)))
}
```

```text
$ mk run vec.mk
[0.5, 1, 1.5, 2, 2.5, 3]
[11, 12, 13, 14] total 50
reversed [14, 13, 12, 11], lane 3 is 14
```

- `FromSlice` and `CopyTo` check the span once per vector, so the loop body has no per-element bounds checks.

### Search with a mask

```mk
// verdict: ok
import std.simd

// FirstEscape returns the offset of the first byte of s that a JSON string
// must escape: a control byte, a double quote or a backslash.
FirstEscape(s: []u8): ?int {
    control := simd.Vec[u8, 16].Splat(0x20)
    dquote := simd.Vec[u8, 16].Splat(b'"')
    backslash := simd.Vec[u8, 16].Splat(b'\\')
    var i := 0
    for i + 16 <= s.Len() {
        v := simd.Vec[u8, 16].FromSlice(s, i)
        m := (v < control) | (v == dquote) | (v == backslash) // a Mask: one truth value per lane
        if k := m.FirstSet() { // the lowest set lane, or none
            return some(i + k)
        }
        i += 16
    }
    for j in i..s.Len() { // the tail, one byte at a time
        if s[j] < 0x20 || s[j] == b'"' || s[j] == b'\\' {
            return some(j)
        }
    }
    return none
}

export Main(sys: &System): WriteError!void {
    for text in ["plain text, nothing to escape here", "a longer line with a \"quote\" in it", "tab\there"] {
        k := FirstEscape(text.AsBytes()) else { -1 }
        try sys.Print("{k}\n", (k))
    }
}
```

```text
$ mk run mask.mk
-1
21
3
```

- A `Mask` is not a `bool`: test it with `Any`, `All` or `FirstSet`.
- For a set of bytes known only at run time, `IndexAny` does this search for you.

### Byte routines

```mk
// verdict: ok
import std.simd

export Main(sys: &System): WriteError!void {
    line := "GET /index.html HTTP/1.1".AsBytes()
    sp := simd.IndexByte(line, b' ') else { -1 } // none when absent
    last := simd.LastIndexByte(line, b' ') else { -1 }
    slashes := simd.Count(line, b'/')
    try sys.Print("first space {sp}, last space {last}, {slashes} slashes\n", (sp, last, slashes))
    q := simd.IndexAny(line, "?#".AsBytes()) else { -1 }
    try sys.Print("query or fragment at {q}\n", (q))
    try sys.Print("equal {e}, compare {c}\n", (e: simd.Equal(line[0..3], "GET".AsBytes()), c: simd.Compare("abc".AsBytes(), "abd".AsBytes())))
    try sys.Print("ascii {a}, utf-8 {u}, utf-16 units {n}\n", (a: simd.IsAscii("héllo".AsBytes()), u: simd.ValidUtf8("héllo".AsBytes()), n: simd.Utf16Len("héllo 😀")))
}
```

```text
$ mk run bytes.mk
first space 3, last space 15, 2 slashes
query or fragment at -1
equal true, compare -1
ascii false, utf-8 true, utf-16 units 8
```

- `Utf16Len` counts two units for a scalar above U+FFFF, such as the emoji. `Utf16LenBytes` counts the same for a `[]u8`.

### Dot products and sums

```mk
// verdict: ok
import std.simd

export Main(sys: &System): WriteError!void {
    var xs: [40]f64 = [0.0; 40]
    var ys: [40]f64 = [0.0; 40]
    for i in 0..40 {
        xs[i] = Cast[f64](i) * 0.1
        ys[i] = 2.0
    }
    d := simd.Dot(xs[..], ys[..]) // the same bits on every CPU
    s := simd.Sum(xs[..])
    try sys.Print("dot {d}, sum {s}\n", (d, s))

    neg: [3]f32 = [-0.0, -0.0, -0.0]
    try sys.Print("sum of -0.0s: {z}\n", (z: simd.Sum(neg[..]))) // starts at +0.0

    cpu := simd.Cpu() // read once, then cached
    try sys.Print("avx2: {a}\n", (a: cpu.Has("avx2")))
}
```

```text
$ mk run dot.mk
dot 156, sum 78
sum of -0.0s: 0
avx2: true
```

- The last line depends on the machine: `avx2` is above the hosted baseline, `x86-64-v2`.
- The order of additions is fixed, so `Dot` and `Sum` may differ from a plain loop by rounding, but never from one CPU to another.

### Widening at compile time

At compile time each operation runs lane by lane, so a vector expression can initialize a constant.

```mk
// verdict: ok
// WidenLo sign-extends a signed lane and zero-extends an unsigned one, here at compile time
import std.simd

const Signed: i16 = simd.Vec[i8, 16].Splat(-1).WidenLo[i16, 8]().Lane(0)
const Unsigned: u16 = simd.Vec[u8, 16].Splat(255).WidenLo[u16, 8]().Lane(0)

Extended() requires Signed == -1 && Unsigned == 255 {
}

Check() {
    Extended()
}
```

It has no `Main`: `mk check` accepts it only if both clauses hold.

## API

<!-- api:begin -->
Stability: Provisional.

### `Choice`

```
export struct Choice[F]
```

One entry of a dispatch table: the function f, to be used when the
running CPU has every feature in needs. Plain.

Fields:

- `needs: CpuFeatures`: The CPU features f needs, all of which the running CPU must have.
- `f: F`: The function to use when the running CPU has every feature in needs.

### `Compare`

```
export Compare(a: []u8, b: []u8): int
```

Orders a and b by their bytes as unsigned numbers: -1, 0 or 1 by the
first byte that differs, and a proper prefix before the longer slice.
O(n) in the shorter length; it allocates nothing. avx2 is used when
both slices hold at least 64 bytes and the CPU has it.

### `Count`

```
export Count(s: []u8, b: u8): int
```

Returns the number of bytes of s equal to b. O(n); it allocates nothing.

Matches are counted in byte-wide vector counters, which are added into
the total every 255 steps; the tail is one overlapping compare. avx2 is
used from 64 bytes when the CPU has it.

### `Cpu`

```
export Cpu(): CpuFeatures
```

Returns the running CPU's features, read once and cached. A feature
whose registers the OS does not save is not reported, so every feature
reported can run. At compile time, the target's baseline. O(1) after the
first call; it allocates nothing.

### `CpuFeatures`

```
export struct CpuFeatures
```

A set of CPU features of the target architecture, such as avx2 or
sse4.1. Plain, 16 bytes.

### `CpuFeatures.Has`

```
export CpuFeatures.Has(&self, const name: str): bool
```

Reports whether the set holds every feature name implies; an unknown name is
E1106, another architecture's name is false. O(1); allocates nothing.

### `CpuFeatures.HasAll`

```
export CpuFeatures.HasAll(&self, other: &CpuFeatures): bool
```

Reports whether the set holds every feature of other. O(1); allocates
nothing.

### `CpuFeatures.Of`

```
export CpuFeatures.Of(const names: str): CpuFeatures
```

The set named by names, written as in a `cpu` clause ("avx2,bmi2" or
"x86-64-v3"); an unknown name is E1106. A constant; it allocates nothing.

### `Dispatch`

```
export Dispatch[F: Plain](choices: []Choice[F], base: F): F
```

- Flows: The result borrows from base. The result borrows what choices refers to; choices is free after the call.

Returns the f of the first choice whose needs the running CPU has, or
base when none matches. Order the choices best first. The CPU is read
once per program and cached. O(choices.Len()), two word compares per
choice; it allocates nothing. At compile time the CPU is the target's
baseline, so a choice that needs more is never taken there.

### `Dot`

```
export Dot[T: Lane](a: []T, b: []T): T
```

- Flows: The result borrows what a refers to; a is free after the call. The result borrows what b refers to; b is free after the call.

Returns the dot product of a and b, the sum of a[i] * b[i], in a fixed
order of additions that is the same on every CPU, so every run gives
the same bits. It panics with code 2 when the lengths differ. T is f32
or f64 (E0701 for integer lanes). O(n); it allocates nothing.

The order: 16 partial sums, the jth taking the elements j, j + 16, j +
32 and so on up to the last multiple of 16, each starting at +0.0 and
each product rounded before it is added. The 16 sums are added as a
pairwise tree, as ReduceSum does, and then the tail in index order.

### `Equal`

```
export Equal(a: []u8, b: []u8): bool
```

Reports whether a and b hold the same bytes. O(n); it allocates nothing.

Unequal lengths answer at once. Otherwise each step compares four
vectors and tests them once, and the tail is one overlapping load;
below one vector's width, two overlapping loads of 8, 4 or 2 bytes.
avx2 is used from 64 bytes and avx512bw from 256 when the CPU has them.

### `IndexAny`

```
export IndexAny(s: []u8, set: []u8): ?int
```

Returns the offset of the first byte of s that is in set, or none (also
when set is empty). O(n + set.Len()); it allocates nothing.

A set of one byte is IndexByte. Two or three bytes are compared lane by
lane. Four or more are looked up in two 16-byte tables built on the
stack once per call, in O(set.Len()), so a large set costs no more per
byte of s than a set of four.

### `IndexByte`

```
export IndexByte(s: []u8, b: u8): ?int
```

Returns the offset of the first byte of s equal to b, or none. O(n); it
allocates nothing.

Each step compares four vectors and tests them once; the tail is one
overlapping load of the last vector's worth of bytes. avx2 is used from
64 bytes and avx512bw from 256 when the CPU has them.

### `IsAscii`

```
export IsAscii(s: []u8): bool
```

Reports whether every byte of s is below 0x80; true for an empty s.
O(n); it allocates nothing. Each step tests four vectors at once, and
the tail is one overlapping load.

### `Lane`

```
export interface Lane {
}
```

The types a vector lane may have: the sized integers, int, uint, f32 and
f64. The bound is built in, so an impl of it is E0704.

### `Lanes`

```
export Lanes[T: Lane](): int
```

The natural lane count of T, Target.vector_bits / (8 * SizeOf[T]()): 16 for
u8 and 4 for f32 on every supported target. A constant for a concrete T,
usable as a const argument (simd.Vec[f32, simd.Lanes[f32]()]); of a type
parameter it is a value at run time and not a const argument (E0703).

### `LastIndexByte`

```
export LastIndexByte(s: []u8, b: u8): ?int
```

Returns the offset of the last byte of s equal to b, or none. O(n); it
allocates nothing. It works as IndexByte does, from the end of s.

### `Lookup16`

```
export Lookup16[const N: int](idx: Vec[u8, N], table: Vec[u8, 16]): Vec[u8, N] requires N == 16 || N == 32 || N == 64
```

Maps each lane of idx below 16 through table, and every other lane to 0
(pshufb). Inline; allocates nothing.

### `Mask`

```
export struct Mask[T: Lane, const N: int]
```

The result of a lane-wise comparison of two Vec[T, N]: laid out as Vec over
the unsigned integer of T's width, each lane all zeros or all ones. Plain.

### `Mask.All`

```
export Mask[T, N].All(self): bool requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

Reports whether every lane is set. Inline; allocates nothing.

### `Mask.Any`

```
export Mask[T, N].Any(self): bool requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

Reports whether any lane is set. Inline; allocates nothing.

### `Mask.Bits`

```
export Mask[T, N].Bits(self): u64 requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

The lanes as bits: bit j set when lane j is. Inline; allocates nothing.

### `Mask.Cast`

```
export Mask[T, N].Cast[U: Lane](self): Mask[U, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self.

The same lanes as a mask over U. Inline; allocates nothing.

### `Mask.CountSet`

```
export Mask[T, N].CountSet(self): int requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

The number of lanes set. Inline; allocates nothing.

### `Mask.FirstSet`

```
export Mask[T, N].FirstSet(self): ?int requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

The index of the lowest lane set, or none. Inline; allocates nothing.

### `Mask.FromBits`

```
export Mask[T, N].FromBits(b: u64): Mask[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

The mask whose lane j is set when bit j of b is; bits from N up are ignored.
Inline; allocates nothing.

### `Mask.Select`

```
export Mask[T, N].Select(self, a: Vec[T, N], b: Vec[T, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self, a and b.

Lane j of a where lane j of self is set, else of b. Inline; allocates
nothing.

### `Sum`

```
export Sum[T: Lane](xs: []T): T
```

- Flows: The result borrows what xs refers to; xs is free after the call.

Returns the sum of xs, in the same fixed order of additions as Dot, so
every CPU gives the same bits. A sum of -0.0 elements alone is +0.0. T
is f32 or f64 (E0701 for integer lanes). O(n); it allocates nothing.

### `Utf16Len`

```
export Utf16Len(s: str): int
```

Returns the number of UTF-16 code units s takes: one per scalar, two
for a scalar above U+FFFF. O(n); it allocates nothing. It is
Utf16LenBytes over s's bytes.

### `Utf16LenBytes`

```
export Utf16LenBytes(b: []u8): int
```

Returns the number of UTF-16 code units the UTF-8 or WTF-8 bytes b
take: one per scalar, two for a scalar above U+FFFF, and one for a
lone surrogate's three bytes. O(n); it allocates nothing.

It counts the bytes that are not continuation bytes (10xxxxxx) and adds
one for each byte from 0xF0 on, a four-byte lead. That count is the
answer on any bytes, so it never depends on the CPU, though on bytes
that are not UTF-8 or WTF-8 it is not a decoder's count.

### `ValidUtf8`

```
export ValidUtf8(s: []u8): bool
```

Reports whether s is well-formed UTF-8, as Unicode's table 3-7 defines
it: no overlong form, no surrogate D800 to DFFF, nothing above 10FFFF,
no truncated sequence and no stray continuation byte. O(n); it
allocates nothing. It uses avx2 or avx512bw when the CPU has them; the
answer never depends on the CPU.

### `ValidWtf8`

```
export ValidWtf8(s: []u8): bool
```

Reports whether s is well-formed WTF-8, as the WTF-8 specification
defines it: UTF-8 by Unicode's table 3-7, except that a surrogate's
three bytes (ED A0..BF 80..BF) are allowed. A lead surrogate (ED A0..AF)
followed at once by a trail surrogate (ED B0..BF) is refused, since
WTF-8 writes that pair as its scalar's four bytes. So every str is
WTF-8, and a JavaScript string's lone surrogates fit. O(n); it
allocates nothing.

Windows file names are converted more leniently: that conversion also
takes a lead and trail surrogate written as two three-byte sequences.

### `Vec`

```
export struct Vec[T: Lane, const N: int]
```

N lanes of T, laid out as [N]T and aligned as T. Every operation requires N
to be a power of two from 2 to 64; the type is well formed for any N. Plain.

### `Vec.Abs`

```
export Vec[T, N].Abs(self): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self.

The absolute value of each float lane (E0701 for integer lanes); AbsWrap
is the integer form. Inline; allocates nothing.

### `Vec.AbsWrap`

```
export Vec[T, N].AbsWrap(self): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self.

The absolute value of each signed integer lane, MIN left as it is (E0701
for other lanes). Inline; allocates nothing.

### `Vec.AddSat`

```
export Vec[T, N].AddSat(self, b: Vec[T, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self and b.

Each pair of integer lanes added, clamped to the lane range (E0701 for
float lanes). Inline; allocates nothing.

### `Vec.Broadcast`

```
export Vec[T, N].Broadcast(self, const i: int): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64 requires i >= 0 && i < N
```

- Flows: The result borrows from self.

A vector whose every lane is lane i of self; i is a constant. Inline;
allocates nothing.

### `Vec.Cast`

```
export Vec[T, N].Cast[U: Lane](self): Vec[U, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self.

Each lane converted as scalar Cast does: panics when a lane does not fit,
passes NaN and the infinities to a float lane. Inline; allocates nothing.

### `Vec.CastSat`

```
export Vec[T, N].CastSat[U: Lane](self): Vec[U, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self.

Each lane converted to U, saturating at U's range; NaN gives 0. Inline;
allocates nothing.

### `Vec.Ceil`

```
export Vec[T, N].Ceil(self): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self.

Each float lane rounded up (E0701 for integer lanes); on x86_64 it
needs sse4.1, as Floor does (E1106 without). Inline; allocates nothing.

### `Vec.CopyTo`

```
export Vec[T, N].CopyTo(self, xs: mut []T, i: int) requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

Stores the lanes to xs from i; panics (code 2) unless i >= 0 and i + N <=
xs.Len(), checked once. Inline; allocates nothing.

### `Vec.CopyToMasked`

```
export Vec[T, N].CopyToMasked(self, xs: mut []T, i: int, m: Mask[T, N]) requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

Stores the lanes set in m to xs from i, checking only those against xs's
bounds (panic, code 2). Inline; allocates nothing.

### `Vec.DeinterleaveEven`

```
export Vec[T, N].DeinterleaveEven(self, b: Vec[T, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self and b.

The even lanes of self, then the even lanes of b. Inline; allocates nothing.

### `Vec.DeinterleaveOdd`

```
export Vec[T, N].DeinterleaveOdd(self, b: Vec[T, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self and b.

The odd lanes of self, then the odd lanes of b. Inline; allocates nothing.

### `Vec.DivTrunc`

```
export Vec[T, N].DivTrunc(self, b: Vec[T, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self and b.

Each pair of integer lanes divided, truncating (E0701 for float lanes,
which take /); panics on a zero divisor lane or a MIN / -1 pair. Inline;
allocates nothing.

### `Vec.Floor`

```
export Vec[T, N].Floor(self): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self.

Each float lane rounded down (E0701 for integer lanes). On x86_64 it
compiles only where the function's features hold sse4.1, which the hosted
baseline does, and is E1106 elsewhere: Mk links no floor routine to fall
back on. Inline; allocates nothing.

### `Vec.Fma`

```
export Vec[T, N].Fma(self, b: Vec[T, N], c: Vec[T, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self, b and c.

self * b + c rounded once, per float lane (E0701 for integer lanes). It
compiles only in a function whose features hold fma, E1106 elsewhere,
since Mk links no fma routine to fall back on. Inline; allocates nothing.

### `Vec.FromArray`

```
export Vec[T, N].FromArray(a: [N]T): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from a.

The vector of a's elements, lane j from a[j]. Inline; allocates nothing.

### `Vec.FromSlice`

```
export Vec[T, N].FromSlice(xs: []T, i: int): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows what xs refers to; xs is free after the call.

The N elements of xs from i; panics (code 2) unless i >= 0 and i + N <=
xs.Len(), checked once. Inline; allocates nothing.

### `Vec.FromSliceOr`

```
export Vec[T, N].FromSliceOr(xs: []T, i: int, fill: Vec[T, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from fill. The result borrows what xs refers to; xs is free after the call.

The elements of xs from i up to xs.Len(), the remaining lanes from fill;
panics (code 2) unless 0 <= i <= xs.Len(). Inline; allocates nothing.

### `Vec.Gather`

```
export Vec[T, N].Gather[I: Lane](xs: []T, idx: Vec[I, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from idx. The result borrows what xs refers to; xs is free after the call.

The vector whose lane j is xs[idx lane j]; every index is checked (panic,
code 2). The index lanes I are integers (E0701 otherwise). Inline;
allocates nothing.

### `Vec.InterleaveHi`

```
export Vec[T, N].InterleaveHi(self, b: Vec[T, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self and b.

The high halves of self and b, lane by lane alternately. Inline; allocates
nothing.

### `Vec.InterleaveLo`

```
export Vec[T, N].InterleaveLo(self, b: Vec[T, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self and b.

The low halves of self and b, lane by lane alternately: self 0, b 0, self 1,
b 1, and so on. Inline; allocates nothing.

### `Vec.Iota`

```
export Vec[T, N].Iota(): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

The vector whose lane j is j. Inline; allocates nothing.

### `Vec.Lane`

```
export Vec[T, N].Lane(self, const i: int): T requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64 requires i >= 0 && i < N
```

- Flows: The result borrows from self.

Lane i; i is a constant from 0 to N - 1 (E0706 otherwise). Inline; allocates
nothing.

### `Vec.Max`

```
export Vec[T, N].Max(self, b: Vec[T, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self and b.

The greater of each pair of lanes; on floats NaN when either is NaN, -0
below +0. Inline; allocates nothing.

### `Vec.Min`

```
export Vec[T, N].Min(self, b: Vec[T, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self and b.

The lesser of each pair of lanes; on floats NaN when either is NaN, -0 below
+0. Inline; allocates nothing.

### `Vec.MulHigh`

```
export Vec[T, N].MulHigh(self, b: Vec[T, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self and b.

The high half of each product of integer lanes of at most 32 bits (E0701
otherwise). Inline; allocates nothing.

### `Vec.MulWidenHi`

```
export Vec[T, N].MulWidenHi[U: Lane, const M: int](self, b: Vec[T, N]): Vec[U, M] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64 requires M * 2 == N && M >= 2 && SizeOf[U]() == 2 * SizeOf[T]()
```

The exact products of the high halves of integer lanes of self and b, as
lanes twice as wide (U of T's class and signedness, E0701 otherwise).
Inline; allocates nothing.

### `Vec.MulWidenLo`

```
export Vec[T, N].MulWidenLo[U: Lane, const M: int](self, b: Vec[T, N]): Vec[U, M] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64 requires M * 2 == N && M >= 2 && SizeOf[U]() == 2 * SizeOf[T]()
```

The exact products of the low halves of integer lanes of self and b, as
lanes twice as wide (U of T's class and signedness, E0701 otherwise).
Inline; allocates nothing.

### `Vec.NarrowSat`

```
export Vec[T, N].NarrowSat[U: Lane, const M: int](self, b: Vec[T, N]): Vec[U, M] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64 requires M == 2 * N && M <= 64 && 2 * SizeOf[U]() == SizeOf[T]()
```

The integer lanes of self, then of b, each clamped to the range of U,
half as wide (E0701 for float lanes). Inline; allocates nothing.

### `Vec.ReduceAnd`

```
export Vec[T, N].ReduceAnd(self): T requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self.

The integer lanes ANDed (E0701 for float lanes). Inline; allocates nothing.

### `Vec.ReduceMax`

```
export Vec[T, N].ReduceMax(self): T requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self.

The greatest lane; NaN when a lane is NaN, -0 below +0. Inline; allocates
nothing.

### `Vec.ReduceMin`

```
export Vec[T, N].ReduceMin(self): T requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self.

The least lane; NaN when a lane is NaN, -0 below +0. Inline; allocates
nothing.

### `Vec.ReduceOr`

```
export Vec[T, N].ReduceOr(self): T requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self.

The integer lanes ORed (E0701 for float lanes). Inline; allocates nothing.

### `Vec.ReduceSum`

```
export Vec[T, N].ReduceSum(self): T requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self.

The float lanes added as a pairwise tree, each half to the other, down to
one lane (E0701 for integer lanes). Inline; allocates nothing.

### `Vec.ReduceSumWide`

```
export Vec[T, N].ReduceSumWide(self): i64 requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

The integer lanes added exactly in i64; lanes of at most 32 bits only
(E0701 otherwise). Inline; allocates nothing.

### `Vec.ReduceSumWrap`

```
export Vec[T, N].ReduceSumWrap(self): T requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self.

The integer lanes added, wrapping; an integer sum has no trapping form,
so use ReduceSumWide for an exact one (E0701 for float lanes). Inline;
allocates nothing.

### `Vec.ReduceXor`

```
export Vec[T, N].ReduceXor(self): T requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self.

The integer lanes XORed (E0701 for float lanes). Inline; allocates nothing.

### `Vec.Reinterpret`

```
export Vec[T, N].Reinterpret[U: Lane, const M: int](self): Vec[U, M] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64 requires M * SizeOf[U]() == N * SizeOf[T]() && M >= 2 && M <= 64
```

The same bytes as M lanes of U (M * SizeOf[U]() == N * SizeOf[T]()). Inline;
allocates nothing.

### `Vec.Reverse`

```
export Vec[T, N].Reverse(self): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self.

The lanes in reverse order. Inline; allocates nothing.

### `Vec.Rotate`

```
export Vec[T, N].Rotate(self, const k: int): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64 requires k >= 0 && k < N
```

- Flows: The result borrows from self.

The lanes rotated down by k: lane j is lane (j + k) mod N. Inline; allocates
nothing.

### `Vec.RoundEven`

```
export Vec[T, N].RoundEven(self): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self.

Each float lane rounded to nearest, ties to even (E0701 for integer
lanes); on x86_64 it needs sse4.1, as Floor does (E1106 without).
Inline; allocates nothing.

### `Vec.RoundToZero`

```
export Vec[T, N].RoundToZero(self): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self.

Each float lane rounded toward zero (E0701 for integer lanes); on x86_64
it needs sse4.1, as Floor does (E1106 without). Inline; allocates nothing.

### `Vec.ShlEach`

```
export Vec[T, N].ShlEach(self, k: Vec[T, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self and k.

Each integer lane shifted left by the matching lane of k (E0701 for
float lanes); panics on an amount at or above the lane width. Inline;
allocates nothing.

### `Vec.ShrEach`

```
export Vec[T, N].ShrEach(self, k: Vec[T, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self and k.

Each integer lane shifted right by the matching lane of k, arithmetic
on signed lanes (E0701 for float lanes); panics on an amount at or above
the lane width. Inline; allocates nothing.

### `Vec.Shuffle`

```
export Vec[T, N].Shuffle(self, b: Vec[T, N], const idx: [N]int): Vec[T, N] from self, b requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self and b.

The vector whose lane j is lane idx[j] of self (0 to N - 1) or of b (N to 2N
- 1); an index outside is E0706. Inline; allocates nothing.

### `Vec.Slide`

```
export Vec[T, N].Slide(self, prev: Vec[T, N], const k: int): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64 requires k >= 0 && k <= N
```

- Flows: The result borrows from self and prev.

The last k lanes of prev followed by the first N - k of self. Inline;
allocates nothing.

### `Vec.Splat`

```
export Vec[T, N].Splat(x: T): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from x.

A vector whose every lane is x. Inline; allocates nothing.

### `Vec.Sqrt`

```
export Vec[T, N].Sqrt(self): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self.

The correctly rounded square root of each float lane (E0701 for integer
lanes). Inline; allocates nothing.

### `Vec.SubSat`

```
export Vec[T, N].SubSat(self, b: Vec[T, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self and b.

Each pair of integer lanes subtracted, clamped to the lane range (E0701
for float lanes). Inline; allocates nothing.

### `Vec.ToArray`

```
export Vec[T, N].ToArray(self): [N]T requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self.

The lanes as an array. Inline; allocates nothing.

### `Vec.Trunc`

```
export Vec[T, N].Trunc[U: Lane](self): Vec[U, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

- Flows: The result borrows from self.

Each integer lane cut to U, keeping its low bits (E0701 for float lanes).
Inline; allocates nothing.

### `Vec.WidenHi`

```
export Vec[T, N].WidenHi[U: Lane, const M: int](self): Vec[U, M] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64 requires M * 2 == N && M >= 2 && SizeOf[U]() == 2 * SizeOf[T]()
```

The high half of the lanes as lanes twice as wide, as WidenLo converts
them. Inline; allocates nothing.

### `Vec.WidenLo`

```
export Vec[T, N].WidenLo[U: Lane, const M: int](self): Vec[U, M] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64 requires M * 2 == N && M >= 2 && SizeOf[U]() == 2 * SizeOf[T]()
```

The low half of the lanes as lanes twice as wide: a signed lane
sign-extended, an unsigned one zero-extended, an f32 to the same f64. U
has T's class and signedness (E0701 otherwise). Inline; allocates nothing.

### `Vec.With`

```
export Vec[T, N].With(self, const i: int, x: T): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64 requires i >= 0 && i < N
```

- Flows: The result borrows from self and x.

self with lane i replaced by x; i is a constant from 0 to N - 1. Inline;
allocates nothing.

### `Vec.Zero`

```
export Vec[T, N].Zero(): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
```

A vector of zero lanes. Inline; allocates nothing.

<!-- api:end -->
