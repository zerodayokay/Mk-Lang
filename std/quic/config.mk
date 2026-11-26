// Why: callers configure QUIC idle timeouts, concurrency limits, TLS roots
// and identities, and server handshake DoS bounds without naming internal
// transport parameters or window constants.
//
// What: Config, DefaultConfig, and Config helpers converting settings into
// local transport Params and StreamLimits.
//
// How: Config is a sealed plain struct starting from DefaultConfig (30 s idle,
// 100 streams, 1,000 half-open, Retry above 256).

import std.time
import std.tls

/// The settings of QUIC connections: the TLS configuration, the idle
/// limit, the streams a server grants, and the server's handshake limits.
/// Start from DefaultConfig; a literal does not compile outside std.
export struct Config {
    /// The TLS settings (roots, identities, handshake limit); none uses
    /// tls.DefaultConfig. ALPN comes from the caller (std.http: "h3").
    export tls: ?&tls.Config
    /// max_idle_timeout this side sends (RFC 9000 10.1); the smaller of
    /// both sides' applies.
    export idle: time.Duration
    /// initial_max_streams_bidi a server grants, renewed as streams end;
    /// a client grants none.
    export streams: int
    /// A server's handshakes in progress at once; more Initials are dropped.
    export half_open: int
    /// A server answers Initials with Retry while more than this many
    /// handshakes are in progress; 0 always, half_open or more never.
    export retry_above: int
    sealed: u8
}

/// 30 s idle, 100 streams, 1,000 half-open, Retry above 256.
export const DefaultConfig: Config = Config(
    tls: none,
    idle: time.Seconds(30),
    streams: 100,
    half_open: 1000,
    retry_above: 256,
    sealed: 0,
)

// Plain copy of Config's non-borrowed scalar settings (safe to store without borrowing &Config).
struct EngineCfg {
    idle: time.Duration
    streams: int
    half_open: int
    retry_above: int
}

// Extracts Plain scalar settings from Config.
Config.ToEngineCfg(&self): EngineCfg {
    return EngineCfg(
        idle: self.idle,
        streams: self.streams,
        half_open: self.half_open,
        retry_above: self.retry_above,
    )
}

EngineCfg.StreamLimits(&self, client: bool): StreamLimits {
    var lim := StreamLimits.Default(client)
    if client {
        lim.max_streams_bidi = 0
    } else {
        s := if self.streams < 0 { 0 } else { self.streams }
        su := Cast[u64](s)
        lim.max_streams_bidi = if su > MaxStreamCount { MaxStreamCount } else { su }
    }
    return lim
}

EngineCfg.ToParams(&self, client: bool): Params {
    var p := DefaultParams(client)
    ms := self.idle.Nanos() / 1_000_000
    p.max_idle_timeout = if ms <= 0 { 0 } else { Cast[u64](ms) }
    p.initial_max_data = ConnWindow
    p.initial_max_stream_data_bidi_local = ClientStreamWindow
    p.initial_max_stream_data_bidi_remote = if client { ClientStreamWindow } else { ServerInitialStreamWindow }
    p.initial_max_stream_data_uni = UniStreamWindow
    if client {
        p.initial_max_streams_bidi = 0
    } else {
        s := if self.streams < 0 { 0 } else { self.streams }
        su := Cast[u64](s)
        p.initial_max_streams_bidi = if su > MaxStreamsLimit { MaxStreamsLimit } else { su }
    }
    p.initial_max_streams_uni = DefaultMaxStreamsUni
    p.disable_active_migration = !client
    return p
}

// Returns the TLS Config to use for handshakes, defaulting to tls.DefaultConfig.
Config.Tls(&self): &tls.Config from self {
    return match self.tls {
        some(t) { t }
        none { &tls.DefaultConfig }
    }
}

// Builds initial StreamLimits from c for client or server role (Section 3.4.2).
Config.StreamLimits(&self, client: bool): StreamLimits {
    ec := self.ToEngineCfg()
    return ec.StreamLimits(client)
}

// Builds local transport Params from c for client or server role (RFC 9000 18.2).
Config.ToParams(&self, client: bool): Params {
    ec := self.ToEngineCfg()
    return ec.ToParams(client)
}
