// Why: a drawing is saved as its strokes, not its pixels, so a reloaded file
// repaints to the same bytes and keeps every committed stroke undoable.
//
// What: Error, BadFile, Save (a document to text) and Load (text to a
// document), and the file format below.
//
// How: line-based text, one record per line:
//   sketch 1 WIDTH HEIGHT
//   stroke R G B RADIUS X,Y X,Y ...
//   clear
//   caption TEXT
// Load checks every number against the limits of doc.mk and names the line
// of the first fault; neither the stroke being drawn nor the redo list is
// saved.

/// Errors Load returns.
export open type Error = BadFile

/// The text is not a sketch file: line is 1-based, msg says what is wrong.
export error BadFile(line: int, msg: str)

/// d's committed strokes and caption as sketch text. Errors: OutOfMemory.
/// O(points); allocates the text in a.
export Save(a: &Arena, d: &Doc): OutOfMemory![]u8 from a {
    var out := List[u8].New(a)
    head := try a.Fmt("sketch 1 {w} {h}\n", (w: d.width, h: d.height))
    try out.Extend(head.AsBytes())
    n := if d.drawing { d.strokes.Len() - 1 } else { d.strokes.Len() }
    for i in 0..n {
        s := &d.strokes[i]
        if s.clear {
            try out.Extend("clear\n".AsBytes())
            continue
        }
        rec := try a.Fmt("stroke {r} {g} {b} {rad}", (r: s.r, g: s.g, b: s.b, rad: s.radius))
        try out.Extend(rec.AsBytes())
        for k in 0..s.points.Len() {
            p := s.points[k]
            pt := try a.Fmt(" {x},{y}", (x: p.x, y: p.y))
            try out.Extend(pt.AsBytes())
        }
        try out.Extend("\n".AsBytes())
    }
    if d.caption.Len() > 0 {
        try out.Extend("caption ".AsBytes())
        try out.Extend(d.caption[..])
        try out.Extend("\n".AsBytes())
    }
    return out.Done()[..]
}

// Num is s as an integer in lo..hi.
Num(s: str, lo: int, hi: int): ?int {
    v := s.ParseInt() else { return none }
    if v < i64(lo) || v > i64(hi) {
        return none
    }
    return some(int(v))
}

// PointOf is X,Y; a point may lie off the canvas by up to one side.
PointOf(s: str): ?Point {
    parts := s.SplitOnce(",") else { return none }
    x := Num(parts.before, -MaxSide, 2 * MaxSide) else { return none }
    y := Num(parts.after, -MaxSide, 2 * MaxSide) else { return none }
    return some(Point(x: x, y: y))
}

// Header reads `sketch 1 WIDTH HEIGHT` into a new document.
Header(a: &Arena, l: str, line: int): Error!Doc from a {
    var w := l.Words()
    _ = w.Next()
    ver := w.Next() else { fail BadFile(line: line, msg: "the header is sketch 1 WIDTH HEIGHT") }
    if !ver.Eq(&"1") {
        fail BadFile(line: line, msg: "unknown version")
    }
    ws := w.Next() else { fail BadFile(line: line, msg: "the header is sketch 1 WIDTH HEIGHT") }
    hs := w.Next() else { fail BadFile(line: line, msg: "the header is sketch 1 WIDTH HEIGHT") }
    width := Num(ws, 1, MaxSide) else { fail BadFile(line: line, msg: "a side is 1..2048") }
    height := Num(hs, 1, MaxSide) else { fail BadFile(line: line, msg: "a side is 1..2048") }
    return NewDoc(a, width, height)
}

// StrokeRecord reads `stroke R G B RADIUS X,Y...` into d.
StrokeRecord(a: &Arena into d, d: mut Doc, w: mut Words, line: int): Error|OutOfMemory!void {
    var nums: [4]int = [0; 4]
    for k in 0..4 {
        f := w.Next() else { fail BadFile(line: line, msg: "a stroke is stroke R G B RADIUS X,Y...") }
        hi := if k < 3 { 255 } else { MaxRadius }
        lo := if k < 3 { 0 } else { 1 }
        nums[k] = Num(f, lo, hi) else { fail BadFile(line: line, msg: "a colour is 0..255, a radius 1..64") }
    }
    d.SetColor(Trunc[u8](nums[0]), Trunc[u8](nums[1]), Trunc[u8](nums[2]))
    d.SetRadius(nums[3])
    first := w.Next() else { fail BadFile(line: line, msg: "a stroke has a point") }
    p0 := PointOf(first) else { fail BadFile(line: line, msg: "a point is X,Y") }
    try d.Begin(a, p0)
    var count := 1
    for {
        f := w.Next() else { break }
        count += 1
        if count > MaxPoints {
            fail BadFile(line: line, msg: "too many points")
        }
        p := PointOf(f) else { fail BadFile(line: line, msg: "a point is X,Y") }
        try d.Extend(p)
    }
    d.End()
}

/// Reads sketch text into a document in a. Errors: Error (BadFile),
/// OutOfMemory. O(text); allocates the document in a.
export Load(a: &Arena, text: str): Error|OutOfMemory!Doc from a {
    var lines := text.Lines()
    var line_no := 0
    var first := ""
    for {
        raw := lines.Next() else { break }
        line_no += 1
        first = raw.Trim()
        if first.Len() > 0 {
            break
        }
    }
    if !first.StartsWith("sketch ") {
        fail BadFile(line: line_no, msg: "the header is sketch 1 WIDTH HEIGHT")
    }
    var d := try Header(a, first, line_no)
    for {
        raw := lines.Next() else { break }
        line_no += 1
        l := raw.Trim()
        if l.Len() == 0 {
            continue
        }
        if l.StartsWith("caption ") {
            for ch in l[8..l.Len()].Scalars() {
                try d.Type(ch)
            }
            continue
        }
        var w := l.Words()
        head := w.Next() else { continue }
        if d.strokes.Len() >= MaxStrokes {
            fail BadFile(line: line_no, msg: "too many strokes")
        }
        if head.Eq(&"clear") {
            try d.Clear(a)
        } else if head.Eq(&"stroke") {
            try StrokeRecord(a, mut d, mut w, line_no)
        } else if head.Eq(&"sketch") {
            fail BadFile(line: line_no, msg: "a second header")
        } else {
            fail BadFile(line: line_no, msg: "unknown record")
        }
    }
    return d
}
