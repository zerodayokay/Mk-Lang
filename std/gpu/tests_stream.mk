// Why: the portable command stream must stay 8-byte aligned, drop redundant
// state changes, patch BarrierRef at RenderPass.End, recycle 64 KiB chunks,
// and refuse runaway streams past 1,048,576 commands.
//
// What: unit tests for stream.mk and pass shadow-state redundancy/BarrierRef.
//
// How: uses CommandStream and OpenStub to inspect recorded opcodes and chunks.

test "stream packing, chunk overflow, and recycling" (sys: &System) {
    a := sys.heap.Alloc()
    var pool := ChunkPool.New(a)
    var st := CommandStream.New(a)
    defer {
        st.FreeAll(a)
        pool.FreeAll(a)
    }

    // Write enough 4-word records to cross a 64 KiB chunk boundary (8192 u64 words).
    for i in 0..2100 {
        try st.Write4(a, mut pool, PackHdr(OpDrawIndexed, 4, 0, Cast[u32](i)), 1, 2, 3)
    }
    Assert(st.chunk_ids.Len() == 2)
    Assert(st.cmd_count == 2100)
    c0 := st.chunk_ids[0]
    Assert(UnpackOp(pool.chunks[c0].Ref().words[0]) == OpDrawIndexed)
    Assert(UnpackWords(pool.chunks[c0].Ref().words[0]) == 4)
    Assert(UnpackArg(pool.chunks[c0].Ref().words[4]) == 1)

    // PatchBarrierRef updates an earlier location in place.
    loc := try st.Write2(a, mut pool, PackHdr(OpBarrierRef, 2, 0, 0), 0)
    st.PatchBarrierRef(mut pool, loc, 7, 3, 1)
    cid := st.chunk_ids[loc.chunk_idx]
    w0 := pool.chunks[cid].Ref().words[loc.word_idx]
    w1 := pool.chunks[cid].Ref().words[loc.word_idx + 1]
    Assert(UnpackOp(w0) == OpBarrierRef && UnpackArg(w0) == 7 && UnpackFlags(w0) == 1)
    Assert(Cast[u32](w1) == 3)

    st.RecycleToPool(a, mut pool, 1, false)
    Assert(pool.inflight.Len() == 2)
    pool.Recycle(a, 1)
    Assert(pool.free_ids.Len() == 2 && pool.inflight.Len() == 0)
}

test "stream enforces MaxStreamCommands bound" (sys: &System) {
    a := sys.heap.Alloc()
    var pool := ChunkPool.New(a)
    var st := CommandStream.New(a)
    defer {
        st.FreeAll(a)
        pool.FreeAll(a)
    }
    st.cmd_count = MaxStreamCommands
    try st.Write1(a, mut pool, PackHdr(OpPopDebugGroup, 1, 0, 0))
    Assert(st.HasError())
    Assert(st.first_error.Must().misuse == Misuse.TooManyCommands)
}

test "render pass redundancy removal and BarrierRef patching via stub" (sys: &System) {
    var dev := try OpenStub(sys.heap, DefaultDeviceOptions)
    defer { _ = dev.Close(sys.io) }

    vs_blob := try DecodeShader(sys.arena, TriVsSpirvZ)
    ps_blob := try DecodeShader(sys.arena, TriPsSpirvZ)
    vs := try dev.NewShader(ShaderCode(spirv: vs_blob, dxil: "".AsBytes()), Stage.Vertex, "VsMain", "vs")
    defer vs.Close(mut dev)
    ps := try dev.NewShader(ShaderCode(spirv: ps_blob, dxil: "".AsBytes()), Stage.Fragment, "PsMain", "ps")
    defer ps.Close(mut dev)
    var no_groups: [0]&BindGroupLayout = []
    pl := try dev.NewPipelineLayout(no_groups[..], 0, "empty")
    defer pl.Close(mut dev)
    targets := [ColorOf(TextureFormat.Rgba8Unorm)]
    var pd := DefaultRenderPipeline
    pd.targets = targets[..]
    pipe := try dev.NewRenderPipeline(sys.io, &pl, &vs, some(&ps), pd)
    defer pipe.Close(mut dev)

    var td := DefaultTextureDesc
    td.width = 16
    td.height = 16
    td.usage.render_target = true
    tex := try dev.NewTexture(td)
    defer tex.Close(mut dev)
    view_ := try dev.NewView(&tex, DefaultViewDesc)
    defer view_.Close(mut dev)

    var enc := try dev.Encoder("frame")
    onerr enc.Abandon()
    colors := [ColorAttachment(target: &view_, resolve: none, load: Load.Clear(r: 0.0, g: 0.0, b: 0.0, a: 1.0), store: Store.Store)]
    desc := PassDesc(colors: colors[..], depth: none, label: "p")
    var pass := enc.RenderPass(&desc)
    pass.SetPipeline(&pipe)
    pass.SetPipeline(&pipe)                          // redundant
    pass.SetViewport(0.0, 0.0, 16.0, 16.0, 0.0, 1.0) // matches default
    pass.SetScissor(0, 0, 16, 16)                    // matches default
    pass.SetBlendConstant(0.0, 0.0, 0.0, 0.0)        // matches default
    pass.SetStencilReference(0)                      // matches default
    pass.Draw(3, 1, 0, 0)
    pass.Draw(3, 1, 0, 0)
    pass.End()

    // Verify BarrierRef at begin was patched with 1 barrier (ResCommon -> ResRenderTarget).
    (barriers, w0: bw0, w1: bw1) := enc.InspectBarrierRef()
    Assert(barriers == 1)
    Assert(UnpackOp(bw0) == OpBarrierRef && Cast[u32](bw1) == 1)
    try enc.Submit()

    // Check recorded opcodes in stub: BarrierRef, BeginRendering, SetPipeline, Draw, Draw, EndRendering.
    match &dev.state.Ref().native {
        .Stub(s) {
            Assert(s.submitted_ops.Len() == 6)
            Assert(s.submitted_ops[0] == OpBarrierRef)
            Assert(s.submitted_ops[1] == OpBeginRendering)
            Assert(s.submitted_ops[2] == OpSetPipeline)
            Assert(s.submitted_ops[3] == OpDraw)
            Assert(s.submitted_ops[4] == OpDraw)
            Assert(s.submitted_ops[5] == OpEndRendering)
        }
        _ { Assert(false) }
    }
}
