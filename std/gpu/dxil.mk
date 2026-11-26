// Why: D3D12 shader blobs arrive as DXBC containers that must be structurally
// valid, signed (non-zero hash), and match the declared shader stage before
// reaching CreatePipeline.
//
// What: CheckDxil — bounded O(bytes) DXBC/DXIL container validator.
//
// How: checks the 32-byte DXBC header, non-zero 16-byte hash (UnsignedDxil),
// exact length, part table bounds, and the DXIL program header's shader kind.

const DxbcMagic: u32 = 0x43425844
const DxilFourCC: u32 = 0x4C495844

// DXIL ShaderKind values from DXBC program header (bits 16..31 of word 0):
// 0 = Pixel, 1 = Vertex, 2 = Geometry, 3 = Hull, 4 = Domain, 5 = Compute.
DxilProgramKindForStage(s: Stage): u32 {
    return match s {
        .Fragment { 0 }
        .Vertex { 1 }
        .Compute { 5 }
    }
}

CheckDxil(b: []u8, stage: Stage): Invalid!void {
    n := b.Len()
    if n < 32 {
        fail Invalid(misuse: Misuse.BadShader, at: -1)
    }
    if ReadU32Le(b, 0) != DxbcMagic {
        fail Invalid(misuse: Misuse.BadShader, at: 0)
    }
    // 16-byte DXBC digest at [4..20]: all zero means unsigned DXIL.
    var hash_nonzero := false
    for i in 4..20 {
        if b[i] != 0 {
            hash_nonzero = true
            break
        }
    }
    if !hash_nonzero {
        fail Invalid(misuse: Misuse.UnsignedDxil, at: 4)
    }
    total_size := i64(ReadU32Le(b, 24))
    if total_size != i64(n) {
        fail Invalid(misuse: Misuse.BadShader, at: 24)
    }
    part_count := i64(ReadU32Le(b, 28))
    if part_count <= 0 || part_count > 64 {
        fail Invalid(misuse: Misuse.BadShader, at: 28)
    }
    header_end := i64(32) + part_count * 4
    if header_end > total_size {
        fail Invalid(misuse: Misuse.BadShader, at: 28)
    }

    want_kind := DxilProgramKindForStage(stage)
    var seen_dxil := false

    for i in 0..int(part_count) {
        part_off := i64(ReadU32Le(b, 32 + i * 4))
        if part_off < header_end || part_off + 8 > total_size {
            fail Invalid(misuse: Misuse.BadShader, at: 32 + i * 4)
        }
        po := int(part_off)
        tag := ReadU32Le(b, po)
        part_sz := i64(ReadU32Le(b, po + 4))
        if part_off + 8 + part_sz > total_size {
            fail Invalid(misuse: Misuse.BadShader, at: po + 4)
        }
        if tag == DxilFourCC {
            seen_dxil = true
            if part_sz < 24 {
                fail Invalid(misuse: Misuse.BadShader, at: po)
            }
            prog_ver := ReadU32Le(b, po + 8)
            kind := (prog_ver >> 16) & 0xFFFF
            if kind != want_kind {
                fail Invalid(misuse: Misuse.StageMismatch, at: po + 8)
            }
            size_words := i64(ReadU32Le(b, po + 12))
            if size_words * 4 > part_sz {
                fail Invalid(misuse: Misuse.BadShader, at: po + 12)
            }
            if ReadU32Le(b, po + 16) != DxilFourCC {
                fail Invalid(misuse: Misuse.BadShader, at: po + 16)
            }
            bc_off := i64(ReadU32Le(b, po + 24))
            bc_sz := i64(ReadU32Le(b, po + 28))
            if bc_off < 16 || bc_sz <= 0 || i64(8) + bc_off + bc_sz > part_sz {
                fail Invalid(misuse: Misuse.BadShader, at: po + 24)
            }
        }
    }
    if !seen_dxil {
        fail Invalid(misuse: Misuse.BadShader, at: -1)
    }
}
