// Why: the HTTP/2 engine must tell a compression error, which ends the
// connection, from a list over its limit, which only refuses one stream,
// and a test must be able to name the exact rule a block broke.
//
// What: the decoder's errors Truncated, BadInteger, BadIndex, BadHuffman,
// BadSizeUpdate, MissingSizeUpdate and Unusable, their set DecodeError, the
// decoder's Outcome (Fits or TooLarge), and the encoder's TooSmall.
//
// How: each error carries the block offset of the representation that
// broke the rule, so a fuzz finding or a peer's bad block can be read
// byte by byte.

/// The block ends inside a representation, or inside a string's bytes, that
/// starts at offset.
export error Truncated(offset: int)

/// The prefix integer at offset runs past 10 bytes, or its value past the
/// int range (RFC 7541 5.1).
export error BadInteger(offset: int)

/// The field at offset names index 0, or an index past the static and
/// dynamic tables (RFC 7541 2.3.3, 6.1).
export error BadIndex(offset: int, index: int)

/// The Huffman string at offset holds the EOS symbol, ends in padding longer
/// than 7 bits, or in padding that is not all ones (RFC 7541 5.2).
export error BadHuffman(offset: int)

/// The dynamic-table size update at offset asks for size, above the
/// decoder's maximum (for the first update after the maximum was lowered,
/// above the lowest maximum since the last block), or comes after a field of
/// the block (RFC 7541 4.2, 6.3; RFC 9113 4.3.1).
export error BadSizeUpdate(offset: int, size: int)

/// The decoder's maximum was lowered below the table's capacity and the
/// block does not start with a size update to at most that maximum (RFC
/// 7541 4.2).
export error MissingSizeUpdate

/// Decode was called on a decoder whose last Decode failed. The table may
/// be out of step with the peer's, so the decoder refuses every later block
/// and the connection must close.
export error Unusable

/// Every connection-fatal outcome of Decoder.Decode besides OutOfMemory: a
/// compression error (RFC 9113 4.3). The table may be out of sync with the
/// peer's, so the decoder refuses every later block with Unusable.
export type DecodeError = Truncated | BadInteger | BadIndex | BadHuffman | BadSizeUpdate | MissingSizeUpdate | Unusable

/// How a block that decoded went. Fits: every field reached the sink.
/// TooLarge: the decoded list size (each field's name and value bytes plus
/// 32, RFC 7541 4.1) passed the decoder's limit; the whole block was still
/// read and the dynamic table is in sync, so only the stream is refused,
/// and the sink got just the fields before the one that passed the limit.
export enum Outcome {
    Fits
    TooLarge
}
/// The output holds fewer than need bytes. Nothing was written and the
/// encoder did not change: give at least need bytes and call again.
export error TooSmall(need: int)

/// The field section at offset has a Required Insert Count encoding that
/// exceeds FullRange, decodes to 0 from a non-zero wire value, or cannot be
/// produced by a conformant encoder (RFC 9204 4.5.1.1).
export error BadRequiredInsertCount(offset: int)

/// The field section at offset has a Sign bit of 1 with Delta Base >= Required
/// Insert Count (negative Base), or overflows int (RFC 9204 4.5.1.2).
export error BadBase(offset: int)

/// A field section could not be decoded immediately and the decoder already
/// holds max_blocked blocked streams (RFC 9204 2.1.2).
export error BlockedLimit

/// A malformed or disallowed QPACK encoder-stream or decoder-stream
/// instruction at offset (RFC 9204 4.3, 4.4).
export error BadInstruction(offset: int)

/// A QPACK dynamic-table capacity update or entry size at offset exceeds the
/// decoder's advertised or current table capacity (RFC 9204 3.2.2, 4.3.1).
export error CapacityExceeded(size: int)

/// Every QPACK decoding error (RFC 9204 6): connection-fatal except BadInteger
/// on a request stream (RFC 9204 7.4), which maps to a stream error.
export type QpackError = Truncated | BadInteger | BadIndex | BadHuffman | BadRequiredInsertCount | BadBase | BlockedLimit | BadInstruction | CapacityExceeded
