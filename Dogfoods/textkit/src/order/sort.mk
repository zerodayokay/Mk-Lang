// Why: `textkit sort` sorts lines stably by a key.
// What: Sort, SortOptions and SortState.
// How: collects every line, then a stable sort by the key.

import std.cli
import std.mem
import src.input

/// `textkit sort`: sorts lines, stably, by a key that may be folded or numeric.
struct SortOptions {
    /// Compare case-folded keys, so Straße and STRASSE are equal
    export fold: bool
    /// Compare keys as numbers: integers exactly, then decimals; words first
    export numeric: bool
    /// Sort in reverse order; equal lines keep their input order
    export reverse: bool
    /// Print only the first of each run of equal keys
    export unique: bool
    /// Compare this field, from 1, instead of the whole line
    export key: ?int
    /// The field separator for --key (default whitespace)
    export separator: ?str
    /// Lines end with a NUL byte instead of a newline
    export zero: bool
    /// Hold the lines in at most this many bytes, and fail past it
    export memory: ?int
    /// The read buffer, in bytes (default 16384)
    export buffer: ?int
    /// Files to read; none, or -, is standard input
    export files: []str
}

gen cli.Args(SortOptions)

/// One line held for sorting, with its key worked out once.
struct Entry {
    line: HeapText
    key: HeapText
    number: Number
}

impl Free for Entry {
    Free(self, a: &Alloc) {
        Entry(line, key, ..) := self
        line.Free(a)
        key.Free(a)
    }
}

struct SortState {
    alloc: &Alloc
    entries: HeapList[Entry]
    keying: Keying
    numeric: bool
    zero: bool
    size: int
}

/// Runs `textkit sort` with `args`. Returns the exit status.
export Sort(c: &input.Ctx, s: mut input.Streams, stdin: mut Reader, args: []str into c): int {
    synopsis := "sort [--fold] [--numeric] [--reverse] [--unique] [--key n [--separator s]] [--zero] [--memory bytes] [file...]"
    if input.WantsHelp(args) {
        return input.Help(s, synopsis, SortOptions.Usage)
    }
    opts := SortOptions.Parse(c.arena, args) else e {
        return input.BadArgs(s, "sort", args, &e, SortOptions.Usage)
    }
    keying := CheckKeying(s, "sort", opts.key, opts.separator, opts.fold) else {
        return input.Usage
    }
    // 0 stands for no --memory: the lines then live on the heap
    limit := if opts.memory.IsSome() {
        input.Size(s, "sort", "--memory", opts.memory, 0, 1) else {
            return input.Usage
        }
    } else {
        0
    }
    // the lines live on the heap, or in a fixed buffer when --memory bounds them
    buf := c.arena.Slice[u8](limit) else {
        return input.NoMemory
    }
    fixed := mem.Fixed.New(buf)
    alloc := if limit > 0 { fixed.Alloc() } else { c.heap.Alloc() }
    size := input.Size(s, "sort", "--buffer", opts.buffer, input.BufferSize, 1) else {
        return input.Usage
    }
    return SortWith(c, s, stdin, &opts, keying, alloc, limit, size)
}

// Reads, sorts and writes the lines in `alloc`. A function of its own, so
// the state's stores reach the allocator `alloc` and stop there, whatever
// buffer a budget's allocator was made over.
SortWith(c: &input.Ctx, s: mut input.Streams, stdin: mut Reader, opts: &SortOptions, keying: Keying into alloc, alloc: &Alloc, limit: int, size: int): int {
    var st := SortState(
        alloc: alloc,
        entries: HeapList[Entry].New(alloc),
        keying: keying,
        numeric: opts.numeric,
        zero: opts.zero,
        size: size,
    )
    var status := input.ForEach(c, s, stdin, opts.files, st.size, mut st, fn(ctx: &input.Ctx, state: mut SortState, streams: mut input.Streams, reader: mut Reader, name_of: str): input.Error!void { try SortInput(ctx, state, reader) })
    if status == input.Ok {
        numeric := opts.numeric
        reverse := opts.reverse
        st.entries.SortStable(alloc, fn(a: &Entry, b: &Entry): bool {
            return if reverse { Before(b, a, numeric) } else { Before(a, b, numeric) }
        }) else {
            st.entries.SortStableInPlace(fn(a: &Entry, b: &Entry): bool {
                return if reverse { Before(b, a, numeric) } else { Before(a, b, numeric) }
            })
        }
        Emit(mut s.out, &st.entries, opts.unique, opts.zero, numeric) else e {
            input.Report(s, "sort", &e)
            status = input.Failed
        }
    } else if status == input.NoMemory && limit > 0 {
        Print(mut s.err, "textkit sort: the input does not fit in --memory {limit} bytes\n", (limit)) else {}
    }
    SortState(entries, ..) := st
    entries.Free(alloc)
    return status
}

/// The keying the options ask for, or `none` after reporting why not.
export CheckKeying(s: mut input.Streams, command: str, key: ?int, separator: ?str, fold: bool): ?Keying from separator {
    field := key else { 0 }
    if field < 0 || (key.IsSome() && field == 0) {
        Print(mut s.err, "textkit {command}: --key counts fields from 1, not {field}\n", (command, field)) else {}
        return none
    }
    if sep := separator {
        if sep.Len() == 0 {
            Print(mut s.err, "textkit {command}: the separator is empty\n", (command)) else {}
            return none
        }
        if field == 0 {
            Print(mut s.err, "textkit {command}: --separator needs --key\n", (command)) else {}
            return none
        }
    }
    return some(Keying(field: field, separator: separator, fold: fold))
}

/// Whether `a` sorts before `b`.
Before(a: &Entry, b: &Entry, numeric: bool): bool {
    if numeric {
        return NumberBefore(&a.number, &b.number)
    }
    return a.key.Str() < b.key.Str()
}

/// Writes the sorted lines, each ended by a newline or a NUL, and with
/// `unique` only the first of each run of equal keys.
Emit(w: mut Writer, entries: &HeapList[Entry], unique: bool, zero: bool, numeric: bool): WriteError!void {
    for i in 0..entries.Len() {
        e := &entries[i]
        if unique && i > 0 && Same(&entries[i - 1], e, numeric) {
            continue
        }
        try w.Str(e.line.Str())
        try End(w, zero)
    }
}

/// Writes the end of a line: a newline, or a NUL byte with `zero`.
export End(w: mut Writer, zero: bool): WriteError!void {
    if zero {
        nul: [1]u8 = [0]
        try w.Bytes(nul[..])
        return
    }
    try w.Str("\n")
}

/// Reads every line of `r` into `st.entries`.
SortInput(c: &input.Ctx, st: mut SortState, r: mut Reader): input.Error!void {
    var scratch := try Arena.New(c.heap)
    defer scratch.Free()
    if st.zero {
        var records := try input.RecordSource.New(c.arena, r, 0, st.size)
        for {
            line := try records.Next(&scratch) else { break }
            try st.Add(&scratch, line)
            scratch.Reset()
        }
        return
    }
    var lines := try input.LineSource.New(c.arena, r, st.size)
    for {
        line := try lines.Next(&scratch) else { break }
        try st.Add(&scratch, line)
        scratch.Reset()
    }
}

/// Copies `line` and its key into `st`'s allocator.
SortState.Add(mut self, scratch: &Arena, line: str): OutOfMemory!void {
    key := try KeyOf(scratch, line, &self.keying)
    number := if self.numeric { NumberOf(key) } else { Number.None }
    var e := Entry(line: HeapText.New(self.alloc), key: HeapText.New(self.alloc), number: number)
    onerr e.Free(self.alloc)
    try e.line.Push(self.alloc, line)
    try e.key.Push(self.alloc, key)
    try self.entries.Push(self.alloc, e)
}

/// Whether neither entry sorts before the other.
Same(a: &Entry, b: &Entry, numeric: bool): bool {
    return !Before(a, b, numeric) && !Before(b, a, numeric)
}
