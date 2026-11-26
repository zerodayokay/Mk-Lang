// Why: transport parameters negotiate limits and authenticate connection IDs;
// every malformed length, out-of-range integer, duplicate ID or role-forbidden
// parameter must fail with TRANSPORT_PARAMETER_ERROR.
//
// What: test blocks for params.mk (RFC 9000 4.6, 7.4, 18.2).
//
// How: verifies every QT transport-parameter row with exact error checks,
// default values, encoder round-trips and client server-only omission.

AssertParamsErr(b: []u8, from_client: bool) {
    var got := false
    _ = DecodeParams(b, from_client) else e {
        match e {
            TransportError(code, frame: _) {
                Assert(code == ErrTransportParameter)
                got = true
            }
            _ { Panic("unexpected error") }
        }
    }
    Assert(got)
}

test "RFC 9000 4.6: a max_streams transport parameter above 2^60 is TRANSPORT_PARAMETER_ERROR" (sys: &System) {
    // initial_max_streams_bidi (0x08) = 2^60 + 1
    var b1: [16]u8 = [0; 16]
    b1[0] = 0x08
    b1[1] = 8
    _ = WriteVarint(mut b1[2..], MaxStreamsLimit + 1)
    AssertParamsErr(b1[0..10], false)

    // initial_max_streams_uni (0x09) = 2^60 + 1
    b1[0] = 0x09
    AssertParamsErr(b1[0..10], false)

    // Exact 2^60 succeeds.
    _ = WriteVarint(mut b1[2..], MaxStreamsLimit)
    p := DecodeParams(b1[0..10], false).Must()
    Assert(p.initial_max_streams_uni == MaxStreamsLimit)
}

test "RFC 9000 4.6: initial_max_streams above 2^60 is TRANSPORT_PARAMETER_ERROR" (sys: &System) {
    var b: [16]u8 = [0; 16]
    b[0] = 0x08
    b[1] = 8
    _ = WriteVarint(mut b[2..], MaxStreamsLimit + 1)
    AssertParamsErr(b[0..10], true)
}

test "RFC 9000 7.4: a malformed or truncated transport parameter is TRANSPORT_PARAMETER_ERROR" (sys: &System) {
    // Truncated ID.
    AssertParamsErr(b"\x40", false)
    // Truncated length.
    AssertParamsErr(b"\x01", false)
    // Length past end of buffer.
    AssertParamsErr(b"\x01\x04\x00\x00", false)
    // Integer varint does not fill parameter length (len=4, 1-byte varint 0x05 + 3 trailing bytes).
    AssertParamsErr(b"\x01\x04\x05\x00\x00\x00", false)
    // Truncated varint inside integer parameter.
    AssertParamsErr(b"\x01\x01\x40", false)
    // CID parameter > 20 bytes (initial_source_connection_id 0x0f).
    var long_cid: [23]u8 = [0; 23]
    long_cid[0] = 0x0f
    long_cid[1] = 21
    AssertParamsErr(long_cid[..], false)
}

test "RFC 9000 7.4: the encoder writes each transport parameter once" (sys: &System) {
    var p := DefaultParams(false)
    p.original_dcid = some(CidFromSlice(b"\x01\x02\x03\x04"))
    p.initial_scid = some(CidFromSlice(b"\x05\x06\x07\x08"))
    p.retry_scid = some(CidFromSlice(b"\x09\x0a\x0b\x0c"))
    var tok: [16]u8 = [0x55; 16]
    p.stateless_reset_token = some(tok)
    p.max_idle_timeout = 30000
    p.initial_max_data = 65536
    p.initial_max_stream_data_bidi_local = 16384
    p.initial_max_stream_data_bidi_remote = 16384
    p.initial_max_stream_data_uni = 16384
    p.initial_max_streams_bidi = 100
    p.initial_max_streams_uni = 3

    var out: [256]u8 = [0; 256]
    n := EncodeParams(mut out[..], &p)
    Assert(n > 0)
    // DecodeParams rejects any duplicate parameter, so decoding proves uniqueness.
    dec := DecodeParams(out[0..n], false).Must()
    Assert(dec.max_idle_timeout == 30000 && dec.initial_max_streams_bidi == 100)
    Assert(dec.disable_active_migration)
    _ = dec.stateless_reset_token.Must()
}

test "RFC 9000 7.4: a duplicated transport parameter is TRANSPORT_PARAMETER_ERROR" (sys: &System) {
    // max_idle_timeout (0x01) twice.
    AssertParamsErr(b"\x01\x01\x10\x01\x01\x20", false)
    // Unknown parameter (0x40 0x42 = 0x42) twice.
    AssertParamsErr(b"\x40\x42\x01\xaa\x40\x42\x01\xbb", false)
}

test "RFC 9000 7.4.2: unknown and reserved transport parameters are ignored" (sys: &System) {
    // Unknown id 0x40 0x2a (len 2) + GREASE id 31*1+27 = 58 = 0x3a (len 0) + max_idle_timeout 0x01 = 50.
    buf: [10]u8 = [0x40, 0x2a, 0x02, 0xde, 0xad, 0x3a, 0x00, 0x01, 0x01, 0x32]
    p := DecodeParams(buf[..], false).Must()
    Assert(p.max_idle_timeout == 50)
}

test "RFC 9000 18.2: max_udp_payload_size below 1200 is TRANSPORT_PARAMETER_ERROR" (sys: &System) {
    // 1199 = 0x44af
    AssertParamsErr(b"\x03\x02\x44\xaf", false)
    // 1200 = 0x44b0 succeeds
    p := DecodeParams(b"\x03\x02\x44\xb0", false).Must()
    Assert(p.max_udp_payload_size == 1200)
    // Absent defaults to 65527.
    def := DecodeParams(b"", false).Must()
    Assert(def.max_udp_payload_size == 65527)
}

test "RFC 9000 18.2: ack_delay_exponent above 20 is TRANSPORT_PARAMETER_ERROR" (sys: &System) {
    AssertParamsErr(b"\x0a\x01\x15", false)
    p := DecodeParams(b"\x0a\x01\x14", false).Must()
    Assert(p.ack_delay_exponent == 20)
}

test "RFC 9000 18.2: max_ack_delay of 2^14 or more is TRANSPORT_PARAMETER_ERROR" (sys: &System) {
    // 16384 = 0x80004000 (4-byte varint)
    AssertParamsErr(b"\x0b\x04\x80\x00\x40\x00", false)
    // 16383 = 0x7fff (2-byte varint) succeeds
    p := DecodeParams(b"\x0b\x02\x7f\xff", false).Must()
    Assert(p.max_ack_delay == 16383)
}

test "RFC 9000 18.2: active_connection_id_limit below 2 is TRANSPORT_PARAMETER_ERROR" (sys: &System) {
    AssertParamsErr(b"\x0e\x01\x01", false)
    AssertParamsErr(b"\x0e\x01\x00", false)
    p := DecodeParams(b"\x0e\x01\x02", false).Must()
    Assert(p.active_connection_id_limit == 2)
}

test "RFC 9000 18.2: we send max_udp_payload_size 1472, ack_delay_exponent 3, max_ack_delay 25 and active_connection_id_limit 4" (sys: &System) {
    p := DefaultParams(true)
    var out: [128]u8 = [0; 128]
    n := EncodeParams(mut out[..], &p)
    Assert(n > 0)
    dec := DecodeParams(out[0..n], true).Must()
    Assert(dec.max_udp_payload_size == 1472)
    Assert(dec.ack_delay_exponent == 3)
    Assert(dec.max_ack_delay == 25)
    Assert(dec.active_connection_id_limit == 4)
}

test "RFC 9000 18.2: stateless_reset_token of a length other than 16 is TRANSPORT_PARAMETER_ERROR" (sys: &System) {
    var b15: [17]u8 = [0; 17]
    b15[0] = 0x02
    b15[1] = 15
    AssertParamsErr(b15[..], false)

    var b17: [19]u8 = [0; 19]
    b17[0] = 0x02
    b17[1] = 17
    AssertParamsErr(b17[..], false)

    var b16: [18]u8 = [0; 18]
    b16[0] = 0x02
    b16[1] = 16
    p := DecodeParams(b16[..], false).Must()
    _ = p.stateless_reset_token.Must()
}

test "RFC 9000 18.2: a non-empty disable_active_migration is TRANSPORT_PARAMETER_ERROR" (sys: &System) {
    AssertParamsErr(b"\x0c\x01\x00", false)
    p := DecodeParams(b"\x0c\x00", false).Must()
    Assert(p.disable_active_migration)
}

test "RFC 9000 18.2: preferred_address with a zero-length or over-long connection id is TRANSPORT_PARAMETER_ERROR" (sys: &System) {
    // preferred_address (0x0d): 4 + 2 + 16 + 2 + 1 (cid_len) + cid + 16.
    // cid_len = 0 -> total val len = 41.
    var z: [43]u8 = [0; 43]
    z[0] = 0x0d
    z[1] = 41
    z[2 + 24] = 0
    AssertParamsErr(z[..], false)

    // cid_len = 21 -> total val len = 62.
    var o: [64]u8 = [0; 64]
    o[0] = 0x0d
    o[1] = 62
    o[2 + 24] = 21
    AssertParamsErr(o[..], false)

    // cid_len = 8 -> total val len = 49 succeeds.
    var g: [51]u8 = [0; 51]
    g[0] = 0x0d
    g[1] = 49
    g[2 + 24] = 8
    g[2 + 25] = 0xab
    p := DecodeParams(g[..], false).Must()
    pa := p.preferred_address.Must()
    Assert(pa.cid.n == 8 && pa.cid.b[0] == 0xab)
}

test "RFC 9000 18.2: a server-only transport parameter from a client is TRANSPORT_PARAMETER_ERROR" (sys: &System) {
    // 0x00 original_destination_connection_id
    AssertParamsErr(b"\x00\x04\x01\x02\x03\x04", true)
    // 0x02 stateless_reset_token
    var srt: [18]u8 = [0; 18]
    srt[0] = 0x02
    srt[1] = 16
    AssertParamsErr(srt[..], true)
    // 0x0d preferred_address
    var pa: [51]u8 = [0; 51]
    pa[0] = 0x0d
    pa[1] = 49
    pa[2 + 24] = 8
    AssertParamsErr(pa[..], true)
    // 0x10 retry_source_connection_id
    AssertParamsErr(b"\x10\x04\x01\x02\x03\x04", true)
}

test "RFC 9000 18.2: the client encoder writes no server-only transport parameter" (sys: &System) {
    var p := DefaultParams(true)
    // Populate server-only fields deliberately to prove EncodeParams refuses them when p.client is true.
    p.original_dcid = some(CidFromSlice(b"\x01\x02\x03\x04"))
    var tok: [16]u8 = [0x11; 16]
    p.stateless_reset_token = some(tok)
    p.retry_scid = some(CidFromSlice(b"\x05\x06\x07\x08"))
    p.preferred_address = some(PreferredAddress(ipv4: [127, 0, 0, 1], ipv4_port: 443, ipv6: [0; 16],
        ipv6_port: 443, cid: CidFromSlice(b"\x01\x02"), reset_token: tok))

    var out: [256]u8 = [0; 256]
    n := EncodeParams(mut out[..], &p)
    Assert(n > 0)
    // Decoding with from_client=true must succeed and none of the server-only fields may be present.
    dec := DecodeParams(out[0..n], true).Must()
    AssertNone(dec.original_dcid)
    AssertNone(dec.stateless_reset_token)
    AssertNone(dec.preferred_address)
    AssertNone(dec.retry_scid)
}
