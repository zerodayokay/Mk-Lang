// Why: the records a handler and a client see are plain data, shared by
// every part of std.http.
//
// What: Header, Request, Reply and Route, and Request's lookups Header,
// Param and ParamInt.
//
// How: every str and slice in a Request or Reply is a view of the arena it
// was read into, so the records own nothing.

/// One header field: its name and value, as they were read or as they are
/// to be written.
export struct Header {
    /// The field name, such as "Content-Type".
    export name: str
    /// The field value.
    export value: str
}

/// A request as the server read it: the method, the target split into path
/// and query, the header fields in the order received, the path parameters
/// Dispatch matched, and the body. path and query are the bytes received,
/// never decoded or cleaned. Every string and slice is a view of the
/// request's arena.
export struct Request {
    /// The method, such as "GET".
    export method: str
    /// The target's path, as received.
    export path: str
    /// The target's query, as received.
    export query: str
    /// The header fields in the order received.
    export headers: []Header
    /// The path parameters Dispatch matched: each pattern name with the text it
    /// matched.
    export params: []Header
    /// The body.
    export body: []u8
}

/// A whole reply, as Fetch and Client.Fetch return it: the status code, the
/// header fields in the order received, and the body. Every string and
/// slice is a view of the arena it was read into.
export struct Reply {
    /// The status code, such as 200.
    export status: int
    /// The header fields in the order received.
    export headers: []Header
    /// The body.
    export body: []u8
}

/// One row of a Dispatch table: a method, a path pattern, and the handler
/// that answers it. A pattern is literal segments, `{name}` segments that
/// match one segment, and a last `{name...}` segment that matches the rest
/// of the path. The handler gets the context C that Dispatch is given.
/// Declare the table as a `const []Route[C]`, so every task can read it.
export struct Route[C] {
    /// The method the row answers, such as "GET".
    export method: str
    /// The path pattern: literal segments, `{name}` segments and a last
    /// `{name...}` segment.
    export pattern: str
    /// The function that answers a request that matches, given the context
    /// Dispatch is given.
    export handler: fn(c: &C, a: &Arena, io: Io, req: &Request, res: mut Response): Error!void
}

/// The value of the first header field named name, or none when there is
/// none. Names compare without regard to ASCII case. O(fields times name
/// length); allocates nothing.
export Request.Header(&self, name: str): ?str from self {
    return Lookup(self.headers, name)
}

/// The value of the path parameter name, which Dispatch filled from the
/// route's pattern, or none when the route has no such parameter. Names
/// compare exactly. O(parameters); allocates nothing.
export Request.Param(&self, name: str): ?str from self {
    for p in self.params {
        if p.name == name {
            return some(p.value)
        }
    }
    return none
}

/// The path parameter name read as a decimal i64, or none when it is
/// absent or not a number. O(parameters plus the value's length); allocates
/// nothing.
export Request.ParamInt(&self, name: str): ?i64 {
    p := self.Param(name) else {
        return none
    }
    v := p.ParseInt() else {
        return none
    }
    return some(v)
}

// Lookup is the first value whose name equals name ignoring ASCII case.
Lookup(hs: []Header, name: str): ?str from hs {
    for h in hs {
        if EqFold(h.name, name) {
            return some(h.value)
        }
    }
    return none
}

// EqFold compares a and b with ASCII letters folded to lower case.
EqFold(a: str, b: str): bool {
    if a.Len() != b.Len() {
        return false
    }
    x := a.AsBytes()
    y := b.AsBytes()
    for i in 0..x.Len() {
        if Lower(x[i]) != Lower(y[i]) {
            return false
        }
    }
    return true
}

Lower(c: u8): u8 {
    if c >= 0x41 && c <= 0x5A {
        return c + 0x20
    }
    return c
}
