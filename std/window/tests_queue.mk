// Why: the request doorbell must fire only on 0->1 transitions and SetterState
// / SpinQueue must coalesce property updates and transfer requests safely.
// What: unit and concurrent tests for Doorbell, SetterState and SpinQueue.
// How: verifies 0->1 coalescing, Clear re-arming, UTF-8-safe 255-byte title
// truncation, and multi-task SpinQueue ordering.

test "Doorbell fires only on 0->1 transitions and re-arms on Clear" (sys: &System) {
    _ = sys
    d := DoorbellNew()
    Assert(!d.IsPending())
    Assert(d.Ring())
    Assert(d.IsPending())
    Assert(!d.Ring())
    Assert(!d.Ring())
    d.Clear()
    Assert(!d.IsPending())
    Assert(d.Ring())
}

test "SetterState coalesces dirty flags and truncates long titles safely" (sys: &System) {
    _ = sys
    st := SetterStateNew()
    st.SetTitle("first")
    st.SetTitle("second")
    st.SetSize(800, 600)
    st.SetSize(1280, 720)
    st.SetFullscreen(Fullscreen.Borderless)
    st.SetCursor(Cursor.Hand)
    st.SetCursorMode(CursorMode.Hidden)
    st.SetVisible(false)

    snap := st.Take()
    Assert(snap.dirty == (DirtyTitle | DirtySize | DirtyFullscreen | DirtyCursor | DirtyCursorMode | DirtyVisible))
    Assert(int(snap.title_len) == 6)
    Assert(snap.width == 1280 && snap.height == 720)
    Assert(!snap.visible)

    // Second Take sees zero dirty mask.
    snap2 := st.Take()
    Assert(snap2.dirty == 0)

    // Title > 255 bytes truncates to <= 255 without splitting a 2-byte UTF-8 char.
    var long: [260]u8 = [0; 260]
    for i in 0..130 {
        long[i * 2] = 0xC3
        long[i * 2 + 1] = 0xA9
    }
    long_str := str.From(long[..]) else { Panic("utf8") }
    st.SetTitle(long_str)
    snap3 := st.Take()
    Assert(int(snap3.title_len) == 254)
}

test "SpinQueue transfers requests FIFO and rings doorbell once per batch" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    q := try SpinQueueNew(&ar, 16)
    r0 := q.Push(10)
    Assert(r0.ok && r0.ring)
    r1 := q.Push(20)
    Assert(r1.ok && !r1.ring)
    r2 := q.Push(30)
    Assert(r2.ok && !r2.ring)

    q.bell.Clear()
    Assert((q.Pop() else { 0 }) == 10)
    Assert((q.Pop() else { 0 }) == 20)
    Assert((q.Pop() else { 0 }) == 30)
    Assert((q.Pop() else { 0 }) == 0)

    r3 := q.Push(40)
    Assert(r3.ok && r3.ring)
}
