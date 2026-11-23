// deflate, Mk, idiomatic std: the task of ../README.md with std.compress.Deflate
// and std.compress.Inflate (RFC 1951 raw DEFLATE, default level) round-tripping
// a generated mixed text buffer in memory. One scratch arena is created before
// the loop and reset after each round; every round verifies exact output length
// and FNV-1a 64 checksum against the source buffer, folds the checksum and
// length, and flips one source byte so no round can be hoisted.
// Usage: deflate <bytes> <rounds> <seed>. Prints the checksum.

import std.compress

error Mismatch

type DeflateError = compress.Error | Mismatch | OutOfMemory | ParseError | WriteError

const Hex: str = "0123456789abcdef"

const Vocab: [16]str = [
    "ERROR", "WARN ", "INFO ", "DEBUG",
    "GET", "POST", "PUT", "DELETE",
    "/api/v1/users", "/api/v1/orders", "/api/v1/items", "/healthz",
    "timeout", "ok", "cached", "retry",
]

const Prose: str = "The quick brown fox jumps over the lazy dog; pack my box with five dozen liquor jugs! 0123456789 "

Mix(x: u64): u64 {
    var z := x
    z = (z ^ (z >> 30)) *% 0xbf58476d1ce4e5b9
    z = (z ^ (z >> 27)) *% 0x94d049bb133111eb
    return z ^ (z >> 31)
}

Fnv(b: []u8): u64 {
    var h: u64 = 0xcbf29ce484222325
    for c in b {
        h = (h ^ u64(c)) *% 0x100000001b3
    }
    return h
}

Emit(dst: mut []u8, pos: mut int, s: str) {
    b := s.AsBytes()
    rem := dst.Len() - *pos
    if rem <= 0 {
        return
    }
    k := if b.Len() < rem { b.Len() } else { rem }
    (mut dst[*pos..*pos + k]).CopyFrom(b[0..k])
    *pos += k
}

Generate(dst: mut []u8, seed: u64) {
    hb := Hex.AsBytes()
    var st := seed
    var pos := 0
    n := dst.Len()
    for pos < n {
        st = st +% 0x9e3779b97f4a7c15
        z := Mix(st)
        Emit(dst, mut pos, "ts=")
        for k in 0..8 {
            if pos < n {
                shift := 60 - 4 * k
                dst[pos] = hb[Cast[int]((z >> shift) & 15)]
                pos += 1
            }
        }
        Emit(dst, mut pos, " level=")
        Emit(dst, mut pos, Vocab[Cast[int](z & 3)])
        Emit(dst, mut pos, " method=")
        Emit(dst, mut pos, Vocab[4 + Cast[int]((z >> 2) & 3)])
        Emit(dst, mut pos, " path=")
        Emit(dst, mut pos, Vocab[8 + Cast[int]((z >> 4) & 3)])
        Emit(dst, mut pos, " msg=")
        Emit(dst, mut pos, Vocab[12 + Cast[int]((z >> 6) & 3)])
        Emit(dst, mut pos, " body=")
        off := Cast[int]((z >> 8) % 60)
        plen := 16 + Cast[int]((z >> 16) % 21)
        Emit(dst, mut pos, Prose[off..off + plen])
        Emit(dst, mut pos, "\n")
    }
}

Round(a: &Arena, src: []u8): compress.Error|Mismatch!u64 {
    want := Fnv(src)
    comp := try compress.Deflate(a, src)
    dec := try compress.Inflate(a, comp, src.Len())
    if dec.Len() != src.Len() {
        fail Mismatch
    }
    got := Fnv(dec)
    if got != want {
        fail Mismatch
    }
    return got +% Cast[u64](dec.Len())
}

export Main(sys: &System): DeflateError!void {
    n := Cast[int](try sys.args[1].ParseInt())
    rounds := Cast[int](try sys.args[2].ParseInt())
    seed := Cast[u64](try sys.args[3].ParseInt())
    src := try sys.arena.Slice[u8](n)
    Generate(src, seed)
    var a := try Arena.New(sys.heap)
    defer a.Free()
    var total: u64 = 0
    for r in 0..rounds {
        total = total +% try Round(&a, src[..])
        a.Reset()
        idx := (r * 1009) % n
        src[idx] = src[idx] ^ 0x01
    }
    var out := sys.Stdout()
    try Print(mut out, "{total}\n", (total))
}
