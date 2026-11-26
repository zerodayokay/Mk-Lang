// Why: every TLS 1.3 handshake message carries an extension block, and each
// extension must be checked before any of it is used.
//
// What: ExtIter and ExtCheck over the block, and one parser per extension:
// server_name, supported_groups, signature_algorithms, ALPN,
// supported_versions, key_share, cookie and psk_key_exchange_modes.
//
// How: ExtIter reads the block in order and ExtCheck refuses a type that
// appears twice (RFC 8446 4.2). Each parser names its RFC section. Every
// list is kept as its encoded content and walked by index, so nothing
// allocates.

// The extension types std.tls names (RFC 8446 4.2, RFC 6066, RFC 7301,
// RFC 7627, RFC 5746).
const ExtServerName: u16 = 0
const ExtSupportedGroups: u16 = 10
const ExtSignatureAlgorithms: u16 = 13
const ExtAlpn: u16 = 16
const ExtExtendedMasterSecret: u16 = 23
const ExtPreSharedKey: u16 = 41
const ExtEarlyData: u16 = 42
const ExtSupportedVersions: u16 = 43
const ExtCookie: u16 = 44
const ExtPskModes: u16 = 45
const ExtSignatureAlgorithmsCert: u16 = 50
const ExtKeyShare: u16 = 51
const ExtQuicParams: u16 = 57 // 0x39, RFC 9001 8.2
const ExtRenegotiationInfo: u16 = 0xff01

// The named groups std.tls builds (RFC 8446 4.2.7).
const GroupX25519: u16 = 0x001d
const GroupP256: u16 = 0x0017

// The signature schemes std.tls verifies (RFC 8446 4.2.3).
const SchemeEcdsaP256: u16 = 0x0403
const SchemeEcdsaP384: u16 = 0x0503
const SchemeEd25519: u16 = 0x0807
const SchemePssSha256: u16 = 0x0804
const SchemePssSha384: u16 = 0x0805
const SchemePssSha512: u16 = 0x0806
const SchemePkcs1Sha256: u16 = 0x0401
const SchemePkcs1Sha384: u16 = 0x0501
const SchemePkcs1Sha512: u16 = 0x0601

// The protocol versions on the wire.
const WireTls12: u16 = 0x0303
const WireTls13: u16 = 0x0304

// ExtIter reads an extension block's entries in order.
struct ExtIter {
    c: Cursor
}

// ExtIter.New is the entries of the block exts.
ExtIter.New(exts: []u8): ExtIter {
    return ExtIter(c: Cursor.New(exts))
}

// ExtIter.Next is the next Extension (RFC 8446 4.2): its type and
// extension_data <0..2^16-1>; none at the end.
ExtIter.Next(mut self): Protocol!?(ty: u16, data: []u8) from self.c.b {
    if self.c.Left() == 0 {
        return none
    }
    ty := try self.c.U16()
    data := try self.c.Vec16(0, 65535)
    return some((ty, data))
}

// SmallExts is the longest extension block ExtCheck checks by comparing
// types pairwise: at most 64 extensions (4 bytes each at least), so at
// most 2,016 comparisons, against zeroing an 8 KiB bitmap.
const SmallExts: int = 256

// ExtCheck reads every entry of exts and fails with Protocol when one is
// malformed or a type appears twice (RFC 8446 4.2): one pass. A block of
// at most SmallExts bytes keeps its types in a 128-byte array and
// compares each with those before it; a longer one marks a bitmap of the
// 65,536 types on the stack (8 KiB, zeroed). Either costs O(exts.Len())
// work with a constant of at most 32 (8,192 bytes zeroed for more than
// 256 bytes of block), so a message of many blocks, such as a
// Certificate's entries, is checked in time linear in its length.
ExtCheck(exts: []u8): Protocol!void {
    if exts.Len() == 0 {
        return
    }
    if exts.Len() <= SmallExts {
        var types: [64]u16 = [0; 64]
        var k := 0
        var small := ExtIter.New(exts)
        for x := try small.Next() {
            for j in 0..k {
                if types[j] == x.ty {
                    fail Protocol
                }
            }
            types[k] = x.ty
            k += 1
        }
        return
    }
    var seen: [1024]u64 = [0; 1024]
    var it := ExtIter.New(exts)
    for x := try it.Next() {
        w := int(x.ty >> 6)
        bit := u64(1) << u64(x.ty & 63)
        if seen[w] & bit != 0 {
            fail Protocol
        }
        seen[w] = seen[w] | bit
    }
}

// FindExt is the data of the extension of type ty in the block exts,
// none when it is absent. exts was checked by ExtCheck.
FindExt(exts: []u8, ty: u16): Protocol!?[]u8 from exts {
    var it := ExtIter.New(exts)
    for x := try it.Next() {
        if x.ty == ty {
            return some(x.data)
        }
    }
    return none
}

// WriteExt writes one extension: its type and data.
WriteExt(w: mut Out, ty: u16, data: []u8) {
    w.U16(ty)
    w.Vec16(data)
}

// U16List checks that b, a list's content, holds whole u16 values, and
// returns it.
U16List(b: []u8): Protocol![]u8 from b {
    if b.Len() % 2 != 0 {
        fail Protocol
    }
    return b
}

// U16At is the i-th u16 of a checked list.
U16At(b: []u8, i: int): u16 {
    return (u16(b[2 * i]) << 8) | u16(b[2 * i + 1])
}

// HasU16 reports whether the checked list b holds v.
HasU16(b: []u8, v: u16): bool {
    for i in 0..b.Len() / 2 {
        if U16At(b, i) == v {
            return true
        }
    }
    return false
}

// WriteU16List writes a list of u16 values with a length of n bytes (1 or
// 2) from the checked content b.
WriteU16List(w: mut Out, n: int, b: []u8) {
    at := w.Open(n)
    for i in 0..b.Len() / 2 {
        w.U16(U16At(b, i))
    }
    w.Close(at, n)
}

// ParseServerName reads a server_name extension's data (RFC 6066 3):
// ServerNameList <1..2^16-1> of ServerName, name_type and a HostName
// <1..2^16-1> for type 0; returns the host_name entry's name, none when
// the list names no host. The RFC allows one name of each type.
ParseServerName(data: []u8): Protocol!?[]u8 from data {
    var c := Cursor.New(data)
    list := try c.Vec16(1, 65535)
    try c.Done()
    var l := Cursor.New(list)
    var host: ?[]u8 = none
    for l.Left() > 0 {
        ty := try l.U8()
        name := try l.Vec16(1, 65535)
        if ty == 0 {
            if host.IsSome() {
                fail Protocol
            }
            host = some(name)
        }
    }
    return host
}

// WriteServerName writes a server_name extension's data naming host.
WriteServerName(w: mut Out, host: []u8) {
    at := w.Open(2)
    w.U8(0)
    w.Vec16(host)
    w.Close(at, 2)
}

// ParseGroups reads a supported_groups extension's data (RFC 8446
// 4.2.7): NamedGroupList <2..2^16-1>, returned as checked u16 content.
ParseGroups(data: []u8): Protocol![]u8 from data {
    var c := Cursor.New(data)
    list := try c.Vec16(2, 65535)
    try c.Done()
    return try U16List(list)
}

// ParseSchemes reads a signature_algorithms or signature_algorithms_cert
// extension's data (RFC 8446 4.2.3): supported_signature_algorithms
// <2..2^16-2>, returned as checked u16 content.
ParseSchemes(data: []u8): Protocol![]u8 from data {
    var c := Cursor.New(data)
    list := try c.Vec16(2, 65534)
    try c.Done()
    return try U16List(list)
}

// ParseAlpn reads an application_layer_protocol_negotiation extension's
// data (RFC 7301 3.1): ProtocolNameList <2..2^16-1> of ProtocolName
// <1..2^8-1>, returned as the checked list content, walked by
// AlpnNames.
ParseAlpn(data: []u8): Protocol![]u8 from data {
    var c := Cursor.New(data)
    list := try c.Vec16(2, 65535)
    try c.Done()
    var l := Cursor.New(list)
    for l.Left() > 0 {
        _ = try l.Vec8(1, 255)
    }
    return list
}

// AlpnNames walks a checked ProtocolNameList content.
struct AlpnNames {
    c: Cursor
}

// AlpnNames.Next is the next protocol name, none at the end.
AlpnNames.Next(mut self): ?[]u8 from self.c.b {
    if self.c.Left() == 0 {
        return none
    }
    v := self.c.Vec8(1, 255) else {
        return none
    }
    return some(v)
}

// WriteAlpn writes an ALPN extension's data from protocol names (each 1
// to 255 bytes; another length sets full).
WriteAlpn(w: mut Out, names: []str) {
    at := w.Open(2)
    for p in names {
        if p.Len() == 0 {
            w.full = true
        }
        w.Vec8(p.AsBytes())
    }
    w.Close(at, 2)
}

// AlpnFits reports whether names can be sent as a ProtocolNameList (RFC
// 7301 3.1): each name 1 to 255 bytes, the list at most 2^16-1 bytes.
// O(names); stops at the first name past the bound.
AlpnFits(names: []str): bool {
    var n := 0
    for p in names {
        if p.Len() == 0 || p.Len() > 255 {
            return false
        }
        n += 1 + p.Len()
        if n > 65535 {
            return false
        }
    }
    return true
}

// WriteAlpnList writes an ALPN extension's data from checked list
// content.
WriteAlpnList(w: mut Out, list: []u8) {
    at := w.Open(2)
    for p in AlpnNames(c: Cursor.New(list)) {
        w.Vec8(p)
    }
    w.Close(at, 2)
}

// ParseVersionsClient reads a ClientHello's supported_versions data (RFC
// 8446 4.2.1): versions <2..254>, as checked u16 content.
ParseVersionsClient(data: []u8): Protocol![]u8 from data {
    var c := Cursor.New(data)
    list := try c.Vec8(2, 254)
    try c.Done()
    return try U16List(list)
}

// ParseVersionServer reads a ServerHello's supported_versions data (RFC
// 8446 4.2.1): the selected_version.
ParseVersionServer(data: []u8): Protocol!u16 {
    var c := Cursor.New(data)
    v := try c.U16()
    try c.Done()
    return v
}

// ParseKeySharesClient reads a ClientHello's key_share data (RFC 8446
// 4.2.8): client_shares <0..2^16-1> of KeyShareEntry, each a group and
// key_exchange <1..2^16-1>, no group twice; returned as checked content,
// walked by KeyShares.
ParseKeySharesClient(data: []u8): Protocol![]u8 from data {
    var c := Cursor.New(data)
    list := try c.Vec16(0, 65535)
    try c.Done()
    var s := KeyShares(c: Cursor.New(list))
    var count := 0
    for e := try s.Next() {
        var t := KeyShares(c: Cursor.New(list))
        for _ in 0..count {
            prev := try t.Next() else {
                fail Protocol
            }
            if prev.group == e.group {
                fail Protocol
            }
        }
        count += 1
        if count > 64 {
            fail Protocol
        }
    }
    return list
}

// KeyShares walks a checked client_shares content.
struct KeyShares {
    c: Cursor
}

// KeyShares.Next is the next KeyShareEntry (RFC 8446 4.2.8), none at the
// end.
KeyShares.Next(mut self): Protocol!?(group: u16, key: []u8) from self.c.b {
    if self.c.Left() == 0 {
        return none
    }
    group := try self.c.U16()
    key := try self.c.Vec16(1, 65535)
    return some((group, key))
}

// ParseKeyShareServer reads a ServerHello's key_share data (RFC 8446
// 4.2.8): one KeyShareEntry.
ParseKeyShareServer(data: []u8): Protocol!(group: u16, key: []u8) from data {
    var c := Cursor.New(data)
    group := try c.U16()
    key := try c.Vec16(1, 65535)
    try c.Done()
    return (group, key)
}

// ParseKeyShareRetry reads a HelloRetryRequest's key_share data (RFC 8446
// 4.2.8): the selected_group.
ParseKeyShareRetry(data: []u8): Protocol!u16 {
    var c := Cursor.New(data)
    g := try c.U16()
    try c.Done()
    return g
}

// WriteKeyShare writes one KeyShareEntry: group and key.
WriteKeyShare(w: mut Out, group: u16, key: []u8) {
    w.U16(group)
    w.Vec16(key)
}

// ParseCookie reads a cookie extension's data (RFC 8446 4.2.2): cookie
// <1..2^16-1>.
ParseCookie(data: []u8): Protocol![]u8 from data {
    var c := Cursor.New(data)
    cookie := try c.Vec16(1, 65535)
    try c.Done()
    return cookie
}

// ParsePskModes reads a psk_key_exchange_modes extension's data (RFC 8446
// 4.2.9): ke_modes <1..255>.
ParsePskModes(data: []u8): Protocol![]u8 from data {
    var c := Cursor.New(data)
    modes := try c.Vec8(1, 255)
    try c.Done()
    return modes
}
