// Why: each HTTP/3 request/response stream carries a state machine of HEADERS,
// DATA, optional trailers, and unknown/GREASE frames over one bidirectional
// net.MuxStream, enforcing RFC 9114 4.1 framing, Content-Length, and stream vs
// connection error boundaries.
//
// What: Exchange, with Open, Accept, Head, RequestHead, Read, Respond, Write,
// Id, Cancel, Reset, StopReading, and Release.
//
// How: Exchange borrows &Session and serializes its per-stream state behind a
// Mutex without holding Session's state lock across Mux I/O; small headers and
// DATA frame headers encode on the stack, and DATA payloads read and write
// directly into the caller's slices without intermediate copies.

import std.core
import hf = std.http.fields
import std.net
import std.rt

// Request-stream receive states.
const RxWantHead = 0
const RxBody = 1
const RxTrailersDone = 2
const RxDone = 3

// Flow-control credit window kept ahead on server-accepted bidi streams.
const ServerRxWindow: i64 = 16384

// ExState holds one Exchange's framing and buffer state.
struct ExState {
    rx_state: int
    saw_interim: bool
    tx_final: bool
    tx_fin: bool
    rx_fin_seen: bool
    rx_buf: [1024]u8
    rx_pos: int
    rx_end: int
    in_frame: bool
    data_rem: u64
    skip_rem: u64
    body_read: i64
    rx_total: i64
    rx_credited: i64
    expected_cl: ?i64
    body_exempt: bool
    released: bool
}

/// One HTTP/3 request/response stream's framing (RFC 9114 4.1). Linear:
/// release with Release. Send and Share.
export linear view struct Exchange {
    sess: &Session
    stream: net.MuxStream
    req_was_head: bool
    st: Mutex[ExState]
}

// NewExchange wraps a bidirectional MuxStream in an Exchange.
NewExchange(s: &Session, stream: net.MuxStream, tx_fin: bool, req_was_head: bool): Exchange from s {
    ex := ExState(
        rx_state: RxWantHead,
        saw_interim: false,
        tx_final: false,
        tx_fin: tx_fin,
        rx_fin_seen: false,
        rx_buf: [0; 1024],
        rx_pos: 0,
        rx_end: 0,
        in_frame: false,
        data_rem: 0,
        skip_rem: 0,
        body_read: 0,
        rx_total: 0,
        rx_credited: 0,
        expected_cl: none,
        body_exempt: false,
        released: false,
    )
    return Exchange(
        sess: s,
        stream: stream,
        req_was_head: req_was_head,
        st: Mutex[ExState].New(ex),
    )
}

// FailStream aborts open halves of the stream with code and returns Violation.
Exchange.FailStream(&self, code: u64): Violation {
    var g := self.st.Lock(core.IoMasked(rt.NativeIo())).Must()
    es := mut *g.Mut()
    if !es.tx_fin {
        self.sess.m.Reset(&self.stream, code)
        es.tx_fin = true
    }
    if !es.rx_fin_seen {
        self.sess.m.Stop(&self.stream, code)
        es.rx_fin_seen = true
    }
    g.Unlock()
    self.sess.CancelQpackStream(self.stream.Id())
    return Violation(code: code, stream: self.stream.Id())
}

// MaybeCreditFields tops up stream receive credit on server-accepted bidi streams.
Exchange.MaybeCreditFields(&self, rx_fin: bool, rx_total: i64, rx_credited: mut i64) {
    if self.sess.is_server && !rx_fin {
        want := rx_total + ServerRxWindow
        if want > *rx_credited {
            self.sess.m.Credit(&self.stream, want)
            *rx_credited = want
        }
    }
}

// ReadStreamFields reads into dst and updates rx_fin_seen/rx_total/rx_credited
// without borrowing es.rx_buf's parent struct as a whole.
Exchange.ReadStreamFields(&self, io: Io, rx_fin: mut bool, rx_total: mut i64, rx_credited: mut i64, dst: mut []u8): Error!int {
    self.MaybeCreditFields(*rx_fin, *rx_total, rx_credited)
    n := (self.sess.m.Read(io, &self.stream, dst, true) else e {
        fail NormMuxErr(e)
    }) else {
        return 0
    }
    if n == 0 {
        *rx_fin = true
    } else {
        *rx_total += i64(n)
        self.MaybeCreditFields(*rx_fin, *rx_total, rx_credited)
    }
    return n
}

// DrainSkip skips es.skip_rem bytes (e.g., unknown/GREASE frame payload).
Exchange.DrainSkip(&self, io: Io, es: mut ExState): Error!void {
    for es.skip_rem > 0 {
        avail := Cast[u64](es.rx_end - es.rx_pos)
        if avail > 0 {
            take := if es.skip_rem < avail { Cast[int](es.skip_rem) } else { Cast[int](avail) }
            es.rx_pos += take
            es.skip_rem -= Cast[u64](take)
            continue
        }
        es.rx_pos = 0
        es.rx_end = 0
        n := try self.ReadStreamFields(io, mut es.rx_fin_seen, mut es.rx_total, mut es.rx_credited, mut es.rx_buf[..])
        if n == 0 {
            fail self.sess.RecordFatal(io, H3FrameError)
        }
        es.rx_end = n
    }
    es.in_frame = false
}

// ReadExact reads exactly dst.Len() bytes from es.rx_buf then stream.
Exchange.ReadExact(&self, io: Io, es: mut ExState, dst: mut []u8): Error!void {
    var off := 0
    for off < dst.Len() {
        avail := es.rx_end - es.rx_pos
        if avail > 0 {
            need := dst.Len() - off
            take := if avail < need { avail } else { need }
            (mut dst[off..off + take]).CopyFrom(es.rx_buf[es.rx_pos..es.rx_pos + take])
            es.rx_pos += take
            off += take
            continue
        }
        es.rx_pos = 0
        es.rx_end = 0
        n := try self.ReadStreamFields(io, mut es.rx_fin_seen, mut es.rx_total, mut es.rx_credited, mut dst[off..])
        if n == 0 {
            fail self.sess.RecordFatal(io, H3FrameError)
        }
        off += n
    }
}

// ReadOneVarint reads a single 1..8-byte QUIC varint from es.
Exchange.ReadOneVarint(&self, io: Io, es: mut ExState): Error!u64 {
    var tmp: [8]u8 = [0; 8]
    try self.ReadExact(io, es, mut tmp[0..1])
    need := 1 << int(tmp[0] >> 6)
    if need > 1 {
        try self.ReadExact(io, es, mut tmp[1..need])
    }
    return ReadVarint(tmp[0..need], 0).Must().v
}

// NextFrame reads the next HTTP/3 frame header on the request stream,
// skipping unknown/GREASE frames and rejecting illegal frame types.
// Returns none on clean FIN at a frame boundary.
Exchange.NextFrame(&self, io: Io, es: mut ExState): Error!?FrameHeader {
    for {
        try self.DrainSkip(io, es)
        avail := es.rx_end - es.rx_pos
        if es.rx_pos > 0 && avail > 0 {
            for i in 0..avail {
                es.rx_buf[i] = es.rx_buf[es.rx_pos + i]
            }
        }
        es.rx_pos = 0
        es.rx_end = avail

        var hdr: ?FrameHeader = none
        for {
            if es.rx_end > 0 {
                es.in_frame = true
                if h := ParseFrameHeader(es.rx_buf[0..es.rx_end]) {
                    hdr = some(h)
                    break
                }
            }
            if es.rx_fin_seen {
                if es.rx_end > 0 {
                    fail self.sess.RecordFatal(io, H3FrameError)
                }
                return none
            }
            n := try self.ReadStreamFields(io, mut es.rx_fin_seen, mut es.rx_total, mut es.rx_credited, mut es.rx_buf[es.rx_end..])
            if n == 0 {
                if es.rx_end > 0 {
                    fail self.sess.RecordFatal(io, H3FrameError)
                }
                return none
            }
            es.rx_end += n
        }

        fh := hdr.Must()
        es.rx_pos = fh.header_len
        es.in_frame = fh.length > 0

        if IsReservedH2Frame(fh.kind) || fh.kind == FrameCancelPush || fh.kind == FrameSettings || fh.kind == FrameGoaway || fh.kind == FrameMaxPushId {
            fail self.sess.RecordFatal(io, H3FrameUnexpected)
        }
        if fh.kind == FramePushPromise {
            if self.sess.is_server {
                fail self.sess.RecordFatal(io, H3FrameUnexpected)
            }
            if fh.length == 0 {
                fail self.sess.RecordFatal(io, H3FrameError)
            }
            start_total := es.rx_total - i64(es.rx_end - es.rx_pos)
            _ = try self.ReadOneVarint(io, es)
            consumed := (es.rx_total - i64(es.rx_end - es.rx_pos)) - start_total
            if Cast[u64](consumed) > fh.length {
                fail self.sess.RecordFatal(io, H3FrameError)
            }
            fail self.sess.RecordFatal(io, H3IdError)
        }
        if fh.kind != FrameData && fh.kind != FrameHeaders {
            es.skip_rem = fh.length
            continue
        }
        return some(fh)
    }
}

// MaxHeadersWire returns the hard wire limit for a single HEADERS payload.
MaxHeadersWire(header_bytes: int): u64 {
    if header_bytes <= 0 {
        return 65536
    }
    w := header_bytes * 4 + 16384
    if w < 65536 {
        return 65536
    }
    if w > 4 * 1024 * 1024 {
        return 4 * 1024 * 1024
    }
    return Cast[u64](w)
}

// DecodeQpackBytes decodes a HEADERS payload into block (using QDecoder's
// allocator a_qdec), waiting for encoder-stream inserts if blocked.
// Returns (too_large, stream_err_code).
Exchange.DecodeQpackBytes(&self, io: Io, a_qdec: &Alloc, block: mut hf.FieldBlock, payload: []u8): Error!(too_large: bool, stream_code: u64) {
    sid := self.stream.Id()
    for {
        var g := self.sess.state_mu.Lock(io.NoCancel()).Must()
        st := mut *g.Mut()
        if fe := st.fatal_err {
            g.Unlock()
            fail fe
        }
        if st.closed {
            g.Unlock()
            fail Closed
        }
        res := st.qdec.Decode(a_qdec, block, sid, payload) else e {
            g.Unlock()
            match e {
                hf.BadInteger(offset: _) {
                    return (too_large: false, stream_code: QpackDecompressionFailed)
                }
                OutOfMemory { fail OutOfMemory }
                _ { fail self.sess.RecordFatal(io, QpackDecompressionFailed) }
            }
        }
        has_qout := st.has_qdec_out
        is_serving := st.serving
        g.Unlock()

        match res {
            hf.QOutcome.Fits {
                if has_qout {
                    try self.sess.FlushQpackDecoder(io)
                }
                return (too_large: false, stream_code: 0)
            }
            hf.QOutcome.TooLarge {
                if has_qout {
                    try self.sess.FlushQpackDecoder(io)
                }
                return (too_large: true, stream_code: 0)
            }
            hf.QOutcome.Blocked {
                try self.sess.Poll(io)
                if is_serving {
                    try self.sess.qpack_ev.Wait(io)
                } else {
                    try self.sess.WaitStep(io, false)
                }
                block.Reset()
            }
        }
    }
}

// DecodeHeadersPayload reads fh.length bytes and QPACK-decodes them into block.
Exchange.DecodeHeadersPayload(&self, io: Io, es: mut ExState, fh: FrameHeader, a_qdec: &Alloc, block: mut hf.FieldBlock): Error!(too_large: bool, stream_code: u64) {
    if fh.length > MaxHeadersWire(self.sess.header_bytes) {
        fail self.sess.RecordFatal(io, H3ExcessiveLoad)
    }
    plen := Cast[int](fh.length)
    if plen <= 4096 {
        var sbuf: [4096]u8 = [0; 4096]
        try self.ReadExact(io, es, mut sbuf[0..plen])
        es.in_frame = false
        return try self.DecodeQpackBytes(io, a_qdec, block, sbuf[0..plen])
    }
    var hbuf := HeapList[u8].New(a_qdec)
    defer hbuf.Free(a_qdec)
    for _ in 0..plen {
        try hbuf.Push(a_qdec, 0)
    }
    try self.ReadExact(io, es, mut hbuf.ItemsMut()[0..plen])
    es.in_frame = false
    return try self.DecodeQpackBytes(io, a_qdec, block, hbuf.Items()[0..plen])
}

// CheckSectionSize checks that decoded_size fits within the peer's
// SETTINGS_MAX_FIELD_SECTION_SIZE.
CheckSectionSize(s: &Session, io: Io, decoded_size: i64): Error!void {
    var g := s.state_mu.Lock(io.NoCancel()).Must()
    lim := (&*g.Ref()).peer_settings.max_field_section_size
    g.Unlock()
    if m := lim {
        if Cast[u64](decoded_size) > m {
            fail SendTooLarge
        }
    }
}

// WriteHeadersFrame writes an encoded HEADERS frame to stream.
WriteHeadersFrame(s: &Session, io: Io, stream: &net.MuxStream, frame_bytes: []u8, fin: bool): Error!void {
    s.m.Write(io, stream, frame_bytes, fin) else e {
        fail NormMuxErr(e)
    }
}

/// Opens a client HTTP/3 request stream on s, sends pseudo and headers as one
/// HEADERS frame, and closes the send side immediately when end is true (RFC
/// 9114 4.1, 4.3). Errors: SendTooLarge, Violation, or net.Error. O(headers +
/// bytes); allocates nothing for sections up to 4 KiB.
export Exchange.Open[F: hf.Field](s: &Session, io: Io, pseudo: &hf.Pseudo, headers: []F, end: bool): Error!Exchange from s {
    try s.Poll(io)
    try s.CheckFatal(io)

    m_bytes := pseudo.method.AsBytes()
    sc_bytes := pseudo.scheme.AsBytes()
    au_bytes := pseudo.authority.AsBytes()
    pa_bytes := pseudo.path.AsBytes()

    var decoded_size: i64 = i64(7 + m_bytes.Len() + 32)
    if sc_bytes.Len() > 0 {
        decoded_size += i64(7 + sc_bytes.Len() + 32)
    }
    if au_bytes.Len() > 0 {
        decoded_size += i64(10 + au_bytes.Len() + 32)
    }
    if pa_bytes.Len() > 0 {
        decoded_size += i64(5 + pa_bytes.Len() + 32)
    }
    for h in &headers {
        decoded_size += i64(h.Name().Len() + h.Value().Len() + 32)
    }
    try CheckSectionSize(s, io, decoded_size)

    qenc := hf.QEncoder.New()
    var bound := qenc.StartBound() + qenc.FieldBound(":method".AsBytes(), m_bytes)
    if sc_bytes.Len() > 0 {
        bound += qenc.FieldBound(":scheme".AsBytes(), sc_bytes)
    }
    if au_bytes.Len() > 0 {
        bound += qenc.FieldBound(":authority".AsBytes(), au_bytes)
    }
    if pa_bytes.Len() > 0 {
        bound += qenc.FieldBound(":path".AsBytes(), pa_bytes)
    }
    bound += qenc.Bound(headers) - qenc.StartBound()

    stream := (s.m.Open(io, false, true) else e {
        fail NormMuxErr(e)
    }) else {
        fail Closed
    }
    onerr s.m.Release(stream)

    if bound + 16 <= 4096 {
        var buf: [4096]u8 = [0; 4096]
        var o := qenc.Start(mut buf[16..]).Must()
        o += qenc.Write(mut buf[16 + o..], ":method".AsBytes(), m_bytes, false).Must()
        if sc_bytes.Len() > 0 {
            o += qenc.Write(mut buf[16 + o..], ":scheme".AsBytes(), sc_bytes, false).Must()
        }
        if au_bytes.Len() > 0 {
            o += qenc.Write(mut buf[16 + o..], ":authority".AsBytes(), au_bytes, false).Must()
        }
        if pa_bytes.Len() > 0 {
            o += qenc.Write(mut buf[16 + o..], ":path".AsBytes(), pa_bytes, false).Must()
        }
        for h in &headers {
            o += qenc.Write(mut buf[16 + o..], h.Name(), h.Value(), h.Sensitive()).Must()
        }
        hlen := 1 + VarintLen(Cast[u64](o))
        start := 16 - hlen
        _ = WriteFrameHeader(mut buf[start..16], FrameHeaders, Cast[u64](o))
        try WriteHeadersFrame(s, io, &stream, buf[start..16 + o], end)
        return NewExchange(s, stream, end, pseudo.method == "HEAD")
    }

    a := s.heap.Alloc()
    var hbuf := HeapList[u8].New(a)
    defer hbuf.Free(a)
    for _ in 0..(bound + 16) {
        try hbuf.Push(a, 0)
    }
    items := hbuf.ItemsMut()
    var o := qenc.Start(mut items[16..]).Must()
    o += qenc.Write(mut items[16 + o..], ":method".AsBytes(), m_bytes, false).Must()
    if sc_bytes.Len() > 0 {
        o += qenc.Write(mut items[16 + o..], ":scheme".AsBytes(), sc_bytes, false).Must()
    }
    if au_bytes.Len() > 0 {
        o += qenc.Write(mut items[16 + o..], ":authority".AsBytes(), au_bytes, false).Must()
    }
    if pa_bytes.Len() > 0 {
        o += qenc.Write(mut items[16 + o..], ":path".AsBytes(), pa_bytes, false).Must()
    }
    for h in &headers {
        o += qenc.Write(mut items[16 + o..], h.Name(), h.Value(), h.Sensitive()).Must()
    }
    hlen := 1 + VarintLen(Cast[u64](o))
    start := 16 - hlen
    _ = WriteFrameHeader(mut items[start..16], FrameHeaders, Cast[u64](o))
    try WriteHeadersFrame(s, io, &stream, items[start..16 + o], end)
    return NewExchange(s, stream, end, pseudo.method == "HEAD")
}

/// Accepts the next incoming client request stream on a server Session,
/// polling unidirectional streams as needed. Errors: Violation, Closed,
/// Canceled, or net.Error. O(1) besides wait; allocates nothing.
export Exchange.Accept(s: &Session, io: Io): Error!Exchange from s {
    for {
        try s.Poll(io)
        var g := s.state_mu.Lock(io.NoCancel()).Must()
        st := mut *g.Mut()
        if fe := st.fatal_err {
            g.Unlock()
            fail fe
        }
        if st.closed {
            g.Unlock()
            fail Closed
        }
        if st.accept_q.Len() > 0 {
            stream := st.accept_q.Remove(0)
            g.Unlock()
            return NewExchange(s, stream, false, false)
        }
        is_serving := st.serving
        g.Unlock()

        if is_serving {
            try s.accept_ev.Wait(io)
        } else {
            try s.WaitStep(io, true)
        }
    }
}

// HeadInner executes Exchange.Head under an already-locked ExState, returning
// either the final head or a non-zero stream_code to fail the stream with.
Exchange.HeadInner(&self, a: &Arena, io: Io, es: mut ExState): Error!(ok: bool, status: int, headers: []hf.FieldView, content_length: ?i64, too_large: bool, stream_code: u64) from a {
    if es.rx_state != RxWantHead {
        fail self.sess.RecordFatal(io, H3InternalError)
    }

    var block := hf.FieldBlock.New(self.sess.heap.Alloc())
    defer block.Free(self.sess.heap.Alloc())

    for {
        fh := (try self.NextFrame(io, es)) else {
            return (ok: false, status: 0, headers: List[hf.FieldView].New(a).Done()[..], content_length: none, too_large: false, stream_code: H3MessageError)
        }
        if fh.kind == FrameData {
            if es.saw_interim {
                return (ok: false, status: 0, headers: List[hf.FieldView].New(a).Done()[..], content_length: none, too_large: false, stream_code: H3MessageError)
            }
            fail self.sess.RecordFatal(io, H3FrameUnexpected)
        }
        dec := try self.DecodeHeadersPayload(io, es, fh, self.sess.heap.Alloc(), mut block)
        if dec.stream_code != 0 {
            return (ok: false, status: 0, headers: List[hf.FieldView].New(a).Done()[..], content_length: none, too_large: false, stream_code: dec.stream_code)
        }
        if dec.too_large {
            return (ok: false, status: 0, headers: List[hf.FieldView].New(a).Done()[..], content_length: none, too_large: true, stream_code: H3MessageError)
        }
        res := hf.CheckResponse(&block) else {
            return (ok: false, status: 0, headers: List[hf.FieldView].New(a).Done()[..], content_length: none, too_large: false, stream_code: H3MessageError)
        }
        if res.status >= 100 && res.status < 200 {
            es.saw_interim = true
            block.Reset()
            continue
        }
        es.rx_state = RxBody
        es.expected_cl = res.content_length
        es.body_exempt = self.req_was_head || res.status == 204 || res.status == 304

        var out := List[hf.FieldView].New(a)
        for i in 0..block.Len() {
            t := block.Text(i).Must()
            if t.name.AsBytes()[0] == b':' {
                continue
            }
            nc := try a.Dup(t.name)
            vc := try a.Dup(t.value)
            try out.Push(hf.FieldView(name: nc, value: vc))
        }
        return (ok: true, status: res.status, headers: out.Done()[..], content_length: res.content_length, too_large: false, stream_code: 0)
    }
}

/// Reads and validates the HTTP/3 response head on a client Exchange,
/// consuming any 1xx interim responses and returning the final status, regular
/// header views in a, and Content-Length (RFC 9114 4.1, 4.3.2). Errors:
/// TooLarge, Violation, StreamReset, or net.Error. O(headers); allocates
/// returned strings and FieldViews on a.
export Exchange.Head(&self, a: &Arena, io: Io): Error!(status: int, headers: []hf.FieldView, content_length: ?i64) from a {
    try self.sess.Poll(io)
    var g := try self.st.Lock(io)
    r := self.HeadInner(a, io, mut *g.Mut()) else e {
        g.Unlock()
        fail e
    }
    g.Unlock()
    if !r.ok {
        v := self.FailStream(r.stream_code)
        if r.too_large {
            fail TooLarge
        }
        fail v
    }
    return (status: r.status, headers: r.headers, content_length: r.content_length)
}

// RequestHeadInner executes Exchange.RequestHead under an already-locked ExState.
Exchange.RequestHeadInner(&self, io: Io, es: mut ExState, block: mut hf.FieldBlock): Error!(too_large: bool, stream_code: u64) {
    if es.rx_state != RxWantHead {
        fail self.sess.RecordFatal(io, H3InternalError)
    }
    fh := (try self.NextFrame(io, es)) else {
        return (too_large: false, stream_code: H3RequestIncomplete)
    }
    if fh.kind == FrameData {
        fail self.sess.RecordFatal(io, H3FrameUnexpected)
    }
    dec := try self.DecodeHeadersPayload(io, es, fh, self.sess.heap.Alloc(), block)
    if dec.stream_code != 0 {
        return (too_large: false, stream_code: dec.stream_code)
    }
    if dec.too_large {
        es.rx_state = RxBody
        return (too_large: true, stream_code: 0)
    }
    rh := hf.CheckRequest(&*block) else {
        return (too_large: false, stream_code: H3MessageError)
    }
    if !rh.connect && (rh.scheme == "http" || rh.scheme == "https") {
        // RFC 9114 4.3.1 (H3-43): must contain non-empty :authority or Host.
        var has_auth := false
        var has_host := false
        var empty_field := false
        for i in 0..block.Len() {
            f := block.Get(i)
            if f.name.Len() == 10 && core.BytesEqual(f.name, 0, ":authority".AsBytes()) {
                has_auth = true
                if f.value.Len() == 0 {
                    empty_field = true
                }
            } else if f.name.Len() == 4 && core.BytesEqual(f.name, 0, "host".AsBytes()) {
                has_host = true
                if f.value.Len() == 0 {
                    empty_field = true
                }
            }
        }
        if empty_field || (!has_auth && !has_host) {
            return (too_large: false, stream_code: H3MessageError)
        }
    }
    es.rx_state = RxBody
    es.expected_cl = rh.content_length
    es.body_exempt = false
    return (too_large: false, stream_code: 0)
}

/// Reads and validates the HTTP/3 request head on a server Exchange into
/// block (which must have been created on the session's heap). On TooLarge
/// (H3-36), the stream stays open so the server may answer 431. Errors:
/// TooLarge, Violation, or net.Error. O(headers); grows block as needed.
export Exchange.RequestHead(&self, io: Io, block: mut hf.FieldBlock): Error!void {
    try self.sess.Poll(io)
    var g := try self.st.Lock(io)
    r := self.RequestHeadInner(io, mut *g.Mut(), block) else e {
        g.Unlock()
        fail e
    }
    g.Unlock()
    if r.too_large {
        fail TooLarge
    }
    if r.stream_code != 0 {
        fail self.FailStream(r.stream_code)
    }
}

// ValidateTrailers decodes and checks a trailing HEADERS frame.
Exchange.ValidateTrailers(&self, io: Io, es: mut ExState, fh: FrameHeader): Error!u64 {
    var tblock := hf.FieldBlock.New(self.sess.heap.Alloc())
    defer tblock.Free(self.sess.heap.Alloc())
    dec := try self.DecodeHeadersPayload(io, es, fh, self.sess.heap.Alloc(), mut tblock)
    if dec.stream_code != 0 {
        return dec.stream_code
    }
    if dec.too_large {
        return H3MessageError
    }
    hf.CheckTrailers(&tblock) else {
        return H3MessageError
    }
    return 0
}

// ReadInner executes Exchange.Read under an already-locked ExState.
Exchange.ReadInner(&self, io: Io, es: mut ExState, buf: mut []u8): Error!(n: int, stream_code: u64) {
    if es.rx_state == RxWantHead {
        fail self.sess.RecordFatal(io, H3InternalError)
    }
    if es.rx_state == RxDone {
        return (n: 0, stream_code: 0)
    }

    for {
        if es.data_rem > 0 {
            if buf.Len() == 0 {
                return (n: 0, stream_code: 0)
            }
            avail := es.rx_end - es.rx_pos
            if avail > 0 {
                lim := if Cast[u64](buf.Len()) < es.data_rem { buf.Len() } else { Cast[int](es.data_rem) }
                take := if avail < lim { avail } else { lim }
                (mut buf[0..take]).CopyFrom(es.rx_buf[es.rx_pos..es.rx_pos + take])
                es.rx_pos += take
                es.data_rem -= Cast[u64](take)
                if es.data_rem == 0 {
                    es.in_frame = false
                }
                es.body_read += i64(take)
                if !es.body_exempt {
                    if cl := es.expected_cl {
                        if es.body_read > cl {
                            return (n: 0, stream_code: H3MessageError)
                        }
                    }
                }
                return (n: take, stream_code: 0)
            }
            want := if Cast[u64](buf.Len()) < es.data_rem { buf.Len() } else { Cast[int](es.data_rem) }
            n := try self.ReadStreamFields(io, mut es.rx_fin_seen, mut es.rx_total, mut es.rx_credited, mut buf[0..want])
            if n == 0 {
                fail self.sess.RecordFatal(io, H3FrameError)
            }
            es.data_rem -= Cast[u64](n)
            if es.data_rem == 0 {
                es.in_frame = false
            }
            es.body_read += i64(n)
            if !es.body_exempt {
                if cl := es.expected_cl {
                    if es.body_read > cl {
                        return (n: 0, stream_code: H3MessageError)
                    }
                }
            }
            return (n: n, stream_code: 0)
        }

        fh := (try self.NextFrame(io, es)) else {
            es.rx_state = RxDone
            if !es.body_exempt {
                if cl := es.expected_cl {
                    if es.body_read != cl {
                        return (n: 0, stream_code: H3MessageError)
                    }
                }
            }
            return (n: 0, stream_code: 0)
        }

        if es.rx_state == RxTrailersDone {
            fail self.sess.RecordFatal(io, H3FrameUnexpected)
        }

        if fh.kind == FrameData {
            if !es.body_exempt {
                if cl := es.expected_cl {
                    if fh.length > Cast[u64](cl - es.body_read) {
                        return (n: 0, stream_code: H3MessageError)
                    }
                }
            }
            es.data_rem = fh.length
            if es.data_rem == 0 {
                es.in_frame = false
            }
            continue
        }

        sc := try self.ValidateTrailers(io, es, fh)
        if sc != 0 {
            return (n: 0, stream_code: sc)
        }
        es.rx_state = RxTrailersDone
    }
}

/// Reads body bytes into buf, returning 0 at end of stream (RFC 9114 4.1).
/// Skips unknown/GREASE frames, validates optional trailing HEADERS, checks
/// Content-Length, and rejects frames after trailers. Errors: Violation,
/// StreamReset, or net.Error. O(buf.Len()) besides wait; allocates nothing
/// for DATA.
export Exchange.Read(&self, io: Io, buf: mut []u8): Error!int {
    var g := try self.st.Lock(io)
    r := self.ReadInner(io, mut *g.Mut(), buf) else e {
        g.Unlock()
        fail e
    }
    g.Unlock()
    if r.stream_code != 0 {
        fail self.FailStream(r.stream_code)
    }
    return r.n
}

/// Sends an HTTP/3 response head (1xx interim or final status) on a server
/// Exchange, closing the send side when end is true and status >= 200 (RFC
/// 9114 4.1, 4.3.2). Errors: SendTooLarge, Violation, or net.Error.
/// O(headers + bytes); allocates nothing for sections up to 4 KiB.
export Exchange.Respond[F: hf.Field](&self, io: Io, status: int, headers: []F, end: bool): Error!void {
    if status < 100 || status > 999 || status == 101 {
        fail Violation(code: H3InternalError, stream: self.stream.Id())
    }
    var g := try self.st.Lock(io)
    defer g.Unlock()
    es := mut *g.Mut()
    if es.tx_final || es.tx_fin {
        fail Violation(code: H3InternalError, stream: self.stream.Id())
    }

    var st_buf: [3]u8 = [
        Trunc[u8](48 + (status / 100)),
        Trunc[u8](48 + ((status / 10) % 10)),
        Trunc[u8](48 + (status % 10)),
    ]
    var decoded_size: i64 = i64(7 + 3 + 32)
    for h in &headers {
        decoded_size += i64(h.Name().Len() + h.Value().Len() + 32)
    }
    try CheckSectionSize(self.sess, io, decoded_size)

    qenc := hf.QEncoder.New()
    bound := qenc.FieldBound(":status".AsBytes(), st_buf[..]) + qenc.Bound(headers)
    is_final := status >= 200
    fin := is_final && end

    if bound + 16 <= 4096 {
        var buf: [4096]u8 = [0; 4096]
        var o := qenc.Start(mut buf[16..]).Must()
        o += qenc.Write(mut buf[16 + o..], ":status".AsBytes(), st_buf[..], false).Must()
        for h in &headers {
            o += qenc.Write(mut buf[16 + o..], h.Name(), h.Value(), h.Sensitive()).Must()
        }
        hlen := 1 + VarintLen(Cast[u64](o))
        start := 16 - hlen
        _ = WriteFrameHeader(mut buf[start..16], FrameHeaders, Cast[u64](o))
        try WriteHeadersFrame(self.sess, io, &self.stream, buf[start..16 + o], fin)
    } else {
        a := self.sess.heap.Alloc()
        var hbuf := HeapList[u8].New(a)
        defer hbuf.Free(a)
        for _ in 0..(bound + 16) {
            try hbuf.Push(a, 0)
        }
        items := hbuf.ItemsMut()
        var o := qenc.Start(mut items[16..]).Must()
        o += qenc.Write(mut items[16 + o..], ":status".AsBytes(), st_buf[..], false).Must()
        for h in &headers {
            o += qenc.Write(mut items[16 + o..], h.Name(), h.Value(), h.Sensitive()).Must()
        }
        hlen := 1 + VarintLen(Cast[u64](o))
        start := 16 - hlen
        _ = WriteFrameHeader(mut items[start..16], FrameHeaders, Cast[u64](o))
        try WriteHeadersFrame(self.sess, io, &self.stream, items[start..16 + o], fin)
    }

    if is_final {
        es.tx_final = true
        es.tx_fin = fin
    }
}

/// Writes data as one HTTP/3 DATA frame and closes the send side when end is
/// true (RFC 9114 4.1, 7.2.1). On STOP_SENDING (StreamReset), resets the
/// send half without disturbing the receive half (H3-21). Errors: Violation,
/// StreamReset, or net.Error. O(data.Len()) besides wait; allocates nothing.
export Exchange.Write(&self, io: Io, data: []u8, end: bool): Error!void {
    var g := try self.st.Lock(io)
    defer g.Unlock()
    es := mut *g.Mut()
    if es.tx_fin || (self.sess.is_server && !es.tx_final) {
        fail Violation(code: H3InternalError, stream: self.stream.Id())
    }
    if data.Len() > 0 {
        var hdr: [16]u8 = [0; 16]
        hlen := WriteFrameHeader(mut hdr[..], FrameData, Cast[u64](data.Len()))
        self.sess.m.Write(io, &self.stream, hdr[0..hlen], false) else e {
            match e {
                net.StreamReset(code) {
                    nc := NormError(code)
                    self.sess.m.Reset(&self.stream, nc)
                    es.tx_fin = true
                    fail net.StreamReset(code: nc)
                }
                _ { fail NormMuxErr(e) }
            }
        }
        self.sess.m.Write(io, &self.stream, data, end) else e {
            match e {
                net.StreamReset(code) {
                    nc := NormError(code)
                    self.sess.m.Reset(&self.stream, nc)
                    es.tx_fin = true
                    fail net.StreamReset(code: nc)
                }
                _ { fail NormMuxErr(e) }
            }
        }
        es.tx_fin = end
        return
    }
    if end {
        self.sess.m.Write(io, &self.stream, data[0..0], true) else e {
            match e {
                net.StreamReset(code) {
                    nc := NormError(code)
                    self.sess.m.Reset(&self.stream, nc)
                    es.tx_fin = true
                    fail net.StreamReset(code: nc)
                }
                _ { fail NormMuxErr(e) }
            }
        }
        es.tx_fin = true
    }
}

/// Returns the underlying QUIC/Mux stream identifier. O(1); allocates nothing.
export Exchange.Id(&self): i64 {
    return self.stream.Id()
}

/// Cancels the exchange by resetting open send and receive halves with
/// H3RequestCancelled (RFC 9114 4.1.1). O(1); allocates nothing.
export Exchange.Cancel(&self) {
    _ = self.FailStream(H3RequestCancelled)
}

/// Resets open send and receive halves of the stream with code (RFC 9114
/// 4.1.1, 8). O(1); allocates nothing.
export Exchange.Reset(&self, code: u64) {
    _ = self.FailStream(code)
}

/// Aborts reading further request or response bytes with STOP_SENDING(code)
/// without resetting the send half (RFC 9114 4.1, H3-22). O(1); allocates
/// nothing.
export Exchange.StopReading(&self, code: u64) {
    var g := self.st.Lock(core.IoMasked(rt.NativeIo())).Must()
    es := mut *g.Mut()
    if !es.rx_fin_seen {
        self.sess.m.Stop(&self.stream, code)
        es.rx_fin_seen = true
    }
    g.Unlock()
    self.sess.CancelQpackStream(self.stream.Id())
}

/// Releases the Exchange's underlying MuxStream back to s. O(1); allocates
/// nothing.
export Exchange.Release(self, s: &Session) {
    Exchange(sess, stream, req_was_head, st) := self
    _ = sess
    _ = req_was_head
    _ = st.Into()
    s.CancelQpackStream(stream.Id())
    s.m.Release(stream)
}
