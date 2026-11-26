// Why: flow-control window arithmetic and server body admission must enforce
// RFC 9113 5.2 and 6.9 overflow, negative-window, credit and FIFO fairness
// invariants without deadlock or integer overflow.
//
// What: table-driven tests for Window (Grow, Adjust, Take, Avail), Credit
// (Buffered, Consumed, Owed), and Admission (Ask, Return, Cancel).
//
// How: exercises boundary values around 0, negative windows, 2^31-1, half
// window consumption, and a large reservation queued ahead of small arrivals.

test "RFC 9113 6.9.2: the connection window starts at 65535 and SETTINGS do not change it" {
    var conn := Window.New(InitialWindow)
    var stream := Window.New(InitialWindow)
    Assert(conn.Avail() == 65535 && stream.Avail() == 65535)
    // SETTINGS_INITIAL_WINDOW_SIZE changes stream windows by delta, leaving conn untouched.
    try stream.Adjust(16384 - 65535)
    Assert(conn.Avail() == 65535 && stream.Avail() == 16384)
}

test "RFC 9113 6.9.1: DATA never exceeds the stream or the connection window" {
    var conn := Window.New(100)
    var stream := Window.New(60)
    // Sending 0-length DATA with END_STREAM succeeds even with empty or negative window.
    var neg := Window.New(-10)
    try neg.Take(0)
    Assert(neg.Avail() == -10)
    try conn.Take(60)
    try stream.Take(60)
    Assert(conn.Avail() == 40 && stream.Avail() == 0)
    match stream.Take(1) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.code == FlowControlError && stream.Avail() == 0) }
    }
}

test "RFC 9113 6.9: DATA beyond our window is a FLOW_CONTROL_ERROR" {
    var conn := Window.New(1000)
    var stream := Window.New(500)
    // Negative byte counts are also rejected.
    match stream.Take(-1) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.code == FlowControlError) }
    }
    match stream.Take(501) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.code == FlowControlError && stream.Avail() == 500) }
    }
    match conn.Take(1001) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.code == FlowControlError && conn.Avail() == 1000) }
    }
}

test "RFC 9113 6.9: no WINDOW_UPDATE of 0 is sent" {
    // Connection credit returns on Buffered (> 0 only).
    var cc := Credit.New(ConnWindow)
    cc.Buffered(0)
    cc.Buffered(-5)
    Assert(cc.Owed() == 0)
    cc.Buffered(4096)
    Assert(cc.Owed() == 4096)
    Assert(cc.Owed() == 0)
    // Stream credit returns at half the window consumed, and never 0.
    var sc := Credit.New(16384)
    sc.Consumed(0)
    Assert(sc.Owed() == 0)
    sc.Consumed(8191)
    Assert(sc.Owed() == 0)
    sc.Consumed(1)
    Assert(sc.Owed() == 8192)
    Assert(sc.Owed() == 0)
    // Tiny window of 1 byte still never emits 0.
    var tiny := Credit.New(1)
    tiny.Consumed(1)
    Assert(tiny.Owed() == 1)
}

test "RFC 9113 6.9: a WINDOW_UPDATE of 0 is a PROTOCOL_ERROR" {
    var w := Window.New(65535)
    match w.Grow(0) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.code == ProtocolError && w.Avail() == 65535) }
    }
    match w.Grow(-10) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.code == ProtocolError && w.Avail() == 65535) }
    }
}

test "RFC 9113 6.9.1: a window over 2^31-1 is a FLOW_CONTROL_ERROR" {
    // Table of (initial, increment, ok, want_avail)
    var w1 := Window.New(MaxWindow - 10)
    try w1.Grow(10)
    Assert(w1.Avail() == MaxWindow)
    match w1.Grow(1) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.code == FlowControlError && w1.Avail() == MaxWindow) }
    }
    var w2 := Window.New(InitialWindow)
    match w2.Grow(MaxWindow) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.code == FlowControlError && w2.Avail() == InitialWindow) }
    }
    // Negative window can grow by MaxWindow without overflowing 2^31-1.
    var w3 := Window.New(-44000)
    try w3.Grow(MaxWindow)
    Assert(w3.Avail() == MaxWindow - 44000)
}

test "RFC 9113 6.9.2: a new INITIAL_WINDOW_SIZE moves every stream window, possibly below 0" {
    // RFC 9113 6.9.2 example: sent 60 KiB from 65535 -> 4095, then initial drops to 16384.
    var w := Window.New(65535)
    try w.Take(61440)
    Assert(w.Avail() == 4095)
    try w.Adjust(16384 - 65535)
    Assert(w.Avail() == -45056)
    // Sending anything while negative fails; growing to positive restores sending.
    match w.Take(1) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.code == FlowControlError) }
    }
    try w.Grow(45057)
    Assert(w.Avail() == 1)
    try w.Take(1)
    Assert(w.Avail() == 0)
}

test "RFC 9113 6.9.2: an INITIAL_WINDOW_SIZE that overflows a stream window is a FLOW_CONTROL_ERROR" {
    var w := Window.New(65535)
    try w.Grow(MaxWindow - 65535)
    match w.Adjust(1) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.code == FlowControlError && w.Avail() == MaxWindow) }
    }
    // Negative delta always succeeds without overflow.
    try w.Adjust(-MaxWindow)
    Assert(w.Avail() == 0)
}

test "admission FIFO admits in order and never starves a queued large request behind smaller ones" (sys: &System) {
    h := sys.heap.Alloc()
    var adm := Admission.New(h, 8000)
    defer adm.Free(h)
    // Stream 1 takes 6000 -> admitted immediately.
    Assert(try adm.Ask(h, 1, 6000) == true)
    Assert(adm.Used() == 6000 && adm.Waiting() == 0)
    // Stream 3 asks for 6000 -> queued.
    Assert(try adm.Ask(h, 3, 6000) == false)
    // Stream 5 asks for 3000 -> queued behind stream 3.
    Assert(try adm.Ask(h, 5, 3000) == false)
    Assert(adm.Used() == 6000 && adm.Waiting() == 2)
    var out: [4]u32 = [0; 4]
    // Stream 1 finishes: frees 6000 -> stream 3 (6000) is admitted; stream 5 (3000) waits.
    n1 := adm.Return(6000, mut out[..])
    Assert(n1 == 1 && out[0] == 3 && adm.Used() == 6000 && adm.Waiting() == 1)
    // Stream 7 asks for 1000 -> fits remaining 2000, but MUST wait behind stream 5 (FIFO).
    Assert(try adm.Ask(h, 7, 1000) == false)
    // Cancel stream 5 -> stream 7 is now at head, waits for stream 3.
    adm.Cancel(5)
    Assert(adm.Waiting() == 1)
    // Stream 3 finishes: stream 7 admitted.
    n2 := adm.Return(6000, mut out[..])
    Assert(n2 == 1 && out[0] == 7 && adm.Used() == 1000 && adm.Waiting() == 0)
    n3 := adm.Return(1000, mut out[..])
    Assert(n3 == 0 && adm.Used() == 0)
}
