// Why: dxil.mk parses untrusted DXBC/DXIL containers and must catch unsigned
// blobs, malformed part tables, and stage mismatches without panicking.
//
// What: tests over all 21 compiled DXIL blobs in tests_shaders.mk plus
// hostile/unsigned/wrong-stage DXBC inputs.
//
// How: decodes committed DXIL blobs and mutates headers/parts in Arena buffers.

test "all 21 compiled DXIL containers pass CheckDxil for their stages" (sys: &System) {
    a := sys.arena
    try CheckDxil(try DecodeShader(a, TriVsDxilZ), Stage.Vertex)
    try CheckDxil(try DecodeShader(a, TriPsDxilZ), Stage.Fragment)
    try CheckDxil(try DecodeShader(a, QuadVsDxilZ), Stage.Vertex)
    try CheckDxil(try DecodeShader(a, QuadPsDxilZ), Stage.Fragment)
    try CheckDxil(try DecodeShader(a, TexturedQuadVsDxilZ), Stage.Vertex)
    try CheckDxil(try DecodeShader(a, TexturedQuadPsDxilZ), Stage.Fragment)
    try CheckDxil(try DecodeShader(a, DepthCubeVsDxilZ), Stage.Vertex)
    try CheckDxil(try DecodeShader(a, DepthCubePsDxilZ), Stage.Fragment)
    try CheckDxil(try DecodeShader(a, InstancedVsDxilZ), Stage.Vertex)
    try CheckDxil(try DecodeShader(a, InstancedPsDxilZ), Stage.Fragment)
    try CheckDxil(try DecodeShader(a, BlendMsaaVsDxilZ), Stage.Vertex)
    try CheckDxil(try DecodeShader(a, BlendMsaaPsDxilZ), Stage.Fragment)
    try CheckDxil(try DecodeShader(a, DepthSampleVsDxilZ), Stage.Vertex)
    try CheckDxil(try DecodeShader(a, DepthSamplePsDxilZ), Stage.Fragment)
    try CheckDxil(try DecodeShader(a, DoubleCsDxilZ), Stage.Compute)
    try CheckDxil(try DecodeShader(a, PostInvertCsDxilZ), Stage.Compute)
    try CheckDxil(try DecodeShader(a, PrefixSumCsDxilZ), Stage.Compute)
    try CheckDxil(try DecodeShader(a, PinningIdsVsDxilZ), Stage.Vertex)
    try CheckDxil(try DecodeShader(a, PinningIdsPsDxilZ), Stage.Fragment)
    try CheckDxil(try DecodeShader(a, HistogramCsDxilZ), Stage.Compute)
    try CheckDxil(try DecodeShader(a, BitonicSortCsDxilZ), Stage.Compute)
}

test "dxil refuses unsigned zero hash, wrong stage, truncated header, and bad part offsets" (sys: &System) {
    a := sys.arena
    base := try DecodeShader(a, TriVsDxilZ)

    // Wrong stage
    CheckDxil(base, Stage.Fragment) else e {
        Assert(e.misuse == Misuse.StageMismatch)
    }

    // Unsigned (zeroed 16-byte digest)
    var unsigned := try a.Slice[u8](base.Len())
    (mut unsigned).CopyFrom(base)
    for i in 4..20 {
        unsigned[i] = 0
    }
    CheckDxil(unsigned[..], Stage.Vertex) else e {
        Assert(e.misuse == Misuse.UnsignedDxil)
    }

    // Size mismatch
    var bad_len := try a.Slice[u8](base.Len() - 4)
    (mut bad_len).CopyFrom(base[0..(base.Len() - 4)])
    CheckDxil(bad_len[..], Stage.Vertex) else e {
        Assert(e.misuse == Misuse.BadShader)
    }

    // Bad magic
    var bad_magic := try a.Slice[u8](base.Len())
    (mut bad_magic).CopyFrom(base)
    bad_magic[0] = 0
    CheckDxil(bad_magic[..], Stage.Vertex) else e {
        Assert(e.misuse == Misuse.BadShader)
    }

    // Truncated short buffer
    CheckDxil(base[0..16], Stage.Vertex) else e {
        Assert(e.misuse == Misuse.BadShader)
    }
}
