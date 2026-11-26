# std.big

`std.big` has arbitrary-precision integers: `big.Int`, with arithmetic, division, powers modulo a number, greatest common divisors, square roots, and conversion to and from text in any base from 2 to 36. Reach for it when a value can outgrow `i64` or `u64`: exact factorials and combinatorics, big literals in a parser, number theory. Every result is written into an arena you pass, so there is no heap form and nothing to free one value at a time. It is not constant time, so it is not for secret data: [`std.crypto`](crypto.md) has its own constant-time arithmetic, and no other std module imports `std.big`.

Read the [library guide](README.md) first.

```mk
// verdict: parse
import std.big
```

## Contents

- [The main pieces](#the-main-pieces)
- [Values and arenas](#values-and-arenas)
- [Division and signs](#division-and-signs)
- [Text](#text)
- [Errors](#errors)
- [Memory and cost](#memory-and-cost)
- [Examples](#examples): [compute past i64](#compute-past-i64), [parse untrusted text](#parse-untrusted-text), [number theory](#number-theory), [keep one result](#keep-one-result)
- [API](#api)

## The main pieces

| Task | Use |
|---|---|
| Make a value | `FromI64`, `FromBytes` (big-endian), `FromWords` (a view of your words), `Parse` (text) |
| Arithmetic | `Int.Add`, `Int.Sub`, `Int.Mul`, `Int.Neg`, `Int.Shl`, `Int.Shr` |
| Division | `Int.DivMod` (truncated), `Int.Mod` (Euclidean) |
| Number theory | `Int.ModPow`, `Int.ModInverse`, `Int.Gcd`, `Int.Sqrt` |
| Compare | `Int.Cmp`, `Int.Sign`, and `x.Eq(&y)` and `x.Less(&y)` from `Eq` and `Ord`, which `Map`, `Set` and sorting use. The `==` and `<` operators do not apply to `Int` |
| Inspect | `Int.BitLen`, `Int.Bit`, `Int.ToI64`, `Int.ToF64` |
| Write out | `Int.Text` (any base), `{}` through `Format`, `Int.Bytes`, `Int.Words` |
| Keep a value past its arena | `Int.Clone` into another arena, or `Int.Words` then `FromWords` |

## Values and arenas

An `Int` is a sign and a magnitude: little-endian `u64` words with no zero top word. Every value has exactly one form, so zero is the empty magnitude and is never negative, and two equal values have equal words. `Int` implements `Eq`, `Ord` and `Hash`, so it works as a `Map` key.

An `Int` is an immutable view of words in an arena. Every operation that makes a value takes an `a: &Arena` and writes the new words there; the signature shows this as `from a`. The operands are never changed, so a loop that updates a variable leaves the old values in the arena until it is reset.

- An `Int` is unique: `y := x`, or passing `x` by value (to `Map.Set`, say), moves it. Pass `&x` to borrow it, as the examples do with `&n` and `(f: &f)`, and `Clone` it for a second copy.
- To bound the memory of a long computation, run it in a scratch arena (`a.Sub()`). Then `Clone` the one result you keep into the caller's arena, as the [keep one result](#keep-one-result) example does.
- To keep a value after every arena is gone, copy `Words()` into storage you own, such as a `HeapList[u64]`. Make the value again with `FromWords(words, negative)`, which views the words without copying.
- `FromBytes` and `Bytes` convert to and from big-endian bytes of the magnitude; `Bytes` drops the sign.

## Division and signs

- `DivMod` returns the truncated quotient and remainder: `q` rounds toward zero, and `r = x - q * y` has `x`'s sign, as `/` and `%` do on the fixed integer types.
- `Mod` is the Euclidean remainder: `0 <= r < |m|` whatever the signs. `ModPow` and `ModInverse` also return values in that range.
- `Shr` rounds toward negative infinity, as `>>` does on the fixed signed types, so `-1 >> n` is `-1`. `Bit(i)` reads two's complement bits, so a negative value has every bit above its length set.
- Division by zero panics, in `DivMod`, `Mod`, `ModPow` and `ModInverse`. So do a negative shift, a negative exponent to `ModPow`, `Sqrt` of a negative value, and a base outside 2 to 36 to `Text`.

## Text

`Parse(a, s, base, max_digits)` reads an optional `+` or `-`, then one or more digits in `base`, 2 to 36. Letters in either case are the digit values 10 to 35. Nothing else is accepted: no spaces, no `_`, no `0x` prefix.

`Parse` never panics on its input. `max_digits` bounds what text from a peer or a file can cost: a longer input fails with `TooLarge`. The check uses the text's length, before any digit is read or any memory is taken.

`Int.Text(a, base)` writes any value in any base, in lower case, with a `-` for a negative value and no prefix. Printing with `{}` goes through `Format`, which has no allocator. It writes decimal for values of up to 512 words (about 9,800 digits), and hexadecimal with a `0x` prefix above that. Call `Text` for decimal of any size.

## Errors

| Member | Meaning |
|---|---|
| `ParseError` | `Parse`'s text is not an integer in the base, or the base is outside 2 to 36 (from the prelude) |
| `TooLarge` | `Parse`'s text has more than `max_digits` digits |
| `OutOfMemory` | The arena cannot give the words (from the prelude) |

Every function that writes a value fails only with `OutOfMemory`; `Parse` adds the other two. `big.Error` is open, so a `match` on it outside std keeps a `_` arm.

## Memory and cost

Each function's doc states its cost in words and what it allocates in `a`. In short, for operands of n words:

- `Add`, `Sub`, `Neg` and the shifts are O(n). `Cmp`, `Sign`, `BitLen` and `ToI64` allocate nothing.
- `Mul` is O(n^2) below 40 words, Karatsuba below 300 words and Toom-3 above, and squares when both operands hold the same value.
- `DivMod` and `Mod` are O(m (n - m + 1)) for an m-word divisor below 60 words, and recursive division above.
- `ModPow` is O(e.BitLen() n^2) word operations for an n-word modulus, so a caller bounds the exponent and modulus to bound the time.
- `Parse` and `Text` are O(n) in a base that is a power of two. In other bases they are quadratic for short values and divide and conquer over `Mul` and `DivMod` for long ones.
- Some functions take part of their scratch from a sub-arena of `a` and free it before they return. They are `DivMod` and `Mod` from 60 divisor words, `ModPow` when the odd part of its modulus has 60 words or more, `Sqrt` from 5 words, and `Parse` above 20,000 digits and `Text` above 30 words in a base that is not a power of two. The reduction of a long `x` in `ModPow`, `Gcd` and `ModInverse` does the same. `Mul` keeps all its scratch in `a`. Each function's doc gives the number of words.

Every operation's stack is bounded. Each divide-and-conquer recursion (the products and squares, recursive division, `Text`, `Parse` and `Sqrt`) takes a `u8` depth that falls by one per level, so [`mk query stack`](../tools.md#52-depth-bounded-recursion) gives every function a deepest call chain; the arena's own calls through `fn` values aside, it reports no other unbounded call.

Nothing in `std.big` is constant time: running time and memory follow the operands' sizes and, for division and `ModPow`, their values.

## Examples

Each program runs as written with `mk run`.

### Compute past i64

```mk
// verdict: ok
import std.big

export Main(sys: &System): OutOfMemory|WriteError!void {
    var f := try big.FromI64(sys.arena, 1)
    for i in 1..31 {
        n := try big.FromI64(sys.arena, i64(i))
        f = try f.Mul(sys.arena, &n) // each step writes a new value in sys.arena
    }
    try sys.Print("30! = {f}\n", (f: &f)) // Format writes decimal
    try sys.Print("bits: {b}\n", (b: f.BitLen()))
    hex := try f.Text(sys.arena, 16)
    try sys.Print("hex:  {hex}\n", (hex))
    try sys.Print("fits in i64: {fits}\n", (fits: f.ToI64().IsSome()))
}
```

```text
$ mk run factorial.mk
30! = 265252859812191058636308480000000
bits: 108
hex:  d13f6370f96865df5dd54000000
fits in i64: false
```

### Parse untrusted text

```mk
// verdict: ok
import std.big

Show(sys: &System, s: str, base: int): OutOfMemory|WriteError!void {
    x := big.Parse(sys.arena, s, base, 40) else e { // at most 40 digits
        why := match e {
            ParseError { "not a number" }
            big.TooLarge { "too many digits" }
            OutOfMemory { fail OutOfMemory }
            _ { "other" } // big.Error is open
        }
        try sys.Print("{s}: {why}\n", (s, why))
        return
    }
    try sys.Print("{s} = {x}\n", (s, x: &x))
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    try Show(sys, "-ff", 16)
    try Show(sys, "1010", 2)
    try Show(sys, "0x10", 16) // no prefix allowed
    try Show(sys, "12 34", 10) // no spaces either
    try Show(sys, "1234567890123456789012345678901234567890", 10)
    try Show(sys, "12345678901234567890123456789012345678901", 10)
}
```

```text
$ mk run parse.mk
-ff = -255
1010 = 10
0x10: not a number
12 34: not a number
1234567890123456789012345678901234567890 = 1234567890123456789012345678901234567890
12345678901234567890123456789012345678901: too many digits
```

- The digit limit counts the digits, not the sign.

### Number theory

```mk
// verdict: ok
import std.big

export Main(sys: &System): OutOfMemory|WriteError!void {
    a := sys.arena
    x := try big.FromI64(a, -7)
    y := try big.FromI64(a, 3)
    (q, r) := try x.DivMod(a, &y) // truncates toward zero, as / and % do
    m := try x.Mod(a, &y) // Euclidean: never negative
    try sys.Print("-7 divmod 3 = {q}, {r}; -7 mod 3 = {m}\n", (q: &q, r: &r, m: &m))

    one := try big.FromI64(a, 1)
    p := try (try one.Shl(a, 127)).Sub(a, &one) // 2^127 - 1, a prime
    base := try big.FromI64(a, 5)
    pm1 := try p.Sub(a, &one)
    fermat := try base.ModPow(a, &pm1, &p) // 5^(p-1) mod p is 1 for a prime p
    try sys.Print("5^(p-1) mod p = {v}\n", (v: &fermat))

    g := try (try big.FromI64(a, 84)).Gcd(a, &(try big.FromI64(a, 36)))
    try sys.Print("gcd(84, 36) = {g}\n", (g: &g))
    seven := try big.FromI64(a, 7)
    inv := try seven.ModInverse(a, &p) else {
        return
    }
    check := try (try seven.Mul(a, &inv)).Mod(a, &p)
    try sys.Print("7 * 7^-1 mod p = {c}\n", (c: &check))
    s := try p.Sqrt(a)
    try sys.Print("floor(sqrt(p)) = {s}\n", (s: &s))
}
```

```text
$ mk run theory.mk
-7 divmod 3 = -2, -1; -7 mod 3 = 2
5^(p-1) mod p = 1
gcd(84, 36) = 12
7 * 7^-1 mod p = 1
floor(sqrt(p)) = 13043817825332782212
```

- `ModInverse` returns `none` when the value and the modulus share a factor, so its result is handled with `else`.

### Keep one result

```mk
// verdict: ok
import std.big

// Fib returns the nth Fibonacci number in a. The intermediate values
// live in a scratch arena that is freed before Fib returns.
Fib(a: &Arena, n: int): OutOfMemory!big.Int from a {
    var scratch := try a.Sub()
    defer scratch.Free()
    var x := try big.FromI64(&scratch, 0)
    var y := try big.FromI64(&scratch, 1)
    for _ in 0..n {
        z := try x.Add(&scratch, &y)
        x = y
        y = z
    }
    return try x.Clone(a) // copy the one value the caller needs into a
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    f := try Fib(sys.arena, 200)
    try sys.Print("fib(200) = {f}\n", (f: &f))
    try sys.Print("{w} words\n", (w: f.Words().Len()))
}
```

```text
$ mk run fib.mk
fib(200) = 280571172992510140037611932413038677189525
3 words
```

- The loop makes 200 values, but only the last one reaches `a`; the rest go with the scratch arena.

## API

<!-- api:begin -->
Stability: Stable.

### `Error`

```
export open type Error = ParseError | TooLarge | OutOfMemory
```

Error is every failure of std.big: ParseError for text that is not an
integer in the base, TooLarge past Parse's max_digits, and OutOfMemory
when an arena cannot give the words. It is open, so a match on it
outside std keeps a _ arm.

### `FromBytes`

```
export FromBytes(a: &Arena, b: []u8): OutOfMemory!Int from a
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

The non-negative Int whose big-endian bytes are b, leading zero bytes
allowed. Fails with OutOfMemory when a cannot give the words. O(n) in
b.Len(); allocates ceil(b.Len() / 8) words in a.

### `FromI64`

```
export FromI64(a: &Arena, x: i64): OutOfMemory!Int from a
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

The Int whose value is x. Fails with OutOfMemory when a cannot give one
word. O(1); allocates one word in a (none for zero).

### `FromWords`

```
export FromWords(words: []u64, negative: bool): Int from words
```

- Flows: The result borrows from words.

The Int whose magnitude is words, little-endian, negative when negative
is true and the value is not zero. Views words without copying: zero top
words are left out of the view. Cannot fail. O(number of zero top
words); allocates nothing.

### `Int`

```
export view struct Int
```

Int is an arbitrary-precision signed integer. It is an immutable view of
words in an arena, or of the caller's words for FromWords, and has no
heap form. It is unique: assigning an Int or passing it by value moves
it, so pass &x to borrow it and Clone it to keep a second copy. Every
value has exactly one form, so equal values have equal words. Each
operation takes an arena and writes its result there; the result lives
as long as that arena. To keep a value past its arena, copy it with
Clone into a longer-lived arena, or copy Words out and rebuild it with
FromWords.

Int implements Eq, Ord and Hash: compare with x.Eq(&y) and x.Less(&y),
as the == and < operators do not apply to it. Nothing in std.big is
constant time: running time follows the operands' sizes and, for
division and ModPow, their values, so it is not for secret data.

### `Int.Add`

```
export Int.Add(&self, a: &Arena, y: &Int): OutOfMemory!Int from a
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

x + y. Fails with OutOfMemory when a cannot give the words. O(n);
allocates max(n, m) + 1 words in a.

### `Int.Bit`

```
export Int.Bit(&self, i: int): bool
```

Bit i of x in two's complement, as for the fixed integer types, so a
negative value has every bit above its length set. Panics when i is
negative. O(i / 64) for a negative x, else O(1); allocates nothing.

### `Int.BitLen`

```
export Int.BitLen(&self): int
```

The number of bits of |x|, 0 for zero. O(1); allocates nothing.

### `Int.Bytes`

```
export Int.Bytes(&self, a: &Arena): OutOfMemory![]u8 from a
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

|x| as big-endian bytes with no leading zero byte (empty for zero); the
sign is dropped. Fails with OutOfMemory when a cannot give the bytes.
O(n); allocates ceil(BitLen() / 8) bytes in a.

### `Int.Clone`

```
export Int.Clone(&self, a: &Arena): OutOfMemory!Int from a
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

A copy of the value whose words are in a, so it outlives the arena or
words the original views. Fails with OutOfMemory when a cannot give the
words. O(n); allocates n words in a.

### `Int.Cmp`

```
export Int.Cmp(&self, y: &Int): int
```

-1, 0 or 1 as x is below, equal to or above y. O(n) in the shorter
magnitude when the lengths are equal, else O(1); allocates nothing.

### `Int.DivMod`

```
export Int.DivMod(&self, a: &Arena, y: &Int): OutOfMemory!(q: Int, r: Int) from a
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

The truncated quotient and remainder of x / y: q rounds toward zero, and
r = x - q * y has x's sign, as / and % do on the fixed integer types.
Panics when y is zero. Fails with OutOfMemory when a cannot give the
words.

For n-word x and m-word y below 60 words, it costs O(m * (n - m + 1))
word operations (Knuth's Algorithm D). From 60 words of y it costs
O(M(m) log m) per m words of quotient (Burnikel-Ziegler, M the product's
cost). It allocates about 2n + 2 words in a. From 60 words of y it also
takes O(n log n) scratch words from a sub-arena it frees before
returning.

### `Int.Gcd`

```
export Int.Gcd(&self, a: &Arena, y: &Int): OutOfMemory!Int from a
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

gcd(|x|, |y|), never negative; Gcd of zero and zero is zero. Fails with
OutOfMemory when a cannot give the words.

For operands of n >= m words, in either order, reducing the longer
modulo the shorter costs O(n m) word operations when n > m + 1, or
O(n / m M(m) log m) from 60 words of the shorter, M being the product's
cost. The gcd loop then costs O(m^2). It allocates about 6m words in a
for the loop. The reduction allocates about 2n words in a up to n = 2m;
above that, m words in a and at most 3 (m + max(m, 256)) + 2 in a
sub-arena freed before the gcd loop starts.

### `Int.Mod`

```
export Int.Mod(&self, a: &Arena, m: &Int): OutOfMemory!Int from a
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

The Euclidean remainder of x / m: 0 <= r < |m| whatever the signs.
Panics when m is zero. Fails with OutOfMemory when a cannot give the
words. It costs and allocates as DivMod: O(m * (n - m + 1)) word
operations below 60 words of m, recursive division from there, about
2n + 2 words in a, and from 60 words of m O(n log n) scratch words in a
sub-arena it frees before returning.

### `Int.ModInverse`

```
export Int.ModInverse(&self, a: &Arena, m: &Int): OutOfMemory!?Int from a
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

The inverse of x modulo |m|, in 0 <= r < |m|, or none when x and m share
a factor. Panics when m is zero. Fails with OutOfMemory when a cannot
give the words.

It costs O(x.Len() * n + n^2) word operations for an n-word m, as x is
first reduced modulo m. It allocates about 13n words in a, however long
x is. Reducing an x of more than 2n words holds at most
3 (n + max(n, 256)) + 2 words more, and a recursive division's own, in a
sub-arena freed before the gcd loop starts.

### `Int.ModPow`

```
export Int.ModPow(&self, a: &Arena, e: &Int, m: &Int): OutOfMemory!Int from a
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

x^e mod |m|, in 0 <= r < |m| whatever x's sign. Panics when m is zero or
e is negative; for a negative power, take ModInverse first. Fails with
OutOfMemory when a cannot give the words. Not constant time: the
sequence of operations follows e's bits, so never pass a secret
exponent.

O(x.Len() * n + e.BitLen() * n^2) word operations for an n-word m: x is
first reduced modulo m, then it takes one Montgomery product per bit of
e and one per window. An even m = m1 2^k takes one such power modulo m1
and one modulo 2^k, and joins them by the Chinese remainder theorem. A
caller bounds x, e and m to bound the time.

However long x and e are, it allocates in a about 40n words for an odd
m (a window table of up to 32 values and the scratch) and about 100n for
an even one (both powers, a ModInverse modulo 2^k and the join's
products). Reducing an x of more than 2n words holds at most 3 (n +
max(n, 256)) + 2 words more, and a recursive division's own, in a
sub-arena freed before the power starts.

### `Int.Mul`

```
export Int.Mul(&self, a: &Arena, y: &Int): OutOfMemory!Int from a
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

x * y. Fails with OutOfMemory when a cannot give the words. For
operands of n and m words: O(n * m) below 40 words, O(n^1.585)
(Karatsuba) below 300 and O(n^1.465) (Toom-3) above. When x and y hold
the same value, x.Mul(a, &x) included, it squares, with about half the
word products. It allocates n + m words for the product and, from 40
words, at most 10 min(n, m) scratch words in a.

### `Int.Neg`

```
export Int.Neg(&self, a: &Arena): OutOfMemory!Int from a
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

-x. Fails with OutOfMemory when a cannot give the words. O(n); allocates
n words in a.

### `Int.Shl`

```
export Int.Shl(&self, a: &Arena, n: int): OutOfMemory!Int from a
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

x * 2^n. Panics when n is negative. Fails with OutOfMemory when a cannot
give the words. O(len + n / 64); allocates len + n / 64 + 1 words in a.

### `Int.Shr`

```
export Int.Shr(&self, a: &Arena, n: int): OutOfMemory!Int from a
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

x / 2^n rounded toward negative infinity, as >> is on the fixed signed
types: -1 >> n is -1. Panics when n is negative. Fails with OutOfMemory
when a cannot give the words. O(len); allocates len - n / 64 words in a.

### `Int.Sign`

```
export Int.Sign(&self): int
```

-1, 0 or 1 as x is below, equal to or above zero. O(1); allocates
nothing.

### `Int.Sqrt`

```
export Int.Sqrt(&self, a: &Arena): OutOfMemory!Int from a
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

floor(sqrt(x)). Panics when x is negative. Fails with OutOfMemory when a
cannot give the words.

For n-word x below about 240 words it costs O(n^2) word operations.
Above, it costs O(M(n) log n), M being the product's cost (Zimmermann's
Karatsuba square root over recursive division). For x of up to 4 words
it allocates at most 23 words in a. From 5 words it allocates the root's
n/2 + 1 words in a, and about 14n scratch words plus each division's own
in a sub-arena it frees before returning.

### `Int.Sub`

```
export Int.Sub(&self, a: &Arena, y: &Int): OutOfMemory!Int from a
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

x - y. Fails with OutOfMemory when a cannot give the words. O(n);
allocates max(n, m) + 1 words in a.

### `Int.Text`

```
export Int.Text(&self, a: &Arena, base: int): OutOfMemory!str from a
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

Writes x as text in base, 2 to 36: lower-case digits, a - when x is
negative, no prefix, and "0" for zero. It writes a value of any size.
Panics when base is outside 2 to 36. Fails with OutOfMemory when a
cannot give the words.

O(n) for a base that is a power of two, else O(M(n) log^2 n) word
operations for n words, M being Karatsuba's cost: above 30 words it
divides and conquers over DivMod's divisions, below it divides by one
word per run of digits. A caller holding untrusted input bounds n. It
allocates the text, at most n * 64 / floor(log2 base) + 2 bytes, in a,
with n scratch words in a up to 30 words and, above, O(n log n) scratch
words in a sub-arena that it frees before returning.

### `Int.ToF64`

```
export Int.ToF64(&self): f64
```

x rounded to the nearest f64, ties to even; past f64's range, an
infinity of x's sign. O(n) for the sticky bit; allocates nothing.

### `Int.ToI64`

```
export Int.ToI64(&self): ?i64
```

x as an i64, or none when it does not fit. O(1); allocates nothing.

### `Int.Words`

```
export Int.Words(&self): []u64 from self
```

- Flows: The result borrows from self.

The magnitude, little-endian u64 words with no zero top word (empty for
zero); the sign is Sign's. To keep a value after its arena is reset,
copy these words into storage you own, such as a HeapList[u64], and make
the value again with FromWords. O(1); allocates nothing.

### `Parse`

```
export Parse(a: &Arena, s: str, base: int, max_digits: int): Error!Int from a
```

- Flows: The result borrows from a.
- Errors: Error (TooLarge, OutOfMemory, ParseError)

Parses the integer s spells in base, 2 to 36. The text is an optional +
or -, then one or more digits, with letters in either case for the
digits 10 to 35. Nothing else is allowed: no space, no _ and no prefix
such as 0x.

Errors, checked in this order: ParseError when base is outside 2 to 36
or there are no digits; TooLarge when s has more than max_digits digits,
decided from its length before any digit is read or any word allocated;
ParseError for a character that is not a digit in base; OutOfMemory when
a cannot give the words. Never panics on input, so max_digits bounds the
time and memory untrusted text can cost.

Cost for d digits and n = d log2(base) / 64 words: O(d) for a base that
is a power of two, else O(d^2) up to 20,000 digits and O(M(n) log n) word
operations above, M being the product's cost (Karatsuba, Toom-3). It
allocates n + 2 words in a, and above 20,000 digits at most about 14n
scratch words (the powers of base it multiplies by, and one product's
scratch) in a sub-arena of a that it frees before returning. Its
recursion is at most log2(n) levels deep.

### `TooLarge`

```
export error TooLarge
```

Parse's text has more digits than its max_digits allows.

<!-- api:end -->
