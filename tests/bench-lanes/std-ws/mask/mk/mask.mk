// ws-mask, Mk: std/websocket/frame.mk's Mask, copied byte for byte (it is
// no public std name, so a program cannot call it;
// std/std-ws_bench_test.go checks the copy against the library's): RFC
// 6455 5.3's masking, a scalar head to phase 0, 16 bytes a step with
// std.simd, a scalar tail. n bytes of splitmix64 output (seed 1), rounds
// times masked in place with round r's key (the four low bytes of (r + 1)
// * 0x9E3779B97F4A7C15) from phase r % 4; the checksum adds, per round,
// the byte at (r * 7919) % n and the phase Mask returns. The buffer is an
// arena slice, whose alignment Mk code cannot set without std.raw (BS-4's
// labelled alignment exception, README.md).
// Usage: ws-mask <n> <rounds>.

import std.simd

// Mask is std/websocket/frame.mk's, below its doc comment.
Mask(b: mut []u8, key: [4]u8, phase: int): int {
    n := b.Len()
    var i := 0
    var p := phase & 3
    // A scalar head, so the vector loop starts at phase 0 and its key
    // pattern is the key itself, repeated.
    for i < n && p != 0 {
        b[i] = b[i] ^ key[p]
        p = (p + 1) & 3
        i += 1
    }
    if n - i >= 16 {
        k := simd.Vec[u8, 16].FromArray([key[0], key[1], key[2], key[3], key[0], key[1], key[2], key[3], key[0], key[1],
            key[2], key[3], key[0], key[1], key[2], key[3]])
        for n - i >= 16 {
            v := simd.Vec[u8, 16].FromSlice(b[..], i) ^ k
            v.CopyTo(b, i)
            i += 16
        }
    }
    for i < n {
        b[i] = b[i] ^ key[p]
        p = (p + 1) & 3
        i += 1
    }
    return p
}

export Main(sys: &System): OutOfMemory|ParseError|WriteError!void {
    n := Cast[int](try sys.args[1].ParseInt())
    rounds := Cast[int](try sys.args[2].ParseInt())
    data := try sys.arena.Slice[u8](n)
    var st: u64 = 1
    var i := 0
    for i < n {
        st = st +% 0x9E3779B97F4A7C15
        var z := st
        z = (z ^ (z >> 30)) *% 0xBF58476D1CE4E5B9
        z = (z ^ (z >> 27)) *% 0x94D049BB133111EB
        z = z ^ (z >> 31)
        var k := 0
        for k < 8 && i < n {
            data[i] = Trunc[u8](z >> Trunc[u64](8 * k))
            k += 1
            i += 1
        }
    }
    var total: u64 = 0
    for r in 0..rounds {
        kw := (Cast[u64](r) +% 1) *% 0x9E3779B97F4A7C15
        key: [4]u8 = [Trunc[u8](kw), Trunc[u8](kw >> 8), Trunc[u8](kw >> 16), Trunc[u8](kw >> 24)]
        p := Mask(mut data[..], key, r % 4)
        total = total +% u64(data[(r * 7919) % n]) +% Cast[u64](p)
    }
    var w := sys.Stdout()
    try Print(mut w, "{total}\n", (total))
}
