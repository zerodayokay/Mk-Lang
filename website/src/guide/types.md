---
description: Mk's built-in types - integers, floats, bool, char, str, arrays, slices, references, optionals and records - and the three explicit conversion forms T(x), Cast and Trunc.
---

# Types and conversions

Every value has a type known at compile time. Mk converts nothing implicitly: an `i32` never becomes an `i64`, and an integer never becomes a float, unless you write the conversion.

## The built-in types

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    n: u8 = 200 // an integer of the expected type
    x := 2.5 // a float: f64
    ok := n > 100 // a bool
    c := 'λ' // a char
    s := "Mk" // a str
    a := [1, 2, 3] // an array: [3]int
    r := (lo: 1, hi: 9) // a record
    o: ?int = some(4) // an optional
    try sys.Print("{n} {x} {ok} {c} {s} {len} {lo} {o}\n", (n, x, ok, c, s, len: a.Len(), lo: r.lo, o))
}
```

```text
200 2.5 true λ Mk 3 1 some(4)
```

| Type | What it is |
|---|---|
| `i8 i16 i32 i64`, `u8 u16 u32 u64` | Fixed-width integers |
| `int`, `uint` | Pointer-sized integers, 64 bits on every supported target. Lengths and counts are `int` |
| `f32 f64` | IEEE 754 floats |
| `bool` | `true` or `false` |
| `char` | One Unicode scalar value |
| `str` | A read-only view of valid UTF-8 ([strings](./strings)) |
| `[N]T` | An array: `N` values held inline ([collections](./collections)) |
| `[]T`, `mut []T` | A slice: a shared or exclusive view of consecutive values |
| `&T`, `mut T` | A shared or exclusive reference. Never null ([memory](./memory#borrowing)) |
| `?T` | An optional: `some(x)` or `none` ([optionals](./errors#optionals)) |
| `E!T` | An error from the set `E`, or a `T` ([errors](./errors)) |
| `(a: T, b: U)` | A record: a struct with no name ([records](./structs-and-enums#records)) |
| `fn(T): U` | A function value, with no captures ([functions](./functions#function-values)) |
| `void`, `never` | No value; a function that never returns |
| `*T` | A raw pointer, dereferenced only in `unsafe` code ([unsafe](./unsafe-and-c)) |

You declare more with `struct`, `enum` and `error` ([structs and enums](./structs-and-enums)), and give any type a second name with `type`.

## Integers

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    a: u8 = 250 // a literal takes the type it is expected to have
    b := 1_000_000 // nothing expected: int
    c := a +% 10 // wrapping add: 260 wraps to 4
    d := -7 / 2 // division truncates toward zero
    e := -7 % 2 // the remainder has the sign of the dividend
    try sys.Print("{a} {b} {c} {d} {e}\n", (a, b, c, d, e))
}
```

```text
250 1000000 4 -3 -1
```

- Integer literals take `_` separators and the prefixes `0x`, `0o` and `0b`: `0xFF`, `0b1010_1010`.
- A literal takes the type it is expected to have, and is `int` when nothing is expected. A literal that does not fit its type, such as `256` for a `u8`, is E0303.
- `int` and `i64` are different types, as are `uint` and `u64`.
- `+`, `-` and `*` panic on overflow in every build, and `/` and `%` panic on a zero divisor. `+%`, `-%` and `*%` wrap ([operators](./operators#arithmetic)).

## Floats and `bool`

`f32` and `f64` are IEEE 754 floats. A float literal has a `.` or an exponent (`1.0`, `2.5e-3`) and is `f64` unless an `f32` is expected. An integer literal is never a float, so `x / 2` on an `f64` is E0301: write `x / 2.0`. Float division by zero does not panic; it gives an infinity or NaN.

`bool` is `true` or `false`. A condition (`if`, `for`, `Assert`) must be a `bool`: an integer is not a condition, so write `n != 0`.

## Characters and bytes

A `char` is one Unicode scalar value and takes 4 bytes. A byte is a `u8`, written `b'a'`.

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    c := 'é'
    nl := '\n'
    smile := '\u{1F600}'
    code := u32(c) // a char widens to any integer of 32 bits or more
    byte := b'A' // a u8
    letter := char(byte) // a u8 widens to a char
    next := Cast[char](u32(c) + 1) // back from an integer: Cast checks the value
    later := c < 'z' // chars compare by scalar value
    try sys.Print("{c} {code} {byte} {letter} {next} {later} {smile}{nl}", (c, code, byte, letter, next, later, smile, nl))
}
```

```text
é 233 65 A ê false 😀
```

A `char` has no arithmetic: convert it to `u32`, compute, and convert back with `Cast[char]`. Text is UTF-8 bytes, not `char`s ([strings](./strings)).

## Conversions

There are three explicit forms:

| Form | Converts | When the value does not fit |
|---|---|---|
| `T(x)` | Only when no value can change, such as `i32` to `i64` | Does not compile (E0303) |
| `Cast[T](x)` | Between integers, floats, `char` and enums with a tag type | Panics. An integer to a float rounds instead |
| `Trunc[T](x)` | Between integers | Keeps the low bits |

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    small: i32 = 300
    a := i64(small) // widening: every i32 fits an i64
    b := f64(small) // every i32 is exact in an f64
    c := Cast[u8](small / 2) // 150 fits a u8
    d := Trunc[u8](small) // the low 8 bits: 300 is 0x12C, so 0x2C
    e := Cast[i32](-2.7) // toward zero
    f := Cast[f32](0.1) // rounded to the nearest f32
    g := u8(200) // a constant that fits needs no Cast
    try sys.Print("{a} {b} {c} {d} {e} {f} {g}\n", (a, b, c, d, e, f, g))
    h := Cast[u8](small) // 300 does not fit a u8
    try sys.Print("{h}\n", (h))
}
```

```text
300 300 150 44 -2 0.1 200
panic: cast at main.mk:12:10
backtrace:
  Main (main.mk)
```

`T(x)` on a conversion that could lose a value does not compile:

```mk
// verdict: reject E0303
Narrow(a: i64): i32 {
    return i32(a) // <- E0303
}
```

Write `Cast[i32](a)` to check the value at run time, or `Trunc[i32](a)` to keep its low bits. `bool`, `str`, references and slices convert to nothing (E0302): for a number from a `bool`, write `if b { 1 } else { 0 }`. [Conversions](/reference/language/types-and-kinds#conversions) lists every allowed `T(x)`.

## `void` and `never`

A function with no result type returns `void`. A function whose result is `never` does not return at all, like `Panic`, so a call to one ends a path:

```mk
// verdict: ok
Fatal(msg: str): never {
    Panic(msg)
}

Digit(c: char): int {
    if c < '0' || c > '9' {
        Fatal("not a digit") // a never call ends this path: no return needed
    }
    return int(u32(c) - u32('0'))
}
```

## Copy, move and release

Every type also has a *kind*, which says what happens when you use a value:

| Kind | On use | Examples |
|---|---|---|
| Plain | Copied | Numbers, `bool`, `char`, `str`, `&T`, `[]T` |
| Unique | Moved; the old name can no longer be used | `mut T`, `List[T]`, a struct holding one |
| Linear | Moved, and must be consumed exactly once on every path | Files, lock guards, heap containers |

A struct takes the strictest kind among its fields. [Memory and ownership](./memory#plain-unique-and-linear-values) explains what this means for your code.

## Type aliases

`type` gives a type a second name. The two names are the same type:

```mk
// verdict: ok
type Span = (lo: int, hi: int)

Widen(s: Span, by: int): Span {
    return (lo: s.lo - by, hi: s.hi + by)
}
```

## Next

[Operators](./operators): arithmetic, comparison, logic and bits, and how they bind.
