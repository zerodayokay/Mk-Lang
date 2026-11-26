import std.raw

export Use() {
    var w: u64 = 0
    p := raw.OfMut(mut w)
    raw.WriteUnaligned(p, 1)                         // <- E1101
    g: fn(p: *u64, x: u64) = raw.WriteUnaligned[u64] // <- E1101
    unsafe "p is w's address" {
        raw.WriteUnaligned(p, 2)
        g(p, 3)
    }
}
