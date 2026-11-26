// Why: std.http speaks HTTP/2 (RFC 9113) through an engine of its own, in a
// std-internal module, so the engine's frames, streams and windows are never
// part of a program's API.
//
// What: the module std.http.h2. This file declares no items: the HTTP/2
// frame header and each frame's parse (RFC 9113 4, 6) go here.
//
// How: only std modules may import std.http.h2 (E0202, reason
// std-internal); std.http maps the engine's results to its own Request,
// Response and errors.
