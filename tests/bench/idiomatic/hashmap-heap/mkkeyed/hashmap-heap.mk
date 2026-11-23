// hashmap-heap, Mk keyed, idiomatic std: the mk column's program (../mk)
// with HeapMap.NewKeyed on sys.heap.Alloc() under a random key from
// HashKey.New (SipHash-1-3, DoS-resistant peer of Rust's RandomState).
// Usage: hashmap-heap <n> <rounds> <seed>. Prints the checksum.

Mix(x: u64): u64 {
    var z := x
    z = (z ^ (z >> 30)) *% 0xbf58476d1ce4e5b9
    z = (z ^ (z >> 27)) *% 0x94d049bb133111eb
    return z ^ (z >> 31)
}

Round(a: &Alloc, n: int, seed: u64, key: HashKey): OutOfMemory!u64 {
    var m := HeapMap[u64, u64].NewKeyed(a, key)
    defer m.Free(a)
    var st := seed
    for i in 0..n {
        st = st +% 0x9e3779b97f4a7c15
        try m.Set(a, Mix(st), Trunc[u64](i))
    }
    var sum: u64 = 0
    st = seed
    for i in 0..n {
        st = st +% 0x9e3779b97f4a7c15
        k := Mix(st)
        if v := m.Get(&k) {
            sum = sum +% *v
        }
        miss := Mix(st ^ 0x5555555555555555)
        if mv := m.Get(&miss) {
            _ = mv
            sum = sum +% 1
        }
        if i % 2 == 0 {
            _ = m.Remove(&k)
        }
    }
    st = seed
    for _ in 0..n {
        st = st +% 0x9e3779b97f4a7c15
        k := Mix(st)
        if kv := m.Get(&k) {
            _ = kv
            sum = sum +% 3
        }
    }
    return sum +% Trunc[u64](m.Len())
}

export Main(sys: &System): Canceled|OutOfMemory|ParseError|WriteError!void {
    n := Cast[int](try sys.args[1].ParseInt())
    rounds := Cast[int](try sys.args[2].ParseInt())
    seed := Cast[u64](try sys.args[3].ParseInt())
    a := sys.heap.Alloc()
    key := try HashKey.New(sys.io)
    var total: u64 = 0
    for r in 0..rounds {
        total = total +% try Round(a, n, seed +% Trunc[u64](r), key)
    }
    var out := sys.Stdout()
    try Print(mut out, "{total}\n", (total))
}
