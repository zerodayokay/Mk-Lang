// Why: the HTTP/2 engine must tell a compression error, which ends the
// connection, from a list over its limit, which only refuses one stream,
// and a test must be able to name the exact rule a block broke.
//
// What: the decoder's errors Truncated, BadInteger, BadIndex, BadHuffman,
// BadSizeUpdate and MissingSizeUpdate, their set DecodeError, the decoder's
// Outcome (Fits or TooLarge), and the encoder's TooSmall.
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
/// decoder's maximum, or comes after a field of the block (RFC 7541 4.2,
/// 6.3).
export error BadSizeUpdate(offset: int, size: int)

/// The decoder's maximum was lowered below the table's capacity and the
/// block does not start with a size update to at most that maximum (RFC
/// 7541 4.2).
export error MissingSizeUpdate

/// Every connection-fatal outcome of Decoder.Decode besides OutOfMemory: a
/// compression error (RFC 9113 4.3). The table may be out of sync with the
/// peer's, so the decoder must not be used again.
export type DecodeError = Truncated | BadInteger | BadIndex | BadHuffman | BadSizeUpdate | MissingSizeUpdate

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
