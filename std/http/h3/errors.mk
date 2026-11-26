// Why: HTTP/3 framing, control streams, settings and message validation fail
// with RFC 9114 8.1 and RFC 9204 6 error codes that are either connection-fatal
// or stream-scoped, and unknown peer error codes must normalize to H3NoError.
//
// What: the RFC 9114 8.1 error codes (0x0100-0x0110), RFC 9204 6 QPACK error
// codes (0x0200-0x0202), Violation, TooLarge, SendTooLarge, Error, NormError,
// and Unprocessed.
//
// How: plain u64 constants matching RFC 9114 8.1 and RFC 9204 6, one
// Violation error carrying the code and stream (-1 for connection errors),
// and an open Error set over Violation, size bounds and net.Error.

import std.net

/// RFC 9114 8.1: no error; graceful close or stream end.
export const H3NoError: u64 = 0x0100

/// RFC 9114 8.1: general protocol violation.
export const H3GeneralProtocolError: u64 = 0x0101

/// RFC 9114 8.1: internal HTTP/3 stack error.
export const H3InternalError: u64 = 0x0102

/// RFC 9114 8.1: peer created an unaccepted stream.
export const H3StreamCreationError: u64 = 0x0103

/// RFC 9114 8.1: critical unidirectional stream was closed or reset.
export const H3ClosedCriticalStream: u64 = 0x0104

/// RFC 9114 8.1: frame not permitted in current state or on this stream.
export const H3FrameUnexpected: u64 = 0x0105

/// RFC 9114 8.1: frame failed layout requirements or had an invalid size.
export const H3FrameError: u64 = 0x0106

/// RFC 9114 8.1: peer exhibited behavior generating excessive load.
export const H3ExcessiveLoad: u64 = 0x0107

/// RFC 9114 8.1: stream ID or push ID used incorrectly.
export const H3IdError: u64 = 0x0108

/// RFC 9114 8.1: error in SETTINGS payload.
export const H3SettingsError: u64 = 0x0109

/// RFC 9114 8.1: control stream did not start with a SETTINGS frame.
export const H3MissingSettings: u64 = 0x010a

/// RFC 9114 8.1: request rejected prior to application processing.
export const H3RequestRejected: u64 = 0x010b

/// RFC 9114 8.1: request or response cancelled.
export const H3RequestCancelled: u64 = 0x010c

/// RFC 9114 8.1: request stream ended before a complete request arrived.
export const H3RequestIncomplete: u64 = 0x010d

/// RFC 9114 8.1: malformed HTTP message.
export const H3MessageError: u64 = 0x010e

/// RFC 9114 8.1: TCP connection for CONNECT reset or abnormally closed.
export const H3ConnectError: u64 = 0x010f

/// RFC 9114 8.1: requested operation cannot be served over HTTP/3.
export const H3VersionFallback: u64 = 0x0110

/// RFC 9204 6: QPACK field section decompression failed.
export const QpackDecompressionFailed: u64 = 0x0200

/// RFC 9204 6: QPACK encoder stream instruction error.
export const QpackEncoderStreamError: u64 = 0x0201

/// RFC 9204 6: QPACK decoder stream instruction error.
export const QpackDecoderStreamError: u64 = 0x0202

/// An HTTP/3 protocol violation detected on the connection or a stream:
/// stream is -1 for connection-scoped errors and >= 0 for stream errors.
export error Violation(code: u64, stream: i64)

/// A received field section exceeded the endpoint's header_bytes limit
/// (RFC 9114 4.2.2).
export error TooLarge

/// An outgoing field section would exceed the peer's
/// SETTINGS_MAX_FIELD_SECTION_SIZE (RFC 9114 4.2.2).
export error SendTooLarge

/// Every error an HTTP/3 session or exchange operation returns.
export open type Error = Violation | TooLarge | SendTooLarge | net.Error

/// Maps unknown or out-of-context error codes outside 0x0100..0x0110 to
/// H3NoError (RFC 9114 8). O(1); allocates nothing.
export NormError(code: u64): u64 {
    if code >= H3NoError && code <= H3VersionFallback {
        return code
    }
    return H3NoError
}

/// Reports whether stream id was not processed under an HTTP/3 GOAWAY
/// identifier (RFC 9114 5.2: id >= goaway, inclusive). O(1); allocates nothing.
export Unprocessed(id: i64, goaway: i64): bool {
    return goaway >= 0 && id >= goaway
}
