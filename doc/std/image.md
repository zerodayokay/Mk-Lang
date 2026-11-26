# std.image

`std.image` encodes and decodes 8-bit PNG images and compares images pixel by pixel with a tolerance for rasteriser edge differences. `DecodePng` bounds every dimension and decoded byte count by `DecodeLimits` and never panics on untrusted input.

Read the [library guide](README.md) first.

```mk
// verdict: parse
import std.image
```

## Contents

- [The main pieces](#the-main-pieces)
- [PNG subset and limits](#png-subset-and-limits)
- [Image comparison](#image-comparison)
- [Errors](#errors)
- [Examples](#examples): [encode and decode a PNG](#encode-and-decode-a-png), [compare two images](#compare-two-images)
- [API](#api)

## The main pieces

| Task | Use |
|---|---|
| Describe pixel memory | `Image` with `Pixels` (`Gray8`, `Rgb8`, `Rgba8`) |
| Write a PNG file | `EncodePng` |
| Read a PNG file safely | `DecodePng` with `DecodeLimits` (`DefaultDecodeLimits`) |
| Diff two images with tolerance | `Compare` returning `Difference` |

`Image.data` holds tightly packed rows, top row first, with length `width * height * channels`.

## PNG subset and limits

`DecodePng` accepts non-interlaced 8-bit grayscale (`Gray8`), RGB (`Rgb8`) and RGBA (`Rgba8`) PNGs, checks every chunk CRC-32 and scanline filter byte (0 through 4), and enforces `DecodeLimits` before allocating pixel buffers:

- `DefaultDecodeLimits` allows up to 16,384 × 16,384 pixels and 256 MiB (`max_bytes: 268435456`).
- Interlaced PNGs, 16-bit channels, sub-8-bit depths, palette (`PLTE` color type 3), grayscale-with-alpha (color type 4) and unknown critical chunks fail with `Unsupported`.
- Images exceeding `max_width`, `max_height` or `max_bytes` fail with `TooLarge`.

## Image comparison

`Compare(a, b, tolerance)` scans both images without allocating:

- `max`: largest per-channel absolute difference.
- `over`: count of pixels where any channel difference exceeds `tolerance`.
- `isolated`: count of pixels over `tolerance` none of whose 4-neighbours is within `tolerance`. Rasteriser edge differences typically have `isolated == 0` even when `over > 0`.
- `pixels`: total pixel count (`width * height`).

## Errors

`image.Error` is open (`Invalid | Unsupported | TooLarge | OutOfMemory`). `Invalid(why)` carries a `Why` variant naming the failure (`BadSignature`, `BadCrc`, `BadChunk`, `BadHeader`, `BadZlib`, `BadFilter`, `Truncated`, `BadImage`, `SizeMismatch`).

## Examples

### Encode and decode a PNG

```mk
// verdict: ok
import std.image

export Main(sys: &System): image.Error!void {
    var pixels: [16]u8 = [
        255, 0, 0, 255, 0, 255, 0, 255,
        0, 0, 255, 255, 255, 255, 255, 255,
    ]
    img := image.Image(width: 2, height: 2, pixels: image.Pixels.Rgba8, data: pixels[..])
    png := try image.EncodePng(sys.arena, img)
    decoded := try image.DecodePng(sys.arena, png, image.DefaultDecodeLimits)
    Assert(decoded.width == 2 && decoded.height == 2)
}
```

### Compare two images

```mk
// verdict: ok
import std.image

export Main(sys: &System): image.Error!void {
    _ = sys
    var a_px: [4]u8 = [100, 100, 100, 255]
    var b_px: [4]u8 = [101, 100, 100, 255]
    a := image.Image(width: 1, height: 1, pixels: image.Pixels.Rgba8, data: a_px[..])
    b := image.Image(width: 1, height: 1, pixels: image.Pixels.Rgba8, data: b_px[..])
    diff := try image.Compare(&a, &b, 2)
    Assert(diff.max == 1 && diff.over == 0 && diff.isolated == 0)
}
```

## API

- `open enum Pixels { Gray8 Rgb8 Rgba8 }`
- `struct Image { width: int height: int pixels: Pixels data: []u8 }`
- `struct DecodeLimits { max_width: int max_height: int max_bytes: int }`
- `const DefaultDecodeLimits: DecodeLimits`
- `EncodePng(a: &Arena, img: Image): Error![]u8 from a`
- `DecodePng(a: &Arena, bytes: []u8, limits: DecodeLimits): Error!Image from a`
- `struct Difference { max: int over: int isolated: int pixels: int }`
- `Compare(a: &Image, b: &Image, tolerance: int): Error!Difference`
- `open type Error = Invalid | Unsupported | TooLarge | OutOfMemory`
- `error Invalid(why: Why)`
- `error Unsupported`
- `error TooLarge`
- `open enum Why { BadSignature BadCrc BadChunk BadHeader BadZlib BadFilter Truncated BadImage SizeMismatch }`
