// verdict: ok
// note: TestDiffLayout (internal/corpus) reads this file with layout.<target>.golden: every function Layout<Name>() returns SizeOf and AlignOf of one type, and the golden has one line per function
import std.simd

// Convention, for the reader of this file: function Layout<Name>() is the golden's line <Name>.
// The golden's offsets are those of the type's fields in declaration order (a record's in name
// order, as it is laid out), of a slice's pointer and length, of an array's elements, of an
// optional's some flag and value, of an enum's tag and payload, and of a result's code and
// payloads (each where it starts). Mk has no OffsetOf, so the offsets are compared with the checker's types.Layout and
// with native reads, not returned by these functions.

struct Layout {
    size: int
    align: int
}

struct Pair {
    a: u8
    b: i64
}

struct Mixed {
    a: u8
    b: u16
    c: u8
    d: u32
    e: u8
}

struct Nested {
    p: Pair
    x: u8
}

struct Tail {
    x: i32
    b: bool
}

packed struct PackedMixed {
    a: u8
    b: u16
    c: u8
    d: u32
    e: u8
}

packed(2) struct Packed2Mixed {
    a: u8
    b: u16
    c: u8
    d: u32
    e: u8
}

type Rec = (b: u8, a: i64)

enum Color {
    Red
    Green
    Blue
}

enum Level: u16 {
    Low = 1
    High = 2
}

enum Code {
    Small = 0
    Large = 70000
}

enum Shape {
    Circle(r: f64)
    Rect(w: f32, h: f32)
    Empty
}

enum Tok {
    Num(n: u16)
    Ch(c: u8)
}

error Bad

error Big(code: i64)

LayoutI8(): Layout {
    return Layout(size: SizeOf[i8](), align: AlignOf[i8]())
}

LayoutI16(): Layout {
    return Layout(size: SizeOf[i16](), align: AlignOf[i16]())
}

LayoutI32(): Layout {
    return Layout(size: SizeOf[i32](), align: AlignOf[i32]())
}

LayoutI64(): Layout {
    return Layout(size: SizeOf[i64](), align: AlignOf[i64]())
}

LayoutU8(): Layout {
    return Layout(size: SizeOf[u8](), align: AlignOf[u8]())
}

LayoutU16(): Layout {
    return Layout(size: SizeOf[u16](), align: AlignOf[u16]())
}

LayoutU32(): Layout {
    return Layout(size: SizeOf[u32](), align: AlignOf[u32]())
}

LayoutU64(): Layout {
    return Layout(size: SizeOf[u64](), align: AlignOf[u64]())
}

LayoutInt(): Layout {
    return Layout(size: SizeOf[int](), align: AlignOf[int]())
}

LayoutUint(): Layout {
    return Layout(size: SizeOf[uint](), align: AlignOf[uint]())
}

LayoutF32(): Layout {
    return Layout(size: SizeOf[f32](), align: AlignOf[f32]())
}

LayoutF64(): Layout {
    return Layout(size: SizeOf[f64](), align: AlignOf[f64]())
}

LayoutBool(): Layout {
    return Layout(size: SizeOf[bool](), align: AlignOf[bool]())
}

LayoutChar(): Layout {
    return Layout(size: SizeOf[char](), align: AlignOf[char]())
}

LayoutRefI32(): Layout {
    return Layout(size: SizeOf[&i32](), align: AlignOf[&i32]())
}

LayoutSliceU8(): Layout {
    return Layout(size: SizeOf[[]u8](), align: AlignOf[[]u8]())
}

LayoutSliceI64(): Layout {
    return Layout(size: SizeOf[[]i64](), align: AlignOf[[]i64]())
}

LayoutStr(): Layout {
    return Layout(size: SizeOf[str](), align: AlignOf[str]())
}

LayoutArrayU16(): Layout {
    return Layout(size: SizeOf[[3]u16](), align: AlignOf[[3]u16]())
}

LayoutArrayPair(): Layout {
    return Layout(size: SizeOf[[2]Pair](), align: AlignOf[[2]Pair]())
}

LayoutFn(): Layout {
    return Layout(size: SizeOf[fn(i32): i32](), align: AlignOf[fn(i32): i32]())
}

LayoutPair(): Layout {
    return Layout(size: SizeOf[Pair](), align: AlignOf[Pair]())
}

LayoutMixed(): Layout {
    return Layout(size: SizeOf[Mixed](), align: AlignOf[Mixed]())
}

LayoutNested(): Layout {
    return Layout(size: SizeOf[Nested](), align: AlignOf[Nested]())
}

LayoutTail(): Layout {
    return Layout(size: SizeOf[Tail](), align: AlignOf[Tail]())
}

LayoutPackedMixed(): Layout {
    return Layout(size: SizeOf[PackedMixed](), align: AlignOf[PackedMixed]())
}

LayoutPacked2Mixed(): Layout {
    return Layout(size: SizeOf[Packed2Mixed](), align: AlignOf[Packed2Mixed]())
}

LayoutRecord(): Layout {
    return Layout(size: SizeOf[Rec](), align: AlignOf[Rec]())
}

LayoutColor(): Layout {
    return Layout(size: SizeOf[Color](), align: AlignOf[Color]())
}

LayoutLevel(): Layout {
    return Layout(size: SizeOf[Level](), align: AlignOf[Level]())
}

LayoutCode(): Layout {
    return Layout(size: SizeOf[Code](), align: AlignOf[Code]())
}

LayoutShape(): Layout {
    return Layout(size: SizeOf[Shape](), align: AlignOf[Shape]())
}

LayoutTok(): Layout {
    return Layout(size: SizeOf[Tok](), align: AlignOf[Tok]())
}

LayoutOptRef(): Layout {
    return Layout(size: SizeOf[?&i32](), align: AlignOf[?&i32]())
}

LayoutOptI32(): Layout {
    return Layout(size: SizeOf[?i32](), align: AlignOf[?i32]())
}

LayoutOptU8(): Layout {
    return Layout(size: SizeOf[?u8](), align: AlignOf[?u8]())
}

LayoutOptPair(): Layout {
    return Layout(size: SizeOf[?Pair](), align: AlignOf[?Pair]())
}

LayoutBadI64(): Layout {
    return Layout(size: SizeOf[Bad!i64](), align: AlignOf[Bad!i64]())
}

LayoutBadU8(): Layout {
    return Layout(size: SizeOf[Bad!u8](), align: AlignOf[Bad!u8]())
}

LayoutBigU8(): Layout {
    return Layout(size: SizeOf[Big!u8](), align: AlignOf[Big!u8]())
}

LayoutCellI32(): Layout {
    return Layout(size: SizeOf[Cell[i32]](), align: AlignOf[Cell[i32]]())
}

// align(n) raises the alignment to n and rounds the size up to a multiple of it,
// as C's alignas does; it never lowers an alignment, so align(1) on a u32 struct changes nothing.
align(4096) struct PageTable {
    entries: [512]u64
}

align(16) struct Align16 {
    a: u8
}

align(64) struct Align64Pair {
    a: u8
    b: i64
}

align(1) struct Align1 {
    a: u32
}

struct HoldsAligned {
    x: Align16
    y: u8
}

LayoutPageTable(): Layout {
    return Layout(size: SizeOf[PageTable](), align: AlignOf[PageTable]())
}

LayoutAlign16(): Layout {
    return Layout(size: SizeOf[Align16](), align: AlignOf[Align16]())
}

LayoutAlign64Pair(): Layout {
    return Layout(size: SizeOf[Align64Pair](), align: AlignOf[Align64Pair]())
}

LayoutAlign1(): Layout {
    return Layout(size: SizeOf[Align1](), align: AlignOf[Align1]())
}

LayoutHoldsAligned(): Layout {
    return Layout(size: SizeOf[HoldsAligned](), align: AlignOf[HoldsAligned]())
}

// Vec[T, N] has the size of [N]T and the alignment of T,
// and Mask[T, N] the shape of Vec[U, N] with U the unsigned integer as wide as T.
struct HoldsVec {
    a: u8
    v: simd.Vec[f32, 4]
}

LayoutVecF32x4(): Layout {
    return Layout(size: SizeOf[simd.Vec[f32, 4]](), align: AlignOf[simd.Vec[f32, 4]]())
}

LayoutVecU8x32(): Layout {
    return Layout(size: SizeOf[simd.Vec[u8, 32]](), align: AlignOf[simd.Vec[u8, 32]]())
}

LayoutMaskF64x2(): Layout {
    return Layout(size: SizeOf[simd.Mask[f64, 2]](), align: AlignOf[simd.Mask[f64, 2]]())
}

LayoutHoldsVec(): Layout {
    return Layout(size: SizeOf[HoldsVec](), align: AlignOf[HoldsVec]())
}
