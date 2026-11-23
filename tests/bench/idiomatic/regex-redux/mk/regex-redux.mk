// regex-redux, Mk, idiomatic std: the task of ../README.md with std.regex
// (RE2-style linear-time DFA/Pike engine). Generates a deterministic FASTA DNA
// sequence with Homo sapiens and IUB sections, compiles the cleanup regex, 9
// classic DNA alternation/class match-count patterns, and 5 IUB-style
// replacement patterns once, then runs rounds passes folding all counts and
// sequence lengths into a wrapping u64 checksum.
// Usage: regex-redux <n> <rounds>. Prints the checksum.

import std.regex

const Im: int = 139968
const Ia: int = 3877
const Ic: int = 29573

const CleanPat: str = ">[^\\n]*\\n|\\n"

const CountPats: [9]str = [
    "agggtaaa|tttaccct",
    "[cgt]gggtaaa|tttaccc[acg]",
    "a[act]ggtaaa|tttacc[agt]t",
    "ag[act]gtaaa|tttac[agt]ct",
    "agg[act]taaa|ttta[agt]cct",
    "aggg[acg]aaa|ttt[cgt]ccct",
    "agggt[cgt]aa|tt[acg]accct",
    "agggta[cgt]a|t[acg]taccct",
    "agggtaa[cgt]|[acg]ttaccct",
]

const ReplPats: [5]str = [
    "tHa[Nt]",
    "aND|caN|Ha[DS]|WaS",
    "a[NSt]|BY",
    "<[^>]*>",
    "\\|[^|][^|]*\\|",
]

const ReplWith: [5]str = [
    "<4>",
    "<3>",
    "<2>",
    "|",
    "-",
]

NextRand(seed: mut int): int {
    *seed = (*seed * Ia + Ic) % Im
    return *seed
}

HomoBase(r: int): u8 {
    if r < 42404 {
        return b'a'
    }
    if r < 70116 {
        return b'c'
    }
    if r < 97766 {
        return b'g'
    }
    return b't'
}

IubBase(r: int): u8 {
    if r < 37791 {
        return b'a'
    }
    if r < 54587 {
        return b'c'
    }
    if r < 71383 {
        return b'g'
    }
    if r < 109174 {
        return b't'
    }
    if r < 111973 {
        return b'B'
    }
    if r < 114772 {
        return b'D'
    }
    if r < 117571 {
        return b'H'
    }
    if r < 120370 {
        return b'K'
    }
    if r < 123169 {
        return b'M'
    }
    if r < 125968 {
        return b'N'
    }
    if r < 128767 {
        return b'R'
    }
    if r < 131566 {
        return b'S'
    }
    if r < 134365 {
        return b'V'
    }
    if r < 137164 {
        return b'W'
    }
    return b'Y'
}

WriteSection(out: mut List[u8], header: str, count: int, iub: bool, seed: mut int): OutOfMemory!void {
    for c in header.AsBytes() {
        try out.Push(c)
    }
    var col := 0
    for _ in 0..count {
        r := NextRand(seed)
        ch := if iub { IubBase(r) } else { HomoBase(r) }
        try out.Push(ch)
        col += 1
        if col == 60 {
            try out.Push(b'\n')
            col = 0
        }
    }
    if col > 0 {
        try out.Push(b'\n')
    }
}

GenFasta(a: &Arena, n: int): OutOfMemory|Utf8Error!str from a {
    var out := List[u8].New(a)
    var seed := 42
    n1 := n / 2
    n2 := n - n1
    try WriteSection(mut out, ">ONE Homo sapiens frequency\n", n1, false, mut seed)
    try WriteSection(mut out, ">TWO IUB ambiguity codes\n", n2, true, mut seed)
    return try str.From(out.Done()[..])
}

Round(a: &Arena, raw: str, clean: &regex.Regex, counts: []regex.Regex, repls: []regex.Regex): OutOfMemory!u64 {
    seq := try clean.Replace(a, raw, "")
    var h: u64 = Cast[u64](raw.Len()) *% 131 +% Cast[u64](seq.Len())
    for i in 0..counts.Len() {
        ms := try counts[i].FindAll(a, seq)
        h = h *% 131 +% Cast[u64](ms.Len())
    }
    var cur := seq
    for i in 0..repls.Len() {
        cur = try repls[i].Replace(a, cur, ReplWith[i])
    }
    h = h *% 131 +% Cast[u64](cur.Len())
    return h
}

export Main(sys: &System): regex.Error|Utf8Error|OutOfMemory|ParseError|WriteError!void {
    n := Cast[int](try sys.args[1].ParseInt())
    rounds := Cast[int](try sys.args[2].ParseInt())
    raw := try GenFasta(sys.arena, n)
    clean := try regex.Compile(sys.arena, CleanPat)
    var clist := List[regex.Regex].New(sys.arena)
    for i in 0..9 {
        try clist.Push(try regex.Compile(sys.arena, CountPats[i]))
    }
    var rlist := List[regex.Regex].New(sys.arena)
    for i in 0..5 {
        try rlist.Push(try regex.Compile(sys.arena, ReplPats[i]))
    }
    counts := clist.Done()[..]
    repls := rlist.Done()[..]
    var a := try Arena.New(sys.heap)
    defer a.Free()
    var total: u64 = 0
    for _ in 0..rounds {
        total = total +% try Round(&a, raw, &clean, counts, repls)
        a.Reset()
    }
    var out := sys.Stdout()
    try Print(mut out, "{total}\n", (total))
}
