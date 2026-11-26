// Why: every request and response starts with fields of the static table
// (RFC 7541 Appendix A), so the encoder looks every field up in it and
// the lookup must cost one probe, not a scan of 61 names.
//
// What: StaticEntry, StaticTable (the 61 entries; index i is
// StaticTable[i - 1]), StaticLen, NameHash, NameSlots, FirstIndex,
// StaticFind and StaticNameIndex.
//
// How: the 52 distinct names hash without collision to 128 slots by
// NameHash, on the length and the first and last bytes (multipliers found
// by search). NameSlots, built at compile time, holds the first index of
// each name; BuildNameSlots fails the build if two names ever collide.
// Entries of one name are consecutive in the table, so a (name, value)
// lookup compares the value with at most the 7 values of :status.

import std.core

// One static-table entry.
struct StaticEntry {
    name: str
    value: str
}

// The number of static-table entries: dynamic-table indexes start after
// it (RFC 7541 2.3.3).
const StaticLen = 61

// RFC 7541 Appendix A, Table 1: index i is StaticTable[i - 1].
const StaticTable: [61]StaticEntry = [
    StaticEntry(name: ":authority", value: ""),                   // 1
    StaticEntry(name: ":method", value: "GET"),                   // 2
    StaticEntry(name: ":method", value: "POST"),                  // 3
    StaticEntry(name: ":path", value: "/"),                       // 4
    StaticEntry(name: ":path", value: "/index.html"),             // 5
    StaticEntry(name: ":scheme", value: "http"),                  // 6
    StaticEntry(name: ":scheme", value: "https"),                 // 7
    StaticEntry(name: ":status", value: "200"),                   // 8
    StaticEntry(name: ":status", value: "204"),                   // 9
    StaticEntry(name: ":status", value: "206"),                   // 10
    StaticEntry(name: ":status", value: "304"),                   // 11
    StaticEntry(name: ":status", value: "400"),                   // 12
    StaticEntry(name: ":status", value: "404"),                   // 13
    StaticEntry(name: ":status", value: "500"),                   // 14
    StaticEntry(name: "accept-charset", value: ""),               // 15
    StaticEntry(name: "accept-encoding", value: "gzip, deflate"), // 16
    StaticEntry(name: "accept-language", value: ""),              // 17
    StaticEntry(name: "accept-ranges", value: ""),                // 18
    StaticEntry(name: "accept", value: ""),                       // 19
    StaticEntry(name: "access-control-allow-origin", value: ""),  // 20
    StaticEntry(name: "age", value: ""),                          // 21
    StaticEntry(name: "allow", value: ""),                        // 22
    StaticEntry(name: "authorization", value: ""),                // 23
    StaticEntry(name: "cache-control", value: ""),                // 24
    StaticEntry(name: "content-disposition", value: ""),          // 25
    StaticEntry(name: "content-encoding", value: ""),             // 26
    StaticEntry(name: "content-language", value: ""),             // 27
    StaticEntry(name: "content-length", value: ""),               // 28
    StaticEntry(name: "content-location", value: ""),             // 29
    StaticEntry(name: "content-range", value: ""),                // 30
    StaticEntry(name: "content-type", value: ""),                 // 31
    StaticEntry(name: "cookie", value: ""),                       // 32
    StaticEntry(name: "date", value: ""),                         // 33
    StaticEntry(name: "etag", value: ""),                         // 34
    StaticEntry(name: "expect", value: ""),                       // 35
    StaticEntry(name: "expires", value: ""),                      // 36
    StaticEntry(name: "from", value: ""),                         // 37
    StaticEntry(name: "host", value: ""),                         // 38
    StaticEntry(name: "if-match", value: ""),                     // 39
    StaticEntry(name: "if-modified-since", value: ""),            // 40
    StaticEntry(name: "if-none-match", value: ""),                // 41
    StaticEntry(name: "if-range", value: ""),                     // 42
    StaticEntry(name: "if-unmodified-since", value: ""),          // 43
    StaticEntry(name: "last-modified", value: ""),                // 44
    StaticEntry(name: "link", value: ""),                         // 45
    StaticEntry(name: "location", value: ""),                     // 46
    StaticEntry(name: "max-forwards", value: ""),                 // 47
    StaticEntry(name: "proxy-authenticate", value: ""),           // 48
    StaticEntry(name: "proxy-authorization", value: ""),          // 49
    StaticEntry(name: "range", value: ""),                        // 50
    StaticEntry(name: "referer", value: ""),                      // 51
    StaticEntry(name: "refresh", value: ""),                      // 52
    StaticEntry(name: "retry-after", value: ""),                  // 53
    StaticEntry(name: "server", value: ""),                       // 54
    StaticEntry(name: "set-cookie", value: ""),                   // 55
    StaticEntry(name: "strict-transport-security", value: ""),    // 56
    StaticEntry(name: "transfer-encoding", value: ""),            // 57
    StaticEntry(name: "user-agent", value: ""),                   // 58
    StaticEntry(name: "vary", value: ""),                         // 59
    StaticEntry(name: "via", value: ""),                          // 60
    StaticEntry(name: "www-authenticate", value: ""),             // 61
]

// BytesEq is whether a and b hold the same bytes. O(min(a, b)).
BytesEq(a: []u8, b: []u8): bool {
    return a.Len() == b.Len() && core.BytesEqual(a, 0, b)
}

// NameHash is the slot of name in NameSlots. O(1).
NameHash(name: []u8): int {
    n := name.Len()
    return (n * 3 + int(name[0]) * 54 + int(name[n - 1]) * 59) & 127
}

// BuildNameSlots is the slot table of StaticTable's names: 0 for a free
// slot, else the first index of the name that hashes there. It panics, so
// the build fails, when two names share a slot.
BuildNameSlots(): [128]u8 {
    var slots: [128]u8 = [0; 128]
    for i in 0..StaticLen {
        name := StaticTable[i].name
        if i > 0 && StaticTable[i - 1].name == name {
            continue
        }
        h := NameHash(name.AsBytes())
        if slots[h] != 0 {
            Panic("hpack_static.mk: two static names share a NameSlots slot")
        }
        slots[h] = Trunc[u8](i + 1)
    }
    return slots
}

// The slot table, computed at compile time.
const NameSlots: [128]u8 = BuildNameSlots()

// StaticFind looks (name, value) up in the static table: the index of the
// entry holding both, or else of the first entry with that name, with
// exact telling which; index 0 when no entry has that name. O(name +
// value): one probe, one name comparison, at most 7 value comparisons;
// allocates nothing.
StaticFind(name: []u8, value: []u8): (index: int, exact: bool) {
    if name.Len() == 0 {
        return (index: 0, exact: false)
    }
    first := int(NameSlots[NameHash(name)])
    if first == 0 || !BytesEq(StaticTable[first - 1].name.AsBytes(), name) {
        return (index: 0, exact: false)
    }
    var i := first
    for i <= StaticLen && BytesEq(StaticTable[i - 1].name.AsBytes(), name) {
        if BytesEq(StaticTable[i - 1].value.AsBytes(), value) {
            return (index: i, exact: true)
        }
        i += 1
    }
    return (index: first, exact: false)
}

// BuildFirstIndex maps each static index to the first index of its name:
// :method's 2 and 3 both to 2, :status's 8 to 14 to 8; entry 0 is 0.
BuildFirstIndex(): [62]u8 {
    var first: [62]u8 = [0; 62]
    for i in 1..StaticLen + 1 {
        same := i > 1 && StaticTable[i - 2].name == StaticTable[i - 1].name
        first[i] = if same { first[i - 1] } else { Trunc[u8](i) }
    }
    return first
}

// The first index of each static entry's name, computed at compile time.
const FirstIndex: [62]u8 = BuildFirstIndex()

// StaticNameIndex is the first static index whose name is name, or 0. O(name):
// one probe, one comparison; allocates nothing.
StaticNameIndex(name: []u8): int {
    if name.Len() == 0 {
        return 0
    }
    first := int(NameSlots[NameHash(name)])
    if first == 0 || !BytesEq(StaticTable[first - 1].name.AsBytes(), name) {
        return 0
    }
    return first
}
