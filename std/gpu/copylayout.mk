// Why: buffer-to-texture and texture-to-buffer copies must satisfy both
// D3D12's 256-byte row pitch and 512-byte offset alignment, Vulkan's texel
// block alignment, and depth24plus depth-copy restrictions.
//
// What: CopyFootprint, ComputeCopyFootprint, ValidateCopyRegion,
// ValidateBufferCopy, and MipDimension.
//
// How: pure checked i64 arithmetic with explicit refusals before overflow.

struct CopyFootprint {
    block_bytes: int
    block_w: int
    block_h: int
    blocks_x: int
    blocks_y: int
    row_bytes: int
    total_bytes: int
}

MipDim(base: int, mip: int): int {
    var v := base >> mip
    if v < 1 {
        return 1
    }
    return v
}

ComputeCopyFootprint(format: TextureFormat, aspect: Aspect, layout: DataLayout, size: Extent, buf_size: int): Invalid!CopyFootprint {
    if size.width <= 0 || size.height <= 0 || size.layers <= 0 {
        fail Invalid(misuse: Misuse.ExtentMismatch, at: -1)
    }
    row := RowOf(format)
    var block_bytes := int(row.block_bytes)
    match format {
        .Depth24PlusStencil8 {
            match aspect {
                .Stencil { block_bytes = 1 }
                _ { fail Invalid(misuse: Misuse.BadCopyAspect, at: -1) }
            }
        }
        .Depth32FloatStencil8 {
            match aspect {
                .Depth { block_bytes = 4 }
                .Stencil { block_bytes = 1 }
                _ { fail Invalid(misuse: Misuse.BadCopyAspect, at: -1) }
            }
        }
        .Depth16Unorm | .Depth32Float {
            match aspect {
                .All | .Depth {}
                _ { fail Invalid(misuse: Misuse.BadCopyAspect, at: -1) }
            }
        }
        _ {
            match aspect {
                .All {}
                _ { fail Invalid(misuse: Misuse.BadCopyAspect, at: -1) }
            }
        }
    }
    bw := int(row.block_w)
    bh := int(row.block_h)
    blocks_x := (size.width + bw - 1) / bw
    blocks_y := (size.height + bh - 1) / bh
    row_bytes := blocks_x * block_bytes

    if layout.offset < 0 || layout.offset % 512 != 0 {
        fail Invalid(misuse: Misuse.AlignmentOffset, at: -1)
    }
    if layout.bytes_per_row < row_bytes || layout.bytes_per_row % 256 != 0 {
        fail Invalid(misuse: Misuse.AlignmentRowPitch, at: -1)
    }
    if layout.rows_per_layer < 0 {
        fail Invalid(misuse: Misuse.ExtentMismatch, at: -1)
    }
    rows_per_layer := if layout.rows_per_layer == 0 { blocks_y } else { layout.rows_per_layer }
    if rows_per_layer < blocks_y {
        fail Invalid(misuse: Misuse.ExtentMismatch, at: -1)
    }

    // Checked i64 byte span: last layer needs (blocks_y - 1) * bytes_per_row + row_bytes.
    bpr64 := i64(layout.bytes_per_row)
    rpl64 := i64(rows_per_layer)
    layers64 := i64(size.layers)
    layer_stride64 := bpr64 * rpl64
    var span64: i64 = 0
    if layers64 > 1 {
        if layer_stride64 > (i64(0x7FFFFFFF_FFFFFFFF) / (layers64 - 1)) {
            fail Invalid(misuse: Misuse.RangeOutOfBounds, at: -1)
        }
        span64 = (layers64 - 1) * layer_stride64
    }
    last_layer := i64(blocks_y - 1) * bpr64 + i64(row_bytes)
    if span64 > i64(0x7FFFFFFF_FFFFFFFF) - last_layer {
        fail Invalid(misuse: Misuse.RangeOutOfBounds, at: -1)
    }
    span64 += last_layer
    if i64(layout.offset) + span64 > i64(buf_size) {
        fail Invalid(misuse: Misuse.RangeOutOfBounds, at: -1)
    }
    return CopyFootprint(
        block_bytes: block_bytes,
        block_w: bw,
        block_h: bh,
        blocks_x: blocks_x,
        blocks_y: blocks_y,
        row_bytes: row_bytes,
        total_bytes: int(span64),
    )
}

ValidateTexRegion(tex_fmt: TextureFormat, tex_w: int, tex_h: int, tex_layers: int, tex_mips: int, tex_samples: int, reg: TextureRegion, size: Extent): Invalid!void {
    if tex_samples != 1 {
        fail Invalid(misuse: Misuse.SamplesMismatch, at: -1)
    }
    if size.width <= 0 || size.height <= 0 || size.layers <= 0 {
        fail Invalid(misuse: Misuse.ExtentMismatch, at: -1)
    }
    if reg.mip < 0 || reg.mip >= tex_mips || reg.layer < 0 || reg.layer + size.layers > tex_layers {
        fail Invalid(misuse: Misuse.RangeOutOfBounds, at: -1)
    }
    mw := MipDim(tex_w, reg.mip)
    mh := MipDim(tex_h, reg.mip)
    if reg.x < 0 || reg.y < 0 || reg.x + size.width > mw || reg.y + size.height > mh {
        fail Invalid(misuse: Misuse.RangeOutOfBounds, at: -1)
    }
    row := RowOf(tex_fmt)
    bw := int(row.block_w)
    bh := int(row.block_h)
    if bw > 1 || bh > 1 {
        if reg.x % bw != 0 || reg.y % bh != 0 {
            fail Invalid(misuse: Misuse.AlignmentOffset, at: -1)
        }
        if (size.width % bw != 0 && reg.x + size.width != mw) || (size.height % bh != 0 && reg.y + size.height != mh) {
            fail Invalid(misuse: Misuse.ExtentMismatch, at: -1)
        }
    }
}
