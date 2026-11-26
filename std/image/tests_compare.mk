// Why: std.image's Compare must accurately measure max channel difference,
// pixels over tolerance, and isolated vs edge-adjacent differences.
// What: unit tests for Compare.
// How: compares identical, within-tolerance, edge-line, solid-patch and
// mismatched-size images.

RunCompare(a: &Image, b_buf: []u8, w: int, h: int, px: Pixels, tol: int): Error!Difference {
    b := Image(width: w, height: h, pixels: px, data: b_buf)
    return try Compare(a, &b, tol)
}

test "Compare measures exact, within-tolerance, edge and isolated diffs" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    w := 5
    h := 5
    n := w * h * 4
    a_buf := try ar.Slice[u8](n)
    b_buf := try ar.Slice[u8](n)
    for i in 0..n {
        a_buf[i] = 100
        b_buf[i] = 100
    }
    img_a := Image(width: w, height: h, pixels: Pixels.Rgba8, data: a_buf[..])
    // Identical.
    d0 := try RunCompare(&img_a, b_buf[..], w, h, Pixels.Rgba8, 2)
    Assert(d0.max == 0 && d0.over == 0 && d0.isolated == 0 && d0.pixels == 25)

    // Within tolerance (delta 2 on channel 1).
    b_buf[(2 * w + 2) * 4 + 1] = 102
    d1 := try RunCompare(&img_a, b_buf[..], w, h, Pixels.Rgba8, 2)
    Assert(d1.max == 2 && d1.over == 0 && d1.isolated == 0)

    // 1-pixel vertical edge at x = 2: every pixel has a horizontal neighbour within tolerance.
    for y in 0..h {
        b_buf[(y * w + 2) * 4] = 110
    }
    d2 := try RunCompare(&img_a, b_buf[..], w, h, Pixels.Rgba8, 2)
    Assert(d2.max == 10 && d2.over == 5 && d2.isolated == 0)

    // Reset to identical, then make a 3x3 block (x in 1..4, y in 1..4) all over tolerance -> center (2, 2) has all 4 neighbours over tolerance -> isolated == 1.
    for i in 0..n {
        b_buf[i] = 100
    }
    for y in 1..4 {
        for x in 1..4 {
            b_buf[(y * w + x) * 4] = 120
        }
    }
    d3 := try RunCompare(&img_a, b_buf[..], w, h, Pixels.Rgba8, 2)
    Assert(d3.max == 20 && d3.over == 9 && d3.isolated == 1)

    // SizeMismatch.
    img_small := Image(width: 4, height: 5, pixels: Pixels.Rgba8, data: a_buf[0..4 * 5 * 4])
    _ = Compare(&img_a, &img_small, 2) else e {
        match e {
            Invalid(why: w_err) {
                Assert(WhyCode(w_err) == WhyCode(Why.SizeMismatch))
            }
            _ { Assert(false) }
        }
    }
    img_rgb := Image(width: w, height: h, pixels: Pixels.Rgb8, data: a_buf[0..w * h * 3])
    _ = Compare(&img_a, &img_rgb, 2) else e {
        match e {
            Invalid(why: w_err) {
                Assert(WhyCode(w_err) == WhyCode(Why.SizeMismatch))
            }
            _ { Assert(false) }
        }
    }
}
