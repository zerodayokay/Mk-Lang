// Why: every texel and vertex format the library offers means the same on
// both APIs, and what a format supports is queried, not assumed.
//
// What: TextureFormat, FormatCaps, VertexFormat, IndexFormat, and the
// per-format metadata table (block size, aspects, four DXGI codes, VkFormat,
// guaranteed capabilities, twins, and vertex format descriptors).
//
// How: pure enum-indexed table lookups with zero allocation.

/// The texel formats. Depth24PlusStencil8 is D24S8 where the device has it
/// and D32S8 where it does not; its depth aspect cannot be copied.
export open enum TextureFormat {
    R8Unorm
    R8Snorm
    R8Uint
    R8Sint
    Rg8Unorm
    Rg8Snorm
    Rg8Uint
    Rg8Sint
    Rgba8Unorm
    Rgba8UnormSrgb
    Rgba8Snorm
    Rgba8Uint
    Rgba8Sint
    Bgra8Unorm
    Bgra8UnormSrgb
    Rgb10a2Unorm
    Rg11b10Ufloat
    R16Float
    Rg16Float
    Rgba16Float
    R16Uint
    R16Sint
    Rgba16Uint
    Rgba16Sint
    R32Float
    Rg32Float
    Rgba32Float
    R32Uint
    R32Sint
    Rg32Uint
    Rgba32Uint
    Depth16Unorm
    Depth32Float
    Depth24PlusStencil8
    Depth32FloatStencil8
    Bc1Unorm
    Bc1UnormSrgb
    Bc3Unorm
    Bc3UnormSrgb
    Bc4Unorm
    Bc5Unorm
    Bc6hUfloat
    Bc7Unorm
    Bc7UnormSrgb
}

/// What one format supports on one device, read from the driver at Open.
export struct FormatCaps {
    export sampled: bool
    export filterable: bool
    export render_target: bool
    export blendable: bool
    export multisample: bool
    export resolve: bool
    export depth_stencil: bool
    sealed: u8
}

/// The vertex attribute formats: WebGPU's set, which has no 3-component 8-
/// or 16-bit formats (D3D12 has none).
export open enum VertexFormat {
    Uint8x2
    Uint8x4
    Sint8x2
    Sint8x4
    Unorm8x2
    Unorm8x4
    Snorm8x2
    Snorm8x4
    Uint16x2
    Uint16x4
    Sint16x2
    Sint16x4
    Unorm16x2
    Unorm16x4
    Snorm16x2
    Snorm16x4
    Float16x2
    Float16x4
    Float32
    Float32x2
    Float32x3
    Float32x4
    Uint32
    Uint32x2
    Uint32x3
    Uint32x4
    Sint32
    Sint32x2
    Sint32x3
    Sint32x4
}

/// The index formats.
export open enum IndexFormat {
    Uint16
    Uint32
}

const TextureFormatCount: int = 44

const AspectBitColor: u8 = 1
const AspectBitDepth: u8 = 2
const AspectBitStencil: u8 = 4

const NumClassFloat: u8 = 0
const NumClassUint: u8 = 1
const NumClassSint: u8 = 2
const NumClassDepth: u8 = 3

struct FormatRow {
    block_bytes: u8
    block_w: u8
    block_h: u8
    aspects: u8
    num_class: u8
    is_srgb: bool
    is_compressed: bool
    needs_bc: bool
    needs_f32_filter: bool
    dxgi_res: u32
    dxgi_rtv_dsv: u32
    dxgi_srv: u32
    dxgi_stencil_srv: u32
    vk_format: i32
    guaranteed: FormatCaps
    twin: ?TextureFormat
}

Caps(s: bool, f: bool, rt: bool, bl: bool, ms: bool, res: bool, ds: bool): FormatCaps {
    return FormatCaps(sampled: s, filterable: f, render_target: rt, blendable: bl, multisample: ms, resolve: res, depth_stencil: ds, sealed: 0)
}

const NoCaps: FormatCaps = FormatCaps(sampled: false, filterable: false, render_target: false, blendable: false, multisample: false, resolve: false, depth_stencil: false, sealed: 0)

FormatIndex(f: TextureFormat): int {
    return match f {
        .R8Unorm { 0 }
        .R8Snorm { 1 }
        .R8Uint { 2 }
        .R8Sint { 3 }
        .Rg8Unorm { 4 }
        .Rg8Snorm { 5 }
        .Rg8Uint { 6 }
        .Rg8Sint { 7 }
        .Rgba8Unorm { 8 }
        .Rgba8UnormSrgb { 9 }
        .Rgba8Snorm { 10 }
        .Rgba8Uint { 11 }
        .Rgba8Sint { 12 }
        .Bgra8Unorm { 13 }
        .Bgra8UnormSrgb { 14 }
        .Rgb10a2Unorm { 15 }
        .Rg11b10Ufloat { 16 }
        .R16Float { 17 }
        .Rg16Float { 18 }
        .Rgba16Float { 19 }
        .R16Uint { 20 }
        .R16Sint { 21 }
        .Rgba16Uint { 22 }
        .Rgba16Sint { 23 }
        .R32Float { 24 }
        .Rg32Float { 25 }
        .Rgba32Float { 26 }
        .R32Uint { 27 }
        .R32Sint { 28 }
        .Rg32Uint { 29 }
        .Rgba32Uint { 30 }
        .Depth16Unorm { 31 }
        .Depth32Float { 32 }
        .Depth24PlusStencil8 { 33 }
        .Depth32FloatStencil8 { 34 }
        .Bc1Unorm { 35 }
        .Bc1UnormSrgb { 36 }
        .Bc3Unorm { 37 }
        .Bc3UnormSrgb { 38 }
        .Bc4Unorm { 39 }
        .Bc5Unorm { 40 }
        .Bc6hUfloat { 41 }
        .Bc7Unorm { 42 }
        .Bc7UnormSrgb { 43 }
    }
}

FormatByIndex(i: int): TextureFormat {
    return match i {
        0 { TextureFormat.R8Unorm }
        1 { TextureFormat.R8Snorm }
        2 { TextureFormat.R8Uint }
        3 { TextureFormat.R8Sint }
        4 { TextureFormat.Rg8Unorm }
        5 { TextureFormat.Rg8Snorm }
        6 { TextureFormat.Rg8Uint }
        7 { TextureFormat.Rg8Sint }
        8 { TextureFormat.Rgba8Unorm }
        9 { TextureFormat.Rgba8UnormSrgb }
        10 { TextureFormat.Rgba8Snorm }
        11 { TextureFormat.Rgba8Uint }
        12 { TextureFormat.Rgba8Sint }
        13 { TextureFormat.Bgra8Unorm }
        14 { TextureFormat.Bgra8UnormSrgb }
        15 { TextureFormat.Rgb10a2Unorm }
        16 { TextureFormat.Rg11b10Ufloat }
        17 { TextureFormat.R16Float }
        18 { TextureFormat.Rg16Float }
        19 { TextureFormat.Rgba16Float }
        20 { TextureFormat.R16Uint }
        21 { TextureFormat.R16Sint }
        22 { TextureFormat.Rgba16Uint }
        23 { TextureFormat.Rgba16Sint }
        24 { TextureFormat.R32Float }
        25 { TextureFormat.Rg32Float }
        26 { TextureFormat.Rgba32Float }
        27 { TextureFormat.R32Uint }
        28 { TextureFormat.R32Sint }
        29 { TextureFormat.Rg32Uint }
        30 { TextureFormat.Rgba32Uint }
        31 { TextureFormat.Depth16Unorm }
        32 { TextureFormat.Depth32Float }
        33 { TextureFormat.Depth24PlusStencil8 }
        34 { TextureFormat.Depth32FloatStencil8 }
        35 { TextureFormat.Bc1Unorm }
        36 { TextureFormat.Bc1UnormSrgb }
        37 { TextureFormat.Bc3Unorm }
        38 { TextureFormat.Bc3UnormSrgb }
        39 { TextureFormat.Bc4Unorm }
        40 { TextureFormat.Bc5Unorm }
        41 { TextureFormat.Bc6hUfloat }
        42 { TextureFormat.Bc7Unorm }
        _ { TextureFormat.Bc7UnormSrgb }
    }
}

RowOf(f: TextureFormat): FormatRow {
    return match f {
        .R8Unorm { FormatRow(block_bytes: 1, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassFloat, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 61, dxgi_rtv_dsv: 61, dxgi_srv: 61, dxgi_stencil_srv: 0, vk_format: 9, guaranteed: Caps(true, true, true, true, true, true, false), twin: none) }
        .R8Snorm { FormatRow(block_bytes: 1, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassFloat, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 63, dxgi_rtv_dsv: 0, dxgi_srv: 63, dxgi_stencil_srv: 0, vk_format: 10, guaranteed: Caps(true, true, false, false, false, false, false), twin: none) }
        .R8Uint { FormatRow(block_bytes: 1, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassUint, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 62, dxgi_rtv_dsv: 62, dxgi_srv: 62, dxgi_stencil_srv: 0, vk_format: 13, guaranteed: Caps(true, false, true, false, true, false, false), twin: none) }
        .R8Sint { FormatRow(block_bytes: 1, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassSint, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 64, dxgi_rtv_dsv: 64, dxgi_srv: 64, dxgi_stencil_srv: 0, vk_format: 14, guaranteed: Caps(true, false, true, false, true, false, false), twin: none) }

        .Rg8Unorm { FormatRow(block_bytes: 2, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassFloat, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 49, dxgi_rtv_dsv: 49, dxgi_srv: 49, dxgi_stencil_srv: 0, vk_format: 16, guaranteed: Caps(true, true, true, true, true, true, false), twin: none) }
        .Rg8Snorm { FormatRow(block_bytes: 2, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassFloat, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 51, dxgi_rtv_dsv: 0, dxgi_srv: 51, dxgi_stencil_srv: 0, vk_format: 17, guaranteed: Caps(true, true, false, false, false, false, false), twin: none) }
        .Rg8Uint { FormatRow(block_bytes: 2, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassUint, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 50, dxgi_rtv_dsv: 50, dxgi_srv: 50, dxgi_stencil_srv: 0, vk_format: 20, guaranteed: Caps(true, false, true, false, true, false, false), twin: none) }
        .Rg8Sint { FormatRow(block_bytes: 2, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassSint, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 52, dxgi_rtv_dsv: 52, dxgi_srv: 52, dxgi_stencil_srv: 0, vk_format: 21, guaranteed: Caps(true, false, true, false, true, false, false), twin: none) }

        .Rgba8Unorm { FormatRow(block_bytes: 4, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassFloat, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 27, dxgi_rtv_dsv: 28, dxgi_srv: 28, dxgi_stencil_srv: 0, vk_format: 37, guaranteed: Caps(true, true, true, true, true, true, false), twin: some(TextureFormat.Rgba8UnormSrgb)) }
        .Rgba8UnormSrgb { FormatRow(block_bytes: 4, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassFloat, is_srgb: true, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 27, dxgi_rtv_dsv: 29, dxgi_srv: 29, dxgi_stencil_srv: 0, vk_format: 43, guaranteed: Caps(true, true, true, true, true, true, false), twin: some(TextureFormat.Rgba8Unorm)) }
        .Rgba8Snorm { FormatRow(block_bytes: 4, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassFloat, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 31, dxgi_rtv_dsv: 0, dxgi_srv: 31, dxgi_stencil_srv: 0, vk_format: 38, guaranteed: Caps(true, true, false, false, false, false, false), twin: none) }
        .Rgba8Uint { FormatRow(block_bytes: 4, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassUint, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 30, dxgi_rtv_dsv: 30, dxgi_srv: 30, dxgi_stencil_srv: 0, vk_format: 41, guaranteed: Caps(true, false, true, false, true, false, false), twin: none) }
        .Rgba8Sint { FormatRow(block_bytes: 4, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassSint, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 32, dxgi_rtv_dsv: 32, dxgi_srv: 32, dxgi_stencil_srv: 0, vk_format: 42, guaranteed: Caps(true, false, true, false, true, false, false), twin: none) }

        .Bgra8Unorm { FormatRow(block_bytes: 4, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassFloat, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 90, dxgi_rtv_dsv: 87, dxgi_srv: 87, dxgi_stencil_srv: 0, vk_format: 44, guaranteed: Caps(true, true, true, true, true, true, false), twin: some(TextureFormat.Bgra8UnormSrgb)) }
        .Bgra8UnormSrgb { FormatRow(block_bytes: 4, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassFloat, is_srgb: true, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 90, dxgi_rtv_dsv: 91, dxgi_srv: 91, dxgi_stencil_srv: 0, vk_format: 50, guaranteed: Caps(true, true, true, true, true, true, false), twin: some(TextureFormat.Bgra8Unorm)) }
        .Rgb10a2Unorm { FormatRow(block_bytes: 4, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassFloat, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 24, dxgi_rtv_dsv: 24, dxgi_srv: 24, dxgi_stencil_srv: 0, vk_format: 64, guaranteed: Caps(true, true, true, true, true, true, false), twin: none) }
        .Rg11b10Ufloat { FormatRow(block_bytes: 4, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassFloat, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 26, dxgi_rtv_dsv: 26, dxgi_srv: 26, dxgi_stencil_srv: 0, vk_format: 122, guaranteed: Caps(true, true, false, false, false, false, false), twin: none) }

        .R16Float { FormatRow(block_bytes: 2, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassFloat, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 54, dxgi_rtv_dsv: 54, dxgi_srv: 54, dxgi_stencil_srv: 0, vk_format: 76, guaranteed: Caps(true, true, true, true, true, true, false), twin: none) }
        .Rg16Float { FormatRow(block_bytes: 4, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassFloat, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 34, dxgi_rtv_dsv: 34, dxgi_srv: 34, dxgi_stencil_srv: 0, vk_format: 83, guaranteed: Caps(true, true, true, true, true, true, false), twin: none) }
        .Rgba16Float { FormatRow(block_bytes: 8, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassFloat, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 10, dxgi_rtv_dsv: 10, dxgi_srv: 10, dxgi_stencil_srv: 0, vk_format: 97, guaranteed: Caps(true, true, true, true, true, true, false), twin: none) }
        .R16Uint { FormatRow(block_bytes: 2, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassUint, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 57, dxgi_rtv_dsv: 57, dxgi_srv: 57, dxgi_stencil_srv: 0, vk_format: 74, guaranteed: Caps(true, false, true, false, true, false, false), twin: none) }
        .R16Sint { FormatRow(block_bytes: 2, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassSint, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 59, dxgi_rtv_dsv: 59, dxgi_srv: 59, dxgi_stencil_srv: 0, vk_format: 75, guaranteed: Caps(true, false, true, false, true, false, false), twin: none) }
        .Rgba16Uint { FormatRow(block_bytes: 8, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassUint, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 12, dxgi_rtv_dsv: 12, dxgi_srv: 12, dxgi_stencil_srv: 0, vk_format: 95, guaranteed: Caps(true, false, true, false, true, false, false), twin: none) }
        .Rgba16Sint { FormatRow(block_bytes: 8, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassSint, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 14, dxgi_rtv_dsv: 14, dxgi_srv: 14, dxgi_stencil_srv: 0, vk_format: 96, guaranteed: Caps(true, false, true, false, true, false, false), twin: none) }

        .R32Float { FormatRow(block_bytes: 4, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassFloat, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: true, dxgi_res: 41, dxgi_rtv_dsv: 41, dxgi_srv: 41, dxgi_stencil_srv: 0, vk_format: 100, guaranteed: Caps(true, false, true, true, true, false, false), twin: none) }
        .Rg32Float { FormatRow(block_bytes: 8, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassFloat, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: true, dxgi_res: 16, dxgi_rtv_dsv: 16, dxgi_srv: 16, dxgi_stencil_srv: 0, vk_format: 103, guaranteed: Caps(true, false, true, false, false, false, false), twin: none) }
        .Rgba32Float { FormatRow(block_bytes: 16, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassFloat, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: true, dxgi_res: 2, dxgi_rtv_dsv: 2, dxgi_srv: 2, dxgi_stencil_srv: 0, vk_format: 109, guaranteed: Caps(true, false, true, false, false, false, false), twin: none) }
        .R32Uint { FormatRow(block_bytes: 4, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassUint, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 42, dxgi_rtv_dsv: 42, dxgi_srv: 42, dxgi_stencil_srv: 0, vk_format: 98, guaranteed: Caps(true, false, true, false, false, false, false), twin: none) }
        .R32Sint { FormatRow(block_bytes: 4, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassSint, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 43, dxgi_rtv_dsv: 43, dxgi_srv: 43, dxgi_stencil_srv: 0, vk_format: 99, guaranteed: Caps(true, false, true, false, false, false, false), twin: none) }
        .Rg32Uint { FormatRow(block_bytes: 8, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassUint, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 17, dxgi_rtv_dsv: 17, dxgi_srv: 17, dxgi_stencil_srv: 0, vk_format: 101, guaranteed: Caps(true, false, true, false, false, false, false), twin: none) }
        .Rgba32Uint { FormatRow(block_bytes: 16, block_w: 1, block_h: 1, aspects: AspectBitColor, num_class: NumClassUint, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 3, dxgi_rtv_dsv: 3, dxgi_srv: 3, dxgi_stencil_srv: 0, vk_format: 107, guaranteed: Caps(true, false, true, false, false, false, false), twin: none) }

        .Depth16Unorm { FormatRow(block_bytes: 2, block_w: 1, block_h: 1, aspects: AspectBitDepth, num_class: NumClassDepth, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 53, dxgi_rtv_dsv: 55, dxgi_srv: 56, dxgi_stencil_srv: 0, vk_format: 124, guaranteed: Caps(true, false, false, false, true, false, true), twin: none) }
        .Depth32Float { FormatRow(block_bytes: 4, block_w: 1, block_h: 1, aspects: AspectBitDepth, num_class: NumClassDepth, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 39, dxgi_rtv_dsv: 40, dxgi_srv: 41, dxgi_stencil_srv: 0, vk_format: 126, guaranteed: Caps(true, false, false, false, true, false, true), twin: none) }
        .Depth24PlusStencil8 { FormatRow(block_bytes: 4, block_w: 1, block_h: 1, aspects: AspectBitDepth | AspectBitStencil, num_class: NumClassDepth, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 44, dxgi_rtv_dsv: 45, dxgi_srv: 46, dxgi_stencil_srv: 47, vk_format: 129, guaranteed: Caps(true, false, false, false, true, false, true), twin: none) }
        .Depth32FloatStencil8 { FormatRow(block_bytes: 8, block_w: 1, block_h: 1, aspects: AspectBitDepth | AspectBitStencil, num_class: NumClassDepth, is_srgb: false, is_compressed: false, needs_bc: false, needs_f32_filter: false, dxgi_res: 19, dxgi_rtv_dsv: 20, dxgi_srv: 21, dxgi_stencil_srv: 22, vk_format: 130, guaranteed: Caps(true, false, false, false, true, false, true), twin: none) }

        .Bc1Unorm { FormatRow(block_bytes: 8, block_w: 4, block_h: 4, aspects: AspectBitColor, num_class: NumClassFloat, is_srgb: false, is_compressed: true, needs_bc: true, needs_f32_filter: false, dxgi_res: 70, dxgi_rtv_dsv: 0, dxgi_srv: 71, dxgi_stencil_srv: 0, vk_format: 133, guaranteed: Caps(true, true, false, false, false, false, false), twin: some(TextureFormat.Bc1UnormSrgb)) }
        .Bc1UnormSrgb { FormatRow(block_bytes: 8, block_w: 4, block_h: 4, aspects: AspectBitColor, num_class: NumClassFloat, is_srgb: true, is_compressed: true, needs_bc: true, needs_f32_filter: false, dxgi_res: 70, dxgi_rtv_dsv: 0, dxgi_srv: 72, dxgi_stencil_srv: 0, vk_format: 134, guaranteed: Caps(true, true, false, false, false, false, false), twin: some(TextureFormat.Bc1Unorm)) }
        .Bc3Unorm { FormatRow(block_bytes: 16, block_w: 4, block_h: 4, aspects: AspectBitColor, num_class: NumClassFloat, is_srgb: false, is_compressed: true, needs_bc: true, needs_f32_filter: false, dxgi_res: 76, dxgi_rtv_dsv: 0, dxgi_srv: 77, dxgi_stencil_srv: 0, vk_format: 137, guaranteed: Caps(true, true, false, false, false, false, false), twin: some(TextureFormat.Bc3UnormSrgb)) }
        .Bc3UnormSrgb { FormatRow(block_bytes: 16, block_w: 4, block_h: 4, aspects: AspectBitColor, num_class: NumClassFloat, is_srgb: true, is_compressed: true, needs_bc: true, needs_f32_filter: false, dxgi_res: 76, dxgi_rtv_dsv: 0, dxgi_srv: 78, dxgi_stencil_srv: 0, vk_format: 138, guaranteed: Caps(true, true, false, false, false, false, false), twin: some(TextureFormat.Bc3Unorm)) }
        .Bc4Unorm { FormatRow(block_bytes: 8, block_w: 4, block_h: 4, aspects: AspectBitColor, num_class: NumClassFloat, is_srgb: false, is_compressed: true, needs_bc: true, needs_f32_filter: false, dxgi_res: 79, dxgi_rtv_dsv: 0, dxgi_srv: 80, dxgi_stencil_srv: 0, vk_format: 139, guaranteed: Caps(true, true, false, false, false, false, false), twin: none) }
        .Bc5Unorm { FormatRow(block_bytes: 16, block_w: 4, block_h: 4, aspects: AspectBitColor, num_class: NumClassFloat, is_srgb: false, is_compressed: true, needs_bc: true, needs_f32_filter: false, dxgi_res: 82, dxgi_rtv_dsv: 0, dxgi_srv: 83, dxgi_stencil_srv: 0, vk_format: 141, guaranteed: Caps(true, true, false, false, false, false, false), twin: none) }
        .Bc6hUfloat { FormatRow(block_bytes: 16, block_w: 4, block_h: 4, aspects: AspectBitColor, num_class: NumClassFloat, is_srgb: false, is_compressed: true, needs_bc: true, needs_f32_filter: false, dxgi_res: 94, dxgi_rtv_dsv: 0, dxgi_srv: 95, dxgi_stencil_srv: 0, vk_format: 143, guaranteed: Caps(true, true, false, false, false, false, false), twin: none) }
        .Bc7Unorm { FormatRow(block_bytes: 16, block_w: 4, block_h: 4, aspects: AspectBitColor, num_class: NumClassFloat, is_srgb: false, is_compressed: true, needs_bc: true, needs_f32_filter: false, dxgi_res: 97, dxgi_rtv_dsv: 0, dxgi_srv: 98, dxgi_stencil_srv: 0, vk_format: 145, guaranteed: Caps(true, true, false, false, false, false, false), twin: some(TextureFormat.Bc7UnormSrgb)) }
        .Bc7UnormSrgb { FormatRow(block_bytes: 16, block_w: 4, block_h: 4, aspects: AspectBitColor, num_class: NumClassFloat, is_srgb: true, is_compressed: true, needs_bc: true, needs_f32_filter: false, dxgi_res: 97, dxgi_rtv_dsv: 0, dxgi_srv: 99, dxgi_stencil_srv: 0, vk_format: 146, guaranteed: Caps(true, true, false, false, false, false, false), twin: some(TextureFormat.Bc7Unorm)) }
    }
}

IsDepthStencilFormat(f: TextureFormat): bool {
    return (RowOf(f).aspects & (AspectBitDepth | AspectBitStencil)) != 0
}

HasStencilAspect(f: TextureFormat): bool {
    return (RowOf(f).aspects & AspectBitStencil) != 0
}

PlaneCount(f: TextureFormat): int {
    a := RowOf(f).aspects
    if (a & AspectBitDepth) != 0 && (a & AspectBitStencil) != 0 {
        return 2
    }
    return 1
}

IsTwinFormat(base: TextureFormat, view_fmt: TextureFormat): bool {
    if FormatIndex(base) == FormatIndex(view_fmt) {
        return true
    }
    match RowOf(base).twin {
        some(t) { return FormatIndex(t) == FormatIndex(view_fmt) }
        none { return false }
    }
}

CapsSatisfyGuaranteed(have: FormatCaps, want: FormatCaps): bool {
    if want.sampled && !have.sampled { return false }
    if want.filterable && !have.filterable { return false }
    if want.render_target && !have.render_target { return false }
    if want.blendable && !have.blendable { return false }
    if want.multisample && !have.multisample { return false }
    if want.resolve && !have.resolve { return false }
    if want.depth_stencil && !have.depth_stencil { return false }
    return true
}

struct VertexFormatInfo {
    bytes: int
    components: int
    num_class: u8
    dxgi_format: u32
    vk_format: i32
}

VertexInfoOf(f: VertexFormat): VertexFormatInfo {
    return match f {
        .Uint8x2 { VertexFormatInfo(bytes: 2, components: 2, num_class: NumClassUint, dxgi_format: 50, vk_format: 20) }
        .Uint8x4 { VertexFormatInfo(bytes: 4, components: 4, num_class: NumClassUint, dxgi_format: 30, vk_format: 41) }
        .Sint8x2 { VertexFormatInfo(bytes: 2, components: 2, num_class: NumClassSint, dxgi_format: 52, vk_format: 21) }
        .Sint8x4 { VertexFormatInfo(bytes: 4, components: 4, num_class: NumClassSint, dxgi_format: 32, vk_format: 42) }
        .Unorm8x2 { VertexFormatInfo(bytes: 2, components: 2, num_class: NumClassFloat, dxgi_format: 49, vk_format: 16) }
        .Unorm8x4 { VertexFormatInfo(bytes: 4, components: 4, num_class: NumClassFloat, dxgi_format: 28, vk_format: 37) }
        .Snorm8x2 { VertexFormatInfo(bytes: 2, components: 2, num_class: NumClassFloat, dxgi_format: 51, vk_format: 17) }
        .Snorm8x4 { VertexFormatInfo(bytes: 4, components: 4, num_class: NumClassFloat, dxgi_format: 31, vk_format: 38) }
        .Uint16x2 { VertexFormatInfo(bytes: 4, components: 2, num_class: NumClassUint, dxgi_format: 36, vk_format: 81) }
        .Uint16x4 { VertexFormatInfo(bytes: 8, components: 4, num_class: NumClassUint, dxgi_format: 12, vk_format: 95) }
        .Sint16x2 { VertexFormatInfo(bytes: 4, components: 2, num_class: NumClassSint, dxgi_format: 38, vk_format: 82) }
        .Sint16x4 { VertexFormatInfo(bytes: 8, components: 4, num_class: NumClassSint, dxgi_format: 14, vk_format: 96) }
        .Unorm16x2 { VertexFormatInfo(bytes: 4, components: 2, num_class: NumClassFloat, dxgi_format: 35, vk_format: 77) }
        .Unorm16x4 { VertexFormatInfo(bytes: 8, components: 4, num_class: NumClassFloat, dxgi_format: 11, vk_format: 91) }
        .Snorm16x2 { VertexFormatInfo(bytes: 4, components: 2, num_class: NumClassFloat, dxgi_format: 37, vk_format: 78) }
        .Snorm16x4 { VertexFormatInfo(bytes: 8, components: 4, num_class: NumClassFloat, dxgi_format: 13, vk_format: 92) }
        .Float16x2 { VertexFormatInfo(bytes: 4, components: 2, num_class: NumClassFloat, dxgi_format: 34, vk_format: 83) }
        .Float16x4 { VertexFormatInfo(bytes: 8, components: 4, num_class: NumClassFloat, dxgi_format: 10, vk_format: 97) }
        .Float32 { VertexFormatInfo(bytes: 4, components: 1, num_class: NumClassFloat, dxgi_format: 41, vk_format: 100) }
        .Float32x2 { VertexFormatInfo(bytes: 8, components: 2, num_class: NumClassFloat, dxgi_format: 16, vk_format: 103) }
        .Float32x3 { VertexFormatInfo(bytes: 12, components: 3, num_class: NumClassFloat, dxgi_format: 6, vk_format: 106) }
        .Float32x4 { VertexFormatInfo(bytes: 16, components: 4, num_class: NumClassFloat, dxgi_format: 2, vk_format: 109) }
        .Uint32 { VertexFormatInfo(bytes: 4, components: 1, num_class: NumClassUint, dxgi_format: 42, vk_format: 98) }
        .Uint32x2 { VertexFormatInfo(bytes: 8, components: 2, num_class: NumClassUint, dxgi_format: 17, vk_format: 101) }
        .Uint32x3 { VertexFormatInfo(bytes: 12, components: 3, num_class: NumClassUint, dxgi_format: 7, vk_format: 104) }
        .Uint32x4 { VertexFormatInfo(bytes: 16, components: 4, num_class: NumClassUint, dxgi_format: 3, vk_format: 107) }
        .Sint32 { VertexFormatInfo(bytes: 4, components: 1, num_class: NumClassSint, dxgi_format: 43, vk_format: 99) }
        .Sint32x2 { VertexFormatInfo(bytes: 8, components: 2, num_class: NumClassSint, dxgi_format: 18, vk_format: 102) }
        .Sint32x3 { VertexFormatInfo(bytes: 12, components: 3, num_class: NumClassSint, dxgi_format: 8, vk_format: 105) }
        .Sint32x4 { VertexFormatInfo(bytes: 16, components: 4, num_class: NumClassSint, dxgi_format: 4, vk_format: 108) }
    }
}

IndexByteSize(f: IndexFormat): int {
    return match f {
        .Uint16 { 2 }
        .Uint32 { 4 }
    }
}
