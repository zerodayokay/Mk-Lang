// Why: HTTP/3 and higher-level multiplexed protocols need an in-memory
// net.Mux pair built through net.Mux.Of so tests can exercise flow control,
// stream limits, resets, Wait and backpressure without QUIC or sockets.
//
// What: std.net.pipe's fake MuxEngine pair: MuxPair and MuxPairWith.
//
// How: a sync.Shared[MuxShared] wraps a Mutex[?MuxState] and per-side Notify
// signals (accept, open, wait) plus per-stream StreamWake Notify pairs. Each
// direction buffers up to `cap` bytes with QUIC-style stream IDs and
// peer-bidi explicit Credit rules.

import std.core
import std.net
import std.rt
import std.sync
import std.time

view struct StreamWake {
    rd: sync.Notify
    wr: sync.Notify
}

linear view struct StreamDir {
    buf: HeapList[u8]
    cap: int
    head: int
    len: int
    fin: bool
    reset: bool
    reset_code: u64
    stop: bool
    stop_code: u64
    tx_off: i64
    rx_limit: i64
}

StreamDir.New(a: &Alloc, cap: int): StreamDir {
    return StreamDir(
        buf: HeapList[u8].New(a),
        cap: cap,
        head: 0,
        len: 0,
        fin: false,
        reset: false,
        reset_code: 0,
        stop: false,
        stop_code: 0,
        tx_off: 0,
        rx_limit: i64(cap),
    )
}

StreamDir.Free(self, a: &Alloc) {
    StreamDir(buf, ..) := self
    buf.Free(a)
}

StreamDir.Read(mut self, dst: mut []u8): int {
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

StreamDir.Write(mut self, a: &Alloc, data: []u8): OutOfMemory!int {
    if self.buf.Len() < self.cap {
        for self.buf.Len() < self.cap {
            try self.buf.Push(a, 0)
        }
    }
    room := self.cap - self.len
    credit := self.rx_limit - self.tx_off
    avail := if credit < i64(room) { if credit < 0 { 0 } else { int(credit) } } else { room }
    n := if data.Len() < avail { data.Len() } else { avail }
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
    self.tx_off += i64(n)
    return n
}

// A single stream. d0_to_1 carries side 0's writes to side 1; d1_to_0 carries
// side 1's writes to side 0.
linear view struct PipeStream {
    id: i64
    gen_: u32
    uni: bool
    initiator: int
    accepted: bool
    released: [2]bool
    d0_to_1: StreamDir
    d1_to_0: StreamDir
    wake: sync.Shared[StreamWake]
}

PipeStream.Free(self, a: &Alloc) {
    PipeStream(d0_to_1, d1_to_0, wake, ..) := self
    d0_to_1.Free(a)
    d1_to_0.Free(a)
    wake.Free(rt.ProcessHeap())
}

PipeStream.IsReadable(&self, me: int): bool {
    in_d := if me == 0 { &self.d1_to_0 } else { &self.d0_to_1 }
    return in_d.len > 0 || in_d.fin || in_d.reset
}

linear view struct MuxState {
    streams: HeapList[PipeStream]
    cap: int
    next_bidi: [2]i64
    next_uni: [2]i64
    max_bidi: [2]i64
    max_uni: [2]i64
    opened_bidi: [2]i64
    opened_uni: [2]i64
    closed: bool
    close_code: u64
    close_by: int
}

MuxState.Free(self, a: &Alloc) {
    MuxState(streams, ..) := self
    var s := streams
    for {
        p := s.Pop() else {
            break
        }
        p.Free(a)
    }
    s.FreeEmpty(a)
}

linear view struct MuxShared {
    st: Mutex[MuxState]
    accept_bidi: [2]sync.Notify
    accept_uni: [2]sync.Notify
    open_bidi: [2]sync.Notify
    open_uni: [2]sync.Notify
    wait_ev: [2]sync.Notify
}

MuxShared.Free(self, a: &Alloc) {
    MuxShared(st, accept_bidi, accept_uni, open_bidi, open_uni, wait_ev) := self
    _ = accept_bidi
    _ = accept_uni
    _ = open_bidi
    _ = open_uni
    _ = wait_ev
    st.Into().Free(a)
}

linear view struct PipeMux {
    side: int
    shared: PipeBox[MuxShared]
    heap: &Heap
}

WakeAllMux(sh: &MuxShared, st: &MuxState) {
    for i in 0..2 {
        WakeSignal(&sh.accept_bidi[i])
        WakeSignal(&sh.accept_uni[i])
        WakeSignal(&sh.open_bidi[i])
        WakeSignal(&sh.open_uni[i])
        WakeSignal(&sh.wait_ev[i])
    }
    ss := st.streams.Items()
    for i in 0..ss.Len() {
        w := ss[i].wake.Ref()
        WakeSignal(&w.rd)
        WakeSignal(&w.wr)
    }
}

FindStream(st: &MuxState, id: i64, gen_: u32): int {
    ss := st.streams.Items()
    for i in 0..ss.Len() {
        if ss[i].id == id && ss[i].gen_ == gen_ {
            return i
        }
    }
    return -1
}

FindStreamById(st: &MuxState, id: i64): int {
    ss := st.streams.Items()
    for i in 0..ss.Len() {
        if ss[i].id == id {
            return i
        }
    }
    return -1
}

ClosedErr(st: &MuxState, me: int): net.Error {
    return net.MuxClosed(code: st.close_code, app: true, peer: st.close_by != me)
}

PipeMuxReadStep(sh: &MuxShared, me: int, nc: Io, id: i64, gen_: u32, buf: mut []u8, wait: bool): net.Error!(done: bool, n: ?int, wake: ?sync.Shared[StreamWake]) from sh {
    var g := sh.st.Lock(nc).Must()
    st := mut *g.Mut()
    if st.closed {
        err := ClosedErr(&*st, me)
        g.Unlock()
        fail err
    }
    idx := FindStream(&*st, id, gen_)
    if idx < 0 {
        g.Unlock()
        fail net.StreamReset(code: 0)
    }
    ss := st.streams.ItemsMut()
    s := mut ss[idx]
    in_d := if me == 0 { mut s.d1_to_0 } else { mut s.d0_to_1 }
    if in_d.len > 0 {
        if buf.Len() == 0 {
            g.Unlock()
            return (done: true, n: some(0), wake: none)
        }
        n := in_d.Read(buf)
        if s.initiator == me || s.uni {
            in_d.rx_limit += i64(n)
        }
        w := s.wake.Clone()
        g.Unlock()
        w.Ref().wr.NotifyOne()
        w.Free(rt.ProcessHeap())
        return (done: true, n: some(n), wake: none)
    }
    if in_d.reset {
        code := in_d.reset_code
        g.Unlock()
        fail net.StreamReset(code: code)
    }
    if in_d.fin {
        g.Unlock()
        return (done: true, n: some(0), wake: none)
    }
    if !wait {
        g.Unlock()
        return (done: true, n: none, wake: none)
    }
    w := s.wake.Clone()
    g.Unlock()
    return (done: false, n: none, wake: some(w))
}

PipeMuxWriteStep(sh: &MuxShared, me: int, peer: int, a: &Alloc, nc: Io, id: i64, gen_: u32, data: []u8, off: int, fin: bool): net.Error!(done: bool, off: int, wake: ?sync.Shared[StreamWake]) from sh {
    var g := sh.st.Lock(nc).Must()
    st := mut *g.Mut()
    if st.closed {
        err := ClosedErr(&*st, me)
        g.Unlock()
        fail err
    }
    idx := FindStream(&*st, id, gen_)
    if idx < 0 {
        g.Unlock()
        fail net.StreamReset(code: 0)
    }
    ss := st.streams.ItemsMut()
    s := mut ss[idx]
    out_d := if me == 0 { mut s.d0_to_1 } else { mut s.d1_to_0 }
    if out_d.stop {
        code := out_d.stop_code
        g.Unlock()
        fail net.StreamReset(code: code)
    }
    if out_d.reset {
        code := out_d.reset_code
        g.Unlock()
        fail net.StreamReset(code: code)
    }
    if off < data.Len() {
        n := out_d.Write(a, data[off..]) else {
            g.Unlock()
            fail OutOfMemory
        }
        if n > 0 {
            next_off := off + n
            if next_off == data.Len() && fin {
                out_d.fin = true
            }
            w := s.wake.Clone()
            g.Unlock()
            w.Ref().rd.NotifyOne()
            w.Free(rt.ProcessHeap())
            sh.wait_ev[peer].NotifyOne()
            return (done: next_off == data.Len(), off: next_off, wake: none)
        }
        w := s.wake.Clone()
        g.Unlock()
        return (done: false, off: off, wake: some(w))
    }
    if fin {
        out_d.fin = true
    }
    w := s.wake.Clone()
    g.Unlock()
    w.Ref().rd.NotifyOne()
    w.Free(rt.ProcessHeap())
    sh.wait_ev[peer].NotifyOne()
    return (done: true, off: off, wake: none)
}

PipeMuxMarkClosed(sh: &MuxShared, me: int) {
    var g := sh.st.Lock(core.IoMasked(rt.NativeIo())).Must()
    st := mut *g.Mut()
    if !st.closed {
        st.closed = true
        st.close_code = 0
        st.close_by = me
    }
    WakeAllMux(sh, &*st)
    g.Unlock()
}

impl net.MuxEngine for PipeMux {
    Open(&self, io: Io, uni: bool, wait: bool): net.Error!?(id: i64, gen_: u32) {
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
            if st.closed {
                err := ClosedErr(&*st, me)
                g.Unlock()
                fail err
            }
            can := if uni { st.opened_uni[me] < st.max_uni[me] } else { st.opened_bidi[me] < st.max_bidi[me] }
            if can {
                seq := if uni { st.next_uni[me] } else { st.next_bidi[me] }
                id := seq * 4 + i64(me) + (if uni { 2 } else { 0 })
                if uni {
                    st.next_uni[me] += 1
                    st.opened_uni[me] += 1
                } else {
                    st.next_bidi[me] += 1
                    st.opened_bidi[me] += 1
                }
                sw := sync.Shared[StreamWake].New(rt.ProcessHeap(), StreamWake(rd: sync.Notify.New(), wr: sync.Notify.New())) else {
                    g.Unlock()
                    fail OutOfMemory
                }
                a := self.heap.Alloc()
                ps := PipeStream(
                    id: id,
                    gen_: 1,
                    uni: uni,
                    initiator: me,
                    accepted: false,
                    released: [false, false],
                    d0_to_1: StreamDir.New(a, st.cap),
                    d1_to_0: StreamDir.New(a, st.cap),
                    wake: sw,
                )
                st.streams.Push(a, ps) else {
                    ps.Free(a)
                    g.Unlock()
                    fail OutOfMemory
                }
                g.Unlock()
                if uni {
                    sh.accept_uni[peer].NotifyOne()
                } else {
                    sh.accept_bidi[peer].NotifyOne()
                }
                sh.wait_ev[peer].NotifyOne()
                return some((id: id, gen_: 1))
            }
            if !wait {
                g.Unlock()
                return none
            }
            g.Unlock()
            if uni {
                try sh.open_uni[me].Wait(io)
            } else {
                try sh.open_bidi[me].Wait(io)
            }
        }
    }

    Accept(&self, io: Io, uni: bool, wait: bool): net.Error!?(id: i64, gen_: u32) {
        sh := self.shared.Ref()
        me := self.side
        nc := io.NoCancel()
        for {
            core.SyncCancel(io) else {
                fail Canceled
            }
            var g := sh.st.Lock(nc).Must()
            st := mut *g.Mut()
            if st.closed {
                err := ClosedErr(&*st, me)
                g.Unlock()
                fail err
            }
            ss := st.streams.ItemsMut()
            for i in 0..ss.Len() {
                if ss[i].initiator != me && ss[i].uni == uni && !ss[i].accepted {
                    ss[i].accepted = true
                    id := ss[i].id
                    gen_ := ss[i].gen_
                    g.Unlock()
                    return some((id: id, gen_: gen_))
                }
            }
            if !wait {
                g.Unlock()
                return none
            }
            g.Unlock()
            if uni {
                try sh.accept_uni[me].Wait(io)
            } else {
                try sh.accept_bidi[me].Wait(io)
            }
        }
    }

    Read(&self, io: Io, id: i64, gen_: u32, buf: mut []u8, wait: bool): net.Error!?int {
        sh := self.shared.Ref()
        me := self.side
        nc := io.NoCancel()
        for {
            core.SyncCancel(io) else {
                fail Canceled
            }
            (done, n, wake) := try PipeMuxReadStep(sh, me, nc, id, gen_, buf, wait)
            if done {
                match wake {
                    some(w) { w.Free(rt.ProcessHeap()) }
                    none {}
                }
                return n
            }
            match wake {
                some(w) {
                    w.Ref().rd.Wait(io) else {
                        w.Free(rt.ProcessHeap())
                        fail Canceled
                    }
                    w.Free(rt.ProcessHeap())
                }
                none {}
            }
        }
    }

    Write(&self, io: Io, id: i64, gen_: u32, data: []u8, fin: bool): net.Error!void {
        sh := self.shared.Ref()
        me := self.side
        peer := 1 - me
        nc := io.NoCancel()
        a := self.heap.Alloc()
        var off := 0
        for {
            core.SyncCancel(io) else {
                fail Canceled
            }
            (done: done, off: next_off, wake: wake) := try PipeMuxWriteStep(sh, me, peer, a, nc, id, gen_, data, off, fin)
            off = next_off
            if done {
                match wake {
                    some(w) { w.Free(rt.ProcessHeap()) }
                    none {}
                }
                return
            }
            match wake {
                some(w) {
                    w.Ref().wr.Wait(io) else {
                        w.Free(rt.ProcessHeap())
                        fail Canceled
                    }
                    w.Free(rt.ProcessHeap())
                }
                none {}
            }
        }
    }

    Credit(&self, id: i64, gen_: u32, total: i64) {
        sh := self.shared.Ref()
        me := self.side
        var g := sh.st.Lock(core.IoMasked(rt.NativeIo())).Must()
        st := mut *g.Mut()
        idx := FindStream(&*st, id, gen_)
        if idx >= 0 {
            ss := st.streams.ItemsMut()
            s := mut ss[idx]
            in_d := if me == 0 { mut s.d1_to_0 } else { mut s.d0_to_1 }
            if total > in_d.rx_limit {
                in_d.rx_limit = total
                w := s.wake.Clone()
                g.Unlock()
                w.Ref().wr.NotifyOne()
                w.Free(rt.ProcessHeap())
                return
            }
        }
        g.Unlock()
    }

    Wait(&self, io: Io, ids: []i64, incoming: bool): net.Error!net.MuxReady {
        sh := self.shared.Ref()
        me := self.side
        nc := io.NoCancel()
        for {
            core.SyncCancel(io) else {
                fail Canceled
            }
            var g := sh.st.Lock(nc).Must()
            st := &*g.Ref()
            if st.closed {
                err := ClosedErr(st, me)
                g.Unlock()
                fail err
            }
            ss := st.streams.Items()
            for k in 0..ids.Len() {
                idx := FindStreamById(st, ids[k])
                if idx >= 0 && ss[idx].IsReadable(me) {
                    g.Unlock()
                    return net.MuxReady.Readable(index: k)
                }
            }
            if incoming {
                for i in 0..ss.Len() {
                    if ss[i].initiator != me && !ss[i].accepted {
                        uni := ss[i].uni
                        g.Unlock()
                        return net.MuxReady.Incoming(uni: uni)
                    }
                }
            }
            g.Unlock()
            try sh.wait_ev[me].Wait(io)
        }
    }

    Reset(&self, id: i64, gen_: u32, code: u64) {
        sh := self.shared.Ref()
        me := self.side
        peer := 1 - me
        var g := sh.st.Lock(core.IoMasked(rt.NativeIo())).Must()
        st := mut *g.Mut()
        idx := FindStream(&*st, id, gen_)
        if idx >= 0 {
            ss := st.streams.ItemsMut()
            s := mut ss[idx]
            out_d := if me == 0 { mut s.d0_to_1 } else { mut s.d1_to_0 }
            out_d.reset = true
            out_d.reset_code = code
            w := s.wake.Clone()
            g.Unlock()
            WakeSignal(&w.Ref().rd)
            WakeSignal(&w.Ref().wr)
            w.Free(rt.ProcessHeap())
            sh.wait_ev[peer].NotifyOne()
            return
        }
        g.Unlock()
    }

    Stop(&self, id: i64, gen_: u32, code: u64) {
        sh := self.shared.Ref()
        me := self.side
        var g := sh.st.Lock(core.IoMasked(rt.NativeIo())).Must()
        st := mut *g.Mut()
        idx := FindStream(&*st, id, gen_)
        if idx >= 0 {
            ss := st.streams.ItemsMut()
            s := mut ss[idx]
            in_d := if me == 0 { mut s.d1_to_0 } else { mut s.d0_to_1 }
            in_d.stop = true
            in_d.stop_code = code
            w := s.wake.Clone()
            g.Unlock()
            WakeSignal(&w.Ref().wr)
            w.Free(rt.ProcessHeap())
            return
        }
        g.Unlock()
    }

    Release(&self, id: i64, gen_: u32) {
        sh := self.shared.Ref()
        me := self.side
        peer := 1 - me
        var g := sh.st.Lock(core.IoMasked(rt.NativeIo())).Must()
        st := mut *g.Mut()
        var repl_uni := false
        var repl_bidi := false
        idx := FindStream(&*st, id, gen_)
        if idx >= 0 {
            ss := st.streams.ItemsMut()
            s := mut ss[idx]
            if !s.released[me] {
                s.released[me] = true
                if s.initiator != me {
                    if s.uni {
                        st.max_uni[peer] += 1
                        repl_uni = true
                    } else {
                        st.max_bidi[peer] += 1
                        repl_bidi = true
                    }
                }
            }
        }
        g.Unlock()
        if repl_uni {
            sh.open_uni[peer].NotifyOne()
        }
        if repl_bidi {
            sh.open_bidi[peer].NotifyOne()
        }
    }

    Limits(&self): net.MuxLimits {
        sh := self.shared.Ref()
        me := self.side
        var g := sh.st.Lock(core.IoMasked(rt.NativeIo())).Must()
        st := &*g.Ref()
        var bidi := st.max_bidi[me] - st.opened_bidi[me]
        var uni := st.max_uni[me] - st.opened_uni[me]
        if bidi < 0 {
            bidi = 0
        }
        if uni < 0 {
            uni = 0
        }
        g.Unlock()
        return net.MuxLimits(bidi: bidi, uni: uni, idle: time.Seconds(30))
    }

    Usable(&self, io: Io): bool {
        _ = io
        sh := self.shared.Ref()
        var g := sh.st.Lock(core.IoMasked(rt.NativeIo())).Must()
        ok_ := !(&*g.Ref()).closed
        g.Unlock()
        return ok_
    }

    PeerAddr(&self): ?net.Addr {
        return none
    }

    Close(&self, io: Io, code: u64): net.Error!void {
        core.SyncCancel(io) else {
            fail Canceled
        }
        sh := self.shared.Ref()
        me := self.side
        var g := sh.st.Lock(io.NoCancel()).Must()
        st := mut *g.Mut()
        if !st.closed {
            st.closed = true
            st.close_code = code
            st.close_by = me
        }
        WakeAllMux(sh, &*st)
        g.Unlock()
    }

    Abandon(self) {
        PipeMux(side, shared, heap) := self
        PipeMuxMarkClosed(shared.Ref(), side)
        match shared.Release(heap) {
            some(ms) {
                ms.Free(heap.Alloc())
            }
            none {}
        }
    }
}

/// Creates a connected pair of in-memory net.Mux connections with `cap` bytes
/// buffered per stream direction on `h` and default stream limits (64 bidi,
/// 64 uni). Fails with OutOfMemory. O(1); allocates shared state on `h`.
export MuxPair(h: &Heap, cap: int): OutOfMemory!(client: net.Mux, server: net.Mux) from h {
    return try MuxPairWith(h, 64, 64, cap)
}

/// Creates a connected pair of in-memory net.Mux connections with initial
/// stream limits (`bidi`, `uni`) and `cap` bytes buffered per stream direction
/// on `h`. Fails with OutOfMemory. O(1); allocates shared state on `h`.
export MuxPairWith(h: &Heap, bidi: i64, uni: i64, cap: int): OutOfMemory!(client: net.Mux, server: net.Mux) from h {
    c := if cap < 1 { 1 } else { cap }
    b := if bidi < 0 { 0 } else { bidi }
    u := if uni < 0 { 0 } else { uni }
    a := h.Alloc()
    var empty0: ?PipeMux = none
    b0 := Box[?PipeMux].New(a, empty0) else e {
        if p := empty0 { p.Abandon() }
        fail e
    }
    onerr {
        if p := b0.Take(a) { p.Abandon() }
    }
    var empty1: ?PipeMux = none
    b1 := Box[?PipeMux].New(a, empty1) else e {
        if p := empty1 { p.Abandon() }
        fail e
    }
    onerr {
        if p := b1.Take(a) { p.Abandon() }
    }
    var mst := MuxState(
        streams: HeapList[PipeStream].New(a),
        cap: c,
        next_bidi: [0, 0],
        next_uni: [0, 0],
        max_bidi: [b, b],
        max_uni: [u, u],
        opened_bidi: [0, 0],
        opened_uni: [0, 0],
        closed: false,
        close_code: 0,
        close_by: 0,
    )
    onerr mst.Free(a)
    var msh := MuxShared(
        st: Mutex[MuxState].New(mst),
        accept_bidi: [sync.Notify.New(), sync.Notify.New()],
        accept_uni: [sync.Notify.New(), sync.Notify.New()],
        open_bidi: [sync.Notify.New(), sync.Notify.New()],
        open_uni: [sync.Notify.New(), sync.Notify.New()],
        wait_ev: [sync.Notify.New(), sync.Notify.New()],
    )
    onerr msh.Free(a)
    sh_box := try PipeBox[MuxShared].New(h, msh)
    p0 := PipeMux(side: 0, shared: sh_box.Clone(), heap: h)
    p1 := PipeMux(side: 1, shared: sh_box, heap: h)
    e0 := core.SealedBox.Fill[PipeMux](b0, p0)
    e1 := core.SealedBox.Fill[PipeMux](b1, p1)
    m0 := net.Mux.Of[PipeMux](h, e0)
    m1 := net.Mux.Of[PipeMux](h, e1)
    return (client: m0, server: m1)
}
