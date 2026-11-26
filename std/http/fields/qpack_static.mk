// Why: every HTTP/3 request and response carries fields from the QPACK static
// table (RFC 9204 Appendix A), so both the decoder and the encoder need
// O(1)-probe lookups over all 99 entries without linear scans.
//
// What: QStaticLen, QStaticTable (99 entries, 0 to 98), QFirstIndex,
// QNextSame, QStaticFind, and QStaticNameIndex.
//
// How: entries with the same name are not contiguous in QPACK Appendix A (for
// example :status appears at 24..28 and 63..71), so BuildQNameSlots hashes
// each distinct name into 128 slots without collision, QFirstIndex maps every
// index to (first index + 1) of its name, and QNextSame links entries sharing
// a name. BuildQNameSlots panics at compile time if two names ever collide.

// The number of QPACK static-table entries (RFC 9204 3.1, Appendix A).
const QStaticLen = 99

// RFC 9204 Appendix A, Table 4: index i (0 to 98) is QStaticTable[i].
const QStaticTable: [99]StaticEntry = [
    StaticEntry(name: ":authority", value: ""),                                                                   // 0
    StaticEntry(name: ":path", value: "/"),                                                                       // 1
    StaticEntry(name: "age", value: "0"),                                                                         // 2
    StaticEntry(name: "content-disposition", value: ""),                                                          // 3
    StaticEntry(name: "content-length", value: "0"),                                                              // 4
    StaticEntry(name: "cookie", value: ""),                                                                       // 5
    StaticEntry(name: "date", value: ""),                                                                         // 6
    StaticEntry(name: "etag", value: ""),                                                                         // 7
    StaticEntry(name: "if-modified-since", value: ""),                                                            // 8
    StaticEntry(name: "if-none-match", value: ""),                                                                // 9
    StaticEntry(name: "last-modified", value: ""),                                                                // 10
    StaticEntry(name: "link", value: ""),                                                                         // 11
    StaticEntry(name: "location", value: ""),                                                                     // 12
    StaticEntry(name: "referer", value: ""),                                                                      // 13
    StaticEntry(name: "set-cookie", value: ""),                                                                   // 14
    StaticEntry(name: ":method", value: "CONNECT"),                                                               // 15
    StaticEntry(name: ":method", value: "DELETE"),                                                                // 16
    StaticEntry(name: ":method", value: "GET"),                                                                   // 17
    StaticEntry(name: ":method", value: "HEAD"),                                                                  // 18
    StaticEntry(name: ":method", value: "OPTIONS"),                                                               // 19
    StaticEntry(name: ":method", value: "POST"),                                                                  // 20
    StaticEntry(name: ":method", value: "PUT"),                                                                   // 21
    StaticEntry(name: ":scheme", value: "http"),                                                                  // 22
    StaticEntry(name: ":scheme", value: "https"),                                                                 // 23
    StaticEntry(name: ":status", value: "103"),                                                                   // 24
    StaticEntry(name: ":status", value: "200"),                                                                   // 25
    StaticEntry(name: ":status", value: "304"),                                                                   // 26
    StaticEntry(name: ":status", value: "404"),                                                                   // 27
    StaticEntry(name: ":status", value: "503"),                                                                   // 28
    StaticEntry(name: "accept", value: "*/*"),                                                                    // 29
    StaticEntry(name: "accept", value: "application/dns-message"),                                                // 30
    StaticEntry(name: "accept-encoding", value: "gzip, deflate, br"),                                             // 31
    StaticEntry(name: "accept-ranges", value: "bytes"),                                                           // 32
    StaticEntry(name: "access-control-allow-headers", value: "cache-control"),                                    // 33
    StaticEntry(name: "access-control-allow-headers", value: "content-type"),                                     // 34
    StaticEntry(name: "access-control-allow-origin", value: "*"),                                                 // 35
    StaticEntry(name: "cache-control", value: "max-age=0"),                                                       // 36
    StaticEntry(name: "cache-control", value: "max-age=2592000"),                                                 // 37
    StaticEntry(name: "cache-control", value: "max-age=604800"),                                                  // 38
    StaticEntry(name: "cache-control", value: "no-cache"),                                                        // 39
    StaticEntry(name: "cache-control", value: "no-store"),                                                        // 40
    StaticEntry(name: "cache-control", value: "public, max-age=31536000"),                                        // 41
    StaticEntry(name: "content-encoding", value: "br"),                                                           // 42
    StaticEntry(name: "content-encoding", value: "gzip"),                                                         // 43
    StaticEntry(name: "content-type", value: "application/dns-message"),                                          // 44
    StaticEntry(name: "content-type", value: "application/javascript"),                                           // 45
    StaticEntry(name: "content-type", value: "application/json"),                                                 // 46
    StaticEntry(name: "content-type", value: "application/x-www-form-urlencoded"),                                // 47
    StaticEntry(name: "content-type", value: "image/gif"),                                                        // 48
    StaticEntry(name: "content-type", value: "image/jpeg"),                                                       // 49
    StaticEntry(name: "content-type", value: "image/png"),                                                        // 50
    StaticEntry(name: "content-type", value: "text/css"),                                                         // 51
    StaticEntry(name: "content-type", value: "text/html; charset=utf-8"),                                         // 52
    StaticEntry(name: "content-type", value: "text/plain"),                                                       // 53
    StaticEntry(name: "content-type", value: "text/plain;charset=utf-8"),                                         // 54
    StaticEntry(name: "range", value: "bytes=0-"),                                                                // 55
    StaticEntry(name: "strict-transport-security", value: "max-age=31536000"),                                    // 56
    StaticEntry(name: "strict-transport-security", value: "max-age=31536000; includesubdomains"),                 // 57
    StaticEntry(name: "strict-transport-security", value: "max-age=31536000; includesubdomains; preload"),        // 58
    StaticEntry(name: "vary", value: "accept-encoding"),                                                          // 59
    StaticEntry(name: "vary", value: "origin"),                                                                   // 60
    StaticEntry(name: "x-content-type-options", value: "nosniff"),                                                // 61
    StaticEntry(name: "x-xss-protection", value: "1; mode=block"),                                                // 62
    StaticEntry(name: ":status", value: "100"),                                                                   // 63
    StaticEntry(name: ":status", value: "204"),                                                                   // 64
    StaticEntry(name: ":status", value: "206"),                                                                   // 65
    StaticEntry(name: ":status", value: "302"),                                                                   // 66
    StaticEntry(name: ":status", value: "400"),                                                                   // 67
    StaticEntry(name: ":status", value: "403"),                                                                   // 68
    StaticEntry(name: ":status", value: "421"),                                                                   // 69
    StaticEntry(name: ":status", value: "425"),                                                                   // 70
    StaticEntry(name: ":status", value: "500"),                                                                   // 71
    StaticEntry(name: "accept-language", value: ""),                                                              // 72
    StaticEntry(name: "access-control-allow-credentials", value: "FALSE"),                                        // 73
    StaticEntry(name: "access-control-allow-credentials", value: "TRUE"),                                         // 74
    StaticEntry(name: "access-control-allow-headers", value: "*"),                                                // 75
    StaticEntry(name: "access-control-allow-methods", value: "get"),                                              // 76
    StaticEntry(name: "access-control-allow-methods", value: "get, post, options"),                               // 77
    StaticEntry(name: "access-control-allow-methods", value: "options"),                                          // 78
    StaticEntry(name: "access-control-expose-headers", value: "content-length"),                                  // 79
    StaticEntry(name: "access-control-request-headers", value: "content-type"),                                   // 80
    StaticEntry(name: "access-control-request-method", value: "get"),                                             // 81
    StaticEntry(name: "access-control-request-method", value: "post"),                                            // 82
    StaticEntry(name: "alt-svc", value: "clear"),                                                                 // 83
    StaticEntry(name: "authorization", value: ""),                                                                // 84
    StaticEntry(name: "content-security-policy", value: "script-src 'none'; object-src 'none'; base-uri 'none'"), // 85
    StaticEntry(name: "early-data", value: "1"),                                                                  // 86
    StaticEntry(name: "expect-ct", value: ""),                                                                    // 87
    StaticEntry(name: "forwarded", value: ""),                                                                    // 88
    StaticEntry(name: "if-range", value: ""),                                                                     // 89
    StaticEntry(name: "origin", value: ""),                                                                       // 90
    StaticEntry(name: "purpose", value: "prefetch"),                                                              // 91
    StaticEntry(name: "server", value: ""),                                                                       // 92
    StaticEntry(name: "timing-allow-origin", value: "*"),                                                         // 93
    StaticEntry(name: "upgrade-insecure-requests", value: "1"),                                                   // 94
    StaticEntry(name: "user-agent", value: ""),                                                                   // 95
    StaticEntry(name: "x-forwarded-for", value: ""),                                                              // 96
    StaticEntry(name: "x-frame-options", value: "deny"),                                                          // 97
    StaticEntry(name: "x-frame-options", value: "sameorigin"),                                                    // 98
]

// Sentinel for QNextSame when no further entry shares the name.
const QNoNext: u8 = 255

// QNameHash is the slot of name in QNameSlots. O(1).
QNameHash(name: []u8): int {
    n := name.Len()
    p := if n >= 5 { int(name[n - 5]) } else { int(name[0]) }
    return (n * 23 + int(name[0]) * 55 + p * 55 + int(name[n - 1]) * 31) & 127
}

// BuildQNameSlots builds the collision-free 128-slot name lookup table at
// compile time: 0 for empty, else (first index + 1) of the name.
BuildQNameSlots(): [128]u8 {
    var slots: [128]u8 = [0; 128]
    for i in 0..QStaticLen {
        name := QStaticTable[i].name
        var seen := false
        for j in 0..i {
            if QStaticTable[j].name == name {
                seen = true
                break
            }
        }
        if seen {
            continue
        }
        h := QNameHash(name.AsBytes())
        if slots[h] != 0 {
            Panic("qpack_static.mk: two static names share a QNameSlots slot")
        }
        slots[h] = Trunc[u8](i + 1)
    }
    return slots
}

const QNameSlots: [128]u8 = BuildQNameSlots()

// BuildQFirstIndex maps each QPACK static index 0..98 to (first index + 1) of
// its name so Sink.Add receives a consistent 1-based name_index (0 = none).
BuildQFirstIndex(): [99]u8 {
    var first: [99]u8 = [0; 99]
    for i in 0..QStaticLen {
        name := QStaticTable[i].name
        first[i] = Trunc[u8](i + 1)
        for j in 0..i {
            if QStaticTable[j].name == name {
                first[i] = Trunc[u8](j + 1)
                break
            }
        }
    }
    return first
}

const QFirstIndex: [99]u8 = BuildQFirstIndex()

// BuildQNextSame links each QPACK static index to the next static index with
// the same name, or QNoNext.
BuildQNextSame(): [99]u8 {
    var next: [99]u8 = [QNoNext; 99]
    for i in 0..QStaticLen {
        name := QStaticTable[i].name
        for j in (i + 1)..QStaticLen {
            if QStaticTable[j].name == name {
                next[i] = Trunc[u8](j)
                break
            }
        }
    }
    return next
}

const QNextSame: [99]u8 = BuildQNextSame()

// QStaticFind looks (name, value) up in QStaticTable: returns the 0-based
// index (-1 when the name is absent) and whether both name and value match.
// O(name + value); allocates nothing.
QStaticFind(name: []u8, value: []u8): (index: int, exact: bool) {
    if name.Len() == 0 {
        return (index: -1, exact: false)
    }
    slot := int(QNameSlots[QNameHash(name)])
    if slot == 0 {
        return (index: -1, exact: false)
    }
    first := slot - 1
    if !BytesEq(QStaticTable[first].name.AsBytes(), name) {
        return (index: -1, exact: false)
    }
    var i := first
    for i != int(QNoNext) {
        if BytesEq(QStaticTable[i].value.AsBytes(), value) {
            return (index: i, exact: true)
        }
        i = int(QNextSame[i])
    }
    return (index: first, exact: false)
}

// QStaticNameIndex returns (first QPACK index + 1) whose name is name, or 0
// when not in the QPACK static table. O(name); allocates nothing.
QStaticNameIndex(name: []u8): int {
    if name.Len() == 0 {
        return 0
    }
    slot := int(QNameSlots[QNameHash(name)])
    if slot == 0 || !BytesEq(QStaticTable[slot - 1].name.AsBytes(), name) {
        return 0
    }
    return slot
}
