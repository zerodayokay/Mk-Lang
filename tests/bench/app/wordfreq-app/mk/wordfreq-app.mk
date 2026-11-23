// wordfreq-app, Mk, std: the task of ../README.md. Streams the input file
// through a 64 KiB buffer via fs.File.Read, tokenizes ASCII words ([A-Za-z],
// folded to lowercase) carrying partial words across chunk boundaries, counts
// frequencies in std's Map[str, i64] (fast fixed hash, arena-owned keys copied
// on first insert only), sorts unique entries by (count desc, word asc) via
// List.Sort, and prints total, unique and top-K word counts.
// Usage: wordfreq-app <file> <top_k> [<max_words>].

import std.fs

type AppError = fs.Error | OutOfMemory | ParseError | WriteError | Utf8Error

struct Entry {
    word: str
    count: i64
}

RecordWord(a: &Arena into counts, counts: mut Map[str, i64], wbuf: []u8): OutOfMemory|Utf8Error!void {
    w := try str.From(wbuf)
    if p := counts.GetMut(&w) {
        *p += 1
        return
    }
    owned := try a.Dup(w)
    try counts.Set(owned, 1)
}

export Main(sys: &System): AppError!void {
    if sys.args.Len() < 3 || sys.args.Len() > 4 {
        var e := sys.Stderr()
        try Print(mut e, "usage: wordfreq-app <file> <top_k> [<max_words>]\n")
        fail ParseError
    }
    top_k := Cast[int](try sys.args[2].ParseInt())
    var limit: i64 = -1
    if sys.args.Len() == 4 {
        limit = try sys.args[3].ParseInt()
    }

    var a := try Arena.New(sys.heap)
    defer a.Free()

    var counts := Map[str, i64].New(&a)
    buf := try a.Slice[u8](65536)
    var wbuf := List[u8].New(&a)
    var total: i64 = 0

    var f := try fs.Open(sys.io, sys.args[1])
    onerr f.Abandon()

    var done := false
    for !done {
        n := try f.Read(sys.io, buf)
        if n == 0 {
            break
        }
        var i := 0
        for i < n {
            c := buf[i]
            if (c >= 97 && c <= 122) || (c >= 65 && c <= 90) {
                if wbuf.Len() == 0 {
                    // Fast path: scan a contiguous lowercase/ASCII word inside buf.
                    start := i
                    var has_upper := c >= 65 && c <= 90
                    i += 1
                    for i < n {
                        b := buf[i]
                        if b >= 97 && b <= 122 {
                            i += 1
                        } else if b >= 65 && b <= 90 {
                            has_upper = true
                            i += 1
                        } else {
                            break
                        }
                    }
                    if i == n {
                        // Word touches chunk boundary; carry into wbuf.
                        for j in start..i {
                            var ch := buf[j]
                            if ch >= 65 && ch <= 90 {
                                ch = ch + 32
                            }
                            try wbuf.Push(ch)
                        }
                    } else {
                        if !has_upper {
                            try RecordWord(&a, mut counts, buf[start..i])
                        } else {
                            wbuf.Clear()
                            for j in start..i {
                                var ch := buf[j]
                                if ch >= 65 && ch <= 90 {
                                    ch = ch + 32
                                }
                                try wbuf.Push(ch)
                            }
                            try RecordWord(&a, mut counts, wbuf.Items())
                            wbuf.Clear()
                        }
                        total += 1
                        if limit >= 0 && total >= limit {
                            done = true
                            break
                        }
                    }
                } else {
                    var ch := c
                    if ch >= 65 && ch <= 90 {
                        ch = ch + 32
                    }
                    try wbuf.Push(ch)
                    i += 1
                }
            } else {
                if wbuf.Len() > 0 {
                    try RecordWord(&a, mut counts, wbuf.Items())
                    wbuf.Clear()
                    total += 1
                    if limit >= 0 && total >= limit {
                        done = true
                        break
                    }
                }
                i += 1
            }
        }
    }
    if !done && wbuf.Len() > 0 {
        try RecordWord(&a, mut counts, wbuf.Items())
        total += 1
    }
    try f.Close(sys.io)

    var entries := List[Entry].New(&a)
    var it := counts.Iter()
    for e := it.Next() {
        try entries.Push(Entry(word: *e.key, count: *e.value))
    }
    entries.Sort(fn(x: &Entry, y: &Entry): bool {
        if x.count != y.count {
            return x.count > y.count
        }
        return x.word < y.word
    })

    uniq := entries.Len()
    var k := top_k
    if k > uniq {
        k = uniq
    }
    var out := sys.Stdout()
    try Print(mut out, "total {total}\nunique {uniq}\n", (total, uniq))
    for i in 0..k {
        en := entries[i]
        try Print(mut out, "{w} {c}\n", (w: en.word, c: en.count))
    }
}
