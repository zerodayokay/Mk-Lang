// Why: rendering a parsed Scene requires managing a GPU device, pipelines,
// textures with pass-generated mips, shadow maps, dynamic uniforms, 4,096
// instanced draws, MSAA 4x resolve, blended overlays, and compute blur.
//
// What: RenderOptions, GpuMesh, GpuTexture, DrawItem, ObjectXform,
// SceneRenderer and NewRenderer, which creates every device object a scene needs.
//
// How: owns GPU handles in linear structs and HeapLists, passes disjoint struct
// fields to std.gpu calls, records into one Encoder per frame with debug
// groups, and releases every handle in dependency order on Close or onerr.

import std.image
import std.math
import std.window
import std.gpu
import src.math3d
import src.obj
import src.scenedoc
import src.shaders

const ShadowSize: int = 512
const BlurTileSize: int = 8
const MaxDynamicObjects: int = 64
const DynamicStride: int = 256
// The frame uniform buffer holds the 176-byte frame block at 0 and a copy of
// the light matrix at ShadowUboOffset, as a uniform binding offset must be a
// multiple of 256.
const ShadowUboOffset: int = 256
const FrameUboSize: int = 512

/// Options controlling device selection and render behavior.
export struct RenderOptions {
    export backend: gpu.Backend
    export software: bool
    export debug: gpu.DebugMode
    export present_mode: gpu.PresentMode
}

/// Default render options (Auto backend, hardware preferred, no debug layer).
export const DefaultRenderOptions: RenderOptions = RenderOptions(
    backend: gpu.Backend.Auto,
    software: false,
    debug: gpu.DebugMode.Off,
    present_mode: gpu.PresentMode.Fifo,
)

/// Uploaded GPU buffers for one mesh asset.
export linear struct GpuMesh {
    vb: gpu.Buffer
    ib: gpu.Buffer
    index_count: int
}

/// Uploaded GPU texture with its full mip chain view and per-mip render views.
export linear view struct GpuTexture {
    tex: gpu.Texture
    full_view: gpu.TextureView
    mip_views: HeapList[gpu.TextureView]
    width: int
    height: int
    mips: int
}

/// Per-object draw reference bound to a GPU mesh, texture, and dynamic offset.
export struct DrawItem {
    mesh_idx: int
    tex_idx: int
    dyn_offset: u32
    alpha: f32
}

/// Per-object transform and tint stored on the renderer (plain, no loans).
export struct ObjectXform {
    tx: f32
    ty: f32
    tz: f32
    rot_y: f32
    scale: f32
    color: math3d.Vec4
}

/// Complete GPU state for rendering a Scene offscreen or into a window.
export linear view struct SceneRenderer {
    heap: &Heap
    dev: gpu.Device
    sc: ?gpu.Swapchain

    sh_shadow_vs: gpu.Shader
    sh_mip_vs: gpu.Shader
    sh_mip_ps: gpu.Shader
    sh_scene_vs: gpu.Shader
    sh_scene_ps: gpu.Shader
    sh_inst_vs: gpu.Shader
    sh_inst_ps: gpu.Shader
    sh_ov_vs: gpu.Shader
    sh_ov_ps: gpu.Shader
    sh_blur_cs: gpu.Shader

    bgl_shadow: gpu.BindGroupLayout
    pl_shadow: gpu.PipelineLayout
    pipe_shadow: gpu.RenderPipeline

    bgl_mip: gpu.BindGroupLayout
    pl_mip: gpu.PipelineLayout
    pipe_mip: gpu.RenderPipeline

    bgl_scene: gpu.BindGroupLayout
    pl_scene: gpu.PipelineLayout
    pipe_scene: gpu.RenderPipeline
    pipe_scene_msaa: gpu.RenderPipeline

    bgl_inst: gpu.BindGroupLayout
    pl_inst: gpu.PipelineLayout
    pipe_inst: gpu.RenderPipeline
    pipe_inst_msaa: gpu.RenderPipeline

    pl_ov: gpu.PipelineLayout
    pipe_ov: gpu.RenderPipeline
    pipe_ov_msaa: gpu.RenderPipeline

    bgl_blur: gpu.BindGroupLayout
    pl_blur: gpu.PipelineLayout
    pipe_blur: gpu.ComputePipeline

    samp_linear: gpu.Sampler
    samp_shadow: gpu.Sampler

    frame_ubo: gpu.Buffer
    draw_ubo: gpu.Buffer
    inst_buf: gpu.Buffer
    blur_buf_a: gpu.Buffer
    blur_buf_b: gpu.Buffer

    shadow_tex: gpu.Texture
    shadow_view: gpu.TextureView

    color_tex: gpu.Texture
    color_view: gpu.TextureView
    msaa_tex: gpu.Texture
    msaa_view: gpu.TextureView
    depth_tex: gpu.Texture
    depth_view: gpu.TextureView
    msaa_depth_tex: gpu.Texture
    msaa_depth_view: gpu.TextureView

    bg_shadow: gpu.BindGroup
    bg_inst: gpu.BindGroup
    bg_blur_h: gpu.BindGroup
    bg_blur_v: gpu.BindGroup
    scene_bgs: HeapList[gpu.BindGroup]

    meshes: HeapList[GpuMesh]
    textures: HeapList[GpuTexture]

    draw_items: HeapList[DrawItem]
    objects_data: HeapList[ObjectXform]

    width: int
    height: int
    blur_stride_u32: int
    msaa: int
    shadows: bool
    blur: bool
    overlay: bool
    instances: int
    clear_r: f64
    clear_g: f64
    clear_b: f64
    clear_a: f64
    camera: scenedoc.Camera
    light: scenedoc.Light
    yaw_deg: f32
    pitch_deg: f32
    zoom: f32
    sc_format: gpu.TextureFormat
}

CloseMeshes(a: &Alloc, ms_in: HeapList[GpuMesh], dev: mut gpu.Device) {
    var ms := ms_in
    for ms.Len() > 0 {
        match ms.Pop() {
            some(gm) {
                var GpuMesh(vb, ib, index_count: _) := gm
                vb.Close(dev)
                ib.Close(dev)
            }
            none { break }
        }
    }
    ms.FreeEmpty(a)
}

CloseViews(a: &Alloc, vs_in: HeapList[gpu.TextureView], dev: mut gpu.Device) {
    var vs := vs_in
    for vs.Len() > 0 {
        match vs.Pop() {
            some(v) { v.Close(dev) }
            none { break }
        }
    }
    vs.FreeEmpty(a)
}

CloseTextures(a: &Alloc, ts_in: HeapList[GpuTexture], dev: mut gpu.Device) {
    var ts := ts_in
    for ts.Len() > 0 {
        match ts.Pop() {
            some(gt) {
                var GpuTexture(tex, full_view, mip_views, width: _, height: _, mips: _) := gt
                CloseViews(a, mip_views, dev)
                full_view.Close(dev)
                tex.Close(dev)
            }
            none { break }
        }
    }
    ts.FreeEmpty(a)
}

CloseGroups(a: &Alloc, gs_in: HeapList[gpu.BindGroup], dev: mut gpu.Device) {
    var gs := gs_in
    for gs.Len() > 0 {
        match gs.Pop() {
            some(g) { g.Close(dev) }
            none { break }
        }
    }
    gs.FreeEmpty(a)
}

linear struct TargetSet {
    color_tex: gpu.Texture
    color_view: gpu.TextureView
    msaa_tex: gpu.Texture
    msaa_view: gpu.TextureView
    depth_tex: gpu.Texture
    depth_view: gpu.TextureView
    msaa_depth_tex: gpu.Texture
    msaa_depth_view: gpu.TextureView
}

TargetSet.Close(self, dev: mut gpu.Device) {
    var TargetSet(
        color_tex,
        color_view,
        msaa_tex,
        msaa_view,
        depth_tex,
        depth_view,
        msaa_depth_tex,
        msaa_depth_view,
    ) := self
    msaa_depth_view.Close(dev)
    msaa_depth_tex.Close(dev)
    depth_view.Close(dev)
    depth_tex.Close(dev)
    msaa_view.Close(dev)
    msaa_tex.Close(dev)
    color_view.Close(dev)
    color_tex.Close(dev)
}

MipCountFor(w: int, h: int): int {
    var m := 1
    var cw := w
    var ch := h
    for cw > 1 && ch > 1 && m < 5 {
        cw = cw / 2
        ch = ch / 2
        m += 1
    }
    return m
}

UploadMesh(a: &Arena, dev: mut gpu.Device, mesh: &obj.Mesh, label: str): gpu.Error|OutOfMemory!GpuMesh {
    vb_bytes := try obj.PackVertices(a, mesh)
    ib_bytes := try obj.PackIndices(a, mesh)

    var vu := gpu.NoBufferUsage
    vu.vertex = true
    vu.copy_dst = true
    vb := try dev.NewBuffer(vb_bytes.Len(), vu, label)
    onerr vb.Close(dev)
    try dev.WriteBuffer(&vb, 0, vb_bytes)

    var iu := gpu.NoBufferUsage
    iu.index = true
    iu.copy_dst = true
    ib := try dev.NewBuffer(ib_bytes.Len(), iu, label)
    onerr ib.Close(dev)
    try dev.WriteBuffer(&ib, 0, ib_bytes)

    return GpuMesh(vb: vb, ib: ib, index_count: mesh.indices.Len())
}

UploadTexture(
    alloc: &Alloc,
    dev: mut gpu.Device,
    img: &image.Image,
    label: str,
): gpu.Error|OutOfMemory!GpuTexture from alloc {
    mips := MipCountFor(img.width, img.height)
    var td := gpu.DefaultTextureDesc
    td.width = img.width
    td.height = img.height
    td.mips = mips
    td.format = gpu.TextureFormat.Rgba8Unorm
    td.usage.sampled = true
    td.usage.render_target = true
    td.usage.copy_dst = true
    td.label = label
    tex := try dev.NewTexture(td)
    onerr tex.Close(dev)

    region := gpu.TextureRegion(texture: &tex, mip: 0, layer: 0, x: 0, y: 0, aspect: gpu.Aspect.All)
    try dev.WriteTexture(region, img.data, gpu.Extent(width: img.width, height: img.height, layers: 1))

    full_view := try dev.NewView(&tex, gpu.DefaultViewDesc)
    onerr full_view.Close(dev)

    var mip_views := HeapList[gpu.TextureView].New(alloc)
    onerr CloseViews(alloc, mip_views, dev)
    for m in 0..mips {
        var vd := gpu.DefaultViewDesc
        vd.first_mip = m
        vd.mips = some(1)
        mv := try dev.NewView(&tex, vd)
        onerr mv.Close(dev)
        try mip_views.Push(alloc, mv)
    }
    return GpuTexture(tex: tex, full_view: full_view, mip_views: mip_views, width: img.width, height: img.height, mips: mips)
}

WriteInstances(a: &Arena, dev: mut gpu.Device, buf: &gpu.Buffer, count: int): gpu.Error|OutOfMemory!void {
    if count <= 0 {
        return
    }
    bytes := try a.Slice[u8](count * 32)
    // Arrange up to 4,096 instances on a 16x16x16 lattice or 64x64 grid.
    var side := 1
    for side * side < count {
        side += 1
    }
    f_side := Cast[f32](side)
    for i in 0..count {
        ix := i % side
        iz := i / side
        fx := (Cast[f32](ix) / f_side - 0.5) * 8.0
        fz := (Cast[f32](iz) / f_side - 0.5) * 8.0
        fy := -0.85 + 0.15 * math.SinF32(Cast[f32](ix) * 0.35 + Cast[f32](iz) * 0.25)
        scale: f32 = 0.055
        r := 0.3 + 0.65 * (Cast[f32](ix) / f_side)
        g := 0.35 + 0.55 * (Cast[f32](iz) / f_side)
        b := 0.85 - 0.5 * (Cast[f32](ix) / f_side)
        o := i * 32
        math3d.PutVec4(bytes, o, math3d.V4(fx, fy, fz, scale))
        math3d.PutVec4(bytes, o + 16, math3d.V4(r, g, b, 1.0))
    }
    try dev.WriteBuffer(buf, 0, bytes[..])
}

CreateTargetSet(
    dev: mut gpu.Device,
    w: int,
    h: int,
    color_fmt: gpu.TextureFormat,
): gpu.Error!TargetSet {
    var cd := gpu.DefaultTextureDesc
    cd.width = w
    cd.height = h
    cd.format = color_fmt
    cd.usage.render_target = true
    cd.usage.copy_src = true
    cd.usage.copy_dst = true
    cd.usage.sampled = true
    cd.label = "color"
    ct := try dev.NewTexture(cd)
    onerr ct.Close(dev)
    cv := try dev.NewView(&ct, gpu.DefaultViewDesc)
    onerr cv.Close(dev)

    var mcd := gpu.DefaultTextureDesc
    mcd.width = w
    mcd.height = h
    mcd.samples = 4
    mcd.format = color_fmt
    mcd.usage.render_target = true
    mcd.label = "msaa_color"
    mct := try dev.NewTexture(mcd)
    onerr mct.Close(dev)
    mcv := try dev.NewView(&mct, gpu.DefaultViewDesc)
    onerr mcv.Close(dev)

    var dd := gpu.DefaultTextureDesc
    dd.width = w
    dd.height = h
    dd.format = gpu.TextureFormat.Depth32Float
    dd.usage.render_target = true
    dd.usage.sampled = true
    dd.label = "depth"
    dt := try dev.NewTexture(dd)
    onerr dt.Close(dev)
    var dv_d := gpu.DefaultViewDesc
    dv_d.aspect = gpu.Aspect.Depth
    dv := try dev.NewView(&dt, dv_d)
    onerr dv.Close(dev)

    var mdd := gpu.DefaultTextureDesc
    mdd.width = w
    mdd.height = h
    mdd.samples = 4
    mdd.format = gpu.TextureFormat.Depth32Float
    mdd.usage.render_target = true
    mdd.label = "msaa_depth"
    mdt := try dev.NewTexture(mdd)
    onerr mdt.Close(dev)
    mdv := try dev.NewView(&mdt, dv_d)

    return TargetSet(
        color_tex: ct,
        color_view: cv,
        msaa_tex: mct,
        msaa_view: mcv,
        depth_tex: dt,
        depth_view: dv,
        msaa_depth_tex: mdt,
        msaa_depth_view: mdv,
    )
}

BuildRenderer(
    heap: &Heap,
    io: Io,
    sc: &scenedoc.Scene,
    opts: RenderOptions,
    win: ?&window.Window,
): gpu.Error|OutOfMemory!SceneRenderer from heap {
    alloc := heap.Alloc()
    var ar := try Arena.New(heap)
    defer ar.Free()

    var dev_opts := gpu.DefaultDeviceOptions
    dev_opts.backend = opts.backend
    dev_opts.software = opts.software
    dev_opts.debug = opts.debug
    var dev := try gpu.Open(heap, io, dev_opts)
    onerr dev.Abandon(io)

    var so := gpu.DefaultSwapchainOptions
    so.mode = opts.present_mode
    so.copy_src = true
    var swap: ?gpu.Swapchain = match win {
        some(w) { some(try dev.NewSwapchain(io, w, so)) }
        none { none }
    }
    onerr {
        if s_obj := swap {
            s_obj.Close(io, mut dev)
        }
    }
    sc_fmt := match &swap {
        some(s_obj) { dev.SwapchainInfo(s_obj).format }
        none { gpu.TextureFormat.Rgba8Unorm }
    }

    // Load and validate all shaders.
    sh_shadow_vs := try dev.NewShader(shaders.CodeOf(shaders.ShadowVsSpv, shaders.ShadowVsDxil), gpu.Stage.Vertex, "VsMain", "shadow_vs")
    onerr sh_shadow_vs.Close(mut dev)
    sh_mip_vs := try dev.NewShader(shaders.CodeOf(shaders.MipVsSpv, shaders.MipVsDxil), gpu.Stage.Vertex, "VsMain", "mip_vs")
    onerr sh_mip_vs.Close(mut dev)
    sh_mip_ps := try dev.NewShader(shaders.CodeOf(shaders.MipPsSpv, shaders.MipPsDxil), gpu.Stage.Fragment, "PsMain", "mip_ps")
    onerr sh_mip_ps.Close(mut dev)
    sh_scene_vs := try dev.NewShader(shaders.CodeOf(shaders.SceneVsSpv, shaders.SceneVsDxil), gpu.Stage.Vertex, "VsMain", "scene_vs")
    onerr sh_scene_vs.Close(mut dev)
    sh_scene_ps := try dev.NewShader(shaders.CodeOf(shaders.ScenePsSpv, shaders.ScenePsDxil), gpu.Stage.Fragment, "PsMain", "scene_ps")
    onerr sh_scene_ps.Close(mut dev)
    sh_inst_vs := try dev.NewShader(shaders.CodeOf(shaders.InstancedVsSpv, shaders.InstancedVsDxil), gpu.Stage.Vertex, "VsMain", "inst_vs")
    onerr sh_inst_vs.Close(mut dev)
    sh_inst_ps := try dev.NewShader(shaders.CodeOf(shaders.InstancedPsSpv, shaders.InstancedPsDxil), gpu.Stage.Fragment, "PsMain", "inst_ps")
    onerr sh_inst_ps.Close(mut dev)
    sh_ov_vs := try dev.NewShader(shaders.CodeOf(shaders.OverlayVsSpv, shaders.OverlayVsDxil), gpu.Stage.Vertex, "VsMain", "ov_vs")
    onerr sh_ov_vs.Close(mut dev)
    sh_ov_ps := try dev.NewShader(shaders.CodeOf(shaders.OverlayPsSpv, shaders.OverlayPsDxil), gpu.Stage.Fragment, "PsMain", "ov_ps")
    onerr sh_ov_ps.Close(mut dev)
    sh_blur_cs := try dev.NewShader(shaders.CodeOf(shaders.BlurCsSpv, shaders.BlurCsDxil), gpu.Stage.Compute, "CsMain", "blur_cs")
    onerr sh_blur_cs.Close(mut dev)

    v_vert := gpu.Visibility(vertex: true, fragment: false, compute: false)
    v_frag := gpu.Visibility(vertex: false, fragment: true, compute: false)
    v_vf := gpu.Visibility(vertex: true, fragment: true, compute: false)
    v_comp := gpu.Visibility(vertex: false, fragment: false, compute: true)

    // Layouts & pipelines.
    sh_entries := [
        gpu.LayoutEntry(binding: 0, visibility: v_vert, kind: gpu.BindingKind.Uniform(dynamic: false, min_size: 64)),
        gpu.LayoutEntry(binding: 1, visibility: v_vert, kind: gpu.BindingKind.Uniform(dynamic: true, min_size: 96)),
    ]
    bgl_shadow := try dev.NewBindGroupLayout(sh_entries[..], "shadow_bgl")
    onerr bgl_shadow.Close(mut dev)
    sh_groups := [&bgl_shadow]
    pl_shadow := try dev.NewPipelineLayout(sh_groups[..], 0, "shadow_pl")
    onerr pl_shadow.Close(mut dev)

    mesh_attrs := [
        gpu.VertexAttribute(location: 0, format: gpu.VertexFormat.Float32x3, offset: 0),
        gpu.VertexAttribute(location: 1, format: gpu.VertexFormat.Float32x3, offset: 12),
        gpu.VertexAttribute(location: 2, format: gpu.VertexFormat.Float32x2, offset: 24),
    ]
    mesh_vls := [gpu.VertexLayout(stride: 32, step: gpu.StepMode.Vertex, attributes: mesh_attrs[..])]

    var no_targets: [0]gpu.ColorTarget = []
    var pd_shadow := gpu.DefaultRenderPipeline
    pd_shadow.vertex_layouts = mesh_vls[..]
    pd_shadow.targets = no_targets[..]
    pd_shadow.cull = gpu.Cull.Back
    pd_shadow.depth = some(gpu.DepthOf(gpu.TextureFormat.Depth32Float, gpu.Compare.Less))
    pd_shadow.label = "shadow_pipe"
    pipe_shadow := try dev.NewRenderPipeline(io, &pl_shadow, &sh_shadow_vs, none, pd_shadow)
    onerr pipe_shadow.Close(mut dev)

    // Mip pipeline.
    mip_entries := [
        gpu.LayoutEntry(binding: 0, visibility: v_frag, kind: gpu.BindingKind.Texture(sample: gpu.SampleType.Float, dimension: gpu.ViewDimension.D2, multisampled: false)),
        gpu.LayoutEntry(binding: 1, visibility: v_frag, kind: gpu.BindingKind.Sampler(comparison: false)),
    ]
    bgl_mip := try dev.NewBindGroupLayout(mip_entries[..], "mip_bgl")
    onerr bgl_mip.Close(mut dev)
    mip_groups := [&bgl_mip]
    pl_mip := try dev.NewPipelineLayout(mip_groups[..], 16, "mip_pl")
    onerr pl_mip.Close(mut dev)
    mip_tgts := [gpu.ColorOf(gpu.TextureFormat.Rgba8Unorm)]
    var pd_mip := gpu.DefaultRenderPipeline
    pd_mip.targets = mip_tgts[..]
    pd_mip.label = "mip_pipe"
    pipe_mip := try dev.NewRenderPipeline(io, &pl_mip, &sh_mip_vs, some(&sh_mip_ps), pd_mip)
    onerr pipe_mip.Close(mut dev)

    // Scene pipeline (1x and 4x MSAA).
    sc_entries := [
        gpu.LayoutEntry(binding: 0, visibility: v_vf, kind: gpu.BindingKind.Uniform(dynamic: false, min_size: 176)),
        gpu.LayoutEntry(binding: 1, visibility: v_vf, kind: gpu.BindingKind.Uniform(dynamic: true, min_size: 96)),
        gpu.LayoutEntry(binding: 2, visibility: v_frag, kind: gpu.BindingKind.Texture(sample: gpu.SampleType.Float, dimension: gpu.ViewDimension.D2, multisampled: false)),
        gpu.LayoutEntry(binding: 3, visibility: v_frag, kind: gpu.BindingKind.Sampler(comparison: false)),
        gpu.LayoutEntry(binding: 4, visibility: v_frag, kind: gpu.BindingKind.Texture(sample: gpu.SampleType.Depth, dimension: gpu.ViewDimension.D2, multisampled: false)),
        gpu.LayoutEntry(binding: 5, visibility: v_frag, kind: gpu.BindingKind.Sampler(comparison: true)),
    ]
    bgl_scene := try dev.NewBindGroupLayout(sc_entries[..], "scene_bgl")
    onerr bgl_scene.Close(mut dev)
    sc_groups := [&bgl_scene]
    pl_scene := try dev.NewPipelineLayout(sc_groups[..], 16, "scene_pl")
    onerr pl_scene.Close(mut dev)
    sc_tgts := [gpu.ColorOf(sc_fmt)]
    var pd_scene := gpu.DefaultRenderPipeline
    pd_scene.vertex_layouts = mesh_vls[..]
    pd_scene.targets = sc_tgts[..]
    pd_scene.cull = gpu.Cull.Back
    pd_scene.depth = some(gpu.DepthOf(gpu.TextureFormat.Depth32Float, gpu.Compare.Less))
    pd_scene.label = "scene_pipe"
    pipe_scene := try dev.NewRenderPipeline(io, &pl_scene, &sh_scene_vs, some(&sh_scene_ps), pd_scene)
    onerr pipe_scene.Close(mut dev)

    var pd_scene_msaa := pd_scene
    pd_scene_msaa.samples = 4
    pd_scene_msaa.label = "scene_msaa"
    pipe_scene_msaa := try dev.NewRenderPipeline(io, &pl_scene, &sh_scene_vs, some(&sh_scene_ps), pd_scene_msaa)
    onerr pipe_scene_msaa.Close(mut dev)

    // Instanced pipeline (4,096 instances from storage buffer).
    inst_entries := [
        gpu.LayoutEntry(binding: 0, visibility: v_vf, kind: gpu.BindingKind.Uniform(dynamic: false, min_size: 176)),
        gpu.LayoutEntry(binding: 1, visibility: v_vert, kind: gpu.BindingKind.Storage(read_only: true, min_size: 16, stride: 16)),
    ]
    bgl_inst := try dev.NewBindGroupLayout(inst_entries[..], "inst_bgl")
    onerr bgl_inst.Close(mut dev)
    inst_groups := [&bgl_inst]
    pl_inst := try dev.NewPipelineLayout(inst_groups[..], 16, "inst_pl")
    onerr pl_inst.Close(mut dev)
    var pd_inst := gpu.DefaultRenderPipeline
    pd_inst.vertex_layouts = mesh_vls[..]
    pd_inst.targets = sc_tgts[..]
    pd_inst.cull = gpu.Cull.Back
    pd_inst.depth = some(gpu.DepthOf(gpu.TextureFormat.Depth32Float, gpu.Compare.Less))
    pd_inst.label = "inst_pipe"
    pipe_inst := try dev.NewRenderPipeline(io, &pl_inst, &sh_inst_vs, some(&sh_inst_ps), pd_inst)
    onerr pipe_inst.Close(mut dev)

    var pd_inst_msaa := pd_inst
    pd_inst_msaa.samples = 4
    pd_inst_msaa.label = "inst_msaa"
    pipe_inst_msaa := try dev.NewRenderPipeline(io, &pl_inst, &sh_inst_vs, some(&sh_inst_ps), pd_inst_msaa)
    onerr pipe_inst_msaa.Close(mut dev)

    // Blended overlay pipeline.
    var no_groups: [0]&gpu.BindGroupLayout = []
    pl_ov := try dev.NewPipelineLayout(no_groups[..], 32, "ov_pl")
    onerr pl_ov.Close(mut dev)
    var ov_t := gpu.ColorOf(sc_fmt)
    ov_t.blend = some(gpu.BlendOver)
    ov_tgts := [ov_t]
    var pd_ov := gpu.DefaultRenderPipeline
    pd_ov.targets = ov_tgts[..]
    var ro_ds := gpu.DepthOf(gpu.TextureFormat.Depth32Float, gpu.Compare.Always)
    ro_ds.write = false
    pd_ov.depth = some(ro_ds)
    pd_ov.label = "ov_pipe"
    pipe_ov := try dev.NewRenderPipeline(io, &pl_ov, &sh_ov_vs, some(&sh_ov_ps), pd_ov)
    onerr pipe_ov.Close(mut dev)

    var pd_ov_msaa := pd_ov
    pd_ov_msaa.samples = 4
    pd_ov_msaa.label = "ov_msaa"
    pipe_ov_msaa := try dev.NewRenderPipeline(io, &pl_ov, &sh_ov_vs, some(&sh_ov_ps), pd_ov_msaa)
    onerr pipe_ov_msaa.Close(mut dev)

    // Compute separable blur pipeline.
    blur_entries := [
        gpu.LayoutEntry(binding: 0, visibility: v_comp, kind: gpu.BindingKind.Storage(read_only: true, min_size: 4, stride: 4)),
        gpu.LayoutEntry(binding: 1, visibility: v_comp, kind: gpu.BindingKind.Storage(read_only: false, min_size: 4, stride: 4)),
    ]
    bgl_blur := try dev.NewBindGroupLayout(blur_entries[..], "blur_bgl")
    onerr bgl_blur.Close(mut dev)
    blur_groups := [&bgl_blur]
    pl_blur := try dev.NewPipelineLayout(blur_groups[..], 16, "blur_pl")
    onerr pl_blur.Close(mut dev)
    pipe_blur := try dev.NewComputePipeline(io, &pl_blur, &sh_blur_cs, "blur_pipe")
    onerr pipe_blur.Close(mut dev)

    // Samplers.
    var sd_lin := gpu.DefaultSamplerDesc
    sd_lin.address_u = gpu.Address.Repeat
    sd_lin.address_v = gpu.Address.Repeat
    samp_linear := try dev.NewSampler(sd_lin)
    onerr samp_linear.Close(mut dev)

    var sd_sh := gpu.DefaultSamplerDesc
    sd_sh.compare = some(gpu.Compare.LessEqual)
    samp_shadow := try dev.NewSampler(sd_sh)
    onerr samp_shadow.Close(mut dev)

    // Uniform and storage buffers.
    var fu := gpu.NoBufferUsage
    fu.uniform = true
    fu.copy_dst = true
    frame_ubo := try dev.NewBuffer(FrameUboSize, fu, "frame_ubo")
    onerr frame_ubo.Close(mut dev)

    var du := gpu.NoBufferUsage
    du.uniform_dynamic = true
    du.copy_dst = true
    draw_ubo := try dev.NewBuffer(MaxDynamicObjects * DynamicStride, du, "draw_ubo")
    onerr draw_ubo.Close(mut dev)

    inst_cap := if sc.instances > 0 { sc.instances } else { 1 }
    var iu := gpu.NoBufferUsage
    iu.storage = true
    iu.copy_dst = true
    inst_buf := try dev.NewBuffer(inst_cap * 32, iu, "instances")
    onerr inst_buf.Close(mut dev)
    try WriteInstances(&ar, mut dev, &inst_buf, sc.instances)

    pitch := math3d.AlignUp(sc.width * 4, 256)
    blur_stride_u32 := pitch / 4
    blur_bytes := pitch * sc.height
    var bu := gpu.NoBufferUsage
    bu.storage = true
    bu.copy_src = true
    bu.copy_dst = true
    blur_buf_a := try dev.NewBuffer(blur_bytes, bu, "blur_a")
    onerr blur_buf_a.Close(mut dev)
    blur_buf_b := try dev.NewBuffer(blur_bytes, bu, "blur_b")
    onerr blur_buf_b.Close(mut dev)

    // Shadow depth texture.
    var std_desc := gpu.DefaultTextureDesc
    std_desc.width = ShadowSize
    std_desc.height = ShadowSize
    std_desc.format = gpu.TextureFormat.Depth32Float
    std_desc.usage.render_target = true
    std_desc.usage.sampled = true
    std_desc.label = "shadow_depth"
    shadow_tex := try dev.NewTexture(std_desc)
    onerr shadow_tex.Close(mut dev)
    var sdv_desc := gpu.DefaultViewDesc
    sdv_desc.aspect = gpu.Aspect.Depth
    shadow_view := try dev.NewView(&shadow_tex, sdv_desc)
    onerr shadow_view.Close(mut dev)

    ts := try CreateTargetSet(mut dev, sc.width, sc.height, sc_fmt)
    onerr ts.Close(mut dev)

    // Upload meshes.
    var gpu_meshes := HeapList[GpuMesh].New(alloc)
    onerr CloseMeshes(alloc, gpu_meshes, mut dev)
    for i in 0..sc.meshes.Len() {
        gm := try UploadMesh(&ar, mut dev, &sc.meshes[i].mesh, sc.meshes[i].name)
        onerr {
            var GpuMesh(vb, ib, index_count: _) := gm
            vb.Close(mut dev)
            ib.Close(mut dev)
        }
        try gpu_meshes.Push(alloc, gm)
    }

    // Upload textures (guarantee at least 1 default texture if none listed).
    var gpu_textures := HeapList[GpuTexture].New(alloc)
    onerr CloseTextures(alloc, gpu_textures, mut dev)
    for i in 0..sc.textures.Len() {
        gt := try UploadTexture(alloc, mut dev, &sc.textures[i].img, sc.textures[i].name)
        onerr {
            var GpuTexture(tex, full_view, mip_views, width: _, height: _, mips: _) := gt
            CloseViews(alloc, mip_views, mut dev)
            full_view.Close(mut dev)
            tex.Close(mut dev)
        }
        try gpu_textures.Push(alloc, gt)
    }
    if gpu_textures.Len() == 0 {
        var white: [16]u8 = [255; 16]
        def_img := image.Image(width: 2, height: 2, pixels: image.Pixels.Rgba8, data: white[..])
        gt := try UploadTexture(alloc, mut dev, &def_img, "default_white")
        onerr {
            var GpuTexture(tex, full_view, mip_views, width: _, height: _, mips: _) := gt
            CloseViews(alloc, mip_views, mut dev)
            full_view.Close(mut dev)
            tex.Close(mut dev)
        }
        try gpu_textures.Push(alloc, gt)
    }

    // Bind groups.
    bg_sh_binds := [
        gpu.BindEntry(binding: 0, bound: gpu.Bound.Buffer(b: &frame_ubo, offset: ShadowUboOffset, size: 64)),
        gpu.BindEntry(binding: 1, bound: gpu.Bound.Buffer(b: &draw_ubo, offset: 0, size: 96)),
    ]
    bg_shadow := try dev.NewBindGroup(&bgl_shadow, bg_sh_binds[..], "bg_shadow")
    onerr bg_shadow.Close(mut dev)

    bg_inst_binds := [
        gpu.BindEntry(binding: 0, bound: gpu.Bound.Buffer(b: &frame_ubo, offset: 0, size: 176)),
        gpu.BindEntry(binding: 1, bound: gpu.Bound.Buffer(b: &inst_buf, offset: 0, size: inst_cap * 32)),
    ]
    bg_inst := try dev.NewBindGroup(&bgl_inst, bg_inst_binds[..], "bg_inst")
    onerr bg_inst.Close(mut dev)

    bg_bh_binds := [
        gpu.BindEntry(binding: 0, bound: gpu.Bound.Buffer(b: &blur_buf_a, offset: 0, size: blur_bytes)),
        gpu.BindEntry(binding: 1, bound: gpu.Bound.Buffer(b: &blur_buf_b, offset: 0, size: blur_bytes)),
    ]
    bg_blur_h := try dev.NewBindGroup(&bgl_blur, bg_bh_binds[..], "bg_blur_h")
    onerr bg_blur_h.Close(mut dev)

    bg_bv_binds := [
        gpu.BindEntry(binding: 0, bound: gpu.Bound.Buffer(b: &blur_buf_b, offset: 0, size: blur_bytes)),
        gpu.BindEntry(binding: 1, bound: gpu.Bound.Buffer(b: &blur_buf_a, offset: 0, size: blur_bytes)),
    ]
    bg_blur_v := try dev.NewBindGroup(&bgl_blur, bg_bv_binds[..], "bg_blur_v")
    onerr bg_blur_v.Close(mut dev)

    var scene_bgs := HeapList[gpu.BindGroup].New(alloc)
    onerr CloseGroups(alloc, scene_bgs, mut dev)
    for i in 0..gpu_textures.Len() {
        gt := &gpu_textures[i]
        sc_binds := [
            gpu.BindEntry(binding: 0, bound: gpu.Bound.Buffer(b: &frame_ubo, offset: 0, size: 176)),
            gpu.BindEntry(binding: 1, bound: gpu.Bound.Buffer(b: &draw_ubo, offset: 0, size: 96)),
            gpu.BindEntry(binding: 2, bound: gpu.Bound.View(v: &gt.full_view)),
            gpu.BindEntry(binding: 3, bound: gpu.Bound.Sampler(s: &samp_linear)),
            gpu.BindEntry(binding: 4, bound: gpu.Bound.View(v: &shadow_view)),
            gpu.BindEntry(binding: 5, bound: gpu.Bound.Sampler(s: &samp_shadow)),
        ]
        bg := try dev.NewBindGroup(&bgl_scene, sc_binds[..], "bg_scene")
        onerr bg.Close(mut dev)
        try scene_bgs.Push(alloc, bg)
    }

    var draw_items := HeapList[DrawItem].New(alloc)
    onerr draw_items.Free(alloc)
    var objects_data := HeapList[ObjectXform].New(alloc)
    onerr objects_data.Free(alloc)

    obj_count := if sc.objects.Len() > MaxDynamicObjects { MaxDynamicObjects } else { sc.objects.Len() }
    for i in 0..obj_count {
        od := sc.objects[i]
        try draw_items.Push(alloc, DrawItem(
            mesh_idx: od.mesh_idx,
            tex_idx: od.tex_idx,
            dyn_offset: Trunc[u32](i * DynamicStride),
            alpha: od.color.w,
        ))
        try objects_data.Push(alloc, ObjectXform(
            tx: od.tx,
            ty: od.ty,
            tz: od.tz,
            rot_y: od.rot_y,
            scale: od.scale,
            color: od.color,
        ))
    }

    var TargetSet(
        color_tex,
        color_view,
        msaa_tex,
        msaa_view,
        depth_tex,
        depth_view,
        msaa_depth_tex,
        msaa_depth_view,
    ) := ts

    return SceneRenderer(
        heap: heap,
        dev: dev,
        sc: swap,
        sh_shadow_vs: sh_shadow_vs,
        sh_mip_vs: sh_mip_vs,
        sh_mip_ps: sh_mip_ps,
        sh_scene_vs: sh_scene_vs,
        sh_scene_ps: sh_scene_ps,
        sh_inst_vs: sh_inst_vs,
        sh_inst_ps: sh_inst_ps,
        sh_ov_vs: sh_ov_vs,
        sh_ov_ps: sh_ov_ps,
        sh_blur_cs: sh_blur_cs,
        bgl_shadow: bgl_shadow,
        pl_shadow: pl_shadow,
        pipe_shadow: pipe_shadow,
        bgl_mip: bgl_mip,
        pl_mip: pl_mip,
        pipe_mip: pipe_mip,
        bgl_scene: bgl_scene,
        pl_scene: pl_scene,
        pipe_scene: pipe_scene,
        pipe_scene_msaa: pipe_scene_msaa,
        bgl_inst: bgl_inst,
        pl_inst: pl_inst,
        pipe_inst: pipe_inst,
        pipe_inst_msaa: pipe_inst_msaa,
        pl_ov: pl_ov,
        pipe_ov: pipe_ov,
        pipe_ov_msaa: pipe_ov_msaa,
        bgl_blur: bgl_blur,
        pl_blur: pl_blur,
        pipe_blur: pipe_blur,
        samp_linear: samp_linear,
        samp_shadow: samp_shadow,
        frame_ubo: frame_ubo,
        draw_ubo: draw_ubo,
        inst_buf: inst_buf,
        blur_buf_a: blur_buf_a,
        blur_buf_b: blur_buf_b,
        shadow_tex: shadow_tex,
        shadow_view: shadow_view,
        color_tex: color_tex,
        color_view: color_view,
        msaa_tex: msaa_tex,
        msaa_view: msaa_view,
        depth_tex: depth_tex,
        depth_view: depth_view,
        msaa_depth_tex: msaa_depth_tex,
        msaa_depth_view: msaa_depth_view,
        bg_shadow: bg_shadow,
        bg_inst: bg_inst,
        bg_blur_h: bg_blur_h,
        bg_blur_v: bg_blur_v,
        scene_bgs: scene_bgs,
        meshes: gpu_meshes,
        textures: gpu_textures,
        draw_items: draw_items,
        objects_data: objects_data,
        width: sc.width,
        height: sc.height,
        blur_stride_u32: blur_stride_u32,
        msaa: sc.msaa,
        shadows: sc.shadows,
        blur: sc.blur,
        overlay: sc.overlay,
        instances: sc.instances,
        clear_r: sc.clear_r,
        clear_g: sc.clear_g,
        clear_b: sc.clear_b,
        clear_a: sc.clear_a,
        camera: sc.camera,
        light: sc.light,
        yaw_deg: 0.0,
        pitch_deg: 0.0,
        zoom: 1.0,
        sc_format: sc_fmt,
    )
}

/// Builds a SceneRenderer for sc (with win = none for offscreen, or some(&win)
/// for swapchain presentation) and fills each texture's mip chain.
/// Errors: gpu.Error, OutOfMemory. O(meshes + textures + instances); allocates
/// device objects and the renderer's lists on heap.
export NewRenderer(
    heap: &Heap,
    io: Io,
    sc: &scenedoc.Scene,
    opts: RenderOptions,
    win: ?&window.Window,
): gpu.Error|OutOfMemory!SceneRenderer from heap {
    var r := try BuildRenderer(heap, io, sc, opts, win)
    onerr {
        _ = r.Close(io)
    }
    try r.GenerateMips()
    return r
}
