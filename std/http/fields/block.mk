// Why: the decoder runs in the task that reads the connection and its
// views last one call, while the fields belong to the task that serves the
// stream; they must be copied once into memory that task can own, with no
// allocation per block once warm. HPACK carries octets, and an HTTP field
// value may hold bytes that are not UTF-8 (obs-text, RFC 9110 5.5), so the
// block keeps every field as it came and leaves the verdict to its reader.
//
// What: FieldBlock, the sink std.http uses (New, Free, Reset, Len, Bytes,
// Get, Text, Iter, and its Sink impl), and FieldIter.
//
// How: the fields' bytes back to back in one buffer, each field's name then
// its value, and one 16-byte Span per field: the offset, the two lengths,
// the never-indexed flag and the name's static index. Both buffers are
// owned HeapLists, so a block is Send: the server moves a filled one into
// the stream's task. Reset keeps their capacity. The decoder's list budget
// bounds a block: at most list_bytes of bytes and list_bytes / 32 spans.

// The most name and value bytes one block holds: a Span's offsets are u32.
const MaxBlockBytes = 0xFFFF_FFFF

// One field of a block.
struct Span {
    at: u32
    name: u32
    value: u32
    never_index: bool
    index: u8
}

/// The fields of one header block, owned, as bytes: Add (its Sink impl)
/// keeps every field it is given, in order. Get and Iter give a field's
/// bytes; Text gives it as str, or none when its name or value is not
/// UTF-8, so the reader can refuse the message as std.http's HTTP/1.1
/// parser refuses such a head. Linear: it owns heap memory; release it with
/// Free. Send and Share.
export linear view struct FieldBlock {
    bytes: HeapList[u8]
    spans: HeapList[Span]
}

/// An empty block whose buffers will grow from a (the allocator every Add
/// gets must be a). O(1); allocates nothing until the first Add.
export FieldBlock.New(a: &Alloc): FieldBlock {
    return FieldBlock(bytes: HeapList[u8].New(a), spans: HeapList[Span].New(a))
}

/// Releases the block's buffers. O(1).
export FieldBlock.Free(self, a: &Alloc) {
    FieldBlock(bytes, spans) := self
    bytes.Free(a)
    spans.Free(a)
}

/// Empties the block and keeps its buffers, so the next block of the same
/// size allocates nothing. O(fields + bytes) at worst (the lists clear
/// plain elements); allocates nothing.
export FieldBlock.Reset(mut self) {
    self.bytes.Clear()
    self.spans.Clear()
}

/// The number of fields. O(1).
export FieldBlock.Len(&self): int {
    return self.spans.Len()
}

/// The name and value bytes held. O(1).
export FieldBlock.Bytes(&self): int {
    return self.bytes.Len()
}

/// Field i: its name and value bytes, whether it was sent never indexed,
/// and its name's static index (0 for none). i must be below Len; another i
/// panics. O(1); allocates nothing.
export FieldBlock.Get(&self, i: int): (name: []u8, value: []u8, sensitive: bool, name_index: int) from self {
    s := self.spans[i]
    at := int(s.at)
    mid := at + int(s.name)
    end := mid + int(s.value)
    b := self.bytes.Items()
    return (name: b[at..mid], value: b[mid..end], sensitive: s.never_index, name_index: int(s.index))
}

/// Field i as Get gives it, its name and value as str, or none when either
/// is not UTF-8. i must be below Len; another i panics. O(name + value);
/// allocates nothing.
export FieldBlock.Text(&self, i: int): ?(name: str, value: str, sensitive: bool, name_index: int) from self {
    f := self.Get(i)
    name := str.From(f.name) else {
        return none
    }
    value := str.From(f.value) else {
        return none
    }
    return some((name, value, sensitive: f.sensitive, name_index: f.name_index))
}

/// The fields in order, as Get gives them. O(1); allocates nothing.
export FieldBlock.Iter(&self): FieldIter from self {
    return FieldIter(block: self, i: 0)
}

/// An iterator over a block's fields.
export view struct FieldIter {
    block: &FieldBlock
    i: int
}

/// The next field, as FieldBlock.Get gives it, or none after the last.
/// O(1); allocates nothing.
export FieldIter.Next(mut self): ?(name: []u8, value: []u8, sensitive: bool, name_index: int) from self.block {
    if self.i >= self.block.Len() {
        return none
    }
    self.i += 1
    return some(self.block.Get(self.i - 1))
}

// Copies each field it is given. Fails with OutOfMemory when a buffer
// cannot grow, or when the block would pass MaxBlockBytes (a block reused
// without Reset). O(name + value); amortised growth from a.
impl Sink for FieldBlock {
    Add(mut self, a: &Alloc, name: []u8, value: []u8, never_index: bool, name_index: int): OutOfMemory!void {
        at := self.bytes.Len()
        if name.Len() + value.Len() > MaxBlockBytes - at {
            fail OutOfMemory
        }
        try self.bytes.Extend(a, name)
        try self.bytes.Extend(a, value)
        try self.spans.Push(a, Span(at: Cast[u32](at), name: Cast[u32](name.Len()), value: Cast[u32](value.Len()), never_index: never_index, index: Trunc[u8](name_index)))
    }
}
