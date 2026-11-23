// Why: `textkit uniq` drops repeated lines.
// What: Uniq, UniqOptions and UniqState.
// How: adjacent repeats, or all repeats through a set of seen keys.

import std.cli
import src.input

/// `textkit uniq`: drops repeated lines, adjacent ones or all of them.
struct UniqOptions {
    /// Print how many times each line occurred
    export count: bool
    /// Print only lines that occur more than once
    export repeated: bool
    /// Print only lines that occur once
    export single: bool
    /// Compare case-folded keys
    export fold: bool
    /// Compare this field, from 1, instead of the whole line
    export key: ?int
    /// The field separator for --key (default whitespace)
    export separator: ?str
    /// Drop every repeat, not only adjacent ones, keeping first occurrences in order
    export global: bool
    /// Lines end with a NUL byte instead of a newline
    export zero: bool
    /// The read buffer, in bytes (default 16384)
    export buffer: ?int
    /// Files to read; none, or -, is standard input
    export files: []str
}

gen cli.Args(UniqOptions)

/// A distinct line in global mode: its first occurrence and how many there were.
struct Seen {
    line: str
    key: str
    count: int
}

/// Which runs to print.
struct Filter {
    count: bool
    repeated: bool
    single: bool
    zero: bool
}

struct UniqState {
    arena: &Arena
    alloc: &Alloc
    keying: Keying
    filter: Filter
    global: bool
    size: int
    // adjacent mode: the line of the current run, its key and its length
    line: HeapText
    key: HeapText
    run: int
    // global mode: distinct lines in input order, and their indices sorted by key
    seen: List[Seen]
    order: List[int]
}

/// Runs `textkit uniq` with `args`. Returns the exit status.
export Uniq(c: &input.Ctx, s: mut input.Streams, stdin: mut Reader, args: []str into c): int {
    synopsis := "uniq [--count] [--repeated | --single] [--fold] [--key n [--separator s]] [--global] [--zero] [file...]"
    if input.WantsHelp(args) {
        return input.Help(s, synopsis, UniqOptions.Usage)
    }
    opts := UniqOptions.Parse(c.arena, args) else e {
        return input.BadArgs(s, "uniq", args, &e, UniqOptions.Usage)
    }
    if opts.repeated && opts.single {
        Print(mut s.err, "textkit uniq: --repeated and --single exclude each other\n") else {}
        return input.Usage
    }
    keying := CheckKeying(s, "uniq", opts.key, opts.separator, opts.fold) else {
        return input.Usage
    }
    alloc := c.heap.Alloc()
    size := input.Size(s, "uniq", "--buffer", opts.buffer, input.BufferSize, 1) else {
        return input.Usage
    }
    var st := UniqState(
        arena: c.arena,
        alloc: alloc,
        keying: keying,
        filter: Filter(count: opts.count, repeated: opts.repeated, single: opts.single, zero: opts.zero),
        global: opts.global,
        size: size,
        line: HeapText.New(alloc),
        key: HeapText.New(alloc),
        run: 0,
        seen: List[Seen].New(c.arena),
        order: List[int].New(c.arena),
    )
    var status := input.ForEach(c, s, stdin, opts.files, st.size, mut st, fn(ctx: &input.Ctx, state: mut UniqState, streams: mut input.Streams, reader: mut Reader, name_of: str): input.Error!void { try UniqInput(ctx, state, streams, reader) })
    Finish(mut s.out, &st) else e {
        input.Report(s, "uniq", &e)
        status = input.Failed
    }
    UniqState(line, key, seen, order, ..) := st
    _ = seen
    _ = order
    line.Free(alloc)
    key.Free(alloc)
    return status
}

/// Writes one run of `n` equal lines if `f` lets it through.
EmitRun(w: mut Writer, line: str, n: int, f: &Filter): WriteError!void {
    if n == 0 || (f.repeated && n < 2) || (f.single && n > 1) {
        return
    }
    if f.count {
        try Print(w, "{n:>7} ", (n))
    }
    try w.Str(line)
    try End(w, f.zero)
}

EmitDistinct(w: mut Writer, seen: &List[Seen], f: &Filter): WriteError!void {
    for x in seen {
        try EmitRun(w, x.line, x.count, f)
    }
}

/// Takes one line: extends the current run, or ends it and starts another.
UniqState.Take(mut self, scratch: &Arena, s: mut input.Streams, line: str): input.Error!void {
    if self.global {
        try self.TakeGlobal(line)
        return
    }
    key := try KeyOf(scratch, line, &self.keying)
    if self.run > 0 && key == self.key.Str() {
        self.run += 1
        return
    }
    try EmitRun(mut s.out, self.line.Str(), self.run, &self.filter)
    try self.line.Set(self.alloc, line)
    try self.key.Set(self.alloc, key)
    self.run = 1
}

/// Takes one line in global mode. The distinct keys are kept sorted, so a
/// line is found by binary search: O(log n) compares and O(n) to insert.
UniqState.TakeGlobal(mut self, line: str): input.Error!void {
    held := try self.arena.Dup(line)
    key := try KeyOf(self.arena, held, &self.keying)
    seen := &self.seen
    i := self.order.Search(&key, fn(e: &int, k: &str): bool { return seen[*e].key < *k })
    if i < self.order.Len() && self.seen[self.order[i]].key == key {
        self.seen[self.order[i]].count += 1
        return
    }
    try self.order.Insert(i, self.seen.Len())
    try self.seen.Push(Seen(line: held, key: key, count: 1))
}

UniqInput(c: &input.Ctx, st: mut UniqState, s: mut input.Streams, r: mut Reader): input.Error!void {
    var scratch := try Arena.New(c.heap)
    defer scratch.Free()
    if st.filter.zero {
        var records := try input.RecordSource.New(c.arena, r, 0, st.size)
        for {
            line := try records.Next(&scratch) else { break }
            try st.Take(&scratch, s, line)
            scratch.Reset()
        }
        return
    }
    var lines := try input.LineSource.New(c.arena, r, st.size)
    for {
        line := try lines.Next(&scratch) else { break }
        try st.Take(&scratch, s, line)
        scratch.Reset()
    }
}

/// Writes what is still held at the end of the input: the last run, or
/// every distinct line in global mode.
Finish(w: mut Writer, st: &UniqState): WriteError!void {
    if st.global {
        try EmitDistinct(w, &st.seen, &st.filter)
        return
    }
    try EmitRun(w, st.line.Str(), st.run, &st.filter)
}
