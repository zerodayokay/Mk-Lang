// Why: golden tests and screenshots need a compact, exact PNG encoder
// built on std.compress and std.hash.
// What: EncodePng and per-scanline adaptive PNG filtering.
// How: validates dimensions and buffer length, chooses per row the PNG
// filter (None, Sub, Up, Average, Paeth) with minimum sum of absolute
// signed residuals in a scratch sub-arena, compresses with compress.Zlib,
// and emits Signature, IHDR, IDAT and IEND with CRC-32 checksums.

import std.compress
import std.hash

// PaethPred returns the PNG Paeth predictor of left (a), up (b) and up-left (c).
PaethPred(a: u8, b: u8, c: u8): u8 {
    ia := int(a)
    ib := int(b)
    ic := int(c)
    p := ia + ib - ic
    pa := if p >= ia { p - ia } else { ia - p }
    pb := if p >= ib { p - ib } else { ib - p }
    pc := if p >= ic { p - ic } else { ic - p }
    if pa <= pb && pa <= pc {
        return a
    }
    if pb <= pc {
        return b
    }
    return c
}

// AbsSignedByte returns the absolute value of byte x interpreted as signed i8.
AbsSignedByte(x: u8): int {
    v := int(x)
    return if v >= 128 { 256 - v } else { v }
}

// FilterRow writes filtered row y (filter byte + row_bytes) into dst[dst_off..]
// using the filter type (0..4) with the smallest sum of absolute signed bytes.
FilterRow(src: []u8, y: int, row_bytes: int, bpp: int, scratch: mut []u8, dst: mut []u8, dst_off: int) {
    row_off := y * row_bytes
    prev_off := (y - 1) * row_bytes
    var best_f := 0
    var best_score := 0
    // Score filter 0 (None) directly.
    for x in 0..row_bytes {
        best_score += AbsSignedByte(src[row_off + x])
    }
    if best_score == 0 {
        dst[dst_off] = 0
        (mut dst[dst_off + 1..dst_off + 1 + row_bytes]).CopyFrom(src[row_off..row_off + row_bytes])
        return
    }
    // Try filters 1..4 in scratch[0..row_bytes], keeping the winning filter in dst.
    (mut dst[dst_off + 1..dst_off + 1 + row_bytes]).CopyFrom(src[row_off..row_off + row_bytes])
    dst[dst_off] = 0
    for f in 1..5 {
        var score := 0
        for x in 0..row_bytes {
            cur := src[row_off + x]
            left: u8 = if x >= bpp { src[row_off + x - bpp] } else { 0 }
            up: u8 = if y > 0 { src[prev_off + x] } else { 0 }
            var pred: u8 = 0
            if f == 1 {
                pred = left
            } else if f == 2 {
                pred = up
            } else if f == 3 {
                pred = Trunc[u8]((int(left) + int(up)) >> 1)
            } else {
                up_left: u8 = if y > 0 && x >= bpp { src[prev_off + x - bpp] } else { 0 }
                pred = PaethPred(left, up, up_left)
            }
            v := cur -% pred
            scratch[x] = v
            score += AbsSignedByte(v)
            if score >= best_score {
                break
            }
        }
        if score < best_score {
            best_score = score
            best_f = f
            dst[dst_off] = Trunc[u8](f)
            (mut dst[dst_off + 1..dst_off + 1 + row_bytes]).CopyFrom(scratch[0..row_bytes])
        }
    }
}

// WriteChunk writes one PNG chunk (length, 4-byte type, data, CRC-32) at
// dst[off..] and returns the updated offset.
WriteChunk(dst: mut []u8, off: int, tag: u32, data: []u8): int {
    n := data.Len()
    PutBe32(dst, off, Trunc[u32](n))
    PutBe32(dst, off + 4, tag)
    if n > 0 {
        (mut dst[off + 8..off + 8 + n]).CopyFrom(data)
    }
    crc := hash.Crc32(dst[off + 4..off + 8 + n])
    PutBe32(dst, off + 8 + n, crc)
    return off + 12 + n
}

/// Encodes img as a PNG. Errors: Invalid(BadImage), OutOfMemory. O(bytes);
/// allocates the file in a.
export EncodePng(a: &Arena, img: Image): Error![]u8 from a {
    bpp := BytesPerPixel(img.pixels)
    ctype := PngColorType(img.pixels)
    if bpp == 0 || img.width <= 0 || img.height <= 0 || img.width > 2147483647 || img.height > 2147483647 {
        fail Invalid(why: Why.BadImage)
    }
    max_i64: i64 = 9223372036854775807
    w64 := Cast[i64](img.width)
    h64 := Cast[i64](img.height)
    bpp64 := Cast[i64](bpp)
    if w64 > max_i64 / bpp64 {
        fail Invalid(why: Why.BadImage)
    }
    row64 := w64 * bpp64
    if h64 > max_i64 / (row64 + 1) {
        fail Invalid(why: Why.BadImage)
    }
    need64 := row64 * h64
    if Cast[i64](img.data.Len()) != need64 {
        fail Invalid(why: Why.BadImage)
    }
    row_bytes := img.width * bpp
    raw_len := img.height * (1 + row_bytes)
    var tmp := try a.Sub()
    defer tmp.Free()
    raw := try tmp.Slice[u8](raw_len)
    scratch := try tmp.Slice[u8](row_bytes)
    stride := 1 + row_bytes
    for y in 0..img.height {
        FilterRow(img.data, y, row_bytes, bpp, scratch, raw, y * stride)
    }
    z := try compress.Zlib(&tmp, raw[..])
    if Cast[i64](z.Len()) > 2147483647 {
        fail Invalid(why: Why.BadImage)
    }
    // Signature (8) + IHDR (12 + 13 = 25) + IDAT (12 + z.Len()) + IEND (12).
    total := 8 + 25 + 12 + z.Len() + 12
    out := try a.Slice[u8](total)
    for i in 0..8 {
        out[i] = PngSig[i]
    }
    var ihdr: [13]u8 = [0; 13]
    PutBe32(mut ihdr[..], 0, Trunc[u32](img.width))
    PutBe32(mut ihdr[..], 4, Trunc[u32](img.height))
    ihdr[8] = 8
    ihdr[9] = ctype
    ihdr[10] = 0
    ihdr[11] = 0
    ihdr[12] = 0
    var pos := 8
    pos = WriteChunk(out, pos, 0x49484452, ihdr[..])
    pos = WriteChunk(out, pos, 0x49444154, z)
    var empty: [0]u8 = []
    pos = WriteChunk(out, pos, 0x49454E44, empty[..])
    return out[0..pos]
}
