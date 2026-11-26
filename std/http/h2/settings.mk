// Why: HTTP/2 endpoints exchange connection parameters via SETTINGS frames
// (RFC 9113 6.5), and invalid parameter values from a peer must fail with the
// exact RFC 9113 error code.
//
// What: the RFC 9113 6.5.2 setting identifiers, Settings, InitialSettings, and
// Settings.Apply.
//
// How: Settings holds the six defined RFC 9113 parameters initialized to their
// RFC defaults; Apply validates a 6-byte-per-entry payload into a copy first so
// an invalid entry never leaves partial state, processing entries in order so
// the last value wins (RFC 9113 6.5.3).

// RFC 9113 6.5.2 setting identifiers.
const SettingHeaderTableSize: u16 = 0x1
const SettingEnablePush: u16 = 0x2
const SettingMaxConcurrentStreams: u16 = 0x3
const SettingInitialWindowSize: u16 = 0x4
const SettingMaxFrameSize: u16 = 0x5
const SettingMaxHeaderListSize: u16 = 0x6

// RFC 9113 6.5.2 bounds.
const MinFrameSize: u32 = 16384
const MaxFrameSize: u32 = 16777215
const MaxWindowSize: u32 = 0x7FFF_FFFF

/// One side's HTTP/2 settings (RFC 9113 6.5.2), initialized to the RFC's
/// initial values.
export struct Settings {
    /// SETTINGS_HEADER_TABLE_SIZE (0x01), initially 4,096 octets.
    export table_bytes: int
    /// SETTINGS_ENABLE_PUSH (0x02), initially true (1).
    export enable_push: bool
    /// SETTINGS_MAX_CONCURRENT_STREAMS (0x03), initially unlimited (none).
    export max_streams: ?int
    /// SETTINGS_INITIAL_WINDOW_SIZE (0x04), initially 65,535 octets.
    export initial_window: int
    /// SETTINGS_MAX_FRAME_SIZE (0x05), initially 16,384 octets.
    export max_frame: int
    /// SETTINGS_MAX_HEADER_LIST_SIZE (0x06), initially unlimited (none).
    export max_header_list: ?int
}

/// The RFC 9113 6.5.2 initial values of Settings.
export const InitialSettings: Settings = Settings(
    table_bytes: 4096,
    enable_push: true,
    max_streams: none,
    initial_window: 65535,
    max_frame: 16384,
    max_header_list: none,
)

/// Applies a SETTINGS payload to self, processing each 6-byte entry in order
/// (RFC 9113 6.5.3: last value wins) and ignoring unknown setting ids.
/// Errors: Violation(FrameSizeError, 0) when entries length is not a multiple
/// of 6; Violation(ProtocolError, 0) for ENABLE_PUSH outside 0..1, ENABLE_PUSH
/// 1 when from_server is true, or MAX_FRAME_SIZE outside 16,384..16,777,215;
/// Violation(FlowControlError, 0) for INITIAL_WINDOW_SIZE above 2^31 - 1.
/// O(entries); allocates nothing.
export Settings.Apply(mut self, entries: []u8, from_server: bool): Violation!void {
    if entries.Len() % 6 != 0 {
        fail Violation(code: FrameSizeError, stream: 0)
    }
    var next := *self
    var i := 0
    for i < entries.Len() {
        id := (u16(entries[i]) << 8) | u16(entries[i + 1])
        val := (u32(entries[i + 2]) << 24) | (u32(entries[i + 3]) << 16) | (u32(entries[i + 4]) << 8) | u32(entries[i + 5])
        if id == SettingHeaderTableSize {
            next.table_bytes = int(val)
        } else if id == SettingEnablePush {
            if val > 1 || (from_server && val == 1) {
                fail Violation(code: ProtocolError, stream: 0)
            }
            next.enable_push = val == 1
        } else if id == SettingMaxConcurrentStreams {
            next.max_streams = some(int(val))
        } else if id == SettingInitialWindowSize {
            if val > MaxWindowSize {
                fail Violation(code: FlowControlError, stream: 0)
            }
            next.initial_window = int(val)
        } else if id == SettingMaxFrameSize {
            if val < MinFrameSize || val > MaxFrameSize {
                fail Violation(code: ProtocolError, stream: 0)
            }
            next.max_frame = int(val)
        } else if id == SettingMaxHeaderListSize {
            next.max_header_list = some(int(val))
        }
        i += 6
    }
    *self = next
}
