// Why: golden tests, screenshots and texture loading share one plain pixel
// buffer description and bounded decoding policy.
// What: Pixels, Image, DecodeLimits, DefaultDecodeLimits and internal
// pixel-format helpers (BytesPerPixel, PngColorType, Be32, PutBe32).
// How: plain value types with byte-level big-endian helpers for PNG chunks.

/// How pixels are stored.
export open enum Pixels {
    Gray8
    Rgb8
    Rgba8
}

/// Pixels in memory: rows top first, tightly packed.
export struct Image {
    export width: int
    export height: int
    export pixels: Pixels
    export data: []u8
}

/// What DecodePng accepts. Start from DefaultDecodeLimits.
export struct DecodeLimits {
    export max_width: int
    export max_height: int
    /// Decoded bytes at most; also bounds the inflate output.
    export max_bytes: int
    sealed: u8
}

/// 16,384 by 16,384, 256 MiB.
export const DefaultDecodeLimits: DecodeLimits = DecodeLimits(max_width: 16384, max_height: 16384, max_bytes: 268435456, sealed: 0)

// BytesPerPixel returns the channel byte count for p, or 0 for an unknown variant.
BytesPerPixel(p: Pixels): int {
    match p {
        Pixels.Gray8 { return 1 }
        Pixels.Rgb8 { return 3 }
        Pixels.Rgba8 { return 4 }
        _ { return 0 }
    }
}

// PngColorType returns the PNG IHDR color type for p, or 255 for unknown.
PngColorType(p: Pixels): u8 {
    match p {
        Pixels.Gray8 { return 0 }
        Pixels.Rgb8 { return 2 }
        Pixels.Rgba8 { return 6 }
        _ { return 255 }
    }
}

// PixelsEq reports whether two Pixels values name the same format.
PixelsEq(a: Pixels, b: Pixels): bool {
    return BytesPerPixel(a) == BytesPerPixel(b)
}

// WhyCode returns a stable integer code for a Why variant.
WhyCode(w: Why): int {
    match w {
        Why.BadSignature { return 1 }
        Why.BadCrc { return 2 }
        Why.BadChunk { return 3 }
        Why.BadHeader { return 4 }
        Why.BadZlib { return 5 }
        Why.BadFilter { return 6 }
        Why.Truncated { return 7 }
        Why.BadImage { return 8 }
        Why.SizeMismatch { return 9 }
        _ { return 0 }
    }
}

// PutBe32 writes v big-endian at dst[off..off + 4].
PutBe32(dst: mut []u8, off: int, v: u32) {
    dst[off] = Trunc[u8](v >> 24)
    dst[off + 1] = Trunc[u8](v >> 16)
    dst[off + 2] = Trunc[u8](v >> 8)
    dst[off + 3] = Trunc[u8](v)
}

// Be32 reads a big-endian u32 at src[off..off + 4].
Be32(src: []u8, off: int): u32 {
    return (u32(src[off]) << 24) | (u32(src[off + 1]) << 16) | (u32(src[off + 2]) << 8) | u32(src[off + 3])
}

// PngSig is the 8-byte PNG file signature (RFC 2083 section 3.1).
const PngSig: [8]u8 = [137, 80, 78, 71, 13, 10, 26, 10]
