// Why: compute results on a real device are only trustworthy when checked
// against an independent answer, and multi-dispatch passes are where a
// missing storage barrier between dispatches shows.
//
// What: GPU-against-CPU checks on the adapter MK_GPU_ADAPTER selects: a
// 64-bin histogram of 4,096 keys with atomic adds, a 4,096-key bitonic sort
// as 78 dispatches in one pass driven by immediates, and a 4,096-element
// prefix sum scanned per 64-thread group.
//
// How: keys come from a fixed linear congruential sequence; each kernel
// runs from the tests_shaders.mk blobs, its buffer is copied to a mappable
// one and compared word by word with the same computation on the CPU.

const ComputeKeys: int = 4096
const ComputeGroup: int = 64
const HistogramBins: int = 64

// ComputeKeyBytes fills dst with ComputeKeys little-endian u32 keys from a
// linear congruential sequence started at seed.
ComputeKeyBytes(dst: mut []u8, seed: u32) {
    var x := seed
    for i in 0..ComputeKeys {
        x = x *% 1664525 +% 1013904223
        WriteLeU32(dst, i, x >> 8)
    }
}

U32Less(a: &u32, b: &u32): bool {
    return *a < *b
}

StorageEntry(binding: int, read_only: bool, min_size: int): LayoutEntry {
    return LayoutEntry(binding: binding, visibility: Visibility(vertex: false, fragment: false, compute: true), kind: BindingKind.Storage(read_only: read_only, min_size: min_size, stride: 4))
}

NewStorageBuffer(dev: mut Device, size: int, label: str): Error!Buffer {
    var u := NoBufferUsage
    u.storage = true
    u.copy_src = true
    u.copy_dst = true
    return try dev.NewBuffer(size, u, label)
}

NewReadbackBuffer(dev: mut Device, size: int, label: str): Error!Buffer {
    var u := NoBufferUsage
    u.copy_dst = true
    u.map_read = true
    return try dev.NewBuffer(size, u, label)
}

// OpenComputeDevice opens the adapter the run selects, or reports why the
// block skips.
OpenComputeDevice(sys: &System): Error!?Device from sys.heap {
    kind := match ActiveAdapterKind(sys.arena, sys.io) {
        some(k) { k }
        none {
            _ = sys.Print("skip: no GPU adapter available\n") else {}
            return none
        }
    }
    return some(try OpenDeviceForKind(sys.heap, sys.io, kind, DebugMode.Strict))
}

test "device compute: 64-bin histogram of 4096 keys matches the CPU" (sys: &System) {
    var dev := match try OpenComputeDevice(sys) {
        some(d) { d }
        none { return }
    }
    defer AssertCleanLeaks(sys, dev)

    cs := try LoadShaderPair(sys.arena, mut dev, HistogramCsSpirvZ, HistogramCsDxilZ, Stage.Compute, "CsMain", "hist_cs")
    defer cs.Close(mut dev)
    bgl := try dev.NewBindGroupLayout([StorageEntry(0, true, ComputeKeys * 4), StorageEntry(1, false, HistogramBins * 4)][..], "hist_bgl")
    defer bgl.Close(mut dev)
    pl := try dev.NewPipelineLayout([&bgl][..], 0, "hist_pl")
    defer pl.Close(mut dev)
    pipe := try dev.NewComputePipeline(sys.io, &pl, &cs, "hist_pipe")
    defer pipe.Close(mut dev)

    keys := try NewStorageBuffer(mut dev, ComputeKeys * 4, "hist_keys")
    defer keys.Close(mut dev)
    // The bins start as a fresh buffer: the histogram is right only if
    // creation zeroed it.
    bins := try NewStorageBuffer(mut dev, HistogramBins * 4, "hist_bins")
    defer bins.Close(mut dev)
    rb := try NewReadbackBuffer(mut dev, HistogramBins * 4, "hist_rb")
    defer rb.Close(mut dev)

    var kb := try sys.arena.Slice[u8](ComputeKeys * 4)
    ComputeKeyBytes(mut kb[..], 7)
    try dev.WriteBuffer(&keys, 0, kb[..])
    bg := try dev.NewBindGroup(&bgl, [
        BindEntry(binding: 0, bound: Bound.Buffer(b: &keys, offset: 0, size: ComputeKeys * 4)),
        BindEntry(binding: 1, bound: Bound.Buffer(b: &bins, offset: 0, size: HistogramBins * 4)),
    ][..], "hist_bg")
    defer bg.Close(mut dev)

    var no_dyn: [0]u32 = []
    var enc := try dev.Encoder("hist_enc")
    onerr enc.Abandon()
    var cp := enc.ComputePass("hist")
    cp.SetPipeline(&pipe)
    cp.SetBindGroup(0, &bg, no_dyn[..])
    cp.Dispatch(ComputeKeys / ComputeGroup, 1, 1)
    cp.End()
    enc.CopyBuffer(&bins, 0, &rb, 0, HistogramBins * 4)
    try enc.Submit()

    var want: [HistogramBins]u32 = [0; HistogramBins]
    for i in 0..ComputeKeys {
        b := int(ReadLeU32(kb[..], i) & 63)
        want[b] += 1
    }
    mr := try dev.MapRead(sys.io, &rb, 0, HistogramBins * 4)
    got := mr.Bytes()
    var total: u32 = 0
    for b in 0..HistogramBins {
        Assert(ReadLeU32(got, b) == want[b])
        total += want[b]
    }
    mr.Unmap()
    Assert(int(total) == ComputeKeys)
}

test "device compute: 4096-key bitonic sort in 78 dispatches matches the CPU" (sys: &System) {
    var dev := match try OpenComputeDevice(sys) {
        some(d) { d }
        none { return }
    }
    defer AssertCleanLeaks(sys, dev)

    cs := try LoadShaderPair(sys.arena, mut dev, BitonicSortCsSpirvZ, BitonicSortCsDxilZ, Stage.Compute, "CsMain", "sort_cs")
    defer cs.Close(mut dev)
    bgl := try dev.NewBindGroupLayout([StorageEntry(0, false, ComputeKeys * 4)][..], "sort_bgl")
    defer bgl.Close(mut dev)
    pl := try dev.NewPipelineLayout([&bgl][..], 16, "sort_pl")
    defer pl.Close(mut dev)
    pipe := try dev.NewComputePipeline(sys.io, &pl, &cs, "sort_pipe")
    defer pipe.Close(mut dev)

    keys := try NewStorageBuffer(mut dev, ComputeKeys * 4, "sort_keys")
    defer keys.Close(mut dev)
    rb := try NewReadbackBuffer(mut dev, ComputeKeys * 4, "sort_rb")
    defer rb.Close(mut dev)
    var kb := try sys.arena.Slice[u8](ComputeKeys * 4)
    ComputeKeyBytes(mut kb[..], 11)
    try dev.WriteBuffer(&keys, 0, kb[..])
    bg := try dev.NewBindGroup(&bgl, [BindEntry(binding: 0, bound: Bound.Buffer(b: &keys, offset: 0, size: ComputeKeys * 4))][..], "sort_bg")
    defer bg.Close(mut dev)

    // Each (k, j) step reads what the previous dispatch wrote, so every
    // dispatch after the first depends on a storage barrier between them.
    var no_dyn: [0]u32 = []
    var imm: [16]u8 = [0; 16]
    var dispatches := 0
    var enc := try dev.Encoder("sort_enc")
    onerr enc.Abandon()
    var cp := enc.ComputePass("sort")
    cp.SetPipeline(&pipe)
    cp.SetBindGroup(0, &bg, no_dyn[..])
    var k := 2
    for k <= ComputeKeys {
        var j := k / 2
        for j >= 1 {
            WriteLeU32(mut imm[..], 0, Cast[u32](k))
            WriteLeU32(mut imm[..], 1, Cast[u32](j))
            WriteLeU32(mut imm[..], 2, Cast[u32](ComputeKeys))
            cp.SetImmediates(0, imm[..])
            cp.Dispatch(ComputeKeys / ComputeGroup, 1, 1)
            dispatches += 1
            j /= 2
        }
        k *= 2
    }
    cp.End()
    enc.CopyBuffer(&keys, 0, &rb, 0, ComputeKeys * 4)
    try enc.Submit()
    Assert(dispatches == 78)

    var want := try sys.arena.Slice[u32](ComputeKeys)
    for i in 0..ComputeKeys {
        want[i] = ReadLeU32(kb[..], i)
    }
    want.Sort(U32Less)
    mr := try dev.MapRead(sys.io, &rb, 0, ComputeKeys * 4)
    got := mr.Bytes()
    for i in 0..ComputeKeys {
        Assert(ReadLeU32(got, i) == want[i])
    }
    mr.Unmap()
}

test "device compute: 4096-element per-group prefix sum matches the CPU" (sys: &System) {
    var dev := match try OpenComputeDevice(sys) {
        some(d) { d }
        none { return }
    }
    defer AssertCleanLeaks(sys, dev)

    cs := try LoadShaderPair(sys.arena, mut dev, PrefixSumCsSpirvZ, PrefixSumCsDxilZ, Stage.Compute, "CsMain", "scan_cs")
    defer cs.Close(mut dev)
    bgl := try dev.NewBindGroupLayout([StorageEntry(0, false, ComputeKeys * 4)][..], "scan_bgl")
    defer bgl.Close(mut dev)
    pl := try dev.NewPipelineLayout([&bgl][..], 0, "scan_pl")
    defer pl.Close(mut dev)
    pipe := try dev.NewComputePipeline(sys.io, &pl, &cs, "scan_pipe")
    defer pipe.Close(mut dev)

    data := try NewStorageBuffer(mut dev, ComputeKeys * 4, "scan_data")
    defer data.Close(mut dev)
    rb := try NewReadbackBuffer(mut dev, ComputeKeys * 4, "scan_rb")
    defer rb.Close(mut dev)
    // Small values keep each group's sum far from overflow.
    var vb := try sys.arena.Slice[u8](ComputeKeys * 4)
    ComputeKeyBytes(mut vb[..], 3)
    for i in 0..ComputeKeys {
        WriteLeU32(mut vb[..], i, ReadLeU32(vb[..], i) & 0xFFFF)
    }
    try dev.WriteBuffer(&data, 0, vb[..])
    bg := try dev.NewBindGroup(&bgl, [BindEntry(binding: 0, bound: Bound.Buffer(b: &data, offset: 0, size: ComputeKeys * 4))][..], "scan_bg")
    defer bg.Close(mut dev)

    var no_dyn: [0]u32 = []
    var enc := try dev.Encoder("scan_enc")
    onerr enc.Abandon()
    var cp := enc.ComputePass("scan")
    cp.SetPipeline(&pipe)
    cp.SetBindGroup(0, &bg, no_dyn[..])
    cp.Dispatch(ComputeKeys / ComputeGroup, 1, 1)
    cp.End()
    enc.CopyBuffer(&data, 0, &rb, 0, ComputeKeys * 4)
    try enc.Submit()

    mr := try dev.MapRead(sys.io, &rb, 0, ComputeKeys * 4)
    got := mr.Bytes()
    var run: u32 = 0
    for i in 0..ComputeKeys {
        if i % ComputeGroup == 0 {
            run = 0
        }
        run += ReadLeU32(vb[..], i)
        Assert(ReadLeU32(got, i) == run)
    }
    mr.Unmap()
}
