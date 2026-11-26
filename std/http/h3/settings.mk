// Why: HTTP/3 endpoints advertise connection parameters in a single initial
// SETTINGS frame (RFC 9114 7.2.4), and malformed, duplicate or HTTP/2-reserved
// identifiers must fail with the exact RFC 9114 connection error code.
//
// What: setting identifier constants (0x01, 0x06, 0x07), IsReservedH2Setting,
// Settings, InitialSettings, Settings.Apply, and EncodeSettings.
//
// How: Settings.Apply walks (id, value) varint pairs into a copy first so an
// error leaves self unchanged, rejects truncated pairs (H3FrameError), HTTP/2
// settings 0x02-0x05 and duplicate identifiers (H3SettingsError), and ignores
// unknown or GREASE identifiers using an O(N) hash set when more than 32
// unknown identifiers appear.

/// RFC 9204 5: maximum QPACK dynamic table capacity (default 0).
export const SettingQpackMaxTableCapacity: u64 = 0x01

/// RFC 9114 7.2.4.1: maximum decoded HTTP field section size (default unlimited).
export const SettingMaxFieldSectionSize: u64 = 0x06

/// RFC 9204 5: maximum number of QPACK blocked streams (default 0).
export const SettingQpackBlockedStreams: u64 = 0x07

/// Reports whether id is one of the HTTP/2-only settings reserved in HTTP/3
/// (RFC 9114 7.2.4.1, Table 3: 0x02, 0x03, 0x04, 0x05). O(1); allocates
/// nothing.
export IsReservedH2Setting(id: u64): bool {
    return id == 0x02 || id == 0x03 || id == 0x04 || id == 0x05
}

/// HTTP/3 connection settings (RFC 9114 7.2.4.1, RFC 9204 5).
export struct Settings {
    /// SETTINGS_QPACK_MAX_TABLE_CAPACITY (0x01), default 0.
    export qpack_max_table_capacity: u64
    /// SETTINGS_MAX_FIELD_SECTION_SIZE (0x06), default none (unlimited).
    export max_field_section_size: ?u64
    /// SETTINGS_QPACK_BLOCKED_STREAMS (0x07), default 0.
    export qpack_blocked_streams: u64
}

/// Default HTTP/3 settings before the peer's SETTINGS frame arrives (RFC
/// 9114 7.2.4.2).
export const InitialSettings: Settings = Settings(
    qpack_max_table_capacity: 0,
    max_field_section_size: none,
    qpack_blocked_streams: 0,
)

// HashU64 mixes a 64-bit setting identifier for open-addressed duplicate checks.
HashU64(v: u64): int {
    x := v ^ (v >> 33)
    y := x *% 0xff51_afd7_ed55_8ccd
    z := y ^ (y >> 33)
    return Trunc[int](z & 0x7fff_ffff)
}

// TrackUnknown checks for duplicate unknown setting identifiers in O(1)
// expected time per entry using an open-addressed set in a HeapList.
TrackUnknown(a: &Alloc, seen_small: mut [32]u64, small_n: mut int, table: mut HeapList[u64], used: mut HeapList[u8], id: u64): Violation|OutOfMemory!void {
    if table.Len() == 0 {
        for i in 0..(*small_n) {
            if seen_small[i] == id {
                fail Violation(code: H3SettingsError, stream: -1)
            }
        }
        if *small_n < 32 {
            seen_small[*small_n] = id
            *small_n += 1
            return
        }
        // Promote the 32 small entries into a 256-slot open-addressed hash table.
        for _ in 0..256 {
            try table.Push(a, 0)
            try used.Push(a, 0)
        }
        for i in 0..32 {
            InsertSet(table, used, seen_small[i])
        }
    }
    if ContainsOrInsert(a, table, used, id) else e {
        fail e
    } {
        fail Violation(code: H3SettingsError, stream: -1)
    }
}

// InsertSet inserts id into an open-addressed table that already has room.
InsertSet(table: mut HeapList[u64], used: mut HeapList[u8], id: u64) {
    cap := table.Len()
    mask := cap - 1
    var slot := HashU64(id) & mask
    for used[slot] != 0 {
        slot = (slot + 1) & mask
    }
    table.ItemsMut()[slot] = id
    used.ItemsMut()[slot] = 1
}

// ContainsOrInsert returns true if id was already present, else inserts it
// (growing if load > 50%).
ContainsOrInsert(a: &Alloc, table: mut HeapList[u64], used: mut HeapList[u8], id: u64): OutOfMemory!bool {
    cap := table.Len()
    var count := 0
    for i in 0..cap {
        if used[i] != 0 {
            count += 1
        }
    }
    if (count + 1) * 2 > cap {
        new_cap := cap * 2
        var nt := HeapList[u64].New(a)
        onerr nt.Free(a)
        var nu := HeapList[u8].New(a)
        onerr nu.Free(a)
        for _ in 0..new_cap {
            try nt.Push(a, 0)
            try nu.Push(a, 0)
        }
        for i in 0..cap {
            if used[i] != 0 {
                InsertSet(mut nt, mut nu, table[i])
            }
        }
        table.Clear()
        used.Clear()
        try table.Extend(a, nt.Items())
        try used.Extend(a, nu.Items())
        nt.Free(a)
        nu.Free(a)
    }
    nmask := table.Len() - 1
    var slot := HashU64(id) & nmask
    for used[slot] != 0 {
        if table[slot] == id {
            return true
        }
        slot = (slot + 1) & nmask
    }
    table.ItemsMut()[slot] = id
    used.ItemsMut()[slot] = 1
    return false
}

/// Decodes a SETTINGS frame payload (zero or more varint (id, value) pairs)
/// and updates self atomically on success (RFC 9114 7.2.4). Errors:
/// Violation(H3FrameError, -1) on a truncated pair; Violation(H3SettingsError,
/// -1) on reserved HTTP/2 settings 0x02-0x05 or duplicate setting identifiers;
/// OutOfMemory if tracking > 32 unknown identifiers cannot grow. O(pairs);
/// allocates nothing for <= 32 unknown identifiers.
export Settings.Apply(mut self, a: &Alloc, payload: []u8): Violation|OutOfMemory!void {
    var next := *self
    var seen_qcap := false
    var seen_max_fs := false
    var seen_blocked := false
    var seen_small: [32]u64 = [0; 32]
    var small_n := 0
    var table := HeapList[u64].New(a)
    defer table.Free(a)
    var used := HeapList[u8].New(a)
    defer used.Free(a)

    var at := 0
    n := payload.Len()
    for at < n {
        ri := ReadVarint(payload, at) else {
            fail Violation(code: H3FrameError, stream: -1)
        }
        at += ri.n
        rv := ReadVarint(payload, at) else {
            fail Violation(code: H3FrameError, stream: -1)
        }
        at += rv.n
        id := ri.v
        val := rv.v
        if IsReservedH2Setting(id) {
            fail Violation(code: H3SettingsError, stream: -1)
        }
        if id == SettingQpackMaxTableCapacity {
            if seen_qcap {
                fail Violation(code: H3SettingsError, stream: -1)
            }
            seen_qcap = true
            next.qpack_max_table_capacity = val
        } else if id == SettingMaxFieldSectionSize {
            if seen_max_fs {
                fail Violation(code: H3SettingsError, stream: -1)
            }
            seen_max_fs = true
            next.max_field_section_size = some(val)
        } else if id == SettingQpackBlockedStreams {
            if seen_blocked {
                fail Violation(code: H3SettingsError, stream: -1)
            }
            seen_blocked = true
            next.qpack_blocked_streams = val
        } else {
            try TrackUnknown(a, mut seen_small, mut small_n, mut table, mut used, id)
        }
    }
    *self = next
}

// WriteSettingPair writes one (id, val) varint pair into out.
WriteSettingPair(out: mut []u8, id: u64, val: u64): int {
    n1 := WriteVarint(out, id)
    if n1 == 0 {
        return 0
    }
    n2 := WriteVarint(mut out[n1..], val)
    if n2 == 0 {
        return 0
    }
    return n1 + n2
}

/// Encodes a SETTINGS frame into out and returns the byte count written, or 0
/// if out is too short or any value exceeds MaxVarint (RFC 9114 7.2.4). Each
/// setting identifier appears at most once; include_grease adds a reserved
/// 0x21 setting (RFC 9114 7.2.4.1). O(1); allocates nothing.
export EncodeSettings(out: mut []u8, max_field_section_size: ?u64, qpack_cap: u64, qpack_blocked: u64, include_grease: bool): int {
    var payload: [64]u8 = [0; 64]
    var plen := 0
    if qpack_cap > 0 {
        n := WriteSettingPair(mut payload[plen..], SettingQpackMaxTableCapacity, qpack_cap)
        if n == 0 {
            return 0
        }
        plen += n
    }
    if mfs := max_field_section_size {
        n := WriteSettingPair(mut payload[plen..], SettingMaxFieldSectionSize, mfs)
        if n == 0 {
            return 0
        }
        plen += n
    }
    if qpack_blocked > 0 {
        n := WriteSettingPair(mut payload[plen..], SettingQpackBlockedStreams, qpack_blocked)
        if n == 0 {
            return 0
        }
        plen += n
    }
    if include_grease {
        n := WriteSettingPair(mut payload[plen..], 0x21, 0)
        if n == 0 {
            return 0
        }
        plen += n
    }
    hlen := WriteFrameHeader(out, FrameSettings, Cast[u64](plen))
    if hlen == 0 || out.Len() - hlen < plen {
        return 0
    }
    (mut out[hlen..hlen + plen]).CopyFrom(payload[0..plen])
    return hlen + plen
}
