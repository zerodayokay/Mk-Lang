// verdict: reject E0701
// note: mem.Zeroed's T must be zero-valid: a str, a reference, a fn value, a struct holding one, or an open type parameter is never all zero bytes; a plain record of many fields, an array of one and an optional are. T is the result, so it is not an error union either, written, through an alias, under try or as a fn value; a field of one, or an inferred ok type, is fine
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

struct Held {
    r: OutOfMemory!int
}

type Result = OutOfMemory!int

Ok(): int {
    w := mem.Zeroed[Wide]()
    xs := mem.Zeroed[[3]Wide]()
    return w.a + xs[2].a
}

Inferred(): OutOfMemory!Wide {
    return mem.Zeroed()
}

Field(): int {
    h := mem.Zeroed[Held]()
    v := h.r else {
        return 1
    }
    return v
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

Union(): int {
    v := mem.Zeroed[OutOfMemory!int]() else { // <- E0701
        return 1
    }
    return v
}

Aliased(): int {
    v := mem.Zeroed[Result]() else { // <- E0701
        return 1
    }
    return v
}

Tried(): OutOfMemory!int {
    v := try mem.Zeroed[OutOfMemory!int]() // <- E0701
    return v
}

UnionValue(): int {
    f := mem.Zeroed[OutOfMemory!int] // <- E0701
    v := f() else {
        return 1
    }
    return v
}
