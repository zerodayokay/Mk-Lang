---
description: Mk's arithmetic, wrapping, bitwise, comparison and logical operators, how they bind, and what panics - overflow, division by zero and bad shifts are checked in every build.
---

# Operators

Mk's operators are C's, with a few deliberate differences:

- Integer `+ - *` panic on overflow, in release builds too. `+% -% *%` wrap.
- `<<`, `>>` and `&` bind like `*`, and `|` and `^` like `+`, so all of them bind tighter than comparisons.
- Operands are never converted implicitly: both sides have one type.
- Comparisons do not chain: `a < b < c` is an error.
- There is no `++`, no `?:` and no operator overloading.

## Arithmetic

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    q := 7 / -2 // division truncates toward zero
    m := 7 % -2 // the remainder has the sign of the dividend
    m2 := -7 % 2
    try sys.Print("{q} {m} {m2}\n", (q, m, m2))
    big: u8 = 250
    w := big +% 10 // wraps: 260 - 256
    k: i8 = 127
    kw := k +% 1 // wraps to the minimum
    try sys.Print("{w} {kw}\n", (w, kw))
    f := 7.0 / 2.0
    fm := -7.5 % 2.0
    zero := 0.0
    inf := 1.0 / zero // floats do not panic
    nan := zero / zero
    same := nan == nan // NaN equals nothing, itself included
    try sys.Print("{f} {fm} {inf} {same}\n", (f, fm, inf, same))
}
```

```text
-3 1 -1
4 -128
3.5 -1.5 inf false
```

| Operator | On integers | On floats |
|---|---|---|
| `+ - *` | Panics on overflow | IEEE 754 |
| `/` | Truncates toward zero. Panics on a zero divisor | IEEE 754 |
| `%` | Remainder with the sign of the dividend. Panics on a zero divisor | Remainder with the sign of the dividend |
| `+% -% *%` | Wraps around, never panics | Not allowed |
| `-x` | Signed types only. Panics on the minimum | IEEE 754 |

An overflow is a panic, reported at the expression, and the process exits with status 101:

```mk
// verdict: ok
export Main(sys: &System): int {
    x: u8 = 255
    n := sys.args.Len() // 1: the program's path
    y := x + Cast[u8](n)
    return int(y)
}
```

```text
panic: overflow at main.mk:5:10
backtrace:
  Main (main.mk)
```

When wrapping is what you want, as in a hash function, say so with the wrapping operator:

```mk
// verdict: ok
Hash(data: []u8): u32 {
    var h: u32 = 2166136261
    for b in data {
        h = (h ^ u32(b)) *% 16777619 // FNV-1a: the multiply wraps on purpose
    }
    return h
}
```

For arithmetic that reports overflow as a value instead (`AddOverflow`, `CheckedPow` and others), see [`std.math`](/reference/std/math#checked-and-wrapping-arithmetic).

## Bit operations

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    x: u8 = 0b1100
    y: u8 = 0b1010
    and := x & y
    or := x | y
    xor := x ^ y
    not := ~x
    try sys.Print("{and} {or} {xor} {not}\n", (and, or, xor, not))
    big: u8 = 250
    left := big << 4 // bits shifted out are dropped
    right := big >> 4
    neg: i8 = -128
    sr := neg >> 1 // >> on a signed type copies the sign bit
    try sys.Print("{left} {right} {sr}\n", (left, right, sr))
}
```

```text
8 14 6 243
160 15 -64
```

- `& | ^ ~` work on integers only. Not is `~x`; `^x` is an error.
- A shift amount below 0, or not below the bit width, panics.
- `>>` is arithmetic on signed types and logical on unsigned ones.

## Comparison

`== !=` test equality and `< <= > >=` order. Each gives a `bool`.

| Operation | Works on |
|---|---|
| `==`, `!=` | Numbers, `bool`, `char`, enums without payloads, and `str` by content |
| `<`, `<=`, `>`, `>=` | Numbers, `char`, and `str` by bytes |

Structs, records, arrays, slices and optionals have no `==` (E0308). A type that implements the `Eq` interface compares with `a.Eq(&b)`, and one that implements `Ord` orders with `a.Less(&b)` ([methods and interfaces](./methods-and-interfaces)). Test an optional with `if x := o` ([optionals](./errors#optionals)).

## Logical operators

`&&`, `||` and `!` take `bool` operands. `&&` and `||` evaluate their right side only when the left does not decide the result:

```mk
// verdict: ok
Loud(sys: &System, name: str): bool {
    sys.Print("{name} ", (name)) else {}
    return true
}

export Main(sys: &System): WriteError!void {
    a := false && Loud(sys, "and") // not evaluated
    b := true || Loud(sys, "or") // not evaluated
    c := true && Loud(sys, "evaluated")
    try sys.Print("{a} {b} {c}\n", (a, b, c))
}
```

```text
evaluated false true true
```

An integer is not a `bool`: `n && ok` and `!n` are errors. Write `n != 0`.

## Precedence

Tightest first. Operators in one row group left to right: `10 - 3 - 2` is `(10 - 3) - 2`.

| Precedence | Operators |
|---|---|
| Postfix | `a.b`, `a[i]`, `a[i..j]`, `f(x)` |
| Prefix | `- ! ~ & mut * try` |
| Multiply | `* / % *% << >> &` |
| Add | `+ - +% -% \| ^` |
| Compare, no chaining | `== != < <= > >=` |
| Logic | `&&`, then `\|\|` |
| Fallback | `e else { }`, `e else x { }` |

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    a := 1 + 2 * 3 << 1 // * and << bind alike, left to right: 1 + ((2 * 3) << 1)
    flags: u8 = 0b0110
    set := flags & 0b0100 != 0 // & binds tighter than !=, unlike C
    in_range := 0 < a && a < 20 // comparisons do not chain
    try sys.Print("{a} {set} {in_range}\n", (a, set, in_range))
}
```

```text
13 true true
```

Two things to watch for:

- A lone name in parentheses, `(x)`, is a one-field record `(x: x)`, not a grouping. Group expressions, not single names.
- `try` applies to the whole postfix chain after it: `try Load(x).Len()` tries `Len()`. Write `(try Load(x)).Len()`.

```mk
// verdict: reject E0101
Between(a: int, b: int, c: int): bool {
    return a < b < c // <- E0101
}
```

## References

`&x` borrows `x` shared and gives a `&T`. `mut x` borrows it exclusively and gives a `mut T`. `*r` is the value behind a reference, though fields and methods reach through references without it: `r.n`, `r.Get()`. Borrowing has rules of its own, in [memory and ownership](./memory#borrowing).

## Next

[Strings and formatting](./strings).
