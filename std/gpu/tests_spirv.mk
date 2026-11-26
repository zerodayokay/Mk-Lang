// Why: spirv.mk parses untrusted shader bytecode and enforces binding,
// push-constant, and capability rules before reaching GPU drivers.
//
// What: tests over all 21 compiled shaders in tests_shaders.mk, hand-
// assembled valid/invalid SPIR-V modules, and hostile fuzz-style inputs.
//
// How: constructs little-endian SPIR-V word lists and asserts exact errors
// without panicking.

PushWord(a: &Arena, buf: mut List[u8], w: u32) {
    _ = a
    buf.Push(Trunc[u8](w & 0xFF)).Must()
    buf.Push(Trunc[u8]((w >> 8) & 0xFF)).Must()
    buf.Push(Trunc[u8]((w >> 16) & 0xFF)).Must()
    buf.Push(Trunc[u8]((w >> 24) & 0xFF)).Must()
}

PushOp(a: &Arena, buf: mut List[u8], op: u16, wc: int) {
    PushWord(a, buf, u32(op) | (Cast[u32](wc) << 16))
}

PushOp1(a: &Arena, buf: mut List[u8], op: u16, w1: u32) {
    PushOp(a, buf, op, 2)
    PushWord(a, buf, w1)
}

PushOp2(a: &Arena, buf: mut List[u8], op: u16, w1: u32, w2: u32) {
    PushOp(a, buf, op, 3)
    PushWord(a, buf, w1)
    PushWord(a, buf, w2)
}

PushOp3(a: &Arena, buf: mut List[u8], op: u16, w1: u32, w2: u32, w3: u32) {
    PushOp(a, buf, op, 4)
    PushWord(a, buf, w1)
    PushWord(a, buf, w2)
    PushWord(a, buf, w3)
}

PushOp4(a: &Arena, buf: mut List[u8], op: u16, w1: u32, w2: u32, w3: u32, w4: u32) {
    PushOp(a, buf, op, 5)
    PushWord(a, buf, w1)
    PushWord(a, buf, w2)
    PushWord(a, buf, w3)
    PushWord(a, buf, w4)
}

PushStrWords(a: &Arena, buf: mut List[u8], s: str) {
    bs := s.AsBytes()
    words := (bs.Len() + 4) / 4
    for wi in 0..words {
        var w: u32 = 0
        for b in 0..4 {
            idx := wi * 4 + b
            if idx < bs.Len() {
                w |= u32(bs[idx]) << Cast[u32](b * 8)
            }
        }
        PushWord(a, buf, w)
    }
}

PushHeader(a: &Arena, buf: mut List[u8], ver: u32, bound: u32) {
    PushWord(a, buf, SpirvMagic)
    PushWord(a, buf, ver)
    PushWord(a, buf, 0)
    PushWord(a, buf, bound)
    PushWord(a, buf, 0)
}

test "all 21 compiled DXC SPIR-V shaders pass CheckShader" (sys: &System) {
    a := sys.arena
    try CheckShader(ShaderCode(spirv: try DecodeShader(a, TriVsSpirvZ), dxil: try DecodeShader(a, TriVsDxilZ)), Stage.Vertex, "VsMain")
    try CheckShader(ShaderCode(spirv: try DecodeShader(a, TriPsSpirvZ), dxil: try DecodeShader(a, TriPsDxilZ)), Stage.Fragment, "PsMain")
    try CheckShader(ShaderCode(spirv: try DecodeShader(a, QuadVsSpirvZ), dxil: try DecodeShader(a, QuadVsDxilZ)), Stage.Vertex, "VsMain")
    try CheckShader(ShaderCode(spirv: try DecodeShader(a, QuadPsSpirvZ), dxil: try DecodeShader(a, QuadPsDxilZ)), Stage.Fragment, "PsMain")
    try CheckShader(ShaderCode(spirv: try DecodeShader(a, TexturedQuadVsSpirvZ), dxil: try DecodeShader(a, TexturedQuadVsDxilZ)), Stage.Vertex, "VsMain")
    try CheckShader(ShaderCode(spirv: try DecodeShader(a, TexturedQuadPsSpirvZ), dxil: try DecodeShader(a, TexturedQuadPsDxilZ)), Stage.Fragment, "PsMain")
    try CheckShader(ShaderCode(spirv: try DecodeShader(a, DepthCubeVsSpirvZ), dxil: try DecodeShader(a, DepthCubeVsDxilZ)), Stage.Vertex, "VsMain")
    try CheckShader(ShaderCode(spirv: try DecodeShader(a, DepthCubePsSpirvZ), dxil: try DecodeShader(a, DepthCubePsDxilZ)), Stage.Fragment, "PsMain")
    try CheckShader(ShaderCode(spirv: try DecodeShader(a, InstancedVsSpirvZ), dxil: try DecodeShader(a, InstancedVsDxilZ)), Stage.Vertex, "VsMain")
    try CheckShader(ShaderCode(spirv: try DecodeShader(a, InstancedPsSpirvZ), dxil: try DecodeShader(a, InstancedPsDxilZ)), Stage.Fragment, "PsMain")
    try CheckShader(ShaderCode(spirv: try DecodeShader(a, BlendMsaaVsSpirvZ), dxil: try DecodeShader(a, BlendMsaaVsDxilZ)), Stage.Vertex, "VsMain")
    try CheckShader(ShaderCode(spirv: try DecodeShader(a, BlendMsaaPsSpirvZ), dxil: try DecodeShader(a, BlendMsaaPsDxilZ)), Stage.Fragment, "PsMain")
    try CheckShader(ShaderCode(spirv: try DecodeShader(a, DepthSampleVsSpirvZ), dxil: try DecodeShader(a, DepthSampleVsDxilZ)), Stage.Vertex, "VsMain")
    try CheckShader(ShaderCode(spirv: try DecodeShader(a, DepthSamplePsSpirvZ), dxil: try DecodeShader(a, DepthSamplePsDxilZ)), Stage.Fragment, "PsMain")
    try CheckShader(ShaderCode(spirv: try DecodeShader(a, DoubleCsSpirvZ), dxil: try DecodeShader(a, DoubleCsDxilZ)), Stage.Compute, "CsMain")
    try CheckShader(ShaderCode(spirv: try DecodeShader(a, PostInvertCsSpirvZ), dxil: try DecodeShader(a, PostInvertCsDxilZ)), Stage.Compute, "CsMain")
    try CheckShader(ShaderCode(spirv: try DecodeShader(a, PrefixSumCsSpirvZ), dxil: try DecodeShader(a, PrefixSumCsDxilZ)), Stage.Compute, "CsMain")
    try CheckShader(ShaderCode(spirv: try DecodeShader(a, PinningIdsVsSpirvZ), dxil: try DecodeShader(a, PinningIdsVsDxilZ)), Stage.Vertex, "VsMain")
    try CheckShader(ShaderCode(spirv: try DecodeShader(a, PinningIdsPsSpirvZ), dxil: try DecodeShader(a, PinningIdsPsDxilZ)), Stage.Fragment, "PsMain")
    try CheckShader(ShaderCode(spirv: try DecodeShader(a, HistogramCsSpirvZ), dxil: try DecodeShader(a, HistogramCsDxilZ)), Stage.Compute, "CsMain")
    try CheckShader(ShaderCode(spirv: try DecodeShader(a, BitonicSortCsSpirvZ), dxil: try DecodeShader(a, BitonicSortCsDxilZ)), Stage.Compute, "CsMain")
}

test "spirv hostile header, word-count 0, bounds, unterminated string, and layout order" (sys: &System) {
    a := sys.arena

    // MissingSpirv
    CheckShader(ShaderCode(spirv: "".AsBytes(), dxil: "".AsBytes()), Stage.Vertex, "VsMain") else e {
        match e {
            Invalid(misuse, at: _) { Assert(misuse == Misuse.MissingSpirv) }
            _ { Assert(false) }
        }
    }

    // Word count 0 must not loop
    var wc0 := List[u8].New(a)
    PushHeader(a, mut wc0, 0x00010600, 10)
    PushWord(a, mut wc0, u32(SpvOpCapability))
    CheckSpirv(wc0.Items(), Stage.Vertex, "main") else e {
        match e {
            Invalid(misuse, at: _) { Assert(misuse == Misuse.BadShader) }
            _ { Assert(false) }
        }
    }

    // Bound past cap (4,194,304)
    var bcap := List[u8].New(a)
    PushHeader(a, mut bcap, 0x00010600, 4194305)
    CheckSpirv(bcap.Items(), Stage.Vertex, "main") else e {
        match e {
            Invalid(misuse, at: _) { Assert(misuse == Misuse.BadShader) }
            _ { Assert(false) }
        }
    }

    // ID >= bound
    var idoob := List[u8].New(a)
    PushHeader(a, mut idoob, 0x00010600, 5)
    PushOp1(a, mut idoob, SpvOpTypeVoid, 5)
    CheckSpirv(idoob.Items(), Stage.Vertex, "main") else e {
        match e {
            Invalid(misuse, at: _) { Assert(misuse == Misuse.BadShader) }
            _ { Assert(false) }
        }
    }

    // Unterminated string in OpEntryPoint
    var ustr := List[u8].New(a)
    PushHeader(a, mut ustr, 0x00010600, 10)
    PushOp3(a, mut ustr, SpvOpEntryPoint, SpvExecVertex, 1, 0x41424344)
    CheckSpirv(ustr.Items(), Stage.Vertex, "main") else e {
        match e {
            Invalid(misuse, at: _) { Assert(misuse == Misuse.BadShader) }
            _ { Assert(false) }
        }
    }

    // Decoration after first OpFunction
    var late_dec := List[u8].New(a)
    PushHeader(a, mut late_dec, 0x00010600, 10)
    PushOp(a, mut late_dec, SpvOpEntryPoint, 5)
    PushWord(a, mut late_dec, SpvExecVertex)
    PushWord(a, mut late_dec, 1)
    PushStrWords(a, mut late_dec, "main")
    PushOp1(a, mut late_dec, SpvOpTypeVoid, 2)
    PushOp4(a, mut late_dec, SpvOpFunction, 2, 1, 0, 2)
    PushOp2(a, mut late_dec, SpvOpDecorate, 3, SpvDecBlock)
    CheckSpirv(late_dec.Items(), Stage.Vertex, "main") else e {
        match e {
            Invalid(misuse, at: _) { Assert(misuse == Misuse.BadShader) }
            _ { Assert(false) }
        }
    }
}

test "spirv binding convention: duplicate binding, sampled image, counter, push-constant row straddle, deep types" (sys: &System) {
    a := sys.arena

    // Duplicate (set=0, binding=0) across two variables refused.
    var dup := List[u8].New(a)
    PushHeader(a, mut dup, 0x00010600, 20)
    PushOp(a, mut dup, SpvOpEntryPoint, 5)
    PushWord(a, mut dup, SpvExecVertex)
    PushWord(a, mut dup, 1)
    PushStrWords(a, mut dup, "main")
    PushOp3(a, mut dup, SpvOpDecorate, 10, SpvDecDescriptorSet, 0)
    PushOp3(a, mut dup, SpvOpDecorate, 10, SpvDecBinding, 0)
    PushOp3(a, mut dup, SpvOpDecorate, 11, SpvDecDescriptorSet, 0)
    PushOp3(a, mut dup, SpvOpDecorate, 11, SpvDecBinding, 0)
    PushOp2(a, mut dup, SpvOpDecorate, 4, SpvDecBlock)
    PushOp2(a, mut dup, SpvOpTypeFloat, 3, 32)
    PushOp2(a, mut dup, SpvOpTypeStruct, 4, 3)
    PushOp3(a, mut dup, SpvOpTypePointer, 5, SpvScUniform, 4)
    PushOp3(a, mut dup, SpvOpVariable, 5, 10, SpvScUniform)
    PushOp3(a, mut dup, SpvOpVariable, 5, 11, SpvScUniform)
    CheckSpirv(dup.Items(), Stage.Vertex, "main") else e {
        match e {
            Invalid(misuse, at: _) { Assert(misuse == Misuse.ShaderMismatch) }
            _ { Assert(false) }
        }
    }

    // Combined sampler variable (UniformConstant pointing to OpTypeSampledImage) refused.
    var simp := List[u8].New(a)
    PushHeader(a, mut simp, 0x00010600, 12)
    PushOp(a, mut simp, SpvOpEntryPoint, 5)
    PushWord(a, mut simp, SpvExecFragment)
    PushWord(a, mut simp, 1)
    PushStrWords(a, mut simp, "main")
    PushOp3(a, mut simp, SpvOpDecorate, 9, SpvDecDescriptorSet, 0)
    PushOp3(a, mut simp, SpvOpDecorate, 9, SpvDecBinding, 0)
    PushOp2(a, mut simp, SpvOpTypeFloat, 2, 32)
    PushOp(a, mut simp, SpvOpTypeImage, 9)
    PushWord(a, mut simp, 3)
    PushWord(a, mut simp, 2)
    PushWord(a, mut simp, 1)
    PushWord(a, mut simp, 0)
    PushWord(a, mut simp, 0)
    PushWord(a, mut simp, 0)
    PushWord(a, mut simp, 1)
    PushWord(a, mut simp, 0)
    PushOp2(a, mut simp, SpvOpTypeSampledImage, 4, 3)
    PushOp3(a, mut simp, SpvOpTypePointer, 5, SpvScUniformConstant, 4)
    PushOp3(a, mut simp, SpvOpVariable, 5, 9, SpvScUniformConstant)
    CheckSpirv(simp.Items(), Stage.Fragment, "main") else e {
        match e {
            Invalid(misuse, at: _) { Assert(misuse == Misuse.ShaderMismatch) }
            _ { Assert(false) }
        }
    }

    // CounterBuffer decoration refused.
    var cnt := List[u8].New(a)
    PushHeader(a, mut cnt, 0x00010600, 10)
    PushOp(a, mut cnt, SpvOpEntryPoint, 5)
    PushWord(a, mut cnt, SpvExecGlCompute)
    PushWord(a, mut cnt, 1)
    PushStrWords(a, mut cnt, "main")
    PushOp3(a, mut cnt, SpvOpDecorate, 2, SpvDecCounterBuffer, 3)
    CheckSpirv(cnt.Items(), Stage.Compute, "main") else e {
        match e {
            Invalid(misuse, at: _) { Assert(misuse == Misuse.ShaderMismatch) }
            _ { Assert(false) }
        }
    }

    // Push constant member straddling 16-byte row (vec3 at offset 8 -> bytes 8..20).
    var pc := List[u8].New(a)
    PushHeader(a, mut pc, 0x00010600, 20)
    PushOp(a, mut pc, SpvOpEntryPoint, 5)
    PushWord(a, mut pc, SpvExecVertex)
    PushWord(a, mut pc, 1)
    PushStrWords(a, mut pc, "main")
    PushOp4(a, mut pc, SpvOpMemberDecorate, 4, 0, SpvDecOffset, 8)
    PushOp2(a, mut pc, SpvOpTypeFloat, 2, 32)
    PushOp3(a, mut pc, SpvOpTypeVector, 3, 2, 3)
    PushOp2(a, mut pc, SpvOpTypeStruct, 4, 3)
    PushOp3(a, mut pc, SpvOpTypePointer, 5, SpvScPushConstant, 4)
    PushOp3(a, mut pc, SpvOpVariable, 5, 6, SpvScPushConstant)
    CheckSpirv(pc.Items(), Stage.Vertex, "main") else e {
        match e {
            Invalid(misuse, at: _) { Assert(misuse == Misuse.ShaderMismatch) }
            _ { Assert(false) }
        }
    }

    // Deep type chain > 64 fails cleanly without stack overflow.
    var deep := List[u8].New(a)
    PushHeader(a, mut deep, 0x00010600, 200)
    PushOp(a, mut deep, SpvOpEntryPoint, 5)
    PushWord(a, mut deep, SpvExecVertex)
    PushWord(a, mut deep, 1)
    PushStrWords(a, mut deep, "main")
    PushOp2(a, mut deep, SpvOpDecorate, 75, SpvDecBlock)
    PushOp3(a, mut deep, SpvOpDecorate, 80, SpvDecDescriptorSet, 0)
    PushOp3(a, mut deep, SpvOpDecorate, 80, SpvDecBinding, 0)
    PushOp2(a, mut deep, SpvOpTypeFloat, 10, 32)
    for k in 11..76 {
        PushOp2(a, mut deep, SpvOpTypeStruct, Cast[u32](k), Cast[u32](k - 1))
    }
    PushOp3(a, mut deep, SpvOpTypePointer, 76, SpvScUniform, 75)
    PushOp3(a, mut deep, SpvOpVariable, 76, 80, SpvScUniform)
    CheckSpirv(deep.Items(), Stage.Vertex, "main") else e {
        match e {
            Invalid(misuse, at: _) { Assert(misuse == Misuse.BadShader) }
            _ { Assert(false) }
        }
    }
}
