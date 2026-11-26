// A narrowing to another type parameter follows that parameter's own
// narrowing: inside `if T is U { if U is u32 { .. } }` T is u32, for its
// values, the types written T, a `mut T` written through and the result.
// Each instance takes its own branch.
import std.mem

Id[T](x: T): T {
    return x
}

Ch[T: Plain, U: Plain](x: T, n: int): int {
    if T is U {
        if U is u32 {
            a := Id[T](x) + 1
            var b: T = x + 2
            c := Cast[T](n) + 3
            return int(a + b + c)
        }
    }
    return 0
}

Deep[T: Plain, U: Plain, V: Plain](x: T): int {
    if T is U {
        if U is V {
            if V is u16 {
                var b: [2]T = [x, x + 1]
                return int(Id[T](b[1])) + int(SizeOf[T]())
            }
        }
    }
    return 0
}

Opt[T: Plain, U: Plain](x: T): int {
    if T is ?U {
        if U is u8 {
            var y: T = some(Id[U](3) + 1)
            return int(y else { 0 }) + int(x else { 9 })
        }
    }
    return -1
}

Wb[T: Plain, U: Plain](x: T, p: mut T): T {
    var v: T = x
    if T is U {
        if U is u32 {
            v = v + 10
            *p = *p + 100
            return v * 3
        }
    }
    return v
}

struct Bytes[const N: int] {
    data: [N]u8
}

ReadSizes[T: Plain](a: &[SizeOf[T]()]u8, b: &Bytes[SizeOf[T]()]): int {
    return a.Len() + b.data.Len()
}

NarrowSizes[T: Plain, U: Plain](a: [SizeOf[T]()]u8, b: Bytes[SizeOf[T]()]): int {
    if T is U {
        if U is u32 {
            return ReadSizes[T](&a, &b)
        }
    }
    return -1
}

Sizes[T: Plain, U: Plain](): int {
    a := mem.Zeroed[[SizeOf[T]()]u8]()
    b := mem.Zeroed[Bytes[SizeOf[T]()]]()
    return NarrowSizes[T, U](a, b)
}

export Main(sys: &System): WriteError!void {
    try sys.Print("{a} {b} {c}\n", (a: Ch[u32, u32](1, 2), b: Ch[i64, i64](1, 2), c: Ch[u32, i64](1, 2)))
    try sys.Print("{a} {b} {c} {d}\n", (a: Deep[u16, u16, u16](4), b: Deep[u16, u16, u32](4), c: Opt[?u8, u8](some(2)), d: Opt[?u8, u16](none)))
    var m: u32 = 1
    r := Wb[u32, u32](5, mut m)
    var k: i64 = 1
    s := Wb[i64, i64](5, mut k)
    try sys.Print("{r} {m} {s} {k}\n", (r, m, s, k))
    try sys.Print("{a} {b}\n", (a: Sizes[u32, u32](), b: Sizes[u32, i64]()))
}
