// Why: texture loading and golden checks read untrusted PNG files and must
// bound memory, validate every header/CRC/filter byte and never panic.
// What: DecodePng and scanline unfiltering.
// How: validates signature, chunks and CRC-32s sequentially without
// allocation past DecodeLimits, inflates IDAT bytes in a temporary
// sub-arena with exact scanline byte limit, then unfilters into a.

import std.compress
import std.hash

const TagIhdr: u32 = 0x49484452
const TagPlte: u32 = 0x504C5445
const TagIdat: u32 = 0x49444154
const TagIend: u32 = 0x49454E44

// ValidChunkType reports whether tag consists of four ASCII letters [A-Za-z].
ValidChunkType(tag: u32): bool {
    for shift in [24, 16, 8, 0] {
        b := (tag >> Trunc[u32](shift)) & 0xFF
        up := b & 0xDF
        if up < 0x41 || up > 0x5A {
            return false
        }
    }
    return true
}

// ValidIhdrPair reports whether (color_type, bit_depth) is legal per RFC 2083.
ValidIhdrPair(ctype: u8, depth: u8): bool {
    if ctype == 0 {
        return depth == 1 || depth == 2 || depth == 4 || depth == 8 || depth == 16
    }
    if ctype == 2 || ctype == 4 || ctype == 6 {
        return depth == 8 || depth == 16
    }
    if ctype == 3 {
        return depth == 1 || depth == 2 || depth == 4 || depth == 8
    }
    return false
}

// PixelsForColor returns the Pixels format for a supported 8-bit color_type.
PixelsForColor(ctype: u8): ?Pixels {
    if ctype == 0 {
        return some(Pixels.Gray8)
    }
    if ctype == 2 {
        return some(Pixels.Rgb8)
    }
    if ctype == 6 {
        return some(Pixels.Rgba8)
    }
    return none
}

// UnfilterScanlines reverses PNG scanline filters 0..4 from raw into out.
UnfilterScanlines(raw: []u8, out: mut []u8, height: int, row_bytes: int, bpp: int): Error!void {
    stride_in := 1 + row_bytes
    for y in 0..height {
        src_off := y * stride_in
        f := raw[src_off]
        if f > 4 {
            fail Invalid(why: Why.BadFilter)
        }
        dst_off := y * row_bytes
        prev_off := (y - 1) * row_bytes
        if f == 0 {
            (mut out[dst_off..dst_off + row_bytes]).CopyFrom(raw[src_off + 1..src_off + 1 + row_bytes])
        } else if f == 1 {
            for x in 0..row_bytes {
                left: u8 = if x >= bpp { out[dst_off + x - bpp] } else { 0 }
                out[dst_off + x] = raw[src_off + 1 + x] +% left
            }
        } else if f == 2 {
            if y == 0 {
                (mut out[dst_off..dst_off + row_bytes]).CopyFrom(raw[src_off + 1..src_off + 1 + row_bytes])
            } else {
                for x in 0..row_bytes {
                    out[dst_off + x] = raw[src_off + 1 + x] +% out[prev_off + x]
                }
            }
        } else if f == 3 {
            for x in 0..row_bytes {
                left: u8 = if x >= bpp { out[dst_off + x - bpp] } else { 0 }
                up: u8 = if y > 0 { out[prev_off + x] } else { 0 }
                out[dst_off + x] = raw[src_off + 1 + x] +% Trunc[u8]((int(left) + int(up)) >> 1)
            }
        } else {
            for x in 0..row_bytes {
                left: u8 = if x >= bpp { out[dst_off + x - bpp] } else { 0 }
                up: u8 = if y > 0 { out[prev_off + x] } else { 0 }
                up_left: u8 = if y > 0 && x >= bpp { out[prev_off + x - bpp] } else { 0 }
                out[dst_off + x] = raw[src_off + 1 + x] +% PaethPred(left, up, up_left)
            }
        }
    }
}

// GatherIdat returns the concatenated IDAT payload, avoiding a copy when there
// is only one IDAT chunk.
GatherIdat(tmp: &Arena, bytes: []u8, starts: []int, ends: []int, total_idat: int): Error![]u8 from tmp, bytes {
    if starts.Len() == 1 {
        return bytes[starts[0]..ends[0]]
    }
    buf := try tmp.Slice[u8](total_idat)
    var off := 0
    for i in 0..starts.Len() {
        part := bytes[starts[i]..ends[i]]
        (mut buf[off..off + part.Len()]).CopyFrom(part)
        off += part.Len()
    }
    return buf[..]
}

/// Decodes a non-interlaced 8-bit Gray, RGB or RGBA PNG. Never panics on its
/// input. Errors: Invalid(why), Unsupported, TooLarge, OutOfMemory. O(bytes);
/// allocates the image in a, bounded by limits.max_bytes.
export DecodePng(a: &Arena, bytes: []u8, limits: DecodeLimits): Error!Image from a {
    n := bytes.Len()
    sig_check := if n < 8 { n } else { 8 }
    for i in 0..sig_check {
        if bytes[i] != PngSig[i] {
            fail Invalid(why: Why.BadSignature)
        }
    }
    if n < 8 {
        fail Invalid(why: Why.Truncated)
    }
    var pos := 8
    var seen_ihdr := false
    var seen_plte := false
    var seen_idat := false
    var ended_idat := false
    var seen_iend := false
    var width := 0
    var height := 0
    var bpp := 0
    var color_type: u8 = 0
    var px := Pixels.Rgba8
    var row_bytes := 0
    var pixel_bytes := 0
    var raw_bytes := 0
    var total_idat := 0
    var tmp := try a.Sub()
    defer tmp.Free()
    var idat_starts := List[int].New(&tmp)
    var idat_ends := List[int].New(&tmp)

    for pos < n {
        if n - pos < 12 {
            fail Invalid(why: Why.Truncated)
        }
        clen64 := Cast[i64](Be32(bytes, pos))
        if clen64 > 2147483647 {
            fail Invalid(why: Why.BadChunk)
        }
        rem := Cast[i64](n - pos - 12)
        if clen64 > rem {
            fail Invalid(why: Why.Truncated)
        }
        clen := int(clen64)
        tag := Be32(bytes, pos + 4)
        if !ValidChunkType(tag) {
            fail Invalid(why: Why.BadChunk)
        }
        want_crc := Be32(bytes, pos + 8 + clen)
        got_crc := hash.Crc32(bytes[pos + 4..pos + 8 + clen])
        if want_crc != got_crc {
            fail Invalid(why: Why.BadCrc)
        }
        data_start := pos + 8
        data_end := data_start + clen
        data := bytes[data_start..data_end]
        pos = data_end + 4

        if !seen_ihdr {
            if tag != TagIhdr {
                fail Invalid(why: Why.BadChunk)
            }
            if clen != 13 {
                fail Invalid(why: Why.BadHeader)
            }
            w64 := Cast[i64](Be32(data, 0))
            h64 := Cast[i64](Be32(data, 4))
            depth := data[8]
            color_type = data[9]
            comp := data[10]
            filt := data[11]
            interlace := data[12]
            if w64 == 0 || h64 == 0 || w64 > 2147483647 || h64 > 2147483647 {
                fail Invalid(why: Why.BadHeader)
            }
            if comp != 0 || filt != 0 || (interlace != 0 && interlace != 1) {
                fail Invalid(why: Why.BadHeader)
            }
            if !ValidIhdrPair(color_type, depth) {
                fail Invalid(why: Why.BadHeader)
            }
            if interlace != 0 || depth != 8 {
                fail Unsupported
            }
            px = PixelsForColor(color_type) else {
                fail Unsupported
            }
            bpp = BytesPerPixel(px)
            if limits.max_width <= 0 || limits.max_height <= 0 || limits.max_bytes <= 0 {
                fail TooLarge
            }
            if w64 > Cast[i64](limits.max_width) || h64 > Cast[i64](limits.max_height) {
                fail TooLarge
            }
            max_b := Cast[i64](limits.max_bytes)
            if w64 > max_b / Cast[i64](bpp) {
                fail TooLarge
            }
            row64 := w64 * Cast[i64](bpp)
            if h64 > max_b / row64 {
                fail TooLarge
            }
            pix64 := row64 * h64
            max_i64: i64 = 9223372036854775807
            if h64 > max_i64 - pix64 {
                fail TooLarge
            }
            raw64 := pix64 + h64
            width = int(w64)
            height = int(h64)
            row_bytes = int(row64)
            pixel_bytes = int(pix64)
            raw_bytes = int(raw64)
            seen_ihdr = true
        } else if tag == TagIhdr {
            fail Invalid(why: Why.BadChunk)
        } else if tag == TagPlte {
            if seen_idat || seen_plte || color_type == 0 {
                fail Invalid(why: Why.BadChunk)
            }
            if clen == 0 || clen % 3 != 0 || clen > 768 {
                fail Invalid(why: Why.BadChunk)
            }
            seen_plte = true
        } else if tag == TagIdat {
            if ended_idat {
                fail Invalid(why: Why.BadChunk)
            }
            seen_idat = true
            try idat_starts.Push(data_start)
            try idat_ends.Push(data_end)
            total_idat += clen
        } else if tag == TagIend {
            if clen != 0 || !seen_idat {
                fail Invalid(why: Why.BadChunk)
            }
            seen_iend = true
            break
        } else {
            if seen_idat {
                ended_idat = true
            }
            // Bit 5 of byte 0 is 0 for critical chunks: any other critical chunk is unsupported.
            if (tag >> 24) & 0x20 == 0 {
                fail Unsupported
            }
        }
    }

    if !seen_ihdr || !seen_iend || !seen_idat {
        fail Invalid(why: Why.Truncated)
    }

    idat := try GatherIdat(&tmp, bytes, idat_starts.Items(), idat_ends.Items(), total_idat)
    raw := compress.Unzlib(&tmp, idat, raw_bytes) else e {
        match e {
            OutOfMemory { fail OutOfMemory }
            _ { fail Invalid(why: Why.BadZlib) }
        }
    }
    if raw.Len() != raw_bytes {
        fail Invalid(why: Why.BadZlib)
    }
    out := try a.Slice[u8](pixel_bytes)
    try UnfilterScanlines(raw, out, height, row_bytes, bpp)
    return Image(width: width, height: height, pixels: px, data: out[..])
}
