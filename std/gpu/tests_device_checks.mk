// Why: a device must stay correct past the happy path: after it is lost,
// across a long run of frames, around driver calls that may touch the
// floating-point environment, and at the edges of copies and dynamic
// offsets.
//
// What: on the adapter MK_GPU_ADAPTER selects: device loss (every call then
// fails DeviceLost, every Close releases, a new Open works); MXCSR unchanged
// across Open, pipeline creation, submission and present; no heap
// allocation across 1,000 recorded and submitted frames; WriteBuffer
// ordered against submissions; odd-size texture copies; and a dynamic
// uniform offset at the last aligned position of its buffer.
//
// How: loss is injected with Device.TriggerLossForTest (D3D12 RemoveDevice,
// Vulkan a fault at the next submit); the counting heap of std.gpu.dx counts
// every allocation the device makes; results are read back and compared
// with what was written.

import std.gpu.dx
import std.window

IsDeviceLost(e: Error): bool {
    return match e {
        DeviceLost(reason: _) { true }
        _ { false }
    }
}

// SelectedKind is the adapter the run selects, printing the skip reason when
// there is none.
SelectedKind(sys: &System): ?str {
    return match ActiveAdapterKind(sys.arena, sys.io) {
        some(k) { some(k) }
        none {
            _ = sys.Print("skip: no GPU adapter available\n") else {}
            none
        }
    }
}

// SubmitClear records one ClearBuffer of b and submits it.
SubmitClear(dev: mut Device, b: &Buffer): Error!void {
    var enc := try dev.Encoder("clear")
    onerr enc.Abandon()
    enc.ClearBuffer(b, 0, 256)
    try enc.Submit()
}

// The Lost helpers each make one call and report whether it failed with
// DeviceLost; a call that succeeds releases what it made.
SubmitLost(dev: mut Device, b: &Buffer): bool {
    SubmitClear(dev, b) else e { return IsDeviceLost(e) }
    return false
}

NewBufferLost(dev: mut Device): bool {
    var u := NoBufferUsage
    u.storage = true
    nb := dev.NewBuffer(256, u, "after_loss") else e { return IsDeviceLost(e) }
    nb.Close(dev)
    return false
}

EncoderLost(dev: mut Device): bool {
    var enc := dev.Encoder("after_loss") else e { return IsDeviceLost(e) }
    enc.Abandon()
    return false
}

WaitIdleLost(dev: mut Device, io: Io): bool {
    dev.WaitIdle(io) else e { return IsDeviceLost(e) }
    return false
}

test "device loss: calls fail DeviceLost, Close releases everything, a new Open works" (sys: &System) {
    kind := match SelectedKind(sys) {
        some(k) { k }
        none { return }
    }
    var dev := try OpenDeviceForKind(sys.heap, sys.io, kind, DebugMode.Validate)
    onerr dev.Abandon(sys.io)
    b := try NewStorageBuffer(mut dev, 256, "loss_b")
    onerr b.Close(mut dev)
    try SubmitClear(mut dev, &b)
    try dev.WaitIdle(sys.io)

    // Vulkan reports the loss from the next submit; D3D12 accepts that
    // submit and reports the removal from the wait on it. Once a call has
    // reported the loss, every call does.
    dev.TriggerLossForTest()
    at_submit := SubmitLost(mut dev, &b)
    at_wait := WaitIdleLost(mut dev, sys.io)
    _ = sys.Print("loss reported at submit={s} wait={w}\n", (s: at_submit, w: at_wait)) else {}
    Assert(at_submit || at_wait)
    Assert(NewBufferLost(mut dev))
    Assert(EncoderLost(mut dev))
    Assert(SubmitLost(mut dev, &b))
    Assert(WaitIdleLost(mut dev, sys.io))
    b.Close(mut dev)
    l := dev.Close(sys.io)
    Assert(l.native == 0)

    var again := try OpenDeviceForKind(sys.heap, sys.io, kind, DebugMode.Validate)
    onerr again.Abandon(sys.io)
    b2 := try NewStorageBuffer(mut again, 256, "reopen_b")
    onerr b2.Close(mut again)
    try SubmitClear(mut again, &b2)
    try again.WaitIdle(sys.io)
    b2.Close(mut again)
    AssertCleanLeaks(sys, again)
}

// PresentLoop shows n cleared frames on sc; false when the window is
// hidden and nothing can be shown.
PresentLoop(io: Io, dev: mut Device, sc: mut Swapchain, n: int): Error!bool {
    var shown := 0
    for _ in 0..n * 4 {
        if shown == n {
            break
        }
        match try dev.Acquire(io, sc) {
            .Ready(frame) {
                onerr frame.Abandon()
                var enc := try dev.Encoder("present")
                onerr enc.Abandon()
                cols := [ColorAttachment(target: frame.View(), resolve: none, load: Load.Clear(r: 0.2, g: 0.3, b: 0.4, a: 1.0), store: Store.Store)]
                var rp := enc.RenderPass(&PassDesc(colors: cols[..], depth: none, label: "present"))
                rp.End()
                try enc.Submit()
                _ = try dev.Present(io, frame)
                shown += 1
            }
            .Outdated { try dev.Resize(io, sc, 64, 64) }
            .Hidden { return false }
        }
    }
    return shown == n
}

// PresentFrames shows n cleared frames on a hidden window, or returns false
// when presentation is unavailable (no window station or surface).
PresentFrames(sys: &System, dev: mut Device, n: int): Error!bool {
    var opts := window.DefaultWindowOptions
    opts.visible = false
    opts.width = 64
    opts.height = 64
    w := window.Open(sys.heap, sys.io, opts) else { return false }
    var sc := dev.NewSwapchain(sys.io, &w, DefaultSwapchainOptions) else e {
        w.Close()
        if IsDeviceLost(e) {
            fail e
        }
        return false
    }
    shown := PresentLoop(sys.io, dev, mut sc, n) else e {
        sc.Close(sys.io, dev)
        w.Close()
        fail e
    }
    sc.Close(sys.io, dev)
    w.Close()
    return shown
}

// MxcsrControl is the environment part of MXCSR: the exception masks,
// rounding mode, flush-to-zero and denormals-are-zero (bits 6-15). Bits 0-5
// are sticky status flags that any inexact operation sets, Mk's own
// arithmetic included, so they are not compared.
MxcsrControl(): u32 {
    return dx.ReadMxcsr() & 0xFFC0
}

test "device MXCSR: unchanged across Open, pipeline creation, submit and present" (sys: &System) {
    if Target.arch != Arch.X64 {
        _ = sys.Print("skip: MXCSR is x86_64 only\n", ()) else {}
        return
    }
    kind := match SelectedKind(sys) {
        some(k) { k }
        none { return }
    }
    // The environment matters on the path programs ship, so the device runs
    // without debug layers, which add their own code to every call.
    m0 := MxcsrControl()
    var dev := try OpenDeviceForKind(sys.heap, sys.io, kind, DebugMode.Off)
    defer AssertCleanLeaks(sys, dev)
    Assert(MxcsrControl() == m0)

    rt := try DevNewRtTex(mut dev, 64, 64, 1, 1, "mx_rt")
    defer rt.Close(mut dev)
    rt_v := try dev.NewView(&rt, DefaultViewDesc)
    defer rt_v.Close(mut dev)
    img := try RenderSceneTriangle(sys.arena, sys.io, mut dev, &rt, &rt_v)
    Assert(img.width == 64)
    Assert(MxcsrControl() == m0)

    if !(try PresentFrames(sys, mut dev, 3)) {
        _ = sys.Print("present step skipped: presentation unavailable\n") else {}
    }
    Assert(MxcsrControl() == m0)
}

test "device frames: 1000 recorded and submitted frames allocate nothing" (sys: &System) {
    kind := match SelectedKind(sys) {
        some(k) { k }
        none { return }
    }
    counts := dx.NewCountingHeapState(sys.heap.Alloc())
    heap := dx.MakeCountingHeap(&counts)
    var dev := try OpenDeviceForKind(&heap, sys.io, kind, DebugMode.Strict)
    defer AssertCleanLeaks(sys, dev)

    rt := try DevNewRtTex(mut dev, 64, 64, 1, 1, "fr_rt")
    defer rt.Close(mut dev)
    rt_v := try dev.NewView(&rt, DefaultViewDesc)
    defer rt_v.Close(mut dev)
    vs := try LoadShaderPair(sys.arena, mut dev, TriVsSpirvZ, TriVsDxilZ, Stage.Vertex, "VsMain", "fr_vs")
    defer vs.Close(mut dev)
    ps := try LoadShaderPair(sys.arena, mut dev, TriPsSpirvZ, TriPsDxilZ, Stage.Fragment, "PsMain", "fr_ps")
    defer ps.Close(mut dev)
    var empty_bgls: [0]&BindGroupLayout = []
    pl := try dev.NewPipelineLayout(empty_bgls[..], 0, "fr_pl")
    defer pl.Close(mut dev)
    tgts := [ColorOf(TextureFormat.Rgba8Unorm)]
    var pd := DefaultRenderPipeline
    pd.targets = tgts[..]
    pipe := try dev.NewRenderPipeline(sys.io, &pl, &vs, some(&ps), pd)
    defer pipe.Close(mut dev)

    // Warm-up frames grow the stream chunks, deferred lists and staging to
    // their steady size; after that a frame must reuse what exists.
    var before: i64 = 0
    for f in 0..1032 {
        if f == 32 {
            try dev.WaitIdle(sys.io)
            before = dx.CountingHeapAllocs(&counts).allocs
        }
        var enc := try dev.Encoder("frame")
        onerr enc.Abandon()
        cols := [ColorAttachment(target: &rt_v, resolve: none, load: Load.Clear(r: 0.0, g: 0.0, b: 0.0, a: 1.0), store: Store.Store)]
        var rp := enc.RenderPass(&PassDesc(colors: cols[..], depth: none, label: "frame"))
        rp.SetPipeline(&pipe)
        rp.Draw(3, 1, 0, 0)
        rp.End()
        try enc.Submit()
        if f % 3 == 2 {
            try dev.WaitIdle(sys.io)
        }
    }
    try dev.WaitIdle(sys.io)
    after := dx.CountingHeapAllocs(&counts).allocs
    _ = sys.Print("device heap allocations across 1000 frames: {n}\n", (n: after - before)) else {}
    Assert(after == before)
}

test "device writes: WriteBuffer ordered against submissions, odd-size texture copies" (sys: &System) {
    kind := match SelectedKind(sys) {
        some(k) { k }
        none { return }
    }
    var dev := try OpenDeviceForKind(sys.heap, sys.io, kind, DebugMode.Strict)
    defer AssertCleanLeaks(sys, dev)

    // Two writes to one range around a submitted copy: the copy sees the
    // first write and a second copy the second, so a write may not land
    // before the work submitted ahead of it.
    b := try NewStorageBuffer(mut dev, 256, "wr_b")
    defer b.Close(mut dev)
    rb := try NewReadbackBuffer(mut dev, 768, "wr_rb")
    defer rb.Close(mut dev)
    var first := try sys.arena.Slice[u8](256)
    var second := try sys.arena.Slice[u8](256)
    for i in 0..256 {
        first[i] = Trunc[u8](i)
        second[i] = Trunc[u8](255 - i)
    }
    try dev.WriteBuffer(&b, 0, first[..])
    try dev.WriteBuffer(&b, 64, second[64..128])
    var e1 := try dev.Encoder("wr1")
    onerr e1.Abandon()
    e1.CopyBuffer(&b, 0, &rb, 0, 256)
    try e1.Submit()
    try dev.WriteBuffer(&b, 0, second[..])
    var e2 := try dev.Encoder("wr2")
    onerr e2.Abandon()
    e2.CopyBuffer(&b, 0, &rb, 256, 256)
    try e2.Submit()
    var in_stream := try dev.Encoder("in-stream write")
    onerr in_stream.Abandon()
    try in_stream.WriteBuffer(&b, 0, first[..])
    in_stream.CopyBuffer(&b, 0, &rb, 512, 256)
    try in_stream.Submit()
    mr := try dev.MapRead(sys.io, &rb, 0, 768)
    got := mr.Bytes()
    for i in 0..256 {
        want_first := if i >= 64 && i < 128 { second[i] } else { first[i] }
        Assert(got[i] == want_first)
        Assert(got[256 + i] == second[i])
        Assert(got[512 + i] == first[i])
    }
    mr.Unmap()

    // A 13x7 texture: rows of 52 bytes, neither a multiple of 256 nor of 4
    // texels, written tight and read back both tight and through a buffer
    // with 256-byte rows at a non-zero offset.
    var td := DefaultTextureDesc
    td.width = 13
    td.height = 7
    td.layers = 2
    td.usage.copy_dst = true
    td.usage.copy_src = true
    td.usage.render_target = true
    tex := try dev.NewTexture(td)
    defer tex.Close(mut dev)
    var vd := DefaultViewDesc
    vd.first_layer = 1
    layer_view := try dev.NewView(&tex, vd)
    defer layer_view.Close(mut dev)
    var layer_enc := try dev.Encoder("different layer states")
    onerr layer_enc.Abandon()
    colors := [ColorAttachment(target: &layer_view, resolve: none, load: Load.Clear(r: 0.0, g: 0.0, b: 0.0, a: 1.0), store: Store.Store)]
    desc := PassDesc(colors: colors[..], depth: none, label: "layer 1")
    var layer_pass := layer_enc.RenderPass(&desc)
    layer_pass.End()
    try layer_enc.Submit()

    var px := try sys.arena.Slice[u8](13 * 7 * 4 * 2)
    for i in 0..px.Len() {
        px[i] = Trunc[u8](i * 7 + 3)
    }
    region := TextureRegion(texture: &tex, mip: 0, layer: 0, x: 0, y: 0, aspect: Aspect.All)
    size := Extent(width: 13, height: 7, layers: 2)
    try dev.WriteTexture(region, px[..], size)
    tight := try dev.ReadTexture(sys.arena, sys.io, region, size)
    Assert(tight.Len() == px.Len())
    for i in 0..px.Len() {
        Assert(tight[i] == px[i])
    }
    tb := try NewReadbackBuffer(mut dev, 512 + 256 * 7, "odd_rb")
    defer tb.Close(mut dev)
    var e3 := try dev.Encoder("odd")
    onerr e3.Abandon()
    e3.CopyTextureToBuffer(region, &tb, DataLayout(offset: 512, bytes_per_row: 256, rows_per_layer: 7), Extent(width: 13, height: 7, layers: 1))
    try e3.Submit()
    mt := try dev.MapRead(sys.io, &tb, 0, 512 + 256 * 7)
    tbytes := mt.Bytes()
    for y in 0..7 {
        for x in 0..52 {
            Assert(tbytes[512 + y * 256 + x] == px[y * 52 + x])
        }
    }
    mt.Unmap()
}

const DynUniformBytes: int = 1024

test "device dynamic offset: a uniform at the last aligned offset of its buffer" (sys: &System) {
    kind := match SelectedKind(sys) {
        some(k) { k }
        none { return }
    }
    var dev := try OpenDeviceForKind(sys.heap, sys.io, kind, DebugMode.Strict)
    defer AssertCleanLeaks(sys, dev)

    rt := try DevNewRtTex(mut dev, 64, 64, 1, 1, "dyn_rt")
    defer rt.Close(mut dev)
    rt_v := try dev.NewView(&rt, DefaultViewDesc)
    defer rt_v.Close(mut dev)
    vs := try LoadShaderPair(sys.arena, mut dev, QuadVsSpirvZ, QuadVsDxilZ, Stage.Vertex, "VsMain", "dyn_vs")
    defer vs.Close(mut dev)
    ps := try LoadShaderPair(sys.arena, mut dev, QuadPsSpirvZ, QuadPsDxilZ, Stage.Fragment, "PsMain", "dyn_ps")
    defer ps.Close(mut dev)
    bgl := try dev.NewBindGroupLayout([
        LayoutEntry(binding: 0, visibility: Visibility(vertex: false, fragment: true, compute: false), kind: BindingKind.Uniform(dynamic: true, min_size: 16)),
    ][..], "dyn_bgl")
    defer bgl.Close(mut dev)
    pl := try dev.NewPipelineLayout([&bgl][..], 0, "dyn_pl")
    defer pl.Close(mut dev)
    attrs := [VertexAttribute(location: 0, format: VertexFormat.Float32x2, offset: 0)]
    vls := [VertexLayout(stride: 8, step: StepMode.Vertex, attributes: attrs[..])]
    tgts := [ColorOf(TextureFormat.Rgba8Unorm)]
    var pd := DefaultRenderPipeline
    pd.vertex_layouts = vls[..]
    pd.targets = tgts[..]
    pipe := try dev.NewRenderPipeline(sys.io, &pl, &vs, some(&ps), pd)
    defer pipe.Close(mut dev)

    var vu := NoBufferUsage
    vu.vertex = true
    vu.copy_dst = true
    vb := try dev.NewBuffer(24, vu, "dyn_vb")
    defer vb.Close(mut dev)
    var vbytes := try sys.arena.Slice[u8](24)
    tri: [6]f32 = [-1.0, -1.0, 3.0, -1.0, -1.0, 3.0]
    for i in 0..6 {
        WriteLeF32(mut vbytes[..], i, tri[i])
    }
    try dev.WriteBuffer(&vb, 0, vbytes[..])

    // Four 256-byte slots; only the last holds the colour drawn, so a
    // wrong or clamped offset shows as another colour or as black.
    var uu := NoBufferUsage
    uu.uniform_dynamic = true
    uu.copy_dst = true
    ub := try dev.NewBuffer(DynUniformBytes, uu, "dyn_ub")
    defer ub.Close(mut dev)
    var ubytes := try sys.arena.Slice[u8](DynUniformBytes)
    for slot in 0..4 {
        g: f32 = if slot == 3 { 1.0 } else { 0.0 }
        WriteLeF32(mut ubytes[..], slot * 64, 0.0)
        WriteLeF32(mut ubytes[..], slot * 64 + 1, g)
        WriteLeF32(mut ubytes[..], slot * 64 + 2, 1.0 - g)
        WriteLeF32(mut ubytes[..], slot * 64 + 3, 1.0)
    }
    try dev.WriteBuffer(&ub, 0, ubytes[..])
    bg := try dev.NewBindGroup(&bgl, [BindEntry(binding: 0, bound: Bound.Buffer(b: &ub, offset: 0, size: 16))][..], "dyn_bg")
    defer bg.Close(mut dev)

    last := [Cast[u32](DynUniformBytes - 256)]
    var enc := try dev.Encoder("dyn_enc")
    onerr enc.Abandon()
    cols := [ColorAttachment(target: &rt_v, resolve: none, load: Load.Clear(r: 0.0, g: 0.0, b: 0.0, a: 1.0), store: Store.Store)]
    var rp := enc.RenderPass(&PassDesc(colors: cols[..], depth: none, label: "dyn"))
    rp.SetPipeline(&pipe)
    rp.SetBindGroup(0, &bg, last[..])
    rp.SetVertexBuffer(0, &vb, 0)
    rp.Draw(3, 1, 0, 0)
    rp.End()
    try enc.Submit()
    img := try DevReadImage(sys.arena, sys.io, mut dev, &rt, 64, 64)
    c := (32 * 64 + 32) * 4
    Assert(img.data[c] == 0 && img.data[c + 1] == 255 && img.data[c + 2] == 0 && img.data[c + 3] == 255)
}
