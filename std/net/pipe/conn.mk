// Why: the tests of std's HTTP/2 and HTTP/3 engines need a peer whose
// buffers are small and bounded, so backpressure and blocked writes happen
// in memory, without sockets.
//
// What: the module std.net.pipe, test support for std. This file declares
// no items: the in-memory net.Conn pair, each direction bounded, goes here.
//
// How: only std modules may import std.net.pipe (E0202, reason
// std-internal), and only std's tests do.
