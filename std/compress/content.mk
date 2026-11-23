// Why: an HTTP client must undo the content codings it advertised, and a
// binary should link only the decoders its program accepts.
//
// What: compress.Content (a token and a wrap function), GzipContent and
// DeflateContent, with GzipReader and ZlibReader behind them.
//
// How: each value pairs a token from RFC 9110 section 8.4.1 with a function
// that wraps a body Reader in the decoder's Reader. A program names the ones
// it accepts in http.ClientLimits.decoders, so only those are linked.

/// An HTTP content coding: a name and a way to decode a body. name is the
/// coding's token in Accept-Encoding and Content-Encoding. wrap makes a
/// Reader of the decoded body over the body's Reader src, with buf as its
/// inline buffer and the decoder's state in a. GzipContent, DeflateContent
/// and ZstdContent are the values to list in an HTTP client's decoders.
///
/// wrap fails only with OutOfMemory, when a cannot give the decoder its
/// buffers. The Reader it returns fails with ReadError where the stream is
/// bad or src fails, as Inflater.Reader does; for zstd also where a cannot
/// give a frame's window, which the decoder takes at the frame's header
/// (ZstdDecoder.NewLazy).
export struct Content {
    /// The coding's token in Accept-Encoding and Content-Encoding, such as
    /// "gzip".
    export name: str
    /// Makes a Reader of the decoded body over src, with buf as its inline
    /// buffer and the decoder's state in a.
    export wrap: (fn(a: &Arena, src: mut Reader, buf: mut []u8): OutOfMemory!Reader from a, src, buf)
}

/// The gzip content coding (RFC 9110): gzip members back to back, read by
/// Inflater.NewGzip.
export const GzipContent = Content(name: "gzip", wrap: GzipReader)

/// The deflate content coding (RFC 9110): a zlib stream (RFC 1950), read by
/// Inflater.NewZlib. A raw DEFLATE stream sent as deflate is Corrupt at its
/// first byte.
export const DeflateContent = Content(name: "deflate", wrap: ZlibReader)

// GzipReader is GzipContent's wrap: an Inflater.NewGzip in a, and its
// Reader over src. OutOfMemory when a cannot give the decoder's buffers
// up front: the Reader would only say ReadError, and the decoder, whose
// Cause would say why, is not reachable through it.
GzipReader(a: &Arena, src: mut Reader, buf: mut []u8): OutOfMemory!Reader from a, src, buf {
    i := Inflater.NewGzip(a)
    // Before any read, Cause is OutOfMemory or none.
    if i.Cause().IsSome() {
        fail OutOfMemory
    }
    d := try a.Put(i)
    return d.Reader(src, buf)
}

// ZlibReader is DeflateContent's wrap: an Inflater.NewZlib in a, and its
// Reader over src; OutOfMemory as GzipReader.
ZlibReader(a: &Arena, src: mut Reader, buf: mut []u8): OutOfMemory!Reader from a, src, buf {
    i := Inflater.NewZlib(a)
    if i.Cause().IsSome() {
        fail OutOfMemory
    }
    d := try a.Put(i)
    return d.Reader(src, buf)
}
