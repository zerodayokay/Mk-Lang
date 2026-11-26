// Why: SETTINGS frames negotiate frame size, concurrency, header table size
// and initial windows; each malformed or out-of-range setting must fail with
// the exact RFC 9113 6.5 error code without partial mutation.
//
// What: unit tests for Settings, InitialSettings, Settings.Apply, and
// WriteSettings.
//
// How: builds raw 6-byte setting payloads and SETTINGS frames, checks every
// error rule and order rule of RFC 9113 6.5, and round-trips WriteSettings.

// TestSetEntry encodes one 6-byte setting entry into out[0..6].
TestSetEntry(out: mut []u8, id: u16, val: u32) {
    out[0] = Trunc[u8](id >> 8)
    out[1] = Trunc[u8](id)
    out[2] = Trunc[u8](val >> 24)
    out[3] = Trunc[u8](val >> 16)
    out[4] = Trunc[u8](val >> 8)
    out[5] = Trunc[u8](val)
}

test "RFC 9113 6.5: a SETTINGS ACK with a payload is a connection FRAME_SIZE_ERROR" {
    var raw: [15]u8 = [0; 15]
    raw[2] = 6
    raw[3] = TypeSettings
    raw[4] = 0x01
    h := ParseHeader(raw[..]).Must()
    match Parse(&h, raw[..], 16384) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.code == FrameSizeError && e.stream == 0) }
    }
}

test "RFC 9113 6.5: a SETTINGS length not a multiple of 6 is a connection FRAME_SIZE_ERROR" {
    var raw: [14]u8 = [0; 14]
    raw[2] = 5
    raw[3] = TypeSettings
    h := ParseHeader(raw[..]).Must()
    match Parse(&h, raw[..], 16384) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.code == FrameSizeError && e.stream == 0) }
    }
    var s := InitialSettings
    match s.Apply(raw[9..14], false) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.code == FrameSizeError && e.stream == 0) }
    }
}

test "RFC 9113 6.5.2: ENABLE_PUSH other than 0 or 1 is a connection PROTOCOL_ERROR" {
    var entries: [6]u8 = [0; 6]
    TestSetEntry(mut entries[..], 0x2, 2)
    var s := InitialSettings
    match s.Apply(entries[..], false) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.code == ProtocolError && e.stream == 0) }
    }
}

test "RFC 9113 6.5.2: a server SETTINGS with ENABLE_PUSH 1 is a connection PROTOCOL_ERROR" {
    var entries: [6]u8 = [0; 6]
    TestSetEntry(mut entries[..], 0x2, 1)
    var s := InitialSettings
    // Accepted from a client, rejected from a server.
    try s.Apply(entries[..], false)
    Assert(s.enable_push)
    match s.Apply(entries[..], true) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.code == ProtocolError && e.stream == 0) }
    }
}

test "RFC 9113 6.5.2: INITIAL_WINDOW_SIZE above 2^31-1 is a connection FLOW_CONTROL_ERROR" {
    var entries: [6]u8 = [0; 6]
    TestSetEntry(mut entries[..], 0x4, 0x8000_0000)
    var s := InitialSettings
    match s.Apply(entries[..], false) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.code == FlowControlError && e.stream == 0) }
    }
    // Exactly 2^31 - 1 succeeds.
    TestSetEntry(mut entries[..], 0x4, 0x7FFF_FFFF)
    try s.Apply(entries[..], false)
    Assert(s.initial_window == 0x7FFF_FFFF)
}

test "RFC 9113 6.5.2: MAX_FRAME_SIZE outside 2^14..2^24-1 is a connection PROTOCOL_ERROR" {
    var entries: [6]u8 = [0; 6]
    var s := InitialSettings
    TestSetEntry(mut entries[..], 0x5, 16383)
    match s.Apply(entries[..], false) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.code == ProtocolError && e.stream == 0) }
    }
    TestSetEntry(mut entries[..], 0x5, 16777216)
    match s.Apply(entries[..], false) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.code == ProtocolError && e.stream == 0) }
    }
    // Boundary values 16384 and 16777215 succeed.
    TestSetEntry(mut entries[..], 0x5, 16777215)
    try s.Apply(entries[..], false)
    Assert(s.max_frame == 16777215)
    TestSetEntry(mut entries[..], 0x5, 16384)
    try s.Apply(entries[..], false)
    Assert(s.max_frame == 16384)
}

test "RFC 9113 6.5.2: unknown setting identifiers are ignored" {
    var entries: [18]u8 = [0; 18]
    TestSetEntry(mut entries[0..6], 0x0008, 1)    // ENABLE_CONNECT_PROTOCOL
    TestSetEntry(mut entries[6..12], 0x0009, 1)   // NO_RFC7540_PRIORITIES
    TestSetEntry(mut entries[12..18], 0x0A0A, 42) // GREASE
    var s := InitialSettings
    try s.Apply(entries[..], false)
    Assert(s.table_bytes == 4096 && s.enable_push && s.initial_window == 65535 && s.max_frame == 16384)
    Assert(s.max_streams.IsNone() && s.max_header_list.IsNone())
}

test "RFC 9113 6.5.3: a setting repeated in one frame takes its last value" {
    var entries: [18]u8 = [0; 18]
    TestSetEntry(mut entries[0..6], 0x3, 100)
    TestSetEntry(mut entries[6..12], 0x4, 32768)
    TestSetEntry(mut entries[12..18], 0x3, 50)
    var s := InitialSettings
    try s.Apply(entries[..], false)
    ms := s.max_streams else { -1 }
    Assert(ms == 50 && s.initial_window == 32768)
    // An invalid second entry fails atomically without updating earlier entries.
    TestSetEntry(mut entries[0..6], 0x3, 10)
    TestSetEntry(mut entries[6..12], 0x5, 100)
    match s.Apply(entries[0..12], false) {
        ok(_) { Assert(false) }
        err(e) {
            ms2 := s.max_streams else { -1 }
            Assert(e.code == ProtocolError && ms2 == 50)
        }
    }
}

test "WriteSettings round-trips through Parse and Apply" {
    var out0: [16]u8 = [0; 16]
    // InitialSettings writes an empty 9-byte SETTINGS frame.
    n0 := try WriteSettings(mut out0[..], &InitialSettings)
    Assert(n0 == 9)
    // Custom settings: TooSmall leaves out unchanged.
    var out: [64]u8 = [0xBB; 64]
    custom := Settings(
        table_bytes: 8192,
        enable_push: false,
        max_streams: some(100),
        initial_window: 1 << 20,
        max_frame: 32768,
        max_header_list: some(32768),
    )
    match WriteSettings(mut out[0..44], &custom) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.need == 45 && out[0] == 0xBB) }
    }
    n := try WriteSettings(mut out[..], &custom)
    Assert(n == 45)
    h := ParseHeader(out[0..n]).Must()
    f := try Parse(&h, out[0..n], 16384)
    var got := InitialSettings
    match f {
        .Settings(ack, entries) {
            Assert(!ack)
            try got.Apply(entries, false)
        }
        _ { Assert(false) }
    }
    gms := got.max_streams else { -1 }
    gmhl := got.max_header_list else { -1 }
    Assert(got.table_bytes == 8192 && !got.enable_push && gms == 100)
    Assert(got.initial_window == 1 << 20 && got.max_frame == 32768 && gmhl == 32768)
}
