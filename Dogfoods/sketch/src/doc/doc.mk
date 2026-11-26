// Why: a drawing is its strokes, so undo and redo are moves between two
// lists, and the picture is always the strokes painted in order.
//
// What: Point, Stroke, Doc with Begin, Extend, End, Cancel, Clear, Undo,
// Redo, SetColor, SetRadius, Type, Backspace, CaptionText and Counts, and
// the limits a document holds to.
//
// How: everything lives in one arena; a stroke being drawn is the last of
// the strokes, marked by drawing, until End commits it; Begin empties the
// redo list. Clear is a stroke too, so it can be undone.

/// The largest canvas side, in pixels.
export const MaxSide: int = 2048

/// The most strokes a document keeps (undone ones included).
export const MaxStrokes: int = 4096

/// The most points in one stroke.
export const MaxPoints: int = 65536

/// The largest brush radius, in pixels.
export const MaxRadius: int = 64

/// The longest caption, in bytes.
export const MaxCaption: int = 256

/// A canvas pixel position; may lie outside the canvas, where it paints
/// nothing.
export struct Point {
    export x: int
    export y: int
}

/// One brush stroke, or (clear) a fill of the whole canvas with white.
export struct Stroke {
    export clear: bool
    export r: u8
    export g: u8
    export b: u8
    export radius: int
    export points: List[Point]
}

/// A drawing: its size, its strokes in order (the last one still being
/// drawn while drawing is true), the strokes undone (most recent last), the
/// brush, and a caption.
export struct Doc {
    export width: int
    export height: int
    export strokes: List[Stroke]
    export drawing: bool
    export redo: List[Stroke]
    export r: u8
    export g: u8
    export b: u8
    export radius: int
    export caption: List[u8]
}

/// An empty width by height document with a black brush of radius 2.
/// Panics (code 1) when a side is outside 1..MaxSide. O(1); allocates nothing
/// until it is drawn on.
export NewDoc(a: &Arena, width: int, height: int): Doc from a {
    if width < 1 || width > MaxSide || height < 1 || height > MaxSide {
        Panic("sketch: canvas side out of range")
    }
    return Doc(
        width: width,
        height: height,
        strokes: List[Stroke].New(a),
        drawing: false,
        redo: List[Stroke].New(a),
        r: 0,
        g: 0,
        b: 0,
        radius: 2,
        caption: List[u8].New(a),
    )
}

/// Starts a stroke at p with the current brush, ending one still open, and
/// empties the redo list. A document holding MaxStrokes starts none.
/// Errors: OutOfMemory. O(1) amortised; allocates the stroke's points in a.
export Doc.Begin(mut self, a: &Arena into self, p: Point): OutOfMemory!void {
    self.End()
    if self.strokes.Len() >= MaxStrokes {
        return
    }
    var pts := List[Point].New(a)
    try pts.Push(p)
    self.redo.Clear()
    try self.strokes.Push(Stroke(clear: false, r: self.r, g: self.g, b: self.b, radius: self.radius, points: pts))
    self.drawing = true
}

/// Adds p to the open stroke; does nothing without one, when p repeats the
/// last point, or once the stroke holds MaxPoints. Errors: OutOfMemory.
/// O(1) amortised; grows the point list.
export Doc.Extend(mut self, p: Point): OutOfMemory!void {
    if !self.drawing {
        return
    }
    k := self.strokes.Len() - 1
    n := self.strokes[k].points.Len()
    last := self.strokes[k].points[n - 1]
    if n >= MaxPoints || (last.x == p.x && last.y == p.y) {
        return
    }
    try self.strokes[k].points.Push(p)
}

/// Commits the open stroke, if any. O(1).
export Doc.End(mut self) {
    self.drawing = false
}

/// Drops the open stroke, as when the window loses focus mid-stroke. O(1).
export Doc.Cancel(mut self) {
    if self.drawing {
        _ = self.strokes.Pop()
        self.drawing = false
    }
}

/// Commits a clear of the whole canvas. Errors: OutOfMemory. O(1) amortised;
/// allocates an empty point list in a.
export Doc.Clear(mut self, a: &Arena into self): OutOfMemory!void {
    self.End()
    if self.strokes.Len() >= MaxStrokes {
        return
    }
    self.redo.Clear()
    try self.strokes.Push(Stroke(clear: true, r: 255, g: 255, b: 255, radius: 0, points: List[Point].New(a)))
}

/// Moves the last stroke to the redo list; false when there is none.
/// Errors: OutOfMemory. O(1) amortised.
export Doc.Undo(mut self): OutOfMemory!bool {
    self.End()
    if s := self.strokes.Pop() {
        try self.redo.Push(s)
        return true
    }
    return false
}

/// Moves the most recently undone stroke back; false when there is none.
/// Errors: OutOfMemory. O(1) amortised.
export Doc.Redo(mut self): OutOfMemory!bool {
    self.End()
    if s := self.redo.Pop() {
        try self.strokes.Push(s)
        return true
    }
    return false
}

/// Sets the brush colour. O(1); allocates nothing.
export Doc.SetColor(mut self, r: u8, g: u8, b: u8) {
    self.r = r
    self.g = g
    self.b = b
}

/// Sets the brush radius, clamped to 1..MaxRadius. O(1); allocates nothing.
export Doc.SetRadius(mut self, radius: int) {
    self.radius = if radius < 1 { 1 } else if radius > MaxRadius { MaxRadius } else { radius }
}

/// Appends ch to the caption in UTF-8; a caption at MaxCaption bytes keeps
/// what it has. Errors: OutOfMemory. O(1) amortised.
export Doc.Type(mut self, ch: char): OutOfMemory!void {
    var buf: [4]u8 = [0; 4]
    n := Utf8(ch, mut buf)
    if self.caption.Len() + n > MaxCaption {
        return
    }
    try self.caption.Extend(buf[0..n])
}

/// Removes the caption's last character. O(1); allocates nothing.
export Doc.Backspace(mut self) {
    var n := self.caption.Len()
    for n > 0 {
        n -= 1
        // A UTF-8 continuation byte is 10xxxxxx.
        if self.caption[n] & 0xC0 != 0x80 {
            break
        }
    }
    self.caption.Truncate(n)
}

/// The caption as text. O(1); allocates nothing.
export Doc.CaptionText(&self): str from self {
    return str.From(self.caption[..]) else { "" }
}

/// The number of strokes drawn (clears included) and undone. O(1).
export Doc.Counts(&self): (strokes: int, undone: int) {
    return (strokes: self.strokes.Len(), undone: self.redo.Len())
}

// Utf8 writes ch's UTF-8 encoding into buf and returns its length.
Utf8(ch: char, buf: mut [4]u8): int {
    v := u32(ch)
    if v < 0x80 {
        buf[0] = Trunc[u8](v)
        return 1
    }
    if v < 0x800 {
        buf[0] = Trunc[u8](0xC0 | (v >> 6))
        buf[1] = Trunc[u8](0x80 | (v & 0x3F))
        return 2
    }
    if v < 0x10000 {
        buf[0] = Trunc[u8](0xE0 | (v >> 12))
        buf[1] = Trunc[u8](0x80 | ((v >> 6) & 0x3F))
        buf[2] = Trunc[u8](0x80 | (v & 0x3F))
        return 3
    }
    buf[0] = Trunc[u8](0xF0 | (v >> 18))
    buf[1] = Trunc[u8](0x80 | ((v >> 12) & 0x3F))
    buf[2] = Trunc[u8](0x80 | ((v >> 6) & 0x3F))
    buf[3] = Trunc[u8](0x80 | (v & 0x3F))
    return 4
}
