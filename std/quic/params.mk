// Why: QUIC endpoints exchange transport parameters inside the TLS
// quic_transport_parameters extension; malformed, duplicated, out-of-range
// or role-forbidden parameters must fail the handshake cleanly.
//
// What: PreferredAddress, Params, DefaultParams, DecodeParams and
// EncodeParams, with RFC 9000 18.2 parameter IDs and default constants.
//
// How: RFC 9000 sections 7.3, 7.4 and 18.2. DecodeParams validates lengths,
// exact varint widths, bounds, server-only rules and duplicate IDs without
// allocation; EncodeParams writes each parameter once and omits all
// server-only parameters when p.client is true.

// RFC 9000 18.2 transport parameter identifiers.
const ParamOriginalDcid: u64 = 0x00
const ParamMaxIdleTimeout: u64 = 0x01
const ParamStatelessResetToken: u64 = 0x02
const ParamMaxUdpPayloadSize: u64 = 0x03
const ParamInitialMaxData: u64 = 0x04
const ParamInitialMaxStreamDataBidiLocal: u64 = 0x05
const ParamInitialMaxStreamDataBidiRemote: u64 = 0x06
const ParamInitialMaxStreamDataUni: u64 = 0x07
const ParamInitialMaxStreamsBidi: u64 = 0x08
const ParamInitialMaxStreamsUni: u64 = 0x09
const ParamAckDelayExponent: u64 = 0x0a
const ParamMaxAckDelay: u64 = 0x0b
const ParamDisableActiveMigration: u64 = 0x0c
const ParamPreferredAddress: u64 = 0x0d
const ParamActiveConnectionIdLimit: u64 = 0x0e
const ParamInitialScid: u64 = 0x0f
const ParamRetryScid: u64 = 0x10

// RFC 9000 18.2 defaults and bounds.
const DefaultMaxUdpPayloadSize: u64 = 65527
const MinMaxUdpPayloadSize: u64 = 1200
const SendMaxUdpPayloadSize: u64 = 1472
const DefaultAckDelayExponent: u64 = 3
const MaxAckDelayExponent: u64 = 20
const DefaultMaxAckDelay: u64 = 25
const MaxAckDelayLimit: u64 = 16384
const DefaultActiveCidLimit: u64 = 2
const SendActiveCidLimit: u64 = 4

// PreferredAddress holds a server's preferred_address transport parameter
// (RFC 9000 18.2).
struct PreferredAddress {
    ipv4: [4]u8
    ipv4_port: u16
    ipv6: [16]u8
    ipv6_port: u16
    cid: Cid
    reset_token: [16]u8
}

// Params holds all RFC 9000 18.2 transport parameters. client controls
// whether EncodeParams omits server-only parameters.
struct Params {
    client: bool
    original_dcid: ?Cid
    max_idle_timeout: u64
    stateless_reset_token: ?[16]u8
    max_udp_payload_size: u64
    initial_max_data: u64
    initial_max_stream_data_bidi_local: u64
    initial_max_stream_data_bidi_remote: u64
    initial_max_stream_data_uni: u64
    initial_max_streams_bidi: u64
    initial_max_streams_uni: u64
    ack_delay_exponent: u64
    max_ack_delay: u64
    disable_active_migration: bool
    preferred_address: ?PreferredAddress
    active_connection_id_limit: u64
    initial_scid: ?Cid
    retry_scid: ?Cid
}

// DefaultParams returns the local transport parameters advertised by this
// implementation for the given role (RFC 9000 18.2, 5.2.3). O(1).
DefaultParams(client: bool): Params {
    return Params(client: client, original_dcid: none, max_idle_timeout: 0, stateless_reset_token: none,
        max_udp_payload_size: SendMaxUdpPayloadSize, initial_max_data: 0, initial_max_stream_data_bidi_local: 0,
        initial_max_stream_data_bidi_remote: 0, initial_max_stream_data_uni: 0, initial_max_streams_bidi: 0,
        initial_max_streams_uni: 0, ack_delay_exponent: DefaultAckDelayExponent, max_ack_delay: DefaultMaxAckDelay,
        disable_active_migration: !client, preferred_address: none, active_connection_id_limit: SendActiveCidLimit,
        initial_scid: none, retry_scid: none)
}

// ReadParamInt decodes a varint parameter value that must consume every byte
// of val (RFC 9000 7.4, 18.2).
ReadParamInt(val: []u8): Error!u64 {
    r := ReadVarint(val, 0) else {
        fail TransportError(code: ErrTransportParameter, frame: 0)
    }
    if r.n != val.Len() {
        fail TransportError(code: ErrTransportParameter, frame: 0)
    }
    return r.v
}

// DecodePreferredAddress parses and validates a preferred_address parameter
// value (RFC 9000 18.2).
DecodePreferredAddress(val: []u8): Error!PreferredAddress {
    // 4 (ipv4) + 2 (port) + 16 (ipv6) + 2 (port) + 1 (cid_len) + 1..20 (cid) + 16 (token)
    if val.Len() < 42 || val.Len() > 61 {
        fail TransportError(code: ErrTransportParameter, frame: 0)
    }
    cid_len := int(val[24])
    if cid_len < 1 || cid_len > MaxCidLen || val.Len() != 41 + cid_len {
        fail TransportError(code: ErrTransportParameter, frame: 0)
    }
    var ipv4: [4]u8 = [0; 4]
    (mut ipv4[..]).CopyFrom(val[0..4])
    ipv4_port := ReadBe16(val, 4)
    var ipv6: [16]u8 = [0; 16]
    (mut ipv6[..]).CopyFrom(val[6..22])
    ipv6_port := ReadBe16(val, 22)
    cid := CidFromSlice(val[25..25 + cid_len])
    var tok: [16]u8 = [0; 16]
    (mut tok[..]).CopyFrom(val[25 + cid_len..41 + cid_len])
    return PreferredAddress(ipv4: ipv4, ipv4_port: ipv4_port, ipv6: ipv6, ipv6_port: ipv6_port, cid: cid,
        reset_token: tok)
}

// DecodeParams parses and validates RFC 9000 18.2 transport parameters from
// b received from a client when from_client is true. Fails with
// TransportError(ErrTransportParameter, 0) on truncation, malformed values,
// duplicates, out-of-range limits or server-only parameters from a client.
// O(b.Len()); allocates nothing.
DecodeParams(b: []u8, from_client: bool): Error!Params {
    var p := Params(client: from_client, original_dcid: none, max_idle_timeout: 0, stateless_reset_token: none,
        max_udp_payload_size: DefaultMaxUdpPayloadSize, initial_max_data: 0, initial_max_stream_data_bidi_local: 0,
        initial_max_stream_data_bidi_remote: 0, initial_max_stream_data_uni: 0, initial_max_streams_bidi: 0,
        initial_max_streams_uni: 0, ack_delay_exponent: DefaultAckDelayExponent, max_ack_delay: DefaultMaxAckDelay,
        disable_active_migration: false, preferred_address: none, active_connection_id_limit: DefaultActiveCidLimit,
        initial_scid: none, retry_scid: none)
    var seen: u32 = 0
    var unk: [32]u64 = [0; 32]
    var unk_n := 0
    var at := 0
    for at < b.Len() {
        idr := ReadVarint(b, at) else {
            fail TransportError(code: ErrTransportParameter, frame: 0)
        }
        at += idr.n
        lr := ReadVarint(b, at) else {
            fail TransportError(code: ErrTransportParameter, frame: 0)
        }
        at += lr.n
        if lr.v > Trunc[u64](b.Len() - at) {
            fail TransportError(code: ErrTransportParameter, frame: 0)
        }
        plen := Trunc[int](lr.v)
        val := b[at..at + plen]
        at += plen
        id := idr.v
        if id <= ParamRetryScid {
            bit := u32(1) << Trunc[int](id)
            if (seen & bit) != 0 {
                fail TransportError(code: ErrTransportParameter, frame: 0)
            }
            seen = seen | bit
        } else {
            for i in 0..unk_n {
                if unk[i] == id {
                    fail TransportError(code: ErrTransportParameter, frame: 0)
                }
            }
            if unk_n < 32 {
                unk[unk_n] = id
                unk_n += 1
            }
            continue
        }
        if from_client && (id == ParamOriginalDcid || id == ParamStatelessResetToken || id == ParamPreferredAddress ||
            id == ParamRetryScid) {
            fail TransportError(code: ErrTransportParameter, frame: 0)
        }
        match id {
            0x00 {
                if plen > MaxCidLen {
                    fail TransportError(code: ErrTransportParameter, frame: 0)
                }
                p.original_dcid = some(CidFromSlice(val))
            }
            0x01 {
                p.max_idle_timeout = try ReadParamInt(val)
            }
            0x02 {
                if plen != 16 {
                    fail TransportError(code: ErrTransportParameter, frame: 0)
                }
                var tok: [16]u8 = [0; 16]
                (mut tok[..]).CopyFrom(val)
                p.stateless_reset_token = some(tok)
            }
            0x03 {
                v := try ReadParamInt(val)
                if v < MinMaxUdpPayloadSize {
                    fail TransportError(code: ErrTransportParameter, frame: 0)
                }
                p.max_udp_payload_size = v
            }
            0x04 {
                p.initial_max_data = try ReadParamInt(val)
            }
            0x05 {
                p.initial_max_stream_data_bidi_local = try ReadParamInt(val)
            }
            0x06 {
                p.initial_max_stream_data_bidi_remote = try ReadParamInt(val)
            }
            0x07 {
                p.initial_max_stream_data_uni = try ReadParamInt(val)
            }
            0x08 {
                v := try ReadParamInt(val)
                if v > MaxStreamsLimit {
                    fail TransportError(code: ErrTransportParameter, frame: 0)
                }
                p.initial_max_streams_bidi = v
            }
            0x09 {
                v := try ReadParamInt(val)
                if v > MaxStreamsLimit {
                    fail TransportError(code: ErrTransportParameter, frame: 0)
                }
                p.initial_max_streams_uni = v
            }
            0x0a {
                v := try ReadParamInt(val)
                if v > MaxAckDelayExponent {
                    fail TransportError(code: ErrTransportParameter, frame: 0)
                }
                p.ack_delay_exponent = v
            }
            0x0b {
                v := try ReadParamInt(val)
                if v >= MaxAckDelayLimit {
                    fail TransportError(code: ErrTransportParameter, frame: 0)
                }
                p.max_ack_delay = v
            }
            0x0c {
                if plen != 0 {
                    fail TransportError(code: ErrTransportParameter, frame: 0)
                }
                p.disable_active_migration = true
            }
            0x0d {
                p.preferred_address = some(try DecodePreferredAddress(val))
            }
            0x0e {
                v := try ReadParamInt(val)
                if v < 2 {
                    fail TransportError(code: ErrTransportParameter, frame: 0)
                }
                p.active_connection_id_limit = v
            }
            0x0f {
                if plen > MaxCidLen {
                    fail TransportError(code: ErrTransportParameter, frame: 0)
                }
                p.initial_scid = some(CidFromSlice(val))
            }
            _ {
                if plen > MaxCidLen {
                    fail TransportError(code: ErrTransportParameter, frame: 0)
                }
                p.retry_scid = some(CidFromSlice(val))
            }
        }
    }
    return p
}

WriteParamBytes(out: mut []u8, pos: mut int, id: u64, val: []u8): bool {
    il := VarintLen(id)
    ll := VarintLen(Trunc[u64](val.Len()))
    if il == 0 || ll == 0 || out.Len() - *pos < il + ll + val.Len() {
        return false
    }
    *pos += WriteVarint(mut out[*pos..], id)
    *pos += WriteVarint(mut out[*pos..], Trunc[u64](val.Len()))
    (mut out[*pos..*pos + val.Len()]).CopyFrom(val)
    *pos += val.Len()
    return true
}

WriteParamInt(out: mut []u8, pos: mut int, id: u64, v: u64): bool {
    vl := VarintLen(v)
    il := VarintLen(id)
    if vl == 0 || il == 0 || out.Len() - *pos < il + 1 + vl {
        return false
    }
    *pos += WriteVarint(mut out[*pos..], id)
    *pos += WriteVarint(mut out[*pos..], Trunc[u64](vl))
    *pos += WriteVarint(mut out[*pos..], v)
    return true
}

// EncodeParams encodes p into out (RFC 9000 18.2), writing each parameter at
// most once and omitting all server-only parameters when p.client is true.
// Returns the number of bytes written, or 0 if out is too short or a field
// exceeds varint limits. O(1); allocates nothing.
EncodeParams(out: mut []u8, p: &Params): int {
    var pos := 0
    if !p.client {
        if cid := &p.original_dcid {
            if !WriteParamBytes(out, mut pos, ParamOriginalDcid, CidBytes(cid)) {
                return 0
            }
        }
        if tok := &p.stateless_reset_token {
            if !WriteParamBytes(out, mut pos, ParamStatelessResetToken, tok[..]) {
                return 0
            }
        }
        if pa := &p.preferred_address {
            var buf: [61]u8 = [0; 61]
            (mut buf[0..4]).CopyFrom(pa.ipv4[..])
            PutBe16(mut buf[..], 4, pa.ipv4_port)
            (mut buf[6..22]).CopyFrom(pa.ipv6[..])
            PutBe16(mut buf[..], 22, pa.ipv6_port)
            if pa.cid.n < 1 || pa.cid.n > MaxCidLen {
                return 0
            }
            buf[24] = Trunc[u8](pa.cid.n)
            (mut buf[25..25 + pa.cid.n]).CopyFrom(pa.cid.b[0..pa.cid.n])
            (mut buf[25 + pa.cid.n..41 + pa.cid.n]).CopyFrom(pa.reset_token[..])
            if !WriteParamBytes(out, mut pos, ParamPreferredAddress, buf[0..41 + pa.cid.n]) {
                return 0
            }
        }
        if cid := &p.retry_scid {
            if !WriteParamBytes(out, mut pos, ParamRetryScid, CidBytes(cid)) {
                return 0
            }
        }
    }
    if p.max_idle_timeout > 0 {
        if !WriteParamInt(out, mut pos, ParamMaxIdleTimeout, p.max_idle_timeout) {
            return 0
        }
    }
    if !WriteParamInt(out, mut pos, ParamMaxUdpPayloadSize, p.max_udp_payload_size) {
        return 0
    }
    if p.initial_max_data > 0 {
        if !WriteParamInt(out, mut pos, ParamInitialMaxData, p.initial_max_data) {
            return 0
        }
    }
    if p.initial_max_stream_data_bidi_local > 0 {
        if !WriteParamInt(out, mut pos, ParamInitialMaxStreamDataBidiLocal, p.initial_max_stream_data_bidi_local) {
            return 0
        }
    }
    if p.initial_max_stream_data_bidi_remote > 0 {
        if !WriteParamInt(out, mut pos, ParamInitialMaxStreamDataBidiRemote, p.initial_max_stream_data_bidi_remote) {
            return 0
        }
    }
    if p.initial_max_stream_data_uni > 0 {
        if !WriteParamInt(out, mut pos, ParamInitialMaxStreamDataUni, p.initial_max_stream_data_uni) {
            return 0
        }
    }
    if p.initial_max_streams_bidi > 0 {
        if !WriteParamInt(out, mut pos, ParamInitialMaxStreamsBidi, p.initial_max_streams_bidi) {
            return 0
        }
    }
    if p.initial_max_streams_uni > 0 {
        if !WriteParamInt(out, mut pos, ParamInitialMaxStreamsUni, p.initial_max_streams_uni) {
            return 0
        }
    }
    if !WriteParamInt(out, mut pos, ParamAckDelayExponent, p.ack_delay_exponent) {
        return 0
    }
    if !WriteParamInt(out, mut pos, ParamMaxAckDelay, p.max_ack_delay) {
        return 0
    }
    if p.disable_active_migration {
        if !WriteParamBytes(out, mut pos, ParamDisableActiveMigration, b"") {
            return 0
        }
    }
    if !WriteParamInt(out, mut pos, ParamActiveConnectionIdLimit, p.active_connection_id_limit) {
        return 0
    }
    if cid := &p.initial_scid {
        if !WriteParamBytes(out, mut pos, ParamInitialScid, CidBytes(cid)) {
            return 0
        }
    }
    return pos
}
