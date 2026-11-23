// blake3, Mk, idiomatic std: the task of ../README.md with std.hash.Blake3
// over a generated multi-MiB SplitMix64 buffer: bulk_reps full-buffer passes
// plus short_reps passes over 64-byte slices, XOR-folding the first 8 bytes
// (little-endian) of every 32-byte digest into a u64 checksum and mutating
// one buffer byte after each pass so no pass can be hoisted.
// Usage: blake3 <mib> <bulk_reps> <short_reps> <seed>. Prints the checksum.

import std.hash

Mix(x: u64): u64 {
    var z := x
    z = (z ^ (z >> 30)) *% 0xbf58476d1ce4e5b9
    z = (z ^ (z >> 27)) *% 0x94d049bb133111eb
    return z ^ (z >> 31)
}

Lo64(d: []u8): u64 {
    var x: u64 = 0
    for k in 0..8 {
        x = x | (u64(d[k]) << (8 * k))
    }
    return x
}

export Main(sys: &System): OutOfMemory|ParseError|WriteError!void {
    mib := Cast[int](try sys.args[1].ParseInt())
    bulk_reps := Cast[int](try sys.args[2].ParseInt())
    short_reps := Cast[int](try sys.args[3].ParseInt())
    var st := Cast[u64](try sys.args[4].ParseInt())
    n := mib * 1048576
    buf := try sys.arena.Slice[u8](n)
    var i := 0
    for i < n {
        st = st +% 0x9e3779b97f4a7c15
        x := Mix(st)
        for k in 0..8 {
            buf[i + k] = Trunc[u8](x >> (8 * k))
        }
        i += 8
    }
    var sum: u64 = 0
    for r in 0..bulk_reps {
        h := Lo64(hash.Blake3(buf[..])[..])
        sum = sum ^ h
        buf[r % n] = buf[r % n] ^ Trunc[u8](h)
    }
    for r in 0..short_reps {
        var j := 0
        for j < n {
            sum = sum ^ Lo64(hash.Blake3(buf[j..j + 64])[..])
            j += 64
        }
        idx := (r * 997) % n
        buf[idx] = buf[idx] ^ Trunc[u8](sum)
    }
    var out := sys.Stdout()
    try Print(mut out, "{sum}\n", (sum))
}
