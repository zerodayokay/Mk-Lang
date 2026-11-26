// Why: HTTP/2 (RFC 9113 8.1-8.5) and HTTP/3 (RFC 9114 4.1-4.3) share the
// exact same field-byte, pseudo-header, connection-header, cookie, host and
// content-length validation rules over a decoded FieldBlock, and both must
// enforce them in one zero-allocation pass before any handler or caller sees
// the message.
//
// What: FieldView, Role, Malformed, Pseudo, RequestHead, CheckRequest,
// CheckResponse, CheckTrailers, JoinCookies, Sendable, and LowerName.
//
// How: a single pass over FieldBlock checks raw name and value bytes first,
// ensures UTF-8 via FieldBlock.Text, tracks pseudo-header ordering and
// uniqueness, enforces role-specific pseudo-headers and connection-specific
// prohibitions, parses Content-Length and :status without overflow, and
// compares :authority against Host using RFC 3986 scheme-based normalisation.

/// One decoded header field whose name and value are UTF-8 views of an arena
/// or FieldBlock.
export struct FieldView {
    /// Lower-case field name.
    export name: str
    /// Field value.
    export value: str
}

/// Which HTTP/2 or HTTP/3 field section is being validated.
export enum Role {
    Request
    Response
    Trailers
}

/// Why a decoded field section is malformed (RFC 9113 8.1.1, RFC 9114 4.1.2).
export enum Malformed {
    /// Empty name, byte in 0x00-0x20, 0x41-0x5a, or 0x7f-0xff, or colon past byte 0.
    NameByte
    /// NUL, LF, or CR in value, or leading/trailing SP or HTAB.
    ValueByte
    /// Name or value is not valid UTF-8.
    NotText
    /// A pseudo-header appeared after a regular field.
    PseudoAfter
    /// Undefined or disallowed pseudo-header for the role (e.g. :protocol, or any in trailers).
    PseudoUnknown
    /// A pseudo-header appeared more than once.
    PseudoRepeated
    /// Missing mandatory pseudo-header (:method, :scheme, :path, or :authority on CONNECT, or :status).
    PseudoMissing
    /// :path is empty, "*" without OPTIONS, or does not start with "/".
    BadPath
    /// :scheme is neither "http" nor "https".
    BadScheme
    /// Forbidden connection-specific field (connection, proxy-connection, keep-alive, transfer-encoding, upgrade).
    Connection
    /// te field with a value other than "trailers" (or outside a request).
    Te
    /// Host header identifies a different entity from :authority after normalisation.
    HostMismatch
    /// Invalid or conflicting content-length field value.
    ContentLength
    /// :status is not a valid three-digit HTTP status code (or is 101).
    Status
}

/// A malformed HTTP/2 or HTTP/3 field section (RFC 9113 8.1.1, RFC 9114 4.1.2).
export error MalformedField(why: Malformed)

/// A request's pseudo-headers when sending over HTTP/2 or HTTP/3.
export struct Pseudo {
    export method: str
    export scheme: str
    export authority: str
    export path: str
}

/// The validated pseudo-headers and framing metadata of a decoded request.
export struct RequestHead {
    export method: str
    export scheme: str
    export authority: str
    export path: str
    export content_length: ?i64
    export connect: bool
}

const MaxI64: i64 = 0x7FFF_FFFF_FFFF_FFFF

// ValidNameBytes checks RFC 9113 8.2.1 name rules: non-empty, no bytes in
// 0x00-0x20, 0x41-0x5a, or 0x7f-0xff, and no ':' after byte 0.
ValidNameBytes(name: []u8): bool {
    if name.Len() == 0 {
        return false
    }
    for i, c in name {
        if c <= 0x20 || (c >= 0x41 && c <= 0x5A) || c >= 0x7F {
            return false
        }
        if c == b':' && i > 0 {
            return false
        }
    }
    return true
}

// ValidValueBytes checks RFC 9113 8.2.1 value rules: no NUL, LF, CR anywhere,
// and no leading or trailing SP or HTAB.
ValidValueBytes(val: []u8): bool {
    n := val.Len()
    if n > 0 {
        first := val[0]
        last := val[n - 1]
        if first == 0x20 || first == 0x09 || last == 0x20 || last == 0x09 {
            return false
        }
    }
    for c in val {
        if c == 0x00 || c == 0x0A || c == 0x0D {
            return false
        }
    }
    return true
}

// IsConnectionField is true for the five connection-specific header names
// forbidden by RFC 9113 8.2.2 (lower-case exact match).
IsConnectionField(name: str): bool {
    return name == "connection" || name == "proxy-connection" || name == "keep-alive" || name == "transfer-encoding" || name == "upgrade"
}

// AsciiLowerEq compares a and b ignoring ASCII case.
AsciiLowerEq(a: []u8, b: []u8): bool {
    if a.Len() != b.Len() {
        return false
    }
    for i in 0..a.Len() {
        var ca := a[i]
        var cb := b[i]
        if ca >= b'A' && ca <= b'Z' {
            ca += 32
        }
        if cb >= b'A' && cb <= b'Z' {
            cb += 32
        }
        if ca != cb {
            return false
        }
    }
    return true
}

// ParseContentLength parses a non-negative decimal i64 content-length value.
ParseContentLength(s: str): ?i64 {
    b := s.AsBytes()
    if b.Len() == 0 {
        return none
    }
    var v: i64 = 0
    for c in b {
        if c < b'0' || c > b'9' {
            return none
        }
        d := i64(c - b'0')
        if v > (MaxI64 - d) / 10 {
            return none
        }
        v = v * 10 + d
    }
    return some(v)
}

// ParseStatus parses a 3-digit HTTP/2 or HTTP/3 status code, rejecting 101.
ParseStatus(s: str): ?int {
    b := s.AsBytes()
    if b.Len() != 3 {
        return none
    }
    if b[0] < b'1' || b[0] > b'9' || b[1] < b'0' || b[1] > b'9' || b[2] < b'0' || b[2] > b'9' {
        return none
    }
    code := int(b[0] - b'0') * 100 + int(b[1] - b'0') * 10 + int(b[2] - b'0')
    if code == 101 {
        return none
    }
    return some(code)
}

// HasAtSign reports whether s contains '@'.
HasAtSign(s: str): bool {
    for c in s.AsBytes() {
        if c == b'@' {
            return true
        }
    }
    return false
}

// ParsePort parses an optional port string after ':'; empty string returns
// default_port, non-digits or > 65535 return none.
ParsePort(b: []u8, default_port: int): ?int {
    if b.Len() == 0 {
        return some(default_port)
    }
    var p := 0
    for c in b {
        if c < b'0' || c > b'9' {
            return none
        }
        p = p * 10 + int(c - b'0')
        if p > 65535 {
            return none
        }
    }
    return some(p)
}

// SplitHostPort splits an authority or Host value into (host, effective_port)
// using default_port when omitted or empty; none on syntax error.
SplitHostPort(s: str, default_port: int): ?(host: []u8, port: int) {
    b := s.AsBytes()
    n := b.Len()
    if n == 0 {
        return some((host: b, port: default_port))
    }
    if b[0] == b'[' {
        var close := -1
        for i in 1..n {
            if b[i] == b']' {
                close = i
                break
            }
        }
        if close < 0 {
            return none
        }
        host := b[0..close + 1]
        if close + 1 == n {
            return some((host: host, port: default_port))
        }
        if b[close + 1] != b':' {
            return none
        }
        port := ParsePort(b[close + 2..], default_port) else {
            return none
        }
        return some((host: host, port: port))
    }
    var colon := -1
    for i in 0..n {
        if b[i] == b':' {
            if colon >= 0 {
                return none
            }
            colon = i
        }
    }
    if colon < 0 {
        return some((host: b, port: default_port))
    }
    port := ParsePort(b[colon + 1..], default_port) else {
        return none
    }
    return some((host: b[0..colon], port: port))
}

// SameAuthority compares :authority against Host after scheme-based
// normalisation (RFC 9113 8.3.1, RFC 3986 6.2.3).
SameAuthority(authority: str, host: str, scheme: str): bool {
    def := if scheme == "http" { 80 } else if scheme == "https" { 443 } else { -1 }
    a := SplitHostPort(authority, def) else {
        return false
    }
    h := SplitHostPort(host, def) else {
        return false
    }
    return AsciiLowerEq(a.host, h.host) && a.port == h.port
}

// CheckFieldCommon validates byte rules, UTF-8, connection-specific headers,
// te, and content-length for one field, updating cl.
CheckFieldCommon(b: &FieldBlock, i: int, role: Role, saw_regular: mut bool, cl: mut ?i64): MalformedField!(name: str, value: str, pseudo: bool) from b {
    raw := b.Get(i)
    if !ValidNameBytes(raw.name) {
        fail MalformedField(why: Malformed.NameByte)
    }
    if !ValidValueBytes(raw.value) {
        fail MalformedField(why: Malformed.ValueByte)
    }
    t := b.Text(i) else {
        fail MalformedField(why: Malformed.NotText)
    }
    if raw.name[0] == b':' {
        if *saw_regular {
            fail MalformedField(why: Malformed.PseudoAfter)
        }
        if role == Role.Trailers {
            fail MalformedField(why: Malformed.PseudoUnknown)
        }
        return (name: t.name, value: t.value, pseudo: true)
    }
    *saw_regular = true
    if IsConnectionField(t.name) {
        fail MalformedField(why: Malformed.Connection)
    }
    if t.name == "te" && (role != Role.Request || t.value != "trailers") {
        fail MalformedField(why: Malformed.Te)
    }
    if t.name == "content-length" {
        v := ParseContentLength(t.value) else {
            fail MalformedField(why: Malformed.ContentLength)
        }
        if prev := *cl {
            if prev != v {
                fail MalformedField(why: Malformed.ContentLength)
            }
        } else {
            *cl = some(v)
        }
    }
    return (name: t.name, value: t.value, pseudo: false)
}

/// Validates every field of b as a decoded HTTP/2 or HTTP/3 request section
/// in one pass (RFC 9113 8.1.1-8.5, RFC 9114 4.1.2-4.3). Errors:
/// MalformedField naming the exact violated Malformed rule. O(bytes of b);
/// allocates nothing. Returned strings borrow from b.
export CheckRequest(b: &FieldBlock): MalformedField!RequestHead from b {
    var saw_regular := false
    var seen_method := false
    var seen_scheme := false
    var seen_auth := false
    var seen_path := false
    var method := ""
    var scheme := ""
    var authority := ""
    var path := ""
    var host: ?str = none
    var cl: ?i64 = none
    for i in 0..b.Len() {
        f := try CheckFieldCommon(b, i, Role.Request, mut saw_regular, mut cl)
        if f.pseudo {
            if f.name == ":method" {
                if seen_method {
                    fail MalformedField(why: Malformed.PseudoRepeated)
                }
                seen_method = true
                method = f.value
            } else if f.name == ":scheme" {
                if seen_scheme {
                    fail MalformedField(why: Malformed.PseudoRepeated)
                }
                seen_scheme = true
                scheme = f.value
            } else if f.name == ":authority" {
                if seen_auth {
                    fail MalformedField(why: Malformed.PseudoRepeated)
                }
                seen_auth = true
                authority = f.value
            } else if f.name == ":path" {
                if seen_path {
                    fail MalformedField(why: Malformed.PseudoRepeated)
                }
                seen_path = true
                path = f.value
            } else {
                fail MalformedField(why: Malformed.PseudoUnknown)
            }
        } else if f.name == "host" {
            if prev := host {
                if !SameAuthority(prev, f.value, if seen_scheme { scheme } else { "" }) {
                    fail MalformedField(why: Malformed.HostMismatch)
                }
            } else {
                host = some(f.value)
            }
        }
    }
    if !seen_method || method == "" {
        fail MalformedField(why: Malformed.PseudoMissing)
    }
    if method == "CONNECT" {
        if seen_scheme || seen_path {
            fail MalformedField(why: Malformed.PseudoUnknown)
        }
        if !seen_auth || authority == "" {
            fail MalformedField(why: Malformed.PseudoMissing)
        }
        if HasAtSign(authority) {
            fail MalformedField(why: Malformed.PseudoUnknown)
        }
        if hv := host {
            if !SameAuthority(authority, hv, "") {
                fail MalformedField(why: Malformed.HostMismatch)
            }
        }
        return RequestHead(method: method, scheme: "", authority: authority, path: "", content_length: cl, connect: true)
    }
    if !seen_scheme || !seen_path {
        fail MalformedField(why: Malformed.PseudoMissing)
    }
    if scheme != "http" && scheme != "https" {
        fail MalformedField(why: Malformed.BadScheme)
    }
    if path == "" {
        fail MalformedField(why: Malformed.BadPath)
    }
    if path == "*" {
        if method != "OPTIONS" {
            fail MalformedField(why: Malformed.BadPath)
        }
    } else if path.AsBytes()[0] != b'/' {
        fail MalformedField(why: Malformed.BadPath)
    }
    if seen_auth {
        if HasAtSign(authority) {
            fail MalformedField(why: Malformed.PseudoUnknown)
        }
        if hv := host {
            if !SameAuthority(authority, hv, scheme) {
                fail MalformedField(why: Malformed.HostMismatch)
            }
        }
    }
    return RequestHead(method: method, scheme: scheme, authority: authority, path: path, content_length: cl, connect: false)
}

/// Validates every field of b as a decoded HTTP/2 or HTTP/3 response section
/// in one pass (RFC 9113 8.3.2, 8.6). Errors: MalformedField naming the exact
/// violated Malformed rule. O(bytes of b); allocates nothing.
export CheckResponse(b: &FieldBlock): MalformedField!(status: int, content_length: ?i64) {
    var saw_regular := false
    var seen_status := false
    var status := 0
    var cl: ?i64 = none
    for i in 0..b.Len() {
        f := try CheckFieldCommon(b, i, Role.Response, mut saw_regular, mut cl)
        if f.pseudo {
            if f.name == ":status" {
                if seen_status {
                    fail MalformedField(why: Malformed.PseudoRepeated)
                }
                seen_status = true
                status = ParseStatus(f.value) else {
                    fail MalformedField(why: Malformed.Status)
                }
            } else {
                fail MalformedField(why: Malformed.PseudoUnknown)
            }
        }
    }
    if !seen_status {
        fail MalformedField(why: Malformed.PseudoMissing)
    }
    return (status: status, content_length: cl)
}

/// Validates every field of b as a decoded HTTP/2 or HTTP/3 trailer section
/// in one pass (RFC 9113 8.1, 8.3). Errors: MalformedField naming the exact
/// violated Malformed rule. O(bytes of b); allocates nothing.
export CheckTrailers(b: &FieldBlock): MalformedField!void {
    var saw_regular := false
    var cl: ?i64 = none
    for i in 0..b.Len() {
        _ = try CheckFieldCommon(b, i, Role.Trailers, mut saw_regular, mut cl)
    }
}

/// Joins multiple cookie fields in b with "; " into out (RFC 9113 8.2.3) and
/// returns the length written, or none when b holds at most one cookie field
/// or out is shorter than the joined value. O(bytes of b); allocates nothing.
export JoinCookies(b: &FieldBlock, out: mut []u8): ?int {
    cookie := "cookie".AsBytes()
    var count := 0
    var need := 0
    for f in b.Iter() {
        if BytesEq(f.name, cookie) {
            if count > 0 {
                need += 2
            }
            need += f.value.Len()
            count += 1
        }
    }
    if count <= 1 || need > out.Len() {
        return none
    }
    var o := 0
    var first := true
    for f in b.Iter() {
        if BytesEq(f.name, cookie) {
            if !first {
                out[o] = b';'
                out[o + 1] = b' '
                o += 2
            }
            first = false
            (mut out[o..o + f.value.Len()]).CopyFrom(f.value)
            o += f.value.Len()
        }
    }
    return some(o)
}

/// Reports whether a header field named name may be sent on HTTP/2 or HTTP/3
/// (RFC 9113 8.2.2, 8.3.1): false (case-insensitively) for connection,
/// proxy-connection, keep-alive, transfer-encoding, upgrade, te, and host.
/// O(name); allocates nothing.
export Sendable(name: []u8): bool {
    return !AsciiLowerEq(name, "connection".AsBytes()) && !AsciiLowerEq(name, "proxy-connection".AsBytes()) && !AsciiLowerEq(name, "keep-alive".AsBytes()) && !AsciiLowerEq(name, "transfer-encoding".AsBytes()) && !AsciiLowerEq(name, "upgrade".AsBytes()) && !AsciiLowerEq(name, "te".AsBytes()) && !AsciiLowerEq(name, "host".AsBytes())
}

/// Copies name lower-cased into out[0..name.Len()] (RFC 9113 8.2) and checks
/// that name and value contain only sendable bytes (RFC 9113 8.2.1); returns
/// false when name is empty, out is too short, or any byte of name or value
/// violates RFC 9113 8.2.1 (colons in name included). O(name + value);
/// allocates nothing.
export LowerName(name: []u8, value: []u8, out: mut []u8): bool {
    n := name.Len()
    if n == 0 || out.Len() < n || !ValidValueBytes(value) {
        return false
    }
    for i, c in name {
        if c <= 0x20 || c >= 0x7F || c == b':' {
            return false
        }
        out[i] = if c >= b'A' && c <= b'Z' { c + 32 } else { c }
    }
    return true
}
