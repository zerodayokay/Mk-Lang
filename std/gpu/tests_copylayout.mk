// Why: buffer/texture copy validation prevents D3D12/Vulkan pitch errors,
// overflow, and illegal depth24plus depth copies.
//
// What: unit tests for copylayout.mk covering RGBA8, BC compressed blocks,
// depth/stencil aspects, 256-byte pitch, 512-byte offset, and overflow limits.
//
// How: pure test blocks asserting accepted footprints and exact Misuse codes.

test "copy footprint for RGBA8 and BC1/BC7 blocks" {
    fp := ComputeCopyFootprint(
        TextureFormat.Rgba8Unorm,
        Aspect.All,
        DataLayout(offset: 0, bytes_per_row: 256, rows_per_layer: 4),
        Extent(width: 64, height: 4, layers: 2),
        1024 + 256 * 4,
    ).Must()
    Assert(fp.block_bytes == 4 && fp.blocks_x == 64 && fp.blocks_y == 4 && fp.row_bytes == 256)
    Assert(fp.total_bytes == 256 * 4 + 256 * 3 + 256)

    bc := ComputeCopyFootprint(
        TextureFormat.Bc1Unorm,
        Aspect.All,
        DataLayout(offset: 512, bytes_per_row: 256, rows_per_layer: 2),
        Extent(width: 8, height: 8, layers: 1),
        512 + 256 + 16,
    ).Must()
    Assert(bc.block_bytes == 8 && bc.block_w == 4 && bc.block_h == 4)
    Assert(bc.blocks_x == 2 && bc.blocks_y == 2 && bc.row_bytes == 16)
    Assert(bc.total_bytes == 256 + 16)
}

test "copy footprint refuses bad pitch, bad offset, overflow and depth24plus depth" {
    // Unaligned row pitch
    _ = ComputeCopyFootprint(
        TextureFormat.Rgba8Unorm, Aspect.All,
        DataLayout(offset: 0, bytes_per_row: 128, rows_per_layer: 1),
        Extent(width: 16, height: 1, layers: 1), 1024,
    ) else e {
        Assert(e.misuse == Misuse.AlignmentRowPitch)
    }

    // Unaligned offset (not 512-byte aligned)
    _ = ComputeCopyFootprint(
        TextureFormat.Rgba8Unorm, Aspect.All,
        DataLayout(offset: 256, bytes_per_row: 256, rows_per_layer: 1),
        Extent(width: 16, height: 1, layers: 1), 1024,
    ) else e {
        Assert(e.misuse == Misuse.AlignmentOffset)
    }

    // Buffer too small
    _ = ComputeCopyFootprint(
        TextureFormat.Rgba8Unorm, Aspect.All,
        DataLayout(offset: 0, bytes_per_row: 256, rows_per_layer: 2),
        Extent(width: 16, height: 2, layers: 1), 300,
    ) else e {
        Assert(e.misuse == Misuse.RangeOutOfBounds)
    }

    // Depth24PlusStencil8 refuses Depth and All, allows Stencil
    _ = ComputeCopyFootprint(
        TextureFormat.Depth24PlusStencil8, Aspect.Depth,
        DataLayout(offset: 0, bytes_per_row: 256, rows_per_layer: 1),
        Extent(width: 4, height: 1, layers: 1), 1024,
    ) else e {
        Assert(e.misuse == Misuse.BadCopyAspect)
    }
    st_fp := ComputeCopyFootprint(
        TextureFormat.Depth24PlusStencil8, Aspect.Stencil,
        DataLayout(offset: 0, bytes_per_row: 256, rows_per_layer: 1),
        Extent(width: 16, height: 1, layers: 1), 1024,
    ).Must()
    Assert(st_fp.block_bytes == 1 && st_fp.row_bytes == 16)

    // Depth32FloatStencil8 requires Depth or Stencil, refuses All
    _ = ComputeCopyFootprint(
        TextureFormat.Depth32FloatStencil8, Aspect.All,
        DataLayout(offset: 0, bytes_per_row: 256, rows_per_layer: 1),
        Extent(width: 4, height: 1, layers: 1), 1024,
    ) else e {
        Assert(e.misuse == Misuse.BadCopyAspect)
    }
}

test "compressed texture region alignment and edge clamping" {
    t := Texture(slot: 1, generation: 1, device: 1)
    reg := TextureRegion(texture: &t, mip: 1, layer: 0, x: 0, y: 0, aspect: Aspect.All)
    // Mip 1 of 12x12 is 6x6; copying 6x6 from (0,0) reaches the mip edge and is valid.
    ValidateTexRegion(TextureFormat.Bc7Unorm, 12, 12, 1, 2, 1, reg, Extent(width: 6, height: 6, layers: 1)).Must()
    // Non-edge 6x4 fails ExtentMismatch.
    ValidateTexRegion(TextureFormat.Bc7Unorm, 16, 16, 1, 2, 1, reg, Extent(width: 6, height: 4, layers: 1)) else e {
        Assert(e.misuse == Misuse.ExtentMismatch)
    }
    Texture(slot: _, generation: _, device: _) := t
}
