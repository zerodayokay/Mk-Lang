// Why: state tracking bugs cause D3D12 debug layer errors, Vulkan
// synchronization validation failures, or stale barrier before-states.
//
// What: unit tests for statetrack.mk covering uniform/split subresource
// transitions, mip chain, read-only depth, sampled-after-render, copy/readback,
// D3D12 COMMON decay, undo log, and planted-bug hooks.
//
// How: constructs TexState and BufState values and asserts exact BarrierItem
// outputs and rollback invariants.

test "statetrack uniform collapse, split, mip chain, and sampled-after-render" (sys: &System) {
    a := sys.heap.Alloc()
    var barriers := HeapList[BarrierItem].New(a)
    var undo := HeapList[UndoOp].New(a)
    defer {
        barriers.Free(a)
        undo.Free(a)
    }

    // Mip chain: 4 mips, 1 layer, 1 plane.
    var tex := TexState.New(a, 4, 1, 1, ResCommon)
    defer tex.Free(a)

    // Pass 1 renders mip 0 -> RT, then Pass 2 renders mip 1 sampling mip 0.
    try TransitionTexture(a, 1, mut tex, 0, 1, 0, 1, 1, ResRenderTarget, mut barriers, mut undo, PlantedBug.None)
    Assert(!tex.uniform)
    Assert(barriers.Len() == 1)
    Assert(barriers[0].tex.old_state == ResCommon && barriers[0].tex.new_state == ResRenderTarget)
    Assert(barriers[0].tex.first_mip == 0 && !barriers[0].tex.all_sub)

    barriers.Clear()
    // Pass 2: mip 0 -> SRV, mip 1 -> RT.
    try TransitionTexture(a, 1, mut tex, 0, 1, 0, 1, 1, ResShaderRead, mut barriers, mut undo, PlantedBug.None)
    try TransitionTexture(a, 1, mut tex, 1, 1, 0, 1, 1, ResRenderTarget, mut barriers, mut undo, PlantedBug.None)
    Assert(barriers.Len() == 2)
    Assert(barriers[0].tex.first_mip == 0 && barriers[0].tex.old_state == ResRenderTarget && barriers[0].tex.new_state == ResShaderRead)
    Assert(barriers[1].tex.first_mip == 1 && barriers[1].tex.old_state == ResCommon && barriers[1].tex.new_state == ResRenderTarget)

    // Uniform full-texture transition merges back to uniform when all subresources agree.
    barriers.Clear()
    try TransitionTexture(a, 1, mut tex, 0, 4, 0, 1, 1, ResShaderRead, mut barriers, mut undo, PlantedBug.None)
    Assert(tex.uniform && tex.uni_state == ResShaderRead)
    // Only mip 1, 2, 3 differed (mip 0 was already ResShaderRead): 3 barriers emitted.
    Assert(barriers.Len() == 3)

    // Next uniform transition across whole texture uses all_sub = true fast path.
    barriers.Clear()
    try TransitionTexture(a, 1, mut tex, 0, 4, 0, 1, 1, ResCopySrc, mut barriers, mut undo, PlantedBug.None)
    Assert(barriers.Len() == 1 && barriers[0].tex.all_sub)
    Assert(barriers[0].tex.old_state == ResShaderRead && barriers[0].tex.new_state == ResCopySrc)
}

test "statetrack read-only depth combined state and undo log on Abandon" (sys: &System) {
    a := sys.heap.Alloc()
    var barriers := HeapList[BarrierItem].New(a)
    var undo := HeapList[UndoOp].New(a)
    defer {
        barriers.Free(a)
        undo.Free(a)
    }

    // Combined read-only depth + shader read merges cleanly.
    m := MergeTexReadStates(ResDepthRead, ResShaderRead)
    match m {
        some(v) { Assert(v == (ResDepthRead | ResShaderRead)) }
        none { Assert(false) }
    }
    Assert(MergeTexReadStates(ResRenderTarget, ResShaderRead).IsNone())

    var tex := TexState.New(a, 2, 2, 1, ResCommon)
    defer tex.Free(a)

    try TransitionTexture(a, 1, mut tex, 0, 1, 0, 1, 1, ResRenderTarget, mut barriers, mut undo, PlantedBug.None)
    try TransitionTexture(a, 1, mut tex, 1, 1, 1, 1, 1, ResShaderRead, mut barriers, mut undo, PlantedBug.None)
    Assert(!tex.uniform)
    Assert(tex.StateAt(0, 0, 0) == ResRenderTarget)
    Assert(tex.StateAt(0, 1, 1) == ResShaderRead)

    // Replay undo log backwards -> restores uniform ResCommon!
    var i := undo.Len() - 1
    for i >= 0 {
        ApplyTexUndo(mut tex, &undo[i])
        i -= 1
    }
    Assert(tex.uniform && tex.uni_state == ResCommon)
}

test "statetrack D3D12 buffer decay, combined read promotion, UAV barrier, and SkipDecay planted bug" (sys: &System) {
    a := sys.heap.Alloc()
    var barriers := HeapList[BarrierItem].New(a)
    var undo := HeapList[UndoOp].New(a)
    defer {
        barriers.Free(a)
        undo.Free(a)
    }

    var buf := BufState.New(false, ResCommon)
    // Submission 1: read as Vertex then Uniform -> implicit promotion, no D3D12 barrier.
    try TransitionBuffer(a, 1, mut buf, 1, ResVertex, mut barriers, mut undo, PlantedBug.None)
    try TransitionBuffer(a, 1, mut buf, 1, ResUniform, mut barriers, mut undo, PlantedBug.None)
    Assert(barriers.Len() == 0)
    Assert(buf.promoted == (ResVertex | ResUniform))

    // Write after combined reads in same list -> barrier from combined read state to ShaderWrite.
    try TransitionBuffer(a, 1, mut buf, 1, ResShaderWrite, mut barriers, mut undo, PlantedBug.None)
    Assert(barriers.Len() == 1 && !barriers[0].buf.for_vk_only)
    Assert(barriers[0].buf.old_state == (ResVertex | ResUniform) && barriers[0].buf.new_state == ResShaderWrite)

    // Second ShaderWrite in same list -> UAV barrier.
    barriers.Clear()
    try TransitionBuffer(a, 1, mut buf, 1, ResShaderWrite, mut barriers, mut undo, PlantedBug.None)
    Assert(barriers.Len() == 1 && barriers[0].buf.is_uav)

    // Submission 2: decays back to COMMON on D3D12 (read in list 2 needs no D3D12 barrier).
    barriers.Clear()
    try TransitionBuffer(a, 1, mut buf, 2, ResCopySrc, mut barriers, mut undo, PlantedBug.None)
    Assert(buf.list_serial == 2 && !buf.written && buf.promoted == ResCopySrc)
    // Only Vulkan RAW barrier emitted, not D3D12.
    Assert(barriers.Len() == 1 && barriers[0].buf.for_vk_only)

    // Planted bug SkipDecay in Test profile: skips decay to COMMON on list 3.
    barriers.Clear()
    try TransitionBuffer(a, 1, mut buf, 3, ResCopyDst, mut barriers, mut undo, PlantedBug.SkipDecay)
    Assert(buf.list_serial == 2)
    Assert(barriers.Len() == 1 && !barriers[0].buf.for_vk_only)

    // Fixed map_read / map_write buffer never emits transitions.
    var map_buf := BufState.New(true, ResCopyDst)
    barriers.Clear()
    try TransitionBuffer(a, 2, mut map_buf, 1, ResCopyDst, mut barriers, mut undo, PlantedBug.None)
    Assert(barriers.Len() == 0)
}
