// utf8scan, Mk, idiomatic std: the task of ../README.md with std's UTF-8
// validation (str.From) and scalar iteration (str.Scalars) over a generated
// mixed-script UTF-8 buffer (ASCII, Latin-1, Greek, Cyrillic, CJK, and
// 4-byte emoji). Each round validates the byte buffer with str.From, decodes
// every Unicode scalar value with Scalars, folds count and codepoint sum,
// and toggles one ASCII letter so no round can be hoisted.
// Usage: utf8scan <n> <rounds>. Prints the checksum.

const Unit: str = "The quick brown fox jumps over 42 lazy dogs! Caf\u{00e9} na\u{00ef}ve r\u{00e9}sum\u{00e9} \u{00fc}ber\n\u{0391}\u{03bb}\u{03b3}\u{03cc}\u{03c1}\u{03b9}\u{03b8}\u{03bc}\u{03bf}\u{03c2} \u{03ba}\u{03b1}\u{03b9} \u{041f}\u{0440}\u{0438}\u{0432}\u{0435}\u{0442} \u{043c}\u{0438}\u{0440} 2026\n\u{65e5}\u{672c}\u{8a9e}\u{30c6}\u{30ad}\u{30b9}\u{30c8} \u{4e2d}\u{6587}\u{6d4b}\u{8bd5} \u{d55c}\u{ad6d}\u{c5b4} \u{6c34}\u{306f}\u{51b7}\u{305f}\u{3044}\nStatus: OK \u{1f600}\u{1f680}\u{1f30d}\u{1f525}\u{2728}\u{1f4bb}\u{1f389} end-of-record.\n"

export Main(sys: &System): OutOfMemory|Utf8Error|ParseError|WriteError!void {
    n := Cast[int](try sys.args[1].ParseInt())
    rounds := Cast[int](try sys.args[2].ParseInt())
    ub := Unit.AsBytes()
    ulen := ub.Len()
    buf := try sys.arena.Slice[u8](n * ulen)
    for i in 0..n {
        (mut buf[i * ulen..(i + 1) * ulen]).CopyFrom(ub)
    }
    var total: u64 = 0
    for r in 0..rounds {
        s := try str.From(buf[..])
        var count: u64 = 0
        var cpsum: u64 = 0
        var sc := s.Scalars()
        for c := sc.Next() {
            count = count +% 1
            cpsum = cpsum +% u64(u32(c))
        }
        total = total +% cpsum +% count
        idx := (r % n) * ulen
        buf[idx] = buf[idx] ^ 0x20
    }
    var out := sys.Stdout()
    try Print(mut out, "{total}\n", (total))
}
