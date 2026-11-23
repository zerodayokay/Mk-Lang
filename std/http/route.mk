// Why: most servers map a method and a path pattern to a handler; a const
// table of routes does that with no registration step, and allocates
// only the matched route's params.
//
// What: Dispatch over a []Route[C], and the matching it uses.
//
// How: one scan of the table, each pattern walked segment by segment
// against the path. Of the routes whose pattern and method match, the most
// specific pattern wins, as Go 1.22's ServeMux decides; the details are
// below.

// A HEAD request also matches a GET route (RFC 9110 9.3.2). Two patterns
// that match one path are compared segment by segment, and at the first
// segment where their kinds differ a literal beats {name}, which beats
// {name...} (so a literal beats a parameter, and a longer pattern beats a
// shorter one ending in {name...}). Equal patterns then prefer the route of
// the request's own method over GET for a HEAD request, then the earlier
// route: a const table cannot be refused at registration, where Go panics
// on two patterns neither of which is more specific. A path that some
// route's pattern matches with only other methods is 405 with an Allow
// field naming them (RFC 9110 15.5.6: sorted, HEAD beside GET, as Go's
// ServeMux writes it), and a path no pattern matches is 404. A literal
// segment matches the path's segment percent-decoded, as Go 1.22 ServeMux
// compares them, so /ab matches /a%62. A route table is a few dozen entries
// written in the program, so the scan costs about one string compare per
// segment and route; a routing trie (httprouter) pays off only for tables
// far larger, and would need building, which a const table cannot do.

/// Calls the handler of the most specific route whose method and pattern
/// match req, and passes its errors through. One scan of the table,
/// O(routes x segments); allocates the params and their values in a.
///
/// Matching: a literal segment matches the path's segment percent-decoded,
/// so /ab matches /a%62. Where two patterns match, the first segment where
/// they differ decides: a literal beats {name}, which beats {name...}, so a
/// longer pattern beats a shorter one ending in {name...}. Between equal
/// patterns, a HEAD request prefers a HEAD route to a GET route, then the
/// earlier route wins. A HEAD request also matches a GET route, and its
/// reply carries no body.
///
/// The handler gets a copy of req whose params hold the path's segments
/// for the pattern's {name} and {name...} segments, decoded and copied
/// into a, so a holds no loan of req. The names are views of the route's
/// pattern (`*routes into a`).
///
/// Errors: a path with a "." or ".." segment, plain or percent-encoded, is
/// Status(400) before any route is tried, since Request.path is never
/// cleaned. When routes match the path with other methods only, it is
/// Status(405) with res's Allow field naming them; when none matches, it
/// is Status(404).
export Dispatch[C](a: &Arena, io: Io, routes: []Route[C], c: &C, req: &Request, res: mut Response): Error!void *routes into a {
    if HasDotSegment(req.path) {
        fail Status(code: 400)
    }
    var best := -1
    var exact := false
    var other := false
    for k in 0..routes.Len() {
        if !Matches(routes[k].pattern, req.path) {
            continue
        }
        m := routes[k].method
        x := m == req.method
        if !x && !(req.method == "HEAD" && m == "GET") {
            other = true
            continue
        }
        if best < 0 {
            best = k
            exact = x
            continue
        }
        d := Specific(routes[k].pattern, routes[best].pattern)
        if d > 0 || (d == 0 && x && !exact) {
            best = k
            exact = x
        }
    }
    if best < 0 {
        if other {
            try res.Header("Allow", try Allowed(a, routes, req.path))
            fail Status(code: 405)
        }
        fail Status(code: 404)
    }
    if req.method == "HEAD" {
        res.head = true
    }
    // The {name} and {name...} segments of the path, by the winning
    // pattern, which matches it.
    pattern := routes[best].pattern
    path := req.path
    var ps := List[Header].New(a)
    var i := 0
    var j := 0
    for {
        pe := SegEnd(pattern, i)
        seg := pattern[i..pe]
        k := SegKind(seg)
        if k == 2 {
            v := try Unescape(a, path[j..])
            try ps.Push(Header(name: seg[1..seg.Len() - 4], value: v))
            break
        }
        qe := SegEnd(path, j)
        if k == 1 {
            v := try Unescape(a, path[j..qe])
            try ps.Push(Header(name: seg[1..seg.Len() - 1], value: v))
        }
        if pe == pattern.Len() {
            break
        }
        i = pe + 1
        j = qe + 1
    }
    r := Request(method: req.method, path: req.path, query: req.query, headers: req.headers, params: ps.Done()[..], body: req.body)
    h := routes[best].handler
    try h(c, a, io, &r, res)
}

// Specific compares two patterns that both match one path: 1 when p is
// the more specific, -1 when q is, 0 when neither is. At the first segment
// where their kinds differ, the lower kind wins (literal 0, {name} 1,
// {name...} 2). Two literals at one place both equal the path's decoded
// segment, so they are equal; a {name...} ends the walk.
Specific(p: str, q: str): int {
    var i := 0
    var j := 0
    for {
        pe := SegEnd(p, i)
        qe := SegEnd(q, j)
        kp := SegKind(p[i..pe])
        kq := SegKind(q[j..qe])
        if kp != kq {
            if kp < kq {
                return 1
            }
            return -1
        }
        if kp == 2 || pe == p.Len() || qe == q.Len() {
            return 0
        }
        i = pe + 1
        j = qe + 1
    }
}

// Allowed is the Allow field for path: the methods of the routes whose
// pattern matches it, each once, sorted, with HEAD when GET is there (Go's
// ServeMux), joined by ", " in a.
Allowed[C](a: &Arena, routes: []Route[C], path: str): OutOfMemory!str from a *routes into a {
    var ms := List[str].New(a)
    var get := false
    for r in routes {
        if Matches(r.pattern, path) {
            get = get || r.method == "GET"
            try AddSorted(mut ms, r.method)
        }
    }
    if get {
        try AddSorted(mut ms, "HEAD")
    }
    xs := ms.Items()
    var size := 0
    for m in xs {
        size += m.Len() + 2
    }
    out := try a.Slice[u8](size - 2)
    var o := 0
    for i in 0..xs.Len() {
        if i > 0 {
            out[o] = 0x2C
            out[o + 1] = 0x20
            o += 2
        }
        (mut out[o..o + xs[i].Len()]).CopyFrom(xs[i].AsBytes())
        o += xs[i].Len()
    }
    // Valid strings joined by ASCII bytes are well-formed UTF-8.
    return str.From(out[..]).Must()
}

// AddSorted inserts m into the sorted list ms unless it is there.
AddSorted(ms: mut List[str], m: str into ms): OutOfMemory!void {
    xs := ms.Items()
    var at := 0
    for at < xs.Len() && xs[at] < m {
        at += 1
    }
    if at < xs.Len() && xs[at] == m {
        return
    }
    try ms.Insert(at, m)
}

// SegEnd is the index of the "/" that ends the segment of s starting at i,
// or s.Len().
SegEnd(s: str, i: int): int {
    b := s.AsBytes()
    var j := i
    for j < b.Len() && b[j] != 0x2F {
        j += 1
    }
    return j
}

// SegKind is 0 for a literal segment, 1 for {name}, 2 for {name...}.
SegKind(seg: str): int {
    if seg.Len() < 2 || seg.Byte(0) != 0x7B || seg.Byte(seg.Len() - 1) != 0x7D {
        return 0
    }
    if seg.EndsWith("...}") {
        return 2
    }
    return 1
}

// Matches: path matches pattern. Both are walked from their first byte;
// a segment is the text between two "/" (the first one before the first
// "/", empty for a path that starts with one). A literal segment is
// compared with the path's segment percent-decoded (SegEq).
Matches(pattern: str, path: str): bool {
    var i := 0
    var j := 0
    for {
        pe := SegEnd(pattern, i)
        k := SegKind(pattern[i..pe])
        if k == 2 {
            return pe == pattern.Len()
        }
        if j > path.Len() {
            return false
        }
        qe := SegEnd(path, j)
        if k == 1 {
            if qe == j {
                return false
            }
        } else if !SegEq(pattern[i..pe], path[j..qe]) {
            return false
        }
        if pe == pattern.Len() || qe == path.Len() {
            return pe == pattern.Len() && qe == path.Len()
        }
        i = pe + 1
        j = qe + 1
    }
}

// SegEq: the literal lit equals seg with each %HH decoded (RFC 3986 2.1),
// with no allocation; a "%" not followed by two hex digits is itself.
SegEq(lit: str, seg: str): bool {
    x := lit.AsBytes()
    y := seg.AsBytes()
    var i := 0
    var j := 0
    for j < y.Len() {
        var c := y[j]
        j += 1
        if c == 0x25 && j + 1 < y.Len() && HexVal(y[j]) >= 0 && HexVal(y[j + 1]) >= 0 {
            c = Trunc[u8](HexVal(y[j]) * 16 + HexVal(y[j + 1]))
            j += 2
        }
        if i >= x.Len() || x[i] != c {
            return false
        }
        i += 1
    }
    return i == x.Len()
}

// Unescape is s with each %HH decoded (RFC 3986 2.1), a copy in a even
// when it holds no "%", so a param's value holds no loan of the request.
// ReadRequest refuses a malformed escape, so every "%" is
// followed by two hex digits; a decoded value that is not UTF-8 is
// Status(400).
Unescape(a: &Arena, s: str): Error!str from a {
    b := s.AsBytes()
    var pct := 0
    for i in 0..b.Len() {
        if b[i] == 0x25 {
            pct += 1
        }
    }
    if pct == 0 {
        return try a.Dup(s)
    }
    out := try a.Slice[u8](b.Len() - 2 * pct)
    var i := 0
    var o := 0
    for i < b.Len() {
        if b[i] == 0x25 && i + 2 < b.Len() && HexVal(b[i + 1]) >= 0 && HexVal(b[i + 2]) >= 0 {
            out[o] = Trunc[u8](HexVal(b[i + 1]) * 16 + HexVal(b[i + 2]))
            i += 3
        } else {
            if b[i] == 0x25 {
                fail Status(code: 400)
            }
            out[o] = b[i]
            i += 1
        }
        o += 1
    }
    v := str.From(out[..]) else {
        fail Status(code: 400)
    }
    return v
}

// HasDotSegment: some segment of path, percent-decoded, is "." or ".."
// (RFC 3986 3.3's dot-segments; "%2e" and "%2E" are "."). One pass;
// allocates nothing.
HasDotSegment(path: str): bool {
    var i := 0
    for i <= path.Len() {
        e := SegEnd(path, i)
        seg := path[i..e]
        if SegEq(".", seg) || SegEq("..", seg) {
            return true
        }
        i = e + 1
    }
    return false
}
