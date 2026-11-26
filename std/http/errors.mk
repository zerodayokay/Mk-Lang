// Why: the client and the server fail in many of the same ways, so std.http
// declares its errors once and callers match one type.
//
// What: BadRequest, TooLarge, Status, NoSecureTransport, BodyNotReplayable,
// TooManyRedirects, BadUrl and ProxyRefused, and Error, their union with
// net.Error, OutOfMemory, ReadError and WriteError.
//
// How: plain error declarations. BadRequest carries the byte offset where
// reading stopped; Status and ProxyRefused carry a status code.

import std.net

/// A malformed request or reply. offset is the byte where reading stopped,
/// counted from the first byte read for it.
export error BadRequest(offset: int)
/// A request or reply passed its byte limit.
export error TooLarge
/// A handler's answer with a status code: the server replies with code and
/// an empty body. Dispatch fails with it too, for 400, 404 and 405.
export error Status(code: int)
/// An https URL, and the client's limits name no secure connector. Put a
/// tls.Connector in ClientLimits.secure to send https requests.
export error NoSecureTransport
/// A redirect asked to send again a body that DoStreaming read from a
/// Reader, which cannot be read twice.
export error BodyNotReplayable
/// The reply was still a redirect after the client followed its limit of
/// redirects.
export error TooManyRedirects
/// A client request's URL, or a redirect's Location, is not an http or
/// https URL the client can send to, or ClientLimits.proxy is not an http
/// URL with a host and no userinfo.
export error BadUrl
/// The client's proxy answered its CONNECT with code, a status other than
/// 2xx, so no tunnel was made.
export error ProxyRefused(code: int)
/// The peer broke HTTP/2 or HTTP/3 framing or field rules, as this side
/// found; code is the RFC's error code (RFC 9113 7, RFC 9114 8.1).
export error Protocol(code: u64)

/// Every way std.http fails. ReadError is a failed read of the Reader a
/// request or reply comes from, and WriteError a failed write. net.Error
/// brings Canceled, Closed and time.TimedOut. The set is open, so a match
/// on it outside std keeps a `_` arm.
export open type Error = BadRequest | TooLarge | Status | net.Error | OutOfMemory | ReadError | WriteError | NoSecureTransport | BodyNotReplayable | TooManyRedirects | BadUrl | ProxyRefused | Protocol
