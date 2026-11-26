// Why: std.http keeps its fields as a List[Header] in the stream's arena,
// and HPACK and QPACK must fill it, and read it back, with no header type
// of their own and no conversion copy.
//
// What: Sink, which the decoders feed; Field, which the encoders read; Mode,
// the representation an encoder may choose for one field; the constants
// EntryOverhead and DefaultTableBytes.
//
// How: both interfaces are generic bounds, so each caller's impl is
// monomorphised into the codec's loop: one direct call per field. Sink.Add
// declares its stores, so an impl may keep the views it gets, and a decoder
// can hand it only what its own signature lets flow into the sink.

/// The size of a dynamic-table entry beyond its name and value bytes, and
/// the per-field overhead in the decoded list size (RFC 7541 4.1).
export const EntryOverhead = 32

/// The initial maximum dynamic-table size of HTTP/2,
/// SETTINGS_HEADER_TABLE_SIZE (RFC 9113 6.5.2).
export const DefaultTableBytes = 4096

/// Where a decoder puts each decoded field, in the block's order. name and
/// value are views of static data or of the arena given to the decoder, so
/// an impl may keep them. never_index is true for a field sent as never
/// indexed (RFC 7541 6.2.3), which an intermediary must forward the same way
/// (7.1.3). Add fails only with OutOfMemory, which ends the block and leaves
/// the decoder unusable. To refuse a field (a malformed name, a value that
/// is not UTF-8), an impl records it in self and returns: the decoder must
/// read the block to its end, or its table goes out of sync with the peer's.
export interface Sink {
    Add(mut self, name: []u8 into self, value: []u8 into self, never_index: bool): OutOfMemory!void
}

/// A field an encoder reads: its name and value bytes, and whether it is
/// sensitive. A sensitive field is sent never indexed (RFC 7541 6.2.3,
/// 7.1.3) and is never added to the dynamic table. The encoder writes the
/// name as given: HTTP/2 and HTTP/3 need it in lower case.
export interface Field {
    Name(&self): []u8
    Value(&self): []u8
    Sensitive(&self): bool
}

/// How an encoder may represent one field (RFC 7541 6.1, 6.2).
export enum Mode {
    /// An index when the static or dynamic table holds the field; otherwise
    /// a literal added to the dynamic table when its entry is at most half
    /// the table's capacity (6.2.1), else a literal without indexing
    /// (6.2.2).
    Index
    /// An index when a table holds the field; otherwise a literal without
    /// indexing (6.2.2). The dynamic table does not change.
    NoIndex
    /// A never-indexed literal (6.2.3), whatever the tables hold; the name
    /// may be an index. The dynamic table does not change.
    Never
}
