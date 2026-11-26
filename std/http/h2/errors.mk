// Why: HTTP/2 frames, settings and flow control fail with RFC 9113 error codes
// that are either connection-fatal (stream 0) or stream-scoped (non-zero
// stream), and std.http maps those violations into HTTP errors.
//
// What: the RFC 9113 section 7 error code constants (0x0 to 0xd), Violation,
// and NormError.
//
// How: plain u32 constants matching RFC 9113 section 7, Violation,
// TooLarge, BadField, and their Error union with net.Error.

import std.net

/// RFC 9113 7: graceful shutdown or no error.
export const NoError: u32 = 0x0

/// RFC 9113 7: unspecific protocol error.
export const ProtocolError: u32 = 0x1

/// RFC 9113 7: unexpected internal error.
export const InternalError: u32 = 0x2

/// RFC 9113 7: flow-control protocol violation.
export const FlowControlError: u32 = 0x3

/// RFC 9113 7: SETTINGS acknowledgment timeout.
export const SettingsTimeout: u32 = 0x4

/// RFC 9113 7: frame received after stream half-closed.
export const StreamClosed: u32 = 0x5

/// RFC 9113 7: frame with invalid size.
export const FrameSizeError: u32 = 0x6

/// RFC 9113 7: stream refused prior to application processing.
export const RefusedStream: u32 = 0x7

/// RFC 9113 7: stream no longer needed.
export const Cancel: u32 = 0x8

/// RFC 9113 7: unable to maintain compression context.
export const CompressionError: u32 = 0x9

/// RFC 9113 7: CONNECT stream reset or abnormally closed.
export const ConnectError: u32 = 0xa

/// RFC 9113 7: peer generating excessive load.
export const EnhanceYourCalm: u32 = 0xb

/// RFC 9113 7: transport fails minimum security requirements.
export const InadequateSecurity: u32 = 0xc

/// RFC 9113 7: HTTP/1.1 required instead of HTTP/2.
export const Http11Required: u32 = 0xd

/// A protocol violation detected on the HTTP/2 connection: stream is 0 for a
/// connection error, or a non-zero stream identifier for a stream error.
export error Violation(code: u32, stream: u32)

/// A header block or retained head set exceeded its byte limit.
export error TooLarge

/// An outgoing header field failed RFC 9113 byte or length validation before
/// any frame was written.
export error BadField

/// Every error an HTTP/2 connection operation may return: transport or
/// stream errors from std.net, a protocol Violation, TooLarge, or BadField.
export open type Error = net.Error | Violation | TooLarge | BadField

// NormError maps unknown or unsupported RFC 9113 7 error codes to
// InternalError so they never trigger special behaviour. O(1).
NormError(code: u32): u32 {
    return if code > Http11Required { InternalError } else { code }
}
