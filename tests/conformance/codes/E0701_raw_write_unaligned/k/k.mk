import std.raw

struct Pair {
    a: u8
    b: u16
}

export Use() {
    var b: [16]u8 = [0; 16]
    unsafe "every store is at offset 1 of b, and the widest is 8 bytes" {
        p := raw.Offset(raw.DataMut(mut b[..]), 1)
        raw.WriteUnaligned(p, Trunc[u8](1))
        raw.WriteUnaligned[u16](raw.PtrCast[u8, u16](p), 2)
        raw.WriteUnaligned(raw.PtrCast[u8, u32](p), raw.F32Bits(1.5))
        raw.WriteUnaligned(raw.PtrCast[u8, u64](p), raw.F64Bits(2.5))
        raw.WriteUnaligned[i64](raw.PtrCast[u8, i64](p), -1)
        raw.WriteUnaligned[f32](raw.PtrCast[u8, f32](p), 1.5)                // <- E0701
        raw.WriteUnaligned(raw.PtrCast[u8, f64](p), 2.5)                     // <- E0701
        raw.WriteUnaligned(raw.PtrCast[u8, Pair](p), Pair(a: 1, b: 2))       // <- E0701
        raw.WriteUnaligned(raw.PtrCast[u8, (x: u8, y: u8)](p), (x: 1, y: 2)) // <- E0701
    }
}
