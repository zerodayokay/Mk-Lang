// fasta, Mk, checked: the reference algorithm of ../README.md (the Benchmarks
// Game's fasta generator, hashed by line into an i64 checksum instead of
// writing to stdout). Every index is bounds checked and every integer
// operation is checked (overflow class "never").
// Usage: fasta <n>. Prints the checksum.

// tools/bench flips this line in a copy for the counting build.
const BenchCount: bool = false

const Im: int = 139968
const Ia: int = 3877
const Ic: int = 29573
const LineLen: int = 60

struct Freq {
    ch: u8
    p: f64
}

MakeCumulative(tab: mut []Freq) {
    var cp := 0.0
    for i in 0..tab.Len() {
        cp = cp + tab[i].p
        tab[i].p = cp
    }
    tab[tab.Len() - 1].p = 1.0
}

NextRand(seed: mut int): f64 {
    *seed = (*seed * Ia + Ic) % Im
    return Cast[f64](*seed) / Cast[f64](Im)
}

LineSum(buf: []u8, m: int): int {
    var s := 0
    for k in 0..m {
        s += int(buf[k]) * (k + 1)
    }
    return s
}

RepeatFasta(alu: []u8, count: int): int {
    var buf: [61]u8 = [0; 61]
    alu_len := alu.Len()
    var pos := 0
    var rem := count
    var total := 0
    for rem > 0 {
        m := if rem < LineLen { rem } else { LineLen }
        for k in 0..m {
            buf[k] = alu[pos]
            pos += 1
            if pos == alu_len {
                pos = 0
            }
        }
        buf[m] = 10
        total += LineSum(buf[0..m + 1], m + 1)
        rem -= m
    }
    return total
}

RandomFasta(tab: []Freq, count: int, seed: mut int, probes: mut int): int {
    var buf: [61]u8 = [0; 61]
    var rem := count
    var total := 0
    for rem > 0 {
        m := if rem < LineLen { rem } else { LineLen }
        for k in 0..m {
            r := NextRand(seed)
            var j := 0
            for {
                if BenchCount {
                    *probes += 1
                }
                if tab[j].p >= r {
                    break
                }
                j += 1
            }
            buf[k] = tab[j].ch
        }
        buf[m] = 10
        total += LineSum(buf[0..m + 1], m + 1)
        rem -= m
    }
    return total
}

export Main(sys: &System): ParseError|WriteError!void {
    n := Cast[int](try sys.args[1].ParseInt())
    alu := "GGCCGGGCGCGGTGGCTCACGCCTGTAATCCCAGCACTTTGGGAGGCCGAGGCGGGCGGATCACCTGAGGTCAGGAGTTCGAGACCAGCCTGGCCAACATGGTGAAACCCCGTCTCTACTAAAAATACAAAAATTAGCCGGGCGTGGTGGCGCGCGCCTGTAATCCCAGCTACTCGGGAGGCTGAGGCAGGAGAATCGCTTGAACCCGGGAGGCGGAGGTTGCAGTGAGCCGAGATCGCGCCACTGCACTCCAGCCTGGGCGACAGAGCGAGACTCCGTCTCAAAAA"
    var iub: [15]Freq = [
        Freq(ch: b'a', p: 0.27),
        Freq(ch: b'c', p: 0.12),
        Freq(ch: b'g', p: 0.12),
        Freq(ch: b't', p: 0.27),
        Freq(ch: b'B', p: 0.02),
        Freq(ch: b'D', p: 0.02),
        Freq(ch: b'H', p: 0.02),
        Freq(ch: b'K', p: 0.02),
        Freq(ch: b'M', p: 0.02),
        Freq(ch: b'N', p: 0.02),
        Freq(ch: b'R', p: 0.02),
        Freq(ch: b'S', p: 0.02),
        Freq(ch: b'V', p: 0.02),
        Freq(ch: b'W', p: 0.02),
        Freq(ch: b'Y', p: 0.02),
    ]
    var homo: [4]Freq = [
        Freq(ch: b'a', p: 0.3029549426680),
        Freq(ch: b'c', p: 0.1979883004921),
        Freq(ch: b'g', p: 0.1975473066391),
        Freq(ch: b't', p: 0.3015094502008),
    ]
    MakeCumulative(mut iub[..])
    MakeCumulative(mut homo[..])
    var seed := 42
    var probes := 0
    var total := 0
    total += RepeatFasta(alu.AsBytes(), 2 * n)
    total += RandomFasta(iub[..], 3 * n, mut seed, mut probes)
    total += RandomFasta(homo[..], 5 * n, mut seed, mut probes)
    var out := sys.Stdout()
    try Print(mut out, "{total}\n", (total))
    if BenchCount {
        try Print(mut out, "counts probes={probes}\n", (probes))
    }
}
