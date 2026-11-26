// Why: an HTTP server with h2c enabled on cleartext must recognize the
// 24-byte HTTP/2 client preface without stalling shorter HTTP/1.0 or
// HTTP/1.1 requests such as "GET / HTTP/1.0\r\n\r\n" (18 bytes).
//
// What: SniffH2c, which inspects arriving prefix bytes against h2.Preface.
//
// How: reads into a 24-byte buffer with HeadRead timing (keep_alive before the
// first byte, request_header after) and returns immediately on the first
// differing byte with the exact bytes already read so ServeConnLoop can seed
// its HTTP/1 buffer without re-reading.

import std.http.h2
import std.net
import std.time

// SniffH2c checks whether the incoming connection begins with h2.Preface,
// placing all read bytes into out[0..n]. Returns is_h2=true with n=24 when
// the full 24-byte preface matches, or is_h2=false on EOF, timeout, or the
// first differing byte without waiting for additional bytes.
SniffH2c(conn: &Mutex[?net.Conn], io: Io, o: &ServerOptions, out: mut [24]u8): (is_h2: bool, n: int) {
    var started: ?time.Instant = none
    var off := 0
    for off < 24 {
        got := HeadRead(conn, io, mut out[off..24], o, mut started)
        if got == 0 {
            return (is_h2: false, n: off)
        }
        for i in off..off + got {
            if out[i] != h2.Preface[i] {
                return (is_h2: false, n: off + got)
            }
        }
        off += got
    }
    return (is_h2: true, n: 24)
}
