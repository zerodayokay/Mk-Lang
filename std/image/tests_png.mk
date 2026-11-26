// Why: std.image's PNG encoder and decoder must round-trip every pixel
// format and refuse every malformed or oversized input without panicking.
// What: tests of EncodePng and DecodePng over valid, split-IDAT,
// multi-filter, hostile and oversized inputs.
// How: builds synthetic PNG chunks and tests both round-trips and every
// Error / Why variant.

import std.compress

// BuildTestPng constructs a PNG with the given IHDR fields, optional raw
// chunks, and IDAT payload(s).
BuildTestPng(a: &Arena, w: u32, h: u32, depth: u8, ctype: u8, comp: u8, filt: u8, inter: u8, idat_parts: [][]u8, extra_tag: u32, extra_data: []u8): OutOfMemory![]u8 from a {
    var total := 8 + 25 + 12
    for i in 0..idat_parts.Len() {
        total += 12 + idat_parts[i].Len()
    }
    if extra_tag != 0 {
        total += 12 + extra_data.Len()
    }
    out := try a.Slice[u8](total)
    for i in 0..8 {
        out[i] = PngSig[i]
    }
    var ihdr: [13]u8 = [0; 13]
    PutBe32(mut ihdr[..], 0, w)
    PutBe32(mut ihdr[..], 4, h)
    ihdr[8] = depth
    ihdr[9] = ctype
    ihdr[10] = comp
    ihdr[11] = filt
    ihdr[12] = inter
    var pos := 8
    pos = WriteChunk(out, pos, TagIhdr, ihdr[..])
    if extra_tag != 0 {
        pos = WriteChunk(out, pos, extra_tag, extra_data)
    }
    for i in 0..idat_parts.Len() {
        pos = WriteChunk(out, pos, TagIdat, idat_parts[i])
    }
    var empty: [0]u8 = []
    pos = WriteChunk(out, pos, TagIend, empty[..])
    return out[0..pos]
}

// AssertInvalidWhy checks that DecodePng(bytes, limits) fails with Invalid(want).
AssertInvalidWhy(a: &Arena, bytes: []u8, limits: DecodeLimits, want: Why) {
    _ = DecodePng(a, bytes, limits) else e {
        match e {
            Invalid(why: w) {
                Assert(WhyCode(w) == WhyCode(want))
                return
            }
            _ {
                Assert(false)
                return
            }
        }
    }
    Assert(false)
}

test "PNG round-trip for Gray8, Rgb8 and Rgba8" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    for px in [Pixels.Gray8, Pixels.Rgb8, Pixels.Rgba8] {
        bpp := BytesPerPixel(px)
        for dim in [[1, 1], [3, 5], [17, 13], [32, 24]] {
            w := dim[0]
            h := dim[1]
            n := w * h * bpp
            raw := try ar.Slice[u8](n)
            for i in 0..n {
                raw[i] = Trunc[u8]((i * 37 + i / 7) & 0xFF)
            }
            img := Image(width: w, height: h, pixels: px, data: raw[..])
            png := try EncodePng(&ar, img)
            dec := try DecodePng(&ar, png, DefaultDecodeLimits)
            Assert(dec.width == w && dec.height == h)
            Assert(PixelsEq(dec.pixels, px))
            Assert(dec.data.Len() == n)
            for i in 0..n {
                Assert(dec.data[i] == raw[i])
            }
        }
    }
}

test "DecodePng decodes all five PNG filters and split IDAT with ancillary chunk" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    w := 4
    h := 5
    bpp := 3
    row := w * bpp
    expected := try ar.Slice[u8](h * row)
    for y in 0..h {
        for x in 0..row {
            expected[y * row + x] = Trunc[u8]((y * 53 + x * 19 + 7) & 0xFF)
        }
    }
    scanlines := try ar.Slice[u8](h * (1 + row))
    for y in 0..h {
        f := Trunc[u8](y)
        scanlines[y * (1 + row)] = f
        for x in 0..row {
            cur := expected[y * row + x]
            left: u8 = if x >= bpp { expected[y * row + x - bpp] } else { 0 }
            up: u8 = if y > 0 { expected[(y - 1) * row + x] } else { 0 }
            up_left: u8 = if y > 0 && x >= bpp { expected[(y - 1) * row + x - bpp] } else { 0 }
            var pred: u8 = 0
            if f == 1 {
                pred = left
            } else if f == 2 {
                pred = up
            } else if f == 3 {
                pred = Trunc[u8]((int(left) + int(up)) >> 1)
            } else if f == 4 {
                pred = PaethPred(left, up, up_left)
            }
            scanlines[y * (1 + row) + 1 + x] = cur -% pred
        }
    }
    z := try compress.Zlib(&ar, scanlines[..])
    mid := z.Len() / 2
    parts := [z[0..mid], z[mid..z.Len()]]
    // Ancillary chunk 'tEXt' (0x74455874) before IDAT.
    note := "hi".AsBytes()
    png := try BuildTestPng(&ar, 4, 5, 8, 2, 0, 0, 0, parts[..], 0x74455874, note)
    dec := try DecodePng(&ar, png, DefaultDecodeLimits)
    Assert(dec.width == w && dec.height == h)
    Assert(dec.data.Len() == expected.Len())
    for i in 0..expected.Len() {
        Assert(dec.data[i] == expected[i])
    }
}

test "DecodePng enforces DecodeLimits exactly" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    px_data := try ar.Slice[u8](4 * 3 * 4)
    png := try EncodePng(&ar, Image(width: 4, height: 3, pixels: Pixels.Rgba8, data: px_data[..]))
    // Exact byte limit succeeds.
    exact := DecodeLimits(max_width: 4, max_height: 3, max_bytes: 48, sealed: 0)
    dec := try DecodePng(&ar, png, exact)
    Assert(dec.width == 4 && dec.height == 3)
    // One below max_width, max_height, or max_bytes fails TooLarge.
    for lim in [
        DecodeLimits(max_width: 3, max_height: 3, max_bytes: 48, sealed: 0),
        DecodeLimits(max_width: 4, max_height: 2, max_bytes: 48, sealed: 0),
        DecodeLimits(max_width: 4, max_height: 3, max_bytes: 47, sealed: 0),
        DecodeLimits(max_width: 0, max_height: 3, max_bytes: 48, sealed: 0),
    ] {
        _ = DecodePng(&ar, png, lim) else e {
            match e {
                TooLarge { continue }
                _ { Assert(false) }
            }
        }
        Assert(false)
    }
}

test "DecodePng refuses malformed and unsupported PNG inputs" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    var raw: [4]u8 = [10, 20, 30, 40]
    good := try EncodePng(&ar, Image(width: 1, height: 1, pixels: Pixels.Rgba8, data: raw[..]))

    // Truncated prefixes.
    var empty: [0]u8 = []
    AssertInvalidWhy(&ar, empty[..], DefaultDecodeLimits, Why.Truncated)
    AssertInvalidWhy(&ar, good[0..5], DefaultDecodeLimits, Why.Truncated)
    AssertInvalidWhy(&ar, good[0..good.Len() - 4], DefaultDecodeLimits, Why.Truncated)

    // Bad signature.
    bad_sig := try ar.Slice[u8](good.Len())
    bad_sig.CopyFrom(good)
    bad_sig[0] = 0
    AssertInvalidWhy(&ar, bad_sig[..], DefaultDecodeLimits, Why.BadSignature)

    // Bad CRC.
    bad_crc := try ar.Slice[u8](good.Len())
    bad_crc.CopyFrom(good)
    bad_crc[32] = bad_crc[32] ^ 1
    AssertInvalidWhy(&ar, bad_crc[..], DefaultDecodeLimits, Why.BadCrc)

    // Unsupported: interlaced, 16-bit, indexed, gray+alpha.
    var z0_in: [2]u8 = [0, 1]
    z0 := try compress.Zlib(&ar, z0_in[..])
    p0 := [z0]
    var no_extra: [0]u8 = []
    for bad_pair in [[8, 0, 1], [16, 0, 0], [8, 3, 0], [8, 4, 0]] {
        u := try BuildTestPng(&ar, 1, 1, Trunc[u8](bad_pair[0]), Trunc[u8](bad_pair[1]), 0, 0, Trunc[u8](bad_pair[2]), p0[..], 0, no_extra[..])
        _ = DecodePng(&ar, u, DefaultDecodeLimits) else e {
            match e {
                Unsupported { continue }
                _ { Assert(false) }
            }
        }
        Assert(false)
    }

    // Bad header: zero dimensions, invalid bit depth.
    for hdr in [[0, 1, 8, 6], [1, 0, 8, 6], [1, 1, 3, 6]] {
        bh := try BuildTestPng(&ar, Trunc[u32](hdr[0]), Trunc[u32](hdr[1]), Trunc[u8](hdr[2]), Trunc[u8](hdr[3]), 0, 0, 0, p0[..], 0, no_extra[..])
        AssertInvalidWhy(&ar, bh, DefaultDecodeLimits, Why.BadHeader)
    }

    // Bad filter byte (5).
    var bad_f_in: [5]u8 = [5, 0, 0, 0, 0]
    bad_f_zlib := try compress.Zlib(&ar, bad_f_in[..])
    pf := [bad_f_zlib]
    bf := try BuildTestPng(&ar, 1, 1, 8, 6, 0, 0, 0, pf[..], 0, no_extra[..])
    AssertInvalidWhy(&ar, bf, DefaultDecodeLimits, Why.BadFilter)

    // Bad zlib (too long or corrupt).
    var long_in: [7]u8 = [0, 1, 2, 3, 4, 5, 6]
    long_zlib := try compress.Zlib(&ar, long_in[..])
    pl := [long_zlib]
    bl := try BuildTestPng(&ar, 1, 1, 8, 6, 0, 0, 0, pl[..], 0, no_extra[..])
    AssertInvalidWhy(&ar, bl, DefaultDecodeLimits, Why.BadZlib)

    var bad_z: [4]u8 = [0x78, 0x9C, 0xFF, 0x00]
    pz := [bad_z[..]]
    bz := try BuildTestPng(&ar, 1, 1, 8, 6, 0, 0, 0, pz[..], 0, no_extra[..])
    AssertInvalidWhy(&ar, bz, DefaultDecodeLimits, Why.BadZlib)
}

test "EncodePng validates Image dimensions and data length" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    var d: [4]u8 = [0; 4]
    _ = EncodePng(&ar, Image(width: 0, height: 1, pixels: Pixels.Rgba8, data: d[0..0])) else e {
        match e {
            Invalid(why: w) { Assert(WhyCode(w) == WhyCode(Why.BadImage)) }
            _ { Assert(false) }
        }
    }
    _ = EncodePng(&ar, Image(width: 1, height: 1, pixels: Pixels.Rgba8, data: d[0..3])) else e {
        match e {
            Invalid(why: w) { Assert(WhyCode(w) == WhyCode(Why.BadImage)) }
            _ { Assert(false) }
        }
    }
}
