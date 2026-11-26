// Why: the RFC 7541 examples are hex dumps and lists of fields, and every
// test must compare a decoded list and a dynamic table with them.
//
// What: Unhex (an RFC hex dump to bytes), Same, PairsAre (a FieldBlock
// against "name: value" lines), TableIs (a table against the RFC's
// listing), TField and ToFields (fields to encode), and Block (one block of
// an Appendix C sequence, both ways).
//
// How: plain helpers over the module's own types; nothing here is
// exported. The arenas here hold only the tests' own inputs.

// Unhex is the bytes of an RFC hex dump: hex digits two by two, spaces
// ignored. It panics on any other byte: the dumps are the tests' own.
Unhex(a: &Arena, hex: str): OutOfMemory![]u8 from a {
    var out := List[u8].New(a)
    var hi := -1
    for c in hex.AsBytes() {
        d := if c >= b'0' && c <= b'9' {
            int(c - b'0')
        } else if c >= b'a' && c <= b'f' {
            int(c - b'a') + 10
        } else if c == b' ' || c == b'\n' {
            continue
        } else {
            Panic("Unhex: not a hex digit")
        }
        if hi < 0 {
            hi = d
        } else {
            try out.Push(Trunc[u8](hi * 16 + d))
            hi = -1
        }
    }
    Assert(hi < 0)
    return out.Done()[..]
}

// Same is whether b holds the bytes of s.
Same(b: []u8, s: str): bool {
    return BytesEq(b, s.AsBytes())
}

// PairsAre is whether got holds exactly the fields of want, a list of
// "name: value" lines.
PairsAre(got: &FieldBlock, want: []str): bool {
    if got.Len() != want.Len() {
        return false
    }
    for i, w in want {
        (after, before) := w.SplitOnce(": ") else {
            return false
        }
        f := got.Get(i)
        if f.name != before || f.value != after {
            return false
        }
    }
    return true
}

// TableIs is whether t holds, newest first, the entries of want ("name:
// value" lines) and has size size.
TableIs(t: &Table, want: []str, size: int): bool {
    if t.count != want.Len() || t.size != size {
        return false
    }
    var buf: [256]u8 = [0; 256]
    for i, w in want {
        (after, before) := w.SplitOnce(": ") else {
            return false
        }
        abs := t.inserted - 1 - i
        if !t.NameIs(abs, before.AsBytes()) || !t.ValueIs(abs, after.AsBytes()) {
            return false
        }
        n := t.NameLen(abs)
        t.CopyName(abs, mut buf[0..n])
        if !Same(buf[0..n], before) {
            return false
        }
    }
    return true
}

// A field the tests encode: a name and a value, and whether it is
// sensitive.
struct TField {
    name: str
    value: str
    sensitive: bool
}

impl Field for TField {
    Name(&self): []u8 {
        return self.name.AsBytes()
    }

    Value(&self): []u8 {
        return self.value.AsBytes()
    }

    Sensitive(&self): bool {
        return self.sensitive
    }
}

// ToFields is the "name: value" lines as fields, none sensitive.
ToFields(a: &Arena, lines: []str): OutOfMemory![]TField from a, lines *lines into a {
    var out := List[TField].New(a)
    for w in lines {
        (after, before) := w.SplitOnce(": ").Must()
        try out.Push(TField(name: before, value: after, sensitive: false))
    }
    return out.Done()[..]
}

// Fits decodes block with dec into got (emptied first) and wants it to fit
// the list budget.
Fits(h: &Alloc, dec: mut Decoder, got: mut FieldBlock, block: []u8): DecodeError|OutOfMemory!void {
    got.Reset()
    Assert(try dec.Decode(h, got, block) == Outcome.Fits)
}

// Block checks one block of an RFC 7541 Appendix C sequence both ways:
// hex decodes with dec to list and leaves dec's table as table (newest
// first) of size size, and list encodes with enc to exactly hex and leaves
// enc's table the same.
Block(a: &Arena, h: &Alloc, dec: mut Decoder, enc: mut Encoder, got: mut FieldBlock, hex: str, list: []str, table: []str, size: int): DecodeError|OutOfMemory|TooSmall!void *list into a {
    wire := try Unhex(a, hex)
    try Fits(h, dec, got, wire)
    Assert(PairsAre(&*got, list))
    Assert(TableIs(&dec.table, table, size))
    fs := try ToFields(a, list)
    out := try a.Slice[u8](enc.Bound(fs))
    n := try enc.Encode(out, fs)
    Assert(BytesEq(out[0..n], wire))
    Assert(TableIs(&enc.table, table, size))
}
