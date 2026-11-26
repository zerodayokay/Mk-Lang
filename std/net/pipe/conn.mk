// Why: tests of std's HTTP/2 and HTTP/3 engines need a peer whose buffers are
// small and bounded, so backpressure, blocked writes and concurrent shutdown
// happen deterministically in memory without OS sockets.
//
// What: std.net.pipe's bounded net.Conn pair: DefaultCap, Pair and ConnPair.
//
// How: a sync.Shared[ConnShared] wraps a Mutex[?ConnPairState] and per-side
// read/write Notify signals over two fixed-capacity circular byte buffers. The
// last Abandon frees the buffers back to the Heap.

import std.core
import std.net
import std.rt
import std.sync

/// Default buffer size per direction of a pipe pair (64 KiB).
export const DefaultCap: int = 65536

struct RingBuf {
    buf: HeapList[u8]
    cap: int
    head: int
    len: int
    rshut: bool
    wshut: bool
}

RingBuf.New(a: &Alloc, cap: int): OutOfMemory!RingBuf {
    var b := HeapList[u8].New(a)
    onerr b.Free(a)
    for _ in 0..cap {
        try b.Push(a, 0)
    }
    return RingBuf(buf: b, cap: cap, head: 0, len: 0, rshut: false, wshut: false)
}

RingBuf.Free(self, a: &Alloc) {
    RingBuf(buf, ..) := self
    buf.Free(a)
}

RingBuf.Read(mut self, dst: mut []u8): int {
    n := if dst.Len() < self.len { dst.Len() } else { self.len }
    if n == 0 {
        return 0
    }
    src := self.buf.Items()
    first := self.cap - self.head
    if n <= first {
        (mut dst[0..n]).CopyFrom(src[self.head..self.head + n])
    } else {
        (mut dst[0..first]).CopyFrom(src[self.head..self.cap])
        rem := n - first
        (mut dst[first..n]).CopyFrom(src[0..rem])
    }
    self.head = (self.head + n) % self.cap
    self.len -= n
    return n
}

RingBuf.Write(mut self, data: []u8): int {
    room := self.cap - self.len
    n := if data.Len() < room { data.Len() } else { room }
    if n == 0 {
        return 0
    }
    dst := self.buf.ItemsMut()
    tail := (self.head + self.len) % self.cap
    first := self.cap - tail
    if n <= first {
        (mut dst[tail..tail + n]).CopyFrom(data[0..n])
    } else {
        (mut dst[tail..self.cap]).CopyFrom(data[0..first])
        rem := n - first
        (mut dst[0..rem]).CopyFrom(data[first..n])
    }
    self.len += n
    return n
}

linear view struct ConnPairState {
    a_to_b: RingBuf
    b_to_a: RingBuf
    closed: [2]bool
}

ConnPairState.Free(self, a: &Alloc) {
    ConnPairState(a_to_b, b_to_a, ..) := self
    a_to_b.Free(a)
    b_to_a.Free(a)
}

linear view struct ConnShared {
    st: Mutex[ConnPairState]
    rd_wake: [2]sync.Notify
    wr_wake: [2]sync.Notify
}

ConnShared.Free(self, a: &Alloc) {
    ConnShared(st, rd_wake, wr_wake) := self
    _ = rd_wake
    _ = wr_wake
    st.Into().Free(a)
}

linear view struct PipeEnd {
    side: int
    shared: PipeBox[ConnShared]
    heap: &Heap
}

WakeSignal(n: &sync.Notify) {
    n.NotifyAll()
    n.NotifyOne()
}

PipeEndMarkClosed(sh: &ConnShared, side: int) {
    peer := 1 - side
    var g := sh.st.Lock(core.IoMasked(rt.NativeIo())).Must()
    st := mut *g.Mut()
    st.closed[side] = true
    g.Unlock()
    WakeSignal(&sh.rd_wake[side])
    WakeSignal(&sh.wr_wake[side])
    WakeSignal(&sh.rd_wake[peer])
    WakeSignal(&sh.wr_wake[peer])
}

impl net.Transport for PipeEnd {
    Read(&self, io: Io, buf: mut []u8): net.Error!int {
        if buf.Len() == 0 {
            core.SyncCancel(io) else {
                fail Canceled
            }
            return 0
        }
        sh := self.shared.Ref()
        me := self.side
        peer := 1 - me
        nc := io.NoCancel()
        for {
            core.SyncCancel(io) else {
                fail Canceled
            }
            var g := sh.st.Lock(nc).Must()
            st := mut *g.Mut()
            p_closed := st.closed[peer]
            ring := if me == 0 { mut st.b_to_a } else { mut st.a_to_b }
            if ring.rshut {
                g.Unlock()
                return 0
            }
            if ring.len > 0 {
                n := ring.Read(buf)
                g.Unlock()
                sh.wr_wake[peer].NotifyOne()
                return n
            }
            if ring.wshut || p_closed {
                g.Unlock()
                return 0
            }
            g.Unlock()
            try sh.rd_wake[me].Wait(io)
        }
    }

    WriteAll(&self, io: Io, data: []u8): net.Error!void {
        core.SyncCancel(io) else {
            fail Canceled
        }
        sh := self.shared.Ref()
        me := self.side
        peer := 1 - me
        nc := io.NoCancel()
        if data.Len() == 0 {
            var g := sh.st.Lock(nc).Must()
            st := &*g.Ref()
            p_closed := st.closed[peer]
            ring := if me == 0 { &st.a_to_b } else { &st.b_to_a }
            reset := ring.wshut || ring.rshut || p_closed
            g.Unlock()
            if reset {
                fail net.Reset
            }
            return
        }
        var off := 0
        for off < data.Len() {
            core.SyncCancel(io) else {
                fail Canceled
            }
            var g := sh.st.Lock(nc).Must()
            st := mut *g.Mut()
            p_closed := st.closed[peer]
            ring := if me == 0 { mut st.a_to_b } else { mut st.b_to_a }
            var wrote := 0
            var reset := false
            if ring.wshut || ring.rshut || p_closed {
                reset = true
            } else {
                wrote = ring.Write(data[off..])
            }
            g.Unlock()
            if reset {
                fail net.Reset
            }
            if wrote > 0 {
                off += wrote
                sh.rd_wake[peer].NotifyOne()
                continue
            }
            try sh.wr_wake[me].Wait(io)
        }
    }

    Shutdown(&self, io: Io, how: net.Shutdown): net.Error!void {
        core.SyncCancel(io) else {
            fail Canceled
        }
        sh := self.shared.Ref()
        me := self.side
        peer := 1 - me
        var wake_rd := false
        var wake_wr := false
        var g := sh.st.Lock(io.NoCancel()).Must()
        st := mut *g.Mut()
        match how {
            net.Shutdown.Read {
                if me == 0 {
                    st.b_to_a.rshut = true
                } else {
                    st.a_to_b.rshut = true
                }
                wake_rd = true
            }
            net.Shutdown.Write {
                if me == 0 {
                    st.a_to_b.wshut = true
                } else {
                    st.b_to_a.wshut = true
                }
                wake_wr = true
            }
            net.Shutdown.Both {
                if me == 0 {
                    st.b_to_a.rshut = true
                    st.a_to_b.wshut = true
                } else {
                    st.a_to_b.rshut = true
                    st.b_to_a.wshut = true
                }
                wake_rd = true
                wake_wr = true
            }
        }
        g.Unlock()
        if wake_rd {
            WakeSignal(&sh.rd_wake[me])
            WakeSignal(&sh.wr_wake[peer])
        }
        if wake_wr {
            WakeSignal(&sh.rd_wake[peer])
            WakeSignal(&sh.wr_wake[me])
        }
    }

    PeerAddr(&self): ?net.Addr {
        return none
    }

    Close(&self, io: Io): net.Error!void {
        sh := self.shared.Ref()
        me := self.side
        peer := 1 - me
        var g := sh.st.Lock(io.NoCancel()).Must()
        st := mut *g.Mut()
        st.closed[me] = true
        g.Unlock()
        WakeSignal(&sh.rd_wake[me])
        WakeSignal(&sh.wr_wake[me])
        WakeSignal(&sh.rd_wake[peer])
        WakeSignal(&sh.wr_wake[peer])
    }

    Abandon(self) {
        PipeEnd(side, shared, heap) := self
        PipeEndMarkClosed(shared.Ref(), side)
        match shared.Release(heap) {
            some(cs) {
                cs.Free(heap.Alloc())
            }
            none {
            }
        }
    }
}

/// Creates a connected pair of in-memory net.Conns with DefaultCap (64 KiB)
/// buffered per direction on `h`. Fails with OutOfMemory. O(DefaultCap);
/// allocates the two ring buffers and shared state on `h`.
export Pair(h: &Heap): OutOfMemory!(a: net.Conn, b: net.Conn) from h {
    return try ConnPair(h, DefaultCap)
}

/// Creates a connected pair of in-memory net.Conns with `cap` bytes buffered
/// per direction on `h` (clamped to at least 1). Writes wait while the peer's
/// buffer is full. Fails with OutOfMemory. O(cap); allocates the two ring
/// buffers and shared state on `h`.
export ConnPair(h: &Heap, cap: int): OutOfMemory!(a: net.Conn, b: net.Conn) from h {
    c := if cap < 1 { 1 } else { cap }
    a := h.Alloc()
    s0 := try net.ReserveConn[PipeEnd](h)
    onerr s0.Free(a)
    s1 := try net.ReserveConn[PipeEnd](h)
    onerr s1.Free(a)
    var r0 := try RingBuf.New(a, c)
    onerr r0.Free(a)
    var r1 := try RingBuf.New(a, c)
    onerr r1.Free(a)
    var cs := ConnShared(
        st: Mutex[ConnPairState].New(ConnPairState(a_to_b: r0, b_to_a: r1, closed: [false, false])),
        rd_wake: [sync.Notify.New(), sync.Notify.New()],
        wr_wake: [sync.Notify.New(), sync.Notify.New()],
    )
    onerr cs.Free(a)
    let_box := try PipeBox[ConnShared].New(h, cs)
    e0 := PipeEnd(side: 0, shared: let_box.Clone(), heap: h)
    e1 := PipeEnd(side: 1, shared: let_box, heap: h)
    return (a: s0.Fill(e0), b: s1.Fill(e1))
}
