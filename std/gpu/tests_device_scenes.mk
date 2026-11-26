// Why: golden verification, cross-backend comparison, and tools/gpugolden
// share the exact same deterministic 64x64 render/compute scenes and pinning
// scenes so WARP, D3D12 hardware, and Vulkan produce comparable images.
//
// What: the 9 golden scene renderers (triangle, indexed_textured_quad,
// depth_cubes, instancing, msaa_resolve, blending, mip_chain, depth_sample,
// compute_post), the 2 pinning scenes (CW/CCW back-face culling and nonzero
// base_vertex/first_instance IDs), and adapter-selection helpers.
//
// How: renders each 64x64 Rgba8Unorm scene using tests_shaders.mk SPIR-V +
// DXIL blobs, reads back tight RGBA8 pixels into std.image.Image, and uses
// SampleLevel-only lookups with anisotropy 1.

import std.fs
import std.image
import std.rt

const GoldenSceneCount: int = 9
const GoldenWidth: int = 64
const GoldenHeight: int = 64

GoldenSceneName(idx: int): str {
    return match idx {
        0 { "triangle" }
        1 { "indexed_textured_quad" }
        2 { "depth_cubes" }
        3 { "instancing" }
        4 { "msaa_resolve" }
        5 { "blending" }
        6 { "mip_chain" }
        7 { "depth_sample" }
        _ { "compute_post" }
    }
}

RequestedGpuAdapter(): str {
    match rt.Env("MK_GPU_ADAPTER") {
        some(v) { return v }
        none { return "" }
    }
}

HasNonSoftwareDx(a: &Arena, io: Io): bool {
    if Target.os != Os.Windows {
        return false
    }
    ads := Adapters(a, io, Backend.D3d12) else { return false }
    for i in 0..ads.Len() {
        if ads[i].kind != AdapterKind.Software {
            return true
        }
    }
    return false
}

HasVulkanAdapter(a: &Arena, io: Io): bool {
    ads := Adapters(a, io, Backend.Vulkan) else { return false }
    return ads.Len() > 0
}

OpenDeviceForKind(heap: &Heap, io: Io, kind: str, dbg: DebugMode): Error!Device from heap {
    var o := DefaultDeviceOptions
    o.debug = dbg
    if kind.Eq(&"warp") {
        if Target.os != Os.Windows {
            fail Unsupported
        }
        o.backend = Backend.D3d12
        o.software = true
        return try Open(heap, io, o)
    }
    if kind.Eq(&"d3d12") {
        if Target.os != Os.Windows {
            fail Unsupported
        }
        o.backend = Backend.D3d12
        o.software = false
        return try Open(heap, io, o)
    }
    if kind.Eq(&"vulkan") {
        o.backend = Backend.Vulkan
        o.software = false
        o.debug = if dbg == DebugMode.Strict { DebugMode.Validate } else { dbg }
        return try Open(heap, io, o)
    }
    fail Unsupported
}

ActiveAdapterKind(a: &Arena, io: Io): ?str {
    req := RequestedGpuAdapter()
    if req.Eq(&"warp") {
        return if Target.os == Os.Windows { some("warp") } else { none }
    }
    if req.Eq(&"d3d12") {
        return if HasNonSoftwareDx(a, io) { some("d3d12") } else { none }
    }
    if req.Eq(&"vulkan") {
        return if HasVulkanAdapter(a, io) { some("vulkan") } else { none }
    }
    if Target.os == Os.Windows {
        return some("warp")
    }
    if HasVulkanAdapter(a, io) {
        return some("vulkan")
    }
    return none
}

DevWriteU16(dst: mut []u8, idx: int, v: u16) {
    o := idx * 2
    dst[o] = Trunc[u8](v & 0xFF)
    dst[o + 1] = Trunc[u8]((v >> 8) & 0xFF)
}

DevImageFromPixels(a: &Arena, w: int, h: int, px: []u8): OutOfMemory!image.Image from a {
    var copy := try a.Slice[u8](px.Len())
    (mut copy[..]).CopyFrom(px)
    return image.Image(width: w, height: h, pixels: image.Pixels.Rgba8, data: copy[..])
}

DevNewRtTex(dev: mut Device, w: int, h: int, mips: int, samples: int, lbl: str): Error!Texture {
    var td := DefaultTextureDesc
    td.width = w
    td.height = h
    td.mips = mips
    td.samples = samples
    td.usage = TextureUsage(copy_src: samples == 1, copy_dst: true, sampled: samples == 1, render_target: true, sealed: 0)
    td.label = lbl
    return try dev.NewTexture(td)
}

DevReadImage(a: &Arena, io: Io, dev: mut Device, tex: &Texture, w: int, h: int): Error!image.Image from a {
    px := try dev.ReadTexture(a, io, TextureRegion(texture: tex, mip: 0, layer: 0, x: 0, y: 0, aspect: Aspect.All), Extent(width: w, height: h, layers: 1))
    return try DevImageFromPixels(a, w, h, px)
}

DevWriteQuadVertsPosUv(dst: mut []u8, x0: f32, y0: f32, x1: f32, y1: f32) {
    // 4 vertices: (x0,y0, 0,1), (x1,y0, 1,1), (x0,y1, 0,0), (x1,y1, 1,0)
    vals: [16]f32 = [x0, y0, 0.0, 1.0, x1, y0, 1.0, 1.0, x0, y1, 0.0, 0.0, x1, y1, 1.0, 0.0]
    for i in 0..16 {
        WriteLeF32(dst, i, vals[i])
    }
}

DevWriteFullQuad6(dst: mut []u8) {
    vals: [12]f32 = [-1.0, -1.0, 1.0, -1.0, -1.0, 1.0, 1.0, -1.0, 1.0, 1.0, -1.0, 1.0]
    for i in 0..12 {
        WriteLeF32(dst, i, vals[i])
    }
}

RenderSceneTriangle(a: &Arena, io: Io, dev: mut Device, rt: &Texture, rt_v: &TextureView): Error!image.Image from a {
    vs := try LoadShaderPair(a, dev, TriVsSpirvZ, TriVsDxilZ, Stage.Vertex, "VsMain", "g_tri_vs")
    defer vs.Close(dev)
    ps := try LoadShaderPair(a, dev, TriPsSpirvZ, TriPsDxilZ, Stage.Fragment, "PsMain", "g_tri_ps")
    defer ps.Close(dev)
    var empty_bgls: [0]&BindGroupLayout = []
    pl := try dev.NewPipelineLayout(empty_bgls[..], 0, "g_tri_pl")
    defer pl.Close(dev)
    tgts := [ColorOf(TextureFormat.Rgba8Unorm)]
    var pd := DefaultRenderPipeline
    pd.targets = tgts[..]
    pipe := try dev.NewRenderPipeline(io, &pl, &vs, some(&ps), pd)
    defer pipe.Close(dev)

    var enc := try dev.Encoder("g_tri_enc")
    onerr enc.Abandon()
    cols := [ColorAttachment(target: rt_v, resolve: none, load: Load.Clear(r: 0.05, g: 0.05, b: 0.1, a: 1.0), store: Store.Store)]
    var rp := enc.RenderPass(&PassDesc(colors: cols[..], depth: none, label: "g_tri"))
    rp.SetPipeline(&pipe)
    rp.Draw(3, 1, 0, 0)
    rp.End()
    try enc.Submit()
    return try DevReadImage(a, io, dev, rt, GoldenWidth, GoldenHeight)
}

RenderSceneIndexedTexturedQuad(a: &Arena, io: Io, dev: mut Device, rt: &Texture, rt_v: &TextureView): Error!image.Image from a {
    vs := try LoadShaderPair(a, dev, TexturedQuadVsSpirvZ, TexturedQuadVsDxilZ, Stage.Vertex, "VsMain", "g_itq_vs")
    defer vs.Close(dev)
    ps := try LoadShaderPair(a, dev, TexturedQuadPsSpirvZ, TexturedQuadPsDxilZ, Stage.Fragment, "PsMain", "g_itq_ps")
    defer ps.Close(dev)
    bgl := try dev.NewBindGroupLayout([
        LayoutEntry(binding: 0, visibility: Visibility(vertex: false, fragment: true, compute: false), kind: BindingKind.Uniform(dynamic: false, min_size: 16)),
        LayoutEntry(binding: 1, visibility: Visibility(vertex: false, fragment: true, compute: false), kind: BindingKind.Texture(sample: SampleType.Float, dimension: ViewDimension.D2, multisampled: false)),
        LayoutEntry(binding: 2, visibility: Visibility(vertex: false, fragment: true, compute: false), kind: BindingKind.Sampler(comparison: false)),
    ][..], "g_itq_bgl")
    defer bgl.Close(dev)
    pl := try dev.NewPipelineLayout([&bgl][..], 0, "g_itq_pl")
    defer pl.Close(dev)
    attrs := [VertexAttribute(location: 0, format: VertexFormat.Float32x2, offset: 0), VertexAttribute(location: 1, format: VertexFormat.Float32x2, offset: 8)]
    vls := [VertexLayout(stride: 16, step: StepMode.Vertex, attributes: attrs[..])]
    tgts := [ColorOf(TextureFormat.Rgba8Unorm)]
    var pd := DefaultRenderPipeline
    pd.vertex_layouts = vls[..]
    pd.targets = tgts[..]
    pipe := try dev.NewRenderPipeline(io, &pl, &vs, some(&ps), pd)
    defer pipe.Close(dev)

    var src_td := DefaultTextureDesc
    src_td.width = 8
    src_td.height = 8
    src_tex := try dev.NewTexture(src_td)
    defer src_tex.Close(dev)
    var src_px := try a.Slice[u8](8 * 8 * 4)
    for y in 0..8 {
        for x in 0..8 {
            o := (y * 8 + x) * 4
            even := ((x + y) & 1) == 0
            src_px[o] = if even { 240 } else { 40 }
            src_px[o + 1] = Trunc[u8](x * 32)
            src_px[o + 2] = Trunc[u8](y * 32)
            src_px[o + 3] = 255
        }
    }
    try dev.WriteTexture(TextureRegion(texture: &src_tex, mip: 0, layer: 0, x: 0, y: 0, aspect: Aspect.All), src_px[..], Extent(width: 8, height: 8, layers: 1))
    src_v := try dev.NewView(&src_tex, DefaultViewDesc)
    defer src_v.Close(dev)

    var sd := DefaultSamplerDesc
    sd.min = Filter.Nearest
    sd.mag = Filter.Nearest
    sd.mip = Filter.Nearest
    smp := try dev.NewSampler(sd)
    defer smp.Close(dev)

    var vu := NoBufferUsage
    vu.vertex = true
    vu.copy_dst = true
    vb := try dev.NewBuffer(64, vu, "g_itq_vb")
    defer vb.Close(dev)
    var vbytes := try a.Slice[u8](64)
    DevWriteQuadVertsPosUv(mut vbytes[..], -0.75, -0.75, 0.75, 0.75)
    try dev.WriteBuffer(&vb, 0, vbytes[..])

    var iu := NoBufferUsage
    iu.index = true
    iu.copy_dst = true
    ib := try dev.NewBuffer(16, iu, "g_itq_ib")
    defer ib.Close(dev)
    var ibytes := try a.Slice[u8](12)
    idxs: [6]u16 = [0, 1, 2, 2, 1, 3]
    for i in 0..6 {
        DevWriteU16(mut ibytes[..], i, idxs[i])
    }
    try dev.WriteBuffer(&ib, 0, ibytes[..])

    var uu := NoBufferUsage
    uu.uniform = true
    uu.copy_dst = true
    ub := try dev.NewBuffer(256, uu, "g_itq_ub")
    defer ub.Close(dev)
    var ubytes := try a.Slice[u8](16)
    WriteLeF32(mut ubytes[..], 0, 1.0)
    WriteLeF32(mut ubytes[..], 1, 0.9)
    WriteLeF32(mut ubytes[..], 2, 0.8)
    WriteLeF32(mut ubytes[..], 3, 1.0)
    try dev.WriteBuffer(&ub, 0, ubytes[..])

    bg := try dev.NewBindGroup(&bgl, [
        BindEntry(binding: 0, bound: Bound.Buffer(b: &ub, offset: 0, size: 16)),
        BindEntry(binding: 1, bound: Bound.View(v: &src_v)),
        BindEntry(binding: 2, bound: Bound.Sampler(s: &smp)),
    ][..], "g_itq_bg")
    defer bg.Close(dev)

    var no_dyn: [0]u32 = []
    var enc := try dev.Encoder("g_itq_enc")
    onerr enc.Abandon()
    cols := [ColorAttachment(target: rt_v, resolve: none, load: Load.Clear(r: 0.1, g: 0.1, b: 0.2, a: 1.0), store: Store.Store)]
    var rp := enc.RenderPass(&PassDesc(colors: cols[..], depth: none, label: "g_itq"))
    rp.SetPipeline(&pipe)
    rp.SetBindGroup(0, &bg, no_dyn[..])
    rp.SetVertexBuffer(0, &vb, 0)
    rp.SetIndexBuffer(&ib, IndexFormat.Uint16, 0)
    rp.DrawIndexed(6, 1, 0, 0, 0)
    rp.End()
    try enc.Submit()
    return try DevReadImage(a, io, dev, rt, GoldenWidth, GoldenHeight)
}

RenderSceneDepthCubes(a: &Arena, io: Io, dev: mut Device, rt: &Texture, rt_v: &TextureView): Error!image.Image from a {
    var dd := DefaultTextureDesc
    dd.format = TextureFormat.Depth32Float
    dd.width = GoldenWidth
    dd.height = GoldenHeight
    dd.usage = TextureUsage(copy_src: false, copy_dst: false, sampled: true, render_target: true, sealed: 0)
    dtex := try dev.NewTexture(dd)
    defer dtex.Close(dev)
    var dv_d := DefaultViewDesc
    dv_d.aspect = Aspect.Depth
    dv := try dev.NewView(&dtex, dv_d)
    defer dv.Close(dev)

    vs := try LoadShaderPair(a, dev, DepthCubeVsSpirvZ, DepthCubeVsDxilZ, Stage.Vertex, "VsMain", "g_dc_vs")
    defer vs.Close(dev)
    ps := try LoadShaderPair(a, dev, DepthCubePsSpirvZ, DepthCubePsDxilZ, Stage.Fragment, "PsMain", "g_dc_ps")
    defer ps.Close(dev)
    bgl := try dev.NewBindGroupLayout([
        LayoutEntry(binding: 0, visibility: Visibility(vertex: true, fragment: false, compute: false), kind: BindingKind.Uniform(dynamic: false, min_size: 64)),
    ][..], "g_dc_bgl")
    defer bgl.Close(dev)
    pl := try dev.NewPipelineLayout([&bgl][..], 16, "g_dc_pl")
    defer pl.Close(dev)
    attrs := [VertexAttribute(location: 0, format: VertexFormat.Float32x3, offset: 0), VertexAttribute(location: 1, format: VertexFormat.Float32x3, offset: 12)]
    vls := [VertexLayout(stride: 24, step: StepMode.Vertex, attributes: attrs[..])]
    tgts := [ColorOf(TextureFormat.Rgba8Unorm)]
    var pd := DefaultRenderPipeline
    pd.vertex_layouts = vls[..]
    pd.targets = tgts[..]
    pd.depth = some(DepthOf(TextureFormat.Depth32Float, Compare.Less))
    pipe := try dev.NewRenderPipeline(io, &pl, &vs, some(&ps), pd)
    defer pipe.Close(dev)

    var vu := NoBufferUsage
    vu.vertex = true
    vu.copy_dst = true
    vb := try dev.NewBuffer(12 * 24, vu, "g_dc_vb")
    defer vb.Close(dev)
    var vbytes := try a.Slice[u8](12 * 24)
    // Quad A: slopes in X from z=0.2 (left) to z=0.8 (right), red-orange.
    // Quad B: slopes in X from z=0.8 (left) to z=0.2 (right), green-cyan -> crosses Quad A at x=0!
    xs: [6]f32 = [-0.8, 0.8, -0.8, 0.8, 0.8, -0.8]
    ys: [6]f32 = [-0.8, -0.8, 0.8, -0.8, 0.8, 0.8]
    for vi in 0..6 {
        za: f32 = if xs[vi] < 0.0 { 0.2 } else { 0.8 }
        zb: f32 = if xs[vi] < 0.0 { 0.8 } else { 0.2 }
        o1 := vi * 6
        WriteLeF32(mut vbytes[..], o1, xs[vi])
        WriteLeF32(mut vbytes[..], o1 + 1, ys[vi])
        WriteLeF32(mut vbytes[..], o1 + 2, za)
        WriteLeF32(mut vbytes[..], o1 + 3, 0.9)
        WriteLeF32(mut vbytes[..], o1 + 4, 0.2)
        WriteLeF32(mut vbytes[..], o1 + 5, 0.1)

        o2 := (6 + vi) * 6
        WriteLeF32(mut vbytes[..], o2, xs[vi])
        WriteLeF32(mut vbytes[..], o2 + 1, ys[vi] * 0.7)
        WriteLeF32(mut vbytes[..], o2 + 2, zb)
        WriteLeF32(mut vbytes[..], o2 + 3, 0.1)
        WriteLeF32(mut vbytes[..], o2 + 4, 0.85)
        WriteLeF32(mut vbytes[..], o2 + 5, 0.6)
    }
    try dev.WriteBuffer(&vb, 0, vbytes[..])

    var uu := NoBufferUsage
    uu.uniform = true
    uu.copy_dst = true
    ub := try dev.NewBuffer(256, uu, "g_dc_ub")
    defer ub.Close(dev)
    var mvp := try a.Slice[u8](64)
    WriteLeF32(mut mvp[..], 0, 1.0)
    WriteLeF32(mut mvp[..], 5, 1.0)
    WriteLeF32(mut mvp[..], 10, 1.0)
    WriteLeF32(mut mvp[..], 15, 1.0)
    try dev.WriteBuffer(&ub, 0, mvp[..])
    bg := try dev.NewBindGroup(&bgl, [BindEntry(binding: 0, bound: Bound.Buffer(b: &ub, offset: 0, size: 64))][..], "g_dc_bg")
    defer bg.Close(dev)

    var imm := try a.Slice[u8](16)
    for k in 0..4 {
        WriteLeF32(mut imm[..], k, 1.0)
    }
    var no_dyn: [0]u32 = []
    var enc := try dev.Encoder("g_dc_enc")
    onerr enc.Abandon()
    cols := [ColorAttachment(target: rt_v, resolve: none, load: Load.Clear(r: 0.05, g: 0.05, b: 0.1, a: 1.0), store: Store.Store)]
    da := DepthAttachment(target: &dv, depth_load: LoadDepth.Clear(depth: 1.0), depth_store: Store.Store, stencil_load: LoadStencil.DontCare, stencil_store: Store.Discard, read_only: false)
    var rp := enc.RenderPass(&PassDesc(colors: cols[..], depth: some(da), label: "g_dc"))
    rp.SetPipeline(&pipe)
    rp.SetBindGroup(0, &bg, no_dyn[..])
    rp.SetImmediates(0, imm[..])
    rp.SetVertexBuffer(0, &vb, 0)
    rp.Draw(6, 1, 0, 0)
    rp.Draw(6, 1, 6, 0)
    rp.End()
    try enc.Submit()
    return try DevReadImage(a, io, dev, rt, GoldenWidth, GoldenHeight)
}

RenderSceneInstancing(a: &Arena, io: Io, dev: mut Device, rt: &Texture, rt_v: &TextureView): Error!image.Image from a {
    vs := try LoadShaderPair(a, dev, InstancedVsSpirvZ, InstancedVsDxilZ, Stage.Vertex, "VsMain", "g_in_vs")
    defer vs.Close(dev)
    ps := try LoadShaderPair(a, dev, InstancedPsSpirvZ, InstancedPsDxilZ, Stage.Fragment, "PsMain", "g_in_ps")
    defer ps.Close(dev)
    bgl := try dev.NewBindGroupLayout([
        LayoutEntry(binding: 0, visibility: Visibility(vertex: true, fragment: false, compute: false), kind: BindingKind.Storage(read_only: true, min_size: 16, stride: 16)),
    ][..], "g_in_bgl")
    defer bgl.Close(dev)
    pl := try dev.NewPipelineLayout([&bgl][..], 0, "g_in_pl")
    defer pl.Close(dev)
    tgts := [ColorOf(TextureFormat.Rgba8Unorm)]
    var pd := DefaultRenderPipeline
    pd.targets = tgts[..]
    pd.topology = Topology.TriangleStrip
    pipe := try dev.NewRenderPipeline(io, &pl, &vs, some(&ps), pd)
    defer pipe.Close(dev)

    var su := NoBufferUsage
    su.storage = true
    su.copy_dst = true
    sb := try dev.NewBuffer(256, su, "g_in_sb")
    defer sb.Close(dev)
    var sdata := try a.Slice[u8](256)
    for row in 0..4 {
        for col in 0..4 {
            idx := row * 4 + col
            cx := -0.6 + Cast[f32](col) * 0.4
            cy := -0.6 + Cast[f32](row) * 0.4
            bval := Cast[f32](idx) / 15.0
            WriteLeF32(mut sdata[..], idx * 4, cx)
            WriteLeF32(mut sdata[..], idx * 4 + 1, cy)
            WriteLeF32(mut sdata[..], idx * 4 + 2, bval)
            WriteLeF32(mut sdata[..], idx * 4 + 3, 1.0)
        }
    }
    try dev.WriteBuffer(&sb, 0, sdata[..])
    bg := try dev.NewBindGroup(&bgl, [BindEntry(binding: 0, bound: Bound.Buffer(b: &sb, offset: 0, size: 256))][..], "g_in_bg")
    defer bg.Close(dev)

    var no_dyn: [0]u32 = []
    var enc := try dev.Encoder("g_in_enc")
    onerr enc.Abandon()
    cols := [ColorAttachment(target: rt_v, resolve: none, load: Load.Clear(r: 0.0, g: 0.0, b: 0.0, a: 1.0), store: Store.Store)]
    var rp := enc.RenderPass(&PassDesc(colors: cols[..], depth: none, label: "g_in"))
    rp.SetPipeline(&pipe)
    rp.SetBindGroup(0, &bg, no_dyn[..])
    rp.Draw(4, 16, 0, 0)
    rp.End()
    try enc.Submit()
    return try DevReadImage(a, io, dev, rt, GoldenWidth, GoldenHeight)
}

RenderSceneMsaaResolve(a: &Arena, io: Io, dev: mut Device, rt: &Texture, rt_v: &TextureView): Error!image.Image from a {
    ms_tex := try DevNewRtTex(dev, GoldenWidth, GoldenHeight, 1, 4, "g_ms_tex")
    defer ms_tex.Close(dev)
    ms_v := try dev.NewView(&ms_tex, DefaultViewDesc)
    defer ms_v.Close(dev)

    vs := try LoadShaderPair(a, dev, TriVsSpirvZ, TriVsDxilZ, Stage.Vertex, "VsMain", "g_ms_vs")
    defer vs.Close(dev)
    ps := try LoadShaderPair(a, dev, TriPsSpirvZ, TriPsDxilZ, Stage.Fragment, "PsMain", "g_ms_ps")
    defer ps.Close(dev)
    var empty_bgls: [0]&BindGroupLayout = []
    pl := try dev.NewPipelineLayout(empty_bgls[..], 0, "g_ms_pl")
    defer pl.Close(dev)
    tgts := [ColorOf(TextureFormat.Rgba8Unorm)]
    var pd := DefaultRenderPipeline
    pd.targets = tgts[..]
    pd.samples = 4
    pipe := try dev.NewRenderPipeline(io, &pl, &vs, some(&ps), pd)
    defer pipe.Close(dev)

    var enc := try dev.Encoder("g_ms_enc")
    onerr enc.Abandon()
    cols := [ColorAttachment(target: &ms_v, resolve: some(rt_v), load: Load.Clear(r: 0.05, g: 0.05, b: 0.1, a: 1.0), store: Store.Discard)]
    var rp := enc.RenderPass(&PassDesc(colors: cols[..], depth: none, label: "g_ms"))
    rp.SetPipeline(&pipe)
    rp.Draw(3, 1, 0, 0)
    rp.End()
    try enc.Submit()
    return try DevReadImage(a, io, dev, rt, GoldenWidth, GoldenHeight)
}

RenderSceneBlending(a: &Arena, io: Io, dev: mut Device, rt: &Texture, rt_v: &TextureView): Error!image.Image from a {
    vs := try LoadShaderPair(a, dev, BlendMsaaVsSpirvZ, BlendMsaaVsDxilZ, Stage.Vertex, "VsMain", "g_bl_vs")
    defer vs.Close(dev)
    ps := try LoadShaderPair(a, dev, BlendMsaaPsSpirvZ, BlendMsaaPsDxilZ, Stage.Fragment, "PsMain", "g_bl_ps")
    defer ps.Close(dev)
    var empty_bgls: [0]&BindGroupLayout = []
    pl := try dev.NewPipelineLayout(empty_bgls[..], 16, "g_bl_pl")
    defer pl.Close(dev)
    attrs := [VertexAttribute(location: 0, format: VertexFormat.Float32x2, offset: 0)]
    vls := [VertexLayout(stride: 8, step: StepMode.Vertex, attributes: attrs[..])]
    tgts := [ColorTarget(format: TextureFormat.Rgba8Unorm, blend: some(BlendOver), write: WriteAll, sealed: 0)]
    var pd := DefaultRenderPipeline
    pd.vertex_layouts = vls[..]
    pd.targets = tgts[..]
    pipe := try dev.NewRenderPipeline(io, &pl, &vs, some(&ps), pd)
    defer pipe.Close(dev)

    var vu := NoBufferUsage
    vu.vertex = true
    vu.copy_dst = true
    vb := try dev.NewBuffer(48, vu, "g_bl_vb")
    defer vb.Close(dev)
    var vbytes := try a.Slice[u8](48)
    DevWriteFullQuad6(mut vbytes[..])
    try dev.WriteBuffer(&vb, 0, vbytes[..])

    var imm := try a.Slice[u8](16)
    var enc := try dev.Encoder("g_bl_enc")
    onerr enc.Abandon()
    cols := [ColorAttachment(target: rt_v, resolve: none, load: Load.Clear(r: 0.1, g: 0.2, b: 0.3, a: 1.0), store: Store.Store)]
    var rp := enc.RenderPass(&PassDesc(colors: cols[..], depth: none, label: "g_bl"))
    rp.SetPipeline(&pipe)
    rp.SetVertexBuffer(0, &vb, 0)

    WriteLeF32(mut imm[..], 0, 0.5)
    WriteLeF32(mut imm[..], 1, 0.0)
    WriteLeF32(mut imm[..], 2, 0.0)
    WriteLeF32(mut imm[..], 3, 0.5)
    rp.SetImmediates(0, imm[..])
    rp.SetViewport(4.0, 4.0, 40.0, 40.0, 0.0, 1.0)
    rp.Draw(6, 1, 0, 0)

    WriteLeF32(mut imm[..], 0, 0.0)
    WriteLeF32(mut imm[..], 1, 0.5)
    WriteLeF32(mut imm[..], 2, 0.0)
    WriteLeF32(mut imm[..], 3, 0.5)
    rp.SetImmediates(0, imm[..])
    rp.SetViewport(20.0, 12.0, 40.0, 40.0, 0.0, 1.0)
    rp.Draw(6, 1, 0, 0)

    WriteLeF32(mut imm[..], 0, 0.0)
    WriteLeF32(mut imm[..], 1, 0.0)
    WriteLeF32(mut imm[..], 2, 0.5)
    WriteLeF32(mut imm[..], 3, 0.5)
    rp.SetImmediates(0, imm[..])
    rp.SetViewport(12.0, 20.0, 40.0, 40.0, 0.0, 1.0)
    rp.Draw(6, 1, 0, 0)

    rp.End()
    try enc.Submit()
    return try DevReadImage(a, io, dev, rt, GoldenWidth, GoldenHeight)
}

RenderSceneMipChain(a: &Arena, io: Io, dev: mut Device, rt: &Texture, rt_v: &TextureView): Error!image.Image from a {
    vs := try LoadShaderPair(a, dev, TexturedQuadVsSpirvZ, TexturedQuadVsDxilZ, Stage.Vertex, "VsMain", "g_mc_vs")
    defer vs.Close(dev)
    ps := try LoadShaderPair(a, dev, TexturedQuadPsSpirvZ, TexturedQuadPsDxilZ, Stage.Fragment, "PsMain", "g_mc_ps")
    defer ps.Close(dev)
    bgl := try dev.NewBindGroupLayout([
        LayoutEntry(binding: 0, visibility: Visibility(vertex: false, fragment: true, compute: false), kind: BindingKind.Uniform(dynamic: false, min_size: 16)),
        LayoutEntry(binding: 1, visibility: Visibility(vertex: false, fragment: true, compute: false), kind: BindingKind.Texture(sample: SampleType.Float, dimension: ViewDimension.D2, multisampled: false)),
        LayoutEntry(binding: 2, visibility: Visibility(vertex: false, fragment: true, compute: false), kind: BindingKind.Sampler(comparison: false)),
    ][..], "g_mc_bgl")
    defer bgl.Close(dev)
    pl := try dev.NewPipelineLayout([&bgl][..], 0, "g_mc_pl")
    defer pl.Close(dev)
    attrs := [VertexAttribute(location: 0, format: VertexFormat.Float32x2, offset: 0), VertexAttribute(location: 1, format: VertexFormat.Float32x2, offset: 8)]
    vls := [VertexLayout(stride: 16, step: StepMode.Vertex, attributes: attrs[..])]
    tgts := [ColorOf(TextureFormat.Rgba8Unorm)]
    var pd := DefaultRenderPipeline
    pd.vertex_layouts = vls[..]
    pd.targets = tgts[..]
    pipe := try dev.NewRenderPipeline(io, &pl, &vs, some(&ps), pd)
    defer pipe.Close(dev)

    mip_tex := try DevNewRtTex(dev, 64, 64, 4, 1, "g_mc_tex")
    defer mip_tex.Close(dev)
    var m0_px := try a.Slice[u8](64 * 64 * 4)
    for y in 0..64 {
        for x in 0..64 {
            o := (y * 64 + x) * 4
            cx := (x / 8) & 1
            cy := (y / 8) & 1
            m0_px[o] = if cx == 0 { 255 } else { 40 }
            m0_px[o + 1] = if cy == 0 { 220 } else { 40 }
            m0_px[o + 2] = if cx != cy { 200 } else { 20 }
            m0_px[o + 3] = 255
        }
    }
    try dev.WriteTexture(TextureRegion(texture: &mip_tex, mip: 0, layer: 0, x: 0, y: 0, aspect: Aspect.All), m0_px[..], Extent(width: 64, height: 64, layers: 1))

    var vd0 := DefaultViewDesc
    vd0.first_mip = 0
    vd0.mips = some(1)
    v0 := try dev.NewView(&mip_tex, vd0)
    defer v0.Close(dev)
    var vd1 := DefaultViewDesc
    vd1.first_mip = 1
    vd1.mips = some(1)
    v1 := try dev.NewView(&mip_tex, vd1)
    defer v1.Close(dev)
    var vd2 := DefaultViewDesc
    vd2.first_mip = 2
    vd2.mips = some(1)
    v2 := try dev.NewView(&mip_tex, vd2)
    defer v2.Close(dev)
    var vd3 := DefaultViewDesc
    vd3.first_mip = 3
    vd3.mips = some(1)
    v3 := try dev.NewView(&mip_tex, vd3)
    defer v3.Close(dev)

    var sd := DefaultSamplerDesc
    sd.min = Filter.Linear
    sd.mag = Filter.Linear
    sd.mip = Filter.Nearest
    smp := try dev.NewSampler(sd)
    defer smp.Close(dev)

    var vu := NoBufferUsage
    vu.vertex = true
    vu.copy_dst = true
    vb := try dev.NewBuffer(96, vu, "g_mc_vb")
    defer vb.Close(dev)
    var vbytes := try a.Slice[u8](96)
    q6: [24]f32 = [
        -1.0, -1.0, 0.0, 1.0, 1.0, -1.0, 1.0, 1.0, -1.0, 1.0, 0.0, 0.0,
        1.0, -1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 0.0, -1.0, 1.0, 0.0, 0.0,
    ]
    for i in 0..24 {
        WriteLeF32(mut vbytes[..], i, q6[i])
    }
    try dev.WriteBuffer(&vb, 0, vbytes[..])

    var uu := NoBufferUsage
    uu.uniform = true
    uu.copy_dst = true
    ub := try dev.NewBuffer(256, uu, "g_mc_ub")
    defer ub.Close(dev)
    var ubytes := try a.Slice[u8](16)
    for k in 0..4 {
        WriteLeF32(mut ubytes[..], k, 1.0)
    }
    try dev.WriteBuffer(&ub, 0, ubytes[..])

    bg0 := try dev.NewBindGroup(&bgl, [
        BindEntry(binding: 0, bound: Bound.Buffer(b: &ub, offset: 0, size: 16)),
        BindEntry(binding: 1, bound: Bound.View(v: &v0)),
        BindEntry(binding: 2, bound: Bound.Sampler(s: &smp)),
    ][..], "g_mc_bg0")
    defer bg0.Close(dev)
    bg1 := try dev.NewBindGroup(&bgl, [
        BindEntry(binding: 0, bound: Bound.Buffer(b: &ub, offset: 0, size: 16)),
        BindEntry(binding: 1, bound: Bound.View(v: &v1)),
        BindEntry(binding: 2, bound: Bound.Sampler(s: &smp)),
    ][..], "g_mc_bg1")
    defer bg1.Close(dev)
    bg2 := try dev.NewBindGroup(&bgl, [
        BindEntry(binding: 0, bound: Bound.Buffer(b: &ub, offset: 0, size: 16)),
        BindEntry(binding: 1, bound: Bound.View(v: &v2)),
        BindEntry(binding: 2, bound: Bound.Sampler(s: &smp)),
    ][..], "g_mc_bg2")
    defer bg2.Close(dev)
    bg3 := try dev.NewBindGroup(&bgl, [
        BindEntry(binding: 0, bound: Bound.Buffer(b: &ub, offset: 0, size: 16)),
        BindEntry(binding: 1, bound: Bound.View(v: &v3)),
        BindEntry(binding: 2, bound: Bound.Sampler(s: &smp)),
    ][..], "g_mc_bg3")
    defer bg3.Close(dev)

    var no_dyn: [0]u32 = []
    var enc := try dev.Encoder("g_mc_enc")
    onerr enc.Abandon()

    // Downsample mip 0 -> 1 -> 2 -> 3 in 3 render passes.
    c1 := [ColorAttachment(target: &v1, resolve: none, load: Load.DontCare, store: Store.Store)]
    var p1 := enc.RenderPass(&PassDesc(colors: c1[..], depth: none, label: "mip1"))
    p1.SetPipeline(&pipe)
    p1.SetBindGroup(0, &bg0, no_dyn[..])
    p1.SetVertexBuffer(0, &vb, 0)
    p1.Draw(6, 1, 0, 0)
    p1.End()

    c2 := [ColorAttachment(target: &v2, resolve: none, load: Load.DontCare, store: Store.Store)]
    var p2 := enc.RenderPass(&PassDesc(colors: c2[..], depth: none, label: "mip2"))
    p2.SetPipeline(&pipe)
    p2.SetBindGroup(0, &bg1, no_dyn[..])
    p2.SetVertexBuffer(0, &vb, 0)
    p2.Draw(6, 1, 0, 0)
    p2.End()

    c3 := [ColorAttachment(target: &v3, resolve: none, load: Load.DontCare, store: Store.Store)]
    var p3 := enc.RenderPass(&PassDesc(colors: c3[..], depth: none, label: "mip3"))
    p3.SetPipeline(&pipe)
    p3.SetBindGroup(0, &bg2, no_dyn[..])
    p3.SetVertexBuffer(0, &vb, 0)
    p3.Draw(6, 1, 0, 0)
    p3.End()

    // Composite all 4 mips into 4 quadrants of the 64x64 target.
    cf := [ColorAttachment(target: rt_v, resolve: none, load: Load.Clear(r: 0.0, g: 0.0, b: 0.0, a: 1.0), store: Store.Store)]
    var pf := enc.RenderPass(&PassDesc(colors: cf[..], depth: none, label: "mip_out"))
    pf.SetPipeline(&pipe)
    pf.SetVertexBuffer(0, &vb, 0)
    pf.SetViewport(0.0, 0.0, 32.0, 32.0, 0.0, 1.0)
    pf.SetBindGroup(0, &bg0, no_dyn[..])
    pf.Draw(6, 1, 0, 0)
    pf.SetViewport(32.0, 0.0, 32.0, 32.0, 0.0, 1.0)
    pf.SetBindGroup(0, &bg1, no_dyn[..])
    pf.Draw(6, 1, 0, 0)
    pf.SetViewport(0.0, 32.0, 32.0, 32.0, 0.0, 1.0)
    pf.SetBindGroup(0, &bg2, no_dyn[..])
    pf.Draw(6, 1, 0, 0)
    pf.SetViewport(32.0, 32.0, 32.0, 32.0, 0.0, 1.0)
    pf.SetBindGroup(0, &bg3, no_dyn[..])
    pf.Draw(6, 1, 0, 0)
    pf.End()

    try enc.Submit()
    return try DevReadImage(a, io, dev, rt, GoldenWidth, GoldenHeight)
}

RenderSceneDepthSample(a: &Arena, io: Io, dev: mut Device, rt: &Texture, rt_v: &TextureView): Error!image.Image from a {
    var dd := DefaultTextureDesc
    dd.format = TextureFormat.Depth32Float
    dd.width = GoldenWidth
    dd.height = GoldenHeight
    dd.usage = TextureUsage(copy_src: false, copy_dst: false, sampled: true, render_target: true, sealed: 0)
    dtex := try dev.NewTexture(dd)
    defer dtex.Close(dev)
    var dvd := DefaultViewDesc
    dvd.aspect = Aspect.Depth
    dv := try dev.NewView(&dtex, dvd)
    defer dv.Close(dev)

    dc_vs := try LoadShaderPair(a, dev, DepthCubeVsSpirvZ, DepthCubeVsDxilZ, Stage.Vertex, "VsMain", "g_ds_dc_vs")
    defer dc_vs.Close(dev)
    dc_ps := try LoadShaderPair(a, dev, DepthCubePsSpirvZ, DepthCubePsDxilZ, Stage.Fragment, "PsMain", "g_ds_dc_ps")
    defer dc_ps.Close(dev)
    dc_bgl := try dev.NewBindGroupLayout([
        LayoutEntry(binding: 0, visibility: Visibility(vertex: true, fragment: false, compute: false), kind: BindingKind.Uniform(dynamic: false, min_size: 64)),
    ][..], "g_ds_dc_bgl")
    defer dc_bgl.Close(dev)
    dc_pl := try dev.NewPipelineLayout([&dc_bgl][..], 16, "g_ds_dc_pl")
    defer dc_pl.Close(dev)
    attrs := [VertexAttribute(location: 0, format: VertexFormat.Float32x3, offset: 0), VertexAttribute(location: 1, format: VertexFormat.Float32x3, offset: 12)]
    vls := [VertexLayout(stride: 24, step: StepMode.Vertex, attributes: attrs[..])]
    tgts := [ColorOf(TextureFormat.Rgba8Unorm)]
    var dc_pd := DefaultRenderPipeline
    dc_pd.vertex_layouts = vls[..]
    dc_pd.targets = tgts[..]
    dc_pd.depth = some(DepthOf(TextureFormat.Depth32Float, Compare.Less))
    dc_pipe := try dev.NewRenderPipeline(io, &dc_pl, &dc_vs, some(&dc_ps), dc_pd)
    defer dc_pipe.Close(dev)

    ds_vs := try LoadShaderPair(a, dev, DepthSampleVsSpirvZ, DepthSampleVsDxilZ, Stage.Vertex, "VsMain", "g_ds_vs")
    defer ds_vs.Close(dev)
    ds_ps := try LoadShaderPair(a, dev, DepthSamplePsSpirvZ, DepthSamplePsDxilZ, Stage.Fragment, "PsMain", "g_ds_ps")
    defer ds_ps.Close(dev)
    ds_bgl := try dev.NewBindGroupLayout([
        LayoutEntry(binding: 0, visibility: Visibility(vertex: false, fragment: true, compute: false), kind: BindingKind.Texture(sample: SampleType.Depth, dimension: ViewDimension.D2, multisampled: false)),
        LayoutEntry(binding: 1, visibility: Visibility(vertex: false, fragment: true, compute: false), kind: BindingKind.Sampler(comparison: false)),
    ][..], "g_ds_bgl")
    defer ds_bgl.Close(dev)
    ds_pl := try dev.NewPipelineLayout([&ds_bgl][..], 0, "g_ds_pl")
    defer ds_pl.Close(dev)
    var ds_pd := DefaultRenderPipeline
    ds_pd.targets = tgts[..]
    var ro_ds := DepthOf(TextureFormat.Depth32Float, Compare.Always)
    ro_ds.write = false
    ds_pd.depth = some(ro_ds)
    ds_pipe := try dev.NewRenderPipeline(io, &ds_pl, &ds_vs, some(&ds_ps), ds_pd)
    defer ds_pipe.Close(dev)

    var vu := NoBufferUsage
    vu.vertex = true
    vu.copy_dst = true
    vb := try dev.NewBuffer(6 * 24, vu, "g_ds_vb")
    defer vb.Close(dev)
    var vbytes := try a.Slice[u8](6 * 24)
    xs: [6]f32 = [-0.75, 0.75, -0.75, 0.75, 0.75, -0.75]
    ys: [6]f32 = [-0.75, -0.75, 0.75, -0.75, 0.75, 0.75]
    for vi in 0..6 {
        z: f32 = if xs[vi] < 0.0 { 0.15 } else { 0.85 }
        o := vi * 6
        WriteLeF32(mut vbytes[..], o, xs[vi])
        WriteLeF32(mut vbytes[..], o + 1, ys[vi])
        WriteLeF32(mut vbytes[..], o + 2, z)
        WriteLeF32(mut vbytes[..], o + 3, 1.0)
        WriteLeF32(mut vbytes[..], o + 4, 1.0)
        WriteLeF32(mut vbytes[..], o + 5, 1.0)
    }
    try dev.WriteBuffer(&vb, 0, vbytes[..])

    var uu := NoBufferUsage
    uu.uniform = true
    uu.copy_dst = true
    ub := try dev.NewBuffer(256, uu, "g_ds_ub")
    defer ub.Close(dev)
    var mvp := try a.Slice[u8](64)
    WriteLeF32(mut mvp[..], 0, 1.0)
    WriteLeF32(mut mvp[..], 5, 1.0)
    WriteLeF32(mut mvp[..], 10, 1.0)
    WriteLeF32(mut mvp[..], 15, 1.0)
    try dev.WriteBuffer(&ub, 0, mvp[..])
    dc_bg := try dev.NewBindGroup(&dc_bgl, [BindEntry(binding: 0, bound: Bound.Buffer(b: &ub, offset: 0, size: 64))][..], "g_ds_dc_bg")
    defer dc_bg.Close(dev)

    var sd := DefaultSamplerDesc
    sd.min = Filter.Nearest
    sd.mag = Filter.Nearest
    sd.mip = Filter.Nearest
    smp := try dev.NewSampler(sd)
    defer smp.Close(dev)
    ds_bg := try dev.NewBindGroup(&ds_bgl, [
        BindEntry(binding: 0, bound: Bound.View(v: &dv)),
        BindEntry(binding: 1, bound: Bound.Sampler(s: &smp)),
    ][..], "g_ds_bg")
    defer ds_bg.Close(dev)

    var imm := try a.Slice[u8](16)
    for k in 0..4 {
        WriteLeF32(mut imm[..], k, 1.0)
    }
    var no_dyn: [0]u32 = []
    var enc := try dev.Encoder("g_ds_enc")
    onerr enc.Abandon()

    cols := [ColorAttachment(target: rt_v, resolve: none, load: Load.Clear(r: 0.0, g: 0.0, b: 0.0, a: 1.0), store: Store.Store)]
    da_w := DepthAttachment(target: &dv, depth_load: LoadDepth.Clear(depth: 1.0), depth_store: Store.Store, stencil_load: LoadStencil.DontCare, stencil_store: Store.Discard, read_only: false)
    var p1 := enc.RenderPass(&PassDesc(colors: cols[..], depth: some(da_w), label: "ds_write"))
    p1.SetPipeline(&dc_pipe)
    p1.SetBindGroup(0, &dc_bg, no_dyn[..])
    p1.SetImmediates(0, imm[..])
    p1.SetVertexBuffer(0, &vb, 0)
    p1.Draw(6, 1, 0, 0)
    p1.End()

    da_ro := DepthAttachment(target: &dv, depth_load: LoadDepth.Keep, depth_store: Store.Store, stencil_load: LoadStencil.DontCare, stencil_store: Store.Discard, read_only: true)
    var p2 := enc.RenderPass(&PassDesc(colors: cols[..], depth: some(da_ro), label: "ds_sample"))
    p2.SetPipeline(&ds_pipe)
    p2.SetBindGroup(0, &ds_bg, no_dyn[..])
    p2.Draw(3, 1, 0, 0)
    p2.End()

    try enc.Submit()
    return try DevReadImage(a, io, dev, rt, GoldenWidth, GoldenHeight)
}

RenderSceneComputePost(a: &Arena, io: Io, dev: mut Device, rt: &Texture, rt_v: &TextureView): Error!image.Image from a {
    tri_vs := try LoadShaderPair(a, dev, TriVsSpirvZ, TriVsDxilZ, Stage.Vertex, "VsMain", "g_cp_tvs")
    defer tri_vs.Close(dev)
    tri_ps := try LoadShaderPair(a, dev, TriPsSpirvZ, TriPsDxilZ, Stage.Fragment, "PsMain", "g_cp_tps")
    defer tri_ps.Close(dev)
    var empty_bgls: [0]&BindGroupLayout = []
    tri_pl := try dev.NewPipelineLayout(empty_bgls[..], 0, "g_cp_tpl")
    defer tri_pl.Close(dev)
    tgts := [ColorOf(TextureFormat.Rgba8Unorm)]
    var tri_pd := DefaultRenderPipeline
    tri_pd.targets = tgts[..]
    tri_pipe := try dev.NewRenderPipeline(io, &tri_pl, &tri_vs, some(&tri_ps), tri_pd)
    defer tri_pipe.Close(dev)

    cs := try LoadShaderPair(a, dev, PostInvertCsSpirvZ, PostInvertCsDxilZ, Stage.Compute, "CsMain", "g_cp_cs")
    defer cs.Close(dev)
    bgl := try dev.NewBindGroupLayout([
        LayoutEntry(binding: 0, visibility: Visibility(vertex: false, fragment: false, compute: true), kind: BindingKind.Storage(read_only: true, min_size: 4, stride: 4)),
        LayoutEntry(binding: 1, visibility: Visibility(vertex: false, fragment: false, compute: true), kind: BindingKind.Storage(read_only: false, min_size: 4, stride: 4)),
    ][..], "g_cp_bgl")
    defer bgl.Close(dev)
    pl := try dev.NewPipelineLayout([&bgl][..], 0, "g_cp_pl")
    defer pl.Close(dev)
    c_pipe := try dev.NewComputePipeline(io, &pl, &cs, "g_cp_pipe")
    defer c_pipe.Close(dev)

    var su := NoBufferUsage
    su.storage = true
    su.copy_src = true
    su.copy_dst = true
    bytes := GoldenWidth * GoldenHeight * 4
    b_src := try dev.NewBuffer(bytes, su, "g_cp_src")
    defer b_src.Close(dev)
    b_dst := try dev.NewBuffer(bytes, su, "g_cp_dst")
    defer b_dst.Close(dev)
    bg := try dev.NewBindGroup(&bgl, [
        BindEntry(binding: 0, bound: Bound.Buffer(b: &b_src, offset: 0, size: bytes)),
        BindEntry(binding: 1, bound: Bound.Buffer(b: &b_dst, offset: 0, size: bytes)),
    ][..], "g_cp_bg")
    defer bg.Close(dev)

    var no_dyn: [0]u32 = []
    var enc := try dev.Encoder("g_cp_enc")
    onerr enc.Abandon()
    cols := [ColorAttachment(target: rt_v, resolve: none, load: Load.Clear(r: 0.05, g: 0.05, b: 0.1, a: 1.0), store: Store.Store)]
    var rp := enc.RenderPass(&PassDesc(colors: cols[..], depth: none, label: "cp_tri"))
    rp.SetPipeline(&tri_pipe)
    rp.Draw(3, 1, 0, 0)
    rp.End()

    reg := TextureRegion(texture: rt, mip: 0, layer: 0, x: 0, y: 0, aspect: Aspect.All)
    lay := DataLayout(offset: 0, bytes_per_row: GoldenWidth * 4, rows_per_layer: GoldenHeight)
    ext := Extent(width: GoldenWidth, height: GoldenHeight, layers: 1)
    enc.CopyTextureToBuffer(reg, &b_src, lay, ext)

    var cp := enc.ComputePass("cp_inv")
    cp.SetPipeline(&c_pipe)
    cp.SetBindGroup(0, &bg, no_dyn[..])
    cp.Dispatch((GoldenWidth * GoldenHeight) / 64, 1, 1)
    cp.End()

    enc.CopyBufferToTexture(&b_dst, lay, reg, ext)
    try enc.Submit()
    return try DevReadImage(a, io, dev, rt, GoldenWidth, GoldenHeight)
}

RenderGoldenScene(a: &Arena, io: Io, dev: mut Device, idx: int): Error!image.Image from a {
    rt := try DevNewRtTex(dev, GoldenWidth, GoldenHeight, 1, 1, "g_rt")
    defer rt.Close(dev)
    rt_v := try dev.NewView(&rt, DefaultViewDesc)
    defer rt_v.Close(dev)

    return match idx {
        0 { try RenderSceneTriangle(a, io, dev, &rt, &rt_v) }
        1 { try RenderSceneIndexedTexturedQuad(a, io, dev, &rt, &rt_v) }
        2 { try RenderSceneDepthCubes(a, io, dev, &rt, &rt_v) }
        3 { try RenderSceneInstancing(a, io, dev, &rt, &rt_v) }
        4 { try RenderSceneMsaaResolve(a, io, dev, &rt, &rt_v) }
        5 { try RenderSceneBlending(a, io, dev, &rt, &rt_v) }
        6 { try RenderSceneMipChain(a, io, dev, &rt, &rt_v) }
        7 { try RenderSceneDepthSample(a, io, dev, &rt, &rt_v) }
        _ { try RenderSceneComputePost(a, io, dev, &rt, &rt_v) }
    }
}

RenderPinningCullScene(a: &Arena, io: Io, dev: mut Device, front_face: FrontFace): Error!image.Image from a {
    rt := try DevNewRtTex(dev, 64, 64, 1, 1, "pin_cull_rt")
    defer rt.Close(dev)
    rt_v := try dev.NewView(&rt, DefaultViewDesc)
    defer rt_v.Close(dev)

    vs := try LoadShaderPair(a, dev, QuadVsSpirvZ, QuadVsDxilZ, Stage.Vertex, "VsMain", "pc_vs")
    defer vs.Close(dev)
    ps := try LoadShaderPair(a, dev, QuadPsSpirvZ, QuadPsDxilZ, Stage.Fragment, "PsMain", "pc_ps")
    defer ps.Close(dev)
    bgl := try dev.NewBindGroupLayout([
        LayoutEntry(binding: 0, visibility: Visibility(vertex: false, fragment: true, compute: false), kind: BindingKind.Uniform(dynamic: false, min_size: 16)),
    ][..], "pc_bgl")
    defer bgl.Close(dev)
    pl := try dev.NewPipelineLayout([&bgl][..], 0, "pc_pl")
    defer pl.Close(dev)
    attrs := [VertexAttribute(location: 0, format: VertexFormat.Float32x2, offset: 0)]
    vls := [VertexLayout(stride: 8, step: StepMode.Vertex, attributes: attrs[..])]
    tgts := [ColorOf(TextureFormat.Rgba8Unorm)]
    var pd := DefaultRenderPipeline
    pd.vertex_layouts = vls[..]
    pd.targets = tgts[..]
    pd.cull = Cull.Back
    pd.front = front_face
    pipe := try dev.NewRenderPipeline(io, &pl, &vs, some(&ps), pd)
    defer pipe.Close(dev)

    var vu := NoBufferUsage
    vu.vertex = true
    vu.copy_dst = true
    vb := try dev.NewBuffer(48, vu, "pc_vb")
    defer vb.Close(dev)
    var vbytes := try a.Slice[u8](48)
    // Clip space has Y up, framebuffer space Y down. Left triangle (0..2):
    // bottom-left, bottom-right, top, counter-clockwise on screen. Right
    // triangle (3..5): bottom-left, top, bottom-right, clockwise on screen.
    coords: [12]f32 = [
        -0.9, -0.5, -0.1, -0.5, -0.5, 0.5,
        0.1, -0.5, 0.5, 0.5, 0.9, -0.5,
    ]
    for i in 0..12 {
        WriteLeF32(mut vbytes[..], i, coords[i])
    }
    try dev.WriteBuffer(&vb, 0, vbytes[..])

    var uu := NoBufferUsage
    uu.uniform = true
    uu.copy_dst = true
    ub := try dev.NewBuffer(256, uu, "pc_ub")
    defer ub.Close(dev)
    var ubytes := try a.Slice[u8](16)
    WriteLeF32(mut ubytes[..], 0, 1.0)
    WriteLeF32(mut ubytes[..], 1, 0.25)
    WriteLeF32(mut ubytes[..], 2, 0.0)
    WriteLeF32(mut ubytes[..], 3, 1.0)
    try dev.WriteBuffer(&ub, 0, ubytes[..])
    bg := try dev.NewBindGroup(&bgl, [BindEntry(binding: 0, bound: Bound.Buffer(b: &ub, offset: 0, size: 16))][..], "pc_bg")
    defer bg.Close(dev)

    var no_dyn: [0]u32 = []
    var enc := try dev.Encoder("pc_enc")
    onerr enc.Abandon()
    cols := [ColorAttachment(target: &rt_v, resolve: none, load: Load.Clear(r: 0.0, g: 0.0, b: 0.0, a: 1.0), store: Store.Store)]
    var rp := enc.RenderPass(&PassDesc(colors: cols[..], depth: none, label: "pc"))
    rp.SetPipeline(&pipe)
    rp.SetBindGroup(0, &bg, no_dyn[..])
    rp.SetVertexBuffer(0, &vb, 0)
    rp.Draw(6, 1, 0, 0)
    rp.End()
    try enc.Submit()
    return try DevReadImage(a, io, dev, &rt, 64, 64)
}

RenderPinningIdsScene(a: &Arena, io: Io, dev: mut Device): Error!image.Image from a {
    rt := try DevNewRtTex(dev, 64, 64, 1, 1, "pin_id_rt")
    defer rt.Close(dev)
    rt_v := try dev.NewView(&rt, DefaultViewDesc)
    defer rt_v.Close(dev)

    vs := try LoadShaderPair(a, dev, PinningIdsVsSpirvZ, PinningIdsVsDxilZ, Stage.Vertex, "VsMain", "pi_vs")
    defer vs.Close(dev)
    ps := try LoadShaderPair(a, dev, PinningIdsPsSpirvZ, PinningIdsPsDxilZ, Stage.Fragment, "PsMain", "pi_ps")
    defer ps.Close(dev)
    var empty_bgls: [0]&BindGroupLayout = []
    pl := try dev.NewPipelineLayout(empty_bgls[..], 0, "pi_pl")
    defer pl.Close(dev)
    attrs := [VertexAttribute(location: 0, format: VertexFormat.Float32x2, offset: 0), VertexAttribute(location: 1, format: VertexFormat.Float32x4, offset: 8)]
    vls := [VertexLayout(stride: 24, step: StepMode.Vertex, attributes: attrs[..])]
    tgts := [ColorOf(TextureFormat.Rgba8Unorm)]
    var pd := DefaultRenderPipeline
    pd.vertex_layouts = vls[..]
    pd.targets = tgts[..]
    pipe := try dev.NewRenderPipeline(io, &pl, &vs, some(&ps), pd)
    defer pipe.Close(dev)

    // 10 vertices x 24 bytes = 240 bytes.
    // Vertices 0..4 have sentinel blue=0.0; vertices 5..7 (used via base_vertex=5) have blue=0.8.
    var vu := NoBufferUsage
    vu.vertex = true
    vu.copy_dst = true
    vb := try dev.NewBuffer(240, vu, "pi_vb")
    defer vb.Close(dev)
    var vbytes := try a.Slice[u8](240)
    // Vertices 0..4: bad coords / blue=0
    for vi in 0..5 {
        o := vi * 6
        WriteLeF32(mut vbytes[..], o, -0.9)
        WriteLeF32(mut vbytes[..], o + 1, -0.9)
        WriteLeF32(mut vbytes[..], o + 4, 0.0)
        WriteLeF32(mut vbytes[..], o + 5, 1.0)
    }
    // Vertices 5..7: full-screen triangle covering (32, 32) with blue=0.8
    tri_xy: [6]f32 = [-1.0, -1.0, 3.0, -1.0, -1.0, 3.0]
    for k in 0..3 {
        o := (5 + k) * 6
        WriteLeF32(mut vbytes[..], o, tri_xy[k * 2])
        WriteLeF32(mut vbytes[..], o + 1, tri_xy[k * 2 + 1])
        WriteLeF32(mut vbytes[..], o + 4, 0.8)
        WriteLeF32(mut vbytes[..], o + 5, 1.0)
    }
    try dev.WriteBuffer(&vb, 0, vbytes[..])

    var iu := NoBufferUsage
    iu.index = true
    iu.copy_dst = true
    ib := try dev.NewBuffer(16, iu, "pi_ib")
    defer ib.Close(dev)
    var ibytes := try a.Slice[u8](12)
    // Indices 0..1 dummy; indices 2..4 = [0, 1, 2] used via first_index=2, base_vertex=5.
    idxs: [6]u16 = [9, 9, 0, 1, 2, 9]
    for i in 0..6 {
        DevWriteU16(mut ibytes[..], i, idxs[i])
    }
    try dev.WriteBuffer(&ib, 0, ibytes[..])

    var no_dyn: [0]u32 = []
    _ = no_dyn[..]
    var enc := try dev.Encoder("pi_enc")
    onerr enc.Abandon()
    cols := [ColorAttachment(target: &rt_v, resolve: none, load: Load.Clear(r: 0.0, g: 0.0, b: 0.0, a: 1.0), store: Store.Store)]
    var rp := enc.RenderPass(&PassDesc(colors: cols[..], depth: none, label: "pi"))
    rp.SetPipeline(&pipe)
    rp.SetVertexBuffer(0, &vb, 0)
    rp.SetIndexBuffer(&ib, IndexFormat.Uint16, 0)
    // DrawIndexed(indices: 3, instances: 2, first_index: 2, base_vertex: 5, first_instance: 7)
    // Instance 1 overwrites with SV_VertexID in R (0..2/255) and SV_InstanceID=1 in G (1/255), blue=0.8 (~204).
    rp.DrawIndexed(3, 2, 2, 5, 7)
    rp.End()
    try enc.Submit()
    return try DevReadImage(a, io, dev, &rt, 64, 64)
}

test "gpu dump warp goldens" (sys: &System) {
    out_dir := match rt.Env("MK_GPU_DUMP_GOLDENS") {
        some(d) { d }
        none {
            _ = sys.Print("skip: MK_GPU_DUMP_GOLDENS not set\n") else {}
            return
        }
    }
    if Target.os != Os.Windows {
        _ = sys.Print("skip: requires Windows WARP\n", ()) else {}
        return
    }
    var dev := try OpenDeviceForKind(sys.heap, sys.io, "warp", DebugMode.Strict)
    defer AssertCleanLeaks(sys, dev)

    for i in 0..GoldenSceneCount {
        img := try RenderGoldenScene(sys.arena, sys.io, mut dev, i)
        png := try image.EncodePng(sys.arena, img)
        name := GoldenSceneName(i)
        path := try sys.arena.Fmt("{d}/{n}.png", (d: out_dir, n: name))
        try fs.WriteAll(sys.io, path, png)
    }
}
