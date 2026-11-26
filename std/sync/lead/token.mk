// Why: a multiplexed connection is read by whichever waiting task holds its
// read token, and the HTTP/2 engine and QUIC need the same protocol for
// passing that token, so it is written once, below both.
//
// What: the module std.sync.lead. This file declares no items: the read
// token's state goes here.
//
// How: only std modules may import std.sync.lead (E0202, reason
// std-internal). It sits above std.sync and imports nothing of networking,
// so std.http.h2 and std.quic both import it and neither imports the other.
