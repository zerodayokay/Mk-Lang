// Why: callers of PNG encoding, decoding and image comparison need one
// open error set and a clear reason when an input is malformed.
// What: Error, Invalid, Unsupported, TooLarge and Why.
// How: open error set with OutOfMemory from the prelude and domain errors
// declared here.

/// Every error std.image returns.
export open type Error = Invalid | Unsupported | TooLarge | OutOfMemory

/// The input is malformed, for the reason given.
export error Invalid(why: Why)

/// A valid PNG outside the supported subset.
export error Unsupported

/// The image passes the limits.
export error TooLarge

/// Why an image was refused.
export open enum Why {
    BadSignature
    BadCrc
    BadChunk
    BadHeader
    BadZlib
    BadFilter
    Truncated
    BadImage
    SizeMismatch
}
