// Why: intrinsics are lowered by codegen and run by the evaluator; both
// paths must agree.
// What: tests of std.mem's OffsetOf, Prefetch and Zeroed (intrinsics.mk), std.raw's
// AtomicCopy, AtomicFill, FnFromAddr and Select, and std.atomic's Pause.
// std.runtime's Cycles is tested in std/runtime/tests_cycles.mk, since
// std.mem builds on freestanding and std.runtime does not.
// How: run by `mk test --std=std.mem`: each runs natively, and the
// consts run the same functions in the evaluator
// (internal/eval/intrinsic_machine.go).

import std.atomic
import std.raw

struct OffRec {
    a: u8
    b: u64
    c: u16
}

const OffA: int = OffsetOf[OffRec]("a")
const OffB: int = OffsetOf[OffRec]("b")
const OffC: int = OffsetOf[OffRec]("c")

// AtomicElems fills words 1 and 2 with 7, copies words 0 and 1 up one
// (an overlap memmove copies backward), then words 2 and 3 down two (no
// overlap), each element whole and unordered.
AtomicElems(): [6]u32 {
    var w: [6]u32 = [1, 2, 3, 4, 5, 6]
    unsafe "w is a local array of six words" {
        p := raw.PtrCast[[6]u32, u32](raw.OfMut(mut w))
        raw.AtomicFill[u32](raw.Offset(p, 1), 7, 2)
        raw.AtomicCopy[u32](raw.Offset(p, 1), p, 2)
        raw.AtomicCopy[u32](raw.Offset(p, 4), raw.Offset(p, 2), 2)
        raw.AtomicCopy[u32](raw.Offset(p, 2), raw.Offset(p, 3), 2)
    }
    return w
}

const Elems: [6]u32 = AtomicElems()

align(4) struct AtomicPair {
    a: u16
    b: u16
}

// AtomicFloats fills with a float and a plain pair, any plain T of 1, 2, 4
// or 8 bytes aligned to its size (an atomic needs its natural
// alignment; AtomicPair is align(4)).
AtomicFloats(): bool {
    var f: [3]f64 = [0.0, 0.0, 0.0]
    var q: [3]AtomicPair = [AtomicPair(a: 0, b: 0), AtomicPair(a: 0, b: 0), AtomicPair(a: 0, b: 0)]
    unsafe "f and q are local arrays of three elements" {
        raw.AtomicFill[f64](raw.PtrCast[[3]f64, f64](raw.OfMut(mut f)), -2.5, 3)
        pq := raw.PtrCast[[3]AtomicPair, AtomicPair](raw.OfMut(mut q))
        raw.AtomicFill[AtomicPair](pq, AtomicPair(a: 9, b: 65535), 2)
        raw.AtomicCopy[AtomicPair](raw.Offset(pq, 2), pq, 1)
    }
    return f[0] == -2.5 && f[2] == -2.5 && q[2].a == 9 && q[2].b == 65535 && q[1].a == 9
}

const EvalFloats: bool = AtomicFloats()

test "OffsetOf reads the layout" {
    // Each field's address, taken natively, less the record's: the
    // offsets codegen laid out, against the evaluator's by name.
    r := OffRec(a: 1, b: 2, c: 3)
    var da := 0
    var db := 0
    var dc := 0
    unsafe "addresses of a local's fields, read only as integers" {
        base := raw.Addr(raw.Of(&r))
        da = Cast[int](raw.Addr(raw.Of(&r.a)) - base)
        db = Cast[int](raw.Addr(raw.Of(&r.b)) - base)
        dc = Cast[int](raw.Addr(raw.Of(&r.c)) - base)
    }
    Assert(OffA == da && OffB == db && OffC == dc)
    Assert(OffA != OffB && OffB != OffC && OffA != OffC)
}

test "AtomicFill and AtomicCopy, natively and in the evaluator" {
    // Fill: [1 7 7 4 5 6]; up one, backward: [1 1 7 4 5 6]; down two:
    // [1 1 7 4 7 4]; 3, 4 down one: [1 1 4 7 7 4].
    want: [6]u32 = [1, 1, 4, 7, 7, 4]
    n := AtomicElems()
    for i in 0..6 {
        Assert(Elems[i] == want[i])
        Assert(n[i] == want[i])
    }
    Assert(EvalFloats)
    Assert(AtomicFloats())
}

test "OffsetOf at run time, Prefetch and Pause" {
    r := OffRec(a: 1, b: 2, c: 3)
    Assert(OffsetOf[OffRec]("b") == OffB)
    Prefetch[OffRec](&r, false)
    Prefetch[OffRec](&r, true)
    var w := false
    for _ in 0..3 {
        Prefetch[OffRec](&r, w)
        w = !w
    }
    atomic.Pause()
}

ReturnSeven(): int {
    return 7
}

test "FnFromAddr calls a function at an address" {
    f := ReturnSeven
    var g: fn(): int = ReturnSeven
    unsafe "f's bits are its function's address" {
        a := raw.Read(raw.PtrCast[fn(): int, uint](raw.Of(&f)))
        g = raw.FnFromAddr[fn(): int](a)
    }
    Assert(g() == 7)
}

struct SelRec {
    a: u8
    b: u64
    c: u16
}

// Selects folds raw.Select over scalars, a pointer and a record, for both
// conditions; SelectsAt is the same at compile time.
Selects(): u64 {
    var acc: u64 = 0
    x: u64 = 5
    y: u64 = 9
    for i in 0..4 {
        c := i % 2 == 0
        acc = acc *% 31 +% raw.Select(c, x, y)
        acc = acc *% 31 +% Cast[u64](raw.Select(c, -3, 40) + 3)
        bv: u64 = if raw.Select(c, true, false) { 1 } else { 2 }
        acc = acc *% 31 +% bv
        r := raw.Select(c, SelRec(a: 1, b: 2, c: 3), SelRec(a: 4, b: 5, c: 6))
        acc = acc *% 31 +% Cast[u64](r.a) + r.b + Cast[u64](r.c)
        p := raw.Select(c, raw.Of(&x), raw.Of(&y))
        pv: u64 = if raw.Same(p, raw.Of(&x)) { 7 } else { 8 }
        acc = acc *% 31 +% pv
        acc = acc *% 31 +% Cast[u64](raw.Select(c, 1.5, 2.5) * 2.0)
    }
    return acc
}

const SelectsAt: u64 = Selects()

test "Select picks a or b, at run time and at compile time" {
    Assert(raw.Select(true, 1, 2) == 1 && raw.Select(false, 1, 2) == 2)
    r := raw.Select(false, SelRec(a: 1, b: 2, c: 3), SelRec(a: 4, b: 5, c: 6))
    Assert(r.a == 4 && r.b == 5 && r.c == 6)
    Assert(Selects() == SelectsAt)
}

struct ZeroInner {
    a: i32
    b: f64
}

// ZeroRec has more fields than a caller would name by hand, of each
// zero-valid kind: integers of every width, floats, bool, char, raw
// pointers, an optional fn, arrays and a nested struct.
struct ZeroRec {
    f01: i8
    f02: i16
    f03: i32
    f04: i64
    f05: u8
    f06: u16
    f07: u32
    f08: u64
    f09: int
    f10: uint
    f11: f32
    f12: f64
    f13: bool
    f14: char
    f15: *u8
    f16: *ZeroInner
    f17: ?fn(i32): i32
    f18: [5]u16
    f19: ZeroInner
    f20: [2]ZeroInner
}

// IsZero reports whether every field of r is its zero.
IsZero(r: &ZeroRec): bool {
    ok := r.f01 == 0 && r.f02 == 0 && r.f03 == 0 && r.f04 == 0 && r.f05 == 0 && r.f06 == 0 && r.f07 == 0 && r.f08 == 0
    ok2 := r.f09 == 0 && r.f10 == 0 && r.f11 == 0.0 && r.f12 == 0.0 && !r.f13 && r.f14 == '\0'
    ok3 := raw.IsNull(r.f15) && raw.IsNull(r.f16) && r.f17.IsNone() && r.f19.a == 0 && r.f19.b == 0.0
    var ok4 := true
    for i in 0..5 {
        ok4 = ok4 && r.f18[i] == 0
    }
    for i in 0..2 {
        ok4 = ok4 && r.f20[i].a == 0 && r.f20[i].b == 0.0
    }
    return ok && ok2 && ok3 && ok4
}

Twice(x: i32): i32 {
    return x * 2
}

// Dirty is a ZeroRec with no field zero.
Dirty(p: *u8): ZeroRec {
    inner := ZeroInner(a: 3, b: 1.5)
    return ZeroRec(f01: -1, f02: -2, f03: -3, f04: -4, f05: 5, f06: 6, f07: 7, f08: 8, f09: 9, f10: 10, f11: 1.25, f12: 2.5,
        f13: true, f14: 'z', f15: p, f16: raw.Null[ZeroInner](), f17: some(Twice), f18: [1, 2, 3, 4, 5], f19: inner, f20: [inner, inner])
}

ZeroedIsZero(): bool {
    r := Zeroed[ZeroRec]()
    return IsZero(&r)
}

const ZeroedAt: bool = ZeroedIsZero()

test "Zeroed is a record of zero bytes, natively and in the evaluator" {
    Assert(ZeroedAt)
    b: u8 = 1
    var r := Dirty(raw.Of(&b))
    Assert(!IsZero(&r))
    r = Zeroed[ZeroRec]()
    Assert(IsZero(&r))
    xs := Zeroed[[3]ZeroInner]()
    Assert(xs[2].a == 0 && xs[2].b == 0.0)
    Assert(Zeroed[u64]() == 0 && Zeroed[?u32]().IsNone())
    _ = Zeroed[void]()
}

struct BvPair {
    a: u16
    b: u16
}

struct BvNested {
    head: BvPair
    tag: bool
    pad: u8
    tail: [2]u8
}

packed struct BvPacked {
    tag: u8
    val: u32
    flags: u16
}

struct BvEmpty {
}

BvCompileCheck(): bool {
    a: u32 = 0x1122_3344
    ab := AsBytes(&a)
    if ab.Len() != 4 || ab[0] != 0x44 || ab[3] != 0x11 {
        return false
    }
    arr := [BvPair(a: 0x0102, b: 0x0304), BvPair(a: 0x0506, b: 0x0708)]
    sb := SliceAsBytes(arr[..])
    return sb.Len() == 8 && sb[0] == 0x02 && sb[1] == 0x01 && sb[6] == 0x08 && sb[7] == 0x07
}

const BvAtCompileTime: bool = BvCompileCheck()

test "AsBytes and SliceAsBytes view scalars, arrays and unpadded structs, natively and in eval" {
    Assert(BvAtCompileTime)
    flag := true
    fb := AsBytes(&flag)
    Assert(fb.Len() == 1 && fb[0] == 1)
    ch: char = 'A'
    cb := AsBytes(&ch)
    Assert(cb.Len() == 4 && cb[0] == 65 && cb[1] == 0 && cb[2] == 0 && cb[3] == 0)
    f: f32 = 1.0
    Assert(AsBytes(&f).Len() == 4)
    n := BvNested(head: BvPair(a: 0x0A0B, b: 0x0C0D), tag: true, pad: 0xEE, tail: [0x12, 0x34])
    nb := AsBytes(&n)
    Assert(nb.Len() == 8 && nb[0] == 0x0B && nb[4] == 1 && nb[5] == 0xEE && nb[6] == 0x12 && nb[7] == 0x34)
    pk := BvPacked(tag: 0x7F, val: 0x0102_0304, flags: 0xAABB)
    pkb := AsBytes(&pk)
    Assert(pkb.Len() == 7 && pkb[0] == 0x7F && pkb[1] == 0x04 && pkb[4] == 0x01 && pkb[5] == 0xBB && pkb[6] == 0xAA)
    emp := BvEmpty()
    Assert(AsBytes(&emp).Len() == 0)
    empty_vs: [0]BvPair = []
    Assert(SliceAsBytes(empty_vs[..]).Len() == 0)
    nums: [3]u16 = [0x0102, 0x0304, 0x0506]
    nsb := SliceAsBytes(nums[..])
    Assert(nsb.Len() == 6 && nsb[0] == 0x02 && nsb[5] == 0x05)
}
