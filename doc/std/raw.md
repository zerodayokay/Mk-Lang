# std.raw

`std.raw` is the lowest layer of Mk: raw pointers, reads and writes through them, word-sized atomics, bit counts, float encodings, carry chains and the CPU's crypto instructions. The standard library is written with it. Reach for it when you write a driver, an allocator, an interpreter or a binding to C, and the safe modules cannot say what you need. For memory shared between tasks, use `std.atomic`; for device registers, use `std.mmio`; for slices and strings, use the language itself.

Read [unsafe code](../reference.md#14-unsafe-and-c) and [raw pointers](../reference.md#raw-pointers) in the reference first.

```mk
// verdict: parse
import std.raw
```

## Contents

- [The main pieces](#the-main-pieces)
- [Who may import it](#who-may-import-it)
- [Safe and unsafe functions](#safe-and-unsafe-functions)
- [Atomics and orders](#atomics-and-orders)
- [CPU instructions](#cpu-instructions)
- [Memory and cost](#memory-and-cost)
- [Examples](#examples): [count bits and read float encodings](#count-bits-and-read-float-encodings), [read through a pointer](#read-through-a-pointer), [use an atomic word](#use-an-atomic-word), [add multi-word numbers](#add-multi-word-numbers), [what the compiler refuses](#what-the-compiler-refuses)
- [API](#api)

## The main pieces

| Task | Use |
|---|---|
| Take an address | `Of(r)`, `OfMut(r)`, `Data(s)`, `DataMut(s)`, `StrData(s)` |
| Make or test a pointer | `Null[T]()`, `IsNull`, `Same`, `Offset(p, n)`, `PtrCast[T, U](p)`, `Addr(p)`, `FromAddr[T](a)` |
| Read and write through a pointer | `Read`, `ReadUnaligned`, `Write` |
| Copy, fill and compare memory | `Copy`, `Move`, `Fill`, `Compare`; `AtomicCopy`, `AtomicFill` for memory other threads read |
| View memory as a slice or a string | `Slice(p, n)`, `SliceMut`, `Str(p, n)` |
| End a value by hand | `Forget(x)`, `Release(x, a)`, `IsLinear[T]()`, `HasFree[T]()` |
| A word shared between threads | `Atomic[T]` with `Load`, `Store`, `Add`, `Swap`, `Cas` and the ordered forms; `Fence`, `FenceOrd` |
| Count and move bits | `Ctz64`, `Clz64`, `Popcount64`, `RotL64` |
| Wide arithmetic | `Mul128`, `AddCarry64`, `SubBorrow64` |
| Float encodings | `F64Bits`, `F64From`, `F32Bits`, `F32From`, `SqrtF64` |
| Choose without a branch | `Select(c, a, b)`, a conditional move for an integer, bool, pointer or record (a float may still branch on x86_64) |
| Call code at an address | `FnFromAddr[F](addr)` |
| Panic with a code | `Fail(code, msg)` |
| AES and SHA instructions | `AesRound`, `AesLastRound`, `Clmul64`, `Sha256Rounds4`, `Sha256Schedule4`, `Sha1Rounds4`, `Sha1NextE`, `Sha1Schedule4` |

A raw pointer `*T` is an address and nothing more: the compiler tracks no loan through it, and nothing stops it from dangling. Every function here is built into the compiler and compiles inline.

A raw pointer has no arithmetic: `p + 1` is E0301. Move a pointer by `n` elements with `Offset(p, n)`.

`*void` is C's `void *`, an address with no element type. Make one with `void` as the type argument: `PtrCast[T, void](p)` from a typed pointer, `Null[void]()`, or `FromAddr[void](a)`; `PtrCast[void, T](p)` gives the typed pointer back. `Addr`, `IsNull` and `Same` take a `*void` as they take any pointer. A `*void` has no element, so its element size is zero: cast it to a typed pointer before you read, write or `Offset` through it.

## Who may import it

`std.raw` is internal to the standard library. Outside std, only a module that the package lists in its manifest's `unsafe_modules` may import it, on every target, freestanding included. Any other import is E0202 (`reason: std-internal`). See [grants for unsafe code](../packages.md#11-grants-for-unsafe-code).

```mk
// verdict: parse
const Package = pkg.Package(
    name: "ring",
    mk: "0.1",
    bins: [pkg.Bin(name: "ring", root: "src", subsystem: "console")],
    unsafe_modules: ["src"], // src may import std.raw and write unsafe blocks
)
```

## Safe and unsafe functions

Taking an address, making a null pointer, testing or comparing pointers, fences, bit counts, wide arithmetic, the float encodings, `Select`, `IsLinear`, `Fail` and every method of `Atomic` are safe, and so are the AES and SHA functions in a function that has [their CPU feature](#cpu-instructions). The rest need an `unsafe "reason" { }` block: moving or casting a pointer, making one from an integer, reading, writing, copying or viewing memory, and ending a value by hand. Elsewhere the call is E1101. The table is the full list.

| Safe | Needs `unsafe` |
|---|---|
| `Of`, `OfMut`, `Null`, `IsNull`, `Same`, `Data`, `DataMut`, `StrData`, `Addr` | `Offset`, `PtrCast`, `FromAddr`, `FnFromAddr` |
| `IsLinear`, `Fail` | `Read`, `ReadUnaligned`, `Write` |
| `Fence`, `FenceOrd`, every method of `Atomic` | `Copy`, `Move`, `Fill`, `Compare`, `AtomicCopy`, `AtomicFill` |
| `Ctz64`, `Clz64`, `Popcount64`, `RotL64`, `Mul128` | `Slice`, `SliceMut`, `Str` |
| `AddCarry64`, `SubBorrow64` | `Forget`, `Release`, `HasFree` |
| `F64Bits`, `F64From`, `F32Bits`, `F32From`, `SqrtF64` | |
| `Select` | |
| The AES and SHA functions, [with their CPU feature](#cpu-instructions) | |

What the caller answers for inside the block:

- `Read` and `Write` need a pointer aligned to `AlignOf[T]()`. `ReadUnaligned` takes any alignment, for an integer `T` only (E0701 otherwise).
- `Write` overwrites the old value without releasing it. Release it first, or use it only on memory that holds no value.
- `Copy` needs ranges that do not overlap; `Move` allows overlap. Both count elements of `T`, not bytes. `Fill` and `Compare` count bytes.
- `Slice`, `SliceMut` and `Str` make a view the compiler treats as valid. The memory must stay valid while the view is in use, and `Str`'s bytes must be UTF-8.
- `Forget` ends a value without releasing what it owns, a linear value too. `Release(x, a)` calls `T`'s `Free` with `a` when `T` implements `Free`, and forgets `x` otherwise.
- `FnFromAddr` makes a `fn` value from a code address, such as a `std.vm` code region's entry. It cannot run at compile time.

`Fail(code, msg)` panics with `msg` and the panic code you give; [`std.runtime`](runtime.md#panics) lists the codes.

## Atomics and orders

`Atomic[T]` is a word that tasks on any thread read and write whole. `T` is an integer type of 1, 2, 4 or 8 bytes or a raw pointer; any other `T` is E0701. `Add` and the ordered arithmetic need an integer `T`. A global `var` of an `Atomic` is `Share`, so its value can change behind `&`.

- `Load`, `Store`, `Add` (wrapping), `Swap` and `Cas(old, new): bool` are sequentially consistent. `New(v)` makes an atomic holding `v`.
- The ordered forms `LoadOrd`, `StoreOrd`, `SwapOrd`, `AddOrd`, `SubOrd`, `AndOrd`, `OrOrd`, `XorOrd` and `CasOrd(old, new, s, f, weak): (ok, value)` take a constant order. `FenceOrd(o)` is a fence with an order; `Fence()` is a sequentially consistent one.
- An order is `std.atomic`'s `Order` value, as a number:

| Value | Order | Allowed on |
|---|---|---|
| 0 | relaxed | loads, stores, read-modify-writes, `CasOrd`'s failure order |
| 1 | acquire | loads, read-modify-writes, fences, `CasOrd`'s failure order |
| 2 | release | stores, read-modify-writes, fences |
| 3 | acquire-release | read-modify-writes, fences |
| 4 | sequentially consistent | everything |
| 5 | unordered | loads and stores only |

An order an operation does not allow is E0706 at the call, never a crash in the compiler. `std.atomic` wraps all of this with named orders; prefer it outside low-level code. The rules for `unsafe` code and the memory model are in [the memory model, R19](../memory.md#r19-unsafe).

`AtomicCopy` and `AtomicFill` copy and fill element by element, each element a relaxed atomic access, for memory that another thread may read at the same time. The element is a plain type of 1, 2, 4 or 8 bytes, aligned to its size (E0701 otherwise). `AtomicCopy` allows overlap, as `Move` does.

## CPU instructions

`AesRound`, `AesLastRound` and `Clmul64` are FIPS 197's AES rounds and the carry-less multiply that GCM uses. `Sha256Rounds4`, `Sha256Schedule4`, `Sha1Rounds4`, `Sha1NextE` and `Sha1Schedule4` are four rounds and four schedule words of FIPS 180-4's SHA-256 and SHA-1.

- An AES state or key is 16 bytes in FIPS 197's input order: byte r + 4c is row r of column c.
- A SHA-1 `[4]u32` has its oldest word in lane 3: a state holds d, c, b, a from lane 0, and a word group W[t + 3] to W[t]. A SHA-256 state holds f, e, b, a, then h, g, d, c.

- Each is safe in a function whose features include its CPU feature (`aes`, `pclmul` with `aes`, or `sha`), set with a [`cpu` clause](../reference.md#cpu-features). Elsewhere the call needs `unsafe`, behind a `simd.Cpu()` test (E1101).
- On x86_64 the calling function's features must also include `sse2`, inside `unsafe` too (E1106). Hosted x86_64 targets always have it. On `x86_64-freestanding` the function's `cpu` clause must name it, whatever the bin's `cpu`.
- `Sha1Rounds4`'s round group is a constant from 0 to 3 (E0706 otherwise).
- `std.crypto` and `std.hash` use them through `std.simd`'s kernels, which test `simd.Cpu()` and fall back to portable code.

`AddCarry64(x, y, c)` and `SubBorrow64(x, y, b)` are the steps of multi-word addition and subtraction. A chain of calls compiles to one `adc` or `sbb` chain on x86_64, with no branches, in every build, while its results feed arithmetic. A branch on the last carry or borrow may compile as a comparison of the operands with a jump per word, and a select on it as a `cmov` or a branch, as the compiler chooses. So constant-time code that branches on a carry computes it by the formula of Go's `math/bits`, and code that selects on one is checked against the machine code it compiles to.

## Memory and cost

Nothing in `std.raw` allocates. Every function is O(1) except `Copy`, `Move`, `Fill`, `Compare`, `AtomicCopy` and `AtomicFill`, which are O(n) in the count they are given. `IsLinear` and `HasFree` are constants for each `T`, and `Release` picks `Free` or `Forget` at compile time.

## Examples

Each program below runs as written with `mk run` in a package whose `pkg.mk` lists its module in `unsafe_modules`, as above.

### Count bits and read float encodings

```mk
// verdict: ok
import std.raw

export Main(sys: &System): WriteError!void {
    x: u64 = 0b1011_0000
    try sys.Print("ctz {a}, clz {b}, ones {c}\n", (a: raw.Ctz64(x), b: raw.Clz64(x), c: raw.Popcount64(x)))
    try sys.Print("rotl {r:x}\n", (r: raw.RotL64(0x8000_0000_0000_0001, 4)))
    bits := raw.F64Bits(-1.5)
    try sys.Print("-1.5 is {bits:x}, back {f}\n", (bits, f: raw.F64From(bits)))
    p := raw.Mul128(0xFFFF_FFFF_FFFF_FFFF, 3) // the full product: 3 * (2^64 - 1)
    try sys.Print("hi {hi} lo {lo:x}\n", (hi: p.hi, lo: p.lo))
}
```

```text
$ mk run .
ctz 4, clz 56, ones 3
rotl 18
-1.5 is bff8000000000000, back -1.5
hi 2 lo fffffffffffffffd
```

- All of these are safe: they compute a number and touch no memory.

### Read through a pointer

```mk
// verdict: ok
import std.raw

// Le32 reads a little-endian u32 at offset off of b, at any alignment.
Le32(b: []u8, off: int): u32 {
    Assert(off >= 0 && off + 4 <= b.Len()) // the bounds the raw read cannot check
    unsafe "off + 4 <= b.Len(), and u32 is an integer, read at alignment 1" {
        p := raw.Offset(raw.Data(b), off)
        return raw.ReadUnaligned(raw.PtrCast[u8, u32](p)) // x86_64 is little-endian
    }
}

export Main(sys: &System): WriteError!void {
    packet: [9]u8 = [0xFF, 0x01, 0x00, 0x00, 0x00, 0x2A, 0x00, 0x00, 0x00]
    try sys.Print("{a} {b}\n", (a: Le32(packet[..], 1), b: Le32(packet[..], 5)))
    var dst: [4]u8 = [0; 4]
    unsafe "dst has room for 4 bytes, and packet[1..5] does not overlap it" {
        raw.Copy(raw.DataMut(mut dst[..]), raw.Offset(raw.Data(packet[..]), 1), 4)
    }
    try sys.Print("{d}\n", (d: dst[0]))
}
```

```text
$ mk run .
1 42
1
```

- `Data` is safe: taking an address checks nothing. `Offset`, `PtrCast`, `ReadUnaligned` and `Copy` are not, so they sit in `unsafe` blocks whose reasons say why the access is in bounds.
- `Read` would need a 4-byte-aligned pointer; a byte array's offset 1 need not be.

### Use an atomic word

```mk
// verdict: ok
import std.raw

// A global var of an atomic can change behind a shared reference.
var Next: raw.Atomic[u64] = raw.Atomic[u64].New(1)

// Id hands out ids, one at a time, from any task.
Id(): u64 {
    return Next.Add(1) // gives the old value
}

// SetMax raises max to x unless it is already higher.
SetMax(max: &raw.Atomic[u64], x: u64) {
    var cur := max.LoadOrd(0) // relaxed
    for cur < x {
        r := max.CasOrd(cur, x, 4, 0, true) // weak: may fail spuriously, so loop
        if r.ok {
            return
        }
        cur = r.value // what the word held instead
    }
}

export Main(sys: &System): WriteError!void {
    a := Id()
    b := Id()
    max := raw.Atomic[u64].New(0)
    xs: [3]u64 = [3, 9, 4]
    for x in xs {
        SetMax(&max, x)
    }
    try sys.Print("ids {a} {b}, max {m}\n", (a, b, m: max.Load()))
}
```

```text
$ mk run .
ids 1 2, max 9
```

- Every method of `Atomic` is safe; only naming the type needs the `unsafe_modules` grant.
- `CasOrd`'s success order is 4 (sequentially consistent) and its failure order 0 (relaxed). A failure order of 2 would be E0706.

### Add multi-word numbers

```mk
// verdict: ok
import std.raw

// Add256 adds two 256-bit numbers held as four 64-bit limbs, lowest first.
Add256(x: [4]u64, y: [4]u64): (sum: [4]u64, carry: u64) {
    var out: [4]u64 = [0; 4]
    var c: u64 = 0
    for i in 0..4 {
        r := raw.AddCarry64(x[i], y[i], c) // one adc each, no branch
        out[i] = r.sum
        c = r.carry
    }
    return (sum: out, carry: c)
}

export Main(sys: &System): WriteError!void {
    max: u64 = 0xFFFF_FFFF_FFFF_FFFF
    r := Add256([max, max, 0, 0], [1, 0, 0, 0])
    try sys.Print("{a} {b} {c} {d}, carry {k}\n", (a: r.sum[0], b: r.sum[1], c: r.sum[2], d: r.sum[3], k: r.carry))
    d := raw.SubBorrow64(0, 1, 0)
    try sys.Print("0 - 1 = {v:x}, borrow {b}\n", (v: d.diff, b: d.borrow))
}
```

```text
$ mk run .
0 0 1 0, carry 0
0 - 1 = ffffffffffffffff, borrow 1
```

- The loop has no branch on the carry, so its time does not depend on the values: `std.crypto`'s field arithmetic relies on that.

### What the compiler refuses

```mk
// verdict: reject E1101
import std.raw

Peek(p: *u64): u64 {
    return raw.Read(p) // <- E1101
}
```

```mk
// verdict: reject E0706
import std.raw

Get(a: &raw.Atomic[u64]): u64 {
    return a.LoadOrd(2) // <- E0706
}
```

```mk
// verdict: reject E0701
import std.raw

var Level: raw.Atomic[f64] = raw.Atomic[f64].New(0.0) // <- E0701
```

- A read through a raw pointer needs `unsafe`.
- A load cannot have release order (2). Use 0, 1, 4 or 5.
- An atomic holds an integer or a raw pointer. Store a float's bits with `F64Bits` in an `Atomic[u64]`.

## API

<!-- api:begin -->
Stability: Stable.

### `AddCarry64`

```
export AddCarry64(x: u64, y: u64, c: u64): (sum: u64, carry: u64)
```

AddCarry64 is x + y + c: sum is its low 64 bits, and carry is 1 when it
is 2^64 or more, else 0. c is meant to be 0 or 1, the carry of the limb
below. Safe. A chain of calls compiles to one add-with-carry chain
(adc on x86_64), with no branches, in every build. O(1); allocates
nothing.

### `Addr`

```
export Addr[T](p: *T): uint
```

Addr is p's address as an integer. Safe.

### `AesLastRound`

```
export AesLastRound(state: [16]u8, key: [16]u8): [16]u8 cpu "aes"
```

AesLastRound is the last round of FIPS 197's Cipher: SubBytes and
ShiftRows of state, then AddRoundKey with key. It needs the aes
feature as AesRound does. O(1).

### `AesRound`

```
export AesRound(state: [16]u8, key: [16]u8): [16]u8 cpu "aes"
```

AesRound is one round of FIPS 197's Cipher: SubBytes, ShiftRows and
MixColumns of state, then AddRoundKey with key. state and key are 16
bytes in FIPS 197's input order: byte r + 4c is row r of column c.
Safe in a function whose features include aes; elsewhere the call
needs unsafe (E1101) behind a simd.Cpu() test. On x86_64 the
function's features must also include sse2, inside unsafe too
(E1106): hosted targets always have it; on x86_64-freestanding the
function's cpu clause must name it, whatever the bin's cpu. O(1).

### `Atomic`

```
export struct Atomic[T: Plain]
```

Atomic is a word that tasks on any thread read and write whole. T is
an integer type of 1, 2, 4 or 8 bytes or a raw pointer; any other T is
E0701. The methods without an order are sequentially consistent. The
methods whose names end in Ord take a constant order o, std.atomic's
Order value: 0 relaxed, 1 acquire, 2 release, 3 acquire-release, 4
sequentially consistent, 5 unordered (loads and stores only). An order
a method does not allow is E0706 at the call. Every method is safe.

### `Atomic.Add`

```
export Atomic[T].Add(&self, x: T): T
```

- Flows: The result borrows from x. The result borrows what self refers to; self is free after the call.

Add adds x, wrapping, and gives the old value. T must be an integer
(E0701).

### `Atomic.AddOrd`

```
export Atomic[T].AddOrd(&self, x: T, const o: u8): T
    requires o <= 4
```

- Flows: The result borrows from x. The result borrows what self refers to; self is free after the call.

AddOrd adds x, wrapping, with order o (0 to 4) and gives the old
value. T must be an integer.

### `Atomic.AndOrd`

```
export Atomic[T].AndOrd(&self, x: T, const o: u8): T
    requires o <= 4
```

- Flows: The result borrows from x. The result borrows what self refers to; self is free after the call.

AndOrd ands x into the word with order o (0 to 4) and gives the old
value. T must be an integer.

### `Atomic.Cas`

```
export Atomic[T].Cas(&self, old: T, new: T): bool
```

Cas writes new when the word holds old, and reports whether it did.

### `Atomic.CasOrd`

```
export Atomic[T].CasOrd(&self, old: T, new: T, const s: u8, const f: u8, const weak: bool): (ok: bool, value: T)
    requires s <= 4
    requires f == 0 || f == 1 || f == 4
```

- Flows: The result borrows from old and new. The result borrows what self refers to; self is free after the call.

CasOrd writes new when the word holds old, with order s (0 to 4) on
success and f (relaxed, acquire or sequentially consistent) on
failure. ok reports whether it wrote, and value is what the word held.
A weak CasOrd may fail even when the word holds old, so call it in a
loop.

### `Atomic.Load`

```
export Atomic[T].Load(&self): T
```

- Flows: The result borrows what self refers to; self is free after the call.

Load reads the word.

### `Atomic.LoadOrd`

```
export Atomic[T].LoadOrd(&self, const o: u8): T
    requires o == 0 || o == 1 || o == 4 || o == 5
```

- Flows: The result borrows what self refers to; self is free after the call.

LoadOrd reads the word with order o: relaxed, acquire, sequentially
consistent or unordered.

### `Atomic.New`

```
export Atomic[T].New(x: T): Atomic[T]
```

- Flows: The result borrows from x.

New is an Atomic that holds x.

### `Atomic.OrOrd`

```
export Atomic[T].OrOrd(&self, x: T, const o: u8): T
    requires o <= 4
```

- Flows: The result borrows from x. The result borrows what self refers to; self is free after the call.

OrOrd ors x into the word with order o (0 to 4) and gives the old
value. T must be an integer.

### `Atomic.Store`

```
export Atomic[T].Store(&self, x: T)
```

Store writes x.

### `Atomic.StoreOrd`

```
export Atomic[T].StoreOrd(&self, x: T, const o: u8)
    requires o == 0 || o == 2 || o == 4 || o == 5
```

StoreOrd writes x with order o: relaxed, release, sequentially
consistent or unordered.

### `Atomic.SubOrd`

```
export Atomic[T].SubOrd(&self, x: T, const o: u8): T
    requires o <= 4
```

- Flows: The result borrows from x. The result borrows what self refers to; self is free after the call.

SubOrd subtracts x, wrapping, with order o (0 to 4) and gives the old
value. T must be an integer.

### `Atomic.Swap`

```
export Atomic[T].Swap(&self, x: T): T
```

- Flows: The result borrows from x. The result borrows what self refers to; self is free after the call.

Swap writes x and gives the old value.

### `Atomic.SwapOrd`

```
export Atomic[T].SwapOrd(&self, x: T, const o: u8): T
    requires o <= 4
```

- Flows: The result borrows from x. The result borrows what self refers to; self is free after the call.

SwapOrd writes x with order o (0 to 4) and gives the old value.

### `Atomic.XorOrd`

```
export Atomic[T].XorOrd(&self, x: T, const o: u8): T
    requires o <= 4
```

- Flows: The result borrows from x. The result borrows what self refers to; self is free after the call.

XorOrd xors x into the word with order o (0 to 4) and gives the old
value. T must be an integer.

### `AtomicCopy`

```
export AtomicCopy[T](dst: *T, src: *T, n: int)
```

AtomicCopy copies n elements from src to dst, each element read and
written whole, as a relaxed atomic access. T is a plain type of 1, 2, 4
or 8 bytes, aligned to its size (E0701 otherwise). The ranges may
overlap, as Move's may. O(n). Needs unsafe.

### `AtomicFill`

```
export AtomicFill[T](dst: *T, x: T, n: int)
```

AtomicFill writes x to the n elements at dst, each element written
whole, as a relaxed atomic access. T is as for AtomicCopy. O(n). Needs
unsafe.

### `Clmul64`

```
export Clmul64(a: u64, b: u64): (hi: u64, lo: u64) cpu "pclmul, aes"
```

Clmul64 is the 128-bit carry-less product of a and b as polynomials over
GF(2), bit i the coefficient of x^i. It needs the pclmul and aes
features as AesRound needs aes. O(1).

### `Clz64`

```
export Clz64(x: u64): int
```

Clz64 counts x's leading zero bits; 64 for 0. Safe.

### `Compare`

```
export Compare(a: *u8, b: *u8, n: int): int
```

Compare orders the n bytes at a and b as unsigned bytes: below 0 when
a's come first, 0 when they are equal, above 0 otherwise. O(n). Needs
unsafe.

### `Copy`

```
export Copy[T](dst: *T, src: *T, n: int)
```

Copy copies n elements from src to dst; the two ranges must not
overlap. O(n). Needs unsafe.

### `Ctz64`

```
export Ctz64(x: u64): int
```

Ctz64 counts x's trailing zero bits; 64 for 0. Safe.

### `Data`

```
export Data[T](s: []T): *T
```

Data is the address of s's first element. Safe.

### `DataMut`

```
export DataMut[T](s: mut []T): *T
```

DataMut is the address of s's first element, to write through. Safe.

### `F32Bits`

```
export F32Bits(x: f32): u32
```

F32Bits is x's IEEE 754 encoding, NaN payloads included. Safe.

### `F32From`

```
export F32From(x: u32): f32
```

F32From is the f32 whose IEEE 754 encoding is x. Safe.

### `F64Bits`

```
export F64Bits(x: f64): u64
```

F64Bits is x's IEEE 754 encoding, NaN payloads included. Safe.

### `F64From`

```
export F64From(x: u64): f64
```

F64From is the f64 whose IEEE 754 encoding is x. Safe.

### `Fail`

```
export Fail(code: u32, msg: str): never
```

Fail panics with the message msg and the panic code code. Safe.

### `Fence`

```
export Fence()
```

Fence is a sequentially consistent fence. Safe.

### `FenceOrd`

```
export FenceOrd(const o: u8)
    requires o >= 1 && o <= 4
```

FenceOrd is a fence with order o: acquire, release, acquire-release or
sequentially consistent. Safe.

### `Fill`

```
export Fill(dst: *u8, b: u8, n: int)
```

Fill sets n bytes at dst to b. O(n). Needs unsafe.

### `FnFromAddr`

```
export FnFromAddr[F](addr: uint): F
```

FnFromAddr is the function at addr, as the fn type F. addr must be the
address of code with F's signature, such as an entry of a std.vm code
region. Needs unsafe. It cannot run at compile time.

### `Forget`

```
export Forget[T](x: T)
```

Forget ends x, a linear value too, without releasing what it owns.
Needs unsafe.

### `FromAddr`

```
export FromAddr[T](a: uint): *T
```

FromAddr is the address a as a pointer to T, which may be void. Needs
unsafe.

### `HasFree`

```
export HasFree[T](): bool
```

HasFree reports whether T implements Free, as a constant for each T.
Needs unsafe.

### `IsLinear`

```
export IsLinear[T](): bool
```

IsLinear reports whether T is linear, as a constant for each T. Safe.

### `IsNull`

```
export IsNull[T](p: *T): bool
```

IsNull reports whether p is null. Safe.

### `Move`

```
export Move[T](dst: *T, src: *T, n: int)
```

Move copies n elements from src to dst; the ranges may overlap. O(n).
Needs unsafe.

### `Mul128`

```
export Mul128(a: u64, b: u64): (hi: u64, lo: u64)
```

Mul128 is the full 128-bit product of a and b: hi holds its high 64
bits and lo its low 64. Safe.

### `Null`

```
export Null[T](): *T
```

Null is the null pointer. Null[void]() is a null *void. Safe.

### `Of`

```
export Of[T](r: &T): *T
```

Of is r's address as a raw pointer. Safe.

### `OfMut`

```
export OfMut[T](r: mut T): *T
```

OfMut is r's address as a raw pointer to write through. Safe.

### `Offset`

```
export Offset[T](p: *T, n: int): *T
```

Offset is p moved by n elements of T, n times T's size in bytes; n
may be negative. Needs unsafe.

### `Popcount64`

```
export Popcount64(x: u64): int
```

Popcount64 counts x's one bits. Safe.

### `PtrCast`

```
export PtrCast[T, U](p: *T): *U
```

PtrCast is p as a pointer to U, at the same address. T or U may be
void: PtrCast[T, void](p) is p as C's untyped `void *`. Needs unsafe.

### `Read`

```
export Read[T](p: *T): T
```

- Flows: The result borrows from p.

Read loads a T from p, which must be aligned to AlignOf[T](). Needs
unsafe.

### `ReadUnaligned`

```
export ReadUnaligned[T: Plain](p: *T): T
```

- Flows: The result borrows from p.

ReadUnaligned loads an integer T from p, at any alignment; any other
T is E0701. Needs unsafe.

### `Release`

```
export Release[T, A](x: T, a: &A)
```

Release ends x: it calls T's Free with a (an &Alloc) when T
implements Free, and otherwise forgets x. The choice is made at
compile time for each T. Needs unsafe.

### `RotL64`

```
export RotL64(x: u64, n: int): u64
```

RotL64 rotates x left by n bits, n taken modulo 64. Safe.

### `Same`

```
export Same[T](a: *T, b: *T): bool
```

Same reports whether a and b hold the same address. Safe.

### `Select`

```
export Select[T: Plain](c: bool, a: T, b: T): T
```

- Flows: The result borrows from a and b.

Select is a when c holds, else b. The compiler is told c is
unpredictable, so for an integer, bool, pointer or record T the choice
has no branch on c (a cmov on x86_64, a csel on aarch64), even in a
loop whose next load depends on it, a binary search's, where the
backend would otherwise branch; a float T may still branch on x86_64.
Both a and b are evaluated. Safe.

### `Sha1NextE`

```
export Sha1NextE(abcd: [4]u32, w: [4]u32): [4]u32 cpu "sha, sha2"
```

Sha1NextE is w with ROTL30 of abcd's a added to lane 3: given the state
before four rounds, the e they leave plus the next group's W[t]. It
needs the sha feature as Sha1Rounds4 does. O(1).

### `Sha1Rounds4`

```
export Sha1Rounds4(abcd: [4]u32, we: [4]u32, const g: u8): [4]u32
    requires g < 4
    cpu "sha, sha2"
```

Sha1Rounds4 is four rounds of FIPS 180-4 section 6.1.2 step 3 inside
round group g (rounds 20g to 20g + 19), with its f and K (0 Ch,
1 Parity, 2 Maj, 3 Parity). Each [4]u32 has its oldest word in lane
3: abcd holds d, c, b, a from lane 0, and a word group holds W[t + 3]
to W[t]. we is the four words with e added to W[t], lane 3. The result
is the new a to d in the same order; the e they leave is ROTL30 of
abcd's a, which Sha1NextE adds. g is a constant from 0 to 3 (E0706
otherwise). Safe in a function whose features include sha; elsewhere
the call needs unsafe (E1101) behind a simd.Cpu() test. On x86_64 the
function's features must also include sse2, inside unsafe too (E1106):
hosted targets always have it; on x86_64-freestanding the function's
cpu clause must name it, whatever the bin's cpu. O(1).

### `Sha1Schedule4`

```
export Sha1Schedule4(w0: [4]u32, w1: [4]u32, w2: [4]u32, w3: [4]u32): [4]u32 cpu "sha, sha2"
```

Sha1Schedule4 is W[t] to W[t + 3] of FIPS 180-4 section 6.1.2 step 1,
from w0 = W[t - 16] .. W[t - 13], w1, w2 and w3 = W[t - 4] .. W[t - 1].
It needs the sha feature as Sha1Rounds4 does. O(1).

### `Sha256Rounds4`

```
export Sha256Rounds4(s: [8]u32, wk: [4]u32): [8]u32 cpu "sha, sha2"
```

Sha256Rounds4 is four rounds of FIPS 180-4 section 6.2.2 step 3 over the
working variables in s, wk holding W[t] + K[t] for the four rounds in
order. s holds f, e, b, a, then h, g, d, c (x86_64's SHA256RNDS2 order;
aarch64 permutes), and so does the result. Safe in a function whose
features include sha; elsewhere the call needs unsafe (E1101) behind a
simd.Cpu() test. On x86_64 the function's features must also include
sse2, inside unsafe too (E1106): hosted targets always have it; on
x86_64-freestanding the function's cpu clause must name it, whatever
the bin's cpu. O(1).

### `Sha256Schedule4`

```
export Sha256Schedule4(w0: [4]u32, w1: [4]u32, w2: [4]u32, w3: [4]u32): [4]u32 cpu "sha, sha2"
```

Sha256Schedule4 is W[t] to W[t + 3] of FIPS 180-4 section 6.2.2 step 1,
from w0 = W[t - 16] .. W[t - 13], w1, w2 and w3 = W[t - 4] .. W[t - 1].
It needs the sha feature as Sha256Rounds4 does. O(1).

### `Slice`

```
export Slice[T](p: *T, n: int): []T
```

Slice is a slice of the n elements at p. Needs unsafe: the caller
answers for the memory staying valid while the slice is in use.

### `SliceMut`

```
export SliceMut[T](p: *T, n: int): mut []T
```

SliceMut is a writable slice of the n elements at p. Needs unsafe.

### `SqrtF64`

```
export SqrtF64(x: f64): f64
```

SqrtF64 is the correctly rounded square root of x. Safe.

### `Str`

```
export Str(p: *u8, n: int): str
```

Str is the n bytes at p as a str. Needs unsafe: the caller answers
for the bytes being UTF-8 and staying valid.

### `StrData`

```
export StrData(s: str): *u8
```

StrData is the address of s's first byte. Safe.

### `SubBorrow64`

```
export SubBorrow64(x: u64, y: u64, b: u64): (diff: u64, borrow: u64)
```

SubBorrow64 is x - y - b: diff is its value modulo 2^64, and borrow is 1
when x is less than y + b, else 0. b is meant to be 0 or 1, the borrow
of the limb below. Safe. A chain of calls compiles to one
subtract-with-borrow chain (sbb on x86_64), with no branches, in every
build. O(1); allocates nothing.

### `Write`

```
export Write[T](p: *T, x: T)
```

Write stores x at p, which must be aligned to AlignOf[T](). The value
that was there is overwritten, not released. Needs unsafe.

<!-- api:end -->
