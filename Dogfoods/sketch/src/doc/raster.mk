// Why: the picture must be the same bytes on every machine and backend, so
// that a script's result can be checked by a hash and a GPU readback can be
// compared with it exactly.
//
// What: Paint (the document to RGBA8 pixels), PaintTail (the newest piece
// of the stroke being drawn), HashHex (SHA-256 of the pixels) and
// ToImage.
//
// How: integer arithmetic only: each segment is walked with Bresenham's
// steps and a filled disc of the stroke's radius is stamped at each step,
// clipped to the canvas; a clear stroke fills with white.

import std.encoding
import std.hash
import std.image

/// Paints d into px, which holds width * height * 4 bytes of RGBA8, top
/// row first; the open stroke is painted too. Panics (code 1) when px has
/// the wrong length. O(sum over strokes of points * radius²); allocates
/// nothing.
export Paint(d: &Doc, px: mut []u8) {
    if px.Len() != d.width * d.height * 4 {
        Panic("sketch: pixel buffer size")
    }
    Fill(px, 255, 255, 255)
    for i in 0..d.strokes.Len() {
        PaintStroke(d, &d.strokes[i], px)
    }
}

Fill(px: mut []u8, r: u8, g: u8, b: u8) {
    var o := 0
    for o < px.Len() {
        px[o] = r
        px[o + 1] = g
        px[o + 2] = b
        px[o + 3] = 255
        o += 4
    }
}

PaintStroke(d: &Doc, s: &Stroke, px: mut []u8) {
    if s.clear {
        Fill(px, 255, 255, 255)
        return
    }
    n := s.points.Len()
    if n == 0 {
        return
    }
    Stamp(d, s, s.points[0], px)
    for k in 1..n {
        Segment(d, s, s.points[k - 1], s.points[k], px)
    }
}

// Segment stamps the disc at every Bresenham step from a (exclusive) to b
// (inclusive).
Segment(d: &Doc, s: &Stroke, a: Point, b: Point, px: mut []u8) {
    dx := if b.x > a.x { b.x - a.x } else { a.x - b.x }
    dy := if b.y > a.y { a.y - b.y } else { b.y - a.y }
    sx := if a.x < b.x { 1 } else { -1 }
    sy := if a.y < b.y { 1 } else { -1 }
    var err := dx + dy
    var x := a.x
    var y := a.y
    for x != b.x || y != b.y {
        e2 := 2 * err
        if e2 >= dy {
            err += dy
            x += sx
        }
        if e2 <= dx {
            err += dx
            y += sy
        }
        Stamp(d, s, Point(x: x, y: y), px)
    }
}

// Stamp paints the disc of s.radius centred on c, clipped to the canvas.
Stamp(d: &Doc, s: &Stroke, c: Point, px: mut []u8) {
    r := s.radius
    rr := r * r
    for oy in -r..r + 1 {
        y := c.y + oy
        if y < 0 || y >= d.height {
            continue
        }
        for ox in -r..r + 1 {
            x := c.x + ox
            if x < 0 || x >= d.width || ox * ox + oy * oy > rr {
                continue
            }
            o := (y * d.width + x) * 4
            px[o] = s.r
            px[o + 1] = s.g
            px[o + 2] = s.b
            px[o + 3] = 255
        }
    }
}

/// Paints only the newest piece of the open stroke into px, already holding
/// the rest of the picture: its first stamp, or its last segment. Painting
/// is idempotent, so a repeated call changes nothing. O(segment * radius²);
/// allocates nothing.
export PaintTail(d: &Doc, px: mut []u8) {
    if !d.drawing {
        return
    }
    s := &d.strokes[d.strokes.Len() - 1]
    n := s.points.Len()
    if n == 1 {
        Stamp(d, s, s.points[0], px)
    } else if n > 1 {
        Segment(d, s, s.points[n - 2], s.points[n - 1], px)
    }
}

/// The SHA-256 of px in lower-case hex. Errors: OutOfMemory. O(px.Len());
/// allocates the 64-byte text in a.
export HashHex(a: &Arena, px: []u8): encoding.Error|OutOfMemory!str from a {
    sum := hash.Sha256(px)
    return try encoding.HexEncode(a, sum[..])
}

/// px as an RGBA8 image of d's size, borrowing px. O(1); allocates nothing.
export ToImage(d: &Doc, px: []u8): image.Image from px {
    return image.Image(width: d.width, height: d.height, pixels: image.Pixels.Rgba8, data: px)
}
