// Why: golden tests comparing GPU output across WARP, D3D12 and Vulkan
// need a tolerance-aware image diff that distinguishes edge-antialiasing
// differences from isolated pixel errors.
// What: Difference and Compare.
// How: scans both tightly-packed pixel buffers in place with zero
// allocation, tracking max channel delta, pixels over tolerance, and
// pixels over tolerance whose 4-neighbours are all over tolerance.

/// How two images differ.
export struct Difference {
    /// The largest channel difference.
    export max: int
    /// Pixels with a channel over tolerance.
    export over: int
    /// Pixels over tolerance none of whose 4-neighbours is within it.
    export isolated: int
    export pixels: int
}

// PixelMaxDiff returns the largest per-channel absolute difference at (x, y).
PixelMaxDiff(a: []u8, b: []u8, x: int, y: int, width: int, bpp: int): int {
    off := (y * width + x) * bpp
    var best := 0
    for c in 0..bpp {
        va := int(a[off + c])
        vb := int(b[off + c])
        d := if va >= vb { va - vb } else { vb - va }
        if d > best {
            best = d
        }
    }
    return best
}

/// Compares a and b channel by channel. Errors: Invalid(SizeMismatch).
/// O(pixels); allocates nothing.
export Compare(a: &Image, b: &Image, tolerance: int): Error!Difference {
    if a.width != b.width || a.height != b.height || !PixelsEq(a.pixels, b.pixels) {
        fail Invalid(why: Why.SizeMismatch)
    }
    bpp := BytesPerPixel(a.pixels)
    if bpp == 0 || a.width <= 0 || a.height <= 0 {
        fail Invalid(why: Why.BadImage)
    }
    max_i64: i64 = 9223372036854775807
    w64 := Cast[i64](a.width)
    h64 := Cast[i64](a.height)
    bpp64 := Cast[i64](bpp)
    if w64 > max_i64 / bpp64 || h64 > max_i64 / (w64 * bpp64) {
        fail Invalid(why: Why.BadImage)
    }
    need := w64 * h64 * bpp64
    if Cast[i64](a.data.Len()) != need || Cast[i64](b.data.Len()) != need {
        fail Invalid(why: Why.BadImage)
    }
    tol := if tolerance < 0 { 0 } else { tolerance }
    w := a.width
    h := a.height
    var max_d := 0
    var over := 0
    var isolated := 0
    for y in 0..h {
        for x in 0..w {
            d := PixelMaxDiff(a.data, b.data, x, y, w, bpp)
            if d > max_d {
                max_d = d
            }
            if d > tol {
                over += 1
                has_within := (x > 0 && PixelMaxDiff(a.data, b.data, x - 1, y, w, bpp) <= tol) ||
                    (x + 1 < w && PixelMaxDiff(a.data, b.data, x + 1, y, w, bpp) <= tol) ||
                    (y > 0 && PixelMaxDiff(a.data, b.data, x, y - 1, w, bpp) <= tol) ||
                    (y + 1 < h && PixelMaxDiff(a.data, b.data, x, y + 1, w, bpp) <= tol)
                if !has_within {
                    isolated += 1
                }
            }
        }
    }
    return Difference(max: max_d, over: over, isolated: isolated, pixels: w * h)
}
