// hashmap-str, Mk keyed, idiomatic std: the mk column's program (../mk)
// with one change, the map: Map.NewKeyed under a key from HashKey.New, made
// once per process (SipHash-1-3 under 16 bytes of Io.Random), the
// DoS-resistant peer of Rust's RandomState and Go's per-map seed.
// Usage: hashmap-str <n> <rounds> <seed>. Prints the checksum.

const Alph: str = "abcdefghijklmnopqrstuvwxyz012345"

Mix(x: u64): u64 {
    var z := x
    z = (z ^ (z >> 30)) *% 0xbf58476d1ce4e5b9
    z = (z ^ (z >> 27)) *% 0x94d049bb133111eb
    return z ^ (z >> 31)
}

struct Span {
    lo: int
    hi: int
}

FillKey(buf: mut []u8, pos: int, idx: int, tag: u8, st: mut u64, seed: u64): int {
    ab := Alph.AsBytes()
    *st = *st +% 0x9e3779b97f4a7c15
    z := Mix(*st)
    length := 8 + Cast[int](z % 17)
    u := (Cast[u64](idx) *% 0x9e3779b97f4a7c15) ^ (seed *% 0x517cc1b727220a95)
    for j in 0..7 {
        buf[pos + j] = ab[Cast[int]((u >> (5 * j)) & 31)]
    }
    buf[pos + 7] = tag
    var w := z >> 8
    for j in 8..length {
        if j == 16 {
            *st = *st +% 0x9e3779b97f4a7c15
            w = Mix(*st)
        }
        buf[pos + j] = ab[Cast[int](w & 31)]
        w = w >> 5
    }
    return pos + length
}

Round(h: &Arena, keys: []str, misses: []str, r: u64, key: HashKey): OutOfMemory!u64 {
    var a := try h.Sub()
    defer a.Free()
    var m := Map[str, u64].NewKeyed(&a, key)
    n := keys.Len()
    for i in 0..n {
        try m.Set(keys[i], Cast[u64](i) +% r)
    }
    var sum: u64 = 0
    for i in 0..n {
        k := keys[i]
        if v := m.Get(&k) {
            sum = sum +% *v
        }
        miss := misses[i]
        if m.Has(&miss) {
            sum = sum +% 1
        }
        if i % 2 == 0 {
            _ = m.Remove(&k)
        }
    }
    for i in 0..n {
        k := keys[i]
        if m.Has(&k) {
            sum = sum +% 3
        }
    }
    return sum +% Cast[u64](m.Len())
}

export Main(sys: &System): Canceled|OutOfMemory|Utf8Error|ParseError|WriteError!void {
    n := Cast[int](try sys.args[1].ParseInt())
    rounds := Cast[int](try sys.args[2].ParseInt())
    seed := Cast[u64](try sys.args[3].ParseInt())
    key := try HashKey.New(sys.io)
    buf := try sys.arena.Slice[u8](2 * n * 24)
    kspans := try sys.arena.Slice[Span](n)
    mspans := try sys.arena.Slice[Span](n)
    var st := seed
    var pos := 0
    for i in 0..n {
        lo := pos
        pos = FillKey(buf, pos, i, b'k', mut st, seed)
        kspans[i] = Span(lo: lo, hi: pos)
    }
    for i in 0..n {
        lo := pos
        pos = FillKey(buf, pos, i, b'm', mut st, seed)
        mspans[i] = Span(lo: lo, hi: pos)
    }
    text := try str.From(buf[0..pos])
    var klist := List[str].New(sys.arena)
    var mlist := List[str].New(sys.arena)
    for i in 0..n {
        sp := kspans[i]
        try klist.Push(text[sp.lo..sp.hi])
    }
    for i in 0..n {
        sp := mspans[i]
        try mlist.Push(text[sp.lo..sp.hi])
    }
    keys := klist.Done()[..]
    misses := mlist.Done()[..]
    var total: u64 = 0
    for r in 0..rounds {
        total = total +% try Round(sys.arena, keys, misses, Cast[u64](r), key)
    }
    var out := sys.Stdout()
    try Print(mut out, "{total}\n", (total))
}
