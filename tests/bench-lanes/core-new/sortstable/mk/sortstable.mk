// sortstable, Mk: the task of ../README.md with std's Slice.SortStable
// over a HeapList of (key, id) records. Usage: sortstable <n> <rounds>
// <seed>. Prints the checksum.

struct Rec {
    key: u32
    id: u32
}

Mix(x: u64): u64 {
    var z := x
    z = (z ^ (z >> 30)) *% 0xbf58476d1ce4e5b9
    z = (z ^ (z >> 27)) *% 0x94d049bb133111eb
    return z ^ (z >> 31)
}

// Fold samples the sorted records: every 1,024th id times its index, and
// the last record's key and id.
Fold(xs: &HeapList[Rec]): u64 {
    n := xs.Len()
    var sum: u64 = Cast[u64](xs[n - 1].key) *% 1000003 +% Cast[u64](xs[n - 1].id)
    var i := 0
    for i < n {
        sum = sum +% Cast[u64](xs[i].id) *% Cast[u64](i)
        i += 1024
    }
    return sum
}

export Main(sys: &System): OutOfMemory|ParseError|WriteError!void {
    n := Cast[int](try sys.args[1].ParseInt())
    rounds := Cast[int](try sys.args[2].ParseInt())
    seed := Cast[u64](try sys.args[3].ParseInt())
    a := sys.heap.Alloc()
    var xs := HeapList[Rec].New(a)
    onerr xs.Free(a)
    for _ in 0..n {
        try xs.Push(a, Rec(key: 0, id: 0))
    }
    var total: u64 = 0
    for r in 0..rounds {
        var st := seed +% Cast[u64](r) *% 0x5555555555555555
        for i in 0..n {
            st = st +% 0x9e3779b97f4a7c15
            xs[i] = Rec(key: Trunc[u32](Mix(st) % 1000), id: Trunc[u32](Cast[u64](i)))
        }
        try xs.SortStable(a, fn(p: &Rec, q: &Rec): bool { return p.key < q.key })
        total = total +% Fold(&xs)
    }
    xs.Free(a)
    var out := sys.Stdout()
    try Print(mut out, "{total}\n", (total))
}
