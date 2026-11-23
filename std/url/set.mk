// Why: changing one part of a URL must give the same result as the URL
// Standard's setters, which reparse the part in a state of the parser.
//
// What: Url.Set, the URL Standard's section 6.1 setters (href, protocol,
// username, password, host, hostname, port, pathname, search, hash), with
// CannotHaveCredentials and Parser.Override.
//
// How: the URL's record is rebuilt from its href, the setter runs the basic
// URL parser with its state override on the value (or sets the component
// itself, for username and password), and the result is serialized
// afresh.

/// A copy of the URL with part set to value, as the URL Standard's setter
/// of that part does it, in a. self is unchanged.
///
/// A value the setter ignores gives the URL as it was, and one it refuses
/// part way keeps what it had set (a host whose port is out of range keeps
/// the new host). Protocol takes the scheme with or without its ':',
/// Search and Hash take their text with or without the '?' or '#', and ""
/// for Port, Search or Hash removes that component.
///
/// Errors: Href parses value and fails as Parse does (BadUrl, BadHost); no
/// other part fails with BadUrl or BadHost. Any part fails with
/// IdnaUnsupported for a host that needs IDNA in a build without it.
/// O(n + m) for the URL's n bytes and the value's m; allocates the new
/// serialization in a, and the components that need encoding. value is
/// copied into a first, an Href's too, so the result holds no loan of
/// value.
export Url.Set(&self, a: &Arena, part: Part, value: str): Error|OutOfMemory!Url from a {
    if part == Part.Href {
        return try Parse(a, try a.Dup(value), none)
    }
    // A copy in a: the record's path list lives in a, and holds views of
    // what the setter parses.
    v := (try a.Dup(value)).AsBytes()
    var parser := NewParser(a, v, v, 0, State.SchemeStart, try RecordOf(a, self))
    match part {
        .Protocol {
            // value followed by ':', from the scheme start state.
            input := try a.Fmt("{value}:", (value))
            parser.text = try RemoveTabs(a, input.AsBytes())
            try parser.Override(State.SchemeStart)
        }
        .Username {
            if !CannotHaveCredentials(&parser.url) {
                parser.url.username = try Encode(a, v, InUserinfo)
            }
        }
        .Password {
            if !CannotHaveCredentials(&parser.url) {
                parser.url.password = try Encode(a, v, InUserinfo)
            }
        }
        .Host {
            if !parser.url.opaque {
                parser.text = try RemoveTabs(a, v)
                try parser.Override(State.Host)
            }
        }
        .Hostname {
            if !parser.url.opaque {
                parser.text = try RemoveTabs(a, v)
                try parser.Override(State.Hostname)
            }
        }
        .Port {
            if !CannotHaveCredentials(&parser.url) {
                if v.Len() == 0 {
                    parser.url.port = none
                } else {
                    parser.text = try RemoveTabs(a, v)
                    try parser.Override(State.Port)
                }
            }
        }
        .Pathname {
            if !parser.url.opaque {
                parser.url.path.Clear()
                parser.text = try RemoveTabs(a, v)
                try parser.Override(State.PathStart)
            }
        }
        .Search {
            if v.Len() == 0 {
                parser.url.query = none
            } else {
                parser.url.query = some("".AsBytes())
                parser.text = try RemoveTabs(a, if v[0] == b'?' { v[1..] } else { v })
                try parser.Override(State.Query)
            }
        }
        .Hash {
            if v.Len() == 0 {
                parser.url.fragment = none
            } else {
                parser.url.fragment = some("".AsBytes())
                parser.text = try RemoveTabs(a, if v[0] == b'#' { v[1..] } else { v })
                try parser.Override(State.Fragment)
            }
        }
        .Href {}
    }
    Parser(url, base: old_base, ..) := parser
    _ = old_base
    return try Serialize(a, &url)
}

// CannotHaveCredentials is the standard's "cannot have a username,
// password or port": no host, the empty host, or the scheme "file".
CannotHaveCredentials(url: &Record): bool {
    return url.host.IsNone() || IsEmptyHost(&url.host) || SameText(url.scheme, "file")
}

// Parser.Override runs the basic URL parser on self.text, the setter's
// value with its tabs and newlines removed (steps 2 and 3; step 1 does not
// run when a URL is given), with the URL and state as the state override,
// and leaves the URL as the run left it: a failure of the parser keeps what
// it had set, as the setters ignore it. Fails only with IdnaUnsupported and
// OutOfMemory.
Parser.Override(mut self, state: State): Error|OutOfMemory!void {
    self.raw = self.text
    self.p = 0
    self.buf = 0
    self.state = state
    self.over = some(state)
    self.Run() else e {
        match e {
            OutOfMemory {
                fail OutOfMemory
            }
            IdnaUnsupported {
                fail IdnaUnsupported
            }
            _ {}
        }
    }
}
