// sortstr, Mk, idiomatic std: the task of ../README.md with std's unstable
// sort, Slice[str].Sort(StrLess) (pdqsort; str's < compares bytes), over
// views into one buffer: the text is generated into a byte slice, checked
// once by str.From, and each key is a str view of it (no copy per key).
// Each round pushes the views into a List[str] in a sub-arena (the other
// columns copy them into a preallocated array), sorts its elements and
// checks them.
// Usage: sortstr <n> <rounds> <seed>. Prints the checksum.

Mix(x: u64): u64 {
    var z := x
    z = (z ^ (z >> 30)) *% 0xbf58476d1ce4e5b9
    z = (z ^ (z >> 27)) *% 0x94d049bb133111eb
    return z ^ (z >> 31)
}

Fnv(w: str): u64 {
    var h: u64 = 0xcbf29ce484222325
    for c in w.AsBytes() {
        h = (h ^ u64(c)) *% 0x100000001b3
    }
    return h
}

StrLess(a: &str, b: &str): bool {
    return *a < *b
}

// Check is the weighted sum of the keys' FNV-1a hashes in sorted order, or
// 0xdead when they are not in order.
Check(xs: []str): u64 {
    var sum: u64 = 0
    for i in 0..xs.Len() {
        if i > 0 && xs[i - 1] > xs[i] {
            return 0xdead
        }
        sum = sum +% Fnv(xs[i]) *% Cast[u64](i % 1000 + 1)
    }
    return sum
}

// Round copies the keys in their unsorted order into a list in a
// sub-arena, sorts it and checks it.
Round(h: &Arena, orig: &List[str]): OutOfMemory!u64 {
    var a := try h.Sub()
    defer a.Free()
    var w := List[str].New(&a)
    for i in 0..orig.Len() {
        try w.Push(orig[i])
    }
    work := w.Done()
    work.Sort(StrLess)
    return Check(work[..])
}

export Main(sys: &System): Utf8Error|OutOfMemory|ParseError|WriteError!void {
    n := Cast[int](try sys.args[1].ParseInt())
    rounds := Cast[int](try sys.args[2].ParseInt())
    var st := Cast[u64](try sys.args[3].ParseInt())
    buf := try sys.arena.Slice[u8](n * 16)
    offs := try sys.arena.Slice[int](n + 1)
    var p := 0
    for i in 0..n {
        offs[i] = p
        st = st +% 0x9e3779b97f4a7c15
        l := 1 + Cast[int](Mix(st) % 16)
        for _ in 0..l {
            st = st +% 0x9e3779b97f4a7c15
            buf[p] = Trunc[u8](0x61 + Mix(st) % 26)
            p += 1
        }
    }
    offs[n] = p
    text := try str.From(buf[0..p])
    var orig := List[str].New(sys.arena)
    for i in 0..n {
        try orig.Push(text[offs[i]..offs[i + 1]])
    }
    var total: u64 = 0
    for _ in 0..rounds {
        total = total +% try Round(sys.arena, &orig)
    }
    var out := sys.Stdout()
    try Print(mut out, "{total}\n", (total))
}
