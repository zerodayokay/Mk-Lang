// Why: QUIC packets have long, short, Retry and Version Negotiation headers
// that must be parsed from untrusted datagrams before keys are selected, and
// packet numbers are truncated and recovered modulo a sliding window.
//
// What: LongHeader, ShortHeader, UnknownHeader, Parsed, ParsePacket,
// DecodePn, EncodePnLen, CheckReservedBits, Cid helpers, and header writers
// (WriteVersionNeg, WriteLongHeader, WriteShortHeader).
//
// How: RFC 9000 sections 17.1-17.4 and Appendix A.2-A.3. Every length and
// offset is bounds-checked before slicing so no malformed or coalesced
// datagram can panic, and unknown versions parse only the version-invariant
// CID fields (up to 255 bytes) so Version Negotiation never applies v1 limits.

// Long header packet types (RFC 9000 17.2 Table 5).
const PktInitial: u8 = 0
const PktZeroRtt: u8 = 1
const PktHandshake: u8 = 2
const PktRetry: u8 = 3

// Minimum UDP datagram payload size for Initial packets (RFC 9000 14.1).
const MinInitialDatagramLen: int = 1200

// Minimum valid short-header packet length (RFC 9000 10.3: 1-byte header,
// 0-byte CID, 4-byte skipped PN + 16-byte sample/tag).
const MinShortPacketLen: int = 21

// Maximum packet number in QUIC (RFC 9000 12.3: 2^62 - 1).
const MaxPacketNum: i64 = 4_611_686_018_427_387_903

// LongHeader holds the unprotected fields of a QUIC v1 Initial, 0-RTT or
// Handshake packet header (RFC 9000 17.2).
struct LongHeader {
    kind: u8
    version: u32
    dcid: Cid
    scid: Cid
    token: []u8
    length: int
    pn_offset: int
}

// ShortHeader holds the unprotected fields of a 1-RTT packet header
// (RFC 9000 17.3.1). Spin is exposed for diagnostics and ignored on receipt.
struct ShortHeader {
    dcid: Cid
    spin: bool
    pn_offset: int
}

// UnknownHeader holds the version-invariant header fields of a long-header
// packet carrying an unsupported non-zero version (RFC 9000 17.2, 17.2.1).
struct UnknownHeader {
    version: u32
    dcid: []u8
    scid: []u8
}

// Parsed is one QUIC packet decoded up to its protected payload. Slices in
// each variant borrow from the input datagram buffer.
enum Parsed {
    Long(hdr: LongHeader, packet: []u8)
    Short(hdr: ShortHeader, packet: []u8)
    Retry(version: u32, dcid: Cid, scid: Cid, token: []u8, tag: [16]u8, packet: []u8)
    VersionNeg(dcid: []u8, scid: []u8, versions: []u8)
    UnknownVersion(hdr: UnknownHeader)
}

// CidFromSlice builds a Cid from b (at most MaxCidLen bytes; longer slices
// are truncated to MaxCidLen). O(1); allocates nothing.
CidFromSlice(b: []u8): Cid {
    var out := Cid(b: [0; MaxCidLen], n: 0)
    n := if b.Len() < MaxCidLen { b.Len() } else { MaxCidLen }
    (mut out.b[0..n]).CopyFrom(b[0..n])
    out.n = n
    return out
}

// CidBytes returns the active slice of c. O(1).
CidBytes(c: &Cid): []u8 from c {
    return c.b[0..c.n]
}

// CidEqual reports whether a and b have identical length and bytes. O(1).
CidEqual(a: &Cid, b: &Cid): bool {
    if a.n != b.n {
        return false
    }
    for i in 0..a.n {
        if a.b[i] != b.b[i] {
            return false
        }
    }
    return true
}

// ParsePacket parses the header of the next QUIC packet in b before keys are
// applied, using short_cid_len for short-header packets (RFC 9000 17).
// Returns none when b is truncated, has Fixed Bit 0 (outside VN), has a v1
// CID longer than 20 bytes, or is a short header too short for a full header
// protection sample. O(1); allocates nothing.
ParsePacket(b: []u8, short_cid_len: int): ?Parsed from b {
    if b.Len() == 0 {
        return none
    }
    first := b[0]
    if (first & 0x80) != 0 {
        // Long header requires at least first byte, 4-byte version, and two 1-byte CID lengths.
        if b.Len() < 7 {
            return none
        }
        version := ReadBe32(b, 1)
        dcid_len := int(b[5])
        if b.Len() - 6 < dcid_len + 1 {
            return none
        }
        dcid_raw := b[6..6 + dcid_len]
        scid_pos := 6 + dcid_len
        scid_len := int(b[scid_pos])
        if b.Len() - (scid_pos + 1) < scid_len {
            return none
        }
        scid_raw := b[scid_pos + 1..scid_pos + 1 + scid_len]
        pos := scid_pos + 1 + scid_len
        if version == 0 {
            // Version Negotiation ignores Unused bits and reads full 8-bit CID lengths (RFC 9000 17.2.1).
            rem := b.Len() - pos
            if rem < 4 || rem % 4 != 0 {
                return none
            }
            return some(Parsed.VersionNeg(dcid: dcid_raw, scid: scid_raw, versions: b[pos..b.Len()]))
        }
        if version != Version1 {
            // Unsupported version: keep full 8-bit CID slices for Version Negotiation (RFC 9000 17.2.1).
            return some(Parsed.UnknownVersion(hdr: UnknownHeader(version: version, dcid: dcid_raw, scid: scid_raw)))
        }
        if (first & 0x40) == 0 {
            return none
        }
        if dcid_len > MaxCidLen || scid_len > MaxCidLen {
            return none
        }
        dcid := CidFromSlice(dcid_raw)
        scid := CidFromSlice(scid_raw)
        kind := (first >> 4) & 0x03
        if kind == PktRetry {
            if b.Len() - pos < 16 {
                return none
            }
            split := b.Len() - 16
            var tag: [16]u8 = [0; 16]
            (mut tag[..]).CopyFrom(b[split..b.Len()])
            return some(Parsed.Retry(version: version, dcid: dcid, scid: scid, token: b[pos..split], tag: tag,
                packet: b[0..split]))
        }
        var token := b[0..0]
        var cur := pos
        if kind == PktInitial {
            tok := ReadVarint(b, cur) else {
                return none
            }
            cur += tok.n
            if tok.v > Trunc[u64](b.Len() - cur) {
                return none
            }
            token = b[cur..cur + Trunc[int](tok.v)]
            cur += Trunc[int](tok.v)
        }
        lp := ReadVarint(b, cur) else {
            return none
        }
        pn_offset := cur + lp.n
        if lp.v > Trunc[u64](b.Len() - pn_offset) {
            return none
        }
        length := Trunc[int](lp.v)
        return some(Parsed.Long(hdr: LongHeader(kind: kind, version: version, dcid: dcid, scid: scid, token: token,
            length: length, pn_offset: pn_offset), packet: b[0..pn_offset + length]))
    }
    // Short header (1-RTT): Fixed Bit must be 1 (RFC 9000 17.3.1), minimum 21 bytes (10.3),
    // and long enough for a 4-byte skipped PN + 16-byte sample (RFC 9001 5.4.2).
    if (first & 0x40) == 0 {
        return none
    }
    if short_cid_len < 0 || short_cid_len > MaxCidLen {
        return none
    }
    if b.Len() < MinShortPacketLen {
        return none
    }
    pn_offset := 1 + short_cid_len
    if b.Len() < pn_offset + 20 {
        return none
    }
    dcid := CidFromSlice(b[1..1 + short_cid_len])
    spin := (first & 0x20) != 0
    return some(Parsed.Short(hdr: ShortHeader(dcid: dcid, spin: spin, pn_offset: pn_offset), packet: b[..]))
}

// CheckReservedBits checks that the header-protected reserved bits are zero
// AFTER both header protection and AEAD packet protection have succeeded
// (RFC 9000 17.2, 17.3.1). Fails with TransportError(ErrProtocolViolation, 0)
// when any reserved bit is set. O(1).
CheckReservedBits(first: u8, long: bool): Error!void {
    mask: u8 = if long { 0x0c } else { 0x18 }
    if (first & mask) != 0 {
        fail TransportError(code: ErrProtocolViolation, frame: 0)
    }
}

// EncodePnLen selects the packet number byte length (1, 2, 3 or 4) needed to
// represent more than twice the unacknowledged range (RFC 9000 17.1, A.2).
// Pass largest_acked < 0 before any packet in the space has been acknowledged.
// O(1); allocates nothing.
EncodePnLen(pn: i64, largest_acked: i64): int {
    var num_unacked: i64 = if largest_acked < 0 {
        pn + 1
    } else {
        pn - largest_acked
    }
    if num_unacked < 1 {
        num_unacked = 1
    }
    if num_unacked <= 0x80 {
        return 1
    }
    if num_unacked <= 0x8000 {
        return 2
    }
    if num_unacked <= 0x800000 {
        return 3
    }
    return 4
}

// DecodePn reconstructs a full 62-bit packet number from its truncated
// wire value of bits (8, 16, 24 or 32) closest to largest + 1 (RFC 9000 A.3).
// Pass largest < 0 when no packet has been authenticated yet in the space.
// O(1); allocates nothing.
DecodePn(largest: i64, truncated: u64, bits: int): i64 {
    expected_pn: i64 = if largest < 0 { 0 } else { largest + 1 }
    pn_win: i64 = 1 << bits
    pn_hwin: i64 = pn_win / 2
    pn_mask: u64 = Trunc[u64](pn_win - 1)
    candidate_pn: i64 = Trunc[i64]((Trunc[u64](expected_pn) & (0xffff_ffff_ffff_ffff ^ pn_mask)) | (truncated & pn_mask))
    limit: i64 = 1 << 62
    if candidate_pn <= expected_pn - pn_hwin && candidate_pn < limit - pn_win {
        return candidate_pn + pn_win
    }
    if candidate_pn > expected_pn + pn_hwin && candidate_pn >= pn_win {
        return candidate_pn - pn_win
    }
    return candidate_pn
}

// ReadPn reads pn_len (1..4) big-endian bytes starting at b[at]. Caller
// ensures 1 <= pn_len <= 4 and b.Len() - at >= pn_len.
ReadPn(b: []u8, at: int, pn_len: int): u64 {
    var v: u64 = 0
    for i in 0..pn_len {
        v = (v << 8) | u64(b[at + i])
    }
    return v
}

// WritePn writes the lowest pn_len (1..4) bytes of pn big-endian at out[at..].
// Caller ensures 1 <= pn_len <= 4 and out.Len() - at >= pn_len.
WritePn(out: mut []u8, at: int, pn: i64, pn_len: int) {
    u := Trunc[u64](pn)
    for i in 0..pn_len {
        shift := 8 * (pn_len - 1 - i)
        out[at + i] = Trunc[u8](u >> shift)
    }
}

// WriteVersionNeg writes a Version Negotiation packet into out (RFC 9000 17.2.1),
// returning bytes written or 0 if out is too short or CIDs exceed 255 bytes.
// O(versions.Len()); allocates nothing.
WriteVersionNeg(out: mut []u8, unused: u8, dcid: []u8, scid: []u8, versions: []u32): int {
    if dcid.Len() > 255 || scid.Len() > 255 || versions.Len() == 0 {
        return 0
    }
    need := 7 + dcid.Len() + scid.Len() + 4 * versions.Len()
    if out.Len() < need {
        return 0
    }
    out[0] = 0x80 | (unused & 0x7f)
    PutBe32(out, 1, 0)
    out[5] = Trunc[u8](dcid.Len())
    (mut out[6..6 + dcid.Len()]).CopyFrom(dcid)
    var pos := 6 + dcid.Len()
    out[pos] = Trunc[u8](scid.Len())
    pos += 1
    (mut out[pos..pos + scid.Len()]).CopyFrom(scid)
    pos += scid.Len()
    for i in 0..versions.Len() {
        PutBe32(out, pos, versions[i])
        pos += 4
    }
    return pos
}

// WriteLongHeader writes an unprotected QUIC v1 Initial, 0-RTT or Handshake
// header (including the Length varint and pn_len-byte Packet Number) into out,
// returning (total_header_len, pn_offset) or none when out is too short or
// arguments are out of range. Reserved bits are 0. O(1); allocates nothing.
WriteLongHeader(out: mut []u8, kind: u8, dcid: &Cid, scid: &Cid, token: []u8, body_and_tag_len: int, pn: i64,
    pn_len: int): ?(header_len: int, pn_offset: int) {
    if kind > PktHandshake || dcid.n > MaxCidLen || scid.n > MaxCidLen || pn_len < 1 || pn_len > 4 ||
        body_and_tag_len < 0 {
        return none
    }
    if kind != PktInitial && token.Len() > 0 {
        return none
    }
    length_val := Trunc[u64](pn_len + body_and_tag_len)
    len_var_bytes := VarintLen(length_val)
    if len_var_bytes == 0 {
        return none
    }
    var tok_bytes := 0
    if kind == PktInitial {
        tl := VarintLen(Trunc[u64](token.Len()))
        if tl == 0 {
            return none
        }
        tok_bytes = tl + token.Len()
    }
    pn_offset := 7 + dcid.n + scid.n + tok_bytes + len_var_bytes
    header_len := pn_offset + pn_len
    if out.Len() < header_len {
        return none
    }
    out[0] = 0xc0 | ((kind & 0x03) << 4) | Trunc[u8](pn_len - 1)
    PutBe32(out, 1, Version1)
    out[5] = Trunc[u8](dcid.n)
    (mut out[6..6 + dcid.n]).CopyFrom(dcid.b[0..dcid.n])
    var pos := 6 + dcid.n
    out[pos] = Trunc[u8](scid.n)
    pos += 1
    (mut out[pos..pos + scid.n]).CopyFrom(scid.b[0..scid.n])
    pos += scid.n
    if kind == PktInitial {
        pos += WriteVarint(mut out[pos..], Trunc[u64](token.Len()))
        (mut out[pos..pos + token.Len()]).CopyFrom(token)
        pos += token.Len()
    }
    pos += WriteVarint(mut out[pos..], length_val)
    WritePn(out, pos, pn, pn_len)
    return some((header_len: header_len, pn_offset: pn_offset))
}

// WriteShortHeader writes an unprotected 1-RTT short header with Fixed Bit 1,
// Spin Bit 0 (RFC 9000 17.4), Reserved Bits 0, Key Phase key_phase and pn_len
// bytes of pn into out. Returns (header_len, pn_offset) or none when out is
// too short. O(1); allocates nothing.
WriteShortHeader(out: mut []u8, dcid: &Cid, key_phase: bool, pn: i64, pn_len: int): ?(header_len: int, pn_offset: int) {
    if dcid.n > MaxCidLen || pn_len < 1 || pn_len > 4 {
        return none
    }
    pn_offset := 1 + dcid.n
    header_len := pn_offset + pn_len
    if out.Len() < header_len {
        return none
    }
    kp: u8 = if key_phase { 0x04 } else { 0x00 }
    out[0] = 0x40 | kp | Trunc[u8](pn_len - 1)
    (mut out[1..1 + dcid.n]).CopyFrom(dcid.b[0..dcid.n])
    WritePn(out, pn_offset, pn, pn_len)
    return some((header_len: header_len, pn_offset: pn_offset))
}
