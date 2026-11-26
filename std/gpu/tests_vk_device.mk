// Why: portable std.gpu must work end-to-end on real Vulkan hardware through
// every resource kind, SPIR-V S1 shader pipeline, offscreen render + compute
// dispatch, readback verification, dynamic uniforms, MSAA resolve, read-only
// depth sampling, and zero native leaks.
//
// What: hardware integration tests in std.gpu using Backend.Vulkan, exercising
// Adapters, Open, all resource kinds + zeroing, all S1 SPIR-V graphics and
// compute shaders, ReadTexture/MapRead verification, TakeMessages, and Close.
//
// How: queries Adapters(a, io, Backend.Vulkan), opens Backend.Vulkan, compiles
// pipelines from tests_shaders.mk SPIR-V blobs, executes draws and dispatches
// into offscreen targets/buffers, and verifies pixel/element outputs.

import std.rt

HasVkAdapter(sys: &System): Error!bool {
    var a := try Arena.New(sys.heap)
    defer a.Free()
    ads := Adapters(&a, sys.io, Backend.Vulkan) else e {
        match e {
            OutOfMemory { fail OutOfMemory }
            _ { return false }
        }
    }
    return ads.Len() > 0
}

OpenVkDev(sys: &System, want_bc: bool): Error!Device from sys.heap {
    var opts := DefaultDeviceOptions
    opts.backend = Backend.Vulkan
    opts.features.texture_compression_bc = want_bc
    return try Open(sys.heap, sys.io, opts)
}

TestWriteU32(b: mut []u8, at: int, v: u32) {
    b[at] = Trunc[u8](v)
    b[at + 1] = Trunc[u8](v >> 8)
    b[at + 2] = Trunc[u8](v >> 16)
    b[at + 3] = Trunc[u8](v >> 24)
}

WriteF32x4(buf: mut []u8, byte_at: int, x: f32, y: f32, z: f32, w: f32) {
    TestWriteU32(buf, byte_at, F32ToU32(x))
    TestWriteU32(buf, byte_at + 4, F32ToU32(y))
    TestWriteU32(buf, byte_at + 8, F32ToU32(z))
    TestWriteU32(buf, byte_at + 12, F32ToU32(w))
}

TestReadU32(b: []u8): u32 {
    return u32(b[0]) | (u32(b[1]) << 8) | (u32(b[2]) << 16) | (u32(b[3]) << 24)
}

MakeShader(dev: mut Device, a: &Arena, z_b64: str, stage: Stage, entry: str, lbl: str): Error!Shader {
    spv := DecodeShader(a, z_b64) else { fail Unsupported }
    empty_dx: [0]u8 = []
    return try dev.NewShader(ShaderCode(spirv: spv, dxil: empty_dx[..]), stage, entry, lbl)
}

NewSimpleTex(dev: mut Device, fmt: TextureFormat, w: int, h: int, layers: int, mips: int, samples: int, csrc: bool, cdst: bool, smp: bool, rt: bool, lbl: str): Error!Texture {
    var td := DefaultTextureDesc
    td.format = fmt
    td.width = w
    td.height = h
    td.layers = layers
    td.mips = mips
    td.samples = samples
    var tu := NoTextureUsage
    tu.copy_src = csrc
    tu.copy_dst = cdst
    tu.sampled = smp
    tu.render_target = rt
    td.usage = tu
    td.label = lbl
    return try dev.NewTexture(td)
}

VkTestSkipReason(sys: &System): Error!?str {
    match rt.Env("MK_GPU_ADAPTER") {
        some(v) {
            if v.Eq(&"warp") || v.Eq(&"d3d12") {
                return some(try sys.arena.Fmt("MK_GPU_ADAPTER={v}", (v)))
            }
        }
        none {}
    }
    if !(try HasVkAdapter(sys)) {
        return some("no vulkan adapter")
    }
    return none
}

test "gpu vulkan Adapters, Open, resource kinds, zeroing, and Close zero leaks" (sys: &System) {
    if r := try VkTestSkipReason(sys) {
        _ = sys.Print("skip: {s}\n", (s: r)) else {}
        return
    }
    var a := try Arena.New(sys.heap)
    defer a.Free()

    ads := try Adapters(&a, sys.io, Backend.Vulkan)
    Assert(ads.Len() >= 1)
    bc_ok := ads[0].features.texture_compression_bc

    var dev := try OpenVkDev(sys, bc_ok)
    defer {
        st := dev.Close(sys.io)
        Assert(st.native == 0)
    }

    Assert(dev.Backend() == Backend.Vulkan)
    Assert(dev.Limits().max_texture_size >= 4096)
    Assert(dev.FormatCaps(TextureFormat.Depth24PlusStencil8).depth_stencil)
    Assert(dev.FormatCaps(TextureFormat.Rgba8Unorm).render_target)

    var uv := NoBufferUsage
    uv.vertex = true
    uv.copy_dst = true
    var ui := NoBufferUsage
    ui.index = true
    ui.copy_dst = true
    var uu := NoBufferUsage
    uu.uniform = true
    uu.copy_dst = true
    var uud := NoBufferUsage
    uud.uniform_dynamic = true
    uud.copy_dst = true
    var us := NoBufferUsage
    us.storage = true
    us.copy_src = true
    us.copy_dst = true
    var umr := NoBufferUsage
    umr.map_read = true
    umr.copy_dst = true
    var umw := NoBufferUsage
    umw.map_write = true
    umw.copy_src = true

    bv := try dev.NewBuffer(256, uv, "bv")
    defer bv.Close(mut dev)
    bi := try dev.NewBuffer(256, ui, "bi")
    defer bi.Close(mut dev)
    bu := try dev.NewBuffer(256, uu, "bu")
    defer bu.Close(mut dev)
    bud := try dev.NewBuffer(512, uud, "bud")
    defer bud.Close(mut dev)
    bs := try dev.NewBuffer(256, us, "bs")
    defer bs.Close(mut dev)
    bmr := try dev.NewBuffer(256, umr, "bmr")
    defer bmr.Close(mut dev)
    bmw := try dev.NewBuffer(256, umw, "bmw")
    defer bmw.Close(mut dev)

    var td_rgba := DefaultTextureDesc
    td_rgba.format = TextureFormat.Rgba8Unorm
    td_rgba.width = 16
    td_rgba.height = 16
    td_rgba.mips = 2
    var tu_all := NoTextureUsage
    tu_all.copy_src = true
    tu_all.copy_dst = true
    tu_all.sampled = true
    tu_all.render_target = true
    td_rgba.usage = tu_all
    vfs_srgb := [TextureFormat.Rgba8UnormSrgb]
    td_rgba.view_formats = vfs_srgb[..]
    td_rgba.label = "t-rgba"
    t_rgba := try dev.NewTexture(td_rgba)
    defer t_rgba.Close(mut dev)

    t_d24 := try NewSimpleTex(mut dev, TextureFormat.Depth24PlusStencil8, 16, 16, 1, 1, 1, false, false, true, true, "t-d24")
    defer t_d24.Close(mut dev)
    t_d32 := try NewSimpleTex(mut dev, TextureFormat.Depth32Float, 16, 16, 1, 1, 1, true, false, true, true, "t-d32")
    defer t_d32.Close(mut dev)
    t_msaa := try NewSimpleTex(mut dev, TextureFormat.Rgba8Unorm, 16, 16, 1, 1, 4, false, false, false, true, "t-msaa")
    defer t_msaa.Close(mut dev)
    t_cube := try NewSimpleTex(mut dev, TextureFormat.Rgba8Unorm, 8, 8, 6, 1, 1, false, true, true, false, "t-cube")
    defer t_cube.Close(mut dev)
    t_arr := try NewSimpleTex(mut dev, TextureFormat.Rgba8Unorm, 8, 8, 4, 1, 1, false, true, true, false, "t-arr")
    defer t_arr.Close(mut dev)

    if bc_ok {
        t_bc1 := try NewSimpleTex(mut dev, TextureFormat.Bc1Unorm, 8, 8, 1, 1, 1, false, true, true, false, "t-bc1")
        t_bc1.Close(mut dev)
    }

    var vd_srgb := DefaultViewDesc
    vd_srgb.format = some(TextureFormat.Rgba8UnormSrgb)
    vd_srgb.dimension = some(ViewDimension.D2)
    vd_srgb.mips = some(1)
    vd_srgb.layers = some(1)
    v_srgb := try dev.NewView(&t_rgba, vd_srgb)
    defer v_srgb.Close(mut dev)

    var vd_depth := DefaultViewDesc
    vd_depth.aspect = Aspect.Depth
    vd_depth.mips = some(1)
    vd_depth.layers = some(1)
    v_depth := try dev.NewView(&t_d24, vd_depth)
    defer v_depth.Close(mut dev)

    var vd_cube := DefaultViewDesc
    vd_cube.dimension = some(ViewDimension.Cube)
    vd_cube.mips = some(1)
    vd_cube.layers = some(6)
    v_cube := try dev.NewView(&t_cube, vd_cube)
    defer v_cube.Close(mut dev)

    var vd_arr := DefaultViewDesc
    vd_arr.dimension = some(ViewDimension.D2Array)
    vd_arr.mips = some(1)
    vd_arr.first_layer = 1
    vd_arr.layers = some(2)
    v_arr := try dev.NewView(&t_arr, vd_arr)
    defer v_arr.Close(mut dev)

    smp1 := try dev.NewSampler(DefaultSamplerDesc)
    defer smp1.Close(mut dev)
    var sdc := DefaultSamplerDesc
    sdc.compare = some(Compare.Less)
    smp_cmp := try dev.NewSampler(sdc)
    defer smp_cmp.Close(mut dev)

    // Verify fresh texture zeroing via ReadTexture, and MapRead zeroing.
    zpx := try dev.ReadTexture(&a, sys.io, TextureRegion(texture: &t_rgba, mip: 0, layer: 0, x: 0, y: 0, aspect: Aspect.All), Extent(width: 16, height: 16, layers: 1))
    Assert(zpx.Len() == 16 * 16 * 4)
    for pi in 0..zpx.Len() {
        Assert(zpx[pi] == 0)
    }
    var mr := try dev.MapRead(sys.io, &bmr, 0, 256)
    mb := mr.Bytes()
    for bi2 in 0..mb.Len() {
        Assert(mb[bi2] == 0)
    }
    mr.Unmap()

    // Test MapWrite + Unmap + CopyBuffer + MapRead round-trip.
    var mw := try dev.MapWrite(sys.io, &bmw, 0, 16)
    mwb := mw.Bytes()
    mwb[0] = 0x12
    mwb[1] = 0x34
    mwb[2] = 0x56
    mwb[3] = 0x78
    mw.Unmap()

    var enc := try dev.Encoder("copy")
    enc.CopyBuffer(&bmw, 0, &bmr, 0, 16)
    enc.Submit() else { Panic("vk copy submit failed") }
    var mr2 := try dev.MapRead(sys.io, &bmr, 0, 16)
    Assert(TestReadU32(mr2.Bytes()[0..4]) == 0x78563412)
    mr2.Unmap()

    _ = try dev.TakeMessages(&a)
}

RunTriAndQuadTest(a: &Arena, io: Io, dev: mut Device, rt: &Texture, rt_v: &TextureView): Error!void {
    no_dyn: [0]u32 = []
    bgl_empty: [0]&BindGroupLayout = []
    tgts_rgba := [ColorOf(TextureFormat.Rgba8Unorm)]

    // 1. Tri shader
    tri_vs := try MakeShader(dev, a, TriVsSpirvZ, Stage.Vertex, "VsMain", "tri-vs")
    defer tri_vs.Close(dev)
    tri_ps := try MakeShader(dev, a, TriPsSpirvZ, Stage.Fragment, "PsMain", "tri-ps")
    defer tri_ps.Close(dev)
    pl_empty := try dev.NewPipelineLayout(bgl_empty[..], 0, "pl-empty")
    defer pl_empty.Close(dev)
    var pd_tri := DefaultRenderPipeline
    pd_tri.targets = tgts_rgba[..]
    pipe_tri := try dev.NewRenderPipeline(io, &pl_empty, &tri_vs, some(&tri_ps), pd_tri)
    defer pipe_tri.Close(dev)
    var e1 := try dev.Encoder("tri-pass")
    ca1 := [ColorAttachment(target: rt_v, resolve: none, load: Load.Clear(r: 0.0, g: 0.0, b: 0.0, a: 1.0), store: Store.Store)]
    var rp1 := e1.RenderPass(&PassDesc(colors: ca1[..], depth: none, label: "tri"))
    rp1.SetPipeline(&pipe_tri)
    rp1.Draw(3, 1, 0, 0)
    rp1.End()
    e1.Submit() else { Panic("tri submit failed") }
    px_tri := try dev.ReadTexture(a, io, TextureRegion(texture: rt, mip: 0, layer: 0, x: 0, y: 0, aspect: Aspect.All), Extent(width: 64, height: 64, layers: 1))
    Assert(px_tri[0] == 0 && px_tri[1] == 0 && px_tri[2] == 0 && px_tri[3] == 255)
    c_off := (32 * 64 + 32) * 4
    Assert(int(px_tri[c_off]) + int(px_tri[c_off + 1]) + int(px_tri[c_off + 2]) > 50 && px_tri[c_off + 3] == 255)

    // 2. Quad shader (vertex buffer + uniform_dynamic) + BlendMsaa reuse of quad vbuf
    quad_vs := try MakeShader(dev, a, QuadVsSpirvZ, Stage.Vertex, "VsMain", "quad-vs")
    defer quad_vs.Close(dev)
    quad_ps := try MakeShader(dev, a, QuadPsSpirvZ, Stage.Fragment, "PsMain", "quad-ps")
    defer quad_ps.Close(dev)
    bgl_quad := try dev.NewBindGroupLayout([LayoutEntry(binding: 0, visibility: Visibility(vertex: false, fragment: true, compute: false), kind: BindingKind.Uniform(dynamic: true, min_size: 16))][..], "bgl-quad")
    defer bgl_quad.Close(dev)
    pl_quad := try dev.NewPipelineLayout([&bgl_quad][..], 0, "pl-quad")
    defer pl_quad.Close(dev)
    var uv_dst := NoBufferUsage
    uv_dst.vertex = true
    uv_dst.copy_dst = true
    vbuf_quad := try dev.NewBuffer(48, uv_dst, "qvb")
    defer vbuf_quad.Close(dev)
    var qv_bytes: [48]u8 = [0; 48]
    TestWriteU32(mut qv_bytes[..], 0, F32ToU32(-1.0))
    TestWriteU32(mut qv_bytes[..], 4, F32ToU32(1.0))
    TestWriteU32(mut qv_bytes[..], 8, F32ToU32(1.0))
    TestWriteU32(mut qv_bytes[..], 12, F32ToU32(1.0))
    TestWriteU32(mut qv_bytes[..], 16, F32ToU32(-1.0))
    TestWriteU32(mut qv_bytes[..], 20, F32ToU32(-1.0))
    TestWriteU32(mut qv_bytes[..], 24, F32ToU32(1.0))
    TestWriteU32(mut qv_bytes[..], 28, F32ToU32(1.0))
    TestWriteU32(mut qv_bytes[..], 32, F32ToU32(1.0))
    TestWriteU32(mut qv_bytes[..], 36, F32ToU32(-1.0))
    TestWriteU32(mut qv_bytes[..], 40, F32ToU32(-1.0))
    TestWriteU32(mut qv_bytes[..], 44, F32ToU32(-1.0))
    try dev.WriteBuffer(&vbuf_quad, 0, qv_bytes[..])

    var uud_dst := NoBufferUsage
    uud_dst.uniform_dynamic = true
    uud_dst.copy_dst = true
    ubuf_quad := try dev.NewBuffer(512, uud_dst, "qubo")
    defer ubuf_quad.Close(dev)
    var qu_bytes: [272]u8 = [0; 272]
    WriteF32x4(mut qu_bytes[..], 256, 0.0, 1.0, 0.5, 1.0)
    try dev.WriteBuffer(&ubuf_quad, 0, qu_bytes[..])
    bg_quad := try dev.NewBindGroup(&bgl_quad, [BindEntry(binding: 0, bound: Bound.Buffer(b: &ubuf_quad, offset: 0, size: 16))][..], "bg-quad")
    defer bg_quad.Close(dev)

    var pd_quad := DefaultRenderPipeline
    q_attrs := [VertexAttribute(location: 0, format: VertexFormat.Float32x2, offset: 0)]
    q_vls := [VertexLayout(stride: 8, step: StepMode.Vertex, attributes: q_attrs[..])]
    pd_quad.vertex_layouts = q_vls[..]
    pd_quad.targets = tgts_rgba[..]
    pipe_quad := try dev.NewRenderPipeline(io, &pl_quad, &quad_vs, some(&quad_ps), pd_quad)
    defer pipe_quad.Close(dev)
    var e2 := try dev.Encoder("quad-pass")
    ca2 := [ColorAttachment(target: rt_v, resolve: none, load: Load.Clear(r: 0.0, g: 0.0, b: 0.0, a: 1.0), store: Store.Store)]
    var rp2 := e2.RenderPass(&PassDesc(colors: ca2[..], depth: none, label: "quad"))
    rp2.SetPipeline(&pipe_quad)
    rp2.SetBindGroup(0, &bg_quad, [u32(256)][..])
    rp2.SetVertexBuffer(0, &vbuf_quad, 0)
    rp2.Draw(6, 1, 0, 0)
    rp2.End()
    e2.Submit() else { Panic("quad submit failed") }
    px_quad := try dev.ReadTexture(a, io, TextureRegion(texture: rt, mip: 0, layer: 0, x: 0, y: 0, aspect: Aspect.All), Extent(width: 64, height: 64, layers: 1))
    Assert(px_quad[0] == 0 && px_quad[1] == 255 && px_quad[2] >= 127 && px_quad[2] <= 128 && px_quad[3] == 255)

    // 3. BlendMsaa (4x MSAA + BlendOver + push immediates + resolve -> 1x rt)
    bm_vs := try MakeShader(dev, a, BlendMsaaVsSpirvZ, Stage.Vertex, "VsMain", "bm-vs")
    defer bm_vs.Close(dev)
    bm_ps := try MakeShader(dev, a, BlendMsaaPsSpirvZ, Stage.Fragment, "PsMain", "bm-ps")
    defer bm_ps.Close(dev)
    msaa_tex := try NewSimpleTex(dev, TextureFormat.Rgba8Unorm, 64, 64, 1, 1, 4, false, false, false, true, "msaa")
    defer msaa_tex.Close(dev)
    msaa_v := try dev.NewView(&msaa_tex, DefaultViewDesc)
    defer msaa_v.Close(dev)
    pl_bm := try dev.NewPipelineLayout(bgl_empty[..], 16, "pl-bm")
    defer pl_bm.Close(dev)
    var pd_bm := DefaultRenderPipeline
    pd_bm.vertex_layouts = q_vls[..]
    var ct_blend := ColorOf(TextureFormat.Rgba8Unorm)
    ct_blend.blend = some(BlendOver)
    tgts_blend := [ct_blend]
    pd_bm.targets = tgts_blend[..]
    pd_bm.samples = 4
    pipe_bm := try dev.NewRenderPipeline(io, &pl_bm, &bm_vs, some(&bm_ps), pd_bm)
    defer pipe_bm.Close(dev)
    var e7 := try dev.Encoder("bm-pass")
    ca7 := [ColorAttachment(target: &msaa_v, resolve: some(rt_v), load: Load.Clear(r: 0.0, g: 0.0, b: 1.0, a: 1.0), store: Store.Store)]
    var rp7 := e7.RenderPass(&PassDesc(colors: ca7[..], depth: none, label: "bm"))
    rp7.SetPipeline(&pipe_bm)
    var col_pc: [16]u8 = [0; 16]
    WriteF32x4(mut col_pc[..], 0, 1.0, 0.0, 0.0, 0.5)
    rp7.SetImmediates(0, col_pc[..])
    rp7.SetVertexBuffer(0, &vbuf_quad, 0)
    rp7.Draw(6, 1, 0, 0)
    rp7.End()
    e7.Submit() else { Panic("bm submit failed") }
    px_bm := try dev.ReadTexture(a, io, TextureRegion(texture: rt, mip: 0, layer: 0, x: 0, y: 0, aspect: Aspect.All), Extent(width: 64, height: 64, layers: 1))
    Assert(px_bm[0] == 255 && px_bm[1] == 0 && px_bm[2] >= 127 && px_bm[2] <= 128 && px_bm[3] == 255)
    _ = no_dyn[..]
}

RunTexturedAndInstancedTest(a: &Arena, io: Io, dev: mut Device, rt: &Texture, rt_v: &TextureView): Error!void {
    no_dyn: [0]u32 = []
    tgts_rgba := [ColorOf(TextureFormat.Rgba8Unorm)]

    // 4. TexturedQuad (UBO + Texture2D + SamplerState + WriteTexture)
    tq_vs := try MakeShader(dev, a, TexturedQuadVsSpirvZ, Stage.Vertex, "VsMain", "tq-vs")
    defer tq_vs.Close(dev)
    tq_ps := try MakeShader(dev, a, TexturedQuadPsSpirvZ, Stage.Fragment, "PsMain", "tq-ps")
    defer tq_ps.Close(dev)
    src_tex := try NewSimpleTex(dev, TextureFormat.Rgba8Unorm, 2, 2, 1, 1, 1, false, true, true, false, "src-tex")
    defer src_tex.Close(dev)
    var texels: [16]u8 = [200, 100, 50, 255, 200, 100, 50, 255, 200, 100, 50, 255, 200, 100, 50, 255]
    try dev.WriteTexture(TextureRegion(texture: &src_tex, mip: 0, layer: 0, x: 0, y: 0, aspect: Aspect.All), texels[..], Extent(width: 2, height: 2, layers: 1))
    src_tv := try dev.NewView(&src_tex, DefaultViewDesc)
    defer src_tv.Close(dev)
    var sdn := DefaultSamplerDesc
    sdn.min = Filter.Nearest
    sdn.mag = Filter.Nearest
    smp_near := try dev.NewSampler(sdn)
    defer smp_near.Close(dev)
    var uu_dst := NoBufferUsage
    uu_dst.uniform = true
    uu_dst.copy_dst = true
    ubuf_tq := try dev.NewBuffer(256, uu_dst, "tq-ubo")
    defer ubuf_tq.Close(dev)
    var tqu: [16]u8 = [0; 16]
    WriteF32x4(mut tqu[..], 0, 0.5, 1.0, 1.0, 1.0)
    try dev.WriteBuffer(&ubuf_tq, 0, tqu[..])
    bgl_tq := try dev.NewBindGroupLayout([
        LayoutEntry(binding: 0, visibility: Visibility(vertex: false, fragment: true, compute: false), kind: BindingKind.Uniform(dynamic: false, min_size: 16)),
        LayoutEntry(binding: 1, visibility: Visibility(vertex: false, fragment: true, compute: false), kind: BindingKind.Texture(sample: SampleType.Float, dimension: ViewDimension.D2, multisampled: false)),
        LayoutEntry(binding: 2, visibility: Visibility(vertex: false, fragment: true, compute: false), kind: BindingKind.Sampler(comparison: false)),
    ][..], "bgl-tq")
    defer bgl_tq.Close(dev)
    bg_tq := try dev.NewBindGroup(&bgl_tq, [
        BindEntry(binding: 0, bound: Bound.Buffer(b: &ubuf_tq, offset: 0, size: 16)),
        BindEntry(binding: 1, bound: Bound.View(v: &src_tv)),
        BindEntry(binding: 2, bound: Bound.Sampler(s: &smp_near)),
    ][..], "bg-tq")
    defer bg_tq.Close(dev)
    pl_tq := try dev.NewPipelineLayout([&bgl_tq][..], 0, "pl-tq")
    defer pl_tq.Close(dev)
    var uv_dst := NoBufferUsage
    uv_dst.vertex = true
    uv_dst.copy_dst = true
    vb_tq := try dev.NewBuffer(96, uv_dst, "tq-vb")
    defer vb_tq.Close(dev)
    var tqv: [96]u8 = [0; 96]
    for vi in 0..6 {
        var x: f32 = if vi == 1 || vi == 3 || vi == 4 { 1.0 } else { -1.0 }
        var y: f32 = if vi == 0 || vi == 1 || vi == 3 { 1.0 } else { -1.0 }
        WriteF32x4(mut tqv[..], vi * 16, x, y, 0.5, 0.5)
    }
    try dev.WriteBuffer(&vb_tq, 0, tqv[..])
    var pd_tq := DefaultRenderPipeline
    tq_attrs := [VertexAttribute(location: 0, format: VertexFormat.Float32x2, offset: 0), VertexAttribute(location: 1, format: VertexFormat.Float32x2, offset: 8)]
    tq_vls := [VertexLayout(stride: 16, step: StepMode.Vertex, attributes: tq_attrs[..])]
    pd_tq.vertex_layouts = tq_vls[..]
    pd_tq.targets = tgts_rgba[..]
    pipe_tq := try dev.NewRenderPipeline(io, &pl_tq, &tq_vs, some(&tq_ps), pd_tq)
    defer pipe_tq.Close(dev)
    var e3 := try dev.Encoder("tq-pass")
    ca3 := [ColorAttachment(target: rt_v, resolve: none, load: Load.Clear(r: 0.0, g: 0.0, b: 0.0, a: 1.0), store: Store.Store)]
    var rp3 := e3.RenderPass(&PassDesc(colors: ca3[..], depth: none, label: "tq"))
    rp3.SetPipeline(&pipe_tq)
    rp3.SetBindGroup(0, &bg_tq, no_dyn[..])
    rp3.SetVertexBuffer(0, &vb_tq, 0)
    rp3.Draw(6, 1, 0, 0)
    rp3.End()
    e3.Submit() else { Panic("tq submit failed") }
    px_tq := try dev.ReadTexture(a, io, TextureRegion(texture: rt, mip: 0, layer: 0, x: 0, y: 0, aspect: Aspect.All), Extent(width: 64, height: 64, layers: 1))
    Assert(px_tq[0] >= 98 && px_tq[0] <= 102 && px_tq[1] >= 98 && px_tq[1] <= 102 && px_tq[2] >= 48 && px_tq[2] <= 52 && px_tq[3] == 255)

    // 5. Instanced shader (storage buffer in VS)
    ins_vs := try MakeShader(dev, a, InstancedVsSpirvZ, Stage.Vertex, "VsMain", "ins-vs")
    defer ins_vs.Close(dev)
    ins_ps := try MakeShader(dev, a, InstancedPsSpirvZ, Stage.Fragment, "PsMain", "ins-ps")
    defer ins_ps.Close(dev)
    bgl_ins := try dev.NewBindGroupLayout([LayoutEntry(binding: 0, visibility: Visibility(vertex: true, fragment: false, compute: false), kind: BindingKind.Storage(read_only: true, min_size: 16, stride: 16))][..], "bgl-ins")
    defer bgl_ins.Close(dev)
    var us_dst := NoBufferUsage
    us_dst.storage = true
    us_dst.copy_dst = true
    sbuf_ins := try dev.NewBuffer(64, us_dst, "sbuf-ins")
    defer sbuf_ins.Close(dev)
    var ins_b: [64]u8 = [0; 64]
    WriteF32x4(mut ins_b[..], 0, 0.0, 0.0, 1.0, 1.0)
    try dev.WriteBuffer(&sbuf_ins, 0, ins_b[..])
    bg_ins := try dev.NewBindGroup(&bgl_ins, [BindEntry(binding: 0, bound: Bound.Buffer(b: &sbuf_ins, offset: 0, size: 64))][..], "bg-ins")
    defer bg_ins.Close(dev)
    pl_ins := try dev.NewPipelineLayout([&bgl_ins][..], 0, "pl-ins")
    defer pl_ins.Close(dev)
    var pd_ins := DefaultRenderPipeline
    pd_ins.topology = Topology.TriangleStrip
    pd_ins.targets = tgts_rgba[..]
    pipe_ins := try dev.NewRenderPipeline(io, &pl_ins, &ins_vs, some(&ins_ps), pd_ins)
    defer pipe_ins.Close(dev)
    var e6 := try dev.Encoder("ins-pass")
    ca6 := [ColorAttachment(target: rt_v, resolve: none, load: Load.Clear(r: 0.0, g: 0.0, b: 0.0, a: 1.0), store: Store.Store)]
    var rp6 := e6.RenderPass(&PassDesc(colors: ca6[..], depth: none, label: "ins"))
    rp6.SetPipeline(&pipe_ins)
    rp6.SetBindGroup(0, &bg_ins, no_dyn[..])
    rp6.Draw(4, 1, 0, 0)
    rp6.End()
    e6.Submit() else { Panic("ins submit failed") }
    px_ins := try dev.ReadTexture(a, io, TextureRegion(texture: rt, mip: 0, layer: 0, x: 0, y: 0, aspect: Aspect.All), Extent(width: 64, height: 64, layers: 1))
    c_off := (32 * 64 + 32) * 4
    Assert(px_ins[c_off + 2] == 255 && px_ins[c_off + 3] == 255)
}

RunDepthTest(a: &Arena, io: Io, dev: mut Device, rt: &Texture, rt_v: &TextureView): Error!void {
    no_dyn: [0]u32 = []
    tgts_rgba := [ColorOf(TextureFormat.Rgba8Unorm)]

    dc_vs := try MakeShader(dev, a, DepthCubeVsSpirvZ, Stage.Vertex, "VsMain", "dc-vs")
    defer dc_vs.Close(dev)
    dc_ps := try MakeShader(dev, a, DepthCubePsSpirvZ, Stage.Fragment, "PsMain", "dc-ps")
    defer dc_ps.Close(dev)
    ds_vs := try MakeShader(dev, a, DepthSampleVsSpirvZ, Stage.Vertex, "VsMain", "ds-vs")
    defer ds_vs.Close(dev)
    ds_ps := try MakeShader(dev, a, DepthSamplePsSpirvZ, Stage.Fragment, "PsMain", "ds-ps")
    defer ds_ps.Close(dev)

    d32_tex := try NewSimpleTex(dev, TextureFormat.Depth32Float, 64, 64, 1, 1, 1, false, false, true, true, "d32")
    defer d32_tex.Close(dev)
    var d32_vd := DefaultViewDesc
    d32_vd.aspect = Aspect.Depth
    d32_vd.mips = some(1)
    d32_vd.layers = some(1)
    d32_v := try dev.NewView(&d32_tex, d32_vd)
    defer d32_v.Close(dev)

    bgl_dc := try dev.NewBindGroupLayout([LayoutEntry(binding: 0, visibility: Visibility(vertex: true, fragment: false, compute: false), kind: BindingKind.Uniform(dynamic: false, min_size: 64))][..], "bgl-dc")
    defer bgl_dc.Close(dev)
    pl_dc := try dev.NewPipelineLayout([&bgl_dc][..], 16, "pl-dc")
    defer pl_dc.Close(dev)
    var uu_dst := NoBufferUsage
    uu_dst.uniform = true
    uu_dst.copy_dst = true
    ubo_dc := try dev.NewBuffer(64, uu_dst, "ubo-dc")
    defer ubo_dc.Close(dev)
    var id_mat: [64]u8 = [0; 64]
    WriteF32x4(mut id_mat[..], 0, 1.0, 0.0, 0.0, 0.0)
    WriteF32x4(mut id_mat[..], 16, 0.0, 1.0, 0.0, 0.0)
    WriteF32x4(mut id_mat[..], 32, 0.0, 0.0, 1.0, 0.0)
    WriteF32x4(mut id_mat[..], 48, 0.0, 0.0, 0.0, 1.0)
    try dev.WriteBuffer(&ubo_dc, 0, id_mat[..])
    bg_dc := try dev.NewBindGroup(&bgl_dc, [BindEntry(binding: 0, bound: Bound.Buffer(b: &ubo_dc, offset: 0, size: 64))][..], "bg-dc")
    defer bg_dc.Close(dev)

    var uv_dst := NoBufferUsage
    uv_dst.vertex = true
    uv_dst.copy_dst = true
    vb_dc := try dev.NewBuffer(12 * 24, uv_dst, "vb-dc")
    defer vb_dc.Close(dev)
    var dcv: [288]u8 = [0; 288]
    for vi in 0..6 {
        var x: f32 = if vi == 1 || vi == 3 || vi == 4 { 1.0 } else { -1.0 }
        var y: f32 = if vi == 0 || vi == 1 || vi == 3 { 1.0 } else { -1.0 }
        WriteF32x4(mut dcv[..], vi * 24, x, y, 0.25, 0.0)
        TestWriteU32(mut dcv[..], vi * 24 + 16, F32ToU32(1.0))
        TestWriteU32(mut dcv[..], vi * 24 + 20, F32ToU32(0.0))
        o2 := (6 + vi) * 24
        WriteF32x4(mut dcv[..], o2, x, y, 0.75, 1.0)
        TestWriteU32(mut dcv[..], o2 + 16, F32ToU32(0.0))
        TestWriteU32(mut dcv[..], o2 + 20, F32ToU32(0.0))
    }
    try dev.WriteBuffer(&vb_dc, 0, dcv[..])

    var pd_dc := DefaultRenderPipeline
    dc_attrs := [VertexAttribute(location: 0, format: VertexFormat.Float32x3, offset: 0), VertexAttribute(location: 1, format: VertexFormat.Float32x3, offset: 12)]
    dc_vls := [VertexLayout(stride: 24, step: StepMode.Vertex, attributes: dc_attrs[..])]
    pd_dc.vertex_layouts = dc_vls[..]
    pd_dc.targets = tgts_rgba[..]
    pd_dc.depth = some(DepthOf(TextureFormat.Depth32Float, Compare.Less))
    pipe_dc := try dev.NewRenderPipeline(io, &pl_dc, &dc_vs, some(&dc_ps), pd_dc)
    defer pipe_dc.Close(dev)
    var e4 := try dev.Encoder("dc-pass")
    ca4 := [ColorAttachment(target: rt_v, resolve: none, load: Load.Clear(r: 0.0, g: 0.0, b: 0.0, a: 1.0), store: Store.Store)]
    da4 := DepthAttachment(target: &d32_v, depth_load: LoadDepth.Clear(depth: 1.0), depth_store: Store.Store, stencil_load: LoadStencil.DontCare, stencil_store: Store.Discard, read_only: false)
    var rp4 := e4.RenderPass(&PassDesc(colors: ca4[..], depth: some(da4), label: "dc"))
    rp4.SetPipeline(&pipe_dc)
    rp4.SetBindGroup(0, &bg_dc, no_dyn[..])
    var tint_pc: [16]u8 = [0; 16]
    WriteF32x4(mut tint_pc[..], 0, 1.0, 1.0, 1.0, 1.0)
    rp4.SetImmediates(0, tint_pc[..])
    rp4.SetVertexBuffer(0, &vb_dc, 0)
    rp4.Draw(6, 1, 0, 0)
    rp4.Draw(6, 1, 6, 0)
    rp4.End()
    e4.Submit() else { Panic("dc submit failed") }
    px_dc := try dev.ReadTexture(a, io, TextureRegion(texture: rt, mip: 0, layer: 0, x: 0, y: 0, aspect: Aspect.All), Extent(width: 64, height: 64, layers: 1))
    Assert(px_dc[0] == 0 && px_dc[1] == 255 && px_dc[2] == 0 && px_dc[3] == 255)

    // DepthSample pass: read_only depth attach + sampled view in FS.
    var sds := DefaultSamplerDesc
    sds.min = Filter.Nearest
    sds.mag = Filter.Nearest
    smp_ds := try dev.NewSampler(sds)
    defer smp_ds.Close(dev)
    bgl_ds := try dev.NewBindGroupLayout([
        LayoutEntry(binding: 0, visibility: Visibility(vertex: false, fragment: true, compute: false), kind: BindingKind.Texture(sample: SampleType.Depth, dimension: ViewDimension.D2, multisampled: false)),
        LayoutEntry(binding: 1, visibility: Visibility(vertex: false, fragment: true, compute: false), kind: BindingKind.Sampler(comparison: false)),
    ][..], "bgl-ds")
    defer bgl_ds.Close(dev)
    bg_ds := try dev.NewBindGroup(&bgl_ds, [
        BindEntry(binding: 0, bound: Bound.View(v: &d32_v)),
        BindEntry(binding: 1, bound: Bound.Sampler(s: &smp_ds)),
    ][..], "bg-ds")
    defer bg_ds.Close(dev)
    pl_ds := try dev.NewPipelineLayout([&bgl_ds][..], 0, "pl-ds")
    defer pl_ds.Close(dev)
    var pd_ds := DefaultRenderPipeline
    pd_ds.targets = tgts_rgba[..]
    var ds_st := DepthOf(TextureFormat.Depth32Float, Compare.Always)
    ds_st.write = false
    pd_ds.depth = some(ds_st)
    pipe_ds := try dev.NewRenderPipeline(io, &pl_ds, &ds_vs, some(&ds_ps), pd_ds)
    defer pipe_ds.Close(dev)
    var e5 := try dev.Encoder("ds-pass")
    ca5 := [ColorAttachment(target: rt_v, resolve: none, load: Load.Clear(r: 0.0, g: 0.0, b: 0.0, a: 1.0), store: Store.Store)]
    da5 := DepthAttachment(target: &d32_v, depth_load: LoadDepth.Keep, depth_store: Store.Store, stencil_load: LoadStencil.DontCare, stencil_store: Store.Discard, read_only: true)
    var rp5 := e5.RenderPass(&PassDesc(colors: ca5[..], depth: some(da5), label: "ds"))
    rp5.SetPipeline(&pipe_ds)
    rp5.SetBindGroup(0, &bg_ds, no_dyn[..])
    rp5.Draw(3, 1, 0, 0)
    rp5.End()
    e5.Submit() else { Panic("ds submit failed") }
    px_ds := try dev.ReadTexture(a, io, TextureRegion(texture: rt, mip: 0, layer: 0, x: 0, y: 0, aspect: Aspect.All), Extent(width: 64, height: 64, layers: 1))
    Assert(px_ds[0] >= 60 && px_ds[0] <= 68 && px_ds[3] == 255)
}

test "gpu vulkan S1 graphics pipelines Tri, Quad, TexturedQuad, DepthCube, Instanced, BlendMsaa, DepthSample" (sys: &System) {
    if r := try VkTestSkipReason(sys) {
        _ = sys.Print("skip: {s}\n", (s: r)) else {}
        return
    }
    var a := try Arena.New(sys.heap)
    defer a.Free()

    var opts_cs := DefaultDeviceOptions
    opts_cs.backend = Backend.Vulkan
    opts_cs.debug = DebugMode.Validate
    var dev := try Open(sys.heap, sys.io, opts_cs)
    defer {
        st := dev.Close(sys.io)
        Assert(st.native == 0)
    }

    rt := try NewSimpleTex(mut dev, TextureFormat.Rgba8Unorm, 64, 64, 1, 1, 1, true, true, true, true, "rt")
    defer rt.Close(mut dev)
    rt_v := try dev.NewView(&rt, DefaultViewDesc)
    defer rt_v.Close(mut dev)

    try RunTriAndQuadTest(&a, sys.io, mut dev, &rt, &rt_v)
    try RunTexturedAndInstancedTest(&a, sys.io, mut dev, &rt, &rt_v)
    try RunDepthTest(&a, sys.io, mut dev, &rt, &rt_v)
}

test "gpu vulkan S1 compute pipelines DoubleCs, PostInvertCs, and PrefixSumCs" (sys: &System) {
    if r := try VkTestSkipReason(sys) {
        _ = sys.Print("skip: {s}\n", (s: r)) else {}
        return
    }
    var a := try Arena.New(sys.heap)
    defer a.Free()

    var opts_cs := DefaultDeviceOptions
    opts_cs.backend = Backend.Vulkan
    opts_cs.debug = DebugMode.Validate
    var dev := try Open(sys.heap, sys.io, opts_cs)
    defer {
        st := dev.Close(sys.io)
        Assert(st.native == 0)
    }

    no_dyn: [0]u32 = []
    cs_dbl := try MakeShader(mut dev, &a, DoubleCsSpirvZ, Stage.Compute, "CsMain", "dbl-cs")
    defer cs_dbl.Close(mut dev)
    bgl_rw := try dev.NewBindGroupLayout([LayoutEntry(binding: 0, visibility: Visibility(vertex: false, fragment: false, compute: true), kind: BindingKind.Storage(read_only: false, min_size: 4, stride: 4))][..], "bgl-rw")
    defer bgl_rw.Close(mut dev)
    pl_rw := try dev.NewPipelineLayout([&bgl_rw][..], 0, "pl-rw")
    defer pl_rw.Close(mut dev)
    pipe_dbl := try dev.NewComputePipeline(sys.io, &pl_rw, &cs_dbl, "dbl")
    defer pipe_dbl.Close(mut dev)

    var us_cs := NoBufferUsage
    us_cs.storage = true
    us_cs.copy_src = true
    us_cs.copy_dst = true
    var umr_cs := NoBufferUsage
    umr_cs.map_read = true
    umr_cs.copy_dst = true
    sbuf := try dev.NewBuffer(256, us_cs, "cs-sbuf")
    defer sbuf.Close(mut dev)
    rbuf := try dev.NewBuffer(256, umr_cs, "cs-rbuf")
    defer rbuf.Close(mut dev)
    bg_rw := try dev.NewBindGroup(&bgl_rw, [BindEntry(binding: 0, bound: Bound.Buffer(b: &sbuf, offset: 0, size: 256))][..], "bg-rw")
    defer bg_rw.Close(mut dev)

    var dbl_in: [16]u8 = [0; 16]
    WriteF32x4(mut dbl_in[..], 0, 1.5, 2.5, 3.0, 4.0)
    var ce1 := try dev.Encoder("dbl")
    ce1.WriteBuffer(&sbuf, 0, dbl_in[..]) else { Panic("enc WriteBuffer failed") }
    var cp1 := ce1.ComputePass("dbl")
    cp1.SetPipeline(&pipe_dbl)
    cp1.SetBindGroup(0, &bg_rw, no_dyn[..])
    cp1.Dispatch(1, 1, 1)
    cp1.End()
    ce1.CopyBuffer(&sbuf, 0, &rbuf, 0, 16)
    ce1.Submit() else { Panic("dbl submit failed") }
    var mr1 := try dev.MapRead(sys.io, &rbuf, 0, 16)
    mb1 := mr1.Bytes()
    got0 := TestReadU32(mb1[0..4])
    got1 := TestReadU32(mb1[4..8])
    got2 := TestReadU32(mb1[8..12])
    got3 := TestReadU32(mb1[12..16])
    mr1.Unmap()
    Assert(got0 == F32ToU32(3.0))
    Assert(got1 == F32ToU32(5.0))
    Assert(got2 == F32ToU32(6.0))
    Assert(got3 == F32ToU32(8.0))

    // 2. PrefixSumCs
    cs_pre := try MakeShader(mut dev, &a, PrefixSumCsSpirvZ, Stage.Compute, "CsMain", "pre-cs")
    defer cs_pre.Close(mut dev)
    pipe_pre := try dev.NewComputePipeline(sys.io, &pl_rw, &cs_pre, "pre")
    defer pipe_pre.Close(mut dev)
    var pre_in: [256]u8 = [0; 256]
    for i in 0..64 {
        TestWriteU32(mut pre_in[..], i * 4, Cast[u32](i + 1))
    }
    try dev.WriteBuffer(&sbuf, 0, pre_in[..])
    var ce2 := try dev.Encoder("pre")
    var cp2 := ce2.ComputePass("pre")
    cp2.SetPipeline(&pipe_pre)
    cp2.SetBindGroup(0, &bg_rw, no_dyn[..])
    cp2.Dispatch(1, 1, 1)
    cp2.End()
    ce2.CopyBuffer(&sbuf, 0, &rbuf, 0, 256)
    ce2.Submit() else { Panic("pre submit failed") }
    var mr2 := try dev.MapRead(sys.io, &rbuf, 0, 256)
    mb2 := mr2.Bytes()
    Assert(TestReadU32(mb2[0..4]) == 1)
    Assert(TestReadU32(mb2[4..8]) == 3)
    Assert(TestReadU32(mb2[8..12]) == 6)
    Assert(TestReadU32(mb2[12..16]) == 10)
    Assert(TestReadU32(mb2[252..256]) == 2080)
    mr2.Unmap()

    // 3. PostInvertCs
    cs_inv := try MakeShader(mut dev, &a, PostInvertCsSpirvZ, Stage.Compute, "CsMain", "inv-cs")
    defer cs_inv.Close(mut dev)
    bgl_inv := try dev.NewBindGroupLayout([
        LayoutEntry(binding: 0, visibility: Visibility(vertex: false, fragment: false, compute: true), kind: BindingKind.Storage(read_only: true, min_size: 4, stride: 4)),
        LayoutEntry(binding: 1, visibility: Visibility(vertex: false, fragment: false, compute: true), kind: BindingKind.Storage(read_only: false, min_size: 4, stride: 4)),
    ][..], "bgl-inv")
    defer bgl_inv.Close(mut dev)
    pl_inv := try dev.NewPipelineLayout([&bgl_inv][..], 0, "pl-inv")
    defer pl_inv.Close(mut dev)
    pipe_inv := try dev.NewComputePipeline(sys.io, &pl_inv, &cs_inv, "inv")
    defer pipe_inv.Close(mut dev)
    var us_src := NoBufferUsage
    us_src.storage = true
    us_src.copy_dst = true
    sbuf_src := try dev.NewBuffer(256, us_src, "inv-src")
    defer sbuf_src.Close(mut dev)
    var inv_in: [16]u8 = [0; 16]
    TestWriteU32(mut inv_in[..], 0, 0x00010203)
    TestWriteU32(mut inv_in[..], 4, 0x00AABBCC)
    try dev.WriteBuffer(&sbuf_src, 0, inv_in[..])
    bg_inv := try dev.NewBindGroup(&bgl_inv, [
        BindEntry(binding: 0, bound: Bound.Buffer(b: &sbuf_src, offset: 0, size: 256)),
        BindEntry(binding: 1, bound: Bound.Buffer(b: &sbuf, offset: 0, size: 256)),
    ][..], "bg-inv")
    defer bg_inv.Close(mut dev)
    var ce3 := try dev.Encoder("inv")
    var cp3 := ce3.ComputePass("inv")
    cp3.SetPipeline(&pipe_inv)
    cp3.SetBindGroup(0, &bg_inv, no_dyn[..])
    cp3.Dispatch(1, 1, 1)
    cp3.End()
    ce3.CopyBuffer(&sbuf, 0, &rbuf, 0, 16)
    ce3.Submit() else { Panic("inv submit failed") }
    var mr3 := try dev.MapRead(sys.io, &rbuf, 0, 16)
    mb3 := mr3.Bytes()
    Assert(TestReadU32(mb3[0..4]) == (0x00010203 ^ 0x00FFFFFF))
    Assert(TestReadU32(mb3[4..8]) == (0x00AABBCC ^ 0x00FFFFFF))
    mr3.Unmap()
}
