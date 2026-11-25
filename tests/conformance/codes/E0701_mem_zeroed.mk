// verdict: reject E0701
// note: mem.Zeroed's T must be zero-valid: a str, a reference, a fn value, a struct holding one, or an open type parameter is never all zero bytes; a plain record of many fields, an array of one and an optional are
import std.mem

struct Wide {
    a: int
    b: u64
    c: f64
    d: ?u32
    e: *u8
    f: [4]u16
}

struct Named {
    id: int
    name: str
}

Ok(): int {
    w := mem.Zeroed[Wide]()
    xs := mem.Zeroed[[3]Wide]()
    return w.a + xs[2].a
}

Text(): Named {
    return mem.Zeroed[Named]() // <- E0701
}

Ref(): &int {
    return mem.Zeroed[&int]() // <- E0701
}

Open[T](): T {
    return mem.Zeroed[T]() // <- E0701
}

Value(): fn(int): int {
    f := mem.Zeroed[str] // <- E0701
    _ = f
    return Value2
}

Value2(x: int): int {
    return x
}
