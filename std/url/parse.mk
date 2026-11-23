// Why: URLs are untrusted input, and only the URL Standard's own parser
// gives the same answers as browsers and other conforming parsers.
//
// What: the URL Standard's basic URL parser (its section 4.4): Parse
// (which tries plain.mk's ParsePlain first) and ParseWith (steps 1 to 3),
// the Parser with its state machine Parser.Run (step 9), and one method
// per state, with the state's step numbers in comments (the
// validation-error steps are not kept, as the standard allows).
//
// How: the pointer and the buffer are offsets into the input: every state's
// buffer is the input from Parser.buf to the pointer, and it is encoded
// once, when its state ends, so a component that needs no encoding is a
// view of the input. A state whose next code points cannot end it consumes
// them in one loop, which is what running it once per code point would do.
// Encoding is UTF-8 only, so the query state's encoding step has nothing to
// do.

// State is a state of the basic URL parser.
enum State {
    SchemeStart
    Scheme
    NoScheme
    SpecialRelativeOrAuthority
    PathOrAuthority
    Relative
    RelativeSlash
    SpecialAuthoritySlashes
    SpecialAuthorityIgnoreSlashes
    Authority
    Host
    Hostname
    Port
    File
    FileSlash
    FileHost
    PathStart
    Path
    OpaquePath
    Query
    Fragment
}

// Go is what a state tells the machine: run on, or stop with success (a
// state override's return).
enum Go {
    Next
    Stop
}

// The code points the states compare c with; c is a byte, or Eof.
const Eof: int = -1
const CHash: int = 0x23
const CPercent: int = 0x25
const CSlash: int = 0x2F
const CColon: int = 0x3A
const CQuestion: int = 0x3F
const CAt: int = 0x40
const CLeftBracket: int = 0x5B
const CBackslash: int = 0x5C
const CRightBracket: int = 0x5D
const CSpace: int = 0x20

// Parser is the state of one run of the basic URL parser over in.
struct Parser {
    a: &Arena
    text: []u8
    // p is the pointer; buf is where the current buffer starts in in.
    p: int
    buf: int
    state: State
    over: ?State
    url: Record
    // The base. Its record, base, is built only when a state reads more of
    // it than its scheme: Run stops with want_base set before such
    // a state, ParseWith builds the record (built), and Run goes on. So an
    // absolute input never copies the base. base_special is the base's
    // scheme's SpecialIndex (step 2.6 compares only a special scheme), and
    // base_file whether it is "file"; neither holds a loan of the base.
    base: Record
    has_base: bool
    built: bool
    want_base: bool
    base_special: int
    base_file: bool
    // The authority state's atSignSeen, the offset of its last '@' and
    // where the authority started; the host state's insideBrackets.
    at_seen: bool
    last_at: int
    auth: int
    brackets: bool
    // lead is how many bytes step 1 trimmed from the input's start, and
    // raw the input before steps 2 and 3 removed tabs and newlines, so an
    // error's offset is the input's as given.
    lead: int
    raw: []u8
    // no_run is where the last PathRun refused: no run is tried again until
    // the pointer is past it, so a failed run reads each byte once.
    no_run: int
}

/// Parses input as a URL, resolved against base when one is given, by the
/// WHATWG URL Standard's parser; input is UTF-8, as every str is. The
/// result lives in a.
///
/// Errors: BadUrl where the parser fails, BadHost for a host the host
/// parser refuses, IdnaUnsupported for a host that needs IDNA in a build
/// without it, OutOfMemory. An ASCII host is lower-cased and not checked
/// by UTS 46, so an xn-- label in a parsed URL need not be valid Punycode;
/// DomainToAscii(a, host, true) checks it. Input from a peer never makes
/// Parse panic, and its memory is bounded by the input's length.
///
/// O(n + m) for n bytes of input and an m-byte base. It allocates in a the
/// URL's serialization (at most 3n + m + 64 bytes) and O(n + m) bytes of
/// scratch: the components that need encoding, the path's segments, and,
/// only when the parser reads the base beyond its scheme (a relative
/// input), a copy of the base's href and segments. Input that is already
/// in its serialized form costs one copy and no scratch, with or without a
/// base. The parser keeps views of input in a, so input is stored into a
/// and the result holds its loan.
export Parse(a: &Arena, input: str into a, base: ?&Url): Error|OutOfMemory!Url from a, input {
    var read := false
    return try ParseWith(a, input, base, mut read)
}

// ParseWith is Parse, and sets read to whether the parser built the base's
// record (a copy of the base in a): the tests' witness that an input that
// does not need the base never copies it.
ParseWith(a: &Arena, input: str into a, base: ?&Url, read: mut bool): Error|OutOfMemory!Url from a, input {
    // An absolute URL in its serialized form, but for capitals in its
    // scheme and host, reads no base: one pass decides it (ParsePlain).
    if u := try ParsePlain(a, input) {
        *read = false
        return u
    }
    b := input.AsBytes()
    // Step 1: trim leading and trailing C0 controls and spaces.
    var start := 0
    var end := b.Len()
    for start < end && b[start] <= 0x20 {
        start += 1
    }
    for end > start && b[end - 1] <= 0x20 {
        end -= 1
    }
    trimmed := b[start..end]
    // Steps 2 and 3: remove tabs and newlines.
    clean := try RemoveTabs(a, trimmed)
    var parser := NewParser(a, clean, trimmed, start, State.SchemeStart, Record.New(a))
    if u := base {
        parser.has_base = true
        parser.base_special = SpecialIndex(u.Scheme().AsBytes())
        parser.base_file = SameText(u.Scheme().AsBytes(), "file")
    }
    try parser.Run()
    if parser.want_base {
        // Run stopped before the first state that reads the base.
        if u := base {
            parser.base = try RecordOf(a, u)
        }
        parser.built = true
        parser.want_base = false
        try parser.Run()
    }
    Parser(url, base: old_base, built, ..) := parser
    _ = old_base
    *read = built
    // Input that is already its serialization is copied once
    // (SerializeSame); a removed tab, or a base the parser read, rules that
    // out at once.
    if !built && clean.Len() == trimmed.Len() {
        if u := try SerializeSame(a, &url, input[start..end]) {
            return u
        }
    }
    return try Serialize(a, &url)
}

// NewParser is a parser at the start of text, the input after steps 1 to
// 3 (raw before steps 2 and 3, lead bytes trimmed before it), in state,
// building url, with no base.
NewParser(a: &Arena, text: []u8, raw: []u8, lead: int, state: State, url: Record): Parser from a, text, raw, url {
    return Parser(a: a, text: text, p: 0, buf: 0, state: state, over: none, url: url, base: Record.New(a), has_base: false, built: false, want_base: false, base_special: 0, base_file: false, at_seen: false, last_at: 0, auth: 0, brackets: false, lead: lead, raw: raw, no_run: -1)
}

// RemoveTabs is s without its tabs, line feeds and carriage returns: s
// itself when it has none, else a copy in a. O(n); allocates at most n
// bytes.
RemoveTabs(a: &Arena, s: []u8): OutOfMemory![]u8 from a, s {
    var n := 0
    for c in s {
        if c == b'\t' || c == b'\n' || c == b'\r' {
            n += 1
        }
    }
    if n == 0 {
        return s
    }
    out := try a.Slice[u8](s.Len() - n)
    var o := 0
    for c in s {
        if c != b'\t' && c != b'\n' && c != b'\r' {
            out[o] = c
            o += 1
        }
    }
    return out[..]
}

// Parser.Bad is BadUrl at the pointer, as an offset of the input as given:
// the trimmed bytes before it, and the tabs and newlines removed before it.
Parser.Bad(&self): Error {
    var p := self.p
    if p < 0 {
        p = 0
    }
    if p > self.text.Len() {
        p = self.text.Len()
    }
    if self.raw.Len() == self.text.Len() {
        return BadUrl(offset: self.lead + p)
    }
    var kept := 0
    var i := 0
    for i < self.raw.Len() && kept < p {
        c := self.raw[i]
        if c != b'\t' && c != b'\n' && c != b'\r' {
            kept += 1
        }
        i += 1
    }
    // The loop stops after the p-th kept byte; tabs and newlines between it
    // and the byte at the pointer were removed too.
    for i < self.raw.Len() && (self.raw[i] == b'\t' || self.raw[i] == b'\n' || self.raw[i] == b'\r') {
        i += 1
    }
    return BadUrl(offset: self.lead + i)
}

// Parser.Run is step 9: the state machine, from self.state, until the
// pointer is past the input or a state stops it.
Parser.Run(mut self): Error|OutOfMemory!void {
    for {
        c := if self.p >= 0 && self.p < self.text.Len() { int(self.text[self.p]) } else { Eof }
        if self.has_base && !self.built && self.ReadsBase(c) {
            // ParseWith builds the base's record and runs on from here.
            self.want_base = true
            return
        }
        step := match self.state {
            .SchemeStart { try self.SchemeStartState(c) }
            .Scheme { try self.SchemeState(c) }
            .NoScheme { try self.NoSchemeState(c) }
            .SpecialRelativeOrAuthority { self.SpecialRelativeOrAuthorityState(c) }
            .PathOrAuthority { self.PathOrAuthorityState(c) }
            .Relative { try self.RelativeState(c) }
            .RelativeSlash { self.RelativeSlashState(c) }
            .SpecialAuthoritySlashes { self.SpecialAuthoritySlashesState(c) }
            .SpecialAuthorityIgnoreSlashes { self.SpecialAuthorityIgnoreSlashesState(c) }
            .Authority { try self.AuthorityState(c) }
            .Host | .Hostname { try self.HostState(c) }
            .Port { try self.PortState(c) }
            .File { try self.FileState(c) }
            .FileSlash { try self.FileSlashState(c) }
            .FileHost { try self.FileHostState(c) }
            .PathStart { try self.PathStartState(c) }
            .Path { try self.PathState(c) }
            .OpaquePath { try self.OpaquePathState(c) }
            .Query { try self.QueryState(c) }
            .Fragment { try self.FragmentState() }
        }
        if step == Go.Stop {
            return
        }
        if self.p >= self.text.Len() {
            return
        }
        self.p += 1
    }
}

// Parser.Enter sets the state to next with an empty buffer, which starts
// at the next code point the machine reads.
Parser.Enter(mut self, next: State): Go {
    self.state = next
    self.buf = self.p + 1
    return Go.Next
}

// Parser.Back is "decrease pointer by 1" and Enter: next reads c again.
Parser.Back(mut self, next: State): Go {
    self.p -= 1
    return self.Enter(next)
}

// Parser.Next is the code point after the pointer, or Eof: the first code
// point of the standard's "remaining".
Parser.Next(&self): int {
    if self.p + 1 >= 0 && self.p + 1 < self.text.Len() {
        return int(self.text[self.p + 1])
    }
    return Eof
}

// Parser.Overridden is whether a state override is given.
Parser.Overridden(&self): bool {
    return self.over.IsSome()
}

// Parser.IsHostnameSetter is whether the state override is the hostname
// state.
Parser.IsHostnameSetter(&self): bool {
    if o := self.over {
        return o == State.Hostname
    }
    return false
}

// Parser.Buffer is the current buffer: the input from buf to the pointer.
Parser.Buffer(&self): []u8 from self.text {
    return self.text[self.buf..self.p]
}

// Parser.HasCredentials is whether the URL includes credentials.
Parser.HasCredentials(&self): bool {
    return self.url.username.Len() > 0 || self.url.password.Len() > 0
}

// Parser.IsFile is whether the URL's scheme is "file".
Parser.IsFile(&self): bool {
    return self.url.file
}

// Parser.BaseIsFile is whether there is a base and its scheme is "file",
// known without the base's record.
Parser.BaseIsFile(&self): bool {
    return self.has_base && self.base_file
}

// Parser.ReadsBase is whether the state about to run on c reads the base's
// record: the no scheme, relative and relative slash states, and the file
// and file slash states on a code point other than '/' or '' with a file
// base. Every read of self.base is in one of these.
Parser.ReadsBase(&self, c: int): bool {
    return match self.state {
        .NoScheme | .Relative | .RelativeSlash { true }
        .File | .FileSlash { self.base_file && c != CSlash && c != CBackslash }
        _ { false }
    }
}

// SpecialIndex numbers the special schemes from 1 (ws, wss, ftp, http,
// file, https); any other scheme is 0.
SpecialIndex(scheme: []u8): int {
    if !IsSpecialScheme(scheme) {
        return 0
    }
    return match scheme.Len() {
        2 { 1 }
        3 { if SameText(scheme, "wss") { 2 } else { 3 } }
        4 { if SameText(scheme, "http") { 4 } else { 5 } }
        _ { 6 }
    }
}

// Parser.SetScheme sets the URL's scheme and whether it is special.
Parser.SetScheme(mut self, scheme: []u8 into self) {
    self.url.scheme = scheme
    self.url.special = IsSpecialScheme(scheme)
    self.url.file = SameText(scheme, "file")
    self.url.dport = DefaultPort(scheme)
}

// Parser.CloneBasePath sets the URL's path to a copy of the base's.
Parser.CloneBasePath(mut self): OutOfMemory!void {
    self.url.path.Clear()
    for seg in self.base.path {
        try self.url.path.Push(seg)
    }
}

// Parser.Shorten is the standard's shorten a URL's path: drop the last
// segment, unless the URL is a file URL whose path is one normalized
// Windows drive letter.
Parser.Shorten(mut self) {
    n := self.url.path.Len()
    if self.IsFile() && n == 1 && IsNormalizedDriveLetter(self.url.path[0]) {
        return
    }
    _ = self.url.path.Pop()
}

// SchemeStartState is the scheme start state.
Parser.SchemeStartState(mut self, c: int): Error!Go {
    // Step 1: a letter starts the scheme; the buffer starts here.
    if c >= 0 && IsAlpha(Trunc[u8](c)) {
        self.state = State.Scheme
        self.buf = self.p
        return Go.Next
    }
    // Step 2.
    if !self.Overridden() {
        return self.Back(State.NoScheme)
    }
    // Step 3.
    fail self.Bad()
}

// IsSchemeByte is whether c may follow a scheme's first letter.
IsSchemeByte(c: u8): bool {
    return IsAlpha(c) || IsDigit(c) || c == b'+' || c == b'-' || c == b'.'
}

// SchemeState is the scheme state.
Parser.SchemeState(mut self, c: int): Error|OutOfMemory!Go {
    // Step 1: the scheme goes on; take every scheme byte that follows.
    if c >= 0 && IsSchemeByte(Trunc[u8](c)) {
        for self.p + 1 < self.text.Len() && IsSchemeByte(self.text[self.p + 1]) {
            self.p += 1
        }
        return Go.Next
    }
    // Step 2: the scheme ends.
    if c == CColon {
        buffer := try SchemeOf(self.a, self.Buffer())
        // Step 2.1: a setter may not change a scheme's kind.
        if self.Overridden() {
            special := IsSpecialScheme(buffer)
            if self.url.special != special {
                return Go.Stop
            }
            if (self.HasCredentials() || self.url.port.IsSome()) && SameText(buffer, "file") {
                return Go.Stop
            }
            if self.IsFile() && IsEmptyHost(&self.url.host) {
                return Go.Stop
            }
        }
        // Step 2.2.
        self.SetScheme(buffer)
        // Step 2.3.
        if self.Overridden() {
            if SamePort(self.url.port, self.url.dport) {
                self.url.port = none
            }
            return Go.Stop
        }
        // Steps 2.4 and 2.5.
        if self.IsFile() {
            return self.Enter(State.File)
        }
        // Step 2.6.
        if self.url.special && self.has_base && self.base_special == SpecialIndex(self.url.scheme) {
            return self.Enter(State.SpecialRelativeOrAuthority)
        }
        // Step 2.7.
        if self.url.special {
            return self.Enter(State.SpecialAuthoritySlashes)
        }
        // Step 2.8.
        if self.Next() == CSlash {
            self.p += 1
            return self.Enter(State.PathOrAuthority)
        }
        // Step 2.9: an opaque path.
        self.url.opaque = true
        self.url.opath = "".AsBytes()
        return self.Enter(State.OpaquePath)
    }
    // Step 3: no scheme after all; start over.
    if !self.Overridden() {
        self.p = -1
        return self.Enter(State.NoScheme)
    }
    // Step 4.
    fail self.Bad()
}

// NoSchemeState is the no scheme state.
Parser.NoSchemeState(mut self, c: int): Error!Go {
    // Step 1.
    if !self.has_base || self.base.opaque && c != CHash {
        fail self.Bad()
    }
    // Step 2: a fragment on a base with an opaque path.
    if self.base.opaque && c == CHash {
        self.SetScheme(self.base.scheme)
        self.url.opaque = true
        self.url.opath = self.base.opath
        self.url.query = self.base.query
        self.url.fragment = some("".AsBytes())
        return self.Enter(State.Fragment)
    }
    // Step 3.
    if !self.BaseIsFile() {
        return self.Back(State.Relative)
    }
    // Step 4.
    return self.Back(State.File)
}

// SpecialRelativeOrAuthorityState is the special relative or authority
// state.
Parser.SpecialRelativeOrAuthorityState(mut self, c: int): Go {
    // Step 1.
    if c == CSlash && self.Next() == CSlash {
        self.p += 1
        return self.Enter(State.SpecialAuthorityIgnoreSlashes)
    }
    // Step 2.
    return self.Back(State.Relative)
}

// PathOrAuthorityState is the path or authority state.
Parser.PathOrAuthorityState(mut self, c: int): Go {
    // Step 1.
    if c == CSlash {
        self.auth = self.p + 1
        return self.Enter(State.Authority)
    }
    // Step 2.
    return self.Back(State.Path)
}

// Parser.CopyBaseAuthority sets the URL's username, password, host and
// port to the base's.
Parser.CopyBaseAuthority(mut self) {
    self.url.username = self.base.username
    self.url.password = self.base.password
    self.url.host = self.base.host
    self.url.port = self.base.port
}

// RelativeState is the relative state.
Parser.RelativeState(mut self, c: int): OutOfMemory!Go {
    // Steps 1 and 2: the base's scheme is not "file".
    self.SetScheme(self.base.scheme)
    // Steps 3 and 4.
    if c == CSlash || self.url.special && c == CBackslash {
        return self.Enter(State.RelativeSlash)
    }
    // Step 5.1.
    self.CopyBaseAuthority()
    try self.CloneBasePath()
    self.url.query = self.base.query
    // Step 5.2.
    if c == CQuestion {
        self.url.query = some("".AsBytes())
        return self.Enter(State.Query)
    }
    // Step 5.3.
    if c == CHash {
        self.url.fragment = some("".AsBytes())
        return self.Enter(State.Fragment)
    }
    // Step 5.4.
    if c != Eof {
        self.url.query = none
        self.Shorten()
        return self.Back(State.Path)
    }
    return Go.Next
}

// RelativeSlashState is the relative slash state.
Parser.RelativeSlashState(mut self, c: int): Go {
    // Step 1.
    if self.url.special && (c == CSlash || c == CBackslash) {
        return self.Enter(State.SpecialAuthorityIgnoreSlashes)
    }
    // Step 2.
    if c == CSlash {
        self.auth = self.p + 1
        return self.Enter(State.Authority)
    }
    // Step 3.
    self.CopyBaseAuthority()
    return self.Back(State.Path)
}

// SpecialAuthoritySlashesState is the special authority slashes state.
Parser.SpecialAuthoritySlashesState(mut self, c: int): Go {
    // Step 1.
    if c == CSlash && self.Next() == CSlash {
        self.p += 1
        return self.Enter(State.SpecialAuthorityIgnoreSlashes)
    }
    // Step 2.
    return self.Back(State.SpecialAuthorityIgnoreSlashes)
}

// SpecialAuthorityIgnoreSlashesState is the special authority ignore
// slashes state.
Parser.SpecialAuthorityIgnoreSlashesState(mut self, c: int): Go {
    // Step 1.
    if c != CSlash && c != CBackslash {
        self.p -= 1
        self.auth = self.p + 1
        return self.Enter(State.Authority)
    }
    // Step 2: a slash is skipped.
    return Go.Next
}

// Parser.EndsAuthority is whether c ends an authority, a host or a port:
// EOF, '/', '?', '#', or '\' in a special URL.
Parser.EndsAuthority(&self, c: int): bool {
    return c == Eof || c == CSlash || c == CQuestion || c == CHash || self.url.special && c == CBackslash
}

// AuthorityState is the authority state. The buffers of step 1 are one
// span: every '@' but the last is encoded as "%40" by the userinfo set, so
// the username and password are the input from the authority's start to
// its last '@', split at its first ':'.
Parser.AuthorityState(mut self, c: int): Error|OutOfMemory!Go {
    // Step 1: an '@' ends the credentials so far.
    if c == CAt {
        self.at_seen = true
        self.last_at = self.p
        self.buf = self.p + 1
        return Go.Next
    }
    // Step 2: the authority ends.
    if self.EndsAuthority(c) {
        // Step 2.1.
        if self.at_seen && self.buf == self.p {
            fail self.Bad()
        }
        if self.at_seen {
            info := self.text[self.auth..self.last_at]
            var colon := info.Len()
            for i, x in info {
                if x == b':' && colon == info.Len() {
                    colon = i
                }
            }
            self.url.username = try Encode(self.a, info[0..colon], InUserinfo)
            if colon < info.Len() {
                self.url.password = try Encode(self.a, info[colon + 1..], InUserinfo)
            }
        }
        // Step 2.2: back to the buffer's start, which the host state reads.
        self.p = self.buf - 1
        self.state = State.Host
        self.brackets = false
        return Go.Next
    }
    // Step 3: take every byte up to the next '@' or end.
    self.Skip(StopPath | StopAt | self.BackslashStop())
    return Go.Next
}

// HostState is the host state and the hostname state.
Parser.HostState(mut self, c: int): Error|OutOfMemory!Go {
    // Step 1: a file URL's host setter goes to the file host state.
    if self.Overridden() && self.IsFile() {
        return self.Back(State.FileHost)
    }
    // Step 2: a ':' outside brackets ends the host before a port.
    if c == CColon && !self.brackets {
        if self.buf == self.p {
            fail self.Bad()
        }
        if self.IsHostnameSetter() {
            fail self.Bad()
        }
        host := try ParseHost(self.a, self.Buffer(), !self.url.special)
        self.url.host = some(host)
        return self.Enter(State.Port)
    }
    // Step 3: the host ends.
    if self.EndsAuthority(c) {
        buffer := self.Buffer()
        self.p -= 1
        // Step 3.1.
        if self.url.special && buffer.Len() == 0 {
            fail self.Bad()
        }
        // Step 3.2.
        if self.Overridden() && buffer.Len() == 0 && (self.HasCredentials() || self.url.port.IsSome()) {
            fail self.Bad()
        }
        // Steps 3.3 to 3.5.
        host := try ParseHost(self.a, buffer, !self.url.special)
        self.url.host = some(host)
        _ = self.Enter(State.PathStart)
        // Step 3.6.
        if self.Overridden() {
            return Go.Stop
        }
        return Go.Next
    }
    // Step 4: the host goes on; brackets hide a ':'.
    if c == CLeftBracket {
        self.brackets = true
    }
    if c == CRightBracket {
        self.brackets = false
    }
    self.Skip(StopPath | StopHost | self.BackslashStop())
    return Go.Next
}

// PortState is the port state.
Parser.PortState(mut self, c: int): Error!Go {
    // Step 1: take every digit.
    if c >= 0 && IsDigit(Trunc[u8](c)) {
        for self.p + 1 < self.text.Len() && IsDigit(self.text[self.p + 1]) {
            self.p += 1
        }
        return Go.Next
    }
    // Step 2: the port ends.
    if self.EndsAuthority(c) || self.Overridden() {
        // Step 2.1.
        if self.buf < self.p {
            var port := 0
            for x in self.Buffer() {
                port = port * 10 + int(x - b'0')
                if port > 0xFFFF {
                    fail self.Bad()
                }
            }
            value := Trunc[u16](port)
            self.url.port = if SamePort(some(value), self.url.dport) { none } else { some(value) }
            self.buf = self.p
            if self.Overridden() {
                return Go.Stop
            }
        }
        // Step 2.2.
        if self.Overridden() {
            fail self.Bad()
        }
        // Step 2.3.
        return self.Back(State.PathStart)
    }
    // Step 3.
    fail self.Bad()
}

// IsDriveLetter is whether s is a Windows drive letter: a letter and ':'
// or '|'.
IsDriveLetter(s: []u8): bool {
    return s.Len() == 2 && IsAlpha(s[0]) && (s[1] == b':' || s[1] == b'|')
}

// IsNormalizedDriveLetter is whether s is a letter and ':'.
IsNormalizedDriveLetter(s: []u8): bool {
    return s.Len() == 2 && IsAlpha(s[0]) && s[1] == b':'
}

// StartsWithDriveLetter is whether s starts with a Windows drive letter
// that is all of s or is followed by '/', '\', '?' or '#'.
StartsWithDriveLetter(s: []u8): bool {
    if s.Len() < 2 || !IsDriveLetter(s[0..2]) {
        return false
    }
    if s.Len() == 2 {
        return true
    }
    x := s[2]
    return x == b'/' || x == b'\\' || x == b'?' || x == b'#'
}

// DriveLetters holds each letter followed by ':', so a drive letter
// normalized is a view of it.
const DriveLetters: str = "A:B:C:D:E:F:G:H:I:J:K:L:M:N:O:P:Q:R:S:T:U:V:W:X:Y:Z:a:b:c:d:e:f:g:h:i:j:k:l:m:n:o:p:q:r:s:t:u:v:w:x:y:z:"

// NormalizedDriveLetter is drive letter s with ':' second.
NormalizedDriveLetter(s: []u8): []u8 {
    l := s[0]
    i := if l >= b'a' { 52 + 2 * int(l - b'a') } else { 2 * int(l - b'A') }
    return DriveLetters.AsBytes()[i..i + 2]
}

// FileState is the file state.
Parser.FileState(mut self, c: int): OutOfMemory!Go {
    // Steps 1 and 2.
    self.SetScheme("file".AsBytes())
    self.url.host = some(HostValue.Empty)
    // Step 3.
    if c == CSlash || c == CBackslash {
        return self.Enter(State.FileSlash)
    }
    // Step 4: relative to a file base.
    if self.BaseIsFile() {
        // Step 4.1.
        self.url.host = self.base.host
        try self.CloneBasePath()
        self.url.query = self.base.query
        // Step 4.2.
        if c == CQuestion {
            self.url.query = some("".AsBytes())
            return self.Enter(State.Query)
        }
        // Step 4.3.
        if c == CHash {
            self.url.fragment = some("".AsBytes())
            return self.Enter(State.Fragment)
        }
        // Step 4.4.
        if c != Eof {
            self.url.query = none
            if !StartsWithDriveLetter(self.text[self.p..]) {
                self.Shorten()
            } else {
                self.url.path.Clear()
            }
            return self.Back(State.Path)
        }
        return Go.Next
    }
    // Step 5.
    return self.Back(State.Path)
}

// FileSlashState is the file slash state.
Parser.FileSlashState(mut self, c: int): OutOfMemory!Go {
    // Step 1.
    if c == CSlash || c == CBackslash {
        return self.Enter(State.FileHost)
    }
    // Step 2.1.
    if self.BaseIsFile() {
        self.url.host = self.base.host
        if !StartsWithDriveLetter(self.text[self.p..]) && self.base.path.Len() > 0 && IsNormalizedDriveLetter(self.base.path[0]) {
            try self.url.path.Push(self.base.path[0])
        }
    }
    // Step 2.2.
    return self.Back(State.Path)
}

// FileHostState is the file host state.
Parser.FileHostState(mut self, c: int): Error|OutOfMemory!Go {
    // Step 1: the host ends.
    if c == Eof || c == CSlash || c == CBackslash || c == CQuestion || c == CHash {
        buffer := self.Buffer()
        self.p -= 1
        // Step 1.1: a drive letter is the path's first segment; the path
        // state goes on with this buffer.
        if !self.Overridden() && IsDriveLetter(buffer) {
            self.state = State.Path
            return Go.Next
        }
        // Step 1.2.
        if buffer.Len() == 0 {
            self.url.host = some(HostValue.Empty)
            if self.Overridden() {
                return Go.Stop
            }
            return self.Enter(State.PathStart)
        }
        // Step 1.3.
        var host := try ParseHost(self.a, buffer, !self.url.special)
        if IsLocalhost(&host) {
            host = HostValue.Empty
        }
        self.url.host = some(host)
        if self.Overridden() {
            return Go.Stop
        }
        return self.Enter(State.PathStart)
    }
    // Step 2: take every byte up to the end.
    self.Skip(StopPath | StopBackslash)
    return Go.Next
}

// IsLocalhost is whether h is the domain "localhost".
IsLocalhost(h: &HostValue): bool {
    return match *h {
        .Domain(name) { SameText(name, "localhost") }
        _ { false }
    }
}

// PathStartState is the path start state.
Parser.PathStartState(mut self, c: int): OutOfMemory!Go {
    // Step 1.
    if self.url.special {
        if c != CSlash && c != CBackslash {
            return self.Back(State.Path)
        }
        return self.Enter(State.Path)
    }
    // Step 2.
    if !self.Overridden() && c == CQuestion {
        self.url.query = some("".AsBytes())
        return self.Enter(State.Query)
    }
    // Step 3.
    if !self.Overridden() && c == CHash {
        self.url.fragment = some("".AsBytes())
        return self.Enter(State.Fragment)
    }
    // Step 4.
    if c != Eof {
        if c != CSlash {
            return self.Back(State.Path)
        }
        return self.Enter(State.Path)
    }
    // Step 5.
    if self.Overridden() && self.url.host.IsNone() {
        try self.url.path.Push("".AsBytes())
    }
    return Go.Next
}

// The stop bytes of the scanning loops: a byte with one of a loop's bits
// may end its state's run.
const StopPath: u8 = 0x01
const StopPathSet: u8 = 0x02
const StopBackslash: u8 = 0x04
const StopAt: u8 = 0x08
const StopHost: u8 = 0x10
const StopHash: u8 = 0x20
const StopQuestion: u8 = 0x40

StopTable(): [128]u8 {
    var t: [128]u8 = [0; 128]
    t[0x2F] = StopPath | StopPathSet
    t[0x3F] = StopPath | StopQuestion
    t[0x23] = StopPath | StopHash
    t[0x5C] = StopBackslash
    t[0x40] = StopAt
    t[0x3A] = StopHost
    t[0x5B] = StopHost
    t[0x5D] = StopHost
    return t
}

// Parser.Skip moves the pointer to the last byte before the next byte
// with one of the stop bits, or before the end: the run a state takes in
// one step.
Parser.Skip(mut self, stop: u8) {
    text := self.text
    var p := self.p + 1
    for p < text.Len() && (text[p] >= 0x80 || Stops[int(text[p])] & stop == 0) {
        p += 1
    }
    self.p = p - 1
}

// Parser.BackslashStop is StopBackslash when the URL is special, else 0.
Parser.BackslashStop(&self): u8 {
    return if self.url.special { StopBackslash } else { 0 }
}

const Stops: [128]u8 = StopTable()

// Parser.EndsSegment is whether c ends a path segment (path state, step 1).
Parser.EndsSegment(&self, c: int): bool {
    if c == Eof || c == CSlash || self.url.special && c == CBackslash {
        return true
    }
    return !self.Overridden() && (c == CQuestion || c == CHash)
}

// IsSingleDot is whether s is "." or "%2e" in any case.
IsSingleDot(s: []u8): bool {
    return s.Len() == 1 && s[0] == b'.' || s.Len() == 3 && IsDotEscape(s, 0)
}

// IsDoubleDot is whether s is "..", ".%2e", "%2e." or "%2e%2e" in any case.
IsDoubleDot(s: []u8): bool {
    return match s.Len() {
        2 { s[0] == b'.' && s[1] == b'.' }
        4 { s[0] == b'.' && IsDotEscape(s, 1) || IsDotEscape(s, 0) && s[3] == b'.' }
        6 { IsDotEscape(s, 0) && IsDotEscape(s, 3) }
        _ { false }
    }
}

// IsDotEscape is whether s[i..i + 3] is "%2e" in any case.
IsDotEscape(s: []u8, i: int): bool {
    return s[i] == b'%' && s[i + 1] == b'2' && s[i + 2] | 0x20 == b'e'
}

// PathState is the path state.
Parser.PathState(mut self, c: int): OutOfMemory!Go {
    // Steps 1 and 2 over a run of segments that need nothing done, in one
    // step (PathRun), at a segment's start.
    if self.buf == self.p && self.p > self.no_run && !self.Overridden() && !self.url.file && self.url.host.IsSome() && self.PathRun() {
        end := if self.p < self.text.Len() { int(self.text[self.p]) } else { Eof }
        self.buf = self.p + 1
        // Step 1.6.
        if end == CQuestion {
            self.url.query = some("".AsBytes())
            return self.Enter(State.Query)
        }
        // Step 1.7.
        if end == CHash {
            self.url.fragment = some("".AsBytes())
            return self.Enter(State.Fragment)
        }
        return Go.Next
    }
    // Step 1: the segment ends.
    if self.EndsSegment(c) {
        buffer := self.Buffer()
        slash := c == CSlash || self.url.special && c == CBackslash
        if IsDoubleDot(buffer) {
            // Step 1.2.
            self.Shorten()
            if !slash {
                try self.url.path.Push("".AsBytes())
            }
        } else if IsSingleDot(buffer) {
            // Step 1.3.
            if !slash {
                try self.url.path.Push("".AsBytes())
            }
        } else {
            // Step 1.4.
            var segment := try Encode(self.a, buffer, InPath)
            if self.IsFile() && self.url.path.Len() == 0 && IsDriveLetter(segment) {
                segment = NormalizedDriveLetter(segment)
            }
            try self.url.path.Push(segment)
        }
        // Step 1.5.
        self.buf = self.p + 1
        // Step 1.6.
        if c == CQuestion {
            self.url.query = some("".AsBytes())
            return self.Enter(State.Query)
        }
        // Step 1.7.
        if c == CHash {
            self.url.fragment = some("".AsBytes())
            return self.Enter(State.Fragment)
        }
        return Go.Next
    }
    // Step 2: the segment goes on; take every byte up to its end.
    base := if self.Overridden() { StopPathSet } else { StopPath }
    self.Skip(base | self.BackslashStop())
    return Go.Next
}

// The kinds of byte PathRun tells apart.
const PathPlain: u8 = 0
const PathEnd: u8 = 1
const PathSlash: u8 = 2
const PathDot: u8 = 3
const PathRefuse: u8 = 4

// PathTable is each byte's kind for PathRun: '?' and '#' end the path, '/'
// a segment, '.' and '%' may make a dot segment, and a byte the path set
// encodes, or '\', which a special URL reads as '/', refuses the run.
PathTable(): [256]u8 {
    var t: [256]u8 = [0; 256]
    for i in 0..256 {
        c := Trunc[u8](i)
        if Encodes(c, InPath) || c == b'\\' {
            t[i] = PathRefuse
        }
    }
    t[0x3F] = PathEnd
    t[0x23] = PathEnd
    t[0x2F] = PathSlash
    t[0x2E] = PathDot
    t[0x25] = PathDot
    return t
}

const PathBytes: [256]u8 = PathTable()

// IsDotSegment is whether s is a single-dot or double-dot segment.
IsDotSegment(s: []u8): bool {
    return IsSingleDot(s) || IsDoubleDot(s)
}

// Parser.PathRun is what the path state does from the pointer, at a
// segment's start, to the next '?' or '#' or the end, when no segment on the
// way is a dot segment, holds a byte the path set encodes, or holds a '\':
// step 1.4 appends each segment as it is, so the run is the path's last
// segments, kept as one span (Record.tail). Then the pointer is at the byte
// that ended the run, and the result true; else nothing changes and the
// result is false, and no run starts again before the pointer passes the
// byte that refused it. A file URL (step 1.4's drive letter) and a URL with no
// host (the serializer's "/." step reads the segments) do not come here.
// O(n); allocates nothing.
Parser.PathRun(mut self): bool {
    end := PathRunEnd(self.text, self.p)
    if end < 0 {
        self.no_run = -1 - end
        return false
    }
    self.url.tail = some(self.text[self.p..end])
    self.p = end
    return true
}

// OpaquePathState is the opaque path state. The path is the input from
// the state's start, encoded with the C0 control set, except that a space
// right before a '?' or '#' is "%20" (step 3.2).
Parser.OpaquePathState(mut self, c: int): OutOfMemory!Go {
    // Steps 1 and 2, and EOF: the path ends.
    if c == CQuestion || c == CHash || c == Eof {
        buffer := self.Buffer()
        n := buffer.Len()
        if c != Eof && n > 0 && buffer[n - 1] == b' ' {
            self.url.opath = try EncodeTrailingSpace(self.a, buffer[0..n - 1])
        } else {
            self.url.opath = try Encode(self.a, buffer, InC0)
        }
        if c == CQuestion {
            self.url.query = some("".AsBytes())
            return self.Enter(State.Query)
        }
        if c == CHash {
            self.url.fragment = some("".AsBytes())
            return self.Enter(State.Fragment)
        }
        return Go.Next
    }
    // Steps 3 and 4: take every byte up to the end.
    self.Skip(StopQuestion | StopHash)
    return Go.Next
}

// EncodeTrailingSpace is s encoded with the C0 control set and "%20"
// after it, in a. O(n); allocates at most 3n + 3 bytes.
EncodeTrailingSpace(a: &Arena, s: []u8): OutOfMemory![]u8 from a {
    head := try Encode(a, s, InC0)
    out := try a.Slice[u8](head.Len() + 3)
    (mut out[0..head.Len()]).CopyFrom(head)
    n := head.Len()
    out[n] = b'%'
    out[n + 1] = b'2'
    out[n + 2] = b'0'
    return out[..]
}

// QueryState is the query state.
Parser.QueryState(mut self, c: int): OutOfMemory!Go {
    // Step 2: the query ends.
    if !self.Overridden() && c == CHash || c == Eof {
        bit := if self.url.special { InSpecialQuery } else { InQuery }
        self.url.query = some(try Encode(self.a, self.Buffer(), bit))
        if c == CHash {
            self.url.fragment = some("".AsBytes())
            return self.Enter(State.Fragment)
        }
        return Go.Next
    }
    // Step 3: take every byte up to the end.
    stop := if self.Overridden() { 0 } else { StopHash }
    self.Skip(stop)
    return Go.Next
}

// FragmentState is the fragment state: the rest of the input, encoded
// with the fragment set.
Parser.FragmentState(mut self): OutOfMemory!Go {
    self.p = self.text.Len()
    self.url.fragment = some(try Encode(self.a, self.text[self.buf..], InFragment))
    return Go.Next
}
