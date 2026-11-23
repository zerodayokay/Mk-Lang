// Why: `textkit columns` aligns fields as a terminal shows them.
// What: Columns, Table and Layout.
// How: widths come from terminal columns, so wide and combining characters
// line up as narrow ones do.

import std.cli
import std.text
import src.input

/// `textkit columns`: lines up separated fields into columns by their width
/// on a terminal, so wide and combining characters line up as narrow ones do.
struct ColumnsOptions {
    /// The field separator (default a tab)
    export separator: ?str
    /// Spaces between columns (default 2)
    export gap: ?int
    /// Align every column but the first to the right
    export right: bool
    /// The most bytes to read, all inputs together (default 8388608)
    export limit: ?int
    /// Files to read; none, or -, is standard input
    export files: []str
}

gen cli.Args(ColumnsOptions)

struct Table {
    arena: &Arena
    rows: List[List[str]]
    limit: int
    separator: str
}

/// Runs `textkit columns` with `args`. Returns the exit status.
export Columns(c: &input.Ctx, s: mut input.Streams, stdin: mut Reader, args: []str into c): int {
    synopsis := "columns [--separator s] [--gap n] [--right] [--limit bytes] [file...]"
    if input.WantsHelp(args) {
        return input.Help(s, synopsis, ColumnsOptions.Usage)
    }
    opts := ColumnsOptions.Parse(c.arena, args) else e {
        return input.BadArgs(s, "columns", args, &e, ColumnsOptions.Usage)
    }
    separator := opts.separator else { "\t" }
    if separator.Len() == 0 {
        Print(mut s.err, "textkit columns: the separator is empty\n") else {}
        return input.Usage
    }
    gap := opts.gap else { 2 }
    if gap < 0 {
        Print(mut s.err, "textkit columns: --gap must be 0 or more, not {gap}\n", (gap)) else {}
        return input.Usage
    }
    limit := input.Size(s, "columns", "--limit", opts.limit, input.WholeLimit, 0) else {
        return input.Usage
    }
    var table := Table(arena: c.arena, rows: List[List[str]].New(c.arena), limit: limit, separator: separator)
    status := input.ForEach(c, s, stdin, opts.files, input.BufferSize, mut table, fn(ctx: &input.Ctx, state: mut Table, streams: mut input.Streams, reader: mut Reader, name_of: str): input.Error!void { try Collect(state, reader) })
    if status != input.Ok {
        return status
    }
    Layout(c.arena, mut s.out, &table, gap, opts.right) else e {
        input.Report(s, "columns", &e)
        return match e {
            OutOfMemory { input.NoMemory }
            _ { input.Failed }
        }
    }
    return input.Ok
}

/// Reads the rest of `r` whole and splits it into rows and fields. The
/// limit is shared by every input.
Collect(t: mut Table, r: mut Reader): input.Error!void {
    content := try input.ReadText(t.arena, r, t.limit)
    t.limit -= content.Len()
    for line in content.Lines() {
        var row := List[str].New(t.arena)
        for field in line.Split(t.separator) {
            try row.Push(field)
        }
        try t.rows.Push(row)
    }
}

/// Writes `t` with each column as wide as its widest field, in terminal
/// columns, and `gap` spaces between columns. O(fields); allocates the
/// column widths and one line in `a`.
Layout(a: &Arena, w: mut Writer, t: &Table, gap: int, right: bool): OutOfMemory|WriteError!void {
    var widths := List[int].New(a)
    for row in &t.rows {
        for i, field in row {
            fw := text.Width(field)
            if i == widths.Len() {
                try widths.Push(fw)
            } else if fw > widths[i] {
                widths[i] = fw
            }
        }
    }
    var line := Text.New(a)
    for row in &t.rows {
        line.Clear()
        last := row.Len() - 1
        for i, field in row {
            pad := widths[i] - text.Width(field)
            if right && i > 0 {
                try Spaces(mut line, pad)
                try line.Push(field)
            } else {
                try line.Push(field)
                if i < last {
                    try Spaces(mut line, pad)
                }
            }
            if i < last {
                try Spaces(mut line, gap)
            }
        }
        try w.Str(line.Str())
        try w.Str("\n")
    }
}

Spaces(t: mut Text, n: int): OutOfMemory!void {
    for _ in 0..n {
        try t.PushChar(' ')
    }
}
