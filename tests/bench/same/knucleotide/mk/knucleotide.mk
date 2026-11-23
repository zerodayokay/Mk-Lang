// knucleotide, Mk, checked: the reference algorithm of ../README.md (2-bit
// packed DNA k-mers in a fixed open-addressed linear-probing hash table,
// Fibonacci hash, 70% load factor). Wrapping u64 hashing and checksum
// (overflow class "wraps"). Every slice access is bounds checked.
// Usage: knucleotide <n>. Prints the checksum.

// tools/bench flips this line in a copy for the counting build.
const BenchCount: bool = false

const Im: int = 139968
const Ia: int = 3877
const Ic: int = 29573
const FibMul: u64 = 0x9e3779b97f4a7c15

struct Entry {
    key: u64
    val: u64
}

struct Table {
    entries: mut []Entry
    cap: int
    shift: u32
    len: int
}

TableNew(a: &Arena): OutOfMemory!Table from a {
    entries := try a.Slice[Entry](64)
    return Table(entries: entries, cap: 64, shift: 6, len: 0)
}

TableGrow(a: &Arena into tab, tab: mut Table, rehashes: mut u64): OutOfMemory!void {
    if BenchCount {
        *rehashes = *rehashes +% 1
    }
    new_cap := tab.cap * 2
    new_shift := tab.shift + 1
    next := try a.Slice[Entry](new_cap)
    mask := new_cap - 1
    sh := 64 - new_shift
    for i in 0..tab.cap {
        e := tab.entries[i]
        if e.val != 0 {
            var slot := Cast[int]((e.key *% FibMul) >> sh) & mask
            for {
                if next[slot].val == 0 {
                    next[slot] = e
                    break
                }
                slot = (slot + 1) & mask
            }
        }
    }
    tab.entries = next
    tab.cap = new_cap
    tab.shift = new_shift
}

TableInc(a: &Arena into tab, tab: mut Table, key: u64, probes: mut u64, rehashes: mut u64): OutOfMemory!void {
    if (tab.len + 1) * 10 > tab.cap * 7 {
        try TableGrow(a, tab, rehashes)
    }
    mask := tab.cap - 1
    var slot := Cast[int]((key *% FibMul) >> (64 - tab.shift)) & mask
    for {
        if BenchCount {
            *probes = *probes +% 1
        }
        v := tab.entries[slot].val
        if v == 0 {
            tab.entries[slot] = Entry(key: key, val: 1)
            tab.len += 1
            return
        }
        if tab.entries[slot].key == key {
            tab.entries[slot].val = v +% 1
            return
        }
        slot = (slot + 1) & mask
    }
}

TableGet(tab: &Table, key: u64, probes: mut u64): u64 {
    mask := tab.cap - 1
    var slot := Cast[int]((key *% FibMul) >> (64 - tab.shift)) & mask
    for {
        if BenchCount {
            *probes = *probes +% 1
        }
        v := tab.entries[slot].val
        if v == 0 {
            return 0
        }
        if tab.entries[slot].key == key {
            return v
        }
        slot = (slot + 1) & mask
    }
}

PackSeq(s: str): u64 {
    var key: u64 = 0
    bs := s.AsBytes()
    for i in 0..bs.Len() {
        c := bs[i]
        var code: u64 = 3
        if c == b'A' {
            code = 0
        } else if c == b'C' {
            code = 1
        } else if c == b'G' {
            code = 2
        }
        key = (key << 2) | code
    }
    return key
}

CountK(heap: &Heap, seq: []u8, k: int, probes: mut u64, rehashes: mut u64): OutOfMemory!u64 {
    var a := try Arena.New(heap)
    defer a.Free()
    var tab := try TableNew(&a)
    n := seq.Len()
    if n >= k {
        mask := (u64(1) << Cast[u32](2 * k)) -% 1
        var key: u64 = 0
        for i in 0..k - 1 {
            key = (key << 2) | u64(seq[i])
        }
        for i in k - 1..n {
            key = ((key << 2) | u64(seq[i])) & mask
            try TableInc(&a, mut tab, key, probes, rehashes)
        }
    }
    var h: u64 = Cast[u64](tab.len)
    var max_val: u64 = 0
    for i in 0..tab.cap {
        v := tab.entries[i].val
        if v > max_val {
            max_val = v
        }
    }
    h = h *% 31 +% max_val
    if k == 1 {
        for c in 0..4 {
            h = h *% 31 +% TableGet(&tab, Cast[u64](c), probes)
        }
    } else if k == 2 {
        for c in 0..16 {
            h = h *% 31 +% TableGet(&tab, Cast[u64](c), probes)
        }
    } else if k == 3 {
        h = h *% 31 +% TableGet(&tab, PackSeq("GGT"), probes)
    } else if k == 4 {
        h = h *% 31 +% TableGet(&tab, PackSeq("GGTA"), probes)
    } else if k == 6 {
        h = h *% 31 +% TableGet(&tab, PackSeq("GGTATT"), probes)
    } else if k == 12 {
        h = h *% 31 +% TableGet(&tab, PackSeq("GGTATTTTAATT"), probes)
    } else if k == 18 {
        h = h *% 31 +% TableGet(&tab, PackSeq("GGTATTTTAATTTATAGT"), probes)
    }
    return h
}

export Main(sys: &System): OutOfMemory|ParseError|WriteError!void {
    n := Cast[int](try sys.args[1].ParseInt())
    rounds := Cast[int](try sys.args[2].ParseInt())
    seq := try sys.arena.Slice[u8](n)
    p0 := 0.3029549426680
    p1 := p0 + 0.1979883004921
    p2 := p1 + 0.1975473066391
    var seed := 42
    for i in 0..n {
        seed = (seed * Ia + Ic) % Im
        r := Cast[f64](seed) / Cast[f64](Im)
        if r < p0 {
            seq[i] = 0
        } else if r < p1 {
            seq[i] = 1
        } else if r < p2 {
            seq[i] = 2
        } else {
            seq[i] = 3
        }
    }
    ks: [7]int = [1, 2, 3, 4, 6, 12, 18]
    var probes: u64 = 0
    var rehashes: u64 = 0
    var total: u64 = 0
    for _ in 0..rounds {
        for i in 0..7 {
            total = total *% 31 +% try CountK(sys.heap, seq[..], ks[i], mut probes, mut rehashes)
        }
    }
    var out := sys.Stdout()
    try Print(mut out, "{total}\n", (total))
    if BenchCount {
        try Print(mut out, "counts probes={probes} rehashes={rehashes}\n", (probes, rehashes))
    }
}
