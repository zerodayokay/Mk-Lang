// strbuild, Mk, idiomatic std: the task of ../README.md with std's
// growable UTF-8 string builder Text in one scratch arena reset after each
// document. Each document starts from an empty Text and appends literals,
// slices, repeated chunks and formatted integers (through Text.Writer and
// Print), then hashes the built bytes with FNV-1a 64 plus their length.
// Usage: strbuild <docs> <lines>. Prints the checksum.

const Words: [16]str = [
    "alpha", "bravo", "charlie", "delta",
    "echo", "foxtrot", "golf", "hotel",
    "india", "juliet", "kilo", "lima",
    "mike", "november", "oscar", "papa",
]

const Src: str = "the quick brown fox jumps over the lazy dog 0123456789 ABCDEFGHIJKLMNOPQRSTUVWXYZ "

Fnv(b: []u8): u64 {
    var h: u64 = 0xcbf29ce484222325
    for c in b {
        h = (h ^ u64(c)) *% 0x100000001b3
    }
    return h
}

PushInt(t: mut Text, v: int): WriteError!void {
    var w := t.Writer()
    try Print(mut w, "{v}", (v))
}

BuildDoc(a: &Arena, d: int, lines: int): OutOfMemory|WriteError!u64 {
    var t := Text.New(a)
    for i in 0..lines {
        x := d * 131 + i * 17
        try t.Push("rec[")
        try PushInt(mut t, i)
        try t.Push("] ")
        try t.Push(Words[x & 15])
        try t.Push(":")
        off := (x * 7) % 40
        slen := 8 + (x % 25)
        try t.Push(Src[off..off + slen])
        reps := (i & 3) + 1
        for _ in 0..reps {
            try t.Push("-=")
        }
        try t.Push(" #")
        try PushInt(mut t, x)
        try t.Push("\n")
    }
    b := t.Str().AsBytes()
    return Fnv(b) +% Cast[u64](b.Len())
}

export Main(sys: &System): OutOfMemory|ParseError|WriteError!void {
    docs := Cast[int](try sys.args[1].ParseInt())
    lines := Cast[int](try sys.args[2].ParseInt())
    var a := try Arena.New(sys.heap)
    defer a.Free()
    var total: u64 = 0
    for d in 0..docs {
        total = total +% try BuildDoc(&a, d, lines)
        a.Reset()
    }
    var out := sys.Stdout()
    try Print(mut out, "{total}\n", (total))
}
