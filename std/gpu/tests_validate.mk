// Why: every portable validation check must reject misuse with the exact
// Misuse variant and at index, and deferred/serial/abandon paths must leave
// zero leaks.
//
// What: creation, bind group, pipeline, pass, copy, mapping, swapchain,
// deferred release, WriteBuffer->MapRead, and Abandon->WaitIdle tests via
// OpenStub.
//
// How: exercises every Misuse rule on minimal wrong setups against OpenStub.

CheckInvalid(e: Error, want: Misuse) {
    match e {
        Invalid(misuse, at: _) { Assert(misuse == want) }
        _ { Assert(false) }
    }
}

CheckInvalidAt(e: Error, want: Misuse, want_at: int) {
    match e {
        Invalid(misuse, at) { Assert(misuse == want && at == want_at) }
        _ { Assert(false) }
    }
}

ExpectBadBuf(dev: mut Device, sz: int, u: BufferUsage, want: Misuse) {
    b := dev.NewBuffer(sz, u, "b") else e {
        CheckInvalid(e, want)
        return
    }
    b.Close(dev)
    Assert(false)
}

ExpectBadTex(dev: mut Device, td: TextureDesc, want: Misuse) {
    t := dev.NewTexture(td) else e {
        CheckInvalid(e, want)
        return
    }
    t.Close(dev)
    Assert(false)
}

ExpectBadView(dev: mut Device, t: &Texture, vd: ViewDesc, want: Misuse) {
    v := dev.NewView(t, vd) else e {
        CheckInvalid(e, want)
        return
    }
    v.Close(dev)
    Assert(false)
}

ExpectBadSampler(dev: mut Device, sd: SamplerDesc, want: Misuse) {
    s := dev.NewSampler(sd) else e {
        CheckInvalid(e, want)
        return
    }
    s.Close(dev)
    Assert(false)
}

ExpectBadBgl(dev: mut Device, entries: []LayoutEntry, want: Misuse, want_at: int) {
    l := dev.NewBindGroupLayout(entries, "bad") else e {
        CheckInvalidAt(e, want, want_at)
        return
    }
    l.Close(dev)
    Assert(false)
}

ExpectBadBg(dev: mut Device, l: &BindGroupLayout, binds: []BindEntry, want: Misuse, want_at: int) {
    g := dev.NewBindGroup(l, binds, "bad") else e {
        CheckInvalidAt(e, want, want_at)
        return
    }
    g.Close(dev)
    Assert(false)
}

ExpectBadPipe(dev: mut Device, io: Io, pl: &PipelineLayout, vs: &Shader, ps: ?&Shader, pd: RenderPipelineDesc, want: Misuse) {
    p := dev.NewRenderPipeline(io, pl, vs, ps, pd) else e {
        CheckInvalid(e, want)
        return
    }
    p.Close(dev)
    Assert(false)
}

ExpectSubmitMisuse(enc: Encoder, want: Misuse) {
    var failed := false
    enc.Submit() else e {
        CheckInvalid(e, want)
        failed = true
        enc.Abandon()
    }
    Assert(failed)
}

test "validate buffer, texture, view, sampler creation rules" (sys: &System) {
    var dev := try OpenStub(sys.heap, DefaultDeviceOptions)
    defer {
        Assert(dev.Close(sys.io).native == 0)
    }

    ExpectBadBuf(mut dev, 64, NoBufferUsage, Misuse.BadUsage)
    var bad_mr := NoBufferUsage
    bad_mr.map_read = true
    ExpectBadBuf(mut dev, 64, bad_mr, Misuse.BadUsage)
    bad_mr.copy_dst = true
    bad_mr.uniform = true
    ExpectBadBuf(mut dev, 64, bad_mr, Misuse.BadUsage)

    var vu := NoBufferUsage
    vu.vertex = true
    ExpectBadBuf(mut dev, 0, vu, Misuse.LimitExceeded)

    var bc_td := DefaultTextureDesc
    bc_td.format = TextureFormat.Bc1Unorm
    bc_td.width = 4
    bc_td.height = 4
    ExpectBadTex(mut dev, bc_td, Misuse.FormatCapability)

    var ms_td := DefaultTextureDesc
    ms_td.samples = 2
    ExpectBadTex(mut dev, ms_td, Misuse.SamplesMismatch)

    var twin_td := DefaultTextureDesc
    vfs := [TextureFormat.Bgra8Unorm]
    twin_td.view_formats = vfs[..]
    ExpectBadTex(mut dev, twin_td, Misuse.FormatMismatch)

    tex := try dev.NewTexture(DefaultTextureDesc)
    defer tex.Close(mut dev)
    var vd := DefaultViewDesc
    vd.format = some(TextureFormat.Rgba8UnormSrgb)
    ExpectBadView(mut dev, &tex, vd, Misuse.ViewFormatNotListed)
    var bvd := DefaultViewDesc
    bvd.first_mip = 5
    ExpectBadView(mut dev, &tex, bvd, Misuse.BadView)

    var sd := DefaultSamplerDesc
    sd.anisotropy = 4
    ExpectBadSampler(mut dev, sd, Misuse.BadUsage)
}

test "validate bind group layout, pipeline layout, bind group, and pipeline rules" (sys: &System) {
    var dev := try OpenStub(sys.heap, DefaultDeviceOptions)
    defer {
        Assert(dev.Close(sys.io).native == 0)
    }

    v_frag := Visibility(vertex: false, fragment: true, compute: false)
    w_entries := [LayoutEntry(binding: 0, visibility: v_frag, kind: BindingKind.Storage(read_only: false, min_size: 4, stride: 4))]
    ExpectBadBgl(mut dev, w_entries[..], Misuse.StorageWriteInRender, 0)

    dup_entries := [
        LayoutEntry(binding: 1, visibility: v_frag, kind: BindingKind.Uniform(dynamic: false, min_size: 16)),
        LayoutEntry(binding: 1, visibility: v_frag, kind: BindingKind.Sampler(comparison: false)),
    ]
    ExpectBadBgl(mut dev, dup_entries[..], Misuse.BadLayout, 1)

    bgl_entries := [LayoutEntry(binding: 0, visibility: v_frag, kind: BindingKind.Uniform(dynamic: false, min_size: 16))]
    bgl := try dev.NewBindGroupLayout(bgl_entries[..], "u")
    defer bgl.Close(mut dev)

    var vu := NoBufferUsage
    vu.vertex = true
    vbuf := try dev.NewBuffer(256, vu, "vb")
    defer vbuf.Close(mut dev)
    bad_bind := [BindEntry(binding: 0, bound: Bound.Buffer(b: &vbuf, offset: 0, size: 16))]
    ExpectBadBg(mut dev, &bgl, bad_bind[..], Misuse.BindMismatch, 0)

    vs_blob := try DecodeShader(sys.arena, QuadVsSpirvZ)
    ps_blob := try DecodeShader(sys.arena, QuadPsSpirvZ)
    vs := try dev.NewShader(ShaderCode(spirv: vs_blob, dxil: "".AsBytes()), Stage.Vertex, "VsMain", "vs")
    defer vs.Close(mut dev)
    ps := try dev.NewShader(ShaderCode(spirv: ps_blob, dxil: "".AsBytes()), Stage.Fragment, "PsMain", "ps")
    defer ps.Close(mut dev)

    small_entries := [LayoutEntry(binding: 0, visibility: v_frag, kind: BindingKind.Uniform(dynamic: false, min_size: 8))]
    small_bgl := try dev.NewBindGroupLayout(small_entries[..], "small")
    defer small_bgl.Close(mut dev)
    small_gs := [&small_bgl]
    small_pl := try dev.NewPipelineLayout(small_gs[..], 0, "small")
    defer small_pl.Close(mut dev)
    targets := [ColorOf(TextureFormat.Rgba8Unorm)]
    attrs := [VertexAttribute(location: 0, format: VertexFormat.Float32x2, offset: 0)]
    vls := [VertexLayout(stride: 8, step: StepMode.Vertex, attributes: attrs[..])]
    var pd := DefaultRenderPipeline
    pd.vertex_layouts = vls[..]
    pd.targets = targets[..]
    ExpectBadPipe(mut dev, sys.io, &small_pl, &vs, some(&ps), pd, Misuse.BlockTooLarge)
    ExpectBadPipe(mut dev, sys.io, &small_pl, &ps, none, pd, Misuse.StageMismatch)
}

test "validate pass/encoder recording misuse, WriteBuffer->MapRead, Abandon->WaitIdle, and deferred close" (sys: &System) {
    var dev := try OpenStub(sys.heap, DefaultDeviceOptions)
    defer {
        Assert(dev.Close(sys.io).native == 0)
    }

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
    td.usage.sampled = true
    tex := try dev.NewTexture(td)
    defer tex.Close(mut dev)
    view_ := try dev.NewView(&tex, DefaultViewDesc)
    defer view_.Close(mut dev)

    // 1. NoPipeline
    var e1 := try dev.Encoder("e1")
    colors := [ColorAttachment(target: &view_, resolve: none, load: Load.DontCare, store: Store.Store)]
    desc := PassDesc(colors: colors[..], depth: none, label: "p")
    var p1 := e1.RenderPass(&desc)
    p1.Draw(3, 1, 0, 0)
    p1.End()
    ExpectSubmitMisuse(e1, Misuse.NoPipeline)

    // 2. DebugGroupUnbalanced
    var e2 := try dev.Encoder("e2")
    e2.PushDebugGroup("unclosed")
    ExpectSubmitMisuse(e2, Misuse.DebugGroupUnbalanced)

    // 3. ExtentMismatch in RenderPass
    var td2 := DefaultTextureDesc
    td2.width = 8
    td2.height = 8
    td2.usage.render_target = true
    tex2 := try dev.NewTexture(td2)
    defer tex2.Close(mut dev)
    view2 := try dev.NewView(&tex2, DefaultViewDesc)
    defer view2.Close(mut dev)
    var e3 := try dev.Encoder("e3")
    bad_colors := [
        ColorAttachment(target: &view_, resolve: none, load: Load.DontCare, store: Store.Store),
        ColorAttachment(target: &view2, resolve: none, load: Load.DontCare, store: Store.Store),
    ]
    bad_desc := PassDesc(colors: bad_colors[..], depth: none, label: "bad")
    var p3 := e3.RenderPass(&bad_desc)
    p3.End()
    ExpectSubmitMisuse(e3, Misuse.ExtentMismatch)

    // 4. WriteBuffer -> MapRead with no Submit, and Abandon -> WaitIdle
    var rwu := NoBufferUsage
    rwu.map_read = true
    rwu.copy_dst = true
    rbuf := try dev.NewBuffer(64, rwu, "rb")
    defer rbuf.Close(mut dev)
    payload: [4]u8 = [10, 20, 30, 40]
    try dev.WriteBuffer(&rbuf, 4, payload[..])
    m := try dev.MapRead(sys.io, &rbuf, 0, 16)
    got := m.Bytes()
    Assert(got[4] == 10 && got[5] == 20 && got[6] == 30 && got[7] == 40)
    m.Unmap()

    var e4 := try dev.Encoder("e4")
    e4.Abandon()
    try dev.WaitIdle(sys.io)

    // 5. AttachmentAlsoBound
    v_all := Visibility(vertex: false, fragment: true, compute: true)
    t_entries := [LayoutEntry(binding: 0, visibility: v_all, kind: BindingKind.Texture(sample: SampleType.Float, dimension: ViewDimension.D2, multisampled: false))]
    tbgl := try dev.NewBindGroupLayout(t_entries[..], "t")
    defer tbgl.Close(mut dev)
    tgs := [&tbgl]
    tpl := try dev.NewPipelineLayout(tgs[..], 0, "tpl")
    defer tpl.Close(mut dev)
    tbinds := [BindEntry(binding: 0, bound: Bound.View(v: &view_))]
    tbg := try dev.NewBindGroup(&tbgl, tbinds[..], "tbg")
    defer tbg.Close(mut dev)
    tpipe := try dev.NewRenderPipeline(sys.io, &tpl, &vs, some(&ps), pd)
    defer tpipe.Close(mut dev)

    var e5 := try dev.Encoder("e5")
    var p5 := e5.RenderPass(&desc)
    p5.SetPipeline(&tpipe)
    var no_dyn: [0]u32 = []
    p5.SetBindGroup(0, &tbg, no_dyn[..])
    p5.Draw(3, 1, 0, 0)
    p5.End()
    ExpectSubmitMisuse(e5, Misuse.AttachmentAlsoBound)

    // 6. WriteReadConflict in ComputePass
    cs_blob := try DecodeShader(sys.arena, DoubleCsSpirvZ)
    cs := try dev.NewShader(ShaderCode(spirv: cs_blob, dxil: "".AsBytes()), Stage.Compute, "CsMain", "cs")
    defer cs.Close(mut dev)
    v_comp := Visibility(vertex: false, fragment: false, compute: true)
    c_entries := [
        LayoutEntry(binding: 0, visibility: v_comp, kind: BindingKind.Storage(read_only: false, min_size: 4, stride: 4)),
        LayoutEntry(binding: 1, visibility: v_comp, kind: BindingKind.Storage(read_only: true, min_size: 4, stride: 4)),
    ]
    cbgl := try dev.NewBindGroupLayout(c_entries[..], "c")
    defer cbgl.Close(mut dev)
    cgs := [&cbgl]
    cpl := try dev.NewPipelineLayout(cgs[..], 0, "cpl")
    defer cpl.Close(mut dev)
    var su := NoBufferUsage
    su.storage = true
    sbuf := try dev.NewBuffer(256, su, "sb")
    defer sbuf.Close(mut dev)
    cbinds := [
        BindEntry(binding: 0, bound: Bound.Buffer(b: &sbuf, offset: 0, size: 256)),
        BindEntry(binding: 1, bound: Bound.Buffer(b: &sbuf, offset: 0, size: 256)),
    ]
    cbg := try dev.NewBindGroup(&cbgl, cbinds[..], "cbg")
    defer cbg.Close(mut dev)
    cpipe := try dev.NewComputePipeline(sys.io, &cpl, &cs, "cp")
    defer cpipe.Close(mut dev)

    var e6 := try dev.Encoder("e6")
    var cp := e6.ComputePass("cp")
    cp.SetPipeline(&cpipe)
    cp.SetBindGroup(0, &cbg, no_dyn[..])
    cp.Dispatch(1, 1, 1)
    cp.End()
    ExpectSubmitMisuse(e6, Misuse.WriteReadConflict)
}

test "validate distinct device identities" (sys: &System) {
    d1 := try OpenStub(sys.heap, DefaultDeviceOptions)
    defer d1.Abandon(sys.io)
    d2 := try OpenStub(sys.heap, DefaultDeviceOptions)
    defer d2.Abandon(sys.io)
    Assert(d1.serial != d2.serial)
}

test "device loss sticky error and deferred close release" (sys: &System) {
    var dev := try OpenStub(sys.heap, DefaultDeviceOptions)
    defer {
        Assert(dev.Close(sys.io).native == 0)
    }

    vs_blob := try DecodeShader(sys.arena, TriVsSpirvZ)
    vs := try dev.NewShader(ShaderCode(spirv: vs_blob, dxil: "".AsBytes()), Stage.Vertex, "VsMain", "vs")
    defer vs.Close(mut dev)
    var no_groups: [0]&BindGroupLayout = []
    pl := try dev.NewPipelineLayout(no_groups[..], 0, "empty")
    defer pl.Close(mut dev)
    var td := DefaultTextureDesc
    td.width = 8
    td.height = 8
    td.usage.render_target = true
    tex := try dev.NewTexture(td)
    defer tex.Close(mut dev)
    view_ := try dev.NewView(&tex, DefaultViewDesc)
    defer view_.Close(mut dev)
    targets := [ColorOf(TextureFormat.Rgba8Unorm)]
    var pd := DefaultRenderPipeline
    pd.targets = targets[..]
    pipe := try dev.NewRenderPipeline(sys.io, &pl, &vs, none, pd)
    defer pipe.Close(mut dev)
    var vu := NoBufferUsage
    vu.vertex = true
    b := try dev.NewBuffer(64, vu, "b")
    onerr b.Close(mut dev)
    var enc := try dev.Encoder("use")
    onerr enc.Abandon()
    colors := [ColorAttachment(target: &view_, resolve: none, load: Load.DontCare, store: Store.Store)]
    desc := PassDesc(colors: colors[..], depth: none, label: "p")
    var pass := enc.RenderPass(&desc)
    pass.SetPipeline(&pipe)
    pass.SetVertexBuffer(0, &b, 0)
    pass.Draw(3, 1, 0, 0)
    pass.End()
    try enc.Submit()

    b.Close(mut dev)
    Assert(dev.state.Ref().deferred.Len() == 1)
    try dev.WaitIdle(sys.io)
    Assert(dev.state.Ref().deferred.Len() == 0)

    dev.SetLost(LossReason.Removed)
    after := dev.NewBuffer(64, vu, "after-loss") else e {
        match e {
            DeviceLost(reason) { Assert(reason == LossReason.Removed) }
            _ { Assert(false) }
        }
        return
    }
    after.Close(mut dev)
    Assert(false)
}
