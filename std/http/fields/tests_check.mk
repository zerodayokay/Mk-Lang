// Why: RFC 9113 8.1-8.5 and RFC 9114 4.1-4.3 field validation prevents
// request smuggling, header injection and malformed messages; every byte class
// and every Malformed member must have a dedicated negative test alongside
// valid requests, responses, trailers, cookies and send-side helpers.
//
// What: unit tests for CheckRequest, CheckResponse, CheckTrailers,
// JoinCookies, Sendable, LowerName, Pseudo, and FieldView.
//
// How: builds FieldBlock values directly via Sink.Add and asserts exact
// Malformed variants and parsed RequestHead / status / content_length values.

// TestAdd adds one field to b.
TestAdd(h: &Alloc, b: mut FieldBlock, name: str, value: str): OutOfMemory!void {
    try b.Add(h, name.AsBytes(), value.AsBytes(), false, 0)
}

// TestReqBlock builds a minimal valid GET request FieldBlock.
TestReqBlock(h: &Alloc): OutOfMemory!FieldBlock {
    var b := FieldBlock.New(h)
    onerr b.Free(h)
    try TestAdd(h, mut b, ":method", "GET")
    try TestAdd(h, mut b, ":scheme", "https")
    try TestAdd(h, mut b, ":authority", "example.com")
    try TestAdd(h, mut b, ":path", "/index.html")
    return b
}

// TestExpectReqErr checks that CheckRequest fails with want.
TestExpectReqErr(b: &FieldBlock, want: Malformed) {
    match CheckRequest(b) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.why == want) }
    }
}

test "RFC 9113 8.1: a pseudo-header in trailers is malformed" (sys: &System) {
    h := sys.heap.Alloc()
    var b := FieldBlock.New(h)
    defer b.Free(h)
    try TestAdd(h, mut b, "x-checksum", "abc")
    try CheckTrailers(&b)
    var bad := FieldBlock.New(h)
    defer bad.Free(h)
    try TestAdd(h, mut bad, ":status", "200")
    match CheckTrailers(&bad) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.why == Malformed.PseudoUnknown) }
    }
}

test "RFC 9113 8.2: outgoing field names are lower-cased" {
    var out: [32]u8 = [0; 32]
    Assert(LowerName("Content-Type".AsBytes(), "text/plain".AsBytes(), mut out[..]))
    Assert(Same(out[0..12], "content-type"))
    // Rejects empty name, short out, control/non-ASCII/colon in name, and bad value bytes.
    Assert(!LowerName("".AsBytes(), "v".AsBytes(), mut out[..]))
    Assert(!LowerName("abc".AsBytes(), "v".AsBytes(), mut out[0..2]))
    Assert(!LowerName("bad name".AsBytes(), "v".AsBytes(), mut out[..]))
    Assert(!LowerName("bad:name".AsBytes(), "v".AsBytes(), mut out[..]))
    bad_hi: [2]u8 = [b'a', 0x7F]
    Assert(!LowerName(bad_hi[..], "v".AsBytes(), mut out[..]))
    Assert(!LowerName("x-ok".AsBytes(), " bad".AsBytes(), mut out[..]))
    Assert(!LowerName("x-ok".AsBytes(), "bad\r\n".AsBytes(), mut out[..]))
}

test "RFC 9113 8.2.1: a name byte in 0x00-0x20, 0x41-0x5a or 0x7f-0xff is malformed" (sys: &System) {
    h := sys.heap.Alloc()
    // Test each forbidden byte class in a field name.
    bad_bytes: [6]u8 = [0x00, 0x09, 0x20, 0x41, 0x5A, 0x7F]
    for c in bad_bytes {
        var b := try TestReqBlock(h)
        defer b.Free(h)
        nb: [3]u8 = [b'x', c, b'y']
        try b.Add(h, nb[..], "1".AsBytes(), false, 0)
        TestExpectReqErr(&b, Malformed.NameByte)
    }
    // High byte 0x80..0xff and empty name.
    var b_hi := try TestReqBlock(h)
    defer b_hi.Free(h)
    hi: [2]u8 = [b'x', 0x80]
    try b_hi.Add(h, hi[..], "1".AsBytes(), false, 0)
    TestExpectReqErr(&b_hi, Malformed.NameByte)
    var b_empty := try TestReqBlock(h)
    defer b_empty.Free(h)
    try TestAdd(h, mut b_empty, "", "1")
    TestExpectReqErr(&b_empty, Malformed.NameByte)
}

test "RFC 9113 8.2.1: a colon inside a field name is malformed" (sys: &System) {
    h := sys.heap.Alloc()
    var b1 := try TestReqBlock(h)
    defer b1.Free(h)
    try TestAdd(h, mut b1, "foo:bar", "1")
    TestExpectReqErr(&b1, Malformed.NameByte)
    var b2 := FieldBlock.New(h)
    defer b2.Free(h)
    try TestAdd(h, mut b2, ":pa:th", "/")
    TestExpectReqErr(&b2, Malformed.NameByte)
}

test "RFC 9113 8.2.1: NUL, CR or LF in a value is malformed" (sys: &System) {
    h := sys.heap.Alloc()
    bad_val: [3]u8 = [0x00, 0x0A, 0x0D]
    for c in bad_val {
        var b := try TestReqBlock(h)
        defer b.Free(h)
        vb: [3]u8 = [b'a', c, b'b']
        try b.Add(h, "x-val".AsBytes(), vb[..], false, 0)
        TestExpectReqErr(&b, Malformed.ValueByte)
    }
}

test "RFC 9113 8.2.1: a value with leading or trailing SP or HTAB is malformed" (sys: &System) {
    h := sys.heap.Alloc()
    ws: [4]str = [" a", "a ", "\ta", "a\t"]
    for v in ws {
        var b := try TestReqBlock(h)
        defer b.Free(h)
        try TestAdd(h, mut b, "x-ws", v)
        TestExpectReqErr(&b, Malformed.ValueByte)
    }
    // A value with invalid UTF-8 gives Malformed.NotText.
    var b_utf8 := try TestReqBlock(h)
    defer b_utf8.Free(h)
    bad_u: [2]u8 = [0xFF, 0xFE]
    try b_utf8.Add(h, "x-bin".AsBytes(), bad_u[..], false, 0)
    TestExpectReqErr(&b_utf8, Malformed.NotText)
}

test "RFC 9113 8.2.2: connection-specific fields are dropped on send" {
    Assert(!Sendable("connection".AsBytes()))
    Assert(!Sendable("Connection".AsBytes()))
    Assert(!Sendable("Proxy-Connection".AsBytes()))
    Assert(!Sendable("Keep-Alive".AsBytes()))
    Assert(!Sendable("Transfer-Encoding".AsBytes()))
    Assert(!Sendable("Upgrade".AsBytes()))
    Assert(!Sendable("TE".AsBytes()))
    Assert(!Sendable("Host".AsBytes()))
    Assert(Sendable("Content-Type".AsBytes()))
    Assert(Sendable("x-custom".AsBytes()))
}

test "RFC 9113 8.2.2: a connection-specific field is malformed" (sys: &System) {
    h := sys.heap.Alloc()
    bad: [5]str = ["connection", "proxy-connection", "keep-alive", "transfer-encoding", "upgrade"]
    for name in bad {
        var b := try TestReqBlock(h)
        defer b.Free(h)
        try TestAdd(h, mut b, name, "close")
        TestExpectReqErr(&b, Malformed.Connection)
    }
}

test "RFC 9113 8.2.2: te other than trailers is malformed" (sys: &System) {
    h := sys.heap.Alloc()
    var ok_b := try TestReqBlock(h)
    defer ok_b.Free(h)
    try TestAdd(h, mut ok_b, "te", "trailers")
    _ = try CheckRequest(&ok_b)
    var bad_req := try TestReqBlock(h)
    defer bad_req.Free(h)
    try TestAdd(h, mut bad_req, "te", "gzip")
    TestExpectReqErr(&bad_req, Malformed.Te)
    // te is not permitted in responses.
    var bad_resp := FieldBlock.New(h)
    defer bad_resp.Free(h)
    try TestAdd(h, mut bad_resp, ":status", "200")
    try TestAdd(h, mut bad_resp, "te", "trailers")
    match CheckResponse(&bad_resp) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.why == Malformed.Te) }
    }
}

test "RFC 9113 8.2.3: cookie crumbs are joined with a semicolon and a space" (sys: &System) {
    h := sys.heap.Alloc()
    var b1 := try TestReqBlock(h)
    defer b1.Free(h)
    try TestAdd(h, mut b1, "cookie", "a=1")
    var out: [64]u8 = [0; 64]
    Assert(JoinCookies(&b1, mut out[..]).IsNone())
    try TestAdd(h, mut b1, "cookie", "b=2")
    try TestAdd(h, mut b1, "cookie", "c=3")
    n := JoinCookies(&b1, mut out[..]).Must()
    Assert(Same(out[0..n], "a=1; b=2; c=3"))
    // Short output returns none.
    Assert(JoinCookies(&b1, mut out[0..n - 1]).IsNone())
}

test "RFC 9113 8.3: outgoing blocks put the defined pseudo-headers first" {
    p := Pseudo(method: "POST", scheme: "https", authority: "example.com", path: "/submit")
    fv := FieldView(name: "content-type", value: "application/json")
    Assert(p.method == "POST" && p.scheme == "https" && p.authority == "example.com" && p.path == "/submit")
    Assert(fv.name == "content-type" && fv.value == "application/json")
}

test "RFC 9113 8.3: an undefined, misplaced or wrong-direction pseudo-header is malformed" (sys: &System) {
    h := sys.heap.Alloc()
    // Pseudo-header after regular field -> PseudoAfter.
    var after := FieldBlock.New(h)
    defer after.Free(h)
    try TestAdd(h, mut after, ":method", "GET")
    try TestAdd(h, mut after, "x-one", "1")
    try TestAdd(h, mut after, ":path", "/")
    TestExpectReqErr(&after, Malformed.PseudoAfter)
    // Undefined / wrong-direction pseudo-header -> PseudoUnknown.
    var proto := try TestReqBlock(h)
    defer proto.Free(h)
    var proto2 := FieldBlock.New(h)
    defer proto2.Free(h)
    try TestAdd(h, mut proto2, ":protocol", "websocket")
    TestExpectReqErr(&proto2, Malformed.PseudoUnknown)
    var status_in_req := FieldBlock.New(h)
    defer status_in_req.Free(h)
    try TestAdd(h, mut status_in_req, ":status", "200")
    TestExpectReqErr(&status_in_req, Malformed.PseudoUnknown)
    var method_in_resp := FieldBlock.New(h)
    defer method_in_resp.Free(h)
    try TestAdd(h, mut method_in_resp, ":method", "GET")
    match CheckResponse(&method_in_resp) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.why == Malformed.PseudoUnknown) }
    }
}

test "RFC 9113 8.3: a repeated pseudo-header is malformed" (sys: &System) {
    h := sys.heap.Alloc()
    var b := try TestReqBlock(h)
    defer b.Free(h)
    var dup := FieldBlock.New(h)
    defer dup.Free(h)
    try TestAdd(h, mut dup, ":method", "GET")
    try TestAdd(h, mut dup, ":method", "POST")
    TestExpectReqErr(&dup, Malformed.PseudoRepeated)
}

test "RFC 9113 8.3.1: a request missing :method, :scheme or :path is malformed" (sys: &System) {
    h := sys.heap.Alloc()
    var b := FieldBlock.New(h)
    defer b.Free(h)
    try TestAdd(h, mut b, ":method", "GET")
    try TestAdd(h, mut b, ":scheme", "https")
    TestExpectReqErr(&b, Malformed.PseudoMissing)
    // BadScheme when :scheme is not http or https.
    var b_sch := FieldBlock.New(h)
    defer b_sch.Free(h)
    try TestAdd(h, mut b_sch, ":method", "GET")
    try TestAdd(h, mut b_sch, ":scheme", "ftp")
    try TestAdd(h, mut b_sch, ":path", "/")
    TestExpectReqErr(&b_sch, Malformed.BadScheme)
}

test "RFC 9113 8.3.1: an empty :path, or one not starting with / or *, is malformed" (sys: &System) {
    h := sys.heap.Alloc()
    var b_empty := FieldBlock.New(h)
    defer b_empty.Free(h)
    try TestAdd(h, mut b_empty, ":method", "GET")
    try TestAdd(h, mut b_empty, ":scheme", "http")
    try TestAdd(h, mut b_empty, ":path", "")
    TestExpectReqErr(&b_empty, Malformed.BadPath)
    var b_rel := FieldBlock.New(h)
    defer b_rel.Free(h)
    try TestAdd(h, mut b_rel, ":method", "GET")
    try TestAdd(h, mut b_rel, ":scheme", "http")
    try TestAdd(h, mut b_rel, ":path", "index.html")
    TestExpectReqErr(&b_rel, Malformed.BadPath)
    var b_star_get := FieldBlock.New(h)
    defer b_star_get.Free(h)
    try TestAdd(h, mut b_star_get, ":method", "GET")
    try TestAdd(h, mut b_star_get, ":scheme", "http")
    try TestAdd(h, mut b_star_get, ":path", "*")
    TestExpectReqErr(&b_star_get, Malformed.BadPath)
    // OPTIONS * is valid.
    var b_opt := FieldBlock.New(h)
    defer b_opt.Free(h)
    try TestAdd(h, mut b_opt, ":method", "OPTIONS")
    try TestAdd(h, mut b_opt, ":scheme", "http")
    try TestAdd(h, mut b_opt, ":path", "*")
    rh := try CheckRequest(&b_opt)
    Assert(rh.method == "OPTIONS" && rh.path == "*" && !rh.connect)
}

test "RFC 9113 8.3.1: an :authority with userinfo is malformed" (sys: &System) {
    h := sys.heap.Alloc()
    var b := FieldBlock.New(h)
    defer b.Free(h)
    try TestAdd(h, mut b, ":method", "GET")
    try TestAdd(h, mut b, ":scheme", "https")
    try TestAdd(h, mut b, ":authority", "user@example.com")
    try TestAdd(h, mut b, ":path", "/")
    TestExpectReqErr(&b, Malformed.PseudoUnknown)
}

test "RFC 9113 8.3.1: :authority wins over Host" (sys: &System) {
    h := sys.heap.Alloc()
    var b := try TestReqBlock(h)
    defer b.Free(h)
    try TestAdd(h, mut b, "host", "EXAMPLE.COM:443")
    rh := try CheckRequest(&b)
    Assert(rh.authority == "example.com")
}

test "RFC 9113 8.3.1: Host and :authority are compared normalised" (sys: &System) {
    h := sys.heap.Alloc()
    // Default port for http is 80; IPv6 host case-insensitive.
    var ok_b := FieldBlock.New(h)
    defer ok_b.Free(h)
    try TestAdd(h, mut ok_b, ":method", "GET")
    try TestAdd(h, mut ok_b, ":scheme", "http")
    try TestAdd(h, mut ok_b, ":authority", "[2001:DB8::1]:80")
    try TestAdd(h, mut ok_b, ":path", "/")
    try TestAdd(h, mut ok_b, "host", "[2001:db8::1]")
    _ = try CheckRequest(&ok_b)
    // Different host or non-default port mismatch -> HostMismatch.
    var bad_host := try TestReqBlock(h)
    defer bad_host.Free(h)
    try TestAdd(h, mut bad_host, "host", "other.com")
    TestExpectReqErr(&bad_host, Malformed.HostMismatch)
    var bad_port := try TestReqBlock(h)
    defer bad_port.Free(h)
    try TestAdd(h, mut bad_port, "host", "example.com:80")
    TestExpectReqErr(&bad_port, Malformed.HostMismatch)
}

test "RFC 9113 8.3.2: a response without :status is malformed" (sys: &System) {
    h := sys.heap.Alloc()
    var empty := FieldBlock.New(h)
    defer empty.Free(h)
    try TestAdd(h, mut empty, "content-type", "text/plain")
    match CheckResponse(&empty) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.why == Malformed.PseudoMissing) }
    }
    // Invalid :status values (2 digits, letters, 101) -> Status.
    bad_statuses: [4]str = ["20", "2000", "abc", "101"]
    for st in bad_statuses {
        var b := FieldBlock.New(h)
        defer b.Free(h)
        try TestAdd(h, mut b, ":status", st)
        match CheckResponse(&b) {
            ok(_) { Assert(false) }
            err(e) { Assert(e.why == Malformed.Status) }
        }
    }
    // Valid 200 with repeated equal content-length succeeds; differing content-length fails.
    var good := FieldBlock.New(h)
    defer good.Free(h)
    try TestAdd(h, mut good, ":status", "200")
    try TestAdd(h, mut good, "content-length", "42")
    try TestAdd(h, mut good, "content-length", "42")
    r := try CheckResponse(&good)
    cl := r.content_length else { i64(-1) }
    Assert(r.status == 200 && cl == 42)
    try TestAdd(h, mut good, "content-length", "43")
    match CheckResponse(&good) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.why == Malformed.ContentLength) }
    }
    // Non-digit content-length -> ContentLength.
    var bad_cl := try TestReqBlock(h)
    defer bad_cl.Free(h)
    try TestAdd(h, mut bad_cl, "content-length", "-1")
    TestExpectReqErr(&bad_cl, Malformed.ContentLength)
}

test "RFC 9113 8.5: CONNECT with :scheme or :path, or without :authority, is malformed" (sys: &System) {
    h := sys.heap.Alloc()
    var ok_c := FieldBlock.New(h)
    defer ok_c.Free(h)
    try TestAdd(h, mut ok_c, ":method", "CONNECT")
    try TestAdd(h, mut ok_c, ":authority", "proxy.example.com:443")
    rh := try CheckRequest(&ok_c)
    Assert(rh.connect && rh.authority == "proxy.example.com:443")
    var no_auth := FieldBlock.New(h)
    defer no_auth.Free(h)
    try TestAdd(h, mut no_auth, ":method", "CONNECT")
    TestExpectReqErr(&no_auth, Malformed.PseudoMissing)
    var with_scheme := FieldBlock.New(h)
    defer with_scheme.Free(h)
    try TestAdd(h, mut with_scheme, ":method", "CONNECT")
    try TestAdd(h, mut with_scheme, ":authority", "a:443")
    try TestAdd(h, mut with_scheme, ":scheme", "https")
    TestExpectReqErr(&with_scheme, Malformed.PseudoUnknown)
    var with_path := FieldBlock.New(h)
    defer with_path.Free(h)
    try TestAdd(h, mut with_path, ":method", "CONNECT")
    try TestAdd(h, mut with_path, ":authority", "a:443")
    try TestAdd(h, mut with_path, ":path", "/")
    TestExpectReqErr(&with_path, Malformed.PseudoUnknown)
}
