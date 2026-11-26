// Why: frame rendering, mip generation, resize, interactive orbit/toggles,
// and clean resource teardown live separately from renderer initialization to
// keep each file focused and readable.
//
// What: GenerateMips, RenderOffscreen, ResizeTargets, DrawSwapchainFrame,
// Orbit, ToggleMsaa, TakeMessages, Backend and Close.
//
// How: passes disjoint struct fields (self.dev, self.sc, etc.) to std.gpu,
// builds temporary mip bind groups before opening the mip Encoder, and closes
// every handle in reverse dependency order.

import std.image
import std.gpu
import src.math3d

/// Generates mip levels 1..mips-1 for every loaded texture via render passes.
/// Errors: gpu.Error, OutOfMemory. O(total mips); allocates one bind group per
/// mip level, released before it returns.
export SceneRenderer.GenerateMips(mut self): gpu.Error|OutOfMemory!void {
    alloc := self.heap.Alloc()
    var bgs := HeapList[gpu.BindGroup].New(alloc)
    defer CloseGroups(alloc, bgs, mut self.dev)

    for i in 0..self.textures.Len() {
        gt := &self.textures[i]
        for m in 1..gt.mips {
            binds := [
                gpu.BindEntry(binding: 0, bound: gpu.Bound.View(v: &gt.mip_views[m - 1])),
                gpu.BindEntry(binding: 1, bound: gpu.Bound.Sampler(s: &self.samp_linear)),
            ]
            bg := try self.dev.NewBindGroup(&self.bgl_mip, binds[..], "mip_bg")
            onerr bg.Close(mut self.dev)
            try bgs.Push(alloc, bg)
        }
    }
    if bgs.Len() == 0 {
        return
    }
    var enc := try self.dev.Encoder("generate_mips")
    onerr enc.Abandon()
    enc.PushDebugGroup("mip_chain")
    var bg_idx := 0
    for i in 0..self.textures.Len() {
        gt := &self.textures[i]
        var w := gt.width
        var h := gt.height
        for m in 1..gt.mips {
            colors := [gpu.ColorAttachment(target: &gt.mip_views[m], resolve: none, load: gpu.Load.DontCare, store: gpu.Store.Store)]
            pdesc := gpu.PassDesc(colors: colors[..], depth: none, label: "mip_pass")
            var pass := enc.RenderPass(&pdesc)
            pass.SetPipeline(&self.pipe_mip)
            var no_dyn: [0]u32 = []
            pass.SetBindGroup(0, &bgs[bg_idx], no_dyn[..])
            var push: [16]u8 = [0; 16]
            math3d.PutVec4(mut push[..], 0, math3d.V4(1.0 / Cast[f32](w), 1.0 / Cast[f32](h), 0.0, 0.0))
            pass.SetImmediates(0, push[..])
            pass.Draw(3, 1, 0, 0)
            pass.End()
            bg_idx += 1
            w = if w > 1 { w / 2 } else { 1 }
            h = if h > 1 { h / 2 } else { 1 }
        }
    }
    enc.PopDebugGroup()
    try enc.Submit()
}

/// Updates camera/light frame uniform and per-object dynamic uniform buffer.
/// Errors: gpu.Error. O(objects); allocates nothing.
SceneRenderer.UpdateUniforms(mut self, time_deg: f32): gpu.Error!void {
    aspect := Cast[f32](self.width) / Cast[f32](if self.height > 0 { self.height } else { 1 })
    off := math3d.Sub3(self.camera.eye, self.camera.target)
    r_yaw := math3d.RotateY(self.yaw_deg)
    r_pitch := math3d.RotateX(self.pitch_deg)
    // Apply orbit rotation + zoom to camera offset.
    rot := math3d.MulMat4(r_yaw, r_pitch)
    rx := rot.m[0] * off.x + rot.m[1] * off.y + rot.m[2] * off.z
    ry := rot.m[4] * off.x + rot.m[5] * off.y + rot.m[6] * off.z
    rz := rot.m[8] * off.x + rot.m[9] * off.y + rot.m[10] * off.z
    eye := math3d.Add3(self.camera.target, math3d.Scale3(math3d.V3(rx, ry, rz), self.zoom))

    view_mat := math3d.LookAt(eye, self.camera.target, math3d.V3(0.0, 1.0, 0.0))
    proj := math3d.Perspective(self.camera.fov, aspect, 0.1, 100.0)
    vp := math3d.MulMat4(proj, view_mat)

    ldir := math3d.Normalize3(self.light.dir)
    l_eye := math3d.Scale3(ldir, -8.0)
    l_view := math3d.LookAt(l_eye, math3d.V3(0.0, 0.0, 0.0), math3d.V3(0.0, 1.0, 0.0))
    l_proj := math3d.Ortho(-6.0, 6.0, -6.0, 6.0, 0.5, 24.0)
    lvp := math3d.MulMat4(l_proj, l_view)

    var fbuf: [176]u8 = [0; 176]
    math3d.PutMat4(mut fbuf[..], 0, vp)
    math3d.PutMat4(mut fbuf[..], 64, lvp)
    math3d.PutVec4(mut fbuf[..], 128, math3d.V4(ldir.x, ldir.y, ldir.z, 0.0))
    math3d.PutVec4(mut fbuf[..], 144, math3d.V4(self.light.color.x, self.light.color.y, self.light.color.z, 1.0))
    math3d.PutVec4(mut fbuf[..], 160, math3d.V4(self.light.ambient.x, self.light.ambient.y, self.light.ambient.z, 1.0))
    try self.dev.WriteBuffer(&self.frame_ubo, 0, fbuf[..])
    try self.dev.WriteBuffer(&self.frame_ubo, ShadowUboOffset, fbuf[64..128])

    for i in 0..self.objects_data.Len() {
        od := self.objects_data[i]
        m := math3d.ModelMatrix(od.tx, od.ty, od.tz, od.rot_y + time_deg, od.scale)
        var dbuf: [96]u8 = [0; 96]
        math3d.PutMat4(mut dbuf[..], 0, m)
        math3d.PutVec4(mut dbuf[..], 64, od.color)
        math3d.PutVec4(mut dbuf[..], 80, math3d.V4(1.0, 0.0, 0.0, 0.0))
        try self.dev.WriteBuffer(&self.draw_ubo, i * DynamicStride, dbuf[..])
    }
}

/// Records shadow pass, main render pass (1x or 4x MSAA), instanced pass,
/// blended overlay, and optional compute blur into enc.
RecordFramePasses(
    enc: mut gpu.Encoder,
    color_target: &gpu.TextureView,
    color_copy_tex: &gpu.Texture,
    msaa_view: &gpu.TextureView,
    depth_view: &gpu.TextureView,
    msaa_depth_view: &gpu.TextureView,
    shadow_view: &gpu.TextureView,
    pipe_shadow: &gpu.RenderPipeline,
    pipe_scene: &gpu.RenderPipeline,
    pipe_scene_msaa: &gpu.RenderPipeline,
    pipe_inst: &gpu.RenderPipeline,
    pipe_inst_msaa: &gpu.RenderPipeline,
    pipe_ov: &gpu.RenderPipeline,
    pipe_ov_msaa: &gpu.RenderPipeline,
    pipe_blur: &gpu.ComputePipeline,
    bg_shadow: &gpu.BindGroup,
    bg_inst: &gpu.BindGroup,
    bg_blur_h: &gpu.BindGroup,
    bg_blur_v: &gpu.BindGroup,
    scene_bgs: &HeapList[gpu.BindGroup],
    meshes: &HeapList[GpuMesh],
    draw_items: &HeapList[DrawItem],
    blur_buf_a: &gpu.Buffer,
    width: int,
    height: int,
    blur_stride_u32: int,
    msaa: int,
    shadows: bool,
    blur: bool,
    overlay: bool,
    instances: int,
    clear_r: f64,
    clear_g: f64,
    clear_b: f64,
    clear_a: f64,
) {
    enc.PushDebugGroup("scene_frame")
    var no_dyn: [0]u32 = []

    if shadows && draw_items.Len() > 0 {
        var no_cols: [0]gpu.ColorAttachment = []
        s_da := gpu.DepthAttachment(
            target: shadow_view,
            depth_load: gpu.LoadDepth.Clear(depth: 1.0),
            depth_store: gpu.Store.Store,
            stencil_load: gpu.LoadStencil.DontCare,
            stencil_store: gpu.Store.Discard,
            read_only: false,
        )
        s_desc := gpu.PassDesc(colors: no_cols[..], depth: some(s_da), label: "shadow")
        var sp := enc.RenderPass(&s_desc)
        sp.PushDebugGroup("shadow_pass")
        sp.SetPipeline(pipe_shadow)
        for i in 0..draw_items.Len() {
            di := draw_items[i]
            gm := &meshes[di.mesh_idx]
            dyn := [di.dyn_offset]
            sp.SetBindGroup(0, bg_shadow, dyn[..])
            sp.SetVertexBuffer(0, &gm.vb, 0)
            sp.SetIndexBuffer(&gm.ib, gpu.IndexFormat.Uint32, 0)
            sp.DrawIndexed(gm.index_count, 1, 0, 0, 0)
        }
        sp.PopDebugGroup()
        sp.End()
    }

    use_msaa := msaa == 4
    ca := if use_msaa {
        gpu.ColorAttachment(
            target: msaa_view,
            resolve: some(color_target),
            load: gpu.Load.Clear(r: clear_r, g: clear_g, b: clear_b, a: clear_a),
            store: gpu.Store.Discard,
        )
    } else {
        gpu.ColorAttachment(
            target: color_target,
            resolve: none,
            load: gpu.Load.Clear(r: clear_r, g: clear_g, b: clear_b, a: clear_a),
            store: gpu.Store.Store,
        )
    }
    cols := [ca]
    dv := if use_msaa { msaa_depth_view } else { depth_view }
    da := gpu.DepthAttachment(
        target: dv,
        depth_load: gpu.LoadDepth.Clear(depth: 1.0),
        depth_store: gpu.Store.Discard,
        stencil_load: gpu.LoadStencil.DontCare,
        stencil_store: gpu.Store.Discard,
        read_only: false,
    )
    m_desc := gpu.PassDesc(colors: cols[..], depth: some(da), label: "main")
    var mp := enc.RenderPass(&m_desc)
    mp.PushDebugGroup("main_pass")

    if draw_items.Len() > 0 {
        mp.SetPipeline(if use_msaa { pipe_scene_msaa } else { pipe_scene })
        var push: [16]u8 = [0; 16]
        sh_flag: f32 = if shadows { 1.0 } else { 0.0 }
        math3d.PutVec4(mut push[..], 0, math3d.V4(sh_flag, 0.005, 1.0, 0.0))
        mp.SetImmediates(0, push[..])
        for i in 0..draw_items.Len() {
            di := draw_items[i]
            gm := &meshes[di.mesh_idx]
            dyn := [di.dyn_offset]
            mp.SetBindGroup(0, &scene_bgs[di.tex_idx], dyn[..])
            mp.SetVertexBuffer(0, &gm.vb, 0)
            sp_off := 0
            mp.SetIndexBuffer(&gm.ib, gpu.IndexFormat.Uint32, sp_off)
            mp.DrawIndexed(gm.index_count, 1, 0, 0, 0)
        }
    }

    if instances > 0 && meshes.Len() > 0 {
        mp.PushDebugGroup("instanced")
        mp.SetPipeline(if use_msaa { pipe_inst_msaa } else { pipe_inst })
        var ipush: [16]u8 = [0; 16]
        math3d.PutVec4(mut ipush[..], 0, math3d.V4(1.0, 1.0, 1.0, 1.0))
        mp.SetImmediates(0, ipush[..])
        mp.SetBindGroup(0, bg_inst, no_dyn[..])
        gm0 := &meshes[0]
        mp.SetVertexBuffer(0, &gm0.vb, 0)
        mp.SetIndexBuffer(&gm0.ib, gpu.IndexFormat.Uint32, 0)
        mp.DrawIndexed(gm0.index_count, instances, 0, 0, 0)
        mp.PopDebugGroup()
    }

    if overlay {
        mp.PushDebugGroup("overlay")
        mp.SetPipeline(if use_msaa { pipe_ov_msaa } else { pipe_ov })
        var opush: [32]u8 = [0; 32]
        math3d.PutVec4(mut opush[..], 0, math3d.V4(-0.92, 0.72, -0.42, 0.92))
        math3d.PutVec4(mut opush[..], 16, math3d.V4(0.15, 0.55, 0.9, 0.55))
        mp.SetImmediates(0, opush[..])
        mp.Draw(6, 1, 0, 0)
        mp.PopDebugGroup()
    }

    mp.PopDebugGroup()
    mp.End()

    if blur {
        enc.PushDebugGroup("post_blur")
        pitch := blur_stride_u32 * 4
        layout := gpu.DataLayout(offset: 0, bytes_per_row: pitch, rows_per_layer: height)
        reg := gpu.TextureRegion(texture: color_copy_tex, mip: 0, layer: 0, x: 0, y: 0, aspect: gpu.Aspect.All)
        ext := gpu.Extent(width: width, height: height, layers: 1)
        enc.CopyTextureToBuffer(reg, blur_buf_a, layout, ext)

        gx := (width + BlurTileSize - 1) / BlurTileSize
        gy := (height + BlurTileSize - 1) / BlurTileSize

        var cp := enc.ComputePass("blur")
        cp.PushDebugGroup("blur_h")
        cp.SetPipeline(pipe_blur)
        cp.SetBindGroup(0, bg_blur_h, no_dyn[..])
        var bpush_h: [16]u8 = [0; 16]
        math3d.PutU32(mut bpush_h[..], 0, Trunc[u32](width))
        math3d.PutU32(mut bpush_h[..], 4, Trunc[u32](height))
        math3d.PutU32(mut bpush_h[..], 8, Trunc[u32](blur_stride_u32))
        math3d.PutU32(mut bpush_h[..], 12, 0)
        cp.SetImmediates(0, bpush_h[..])
        cp.Dispatch(gx, gy, 1)
        cp.PopDebugGroup()

        cp.PushDebugGroup("blur_v")
        cp.SetBindGroup(0, bg_blur_v, no_dyn[..])
        var bpush_v: [16]u8 = [0; 16]
        math3d.PutU32(mut bpush_v[..], 0, Trunc[u32](width))
        math3d.PutU32(mut bpush_v[..], 4, Trunc[u32](height))
        math3d.PutU32(mut bpush_v[..], 8, Trunc[u32](blur_stride_u32))
        math3d.PutU32(mut bpush_v[..], 12, 1)
        cp.SetImmediates(0, bpush_v[..])
        cp.Dispatch(gx, gy, 1)
        cp.PopDebugGroup()
        cp.End()

        enc.CopyBufferToTexture(blur_buf_a, layout, reg, ext)
        enc.PopDebugGroup()
    }

    enc.PopDebugGroup()
}

/// Renders the scene offscreen and reads back an RGBA8 image into a.
/// Errors: gpu.Error, OutOfMemory. O(objects + instances + pixels); allocates
/// the image's pixels in a.
export SceneRenderer.RenderOffscreen(mut self, a: &Arena, io: Io, time_deg: f32): gpu.Error|OutOfMemory!image.Image from a {
    try self.UpdateUniforms(time_deg)
    var enc := try self.dev.Encoder("offscreen")
    onerr enc.Abandon()
    RecordFramePasses(
        mut enc,
        &self.color_view,
        &self.color_tex,
        &self.msaa_view,
        &self.depth_view,
        &self.msaa_depth_view,
        &self.shadow_view,
        &self.pipe_shadow,
        &self.pipe_scene,
        &self.pipe_scene_msaa,
        &self.pipe_inst,
        &self.pipe_inst_msaa,
        &self.pipe_ov,
        &self.pipe_ov_msaa,
        &self.pipe_blur,
        &self.bg_shadow,
        &self.bg_inst,
        &self.bg_blur_h,
        &self.bg_blur_v,
        &self.scene_bgs,
        &self.meshes,
        &self.draw_items,
        &self.blur_buf_a,
        self.width,
        self.height,
        self.blur_stride_u32,
        self.msaa,
        self.shadows,
        self.blur,
        self.overlay,
        self.instances,
        self.clear_r,
        self.clear_g,
        self.clear_b,
        self.clear_a,
    )
    try enc.Submit()

    reg := gpu.TextureRegion(texture: &self.color_tex, mip: 0, layer: 0, x: 0, y: 0, aspect: gpu.Aspect.All)
    px := try self.dev.ReadTexture(a, io, reg, gpu.Extent(width: self.width, height: self.height, layers: 1))
    return image.Image(width: self.width, height: self.height, pixels: image.Pixels.Rgba8, data: px)
}

/// Resizes the swapchain and the colour, MSAA, depth and blur targets to w by h;
/// a zero size (a minimised window) changes nothing.
/// Errors: gpu.Error. O(1) device calls; allocates the new targets on the device.
export SceneRenderer.ResizeTargets(mut self, io: Io, w: int, h: int): gpu.Error!void {
    if w <= 0 || h <= 0 {
        return
    }
    if swap_in := Replace(mut self.sc, none) {
        var swap := swap_in
        onerr {
            if prev := Replace(mut self.sc, some(swap)) {
                prev.Close(io, mut self.dev)
            }
        }
        try self.dev.Resize(io, mut swap, w, h)
        if prev := Replace(mut self.sc, some(swap)) {
            prev.Close(io, mut self.dev)
        }
    }
    ts := try CreateTargetSet(mut self.dev, w, h, self.sc_format)
    onerr ts.Close(mut self.dev)
    pitch := math3d.AlignUp(w * 4, 256)
    blur_stride_u32 := pitch / 4
    blur_bytes := pitch * h
    var bu := gpu.NoBufferUsage
    bu.storage = true
    bu.copy_src = true
    bu.copy_dst = true
    na := try self.dev.NewBuffer(blur_bytes, bu, "blur_a")
    onerr na.Close(mut self.dev)
    nb := try self.dev.NewBuffer(blur_bytes, bu, "blur_b")
    onerr nb.Close(mut self.dev)

    bg_bh_binds := [
        gpu.BindEntry(binding: 0, bound: gpu.Bound.Buffer(b: &na, offset: 0, size: blur_bytes)),
        gpu.BindEntry(binding: 1, bound: gpu.Bound.Buffer(b: &nb, offset: 0, size: blur_bytes)),
    ]
    nh := try self.dev.NewBindGroup(&self.bgl_blur, bg_bh_binds[..], "bg_blur_h")
    onerr nh.Close(mut self.dev)
    bg_bv_binds := [
        gpu.BindEntry(binding: 0, bound: gpu.Bound.Buffer(b: &nb, offset: 0, size: blur_bytes)),
        gpu.BindEntry(binding: 1, bound: gpu.Bound.Buffer(b: &na, offset: 0, size: blur_bytes)),
    ]
    nv := try self.dev.NewBindGroup(&self.bgl_blur, bg_bv_binds[..], "bg_blur_v")

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
    Replace(mut self.msaa_depth_view, msaa_depth_view).Close(mut self.dev)
    Replace(mut self.msaa_depth_tex, msaa_depth_tex).Close(mut self.dev)
    Replace(mut self.depth_view, depth_view).Close(mut self.dev)
    Replace(mut self.depth_tex, depth_tex).Close(mut self.dev)
    Replace(mut self.msaa_view, msaa_view).Close(mut self.dev)
    Replace(mut self.msaa_tex, msaa_tex).Close(mut self.dev)
    Replace(mut self.color_view, color_view).Close(mut self.dev)
    Replace(mut self.color_tex, color_tex).Close(mut self.dev)
    Replace(mut self.bg_blur_h, nh).Close(mut self.dev)
    Replace(mut self.bg_blur_v, nv).Close(mut self.dev)
    Replace(mut self.blur_buf_a, na).Close(mut self.dev)
    Replace(mut self.blur_buf_b, nb).Close(mut self.dev)

    self.width = w
    self.height = h
    self.blur_stride_u32 = blur_stride_u32
}

/// Renders one frame into the swapchain and presents it; returns false when the
/// window is hidden, so the caller waits for an event. The blur runs offscreen
/// only.
/// Errors: gpu.Error. O(objects + instances); allocates nothing per frame.
export SceneRenderer.DrawSwapchainFrame(mut self, io: Io, w: int, h: int, time_deg: f32): gpu.Error!bool {
    try self.UpdateUniforms(time_deg)
    match Replace(mut self.sc, none) {
        some(swap_in) {
            var swap := swap_in
            acq := self.dev.Acquire(io, mut swap) else e {
                if prev := Replace(mut self.sc, some(swap)) {
                    prev.Close(io, mut self.dev)
                }
                fail e
            }
            match acq {
                .Ready(frame) {
                    onerr {
                        if prev := Replace(mut self.sc, some(swap)) {
                            prev.Close(io, mut self.dev)
                        }
                    }
                    onerr frame.Abandon()
                    var enc := try self.dev.Encoder("swapchain_frame")
                    onerr enc.Abandon()
                    RecordFramePasses(
                        mut enc,
                        frame.View(),
                        &self.color_tex,
                        &self.msaa_view,
                        &self.depth_view,
                        &self.msaa_depth_view,
                        &self.shadow_view,
                        &self.pipe_shadow,
                        &self.pipe_scene,
                        &self.pipe_scene_msaa,
                        &self.pipe_inst,
                        &self.pipe_inst_msaa,
                        &self.pipe_ov,
                        &self.pipe_ov_msaa,
                        &self.pipe_blur,
                        &self.bg_shadow,
                        &self.bg_inst,
                        &self.bg_blur_h,
                        &self.bg_blur_v,
                        &self.scene_bgs,
                        &self.meshes,
                        &self.draw_items,
                        &self.blur_buf_a,
                        self.width,
                        self.height,
                        self.blur_stride_u32,
                        self.msaa,
                        self.shadows,
                        false,
                        self.overlay,
                        self.instances,
                        self.clear_r,
                        self.clear_g,
                        self.clear_b,
                        self.clear_a,
                    )
                    try enc.Submit()
                    _ = try self.dev.Present(io, frame)
                    if prev := Replace(mut self.sc, some(swap)) {
                        prev.Close(io, mut self.dev)
                    }
                    return true
                }
                .Outdated {
                    if prev := Replace(mut self.sc, some(swap)) {
                        prev.Close(io, mut self.dev)
                    }
                    try self.ResizeTargets(io, w, h)
                    return true
                }
                .Hidden {
                    if prev := Replace(mut self.sc, some(swap)) {
                        prev.Close(io, mut self.dev)
                    }
                    return false
                }
            }
        }
        none { return false }
    }
}

/// Adjusts orbit camera yaw, pitch, and zoom. O(1); allocates nothing.
export SceneRenderer.Orbit(mut self, dyaw: f32, dpitch: f32, dzoom: f32) {
    self.yaw_deg += dyaw
    self.pitch_deg += dpitch
    if self.pitch_deg > 80.0 { self.pitch_deg = 80.0 }
    if self.pitch_deg < -80.0 { self.pitch_deg = -80.0 }
    self.zoom += dzoom
    if self.zoom < 0.25 { self.zoom = 0.25 }
    if self.zoom > 5.0 { self.zoom = 5.0 }
}

/// Toggles 4x MSAA on or off. O(1); allocates nothing.
export SceneRenderer.ToggleMsaa(mut self) {
    self.msaa = if self.msaa == 4 { 1 } else { 4 }
}

/// Drains the device's debug messages into a. Errors: OutOfMemory.
/// O(messages); allocates the list and its text in a.
export SceneRenderer.TakeMessages(mut self, a: &Arena): OutOfMemory!List[gpu.Message] from a {
    return try self.dev.TakeMessages(a)
}

/// Returns the device's Backend. O(1); allocates nothing.
export SceneRenderer.Backend(&self): gpu.Backend {
    return self.dev.Backend()
}

/// Releases every GPU handle in dependency order, closes the device and returns
/// what it leaked. O(handles); frees, allocates nothing.
export SceneRenderer.Close(self, io: Io): gpu.Leaks {
    var SceneRenderer(
        heap,
        dev,
        sc,
        sh_shadow_vs,
        sh_mip_vs,
        sh_mip_ps,
        sh_scene_vs,
        sh_scene_ps,
        sh_inst_vs,
        sh_inst_ps,
        sh_ov_vs,
        sh_ov_ps,
        sh_blur_cs,
        bgl_shadow,
        pl_shadow,
        pipe_shadow,
        bgl_mip,
        pl_mip,
        pipe_mip,
        bgl_scene,
        pl_scene,
        pipe_scene,
        pipe_scene_msaa,
        bgl_inst,
        pl_inst,
        pipe_inst,
        pipe_inst_msaa,
        pl_ov,
        pipe_ov,
        pipe_ov_msaa,
        bgl_blur,
        pl_blur,
        pipe_blur,
        samp_linear,
        samp_shadow,
        frame_ubo,
        draw_ubo,
        inst_buf,
        blur_buf_a,
        blur_buf_b,
        shadow_tex,
        shadow_view,
        color_tex,
        color_view,
        msaa_tex,
        msaa_view,
        depth_tex,
        depth_view,
        msaa_depth_tex,
        msaa_depth_view,
        bg_shadow,
        bg_inst,
        bg_blur_h,
        bg_blur_v,
        scene_bgs,
        meshes,
        textures,
        draw_items,
        objects_data,
        width: _,
        height: _,
        blur_stride_u32: _,
        msaa: _,
        shadows: _,
        blur: _,
        overlay: _,
        instances: _,
        clear_r: _,
        clear_g: _,
        clear_b: _,
        clear_a: _,
        camera: _,
        light: _,
        yaw_deg: _,
        pitch_deg: _,
        zoom: _,
        sc_format: _,
    ) := self
    alloc := heap.Alloc()
    draw_items.Free(alloc)
    objects_data.Free(alloc)

    CloseGroups(alloc, scene_bgs, mut dev)
    bg_blur_v.Close(mut dev)
    bg_blur_h.Close(mut dev)
    bg_inst.Close(mut dev)
    bg_shadow.Close(mut dev)

    msaa_depth_view.Close(mut dev)
    msaa_depth_tex.Close(mut dev)
    depth_view.Close(mut dev)
    depth_tex.Close(mut dev)
    msaa_view.Close(mut dev)
    msaa_tex.Close(mut dev)
    color_view.Close(mut dev)
    color_tex.Close(mut dev)
    shadow_view.Close(mut dev)
    shadow_tex.Close(mut dev)

    CloseTextures(alloc, textures, mut dev)
    CloseMeshes(alloc, meshes, mut dev)

    blur_buf_b.Close(mut dev)
    blur_buf_a.Close(mut dev)
    inst_buf.Close(mut dev)
    draw_ubo.Close(mut dev)
    frame_ubo.Close(mut dev)

    samp_shadow.Close(mut dev)
    samp_linear.Close(mut dev)

    pipe_blur.Close(mut dev)
    pl_blur.Close(mut dev)
    bgl_blur.Close(mut dev)

    pipe_ov_msaa.Close(mut dev)
    pipe_ov.Close(mut dev)
    pl_ov.Close(mut dev)

    pipe_inst_msaa.Close(mut dev)
    pipe_inst.Close(mut dev)
    pl_inst.Close(mut dev)
    bgl_inst.Close(mut dev)

    pipe_scene_msaa.Close(mut dev)
    pipe_scene.Close(mut dev)
    pl_scene.Close(mut dev)
    bgl_scene.Close(mut dev)

    pipe_mip.Close(mut dev)
    pl_mip.Close(mut dev)
    bgl_mip.Close(mut dev)

    pipe_shadow.Close(mut dev)
    pl_shadow.Close(mut dev)
    bgl_shadow.Close(mut dev)

    sh_blur_cs.Close(mut dev)
    sh_ov_ps.Close(mut dev)
    sh_ov_vs.Close(mut dev)
    sh_inst_ps.Close(mut dev)
    sh_inst_vs.Close(mut dev)
    sh_scene_ps.Close(mut dev)
    sh_scene_vs.Close(mut dev)
    sh_mip_ps.Close(mut dev)
    sh_mip_vs.Close(mut dev)
    sh_shadow_vs.Close(mut dev)

    if swap := sc {
        swap.Close(io, mut dev)
    }
    return dev.Close(io)
}
