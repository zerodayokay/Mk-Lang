// Why: format metadata drives D3D12 resource/view DXGI formats, Vulkan
// formats, copy footprints, and adapter capability filtering, so every row
// must agree across all 44 texture formats and 30 vertex formats.
//
// What: unit tests for format.mk (rows, 4 DXGI codes, VkFormat, twins,
// guaranteed capabilities, vertex formats).
//
// How: pure test blocks verifying bijections, typeless/view DXGI codes,
// depth-stencil aspects, twins, and capability checks.

test "format index bijection across all 44 texture formats" {
    for i in 0..TextureFormatCount {
        f := FormatByIndex(i)
        Assert(FormatIndex(f) == i)
        row := RowOf(f)
        Assert(row.block_bytes > 0)
        Assert(row.block_w >= 1 && row.block_h >= 1)
        Assert(row.vk_format > 0)
        Assert(row.dxgi_res != 0)
        Assert(row.dxgi_srv != 0)
    }
}

test "four DXGI codes and aspects for typeless twins and depth-stencil formats" {
    rgba := RowOf(TextureFormat.Rgba8Unorm)
    rgba_srgb := RowOf(TextureFormat.Rgba8UnormSrgb)
    Assert(rgba.dxgi_res == 27 && rgba.dxgi_rtv_dsv == 28 && rgba.dxgi_srv == 28 && rgba.dxgi_stencil_srv == 0)
    Assert(rgba_srgb.dxgi_res == 27 && rgba_srgb.dxgi_rtv_dsv == 29 && rgba_srgb.dxgi_srv == 29 && rgba_srgb.dxgi_stencil_srv == 0)

    bgra := RowOf(TextureFormat.Bgra8Unorm)
    bgra_srgb := RowOf(TextureFormat.Bgra8UnormSrgb)
    Assert(bgra.dxgi_res == 90 && bgra.dxgi_rtv_dsv == 87 && bgra.dxgi_srv == 87)
    Assert(bgra_srgb.dxgi_res == 90 && bgra_srgb.dxgi_rtv_dsv == 91 && bgra_srgb.dxgi_srv == 91)

    d24 := RowOf(TextureFormat.Depth24PlusStencil8)
    Assert(d24.dxgi_res == 44 && d24.dxgi_rtv_dsv == 45 && d24.dxgi_srv == 46 && d24.dxgi_stencil_srv == 47)
    Assert(IsDepthStencilFormat(TextureFormat.Depth24PlusStencil8))
    Assert(HasStencilAspect(TextureFormat.Depth24PlusStencil8))
    Assert(PlaneCount(TextureFormat.Depth24PlusStencil8) == 2)

    d32s8 := RowOf(TextureFormat.Depth32FloatStencil8)
    Assert(d32s8.dxgi_res == 19 && d32s8.dxgi_rtv_dsv == 20 && d32s8.dxgi_srv == 21 && d32s8.dxgi_stencil_srv == 22)
    Assert(PlaneCount(TextureFormat.Depth32FloatStencil8) == 2)

    d32 := RowOf(TextureFormat.Depth32Float)
    Assert(d32.dxgi_res == 39 && d32.dxgi_rtv_dsv == 40 && d32.dxgi_srv == 41 && d32.dxgi_stencil_srv == 0)
    Assert(PlaneCount(TextureFormat.Depth32Float) == 1)
    Assert(!HasStencilAspect(TextureFormat.Depth32Float))
}

test "sRGB and linear twins match symmetrically" {
    Assert(IsTwinFormat(TextureFormat.Rgba8Unorm, TextureFormat.Rgba8UnormSrgb))
    Assert(IsTwinFormat(TextureFormat.Rgba8UnormSrgb, TextureFormat.Rgba8Unorm))
    Assert(IsTwinFormat(TextureFormat.Bgra8Unorm, TextureFormat.Bgra8UnormSrgb))
    Assert(IsTwinFormat(TextureFormat.Bc1Unorm, TextureFormat.Bc1UnormSrgb))
    Assert(IsTwinFormat(TextureFormat.Bc3Unorm, TextureFormat.Bc3UnormSrgb))
    Assert(IsTwinFormat(TextureFormat.Bc7Unorm, TextureFormat.Bc7UnormSrgb))
    Assert(!IsTwinFormat(TextureFormat.Rgba8Unorm, TextureFormat.Bgra8Unorm))
    Assert(!IsTwinFormat(TextureFormat.R8Unorm, TextureFormat.R8Snorm))
}

test "guaranteed capabilities and vertex formats" {
    Assert(CapsSatisfyGuaranteed(RowOf(TextureFormat.Rgba8Unorm).guaranteed, RowOf(TextureFormat.Rgba8Unorm).guaranteed))
    var weak := RowOf(TextureFormat.Rgba8Unorm).guaranteed
    weak.blendable = false
    Assert(!CapsSatisfyGuaranteed(weak, RowOf(TextureFormat.Rgba8Unorm).guaranteed))

    Assert(VertexInfoOf(VertexFormat.Float32x3).bytes == 12)
    Assert(VertexInfoOf(VertexFormat.Float32x3).num_class == NumClassFloat)
    Assert(VertexInfoOf(VertexFormat.Uint32x2).num_class == NumClassUint)
    Assert(VertexInfoOf(VertexFormat.Sint16x4).num_class == NumClassSint)
    Assert(IndexByteSize(IndexFormat.Uint16) == 2)
    Assert(IndexByteSize(IndexFormat.Uint32) == 4)
}
