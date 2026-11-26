// Why: the portable std.gpu surface must drive D3D12 end-to-end on both WARP
// and hardware with Strict debug-layer validation clean, zero leaks, creation
// zeroing, full graphics/compute feature coverage, and caught planted bugs.
//
// What: tests_dx_device.mk exercising D2a + D2b through public std.gpu APIs.
//
// How: selects MK_GPU_ADAPTER ("warp" -> software:true, "d3d12"/unset -> hw),
// runs every shader from tests_shaders.mk, checks readbacks and Strict message
// counts, and verifies planted synchronization faults are caught.

import std.raw
import std.rt
import std.window

DxTestSkipReason(a: &Arena, io: Io): ?str {
    if Target.os != Os.Windows {
        return some("requires Windows")
    }
    match rt.Env("MK_GPU_ADAPTER") {
        some(v) {
            if v.Eq(&"vulkan") {
                return some("MK_GPU_ADAPTER=vulkan")
            }
            if v.Eq(&"warp") {
                return none
            }
        }
        none {}
    }
    ads := Adapters(a, io, Backend.D3d12) else { return some("no d3d12 adapter") }
    for i in 0..ads.Len() {
        if ads[i].kind != AdapterKind.Software {
            return none
        }
    }
    return some("no hardware d3d12 adapter")
}

DxTestWantsSoftware(): bool {
    match rt.Env("MK_GPU_ADAPTER") {
        some(v) {
            if v.Eq(&"warp") { return true }
            if v.Eq(&"d3d12") { return false }
        }
        none {}
    }
    return false
}

OpenDxPortable(heap: &Heap, io: Io, dbg: DebugMode): Error!Device from heap {
    var o := DefaultDeviceOptions
    o.backend = Backend.D3d12
    o.software = DxTestWantsSoftware()
    o.debug = dbg
    return try Open(heap, io, o)
}

WriteLeU32(dst: mut []u8, idx: int, v: u32) {
    o := idx * 4
    dst[o] = Trunc[u8](v & 0xFF)
    dst[o + 1] = Trunc[u8]((v >> 8) & 0xFF)
    dst[o + 2] = Trunc[u8]((v >> 16) & 0xFF)
    dst[o + 3] = Trunc[u8]((v >> 24) & 0xFF)
}

ReadLeU32(src: []u8, idx: int): u32 {
    o := idx * 4
    return u32(src[o]) | (u32(src[o + 1]) << 8) | (u32(src[o + 2]) << 16) | (u32(src[o + 3]) << 24)
}

WriteLeF32(dst: mut []u8, idx: int, v: f32) {
    WriteLeU32(dst, idx, raw.F32Bits(v))
}

ReadLeF32(src: []u8, idx: int): f32 {
    return raw.F32From(ReadLeU32(src, idx))
}

LoadShaderPair(a: &Arena, dev: mut Device, spv_z: str, dxil_z: str, stage: Stage, entry: str, lbl: str): Error!Shader {
    spv := DecodeShader(a, spv_z) else { fail Unsupported }
    dxil := DecodeShader(a, dxil_z) else { fail Unsupported }
    return try dev.NewShader(ShaderCode(spirv: spv, dxil: dxil), stage, entry, lbl)
}

AssertCleanLeaks(sys: &System, dev_in: Device) {
    var dev := dev_in
    msgs := dev.TakeMessages(sys.arena) else { List[Message].New(sys.arena) }
    for i in 0..msgs.Len() {
        m := &msgs[i]
        if m.severity != Severity.Info {
            _ = sys.PrintErr("GPUMSG: {t}\n", (t: m.text)) else {}
        }
    }
    l := dev.Close(sys.io)
    Assert(l.native == 0 && l.errors == 0 && l.warnings == 0)
}

CheckWindowSwapchain(heap: &Heap, io: Io, dev: mut Device) {
    var opts := window.DefaultWindowOptions
    opts.visible = false
    opts.width = 64
    opts.height = 64
    w := window.Open(heap, io, opts) else {
        Assert(false)
        return
    }
    sc := dev.NewSwapchain(io, &w, DefaultSwapchainOptions) else {
        w.Close()
        Assert(false)
        return
    }
    sc.Close(io, dev)
    w.Close()
}

test "dx portable: Adapters, Open Strict, FormatCaps, Limits, and zero leaks" (sys: &System) {
    if r := DxTestSkipReason(sys.arena, sys.io) {
        _ = sys.Print("skip: {s}\n", (s: r)) else {}
        return
    }
    ads := try Adapters(sys.arena, sys.io, Backend.D3d12)
    Assert(ads.Len() >= 1)

    var dev := try OpenDxPortable(sys.heap, sys.io, DebugMode.Strict)
    defer AssertCleanLeaks(sys, dev)

    if DxTestWantsSoftware() {
        Assert(dev.Info().kind == AdapterKind.Software)
    } else {
        Assert(dev.Info().kind != AdapterKind.Software)
    }

    lim := dev.Limits()
    Assert(lim.max_texture_size >= 16384 && lim.max_bind_groups == 4 && lim.uniform_offset_align == 256)

    c_rgba := dev.FormatCaps(TextureFormat.Rgba8Unorm)
    Assert(c_rgba.sampled && c_rgba.filterable && c_rgba.render_target && c_rgba.blendable)
    c_bgra_s := dev.FormatCaps(TextureFormat.Bgra8UnormSrgb)
    Assert(c_bgra_s.sampled && c_bgra_s.render_target && c_bgra_s.blendable)
    c_d32 := dev.FormatCaps(TextureFormat.Depth32Float)
    Assert(c_d32.sampled && c_d32.depth_stencil)

    // W2 window.Open + NewSwapchain works cleanly through portable std.gpu.
    CheckWindowSwapchain(sys.heap, sys.io, mut dev)

    msgs := try dev.TakeMessages(sys.arena)
    _ = msgs
}

test "dx portable zeroing: NewBuffer and NewTexture color + depth start zeroed" (sys: &System) {
    if r := DxTestSkipReason(sys.arena, sys.io) {
        _ = sys.Print("skip: {s}\n", (s: r)) else {}
        return
    }
    var dev := try OpenDxPortable(sys.heap, sys.io, DebugMode.Strict)
    defer AssertCleanLeaks(sys, dev)

    var su := NoBufferUsage
    su.storage = true
    su.copy_src = true
    b_src := try dev.NewBuffer(256, su, "zero_src")
    defer b_src.Close(mut dev)

    var ru := NoBufferUsage
    ru.copy_dst = true
    ru.map_read = true
    b_rb := try dev.NewBuffer(256, ru, "zero_rb")
    defer b_rb.Close(mut dev)

    var enc := try dev.Encoder("zero_copy")
    onerr enc.Abandon()
    enc.CopyBuffer(&b_src, 0, &b_rb, 0, 256)
    try enc.Submit()

    mr := try dev.MapRead(sys.io, &b_rb, 0, 256)
    bs := mr.Bytes()
    for i in 0..256 {
        Assert(bs[i] == 0)
    }
    mr.Unmap()

    var td := DefaultTextureDesc
    td.width = 8
    td.height = 8
    td.usage = TextureUsage(copy_src: true, copy_dst: false, sampled: true, render_target: true, sealed: 0)
    t_col := try dev.NewTexture(td)
    defer t_col.Close(mut dev)
    col_px := try dev.ReadTexture(sys.arena, sys.io, TextureRegion(texture: &t_col, mip: 0, layer: 0, x: 0, y: 0, aspect: Aspect.All), Extent(width: 8, height: 8, layers: 1))
    Assert(col_px.Len() == 8 * 8 * 4)
    for i in 0..col_px.Len() {
        Assert(col_px[i] == 0)
    }

    var dd := DefaultTextureDesc
    dd.format = TextureFormat.Depth32Float
    dd.width = 8
    dd.height = 8
    dd.usage = TextureUsage(copy_src: true, copy_dst: false, sampled: true, render_target: true, sealed: 0)
    t_d32 := try dev.NewTexture(dd)
    defer t_d32.Close(mut dev)
    d32_px := try dev.ReadTexture(sys.arena, sys.io, TextureRegion(texture: &t_d32, mip: 0, layer: 0, x: 0, y: 0, aspect: Aspect.Depth), Extent(width: 8, height: 8, layers: 1))
    for i in 0..d32_px.Len() {
        Assert(d32_px[i] == 0)
    }

    dd.format = TextureFormat.Depth24PlusStencil8
    t_d24 := try dev.NewTexture(dd)
    defer t_d24.Close(mut dev)
    d24_px := try dev.ReadTexture(sys.arena, sys.io, TextureRegion(texture: &t_d24, mip: 0, layer: 0, x: 0, y: 0, aspect: Aspect.Stencil), Extent(width: 8, height: 8, layers: 1))
    for i in 0..d24_px.Len() {
        Assert(d24_px[i] == 0)
    }
}

test "dx portable render: tri, dynamic-uniform quad, and textured_quad" (sys: &System) {
    if r := DxTestSkipReason(sys.arena, sys.io) {
        _ = sys.Print("skip: {s}\n", (s: r)) else {}
        return
    }
    var dev := try OpenDxPortable(sys.heap, sys.io, DebugMode.Strict)
    defer AssertCleanLeaks(sys, dev)

    var rt_desc := DefaultTextureDesc
    rt_desc.width = 16
    rt_desc.height = 16
    rt_desc.usage = TextureUsage(copy_src: true, copy_dst: true, sampled: true, render_target: true, sealed: 0)
    rt := try dev.NewTexture(rt_desc)
    defer rt.Close(mut dev)
    rt_v := try dev.NewView(&rt, DefaultViewDesc)
    defer rt_v.Close(mut dev)

    // 1. tri (SV_VertexID, no VB, no bind group).
    tri_vs := try LoadShaderPair(sys.arena, mut dev, TriVsSpirvZ, TriVsDxilZ, Stage.Vertex, "VsMain", "tri_vs")
    defer tri_vs.Close(mut dev)
    tri_ps := try LoadShaderPair(sys.arena, mut dev, TriPsSpirvZ, TriPsDxilZ, Stage.Fragment, "PsMain", "tri_ps")
    defer tri_ps.Close(mut dev)
    var empty_bgls: [0]&BindGroupLayout = []
    tri_pl := try dev.NewPipelineLayout(empty_bgls[..], 0, "tri_pl")
    defer tri_pl.Close(mut dev)
    targets := [ColorOf(TextureFormat.Rgba8Unorm)]
    var tri_pd := DefaultRenderPipeline
    tri_pd.targets = targets[..]
    tri_pipe := try dev.NewRenderPipeline(sys.io, &tri_pl, &tri_vs, some(&tri_ps), tri_pd)
    defer tri_pipe.Close(mut dev)

    var enc := try dev.Encoder("tri_enc")
    onerr enc.Abandon()
    c_clear := [ColorAttachment(target: &rt_v, resolve: none, load: Load.Clear(r: 0.0, g: 0.0, b: 0.0, a: 1.0), store: Store.Store)]
    var rp := enc.RenderPass(&PassDesc(colors: c_clear[..], depth: none, label: "tri"))
    rp.SetPipeline(&tri_pipe)
    rp.Draw(3, 1, 0, 0)
    rp.End()
    try enc.Submit()

    tri_px := try dev.ReadTexture(sys.arena, sys.io, TextureRegion(texture: &rt, mip: 0, layer: 0, x: 0, y: 0, aspect: Aspect.All), Extent(width: 16, height: 16, layers: 1))
    // Top-left (0,0) is outside triangle -> black clear (0,0,0,255); center (8,10) is inside -> non-zero color.
    Assert(tri_px[0] == 0 && tri_px[1] == 0 && tri_px[2] == 0 && tri_px[3] == 255)
    c_off := (10 * 16 + 8) * 4
    Assert(tri_px[c_off] > 0 || tri_px[c_off + 1] > 0 || tri_px[c_off + 2] > 0)

    // 2. quad with dynamic uniform buffer at offset 0 (red) and offset 256 (green).
    q_vs := try LoadShaderPair(sys.arena, mut dev, QuadVsSpirvZ, QuadVsDxilZ, Stage.Vertex, "VsMain", "q_vs")
    defer q_vs.Close(mut dev)
    q_ps := try LoadShaderPair(sys.arena, mut dev, QuadPsSpirvZ, QuadPsDxilZ, Stage.Fragment, "PsMain", "q_ps")
    defer q_ps.Close(mut dev)
    q_bgl := try dev.NewBindGroupLayout([
        LayoutEntry(binding: 0, visibility: Visibility(vertex: false, fragment: true, compute: false), kind: BindingKind.Uniform(dynamic: true, min_size: 16)),
    ][..], "q_bgl")
    defer q_bgl.Close(mut dev)
    q_bgls := [&q_bgl]
    q_pl := try dev.NewPipelineLayout(q_bgls[..], 0, "q_pl")
    defer q_pl.Close(mut dev)
    q_attrs := [VertexAttribute(location: 0, format: VertexFormat.Float32x2, offset: 0)]
    q_vls := [VertexLayout(stride: 8, step: StepMode.Vertex, attributes: q_attrs[..])]
    var q_pd := DefaultRenderPipeline
    q_pd.vertex_layouts = q_vls[..]
    q_pd.targets = targets[..]
    q_pipe := try dev.NewRenderPipeline(sys.io, &q_pl, &q_vs, some(&q_ps), q_pd)
    defer q_pipe.Close(mut dev)

    var vu := NoBufferUsage
    vu.vertex = true
    vu.copy_dst = true
    q_vb := try dev.NewBuffer(12 * 4, vu, "q_vb")
    defer q_vb.Close(mut dev)
    var q_verts := try sys.arena.Slice[u8](12 * 4)
    // Full-screen triangle-pair quad (2 triangles = 6 vec2).
    q_xy: [12]f32 = [-1.0, -1.0, 1.0, -1.0, -1.0, 1.0, 1.0, -1.0, 1.0, 1.0, -1.0, 1.0]
    for i in 0..12 {
        WriteLeF32(mut q_verts[..], i, q_xy[i])
    }
    try dev.WriteBuffer(&q_vb, 0, q_verts[..])

    var uu := NoBufferUsage
    uu.uniform_dynamic = true
    uu.copy_dst = true
    q_ub := try dev.NewBuffer(512, uu, "q_ub")
    defer q_ub.Close(mut dev)
    var q_udata := try sys.arena.Slice[u8](512)
    WriteLeF32(mut q_udata[..], 0, 1.0)
    WriteLeF32(mut q_udata[..], 1, 0.0)
    WriteLeF32(mut q_udata[..], 2, 0.0)
    WriteLeF32(mut q_udata[..], 3, 1.0)
    WriteLeF32(mut q_udata[..], 64, 0.0)
    WriteLeF32(mut q_udata[..], 65, 1.0)
    WriteLeF32(mut q_udata[..], 66, 0.0)
    WriteLeF32(mut q_udata[..], 67, 1.0)
    try dev.WriteBuffer(&q_ub, 0, q_udata[..])

    q_bg := try dev.NewBindGroup(&q_bgl, [
        BindEntry(binding: 0, bound: Bound.Buffer(b: &q_ub, offset: 0, size: 16)),
    ][..], "q_bg")
    defer q_bg.Close(mut dev)

    var enc2 := try dev.Encoder("q_enc")
    onerr enc2.Abandon()
    var rp2 := enc2.RenderPass(&PassDesc(colors: c_clear[..], depth: none, label: "q_red"))
    rp2.SetPipeline(&q_pipe)
    rp2.SetBindGroup(0, &q_bg, [u32(0)][..])
    rp2.SetVertexBuffer(0, &q_vb, 0)
    rp2.Draw(6, 1, 0, 0)
    rp2.SetBindGroup(0, &q_bg, [u32(256)][..])
    rp2.SetScissor(8, 0, 8, 16)
    rp2.Draw(6, 1, 0, 0)
    rp2.End()
    try enc2.Submit()

    q_px := try dev.ReadTexture(sys.arena, sys.io, TextureRegion(texture: &rt, mip: 0, layer: 0, x: 0, y: 0, aspect: Aspect.All), Extent(width: 16, height: 16, layers: 1))
    // Left half (x=2, y=2) is red (255,0,0,255); right half (x=12, y=2) is green (0,255,0,255).
    left := (2 * 16 + 2) * 4
    right := (2 * 16 + 12) * 4
    Assert(q_px[left] == 255 && q_px[left + 1] == 0 && q_px[left + 2] == 0 && q_px[left + 3] == 255)
    Assert(q_px[right] == 0 && q_px[right + 1] == 255 && q_px[right + 2] == 0 && q_px[right + 3] == 255)

    // 3. textured_quad: WriteTexture + Sampler + BindGroup.
    tq_vs := try LoadShaderPair(sys.arena, mut dev, TexturedQuadVsSpirvZ, TexturedQuadVsDxilZ, Stage.Vertex, "VsMain", "tq_vs")
    defer tq_vs.Close(mut dev)
    tq_ps := try LoadShaderPair(sys.arena, mut dev, TexturedQuadPsSpirvZ, TexturedQuadPsDxilZ, Stage.Fragment, "PsMain", "tq_ps")
    defer tq_ps.Close(mut dev)
    tq_bgl := try dev.NewBindGroupLayout([
        LayoutEntry(binding: 0, visibility: Visibility(vertex: false, fragment: true, compute: false), kind: BindingKind.Uniform(dynamic: false, min_size: 16)),
        LayoutEntry(binding: 1, visibility: Visibility(vertex: false, fragment: true, compute: false), kind: BindingKind.Texture(sample: SampleType.Float, dimension: ViewDimension.D2, multisampled: false)),
        LayoutEntry(binding: 2, visibility: Visibility(vertex: false, fragment: true, compute: false), kind: BindingKind.Sampler(comparison: false)),
    ][..], "tq_bgl")
    defer tq_bgl.Close(mut dev)
    tq_bgls := [&tq_bgl]
    tq_pl := try dev.NewPipelineLayout(tq_bgls[..], 0, "tq_pl")
    defer tq_pl.Close(mut dev)
    tq_a0 := [VertexAttribute(location: 0, format: VertexFormat.Float32x2, offset: 0)]
    tq_a1 := [VertexAttribute(location: 1, format: VertexFormat.Float32x2, offset: 0)]
    tq_vls := [
        VertexLayout(stride: 8, step: StepMode.Vertex, attributes: tq_a0[..]),
        VertexLayout(stride: 8, step: StepMode.Vertex, attributes: tq_a1[..]),
    ]
    var tq_pd := DefaultRenderPipeline
    tq_pd.vertex_layouts = tq_vls[..]
    tq_pd.targets = targets[..]
    tq_pipe := try dev.NewRenderPipeline(sys.io, &tq_pl, &tq_vs, some(&tq_ps), tq_pd)
    defer tq_pipe.Close(mut dev)

    var src_td := DefaultTextureDesc
    src_td.width = 4
    src_td.height = 4
    src_tex := try dev.NewTexture(src_td)
    defer src_tex.Close(mut dev)
    var src_px := try sys.arena.Slice[u8](4 * 4 * 4)
    for pi in 0..(4 * 4) {
        src_px[pi * 4] = 0
        src_px[pi * 4 + 1] = 0
        src_px[pi * 4 + 2] = 255
        src_px[pi * 4 + 3] = 255
    }
    try dev.WriteTexture(TextureRegion(texture: &src_tex, mip: 0, layer: 0, x: 0, y: 0, aspect: Aspect.All), src_px[..], Extent(width: 4, height: 4, layers: 1))
    src_v := try dev.NewView(&src_tex, DefaultViewDesc)
    defer src_v.Close(mut dev)

    var sd := DefaultSamplerDesc
    sd.min = Filter.Nearest
    sd.mag = Filter.Nearest
    sd.mip = Filter.Nearest
    smp := try dev.NewSampler(sd)
    defer smp.Close(mut dev)

    var tq_uv_buf := try dev.NewBuffer(12 * 4, vu, "tq_uv")
    defer tq_uv_buf.Close(mut dev)
    var tq_uvs := try sys.arena.Slice[u8](12 * 4)
    uv_vals: [12]f32 = [0.0, 0.0, 1.0, 0.0, 0.0, 1.0, 1.0, 0.0, 1.0, 1.0, 0.0, 1.0]
    for i in 0..12 {
        WriteLeF32(mut tq_uvs[..], i, uv_vals[i])
    }
    try dev.WriteBuffer(&tq_uv_buf, 0, tq_uvs[..])

    var tq_ub := try dev.NewBuffer(256, BufferUsage(vertex: false, index: false, uniform: true, uniform_dynamic: false, storage: false, copy_src: false, copy_dst: true, map_read: false, map_write: false, sealed: 0), "tq_ub")
    defer tq_ub.Close(mut dev)
    var tq_tint := try sys.arena.Slice[u8](16)
    WriteLeF32(mut tq_tint[..], 0, 1.0)
    WriteLeF32(mut tq_tint[..], 1, 1.0)
    WriteLeF32(mut tq_tint[..], 2, 1.0)
    WriteLeF32(mut tq_tint[..], 3, 1.0)
    try dev.WriteBuffer(&tq_ub, 0, tq_tint[..])

    tq_bg := try dev.NewBindGroup(&tq_bgl, [
        BindEntry(binding: 0, bound: Bound.Buffer(b: &tq_ub, offset: 0, size: 16)),
        BindEntry(binding: 1, bound: Bound.View(v: &src_v)),
        BindEntry(binding: 2, bound: Bound.Sampler(s: &smp)),
    ][..], "tq_bg")
    defer tq_bg.Close(mut dev)

    var no_dyn: [0]u32 = []
    var enc3 := try dev.Encoder("tq_enc")
    onerr enc3.Abandon()
    var rp3 := enc3.RenderPass(&PassDesc(colors: c_clear[..], depth: none, label: "tq"))
    rp3.SetPipeline(&tq_pipe)
    rp3.SetBindGroup(0, &tq_bg, no_dyn[..])
    rp3.SetVertexBuffer(0, &q_vb, 0)
    rp3.SetVertexBuffer(1, &tq_uv_buf, 0)
    rp3.Draw(6, 1, 0, 0)
    rp3.End()
    try enc3.Submit()

    tq_px := try dev.ReadTexture(sys.arena, sys.io, TextureRegion(texture: &rt, mip: 0, layer: 0, x: 0, y: 0, aspect: Aspect.All), Extent(width: 16, height: 16, layers: 1))
    Assert(tq_px[left] == 0 && tq_px[left + 1] == 0 && tq_px[left + 2] == 255 && tq_px[left + 3] == 255)
}

test "dx portable render: depth_cube + depth_sample, blend_msaa resolve, instanced + copies + debug groups" (sys: &System) {
    if r := DxTestSkipReason(sys.arena, sys.io) {
        _ = sys.Print("skip: {s}\n", (s: r)) else {}
        return
    }
    var dev := try OpenDxPortable(sys.heap, sys.io, DebugMode.Strict)
    defer AssertCleanLeaks(sys, dev)

    // 1. depth_cube + depth_sample.
    var col_td := DefaultTextureDesc
    col_td.width = 8
    col_td.height = 8
    col_td.usage = TextureUsage(copy_src: true, copy_dst: true, sampled: true, render_target: true, sealed: 0)
    col_t := try dev.NewTexture(col_td)
    defer col_t.Close(mut dev)
    col_v := try dev.NewView(&col_t, DefaultViewDesc)
    defer col_v.Close(mut dev)

    var dep_td := DefaultTextureDesc
    dep_td.format = TextureFormat.Depth32Float
    dep_td.width = 8
    dep_td.height = 8
    dep_td.usage = TextureUsage(copy_src: true, copy_dst: false, sampled: true, render_target: true, sealed: 0)
    dep_t := try dev.NewTexture(dep_td)
    defer dep_t.Close(mut dev)
    var dv_d := DefaultViewDesc
    dv_d.aspect = Aspect.Depth
    dep_v := try dev.NewView(&dep_t, dv_d)
    defer dep_v.Close(mut dev)

    dc_vs := try LoadShaderPair(sys.arena, mut dev, DepthCubeVsSpirvZ, DepthCubeVsDxilZ, Stage.Vertex, "VsMain", "dc_vs")
    defer dc_vs.Close(mut dev)
    dc_ps := try LoadShaderPair(sys.arena, mut dev, DepthCubePsSpirvZ, DepthCubePsDxilZ, Stage.Fragment, "PsMain", "dc_ps")
    defer dc_ps.Close(mut dev)
    dc_bgl := try dev.NewBindGroupLayout([
        LayoutEntry(binding: 0, visibility: Visibility(vertex: true, fragment: false, compute: false), kind: BindingKind.Uniform(dynamic: false, min_size: 64)),
    ][..], "dc_bgl")
    defer dc_bgl.Close(mut dev)
    dc_bgls := [&dc_bgl]
    dc_pl := try dev.NewPipelineLayout(dc_bgls[..], 16, "dc_pl")
    defer dc_pl.Close(mut dev)

    targets := [ColorOf(TextureFormat.Rgba8Unorm)]
    dc_a0 := [VertexAttribute(location: 0, format: VertexFormat.Float32x3, offset: 0)]
    dc_a1 := [VertexAttribute(location: 1, format: VertexFormat.Float32x3, offset: 0)]
    dc_vls := [
        VertexLayout(stride: 12, step: StepMode.Vertex, attributes: dc_a0[..]),
        VertexLayout(stride: 12, step: StepMode.Vertex, attributes: dc_a1[..]),
    ]
    var dc_pd := DefaultRenderPipeline
    dc_pd.vertex_layouts = dc_vls[..]
    dc_pd.targets = targets[..]
    dc_pd.depth = some(DepthOf(TextureFormat.Depth32Float, Compare.Less))
    dc_pipe := try dev.NewRenderPipeline(sys.io, &dc_pl, &dc_vs, some(&dc_ps), dc_pd)
    defer dc_pipe.Close(mut dev)

    var vu := NoBufferUsage
    vu.vertex = true
    vu.copy_dst = true
    // Two quads: verts 0..5 at z=0.25 (white), verts 6..11 at z=0.75 (red, occluded by z=0.25).
    dc_pos_b := try dev.NewBuffer(36 * 4, vu, "dc_pos")
    defer dc_pos_b.Close(mut dev)
    dc_col_b := try dev.NewBuffer(36 * 4, vu, "dc_col")
    defer dc_col_b.Close(mut dev)
    var dc_pos := try sys.arena.Slice[u8](36 * 4)
    var dc_col := try sys.arena.Slice[u8](36 * 4)
    p3_near: [18]f32 = [-1.0, -1.0, 0.25, 1.0, -1.0, 0.25, -1.0, 1.0, 0.25, 1.0, -1.0, 0.25, 1.0, 1.0, 0.25, -1.0, 1.0, 0.25]
    p3_far: [18]f32 = [-1.0, -1.0, 0.75, 1.0, -1.0, 0.75, -1.0, 1.0, 0.75, 1.0, -1.0, 0.75, 1.0, 1.0, 0.75, -1.0, 1.0, 0.75]
    for i in 0..18 {
        WriteLeF32(mut dc_pos[..], i, p3_near[i])
        WriteLeF32(mut dc_pos[..], 18 + i, p3_far[i])
    }
    for vi in 0..6 {
        WriteLeF32(mut dc_col[..], vi * 3, 0.0)
        WriteLeF32(mut dc_col[..], vi * 3 + 1, 1.0)
        WriteLeF32(mut dc_col[..], vi * 3 + 2, 0.0)
        WriteLeF32(mut dc_col[..], (6 + vi) * 3, 1.0)
        WriteLeF32(mut dc_col[..], (6 + vi) * 3 + 1, 0.0)
        WriteLeF32(mut dc_col[..], (6 + vi) * 3 + 2, 0.0)
    }
    try dev.WriteBuffer(&dc_pos_b, 0, dc_pos[..])
    try dev.WriteBuffer(&dc_col_b, 0, dc_col[..])

    var dc_ub := try dev.NewBuffer(256, BufferUsage(vertex: false, index: false, uniform: true, uniform_dynamic: false, storage: false, copy_src: false, copy_dst: true, map_read: false, map_write: false, sealed: 0), "dc_ub")
    defer dc_ub.Close(mut dev)
    var mvp := try sys.arena.Slice[u8](64)
    WriteLeF32(mut mvp[..], 0, 1.0)
    WriteLeF32(mut mvp[..], 5, 1.0)
    WriteLeF32(mut mvp[..], 10, 1.0)
    WriteLeF32(mut mvp[..], 15, 1.0)
    try dev.WriteBuffer(&dc_ub, 0, mvp[..])
    dc_bg := try dev.NewBindGroup(&dc_bgl, [BindEntry(binding: 0, bound: Bound.Buffer(b: &dc_ub, offset: 0, size: 64))][..], "dc_bg")
    defer dc_bg.Close(mut dev)

    var imm := try sys.arena.Slice[u8](16)
    WriteLeF32(mut imm[..], 0, 1.0)
    WriteLeF32(mut imm[..], 1, 1.0)
    WriteLeF32(mut imm[..], 2, 1.0)
    WriteLeF32(mut imm[..], 3, 1.0)

    var no_dyn: [0]u32 = []
    var enc := try dev.Encoder("dc_enc")
    onerr enc.Abandon()
    enc.PushDebugGroup("depth_cube")
    dc_ca := [ColorAttachment(target: &col_v, resolve: none, load: Load.Clear(r: 0.0, g: 0.0, b: 0.0, a: 1.0), store: Store.Store)]
    dc_da := DepthAttachment(target: &dep_v, depth_load: LoadDepth.Clear(depth: 1.0), depth_store: Store.Store, stencil_load: LoadStencil.DontCare, stencil_store: Store.Discard, read_only: false)
    var rp := enc.RenderPass(&PassDesc(colors: dc_ca[..], depth: some(dc_da), label: "dc"))
    rp.SetPipeline(&dc_pipe)
    rp.SetBindGroup(0, &dc_bg, no_dyn[..])
    rp.SetImmediates(0, imm[..])
    rp.SetVertexBuffer(0, &dc_pos_b, 0)
    rp.SetVertexBuffer(1, &dc_col_b, 0)
    rp.Draw(6, 1, 0, 0)
    rp.Draw(6, 1, 6, 0)
    rp.End()
    enc.PopDebugGroup()
    try enc.Submit()

    dc_px := try dev.ReadTexture(sys.arena, sys.io, TextureRegion(texture: &col_t, mip: 0, layer: 0, x: 0, y: 0, aspect: Aspect.All), Extent(width: 8, height: 8, layers: 1))
    // Near quad (green) kept over far quad (red) by Depth32Float Less test.
    Assert(dc_px[0] == 0 && dc_px[1] == 255 && dc_px[2] == 0 && dc_px[3] == 255)

    // Sample depth texture with depth_sample shader.
    ds_vs := try LoadShaderPair(sys.arena, mut dev, DepthSampleVsSpirvZ, DepthSampleVsDxilZ, Stage.Vertex, "VsMain", "ds_vs")
    defer ds_vs.Close(mut dev)
    ds_ps := try LoadShaderPair(sys.arena, mut dev, DepthSamplePsSpirvZ, DepthSamplePsDxilZ, Stage.Fragment, "PsMain", "ds_ps")
    defer ds_ps.Close(mut dev)
    ds_bgl := try dev.NewBindGroupLayout([
        LayoutEntry(binding: 0, visibility: Visibility(vertex: false, fragment: true, compute: false), kind: BindingKind.Texture(sample: SampleType.Depth, dimension: ViewDimension.D2, multisampled: false)),
        LayoutEntry(binding: 1, visibility: Visibility(vertex: false, fragment: true, compute: false), kind: BindingKind.Sampler(comparison: false)),
    ][..], "ds_bgl")
    defer ds_bgl.Close(mut dev)
    ds_bgls := [&ds_bgl]
    ds_pl := try dev.NewPipelineLayout(ds_bgls[..], 0, "ds_pl")
    defer ds_pl.Close(mut dev)
    var ds_pd := DefaultRenderPipeline
    ds_pd.targets = targets[..]
    ds_pipe := try dev.NewRenderPipeline(sys.io, &ds_pl, &ds_vs, some(&ds_ps), ds_pd)
    defer ds_pipe.Close(mut dev)
    var smp_d := DefaultSamplerDesc
    smp_d.min = Filter.Nearest
    smp_d.mag = Filter.Nearest
    smp_d.mip = Filter.Nearest
    ds_smp := try dev.NewSampler(smp_d)
    defer ds_smp.Close(mut dev)
    ds_bg := try dev.NewBindGroup(&ds_bgl, [
        BindEntry(binding: 0, bound: Bound.View(v: &dep_v)),
        BindEntry(binding: 1, bound: Bound.Sampler(s: &ds_smp)),
    ][..], "ds_bg")
    defer ds_bg.Close(mut dev)

    var enc_ds := try dev.Encoder("ds_enc")
    onerr enc_ds.Abandon()
    var rp_ds := enc_ds.RenderPass(&PassDesc(colors: dc_ca[..], depth: none, label: "ds"))
    rp_ds.SetPipeline(&ds_pipe)
    rp_ds.SetBindGroup(0, &ds_bg, no_dyn[..])
    rp_ds.Draw(3, 1, 0, 0)
    rp_ds.End()
    try enc_ds.Submit()

    ds_px := try dev.ReadTexture(sys.arena, sys.io, TextureRegion(texture: &col_t, mip: 0, layer: 0, x: 0, y: 0, aspect: Aspect.All), Extent(width: 8, height: 8, layers: 1))
    // Depth 0.25 -> ~64 in UNORM8.
    Assert(ds_px[0] >= 60 && ds_px[0] <= 68 && ds_px[3] == 255)

    // 2. blend_msaa: 4x MSAA color + read-only depth + resolve to col_t.
    var ms_td := DefaultTextureDesc
    ms_td.width = 8
    ms_td.height = 8
    ms_td.samples = 4
    ms_td.usage = TextureUsage(copy_src: false, copy_dst: false, sampled: false, render_target: true, sealed: 0)
    ms_col := try dev.NewTexture(ms_td)
    defer ms_col.Close(mut dev)
    ms_col_v := try dev.NewView(&ms_col, DefaultViewDesc)
    defer ms_col_v.Close(mut dev)

    ms_td.format = TextureFormat.Depth32Float
    ms_dep := try dev.NewTexture(ms_td)
    defer ms_dep.Close(mut dev)
    var ms_dv_d := DefaultViewDesc
    ms_dv_d.aspect = Aspect.Depth
    ms_dep_v := try dev.NewView(&ms_dep, ms_dv_d)
    defer ms_dep_v.Close(mut dev)

    bm_vs := try LoadShaderPair(sys.arena, mut dev, BlendMsaaVsSpirvZ, BlendMsaaVsDxilZ, Stage.Vertex, "VsMain", "bm_vs")
    defer bm_vs.Close(mut dev)
    bm_ps := try LoadShaderPair(sys.arena, mut dev, BlendMsaaPsSpirvZ, BlendMsaaPsDxilZ, Stage.Fragment, "PsMain", "bm_ps")
    defer bm_ps.Close(mut dev)
    var empty_bgls: [0]&BindGroupLayout = []
    bm_pl := try dev.NewPipelineLayout(empty_bgls[..], 16, "bm_pl")
    defer bm_pl.Close(mut dev)
    bm_tgts := [ColorTarget(format: TextureFormat.Rgba8Unorm, blend: some(BlendOver), write: WriteAll, sealed: 0)]
    bm_attrs := [VertexAttribute(location: 0, format: VertexFormat.Float32x2, offset: 0)]
    bm_vls := [VertexLayout(stride: 8, step: StepMode.Vertex, attributes: bm_attrs[..])]
    var bm_pd := DefaultRenderPipeline
    bm_pd.vertex_layouts = bm_vls[..]
    bm_pd.targets = bm_tgts[..]
    bm_pd.samples = 4
    var ro_ds := DepthOf(TextureFormat.Depth32Float, Compare.Always)
    ro_ds.write = false
    bm_pd.depth = some(ro_ds)
    bm_pipe := try dev.NewRenderPipeline(sys.io, &bm_pl, &bm_vs, some(&bm_ps), bm_pd)
    defer bm_pipe.Close(mut dev)

    q_vb2 := try dev.NewBuffer(12 * 4, vu, "bm_vb")
    defer q_vb2.Close(mut dev)
    var bm_verts := try sys.arena.Slice[u8](12 * 4)
    q_xy: [12]f32 = [-1.0, -1.0, 1.0, -1.0, -1.0, 1.0, 1.0, -1.0, 1.0, 1.0, -1.0, 1.0]
    for i in 0..12 {
        WriteLeF32(mut bm_verts[..], i, q_xy[i])
    }
    try dev.WriteBuffer(&q_vb2, 0, bm_verts[..])

    var bm_imm := try sys.arena.Slice[u8](16)
    WriteLeF32(mut bm_imm[..], 0, 0.5)
    WriteLeF32(mut bm_imm[..], 1, 0.0)
    WriteLeF32(mut bm_imm[..], 2, 0.0)
    WriteLeF32(mut bm_imm[..], 3, 0.5)

    var enc_bm := try dev.Encoder("bm_enc")
    onerr enc_bm.Abandon()
    bm_ca := [ColorAttachment(target: &ms_col_v, resolve: some(&col_v), load: Load.Clear(r: 0.0, g: 0.0, b: 1.0, a: 1.0), store: Store.Discard)]
    bm_da := DepthAttachment(target: &ms_dep_v, depth_load: LoadDepth.Keep, depth_store: Store.Store, stencil_load: LoadStencil.DontCare, stencil_store: Store.Discard, read_only: true)
    var rp_bm := enc_bm.RenderPass(&PassDesc(colors: bm_ca[..], depth: some(bm_da), label: "bm"))
    rp_bm.SetPipeline(&bm_pipe)
    rp_bm.SetImmediates(0, bm_imm[..])
    rp_bm.SetVertexBuffer(0, &q_vb2, 0)
    rp_bm.Draw(6, 1, 0, 0)
    rp_bm.End()
    try enc_bm.Submit()

    bm_px := try dev.ReadTexture(sys.arena, sys.io, TextureRegion(texture: &col_t, mip: 0, layer: 0, x: 0, y: 0, aspect: Aspect.All), Extent(width: 8, height: 8, layers: 1))
    // Premultiplied over (0.5, 0, 0, 0.5) on (0, 0, 1, 1) -> (~0.5, 0, ~0.5, 1.0).
    Assert(bm_px[0] >= 120 && bm_px[0] <= 136 && bm_px[1] == 0 && bm_px[2] >= 120 && bm_px[2] <= 136 && bm_px[3] == 255)

    // 3. instanced + SetViewport + SetScissor + SetBlendConstant + SetStencilReference + CopyTexture + CopyTextureToBuffer + CopyBufferToTexture.
    in_vs := try LoadShaderPair(sys.arena, mut dev, InstancedVsSpirvZ, InstancedVsDxilZ, Stage.Vertex, "VsMain", "in_vs")
    defer in_vs.Close(mut dev)
    in_ps := try LoadShaderPair(sys.arena, mut dev, InstancedPsSpirvZ, InstancedPsDxilZ, Stage.Fragment, "PsMain", "in_ps")
    defer in_ps.Close(mut dev)
    in_bgl := try dev.NewBindGroupLayout([
        LayoutEntry(binding: 0, visibility: Visibility(vertex: true, fragment: false, compute: false), kind: BindingKind.Storage(read_only: true, min_size: 16, stride: 16)),
    ][..], "in_bgl")
    defer in_bgl.Close(mut dev)
    in_bgls := [&in_bgl]
    in_pl := try dev.NewPipelineLayout(in_bgls[..], 0, "in_pl")
    defer in_pl.Close(mut dev)
    var in_pd := DefaultRenderPipeline
    in_pd.targets = targets[..]
    in_pd.topology = Topology.TriangleStrip
    in_pipe := try dev.NewRenderPipeline(sys.io, &in_pl, &in_vs, some(&in_ps), in_pd)
    defer in_pipe.Close(mut dev)

    var su := NoBufferUsage
    su.storage = true
    su.copy_dst = true
    in_sb := try dev.NewBuffer(64, su, "in_sb")
    defer in_sb.Close(mut dev)
    in_bg := try dev.NewBindGroup(&in_bgl, [BindEntry(binding: 0, bound: Bound.Buffer(b: &in_sb, offset: 0, size: 32))][..], "in_bg")
    defer in_bg.Close(mut dev)
    var col2_t := try dev.NewTexture(col_td)
    defer col2_t.Close(mut dev)
    var stg_u := NoBufferUsage
    stg_u.copy_src = true
    stg_u.copy_dst = true
    stg_u.map_read = false
    stg_b := try dev.NewBuffer(256 * 8, stg_u, "stg_b")
    defer stg_b.Close(mut dev)

    var enc_c := try dev.Encoder("in_enc")
    onerr enc_c.Abandon()
    var in_data := try sys.arena.Slice[u8](32)
    WriteLeF32(mut in_data[..], 0, 0.0)
    WriteLeF32(mut in_data[..], 1, 0.0)
    WriteLeF32(mut in_data[..], 2, 1.0)
    WriteLeF32(mut in_data[..], 3, 0.0)
    WriteLeF32(mut in_data[..], 4, 0.5)
    WriteLeF32(mut in_data[..], 5, 0.5)
    WriteLeF32(mut in_data[..], 6, 0.5)
    WriteLeF32(mut in_data[..], 7, 0.0)
    try enc_c.WriteBuffer(&in_sb, 0, in_data[..])

    var rp_in := enc_c.RenderPass(&PassDesc(colors: dc_ca[..], depth: none, label: "inst"))
    rp_in.PushDebugGroup("inst_draw")
    rp_in.SetPipeline(&in_pipe)
    rp_in.SetBindGroup(0, &in_bg, no_dyn[..])
    rp_in.SetViewport(0.0, 0.0, 8.0, 8.0, 0.0, 1.0)
    rp_in.SetScissor(0, 0, 8, 8)
    rp_in.SetBlendConstant(1.0, 1.0, 1.0, 1.0)
    rp_in.SetStencilReference(1)
    rp_in.Draw(4, 2, 0, 0)
    rp_in.PopDebugGroup()
    rp_in.End()

    src_reg := TextureRegion(texture: &col_t, mip: 0, layer: 0, x: 0, y: 0, aspect: Aspect.All)
    dst_reg := TextureRegion(texture: &col2_t, mip: 0, layer: 0, x: 0, y: 0, aspect: Aspect.All)
    lay := DataLayout(offset: 0, bytes_per_row: 256, rows_per_layer: 8)
    ext := Extent(width: 8, height: 8, layers: 1)
    enc_c.CopyTextureToBuffer(src_reg, &stg_b, lay, ext)
    enc_c.CopyBufferToTexture(&stg_b, lay, dst_reg, ext)
    enc_c.CopyTexture(dst_reg, src_reg, ext)
    try enc_c.Submit()

    in_px := try dev.ReadTexture(sys.arena, sys.io, src_reg, ext)
    Assert(in_px.Len() == 8 * 8 * 4)
}

test "dx portable compute: double, prefix_sum->post_invert UAV->SRV, ClearBuffer, MapWrite, deferred close" (sys: &System) {
    if r := DxTestSkipReason(sys.arena, sys.io) {
        _ = sys.Print("skip: {s}\n", (s: r)) else {}
        return
    }
    var dev := try OpenDxPortable(sys.heap, sys.io, DebugMode.Strict)
    defer AssertCleanLeaks(sys, dev)

    dbl_cs := try LoadShaderPair(sys.arena, mut dev, DoubleCsSpirvZ, DoubleCsDxilZ, Stage.Compute, "CsMain", "dbl_cs")
    defer dbl_cs.Close(mut dev)
    ps_cs := try LoadShaderPair(sys.arena, mut dev, PrefixSumCsSpirvZ, PrefixSumCsDxilZ, Stage.Compute, "CsMain", "ps_cs")
    defer ps_cs.Close(mut dev)
    pi_cs := try LoadShaderPair(sys.arena, mut dev, PostInvertCsSpirvZ, PostInvertCsDxilZ, Stage.Compute, "CsMain", "pi_cs")
    defer pi_cs.Close(mut dev)

    rw_bgl := try dev.NewBindGroupLayout([
        LayoutEntry(binding: 0, visibility: Visibility(vertex: false, fragment: false, compute: true), kind: BindingKind.Storage(read_only: false, min_size: 256, stride: 4)),
    ][..], "rw_bgl")
    defer rw_bgl.Close(mut dev)
    rw_bgls := [&rw_bgl]
    rw_pl := try dev.NewPipelineLayout(rw_bgls[..], 0, "rw_pl")
    defer rw_pl.Close(mut dev)
    dbl_pipe := try dev.NewComputePipeline(sys.io, &rw_pl, &dbl_cs, "dbl_pipe")
    defer dbl_pipe.Close(mut dev)
    ps_pipe := try dev.NewComputePipeline(sys.io, &rw_pl, &ps_cs, "ps_pipe")
    defer ps_pipe.Close(mut dev)

    pi_bgl := try dev.NewBindGroupLayout([
        LayoutEntry(binding: 0, visibility: Visibility(vertex: false, fragment: false, compute: true), kind: BindingKind.Storage(read_only: true, min_size: 256, stride: 4)),
        LayoutEntry(binding: 1, visibility: Visibility(vertex: false, fragment: false, compute: true), kind: BindingKind.Storage(read_only: false, min_size: 256, stride: 4)),
    ][..], "pi_bgl")
    defer pi_bgl.Close(mut dev)
    pi_bgls := [&pi_bgl]
    pi_pl := try dev.NewPipelineLayout(pi_bgls[..], 0, "pi_pl")
    defer pi_pl.Close(mut dev)
    pi_pipe := try dev.NewComputePipeline(sys.io, &pi_pl, &pi_cs, "pi_pipe")
    defer pi_pipe.Close(mut dev)

    var wu := NoBufferUsage
    wu.copy_src = true
    wu.map_write = true
    b_up := try dev.NewBuffer(256, wu, "b_up")
    defer b_up.Close(mut dev)
    var mw := try dev.MapWrite(sys.io, &b_up, 0, 16)
    mw_b := mw.Bytes()
    WriteLeF32(mw_b, 0, 1.5)
    WriteLeF32(mw_b, 1, 2.5)
    WriteLeF32(mw_b, 2, 3.5)
    WriteLeF32(mw_b, 3, 4.5)
    mw.Unmap()

    var su := NoBufferUsage
    su.storage = true
    su.copy_src = true
    su.copy_dst = true
    b_flt := try dev.NewBuffer(256, su, "b_flt")
    defer b_flt.Close(mut dev)
    b_a := try dev.NewBuffer(256, su, "b_a")
    defer b_a.Close(mut dev)
    b_b := try dev.NewBuffer(256, su, "b_b")
    defer b_b.Close(mut dev)

    var ru := NoBufferUsage
    ru.copy_dst = true
    ru.map_read = true
    b_rb := try dev.NewBuffer(256, ru, "b_rb")
    defer b_rb.Close(mut dev)

    var a_init := try sys.arena.Slice[u8](256)
    WriteLeU32(mut a_init[..], 0, 1)
    WriteLeU32(mut a_init[..], 1, 2)
    WriteLeU32(mut a_init[..], 2, 3)
    WriteLeU32(mut a_init[..], 3, 4)
    try dev.WriteBuffer(&b_a, 0, a_init[..])

    dbl_bg := try dev.NewBindGroup(&rw_bgl, [BindEntry(binding: 0, bound: Bound.Buffer(b: &b_flt, offset: 0, size: 256))][..], "dbl_bg")
    defer dbl_bg.Close(mut dev)
    ps_bg := try dev.NewBindGroup(&rw_bgl, [BindEntry(binding: 0, bound: Bound.Buffer(b: &b_a, offset: 0, size: 256))][..], "ps_bg")
    defer ps_bg.Close(mut dev)
    pi_bg := try dev.NewBindGroup(&pi_bgl, [
        BindEntry(binding: 0, bound: Bound.Buffer(b: &b_a, offset: 0, size: 256)),
        BindEntry(binding: 1, bound: Bound.Buffer(b: &b_b, offset: 0, size: 256)),
    ][..], "pi_bg")
    defer pi_bg.Close(mut dev)

    var no_dyn: [0]u32 = []
    var enc := try dev.Encoder("comp_enc")
    onerr enc.Abandon()
    enc.CopyBuffer(&b_up, 0, &b_flt, 0, 256)
    var cp := enc.ComputePass("comp")
    cp.SetPipeline(&dbl_pipe)
    cp.SetBindGroup(0, &dbl_bg, no_dyn[..])
    cp.Dispatch(1, 1, 1)
    // prefix_sum -> post_invert in same compute pass tests UAV->SRV transition.
    cp.SetPipeline(&ps_pipe)
    cp.SetBindGroup(0, &ps_bg, no_dyn[..])
    cp.Dispatch(1, 1, 1)
    cp.SetPipeline(&pi_pipe)
    cp.SetBindGroup(0, &pi_bg, no_dyn[..])
    cp.Dispatch(1, 1, 1)
    cp.End()
    enc.CopyBuffer(&b_flt, 0, &b_rb, 0, 16)
    enc.CopyBuffer(&b_b, 0, &b_rb, 16, 16)
    try enc.Submit()

    mr := try dev.MapRead(sys.io, &b_rb, 0, 32)
    rb := mr.Bytes()
    Assert(ReadLeF32(rb, 0) == 3.0 && ReadLeF32(rb, 1) == 5.0 && ReadLeF32(rb, 2) == 7.0 && ReadLeF32(rb, 3) == 9.0)
    Assert(ReadLeU32(rb, 4) == (1 ^ 0x00FFFFFF))
    Assert(ReadLeU32(rb, 5) == (3 ^ 0x00FFFFFF))
    Assert(ReadLeU32(rb, 6) == (6 ^ 0x00FFFFFF))
    Assert(ReadLeU32(rb, 7) == (10 ^ 0x00FFFFFF))
    mr.Unmap()

    // ClearBuffer + deferred close immediately after Submit before WaitIdle.
    tmp_b := try dev.NewBuffer(256, su, "tmp_def")
    onerr tmp_b.Close(mut dev)
    var enc2 := try dev.Encoder("clr_enc")
    onerr enc2.Abandon()
    enc2.ClearBuffer(&b_b, 0, 256)
    enc2.ClearBuffer(&tmp_b, 0, 256)
    enc2.CopyBuffer(&b_b, 0, &b_rb, 0, 256)
    try enc2.Submit()
    tmp_b.Close(mut dev)
    try dev.WaitIdle(sys.io)

    mr2 := try dev.MapRead(sys.io, &b_rb, 0, 256)
    rb2 := mr2.Bytes()
    for i in 0..256 {
        Assert(rb2[i] == 0)
    }
    mr2.Unmap()
}

RunPlantedBugCheck(sys: &System, bug: PlantedBug): Error!(errs: int, warns: int, failed: bool) {
    var dev := try OpenDxPortable(sys.heap, sys.io, DebugMode.Strict)
    onerr dev.Abandon(sys.io)
    dev.SetPlantedBug(bug)

    var failed := false
    match bug {
        .SkipDecay {
            // Submit 1 uses b as CopySrc (read-only); Submit 2 clears b as CopyDst without decay reset.
            var su := NoBufferUsage
            su.copy_src = true
            su.copy_dst = true
            b := try dev.NewBuffer(256, su, "decay_b")
            defer b.Close(mut dev)
            b2 := try dev.NewBuffer(256, su, "decay_b2")
            defer b2.Close(mut dev)

            var enc2 := try dev.Encoder("e2")
            onerr enc2.Abandon()
            enc2.CopyBuffer(&b, 0, &b2, 0, 256)
            try enc2.Submit()
            try dev.WaitIdle(sys.io)

            var enc3 := try dev.Encoder("e3")
            onerr enc3.Abandon()
            enc3.ClearBuffer(&b, 0, 256)
            enc3.Submit() else {
                failed = true
                enc3.Abandon()
            }
            // SkipDecay emits a stale cross-list transition barrier from ResCopySrc -> ResCopyDst instead of promoting from COMMON.
            bars := &dev.state.Ref().dx_barriers
            if bars.Len() == 1 && bars.Items()[0].old_state == ResCopySrc && bars.Items()[0].new_state == ResCopyDst {
                failed = true
            }
            _ = dev.WaitIdle(sys.io) else { failed = true }
        }
        .SkipPassBarrier {
            var td := DefaultTextureDesc
            td.width = 4
            td.height = 4
            td.usage = TextureUsage(copy_src: true, copy_dst: false, sampled: false, render_target: true, sealed: 0)
            t := try dev.NewTexture(td)
            defer t.Close(mut dev)
            v := try dev.NewView(&t, DefaultViewDesc)
            defer v.Close(mut dev)
            var ru := NoBufferUsage
            ru.copy_dst = true
            rb := try dev.NewBuffer(256 * 4, ru, "pb_rb")
            defer rb.Close(mut dev)
            var enc := try dev.Encoder("pb_enc")
            onerr enc.Abandon()
            cols := [ColorAttachment(target: &v, resolve: none, load: Load.Clear(r: 0.2, g: 0.4, b: 0.6, a: 1.0), store: Store.Store)]
            var rp := enc.RenderPass(&PassDesc(colors: cols[..], depth: none, label: "pb"))
            rp.End()
            enc.CopyTextureToBuffer(TextureRegion(texture: &t, mip: 0, layer: 0, x: 0, y: 0, aspect: Aspect.All), &rb, DataLayout(offset: 0, bytes_per_row: 256, rows_per_layer: 4), Extent(width: 4, height: 4, layers: 1))
            enc.Submit() else {
                failed = true
                enc.Abandon()
            }
            _ = dev.WaitIdle(sys.io) else { failed = true }
        }
        .SkipDirtyGroupOnLayout {
            // Pipe A and Pipe B have distinct PipelineLayouts ([bgl_u], 0). Switching A->B must re-emit
            // OpSetGroup for group 0; SkipDirtyGroupOnLayout skips marking DirtyGroup0, emitting only 1 OpSetGroup.
            q_vs := try LoadShaderPair(sys.arena, mut dev, QuadVsSpirvZ, QuadVsDxilZ, Stage.Vertex, "VsMain", "q_vs")
            defer q_vs.Close(mut dev)
            q_ps := try LoadShaderPair(sys.arena, mut dev, QuadPsSpirvZ, QuadPsDxilZ, Stage.Fragment, "PsMain", "q_ps")
            defer q_ps.Close(mut dev)
            bgl_u := try dev.NewBindGroupLayout([
                LayoutEntry(binding: 0, visibility: Visibility(vertex: false, fragment: true, compute: false), kind: BindingKind.Uniform(dynamic: false, min_size: 16)),
            ][..], "bgl_u")
            defer bgl_u.Close(mut dev)
            bgls := [&bgl_u]
            pl_a := try dev.NewPipelineLayout(bgls[..], 0, "pl_a")
            defer pl_a.Close(mut dev)
            pl_b := try dev.NewPipelineLayout(bgls[..], 0, "pl_b")
            defer pl_b.Close(mut dev)
            targets := [ColorOf(TextureFormat.Rgba8Unorm)]
            attrs := [VertexAttribute(location: 0, format: VertexFormat.Float32x2, offset: 0)]
            vls := [VertexLayout(stride: 8, step: StepMode.Vertex, attributes: attrs[..])]
            var pd := DefaultRenderPipeline
            pd.vertex_layouts = vls[..]
            pd.targets = targets[..]
            pipe_a := try dev.NewRenderPipeline(sys.io, &pl_a, &q_vs, some(&q_ps), pd)
            defer pipe_a.Close(mut dev)
            pipe_b := try dev.NewRenderPipeline(sys.io, &pl_b, &q_vs, some(&q_ps), pd)
            defer pipe_b.Close(mut dev)

            var vu := NoBufferUsage
            vu.vertex = true
            vu.copy_dst = true
            vb := try dev.NewBuffer(12 * 4, vu, "vb")
            defer vb.Close(mut dev)
            var vbytes := try sys.arena.Slice[u8](12 * 4)
            q_xy: [12]f32 = [-1.0, -1.0, 1.0, -1.0, -1.0, 1.0, 1.0, -1.0, 1.0, 1.0, -1.0, 1.0]
            for i in 0..12 {
                WriteLeF32(mut vbytes[..], i, q_xy[i])
            }
            try dev.WriteBuffer(&vb, 0, vbytes[..])

            var uu := NoBufferUsage
            uu.uniform = true
            ub := try dev.NewBuffer(256, uu, "ub")
            defer ub.Close(mut dev)
            bg := try dev.NewBindGroup(&bgl_u, [BindEntry(binding: 0, bound: Bound.Buffer(b: &ub, offset: 0, size: 16))][..], "bg")
            defer bg.Close(mut dev)

            var td := DefaultTextureDesc
            td.width = 4
            td.height = 4
            td.usage = TextureUsage(copy_src: false, copy_dst: false, sampled: false, render_target: true, sealed: 0)
            rt := try dev.NewTexture(td)
            defer rt.Close(mut dev)
            rt_v := try dev.NewView(&rt, DefaultViewDesc)
            defer rt_v.Close(mut dev)

            var no_dyn: [0]u32 = []
            var enc := try dev.Encoder("dg_enc")
            onerr enc.Abandon()
            cols := [ColorAttachment(target: &rt_v, resolve: none, load: Load.Clear(r: 0.0, g: 0.0, b: 0.0, a: 1.0), store: Store.Store)]
            var rp := enc.RenderPass(&PassDesc(colors: cols[..], depth: none, label: "dg"))
            rp.SetPipeline(&pipe_a)
            rp.SetBindGroup(0, &bg, no_dyn[..])
            rp.SetVertexBuffer(0, &vb, 0)
            rp.Draw(6, 1, 0, 0)
            rp.SetPipeline(&pipe_b)
            rp.Draw(6, 1, 0, 0)
            rp.End()
            enc.Submit() else {
                failed = true
                enc.Abandon()
            }
            // Draw 1: BarrierRef(2)+Begin(2)+Pipe(2)+Group(2)+VB(3)+VP(4)+Scissor(3)+Blend(3)+Stencil(2)+Draw(3)=26.
            // Draw 2: Pipe(2)+Draw(3)=5 without re-emitting SetGroup(2) -> total 32 words (+End 1 = 33).
            chks := dev.state.Ref().dx_chunks.Items()
            if chks.Len() == 1 && chks[0].len < 34 {
                failed = true
            }
            _ = dev.WaitIdle(sys.io) else { failed = true }
        }
        .SkipComputeUavBarrier {
            dbl_cs := try LoadShaderPair(sys.arena, mut dev, DoubleCsSpirvZ, DoubleCsDxilZ, Stage.Compute, "CsMain", "dbl_cs")
            defer dbl_cs.Close(mut dev)
            rw_bgl := try dev.NewBindGroupLayout([
                LayoutEntry(binding: 0, visibility: Visibility(vertex: false, fragment: false, compute: true), kind: BindingKind.Storage(read_only: false, min_size: 256, stride: 4)),
            ][..], "rw_bgl")
            defer rw_bgl.Close(mut dev)
            rw_bgls := [&rw_bgl]
            rw_pl := try dev.NewPipelineLayout(rw_bgls[..], 0, "rw_pl")
            defer rw_pl.Close(mut dev)
            dbl_pipe := try dev.NewComputePipeline(sys.io, &rw_pl, &dbl_cs, "dbl_pipe")
            defer dbl_pipe.Close(mut dev)

            var su := NoBufferUsage
            su.storage = true
            b := try dev.NewBuffer(256, su, "uav_b")
            defer b.Close(mut dev)
            bg := try dev.NewBindGroup(&rw_bgl, [BindEntry(binding: 0, bound: Bound.Buffer(b: &b, offset: 0, size: 256))][..], "bg")
            defer bg.Close(mut dev)
            var no_dyn: [0]u32 = []

            var enc := try dev.Encoder("uav_enc")
            onerr enc.Abandon()
            var cp := enc.ComputePass("cp")
            cp.SetPipeline(&dbl_pipe)
            cp.SetBindGroup(0, &bg, no_dyn[..])
            cp.Dispatch(1, 1, 1)
            cp.Dispatch(1, 1, 1)
            cp.End()
            enc.Submit() else {
                failed = true
                enc.Abandon()
            }
            // SkipComputeUavBarrier omits the intra-pass UAV barrier between consecutive writable dispatches.
            if dev.state.Ref().dx_barriers.Len() == 0 {
                failed = true
            }
            _ = dev.WaitIdle(sys.io) else { failed = true }
        }
        _ {}
    }

    leaks := dev.Close(sys.io)
    Assert(leaks.native == 0)
    return (errs: leaks.errors, warns: leaks.warnings, failed: failed)
}

test "dx portable sync & planted bugs: WaitIdle cancel, SkipDecay, SkipPassBarrier" (sys: &System) {
    if r := DxTestSkipReason(sys.arena, sys.io) {
        _ = sys.Print("skip: {s}\n", (s: r)) else {}
        return
    }
    var dev := try OpenDxPortable(sys.heap, sys.io, DebugMode.Strict)
    defer AssertCleanLeaks(sys, dev)

    var su := NoBufferUsage
    su.copy_dst = true
    b := try dev.NewBuffer(256, su, "wi_b")
    defer b.Close(mut dev)
    var enc := try dev.Encoder("wi_enc")
    onerr enc.Abandon()
    enc.ClearBuffer(&b, 0, 256)
    try enc.Submit()

    scope cscope := sys.io.Scope() {
        cscope.Cancel()
        _ = dev.WaitIdle(sys.io) else e {
            match e {
                Canceled {}
                _ { Assert(false) }
            }
        }
    }
    try dev.WaitIdle(sys.io)

    r_pb := try RunPlantedBugCheck(sys, PlantedBug.SkipPassBarrier)
    Assert(r_pb.failed || r_pb.errs > 0 || r_pb.warns > 0)

    r_dg := try RunPlantedBugCheck(sys, PlantedBug.SkipDirtyGroupOnLayout)
    Assert(r_dg.failed || r_dg.errs > 0 || r_dg.warns > 0)

    r_decay := try RunPlantedBugCheck(sys, PlantedBug.SkipDecay)
    Assert(r_decay.failed || r_decay.errs > 0 || r_decay.warns > 0)

    r_uav := try RunPlantedBugCheck(sys, PlantedBug.SkipComputeUavBarrier)
    Assert(r_uav.failed || r_uav.errs > 0 || r_uav.warns > 0)
}
