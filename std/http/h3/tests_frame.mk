// Why: every HTTP/3 varint, frame header, single-id payload and error-code
// invariant of RFC 9114 7 and 8.1 must be pinned by exact positive and
// negative unit tests.
//
// What: test blocks for varints, GREASE helpers, RFC 9114 8.1 error code
// constants (H3-83), frame header parsing/writing, reserved HTTP/2 frame
// types (H3-81), skipped unknown/GREASE frames (H3-17), exact payload length
// checks (H3-62), and truncated-frame FIN handling (H3-63).
//
// How: exercises pure varint/frame codecs directly and exercises stream-level
// frame truncation and skipping over std.net.pipe's bounded MuxPair.

import hf = std.http.fields
import std.net.pipe

test "RFC 9114 8.1: error codes 0x0100 to 0x0110 have their registered values" {
    Assert(H3NoError == 0x0100)
    Assert(H3GeneralProtocolError == 0x0101)
    Assert(H3InternalError == 0x0102)
    Assert(H3StreamCreationError == 0x0103)
    Assert(H3ClosedCriticalStream == 0x0104)
    Assert(H3FrameUnexpected == 0x0105)
    Assert(H3FrameError == 0x0106)
    Assert(H3ExcessiveLoad == 0x0107)
    Assert(H3IdError == 0x0108)
    Assert(H3SettingsError == 0x0109)
    Assert(H3MissingSettings == 0x010a)
    Assert(H3RequestRejected == 0x010b)
    Assert(H3RequestCancelled == 0x010c)
    Assert(H3RequestIncomplete == 0x010d)
    Assert(H3MessageError == 0x010e)
    Assert(H3ConnectError == 0x010f)
    Assert(H3VersionFallback == 0x0110)
    Assert(QpackDecompressionFailed == 0x0200)
    Assert(QpackEncoderStreamError == 0x0201)
    Assert(QpackDecoderStreamError == 0x0202)
    Assert(Unprocessed(0, 0) && Unprocessed(4, 4) && Unprocessed(8, 4) && !Unprocessed(0, 4))
}

test "HTTP/3 varint codec and GREASE helpers" {
    var buf: [16]u8 = [0; 16]
    for v in [u64(0), u64(37), u64(63), u64(64), u64(15293), u64(16383), u64(16384), u64(494878333), u64(1073741823), u64(1073741824), MaxVarint] {
        n := WriteVarint(mut buf[..], v)
        Assert(n == VarintLen(v) && n > 0)
        dec := ReadVarint(buf[0..n], 0).Must()
        Assert(dec.v == v && dec.n == n)
        // Truncated slice must return none.
        Assert(ReadVarint(buf[0..n - 1], 0).IsNone())
    }
    Assert(WriteVarint(mut buf[..], MaxVarint + 1) == 0)
    Assert(IsGrease(0x21) && IsGrease(0x40) && IsGrease(GreaseId(5)) && !IsGrease(0x22) && !IsGrease(0x00))
}

test "RFC 9114 7.1: a frame payload longer or shorter than its fields is a connection H3_FRAME_ERROR" {
    // Empty GOAWAY / CANCEL_PUSH / MAX_PUSH_ID -> H3FrameError.
    var empty: [0]u8 = []
    var got_err: u64 = 0
    _ = ParseSingleIdPayload(empty[..]) else e {
        got_err = e.code
        0
    }
    Assert(got_err == H3FrameError)

    // 2-byte varint missing second byte.
    var trunc: [1]u8 = [0x40]
    got_err = 0
    _ = ParseSingleIdPayload(trunc[..]) else e {
        got_err = e.code
        0
    }
    Assert(got_err == H3FrameError)

    // Trailing extra byte after 1-byte varint.
    var extra: [2]u8 = [0x04, 0x00]
    got_err = 0
    _ = ParseSingleIdPayload(extra[..]) else e {
        got_err = e.code
        0
    }
    Assert(got_err == H3FrameError)

    // Exact 1-byte varint succeeds.
    var exact: [1]u8 = [0x04]
    Assert((try ParseSingleIdPayload(exact[..])) == 4)
}

test "RFC 9114 7.2.8: reserved and unknown frame types are skipped on every stream" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()

    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())
    ss := try Session.Server(sys.heap, sys.io, &sm, 4096)
    defer ss.Free(sys.heap.Alloc())

    // Client opens a raw bidi request stream with GREASE frames before, between, and after HEADERS + DATA.
    b := (try cm.Open(sys.io, false, true)).Must()
    defer cm.Release(b)

    // GREASE frame 0x21 (len 3: "abc"), then HEADERS (:method GET, :scheme https, :authority ex, :path /),
    // then GREASE frame 0x40 (len 2: "xy"), then DATA ("hi"), then GREASE frame 0x5f (len 1: "z") + FIN.
    var raw: [64]u8 = [0; 64]
    var o := WriteFrameHeader(mut raw[..], 0x21, 3)
    raw[o] = b'a'
    raw[o + 1] = b'b'
    raw[o + 2] = b'c'
    o += 3
    // Static QPACK GET https / with :authority "ex":
    // prefix 00 00, :method GET (0xD1 = 0xC0|17), :scheme https (0xD7 = 0xC0|23), :path / (0xC1 = 0xC0|1),
    // :authority "ex" (0x50 0x02 'e' 'x')
    var hdr_sec: [7]u8 = [0x00, 0x00, 0xD1, 0xD7, 0xC1, 0x50, 0x02]
    o += WriteFrameHeader(mut raw[o..], FrameHeaders, 9)
    (mut raw[o..o + 7]).CopyFrom(hdr_sec[..])
    raw[o + 7] = b'e'
    raw[o + 8] = b'x'
    o += 9
    o += WriteFrameHeader(mut raw[o..], 0x40, 2)
    raw[o] = b'x'
    raw[o + 1] = b'y'
    o += 2
    o += WriteFrameHeader(mut raw[o..], FrameData, 2)
    raw[o] = b'h'
    raw[o + 1] = b'i'
    o += 2
    o += WriteFrameHeader(mut raw[o..], 0x5f, 1)
    raw[o] = b'z'
    o += 1
    try cm.Write(sys.io, &b, raw[0..o], true)

    try ss.Ref().Poll(sys.io)
    ex := try Exchange.Accept(ss.Ref(), sys.io)
    defer ex.Release(ss.Ref())
    var fb := hf.FieldBlock.New(sys.heap.Alloc())
    defer fb.Free(sys.heap.Alloc())
    try ex.RequestHead(sys.io, mut fb)
    var body: [16]u8 = [0; 16]
    n := try ex.Read(sys.io, mut body[..])
    Assert(n == 2 && body[0] == b'h' && body[1] == b'i')
    Assert((try ex.Read(sys.io, mut body[..])) == 0)
}

test "RFC 9114 7.2.8: HTTP/2 frame types 0x02 0x06 0x08 0x09 are a connection H3_FRAME_UNEXPECTED" (sys: &System) {
    for h2t in [u64(0x02), u64(0x06), u64(0x08), u64(0x09)] {
        (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
        defer cm.Abandon()
        defer sm.Abandon()
        cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
        defer cs.Free(sys.heap.Alloc())
        ss := try Session.Server(sys.heap, sys.io, &sm, 4096)
        defer ss.Free(sys.heap.Alloc())

        b := (try cm.Open(sys.io, false, true)).Must()
        defer cm.Release(b)
        var f: [8]u8 = [0; 8]
        n := WriteFrameHeader(mut f[..], h2t, 0)
        try cm.Write(sys.io, &b, f[0..n], false)

        ex := try Exchange.Accept(ss.Ref(), sys.io)
        defer ex.Release(ss.Ref())
        var fb := hf.FieldBlock.New(sys.heap.Alloc())
        defer fb.Free(sys.heap.Alloc())
        var code: u64 = 0
        var stream: i64 = 0
        ex.RequestHead(sys.io, mut fb) else e {
            match e {
                Violation(code: c, stream: s) {
                    code = c
                    stream = s
                }
                _ { Assert(false) }
            }
        }
        Assert(code == H3FrameUnexpected && stream == -1)
    }
}

test "RFC 9114 7.1: FIN inside a frame is a connection H3_FRAME_ERROR" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())
    ss := try Session.Server(sys.heap, sys.io, &sm, 4096)
    defer ss.Free(sys.heap.Alloc())

    b := (try cm.Open(sys.io, false, true)).Must()
    defer cm.Release(b)
    // HEADERS frame declaring length 10, followed by only 3 bytes and clean FIN.
    var f: [8]u8 = [0; 8]
    n := WriteFrameHeader(mut f[..], FrameHeaders, 10)
    f[n] = 0x00
    f[n + 1] = 0x00
    f[n + 2] = 0xD1
    try cm.Write(sys.io, &b, f[0..n + 3], true)

    ex := try Exchange.Accept(ss.Ref(), sys.io)
    defer ex.Release(ss.Ref())
    var fb := hf.FieldBlock.New(sys.heap.Alloc())
    defer fb.Free(sys.heap.Alloc())
    var code: u64 = 0
    ex.RequestHead(sys.io, mut fb) else e {
        match e {
            Violation(code: c, stream: s) {
                Assert(s == -1)
                code = c
            }
            _ { Assert(false) }
        }
    }
    Assert(code == H3FrameError)
}
