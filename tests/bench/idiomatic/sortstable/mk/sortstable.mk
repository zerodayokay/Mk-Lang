// sortstable, Mk, idiomatic std: the task of ../README.md with std's
// stable sort, Slice[u64].SortStable(sys.heap.Alloc(), Less)
// (std/core/coll_sort.mk; the comparator is a const fn argument, so the call
// is direct). Usage: sortstable <n> <rounds> <seed>. Prints the checksum.

Mix(x: u64): u64 {
    var z := x
    z = (z ^ (z >> 30)) *% 0xbf58476d1ce4e5b9
    z = (z ^ (z >> 27)) *% 0x94d049bb133111eb
    return z ^ (z >> 31)
}

Less(a: &u64, b: &u64): bool {
    return *a < *b
}

// Check is the weighted sum of the sorted keys, or 0xdead when they are
// not in order.
Check(xs: []u64): u64 {
    var sum: u64 = 0
    for i in 0..xs.Len() {
        if i > 0 && xs[i - 1] > xs[i] {
            return 0xdead
        }
        sum = sum +% xs[i] *% Cast[u64](i % 1000 + 1)
    }
    return sum
}

export Main(sys: &System): OutOfMemory|ParseError|WriteError!void {
    n := Cast[int](try sys.args[1].ParseInt())
    rounds := Cast[int](try sys.args[2].ParseInt())
    var st := Cast[u64](try sys.args[3].ParseInt())
    xs := try sys.arena.Slice[u64](n)
    var total: u64 = 0
    for _ in 0..rounds {
        for p in 0..4 {
            for i in 0..n {
                if p == 0 {
                    st = st +% 0x9e3779b97f4a7c15
                    xs[i] = Mix(st)
                } else if p == 1 {
                    xs[i] = Cast[u64](n - i)
                } else if p == 2 {
                    xs[i] = Cast[u64](i % 1000)
                } else {
                    st = st +% 0x9e3779b97f4a7c15
                    xs[i] = Mix(st) >> 60
                }
            }
            try xs.SortStable(sys.heap.Alloc(), Less)
            total = total +% Check(xs[..])
        }
    }
    var out := sys.Stdout()
    try Print(mut out, "{total}\n", (total))
}
