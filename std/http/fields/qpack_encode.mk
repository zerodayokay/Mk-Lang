// Why: an HTTP/3 endpoint encoding with zero dynamic-table inserts never
// blocks the peer, needs no encoder stream, and writes each field section in
// one zero-allocation pass directly into the caller's frame buffer.
//
// What: QEncoder, with New, Bound, StartBound, FieldBound, BlockBound,
// Encode, EncodeBlock, Start, and Write (RFC 9204 4.5).
//
// How: Start writes the two-byte 0x00 0x00 section prefix (Required Insert
// Count = 0, Base = 0); Write looks up each field in QStaticTable, emitting
// a 6-bit static Indexed Field Line when exact and not never-indexed, a
// 4-bit static Literal with Name Reference when the name is static, or a
// 4-bit prefix Literal with Literal Name otherwise, using Huffman coding when
// its coded length is at most raw length.

/// A static-and-literal QPACK encoder: never inserts into the dynamic table,
/// never blocks the peer, emits no encoder-stream instructions, and starts
/// every field section with 0x00 0x00 (Required Insert Count = 0, Base = 0).
export struct QEncoder {
    huffman: bool
}

/// A QPACK encoder using Huffman coding when no longer than raw bytes. O(1);
/// allocates nothing.
export QEncoder.New(): QEncoder {
    return QEncoder(huffman: true)
}

/// A QPACK encoder with Huffman coding explicitly enabled or disabled. O(1);
/// allocates nothing.
export QEncoder.WithHuffman(huffman: bool): QEncoder {
    return QEncoder(huffman: huffman)
}

/// The byte length of a section prefix (always 2: 0x00 0x00). O(1); allocates
/// nothing.
export QEncoder.StartBound(&self): int {
    _ = self
    return 2
}

/// An upper bound on the bytes Write emits for (name, value). O(1); allocates
/// nothing.
export QEncoder.FieldBound(&self, name: []u8, value: []u8): int {
    _ = self
    return 2 + IntLen(name.Len(), 3) + name.Len() + IntLen(value.Len(), 7) + value.Len()
}

/// An upper bound on the bytes Encode writes for list. O(fields); allocates
/// nothing.
export QEncoder.Bound[F: Field](&self, list: []F): int {
    var n := self.StartBound()
    for f in &list {
        n += self.FieldBound(f.Name(), f.Value())
    }
    return n
}

/// An upper bound on the bytes EncodeBlock writes for b. O(fields); allocates
/// nothing.
export QEncoder.BlockBound(&self, b: &FieldBlock): int {
    var n := self.StartBound()
    for f in b.Iter() {
        n += self.FieldBound(f.name, f.value)
    }
    return n
}

/// Writes the 2-byte field-section prefix (0x00 0x00: Required Insert Count
/// = 0, S = 0, Delta Base = 0) into out from out[0] and returns 2. Errors:
/// TooSmall(2) when out is shorter than 2 bytes. O(1); allocates nothing.
export QEncoder.Start(&self, out: mut []u8): TooSmall!int {
    _ = self
    if out.Len() < 2 {
        fail TooSmall(need: 2)
    }
    out[0] = 0x00
    out[1] = 0x00
    return 2
}

/// Writes one field line into out from out[0] and returns its length, using
/// never_index = true for sensitive fields (N bit set, RFC 9204 7.1.3).
/// Errors: TooSmall(need) when out is shorter than need, leaving out
/// unchanged. O(name + value); allocates nothing.
export QEncoder.Write(&self, out: mut []u8, name: []u8, value: []u8, never_index: bool): TooSmall!int {
    (index: idx, exact) := QStaticFind(name, value)
    if exact && !never_index {
        need := IntLen(idx, 6)
        if out.Len() < need {
            fail TooSmall(need: need)
        }
        return PutInt(out, idx, 6, 0xC0)
    }
    vc := StrCost(value, 7, self.huffman)
    if idx >= 0 {
        flags := if never_index { u8(0x70) } else { u8(0x50) }
        need := IntLen(idx, 4) + vc.len
        if out.Len() < need {
            fail TooSmall(need: need)
        }
        var o := PutInt(out, idx, 4, flags)
        o += PutStr(mut out[o..], value, 7, 0, vc)
        return o
    }
    flags := if never_index { u8(0x30) } else { u8(0x20) }
    nc := StrCost(name, 3, self.huffman)
    need := nc.len + vc.len
    if out.Len() < need {
        fail TooSmall(need: need)
    }
    var o := PutStr(out, name, 3, flags, nc)
    o += PutStr(mut out[o..], value, 7, 0, vc)
    return o
}

/// Encodes all fields of list in order into out from out[0] and returns the
/// total bytes written, setting N on fields where Field.Sensitive is true.
/// Errors: TooSmall(need) when out is shorter than Bound(list), leaving out
/// unchanged. O(Σ name + value); allocates nothing.
export QEncoder.Encode[F: Field](&self, out: mut []u8, list: []F): TooSmall!int {
    need := self.Bound(list)
    if out.Len() < need {
        fail TooSmall(need: need)
    }
    var o := self.Start(out).Must()
    for f in &list {
        o += self.Write(mut out[o..], f.Name(), f.Value(), f.Sensitive()).Must()
    }
    return o
}

/// Encodes all fields of b in order into out from out[0], preserving
/// sensitive (N bit) on never-indexed fields (RFC 9204 7.1.3). Errors:
/// TooSmall(need) when out is shorter than BlockBound(b), leaving out
/// unchanged. O(Σ name + value); allocates nothing.
export QEncoder.EncodeBlock(&self, out: mut []u8, b: &FieldBlock): TooSmall!int {
    need := self.BlockBound(b)
    if out.Len() < need {
        fail TooSmall(need: need)
    }
    var o := self.Start(out).Must()
    for f in b.Iter() {
        o += self.Write(mut out[o..], f.name, f.value, f.sensitive).Must()
    }
    return o
}
