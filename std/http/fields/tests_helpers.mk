// Why: the RFC 7541 examples are hex dumps and lists of fields, and every
// test must compare a decoded list and a dynamic table with them.
//
// What: Unhex (an RFC hex dump to bytes), Pair and Got (a Sink that keeps
// what it gets), Same (bytes against text), PairsAre, and DynamicIs (a
// decoder's or encoder's table against the RFC's listing).
//
// How: plain helpers over the module's own types; nothing here is
// exported.

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

// One field a sink got.
struct Pair {
    name: []u8
    value: []u8
    never_index: bool
}

// A Sink that keeps every field in order.
struct Got {
    list: List[Pair]
}

impl Sink for Got {
    Add(mut self, name: []u8 into self, value: []u8 into self, never_index: bool): OutOfMemory!void {
        try self.list.Push(Pair(name: name, value: value, never_index: never_index))
    }
}

// Same is whether b holds the bytes of s.
Same(b: []u8, s: str): bool {
    return BytesEq(b, s.AsBytes())
}

// PairsAre is whether got holds exactly the fields of want, a list of
// "name: value" lines.
PairsAre(got: &Got, want: []str): bool {
    if got.list.Len() != want.Len() {
        return false
    }
    for i, w in want {
        (after, before) := w.SplitOnce(": ") else {
            return false
        }
        p := &got.list[i]
        if !Same(p.name, before) || !Same(p.value, after) {
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

// Block checks one block of an RFC 7541 Appendix C sequence both ways:
// hex decodes with dec to list and leaves dec's table as table (newest
// first) of size size, and list encodes with enc to exactly hex and leaves
// enc's table the same.
Block(a: &Arena, dec: mut Decoder, enc: mut Encoder, hex: str, list: []str, table: []str, size: int): DecodeError|OutOfMemory|TooSmall!void *list into a {
    wire := try Unhex(a, hex)
    var got := Got(list: List[Pair].New(a))
    try dec.Decode(a, mut got, wire)
    Assert(PairsAre(&got, list))
    Assert(TableIs(&dec.table, table, size))
    fs := try ToFields(a, list)
    out := try a.Slice[u8](enc.Bound(fs))
    n := try enc.Encode(out, fs)
    Assert(BytesEq(out[0..n], wire))
    Assert(TableIs(&enc.table, table, size))
}
