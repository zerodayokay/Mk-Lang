// Why: deferred object destruction and stream chunk recycling depend on
// accurate serial clamping and min-heap ordering.
//
// What: unit tests for serial.mk (SerialTracker, DeferredQueue, ChunkPool).
//
// How: pushes out-of-order serials, tests ClampWait and chunk reuse.

test "serial tracker clamp and deferred min-heap ordering" (sys: &System) {
    a := sys.heap.Alloc()
    var st := SerialTracker.New()
    Assert(st.LastSubmitted() == 0)
    Assert(st.ClampWait(5) == 0)

    s1 := st.Advance()
    s2 := st.Advance()
    s3 := st.Advance()
    Assert(s1 == 1 && s2 == 2 && s3 == 3)
    Assert(st.ClampWait(99) == 3)

    var dq := DeferredQueue.New(a)
    defer dq.Free(a)
    try dq.Push(a, DeferredItem(serial: 3, kind: DefBuffer, slot: 30))
    try dq.Push(a, DeferredItem(serial: 1, kind: DefBuffer, slot: 10))
    try dq.Push(a, DeferredItem(serial: 2, kind: DefTexture, slot: 20))
    try dq.Push(a, DeferredItem(serial: 2, kind: DefView, slot: 21))

    Assert(dq.PopReady(0).IsNone())
    st.Complete(1)
    d1 := dq.PopReady(st.completed).Must()
    Assert(d1.slot == 10 && d1.serial == 1)
    Assert(dq.PopReady(st.completed).IsNone())

    st.Complete(3)
    d2 := dq.PopReady(st.completed).Must()
    Assert(d2.serial == 2)
    d3 := dq.PopReady(st.completed).Must()
    Assert(d3.serial == 2)
    d4 := dq.PopReady(st.completed).Must()
    Assert(d4.serial == 3 && d4.slot == 30)
    Assert(dq.Len() == 0)
}

test "chunk pool recycles chunks after serial completes" (sys: &System) {
    a := sys.heap.Alloc()
    var pool := ChunkPool.New(a)
    defer pool.FreeAll(a)

    cid := try pool.AllocChunk(a)
    pool.ReturnInflight(a, 5, cid)
    pool.Recycle(a, 4)
    Assert(pool.free_ids.Len() == 0 && pool.inflight.Len() == 1)
    pool.Recycle(a, 5)
    Assert(pool.free_ids.Len() == 1 && pool.inflight.Len() == 0)
}
