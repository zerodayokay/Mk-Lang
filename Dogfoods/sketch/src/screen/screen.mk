// Why: the canvas is painted on the CPU, so the GPU's job is to show it in
// the window through a swapchain and to prove, by reading back an offscreen
// copy, that what it shows is the canvas byte for byte.
//
// What: Shown, Viewer, ViewOptions, NewViewer, Viewer.Upload, Viewer.Draw,
// Viewer.Resize, Viewer.Readback and Viewer.Close.
//
// How: one RGBA8 canvas texture, written whole when the picture changes,
// drawn by a full-target triangle with a nearest sampler into the swapchain
// image (stretched) or into an offscreen texture of the canvas's size (texel
// for texel, so the readback equals the canvas exactly).

import std.gpu
import std.window
import src.shaders

/// What Draw did with a frame.
export enum Shown {
    Presented
    /// The window is minimised; wait for an event.
    Hidden
    /// The surface changed; Resize, then draw again.
    Outdated
}

/// The device objects that show a canvas in a window.
export linear view struct Viewer {
    dev: gpu.Device
    sc: gpu.Swapchain
    vs: gpu.Shader
    ps: gpu.Shader
    bgl: gpu.BindGroupLayout
    pl: gpu.PipelineLayout
    pipe: gpu.RenderPipeline
    pipe_off: gpu.RenderPipeline
    samp: gpu.Sampler
    tex: gpu.Texture
    tex_view: gpu.TextureView
    bg: gpu.BindGroup
    off: gpu.Texture
    off_view: gpu.TextureView
    width: int
    height: int
}

/// The device and swapchain options NewViewer applies.
export struct ViewOptions {
    export backend: gpu.Backend
    export software: bool
    export debug: gpu.DebugMode
    export mode: gpu.PresentMode
}

Tex(dev: mut gpu.Device, w: int, h: int, sampled: bool, label: str): gpu.Error!gpu.Texture {
    var d := gpu.DefaultTextureDesc
    d.width = w
    d.height = h
    d.format = gpu.TextureFormat.Rgba8Unorm
    d.usage.sampled = sampled
    d.usage.copy_dst = sampled
    d.usage.render_target = !sampled
    d.usage.copy_src = !sampled
    d.label = label
    return try dev.NewTexture(d)
}

/// Opens a device, a swapchain on win in Bgra8Unorm (no sRGB conversion,
/// so the canvas's bytes reach the screen), and the objects that draw a
/// width by height canvas. Errors: gpu.Error. O(1) device calls; allocates
/// the device on heap.
export NewViewer(heap: &Heap, io: Io, win: &window.Window, width: int, height: int, o: ViewOptions): gpu.Error!Viewer from heap {
    var dopt := gpu.DefaultDeviceOptions
    dopt.backend = o.backend
    dopt.software = o.software
    dopt.debug = o.debug
    var dev := try gpu.Open(heap, io, dopt)
    onerr dev.Abandon(io)
    var so := gpu.DefaultSwapchainOptions
    so.format = gpu.TextureFormat.Bgra8Unorm
    so.mode = o.mode
    sc := try dev.NewSwapchain(io, win, so)
    onerr sc.Close(io, mut dev)
    vs := try dev.NewShader(shaders.CodeOf(shaders.BlitVsSpv, shaders.BlitVsDxil), gpu.Stage.Vertex, "VsMain", "blit_vs")
    onerr vs.Close(mut dev)
    ps := try dev.NewShader(shaders.CodeOf(shaders.BlitPsSpv, shaders.BlitPsDxil), gpu.Stage.Fragment, "PsMain", "blit_ps")
    onerr ps.Close(mut dev)
    frag := gpu.Visibility(vertex: false, fragment: true, compute: false)
    entries := [
        gpu.LayoutEntry(binding: 0, visibility: frag, kind: gpu.BindingKind.Texture(sample: gpu.SampleType.Float, dimension: gpu.ViewDimension.D2, multisampled: false)),
        gpu.LayoutEntry(binding: 1, visibility: frag, kind: gpu.BindingKind.Sampler(comparison: false)),
    ]
    bgl := try dev.NewBindGroupLayout(entries[..], "canvas_bgl")
    onerr bgl.Close(mut dev)
    groups := [&bgl]
    pl := try dev.NewPipelineLayout(groups[..], 0, "canvas_pl")
    onerr pl.Close(mut dev)
    on_screen := [gpu.ColorOf(gpu.TextureFormat.Bgra8Unorm)]
    var pd := gpu.DefaultRenderPipeline
    pd.targets = on_screen[..]
    pd.label = "blit"
    pipe := try dev.NewRenderPipeline(io, &pl, &vs, some(&ps), pd)
    onerr pipe.Close(mut dev)
    off_screen := [gpu.ColorOf(gpu.TextureFormat.Rgba8Unorm)]
    pd.targets = off_screen[..]
    pd.label = "blit_offscreen"
    pipe_off := try dev.NewRenderPipeline(io, &pl, &vs, some(&ps), pd)
    onerr pipe_off.Close(mut dev)
    var sd := gpu.DefaultSamplerDesc
    sd.min = gpu.Filter.Nearest
    sd.mag = gpu.Filter.Nearest
    sd.mip = gpu.Filter.Nearest
    samp := try dev.NewSampler(sd)
    onerr samp.Close(mut dev)
    tex := try Tex(mut dev, width, height, true, "canvas")
    onerr tex.Close(mut dev)
    tex_view := try dev.NewView(&tex, gpu.DefaultViewDesc)
    onerr tex_view.Close(mut dev)
    binds := [
        gpu.BindEntry(binding: 0, bound: gpu.Bound.View(v: &tex_view)),
        gpu.BindEntry(binding: 1, bound: gpu.Bound.Sampler(s: &samp)),
    ]
    bg := try dev.NewBindGroup(&bgl, binds[..], "canvas_bg")
    onerr bg.Close(mut dev)
    off := try Tex(mut dev, width, height, false, "readback")
    onerr off.Close(mut dev)
    off_view := try dev.NewView(&off, gpu.DefaultViewDesc)
    return Viewer(
        dev: dev,
        sc: sc,
        vs: vs,
        ps: ps,
        bgl: bgl,
        pl: pl,
        pipe: pipe,
        pipe_off: pipe_off,
        samp: samp,
        tex: tex,
        tex_view: tex_view,
        bg: bg,
        off: off,
        off_view: off_view,
        width: width,
        height: height,
    )
}

/// Writes the whole canvas, width * height * 4 bytes of RGBA8. Errors:
/// gpu.Error. O(pixels); copies through the device's upload ring.
export Viewer.Upload(mut self, px: []u8): gpu.Error!void {
    region := gpu.TextureRegion(texture: &self.tex, mip: 0, layer: 0, x: 0, y: 0, aspect: gpu.Aspect.All)
    try self.dev.WriteTexture(region, px, gpu.Extent(width: self.width, height: self.height, layers: 1))
}

// Blit records one pass drawing the canvas into target.
Blit(enc: mut gpu.Encoder, target: &gpu.TextureView, pipe: &gpu.RenderPipeline, bg: &gpu.BindGroup) {
    cols := [gpu.ColorAttachment(target: target, resolve: none, load: gpu.Load.DontCare, store: gpu.Store.Store)]
    pd := gpu.PassDesc(colors: cols[..], depth: none, label: "canvas")
    var p := enc.RenderPass(&pd)
    p.SetPipeline(pipe)
    var no_dyn: [0]u32 = []
    p.SetBindGroup(0, bg, no_dyn[..])
    p.Draw(3, 1, 0, 0)
    p.End()
}

/// Draws the canvas into the next swapchain image and presents it.
/// Errors: gpu.Error. O(1) device calls; allocates nothing.
export Viewer.Draw(mut self, io: Io): gpu.Error!Shown {
    acq := try self.dev.Acquire(io, mut self.sc)
    match acq {
        .Ready(frame) {
            onerr frame.Abandon()
            var enc := try self.dev.Encoder("frame")
            onerr enc.Abandon()
            enc.PushDebugGroup("present")
            Blit(mut enc, frame.View(), &self.pipe, &self.bg)
            enc.PopDebugGroup()
            try enc.Submit()
            _ = try self.dev.Present(io, frame)
            return Shown.Presented
        }
        .Outdated { return Shown.Outdated }
        .Hidden { return Shown.Hidden }
    }
}

/// Resizes the swapchain to width by height after a Resized event or an
/// Outdated frame. Errors: gpu.Error. O(1) device calls.
export Viewer.Resize(mut self, io: Io, width: int, height: int): gpu.Error!void {
    try self.dev.Resize(io, mut self.sc, width, height)
}

/// Draws the canvas offscreen at its own size and reads it back as RGBA8,
/// which equals the uploaded canvas. Errors: gpu.Error, OutOfMemory.
/// O(pixels); allocates the pixels in a.
export Viewer.Readback(mut self, a: &Arena, io: Io): gpu.Error![]u8 from a {
    var enc := try self.dev.Encoder("readback")
    onerr enc.Abandon()
    Blit(mut enc, &self.off_view, &self.pipe_off, &self.bg)
    try enc.Submit()
    region := gpu.TextureRegion(texture: &self.off, mip: 0, layer: 0, x: 0, y: 0, aspect: gpu.Aspect.All)
    return try self.dev.ReadTexture(a, io, region, gpu.Extent(width: self.width, height: self.height, layers: 1))
}

/// Releases every object, closes the device and returns its leaks.
/// O(objects).
export Viewer.Close(self, io: Io): gpu.Leaks {
    var Viewer(dev, sc, vs, ps, bgl, pl, pipe, pipe_off, samp, tex, tex_view, bg, off, off_view, width: _, height: _) := self
    off_view.Close(mut dev)
    off.Close(mut dev)
    bg.Close(mut dev)
    tex_view.Close(mut dev)
    tex.Close(mut dev)
    samp.Close(mut dev)
    pipe_off.Close(mut dev)
    pipe.Close(mut dev)
    pl.Close(mut dev)
    bgl.Close(mut dev)
    ps.Close(mut dev)
    vs.Close(mut dev)
    sc.Close(io, mut dev)
    return dev.Close(io)
}
