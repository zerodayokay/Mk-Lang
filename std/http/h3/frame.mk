// Why: std.http speaks HTTP/3 (RFC 9114) over any multiplexed transport a
// net.Mux gives, so its framing and control streams sit in a std-internal
// module that knows nothing of QUIC.
//
// What: the module std.http.h3. This file declares no items: the HTTP/3
// frames (RFC 9114 7) go here.
//
// How: only std modules may import std.http.h3 (E0202, reason
// std-internal); it reaches its transport only through std.net's Mux.
